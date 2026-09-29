// UI notifications and the session wiring that publishes them.
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

type MsgKind string

const (
	MsgKindUser      MsgKind = "user"
	MsgKindAssistant MsgKind = "assistant"
	MsgKindReasoning MsgKind = "reasoning"
	MsgKindSystem    MsgKind = "system"
	MsgKindTool      MsgKind = "tool"
	MsgKindError     MsgKind = "error"
	MsgKindPlan      MsgKind = "plan"
)

type Message struct {
	Kind     MsgKind
	Content  string
	StepID   string // Stable tool-call step id shared by started/completed messages.
	ToolName string
	FilePath string // Phase 2 redesign: file path from tool Input; used by Tracker for unique-file dedup (2A).
	Summary  string // Phase 1 redesign: pre-rendered one-line summary for terse block rendering.
	// Title overrides the frame heading a kind would otherwise use. A failure
	// that belongs to a named subsystem says which one here, so the reader knows
	// where it came from before reading the error itself.
	Title     string
	ToolMeta  tool.ToolMeta
	AgentID   string
	RunID     string // Non-empty for subagent frames so reducer keys on child run, not primary.
	ToolPhase string // event.RunEventToolStarted ("tool_call_started") or RunEventToolCompleted ("tool_call_completed")
	// ToolOutputDelta marks Content as an incremental stdout/stderr chunk. The
	// TUI accumulates it into the existing running tool frame.
	ToolOutputDelta bool
	// RetainAsHistory prevents a completed execution attempt from being
	// overwritten by a later lifecycle event with the same StepID.
	RetainAsHistory bool
	Duration        time.Duration
	Timestamp       time.Time
}

type ApprovalSubmit struct {
	ActionID      string
	Approved      bool
	Cancelled     bool
	Reason        string
	KeyChord      string
	PolicyKind    string
	DetailPreview string
	AskAnswerJSON string
}

type NewMessageMsg struct {
	Msg  Message
	turn *foregroundTurn
}
type ReasoningDoneMsg struct {
	turn      *foregroundTurn
	AgentID   string // optional: for subagent-specific reasoning finalization
	Timestamp time.Time
}
type StreamResetMsg struct{ turn *foregroundTurn }
type RunStartedMsg struct {
	RunID string
	turn  *foregroundTurn
}
type RunEndedMsg struct {
	turn           *foregroundTurn
	RunID          string
	Error          string
	WorkedDuration time.Duration
	InputTokens    int // Phase 2 redesign: provider-reported prompt token count; 0 when provider doesn't expose it.
	OutputTokens   int // Phase 2 redesign: provider-reported completion token count; 0 when provider doesn't expose it.
}

// PendingSteersChangedMsg is emitted when a run drains queued steers into the
// model at a tool boundary. Count is how many steers remain queued afterwards;
// the surface diffs it against its own queue mirror to learn which messages were
// delivered, then moves those into the transcript so they do not silently
// disappear from the pending preview.
type PendingSteersChangedMsg struct {
	RunID     string
	SessionID string
	Channel   string
	Count     int
}

// TokenUsageDeltaMsg is an internal UI event emitted when one LLM call
// returns provider usage during an active run. Reducers may fold it into
// transient counters without rendering a visible frame.
type TokenUsageDeltaMsg struct {
	RunID        string
	AgentID      string // non-empty for subagent usage; routes the delta to the owning fanout task
	InputTokens  int
	OutputTokens int
}

// A compaction reaches the reducer as its lifecycle events, one message each.
// CompactionID keys the one card all of them draw; AgentID routes that card to
// the subagent whose history was compacted, and is empty for the
// conversation's own.

type ContextCompactingMsg struct {
	CompactionID string
	AgentID      string
	Trigger      string
	TokensBefore int
}

type ContextCompactProgressMsg struct {
	CompactionID string
	AgentID      string
	Percent      int
	Phase        string
}

type ContextCompactedMsg struct {
	Payload event.ContextCompactedPayload
}

type ContextCompactFailedMsg struct {
	CompactionID string
	AgentID      string
	Error        string
	Cancelled    bool
}

// A /goal reaches the reducer as its lifecycle events: it opens with the
// objective, each continuation round opens with the check's reason for it, and
// it closes with how it ended.

type GoalStartedMsg struct {
	RunID     string
	Objective string
}

type GoalRoundStartedMsg struct {
	RunID        string
	Round        int
	Why          string
	CheckAgentID string
}

type GoalCompletedMsg struct {
	RunID   string
	Payload event.GoalCompletedPayload
}

// goalEventMessage is the message a goal's event reaches the reducer as, live
// and on replay alike, so a resumed session draws a goal's lines through the
// same reduction that drew them live.
func goalEventMessage(evt event.RunEvent) (any, bool) {
	switch evt.Type {
	case event.RunEventGoalStarted:
		var p event.GoalStartedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return nil, false
		}
		return GoalStartedMsg{RunID: evt.RunID, Objective: p.Objective}, true
	case event.RunEventGoalRoundStarted:
		var p event.GoalRoundStartedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return nil, false
		}
		return GoalRoundStartedMsg{RunID: evt.RunID, Round: p.Round, Why: p.Why, CheckAgentID: p.CheckAgentID}, true
	case event.RunEventGoalCompleted:
		var p event.GoalCompletedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return nil, false
		}
		return GoalCompletedMsg{RunID: evt.RunID, Payload: p}, true
	default:
		return nil, false
	}
}

type QuitRequestedMsg struct {
	Reason string
}

// MigrationProgressMsg is one throttled checkpoint of a running import. It
// renders as the transient status line, never as transcript history.
type MigrationProgressMsg struct {
	Stage  string
	Done   int
	Total  int
	Detail string
}

// MigrationPreviewMsg carries a finished dry run. The surface shows the plan
// and asks for confirmation before anything is written; Run starts the real
// import when the user accepts.
type MigrationPreviewMsg struct {
	Plan   string
	Report string
	Run    func()
}

// MigrationDoneMsg carries a finished import's report. The report was
// persisted as a session event before this message is emitted, so what the
// user sees is what a resume replays.
type MigrationDoneMsg struct {
	Title  string
	Report string
	Err    error
}

// SkillInstallProgressMsg is one checkpoint of a background skill install.
// It renders as a live card in the transcript, replaced in place by the next
// checkpoint and finally by SkillInstallDoneMsg carrying the same ID.
type SkillInstallProgressMsg struct {
	ID        string
	SourceRef string
	// Phase is the shared human phrase for what the install is doing now.
	Phase   string
	Elapsed time.Duration
}

// SkillInstallDoneMsg carries a finished install. Report is the body of the
// completed card; Err replaces it when the install failed.
type SkillInstallDoneMsg struct {
	ID        string
	SourceRef string
	Summary   string
	Report    string
	Elapsed   time.Duration
	Err       error
}

type SessionSwitchedMsg struct {
	SessionID string
	Title     string
	Reason    string
	Select    bool
}

// SlashPickerRequestedMsg asks the main loop to show the picker a slash
// command offered from a path that cannot show it itself.
type SlashPickerRequestedMsg struct{ Picker *turn.Picker }
type SessionSelectionRequestedMsg struct{}
type PermissionManagementRequestedMsg struct{}
type SkillSelectionRequestedMsg struct{}

type TokenBudgetUpdatedMsg struct {
	Model                string
	TokenUsage           int
	PercentLeft          int
	ContextWindow        int
	EffectiveWindow      int
	AutoCompactThreshold int
}

type PlanUpdatedMsg struct {
	Payload event.PlanUpdatedPayload
}

// ActivityStatusUpdatedMsg carries a subagent's latest tool activity description
// for the fanout third-layer live progress display.
type ActivityStatusUpdatedMsg struct {
	AgentID string
	Status  string
}

// SubagentSpawnedMsg is emitted when a subagent run is dispatched. The reducer
// converts it into a FrameStatus carrying AgentID so renderer can open a
// subagent block and tag downstream frames produced by that run.
type SubagentSpawnedMsg struct {
	AgentID   string
	AgentType string
	TaskID    string
	// Title is the short name the dispatching agent gave this task, and is what
	// a roster row and a task card show. Task is the whole prompt it sent.
	Title string
	// Task is the prompt the dispatching agent sent this subagent. It opens the
	// subagent's own view, where it is the first message: without it that view
	// starts mid-conversation, showing answers to a question the reader cannot
	// see.
	Task             string
	ParentToolCallID string
	TaskIndex        int
	ExecutionID      string
	Timestamp        time.Time
}

// SubagentEndedMsg is emitted when a subagent run reaches a terminal state
// (completed / failed / cancelled). Reducer closes the subagent block, folds
// counters back into the parent context, and emits a FrameStatus with the
// final Status.
type SubagentEndedMsg struct {
	AgentID          string
	AgentType        string
	TaskID           string
	Status           string
	Error            string
	Output           string
	ParentToolCallID string
	TaskIndex        int
	ExecutionID      string
	Timestamp        time.Time
}

type SendMsgFunc func(content string)
type ApproveFn func(ApprovalSubmit)

const DefaultNotificationTimeout = 8 * time.Second

type NotificationPriority string

const (
	NotificationLow       NotificationPriority = "low"
	NotificationMedium    NotificationPriority = "medium"
	NotificationHigh      NotificationPriority = "high"
	NotificationImmediate NotificationPriority = "immediate"
)

type Notification struct {
	Key         string
	Text        string
	Priority    NotificationPriority
	Invalidates []string
	Timeout     time.Duration
	Fold        func(current Notification, incoming Notification) Notification
}

type NotificationQueue struct {
	current *Notification
	queue   []Notification
}

type toolDisplayMode int

const (
	toolDisplayModeOnRequest toolDisplayMode = iota
	toolDisplayModeCompactInput
)

func (s *ChatSession) notifyToolStepHooks(ctx context.Context, sessionID, runID, channel string, evt tool.StepEvent) {
	if s == nil {
		return
	}
	if !tool.ToolStepUserVisible(evt) {
		return
	}
	evt = tool.NormalizeToolStepForDisplay(evt)
	if evt.Kind == tool.StepKindToolOutputDelta {
		chunk, _ := evt.Output["chunk"].(string)
		if chunk == "" {
			return
		}
		meta := tool.EnrichToolMeta(ctx, tool.BuildToolMeta(evt))
		s.notifyUI(NewMessageMsg{Msg: Message{
			Kind:            MsgKindTool,
			StepID:          strings.TrimSpace(evt.StepID),
			ToolName:        strings.TrimSpace(evt.ToolName),
			Content:         chunk,
			Summary:         toolStepSummary(evt),
			ToolMeta:        meta,
			AgentID:         strings.TrimSpace(meta.AgentID),
			RunID:           strings.TrimSpace(runID),
			ToolPhase:       evt.Kind,
			ToolOutputDelta: true,
			RetainAsHistory: evt.RetainAsHistory,
			Timestamp:       time.Now(),
		}})
		return
	}
	if evt.Kind == tool.StepKindToolParallelStarted || evt.Kind == tool.StepKindToolParallelCompleted {
		s.notifyToolBatchSummary(ctx, evt)
		return
	}
	rendered, ok := tool.BuildRenderedToolStepNotification(sessionID, runID, channel, evt)
	meta := tool.EnrichToolMeta(ctx, tool.BuildToolMeta(evt))
	summary := toolStepSummary(evt)
	mode := toolDisplayModeOnRequest
	if strings.HasPrefix(strings.TrimSpace(evt.ToolName), "mcp__") && (evt.Kind == tool.StepKindToolStarted || stepRequiresApprovalStepEvent(evt)) {
		mode = toolDisplayModeCompactInput
	}
	if !ok {
		if strings.TrimSpace(summary) == "" {
			return
		}
		body := ""
		if body == "" && mode == toolDisplayModeCompactInput {
			body = compactToolInputBody(meta)
		}
		// The ChatSession dispatcher enqueues in FIFO order without invoking the
		// surface callback from this StepHook, so tool boundaries stay ordered
		// without allowing a blocked UI sink to stall tool execution.
		s.notifyUI(NewMessageMsg{Msg: Message{
			Kind:            MsgKindTool,
			StepID:          strings.TrimSpace(evt.StepID),
			ToolName:        strings.TrimSpace(evt.ToolName),
			FilePath:        extractFilePathFromEvent(evt),
			Content:         body,
			Summary:         summary,
			ToolMeta:        meta,
			AgentID:         strings.TrimSpace(meta.AgentID),
			ToolPhase:       evt.Kind,
			Duration:        evt.Duration,
			RetainAsHistory: evt.RetainAsHistory,
			Timestamp:       time.Now(),
		}})
		return
	}
	rendered.ToolMeta = meta
	body := strings.TrimSpace(rendered.DisplayBody)
	if body == "" && mode == toolDisplayModeCompactInput {
		body = compactToolInputBody(meta)
	}
	if body == "" && strings.TrimSpace(summary) == "" {
		return
	}
	// See the fallback branch above: keep the producer ordering without making
	// tool execution wait for the TUI consumer.
	s.notifyUI(NewMessageMsg{Msg: Message{
		Kind:            MsgKindTool,
		StepID:          strings.TrimSpace(evt.StepID),
		ToolName:        strings.TrimSpace(rendered.Input.ToolName),
		FilePath:        extractFilePathFromEvent(evt),
		Content:         body,
		Summary:         summary,
		ToolMeta:        meta,
		AgentID:         strings.TrimSpace(meta.AgentID),
		ToolPhase:       evt.Kind,
		Duration:        evt.Duration,
		RetainAsHistory: evt.RetainAsHistory,
		Timestamp:       time.Now(),
	}})
}

func stepRequiresApprovalStepEvent(evt tool.StepEvent) bool {
	if strings.TrimSpace(evt.ActionID) != "" || strings.TrimSpace(evt.ActionKind) != "" {
		return true
	}
	return boolValueFromMap(evt.Output, "requires_action")
}

func compactToolInputBody(meta tool.ToolMeta) string {
	if len(meta.Input) == 0 {
		return ""
	}
	b, err := json.MarshalIndent(meta.Input, "", "  ")
	if err != nil {
		return ""
	}
	return "input:\n\n```json\n" + string(b) + "\n```"
}

func (s *ChatSession) notifyToolBatchSummary(ctx context.Context, evt tool.StepEvent) {
	summary := strings.TrimSpace(stringValueFromMap(evt.Output, "summary"))
	if summary == "" {
		summary = toolStepSummary(evt)
	}
	summary = normalizeToolBatchSummary(evt, summary)
	if summary == "" {
		return
	}
	meta := tool.EnrichToolMeta(ctx, tool.BuildToolMeta(evt))
	// Preserve producer order through ChatSession's nonblocking dispatcher; in
	// particular the parallel batch summary must precede its child tool cards.
	s.notifyUI(NewMessageMsg{Msg: Message{
		Kind:      MsgKindTool,
		StepID:    strings.TrimSpace(evt.StepID),
		ToolName:  strings.TrimSpace(evt.ToolName),
		Summary:   summary,
		ToolMeta:  meta,
		AgentID:   strings.TrimSpace(meta.AgentID),
		RunID:     strings.TrimSpace(tool.RunIDFromContext(ctx)),
		ToolPhase: strings.TrimSpace(evt.Kind),
		Duration:  evt.Duration,
		Timestamp: time.Now(),
	}})
}

func normalizeToolBatchSummary(evt tool.StepEvent, summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	if strings.TrimSpace(evt.Kind) != tool.StepKindToolParallelStarted && strings.TrimSpace(evt.Kind) != tool.StepKindToolParallelCompleted {
		return summary
	}
	if strings.TrimSpace(evt.ToolName) != "read_file" {
		return summary
	}
	if rest, ok := strings.CutPrefix(summary, "Read "); ok {
		return "running read " + strings.TrimSpace(rest)
	}
	return summary
}

func toolStepSummary(evt tool.StepEvent) string {
	if summary := tool.SummarizeToolStep(evt); strings.TrimSpace(summary) != "" {
		return strings.TrimSpace(summary)
	}
	tool := strings.TrimSpace(evt.ToolName)
	if errText := strings.TrimSpace(evt.Error); errText != "" {
		return "failed: " + truncateSummary(errText)
	}
	switch tool {
	case "skill":
		if name := stringValueFromMap(evt.Input, "command"); name != "" {
			return name + " loaded"
		}
	case "read_file":
		if path := extractFilePathFromInput(evt.Input); path != "" {
			return pathBaseOrPath(path)
		}
	case "shell":
		if cmd := stringValueFromMap(evt.Input, "command"); cmd != "" {
			return truncateSummary(cmd)
		}
	}
	if summary := stringValueFromMap(evt.Output, "summary"); summary != "" {
		return truncateSummary(summary)
	}
	if path := extractFilePathFromInput(evt.Input); path != "" {
		return pathBaseOrPath(path)
	}
	for _, key := range []string{"stdout_bytes", "stderr_bytes", "omitted_bytes", "bytes"} {
		if n := intValueFromMap(evt.Output, key); n > 0 {
			return formatByteCount(n)
		}
	}
	for _, key := range []string{"result_lines", "stdout_lines"} {
		if n := intValueFromMap(evt.Output, key); n > 0 {
			if n == 1 {
				return "1 line"
			}
			return strconv.Itoa(n) + " lines"
		}
	}
	if text := stringValueFromMap(evt.Output, "preview_text"); text != "" {
		return summarizeLineCount(text)
	}
	if len(evt.Output) > 0 {
		return "done"
	}
	return "ok"
}

func pathBaseOrPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return path
	}
	last := strings.TrimSpace(parts[len(parts)-1])
	if last == "" {
		return path
	}
	return last
}

func intValueFromMap(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return 0
}

func formatByteCount(n int) string {
	if n < 1024 {
		return strconv.Itoa(n) + " bytes"
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
}

func summarizeLineCount(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "ok"
	}
	lines := strings.Count(text, "\n") + 1
	if lines == 1 {
		return truncateSummary(text)
	}
	return strconv.Itoa(lines) + " lines"
}

func truncateSummary(s string) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	const maxRunes = 72
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}

func extractFilePathFromInput(input map[string]any) string {
	if input == nil {
		return ""
	}
	for _, key := range []string{"file_path", "path", "full_path", "abs_path"} {
		if fp, ok := input[key].(string); ok {
			if fp = strings.TrimSpace(fp); fp != "" {
				return fp
			}
		}
	}
	return ""
}

// extractFilePathFromEvent resolves the display file path for a tool step.
// It checks the input first (read_file/write_file/edit_file carry file_path
// in their arguments). For exit_plan_mode the plan file path lives in the
// output JSON envelope instead, so parse it from there so the renderer can
// display it.
func extractFilePathFromEvent(evt tool.StepEvent) string {
	// A read_file whose path was repaired into the skill catalog read a
	// different file than the arguments name; the card has to name the file
	// that was actually read.
	if fp := tool.RepairedReadPath(evt); fp != "" {
		return fp
	}
	if fp := extractFilePathFromInput(evt.Input); fp != "" {
		return fp
	}
	if strings.EqualFold(strings.TrimSpace(evt.ToolName), "exit_plan_mode") {
		return extractPlanFileFromOutput(evt.Output)
	}
	return ""
}

// extractPlanFileFromOutput parses the exit_plan_mode output envelope (wrapped
// under the "output" key as a JSON string) and returns the plan_file path.
func extractPlanFileFromOutput(output map[string]any) string {
	raw := strings.TrimSpace(stringValueFromMap(output, "output"))
	if raw == "" {
		return ""
	}
	var parsed struct {
		PlanFile string `json:"plan_file"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.PlanFile)
}

// isSkillTool was the natural-language prefix check that recognized an
// explicit skill step from its ToolDescription. Skill steps now carry their
// identity in structured StepEvent fields (Category/SkillName/SkillPath), so
// the surface never inspects descriptions.

func (s *ChatSession) publishRunEvent(ctx context.Context, evt event.RunEvent) error {
	if s == nil {
		return nil
	}
	if s.runSvc() != nil && strings.TrimSpace(evt.SessionID) != "" {
		persisted, inserted, err := s.runSvc().AppendSessionEventOnce(ctx, state.SessionEvent{
			ID: evt.ID, RunID: evt.RunID, SessionID: evt.SessionID, Type: evt.Type,
			Payload: append(json.RawMessage(nil), evt.Payload...), CreatedAt: evt.CreatedAt,
		})
		switch {
		case errors.Is(err, state.ErrSessionNotStarted):
			// The conversation has not started: the event is shown and belongs
			// to no session's history. A resume re-observes whatever still
			// holds, against the session that exists by then.
		case err != nil:
			return err
		case !inserted:
			// The event is already in the session's log, so it has already been
			// shown: this is a producer observing the same fact a second time —
			// a surface that resubscribed and replayed its state, a reconnected
			// client — and not a second occurrence. The store is what makes that
			// decidable; a surface's own memory of what it has sent would not
			// survive a restart, and the duplicate it let through would be a
			// second error block for one failure.
			return nil
		default:
			evt = turn.RunEventFromRecord(persisted)
		}
	}
	switch evt.Type {
	case event.RunEventAssistantDelta:
		var p event.AssistantDeltaPayload
		if json.Unmarshal(evt.Payload, &p) == nil && p.Text != "" {
			s.notifyUI(NewMessageMsg{Msg: Message{Kind: MsgKindAssistant, Content: p.Text, AgentID: p.AgentID, RunID: evt.RunID, Timestamp: evt.CreatedAt}})
		}
	case event.RunEventReasoningDelta:
		var p event.ReasoningDeltaPayload
		if json.Unmarshal(evt.Payload, &p) == nil && p.Text != "" {
			s.notifyUI(NewMessageMsg{Msg: Message{Kind: MsgKindReasoning, Content: p.Text, AgentID: p.AgentID, RunID: evt.RunID, Timestamp: evt.CreatedAt}})
		}
	case event.RunEventReasoningDone:
		var p event.ReasoningDonePayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			s.notifyUI(ReasoningDoneMsg{AgentID: p.AgentID, Timestamp: evt.CreatedAt})
		}
	case event.RunEventUsageDelta:
		var p event.UsageDeltaPayload
		if json.Unmarshal(evt.Payload, &p) == nil && (p.InputTokens != 0 || p.OutputTokens != 0) {
			s.notifyUI(TokenUsageDeltaMsg{RunID: evt.RunID, AgentID: p.AgentID, InputTokens: p.InputTokens, OutputTokens: p.OutputTokens})
		}
	case event.RunEventToolStarted:
		var p event.ToolCallStartedPayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			s.notifyUI(subagentToolStepMsg(evt, p.StepID, p.ToolName, firstNonEmpty(p.Summary, p.Description), p.ToolMeta, evt.Type))
		}
	case event.RunEventToolCompleted:
		var p event.ToolCallCompletedPayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			msg := subagentToolStepMsg(evt, p.StepID, p.ToolName, firstNonEmpty(p.Summary, p.Description), p.ToolMeta, evt.Type)
			msg.Msg.Content = p.DisplayBody
			msg.Msg.Duration = time.Duration(p.DurationSeconds * float64(time.Second))
			msg.Msg.RetainAsHistory = p.RetainAsHistory
			s.notifyUI(msg)
		}
	case event.RunEventToolOutputDelta:
		var p event.ToolCallOutputDeltaPayload
		if json.Unmarshal(evt.Payload, &p) == nil && p.Text != "" {
			msg := subagentToolStepMsg(evt, p.StepID, p.ToolName, p.Summary, p.ToolMeta, evt.Type)
			msg.Msg.Content = p.Text
			msg.Msg.ToolOutputDelta = true
			s.notifyUI(msg)
		}
	case event.RunEventPlanUpdated:
		var p event.PlanUpdatedPayload
		if json.Unmarshal(evt.Payload, &p) == nil && (len(p.Items) > 0 || strings.TrimSpace(p.Title) != "" || p.Total > 0) {
			s.notifyUI(PlanUpdatedMsg{Payload: p})
		}
	case event.RunEventSubagentSpawned:
		var p event.SubagentSpawnedPayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			s.notifyUI(SubagentSpawnedMsg{AgentID: p.AgentID, AgentType: p.AgentType, TaskID: p.TaskID, Title: p.Title, Task: p.Task, ParentToolCallID: p.ParentToolCallID, TaskIndex: p.TaskIndex, ExecutionID: p.ExecutionID, Timestamp: evt.CreatedAt})
		}
	case event.RunEventSubagentEnded:
		var p event.SubagentEndedPayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			s.notifyUI(SubagentEndedMsg{AgentID: p.AgentID, AgentType: p.AgentType, TaskID: p.TaskID, Status: p.Status, Error: p.Error, Output: p.Output, ParentToolCallID: p.ParentToolCallID, TaskIndex: p.TaskIndex, ExecutionID: p.ExecutionID, Timestamp: evt.CreatedAt})
		}
	// Both approval branches below are replay-and-subagent only: their guard
	// stays. A conversation approval never reaches them, because the live
	// surface already shows its gate as a tool card and its answer as the
	// confirmation line, so notifying here as well would stack a second card and
	// a second line on top of the ones on screen. What the conversation's own
	// approval needs from the event log is on the replay side (pkg/tui/commands.go):
	// the gate is the tool call itself, and the confirmation line is a frame.
	case event.RunEventApprovalReq:
		var p event.ApprovalRequestedPayload
		if json.Unmarshal(evt.Payload, &p) == nil && strings.TrimSpace(p.AgentID) != "" {
			s.notifyUI(replayApprovalMessage(evt, p.AgentID, p.ActionID, p.ActionKind, "waiting approval", p.Message))
		}
	case event.RunEventApprovalResolved:
		var p event.ApprovalResolvedPayload
		if json.Unmarshal(evt.Payload, &p) == nil && strings.TrimSpace(p.AgentID) != "" {
			s.notifyUI(replayApprovalMessage(evt, p.AgentID, p.ActionID, p.ActionKind, firstNonEmpty(p.Decision, "resolved"), p.Reason))
		}
	case event.RunEventGoalStarted, event.RunEventGoalRoundStarted, event.RunEventGoalCompleted:
		if msg, ok := goalEventMessage(evt); ok {
			s.notifyUI(msg)
		}
	case event.RunEventContextCompacting:
		var p event.ContextCompactingPayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			s.notifyUI(ContextCompactingMsg{CompactionID: p.CompactionID, AgentID: p.AgentID, Trigger: p.Trigger, TokensBefore: p.TokensBefore})
		}
	case event.RunEventContextCompactProgress:
		var p event.ContextCompactProgressPayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			s.notifyUI(ContextCompactProgressMsg{CompactionID: p.CompactionID, AgentID: p.AgentID, Percent: p.Percent, Phase: p.Phase})
		}
	case event.RunEventContextCompactError:
		var p event.ContextCompactFailedPayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			s.notifyUI(ContextCompactFailedMsg{CompactionID: p.CompactionID, AgentID: p.AgentID, Error: p.Error, Cancelled: p.Cancelled})
		}
	case event.RunEventContextCompacted:
		var p event.ContextCompactedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return nil
		}
		s.notifyUI(ContextCompactedMsg{Payload: p})
		// The footer reads the last response's prompt, which described the
		// history that was just replaced. Until the next response reports
		// again, the checkpoint's own size is what the context holds. A
		// subagent's compaction leaves the conversation's context as it was.
		if strings.TrimSpace(p.AgentID) == "" {
			if budget, ok := s.tokenBudgetMessageFromUsage(p.TokensAfter); ok {
				s.notifyUI(budget)
			}
		}
	// A turn error is durable and it is the surface's own conversation, so the
	// live path renders it here rather than only on replay. Without this branch
	// the event was written to the session log and nothing ever showed it: the
	// live error block came from the run's return value and the log had no
	// reader, so a failure that only ever existed as an event — an MCP startup
	// failure, which has no run of its own to return from — reached the user's
	// transcript only after a resume.
	case event.RunEventTurnError:
		s.notifyTurnError(evt)
	// The engine's auto-continue lifecycle: the wait is drawn from these, so a
	// continuation armed, cancelled or fired by any path reads the same.
	case event.RunEventAutoContinueScheduled, event.RunEventAutoContinueCancelled:
		if msg, ok := autoContinueUIMessage(evt); ok {
			s.notifyUI(msg)
		}
	}
	return nil
}

// notifyTurnError draws one turn-error event the way the live path does. A
// producer whose event has no session to be persisted against — a startup
// failure that happened before the conversation existed — draws it through here
// so the user still sees it.
func (s *ChatSession) notifyTurnError(evt event.RunEvent) {
	var p event.TurnErrorPayload
	if json.Unmarshal(evt.Payload, &p) != nil {
		return
	}
	text := turnErrorDisplayText(p)
	if text == "" {
		return
	}
	s.notifyUI(NewMessageMsg{Msg: Message{
		Kind: MsgKindError, Content: text, Title: p.Title, Timestamp: evt.CreatedAt,
	}})
}

// turnErrorDisplayText is the body a turn-error event shows: the runtime's
// sentence when it has one, else the message, else the detail's provider text.
// Both the live path and the replay path read it, so a failure reads the same
// whether it just happened or was replayed from the log.
func turnErrorDisplayText(p event.TurnErrorPayload) string {
	if text := strings.TrimSpace(p.Error); text != "" {
		return text
	}
	if text := strings.TrimSpace(p.Message); text != "" {
		return text
	}
	if p.Detail != nil {
		return strings.TrimSpace(p.Detail.ProviderMessage)
	}
	return ""
}

// publishTUIRunEvent records one lifecycle transition the terminal surface
// itself produced (approval recovery and cancellation in particular) on the
// conversation's event log. Runtime tool hooks perform the same write at their
// own event boundary; these surface-only paths do not pass through those hooks.
func (s *ChatSession) publishTUIRunEvent(ctx context.Context, sessionID, runID, eventType string, payload any) error {
	if s == nil || s.runSvc() == nil {
		return nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("publish TUI run event: run id is required")
	}
	return s.publishRunEvent(ctx, event.NewRunEvent("", runID, strings.TrimSpace(sessionID), eventType, payload, time.Now()))
}

// persistRunEvent stores one canonical event in the conversation's log without
// drawing it: the surface already painted the card from the step hook that
// observed the call, and this row is what a later resume replays.
func (s *ChatSession) persistRunEvent(ctx context.Context, evt event.RunEvent) {
	if s == nil || s.runSvc() == nil || strings.TrimSpace(evt.SessionID) == "" {
		return
	}
	_, _, _ = s.runSvc().AppendSessionEventOnce(ctx, state.SessionEvent{
		ID: evt.ID, RunID: evt.RunID, SessionID: evt.SessionID, Type: evt.Type,
		Payload: append(json.RawMessage(nil), evt.Payload...), CreatedAt: evt.CreatedAt,
	})
}

// mcpStartupErrorEvent builds the durable record of one MCP server's startup
// failure.
//
// The event ID is derived from the failure rather than generated per attempt, so
// the store can refuse a repeat: a surface that resubscribes replays the current
// snapshot, and every resubscription would otherwise append the same failure
// again. RunID stays empty because no run is what failed — the server did, before
// any turn existed — and inventing one would file the failure against work that
// never happened.
func mcpStartupErrorEvent(sessionID string, generation string, rec mcp.ServerRecord, now time.Time) event.RunEvent {
	payload := event.TurnErrorPayload{
		Error:      rec.Error,
		Message:    rec.Error,
		Title:      mcpFailureTitle(rec.Name),
		Source:     event.MCPStartupErrorSource,
		Server:     rec.Name,
		Generation: generation,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		body = json.RawMessage(`{}`)
	}
	return event.RunEvent{
		ID:        mcpFailureEventID(generation, rec.Name),
		SessionID: sessionID,
		Type:      event.RunEventTurnError,
		Payload:   body,
		CreatedAt: now,
	}
}

// mcpFailureEventID is the stable identity of one server's failure in one
// generation. Two failures of the same server in different generations are two
// events, which is what makes a retry visible.
func mcpFailureEventID(generation, server string) string {
	return "mcp:" + strings.TrimSpace(generation) + ":" + strings.TrimSpace(server) + ":error"
}

// mcpFailureTitle names the failure in the heading the reader sees first.
func mcpFailureTitle(server string) string {
	if name := strings.TrimSpace(server); name != "" {
		return "mcp " + name
	}
	return "mcp"
}

// MCPStatusMsg carries one MCP startup snapshot to the main loop. It is a UI
// message rather than a transcript frame: it describes work in progress, not
// something the conversation said, and it must never be persisted as history.
type MCPStatusMsg struct {
	Snapshot run.MCPSnapshot
	Progress run.MCPStartupProgress
}

// WatchMCPStartup follows the runner's MCP startup for one session: it records
// each server's failure against that session and reports progress to the UI.
//
// It is per session because both halves are: a failure is appended to the
// conversation the user has open, and the progress line belongs to the screen
// showing it. Switching sessions re-arms the watch for the new one, which also
// means a startup that happened before the switch is still found — the
// subscription replays the registry's current state.
//
// Idempotent per session id, so a caller that cannot tell whether it is first (a
// resume, a switch) can simply call it.
func (s *ChatSession) WatchMCPStartup(sessionID string) {
	if s == nil {
		return
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return
	}
	s.mcpWatchMu.Lock()
	if s.mcpWatchSession == sid && s.mcpWatchCancel != nil {
		s.mcpWatchMu.Unlock()
		return
	}
	previous := s.mcpWatchCancel
	s.mcpWatchSession = sid
	s.mcpWatchMu.Unlock()
	if previous != nil {
		previous()
	}

	runner := s.runner()
	if runner == nil {
		return
	}
	cancel := runner.MCPStartup().Subscribe(func(snapshot run.MCPSnapshot) {
		s.recordMCPStartupFailures(sid, snapshot)
		// An empty snapshot is delivered too: it is how a generation says its
		// servers are gone (the registry closed with the Runner), and dropping it
		// would leave a startup line on screen for a startup that can no longer
		// finish. The progress it carries is zero, which is what ends the line.
		//
		// The progress travels with the snapshot rather than being asked of the
		// Runner here: this callback runs inside a registry's delivery lock, and
		// the Runner's answer would need the load lock — which the generation
		// that is currently writing to that registry holds.
		s.notifyUI(MCPStatusMsg{Snapshot: snapshot, Progress: snapshot.Progress})
	})

	s.mcpWatchMu.Lock()
	if s.mcpWatchSession != sid {
		// A newer session took over while this one subscribed; that watch owns
		// the subscription now.
		s.mcpWatchMu.Unlock()
		cancel()
		return
	}
	if s.mcpWatchCancel != nil {
		s.mcpWatchCancel()
	}
	s.mcpWatchCancel = cancel
	s.mcpWatchMu.Unlock()
}

// recordMCPStartupFailures writes one durable event per failed server of this
// snapshot, off the delivery path.
//
// It runs on its own goroutine because the caller is a registry subscription
// callback: that runs inside the registry's delivery lock — and, for the replay
// a subscription starts with, inside the Runner's load lock as well — so a
// database write here would hold up every other server's status transition, and
// the settling generation behind them.
//
// Repeats are harmless by construction: the event id is derived from the
// generation and the server, so the store keeps the first one and reports the
// rest as already present. That is also why the goroutines need no ordering
// between them.
func (s *ChatSession) recordMCPStartupFailures(sessionID string, snapshot run.MCPSnapshot) {
	var failures []mcp.ServerRecord
	for _, rec := range snapshot.Servers {
		// A failure with no text is still a failure, but the event exists to
		// carry the text the reader needs, so a record without one has nothing
		// to record. cancelled is deliberately not a failure — a server the
		// operator skipped must not appear as an error.
		if rec.ConnStatus != mcp.ConnStatusError || strings.TrimSpace(rec.Error) == "" {
			continue
		}
		failures = append(failures, rec)
	}
	if len(failures) == 0 {
		return
	}
	generation := snapshot.Generation
	now := time.Now().UTC()
	go func() {
		bg := context.Background()
		for _, rec := range failures {
			// Persist first, then let the store decide whether this is new: the
			// transcript shows a failure that is already in the log, and a
			// resubscription replays the same snapshot without appending it
			// twice. A failure observed before the conversation exists is shown
			// and not stored (state.ErrSessionNotStarted): no session's history
			// may claim work that happened before the session did, and a resume
			// restarts MCP, recording a failure then against the session that
			// exists by then.
			_ = s.publishRunEvent(bg, mcpStartupErrorEvent(sessionID, generation, rec, now))
		}
	}()
}

// SkipOptionalMCPStartup cancels the MCP servers the run can do without and
// reports whether it cancelled anything.
//
// It is the interactive half of the ready barrier, and it is deliberately
// all-or-nothing: false means nothing was skipped — no startup is in flight, or
// only required servers are left — and the caller must fall through to its own
// behaviour rather than claim a skip that did not happen.
func (s *ChatSession) SkipOptionalMCPStartup() bool {
	if s == nil {
		return false
	}
	runner := s.runner()
	if runner == nil {
		return false
	}
	return runner.MCPStartup().CancelOptional()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// subagentToolStepMsg renders a tool step that reached the surface as a run
// event rather than through the StepHook. Provider-executed web search is the
// case that needs it: it has no client-side call, so a subagent's search is
// published by that subagent's own stream sink, and the AgentID on the meta is
// what routes the card into the subagent's view instead of the conversation.
func subagentToolStepMsg(evt event.RunEvent, stepID, toolName, summary string, meta event.ToolCallMeta, phase string) NewMessageMsg {
	return NewMessageMsg{Msg: Message{
		Kind:     MsgKindTool,
		StepID:   strings.TrimSpace(stepID),
		ToolName: strings.TrimSpace(toolName),
		Summary:  strings.TrimSpace(summary),
		ToolMeta: tool.ToolMeta{
			ToolName:     strings.TrimSpace(meta.ToolName),
			Status:       strings.TrimSpace(meta.Status),
			Purpose:      strings.TrimSpace(meta.Purpose),
			Invocation:   strings.TrimSpace(meta.Invocation),
			Input:        meta.Input,
			AgentID:      strings.TrimSpace(meta.AgentID),
			AgentType:    strings.TrimSpace(meta.AgentType),
			AgentKind:    strings.TrimSpace(meta.AgentKind),
			Category:     strings.TrimSpace(meta.Category),
			SkillName:    strings.TrimSpace(meta.SkillName),
			SkillPath:    strings.TrimSpace(meta.SkillPath),
			ResultLines:  meta.ResultLines,
			ResultOffset: meta.ResultOffset,
			StartedAtMs:  meta.StartedAtMs,
		},
		ToolPhase: phase,
		AgentID:   strings.TrimSpace(meta.AgentID),
		RunID:     strings.TrimSpace(evt.RunID),
		Timestamp: evt.CreatedAt,
	}}
}

func openProcessChatSession(ctx context.Context, cfg config.Root, cwd string) (*ChatSession, error) {
	env, err := process.Open(ctx, process.OpenOptions{
		Config:        &cfg,
		LaunchDir:     cwd,
		SessionSource: memory.SessionSourceTUI,
	})
	if err != nil {
		return nil, err
	}
	log, err := newChatDiskLog(env.Root)
	if err != nil {
		env.Close()
		return nil, err
	}
	runner := env.Runner
	if runner == nil || env.Deps.SessionStore == nil || env.Hooks == nil {
		log.Close()
		env.Close()
		return nil, fmt.Errorf("process environment is incomplete")
	}
	s := &ChatSession{
		Env:        env,
		tuiControl: env.Control,
		// Core is replaced below, once s exists: the executor's per-surface hooks
		// close over the session itself.
		Core:          turn.New(turn.WithSessionStore(env.Deps.SessionStore), turn.WithSessionSource(memory.SessionSourceTUI)),
		WorkingDir:    strings.TrimSpace(cwd),
		LaunchProject: env.LaunchProject,
		chatLog:       log,
	}
	s.Core = turn.New(
		turn.WithSessionStore(env.Deps.SessionStore),
		// The run-step ledger backs the projected history this session serves
		// (SurfaceSessionPlanUpdates replays the conversation's plan cards from
		// it); without the store those reads silently come back empty.
		turn.WithRunEventStore(env.Deps.RunRT),
		// No ForegroundLocker: unlike the gateway, the TUI's serialization
		// boundary is DispatchSurfaceTurn, which holds the foreground lock
		// across the dispatch AND the interactive approval loop that follows it.
		// That loop sits above Submit and cannot be covered by it, so the surface
		// owns the wider boundary. session.Locker is a capacity-1 channel
		// semaphore, not a reentrant mutex, so configuring one here too would
		// deadlock the second acquisition against the first.
		turn.WithSessionSource(memory.SessionSourceTUI),
		// A turn stopped by a spent usage allowance continues by itself once
		// the allowance returns. The engine keeps the wait and the terminal
		// shows it: the lifecycle arrives through the same event path as every
		// other run event, and the continuation is handed to the event loop.
		turn.WithAutoContinue(turn.AutoContinueConfig{
			Continue: s.continueAfterUsageLimit,
			Events:   event.SinkFunc(s.publishRunEvent),
			Surfaces: []turn.Surface{turn.SurfaceTUI},
		}),
		turn.WithRunExecutor(env.NewRunExecutor(memory.SessionSourceTUI, process.RunExecutorOptions{
			PreviewMax: 4096,
			Finish:     s.tuiFinish,
			BeforeAgent: func(agentCtx context.Context, runID, _, _ string) error {
				if hook := s.turnBeforeAgent; hook != nil {
					return hook(agentCtx, runID)
				}
				return nil
			},
		})),
	)
	// A hot reload has to honour the /permissions preset the same way the
	// slash-command reload path does; the environment cannot see it otherwise.
	env.OnConfigLoaded = s.reapplyPermissionPreset
	env.OnConfigReload = func(next *config.Root) {
		if next == nil {
			return
		}
		// The environment has already published next to Deps, so the session
		// reads it through s.cfg() without being told. What it does own are the
		// pointers it handed out itself.
		s.configApplyMu.Lock()
		s.adoptConfig(next)
		s.configApplyMu.Unlock()
	}
	s.pipe().Add("00-project-context", s.projectContextPreHook)
	runner.Events = event.SinkFunc(s.publishRunEvent)
	s.userHooks = newUserHooks(s, env)
	process.StartConfigHotReload(ctx, env)
	return s, nil
}

func newUserHooks(s *ChatSession, env *process.Environment) *hook.Runtime {
	runner := env.Runner
	return &hook.Runtime{
		Home:          env.Root,
		WorkspaceRoot: runner.StateRoot(),
		Cfg:           s.cfg(),
		Sess:          env.Deps.SessionStore,
		Actions:       env.Deps.Actions,
		NewPromptRunner: func(label string) (hook.PromptRun, error) {
			fac := run.Factory{
				Home:          env.Root,
				WorkspaceRoot: runner.StateRoot(),
				ProjectKey:    runner.ProjectKey,
				ProjectRoot:   runner.ProjectRoot,
				MemoryStore:   env.Deps.MemoryStore,
				AppCfg:        s.cfg(),
			}
			rr := fac.NewIsolatedRunner(label)
			if rr == nil {
				return nil, fmt.Errorf("nil isolated runner")
			}
			if err := rr.Load(); err != nil {
				return nil, err
			}
			return func(ctx context.Context, input string) (string, error) {
				return run.RunText(rr, ctx, input)
			}, nil
		},
		RunAgentHook: runner.RunAgentHook,
		SessionID:    llm.AgentSessionIDFromContext,
		ToolUseID:    tool.ToolUseIDFromContext,
	}
}

type Session interface {
	DispatchSurfaceTurn(ctx context.Context, submission turn.TurnSubmission) error
	RunSurfaceShellCommand(ctx context.Context, sessionID string, channel string, rawInput string) error
	SurfaceTranscript(ctx context.Context, sessionID string, limit int) (string, error)
	// Structured transcript for reducer replay on --resume. Returns the entire
	// user-visible session transcript; model-only runtime messages are omitted.
	SurfaceTranscriptMessages(ctx context.Context, sessionID string) ([]state.Message, error)
	// Active model-context turns for budget accounting on resume. Unlike
	// SurfaceTranscriptMessages this honors compaction and context-reset floors, so
	// visually replaying full history never restores cleared token usage.
	SurfaceActiveContextMessages(ctx context.Context, sessionID string) ([]state.Message, error)
	// SurfaceComposerTokenStats is the footer's budget for a context holding
	// usage tokens, computed the way every live budget update computes it.
	SurfaceComposerTokenStats(usage int) ComposerTokenStats
	ResumeSession(ctx context.Context, sessionID string) (id string, title string, warning string, err error)
	// ActivateSessionModel puts the runner on sessionID's own stored model
	// selection (or the config default when it has none), returning a
	// non-fatal warning when a stored choice fell back. It is idempotent for
	// a session whose selection already matches the live runner, because
	// startup resume and a switch to the same session can both reach it.
	ActivateSessionModel(ctx context.Context, sessionID string) (warning string, err error)
	// SelectModel applies a model choice as sessionID's live runtime model
	// and persists it as that session's own selection. Partial outcomes
	// travel as turn.ModelSelectionError (applied-not-durable,
	// runtime-uncertain), never as bare message text.
	SelectModel(ctx context.Context, sessionID string, choice turn.ModelChoice, effort *string) error
	PreferredSurfaceTranscriptSessionID(ctx context.Context) string
	NewSessionID(prefix string) string
	ListSessionsRecent(ctx context.Context, limit int) ([]SessionSummary, error)
	ModelSummaryString() string
	SkillListString() string
	AvailableSkillToggleOptions() []skill.Entry
	// ReloadConfig re-reads the config file into the running session, so a
	// provider written by /connect governs the next turn without a restart.
	// It belongs in the interface rather than behind a runtime type assertion:
	// a surface that cannot do this reduces /connect to a command that saves a
	// file and changes nothing, which is worth catching at compile time.
	ReloadConfig() error
	CurrentModelOption() string
	CurrentModelReasoningEffort() string
	// SubagentModelSummary names the model and reasoning effort one subagent
	// type runs on, reporting false when that type has no LLM chain of its own
	// and so runs on the primary agent's model. A subagent's own view names
	// what that subagent is running, not what the conversation is running.
	SubagentModelSummary(agentType string) (model string, effort string, ok bool)
	ApplySkillSelection(label string) (string, error)
	ApplySkillEnabledSelection(enabledPaths []string) (string, error)
	// MemorySkillOptions lists the skills the memory consolidation agent wrote.
	// They are proposals: nothing there reaches the model until the user
	// promotes it, which is what PromoteMemorySkills does.
	MemorySkillOptions() []MemorySkillOption
	PromoteMemorySkills(names []string) (string, error)
	// InstallSkillPackageAsync starts an install and returns immediately. A
	// package is fetched over the network, so the surface reports it through
	// notifications rather than holding the event loop for its duration.
	InstallSkillPackageAsync(ctx context.Context, sourceRef string, destScope string)
	// SkillSelectionByName resolves a /skills picker entry to the trusted
	// explicit-skill selection (name and SKILL.md path) the runner preload
	// loads. Authoring and installing skills is the skill workshop's job,
	// reached this way — the surface does not write SKILL.md files behind the
	// model's back.
	SkillSelectionByName(name string) (skillName string, skillPath string, err error)
	HandleStatusSlash(sessionID, channel string, side bool) (string, bool)
	HandlePermissionsSlash(sessionID, channel string, args []string) (string, bool)
	// PermissionPresets lists the built-in approval presets as they apply to
	// this session's settings, and names the one it is running under — ""
	// when its approval mode and sandbox do not add up to one, which a
	// settings file is free to arrange.
	PermissionPresets(sessionID string) (presets []safety.ApprovalPreset, current string)
	ApplyPermissionPreset(sessionID, presetID string) (string, error)
	HandleMCPSlash(sessionID, channel string) (string, bool)
	// HandleSandboxSlash returns the sandbox report. It reports only — the
	// sandbox is chosen with /permissions, which moves it together with the
	// approval policy.
	HandleSandboxSlash(sessionID, channel string, args []string) (string, bool)
	HandleDiffSlash(sessionID, channel string, args []string) (string, bool)
	ExecuteSurfaceSlash(ctx context.Context, sessionID string, line string) (SlashOutcome, bool)
	// ChooseSurfaceSlash applies a choice made in the picker a slash command
	// offered.
	ChooseSurfaceSlash(ctx context.Context, sessionID string, choice turn.SlashChoice) SlashOutcome
	PrependUINotify(fn func(any))
	// StartApprovalRecovery drains the approval decisions of one conversation
	// that were committed while no process was driving it. The surface calls it
	// with the session it has opened, so an interrupted approval is reported to
	// the conversation it belongs to and nowhere else.
	StartApprovalRecovery(sessionID string)
	SetToolApprovalSink(sink turn.ToolApprovalDecisionSink)
	CancelActiveRun() bool
	SteerSurfaceRun(sessionID string, channel string, parts []llm.ContentPart) bool
	QueueSurfaceFollowUp(sessionID string, channel string, parts []llm.ContentPart) bool
	// RetractSurfaceSteer removes the most recently enqueued steer from the
	// active run's input runtime so it can be pulled back into the composer for
	// editing. Returns false when no steer is still pending (already delivered
	// at a tool boundary, or no active run), which is what makes the queue
	// preview's "edit last queued message" hint honest for pending steers.
	RetractSurfaceSteer(sessionID string, channel string) bool
	// SurfacePendingSteerCount reports how many steers the active run still has
	// queued (enqueued but not yet delivered to the model at a tool boundary).
	// ok is false when no run is active. Steers are enqueued into the runtime in
	// lockstep with the local mirror and drained in FIFO order, so the surviving
	// runtime steers are always a suffix of the mirror; the caller uses this
	// count to drop already-delivered (leading) entries from its mirror.
	SurfacePendingSteerCount(sessionID string, channel string) (count int, ok bool)
	// IsFastMode reports whether the active session has /fast enabled.
	// Anthropic Opus only — sessionFastAvailable() guards the slash UI so
	// non-Opus models never expose the toggle.
	IsFastMode() bool
	// SetFastMode persists the /fast toggle for the active state.
	// Implementations should reject when the active model is not Opus.
	SetFastMode(enabled bool) error
	AgentRosterSnapshot(sessionID string) AgentRosterSnapshot
	CancelSubagent(query SubagentControlQuery) bool
	CancelAllAgents() AgentCancelSummary
	// ApprovalDisplayRecorder is embedded rather than left to a type
	// assertion: a surface that cannot persist what it drew is exactly the
	// defect this contract exists to prevent, so it belongs in the interface
	// every session implementation has to satisfy.
	ApprovalDisplayRecorder
}

type SessionSummary = state.SessionSummary

type SlashOutcome = turn.SlashOutcome

type InputAttachment = turn.InputAttachment

type PendingPaste = turn.PendingPaste

type AgentRosterRow struct {
	ID       string
	Kind     string
	ParentID string
	Label    string
	Status   string
	// Title is the short name of what this agent was dispatched to do; Task is
	// the full prompt. The row shows the title — the prompt belongs to the
	// agent's own view, not to a one-line roster entry.
	Title          string
	Task           string
	SessionID      string
	RunID          string
	ElapsedSeconds int
	ToolCount      int
	Activity       string // current tool call description for fanout third layer
	FileCount      int
}

type AgentRosterSnapshot struct {
	Rows []AgentRosterRow
}

// MemorySkillOption is one skill the memory consolidation agent proposed, with
// everything the picker must show before a person approves promoting it: what
// it is, how it compares to any copy already promoted, what promoting it would
// newly expose, and why it cannot be promoted when that is the case.
type MemorySkillOption struct {
	Name        string
	Description string
	// Status compares the proposal to the promoted copy: new, up-to-date,
	// outdated (consolidation rewrote the source), or diverged (the promoted
	// copy was edited, and promoting again would overwrite those edits).
	Status string
	// SlashCommand is the command promotion would add.
	SlashCommand string
	// Shadows is the path of a same-named skill from a lower-priority root that
	// a promoted copy would take precedence over, empty when there is none.
	Shadows string
	// Blocked explains why this proposal cannot be promoted, empty when it can.
	Blocked string
	// Promoted reports whether a promoted copy exists on disk.
	Promoted bool
	// LoadedInSession reports whether that copy is in this session's tool table.
	// Promotion never rebuilds it, so a freshly promoted skill stays false until
	// the next session — promoted but not yet active.
	LoadedInSession bool
}

type SubagentControlQuery struct {
	AgentID string
	TaskID  string
	RunID   string
}

type AgentCancelSummary struct {
	Main      int
	Subagents int
}

type ComposerState struct {
	Text          string
	DraftText     string
	Cursor        int
	Attachments   []InputAttachment
	PendingPastes []PendingPaste
	// SlashOverlay is non-nil when the non-modal slash-command picker is
	// active or has locked a HintCommand. Owned by run.go's event loop;
	// see slash_overlay.go.
	SlashOverlay *SlashOverlay
	// MentionOverlay is non-nil while the @ file-mention typeahead is open.
	MentionOverlay *MentionOverlay
	// MentionDismissedDraft remembers the draft at the moment the user
	// pressed Esc on the mention overlay, suppressing re-open until the
	// draft changes.
	MentionDismissedDraft string
}

// HandleDraftUpdate is the single entry point for applying a new DraftText
// value coming out of input_events. It owns the SlashOverlay invariants:
// activate on "/", update via SlashOverlay.UpdateFromDraft, clear when the
// draft drops the leading slash. Mid-line slashes (e.g. "hello /co") never
// activate the overlay because the activation guard requires the composer to
// show nothing but "/" to *start* a session; subsequent updates flow through
// UpdateFromDraft which also rejects non-"/" prefixes.
//
// Both guards read the draft through composerDisplayText, the same view the
// composer card is drawn from, so the picker opens exactly when the user can
// see a bare slash. The mention overlay is deliberately left on the raw draft:
// it addresses a token by byte offsets that are spliced back into that buffer.
func (c *ComposerState) HandleDraftUpdate(draft string, session Session) {
	if c == nil {
		return
	}
	c.DraftText = draft
	c.Cursor = composerCursorClamp(draft, c.Cursor)
	if c.SlashOverlay == nil {
		if composerDisplayText(draft) == "/" {
			c.SlashOverlay = newSlashOverlay()
			c.SlashOverlay.rebuildFiltered(session)
		}
	} else if !c.SlashOverlay.UpdateFromDraft(draft, session) {
		c.SlashOverlay = nil
	}
	c.updateMentionOverlay(draft)
}

type ClipboardImageReader interface {
	ReadClipboardImage(ctx context.Context) (InputAttachment, error)
}

type ClipboardImageFunc func(ctx context.Context) (InputAttachment, error)

type ToolApprovalRequest = turn.ToolApprovalRequest
type ToolApprovalDecision = turn.ToolApprovalDecision
type PermissionDestination = safety.PermissionDestination
type PermissionMode = safety.PermissionMode
type PermissionUpdate = safety.PermissionUpdate
type PermissionRuleValue = safety.PermissionRuleValue

type Options struct {
	In               io.Reader
	Out              io.Writer
	Err              io.Writer
	Session          Session
	InitialSessionID string
	Banner           string
	Version          string
	Clipboard        ClipboardImageReader
	InputHistoryPath string
	WorkingDirectory string
	Home             string
	// WorkspaceRoot is the active primary agent's workspace root, onto which
	// the mode store joins "state". The TUI plan-mode hotkey reads/writes mode
	// here, so it must match the root the agent runtime uses or the toggle and
	// the agent will disagree on plan state. Falls back to <home>/workspace.
	WorkspaceRoot string
}

func (f ClipboardImageFunc) ReadClipboardImage(ctx context.Context) (InputAttachment, error) {
	if f == nil {
		return InputAttachment{}, nil
	}
	return f(ctx)
}

// PanelCloseMsg asks the main loop to close the interactive slash panel: an
// approval or question needs the composer area. The loop closes Done once the
// panel is down, and the approval paints only after that — closing the panel
// clears the overlay area, which would otherwise erase the approval.
type PanelCloseMsg struct {
	Reason string
	Done   chan struct{}
}

// MCPStatusTickMsg tells the main loop an MCP server changed state while the
// /mcp panel is open, so the panel repaints in place.
type MCPStatusTickMsg struct{}

// MCPAuthMsg reports a panel-started OAuth flow to the main loop: first the
// URL to open, then the outcome sentence once the flow ends.
type MCPAuthMsg struct {
	Server string
	URL    string
	Result string
}

// MCPResourcesMsg delivers a server's resource list, fetched off the main
// loop for the /mcp panel; Err is the underlying error verbatim.
type MCPResourcesMsg struct {
	Server string
	Items  []mcp.ResourceInfo
	Err    string
}

// Auto-continue in the terminal: the notice while a conversation stopped by a
// usage limit waits, Esc or typing to cancel it, and the continuation turn when
// the wait ends. The waiting itself is the turn engine's; this section only
// shows it and hands the engine's continuation to the event loop.

// autoContinueNoticeStyle and autoContinueNoticeGlyph draw the notice under
// the composer as a warning: it announces something that will happen without
// the reader doing anything, which is exactly what they must be able to notice.
const (
	autoContinueNoticeStyle = "\x1b[38;5;214m"
	autoContinueNoticeGlyph = "⚠ "
)

// AutoContinueScheduledMsg reports a continuation armed by the engine: the
// conversation will resume by itself at ContinueAt.
type AutoContinueScheduledMsg struct {
	SessionID  string
	RunID      string
	ContinueAt time.Time
	Code       string
	Attempt    int
}

// AutoContinueCancelledMsg reports a continuation that ended without firing.
type AutoContinueCancelledMsg struct {
	SessionID string
	Reason    string
	Error     string
}

// AutoContinueDueMsg carries the engine's continuation to the event loop,
// which submits Prompt as the conversation's next turn.
type AutoContinueDueMsg struct {
	SessionID string
	Prompt    string
}

// autoContinueView is what the event loop knows about a pending continuation.
type autoContinueView struct {
	sessionID  string
	continueAt time.Time
	code       string
	// announced is whether the transcript line has been drawn. A limit that
	// ends a turn is announced after that turn's own error, so the line reads
	// as the answer to it rather than appearing above it.
	announced bool
}

// autoContinueCanceller is the optional half of Session that can stop a
// pending continuation. It is optional so surfaces without an engine behind
// them (tests, replay) need not grow a method they would stub.
type autoContinueCanceller interface {
	CancelAutoContinue(sessionID string) bool
}

// CancelAutoContinue stops the session's pending continuation on the reader's
// behalf, and reports whether there was one.
func (s *ChatSession) CancelAutoContinue(sessionID string) bool {
	if s == nil || s.Core == nil {
		return false
	}
	return s.Core.CancelAutoContinue(context.Background(), sessionID, turn.AutoContinueCancelledByUser)
}

// continueAfterUsageLimit is the engine's port into this surface. The turn is
// not run here: it is handed to the event loop, which owns the composer, the
// transcript and cancellation, and runs it the way it runs anything the reader
// submits.
func (s *ChatSession) continueAfterUsageLimit(_ context.Context, plan turn.AutoContinuePlan, prompt string) error {
	if s == nil {
		return turn.ErrAutoContinueUnavailable
	}
	s.tuiMu.Lock()
	attached := s.uiNotify != nil
	s.tuiMu.Unlock()
	if !attached {
		return turn.ErrAutoContinueUnavailable
	}
	s.notifyUI(AutoContinueDueMsg{SessionID: plan.SessionID, Prompt: prompt})
	return nil
}

// autoContinueUIMessage turns an auto-continue lifecycle event into the
// message the event loop acts on. started has no message of its own: the due
// message that follows it is what the loop acts on.
func autoContinueUIMessage(evt event.RunEvent) (any, bool) {
	switch evt.Type {
	case event.RunEventAutoContinueScheduled:
		var p event.AutoContinueScheduledPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return nil, false
		}
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(p.ContinueAt))
		if err != nil {
			return nil, false
		}
		return AutoContinueScheduledMsg{SessionID: evt.SessionID, RunID: evt.RunID, ContinueAt: at, Code: p.Code, Attempt: p.Attempt}, true
	case event.RunEventAutoContinueCancelled:
		var p event.AutoContinueCancelledPayload
		_ = json.Unmarshal(evt.Payload, &p)
		return AutoContinueCancelledMsg{SessionID: evt.SessionID, Reason: p.Reason, Error: p.Error}, true
	}
	return nil, false
}

// handleAutoContinueNotification applies one auto-continue message to the
// event loop's state, reporting false for any other message.
func handleAutoContinueNotification(renderer *Renderer, state *streamState, m any) bool {
	switch msg := m.(type) {
	case AutoContinueScheduledMsg:
		if state == nil || !sameSessionID(msg.SessionID, state.sessionID) {
			return true
		}
		state.autoContinue = &autoContinueView{sessionID: msg.SessionID, continueAt: msg.ContinueAt, code: msg.Code}
		renderer.SetAutoContinueNotice(autoContinueFooterText(msg.Code, msg.ContinueAt, time.Now()))
		if state.activeForeground == nil {
			state.announceAutoContinue(renderer)
		}
		renderComposerWithState(renderer, state)
		return true
	case AutoContinueCancelledMsg:
		if state == nil || state.autoContinue == nil || !sameSessionID(msg.SessionID, state.autoContinue.sessionID) {
			return true
		}
		view := state.autoContinue
		state.clearAutoContinue(renderer)
		// A turn the reader sent is its own explanation; anything else ending
		// the wait is said, so the notice already in scrollback is not left
		// promising a continuation that will never come.
		switch {
		case msg.Reason == turn.AutoContinueSuperseded:
		case msg.Reason == turn.AutoContinueUnavailable && view.announced:
			renderer.RenderFrame(Frame{Kind: FrameStatus, Title: autoContinueUnavailableText(msg.Error), Final: true})
		case view.announced:
			renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "Auto-continue cancelled", Final: true})
		}
		renderComposerWithState(renderer, state)
		return true
	case AutoContinueDueMsg:
		// Only a wait this loop is still showing is continued. A reader who
		// pressed Esc as the timer fired has cleared it, and the continuation
		// that raced the key is dropped rather than run against their wish.
		if state == nil || state.autoContinue == nil || !sameSessionID(msg.SessionID, state.autoContinue.sessionID) {
			return true
		}
		state.clearAutoContinue(renderer)
		prompt := strings.TrimSpace(msg.Prompt)
		if prompt == "" {
			return true
		}
		state.autoContinueDue = &ComposerSubmission{
			Text:        prompt,
			Parts:       []llm.ContentPart{llm.Text(prompt)},
			DisplayText: prompt,
		}
		renderComposerWithState(renderer, state)
		return true
	}
	return false
}

// announceAutoContinue draws the transcript line for a pending continuation,
// once.
func (s *streamState) announceAutoContinue(renderer *Renderer) {
	if s == nil || s.autoContinue == nil || s.autoContinue.announced {
		return
	}
	s.autoContinue.announced = true
	renderer.RenderFrame(Frame{
		Kind:  FrameStatus,
		Title: autoContinueTranscriptText(s.autoContinue.code, s.autoContinue.continueAt, time.Now()),
		Final: true,
	})
}

// cancelAutoContinue is Esc or typing while a continuation waits. It reports
// whether there was one, so Esc is consumed only when it did something.
func (s *streamState) cancelAutoContinue(renderer *Renderer) bool {
	if s == nil || s.autoContinue == nil {
		return false
	}
	sessionID := s.autoContinue.sessionID
	if canceller, ok := s.session.(autoContinueCanceller); ok && canceller.CancelAutoContinue(sessionID) {
		// The engine's cancellation event clears the notice and says so in
		// the transcript, the same way a cancel from any other surface does.
		return true
	}
	// Nothing left to cancel in the engine: the timer fired as the key was
	// pressed. Clearing here is what makes the racing continuation drop.
	announced := s.autoContinue.announced
	s.clearAutoContinue(renderer)
	if announced {
		renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "Auto-continue cancelled", Final: true})
	}
	return true
}

// leaveAutoContinue drops the pending or due continuation of the conversation
// being switched away from.
func (s *streamState) leaveAutoContinue(renderer *Renderer) {
	if s == nil {
		return
	}
	s.autoContinueDue = nil
	if s.autoContinue == nil {
		return
	}
	if canceller, ok := s.session.(autoContinueCanceller); ok {
		canceller.CancelAutoContinue(s.autoContinue.sessionID)
	}
	s.clearAutoContinue(renderer)
}

func (s *streamState) clearAutoContinue(renderer *Renderer) {
	if s == nil {
		return
	}
	s.autoContinue = nil
	renderer.SetAutoContinueNotice("")
}

// takeAutoContinueDue hands the loop a continuation that is due, once.
func (s *streamState) takeAutoContinueDue() (ComposerSubmission, bool) {
	if s == nil || s.autoContinueDue == nil {
		return ComposerSubmission{}, false
	}
	sub := *s.autoContinueDue
	s.autoContinueDue = nil
	return sub, true
}

// autoContinueWake tells the idle loop's wait to return so it can run a due
// continuation.
func (s *streamState) autoContinueWake() bool {
	return s != nil && s.autoContinueDue != nil
}

func sameSessionID(a, b string) bool {
	return strings.TrimSpace(a) == strings.TrimSpace(b)
}

// autoContinueHeadline names what stopped the conversation.
func autoContinueHeadline(code string) string {
	if code == string(llm.ExplainRateLimitThrottle) {
		return "Rate limited"
	}
	return "Usage limit reached"
}

// autoContinueTranscriptText is the line the transcript keeps.
func autoContinueTranscriptText(code string, at, now time.Time) string {
	return autoContinueHeadline(code) + " · continuing automatically at " + autoContinueClock(at, now) + " · esc or type to cancel"
}

// autoContinueFooterText is the live notice under the composer.
func autoContinueFooterText(code string, at, now time.Time) string {
	return autoContinueHeadline(code) + " · continuing automatically at " + autoContinueClock(at, now) + " · esc to cancel"
}

func autoContinueUnavailableText(detail string) string {
	text := "Auto-continue could not start"
	if detail = strings.TrimSpace(detail); detail != "" {
		text += ": " + detail
	}
	return text
}

// autoContinueClock renders the continuation time on the reader's clock, as
// coarsely as it can while staying unambiguous: the time alone today, the
// weekday within the week, the date beyond it.
func autoContinueClock(at, now time.Time) string {
	at = at.In(now.Location())
	clock := at.Format("3:04pm")
	ay, am, ad := at.Date()
	ny, nm, nd := now.Date()
	switch {
	case ay == ny && am == nm && ad == nd:
		return clock
	case at.After(now) && at.Sub(now) < 6*24*time.Hour:
		return at.Format("Mon") + " " + clock
	case ay == ny:
		return at.Format("Jan 2") + ", " + clock
	default:
		return at.Format("Jan 2 2006") + ", " + clock
	}
}
