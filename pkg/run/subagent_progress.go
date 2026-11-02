// What a subagent is doing and has done, read from its own conversation.
package run

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

const (
	// subagentProgressMessageLimit is how much of a subagent's conversation
	// one progress read looks at: its newest rows, never paged. A subagent
	// that has taken more steps than this reports the steps inside the
	// window, which is what "what is it doing now" asks for.
	subagentProgressMessageLimit = 200
	// subagentProgressRecentLimit is how many settled steps the progress
	// carries: enough to see the shape of the work, few enough to keep the
	// answer small.
	subagentProgressRecentLimit = 5
	// subagentProgressLastMessageRunes bounds the last thing the subagent
	// said, in runes — the unit a reader counts.
	subagentProgressLastMessageRunes = 600
)

// subagentProgress is what a subagent is doing and has done, read from its own
// conversation: the steps it took, the step in flight, its todo list, the last
// thing it said, and what it has used. It never interrupts the subagent.
type subagentProgress struct {
	Phase       string                `json:"phase"` // running | waiting_approval | idle | finished
	ElapsedMs   int64                 `json:"elapsed_ms"`
	Steps       int                   `json:"steps"`
	Current     string                `json:"current,omitempty"`
	Recent      []subagentStepSummary `json:"recent,omitempty"` // newest last, at most 5
	Todos       *subagentTodoSummary  `json:"todos,omitempty"`
	LastMessage string                `json:"last_message,omitempty"` // at most 600 runes
	Usage       *subagentUsageSummary `json:"usage,omitempty"`
}

// subagentStepSummary is one step that has settled, named the way its card is
// named.
type subagentStepSummary struct {
	Header     string `json:"header"`
	Outcome    string `json:"outcome"` // ok | failed | denied
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// subagentTodoSummary is the subagent's checklist: how much of it is done and
// which rows are which.
type subagentTodoSummary struct {
	Completed int                `json:"completed"`
	Total     int                `json:"total"`
	Items     []subagentTodoItem `json:"items,omitempty"`
}

type subagentTodoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

// subagentTodoCount is the short form a list row carries: the numbers, not the
// rows.
type subagentTodoCount struct {
	Completed int `json:"completed"`
	Total     int `json:"total"`
}

// subagentUsageSummary is what the subagent has spent so far.
type subagentUsageSummary struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// subagentProgressShort is the progress a list row carries: the phase, how far
// the subagent has got and what it is on now, without the step-by-step detail
// that would make a roster expensive.
type subagentProgressShort struct {
	Phase   string             `json:"phase"`
	Steps   int                `json:"steps"`
	Current string             `json:"current,omitempty"`
	Todos   *subagentTodoCount `json:"todos,omitempty"`
}

// subagentListRow is one roster row: the record as every reader knows it, with
// its own progress beside it.
type subagentListRow struct {
	agent.HistoryEntry
	Progress *subagentProgressShort `json:"progress,omitempty"`
}

// buildSubagentProgress reads a subagent's progress. It is read-only: nothing
// here writes to the subagent's conversation, its todo list or its run.
func buildSubagentProgress(ctx context.Context, fac Factory, record agent.HistoryEntry) subagentProgress {
	p := subagentProgress{
		Phase:     subagentPhase(fac, record),
		ElapsedMs: subagentElapsedMs(record),
	}
	store := fac.ownerSessionStore()
	workerSessionID := strings.TrimSpace(record.WorkerSessionID)
	if store == nil || workerSessionID == "" {
		return p
	}
	rows, _, err := store.ListMessagesBefore(ctx, workerSessionID, 0, subagentProgressMessageLimit)
	if err != nil {
		return p
	}
	rows = subagentProgressRows(rows)
	p.Steps, p.Current, p.Recent = subagentStepsOf(rows, fac.projectRoot())
	p.LastMessage = subagentLastMessage(rows)
	p.Todos = subagentTodosOf(fac, workerSessionID)
	p.Usage = subagentUsageOf(fac, ctx, workerSessionID)
	return p
}

// subagentProgressRows keeps the rows a step and a message are read from, in
// the order the subagent wrote them.
func subagentProgressRows(rows []state.Message) []state.Message {
	out := make([]state.Message, 0, len(rows))
	for _, row := range rows {
		switch strings.ToLower(strings.TrimSpace(row.Role)) {
		case llm.RoleAssistant, llm.RoleTool:
			out = append(out, row)
		}
	}
	return out
}

// subagentStepsOf counts the subagent's steps and names the one in flight.
//
// A step that settled is the tool row's own card header — the words the
// surface drew — so the dispatcher reads the same names the user did. The step
// in flight is a call the subagent made and has not got back yet: the model's
// call is written to the transcript before it runs, so a call with no tool row
// behind it is exactly the one executing now, and its header is built from the
// same card facts with the call's running status.
func subagentStepsOf(rows []state.Message, cwd string) (steps int, current string, recent []subagentStepSummary) {
	pending := make([]llm.ToolCall, 0, 4)
	for _, row := range rows {
		if strings.EqualFold(strings.TrimSpace(row.Role), llm.RoleAssistant) {
			_, calls, _, _ := state.ParseMessageParts(row.PartsJSON, row.Content)
			pending = append(pending, calls...)
			continue
		}
		if strings.TrimSpace(row.ToolStepID) == "" {
			continue
		}
		steps++
		if summary, ok := subagentStepSummaryOf(row, cwd); ok {
			recent = append(recent, summary)
		}
		pending = dropToolCall(pending, row.ToolStepID)
	}
	steps += len(pending)
	if len(pending) > 0 {
		current = subagentPendingHeader(pending[len(pending)-1], cwd)
	}
	if len(recent) > subagentProgressRecentLimit {
		recent = recent[len(recent)-subagentProgressRecentLimit:]
	}
	return steps, current, recent
}

// subagentStepSummaryOf names one settled step from its own card facts.
func subagentStepSummaryOf(row state.Message, cwd string) (subagentStepSummary, bool) {
	meta, summary, ok := subagentStepMeta(row)
	if !ok {
		return subagentStepSummary{}, false
	}
	header := tool.DisplayHeader(tool.DisplayFrame{
		Title:   strings.TrimSpace(meta.ToolName),
		Summary: summary,
		Meta:    meta,
		Final:   true,
	}, cwd)
	if header == "" {
		return subagentStepSummary{}, false
	}
	return subagentStepSummary{
		Header:     header,
		Outcome:    subagentStepOutcome(meta.Status),
		DurationMs: row.ExecDurationMs,
	}, true
}

// subagentStepMeta reads the card facts a tool row was recorded with: the
// display part carries them for every row the turn wrote, and the row's own
// tool_meta_json column carries them for the rows written outside a turn.
func subagentStepMeta(row state.Message) (tool.ToolMeta, string, bool) {
	if display, ok := state.ParseToolDisplayPart(row.PartsJSON); ok {
		if meta, ok := toolMetaFromJSON(display.ToolMetaJSON); ok {
			return meta, display.Summary, true
		}
	}
	if meta, ok := toolMetaFromJSON(row.ToolMetaJSON); ok {
		return meta, "", true
	}
	return tool.ToolMeta{}, "", false
}

// subagentStepOutcome reduces a card status to the three outcomes a step has.
func subagentStepOutcome(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed":
		return "failed"
	case "denied":
		return "denied"
	default:
		return "ok"
	}
}

// subagentPendingHeader names a call that has not returned yet, in the words
// its card uses while it runs.
func subagentPendingHeader(call llm.ToolCall, cwd string) string {
	input := map[string]any{}
	if args := strings.TrimSpace(call.Function.Arguments); args != "" {
		_ = json.Unmarshal([]byte(args), &input)
	}
	meta := tool.BuildToolMeta(tool.StepEvent{
		Kind:     tool.StepKindToolStarted,
		StepID:   strings.TrimSpace(call.ID),
		ToolName: strings.TrimSpace(call.Function.Name),
		Input:    input,
	})
	return tool.DisplayHeader(tool.DisplayFrame{
		Title: strings.TrimSpace(meta.ToolName),
		Meta:  meta,
	}, cwd)
}

// dropToolCall forgets the call a tool row answered.
func dropToolCall(calls []llm.ToolCall, stepID string) []llm.ToolCall {
	stepID = strings.TrimSpace(stepID)
	for i, call := range calls {
		if strings.TrimSpace(call.ID) == stepID {
			return append(calls[:i], calls[i+1:]...)
		}
	}
	return calls
}

// subagentLastMessage is the newest thing the subagent said, bounded so one
// answer cannot flood the dispatcher's context.
func subagentLastMessage(rows []state.Message) string {
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		if !strings.EqualFold(strings.TrimSpace(row.Role), llm.RoleAssistant) {
			continue
		}
		text := strings.TrimSpace(row.Content)
		if text == "" {
			continue
		}
		return truncateRunes(text, subagentProgressLastMessageRunes)
	}
	return ""
}

// truncateRunes bounds text by runes, the unit a reader counts characters in.
func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// subagentTodosOf reads the subagent's checklist. A subagent that kept none
// reports none: an empty list is not progress.
func subagentTodosOf(fac Factory, workerSessionID string) *subagentTodoSummary {
	root := strings.TrimSpace(fac.workspaceRoot())
	if root == "" {
		return nil
	}
	list, err := state.Load(root, workerSessionID)
	if err != nil || len(list.Items) == 0 {
		return nil
	}
	out := &subagentTodoSummary{Total: len(list.Items), Items: make([]subagentTodoItem, 0, len(list.Items))}
	for _, item := range list.Items {
		if item.Status == state.StatusCompleted {
			out.Completed++
		}
		out.Items = append(out.Items, subagentTodoItem{
			Content: item.Content,
			Status:  string(item.Status),
		})
	}
	return out
}

// subagentUsageOf sums what the subagent's runs have spent. A store that cannot
// answer, or a subagent that has spent nothing yet, carries no usage at all.
func subagentUsageOf(fac Factory, ctx context.Context, workerSessionID string) *subagentUsageSummary {
	runRT := fac.ownerRunRT()
	if runRT == nil {
		return nil
	}
	usage, err := runRT.SessionUsageForSession(ctx, workerSessionID)
	if err != nil {
		return nil
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
		return nil
	}
	return &subagentUsageSummary{InputTokens: usage.PromptTokens, OutputTokens: usage.CompletionTokens}
}

// subagentPhase says where a subagent stands: a finished record is finished, a
// live execution behind an approval gate is waiting for that approval, a live
// execution is running, and anything else is idle — the dispatcher's to
// continue.
func subagentPhase(fac Factory, record agent.HistoryEntry) string {
	status := strings.TrimSpace(string(record.Status))
	if status != "" && status != string(agent.StatusRunning) {
		return "finished"
	}
	if !subagentRunning(fac, record) {
		return "idle"
	}
	if runRT := fac.ownerRunRT(); runRT != nil && strings.TrimSpace(record.RunID) != "" {
		if wait, _ := runRT.GetWaitForRun(context.Background(), strings.TrimSpace(record.RunID)); wait != nil {
			return "waiting_approval"
		}
	}
	return "running"
}

// subagentRunning reports whether this process is executing the subagent now.
func subagentRunning(fac Factory, record agent.HistoryEntry) bool {
	ch := subagentChannelFor(fac, record)
	if ch == nil {
		return false
	}
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.running != nil
}

// subagentElapsedMs is how long the subagent has been at it: up to its end
// once it ended, up to now while it runs.
func subagentElapsedMs(record agent.HistoryEntry) int64 {
	if record.StartedAt <= 0 {
		return 0
	}
	end := record.FinishedAt
	if end <= 0 {
		end = time.Now().Unix()
	}
	if end < record.StartedAt {
		return 0
	}
	return (end - record.StartedAt) * 1000
}

// subagentProgressShortOf is the list-row form of a progress read.
func subagentProgressShortOf(p subagentProgress) subagentProgressShort {
	out := subagentProgressShort{Phase: p.Phase, Steps: p.Steps, Current: p.Current}
	if p.Todos != nil {
		out.Todos = &subagentTodoCount{Completed: p.Todos.Completed, Total: p.Todos.Total}
	}
	return out
}

// toolMetaFromJSON reads the card facts a row recorded, and reports whether the
// row carried any: a row with no facts has no header to show.
func toolMetaFromJSON(raw string) (tool.ToolMeta, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return tool.ToolMeta{}, false
	}
	var meta tool.ToolMeta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return tool.ToolMeta{}, false
	}
	if strings.TrimSpace(meta.ToolName) == "" {
		return tool.ToolMeta{}, false
	}
	return meta, true
}
