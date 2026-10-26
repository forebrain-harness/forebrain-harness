package tool

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
)

// HeaderParts is a card header in its four parts, for a surface that lays the
// header out itself (the web): the action ("Read", "Running", "Failed to run"),
// what it acted on, the suffix ("lines 2-101", "matching “x”", an MCP call's
// input summary) and — apart from the suffix — the duration ("0.3s", or the
// running "12s"). DisplayParts joins the duration back into the suffix.
type HeaderParts struct {
	Action   string `json:"action"`
	Target   string `json:"target,omitempty"`
	Suffix   string `json:"suffix,omitempty"`
	Duration string `json:"duration,omitempty"`
}

// DisplayHeaderParts is DisplayParts with the duration kept apart.
func DisplayHeaderParts(f DisplayFrame, cwd string) HeaderParts {
	summary := strings.TrimSpace(f.Summary)
	title := strings.TrimSpace(f.Title)
	if title == "" {
		return HeaderParts{}
	}
	meta := f.Meta
	lower := strings.ToLower(title)
	var action, target, suffix string

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
	duration := ""
	if !isRunning && !isPending && !isDenied && !isCanceled && (isFailed || isCompleted) {
		duration = FormatDuration(f.Duration)
	}

	// A tool that is still executing shows how long it has been running; the
	// viewport supplies the elapsed time, and sub-second runs show nothing.
	if isRunning && f.Duration >= time.Second && !hdrStatus(f, "awaiting approval") {
		duration = FormatWorkingElapsed(f.Duration)
	}

	return HeaderParts{
		Action:   strings.TrimSpace(action),
		Target:   strings.TrimSpace(target),
		Suffix:   strings.TrimSpace(suffix),
		Duration: duration,
	}
}

// DisplayParts maps a card's tool title and summary to three user-facing
// parts: action (past/progressive verb), target (the thing acted on), and
// suffix (extra metadata like duration or exit status).
func DisplayParts(f DisplayFrame, cwd string) (action, target, suffix string) {
	p := DisplayHeaderParts(f, cwd)
	suffix = p.Suffix
	if p.Duration != "" {
		if suffix == "" {
			suffix = p.Duration
		} else {
			suffix = suffix + " · " + p.Duration
		}
	}
	return p.Action, p.Target, suffix
}

// HeaderActions is every action DisplayHeaderParts can produce for a known
// tool, in English. The web words each one in the reader's language from a
// table keyed by these strings (frontend/src/locales, plan 136); the golden
// copy in testdata/header_actions.json is what the frontend's test reads, so
// a new action here fails both sides until the table has it.
func HeaderActions() []string {
	actions := []string{
		// Filesystem
		"Read", "Reading", "Failed to read",
		"Edited", "Editing", "Failed to edit",
		"Created", "Wrote", "Writing", "Failed to write",
		// Permissions
		"Requested permissions", "Requesting permissions", "Failed to request permissions",
		"Permission request denied", "Permission request canceled",
		// Shell
		"Ran", "Running", "Failed to run",
		// Web
		"Fetched", "Fetching", "Failed to fetch",
		"Searched", "Searching", "Failed to search",
		// LSP
		"Looked up", "Looking up", "Failed to look up",
		// retrieve_output
		"Retrieved saved output", "Retrieving saved output", "Failed to retrieve saved output",
		"Searched saved output", "Searching saved output",
		"Output retrieval denied", "Output retrieval canceled",
		// Session
		"Todo", "Updating todo", "Failed to update todo",
		"Entered plan mode", "Entering plan mode", "Failed to enter plan mode",
		"Exited plan mode", "Failed to exit plan mode", "Kept planning",
		// Memories
		"Saved memory note", "Saving memory note", "Failed to save memory note",
		"Memory note save denied", "Memory note save canceled",
		"Listed memories", "Listing memories", "Failed to list memories",
		"Memory listing denied", "Memory listing canceled",
		"Read memory", "Reading memory", "Failed to read memory",
		"Memory read denied", "Memory read canceled",
		"Searched memories", "Searching memories", "Failed to search memories",
		"Memory search denied", "Memory search canceled",
		// Notes and questions
		"Notes", "Saving notes", "Failed to save notes",
		"Asked user", "Asking user", "Failed to ask user",
		// Skill and MCP cards
		"Loading skill", "Skill",
		"MCP",
		// The generic refused and dismissed labels
		"Denied", "Canceled",
	}
	sort.Strings(actions)
	out := actions[:0]
	for i, a := range actions {
		if i > 0 && actions[i-1] == a {
			continue
		}
		out = append(out, a)
	}
	return out
}
