// Permission evaluation: the tool middleware, helpers, and the runtime facade.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// newToolPermissionMiddleware is the single approval boundary shared by built-
// in, skill, and MCP tools. PreToolUse middleware is registered before it, so
// hooks can block or rewrite input; rewritten input is then evaluated here and
// finally executed by the tool (whose shell path applies the sandbox).
func (r *Runner) newToolPermissionMiddleware() llm.ToolMiddleware {
	return func(llmTool *llm.Tool, next llm.ToolHandler) llm.ToolHandler {
		return func(ctx context.Context, arguments string) (any, error) {
			if r == nil || llmTool == nil {
				return next(ctx, arguments)
			}
			name := strings.TrimSpace(llmTool.Name())
			payload := toolArgumentsMap(arguments)
			// What tool policy already blocks is refused before the operator is
			// asked about it: a prompt whose only outcome can be a refusal is
			// noise, and for a subagent it interrupts the user over a tool the
			// child was never allowed to call. The tools re-check this
			// themselves; running it here only moves the answer earlier.
			if r.tools != nil {
				if err := r.tools.GuardTool(ctx, name); err != nil {
					return nil, err
				}
			}
			sessionID := tool.ConversationSessionIDFromContext(ctx)
			decision := r.evaluateToolPermission(sessionID, name, payload)
			decision = r.applyMCPToolApprovalPolicy(ctx, name, decision)
			requestPermissions := strings.EqualFold(name, "request_permissions")
			if decision.Behavior == safety.BehaviorDeny {
				if requestPermissions {
					return deniedRequestPermissionsResult(ctx, next, arguments, requestPermissionsPolicyDenyReason(decision))
				}
				return nil, formatPermissionDenyError(decision, name, payload)
			}

			// A replay approval is scoped to this one call by the orchestration
			// layer. Do not create a second pending action for it.
			if tool.ApprovedActionIDFromContext(ctx) != "" {
				approvedCtx := tool.WithPolicyApproved(ctx, true)
				if r.approvedActionBypassesSandbox(ctx) {
					approvedCtx = tool.WithSandboxBypassApproved(approvedCtx, true)
				}
				if access, ok := r.approvedActionNetworkAccess(ctx); ok {
					approvedCtx = tool.WithApprovedNetworkAccess(approvedCtx, access)
				}
				if requestPermissions {
					tool.CapturePolicyApprovalReason(approvedCtx, "the user approved this request in an approval prompt")
				}
				return next(approvedCtx, arguments)
			}
			if strings.EqualFold(name, "shell") && tool.ShellCommandIsApplyPatch(permissionInput(name, payload)) {
				// The patch handler evaluates all affected paths as one operation.
				// Routing it through the generic shell prompt would approve an
				// opaque heredoc before those paths have been inspected.
				return next(ctx, arguments)
			}

			if r.tools != nil {
				if meta, known := r.tools.ToolMetaByName(name); known && strings.TrimSpace(meta.Category) != "" {
					payload["category"] = strings.TrimSpace(meta.Category)
					if strings.EqualFold(strings.TrimSpace(meta.Category), "mcp") {
						payload["mcp_approval_mode"] = appcfg.MCPToolApprovalMode(meta.MCPApprovalMode).Normalized()
					}
				}
			}
			if decision.Behavior == safety.BehaviorAllow {
				approvedCtx := tool.WithPolicyApproved(ctx, true)
				if decision.BypassSandbox {
					approvedCtx = tool.WithSandboxBypassApproved(approvedCtx, true)
				}
				if requestPermissions {
					tool.CapturePolicyApprovalReason(approvedCtx, requestPermissionsApprovalExemptionReason(decision))
				}
				return next(approvedCtx, arguments)
			}
			// Known-safe commands skip the prompt under unless-trusted, matching
			// the upstream safelist, whichever default reason was reached.
			if strings.EqualFold(name, "shell") && decision.IsUnmatchedAsk() &&
				r.PermissionSnapshotForSession(sessionID).ApprovalPolicy.Mode == safety.ApprovalUnlessTrusted &&
				safety.ShellCommandIsKnownSafe(permissionInput(name, payload)) {
				return next(tool.WithPolicyApproved(ctx, true), arguments)
			}
			if requestPermissions {
				policy := r.PermissionSnapshotForSession(sessionID).ApprovalPolicy
				if policy.Mode == safety.ApprovalGranular && !policy.Granular.RequestPermissions {
					return deniedRequestPermissionsResult(ctx, next, arguments, "request_permissions approval prompts are disabled by the granular approval policy")
				}
				// The tool normalizes paths and owns the structured permission
				// approval event. Generic middleware must not pre-authorize it.
				return next(ctx, arguments)
			}
			if toolOwnsApprovalProtocol(name) {
				return next(ctx, arguments)
			}
			// Plan mode owns the answer for a shell command it has judged. In
			// the one configuration where no sandbox contains the command, this
			// boundary asks about every unmatched one - which arrives ahead of
			// the gate, and would offer the user a command plan mode provably
			// refuses as something they may allow. The shell tool raises the
			// ask for the tier that needs one, with the sentence that explains
			// it; the refusal and the read-only pass-through come from there
			// too.
			if strings.EqualFold(name, "shell") &&
				tool.PlanModeShellGateForCommand(ctx, r.sessionConfig(ctx).DangerFullAccessEnabled(), permissionInput(name, payload)) != tool.PlanModeShellAllow {
				return next(ctx, arguments)
			}

			id, pending, err := r.actionHook(ctx, name, payload)
			if err != nil {
				return nil, err
			}
			if pending && strings.TrimSpace(id) != "" {
				tool.CaptureToolRequiresAction(ctx, id, name, map[string]any{"requires_action": true})
				return nil, &tool.RequiresActionError{
					ActionID:   id,
					ActionKind: name,
					ToolName:   name,
					ToolInput:  payload,
				}
			}
			return next(tool.WithPolicyApproved(ctx, true), arguments)
		}
	}
}

// sessionConfig is the configuration the calling conversation's sandbox
// decisions are made under: AppCfg, with the conversation's own sandbox mode
// in force when it picked one for itself.
func (r *Runner) sessionConfig(ctx context.Context) *appcfg.Root {
	return safety.ConfigForSnapshot(r.AppCfg, r.PermissionSnapshotForSession(tool.ConversationSessionIDFromContext(ctx)))
}

func (r *Runner) sandboxAllowsFileMutation(ctx context.Context, kind string, payload map[string]any, decision safety.Decision) bool {
	if r == nil || r.AppCfg == nil || decision.Behavior != safety.BehaviorAsk || decision.Reason != "default_ask" {
		return false
	}
	switch safety.CanonicalToolName(kind) {
	case "Write", "Edit", "MultiEdit":
	default:
		return false
	}
	snapshot := r.PermissionSnapshotForSession(tool.ConversationSessionIDFromContext(ctx))
	cfg := safety.ConfigForSnapshot(r.AppCfg, snapshot)
	if cfg.DangerFullAccessEnabled() {
		return true
	}
	if cfg.SandboxMode != appcfg.SandboxModeWorkspaceWrite {
		return false
	}

	targets := approvalPayloadPaths(payload)
	if len(targets) == 0 {
		return false
	}
	cwd := strings.TrimSpace(tool.ProjectRootFromContext(ctx))
	if cwd == "" {
		cwd = strings.TrimSpace(r.ProjectRoot)
	}
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return false
		}
	}
	var err error
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return false
	}
	additionalDirs := []string(nil)
	if r.tools != nil {
		additionalDirs = r.tools.AllowedRoots()
	}
	runtimeConfig := safety.ConvertToRuntimeConfig(
		safety.LocalConfigSources(strings.TrimSpace(r.Home), cfg),
		snapshot, cwd, cwd, os.TempDir(), additionalDirs,
	)
	for _, target := range targets {
		if !filepath.IsAbs(target) {
			target = filepath.Join(cwd, target)
		}
		target, err = filepath.Abs(target)
		if err != nil {
			return false
		}
		for _, denied := range runtimeConfig.Filesystem.DenyWrite {
			if sandboxPathContains(denied, target) {
				return false
			}
		}
		allowed := false
		for _, root := range runtimeConfig.Filesystem.AllowWrite {
			if sandboxPathContains(root, target) {
				allowed = true
				break
			}
		}
		if !allowed || permissionSnapshotDeniesWrite(snapshot, target, cwd, tool.RunIDFromContext(ctx)) {
			return false
		}
	}
	return true
}

func approvalPayloadPaths(payload map[string]any) []string {
	var paths []string
	switch values := payload["resolved_paths"].(type) {
	case []string:
		paths = append(paths, values...)
	case []any:
		for _, value := range values {
			if path, ok := value.(string); ok {
				paths = append(paths, path)
			}
		}
	}
	if len(paths) == 0 {
		if path := strings.TrimSpace(payloadString(payload, "resolved_file_path")); path != "" {
			paths = append(paths, path)
		} else if path := approvalPayloadPath(payload); path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func permissionSnapshotDeniesWrite(snapshot safety.Snapshot, target, cwd, runID string) bool {
	for _, grant := range snapshot.FileSystemGrants {
		if grant.Scope == safety.GrantScopeTurn && (runID == "" || strings.TrimSpace(grant.RunID) != strings.TrimSpace(runID)) {
			continue
		}
		if grant.Entry.Access != safety.FileSystemAccessDeny {
			continue
		}
		path, ok := grant.Entry.Path.Resolve(cwd)
		if ok && sandboxPathContains(path, target) {
			return true
		}
	}
	return false
}

func sandboxPathContains(root, target string) bool {
	root = strings.TrimSpace(root)
	target = strings.TrimSpace(target)
	if root == "" || target == "" {
		return false
	}
	_, err := tool.ResolveWithinRoots(target, []string{root})
	return err == nil
}

func (r *Runner) applyMCPToolApprovalPolicy(ctx context.Context, name string, decision safety.Decision) safety.Decision {
	if r == nil || r.tools == nil {
		return decision
	}
	meta, ok := r.tools.ToolMetaByName(name)
	if !ok || !strings.EqualFold(strings.TrimSpace(meta.Category), "mcp") {
		return decision
	}
	if strings.HasPrefix(decision.Reason, "matched_deny_rule") {
		return decision
	}
	// An operator escape already settled this call, and the MCP hints are an
	// approval requirement — there is no approval left to require. The two
	// escapes have to be treated alike here: mcpPermissionPromptAutoApproved
	// below only recognizes full access when it is the *configured* approval
	// policy, so a Full Access preset picked for the session left destructive
	// MCP tools asking under a mode whose whole point is not to ask, and with
	// the session's approval mode set to never that ask is refused outright.
	if decision.IsOperatorOverride() {
		return decision
	}
	mode := appcfg.MCPToolApprovalMode(meta.MCPApprovalMode).Normalized()
	if mode == appcfg.MCPToolApprovalApprove || r.mcpPermissionPromptAutoApproved(ctx) {
		return safety.Decision{Behavior: safety.BehaviorAllow, Mode: decision.Mode, Reason: "mcp_tool_auto_approved"}
	}
	if !mcpToolRequiresApproval(meta.ReadOnlyHint, meta.DestructiveHint, meta.OpenWorldHint, mode) {
		return safety.Decision{Behavior: safety.BehaviorAllow, Mode: decision.Mode, Reason: "mcp_tool_approval_not_required"}
	}
	if decision.Reason == "matched_allow_rule" {
		return decision
	}
	return safety.Decision{Behavior: safety.BehaviorAsk, Mode: decision.Mode, Reason: "mcp_tool_requires_approval"}
}

func (r *Runner) mcpPermissionPromptAutoApproved(ctx context.Context) bool {
	if r == nil {
		return false
	}
	snapshot := r.PermissionSnapshotForSession(tool.ConversationSessionIDFromContext(ctx))
	if snapshot.ApprovalPolicy.Mode != safety.ApprovalNever || r.AppCfg == nil {
		return false
	}
	if safety.ConfigForSnapshot(r.AppCfg, snapshot).DangerFullAccessEnabled() {
		return true
	}
	return r.restrictedProfileHasFullDiskWrite(ctx, snapshot)
}

func (r *Runner) restrictedProfileHasFullDiskWrite(ctx context.Context, snapshot safety.Snapshot) bool {
	if r == nil || r.AppCfg == nil {
		return false
	}
	cfg := safety.ConfigForSnapshot(r.AppCfg, snapshot)
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	if root := strings.TrimSpace(tool.ProjectRootFromContext(ctx)); root != "" {
		cwd = root
	}
	if cwd, err = filepath.Abs(cwd); err != nil {
		return false
	}
	entries := []safety.FileSystemPermissionEntry{{
		Path: safety.FileSystemPermissionPath{
			Type:  safety.FileSystemPermissionPathTypeSpecial,
			Value: &safety.FileSystemSpecialPath{Kind: safety.FileSystemSpecialPathRoot},
		},
		Access: safety.FileSystemAccessRead,
	}}
	if cfg.SandboxMode == appcfg.SandboxModeWorkspaceWrite {
		entries = append(entries, safety.FileSystemPermissionEntry{
			Path: safety.FileSystemPermissionPath{
				Type:  safety.FileSystemPermissionPathTypeSpecial,
				Value: &safety.FileSystemSpecialPath{Kind: safety.FileSystemSpecialPathProjectRoots},
			},
			Access: safety.FileSystemAccessWrite,
		})
		runtimeConfig := safety.ConvertToRuntimeConfig(
			safety.LocalConfigSources(strings.TrimSpace(r.Home), cfg),
			safety.Snapshot{}, cwd, cwd, os.TempDir(), nil,
		)
		for _, path := range runtimeConfig.Filesystem.AllowWrite {
			entries = append(entries, safety.FileSystemPermissionEntry{
				Path: safety.NewFileSystemPermissionPath(path), Access: safety.FileSystemAccessWrite,
			})
		}
	}
	runID := strings.TrimSpace(tool.RunIDFromContext(ctx))
	for _, grant := range snapshot.FileSystemGrants {
		if grant.Scope == safety.GrantScopeTurn && (runID == "" || strings.TrimSpace(grant.RunID) != runID) {
			continue
		}
		entries = append(entries, grant.Entry)
	}
	return fileSystemEntriesHaveFullDiskWrite(entries, cwd)
}

func fileSystemEntriesHaveFullDiskWrite(entries []safety.FileSystemPermissionEntry, cwd string) bool {
	hasRootWrite := false
	for _, entry := range entries {
		if entry.Access == safety.FileSystemAccessWrite && entry.Path.Type == safety.FileSystemPermissionPathTypeSpecial &&
			entry.Path.Value != nil && entry.Path.Value.Kind == safety.FileSystemSpecialPathRoot {
			hasRootWrite = true
			break
		}
	}
	if !hasRootWrite {
		return false
	}
	for _, entry := range entries {
		if entry.Access == safety.FileSystemAccessWrite {
			continue
		}
		switch entry.Path.Type {
		case safety.FileSystemPermissionPathTypeGlobPattern:
			return false
		case safety.FileSystemPermissionPathTypePath:
			if !hasSameTargetWrite(entries, entry.Path, cwd) {
				return false
			}
		case safety.FileSystemPermissionPathTypeSpecial:
			if entry.Path.Value == nil {
				continue
			}
			switch entry.Path.Value.Kind {
			case safety.FileSystemSpecialPathRoot:
				if entry.Access == safety.FileSystemAccessDeny {
					return false
				}
			case safety.FileSystemSpecialPathSlashTmp:
				if runtime.GOOS != "windows" && !hasSameTargetWrite(entries, entry.Path, cwd) {
					return false
				}
			case safety.FileSystemSpecialPathMinimal, safety.FileSystemSpecialPathUnknown:
			default:
				if !hasSameTargetWrite(entries, entry.Path, cwd) {
					return false
				}
			}
		}
	}
	return true
}

func hasSameTargetWrite(entries []safety.FileSystemPermissionEntry, target safety.FileSystemPermissionPath, cwd string) bool {
	for _, candidate := range entries {
		if candidate.Access != safety.FileSystemAccessWrite {
			continue
		}
		if candidate.Path.Key() == target.Key() {
			return true
		}
		candidatePath, candidateOK := candidate.Path.Resolve(cwd)
		targetPath, targetOK := target.Resolve(cwd)
		if candidateOK && targetOK && filepath.Clean(candidatePath) == filepath.Clean(targetPath) {
			stableCandidate := candidate.Path.Type != safety.FileSystemPermissionPathTypeSpecial ||
				(candidate.Path.Value != nil && (candidate.Path.Value.Kind == safety.FileSystemSpecialPathRoot || candidate.Path.Value.Kind == safety.FileSystemSpecialPathSlashTmp))
			stableTarget := target.Type != safety.FileSystemPermissionPathTypeSpecial ||
				(target.Value != nil && (target.Value.Kind == safety.FileSystemSpecialPathRoot || target.Value.Kind == safety.FileSystemSpecialPathSlashTmp))
			if stableCandidate && stableTarget {
				return true
			}
		}
	}
	return false
}

func deniedRequestPermissionsResult(ctx context.Context, next llm.ToolHandler, arguments, reason string) (any, error) {
	if next == nil {
		return nil, nil
	}
	return next(tool.WithRequestPermissionsDenied(ctx, reason), arguments)
}

func toolOwnsApprovalProtocol(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "write_file", "edit_file":
		// File mutations resolve and validate their target before calling the
		// shared action hook. Asking here would bypass that ordering, turning
		// an invalid path into an approval with no target or accept choices.
		return true
	case "user_interaction", "enter_plan_mode", "exit_plan_mode":
		return true
	default:
		return false
	}
}

func (r *Runner) approvedActionBypassesSandbox(ctx context.Context) bool {
	if r == nil || r.Actions == nil {
		return false
	}
	actionID := strings.TrimSpace(tool.ApprovedActionIDFromContext(ctx))
	if actionID == "" {
		return false
	}
	action, err := r.Actions.Get(ctx, actionID)
	if err != nil || action == nil || action.Status != state.ActionApproved {
		return false
	}
	var payload map[string]any
	if json.Unmarshal([]byte(action.PayloadJSON), &payload) != nil {
		return false
	}
	return strings.EqualFold(
		strings.TrimSpace(payloadString(payload, "sandbox_permissions")),
		string(safety.SandboxPermissionsRequireEscalated),
	)
}

func (r *Runner) approvedActionNetworkAccess(ctx context.Context) (tool.ApprovedNetworkAccess, bool) {
	if r == nil || r.Actions == nil {
		return tool.ApprovedNetworkAccess{}, false
	}
	actionID := strings.TrimSpace(tool.ApprovedActionIDFromContext(ctx))
	if actionID == "" {
		return tool.ApprovedNetworkAccess{}, false
	}
	action, err := r.Actions.Get(ctx, actionID)
	if err != nil || action == nil || action.Status != state.ActionApproved {
		return tool.ApprovedNetworkAccess{}, false
	}
	var payload struct {
		Context safety.NetworkApprovalContext `json:"network_approval_context"`
		Port    int                           `json:"network_port"`
	}
	if json.Unmarshal([]byte(action.PayloadJSON), &payload) != nil ||
		strings.TrimSpace(payload.Context.Host) == "" || !payload.Context.Protocol.Valid() ||
		payload.Port < 0 || payload.Port > 65535 {
		return tool.ApprovedNetworkAccess{}, false
	}
	return tool.ApprovedNetworkAccess{Context: payload.Context, Port: payload.Port}, true
}

func toolArgumentsMap(arguments string) map[string]any {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return map[string]any{}
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(arguments), &payload); err != nil || payload == nil {
		return map[string]any{"raw": arguments}
	}
	return payload
}

func approvalPayloadPath(payload map[string]any) string {
	for _, key := range []string{"file_path", "path"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

var errToolRequestPreviouslyDenied = errors.New("this tool request was already denied; change the command or ask to adjust permissions")

func formatPermissionDenyError(decision safety.Decision, kind string, payload map[string]any) error {
	reason := strings.TrimSpace(decision.Reason)
	suggestion := buildDenySuggestionHint(kind, payload)
	if decision.Matched != nil {
		mv := decision.Matched.Value
		src := strings.TrimSpace(string(decision.Matched.Source))
		rule := strings.TrimSpace(safety.FormatRuleString(mv))
		if rule == "" {
			rule = strings.TrimSpace(mv.ToolName)
		}
		if rule != "" {
			detail := rule
			if src != "" {
				detail = rule + " (" + src + ")"
			}
			if reason == "" {
				return fmt.Errorf("permission denied by rule %s%s", detail, suggestion)
			}
			return fmt.Errorf("permission denied (%s) by rule %s%s", reason, detail, suggestion)
		}
	}
	if reason != "" {
		return fmt.Errorf("permission denied (%s)%s", reason, suggestion)
	}
	if suggestion != "" {
		return fmt.Errorf("permission denied%s", suggestion)
	}
	return errToolRequestPreviouslyDenied
}

// buildDenySuggestionHint returns a suffix string with copy-pasteable rule
// suggestions when the permission engine denies a tool call. Empty when no
// suggestion can be produced.
func buildDenySuggestionHint(kind string, payload map[string]any) string {
	if strings.TrimSpace(kind) == "" {
		return ""
	}
	sug := safety.BuildToolApprovalSuggestion(kind, payload)
	exact := strings.TrimSpace(sug.ExactRuleContent)
	prefix := strings.TrimSpace(sug.PrefixRuleContent)
	tool := strings.TrimSpace(sug.PermissionToolName)
	if tool == "" || (exact == "" && prefix == "") {
		return "; change the input or adjust permissions"
	}
	parts := make([]string, 0, 2)
	if prefix != "" {
		parts = append(parts, safety.FormatRuleString(safety.PermissionRuleValue{
			ToolName:    tool,
			RuleContent: prefix,
		}))
	}
	if exact != "" && exact != prefix {
		parts = append(parts, safety.FormatRuleString(safety.PermissionRuleValue{
			ToolName:    tool,
			RuleContent: exact,
		}))
	}
	if len(parts) == 0 {
		return "; change the input or adjust permissions"
	}
	return "; to allow, add an allow rule such as " + strings.Join(parts, " or ")
}

// requestPermissionsApprovalExemptionReason renders a user-facing explanation
// of why a request_permissions call was auto-approved, i.e. exempted from an
// interactive approval prompt. The text names the concrete policy condition
// that matched so transcripts can explain approval-exempt decisions.
func requestPermissionsApprovalExemptionReason(decision safety.Decision) string {
	switch strings.TrimSpace(decision.Reason) {
	case "matched_allow_rule":
		if rule := formatMatchedPermissionRule(decision); rule != "" {
			return "matched allow rule " + rule
		}
		return "matched a configured allow rule"
	default:
		if reason := strings.TrimSpace(decision.Reason); reason != "" {
			return "approved by the permission policy (" + reason + ")"
		}
		return "approved by the permission policy"
	}
}

// requestPermissionsPolicyDenyReason renders a user-facing explanation of why
// a request_permissions call was denied by the permission policy without an
// interactive approval prompt.
func requestPermissionsPolicyDenyReason(decision safety.Decision) string {
	switch strings.TrimSpace(decision.Reason) {
	case "matched_deny_rule", "matched_deny_rule_in_shell_clause":
		if rule := formatMatchedPermissionRule(decision); rule != "" {
			return "matched deny rule " + rule
		}
		return "matched a configured deny rule"
	case "never_policy_default_deny", "never_policy_converts_ask_to_deny":
		return "the approval policy never prompts and no allow rule matched"
	default:
		if reason := strings.TrimSpace(decision.Reason); reason != "" {
			return "denied by the permission policy (" + reason + ")"
		}
		return "denied by the permission policy"
	}
}

func formatMatchedPermissionRule(decision safety.Decision) string {
	if decision.Matched == nil {
		return ""
	}
	rule := strings.TrimSpace(safety.FormatRuleString(decision.Matched.Value))
	if rule == "" {
		return ""
	}
	if src := strings.TrimSpace(string(decision.Matched.Source)); src != "" {
		return "`" + rule + "` (" + src + ")"
	}
	return "`" + rule + "`"
}

func payloadToMap(payload any) map[string]any {
	if m, ok := payload.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{}
	}
	if m == nil {
		return map[string]any{}
	}
	return m
}

func permissionToolName(kind string) string {
	k := strings.TrimSpace(kind)
	if strings.EqualFold(k, "shell") {
		return "Bash"
	}
	return k
}

// permissionToolNameFor maps a tool kind onto the policy name its rules are
// written against, taking the payload into account where one kind answers to
// two policies. The lsp tool is that case: a call that names a file reads that
// file, so the Read rules govern it, while the file-less workspace symbol
// search is the read-only LSP policy itself.
func permissionToolNameFor(kind string, payload map[string]any) string {
	k := strings.TrimSpace(kind)
	if strings.EqualFold(k, "lsp") {
		if p, ok := payload["file_path"].(string); ok && strings.TrimSpace(p) != "" {
			return "Read"
		}
		return "LSP"
	}
	return permissionToolName(k)
}

func permissionInput(kind string, payload map[string]any) string {
	k := strings.TrimSpace(kind)
	switch {
	case strings.EqualFold(safety.CanonicalToolName(k), "Bash"):
		if cmd, ok := payload["command"].(string); ok {
			return strings.TrimSpace(cmd)
		}
	case strings.EqualFold(k, "request_permissions"):
		return "request_permissions"
	case strings.EqualFold(k, "read_file"),
		strings.EqualFold(k, "write_file"),
		strings.EqualFold(k, "edit_file"),
		strings.EqualFold(k, "multi_edit"),
		strings.EqualFold(k, "lsp"):
		if p, ok := payload["file_path"].(string); ok {
			return strings.TrimSpace(p)
		}
	case strings.EqualFold(k, "web_fetch"):
		if u, ok := payload["url"].(string); ok {
			return strings.TrimSpace(u)
		}
	case strings.EqualFold(k, "web_search"):
		if q, ok := payload["query"].(string); ok {
			return strings.TrimSpace(q)
		}
	}
	b, _ := json.Marshal(payload)
	in := strings.TrimSpace(string(b))
	if in == "" || in == "{}" {
		return k
	}
	return in
}

// evaluateToolPermission decides a tool call, and for a shell command also
// considers the rewritten form of that command.
//
// The shell tool rewrites a command before it runs, and the approval payload
// and any remembered rule are both derived from the rewritten form — a
// "don't ask again for `git add`" approval persists as `git --no-pager add`.
// Evaluating only what the model typed can therefore never match the rule the
// user just granted, and they are asked again on every call.
//
// A rule that matched the command as typed always wins: the rewritten form is
// consulted only when no rule matched at all, so a deny or ask rule written
// against the plain command cannot be sidestepped by the tool. An unmatched
// decision is not enough to stop here even when it permits the call: the
// sandbox defaults ("run it, the sandbox contains it") are Allow without a
// matched rule, and returning one of those hides the remembered rule together
// with the sandbox bypass it carries. The command is then sandboxed anyway, the
// sandbox denies it, and the same rule reappears one escalation later — the
// grant applying only after a failed attempt instead of on the first one.
func (r *Runner) evaluateToolPermission(sessionID, kind string, payload map[string]any) safety.Decision {
	toolName := permissionToolNameFor(kind, payload)
	input := permissionInput(kind, payload)
	decision := r.EvaluatePermissionForSession(sessionID, toolName, input)
	if decision.Matched != nil {
		return decision
	}
	rewritten := r.rewrittenShellPermissionInput(kind, input)
	if rewritten == "" || rewritten == input {
		return decision
	}
	// Any matched rule is adopted, not just an allow: the rewritten form is the
	// command that actually runs, so a restriction naming it must apply too.
	if alt := r.EvaluatePermissionForSession(sessionID, toolName, rewritten); alt.Matched != nil {
		return alt
	}
	return decision
}

// rewrittenShellPermissionInput returns the command as the shell tool will
// actually run it, or "" when the input is not a shell command or rewriting is
// off. Rewriting is idempotent, so an already-rewritten command is unchanged.
func (r *Runner) rewrittenShellPermissionInput(kind, input string) string {
	if !isShellPermissionKind(kind) || strings.TrimSpace(input) == "" {
		return ""
	}
	if r != nil && r.AppCfg != nil && !r.AppCfg.Tools.CommandRewrite.UseRewrite() {
		return ""
	}
	result := safety.Rewrite(input)
	if !result.Applied {
		return ""
	}
	return strings.TrimSpace(result.Command)
}

func isShellPermissionKind(kind string) bool {
	return strings.EqualFold(safety.CanonicalToolName(strings.TrimSpace(kind)), "Bash")
}

func payloadForcesToolApproval(payload map[string]any) bool {
	return payloadBool(payload, "force_tool_approval")
}

func payloadBool(payload map[string]any, keys ...string) bool {
	for _, key := range keys {
		switch v := payload[key].(type) {
		case bool:
			if v {
				return true
			}
		case string:
			if strings.EqualFold(strings.TrimSpace(v), "true") {
				return true
			}
		}
	}
	return false
}

// permRuntimeGet returns r's permission runtime, creating it if this is the
// first call on a Runner value built without one (e.g. a test that
// constructs &Runner{...} by hand rather than through the factory). The nil
// check inside Do (rather than unconditionally assigning) lets a Runner
// built with permRuntime already set via struct literal — several
// pkg/run tests do this — keep that value instead of having the first
// permRuntimeGet call silently replace it.
func (r *Runner) permRuntimeGet() *safety.Runtime {
	r.permRuntimeOnce.Do(func() {
		if r.permRuntime == nil {
			r.permRuntime = safety.NewRuntime()
		}
	})
	return r.permRuntime
}

func (r *Runner) permPaths() safety.Paths {
	return safety.Paths{Home: r.Home, WorkspaceRoot: r.workspaceRoot(), ProjectRoot: r.ProjectRoot}
}

// yoloEnabled reports whether YOLO / danger-full-access is on.
//
// It reads the environment rather than a field on the Runner. The Runner used
// to cache safety.RuntimeYOLOEnabled() in a YOLO field, but every production
// writer set that field to exactly that call and this reader then OR'd it with
// the same call, so the copy could never carry information the environment did
// not already have -- an I6 dual owner with only one real owner.
func (r *Runner) yoloEnabled() bool {
	if r == nil {
		return false
	}
	return safety.RuntimeYOLOEnabled()
}

func (r *Runner) PermissionSnapshot() safety.Snapshot {
	return r.PermissionSnapshotForSession("")
}

func (r *Runner) PermissionSnapshotForSession(sessionID string) safety.Snapshot {
	if r == nil {
		return safety.Snapshot{
			Mode:  safety.ModeOnRequest,
			Rules: map[safety.PermissionSource]map[safety.PermissionBehavior][]safety.PermissionRuleValue{},
		}
	}
	return r.permRuntimeGet().SnapshotForSession(sessionID, r.AppCfg)
}

func (r *Runner) EvaluatePermission(toolName, input string) safety.Decision {
	return r.EvaluatePermissionForSession("", toolName, input)
}

func (r *Runner) EvaluatePermissionForSession(sessionID, toolName, input string) safety.Decision {
	if r == nil {
		return safety.Decision{Behavior: safety.BehaviorAsk, Mode: safety.ModeOnRequest, Reason: "nil_runner"}
	}
	return r.permRuntimeGet().Evaluate(sessionID, toolName, input, r.AppCfg, r.yoloEnabled())
}

func (r *Runner) ExplainPermission(toolName, input string) safety.ExplainResult {
	return r.ExplainPermissionForSession("", toolName, input)
}

func (r *Runner) ExplainPermissionForSession(sessionID, toolName, input string) safety.ExplainResult {
	if r == nil {
		return safety.ExplainResult{
			Decision: safety.Decision{
				Behavior: safety.BehaviorAsk,
				Mode:     safety.ModeOnRequest,
				Reason:   "nil_runner",
			},
		}
	}
	return r.permRuntimeGet().Explain(sessionID, toolName, input, r.AppCfg, r.yoloEnabled())
}

// ResetRuntimePermissionGrants drops approvals scoped to the running agent so
// they cannot follow the runner into a different primary agent's workspace.
// Persisted settings rules survive and are reloaded by the next Load.
func (r *Runner) ResetRuntimePermissionGrants() {
	if r == nil {
		return
	}
	r.permRuntimeGet().ResetGrants(r.AppCfg)
}

func (r *Runner) ApplyPermissionUpdate(update safety.PermissionUpdate) {
	if r == nil {
		return
	}
	r.permRuntimeGet().ApplyUpdate(update, r.AppCfg, r.permPaths())
	r.RefreshFilesystemPolicy()
}

// RefreshSandboxAvailability re-derives whether a sandbox will contain shell
// commands, for callers that changed the in-memory config's sandbox mode
// without going through Load. Without it the store keeps answering from the
// mode that was live when it was last loaded, and the engine decides whether an
// unmatched command may run unattended on a stale premise.
func (r *Runner) RefreshSandboxAvailability() {
	if r == nil {
		return
	}
	r.permRuntimeGet().RefreshSandboxAvailability(r.AppCfg)
}

// RefreshFilesystemPolicy re-resolves active filesystem capabilities and
// restrictions for in-process tools. No-op until tools exist.
func (r *Runner) RefreshFilesystemPolicy() {
	if r == nil {
		return
	}
	r.mu.load.RLock()
	st := r.tools
	home := r.Home
	cwd := r.ProjectRoot
	cfg := r.AppCfg
	r.mu.load.RUnlock()
	if st == nil || cfg == nil {
		return
	}
	tool.ApplyFilesystemPolicy(st, home, cwd, cfg, r.PermissionSnapshot())
}
