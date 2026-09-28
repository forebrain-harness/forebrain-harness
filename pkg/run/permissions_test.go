package run

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

func TestFileMutationValidatesPathBeforeApproval(t *testing.T) {
	for _, name := range []string{"edit_file", "write_file"} {
		for _, pathJSON := range []string{"", `"file_path":"",`, `"file_path":"   ",`} {
			t.Run(name+"/"+pathJSON, func(t *testing.T) {
				runner, st := outsideWorkspaceRunner(t, t.TempDir(), safety.ApprovalOnRequest)
				var fileTool *llm.Tool
				var err error
				if name == "edit_file" {
					fileTool, err = tool.NewFileEditTool(st)
				} else {
					fileTool, err = tool.NewFileWriteTool(st, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
				fileTool.Use(runner.newToolPermissionMiddleware())
				_, err = fileTool.Handle(context.Background(), `{`+pathJSON+`"old_string":"before","new_string":"after","content":"after"}`)
				if err == nil || !strings.Contains(err.Error(), "file_path is required") {
					t.Fatalf("invalid path must reach path validation without an approval, got %v", err)
				}
			})
		}
	}
}

func TestCWDFileToolsUseResolvedApprovalPath(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	for _, relative := range []bool{false, true} {
		t.Run(map[bool]string{false: "absolute", true: "relative"}[relative], func(t *testing.T) {
			runner, st := outsideWorkspaceRunner(t, project, safety.ApprovalOnRequest)
			ctx := llm.WithAgentSessionID(context.Background(), "session-1")
			target := filepath.Join(project, "file.txt")
			path := target
			if relative {
				path = "file.txt"
			}
			if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
				t.Fatal(err)
			}
			readTool, err := tool.NewFileReadTool(st)
			if err != nil {
				t.Fatal(err)
			}
			editTool, err := tool.NewFileEditTool(st)
			if err != nil {
				t.Fatal(err)
			}
			writeTool, err := tool.NewFileWriteTool(st, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, fileTool := range []*llm.Tool{readTool, editTool, writeTool} {
				fileTool.Use(runner.newToolPermissionMiddleware())
				input, _ := json.Marshal(map[string]any{"file_path": path, "old_string": "before", "new_string": "edited", "content": "written"})
				if _, err := fileTool.Handle(ctx, string(input)); err != nil {
					t.Fatalf("%s in cwd should run without approval: %v", fileTool.Name(), err)
				}
			}
			content, err := os.ReadFile(target)
			if err != nil || string(content) != "written" {
				t.Fatalf("content=%q err=%v", content, err)
			}

			// An explicit ask still produces the service's complete choice list.
			runner.permRuntimeGet().Store(runner.AppCfg).AddRule(safety.SourceSession, safety.BehaviorAsk,
				safety.PermissionRuleValue{ToolName: "Edit", RuleContent: path, SessionID: "session-1"})
			input, _ := json.Marshal(tool.FileEditInput{FilePath: path, OldString: "written", NewString: "approved"})
			_, err = editTool.Handle(ctx, string(input))
			var approval *tool.RequiresActionError
			if !errors.As(err, &approval) {
				t.Fatalf("explicit ask must require approval, got %v", err)
			}
			action, err := runner.Actions.Get(ctx, approval.ActionID)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(action.PayloadJSON), &payload); err != nil {
				t.Fatal(err)
			}
			if payload["resolved_file_path"] != target {
				t.Fatalf("approval must use the resolved file path, got %#v", payload)
			}
			request, _, _ := turn.BuildToolApprovalRequest(turn.ApprovalSource{
				ToolName: approval.ToolName, ActionKind: approval.ActionKind, ToolInput: approval.ToolInput,
			}, nil)
			var displayedPayload map[string]any
			if err := json.Unmarshal([]byte(request.ToolInputJSON), &displayedPayload); err != nil || displayedPayload["resolved_file_path"] != target {
				t.Fatalf("approval display lost the resolved target: %s (err=%v)", request.ToolInputJSON, err)
			}
			var choices []safety.ApprovalDecisionName
			for _, choice := range request.AvailableDecisions {
				choices = append(choices, choice.Decision)
			}
			if !slices.Equal(choices, []safety.ApprovalDecisionName{safety.DecisionAccept, safety.DecisionAcceptForSession, safety.DecisionCancel}) {
				t.Fatalf("approval choices=%v", choices)
			}
		})
	}
}

func TestSandboxAllowsFileMutationOnlyWhenTargetIsWritable(t *testing.T) {
	project := t.TempDir()
	outside := filepath.Join(string(filepath.Separator), "outside-workspace-policy")
	decision := safety.Decision{Behavior: safety.BehaviorAsk, Reason: "default_ask"}
	ctx := tool.WithProjectRoot(context.Background(), project)

	readOnly := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeReadOnly}, ProjectRoot: project}}
	if readOnly.sandboxAllowsFileMutation(ctx, "write_file", map[string]any{"resolved_file_path": filepath.Join(project, "a.go")}, decision) {
		t.Fatal("read-only policy must not auto-allow a file mutation")
	}

	workspaceWrite := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite}, ProjectRoot: project}}
	if !workspaceWrite.sandboxAllowsFileMutation(ctx, "write_file", map[string]any{"resolved_file_path": filepath.Join(project, "a.go")}, decision) {
		t.Fatal("workspace-write policy should auto-allow a project mutation")
	}
	if workspaceWrite.sandboxAllowsFileMutation(ctx, "edit_file", map[string]any{"resolved_file_path": filepath.Join(outside, "a.go")}, decision) {
		t.Fatal("workspace-write policy must not auto-allow an outside mutation")
	}
	if workspaceWrite.sandboxAllowsFileMutation(ctx, "custom_tool", map[string]any{"resolved_file_path": filepath.Join(project, "a.go")}, decision) {
		t.Fatal("a generic tool must not inherit file-mutation approval behavior")
	}
	if workspaceWrite.sandboxAllowsFileMutation(ctx, "write_file", map[string]any{"resolved_file_path": filepath.Join(project, "a.go")}, safety.Decision{Behavior: safety.BehaviorAsk, Reason: "matched_ask_rule"}) {
		t.Fatal("an explicit ask rule must not be overridden")
	}

	fullAccess := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}}}
	if !fullAccess.sandboxAllowsFileMutation(ctx, "edit_file", map[string]any{"resolved_file_path": filepath.Join(outside, "a.go")}, decision) {
		t.Fatal("danger-full-access should auto-allow file mutations")
	}
}

func TestSandboxAllowsFileMutationHonorsDeniedMetadataPaths(t *testing.T) {
	project := t.TempDir()
	runner := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite}, ProjectRoot: project}}
	decision := safety.Decision{Behavior: safety.BehaviorAsk, Reason: "default_ask"}
	target := filepath.Join(project, ".forebrain", "permissions.json")
	if runner.sandboxAllowsFileMutation(context.Background(), "write_file", map[string]any{"resolved_file_path": target}, decision) {
		t.Fatal("a denied metadata path must not be auto-allowed")
	}
	// Repository metadata is not agent configuration: .git carries the project's
	// ordinary write permission, so a git write must not raise an approval.
	gitPath := filepath.Join(project, ".git", "index.lock")
	if !runner.sandboxAllowsFileMutation(context.Background(), "write_file", map[string]any{"resolved_file_path": gitPath}, decision) {
		t.Fatal("a project .git write must be auto-allowed like any other project file")
	}
	// Except the places git executes from, which reach the host outside the
	// sandbox and so must still be put in front of the user.
	for _, surface := range []string{
		filepath.Join(project, ".git", "hooks", "pre-commit"),
		filepath.Join(project, ".git", "config"),
	} {
		if runner.sandboxAllowsFileMutation(context.Background(), "write_file", map[string]any{"resolved_file_path": surface}, decision) {
			t.Fatalf("a git execution surface must not be auto-allowed: %s", surface)
		}
	}
}

func TestSandboxAllowsFileMutationRequiresEveryPatchPath(t *testing.T) {
	project := t.TempDir()
	runner := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite}, ProjectRoot: project}}
	decision := safety.Decision{Behavior: safety.BehaviorAsk, Reason: "default_ask"}
	ctx := tool.WithProjectRoot(context.Background(), project)
	if !runner.sandboxAllowsFileMutation(ctx, "edit_file", map[string]any{
		"resolved_paths": []string{filepath.Join(project, "a.go"), filepath.Join(project, "b.go")},
	}, decision) {
		t.Fatal("all project paths should be writable")
	}
	if runner.sandboxAllowsFileMutation(ctx, "edit_file", map[string]any{
		"resolved_paths": []string{filepath.Join(project, "a.go"), filepath.Join(string(filepath.Separator), "outside", "b.go")},
	}, decision) {
		t.Fatal("one outside path must make the whole patch require approval")
	}
}

func TestApplyPatchSessionApprovalRequiresEveryPath(t *testing.T) {
	runner := &Runner{Deps: &Deps{}}
	for _, path := range []string{"/repo/a.go", "/repo/b.go"} {
		runner.permRuntimeGet().Store(runner.AppCfg).AddRule(safety.SourceSession, safety.BehaviorAllow, safety.PermissionRuleValue{
			ToolName: "apply_patch", RuleContent: path, BypassSandbox: true, SessionID: "session-1",
		})
	}
	ctx := llm.WithAgentSessionID(context.Background(), "session-1")
	approved, err := runner.applyPatchSessionApproval(ctx, map[string]any{"resolved_paths": []string{"/repo/a.go", "/repo/b.go"}})
	if err != nil || !approved {
		t.Fatalf("approved=%t err=%v", approved, err)
	}
	approved, err = runner.applyPatchSessionApproval(ctx, map[string]any{"resolved_paths": []string{"/repo/a.go", "/repo/c.go"}})
	if err != nil || approved {
		t.Fatalf("partial path approval must not authorize patch: approved=%t err=%v", approved, err)
	}
}

func TestApplyPatchReadOnlyNeverRejectsWithoutPrompt(t *testing.T) {
	runner := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeReadOnly}}}
	runner.permRuntimeGet().Store(runner.AppCfg).SetApprovalPolicy(safety.ApprovalPolicy{Mode: safety.ApprovalNever})
	_, _, err := runner.actionHook(context.Background(), "apply_patch", map[string]any{"resolved_paths": []string{"/repo/a.go"}})
	if err == nil || !strings.Contains(err.Error(), "writing is blocked by read-only sandbox") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// A plan-mode shell command whose effect could not be proven read-only is a
// question, and the runtime has to put it: the tool marks the payload with
// force_tool_approval, which has to carry past a policy default that would
// otherwise run the command unattended. Before it did, the plan-mode gate
// refused these commands outright and the approval path its own comment
// promised never opened under any configuration.
func TestPlanModeUnprovenShellCommandReachesThePromptUnderOnRequest(t *testing.T) {
	runner := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{}}}
	runner.permRuntimeGet().Store(runner.AppCfg).SetApprovalPolicy(safety.ApprovalPolicy{Mode: safety.ApprovalOnRequest})

	_, pending, err := runner.actionHook(context.Background(), "shell", map[string]any{
		"command": "python3 script.py", "force_tool_approval": true,
		"approval_reason": "plan_mode_unproven_command", "justification": "plan mode is active",
	})
	if err == nil || pending {
		t.Fatalf("pending=%v err=%v, want the approval prompt to be attempted", pending, err)
	}
	if !strings.Contains(err.Error(), "actions service is unavailable") {
		t.Fatalf("err=%v, want the actions-service error that only an attempted prompt produces", err)
	}
}

// With approval turned off there is no way to ask, and the refusal comes from
// the user's own policy rather than from the gate silently deciding for them.
// unless-trusted prompts for everything it does not recognise, and the plan-mode
// marking must not accidentally become an exemption from that: the user is the
// one who decides a command nothing can prove read-only.
func TestPlanModeUnprovenShellCommandIsAskedUnderUnlessTrusted(t *testing.T) {
	runner := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{}}}
	runner.permRuntimeGet().Store(runner.AppCfg).SetApprovalPolicy(safety.ApprovalPolicy{Mode: safety.ApprovalUnlessTrusted})

	_, pending, err := runner.actionHook(context.Background(), "shell", map[string]any{
		"command": "python3 script.py", "force_tool_approval": true,
		"approval_reason": "plan_mode_unproven_command",
	})
	if err == nil || pending {
		t.Fatalf("pending=%v err=%v, want the approval prompt to be attempted", pending, err)
	}
	if !strings.Contains(err.Error(), "actions service is unavailable") {
		t.Fatalf("err=%v, want the actions-service error that only an attempted prompt produces", err)
	}
}

func TestPlanModeUnprovenShellCommandIsDeniedByNeverPolicy(t *testing.T) {
	runner := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{}}}
	runner.permRuntimeGet().Store(runner.AppCfg).SetApprovalPolicy(safety.ApprovalPolicy{Mode: safety.ApprovalNever})

	_, _, err := runner.actionHook(context.Background(), "shell", map[string]any{
		"command": "python3 script.py", "force_tool_approval": true,
		"approval_reason": "plan_mode_unproven_command",
	})
	if err == nil || !strings.Contains(err.Error(), "approval policy is never") {
		t.Fatalf("err=%v, want a policy-level denial", err)
	}
}

// The gate refuses without asking, so the payload the gate would have marked
// never reaches the approval layer at all.
func TestPlanModeUnprovenShellCommandRunsWhenPolicyAlreadyAllowsIt(t *testing.T) {
	runner := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{}}}
	store := runner.permRuntimeGet().Store(runner.AppCfg)
	store.SetApprovalPolicy(safety.ApprovalPolicy{Mode: safety.ApprovalOnRequest})
	store.AddRule(safety.SourceSession, safety.BehaviorAllow, safety.PermissionRuleValue{
		ToolName: "Bash", RuleContent: "python3 script.py", SessionID: "session-1",
	})

	// A rule the user wrote is their standing decision: it is what ends the
	// question, exactly as it does for a protected-path write. YOLO answers the
	// same way and is covered by its own test.
	ctx := llm.WithAgentSessionID(context.Background(), "session-1")
	if _, pending, err := runner.actionHook(ctx, "shell", map[string]any{
		"command": "python3 script.py", "force_tool_approval": true,
	}); err != nil || pending {
		t.Fatalf("pending=%v err=%v, want the user's own allow rule to answer", pending, err)
	}
}

func TestPayloadBoolAcceptsBooleanStrings(t *testing.T) {
	payload := map[string]any{
		"first":  false,
		"second": "true",
	}
	if !payloadBool(payload, "first", "second") {
		t.Fatalf("expected string true to be accepted")
	}
	if payloadBool(map[string]any{"value": "false"}, "value") {
		t.Fatalf("expected false string to remain false")
	}
}

func TestPayloadForcesToolApproval(t *testing.T) {
	if !payloadForcesToolApproval(map[string]any{"force_tool_approval": true}) {
		t.Fatalf("expected force approval flag")
	}
	if payloadForcesToolApproval(map[string]any{"force_tool_approval": "false"}) {
		t.Fatalf("false force approval flag must not force approval")
	}
}

func TestPermissionInputUsesRequestPermissionsMarker(t *testing.T) {
	if got := permissionInput("request_permissions", map[string]any{"request_permissions": true}); got != "request_permissions" {
		t.Fatalf("unexpected permission input %q", got)
	}
}

func TestPermissionInputExtractsFilePathForFileTools(t *testing.T) {
	cases := []struct {
		kind     string
		payload  map[string]any
		expected string
	}{
		{"read_file", map[string]any{"file_path": "/repo/src/app.go"}, "/repo/src/app.go"},
		{"write_file", map[string]any{"file_path": "/repo/out.txt", "content": "x"}, "/repo/out.txt"},
		{"edit_file", map[string]any{"file_path": "/repo/edit.go", "old_string": "a", "new_string": "b"}, "/repo/edit.go"},
		{"multi_edit", map[string]any{"file_path": "/repo/multi.go"}, "/repo/multi.go"},
	}
	for _, tc := range cases {
		if got := permissionInput(tc.kind, tc.payload); got != tc.expected {
			t.Fatalf("%s: got %q want %q", tc.kind, got, tc.expected)
		}
	}
}

func TestPermissionInputExtractsURLForWebFetch(t *testing.T) {
	if got := permissionInput("web_fetch", map[string]any{"url": "https://example.com/x"}); got != "https://example.com/x" {
		t.Fatalf("unexpected web_fetch input %q", got)
	}
	if got := permissionInput("web_search", map[string]any{"query": "go modules"}); got != "go modules" {
		t.Fatalf("unexpected web_search input %q", got)
	}
}

func TestPermissionInputUsesToolNameForEmptyGenericPayload(t *testing.T) {
	if got := permissionInput("custom_tool", map[string]any{}); got != "custom_tool" {
		t.Fatalf("unexpected generic permission input %q", got)
	}
}

func TestFormatPermissionDenyErrorIncludesMatchedRule(t *testing.T) {
	rule := &safety.PermissionRule{
		Behavior: safety.BehaviorDeny,
		Source:   safety.SourceLocalSettings,
		Value:    safety.PermissionRuleValue{ToolName: "Bash", RuleContent: "rm -rf:*"},
	}
	err := formatPermissionDenyError(safety.Decision{
		Behavior: safety.BehaviorDeny,
		Reason:   "matched_deny_rule",
		Matched:  rule,
	}, "shell", map[string]any{"command": "rm -rf /tmp/x"})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Bash(rm -rf:*)") {
		t.Fatalf("error should include rule string, got %q", msg)
	}
	if !strings.Contains(msg, "localSettings") {
		t.Fatalf("error should include source, got %q", msg)
	}
	if !strings.Contains(msg, "matched_deny_rule") {
		t.Fatalf("error should include reason, got %q", msg)
	}
	if !strings.Contains(msg, "permission denied") {
		t.Fatalf("error should classify as permission_denied via substring, got %q", msg)
	}
}

func TestFormatPermissionDenyErrorFallsBackToSentinel(t *testing.T) {
	err := formatPermissionDenyError(safety.Decision{Behavior: safety.BehaviorDeny}, "", nil)
	if !errors.Is(err, errToolRequestPreviouslyDenied) {
		t.Fatalf("expected sentinel fallback, got %v", err)
	}
}

func TestFormatPermissionDenyErrorWithReasonOnly(t *testing.T) {
	err := formatPermissionDenyError(safety.Decision{
		Behavior: safety.BehaviorDeny,
		Reason:   "dont_ask_default_deny",
	}, "", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "dont_ask_default_deny") {
		t.Fatalf("expected reason in error, got %q", err.Error())
	}
}

func TestFormatPermissionDenyErrorIncludesSuggestion(t *testing.T) {
	err := formatPermissionDenyError(safety.Decision{
		Behavior: safety.BehaviorDeny,
		Reason:   "matched_deny_rule",
		Matched: &safety.PermissionRule{
			Behavior: safety.BehaviorDeny,
			Source:   safety.SourceLocalSettings,
			Value:    safety.PermissionRuleValue{ToolName: "Bash", RuleContent: "rm -rf:*"},
		},
	}, "shell", map[string]any{"command": "git push origin main"})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Bash(git push:*)") {
		t.Fatalf("expected suggested prefix rule, got %q", msg)
	}
	if !strings.Contains(msg, "Bash(git push origin main)") {
		t.Fatalf("expected suggested exact rule alongside the prefix, got %q", msg)
	}
	if !strings.Contains(msg, "to allow") {
		t.Fatalf("expected suggestion hint phrase, got %q", msg)
	}
}

func TestFormatPermissionDenyErrorSuggestionForFileTool(t *testing.T) {
	err := formatPermissionDenyError(safety.Decision{
		Behavior: safety.BehaviorDeny,
		Reason:   "matched_deny_rule",
		Matched: &safety.PermissionRule{
			Behavior: safety.BehaviorDeny,
			Source:   safety.SourceLocalSettings,
			Value:    safety.PermissionRuleValue{ToolName: "Write", RuleContent: "/etc/*"},
		},
	}, "write_file", map[string]any{"file_path": "/repo/src/secret.go"})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Write(/repo/src/*)") {
		t.Fatalf("expected directory prefix rule, got %q", msg)
	}
}

// applyPatchDecision names the three outcomes the apply_patch branch of
// actionHook can produce. Without an actions service an approval prompt cannot
// be created, so "ask" surfaces as that unavailability rather than as a
// rejection; the two are distinguished by message.
type applyPatchDecision string

const (
	applyPatchAutoApprove applyPatchDecision = "auto-approve"
	applyPatchAsk         applyPatchDecision = "ask"
	applyPatchReject      applyPatchDecision = "reject"
)

func applyPatchOutcome(t *testing.T, runner *Runner, ctx context.Context, payload map[string]any) (applyPatchDecision, string) {
	t.Helper()
	_, _, err := runner.actionHook(ctx, "apply_patch", payload)
	switch {
	case err == nil:
		return applyPatchAutoApprove, ""
	case strings.Contains(err.Error(), "actions service is unavailable"):
		return applyPatchAsk, err.Error()
	case strings.Contains(err.Error(), "patch rejected:"):
		return applyPatchReject, err.Error()
	default:
		t.Fatalf("unexpected error: %v", err)
		return "", ""
	}
}

// The whole decision table for a patch, so a change to one branch cannot
// silently move another. The pairing that matters most: a policy that refuses
// approval prompts rejects an unconstrained patch outright, but still lets a
// patch the sandbox can contain through without asking.
func TestApplyPatchDecisionTable(t *testing.T) {
	project := t.TempDir()
	inside := map[string]any{"resolved_paths": []string{filepath.Join(project, "a.go")}}
	outside := map[string]any{"resolved_paths": []string{filepath.Join(string(filepath.Separator), "outside-workspace", "a.go")}}
	ctx := tool.WithProjectRoot(context.Background(), project)

	granularNoSandbox := safety.ApprovalPolicy{
		Mode:     safety.ApprovalGranular,
		Granular: safety.GranularApprovalConfig{Rules: true, MCPElicitations: true},
	}

	for _, tc := range []struct {
		name        string
		mode        appcfg.SandboxMode
		policy      safety.ApprovalPolicy
		payload     map[string]any
		want        applyPatchDecision
		wantMessage string
	}{
		{
			name: "never rejects a patch reaching outside the workspace",
			mode: appcfg.SandboxModeWorkspaceWrite, policy: safety.ApprovalPolicy{Mode: safety.ApprovalNever},
			payload: outside, want: applyPatchReject, wantMessage: "writing outside of the project",
		},
		{
			name: "granular without sandbox approval rejects the same patch",
			mode: appcfg.SandboxModeWorkspaceWrite, policy: granularNoSandbox,
			payload: outside, want: applyPatchReject, wantMessage: "writing outside of the project",
		},
		{
			name: "read-only names the sandbox rather than the project",
			mode: appcfg.SandboxModeReadOnly, policy: safety.ApprovalPolicy{Mode: safety.ApprovalNever},
			payload: inside, want: applyPatchReject, wantMessage: "writing is blocked by read-only sandbox",
		},
		{
			name: "on-request asks about a patch reaching outside the workspace",
			mode: appcfg.SandboxModeWorkspaceWrite, policy: safety.ApprovalPolicy{Mode: safety.ApprovalOnRequest},
			payload: outside, want: applyPatchAsk,
		},
		{
			// The sandbox can contain this patch, so unless-trusted is the only
			// policy that still wants to see it.
			name: "unless-trusted asks even about a contained patch",
			mode: appcfg.SandboxModeWorkspaceWrite, policy: safety.ApprovalPolicy{Mode: safety.ApprovalUnlessTrusted},
			payload: inside, want: applyPatchAsk,
		},
		{
			// A policy that never prompts is not a policy that never writes: the
			// sandbox can contain this patch, so it runs without an approval.
			name: "never approves a patch the sandbox can contain",
			mode: appcfg.SandboxModeWorkspaceWrite, policy: safety.ApprovalPolicy{Mode: safety.ApprovalNever},
			payload: inside, want: applyPatchAutoApprove,
		},
		{
			name: "danger-full-access approves without asking",
			mode: appcfg.SandboxModeDangerFullAccess, policy: safety.ApprovalPolicy{Mode: safety.ApprovalOnRequest},
			payload: outside, want: applyPatchAutoApprove,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{SandboxMode: tc.mode}, ProjectRoot: project}}
			runner.permRuntimeGet().Store(runner.AppCfg).SetApprovalPolicy(tc.policy)

			got, message := applyPatchOutcome(t, runner, ctx, tc.payload)
			if got != tc.want {
				t.Fatalf("decision = %s (%s), want %s", got, message, tc.want)
			}
			if tc.wantMessage != "" && !strings.Contains(message, tc.wantMessage) {
				t.Fatalf("message = %q, want it to mention %q", message, tc.wantMessage)
			}
		})
	}
}

func TestActionHookUsesPermissionRequestDecisionBeforeReviewer(t *testing.T) {
	for _, test := range []struct {
		name       string
		behavior   string
		message    string
		wantDenied bool
	}{
		{name: "allow", behavior: "allow"},
		{name: "deny", behavior: "deny", message: "blocked by policy", wantDenied: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := `echo '{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"` + test.behavior + `","message":"` + test.message + `"}}}'`
			cfg := &appcfg.Root{Hooks: appcfg.HooksSettings{
				hook.EventPermissionRequest: {{Matcher: "Bash", Hooks: []appcfg.HookCommand{{Type: appcfg.HookTypeCommand, Command: command}}}},
			}}
			store := safety.NewStore()
			// The hook runtime lives on the tool state, which is where Load
			// puts it and where the Runner reads it back from.
			tools := tool.NewState(t.TempDir())
			tools.SetRuntimeValue("hooks_runtime", &hook.Runtime{
				Home: t.TempDir(), Cfg: cfg,
				PermissionMode: func() string { return "on-request" },
			})
			runner := &Runner{Deps: &Deps{AppCfg: cfg}, permRuntime: safety.NewRuntimeWithStore(store), tools: tools}
			_, pending, err := runner.actionHook(context.Background(), "shell", map[string]any{
				"command": "curl example.com", "force_tool_approval": true,
			})
			if test.wantDenied {
				if err == nil || pending || !strings.Contains(err.Error(), test.message) {
					t.Fatalf("pending=%v err=%v", pending, err)
				}
				return
			}
			if err != nil || pending {
				t.Fatalf("pending=%v err=%v", pending, err)
			}
		})
	}
}

func TestPermissionRequestHookPayloads(t *testing.T) {
	tool, input, aliases := permissionRequestHookPayload("shell", map[string]any{
		"command": "curl example.com",
		"network_approval_context": safety.NetworkApprovalContext{
			Host: "Example.COM.", Protocol: safety.NetworkApprovalHTTPS,
		},
		"network_port": 8443,
	})
	if tool != "Bash" || input["command"] != "curl example.com" || input["description"] != "network-access https://example.com:8443" || len(aliases) != 0 {
		t.Fatalf("tool=%q input=%+v aliases=%v", tool, input, aliases)
	}
	tool, input, aliases = permissionRequestHookPayload("apply_patch", map[string]any{"patch": "*** Begin Patch"})
	if tool != "apply_patch" || input["command"] != "*** Begin Patch" || len(aliases) != 2 || aliases[0] != "Write" || aliases[1] != "Edit" {
		t.Fatalf("tool=%q input=%+v aliases=%v", tool, input, aliases)
	}
}

func TestPermissionRequestHookOnlyRunsForApprovalActions(t *testing.T) {
	runner := &Runner{Deps: &Deps{}}
	if !runner.permissionRequestHookApplies("shell") || !runner.permissionRequestHookApplies("apply_patch") || !runner.permissionRequestHookApplies("mcp__server__tool") {
		t.Fatal("approval actions were not recognized")
	}
	if runner.permissionRequestHookApplies("write_file") || runner.permissionRequestHookApplies("request_permissions") || runner.permissionRequestHookApplies("exit_plan_mode") {
		t.Fatal("non-approval actions must not run PermissionRequest hooks")
	}
}

func TestPermissionAPIApplyEvaluateAndSnapshot(t *testing.T) {
	const sessionID = "session-1"
	r := &Runner{Deps: &Deps{}}
	r.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type:        safety.UpdateSetMode,
		Destination: safety.DestinationSession,
		SessionID:   sessionID,
		Mode:        safety.ModeOnRequest,
	})
	r.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type:        safety.UpdateAddRules,
		Destination: safety.DestinationSession,
		SessionID:   sessionID,
		Behavior:    safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{{
			ToolName:    "Bash",
			RuleContent: "npm run:*",
		}},
	})
	d := r.EvaluatePermissionForSession(sessionID, "Bash", "npm run build")
	if d.Behavior != safety.BehaviorAllow {
		t.Fatalf("expected allow, got %s (%s)", d.Behavior, d.Reason)
	}
	snap := r.PermissionSnapshotForSession(sessionID)
	if snap.Mode != safety.ModeOnRequest {
		t.Fatalf("unexpected mode: %s", snap.Mode)
	}
	list := snap.Rules[safety.SourceSession][safety.BehaviorAllow]
	if len(list) != 1 || list[0].RuleContent != "npm run:*" {
		t.Fatalf("unexpected rules: %#v", list)
	}
}

func TestPermissionRulesPersistAcrossRunners(t *testing.T) {
	home := t.TempDir()
	r1 := &Runner{Deps: &Deps{Home: home}}
	r1.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type:        safety.UpdateAddRules,
		Destination: safety.DestinationLocalSettings,
		Behavior:    safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{{
			ToolName:    "Bash",
			RuleContent: "git status",
		}},
	})
	// local settings should persist to state/permissions
	r1.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type:        safety.UpdateAddRules,
		Destination: safety.DestinationLocalSettings,
		Behavior:    safety.BehaviorDeny,
		Rules: []safety.PermissionRuleValue{{
			ToolName:    "Bash",
			RuleContent: "rm -rf:*",
		}},
	})
	r2 := &Runner{Deps: &Deps{Home: home}}
	r2.permRuntimeGet().LoadFromDisk(r2.AppCfg, r2.permPaths())
	if got := r2.permRuntimeGet().Mode(r2.AppCfg); got != safety.ModeOnRequest {
		t.Fatalf("approval mode must come from configuration, got %s", got)
	}
	if d := r2.permRuntimeGet().EvaluateRaw("", "Bash", "git status", r2.AppCfg); d.Behavior != safety.BehaviorAllow {
		t.Fatalf("expected allow from the persisted local rule, got %s", d.Behavior)
	}
	if d := r2.permRuntimeGet().EvaluateRaw("", "Bash", "rm -rf tmp", r2.AppCfg); d.Behavior != safety.BehaviorDeny {
		t.Fatalf("expected deny from persisted local rule, got %s", d.Behavior)
	}

}

// localPermissionSettingsFile mirrors the JSON shape safety.Runtime persists
// (safety.permissionSettingsFile is unexported, so this test — which checks
// the on-disk file directly rather than through Runtime's own API —
// duplicates just the shape it needs).
type localPermissionSettingsFile struct {
	Rules map[safety.PermissionBehavior][]safety.PermissionRuleValue `json:"rules,omitempty"`
}

func TestCommandPermissionPrefixesPersistAsTokenArrays(t *testing.T) {
	home := t.TempDir()
	runner := &Runner{Deps: &Deps{Home: home}}
	runner.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type: safety.UpdateAddRules, Destination: safety.DestinationLocalSettings,
		Behavior: safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{{
			ToolName: "Bash", CommandPrefix: []string{"npm", "run", "build"}, BypassSandbox: true,
		}},
	})

	// Permission settings live under the active agent's workspace root, which
	// for a runner with no explicit workspace is the main agent's.
	raw, err := os.ReadFile(filepath.Join(home, "workspace", "state", "permissions", "local_settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored localPermissionSettingsFile
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	rules := stored.Rules[safety.BehaviorAllow]
	if len(rules) != 1 || rules[0].RuleContent != "" ||
		!slices.Equal(rules[0].CommandPrefix, []string{"npm", "run", "build"}) {
		t.Fatalf("stored command rules=%+v", rules)
	}

	reloaded := &Runner{Deps: &Deps{Home: home}}
	reloaded.permRuntimeGet().LoadFromDisk(reloaded.AppCfg, reloaded.permPaths())
	if decision := reloaded.EvaluatePermission("Bash", "npm run build -- --watch"); decision.Matched == nil || !decision.BypassSandbox {
		t.Fatalf("reloaded decision=%+v", decision)
	}
}

func TestExplainPermission(t *testing.T) {
	const sessionID = "session-1"
	r := &Runner{Deps: &Deps{}}
	r.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type:        safety.UpdateAddRules,
		Destination: safety.DestinationSession,
		SessionID:   sessionID,
		Behavior:    safety.BehaviorAllow,
		Rules:       []safety.PermissionRuleValue{{ToolName: "Bash", RuleContent: "npm run:*"}},
	})
	ex := r.ExplainPermissionForSession(sessionID, "Bash", "npm run build")
	if ex.Decision.Behavior != safety.BehaviorAllow {
		t.Fatalf("expected allow decision, got %s", ex.Decision.Behavior)
	}
	found := false
	for _, it := range ex.Rules {
		if it.Behavior == safety.BehaviorAllow && it.Matched {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected matched allow rule in explain output")
	}
}

func TestSessionRulesAreNotPersisted(t *testing.T) {
	home := t.TempDir()
	r1 := &Runner{Deps: &Deps{Home: home}}
	r1.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type:        safety.UpdateAddRules,
		Destination: safety.DestinationSession,
		SessionID:   "session-1",
		Behavior:    safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{{
			ToolName:    "Bash",
			RuleContent: "npm run:*",
		}},
	})

	r2 := &Runner{Deps: &Deps{Home: home}}
	r2.permRuntimeGet().LoadFromDisk(r2.AppCfg, r2.permPaths())
	if got := r2.permRuntimeGet().Store(r2.AppCfg).Rules(safety.SourceSession, safety.BehaviorAllow); len(got) != 0 {
		t.Fatalf("expected no persisted session rules, got %#v", got)
	}
	if d := r2.permRuntimeGet().EvaluateRaw("", "Bash", "npm run build", r2.AppCfg); d.Matched != nil {
		t.Fatalf("unexpected persisted session rule: %+v", d)
	}
}

func TestRuntimeYOLOAllowsWithoutChangingPersistedPolicy(t *testing.T) {
	// YOLO is read from the environment, not carried on the Runner.
	t.Setenv(safety.EnvYOLO, "1")
	home := t.TempDir()
	r := &Runner{Deps: &Deps{Home: home}}
	r.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type:        safety.UpdateAddRules,
		Destination: safety.DestinationLocalSettings,
		Behavior:    safety.BehaviorDeny,
		Rules: []safety.PermissionRuleValue{{
			ToolName:    "Bash",
			RuleContent: "rm -rf:*",
		}},
	})
	if snap := r.PermissionSnapshot(); snap.Mode != safety.ModeOnRequest {
		t.Fatalf("expected configured approval mode to remain visible, got %s", snap.Mode)
	}
	if d := r.EvaluatePermission("Bash", "git status"); d.Behavior != safety.BehaviorAllow {
		t.Fatalf("expected yolo runtime override to allow, got %s (%s)", d.Behavior, d.Reason)
	}
	if d := r.EvaluatePermission("Bash", "git status"); d.Reason != "yolo" {
		t.Fatalf("expected yolo runtime reason, got %s", d.Reason)
	}
	if d := r.EvaluatePermission("Bash", "git status"); d.Reason != "yolo" {
		t.Fatalf("expected yolo runtime reason, got %s", d.Reason)
	}

	r2 := &Runner{Deps: &Deps{Home: home}}
	r2.permRuntimeGet().LoadFromDisk(r2.AppCfg, r2.permPaths())
	if got := r2.permRuntimeGet().Mode(r2.AppCfg); got != safety.ModeOnRequest {
		t.Fatalf("runtime yolo must not alter persisted approval policy, got %s", got)
	}
}

func TestRuntimeYOLOOverridesNeverPolicyDefaultsButHonorsExplicitDeny(t *testing.T) {
	// YOLO is read from the environment, not carried on the Runner.
	t.Setenv(safety.EnvYOLO, "1")
	const sessionID = "session-yolo"
	cfg := &appcfg.Root{
		SandboxMode:    appcfg.SandboxModeDangerFullAccess,
		ApprovalPolicy: appcfg.NewApprovalPolicy(appcfg.ApprovalPolicyNever),
	}
	r := &Runner{Deps: &Deps{Home: t.TempDir(), AppCfg: cfg}}
	rt := r.permRuntimeGet()
	rt.LoadFromDisk(r.AppCfg, r.permPaths())
	rt.Store(r.AppCfg).AddRule(safety.SourceSession, safety.BehaviorDeny, safety.PermissionRuleValue{
		ToolName: "Bash", RuleContent: "shutdown:*", SessionID: sessionID,
	})

	for _, call := range []struct {
		tool  string
		input string
	}{
		{tool: "edit_file", input: "/repo/main.go"},
		{tool: "shell", input: "rm -rf /tmp/build-output"},
	} {
		decision := r.EvaluatePermissionForSession(sessionID, call.tool, call.input)
		if decision.Behavior != safety.BehaviorAllow || decision.Reason != "yolo" || !decision.BypassSandbox {
			t.Fatalf("%s decision=%+v, want yolo allow", call.tool, decision)
		}
	}

	decision := r.EvaluatePermissionForSession(sessionID, "shell", "shutdown now")
	if decision.Behavior != safety.BehaviorDeny || decision.Reason != "matched_deny_rule" {
		t.Fatalf("explicit deny was bypassed: %+v", decision)
	}
}

// An ask rule under the never policy reaches the runner as a deny only because
// nothing can prompt. YOLO is exactly the switch that says not to prompt, so it
// must run the call rather than refuse it: refusing would block the tools the
// user asked to be consulted about while every unmentioned tool runs freely,
// and with prompting off there is no way to resolve it.
func TestRuntimeYOLOOverridesAskRuleConvertedByNeverPolicy(t *testing.T) {
	// YOLO is read from the environment, not carried on the Runner.
	t.Setenv(safety.EnvYOLO, "1")
	const sessionID = "session-yolo-ask"
	cfg := &appcfg.Root{
		SandboxMode:    appcfg.SandboxModeDangerFullAccess,
		ApprovalPolicy: appcfg.NewApprovalPolicy(appcfg.ApprovalPolicyNever),
	}
	r := &Runner{Deps: &Deps{Home: t.TempDir(), AppCfg: cfg}}
	rt := r.permRuntimeGet()
	rt.LoadFromDisk(r.AppCfg, r.permPaths())
	rt.Store(r.AppCfg).AddRule(safety.SourceSession, safety.BehaviorAsk, safety.PermissionRuleValue{
		ToolName: "Bash", RuleContent: "git push:*", SessionID: sessionID,
	})

	decision := r.EvaluatePermissionForSession(sessionID, "shell", "git push origin main")
	if decision.Behavior != safety.BehaviorAllow || decision.Reason != "yolo" {
		t.Fatalf("ask rule under never policy was refused with no prompt available: %+v", decision)
	}

	// Explain describes what the runner does, so it must reach the same answer.
	explained := r.ExplainPermissionForSession(sessionID, "shell", "git push origin main")
	if explained.Decision.Behavior != decision.Behavior || explained.Decision.Reason != decision.Reason {
		t.Fatalf("explain diverged from evaluate: explain=%+v evaluate=%+v", explained.Decision, decision)
	}
}

// Full Access is the config-level half of the same escape: it relaxes the
// denies the never policy generates for calls no rule mentions, and leaves the
// rules themselves alone.
func TestDangerFullAccessRelaxesOnlyPolicyGeneratedDenies(t *testing.T) {
	const sessionID = "session-full-access"
	cfg := &appcfg.Root{
		SandboxMode:    appcfg.SandboxModeDangerFullAccess,
		ApprovalPolicy: appcfg.NewApprovalPolicy(appcfg.ApprovalPolicyNever),
	}
	r := &Runner{Deps: &Deps{Home: t.TempDir(), AppCfg: cfg}}
	rt := r.permRuntimeGet()
	rt.LoadFromDisk(r.AppCfg, r.permPaths())
	rt.Store(r.AppCfg).AddRule(safety.SourceSession, safety.BehaviorDeny, safety.PermissionRuleValue{
		ToolName: "Bash", RuleContent: "shutdown:*", SessionID: sessionID,
	})

	unmatched := r.EvaluatePermissionForSession(sessionID, "edit_file", "/repo/main.go")
	if unmatched.Behavior != safety.BehaviorAllow || unmatched.Reason != "danger_full_access" || !unmatched.BypassSandbox {
		t.Fatalf("full access still refuses an unmatched call: %+v", unmatched)
	}

	denied := r.EvaluatePermissionForSession(sessionID, "shell", "shutdown now")
	if denied.Behavior != safety.BehaviorDeny {
		t.Fatalf("full access erased an explicit deny rule: %+v", denied)
	}

	explained := r.ExplainPermissionForSession(sessionID, "edit_file", "/repo/main.go")
	if explained.Decision.Reason != unmatched.Reason {
		t.Fatalf("explain diverged from evaluate: explain=%+v evaluate=%+v", explained.Decision, unmatched)
	}
}
