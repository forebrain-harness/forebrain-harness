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

	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
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

// DisplayParts maps a card's tool title and summary to three user-facing
// parts: action (past/progressive verb), target (the thing acted on), and
// suffix (extra metadata like duration or exit status).
func DisplayParts(f DisplayFrame, cwd string) (action, target, suffix string) {
	summary := strings.TrimSpace(f.Summary)
	title := strings.TrimSpace(f.Title)
	if title == "" {
		return "", "", ""
	}
	meta := f.Meta
	lower := strings.ToLower(title)

	isRunning := hdrStatus(f, "running")
	isPending := (hdrStatus(f, "running") || hdrStatus(f, "awaiting approval"))
	isFailed := hdrStatus(f, "failed")
	isDenied := hdrStatus(f, "denied")
	isCanceled := hdrStatus(f, "canceled")
	durationStatus := strings.ToLower(strings.TrimSpace(meta.Status))
	isCompleted := durationStatus == "completed" || (durationStatus == "" && f.Final && !isFailed && !isDenied && !isCanceled)

	filePath := FilePathFromToolMeta(meta)
	if filePath == "" {
		filePath = f.FilePath
	}
	displayP := ""
	if filePath != "" {
		displayP = DisplayPath(filePath, cwd)
	}

	switch {
	case SkillCardStatus(f) != "":
		// The three reachable Skill states own their header. Loading says what
		// is happening; the terminal states name the skill and let the shared
		// tail below append the real duration. Everything else about this frame
		// is ordinary tool rendering.
		target = strings.TrimSpace(meta.SkillName)
		if SkillCardStatus(f) == "loading" {
			action = "Loading skill"
		} else {
			action = "Skill"
		}

	// --- Filesystem ---
	case lower == "read_file" || lower == "read":
		if isFailed {
			action = "Failed to " + FailedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Reading"
		} else {
			action = "Read"
		}
		target = displayP
		if isCompleted {
			if lo, hi, ok := ReadFileDisplayLineRange(meta); ok {
				suffix = fmt.Sprintf("lines %d-%d", lo, hi)
			}
		}

	case lower == "edit_file":
		if isFailed {
			action = "Failed to " + FailedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Editing"
		} else {
			action = "Edited"
		}
		target = displayP

	case lower == "write_file":
		if isFailed {
			action = "Failed to " + FailedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Writing"
		} else {
			_, deleted, ok := TurnDiffStats(f.Content)
			if ok {
				if deleted > 0 {
					action = "Wrote"
				} else {
					action = "Created"
				}
			} else {
				action = "Created"
			}
		}
		target = displayP

	case lower == "request_permissions":
		if isFailed {
			action = "Failed to request permissions"
		} else if isDenied {
			action = "Permission request denied"
		} else if isCanceled {
			action = "Permission request canceled"
		} else if isRunning || isPending {
			action = "Requesting permissions"
		} else {
			action = "Requested permissions"
		}

	// --- Shell ---
	case lower == "shell":
		if isFailed {
			action = "Failed to " + FailedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Running"
		} else {
			action = "Ran"
		}
		target = InputString(meta, "command")
		if target == "" {
			target = extractShellCommand(summary)
		}

	// --- Web ---
	case lower == "web_fetch" || lower == "webfetch":
		if isFailed {
			action = "Failed to " + FailedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Fetching"
		} else {
			action = "Fetched"
		}
		if u := InputString(meta, "url"); u != "" {
			target = u
		}

	case lower == "web_search" || lower == "websearch":
		if isFailed {
			action = "Failed to " + FailedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Searching"
		} else {
			action = "Searched"
		}
		if q := InputString(meta, "query"); q != "" {
			target = q
		}

	case lower == "lsp":
		if isFailed {
			action = "Failed to " + FailedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Looking up"
		} else {
			action = "Looked up"
		}
		target = InputString(meta, "operation")
		qualifier := ""
		if symbol := InputString(meta, "symbol"); symbol != "" {
			qualifier = symbol
		} else if query := InputString(meta, "query"); query != "" {
			qualifier = query
		} else if p := InputString(meta, "file_path"); p != "" {
			qualifier = DisplayPath(p, cwd)
			if line, ok := InputInt(meta, "line"); ok && line > 0 {
				qualifier = fmt.Sprintf("%s:%d", qualifier, line)
			}
		}
		switch {
		case target != "" && qualifier != "":
			target = target + " · " + qualifier
		case qualifier != "":
			target = qualifier
		}

	case lower == "retrieve_output":
		switch {
		case isFailed:
			action = "Failed to retrieve saved output"
		case isDenied:
			action = "Output retrieval denied"
		case isCanceled:
			action = "Output retrieval canceled"
		case InputString(meta, "query") != "" && (isRunning || isPending):
			action = "Searching saved output"
		case InputString(meta, "query") != "":
			action = "Searched saved output"
		case isRunning || isPending:
			action = "Retrieving saved output"
		default:
			action = "Retrieved saved output"
		}
		if id, ok := InputInt(meta, "id"); ok && id > 0 {
			target = fmt.Sprintf("#%d", id)
		}
		if query := strings.Join(strings.Fields(InputString(meta, "query")), " "); query != "" {
			suffix = "matching “" + TruncateForDisplay(query, 72) + "”"
		} else if lines := strings.TrimSpace(InputString(meta, "lines")); lines != "" {
			suffix = "lines " + TruncateForDisplay(lines, 32)
		}

	// --- Session ---
	case lower == "session_todo":
		if isFailed {
			action = "Failed to " + FailedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Updating todo"
		} else {
			action = "Todo"
		}

	case lower == "enter_plan_mode":
		if isFailed {
			action = "Failed to " + FailedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Entering plan mode"
		} else {
			action = "Entered plan mode"
		}

	case lower == "exit_plan_mode":
		// No running/pending title on purpose: the gate's wait paints no card
		// on any surface (ToolStepHoldsNoCard, enforced where the tool
		// message enters the reducer), so no frame can arrive asking for one.
		// The settled states are all that can render.
		if isDenied {
			action = "Kept planning"
		} else if isFailed {
			action = "Failed to " + FailedActionPhrase(lower)
		} else {
			action = "Exited plan mode"
		}

	case lower == "memories_add_ad_hoc_note":
		switch {
		case isFailed:
			action = "Failed to save memory note"
		case isDenied:
			action = "Memory note save denied"
		case isCanceled:
			action = "Memory note save canceled"
		case isRunning || isPending:
			action = "Saving memory note"
		default:
			action = "Saved memory note"
		}
		target = InputString(meta, "filename")
		// A saved note is named by the path it is stored at — the path it is
		// read back by — so a reader is never left guessing where it lives. The
		// states that wrote nothing keep the bare filename rather than claiming
		// a location on disk.
		if target != "" && action == "Saved memory note" {
			target = memory.AdHocNotePath(target)
		}

	// A memory query that the user denied or dismissed never touched the store,
	// so these cases name the refused action rather than falling through to the
	// past tense ("Read memory MEMORY.md") that claims it happened.
	case lower == "memories_list":
		switch {
		case isFailed:
			action = "Failed to " + FailedActionPhrase(lower)
		case isDenied:
			action = "Memory listing denied"
		case isCanceled:
			action = "Memory listing canceled"
		case isRunning || isPending:
			action = "Listing memories"
		default:
			action = "Listed memories"
		}
		target = DisplayLine(InputString(meta, "path"))

	case lower == "memories_read":
		switch {
		case isFailed:
			action = "Failed to " + FailedActionPhrase(lower)
		case isDenied:
			action = "Memory read denied"
		case isCanceled:
			action = "Memory read canceled"
		case isRunning || isPending:
			action = "Reading memory"
		default:
			action = "Read memory"
		}
		target = DisplayLine(InputString(meta, "path"))
		if isCompleted {
			if lo, hi, ok := MemoryReadLineRange(meta.Input); ok {
				suffix = fmt.Sprintf("lines %d-%d", lo, hi)
			}
		}

	case lower == "memories_search":
		switch {
		case isFailed:
			action = "Failed to " + FailedActionPhrase(lower)
		case isDenied:
			action = "Memory search denied"
		case isCanceled:
			action = "Memory search canceled"
		case isRunning || isPending:
			action = "Searching memories"
		default:
			action = "Searched memories"
		}
		// The queries are the call. They are shown in full and the header wraps;
		// where it looked rides in the suffix, so "in MEMORY.md" stays legible
		// next to a long query list, and a search of the whole store shows no
		// scope.
		target = DisplayLine(MemorySearchQueryLabel(meta.Input))
		suffix = DisplayLine(MemorySearchScopeLabel(meta.Input))

	case lower == "intermediate_tool":
		if isFailed {
			action = "Failed to " + FailedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Saving notes"
		} else {
			action = "Notes"
		}
		if a := InputString(meta, "action"); a != "" {
			target = a
		}

	case lower == "user_interaction":
		if isFailed {
			action = "Failed to " + FailedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Asking user"
		} else {
			action = "Asked user"
		}
		// The header carries the verb alone; what was asked is the body's
		// business (the engine pairs each question with its answer).

	// --- MCP ---
	case strings.HasPrefix(lower, "mcp__"):
		action = "MCP"
		if display, ok := McpToolDisplayName(title); ok {
			target = display
		}
		suffix = MCPToolInputSummary(meta.Input)
	default:
		action = title
		target = defaultToolInvocationTarget(title, meta)
	}

	if isCanceled && !toolStatesOwnCanceledLabel(lower) {
		action = "Canceled"
		suffix = ""
	}
	if isDenied && !toolStatesOwnDeniedLabel(lower) {
		action = "Denied"
		suffix = ""
	}

	// Append the executor-supplied duration for terminal states that represent an
	// actual execution. Pending, denied, and canceled cards intentionally omit it.
	if !isRunning && !isPending && !isDenied && !isCanceled && (isFailed || isCompleted) {
		if ds := FormatDuration(f.Duration); ds != "" {
			if suffix == "" {
				suffix = ds
			} else {
				suffix = suffix + " · " + ds
			}
		}
	}

	// A tool that is still executing shows how long it has been running; the
	// viewport supplies the elapsed time, and sub-second runs show nothing.
	if isRunning && f.Duration >= time.Second && !hdrStatus(f, "awaiting approval") {
		if suffix == "" {
			suffix = FormatWorkingElapsed(f.Duration)
		} else {
			suffix = suffix + " · " + FormatWorkingElapsed(f.Duration)
		}
	}

	return strings.TrimSpace(action), strings.TrimSpace(target), strings.TrimSpace(suffix)
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

// truncateForDisplay shortens s to at most maxLen terminal columns, ending it
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
// DisplayLine normalizes a value for a tool header: one logical line, valid
// UTF-8, and no length cap.
//
// A header segment that carries the call's own arguments must use this rather
// than truncateForDisplay. wrapToolDisplayLine already folds a long header onto
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
