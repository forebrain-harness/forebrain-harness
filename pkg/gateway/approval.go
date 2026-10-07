// Approval over the wire: the WS projection, network approval, and the permission facade.
package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
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
func approvalGateOn(home string, cfg *config.Root, runner *run.Runner, runs *state.RunStore, actions *state.ActionService) *turn.PendingApprovalGate {
	gate := &turn.PendingApprovalGate{Runs: runs}
	if actions != nil {
		gate.Actions = actions
	}
	if runner != nil {
		gate.Evaluator = runner
	}
	gate.PlanScope = func(context.Context, string) (string, string) {
		projectKey := ""
		if runner != nil {
			projectKey = strings.TrimSpace(runner.ProjectKey)
		}
		return config.ActiveStateRoot(home, cfg), projectKey
	}
	gate.ReviewModels = func() []turn.PlanReviewModelOption {
		if runner == nil {
			return nil
		}
		provider, model := run.PrimaryModel(runner)
		agentName := strings.TrimSpace(runner.AgentName)
		if agentName == "" {
			agentName = "main"
		}
		return turn.PlanReviewModelOptions(cfg, agentName, provider, model)
	}
	return gate
}

// approvalGate is the gate the gateway's own handlers read through.
func (s *Server) approvalGate() *turn.PendingApprovalGate {
	if s == nil {
		return nil
	}
	return approvalGateOn(s.Home, s.modelConfig(), s.Runner, s.RunRT, s.Actions)
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
		if canonical, ok := tool.RunEventFromStep(stepCtx, stepSessionID, stepRunID, "webchat", step); ok {
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

func (s *Server) publishDetachedGatewayApprovalRequest(ctx context.Context, sessionID string, gate *tool.RequiresActionError) {
	if s == nil || gate == nil {
		return
	}
	data := approvalWSData(s.sessionPermissions(ctx, sessionID), sessionID, gate.ActionID, gate.ActionKind, gate.ToolName, gate.ToolInput)
	data["action_id"] = gate.ActionID
	data["action_kind"] = gate.ActionKind
	data["agent_id"] = gate.AgentID
	data["subagent_type"] = gate.SubagentType
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
	runID, _, err := s.RunRT.FindRunByAction(context.Background(), action.ID)
	if err != nil || strings.TrimSpace(runID) == "" {
		return
	}
	s.runController().Cancel(runID, context.Canceled)
	ctx := context.Background()
	_ = s.RunRT.ClearWait(ctx, runID)
	turn.FinalizeCancel(ctx, s.RunRT, runID)
	sessionID := ""
	if runRecord, runErr := s.RunRT.GetRun(ctx, runID); runErr == nil && runRecord != nil {
		sessionID = runRecord.SessionID
	}
	// The run was parked on this approval, so nothing else will end it.
	s.finishRun(ctx, sessionID, runID, state.RunStatusCancelled)
	_ = s.publishGatewayRunEvent(ctx, sessionID, runID, event.RunEventTurnCancelled, event.TurnCancelledPayload{Message: "cancelled"})
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
