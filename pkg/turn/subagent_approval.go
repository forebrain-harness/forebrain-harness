// Answering a subagent's approval gate in place, shared by every surface.
package turn

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// A subagent's approval gate is answered where the subagent is running, not by
// unwinding the turn that dispatched it. The primary agent can unwind, because
// there is always a surface above it able to replay the turn; a subagent
// dispatched from inside a live tool call has no such caller. subagent_fanout
// is the case that forces the rule: it is holding every sibling task's work
// while one child waits, and nothing above it can replay the dispatch, so
// unwinding one child's question discards all of the fanout's work.
//
// This file is the half of that flow no surface may own privately: which run is
// parked, how the request is described, how the park is released so nothing
// resumes the child twice, and what context the child continues under. Each
// surface supplies only how it puts a question to its user — an overlay in the
// terminal, a websocket message and the answer that comes back in the web UI.

// SubagentApprovalGate is one subagent's paused tool call, reduced to what a
// surface needs to ask about it and what the runtime needs to continue it.
type SubagentApprovalGate struct {
	ActionID string
	// RunID is the subagent's own run — the one that is parked. It is not the
	// run driving the dispatching turn, which keeps running throughout.
	RunID        string
	ToolName     string
	ActionKind   string
	ToolInput    any
	AgentID      string
	SubagentType string
	// Session is the child's orchestration snapshot at the moment it parked,
	// which is what the resume replays.
	Session []llm.Message
}

// SubagentApprovalGateFromError projects the runtime's approval gate onto the
// surface-facing shape. It reports false for anything that is not a gate.
func SubagentApprovalGateFromError(err error) (SubagentApprovalGate, bool) {
	var rae *tool.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		return SubagentApprovalGate{}, false
	}
	return SubagentApprovalGate{
		ActionID:     strings.TrimSpace(rae.ActionID),
		RunID:        strings.TrimSpace(rae.RunID),
		ToolName:     strings.TrimSpace(rae.ToolName),
		ActionKind:   strings.TrimSpace(rae.ActionKind),
		ToolInput:    rae.ToolInput,
		AgentID:      strings.TrimSpace(rae.AgentID),
		SubagentType: strings.TrimSpace(rae.SubagentType),
		Session:      rae.SessionSnapshot,
	}, true
}

// SubagentApprovalDescription names the worker asking, for the line a surface
// shows above the request.
func SubagentApprovalDescription(gate SubagentApprovalGate) string {
	who := strings.TrimSpace(gate.SubagentType)
	if who == "" {
		who = "a subagent"
	}
	return "Requested by " + who + "."
}

// SubagentApprovalRequest describes one tool a subagent wants to run, in the
// shape every approval surface renders.
//
// sessionID is the conversation that dispatched the child, never the child's
// own worker session: a grant the user makes here is their decision for this
// conversation, while the worker session is an implementation detail that
// disappears when the task ends.
func SubagentApprovalRequest(gate SubagentApprovalGate, sessionID string, eval ApprovalEvaluator) ToolApprovalRequest {
	req, _, _ := BuildToolApprovalRequest(ApprovalSource{
		SessionID:    strings.TrimSpace(sessionID),
		ActionID:     gate.ActionID,
		RunID:        gate.RunID,
		ToolName:     gate.ToolName,
		ActionKind:   gate.ActionKind,
		ToolInput:    gate.ToolInput,
		AgentID:      gate.AgentID,
		SubagentType: gate.SubagentType,
	}, eval)
	req.Description = SubagentApprovalDescription(gate)
	return req
}

// SubagentRunPhases is the run-phase half of releasing a park: the in-memory
// phase a resume claim tests against. Declared as an interface so this package
// needs nothing from the runtime that owns it.
type SubagentRunPhases interface {
	// Resume moves a waiting run back to running, reporting whether it moved.
	Resume(runID string) bool
}

// ReleaseSubagentApprovalWait gives the live dispatcher the in-memory resume
// claim without deleting the durable wait. The controller CAS prevents the
// HTTP/WS/TUI generic resume path from starting a second continuation; keeping
// the row preserves the snapshot if the process dies after the decision but
// before the child safely crosses the gate. The durable run stays
// waiting_action through this release, because the prompt is still unanswered
// here and a refresh would otherwise present the child as running; the surface
// moves it back to running once a decision is in hand.
func ReleaseSubagentApprovalWait(ctx context.Context, runs *state.RunStore, phases SubagentRunPhases, runID string) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	_ = ctx
	_ = runs
	if phases != nil {
		phases.Resume(runID)
	}
}

// SubagentApprovalResumeContext returns the context the parked subagent run
// continues under once its gate has been answered.
//
// A denial is an answer, not a failure: the child is resumed with the denial
// and the user's feedback so it can adapt, exactly as the primary agent is. A
// cancellation is the only outcome that ends the run, because there is no
// answer to continue from.
func SubagentApprovalResumeContext(
	ctx context.Context, gate SubagentApprovalGate, resume ApprovalResume, who string,
) (context.Context, error) {
	if !resume.Approved && !resume.Denied {
		return nil, fmt.Errorf("%s was cancelled while waiting for tool approval", strings.TrimSpace(who))
	}
	session := resume.Session
	if len(session) == 0 {
		session = gate.Session
	}
	out := tool.WithApprovedActionID(ctx, gate.ActionID)
	return tool.WithToolApprovalResume(out, &tool.ToolApprovalResumeState{
		Session:    session,
		Denied:     resume.Denied,
		DenyReason: resume.Reason,
	}), nil
}
