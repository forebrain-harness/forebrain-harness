// The chat surface: turn dispatch, approvals, attachments, and plan review.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

func (s *ChatSession) DispatchSurfaceTurn(ctx context.Context, submission turn.TurnSubmission) error {
	if s == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.surfaceSyncResume.Store(true)
	defer s.surfaceSyncResume.Store(false)
	// Runner.Load and hot reload can replace the tool registry after the UI
	// installed its approval sink. Rebind at the turn boundary so inline
	// network and subagent approvals cannot silently degrade into hard denials.
	s.syncToolApprovalHooks()
	// This call owns the whole turn, including the approval loop below and the
	// resume runs it drives. Queued turn input survives those intermediate run
	// boundaries (see tuiWait), so the turn returning here is what
	// retires the queue.
	defer s.discardTUITurnInput()
	sessionID := strings.TrimSpace(submission.SessionID)
	userText := submission.UserText
	ch := strings.TrimSpace(submission.Channel)
	if ch == "" {
		ch = "tui"
	}
	if strings.HasPrefix(strings.TrimSpace(userText), "!") && len(submission.Attachments) == 0 {
		return s.RunSurfaceShellCommand(ctx, sessionID, ch, userText)
	}
	s.ensureSessionStartHooks(sessionID)
	// The tui/TUI path expands a slash command before dispatch, passing
	// the built prompt as UserText and the raw command (e.g. "/init") as
	// RawInput. Seed rawInput from it so the raw command is persisted as the
	// display-only content for resume replay, while UserText stays the model input.
	rawInput := strings.TrimSpace(submission.RawInput)
	// When the TUI surface path already parsed a /goal slash command
	// upstream, the submission carries the objective directly; prefer it
	// so the goal continuation loop fires without re-parsing.
	goalObjective := strings.TrimSpace(submission.GoalObjective)
	skillName := strings.TrimSpace(submission.SkillName)
	skillPath := strings.TrimSpace(submission.SkillPath)
	if res, ok := s.slashCommandResult(ctx, sessionID, ch, userText); ok {
		if res.ShouldContinueRun {
			next := strings.TrimSpace(res.ContinueInput)
			if next != "" {
				rawInput = userText
				userText = next
				submission.DisplayText = ""
				goalObjective = strings.TrimSpace(res.GoalObjective)
				skillName = strings.TrimSpace(res.SkillName)
				skillPath = strings.TrimSpace(res.SkillPath)
			}
		} else if res.Handled {
			if res.SelectSession {
				s.notifyUI(SessionSelectionRequestedMsg{})
			}
			if res.ManagePermissions {
				s.notifyUI(PermissionManagementRequestedMsg{})
			}
			if res.SelectSkill {
				s.notifyUI(SkillSelectionRequestedMsg{})
			}
			if res.Picker != nil {
				s.notifyUI(SlashPickerRequestedMsg{Picker: res.Picker})
			}
			if strings.TrimSpace(res.Reply) == "" &&
				!res.SessionChanged &&
				!res.ExitRequested &&
				!res.SelectSession &&
				!res.ManagePermissions &&
				!res.SelectSkill &&
				res.Picker == nil {
				return nil
			}
			// Slash replies are UI output only (R-model): never appended to
			// the transcript, never sent to the model. They surface as a
			// system report frame, not an assistant message — except when the
			// same result also switches the session: that reply travels with
			// the switch below and renders in the target session only after
			// the switch succeeded, so a failed activation cannot leave a
			// success line behind in the outgoing one.
			if strings.TrimSpace(res.Reply) != "" && !res.SessionSwitched {
				s.notifyUI(NewMessageMsg{Msg: Message{
					Kind:      MsgKindSystem,
					Content:   res.Reply,
					Timestamp: time.Now(),
				}})
			}
			if res.SessionChanged {
				s.notifyUI(SessionSwitchedMsg{
					SessionID: strings.TrimSpace(res.SessionID),
					Title:     strings.TrimSpace(res.SessionTitle),
					Reason:    strings.TrimSpace(res.Reply),
					Select:    res.SessionSwitched,
				})
			}
			if res.ExitRequested {
				s.notifyUI(QuitRequestedMsg{Reason: res.Reply})
			}
			return nil
		}
	}
	// @ mentions are resolved in the composer, not here: picking a file leaves
	// a bare path in the draft and picking an image attaches it outright, so a
	// submitted turn carries no mention syntax left to expand.
	parts := []llm.ContentPart(nil)
	userPartsJSON := ""
	displayText := strings.TrimSpace(submission.DisplayText)
	if len(submission.Attachments) > 0 {
		var err error
		parts, displayText, err = turn.SurfaceTurnContentParts(userText, displayText, submission.Attachments)
		if err != nil {
			return err
		}
		userPartsJSON = turn.BuildSurfaceUserPartsJSON(userText, submission.Attachments)
	}
	// Mentions add nothing to the prompt beyond the path the user typed, so
	// the model input is the submitted text (plus any image placeholder) and
	// nothing else. Persisting exactly that text is what lets the next turn
	// replay this one from a cached prefix instead of a perturbed one.
	modelInput := turn.DisplayTextOrUserText(userText, displayText)
	if len(submission.Attachments) > 0 {
		userPartsJSON = turn.BuildSurfaceUserPartsJSON(modelInput, submission.Attachments)
	}
	unlock := func() {}
	if s.Env != nil && s.Env.Foreground != nil {
		var err error
		unlock, err = s.Env.Foreground.Lock(ctx, sessionID)
		if err != nil {
			return err
		}
	}
	defer unlock()
	if err := s.dispatchUserTurnContent(ctx, sessionID, ch, modelInput, rawInput, parts, userPartsJSON, goalObjective, skillName, skillPath, submission.CallerRendersReturnedError, io.Discard); err != nil {
		return err
	}
	sink := s.toolApprovalSinkGet()
	for s.surfaceNeedsToolApproval(ctx, sessionID) {
		req, err := s.buildSurfaceToolApprovalRequest(ctx, sessionID)
		if err != nil {
			return err
		}
		if req == nil {
			return fmt.Errorf("%w", turn.ErrAwaitingToolApproval)
		}
		if sink == nil {
			return fmt.Errorf("%w", turn.ErrAwaitingToolApproval)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		decision, perr := sink.PromptToolApproval(ctx, *req)
		if perr != nil {
			return perr
		}
		if err := s.completeSurfaceToolApprovalDecision(ctx, req.ActionID, decision); err != nil {
			return err
		}
	}
	return nil
}

func (s *ChatSession) EnsureSurfaceTranscript(ctx context.Context, sessionID string) error {
	if s == nil || s.sessStore() == nil {
		return nil
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return nil
	}
	return s.sessStore().Ensure(ctx, sid, sid)
}

func (s *ChatSession) PreferredSurfaceTranscriptSessionID(ctx context.Context) string {
	if s == nil || s.sessStore() == nil {
		return ""
	}
	sid, err := s.sessStore().SessionIDWithLatestMessage(ctx)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(sid)
}

func (s *ChatSession) ClearSurfaceSession(ctx context.Context, sessionID string) error {
	if s == nil {
		return nil
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return nil
	}

	if s.sessStore() != nil {
		var snapshots turn.SnapshotStore
		if s.runner() != nil && s.Env.Tools() != nil {
			snapshots = s.Env.Tools()
		}
		if err := turn.ClearContext(ctx, s.sessStore(), snapshots, sid); err != nil {
			return err
		}
	}

	// The token budget footer shows the whole window again until the next
	// assembly measures it.
	if postMsg, ok := s.tokenBudgetResetMessage(sid); ok {
		s.notifyUI(postMsg)
	}

	return nil
}

func (s *ChatSession) ListSessionsRecent(ctx context.Context, limit int) ([]state.SessionSummary, error) {
	if s == nil || s.sessStore() == nil {
		return nil, nil
	}
	return s.sessStore().ListSessionsRecent(ctx, limit)
}

// SessionTitle names one session by id — "" when it has none — the same
// store contract the /resume picker and the terminal title read.
func (s *ChatSession) SessionTitle(ctx context.Context, id string) (string, error) {
	if s == nil || s.sessStore() == nil {
		return "", nil
	}
	return s.sessStore().SessionTitle(ctx, id)
}

// ListSessionsRecentPaged backs the /resume picker's lazy browsing: it reads
// one LIMIT/OFFSET page per call so an arbitrarily long session history can be
// scrolled without ever loading it whole.
func (s *ChatSession) ListSessionsRecentPaged(ctx context.Context, limit, offset int) ([]state.SessionSummary, error) {
	if s == nil || s.sessStore() == nil {
		return nil, nil
	}
	return s.sessStore().ListSessionsRecentPaged(ctx, limit, offset)
}

// displayTextOrUserText moved to pkg/turn.DisplayTextOrUserText (P5-2): a
// pure function with no ChatSession dependency, so it belongs in the lower
// layer that owns canonical turn input normalization.

func (s *ChatSession) SetToolApprovalSink(sink turn.ToolApprovalDecisionSink) {
	if s == nil {
		return
	}
	s.toolApprovalMu.Lock()
	s.toolApprovalSink = sink
	s.toolApprovalMu.Unlock()
	s.syncToolApprovalHooks()
}

func (s *ChatSession) syncToolApprovalHooks() {
	if s.runner() == nil || s.Env.Tools() == nil {
		return
	}
	sink := s.toolApprovalSinkGet()
	if sink == nil {
		s.Env.Tools().SetNetworkApprovalPromptHook(nil)
		s.Env.Tools().SetSubagentApprovalHook(nil)
		return
	}
	s.Env.Tools().SetNetworkApprovalPromptHook(s.promptInlineNetworkApproval)
	// A subagent that hits an approval gate is asked here too, so its run and
	// the tool call that dispatched it both stay alive while the user answers.
	s.Env.Tools().SetSubagentApprovalHook(s.resolveSubagentApproval)
}

func (s *ChatSession) promptInlineNetworkApproval(ctx context.Context, actionID string, payload map[string]any, request safety.NetworkApprovalRequest) (safety.NetworkApprovalDecision, error) {
	if s == nil {
		return safety.NetworkApprovalDeny, fmt.Errorf("nil session")
	}
	s.networkApprovalMu.Lock()
	defer s.networkApprovalMu.Unlock()
	resolved := false
	defer func() {
		if !resolved && s.actionSvc() != nil {
			_, _ = s.actionSvc().Cancel(context.Background(), actionID, "network approval was cancelled before a decision was returned")
		}
	}()
	sink := s.toolApprovalSinkGet()
	if sink == nil {
		return safety.NetworkApprovalDeny, fmt.Errorf("network approval requires an interactive surface")
	}
	description := fmt.Sprintf("Network access to %q is blocked by policy.", strings.TrimSpace(request.Context.Host))
	// A blocked host is reported from inside the shell call that hit it, so the
	// execution context still names the agent that made the call. Carrying it
	// onto the request is what tells the overlay which subagent is asking and
	// keeps the confirmation in that subagent's own view.
	approvalRequest, _, _ := s.baseApprovalRequest(turn.ApprovalSource{
		SessionID: request.EnvironmentID, ActionID: actionID, RunID: request.ExecutionID,
		ToolName: "shell", ActionKind: "shell", ToolInput: payload,
		AgentID: tool.HookAgentIDFromContext(ctx), SubagentType: tool.SubagentTypeFromContext(ctx),
	})
	approvalRequest.Description = description
	approvalRequest.Reason = description
	approvalRequest.DestinationOptions = []safety.PermissionDestination{
		safety.DestinationSession, safety.DestinationLocalSettings,
	}
	approvalRequest.SuggestedDestination = safety.DestinationLocalSettings
	// The gate knows the host and port first-hand; the payload copy is only
	// there for the overlay to display.
	approvalRequest.NetworkApproval = &request.Context
	approvalRequest.NetworkPort = request.Port
	decision, err := sink.PromptToolApproval(ctx, approvalRequest)
	if err != nil {
		return safety.NetworkApprovalDeny, err
	}
	if err := s.completeSurfaceToolApprovalDecision(ctx, actionID, decision); err != nil {
		return safety.NetworkApprovalDeny, err
	}
	resolved = true
	switch {
	case decision.NetworkPolicyAmendment != nil && decision.Approved:
		if decision.NetworkPolicyAmendment.Action == safety.NetworkPolicyDeny {
			return safety.NetworkApprovalDenyForSession, nil
		}
		return safety.NetworkApprovalAllowInFuture, nil
	case decision.Update != nil && decision.Update.Destination == safety.DestinationSession && decision.Approved:
		return safety.NetworkApprovalAllowForSession, nil
	case decision.Approved:
		return safety.NetworkApprovalAllowOnce, nil
	case decision.Cancelled:
		return safety.NetworkApprovalCancel, nil
	default:
		return safety.NetworkApprovalDeny, nil
	}
}

func (s *ChatSession) baseApprovalRequest(src turn.ApprovalSource) (turn.ToolApprovalRequest, safety.ToolApprovalSuggestion, bool) {
	return turn.BuildToolApprovalRequest(src, s.approvalEvaluator())
}

// approvalEvaluator supplies the permission explanation an approval request
// carries. Nil when there is no runner, which leaves the request unexplained
// rather than wrongly explained.
func (s *ChatSession) approvalEvaluator() turn.ApprovalEvaluator {
	if s == nil || s.runner() == nil {
		return nil
	}
	return s.runner()
}

func (s *ChatSession) toolApprovalSinkGet() turn.ToolApprovalDecisionSink {
	if s == nil {
		return nil
	}
	s.toolApprovalMu.Lock()
	defer s.toolApprovalMu.Unlock()
	return s.toolApprovalSink
}

func (s *ChatSession) surfaceNeedsToolApproval(ctx context.Context, sessionID string) bool {
	if s == nil {
		return false
	}
	if s.hasPendingToolApproval() {
		return true
	}
	return s.approvalGate().Waiting(ctx, sessionID)
}

// approvalGate is the shared pending-approval lookup, pointed at this
// session's stores. The in-memory approval the TUI is already holding names
// the parked run, which saves the store search on the common path.
func (s *ChatSession) approvalGate() *turn.PendingApprovalGate {
	if s == nil || s.runSvc() == nil {
		return nil
	}
	gate := &turn.PendingApprovalGate{
		Runs: s.runSvc(),
		RunIDHint: func() string {
			if p := s.peekPendingApproval(); p != nil {
				return p.RunID
			}
			return ""
		},
		// The plan a parked plan gate is asking about resolves against the
		// per-agent state root (workspace root), NOT s.home(). The plan is
		// written/edited at state.PlanPathForProject(stateRoot, projectKey) -
		// the same root/project scope the plan-mode LLM wrapper, enter/exit_plan_mode
		// tools and write_file/edit_file gating all resolve. The main agent's root
		// is <home>/workspace, so using s.home() here would resolve a different path
		// and the overlay would show "No plan found".
		PlanScope: func(context.Context, string) (string, string) {
			return s.stateRoot(), runnerProjectKey(s.runner())
		},
		// The models a review may be handed to come from the active agent's
		// configured provider chain, with the model in force marked current.
		ReviewModels: func() []turn.PlanReviewModelOption {
			currentProvider, currentModel := run.PrimaryModel(s.runner())
			return turn.PlanReviewModelOptions(
				modelConfigFromChatSession(s), chatSessionActiveAgentName(s), currentProvider, currentModel,
			)
		},
	}
	if s.actionSvc() != nil {
		gate.Actions = s.actionSvc()
	}
	if s.runner() != nil {
		gate.Evaluator = s.runner()
	}
	return gate
}

func (s *ChatSession) buildSurfaceToolApprovalRequest(ctx context.Context, sessionID string) (*turn.ToolApprovalRequest, error) {
	// Everything the request carries — the parked call's identity, the plan
	// file path, the review models, the reviews already collected — comes
	// from the shared gate; nothing here is this surface's own.
	return s.approvalGate().Pending(ctx, sessionID)
}

func (s *ChatSession) completeSurfaceToolApprovalDecision(ctx context.Context, actionID string, decision turn.ToolApprovalDecision) (err error) {
	return s.completeSurfaceApproval(ctx, actionID, decision, nil)
}

func (s *ChatSession) completeSurfaceApproval(ctx context.Context, actionID string, decision turn.ToolApprovalDecision, audit *ApprovalSubmit) (err error) {
	if s == nil || s.actionSvc() == nil {
		return fmt.Errorf("nil session or action service")
	}
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return fmt.Errorf("empty action id")
	}
	if decision.RequestPlanReview != nil {
		return s.completeSurfacePlanReviewRequest(ctx, actionID, *decision.RequestPlanReview)
	}
	act, aerr := s.actionSvc().Get(ctx, actionID)
	if aerr != nil || act == nil {
		return fmt.Errorf("load action: %w", aerr)
	}
	// The surface knows the conversation this gate belongs to, so the cancel
	// dispatch names it rather than looking it up from the run.
	abort := func(id string) bool { return s.abortPendingApproval(act.SessionID, id) }
	sub := ApprovalSubmit{
		ActionID:  actionID,
		Approved:  decision.Approved,
		Cancelled: decision.Cancelled,
		KeyChord:  "forebrain-tui-permission",
	}
	if audit != nil {
		sub = *audit
		sub.ActionID = actionID
		sub.Approved = decision.Approved
		sub.Cancelled = decision.Cancelled
		if strings.TrimSpace(sub.KeyChord) == "" {
			sub.KeyChord = "forebrain-tui-permission"
		}
	}
	if act != nil && act.Kind == "user_interaction" {
		sub.PolicyKind = "user_interaction"
		sub.AskAnswerJSON = strings.TrimSpace(decision.AskAnswerJSON)
	}
	if act.Kind == "user_interaction" && decision.Approved {
		s.submitAskUserQuestionApproval(ctx, sub)
		return nil
	}
	choice := approvalChoice(act, decision)
	service := turn.ApprovalService{Actions: s.actionSvc(), Policy: s.runner()}
	if s.runner() != nil {
		service.Network = s.Env.Tools()
	}
	result, err := service.Decide(ctx, actionID, turn.ApprovalReply{
		Choice: choice, Update: decision.Update, Network: decision.NetworkPolicyAmendment,
		// The stored reason is the user's own words: it is what the denial
		// card and the approval-resolved event print. The reviews a denied
		// plan collected reach the model when the denial is resumed, composed
		// there (resumeAgentContext), so the conversation never displays them.
		Permissions: decision.RequestPermissionsResponse, Reason: decision.DenyReason, ClearContext: decision.ClearContext,
	})
	if err != nil {
		return err
	}
	if result.Idempotent {
		if result.Cancelled {
			s.retryResumeDispatch(actionID, "cancel", abort)
		} else if result.Denied {
			s.retryResumeDispatch(actionID, "deny", s.resumeAfterDenial)
		} else {
			s.retryResumeDispatch(actionID, "approve", s.resumeAfterApproval)
		}
		return nil
	}
	if result.Action != nil {
		agentID, subagentType := turn.ActionSubagent(result.Action)
		runID := ""
		sessionID := result.Action.SessionID
		if s.runSvc() != nil {
			if foundRunID, _, findErr := s.runSvc().FindRunByAction(ctx, result.Action.ID); findErr == nil {
				runID = strings.TrimSpace(foundRunID)
				if sessionID == "" {
					if runRecord, runErr := s.runSvc().GetRun(ctx, runID); runErr == nil && runRecord != nil {
						sessionID = strings.TrimSpace(runRecord.SessionID)
					}
				}
			}
		}
		_ = s.publishRunEvent(ctx, event.NewRunEvent(
			"approval-resolved:"+result.Action.ID+":"+string(result.Action.Status), runID, sessionID,
			event.RunEventApprovalResolved, event.ApprovalResolvedPayload{
				ActionID: result.Action.ID, ActionKind: result.Action.Kind, Decision: string(result.Action.Status),
				Reason: result.Action.Error, AgentID: agentID, SubagentType: subagentType,
			}, time.Now(),
		))
	}
	if result.Approved && s.runner() != nil {
		s.refreshSandboxRuntime()
	}
	if decision.ClearContext {
		if sid := act.SessionID; sid != "" {
			if cerr := s.ClearSurfaceSession(ctx, sid); cerr != nil && s.chatLog != nil {
				s.chatLog.Debugf("forebrain tui tool_approval clear_context failed session=%s err=%v", sid, cerr)
			}
		}
		s.markPendingApprovalClearedContext(actionID)
	}
	if result.Cancelled {
		s.retryResumeDispatch(actionID, "cancel", abort)
	} else if result.Denied {
		s.retryResumeDispatch(actionID, "deny", s.resumeAfterDenial)
	} else {
		s.retryResumeDispatch(actionID, "approve", s.resumeAfterApproval)
	}
	return nil
}

// retryResumeDispatch retries dispatch (one of abortPendingApproval,
// resumeAfterDenial, resumeAfterApproval) a few times before giving up.
//
// The decision this action just received (service.Decide, above) has already
// been persisted by the time this runs — only the in-process dispatch that
// turns it into an actual resumed run can still fail. dispatch returns false
// when takePendingApprovalForAction can't find the pending state yet, or
// when run.Controller.ClaimResume finds the run's in-memory phase hasn't
// caught up to Waiting — both are narrow timing windows relative to the wait
// this same decision is resolving, not a permanent condition, so retrying
// the same dispatch call (not re-deciding) is the correct recovery: without
// it, a decision that lands in that window is silently dropped and the run
// is left waiting on an action that will never be decided again, which is a
// real defect a live user could hit, not just a test-timing artifact
// (found while stress-testing pkg/tui's characterization suite under -race).
func (s *ChatSession) retryResumeDispatch(actionID, label string, dispatch func(string) bool) {
	if dispatch(actionID) {
		return
	}
	const attempts = 5
	for i := 1; i < attempts; i++ {
		time.Sleep(20 * time.Millisecond)
		if dispatch(actionID) {
			return
		}
	}
	if s.chatLog != nil {
		s.chatLog.Errorf("forebrain tui tool_approval %s dispatch failed after %d attempts action_id=%s: run left waiting", label, attempts, actionID)
	}
}

func approvalChoice(act *state.Action, decision turn.ToolApprovalDecision) string {
	if decision.Cancelled {
		return "cancel"
	}
	if !decision.Approved {
		return "deny"
	}
	if decision.NetworkPolicyAmendment != nil {
		return "apply_network_policy_amendment"
	}
	if act != nil && strings.EqualFold(act.Kind, "request_permissions") {
		if response := decision.RequestPermissionsResponse; response != nil {
			if response.Scope == safety.GrantScopeSession {
				return "grant_for_session"
			}
			if response.StrictAutoReview {
				return "grant_for_turn_with_strict_auto_review"
			}
		}
		return "grant_for_turn"
	}
	return "accept"
}

// approvalDecisionActionStatus is the status the action service stores for a
// decision, derived here rather than read back afterwards.
//
// The display record of an approval has to be written before the decision is
// applied - it is what the user just watched happen - so the recorder cannot
// wait for the action row to name its own status. The event id it derives
// (approval-resolved:<action>:<status>) is the same id the post-decision
// publish uses, and the store keeps whichever arrived first, so a mapping that
// drifted by one status would show one decision twice on resume.
//
// Empty means the decision resolves nothing, which only a plan-review request
// does; approvalDisplayRecord is what turns that into a record of its own.
func approvalDecisionActionStatus(actionKind string, decision turn.ToolApprovalDecision) string {
	switch {
	case decision.RequestPlanReview != nil:
		return ""
	case decision.Cancelled:
		return string(state.ActionCancelled)
	case decision.Denied:
		return string(state.ActionDenied)
	case decision.Approved && strings.EqualFold(strings.TrimSpace(actionKind), "user_interaction"):
		return string(state.ActionAnswered)
	case decision.Approved:
		return string(state.ActionApproved)
	default:
		return ""
	}
}

// approvalDisplayDecisionReviewRequested is the decision a plan-review record
// carries. It is not an action status and never will be: the action is still
// pending when the line is printed. It exists so the record says what happened
// rather than leaving the field blank for a reader to guess at.
const approvalDisplayDecisionReviewRequested = "review_requested"

// approvalDisplayRecord names the display record one decision produces: the
// event id it is stored under and the decision it reports.
//
// A resolution borrows the id the post-decision publish builds from the stored
// action, so the two writes collapse into one event. A plan-review request is
// not a resolution - it leaves the gate open and the user answers it again
// afterwards - but it does print a line, so it gets an id of its own, keyed by
// the model that was asked, rather than claiming a decision nobody made. ok is
// false when the decision names no record at all.
func approvalDisplayRecord(actionID, actionKind string, decision turn.ToolApprovalDecision) (id string, reportedDecision string, ok bool) {
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return "", "", false
	}
	if review := decision.RequestPlanReview; review != nil {
		return "approval-review:" + actionID + ":" +
				strings.TrimSpace(review.Provider) + ":" + strings.TrimSpace(review.Model),
			approvalDisplayDecisionReviewRequested, true
	}
	status := approvalDecisionActionStatus(actionKind, decision)
	if status == "" {
		return "", "", false
	}
	return approvalResolvedEventID(actionID, status), status, true
}

// approvalResolvedEventID is the identity of an approval's display record. It
// is deliberately the same string chain the post-decision publish builds from
// the stored action, so the two writes collapse into one event.
func approvalResolvedEventID(actionID, actionStatus string) string {
	return "approval-resolved:" + strings.TrimSpace(actionID) + ":" + strings.TrimSpace(actionStatus)
}

func (s *ChatSession) SurfaceTranscriptMessages(ctx context.Context, sessionID string) ([]state.Message, error) {
	if s == nil || s.sessStore() == nil {
		return nil, nil
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return nil, nil
	}
	// Resume replay shows the user their full scrollable history, so read the
	// entire on-disk transcript rather than ListRecentMessages' compacted/cleared
	// model-context projection. The model transcript also contains runtime
	// meta messages the live conversation never rendered: the cached
	// <forebrain_environment_context> block, and the context-clear anchor's
	// copy of the gated assistant row. IsMeta is persisted structurally in
	// PartsJSON; do not infer this from message text, because a user may
	// legitimately type the same tags themselves. The marker is role-blind for
	// the same reason it is text-blind: it means "no surface drew this row",
	// whichever role carries it.
	turns, err := s.sessStore().ListAllMessages(ctx, sid, 0)
	if err != nil {
		return nil, err
	}
	visible := make([]state.Message, 0, len(turns))
	for _, turn := range turns {
		if _, _, _, isMeta := state.ParseMessageParts(turn.PartsJSON, ""); isMeta {
			continue
		}
		visible = append(visible, turn)
	}
	return visible, nil
}

// SurfaceActiveContextMessages returns only the active model-context projection.
// TUI uses this for the resume footer budget while it independently
// replays SurfaceTranscriptMessages in full for visual history.
func (s *ChatSession) SurfaceActiveContextMessages(ctx context.Context, sessionID string) ([]state.Message, error) {
	if s == nil || s.sessStore() == nil {
		return nil, nil
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return nil, nil
	}
	return s.sessStore().ListRecentMessages(ctx, sid, 0)
}

// SurfaceSessionEvents returns the immutable display history independently of
// the compactable model context.
func (s *ChatSession) SurfaceSessionEvents(ctx context.Context, sessionID string) ([]event.RunEvent, error) {
	if s == nil || s.runSvc() == nil || strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	var out []event.RunEvent
	var cursor, highWater int64
	for {
		page, err := s.runSvc().ListSessionEvents(ctx, sessionID, cursor, highWater, 1000)
		if err != nil {
			return nil, err
		}
		if highWater == 0 {
			highWater = page.HighWater
		}
		for _, evt := range page.Events {
			out = append(out, turn.RunEventFromRecord(evt))
		}
		if !page.HasMore || page.NextCursor <= cursor {
			return out, nil
		}
		cursor = page.NextCursor
	}
}

// SurfaceSessionPlanUpdates returns the plan cards the conversation's own
// session_todo calls emitted, in the order they were emitted.
//
// The canonical session event log carries every plan update; the ones the
// conversation's own agent raised have no agent id, while a subagent's belong
// to that subagent's view and replay from its own stream. Reading the plan from
// the same log every other fact is read from is what keeps a replay from
// rebuilding the raw tool result the live run never showed.
func (s *ChatSession) SurfaceSessionPlanUpdates(ctx context.Context, sessionID string) ([]event.PlanUpdatedPayload, error) {
	if s == nil || s.runSvc() == nil || strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	records, err := s.runSvc().ListSessionEventsOfType(ctx, sessionID, event.RunEventPlanUpdated, 5000)
	if err != nil {
		return nil, err
	}
	out := make([]event.PlanUpdatedPayload, 0)
	for _, record := range records {
		var p event.PlanUpdatedPayload
		if err := json.Unmarshal(record.Payload, &p); err != nil {
			continue
		}
		// A subagent's plan update is published to the canonical session
		// event log and replays from there into that subagent's own view.
		if strings.TrimSpace(p.AgentID) != "" {
			continue
		}
		if len(p.Items) == 0 && strings.TrimSpace(p.Title) == "" && p.Total == 0 {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *ChatSession) SurfaceTranscript(ctx context.Context, sessionID string, limit int) (string, error) {
	if s == nil || s.sessStore() == nil {
		return "", nil
	}
	if limit <= 0 {
		limit = 200
	}
	turns, err := s.sessStore().ListRecentMessages(ctx, sessionID, limit)
	if err != nil {
		return "", err
	}
	if len(turns) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("session: ")
	b.WriteString(strings.TrimSpace(sessionID))
	for _, turn := range turns {
		role := strings.TrimSpace(turn.Role)
		if role == "" {
			role = "message"
		}
		b.WriteString("\n\n")
		b.WriteString(role)
		b.WriteString(":\n")
		b.WriteString(strings.TrimSpace(turn.Content))
	}
	return b.String(), nil
}

// surfaceTurnContentParts and buildSurfaceUserPartsJSON moved to
// pkg/turn.SurfaceTurnContentParts / pkg/turn.BuildSurfaceUserPartsJSON
// (P5-2): both are pure functions over pkg/llm/pkg/turn/pkg/state types with
// no ChatSession dependency, so they belong in the lower layer that owns
// canonical turn input normalization, not the TUI surface.

// completeSurfacePlanReviewRequest handles the one decision that is not a
// verdict: the user asked another model to review the plan first. The action
// stays pending, so the surface's approval loop prompts again once the review
// is in — with the review attached to the rebuilt request.
func (s *ChatSession) completeSurfacePlanReviewRequest(ctx context.Context, actionID string, selection turn.PlanReviewModelOption) error {
	act, err := s.actionSvc().Get(ctx, actionID)
	if err != nil || act == nil {
		return fmt.Errorf("load action: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(act.Kind), "exit_plan_mode") {
		return fmt.Errorf("plan review is only valid for an exit-plan decision")
	}
	if strings.TrimSpace(selection.Model) == "" {
		return fmt.Errorf("plan review requires a model")
	}
	s.runPlanReview(ctx, actionID, selection)
	return nil
}

// runPlanReview asks the selected model to review the pending plan and records
// the result against the approval, which stays pending: the review is material
// for the user's decision, not a decision of its own.
//
// A failed review is reported to the user and leaves the approval untouched. It
// must not return an error, which would abort the whole turn and take the
// pending approval down with it over a second opinion that did not arrive.
//
// The review announces itself before it starts and closes itself on every path
// out of the shared flow (turn.RunPlanReview), and its closing event is also
// its storage: this surface no longer keeps the result anywhere, so a restart
// or another surface reading the conversation serves the same review.
func (s *ChatSession) runPlanReview(ctx context.Context, actionID string, selection turn.PlanReviewModelOption) {
	if s == nil {
		return
	}
	model := turn.Model{
		Provider: strings.TrimSpace(selection.Provider),
		Model:    strings.TrimSpace(selection.Model),
		Label:    strings.TrimSpace(selection.Label),
	}
	reviewer, err := s.planReviewerFor(model)
	if err != nil {
		// The reviewer never opened. Failing it through the review flow keeps
		// the approval pending and leaves the same trace any reviewer failure
		// leaves, rather than aborting the turn over a second opinion that did
		// not arrive.
		reviewer = planReviewConstructionError{err}
	}
	sessionID, runID := s.planReviewEventTarget(actionID)
	// No "review started" notice is emitted here: the surface already printed
	// the approval confirmation naming the reviewer, and the review run opens
	// its own subagent block in the transcript, which is where its progress is
	// visible while it reads the code.
	//
	// The review runs between two approval prompts, when the run that asked
	// for the approval has already returned, so a user interrupt has nothing
	// else to cancel: the review's own context is what Esc reaches.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.trackPlanReviewCancel(cancel)
	defer s.forgetPlanReviewCancel()
	// A session without a transcript store reviews without a task context
	// rather than handing the flow a typed-nil store: the interface would not
	// see the nil the way a direct call did.
	var transcripts turn.PlanReviewTranscripts
	if store := s.sessStore(); store != nil {
		transcripts = store
	}
	_ = turn.RunPlanReview(ctx, turn.PlanReviewRun{
		ActionID:    actionID,
		Model:       model,
		SessionID:   sessionID,
		RunID:       runID,
		StateRoot:   s.stateRoot(),
		ProjectKey:  runnerProjectKey(s.runner()),
		Transcripts: transcripts,
		Reviewer:    reviewer,
		Publish: func(ctx context.Context, evt event.RunEvent) error {
			if s == nil || s.runner() == nil || s.runner().Events == nil {
				return nil
			}
			return s.runner().Events.Publish(ctx, evt)
		},
	})
}

// planReviewConstructionError lets a reviewer that could not be built fail
// through the review flow every reviewer failure takes.
type planReviewConstructionError struct{ err error }

func (e planReviewConstructionError) Review(context.Context, turn.Request) (turn.PlanReviewResult, error) {
	return turn.PlanReviewResult{}, e.err
}

// planReviewEventTarget names the conversation and run a review belongs to:
// the one the approval gate it answers is holding. The events land there so
// a reload replays them beside the approval that asked for them.
func (s *ChatSession) planReviewEventTarget(actionID string) (sessionID, runID string) {
	if p := s.peekPendingApproval(); p != nil {
		if sid := strings.TrimSpace(p.SessionID); sid != "" {
			return sid, strings.TrimSpace(p.RunID)
		}
	}
	if s.actionSvc() != nil {
		if act, err := s.actionSvc().Get(context.Background(), actionID); err == nil && act != nil {
			return strings.TrimSpace(act.SessionID), ""
		}
	}
	return "", ""
}

// trackPlanReviewCancel publishes the review's cancel func so a user interrupt
// reaches it through the same key the rest of the turn answers to.
func (s *ChatSession) trackPlanReviewCancel(cancel context.CancelFunc) {
	if s == nil {
		return
	}
	s.tuiRunMu.Lock()
	s.planReviewCancel = cancel
	s.tuiRunMu.Unlock()
}

func (s *ChatSession) forgetPlanReviewCancel() {
	if s == nil {
		return
	}
	s.tuiRunMu.Lock()
	s.planReviewCancel = nil
	s.tuiRunMu.Unlock()
}

// planReviewerFor builds the reviewer for one model. The factory field is the
// seam tests use to drive the approval flow without a provider; a session that
// has not set one gets the shared reviewer in pkg/process, which runs as a
// plan-reviewer agent with the repository in front of it. What the surface
// contributes are its own seams: the step hook that publishes the reviewer's
// tool steps, and the approval func that answers the reviewer's own tool
// gates in the place the plan approval left.
func (s *ChatSession) planReviewerFor(model turn.Model) (turn.Reviewer, error) {
	if s.planReviewerFactory != nil {
		return s.planReviewerFactory(model)
	}
	sessionID := planReviewSessionID(s)
	return &process.PlanReviewer{
		Runner:    s.runner(),
		Model:     model,
		Config:    modelConfigFromChatSession(s),
		AgentName: chatSessionActiveAgentName(s),
		StepHook:  s.runAuditStepHook(sessionID, s.currentStepHook),
		Approve: func(runCtx context.Context, rae *tool.RequiresActionError) (context.Context, error) {
			return s.resolvePlanReviewApproval(runCtx, sessionID, rae)
		},
	}, nil
}

// resolvePlanReviewApproval puts one of the reviewer's tool requests to the
// user and returns the context the run resumes under. The plan approval that
// started the review is off screen at this point, so the reviewer's request
// takes that place rather than being refused.
func (s *ChatSession) resolvePlanReviewApproval(
	ctx context.Context, sessionID string, rae *tool.RequiresActionError,
) (context.Context, error) {
	s.subagentApprovalMu.Lock()
	defer s.subagentApprovalMu.Unlock()
	sink := s.toolApprovalSinkGet()
	if sink == nil {
		_, _ = s.actionSvc().Cancel(context.Background(), rae.ActionID, "no interactive surface for the plan review")
		return nil, fmt.Errorf("the reviewer needs approval to run %s and this surface cannot ask for it", strings.TrimSpace(rae.ToolName))
	}
	gate, _ := turn.SubagentApprovalGateFromError(rae)
	return s.answerSubagentApproval(ctx, sink, gate, s.planReviewToolApprovalRequest(sessionID, rae), "the plan review")
}

// resolveSubagentApproval is the seam a model-dispatched subagent's approvals
// travel on. It is installed on the tool state alongside the approval sink, so
// pkg/run can ask this surface without knowing it exists.
//
// The reason a subagent's gate is answered here rather than unwound the way the
// primary agent's is: the dispatching tool call is still in flight. For
// subagent_fanout that call is holding every sibling task's work — eight
// minutes of it in the report this was written for — and there is nothing above
// it that could replay the dispatch, so unwinding turns a question into three
// failed tasks. The run stays parked, the user answers, the child continues.
func (s *ChatSession) resolveSubagentApproval(
	ctx context.Context, rae *tool.RequiresActionError,
) (context.Context, error) {
	if s == nil || rae == nil {
		return nil, fmt.Errorf("nil subagent approval")
	}
	s.subagentApprovalMu.Lock()
	defer s.subagentApprovalMu.Unlock()
	sink := s.toolApprovalSinkGet()
	if sink == nil {
		// Returning the gate unchanged leaves the caller free to fall back to
		// the unwind-and-resume path. Refusing the tool here instead would
		// deny a subagent something the primary agent is allowed to ask for.
		return nil, rae
	}
	gate, ok := turn.SubagentApprovalGateFromError(rae)
	if !ok {
		return nil, fmt.Errorf("nil subagent approval")
	}
	req := turn.SubagentApprovalRequest(gate, s.dispatchingSessionIDForRun(gate.RunID), s.approvalEvaluator())
	return s.answerSubagentApproval(ctx, sink, gate, req, "the subagent")
}

// answerSubagentApproval is the terminal half both subagent approval paths
// share: put the request on screen, resolve the pending action with the answer,
// and hand back the context the child's run resumes under. Everything in it
// that is not "how this surface asks" lives in pkg/turn, so the web surface
// answers the same gate the same way.
func (s *ChatSession) answerSubagentApproval(
	ctx context.Context, sink turn.ToolApprovalDecisionSink,
	gate turn.SubagentApprovalGate, req turn.ToolApprovalRequest, who string,
) (context.Context, error) {
	owner := s.approvalResumeOwner()
	if s.runSvc() != nil {
		if err := s.runSvc().MarkWaitResumeOwner(context.Background(), gate.RunID, gate.ActionID, owner); err != nil {
			return nil, fmt.Errorf("claim subagent approval continuation: %w", err)
		}
	}
	promptCtx, cancelPrompt := context.WithCancel(ctx)
	defer cancelPrompt()
	stopLease := make(chan struct{})
	leaseLost := make(chan error, 1)
	if s.runSvc() != nil {
		go func() {
			ticker := time.NewTicker(state.WaitResumeLease / 3)
			defer ticker.Stop()
			for {
				select {
				case <-stopLease:
					return
				case <-promptCtx.Done():
					return
				case <-ticker.C:
					if err := s.runSvc().MarkWaitResumeOwner(context.Background(), gate.RunID, gate.ActionID, owner); err != nil {
						select {
						case leaseLost <- err:
						default:
						}
						cancelPrompt()
						return
					}
				}
			}
		}()
	}
	defer close(stopLease)
	_ = s.publishRunEvent(ctx, event.NewRunEvent(
		"approval-requested:"+gate.ActionID, gate.RunID, req.SessionID, event.RunEventApprovalReq,
		event.ApprovalRequestedPayload{
			ActionID: gate.ActionID, ActionKind: gate.ActionKind, AgentID: gate.AgentID,
			SubagentType: gate.SubagentType, Message: turn.SubagentApprovalDescription(gate),
		}, time.Now(),
	))
	decision, err := sink.PromptToolApproval(promptCtx, req)
	if err != nil {
		select {
		case leaseErr := <-leaseLost:
			return nil, fmt.Errorf("refresh subagent approval continuation: %w", leaseErr)
		default:
		}
		_, _ = s.actionSvc().Cancel(context.Background(), gate.ActionID, "approval for "+who+" failed")
		return nil, err
	}
	if s.runSvc() != nil {
		owned, ownErr := s.runSvc().WaitResumeOwnedBy(context.Background(), gate.RunID, gate.ActionID, owner)
		if ownErr != nil || !owned {
			if ownErr == nil {
				ownErr = fmt.Errorf("continuation lease was transferred")
			}
			return nil, ownErr
		}
	}
	// Released before the answer is applied: completeSurfaceToolApprovalDecision
	// would otherwise start its own resume of this run in parallel with the
	// dispatcher's own loop, executing the child twice.
	turn.ReleaseSubagentApprovalWait(context.Background(), s.runSvc(), s.tuiController(), gate.RunID)
	if err := s.completeSurfaceToolApprovalDecision(ctx, gate.ActionID, decision); err != nil {
		return nil, err
	}
	resumeCtx, resumeErr := turn.SubagentApprovalResumeContext(ctx, gate, turn.ApprovalResume{
		Session:  gate.Session,
		Approved: decision.Approved,
		Denied:   decision.Denied,
		Reason:   decision.DenyReason,
	}, who)
	if resumeErr != nil {
		return nil, resumeErr
	}
	if resume := tool.ToolApprovalResumeFromContext(resumeCtx); resume != nil && s.runSvc() != nil {
		// The question has been answered, so the child is running again. This
		// no longer rides on the execution fence: the fence now covers only the
		// replay itself, which a denial never reaches, and a denied child would
		// otherwise sit at waiting_action for the rest of its run.
		_ = s.runSvc().SetStatus(context.Background(), gate.RunID, state.RunStatusRunning)
		turn.BindApprovalContinuationFence(resume, s.runSvc(), gate.RunID, gate.ActionID, owner)
	}
	return resumeCtx, nil
}

// dispatchingSessionIDForRun resolves a subagent run back to the conversation
// that dispatched it, through the same durable subagent-history ledger the
// approval resume path reads (agent.GetMerged), so it also answers after a
// restart. It falls back to the empty string, which leaves the request
// evaluated against no session-scoped grants rather than against the wrong
// session's.
func (s *ChatSession) dispatchingSessionIDForRun(runID string) string {
	runID = strings.TrimSpace(runID)
	if s == nil || runID == "" {
		return ""
	}
	entry, ok, err := agent.GetMerged(s.stateRoot(), agent.Query{RunID: runID})
	if err != nil || !ok {
		return ""
	}
	return strings.TrimSpace(entry.SessionID)
}

// planReviewToolApprovalRequest describes one tool the reviewer wants to run.
// It is built from the requires-action error rather than a run-wait row: the
// review run is driven here, not through the surface's approval loop, so no
// wait row is ever recorded for it.
func (s *ChatSession) planReviewToolApprovalRequest(sessionID string, rae *tool.RequiresActionError) turn.ToolApprovalRequest {
	req, _, _ := s.baseApprovalRequest(turn.ApprovalSource{
		SessionID:    sessionID,
		ActionID:     rae.ActionID,
		RunID:        rae.RunID,
		ToolName:     rae.ToolName,
		ActionKind:   rae.ActionKind,
		ToolInput:    rae.ToolInput,
		AgentID:      rae.AgentID,
		SubagentType: run.PlanReviewSubagentType,
	})
	req.Description = "Requested by the plan reviewer."
	return req
}

func planReviewSessionID(s *ChatSession) string {
	if p := s.peekPendingApproval(); p != nil {
		if sid := strings.TrimSpace(p.SessionID); sid != "" {
			return sid
		}
	}
	return "default"
}
