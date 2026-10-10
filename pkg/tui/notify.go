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

// InputQueueChangedMsg is emitted when the conversation's input queue changes
// while a turn runs — a steer enqueued, refused, recalled or delivered, or a
// boundary consuming entries. The surface re-reads the queue: steers the model
// has received (InputQueue.TakeDelivered) move into the transcript so they do
// not silently disappear from the pending preview, and the composer's queue
// preview repaints.
type InputQueueChangedMsg struct {
	SessionID string
	Channel   string
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

// LSPRecommendationMsg asks the user whether to enable (or install and
// enable) a language server. It is shown as a modal; the answer goes back
// through ChatSession.DecideLSPRecommendation.
type LSPRecommendationMsg struct {
	Rec event.LSPRecommendation
}

// LSPInstallProgressMsg is one output line of an install the user started
// from a recommendation. It renders as the transient status line.
type LSPInstallProgressMsg struct {
	ServerID string
	Line     string
}

// LSPInstallDoneMsg ends that install.
type LSPInstallDoneMsg struct {
	ServerID string
	Text     string // the transcript line on success
	Err      string // the failure text, including the output tail
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
	// AgentID is the roster key of the agent whose context the budget
	// measures; empty is the primary agent's, the one the main footer shows.
	AgentID              string
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
	// The model this execution runs on, as the engine resolved it once at
	// dispatch: an override the user picked, a type's own chain, or nothing —
	// which means the conversation's model, and is left for the footer to
	// name only when the spawn actually said one.
	ModelProvider   string
	Model           string
	ReasoningEffort string
	Timestamp       time.Time
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
	// FinishedAt is when this execution actually stopped — the ended event's
	// payload when it carries one, the event's own timestamp otherwise. A
	// task row's final elapsed time is measured to here, so a reaped
	// execution reports the moment it was last seen, not the moment the
	// reaper said so.
	FinishedAt time.Time
	Timestamp  time.Time
}

// SubagentInputDeliveredMsg is a message the user sent a running subagent that
// was handed to its model at a tool boundary (the engine's
// subagent_input_delivered event). The reducer draws it as a user message in
// that subagent's own view, the way a delivered steer is drawn in the
// conversation, so a message queued and then delivered does not silently
// vanish from the preview.
type SubagentInputDeliveredMsg struct {
	AgentID string
	RunID   string
	Text    string
}

// SubagentInputBoundaryMsg is what a subagent's queue decided when one of its
// executions ended. Send is the queued input to run next, as one message the
// surface hands back to SendToSubagent; Restore is the input to give back to
// that subagent's composer. At most one is non-empty.
type SubagentInputBoundaryMsg struct {
	AgentKey string
	Send     []ComposerSubmission
	Restore  []ComposerSubmission
}

// PlanReviewStartedMsg opens a plan review's card. The review request itself is
// the review run's dispatch: the reviewer's subagent_spawned names the ReviewID
// as its parent tool call, so the card binds its task exactly the way a
// subagent_run card does.
type PlanReviewStartedMsg struct {
	ReviewID string
	Provider string
	Model    string
	Label    string
}

// PlanReviewReviewedMsg closes the card PlanReviewStartedMsg opened, with the
// review's outcome: "done", "stopped", "timed_out" or "failed". A failure whose
// reviewer never started (the model is not configured, there is no plan) reads
// "Failed to start"; the reviewer's own failures arrive as its ended event.
type PlanReviewReviewedMsg struct {
	ReviewID string
	Provider string
	Model    string
	Label    string
	Outcome  string
	Error    string
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
		s.notifyUIForSession(sessionID, NewMessageMsg{Msg: Message{
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
		s.notifyToolBatchSummary(ctx, sessionID, evt)
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
		s.notifyUIForSession(sessionID, NewMessageMsg{Msg: Message{
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
	s.notifyUIForSession(sessionID, NewMessageMsg{Msg: Message{
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

func (s *ChatSession) notifyToolBatchSummary(ctx context.Context, sessionID string, evt tool.StepEvent) {
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
	s.notifyUIForSession(sessionID, NewMessageMsg{Msg: Message{
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
// in their arguments).
func extractFilePathFromEvent(evt tool.StepEvent) string {
	// A read_file whose path was repaired into the skill catalog read a
	// different file than the arguments name; the card has to name the file
	// that was actually read.
	if fp := tool.RepairedReadPath(evt); fp != "" {
		return fp
	}
	return extractFilePathFromInput(evt.Input)
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
	// The store has said its piece (or had no session row to say it against):
	// recording and painting are two decisions, and a fact this surface has
	// already painted once in this conversation is not painted again. This is
	// the publishing half's counterpart of the store's dedupe for the window
	// where no session row exists yet — see markEventShownOnce.
	if !s.markEventShownOnce(evt.SessionID, evt.ID) {
		return nil
	}
	switch evt.Type {
	case event.RunEventAssistantDelta:
		var p event.AssistantDeltaPayload
		if json.Unmarshal(evt.Payload, &p) == nil && p.Text != "" {
			s.notifyUIForSession(evt.SessionID, NewMessageMsg{Msg: Message{Kind: MsgKindAssistant, Content: p.Text, AgentID: p.AgentID, RunID: evt.RunID, Timestamp: evt.CreatedAt}})
		}
	case event.RunEventReasoningDelta:
		var p event.ReasoningDeltaPayload
		if json.Unmarshal(evt.Payload, &p) == nil && p.Text != "" {
			s.notifyUIForSession(evt.SessionID, NewMessageMsg{Msg: Message{Kind: MsgKindReasoning, Content: p.Text, AgentID: p.AgentID, RunID: evt.RunID, Timestamp: evt.CreatedAt}})
		}
	case event.RunEventReasoningDone:
		var p event.ReasoningDonePayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			s.notifyUIForSession(evt.SessionID, ReasoningDoneMsg{AgentID: p.AgentID, Timestamp: evt.CreatedAt})
		}
	case event.RunEventUsageDelta:
		var p event.UsageDeltaPayload
		if json.Unmarshal(evt.Payload, &p) == nil && (p.InputTokens != 0 || p.OutputTokens != 0) {
			s.notifyUIForSession(evt.SessionID, TokenUsageDeltaMsg{RunID: evt.RunID, AgentID: p.AgentID, InputTokens: p.InputTokens, OutputTokens: p.OutputTokens})
		}
	case event.RunEventToolStarted:
		var p event.ToolCallStartedPayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			s.notifyUIForSession(evt.SessionID, subagentToolStepMsg(evt, p.StepID, p.ToolName, firstNonEmpty(p.Summary, p.Description), p.ToolMeta, evt.Type))
		}
	case event.RunEventToolCompleted:
		var p event.ToolCallCompletedPayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			msg := subagentToolStepMsg(evt, p.StepID, p.ToolName, firstNonEmpty(p.Summary, p.Description), p.ToolMeta, evt.Type)
			msg.Msg.Content = p.DisplayBody
			msg.Msg.Duration = time.Duration(p.DurationSeconds * float64(time.Second))
			msg.Msg.RetainAsHistory = p.RetainAsHistory
			s.notifyUIForSession(evt.SessionID, msg)
		}
	case event.RunEventToolOutputDelta:
		var p event.ToolCallOutputDeltaPayload
		if json.Unmarshal(evt.Payload, &p) == nil && p.Text != "" {
			msg := subagentToolStepMsg(evt, p.StepID, p.ToolName, p.Summary, p.ToolMeta, evt.Type)
			msg.Msg.Content = p.Text
			msg.Msg.ToolOutputDelta = true
			s.notifyUIForSession(evt.SessionID, msg)
		}
	case event.RunEventPlanUpdated:
		var p event.PlanUpdatedPayload
		if json.Unmarshal(evt.Payload, &p) == nil && (len(p.Items) > 0 || strings.TrimSpace(p.Title) != "" || p.Total > 0) {
			s.notifyUIForSession(evt.SessionID, PlanUpdatedMsg{Payload: p})
		}
	case event.RunEventSubagentSpawned:
		var p event.SubagentSpawnedPayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			s.notifyUIForSession(evt.SessionID, SubagentSpawnedMsg{AgentID: p.AgentID, AgentType: p.AgentType, TaskID: p.TaskID, Title: p.Title, Task: p.Task, ParentToolCallID: p.ParentToolCallID, TaskIndex: p.TaskIndex, ExecutionID: p.ExecutionID, ModelProvider: p.ModelProvider, Model: p.Model, ReasoningEffort: p.ReasoningEffort, Timestamp: evt.CreatedAt})
		}
	case event.RunEventSubagentEnded:
		var p event.SubagentEndedPayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			s.notifyUIForSession(evt.SessionID, SubagentEndedMsg{AgentID: p.AgentID, AgentType: p.AgentType, TaskID: p.TaskID, Status: p.Status, Error: p.Error, Output: p.Output, ParentToolCallID: p.ParentToolCallID, TaskIndex: p.TaskIndex, ExecutionID: p.ExecutionID, FinishedAt: time.UnixMilli(p.FinishedAtMs), Timestamp: evt.CreatedAt})
		}
	case event.RunEventSubagentInputDelivered:
		// A message the user sent a running subagent was handed to its model at
		// a tool boundary: it is drawn as a user message in that subagent's own
		// view, exactly as a delivered steer is drawn in the conversation.
		var p event.SubagentInputDeliveredPayload
		if json.Unmarshal(evt.Payload, &p) == nil && strings.TrimSpace(p.AgentID) != "" && strings.TrimSpace(p.Text) != "" {
			s.notifyUIForSession(evt.SessionID, SubagentInputDeliveredMsg{AgentID: strings.TrimSpace(p.AgentID), RunID: evt.RunID, Text: strings.TrimSpace(p.Text)})
		}
	case event.CompactEventBudgetUpdated:
		// A per-agent context budget — the gauge that opens a subagent's view
		// and follows each of its responses. It carries the roster key, so the
		// reducer routes it to that subagent's own footer, never the
		// conversation's.
		var p event.TokenBudgetUpdatedPayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			s.notifyUIForSession(evt.SessionID, tokenBudgetMsgFromPayload(p))
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
			s.notifyUIForSession(evt.SessionID, replayApprovalMessage(evt, p.AgentID, p.ActionID, p.ActionKind, "waiting approval", p.Message))
		}
	case event.RunEventApprovalResolved:
		var p event.ApprovalResolvedPayload
		if json.Unmarshal(evt.Payload, &p) == nil && strings.TrimSpace(p.AgentID) != "" {
			s.notifyUIForSession(evt.SessionID, replayApprovalMessage(evt, p.AgentID, p.ActionID, p.ActionKind, firstNonEmpty(p.Decision, "resolved"), p.Reason))
		}
	case event.RunEventGoalStarted, event.RunEventGoalRoundStarted, event.RunEventGoalCompleted:
		if msg, ok := goalEventMessage(evt); ok {
			s.notifyUIForSession(evt.SessionID, msg)
		}
	// A plan review's two events are its card: the request opens it, the
	// outcome closes it, and the reviewer's own spawned event binds to it
	// through the ReviewID the way any subagent binds to its dispatching call.
	case event.RunEventPlanReviewStarted:
		var p event.PlanReviewStartedPayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			s.notifyUIForSession(evt.SessionID, PlanReviewStartedMsg{ReviewID: p.ReviewID, Provider: p.Provider, Model: p.Model, Label: p.Label})
		}
	case event.RunEventPlanReviewed:
		var p event.PlanReviewedPayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			s.notifyUIForSession(evt.SessionID, PlanReviewReviewedMsg{ReviewID: p.ReviewID, Provider: p.Provider, Model: p.Model, Label: p.Label, Outcome: p.Outcome, Error: p.Error})
		}
	case event.RunEventContextCompacting:
		var p event.ContextCompactingPayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			s.notifyUIForSession(evt.SessionID, ContextCompactingMsg{CompactionID: p.CompactionID, AgentID: p.AgentID, Trigger: p.Trigger, TokensBefore: p.TokensBefore})
		}
	case event.RunEventContextCompactProgress:
		var p event.ContextCompactProgressPayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			s.notifyUIForSession(evt.SessionID, ContextCompactProgressMsg{CompactionID: p.CompactionID, AgentID: p.AgentID, Percent: p.Percent, Phase: p.Phase})
		}
	case event.RunEventContextCompactError:
		var p event.ContextCompactFailedPayload
		if json.Unmarshal(evt.Payload, &p) == nil {
			s.notifyUIForSession(evt.SessionID, ContextCompactFailedMsg{CompactionID: p.CompactionID, AgentID: p.AgentID, Error: p.Error, Cancelled: p.Cancelled})
		}
	case event.RunEventContextCompacted:
		var p event.ContextCompactedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return nil
		}
		s.notifyUIForSession(evt.SessionID, ContextCompactedMsg{Payload: p})
		// The footer reads the last response's prompt, which described the
		// history that was just replaced. Until the next response reports
		// again, the checkpoint's own size is what the context holds. A
		// subagent's compaction leaves the conversation's context as it was;
		// its own gauge is recomputed the same instant, from its worker
		// session, and stays in its view.
		if agentID := strings.TrimSpace(p.AgentID); agentID == "" {
			if budget, ok := s.tokenBudgetMessageFromUsage(evt.SessionID, p.TokensAfter); ok {
				s.notifyUIForSession(evt.SessionID, budget)
			}
		} else if payload, ok := run.SubagentContextBudget(context.Background(), s.runner(), evt.SessionID, agentID); ok {
			s.notifyUIForSession(evt.SessionID, tokenBudgetMsgFromPayload(payload))
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
			s.notifyUIForSession(evt.SessionID, msg)
		}
	// A language-server recommendation is the user's to answer, not the
	// model's: the event is persisted above (so a reconnect does not ask
	// twice) and only the live surface opens the modal. Replay reads the
	// answer's transcript line, never this.
	case event.RunEventLSPRecommendation:
		var p event.LSPRecommendation
		if json.Unmarshal(evt.Payload, &p) == nil && p.ID != "" {
			s.notifyUIForSession(evt.SessionID, LSPRecommendationMsg{Rec: p})
		}
	}
	return nil
}

// markEventShownOnce claims the right to paint one event carrying a derived
// (stable) identity in one conversation: the first delivery returns true,
// every later delivery of the same (session, event id) returns false.
//
// The store is and stays the arbiter once a session row exists — its
// UNIQUE(session_id,event_id) is what keeps a resubscribing surface quiet, not
// this memory. What the store cannot arbitrate is the window where the
// conversation has not started yet (state.ErrSessionNotStarted): the event is
// shown and stored nowhere, so a subscription that replays its snapshot — a
// registry transition, the generation settling, every later Load — would
// redeliver the same fact with no one to say it was already painted. This
// memory is that someone, and only that: it is per process and per surface,
// keyed per conversation, and deliberately unpersisted. A restart re-observes
// what still holds, which is the same rule the store's own absence follows.
//
// Events with no derived identity (empty session or event id) always pass:
// they are one-shot occurrences with nothing to dedupe on.
func (s *ChatSession) markEventShownOnce(sessionID, eventID string) bool {
	if s == nil {
		return true
	}
	sessionID = strings.TrimSpace(sessionID)
	eventID = strings.TrimSpace(eventID)
	if sessionID == "" || eventID == "" {
		return true
	}
	key := sessionID + "\x00" + eventID
	s.shownEventsMu.Lock()
	defer s.shownEventsMu.Unlock()
	if s.shownEvents == nil {
		s.shownEvents = make(map[string]struct{})
	}
	if _, seen := s.shownEvents[key]; seen {
		return false
	}
	s.shownEvents[key] = struct{}{}
	return true
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
	s.notifyUIForSession(evt.SessionID, NewMessageMsg{Msg: Message{
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

// persistRunTurnError keeps a failed run's error in the conversation's log.
// The error block was the last thing the run said before its "Worked for"
// line; this record is what lets a resume draw it there again.
func (s *ChatSession) persistRunTurnError(sessionID, runID, text string) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(text) == "" {
		return
	}
	s.persistRunEvent(context.Background(), event.NewRunEvent("", runID, sessionID, event.RunEventTurnError,
		event.TurnErrorPayload{Error: text, Message: text}, time.Now()))
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
			// A subagent's own subagent_* call reaches the surface through
			// this mirror; without its card facts the nested dispatch would
			// fall back to a generic tool card instead of its own card.
			SubagentCall: meta.SubagentCall,
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
	// This process reaps the runs whose owners stopped renewing — a gateway
	// that crashed, another terminal that was killed mid-turn — and reports
	// each ending through this session's own run-event funnel, so replay and
	// every page see it the way they see any other ending. The reap itself is
	// a compare-and-swap, so whichever process gets there first settles the
	// run and reports it exactly once.
	s.stopAbandonedRunReaper = turn.AbandonedRunReaper{
		Runs:    env.Deps.RunRT,
		Publish: s.publishTUIRunEvent,
		Recover: func(context.Context) { s.recoverResolvedApprovalWaitsOnce(s.preferredSessionIDForFast()) },
	}.Start(ctx)
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
	// Every subagent execution reports its start and end here, so the engine
	// arms that subagent's own auto-continue the same way it arms the
	// conversation's. The surface is declared at registration because the
	// execution's context carries no reliable surface of its own.
	env.OnSubagentExecution = &process.SubagentExecutionHooks{
		Starting: func(workerSessionID string) {
			s.Core.SubagentExecutionStarting(context.Background(), workerSessionID)
		},
		Ended: func(end run.SubagentExecutionEnd) {
			s.Core.SubagentExecutionEnded(context.Background(), turn.SubagentExecutionEnd{
				ConversationSessionID: end.ConversationSessionID,
				WorkerSessionID:       end.WorkerSessionID,
				AgentKey:              end.AgentKey,
				RunID:                 end.RunID,
				Origin:                turn.Origin{Surface: turn.SurfaceTUI},
				Err:                   end.Err,
			})
		},
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
	SurfaceComposerTokenStats(sessionID string, usage int) ComposerTokenStats
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
	// SessionTitle returns the session's own title, "" when it has none. It
	// reads by id, so a conversation older than any recent list still has
	// its name.
	SessionTitle(ctx context.Context, id string) (string, error)
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
	// HandleLSPSlash answers /lsp for the text fallback (non-viewport mode).
	HandleLSPSlash(sessionID, channel string) (string, bool)
	// HandleSandboxSlash returns the sandbox report. It reports only — the
	// sandbox is chosen with /permissions, which moves it together with the
	// approval policy.
	HandleSandboxSlash(sessionID, channel string, args []string) (string, bool)
	ExecuteSurfaceSlash(ctx context.Context, sessionID string, line string) (SlashOutcome, bool)
	// ChooseSurfaceSlash applies a choice made in the picker a slash command
	// offered.
	ChooseSurfaceSlash(ctx context.Context, sessionID string, choice turn.SlashChoice) SlashOutcome
	PrependUINotify(fn func(any))
	// SetViewingSession records the conversation this surface is attached
	// to. The event funnel consults it to keep other sessions' events off
	// this screen (they still land in their own session's log).
	SetViewingSession(sessionID string)
	// StartApprovalRecovery drains the approval decisions of one conversation
	// that were committed while no process was driving it. The surface calls it
	// with the session it has opened, so an interrupted approval is reported to
	// the conversation it belongs to and nowhere else.
	StartApprovalRecovery(sessionID string)
	SetToolApprovalSink(sink turn.ToolApprovalDecisionSink)
	CancelActiveRun() bool
	// SurfaceInputQueue returns the conversation's input queue — the one
	// place queue semantics are decided. The surface enqueues, recalls,
	// previews and drains through it; the queue belongs to the conversation,
	// so it outlives any one turn.
	SurfaceInputQueue(sessionID string) *run.InputQueue
	// SendToSubagent hands what the user typed in a subagent's own view to that
	// subagent: it starts its next execution when the subagent is idle, and
	// reaches it at its next tool boundary (or waits for the execution after
	// it) when it is running. followUp forces the queued lane while a run is in
	// flight.
	SendToSubagent(sessionID, agentKey string, submission ComposerSubmission, followUp bool) (run.SubagentDelivery, error)
	// SubagentInputPreview is the subagent's queued input as its own view shows it.
	SubagentInputPreview(sessionID, agentKey string) ComposerPendingInputPreview
	// RecallSubagentInput pulls the subagent's newest queued message back out
	// for editing.
	RecallSubagentInput(sessionID, agentKey string) (ComposerSubmission, bool)
	// InterruptSubagentToSend stops the subagent's running execution to send the
	// steers queued behind it (Esc's second meaning in its view); false when
	// there is nothing to flush.
	InterruptSubagentToSend(sessionID, agentKey string) bool
	// WithdrawSubagentInput takes a just-sent message back before the subagent
	// answered, returning it plus everything queued after it.
	WithdrawSubagentInput(sessionID, agentKey string) ([]ComposerSubmission, bool)
	// DiscardSubagentInput empties the subagent's queued input and returns how
	// many messages were dropped, the way the conversation's own queue is
	// discarded when its conversation is left.
	DiscardSubagentInput(sessionID, agentKey string) int
	// CompactSubagent compacts the subagent's own worker context from its view;
	// it refuses while the subagent is running.
	CompactSubagent(ctx context.Context, sessionID, agentKey string) (string, bool)
	// SubagentContextReport answers /context for the subagent whose view it is
	// run from.
	SubagentContextReport(sessionID, agentKey string) (string, bool)
	// SubagentComposerTokenStats is the subagent's own footer gauge — how much
	// of that subagent's context window is left.
	SubagentComposerTokenStats(sessionID, agentKey string) ComposerTokenStats
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
	// SubagentView is true while this composer belongs to a subagent's own
	// view. It is kept in step with the active view by syncComposerToView and
	// makes the slash picker list only the commands available there (D4).
	SubagentView bool
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
			c.SlashOverlay.SubagentView = c.SubagentView
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

// LSPStatusTickMsg tells the main loop a language server's snapshot changed
// while the /lsp panel is open, so the panel repaints in place.
type LSPStatusTickMsg struct{}

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

// AutoContinueScheduledMsg reports a continuation armed by the engine: the view
// it belongs to will resume by itself at ContinueAt. AgentID is the roster key
// of the subagent whose continuation it is, empty for the conversation's own.
type AutoContinueScheduledMsg struct {
	SessionID  string
	RunID      string
	AgentID    string
	ContinueAt time.Time
	Code       string
	Attempt    int
}

// AutoContinueCancelledMsg reports a continuation that ended without firing,
// for the view named by AgentID ("" for the conversation's own).
type AutoContinueCancelledMsg struct {
	SessionID string
	AgentID   string
	Reason    string
	Error     string
}

// AutoContinueDueMsg carries the engine's continuation to the event loop. The
// conversation's own is submitted as its next turn; a subagent's (AgentKey set)
// is delivered to that subagent instead.
type AutoContinueDueMsg struct {
	SessionID string
	AgentKey  string
	Prompt    string
}

// autoContinueView is what the event loop knows about one pending continuation.
type autoContinueView struct {
	sessionID string
	// agentKey is the view this continuation belongs to, "" for the
	// conversation's own.
	agentKey   string
	continueAt time.Time
	code       string
	// announced is whether the transcript line has been drawn. A limit that
	// ends a turn is announced after that turn's own error, so the line reads
	// as the answer to it rather than appearing above it.
	announced bool
}

// autoContinueCanceller is the optional half of Session that can stop a
// pending continuation of the conversation itself. It is optional so surfaces
// without an engine behind them (tests, replay) need not grow a method they
// would stub.
type autoContinueCanceller interface {
	CancelAutoContinue(sessionID string) bool
}

// subagentAutoContinueCanceller is the optional half of Session that can stop
// one subagent's pending continuation, named by its roster key.
type subagentAutoContinueCanceller interface {
	CancelSubagentAutoContinue(sessionID, agentKey string) bool
}

// subagentAutoContinuer is the optional half of Session that runs one
// subagent's due continuation: it delivers the prompt to that subagent rather
// than starting a conversation turn.
type subagentAutoContinuer interface {
	ContinueSubagentAuto(plan turn.AutoContinuePlan, prompt string) error
}

// CancelAutoContinue stops the session's pending continuation on the reader's
// behalf, and reports whether there was one.
func (s *ChatSession) CancelAutoContinue(sessionID string) bool {
	if s == nil || s.Core == nil {
		return false
	}
	return s.Core.CancelAutoContinue(context.Background(), sessionID, turn.AutoContinueCancelledByUser)
}

// CancelSubagentAutoContinue stops one subagent's pending continuation, named
// by its roster key, on the reader's behalf.
func (s *ChatSession) CancelSubagentAutoContinue(sessionID, agentKey string) bool {
	if s == nil || s.Core == nil {
		return false
	}
	return s.Core.CancelAutoContinueForAgent(context.Background(), sessionID, agentKey, turn.AutoContinueCancelledByUser)
}

// ContinueSubagentAuto delivers a subagent's due continuation to that subagent
// through the shared process helper, so the terminal and the web write the same
// delivery.
func (s *ChatSession) ContinueSubagentAuto(plan turn.AutoContinuePlan, prompt string) error {
	if s == nil || s.Env == nil {
		return turn.ErrAutoContinueUnavailable
	}
	return s.Env.ContinueSubagent(s.runner(), s.subagentSurface(plan.SessionID), plan, prompt)
}

// continueAfterUsageLimit is the engine's port into this surface. The turn is
// not run here: it is handed to the event loop, which owns the composer, the
// transcript and cancellation, and runs it the way it runs anything the reader
// submits. A subagent's continuation is delivered to that subagent's view the
// same way.
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
	s.notifyUI(AutoContinueDueMsg{SessionID: plan.SessionID, AgentKey: strings.TrimSpace(plan.AgentKey), Prompt: prompt})
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
		return AutoContinueScheduledMsg{SessionID: evt.SessionID, RunID: evt.RunID, AgentID: strings.TrimSpace(p.AgentID), ContinueAt: at, Code: p.Code, Attempt: p.Attempt}, true
	case event.RunEventAutoContinueCancelled:
		var p event.AutoContinueCancelledPayload
		_ = json.Unmarshal(evt.Payload, &p)
		return AutoContinueCancelledMsg{SessionID: evt.SessionID, AgentID: strings.TrimSpace(p.AgentID), Reason: p.Reason, Error: p.Error}, true
	}
	return nil, false
}

// handleAutoContinueNotification applies one auto-continue message to the
// event loop's state, reporting false for any other message.
func handleAutoContinueNotification(renderer *Renderer, state *streamState, m any) bool {
	switch msg := m.(type) {
	case AutoContinueScheduledMsg:
		view := strings.TrimSpace(msg.AgentID)
		if state == nil || !sameSessionID(msg.SessionID, state.sessionID) {
			return true
		}
		state.setAutoContinue(view, &autoContinueView{sessionID: msg.SessionID, agentKey: view, continueAt: msg.ContinueAt, code: msg.Code})
		renderer.SetAutoContinueNotice(view, autoContinueFooterText(msg.Code, msg.ContinueAt, time.Now()))
		if state.activeForeground == nil {
			state.announceAutoContinue(renderer)
		}
		renderComposerWithState(renderer, state)
		return true
	case AutoContinueCancelledMsg:
		if state == nil {
			return true
		}
		view := strings.TrimSpace(msg.AgentID)
		entry := state.autoContinueFor(view)
		if entry == nil || !sameSessionID(msg.SessionID, entry.sessionID) {
			return true
		}
		state.clearAutoContinue(renderer, view)
		// A turn the reader sent is its own explanation; anything else ending
		// the wait is said, so the notice already in scrollback is not left
		// promising a continuation that will never come.
		switch {
		case msg.Reason == turn.AutoContinueSuperseded:
		case msg.Reason == turn.AutoContinueUnavailable && entry.announced:
			renderer.RenderFrame(Frame{Kind: FrameStatus, Title: autoContinueUnavailableText(msg.Error), AgentID: view, Final: true})
		case entry.announced:
			renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "Auto-continue cancelled", AgentID: view, Final: true})
		}
		renderComposerWithState(renderer, state)
		return true
	case AutoContinueDueMsg:
		if state == nil {
			return true
		}
		view := strings.TrimSpace(msg.AgentKey)
		entry := state.autoContinueFor(view)
		// Only a wait this loop is still showing is continued. A reader who
		// pressed Esc as the timer fired has cleared it, and the continuation
		// that raced the key is dropped rather than run against their wish.
		if entry == nil || !sameSessionID(msg.SessionID, entry.sessionID) {
			return true
		}
		state.clearAutoContinue(renderer, view)
		prompt := strings.TrimSpace(msg.Prompt)
		if prompt == "" {
			return true
		}
		if view == "" {
			state.autoContinueDue = &ComposerSubmission{
				Text:        prompt,
				Parts:       []llm.ContentPart{llm.Text(prompt)},
				DisplayText: prompt,
			}
			renderComposerWithState(renderer, state)
			return true
		}
		// A subagent's continuation is delivered to that subagent, not
		// submitted as a conversation turn.
		if cont, ok := state.session.(subagentAutoContinuer); ok {
			plan := turn.AutoContinuePlan{SessionID: msg.SessionID, AgentKey: view}
			if err := cont.ContinueSubagentAuto(plan, prompt); err != nil {
				renderer.RenderFrame(Frame{Kind: FrameStatus, Title: autoContinueUnavailableText(err.Error()), AgentID: view, Final: true})
			}
		}
		renderComposerWithState(renderer, state)
		return true
	}
	return false
}

// setAutoContinue records one view's pending continuation.
func (s *streamState) setAutoContinue(view string, entry *autoContinueView) {
	if s == nil {
		return
	}
	if s.autoContinue == nil {
		s.autoContinue = map[string]*autoContinueView{}
	}
	s.autoContinue[strings.TrimSpace(view)] = entry
}

// autoContinueFor returns one view's pending continuation, nil when none.
func (s *streamState) autoContinueFor(view string) *autoContinueView {
	if s == nil {
		return nil
	}
	return s.autoContinue[strings.TrimSpace(view)]
}

// announceAutoContinue draws the transcript line for each pending continuation
// that has none, into the view it belongs to, once.
func (s *streamState) announceAutoContinue(renderer *Renderer) {
	if s == nil {
		return
	}
	for view, entry := range s.autoContinue {
		if entry == nil || entry.announced {
			continue
		}
		entry.announced = true
		renderer.RenderFrame(Frame{
			Kind:    FrameStatus,
			Title:   autoContinueTranscriptText(entry.code, entry.continueAt, time.Now()),
			AgentID: strings.TrimSpace(view),
			Final:   true,
		})
	}
}

// cancelAutoContinue is Esc or typing while the view on screen waits. It
// reports whether there was one, so Esc is consumed only when it did something.
func (s *streamState) cancelAutoContinue(renderer *Renderer) bool {
	if s == nil {
		return false
	}
	view := ""
	if renderer != nil {
		view = strings.TrimSpace(renderer.ActiveView())
	}
	entry := s.autoContinueFor(view)
	if entry == nil {
		return false
	}
	if view == "" {
		if canceller, ok := s.session.(autoContinueCanceller); ok && canceller.CancelAutoContinue(entry.sessionID) {
			// The engine's cancellation event clears the notice and says so in
			// the transcript, the same way a cancel from any other surface does.
			return true
		}
	} else if canceller, ok := s.session.(subagentAutoContinueCanceller); ok && canceller.CancelSubagentAutoContinue(entry.sessionID, view) {
		return true
	}
	// Nothing left to cancel in the engine: the timer fired as the key was
	// pressed. Clearing here is what makes the racing continuation drop.
	announced := entry.announced
	s.clearAutoContinue(renderer, view)
	if announced {
		renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "Auto-continue cancelled", AgentID: view, Final: true})
	}
	return true
}

// leaveAutoContinue drops the pending or due continuations of the conversation
// being switched away from, each in its own view.
func (s *streamState) leaveAutoContinue(renderer *Renderer) {
	if s == nil {
		return
	}
	s.autoContinueDue = nil
	for view, entry := range s.autoContinue {
		if entry == nil {
			continue
		}
		key := strings.TrimSpace(view)
		if key == "" {
			if canceller, ok := s.session.(autoContinueCanceller); ok {
				canceller.CancelAutoContinue(entry.sessionID)
			}
		} else if canceller, ok := s.session.(subagentAutoContinueCanceller); ok {
			canceller.CancelSubagentAutoContinue(entry.sessionID, key)
		}
		if renderer != nil {
			renderer.SetAutoContinueNotice(key, "")
		}
	}
	s.autoContinue = nil
}

// clearAutoContinue drops one view's pending continuation and its notice.
func (s *streamState) clearAutoContinue(renderer *Renderer, view string) {
	if s == nil {
		return
	}
	view = strings.TrimSpace(view)
	delete(s.autoContinue, view)
	if renderer != nil {
		renderer.SetAutoContinueNotice(view, "")
	}
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
