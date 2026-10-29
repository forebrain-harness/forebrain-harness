// Tool-call orchestration for a turn.
package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	// transcript, when set, receives the run's conversation and its reasoning
	// blocks as each block ends, not only when the turn does. nil disables it
	// (a bare wrapper with no store).
	transcript transcriptProgressStore
	// transcriptModel names the model the run's rows record. nil reads as "".
	transcriptModel func(context.Context) string
	// withdrawGate cancels the pending action of an approval gate the turn
	// does not stop on. nil leaves it pending (a bare wrapper with no action
	// queue).
	withdrawGate func(err error)
}

type stopAfterToolResultKey struct{}

type orchestratedToolResult struct {
	index   int
	call    llm.ToolCall
	parts   []llm.ContentPart
	timing  llm.ExecutionTiming
	display *llm.ToolDisplayState
	err     error
	// notRun marks a call the turn never started because a queued user
	// message took the turn at the call boundary. Its result is the model-facing
	// "not run" answer, and no surface draws a card for it.
	notRun bool
	// deferred marks a call that started while its response was still
	// streaming and reached an approval or a question: nothing was created,
	// and the call runs again once the stream ends.
	deferred bool
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

// wrapToolOrchestrationLLMWithProgress is the product construction: the same
// orchestration, plus the session store its conversation is written to as each
// block ends. model names the model those rows record; nil reads as "". A
// store-less or model-less test keeps using the plain constructors above.
func wrapToolOrchestrationLLMWithProgress(inner llm.LLM, state *tool.State, home string, transcript transcriptProgressStore, model func(context.Context) string) llm.LLM {
	w := wrapToolOrchestrationLLMWithHome(inner, state, home)
	if o, ok := w.(*toolOrchestrationLLM); ok {
		o.transcript = transcript
		o.transcriptModel = model
	}
	return w
}

// recordReasoningBlocks wraps the foreground sink so each reasoning block is
// written to the transcript as the block ends, then chains to the surface's own
// callbacks untouched. A block ends when the provider signals OnReasoningDone
// within one response — the "thinking turned into the answer" boundary — or,
// for a provider that never signals it, at the first answer delta that follows
// the thinking. Both paths write once and clear the buffer, so the same block
// is never written twice.
func (w *toolOrchestrationLLM) recordReasoningBlocks(ctx context.Context, sink *llm.StreamSink, transcript *transcriptProgress) *llm.StreamSink {
	if sink == nil || transcript == nil {
		return sink
	}
	// The write must survive a cancel that lands as the surfaces tear the turn
	// down, and the closure must not read the loop's reassigned ctx.
	sinkCtx := context.WithoutCancel(ctx)
	wrapped := *sink
	downstreamReasoning := wrapped.OnReasoningDelta
	downstreamDone := wrapped.OnReasoningDone
	downstreamDelta := wrapped.OnDelta
	finishReasoning := func() {
		if text, startedAt, ok := transcript.takeReasoning(); ok {
			transcript.reasoningDone(sinkCtx, text, startedAt, time.Now())
		}
	}
	wrapped.OnReasoningDelta = func(text string) {
		transcript.appendReasoning(text)
		if downstreamReasoning != nil {
			downstreamReasoning(text)
		}
	}
	wrapped.OnReasoningDone = func() {
		finishReasoning()
		if downstreamDone != nil {
			downstreamDone()
		}
	}
	if downstreamDelta != nil {
		wrapped.OnDelta = func(text string) {
			finishReasoning()
			downstreamDelta(text)
		}
	}
	// A transport retry re-sends the whole request from the first token, so
	// the reasoning block the dead attempt was still streaming is void — it
	// is dropped here, never written — while blocks that already ended were
	// written when they ended: a finish keeps them, and a resend that says
	// so (DropFinishedThinking) withdraws their rows with it. The drop runs
	// on every notification, Cleared included: at exhaustion the final dead
	// attempt only ever gets Cleared, and on success that notice lands
	// before the retried stream's first token, when nothing is in flight —
	// a no-op. The downstream call keeps the surface's own handling alive.
	downstreamTransportRetry := wrapped.OnTransportRetry
	wrapped.OnTransportRetry = func(tr llm.TransportRetry) {
		transcript.dropInFlightReasoning()
		if tr.DropFinishedThinking {
			transcript.dropFinishedReasoning(sinkCtx)
		}
		if downstreamTransportRetry != nil {
			downstreamTransportRetry(tr)
		}
	}
	return &wrapped
}

// steerMessages turns delivered steers into the messages the model reads. A
// steer typed as a skill command in a subagent's view first loads that skill,
// with its own Skill card, and the skill's instructions go in just ahead of
// the steer — the same activation a turn that starts with the command gets.
// Every message lands after the delivery's start, so the rollback and the
// steer count of an abandoned call cover the activation with its steer. A
// skill that fails to load shows its failure card and the steer is still
// delivered: the user's words reach the model either way.
//
// The activation is a persisted meta message, not an ephemeral one: the steer
// that brought it is persisted at that point of the conversation, and a
// mid-turn compaction counts the delivery's messages by position.
func (w *toolOrchestrationLLM) steerMessages(ctx context.Context, entries []TurnInputEntry) []llm.Message {
	out := make([]llm.Message, 0, len(entries))
	for i, entry := range entries {
		name, path := strings.TrimSpace(entry.SkillName), strings.TrimSpace(entry.SkillPath)
		if name != "" && path != "" {
			stepID := fmt.Sprintf("skill-steer-%d-%d", i, time.Now().UnixNano())
			selection := explicitSkillSelection{SkillName: name, SkillPath: path}
			if activation, err := loadExplicitSkill(ctx, w.state, selection, stepID); err == nil {
				msg := explicitSkillActivationMessage(activation)
				msg.Ephemeral = false
				out = append(out, msg)
			}
		}
		out = append(out, llm.UserMessage(entry.Parts...))
	}
	return out
}

// WrapToolOrchestrationLLM exposes the canonical execution-chain wrapper used
// by Forebrain Harness product runtimes when tool results must be governed before they are
// replayed into model context.
//
// A request abandoned to send the steers queued behind it — Esc's "interrupt and
// send immediately" — is not a cancellation: the turn keeps what that call
// produced and asks again carrying them, so one run still reaches one boundary.
func (w *toolOrchestrationLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if w == nil || w.inner == nil {
		return nil, fmt.Errorf("nil orchestrated llm")
	}
	ctx = w.state.ContextWithRuntimeSessionMode(ctx)
	tools = effectiveToolsForContext(ctx, tools)
	// Write the run's conversation to its transcript as each block ends, not
	// only when the turn does. Nothing here when no store is wired, when the
	// context names no session, or when the model call has no foreground sink
	// to hang the reasoning-block callbacks on.
	transcript := newTranscriptProgress(w.transcript, llm.AgentSessionIDFromContext(ctx), tool.RunIDFromContext(ctx))
	if transcript != nil {
		if sink := llm.StreamSinkFrom(ctx); sink != nil {
			ctx = llm.WithStreamSink(ctx, w.recordReasoningBlocks(ctx, sink, transcript))
		}
	}
	// modelForTranscript names the model the rows this run writes record; the
	// surface's own write at the turn's end reconciles against the same rows.
	modelForTranscript := func() string {
		if w == nil || w.transcriptModel == nil {
			return ""
		}
		return w.transcriptModel(ctx)
	}
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
		if resumeAssistant.stopped {
			// The user refused without words: the refusal is recorded on the
			// parked call, the calls behind it are answered with the same
			// sentence under a hidden display, and the turn ends without a
			// model request — the run's surface finishes it as a cancel.
			missing := missingToolCalls(resumeAssistant.pendingMsg, resumeAssistant.completedResults)
			var firstDenied *llm.ToolCall
			if len(missing) > 0 {
				first := missing[0]
				firstDenied = &first
			}
			for _, tc := range resumeAssistant.pendingMsg.ToolCalls {
				if msg, done := resumeAssistant.completedResults[tc.ID]; done {
					session = append(session, msg)
					continue
				}
				if firstDenied != nil && tc.ID == firstDenied.ID {
					// The asked-about call is the one whose refusal the user
					// sees: a refused card, announced like any settled call's.
					denial := llm.ToolResultMessage(tc.ID, llm.Text(tool.RejectedStopText))
					refused := tool.DeniedToolStep(toolStepID(tc), tc.Function.Name, toolInputMap(tc.Function.Arguments), resumeAssistant.denyFeedback)
					denial.ToolDisplay = toolDisplayState(refused)
					if step := tool.StepHookFromContext(ctx, w.state); step != nil {
						step(ctx, refused)
					}
					session = append(session, denial)
					continue
				}
				// The calls behind the gate never started: the same sentence
				// for the model, a hidden row for every surface, no step.
				session = append(session, toolResultMessage(rejectionStoppedResult(-1, tc)))
			}
			partialCapture.Set(session)
			transcript.flush(context.WithoutCancel(ctx), session, modelForTranscript(), nil)
			tool.EndApprovalContinuation(ctx)
			return nil, ErrTurnStoppedByRejection
		}
		if resumeAssistant.denied {
			// The user refused this one call. The refusal result lands on the
			// asked-about call alone — the first call still missing a result,
			// the same one an approval's replay spends itself on — while
			// already-completed results stay in context and the calls behind
			// the gate execute as the batch they were.
			missing := missingToolCalls(resumeAssistant.pendingMsg, resumeAssistant.completedResults)
			var fresh []orchestratedToolResult
			if len(missing) > 0 {
				denial := orchestratedToolResult{
					index: -1,
					call:  missing[0],
					parts: []llm.ContentPart{llm.Text(rejectionTextFor(ctx, missing[0], resumeAssistant.denyReason))},
				}
				if resumeAssistant.deliveredReview {
					// A delivered review closed this gate: the card's body is
					// the delivery drop key, never "(no output)". The key is
					// replay plumbing, not user-facing words — every
					// surface's replay projection recognizes it and drops
					// the row whole. No surface draws it, so no step
					// announces it either.
					denialMeta, _ := json.Marshal(map[string]string{
						"tool_name": strings.TrimSpace(missing[0].Function.Name),
						"status":    "denied",
					})
					denial.display = &llm.ToolDisplayState{
						Body:         tool.PlanReviewDeliveredDisplayKey,
						ToolMetaJSON: string(denialMeta),
					}
				} else {
					// The refusal has two halves: the instruction text above is
					// what the model reads, and the refused call's own step is what
					// the card says. The step is announced like any completed call's,
					// and the row it persists to carries the display derived from
					// that same step, so the card a live surface draws and the card
					// every replay of the row draws are one card. Without it a
					// replay printed the model-facing text as if it were the user's
					// words, and a live surface that only sees events drew nothing.
					denial = w.deniedToolResult(ctx, -1, missing[0], rejectionTextFor(ctx, missing[0], resumeAssistant.denyReason), resumeAssistant.denyFeedback)
				}
				fresh = append(fresh, denial)
			}
			var gate error
			denialReplayStopped := false
			if len(missing) > 1 {
				// The fence opens here, immediately before the first replayed
				// call, exactly as an approval's replay does. The replayed
				// calls carry no approved action: the user's answer refused
				// the asked-about call, and each call behind it meets its own
				// gate if it has one.
				if beginErr := tool.BeginApprovalContinuation(ctx); beginErr != nil {
					return nil, beginErr
				}
				toolCtx := withForkRuntimeSnapshot(ctx, session)
				replayResults, stoppedReplay := w.executeToolCalls(toolCtx, missing[1:], toolMap)
				if stoppedReplay {
					denialReplayStopped = true
				}
				for _, result := range replayResults {
					fresh = append(fresh, result)
					if isRequiresActionError(result.err) {
						if gate == nil {
							gate = result.err
						}
						continue
					}
					// A call the batch stopped before reaching has no result
					// to keep.
					if strings.TrimSpace(result.call.ID) == "" {
						continue
					}
					// Capture the replayed results so the resume caller can
					// persist them to the session store — the same reason an
					// approval's replay captures its own.
					if capture := tool.ReplayResultCaptureFromContext(ctx); capture != nil && result.err == nil {
						capture.Add(tool.ReplayResultEntry{
							ToolCallID:  result.call.ID,
							ToolName:    result.call.Function.Name,
							Content:     llm.TextContent(result.parts...),
							Timing:      result.timing,
							ToolDisplay: result.display,
						})
					}
				}
			}
			if denialReplayStopped {
				// A refusal without words stopped the replay itself: record
				// what ran and end the turn the way 129's refusal does.
				session = appendToolResultsInAssistantOrder(session, resumeAssistant.pendingMsg, resumeAssistant.completedResults, fresh)
				partialCapture.Set(session)
				transcript.flush(context.WithoutCancel(ctx), session, modelForTranscript(), nil)
				return nil, ErrTurnStoppedByRejection
			}
			if gate != nil {
				// A gate inside the refusal's replay is the same suspension
				// the first one was: hand the surface the snapshot with every
				// result this resume produced, the refusal's included.
				attachSessionSnapshotToRAE(gate, appendToolResultsInAssistantOrder(session, resumeAssistant.pendingMsg, resumeAssistant.completedResults, fresh))
				return nil, gate
			}
			// Completed, refused and replayed results land once each, in the
			// assistant's own call order, ahead of the model request that
			// continues the turn.
			session = appendToolResultsInAssistantOrder(session, resumeAssistant.pendingMsg, resumeAssistant.completedResults, fresh)
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
	// heldAnswer is the tool-call-free answer a steer delivery reopened the turn
	// after; it sits in the session right before those steers. If the user
	// retracts every one of them before the model answers, the turn ends on it,
	// exactly as it would have with no steer queued.
	var heldAnswer *llm.Result
	// endTurn closes the turn on a tool-call-free answer.
	endTurn := func(res *llm.Result, latestUsage *llm.Usage) (*llm.Result, error) {
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
	for {
		ctx = w.state.ContextWithRuntimeSessionMode(ctx)
		// Snapshot the session right before the LLM call: this is where a
		// cancellation occurs, and the snapshot must reflect everything
		// completed so far (including drain steers from the
		// prior iteration) so the dispatcher can persist it.
		partialCapture.Set(session)
		// The call runs under its own cancellation, so a retracted steer can
		// abandon it without cancelling the run.
		callCtx, abandonCall := context.WithCancelCause(ctx)
		if inFlightSteers != nil {
			provisionalFrom := sessionBeforeSteers
			if heldAnswer != nil {
				provisionalFrom--
			}
			callCtx = withProvisionalTail(callCtx, ctx, len(session), len(session)-provisionalFrom)
			inFlightSteers.bindAbort(func() { abandonCall(errSteerRetracted) })
		}
		callCtx = withSteerDeliveryResponseStart(callCtx, inFlightSteers)
		// Every call gets an abandon handle, not only the ones carrying a steer:
		// Esc has to be able to stop a call that is already streaming an answer
		// nobody asked to continue.
		if rt := TurnInputRuntimeFromContext(ctx); rt != nil {
			rt.bindCallAbort(func() { abandonCall(errInterruptToSend) })
		}
		// The failure decisions ride on what this one request had produced
		// when it died, and a finish is built from what it completed: both
		// are recorded per model call, so the loop resets them each time it
		// asks again.
		callCtx, progress := llm.WithStreamProgress(callCtx)
		callCtx, collector := withResponseCollector(callCtx)
		// A streaming response starts each tool call as soon as the model has
		// written it (OnToolCallReady): the queue is FIFO, a call starts when
		// nothing executes or it and everything executing is
		// concurrency-safe within the cap, and a call that cannot start
		// blocks the ones behind it. A call that would ask anyone defers to
		// the end of the stream, and so does everything queued behind it.
		var exec *streamingToolExecutor
		if sink := llm.StreamSinkFrom(callCtx); sink != nil {
			exec = &streamingToolExecutor{
				w:        w,
				toolMap:  toolMap,
				progress: progress,
				ctx:      ctx,
				limit:    tool.MaxToolConcurrencyFromEnv(),
				beforeStart: func(readyUpTo []llm.ToolCall) context.Context {
					if inFlightSteers != nil {
						inFlightSteers.Commit()
						if inFlightSteers.Retracted() {
							return nil
						}
					}
					prov := llm.AssistantMessage(collector.completedParts(), readyUpTo...)
					provSession := append(append([]llm.Message(nil), session...), prov)
					partialCapture.Set(provSession)
					transcript.flush(ctx, provSession, modelForTranscript(), nil)
					return withForkRuntimeSnapshot(w.state.ContextWithRuntimeSessionMode(ctx), provSession)
				},
				// A refusal without words on a streamed call cancels the model
				// call itself with the refusal as its cause: the stream stops
				// mid-word, the settled exchange is persisted, and the turn
				// ends on ErrTurnStoppedByRejection rather than a plain cancel.
				stopTurn: func() { abandonCall(ErrTurnStoppedByRejection) },
			}
			wrapped := *sink
			downstream := sink.OnToolCallReady
			wrapped.OnToolCallReady = func(call llm.ToolCall) {
				exec.add(call)
				if downstream != nil {
					downstream(call)
				}
			}
			callCtx = llm.WithStreamSink(callCtx, &wrapped)
		}
		transcript.beginAttempt()
		res, err := w.inner.Execute(callCtx, session, tools)
		// Read the cause before retiring the call context: a CancelCauseFunc
		// handed nil records plain context.Canceled, which would hide an
		// interrupt that already landed.
		interruptedToSend := errors.Is(context.Cause(callCtx), errInterruptToSend)
		abandonCall(nil)
		if rt := TurnInputRuntimeFromContext(ctx); rt != nil {
			rt.endCall()
		}
		if err == nil {
			// A successful non-streaming call has no response-start event, so commit
			// here. For streaming calls this is an idempotent no-op: the first event
			// committed before any assistant/reasoning output was forwarded.
			// Committing before the retraction check below is what makes the two
			// exclusive: a retraction that lands after this commit finds the
			// steers delivered and fails.
			inFlightSteers.Commit()
		}
		// A response the stream-retry layer finished from its completed part
		// is the answer that happened: the turn ends on it instead of asking
		// the model the same thing again. It is settled before the retraction
		// check because the model had begun answering — the delivery stands.
		var incomplete *llm.PartialResponseError
		if errors.As(err, &incomplete) {
			if incomplete.Complete {
				collector.sealCurrent()
			}
			res, err = collector.result(), nil
			inFlightSteers.Commit()
			if incomplete.Complete {
				incomplete = nil // a response that fully arrived is an ordinary one
			}
		}
		// Esc stopped this call to send what was waiting behind it. What the
		// model already said stays in the turn, the steers the abandoned call
		// was carrying go back to the queue so they ride the next call together
		// with everything queued since, and the loop asks again instead of
		// returning: the run does not end, so the surface keeps one clock and
		// shows no "Worked for". A call that answered before the abort landed
		// settles below like any other; only one that stopped short is here.
		if interruptedToSend && ctx.Err() == nil && (err != nil || res == nil || res.Message == nil) {
			// The answer text this call produced ends here, at the same boundary
			// a provider's own text-block end marks: the surface seals the live
			// block before the steer opens a user block, so the words it already
			// showed stay their own block instead of being replaced by the answer
			// the next call produces. The same call ends the collector's block,
			// which is what puts those words into the session below.
			if sink := llm.StreamSinkFrom(callCtx); sink != nil && sink.OnTextBlockDone != nil {
				sink.OnTextBlockDone()
			}
			accumulateUsage(&totalUsage, usageCopy(res))
			// Unwind the abandoned call's steers before appending anything: the
			// tail this truncation drops is exactly those user messages, and the
			// partial answer belongs after it.
			carried := 0
			if inFlightSteers != nil {
				carried = len(session) - sessionBeforeSteers
			}
			if inFlightSteers != nil && inFlightSteers.Rollback() {
				session = session[:sessionBeforeSteers]
			}
			// A mid-turn compaction that finished inside the abandoned call
			// published its checkpoint, and the checkpoint is the base the rest
			// of this branch builds on: taking it at the next successful call
			// instead would replace the answer this call already showed and the
			// steers appended below it. It stops before the provisional tail, so
			// the tail is trimmed off it the same way.
			if replaced, ok := adoptionSink.take(); ok && len(replaced) >= carried {
				session = replaced[:len(replaced)-carried]
			}
			// Reminders injected into the abandoned request belong to that
			// request alone: the next call injects them again, and adopting them
			// here would apply positions computed for a slice that is gone.
			reminderSink.discard()
			inFlightSteers = nil
			heldAnswer = nil
			answer := collector.result().Message
			if exec != nil && exec.dispatched() {
				// Calls the stream already started settle here: the ones that
				// finished keep their results, the rest read as interrupted. The
				// run's own ctx is alive, so this is a settlement, not a
				// cancellation, and the turn goes on after it.
				settled := exec.abort(ctx)
				if answer != nil && (len(answer.ToolCalls) > 0 || strings.TrimSpace(answer.TextContent()) != "") {
					session = append(session, *answer)
					noteResponseCompleted()
				}
				for _, result := range settled {
					session = append(session, toolResultMessage(result))
				}
			} else if answer != nil && strings.TrimSpace(answer.TextContent()) != "" {
				// Nothing of this call is worth a session entry when the model had
				// not written a word yet: an empty assistant message would be a
				// message the next request has to carry and answer.
				session = append(session, *answer)
				noteResponseCompleted()
			}
			partialCapture.Set(session)
			transcript.flush(context.WithoutCancel(ctx), session, modelForTranscript(), nil)
			if delivery := TurnInputRuntimeFromContext(ctx).BeginSteerDelivery(ctx); delivery != nil {
				sessionBeforeSteers = len(session)
				session = append(session, w.steerMessages(ctx, delivery.Entries())...)
				inFlightSteers = delivery
				partialCapture.Set(session)
			}
			continue
		}
		// A cancelled request whose tools already started settles them here:
		// the ones that finished keep their results, the rest are interrupted,
		// and the provisional assistant message with those results reaches the
		// transcript before the cancellation returns. The write must survive
		// the cancellation it is racing, so it runs on an uncancellable
		// context — the same rule the reasoning recorder follows.
		//
		// A refusal without words reaches this the same way — its stopTurn
		// cancels the model call with the refusal as the cause, and the stream
		// read can surface that cause itself rather than context.Canceled — so
		// the refusal is matched on the executor's own marker, not the error's
		// shape.
		refused := exec != nil && exec.dispatched() && exec.refusedStop()
		if exec != nil && exec.dispatched() && (ctx.Err() != nil || errors.Is(err, context.Canceled) || refused) {
			if err == nil {
				err = ctx.Err()
			}
			results := exec.abort(ctx)
			// abort joined every call that was still in flight, so a refusal
			// that landed while it waited is recorded now — and this is the
			// only place left that can report it. Read it again: the value
			// above is what decided to enter this branch, not what happened
			// inside it.
			refused = exec != nil && exec.dispatched() && exec.refusedStop()
			prov := collector.result().Message
			session = append(session, *prov)
			noteResponseCompleted()
			for _, result := range results {
				session = append(session, toolResultMessage(result))
			}
			partialCapture.Set(session)
			transcript.flush(context.WithoutCancel(ctx), session, modelForTranscript(), nil)
			if refused {
				// A streamed call the user refused without words: the stream was
				// cancelled with the refusal as its cause, the queued calls read
				// the not-run sentence, and the turn ends the way 129's refusal
				// does — not as an ordinary cancellation. refused was re-read
				// after abort joined the in-flight calls: the refusal can land
				// inside that very wait.
				return nil, ErrTurnStoppedByRejection
			}
			return nil, err
		}
		if inFlightSteers.Retracted() {
			if ctx.Err() == nil {
				// The user took a steer back out of this call before the model
				// began answering it. Whatever the call returned — the abort's
				// cancellation, or an answer written with the retracted message in
				// view — is discarded; only the tokens it spent are counted. The
				// steers still riding on it go back to the queue and are taken
				// again for a fresh call, together with any queued since.
				accumulateUsage(&totalUsage, usageCopy(res))
				steerCount := len(session) - sessionBeforeSteers
				inFlightSteers.Rollback()
				inFlightSteers = nil
				session = session[:sessionBeforeSteers]
				if replaced, ok := adoptionSink.take(); ok && len(replaced) >= steerCount {
					// A mid-turn compaction finished inside the abandoned call. Its
					// checkpoint stops before the steers (see provisionalTail), so
					// the replacement ends with exactly the steer messages: keep the
					// checkpoint, drop them.
					session = replaced[:len(replaced)-steerCount]
				}
				// Reminders injected into the abandoned request were never part of
				// an exchange; the next call injects them again.
				reminderSink.discard()
				if delivery := TurnInputRuntimeFromContext(ctx).BeginSteerDelivery(ctx); delivery != nil {
					inFlightSteers = delivery
					sessionBeforeSteers = len(session)
					session = append(session, w.steerMessages(ctx, delivery.Entries())...)
					continue
				}
				if heldAnswer != nil {
					// Every steer that reopened the turn is gone: end it on the
					// answer it had already reached, whose usage was counted when
					// it arrived.
					answer := heldAnswer
					heldAnswer = nil
					session = session[:len(session)-1]
					partialCapture.Set(session)
					return endTurn(answer, usageCopy(answer))
				}
				continue
			}
			// The run itself is being cancelled too: settle this call the way
			// any failed call is settled below.
			if err == nil {
				res, err = nil, ctx.Err()
			}
		}
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
		inFlightSteers = nil
		heldAnswer = nil
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
		// A response can report fewer calls than its stream announced — a compat
		// adapter synthesizes them from its final frame. The calls that already
		// ran are still this response's own: append the ones it left out so each
		// is answered. Self-healing, so it is a warning, never an error.
		if exec != nil && exec.dispatched() && res != nil && res.Message != nil {
			slog.Warn("tools executed in-stream but the response reports no tool calls")
			res.Message.ToolCalls = exec.reconcile(res.Message.ToolCalls)
		}
		if res == nil || res.Message == nil || len(res.Message.ToolCalls) == 0 {
			if incomplete != nil {
				// The turn ends on the answer that happened. The completed
				// text is kept in the conversation; no further model call, no
				// steer drain, no end-of-turn hooks — the sentence (or the
				// error itself) is what the user reads.
				shown := incomplete.Progress.Kind == llm.ProgressOutput ||
					incomplete.Progress.Kind == llm.ProgressPartialOutput ||
					incomplete.Progress.ToolDispatched
				if strings.TrimSpace(res.Message.TextContent()) != "" {
					session = append(session, *res.Message)
					noteResponseCompleted()
					partialCapture.Set(session)
					transcript.flush(ctx, session, modelForTranscript(), latestUsage)
				}
				if incomplete.Failed {
					return nil, incomplete.Err
				}
				return nil, &responseIncompleteError{sentence: llm.IncompleteSentence(incomplete.Cause, shown), err: incomplete.Err}
			}
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
					heldAnswer = res
					sessionBeforeSteers = len(session)
					session = append(session, w.steerMessages(ctx, delivery.Entries())...)
					inFlightSteers = delivery
					partialCapture.Set(session)
					continue
				}
			}
			if res != nil && res.Message != nil {
				// The answer this turn ends on is a block too: write it now,
				// without disturbing the session endTurn is about to close
				// over.
				transcript.flush(ctx, append(append([]llm.Message(nil), session...), *res.Message), modelForTranscript(), latestUsage)
			}
			return endTurn(res, latestUsage)
		}
		if incomplete != nil {
			// The completed tool calls run as usual and the loop asks the
			// model again — but the conversation first records why this
			// response stops where it does.
			shown := incomplete.Progress.Kind == llm.ProgressOutput ||
				incomplete.Progress.Kind == llm.ProgressPartialOutput ||
				incomplete.Progress.ToolDispatched
			text := llm.IncompleteSentence(incomplete.Cause, shown)
			if incomplete.Failed {
				text = incomplete.Err.Error()
			}
			if sink := llm.StreamSinkFrom(ctx); sink != nil && sink.OnResponseNotice != nil {
				sink.OnResponseNotice(llm.ResponseNotice{Kind: llm.ResponseNoticeIncomplete, Text: text})
			}
		}
		// A dispatched executor holds still here — reconcile freezes it — so
		// its beforeStart will never again read the session this append
		// mutates, and the response owns every call it already ran.
		if exec != nil && exec.dispatched() {
			res.Message.ToolCalls = exec.reconcile(res.Message.ToolCalls)
		}
		session = append(session, *res.Message)
		noteResponseCompleted()
		partialCapture.Set(session)
		// The answer's text block ended as the turn turned to its tool calls:
		// write it now, so the words survive a process that stops mid-turn.
		transcript.flush(ctx, session, modelForTranscript(), latestUsage)
		toolCtx := withForkRuntimeSnapshot(w.state.ContextWithRuntimeSessionMode(ctx), session)
		var results []orchestratedToolResult
		stopped := false
		if exec != nil && exec.dispatched() {
			// Calls that already ran keep their results; the deferred and the
			// never-ready ones run now, approvals included.
			results, stopped = exec.finish(toolCtx, res.Message.ToolCalls)
		} else {
			results, stopped = w.executeToolCalls(toolCtx, res.Message.ToolCalls, toolMap)
		}
		if stopped {
			// The user refused a call without words, answered where it asked:
			// the refusal is on that call, the calls behind it read the same
			// sentence under a hidden row, and the turn ends without another
			// model request — the surface finishes it like a cancel (129).
			for _, result := range results {
				session = append(session, toolResultMessage(result))
			}
			partialCapture.Set(session)
			transcript.flush(context.WithoutCancel(ctx), session, modelForTranscript(), latestUsage)
			return nil, ErrTurnStoppedByRejection
		}
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
		// The tool-result batch ended a block: write the exchange before any
		// steer rides on the next call (a steer is provisional until the model
		// answers it, so it is never part of a flush).
		transcript.flush(ctx, session, modelForTranscript(), latestUsage)
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
			session = append(session, w.steerMessages(ctx, delivery.Entries())...)
			partialCapture.Set(session)
		}
	}
}

// errSteerRetracted is the cause a model call is abandoned with when the user
// takes back a queued steer it was carrying before the model began answering.
var errSteerRetracted = errors.New("queued steer retracted before the model answered it")

// errInterruptToSend is the cause a model call is abandoned with when the user
// pressed Esc with steers waiting: the call stops where it is and the same turn
// asks again carrying them. It is not a cancellation — nothing the turn
// produced is thrown away and no turn boundary is reached.
var errInterruptToSend = errors.New("interrupt to send the queued steers")

// withSteerDeliveryResponseStart binds a provisional delivery to the provider's
// response boundary. The provider fires OnResponseStarted before forwarding its
// first output-bearing event, so the surface enqueues the user message before
// any reasoning or assistant output caused by that message. This is also the
// correct transaction boundary: a later stream failure cannot make the
// already-observed model output "undelivered" and put the input back into the
// queue.
//
// If the user retracted a steer from the delivery before that boundary, the
// call is being abandoned: the commit is refused, and nothing the response
// produces — the acknowledgement included — reaches the surface.
func withSteerDeliveryResponseStart(ctx context.Context, delivery *SteerDelivery) context.Context {
	if delivery == nil {
		return ctx
	}
	sink := llm.StreamSinkFrom(ctx)
	if sink == nil {
		return ctx
	}
	wrapped := *sink
	var abandoned atomic.Bool
	downstream := wrapped.OnResponseStarted
	wrapped.OnResponseStarted = func() {
		delivery.Commit()
		if delivery.Retracted() {
			abandoned.Store(true)
			return
		}
		if downstream != nil {
			downstream()
		}
	}
	if onDelta := wrapped.OnDelta; onDelta != nil {
		wrapped.OnDelta = func(text string) {
			if !abandoned.Load() {
				onDelta(text)
			}
		}
	}
	if onReasoning := wrapped.OnReasoningDelta; onReasoning != nil {
		wrapped.OnReasoningDelta = func(text string) {
			if !abandoned.Load() {
				onReasoning(text)
			}
		}
	}
	if onReasoningDone := wrapped.OnReasoningDone; onReasoningDone != nil {
		wrapped.OnReasoningDone = func() {
			if !abandoned.Load() {
				onReasoningDone()
			}
		}
	}
	if onWebSearch := wrapped.OnWebSearch; onWebSearch != nil {
		wrapped.OnWebSearch = func(id, detail string, completed bool) {
			if !abandoned.Load() {
				onWebSearch(id, detail, completed)
			}
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
		if result.err == nil && !result.notRun && strings.TrimSpace(result.call.Function.Name) == toolName {
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
	// stopped marks a refusal without words: the refusal is recorded on the
	// parked call and the turn ends there (ErrTurnStoppedByRejection), with
	// no model request behind it.
	stopped bool
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
	stopped := state.Stopped
	state.Session = nil
	return resumeSnapshot{session: session, pendingMsg: pending, completedResults: completed, denied: denied, denyReason: denyReason, denyFeedback: denyFeedback, deliveredReview: deliveredReview, stopped: stopped}, true
}

// ErrTurnStoppedByRejection is the resume outcome of a refusal without words:
// the refusal is recorded on the parked call and the turn ends there, waiting
// for the user to speak. Surfaces treat it as a user cancel — no error card,
// the run ends cancelled.
var ErrTurnStoppedByRejection = errors.New("the user refused a tool call and stopped the turn")

// rejectionTextFor is the model-facing text of one refused call. An
// exit_plan_mode refusal keeps its composed guidance verbatim — the plan
// direction the resume composed, not a canned sentence — while every other
// refusal reads the sentence Claude Code writes, primary and subagent runs
// apart. A run is a subagent's when the context carries its hook agent ID,
// the same test the stop hooks use.
func rejectionTextFor(ctx context.Context, call llm.ToolCall, denyReason string) string {
	if strings.EqualFold(strings.TrimSpace(call.Function.Name), "exit_plan_mode") {
		if reason := strings.TrimSpace(denyReason); reason != "" {
			return reason
		}
	}
	return tool.RejectionText(strings.TrimSpace(tool.HookAgentIDFromContext(ctx)) != "", denyReason)
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
	approvedActionID := strings.TrimSpace(tool.ApprovedActionIDFromContext(toolCtx))
	toolCtx = tool.WithApprovedActionID(toolCtx, "")
	// The approval belongs to the first pending call alone: every call ahead
	// of the gate had finished when the turn stopped on it. The calls behind
	// it never started, so they run as the batch they were — partitioned like
	// any other — and the subagents dispatched behind an approved command
	// still start together.
	firstCtx := toolCtx
	if approvedActionID != "" {
		firstCtx = tool.WithApprovedActionID(toolCtx, approvedActionID)
	}
	results := []orchestratedToolResult{w.executeOneToolCall(firstCtx, 0, missing[0], toolMap, toolUISuppression{})}
	replayStopped := false
	if len(missing) > 1 && !isRequiresActionError(results[0].err) {
		more, stopped := w.executeToolCalls(toolCtx, missing[1:], toolMap)
		results = append(results, more...)
		replayStopped = stopped
	}
	if replayStopped {
		// A refusal without words stopped the replay: record what ran — the
		// not-run sentence for everything behind the refusal — and end the
		// turn the way 129's refusal does.
		*session = appendToolResultsInAssistantOrder(*session, assistant, completed, results)
		return ErrTurnStoppedByRejection
	}
	var gate error
	for _, result := range results {
		if isRequiresActionError(result.err) {
			if gate == nil {
				gate = result.err
			}
			continue
		}
		// A call the batch stopped before reaching has no result to keep.
		if strings.TrimSpace(result.call.ID) == "" {
			continue
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
	}
	if gate != nil {
		// A second gate inside a replay is the same suspension the first
		// one was, and it is raised on the same action queue whether the
		// run is the primary agent's or a child's. Rewriting a child's
		// gate into a tool error here told the worker its request had been
		// refused for lack of an approval path, which stopped being true
		// once children began suspending through that queue — and it fires
		// exactly where it does most damage, on the replay that a granted
		// approval just started.
		attachSessionSnapshotToRAE(gate, appendToolResultsInAssistantOrder(*session, assistant, completed, results))
		return gate
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

// toolBatchState is one response's result slots. A concurrent segment's
// goroutines write their own slot while another goroutine's in-place gate
// builds its crash-recovery snapshot over the slots that already hold a
// result, so both go through the lock.
type toolBatchState struct {
	mu      sync.Mutex
	results []orchestratedToolResult
}

func (b *toolBatchState) set(i int, r orchestratedToolResult) {
	b.mu.Lock()
	b.results[i] = r
	b.mu.Unlock()
}

// snapshotMessages appends every result the batch has produced so far, in the
// assistant's own call order, to base. An entry still waiting to be written
// has no call id, which is what "not yet a result" has always looked like.
func (b *toolBatchState) snapshotMessages(base []llm.Message) []llm.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := append([]llm.Message(nil), base...)
	for _, result := range b.results {
		if strings.TrimSpace(result.call.ID) == "" {
			continue
		}
		if isRequiresActionError(result.err) {
			continue
		}
		out = append(out, toolResultMessage(result))
	}
	return out
}

// executeToolCalls runs one response's tool calls. Its second return reports
// that the user refused a call without words and stopped the turn: every call
// the batch had not started then carries the refusal's not-run result, and the
// caller must persist the batch and end the turn with
// ErrTurnStoppedByRejection instead of asking the model again.
func (w *toolOrchestrationLLM) executeToolCalls(
	ctx context.Context,
	calls []llm.ToolCall,
	toolMap map[string]*llm.Tool,
) ([]orchestratedToolResult, bool) {
	batch := &toolBatchState{results: make([]orchestratedToolResult, len(calls))}
	results := batch.results
	assigned := make([]bool, len(calls))
	// A queued steer takes the turn at the next call boundary: whatever has
	// not started yet is answered "not run" so the steer goes out now instead
	// of after the whole batch.
	preempted := false
	steerWaiting := func() bool {
		if !preempted && ctx.Err() == nil && TurnInputRuntimeFromContext(ctx).HasSteers() {
			preempted = true
		}
		return preempted
	}
	// The crash-recovery snapshot of an in-place gate: the session the batch
	// runs in, plus every result this response has produced so far.
	snapshot := func() []llm.Message {
		base, _ := forkRuntimeSnapshotFromContext(ctx)
		return batch.snapshotMessages(base)
	}
	stopped := false
	plans := w.state.PartitionToolCallsFor(calls)
	callIndex := 0
	for _, plan := range plans {
		if stopped {
			break
		}
		batchCalls := calls[callIndex : callIndex+len(plan.ToolNames)]
		for _, seg := range w.segmentToolCallPlan(ctx, plan, batchCalls, callIndex) {
			if seg.concurrent {
				if steerWaiting() {
					for i, call := range seg.calls {
						batch.set(seg.offset+i, steerPreemptedResult(seg.offset+i, call))
						assigned[seg.offset+i] = true
					}
					continue
				}
				segResults := results[seg.offset : seg.offset+len(seg.calls)]
				groups := parallelToolCallSummaryGroups(seg.calls, toolMap)
				w.emitParallelBatchSummary(ctx, groups, tool.StepKindToolParallelStarted, nil)
				for i := range seg.calls {
					assigned[seg.offset+i] = true
				}
				if w.executeConcurrentBatch(ctx, seg.calls, seg.offset, toolMap, batch, snapshot) {
					stopped = true
				}
				w.emitParallelBatchSummary(ctx, groups, tool.StepKindToolParallelCompleted, segResults)
				if w.settleConcurrentGates(segResults) {
					return results, stopped
				}
				continue
			}
			for i, call := range seg.calls {
				if stopped {
					break
				}
				if steerWaiting() {
					batch.set(seg.offset+i, steerPreemptedResult(seg.offset+i, call))
					assigned[seg.offset+i] = true
					continue
				}
				res, stop := w.runCallInPlace(ctx, seg.offset+i, call, toolMap, toolUISuppression{}, snapshot)
				batch.set(seg.offset+i, res)
				assigned[seg.offset+i] = true
				stopped = stop
				if !stop && isRequiresActionError(res.err) {
					return results, false
				}
			}
		}
		callIndex += len(plan.ToolNames)
	}
	if stopped {
		// Everything the batch never started reads the refusal's own sentence
		// under a hidden row: the refused call's card already says what
		// happened, and no step announces a call that never ran.
		for i := range calls {
			if assigned[i] {
				continue
			}
			batch.set(i, rejectionStoppedResult(i, calls[i]))
		}
	}
	return results, stopped
}

// toolCallSegment is a run of one plan's calls that execute together: a
// concurrent segment starts every call at once, any other runs them one at a
// time.
type toolCallSegment struct {
	calls      []llm.ToolCall
	offset     int
	concurrent bool
}

// segmentToolCallPlan splits one partition plan into the segments it executes
// as.
//
// With an approval path, a call that can stop the turn on its own approval
// gate runs alone: the run has one wait row, and a resume hands its approval
// to the first call it replays, so two such gates raised side by side would
// leave the run waiting on one action and spend the approval on another call.
// A surface that answers the primary agent's gates in place lifts that
// constraint: the gates no longer stop the turn, and the run's approval slot
// is what keeps the group asking one question at a time, so the whole
// concurrency-safe group runs as one segment again. A subagent dispatch does
// not stop the turn that way while the surface answers its children's
// approvals in place either, so consecutive dispatches still start together —
// they are the calls that take minutes, and running them one after another is
// what made the model's parallel dispatch serial.
func (w *toolOrchestrationLLM) segmentToolCallPlan(ctx context.Context, plan tool.ToolCallPlan, calls []llm.ToolCall, offset int) []toolCallSegment {
	if !plan.ConcurrencySafe {
		return []toolCallSegment{{calls: calls, offset: offset}}
	}
	if tool.InlineApprovalHookFromContext(ctx, w.state) != nil {
		return []toolCallSegment{{calls: calls, offset: offset, concurrent: true}}
	}
	if w.state.ActionHook() == nil {
		return []toolCallSegment{{calls: calls, offset: offset, concurrent: true}}
	}
	inPlace := tool.SubagentApprovalHookFromContext(ctx, w.state) != nil
	out := make([]toolCallSegment, 0, len(calls))
	for i, call := range calls {
		concurrent := inPlace && subagentDispatchTool(call.Function.Name)
		if last := len(out) - 1; concurrent && last >= 0 && out[last].concurrent {
			out[last].calls = append(out[last].calls, call)
			continue
		}
		out = append(out, toolCallSegment{calls: []llm.ToolCall{call}, offset: offset + i, concurrent: concurrent})
	}
	// A dispatch with no other dispatch beside it has nothing to overlap
	// with, and runs the way it always has.
	for i := range out {
		if len(out[i].calls) == 1 {
			out[i].concurrent = false
		}
	}
	return out
}

// settleConcurrentGates reports whether a concurrent segment raised an
// approval gate. The first gate in call order is the one the turn stops on:
// every call ahead of it finished, so it is the first call a resume replays
// and the one its approval reaches. Any later gate is withdrawn, the way
// subagent_fanout withdraws the gates of its other children; its call is
// replayed after the approved one and asks again then.
func (w *toolOrchestrationLLM) settleConcurrentGates(results []orchestratedToolResult) bool {
	gated := false
	for _, result := range results {
		if !isRequiresActionError(result.err) {
			continue
		}
		if gated && w.withdrawGate != nil {
			w.withdrawGate(result.err)
		}
		gated = true
	}
	return gated
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
		// A subagent call draws its own card — its agents, their progress,
		// their results — from its own lifecycle events. Folding two of them
		// into a summary row would suppress exactly those events.
		if name == "" || subagentToolName(name) {
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

// executeConcurrentBatch runs one concurrent segment and reports whether the
// user refused a call without words (stopped): the segment's other calls are
// then cancelled — the started ones interrupted, the never-started answered
// with the refusal's not-run result — and the caller stops the turn.
func (w *toolOrchestrationLLM) executeConcurrentBatch(
	ctx context.Context,
	batch []llm.ToolCall,
	offset int,
	toolMap map[string]*llm.Tool,
	state *toolBatchState,
	snapshot func() []llm.Message,
) bool {
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
	segCtx, cancelSeg := context.WithCancelCause(ctx)
	defer cancelSeg(nil)
	var stopOnce sync.Once
	stopped := false
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
			case <-segCtx.Done():
				if errors.Is(context.Cause(segCtx), ErrTurnStoppedByRejection) {
					// Never started, and the turn is being refused: the same
					// sentence as the refused call, under the hidden row.
					state.set(index, rejectionStoppedResult(index, tc))
				} else {
					state.set(index, orchestratedToolResult{index: index, call: tc, err: segCtx.Err()})
				}
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			res, stop := w.runCallInPlace(segCtx, index, tc, toolMap, suppress, snapshot)
			state.set(index, res)
			if stop {
				stopOnce.Do(func() {
					stopped = true
					// The refusal stops the turn: the siblings still executing
					// are interrupted with the refusal as their cause.
					cancelSeg(ErrTurnStoppedByRejection)
				})
			}
		}(idx, call, suppress)
	}
	wg.Wait()
	return stopped
}

type toolUISuppression struct {
	started   bool
	completed bool
}

// deniedToolResult is the refusal result of one call: the instruction text the
// model reads — the caller shapes it with rejectionTextFor, or writes
// RejectedStopText for a refusal without words — and the refused call's own
// step, announced like any settled call's so the card a live surface draws and
// the card every replay of the row draws are one card. The resume path and an
// in-place refusal produce it from this same code, so both roads out of a
// refusal leave the same result and the same card.
func (w *toolOrchestrationLLM) deniedToolResult(ctx context.Context, index int, call llm.ToolCall, text, feedback string) orchestratedToolResult {
	refused := tool.DeniedToolStep(toolStepID(call), call.Function.Name, toolInputMap(call.Function.Arguments), feedback)
	if step := tool.StepHookFromContext(ctx, w.state); step != nil {
		step(ctx, refused)
	}
	return orchestratedToolResult{
		index:   index,
		call:    call,
		parts:   []llm.ContentPart{llm.Text(text)},
		display: toolDisplayState(refused),
	}
}

// runCallInPlace executes one call; when the call raises its own approval gate
// and the surface can answer it in place, the gate is answered here and the
// call re-runs approved, or is refused, without ending the turn. snapshot
// returns the session a crash-recovery resume would replay: the history, this
// response's assistant message and every result this response has so far.
func (w *toolOrchestrationLLM) runCallInPlace(
	ctx context.Context, index int, call llm.ToolCall, toolMap map[string]*llm.Tool,
	suppress toolUISuppression, snapshot func() []llm.Message,
) (orchestratedToolResult, bool) {
	hook := tool.InlineApprovalHookFromContext(ctx, w.state)
	if hook == nil {
		return w.executeOneToolCall(ctx, index, call, toolMap, suppress), false
	}
	holder := &tool.ApprovalSlotHolder{RunID: tool.RunIDFromContext(ctx)}
	defer holder.Release()
	callCtx := tool.WithApprovalSlotHolder(ctx, holder)
	var prevResume context.Context
	defer func() {
		if prevResume != nil {
			tool.EndApprovalContinuation(prevResume)
		}
	}()
	// Five gates one call cannot pass is a surface answering nothing; failing
	// the call beats looping the user through the same question forever.
	for round := 0; round < 5; round++ {
		res := w.executeOneToolCall(callCtx, index, call, toolMap, suppress)
		if prevResume != nil {
			// The approved replay settled above, whatever its result: the
			// fence covered exactly that replay.
			tool.EndApprovalContinuation(prevResume)
			prevResume = nil
		}
		var rae *tool.RequiresActionError
		if !errors.As(res.err, &rae) || rae == nil {
			return res, false
		}
		// A subagent's gate still unwinds — its own in-place answering lives
		// around the subagent runner — and so does exit_plan_mode, whose
		// approval carries options only that path can honor.
		if rae.AgentID != "" || strings.EqualFold(strings.TrimSpace(rae.ActionKind), "exit_plan_mode") {
			return res, false
		}
		// Complete the gate so the surface can find the run, the call, and a
		// snapshot a crash recovery would replay.
		if strings.TrimSpace(rae.RunID) == "" {
			rae.RunID = holder.RunID
		}
		if strings.TrimSpace(rae.OriginatingToolCallID) == "" {
			rae.OriginatingToolCallID = toolStepID(call)
		}
		rae.SessionSnapshot = snapshot()
		resumeCtx, err := hook(ctx, rae)
		switch {
		case errors.Is(err, rae):
			// The surface handed the gate back: the turn unwinds on it.
			return res, false
		case errors.Is(err, tool.ErrApprovalRejectedStop):
			// Refused without words (plan 129's semantics, answered here):
			// this call reads the stop sentence, the calls behind it never
			// start, and the turn ends without another model request.
			return w.deniedToolResult(ctx, index, call, tool.RejectedStopText, ""), true
		case err != nil:
			if ctx.Err() != nil {
				return orchestratedToolResult{index: index, call: call, err: ctx.Err()}, false
			}
			return orchestratedToolResult{index: index, call: call, err: err}, false
		}
		if st := tool.ToolApprovalResumeFromContext(resumeCtx); st != nil && st.Denied {
			// Refused with words: only this call is refused, the turn runs on.
			tool.EndApprovalContinuation(resumeCtx)
			return w.deniedToolResult(ctx, index, call, rejectionTextFor(ctx, call, st.DenyReason), st.DenyFeedback), false
		}
		// Approved: the call re-runs as the approved action, in place, under
		// the fence that says the replay is the one execution. resumeCtx
		// itself is never the execution context — it carries the resume
		// snapshot, which nested orchestration would mistake for a session.
		if err := tool.BeginApprovalContinuation(resumeCtx); err != nil {
			return orchestratedToolResult{index: index, call: call, err: err}, false
		}
		prevResume = resumeCtx
		callCtx = tool.WithApprovedActionID(callCtx, rae.ActionID)
	}
	return orchestratedToolResult{index: index, call: call, err: fmt.Errorf("tool %s raised too many approval gates in one call", strings.TrimSpace(call.Function.Name))}, false
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
	if errors.Is(err, tool.ErrDeferToStreamEnd) {
		// Not a completion: the call re-runs after the stream. Its started
		// card stays up and the re-run updates it in place (same step id).
		return orchestratedToolResult{index: index, call: call, deferred: true}
	}
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

// steerPreemptedResultText is what the model reads for a call skipped because
// the user's queued message went out first.
const steerPreemptedResultText = "Not run: the user sent a new message before this tool call started. Read the message and call the tool again if it is still needed."

// steerPreemptedResult is the result of a call the turn never started. Its
// display carries the drop key, so the persisted row draws nothing on any
// surface: the call never ran, and the user never saw it.
func steerPreemptedResult(index int, call llm.ToolCall) orchestratedToolResult {
	metaJSON, _ := json.Marshal(map[string]string{"tool_name": strings.TrimSpace(call.Function.Name), "status": "not_run"})
	return orchestratedToolResult{
		index:   index,
		call:    call,
		parts:   []llm.ContentPart{llm.Text(steerPreemptedResultText)},
		display: &llm.ToolDisplayState{Body: tool.SteerPreemptedDisplayKey, ToolMetaJSON: string(metaJSON)},
		notRun:  true,
	}
}

// rejectionStoppedResult is the result of a call the run never started
// because a refusal without words stopped the turn: the same sentence the
// refused call itself received, under the hidden display key no surface
// renders and with no step announced — the refused call's own card already
// says what happened.
func rejectionStoppedResult(index int, call llm.ToolCall) orchestratedToolResult {
	metaJSON, _ := json.Marshal(map[string]string{"tool_name": strings.TrimSpace(call.Function.Name), "status": "not_run"})
	return orchestratedToolResult{
		index:   index,
		call:    call,
		parts:   []llm.ContentPart{llm.Text(tool.RejectedStopText)},
		display: &llm.ToolDisplayState{Body: tool.RejectionStoppedDisplayKey, ToolMetaJSON: string(metaJSON)},
		notRun:  true,
	}
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
