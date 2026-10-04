// Code search: text search and ranking used by the semantic tools, and the
// code intelligence ports the language-server runtime implements (the lsp
// tool and the edit tools' diagnostics go through them).
package tool

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

// bm25.go — BM25 ranking over cached original outputs, a port of Boost's
// retrieve --query behavior: when a filter hid something the agent needs, it
// can search inside the stored original instead of pulling the whole blob
// back into context (which would defeat the savings).

var bmTokenSplit = regexp.MustCompile(`[^A-Za-z0-9_]+`)

func tokenize(s string) []string {
	parts := bmTokenSplit.Split(strings.ToLower(s), -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ScoredLine is one line of a cached output ranked by BM25 relevance.
type ScoredLine struct {
	// LineNo is 1-indexed within the original output.
	LineNo int
	Line   string
	Score  float64
}

const (
	bm25K1 = 1.2
	bm25B  = 0.75
	// contextLines around each hit keep stack frames / stack traces coherent.
	bm25Context = 1
)

// BM25Rank scores each line of original against query and returns the top-k
// hits (k <= 0 means all hits) with surrounding context lines. Lines are
// returned in document order and deduplicated.
func BM25Rank(query, original string, topK int) []ScoredLine {
	qTokens := tokenize(query)
	if len(qTokens) == 0 || strings.TrimSpace(original) == "" {
		return nil
	}
	lines := strings.Split(original, "\n")
	n := len(lines)
	if n == 0 {
		return nil
	}
	// document frequency per query token
	df := map[string]int{}
	lineTokens := make([][]string, n)
	totalLen := 0
	for i, line := range lines {
		toks := tokenize(line)
		lineTokens[i] = toks
		totalLen += len(toks)
		seen := map[string]bool{}
		for _, t := range toks {
			if !seen[t] {
				seen[t] = true
				df[t]++
			}
		}
	}
	avgdl := float64(totalLen) / float64(n)
	if avgdl == 0 {
		avgdl = 1
	}
	idf := map[string]float64{}
	for _, t := range qTokens {
		idf[t] = math.Log(1 + (float64(n)-float64(df[t])+0.5)/(float64(df[t])+0.5))
	}
	type scored struct {
		idx   int
		score float64
	}
	var hits []scored
	for i, toks := range lineTokens {
		if len(toks) == 0 {
			continue
		}
		tf := map[string]int{}
		for _, t := range toks {
			tf[t]++
		}
		score := 0.0
		for _, t := range qTokens {
			f := float64(tf[t])
			if f == 0 {
				continue
			}
			score += idf[t] * (f * (bm25K1 + 1)) / (f + bm25K1*(1-bm25B+bm25B*float64(len(toks))/avgdl))
		}
		if score > 0 {
			hits = append(hits, scored{i, score})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score > hits[b].score })
	if topK > 0 && len(hits) > topK {
		hits = hits[:topK]
	}
	// expand context and re-sort by document order
	included := map[int]bool{}
	for _, h := range hits {
		for j := h.idx - bm25Context; j <= h.idx+bm25Context; j++ {
			if j >= 0 && j < n {
				included[j] = true
			}
		}
	}
	scoreByLine := map[int]float64{}
	for _, h := range hits {
		scoreByLine[h.idx] = h.score
	}
	out := make([]ScoredLine, 0, len(included))
	for idx, ok := range included {
		if !ok {
			continue
		}
		out = append(out, ScoredLine{LineNo: idx + 1, Line: lines[idx], Score: scoreByLine[idx]})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].LineNo < out[b].LineNo })
	return out
}

// SplitLineRange parses "a-b" or "a" (1-indexed inclusive) and returns the
// selected lines from original. Invalid ranges return nil, false. The end of
// the range is clamped to the available line count rather than rejected,
// since callers rarely know the exact cached length in advance and an
// over-generous upper bound shouldn't discard an otherwise valid range.
func SplitLineRange(original, spec string) ([]string, bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, false
	}
	lines := strings.Split(original, "\n")
	var a, b int
	if _, err := parseInts(spec, &a, &b); err != nil {
		return nil, false
	}
	if a < 1 || a > len(lines) || b < a {
		return nil, false
	}
	if b > len(lines) {
		b = len(lines)
	}
	return lines[a-1 : b], true
}

func parseInts(spec string, a, b *int) (int, error) {
	parts := strings.SplitN(spec, "-", 2)
	n, err := atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, err
	}
	*a = n
	*b = n
	if len(parts) == 2 {
		m, err := atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return 0, err
		}
		*b = m
	}
	return n, nil
}

func atoi(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, errBadRange
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return 0, errBadRange
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

var errBadRange = &rangeError{}

type rangeError struct{}

func (*rangeError) Error() string { return "invalid line range" }

// Filter is one declarative output-compression rule (Boost TOML schema).
// The first filter whose MatchCommand regex matches the command line wins.
type Filter struct {
	Name        string
	Version     string
	Description string
	Source      string // "builtin" | "project" | "user"

	MatchCommand      *regexp.Regexp
	MatchOutputSelect []*regexp.Regexp // selection gate: none match => passthrough
	StripANSI         bool
	Replace           []ReplaceRule
	StripLines        []*regexp.Regexp
	KeepLines         []*regexp.Regexp
	Collapse          []CollapseRule
	TruncateLinesAt   int
	HeadLines         int
	TailLines         int
	MaxLines          int
	MaxLinesKeepTail  int
	MatchOutput       []MatchOutputRule // short-circuit whole-output replacement
	OnEmpty           string

	// Semantic marks a filter backed by a Go parser rather than the regex
	// pipeline; it changes only the reported capability id.
	Semantic bool

	// Tests are the embedded example vectors shipped with Boost filters.
	Tests []FilterTest
}

type ReplaceRule struct {
	Pattern     *regexp.Regexp
	Replacement string
}

type CollapseRule struct {
	Pattern  *regexp.Regexp
	Template string // "{count}" is replaced by the run length
}

type MatchOutputRule struct {
	Pattern *regexp.Regexp
	Message string
}

type FilterTest struct {
	Name              string
	Input             string
	Expected          string
	ExpectMatchOutput *bool // nil = unspecified; false asserts the selector gate rejects
	HasExpected       bool
}

// Result describes what Apply did to one command output.
type Result struct {
	Output        string
	Matched       bool   // a filter matched the command line
	Applied       bool   // the filter actually transformed the output
	FilterName    string // capability id without prefix, e.g. "make"
	FilterVersion string
	BeforeBytes   int
	AfterBytes    int
	SavedTokens   int  // estimated tokens kept out of context
	CompressedPct int  // 0..100
	SelectionMiss bool // selectors present but none matched (passthrough)
	Semantic      bool // handled by a Go-native parser, not the TOML pipeline
}

// Engine holds compiled filters ordered by priority: builtin first, then user
// and project filters (later files may add filters; the first matching
// MatchCommand wins at runtime, mirroring Boost).
type Engine struct {
	filters []*Filter
}

// NewEngine compiles filters from raw TOML documents. Invalid documents are
// skipped with their error collected (fail-open, never fatal).
func NewEngine(docs map[string]string, source string) (*Engine, []error) {
	e := &Engine{}
	var errs []error
	for _, name := range sortedKeys(docs) {
		f, err := CompileFilter(docs[name], source)
		if err != nil {
			errs = append(errs, fmt.Errorf("filter %s: %w", name, err))
			continue
		}
		e.filters = append(e.filters, f)
	}
	return e, errs
}

// AddFilters appends pre-compiled filters (used to layer user/project
// filters after builtins).
func (e *Engine) AddFilters(fs ...*Filter) {
	for _, f := range fs {
		if f != nil {
			e.filters = append(e.filters, f)
		}
	}
}

// Filters returns the engine's filter list (read-only intent).
func (e *Engine) Filters() []*Filter { return e.filters }

// Match returns the first filter whose MatchCommand matches cmd, or nil.
func (e *Engine) Match(cmd string) *Filter {
	if e == nil {
		return nil
	}
	cmd = strings.TrimSpace(cmd)
	for _, f := range e.filters {
		if f.MatchCommand != nil && f.MatchCommand.MatchString(cmd) {
			return f
		}
	}
	return nil
}

// Apply compresses output for the given command line. It never fails: on any
// problem it returns the input unchanged (fail-open).
//
// Go-native semantic filters (semantic.go) get first refusal: when one handles
// the command and actually shrinks the output, it wins, because a structural
// parse of a diff/table/path-listing is strictly better informed than a regex
// over the same bytes. If no semantic filter applies, the TOML pipeline runs as
// before, so existing filters and their embedded test vectors are unaffected.
func (e *Engine) Apply(cmd, output string) Result {
	res := Result{Output: output, BeforeBytes: len(output)}
	if sem, handled := ApplySemantic(cmd, output); handled {
		return sem
	}
	f := e.Match(cmd)
	if f == nil {
		return res
	}
	res.Matched = true
	res.FilterName = f.Name
	res.FilterVersion = f.Version
	out, applied, selectionMiss := f.apply(output, false)
	res.Applied = applied
	res.SelectionMiss = selectionMiss
	res.Output = out
	res.AfterBytes = len(out)
	if res.BeforeBytes > res.AfterBytes {
		res.SavedTokens = EstimateTokens(res.BeforeBytes - res.AfterBytes)
		res.CompressedPct = 100 * (res.BeforeBytes - res.AfterBytes) / res.BeforeBytes
	}
	return res
}

// apply runs the filter pipeline over one output blob. Observed Boost
// semantics (verified against the v0.10.5 binary and its embedded test
// vectors):
//   - empty input short-circuits to on_empty (no selection gate to protect)
//   - replace runs BEFORE strip/keep: strip patterns frequently match the
//     post-replacement text (e.g. valgrind strips the banner only after the
//     ==PID== prefix has been replaced away)
//   - a selection miss passes the output through untouched (bypassGate=false);
//     the embedded [[tests]] vectors exercise the pipeline with the gate
//     bypassed and test the gate explicitly via expect_match_output
//   - the final output has trailing newlines trimmed
//   - if the pipeline empties the output, on_empty fills it
func (f *Filter) apply(output string, bypassGate bool) (out string, applied bool, selectionMiss bool) {
	out = output
	if f.StripANSI {
		out = StripANSI(out)
	}
	// Whole-output short-circuit rules (e.g. "eslint: 0 problems" -> "eslint: ok").
	for _, rule := range f.MatchOutput {
		if rule.Pattern != nil && rule.Pattern.MatchString(out) {
			return strings.TrimRight(rule.Message, "\n"), true, false
		}
	}
	if strings.TrimSpace(out) == "" {
		if f.OnEmpty != "" {
			return f.OnEmpty, true, false
		}
		return "", out != output, false
	}
	// Selection gate: when selectors exist, at least one must match the output,
	// otherwise we refuse to touch it (prevents cross-tool over-compression).
	if !bypassGate && len(f.MatchOutputSelect) > 0 {
		selected := false
		for _, re := range f.MatchOutputSelect {
			if re != nil && re.MatchString(out) {
				selected = true
				break
			}
		}
		if !selected {
			return output, false, true
		}
	}
	for _, rule := range f.Replace {
		out = applyReplacePerLine(out, rule)
	}
	if len(f.KeepLines) > 0 {
		out = keepLines(out, f.KeepLines)
	}
	if len(f.StripLines) > 0 {
		out = stripLines(out, f.StripLines)
	}
	for _, rule := range f.Collapse {
		out = collapseRuns(out, rule)
	}
	if f.TruncateLinesAt > 0 {
		out = truncateLines(out, f.TruncateLinesAt)
	}
	if f.HeadLines > 0 || f.TailLines > 0 {
		out = headTail(out, f.HeadLines, f.TailLines)
	}
	if f.MaxLines > 0 {
		out = capLines(out, f.MaxLines, f.MaxLinesKeepTail)
	}
	out = strings.TrimRight(out, "\n")
	if strings.TrimSpace(out) == "" && f.OnEmpty != "" {
		out = f.OnEmpty
	}
	applied = out != output
	return out, applied, false
}

func keepLines(s string, res []*regexp.Regexp) string {
	lines := strings.Split(s, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		for _, re := range res {
			if re != nil && re.MatchString(line) {
				kept = append(kept, line)
				break
			}
		}
	}
	return strings.Join(kept, "\n")
}

func stripLines(s string, res []*regexp.Regexp) string {
	lines := strings.Split(s, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		drop := false
		for _, re := range res {
			if re != nil && re.MatchString(line) {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// collapseRuns replaces each maximal run of lines matching rule.Pattern with a
// single template line ("{count}" substituted by the run length). Single-line
// runs collapse too (e.g. one `"Action":"pass"` NDJSON line -> "1 passed"),
// matching Boost's go-test-json behavior.
func collapseRuns(s string, rule CollapseRule) string {
	if rule.Pattern == nil {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if rule.Pattern.MatchString(lines[i]) {
			j := i
			for j < len(lines) && rule.Pattern.MatchString(lines[j]) {
				j++
			}
			out = append(out, strings.ReplaceAll(rule.Template, "{count}", strconv.Itoa(j-i)))
			i = j - 1
			continue
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n")
}

func truncateLines(s string, max int) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if len(line) > max {
			// Tool output is the one source that genuinely carries raw bytes
			// (a grep over a binary file, a truncated read), so this is where
			// repairing invalid UTF-8 earns its keep.
			lines[i] = llm.TruncateBytes(line, max, "…")
		}
	}
	return strings.Join(lines, "\n")
}

// applyReplacePerLine applies a replace rule to each line independently. Boost
// replace patterns routinely use line anchors (`^…`, `…$`) — e.g. trimming a
// timestamp prefix or trailing tabs — which only make sense per line. Applying
// them to the whole blob would anchor `^`/`$` to the entire string and miss
// every interior line.
func applyReplacePerLine(s string, rule ReplaceRule) string {
	if rule.Pattern == nil {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = rule.Pattern.ReplaceAllString(line, rule.Replacement)
	}
	return strings.Join(lines, "\n")
}

func headTail(s string, head, tail int) string {
	lines := strings.Split(s, "\n")
	if head > 0 && tail > 0 && len(lines) > head+tail {
		kept := make([]string, 0, head+tail+1)
		kept = append(kept, lines[:head]...)
		kept = append(kept, fmt.Sprintf("... +%d more lines", len(lines)-head-tail))
		kept = append(kept, lines[len(lines)-tail:]...)
		return strings.Join(kept, "\n")
	}
	if head > 0 && len(lines) > head {
		kept := append(append([]string{}, lines[:head]...), fmt.Sprintf("... +%d more lines", len(lines)-head))
		return strings.Join(kept, "\n")
	}
	if tail > 0 && len(lines) > tail {
		kept := append([]string{fmt.Sprintf("... +%d more lines", len(lines)-tail)}, lines[len(lines)-tail:]...)
		return strings.Join(kept, "\n")
	}
	return s
}

// capLines enforces the total line budget, preserving the tail window so the
// final summary/status lines survive (Boost's Never-Block max_lines behavior).
func capLines(s string, max, keepTail int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= max {
		return s
	}
	if keepTail < 0 {
		keepTail = 0
	}
	if keepTail > max {
		keepTail = max
	}
	headCount := max - keepTail
	kept := make([]string, 0, max+1)
	kept = append(kept, lines[:headCount]...)
	dropped := len(lines) - max
	kept = append(kept, fmt.Sprintf("... +%d more lines", dropped))
	if keepTail > 0 {
		kept = append(kept, lines[len(lines)-keepTail:]...)
	}
	return strings.Join(kept, "\n")
}

// CompileFilter parses and compiles one Boost-style TOML filter document.
func CompileFilter(doc, source string) (*Filter, error) {
	raw, err := parseFilterTOML(doc)
	if err != nil {
		return nil, err
	}
	f := &Filter{Name: raw.Name, Source: source}
	g := raw.Fields
	f.Description = toStr(g["description"])
	f.Version = toStr(g["version"])
	f.StripANSI = toBool(g["strip_ansi"])
	f.TruncateLinesAt = toInt(g["truncate_lines_at"])
	f.HeadLines = toInt(g["head_lines"])
	f.TailLines = toInt(g["tail_lines"])
	f.MaxLines = toInt(g["max_lines"])
	f.MaxLinesKeepTail = toInt(g["max_lines_keep_tail"])
	f.OnEmpty = toStr(g["on_empty"])

	if mc := toStr(g["match_command"]); strings.TrimSpace(mc) != "" {
		re, err := regexp.Compile(mc)
		if err != nil {
			return nil, fmt.Errorf("match_command: %w", err)
		}
		f.MatchCommand = re
	} else {
		return nil, fmt.Errorf("match_command is required")
	}
	if f.MatchOutputSelect, err = compileRegexList(g["match_output_select"]); err != nil {
		return nil, fmt.Errorf("match_output_select: %w", err)
	}
	if f.StripLines, err = compileRegexList(g["strip_lines_matching"]); err != nil {
		return nil, fmt.Errorf("strip_lines_matching: %w", err)
	}
	if f.KeepLines, err = compileRegexList(g["keep_lines_matching"]); err != nil {
		return nil, fmt.Errorf("keep_lines_matching: %w", err)
	}
	if f.Replace, err = compileReplaceRules(g["replace"]); err != nil {
		return nil, fmt.Errorf("replace: %w", err)
	}
	if f.Collapse, err = compileCollapseRules(g["collapse_lines_matching"]); err != nil {
		return nil, fmt.Errorf("collapse_lines_matching: %w", err)
	}
	if f.MatchOutput, err = compileMatchOutputRules(g["match_output"]); err != nil {
		return nil, fmt.Errorf("match_output: %w", err)
	}
	for _, rt := range raw.Tests {
		t := FilterTest{
			Name:     toStr(rt.Fields["name"]),
			Input:    toStr(rt.Fields["input"]),
			Expected: toStr(rt.Fields["expected"]),
		}
		if v, ok := rt.Fields["expected"]; ok {
			_ = v
			t.HasExpected = true
		}
		if b, ok := rt.Fields["expect_match_output"].(bool); ok {
			t.ExpectMatchOutput = &b
		}
		f.Tests = append(f.Tests, t)
	}
	return f, nil
}

func compileRegexList(v tomlValue) ([]*regexp.Regexp, error) {
	items, ok := v.([]tomlValue)
	if !ok {
		return nil, nil
	}
	out := make([]*regexp.Regexp, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok || strings.TrimSpace(s) == "" {
			continue
		}
		re, err := regexp.Compile(s)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", s, err)
		}
		out = append(out, re)
	}
	return out, nil
}

func compileReplaceRules(v tomlValue) ([]ReplaceRule, error) {
	items, ok := v.([]tomlValue)
	if !ok {
		return nil, nil
	}
	var out []ReplaceRule
	for _, item := range items {
		m, ok := item.(map[string]tomlValue)
		if !ok {
			continue
		}
		pat := toStr(m["pattern"])
		if pat == "" {
			continue
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", pat, err)
		}
		out = append(out, ReplaceRule{Pattern: re, Replacement: toStr(m["replacement"])})
	}
	return out, nil
}

func compileCollapseRules(v tomlValue) ([]CollapseRule, error) {
	items, ok := v.([]tomlValue)
	if !ok {
		return nil, nil
	}
	var out []CollapseRule
	for _, item := range items {
		switch t := item.(type) {
		case map[string]tomlValue:
			pat := toStr(t["pattern"])
			if pat == "" {
				continue
			}
			re, err := regexp.Compile(pat)
			if err != nil {
				return nil, fmt.Errorf("pattern %q: %w", pat, err)
			}
			tmpl := toStr(t["template"])
			if tmpl == "" {
				tmpl = "{count} lines"
			}
			out = append(out, CollapseRule{Pattern: re, Template: tmpl})
		case string:
			re, err := regexp.Compile(t)
			if err != nil {
				return nil, fmt.Errorf("pattern %q: %w", t, err)
			}
			out = append(out, CollapseRule{Pattern: re, Template: "{count} lines"})
		}
	}
	return out, nil
}

func compileMatchOutputRules(v tomlValue) ([]MatchOutputRule, error) {
	items, ok := v.([]tomlValue)
	if !ok {
		return nil, nil
	}
	var out []MatchOutputRule
	for _, item := range items {
		m, ok := item.(map[string]tomlValue)
		if !ok {
			continue
		}
		pat := toStr(m["pattern"])
		if pat == "" {
			continue
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", pat, err)
		}
		out = append(out, MatchOutputRule{Pattern: re, Message: toStr(m["message"])})
	}
	return out, nil
}

func toStr(v tomlValue) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func toBool(v tomlValue) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func toInt(v tomlValue) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Operations of the lsp tool, in the order its schema lists them.
const (
	LSPOpDefinition       = "definition"
	LSPOpDeclaration      = "declaration"
	LSPOpTypeDefinition   = "type_definition"
	LSPOpImplementation   = "implementation"
	LSPOpReferences       = "references"
	LSPOpHover            = "hover"
	LSPOpDocumentSymbols  = "document_symbols"
	LSPOpWorkspaceSymbols = "workspace_symbols"
	LSPOpIncomingCalls    = "incoming_calls"
	LSPOpOutgoingCalls    = "outgoing_calls"
	LSPOpSupertypes       = "supertypes"
	LSPOpSubtypes         = "subtypes"
	LSPOpDiagnostics      = "diagnostics"
)

// LSPOperations lists every operation in schema order.
var LSPOperations = []string{
	LSPOpDefinition, LSPOpDeclaration, LSPOpTypeDefinition, LSPOpImplementation,
	LSPOpReferences, LSPOpHover, LSPOpDocumentSymbols, LSPOpWorkspaceSymbols,
	LSPOpIncomingCalls, LSPOpOutgoingCalls, LSPOpSupertypes, LSPOpSubtypes,
	LSPOpDiagnostics,
}

// lspToolDescription is appendix B.1 verbatim. It is part of the prompt
// prefix: never interpolate anything into it.
const lspToolDescription = "Look up code through the project's language servers: definitions, declarations, type definitions, implementations, references, hover type information, document and workspace symbols, call and type hierarchies, and current diagnostics. Read-only. Lines are 1-based and numbered the way file reads show them. Pass symbol (a name on that line) instead of column when you are not sure of the exact column. Prefer this over text search when you need where a symbol is defined or used."

type LSPInput struct {
	Operation          string `json:"operation" jsonschema:"enum=definition,enum=declaration,enum=type_definition,enum=implementation,enum=references,enum=hover,enum=document_symbols,enum=workspace_symbols,enum=incoming_calls,enum=outgoing_calls,enum=supertypes,enum=subtypes,enum=diagnostics" jsonschema_description:"What to look up."`
	FilePath           string `json:"file_path,omitempty" jsonschema_description:"Absolute or workspace-relative file path. Required for every operation except workspace_symbols."`
	Line               int    `json:"line,omitempty" jsonschema:"minimum=1" jsonschema_description:"1-based line number, as shown by file reads."`
	Column             int    `json:"column,omitempty" jsonschema:"minimum=1" jsonschema_description:"1-based column in characters. Omit when symbol is given."`
	Symbol             string `json:"symbol,omitempty" jsonschema_description:"A name on the given line to position on, used instead of column."`
	Query              string `json:"query,omitempty" jsonschema_description:"Symbol name or prefix to search for. Required for workspace_symbols."`
	IncludeDeclaration bool   `json:"include_declaration,omitempty" jsonschema_description:"For references: also return the declaration itself."`
	MaxResults         int    `json:"max_results,omitempty" jsonschema:"minimum=1,maximum=200" jsonschema_description:"Maximum locations to return (default 50, at most 200)."`
}

// NewLSPTool builds the lsp tool over rt.CodeIntel.
func NewLSPTool(st *State, rt *AgentToolRuntime) (*llm.Tool, error) {
	return llm.NewTool(
		"lsp",
		lspToolDescription,
		func(ctx context.Context, in *LSPInput) (string, error) {
			if rt == nil || rt.CodeIntel == nil {
				return "", fmt.Errorf("language servers are not available in this session")
			}
			if in == nil {
				in = &LSPInput{}
			}
			operation := strings.ToLower(strings.TrimSpace(in.Operation))
			abs := ""
			if strings.TrimSpace(in.FilePath) != "" {
				resolution, err := authorizeRead(ctx, st, "lsp", in.FilePath)
				if err != nil {
					return "", err
				}
				abs = resolution.Abs
			}
			maxResults := in.MaxResults
			if maxResults <= 0 {
				maxResults = 50
			}
			if maxResults > 200 {
				maxResults = 200
			}
			// A result location outside the readable roots carries only its
			// path and position, never a source preview (spec §9.4): previews
			// are for files this session may read without asking.
			readRoots := MergeAllowedRootPaths(st.AllowedRoots(), st.PermissionRoots(safety.FileSystemAccessRead))
			previewAllowed := func(p string) bool {
				if _, err := ResolveWithinRoots(p, readRoots); err != nil {
					return false
				}
				return !st.ReadPathDenied(p) && st.ProtectedReadReason(p) == ""
			}
			res, err := rt.CodeIntel.Query(ctx, CodeIntelQuery{
				Operation:          operation,
				AbsPath:            abs,
				DisplayPath:        in.FilePath,
				Line:               in.Line,
				Column:             in.Column,
				Symbol:             strings.TrimSpace(in.Symbol),
				Query:              strings.TrimSpace(in.Query),
				IncludeDeclaration: in.IncludeDeclaration,
				MaxResults:         maxResults,
				PreviewAllowed:     previewAllowed,
			})
			if err != nil {
				CaptureToolError(ctx, err)
				return "", err
			}
			display := res.Display
			if display == nil {
				display = map[string]any{}
			}
			display["operation"] = operation
			display["file_path"] = in.FilePath
			if in.Line > 0 {
				display["line"] = in.Line
			}
			if symbol := strings.TrimSpace(in.Symbol); symbol != "" {
				display["symbol"] = symbol
			}
			// The card body is the text the model receives; a captured map
			// replaces the transport fallback that would otherwise carry it.
			display["output"] = res.Text
			CaptureToolOutput(ctx, display)
			return res.Text, nil
		},
	)
}

// CodeIntelQuery is one lsp tool call after the tool validated and authorized it.
type CodeIntelQuery struct {
	Operation          string
	AbsPath            string // resolved and read-authorized; empty when the operation allows it
	DisplayPath        string // what the model passed, for messages
	Line               int    // 1-based; 0 when not applicable
	Column             int    // 1-based characters; 0 when Symbol is used or not applicable
	Symbol             string
	Query              string
	IncludeDeclaration bool
	MaxResults         int // already defaulted (50) and capped (200) by the tool
	// PreviewAllowed reports whether a result location may carry a source
	// preview: true only for paths the session may read without asking.
	PreviewAllowed func(absPath string) bool
}

// CodeIntelResult is the text the model receives plus the fields the
// surfaces render.
type CodeIntelResult struct {
	Text    string
	Display map[string]any
}

// FileChange is one file an edit tool wrote. Before is nil for a new file;
// After is nil for a deleted file.
type FileChange struct {
	AbsPath string
	Before  []byte
	After   []byte
}

// DiagnosticsDelta is what an edit reports: Text is the <diagnostics> block
// appended to the tool result ("" when there is nothing to say).
type DiagnosticsDelta struct {
	Text    string
	Summary event.LSPDiagnosticsSummary
}

// Empty reports whether the edit has nothing to report.
func (d DiagnosticsDelta) Empty() bool { return d.Text == "" }

// CodeIntelligence is what the tools use. Every method must be safe for
// concurrent use and must never start a server on the Runner.Load path.
type CodeIntelligence interface {
	// Handles reports, without starting anything, whether an enabled server
	// covers absPath in this project.
	Handles(absPath string) bool
	// Query runs one lsp tool operation, starting the server if needed.
	Query(ctx context.Context, q CodeIntelQuery) (CodeIntelResult, error)
	// DidWrite syncs files an edit tool just wrote and waits, at most the
	// diagnostics wait window, for the problems the edit introduced. It never
	// fails the edit: problems are logged and reported as an empty delta.
	// agentSessionID (llm.AgentSessionIDFromContext at the call site) keys the
	// late-diagnostics queue that PeekLate reads.
	DidWrite(ctx context.Context, agentSessionID string, changes []FileChange) DiagnosticsDelta
	// DidRead opens absPath on a server that is already running, as a
	// diagnostics baseline. It never starts a server.
	DidRead(ctx context.Context, absPath string, content []byte)
	// DidRunShell tells the runtime a shell command finished, so files changed
	// outside the edit tools are re-synced. It returns immediately.
	DidRunShell(ctx context.Context)
	// PeekLate returns the late-diagnostics reminder text pending for one agent
	// session ("" when none) and a token for AckLate.
	PeekLate(agentSessionID string) (text string, token uint64)
	// AckLate marks everything up to token as delivered.
	AckLate(agentSessionID string, token uint64)
}

// CodeIntelControl is what the surfaces use (/lsp, recommendations, web API).
type CodeIntelControl interface {
	Snapshot() event.LSPSnapshot
	// Subscribe calls fn whenever the snapshot changes; cancel stops it.
	Subscribe(fn func(event.LSPSnapshot)) (cancel func())
	SetEnabled(serverID string, enabled bool) error
	Restart(serverID string) error
	// Install runs the server's install recipe; progress receives output lines.
	Install(ctx context.Context, serverID string, progress func(line string)) error
	// SetRecommendationListener installs the callback that publishes a
	// recommendation to the session named by ctx. Nil removes it.
	SetRecommendationListener(fn func(ctx context.Context, rec event.LSPRecommendation))
	// DecideRecommendation applies the user's answer. A recommendation the
	// runtime did not make, or one already answered, returns
	// ErrUnknownLSPRecommendation.
	DecideRecommendation(recommendationID string, choice event.LSPRecommendationChoice) error
	ResetRecommendations() error
}

// ErrUnknownLSPRecommendation is DecideRecommendation's answer for an id it
// does not know. It lives beside the port so surfaces can test for it without
// importing the runtime.
var ErrUnknownLSPRecommendation = errors.New("unknown or already answered language server recommendation")
