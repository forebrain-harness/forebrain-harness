package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// TestMain puts the segmentation dictionary beside the test binary: the memory
// tools search through it, and read it from there as forebrain does.
func TestMain(m *testing.M) {
	executable, err := os.Executable()
	if err == nil {
		err = memory.InstallDictionary(filepath.Dir(executable))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "install segmentation dictionary:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func withSession(ctx context.Context, sid string) context.Context {
	return llm.WithAgentSessionID(ctx, sid)
}

func approvedExitPlanState() *State {
	st := &State{}
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		return "approved-exit", false, nil
	})
	return st
}

func TestEnterPlanModeRaisesRequiresAction(t *testing.T) {
	home := t.TempDir()
	st := &State{}
	st.SetActionHook(func(ctx context.Context, kind string, payload any) (string, bool, error) {
		return "act-1", true, nil
	})
	tool, err := newEnterPlanModeTool(st, home)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := withSession(context.Background(), "sid-1")
	_, err = tool.Handle(ctx, `{}`)
	if err == nil {
		t.Fatal("expected RequiresActionError")
	}
	var rae *RequiresActionError
	if !errors.As(err, &rae) {
		t.Fatalf("not RequiresActionError: %T %v", err, err)
	}
	if rae.ActionKind != "enter_plan_mode" {
		t.Fatalf("kind=%q", rae.ActionKind)
	}
}

func TestEnterPlanModeStashesPrePlanMode(t *testing.T) {
	home := t.TempDir()
	sid := "sid-stash"
	// seed pre-state: agent mode
	if err := state.Set(home, sid, state.State{Mode: state.ModeAgent}); err != nil {
		t.Fatal(err)
	}
	st := &State{}
	tool, err := newEnterPlanModeTool(st, home)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := WithApprovedActionID(withSession(context.Background(), sid), "approved-x")
	raw, err := tool.Handle(ctx, `{}`)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	got, _ := raw.(string)
	if !strings.Contains(got, `"mode":"plan"`) {
		t.Fatalf("payload=%s", got)
	}
	final, _ := state.Get(home, sid)
	if final.Mode != state.ModePlan {
		t.Fatalf("mode=%v", final.Mode)
	}
	if final.PrePlanMode != state.ModeAgent {
		t.Fatalf("PrePlanMode=%v", final.PrePlanMode)
	}
	if final.PlanTurnCount != 0 {
		t.Fatalf("turn=%d", final.PlanTurnCount)
	}
	if got := ModeFromContext(st.ContextWithRuntimeSessionMode(withSession(context.Background(), sid))); got != "plan" {
		t.Fatalf("runtime mode=%q want plan", got)
	}
}

func TestEnterPlanModeReturnsPlanFile(t *testing.T) {
	home := t.TempDir()
	sid := "sid-pf"
	st := &State{}
	tool, _ := newEnterPlanModeTool(st, home)
	ctx := WithApprovedActionID(withSession(context.Background(), sid), "ok")
	raw, err := tool.Handle(ctx, `{"reason":"Need to plan implementation steps"}`)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	var obj map[string]any
	_ = json.Unmarshal([]byte(raw.(string)), &obj)
	// When no plan exists yet, plan_file must be empty (not plan.md) so the
	// LLM is forced to choose a descriptive name instead of overwriting.
	pf, _ := obj["plan_file"].(string)
	if pf != "" {
		t.Fatalf("plan_file should be empty when no plan exists, got %q", pf)
	}
	ns, _ := obj["next_steps"].(string)
	if !strings.Contains(ns, "descriptive-name") {
		t.Fatalf("next_steps should mention descriptive-name: %q", ns)
	}
	if reason, _ := obj["reason"].(string); reason != "Need to plan implementation steps" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestEnterPlanModeReturnsExistingPlanFile(t *testing.T) {
	home := t.TempDir()
	sid := "sid-pf-existing"
	// Seed an existing descriptive-named plan file.
	planDir := state.PlanDirForSession(home, "", sid)
	existingPath := filepath.Join(planDir, "add-feature-x.md")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existingPath, []byte("# Plan\nexisting content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := &State{}
	tool, _ := newEnterPlanModeTool(st, home)
	ctx := WithApprovedActionID(withSession(context.Background(), sid), "ok")
	raw, err := tool.Handle(ctx, `{}`)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	var obj map[string]any
	_ = json.Unmarshal([]byte(raw.(string)), &obj)
	// When a plan exists, plan_file should be the actual file path (not plan.md).
	pf, _ := obj["plan_file"].(string)
	if pf == "" || !strings.HasSuffix(pf, "add-feature-x.md") {
		t.Fatalf("plan_file should be the existing descriptive-named file, got %q", pf)
	}
}

func TestExitPlanModeRestoresPrePlanMode(t *testing.T) {
	home := t.TempDir()
	sid := "sid-restore"
	_ = state.Set(home, sid, state.State{
		Mode:          state.ModePlan,
		Phase:         "plan",
		PrePlanMode:   state.ModeAgent,
		PlanTurnCount: 7,
	})
	st := approvedExitPlanState()
	tool, _ := newExitPlanModeTool(st, home)
	ctx := withSession(context.Background(), sid)
	raw, err := tool.Handle(ctx, `{}`)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	got, _ := raw.(string)
	if !strings.Contains(got, `"mode":"agent"`) {
		t.Fatalf("payload=%s", got)
	}
	final, _ := state.Get(home, sid)
	if final.Mode != state.ModeAgent {
		t.Fatalf("mode=%v", final.Mode)
	}
	if got := ModeFromContext(st.ContextWithRuntimeSessionMode(withSession(context.Background(), sid))); got != "agent" {
		t.Fatalf("runtime mode=%q want agent", got)
	}
	if string(final.PrePlanMode) != "" {
		t.Fatalf("PrePlanMode not cleared: %v", final.PrePlanMode)
	}
	if final.PlanTurnCount != 0 {
		t.Fatalf("turn count not cleared: %d", final.PlanTurnCount)
	}
}

func TestExitPlanModeOnRequestsToAgent(t *testing.T) {
	home := t.TempDir()
	sid := "sid-default"
	_ = state.Set(home, sid, state.State{
		Mode: state.ModePlan,
		// PrePlanMode left empty
	})
	st := approvedExitPlanState()
	tool, _ := newExitPlanModeTool(st, home)
	ctx := withSession(context.Background(), sid)
	if _, err := tool.Handle(ctx, `{}`); err != nil {
		t.Fatalf("handle: %v", err)
	}
	final, _ := state.Get(home, sid)
	if final.Mode != state.ModeAgent {
		t.Fatalf("default restore expected agent, got %v", final.Mode)
	}
}

func TestExitPlanModeSetsHasExitedPlan(t *testing.T) {
	home := t.TempDir()
	sid := "sid-exited-flag"
	_ = state.Set(home, sid, state.State{
		Mode:          state.ModePlan,
		PrePlanMode:   state.ModeAgent,
		PlanTurnCount: 3,
	})
	st := approvedExitPlanState()
	tool, _ := newExitPlanModeTool(st, home)
	ctx := withSession(context.Background(), sid)
	raw, err := tool.Handle(ctx, `{}`)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	got, _ := raw.(string)
	if !strings.Contains(got, "Exited plan mode") {
		t.Fatalf("exit payload missing confirmation message: %q", got)
	}
	final, _ := state.Get(home, sid)
	if !final.HasExitedPlan {
		t.Fatalf("HasExitedPlan should be true after exit")
	}
}

func TestEnterPlanModeDetectsReentry(t *testing.T) {
	home := t.TempDir()
	sid := "sid-reentry"
	// Seed state: previously exited plan mode, and a plan file exists
	_ = state.Set(home, sid, state.State{
		Mode:          state.ModeAgent,
		HasExitedPlan: true,
	})
	_ = state.SetPlanForSession(home, "", sid, "# Previous Plan\n\nold content")
	st := &State{}
	tool, _ := newEnterPlanModeTool(st, home)
	ctx := WithApprovedActionID(withSession(context.Background(), sid), "ok")
	raw, err := tool.Handle(ctx, `{}`)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	got, _ := raw.(string)
	if !strings.Contains(got, "Re-entering plan mode") {
		t.Fatalf("reentry next_steps missing re-entry guidance: %q", got)
	}
	if !strings.Contains(got, "read the existing plan file") {
		t.Fatalf("reentry next_steps should guide to read existing plan: %q", got)
	}
	final, _ := state.Get(home, sid)
	if final.HasExitedPlan {
		t.Fatalf("HasExitedPlan should be cleared on enter")
	}
}

func TestEnterPlanModeNoReentryWithoutExit(t *testing.T) {
	home := t.TempDir()
	sid := "sid-no-reentry"
	// First entry: HasExitedPlan is false
	_ = state.SetPlanForSession(home, "", sid, "# Previous Plan\n\nold content")
	st := &State{}
	tool, _ := newEnterPlanModeTool(st, home)
	ctx := WithApprovedActionID(withSession(context.Background(), sid), "ok")
	raw, err := tool.Handle(ctx, `{}`)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	got, _ := raw.(string)
	if strings.Contains(got, "Re-entering plan mode") {
		t.Fatalf("first entry should NOT have re-entry guidance: %q", got)
	}
}

func TestExitPlanModeRestoresModeNoPrompts(t *testing.T) {
	home := t.TempDir()
	sid := "sid-rules"
	_ = state.Set(home, sid, state.State{
		Mode:        state.ModePlan,
		PrePlanMode: state.ModeAgent,
	})
	st := approvedExitPlanState()
	tool, _ := newExitPlanModeTool(st, home)
	ctx := withSession(context.Background(), sid)
	raw, err := tool.Handle(ctx, `{}`)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	out, _ := raw.(string)
	if !strings.Contains(out, `"mode":"agent"`) {
		t.Fatalf("expected agent mode in output: %s", out)
	}
	final, _ := state.Get(home, sid)
	if final.Mode != state.ModeAgent {
		t.Fatalf("mode=%v want agent", final.Mode)
	}
}

// TestExitPlanModeReportsOwnSessionPlan pins the isolation at the tool layer:
// after approval the model is told to implement the plan of ITS conversation,
// never a plan another session wrote more recently.
func TestExitPlanModeReportsOwnSessionPlan(t *testing.T) {
	home := t.TempDir()
	sid := "sid-own"
	_ = state.Set(home, sid, state.State{
		Mode:        state.ModePlan,
		PrePlanMode: state.ModeAgent,
	})
	if err := state.SetPlanForSession(home, "", sid, "# Mine"); err != nil {
		t.Fatalf("seed own plan: %v", err)
	}
	// Another conversation's plan, written later: must stay invisible.
	otherDir := state.PlanDirForSession(home, "", "sid-other")
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	theirs := filepath.Join(otherDir, "theirs.md")
	if err := os.WriteFile(theirs, []byte("# Theirs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(theirs, future, future); err != nil {
		t.Fatal(err)
	}

	st := approvedExitPlanState()
	tool, _ := newExitPlanModeTool(st, home)
	raw, err := tool.Handle(withSession(context.Background(), sid), `{}`)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	out, _ := raw.(string)
	want := state.PlanPathForSession(home, "", sid)
	if !strings.Contains(out, want) {
		t.Fatalf("output should name the session's own plan %q: %s", want, out)
	}
	if strings.Contains(out, "theirs.md") {
		t.Fatalf("output must not mention another session's plan: %s", out)
	}
}

func TestExitPlanModeRaisesRequiresActionWithPayload(t *testing.T) {
	home := t.TempDir()
	sid := "sid-need-approval"
	st := &State{}
	var capturedPayload any
	st.SetActionHook(func(ctx context.Context, kind string, payload any) (string, bool, error) {
		capturedPayload = payload
		return "act-2", true, nil
	})
	tool, _ := newExitPlanModeTool(st, home)
	ctx := withSession(context.Background(), sid)
	_, err := tool.Handle(ctx, `{"plan_file":"/tmp/plan.md"}`)
	var rae *RequiresActionError
	if err == nil || !errors.As(err, &rae) {
		t.Fatalf("expected RequiresActionError: %v", err)
	}
	if rae.ActionKind != "exit_plan_mode" {
		t.Fatalf("kind=%q", rae.ActionKind)
	}
	m, _ := capturedPayload.(map[string]any)
	if v, ok := m["action"]; !ok || v != "exit" {
		t.Fatalf("payload missing action=exit: %+v", capturedPayload)
	}
	if v, ok := m["session_id"]; !ok || v != "sid-need-approval" {
		t.Fatalf("payload missing session_id: %+v", capturedPayload)
	}
	if v, ok := m["force_tool_approval"]; !ok || v != true {
		t.Fatalf("payload missing forced approval flag: %+v", capturedPayload)
	}
	if v, ok := m["approval_reason"]; !ok || v != "exit_plan_mode_requires_user_approval" {
		t.Fatalf("payload missing approval reason: %+v", capturedPayload)
	}
}

func TestExitPlanModeFailsClosedWithoutApprovalHook(t *testing.T) {
	home := t.TempDir()
	sid := "sid-no-hook"
	if err := state.Set(home, sid, state.State{Mode: state.ModePlan, PrePlanMode: state.ModeAgent}); err != nil {
		t.Fatal(err)
	}
	tool, _ := newExitPlanModeTool(&State{}, home)
	_, err := tool.Handle(withSession(context.Background(), sid), `{}`)
	if err == nil || !strings.Contains(err.Error(), "approval unavailable") {
		t.Fatalf("expected approval-unavailable error, got %v", err)
	}
	final, _ := state.Get(home, sid)
	if final.Mode != state.ModePlan {
		t.Fatalf("exit without hook changed mode to %v", final.Mode)
	}
}

func TestExitPlanModeFailsClosedForNoopApprovalHook(t *testing.T) {
	home := t.TempDir()
	sid := "sid-noop-hook"
	if err := state.Set(home, sid, state.State{Mode: state.ModePlan, PrePlanMode: state.ModeAgent}); err != nil {
		t.Fatal(err)
	}
	st := &State{}
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		return "", false, nil
	})
	tool, _ := newExitPlanModeTool(st, home)
	_, err := tool.Handle(withSession(context.Background(), sid), `{}`)
	if err == nil || !strings.Contains(err.Error(), "approval unavailable") {
		t.Fatalf("expected approval-unavailable error, got %v", err)
	}
	final, _ := state.Get(home, sid)
	if final.Mode != state.ModePlan {
		t.Fatalf("exit without approval changed mode to %v", final.Mode)
	}
}

func TestEnterPlanModeNoSessionFails(t *testing.T) {
	home := t.TempDir()
	st := &State{}
	tool, _ := newEnterPlanModeTool(st, home)
	if _, err := tool.Handle(context.Background(), `{}`); err == nil {
		t.Fatal("expected error when session not bound")
	}
}

type sessionToolCallLLM struct {
	calls int
}

func (m *sessionToolCallLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	m.calls++
	switch m.calls {
	case 1:
		msg := llm.AssistantMessage(nil, llm.ToolCall{
			ID:   "todo-call",
			Type: llm.ToolTypeFunction,
			Function: llm.FunctionCall{
				Name:      "session_todo",
				Arguments: `{"action":"set","items":[{"id":"review","content":"Review commit","status":"in_progress"}]}`,
			},
		})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	default:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
}

// The plan update the engine publishes names its in-flight task the one way
// every surface reports it: the payload's Active is the shared rule's answer
// for the same items, so the web's working and worked lines and the
// terminal's agree without each deriving it again.
func TestPlanUpdatePayloadActiveMatchesTheSharedRule(t *testing.T) {
	var captured *StepEvent
	st := NewState(t.TempDir())
	st.SetStepHook(func(_ context.Context, evt StepEvent) {
		captured = &evt
	})
	list := state.List{Items: []state.Item{
		{ID: "one", Content: "Read the diff", Status: state.StatusCompleted},
		{ID: "two", Content: "Fix the reducer", Title: "reducer", Status: state.StatusInProgress},
		{ID: "three", Content: "Add a test", Title: "a much longer in-flight title", Status: state.StatusInProgress},
		{ID: "four", Content: "Ship it", Status: state.StatusPending},
	}}

	emitPlanUpdateStep(context.Background(), st, list)
	if captured == nil || captured.PlanUpdate == nil {
		t.Fatalf("no plan update step was emitted: %#v", captured)
	}
	payload := *captured.PlanUpdate
	want := event.PlanProgressOf(payload.Items, payload.Completed, payload.Total, payload.Explanation).Active
	if payload.Active != want {
		t.Fatalf("payload active = %q, want the shared rule's %q", payload.Active, want)
	}
	if payload.Active != "reducer" {
		t.Fatalf("payload active = %q, want the shortest in-progress title", payload.Active)
	}
}

func TestRegisterDefaultToolsRegistersSessionToolsWithWorkspaceRootOnlyRuntime(t *testing.T) {
	workspaceRoot := t.TempDir()
	sid := "workspace-root-only"
	st := NewState(workspaceRoot)
	llmClient := &sessionToolCallLLM{}
	a, err := agent.New(llmClient, "test", "test")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	rt := &AgentToolRuntime{WorkspaceRoot: workspaceRoot}
	if err := RegisterDefaultTools(a, st, rt); err != nil {
		t.Fatalf("RegisterDefaultTools: %v", err)
	}

	ctx := llm.WithAgentSessionID(context.Background(), sid)
	res, err := a.Run(ctx, llm.Text("track progress"))
	if err != nil {
		t.Fatalf("agent run should execute session_todo with workspace-root-only runtime: %v", err)
	}
	if strings.TrimSpace(res.TextContent()) != "done" {
		t.Fatalf("unexpected final text %q", res.TextContent())
	}
	todos, err := state.Load(workspaceRoot, sid)
	if err != nil {
		t.Fatalf("load todos: %v", err)
	}
	if len(todos.Items) != 1 || todos.Items[0].ID != "review" {
		t.Fatalf("session_todo did not persist under workspace root: %+v", todos.Items)
	}
}
