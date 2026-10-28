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
	"sync/atomic"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

func TestSandboxDenialReasonFallsBackToExecutionError(t *testing.T) {
	reason := sandboxDenialReason(safety.SandboxDecision{UseSandbox: true}, safety.CommandResult{}, errors.New("permission denied"))
	if reason != "permission denied" {
		t.Fatalf("fallback denial reason = %q", reason)
	}
}

func TestSandboxDenialReasonUsesConcreteDeniedOutputLine(t *testing.T) {
	res := safety.CommandResult{Stderr: "--- FAIL: TestOAuth\nlistener_test.go:28: listen tcp 127.0.0.1:0: bind: operation not permitted\nFAIL"}
	want := "listener_test.go:28: listen tcp 127.0.0.1:0: bind: operation not permitted"
	if got := sandboxDenialReason(safety.SandboxDecision{UseSandbox: true}, res, nil); got != want {
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

// The engine's dangerous-command conclusion travels by name. A surface cannot
// restate it — "rm -f" is the rule, not "deletes the whole directory, then you
// have to rebuild" — and it cannot re-derive it either: only this parser
// decides what counts as dangerous, so a name it did not write means nothing
// was found.
func TestShellDangerousRuleInPayload(t *testing.T) {
	payload := shellAssessmentApprovalPayload("rm -f /tmp/x", &ShellInput{}, safety.AssessShellCommand("rm -f /tmp/x"))
	if payload["dangerous_rule"] != "forced_rm" {
		t.Fatalf("dangerous_rule = %v, want forced_rm", payload["dangerous_rule"])
	}
	if payload["approval_reason"] != "dangerous_shell_syntax" {
		t.Fatalf("approval_reason = %v, want dangerous_shell_syntax", payload["approval_reason"])
	}

	// Complex parsing without a dangerous rule names no rule: the flag alone is
	// what forces the prompt there, and there is nothing for the card to quote.
	for _, command := range []string{"echo $(date)", "mvn clean verify"} {
		assessment := safety.AssessShellCommand(command)
		if assessment.Dangerous != nil {
			t.Fatalf("%q: test premise broken, the engine found %q", command, assessment.Dangerous.Rule)
		}
		payload := shellAssessmentApprovalPayload(command, &ShellInput{}, assessment)
		if rule, ok := payload["dangerous_rule"]; ok {
			t.Fatalf("%q: complex-but-not-dangerous must not name a rule, got %v", command, rule)
		}
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

// TestShellOutputDeltasCarryTheCallsStart pins that a call's start travels
// with the call's context: the deltas a handler emits while it runs carry the
// start the dispatcher recorded, not one derived from the step id.
func TestShellOutputDeltasCarryTheCallsStart(t *testing.T) {
	st := NewState(t.TempDir())
	var deltas []StepEvent
	st.SetStepHook(func(_ context.Context, evt StepEvent) {
		if evt.Kind == StepKindToolOutputDelta {
			deltas = append(deltas, evt)
		}
	})
	shell, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	start := time.Now().Add(-42 * time.Second).Round(time.Millisecond)
	ctx := WithToolStepStartedAt(WithToolStepID(context.Background(), "shell-step-clock"), start)
	if _, err := shell.Handle(ctx, `{"command":"printf live-output"}`); err != nil {
		t.Fatalf("shell Handle: %v", err)
	}
	if len(deltas) == 0 {
		t.Fatal("expected at least one shell output delta")
	}
	for _, evt := range deltas {
		if !evt.StartedAt.Equal(start) {
			t.Fatalf("delta StartedAt = %v, want the call's start %v", evt.StartedAt, start)
		}
	}
}

func TestShellNotifiesCodeIntel(t *testing.T) {
	// A finished command may have changed files the edit tools never saw, so
	// the runtime is told once per command — after the result is in hand, and
	// even when the command failed (its files may still have changed).
	st := NewState(t.TempDir())
	stub := &editDiagStub{}
	shell, err := NewShellTool(st, &AgentToolRuntime{
		Cfg:       &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
		CodeIntel: stub,
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	if _, err := shell.Handle(context.Background(), `{"command":"printf hi"}`); err != nil {
		t.Fatalf("shell Handle: %v", err)
	}
	if stub.runShells != 1 {
		t.Fatalf("DidRunShell calls=%d want 1", stub.runShells)
	}
	if _, err := shell.Handle(context.Background(), `{"command":"false"}`); err != nil {
		t.Fatalf("failing command must not fail the tool: %v", err)
	}
	if stub.runShells != 2 {
		t.Fatalf("DidRunShell calls after failure=%d want 2", stub.runShells)
	}
	// No runtime wired: nothing to notify, and nothing may panic.
	plain, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	if _, err := plain.Handle(context.Background(), `{"command":"printf hi"}`); err != nil {
		t.Fatalf("shell Handle without code intel: %v", err)
	}
	if stub.runShells != 2 {
		t.Fatalf("stub must stay untouched, calls=%d", stub.runShells)
	}
}

// resolvedWorkingDir returns the symlink-resolved form so a comparison does not
// depend on whether the platform reports a temp directory by its physical path.
func resolvedWorkingDir(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return r
}

// TestShellToolRunsInSessionWorkingDir pins that the shell's working directory
// is the session project root carried on the context, not the process
// directory a gateway serves many projects from.
func TestShellToolRunsInSessionWorkingDir(t *testing.T) {
	home := t.TempDir()
	projectDir := t.TempDir()
	st := NewState(projectDir)
	tl, err := NewShellTool(st, &AgentToolRuntime{
		Home: home,
		Cfg:  &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	ctx := WithProjectRoot(context.Background(), projectDir)
	out, err := tl.Handle(ctx, `{"command":"pwd"}`)
	if err != nil {
		t.Fatalf("shell pwd: %v", err)
	}
	raw, _ := out.(string)
	var parsed struct {
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("decode shell output %q: %v", raw, err)
	}
	got := strings.TrimSpace(parsed.Stdout)
	if got == "" {
		t.Fatalf("empty pwd output: %q", raw)
	}
	if resolvedWorkingDir(t, got) != resolvedWorkingDir(t, projectDir) {
		t.Fatalf("shell ran in %q, want the session working dir %q", got, projectDir)
	}
}

// TestWriteFileRelativePathResolvesAgainstWorkingDir pins that a relative model
// path lands under the session project root rather than the process directory.
func TestWriteFileRelativePathResolvesAgainstWorkingDir(t *testing.T) {
	projectDir := t.TempDir()
	st := NewState(projectDir)
	st.SetPrimaryWorkspaceBoundary("", projectDir, nil)
	tl, err := NewFileWriteTool(st, nil)
	if err != nil {
		t.Fatalf("NewFileWriteTool: %v", err)
	}
	ctx := WithProjectRoot(context.Background(), projectDir)
	if _, err := tl.Handle(ctx, `{"file_path":"x.txt","content":"hello"}`); err != nil {
		t.Fatalf("write_file relative: %v", err)
	}
	target := filepath.Join(projectDir, "x.txt")
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected relative write at %s: %v", target, err)
	}
	if string(body) != "hello" {
		t.Fatalf("content=%q want hello", body)
	}
}

// TestPathGrantedMatchesRelativeGrantAgainstWorkingDir pins that a relative
// grant path is matched against the cwd the caller passes (the session working
// directory), not the process directory.
func TestPathGrantedMatchesRelativeGrantAgainstWorkingDir(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := safety.NewStore()
	safety.ApplyUpdate(store, safety.PermissionUpdate{
		Type: safety.UpdateAddPermissionGrants, Destination: safety.DestinationSession, SessionID: "s",
		FileSystemGrants: []safety.FileSystemPermissionGrant{{
			Entry: safety.FileSystemPermissionEntry{
				Path:   safety.FileSystemPermissionPath{Path: "notes"},
				Access: safety.FileSystemAccessWrite,
			},
			Scope: safety.GrantScopeSession,
		}},
	})
	st := NewState(projectDir)
	st.SetRuntimeValue("permission_snapshot_for_session", store.SnapshotForSession)

	target := filepath.Join(projectDir, "notes", "a.txt")
	if !st.PathGranted(target, safety.FileSystemAccessWrite, "s", "", projectDir) {
		t.Fatalf("relative grant must match a path under the working dir")
	}
	if st.PathGranted(target, safety.FileSystemAccessWrite, "s", "", t.TempDir()) {
		t.Fatalf("relative grant must not match a path under a different dir")
	}
}

// shellSandboxProfile decides the shell's filesystem boundary, and a custom
// subagent's "read only" is configuration rather than a type name: without the
// policy on its context, one `sed -i` would edit the project the policy exists
// to protect.
func TestShellSandboxProfileKeepsACustomReadOnlySubagentReadOnly(t *testing.T) {
	ctx := WithSubagentType(context.Background(), "db-inspector")
	ctx = WithSubagentToolPolicy(ctx, SubagentToolPolicy{Mode: appcfg.SubagentToolsReadOnly})

	profile, approved := shellSandboxProfile(ctx)
	if profile != safety.ProfileReadOnly || approved {
		t.Fatalf("profile = %v approved = %v, want a read-only shell without an approval", profile, approved)
	}
}

func TestShellSandboxProfileAllowsWritesForACustomEditingSubagent(t *testing.T) {
	ctx := WithSubagentType(context.Background(), "db-inspector")
	ctx = WithSubagentToolPolicy(ctx, SubagentToolPolicy{Mode: appcfg.SubagentToolsEdit})

	profile, approved := shellSandboxProfile(ctx)
	if profile != safety.ProfileWorkspaceWrite || approved {
		t.Fatalf("profile = %v approved = %v, want a workspace-write shell", profile, approved)
	}
}

// A pick policy is read-only exactly when the chosen set leaves write_file out:
// the shell follows the same policy the file tools already obey.
func TestShellSandboxProfileFollowsAPickPolicy(t *testing.T) {
	for _, tt := range []struct {
		name    string
		allowed []string
		want    safety.Profile
	}{
		{name: "pick without write_file", allowed: []string{"read_file", "shell"}, want: safety.ProfileReadOnly},
		{name: "pick with write_file", allowed: []string{"read_file", "write_file", "shell"}, want: safety.ProfileWorkspaceWrite},
	} {
		t.Run(tt.name, func(t *testing.T) {
			allowed := make(map[string]struct{}, len(tt.allowed))
			for _, name := range tt.allowed {
				allowed[name] = struct{}{}
			}
			ctx := WithSubagentType(context.Background(), "db-inspector")
			ctx = WithSubagentToolPolicy(ctx, SubagentToolPolicy{Mode: appcfg.SubagentToolsPick, Allowed: allowed})

			if profile, _ := shellSandboxProfile(ctx); profile != tt.want {
				t.Fatalf("profile = %v, want %v", profile, tt.want)
			}
		})
	}
}

// The policy check above must not move the built-in types: their boundary still
// comes from the static table, and a context that carries no policy keeps
// answering exactly as it did before custom subagents existed.
func TestShellSandboxProfilePinsBuiltInTypes(t *testing.T) {
	for _, tt := range []struct {
		name    string
		subtype string
		want    safety.Profile
	}{
		{name: "explore", subtype: "explore", want: safety.ProfileReadOnly},
		{name: "plan", subtype: "plan", want: safety.ProfileReadOnly},
		{name: "general-purpose", subtype: "general-purpose", want: safety.ProfileWorkspaceWrite},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithSubagentType(context.Background(), tt.subtype)
			if profile, _ := shellSandboxProfile(ctx); profile != tt.want {
				t.Fatalf("profile = %v, want %v", profile, tt.want)
			}
		})
	}
}

// The whole point of classifying a network failure as a sandbox denial is that
// the person is asked whether the command may run outside the sandbox. This is
// the real path, end to end: a live sandbox with no network, the shell tool's
// own decision, and the approval request the conversation renders as a card.
func TestShellToolAsksToLeaveTheSandboxWhenTheNetworkIsBlocked(t *testing.T) {
	if os.Getenv("FOREBRAIN_SANDBOXRT_INTEGRATION") != "1" {
		t.Skip("set FOREBRAIN_SANDBOXRT_INTEGRATION=1 to run the live sandbox check")
	}
	cfg := &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite}
	cfg.SandboxWorkspaceWrite.NetworkAccess = appcfg.BoolPtr(false)
	if st := safety.NewManager().Status(cfg); !st.EffectiveEnabled {
		t.Skipf("no sandbox backend on this machine: %s", st.UnavailableReason)
	}
	home := t.TempDir()
	state := NewState(home)
	var asked map[string]any
	state.SetActionHook(func(_ context.Context, kind string, payload any) (string, bool, error) {
		if kind != "shell" {
			t.Fatalf("action kind = %q, want shell", kind)
		}
		asked, _ = payload.(map[string]any)
		// pending: the run parks on the question, which is the card.
		return "act-network-retry", true, nil
	})
	shellTool, err := NewShellTool(state, &AgentToolRuntime{Home: home, Cfg: cfg})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	// The shape the model writes: the shell chain absorbs the exit status.
	command := "curl -sS -m 4 -o /dev/null https://example.com || echo NETFAIL"
	_, err = shellTool.Handle(context.Background(), `{"command":`+mustJSON(t, command)+`}`)
	var rae *RequiresActionError
	if !errors.As(err, &rae) {
		t.Fatalf("expected an approval request for the blocked network, got %T %v", err, err)
	}
	if asked == nil {
		t.Fatal("no approval payload was raised")
	}
	reason, _ := asked["sandbox_denial_reason"].(string)
	if !strings.Contains(reason, "Could not resolve host") {
		t.Fatalf("approval reason = %q, want the resolver failure line", reason)
	}
}

func TestShellToolShowsTheFilesACommandChanged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Not parallel: the run chdirs and the shell-diff registry is process-wide.
	root := t.TempDir()
	t.Chdir(root)
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitRun("init", "-q")
	gitRun("config", "user.email", "t@t")
	gitRun("config", "user.name", "t")
	mustWriteFile(t, filepath.Join(root, "a.txt"), "a\n")
	gitRun("add", "-A")
	gitRun("commit", "-qm", "init")
	topOut, err := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel").CombinedOutput()
	if err != nil {
		t.Fatalf("resolve toplevel: %v", err)
	}
	t.Cleanup(func() { forgetShellDiffRepo(strings.TrimSpace(string(topOut))) })

	st := NewState(root)
	shellTool, err := NewShellTool(st, &AgentToolRuntime{
		Home: t.TempDir(),
		Cfg:  &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	ctx := WithToolCompletionCapture(context.Background())

	heredoc := "cat > note.txt <<'EOF'\nhello\nEOF"
	out, err := shellTool.Handle(ctx, `{"command":`+mustJSON(t, heredoc)+`}`)
	if err != nil {
		t.Fatalf("heredoc command: %v", err)
	}
	if strings.Contains(out.(string), "shell_edit_diff") {
		t.Fatal("the model-facing result must not carry the snapshot diff")
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("shell completion was not captured")
	}
	diff, ok := DecodeShellEditDiff(completion.Output["shell_edit_diff"])
	if !ok || len(diff.Files) != 1 {
		t.Fatalf("shell_edit_diff = %#v ok=%v, want one file", completion.Output["shell_edit_diff"], ok)
	}
	if f := diff.Files[0]; f.Status != "created" || filepath.Base(f.Path) != "note.txt" || f.DisplayPath != "note.txt" {
		t.Fatalf("note.txt entry = %+v", f)
	}
	body, trunc := FormatToolStepResult(StepEvent{
		Kind: StepKindToolCompleted, ToolName: "shell", Output: completion.Output,
	}, DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if !strings.Contains(body, "Created `note.txt` (+1 -0)") || !strings.Contains(body, "```diff") {
		t.Fatalf("card body missing the diff block:\n%s", body)
	}
	if idx := strings.Index(body, "output:"); idx >= 0 && strings.Contains(body[idx:], "shell_edit_diff") {
		t.Fatalf("output JSON must not replay the snapshot diff:\n%s", body)
	}

	writeCmd := `python3 -c "open('a.txt','w').write('a\nB\n')"`
	if _, err := exec.LookPath("python3"); err != nil {
		writeCmd = `printf 'a\nB\n' > a.txt`
	}
	if _, err := shellTool.Handle(ctx, `{"command":`+mustJSON(t, writeCmd)+`}`); err != nil {
		t.Fatalf("write command: %v", err)
	}
	completion, ok = ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("second shell completion was not captured")
	}
	diff, ok = DecodeShellEditDiff(completion.Output["shell_edit_diff"])
	if !ok || len(diff.Files) != 1 {
		t.Fatalf("second shell_edit_diff = %#v ok=%v, want one file", completion.Output["shell_edit_diff"], ok)
	}
	if f := diff.Files[0]; f.Status != "updated" || filepath.Base(f.Path) != "a.txt" {
		t.Fatalf("a.txt entry = %+v", f)
	}

	// A read-only command never watches the repository.
	if _, err := shellTool.Handle(ctx, `{"command":`+mustJSON(t, "cat note.txt")+`}`); err != nil {
		t.Fatalf("read-only command: %v", err)
	}
	completion, ok = ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("read-only completion was not captured")
	}
	if _, has := completion.Output["shell_edit_diff"]; has {
		t.Fatalf("read-only command must not carry shell_edit_diff: %#v", completion.Output["shell_edit_diff"])
	}
}

// The model's copy of a shell result is compressed to save its context, while
// the completion output the surfaces draw the card from keeps what the command
// actually printed (owner 2026-10-25). The make filter strips the make[N]:
// progress lines, so a make run is where the two copies visibly diverge: at
// the moment the card settles it must not swap the streamed lines for the
// compressed text.
func TestShellToolCardKeepsTheOriginalOutputWhenTheModelGetsItCompressed(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skipf("make not available: %v", err)
	}
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "Makefile"), "all:\n"+
		"\t@echo \"make[1]: Entering directory '/tmp/app'\"\n"+
		"\t@for i in $$(seq 1 400); do echo \"make[2]: progress step xxxxxxxxxxxxxxxx [ OK ]\"; done\n"+
		"\t@echo \"ERROR: src/api.c:42:18: error: expected ';' before '}' token\"\n"+
		"\t@echo \"make[1]: Leaving directory '/tmp/app'\"\n")
	t.Chdir(root)
	ResetCompressors()
	defer ResetCompressors()

	shellTool, err := NewShellTool(NewState(root), &AgentToolRuntime{
		WorkspaceRoot: root,
		Cfg:           &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	ctx := WithToolCompletionCapture(context.Background())
	out, err := shellTool.Handle(ctx, `{"command":"make"}`)
	if err != nil {
		t.Fatalf("tool handle: %v", err)
	}

	// The model's result is the compressed one: noise gone, error kept.
	var model map[string]any
	if err := json.Unmarshal([]byte(out.(string)), &model); err != nil {
		t.Fatalf("decode model result: %v", err)
	}
	modelStdout, _ := model["stdout"].(string)
	if strings.Contains(modelStdout, "progress step") {
		t.Fatalf("model stdout must not carry the compressed-away noise:\n%s", modelStdout)
	}
	if !strings.Contains(modelStdout, "ERROR: src/api.c:42:18") {
		t.Fatalf("model stdout must keep the error line:\n%s", modelStdout)
	}
	if _, ok := model["output_compression"]; !ok {
		t.Fatalf("model result must carry output_compression: %#v", model)
	}

	// The completion output keeps the original stdout for the card.
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("shell completion was not captured")
	}
	surfaceStdout, _ := completion.Output["stdout"].(string)
	if !strings.Contains(surfaceStdout, "progress step") || !strings.Contains(surfaceStdout, "ERROR: src/api.c:42:18") {
		t.Fatalf("completion stdout must be the original output:\n%s", surfaceStdout)
	}
	if _, ok := completion.Output["output_compression"]; !ok {
		t.Fatalf("completion must still carry output_compression: %#v", completion.Output)
	}
	compressedBytes, _ := completion.Output["stdout_compressed_bytes"].(int)
	if compressedBytes >= len(surfaceStdout) {
		t.Fatalf("stdout_compressed_bytes = %d, want < len(original) = %d", compressedBytes, len(surfaceStdout))
	}

	// The card body the surfaces draw comes from the completion output, so it
	// shows the original lines too.
	body, truncated := FormatToolStepResult(StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "shell",
		Input:    map[string]any{"command": "make"},
		Output:   completion.Output,
	}, DefaultMaxFormattedBody)
	if truncated {
		t.Fatal("unexpected truncation of the card body")
	}
	if !strings.Contains(body, "progress step") {
		t.Fatalf("card body must show the original output:\n%s", body)
	}
}

// TestBatchShellRequestShape pins the boundary a batch task's shell command is
// assembled from, without needing an OS sandbox: the task's directory is the
// whole write set, the read set is that directory plus the shared skill roots,
// and the project root and Forebrain home are denied (plans/150). The merge
// that turns those paths into the request a backend receives — and the way a
// configured allow_read "/" is kept out of it — is pinned by
// pkg/safety's TestExclusiveReadablePathsDoesNotInheritAllowRead.
func TestBatchShellRequestShape(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "project")
	workspace := filepath.Join(project, "tasks", "T", "1")
	home := filepath.Join(base, "home")
	skillRoot := filepath.Join(home, "skills", "demo")

	boundary := batchShellBoundary(workspace, []string{skillRoot}, nil, project, home)
	if want := []string{workspace}; !reflect.DeepEqual(boundary.writable, want) {
		t.Fatalf("writable = %#v, want %#v", boundary.writable, want)
	}
	wantRead := []string{workspace, skillRoot}
	if !reflect.DeepEqual(boundary.readable, wantRead) {
		t.Fatalf("readable = %#v, want %#v", boundary.readable, wantRead)
	}
	if len(boundary.denied) != 2 || boundary.denied[0] != project || boundary.denied[1] != home {
		t.Fatalf("denied = %#v, want the project root and the home", boundary.denied)
	}
	// A configured deny survives: the batch boundary narrows, never widens.
	withDeny := batchShellBoundary(workspace, []string{skillRoot}, []string{filepath.Join(project, "private")}, project, home)
	if !pathWithinAny(filepath.Join(project, "private"), withDeny.denied) {
		t.Fatalf("denied = %#v, want the configured deny kept", withDeny.denied)
	}

	// YOLO and danger-full-access are an operator's choices for an interactive
	// session, and neither may reach a batch task's shell.
	t.Setenv("FOREBRAIN_YOLO", "1")
	batchCfg := batchSandboxConfig(&appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess})
	if batchCfg.SandboxMode != appcfg.SandboxModeWorkspaceWrite || batchCfg.DefaultPermissions != "" {
		t.Fatalf("batch config = %+v, want workspace-write with no named profile", batchCfg)
	}
	if batchCfg.DangerFullAccessEnabled() {
		t.Fatal("a batch task's config must never be danger-full-access")
	}
}

// TestBatchShellRejectsEscalationRequests pins that a batch task cannot ask for
// more than its sandbox: the refusal happens before any command runs, so a
// model that reaches for the escalation fields learns they do not work
// (plans/150).
func TestBatchShellRejectsEscalationRequests(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "tasks", "T", "1")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(workspace, "escalated.txt")
	shellTool, err := NewShellTool(NewState(workspace), &AgentToolRuntime{Cfg: &appcfg.Root{}})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	ctx := WithTaskWorkspace(context.Background(), workspace)
	for _, input := range []map[string]any{
		{"command": "echo x > escalated.txt", "sandbox_permissions": "require_escalated"},
		{"command": "echo x > escalated.txt", "sandbox_permissions": "with_additional_permissions",
			"additional_permissions": map[string]any{"file_system": map[string]any{"entries": []any{}}}},
	} {
		raw, _ := json.Marshal(input)
		if _, err := shellTool.Handle(ctx, string(raw)); err == nil {
			t.Fatalf("batch shell accepted %v", input)
		}
		if _, statErr := os.Stat(probe); statErr == nil {
			t.Fatalf("batch shell ran a refused escalation request: %v", input)
		}
	}
}

// TestBatchShellIsConfinedToTheTaskWorkspace runs the real seatbelt sandbox
// against a batch task's boundary (plans/150). Nothing here is mocked: every
// case is a command the OS must run or must refuse, and the whole turn must ask
// nobody anything.
func TestBatchShellIsConfinedToTheTaskWorkspace(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the seatbelt sandbox only exists on macOS")
	}
	if testing.Short() {
		t.Skip("runs real sandboxed commands")
	}
	// YOLO is an operator's choice for an interactive session; it must change
	// nothing for an unattended batch task.
	t.Setenv("FOREBRAIN_YOLO", "1")

	base := t.TempDir()
	project := filepath.Join(base, "project")
	workspace := filepath.Join(project, "tasks", "T", "1")
	sibling := filepath.Join(project, "tasks", "T2", "1")
	home := filepath.Join(base, "home")
	skillRoot := filepath.Join(home, "skills", "demo")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{
		filepath.Join(workspace, "inputs"), filepath.Join(workspace, "outputs"), filepath.Join(workspace, ".work"),
		sibling, skillRoot, filepath.Join(home, "other"), outside,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(filepath.Join(sibling, "secret.txt"), "sibling secret")
	writeFile(filepath.Join(project, "shared.txt"), "project secret")
	writeFile(filepath.Join(skillRoot, "SKILL.md"), "# demo skill\n")
	writeFile(filepath.Join(home, "other", "private.txt"), "home secret")
	writeFile(filepath.Join(outside, "private.txt"), "outside secret")
	// The home the command is given is the test's own: the boundary denies it,
	// and that must hold whatever the operator's real home looks like.
	t.Setenv("HOME", home)

	st := NewState(project)
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "demo", RootDir: skillRoot}})
	var hookCalls atomic.Int32
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		hookCalls.Add(1)
		return "", false, nil
	})
	// A remembered grant for "/", on top of the configuration's ambient
	// allow_read "/": neither may reach the request the sandbox receives.
	snapshot := safety.Snapshot{FileSystemGrants: []safety.FileSystemPermissionGrant{{
		Entry: safety.FileSystemPermissionEntry{
			Path:   safety.NewFileSystemPermissionPath(string(filepath.Separator)),
			Access: safety.FileSystemAccessWrite,
		},
		Scope: safety.GrantScopeSession,
	}}}
	shellTool, err := NewShellTool(st, &AgentToolRuntime{
		Home: home, WorkspaceRoot: project, Cfg: &appcfg.Root{},
		PermissionSnapshot: func() safety.Snapshot { return snapshot },
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	ctx := WithProjectRoot(context.Background(), project)
	ctx = WithTaskWorkspace(ctx, workspace)

	type shellOutcome struct {
		stdout   string
		stderr   string
		exitCode int
		err      error
	}
	run := func(t *testing.T, input map[string]any) shellOutcome {
		t.Helper()
		raw, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			t.Fatalf("marshal %v: %v", input, marshalErr)
		}
		out, handleErr := shellTool.Handle(ctx, string(raw))
		body, _ := out.(string)
		var parsed struct {
			Stdout   string `json:"stdout"`
			Stderr   string `json:"stderr"`
			ExitCode int    `json:"exit_code"`
		}
		if body != "" {
			_ = json.Unmarshal([]byte(body), &parsed)
		}
		return shellOutcome{stdout: parsed.Stdout, stderr: parsed.Stderr, exitCode: parsed.ExitCode, err: handleErr}
	}
	mustSucceed := func(t *testing.T, command string) string {
		t.Helper()
		got := run(t, map[string]any{"command": command})
		if got.err != nil || got.exitCode != 0 {
			t.Fatalf("%q must succeed inside the task sandbox: exit=%d err=%v stderr=%s", command, got.exitCode, got.err, got.stderr)
		}
		return got.stdout
	}
	mustFail := func(t *testing.T, command string) {
		t.Helper()
		got := run(t, map[string]any{"command": command})
		if got.err == nil && got.exitCode == 0 {
			t.Fatalf("%q must be refused by the task sandbox; it succeeded with stdout=%q", command, got.stdout)
		}
	}
	// mustNotLeak is mustFail plus the mark the command would print if the
	// sandbox had let it through: a refusal that still returned the file would
	// otherwise read as a pass.
	mustNotLeak := func(t *testing.T, command, secret string) {
		t.Helper()
		got := run(t, map[string]any{"command": command})
		if strings.Contains(got.stdout, secret) {
			t.Fatalf("%q read %q out of the sandbox; stdout=%q", command, secret, got.stdout)
		}
		if got.err == nil && got.exitCode == 0 {
			t.Fatalf("%q must be refused by the task sandbox; it succeeded with stdout=%q", command, got.stdout)
		}
	}

	t.Run("the workspace is writable", func(t *testing.T) {
		mustSucceed(t, "echo ok > outputs/a.txt")
		body, readErr := os.ReadFile(filepath.Join(workspace, "outputs", "a.txt"))
		if readErr != nil {
			t.Fatalf("read the written file: %v", readErr)
		}
		if strings.TrimSpace(string(body)) != "ok" {
			t.Fatalf("outputs/a.txt = %q, want ok", string(body))
		}
		if got := mustSucceed(t, "cat outputs/a.txt"); strings.TrimSpace(got) != "ok" {
			t.Fatalf("reading inside the workspace = %q, want ok", got)
		}
	})

	t.Run("another task's directory is unreachable", func(t *testing.T) {
		mustFail(t, "echo x > ../../T2/1/pwn.txt")
		if _, statErr := os.Stat(filepath.Join(sibling, "pwn.txt")); statErr == nil {
			t.Fatal("a batch task wrote into a sibling task's directory")
		}
		mustNotLeak(t, "cat ../../T2/1/secret.txt", "sibling secret")
	})

	t.Run("the rest of the project is unreachable", func(t *testing.T) {
		mustNotLeak(t, "cat ../../../shared.txt", "project secret")
	})

	t.Run("the shared skill root is readable", func(t *testing.T) {
		got := mustSucceed(t, "cat "+filepath.Join(skillRoot, "SKILL.md"))
		if strings.TrimSpace(got) != "# demo skill" {
			t.Fatalf("skill read = %q", got)
		}
	})

	t.Run("the home and the outside world stay unreadable", func(t *testing.T) {
		mustNotLeak(t, "cat "+filepath.Join(home, "other", "private.txt"), "home secret")
		mustNotLeak(t, "cat $HOME/other/private.txt", "home secret")
		mustNotLeak(t, "cat "+filepath.Join(outside, "private.txt"), "outside secret")
		mustSucceed(t, "cat /etc/hosts")
	})

	t.Run("the temp directory is the task's own", func(t *testing.T) {
		if python, lookErr := exec.LookPath("python3"); lookErr == nil {
			got := mustSucceed(t, `"`+python+`" -c "import tempfile;print(tempfile.gettempdir())"`)
			// The interpreter reports the TMPDIR it was handed; compare the
			// resolved directories so /var and /private/var spellings match.
			if resolvedWorkingDir(t, strings.TrimSpace(got)) != resolvedWorkingDir(t, filepath.Join(workspace, ".work", "tmp")) {
				t.Fatalf("TMPDIR = %q, want %q", strings.TrimSpace(got), filepath.Join(workspace, ".work", "tmp"))
			}
		}
		escape := filepath.Join(os.TempDir(), "batch-escape")
		mustFail(t, `echo x > "`+escape+`"`)
		if _, statErr := os.Stat(escape); statErr == nil {
			t.Fatalf("a batch task wrote %s, outside its workspace", escape)
		}
	})

	t.Run("the home is the task's own", func(t *testing.T) {
		wantHome := filepath.Join(workspace, ".work", "home")
		got := run(t, map[string]any{"command": `echo "$HOME"`})
		if got.err != nil || got.exitCode != 0 {
			t.Fatalf("reading HOME must succeed in the task sandbox: exit=%d err=%v stderr=%s", got.exitCode, got.err, got.stderr)
		}
		if resolvedWorkingDir(t, strings.TrimSpace(got.stdout)) != resolvedWorkingDir(t, wantHome) {
			t.Fatalf("HOME = %q, want %q", strings.TrimSpace(got.stdout), wantHome)
		}
		// The real home stays unreadable across the boundary, so a login shell
		// must not be pointed at it: that read is the ".profile: Operation not
		// permitted" line every batch command used to carry on stderr.
		if strings.Contains(got.stderr, ".profile: Operation not permitted") {
			t.Fatalf("the task's shell read a profile out of the real home; stderr=%q", got.stderr)
		}
		mustSucceed(t, `echo hi > "$HOME/notes.txt"`)
		body, readErr := os.ReadFile(filepath.Join(wantHome, "notes.txt"))
		if readErr != nil {
			t.Fatalf("read the file written under the task's home: %v", readErr)
		}
		if strings.TrimSpace(string(body)) != "hi" {
			t.Fatalf(".work/home/notes.txt = %q, want hi", string(body))
		}
	})

	t.Run("escalation is refused before anything runs", func(t *testing.T) {
		got := run(t, map[string]any{
			"command": "echo x > escalated.txt", "sandbox_permissions": "require_escalated",
			"justification": "needs the host",
		})
		if got.err == nil {
			t.Fatalf("require_escalated must be refused, got exit=%d stdout=%q", got.exitCode, got.stdout)
		}
		if !strings.Contains(got.err.Error(), "task sandbox") {
			t.Fatalf("refusal = %v, want it to name the task sandbox", got.err)
		}
		if _, statErr := os.Stat(filepath.Join(workspace, "escalated.txt")); statErr == nil {
			t.Fatal("the command of a refused escalation ran")
		}
	})

	if calls := hookCalls.Load(); calls != 0 {
		t.Fatalf("a batch turn asked the action hook %d time(s); it must ask nobody anything", calls)
	}
}
