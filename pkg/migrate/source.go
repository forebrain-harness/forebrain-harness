package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Kind names a migration source application.
type Kind string

const (
	// KindClaude is the Claude Code CLI (~/.claude).
	KindClaude Kind = "claude"
	// KindCodex is the Codex CLI (~/.codex).
	KindCodex Kind = "codex"
)

// SourceInfo is one row of the picker's source list: whether the application
// is installed, and one line describing what an import would read.
type SourceInfo struct {
	Kind        Kind
	Title       string
	Description string
	// Detected reports that the application's on-disk home exists.
	Detected bool
	// Available reports that this build can import from it.
	Available bool
	// Note explains an unavailable source in one sentence.
	Note string
}

// DetectSources lists the known sources in picker order. A source is listed
// only when its data directory exists, so a machine with nothing installed
// yields an empty list and the caller shows the no-sources message instead of
// an empty picker. claudeHome/codexHome override the default roots (~/.claude,
// $CODEX_HOME or ~/.codex); empty means "use the default".
func DetectSources(claudeHome, codexHome string) []SourceInfo {
	out := []SourceInfo{}
	if root := claudeRootAt(claudeHome); root != "" {
		out = append(out, SourceInfo{
			Kind: KindClaude, Title: "Claude Code", Detected: true, Available: true,
			Description: sourceSummary(root, claudeSourceCounts),
		})
	}
	if root := codexRootAt(codexHome); root != "" {
		out = append(out, SourceInfo{
			Kind: KindCodex, Title: "Codex", Detected: true, Available: true,
			Description: sourceSummary(root, codexSourceCounts),
		})
	}
	return out
}

// sourceCounts is what an import of a source home would read, counted the
// same way for every source so the picker's rows compare.
type sourceCounts struct {
	Sessions     int
	Subagents    int
	Memories     int
	HistoryLines int
	MCPServers   int
}

// sourceSummary is the picker's one line about a source home: its counts, or
// why there are none.
func sourceSummary(root string, count func(string) (sourceCounts, error)) string {
	counts, err := count(root)
	if err != nil {
		return root + " could not be read: " + err.Error()
	}
	var parts []string
	add := func(n int, one, many string) {
		if n == 1 {
			parts = append(parts, "1 "+one)
		} else if n > 1 {
			parts = append(parts, formatCount(int64(n))+" "+many)
		}
	}
	add(counts.Sessions, "session", "sessions")
	add(counts.Subagents, "subagent transcript", "subagent transcripts")
	add(counts.Memories, "memory", "memories")
	add(counts.HistoryLines, "input-history line", "input-history lines")
	add(counts.MCPServers, "MCP server", "MCP servers")
	if len(parts) == 0 {
		return root + " holds nothing to import"
	}
	return strings.Join(parts, " · ")
}

// SourceRootPrompt is the picker's source-directory question for one source:
// the label names the directory an import reads and says the field is
// optional; the default is what an empty answer resolves to.
func SourceRootPrompt(kind Kind) (label, def string) {
	switch kind {
	case KindCodex:
		return "Codex home (CODEX_HOME) — leave empty for the default directory", DefaultSourceRoot(KindCodex)
	default:
		return "Claude Code home — leave empty for the default directory", DefaultSourceRoot(KindClaude)
	}
}

// DefaultSourceRoot resolves a source's default root: $CODEX_HOME or ~/.codex
// for Codex, $CLAUDE_HOME or ~/.claude for Claude Code.
func DefaultSourceRoot(kind Kind) string {
	switch kind {
	case KindCodex:
		if root := expandSourceRoot(os.Getenv("CODEX_HOME")); root != "" {
			return root
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, ".codex")
		}
		return ""
	default:
		if root := expandSourceRoot(os.Getenv("CLAUDE_HOME")); root != "" {
			return root
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, ".claude")
		}
		return ""
	}
}

// expandSourceRoot expands a leading ~ and makes the result absolute against
// the process working directory, the two spellings a user is allowed to type.
func expandSourceRoot(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	if input == "~" || strings.HasPrefix(input, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		input = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(input, "~"), "/"))
	}
	abs, err := filepath.Abs(input)
	if err != nil {
		return filepath.Clean(input)
	}
	return abs
}

// sourceRootMarkers are the files that prove a directory really is the
// source's home. None of them present is not an importable source.
func sourceRootMarkers(kind Kind) []string {
	switch kind {
	case KindCodex:
		return []string{"sessions", "history.jsonl", "config.toml"}
	default:
		return []string{"projects", "history.jsonl", "settings.json"}
	}
}

// ValidateSourceRoot resolves and validates a source-directory answer. Empty
// input resolves to the source's default root. The returned error carries the
// underlying message verbatim (for example "stat /x/y: no such file or
// directory") — the plan forbids wrapping it in an invented explanation.
func ValidateSourceRoot(kind Kind, input string) (string, error) {
	root := expandSourceRoot(input)
	if root == "" {
		root = DefaultSourceRoot(kind)
	}
	if root == "" {
		return "", fmt.Errorf("no default directory is available for this source")
	}
	info, err := os.Stat(root)
	if err != nil {
		return root, err
	}
	if !info.IsDir() {
		return root, fmt.Errorf("%s is not a directory", root)
	}
	for _, marker := range sourceRootMarkers(kind) {
		if _, err := os.Stat(filepath.Join(root, marker)); err == nil {
			return root, nil
		}
	}
	return root, fmt.Errorf("%s holds none of the markers of a %s install (%s)",
		root, sourceDisplayName(kind), strings.Join(sourceRootMarkers(kind), ", "))
}

func sourceDisplayName(kind Kind) string {
	if kind == KindCodex {
		return "Codex"
	}
	return "Claude Code"
}

// claudeRootOverride lets tests point discovery at a fixture directory.
var claudeRootOverride string

// codexRootOverride lets tests point Codex discovery at a fixture directory.
var codexRootOverride string

// claudeRoot resolves the Claude Code home under default resolution, or ""
// when it is absent.

// claudeRootAt resolves the Claude Code home: an explicit directory (already
// validated by the picker or --home) wins, then the test override, then
// $CLAUDE_HOME or ~/.claude.
func claudeRootAt(explicit string) string {
	if root := expandSourceRoot(explicit); root != "" {
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			return root
		}
		return ""
	}
	if claudeRootOverride != "" {
		return claudeRootOverride
	}
	if root := expandSourceRoot(os.Getenv("CLAUDE_HOME")); root != "" {
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			return root
		}
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	root := filepath.Join(home, ".claude")
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return ""
	}
	return root
}

// codexRootAt resolves the Codex home with the same precedence
// claudeRootAt applies.
func codexRootAt(explicit string) string {
	if root := expandSourceRoot(explicit); root != "" {
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			return root
		}
		return ""
	}
	if codexRootOverride != "" {
		return codexRootOverride
	}
	return DefaultSourceRoot(KindCodex)
}

// Options carries everything an import needs from the calling surface. The
// paths and handles come from the composition root's environment, never from
// process-global resolution, so a test drives the same code with a sandbox.
type Options struct {
	// DryRun plans the import and writes nothing.
	DryRun bool
	// Only restricts the import to these categories (any of "sessions",
	// "memories", "skills", "plans", "mcp", "history"). Empty means all.
	Only []string
	// CurrentProjectRoot, when non-empty together with OnlyProject, limits
	// session and memory import to that project directory.
	CurrentProjectRoot string
	OnlyProject        bool

	// SessionID is the conversation the import was launched from; surfaces
	// use it to attribute the durable completion record.
	SessionID string
	// Source selects the import source (a Kind). The RunSource and
	// PlanSource entry points read it; an empty value defaults to claude.
	Source Kind
	// SourceRoot is the source application's home directory. Empty means the
	// source's default root ($CLAUDE_HOME or ~/.claude; $CODEX_HOME or
	// ~/.codex). Set by the picker's directory input or /migrate's --home;
	// both validate through ValidateSourceRoot before an import runs.
	SourceRoot string

	// Home is this install's FOREBRAIN_HOME.
	Home string
	// AgentID is the importing primary agent (the sessions' tenant).
	AgentID string
	// AgentWorkspace is that agent's workspace root (memory scopes hang off
	// it).
	AgentWorkspace string
	// ConfigPath is forebrain.yaml, for the user-level MCP append.
	ConfigPath string
	// DB is the open state database sessions are written to.
	DB *sql.DB

	// InputHistoryPath is <workspace>/state/cli-input-history.txt.
	InputHistoryPath string
	// Consolidate runs one synchronous memory-consolidation pass after the
	// memory import (B8: injected by the composition root; nil means the
	// report says the notes were written but not consolidated).
	Consolidate func(ctx context.Context, sessionID string) error

	// Now is injectable clock time for deterministic tests.
	Now func() time.Time
}

func (o *Options) now() time.Time {
	if o != nil && o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// SourceName resolves the source this run reads, defaulting to claude.
func (o *Options) SourceName() string {
	if o == nil || strings.TrimSpace(string(o.Source)) == "" {
		return string(KindClaude)
	}
	return strings.TrimSpace(string(o.Source))
}

// wants reports whether a category runs under these options.
func (o *Options) wants(category string) bool {
	if o == nil || len(o.Only) == 0 {
		return true
	}
	for _, item := range o.Only {
		if strings.EqualFold(strings.TrimSpace(item), category) {
			return true
		}
	}
	return false
}

// Progress is one checkpoint of a running import.
type Progress struct {
	// Stage names the phase: "discovering", "sessions", "memories",
	// "skills", "mcp", "history", "consolidating".
	Stage string
	// Done and Total count the stage's units when the stage is countable.
	Done, Total int
	// Detail is one line describing the latest unit.
	Detail string
}
