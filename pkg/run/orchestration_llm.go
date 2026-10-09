// Tool-call orchestration for a turn.
package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

type toolOrchestrationLLM struct {
	inner llm.LLM
	state *tool.State
	home  string
}

type stopAfterToolResultKey struct{}

type orchestratedToolResult struct {
	index   int
	call    llm.ToolCall
	parts   []llm.ContentPart
	timing  llm.ExecutionTiming
	display *llm.ToolDisplayState
	err     error
}

type capturedToolStepData struct {
	completed  *tool.ToolCompletionPayload
	actionID   string
	actionKind string
}

type capturedToolStepKey struct{}

func wrapToolOrchestrationLLM(inner llm.LLM, state *tool.State) llm.LLM {
	return wrapToolOrchestrationLLMWithHome(inner, state, "")
}

func wrapToolOrchestrationLLMWithHome(inner llm.LLM, state *tool.State, home string) llm.LLM {
	if inner == nil || state == nil {
		return inner
	}
	return &toolOrchestrationLLM{inner: inner, state: state, home: strings.TrimSpace(home)}
}

// WrapToolOrchestrationLLM exposes the canonical execution-chain wrapper used
// by Forebrain Harness product runtimes when tool results must be governed before they are
// replayed into model context.
func (w *toolOrchestrationLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if w == nil || w.inner == nil {
		return nil, fmt.Errorf("nil orchestrated llm")
	}
	ctx = w.state.ContextWithRuntimeSessionMode(ctx)
	tools = effectiveToolsForContext(ctx, tools)
	var totalUsage llm.Usage
	toolMap := make(map[string]*llm.Tool, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		toolMap[tool.Name()] = tool
	}
	session := append([]llm.Message(nil), messages...)
	// partialCapture mirrors the orchestration session as it grows so a
	// cancelled run's dispatcher can persist already-completed messages.
	// Only injected by the TUI path; nil for subagents/gateway.
	partialCapture := PartialSessionCaptureFromContext(ctx)
	partialCapture.Set(session)
	// noteResponseCompleted marks the point where a finished assistant response
	// has entered the session and is therefore covered by partialCapture. The
	// surface's streamed-text buffer spans the whole turn, so without this
	// boundary a cancel would persist that buffer on top of the very messages
	// the capture already holds, duplicating the text on resume replay.
	noteResponseCompleted := func() {
		if sink := llm.StreamSinkFrom(ctx); sink != nil && sink.OnResponseCompleted != nil {
			sink.OnResponseCompleted()
		}
	}
	// Install a compaction adoption sink: when an inner mid-turn compaction
	// replaces the model context, the live orchestration session must adopt the
	// compacted history instead of continuing to grow the uncompacted slice.
	// Adopting it is what keeps
	// the persisted transcript (Result.Session) from re-accumulating the
	// pre-compaction history — and its summary — after every checkpoint boundary.
	adoptionSink := &compactionAdoptionSink{}
	ctx = withCompactionAdoptionSink(ctx, adoptionSink)
	// Install a plan-reminder adoption sink for the same reason: a reminder the
	// plan-mode wrapper injects into a request belongs to this session, in the
	// position the model saw it. Persisting it out of band from inside the call
	// wrote it ahead of the tool results the run had not stored yet, splitting an
	// approval gate's assistant tool_calls row from the result answering it.
	reminderSink := &reminderAdoptionSink{}
	ctx = withReminderAdoptionSink(ctx, reminderSink)
	resumeAssistant, ok := w.consumeResumeSnapshot(ctx, messages)
	if ok {
		session = resumeAssistant.session
		if resumeAssistant.denied {
			// User denied the tool call — inject denial results without
			// re-executing. Keep already-completed tool results in context so
			// approval resume does not replay or drop earlier tool calls.
			for _, tc := range resumeAssistant.pendingMsg.ToolCalls {
				if msg, ok := resumeAssistant.completedResults[tc.ID]; ok {
					session = append(session, msg)
					continue
				}
				denial := llm.ToolResultMessage(tc.ID, llm.Text(
					buildDenialMessage(tc.Function.Name, resumeAssistant.denyReason),
				))
				// The refusal has two halves: the instruction text above is
				// what the model reads, and this display body is what the
				// card says — live, and on every replay of the row this
				// result persists to. Without it a replay printed the
				// model-facing text (and, for a denied plan, the reviews it
				// carried) as if it were the user's words.
				denialMeta, _ := json.Marshal(map[string]string{
					"tool_name": strings.TrimSpace(tc.Function.Name),
					"status":    "denied",
				})
				// A delivered review closed this gate: the card's body is the
				// delivery drop key, never "(no output)". The key is replay
				// plumbing, not user-facing words — every surface's replay
				// projection recognizes it and drops the row whole.
				denialBody := tool.DeniedToolDisplayBody(tc.Function.Name, resumeAssistant.denyFeedback)
				if resumeAssistant.deliveredReview {
					denialBody = tool.PlanReviewDeliveredDisplayKey
				}
				denial.ToolDisplay = &llm.ToolDisplayState{
					Body:         denialBody,
					ToolMetaJSON: string(denialMeta),
				}
				session = append(session, denial)
			}
		} else if rerr := w.replayPendingToolCalls(ctx, &session, resumeAssistant.pendingMsg, resumeAssistant.completedResults, toolMap); rerr != nil {
			return nil, rerr
		}
		// The continuation is over: either the denial results are in the
		// session, or every pending call has a result. Releasing the durable
		// fence here is what keeps "the outcome of this tool call is unknown"
		// meaning exactly that — a process that died while the call was
		// running — instead of covering the whole remainder of the turn.
		tool.EndApprovalContinuation(ctx)
		// The approved action ID set by the resume caller authorizes exactly the
		// one tool call replayed above (replayPendingToolCalls scopes it to that
		// single call). Strip it from the loop context so any further
		// approval-gated tool the model emits later in this same turn — e.g.
		// exit_plan_mode after an approved enter_plan_mode — still hits its own
		// approval gate instead of silently inheriting the prior approval.
		ctx = tool.WithApprovedActionID(ctx, "")
		partialCapture.Set(session)
	}
	// inFlightSteers carries the queued steers appended to the session for the
	// next model call, held provisionally until that call begins producing output:
	// sessionBeforeSteers is where they start, so a failed call can unwind them
	// out of the session as well as back into the queue.
	var inFlightSteers *SteerDelivery
	sessionBeforeSteers := 0
	for {
		ctx = w.state.ContextWithRuntimeSessionMode(ctx)
		// Snapshot the session right before the LLM call: this is where a
		// cancellation occurs, and the snapshot must reflect everything
		// completed so far (including drain steers from the
		// prior iteration) so the dispatcher can persist it.
		partialCapture.Set(session)
		callCtx := withSteerDeliveryResponseStart(ctx, inFlightSteers)
		res, err := w.inner.Execute(callCtx, session, tools)
		if err != nil {
			// If the provider produced no model output, this call did not establish
			// delivery. Returning the steers to the queue is what lets the turn
			// boundary resubmit them: the surface treats a delivered steer as
			// answered, so leaving them consumed here shows the user a sent
			// message that no model ever saw and stalls the conversation. Unwind
			// them out of the session too, so the partial transcript this failed
			// turn persists does not carry a user message that is going to be
			// sent again.
			if inFlightSteers != nil {
				rolledBack := inFlightSteers.Rollback()
				inFlightSteers = nil
				if rolledBack {
					session = session[:sessionBeforeSteers]
					partialCapture.Set(session)
				}
			}
			return res, err
		}
		// A successful non-streaming call has no response-start event, so commit
		// here. For streaming calls this is an idempotent no-op: the first event
		// committed before any assistant/reasoning output was forwarded.
		inFlightSteers.Commit()
		inFlightSteers = nil
		// If a mid-turn compaction replaced the model context during this call,
		// adopt the compacted history as the live session before appending the
		// response below. Everything the inner call sent is represented by the
		// compacted messages; only the freshly produced response must still be
		// appended. This bounds the persisted transcript and prevents the
		// pre-compaction history/summary from being written after the boundary.
		if replaced, ok := adoptionSink.take(); ok {
			session = replaced
			// The replacement is the history the inner call actually sent, which
			// already carries any reminder injected below this loop. Adopting it
			// again would duplicate it inside the compacted context.
			reminderSink.discard()
			partialCapture.Set(session)
		}
		// Reminders injected during this call are part of what the model
		// answered, so the live session takes each one before the response is
		// appended after it. A call that produced nothing returned above with
		// the sink still holding them: the exchange never happened, and the
		// next call injects them again. They are adopted in record order —
		// each insertion re-establishes the coordinate space the next recorded
		// index was computed in (see reminderAdoptionSink.take).
		if adopted := reminderSink.take(); len(adopted) > 0 {
			for _, rec := range adopted {
				session = insertPlanReminderMessage(session, rec.message, rec.insertAt)
			}
			partialCapture.Set(session)
		}
		latestUsage := usageCopy(res)
		accumulateUsage(&totalUsage, latestUsage)
		if res == nil || res.Message == nil || len(res.Message.ToolCalls) == 0 {
			if sessionHasToolExecution(session) && isEmptyAssistantResult(res) {
				return nil, fmt.Errorf("empty assistant response after tool execution")
			}
			// A steer enqueued during the model's final (tool-call-free) response
			// would otherwise be lost: the drain only runs in the tool path
			// below, so a turn that ends without a further tool call never
			// delivers it. Inject any pending steer as user input and keep the
			// turn going so the model actually responds to it. As at the tool
			// boundary, the entries are only taken provisionally: a cancelled or
			// failed turn never makes the follow-up model call, so they go back
			// to the queue for the surface to resubmit rather than being
			// reported as delivered.
			if res != nil && res.Message != nil {
				if delivery := TurnInputRuntimeFromContext(ctx).BeginSteerDelivery(ctx); delivery != nil {
					session = append(session, *res.Message)
					noteResponseCompleted()
					sessionBeforeSteers = len(session)
					for _, entry := range delivery.Entries() {
						session = append(session, llm.UserMessage(entry.Parts...))
					}
					inFlightSteers = delivery
					partialCapture.Set(session)
					continue
				}
			}
			resolved, stopErr := w.applyStopHooks(ctx, session, res)
			if stopErr != nil {
				return nil, stopErr
			}
			if !sameUsage(latestUsage, resolved) {
				accumulateUsage(&totalUsage, usageCopy(resolved))
			}
			attachTotalUsage(&totalUsage, resolved)
			// Propagate the full orchestration session (including intermediate
			// tool calls and results) plus the final assistant message so the
			// agent's Result.Session is the complete conversation tail and the
			// persisters (appendAssistantOutcome / post_turn_runtime) write it to
			// the transcript DB. Without appending resolved.Message, the final
			// assistant answer is streamed to the UI but never persisted, so the
			// next round and /resume replay lose it.
			//
			// Safe (no duplicate): in this no-tool-call branch `session` never
			// received the current resolved.Message - the only append of a
			// response message is at line "session = append(session, *res.Message)"
			// below, which is the tool-call branch. See plan fix-message-persistence-loss.
			resolved.Session = append([]llm.Message(nil), session...)
			if resolved.Message != nil {
				resolved.Session = append(resolved.Session, *resolved.Message)
			}
			return resolved, nil
		}
		session = append(session, *res.Message)
		noteResponseCompleted()
		partialCapture.Set(session)
		toolCtx := withForkRuntimeSnapshot(w.state.ContextWithRuntimeSessionMode(ctx), session)
		results := w.executeToolCalls(toolCtx, res.Message.ToolCalls, toolMap)
		if err := firstControlFlowToolError(results); err != nil {
			// An approval from a typed/fork child is a suspension point, not a
			// regular tool failure. Preserve the child's exact orchestration
			// snapshot so the action resume can replay its pending call.
			attachSessionSnapshotToRAE(err, sessionWithCompletedToolResults(session, res.Message, results))
			return nil, err
		}
		resultByID := make(map[string]orchestratedToolResult, len(results))
		for _, result := range results {
			resultByID[result.call.ID] = result
		}
		// Append tool results in the assistant's original ToolCalls order.
		for _, tc := range res.Message.ToolCalls {
			result, ok := resultByID[tc.ID]
			if !ok {
				continue
			}
			session = append(session, toolResultMessage(result))
		}
		partialCapture.Set(session)
		if shouldStopAfterToolResult(ctx, results) {
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
			resolved := &llm.Result{Message: &msg}
			attachTotalUsage(&totalUsage, resolved)
			resolved.Session = append([]llm.Message(nil), session...)
			resolved.Session = append(resolved.Session, msg)
			return resolved, nil
		}
		// These messages ride on the next model call, and only that call producing
		// output (or completing successfully without a stream) marks them as
		// handed to the model - which is what moves them out of the surface's queue
		// and into its transcript. Taking them here is provisional (see
		// SteerDelivery): the call can fail or be abandoned before that boundary,
		// and messages reported as sent but never answered are gone from the
		// queue the turn boundary resubmits. The context check is the same
		// guarantee one step earlier: an esc interrupt cancels the run while its
		// tools are still executing, and those tools return here with context
		// errors, so the loop reaches this boundary knowing the next model call
		// will be abandoned.
		if delivery := TurnInputRuntimeFromContext(ctx).BeginSteerDelivery(ctx); delivery != nil {
			inFlightSteers = delivery
			sessionBeforeSteers = len(session)
			for _, entry := range delivery.Entries() {
				session = append(session, llm.UserMessage(entry.Parts...))
			}
			partialCapture.Set(session)
		}
	}
}

// withSteerDeliveryResponseStart binds a provisional delivery to the provider's
// response boundary. The provider fires OnResponseStarted before forwarding its
// first output-bearing event, so the surface enqueues the user message before
// any reasoning or assistant output caused by that message. This is also the
// correct transaction boundary: a later stream failure cannot make the
// already-observed model output "undelivered" and put the input back into the
// queue.
func withSteerDeliveryResponseStart(ctx context.Context, delivery *SteerDelivery) context.Context {
	if delivery == nil {
		return ctx
	}
	sink := llm.StreamSinkFrom(ctx)
	if sink == nil {
		return ctx
	}
	wrapped := *sink
	downstream := wrapped.OnResponseStarted
	wrapped.OnResponseStarted = func() {
		delivery.Commit()
		if downstream != nil {
			downstream()
		}
	}
	return llm.WithStreamSink(ctx, &wrapped)
}

func shouldStopAfterToolResult(ctx context.Context, results []orchestratedToolResult) bool {
	if ctx == nil {
		return false
	}
	toolName, _ := ctx.Value(stopAfterToolResultKey{}).(string)
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return false
	}
	for _, result := range results {
		if result.err == nil && strings.TrimSpace(result.call.Function.Name) == toolName {
			return true
		}
	}
	return false
}

func accumulateUsage(total *llm.Usage, usage *llm.Usage) {
	if total == nil || usage == nil {
		return
	}
	total.InputTokens += usage.InputTokens
	total.OutputTokens += usage.OutputTokens
	total.CacheCreationInputTokens += usage.CacheCreationInputTokens
	total.CacheReadInputTokens += usage.CacheReadInputTokens
}

func attachTotalUsage(total *llm.Usage, res *llm.Result) {
	if total == nil || res == nil {
		return
	}
	if total.InputTokens <= 0 && total.OutputTokens <= 0 {
		return
	}
	res.Usage = &llm.Usage{
		InputTokens:              total.InputTokens,
		OutputTokens:             total.OutputTokens,
		CacheCreationInputTokens: total.CacheCreationInputTokens,
		CacheReadInputTokens:     total.CacheReadInputTokens,
	}
}

func usageCopy(res *llm.Result) *llm.Usage {
	if res == nil || res.Usage == nil {
		return nil
	}
	cp := *res.Usage
	return &cp
}

func sameUsage(prev *llm.Usage, res *llm.Result) bool {
	if prev == nil || res == nil || res.Usage == nil {
		return false
	}
	return prev.InputTokens == res.Usage.InputTokens &&
		prev.OutputTokens == res.Usage.OutputTokens &&
		prev.CacheCreationInputTokens == res.Usage.CacheCreationInputTokens &&
		prev.CacheReadInputTokens == res.Usage.CacheReadInputTokens
}

type resumeSnapshot struct {
	session          []llm.Message
	pendingMsg       llm.Message
	completedResults map[string]llm.Message
	denied           bool
	denyReason       string
	// denyFeedback is the user's own words, the display half of the denial
	// while denyReason is the model-facing half.
	denyFeedback string
	// deliveredReview marks a denial review delivery closed: the display half
	// says the handoff line, because there are no user words to state.
	deliveredReview bool
}

// consumeResumeSnapshot extracts and clears the resume state, so subsequent
// recursive Execute calls don't re-replay. The snapshot contains the assistant
// tool_use that triggered approval and may already include tool_result messages
// for earlier tool calls that completed before the approval gate fired.
func (w *toolOrchestrationLLM) consumeResumeSnapshot(ctx context.Context, currentSession []llm.Message) (resumeSnapshot, bool) {
	state := tool.ToolApprovalResumeFromContext(ctx)
	if state == nil || len(state.Session) == 0 {
		return resumeSnapshot{}, false
	}
	pendingIdx := lastAssistantToolUseIndex(state.Session)
	if pendingIdx < 0 {
		return resumeSnapshot{}, false
	}
	pending := state.Session[pendingIdx]
	if pending.Role != llm.RoleAssistant || len(pending.ToolCalls) == 0 {
		return resumeSnapshot{}, false
	}
	// The snapshot is authoritative for the conversation and the pending
	// tool-call structure, and it is replayed verbatim: every message in it has
	// already been sent at that exact position, and a provider serving an
	// implicit prefix cache only keeps serving while those positions hold. The
	// environment message is part of that history, so it stays where it is —
	// lifting it out and re-appending the current one forks the prefix at the
	// message it used to occupy and re-bills the whole conversation after it,
	// then forks it back on the next ordinary turn, which rebuilds from the
	// stored transcript.
	//
	// Freshness is still delivered, by appending: a resume whose environment
	// has changed since the last one in the history — a clear-context resume
	// that carries none, a changed launch cwd — gets the current message added
	// ahead of the pending call, which leaves everything before it cached.
	session := make([]llm.Message, 0, pendingIdx+2)
	session = append(session, state.Session[:pendingIdx]...)
	if environment, found := latestPromptCacheEnvironmentMessage(currentSession); found {
		if previous, had := latestPromptCacheEnvironment(session); !had || previous != environment.TextContent() {
			session = append(session, environment)
		}
	}
	session = append(session, pending)
	completed := completedToolResultsAfterAssistant(state.Session[pendingIdx+1:], pending)
	denied := state.Denied
	denyReason := state.DenyReason
	denyFeedback := state.DenyFeedback
	deliveredReview := state.DeliveredReview
	state.Session = nil
	return resumeSnapshot{session: session, pendingMsg: pending, completedResults: completed, denied: denied, denyReason: denyReason, denyFeedback: denyFeedback, deliveredReview: deliveredReview}, true
}

// buildDenialMessage constructs the tool result message injected when the user
// denies a tool call. When denyReason is non-empty (e.g. the "No, keep
// planning" feedback text), it is appended so the LLM sees the user's
// guidance and can adapt accordingly.
func buildDenialMessage(toolName, denyReason string) string {
	base := fmt.Sprintf("Tool approval denied by user: the user rejected this %s tool call. Try a different approach or ask the user for guidance.", toolName)
	reason := strings.TrimSpace(denyReason)
	if reason != "" {
		return base + "\n\nUser feedback: " + reason
	}
	return base
}

// replayPendingToolCalls re-executes the tool_calls that were attempted before
// the RAE fired and do not already have a captured tool result. The approved
// action ID is already set in ctx by the resume caller, so the approval-gated
// tool now runs for real; earlier completed tools are preserved without a
// second execution.
func (w *toolOrchestrationLLM) replayPendingToolCalls(ctx context.Context, session *[]llm.Message, assistant llm.Message, completed map[string]llm.Message, toolMap map[string]*llm.Tool) error {
	missing := missingToolCalls(assistant, completed)
	if len(missing) == 0 {
		*session = appendToolResultsInAssistantOrder(*session, assistant, completed, nil)
		return nil
	}
	// The fence opens here, immediately before the first replayed call, and
	// nowhere earlier: a resume also rebuilds prompts, runs pre-hooks and may
	// compact, and a process that died in that preamble executed nothing.
	if beginErr := tool.BeginApprovalContinuation(ctx); beginErr != nil {
		return beginErr
	}
	toolCtx := withForkRuntimeSnapshot(ctx, *session)
	results := make([]orchestratedToolResult, 0, len(missing))
	approvedActionID := strings.TrimSpace(tool.ApprovedActionIDFromContext(toolCtx))
	toolCtx = tool.WithApprovedActionID(toolCtx, "")
	for i, call := range missing {
		callCtx := toolCtx
		if approvedActionID != "" {
			callCtx = tool.WithApprovedActionID(callCtx, approvedActionID)
		}
		result := w.executeOneToolCall(callCtx, i, call, toolMap, toolUISuppression{})
		results = append(results, result)
		if isRequiresActionError(result.err) {
			// A second gate inside a replay is the same suspension the first
			// one was, and it is raised on the same action queue whether the
			// run is the primary agent's or a child's. Rewriting a child's
			// gate into a tool error here told the worker its request had been
			// refused for lack of an approval path, which stopped being true
			// once children began suspending through that queue — and it fires
			// exactly where it does most damage, on the replay that a granted
			// approval just started.
			attachSessionSnapshotToRAE(result.err, appendToolResultsInAssistantOrder(*session, assistant, completed, results))
			return result.err
		}
		// Capture replayed tool results so the resume caller can persist them
		// to the session store. Without this, the result lives only in this
		// internal session and is discarded, leaving a dangling tool_calls
		// row that RepairDanglingToolResults later strips.
		if capture := tool.ReplayResultCaptureFromContext(ctx); capture != nil && result.err == nil {
			capture.Add(tool.ReplayResultEntry{
				ToolCallID:  result.call.ID,
				ToolName:    result.call.Function.Name,
				Content:     llm.TextContent(result.parts...),
				Timing:      result.timing,
				ToolDisplay: result.display,
			})
		}
		if approvedActionID != "" {
			approvedActionID = ""
		}
	}
	*session = appendToolResultsInAssistantOrder(*session, assistant, completed, results)
	return nil
}

func lastAssistantToolUseIndex(messages []llm.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == llm.RoleAssistant && len(messages[i].ToolCalls) > 0 {
			return i
		}
	}
	return -1
}

func completedToolResultsAfterAssistant(messages []llm.Message, assistant llm.Message) map[string]llm.Message {
	ids := make(map[string]struct{}, len(assistant.ToolCalls))
	for _, call := range assistant.ToolCalls {
		ids[call.ID] = struct{}{}
	}
	out := make(map[string]llm.Message)
	for _, msg := range messages {
		if msg.Role != llm.RoleTool {
			continue
		}
		id := strings.TrimSpace(msg.ToolCallID)
		if _, ok := ids[id]; !ok {
			continue
		}
		if _, exists := out[id]; !exists {
			out[id] = msg
		}
	}
	return out
}

func missingToolCalls(assistant llm.Message, completed map[string]llm.Message) []llm.ToolCall {
	out := make([]llm.ToolCall, 0, len(assistant.ToolCalls))
	for _, call := range assistant.ToolCalls {
		if _, ok := completed[call.ID]; ok {
			continue
		}
		out = append(out, call)
	}
	return out
}

func appendToolResultsInAssistantOrder(session []llm.Message, assistant llm.Message, completed map[string]llm.Message, fresh []orchestratedToolResult) []llm.Message {
	byID := make(map[string]llm.Message, len(completed)+len(fresh))
	for id, msg := range completed {
		byID[id] = msg
	}
	for _, result := range fresh {
		if isRequiresActionError(result.err) {
			continue
		}
		byID[result.call.ID] = toolResultMessage(result)
	}
	out := append([]llm.Message(nil), session...)
	for _, call := range assistant.ToolCalls {
		if msg, ok := byID[call.ID]; ok {
			out = append(out, msg)
		}
	}
	return out
}

func sessionWithCompletedToolResults(session []llm.Message, assistant *llm.Message, results []orchestratedToolResult) []llm.Message {
	if assistant == nil {
		return append([]llm.Message(nil), session...)
	}
	return appendToolResultsInAssistantOrder(session, *assistant, nil, results)
}

func isRequiresActionError(err error) bool {
	if err == nil {
		return false
	}
	var req *tool.RequiresActionError
	return errors.As(err, &req)
}

func attachSessionSnapshotToRAE(err error, session []llm.Message) {
	var rae *tool.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		return
	}
	if len(rae.SessionSnapshot) > 0 {
		return
	}
	snapshot := make([]llm.Message, len(session))
	copy(snapshot, session)
	rae.SessionSnapshot = snapshot
}

func (w *toolOrchestrationLLM) applyStopHooks(ctx context.Context, session []llm.Message, res *llm.Result) (*llm.Result, error) {
	if w == nil || w.state == nil || res == nil || res.Message == nil {
		return res, nil
	}
	sessionID := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
	if sessionID == "" {
		return res, nil
	}
	transcriptPath := tool.HookTranscriptPathFromContext(ctx)
	if transcriptPath == "" {
		return res, nil
	}
	rtAny := w.state.RuntimeValue("hooks_runtime")
	rt, _ := rtAny.(*hook.Runtime)
	if rt == nil {
		return res, nil
	}
	lastAssistant := strings.TrimSpace(res.Message.TextContent())
	agentID := strings.TrimSpace(tool.HookAgentIDFromContext(ctx))
	if agentID != "" {
		out, err := rt.ExecuteSubagentStop(ctx, hook.SubagentStopInput{
			BaseInput: hook.BaseInput{
				HookEventName:  hook.EventSubagentStop,
				SessionID:      sessionID,
				TranscriptPath: transcriptPath,
				Cwd:            rt.Home,
				AgentID:        agentID,
				AgentType:      strings.TrimSpace(tool.SubagentTypeFromContext(ctx)),
			},
			StopHookActive:       false,
			AgentTranscriptPath:  hook.SidechainTranscriptPath(rt.StateRoot(), sessionID, agentID),
			LastAssistantMessage: lastAssistant,
		})
		if err != nil {
			return nil, err
		}
		if out.Blocked {
			return nil, errors.New(strings.TrimSpace(out.StopReason))
		}
		if strings.TrimSpace(out.AdditionalContext) != "" {
			next := append(append([]llm.Message(nil), session...), llm.UserMessage(llm.Text(out.AdditionalContext)))
			return w.inner.Execute(ctx, next, nil)
		}
		return res, nil
	}
	out, err := rt.ExecuteStop(ctx, hook.StopInput{
		BaseInput: hook.BaseInput{
			HookEventName:  hook.EventStop,
			SessionID:      sessionID,
			TranscriptPath: transcriptPath,
			Cwd:            rt.Home,
		},
		StopHookActive:       false,
		LastAssistantMessage: lastAssistant,
	})
	if err != nil {
		return nil, err
	}
	if out.Blocked {
		return nil, errors.New(strings.TrimSpace(out.StopReason))
	}
	if strings.TrimSpace(out.AdditionalContext) != "" {
		next := append(append([]llm.Message(nil), session...), llm.UserMessage(llm.Text(out.AdditionalContext)))
		return w.inner.Execute(ctx, next, nil)
	}
	return res, nil
}

func (w *toolOrchestrationLLM) executeToolCalls(
	ctx context.Context,
	calls []llm.ToolCall,
	toolMap map[string]*llm.Tool,
) []orchestratedToolResult {
	results := make([]orchestratedToolResult, len(calls))
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		names = append(names, call.Function.Name)
	}
	plans := w.state.PartitionToolCalls(names)
	callIndex := 0
	for _, plan := range plans {
		batch := calls[callIndex : callIndex+len(plan.ToolNames)]
		if plan.ConcurrencySafe && !w.requiresSequentialApproval(batch) {
			groups := parallelToolCallSummaryGroups(batch, toolMap)
			w.emitParallelBatchSummary(ctx, groups, tool.StepKindToolParallelStarted, nil)
			w.executeConcurrentBatch(ctx, batch, callIndex, toolMap, results)
			w.emitParallelBatchSummary(ctx, groups, tool.StepKindToolParallelCompleted, results[callIndex:callIndex+len(batch)])
		} else {
			for i, call := range batch {
				results[callIndex+i] = w.executeOneToolCall(ctx, callIndex+i, call, toolMap, toolUISuppression{})
				if isRequiresActionError(results[callIndex+i].err) {
					return results
				}
			}
		}
		callIndex += len(plan.ToolNames)
	}
	return results
}

func (w *toolOrchestrationLLM) requiresSequentialApproval(batch []llm.ToolCall) bool {
	if w == nil || w.state == nil || len(batch) == 0 {
		return false
	}
	return w.state.ActionHook() != nil
}

func (w *toolOrchestrationLLM) emitParallelBatchSummary(ctx context.Context, groups []parallelToolCallSummaryGroup, kind string, results []orchestratedToolResult) {
	if w == nil || w.state == nil || len(groups) == 0 {
		return
	}
	step := tool.StepHookFromContext(ctx, w.state)
	if step == nil {
		return
	}
	// A group's summary card runs from the moment its batch is dispatched.
	var startedAt time.Time
	if kind == tool.StepKindToolParallelStarted {
		startedAt = time.Now()
	}
	for _, group := range groups {
		if len(group.labels) < 2 {
			continue
		}
		output := map[string]any{
			"summary": strings.TrimSpace(group.summary),
			"count":   len(group.labels),
			"items":   append([]string(nil), group.labels...),
		}
		var timing llm.ExecutionTiming
		errText := ""
		if kind == tool.StepKindToolParallelCompleted {
			memberTimings := make([]llm.ExecutionTiming, 0, len(group.resultIndexes))
			failed := 0
			for _, index := range group.resultIndexes {
				if index < 0 || index >= len(results) {
					continue
				}
				memberTimings = append(memberTimings, results[index].timing)
				if results[index].err != nil {
					failed++
					if errText == "" {
						errText = results[index].err.Error()
					}
				}
			}
			timing = llm.AggregateExecutionTimings(memberTimings...)
			output["failed"] = failed
		}
		step(ctx, tool.StepEvent{
			Kind:            kind,
			StepID:          group.stepID,
			ToolName:        group.toolName,
			ToolDescription: group.description,
			Output:          output,
			Error:           errText,
			Duration:        timing.Duration,
			StartedAt:       startedAt,
		})
	}
}

type parallelToolCallSummaryGroup struct {
	stepID        string
	toolName      string
	description   string
	labels        []string
	summary       string
	resultIndexes []int
}

func parallelToolCallSummaryGroups(batch []llm.ToolCall, toolMap map[string]*llm.Tool) []parallelToolCallSummaryGroup {
	type acc struct {
		toolName      string
		description   string
		labels        []string
		resultIndexes []int
	}
	groups := make([]*acc, 0, len(batch))
	byName := make(map[string]*acc)
	for index, call := range batch {
		name := strings.TrimSpace(call.Function.Name)
		if name == "" {
			continue
		}
		g := byName[name]
		if g == nil {
			g = &acc{toolName: name}
			if tool := toolMap[name]; tool != nil {
				g.description = strings.TrimSpace(tool.Description())
			}
			byName[name] = g
			groups = append(groups, g)
		}
		g.labels = append(g.labels, toolCallItemLabel(name, call.Function.Arguments))
		g.resultIndexes = append(g.resultIndexes, index)
	}
	out := make([]parallelToolCallSummaryGroup, 0, len(groups))
	for _, g := range groups {
		labels := compactNonEmptyLabels(g.labels)
		if len(labels) == 0 {
			continue
		}
		out = append(out, parallelToolCallSummaryGroup{
			stepID:        toolBatchStepID(g.toolName, labels),
			toolName:      g.toolName,
			description:   g.description,
			labels:        labels,
			summary:       toolBatchSummaryText(g.toolName, labels),
			resultIndexes: append([]int(nil), g.resultIndexes...),
		})
	}
	return out
}

func toolBatchStepID(toolName string, labels []string) string {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		toolName = "tool"
	}
	keyParts := append([]string{toolName}, labels...)
	sum := sha256.Sum256([]byte(strings.Join(keyParts, "\x00")))
	return "batch-" + hex.EncodeToString(sum[:8])
}

func toolCallItemLabel(toolName, arguments string) string {
	args := toolInputMap(arguments)
	switch strings.TrimSpace(toolName) {
	case "read_file", "write_file", "edit_file":
		return pathBaseOrValue(firstStringInMap(args, "file_path", "path", "abs_path"))
	case "web_search":
		return firstStringInMap(args, "query", "q", "symbol", "name")
	case "web_fetch":
		return firstStringInMap(args, "url", "uri")
	case "shell":
		return firstStringInMap(args, "command")
	}
	if label := firstStringInMap(args, "file_path", "path", "name", "query", "pattern", "command", "url"); label != "" {
		return pathBaseOrValue(label)
	}
	return strings.TrimSpace(toolName)
}

func compactNonEmptyLabels(labels []string) []string {
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		label = strings.Join(strings.Fields(strings.TrimSpace(label)), " ")
		if label == "" {
			continue
		}
		if len(label) > 80 {
			label = llm.TruncateBytes(label, 77, "...")
		}
		out = append(out, label)
	}
	return out
}

func toolBatchSummaryText(toolName string, labels []string) string {
	verb := toolBatchVerb(toolName)
	if len(labels) == 0 {
		return verb + " " + strings.TrimSpace(toolName)
	}
	return verb + " " + strings.Join(labels, ", ")
}

func toolBatchVerb(toolName string) string {
	switch strings.TrimSpace(toolName) {
	case "read_file":
		return "running read"
	case "web_fetch":
		return "Fetched"
	case "web_search":
		return "Searched"
	}
	name := strings.TrimSpace(toolName)
	if name == "" {
		return "Ran"
	}
	return "Ran " + name
}

func firstStringInMap(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			text := strings.TrimSpace(fmt.Sprint(v))
			if text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func pathBaseOrValue(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = strings.TrimRight(path, `/\`)
	if idx := strings.LastIndexAny(path, `/\`); idx >= 0 && idx+1 < len(path) {
		return path[idx+1:]
	}
	return path
}

func (w *toolOrchestrationLLM) executeConcurrentBatch(
	ctx context.Context,
	batch []llm.ToolCall,
	offset int,
	toolMap map[string]*llm.Tool,
	results []orchestratedToolResult,
) {
	limit := tool.MaxToolConcurrencyFromEnv()
	if limit <= 0 {
		limit = len(batch)
	}
	suppressStarted := make(map[string]bool)
	suppressCompleted := make(map[string]bool)
	for _, group := range parallelToolCallSummaryGroups(batch, toolMap) {
		if len(group.labels) >= 2 {
			suppressStarted[group.toolName] = true
			if strings.TrimSpace(group.toolName) != "read_file" {
				suppressCompleted[group.toolName] = true
			}
		}
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i, call := range batch {
		idx := offset + i
		toolName := strings.TrimSpace(call.Function.Name)
		suppress := toolUISuppression{
			started:   suppressStarted[toolName],
			completed: suppressCompleted[toolName],
		}
		wg.Add(1)
		go func(index int, tc llm.ToolCall, suppress toolUISuppression) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				results[index] = orchestratedToolResult{index: index, call: tc, err: ctx.Err()}
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			results[index] = w.executeOneToolCall(ctx, index, tc, toolMap, suppress)
		}(idx, call, suppress)
	}
	wg.Wait()
}

type toolUISuppression struct {
	started   bool
	completed bool
}

func (w *toolOrchestrationLLM) executeOneToolCall(
	ctx context.Context,
	index int,
	call llm.ToolCall,
	toolMap map[string]*llm.Tool,
	suppress toolUISuppression,
) orchestratedToolResult {
	name := strings.TrimSpace(call.Function.Name)
	tracer := agent.FromContext(ctx)
	inProgressID := strings.TrimSpace(call.ID)
	if inProgressID != "" {
		w.state.MarkToolInProgress(inProgressID)
		defer w.state.ClearToolInProgress(inProgressID)
	}
	tracer.Trace(agent.ToolCallEvent{
		ToolName:  name,
		Arguments: call.Function.Arguments,
		CallID:    call.ID,
		Timestamp: time.Now(),
	})
	stepID := toolStepID(call)
	stepInput := toolInputMap(call.Function.Arguments)
	skillName, skillPath := loadedSkillIdentity(w.state, name, stepInput)
	// Category is the structured identity of a skill step. Carrying it here as
	// well as on an explicit invocation is what lets one rendering path serve
	// both, instead of each tool that can load a skill being recognized by its
	// own name downstream.
	skillCategory := ""
	if skillName != "" && skillPath != "" {
		skillCategory = "skill"
	}
	toolDescription := ""
	if tool := toolMap[name]; tool != nil {
		toolDescription = strings.TrimSpace(tool.Description())
	}
	// The call's start is stamped here, where it is dispatched, and travels
	// with the call's context: every event of this call — the start, the
	// handler's output deltas, the completion — carries the same StartedAt.
	startedAt := time.Now()
	if step := tool.StepHookFromContext(ctx, w.state); step != nil {
		step(ctx, tool.StepEvent{
			Kind:            tool.StepKindToolStarted,
			StepID:          stepID,
			ToolName:        name,
			ToolDescription: toolDescription,
			Input:           stepInput,
			StartedAt:       startedAt,
			SuppressUI:      suppress.started,
			SkillName:       skillName,
			SkillPath:       skillPath,
			Category:        skillCategory,
			Origin:          skillStepOrigin(skillCategory),
		})
	}
	toolCtx := withToolStepCapture(tool.WithToolUseID(ctx, strings.TrimSpace(call.ID)), stepID)
	toolCtx = tool.WithToolStepStartedAt(toolCtx, startedAt)
	rawResult, parts, timing, err := executeToolHandler(toolCtx, toolMap, call)
	governedDetails := governToolResultDetails(w.state, call, parts)
	recordToolResultSpills(ctx, w.state, name, call, governedDetails)
	output, errText, actionID, actionKind := toolCompletedPayload(toolCtx, rawResult, parts, err, governedDetails)
	completedEvent := tool.StepEvent{
		Kind:            tool.StepKindToolCompleted,
		StepID:          stepID,
		ToolName:        name,
		ToolDescription: toolDescription,
		Input:           stepInput,
		Output:          output,
		Error:           errText,
		ActionID:        actionID,
		ActionKind:      actionKind,
		Duration:        timing.Duration,
		StartedAt:       startedAt,
		SuppressUI:      suppress.completed,
		ExternalContext: err == nil && toolMap[name] != nil && toolMap[name].ContainsExternalContext(),
		SkillName:       skillName,
		SkillPath:       skillPath,
		Category:        skillCategory,
		Origin:          skillStepOrigin(skillCategory),
	}
	if completedEvent.ExternalContext {
		llm.NotifyExternalContext(ctx)
	}
	if step := tool.StepHookFromContext(ctx, w.state); step != nil {
		for i, attempt := range tool.ToolAttemptsFromContext(toolCtx) {
			attemptEvent := completedEvent
			attemptEvent.Output = attempt.Output
			attemptEvent.Error = attempt.Error
			attemptEvent.ActionID = attempt.ActionID
			attemptEvent.ActionKind = attempt.ActionKind
			attemptEvent.RetainAsHistory = true
			// The ordinal is this attempt's half of its event identity: without
			// it the attempt and the completion that supersedes it claim the
			// same id, and the log — idempotent by id — keeps only whichever
			// arrived first.
			attemptEvent.Attempt = i + 1
			if attempt.Duration > 0 {
				attemptEvent.Duration = attempt.Duration
			}
			step(ctx, attemptEvent)
		}
		step(ctx, completedEvent)
	}
	resultText := llm.TextContent(parts...)
	if err != nil {
		resultText = err.Error()
	}
	tracer.Trace(agent.ToolResultEvent{
		ToolName:  name,
		Result:    resultText,
		Err:       err,
		CallID:    call.ID,
		Timestamp: time.Now(),
	})
	if err == nil && timing.Duration > 30*time.Second {
		if step := tool.StepHookFromContext(ctx, w.state); step != nil {
			step(ctx, tool.StepEvent{
				Kind:     "tool_slow",
				StepID:   stepID,
				ToolName: name,
				Output: map[string]any{
					"duration_ms": timing.Duration.Milliseconds(),
				},
			})
		}
	}
	return orchestratedToolResult{
		index:   index,
		call:    call,
		parts:   governedContentParts(parts, governedDetails),
		timing:  timing,
		display: toolDisplayState(completedEvent),
		err:     err,
	}
}

// skillStepOrigin labels a skill step the model itself produced, so the ledger
// can tell it apart from a skill the user selected.
func skillStepOrigin(category string) string {
	if category == "" {
		return ""
	}
	return "llm-load"
}

// loadedSkillIdentity names the skill a call is loading, from the call's own
// arguments, so the step is a skill step from the moment it starts — before any
// result exists to inspect. The skill tool addresses a skill by name; a
// read_file of a loaded skill's own SKILL.md addresses the same thing by path,
// and both must produce the one skill identity the card and the ledger read.
func loadedSkillIdentity(st *tool.State, toolName string, input map[string]any) (name, path string) {
	if st == nil {
		return "", ""
	}
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "skill":
		requested, _ := input["name"].(string)
		item, ok := loadedSkillByName(st, requested)
		if !ok {
			return "", ""
		}
		return strings.TrimSpace(item.Name), filepath.Join(item.RootDir, "SKILL.md")
	case "read_file":
		requested, _ := input["file_path"].(string)
		item, mainFile, ok := st.LoadedSkillMainFile(requested)
		if !ok {
			return "", ""
		}
		return strings.TrimSpace(item.Name), strings.TrimSpace(mainFile)
	default:
		return "", ""
	}
}

func executeToolHandler(ctx context.Context, toolMap map[string]*llm.Tool, call llm.ToolCall) (any, []llm.ContentPart, llm.ExecutionTiming, error) {
	name := strings.TrimSpace(call.Function.Name)
	tool := toolMap[name]
	if tool == nil {
		return nil, nil, llm.ExecutionTiming{}, fmt.Errorf("unknown tool %q", name)
	}
	execution, err := tool.Execute(ctx, call.Function.Arguments)
	if err != nil {
		return execution.Result, nil, execution.Timing, fmt.Errorf("tool %s: %w", name, err)
	}
	return execution.Result, toolResultContentParts(execution.Result), execution.Timing, nil
}

func toolResultMessage(result orchestratedToolResult) llm.Message {
	var msg llm.Message
	if result.err != nil {
		text := fmt.Sprintf("Error executing tool '%s': %v", result.call.Function.Name, result.err)
		if errors.Is(result.err, tool.ErrOldStringNotFound) {
			text += "\n\nIMPORTANT: The file was modified since you last read it. Call read_file to get the current content before retrying edit_file — your old_string must match what is currently on disk."
		}
		msg = llm.ToolResultMessage(result.call.ID, llm.Text(text))
	} else {
		msg = llm.ToolResultMessage(result.call.ID, result.parts...)
	}
	if result.timing.Valid() {
		timing := result.timing
		msg.ToolExecutionTiming = &timing
	}
	if result.display != nil {
		display := *result.display
		msg.ToolDisplay = &display
	}
	return msg
}

func toolDisplayState(evt tool.StepEvent) *llm.ToolDisplayState {
	body, _ := tool.FormatToolStepResult(evt, tool.DefaultMaxFormattedBody)
	metaJSON := ""
	if encoded, err := json.Marshal(tool.BuildToolMeta(evt)); err == nil {
		metaJSON = string(encoded)
	}
	return &llm.ToolDisplayState{
		Body:         strings.TrimSpace(body),
		Summary:      strings.TrimSpace(tool.SummarizeToolStep(evt)),
		ToolMetaJSON: metaJSON,
	}
}

func toolResultContentParts(v any) []llm.ContentPart {
	if v == nil {
		return nil
	}
	if parts, ok := v.([]llm.ContentPart); ok {
		return parts
	}
	if s, ok := v.(string); ok {
		return []llm.ContentPart{llm.Text(s)}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []llm.ContentPart{llm.Text(fmt.Sprintf("failed to marshal tool result: %v", err))}
	}
	return []llm.ContentPart{llm.Text(string(b))}
}

func governedContentParts(parts []llm.ContentPart, details []tool.GovernedOutput) []llm.ContentPart {
	if len(details) == 0 {
		return parts
	}
	out := make([]llm.ContentPart, 0, len(parts))
	textIdx := 0
	for _, part := range parts {
		if part.Type != llm.ContentTypeText || strings.TrimSpace(part.Text) == "" {
			out = append(out, part)
			continue
		}
		if textIdx >= len(details) {
			out = append(out, part)
			continue
		}
		out = append(out, llm.Text(details[textIdx].Text))
		textIdx++
	}
	return out
}

func governToolResultDetails(st *tool.State, call llm.ToolCall, parts []llm.ContentPart) []tool.GovernedOutput {
	if len(parts) == 0 {
		return nil
	}
	governor := newToolOutputGovernor(st)
	if governor == nil {
		return nil
	}
	out := make([]tool.GovernedOutput, 0, len(parts))
	for _, part := range parts {
		if part.Type != llm.ContentTypeText || strings.TrimSpace(part.Text) == "" {
			continue
		}
		governed := governor.GovernDetailed(strings.TrimSpace(call.Function.Name), strings.TrimSpace(call.ID), part.Text)
		out = append(out, governed)
	}
	return out
}

func recordToolResultSpills(ctx context.Context, st *tool.State, toolName string, call llm.ToolCall, governed []tool.GovernedOutput) {
	if st == nil || len(governed) == 0 {
		return
	}
	runID := strings.TrimSpace(tool.RunIDFromContext(ctx))
	sessionID := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
	if runID == "" && sessionID == "" {
		return
	}
	for _, item := range governed {
		path := strings.TrimSpace(item.StoredPath)
		if path == "" {
			continue
		}
		st.RecordToolResultSpill(tool.ToolResultSpill{
			SessionID:     sessionID,
			RunID:         runID,
			ToolName:      toolName,
			CallID:        strings.TrimSpace(call.ID),
			Path:          path,
			OriginalBytes: item.OriginalBytes,
			OmittedBytes:  item.OmittedBytes,
			TotalLines:    item.TotalLines,
			CreatedAt:     time.Now().UTC(),
		})
	}
}

func newToolOutputGovernor(st *tool.State) *tool.OutputGovernor {
	if st == nil {
		return tool.NewOutputGovernor("")
	}
	dir := strings.TrimSpace(st.ToolResultDir())
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "forebrain-"+tool.SpillDir)
		st.SetToolResultDir(dir)
	}
	return tool.NewOutputGovernor(dir)
}

func toolInputMap(arguments string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(arguments) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(arguments), &out); err != nil {
		return map[string]any{"raw": arguments}
	}
	return out
}

func toolStepID(call llm.ToolCall) string {
	id := strings.TrimSpace(call.ID)
	if id != "" {
		return id
	}
	name := strings.TrimSpace(call.Function.Name)
	if name == "" {
		name = "tool"
	}
	return fmt.Sprintf("%s:%d", name, time.Now().UnixNano())
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func withToolStepCapture(ctx context.Context, stepID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	data := &capturedToolStepData{}
	ctx = context.WithValue(ctx, capturedToolStepKey{}, data)
	ctx = tool.WithToolCompletionCapture(ctx)
	ctx = tool.WithPolicyApprovalReasonCapture(ctx)
	return tool.WithToolStepID(ctx, stepID)
}

func toolCompletedPayload(ctx context.Context, rawResult any, parts []llm.ContentPart, err error, governed []tool.GovernedOutput) (map[string]any, string, string, string) {
	var req *tool.RequiresActionError
	errText := errorString(err)
	actionID := ""
	actionKind := ""
	if errors.As(err, &req) {
		errText = ""
		actionID = strings.TrimSpace(req.ActionID)
		actionKind = strings.TrimSpace(req.ActionKind)
	}
	if data, _ := ctx.Value(capturedToolStepKey{}).(*capturedToolStepData); data != nil {
		if captured, ok := tool.ToolCompletionFromContext(ctx); ok {
			data.completed = &captured
			if strings.TrimSpace(captured.ActionID) != "" {
				data.actionID = strings.TrimSpace(captured.ActionID)
			}
			if strings.TrimSpace(captured.ActionKind) != "" {
				data.actionKind = strings.TrimSpace(captured.ActionKind)
			}
		}
		if strings.TrimSpace(actionID) == "" {
			actionID = strings.TrimSpace(data.actionID)
		}
		if strings.TrimSpace(actionKind) == "" {
			actionKind = strings.TrimSpace(data.actionKind)
		}
		if data.completed != nil {
			cp := data.completed
			if strings.TrimSpace(cp.Error) != "" {
				errText = strings.TrimSpace(cp.Error)
			}
			if strings.TrimSpace(cp.ActionID) != "" && strings.TrimSpace(actionID) == "" {
				actionID = strings.TrimSpace(cp.ActionID)
			}
			if strings.TrimSpace(cp.ActionKind) != "" && strings.TrimSpace(actionKind) == "" {
				actionKind = strings.TrimSpace(cp.ActionKind)
			}
			return applyGovernedToolMetadata(enrichCapturedSearchOutput(cloneStepOutput(cp.Output), rawResult), governed), errText, actionID, actionKind
		}
	}
	return applyGovernedToolMetadata(fallbackToolOutput(rawResult, parts, req), governed), errText, actionID, actionKind
}

func applyGovernedToolMetadata(output map[string]any, governed []tool.GovernedOutput) map[string]any {
	if len(output) == 0 {
		output = map[string]any{}
	}
	for _, item := range governed {
		if !item.Truncated && strings.TrimSpace(item.StoredPath) == "" && item.OmittedBytes == 0 {
			continue
		}
		return item.ApplyMetadata(output)
	}
	return output
}

func fallbackToolOutput(rawResult any, parts []llm.ContentPart, req *tool.RequiresActionError) map[string]any {
	if req != nil {
		return map[string]any{"requires_action": true}
	}
	if structured, ok := rawResult.(map[string]any); ok {
		return cloneStepOutput(structured)
	}
	out := map[string]any{}
	if s, ok := rawResult.(string); ok {
		out["output"] = s
		return out
	}
	text := llm.TextContent(parts...)
	if strings.TrimSpace(text) != "" {
		out["output"] = text
	}
	if len(out) == 0 && rawResult != nil {
		if b, err := json.Marshal(rawResult); err == nil {
			out["output"] = string(b)
		}
	}
	return out
}

func enrichCapturedSearchOutput(output map[string]any, rawResult any) map[string]any {
	if len(output) == 0 {
		return output
	}
	if hasNonEmptyStringValue(output, "preview_text") || hasNonEmptyStringValue(output, "stdout_preview") {
		return output
	}
	raw, ok := rawResult.(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return output
	}
	if preview := searchResultPreview(raw); preview != "" {
		output["preview_text"] = preview
	}
	return output
}

func hasNonEmptyStringValue(m map[string]any, key string) bool {
	if len(m) == 0 {
		return false
	}
	v, ok := m[key]
	if !ok {
		return false
	}
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) != ""
}

func searchResultPreview(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	if preview := codeSearchResultPreview(payload); preview != "" {
		return preview
	}
	return ""
}

func codeSearchResultPreview(payload map[string]json.RawMessage) string {
	raw := payload["matches"]
	if len(raw) == 0 {
		return ""
	}
	var matches []struct {
		Path    string `json:"path"`
		Line    int    `json:"line"`
		Preview string `json:"preview"`
	}
	if err := json.Unmarshal(raw, &matches); err != nil || len(matches) == 0 {
		return ""
	}
	lines := make([]string, 0, len(matches))
	for _, item := range matches {
		path := strings.TrimSpace(item.Path)
		preview := strings.TrimSpace(item.Preview)
		if path == "" && preview == "" {
			continue
		}
		if item.Line > 0 {
			lines = append(lines, fmt.Sprintf("%s:%d: %s", path, item.Line, preview))
			continue
		}
		if preview != "" {
			lines = append(lines, fmt.Sprintf("%s: %s", path, preview))
			continue
		}
		lines = append(lines, path)
	}
	return strings.Join(lines, "\n")
}

func cloneStepOutput(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func firstControlFlowToolError(results []orchestratedToolResult) error {
	for _, result := range results {
		if result.err == nil {
			continue
		}
		var req *tool.RequiresActionError
		if errors.As(result.err, &req) {
			return result.err
		}
	}
	return nil
}

func isEmptyAssistantResult(res *llm.Result) bool {
	if res == nil || res.Message == nil {
		return true
	}
	if len(res.Message.ToolCalls) > 0 {
		return false
	}
	return strings.TrimSpace(res.Message.TextContent()) == ""
}

func sessionHasToolExecution(session []llm.Message) bool {
	for _, msg := range session {
		if msg.Role == llm.RoleTool {
			return true
		}
	}
	return false
}

type ToolRegistry struct {
	agent      *agent.Agent
	canUseTool func(string) bool
	// tools is the registering runtime's state. A fork runs the dispatching
	// agent's tools, so it runs them in that agent's middleware chain — its
	// permissions, its hooks — rather than whichever chain a package variable
	// happened to hold.
	tools *tool.State
}

func NewToolRegistry(a *agent.Agent, canUseTool func(string) bool, tools *tool.State) *ToolRegistry {
	return &ToolRegistry{agent: a, canUseTool: canUseTool, tools: tools}
}

func (r *ToolRegistry) Add(t *llm.Tool) error {
	if t == nil {
		return nil
	}
	if r == nil || r.agent == nil {
		return fmt.Errorf("nil tool registry")
	}
	if r.canUseTool != nil && !r.canUseTool(t.Name()) {
		return nil
	}
	t.Use(func(t *llm.Tool, next llm.ToolHandler) llm.ToolHandler {
		return func(ctx context.Context, arguments string) (any, error) {
			if r.canUseTool != nil && !r.canUseTool(t.Name()) {
				return nil, fmt.Errorf("tool %q not allowed in forked subagent", t.Name())
			}
			return next(ctx, arguments)
		}
	})
	return r.tools.Register(r.agent, t)
}

func debuglogToolsView(a *agent.Agent) []*llm.Tool {
	if a == nil {
		return nil
	}
	tools := a.Tools()
	out := make([]*llm.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool != nil {
			out = append(out, tool)
		}
	}
	return out
}

func cloneToolSlice(in []*llm.Tool) []*llm.Tool {
	if len(in) == 0 {
		return nil
	}
	return append([]*llm.Tool(nil), in...)
}

// codegraphExploreToolName is the MCP tool the codegraph prompt refers to.
const codegraphExploreToolName = "mcp__codegraph__codegraph_explore"

const codegraphPromptInstruction = "When the CodeGraph tool mcp__codegraph__codegraph_explore is available, prefer it first for codebase exploration and code retrieval before broader file search."

type codegraphPromptLLM struct {
	inner llm.LLM
}

func wrapCodegraphPromptLLM(inner llm.LLM) llm.LLM {
	if inner == nil {
		return nil
	}
	return codegraphPromptLLM{inner: inner}
}

func (w codegraphPromptLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if hasToolNamed(tools, codegraphExploreToolName) {
		messages = appendInstructionToSystemMessage(messages, codegraphPromptInstruction)
	}
	return w.inner.Execute(ctx, messages, tools)
}

func hasToolNamed(tools []*llm.Tool, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return false
	}
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(tool.Name()), want) {
			return true
		}
	}
	return false
}

// appendInstructionToSystemMessage prepends an instruction onto the leading
// system message (creating one when absent) and returns a new slice; the
// input slice is never mutated.
func appendInstructionToSystemMessage(messages []llm.Message, instruction string) []llm.Message {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return messages
	}
	out := append([]llm.Message(nil), messages...)
	if len(out) == 0 {
		return []llm.Message{llm.SystemMessage(instruction)}
	}
	if out[0].Role == llm.RoleSystem {
		base := strings.TrimSpace(out[0].TextContent())
		if base == "" {
			out[0] = llm.SystemMessage(instruction)
		} else {
			out[0] = llm.SystemMessage(base + "\n\n" + instruction)
		}
		return out
	}
	return append([]llm.Message{llm.SystemMessage(instruction)}, out...)
}
