// The safety runtime: permission state, suggestions, and request_permissions.
package safety

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

// Runtime holds one agent's permission decision state: the rule store and the
// evaluation engine, plus its own lock. It replaces what used to be
// permissionStore/permissionEngine (two fields on run.Runner, guarded by a
// slice of Runner's split lock) — see R3 in docs/plan/TUI_FIRST_REFACTOR_TASKS.md.
//
// Runtime deliberately does not hold AppCfg, Home, WorkspaceRoot, or
// ProjectRoot: those stay owned by Runner (or whatever else embeds a
// Runtime) and are passed in on every call. Storing a second copy here would
// recreate the I6 dual-owner problem R6 exists to eliminate — Runner's
// AppCfg already changes out from under it on every config reload, and nothing
// keeps a duplicate in sync automatically.
type Runtime struct {
	mu     sync.RWMutex
	store  *Store
	engine *Engine
}

// NewRuntime creates an empty permission runtime. store/engine are created
// lazily on first use (ensureLocked), matching the pre-R3 behavior where a
// Runner could evaluate a permission before its first Load.
func NewRuntime() *Runtime {
	return &Runtime{}
}

// NewRuntimeWithStore seeds a runtime from an already-built store — for
// tests that construct a *Store directly (AddRule, SetApprovalPolicy, ...)
// before wiring it into whatever holds the Runtime, rather than going
// through LoadFromDisk. The engine is always the stateless default; nothing
// in this codebase configures a non-default one.
func NewRuntimeWithStore(store *Store) *Runtime {
	return &Runtime{store: store, engine: NewEngine()}
}

// Paths bundles the three roots ApplyUpdate/LoadFromDisk need to resolve
// where persisted permission settings live — the same three Runner fields
// permissionDestinationPath/trustedProjectRoot read before R3.
type Paths struct {
	Home          string
	WorkspaceRoot string
	ProjectRoot   string
}

// Store returns the underlying rule store, creating it if this is the first
// call. It exists as an escape hatch for callers (chiefly tests) that need
// to manipulate rules directly — AddRule, SetApprovalPolicy,
// SetSessionRuntimeMode, SetSandboxAvailable, and the package-level
// ApplyUpdate all take a *Store — rather than duplicating every Store method
// as a forwarding method on Runtime.
func (rt *Runtime) Store(cfg *appcfg.Root) *Store {
	rt.mu.Lock()
	rt.ensureLocked(cfg)
	store := rt.store
	rt.mu.Unlock()
	return store
}

func (rt *Runtime) ensureLocked(cfg *appcfg.Root) {
	if rt.store == nil {
		rt.store = NewStore()
		// Derived once here as well as on every LoadFromDisk, so a runtime
		// evaluated before its first load still knows whether a sandbox backs
		// an unattended shell command.
		rt.store.SetSandboxAvailable(shellSandboxAvailable(cfg))
	}
	if rt.engine == nil {
		rt.engine = NewEngine()
	}
}

func (rt *Runtime) Snapshot(cfg *appcfg.Root) Snapshot {
	return rt.SnapshotForSession("", cfg)
}

func (rt *Runtime) SnapshotForSession(sessionID string, cfg *appcfg.Root) Snapshot {
	rt.mu.Lock()
	rt.ensureLocked(cfg)
	store := rt.store
	rt.mu.Unlock()
	return store.SnapshotForSession(sessionID)
}

// Evaluate returns the decision for a tool call, with the full-access (YOLO)
// override layered on top of the matched rule.
func (rt *Runtime) Evaluate(sessionID, toolName, input string, cfg *appcfg.Root, fullAccess bool) Decision {
	toolName = strings.TrimSpace(toolName)
	input = strings.TrimSpace(input)
	store, engine := rt.storeAndEngine(cfg)
	return applyRuntimePermissionOverrides(engine.EvaluateForSession(store, sessionID, toolName, input), fullAccess)
}

// Explain is Evaluate's read-only counterpart for `/permissions explain`: it
// must apply the same overrides Evaluate does, so the explanation never
// diverges from the decision the runtime would actually make.
func (rt *Runtime) Explain(sessionID, toolName, input string, cfg *appcfg.Root, fullAccess bool) ExplainResult {
	toolName = strings.TrimSpace(toolName)
	input = strings.TrimSpace(input)
	store, engine := rt.storeAndEngine(cfg)
	out := engine.ExplainForSession(store, sessionID, toolName, input)
	out.Decision = applyRuntimePermissionOverrides(out.Decision, fullAccess)
	return out
}

// EvaluateRaw is the engine decision with no full-access override applied. It
// exists to check whether a specific path already has a session-scoped
// approval (apply_patch's per-path check), which must not be widened by the
// same process-level escape Evaluate layers on for the operator-facing
// decision.
func (rt *Runtime) EvaluateRaw(sessionID, toolName, input string, cfg *appcfg.Root) Decision {
	toolName = strings.TrimSpace(toolName)
	input = strings.TrimSpace(input)
	store, engine := rt.storeAndEngine(cfg)
	return engine.EvaluateForSession(store, sessionID, toolName, input)
}

func (rt *Runtime) storeAndEngine(cfg *appcfg.Root) (*Store, *Engine) {
	rt.mu.Lock()
	rt.ensureLocked(cfg)
	store, engine := rt.store, rt.engine
	rt.mu.Unlock()
	return store, engine
}

// ApprovalPolicy reports the active approval policy, creating a fresh
// runtime (and so answering with the on-request default) if none has loaded
// yet — the same behavior a nil-check on the pre-R3 permissionStore field
// produced for its caller.
func (rt *Runtime) ApprovalPolicy(cfg *appcfg.Root) ApprovalPolicy {
	rt.mu.Lock()
	rt.ensureLocked(cfg)
	store := rt.store
	rt.mu.Unlock()
	return store.ApprovalPolicy()
}

// Mode reports the active permission mode, same fallback behavior as
// ApprovalPolicy.
func (rt *Runtime) Mode(cfg *appcfg.Root) PermissionMode {
	rt.mu.Lock()
	rt.ensureLocked(cfg)
	store := rt.store
	rt.mu.Unlock()
	return store.Mode()
}

// ResetGrants drops approvals scoped to the running agent so they cannot
// follow the runtime into a different primary agent's workspace. Persisted
// settings rules survive and are reloaded by the next LoadFromDisk.
func (rt *Runtime) ResetGrants(cfg *appcfg.Root) {
	rt.mu.Lock()
	rt.ensureLocked(cfg)
	store := rt.store
	rt.mu.Unlock()
	store.ClearRuntimeGrants()
}

// RefreshSandboxAvailability re-derives whether a sandbox will contain shell
// commands, for callers that changed the in-memory config's sandbox mode
// without going through LoadFromDisk.
func (rt *Runtime) RefreshSandboxAvailability(cfg *appcfg.Root) {
	rt.mu.Lock()
	rt.ensureLocked(cfg)
	refreshSandboxAvailability(rt.store, cfg)
	rt.mu.Unlock()
}

// refreshSandboxAvailability re-derives containment for the configured mode
// and for every conversation that chose its own.
func refreshSandboxAvailability(store *Store, cfg *appcfg.Root) {
	store.SetSandboxAvailable(shellSandboxAvailable(cfg))
	store.refreshSessionSandboxAvailability(func(mode appcfg.SandboxMode) bool {
		return shellSandboxAvailable(configForSandboxMode(cfg, mode))
	})
}

// ApplyUpdate applies a permission update and, for rule changes bound to a
// persistent destination, writes the updated rules back to disk.
func (rt *Runtime) ApplyUpdate(update PermissionUpdate, cfg *appcfg.Root, paths Paths) {
	rt.mu.Lock()
	rt.ensureLocked(cfg)
	ApplyUpdate(rt.store, update)
	switch update.Type {
	case UpdateSetSandboxMode:
		refreshSandboxAvailability(rt.store, cfg)
	case UpdateAddRules, UpdateReplaceRules, UpdateRemoveRules:
		switch update.Destination {
		case DestinationLocalSettings, DestinationProjectSettings:
			rt.persistDestinationLocked(update.Destination, paths)
		}
	}
	rt.mu.Unlock()
}

// permissionSettingsFile is the on-disk shape of a persisted permission
// settings destination (local_settings.json / safety.json).
type permissionSettingsFile struct {
	Rules     map[PermissionBehavior][]PermissionRuleValue `json:"rules,omitempty"`
	UpdatedAt int64                                        `json:"updated_at,omitempty"`
}

// destinationPath resolves where a settings destination is stored.
//
// The two destinations deliberately live in different trees:
//
//   - localSettings sits under the active primary agent's workspace, so an
//     approval granted to one agent is never evaluated for another.
//   - projectSettings sits under the user's project next to .forebrain/skills, so
//     the rules travel with the repository and apply to whoever works on it.
//
// Project rules are only honoured for a trusted project, matching how project
// skills are gated. Without that check a hostile repository could commit a
// permissions file that grants itself sandbox-bypass on checkout. An untrusted
// or absent project simply has no project settings.
func destinationPath(dst PermissionDestination, paths Paths) (string, bool) {
	switch dst {
	case DestinationLocalSettings:
		root := strings.TrimSpace(paths.WorkspaceRoot)
		if root == "" {
			// Same fallback the pre-R3 Runner.workspaceRoot() applied before
			// calling this: an empty WorkspaceRoot still resolves under Home,
			// it is not "no local settings destination" the way an absent
			// project is for DestinationProjectSettings below.
			root = filepath.Join(strings.TrimSpace(paths.Home), "workspace")
		}
		if root == "" {
			return "", false
		}
		return filepath.Join(root, "state", "permissions", "local_settings.json"), true
	case DestinationProjectSettings:
		root := trustedProjectRoot(paths)
		if root == "" {
			return "", false
		}
		return filepath.Join(root, ".forebrain", "safety.json"), true
	default:
		return "", false
	}
}

// ProjectSettingsPath answers where a project's permission rules live for
// paths, and whether that project may hold any (it must be trusted) — the very
// answer LoadFromDisk and ApplyUpdate act on, for a surface that manages one
// project's rules without running a session in it.
func ProjectSettingsPath(paths Paths) (string, bool) {
	return destinationPath(DestinationProjectSettings, paths)
}

// TrustedRoot returns the launch project's root when the frozen context says
// it is trusted, and "" otherwise. Version control is not part of the gate:
// project MCP/LSP/skills load in any trusted project, with each entry's own
// consent still required (R6).
//
// It is a pure function of a ProjectContext the caller already holds, on
// purpose: the assembly points freeze the launch project for the whole
// session, and re-reading the trust store at each consumer would introduce a
// second source of truth — a trust decision changed mid-session would then
// flip features that were already decided from it.
func TrustedRoot(launch ProjectContext) string {
	if !launch.IsTrusted() {
		return ""
	}
	root := strings.TrimSpace(launch.Project.Root)
	if root == "" {
		return ""
	}
	return filepath.Clean(root)
}

// trustedProjectRoot returns the user's project directory when it is trusted,
// and "" otherwise. paths.ProjectRoot is always an already-resolved project
// root — a launch project's root or a registered project's — and is taken as
// the exact boundary: walking up from it again would hand a project registered
// inside a larger checkout the checkout's rules file and trust decision.
func trustedProjectRoot(paths Paths) string {
	dir := strings.TrimSpace(paths.ProjectRoot)
	if dir == "" {
		return ""
	}
	project, err := ResolveRegisteredProject(dir)
	if err != nil {
		return ""
	}
	trusted, err := IsTrusted(strings.TrimSpace(paths.Home), project)
	if err != nil || !trusted {
		return ""
	}
	return filepath.Clean(project.Root)
}

func sourceForPersistentDestination(dst PermissionDestination) PermissionSource {
	switch dst {
	case DestinationLocalSettings:
		return SourceLocalSettings
	case DestinationProjectSettings:
		return SourceProjectSettings
	default:
		return ""
	}
}

// readPermissionSettingsFile reads a settings destination. The second result
// separates "this scope has no rules" from "this scope could not be read":
// an absent file is an empty scope, while an unreadable or malformed one is a
// failure the caller must not mistake for an empty one.
func readPermissionSettingsFile(path string) (permissionSettingsFile, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return permissionSettingsFile{}, errors.Is(err, fs.ErrNotExist)
	}
	var f permissionSettingsFile
	if json.Unmarshal(raw, &f) != nil {
		return permissionSettingsFile{}, false
	}
	return f, true
}

// LoadFromDisk rebuilds the approval policy and rule store from the current
// config and the on-disk settings files. Called on every agent Load: the
// rules on disk belong to that agent's workspace, so a load that skipped them
// would evaluate the previously active agent's approvals.
func (rt *Runtime) LoadFromDisk(cfg *appcfg.Root, paths Paths) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.ensureLocked(cfg)
	store := rt.store
	runtimeOverride := store.HasRuntimeMode()
	runtimeMode := store.Mode()
	store.SetApprovalPolicy(approvalPolicyFromConfig(cfg))
	// The engine runs an unmatched shell command unattended only because a
	// sandbox contains it, so it has to know whether one actually will — for
	// the configured mode and for every conversation that chose its own.
	refreshSandboxAvailability(store, cfg)
	if runtimeOverride {
		store.SetRuntimeMode(runtimeMode)
	}
	destinations := []PermissionDestination{
		DestinationLocalSettings,
		DestinationProjectSettings,
	}
	for _, dst := range destinations {
		src := sourceForPersistentDestination(dst)
		if src == "" {
			continue
		}
		// An absent file, or a destination with nowhere to live (no project, or
		// one that is not trusted), means this agent has no rules for that
		// source, so it is replaced with nothing rather than left alone.
		// Skipping the replace would strand the previously active agent's — or
		// the previous project's — rules in the store, which is exactly the
		// cross-scope leak this keying removes.
		//
		// A file that exists but cannot be read or parsed is different: it says
		// nothing about what this agent is allowed to do. Replacing from it
		// would silently drop the approvals already in the store — including
		// one the user granted moments ago — and they would be asked for it
		// again for the rest of the session. Keep what is loaded instead.
		var fileData permissionSettingsFile
		path, hasPath := destinationPath(dst, paths)
		if hasPath {
			readable := false
			fileData, readable = readPermissionSettingsFile(path)
			if !readable {
				slog.Warn("permission settings file unreadable; keeping loaded rules",
					"destination", string(dst), "path", path)
				continue
			}
		}
		for _, b := range []PermissionBehavior{
			BehaviorAllow,
			BehaviorDeny,
			BehaviorAsk,
		} {
			rules := fileData.Rules[b]
			// A settings file is just a file: someone can hand-edit an allow
			// rule into a source that is not permitted to grant one, so it is
			// dropped on the way in rather than trusted.
			if b == BehaviorAllow && !MayPermit(src) {
				rules = nil
			}
			store.ReplaceRules(src, b, rules)
		}
	}
}

func approvalPolicyFromConfig(cfg *appcfg.Root) ApprovalPolicy {
	policy := ApprovalPolicy{Mode: ApprovalOnRequest}
	if cfg == nil {
		return policy
	}
	switch cfg.ApprovalPolicy.Mode {
	case appcfg.ApprovalPolicyUntrusted:
		policy.Mode = ApprovalUnlessTrusted
	case appcfg.ApprovalPolicyNever:
		policy.Mode = ApprovalNever
	default:
		policy.Mode = ApprovalOnRequest
	}
	return policy
}

// persistDestinationLocked requires the caller to already hold rt.mu.
func (rt *Runtime) persistDestinationLocked(dst PermissionDestination, paths Paths) {
	if rt.store == nil {
		return
	}
	path, ok := destinationPath(dst, paths)
	if !ok {
		return
	}
	src := sourceForPersistentDestination(dst)
	if src == "" {
		return
	}
	data := permissionSettingsFile{
		Rules:     make(map[PermissionBehavior][]PermissionRuleValue),
		UpdatedAt: time.Now().Unix(),
	}
	for _, b := range []PermissionBehavior{
		BehaviorAllow,
		BehaviorDeny,
		BehaviorAsk,
	} {
		data.Rules[b] = rt.store.Rules(src, b)
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return
	}
	_ = atomicWritePermissionFile(path, append(raw, '\n'))
}

func atomicWritePermissionFile(target string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(target), ".perm-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return replacePermissionFile(tmp, target)
}

// shellSandboxAvailable reports whether a shell command will be contained.
// Full access is an explicit decision to run uncontained, so it counts as no
// sandbox for this purpose.
func shellSandboxAvailable(cfg *appcfg.Root) bool {
	if cfg == nil || cfg.DangerFullAccessEnabled() {
		return false
	}
	return NewManager().Decide(cfg, ToolKindShell).UseSandbox
}

// applyRuntimePermissionOverrides layers the operator escape over an engine
// decision. Evaluate and Explain must apply it through this one function:
// `/permissions explain` exists to say what the runtime would do, so a second
// copy of the rules is a copy that will eventually describe a decision the
// runtime does not make.
//
// fullAccess is "this conversation's preset is full access", whether the
// environment flag forced it or the user picked it; full access *is* the YOLO
// posture (plan 203), which is why one flag covers both and a full-access
// session cannot drift from what --yolo does.
func applyRuntimePermissionOverrides(decision Decision, fullAccess bool) Decision {
	switch {
	case explicitPermissionDeny(decision):
		return decision
	case fullAccess:
		decision.Behavior = BehaviorAllow
		decision.Reason = ReasonYOLO
		decision.BypassSandbox = true
	}
	return decision
}

// explicitPermissionDeny distinguishes a deny rule the user or project wrote
// from a synthetic deny produced because the active policy cannot prompt. YOLO
// bypasses approval policy, but it must never erase an explicit deny rule.
//
// The matched rule's own behavior is what decides this, not merely that some
// rule matched. An ask rule reaches this function as a deny only because the
// never policy converted it (never_policy_converts_ask_to_deny), and that is a
// refusal for want of a prompt — exactly the kind YOLO is meant to bypass.
// Reading any match as an explicit deny left YOLO refusing precisely the tools
// the user had asked to be consulted about, with no prompt available to resolve
// it, while every tool no rule mentioned ran unattended.
func explicitPermissionDeny(decision Decision) bool {
	return decision.Behavior == BehaviorDeny &&
		decision.Matched != nil &&
		decision.Matched.Behavior == BehaviorDeny
}

type ToolApprovalSuggestion struct {
	PermissionToolName string
	PermissionInput    string
	ExactRuleContent   string
	PrefixRuleContent  string
	BypassSandbox      bool
	OneShotOnly        bool
}

func BuildToolApprovalSuggestion(toolName string, toolInput any) ToolApprovalSuggestion {
	name := strings.TrimSpace(toolName)
	canonical := CanonicalToolName(name)
	switch {
	case strings.EqualFold(canonical, "Bash"):
		cmd := strings.TrimSpace(toolInputCommand(toolInput))
		if cmd == "" {
			return ToolApprovalSuggestion{}
		}
		out := ToolApprovalSuggestion{
			PermissionToolName: "Bash",
			PermissionInput:    cmd,
			ExactRuleContent:   cmd,
			// Persisted command amendments are explicit trust decisions. A
			// matching command may start outside the sandbox on later calls.
			BypassSandbox: true,
		}
		if content := effectivePrefixRuleContent(toolInputPrefixRule(toolInput), cmd); content != "" {
			out.PrefixRuleContent = content
		} else if pfx, ok := GetSimpleCommandPrefix(cmd); ok {
			pfx = strings.TrimSpace(pfx)
			if pfx != "" {
				out.PrefixRuleContent = pfx + ":*"
			}
		}
		return out
	case strings.EqualFold(name, "request_permissions"):
		return ToolApprovalSuggestion{PermissionToolName: name, PermissionInput: "request_permissions"}
	case strings.EqualFold(name, "apply_patch"):
		return ToolApprovalSuggestion{
			PermissionToolName: "apply_patch",
			PermissionInput:    strings.Join(toolInputResolvedPaths(toolInput), "\n"),
		}
	case strings.EqualFold(canonical, "Read"),
		strings.EqualFold(canonical, "Write"),
		strings.EqualFold(canonical, "Edit"),
		strings.EqualFold(canonical, "MultiEdit"):
		path := strings.TrimSpace(toolInputFilePath(toolInput))
		if path == "" {
			return ToolApprovalSuggestion{}
		}
		out := ToolApprovalSuggestion{
			PermissionToolName: canonical,
			PermissionInput:    path,
			ExactRuleContent:   path,
		}
		if dir := filePathSuggestionDir(path); dir != "" {
			out.PrefixRuleContent = dir + "/*"
		}
		return out
	case strings.EqualFold(canonical, "WebFetch"):
		u := strings.TrimSpace(toolInputURL(toolInput))
		if u == "" {
			return ToolApprovalSuggestion{}
		}
		out := ToolApprovalSuggestion{
			PermissionToolName: "WebFetch",
			PermissionInput:    u,
			ExactRuleContent:   u,
		}
		if host := webFetchHost(u); host != "" {
			out.PrefixRuleContent = webFetchDomainPrefix + host
		}
		return out
	case strings.EqualFold(canonical, "WebSearch"):
		query := strings.TrimSpace(toolInputStringField(toolInput, "query"))
		if query == "" {
			query = strings.TrimSpace(toolInputStringField(toolInput, "q"))
		}
		if query == "" {
			return ToolApprovalSuggestion{}
		}
		return ToolApprovalSuggestion{
			PermissionToolName: "WebSearch",
			PermissionInput:    query,
			ExactRuleContent:   query,
		}
	case isMCPToolName(name):
		in := strings.TrimSpace(toolInputString(toolInput))
		mode := strings.ToLower(strings.TrimSpace(toolInputStringField(toolInput, "mcp_approval_mode")))
		return ToolApprovalSuggestion{
			PermissionToolName: name,
			PermissionInput:    in,
			ExactRuleContent:   "*",
			OneShotOnly:        mode == "prompt" || mode == "writes",
		}
	default:
		if name == "" {
			return ToolApprovalSuggestion{}
		}
		in := strings.TrimSpace(toolInputString(toolInput))
		exact := in
		if exact == "" || exact == "{}" {
			exact = name
		}
		return ToolApprovalSuggestion{
			PermissionToolName: name,
			PermissionInput:    exact,
			ExactRuleContent:   exact,
		}
	}
}

// effectivePrefixRuleContent resolves the ":*"-suffixed prefix rule to persist
// for a model-suggested command prefix, or "" when no trustworthy prefix
// applies (the caller then falls back to the whole command).
//
// The prefix must actually prefix cmd — the command that runs. Command
// rewriting (shell.go in this package) inserts presentation-only flags such as
// `git --no-pager` before approval, so a prefix the model derived from the
// pre-rewrite command ("git -C sub add") no longer prefixes the rewritten
// command ("git --no-pager -C sub add -A"). Rewriting the prefix the same way
// restores the match, so the stored rule keeps matching this and future
// (equally rewritten) invocations and a prefix approval sticks instead of
// collapsing to the exact command.
//
// The banned-prefix guard runs on the original tokens first, so inserting a
// presentation flag can never launder a forbidden bare prefix (e.g. "git")
// into an allowed one. Both candidates are validated against cmd, so a prefix
// that cannot be proven to match is discarded rather than widened.
func effectivePrefixRuleContent(requested, cmd string) string {
	requested = strings.TrimSpace(requested)
	if requested == "" || bannedExecPolicyPrefix(splitShellLike(requested)) {
		return ""
	}
	if prefixRuleCoversCommand(requested, cmd) {
		return requested + ":*"
	}
	rewritten := strings.TrimSpace(Rewrite(requested).Command)
	if rewritten != "" && rewritten != requested &&
		!bannedExecPolicyPrefix(splitShellLike(rewritten)) &&
		prefixRuleCoversCommand(rewritten, cmd) {
		return rewritten + ":*"
	}
	return ""
}

// prefixRuleCoversCommand reports whether persisting requested as an allow
// prefix rule would authorize everything cmd does.
//
// It answers the question the engine will later be asked, clause by clause, so
// the proposal cannot promise more or less than the stored rule delivers. A
// known-safe clause is covered without the rule, exactly as the engine treats
// it; every other clause must match the candidate prefix itself. Requiring only
// that the rule prefix the whole command string would reject every compound
// form, so `cd <dir> && <command>` — the shape the model emits most often —
// could never carry a model-supplied prefix.
//
// The rule must also do some work: a prefix matching no clause at all would
// persist a permission unrelated to the command the user is looking at.
func prefixRuleCoversCommand(requested, cmd string) bool {
	rule := ParseShellRule(requested + ":*")
	clauses := SplitShellClauses(strings.TrimSpace(cmd))
	if len(clauses) == 0 {
		clauses = []string{strings.TrimSpace(cmd)}
	}
	matchedAny := false
	for _, clause := range clauses {
		if shellRuleMatchesClause(rule, clause) {
			matchedAny = true
			continue
		}
		if ShellCommandIsKnownSafe(clause) {
			continue
		}
		return false
	}
	return matchedAny
}

func bannedExecPolicyPrefix(tokens []string) bool {
	for len(tokens) > 0 {
		if _, ok := parseEnvAssignment(tokens[0]); !ok {
			break
		}
		tokens = tokens[1:]
	}
	key := strings.Join(tokens, "\x00")
	switch key {
	case "/bin/bash", "/bin/bash\x00-c", "/bin/bash\x00-lc",
		"/bin/sh", "/bin/sh\x00-c", "/bin/sh\x00-lc",
		"/bin/zsh", "/bin/zsh\x00-c", "/bin/zsh\x00-lc",
		"Rscript", "bash", "bash\x00-c", "bash\x00-lc",
		"bun", "bun\x00-e", "bun\x00run",
		"cmd", "cmd\x00/c", "cmd\x00/k", "cmd.exe", "cmd.exe\x00/c", "cmd.exe\x00/k",
		"dash", "dash\x00-c", "deno", "deno\x00eval", "env", "fish", "fish\x00-c", "git",
		"julia", "julia\x00-e", "ksh", "ksh\x00-c", "lua", "lua\x00-e",
		"node", "node\x00-e", "nodejs", "nodejs\x00-e", "npm\x00run", "osascript",
		"perl", "perl\x00-e", "php", "php\x00-r", "pnpm\x00run",
		"powershell", "powershell\x00-Command", "powershell\x00-EncodedCommand", "powershell\x00-File", "powershell\x00-c",
		"powershell.exe", "powershell.exe\x00-Command", "powershell.exe\x00-EncodedCommand", "powershell.exe\x00-File", "powershell.exe\x00-c",
		"pwsh", "pwsh\x00-Command", "pwsh\x00-EncodedCommand", "pwsh\x00-File", "pwsh\x00-c", "pwsh\x00-e", "pwsh\x00-ec", "pwsh\x00-f",
		"py", "py\x00-3", "pypy", "pypy3", "python", "python\x00-", "python\x00-c",
		"python3", "python3\x00-", "python3\x00-c", "pythonw", "pyw", "rm",
		"ruby", "ruby\x00-e", "sh", "sh\x00-c", "sh\x00-lc", "sudo", "yarn\x00run",
		"zsh", "zsh\x00-c", "zsh\x00-lc":
		return true
	default:
		return false
	}
}

func toolInputResolvedPaths(toolInput any) []string {
	m := toolInputMap(toolInput)
	var paths []string
	switch values := m["resolved_paths"].(type) {
	case []string:
		paths = append(paths, values...)
	case []any:
		for _, value := range values {
			if path, ok := value.(string); ok {
				paths = append(paths, path)
			}
		}
	}
	out := paths[:0]
	seen := map[string]struct{}{}
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

func toolInputStringField(toolInput any, key string) string {
	value, _ := toolInputMap(toolInput)[key].(string)
	return value
}

func toolInputPrefixRule(toolInput any) string {
	m := toolInputMap(toolInput)
	if len(m) == 0 {
		return ""
	}
	value, ok := m["prefix_rule"]
	if !ok {
		return ""
	}
	var parts []string
	switch raw := value.(type) {
	case []string:
		parts = append(parts, raw...)
	case []any:
		for _, item := range raw {
			part, ok := item.(string)
			if !ok {
				return ""
			}
			parts = append(parts, part)
		}
	default:
		return ""
	}
	parts = normalizeCommandPrefix(parts)
	if len(parts) == 0 {
		return ""
	}
	return joinShellTokens(parts)
}

func isMCPToolName(toolName string) bool {
	n := strings.TrimSpace(toolName)
	return strings.HasPrefix(n, "mcp__")
}

func toolInputString(toolInput any) string {
	switch v := toolInput.(type) {
	case string:
		return v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// proposedExecPolicyAmendment is the prefix the suggestion carries, before any
// question of whether it is worth storing as a rule. Only
// ProposedCommandApprovalRules asks it; callers outside this package want
// ProposedCommandPrefixAmendment, which answers what actually gets stored.
func proposedExecPolicyAmendment(suggestion ToolApprovalSuggestion) ExecPolicyAmendment {
	rule := ParseShellRule(suggestion.PrefixRuleContent)
	if rule.Type != ShellRulePrefix || strings.TrimSpace(rule.Value) == "" {
		return nil
	}
	tokens := splitShellLike(rule.Value)
	if len(tokens) == 0 {
		return nil
	}
	return ExecPolicyAmendment(tokens)
}

// maxProposedClauseRules bounds the per-clause rule set. The user approves what
// the overlay shows them, and a set naming a dozen fragments of a long script
// is not something anyone can judge in one keystroke; past this the whole
// command as one exact rule is the honest proposal.
const maxProposedClauseRules = 6

// ProposedCommandApprovalRules is the rule set a persistent Bash approval
// stores. It is the server's single answer to "what does remembering this
// command mean", and every surface and validator derives its wording and its
// verdict from it.
//
// The engine allows a compound command only when every clause of it that is
// not already known-safe matches an allow rule of its own, so a proposal that
// names one clause of several silences nothing: the prompt returns on the next
// call and the rule the user accepted sits in local settings doing nothing.
// That is why the proposal is a set. The model wraps its real work in the same
// scaffolding every time — an `export VAR=$(...)` in front, a `| tail -N` or
// `; echo "EXIT=$?"` behind — and only the middle clause varies, so remembering
// the scaffolding verbatim and the work as a prefix is what makes the second
// call of a build or a test suite stop asking.
//
// The three shapes, in the order they are tried:
//
//   - One prefix for the whole command, when a single clause carries it. This
//     is the common case and the narrowest useful rule there is.
//   - One rule per clause that needs one, used only when at least one of them
//     is a prefix. Without a prefix in the set it would grant exactly what the
//     whole-command rule below grants, spread over more rules.
//   - The whole command as one exact rule, which authorizes that command and
//     nothing else.
//
// Reporting false means nothing can be remembered and the prompt offers to run
// the command this once.
func ProposedCommandApprovalRules(suggestion ToolApprovalSuggestion) ([]PermissionRuleValue, bool) {
	if !strings.EqualFold(CanonicalToolName(suggestion.PermissionToolName), "Bash") {
		return nil, false
	}
	// A prefix rule matches every command that starts with it, so one that kept
	// every word of this command still grants arguments the user never saw:
	// remembering "ls -la" would authorize "ls -la ~/.ssh" unasked. A prefix
	// earns its rule only by actually dropping part of the command; otherwise
	// the command itself is the rule, which is the same test clauseApprovalRule
	// applies to each clause of a compound line.
	if amendment := proposedExecPolicyAmendment(suggestion); len(amendment) > 0 &&
		ExecPolicyAmendmentTruncatesCommand(amendment, suggestion.ExactRuleContent) {
		return []PermissionRuleValue{{
			ToolName: "Bash", CommandPrefix: []string(amendment), BypassSandbox: true,
		}}, true
	}
	if rules, ok := proposedClauseRules(suggestion.ExactRuleContent); ok {
		return rules, true
	}
	command := strings.TrimSpace(suggestion.ExactRuleContent)
	if command == "" {
		return nil, false
	}
	return []PermissionRuleValue{{ToolName: "Bash", Command: command, BypassSandbox: true}}, true
}

// proposedClauseRules derives one rule per clause of command that needs an
// approval of its own, or false when the command is not the kind of text a
// clause list describes or when no clause yields a prefix.
func proposedClauseRules(command string) ([]PermissionRuleValue, bool) {
	command = strings.TrimSpace(command)
	// A heredoc body is input to the preceding command; its lines are not
	// clauses and must never become rules. A loop or a conditional splits into
	// fragments ("do ...", "done") that are not commands either.
	if command == "" || hasShellHeredocOperator(command) || containsShellControlKeyword(splitShellLike(command)) {
		return nil, false
	}
	clauses := SplitShellClauses(command)
	if len(clauses) <= 1 {
		return nil, false
	}
	rules := make([]PermissionRuleValue, 0, len(clauses))
	prefixes := 0
	for _, clause := range clauses {
		if ShellCommandIsKnownSafe(clause) {
			continue
		}
		rule, ok := clauseApprovalRule(clause)
		if !ok {
			return nil, false
		}
		if len(rule.CommandPrefix) > 0 {
			prefixes++
		}
		rules = append(rules, rule)
		if len(rules) > maxProposedClauseRules {
			return nil, false
		}
	}
	if prefixes == 0 || len(rules) < 2 {
		return nil, false
	}
	return rules, true
}

// clauseApprovalRule is the narrowest rule that authorizes one clause: the
// token prefix when deriving one actually drops part of the clause, and the
// clause verbatim otherwise. A prefix holding every word of the clause would
// additionally authorize arguments the user never saw, so the exact rule wins
// there.
func clauseApprovalRule(clause string) (PermissionRuleValue, bool) {
	if prefix, ok := GetSimpleCommandPrefix(clause); ok {
		if tokens := splitShellLike(prefix); ExecPolicyAmendmentTruncatesCommand(tokens, clause) {
			return PermissionRuleValue{ToolName: "Bash", CommandPrefix: tokens, BypassSandbox: true}, true
		}
	}
	clause = strings.TrimSpace(clause)
	if clause == "" {
		return PermissionRuleValue{}, false
	}
	return PermissionRuleValue{ToolName: "Bash", Command: clause, BypassSandbox: true}, true
}

// CommandApprovalRulePrefixes lists, in order, the command prefixes a proposed
// rule set leaves free to vary. Surfaces word the persistent choice from it:
// what the user is being asked to accept beyond the command in front of them
// is exactly these.
func CommandApprovalRulePrefixes(rules []PermissionRuleValue) []string {
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		if len(rule.CommandPrefix) > 0 {
			out = append(out, joinShellTokens(rule.CommandPrefix))
		}
	}
	return out
}

// CommandApprovalScopeKind names what a persistent Bash choice covers, which
// is what decides the sentence a surface puts on the row.
type CommandApprovalScopeKind string

const (
	// CommandApprovalScopePrefix: commands that start with Prefixes[0]. The
	// prefix drops part of the command, so it covers more than this one call.
	CommandApprovalScopePrefix CommandApprovalScopeKind = "prefix"
	// CommandApprovalScopeCommand: the command shown above the choices, and
	// nothing else. The rules behind it authorize that command alone, so a
	// surface states it plainly and never repeats the text as if it were a
	// pattern.
	CommandApprovalScopeCommand CommandApprovalScopeKind = "command"
	// CommandApprovalScopeCommandWithVariants: the command shown above the
	// choices, with the clauses named by Prefixes free to vary.
	CommandApprovalScopeCommandWithVariants CommandApprovalScopeKind = "command_with_variants"
)

// CommandApprovalScope is the surface-neutral description of what remembering a
// command grants. Deciding it is the engine's job — it is the same question as
// deciding the rules — and rendering it is each surface's, which is why the
// wording lives with the surface and never here: the terminal writes one
// English sentence, the web writes whichever language the reader picked.
type CommandApprovalScope struct {
	Kind     CommandApprovalScopeKind `json:"kind"`
	Prefixes []string                 `json:"prefixes,omitempty"`
}

// CommandApprovalScopeFor describes what the proposed rules grant. A proposed
// prefix rule always drops part of the command it came from, so a single one
// of them is exactly the "commands that start with" case.
func CommandApprovalScopeFor(rules []PermissionRuleValue) CommandApprovalScope {
	prefixes := CommandApprovalRulePrefixes(rules)
	switch {
	case len(rules) == 1 && len(prefixes) == 1:
		return CommandApprovalScope{Kind: CommandApprovalScopePrefix, Prefixes: prefixes}
	case len(prefixes) == 0:
		return CommandApprovalScope{Kind: CommandApprovalScopeCommand}
	default:
		return CommandApprovalScope{Kind: CommandApprovalScopeCommandWithVariants, Prefixes: prefixes}
	}
}

// ProposedCommandPrefixAmendment is the token prefix a command's persistent
// choice stores, or nil when that choice is one or more whole-command rules.
// It is the one test for "is this approval a prefix amendment". Asking
// proposedExecPolicyAmendment answers a different question — whether a prefix
// could be derived at all — and says yes for commands whose proposal is the
// command itself.
func ProposedCommandPrefixAmendment(suggestion ToolApprovalSuggestion) ExecPolicyAmendment {
	rules, ok := ProposedCommandApprovalRules(suggestion)
	if !ok || len(rules) != 1 || len(rules[0].CommandPrefix) == 0 {
		return nil
	}
	return ExecPolicyAmendment(rules[0].CommandPrefix)
}

func toolInputCommand(toolInput any) string {
	switch v := toolInput.(type) {
	case map[string]any:
		if cmd, ok := v["command"].(string); ok {
			return cmd
		}
	case string:
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(v)), &in) == nil {
			return in.Command
		}
	default:
		b, err := json.Marshal(v)
		if err == nil {
			var in struct {
				Command string `json:"command"`
			}
			if json.Unmarshal(b, &in) == nil {
				return in.Command
			}
		}
	}
	return ""
}

func toolInputFilePath(toolInput any) string {
	switch v := toolInput.(type) {
	case map[string]any:
		if p, ok := v["file_path"].(string); ok {
			return p
		}
	case string:
		var in struct {
			FilePath string `json:"file_path"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(v)), &in) == nil {
			return in.FilePath
		}
	default:
		b, err := json.Marshal(v)
		if err == nil {
			var in struct {
				FilePath string `json:"file_path"`
			}
			if json.Unmarshal(b, &in) == nil {
				return in.FilePath
			}
		}
	}
	return ""
}

func toolInputURL(toolInput any) string {
	switch v := toolInput.(type) {
	case map[string]any:
		if u, ok := v["url"].(string); ok {
			return u
		}
	case string:
		var in struct {
			URL string `json:"url"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(v)), &in) == nil {
			return in.URL
		}
	default:
		b, err := json.Marshal(v)
		if err == nil {
			var in struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(b, &in) == nil {
				return in.URL
			}
		}
	}
	return ""
}

func filePathSuggestionDir(p string) string {
	clean := strings.TrimSpace(p)
	if clean == "" {
		return ""
	}
	idx := strings.LastIndexByte(clean, '/')
	if idx <= 0 {
		return ""
	}
	return clean[:idx]
}

func NormalizeRequestPermissionsResponseAtCWD(response RequestPermissionsResponse, cwd string) (RequestPermissionsResponse, error) {
	cwd = filepath.Clean(strings.TrimSpace(cwd))
	if cwd == "" || !filepath.IsAbs(cwd) {
		return RequestPermissionsResponse{}, fmt.Errorf("permission cwd must be absolute: %q", cwd)
	}
	switch response.Scope {
	case "", GrantScopeTurn:
		response.Scope = GrantScopeTurn
	case GrantScopeSession:
	default:
		return RequestPermissionsResponse{}, fmt.Errorf("invalid permission scope %q", response.Scope)
	}
	if response.Permissions.Network.IsEmpty() {
		response.Permissions.Network = nil
	}
	fs := response.Permissions.FileSystem
	if fs == nil {
		return response, nil
	}
	entries := make([]FileSystemPermissionEntry, 0, len(fs.Read)+len(fs.Write)+len(fs.Entries))
	for _, path := range fs.Read {
		entries = append(entries, FileSystemPermissionEntry{Path: NewFileSystemPermissionPath(path), Access: FileSystemAccessRead})
	}
	for _, path := range fs.Write {
		entries = append(entries, FileSystemPermissionEntry{Path: NewFileSystemPermissionPath(path), Access: FileSystemAccessWrite})
	}
	entries = append(entries, fs.Entries...)
	if fs.GlobScanMaxDepth != nil && *fs.GlobScanMaxDepth <= 0 {
		return RequestPermissionsResponse{}, fmt.Errorf("glob_scan_max_depth must be greater than zero")
	}
	seen := map[string]struct{}{}
	normalized := make([]FileSystemPermissionEntry, 0, len(entries))
	for _, entry := range entries {
		switch entry.Access {
		case FileSystemAccessRead, FileSystemAccessWrite, FileSystemAccessDeny:
		default:
			return RequestPermissionsResponse{}, fmt.Errorf("invalid filesystem permission access %q", entry.Access)
		}
		if entry.MissingPathBehavior != "" && entry.MissingPathBehavior != FileSystemPermissionMissingPathSkip {
			return RequestPermissionsResponse{}, fmt.Errorf("invalid missing_path_behavior %q", entry.MissingPathBehavior)
		}
		entry.Path.Type = entry.Path.normalizedType()
		switch entry.Path.Type {
		case FileSystemPermissionPathTypePath:
			path := strings.TrimSpace(entry.Path.Path)
			if path == "" {
				return RequestPermissionsResponse{}, fmt.Errorf("filesystem permission path required")
			}
			if !filepath.IsAbs(path) {
				path = filepath.Join(cwd, path)
			}
			entry.Path.Path = filepath.Clean(path)
			entry.Path.Pattern = ""
			entry.Path.Value = nil
		case FileSystemPermissionPathTypeGlobPattern:
			if entry.Access != FileSystemAccessDeny {
				return RequestPermissionsResponse{}, fmt.Errorf("glob file system permissions only support deny-read entries")
			}
			entry.Path.Pattern = strings.TrimSpace(entry.Path.Pattern)
			if entry.Path.Pattern == "" {
				return RequestPermissionsResponse{}, fmt.Errorf("filesystem glob pattern required")
			}
			entry.Path.Path = ""
			entry.Path.Value = nil
		case FileSystemPermissionPathTypeSpecial:
			if entry.Path.Value == nil {
				return RequestPermissionsResponse{}, fmt.Errorf("filesystem special path value required")
			}
			if err := validateFileSystemSpecialPath(*entry.Path.Value); err != nil {
				return RequestPermissionsResponse{}, err
			}
			entry.Path.Path = ""
			entry.Path.Pattern = ""
		default:
			return RequestPermissionsResponse{}, fmt.Errorf("invalid filesystem permission path type %q", entry.Path.Type)
		}
		key := string(entry.Access) + "\x00" + entry.Path.Key() + "\x00" + string(entry.MissingPathBehavior)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, entry)
	}
	if len(normalized) == 0 {
		response.Permissions.FileSystem = nil
		return response, nil
	}
	response.Permissions.FileSystem = &FileSystemPermissionProfile{Entries: normalized, GlobScanMaxDepth: fs.GlobScanMaxDepth}
	return response, nil
}

func IntersectRequestPermissionsResponseAtCWD(requested, granted RequestPermissionsResponse, cwd string) (RequestPermissionsResponse, error) {
	var err error
	requested, err = NormalizeRequestPermissionsResponseAtCWD(requested, cwd)
	if err != nil {
		return RequestPermissionsResponse{}, fmt.Errorf("invalid requested permissions: %w", err)
	}
	granted, err = NormalizeRequestPermissionsResponseAtCWD(granted, cwd)
	if err != nil {
		return RequestPermissionsResponse{}, fmt.Errorf("invalid granted permissions: %w", err)
	}
	if !requested.Permissions.Network.AllowsNetwork() || !granted.Permissions.Network.AllowsNetwork() {
		granted.Permissions.Network = nil
	}
	if granted.Permissions.FileSystem == nil || requested.Permissions.FileSystem == nil {
		granted.Permissions.FileSystem = nil
		return granted, nil
	}

	accepted := make([]FileSystemPermissionEntry, 0, len(granted.Permissions.FileSystem.Entries))
	for _, entry := range granted.Permissions.FileSystem.Entries {
		if entry.Access == FileSystemAccessDeny {
			continue
		}
		matched := false
		for _, want := range requested.Permissions.FileSystem.Entries {
			if permissionAccessCovers(want.Access, entry.Access) && permissionPathCovers(want.Path, entry.Path, cwd) {
				matched = true
				break
			}
		}
		if matched && !permissionDeniedByRequestedEntries(entry, requested.Permissions.FileSystem.Entries, cwd) {
			accepted = appendUniquePermissionEntry(accepted, materializePermissionEntry(entry, cwd))
		}
	}
	for _, entries := range [][]FileSystemPermissionEntry{
		requested.Permissions.FileSystem.Entries,
		granted.Permissions.FileSystem.Entries,
	} {
		for _, entry := range entries {
			if entry.Access == FileSystemAccessDeny && denyEntryConstrainsGrant(entry, accepted, cwd) {
				accepted = appendUniquePermissionEntry(accepted, materializePermissionEntry(entry, cwd))
			}
		}
	}
	if len(accepted) == 0 {
		granted.Permissions.FileSystem = nil
	} else {
		granted.Permissions.FileSystem = &FileSystemPermissionProfile{
			Entries: accepted,
			GlobScanMaxDepth: mergePermissionGlobScanDepth(
				requested.Permissions.FileSystem,
				granted.Permissions.FileSystem,
				accepted,
				cwd,
			),
		}
	}
	return granted, nil
}

func permissionAccessCovers(requested, granted FileSystemPermissionAccess) bool {
	switch granted {
	case FileSystemAccessRead:
		return requested == FileSystemAccessRead || requested == FileSystemAccessWrite
	case FileSystemAccessWrite:
		return requested == FileSystemAccessWrite
	default:
		return false
	}
}

func denyEntryConstrainsGrant(deny FileSystemPermissionEntry, grants []FileSystemPermissionEntry, cwd string) bool {
	for _, grant := range grants {
		if permissionPathsOverlap(deny.Path, grant.Path, cwd) {
			return true
		}
	}
	return false
}

func permissionPathsOverlap(left, right FileSystemPermissionPath, cwd string) bool {
	if left.normalizedType() == FileSystemPermissionPathTypeGlobPattern {
		return globStaticPrefixOverlaps(left.Pattern, right, cwd)
	}
	if right.normalizedType() == FileSystemPermissionPathTypeGlobPattern {
		return globStaticPrefixOverlaps(right.Pattern, left, cwd)
	}
	leftPath, leftOK := left.Resolve(cwd)
	rightPath, rightOK := right.Resolve(cwd)
	if !leftOK || !rightOK {
		return left.Key() != "" && left.Key() == right.Key()
	}
	return permissionPathWithin(leftPath, rightPath) || permissionPathWithin(rightPath, leftPath)
}

func appendUniquePermissionEntry(entries []FileSystemPermissionEntry, entry FileSystemPermissionEntry) []FileSystemPermissionEntry {
	for _, existing := range entries {
		if existing.Access == entry.Access && existing.Path.Key() == entry.Path.Key() && existing.MissingPathBehavior == entry.MissingPathBehavior {
			return entries
		}
	}
	return append(entries, entry)
}

func permissionPathCovers(requested, granted FileSystemPermissionPath, cwd string) bool {
	if !permissionPathSupportedOnPlatform(granted, runtime.GOOS) {
		return false
	}
	requestedPath, requestedOK := requested.Resolve(cwd)
	grantedPath, grantedOK := granted.Resolve(cwd)
	if requestedOK && grantedOK {
		return permissionPathWithin(grantedPath, requestedPath)
	}
	return requested.Key() != "" && requested.Key() == granted.Key()
}

func permissionPathSupportedOnPlatform(path FileSystemPermissionPath, platform string) bool {
	return !(platform == "windows" && path.normalizedType() == FileSystemPermissionPathTypeSpecial &&
		path.Value != nil && path.Value.Kind == FileSystemSpecialPathSlashTmp)
}

func globStaticPrefixOverlaps(pattern string, other FileSystemPermissionPath, cwd string) bool {
	otherPath, ok := other.Resolve(cwd)
	if !ok {
		return false
	}
	pattern = strings.TrimSpace(pattern)
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(cwd, pattern)
	}
	index := strings.IndexAny(pattern, "*?[]")
	prefix := pattern
	if index >= 0 {
		if index == 0 {
			return false
		}
		prefix = pattern[:index]
		if !strings.HasSuffix(prefix, string(filepath.Separator)) && !strings.HasSuffix(prefix, "/") && !strings.HasSuffix(prefix, "\\") {
			prefix = filepath.Dir(prefix)
		}
	}
	return permissionPathWithin(prefix, otherPath) || permissionPathWithin(otherPath, prefix)
}

func permissionDeniedByRequestedEntries(granted FileSystemPermissionEntry, requested []FileSystemPermissionEntry, cwd string) bool {
	path, ok := granted.Path.Resolve(cwd)
	if !ok {
		return false
	}
	for _, entry := range requested {
		if entry.Access == FileSystemAccessDeny && entry.Path.Matches(path, cwd) {
			return true
		}
	}
	return false
}

func materializePermissionEntry(entry FileSystemPermissionEntry, cwd string) FileSystemPermissionEntry {
	switch entry.Path.normalizedType() {
	case FileSystemPermissionPathTypeGlobPattern:
		pattern := strings.TrimSpace(entry.Path.Pattern)
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(cwd, pattern)
		}
		entry.Path = FileSystemPermissionPath{Type: FileSystemPermissionPathTypeGlobPattern, Pattern: filepath.Clean(pattern)}
	case FileSystemPermissionPathTypeSpecial:
		if entry.Path.Value != nil && entry.Path.Value.Kind == FileSystemSpecialPathProjectRoots {
			if path, ok := entry.Path.Resolve(cwd); ok {
				entry.Path = NewFileSystemPermissionPath(path)
			}
		}
	default:
		entry.Path.Type = entry.Path.normalizedType()
	}
	return entry
}

func mergePermissionGlobScanDepth(requested, granted *FileSystemPermissionProfile, entries []FileSystemPermissionEntry, cwd string) *int {
	type contribution struct {
		present   bool
		unbounded bool
		depth     int
	}
	read := func(profile *FileSystemPermissionProfile) contribution {
		if profile == nil {
			return contribution{}
		}
		present := false
		for _, candidate := range profile.Entries {
			if candidate.Access != FileSystemAccessDeny || candidate.Path.normalizedType() != FileSystemPermissionPathTypeGlobPattern {
				continue
			}
			materialized := materializePermissionEntry(candidate, cwd)
			for _, retained := range entries {
				if retained.Access == FileSystemAccessDeny && retained.Path.Key() == materialized.Path.Key() {
					present = true
					break
				}
			}
		}
		if !present {
			return contribution{}
		}
		if profile.GlobScanMaxDepth == nil {
			return contribution{present: true, unbounded: true}
		}
		return contribution{present: true, depth: *profile.GlobScanMaxDepth}
	}
	left, right := read(requested), read(granted)
	if !left.present && !right.present {
		return nil
	}
	if left.unbounded || right.unbounded {
		return nil
	}
	depth := left.depth
	if right.depth > depth {
		depth = right.depth
	}
	return &depth
}

func permissionPathWithin(path, root string) bool {
	path = filepath.Clean(strings.TrimSpace(path))
	root = filepath.Clean(strings.TrimSpace(root))
	if path == "" || root == "" || !filepath.IsAbs(path) || !filepath.IsAbs(root) {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}
