// Turn supervision on the chat session: approvals, resume, and auto-compaction.
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

type chatApprovalResume struct {
	ActionID string
	RunID    string
	// SubagentRunID is the run ID of the subagent whose own tool call raised
	// the approval gate, when the gate originated inside one; empty when the
	// gate was raised by the top-level agent's own tool call, in which case
	// it would just duplicate RunID. resumeAgentContext uses it to look up
	// the subagent's live registry entry and continue under its own
	// WorkerSessionID instead of the top-level chat session.
	SubagentRunID   string
	SessionID       string
	Channel         string
	Input           string
	ToolStepID      string
	ToolName        string
	AgentID         string
	SubagentType    string
	SessionSnapshot []llm.Message
	TurnStartedAt   time.Time
	// ClearedContext is set when the user approved exit_plan_mode option 1
	// ("clear context and proceed"). On resume the pre-clear snapshot
	// must be trimmed to a minimal anchor so the old history is not replayed
	// back into the model — mirroring the store-side context reset.
	ClearedContext bool
	// OwnsDurableFence is true when this process holds the wait row's resume
	// lease, whether it claimed one from a persisted wait or fenced the wait
	// backing its own in-memory pending record. If the in-memory controller
	// claim then fails, that lease is relinquished so a later recovery pass —
	// or the user's next attempt — is not stranded behind this owner for the
	// rest of the lease window.
	OwnsDurableFence bool
}

// pendingApprovalToolCall and pendingApprovalToolStepID moved to
// pkg/turn.PendingApprovalToolCall / pkg/turn.PendingApprovalToolStepID: the
// wait row's session snapshot is the durable source for the parked call, and
// every surface holding a gate resolves it the same way. No local copy is kept
// here so the two cannot drift.

func (s *ChatSession) setPendingApproval(p *chatApprovalResume) {
	if s == nil {
		return
	}
	s.approvalMu.Lock()
	defer s.approvalMu.Unlock()
	s.approvalPending = p
}

// markPendingApprovalClearedContext flags the pending approval (matched by
// actionID) so its resume trims the pre-clear snapshot. Called when the user
// picks exit_plan_mode option 1 (clear context) before resumeAfterApproval
// consumes the pending state.
func (s *ChatSession) markPendingApprovalClearedContext(actionID string) {
	if s == nil {
		return
	}
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return
	}
	s.approvalMu.Lock()
	defer s.approvalMu.Unlock()
	if s.approvalPending != nil && s.approvalPending.ActionID == actionID {
		s.approvalPending.ClearedContext = true
	}
}

func (s *ChatSession) clearPendingApproval() {
	if s == nil {
		return
	}
	s.approvalMu.Lock()
	defer s.approvalMu.Unlock()
	s.approvalPending = nil
}

func (s *ChatSession) takePendingApprovalIfMatch(actionID string) *chatApprovalResume {
	if s == nil {
		return nil
	}
	s.approvalMu.Lock()
	defer s.approvalMu.Unlock()
	if s.approvalPending == nil || s.approvalPending.ActionID != actionID {
		return nil
	}
	cp := *s.approvalPending
	s.approvalPending = nil
	return &cp
}

func (s *ChatSession) takePendingApprovalForAction(actionID string) *chatApprovalResume {
	if s == nil {
		return nil
	}
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return nil
	}
	if p := s.takePendingApprovalIfMatch(actionID); p != nil {
		if runs := s.runSvc(); runs != nil {
			// The exact in-memory continuation predates any later durable wait for
			// the same run. Fence it only when its own wait row still exists; a
			// replacement wait must not make us discard the captured snapshot.
			wait, waitErr := runs.GetWaitForRun(context.Background(), p.RunID)
			if waitErr != nil {
				s.approvalMu.Lock()
				if s.approvalPending == nil {
					s.approvalPending = p
				}
				s.approvalMu.Unlock()
				return nil
			}
			if wait != nil && strings.TrimSpace(wait.ActionID) == p.ActionID {
				if err := runs.MarkWaitResumeOwner(context.Background(), p.RunID, p.ActionID, s.approvalResumeOwner()); err != nil {
					// The durable fence rejected this process. Put the in-memory
					// resume record back so a transient store failure does not turn
					// an otherwise recoverable approval into a lost continuation.
					s.approvalMu.Lock()
					if s.approvalPending == nil {
						s.approvalPending = p
					}
					s.approvalMu.Unlock()
					return nil
				}
				p.OwnsDurableFence = true
			}
		}
		return p
	}
	if s.runSvc() == nil {
		return nil
	}
	bg := context.Background()
	runID, wait, ferr := s.runSvc().FindRunByAction(bg, actionID)
	if ferr != nil || strings.TrimSpace(runID) == "" || wait == nil {
		return nil
	}
	claimed, claimErr := s.runSvc().ClaimWaitResume(bg, runID, actionID, s.approvalResumeOwner())
	if claimErr != nil || !claimed {
		return nil
	}
	rn, rerr := s.runSvc().GetRun(bg, runID)
	if rerr != nil || rn == nil {
		_ = s.runSvc().ReleaseWaitResumeOwner(bg, runID, actionID, s.approvalResumeOwner())
		return nil
	}
	cleared := false
	if s.actionSvc() != nil {
		if act, err := s.actionSvc().Get(bg, actionID); err == nil && act != nil {
			cleared = turn.ActionClearedContext(act)
		}
	}
	return &chatApprovalResume{
		ActionID:         actionID,
		RunID:            runID,
		SubagentRunID:    strings.TrimSpace(wait.SubagentRunID),
		SessionID:        rn.SessionID,
		Channel:          "tui",
		Input:            rn.InputText,
		ToolStepID:       turn.PendingApprovalToolStepID(wait.SessionSnapshot, wait.ToolName),
		ToolName:         wait.ToolName,
		AgentID:          wait.AgentID,
		SubagentType:     wait.SubagentType,
		SessionSnapshot:  append([]llm.Message(nil), wait.SessionSnapshot...),
		ClearedContext:   cleared,
		OwnsDurableFence: true,
	}
}

func (s *ChatSession) hasPendingToolApproval() bool {
	if s == nil {
		return false
	}
	s.approvalMu.Lock()
	defer s.approvalMu.Unlock()
	return s.approvalPending != nil
}

func (s *ChatSession) peekPendingApproval() *chatApprovalResume {
	if s == nil {
		return nil
	}
	s.approvalMu.Lock()
	defer s.approvalMu.Unlock()
	if s.approvalPending == nil {
		return nil
	}
	cp := *s.approvalPending
	return &cp
}

func (s *ChatSession) ResetTUIPendingState() {
	if s == nil {
		return
	}
	s.CancelActiveRun()
	s.clearPendingApproval()
}

// runAuditStepHook builds the hook that publishes one session's tool steps as
// canonical events and UI messages. prev — resolved after the load below, the
// hook already installed where this one will run — runs first, so a
// process-level observer keeps seeing every step. The same hook serves two
// mounts: installed process-wide for a foreground turn by
// installRunAuditStepHook, and carried on the run context of a plan review,
// which executes outside any turn. It returns nil when the session cannot
// publish (no runner, no run store, a failed load), and a nil hook means
// "nothing to add" to every consumer.
func (s *ChatSession) runAuditStepHook(sessionID string, prev func() tool.StepHook) tool.StepHook {
	if s == nil || s.runSvc() == nil || s.runner() == nil {
		return nil
	}
	if err := s.runner().Load(); err != nil {
		return nil
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		sid = "default"
	}
	// The tool registry exists only after a load, which is why prev arrives
	// as a resolver rather than a value.
	chained := prev()
	return func(stepCtx context.Context, evt tool.StepEvent) {
		if chained != nil {
			chained(stepCtx, evt)
		}
		rid := strings.TrimSpace(tool.RunIDFromContext(stepCtx))
		if rid == "" {
			return
		}
		// Publish main-agent tool steps from the hook that observes their
		// original execution: the card is drawn here, and the canonical event
		// laid down alongside it is what a later resume replays.
		//
		// Output deltas are transient UI updates. Persisting every process-pipe
		// chunk would bloat the event log; the completed event already carries
		// the governed full stdout/stderr result.
		if agentID := strings.TrimSpace(tool.HookAgentIDFromContext(stepCtx)); agentID == "" {
			switch evt.Kind {
			case tool.StepKindToolParallelStarted, tool.StepKindToolParallelCompleted:
				s.notifyToolStepHooks(stepCtx, sid, rid, "tui", evt)
			case event.RunEventPlanUpdated:
				// The main agent's plan card is the only record of the update
				// — no transcript row carries it — so the canonical event is
				// persisted first and the card drawn from it.
				if evt.PlanUpdate == nil {
					break
				}
				if canonical, ok := tool.RunEventFromStep(stepCtx, sid, rid, "tui", evt); ok {
					s.persistRunEvent(stepCtx, canonical)
				}
				s.notifyUIForSession(sid, PlanUpdatedMsg{Payload: *evt.PlanUpdate})
			case event.RunEventToolStarted, event.RunEventToolOutputDelta, event.RunEventToolCompleted:
				if strings.TrimSpace(evt.ToolName) == "" || !shouldNotifyToolStep(evt) || tool.ToolStepRendersAsPlan(evt) {
					break
				}
				canonical, canonicalOK := tool.RunEventFromStep(stepCtx, sid, rid, "tui", evt)
				// An explicit skill step has no transcript tool row — the model
				// never issued a call — so it reaches the surfaces through the
				// canonical event, which persists it and draws it; the hook's
				// ordinary path would show the card live and lose it on resume.
				if strings.EqualFold(strings.TrimSpace(evt.Category), "skill") && canonicalOK && s.runner().Events != nil {
					_ = s.runner().Events.Publish(stepCtx, canonical)
					return
				}
				if canonicalOK {
					s.persistRunEvent(stepCtx, canonical)
				}
				s.notifyToolStepHooks(stepCtx, sid, rid, "tui", evt)
			}
		} else {
			switch evt.Kind {
			case event.RunEventPlanUpdated:
				// The parent's plan update reaches the conversation through the
				// main-agent branch above. A child's step is recorded on the child
				// run, so its plan card has to be published here or the subagent's
				// view never receives one — and the raw session_todo result was
				// shown in its place.
				if evt.PlanUpdate == nil {
					return
				}
				if canonical, ok := tool.RunEventFromStep(stepCtx, sid, rid, "tui", evt); ok && s.runner().Events != nil {
					_ = s.runner().Events.Publish(stepCtx, canonical)
					return
				}
				plan := *evt.PlanUpdate
				if strings.TrimSpace(plan.AgentID) == "" {
					plan.AgentID = agentID
				}
				s.notifyUIForSession(sid, PlanUpdatedMsg{Payload: plan})
				return
			case event.RunEventToolStarted, event.RunEventToolOutputDelta, event.RunEventToolCompleted:
				if !shouldNotifyToolStep(evt) || tool.ToolStepRendersAsPlan(evt) {
					break
				}
				if canonical, ok := tool.RunEventFromStep(stepCtx, sid, rid, "tui", evt); ok && s.runner().Events != nil {
					_ = s.runner().Events.Publish(stepCtx, canonical)
					return
				}
				// Populate Content on the completed phase so the subagent's per-agent
				// view shows the tool's actual output. The started phase stays empty:
				// the renderer shows no body for a running/pending tool.
				body := ""
				if evt.Kind == event.RunEventToolOutputDelta {
					body, _ = evt.Output["chunk"].(string)
				} else if evt.Kind == event.RunEventToolCompleted {
					formatted, _ := tool.FormatToolStepResult(evt, tool.DefaultMaxFormattedBody)
					body = strings.TrimSpace(formatted)
				}
				meta := tool.BuildToolMeta(evt)
				s.notifyUIForSession(sid, NewMessageMsg{Msg: Message{
					Kind:            MsgKindTool,
					StepID:          evt.StepID,
					ToolName:        evt.ToolName,
					Content:         body,
					Summary:         strings.TrimSpace(tool.SummarizeToolStep(evt)),
					ToolMeta:        meta,
					ToolPhase:       evt.Kind,
					ToolOutputDelta: evt.Kind == event.RunEventToolOutputDelta,
					RetainAsHistory: evt.RetainAsHistory,
					AgentID:         agentID,
					RunID:           rid,
					Duration:        evt.Duration,
					Timestamp:       time.Now(),
				}})
			}
		}
	}
}

// installRunAuditStepHook installs the session's tool step hook as the
// process-wide hook for the duration of one turn, restoring whatever was
// installed before. Runs dispatched inside that turn inherit it through the
// context prepareTUIAgentBase freezes; anything running outside a turn must
// carry the hook itself (the plan reviewer's does, as a field on the shared
// reviewer pkg/process builds this surface's wiring into).
func (s *ChatSession) installRunAuditStepHook(sessionID string) func() {
	if s == nil || s.runSvc() == nil || s.runner() == nil {
		return func() {}
	}
	hook := s.runAuditStepHook(sessionID, s.currentStepHook)
	if hook == nil {
		return func() {}
	}
	tools := s.Env.Tools()
	if tools == nil {
		return func() {}
	}
	prev := tools.StepHook()
	tools.SetStepHook(hook)
	return func() { tools.SetStepHook(prev) }
}

// currentStepHook reads the process-wide step hook, wherever it is installed
// at the moment. Nil when there is no tool registry yet.
func (s *ChatSession) currentStepHook() tool.StepHook {
	if s == nil || s.Env == nil {
		return nil
	}
	if tools := s.Env.Tools(); tools != nil {
		return tools.StepHook()
	}
	return nil
}

func shouldNotifyToolStep(evt tool.StepEvent) bool {
	return tool.ToolStepUserVisible(evt)
}

func stringValueFromMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if raw, ok := m[key].(string); ok {
		return strings.TrimSpace(raw)
	}
	return ""
}

func boolValueFromMap(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	}
	return false
}

func (s *ChatSession) prepareTUIAgentBase(sessionID string, agBase context.Context) (wrapped context.Context, streamFlag *bool, cleanup func()) {
	if s == nil {
		cleanup = func() {}
		streamed := false
		streamFlag = &streamed
		return agBase, streamFlag, cleanup
	}
	cleanup = func() {}
	streamed := false
	streamFlag = &streamed
	// Accumulate streamed assistant text + reasoning so a cancelled turn can
	// persist what was already displayed to the user. Reset per turn.
	accum := &turn.StreamPartial{}
	s.tuiStreamAccum = accum
	capture := run.NewPartialSessionCapture()
	s.tuiPartialCapture = capture
	foreground := foregroundTurnFrom(agBase)
	agBase = run.WithPartialSessionCapture(agBase, capture)
	if rt := s.ensureTUITurnInputRuntime(sessionID); rt != nil {
		agBase = run.WithTurnInputRuntime(agBase, rt)
	}
	agBase = llm.WithStreamSink(agBase, &llm.StreamSink{
		Streamed:          &streamed,
		OnResponseStarted: func() { foreground.acknowledgeResponse() },
		OnDelta: func(text string) {
			if text == "" || !foreground.acknowledgeResponse() {
				return
			}
			accum.AppendContent(text)
			s.notifyUIForSession(sessionID, NewMessageMsg{turn: foreground, Msg: Message{
				Kind:      MsgKindAssistant,
				Content:   text,
				Timestamp: time.Now(),
			}})
		},
		OnReasoningDelta: func(text string) {
			if text == "" || !foreground.acknowledgeResponse() {
				return
			}
			accum.AppendReasoning(text)
			s.notifyUIForSession(sessionID, NewMessageMsg{turn: foreground, Msg: Message{
				Kind:      MsgKindReasoning,
				Content:   text,
				Timestamp: time.Now(),
			}})
		},
		OnReasoningDone: func() {
			s.notifyUIForSession(sessionID, ReasoningDoneMsg{turn: foreground})
		},
		OnResponseCompleted: func() {
			foreground.acknowledgeResponse()
			accum.ResponseCompleted()
		},
		OnWebSearch: func(id, detail string, completed bool) {
			if !foreground.acknowledgeResponse() {
				return
			}
			phase := event.RunEventToolStarted
			if completed {
				phase = event.RunEventToolCompleted
			}
			// Same step id, wording and meta a subagent's search is published
			// with, so the two read identically wherever they are shown.
			s.notifyUIForSession(sessionID, NewMessageMsg{turn: foreground, Msg: Message{
				Kind:      MsgKindTool,
				StepID:    tool.ProviderWebSearchStepID(id),
				ToolName:  tool.ProviderWebSearchToolName,
				Summary:   tool.ProviderWebSearchSummary(detail, completed),
				ToolMeta:  tool.ProviderWebSearchMeta(detail, completed, ""),
				ToolPhase: phase,
				Timestamp: time.Now(),
			}})
		},
		OnUsage: func(inputTokens int, outputTokens int) {
			if inputTokens <= 0 && outputTokens <= 0 {
				return
			}
			s.notifyUIForSession(sessionID, TokenUsageDeltaMsg{
				RunID:        strings.TrimSpace(tool.RunIDFromContext(agBase)),
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
			})
		},
		OnUsageSnapshot: func(inputTokens int, outputTokens int) {
			if inputTokens <= 0 && outputTokens <= 0 {
				return
			}
			// Surface the context budget live (per LLM response) so the
			// composer footer shows "N%" during long
			// agentic turns, not only after the turn persists its usage. This
			// callback is an absolute snapshot: usage deltas are unsuitable
			// here because Anthropic emits input at message_start and the
			// remaining output at message_delta.
			if msg, ok := s.tokenBudgetMessageFromUsage(sessionID, inputTokens+outputTokens); ok {
				s.notifyUIForSession(sessionID, msg)
			}
		},
	})
	restoreAudit := s.installRunAuditStepHook(sessionID)
	// Freeze the audit hook onto this run's context before restoring the
	// process-wide TUI fallback. Detached/async subagents inherit this context
	// and may emit tool events after the foreground turn has returned; relying
	// on State.StepHook at that point either drops those events or routes them
	// through a later session's hook.
	if s.Env != nil && s.Env.Tools() != nil {
		if audit := s.Env.Tools().StepHook(); audit != nil {
			agBase = tool.WithStepHook(agBase, audit)
		}
	}
	cleanup = restoreAudit
	return agBase, streamFlag, cleanup
}

func (s *ChatSession) appendAssistantOutcome(sessionID string, runID string, channel string, userInput string, completion turnCompletion, res *agent.Result, streamAssistantViaTUI bool, out io.Writer) {
	// reasoningText is still needed below for the TUI-only rendering.
	outText, reasoningText := turn.AssistantOutcomeText(res)
	turn.PersistAssistantTurn(context.Background(), s.sessStore(), turn.AssistantTurn{
		SessionID: sessionID,
		RunID:     runID,
		Result:    res,
		Model:     cliResultModel(s),
		End:       completion.runEnd(),
		OnSequenceError: func(err error) {
			if s.chatLog != nil {
				s.chatLog.Errorf("forebrain chat append_assistant_outcome session=%s run=%s failed: %v", sessionID, runID, err)
			}
		},
	})
	s.notifyTokenBudget(context.Background(), sessionID)
	hideFromForeground := false
	elapsed := completion.Duration
	if s.chatLog != nil {
		s.chatLog.Infof("forebrain chat turn ok session=%s assistant_bytes=%d elapsed=%s", sessionID, len(outText), elapsed)
		ot := llm.TruncateBytes(outText, 512, "...")
		s.chatLog.Debugf("forebrain chat turn ok session=%s assistant_bytes=%d elapsed=%s preview=%q", sessionID, len(outText), elapsed, ot)
	}
	if strings.TrimSpace(outText) != "" && !streamAssistantViaTUI && !hideFromForeground {
		if reasoningText != "" {
			s.notifyUIForSession(sessionID, NewMessageMsg{Msg: Message{
				Kind:      MsgKindReasoning,
				Content:   reasoningText,
				Timestamp: time.Now(),
			}})
			s.notifyUIForSession(sessionID, ReasoningDoneMsg{})
		}
		s.notifyUIForSession(sessionID, NewMessageMsg{Msg: Message{
			Kind:      MsgKindAssistant,
			Content:   outText,
			Timestamp: time.Now(),
		}})
	} else if reasoningText != "" && !streamAssistantViaTUI {
		s.notifyUIForSession(sessionID, NewMessageMsg{Msg: Message{
			Kind:      MsgKindReasoning,
			Content:   reasoningText,
			Timestamp: time.Now(),
		}})
		s.notifyUIForSession(sessionID, ReasoningDoneMsg{})
	}
	if out != nil && strings.TrimSpace(outText) != "" && !hideFromForeground {
		_, _ = fmt.Fprintln(out, outText)
	}
}

func cliResultModel(s *ChatSession) string {
	if s == nil || s.runner() == nil {
		return ""
	}
	_, model := run.PrimaryModel(s.runner())
	return model
}

func (s *ChatSession) emitPartialAssistantFromRAE(rae *tool.RequiresActionError, alreadyStreamed bool) {
	if s == nil || rae == nil {
		return
	}
	// Streaming providers already sent this assistant text through the TUI
	// sink before the approval-gated tool returned RequiresActionError. Sending
	// the snapshot text again leaves a duplicate in the reducer buffer, which
	// is flushed when the approval is resolved (most visibly after denying
	// exit_plan_mode). Keep this as a fallback only for non-streaming providers.
	if alreadyStreamed {
		return
	}
	if len(rae.SessionSnapshot) == 0 {
		return
	}
	last, ok := turn.LastToolCallMessage(rae.SessionSnapshot)
	if !ok {
		return
	}
	text := strings.TrimSpace(last.TextContent())
	if text == "" {
		return
	}
	// A gate raised inside a subagent's own tool call carries that subagent's
	// snapshot, so this text is the subagent speaking. It belongs to its view
	// like the rest of its output; untagged it would be attributed to the
	// primary agent and retained in the conversation instead.
	s.notifyUI(NewMessageMsg{Msg: Message{
		Kind:      MsgKindAssistant,
		Content:   text,
		AgentID:   strings.TrimSpace(rae.AgentID),
		Timestamp: time.Now(),
	}})
}

func (s *ChatSession) resumeAfterApproval(actionID string) bool {
	if s == nil {
		return false
	}
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return false
	}
	p := s.takePendingApprovalForAction(actionID)
	if p == nil {
		if s.chatLog != nil {
			s.chatLog.DebugForceFlushf("forebrain tui tool_approval resumeAfterApproval skipped action_id=%s (no matching pending state and no fb_run_waits row)", actionID)
		}
		return false
	}
	if !s.tuiController().ClaimResume(p.RunID, p.SessionID) {
		if p.OwnsDurableFence && s.runSvc() != nil {
			_ = s.runSvc().ReleaseWaitResumeOwner(context.Background(), p.RunID, p.ActionID, s.approvalResumeOwner())
		}
		return false
	}
	if s.runSvc() != nil && strings.TrimSpace(p.RunID) != "" {
		bg := context.Background()
		_ = s.runSvc().SetStatus(bg, p.RunID, state.RunStatusRunning)
	}
	s.clearPendingApproval()
	if s.runner() == nil || s.pipe() == nil {
		return true
	}
	if s.surfaceSyncResume.Load() {
		s.runResumeApprovedTurn(p, actionID)
		return true
	}
	go s.runResumeApprovedTurn(p, actionID)
	return true
}

// resumeVariant describes what differs between resuming after the user
// approved the pending tool call and resuming after they rejected it.
// Everything else about a resume — the run bookkeeping, the agent context, the
// supervisor options, and the requires-action, cancel and error handling — is
// the same either way, so it lives once in runResumeTurn.
type resumeVariant struct {
	// logLabel names the flow in this resume's log lines.
	logLabel string
	// denied carries the user's rejection back to the model instead of
	// replaying the call. It also switches off the three pieces of work only an
	// approval produces: applying a permission update the approval granted,
	// trimming the snapshot when the approval cleared context, and capturing
	// the replayed tool results.
	denied bool
}

func (s *ChatSession) runResumeApprovedTurn(p *chatApprovalResume, actionID string) {
	s.runResumeTurn(p, actionID, resumeVariant{logLabel: "resume"})
}

func (s *ChatSession) runResumeDeniedTurn(p *chatApprovalResume, actionID string) {
	s.runResumeTurn(p, actionID, resumeVariant{logLabel: "resume-denied", denied: true})
}

// resumeSessionID returns the AgentSessionID the resumed tool call should run
// under. When the approval gate was raised inside a subagent (p.SubagentRunID
// set), that subagent's tool calls — including session-scoped ones like
// intermediate_tool and session_todo — were keyed by its own WorkerSessionID,
// not the top-level chat session. Without recovering it here, replaying the
// approved call under p.SessionID silently moves it to the top-level chat
// session's state files, orphaning everything the subagent wrote (notes,
// todos, mode) before the gate fired: append succeeds under the subagent's
// session, and a later read - now running post-resume under the wrong
// session - finds nothing.
//
// agent.GetMerged reads both the in-memory registry (the common same-process
// approve/deny flow) and the durable subagent-history ledger startSubagent
// writes at spawn time — before the subagent can reach an approval gate, let
// alone finish — so the paused subagent's WorkerSessionID is recoverable by
// its RunID even after a process restart wiped the in-memory registry, as
// long as p.SubagentRunID itself survived the restart. It does: chat_turn.go
// and chat_session.go persist it into state.Wait's source metadata alongside
// AgentID/SubagentType, and takePendingApprovalForAction's fallback carries
// it into p when reconstructing a resume from that persisted wait.
func (s *ChatSession) resumeSessionID(p *chatApprovalResume) string {
	if p == nil {
		return ""
	}
	runID := strings.TrimSpace(p.SubagentRunID)
	if runID == "" {
		return p.SessionID
	}
	entry, ok, err := agent.GetMerged(s.stateRoot(), agent.Query{RunID: runID})
	if err != nil || !ok {
		return p.SessionID
	}
	if wsid := strings.TrimSpace(entry.WorkerSessionID); wsid != "" {
		return wsid
	}
	return p.SessionID
}

// resumeAgentContext assembles the agent context one resume runs under and, for
// an approval, the capture that collects the results of the replayed tool call.
// It is where the two flows differ, so it is also where they are tested from.
func (s *ChatSession) resumeAgentContext(
	p *chatApprovalResume, actionID string, v resumeVariant, t0 time.Time,
) (context.Context, *tool.ReplayResultCapture) {
	agBase := process.AgentContextForProject(context.Background(), s.stateRoot(), s.resumeSessionID(p), runnerProjectKey(s.runner()))
	agBase = tool.WithConversationSessionID(agBase, p.SessionID)
	if subtype := strings.TrimSpace(p.SubagentType); subtype != "" {
		agBase = tool.WithSubagentType(agBase, subtype)
	}
	if agentID := strings.TrimSpace(p.AgentID); agentID != "" {
		agBase = tool.WithHookAgentID(agBase, agentID)
	}
	agBase = tool.WithRunID(agBase, p.RunID)
	if !v.denied {
		agBase = tool.WithApprovedActionID(agBase, actionID)
	}
	resumeSnapshot := p.SessionSnapshot
	denyReason := ""
	var action *state.Action
	if s.actionSvc() != nil {
		action, _ = s.actionSvc().Get(context.Background(), actionID)
	}
	resume := turn.BuildApprovalResumeWithPlanReviews(context.Background(), s.runSvc(), action, p.SessionSnapshot, p.ToolName, p.ClearedContext)
	if v.denied {
		// The user's feedback lives in the action's Error field, where
		// ActionSvc.Deny stores it. For a denied plan the reviews the
		// approval collected are composed onto it at the resume — read from
		// the conversation's plan_reviewed events, where every surface and a
		// restart read them from — so the stored row and every card printed
		// from it say only what the user said, while the model still gets
		// the full guidance.
		denyReason = resume.Reason
	} else if resume.Cleared {
		// The user cleared context: do not replay the pre-clear history back
		// into the model. Trim to a minimal anchor that still lets the approved
		// tool (exit_plan_mode) replay. The store-side reset cursor already
		// excludes the old rows from the next turn's projection.
		resumeSnapshot = resume.Session
		// Persist the post-reset anchor before replaying the tool. The resume
		// flow persists replayed tool results separately before appending the
		// final assistant outcome; without this anchor that first tool-result
		// row would be orphaned because the pre-clear assistant tool_call is
		// behind the reset cursor. AppendMessageSequence is idempotent, so an
		// idempotent approval retry does not duplicate the anchor.
		if s.sessStore() != nil && len(resumeSnapshot) > 0 {
			_ = s.sessStore().AppendMessageSequence(
				context.Background(),
				p.SessionID,
				resumeSnapshot,
				cliResultModel(s),
				"",
			)
		}
	}
	if len(resumeSnapshot) > 0 {
		resumeState := &tool.ToolApprovalResumeState{
			Session:      resumeSnapshot,
			Denied:       v.denied,
			DenyReason:   denyReason,
			DenyFeedback: resume.Feedback,
		}
		// The fence travels with the resume state rather than being crossed
		// here, so it opens when the replay starts and closes when the replay
		// ends. OwnsDurableFence is the same test the eager version made: a
		// wait row for this action exists and this process holds its lease.
		if p.OwnsDurableFence {
			turn.BindApprovalContinuationFence(resumeState, s.runSvc(), p.RunID, actionID, s.approvalResumeOwner())
		}
		agBase = tool.WithToolApprovalResume(agBase, resumeState)
	}
	// Capture replayed tool results so they can be persisted to the session
	// store after the run. Without this, the tool result lives only inside
	// the orchestration LLM's internal session and is discarded, leaving a
	// dangling tool_calls row that RepairDanglingToolResults later strips.
	// The LLM then loses knowledge that the tool was called and approved
	// (e.g. exit_plan_mode), and may re-invoke it on subsequent turns.
	// A denial replays nothing, so it has no results to capture.
	var replayCapture *tool.ReplayResultCapture
	if !v.denied {
		replayCapture = tool.NewReplayResultCapture()
		agBase = tool.WithReplayResultCapture(agBase, replayCapture)
	}
	return agBase, replayCapture
}

func (s *ChatSession) runResumeTurn(p *chatApprovalResume, actionID string, v resumeVariant) {
	s.dispatchTurnMu.Lock()
	defer s.dispatchTurnMu.Unlock()
	t0 := time.Now()
	if !p.TurnStartedAt.IsZero() {
		t0 = p.TurnStartedAt
	}
	var runUsage runUsageCarrier
	var completion turnCompletion
	emitRunEnded := true
	defer func() {
		if !emitRunEnded {
			return
		}
		// The run ends here, so its clock is owed to the transcript too. An
		// ending that wrote its own output stamped it already, with the same
		// window; one that wrote nothing — the approval's effect failed — is
		// stamped here, so replay closes it the way this line closes it live.
		end := completion
		if end.StartedAt.IsZero() {
			end = completeTurn(t0)
		}
		runID := strings.TrimSpace(runUsage.runID)
		if runID == "" {
			runID = strings.TrimSpace(p.RunID)
		}
		_ = s.sessStore().StampRunTiming(context.Background(), runID, state.RunTiming{StartedAt: end.StartedAt, FinishedAt: end.FinishedAt, Worked: end.Duration})
		s.notifyUI(RunEndedMsg{
			RunID:          runUsage.runID,
			WorkedDuration: completion.Duration,
			InputTokens:    runUsage.in,
			OutputTokens:   runUsage.out,
		})
	}()
	if s.chatLog != nil {
		s.chatLog.Debugf("forebrain chat %s session=%s run_id=%s action_id=%s", v.logLabel, p.SessionID, p.RunID, actionID)
	}
	if !v.denied && s.actionSvc() != nil && s.runner() != nil {
		if act, aerr := s.actionSvc().Get(context.Background(), actionID); aerr == nil && act != nil && act.Status == state.ActionApproved {
			service := turn.ApprovalService{Actions: s.actionSvc(), Policy: s.runner()}
			if s.Env != nil {
				service.Network = s.Env.Tools()
			}
			if effectErr := service.ApplyResolvedEffect(act, p.RunID); effectErr != nil {
				explained := llm.ExplainError(effectErr)
				if s.runSvc() != nil {
					bg := context.Background()
					_ = s.runSvc().ClearWait(bg, p.RunID)
					_ = s.runSvc().SetStatus(bg, p.RunID, state.RunStatusFailed)
					// Publishing the turn error is what draws it, live as on
					// replay, in the words the error block always uses.
					_ = s.publishTUIRunEvent(bg, p.SessionID, p.RunID, "turn_error", event.TurnErrorPayload{Error: explained, Message: explained})
				} else {
					s.notifyUI(NewMessageMsg{Msg: Message{Kind: MsgKindError, Content: explained, RunID: p.RunID, Timestamp: time.Now()}})
				}
				s.tuiFinish(p.RunID)
				return
			}
			s.refreshSandboxRuntime()
		}
	}
	agBase, replayCapture := s.resumeAgentContext(p, actionID, v, t0)
	agWrapped, streamFlag, cleanup := s.prepareTUIAgentBase(p.SessionID, agBase)
	defer cleanup()
	var runIDOut string
	// Continuing past an approval gate is a turn like any other, so it goes
	// through Submit as well: reusing the gated run's ID as ExistingRunID is
	// what keeps it one run rather than a second one, and Trigger tells hooks
	// this is a resume rather than a fresh user turn.
	s.turnBeforeAgent = func(agentCtx context.Context, runID string) error {
		// See the identical call in dispatchUserTurnContent: this must run
		// before any tool/usage event for the run reaches the reducer, so
		// per-run counters key off the real run ID instead of "".
		s.notifyUI(RunStartedMsg{RunID: runID})
		return nil
	}
	defer func() { s.turnBeforeAgent = nil }()
	outcome, err := s.Core.Submit(agWrapped, turn.TurnRequest{
		SessionID:     p.SessionID,
		Origin:        turn.Origin{Surface: turn.SurfaceTUI, ChannelID: p.Channel},
		Trigger:       "resume",
		UserText:      p.Input,
		ExistingRunID: p.RunID,
	}, nil)
	res := outcome.Result
	runIDOut = outcome.RunID
	var rae *tool.RequiresActionError
	if r := requiresActionErrorFromOutcome(outcome); r != nil {
		rae = r
		err = rae
	}
	if errors.As(err, &rae) {
		runID := strings.TrimSpace(runIDOut)
		if runID == "" && rae != nil {
			runID = strings.TrimSpace(rae.RunID)
		}
		s.tuiWait(runID)
	} else {
		if s.runSvc() != nil {
			_ = s.runSvc().ClearWait(context.Background(), p.RunID)
		}
		// This turn supplied its own run id (it resumes the gated run), so the
		// executor leaves the finished-transition to the caller.
		s.tuiFinish(runIDOut)
	}
	completion = completeTurn(t0)
	runUsage.runID = strings.TrimSpace(runIDOut)
	if runUsage.runID == "" {
		runUsage.runID = strings.TrimSpace(p.RunID)
	}
	if res != nil && res.Summary != nil {
		runUsage.in = res.Summary.Usage.InputTokens
		runUsage.out = res.Summary.Usage.OutputTokens
	}
	s.hydrateRunUsage(&runUsage)
	streamAssistantViaTUI := streamFlag != nil && *streamFlag
	if err != nil {
		var rae *tool.RequiresActionError
		if errors.As(err, &rae) && rae != nil && s.runSvc() != nil {
			rid := strings.TrimSpace(runIDOut)
			if rid == "" {
				rid = strings.TrimSpace(rae.RunID)
			}
			if rid != "" {
				s.persistRequiresActionSnapshot(p.SessionID, rid, rae.SessionSnapshot)
				s.attachRunWaitForRequiresAction(rid, p.SessionID, p.Channel, p.Input, rae, t0)
				s.emitPartialAssistantFromRAE(rae, streamAssistantViaTUI)
				emitRunEnded = shouldEmitRunEndedOnRequiresAction(true)
				return
			}
		}
		if errors.Is(err, context.Canceled) {
			if s.chatLog != nil {
				s.chatLog.Infof("forebrain chat %s cancelled session=%s elapsed=%s", v.logLabel, p.SessionID, time.Since(t0))
			}
			// Persist any tool result replayed during this resume (e.g. an
			// approved exit_plan_mode) before discarding the cancelled run.
			// persistRequiresActionSnapshot left a dangling tool_calls row in
			// the store; the replay produced the matching result, but it lives
			// only in the orchestration LLM's in-memory session and would be
			// dropped on cancel. Without persisting it here, the next turn's
			// RepairDanglingToolResults strips the row and the LLM loses the
			// knowledge that the tool was called and approved - so it re-invokes
			// exit_plan_mode even though the mode already transitioned to agent.
			s.persistReplayResults(p.SessionID, runUsage.runID, replayCapture)
			// Also persist any additional partial content produced after the
			// replay (completed tool calls/results + streamed partial text).
			s.persistCancelledTurnOutcome(p.SessionID, runUsage.runID, completion)
			s.notifyUI(StreamResetMsg{})
			return
		}
		// Persist replay results and partial session before returning the
		// error, so a transient LLM error (429, network, etc.) does not
		// lose the work already completed (approved tool replay + any
		// additional tool calls/results produced after the replay).
		s.persistReplayResults(p.SessionID, runUsage.runID, replayCapture)
		s.persistCancelledTurnOutcome(p.SessionID, runUsage.runID, completion)
		if s.chatLog != nil {
			s.chatLog.Errorf("forebrain chat %s error session=%s elapsed=%s err=%v", v.logLabel, p.SessionID, time.Since(t0), err)
		}
		explained := llm.ExplainError(err)
		s.notifyUI(NewMessageMsg{Msg: Message{
			Kind:      MsgKindError,
			Content:   explained,
			Timestamp: time.Now(),
		}})
		s.persistRunTurnError(p.SessionID, runUsage.runID, explained)
		return
	}
	s.persistReplayResults(p.SessionID, runUsage.runID, replayCapture)
	s.appendAssistantOutcome(p.SessionID, p.RunID, p.Channel, p.Input, completion, res, streamAssistantViaTUI, io.Discard)
}

func (s *ChatSession) resumeAfterDenial(actionID string) bool {
	if s == nil {
		return false
	}
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return false
	}
	p := s.takePendingApprovalForAction(actionID)
	if p == nil {
		if s.chatLog != nil {
			s.chatLog.DebugForceFlushf("forebrain tui tool_approval resumeAfterDenial skipped action_id=%s (no matching pending state)", actionID)
		}
		return false
	}
	if !s.tuiController().ClaimResume(p.RunID, p.SessionID) {
		if p.OwnsDurableFence && s.runSvc() != nil {
			_ = s.runSvc().ReleaseWaitResumeOwner(context.Background(), p.RunID, p.ActionID, s.approvalResumeOwner())
		}
		return false
	}
	s.notifyToolApprovalDenied(p, actionID)
	if s.runSvc() != nil && strings.TrimSpace(p.RunID) != "" {
		bg := context.Background()
		_ = s.runSvc().SetStatus(bg, p.RunID, state.RunStatusRunning)
	}
	s.clearPendingApproval()
	if s.runner() == nil || s.pipe() == nil {
		return true
	}
	if s.surfaceSyncResume.Load() {
		s.runResumeDeniedTurn(p, actionID)
		return true
	}
	go s.runResumeDeniedTurn(p, actionID)
	return true
}

// notifyToolApprovalDenied replaces the pending approval card before the
// asynchronous denied resume calls back into the model. The orchestration run
// continues with a denial tool result, but the original UI tool step is done.
func (s *ChatSession) notifyToolApprovalDenied(p *chatApprovalResume, actionID string) {
	if s == nil || p == nil {
		return
	}
	call, ok := turn.PendingApprovalToolCall(p.SessionSnapshot, p.ToolName, p.ToolStepID)
	if !ok {
		return
	}
	stepID := strings.TrimSpace(call.ID)
	toolName := strings.TrimSpace(call.Function.Name)
	if stepID == "" || toolName == "" {
		return
	}

	input := map[string]any{}
	if raw := strings.TrimSpace(call.Function.Arguments); raw != "" {
		if err := json.Unmarshal([]byte(raw), &input); err != nil {
			input = map[string]any{"raw": raw}
		}
	}
	meta := tool.BuildToolMeta(tool.StepEvent{
		Kind:     tool.StepKindToolCompleted,
		StepID:   stepID,
		ToolName: toolName,
		Input:    input,
	})

	// The verdict belongs to the tool the user was actually asked about. A gate
	// raised by a nested request_permissions under another tool replaces that
	// outer tool's card: it never ran and was never itself refused, so it reads
	// as canceled. Every other card here is the card of the refused call, and
	// calling that one "canceled" claimed the turn had been torn down when the
	// run is in fact carrying on with a refusal result.
	status := "denied"
	if !strings.EqualFold(strings.TrimSpace(p.ToolName), toolName) {
		status = "canceled"
	}
	// The card says what the user said — the one sentence the denial's
	// display half carries, from the same place the persisted tool result's
	// tool_display body comes — never the guidance composed for the model.
	feedback := ""
	if status == "denied" && s.actionSvc() != nil {
		if act, err := s.actionSvc().Get(context.Background(), actionID); err == nil && act != nil {
			feedback = act.Error
		}
	}
	content := tool.DeniedToolDisplayBody(toolName, feedback)
	meta.Status = status
	s.notifyUI(NewMessageMsg{Msg: Message{
		Kind:      MsgKindTool,
		StepID:    stepID,
		ToolName:  toolName,
		FilePath:  extractFilePathFromInput(input),
		Content:   content,
		ToolMeta:  meta,
		ToolPhase: event.RunEventToolCompleted,
		RunID:     p.RunID,
		Timestamp: time.Now(),
	}})
}

func (s *ChatSession) abortPendingApproval(sessionID, actionID string) bool {
	if s == nil {
		return false
	}
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return false
	}
	p := s.takePendingApprovalForAction(actionID)
	runID := ""
	if p != nil {
		runID = strings.TrimSpace(p.RunID)
	}
	if runID == "" && s.runSvc() != nil {
		if rid, _, err := s.runSvc().FindRunByAction(context.Background(), actionID); err == nil {
			runID = strings.TrimSpace(rid)
		}
	}
	// A subagent approval is nested inside the still-running parent turn. Its
	// cancel button targets that child only; CancelActiveRun chooses the
	// surface's foreground run and could otherwise tear down the parent (or a
	// sibling) and reset the primary assistant buffer.
	scopedToSubagent := p != nil && (strings.TrimSpace(p.AgentID) != "" || strings.TrimSpace(p.SubagentType) != "")
	cancelledActive := false
	controllerRunID := runID
	if scopedToSubagent {
		if childRunID := strings.TrimSpace(p.SubagentRunID); childRunID != "" {
			controllerRunID = childRunID
		}
	}
	if controllerRunID != "" {
		controller := s.tuiController()
		cancelledActive = controller.Cancel(controllerRunID, context.Canceled)
		if scopedToSubagent {
			// The child shares the primary turn's input runtime. Remove only its
			// controller entry so the still-running parent keeps queued input.
			controller.Finish(controllerRunID)
		} else {
			// The gated primary run owns the surface turn. Retire that exact run
			// through the established finish path so queued input and deferred
			// config reload receive their normal end-of-turn cleanup.
			s.tuiFinish(controllerRunID)
		}
	}
	if s.runSvc() != nil && runID != "" {
		bg := context.Background()
		_ = s.runSvc().ClearWait(bg, runID)
		_ = s.runSvc().SetStatus(bg, runID, state.RunStatusCancelled)
		_ = s.publishTUIRunEvent(bg, sessionID, runID, "turn_cancelled", event.TurnCancelledPayload{Message: "cancelled"})
		_ = s.runSvc().CancelRunningDescendants(bg, runID)
	}
	// The gated call is now known never to have run, so the transcript says so
	// here rather than being left to the next turn's repair - which made the
	// transcript legal by deleting the call, taking the canceled card the user
	// is looking at out of their history with it.
	//
	// Only the conversation's own gate: a subagent's parked call lives in that
	// child's transcript, not in the session this record names.
	if p != nil && !scopedToSubagent && s.sessStore() != nil {
		turn.AnswerAbandonedToolCalls(context.Background(), s.sessStore(), p.SessionID, cliResultModel(s))
	}
	s.clearPendingApproval()
	if !scopedToSubagent {
		s.notifyUI(StreamResetMsg{})
		// The dispatch that parked this run suppressed its own RunEnded so the
		// resume could close the turn instead. Cancelling the gate means that
		// resume never happens, so this is where the run ends and where its
		// closing "Worked for …" line is owed: every run ends with one,
		// whatever ended it. Without this the transcript kept the cancelled
		// card as its last word, with no turn boundary, no duration and no
		// spend, and the usage tracker held the run open for the rest of the
		// session.
		//
		// Only a run this process is actually retiring. cancelledActive is that
		// test: the controller entry exists and was still live, which is true
		// exactly when this process parked the turn. Recovery replays a decision
		// an earlier process committed and rebuilds its continuation from the
		// durable wait, so it holds no controller entry and closes no turn of
		// this session's.
		if runID != "" && cancelledActive {
			runUsage := runUsageCarrier{runID: runID}
			s.hydrateRunUsage(&runUsage)
			// A zero duration is left to the reducer, which still holds the
			// parked run's start; a guessed "0s" would read as a turn that
			// never ran.
			var worked time.Duration
			if p != nil && !p.TurnStartedAt.IsZero() {
				end := completeTurn(p.TurnStartedAt)
				worked = end.Duration
				// The run ends here; replay closes it with this same line.
				_ = s.sessStore().StampRunTiming(context.Background(), runID, state.RunTiming{StartedAt: end.StartedAt, FinishedAt: end.FinishedAt, Worked: end.Duration})
			}
			s.notifyUI(RunEndedMsg{
				RunID:          runID,
				WorkedDuration: worked,
				InputTokens:    runUsage.in,
				OutputTokens:   runUsage.out,
			})
		}
	}
	if s.chatLog != nil {
		s.chatLog.DebugForceFlushf("forebrain tui tool_approval abort action_id=%s run_id=%s cancel_active=%t", actionID, runID, cancelledActive)
	}
	return p != nil || runID != "" || cancelledActive
}

// maybeAutoCompactBeforeAppend compacts the conversation before a turn when it
// no longer leaves room for one. The compaction publishes its own lifecycle,
// which is what draws its card.
func (s *ChatSession) maybeAutoCompactBeforeAppend(ctx context.Context, sessionID string, channel string, input string, rawInput string) error {
	if s == nil || s.sessStore() == nil {
		return nil
	}
	svc := run.CompactionService(s.runner(), s.sessStore())
	_, err := assembly.RunPreflight(ctx, assembly.PreflightConfig{
		HookContext: hook.HookContext{
			SessionID: sessionID,
			Channel:   channel,
			Trigger:   "user",
			RawInput:  rawInput,
		},
		Input: input,
		Now:   time.Now,
		Assemble: func(ctx context.Context, hc hook.HookContext, input string) (assembly.AssemblyResult, bool, error) {
			if s.ctxHook() == nil {
				return assembly.AssemblyResult{}, false, nil
			}
			return s.ctxHook().Assemble(hc, input)
		},
		AutoCompact: svc.AutoCompactSession,
	})
	if err != nil {
		return fmt.Errorf("auto-compact preflight: %w", err)
	}
	return nil
}

func (s *ChatSession) notifyTokenBudget(ctx context.Context, sessionID string) {
	if s == nil {
		return
	}
	msg, ok := s.tokenBudgetMessage(ctx, sessionID)
	if !ok {
		return
	}
	s.notifyUI(msg)
}

func (s *ChatSession) tokenBudgetMessage(ctx context.Context, sessionID string) (TokenBudgetUpdatedMsg, bool) {
	usage, ok := run.ContextOccupancy(ctx, s.sessStore(), sessionID)
	if !ok {
		return TokenBudgetUpdatedMsg{}, false
	}
	return s.tokenBudgetMessageFromUsage(sessionID, usage)
}

// tokenBudgetMessageFromUsage builds the composer-footer budget message from a
// raw context-occupancy token count of sessionID's conversation. Splitting it
// out lets the live streaming path (OnUsage, fired per LLM response mid-turn)
// surface the budget without waiting for the turn to finish and persist its
// usage to the session state. The computation itself is the engine's one copy,
// which sizes it by the model the conversation's session runs on.
func (s *ChatSession) tokenBudgetMessageFromUsage(sessionID string, usage int) (TokenBudgetUpdatedMsg, bool) {
	if s == nil || usage <= 0 {
		return TokenBudgetUpdatedMsg{}, false
	}
	payload, ok := run.ContextBudget(s.runner(), sessionID, nil, usage)
	if !ok {
		return TokenBudgetUpdatedMsg{}, false
	}
	return tokenBudgetMsgFromPayload(payload), true
}

// tokenBudgetResetMessage builds the composer-footer budget message for a freshly
// cleared context (zero usage → "100%"). Unlike
// tokenBudgetMessageFromUsage, it does not early-return on usage 0: a clear must
// actively push the footer back to full. It returns false only when the model is
// unknown to the catalog (no context window to render).
func (s *ChatSession) tokenBudgetResetMessage(sessionID string) (TokenBudgetUpdatedMsg, bool) {
	if s == nil {
		return TokenBudgetUpdatedMsg{}, false
	}
	payload, ok := run.ContextBudget(s.runner(), sessionID, nil, 0)
	if !ok {
		return TokenBudgetUpdatedMsg{}, false
	}
	return tokenBudgetMsgFromPayload(payload), true
}

// tokenBudgetMsgFromPayload is the terminal's message form of the engine's
// budget payload — the same numbers, tagged with the agent whose context they
// measure so the reducer never confuses a subagent's gauge with the primary
// agent's.
func tokenBudgetMsgFromPayload(payload event.TokenBudgetUpdatedPayload) TokenBudgetUpdatedMsg {
	return TokenBudgetUpdatedMsg{
		AgentID:              payload.AgentID,
		Model:                payload.Model,
		TokenUsage:           payload.TokenUsage,
		PercentLeft:          payload.PercentLeft,
		ContextWindow:        payload.ContextWindow,
		EffectiveWindow:      payload.EffectiveWindow,
		AutoCompactThreshold: payload.AutoCompactThreshold,
	}
}

// SurfaceComposerTokenStats is the footer's budget for a context holding
// usage tokens — the configured auto-compact limit included — so the footer
// reads the same at startup, on resume and after every response.
func (s *ChatSession) SurfaceComposerTokenStats(sessionID string, usage int) ComposerTokenStats {
	msg, ok := s.tokenBudgetResetMessage(sessionID)
	if usage > 0 {
		msg, ok = s.tokenBudgetMessageFromUsage(sessionID, usage)
	}
	if !ok {
		return ComposerTokenStats{}
	}
	return composerTokenStatsFromBudget(msg)
}

func (s *ChatSession) compactExplicitLimit() int {
	if s == nil || s.runner() == nil || s.runner().AppCfg == nil {
		return 0
	}
	return s.runner().AppCfg.Compact.ModelAutoCompactTokenLimit
}

// turnCompletion is the single authoritative completion snapshot for a turn.
// Both persistence and the terminal notification must reuse this value so the
// displayed and stored durations cannot drift while post-turn work runs.
type turnCompletion struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Duration   time.Duration
}

func completeTurn(startedAt time.Time) turnCompletion {
	if startedAt.IsZero() {
		return turnCompletion{}
	}
	finishedAt := time.Now()
	duration := finishedAt.Sub(startedAt)
	if duration < 0 {
		duration = 0
	}
	return turnCompletion{
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		Duration:   duration,
	}
}

// runEnd is the completion as the run's clock the transcript stamps.
func (c turnCompletion) runEnd() turn.RunEnd {
	return turn.RunEnd{StartedAt: c.StartedAt, FinishedAt: c.FinishedAt, Worked: c.Duration}
}
