// The permission store: rules, grants, updates, resolution, and retrieval.
package safety

import (
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

type Store struct {
	mu sync.RWMutex

	mode           PermissionMode
	sessionModes   map[string]PermissionMode
	approvalPolicy ApprovalPolicy
	// runtimeMode keeps an explicit in-session choice active when the runner
	// reloads its configuration and rule files.
	runtimeMode bool
	// rules[source][behavior][]rule
	rules map[PermissionSource]map[PermissionBehavior][]PermissionRuleValue
	// fileSystemGrants are runtime-scoped capabilities produced by
	// request_permissions. They are deliberately not persisted as settings.
	fileSystemGrants []FileSystemPermissionGrant
	networkGrants    []NetworkPermissionGrant
	// strictAutoReviewRunIDs maps run IDs to their owning session. An empty
	// owner is reserved for process-level callers that do not have a session.
	strictAutoReviewRunIDs map[string]string
	// sandboxAvailable reports whether a sandbox backend will contain shell
	// commands that run without approval.
	sandboxAvailable bool
	// sessionSandbox holds the conversations that chose their own sandbox
	// mode — the sandbox half of an approval preset picked for that
	// conversation alone. Everything else runs under the configured mode.
	sessionSandbox map[string]sessionSandboxChoice
}

// sessionSandboxChoice is one conversation's own sandbox mode and whether a
// sandbox actually contains its shell commands under it.
type sessionSandboxChoice struct {
	mode      appcfg.SandboxMode
	available bool
}

type Snapshot struct {
	Mode PermissionMode `json:"mode"`
	// SandboxMode is the conversation's own sandbox mode when it chose one
	// (see ConfigForSnapshot), and empty when it runs under the configured
	// mode.
	SandboxMode            appcfg.SandboxMode                                                `json:"sandbox_mode,omitempty"`
	ApprovalPolicy         ApprovalPolicy                                                    `json:"approval_policy"`
	Rules                  map[PermissionSource]map[PermissionBehavior][]PermissionRuleValue `json:"rules"`
	FileSystemGrants       []FileSystemPermissionGrant                                       `json:"file_system_grants,omitempty"`
	NetworkGrants          []NetworkPermissionGrant                                          `json:"network_grants,omitempty"`
	StrictAutoReviewRunIDs []string                                                          `json:"strict_auto_review_run_ids,omitempty"`
}

func NewStore() *Store {
	return &Store{
		mode:                   ModeOnRequest,
		sessionModes:           make(map[string]PermissionMode),
		sessionSandbox:         make(map[string]sessionSandboxChoice),
		approvalPolicy:         ApprovalPolicy{Mode: ApprovalOnRequest},
		rules:                  make(map[PermissionSource]map[PermissionBehavior][]PermissionRuleValue),
		strictAutoReviewRunIDs: make(map[string]string),
	}
}

func (s *Store) Snapshot() Snapshot {
	return s.SnapshotForSession("")
}

func (s *Store) SnapshotForSession(sessionID string) Snapshot {
	if s == nil {
		return Snapshot{
			Mode:           ModeOnRequest,
			ApprovalPolicy: ApprovalPolicy{Mode: ApprovalOnRequest},
			Rules:          map[PermissionSource]map[PermissionBehavior][]PermissionRuleValue{},
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sessionID = strings.TrimSpace(sessionID)
	out := Snapshot{
		Mode:             s.mode,
		ApprovalPolicy:   s.approvalPolicy,
		Rules:            make(map[PermissionSource]map[PermissionBehavior][]PermissionRuleValue),
		FileSystemGrants: filterFileSystemGrantsForSession(s.fileSystemGrants, sessionID),
		NetworkGrants:    filterNetworkGrantsForSession(s.networkGrants, sessionID),
	}
	if sessionID != "" {
		if mode := s.sessionModes[sessionID]; mode != "" {
			// The conversation's own mode is its approval policy too: every
			// caller that reads the policy rather than the mode — escalation
			// refusals, sandbox-retry prompts, MCP auto-approval — must see
			// the same answer the rule engine does.
			out.Mode = mode
			out.ApprovalPolicy.Mode = mode
		}
		if choice, ok := s.sessionSandbox[sessionID]; ok {
			out.SandboxMode = choice.mode
		}
	}
	for runID, ownerSessionID := range s.strictAutoReviewRunIDs {
		if sessionID == "" {
			if ownerSessionID != "" {
				continue
			}
		} else if ownerSessionID != "" && ownerSessionID != sessionID {
			continue
		}
		out.StrictAutoReviewRunIDs = append(out.StrictAutoReviewRunIDs, runID)
	}
	sort.Strings(out.StrictAutoReviewRunIDs)
	if out.Mode == "" {
		out.Mode = ModeOnRequest
	}
	for src, byBehavior := range s.rules {
		if byBehavior == nil {
			continue
		}
		out.Rules[src] = make(map[PermissionBehavior][]PermissionRuleValue)
		for b, list := range byBehavior {
			cp := make([]PermissionRuleValue, 0, len(list))
			for _, rule := range list {
				if src == SourceSession && (sessionID == "" || strings.TrimSpace(rule.SessionID) != sessionID) {
					continue
				}
				cp = append(cp, clonePermissionRuleValue(rule))
			}
			out.Rules[src][b] = cp
		}
	}
	return out
}

func filterFileSystemGrantsForSession(grants []FileSystemPermissionGrant, sessionID string) []FileSystemPermissionGrant {
	out := make([]FileSystemPermissionGrant, 0, len(grants))
	for _, grant := range grants {
		if sessionID == "" || strings.TrimSpace(grant.SessionID) != sessionID {
			continue
		}
		out = append(out, cloneFileSystemGrant(grant))
	}
	return out
}

func filterNetworkGrantsForSession(grants []NetworkPermissionGrant, sessionID string) []NetworkPermissionGrant {
	out := make([]NetworkPermissionGrant, 0, len(grants))
	for _, grant := range grants {
		if sessionID == "" || strings.TrimSpace(grant.SessionID) != sessionID {
			continue
		}
		out = append(out, grant)
	}
	return out
}

func (s *Store) ApprovalPolicy() ApprovalPolicy {
	if s == nil {
		return ApprovalPolicy{Mode: ApprovalOnRequest}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	policy := s.approvalPolicy
	if policy.Mode == "" {
		policy.Mode = ApprovalOnRequest
	}
	return policy
}

func (s *Store) SetApprovalPolicy(policy ApprovalPolicy) {
	if s == nil {
		return
	}
	switch policy.Mode {
	case ApprovalUnlessTrusted, ApprovalOnRequest, ApprovalNever, ApprovalGranular:
	default:
		policy.Mode = ApprovalOnRequest
	}
	s.mu.Lock()
	s.approvalPolicy = policy
	s.mode = PermissionMode(policy.Mode)
	s.mu.Unlock()
}

func (s *Store) Mode() PermissionMode {
	return s.ModeForSession("")
}

func (s *Store) ModeForSession(sessionID string) PermissionMode {
	if s == nil {
		return ModeOnRequest
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		if mode := s.sessionModes[sessionID]; mode != "" {
			return mode
		}
	}
	if s.mode == "" {
		return ModeOnRequest
	}
	return s.mode
}

func (s *Store) SetSessionRuntimeMode(sessionID string, mode PermissionMode) {
	if s == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	if s.sessionModes == nil {
		s.sessionModes = make(map[string]PermissionMode)
	}
	s.sessionModes[sessionID] = normalizeMode(mode)
	s.mu.Unlock()
}

func (s *Store) SetMode(mode PermissionMode) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.mode = normalizeMode(mode)
	s.approvalPolicy.Mode = AskForApproval(s.mode)
	s.runtimeMode = false
	s.mu.Unlock()
}

func (s *Store) SetRuntimeMode(mode PermissionMode) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.mode = normalizeMode(mode)
	s.approvalPolicy.Mode = AskForApproval(s.mode)
	s.runtimeMode = true
	s.mu.Unlock()
}

// SetSandboxAvailable records whether a sandbox actually backs shell commands.
//
// The default is false, so a store that was never told fails closed: an
// unmatched shell command is only run without asking when something is known
// to contain it.
func (s *Store) SetSandboxAvailable(available bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.sandboxAvailable = available
	s.mu.Unlock()
}

func (s *Store) SandboxAvailable() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sandboxAvailable
}

// sandboxAvailableForSession reports whether a sandbox contains the shell
// commands of one conversation: under its own sandbox mode when it chose
// one, and under the configured mode otherwise.
func (s *Store) sandboxAvailableForSession(sessionID string) bool {
	if s == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if choice, ok := s.sessionSandbox[sessionID]; ok && sessionID != "" {
		return choice.available
	}
	return s.sandboxAvailable
}

// sessionSandboxMode is the sandbox mode one conversation picked for itself,
// or empty when it runs under the configured mode.
func (s *Store) sessionSandboxMode(sessionID string) appcfg.SandboxMode {
	if s == nil {
		return ""
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionSandbox[sessionID].mode
}

// setSessionSandboxMode records one conversation's own sandbox mode. Only the
// three modes a preset can pick are accepted; containment for it is unknown —
// and so fails closed — until the runtime refreshes availability, which it
// does as part of applying the update.
func (s *Store) setSessionSandboxMode(sessionID string, mode appcfg.SandboxMode) {
	if s == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	switch mode {
	case appcfg.SandboxModeReadOnly, appcfg.SandboxModeWorkspaceWrite, appcfg.SandboxModeDangerFullAccess:
	default:
		return
	}
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	if s.sessionSandbox == nil {
		s.sessionSandbox = make(map[string]sessionSandboxChoice)
	}
	s.sessionSandbox[sessionID] = sessionSandboxChoice{mode: mode}
	s.mu.Unlock()
}

// refreshSessionSandboxAvailability re-derives containment for every
// conversation that chose its own sandbox mode, the same way the configured
// mode's is derived: availability depends on the platform and the sandbox
// settings as well as the mode, and those can change on a config reload.
func (s *Store) refreshSessionSandboxAvailability(available func(appcfg.SandboxMode) bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for sessionID, choice := range s.sessionSandbox {
		choice.available = available(choice.mode)
		s.sessionSandbox[sessionID] = choice
	}
}

func (s *Store) HasRuntimeMode() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.runtimeMode
}

func (s *Store) AddRule(source PermissionSource, behavior PermissionBehavior, rule PermissionRuleValue) {
	if s == nil {
		return
	}
	source = normalizeSource(source)
	behavior = normalizeBehavior(behavior)
	rule = normalizeRule(rule)
	if rule.ToolName == "" || behavior == "" || source == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rules[source] == nil {
		s.rules[source] = make(map[PermissionBehavior][]PermissionRuleValue)
	}
	list, changed := appendOrMergeRule(s.rules[source][behavior], rule)
	if changed {
		s.rules[source][behavior] = list
	}
}

func (s *Store) ReplaceRules(source PermissionSource, behavior PermissionBehavior, rules []PermissionRuleValue) {
	if s == nil {
		return
	}
	source = normalizeSource(source)
	behavior = normalizeBehavior(behavior)
	if source == "" || behavior == "" {
		return
	}
	out := make([]PermissionRuleValue, 0, len(rules))
	for _, r := range rules {
		r = normalizeRule(r)
		if r.ToolName == "" {
			continue
		}
		out, _ = appendOrMergeRule(out, r)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rules[source] == nil {
		s.rules[source] = make(map[PermissionBehavior][]PermissionRuleValue)
	}
	s.rules[source][behavior] = out
}

func (s *Store) replaceSessionRules(behavior PermissionBehavior, sessionID string, rules []PermissionRuleValue) {
	if s == nil {
		return
	}
	behavior = normalizeBehavior(behavior)
	sessionID = strings.TrimSpace(sessionID)
	if behavior == "" || sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rules[SourceSession] == nil {
		s.rules[SourceSession] = make(map[PermissionBehavior][]PermissionRuleValue)
	}
	next := make([]PermissionRuleValue, 0, len(s.rules[SourceSession][behavior])+len(rules))
	for _, current := range s.rules[SourceSession][behavior] {
		if strings.TrimSpace(current.SessionID) != sessionID {
			next = append(next, current)
		}
	}
	for _, rule := range rules {
		rule = normalizeRule(rule)
		rule.SessionID = sessionID
		if rule.ToolName != "" {
			next, _ = appendOrMergeRule(next, rule)
		}
	}
	s.rules[SourceSession][behavior] = next
}

// ClearRuntimeGrants drops every approval that was granted for the current
// runtime rather than written to settings: session-scoped rules, filesystem
// and network capability grants, and strict auto-review run markers.
//
// Switching the active primary agent must call this. Those grants are answers
// to "may this agent do that in this workspace", so carrying them into a
// different agent's workspace would silently widen its permissions beyond
// anything the user approved for it. Persisted settings rules are untouched.
func (s *Store) ClearRuntimeGrants() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rules, SourceSession)
	s.fileSystemGrants = nil
	s.networkGrants = nil
	s.strictAutoReviewRunIDs = make(map[string]string)
	s.sessionModes = make(map[string]PermissionMode)
	s.sessionSandbox = make(map[string]sessionSandboxChoice)
}

func (s *Store) RemoveRules(source PermissionSource, behavior PermissionBehavior, rules []PermissionRuleValue) {
	if s == nil {
		return
	}
	source = normalizeSource(source)
	behavior = normalizeBehavior(behavior)
	if source == "" || behavior == "" {
		return
	}
	toDelete := make([]PermissionRuleValue, 0, len(rules))
	for _, r := range rules {
		r = normalizeRule(r)
		if r.ToolName != "" {
			toDelete = append(toDelete, r)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.rules[source][behavior]
	if len(list) == 0 {
		return
	}
	next := make([]PermissionRuleValue, 0, len(list))
	for _, cur := range list {
		if !containsRule(toDelete, cur) {
			next = append(next, cur)
		}
	}
	s.rules[source][behavior] = next
}

func (s *Store) Rules(source PermissionSource, behavior PermissionBehavior) []PermissionRuleValue {
	if s == nil {
		return nil
	}
	source = normalizeSource(source)
	behavior = normalizeBehavior(behavior)
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.rules[source][behavior]
	out := make([]PermissionRuleValue, 0, len(list))
	for _, rule := range list {
		out = append(out, clonePermissionRuleValue(rule))
	}
	return out
}

func (s *Store) AddFileSystemGrant(grant FileSystemPermissionGrant) {
	if s == nil {
		return
	}
	grant = normalizeFileSystemGrant(grant)
	if grant.Entry.Path.Key() == "" || grant.Entry.Access == "" {
		return
	}
	if grant.SessionID == "" || (grant.Scope == GrantScopeTurn && grant.RunID == "") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.fileSystemGrants {
		if existing.Entry.Path.Key() == grant.Entry.Path.Key() &&
			existing.Entry.Access == grant.Entry.Access &&
			existing.Entry.MissingPathBehavior == grant.Entry.MissingPathBehavior &&
			existing.Scope == grant.Scope && existing.SessionID == grant.SessionID && existing.RunID == grant.RunID &&
			intPointersEqual(existing.GlobScanMaxDepth, grant.GlobScanMaxDepth) {
			return
		}
	}
	s.fileSystemGrants = append(s.fileSystemGrants, grant)
}

func (s *Store) FileSystemGrants() []FileSystemPermissionGrant {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]FileSystemPermissionGrant, 0, len(s.fileSystemGrants))
	for _, grant := range s.fileSystemGrants {
		out = append(out, cloneFileSystemGrant(grant))
	}
	return out
}

func (s *Store) AddNetworkGrant(grant NetworkPermissionGrant) {
	if s == nil || !grant.Enabled {
		return
	}
	grant.RunID = strings.TrimSpace(grant.RunID)
	grant.SessionID = strings.TrimSpace(grant.SessionID)
	switch grant.Scope {
	case GrantScopeSession, GrantScopeTurn:
	default:
		grant.Scope = GrantScopeTurn
	}
	if grant.SessionID == "" || (grant.Scope == GrantScopeTurn && grant.RunID == "") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.networkGrants {
		if existing.Enabled == grant.Enabled && existing.Scope == grant.Scope && existing.SessionID == grant.SessionID && existing.RunID == grant.RunID {
			return
		}
	}
	s.networkGrants = append(s.networkGrants, grant)
}

func (s *Store) NetworkGrants() []NetworkPermissionGrant {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]NetworkPermissionGrant(nil), s.networkGrants...)
}

func (s *Store) EnableStrictAutoReview(runID string) {
	s.EnableStrictAutoReviewForSession("", runID)
}

func (s *Store) EnableStrictAutoReviewForSession(sessionID, runID string) {
	if s == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.strictAutoReviewRunIDs == nil {
		s.strictAutoReviewRunIDs = make(map[string]string)
	}
	s.strictAutoReviewRunIDs[runID] = sessionID
}

func (s *Store) StrictAutoReviewEnabled(runID string) bool {
	return s.StrictAutoReviewEnabledForSession("", runID)
}

func (s *Store) StrictAutoReviewEnabledForSession(sessionID, runID string) bool {
	if s == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ownerSessionID, ok := s.strictAutoReviewRunIDs[runID]
	return ok && (ownerSessionID == "" || ownerSessionID == sessionID)
}

func normalizeFileSystemGrant(grant FileSystemPermissionGrant) FileSystemPermissionGrant {
	grant = cloneFileSystemGrant(grant)
	grant.Entry.Path.Type = grant.Entry.Path.normalizedType()
	switch grant.Entry.Path.Type {
	case FileSystemPermissionPathTypePath:
		grant.Entry.Path.Path = strings.TrimSpace(grant.Entry.Path.Path)
		grant.Entry.Path.Pattern = ""
		grant.Entry.Path.Value = nil
	case FileSystemPermissionPathTypeGlobPattern:
		grant.Entry.Path.Path = ""
		grant.Entry.Path.Pattern = strings.TrimSpace(grant.Entry.Path.Pattern)
		grant.Entry.Path.Value = nil
	case FileSystemPermissionPathTypeSpecial:
		grant.Entry.Path.Path = ""
		grant.Entry.Path.Pattern = ""
	default:
		grant.Entry.Path = FileSystemPermissionPath{}
	}
	grant.RunID = strings.TrimSpace(grant.RunID)
	grant.SessionID = strings.TrimSpace(grant.SessionID)
	switch grant.Scope {
	case GrantScopeSession, GrantScopeTurn:
	default:
		grant.Scope = GrantScopeTurn
	}
	switch grant.Entry.Access {
	case FileSystemAccessRead, FileSystemAccessWrite, FileSystemAccessDeny:
	default:
		grant.Entry.Access = ""
	}
	return grant
}

func cloneFileSystemGrant(grant FileSystemPermissionGrant) FileSystemPermissionGrant {
	if grant.Entry.Path.Value != nil {
		value := *grant.Entry.Path.Value
		grant.Entry.Path.Value = &value
	}
	if grant.GlobScanMaxDepth != nil {
		depth := *grant.GlobScanMaxDepth
		grant.GlobScanMaxDepth = &depth
	}
	return grant
}

func intPointersEqual(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *Store) rulesByBehaviorInPriorityForSession(behavior PermissionBehavior, sessionID string) []PermissionRule {
	if s == nil {
		return nil
	}
	behavior = normalizeBehavior(behavior)
	if behavior == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sessionID = strings.TrimSpace(sessionID)
	out := make([]PermissionRule, 0, 32)
	for _, src := range sourcePriority {
		m := s.rules[src]
		if m == nil {
			continue
		}
		// Every lookup passes through here, so a source that cannot permit is
		// filtered once rather than at each call site.
		if behavior == BehaviorAllow && !MayPermit(src) {
			continue
		}
		list := m[behavior]
		for _, v := range list {
			if src == SourceSession && (sessionID == "" || strings.TrimSpace(v.SessionID) != sessionID) {
				continue
			}
			out = append(out, PermissionRule{
				Behavior: behavior,
				Value:    clonePermissionRuleValue(v),
				Source:   src,
			})
		}
	}
	return out
}

// sourcePriority breaks ties between rules of the SAME behavior, strongest
// first. It is not the top-level decision order: Engine.Evaluate checks deny
// across every source before ask, and ask before allow, so a rule that denies
// always beats one that allows regardless of which source holds it.
//
// Where this order does decide the outcome is a tie between two matching allow
// rules, and the winner carries its own BypassSandbox flag. That is the most
// dangerous attribute in the system, so it resolves in favour of the approval
// the operator granted to this specific agent rather than one committed to a
// repository. Upstream codex ranks its project layer above its user layer, but
// codex has no per-agent layer and does have managed layers above project to
// fall back on; forebrain has neither, so the agent's own grant wins here.
var sourcePriority = []PermissionSource{
	SourceSession,
	SourceLocalSettings,
	SourceProjectSettings,
}

func sourceFromDestination(dst PermissionDestination) PermissionSource {
	switch normalizeDestination(dst) {
	case DestinationSession:
		return SourceSession
	case DestinationLocalSettings:
		return SourceLocalSettings
	case DestinationProjectSettings:
		return SourceProjectSettings
	default:
		return ""
	}
}

func normalizeRule(v PermissionRuleValue) PermissionRuleValue {
	prefix := normalizeCommandPrefix(v.CommandPrefix)
	if len(v.CommandPrefix) > 0 && len(prefix) == 0 {
		return PermissionRuleValue{}
	}
	command := strings.TrimSpace(v.Command)
	if (len(prefix) > 0 || command != "") && !isShellTool(v.ToolName) {
		return PermissionRuleValue{}
	}
	// The two typed shell fields say opposite things — every command starting
	// with these tokens, versus this command alone — so a rule carrying both
	// has no meaning to apply.
	if len(prefix) > 0 && command != "" {
		return PermissionRuleValue{}
	}
	rule := PermissionRuleValue{
		ToolName:      strings.TrimSpace(v.ToolName),
		RuleContent:   strings.TrimSpace(v.RuleContent),
		CommandPrefix: prefix,
		Command:       command,
		BypassSandbox: v.BypassSandbox,
		SessionID:     strings.TrimSpace(v.SessionID),
	}
	// A typed field is the rule; the pattern string would only be a second,
	// disagreeing answer to the same question.
	if len(rule.CommandPrefix) > 0 || rule.Command != "" {
		rule.RuleContent = ""
	}
	return rule
}

func clonePermissionRuleValue(rule PermissionRuleValue) PermissionRuleValue {
	rule.CommandPrefix = append([]string(nil), rule.CommandPrefix...)
	return rule
}

func appendOrMergeRule(list []PermissionRuleValue, target PermissionRuleValue) ([]PermissionRuleValue, bool) {
	for i, it := range list {
		if !sameRuleIdentity(it, target) {
			continue
		}
		if target.BypassSandbox && !it.BypassSandbox {
			list[i].BypassSandbox = true
			return list, true
		}
		return list, false
	}
	return append(list, target), true
}

func containsRule(list []PermissionRuleValue, target PermissionRuleValue) bool {
	for _, it := range list {
		if sameRuleIdentity(it, target) {
			return true
		}
	}
	return false
}

func sameRuleIdentity(a, b PermissionRuleValue) bool {
	return strings.EqualFold(strings.TrimSpace(a.ToolName), strings.TrimSpace(b.ToolName)) &&
		strings.TrimSpace(a.RuleContent) == strings.TrimSpace(b.RuleContent) &&
		slices.Equal(normalizeCommandPrefix(a.CommandPrefix), normalizeCommandPrefix(b.CommandPrefix)) &&
		strings.TrimSpace(a.Command) == strings.TrimSpace(b.Command) &&
		strings.TrimSpace(a.SessionID) == strings.TrimSpace(b.SessionID)
}

func normalizeBehavior(b PermissionBehavior) PermissionBehavior {
	switch b {
	case BehaviorAllow, BehaviorDeny, BehaviorAsk:
		return b
	default:
		return ""
	}
}

func normalizeSource(s PermissionSource) PermissionSource {
	switch s {
	case SourceProjectSettings, SourceLocalSettings, SourceSession:
		return s
	default:
		return ""
	}
}

func normalizeDestination(d PermissionDestination) PermissionDestination {
	switch d {
	case DestinationSession, DestinationLocalSettings, DestinationProjectSettings:
		return d
	default:
		return ""
	}
}

func normalizeMode(m PermissionMode) PermissionMode {
	switch m {
	case ModeUnlessTrusted, ModeOnRequest, ModeNever, ModeGranular:
		return m
	default:
		return ModeOnRequest
	}
}

// SnapshotGrantsShellSandboxBypass reports whether a remembered allow rule in
// snap authorizes command to run outside the sandbox.
//
// The engine answers the same question during evaluation, but that answer
// reaches the shell tool as a context flag set by the tool-permission
// middleware. The tool asks again, from the snapshot it already holds, so that
// a persisted "don't ask again for commands that start with ..." keeps lifting
// the sandbox even when the flag did not survive the call path. Without it the
// command is sandboxed, the sandbox denies it, and the escalation prompt asks
// the user to grant the very rule they already granted — on every call.
//
// Restrictions win: a deny or ask rule from any source, matched the way the
// engine matches restrictions, refuses the bypass outright. Only a source the
// operator controls may widen (MayWidenSandbox), which is what keeps a rule
// committed to a repository from escalating itself on checkout.
// A compound command is answered clause by clause, the way the engine answers
// it: every clause must be covered, and a known-safe clause covers itself.
// Without that, one `&& git status` behind the remembered command means no
// snapshot grant can ever apply to the command the user actually ran.
func SnapshotGrantsShellSandboxBypass(snap Snapshot, command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	if snapshotRestrictsShellCommand(snap, command) {
		return false
	}
	if snapshotAllowsShellCommandWithBypass(snap, command) {
		return true
	}
	clauses := SplitShellClauses(command)
	if len(clauses) <= 1 {
		return false
	}
	granted := false
	for _, clause := range clauses {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		if snapshotRestrictsShellCommand(snap, clause) {
			return false
		}
		if ShellCommandIsKnownSafe(clause) {
			continue
		}
		if !snapshotAllowsShellCommandWithBypass(snap, clause) {
			return false
		}
		granted = true
	}
	return granted
}

func snapshotRestrictsShellCommand(snap Snapshot, command string) bool {
	for _, src := range sourcePriority {
		for _, behavior := range []PermissionBehavior{BehaviorDeny, BehaviorAsk} {
			for _, rule := range snap.Rules[src][behavior] {
				if ruleMatchesForBehavior(rule, "Bash", command, behavior) {
					return true
				}
			}
		}
	}
	return false
}

func snapshotAllowsShellCommandWithBypass(snap Snapshot, command string) bool {
	for _, src := range sourcePriority {
		if !MayPermit(src) || !MayWidenSandbox(src) {
			continue
		}
		for _, rule := range snap.Rules[src][BehaviorAllow] {
			if !rule.BypassSandbox {
				continue
			}
			if ruleMatchesForBehavior(rule, "Bash", command, BehaviorAllow) {
				return true
			}
		}
	}
	return false
}

func ApplyUpdate(store *Store, u PermissionUpdate) {
	if store == nil {
		return
	}
	if u.Destination == DestinationSession {
		u.SessionID = strings.TrimSpace(u.SessionID)
		if u.SessionID == "" {
			return
		}
		for index := range u.Rules {
			u.Rules[index].SessionID = u.SessionID
		}
		for index := range u.FileSystemGrants {
			u.FileSystemGrants[index].SessionID = u.SessionID
		}
		for index := range u.NetworkGrants {
			u.NetworkGrants[index].SessionID = u.SessionID
		}
	}
	switch u.Type {
	case UpdateSetMode:
		if u.Destination == DestinationSession {
			store.SetSessionRuntimeMode(u.SessionID, u.Mode)
		} else {
			store.SetRuntimeMode(u.Mode)
		}
	case UpdateSetSandboxMode:
		// The configured sandbox mode lives in the configuration; only a
		// conversation's own choice is runtime state.
		if u.Destination == DestinationSession {
			store.setSessionSandboxMode(u.SessionID, u.SandboxMode)
		}
	case UpdateAddRules:
		src := sourceFromDestination(u.Destination)
		if src == "" || !updateIsPermitted(src, u.Behavior) {
			return
		}
		for _, r := range u.Rules {
			store.AddRule(src, u.Behavior, r)
		}
	case UpdateReplaceRules:
		src := sourceFromDestination(u.Destination)
		if src == "" || !updateIsPermitted(src, u.Behavior) {
			return
		}
		if u.Destination == DestinationSession {
			store.replaceSessionRules(u.Behavior, u.SessionID, u.Rules)
		} else {
			store.ReplaceRules(src, u.Behavior, u.Rules)
		}
	case UpdateRemoveRules:
		src := sourceFromDestination(u.Destination)
		if src == "" {
			return
		}
		store.RemoveRules(src, u.Behavior, u.Rules)
	case UpdateAddPermissionGrants:
		for _, grant := range u.FileSystemGrants {
			store.AddFileSystemGrant(grant)
		}
		for _, grant := range u.NetworkGrants {
			store.AddNetworkGrant(grant)
		}
		store.EnableStrictAutoReviewForSession(u.SessionID, u.StrictAutoReviewRunID)
	}
}

// updateIsPermitted rejects writing an allow rule to a source that is not
// allowed to grant one. Removal is always permitted: taking a rule away can
// only narrow what is possible.
func updateIsPermitted(src PermissionSource, behavior PermissionBehavior) bool {
	return normalizeBehavior(behavior) != BehaviorAllow || MayPermit(src)
}

// ExplainRefusedUpdate returns why an update would be ignored, or "" when it is
// acceptable. Callers use it to refuse a request outright instead of reporting
// success for a rule that would never take effect.
func ExplainRefusedUpdate(dst PermissionDestination, behavior PermissionBehavior) string {
	src := sourceFromDestination(dst)
	if src == "" || updateIsPermitted(src, behavior) {
		return ""
	}
	return string(dst) + " cannot grant allow rules: rules committed to a project may deny or ask, but only you can permit an action"
}

type Resolved struct {
	InputMaxRunes  int
	InputBlockSubs []string
	OutputEnabled  bool

	RetrievalMaxChunk int
}

func ResolveFromRoot(cfg *appcfg.Root) *Resolved {
	if cfg == nil {
		return StrictDefaults()
	}
	return ResolveGuardrails(cfg.Agents.Defaults.Guardrails)
}

func StrictDefaults() *Resolved {
	return &Resolved{
		InputMaxRunes:     200000,
		InputBlockSubs:    nil,
		OutputEnabled:     false,
		RetrievalMaxChunk: 120000,
	}
}

func ResolveGuardrails(g appcfg.GuardrailsConfig) *Resolved {
	if reflect.DeepEqual(g, appcfg.GuardrailsConfig{}) {
		return StrictDefaults()
	}
	r := StrictDefaults()
	if g.Input.MaxRunes > 0 {
		r.InputMaxRunes = g.Input.MaxRunes
	}
	if len(g.Input.BlockSubstrings) > 0 {
		r.InputBlockSubs = append([]string(nil), g.Input.BlockSubstrings...)
	}
	if g.Output.Enabled != nil {
		r.OutputEnabled = *g.Output.Enabled
	}
	if g.Retrieval.MaxChunkRunes > 0 {
		r.RetrievalMaxChunk = g.Retrieval.MaxChunkRunes
	}
	return r
}

type Chunk struct {
	Text   string
	Source string
	MIME   string
	Score  float64
	Tags   []string
}

type RejectReason struct {
	Source string
	Reason string
}

func EvaluateRetrieval(r *Resolved, chunks []Chunk) ([]Chunk, []RejectReason) {
	if r == nil {
		return chunks, nil
	}
	maxR := r.RetrievalMaxChunk
	if maxR <= 0 {
		maxR = 120000
	}
	var kept []Chunk
	var rejects []RejectReason
	for _, ch := range chunks {
		t := strings.TrimSpace(ch.Text)
		if t == "" {
			rejects = append(rejects, RejectReason{Source: ch.Source, Reason: "empty_chunk"})
			continue
		}
		if utf8.RuneCountInString(t) > maxR {
			rejects = append(rejects, RejectReason{Source: ch.Source, Reason: "oversized_chunk"})
			continue
		}
		lt := strings.ToLower(t)
		if strings.Contains(lt, "ignore previous instructions") || strings.Contains(lt, "disregard the above") {
			rejects = append(rejects, RejectReason{Source: ch.Source, Reason: "injection_substring"})
			continue
		}
		kept = append(kept, ch)
	}
	return kept, rejects
}

func ContextItemsToChunks(sourceID string, content string) []Chunk {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	return []Chunk{{Text: content, Source: sourceID, MIME: "text/plain"}}
}

// Rule declares one presentation-only flag insertion.
//
// Every field is intentionally narrow. A rule cannot supply arbitrary text: it
// names a canonical command form and a flag to insert at a fixed position. The
// flag must also appear in presentationFlags, which is what verifySafe checks
// against, so adding a rule can never widen what rewriting is able to do.
type Rule struct {
	// Head is the required command head ("git", "npm", ...).
	Head string
	// Sub is the required subcommand, or "" to match any subcommand.
	Sub string
	// Flag is the presentation-only flag to insert.
	Flag string
	// AfterHead inserts the flag immediately after the head word rather than
	// at the end of the segment. Required for options git only accepts before
	// the subcommand (--no-pager).
	AfterHead bool
	// Conflicts lists flags whose presence means the rule must not fire,
	// because the user already expressed an intent about this dimension.
	Conflicts []string
	// Why documents the reason, surfaced in RewriteNote for the approval UI.
	Why string
}

// presentationFlags is the complete allowlist of tokens rewriting may insert.
//
// This set is the security boundary. Membership requires that a flag only
// affect how output is rendered: it must not change which files are touched,
// which network calls happen, exit status, or any other observable effect. A
// flag that alters behavior (--force, --yes, -r) must never be listed here, and
// verifySafe rejects any inserted token that is absent from this map.
var presentationFlags = map[string]string{
	"--no-pager":          "avoid pager control codes; output is captured, never viewed interactively",
	"--no-color":          "strip ANSI color escapes that cost tokens and carry no information",
	"--no-progress":       "suppress progress bars that emit thousands of redundant bytes",
	"--progress=none":     "suppress progress bars that emit thousands of redundant bytes",
	"--no-progress-meter": "suppress transfer progress meter noise",
	"--quiet":             "drop per-item chatter while preserving warnings and errors",
	"--no-ansi":           "strip ANSI escapes from captured output",
	"--color=never":       "strip ANSI color escapes that cost tokens and carry no information",
	"-s":                  "silence progress meter",
	"--batch":             "never invoke an interactive pager",
}

// colorConflicts groups the flags that express a color intent. If any is
// present, no color-related rule fires.
var colorConflicts = []string{"--color", "--color=always", "--color=auto", "--colour", "--no-color", "--color=never", "--no-ansi", "--ansi"}

// pagerConflicts groups flags expressing a pager intent.
var pagerConflicts = []string{"--paginate", "-p", "--no-pager"}

// progressConflicts groups flags expressing a progress intent.
var progressConflicts = []string{"--progress", "--no-progress", "--progress=none", "--no-progress-meter"}

// rules is the built-in rule table.
//
// Kept deliberately small. Each entry targets a command that is common in agent
// transcripts and whose default output carries pager control sequences, ANSI
// color, or progress spinners, all of which cost context-window tokens and
// convey nothing to a model reading captured text.
var rules = []Rule{
	// git pages by default when stdout looks like a terminal, and emits color
	// for diff/log/show. Neither survives usefully into a transcript.
	{Head: "git", Flag: "--no-pager", AfterHead: true, Conflicts: pagerConflicts, Why: presentationFlags["--no-pager"]},

	// Package managers with progress spinners.
	{Head: "npm", Flag: "--no-progress", Conflicts: progressConflicts, Why: presentationFlags["--no-progress"]},
	{Head: "npm", Flag: "--no-color", Conflicts: colorConflicts, Why: presentationFlags["--no-color"]},
	{Head: "yarn", Flag: "--no-progress", Conflicts: progressConflicts, Why: presentationFlags["--no-progress"]},
	{Head: "composer", Flag: "--no-ansi", Conflicts: colorConflicts, Why: presentationFlags["--no-ansi"]},
	{Head: "composer", Flag: "--no-progress", Conflicts: progressConflicts, Why: presentationFlags["--no-progress"]},

	// Build tools whose color output is pure escape-sequence overhead.
	{Head: "cargo", Flag: "--color=never", Conflicts: colorConflicts, Why: presentationFlags["--color=never"]},
	{Head: "mvn", Flag: "--batch-mode", Conflicts: []string{"--batch-mode", "-B", "--no-transfer-progress"}, Why: "suppress Maven download progress lines"},

	// Infra tools that colorize plans heavily.
	{Head: "terraform", Flag: "-no-color", Conflicts: []string{"-no-color"}, Why: presentationFlags["--no-color"]},
	{Head: "tofu", Flag: "-no-color", Conflicts: []string{"-no-color"}, Why: presentationFlags["--no-color"]},

	// curl's transfer meter is written to stderr but still lands in captures.
	{Head: "curl", Flag: "--no-progress-meter", Conflicts: append([]string{"-s", "--silent", "-#", "--progress-bar"}, progressConflicts...), Why: presentationFlags["--no-progress-meter"]},
}

// extraAllowed lists inserted tokens that are presentation-only but do not use
// the double-dash spelling, so they need explicit registration for verifySafe.
var extraAllowed = map[string]string{
	"-no-color":    presentationFlags["--no-color"],
	"--batch-mode": "suppress Maven download progress lines",
	"-B":           "suppress Maven download progress lines",
}

// allowedInsert reports whether tok may be inserted by a rewrite, and why.
func allowedInsert(tok string) (string, bool) {
	if why, ok := presentationFlags[tok]; ok {
		return why, true
	}
	why, ok := extraAllowed[tok]
	return why, ok
}

// rulesFor returns the rules applicable to a classification, in table order.
func rulesFor(c Classification) []Rule {
	if c.Head == "" {
		return nil
	}
	out := make([]Rule, 0, 2)
	for _, r := range rules {
		if r.Head != c.Head {
			continue
		}
		if r.Sub != "" && r.Sub != c.Sub {
			continue
		}
		out = append(out, r)
	}
	return out
}

// hasFlag reports whether any word in args equals flag or starts with "flag=".
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		u := unquoteWord(a)
		if u == flag {
			return true
		}
		if strings.HasPrefix(u, flag+"=") {
			return true
		}
	}
	return false
}

// conflicted reports whether any of the rule's conflict flags is present.
func conflicted(args []string, r Rule) bool {
	if hasFlag(args, r.Flag) {
		return true
	}
	for _, c := range r.Conflicts {
		if hasFlag(args, c) {
			return true
		}
	}
	return false
}

const (
	EnvYOLO = "FOREBRAIN_YOLO"
)

func RuntimeYOLOEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvYOLO))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func ApplyYOLO(cfg *appcfg.Root) bool {
	if cfg == nil || !RuntimeYOLOEnabled() {
		return false
	}
	cfg.DefaultPermissions = ""
	cfg.SandboxMode = appcfg.SandboxModeDangerFullAccess
	cfg.ApprovalPolicy = appcfg.NewApprovalPolicy(appcfg.ApprovalPolicyNever)
	return true
}
