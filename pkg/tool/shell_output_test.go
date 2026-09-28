package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

func TestShellBuiltinRequiresApprovalForExpansionAndShellBypassPatterns(t *testing.T) {
	tests := []string{
		"echo $(whoami)",
		"cat <(curl x)",
		"echo ${HOME}",
		"echo $[1+1]",
		"=curl evil.example",
		"zmodload zsh/net/tcp",
		"sysopen file",
		"Invoke-Expression $x",
		"Start-Process powershell",
		"[System.IO.File]::WriteAllText('x','y')",
	}
	for _, tc := range tests {
		t.Run(tc, func(t *testing.T) {
			if !shellBuiltinRequiresApproval(tc) {
				t.Fatalf("expected command to require approval")
			}
		})
	}
}

func TestShellBuiltinRequiresApprovalAllowsOrdinaryReadOnlyCommand(t *testing.T) {
	if shellBuiltinRequiresApproval("git status --short") {
		t.Fatalf("ordinary read command requires approval")
	}
}

func TestShellBuiltinRequiresApprovalAllowsPOSIXReadOnlyCompoundStartingWithPowerShellAlias(t *testing.T) {
	cmd := "pwd && git rev-parse --show-toplevel && git worktree list --porcelain"
	if shellBuiltinRequiresApproval(cmd) {
		t.Fatalf("readonly POSIX compound command requires approval")
	}
}

func TestShellBuiltinRequiresApprovalWithFdDup(t *testing.T) {
	// fd duplication (2>&1) should not force approval on otherwise-safe commands
	if shellBuiltinRequiresApproval("git diff -- file 2>&1") {
		t.Fatalf("fd dup on read-only command should not require approval")
	}
	if shellBuiltinRequiresApproval("cd /tmp && git status --short 2>&1") {
		t.Fatalf("compound with fd dup should not require approval")
	}
	// But dangerous commands with fd dup still require approval
	if !shellBuiltinRequiresApproval("rm -rf / 2>&1") {
		t.Fatalf("dangerous command with fd dup must still require approval")
	}
}

func TestShellAllowedRootsIncludesRunScopedFilesystemGrants(t *testing.T) {
	staticRoot := t.TempDir()
	turnRoot := t.TempDir()
	sessionRoot := t.TempDir()
	writeRoot := t.TempDir()
	denyRoot := t.TempDir()
	store := safety.NewStore()
	store.AddFileSystemGrant(safety.FileSystemPermissionGrant{
		Entry: safety.FileSystemPermissionEntry{
			Path:   safety.FileSystemPermissionPath{Path: turnRoot},
			Access: safety.FileSystemAccessRead,
		},
		Scope:     safety.GrantScopeTurn,
		SessionID: "session-1",
		RunID:     "run-1",
	})
	store.AddFileSystemGrant(safety.FileSystemPermissionGrant{
		Entry: safety.FileSystemPermissionEntry{
			Path:   safety.FileSystemPermissionPath{Path: sessionRoot},
			Access: safety.FileSystemAccessRead,
		},
		Scope:     safety.GrantScopeSession,
		SessionID: "session-1",
	})
	store.AddFileSystemGrant(safety.FileSystemPermissionGrant{
		Entry: safety.FileSystemPermissionEntry{
			Path:   safety.FileSystemPermissionPath{Path: writeRoot},
			Access: safety.FileSystemAccessWrite,
		},
		Scope:     safety.GrantScopeSession,
		SessionID: "session-1",
	})
	store.AddFileSystemGrant(safety.FileSystemPermissionGrant{
		Entry: safety.FileSystemPermissionEntry{
			Path:   safety.FileSystemPermissionPath{Path: denyRoot},
			Access: safety.FileSystemAccessDeny,
		},
		Scope:     safety.GrantScopeSession,
		SessionID: "session-1",
	})

	st := NewState(staticRoot)
	rt := &AgentToolRuntime{PermissionSnapshot: store.Snapshot, PermissionSnapshotForSession: store.SnapshotForSession}
	readCommand := func(root string) string {
		return `rg -n "needle" ` + filepath.Join(root, "src", "file.txt") + ` | head -400`
	}
	for _, tt := range []struct {
		name   string
		runID  string
		root   string
		wantOK bool
	}{
		{name: "turn grant current run", runID: "run-1", root: turnRoot, wantOK: true},
		{name: "turn grant other run", runID: "run-2", root: turnRoot, wantOK: false},
		{name: "session grant other run", runID: "run-2", root: sessionRoot, wantOK: true},
		{name: "write grant includes read", runID: "run-2", root: writeRoot, wantOK: true},
		{name: "deny grant is not a root", runID: "run-2", root: denyRoot, wantOK: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := llm.WithAgentSessionID(WithRunID(context.Background(), tt.runID), "session-1")
			roots := shellAllowedRoots(ctx, st, rt)
			got := safety.ClassifyShellCommand(readCommand(tt.root), roots, staticRoot)
			if ok := got == safety.ShellMutationReadOnly; ok != tt.wantOK {
				t.Fatalf("run=%q root=%q allowed=%v, classified=%s want read-only=%v", tt.runID, tt.root, roots, got, tt.wantOK)
			}
		})
	}
}

func TestPowerShellSafetyBlocksDangerousFamiliesAndModes(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
	}{
		{name: "invoke expression", cmd: "Invoke-Expression $x"},
		{name: "encoded command", cmd: "pwsh -EncodedCommand SQBFAFgA"},
		{name: "bypass execution policy", cmd: "powershell -ExecutionPolicy Bypass -Command Get-ChildItem"},
		{name: "unsafe dotnet type", cmd: "[System.IO.File]::WriteAllText('x','y')"},
		{name: "remove git internals", cmd: "Remove-Item -Recurse .git"},
		{name: "write content", cmd: "Set-Content README.md x"},
		{name: "path traversal", cmd: "Get-Content ..\\secret.txt"},
		{name: "unsafe get content flag", cmd: "Get-Content README.md -Credential admin"},
		{name: "unsafe select string flag", cmd: "Select-String -Path README.md -Pattern token -Credential admin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := powerShellReadOnlySafe(tt.cmd); got {
				t.Fatalf("powerShellReadOnlySafe(%q)=true, want false", tt.cmd)
			}
			if !shellBuiltinRequiresApproval(tt.cmd) {
				t.Fatalf("shellBuiltinRequiresApproval(%q)=false, want true", tt.cmd)
			}
		})
	}
}

func TestPowerShellSafetyAllowsReadOnlyCommandsAndCommonParameters(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
	}{
		{name: "get child item", cmd: "pwsh -NoProfile -NonInteractive -Command Get-ChildItem -Path internal/codetools"},
		{name: "select string", cmd: "Select-String -Path internal/codetools/*.go -Pattern PowerShell"},
		{name: "allowed clm type", cmd: "[System.Math]::Max(1,2)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !powerShellReadOnlySafe(tt.cmd) {
				t.Fatalf("powerShellReadOnlySafe(%q)=false, want true", tt.cmd)
			}
			if shellBuiltinRequiresApproval(tt.cmd) {
				t.Fatalf("shellBuiltinRequiresApproval(%q)=true, want false", tt.cmd)
			}
		})
	}
}

// shellRuntimeWithRules builds a shell runtime whose permission snapshot holds
// local-settings rules, the way one lands there after the user answers "don't
// ask again for commands that start with ...".
func shellRuntimeWithRules(t *testing.T, rules ...safety.PermissionRuleValue) (*State, *AgentToolRuntime) {
	t.Helper()
	store := safety.NewStore()
	store.ReplaceRules(safety.SourceLocalSettings, safety.BehaviorAllow, rules)
	return NewState(t.TempDir()), &AgentToolRuntime{
		Cfg:                          &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite},
		PermissionSnapshot:           store.Snapshot,
		PermissionSnapshotForSession: store.SnapshotForSession,
	}
}

func runShellForSandboxReason(t *testing.T, st *State, rt *AgentToolRuntime, command string) string {
	t.Helper()
	tool, err := NewShellTool(st, rt)
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	ctx := WithToolCompletionCapture(context.Background())
	if _, err := tool.Handle(ctx, `{"command":"`+command+`","timeout_ms":20000}`); err != nil {
		t.Fatalf("shell handle: %v", err)
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected tool completion capture")
	}
	reason, _ := completion.Output["sandbox_reason"].(string)
	return strings.TrimSpace(reason)
}

// A persisted "don't ask again for commands that start with `go test`" carries
// bypass_sandbox. When that grant is not applied the command is sandboxed, the
// sandbox denies it, and the escalation prompt asks the user to grant the rule
// they already granted — on every call.
func TestRememberedPrefixApprovalRunsTheCommandUnsandboxed(t *testing.T) {
	st, rt := shellRuntimeWithRules(t, safety.PermissionRuleValue{
		ToolName: "Bash", CommandPrefix: []string{"go", "env"}, BypassSandbox: true,
	})
	if got := runShellForSandboxReason(t, st, rt, "go env GOCACHE"); got != "require_escalated" {
		t.Fatalf("remembered approval did not lift the sandbox: sandbox_reason=%q", got)
	}
}

// The grant is per-rule: a command the remembered prefix does not cover stays
// contained.
func TestUnrelatedCommandStaysSandboxed(t *testing.T) {
	st, rt := shellRuntimeWithRules(t, safety.PermissionRuleValue{
		ToolName: "Bash", CommandPrefix: []string{"go", "env"}, BypassSandbox: true,
	})
	if got := runShellForSandboxReason(t, st, rt, "echo hello"); got == "require_escalated" {
		t.Fatal("sandbox was lifted for a command no rule covers")
	}
}

func TestCompressionMetaMap(t *testing.T) {
	m := Meta{
		Applied:       true,
		Filter:        "make",
		FilterVersion: "1",
		BeforeBytes:   21242,
		AfterBytes:    144,
		SavedTokens:   5275,
		CompressedPct: 99,
		RetrieveID:    3,
	}
	got := compressionMetaMap(m)
	if got["filter"] != "make" || got["retrieve_id"] != int64(3) {
		t.Fatalf("meta map wrong: %+v", got)
	}
	if got["saved_tokens"] != 5275 || got["compressed_pct"] != 99 {
		t.Fatalf("meta map savings wrong: %+v", got)
	}
}

func TestCompressShellOutputRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ResetCompressors()
	defer ResetCompressors()

	rt := &AgentToolRuntime{WorkspaceRoot: dir}
	var b strings.Builder
	b.WriteString("make[1]: Entering directory '/home/user/app'\n")
	for i := 0; i < 400; i++ {
		b.WriteString("make[2]: progress step xxxxxxxxxxxxxxxx [ OK ]\n")
	}
	b.WriteString("ERROR: src/api.c:42:18: error: expected ';' before '}' token\n")
	b.WriteString("make[1]: Leaving directory '/home/user/app'\n")
	stdout := b.String()

	comp := compressShellOutput(t.Context(), rt, "make", stdout)
	if !comp.Meta.Applied {
		t.Fatalf("expected compression, meta=%+v", comp.Meta)
	}
	if !strings.Contains(comp.Output, "ERROR: src/api.c:42:18") {
		t.Fatalf("error line must survive: %s", comp.Output)
	}
	if strings.Contains(comp.Output, "progress step") {
		t.Fatalf("noise must be stripped: %s", comp.Output)
	}
	if comp.Meta.RetrieveID == 0 {
		t.Fatalf("significant compression must cite a retrieve id, meta=%+v", comp.Meta)
	}

	// Recover the original through the retrieve tool wiring.
	tool, err := NewRetrieveOutputTool(rt)
	if err != nil {
		t.Fatalf("retrieve tool: %v", err)
	}
	_ = tool // registration surface validated by tools.go; exercise store directly
	entry, err := CompressorFor(dir).Store().Get(comp.Meta.RetrieveID)
	if err != nil {
		t.Fatalf("store get: %v", err)
	}
	if !strings.Contains(entry.OriginalOutput, "progress step") {
		t.Fatal("stored original lost noise lines")
	}
}

func TestCompressShellOutputFailOpen(t *testing.T) {
	// nil runtime / tiny output must pass through untouched.
	if got := compressShellOutput(t.Context(), nil, "make", "x"); got.Output != "" || got.Meta.Applied {
		t.Fatalf("nil runtime must be a no-op, got %+v", got)
	}
	rt := &AgentToolRuntime{WorkspaceRoot: t.TempDir()}
	ResetCompressors()
	defer ResetCompressors()
	if got := compressShellOutput(t.Context(), rt, "make", "ok\n"); got.Meta.Applied {
		t.Fatalf("tiny output must pass through, got %+v", got.Meta)
	}
}

func TestTypedExploreUsesReadOnlyShellProfile(t *testing.T) {
	if !ToolVisibleForSubagentSubtype("explore", "shell") {
		t.Fatal("explore must see the ordinary shell tool")
	}
	profile, elevated := shellSandboxProfile(WithSubagentType(context.Background(), "explore"))
	if profile != safety.ProfileReadOnly || elevated {
		t.Fatalf("explore profile = %q elevated=%v, want read-only/non-elevated", profile, elevated)
	}
}

func TestTypedExploreShellPolicyPermitsShellUnderReadOnlyProfile(t *testing.T) {
	st := NewState()
	ctx := WithSubagentType(context.Background(), "explore")
	if err := st.GuardTool(ctx, "shell"); err != nil {
		t.Fatalf("explore shell must be visible; sandbox profile enforces its boundary: %v", err)
	}
	profile, _ := shellSandboxProfile(ctx)
	if profile != safety.ProfileReadOnly {
		t.Fatalf("explore shell profile = %q, want read-only", profile)
	}
}

func planModeShellCtx() context.Context {
	return WithMode(llm.WithAgentSessionID(context.Background(), "sid-plan-shell"), "plan")
}

// The plan-mode reminder promises mutating shell commands are blocked. Only the
// sandbox can keep that promise for shell, since shell writes never reach
// GuardWrite.
func TestPlanModeUsesReadOnlyShellProfile(t *testing.T) {
	profile, elevated := shellSandboxProfile(planModeShellCtx())
	if profile != safety.ProfileReadOnly || elevated {
		t.Fatalf("plan-mode profile = %q elevated=%v, want read-only/non-elevated", profile, elevated)
	}
	// Agent mode is unaffected.
	agentProfile, _ := shellSandboxProfile(WithMode(context.Background(), "agent"))
	if agentProfile != safety.ProfileWorkspaceWrite {
		t.Fatalf("agent-mode profile = %q, want workspace-write", agentProfile)
	}
	// A user approval for this one call is still an escape hatch.
	approved, elevatedApproved := shellSandboxProfile(WithApprovedActionID(planModeShellCtx(), "act-1"))
	if approved != safety.ProfileWorkspaceWrite || !elevatedApproved {
		t.Fatalf("approved plan-mode call = %q elevated=%v, want workspace-write/elevated", approved, elevatedApproved)
	}
}

// danger-full-access runs commands on the host, so the read-only profile is
// advisory there and the tool-level gate is what holds the line. The gate has
// three answers, not two: a proven write is refused, and a command whose effect
// nothing can prove is a question for the operator. Refusing those as well is
// what made plan mode deny every command outside a whitelist.
func TestPlanModeShellGateSplitsReadOnlyWritesAndUnproven(t *testing.T) {
	danger := &AgentToolRuntime{Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}}
	sandboxed := &AgentToolRuntime{Cfg: &appcfg.Root{}}
	planCtx := planModeShellCtx()
	agentCtx := WithMode(context.Background(), "agent")

	gate := func(ctx context.Context, rt *AgentToolRuntime, cmd string) PlanModeShellGateDecision {
		return planModeShellGate(ctx, rt != nil && rt.Cfg != nil && rt.Cfg.DangerFullAccessEnabled(), safety.ClassifyShellCommand(cmd, nil, ""))
	}

	if got := gate(planCtx, danger, "sed -i '' s/a/b/ main.go"); got != PlanModeShellRefuse {
		t.Fatalf("proven write in plan mode = %d, want refuse", got)
	}
	if got := gate(planCtx, danger, "find . -name '*.go' -delete"); got != PlanModeShellRefuse {
		t.Fatalf("find -delete in plan mode = %d, want refuse", got)
	}
	if got := gate(planCtx, danger, "rg TODO internal"); got != PlanModeShellAllow {
		t.Fatalf("read-only command in plan mode = %d, want allow", got)
	}
	if got := gate(planCtx, danger, "python3 script.py"); got != PlanModeShellAsk {
		t.Fatalf("unproven command in plan mode = %d, want ask", got)
	}
	// An approval for this exact call is still an escape hatch, for both the
	// refused and the asked tier.
	approvedCtx := WithApprovedActionID(planCtx, "act-1")
	for _, cmd := range []string{"sed -i '' s/a/b/ main.go", "python3 script.py"} {
		if got := gate(approvedCtx, danger, cmd); got != PlanModeShellAllow {
			t.Fatalf("approved %q = %d, want allow", cmd, got)
		}
	}
	// The sandbox is the enforcement everywhere else, and agent mode is
	// unaffected.
	if got := gate(planCtx, sandboxed, "sed -i '' s/a/b/ main.go"); got != PlanModeShellAllow {
		t.Fatalf("sandboxed config must rely on the read-only profile, not this gate: %d", got)
	}
	if got := gate(agentCtx, danger, "sed -i '' s/a/b/ main.go"); got != PlanModeShellAllow {
		t.Fatalf("agent mode must be unaffected: %d", got)
	}
}

// The gate is what makes the plan-mode promise about shell writes true under
// danger-full-access. Each tier has to reach the tool's own behaviour: the
// unproven one asks, and the asked approval carries the sentence that explains
// why the user is being interrupted.
func TestShellToolPlanModeGateAsksOnlyForUnprovenCommands(t *testing.T) {
	for _, tt := range []struct {
		name        string
		cmd         string
		wantErr     string
		wantAsked   bool
		wantReason  string
		wantJustify bool
	}{
		{name: "read-only runs unasked", cmd: "echo hello"},
		{name: "unsupported read-only runs unasked", cmd: "go env GOPATH"},
		{
			name:    "proven write is refused",
			cmd:     "sed -i '' s/a/b/ main.go",
			wantErr: "exit_plan_mode",
		},
		{
			name:        "unproven asks",
			cmd:         "python3 script.py",
			wantAsked:   true,
			wantReason:  "plan_mode_unproven_command",
			wantJustify: true,
		},
		{
			// The assessment already forced this one for its syntax, so its
			// own tag stays: it names the more specific problem. The plan-mode
			// sentence is still what the user reads, because the syntax tag
			// carries no words of its own.
			name:        "unproven with complex syntax keeps its own tag",
			cmd:         "python3 -c \"print($(date +%s))\"",
			wantAsked:   true,
			wantReason:  "dangerous_shell_syntax",
			wantJustify: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			st := NewState(t.TempDir())
			asked := 0
			var payload map[string]any
			st.SetActionHook(func(_ context.Context, kind string, p any) (string, bool, error) {
				asked++
				if m, ok := p.(map[string]any); ok {
					payload = m
				}
				return "", false, nil
			})
			shellTool, err := NewShellTool(st, &AgentToolRuntime{
				Home: home,
				Cfg:  &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
			})
			if err != nil {
				t.Fatalf("NewShellTool: %v", err)
			}

			_, handleErr := shellTool.Handle(planModeWriteCtx(home, "sid-"+tt.name), `{"command":`+mustJSON(t, tt.cmd)+`}`)
			if tt.wantErr != "" {
				if handleErr == nil || !strings.Contains(handleErr.Error(), tt.wantErr) {
					t.Fatalf("handle error = %v, want one mentioning %q", handleErr, tt.wantErr)
				}
				if asked != 0 {
					t.Fatalf("a refused command must not ask: asked %d times", asked)
				}
				return
			}
			if handleErr != nil {
				t.Fatalf("handle: %v", handleErr)
			}
			wantAsked := 0
			if tt.wantAsked {
				wantAsked = 1
			}
			if asked != wantAsked {
				t.Fatalf("action hook called %d times, want %d", asked, wantAsked)
			}
			if !tt.wantAsked {
				return
			}
			if payload["force_tool_approval"] != true {
				t.Fatalf("plan-mode approval must force the prompt: %#v", payload)
			}
			if got := payload["approval_reason"]; got != tt.wantReason {
				t.Fatalf("approval_reason = %v, want %v", got, tt.wantReason)
			}
			if tt.wantJustify && strings.TrimSpace(payload["justification"].(string)) == "" {
				t.Fatalf("plan-mode approval must explain itself: %#v", payload)
			}
		})
	}
}

// With a sandbox configured, the OS boundary is the enforcement and the gate
// stays out of the way: a command the sandbox can contain must not also be
// judged by text, or plan mode would refuse work the sandbox would have run
// safely.
func TestShellToolPlanModeGateStaysOutOfSandboxedRuns(t *testing.T) {
	home := t.TempDir()
	st := NewState(t.TempDir())
	asked := 0
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		asked++
		return "", false, nil
	})
	shellTool, err := NewShellTool(st, &AgentToolRuntime{Home: home, Cfg: &appcfg.Root{}})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	for _, cmd := range []string{"echo hello", "sed -i '' s/a/b/ main.go", "python3 script.py"} {
		if _, handleErr := shellTool.Handle(planModeWriteCtx(home, "sid-sandboxed"), `{"command":`+mustJSON(t, cmd)+`}`); handleErr != nil {
			t.Fatalf("sandboxed plan mode must let the sandbox decide for %q: %v", cmd, handleErr)
		}
	}
	if asked != 0 {
		t.Fatalf("sandboxed plan mode asked the operator %d times, want 0", asked)
	}
}

// An approval is an answer for one command: on replay both the refused and the
// asked tier run, which is what makes the gate's error message actionable.
func TestShellToolPlanModeGateRunsAnApprovedWrite(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	target := filepath.Join(workspace, "approved.txt")
	st := NewState(workspace)
	shellTool, err := NewShellTool(st, &AgentToolRuntime{
		Home: home,
		Cfg:  &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	ctx := WithApprovedActionID(planModeWriteCtx(home, "sid-approved"), "act-1")
	if _, handleErr := shellTool.Handle(ctx, `{"command":`+mustJSON(t, "touch "+target)+`}`); handleErr != nil {
		t.Fatalf("an approved write must run in plan mode: %v", handleErr)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("approved command did not run: %v", statErr)
	}
}

// "No approval needed" is an answer, not a non-answer: policy already
// authorizes this command — a remembered rule, YOLO, an automatic reviewer —
// so the escalated retry may run. Treating it as a failure returns the denial
// to the model, whose expected response is to ask for escalated permissions
// itself: the same approval, one wasted round-trip later.
func TestSandboxRetryApprovalRunsWhenPolicyAlreadyAuthorizesTheCommand(t *testing.T) {
	st := NewState(t.TempDir())
	asked := 0
	st.SetActionHook(func(ctx context.Context, kind string, payload any) (string, bool, error) {
		asked++
		return "", false, nil
	})
	ctx := WithToolCompletionCapture(context.Background())
	denial := "fatal: Unable to create '/repo/.git/index.lock': Operation not permitted"
	authorized, err := requestSandboxRetryApproval(ctx, st, nil, sandboxedShellRequest{
		command: "git --no-pager add -A", profile: safety.ProfileWorkspaceWrite,
	}, safety.CommandResult{Stderr: denial, ExitCode: 128},
		safety.SandboxDecision{UseSandbox: true}, nil, denial)
	if err != nil {
		t.Fatalf("requestSandboxRetryApproval: %v", err)
	}
	if !authorized {
		t.Fatal("an already-authorized command must run its escalated retry instead of reporting the denial")
	}
	if asked != 1 {
		t.Fatalf("approval surface consulted %d times, want 1", asked)
	}
	if attempts := ToolAttemptsFromContext(ctx); len(attempts) != 0 {
		t.Fatalf("no approval was pending, so no attempt should be recorded: %#v", attempts)
	}
}

// A denial naming one concrete path is turned into a request for that path
// alone. If policy answers that request without asking, the answer covers the
// scoped capability — never unsandboxed host execution.
func TestScopedSandboxRetryIsNotUpgradedToUnsandboxedExecution(t *testing.T) {
	st := NewState(t.TempDir())
	var payload map[string]any
	st.SetActionHook(func(ctx context.Context, kind string, in any) (string, bool, error) {
		payload, _ = in.(map[string]any)
		return "", false, nil
	})
	rt := &AgentToolRuntime{Cfg: &appcfg.Root{
		Features: appcfg.FeaturesSection{ExecPermissionApprovals: appcfg.BoolPtr(true)},
	}}
	blocked := t.TempDir()
	denial := "open " + filepath.Join(blocked, "out.txt") + ": operation not permitted"
	authorized, err := requestSandboxRetryApproval(WithToolCompletionCapture(context.Background()), st, rt,
		sandboxedShellRequest{command: "tee " + filepath.Join(blocked, "out.txt"), profile: safety.ProfileWorkspaceWrite},
		safety.CommandResult{Stderr: denial, ExitCode: 1}, safety.SandboxDecision{UseSandbox: true}, nil, denial)
	if err != nil {
		t.Fatalf("requestSandboxRetryApproval: %v", err)
	}
	if payload["scoped_sandbox_retry"] != true {
		t.Fatalf("denial with a concrete path did not produce a scoped request: %#v", payload)
	}
	if authorized {
		t.Fatal("an approval for one writable path must not authorize running outside the sandbox")
	}
}

// End to end through the real platform sandbox: a write the sandbox refuses,
// with policy that already authorizes the command, must complete rather than
// come back as `sandbox denied command`.
func TestSandboxDeniedCommandRunsAfterPolicyAuthorizesTheRetry(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	outside, err := os.MkdirTemp(home, ".forebrain-sandbox-retry-")
	if err != nil {
		t.Skipf("cannot create a directory outside the workspace: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(outside) })
	target := filepath.Join(outside, "probe.txt")
	command := "printf ok > " + target

	st := NewState(t.TempDir())
	rt := &AgentToolRuntime{Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite}}
	tool, err := NewShellTool(st, rt)
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	args, _ := json.Marshal(map[string]any{"command": command, "timeout_ms": 20000})

	// First, with an approval surface that suspends: this both proves the
	// sandbox really refuses the write here and leaves the file absent.
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		return "act-retry", true, nil
	})
	_, err = tool.Handle(WithToolCompletionCapture(context.Background()), string(args))
	var requiresAction *RequiresActionError
	if !errors.As(err, &requiresAction) {
		os.Remove(target)
		t.Skipf("sandbox does not refuse writes outside the workspace here: err=%v", err)
	}
	if _, statErr := os.Stat(target); statErr == nil {
		t.Fatal("the denied attempt must not have written the file")
	}

	// Now with policy that already authorizes it: the retry runs on the host.
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		return "", false, nil
	})
	out, err := tool.Handle(WithToolCompletionCapture(context.Background()), string(args))
	if err != nil {
		t.Fatalf("authorized retry did not run: %v", err)
	}
	if text, ok := out.(string); ok && strings.Contains(text, "sandbox denied") {
		t.Fatalf("authorized retry still reported a denial: %s", text)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("authorized retry did not perform the write: %v", statErr)
	}
}

// The contract under test: rewriting happens on the shell tool's real inbound
// path, before approval, so the command carried in the approval payload is
// byte-identical to what will execute. These tests drive the actual tool
// returned by NewShellTool through the actual approval hook.

// invokeShell runs the real shell tool handler via its JSON entrypoint.
func invokeShell(t *testing.T, st *State, rt *AgentToolRuntime, in *ShellInput) (string, error) {
	t.Helper()
	tool, err := NewShellTool(st, rt)
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	args, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("encode args: %v", err)
	}
	out, err := tool.Handle(context.Background(), string(args))
	s, _ := out.(string)
	return s, err
}

func TestShellApprovalPayloadCarriesRewrittenCommand(t *testing.T) {
	ws := t.TempDir()
	st := NewState(ws)

	var gotCommand string
	var gotOriginal any
	var gotRewritten any
	st.SetActionHook(func(_ context.Context, kind string, payload any) (string, bool, error) {
		if kind != "shell" {
			return "", false, nil
		}
		m, ok := payload.(map[string]any)
		if !ok {
			t.Fatalf("payload type %T, want map[string]any", payload)
		}
		gotCommand, _ = m["command"].(string)
		gotOriginal = m["original_command"]
		gotRewritten = m["command_rewritten"]
		return "act-1", true, nil
	})

	// git pages by default; the rule inserts --no-pager. fetch is not
	// read-only, so this command must go through approval.
	_, err := invokeShell(t, st, nil, &ShellInput{Command: "git fetch origin", SandboxPermissions: "require_escalated"})
	if err == nil {
		t.Fatal("expected RequiresActionError so the approval path is exercised")
	}

	if !strings.Contains(gotCommand, "--no-pager") {
		t.Fatalf("approval payload command %q lacks the inserted flag; the user\nwould be approving a command different from the one that runs", gotCommand)
	}
	if gotRewritten != true {
		t.Errorf("command_rewritten = %v, want true", gotRewritten)
	}
	if gotOriginal != "git fetch origin" {
		t.Errorf("original_command = %v, want the pre-rewrite command", gotOriginal)
	}
}

// The approved command must equal the executed command. Approval replay
// reconstructs from the original ShellInput, so the rewrite must be
// deterministic or the user would approve one string and run another.
func TestShellRewriteIsDeterministicAcrossReplay(t *testing.T) {
	ws := t.TempDir()
	st := NewState(ws)

	seen := make([]string, 0, 4)
	st.SetActionHook(func(_ context.Context, kind string, payload any) (string, bool, error) {
		if m, ok := payload.(map[string]any); ok && kind == "shell" {
			if c, ok := m["command"].(string); ok {
				seen = append(seen, c)
			}
		}
		return "act-1", true, nil
	})

	in := &ShellInput{Command: "npm install lodash", SandboxPermissions: "require_escalated"}
	for i := 0; i < 3; i++ {
		// Same input replayed, as the approval flow does.
		_, _ = invokeShell(t, st, nil, &ShellInput{Command: in.Command, SandboxPermissions: in.SandboxPermissions})
	}
	if len(seen) < 3 {
		t.Fatalf("approval hook fired %d times, want 3", len(seen))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] != seen[0] {
			t.Fatalf("rewrite not deterministic: replay %d gave %q, first gave %q", i, seen[i], seen[0])
		}
	}
}

func TestShellRewriteDisabledByConfig(t *testing.T) {
	ws := t.TempDir()
	st := NewState(ws)

	var gotCommand string
	st.SetActionHook(func(_ context.Context, kind string, payload any) (string, bool, error) {
		if m, ok := payload.(map[string]any); ok && kind == "shell" {
			gotCommand, _ = m["command"].(string)
		}
		return "act-1", true, nil
	})

	off := false
	cfg := &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}
	cfg.Tools.CommandRewrite.Enabled = &off
	rt := &AgentToolRuntime{Cfg: cfg}

	_, _ = invokeShell(t, st, rt, &ShellInput{Command: "npm install lodash", SandboxPermissions: "require_escalated"})
	if strings.Contains(gotCommand, "--no-progress") || strings.Contains(gotCommand, "--no-color") {
		t.Fatalf("config disabled rewriting but command was still rewritten: %q", gotCommand)
	}
	if gotCommand != "npm install lodash" {
		t.Errorf("command = %q, want it passed through untouched", gotCommand)
	}
}

// Auto-approval rules are evaluated upstream, in agentrun, against the raw tool
// input, i.e. the command *before* rewriting. That ordering is what makes
// rewriting safe to run ahead of the permission layer, and this test pins it.
//
// The direction matters. Inserting after the head can make a rule that matched
// the original stop matching the rewritten form, which fails toward asking the
// user. The reverse (a rule that only matches once the flag is present) would be
// an escalation, and is only unreachable because nothing re-derives permissions
// from the rewritten string. If a future change evaluates permissions on the
// rewritten command, this test fails and says why.
func TestPermissionsEvaluateOriginalCommandNotRewritten(t *testing.T) {
	const original = "git log"
	rewrittenCmd := rewriteShellCommand(context.Background(), nil, original)
	if !rewrittenCmd.Applied {
		t.Fatalf("expected %q to be rewritten; the rest of this test is vacuous otherwise", original)
	}

	// A rule granted for the original command must keep authorizing it, because
	// evaluation sees the original on every subsequent identical request.
	if !safety.ShellRuleMatches("git log:*", original) {
		t.Fatal("granted rule stopped matching the command it was granted for")
	}

	// The escalation shape to guard against: a rule that does not match the
	// original but does match the rewritten form. Harmless only as long as
	// evaluation never sees the rewritten string.
	const broad = "git --no-pager log:*"
	if safety.ShellRuleMatches(broad, original) {
		t.Fatalf("rule %q unexpectedly matches %q; test premise no longer holds", broad, original)
	}
	if !safety.ShellRuleMatches(broad, rewrittenCmd.Command) {
		t.Fatalf("rule %q no longer matches %q; test premise no longer holds", broad, rewrittenCmd.Command)
	}

	// The invariant: the rewrite is not the string permissions were evaluated
	// against, and the original stays available for that evaluation.
	if rewrittenCmd.Original != original {
		t.Errorf("Original = %q, want the pre-rewrite command %q", rewrittenCmd.Original, original)
	}
	if rewrittenCmd.Command == rewrittenCmd.Original {
		t.Error("Command and Original are equal; the outcome no longer distinguishes the two strings")
	}
}

// A command with no matching rule must reach approval untouched, and must not
// claim a rewrite happened.
func TestShellRewriteLeavesUnknownCommandsAlone(t *testing.T) {
	ws := t.TempDir()
	st := NewState(ws)

	var gotCommand string
	var rewritten any
	st.SetActionHook(func(_ context.Context, kind string, payload any) (string, bool, error) {
		if m, ok := payload.(map[string]any); ok && kind == "shell" {
			gotCommand, _ = m["command"].(string)
			rewritten = m["command_rewritten"]
		}
		return "act-1", true, nil
	})

	_, _ = invokeShell(t, st, nil, &ShellInput{Command: "./scripts/deploy.sh --force", SandboxPermissions: "require_escalated"})
	if gotCommand != "./scripts/deploy.sh --force" {
		t.Errorf("command = %q, want unchanged", gotCommand)
	}
	if rewritten != nil {
		t.Errorf("command_rewritten = %v, want absent for an unmodified command", rewritten)
	}
}

func TestShellOutputEmitterCoalescesFlood(t *testing.T) {
	var mu sync.Mutex
	var chunks [][]byte
	emitter := newShellOutputEmitter(func(_ safety.OutputStream, chunk []byte) {
		mu.Lock()
		chunks = append(chunks, append([]byte(nil), chunk...))
		mu.Unlock()
	})
	for i := 0; i < 10_000; i++ {
		emitter.Push(safety.OutputStreamStderr, []byte("failure\n"))
	}
	emitter.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(chunks) == 0 || len(chunks) > 4 {
		t.Fatalf("emitted %d chunks for flood", len(chunks))
	}
	combined := bytes.Join(chunks, nil)
	if !bytes.Contains(combined, []byte("coalesced")) || len(combined) > liveOutputPendingBytes*len(chunks)+1024 {
		t.Fatalf("unexpected live preview bytes=%d", len(combined))
	}
}

func TestSandboxDeniedWriteRoot(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "cache")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	reason := "go: open " + filepath.Join(cache, "item.tmp") + ": operation not permitted"
	if got := sandboxDeniedWriteRoot(reason); got != cache {
		t.Fatalf("root=%q want=%q", got, cache)
	}
}

func TestShellToolGoTestInPlanModeUsesSandboxFallback(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	st := NewState(workspace)
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Home:          home,
		WorkspaceRoot: workspace,
		Cfg:           &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := planModeWriteCtx(home, "go-test-plan-regression")
	_, err = tool.Handle(ctx, `{"command":"go test ./internal/tui ./internal/permissions"}`)
	if err == nil {
		return
	}
	if strings.Contains(err.Error(), "plan mode: write blocked") {
		t.Fatalf("unknown shell command reached GuardWrite: %v", err)
	}
}

func TestShellToolUnknownCommandNeverUsesEmptyGuardWrite(t *testing.T) {
	st := NewState(t.TempDir())
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tool.Handle(context.Background(), `{"command":"go test ./internal/permissions"}`)
	if err != nil && strings.Contains(err.Error(), "only the plan directory is writable") {
		t.Fatalf("unknown command was coupled to file-write policy: %v", err)
	}
}
