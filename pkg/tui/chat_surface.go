// The chat surface: turn dispatch, approvals, attachments, and plan review.
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
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
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
	if postMsg, ok := s.tokenBudgetResetMessage(); ok {
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
	req, err := s.approvalGate().Pending(ctx, sessionID)
	if err != nil || req == nil {
		return req, err
	}
	// Everything above is shared with the gateway. What remains is genuinely
	// this surface's: the plan reviews live in TUI memory, and the plan file
	// path resolves against the per-agent state root.
	toolName := strings.TrimSpace(req.ToolName)
	if strings.EqualFold(toolName, "exit_plan_mode") {
		req.PlanReviewModels = s.planReviewOptions()
		req.PlanReviews = s.planReviewNotes(strings.TrimSpace(req.ActionID))
	}
	if strings.EqualFold(toolName, "enter_plan_mode") || strings.EqualFold(toolName, "exit_plan_mode") {
		// Use the per-agent state root (workspace root), NOT s.home(). The plan
		// is written/edited at state.PlanPathForProject(stateRoot, projectKey) -
		// the same root/project scope the plan-mode LLM wrapper, enter/exit_plan_mode
		// tools and write_file/edit_file gating all resolve. The main agent's root
		// is <home>/workspace, so using s.home() here would resolve a different path
		// and the overlay would show "No plan found".
		req.PlanFilePath = state.PlanPathForProject(s.stateRoot(), runnerProjectKey(s.runner()))
	}
	return req, nil
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
	// Every path below either resolves the approval or fails the turn, so the
	// reviews collected for it have no further reader once one returns cleanly.
	defer func() {
		if err == nil {
			s.clearPlanReviews(actionID)
		}
	}()
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
		Permissions: decision.RequestPermissionsResponse, Reason: s.planReviewDenyReason(actionID, decision.DenyReason), ClearContext: decision.ClearContext,
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

// planReviewTaskMessages bounds how far back the reviewer's task context
// reaches. Only user turns are kept, so this is a window over the request the
// plan is meant to serve, not over the agent's own work.
const (
	planReviewTaskMessages = 6
	planReviewMaxReviews   = 4
)

// planReviewOptions lists the models the user may hand the plan to. They come
// from the active agent's configured provider chain — the same list /model
// offers — because a reviewer must run on credentials the session already has.
func (s *ChatSession) planReviewOptions() []turn.PlanReviewModelOption {
	if s == nil {
		return nil
	}
	// Plan review is the user's own request for a second opinion, not a
	// subagent the model spawns, so agents.defaults.enable_subagent does not
	// govern it. Returning no models — which removes the row from the approval
	// overlay — happens only when there is no configured model to ask.
	entries := turn.ModelChoices(modelConfigFromChatSession(s), chatSessionActiveAgentName(s))
	if len(entries) == 0 {
		return nil
	}
	currentProvider, currentModel := run.PrimaryModel(s.runner())
	seen := make(map[string]struct{}, len(entries))
	out := make([]turn.PlanReviewModelOption, 0, len(entries))
	for _, entry := range entries {
		provider := strings.TrimSpace(entry.Provider)
		model := strings.TrimSpace(entry.Model)
		if model == "" {
			continue
		}
		key := strings.ToLower(provider + "/" + model)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, turn.PlanReviewModelOption{
			Provider: provider,
			Model:    model,
			Label:    strings.TrimSpace(entry.Name),
			Current: strings.EqualFold(provider, strings.TrimSpace(currentProvider)) &&
				strings.EqualFold(model, strings.TrimSpace(currentModel)),
		})
	}
	return out
}

// planReviewNotes returns the reviews already collected for one pending
// approval, so a re-prompted overlay shows them above the choices.
func (s *ChatSession) planReviewNotes(actionID string) []turn.PlanReviewNote {
	results := s.planReviewResults(actionID)
	if len(results) == 0 {
		return nil
	}
	notes := make([]turn.PlanReviewNote, 0, len(results))
	for _, result := range results {
		notes = append(notes, turn.PlanReviewNote{
			Provider: result.Model.Provider,
			Model:    result.Model.Model,
			Text:     result.Text,
			Duration: result.Duration,
		})
	}
	return notes
}

func (s *ChatSession) planReviewResults(actionID string) []turn.PlanReviewResult {
	if s == nil {
		return nil
	}
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return nil
	}
	s.planReviewMu.Lock()
	defer s.planReviewMu.Unlock()
	return append([]turn.PlanReviewResult(nil), s.planReviews[actionID]...)
}

func (s *ChatSession) appendPlanReview(actionID string, result turn.PlanReviewResult) {
	if s == nil {
		return
	}
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return
	}
	s.planReviewMu.Lock()
	defer s.planReviewMu.Unlock()
	if s.planReviews == nil {
		s.planReviews = map[string][]turn.PlanReviewResult{}
	}
	kept := append(s.planReviews[actionID], result)
	if len(kept) > planReviewMaxReviews {
		kept = kept[len(kept)-planReviewMaxReviews:]
	}
	s.planReviews[actionID] = kept
}

// clearPlanReviews drops the reviews for an approval that has been resolved.
func (s *ChatSession) clearPlanReviews(actionID string) {
	if s == nil {
		return
	}
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return
	}
	s.planReviewMu.Lock()
	defer s.planReviewMu.Unlock()
	delete(s.planReviews, actionID)
}

// planReviewDenyReason merges the collected reviews with the user's own
// feedback into the text the planning model receives on denial. Composing it
// here rather than in the surface keeps one wording for every surface, and
// keeps the review reaching the model even when the user denies from the
// choice list without typing anything.
func (s *ChatSession) planReviewDenyReason(actionID, userFeedback string) string {
	return turn.ComposeDenyFeedback(s.planReviewResults(actionID), userFeedback)
}

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
func (s *ChatSession) runPlanReview(ctx context.Context, actionID string, selection turn.PlanReviewModelOption) {
	if s == nil {
		return
	}
	model := turn.Model{
		Provider: strings.TrimSpace(selection.Provider),
		Model:    strings.TrimSpace(selection.Model),
		Label:    strings.TrimSpace(selection.Label),
	}
	plan, err := state.GetPlanForProject(s.stateRoot(), runnerProjectKey(s.runner()))
	if err != nil {
		s.notifyPlanReviewFailure(model, fmt.Errorf("read plan: %w", err))
		return
	}
	if strings.TrimSpace(plan) == "" {
		s.notifyPlanReviewFailure(model, turn.ErrNoPlan)
		return
	}
	reviewer, err := s.planReviewerFor(model)
	if err != nil {
		s.notifyPlanReviewFailure(model, err)
		return
	}
	// No "review started" notice is emitted here: the surface already printed
	// the approval confirmation naming the reviewer, and the review run opens
	// its own subagent block in the transcript, which is where its progress is
	// visible while it reads the code.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.trackPlanReviewCancel(cancel)
	defer s.forgetPlanReviewCancel()
	result, err := reviewer.Review(ctx, turn.Request{
		Plan:  plan,
		Task:  s.planReviewTaskContext(ctx, actionID),
		Model: model,
	})
	if err != nil {
		s.notifyPlanReviewFailure(model, err)
		return
	}
	s.appendPlanReview(actionID, result)
	s.notifyUI(NewMessageMsg{Msg: Message{
		Kind:      MsgKindPlan,
		Summary:   planReviewSummary(result),
		Content:   result.Text,
		Duration:  result.Duration,
		Timestamp: time.Now(),
	}})
}

// planReviewerFor builds the reviewer for one model. The factory field is the
// seam tests use to drive the approval flow without a provider; a session that
// has not set one gets the real reviewer, which runs as a plan-reviewer agent
// with the repository in front of it.
func (s *ChatSession) planReviewerFor(model turn.Model) (turn.Reviewer, error) {
	if s.planReviewerFactory != nil {
		return s.planReviewerFactory(model)
	}
	return &planSubagentReviewer{session: s, model: model}, nil
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

func planReviewSummary(result turn.PlanReviewResult) string {
	summary := "Plan review · " + result.Model.Display()
	if result.Duration > 0 {
		summary += fmt.Sprintf(" · %.1fs", result.Duration.Seconds())
	}
	return summary
}

func (s *ChatSession) notifyPlanReviewFailure(model turn.Model, err error) {
	if s == nil || err == nil {
		return
	}
	target := model.Display()
	if target == "" {
		target = "the selected model"
	}
	// A review the user stopped, or one that ran out of time, is not a failure
	// to report as one — the wording says what actually happened to it.
	message := ""
	switch {
	case errors.Is(err, context.Canceled):
		message = "Plan review by " + target + " was stopped."
	case errors.Is(err, context.DeadlineExceeded):
		message = "Plan review by " + target + " timed out."
	default:
		message = "Plan review by " + target + " failed: " + strings.TrimSpace(err.Error()) + "."
	}
	message += " The plan is still waiting for your decision."
	if s.chatLog != nil {
		s.chatLog.Debugf("forebrain tui plan_review failed model=%s err=%v", target, err)
	}
	s.notifyUI(NewMessageMsg{Msg: Message{
		Kind:      MsgKindError,
		Content:   message,
		Timestamp: time.Now(),
	}})
}

// planReviewTaskContext recovers what the user asked for, so the reviewer
// judges the plan against the actual request. Only user turns are used: the
// agent's own messages are how the plan came to say what it says, and feeding
// them back would have the reviewer grade the plan against its own reasoning.
func (s *ChatSession) planReviewTaskContext(ctx context.Context, actionID string) string {
	if s == nil || s.sessStore() == nil {
		return ""
	}
	sessionID := ""
	if p := s.peekPendingApproval(); p != nil {
		sessionID = strings.TrimSpace(p.SessionID)
	}
	if sessionID == "" && s.actionSvc() != nil {
		if act, err := s.actionSvc().Get(ctx, actionID); err == nil {
			sessionID = act.SessionID
		}
	}
	if sessionID == "" {
		return ""
	}
	messages, err := s.sessStore().ListTranscriptMessages(ctx, sessionID, 200)
	if err != nil {
		return ""
	}
	userTurns := make([]string, 0, planReviewTaskMessages)
	for i := len(messages) - 1; i >= 0 && len(userTurns) < planReviewTaskMessages; i-- {
		if messages[i].Role != llm.RoleUser {
			continue
		}
		text := strings.TrimSpace(messages[i].TextContent())
		if text == "" {
			continue
		}
		userTurns = append(userTurns, text)
	}
	if len(userTurns) == 0 {
		return ""
	}
	var b strings.Builder
	for i := len(userTurns) - 1; i >= 0; i-- {
		b.WriteString(userTurns[i])
		if i > 0 {
			b.WriteString("\n\n")
		}
	}
	return turn.Truncate(b.String(), turn.MaxTaskChars)
}

// planReviewerSubtype is the built-in agent definition the review run adopts.
// It carries the reviewer's system prompt and its tool policy: everything a
// plan subagent may call, minus the two plan-mode tools.
const planReviewerSubtype = run.PlanReviewSubagentType

// planSubagentReviewer runs the review as a first-class subagent: dispatched
// through the same path subagent_run uses, so it appears in the agent roster,
// owns a per-agent view the user can open from that roster, streams its
// assistant text, reasoning and tool calls into that view, and can be stopped
// from the roster row like any other subagent. The only differences are who
// asks for it (the exit-plan approval overlay) and which model answers (the one
// the user picked).
//
// It works in a worker session id of its own, never the user's session: the
// reviewer's transcript must not land in the conversation the primary agent
// replays, both because it is not the user's dialogue and because appending to
// that transcript would perturb the prompt prefix the next turn is cached on.
// Its run row, like every subagent's, is a child of the run that asked for it.
type planSubagentReviewer struct {
	session *ChatSession
	model   turn.Model
}

func (r *planSubagentReviewer) Review(ctx context.Context, req turn.Request) (turn.PlanReviewResult, error) {
	if r == nil || r.session == nil {
		return turn.PlanReviewResult{}, fmt.Errorf("nil plan reviewer session")
	}
	s := r.session
	if s.runner() == nil {
		return turn.PlanReviewResult{}, fmt.Errorf("plan review requires a runner")
	}
	if strings.TrimSpace(req.Plan) == "" {
		return turn.PlanReviewResult{}, turn.ErrNoPlan
	}
	// Fail before the run starts when the chosen model cannot be built, rather
	// than opening a roster row for a run that dies on its first LLM call.
	if _, err := run.ConfiguredModelClient(
		modelConfigFromChatSession(s), chatSessionActiveAgentName(s), r.model.Provider, r.model.Model,
	); err != nil {
		return turn.PlanReviewResult{}, err
	}

	sessionID := planReviewSessionID(s)
	// Dispatch under the conversation's session id, the way a subagent spawned
	// from inside a turn is: it is what gives the reviewer a worker session
	// derived from this conversation and keeps it in the parent's prompt-cache
	// bucket, exactly like every other subagent.
	runCtx := llm.WithAgentSessionID(ctx, sessionID)
	// The review is asked for from the approval gate of a run parked at
	// exit_plan_mode, so it is that run's subagent: its run row is a child of
	// the gated run, recorded in the conversation like every other subagent's,
	// and its tokens count toward the conversation without standing in for the
	// user's last turn.
	if p := s.peekPendingApproval(); p != nil && strings.TrimSpace(p.RunID) != "" {
		runCtx = tool.WithRunID(runCtx, p.RunID)
	}
	started := time.Now()
	text, err := s.runner().RunPlanReviewSubagent(
		runCtx,
		turn.BuildPrompt(req),
		run.SubagentModelOverride{Provider: r.model.Provider, Model: r.model.Model},
		func(runCtx context.Context, rae *tool.RequiresActionError) (context.Context, error) {
			return s.resolvePlanReviewApproval(runCtx, sessionID, rae)
		},
	)
	if err != nil {
		return turn.PlanReviewResult{}, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return turn.PlanReviewResult{}, fmt.Errorf("%s returned an empty review", r.model.Display())
	}
	return turn.PlanReviewResult{Model: r.model, Text: text, Duration: time.Since(started)}, nil
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
		SubagentType: planReviewerSubtype,
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
