// Approval over the wire: the WS projection, network approval, and the permission facade.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// approvalGateOn builds the gateway's pending-approval gate over explicit
// dependencies, so the submit-time gate (RunServeBlocking) and the
// request-time gate (the approval-request read) are one construction. The
// plan scope and review models are injected because they carry facts the
// TUI's overlay derives for itself: which plan file the gate is asking about
// and which models a review may be handed to.
//
// projects is what the plan scope resolves a conversation's project key from:
// the session's own project, never the base runner's — the gateway process has
// no launch project of its own, so the base runner's key is empty while the
// session runs on a project runner whose key is set. The plan the gate advertises
// must be the plan under the same key the session writes to.
func approvalGateOn(home string, cfg *config.Root, runner *run.Runner, runs *state.RunStore, actions *state.ActionService, projects *state.ProjectStore, sessions *state.SessionStore) *turn.PendingApprovalGate {
	gate := &turn.PendingApprovalGate{Runs: runs}
	if actions != nil {
		gate.Actions = actions
	}
	if runner != nil {
		gate.Evaluator = runner
	}
	gate.PlanScope = func(ctx context.Context, sessionID string) (string, string) {
		return config.ActiveStateRoot(home, cfg), projectKeyForSession(ctx, projects, sessionID)
	}
	// The model marked current is the one the conversation under approval runs
	// on — its own stored choice against the live config, not the base
	// runner's default, which is what the web /model switch leaves behind.
	gate.ReviewModels = func(sessionID string) []turn.PlanReviewModelOption {
		if runner == nil {
			return nil
		}
		sel := run.PrimaryModelStateForSession(runner, sessions, sessionID)
		agentName := strings.TrimSpace(runner.AgentName)
		if agentName == "" {
			agentName = "main"
		}
		return turn.PlanReviewModelOptions(cfg, agentName, sel.Provider, sel.Model)
	}
	return gate
}

// approvalGate is the gate the gateway's own handlers read through.
func (s *Server) approvalGate() *turn.PendingApprovalGate {
	if s == nil {
		return nil
	}
	return approvalGateOn(s.Home, s.modelConfig(), s.Runner, s.RunRT, s.Actions, s.Projects, s.Sessions)
}

// modelConfig is the configuration the active agent's models resolve against:
// the runner's own when it holds one, the environment's otherwise.
func (s *Server) modelConfig() *config.Root {
	if s != nil && s.Runner != nil && s.Runner.AppCfg != nil {
		return s.Runner.AppCfg
	}
	return s.liveCfg()
}

// withDetachedGatewayApprovalHooks restores the per-run surface seams for a
// continuation that no longer belongs to the request websocket which started
// it. Canonical session observers remain attached after that socket goes away,
// so tool activity and nested approvals must keep flowing through the durable
// event bus rather than falling back to process-global hooks (which would also
// cross-route concurrent conversations).
func (s *Server) withDetachedGatewayApprovalHooks(ctx context.Context, sessionID, runID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	sid := strings.TrimSpace(sessionID)
	rid := strings.TrimSpace(runID)
	writeDetached := func(message wsServerMsg) {
		for _, evt := range canonicalRunEventsFromWS(message) {
			if err := s.RunEvents().Publish(context.Background(), evt); err != nil {
				slog.Error("publish detached gateway event", "run_id", evt.RunID, "session_id", evt.SessionID, "event", evt.Type, "err", err)
			}
		}
	}
	ctx = tool.WithNetworkApprovalPromptHook(ctx, func(approvalCtx context.Context, actionID string, payload map[string]any, request safety.NetworkApprovalRequest) (safety.NetworkApprovalDecision, error) {
		executionID := strings.TrimSpace(request.ExecutionID)
		if executionID == "" {
			executionID = rid
		}
		return s.promptGatewayNetworkApproval(approvalCtx, actionID, payload, request, "", executionID, sid, writeDetached)
	})
	ctx = tool.WithSubagentApprovalHook(ctx, func(approvalCtx context.Context, rae *tool.RequiresActionError) (context.Context, error) {
		return s.promptGatewaySubagentApproval(approvalCtx, rae, "", sid, writeDetached)
	})
	ctx = tool.WithStepHook(ctx, func(stepCtx context.Context, step tool.StepEvent) {
		stepRunID := strings.TrimSpace(tool.RunIDFromContext(stepCtx))
		if stepRunID == "" {
			stepRunID = rid
		}
		stepSessionID := strings.TrimSpace(tool.ConversationSessionIDFromContext(stepCtx))
		if stepSessionID == "" {
			stepSessionID = sid
		}
		// The step's canonical events, and its turn-diff companion when the
		// call changed a file: the same pair the request socket files, so a
		// continuation past an approval leaves the same diff card behind.
		for _, canonical := range turn.CanonicalEventsFromStep(stepCtx, stepSessionID, stepRunID, "webchat", step) {
			if err := s.RunEvents().Publish(stepCtx, canonical); err != nil {
				slog.Error("publish detached gateway tool step", "run_id", stepRunID, "session_id", stepSessionID, "event", step.Kind, "err", err)
			}
		}
	})
	return ctx
}

// publishGatewayRunEvent records one lifecycle transition of a background
// continuation on the conversation's event log. The ordinary request path
// publishes through writeMsg; a detached approval resume has no such writer, so
// without this the transition would be absent from the canonical session cursor
// entirely.
func (s *Server) publishGatewayRunEvent(ctx context.Context, sessionID, runID, eventType string, payload any) error {
	if s == nil || s.RunEvents() == nil {
		return nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("publish gateway run event: run id is required")
	}
	// Every way a run ends carries its checklist facts: the worked line
	// closes every run, and it names the checklist however the run ended.
	switch ending := payload.(type) {
	case event.TurnCompletedPayload:
		ending.RunPlanFacts = s.runPlanFacts(ctx, runID)
		payload = ending
	case event.TurnCancelledPayload:
		ending.RunPlanFacts = s.runPlanFacts(ctx, runID)
		payload = ending
	case event.TurnErrorPayload:
		ending.RunPlanFacts = s.runPlanFacts(ctx, runID)
		payload = ending
	}
	return s.RunEvents().Publish(ctx, event.NewRunEvent("", runID, strings.TrimSpace(sessionID), eventType, payload, time.Now()))
}

// withApprovalToolStepID names, on a gate's approval data, the tool call the
// gate is holding (turn.ApprovalToolStepID) - the same call the terminal
// records for its own gates - so a surface can place the gate's records at
// that call. A gate whose call nothing names carries none.
func withApprovalToolStepID(data map[string]any, gate *tool.RequiresActionError) map[string]any {
	if id := turn.ApprovalToolStepID(gate); id != "" {
		data["tool_step_id"] = id
	}
	return data
}

func (s *Server) publishDetachedGatewayApprovalRequest(ctx context.Context, sessionID string, gate *tool.RequiresActionError) {
	if s == nil || gate == nil {
		return
	}
	data := approvalWSData(s.sessionPermissions(ctx, sessionID), sessionID, gate.ActionID, gate.ActionKind, gate.ToolName, gate.ToolInput)
	data["action_id"] = gate.ActionID
	data["action_kind"] = gate.ActionKind
	data["agent_id"] = gate.AgentID
	data["subagent_type"] = gate.SubagentType
	data = withApprovalToolStepID(data, gate)
	for _, evt := range canonicalRunEventsFromWS(wsServerMsg{
		Op: "requires_action", RunID: gate.RunID, SessionID: sessionID,
		Data: data, Message: "Awaiting approval or authorization to continue",
	}) {
		if err := s.RunEvents().Publish(ctx, evt); err != nil {
			slog.Error("publish detached gateway approval", "run_id", gate.RunID, "session_id", sessionID, "action_id", gate.ActionID, "err", err)
		}
	}
}

func approvalWSData(eval turn.ApprovalEvaluator, sessionID, actionID, actionKind, toolName string, toolInput any) map[string]any {
	out := map[string]any{
		"id":   strings.TrimSpace(actionID),
		"kind": strings.TrimSpace(actionKind),
	}
	request, _, _ := turn.BuildToolApprovalRequest(turn.ApprovalSource{
		SessionID: sessionID, ActionID: actionID, ToolName: toolName, ActionKind: actionKind,
		ToolInput: toolInput,
	}, eval)
	if request.PermissionToolName == "" {
		return out
	}
	item := map[string]any{
		"permission_tool_name":  request.PermissionToolName,
		"permission_input":      request.PermissionInput,
		"exact_rule_content":    request.ExactRuleContent,
		"prefix_rule_content":   request.PrefixRuleContent,
		"destination_options":   request.DestinationOptions,
		"suggested_destination": request.SuggestedDestination,
		"bypass_sandbox":        request.BypassSandbox,
		"one_shot_only":         request.OneShotOnly,
		"available_decisions":   gatewayApprovalDecisionWire(request.AvailableDecisions),
	}
	if amendment := execAmendment(request.AvailableDecisions); len(amendment) > 0 {
		item["proposed_execpolicy_amendment"] = amendment
	}
	if request.NetworkApproval != nil {
		item["network_approval_context"] = request.NetworkApproval
		if request.NetworkPort > 0 {
			item["network_port"] = request.NetworkPort
		}
		host := request.NetworkApproval.Host
		item["proposed_network_policy_amendments"] = []safety.NetworkPolicyAmendment{
			{Host: host, Action: safety.NetworkPolicyAllow},
			{Host: host, Action: safety.NetworkPolicyDeny},
		}
	}
	if eval != nil {
		item["permission_mode"] = request.PermissionMode
		item["permission_reason"] = request.PermissionReason
	}
	out["permission_suggestion"] = item
	return out
}

func execAmendment(options []safety.ApprovalDecisionOption) safety.ExecPolicyAmendment {
	for _, option := range options {
		if option.Decision == safety.DecisionAcceptWithExecPolicyAmendment {
			return option.ExecPolicyAmendment
		}
	}
	return nil
}

func gatewayApprovalDecisionWire(options []safety.ApprovalDecisionOption) []any {
	out := make([]any, 0, len(options))
	for _, option := range options {
		switch option.Decision {
		case safety.DecisionAcceptWithExecPolicyAmendment:
			out = append(out, map[string]any{string(option.Decision): map[string]any{
				"execpolicy_amendment": option.ExecPolicyAmendment,
				"command_scope":        option.CommandScope,
			}})
		case safety.DecisionAcceptAndRemember:
			// A command's row has to say what remembering it covers, and the
			// scope is the server's answer to that. Without it the web could
			// only fall back to a label that names nothing, while the terminal
			// named the prefix left free to vary.
			if len(option.CommandRules) == 0 {
				out = append(out, string(option.Decision))
				continue
			}
			out = append(out, map[string]any{string(option.Decision): map[string]any{
				"command_scope": option.CommandScope,
			}})
		case safety.DecisionApplyNetworkPolicyAmendment:
			if option.NetworkPolicyAmendment != nil {
				out = append(out, map[string]any{string(option.Decision): map[string]any{
					"network_policy_amendment": *option.NetworkPolicyAmendment,
				}})
			}
		default:
			out = append(out, string(option.Decision))
		}
	}
	return out
}

func (s *Server) promptGatewayNetworkApproval(ctx context.Context, actionID string, payload map[string]any, request safety.NetworkApprovalRequest, requestID, runID, sessionID string, writeMsg func(wsServerMsg)) (safety.NetworkApprovalDecision, error) {
	if s == nil || s.Actions == nil {
		return safety.NetworkApprovalDeny, fmt.Errorf("network approval service is unavailable")
	}
	data := approvalWSData(s.sessionPermissions(ctx, request.EnvironmentID), request.EnvironmentID, actionID, "shell", "shell", payload)
	resolved := false
	defer func() {
		if !resolved {
			_, _ = s.Actions.Cancel(context.Background(), actionID, "network approval was cancelled before a decision was returned")
		}
	}()
	writeMsg(wsServerMsg{Op: "requires_action", RequestID: requestID, RunID: runID, SessionID: sessionID, Data: data, Message: "Awaiting network approval to continue the current command"})
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		action, err := s.Actions.Get(ctx, actionID)
		if err != nil {
			return safety.NetworkApprovalDeny, err
		}
		if decision, done := turn.NetworkResult(action); done {
			resolved = true
			return decision, nil
		}
		select {
		case <-ctx.Done():
			return safety.NetworkApprovalCancel, ctx.Err()
		case <-ticker.C:
		}
	}
}

// promptGatewaySubagentApproval answers one subagent's approval gate in place,
// while the child's run and the tool call that dispatched it both stay alive,
// and returns the context that run continues under.
//
// The web UI's half of this already exists: the pending-actions list renders
// the request and its decision buttons, and the approve/deny endpoints resolve
// it. What this adds is the waiting — the same shape promptGatewayNetworkApproval
// uses, because a subagent's gate is asked from inside a live tool call for the
// same reason a sandboxed command's network gate is: there is no caller above
// it that could replay the dispatch.
func (s *Server) promptGatewaySubagentApproval(
	ctx context.Context, rae *tool.RequiresActionError,
	requestID, sessionID string, writeMsg func(wsServerMsg),
) (context.Context, error) {
	gate, ok := turn.SubagentApprovalGateFromError(rae)
	if !ok {
		return nil, fmt.Errorf("not a subagent approval gate")
	}
	if s == nil || s.Actions == nil {
		// Returning the gate unchanged leaves the caller free to fall back to
		// the unwind-and-resume path. Refusing the tool here instead would deny
		// a subagent something the primary agent is allowed to ask for.
		return nil, rae
	}
	owner := s.approvalResumeOwner()
	if s.RunRT != nil {
		if err := s.RunRT.MarkWaitResumeOwner(context.Background(), gate.RunID, gate.ActionID, owner); err != nil {
			return nil, fmt.Errorf("claim subagent approval continuation: %w", err)
		}
	}
	// Released before the request is published, not after it is answered: the
	// approve endpoint kicks off resumeGatewayRun for whichever run the wait row
	// names, which would continue this child a second time in parallel with the
	// dispatcher's own loop.
	turn.ReleaseSubagentApprovalWait(context.Background(), s.RunRT, s.runController(), gate.RunID)

	resolved := false
	defer func() {
		if !resolved {
			_, _ = s.Actions.Cancel(context.Background(), gate.ActionID, "the subagent approval was abandoned before a decision was returned")
		}
	}()
	data := approvalWSData(s.sessionPermissions(ctx, sessionID), sessionID, gate.ActionID, gate.ActionKind, gate.ToolName, gate.ToolInput)
	data["action_id"] = gate.ActionID
	data["action_kind"] = gate.ActionKind
	data["agent_id"] = gate.AgentID
	data["subagent_type"] = gate.SubagentType
	writeMsg(wsServerMsg{
		Op: "requires_action", RequestID: requestID, RunID: gate.RunID, SessionID: sessionID,
		Data: data, Message: turn.SubagentApprovalDescription(gate) + " Awaiting approval to continue.",
	})
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	lastLeaseRefresh := time.Now()
	for {
		action, err := s.Actions.Get(ctx, gate.ActionID)
		if err != nil {
			return nil, err
		}
		if action != nil && action.Status != state.ActionPending {
			if s.RunRT != nil {
				owned, ownErr := s.RunRT.WaitResumeOwnedBy(context.Background(), gate.RunID, gate.ActionID, owner)
				if ownErr != nil || !owned {
					if ownErr == nil {
						ownErr = fmt.Errorf("continuation lease was transferred")
					}
					return nil, ownErr
				}
			}
			resumeCtx, resumeErr := turn.SubagentApprovalResumeContext(
				ctx, gate, turn.BuildApprovalResume(action, gate.Session, gate.ToolName, false), "the subagent",
			)
			if resumeErr != nil {
				return nil, resumeErr
			}
			if resume := tool.ToolApprovalResumeFromContext(resumeCtx); resume != nil && s.RunRT != nil {
				// Answered, so the child is running again: the status no longer
				// rides on the execution fence, which now covers only the
				// replay a denial never reaches.
				_ = s.RunRT.SetStatus(context.Background(), gate.RunID, state.RunStatusRunning)
				turn.BindApprovalContinuationFence(resume, s.RunRT, gate.RunID, gate.ActionID, owner)
			}
			resolved = true
			return resumeCtx, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			if s.RunRT != nil && time.Since(lastLeaseRefresh) >= state.WaitResumeLease/3 {
				if err := s.RunRT.MarkWaitResumeOwner(context.Background(), gate.RunID, gate.ActionID, owner); err != nil {
					return nil, fmt.Errorf("refresh subagent approval continuation: %w", err)
				}
				lastLeaseRefresh = time.Now()
			}
		}
	}
}

func (s *Server) handleGatewayApprovalMessage(ctx context.Context, message wsClientMsg) error {
	if s == nil || s.Actions == nil {
		return fmt.Errorf("approval service is unavailable")
	}
	actionID := strings.TrimSpace(message.ActionID)
	if actionID == "" {
		return state.ErrActionNotFound
	}
	choice := strings.ToLower(strings.TrimSpace(message.ApprovalDecision))
	if message.Op == "deny_action" {
		choice = "deny"
	}
	if choice == "" {
		choice = "accept"
	}
	err := s.resolveGatewayApproval(ctx, actionID, turn.ApprovalReply{
		Choice: choice, Exec: message.ExecPolicyAmendment, Network: message.NetworkPolicyAmendment,
		Permissions: message.RequestPermissions, Reason: "via websocket",
	})
	if err != nil {
		return err
	}
	return nil
}

func (s *Server) abortGatewayRunForAction(action *state.Action) {
	if s == nil || s.RunRT == nil || action == nil || actionPayloadBool(action.PayloadJSON, "network_inline") {
		return
	}
	runID, wait, err := s.RunRT.FindRunByAction(context.Background(), action.ID)
	if err != nil || strings.TrimSpace(runID) == "" {
		return
	}
	sessionID := ""
	if runRecord, runErr := s.RunRT.GetRun(context.Background(), runID); runErr == nil && runRecord != nil {
		sessionID = runRecord.SessionID
	}
	// The primary agent's own gate records the refusal before the run ends:
	// one rejection-stopped continuation writes the refusal results onto the
	// parked calls and ends the turn, and the run then finishes cancelled
	// exactly as a direct cancel would (plan 129). A subagent's own gate, and
	// legacy wait rows with no snapshot to record a refusal on, keep the
	// direct cancel.
	if agentID, _ := turn.ActionSubagent(action); agentID == "" && wait != nil && len(wait.SessionSnapshot) > 0 && s.Runner != nil && s.Core != nil {
		go s.resumeGatewayRejectedStop(action.ID, runID, wait)
		return
	}
	// The run was parked on this approval, so nothing else will end it.
	s.cancelGatewayRunDirect(sessionID, runID)
}

// cancelGatewayRunDirect is the direct ending a cancelled approval's run has
// always had: stop the controller entry, clear the wait, record the
// cancellation, and report it.
func (s *Server) cancelGatewayRunDirect(sessionID, runID string) {
	if s == nil || s.RunRT == nil {
		return
	}
	ctx := context.Background()
	s.runController().Cancel(runID, context.Canceled)
	_ = s.RunRT.ClearWait(ctx, runID)
	turn.FinalizeCancel(ctx, s.RunRT, runID)
	s.finishRun(ctx, sessionID, runID, state.RunStatusCancelled)
	_ = s.publishGatewayRunEvent(ctx, sessionID, runID, event.RunEventTurnCancelled, event.TurnCancelledPayload{Message: "cancelled"})
}

// resumeGatewayRejectedStop is the gateway's refusal-without-words
// continuation: one resume records the refusal on the parked call — the same
// session the approved/denied continuations dispatch under — and the turn
// ends there with run.ErrTurnStoppedByRejection, which finishes the run the
// way a direct cancel does (plan 129). Structured after resumeGatewayRun.
func (s *Server) resumeGatewayRejectedStop(actionID, runID string, w *state.Wait) {
	if s == nil || s.RunRT == nil || s.Runner == nil || w == nil {
		return
	}
	ctx := context.Background()
	rn, err := s.RunRT.GetRun(ctx, runID)
	if err != nil || rn == nil {
		s.cancelGatewayRunDirect("", runID)
		return
	}
	sid := strings.TrimSpace(rn.SessionID)
	if sid == "" {
		sid = "default"
	}
	claimed, claimErr := s.RunRT.ClaimWaitResume(ctx, runID, actionID, s.approvalResumeOwner())
	if claimErr != nil || !claimed {
		return
	}
	if !s.runController().ClaimResume(runID, rn.SessionID) {
		_ = s.RunRT.ReleaseWaitResumeOwner(ctx, runID, actionID, s.approvalResumeOwner())
		return
	}
	_ = s.RunRT.SetStatus(ctx, runID, state.RunStatusRunning)

	resumeCh := "webchat"
	ctx2 := llm.WithAgentSessionID(ctx, sid)
	ctx2 = tool.WithConversationSessionID(ctx2, sid)
	ctx2 = tool.WithRunID(ctx2, runID)
	if q, _, ok := s.runController().Queue(runID); ok && q.Runtime() != nil {
		ctx2 = run.WithTurnInputRuntime(ctx2, q.Runtime())
	}
	resumeState := turn.ResumeRejectedStop(w.SessionSnapshot)
	// The fence crosses with the resume itself, exactly as the approved and
	// denied continuations cross theirs.
	turn.BindApprovalContinuationFence(resumeState, s.RunRT, runID, actionID, s.approvalResumeOwner())
	ctx2 = tool.WithToolApprovalResume(ctx2, resumeState)
	ctx2 = process.AgentContextForProject(ctx2, s.stateRoot(), sid, s.sessionProjectKey(ctx2, sid))
	ctx2 = s.withDetachedGatewayApprovalHooks(ctx2, sid, runID)
	var streamed bool
	ctx2 = s.withAnswerStream(ctx2, runID, sid, &streamed, &turn.StreamPartial{})
	_, runErr := s.Core.Submit(ctx2, turn.TurnRequest{
		SessionID:     rn.SessionID,
		Origin:        turn.Origin{Surface: turn.SurfaceWebChat, ChannelID: resumeCh},
		Trigger:       "resume",
		UserText:      rn.InputText,
		ExistingRunID: runID,
	}, nil)
	if runErr != nil {
		var rae *tool.RequiresActionError
		if errors.As(runErr, &rae) {
			// A gate the stopped resume still raised: the turn stays open on
			// it, the same suspension any continuation's gate is.
			s.publishDetachedGatewayApprovalRequest(ctx, sid, rae)
			return
		}
		if !errors.Is(runErr, run.ErrTurnStoppedByRejection) {
			slog.Error("resume rejected stop after action", "run_id", runID, "action_id", actionID, "err", runErr)
		}
	}
	// The refusal is recorded and the turn has ended: close the run the way
	// a direct cancel always closed it.
	s.cancelGatewayRunDirect(sid, runID)
}

// expireGatewayApproval turns TTL expiry into a first-class, replayable
// decision. A live in-place subagent still owns its continuation loop, which
// observes ActionExpired and closes the child normally; a parked/restarted run
// is finalized here so it cannot remain waiting forever.
func (s *Server) expireGatewayApproval(ctx context.Context, actionID string) {
	if s == nil || s.Actions == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	action, err := s.Actions.Get(ctx, strings.TrimSpace(actionID))
	if err != nil || action == nil || action.Status != state.ActionExpired {
		return
	}
	runID := ""
	var wait *state.Wait
	if s.RunRT != nil {
		runID, wait, _ = s.RunRT.FindRunByAction(ctx, action.ID)
	}
	sessionID := strings.TrimSpace(action.SessionID)
	var runRecord *state.Run
	if s.RunRT != nil && strings.TrimSpace(runID) != "" {
		runRecord, _ = s.RunRT.GetRun(ctx, runID)
		if sessionID == "" && runRecord != nil {
			sessionID = strings.TrimSpace(runRecord.SessionID)
		}
	}
	agentID, subagentType := turn.ActionSubagent(action)
	if wait != nil {
		if agentID == "" {
			agentID = strings.TrimSpace(wait.AgentID)
		}
		if subagentType == "" {
			subagentType = strings.TrimSpace(wait.SubagentType)
		}
	}
	reason := strings.TrimSpace(action.Error)
	if reason == "" {
		reason = "approval ttl expired"
	}
	_ = s.RunEvents().Publish(ctx, event.NewRunEvent(
		"approval-resolved:"+action.ID+":"+string(action.Status), runID, sessionID,
		event.RunEventApprovalResolved, event.ApprovalResolvedPayload{
			ActionID: action.ID, ActionKind: action.Kind, Decision: string(action.Status), Reason: reason,
			AgentID: agentID, SubagentType: subagentType,
		}, time.Now(),
	))
	if strings.TrimSpace(runID) == "" || s.RunRT == nil {
		return
	}
	if agentID != "" {
		if active, ok := s.runController().Get(runID); ok && active.Phase() == run.Running {
			// promptGatewaySubagentApproval observes the durable expiration on its
			// next poll and lets the ordinary child lifecycle persist its failure.
			return
		}
	}
	_ = s.RunRT.ClearWait(ctx, runID)
	s.runController().Cancel(runID, fmt.Errorf("%s", reason))
	s.finishRun(ctx, sessionID, runID, state.RunStatusFailed)
	if agentID != "" {
		parentRunID := ""
		if runRecord != nil {
			parentRunID = strings.TrimSpace(runRecord.ParentRunID)
		}
		_ = s.RunEvents().Publish(ctx, event.NewRunEvent(
			"approval-expired:"+action.ID, runID, sessionID, event.RunEventSubagentEnded,
			event.SubagentEndedPayload{
				AgentID: agentID, AgentType: subagentType, TaskID: agentID, Status: "failed", Error: reason,
				ParentRunID: parentRunID, ExecutionID: runID,
			}, time.Now(),
		))
	} else {
		_ = s.publishGatewayRunEvent(ctx, sessionID, runID, event.RunEventTurnError, event.TurnErrorPayload{Error: reason, Message: reason})
	}
}

// sessionPermissions judges calls for one conversation the way the runner it
// runs on does — for a project session, that project's pooled runner. The
// process facade is bound to the gateway's own runner and answers for the
// gateway's launch project, so an approval card built from it would offer a
// project session choices its own rules never produced.
func (s *Server) sessionPermissions(ctx context.Context, sessionID string) turn.ApprovalEvaluator {
	runner := s.runnerFor(ctx, sessionID)
	if runner == nil {
		return nil
	}
	return runner
}
