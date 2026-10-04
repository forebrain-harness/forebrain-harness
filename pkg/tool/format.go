package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

const DefaultMaxFormattedBody = 120_000

func NormalizeToolStepForDisplay(evt StepEvent) StepEvent {
	return evt
}

func FormatToolStepResult(evt StepEvent, maxBytes int) (formatted string, truncated bool) {
	evt = NormalizeToolStepForDisplay(evt)
	if maxBytes <= 0 {
		maxBytes = DefaultMaxFormattedBody
	}
	toolName := strings.TrimSpace(evt.ToolName)
	if toolName == "" {
		return "", false
	}
	kind := strings.TrimSpace(evt.Kind)
	if kind == StepKindToolStarted || kind == StepKindToolParallelStarted {
		return "", false
	}
	if stepRequiresApproval(evt) {
		return "", false
	}
	// A skill step (an explicit invocation or a loaded skill's root SKILL.md
	// read) carries one semantic body naming its source; on failure the raw
	// error rides below it verbatim. Only failed and completed steps say so —
	// any other status falls through to the ordinary tool rendering. The
	// skill's activation body, JSON envelope or file preview never appears
	// here.
	if _, path, ok := skillStepMetadata(evt); ok {
		if strings.TrimSpace(evt.Error) != "" {
			return skillLoadDisplayBody(evt, path), false
		}
		switch strings.TrimSpace(evt.Kind) {
		case StepKindToolCompleted, StepKindToolParallelCompleted:
			return skillLoadDisplayBody(evt, path), false
		}
	}
	if strings.TrimSpace(evt.Error) != "" {
		sb := strings.Builder{}
		sb.WriteString("**error** (")
		sb.WriteString(toolName)
		sb.WriteString(")\n\n")
		sb.WriteString("```\n")
		sb.WriteString(evt.Error)
		sb.WriteString("\n```")
		return clampBody(sb.String(), maxBytes)
	}
	// A successful edit_file replacement can produce the same bytes as the
	// original file. Keep the completed tool card, but do not expose its empty
	// turn-diff envelope (or the remaining transport metadata) as UI output.
	if isNoOpEditFileStep(evt, toolName) {
		return "", false
	}
	var body string
	switch toolName {
	case "read_file":
		body, truncated = formatReadFileStep(evt, maxBytes)
	case "shell":
		body, truncated = formatShellStep(evt, maxBytes)
	case "web_fetch":
		body, truncated = formatWebFetchStep(evt, maxBytes)
	case "web_search":
		body, truncated = formatWebSearchStep(evt, maxBytes)
	case "subagent_fanout":
		body, truncated = formatFanoutStep(evt, maxBytes)
	case "subagent_run":
		body, truncated = formatSubagentRunStep(evt, maxBytes)
	case "intermediate_tool":
		body, truncated = formatIntermediateToolStep(evt, maxBytes)
	case "exit_plan_mode":
		body, truncated = formatExitPlanModeStep(evt, maxBytes)
	case "user_interaction":
		body, truncated = formatUserInteractionStep(evt, maxBytes)
	case "working_set_pin":
		body, truncated = formatWorkingSetPinStep(evt, maxBytes)
	case "request_permissions":
		body, truncated = formatRequestPermissionsStep(evt, maxBytes)
	case "retrieve_output":
		body, truncated = formatRetrieveOutputStep(evt, maxBytes)
	case "lsp":
		body, truncated = formatLSPStep(evt, maxBytes)
	case "memories_list", "memories_read", "memories_search", "memories_add_ad_hoc_note":
		body, truncated = formatMemoryToolStep(evt, toolName, maxBytes)
	default:
		body, truncated = formatGenericToolStep(evt, toolName, maxBytes)
	}
	return body, truncated
}

// skillLoadDisplayBody is the semantic text a completed or failed skill step
// carries as its user-visible body. It names where the skill was loaded from
// and, on failure, the raw error as-is: error text is what diagnosis needs, and
// any rewriting risks hiding exactly the detail that mattered. The absolute
// path is intentional — this layer has no cwd — and each surface shortens it
// for display.
func skillLoadDisplayBody(evt StepEvent, path string) string {
	if strings.TrimSpace(evt.Error) != "" {
		return "Failed to load from " + path + "\n" + evt.Error
	}
	return "Loaded from " + path
}

func BuildToolMeta(evt StepEvent) ToolMeta {
	evt = NormalizeToolStepForDisplay(evt)
	meta := ToolMeta{
		ToolName:   strings.TrimSpace(evt.ToolName),
		Status:     strings.TrimSpace(stepStatusLabel(evt)),
		Purpose:    strings.TrimSpace(toolPurpose(evt)),
		Invocation: strings.TrimSpace(toolInvocationLabel(evt)),
	}
	if len(evt.Input) > 0 {
		meta.Input = evt.Input
	}
	// result_lines is the paging read's own report of how many content lines
	// it returned; offset is the 0-based line it started at (absent means the
	// read began at the top). Surfaces count file lines from these facts, so
	// a narrow terminal's wrapped rows can never masquerade as extra lines.
	if n, ok := firstInt(evt.Output, "result_lines"); ok && n > 0 {
		meta.ResultLines = n
		meta.ResultOffset = intFromAny(evt.Output["offset"])
	}
	if meta.Status == "running" && !evt.StartedAt.IsZero() {
		meta.StartedAtMs = evt.StartedAt.UnixMilli()
	}
	if name, path, ok := skillStepMetadata(evt); ok {
		meta.Category = "skill"
		meta.SkillName = name
		meta.SkillPath = path
	}
	return meta
}

// skillStepMetadata projects the structured skill identity of a step. The
// structured Category field is the only identity an explicit invocation needs;
// a read_file step may still carry its identity as output markers persisted by
// older events. Description prefixes, tool names and step-id spellings are
// never consulted.
func skillStepMetadata(evt StepEvent) (name, path string, ok bool) {
	name = strings.TrimSpace(evt.SkillName)
	path = strings.TrimSpace(evt.SkillPath)
	if strings.EqualFold(strings.TrimSpace(evt.Category), "skill") {
		if name == "" || path == "" {
			return "", "", false
		}
		return name, filepath.Clean(path), true
	}
	if !strings.EqualFold(strings.TrimSpace(evt.ToolName), "read_file") {
		return "", "", false
	}
	if name != "" && path != "" {
		return name, filepath.Clean(path), true
	}
	kind := ""
	if evt.Output != nil {
		kind, _ = evt.Output["preview_kind"].(string)
		name, _ = evt.Output["skill_name"].(string)
		path, _ = evt.Output["skill_path"].(string)
	}
	name = strings.TrimSpace(name)
	path = strings.TrimSpace(path)
	if !strings.EqualFold(strings.TrimSpace(kind), "skill") || name == "" || path == "" {
		return "", "", false
	}
	return name, filepath.Clean(path), true
}

func EnrichToolMeta(ctx context.Context, meta ToolMeta) ToolMeta {
	if ctx == nil {
		return meta
	}
	if agentID := strings.TrimSpace(HookAgentIDFromContext(ctx)); agentID != "" {
		meta.AgentID = agentID
	}
	if subtype := strings.TrimSpace(SubagentTypeFromContext(ctx)); subtype != "" {
		meta.AgentType = subtype
		if strings.TrimSpace(meta.AgentKind) == "" {
			meta.AgentKind = "typed"
		}
	}
	if IsForkChildFromContext(ctx) {
		if strings.TrimSpace(meta.AgentKind) == "" {
			meta.AgentKind = "fork"
		}
		if strings.TrimSpace(meta.AgentType) == "" {
			meta.AgentType = "fork"
		}
	}
	if strings.TrimSpace(meta.AgentID) == "" {
		meta.AgentType = ""
		meta.AgentKind = ""
	}
	return meta
}

func SummarizeToolStep(evt StepEvent) string {
	evt = NormalizeToolStepForDisplay(evt)
	toolName := strings.TrimSpace(evt.ToolName)
	if toolName == "" {
		return ""
	}
	if errText := strings.TrimSpace(evt.Error); errText != "" {
		return "failed: " + truncateSummary(errText)
	}
	invocation := toolInvocationLabel(evt)
	if invocation == "" {
		invocation = toolName
	}
	switch strings.TrimSpace(evt.Kind) {
	case StepKindToolStarted, StepKindToolParallelStarted, StepKindToolOutputDelta:
		if toolName == "user_interaction" {
			return userInteractionLabel("asking user", evt)
		}
		return "running " + invocation
	case StepKindToolCompleted, StepKindToolParallelCompleted:
		if toolName == "user_interaction" {
			// The question is the call: while it waits it is being asked, and
			// once answered it was asked — never "ran", never an approval.
			if stepRequiresApproval(evt) {
				return userInteractionLabel("asking user", evt)
			}
			return userInteractionLabel("asked user", evt)
		}
		if stepRequiresApproval(evt) {
			return "awaiting approval · " + invocation
		}
		if toolName == "request_permissions" {
			return requestPermissionsCompletionSummary(evt, invocation)
		}
		if toolName == "lsp" {
			if summary := lspStepSummary(evt); summary != "" {
				return summary
			}
		}
		if toolName == "intermediate_tool" {
			action := strings.ToLower(strings.TrimSpace(firstString(evt.Input, "action")))
			if action == "" {
				action = strings.ToLower(strings.TrimSpace(stringFromAny(evt.Output["action"])))
			}
			switch action {
			case "", "append":
				return "saved intermediate notes"
			case "read":
				if strings.TrimSpace(stringFromAny(evt.Output["content"])) == "" {
					return "reviewed intermediate notes · empty"
				}
				return "reviewed intermediate notes"
			case "clear":
				return "cleared intermediate notes"
			}
		}
		if summary, ok := memoryStepSummary(evt, toolName); ok {
			return summary
		}
		summary := strings.TrimSpace(stringFromAny(evt.Output["summary"]))
		if summary != "" {
			return truncateSummary(summary)
		}
		// read_file / read: show line range (e.g. "lines 586-610") instead of
		// a raw line count ("42 lines"), since the caller passes offset+limit.
		if toolName == "read" || toolName == "read_file" {
			offset := intFromAny(evt.Input["offset"])
			limit := intFromAny(evt.Input["limit"])
			if limit > 0 {
				start := offset + 1 // 0-based offset → 1-based display
				end := offset + limit
				return "ran " + invocation + " · lines " + strconv.Itoa(start) + "-" + strconv.Itoa(end)
			}
			// The LLM may omit offset/limit from its arguments, but the tool
			// implementation always stores the effective normalized values in
			// the output map. Fall back to those so the display always shows a
			// line range instead of a bare count.
			offset = intFromAny(evt.Output["offset"])
			if n := intFromAny(evt.Output["result_lines"]); n > 0 {
				start := offset + 1
				end := offset + n
				return "ran " + invocation + " · lines " + strconv.Itoa(start) + "-" + strconv.Itoa(end)
			}
		}
		if preview := strings.TrimSpace(stringFromAny(evt.Output["stdout_preview"])); strings.EqualFold(preview, "no changes") {
			return "ran " + invocation + " · no changes"
		}
		if n := intFromAny(evt.Output["stdout_bytes"]); n > 0 {
			return "ran " + invocation + " · " + formatBytes(n)
		}
		if n, ok := firstInt(evt.Output, "result_lines", "stdout_lines"); ok {
			return "ran " + invocation + " · " + formatCountLabel(n, "line", "lines")
		}
		if text := strings.TrimSpace(stringFromAny(evt.Output["preview_text"])); text != "" {
			return "ran " + invocation + " · " + formatLineCount(text)
		}
		if n, ok := firstInt(evt.Output, "matches", "paths", "files", "roots", "items", "count", "tokens", "candidates"); ok {
			suffix := formatCountForTool(toolName, n)
			return "ran " + invocation + " · " + suffix
		}
		if path := pathFromInputOrOutput(evt); path != "" {
			return "ran " + invocation + " · " + pathBaseOrPath(path)
		}
		if len(evt.Output) > 0 {
			return "ran " + invocation
		}
		return "ran " + invocation
	default:
		if summary := strings.TrimSpace(stringFromAny(evt.Output["summary"])); summary != "" {
			return truncateSummary(summary)
		}
	}
	return invocation
}

// requestPermissionsCompletionSummary builds the one-line transcript summary
// for a completed request_permissions step, naming the approval outcome and,
// for approval-exempt (auto-approved) calls, the policy condition that
// exempted it from an interactive prompt.
func requestPermissionsCompletionSummary(evt StepEvent, invocation string) string {
	status := strings.ToLower(strings.TrimSpace(stringFromAny(evt.Output["approval_status"])))
	reason := strings.TrimSpace(stringFromAny(evt.Output["approval_reason"]))
	switch status {
	case "auto_approved":
		if reason != "" {
			return "auto-approved " + invocation + " · " + truncateSummary(reason)
		}
		return "auto-approved " + invocation + " (no approval prompt needed)"
	case "denied":
		if reason != "" {
			return "denied " + invocation + " · " + truncateSummary(reason)
		}
		return "denied " + invocation
	case "approved":
		return "approved " + invocation
	default:
		return "ran " + invocation
	}
}

func formatReadFileStep(evt StepEvent, maxBytes int) (string, bool) {
	pathHint := readFilePathHint(evt)
	lang := "text"
	if pathHint != "" {
		lang = chromaLangFromPath(pathHint)
	}
	var body string
	if evt.Output != nil {
		if rt, ok := evt.Output["preview_text"].(string); ok {
			body = rt
		}
	}
	if strings.TrimSpace(body) == "" {
		sb := strings.Builder{}
		sb.WriteString("_(no file body in step payload)_\n\n")
		if pathHint != "" {
			sb.WriteString("path: `")
			sb.WriteString(pathHint)
			sb.WriteString("`\n\n")
		}
		if evt.Output != nil {
			if b, err := json.MarshalIndent(evt.Output, "", "  "); err == nil {
				sb.WriteString("```json\n")
				sb.WriteString(string(b))
				sb.WriteString("\n```")
			}
		}
		return clampBody(sb.String(), maxBytes)
	}
	out := fmt.Sprintf("path: `%s`\n\n```%s\n%s\n```", pathHint, lang, body)
	return clampBody(out, maxBytes)
}

func readFilePathHint(evt StepEvent) string {
	// A read whose path was repaired into the skill catalog read a different
	// file than the one in the arguments; show the file that was read.
	if repaired := RepairedReadPath(evt); repaired != "" {
		return repaired
	}
	if evt.Input != nil {
		if fp, ok := evt.Input["file_path"].(string); ok && strings.TrimSpace(fp) != "" {
			return strings.TrimSpace(fp)
		}
	}
	if evt.Output != nil {
		if fp, ok := evt.Output["file_path"].(string); ok && strings.TrimSpace(fp) != "" {
			return strings.TrimSpace(fp)
		}
		if abs, ok := evt.Output["abs_path"].(string); ok && strings.TrimSpace(abs) != "" {
			return strings.TrimSpace(abs)
		}
	}
	return ""
}

// RepairedReadPath returns the file a read_file step actually read when its
// requested path was repaired into a loaded skill's root, and "" otherwise.
func RepairedReadPath(evt StepEvent) string {
	if evt.Output == nil {
		return ""
	}
	if from, ok := evt.Output["repaired_from"].(string); !ok || strings.TrimSpace(from) == "" {
		return ""
	}
	abs, ok := evt.Output["abs_path"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(abs)
}

func formatShellStep(evt StepEvent, maxBytes int) (string, bool) {
	sb := strings.Builder{}
	sb.WriteString("**shell**\n\n")
	if len(evt.Input) > 0 {
		if b, err := json.MarshalIndent(evt.Input, "", "  "); err == nil {
			sb.WriteString("input:\n\n```json\n")
			sb.WriteString(string(b))
			sb.WriteString("\n```\n\n")
		}
	}
	if len(evt.Output) > 0 {
		stdout := strings.TrimSpace(stringFromAny(evt.Output["stdout"]))
		stderr := strings.TrimSpace(stringFromAny(evt.Output["stderr"]))
		if stdout != "" {
			sb.WriteString("stdout:\n\n```")
			sb.WriteString(shellOutputLang(evt, stdout))
			sb.WriteString("\n")
			sb.WriteString(stdout)
			sb.WriteString("\n```\n\n")
		}
		if stderr != "" {
			sb.WriteString("stderr:\n\n```text\n")
			sb.WriteString(stderr)
			sb.WriteString("\n```\n\n")
		}
		if note := omittedBytesNote(evt.Output); note != "" {
			sb.WriteString(note)
			sb.WriteString("\n\n")
		}
		renderOutput := shellOutputMetadataOnly(evt.Output)
		if len(renderOutput) > 0 {
			if b, err := json.MarshalIndent(renderOutput, "", "  "); err == nil {
				sb.WriteString("output:\n\n```json\n")
				sb.WriteString(string(b))
				sb.WriteString("\n```")
			}
		}
	}
	if diagSection := lspDiagnosticsSection(evt.Output); diagSection != "" {
		sb.WriteString("\n\n")
		sb.WriteString(diagSection)
	}
	return clampBody(sb.String(), maxBytes)
}

func shellOutputLang(evt StepEvent, stdout string) string {
	command := strings.ToLower(strings.TrimSpace(stringFromAny(evt.Input["command"])))
	text := strings.TrimSpace(stdout)
	if strings.HasPrefix(text, "diff --git ") ||
		strings.HasPrefix(text, "--- ") ||
		strings.HasPrefix(command, "git diff") {
		return "diff"
	}
	return "text"
}

func formatMemoryToolStep(evt StepEvent, tool string, maxBytes int) (string, bool) {
	if tool == "memories_add_ad_hoc_note" {
		if len(evt.Output) == 0 {
			return "Memory tool returned an unreadable note result.", false
		}
		result, ok := memoryResultPayload(evt.Output)
		if !ok || !validMemoryAddNoteResult(mapFromAny(result)) {
			return "Memory tool returned an unreadable note result.", false
		}
		note := stringFromAny(evt.Input["note"])
		if strings.TrimSpace(note) == "" {
			return "Memory tool returned a note result without displayable content.", false
		}
		return clampBody(note, maxBytes)
	}
	result, ok := memoryResultPayload(evt.Output)
	if !ok {
		return "Memory tool returned an unreadable result.", false
	}
	data := mapFromAny(result)
	switch tool {
	case "memories_list":
		if !validMemoryListResult(data) {
			return "Memory tool returned an unreadable list result.", false
		}
		return formatMemoryListResult(data, maxBytes)
	case "memories_read":
		if !validMemoryReadResult(data) {
			return "Memory tool returned an unreadable file result.", false
		}
		return formatMemoryReadResult(data, maxBytes)
	case "memories_search":
		if !validMemorySearchResult(data) {
			return "Memory tool returned an unreadable search result.", false
		}
		return formatMemorySearchResult(data, maxBytes)
	default:
		return "Memory tool returned an unreadable result.", false
	}
}

func memoryResultPayload(output map[string]any) (any, bool) {
	if len(output) == 0 {
		return nil, false
	}
	if raw, exists := output["output"]; exists {
		switch value := raw.(type) {
		case map[string]any:
			return value, true
		case string:
			if strings.TrimSpace(value) == "" {
				return nil, false
			}
			decoder := json.NewDecoder(strings.NewReader(value))
			var decoded any
			if err := decoder.Decode(&decoded); err != nil {
				return nil, false
			}
			var trailing any
			if err := decoder.Decode(&trailing); err != io.EOF {
				return nil, false
			}
			if decodedMap, ok := decoded.(map[string]any); ok {
				if nested, exists := decodedMap["output"]; exists {
					return nested, nested != nil
				}
			}
			return decoded, decoded != nil
		default:
			return nil, false
		}
	}
	return output, true
}

func validMemoryListResult(data map[string]any) bool {
	if path, exists := data["path"]; exists && path != nil {
		if _, ok := path.(string); !ok {
			return false
		}
	}
	if _, ok := data["truncated"].(bool); !ok {
		return false
	}
	if cursor, exists := data["next_cursor"]; exists && cursor != nil {
		if _, ok := cursor.(string); !ok {
			return false
		}
	}
	entries, exists := data["entries"]
	if !exists {
		return false
	}
	if entries == nil {
		return true
	}
	items := sliceFromAny(entries)
	if entries != nil && items == nil {
		return false
	}
	for _, entry := range items {
		item := mapFromAny(entry)
		if _, ok := item["path"].(string); !ok {
			return false
		}
		if _, ok := item["entry_type"].(string); !ok {
			return false
		}
	}
	return true
}

// validMemoryAddNoteResult accepts the note-write result shape: the stored
// path, and nothing else. Sessions recorded before the tool returned a path
// carry an empty object, so that stays valid too and old transcripts still
// replay as the note body rather than as an error.
func validMemoryAddNoteResult(data map[string]any) bool {
	if len(data) == 0 {
		return true
	}
	if len(data) != 1 {
		return false
	}
	path, ok := data["path"].(string)
	return ok && strings.TrimSpace(path) != ""
}

func validMemoryReadResult(data map[string]any) bool {
	path, pathOK := data["path"].(string)
	_, contentOK := data["content"].(string)
	_, startOK := integerValue(data["start_line_number"])
	_, truncatedOK := data["truncated"].(bool)
	return pathOK && strings.TrimSpace(path) != "" && contentOK && startOK && truncatedOK
}

func validMemorySearchResult(data map[string]any) bool {
	if path, exists := data["path"]; exists && path != nil {
		if _, ok := path.(string); !ok {
			return false
		}
	}
	queriesValue, queriesOK := data["queries"]
	matches, matchesOK := data["matches"]
	queries := stringSliceFromAny(queriesValue)
	if !queriesOK || !matchesOK || queriesValue == nil || len(queries) == 0 {
		return false
	}
	for _, query := range queries {
		if strings.TrimSpace(query) == "" {
			return false
		}
	}
	if matches == nil {
		return true
	}
	items := sliceFromAny(matches)
	if matches != nil && items == nil {
		return false
	}
	for _, match := range items {
		item := mapFromAny(match)
		path, pathOK := item["path"].(string)
		_, contentOK := item["content"].(string)
		_, lineOK := integerValue(item["match_line_number"])
		if !pathOK || strings.TrimSpace(path) == "" || !contentOK || !lineOK {
			return false
		}
	}
	return true
}

func integerValue(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case int32:
		return int(number), true
	case int64:
		return int(number), true
	case float64:
		return int(number), number == float64(int(number))
	case float32:
		return int(number), number == float32(int(number))
	default:
		return 0, false
	}
}

func formatMemoryListResult(result any, maxBytes int) (string, bool) {
	data := mapFromAny(result)
	path := memoryDisplayPath(stringFromAny(data["path"]))
	entries := sliceFromAny(data["entries"])
	var sb strings.Builder
	fmt.Fprintf(&sb, "**Memories in %s**", markdownCode(path))
	if len(entries) == 0 {
		sb.WriteString("\n\nNo files or folders found.")
	} else {
		for _, entry := range entries {
			item := mapFromAny(entry)
			entryPath := strings.TrimSpace(stringFromAny(item["path"]))
			label := "file"
			if strings.EqualFold(strings.TrimSpace(stringFromAny(item["entry_type"])), "directory") {
				label = "folder"
			}
			fmt.Fprintf(&sb, "\n\n- %s · %s", markdownCode(entryPath), label)
		}
	}
	appendMemoryPagination(&sb, data)
	return clampBody(sb.String(), maxBytes)
}

func formatMemoryReadResult(result any, maxBytes int) (string, bool) {
	data := mapFromAny(result)
	path := memoryDisplayPath(stringFromAny(data["path"]))
	start := intFromAny(data["start_line_number"])
	content := strings.TrimRight(stringFromAny(data["content"]), "\n")
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s · starting at line %d", markdownCode(path), start)
	if content == "" {
		sb.WriteString("\n\nThis file is empty.")
	} else {
		sb.WriteString("\n\n")
		sb.WriteString(content)
	}
	if boolFromAny(data["truncated"]) {
		sb.WriteString("\n\n_Only part of this file is shown. Read again with a narrower line range._")
	}
	return clampBody(sb.String(), maxBytes)
}

// formatMemorySearchResult renders what the search found, and nothing else.
//
// The queries and the file they were searched in are already on the header, so
// repeating them here would say the same thing twice. A search that matched
// nothing therefore has no body at all: an empty result hands every surface its
// own shared empty-output placeholder, which is what a reader recognizes as
// "this call produced nothing", instead of a sentence only this one tool uses.
//
// What the header cannot say is which query recalled which memory: a call
// carries several queries and each hit answers to only some of them. So the
// matched text emphasizes the queries where they occur in it, and the reader
// sees what recalled the memory in the memory itself. input carries the call's
// case and normalization settings, which decide what counts as an occurrence.
func formatMemorySearchResult(result any, maxBytes int) (string, bool) {
	data := mapFromAny(result)
	matches := sliceFromAny(data["matches"])
	if len(matches) == 0 {
		return "", false
	}
	allQueries := stringSliceFromAny(data["queries"])
	var sb strings.Builder
	for index, match := range matches {
		item := mapFromAny(match)
		path := memoryDisplayPath(stringFromAny(item["path"]))
		line := intFromAny(item["match_line_number"])
		// matched_terms are the terms this window shares with the queries, and
		// so the text that actually occurs in it. matched_queries is the
		// fallback for a payload that predates them, and the whole query list
		// for one that carries neither.
		hits := stringSliceFromAny(item["matched_terms"])
		if len(hits) == 0 {
			hits = stringSliceFromAny(item["matched_queries"])
		}
		if len(hits) == 0 {
			hits = allQueries
		}
		if index > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(markdownCode(path))
		if line > 0 {
			fmt.Fprintf(&sb, " · line %d", line)
		}
		if content := strings.TrimRight(stringFromAny(item["content"]), "\n"); content != "" {
			sb.WriteString("\n\n")
			lines := strings.Split(content, "\n")
			for index, line := range lines {
				if index > 0 {
					sb.WriteByte('\n')
				}
				sb.WriteString("> ")
				sb.WriteString(emphasizeMatches(line, hits))
				// One line of the file is one line of the quote. Without an
				// explicit break the renderer folds the whole window into a
				// single paragraph, and a window of several lines comes back
				// run together with spaces where the file had newlines.
				if index < len(lines)-1 {
					sb.WriteString(markdownHardBreak)
				}
			}
		}
	}
	return clampBody(sb.String(), maxBytes)
}

// emphasizeMatches marks every occurrence of terms in line. Locating them is
// memory.MatchRanges' job, so a surface can never mark by a different rule than
// the search matched by.
//
// The line is quoted evidence, so every part of it — marked and unmarked alike
// — is escaped to render as the bytes the file holds, and the mark this adds is
// the only markup in it.
func emphasizeMatches(line string, terms []string) string {
	ranges := memory.MatchRanges(line, terms)
	var out strings.Builder
	out.Grow(len(line) + 8)
	cursor := 0
	for _, span := range ranges {
		if span[0] < cursor || span[1] > len(line) {
			continue
		}
		out.WriteString(markdownLiteral(line[cursor:span[0]], cursor == 0))
		out.WriteString(markdownMark(markdownLiteral(line[span[0]:span[1]], span[0] == 0)))
		cursor = span[1]
	}
	out.WriteString(markdownLiteral(line[cursor:], cursor == 0))
	return out.String()
}

// markdownHardBreak ends a line of quoted evidence. A trailing backslash is the
// one spelling of a Markdown hard break that survives trailing-whitespace
// trimming anywhere on the way to a renderer.
const markdownHardBreak = "\\"

// markdownInlineSyntax are the characters that begin an inline Markdown
// construct wherever they appear.
const markdownInlineSyntax = "\\`*_[]<&~|"

// markdownBlockSyntax are the characters that begin a block construct, and only
// at the start of a line.
const markdownBlockSyntax = "#>-+="

// markdownLiteral escapes text so that a renderer prints the text itself.
//
// A quoted memory line is an exact slice of a file, and files are full of
// characters Markdown reads as markup. A path with a snake_case segment is the
// case that gives the game away: "/…/chatibs_claw_client" holds two
// underscores, a renderer reads them as emphasis and removes them, and the
// reader is shown "/…/chatibsclawclient" — a path that does not exist, quoted
// as though the file said it. Evidence has to be the bytes it came from, so
// every character that could be read as markup is escaped and only the mark
// this package adds is left as markup.
//
// atLineStart adds the characters that begin a construct only there: the same
// hyphen that is inert mid-sentence starts a list at the head of a line, and a
// leading number followed by "." or ")" starts a numbered one.
func markdownLiteral(text string, atLineStart bool) string {
	if text == "" {
		return ""
	}
	var out strings.Builder
	out.Grow(len(text) + 8)
	escapeAt := -1
	if atLineStart {
		escapeAt = markdownBlockMarker(text)
	}
	for index, char := range text {
		if index == escapeAt || strings.ContainsRune(markdownInlineSyntax, char) {
			out.WriteByte('\\')
		}
		out.WriteRune(char)
	}
	return out.String()
}

// markdownBlockMarker returns the byte offset of the character that would make
// the head of text a block marker, or -1 when it would not. A backslash only
// escapes ASCII punctuation, so for "12." it is the dot that has to carry the
// escape rather than the digit that starts it.
func markdownBlockMarker(text string) int {
	if text == "" {
		return -1
	}
	if strings.IndexByte(markdownBlockSyntax, text[0]) >= 0 {
		return 0
	}
	digits := 0
	for digits < len(text) && text[digits] >= '0' && text[digits] <= '9' {
		digits++
	}
	if digits > 0 && digits < len(text) && (text[digits] == '.' || text[digits] == ')') {
		return digits
	}
	return -1
}

// markdownMark marks a span of quoted evidence with strong emphasis, which a
// surface draws in its own mark colour.
//
// Strong emphasis is the one inline construct that parses wherever a matched
// line puts it, including right against Chinese text. It must not trim the
// span: it is an exact slice of the line, and trimming would show the reader
// different text than the file holds. The value it wraps is already escaped,
// so the only markup in a quoted line is the pair of asterisks added here.
func markdownMark(value string) string {
	return "**" + value + "**"
}

func markdownCode(value string) string {
	value = strings.TrimSpace(value)
	fence := "`"
	for strings.Contains(value, fence) {
		fence += "`"
	}
	return fence + value + fence
}

func appendMemoryPagination(sb *strings.Builder, data map[string]any) {
	if sb == nil {
		return
	}
	if cursor := strings.TrimSpace(stringFromAny(data["next_cursor"])); cursor != "" {
		fmt.Fprintf(sb, "\n\n_More results are available. Continue with cursor %s._", markdownCode(cursor))
	} else if boolFromAny(data["truncated"]) {
		sb.WriteString("\n\n_More results are available. Continue with the next cursor._")
	}
}

// MemorySearchQueryLabel renders the queries of a memories_search call as a
// quoted list for tool headers, so a header never has to fall back to the raw
// argument JSON.
//
// Every query is listed in full. A header is where the reader checks what was
// actually asked for, so a surface that cannot fit the list wraps it onto more
// rows; dropping the tail behind an ellipsis would leave a search that matched
// nothing looking like it had asked something it never asked.
func MemorySearchQueryLabel(input map[string]any) string {
	queries := stringSliceFromAny(input["queries"])
	quoted := make([]string, 0, len(queries))
	for _, query := range queries {
		if q := strings.Join(strings.Fields(strings.TrimSpace(query)), " "); q != "" {
			quoted = append(quoted, strconv.Quote(q))
		}
	}
	return strings.Join(quoted, ", ")
}

// MemorySearchScopeLabel names the file or folder a memories_search call was
// restricted to, as the header suffix "in <path>", and "" for a search of the
// whole store.
//
// Without it a scoped search that matched nothing reads as "memory holds
// nothing about this" when it only means "this one file holds nothing about
// this" — which is exactly how a stale MEMORY.md was once mistaken for an
// empty memory store.
//
// It is the whole suffix of a memories_search header: the window around each
// hit is the search's own now, so there is no argument left for the header to
// report or contradict.
func MemorySearchScopeLabel(input map[string]any) string {
	path := strings.TrimSpace(firstString(input, "path"))
	if path == "" {
		return ""
	}
	return "in " + path
}

// joinHeaderParts builds a tool header from its segments without imposing a
// length cap: headers wrap, they do not truncate.
func joinHeaderParts(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.Join(strings.Fields(strings.TrimSpace(part)), " "); part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, " ")
}

// MemoryReadLineRange converts a memories_read call's 1-indexed line_offset and
// max_lines into the inclusive line range shown in completed tool headers. ok is
// false when the call did not bound the read.
func MemoryReadLineRange(input map[string]any) (lo, hi int, ok bool) {
	limit := intFromAny(input["max_lines"])
	if limit <= 0 {
		return 0, 0, false
	}
	offset := intFromAny(input["line_offset"])
	if offset < 1 {
		offset = 1
	}
	return offset, offset + limit - 1, true
}

// memoryStepSummary describes a completed memory tool call by its outcome
// (`searched memories "deploy" in MEMORY.md · 3 matches`). The generic summary
// chain would otherwise repeat the path already in the invocation label, and
// result counts are only claimed when the payload was actually readable.
//
// None of these summaries is length-capped: the call's own arguments are what
// the reader is checking, and a surface that cannot fit them wraps.
func memoryStepSummary(evt StepEvent, tool string) (string, bool) {
	var data map[string]any
	if result, recognized := memoryResultPayload(evt.Output); recognized {
		data = mapFromAny(result)
	}
	countSuffix := func(key, singular, plural string) string {
		items, exists := data[key]
		if !exists {
			return ""
		}
		return formatCountLabel(len(sliceFromAny(items)), singular, plural)
	}
	withCount := func(label, count string) string {
		if count == "" {
			return label
		}
		return label + " · " + count
	}
	path := firstString(evt.Input, "path")
	switch tool {
	case "memories_list":
		label := joinHeaderParts("listed memories", path)
		return withCount(label, countSuffix("entries", "entry", "entries")), true
	case "memories_read":
		label := joinHeaderParts("read memory", path)
		lineRange := ""
		if lo, hi, ok := MemoryReadLineRange(evt.Input); ok {
			lineRange = fmt.Sprintf("lines %d-%d", lo, hi)
		}
		return withCount(label, lineRange), true
	case "memories_search":
		label := joinHeaderParts("searched memories", MemorySearchQueryLabel(evt.Input), MemorySearchScopeLabel(evt.Input))
		return withCount(label, countSuffix("matches", "match", "matches")), true
	default:
		return "", false
	}
}

func memoryDisplayPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "your memory store"
	}
	return path
}

func mapFromAny(value any) map[string]any {
	switch data := value.(type) {
	case map[string]any:
		return data
	case nil:
		return map[string]any{}
	default:
		encoded, err := json.Marshal(data)
		if err != nil {
			return map[string]any{}
		}
		var out map[string]any
		if json.Unmarshal(encoded, &out) != nil {
			return map[string]any{}
		}
		return out
	}
}

func sliceFromAny(value any) []any {
	switch items := value.(type) {
	case []any:
		return items
	case nil:
		return nil
	default:
		encoded, err := json.Marshal(items)
		if err != nil {
			return nil
		}
		var out []any
		if json.Unmarshal(encoded, &out) != nil {
			return nil
		}
		return out
	}
}

func stringSliceFromAny(value any) []string {
	items := sliceFromAny(value)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text := strings.TrimSpace(stringFromAny(item)); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func formatGenericToolStep(evt StepEvent, tool string, maxBytes int) (string, bool) {
	sb := strings.Builder{}
	if len(evt.Output) > 0 {
		if diffSection := turnDiffSection(evt.Output); diffSection != "" {
			sb.WriteString(diffSection)
			sb.WriteString("\n\n")
		}
		if diagSection := lspDiagnosticsSection(evt.Output); diagSection != "" {
			sb.WriteString(diagSection)
			sb.WriteString("\n\n")
		}
		if preview := strings.TrimSpace(stringFromAny(evt.Output["stdout_preview"])); preview != "" {
			sb.WriteString("stdout preview:\n\n```text\n")
			sb.WriteString(preview)
			if !strings.HasSuffix(preview, "\n") {
				sb.WriteString("\n")
			}
			sb.WriteString("```\n\n")
		}
		if body := strings.TrimSpace(stringFromAny(evt.Output["preview_text"])); body != "" {
			sb.WriteString("result preview:\n\n```text\n")
			sb.WriteString(body)
			if !strings.HasSuffix(body, "\n") {
				sb.WriteString("\n")
			}
			sb.WriteString("```\n\n")
		}
		if note := omittedBytesNote(evt.Output); note != "" {
			sb.WriteString(note)
			sb.WriteString("\n\n")
		}
		renderOutput := compactExplainableOutput(outputWithout(evt.Output, "lsp_diagnostics"))
		if len(renderOutput) > 0 {
			if b, err := json.MarshalIndent(renderOutput, "", "  "); err == nil {
				sb.WriteString("output:\n\n```json\n")
				sb.WriteString(string(b))
				sb.WriteString("\n```")
			}
		}
	}
	return clampBody(sb.String(), maxBytes)
}

// formatRetrieveOutputStep unwraps the recovered result from the generic tool
// transport envelope. The recovered value can itself be Markdown (for example
// the heading and matched line groups returned by a query), so callers must
// receive it verbatim rather than an `output: {"output": ...}` JSON block.
func formatRetrieveOutputStep(evt StepEvent, maxBytes int) (string, bool) {
	body := strings.TrimSpace(stringFromAny(evt.Output["output"]))
	return clampBody(body, maxBytes)
}

// formatLSPStep wraps the text the lsp tool returned — the same text the
// model received — in a plain code block. The structured Display fields ride
// on the step for the title and the summary, not the body.
func formatLSPStep(evt StepEvent, maxBytes int) (string, bool) {
	body := strings.TrimSpace(stringFromAny(evt.Output["output"]))
	if body == "" {
		return "", false
	}
	return clampBody("```text\n"+body+"\n```", maxBytes)
}

// lspStepSummary is the completed lsp summary line: the operation that ran
// and how many results it found. The operation is read from the captured
// output (already normalized) and falls back to the call's argument, so a
// step replayed from input alone still names what was looked up.
func lspStepSummary(evt StepEvent) string {
	operation := strings.TrimSpace(stringFromAny(evt.Output["operation"]))
	if operation == "" {
		operation = strings.ToLower(strings.TrimSpace(stringFromAny(evt.Input["operation"])))
	}
	if operation == "" {
		return ""
	}
	if n, ok := firstInt(evt.Output, "result_count"); ok {
		return "looked up " + operation + " · " + formatCountLabel(n, "result", "results")
	}
	return "looked up " + operation
}

type webSearchDisplayResult struct {
	Title     string
	URL       string
	Snippet   string
	Published string
}

type webSearchDisplay struct {
	Status    int
	Truncated bool
	Error     string
	Results   []webSearchDisplayResult
}

func formatWebFetchStep(evt StepEvent, maxBytes int) (string, bool) {
	raw, err := json.Marshal(evt.Output)
	if err != nil {
		return "Web fetch completed, but its content could not be displayed.", false
	}
	body, recognized := FormatWebFetchResult(string(raw))
	if !recognized {
		body = "Web fetch completed, but its content could not be displayed."
	}
	return clampBody(body, maxBytes)
}

// FormatWebFetchResult extracts the human-facing Markdown body from the nested
// web_fetch transport envelope. The tool returns a JSON string, which the step
// event wraps once more under `output`; transport metadata must not reach the
// completion card.
func FormatWebFetchResult(content string) (string, bool) {
	raw := strings.TrimSpace(content)
	if fenced := firstFencedBody(raw); fenced != "" {
		raw = fenced
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return "", false
	}
	return webFetchBodyFromValue(value, 0)
}

const maxWebFetchEnvelopeDepth = 5

func webFetchBodyFromValue(value any, depth int) (string, bool) {
	if depth >= maxWebFetchEnvelopeDepth {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		var nested any
		if json.Unmarshal([]byte(strings.TrimSpace(typed)), &nested) != nil {
			return "", false
		}
		return webFetchBodyFromValue(nested, depth+1)
	case map[string]any:
		// Step events wrap a tool's textual return value in `output`.
		if output, ok := typed["output"]; ok {
			return webFetchBodyFromValue(output, depth+1)
		}
		// The web_fetch tool's body is already Markdown (or plain text for a
		// non-HTML response). Return it verbatim; the renderer owns Markdown.
		if body, ok := typed["body"]; ok {
			text, valid := body.(string)
			return text, valid
		}
	}
	return "", false
}

func formatWebSearchStep(evt StepEvent, maxBytes int) (string, bool) {
	raw, err := json.Marshal(evt.Output)
	if err != nil {
		return "Web search completed, but its results could not be displayed.", false
	}
	body, recognized := FormatWebSearchResult(string(raw))
	if !recognized || strings.TrimSpace(body) == "" {
		body = "Web search completed, but its results could not be displayed."
	}
	return clampBody(body, maxBytes)
}

// FormatWebSearchResult converts the nested web_search transport and provider
// payloads into a compact result list. It intentionally never returns the raw
// JSON payload: provider metadata is useful to the model, but not to a person
// reading the TUI transcript.
func FormatWebSearchResult(content string) (string, bool) {
	raw := strings.TrimSpace(content)
	if fenced := firstFencedBody(raw); fenced != "" {
		raw = fenced
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return "", false
	}
	display, recognized := webSearchDisplayFromValue(value, 0)
	if !recognized {
		return "", false
	}
	return renderWebSearchDisplay(display), true
}

const maxWebSearchEnvelopeDepth = 5

func webSearchDisplayFromValue(value any, depth int) (webSearchDisplay, bool) {
	if depth >= maxWebSearchEnvelopeDepth {
		return webSearchDisplay{}, false
	}
	switch typed := value.(type) {
	case string:
		var nested any
		if json.Unmarshal([]byte(strings.TrimSpace(typed)), &nested) != nil {
			return webSearchDisplay{}, false
		}
		return webSearchDisplayFromValue(nested, depth+1)
	case []any:
		return webSearchDisplay{Results: webSearchResultsFromItems(typed)}, true
	case map[string]any:
		// Step events wrap a tool's textual return value in `output`.
		if output, ok := typed["output"]; ok {
			if display, nestedOK := webSearchDisplayFromValue(output, depth+1); nestedOK {
				return display, true
			}
			return webSearchDisplay{}, true
		}

		// The local web_search tool wraps the provider response in `body` and
		// keeps transport-only fields alongside it.
		if body, ok := typed["body"]; ok {
			display, _ := webSearchDisplayFromValue(body, depth+1)
			display.Status = intFromAny(typed["status"])
			display.Truncated, _ = typed["truncated"].(bool)
			if display.Error == "" {
				display.Error = webSearchErrorFromMap(typed)
			}
			return display, true
		}

		items, found := webSearchResultItems(typed, depth)
		if found {
			return webSearchDisplay{
				Error:   webSearchErrorFromMap(typed),
				Results: webSearchResultsFromItems(items),
			}, true
		}
		if message := webSearchErrorFromMap(typed); message != "" {
			return webSearchDisplay{Error: message}, true
		}
	}
	return webSearchDisplay{}, false
}

func webSearchResultItems(payload map[string]any, depth int) ([]any, bool) {
	for _, key := range []string{"results", "references", "search_results", "items", "value"} {
		value, ok := payload[key]
		if !ok {
			continue
		}
		items, ok := value.([]any)
		if ok {
			return items, true
		}
		return nil, true
	}
	if depth+1 >= maxWebSearchEnvelopeDepth {
		return nil, false
	}
	for _, key := range []string{"web", "webPages", "data", "result", "search_result"} {
		nested, ok := payload[key].(map[string]any)
		if !ok {
			continue
		}
		if items, found := webSearchResultItems(nested, depth+1); found {
			return items, true
		}
	}
	return nil, false
}

func webSearchResultsFromItems(items []any) []webSearchDisplayResult {
	results := make([]webSearchDisplayResult, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		result := webSearchDisplayResult{
			Title:     webSearchText(firstString(item, "title", "name"), 180),
			URL:       webSearchText(firstString(item, "url", "link"), 500),
			Snippet:   webSearchText(firstString(item, "content", "description", "snippet", "summary"), 360),
			Published: webSearchText(firstString(item, "published_date", "page_age", "date", "age"), 100),
		}
		if result.Title == "" && result.URL == "" && result.Snippet == "" {
			continue
		}
		key := result.URL
		if key == "" {
			key = result.Title + "\x00" + result.Snippet
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		results = append(results, result)
	}
	return results
}

func renderWebSearchDisplay(display webSearchDisplay) string {
	if display.Status >= 400 {
		message := fmt.Sprintf("Web search failed (HTTP %d).", display.Status)
		if display.Error != "" {
			message += " " + display.Error
		}
		return message
	}
	if len(display.Results) == 0 {
		if display.Error != "" {
			return "Web search could not be completed: " + display.Error
		}
		return "No search results found."
	}

	var sb strings.Builder
	sb.WriteString("**")
	sb.WriteString(formatCountLabel(len(display.Results), "search result", "search results"))
	sb.WriteString("**")
	for i, result := range display.Results {
		title := result.Title
		if title == "" {
			title = result.URL
		}
		if title == "" {
			title = fmt.Sprintf("Result %d", i+1)
		}
		sb.WriteString(fmt.Sprintf("\n\n%d. ", i+1))
		if result.URL != "" {
			sb.WriteString("[")
			sb.WriteString(webSearchMarkdownText(title))
			sb.WriteString("](")
			sb.WriteString(webSearchMarkdownURL(result.URL))
			sb.WriteString(")")
		} else {
			sb.WriteString("**")
			sb.WriteString(webSearchMarkdownText(title))
			sb.WriteString("**")
		}
		if result.Published != "" {
			sb.WriteString("\n   _Published: ")
			sb.WriteString(webSearchMarkdownText(result.Published))
			sb.WriteString("_")
		}
		if result.Snippet != "" {
			sb.WriteString("\n   ")
			sb.WriteString(webSearchMarkdownText(result.Snippet))
		}
	}
	if display.Truncated {
		sb.WriteString("\n\nSome provider response data was omitted because it was too large.")
	}
	return sb.String()
}

func webSearchMarkdownText(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"`", "\\`",
		"*", "\\*",
		"_", "\\_",
		"[", "\\[",
		"]", "\\]",
	)
	return replacer.Replace(value)
}

func webSearchMarkdownURL(value string) string {
	return strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)", " ", "%20").Replace(value)
}

func webSearchErrorFromMap(payload map[string]any) string {
	for _, key := range []string{"error", "message", "detail", "msg"} {
		value, ok := payload[key]
		if !ok {
			continue
		}
		if message, ok := value.(string); ok {
			if message = webSearchText(message, 300); message != "" {
				return message
			}
		}
		if nested, ok := value.(map[string]any); ok {
			if message := webSearchErrorFromMap(nested); message != "" {
				return message
			}
		}
	}
	return ""
}

func webSearchText(value string, maxRunes int) string {
	value = html.UnescapeString(stripWebSearchHTML(value))
	clean := make([]rune, 0, len(value))
	for _, r := range value {
		if unicode.IsControl(r) {
			clean = append(clean, ' ')
			continue
		}
		clean = append(clean, r)
	}
	value = strings.Join(strings.Fields(string(clean)), " ")
	runes := []rune(value)
	if maxRunes > 0 && len(runes) > maxRunes {
		value = string(runes[:maxRunes-1]) + "…"
	}
	return value
}

func stripWebSearchHTML(value string) string {
	var out strings.Builder
	inTag := false
	for _, r := range value {
		switch r {
		case '<':
			inTag = true
		case '>':
			if inTag {
				inTag = false
				continue
			}
			out.WriteRune(r)
		default:
			if !inTag {
				out.WriteRune(r)
			}
		}
	}
	return out.String()
}

func formatIntermediateToolStep(evt StepEvent, maxBytes int) (string, bool) {
	action := strings.ToLower(strings.TrimSpace(firstString(evt.Input, "action")))
	if action == "" {
		action = strings.ToLower(strings.TrimSpace(stringFromAny(evt.Output["action"])))
	}
	switch action {
	case "", "append":
		// The notes are Markdown authored by the model. Return them verbatim so
		// the TUI viewport renders them as Markdown instead of a labeled blob.
		// Prefer the input payload; fall back to the echoed output content.
		// intermediate_tool notes are the model's own saved findings, read back
		// before finishing - never byte-clamp them, show the full body.
		if txt := strings.TrimSpace(firstString(evt.Input, "content")); txt != "" {
			return txt, false
		}
		if txt := strings.TrimSpace(stringFromAny(evt.Output["content"])); txt != "" {
			return txt, false
		}
		return "", false
	case "read":
		content := strings.TrimSpace(stringFromAny(evt.Output["content"]))
		if content == "" {
			return "No saved notes available.", false
		}
		return clampBody(content, maxBytes)
	case "clear":
		return clampBody("Cleared intermediate notes.", maxBytes)
	default:
		return formatGenericToolStep(evt, "intermediate_tool", maxBytes)
	}
}

// formatExitPlanModeStep renders the human-facing result of exit_plan_mode.
// The tool returns a JSON envelope (wrapped under the "output" key as a string)
// carrying a human-readable "message" plus the restored "mode" and "plan_file".
// Extract just the message so the TUI shows a clean confirmation line instead
// of an escaped JSON blob with an "output:" label.
func formatExitPlanModeStep(evt StepEvent, maxBytes int) (string, bool) {
	raw := strings.TrimSpace(stringFromAny(evt.Output["output"]))
	if raw == "" {
		return "", false
	}
	var parsed struct {
		Message  string `json:"message"`
		Mode     string `json:"mode"`
		PlanFile string `json:"plan_file"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		// Unexpected payload shape - fall back to the generic renderer rather
		// than hiding the result entirely.
		return formatGenericToolStep(evt, "exit_plan_mode", maxBytes)
	}
	body := strings.TrimSpace(parsed.Message)
	if body == "" {
		return "", false
	}
	return clampBody(body, maxBytes)
}

func formatUserInteractionStep(evt StepEvent, maxBytes int) (string, bool) {
	payload, err := json.Marshal(evt.Output)
	if err == nil {
		if body, ok := FormatUserInteractionResult(string(payload), evt.Input); ok {
			return clampBody(body, maxBytes)
		}
	}
	// Preserve unexpected and historical payloads for diagnostics instead of
	// silently turning a completed interaction into an empty card.
	return formatGenericToolStep(evt, "user_interaction", maxBytes)
}

// FormatUserInteractionResult converts a user_interaction result into a compact
// question-and-answer list. Content may be the direct result JSON, a textual
// "output" envelope, or a generic notification body containing fenced JSON.
// The bool is false when the content is not a recognized interaction result.
func FormatUserInteractionResult(content string, input map[string]any) (string, bool) {
	answers, ok := parseUserInteractionAnswers(content, 0)
	if !ok {
		return "", false
	}

	orderedHeaders := userInteractionQuestionHeaders(input)
	normalizedAnswers := make(map[string]userInteractionAnswer, len(answers))
	answerHeaders := make(map[string]string, len(answers))
	for header, answer := range answers {
		header = strings.TrimSpace(header)
		if header == "" {
			continue
		}
		key := strings.ToLower(header)
		if _, exists := normalizedAnswers[key]; exists {
			continue
		}
		normalizedAnswers[key] = answer
		answerHeaders[key] = header
	}

	seen := make(map[string]struct{}, len(orderedHeaders))
	entries := make([]userInteractionDisplayEntry, 0, len(orderedHeaders)+len(normalizedAnswers))
	for _, header := range orderedHeaders {
		key := strings.ToLower(header)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		entries = append(entries, userInteractionDisplayEntry{
			Header: header,
			Answer: normalizedAnswers[key],
		})
	}

	extraKeys := make([]string, 0, len(normalizedAnswers))
	for key := range normalizedAnswers {
		if _, exists := seen[key]; !exists {
			extraKeys = append(extraKeys, key)
		}
	}
	sort.Slice(extraKeys, func(i, j int) bool {
		return strings.ToLower(answerHeaders[extraKeys[i]]) < strings.ToLower(answerHeaders[extraKeys[j]])
	})
	for _, key := range extraKeys {
		entries = append(entries, userInteractionDisplayEntry{
			Header: answerHeaders[key],
			Answer: normalizedAnswers[key],
		})
	}
	if len(entries) == 0 {
		return "", false
	}

	var sb strings.Builder
	for i, entry := range entries {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(entry.Header)
		answerCount := 0
		for _, selection := range entry.Answer.Selections {
			if text := strings.TrimSpace(selection); text != "" {
				appendUserInteractionAnswer(&sb, text)
				answerCount++
			}
		}
		if text := strings.TrimSpace(entry.Answer.Other); text != "" {
			appendUserInteractionAnswer(&sb, text)
			answerCount++
		}
		if answerCount == 0 {
			appendUserInteractionAnswer(&sb, "(no answer)")
		}
	}
	return sb.String(), true
}

type userInteractionAnswer struct {
	Selections []string `json:"selections"`
	Other      string   `json:"other"`
}

type userInteractionDisplayEntry struct {
	Header string
	Answer userInteractionAnswer
}

func parseUserInteractionAnswers(content string, depth int) (map[string]userInteractionAnswer, bool) {
	if depth >= 5 {
		return nil, false
	}
	payload := strings.TrimSpace(content)
	if payload == "" {
		return nil, false
	}
	if fenced := firstFencedBody(payload); fenced != "" {
		payload = fenced
	}

	var value any
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		return nil, false
	}
	return userInteractionAnswersFromValue(value, depth)
}

func userInteractionAnswersFromValue(value any, depth int) (map[string]userInteractionAnswer, bool) {
	if depth >= 5 {
		return nil, false
	}
	switch typed := value.(type) {
	case string:
		return parseUserInteractionAnswers(typed, depth+1)
	case map[string]any:
		if rawAnswers, exists := typed["answers"]; exists {
			encoded, err := json.Marshal(rawAnswers)
			if err != nil {
				return nil, false
			}
			answers := map[string]userInteractionAnswer{}
			if string(encoded) != "null" {
				if err := json.Unmarshal(encoded, &answers); err != nil {
					return nil, false
				}
			}
			return answers, true
		}
		if output, exists := typed["output"]; exists {
			return userInteractionAnswersFromValue(output, depth+1)
		}
	}
	return nil, false
}

// userInteractionLabel is a user_interaction call's header: the verb for its
// state, then what it asks.
func userInteractionLabel(verb string, evt StepEvent) string {
	if questions := UserInteractionQuestionsLabel(evt.Input); questions != "" {
		return verb + " · " + questions
	}
	return verb
}

// UserInteractionQuestionsLabel names what a user_interaction call asks, in
// the order it asks it: each question's short header, or the question itself
// when it has none. It is the call's parameters in words — the card header both
// surfaces show — and every question is named in full.
func UserInteractionQuestionsLabel(input map[string]any) string {
	if len(input) == 0 {
		return ""
	}
	encoded, err := json.Marshal(input["questions"])
	if err != nil {
		return ""
	}
	var questions []struct {
		Header   string `json:"header"`
		Question string `json:"question"`
	}
	if err := json.Unmarshal(encoded, &questions); err != nil {
		return ""
	}
	names := make([]string, 0, len(questions))
	for _, question := range questions {
		name := strings.TrimSpace(question.Header)
		if name == "" {
			name = strings.Join(strings.Fields(question.Question), " ")
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, " · ")
}

func userInteractionQuestionHeaders(input map[string]any) []string {
	if len(input) == 0 {
		return nil
	}
	encoded, err := json.Marshal(input["questions"])
	if err != nil {
		return nil
	}
	var questions []struct {
		Header string `json:"header"`
	}
	if err := json.Unmarshal(encoded, &questions); err != nil {
		return nil
	}
	headers := make([]string, 0, len(questions))
	for _, question := range questions {
		if header := strings.TrimSpace(question.Header); header != "" {
			headers = append(headers, header)
		}
	}
	return headers
}

func appendUserInteractionAnswer(sb *strings.Builder, answer string) {
	lines := strings.Split(strings.ReplaceAll(answer, "\r", ""), "\n")
	if len(lines) == 0 {
		return
	}
	sb.WriteString("\n  → ")
	sb.WriteString(lines[0])
	for _, line := range lines[1:] {
		sb.WriteString("\n    ")
		sb.WriteString(line)
	}
}

func firstFencedBody(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r", ""), "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "```") {
			continue
		}
		body := make([]string, 0, len(lines)-i-1)
		for _, candidate := range lines[i+1:] {
			if strings.HasPrefix(strings.TrimSpace(candidate), "```") {
				return strings.TrimSpace(strings.Join(body, "\n"))
			}
			body = append(body, candidate)
		}
		return strings.TrimSpace(strings.Join(body, "\n"))
	}
	return ""
}

// formatWorkingSetPinStep renders the pinned working set as a readable list.
// The tool result is a JSON object wrapped as a string under the "output" key;
// parsing it here avoids exposing the transport envelope in the TUI.
func formatWorkingSetPinStep(evt StepEvent, maxBytes int) (string, bool) {
	raw := strings.TrimSpace(stringFromAny(evt.Output["output"]))
	if raw == "" {
		return "", false
	}
	var parsed struct {
		Count int      `json:"count"`
		Pins  []string `json:"pins"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return formatGenericToolStep(evt, "working_set_pin", maxBytes)
	}
	count := parsed.Count
	if count == 0 && len(parsed.Pins) > 0 {
		count = len(parsed.Pins)
	}
	var sb strings.Builder
	if count == 0 {
		sb.WriteString("No working set entries are pinned.")
	} else {
		sb.WriteString(fmt.Sprintf("Pinned %s in the working set", formatCountLabel(count, "entry", "entries")))
		if count != len(parsed.Pins) && len(parsed.Pins) > 0 {
			sb.WriteString(fmt.Sprintf(" (%d shown)", len(parsed.Pins)))
		}
		sb.WriteString(":")
		for _, pin := range parsed.Pins {
			pin = strings.TrimSpace(pin)
			if pin == "" {
				continue
			}
			sb.WriteString("\n- `")
			sb.WriteString(pin)
			sb.WriteString("`")
		}
	}
	return clampBody(sb.String(), maxBytes)
}

// formatRequestPermissionsStep renders a request_permissions lifecycle event as
// a readable card. The request is always shown (whether or not it needed an
// interactive approval prompt); when the call was auto-approved — exempt from
// prompting by a concrete policy condition — the exemption reason is rendered
// so transcripts explain why no approval surface appeared. Denied requests
// show the denial reason.
func formatRequestPermissionsStep(evt StepEvent, maxBytes int) (string, bool) {
	var sb strings.Builder
	sb.WriteString("**request_permissions**\n\n")

	status := strings.TrimSpace(stringFromAny(evt.Output["approval_status"]))
	reason := strings.TrimSpace(stringFromAny(evt.Output["approval_reason"]))

	if agentReason := strings.TrimSpace(stringFromAny(evt.Input["reason"])); agentReason != "" {
		sb.WriteString("reason: ")
		sb.WriteString(agentReason)
		sb.WriteString("\n\n")
	}

	if lines := requestPermissionEntryLines(evt.Output["permissions"]); lines != "" {
		sb.WriteString("requested:\n\n")
		sb.WriteString(lines)
		sb.WriteString("\n\n")
	}

	scope := strings.TrimSpace(stringFromAny(evt.Output["scope"]))

	switch strings.ToLower(status) {
	case "auto_approved":
		sb.WriteString("✔ auto-approved without an approval prompt")
		if reason != "" {
			sb.WriteString("\n\n")
			sb.WriteString("why: ")
			sb.WriteString(reason)
		}
	case "denied":
		sb.WriteString("✗ denied — no permissions were granted")
		if reason != "" {
			sb.WriteString("\n\n")
			sb.WriteString("why: ")
			sb.WriteString(reason)
		}
	case "approved":
		sb.WriteString("✔ approved")
		if reason != "" {
			sb.WriteString("\n\n")
			sb.WriteString("why: ")
			sb.WriteString(reason)
		}
	default:
		sb.WriteString("awaiting approval")
	}
	if scope != "" && strings.ToLower(status) != "denied" {
		sb.WriteString("\n\nscope: ")
		sb.WriteString(scope)
	}
	return clampBody(strings.TrimRight(sb.String(), "\n"), maxBytes)
}

// requestPermissionEntryLines renders a RequestPermissionProfile (carried in a
// step output map) as a bullet list of filesystem entries. Returns "" when the
// profile is missing or empty.
func requestPermissionEntryLines(raw any) string {
	profile, ok := requestPermissionProfileFromAny(raw)
	if !ok {
		return ""
	}
	var lines []string
	if profile.Network.AllowsNetwork() {
		lines = append(lines, "- network enabled")
	}
	if profile.FileSystem == nil {
		return strings.Join(lines, "\n")
	}
	for _, p := range profile.FileSystem.Read {
		if p = strings.TrimSpace(p); p != "" {
			lines = append(lines, "- read `"+p+"`")
		}
	}
	for _, p := range profile.FileSystem.Write {
		if p = strings.TrimSpace(p); p != "" {
			lines = append(lines, "- write `"+p+"`")
		}
	}
	for _, entry := range profile.FileSystem.Entries {
		path := requestPermissionPathLabel(entry.Path)
		if path == "" {
			continue
		}
		access := strings.TrimSpace(string(entry.Access))
		if access == "" {
			access = "read"
		}
		lines = append(lines, "- "+access+" `"+path+"`")
	}
	return strings.Join(lines, "\n")
}

func requestPermissionPathLabel(path safety.FileSystemPermissionPath) string {
	switch path.Type {
	case safety.FileSystemPermissionPathTypePath:
		return strings.TrimSpace(path.Path)
	case safety.FileSystemPermissionPathTypeGlobPattern:
		if pattern := strings.TrimSpace(path.Pattern); pattern != "" {
			return "glob " + pattern
		}
	case safety.FileSystemPermissionPathTypeSpecial:
		if path.Value == nil {
			return ""
		}
		base := ""
		switch path.Value.Kind {
		case safety.FileSystemSpecialPathRoot:
			base = ":root"
		case safety.FileSystemSpecialPathMinimal:
			base = ":minimal"
		case safety.FileSystemSpecialPathProjectRoots:
			base = ":workspace_roots"
		case safety.FileSystemSpecialPathTmpdir:
			base = ":tmpdir"
		case safety.FileSystemSpecialPathSlashTmp:
			base = "/tmp"
		case safety.FileSystemSpecialPathUnknown:
			base = strings.TrimSpace(path.Value.Path)
		}
		if subpath := strings.TrimSpace(path.Value.Subpath); subpath != "" {
			base = strings.TrimRight(base, "/\\") + "/" + subpath
		}
		return base
	}
	return strings.TrimSpace(path.Path)
}

func requestPermissionProfileFromAny(raw any) (safety.RequestPermissionProfile, bool) {
	if raw == nil {
		return safety.RequestPermissionProfile{}, false
	}
	if profile, ok := raw.(safety.RequestPermissionProfile); ok {
		return profile, true
	}
	if ptr, ok := raw.(*safety.RequestPermissionProfile); ok && ptr != nil {
		return *ptr, true
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return safety.RequestPermissionProfile{}, false
	}
	var profile safety.RequestPermissionProfile
	if err := json.Unmarshal(b, &profile); err != nil {
		return safety.RequestPermissionProfile{}, false
	}
	return profile, true
}

// formatFanoutStep formats the subagent_fanout task list as the expanded body.
// Each task is rendered as a line item with its title and a snippet of the prompt.
func formatFanoutStep(evt StepEvent, maxBytes int) (string, bool) {
	tasks := tasksFromInput(evt.Input)
	if len(tasks) == 0 {
		return clampBody("_(no tasks)_", maxBytes)
	}
	typeLabel := ""
	firstType := strings.TrimSpace(tasks[0].SubagentType)
	allSame := true
	for _, t := range tasks {
		if strings.TrimSpace(t.SubagentType) != firstType {
			allSame = false
			break
		}
	}
	if allSame && firstType != "" {
		typeLabel = " (" + firstType + ")"
	}

	sb := strings.Builder{}
	sb.WriteString(fmt.Sprintf("%d tasks%s:\n", len(tasks), typeLabel))
	for i, t := range tasks {
		title := strings.TrimSpace(t.Title)
		if title == "" {
			title = truncateSummary(strings.TrimSpace(t.Prompt))
		}
		promptSnippet := strings.TrimSpace(t.Prompt)
		if len(promptSnippet) > 100 {
			promptSnippet = llm.TruncateBytes(promptSnippet, 97, "...")
		}
		sb.WriteString(fmt.Sprintf("%d.", i+1))
		if title != "" {
			sb.WriteString(" ")
			sb.WriteString(title)
		}
		if promptSnippet != "" && promptSnippet != title {
			sb.WriteString(" — ")
			sb.WriteString(promptSnippet)
		}
		sb.WriteString("\n")
	}
	return clampBody(sb.String(), maxBytes)
}

// formatSubagentRunStep formats the subagent_run expanded body.
func formatSubagentRunStep(evt StepEvent, maxBytes int) (string, bool) {
	task := strings.TrimSpace(stringFromAny(evt.Input["task"]))
	subType := strings.TrimSpace(stringFromAny(evt.Input["subagent_type"]))
	sb := strings.Builder{}
	if subType != "" {
		sb.WriteString(fmt.Sprintf("subagent type: %s\n\n", subType))
	}
	if task != "" {
		sb.WriteString(task)
	}
	if sb.Len() == 0 {
		return clampBody("_(no task details)_", maxBytes)
	}
	return clampBody(sb.String(), maxBytes)
}

func stepStatusLabel(evt StepEvent) string {
	if strings.TrimSpace(evt.Error) != "" {
		return "failed"
	}
	switch strings.TrimSpace(evt.Kind) {
	// A chunk of output only exists while the call producing it is still
	// executing, so a delta reports the same status as the call's start. Naming
	// the kind instead left every surface that reads this status to decide for
	// itself what a streaming call is doing, and the ones that did not decide
	// painted a finished card over output that was still arriving.
	case StepKindToolStarted, StepKindToolParallelStarted, StepKindToolOutputDelta:
		return "running"
	case StepKindToolCompleted, StepKindToolParallelCompleted:
		if stepRequiresApproval(evt) {
			return "awaiting approval"
		}
		// request_permissions reports a policy denial as a normal (non-error)
		// completion carrying approval_status=denied, so the generic
		// error/kind inspection above cannot see it. Surface it as "denied"
		// so headers and colors match the rendered card body.
		if requestPermissionsApprovalStatus(evt) == "denied" {
			return "denied"
		}
		return "completed"
	default:
		if strings.TrimSpace(evt.Kind) != "" {
			return strings.TrimSpace(evt.Kind)
		}
		return "completed"
	}
}

// requestPermissionsApprovalStatus returns the lowercase approval_status
// recorded by the request_permissions tool, or "" for any other tool or when
// the step carries no approval decision.
func requestPermissionsApprovalStatus(evt StepEvent) string {
	if !strings.EqualFold(strings.TrimSpace(evt.ToolName), "request_permissions") {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(stringFromAny(evt.Output["approval_status"])))
}

func stepRequiresApproval(evt StepEvent) bool {
	if strings.TrimSpace(evt.ActionID) != "" || strings.TrimSpace(evt.ActionKind) != "" {
		return true
	}
	return boolFromAny(evt.Output["requires_action"])
}

func toolPurpose(evt StepEvent) string {
	if desc := strings.TrimSpace(evt.ToolDescription); desc != "" {
		return desc
	}
	tool := strings.TrimSpace(evt.ToolName)
	switch tool {
	case "read_file":
		return "Read file content before reasoning or editing."
	case "write_file":
		return "Write complete file content."
	case "edit_file":
		return "Apply an exact text replacement to a file."

	case "shell":
		return "Run a shell command requested by the agent."
	case "web_fetch", "web_search":
		return "Retrieve external information for the current task."
	case "retrieve_output":
		return "Recover previously compressed tool output."
	case "intermediate_tool":
		return "Record or review session-scoped intermediate notes during investigation and final synthesis."
	case "memories_add_ad_hoc_note":
		return "Save a user-requested durable memory note."
	case "memories_list", "memories_read", "memories_search":
		return "Inspect durable memory notes."
	case "session_todo", "enter_plan_mode", "exit_plan_mode", "working_set_show", "working_set_pin", "working_set_drop":
		return "Inspect or update session planning context."
	case "user_interaction":
		return "Ask the human for a structured decision."

	}
	if strings.HasPrefix(tool, "mcp__") {
		return "Call an MCP server tool with the shown arguments."
	}
	return "Execute this tool with the shown input."
}

func toolInvocationLabel(evt StepEvent) string {
	if cmd := strings.TrimSpace(stringFromAny(evt.Output["command"])); cmd != "" {
		return cmd
	}
	tool := strings.TrimSpace(evt.ToolName)
	// MCP tools use a semantic input summary rather than raw JSON so transcripts
	// remain compact and readable across arbitrary server schemas.
	if display, ok := McpToolDisplayName(tool); ok {
		if args := MCPToolInputSummary(evt.Input); args != "" {
			return display + " · " + args
		}
		return display
	}
	if label, ok := toolInvocationNamedLabel(evt); ok {
		return label
	}
	// Generic fallback: full input JSON.
	if len(evt.Input) > 0 {
		if b, err := json.Marshal(evt.Input); err == nil {
			return tool + " " + string(b)
		}
	}
	return tool
}

// toolInvocationNamedLabel returns the shared compact label for named
// tools. ok is false when the tool is not a named case, leaving
// MCP-display and generic fallback to the caller.
func toolInvocationNamedLabel(evt StepEvent) (string, bool) {
	tool := strings.TrimSpace(evt.ToolName)
	switch tool {
	case "shell":
		return strings.TrimSpace(stringFromAny(evt.Input["command"])), true
	case "read_file":
		return "read " + readFilePathHint(evt), true
	case "write_file":
		return "write " + firstString(evt.Input, "file_path", "path"), true
	case "edit_file":
		return "edit " + firstString(evt.Input, "file_path", "path"), true
	case "request_permissions":
		return "request permissions", true
	case "user_interaction":
		return userInteractionLabel("ask user", evt), true

	case "web_fetch":
		return "fetch " + firstString(evt.Input, "url", "uri"), true
	case "web_search":
		return "search " + quoteCompact(firstString(evt.Input, "query", "q")), true
	case "retrieve_output":
		return retrieveOutputInvocationLabel(evt.Input), true

	case "memories_add_ad_hoc_note":
		filename := firstString(evt.Input, "filename")
		if filename == "" {
			return "save memory note", true
		}
		return "save memory note " + filename, true
	case "memories_list":
		return joinHeaderParts("list memories", firstString(evt.Input, "path")), true
	case "memories_read":
		return joinHeaderParts("read memory", firstString(evt.Input, "path")), true
	case "memories_search":
		return joinHeaderParts("search memories", MemorySearchQueryLabel(evt.Input), MemorySearchScopeLabel(evt.Input)), true
	case "intermediate_tool":
		action := firstString(evt.Input, "action")
		if strings.TrimSpace(action) == "" {
			action = firstString(evt.Output, "action")
		}
		action = strings.ToLower(strings.TrimSpace(action))
		if action == "" {
			action = "append"
		}
		switch action {
		case "append":
			return "record intermediate note", true
		case "read":
			return "review intermediate notes", true
		case "clear":
			return "clear intermediate notes", true
		default:
			return "intermediate_tool " + action, true
		}
	case "subagent_fanout":
		return fanoutInvocationLabel(evt), true
	case "subagent_run":
		return subagentRunLabel(evt), true
	case "session_todo", "enter_plan_mode", "exit_plan_mode", "working_set_show", "working_set_pin", "working_set_drop":
		return tool + " " + compactFields(evt.Input, "action", "id", "path", "reason"), true
	}
	return "", false
}

// retrieveOutputInvocationLabel summarizes the recovery target without ever
// serializing the tool's argument object into the transcript.
func retrieveOutputInvocationLabel(input map[string]any) string {
	id := intFromAny(input["id"])
	label := "retrieve saved output"
	if id > 0 {
		label += " #" + strconv.Itoa(id)
	}
	if query := firstString(input, "query"); query != "" {
		return label + " matching " + quoteCompact(query)
	}
	if lines := firstString(input, "lines"); lines != "" {
		return label + " lines " + truncateSummary(lines)
	}
	return label
}

// fanoutInvocationLabel builds a compact display label for subagent_fanout.
// Detailed task list goes in the expanded body (formatFanoutStep).
// Example: "fanout 4 explore tasks"
func fanoutInvocationLabel(evt StepEvent) string {
	tasks := tasksFromInput(evt.Input)
	n := len(tasks)
	if n == 0 {
		return "fanout 0 tasks"
	}
	typeLabel := ""
	firstType := strings.TrimSpace(tasks[0].SubagentType)
	allSame := true
	for _, t := range tasks {
		if strings.TrimSpace(t.SubagentType) != firstType {
			allSame = false
			break
		}
	}
	if allSame && firstType != "" {
		typeLabel = " " + firstType
	}
	return fmt.Sprintf("fanout %d%s tasks", n, typeLabel)
}

// subagentRunLabel builds a compact display label for subagent_run.
// Example: "subagent (explore) \"Read project files\""
func subagentRunLabel(evt StepEvent) string {
	title := strings.TrimSpace(stringFromAny(evt.Input["title"]))
	if title == "" {
		title = strings.TrimSpace(stringFromAny(evt.Input["task"]))
	}
	title = truncateSummary(title)
	subType := strings.TrimSpace(stringFromAny(evt.Input["subagent_type"]))
	if subType != "" {
		if title != "" {
			return fmt.Sprintf("subagent (%s) %s", subType, strconv.Quote(title))
		}
		return fmt.Sprintf("subagent (%s)", subType)
	}
	if title != "" {
		return "subagent " + strconv.Quote(title)
	}
	return "subagent"
}

// tasksFromInput extracts SubagentTask items from the "tasks" key of a tool input map.
func tasksFromInput(input map[string]any) []SubagentTask {
	if input == nil {
		return nil
	}
	raw, ok := input["tasks"]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]SubagentTask, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		t := SubagentTask{
			Title:        strings.TrimSpace(stringFromAny(m["title"])),
			Prompt:       strings.TrimSpace(stringFromAny(m["prompt"])),
			SubagentType: strings.TrimSpace(stringFromAny(m["subagent_type"])),
		}
		out = append(out, t)
	}
	return out
}

// SubagentTask mirrors run.SubagentTask for display decoding.
type SubagentTask struct {
	Title        string `json:"title,omitempty"`
	Prompt       string `json:"prompt"`
	SubagentType string `json:"subagent_type,omitempty"`
}

func compactExplainableOutput(output map[string]any) map[string]any {
	if len(output) == 0 {
		return nil
	}
	skip := map[string]struct{}{
		"preview_text":         {},
		"preview_kind":         {},
		"stdout_preview":       {},
		"stderr_preview":       {},
		"turn_diff":            {},
		"full_text":            {},
		"content":              {},
		"old_string":           {},
		"new_string":           {},
		"summary":              {},
		"command":              {},
		"requires_action":      {},
		"stdout_bytes":         {},
		"stderr_bytes":         {},
		"stdout_omitted_bytes": {},
		"stderr_omitted_bytes": {},
		"omitted_bytes":        {},
		"full_path":            {},
	}
	out := make(map[string]any, len(output))
	for k, v := range output {
		if _, ok := skip[k]; ok {
			continue
		}
		out[k] = v
	}
	return out
}

func truncateSummary(s string) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if len(s) <= 120 {
		return s
	}
	return llm.TruncateBytes(s, 117, "...")
}

func formatBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d bytes", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
}

func formatLineCount(s string) string {
	lines := 0
	if strings.TrimSpace(s) != "" {
		lines = strings.Count(strings.TrimRight(s, "\n"), "\n") + 1
	}
	if lines == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", lines)
}

func formatCountForTool(tool string, n int) string {
	switch {
	case strings.Contains(tool, "search"):
		return fmt.Sprintf("%d matches", n)
	default:
		return fmt.Sprintf("%d items", n)
	}
}

func formatCountLabel(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

func firstInt(m map[string]any, keys ...string) (int, bool) {
	if m == nil {
		return 0, false
	}
	for _, key := range keys {
		if _, ok := m[key]; !ok {
			continue
		}
		return intFromAny(m[key]), true
	}
	return 0, false
}

func pathFromInputOrOutput(evt StepEvent) string {
	if p := firstString(evt.Input, "file_path", "path", "abs_path"); p != "" {
		return p
	}
	return firstString(evt.Output, "file_path", "path", "abs_path")
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if s := strings.TrimSpace(stringFromAny(m[key])); s != "" {
			return s
		}
	}
	return ""
}

func pathBaseOrPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return path
	}
	return base
}

func boolFromAny(v any) bool {
	b, _ := v.(bool)
	return b
}

func quoteCompact(s string) string {
	s = truncateSummary(s)
	if s == "" {
		return ""
	}
	return strconv.Quote(s)
}

func compactFields(m map[string]any, keys ...string) string {
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		v, ok := m[key]
		if !ok {
			continue
		}
		s := strings.TrimSpace(fmt.Sprint(v))
		if s == "" || s == "<nil>" {
			continue
		}
		parts = append(parts, key+"="+truncateSummary(s))
	}
	return strings.Join(parts, " ")
}

func McpToolDisplayName(tool string) (string, bool) {
	tool = strings.TrimSpace(tool)
	if !strings.HasPrefix(tool, "mcp__") {
		return "", false
	}
	rest := strings.TrimPrefix(tool, "mcp__")
	if rest == "" {
		return "mcp", true
	}
	server, toolName, _ := strings.Cut(rest, "__")
	server = strings.TrimSpace(server)
	toolName = strings.TrimSpace(toolName)
	switch {
	case server == "" && toolName == "":
		return "mcp", true
	case server != "" && toolName != "":
		return server + "." + toolName, true
	case server != "":
		return server, true
	default:
		return toolName, true
	}
}

// MCPToolInputSummary converts arbitrary MCP input into a stable, human-readable
// header summary. Unlike ordinary compact tool summaries, MCP arguments are
// intentionally lossless: the TUI soft-wraps long headers, so values must not be
// shortened or reduced to collection counts here.
func MCPToolInputSummary(input map[string]any) string {
	if len(input) == 0 {
		return ""
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := mcpInputValueSummary(input[key])
		parts = append(parts, mcpInputFieldLabel(key)+": "+value)
	}
	return strings.Join(parts, " · ")
}

func mcpInputFieldLabel(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "value"
	}
	var b strings.Builder
	lastWasSpace := false
	for i, r := range key {
		switch {
		case r == '_' || r == '-' || unicode.IsSpace(r):
			if b.Len() > 0 && !lastWasSpace {
				b.WriteByte(' ')
				lastWasSpace = true
			}
		case unicode.IsUpper(r) && i > 0 && !lastWasSpace:
			b.WriteByte(' ')
			b.WriteRune(unicode.ToLower(r))
			lastWasSpace = false
		default:
			b.WriteRune(unicode.ToLower(r))
			lastWasSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

func mcpInputValueSummary(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == v && trimmed != "" && !strings.ContainsAny(v, "\r\n\t") {
			return v
		}
		return strconv.Quote(v)
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case int8:
		return strconv.FormatInt(int64(v), 10)
	case int16:
		return strconv.FormatInt(int64(v), 10)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint:
		return strconv.FormatUint(uint64(v), 10)
	case uint8:
		return strconv.FormatUint(uint64(v), 10)
	case uint16:
		return strconv.FormatUint(uint64(v), 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		if encoded, err := json.Marshal(v); err == nil {
			return string(encoded)
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func omittedBytesNote(output map[string]any) string {
	if len(output) == 0 {
		return ""
	}
	var parts []string
	hasStreamOmitted := false
	if n := intFromAny(output["stdout_omitted_bytes"]); n > 0 {
		hasStreamOmitted = true
		parts = append(parts, fmt.Sprintf("stdout omitted %d bytes", n))
	}
	if n := intFromAny(output["stderr_omitted_bytes"]); n > 0 {
		hasStreamOmitted = true
		parts = append(parts, fmt.Sprintf("stderr omitted %d bytes", n))
	}
	if n := intFromAny(output["omitted_bytes"]); n > 0 && !hasStreamOmitted {
		parts = append(parts, fmt.Sprintf("output omitted %d bytes", n))
	}
	if len(parts) == 0 {
		return ""
	}
	note := "preview truncated: " + strings.Join(parts, "; ")
	if fullPath, ok := output["full_path"].(string); ok && strings.TrimSpace(fullPath) != "" {
		note += fmt.Sprintf(" (full output: `%s`)", strings.TrimSpace(fullPath))
	}
	return note
}

func isNoOpEditFileStep(evt StepEvent, toolName string) bool {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "edit_file", "edit":
	default:
		return false
	}
	if strings.TrimSpace(evt.Kind) != StepKindToolCompleted {
		return false
	}
	return isEmptyTurnDiff(evt.Output["turn_diff"])
}

func isEmptyTurnDiff(v any) bool {
	switch x := v.(type) {
	case event.Summary:
		return strings.TrimSpace(x.Path) != "" &&
			x.Added == 0 && x.Deleted == 0 &&
			strings.TrimSpace(x.UnifiedDiff) == ""
	case map[string]any:
		path, ok := x["path"].(string)
		if !ok || strings.TrimSpace(path) == "" {
			return false
		}
		if !isZeroTurnDiffCount(x["added"]) || !isZeroTurnDiffCount(x["deleted"]) {
			return false
		}
		if rawDiff, exists := x["unified_diff"]; exists {
			diff, ok := rawDiff.(string)
			if !ok || strings.TrimSpace(diff) != "" {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func isZeroTurnDiffCount(v any) bool {
	switch x := v.(type) {
	case int:
		return x == 0
	case int32:
		return x == 0
	case int64:
		return x == 0
	case float32:
		return x == 0
	case float64:
		return x == 0
	case json.Number:
		n, err := x.Int64()
		return err == nil && n == 0
	default:
		return false
	}
}

func turnDiffSection(output map[string]any) string {
	summary, ok := extractTurnDiff(output["turn_diff"])
	if !ok {
		return ""
	}
	sb := strings.Builder{}
	sb.WriteString(fmt.Sprintf("turn diff: `%s` (+%d/-%d)", summary.Path, summary.Added, summary.Deleted))
	if strings.TrimSpace(summary.UnifiedDiff) != "" {
		sb.WriteString("\n\n```diff\n")
		sb.WriteString(summary.UnifiedDiff)
		if !strings.HasSuffix(summary.UnifiedDiff, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("```")
	}
	return sb.String()
}

// lspDiagnosticsSection renders the "lsp_diagnostics" summary an edit tool
// recorded (spec §8.3.5): a header line plus a ```text block whose problem
// lines follow appendix B.3's shape. The summary arrives as
// event.LSPDiagnosticsSummary from a live capture and as the equivalent map
// after a replay; both normalize here so live and replayed cards agree.
func lspDiagnosticsSection(output map[string]any) string {
	if len(output) == 0 {
		return ""
	}
	summary, ok := extractLSPDiagnosticsSummary(output["lsp_diagnostics"])
	if !ok || (summary.New == 0 && len(summary.PendingFiles) == 0) {
		return ""
	}
	sb := strings.Builder{}
	if summary.New == 0 {
		sb.WriteString("lsp diagnostics: pending")
	} else {
		fileWord := "files"
		if summary.Files == 1 {
			fileWord = "file"
		}
		fmt.Fprintf(&sb, "lsp diagnostics: %d new in %d %s", summary.New, summary.Files, fileWord)
	}
	sb.WriteString("\n\n```text\n")
	lastPath := ""
	for _, item := range summary.Items {
		if item.Path != lastPath {
			sb.WriteString(item.Path)
			sb.WriteString("\n")
			lastPath = item.Path
		}
		sb.WriteString(lspDiagnosticItemLine(item))
		sb.WriteString("\n")
	}
	for _, path := range summary.PendingFiles {
		fmt.Fprintf(&sb, "diagnostics for %s are still being computed and will follow\n", path)
	}
	sb.WriteString("```")
	return sb.String()
}

// lspDiagnosticItemLine renders one problem the way appendix B.3 prints it.
func lspDiagnosticItemLine(item event.LSPDiagnostic) string {
	message := strings.ReplaceAll(strings.ReplaceAll(item.Message, "\r\n", " / "), "\n", " / ")
	text := fmt.Sprintf("  %s %d:%d %s", strings.TrimSpace(item.Severity), item.Line, item.Column, message)
	switch {
	case item.Source != "" && item.Code != "":
		text += fmt.Sprintf(" [%s %s]", item.Source, item.Code)
	case item.Source != "":
		text += fmt.Sprintf(" [%s]", item.Source)
	case item.Code != "":
		text += fmt.Sprintf(" [%s]", item.Code)
	}
	return text
}

// extractLSPDiagnosticsSummary normalizes the "lsp_diagnostics" output value:
// a live capture carries event.LSPDiagnosticsSummary, a replayed step carries
// its JSON equivalent as a map.
func extractLSPDiagnosticsSummary(v any) (event.LSPDiagnosticsSummary, bool) {
	switch x := v.(type) {
	case nil:
		return event.LSPDiagnosticsSummary{}, false
	case event.LSPDiagnosticsSummary:
		return x, true
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return event.LSPDiagnosticsSummary{}, false
		}
		var summary event.LSPDiagnosticsSummary
		if err := json.Unmarshal(b, &summary); err != nil {
			return event.LSPDiagnosticsSummary{}, false
		}
		return summary, true
	}
}

func outputWithoutTurnDiff(output map[string]any) map[string]any {
	if len(output) == 0 {
		return output
	}
	if _, ok := extractTurnDiff(output["turn_diff"]); !ok {
		return output
	}
	return outputWithout(output, "turn_diff")
}

// outputWithout returns a copy of output without the given keys — the original
// map when none of them are present, so an untouched output is never copied.
func outputWithout(output map[string]any, keys ...string) map[string]any {
	if len(output) == 0 {
		return output
	}
	present := false
	for _, k := range keys {
		if _, ok := output[k]; ok {
			present = true
			break
		}
	}
	if !present {
		return output
	}
	skip := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		skip[k] = struct{}{}
	}
	dup := make(map[string]any, len(output))
	for k, v := range output {
		if _, ok := skip[k]; ok {
			continue
		}
		dup[k] = v
	}
	return dup
}

func shellOutputMetadataOnly(output map[string]any) map[string]any {
	if len(output) == 0 {
		return output
	}
	dup := make(map[string]any, len(output))
	for k, v := range output {
		if k == "stdout" || k == "stderr" {
			continue
		}
		dup[k] = v
	}
	return outputWithout(dup, "turn_diff", "lsp_diagnostics")
}

func extractTurnDiff(v any) (event.Summary, bool) {
	switch x := v.(type) {
	case event.Summary:
		return x, true
	case map[string]any:
		return event.Summary{
			Path:        stringFromAny(x["path"]),
			Added:       intFromAny(x["added"]),
			Deleted:     intFromAny(x["deleted"]),
			UnifiedDiff: stringFromAny(x["unified_diff"]),
		}, true
	default:
		return event.Summary{}, false
	}
}

func intFromAny(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float32:
		return int(x)
	case float64:
		return int(x)
	case []any:
		return len(x)
	case []string:
		return len(x)
	default:
		return 0
	}
}

func stringFromAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func clampBody(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, false
	}
	head := maxBytes - 80
	if head < 0 {
		head = 0
	}
	return s[:head] + "\n\n_(formatted body truncated for display)_\n", true
}

func chromaLangFromPath(p string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
	switch ext {
	case "go":
		return "go"
	case "mod":
		return "go"
	case "rs":
		return "rust"
	case "py":
		return "python"
	case "js", "jsx", "mjs", "cjs":
		return "javascript"
	case "ts", "tsx":
		return "typescript"
	case "json":
		return "json"
	case "yaml", "yml":
		return "yaml"
	case "md":
		return "markdown"
	case "html", "htm":
		return "html"
	case "css":
		return "css"
	case "sh", "bash":
		return "bash"
	case "sql":
		return "sql"
	case "xml":
		return "xml"
	default:
		return "text"
	}
}

// ToolStepRendersAsPlan reports whether this step is already represented to the
// user by the plan card its tool emits, so no tool card of its own belongs to
// it. It is the StepEvent-shaped view of event.ToolStepRendersAsPlan, which the
// projection of persisted steps answers from the payload instead.
func ToolStepRendersAsPlan(evt StepEvent) bool {
	return event.ToolStepRendersAsPlan(evt.ToolName, evt.Kind, evt.Error)
}

// Provider-executed web search has no client-side call/result pair,
// so it never becomes a tool step the way a registered tool does: it arrives
// only as stream-sink callbacks. Every surface still has to show it as one, and
// a subagent's search has to be attributable to that subagent, so the step id,
// the wording and the meta are built here once and reused by the terminal, the
// subagent event stream, and the gateway rather than being re-invented per
// surface.
const ProviderWebSearchToolName = "web_search"

// ProviderWebSearchStepID keys the running search so its completion replaces it
// instead of appending a second block.
func ProviderWebSearchStepID(id string) string {
	return "web-search-" + strings.TrimSpace(id)
}

// ProviderWebSearchSummary is the one line a surface shows for the search. detail
// is the provider's query, which is only known once the search completes.
func ProviderWebSearchSummary(detail string, completed bool) string {
	if !completed {
		return "Searching the web"
	}
	if detail = strings.TrimSpace(detail); detail != "" {
		return "Searched the web for " + detail
	}
	return "Searched the web"
}

// ProviderWebSearchMeta describes the search to the tool-card renderers. agentID
// is the roster key of the agent that ran it, empty for the primary agent.
func ProviderWebSearchMeta(detail string, completed bool, agentID string) ToolMeta {
	meta := ToolMeta{
		ToolName:   ProviderWebSearchToolName,
		Status:     "running",
		Purpose:    "Search the web through the model provider's own search tool.",
		Invocation: ProviderWebSearchToolName,
		AgentID:    strings.TrimSpace(agentID),
	}
	if completed {
		meta.Status = "completed"
	}
	if detail = strings.TrimSpace(detail); detail != "" {
		meta.Input = map[string]any{"query": detail}
		meta.Invocation = ProviderWebSearchToolName + " " + detail
	}
	return meta
}
