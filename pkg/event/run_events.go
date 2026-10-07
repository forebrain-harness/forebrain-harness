package event

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	RunEventTurnStarted            = "turn_started"
	RunEventTurnCompleted          = "turn_completed"
	RunEventTurnCancelled          = "turn_cancelled"
	RunEventTurnError              = "turn_error"
	RunEventTurnFailed             = "turn_failed"
	RunEventAssistantDelta         = "assistant_delta"
	RunEventReasoningDelta         = "reasoning_delta"
	RunEventReasoningDone          = "reasoning_done"
	RunEventUsageDelta             = "usage_delta"
	RunEventToolStarted            = "tool_call_started"
	RunEventToolOutputDelta        = "tool_output_delta"
	RunEventToolCompleted          = "tool_call_completed"
	RunEventTurnDiffUpdated        = DiffEventTurnUpdated
	RunEventApprovalReq            = "approval_requested"
	RunEventApprovalResolved       = ApprovalEventResolved
	RunEventContextCompacting      = "context_compacting"
	RunEventContextCompactProgress = "context_compact_progress"
	RunEventContextCompacted       = CompactEventContextCompacted
	RunEventContextCompactError    = "context_compact_failed"
	RunEventConfigReloaded         = "config_reloaded"
	RunEventModeChanged            = "mode_changed"
	RunEventPlanUpdated            = "plan_updated"
	RunEventForkAgentCompleted     = "fork_agent_completed"
	RunEventGoalStarted            = "goal_started"
	RunEventGoalRoundStarted       = "goal_round_started"
	RunEventGoalCompleted          = "goal_completed"
	RunEventPendingInputUpdated    = "pending_input_updated"
	RunEventQueuedInputReleased    = "queued_input_released"
	RunEventSessionSwitched        = "session_switched"
	RunEventSubagentSpawned        = "subagent_spawned"
	RunEventSubagentEnded          = "subagent_ended"
	// RunEventSubagentInputDelivered reports a message the user sent a running
	// subagent, delivered to the model at a tool boundary. The surface draws it
	// as a user message in that subagent's own view; it persists, so a replay
	// draws it too.
	RunEventSubagentInputDelivered = "subagent_input_delivered"
	// RunEventPlanReviewStarted is a second opinion being asked for an
	// exit-plan approval. ReviewID is the dispatch id of the review run: the
	// reviewer's subagent_spawned names it as its ParentToolCallID, the way a
	// subagent names the tool call that dispatched it.
	RunEventPlanReviewStarted = "plan_review_started"
	// RunEventPlanReviewed ends one: Outcome is "done", "stopped",
	// "timed_out" or "failed", Error the raw error of a failed review.
	RunEventPlanReviewed = "plan_reviewed"
	// The auto-continue lifecycle: a turn stopped by a spent usage allowance is
	// resumed by the runtime itself once the allowance returns. Scheduled when
	// the wait starts, then exactly one of started (the wait ran out and the
	// continuation turn is being submitted) or cancelled (someone stopped it, a
	// new turn took its place, or the surface could not start one).
	RunEventAutoContinueScheduled = "auto_continue_scheduled"
	RunEventAutoContinueStarted   = "auto_continue_started"
	RunEventAutoContinueCancelled = "auto_continue_cancelled"
	// RunEventHeartbeatFired announces a heartbeat starting a turn in its
	// own conversation, so a page watching the run can draw its prompt.
	RunEventHeartbeatFired = "heartbeat_fired"
)

type RunEvent struct {
	ID string `json:"id"`
	// Sequence is the durable, conversation-wide cursor. Run-local step
	// sequence numbers cannot order concurrent children or close the gap
	// between a history snapshot and a live subscription.
	Sequence int64 `json:"sequence,omitempty"`
	// SchemaVersion lets old clients reject or resync an event shape they do
	// not understand instead of silently projecting incomplete history.
	SchemaVersion int             `json:"schema_version,omitempty"`
	RunID         string          `json:"run_id,omitempty"`
	SessionID     string          `json:"session_id,omitempty"`
	Type          string          `json:"type"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
}

const RunEventSchemaVersion = 1

func NewRunEvent(id, runID, sessionID, eventType string, payload any, createdAt time.Time) RunEvent {
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	return RunEvent{
		ID:            id,
		SchemaVersion: RunEventSchemaVersion,
		RunID:         runID,
		SessionID:     sessionID,
		Type:          eventType,
		Payload:       EncodePayload(SanitizeForPublic(payload)),
		CreatedAt:     createdAt.UTC(),
	}
}

func EncodePayload(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	switch raw := v.(type) {
	case json.RawMessage:
		if raw == nil {
			return nil
		}
		return SanitizeRawJSON(raw)
	case []byte:
		if raw == nil {
			return nil
		}
		return SanitizeRawJSON(json.RawMessage(raw))
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		return SanitizeRawJSON(json.RawMessage(b))
	}
}

type TurnStartedPayload struct{}

// RunPlanFacts is a run's final checklist state, carried by every way a run
// ends — completed, cancelled or failed — because every run closes with its
// worked line and the terminal's names the checklist whichever way it ended.
// Empty when the run had no checklist.
type RunPlanFacts struct {
	PlanDone   int    `json:"plan_done,omitempty"`
	PlanTotal  int    `json:"plan_total,omitempty"`
	PlanActive string `json:"plan_active,omitempty"`
}

type TurnCompletedPayload struct {
	Text      string `json:"text,omitempty"`
	ElapsedMS int64  `json:"elapsed_ms,omitempty"`
	RunPlanFacts
}

type TurnCancelledPayload struct {
	Message string `json:"message,omitempty"`
	RunPlanFacts
}

type TurnErrorPayload struct {
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
	RunPlanFacts
	// Title is the heading a surface shows above the error. A turn error that
	// belongs to a named subsystem — an MCP server that did not start, say —
	// says so here rather than in prose, so the surface can render the heading
	// it uses for that subsystem and the reader can tell where the failure came
	// from before reading a word of it.
	Title string `json:"title,omitempty"`
	// Source names the subsystem the failure belongs to, "" for the turn itself
	// and "mcp" for the MCP startup below. It is machine-readable on purpose:
	// a surface picks its heading and its routing from this, not by parsing the
	// error text, which is the server's own sentence and may be in any language.
	Source string `json:"source,omitempty"`
	// Server and Generation identify the MCP server a startup failure belongs
	// to. Together they are also the failure's identity: a server fails once per
	// generation, and the pair is what makes the durable record idempotent.
	Server     string `json:"server,omitempty"`
	Generation string `json:"generation,omitempty"`
	// Detail is the machine-readable half of the failure. Error carries the
	// runtime's English sentence; Detail lets a surface with its own locale
	// phrase the same failure itself, and re-phrase it when the viewer switches
	// language. Nil when the failure was not a provider refusal.
	Detail *TurnErrorDetail `json:"detail,omitempty"`
}

// MCPStartupErrorSource is TurnErrorPayload.Source for an MCP startup failure.
const MCPStartupErrorSource = "mcp"

// TurnErrorDetail is a classified provider failure on the wire: a stable code
// plus the facts a surface needs to write its own sentence. It mirrors
// llm.ErrorExplanation, converted at the gateway so the event vocabulary does
// not depend on the provider layer.
type TurnErrorDetail struct {
	// Code names the failure, e.g. "rate_limit_quota" or "credentials".
	Code string `json:"code"`
	// Status is the HTTP status the provider answered with, 0 when none.
	Status int `json:"status,omitempty"`
	// Plan names the subscription tier a spent allowance belongs to.
	Plan string `json:"plan,omitempty"`
	// ProviderMessage is the provider's own sentence, unwrapped and capped.
	ProviderMessage string `json:"provider_message,omitempty"`
	// ResetAt is when a spent allowance returns, RFC 3339, empty when unknown.
	// It is absolute so a surface renders it in the viewer's own time zone and
	// recomputes the remaining wait at the moment it draws.
	ResetAt string `json:"reset_at,omitempty"`
	// RetryAfterSeconds is how long that wait was when the response arrived.
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`
}

// AutoContinueScheduledPayload says when a session stopped by a usage limit will
// continue by itself. The times are absolute RFC 3339 stamps so every surface
// renders them on its viewer's own clock, and a surface that draws the notice
// long after the event recomputes the remaining wait instead of repeating it.
type AutoContinueScheduledPayload struct {
	// ContinueAt is when the continuation turn is submitted.
	ContinueAt string `json:"continue_at"`
	// ResetAt is when the provider said the allowance returns; ContinueAt is a
	// moment after it, so a clock slightly ahead of the provider's does not
	// spend the continuation on a request that is refused again.
	ResetAt string `json:"reset_at,omitempty"`
	// Code is the classified failure that stopped the turn: "rate_limit_quota"
	// for a spent plan allowance, "rate_limit_throttle" for a request-rate
	// limit that named its own wait.
	Code string `json:"code,omitempty"`
	// Plan names the subscription tier the allowance belongs to, when known.
	Plan string `json:"plan,omitempty"`
	// Attempt counts the continuations in a row this one would be, from 1.
	Attempt int `json:"attempt,omitempty"`
	// AgentID is the roster key of the subagent this continuation belongs to,
	// empty for the conversation's own. It is what routes the notice to that
	// subagent's view rather than the primary one.
	AgentID string `json:"agent_id,omitempty"`
}

// AutoContinueStartedPayload marks the wait running out: the continuation turn
// is being submitted to the session. Prompt is the user turn it submits, so a
// surface watching a run it did not start can show the message that began it.
type AutoContinueStartedPayload struct {
	Attempt int    `json:"attempt,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
	// AgentID is the roster key of the subagent whose continuation this is,
	// empty for the conversation's own.
	AgentID string `json:"agent_id,omitempty"`
}

// AutoContinueCancelledPayload ends a wait that never fired. Reason is one of
// the turn package's AutoContinue* reasons; Error carries the surface's own
// failure text when it could not start the turn.
type AutoContinueCancelledPayload struct {
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
	// AgentID is the roster key of the subagent whose continuation this is,
	// empty for the conversation's own.
	AgentID string `json:"agent_id,omitempty"`
}

// HeartbeatFiredPayload marks a heartbeat starting a turn in its
// conversation. Prompt is the message it sent, so a page watching a run it
// did not start can draw that message as the turn's first row.
type HeartbeatFiredPayload struct {
	Prompt string `json:"prompt,omitempty"`
}

// TurnFailedPayload distinguishes a terminal failure from a streamed error.
type TurnFailedPayload struct {
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
}

// ConfigReloadedPayload describes a successful live configuration swap.
type ConfigReloadedPayload struct {
	Path string `json:"path,omitempty"`
}

// A compaction reaches every surface as one lifecycle keyed by CompactionID:
// context_compacting when it starts, context_compact_progress while it runs,
// and exactly one of context_compacted or context_compact_failed when it ends.
// AgentID names the subagent whose history was compacted and is empty for the
// conversation's own; it routes the whole lifecycle to that subagent's view.

// ContextCompactingPayload marks a compaction that has started. TokensBefore is
// the size of the history being compacted.
type ContextCompactingPayload struct {
	CompactionID string `json:"compaction_id"`
	Trigger      string `json:"trigger,omitempty"`
	TokensBefore int    `json:"tokens_before,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`
}

// ContextCompactProgressPayload reports how far a running compaction has come.
// Percent only rises and stays below 100: the terminal event completes it.
type ContextCompactProgressPayload struct {
	CompactionID string `json:"compaction_id"`
	Percent      int    `json:"percent"`
	Phase        string `json:"phase,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`
}

// ContextCompactFailedPayload ends a compaction that replaced nothing. A
// cancelled compaction is reported as Cancelled rather than as an error the
// user did not cause.
type ContextCompactFailedPayload struct {
	CompactionID string `json:"compaction_id"`
	Trigger      string `json:"trigger,omitempty"`
	Error        string `json:"error,omitempty"`
	Cancelled    bool   `json:"cancelled,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`
}

// CompactionPhase values name what a running compaction is doing, in the
// order it does them: the model takes in the history, writes the summary, and
// the checkpoint is saved.
const (
	CompactPhaseReading     = "reading"
	CompactPhaseSummarizing = "summarizing"
	CompactPhaseSaving      = "saving"
)

type AssistantDeltaPayload struct {
	Text         string `json:"text,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`
	FirstDeltaMS int64  `json:"first_delta_ms,omitempty"`
}

type ReasoningDeltaPayload struct {
	Text    string `json:"text,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
}

type ReasoningDonePayload struct {
	AgentID string `json:"agent_id,omitempty"`
}

type UsageDeltaPayload struct {
	AgentID      string `json:"agent_id,omitempty"`
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
}

type SubagentSpawnedPayload struct {
	AgentID   string `json:"agent_id,omitempty"`
	AgentType string `json:"agent_type,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	// Title is the short name for the task, Task the full dispatch prompt. A
	// roster row and a card show the title; only the subagent's own view opens
	// with the prompt.
	Title            string `json:"title,omitempty"`
	Task             string `json:"task,omitempty"`
	WorkerSessionID  string `json:"worker_session_id,omitempty"`
	ParentRunID      string `json:"parent_run_id,omitempty"`
	ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
	TaskIndex        int    `json:"task_index"`
	ExecutionID      string `json:"execution_id,omitempty"`
	// The model this execution runs on, resolved once when it starts;
	// surfaces show it rather than re-deriving it from the agent's type.
	ModelProvider   string `json:"model_provider,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// Origin is who started this execution: "user" when a message the user
	// sent the subagent began it, empty when the dispatching agent did.
	Origin string `json:"origin,omitempty"`
}

// SubagentInputDeliveredPayload reports a message the user sent a running
// subagent, handed to the model at a tool boundary. A surface renders it as a
// user message in that subagent's own view.
type SubagentInputDeliveredPayload struct {
	AgentID     string `json:"agent_id,omitempty"`
	ExecutionID string `json:"execution_id,omitempty"`
	Text        string `json:"text,omitempty"`
}

type SubagentEndedPayload struct {
	AgentID          string `json:"agent_id,omitempty"`
	AgentType        string `json:"agent_type,omitempty"`
	TaskID           string `json:"task_id,omitempty"`
	Status           string `json:"status,omitempty"`
	Error            string `json:"error,omitempty"`
	Output           string `json:"output,omitempty"`
	WorkerSessionID  string `json:"worker_session_id,omitempty"`
	ParentRunID      string `json:"parent_run_id,omitempty"`
	ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
	TaskIndex        int    `json:"task_index"`
	ExecutionID      string `json:"execution_id,omitempty"`
	// FinishedAtMs is the moment this execution actually stopped, so a
	// surface stops its task clock there; a reaped execution's is when its
	// process was last seen, not when it was reaped.
	FinishedAtMs int64 `json:"finished_at_ms,omitempty"`
}

// PlanReviewStartedPayload opens one plan review: the user asked a model of
// their choosing for a second opinion on the plan an exit-plan approval is
// holding.
type PlanReviewStartedPayload struct {
	ActionID string `json:"action_id"`
	ReviewID string `json:"review_id"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model"`
	Label    string `json:"label,omitempty"`
}

// PlanReviewedPayload closes the review PlanReviewStartedPayload opened: its
// outcome, and the review itself when there is one.
type PlanReviewedPayload struct {
	ActionID   string `json:"action_id"`
	ReviewID   string `json:"review_id"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model"`
	Label      string `json:"label,omitempty"`
	Text       string `json:"text,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Outcome    string `json:"outcome"`
	Error      string `json:"error,omitempty"`
}

type ModeChangedPayload struct {
	Mode  string `json:"mode,omitempty"`
	Phase string `json:"phase,omitempty"`
}

type PendingInputUpdatedPayload struct {
	// AgentID names the subagent whose channel preview changed; empty means
	// the primary conversation's queue.
	AgentID        string   `json:"agent_id,omitempty"`
	PendingSteers  []string `json:"pending_steers"`
	RejectedSteers []string `json:"rejected_steers"`
	QueuedMessages []string `json:"queued_messages"`
}

// QueuedInputReleasedPayload hands back, as a run ends, every message the
// user sent it that it never took: the client that sent them sends them next
// or takes them back, so they are never dropped with the run. Next lists the
// messages to merge into one and send as the next turn, in the engine's
// order; Inputs lists the messages that go back to the composer instead. The
// engine decides which is which — the client only obeys.
type QueuedInputReleasedPayload struct {
	// AgentID names the subagent whose channel released input; empty means
	// the primary conversation's queue.
	AgentID string          `json:"agent_id,omitempty"`
	Inputs  []ReleasedInput `json:"inputs"`
	Next    []ReleasedInput `json:"next,omitempty"`
}

// ReleasedInput is one released message, whole.
type ReleasedInput struct {
	Text          string   `json:"text"`
	Attachments   []string `json:"attachments,omitempty"`
	MentionImages []string `json:"mention_images,omitempty"`
}

// SessionSwitchedPayload records a surface moving to another session.
type SessionSwitchedPayload struct {
	PreviousID string `json:"previous_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
}

type PlanUpdateItem struct {
	ID      string `json:"id,omitempty"`
	Content string `json:"content,omitempty"`
	Status  string `json:"status,omitempty"`
	Active  string `json:"active,omitempty"`
}

type PlanUpdatedPayload struct {
	Title       string           `json:"title,omitempty"`
	Explanation string           `json:"explanation,omitempty"`
	Completed   int              `json:"completed,omitempty"`
	Total       int              `json:"total,omitempty"`
	Items       []PlanUpdateItem `json:"items,omitempty"`
	// Active is the in-progress item's title, the one a working line names, as
	// PlanProgressOf derives it — carried here so every surface reports the
	// same task without deriving it again.
	Active string `json:"active,omitempty"`
	// AgentID is the roster key of the subagent whose todo list this is, empty
	// for the primary agent. A plan update is the rendering of a session_todo
	// call, so it belongs to the same view that call's card would have, and a
	// surface with per-agent views needs the attribution to place it. It is
	// persisted with the step so a replayed subagent view rebuilds the same
	// card the live run showed.
	AgentID string `json:"agent_id,omitempty"`
}

// PlanEmittingTool reports whether a tool is rendered by the plan card it
// emits rather than by an ordinary tool card. Its raw result is a todo list
// serialized as JSON, which is what the plan card already shows in readable
// form, so showing both puts the same information on screen twice — once
// unreadably.
func PlanEmittingTool(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "session_todo":
		return true
	}
	return false
}

// ToolStepRendersAsPlan reports whether one lifecycle step of such a tool is
// already represented by that plan card, so no tool card belongs to it.
//
// This is the single answer every path has to give: the live terminal hook, the
// live gateway hook, and the projection that rebuilds a run from its persisted
// steps. When each decided separately, the primary agent's conversation showed
// the plan card while a subagent's own view showed the raw JSON result, and a
// replayed transcript showed both.
func ToolStepRendersAsPlan(toolName, kind, errText string) bool {
	if !PlanEmittingTool(toolName) {
		return false
	}
	switch strings.TrimSpace(kind) {
	case RunEventToolStarted:
		return true
	case RunEventToolOutputDelta, RunEventToolCompleted:
		// A failure has no plan card to be represented by, so it keeps its own.
		return strings.TrimSpace(errText) == ""
	}
	return false
}

// GoalStartedPayload opens a /goal: the objective the turn works toward, in
// rounds, until a check of the workspace finds it met.
type GoalStartedPayload struct {
	Objective string `json:"objective"`
	MaxRounds int    `json:"max_rounds"`
	// AfterRowID is the transcript row the line is drawn after: the message
	// that asked for the goal.
	AfterRowID int64 `json:"after_row_id,omitempty"`
}

// GoalRoundStartedPayload opens a continuation round. Why is the check's
// reason for carrying on.
type GoalRoundStartedPayload struct {
	Round int    `json:"round"`
	Why   string `json:"why,omitempty"`
	// CheckAgentID is the roster key of the check that sent the goal on.
	CheckAgentID string `json:"check_agent_id,omitempty"`
	// AfterRowID is the transcript row the line is drawn after: the last one
	// the previous rounds wrote.
	AfterRowID int64 `json:"after_row_id,omitempty"`
}

// The ways a goal ends.
const (
	GoalStatusDone        = "done"        // the check found the objective met
	GoalStatusStuck       = "stuck"       // the check found no progress
	GoalStatusCapped      = "capped"      // the round limit was reached
	GoalStatusInterrupted = "interrupted" // the user stopped the turn
	GoalStatusFailed      = "failed"      // a round or the check failed; Why is the error
)

// GoalCompletedPayload closes a /goal.
type GoalCompletedPayload struct {
	Objective  string `json:"objective,omitempty"`
	Rounds     int    `json:"rounds,omitempty"`
	Status     string `json:"status,omitempty"`
	Why        string `json:"why,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	// CheckAgentID is the roster key of the last check, when one ran.
	CheckAgentID string `json:"check_agent_id,omitempty"`
}
