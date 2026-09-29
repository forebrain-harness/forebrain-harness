// The shell tool: execution, builtins, profiles, roots, rewriting, and Go cache policy.
package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

func planModeShellEnv(ctx context.Context, rt *AgentToolRuntime) []string {
	if rt == nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(ModeFromContext(ctx)), string(state.ModePlan)) {
		return nil
	}
	sid := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
	stateRoot := rt.StateRoot()
	if sid == "" || stateRoot == "" {
		return []string{"FOREBRAIN_PLAN_MODE_FORCE=active"}
	}
	projectKey := strings.TrimSpace(rt.ProjectKey)
	planDir := state.PlanDirForProject(stateRoot, projectKey)
	planFile := state.PlanPathForProject(stateRoot, projectKey)
	return []string{
		"FOREBRAIN_PLAN_FILE=" + planFile,
		"FOREBRAIN_PLAN_DIR=" + planDir,
		"FOREBRAIN_PLAN_MODE_FORCE=active",
	}
}

// agentRuntimeEnv returns env vars exposing the agent's identity to shell
// commands: FOREBRAIN_HOME, FOREBRAIN_WORKSPACE_ROOT, FOREBRAIN_PROJECT_KEY.
// These allow skills and scripts to locate project-scoped directories.
func agentRuntimeEnv(rt *AgentToolRuntime) []string {
	if rt == nil {
		return nil
	}
	var env []string
	if home := strings.TrimSpace(rt.Home); home != "" {
		env = append(env, "FOREBRAIN_HOME="+home)
	}
	if ws := strings.TrimSpace(rt.WorkspaceRoot); ws != "" {
		env = append(env, "FOREBRAIN_WORKSPACE_ROOT="+ws)
	}
	if pk := strings.TrimSpace(rt.ProjectKey); pk != "" {
		env = append(env, "FOREBRAIN_PROJECT_KEY="+pk)
	}
	return env
}

type ShellInput struct {
	Command               string                           `json:"command" jsonschema:"description=Shell command to run."`
	Description           string                           `json:"description,omitempty" jsonschema:"description=Human-readable description of what this command does."`
	TimeoutMs             int                              `json:"timeout_ms,omitempty" jsonschema:"description=Timeout in milliseconds (default 10000)."`
	SandboxPermissions    safety.SandboxPermissions        `json:"sandbox_permissions,omitempty" jsonschema:"enum=use_default,enum=with_additional_permissions,enum=require_escalated" jsonschema_description:"Per-command sandbox override. Defaults to use_default; use with_additional_permissions with additional_permissions, or require_escalated for unsandboxed execution."`
	AdditionalPermissions *safety.RequestPermissionProfile `json:"additional_permissions,omitempty" jsonschema_description:"Sandboxed filesystem or network access for this command; only with sandbox_permissions set to with_additional_permissions. Omit when not needed."`
	Justification         string                           `json:"justification,omitempty" jsonschema_description:"User-facing approval question for require_escalated; omit otherwise."`
	PrefixRule            []string                         `json:"prefix_rule,omitempty" jsonschema_description:"Reusable approval prefix for the command; only with sandbox_permissions require_escalated. Omit when not needed."`
}

const shellToolDescription = "Use when running a command — builds, tests, formatters, git, etc. — not for file edits (prefer edit_file/write_file). apply_patch is the only supported shell exception and must be the only command in that shell call: use a heredoc such as apply_patch <<'PATCH' ... PATCH, with its terminator as the final nonblank line. Never append &&, ;, tests, formatters, or other shell commands; run them in separate shell calls. Pass a description; timeout_ms defaults to 10000."

const defaultShellCommandTimeout = 10 * time.Second

func NewShellTool(st *State, rt *AgentToolRuntime) (*llm.Tool, error) {
	tool, err := llm.NewTool(
		"shell",
		shellToolDescription,
		func(ctx context.Context, in *ShellInput) (string, error) {
			if st == nil {
				return "", fmt.Errorf("nil state")
			}
			if in == nil {
				in = &ShellInput{}
			}
			// An empty object expresses "unset" for `additional_permissions`
			// (its natural empty value, mirroring the empty string/array used by
			// justification/prefix_rule). Normalize it to nil so downstream
			// validation treats it exactly like an omitted field.
			if in.AdditionalPermissions != nil && in.AdditionalPermissions.Network == nil && in.AdditionalPermissions.FileSystem == nil {
				in.AdditionalPermissions = nil
			}
			sandboxBypassApproved := SandboxBypassApprovedFromContext(ctx)
			if !in.SandboxPermissions.Valid() {
				return "", fmt.Errorf("sandbox_permissions must be use_default, with_additional_permissions, or require_escalated")
			}
			// `justification` is only meaningful alongside an explicit sandbox
			// selection. An omitted field deserializes to the empty string,
			// while an explicit `use_default` is distinct and may carry a
			// justification. Inspect the raw value before normalizing so the
			// omitted-versus-explicit distinction survives.
			if strings.TrimSpace(in.Justification) != "" && in.SandboxPermissions == "" {
				return "", fmt.Errorf("`justification` requires an explicit `sandbox_permissions`; use `sandbox_permissions: \"require_escalated\"` for unsandboxed execution, or omit `justification`")
			}
			in.SandboxPermissions = in.SandboxPermissions.Normalized()
			if err := validateShellAdditionalPermissions(ctx, in, rt); err != nil {
				return "", err
			}
			profile, profileElevation := shellSandboxProfile(ctx)
			if profile == safety.ProfileWorkspaceWrite && !profileElevation && rt != nil && rt.Cfg != nil {
				profile = safety.ProfileForConfig(rt.Cfg)
			}
			if sandboxBypassApproved {
				in.SandboxPermissions = safety.SandboxPermissionsRequireEscalated
			}
			cmd := strings.TrimSpace(in.Command)
			if cmd == "" {
				return "", fmt.Errorf("command required")
			}
			if handled, out, err := maybeHandleApplyPatchCommand(ctx, st, rt, cmd); handled {
				return out, err
			}
			// Rewrite before any policy is derived, so read-only
			// classification, skill guards, the approval payload, and
			// execution all observe one identical command string. The user
			// therefore approves the command that actually runs, never a
			// pre-rewrite form. Fail-open: on any doubt cmd is unchanged.
			rewriteResult := rewriteShellCommand(ctx, rt, cmd)
			cmd = rewriteResult.Command
			// A persisted approval that carries bypass_sandbox is applied here as
			// well as in the tool-permission middleware, and against the rewritten
			// command the rule was derived from. The middleware's answer travels in
			// a context flag; when it does not arrive the command runs sandboxed,
			// the sandbox denies it, and the escalation prompt asks for the rule
			// the user already persisted. Asking the snapshot directly makes the
			// remembered grant the thing that ends that loop.
			if !sandboxBypassApproved && in.SandboxPermissions == safety.SandboxPermissionsUseDefault &&
				safety.SnapshotGrantsShellSandboxBypass(permissionSnapshotForContext(ctx, st, rt), cmd) {
				sandboxBypassApproved = true
				in.SandboxPermissions = safety.SandboxPermissionsRequireEscalated
			}
			// Resolve cwd early for path-based read-only validation (cd / ls
			// targets must stay under one of the allowed roots).
			wd, err := os.Getwd()
			if err != nil {
				return "", err
			}
			wdAbs, werr := filepath.Abs(wd)
			if werr != nil {
				return "", werr
			}
			assessment := safety.AssessShellCommand(cmd)
			mutation := safety.ClassifyShellCommand(cmd, shellAllowedRoots(ctx, st, rt), wdAbs)
			readOnlyCommand := mutation == safety.ShellMutationReadOnly
			planGate := planModeShellGate(ctx, rt != nil && rt.Cfg != nil && rt.Cfg.DangerFullAccessEnabled(), mutation)
			if planGate == PlanModeShellRefuse {
				err := fmt.Errorf("plan mode: %q modifies files; call exit_plan_mode first", strings.TrimSpace(cmd))
				CaptureToolError(ctx, err)
				return "", err
			}
			// A command that reaches a loaded skill and cannot be proven
			// read-only used to be refused outright, which left the user unable
			// to change a skill through the agent at all. It is a question for
			// them instead: the approval below is forced and says why.
			skillAccess := st.LoadedSkillShellAccessReason(cmd, wdAbs, readOnlyCommand)
			// The safety assessment is an approval heuristic, never a proxy for
			// filesystem mutation. Unknown commands run in the configured sandbox
			// or prompt according to policy; they never enter GuardWrite("").
			requestedBypass := in.SandboxPermissions != safety.SandboxPermissionsUseDefault
			if requestedBypass && !sandboxBypassApproved && ApprovedActionIDFromContext(ctx) == "" {
				policy := safety.ApprovalPolicy{Mode: safety.ApprovalOnRequest}
				if rt != nil && (rt.PermissionSnapshotForSession != nil || rt.PermissionSnapshot != nil) {
					policy = permissionSnapshotForContext(ctx, st, rt).ApprovalPolicy
				}
				if policy.Mode != safety.ApprovalOnRequest {
					return "", fmt.Errorf("approval policy is %s; reject command — you should not ask for escalated permissions if the approval policy is %s", policy.Mode, policy.Mode)
				}
			}
			approvalRequired := skillAccess != "" || planGate == PlanModeShellAsk || shellAssessmentRequiresApproval(ctx, st, rt, cmd, assessment, readOnlyCommand, requestedBypass, sandboxBypassApproved)
			if approvalRequired && ApprovedActionIDFromContext(ctx) == "" {
				payload := rewriteResult.applyTo(shellAssessmentApprovalPayload(cmd, in, assessment))
				markProtectedApproval(payload, skillAccess)
				if planGate == PlanModeShellAsk {
					markPlanModeApproval(payload)
				}
				payload["sandbox_profile"] = string(profile)
				if profileElevation {
					payload["requested_profile"] = string(safety.ProfileWorkspaceWrite)
					payload["profile_elevation"] = true
				}
				if hook := st.ActionHook(); hook != nil {
					id, ok, err := hook(ctx, "shell", payload)
					if err != nil {
						return "", err
					}
					if ok && id != "" {
						CaptureToolRequiresAction(ctx, id, "shell", appendToolExecutionMetadata(map[string]any{
							"requires_action":   true,
							"sandbox_profile":   string(profile),
							"profile_elevation": profileElevation,
						}, rt, safety.ToolKindShell))
						// Carry the payload itself, not the tool's input struct: every surface
						// rebuilds the wait record from this value, and the payload is where the
						// reasons live - the sentence explaining why the user is being asked, the
						// command-safety verdict, the sandbox profile. The input struct holds none
						// of them, so narrowing to it left the overlay with a bare command. The
						// payload also holds the rewritten command (see shellAssessmentApprovalPayload
						// above), which is what the approval UI derives its reusable-prefix
						// amendment from: the amendment is proposed against this record but
						// validated against the command that runs, so a pre-rewrite form here is
						// rejected as a mismatch.
						return "", &RequiresActionError{
							RunID: RunIDFromContext(ctx), AgentID: HookAgentIDFromContext(ctx), SubagentType: SubagentTypeFromContext(ctx),
							ActionID: id, ActionKind: "shell", ToolName: "shell", ToolInput: payload,
						}
					}
				} else if shellBuiltinRequiresApproval(cmd) {
					return "", fmt.Errorf("shell command requires approval: %q", cmd)
				}
			}
			return runSandboxedShellCommand(ctx, st, rt, sandboxedShellRequest{
				command: cmd, timeoutMs: in.TimeoutMs, sandboxPermissions: in.SandboxPermissions,
				additionalPermissions: in.AdditionalPermissions,
				prefixRule:            append([]string(nil), in.PrefixRule...),
				profile:               profile, profileElevation: profileElevation,
				cwd: wdAbs, toolName: "shell", toolDescription: shellToolDescription,
			})
		},
	)
	if err != nil {
		return nil, err
	}
	if err := finalizeShellToolSchema(tool, execPermissionApprovalsEnabled(rt)); err != nil {
		return nil, err
	}
	return tool, nil
}

type sandboxedShellRequest struct {
	command               string
	timeoutMs             int
	sandboxPermissions    safety.SandboxPermissions
	additionalPermissions *safety.RequestPermissionProfile
	prefixRule            []string
	profile               safety.Profile
	profileElevation      bool
	// exclusiveWritablePaths states that this request's writable paths are the
	// whole write policy rather than additions to the configured workspace
	// roots. promoteReadOnlyWriteScope sets it when it promotes the profile.
	exclusiveWritablePaths bool
	cwd                    string
	toolName               string
	toolDescription        string
}

func runSandboxedShellCommand(ctx context.Context, st *State, rt *AgentToolRuntime, req sandboxedShellRequest) (string, error) {
	var snap safety.Snapshot
	snap = permissionSnapshotForRun(permissionSnapshotForContext(ctx, st, rt), RunIDFromContext(ctx))
	manager := safety.NewManager()
	defer manager.Close()
	runtimeCfg := manager.UpdateConfig(rtConfig(rt), shellToolSettingsSources(rt), snap, req.cwd, req.cwd, os.TempDir(), st.AllowedRoots())
	// contained reports whether a sandbox holds this command, which is the
	// manager's decision to make and never the profile's: a full-access session
	// and a user-approved escalation both run the command on the host while the
	// profile still reads workspace-write or read-only. Ask the same question
	// RunCommand asks - the same function, the same inputs - so the two can
	// never disagree.
	//
	// The default sandbox permissions are the ones to ask about: every attempt a
	// sandbox can hold uses them, while require_escalated leaves the sandbox by
	// definition, and a request asking for it drops back to the default exactly
	// when deny-read rules apply - which can make the sandbox apply, never lift
	// it. The profile is read at call time, so an attempt question sees the
	// profile the attempt will carry.
	contained := func(sandboxPermissions safety.SandboxPermissions) bool {
		return manager.DecideShellCommand(rtConfig(rt), req.command, sandboxPermissions, req.profile, false).UseSandbox
	}
	loadedSkillRoots := st.LoadedSkillRoots()
	var goCache goCacheAccess
	extraBinds := make([]string, 0, len(runtimeCfg.Filesystem.AllowWrite))
	seenBind := map[string]struct{}{}
	if req.profile == safety.ProfileWorkspaceWrite {
		seenBind[req.cwd] = struct{}{}
	}
	writablePaths := runtimeCfg.Filesystem.AllowWrite
	if req.profile == safety.ProfileReadOnly {
		writablePaths = nil
		for _, grant := range snap.FileSystemGrants {
			if grant.Entry.Access == safety.FileSystemAccessWrite {
				if path, ok := grant.Entry.Path.Resolve(req.cwd); ok {
					writablePaths = append(writablePaths, path)
				}
			}
		}
	}
	for _, r := range writablePaths {
		ra, raErr := filepath.Abs(strings.TrimSpace(r))
		if raErr != nil || ra == "" {
			continue
		}
		if _, ok := seenBind[ra]; ok {
			continue
		}
		seenBind[ra] = struct{}{}
		if ra == req.cwd && req.profile == safety.ProfileWorkspaceWrite {
			continue
		}
		extraBinds = append(extraBinds, ra)
	}
	denyWrite := make([]string, 0, len(runtimeCfg.Filesystem.DenyWrite))
	for _, r := range runtimeCfg.Filesystem.DenyWrite {
		ra, raErr := filepath.Abs(strings.TrimSpace(r))
		if raErr != nil || ra == "" {
			continue
		}
		denyWrite = append(denyWrite, ra)
	}
	allowRead := make([]string, 0, len(runtimeCfg.Filesystem.AllowRead))
	for _, r := range runtimeCfg.Filesystem.AllowRead {
		ra, raErr := filepath.Abs(strings.TrimSpace(r))
		if raErr != nil || ra == "" {
			continue
		}
		allowRead = append(allowRead, ra)
	}
	if rt != nil {
		allowRead = MergeAllowedRootPaths(allowRead, rt.AdditionalReadRoots)
	}
	for _, root := range loadedSkillRoots {
		allowRead = MergeAllowedRootPaths(allowRead, []string{root})
	}
	denyRead := make([]string, 0, len(runtimeCfg.Filesystem.DenyRead))
	for _, r := range runtimeCfg.Filesystem.DenyRead {
		ra, raErr := filepath.Abs(strings.TrimSpace(r))
		if raErr != nil || ra == "" {
			continue
		}
		denyRead = append(denyRead, ra)
	}
	denyReadPatterns := append([]string(nil), runtimeCfg.Filesystem.DenyReadPatterns...)
	globScanMaxDepth := cloneIntPointer(runtimeCfg.Filesystem.GlobScanMaxDepth)
	allowNetwork := runtimeCfg.NetworkEnabled
	for _, grant := range snap.NetworkGrants {
		if grant.Enabled {
			allowNetwork = true
			break
		}
	}
	if req.additionalPermissions != nil {
		if fs := req.additionalPermissions.FileSystem; fs != nil {
			for _, entry := range fs.Entries {
				path, resolved := entry.Path.Resolve(req.cwd)
				switch entry.Access {
				case safety.FileSystemAccessWrite:
					if !resolved {
						continue
					}
					extraBinds = MergeAllowedRootPaths(extraBinds, []string{path})
					allowRead = MergeAllowedRootPaths(allowRead, []string{path})
				case safety.FileSystemAccessRead:
					if !resolved {
						continue
					}
					allowRead = MergeAllowedRootPaths(allowRead, []string{path})
				case safety.FileSystemAccessDeny:
					if entry.Path.Type == safety.FileSystemPermissionPathTypeGlobPattern {
						hadPatterns := len(denyReadPatterns) > 0
						denyReadPatterns = appendUniqueText(denyReadPatterns, entry.Path.Pattern)
						if !hadPatterns {
							globScanMaxDepth = cloneIntPointer(fs.GlobScanMaxDepth)
						} else {
							globScanMaxDepth = mergeGlobDepth(globScanMaxDepth, fs.GlobScanMaxDepth)
						}
					} else if resolved {
						denyRead = MergeAllowedRootPaths(denyRead, []string{path})
						denyWrite = MergeAllowedRootPaths(denyWrite, []string{path})
					}
				}
			}
		}
		allowNetwork = allowNetwork || req.additionalPermissions.Network.AllowsNetwork()
	}
	denyReadRestricted := len(denyRead) > 0 || len(denyReadPatterns) > 0
	effectiveSandboxPermissions := req.sandboxPermissions
	if effectiveSandboxPermissions == safety.SandboxPermissionsRequireEscalated && denyReadRestricted {
		effectiveSandboxPermissions = safety.SandboxPermissionsUseDefault
	}
	if shellProfilePlumbsGoCache(req.profile, contained(effectiveSandboxPermissions), runtimeCfg.Filesystem.AllowWrite, req.cwd) {
		goCache = resolveGoCacheAccess(ctx, rt, req.cwd)
	}
	extraBinds = MergeAllowedRootPaths(extraBinds, goCache.Writable)
	allowRead = MergeAllowedRootPaths(allowRead, goCache.Readable)
	allowRead = promoteReadOnlyWriteScope(&req, extraBinds, allowRead)
	if len(denyReadPatterns) > 0 {
		expanded, expandErr := safety.ExpandDeniedReadPatterns(denyReadPatterns, req.cwd, globScanMaxDepth)
		if expandErr != nil {
			return "", fmt.Errorf("expand deny-read glob: %w", expandErr)
		}
		denyRead = MergeAllowedRootPaths(denyRead, expanded)
	}
	timeout := defaultShellCommandTimeout
	if req.timeoutMs > 0 {
		timeout = time.Duration(req.timeoutMs) * time.Millisecond
	}
	var onOutput func(safety.OutputStream, []byte)
	var outputEmitter *shellOutputEmitter
	if step := StepHookFromContext(ctx, st); step != nil {
		stepID := ToolStepIDFromContext(ctx)
		startedAt := ToolStepStartedAtFromContext(ctx)
		outputEmitter = newShellOutputEmitter(func(stream safety.OutputStream, chunk []byte) {
			if len(chunk) == 0 {
				return
			}
			step(ctx, StepEvent{
				Kind:            StepKindToolOutputDelta,
				StepID:          stepID,
				ToolName:        req.toolName,
				ToolDescription: req.toolDescription,
				StartedAt:       startedAt,
				Input: map[string]any{
					"command": req.command,
				},
				Output: map[string]any{
					"stream": string(stream),
					"chunk":  string(chunk),
				},
			})
		})
		onOutput = outputEmitter.Push
	}
	defer outputEmitter.Close()
	// The Go cache overrides are deliberately not part of the base environment:
	// they belong to the sandbox that motivated them, so they are added per
	// attempt. An attempt that leaves the sandbox - a user-approved escalation -
	// would otherwise carry cold, Forebrain-owned caches onto the host.
	shellEnv := append(planModeShellEnv(ctx, rt), agentRuntimeEnv(rt)...)
	spoolDir := ""
	if rt != nil && strings.TrimSpace(rt.StateRoot()) != "" {
		spoolDir = filepath.Join(rt.StateRoot(), "state", "tool-output")
	}
	commandRequest := func(sandboxPermissions safety.SandboxPermissions, approvedNetwork *safety.ApprovedNetworkAccess) safety.CommandRequest {
		return safety.CommandRequest{
			ToolKind: safety.ToolKindShell, Command: req.command, WorkDir: req.cwd, Timeout: timeout,
			Profile: req.profile, SandboxPermissions: sandboxPermissions,
			AdditionalWritablePaths: extraBinds, ExclusiveWritablePaths: req.exclusiveWritablePaths,
			DeniedWritablePaths:     denyWrite,
			AdditionalReadablePaths: allowRead, DeniedReadablePaths: denyRead,
			DeniedReadablePatterns: denyReadPatterns, IncludePlatformDefaults: runtimeCfg.Filesystem.IncludePlatformDefaults,
			AllowNetwork: allowNetwork, ApprovedNetworkAccess: approvedNetwork,
			NetworkApproval: shellNetworkApprovalHandler(st, rt), NetworkEnvironmentID: ConversationSessionIDFromContext(ctx), NetworkExecutionID: RunIDFromContext(ctx),
			SessionNetworkRules: st.NetworkSessionRules(ConversationSessionIDFromContext(ctx)),
			Env:                 shellAttemptEnv(shellEnv, goCache.Env, contained(sandboxPermissions)),
			OnOutput:            onOutput, OutputSpoolDir: spoolDir,
		}
	}
	res, decision, runErr := manager.RunCommand(ctx, rtConfig(rt), commandRequest(effectiveSandboxPermissions, approvedNetworkAccessForSandbox(ctx)))
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		return "", runErr
	}
	denied := decision.UseSandbox && safety.IsLikelySandboxDenied(decision, res)
	alreadyApproved := ApprovedActionIDFromContext(ctx) != ""
	if denied && res.NetworkDenial == nil && req.sandboxPermissions == safety.SandboxPermissionsUseDefault &&
		sandboxRetryApprovalAllowed(snap.ApprovalPolicy) && !denyReadRestricted {
		// An approved replay retries directly; otherwise the approval surface
		// decides. It answers "no approval needed" whenever policy already
		// authorizes this command — a remembered rule, YOLO, or an automatic
		// reviewer that allowed it — and that answer means the escalated run may
		// proceed. Reporting the denial instead sends the model back a failure
		// it is expected to answer by asking for escalation itself: the same
		// approval, one wasted round-trip later.
		retry := alreadyApproved && !strictAutoReviewEnabled(ctx, rt, RunIDFromContext(ctx))
		if !retry {
			// Not an error yet: the denial is about to become an approval
			// request or an authorized retry. Only a denial nothing can answer
			// is logged as one, below.
			denialReason := sandboxDenialReason(res, runErr)
			slog.Warn("sandbox denied shell command; asking whether it may run outside the sandbox",
				"command", req.command, "reason", denialReason, "err", runErr)
			authorized, approvalErr := requestSandboxRetryApproval(ctx, st, rt, req, res, decision, runErr, denialReason)
			if approvalErr != nil {
				return "", approvalErr
			}
			retry = authorized
		}
		if retry {
			res, decision, runErr = manager.RunCommand(ctx, rtConfig(rt), commandRequest(safety.SandboxPermissionsRequireEscalated, nil))
			if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
				return "", runErr
			}
			denied = decision.UseSandbox && safety.IsLikelySandboxDenied(decision, res)
		}
	}
	if denied && res.NetworkDenial != nil && res.NetworkDenial.Decision == "ask" && !alreadyApproved {
		if hook := st.ActionHook(); hook != nil {
			networkContext := safety.NetworkApprovalContext{Host: res.NetworkDenial.Host, Protocol: res.NetworkDenial.Protocol}
			retryReason := fmt.Sprintf("Network access to %q is blocked by policy.", res.NetworkDenial.Host)
			payload := map[string]any{
				"command": req.command, "timeout_ms": req.timeoutMs,
				"sandbox_permissions": string(safety.SandboxPermissionsUseDefault),
				"justification":       retryReason, "force_tool_approval": true,
				"approval_reason": "network_denied", "sandbox_denial_reason": retryReason,
				"network_approval_context": networkContext, "network_port": res.NetworkDenial.Port,
				"session_id": ConversationSessionIDFromContext(ctx),
			}
			id, pending, approvalErr := hook(WithApprovedActionID(ctx, ""), "shell", payload)
			if approvalErr != nil {
				return "", approvalErr
			}
			if pending && id != "" {
				CaptureToolRequiresAction(ctx, id, "shell", appendToolExecutionMetadata(map[string]any{
					"requires_action": true, "network_retry": true,
				}, rt, safety.ToolKindShell))
				return "", &RequiresActionError{
					RunID: RunIDFromContext(ctx), AgentID: HookAgentIDFromContext(ctx), SubagentType: SubagentTypeFromContext(ctx),
					ActionID: id, ActionKind: "shell", ToolName: "shell", ToolInput: payload,
				}
			}
			approved := &safety.ApprovedNetworkAccess{Context: networkContext, Port: res.NetworkDenial.Port}
			res, decision, runErr = manager.RunCommand(ctx, rtConfig(rt), commandRequest(effectiveSandboxPermissions, approved))
			if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
				return "", runErr
			}
			denied = decision.UseSandbox && safety.IsLikelySandboxDenied(decision, res)
		}
	}
	stdout := res.Stdout
	stderr := res.Stderr
	stdoutBytes := int(res.StdoutBytes)
	stderrBytes := int(res.StderrBytes)
	if stdoutBytes == 0 && stdout != "" {
		stdoutBytes = len(stdout)
	}
	if stderrBytes == 0 && stderr != "" {
		stderrBytes = len(stderr)
	}
	exitCode := res.ExitCode
	if runErr != nil && exitCode == 0 {
		exitCode = 1
	}
	// Token-saving output compression (Boost-style, fail-open): compress the
	// final stdout on the return path before it enters the model context. The
	// original is kept recoverable via retrieve_output. Only stdout is touched;
	// stderr stays verbatim because errors carry dense signal.
	compression := shellCompression{}
	if res.StdoutOmittedBytes == 0 && res.StderrOmittedBytes == 0 {
		compression = compressShellOutput(ctx, rt, req.command, stdout)
	}
	if compression.Output != "" {
		stdout = compression.Output
	}
	resultPayload := map[string]any{
		"stdout":    stdout,
		"stderr":    stderr,
		"exit_code": exitCode,
	}
	retrieveID := registerShellSpool(ctx, rt, req.command, res)
	if retrieveID > 0 {
		resultPayload["output_retrieval"] = map[string]any{
			"retrieve_id": retrieveID, "stdout_omitted_bytes": res.StdoutOmittedBytes,
			"stderr_omitted_bytes": res.StderrOmittedBytes, "spool_omitted_bytes": res.OutputSpoolOmittedBytes,
		}
	}
	if compression.Meta.Applied {
		resultPayload["output_compression"] = compressionMetaMap(compression.Meta)
	}
	// Only surface YOLO/approval-bypass context to the model. The
	// sandbox_* operational metadata (backend, mode, reason, violations,
	// ...) is telemetry-only noise for the LLM and is captured below via
	// CaptureToolCompletion instead of being returned in the result.
	if rt != nil && rt.YOLO {
		resultPayload["yolo"] = true
		resultPayload["approval_bypassed_by_yolo"] = true
	}
	b, _ := json.Marshal(resultPayload)
	completionOutput := map[string]any{
		"stdout":                     stdout,
		"stderr":                     stderr,
		"exit_code":                  exitCode,
		"stdout_bytes":               stdoutBytes,
		"stderr_bytes":               stderrBytes,
		"stdout_omitted_bytes":       res.StdoutOmittedBytes,
		"stderr_omitted_bytes":       res.StderrOmittedBytes,
		"sandbox_backend":            strings.TrimSpace(string(decision.Backend)),
		"sandbox_profile":            string(req.profile),
		"sandbox_profile_elevation":  req.profileElevation,
		"sandbox_reason":             strings.TrimSpace(decision.Reason),
		"sandbox_unavailable_reason": strings.TrimSpace(decision.UnavailableReason),
	}
	if retrieveID > 0 {
		completionOutput["retrieve_id"] = retrieveID
		completionOutput["spool_bytes"] = res.OutputSpoolBytes
		completionOutput["spool_omitted_bytes"] = res.OutputSpoolOmittedBytes
	}
	if compression.Meta.Applied {
		completionOutput["output_compression"] = compressionMetaMap(compression.Meta)
		completionOutput["stdout_compressed_bytes"] = len(stdout)
		completionOutput["stdout_saved_bytes"] = compression.Meta.BeforeBytes - compression.Meta.AfterBytes
	}
	captureCompletion := func() {
		CaptureToolCompletion(ctx, ToolCompletionPayload{
			Output: appendToolExecutionMetadata(completionOutput, rt, safety.ToolKindShell),
			Error: func() string {
				if runErr == nil {
					return ""
				}
				return runErr.Error()
			}(),
		})
	}
	var unavailable *safety.UnavailableError
	if errors.As(runErr, &unavailable) {
		captureCompletion()
		return "", runErr
	}
	if denied && req.sandboxPermissions == safety.SandboxPermissionsUseDefault {
		// The retry approval was already requested above, before the escalated
		// attempt this denial may have come from; asking again here would show
		// the same prompt twice for one command.
		denialReason := sandboxDenialReason(res, runErr)
		slog.Error("sandbox denied shell command", "command", req.command, "reason", denialReason, "err", runErr)
		captureCompletion()
		return "", sandboxDeniedCommandError(res, denialReason)
	}
	captureCompletion()
	return string(b), nil
}

func filesystemAllowsWriteAt(roots []string, target string) bool {
	target, err := filepath.Abs(strings.TrimSpace(target))
	if err != nil || target == "" {
		return false
	}
	for _, root := range roots {
		root, rootErr := filepath.Abs(strings.TrimSpace(root))
		if rootErr != nil || root == "" {
			continue
		}
		rel, relErr := filepath.Rel(root, target)
		if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func shellNetworkApprovalHandler(st *State, rt *AgentToolRuntime) safety.NetworkApprovalHandler {
	if st == nil {
		return nil
	}
	return func(ctx context.Context, request safety.NetworkApprovalRequest) (safety.NetworkApprovalDecision, error) {
		// Both hooks may be installed after the shell tool is constructed (the
		// TUI does this when an interactive surface attaches), or supplied only
		// for this gateway run through ctx. Resolve them at request time so the
		// handler neither captures a stale surface nor permanently disables
		// approval because startup ordering happened to leave a hook nil.
		actionHook := st.ActionHook()
		promptHook := NetworkApprovalPromptHookFromContext(ctx, st)
		if actionHook == nil || promptHook == nil {
			return safety.NetworkApprovalDeny, fmt.Errorf("network approval is unavailable")
		}
		target := string(request.Context.Protocol) + "://" + request.Context.Host
		if request.Port > 0 {
			target += ":" + strconv.Itoa(request.Port)
		}
		reason := fmt.Sprintf("Network access to %q is blocked by policy.", target)
		payload := map[string]any{
			"command": request.Command, "sandbox_permissions": string(safety.SandboxPermissionsUseDefault),
			"justification": reason, "force_tool_approval": true, "approval_reason": "network_denied",
			"sandbox_denial_reason": reason, "network_approval_context": request.Context,
			"network_port": request.Port, "network_inline": true, "session_id": request.EnvironmentID,
		}
		actionID, pending, err := actionHook(WithApprovedActionID(ctx, ""), "shell", payload)
		if err != nil {
			return safety.NetworkApprovalDeny, err
		}
		if !pending || strings.TrimSpace(actionID) == "" {
			return safety.NetworkApprovalAllowOnce, nil
		}
		if rt != nil && rt.RunRT != nil && strings.TrimSpace(request.ExecutionID) != "" {
			defer func() { _ = rt.RunRT.ClearWait(context.Background(), request.ExecutionID) }()
		}
		decision, promptErr := promptHook(ctx, actionID, payload, request)
		if promptErr == nil {
			switch decision {
			case safety.NetworkApprovalAllowForSession, safety.NetworkApprovalAllowInFuture:
				st.SetNetworkSessionDecision(request.EnvironmentID, request.Context, request.Port, true)
			case safety.NetworkApprovalDenyForSession:
				st.SetNetworkSessionDecision(request.EnvironmentID, request.Context, request.Port, false)
			}
		}
		return decision, promptErr
	}
}

func approvedNetworkAccessForSandbox(ctx context.Context) *safety.ApprovedNetworkAccess {
	access, ok := ApprovedNetworkAccessFromContext(ctx)
	if !ok {
		return nil
	}
	return &safety.ApprovedNetworkAccess{Context: access.Context, Port: access.Port}
}

func appendUniqueText(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func mergeGlobDepth(current, incoming *int) *int {
	if current == nil || incoming == nil {
		return nil
	}
	value := *current
	if *incoming > value {
		value = *incoming
	}
	return &value
}

func sandboxDeniedCommandError(res safety.CommandResult, reason string) error {
	parts := []string{"sandbox denied command: " + strings.TrimSpace(reason)}
	if stdout := strings.TrimSpace(res.Stdout); stdout != "" {
		parts = append(parts, "stdout:\n"+stdout)
	}
	if stderr := strings.TrimSpace(res.Stderr); stderr != "" {
		parts = append(parts, "stderr:\n"+stderr)
	}
	return errors.New(strings.Join(parts, "\n"))
}

// requestSandboxRetryApproval asks whether a command the sandbox denied may run
// on the host. It reports whether the escalated retry is authorized to run now;
// a pending prompt is returned as a RequiresActionError, so the caller resumes
// through the normal replay path instead of running anything here.
func requestSandboxRetryApproval(ctx context.Context, st *State, rt *AgentToolRuntime, req sandboxedShellRequest, res safety.CommandResult, decision safety.SandboxDecision, runErr error, denialReason string) (bool, error) {
	hook := st.ActionHook()
	if hook == nil {
		return false, nil
	}
	retryReason := sandboxRetryApprovalReason(denialReason)
	payload := map[string]any{
		"command":               req.command,
		"timeout_ms":            req.timeoutMs,
		"sandbox_permissions":   string(safety.SandboxPermissionsRequireEscalated),
		"justification":         retryReason,
		"force_tool_approval":   true,
		"approval_reason":       "sandbox_denied",
		"sandbox_denial_reason": denialReason,
	}
	// Prefer a path-scoped sandbox capability over host execution whenever the
	// denial names a concrete write target and structured approvals are enabled.
	if path := sandboxDeniedWriteRoot(denialReason); path != "" && execPermissionApprovalsEnabled(rt) {
		additional := &safety.RequestPermissionProfile{FileSystem: &safety.FileSystemPermissionProfile{
			Entries: []safety.FileSystemPermissionEntry{{Path: safety.NewFileSystemPermissionPath(path), Access: safety.FileSystemAccessWrite}},
		}}
		payload["sandbox_permissions"] = string(safety.SandboxPermissionsWithAdditionalPermissions)
		payload["additional_permissions"] = additional
		payload["justification"] = "The sandbox blocked writing to " + path + ". Allow this command to write only there?"
		payload["scoped_sandbox_retry"] = true
	}
	if len(req.prefixRule) > 0 {
		payload["prefix_rule"] = append([]string(nil), req.prefixRule...)
	}
	id, pending, err := hook(WithApprovedActionID(ctx, ""), "shell", payload)
	if err != nil {
		return false, err
	}
	if !pending || id == "" {
		// Policy answered without asking anyone. The retry may run — except
		// when the prompt had been narrowed to a path-scoped capability, whose
		// approval must not be spent on unsandboxed host execution instead.
		_, scoped := payload["scoped_sandbox_retry"]
		return !scoped, nil
	}

	// This sandboxed process already ran to completion. Preserve its output as
	// a historical attempt before the logical tool call moves into its
	// approval-waiting state and is replayed on the host.
	CaptureToolAttempt(ctx, ToolCompletionPayload{
		Output:   appendToolExecutionMetadata(sandboxAttemptCompletionOutput(res, decision, req, denialReason, runErr), rt, safety.ToolKindShell),
		Duration: res.ExecutionTiming.Duration,
	})
	CaptureToolRequiresAction(ctx, id, "shell", appendToolExecutionMetadata(map[string]any{
		"requires_action": true,
		"sandbox_retry":   true,
	}, rt, safety.ToolKindShell))
	return false, &RequiresActionError{
		RunID: RunIDFromContext(ctx), AgentID: HookAgentIDFromContext(ctx), SubagentType: SubagentTypeFromContext(ctx),
		// Keep the wait record identical to the pending action payload. In
		// particular, sandbox_denial_reason must reach the approval surface
		// instead of disappearing during the RequiresAction handoff.
		ActionID: id, ActionKind: "shell", ToolName: "shell", ToolInput: payload,
	}
}

var sandboxDeniedAbsolutePath = regexp.MustCompile(`(?:open|create|mkdir|rename|unlink|cp:|rm:)\s+["']?(/[^\n"']+)`)

func sandboxDeniedWriteRoot(reason string) string {
	match := sandboxDeniedAbsolutePath.FindStringSubmatch(reason)
	if len(match) != 2 {
		return ""
	}
	path := strings.TrimSpace(match[1])
	for _, marker := range []string{": operation not permitted", ": permission denied", ": read-only file system", ": Operation not permitted", ": Permission denied"} {
		if i := strings.Index(path, marker); i >= 0 {
			path = path[:i]
		}
	}
	path = filepath.Clean(strings.TrimRight(strings.TrimSpace(path), ":"))
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return ""
	}
	if info, err := os.Stat(path); err == nil {
		if !info.IsDir() {
			path = filepath.Dir(path)
		}
	} else {
		for parent := filepath.Dir(path); parent != path; parent = filepath.Dir(parent) {
			if info, statErr := os.Stat(parent); statErr == nil && info.IsDir() {
				path = parent
				break
			}
		}
	}
	home, _ := os.UserHomeDir()
	if path == string(filepath.Separator) || (home != "" && filepath.Clean(path) == filepath.Clean(home)) {
		return ""
	}
	return path
}

func sandboxAttemptCompletionOutput(res safety.CommandResult, decision safety.SandboxDecision, req sandboxedShellRequest, denialReason string, runErr error) map[string]any {
	exitCode := res.ExitCode
	if runErr != nil && exitCode == 0 {
		exitCode = 1
	}
	return map[string]any{
		"stdout":                res.Stdout,
		"stderr":                res.Stderr,
		"exit_code":             exitCode,
		"stdout_bytes":          res.StdoutBytes,
		"stderr_bytes":          res.StderrBytes,
		"stdout_omitted_bytes":  res.StdoutOmittedBytes,
		"stderr_omitted_bytes":  res.StderrOmittedBytes,
		"sandbox_backend":       strings.TrimSpace(string(decision.Backend)),
		"sandbox_profile":       string(req.profile),
		"sandbox_reason":        strings.TrimSpace(decision.Reason),
		"sandbox_denied":        true,
		"sandbox_denial_reason": strings.TrimSpace(denialReason),
	}
}

func sandboxRetryApprovalReason(denialReason string) string {
	detail := strings.TrimSpace(strings.Join(strings.Fields(denialReason), " "))
	if detail == "" || detail == "sandbox rejected the command" {
		return "The sandbox blocked this command. Allow it to run outside the sandbox?"
	}
	detail = strings.TrimRight(detail, ".?!")
	return "The sandbox blocked this command: " + detail + ". Allow it to run outside the sandbox?"
}

// sandboxRetryApprovalAllowed reports whether a command the sandbox denied may
// ask the user for permission to run on the host.
//
// Only two policies refuse the prompt: "never", which refuses every prompt, and
// "granular" with sandbox_approval turned off, which refuses this one
// specifically. "on-request" allows it — that is precisely the case the policy
// exists for, a command that cannot do its work inside the sandbox. Refusing it
// there returned the denial to the model as a plain error, and the model's
// natural response is to re-run the command with require_escalated, which then
// prompts anyway: the same approval, one wasted round-trip later, and a second
// prompt for a command the user may already have approved.
func sandboxRetryApprovalAllowed(policy safety.ApprovalPolicy) bool {
	switch policy.Mode {
	case safety.ApprovalNever:
		return false
	case safety.ApprovalGranular:
		return policy.Granular.SandboxApproval
	default:
		return true
	}
}

func sandboxDenialReason(res safety.CommandResult, runErr error) string {
	const maxReasonLen = 512
	parts := make([]string, 0, 1)
	if denial := res.NetworkDenial; denial != nil {
		detail := fmt.Sprintf("network access to %q was blocked: %s", denial.Host, denial.Reason)
		parts = append(parts, detail)
	}
	if detail := safety.SandboxDenialDetail(res); detail != "" {
		parts = append(parts, detail)
	}
	if runErr != nil && len(parts) == 0 {
		parts = append(parts, strings.TrimSpace(runErr.Error()))
	}
	if len(parts) == 0 {
		parts = append(parts, strings.TrimSpace(res.Stderr))
	}
	reason := strings.Join(parts, "; ")
	if reason == "" {
		reason = "sandbox rejected the command"
	}
	if len(reason) > maxReasonLen {
		reason = reason[:maxReasonLen-3] + "..."
	}
	return reason
}

func shellToolSettingsSources(rt *AgentToolRuntime) []safety.SourceSettings {
	if rt == nil || rt.Cfg == nil {
		return nil
	}
	return safety.LocalConfigSources(strings.TrimSpace(rt.Home), rt.Cfg)
}

// ApplyFilesystemPolicy installs the active filesystem capabilities and
// restrictions on in-process tools. It is safe to call repeatedly.
func ApplyFilesystemPolicy(st *State, home, cwd string, cfg *appcfg.Root, snap safety.Snapshot) {
	if st == nil || cfg == nil {
		return
	}
	wd := strings.TrimSpace(cwd)
	if wd == "" {
		var err error
		wd, err = os.Getwd()
		if err != nil {
			wd = ""
		}
	}
	if abs, aerr := filepath.Abs(wd); aerr == nil {
		wd = abs
	}
	runtimeCfg := safety.ConvertToRuntimeConfig(
		safety.LocalConfigSources(strings.TrimSpace(home), cfg), snap, wd, wd, os.TempDir(), st.AllowedRoots(),
	)
	st.SetReadPolicy(
		runtimeCfg.Filesystem.DenyRead,
		runtimeCfg.Filesystem.AllowRead,
		runtimeCfg.Filesystem.DenyReadPatterns,
		wd,
	)
	st.SetWritePolicy(runtimeCfg.Filesystem.AllowWrite)
}

func shellAssessmentRequiresApproval(ctx context.Context, st *State, rt *AgentToolRuntime, cmd string, assessment safety.ShellCommandAssessment, readOnlyCommand, sandboxBypass, bypassApproved bool) bool {
	if sandboxBypass && !bypassApproved {
		return true
	}
	if ApprovedActionIDFromContext(ctx) == "" && strictAutoReviewEnabled(ctx, rt, RunIDFromContext(ctx)) {
		return true
	}
	if assessment.Dangerous != nil || assessment.UsedComplexParsing {
		if shellBuiltinRequiresApproval(cmd) {
			return true
		}
	}
	if assessment.KnownSafe {
		return false
	}
	// A configured explicit allow rule is already resolved by the permission
	// middleware; retain that approval and do not ask again. The assessment still
	// controls dangerous/complex cases above.
	if PolicyApprovedFromContext(ctx) || ApprovedActionIDFromContext(ctx) != "" {
		return false
	}
	if rt != nil && (rt.PermissionSnapshotForSession != nil || rt.PermissionSnapshot != nil) {
		mode := permissionSnapshotForContext(ctx, st, rt).ApprovalPolicy.Mode
		switch mode {
		case safety.ApprovalNever:
			return false
		case safety.ApprovalUnlessTrusted:
			return true
		}
	}
	_, _, _ = st, readOnlyCommand, sandboxBypass
	return false
}

func strictAutoReviewEnabled(ctx context.Context, rt *AgentToolRuntime, runID string) bool {
	if rt == nil || (rt.PermissionSnapshotForSession == nil && rt.PermissionSnapshot == nil) {
		return false
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return false
	}
	for _, enabledRunID := range permissionSnapshotForContext(ctx, nil, rt).StrictAutoReviewRunIDs {
		if strings.TrimSpace(enabledRunID) == runID {
			return true
		}
	}
	return false
}

func shellAssessmentApprovalPayload(cmd string, in *ShellInput, assessment safety.ShellCommandAssessment) map[string]any {
	payload := shellApprovalPayload(cmd, in, false)
	payload["command_safety"] = map[string]any{
		"origin":               assessment.Origin,
		"known_safe":           assessment.KnownSafe,
		"used_complex_parsing": assessment.UsedComplexParsing,
	}
	if assessment.Dangerous != nil {
		payload["force_tool_approval"] = true
		payload["approval_reason"] = "dangerous_shell_syntax"
	} else if assessment.UsedComplexParsing {
		payload["force_tool_approval"] = true
		payload["approval_reason"] = "dangerous_shell_syntax"
	} else if in != nil && in.SandboxPermissions != safety.SandboxPermissionsUseDefault {
		payload["force_tool_approval"] = true
		payload["approval_reason"] = string(in.SandboxPermissions)
	} else {
		payload["approval_reason"] = "unknown_shell_command"
	}
	return payload
}

func shellApprovalPayload(cmd string, in *ShellInput, force bool) map[string]any {
	payload := map[string]any{
		"command":             strings.TrimSpace(cmd),
		"sandbox_permissions": string(safety.SandboxPermissionsUseDefault),
	}
	if in != nil {
		payload["timeout_ms"] = in.TimeoutMs
		payload["sandbox_permissions"] = string(in.SandboxPermissions.Normalized())
		if justification := strings.TrimSpace(in.Justification); justification != "" {
			payload["justification"] = justification
		}
		if len(in.PrefixRule) > 0 {
			payload["prefix_rule"] = append([]string(nil), in.PrefixRule...)
		}
		if in.AdditionalPermissions != nil {
			payload["additional_permissions"] = in.AdditionalPermissions
		}
	}
	if force {
		payload["force_tool_approval"] = true
		payload["approval_reason"] = "dangerous_shell_syntax"
	}
	return payload
}

func validateShellAdditionalPermissions(ctx context.Context, in *ShellInput, rt *AgentToolRuntime) error {
	if in == nil {
		return nil
	}
	usesAdditional := in.SandboxPermissions == safety.SandboxPermissionsWithAdditionalPermissions
	if !usesAdditional {
		if in.AdditionalPermissions != nil {
			return fmt.Errorf("`additional_permissions` requires `sandbox_permissions` set to `with_additional_permissions`")
		}
		return nil
	}
	if in.AdditionalPermissions == nil {
		return fmt.Errorf("missing `additional_permissions`; provide at least one of `network` or `file_system` when using `with_additional_permissions`")
	}
	normalized, err := safety.NormalizeRequestPermissionsResponseAtCWD(safety.RequestPermissionsResponse{
		Permissions: *in.AdditionalPermissions,
		Scope:       safety.GrantScopeTurn,
	}, requestPermissionsCWD(ctx))
	if err != nil {
		return err
	}
	if normalized.Permissions.Network == nil && normalized.Permissions.FileSystem == nil {
		return fmt.Errorf("`additional_permissions` must include at least one requested permission in `network` or `file_system`")
	}
	in.AdditionalPermissions = &normalized.Permissions
	if !execPermissionApprovalsEnabled(rt) && !additionalPermissionsPreapproved(ctx, rt, normalized.Permissions) {
		return fmt.Errorf("additional permissions are disabled; enable `features.exec_permission_approvals` before using `with_additional_permissions`")
	}
	return nil
}

func execPermissionApprovalsEnabled(rt *AgentToolRuntime) bool {
	return rt != nil && rt.Cfg != nil && rt.Cfg.EffectiveFeatures().ExecPermissionApprovals
}

func additionalPermissionsPreapproved(ctx context.Context, rt *AgentToolRuntime, requested safety.RequestPermissionProfile) bool {
	if rt == nil || rt.Cfg == nil || !rt.Cfg.EffectiveFeatures().RequestPermissionsTool ||
		(rt.PermissionSnapshotForSession == nil && rt.PermissionSnapshot == nil) {
		return false
	}
	snapshot := permissionSnapshotForRun(permissionSnapshotForContext(ctx, nil, rt), RunIDFromContext(ctx))
	granted := safety.RequestPermissionProfile{}
	if len(snapshot.FileSystemGrants) > 0 {
		granted.FileSystem = &safety.FileSystemPermissionProfile{}
		hasGlob := false
		for _, grant := range snapshot.FileSystemGrants {
			granted.FileSystem.Entries = append(granted.FileSystem.Entries, grant.Entry)
			if grant.Entry.Access != safety.FileSystemAccessDeny ||
				grant.Entry.Path.Type != safety.FileSystemPermissionPathTypeGlobPattern {
				continue
			}
			if !hasGlob {
				granted.FileSystem.GlobScanMaxDepth = cloneIntPointer(grant.GlobScanMaxDepth)
				hasGlob = true
			} else {
				granted.FileSystem.GlobScanMaxDepth = mergeGlobDepth(granted.FileSystem.GlobScanMaxDepth, grant.GlobScanMaxDepth)
			}
		}
	}
	for _, grant := range snapshot.NetworkGrants {
		if grant.Enabled {
			granted.Network = safety.NewNetworkPermissionProfile(true)
			break
		}
	}
	cwd := requestPermissionsCWD(ctx)
	want := safety.RequestPermissionsResponse{Permissions: requested, Scope: safety.GrantScopeTurn}
	materialized, err := safety.IntersectRequestPermissionsResponseAtCWD(want, want, cwd)
	if err != nil {
		return false
	}
	covered, err := safety.IntersectRequestPermissionsResponseAtCWD(
		want,
		safety.RequestPermissionsResponse{Permissions: granted, Scope: safety.GrantScopeTurn},
		cwd,
	)
	return err == nil && reflect.DeepEqual(materialized.Permissions, covered.Permissions)
}

// finalizeShellToolSchema adjusts the reflected shell schema before it reaches
// the model. The shell tool is declared with `json:",omitempty"` on its
// escalation-only fields (`justification`, `prefix_rule`,
// `additional_permissions`), so the reflected schema already marks them
// optional — the model may omit them instead of being forced to emit them.
// This was previously worked around with nullable union types, which weaker
// models frequently mis-handle by emitting string-encoded arrays/objects.
//
// When per-command additional permissions are disabled, its field and enum
// value are dropped so the model is never offered a mode it cannot use.
func finalizeShellToolSchema(tool *llm.Tool, execApprovals bool) error {
	if tool == nil {
		return nil
	}
	raw, err := json.Marshal(tool.InputSchema())
	if err != nil {
		return err
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return err
	}
	properties, _ := schema["properties"].(map[string]any)
	if !execApprovals {
		delete(properties, "additional_permissions")
		if required, ok := schema["required"].([]any); ok {
			filtered := required[:0]
			for _, value := range required {
				if value != "additional_permissions" {
					filtered = append(filtered, value)
				}
			}
			schema["required"] = filtered
		}
		if property, ok := properties["sandbox_permissions"].(map[string]any); ok {
			property["enum"] = []any{string(safety.SandboxPermissionsUseDefault), string(safety.SandboxPermissionsRequireEscalated)}
			// with_additional_permissions is gone from the enum, so drop it from
			// the description too; otherwise the model is told about a mode it
			// cannot use.
			property["description"] = "Per-command sandbox override. Defaults to use_default; use require_escalated for unsandboxed execution."
		}
	}
	return tool.SetInputSchema(schema)
}

func shellBuiltinRequiresApproval(cmd string) bool {
	c := strings.ToLower(cmd)
	subs := []string{
		"~/.ssh",
		"/.ssh/",
		"rm -rf /",
		"mkfs",
		"dd if=/dev/",
		":(){:|:&};:",
		"$(",
		"<(",
		">(",
		"${",
		"$[",
		"~[",
		"(e:",
		"(+",
		"} always {",
		"<#",
		"zmodload",
		"sysopen",
		"sysread",
		"syswrite",
		"zpty",
		"ztcp",
		"zsocket",
		"mapfile",
		"zf_rm",
		"zf_mv",
		"zf_ln",
		"zf_chmod",
		"zf_chown",
		"zf_mkdir",
		"zf_rmdir",
		"zf_chgrp",
		"invoke-expression",
		"start-process powershell",
		"start-process pwsh",
		"[system.io.file]::",
	}
	for _, s := range subs {
		if strings.Contains(c, s) {
			return true
		}
	}
	if containsZshEqualsExpansion(c) {
		return true
	}
	if looksLikePowerShellCommand(cmd) && !powerShellReadOnlySafe(cmd) {
		return true
	}
	return false
}

func looksLikePowerShellCommand(cmd string) bool {
	tokens := safety.SplitShellWords(cmd)
	if len(tokens) == 0 {
		return false
	}
	first := strings.ToLower(strings.TrimSpace(tokens[0]))
	if isPowerShell(first) {
		return true
	}
	script := strings.ToLower(strings.TrimSpace(cmd))
	if strings.Contains(script, "]::") {
		return true
	}
	for _, name := range powerShellBlockedCmdlets() {
		if first == name {
			return true
		}
	}
	for _, name := range powerShellExplicitReadOnlyCmdlets() {
		if first == name {
			return true
		}
	}
	return false
}

func powerShellReadOnlySafe(cmd string) bool {
	script, ok := powerShellScript(cmd)
	if !ok {
		script = strings.TrimSpace(cmd)
	}
	if script == "" {
		return false
	}
	return powerShellScriptReadOnlySafe(script)
}

func powerShellScript(cmd string) (string, bool) {
	tokens := safety.SplitShellWords(cmd)
	if len(tokens) == 0 || !isPowerShell(strings.ToLower(tokens[0])) {
		return "", false
	}
	for i := 1; i < len(tokens); i++ {
		tok := strings.ToLower(strings.TrimSpace(tokens[i]))
		if tok == "" {
			continue
		}
		switch tok {
		case "-encodedcommand", "-enc", "-e":
			return "", true
		case "-executionpolicy", "-ep":
			if i+1 >= len(tokens) {
				return "", true
			}
			policy := strings.ToLower(strings.TrimSpace(tokens[i+1]))
			if policy == "bypass" || policy == "unrestricted" {
				return "", true
			}
			i++
		case "-command", "-c":
			if i+1 >= len(tokens) {
				return "", true
			}
			return strings.Join(tokens[i+1:], " "), true
		case "-file", "-f":
			return "", true
		default:
			if strings.HasPrefix(tok, "-encodedcommand:") || strings.HasPrefix(tok, "-encodedcommand=") {
				return "", true
			}
			if strings.HasPrefix(tok, "-executionpolicy:") || strings.HasPrefix(tok, "-executionpolicy=") {
				policy := strings.TrimLeft(strings.TrimPrefix(strings.TrimPrefix(tok, "-executionpolicy"), ":"), "=")
				if policy == "bypass" || policy == "unrestricted" {
					return "", true
				}
			}
		}
	}
	return "", true
}

func isPowerShell(name string) bool {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "powershell", "powershell.exe", "pwsh", "pwsh.exe":
		return true
	default:
		return false
	}
}

func powerShellScriptReadOnlySafe(script string) bool {
	lower := strings.ToLower(strings.TrimSpace(script))
	if lower == "" {
		return false
	}
	if powerShellHasUnsafePath(lower) {
		return false
	}
	for _, blocked := range powerShellBlockedCmdlets() {
		if strings.Contains(lower, blocked) {
			return false
		}
	}
	if strings.Contains(lower, "-encodedcommand") ||
		strings.Contains(lower, "-executionpolicy bypass") ||
		strings.Contains(lower, "-executionpolicy unrestricted") {
		return false
	}
	if !powerShellAllowedDotNetTypes(lower) {
		return false
	}
	tokens := safety.SplitShellWords(script)
	if len(tokens) == 0 {
		return false
	}
	first := strings.ToLower(strings.TrimSpace(tokens[0]))
	if strings.HasPrefix(first, "[") {
		return strings.Contains(first, "]::")
	}
	for _, allowed := range powerShellReadOnlyCmdlets() {
		if first == allowed {
			return powerShellAllowedParameters(first, tokens[1:])
		}
	}
	return false
}

func powerShellBlockedCmdlets() []string {
	return []string{
		"invoke-expression",
		"iex ",
		"start-process",
		"remove-item",
		"set-content",
		"add-content",
		"out-file",
		"new-item",
		"copy-item",
		"move-item",
		"rename-item",
		"clear-content",
		"set-acl",
		"stop-process",
		"invoke-webrequest",
		"curl ",
		"wget ",
	}
}

func powerShellReadOnlyCmdlets() []string {
	return []string{
		"get-childitem",
		"gci",
		"dir",
		"ls",
		"get-content",
		"gc",
		"cat",
		"select-string",
		"get-location",
		"pwd",
		"get-item",
		"resolve-path",
		"test-path",
		"measure-object",
		"where-object",
		"foreach-object",
		"sort-object",
		"select-object",
		"format-table",
		"format-list",
	}
}

func powerShellExplicitReadOnlyCmdlets() []string {
	return []string{
		"get-childitem",
		"gci",
		"get-content",
		"select-string",
		"get-location",
		"get-item",
		"resolve-path",
		"test-path",
		"measure-object",
		"where-object",
		"foreach-object",
		"sort-object",
		"select-object",
		"format-table",
		"format-list",
	}
}

func powerShellAllowedParameters(cmdlet string, args []string) bool {
	allowed, ok := powerShellCmdletSafeParameters(cmdlet)
	if !ok {
		return false
	}
	for _, arg := range args {
		a := strings.TrimSpace(arg)
		if a == "" || !strings.HasPrefix(a, "-") {
			continue
		}
		name := strings.ToLower(a)
		if idx := strings.IndexAny(name, ":="); idx >= 0 {
			name = name[:idx]
		}
		if _, ok := powerShellCommonParameters()[name]; ok {
			continue
		}
		if _, ok := allowed[name]; ok {
			continue
		}
		return false
	}
	return true
}

func powerShellCmdletSafeParameters(cmdlet string) (map[string]struct{}, bool) {
	add := func(names ...string) map[string]struct{} {
		out := make(map[string]struct{}, len(names))
		for _, name := range names {
			out[strings.ToLower(name)] = struct{}{}
		}
		return out
	}
	switch strings.ToLower(strings.TrimSpace(cmdlet)) {
	case "get-childitem", "gci", "dir", "ls":
		return add("-path", "-literalpath", "-filter", "-include", "-exclude", "-recurse", "-depth", "-name", "-force", "-attributes", "-directory", "-file", "-hidden", "-readonly", "-system"), true
	case "get-content", "gc", "cat":
		return add("-path", "-literalpath", "-totalcount", "-head", "-tail", "-raw", "-encoding", "-delimiter", "-readcount"), true
	case "select-string":
		return add("-path", "-literalpath", "-pattern", "-inputobject", "-simplematch", "-casesensitive", "-quiet", "-list", "-notmatch", "-allmatches", "-encoding", "-context", "-raw", "-noemphasis"), true
	case "get-location", "pwd":
		return add("-stackname", "-psprovider", "-psdrive"), true
	case "get-item":
		return add("-path", "-literalpath", "-force", "-stream"), true
	case "resolve-path":
		return add("-path", "-literalpath", "-relative"), true
	case "test-path":
		return add("-path", "-literalpath", "-pathtype", "-filter", "-include", "-exclude", "-isvalid", "-newerthan", "-olderthan"), true
	case "measure-object":
		return add("-inputobject", "-property", "-sum", "-average", "-maximum", "-minimum", "-line", "-word", "-character", "-ignorewhitespace", "-allstats", "-standarddeviation"), true
	case "where-object", "foreach-object", "sort-object", "select-object", "format-table", "format-list":
		return add("-inputobject", "-property", "-filterScript", "-process", "-begin", "-end", "-descending", "-unique", "-first", "-last", "-skip", "-expandproperty", "-autosize", "-wrap"), true
	default:
		return nil, false
	}
}

func powerShellCommonParameters() map[string]struct{} {
	return map[string]struct{}{
		"-verbose":             {},
		"-debug":               {},
		"-erroraction":         {},
		"-warningaction":       {},
		"-informationaction":   {},
		"-errorvariable":       {},
		"-warningvariable":     {},
		"-informationvariable": {},
		"-outvariable":         {},
		"-outbuffer":           {},
		"-pipelinevariable":    {},
		"-whatif":              {},
		"-confirm":             {},
	}
}

// powerShellHasUnsafePath reports whether a script reaches outside the paths it
// names. Only traversal is unsafe: repository metadata under the workspace is
// an ordinary path like any other file in the project.
func powerShellHasUnsafePath(script string) bool {
	return strings.Contains(script, "../") ||
		strings.Contains(script, `..\`) ||
		strings.Contains(script, "'..") ||
		strings.Contains(script, "\"..")
}

func powerShellAllowedDotNetTypes(script string) bool {
	allowed := map[string]struct{}{
		"system.convert":       {},
		"system.datetime":      {},
		"system.guid":          {},
		"system.math":          {},
		"system.text.encoding": {},
		"system.timespan":      {},
	}
	for rest := script; ; {
		start := strings.Index(rest, "[")
		if start < 0 {
			return true
		}
		afterStart := rest[start+1:]
		end := strings.Index(afterStart, "]::")
		if end < 0 {
			return true
		}
		typeName := strings.TrimSpace(afterStart[:end])
		if _, ok := allowed[typeName]; !ok {
			return false
		}
		rest = afterStart[end+3:]
	}
}

func containsZshEqualsExpansion(s string) bool {
	if strings.HasPrefix(s, "=") && len(s) > 1 && isCommandNameStart(s[1]) {
		return true
	}
	for i := 0; i+2 < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', ';', '&', '|':
			if s[i+1] == '=' && isCommandNameStart(s[i+2]) {
				return true
			}
		}
	}
	return false
}

func isCommandNameStart(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_'
}

// shellAttemptEnv returns the environment for one execution attempt. Cache
// overrides belong only to a sandboxed attempt: an escalated retry runs on the
// host and must retain the host's normal Go caches.
func shellAttemptEnv(base, cache []string, sandboxed bool) []string {
	env := append([]string(nil), base...)
	if !sandboxed {
		return env
	}
	return append(env, cache...)
}

// shellProfilePlumbsGoCache reports whether commands under this profile need the
// Go caches to be reachable.
//
// A workspace-write profile can write both the workspace and its caches. A
// managed profile's scope is whatever the permission profile in force grants, so
// its caches are worth plumbing exactly when it can write the project anyway.
// A read-only profile grants neither, which is why the caches have to be asked
// for explicitly there - but only where a sandbox can answer the request: with
// no sandbox there is no grant to make, and the env alone would move a `go`
// invocation off the caches the host already has warm and onto cold ones.
func shellProfilePlumbsGoCache(profile safety.Profile, sandboxApplied bool, writableRoots []string, cwd string) bool {
	switch profile {
	case safety.ProfileWorkspaceWrite:
		return sandboxApplied
	case safety.ProfileManaged:
		return sandboxApplied && filesystemAllowsWriteAt(writableRoots, cwd)
	case safety.ProfileReadOnly:
		return sandboxApplied
	default:
		return false
	}
}

// shellSandboxProfile selects the filesystem boundary for the one ordinary shell
// tool. Tool visibility deliberately does not choose a second shell primitive:
// typed workers receive shell too, but the OS sandbox receives this profile.
func shellSandboxProfile(ctx context.Context) (safety.Profile, bool) {
	subtype := strings.ToLower(strings.TrimSpace(SubagentTypeFromContext(ctx)))
	readOnlyWorker := false
	switch subtype {
	case "explore", "plan", "cavecrew-investigator", "cavecrew-reviewer":
		readOnlyWorker = true
	}
	// Plan mode is a read-only phase for the session's own agent, not only for
	// typed workers. GuardWrite cannot carry that promise on its own: shell
	// writes never pass through it, so without this the plan-mode reminder
	// ("mutating shell commands are BLOCKED until exit_plan_mode") would be a
	// claim the runtime does not keep, and one auto-approved `sed -i` or `git
	// commit` would edit the project mid-planning.
	if strings.EqualFold(strings.TrimSpace(ModeFromContext(ctx)), string(state.ModePlan)) {
		readOnlyWorker = true
	}
	if !readOnlyWorker {
		return safety.ProfileWorkspaceWrite, false
	}
	// An approved action authorizes only the replayed tool call. The
	// orchestration wrapper removes this context value before subsequent calls,
	// so this cannot become a persistent write grant or host-sandbox bypass.
	if ApprovedActionIDFromContext(ctx) != "" {
		return safety.ProfileWorkspaceWrite, true
	}
	return safety.ProfileReadOnly, false
}

// promoteReadOnlyWriteScope turns a read-only request that resolved write
// exceptions into an explicit managed request, and returns the readable paths
// that promotion has to re-state.
//
// A strict read-only profile never consumes writable path slices - the backend
// nils them - so a request can only carry its exceptions by naming the managed
// profile, whose scope is exactly the paths it states. Only structured grants
// and the build caches reach this point, and the flag is what keeps the
// confined request from also inheriting the configured workspace roots: a
// plan-mode command granted its build cache must not be able to edit the
// project it is planning.
func promoteReadOnlyWriteScope(req *sandboxedShellRequest, extraBinds, allowRead []string) []string {
	if req == nil || req.profile != safety.ProfileReadOnly || len(extraBinds) == 0 {
		return allowRead
	}
	req.profile = safety.ProfileManaged
	// The resolved set is the whole policy rather than an addition to the
	// configured workspace roots: the promotion exists to keep a boundary, and
	// inheriting the workspace here would leave it meaningless.
	req.exclusiveWritablePaths = true
	// A managed request also states its readable paths, and the read-only
	// profile it replaces would have made the working directory readable. Keep
	// that: what the promotion buys is the write exception, not the loss of the
	// project it was granted in service of.
	return MergeAllowedRootPaths(allowRead, []string{req.cwd})
}

// PlanModeShellGateDecision is what the plan-mode shell gate does with one
// command: run it, refuse it, or ask the operator.
type PlanModeShellGateDecision int

const (
	// PlanModeShellAllow runs the command without asking.
	PlanModeShellAllow PlanModeShellGateDecision = iota
	// PlanModeShellRefuse refuses the command outright. Only a command proven to
	// write reaches this, and it matches what GuardWrite already does for the
	// file tools.
	PlanModeShellRefuse
	// PlanModeShellAsk forces an approval for the command, because nothing about
	// it says whether it writes and the operator is the one who can decide.
	PlanModeShellAsk
)

// planModeShellGate decides what plan mode does with one shell command.
//
// The gate exists for the one configuration where the OS sandbox is not applied
// at all: danger-full-access runs every command on the host, so the read-only
// profile above is advisory and a mutating command would edit the project while
// the session is still planning. The file tools already refuse such writes there
// (GuardWrite is process-level, not sandbox-level), so refusing here is what
// makes plan mode mean the same thing for both. Everywhere else the sandbox is
// the enforcement and this is a no-op, so ordinary plan-mode exploration keeps
// running unprompted.
//
// The three answers follow from what the classifier can prove, not from what it
// recognises: a command that cannot be shown to be read-only is a question for
// the user, and only a proven write is refused. Refusing the unproven ones was
// what made every command outside a whitelist a flat denial, and it asked the
// classifier a question it does not answer - "is this harmless?" - to make a
// decision that needed "does this write?".
//
// An approval for this exact call is still an escape hatch: the user
// deliberately allowing one command is a decision the runtime should honor. That
// is also why PlanModeShellAsk exists - it is the tier whose answer can only
// come from them.
func planModeShellGate(ctx context.Context, dangerFullAccess bool, mutation safety.ShellMutation) PlanModeShellGateDecision {
	if mutation == safety.ShellMutationReadOnly {
		return PlanModeShellAllow
	}
	if !strings.EqualFold(strings.TrimSpace(ModeFromContext(ctx)), string(state.ModePlan)) {
		return PlanModeShellAllow
	}
	if !dangerFullAccess {
		return PlanModeShellAllow
	}
	if ApprovedActionIDFromContext(ctx) != "" {
		return PlanModeShellAllow
	}
	if mutation == safety.ShellMutationWrites {
		return PlanModeShellRefuse
	}
	return PlanModeShellAsk
}

// PlanModeShellGateForCommand is planModeShellGate for callers that hold only
// the configuration, not the tool state: the approval boundary has to know which
// way plan mode will answer a command *before* it raises an approval of its own.
//
// Without it the boundary asks about any command the sandbox will not contain,
// and its approval arrives ahead of the gate: a command plan mode provably
// refuses is offered to the user as something they may allow, and one plan mode
// wants to question is asked without the sentence that explains why. The gate is
// the plan-mode authority, so it has to answer first. The allowed roots do not
// matter here - they can only turn a read-only verdict into doubt, and both the
// other verdicts take the same path.
func PlanModeShellGateForCommand(ctx context.Context, dangerFullAccess bool, cmd string) PlanModeShellGateDecision {
	return planModeShellGate(ctx, dangerFullAccess, safety.ClassifyShellCommand(cmd, nil, ""))
}

// shellAllowedRoots returns the roots that the shell classifier may treat as
// readable for this run. Filesystem grants are capabilities for classification;
// the sandbox still receives the filtered snapshot separately at execution time.
func shellAllowedRoots(ctx context.Context, st *State, rt *AgentToolRuntime) []string {
	if st == nil {
		return nil
	}
	roots := MergeAllowedRootPaths(st.AllowedRoots(), st.LoadedSkillRoots())
	roots = MergeAllowedRootPaths(roots, st.PermissionRoots(safety.FileSystemAccessRead))
	if len(roots) == 0 && rt != nil && strings.TrimSpace(rt.WorkspaceRoot) != "" {
		roots = MergeAllowedRootPaths(roots, []string{rt.WorkspaceRoot})
	}

	snap := permissionSnapshotForContext(ctx, st, rt)
	filtered := permissionSnapshotForRun(snap, RunIDFromContext(ctx))
	for _, grant := range filtered.FileSystemGrants {
		switch grant.Entry.Access {
		case safety.FileSystemAccessRead, safety.FileSystemAccessWrite:
			path := strings.TrimSpace(grant.Entry.Path.Path)
			if path == "" {
				continue
			}
			roots = MergeAllowedRootPaths(roots, []string{path})
		}
	}
	return roots
}

// shell_rewrite.go — command rewriting on the shell tool's inbound path.
//
// Rewriting happens before approval, so the command
// the user sees and approves is the command that runs. Nothing downstream of
// this point may re-derive the command, or the approval would no longer describe
// execution.
//
// The rewriter only inserts presentation-only flags (pager off, color off,
// progress off) that the rules table declares and the safety verifier
// re-validates. It never adds behavior-changing options, never reorders
// operands, and never introduces shell structure. See this package.

// shellRewriteOutcome carries the rewritten command plus what changed, so the
// approval payload and telemetry can explain the difference to the user.
type shellRewriteOutcome struct {
	Command  string
	Original string
	Applied  bool
	Inserted []string
	Note     string
}

// rewriteShellCommand applies presentation-flag rewriting to a shell command.
// It is fail-open by construction: any doubt returns the original command.
func rewriteShellCommand(ctx context.Context, rt *AgentToolRuntime, cmd string) shellRewriteOutcome {
	out := shellRewriteOutcome{Command: cmd, Original: cmd}
	if strings.TrimSpace(cmd) == "" {
		return out
	}
	if !shellRewriteEnabled(rt) {
		return out
	}
	res := safety.Rewrite(cmd)
	if !res.Applied || strings.TrimSpace(res.Command) == "" {
		return out
	}
	out.Command = res.Command
	out.Applied = true
	out.Inserted = res.Inserted
	out.Note = res.Note()
	slog.Debug("rewrote shell command",
		"original", cmd,
		"rewritten", res.Command,
		"inserted", strings.Join(res.Inserted, " "),
		"session", llm.AgentSessionIDFromContext(ctx),
	)
	return out
}

// shellRewriteEnabled reports whether rewriting is on. Config wins when it
// speaks; otherwise safety's command-rewriting environment override decides.
func shellRewriteEnabled(rt *AgentToolRuntime) bool {
	if rt == nil || rt.Cfg == nil {
		return true
	}
	return rt.Cfg.Tools.CommandRewrite.UseRewrite()
}

// applyTo records the rewrite on an approval payload so the user can see that
// the command was adjusted and exactly which flags were added.
func (o shellRewriteOutcome) applyTo(payload map[string]any) map[string]any {
	if payload == nil || !o.Applied {
		return payload
	}
	payload["command_rewritten"] = true
	payload["original_command"] = o.Original
	if len(o.Inserted) > 0 {
		payload["rewrite_inserted_flags"] = o.Inserted
	}
	if o.Note != "" {
		payload["rewrite_note"] = o.Note
	}
	return payload
}

const (
	goCacheTTL        = 30 * 24 * time.Hour
	goCacheQuota      = int64(10 * 1024 * 1024 * 1024)
	goCachePruneEvery = 24 * time.Hour
)

type goCacheAccess struct {
	Writable []string
	Readable []string
	Env      []string
}

type hostGoEnv struct {
	GOCACHE    string
	GOMODCACHE string
	GOPROXY    string
}

var goCachePruneMu sync.Mutex

// resolveGoCacheAccess installs cache capabilities for every command launched
// from a Go project, so wrappers such as make, task, and project scripts inherit
// them without brittle shell parsing.
func resolveGoCacheAccess(ctx context.Context, rt *AgentToolRuntime, cwd string) goCacheAccess {
	if rt == nil || rt.Cfg == nil || strings.TrimSpace(rt.Home) == "" {
		return goCacheAccess{}
	}
	mode := rt.Cfg.SandboxWorkspaceWrite.EffectiveGoCacheMode()
	if mode == appcfg.GoCacheModeDisabled {
		return goCacheAccess{}
	}
	projectRoot := findGoProjectRoot(cwd)
	if projectRoot == "" {
		return goCacheAccess{}
	}
	host, ok := queryHostGoEnv(ctx)
	if !ok {
		return goCacheAccess{}
	}
	if mode == appcfg.GoCacheModeShared {
		return goCacheAccess{Writable: nonEmptyPaths(host.GOCACHE, host.GOMODCACHE)}
	}

	sum := sha256.Sum256([]byte(filepath.Clean(projectRoot)))
	key := hex.EncodeToString(sum[:8])
	base := filepath.Join(rt.Home, "cache", "go")
	root := filepath.Join(base, key)
	buildDir := filepath.Join(root, "build")
	modDir := filepath.Join(root, "mod")
	tmpDir := filepath.Join(root, "tmp")
	for _, dir := range []string{root, buildDir, modDir, tmpDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return goCacheAccess{}
		}
		_ = os.Chmod(dir, 0o700)
	}
	_ = os.Chtimes(root, time.Now(), time.Now())

	env := []string{"GOCACHE=" + buildDir, "GOMODCACHE=" + modDir, "GOTMPDIR=" + tmpDir}
	var readable []string
	downloadCache := filepath.Join(host.GOMODCACHE, "cache", "download")
	if info, err := os.Stat(downloadCache); err == nil && info.IsDir() {
		readable = append(readable, downloadCache)
		localProxy := (&url.URL{Scheme: "file", Path: downloadCache}).String()
		env = append(env, "GOPROXY="+prependGoProxy(localProxy, host.GOPROXY))
	}
	maybePruneGoCaches(base, key)
	return goCacheAccess{Writable: []string{buildDir, modDir, tmpDir}, Readable: readable, Env: env}
}

func findGoProjectRoot(start string) string {
	dir, err := filepath.Abs(strings.TrimSpace(start))
	if err != nil || dir == "" {
		return ""
	}
	for {
		for _, marker := range []string{"go.work", "go.mod"} {
			if info, statErr := os.Stat(filepath.Join(dir, marker)); statErr == nil && !info.IsDir() {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func queryHostGoEnv(parent context.Context) (hostGoEnv, bool) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		return hostGoEnv{}, false
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, goPath, "env", "-json", "GOCACHE", "GOMODCACHE", "GOPROXY")
	cmd.Env = home.SafeSubprocessEnv(nil, home.Options{})
	out, err := cmd.Output()
	if err != nil {
		return hostGoEnv{}, false
	}
	var raw map[string]string
	if json.Unmarshal(out, &raw) != nil {
		return hostGoEnv{}, false
	}
	result := hostGoEnv{
		GOCACHE: strings.TrimSpace(raw["GOCACHE"]), GOMODCACHE: strings.TrimSpace(raw["GOMODCACHE"]), GOPROXY: strings.TrimSpace(raw["GOPROXY"]),
	}
	return result, result.GOCACHE != "" && result.GOMODCACHE != ""
}

func prependGoProxy(local, original string) string {
	local = strings.TrimSpace(local)
	original = strings.TrimSpace(original)
	if local == "" {
		return original
	}
	if original == "" || original == "off" {
		return local + ",off"
	}
	if strings.Contains(original, local) {
		return original
	}
	return local + "," + original
}

func nonEmptyPaths(paths ...string) []string {
	var out []string
	for _, path := range paths {
		if path = strings.TrimSpace(path); path != "" {
			out = append(out, filepath.Clean(path))
		}
	}
	return out
}

type goCacheDirInfo struct {
	path string
	mod  time.Time
	size int64
}

// removeGoCacheDir prepares only directories in a cache tree for recursive
// deletion. Go's module cache makes extracted module directories read-only, but
// unlinking their files still requires writable parent directories. WalkDir does
// not follow symlinks, and files do not need their own mode changed to unlink.
func removeGoCacheDir(path string) error {
	_ = filepath.WalkDir(path, func(current string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil || !entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		// Owner read/write/execute is the minimum directory mode needed to
		// enumerate and remove a tree. Existing permission bits are preserved.
		_ = os.Chmod(current, info.Mode().Perm()|0o700)
		return nil
	})
	return os.RemoveAll(path)
}

func maybePruneGoCaches(base, activeKey string) {
	goCachePruneMu.Lock()
	defer goCachePruneMu.Unlock()
	marker := filepath.Join(base, ".last-prune")
	if info, err := os.Stat(marker); err == nil && time.Since(info.ModTime()) < goCachePruneEvery {
		return
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	now := time.Now()
	var dirs []goCacheDirInfo
	var total int64
	pruneComplete := true
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == activeKey {
			continue
		}
		path := filepath.Join(base, entry.Name())
		info, infoErr := entry.Info()
		if infoErr != nil {
			pruneComplete = false
			continue
		}
		if now.Sub(info.ModTime()) > goCacheTTL {
			if err := removeGoCacheDir(path); err != nil {
				pruneComplete = false
			}
			continue
		}
		size := directorySize(path)
		total += size
		dirs = append(dirs, goCacheDirInfo{path: path, mod: info.ModTime(), size: size})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].mod.Before(dirs[j].mod) })
	for _, dir := range dirs {
		if total <= goCacheQuota {
			break
		}
		if err := removeGoCacheDir(dir.path); err == nil {
			total -= dir.size
		} else {
			pruneComplete = false
		}
	}
	if !pruneComplete {
		return
	}
	if f, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
		_ = f.Close()
	}
}

func directorySize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
