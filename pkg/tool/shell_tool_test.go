package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

func TestSandboxDenialReasonFallsBackToExecutionError(t *testing.T) {
	reason := sandboxDenialReason(safety.CommandResult{}, errors.New("permission denied"))
	if reason != "permission denied" {
		t.Fatalf("fallback denial reason = %q", reason)
	}
}

func TestSandboxDenialReasonUsesConcreteDeniedOutputLine(t *testing.T) {
	res := safety.CommandResult{Stderr: "--- FAIL: TestOAuth\nlistener_test.go:28: listen tcp 127.0.0.1:0: bind: operation not permitted\nFAIL"}
	want := "listener_test.go:28: listen tcp 127.0.0.1:0: bind: operation not permitted"
	if got := sandboxDenialReason(res, nil); got != want {
		t.Fatalf("sandboxDenialReason() = %q, want %q", got, want)
	}
	reason := sandboxRetryApprovalReason(want)
	for _, part := range []string{"sandbox blocked", "127.0.0.1:0", "outside the sandbox"} {
		if !strings.Contains(reason, part) {
			t.Fatalf("approval reason %q does not contain %q", reason, part)
		}
	}
}

func TestSandboxRetryApprovalPreservesAttemptAndConcreteReason(t *testing.T) {
	st := NewState(t.TempDir())
	var actionPayload map[string]any
	st.SetActionHook(func(ctx context.Context, kind string, payload any) (string, bool, error) {
		if ApprovedActionIDFromContext(ctx) != "" {
			t.Fatalf("sandbox retry must request a fresh approval context")
		}
		if kind != "shell" {
			t.Fatalf("action kind = %q, want shell", kind)
		}
		actionPayload, _ = payload.(map[string]any)
		return "act-sandbox-retry", true, nil
	})
	ctx := WithToolCompletionCapture(WithApprovedActionID(context.Background(), "stale-action"))
	denial := "listener_test.go:28: listen tcp 127.0.0.1:0: bind: operation not permitted"
	res := safety.CommandResult{Stderr: denial, ExitCode: 1}
	authorized, err := requestSandboxRetryApproval(ctx, st, nil, sandboxedShellRequest{
		command: "go test ./... -count=1", timeoutMs: 120000,
		profile: safety.ProfileWorkspaceWrite, prefixRule: []string{"go", "test"},
	}, res, safety.SandboxDecision{UseSandbox: true, Backend: safety.BackendSeatbelt}, nil, denial)
	if authorized {
		t.Fatal("a pending prompt must not authorize the escalated retry")
	}
	var rae *RequiresActionError
	if !errors.As(err, &rae) {
		t.Fatalf("expected RequiresActionError, got %T %v", err, err)
	}
	if actionPayload == nil {
		t.Fatal("action payload was not captured")
	}
	waitPayload, ok := rae.ToolInput.(map[string]any)
	if !ok {
		t.Fatalf("wait payload = %T, want map", rae.ToolInput)
	}
	if !reflect.DeepEqual(waitPayload, actionPayload) {
		t.Fatalf("wait payload must equal action payload:\nwait=%#v\naction=%#v", waitPayload, actionPayload)
	}
	if got := waitPayload["sandbox_denial_reason"]; got != denial {
		t.Fatalf("sandbox denial reason = %q, want %q", got, denial)
	}
	justification, _ := waitPayload["justification"].(string)
	for _, want := range []string{"sandbox blocked", "127.0.0.1:0", "outside the sandbox"} {
		if !strings.Contains(justification, want) {
			t.Fatalf("justification %q missing %q", justification, want)
		}
	}
	attempts := ToolAttemptsFromContext(ctx)
	if len(attempts) != 1 || attempts[0].Output["stderr"] != denial || attempts[0].Output["exit_code"] != 1 {
		t.Fatalf("sandbox attempt was not captured: %#v", attempts)
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok || completion.ActionID != "act-sandbox-retry" || completion.Output["sandbox_retry"] != true {
		t.Fatalf("approval wait completion was not captured: %#v, ok=%v", completion, ok)
	}
}

func TestAdditionalPermissionsPreapprovedWithSessionSnapshotOnly(t *testing.T) {
	snapshot := safety.Snapshot{NetworkGrants: []safety.NetworkPermissionGrant{{
		Enabled: true, Scope: safety.GrantScopeSession, SessionID: "session-1",
	}}}
	rt := &AgentToolRuntime{
		Cfg: &appcfg.Root{Features: appcfg.FeaturesSection{RequestPermissionsTool: appcfg.BoolPtr(true)}},
		PermissionSnapshotForSession: func(sessionID string) safety.Snapshot {
			if sessionID != "session-1" {
				return safety.Snapshot{}
			}
			return snapshot
		},
	}
	ctx := llm.WithAgentSessionID(context.Background(), "session-1")
	if !additionalPermissionsPreapproved(ctx, rt, safety.RequestPermissionProfile{
		Network: safety.NewNetworkPermissionProfile(true),
	}) {
		t.Fatal("session-aware snapshot should satisfy an existing network grant")
	}
}

func TestStrictAutoReviewUsesOwningSessionSnapshot(t *testing.T) {
	rt := &AgentToolRuntime{PermissionSnapshotForSession: func(sessionID string) safety.Snapshot {
		if sessionID == "session-1" {
			return safety.Snapshot{StrictAutoReviewRunIDs: []string{"run-1"}}
		}
		return safety.Snapshot{}
	}}
	if !strictAutoReviewEnabled(llm.WithAgentSessionID(context.Background(), "session-1"), rt, "run-1") {
		t.Fatal("owning session did not observe strict auto review")
	}
	if strictAutoReviewEnabled(llm.WithAgentSessionID(context.Background(), "session-2"), rt, "run-1") {
		t.Fatal("strict auto review leaked through a global snapshot")
	}
}

func TestShellToolDescriptionExplainsStandaloneApplyPatch(t *testing.T) {
	tool, err := NewShellTool(NewState(t.TempDir()), nil)
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	description := tool.Description()
	for _, want := range []string{
		"apply_patch",
		"only command in that shell call",
		"terminator as the final nonblank line",
		"separate shell calls",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("shell tool description must contain %q, got %q", want, description)
		}
	}
}

func TestShellToolAllowsGrepIgnoreCaseInPlanMode(t *testing.T) {
	home := t.TempDir()
	st := NewState(t.TempDir())
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Home: home,
		Cfg:  &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}

	out, err := tool.Handle(planModeWriteCtx(home, "sid-grep-readonly"), `{"command":"printf 'Glob\\n' | grep -i glob"}`)
	if err != nil {
		t.Fatalf("read-only grep pipeline should run in plan mode: %v", err)
	}
	outText, ok := out.(string)
	if !ok {
		t.Fatalf("expected string tool output, got %T", out)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(outText), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if !strings.Contains(payload["stdout"].(string), "Glob") {
		t.Fatalf("unexpected grep output: %#v", payload)
	}
}

func TestShellToolAllowsReadOnlyPathInAdditionalRootInPlanMode(t *testing.T) {
	home := t.TempDir()
	primaryRoot := t.TempDir()
	externalRoot := t.TempDir()
	target := filepath.Join(externalRoot, "OpenclawRelayService.java")
	if err := os.WriteFile(target, []byte("reasoning_end\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	st := NewState(primaryRoot, externalRoot)
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Home: home,
		Cfg:  &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}

	cmd := `rg -n "reasoning_end|reasoning|onReasoning|message_snapshot|op" ` + target
	out, err := tool.Handle(planModeWriteCtx(home, "sid-rg-additional-root"), `{"command":`+mustJSON(t, cmd)+`}`)
	if err != nil {
		t.Fatalf("read-only rg under an additional root should run in plan mode: %v", err)
	}
	if !strings.Contains(out.(string), "reasoning_end") {
		t.Fatalf("unexpected rg output: %s", out)
	}
}
func TestShellToolAllowsReadOnlyPathFromCurrentRunGrantInPlanMode(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	externalRoot := t.TempDir()
	target := filepath.Join(externalRoot, "OpenclawRelayService.java")
	if err := os.WriteFile(target, []byte("reasoning_end\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	store := safety.NewStore()
	store.AddFileSystemGrant(safety.FileSystemPermissionGrant{
		Entry: safety.FileSystemPermissionEntry{
			Path:   safety.FileSystemPermissionPath{Path: externalRoot},
			Access: safety.FileSystemAccessRead,
		},
		Scope: safety.GrantScopeTurn, SessionID: "sid-rg-grant",
		RunID: "run-rg-grant",
	})
	st := NewState(workspace)
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Home:                         home,
		PermissionSnapshot:           store.Snapshot,
		PermissionSnapshotForSession: store.SnapshotForSession,
		Cfg:                          &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}

	cmd := `rg -n "reasoning_end|reasoning|onReasoning|message_snapshot|op" ` + target + ` | head -400`
	ctx := WithRunID(planModeWriteCtx(home, "sid-rg-grant"), "run-rg-grant")
	out, err := tool.Handle(ctx, `{"command":`+mustJSON(t, cmd)+`}`)
	if err != nil {
		t.Fatalf("read-only rg under a current-run grant should run in plan mode: %v", err)
	}
	if !strings.Contains(out.(string), "reasoning_end") {
		t.Fatalf("unexpected rg output: %s", out)
	}
}

func TestShellToolAllowsGitCCompoundReadOnlyCommandInPlanMode(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	gitRoot := t.TempDir()
	if err := exec.Command("git", "-C", gitRoot, "init").Run(); err != nil {
		t.Fatalf("init git fixture: %v", err)
	}
	st := NewState(workspace, gitRoot)
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Home: home,
		Cfg:  &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	cmd := "git -C " + gitRoot + " status --short && git -C" + gitRoot + " rev-parse --show-toplevel"
	out, err := tool.Handle(planModeWriteCtx(home, "sid-git-c"), `{"command":`+mustJSON(t, cmd)+`}`)
	if err != nil {
		t.Fatalf("read-only git -C compound command should run in plan mode: %v", err)
	}
	if !strings.Contains(out.(string), gitRoot) {
		t.Fatalf("unexpected git output: %s", out)
	}
}

func TestShellToolEmitsOutputDeltasWithToolStepID(t *testing.T) {
	st := NewState(t.TempDir())
	var deltas []StepEvent
	st.SetStepHook(func(_ context.Context, evt StepEvent) {
		if evt.Kind == StepKindToolOutputDelta {
			deltas = append(deltas, evt)
		}
	})
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}

	ctx := WithToolStepID(context.Background(), "shell-step-1")
	if _, err := tool.Handle(ctx, `{"command":"printf live-output"}`); err != nil {
		t.Fatalf("shell Handle: %v", err)
	}
	if len(deltas) == 0 {
		t.Fatal("expected at least one shell output delta")
	}
	var combined strings.Builder
	for _, evt := range deltas {
		if evt.StepID != "shell-step-1" {
			t.Fatalf("delta StepID = %q, want shell-step-1", evt.StepID)
		}
		if evt.ToolName != "shell" {
			t.Fatalf("delta ToolName = %q, want shell", evt.ToolName)
		}
		chunk, _ := evt.Output["chunk"].(string)
		combined.WriteString(chunk)
	}
	if combined.String() != "live-output" {
		t.Fatalf("combined delta output = %q, want live-output", combined.String())
	}
}

func TestShellToolFailsClosedWhenSandboxUnavailableAndConfiguredToFail(t *testing.T) {
	t.Setenv("PATH", "")
	st := NewState(t.TempDir())
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{
			SandboxMode: appcfg.SandboxModeWorkspaceWrite,
		},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	_, err = tool.Handle(context.Background(), `{"command":"pwd"}`)
	if err == nil {
		t.Fatalf("expected sandbox unavailable error")
	}
	if !strings.Contains(err.Error(), "sandbox unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestShellToolDoesNotFallbackToHostWhenSandboxUnavailable(t *testing.T) {
	t.Setenv("PATH", "")
	st := NewState(t.TempDir())
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{
			SandboxMode: appcfg.SandboxModeWorkspaceWrite,
		},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	_, err = tool.Handle(context.Background(), `{"command":"pwd"}`)
	if err == nil {
		t.Fatalf("expected sandbox unavailable error")
	}
	if !strings.Contains(err.Error(), "sandbox unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestShellToolPreservesApprovedSandboxBypassForReadOnlyCommand(t *testing.T) {
	t.Setenv("PATH", "")
	st := NewState(t.TempDir())
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}

	ctx := WithSandboxBypassApproved(context.Background(), true)
	out, err := tool.Handle(ctx, `{"command":"pwd"}`)
	if err != nil {
		t.Fatalf("approved read-only sandbox bypass should run on host: %v", err)
	}
	if !strings.Contains(out.(string), `"exit_code":0`) {
		t.Fatalf("unexpected shell output: %s", out)
	}
}

func TestShellToolActionHookIncludesSandboxPermissions(t *testing.T) {
	st := NewState(t.TempDir())
	var got map[string]any
	st.SetActionHook(func(ctx context.Context, kind string, payload any) (string, bool, error) {
		if kind != "shell" {
			t.Fatalf("unexpected action kind %q", kind)
		}
		var ok bool
		got, ok = payload.(map[string]any)
		if !ok {
			t.Fatalf("expected map payload, got %T", payload)
		}
		return "act-shell", true, nil
	})
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	_, err = tool.Handle(context.Background(), `{"command":"touch out.txt","sandbox_permissions":"require_escalated","justification":"write outside the sandbox"}`)
	if err == nil {
		t.Fatalf("expected requires-action error")
	}
	if got["sandbox_permissions"] != "require_escalated" {
		t.Fatalf("action hook must receive sandbox permissions, got %#v", got)
	}
}

// TestShellToolApprovalAmendmentSurvivesCommandRewrite guards the invariant
// that the reusable-prefix amendment proposed by the approval UI (derived from
// the RequiresActionError's ToolInput) validates against the pending action's
// payload. Command rewriting inserts a presentation flag (git --no-pager); if
// the wait record kept the pre-rewrite command while the action payload kept the
// rewritten one, an LLM-supplied prefix_rule matched only one of them and the
// approval was rejected with "approval update does not match the proposed
// command prefix".
func TestShellToolApprovalAmendmentSurvivesCommandRewrite(t *testing.T) {
	st := NewState(t.TempDir())
	var payload map[string]any
	st.SetActionHook(func(ctx context.Context, kind string, p any) (string, bool, error) {
		payload, _ = p.(map[string]any)
		return "act-shell", true, nil
	})
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	_, handleErr := tool.Handle(context.Background(), `{"command":"git -C sub add -A","sandbox_permissions":"require_escalated","justification":"commit inside submodule","prefix_rule":["git","-C","sub","add"]}`)
	var rae *RequiresActionError
	if !errors.As(handleErr, &rae) {
		t.Fatalf("expected requires-action error, got %v", handleErr)
	}
	if payload == nil {
		t.Fatal("action hook did not capture a payload")
	}

	// The action payload must hold the rewritten command that actually runs.
	if got, _ := payload["command"].(string); got != "git --no-pager -C sub add -A" {
		t.Fatalf("action payload command = %q; want the rewritten command", got)
	}
	payloadJSON, _ := json.Marshal(payload)

	// The wait record is populated from the RequiresActionError's ToolInput. It
	// must describe the same command so the amendment agrees on both sides.
	toolInputJSON, _ := json.Marshal(rae.ToolInput)
	suggestion := safety.BuildToolApprovalSuggestion("shell", string(toolInputJSON))
	opts := safety.BuildApprovalDecisionOptions("shell", "shell", string(toolInputJSON))
	var amendment safety.ExecPolicyAmendment
	for _, o := range opts {
		if o.Decision == safety.DecisionAcceptWithExecPolicyAmendment {
			amendment = o.ExecPolicyAmendment
		}
	}
	if len(amendment) == 0 {
		t.Fatalf("expected a reusable-prefix amendment, got suggestion %+v", suggestion)
	}
	// The amendment must stick as the model-intended prefix, rewritten to match
	// the command that actually runs — not collapse to the exact command.
	wantPrefix := []string{"git", "--no-pager", "-C", "sub", "add"}
	if !reflect.DeepEqual([]string(amendment), wantPrefix) {
		t.Fatalf("amendment = %v; want the rewritten prefix %v", amendment, wantPrefix)
	}

	update := safety.PermissionUpdate{
		Type: safety.UpdateAddRules, Destination: safety.DestinationLocalSettings, Behavior: safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{{ToolName: "Bash", CommandPrefix: append([]string(nil), amendment...), BypassSandbox: true}},
	}
	if _, err := ValidateApprovalUpdateForAction("shell", string(payloadJSON), "", update); err != nil {
		t.Fatalf("amendment proposed by the approval UI must validate against the action payload: %v", err)
	}
}

func TestShellToolAllowsExplicitUseDefaultWithJustification(t *testing.T) {
	st := NewState(t.TempDir())
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	out, err := tool.Handle(context.Background(), `{"command":"printf hi","sandbox_permissions":"use_default","justification":"explain the command"}`)
	if err != nil {
		t.Fatalf("explicit use_default paired with a justification must run: %v", err)
	}
	if !strings.Contains(out.(string), `"exit_code":0`) {
		t.Fatalf("unexpected shell output: %s", out)
	}
}

func TestShellToolSchemaKeepsEscalationFieldsOptional(t *testing.T) {
	for _, execApprovals := range []bool{false, true} {
		st := NewState(t.TempDir())
		tool, err := NewShellTool(st, &AgentToolRuntime{
			Cfg: &appcfg.Root{Features: appcfg.FeaturesSection{ExecPermissionApprovals: appcfg.BoolPtr(execApprovals)}},
		})
		if err != nil {
			t.Fatalf("NewShellTool: %v", err)
		}
		schema := tool.InputSchema()
		props, _ := schema["properties"].(map[string]any)
		required := stringSet(schema["required"])

		// The escalation-only fields must be optional and single-typed — not
		// forced required and not nullable unions — so the model can omit them
		// and weak models don't emit string-encoded arrays.
		just, _ := props["justification"].(map[string]any)
		if got := just["type"]; got != "string" {
			t.Fatalf("execApprovals=%v justification type must be a plain string, got %#v", execApprovals, got)
		}
		if required["justification"] {
			t.Fatalf("execApprovals=%v justification must be optional", execApprovals)
		}

		prefix, _ := props["prefix_rule"].(map[string]any)
		if got := prefix["type"]; got != "array" {
			t.Fatalf("execApprovals=%v prefix_rule type must be a plain array, got %#v", execApprovals, got)
		}
		if _, ok := prefix["items"].(map[string]any); !ok {
			t.Fatalf("execApprovals=%v prefix_rule must declare string items", execApprovals)
		}
		if required["prefix_rule"] {
			t.Fatalf("execApprovals=%v prefix_rule must be optional", execApprovals)
		}

		if execApprovals {
			additional, _ := props["additional_permissions"].(map[string]any)
			if got := additional["type"]; got != "object" {
				t.Fatalf("execApprovals=true additional_permissions type must be a plain object, got %#v", got)
			}
			if required["additional_permissions"] {
				t.Fatalf("execApprovals=true additional_permissions must be optional")
			}
		} else if _, present := props["additional_permissions"]; present {
			t.Fatalf("execApprovals=false additional_permissions must be dropped from the schema")
		}

		// command is the one field the shell handler truly requires.
		if !required["command"] {
			t.Fatalf("execApprovals=%v command must be required", execApprovals)
		}
	}
}

func stringSet(raw any) map[string]bool {
	out := map[string]bool{}
	switch v := raw.(type) {
	case []any:
		for _, it := range v {
			if s, ok := it.(string); ok {
				out[s] = true
			}
		}
	case []string:
		for _, s := range v {
			out[s] = true
		}
	}
	return out
}

func TestShellToolRunsWithNullJustification(t *testing.T) {
	st := NewState(t.TempDir())
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	out, err := tool.Handle(context.Background(), `{"command":"printf hi","sandbox_permissions":"use_default","justification":null}`)
	if err != nil {
		t.Fatalf("a null justification must be treated as unset: %v", err)
	}
	if !strings.Contains(out.(string), `"exit_code":0`) {
		t.Fatalf("unexpected shell output: %s", out)
	}
}

func TestShellToolRunsWithStringEncodedPrefixRule(t *testing.T) {
	// Weaker models sometimes emit a string-encoded array for `prefix_rule`
	// (e.g. `"prefix_rule":"[]"`) instead of a real JSON array. The tool call
	// must still run: the empty prefix means "no escalation prefix".
	st := NewState(t.TempDir())
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	for _, prefix := range []string{`"[]"`, `""`} {
		args := `{"command":"printf hi","sandbox_permissions":"use_default","prefix_rule":` + prefix + `}`
		out, err := tool.Handle(context.Background(), args)
		if err != nil {
			t.Fatalf("prefix_rule=%s must be accepted: %v", prefix, err)
		}
		if !strings.Contains(out.(string), `"exit_code":0`) {
			t.Fatalf("prefix_rule=%s unexpected shell output: %s", prefix, out)
		}
	}
}

func TestShellToolTreatsEmptyAdditionalPermissionsAsUnset(t *testing.T) {
	st := NewState(t.TempDir())
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{
			SandboxMode: appcfg.SandboxModeDangerFullAccess,
			Features:    appcfg.FeaturesSection{ExecPermissionApprovals: appcfg.BoolPtr(true)},
		},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	// An empty object for additional_permissions is its natural "unset" value;
	// under use_default it must not trigger "additional_permissions requires
	// with_additional_permissions".
	out, err := tool.Handle(context.Background(), `{"command":"printf hi","sandbox_permissions":"use_default","additional_permissions":{}}`)
	if err != nil {
		t.Fatalf("empty additional_permissions must be treated as unset: %v", err)
	}
	if !strings.Contains(out.(string), `"exit_code":0`) {
		t.Fatalf("unexpected shell output: %s", out)
	}
}

func TestShellToolRejectsJustificationWithoutSandboxPermissions(t *testing.T) {
	st := NewState(t.TempDir())
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	if _, err := tool.Handle(context.Background(), `{"command":"printf hi","justification":"explain the command"}`); err == nil ||
		!strings.Contains(err.Error(), "requires an explicit") {
		t.Fatalf("omitted sandbox_permissions with a justification must be rejected, got: %v", err)
	}
}

func TestShellToolRejectsFreshSandboxOverrideOutsideOnRequest(t *testing.T) {
	for _, policy := range []safety.ApprovalPolicy{
		{Mode: safety.ApprovalUnlessTrusted},
		{Mode: safety.ApprovalNever},
		{Mode: safety.ApprovalGranular, Granular: safety.GranularApprovalConfig{SandboxApproval: true}},
	} {
		t.Run(string(policy.Mode), func(t *testing.T) {
			st := NewState(t.TempDir())
			called := false
			st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
				called = true
				return "action", true, nil
			})
			tool, err := NewShellTool(st, &AgentToolRuntime{
				Cfg:                &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite},
				PermissionSnapshot: func() safety.Snapshot { return safety.Snapshot{ApprovalPolicy: policy} },
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = tool.Handle(context.Background(), `{"command":"pwd","sandbox_permissions":"require_escalated","justification":"needs host access"}`)
			if err == nil || !strings.Contains(err.Error(), "approval policy is") {
				t.Fatalf("policy=%+v err=%v", policy, err)
			}
			if called {
				t.Fatal("rejected override must not create an approval")
			}
		})
	}
}

func TestSandboxRetryApprovalPolicy(t *testing.T) {
	cases := []struct {
		policy safety.ApprovalPolicy
		want   bool
	}{
		{policy: safety.ApprovalPolicy{Mode: safety.ApprovalUnlessTrusted}, want: true},
		// A command that cannot work inside the sandbox is exactly what
		// on-request exists to ask about.
		{policy: safety.ApprovalPolicy{Mode: safety.ApprovalOnRequest}, want: true},
		{policy: safety.ApprovalPolicy{Mode: safety.ApprovalNever}},
		// An unset policy is resolved to on-request before it reaches here.
		{policy: safety.ApprovalPolicy{}, want: true},
		{policy: safety.ApprovalPolicy{Mode: safety.ApprovalGranular, Granular: safety.GranularApprovalConfig{SandboxApproval: true}}, want: true},
		{policy: safety.ApprovalPolicy{Mode: safety.ApprovalGranular}},
	}
	for _, tc := range cases {
		if got := sandboxRetryApprovalAllowed(tc.policy); got != tc.want {
			t.Fatalf("policy=%+v got=%v want=%v", tc.policy, got, tc.want)
		}
	}
}

func TestShellToolBuiltinPolicyReturnsRequiresActionWhenApprovalHookExists(t *testing.T) {
	st := NewState(t.TempDir())
	called := false
	st.SetActionHook(func(ctx context.Context, kind string, payload any) (string, bool, error) {
		called = true
		if kind != "shell" {
			t.Fatalf("unexpected action kind %q", kind)
		}
		m, ok := payload.(map[string]any)
		if !ok {
			t.Fatalf("expected payload map, got %T", payload)
		}
		if strings.TrimSpace(m["command"].(string)) != "echo $(whoami)" {
			t.Fatalf("unexpected payload %#v", payload)
		}
		if m["force_tool_approval"] != true || m["approval_reason"] != "dangerous_shell_syntax" {
			t.Fatalf("dangerous shell syntax must force approval, got %#v", payload)
		}
		return "act-shell", true, nil
	})
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	_, err = tool.Handle(context.Background(), `{"command":"echo $(whoami)"}`)
	if err == nil {
		t.Fatalf("expected requires-action error")
	}
	if !called {
		t.Fatal("expected action hook to be called before builtin policy returns")
	}
	var req *RequiresActionError
	if !errors.As(err, &req) || req == nil {
		t.Fatalf("expected RequiresActionError, got %T %v", err, err)
	}
	if req.ActionID != "act-shell" || req.ActionKind != "shell" || req.ToolName != "shell" {
		t.Fatalf("unexpected requires action payload: %+v", req)
	}
}

func TestShellToolBuiltinPolicyRequiresApprovalWhenNoHookExists(t *testing.T) {
	st := NewState(t.TempDir())
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	_, err = tool.Handle(context.Background(), `{"command":"echo $(whoami)"}`)
	if err == nil {
		t.Fatalf("expected approval required error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "shell command requires approval") {
		t.Fatalf("missing approval phrase in %q", msg)
	}
	if !strings.Contains(msg, "echo $(whoami)") {
		t.Fatalf("error should echo command requiring approval, got %q", msg)
	}
}

func TestShellToolBuiltinPolicyRunsAfterApproval(t *testing.T) {
	st := NewState(t.TempDir())
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	ctx := WithApprovedActionID(context.Background(), "act-shell")
	out, err := tool.Handle(ctx, `{"command":"echo $(printf approved)"}`)
	if err != nil {
		t.Fatalf("approved shell command should run: %v", err)
	}
	outText, ok := out.(string)
	if !ok {
		t.Fatalf("expected string tool output, got %T", out)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(outText), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if strings.TrimSpace(payload["stdout"].(string)) != "approved" {
		t.Fatalf("unexpected stdout: %#v", payload)
	}
}

func TestShellToolDoesNotInterpretCommandOutputAsSandboxMetadata(t *testing.T) {
	st := NewState(t.TempDir())
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{
			SandboxMode: appcfg.SandboxModeDangerFullAccess,
		},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	out, err := tool.Handle(context.Background(), `{"command":"printf ok; printf '<sandbox_violations>[{\"kind\":\"network\"},{\"kind\":\"write\",\"path\":\"/tmp/x\"}]</sandbox_violations>' >&2"}`)
	if err != nil {
		t.Fatalf("shell handle: %v", err)
	}
	outText, ok := out.(string)
	if !ok {
		t.Fatalf("expected string tool output, got %T", out)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(outText), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if !strings.Contains(payload["stderr"].(string), "<sandbox_violations>") {
		t.Fatalf("stderr was modified: %#v", payload)
	}
	// The sandbox_* operational metadata is telemetry-only noise for the LLM
	// and must not leak into the model-facing result payload.
	for k := range payload {
		if strings.HasPrefix(k, "sandbox_") {
			t.Fatalf("model-facing payload must not contain sandbox_ metadata, found %q: %#v", k, payload)
		}
	}
	if _, present := payload["stdout"]; !present {
		t.Fatalf("payload missing stdout: %#v", payload)
	}
	if _, present := payload["exit_code"]; !present {
		t.Fatalf("payload missing exit_code: %#v", payload)
	}
}

// TestShellToolCancellationPropagatesToOrchestration verifies that when the
// context is cancelled during shell execution, the tool returns a context
// error (context.Canceled or context.DeadlineExceeded) so the orchestration
// layer terminates the run instead of wrapping cancellation as a successful
// shell result.
func TestShellToolCancellationPropagatesToOrchestration(t *testing.T) {
	st := NewState(t.TempDir())
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{
			SandboxMode: appcfg.SandboxModeDangerFullAccess,
		},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, err := tool.Handle(ctx, `{"command":"sleep 30"}`)
		errs <- err
	}()

	// Give the command a moment to start.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("expected cancellation error, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shell tool did not return within 5s after cancellation — " +
			"process tree may not have been killed")
	}
}

// TestShellProfilePlumbsGoCache pins when a profile is worth pointing at the Go
// caches. A profile that can already write the project has a reachable cache; a
// read-only profile has neither, so it needs the grant - but only where a
// sandbox can carry it, since the environment alone would move `go` off the
// warm host caches and onto cold ones.
func TestShellProfilePlumbsGoCache(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	cache := filepath.Join(root, "cache", "go")

	for _, tt := range []struct {
		name           string
		profile        safety.Profile
		sandboxApplied bool
		writableRoots  []string
		cwd            string
		want           bool
	}{
		{name: "workspace-write in sandbox", profile: safety.ProfileWorkspaceWrite, sandboxApplied: true, writableRoots: []string{workspace}, cwd: workspace, want: true},
		{name: "workspace-write on host", profile: safety.ProfileWorkspaceWrite, writableRoots: []string{workspace}, cwd: workspace, want: false},
		{name: "managed with the workspace in sandbox", profile: safety.ProfileManaged, sandboxApplied: true, writableRoots: []string{workspace, cache}, cwd: workspace, want: true},
		{name: "managed with the workspace on host", profile: safety.ProfileManaged, writableRoots: []string{workspace, cache}, cwd: workspace, want: false},
		{name: "managed without the workspace", profile: safety.ProfileManaged, sandboxApplied: true, writableRoots: []string{cache}, cwd: workspace, want: false},
		{name: "managed without any write root", profile: safety.ProfileManaged, sandboxApplied: true, cwd: workspace, want: false},
		{name: "read-only under a sandbox", profile: safety.ProfileReadOnly, sandboxApplied: true, cwd: workspace, want: true},
		{name: "read-only with no sandbox", profile: safety.ProfileReadOnly, cwd: workspace, want: false},
		{name: "unknown profile", profile: safety.Profile("nonsense"), sandboxApplied: true, cwd: workspace, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := shellProfilePlumbsGoCache(tt.profile, tt.sandboxApplied, tt.writableRoots, tt.cwd); got != tt.want {
				t.Fatalf("shellProfilePlumbsGoCache = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShellAttemptEnv(t *testing.T) {
	base := []string{"PATH=/usr/bin", "LANG=C"}
	cache := []string{"GOCACHE=/tmp/cache", "GOMODCACHE=/tmp/mod"}

	for _, tt := range []struct {
		name      string
		sandboxed bool
		want      []string
	}{
		{name: "sandbox appends cache", sandboxed: true, want: []string{"PATH=/usr/bin", "LANG=C", "GOCACHE=/tmp/cache", "GOMODCACHE=/tmp/mod"}},
		{name: "host keeps base only", sandboxed: false, want: []string{"PATH=/usr/bin", "LANG=C"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := shellAttemptEnv(base, cache, tt.sandboxed)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("shellAttemptEnv = %#v, want %#v", got, tt.want)
			}
			got[0] = "mutated"
			if base[0] != "PATH=/usr/bin" {
				t.Fatal("shellAttemptEnv modified the base environment")
			}
		})
	}
}

func TestHostShellDoesNotUseIsolatedGoCache(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not installed")
	}

	for _, tt := range []struct {
		name  string
		perms safety.SandboxPermissions
		cfg   *appcfg.Root
	}{
		{
			name:  "danger-full-access",
			perms: safety.SandboxPermissionsUseDefault,
			cfg:   &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
		},
		{
			name:  "require-escalated",
			perms: safety.SandboxPermissionsRequireEscalated,
			cfg:   &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/hostcache\\n\\ngo 1.22\\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			rt := &AgentToolRuntime{
				Home:          home,
				WorkspaceRoot: workspace,
				Cfg:           tt.cfg,
			}
			got, err := runSandboxedShellCommand(context.Background(), NewState(workspace), rt, sandboxedShellRequest{
				command:            "go env GOCACHE GOMODCACHE",
				cwd:                workspace,
				profile:            safety.ProfileWorkspaceWrite,
				sandboxPermissions: tt.perms,
			})
			if err != nil {
				t.Fatalf("host shell command: %v", err)
			}
			var result struct {
				Stdout   string `json:"stdout"`
				ExitCode int    `json:"exit_code"`
			}
			if err := json.Unmarshal([]byte(got), &result); err != nil {
				t.Fatalf("decode shell result %q: %v", got, err)
			}
			cacheRoot := filepath.Join(home, "cache", "go")
			if result.ExitCode != 0 || strings.Contains(result.Stdout, cacheRoot) {
				t.Fatalf("host command used isolated cache: exit=%d stdout=%q cacheRoot=%q", result.ExitCode, result.Stdout, cacheRoot)
			}
			if _, err := os.Stat(cacheRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("isolated cache root was created: stat=%v path=%q", err, cacheRoot)
			}
		})
	}
}

// TestPromoteReadOnlyWriteScopeStatesItsOwnWriteSet guards the boundary a
// plan-mode shell command runs inside. The profile has to become managed for the
// backend to consume the write exceptions at all, and the request has to say
// that those exceptions are the whole policy - otherwise the managed profile
// also inherits the configured workspace roots and a command granted its build
// cache could edit the project the session is still planning.
func TestPromoteReadOnlyWriteScopeStatesItsOwnWriteSet(t *testing.T) {
	cwd := t.TempDir()
	cache := t.TempDir()

	t.Run("promotes and keeps the project readable", func(t *testing.T) {
		req := sandboxedShellRequest{profile: safety.ProfileReadOnly, cwd: cwd}
		allowRead := promoteReadOnlyWriteScope(&req, []string{cache}, nil)
		if req.profile != safety.ProfileManaged {
			t.Fatalf("profile = %q, want %q after the promotion", req.profile, safety.ProfileManaged)
		}
		if !req.exclusiveWritablePaths {
			t.Fatal("a promoted request must state its write set as the whole policy")
		}
		found := false
		for _, root := range allowRead {
			if root == cwd {
				found = true
			}
		}
		if !found {
			t.Fatalf("promoted request lost the working directory it was granted in: %v", allowRead)
		}
	})

	t.Run("without exceptions the profile stays read-only", func(t *testing.T) {
		req := sandboxedShellRequest{profile: safety.ProfileReadOnly, cwd: cwd}
		if allowRead := promoteReadOnlyWriteScope(&req, nil, []string{"/somewhere"}); len(allowRead) != 1 {
			t.Fatalf("allowRead = %v, want it untouched", allowRead)
		}
		if req.profile != safety.ProfileReadOnly || req.exclusiveWritablePaths {
			t.Fatalf("a request with nothing to write was promoted: %q exclusive=%v", req.profile, req.exclusiveWritablePaths)
		}
	})

	t.Run("leaves other profiles alone", func(t *testing.T) {
		for _, profile := range []safety.Profile{safety.ProfileWorkspaceWrite, safety.ProfileManaged} {
			req := sandboxedShellRequest{profile: profile, cwd: cwd}
			promoteReadOnlyWriteScope(&req, []string{cache}, nil)
			if req.profile != profile || req.exclusiveWritablePaths {
				t.Fatalf("profile %q was rewritten to %q exclusive=%v", profile, req.profile, req.exclusiveWritablePaths)
			}
		}
	})
}

func TestMaybePruneGoCachesRemovesReadOnlyNestedModuleCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory permissions are not portable to Windows")
	}

	base := t.TempDir()
	active := filepath.Join(base, "active")
	stale := filepath.Join(base, "stale")
	if err := os.MkdirAll(filepath.Join(active, "mod"), 0o700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(stale, "pkg", "module", "@v")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "go.mod"), []byte("module example.test/stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-(goCacheTTL + time.Hour))
	for _, path := range []string{
		stale,
		filepath.Join(stale, "pkg"),
		filepath.Join(stale, "pkg", "module"),
		nested,
	} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		filepath.Join(stale, "pkg"),
		filepath.Join(stale, "pkg", "module"),
		nested,
	} {
		if err := os.Chmod(path, 0o555); err != nil {
			t.Fatal(err)
		}
	}

	maybePruneGoCaches(base, "active")

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale cache still exists: %v", err)
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("active cache was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, ".last-prune")); err != nil {
		t.Fatalf("successful prune did not create marker: %v", err)
	}
}

func TestRemoveGoCacheDirHandlesReadOnlyQuotaCandidate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory permissions are not portable to Windows")
	}

	base := t.TempDir()
	candidate := filepath.Join(base, "quota-candidate")
	nested := filepath.Join(candidate, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "blob"), []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(nested, 0o555); err != nil {
		t.Fatal(err)
	}

	if err := removeGoCacheDir(candidate); err != nil {
		t.Fatalf("removeGoCacheDir: %v", err)
	}
	if _, err := os.Stat(candidate); !os.IsNotExist(err) {
		t.Fatalf("read-only quota candidate still exists: %v", err)
	}
}
