// Shell output handling: streaming, compression, head/tail, and stats.
package tool

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

const (
	liveOutputFlushInterval = 75 * time.Millisecond
	liveOutputPendingBytes  = 32 * 1024
)

// shellOutputEmitter coalesces process-pipe chunks before they reach the UI.
// Lifecycle events remain lossless; live output is a replaceable preview and
// must not create an unbounded notification backlog.
type shellOutputEmitter struct {
	mu      sync.Mutex
	pending map[safety.OutputStream][]byte
	omitted map[safety.OutputStream]int64
	emit    func(safety.OutputStream, []byte)
	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newShellOutputEmitter(emit func(safety.OutputStream, []byte)) *shellOutputEmitter {
	if emit == nil {
		return nil
	}
	e := &shellOutputEmitter{
		pending: map[safety.OutputStream][]byte{}, omitted: map[safety.OutputStream]int64{},
		emit: emit, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go e.run()
	return e
}

func (e *shellOutputEmitter) Push(stream safety.OutputStream, chunk []byte) {
	if e == nil || len(chunk) == 0 {
		return
	}
	e.mu.Lock()
	buf := append(e.pending[stream], chunk...)
	if len(buf) > liveOutputPendingBytes {
		dropped := len(buf) - liveOutputPendingBytes
		e.omitted[stream] += int64(dropped)
		copy(buf, buf[dropped:])
		buf = buf[:liveOutputPendingBytes]
	}
	e.pending[stream] = buf
	e.mu.Unlock()
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *shellOutputEmitter) run() {
	defer close(e.done)
	ticker := time.NewTicker(liveOutputFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-e.stop:
			e.flush()
			return
		case <-ticker.C:
			e.flush()
		case <-e.wake:
			// Wake only records that output exists. The ticker sets the repaint
			// rate and keeps a fast producer from dominating the main loop.
		}
	}
}

func (e *shellOutputEmitter) flush() {
	if e == nil {
		return
	}
	e.mu.Lock()
	pending := e.pending
	omitted := e.omitted
	e.pending = map[safety.OutputStream][]byte{}
	e.omitted = map[safety.OutputStream]int64{}
	e.mu.Unlock()
	for _, stream := range []safety.OutputStream{safety.OutputStreamStdout, safety.OutputStreamStderr} {
		chunk := pending[stream]
		if len(chunk) == 0 && omitted[stream] == 0 {
			continue
		}
		if omitted[stream] > 0 {
			prefix := []byte(fmt.Sprintf("[live output coalesced: %d bytes omitted]\n", omitted[stream]))
			chunk = append(prefix, chunk...)
		}
		e.emit(stream, chunk)
	}
}

func (e *shellOutputEmitter) Close() {
	if e == nil {
		return
	}
	e.once.Do(func() { close(e.stop) })
	select {
	case <-e.done:
	case <-time.After(time.Second):
	}
}

// shell_output_compression.go — Boost-style token savings for shell output.
//
// JFrog Boost (v0.10.5) demonstrated that wrapping the *return path* of shell
// commands with declarative, command-aware filters keeps task pass rates
// identical while cutting context-window cost (Terminal-Bench 2.0: ~13.5%
// cheaper per task). Forebrain Harness owns the tool boundary directly, so the same
// mechanism lives in-process instead of as a pipe suffix:
//
//   - match the command against compiled TOML filters (first match wins)
//   - compress only what would enter context; never rewrite the command
//   - store the redacted original locally and cite a retrieval id when the
//     reduction is significant (recoverable via the retrieve_output tool)
//   - fail open on any error: the agent always sees truthful output
//
// See docs/plan/TOKEN_OPTIMIZATION.md for the full
// reverse-engineered analysis of Boost's mechanisms.

type shellCompression struct {
	Output string
	Meta   Meta
}

func registerShellSpool(ctx context.Context, rt *AgentToolRuntime, command string, res safety.CommandResult) int64 {
	path := strings.TrimSpace(res.OutputSpoolPath)
	if rt == nil || path == "" || strings.TrimSpace(rt.StateRoot()) == "" {
		if path != "" {
			_ = os.Remove(path)
		}
		return 0
	}
	store := CompressorFor(rt.StateRoot()).Store()
	if store == nil {
		_ = os.Remove(path)
		return 0
	}
	preview := res.Stdout
	if res.Stderr != "" {
		if preview != "" {
			preview += "\n"
		}
		preview += res.Stderr
	}
	id, err := store.Save(Entry{
		Kind: KindShell, Command: command, Timestamp: time.Now(),
		FilteredOutput: preview, OriginalBytes: int(res.StdoutBytes + res.StderrBytes),
		FilteredBytes: len(preview), SessionID: llm.AgentSessionIDFromContext(ctx),
		CapabilityID: "bounded-output", CapabilityVer: "1",
		SpoolPath: path, SpoolOmittedBytes: res.OutputSpoolOmittedBytes,
	})
	if err != nil || id <= 0 {
		_ = os.Remove(path)
		return 0
	}
	go pruneShellSpools(filepath.Dir(path), path)
	return id
}

var shellSpoolPruneMu sync.Mutex

func pruneShellSpools(dir, active string) {
	shellSpoolPruneMu.Lock()
	defer shellSpoolPruneMu.Unlock()
	const (
		maxAge   = 7 * 24 * time.Hour
		maxBytes = int64(512 * 1024 * 1024)
	)
	marker := filepath.Join(dir, ".last-prune")
	if info, err := os.Stat(marker); err == nil && time.Since(info.ModTime()) < 24*time.Hour {
		return
	}
	type spoolInfo struct {
		path string
		mod  time.Time
		size int64
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var files []spoolInfo
	var total int64
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() || path == active || entry.Name() == ".last-prune" {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		if time.Since(info.ModTime()) > maxAge {
			_ = os.Remove(path)
			continue
		}
		total += info.Size()
		files = append(files, spoolInfo{path: path, mod: info.ModTime(), size: info.Size()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, file := range files {
		if total <= maxBytes {
			break
		}
		if os.Remove(file.path) == nil {
			total -= file.size
		}
	}
	if f, createErr := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); createErr == nil {
		_ = f.Close()
	}
}

func compressShellOutput(ctx context.Context, rt *AgentToolRuntime, command, stdout string) shellCompression {
	if rt == nil || strings.TrimSpace(stdout) == "" {
		return shellCompression{}
	}
	stateRoot := strings.TrimSpace(rt.StateRoot())
	if stateRoot == "" {
		return shellCompression{}
	}
	c := CompressorFor(stateRoot)
	if c == nil {
		return shellCompression{}
	}
	out, meta := c.CompressShellOutput(command, stdout, llm.AgentSessionIDFromContext(ctx))
	if strings.TrimSpace(out) == "" && strings.TrimSpace(stdout) != "" {
		// Never collapse non-empty output to nothing; fail open.
		return shellCompression{}
	}
	return shellCompression{Output: out, Meta: meta}
}

func compressionMetaMap(m Meta) map[string]any {
	out := map[string]any{
		"applied":        m.Applied,
		"before_bytes":   m.BeforeBytes,
		"after_bytes":    m.AfterBytes,
		"saved_tokens":   m.SavedTokens,
		"compressed_pct": m.CompressedPct,
	}
	if m.Filter != "" {
		out["filter"] = m.Filter
	}
	if m.FilterVersion != "" {
		out["filter_version"] = m.FilterVersion
	}
	if m.RetrieveID > 0 {
		out["retrieve_id"] = m.RetrieveID
	}
	return out
}

// RetrieveOutputInput recovers original output that outfilter compressed.
type RetrieveOutputInput struct {
	ID     int64  `json:"id" jsonschema:"description=The retrieval id cited in a compression marker."`
	Query  string `json:"query,omitempty" jsonschema:"description=Optional BM25 search inside the cached original instead of printing all of it."`
	Lines  string `json:"lines,omitempty" jsonschema_description:"Optional 1-indexed inclusive line range, e.g. 10-40 or 25."`
	Reason string `json:"reason,omitempty" jsonschema:"description=Short note on why the original output was needed (recorded for filter-quality learning)."`
	Top    int    `json:"top,omitempty" jsonschema_description:"Max --query results (0 = all, default 20)."`
}

const retrieveOutputToolDescription = "Recover the full original output behind a compressed shell or MCP result. When a result contains a marker like [forebrain compressed ~NN% ... retrieve_output tool (id N)], call this tool with that id: query BM25-searches the cached original, lines fetches a range — both instead of re-running the command or tool."

// NewRetrieveOutputTool builds the retrieve_output tool bound to the runtime's
// state root (same store the shell compressor writes to).
func NewRetrieveOutputTool(rt *AgentToolRuntime) (*llm.Tool, error) {
	return llm.NewTool(
		"retrieve_output",
		retrieveOutputToolDescription,
		func(ctx context.Context, in *RetrieveOutputInput) (string, error) {
			if in == nil || in.ID <= 0 {
				return "", fmt.Errorf("id is required (the number cited in the compression marker)")
			}
			if rt == nil {
				return "", fmt.Errorf("runtime unavailable")
			}
			stateRoot := strings.TrimSpace(rt.StateRoot())
			if stateRoot == "" {
				return "", fmt.Errorf("state root unavailable")
			}
			c := CompressorFor(stateRoot)
			store := c.Store()
			if store == nil {
				return "", fmt.Errorf("compression store unavailable")
			}
			entry, err := store.Get(in.ID)
			if err != nil {
				return "", fmt.Errorf("no compressed output with id %d (it may predate this store)", in.ID)
			}
			if reason := strings.TrimSpace(in.Reason); reason != "" {
				_ = store.RecordRetrieve(in.ID, reason)
			}
			original := entry.OriginalOutput
			switch {
			case strings.TrimSpace(in.Query) != "":
				top := in.Top
				if top <= 0 {
					top = 20
				}
				hits := BM25Rank(in.Query, original, top)
				if len(hits) == 0 {
					return fmt.Sprintf("no lines in output %d matched %q", in.ID, in.Query), nil
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "# output %d (%s) — %d matched line group(s) for %q\n", entry.ID, entry.Command, len(hits), in.Query)
				for _, h := range hits {
					fmt.Fprintf(&sb, "%d|%s\n", h.LineNo, h.Line)
				}
				return sb.String(), nil
			case strings.TrimSpace(in.Lines) != "":
				lines, ok := SplitLineRange(original, in.Lines)
				if !ok {
					return "", fmt.Errorf("invalid line range %q for output %d", in.Lines, in.ID)
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "# output %d (%s) — lines %s\n", entry.ID, entry.Command, in.Lines)
				sb.WriteString(strings.Join(lines, "\n"))
				return sb.String(), nil
			default:
				return original, nil
			}
		},
	)
}

type HeadTail struct {
	Text         string
	OmittedBytes int
	Truncated    bool
}

func TrimHeadTail(text string, maxBytes int) HeadTail {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return HeadTail{Text: text}
	}

	marker := func(omitted int) string {
		switch {
		case maxBytes >= 32:
			return fmt.Sprintf("\n...[omitted %d bytes]...\n", omitted)
		case maxBytes >= 16:
			return fmt.Sprintf("...[%dB]...", omitted)
		default:
			return "..."
		}
	}

	headEnd := 0
	tailStart := len(text)
	currentMarker := marker(len(text))

	for i := 0; i < 4; i++ {
		available := maxBytes - len(currentMarker)
		if available <= 0 {
			available = 1
		}
		headBytes := available / 2
		tailBytes := available - headBytes
		headEnd = clampEndToRuneBoundary(text, headBytes)
		tailStart = clampStartToRuneBoundary(text, len(text)-tailBytes)
		omitted := tailStart - headEnd
		nextMarker := marker(omitted)
		if nextMarker == currentMarker {
			currentMarker = nextMarker
			break
		}
		currentMarker = nextMarker
	}

	omitted := tailStart - headEnd

	return HeadTail{
		Text:         text[:headEnd] + currentMarker + text[tailStart:],
		OmittedBytes: omitted,
		Truncated:    true,
	}
}

func clampEndToRuneBoundary(text string, idx int) int {
	if idx <= 0 {
		return 0
	}
	if idx >= len(text) {
		return len(text)
	}
	for idx > 0 && !utf8.RuneStart(text[idx]) {
		idx--
	}
	return idx
}

func clampStartToRuneBoundary(text string, idx int) int {
	if idx <= 0 {
		return 0
	}
	if idx >= len(text) {
		return len(text)
	}
	for idx < len(text) && !utf8.RuneStart(text[idx]) {
		idx++
	}
	return idx
}

// compress.go — the top-level "suffix-pipe" compression entry point used by
// the shell tool. Mirrors Boost's end-to-end behavior:
//
//  1. Match the command line against declarative filters (first match wins).
//  2. Compress only the final output on the return path (never the command).
//  3. Store the redacted original locally and append a retrieval marker when
//     the reduction is significant, so hidden detail stays one tool call away.
//  4. Fail open on any error: the agent always sees *something* truthful.
//
// Honest counting: savings are measured on exactly the bytes that would have
// entered the context window (final stdout), not on any earlier pipeline form.

// DisableEnv turns the whole compression layer off when set truthy.
const DisableEnv = "FOREBRAIN_OUTPUT_COMPRESSION"

const (
	// minCompressBytes skips compression for outputs too small to matter.
	minCompressBytes = 2048
	// significantPct / significantBytes gate the retrieval marker.
	significantPct   = 25
	significantBytes = 512
)

// Meta reports what CompressShellOutput did (for tool metadata / telemetry).
type Meta struct {
	Applied       bool   `json:"applied"`
	Filter        string `json:"filter,omitempty"`
	FilterVersion string `json:"filter_version,omitempty"`
	BeforeBytes   int    `json:"before_bytes,omitempty"`
	AfterBytes    int    `json:"after_bytes,omitempty"`
	SavedTokens   int    `json:"saved_tokens,omitempty"`
	CompressedPct int    `json:"compressed_pct,omitempty"`
	RetrieveID    int64  `json:"retrieve_id,omitempty"`
}

// Compressor couples a filter engine with the local retrieval store.
type Compressor struct {
	engine *Engine
	store  *Store
}

var (
	compressorsMu sync.Mutex
	compressors   = map[string]*Compressor{}
)

// CustomFilterRelDir is the project-level directory (relative to the state
// root) holding user-authored TOML filters, mirroring Boost's .boost/filters.
const CustomFilterRelDir = ".forebrain/filters"

// CompressorFor returns the process-wide compressor for a state root, building
// it lazily. Construction never fails hard: a nil-safe compressor is returned
// so callers can stay fail-open. Each state root gets its own engine copy so
// project filters never leak between workspaces.
func CompressorFor(stateRoot string) *Compressor {
	stateRoot = strings.TrimSpace(stateRoot)
	if stateRoot == "" {
		stateRoot = "."
	}
	compressorsMu.Lock()
	defer compressorsMu.Unlock()
	if c, ok := compressors[stateRoot]; ok {
		return c
	}
	c := &Compressor{}
	if base, _ := DefaultEngine(); base != nil {
		engine := &Engine{filters: make([]*Filter, 0, len(base.Filters())+4)}
		engine.AddFilters(base.Filters()...)
		// Layer project custom filters after builtins (first match wins, so
		// builtins take precedence; custom filters cover uncovered commands).
		if custom, err := loadDirFilters(filepath.Join(stateRoot, CustomFilterRelDir)); err == nil {
			engine.AddFilters(custom...)
		}
		c.engine = engine
	}
	if store, err := OpenStore(filepath.Join(stateRoot, "state", "outfilter", "history.db")); err == nil {
		c.store = store
	}
	compressors[stateRoot] = c
	return c
}

// ResetCompressors clears the compressor cache (tests).
func ResetCompressors() {
	compressorsMu.Lock()
	defer compressorsMu.Unlock()
	for _, c := range compressors {
		if c.store != nil {
			c.store.Close()
		}
	}
	compressors = map[string]*Compressor{}
}

func loadDirFilters(dir string) ([]*Filter, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []*Filter
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		f, err := CompileFilter(string(b), "project")
		if err != nil {
			continue // fail-open: skip bad custom filters
		}
		out = append(out, f)
	}
	return out, nil
}

// CompressShellOutput compresses stdout for command, returning the (possibly
// rewritten) output and metadata. It never returns an error; failures fall
// back to the original output.
func (c *Compressor) CompressShellOutput(command, stdout, sessionID string) (string, Meta) {
	if c == nil || c.engine == nil || stdout == "" || len(stdout) < minCompressBytes {
		return stdout, Meta{}
	}
	if envTruthy(DisableEnv) {
		return stdout, Meta{}
	}
	res := safeApply(c.engine, command, stdout)
	if !res.Matched || !res.Applied {
		return stdout, Meta{Applied: false}
	}
	if RedactEnabled() {
		res.Output = Redact(res.Output)
		res.AfterBytes = len(res.Output)
		if res.BeforeBytes > res.AfterBytes {
			res.SavedTokens = EstimateTokens(res.BeforeBytes - res.AfterBytes)
			res.CompressedPct = 100 * (res.BeforeBytes - res.AfterBytes) / res.BeforeBytes
		} else {
			res.SavedTokens = 0
			res.CompressedPct = 0
		}
	}
	meta := Meta{
		Applied:       true,
		Filter:        res.FilterName,
		FilterVersion: res.FilterVersion,
		BeforeBytes:   res.BeforeBytes,
		AfterBytes:    res.AfterBytes,
		SavedTokens:   res.SavedTokens,
		CompressedPct: res.CompressedPct,
	}
	significant := res.CompressedPct >= significantPct && (res.BeforeBytes-res.AfterBytes) >= significantBytes
	if !significant {
		return res.Output, meta
	}
	original := stdout
	if RedactEnabled() {
		original = Redact(original)
	}
	if c.store != nil {
		id, err := c.store.Save(Entry{
			Command:        command,
			OriginalOutput: original,
			FilteredOutput: res.Output,
			OriginalBytes:  res.BeforeBytes,
			FilteredBytes:  res.AfterBytes,
			SavedTokens:    res.SavedTokens,
			CapabilityID:   capabilityID(res),
			CapabilityVer:  res.FilterVersion,
			SessionID:      sessionID,
		})
		if err == nil && id > 0 {
			meta.RetrieveID = id
			res.Output = res.Output + "\n" + MarkerText(res.CompressedPct, res.SavedTokens, id)
			meta.AfterBytes = len(res.Output)
		}
	}
	return res.Output, meta
}

func capabilityID(res Result) string {
	if res.FilterName == "" {
		return ""
	}
	if res.Semantic {
		// Go-native parser rather than a TOML regex pipeline; the prefix keeps
		// retrieval stats attributable to the right mechanism.
		return "go:" + res.FilterName
	}
	return "toml:" + res.FilterName
}

// MarkerText is the recoverability citation appended to significant
// compressions (Boost: "[Boost compressed ~99%. ... boost retrieve 3]").
func MarkerText(pct, savedTokens int, id int64) string {
	return fmt.Sprintf(
		"[forebrain compressed ~%d%% (~%d tokens kept out of context). Recover the full original output with the retrieve_output tool (id %d)]",
		pct, savedTokens, id,
	)
}

// safeApply wraps Engine.Apply with panic recovery so a bad filter can never
// break the shell tool.
func safeApply(e *Engine, command, output string) (res Result) {
	defer func() {
		if r := recover(); r != nil {
			res = Result{Output: output, BeforeBytes: len(output)}
		}
	}()
	return e.Apply(command, output)
}

// Store exposes the compressor's retrieval store (may be nil).
func (c *Compressor) Store() *Store {
	if c == nil {
		return nil
	}
	return c.store
}

const (
	DefaultPageLimit = 200
)

func CountLinesBytes(raw []byte) int {
	if len(raw) == 0 {
		return 0
	}
	count := bytes.Count(raw, []byte{'\n'})
	if raw[len(raw)-1] == '\n' {
		return count
	}
	return count + 1
}

func CountLinesString(s string) int {
	if s == "" {
		return 0
	}
	count := strings.Count(s, "\n")
	if s[len(s)-1] == '\n' {
		return count
	}
	return count + 1
}

const (
	SpillThresholdBytes = 32 * 1024
	SpillDir            = "tool-outputs"
)

type OutputGovernor struct {
	spillDir       string
	maxInlineBytes int
}

type GovernedOutput struct {
	Text          string
	StoredPath    string
	OriginalBytes int
	OmittedBytes  int
	TotalLines    int
	Truncated     bool
}

func (g GovernedOutput) ApplyMetadata(meta map[string]any) map[string]any {
	if meta == nil {
		meta = map[string]any{}
	}
	if g.OriginalBytes > 0 {
		if _, ok := meta["output_bytes"]; !ok {
			meta["output_bytes"] = g.OriginalBytes
		}
	}
	if g.OmittedBytes > 0 {
		meta["omitted_bytes"] = g.OmittedBytes
	}
	if g.TotalLines > 0 {
		meta["total_lines"] = g.TotalLines
	}
	if strings.TrimSpace(g.StoredPath) != "" {
		meta["full_path"] = strings.TrimSpace(g.StoredPath)
	}
	if g.Truncated {
		meta["result_truncated"] = true
	}
	return meta
}

// NewOutputGovernor writes spills straight into spillDir; callers own the
// layout and pass the leaf directory (conventionally named SpillDir).
func NewOutputGovernor(spillDir string) *OutputGovernor {
	return &OutputGovernor{spillDir: spillDir, maxInlineBytes: SpillThresholdBytes}
}

func (g *OutputGovernor) GovernDetailed(toolName, callID, output string) GovernedOutput {
	res := GovernedOutput{
		Text:          output,
		OriginalBytes: len(output),
		TotalLines:    CountLinesString(output),
	}
	limit := g.maxInlineBytes
	if limit <= 0 {
		limit = SpillThresholdBytes
	}
	if len(output) <= limit {
		return res
	}
	trimmed := TrimHeadTail(output, limit)
	res.Truncated = trimmed.Truncated
	res.OmittedBytes = trimmed.OmittedBytes
	if strings.TrimSpace(g.spillDir) == "" {
		res.Text = trimmed.Text
		return res
	}
	path, err := g.spill(toolName, callID, output)
	if err != nil {
		res.Text = trimmed.Text
		return res
	}
	res.StoredPath = path
	suffix := fmt.Sprintf("\n\n[tool output truncated for context: %d bytes total", len(output))
	if trimmed.OmittedBytes > 0 {
		suffix += fmt.Sprintf(", %d bytes omitted", trimmed.OmittedBytes)
	}
	suffix += fmt.Sprintf("; full output available via read_file: %s]", path)
	res.Text = trimmed.Text + suffix
	return res
}

// Store writes content to the spill directory and returns its path, leaving the
// inline result untouched. Tools that page their own output use this to keep the
// part they could not inline retrievable, rather than discarding it.
func (g *OutputGovernor) Store(toolName, callID, content string) (string, error) {
	return g.spill(toolName, callID, content)
}

func (g *OutputGovernor) spill(toolName, callID, content string) (string, error) {
	dir := filepath.Clean(g.spillDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ts := time.Now().UnixMilli()
	name := fmt.Sprintf("%s-%s-%d.txt", sanitize(toolName), sanitize(callID), ts)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	out := b.String()
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}
