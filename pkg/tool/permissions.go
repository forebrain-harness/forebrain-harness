// Tool permission surface: path approval, filesystem policy, updates, and mode policy.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

// writePathResolution is the write-side counterpart of readPathResolution: the
// file a write will actually land on, the skill catalog repair that produced it
// when the requested path was a shortened catalog path, and what the approval
// prompt has to say about the target.
type writePathResolution struct {
	Abs string
	// PlanWrite marks the session plan file, the one write that is neither
	// protected nor gated by the approval hook.
	PlanWrite bool
	// Protected is the sentence the approval shows, empty for an ordinary write.
	Protected string
	// RepairedFrom is the path the model asked for, set only when that path is
	// not there and resolved into a loaded skill instead. The write tools report
	// it, because a repair the model is not told about leaves it believing it
	// changed a file that does not exist.
	RepairedFrom string
	Skill        LoadedSkill
}

// resolveGuardedWritePath settles both questions a write has to answer before
// anything is written: where it lands, and whether the user has to be asked
// about it first.
//
// The protected reason it returns is not a refusal. A write to agent metadata,
// FOREBRAIN_HOME, a git execution surface or a loaded skill's own files used to fail
// here with an error the user could do nothing about; the path is sensitive
// enough to stop for, but stopping means raising the approval overlay so the
// user can say yes to the change they asked for.
func resolveGuardedWritePath(ctx context.Context, st *State, requested string) (writePathResolution, error) {
	if st == nil {
		return writePathResolution{}, errors.New("nil state")
	}
	// The plan region is settled first and never protected: plan files live
	// under FOREBRAIN_HOME, so every check below would otherwise stop a plan write.
	if planPath, ok := planFileWriteTarget(ctx, requested); ok {
		return writePathResolution{Abs: planPath, PlanWrite: true}, nil
	}
	var out writePathResolution
	// A path addressing a loaded skill is settled by the catalog rather than by
	// root expansion. The catalog is where that skill really lives, so a
	// shortened catalog path is repaired into it instead of resolving against
	// the workspace roots into a stray file beside it.
	if target, item, ok := st.LoadedSkillWriteTarget(requested); ok {
		out.Abs = target
		out.Protected = loadedSkillWriteReason(target, item)
		if normalizeRootPath(requested) != target {
			out.RepairedFrom = strings.TrimSpace(requested)
			out.Skill = item
		}
	} else {
		resolved, resolveErr := resolveWriteFilePath(ctx, st, requested)
		if resolveErr != nil {
			return writePathResolution{}, resolveErr
		}
		out.Abs = resolved
	}
	if err := st.GuardWrite(ctx, out.Abs); err != nil {
		return writePathResolution{}, err
	}
	if out.Protected == "" {
		out.Protected = st.ProtectedWriteReason(ctx, out.Abs)
	}
	return out, nil
}

// skillWritePathRepairNote is skillPathRepairNote for a write: it tells the
// model which file it actually changed, for the same reason the read does.
func skillWritePathRepairNote(res writePathResolution) string {
	if res.RepairedFrom == "" {
		return ""
	}
	return fmt.Sprintf("note: %s does not exist, so the %q skill's file at %s was written instead — that skill's files all live under %s.",
		res.RepairedFrom, res.Skill.Name, res.Abs, res.Skill.RootDir)
}

// loadedSkillWriteReason is the sentence a write into a loaded skill has to
// show the user: the skill catalog is where this session reads its own
// instructions, so a change to it is a change to what the model is told next.
func loadedSkillWriteReason(target string, item LoadedSkill) string {
	return fmt.Sprintf("Writing %s changes the %q skill, whose files this session loads its instructions from.", target, item.Name)
}

// markProtectedApproval marks an approval payload as one the user has to answer
// even where a rule or an earlier grant would have let the call through unasked.
// The target being protected is the fact the prompt exists to surface, so it
// cannot be the thing a prior "allow" silently covers.
func markProtectedApproval(payload map[string]any, reason string) {
	if payload == nil || strings.TrimSpace(reason) == "" {
		return
	}
	payload["force_tool_approval"] = true
	payload["approval_reason"] = protectedPathApprovalReason
	payload["justification"] = strings.TrimSpace(reason)
}

// appendProtectedReason collects the distinct sentences a multi-file patch has
// to show, so a patch touching two protected paths explains both rather than
// only whichever operation happened to be parsed last.
func appendProtectedReason(reasons []string, reason string) []string {
	reason = strings.TrimSpace(reason)
	if reason == "" || slices.Contains(reasons, reason) {
		return reasons
	}
	return append(reasons, reason)
}

// protectedPathApprovalReason tags the approvals raised by a protected write so
// every surface can label them as what they are rather than as an ordinary
// write.
const protectedPathApprovalReason = "protected_path"

// markPlanModeApproval marks an approval payload as one plan mode raised because
// the command's effect could not be proven read-only. Such a command is neither
// safe to run unasked nor right to refuse: the user decides. The marker is what
// makes that decision reachable under an approval policy that would otherwise
// wave the call through, and the sentence is what tells the user what they are
// being asked about.
//
// It fills in only what the payload does not already say. A loaded skill names
// the more specific reason, a sandbox denial names its own, and a justification
// the model wrote for an escalation explains what it wants done - replacing any
// of those would leave the user knowing less than before. What it always adds is
// the one thing none of those can say for themselves: that plan mode is why this
// command is being put to them.
func markPlanModeApproval(payload map[string]any) {
	if payload == nil {
		return
	}
	if forced, ok := payload["force_tool_approval"].(bool); !ok || !forced {
		payload["force_tool_approval"] = true
		payload["approval_reason"] = planModeApprovalReason
	}
	if _, exists := payload["justification"]; !exists {
		payload["justification"] = planModeApprovalJustification
	}
}

// planModeApprovalReason tags the approvals the plan-mode shell gate raises, so
// telemetry and every surface can tell them from an ordinary shell prompt.
const planModeApprovalReason = "plan_mode_unproven_command"

// planModeApprovalJustification is the one sentence the approval has to show. It
// names the question - is this command allowed to run while planning? - because
// the command itself is already on the overlay's Command line above it.
const planModeApprovalJustification = "Plan mode is active: this command could not be proven read-only, so it will run only if you allow it."

// resolveWriteFilePath resolves a path the file tools are about to write.
//
// The file tools carry no path boundary of their own. Which paths may be
// written is settled by the sandbox policy and the approval layer — the same
// decision apply_patch and shell already go through — so a path outside the
// workspace roots is asked about rather than refused outright. Refusing here
// was both weaker and worse than asking: the model reaches the very same file
// through apply_patch after a single approval, so the refusal never protected
// the file, it only pushed the change into an opaque patch the operator has to
// review instead of a named edit with its old and new text.
//
// Two refusals stay hard, because no approval can repair them: a deny entry
// from request_permissions, and another primary agent's workspace. FOREBRAIN_HOME
// used to be a third, and is no longer: it is asked about instead, by the write
// tools through ProtectedWriteReason and by read_file through
// ProtectedReadReason — and once a write to a file has been approved, the read
// that write owes is not asked about again. Everything else is a question for
// the operator, for a subagent's call as much as the primary agent's.
func resolveWriteFilePath(ctx context.Context, st *State, filePath string) (string, error) {
	return resolveFileToolPath(ctx, st, filePath, safety.FileSystemAccessWrite)
}

// readPathResolution is the outcome of resolving a read_file path: the file
// that will actually be read, plus the skill catalog repair that produced it
// when the requested path was a shortened catalog path.
type readPathResolution struct {
	Abs string
	// RepairedFrom is the path the model asked for, set only when that path
	// does not exist and resolved into a loaded skill's root instead. The read
	// tool reports it, because a repair the model is not told about leaves it
	// believing a path that is not there.
	RepairedFrom string
	Skill        LoadedSkill
}

// resolveReadFilePath treats loaded skill roots as catalog-backed read
// capabilities. They intentionally do not join State.AllowedRoots because those
// roots also confer write/sandbox bindings.
func resolveReadFilePath(ctx context.Context, st *State, filePath string) (readPathResolution, error) {
	if st != nil {
		if abs, ok := st.ResolveLoadedSkillRead(filePath); ok {
			return readPathResolution{Abs: abs}, nil
		}
		if abs, item, ok := st.resolveShortenedSkillPath(filePath); ok {
			return readPathResolution{Abs: abs, RepairedFrom: strings.TrimSpace(filePath), Skill: item}, nil
		}
	}
	abs, err := resolveFileToolPath(ctx, st, filePath, safety.FileSystemAccessRead)
	if err != nil {
		return readPathResolution{}, err
	}
	return readPathResolution{Abs: abs}, nil
}

func resolveFileToolPath(ctx context.Context, st *State, requestedPath string, access safety.FileSystemPermissionAccess) (string, error) {
	if st == nil {
		return "", errors.New("nil state")
	}
	trimmed := strings.TrimSpace(requestedPath)
	if trimmed == "" {
		return "", errors.New("file_path is required and must not be blank. Retry this tool call with file_path set to the target file's absolute or workspace-relative path, keeping the other arguments unchanged")
	}
	// A confined State owns a folder, not a workspace: it settles the path on
	// its own and never reaches the approval fallback below.
	if confined := st.ConfinedRoot(); confined != "" {
		return resolveWithinConfinedRoot(confined, requestedPath)
	}
	absCandidate, absErr := filepath.Abs(trimmed)
	if absErr != nil {
		return "", absErr
	}
	absCandidate = filepath.Clean(absCandidate)
	sessionID := ConversationSessionIDFromContext(ctx)
	if st.PathDeniedByGrant(absCandidate, sessionID, RunIDFromContext(ctx)) {
		return "", fmt.Errorf("%w: denied by request_permissions grant: %s", ErrPathNotAllowed, absCandidate)
	}
	resolutionRoots := MergeAllowedRootPaths(st.AllowedRoots(), st.PermissionRoots(access))
	abs, err := ResolveWithinRoots(requestedPath, resolutionRoots)
	if err == nil {
		return abs, nil
	}
	if !errors.Is(err, ErrPathNotAllowed) {
		return "", err
	}
	if st.PathOverlapsSiblingPrimaryWorkspace(absCandidate) {
		return "", fmt.Errorf("%w: primary agent workspace isolation blocks %s", err, trimmed)
	}
	if granted, ok := resolveWithinGrantedRoots(ctx, st, requestedPath, access); ok {
		return granted, nil
	}
	// Nobody can be asked: an embedder that wired the file tools without an
	// approval hook has no way to authorize the write, so refuse it instead of
	// performing it unattended. apply_patch draws the same line. A subagent is
	// not such a case — a child run suspends on the same action queue as the
	// primary run, and its approval reaches the user through the same overlay.
	if access == safety.FileSystemAccessWrite && st.ActionHook() == nil &&
		ApprovedActionIDFromContext(ctx) == "" && !PolicyApprovedFromContext(ctx) && !SandboxBypassApprovedFromContext(ctx) {
		return "", fmt.Errorf("%w: %s", err, trimmed)
	}
	// Outside the roots, and none of the isolation boundaries apply: hand the
	// path back and let the sandbox policy and the approval layer decide.
	// Resolving against the volume root keeps pathguard's symlink and
	// nearest-existing-parent checks in place, exactly as apply_patch does.
	volumeRoot := string(filepath.Separator)
	if volume := filepath.VolumeName(absCandidate); volume != "" {
		volumeRoot = volume + string(filepath.Separator)
	}
	return ResolveWithinRoots(absCandidate, []string{volumeRoot})
}

// resolveWithinConfinedRoot resolves a file-tool path for a State bound to a
// single directory by State.ConfineToRoot. A relative path is joined to that
// directory — the process working directory has no say, because a confined
// agent is not running in it — and an absolute path has to already live inside
// it. Anything else is a hard refusal: there is no operator to ask, so the only
// alternative to refusing is writing somewhere the caller never named.
//
// Resolution still goes through pathguard so the symlink and
// nearest-existing-parent checks apply, exactly as they do for workspace roots.
func resolveWithinConfinedRoot(root, requestedPath string) (string, error) {
	trimmed := strings.TrimSpace(requestedPath)
	if trimmed == "" {
		return "", errors.New("empty path")
	}
	candidate := trimmed
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	abs, err := ResolveWithinRoots(candidate, []string{root})
	if err != nil {
		if errors.Is(err, ErrPathNotAllowed) {
			return "", fmt.Errorf("%w: %s resolves outside the agent's root %s", err, trimmed, root)
		}
		return "", err
	}
	return abs, nil
}

// resolveWithinGrantedRoots resolves requestedPath against the request_permissions
// grants alone. A turn-scoped grant never reaches State.PermissionRoots — the
// sandbox configuration deliberately drops grants bound to a single run — so a
// grant the caller was just handed has to be consulted here or it does nothing.
func resolveWithinGrantedRoots(ctx context.Context, st *State, requestedPath string, access safety.FileSystemPermissionAccess) (string, bool) {
	sessionID := ConversationSessionIDFromContext(ctx)
	runID := RunIDFromContext(ctx)
	abs, err := filepath.Abs(strings.TrimSpace(requestedPath))
	if err != nil {
		return "", false
	}
	if !st.PathGranted(filepath.Clean(abs), access, sessionID, runID) {
		return "", false
	}
	// Reuse pathguard's realpath/nearest-parent checks by treating the
	// normalized grant root as the sole root for this resolution.
	for _, grant := range st.permissionSnapshotForSession(sessionID).FileSystemGrants {
		if !permissionAccessAllows(grant.Entry.Access, access) || !grantAppliesToRun(grant, runID) {
			continue
		}
		root, ok := grant.Entry.Path.Resolve(requestPermissionsCWD(ctx))
		if !ok {
			continue
		}
		if granted, grantErr := ResolveWithinRoots(requestedPath, []string{root}); grantErr == nil {
			return granted, true
		}
	}
	return "", false
}

// planFileWriteTarget reports whether filePath targets a file inside the
// session's writable plan directory while plan mode is active.
func planFileWriteTarget(ctx context.Context, filePath string) (string, bool) {
	if strings.ToLower(strings.TrimSpace(ModeFromContext(ctx))) != "plan" {
		return "", false
	}
	p := strings.TrimSpace(filePath)
	if p == "" {
		return "", false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	abs = filepath.Clean(abs)
	// One predicate decides what the plan region is, shared with GuardWrite.
	// When the two disagree, a path GuardWrite would allow still fails earlier
	// in root resolution, or prompts for approval it should not need.
	if !isSanctionedPlanWrite(ctx, abs) {
		return "", false
	}
	return abs, true
}

func permissionSnapshotForRun(snap safety.Snapshot, runID string) safety.Snapshot {
	filtered := make([]safety.FileSystemPermissionGrant, 0, len(snap.FileSystemGrants))
	for _, grant := range snap.FileSystemGrants {
		if !grantAppliesToRun(grant, runID) {
			continue
		}
		grant.RunID = ""
		filtered = append(filtered, grant)
	}
	snap.FileSystemGrants = filtered
	network := make([]safety.NetworkPermissionGrant, 0, len(snap.NetworkGrants))
	for _, grant := range snap.NetworkGrants {
		if !networkGrantAppliesToRun(grant, runID) {
			continue
		}
		grant.RunID = ""
		network = append(network, grant)
	}
	snap.NetworkGrants = network
	return snap
}

func (s *State) permissionSnapshotForSession(sessionID string) safety.Snapshot {
	if s == nil {
		return safety.Snapshot{}
	}
	if value := s.RuntimeValue("permission_snapshot_for_session"); value != nil {
		if fn, ok := value.(func(string) safety.Snapshot); ok && fn != nil {
			return fn(strings.TrimSpace(sessionID))
		}
	}
	value := s.RuntimeValue("permission_snapshot")
	fn, _ := value.(func() safety.Snapshot)
	if fn == nil {
		return safety.Snapshot{}
	}
	return fn()
}

func permissionSnapshotForContext(ctx context.Context, st *State, rt *AgentToolRuntime) safety.Snapshot {
	sessionID := ConversationSessionIDFromContext(ctx)
	if rt != nil && rt.PermissionSnapshotForSession != nil {
		return rt.PermissionSnapshotForSession(sessionID)
	}
	if rt != nil && rt.PermissionSnapshot != nil {
		return rt.PermissionSnapshot()
	}
	if st != nil {
		return st.permissionSnapshotForSession(sessionID)
	}
	return safety.Snapshot{}
}

// PathGranted reports whether request_permissions grants permit access to path
// for the current run. Explicit deny entries take precedence over allows.
func (s *State) PathGranted(path string, access safety.FileSystemPermissionAccess, sessionID, runID string) bool {
	path = normalizeRootPath(path)
	if path == "" {
		return false
	}
	runID = strings.TrimSpace(runID)
	grants := s.permissionSnapshotForSession(sessionID).FileSystemGrants
	allowed := false
	cwd, _ := os.Getwd()
	for _, grant := range grants {
		if !grantAppliesToRun(grant, runID) || !grant.Entry.Path.Matches(path, cwd) {
			continue
		}
		if grant.Entry.Access == safety.FileSystemAccessDeny {
			return false
		}
		if permissionAccessAllows(grant.Entry.Access, access) {
			allowed = true
		}
	}
	return allowed
}

func permissionAccessAllows(granted, requested safety.FileSystemPermissionAccess) bool {
	if granted == requested {
		return true
	}
	return granted == safety.FileSystemAccessWrite && requested == safety.FileSystemAccessRead
}

func (s *State) PathDeniedByGrant(path, sessionID, runID string) bool {
	path = normalizeRootPath(path)
	if path == "" {
		return false
	}
	for _, grant := range s.permissionSnapshotForSession(sessionID).FileSystemGrants {
		if grant.Entry.Access != safety.FileSystemAccessDeny || !grantAppliesToRun(grant, runID) {
			continue
		}
		cwd, _ := os.Getwd()
		if grant.Entry.Path.Matches(path, cwd) {
			return true
		}
	}
	return false
}

func grantAppliesToRun(grant safety.FileSystemPermissionGrant, runID string) bool {
	switch grant.Scope {
	case safety.GrantScopeSession:
		return true
	case safety.GrantScopeTurn, "":
		return strings.TrimSpace(grant.RunID) == strings.TrimSpace(runID)
	default:
		return false
	}
}

func networkGrantAppliesToRun(grant safety.NetworkPermissionGrant, runID string) bool {
	if !grant.Enabled {
		return false
	}
	switch grant.Scope {
	case safety.GrantScopeSession:
		return true
	case safety.GrantScopeTurn, "":
		return strings.TrimSpace(grant.RunID) == strings.TrimSpace(runID)
	default:
		return false
	}
}

// ValidateApprovalUpdateForAction confines a remembered approval to the
// capability represented by the pending action. The returned update is
// normalized and carries the action's session identifier when session-scoped.
func ValidateApprovalUpdateForAction(kind, payloadJSON, sessionID string, update safety.PermissionUpdate) (safety.PermissionUpdate, error) {
	kind = strings.TrimSpace(kind)
	sessionID = strings.TrimSpace(sessionID)
	var payload map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(payloadJSON)), &payload) != nil {
		return safety.PermissionUpdate{}, fmt.Errorf("invalid approval action payload")
	}
	if update.Type != safety.UpdateAddRules || update.Behavior != safety.BehaviorAllow {
		return safety.PermissionUpdate{}, fmt.Errorf("unsupported approval update")
	}
	if !ActionAllowsApprovalUpdate(kind, payloadJSON) {
		return safety.PermissionUpdate{}, fmt.Errorf("remembered approval is disabled for %q", kind)
	}

	suggestion := safety.BuildToolApprovalSuggestion(kind, payload)
	if suggestion.OneShotOnly || strings.TrimSpace(suggestion.PermissionToolName) == "" {
		return safety.PermissionUpdate{}, fmt.Errorf("this approval can only be granted once")
	}

	if strings.EqualFold(kind, "apply_patch") {
		return validatePatchSessionUpdate(payload, sessionID, update)
	}

	if update.Destination == safety.DestinationSession {
		if sessionID == "" {
			return safety.PermissionUpdate{}, fmt.Errorf("session approval is missing a session id")
		}
		if !strings.HasPrefix(strings.ToLower(suggestion.PermissionToolName), "mcp__") && !safety.ApprovalFileTool(suggestion.PermissionToolName) {
			return safety.PermissionUpdate{}, fmt.Errorf("session approval is not available for %q", kind)
		}
		return validateSingleSuggestedRule(suggestion, sessionID, update, suggestion.ExactRuleContent, false)
	}
	if update.Destination != safety.DestinationLocalSettings {
		return safety.PermissionUpdate{}, fmt.Errorf("persistent approvals use local settings")
	}
	// A command persists the server-proposed prefix amendment, or — when no
	// prefix could be derived from it — the exact command. Both carry sandbox
	// bypass: a remembered command may start outside the sandbox on a later
	// call, which is the reason it was remembered.
	if strings.EqualFold(safety.CanonicalToolName(suggestion.PermissionToolName), "Bash") {
		if len(safety.ProposedCommandPrefixAmendment(suggestion)) > 0 {
			return validateCommandPrefixRule(suggestion, update)
		}
		return validateCommandRuleSet(suggestion, update)
	}
	ruleContent := strings.TrimSpace(suggestion.PrefixRuleContent)
	if strings.HasPrefix(strings.ToLower(suggestion.PermissionToolName), "mcp__") {
		ruleContent = strings.TrimSpace(suggestion.ExactRuleContent)
	}
	if ruleContent == "" {
		return safety.PermissionUpdate{}, fmt.Errorf("no persistent approval amendment was proposed")
	}
	return validateSingleSuggestedRule(suggestion, "", update, ruleContent, false)
}

// validateCommandRuleSet accepts exactly the rule set the server proposed for
// this command and nothing else. The submitted rules are discarded rather than
// merged: what a surface sends is a choice among the proposals, never a rule
// of its own, so the stored grant is always one the server derived.
func validateCommandRuleSet(suggestion safety.ToolApprovalSuggestion, update safety.PermissionUpdate) (safety.PermissionUpdate, error) {
	want, ok := safety.ProposedCommandApprovalRules(suggestion)
	if !ok {
		return safety.PermissionUpdate{}, fmt.Errorf("no persistent approval amendment was proposed")
	}
	if len(update.Rules) != len(want) {
		return safety.PermissionUpdate{}, fmt.Errorf("approval update does not match the proposed command rules")
	}
	for i, rule := range update.Rules {
		if !strings.EqualFold(strings.TrimSpace(rule.ToolName), strings.TrimSpace(suggestion.PermissionToolName)) ||
			strings.TrimSpace(rule.RuleContent) != want[i].RuleContent ||
			strings.TrimSpace(rule.Command) != want[i].Command ||
			!slices.Equal(rule.CommandPrefix, want[i].CommandPrefix) {
			return safety.PermissionUpdate{}, fmt.Errorf("approval update does not match the proposed command rules")
		}
		if !rule.BypassSandbox {
			return safety.PermissionUpdate{}, fmt.Errorf("approval update has invalid sandbox capability")
		}
	}
	return safety.PermissionUpdate{
		Type: update.Type, Destination: safety.DestinationLocalSettings,
		Behavior: safety.BehaviorAllow,
		Rules:    append([]safety.PermissionRuleValue(nil), want...),
	}, nil
}

func validateCommandPrefixRule(suggestion safety.ToolApprovalSuggestion, update safety.PermissionUpdate) (safety.PermissionUpdate, error) {
	if len(update.Rules) != 1 {
		return safety.PermissionUpdate{}, fmt.Errorf("approval update must contain exactly one rule")
	}
	want := safety.ProposedCommandPrefixAmendment(suggestion)
	rule := update.Rules[0]
	if !strings.EqualFold(strings.TrimSpace(rule.ToolName), strings.TrimSpace(suggestion.PermissionToolName)) ||
		strings.TrimSpace(rule.RuleContent) != "" || !slices.Equal(rule.CommandPrefix, want) {
		return safety.PermissionUpdate{}, fmt.Errorf("approval update does not match the proposed command prefix")
	}
	if !rule.BypassSandbox {
		return safety.PermissionUpdate{}, fmt.Errorf("approval update has invalid sandbox capability")
	}
	return safety.PermissionUpdate{
		Type: update.Type, Destination: safety.DestinationLocalSettings,
		Behavior: safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{{
			ToolName: suggestion.PermissionToolName, CommandPrefix: append([]string(nil), want...), BypassSandbox: true,
		}},
	}, nil
}

func validatePatchSessionUpdate(payload map[string]any, sessionID string, update safety.PermissionUpdate) (safety.PermissionUpdate, error) {
	if update.Destination != safety.DestinationSession || sessionID == "" {
		return safety.PermissionUpdate{}, fmt.Errorf("file-change memory is session-scoped")
	}
	paths := approvalResolvedPaths(payload)
	if len(paths) == 0 || len(update.Rules) != len(paths) {
		return safety.PermissionUpdate{}, fmt.Errorf("file-change approval must cover exactly the requested files")
	}
	want := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		want[path] = struct{}{}
	}
	rules := make([]safety.PermissionRuleValue, 0, len(paths))
	for _, rule := range update.Rules {
		path := strings.TrimSpace(rule.RuleContent)
		if !strings.EqualFold(strings.TrimSpace(rule.ToolName), "apply_patch") || !rule.BypassSandbox {
			return safety.PermissionUpdate{}, fmt.Errorf("invalid file-change approval rule")
		}
		if _, ok := want[path]; !ok {
			return safety.PermissionUpdate{}, fmt.Errorf("file-change approval contains an unrequested path")
		}
		delete(want, path)
		rules = append(rules, safety.PermissionRuleValue{ToolName: "apply_patch", RuleContent: path, BypassSandbox: true})
	}
	if len(want) != 0 {
		return safety.PermissionUpdate{}, fmt.Errorf("file-change approval omitted a requested path")
	}
	return safety.PermissionUpdate{
		Type: safety.UpdateAddRules, Destination: safety.DestinationSession, SessionID: sessionID,
		Behavior: safety.BehaviorAllow, Rules: rules,
	}, nil
}

func validateSingleSuggestedRule(suggestion safety.ToolApprovalSuggestion, sessionID string, update safety.PermissionUpdate, content string, bypassSandbox bool) (safety.PermissionUpdate, error) {
	if len(update.Rules) != 1 {
		return safety.PermissionUpdate{}, fmt.Errorf("approval update must contain exactly one rule")
	}
	rule := update.Rules[0]
	if !strings.EqualFold(strings.TrimSpace(rule.ToolName), strings.TrimSpace(suggestion.PermissionToolName)) || strings.TrimSpace(rule.RuleContent) != strings.TrimSpace(content) {
		return safety.PermissionUpdate{}, fmt.Errorf("approval update does not match the proposed capability")
	}
	if rule.BypassSandbox != bypassSandbox {
		return safety.PermissionUpdate{}, fmt.Errorf("approval update has invalid sandbox capability")
	}
	return safety.PermissionUpdate{
		Type: update.Type, Destination: update.Destination, SessionID: sessionID,
		Behavior: safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{{
			ToolName: suggestion.PermissionToolName, RuleContent: strings.TrimSpace(content), BypassSandbox: bypassSandbox,
		}},
	}, nil
}

func approvalResolvedPaths(payload map[string]any) []string {
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
	seen := map[string]struct{}{}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

// planModeBlockedTools are blocked by NAME in plan mode. The file
// create/modify tools (write_file/edit_file) are intentionally NOT
// here: the plan-mode write policy is path-dependent ("only the plan file is
// writable"), which a name blocklist cannot express. They are gated by
// GuardWrite instead, which permits exactly the plan file and blocks every
// other path.
// planModeBlockedTools lists tools blocked by name in plan mode.
// Plan mode no longer blocks any tools by name — all tools available in agent
// mode are also available in plan mode. The only restriction is that write_file
// and edit_file are gated by GuardWrite, which permits only the plan file path.
var planModeBlockedTools = map[string]struct{}{}

// typedSubagentDisallowedTools lists the tools each typed subagent may not
// call. An entry here is a capability its type does not have at all, so the
// call could only ever be refused and is better never offered.
//
// request_permissions is deliberately absent from every entry. It is how an
// agent asks the operator for access it does not have, and denying a subagent
// the question is not a restriction on what it can reach — it only removes the
// operator from the loop, leaving the worker to give up on the path it was sent
// to read. A read-only type still cannot obtain write access through it:
// normalizeRequestPermissionsInput refuses a write entry from a type that
// TypedSubagentBlocksFileWrites reports on.
var typedSubagentDisallowedTools = map[string]map[string]struct{}{
	"explore": {
		"write_file": {}, "edit_file": {},
		"subagent_run": {}, "subagent_fanout": {}, "subagent_send": {}, "subagent_continue": {}, "subagent_close": {}, "subagent_wait": {}, "subagent_status": {}, "subagent_list": {},
	},
	"plan": {
		"write_file": {}, "edit_file": {},
		"subagent_run": {}, "subagent_fanout": {}, "subagent_send": {}, "subagent_continue": {}, "subagent_close": {}, "subagent_wait": {}, "subagent_status": {}, "subagent_list": {},
	},
	// plan-reviewer sees exactly what a plan subagent sees, because it has to
	// do the same investigation: read the code the plan touches and form its own
	// view of the task before it can say where the plan is wrong. The two
	// plan-mode tools are the one difference — the reviewer runs while an
	// exit_plan_mode approval is pending on the very plan it is reviewing, so
	// letting it enter or leave plan mode would let a reviewer decide the
	// question it was called to advise on.
	"plan-reviewer": {
		"write_file": {}, "edit_file": {},
		"enter_plan_mode": {}, "exit_plan_mode": {},
		"subagent_run": {}, "subagent_fanout": {}, "subagent_send": {}, "subagent_continue": {}, "subagent_close": {}, "subagent_wait": {}, "subagent_status": {}, "subagent_list": {},
	},
	// goal-evaluator checks the workspace against a /goal objective, so it may
	// read and run whatever a plan reviewer may, and nothing that changes the
	// workspace, the mode, or starts agents of its own.
	"goal-evaluator": {
		"write_file": {}, "edit_file": {},
		"enter_plan_mode": {}, "exit_plan_mode": {},
		"subagent_run": {}, "subagent_fanout": {}, "subagent_send": {}, "subagent_continue": {}, "subagent_close": {}, "subagent_wait": {}, "subagent_status": {}, "subagent_list": {},
	},
	"verification": {
		"write_file": {}, "edit_file": {},
		"subagent_run": {}, "subagent_fanout": {}, "subagent_send": {}, "subagent_continue": {}, "subagent_close": {}, "subagent_wait": {}, "subagent_status": {}, "subagent_list": {},
	},
	"cavecrew-investigator": {
		"write_file": {}, "edit_file": {},
		"subagent_run": {}, "subagent_fanout": {}, "subagent_send": {}, "subagent_continue": {}, "subagent_close": {}, "subagent_wait": {}, "subagent_status": {}, "subagent_list": {},
	},
	"cavecrew-builder": {
		"subagent_run": {}, "subagent_fanout": {}, "subagent_send": {}, "subagent_continue": {}, "subagent_close": {}, "subagent_wait": {}, "subagent_status": {}, "subagent_list": {},
	},
	"cavecrew-reviewer": {
		"write_file": {}, "edit_file": {},
		"subagent_run": {}, "subagent_fanout": {}, "subagent_send": {}, "subagent_continue": {}, "subagent_close": {}, "subagent_wait": {}, "subagent_status": {}, "subagent_list": {},
	},
}

// TypedSubagentBlocksFileWrites reports whether a typed subagent's own policy
// already forbids both file-writing tools. Plan-mode inheritance keys off this:
// a worker that cannot write files at all is safe to run in agent mode, while a
// write-capable type (general-purpose, or any type without an entry here) must
// inherit the parent's plan mode, or spawning one becomes a way to edit the
// codebase while the session is still planning.
func TypedSubagentBlocksFileWrites(subagentType string) bool {
	blocked, ok := typedSubagentDisallowedTools[strings.ToLower(strings.TrimSpace(subagentType))]
	if !ok {
		return false
	}
	_, write := blocked["write_file"]
	_, edit := blocked["edit_file"]
	return write && edit
}

func (s *State) GuardTool(ctx context.Context, toolName string) error {
	if s == nil {
		return nil
	}
	name := strings.TrimSpace(toolName)
	if name == "" {
		return nil
	}
	st := strings.ToLower(strings.TrimSpace(SubagentTypeFromContext(ctx)))
	if st != "" {
		if blockedByType, ok := typedSubagentDisallowedTools[st]; ok {
			if _, deny := blockedByType[name]; deny {
				return fmt.Errorf("subagent_type %q: tool %q blocked by policy", st, name)
			}
		}
	}
	mode := strings.ToLower(strings.TrimSpace(ModeFromContext(ctx)))
	if mode == "" || mode == "agent" {
		return nil
	}
	if mode == "plan" {
		if _, blocked := planModeBlockedTools[name]; blocked {
			return fmt.Errorf("mode plan: tool %q blocked (planning phase)", name)
		}
		// All tools available in agent mode are also available in plan mode.
		// write_file/edit_file are gated by GuardWrite, which permits only
		// the plan file path and blocks every other write.
		return nil
	}
	if m, ok := s.ToolMetaByName(name); ok && (m.Destructive || !m.ReadOnly) {
		return fmt.Errorf("mode %s: tool %q blocked until agent mode", mode, name)
	}
	return nil
}

func (s *State) ToolMetaByName(name string) (event.ToolMeta, bool) {
	if s == nil {
		return event.ToolMeta{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.toolMetas[strings.TrimSpace(name)]
	return m, ok
}

func GuardToolNameOnly(ctx context.Context, toolName string) error {
	mode := strings.ToLower(strings.TrimSpace(ModeFromContext(ctx)))
	name := strings.TrimSpace(toolName)
	if mode == "" || mode == "agent" {
		return nil
	}
	if name == "" {
		return nil
	}
	if mode == "plan" {
		if _, blocked := planModeBlockedTools[name]; blocked {
			return fmt.Errorf("mode plan: tool %q blocked (planning phase)", name)
		}
		// Plan mode allows all tools; GuardWrite gates write_file/edit_file.
		return nil
	}
	if mode != "" && mode != "agent" {
		return fmt.Errorf("mode %s: tool %q blocked until agent mode", mode, name)
	}
	return nil
}

// PrependAllowedRoot inserts an internal baseline root at the front of the
// allowed-roots list. It is used only during runtime initialization when the
// launch working directory becomes known.
func (s *State) PrependAllowedRoot(abs string) {
	if s == nil {
		return
	}
	abs = strings.TrimSpace(abs)
	if abs == "" {
		return
	}
	norm := filepath.Clean(abs)
	if a, err := filepath.Abs(abs); err == nil && a != "" {
		norm = filepath.Clean(a)
	}
	if s.PathOverlapsSiblingPrimaryWorkspace(norm) || (s.PathInsidePrimaryHome(norm) && !s.PathUnderPrimaryWorkspace(norm)) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.roots) > 0 && filepath.Clean(s.roots[0]) == norm {
		return
	}
	filtered := make([]string, 0, len(s.roots)+1)
	filtered = append(filtered, norm)
	for _, x := range s.roots {
		if filepath.Clean(x) != norm {
			filtered = append(filtered, x)
		}
	}
	s.roots = filtered
}

// preflightDestructive is a hook point for destructive tool preflight checks.
// Previously used for LLM-based auto-allow classification (removed).
func (s *State) preflightDestructive(ctx context.Context, toolName, text, resourcePath, actionKind string, payload any) error {
	return nil
}
