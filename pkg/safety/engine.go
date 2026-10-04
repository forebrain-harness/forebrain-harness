// The permission engine: evaluation, classification, and project trust.
package safety

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type Decision struct {
	Behavior PermissionBehavior `json:"behavior"`
	Mode     PermissionMode     `json:"mode"`
	Reason   string             `json:"reason,omitempty"`
	Matched  *PermissionRule    `json:"matched,omitempty"`
	// BypassSandbox is independent from approval. It is only true for an
	// explicit danger-full-access decision or an explicitly trusted exec rule.
	BypassSandbox bool `json:"bypass_sandbox,omitempty"`
}

// The two reasons a decision can carry that did not come from the engine at
// all. Both are stamped by the runner after evaluation, for the two escapes an
// operator can switch on outside the rules: the YOLO environment variable and
// the Full Access execution profile. They are named here, next to the type that
// carries them, because a consumer that misspells one silently re-imposes the
// approval the operator just turned off.
const (
	ReasonYOLO             = "yolo"
	ReasonDangerFullAccess = "danger_full_access"
)

// IsOperatorOverride reports whether this decision was settled by one of those
// escapes rather than by a rule or the default policy. A layer that adds
// approval requirements of its own (MCP tool hints, for example) has nothing
// left to add once it is true: the operator has removed approvals wholesale,
// and in both states there is no prompt left to answer, so re-asking only turns
// into a refusal.
func (d Decision) IsOperatorOverride() bool {
	return d.Reason == ReasonYOLO || d.Reason == ReasonDangerFullAccess
}

// IsUnmatchedAsk reports that no rule matched and the default was to ask.
//
// Callers that layer their own safelist on top of the defaults test this rather
// than a specific Reason string: the reasons distinguish *why* the default was
// reached and get refined over time, so matching one by name silently changes
// behaviour when a new reason is introduced.
func (d Decision) IsUnmatchedAsk() bool {
	return d.Behavior == BehaviorAsk && d.Matched == nil
}

func (d Decision) ExecRequirement() ExecRequirement {
	switch d.Behavior {
	case BehaviorAllow:
		return ExecSkip
	case BehaviorDeny:
		return ExecForbidden
	default:
		return ExecNeedsApproval
	}
}

// ExplainRule describes one rule considered during evaluation. It deliberately
// omits the rule's bypass_sandbox flag: whether the sandbox is actually lifted
// depends on the rule's source (see MayWidenSandbox), so a per-rule flag could
// only mislead. The effective answer is Decision.BypassSandbox.
type ExplainRule struct {
	Source        PermissionSource   `json:"source"`
	Behavior      PermissionBehavior `json:"behavior"`
	ToolName      string             `json:"tool_name"`
	RuleContent   string             `json:"rule_content,omitempty"`
	CommandPrefix []string           `json:"command_prefix,omitempty"`
	Command       string             `json:"command,omitempty"`
	Matched       bool               `json:"matched"`
}

type ExplainResult struct {
	Decision Decision      `json:"decision"`
	Rules    []ExplainRule `json:"rules"`
}

type Engine struct{}

func NewEngine() *Engine { return &Engine{} }

func (e *Engine) Evaluate(store *Store, toolName, input string) Decision {
	return e.EvaluateForSession(store, "", toolName, input)
}

func (e *Engine) EvaluateForSession(store *Store, sessionID, toolName, input string) Decision {
	mode := ModeOnRequest
	if store != nil {
		mode = store.ModeForSession(sessionID)
	}
	tool := strings.TrimSpace(toolName)
	in := strings.TrimSpace(input)

	if r := firstMatch(store, sessionID, BehaviorDeny, tool, in); r != nil {
		return Decision{Behavior: BehaviorDeny, Mode: mode, Reason: "matched_deny_rule", Matched: r}
	}
	if isShellTool(tool) {
		if r := firstShellClauseMatch(store, sessionID, BehaviorDeny, tool, in); r != nil {
			return Decision{Behavior: BehaviorDeny, Mode: mode, Reason: "matched_deny_rule_in_shell_clause", Matched: r}
		}
	}
	if r := firstMatch(store, sessionID, BehaviorAsk, tool, in); r != nil {
		if mode == ModeNever || approvalPolicyNever(store) {
			return Decision{Behavior: BehaviorDeny, Mode: mode, Reason: "never_policy_converts_ask_to_deny", Matched: r}
		}
		return Decision{Behavior: BehaviorAsk, Mode: mode, Reason: "matched_ask_rule", Matched: r}
	}
	if isShellTool(tool) {
		if r := firstShellClauseMatch(store, sessionID, BehaviorAsk, tool, in); r != nil {
			if mode == ModeNever || approvalPolicyNever(store) {
				return Decision{Behavior: BehaviorDeny, Mode: mode, Reason: "never_policy_converts_ask_to_deny", Matched: r}
			}
			return Decision{Behavior: BehaviorAsk, Mode: mode, Reason: "matched_ask_rule_in_shell_clause", Matched: r}
		}
	}
	if r := firstAllowMatch(store, sessionID, tool, in); r != nil {
		return Decision{Behavior: BehaviorAllow, Mode: mode, Reason: "matched_allow_rule", Matched: r, BypassSandbox: grantsSandboxBypass(r)}
	}

	// For shell tools with compound commands (&&, ||, ;), evaluate each
	// clause independently against allow rules. If every clause matches some
	// allow rule, the compound command is allowed even though no single rule
	// matches the full command string.
	if isShellTool(tool) {
		if clauseDecision := e.evaluateShellCompoundAllow(store, sessionID, tool, in, mode); clauseDecision != nil {
			return *clauseDecision
		}
	}

	if IsSafeReadOnlyTool(tool) {
		return Decision{Behavior: BehaviorAllow, Mode: mode, Reason: "safe_readonly_default_allow"}
	}
	if isSafeInternalTool(tool) {
		return Decision{Behavior: BehaviorAllow, Mode: mode, Reason: "safe_internal_default_allow"}
	}
	if isShellTool(tool) {
		policy := ApprovalOnRequest
		if store != nil {
			policy = store.ApprovalPolicy().Mode
		}
		// An unmatched shell command is only run unattended because the sandbox
		// contains it. When it is flagged dangerous, or no sandbox will back it,
		// that justification is gone: prompt instead, and when the operator has
		// turned prompting off, refuse rather than run it unattended.
		if shellNeedsApprovalDespiteDefault(store, sessionID, in) {
			if policy == ApprovalNever || mode == ModeNever {
				return Decision{Behavior: BehaviorDeny, Mode: mode, Reason: "shell_unsafe_without_prompt_deny"}
			}
			return Decision{Behavior: BehaviorAsk, Mode: mode, Reason: "shell_unsafe_default_ask"}
		}
		switch policy {
		case ApprovalNever, ApprovalOnRequest, ApprovalGranular:
			return Decision{Behavior: BehaviorAllow, Mode: mode, Reason: "shell_default_allow"}
		}
	}

	if mode == ModeNever || approvalPolicyNever(store) {
		return Decision{Behavior: BehaviorDeny, Mode: mode, Reason: "never_policy_default_deny"}
	}
	return Decision{Behavior: BehaviorAsk, Mode: mode, Reason: "default_ask"}
}

func approvalPolicyNever(store *Store) bool {
	return store != nil && store.ApprovalPolicy().Mode == ApprovalNever
}

// firstShellClauseMatch applies deny/ask rules to each independently parsed
// command segment. An exact rule for the whole opaque command is handled by
// firstMatch before this function. Prefix and wildcard rules never authorize
// redirections, substitutions, heredocs, or other complex shell syntax.
func firstShellClauseMatch(store *Store, sessionID string, behavior PermissionBehavior, toolName, input string) *PermissionRule {
	if store == nil {
		return nil
	}
	clauses := SplitShellClauses(input)
	if len(clauses) <= 1 {
		return nil
	}
	for _, clause := range clauses {
		for _, r := range store.rulesByBehaviorInPriorityForSession(behavior, sessionID) {
			if !sameCanonicalTool(r.Value.ToolName, toolName) {
				continue
			}
			content := strings.TrimSpace(r.Value.RuleContent)
			if len(r.Value.CommandPrefix) > 0 {
				matched := CommandPrefixMatches(r.Value.CommandPrefix, clause)
				if behavior == BehaviorDeny || behavior == BehaviorAsk {
					matched = commandPrefixMatchesRestriction(r.Value.CommandPrefix, clause)
				}
				if matched {
					copy := r
					return &copy
				}
			}
			if command := strings.TrimSpace(r.Value.Command); command != "" {
				literal := literalShellRule(command)
				matched := shellRuleMatchesClause(literal, clause)
				if behavior == BehaviorDeny || behavior == BehaviorAsk {
					matched = shellRuleMatchesRestriction(literal, clause)
				}
				if matched {
					copy := r
					return &copy
				}
				continue
			}
			if len(r.Value.CommandPrefix) == 0 {
				matched := content == "" || shellRuleMatchesClause(ParseShellRule(content), clause)
				if behavior == BehaviorDeny || behavior == BehaviorAsk {
					matched = content == "" || shellRuleMatchesRestriction(ParseShellRule(content), clause)
				}
				if matched {
					copy := r
					return &copy
				}
			}
		}
	}
	return nil
}

func (e *Engine) Explain(store *Store, toolName, input string) ExplainResult {
	return e.ExplainForSession(store, "", toolName, input)
}

func (e *Engine) ExplainForSession(store *Store, sessionID, toolName, input string) ExplainResult {
	tool := strings.TrimSpace(toolName)
	in := strings.TrimSpace(input)
	out := ExplainResult{
		Decision: e.EvaluateForSession(store, sessionID, tool, in),
		Rules:    make([]ExplainRule, 0, 64),
	}
	for _, behavior := range []PermissionBehavior{BehaviorDeny, BehaviorAsk, BehaviorAllow} {
		list := store.rulesByBehaviorInPriorityForSession(behavior, sessionID)
		for _, r := range list {
			out.Rules = append(out.Rules, ExplainRule{
				Source:        r.Source,
				Behavior:      behavior,
				ToolName:      r.Value.ToolName,
				RuleContent:   r.Value.RuleContent,
				CommandPrefix: append([]string(nil), r.Value.CommandPrefix...),
				Command:       r.Value.Command,
				Matched:       ruleMatchesForBehavior(r.Value, tool, in, behavior),
			})
		}
	}
	return out
}

// firstAllowMatch returns the matching allow rule that carries the strongest
// sandbox capability, not merely the first one in insertion order.
//
// Allow rules are additive grants, so every matching rule already authorizes the
// command and only their sandbox capability differs. Without this, an earlier
// broad rule that never carried a bypass (a hand-written or legacy
// "go build:*" content rule) shadows the bypass-carrying command-prefix rule a
// "don't ask again for commands that start with ..." approval persisted later.
// The command is then allowed but still sandboxed, the sandbox denies it, and
// the escalation path asks for approval again on every single call — the
// remembered approval appears to have no effect at all.
// grantsSandboxBypass reports whether a matched rule lifts the sandbox. A rule
// may allow a command without being permitted to decide it runs unsandboxed;
// see MayWidenSandbox.
func grantsSandboxBypass(rule *PermissionRule) bool {
	return rule != nil && rule.Value.BypassSandbox && MayWidenSandbox(rule.Source)
}

func firstAllowMatch(store *Store, sessionID, toolName, input string) *PermissionRule {
	match := firstMatch(store, sessionID, BehaviorAllow, toolName, input)
	if match == nil || grantsSandboxBypass(match) {
		return match
	}
	// A bypass granted by any source the operator controls upgrades the
	// decision, even when a lower-priority rule matched first.
	for _, r := range store.rulesByBehaviorInPriorityForSession(BehaviorAllow, sessionID) {
		if !grantsSandboxBypass(&r) {
			continue
		}
		if ruleMatchesForBehavior(r.Value, toolName, input, BehaviorAllow) {
			return &r
		}
	}
	return match
}

func firstMatch(store *Store, sessionID string, behavior PermissionBehavior, toolName, input string) *PermissionRule {
	if store == nil {
		return nil
	}
	list := store.rulesByBehaviorInPriorityForSession(behavior, sessionID)
	for i := range list {
		r := list[i]
		if ruleMatchesForBehavior(r.Value, toolName, input, behavior) {
			return &r
		}
	}
	return nil
}

func ruleMatchesForBehavior(rule PermissionRuleValue, toolName, input string, behavior PermissionBehavior) bool {
	if !sameCanonicalTool(rule.ToolName, toolName) && !mcpServerScopeMatches(rule.ToolName, toolName) {
		return false
	}
	if isShellTool(toolName) && len(rule.CommandPrefix) > 0 {
		if behavior == BehaviorDeny || behavior == BehaviorAsk {
			return commandPrefixMatchesRestriction(rule.CommandPrefix, input)
		}
		return CommandPrefixMatches(rule.CommandPrefix, input)
	}
	if isShellTool(toolName) && strings.TrimSpace(rule.Command) != "" {
		literal := literalShellRule(rule.Command)
		if behavior == BehaviorDeny || behavior == BehaviorAsk {
			return shellRuleMatchesRestriction(literal, input)
		}
		return shellRuleMatchesClause(literal, input)
	}
	content := strings.TrimSpace(rule.RuleContent)
	if content == "" {
		return true
	}
	if isShellTool(toolName) {
		if behavior == BehaviorDeny || behavior == BehaviorAsk {
			return shellRuleMatchesRestriction(ParseShellRule(content), input)
		}
		return ShellRuleMatches(content, input)
	}
	if isWebFetchTool(toolName) {
		return WebFetchRuleMatches(content, input)
	}
	if hasUnescapedStar(content) {
		if isFilePathTool(toolName) {
			return pathWildcardMatch(content, input)
		}
		return wildcardMatch(content, input)
	}
	return input == content
}

// mcpServerScopeMatches reports whether ruleTool authorizes requestedTool via an
// MCP server-level scope. Accepted rule shapes:
//
//   - "mcp__<server>"          — whole server: matches "mcp__<server>" and "mcp__<server>__<tool>"
//   - "mcp__<server>*"         — trailing wildcard: matches anything starting with "mcp__<server>"
//
// Specific-tool rules ("mcp__<server>__<tool>") fall through to the canonical
// name comparison and are not handled here.
func mcpServerScopeMatches(ruleTool, requestedTool string) bool {
	rt := strings.TrimSpace(ruleTool)
	in := strings.TrimSpace(requestedTool)
	if rt == "" || in == "" {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(rt), "mcp__") {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(in), "mcp__") {
		return false
	}
	if strings.HasSuffix(rt, "*") {
		prefix := strings.TrimSuffix(rt, "*")
		return strings.HasPrefix(in, prefix)
	}
	if strings.Contains(rt[len("mcp__"):], "__") {
		return false
	}
	if strings.EqualFold(in, rt) {
		return true
	}
	return strings.HasPrefix(strings.ToLower(in), strings.ToLower(rt)+"__")
}

func sameCanonicalTool(a, b string) bool {
	ca := CanonicalToolName(a)
	cb := CanonicalToolName(b)
	if ca == "" || cb == "" {
		return false
	}
	return strings.EqualFold(ca, cb)
}

func isFilePathTool(toolName string) bool {
	switch CanonicalToolName(toolName) {
	case "Read", "Write", "Edit", "MultiEdit":
		return true
	default:
		return false
	}
}

func isShellTool(toolName string) bool {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "bash", "shell":
		return true
	default:
		return false
	}
}

var safeReadOnlyTools = map[string]struct{}{
	"read_file":        {},
	"list_directory":   {},
	"list_files":       {},
	"working_set_show": {},

	// skill reads one SKILL.md out of the skills this session already
	// discovered, and nothing else: it takes a skill name, not a path, and
	// resolves it against that catalog. Every request already carries the same
	// skills' names, descriptions and paths, and a read_file of the very same
	// file is unprompted, so a prompt here would gate the instructions while
	// their description flows freely.
	"skill": {},

	// retrieve_output only reads back the original output of a shell command
	// or MCP call that already ran under its own approval, from Forebrain Harness's own
	// compression store keyed by the id cited in the compression marker. It
	// reaches no filesystem path and executes nothing, so prompting for it
	// would gate the uncompressed copy of a result the user already saw.
	"retrieve_output": {},

	// Memory queries. The backend confines every path to the memories root
	// (no absolute paths, "..", dotfiles, or symlinks), so these read the
	// user's own memory store and nothing else — and the read path already
	// injects the memory summary into each request without asking. Prompting
	// per call would gate the details while the summary flows unprompted.
	// memories_add_ad_hoc_note writes and is deliberately absent.
	"memories_list":   {},
	"memories_read":   {},
	"memories_search": {},

	// Canonical policy names for Forebrain Harness's read-only tool implementations.
	"Read": {},
	"LS":   {},

	// The lsp tool asks the project's language servers about code the
	// session can already read. A call that names a file is evaluated as
	// Read (the permission layer maps it), so Read rules govern it; what
	// reaches this entry is only the file-less workspace symbol search.
	"LSP": {},
}

var safeInternalTools = map[string]struct{}{
	"intermediate_tool": {},
	"session_todo":      {},
	"working_set_pin":   {},
	"working_set_drop":  {},
	"subagent_run":      {},
	"subagent_fanout":   {},
	"subagent_send":     {},
	"subagent_continue": {},
	"subagent_close":    {},
	"subagent_wait":     {},
	"subagent_status":   {},
	"subagent_list":     {},
}

// IsSafeReadOnlyTool reports whether a tool only reads state and is safe to
// auto-allow under the current approval policy when no rule explicitly addresses
// it. Keep this list conservative: any tool that can mutate workspace files,
// memory, configuration, or session state must be excluded.
func IsSafeReadOnlyTool(toolName string) bool {
	name := strings.TrimSpace(toolName)
	if _, ok := safeReadOnlyTools[name]; ok {
		return true
	}
	if _, ok := safeReadOnlyTools[strings.ToLower(name)]; ok {
		return true
	}
	if _, ok := safeReadOnlyTools[CanonicalToolName(name)]; ok {
		return true
	}
	return false
}

func isSafeInternalTool(toolName string) bool {
	_, ok := safeInternalTools[strings.ToLower(strings.TrimSpace(toolName))]
	return ok
}

// evaluateShellCompoundAllow evaluates a shell command that contains compound
// separators (&&, ||, ;) clause-by-clause.  When every clause independently
// matches an allow rule the whole compound command is allowed.  Returns nil
// when the command is not compound or at least one clause is not covered by an
// allow rule, so the caller falls through to the default decision.
func (e *Engine) evaluateShellCompoundAllow(store *Store, sessionID, toolName, input string, mode PermissionMode) *Decision {
	clauses := SplitShellClauses(input)
	if len(clauses) <= 1 {
		return nil
	}
	bypassSandbox := true
	matchedAny := false
	var firstMatched *PermissionRule
	for _, clause := range clauses {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		// A known-safe clause carries itself: it is the same classification that
		// lets such a command run unasked on its own, so it needs no rule of its
		// own here either. Without this, one `cd <dir> &&` or `pwd &&` in front
		// of the real work means no remembered approval can ever cover the
		// command, and the user is asked again on every call.
		if ShellCommandIsKnownSafe(clause) {
			continue
		}
		match := firstAllowMatch(store, sessionID, toolName, clause)
		if match == nil {
			return nil
		}
		matchedAny = true
		if firstMatched == nil {
			copy := *match
			firstMatched = &copy
		}
		// Same rule as the single-command path: a repository-committed rule can
		// allow a clause but never lift the sandbox for it.
		if !grantsSandboxBypass(match) {
			bypassSandbox = false
		}
	}
	if !matchedAny {
		return nil
	}
	return &Decision{
		Behavior: BehaviorAllow, Mode: mode, Reason: "compound_shell_all_clauses_allowed",
		Matched: firstMatched, BypassSandbox: bypassSandbox,
	}
}

// shellNeedsApprovalDespiteDefault reports whether a shell command must not
// take the unattended default: either it matches a dangerous pattern, or no
// sandbox will contain it — under the conversation's own sandbox mode when it
// chose one.
func shellNeedsApprovalDespiteDefault(store *Store, sessionID, command string) bool {
	if store == nil || !store.sandboxAvailableForSession(sessionID) {
		return true
	}
	return AssessShellCommand(command).Dangerous != nil
}

// Classification is the canonical form of one command segment. It exists so
// output-filter matching and rewrite eligibility can key off a normalized
// "git status" instead of re-deriving it from raw text with a regex per filter.
type Classification struct {
	// Head is the bare command name with any directory prefix removed and
	// quoting resolved: "/usr/local/bin/git" and "'git'" both give "git".
	Head string
	// Sub is the first operand-like word after Head, lowercased, when the head
	// is a known subcommand-style tool (git, go, docker, cargo, kubectl, ...).
	// Empty otherwise. Flags are never reported as Sub.
	Sub string
	// Canonical is "head" or "head sub", the form filters should match on.
	Canonical string
	// EnvAssignments holds any leading VAR=value words that were stripped.
	EnvAssignments []string
	// Sudo reports that the segment was invoked through sudo/doas.
	Sudo bool
	// Wrapper names a package-runner prefix that was stripped to reach the real
	// command: npx, pnpm, yarn, bunx, uvx, poetry, pipenv, or "" when absent.
	Wrapper string
	// AssignmentOnly reports a segment that is nothing but VAR=value words, so
	// there is no command to classify at all.
	AssignmentOnly bool
	// ShellScript reports an explicit interpreter invocation such as
	// "bash -c ..." or "/usr/bin/env sh script.sh". Such segments carry an
	// opaque nested program and are never rewritten.
	ShellScript bool
	// Args holds the segment's words after env, sudo, and wrapper stripping,
	// starting with the head word as written.
	Args []string
}

// envPrefixKeywords are words that may follow leading assignments and still
// leave a real command behind (`FOO=1 command git status`).
var envPrefixKeywords = map[string]struct{}{
	"env":     {},
	"command": {},
	"exec":    {},
	"nohup":   {},
	"time":    {},
	"nice":    {},
	"ionice":  {},
	"stdbuf":  {},
}

// sudoHeads are privilege-elevation prefixes stripped before classification.
var sudoHeads = map[string]struct{}{
	"sudo": {},
	"doas": {},
}

// wrapperHeads are package-runner prefixes stripped to find the real tool.
//
// The value set lists the exec-style subcommands that hand off to a real
// binary, so the wrapper can be stripped to reach it. `run` is deliberately
// absent: `pnpm run build` executes a package *script*, not a binary named
// `build`, so unwrapping it would name a command that does not exist. For those
// the package manager stays the head and `run` becomes the subcommand.
var wrapperHeads = map[string]map[string]struct{}{
	"npx":    {},
	"pnpx":   {},
	"bunx":   {},
	"uvx":    {},
	"npm":    {"exec": {}},
	"pnpm":   {"exec": {}, "dlx": {}},
	"yarn":   {"dlx": {}, "exec": {}},
	"bun":    {"x": {}},
	"poetry": {},
	"pipenv": {},
	"rye":    {},
	"uv":     {"tool": {}},
}

// prefixOptionsWithValue lists options of the env-prefix keywords that consume
// the following word, so a value such as the "10" in "nice -n 10 cargo build"
// is never mistaken for the command head.
var prefixOptionsWithValue = map[string]struct{}{
	"-n": {}, // nice / ionice priority
	"-c": {}, // ionice class
	"-u": {}, // env unset
	"-o": {}, // time / stdbuf output
	"-i": {}, // stdbuf input buffering
	"-e": {}, // stdbuf stderr buffering
	"-f": {}, // time format
	"-p": {}, // ionice pid
}

// subcommandHeads are tools whose first operand meaningfully selects behavior,
// so the canonical form should include it.
var subcommandHeads = map[string]struct{}{
	"git":        {},
	"go":         {},
	"docker":     {},
	"podman":     {},
	"cargo":      {},
	"kubectl":    {},
	"helm":       {},
	"terraform":  {},
	"tofu":       {},
	"gh":         {},
	"glab":       {},
	"npm":        {},
	"pnpm":       {},
	"yarn":       {},
	"pip":        {},
	"pip3":       {},
	"apt":        {},
	"apt-get":    {},
	"brew":       {},
	"systemctl":  {},
	"mvn":        {},
	"gradle":     {},
	"dotnet":     {},
	"nx":         {},
	"bundle":     {},
	"composer":   {},
	"conda":      {},
	"aws":        {},
	"az":         {},
	"gcloud":     {},
	"pulumi":     {},
	"ansible":    {},
	"rclone":     {},
	"pod":        {},
	"mix":        {},
	"dbt":        {},
	"prisma":     {},
	"playwright": {},
	"liquibase":  {},
	"deno":       {},
	"bunx":       {},
	"cdk":        {},
	"act":        {},
	"kind":       {},
	"minikube":   {},
	"vagrant":    {},
	"ip":         {},
}

// shellInterpreters are interpreter heads whose arguments contain a nested
// program the classifier cannot see into.
var shellInterpreters = map[string]struct{}{
	"sh":   {},
	"bash": {},
	"zsh":  {},
	"ksh":  {},
	"dash": {},
	"fish": {},
	"csh":  {},
	"tcsh": {},
}

// gitGlobalOptsWithValue are git global options that consume the next word,
// so the following word must not be mistaken for the subcommand.
var gitGlobalOptsWithValue = map[string]struct{}{
	"-C":             {},
	"-c":             {},
	"--git-dir":      {},
	"--work-tree":    {},
	"--namespace":    {},
	"--exec-path":    {},
	"--config-env":   {},
	"--super-prefix": {},
}

// looksLikeWindowsPath reports whether w has a drive-letter prefix such as
// `C:\tools\git.exe`. Only such words get backslash-to-slash normalization, so
// POSIX escapes elsewhere keep their meaning.
func looksLikeWindowsPath(w string) bool {
	u := strings.Trim(w, `"'`)
	if len(u) < 3 || u[1] != ':' {
		return false
	}
	c := u[0]
	if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') {
		return false
	}
	return u[2] == '\\' || u[2] == '/'
}

// isAssignmentWord reports whether w has the form NAME=value.
func isAssignmentWord(w string) bool {
	if w == "" || !isIdentStart(w[0]) {
		return false
	}
	for i := 0; i < len(w); i++ {
		if w[i] == '=' {
			return i > 0
		}
		if !isIdentChar(w[i]) {
			return false
		}
	}
	return false
}

// firstWordBasename strips any directory prefix and quoting from a command
// word: "/usr/bin/env" gives "env", "./mvnw" gives "mvnw".
func firstWordBasename(w string) string {
	// A Windows path must have its separators normalized before unquoting,
	// because unquoteWord reads a backslash as an escape and would erase them.
	// The check is narrow so that a POSIX escape such as `a\ b` still unquotes
	// to a literal space instead of being split on a phantom separator.
	if looksLikeWindowsPath(w) {
		w = strings.ReplaceAll(w, `\`, "/")
	}
	u := unquoteWord(w)
	if u == "" {
		return ""
	}
	if strings.HasSuffix(u, "/") {
		u = strings.TrimRight(u, "/")
	}
	return path.Base(u)
}

// ClassifyCommand normalizes a single command segment. It never returns an
// error: an unrecognizable segment simply yields an empty Head.
//
// Multi-segment input is reduced to its first segment, so callers that hold a
// whole command line get the classification of the leading command rather than
// a confusing blend. Use SplitTopLevelWithSeparators first when every segment
// matters.
func ClassifyCommand(cmd string) Classification {
	var c Classification
	segs := SplitTopLevelWithSeparators(PeelBalancedWrapper(strings.TrimSpace(cmd)))
	if len(segs) == 0 {
		// Input consisting only of separators carries no command.
		return c
	}
	seg := PeelBalancedWrapper(segs[0].Text)
	words := SplitWords(seg)
	if len(words) == 0 {
		return c
	}

	i := 0
	// Leading VAR=value assignments.
	for i < len(words) && isAssignmentWord(words[i]) {
		c.EnvAssignments = append(c.EnvAssignments, words[i])
		i++
	}
	if i >= len(words) {
		c.AssignmentOnly = len(c.EnvAssignments) > 0
		return c
	}

	// Prefix keywords (env/command/exec/nohup/...), sudo, and package runners
	// may appear in any order and repeat, so loop until the head is real.
	for i < len(words) {
		base := strings.ToLower(firstWordBasename(words[i]))
		if _, ok := sudoHeads[base]; ok {
			c.Sudo = true
			i++
			// sudo's own options and inline assignments precede the command.
			for i < len(words) && (strings.HasPrefix(words[i], "-") || isAssignmentWord(words[i])) {
				if isAssignmentWord(words[i]) {
					c.EnvAssignments = append(c.EnvAssignments, words[i])
				}
				i++
			}
			continue
		}
		if _, ok := envPrefixKeywords[base]; ok {
			i++
			for i < len(words) && (strings.HasPrefix(words[i], "-") || isAssignmentWord(words[i])) {
				if isAssignmentWord(words[i]) {
					c.EnvAssignments = append(c.EnvAssignments, words[i])
					i++
					continue
				}
				// An option that takes a separate value consumes the next word,
				// which would otherwise be read as the command head.
				opt := unquoteWord(words[i])
				i++
				if _, takesValue := prefixOptionsWithValue[opt]; takesValue && i < len(words) {
					i++
				}
			}
			continue
		}
		if skip, ok := wrapperHeads[base]; ok && i+1 < len(words) {
			next := strings.ToLower(firstWordBasename(words[i+1]))
			if _, isSkip := skip[next]; isSkip {
				// "pnpm exec prettier" -> skip both; prettier is the head.
				if i+2 < len(words) {
					c.Wrapper = base
					i += 2
					continue
				}
				break
			}
			if len(skip) == 0 {
				// Bare runner (npx/uvx/bunx): the next word is the tool.
				c.Wrapper = base
				i++
				continue
			}
		}
		break
	}
	if i >= len(words) {
		c.AssignmentOnly = len(c.EnvAssignments) > 0 && c.Head == ""
		return c
	}

	c.Args = words[i:]
	c.Head = strings.ToLower(firstWordBasename(words[i]))
	if c.Head == "" {
		return c
	}
	if _, ok := shellInterpreters[c.Head]; ok {
		c.ShellScript = true
	}
	c.Canonical = c.Head

	// Subcommand extraction.
	if j := SubcommandIndex(c.Args); j >= 0 {
		c.Sub = strings.ToLower(unquoteWord(c.Args[j]))
		if c.Sub != "" {
			c.Canonical = c.Head + " " + c.Sub
		}
	}
	return c
}

// SubcommandIndex returns the index in args of the word that selects what a
// subcommand-style tool does — the "revert" in
// ["git", "--no-pager", "revert", "<sha>"] — or -1 when the head takes no
// subcommand or none is present.
//
// args starts at the head word, exactly as Classification.Args does. Callers
// get an index rather than the lowercased Sub so they can slice the words they
// were given and keep them verbatim.
func SubcommandIndex(args []string) int {
	if len(args) == 0 {
		return -1
	}
	head := strings.ToLower(firstWordBasename(args[0]))
	if _, ok := subcommandHeads[head]; !ok {
		return -1
	}
	start := 1
	if head == "git" {
		start += gitGlobalOptWords(args[1:])
	}
	for j := start; j < len(args); j++ {
		w := args[j]
		if strings.HasPrefix(w, "-") || isAssignmentWord(w) {
			continue
		}
		return j
	}
	return -1
}

// gitGlobalOptWords counts the leading words that are git global options,
// including any value they consume, so the real subcommand is the word that
// follows: "-C /repo --no-pager status" gives 3.
func gitGlobalOptWords(words []string) int {
	for i := 0; i < len(words); i++ {
		w := words[i]
		if !strings.HasPrefix(w, "-") {
			return i
		}
		// "-C/repo" and "--git-dir=x" carry their value inline.
		if strings.Contains(w, "=") {
			continue
		}
		if _, ok := gitGlobalOptsWithValue[w]; ok {
			i++ // consume the value
			continue
		}
		if len(w) > 2 && !strings.HasPrefix(w, "--") {
			// Clustered short option with inline value, e.g. "-C/repo".
			if _, ok := gitGlobalOptsWithValue[w[:2]]; ok {
				continue
			}
		}
	}
	return len(words)
}

// Project is the canonical target represented by a trust decision. Root is
// the repository root when the launch directory is version controlled; it is
// otherwise the canonical launch directory itself.
type Project struct {
	Root              string
	VersionControlled bool
}

// Level is the persisted trust decision for a project. The absence of a
// decision (LevelUnknown) is deliberately distinct from an explicit
// LevelUntrusted: a project nobody has judged yet is not the same as a project
// the user has judged and rejected, and the two select different defaults.
type Level string

const (
	LevelUnknown   Level = ""
	LevelTrusted   Level = "trusted"
	LevelUntrusted Level = "untrusted"
)

type state struct {
	TrustedDirectories   []string `json:"trusted_directories"`
	UntrustedDirectories []string `json:"untrusted_directories,omitempty"`
}

// Resolve returns a canonical project identity for cwd. An empty cwd means the
// process working directory. A .git directory and a worktree-style .git file
// both identify a version-controlled project.
func Resolve(cwd string) (Project, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return Project{}, err
		}
	}
	root, err := CanonicalPath(cwd)
	if err != nil {
		return Project{}, err
	}
	for dir := root; ; dir = filepath.Dir(dir) {
		marker := filepath.Join(dir, ".git")
		info, statErr := os.Lstat(marker)
		if statErr == nil && (info.IsDir() || info.Mode().IsRegular()) {
			return Project{Root: dir, VersionControlled: true}, nil
		}
		if statErr != nil && !os.IsNotExist(statErr) {
			return Project{}, fmt.Errorf("inspect project marker %s: %w", marker, statErr)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return Project{Root: root}, nil
}

// StatePath is the stable path used by older Forebrain Harness workspace-trust state.
func StatePath(home string) string {
	return filepath.Join(strings.TrimSpace(home), "state", "workspace_trust.json")
}

// IsTrusted reports whether project has an exact persisted trust decision.
// Missing state is untrusted; malformed or unreadable state returns an error
// so callers can fail closed instead of guessing.
func IsTrusted(home string, project Project) (bool, error) {
	level, err := TrustLevel(home, project)
	return level == LevelTrusted, err
}

// TrustLevel reports the persisted decision for project, distinguishing "never
// judged" from "explicitly rejected". An explicit rejection wins over a stale
// trusted entry for the same path, so a conflicting state file fails closed.
// Malformed or unreadable state returns an error rather than a guess.
func TrustLevel(home string, project Project) (Level, error) {
	root := strings.TrimSpace(project.Root)
	if root == "" {
		return LevelUnknown, nil
	}
	root, err := CanonicalPath(root)
	if err != nil {
		return LevelUnknown, err
	}
	st, err := load(home)
	if err != nil {
		return LevelUnknown, err
	}
	if containsProject(st.UntrustedDirectories, root) {
		return LevelUntrusted, nil
	}
	if containsProject(st.TrustedDirectories, root) {
		return LevelTrusted, nil
	}
	return LevelUnknown, nil
}

func containsProject(entries []string, root string) bool {
	for _, raw := range entries {
		candidate, err := CanonicalPath(raw)
		if err != nil {
			// Ignore a stale entry that no longer resolves; it cannot establish
			// a decision for a different path.
			continue
		}
		if candidate == root {
			return true
		}
	}
	return false
}

// MarkTrusted persists an exact, canonical trust target. Existing entries are
// retained, deduplicated, sorted, and written atomically.
func MarkTrusted(home string, project Project) error {
	root := strings.TrimSpace(project.Root)
	if root == "" {
		return nil
	}
	root, err := CanonicalPath(root)
	if err != nil {
		return err
	}
	st, err := load(home)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(st.TrustedDirectories)+1)
	for _, raw := range st.TrustedDirectories {
		path, err := CanonicalPath(raw)
		if err == nil && path != "" {
			seen[path] = struct{}{}
		}
	}
	seen[root] = struct{}{}
	st.TrustedDirectories = st.TrustedDirectories[:0]
	for path := range seen {
		st.TrustedDirectories = append(st.TrustedDirectories, path)
	}
	sort.Strings(st.TrustedDirectories)
	return save(home, st)
}

// CanonicalPath is how forebrain spells one directory: absolute, cleaned, and
// with every symlink on the way resolved. A path the user typed and a path a
// subsystem derived have to compare equal when they name the same place — a
// trust decision, a containment check and a project identity are all string
// comparisons — so every path that is stored or compared passes through here
// first. A path that does not exist keeps its absolute form: there is nothing
// to resolve, and refusing it is the caller's decision to make, not this one's.
func CanonicalPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty project path")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && strings.TrimSpace(resolved) != "" {
		return filepath.Clean(resolved), nil
	}
	return abs, nil
}

func load(home string) (state, error) {
	var st state
	raw, err := os.ReadFile(StatePath(home))
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return state{}, err
	}
	if len(raw) == 0 {
		return st, nil
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return state{}, err
	}
	return st, nil
}

func save(home string, st state) error {
	path := StatePath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".workspace_trust-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
