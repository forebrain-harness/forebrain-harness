package tool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// DisplayFrame is what a tool card's header is built from: the call's name, its
// engine summary, the structured facts (ToolMeta) and, once known, how long it
// took. The terminal's frame and the gateway's step both fill one, so the two
// surfaces say the same words about the same call — the terminal's wording is
// the standard (owner rule: shared semantics live in the engine, the surfaces
// only draw them).
type DisplayFrame struct {
	// Title is the tool's name ("read_file", "shell", "mcp__fs__list", ...).
	Title string
	// Summary is the engine's one-line summary of the step; only a shell call
	// with no recorded command reads its command back out of it.
	Summary string
	// Meta carries the call's status, input and observed extents.
	Meta ToolMeta
	// Final marks a card whose call has settled.
	Final bool
	// FilePath is the file the call touched, when Meta has none of its own.
	FilePath string
	// Content is the card's body; a write reads its diff statistics from it.
	Content string
	// Duration is how long the call ran (zero when unknown).
	Duration time.Duration
}

// DisplayHeader returns the header text for a card, e.g. "Read types.go lines
// 2-101" or "Ran ls plans/": the action, the thing acted on and any suffix,
// space-joined. cwd names the directory paths are shown relative to; empty
// means the process's own.
func DisplayHeader(f DisplayFrame, cwd string) string {
	action, target, suffix := DisplayParts(f, cwd)
	var parts []string
	if action != "" {
		parts = append(parts, action)
	}
	if target != "" {
		parts = append(parts, target)
	}
	if suffix != "" {
		parts = append(parts, suffix)
	}
	return strings.Join(parts, " ")
}

// hdrStatus reports whether the card's call is in the given state.
func hdrStatus(f DisplayFrame, status string) bool {
	return f.Meta.Status == status
}

// SkillCardStatus reports which Skill card a frame renders as: "loading"
// while the load is in flight, "completed" or "failed" once it has settled.
// It returns "" whenever the frame is not a Skill card at all — and for any
// status outside the three reachable ones, so a value the shared status
// vocabulary may gain later can never land in the success template.
func SkillCardStatus(f DisplayFrame) string {
	if !strings.EqualFold(strings.TrimSpace(f.Meta.Category), "skill") ||
		strings.TrimSpace(f.Meta.SkillName) == "" ||
		strings.TrimSpace(f.Meta.SkillPath) == "" {
		return ""
	}
	if hdrStatus(f, "failed") {
		return "failed"
	}
	if hdrStatus(f, "running") || hdrStatus(f, "awaiting approval") {
		return "loading"
	}
	status := strings.ToLower(strings.TrimSpace(f.Meta.Status))
	if status == "completed" || (status == "" && f.Final) {
		return "completed"
	}
	return ""
}

// toolStatesOwnCanceledLabel reports whether a tool already phrases its own
// canceled header (e.g. "Memory read canceled"), which the generic "Canceled"
// override must leave alone.
func toolStatesOwnCanceledLabel(lower string) bool {
	return lower == "request_permissions" || lower == "memories_add_ad_hoc_note" || IsMemoryQueryTool(lower)
}

// toolStatesOwnDeniedLabel is the same rule for the refused card. A tool the
// user refused never ran, so without this the switch above would report it in
// the past tense ("Ran ls", "Created main.go") and claim work that a denial
// prevented.
func toolStatesOwnDeniedLabel(lower string) bool {
	switch lower {
	case "request_permissions", "exit_plan_mode", "retrieve_output", "memories_add_ad_hoc_note":
		return true
	default:
		return IsMemoryQueryTool(lower)
	}
}

func defaultToolInvocationTarget(title string, meta ToolMeta) string {
	invocation := strings.TrimSpace(meta.Invocation)
	if invocation == "" {
		return ""
	}
	toolName := strings.TrimSpace(title)
	if toolName == "" {
		toolName = strings.TrimSpace(meta.ToolName)
	}
	if toolName == "" {
		return invocation
	}
	if strings.EqualFold(invocation, toolName) {
		return ""
	}
	if len(invocation) > len(toolName) && strings.EqualFold(invocation[:len(toolName)], toolName) {
		rest := strings.TrimLeft(invocation[len(toolName):], " \t")
		if rest != invocation[len(toolName):] {
			return strings.TrimSpace(rest)
		}
	}
	return invocation
}

// extractShellCommand extracts the shell command from the summary.
// Summary looks like "> command" or "ran command"; trailing details such as
// " · exit N" are omitted from the header.
func extractShellCommand(summary string) string {
	summary = strings.TrimSpace(summary)
	if strings.HasPrefix(summary, "> ") {
		summary = strings.TrimPrefix(summary, "> ")
	} else if strings.HasPrefix(summary, "ran ") {
		summary = strings.TrimPrefix(summary, "ran ")
	} else {
		return ""
	}
	if idx := strings.Index(summary, " · "); idx >= 0 {
		summary = strings.TrimSpace(summary[:idx])
	}
	return summary
}

// FilePathFromToolMeta extracts the file_path string from a tool's input metadata.
// Returns "" when no file_path key is present or the value is not a string.
func FilePathFromToolMeta(tm ToolMeta) string {
	if v, ok := tm.Input["file_path"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// InputString extracts a string value from a tool's Input metadata map.
// Returns "" when the key is absent or the value is not a string.
func InputString(tm ToolMeta, key string) string {
	if v, ok := tm.Input[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// ReadFileDisplayLineRange returns the 1-based inclusive file line range a
// completed read_file card actually shows. Engine facts win over the request
// arguments: the caller's limit may exceed the page the engine returned (the
// 200-line ceiling), and the header must describe the result, not the request.
// The input-derived range remains only as the fallback for sessions replayed
// before the facts were carried in ToolMeta.
func ReadFileDisplayLineRange(tm ToolMeta) (lo, hi int, ok bool) {
	if tm.ResultLines > 0 {
		return tm.ResultOffset + 1, tm.ResultOffset + tm.ResultLines, true
	}
	return readFileInputLineRange(tm)
}

// readFileInputLineRange converts read_file's 0-based offset and line limit
// into the 1-based inclusive range shown in completed tool headers.
func readFileInputLineRange(tm ToolMeta) (lo, hi int, ok bool) {
	offset, hasOffset := InputInt(tm, "offset")
	limit, hasLimit := InputInt(tm, "limit")
	if !hasOffset || !hasLimit || offset < 0 || limit <= 0 {
		return 0, 0, false
	}
	return offset + 1, offset + limit, true
}

func InputInt(tm ToolMeta, key string) (int, bool) {
	value, ok := tm.Input[key]
	if !ok {
		return 0, false
	}
	switch n := value.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		value := int(n)
		return value, float64(value) == n
	case json.Number:
		value, err := n.Int64()
		return int(value), err == nil
	default:
		return 0, false
	}
}

// FailedActionPhrase returns a natural-language verb phrase for a failed tool,
// e.g. "edit" for edit_file. Falls back to the tool name
// verbatim when no specific mapping exists.
func FailedActionPhrase(toolName string) string {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "read_file", "read":
		return "read"
	case "edit_file", "edit":
		return "edit"
	case "write_file", "write":
		return "write"
	case "shell", "bash", "sh":
		return "run"
	case "web_fetch", "webfetch":
		return "fetch"
	case "web_search", "websearch":
		return "search"
	case "lsp":
		return "look up"
	case "enter_plan_mode":
		return "enter plan mode"
	case "exit_plan_mode":
		return "exit plan mode"
	case "request_permissions":
		return "request permissions"
	case "session_todo":
		return "update todo"
	case "user_interaction":
		return "ask user"
	case "present_files":
		return "present files"
	case "intermediate_tool":
		return "save notes"
	case "memories_list":
		return "list memories"
	case "memories_read":
		return "read memory"
	case "memories_search":
		return "search memories"
	default:
		return toolName
	}
}

// FormatDuration formats a time.Duration for display in tool headers.
// Durations under 100ms return empty string (too fast to be useful).
// >= 60s renders as "1m23s"; >= 10s as "12s"; else "1.2s".
func FormatDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	if d < 100*time.Millisecond {
		return "<0.1s"
	}
	sec := d.Seconds()
	if sec >= 60 {
		m := int(sec) / 60
		s := int(sec) % 60
		return fmt.Sprintf("%dm%ds", m, s)
	}
	if sec >= 10 {
		return fmt.Sprintf("%ds", int(sec))
	}
	return fmt.Sprintf("%.1fs", sec)
}

// DisplayPath returns a path relative to cwd when the path is under cwd,
// otherwise the path unchanged (absolute or already-relative).  When cwd is
// empty the function falls back to os.Getwd() so displayPath is correct even
// in contexts where the caller does not hold a cwd reference (e.g. the
// viewport fold-block path which re-captures via a scratch Renderer).
func DisplayPath(absPath, cwd string) string {
	if absPath == "" {
		return absPath
	}
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return absPath
		}
	}
	// If the path is already relative (no leading /) or outside cwd,
	// return unchanged.
	if absPath == cwd {
		return "."
	}
	if strings.HasPrefix(absPath, cwd+string(filepath.Separator)) {
		rel, err := filepath.Rel(cwd, absPath)
		if err == nil {
			return rel
		}
		return strings.TrimPrefix(absPath, cwd+string(filepath.Separator))
	}
	return absPath
}

// TurnDiffStats extracts added/deleted line counts from a ```diff fenced block.
func TurnDiffStats(content string) (added, deleted int, ok bool) {
	lines := strings.Split(content, "\n")
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				// Match only the exact closing fence (``` with optional trailing
				// whitespace). Diff context lines (e.g. "  ```sh" or "  ``` ")
				// have leading space and must not terminate counting.
				if strings.TrimRight(line, " \t\r") == "```" {
					break
				}
				continue
			}
			inFence = strings.HasPrefix(trimmed, "```diff")
			continue
		}
		if inFence {
			if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "@@") {
				continue
			}
			if len(line) > 0 && line[0] == '+' {
				added++
			} else if len(line) > 0 && line[0] == '-' {
				deleted++
			}
		}
	}
	if added == 0 && deleted == 0 {
		return 0, 0, false
	}
	return added, deleted, true
}

// IsMemoryQueryTool reports whether a tool reads the memory store and therefore
// renders a Markdown result card (memories_add_ad_hoc_note writes, and has its
// own note-body path).
func IsMemoryQueryTool(title string) bool {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "memories_list", "memories_read", "memories_search":
		return true
	}
	return false
}

// SingleDisplayLine folds text that arrives with its own line breaks onto one
// line, joining what the breaks separated with a single space.
//
// Every caller renders into a row of a card, and those rows are line-oriented:
// a row's role is encoded in its leading whitespace and per-row data is indexed
// by line number, so a value that is semantically one row must not carry a
// newline into it. A column budget is meaningless across a line break as well —
// the second line starts over at column zero — so folding first is what makes
// truncation measure the text the reader actually sees.
func SingleDisplayLine(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
	out := parts[:0]
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, " ")
}

// DisplayLine normalizes a value for a tool header: one logical line, valid
// UTF-8, and no length cap.
//
// A header segment that carries the call's own arguments must use this rather
// than TruncateForDisplay. wrapToolDisplayLine already folds a long header onto
// continuation rows, so there is nothing to gain from cutting the tail off —
// and a reader who cannot see the last of the arguments cannot tell what the
// call actually asked for.
func DisplayLine(s string) string {
	s = SingleDisplayLine(strings.TrimSpace(s))
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return s
}

// TruncateForDisplay shortens s to at most maxLen terminal columns, ending it
// with an ellipsis when it had to cut.
//
// Every caller spends maxLen as a column budget (a task title beside a fixed
// label, a query inside a card), so columns — not bytes — is the unit that
// makes those budgets come out right. Counting bytes went wrong twice: it cut
// multi-byte characters mid-rune, which the terminal draws as a replacement
// glyph, and it under-filled the budget for wide text, since CJK costs three
// bytes per two columns — a title got barely a third of the width it was given.
//
// Input that is already invalid UTF-8 is repaired rather than passed along:
// these strings come from tool output and model text that something upstream
// may have byte-sliced, and runewidth measures a stray byte as zero columns,
// which would silently skew the budget it is asked to enforce.
func TruncateForDisplay(s string, maxLen int) string {
	return runewidth.Truncate(DisplayLine(s), maxLen, "…")
}

func FormatWorkingElapsed(elapsed time.Duration) string {
	totalSeconds := int(elapsed / time.Second)
	if totalSeconds < 60 {
		return fmt.Sprintf("%ds", totalSeconds)
	}
	seconds := totalSeconds % 60
	totalMinutes := totalSeconds / 60
	if totalMinutes < 60 {
		return fmt.Sprintf("%dm %02ds", totalMinutes, seconds)
	}
	minutes := totalMinutes % 60
	hours := totalMinutes / 60
	return fmt.Sprintf("%dh %02dm %02ds", hours, minutes, seconds)
}
