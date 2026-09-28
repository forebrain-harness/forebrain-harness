package safety

import (
	"slices"
	"testing"
)

const permissionTestSessionID = "session-1"

// sandboxedStore returns a store that knows a sandbox will contain shell
// commands. That is what justifies running an unmatched shell command without
// asking, so a test relying on that default has to say so.
func sandboxedStore() *Store {
	s := NewStore()
	s.SetSandboxAvailable(true)
	return s
}

func TestEngineDenyWinsOverAllow(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationLocalSettings,
		Behavior:    BehaviorAllow,
		Rules:       []PermissionRuleValue{{ToolName: "Bash", RuleContent: "git commit:*"}},
	})
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationSession,
		SessionID:   permissionTestSessionID,
		Behavior:    BehaviorDeny,
		Rules:       []PermissionRuleValue{{ToolName: "Bash", RuleContent: `git commit -m "x"`}},
	})
	d := NewEngine().EvaluateForSession(s, permissionTestSessionID, "Bash", `git commit -m "x"`)
	if d.Behavior != BehaviorDeny {
		t.Fatalf("expected deny, got %s (%s)", d.Behavior, d.Reason)
	}
}

func TestStoreRejectsCommandPrefixForNonShellTool(t *testing.T) {
	t.Parallel()

	store := NewStore()
	store.AddRule(SourceLocalSettings, BehaviorAllow, PermissionRuleValue{
		ToolName: "Write", CommandPrefix: []string{"/tmp/approved"}, BypassSandbox: true,
	})
	if rules := store.Rules(SourceLocalSettings, BehaviorAllow); len(rules) != 0 {
		t.Fatalf("non-shell command-prefix rule was retained: %+v", rules)
	}
}

func TestEngineNativeCommandPrefixMatchesTokensWithoutWidening(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{
		Type: UpdateAddRules, Destination: DestinationLocalSettings, Behavior: BehaviorAllow,
		Rules: []PermissionRuleValue{{
			ToolName: "Bash", CommandPrefix: []string{"npm", "run", "build"}, BypassSandbox: true,
		}},
	})
	for _, command := range []string{"npm run build", "npm run build -- --watch"} {
		decision := NewEngine().Evaluate(s, "Bash", command)
		if decision.Matched == nil || !decision.BypassSandbox {
			t.Fatalf("expected %q to match native prefix: %+v", command, decision)
		}
	}
	for _, command := range []string{
		"npm run test",
		"npm run buildx",
		"npm run build && rm -rf /tmp/x",
		`npm run build "$(touch /tmp/not-authorized)"`,
		"npm run build \"`touch /tmp/not-authorized`\"",
	} {
		decision := NewEngine().Evaluate(s, "Bash", command)
		if decision.Matched != nil || decision.BypassSandbox {
			t.Fatalf("native prefix widened to %q: %+v", command, decision)
		}
	}
}

func TestEngineNativeCommandPrefixPreservesEmptyAndWhitespaceArguments(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{
		Type: UpdateAddRules, Destination: DestinationLocalSettings, Behavior: BehaviorAllow,
		Rules: []PermissionRuleValue{{
			ToolName: "Bash", CommandPrefix: []string{"printf", "%s", ""}, BypassSandbox: true,
		}},
	})
	if decision := NewEngine().Evaluate(s, "Bash", `printf '%s' ''`); decision.Matched == nil {
		t.Fatalf("empty argument did not match: %+v", decision)
	}
	if decision := NewEngine().Evaluate(s, "Bash", `printf '%s' value`); decision.Matched != nil {
		t.Fatalf("empty argument prefix widened to a non-empty argument: %+v", decision)
	}

	s.AddRule(SourceLocalSettings, BehaviorAllow, PermissionRuleValue{
		ToolName: "Bash", CommandPrefix: []string{"printf", " %s "}, BypassSandbox: true,
	})
	if decision := NewEngine().Evaluate(s, "Bash", `printf ' %s '`); decision.Matched == nil {
		t.Fatalf("whitespace argument did not match exactly: %+v", decision)
	}
	if decision := NewEngine().Evaluate(s, "Bash", `printf %s`); decision.Matched != nil {
		t.Fatalf("whitespace argument was trimmed and widened: %+v", decision)
	}
}

func TestEngineNativeCommandPrefixPreservesEnvironmentAssignments(t *testing.T) {
	suggestion := BuildToolApprovalSuggestion("Bash", map[string]any{
		"command": "PATH=/approved/bin NODE_ENV=production npm run build",
	})
	amendment := proposedExecPolicyAmendment(suggestion)
	want := []string{"PATH=/approved/bin", "NODE_ENV=production", "npm", "run", "build"}
	if !slices.Equal(amendment, want) {
		t.Fatalf("amendment=%q want=%q", amendment, want)
	}

	s := NewStore()
	s.AddRule(SourceLocalSettings, BehaviorAllow, PermissionRuleValue{
		ToolName: "Bash", CommandPrefix: amendment, BypassSandbox: true,
	})
	if decision := NewEngine().Evaluate(s, "Bash", "PATH=/approved/bin NODE_ENV=production npm run build"); decision.Matched == nil {
		t.Fatalf("approved command did not match: %+v", decision)
	}
	for _, command := range []string{
		"npm run build",
		"PATH=/other/bin NODE_ENV=production npm run build",
		"PATH=/approved/bin NODE_ENV=development npm run build",
	} {
		if decision := NewEngine().Evaluate(s, "Bash", command); decision.Matched != nil {
			t.Fatalf("environment prefix widened to %q: %+v", command, decision)
		}
	}
}

func TestEngineNativeCommandPolicyConflictUsesMostRestrictiveDecision(t *testing.T) {
	s := sandboxedStore()
	s.AddRule(SourceProjectSettings, BehaviorAllow, PermissionRuleValue{ToolName: "Bash", CommandPrefix: []string{"git"}, BypassSandbox: true})
	s.AddRule(SourceProjectSettings, BehaviorAsk, PermissionRuleValue{ToolName: "Bash", CommandPrefix: []string{"git", "push"}})
	s.AddRule(SourceLocalSettings, BehaviorDeny, PermissionRuleValue{ToolName: "Bash", CommandPrefix: []string{"git", "push", "--force"}})

	if decision := NewEngine().Evaluate(s, "Bash", "git status"); decision.Behavior != BehaviorAllow {
		t.Fatalf("git status decision=%+v", decision)
	}
	if decision := NewEngine().Evaluate(s, "Bash", "git push origin main"); decision.Behavior != BehaviorAsk {
		t.Fatalf("git push decision=%+v", decision)
	}
	if decision := NewEngine().Evaluate(s, "Bash", "git push --force origin main"); decision.Behavior != BehaviorDeny {
		t.Fatalf("forced push decision=%+v", decision)
	}
}

func TestEngineNativeDenyPrefixCannotBeEvadedByShellSyntax(t *testing.T) {
	s := NewStore()
	s.AddRule(SourceLocalSettings, BehaviorDeny, PermissionRuleValue{
		ToolName: "Bash", CommandPrefix: []string{"rm"},
	})

	for _, command := range []string{
		"rm target > /dev/null",
		`rm "$(printf target)"`,
		"echo safe && rm target",
		`echo safe && rm "$(printf target)"`,
	} {
		decision := NewEngine().Evaluate(s, "Bash", command)
		if decision.Behavior != BehaviorDeny {
			t.Fatalf("deny prefix was evaded by %q: %+v", command, decision)
		}
	}
}

func TestEngineNativeAskPrefixCannotBeEvadedByRedirection(t *testing.T) {
	s := NewStore()
	s.AddRule(SourceLocalSettings, BehaviorAsk, PermissionRuleValue{
		ToolName: "Bash", CommandPrefix: []string{"deploy"},
	})
	decision := NewEngine().Evaluate(s, "Bash", "deploy production > deploy.log")
	if decision.Behavior != BehaviorAsk {
		t.Fatalf("ask prefix was evaded: %+v", decision)
	}
}

func TestEngineNeverConvertsAskToDeny(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationLocalSettings,
		Behavior:    BehaviorAsk,
		Rules:       []PermissionRuleValue{{ToolName: "Bash", RuleContent: "npm run:*"}},
	})
	d := NewEngine().Evaluate(s, "Bash", "npm run build")
	if d.Behavior != BehaviorAsk {
		t.Fatalf("expected ask, got %s", d.Behavior)
	}
	ApplyUpdate(s, PermissionUpdate{Type: UpdateSetMode, Mode: ModeNever})
	d = NewEngine().Evaluate(s, "Bash", "npm run build")
	if d.Behavior != BehaviorDeny {
		t.Fatalf("expected never to convert ask to deny, got %s", d.Behavior)
	}
}

func TestEngineOnRequestRunsOrdinaryShellInSandbox(t *testing.T) {
	s := sandboxedStore()
	d := NewEngine().Evaluate(s, "Bash", "mkdir tmpdir")
	if d.Behavior != BehaviorAllow {
		t.Fatalf("expected sandboxed execution without an approval prompt, got %s", d.Behavior)
	}
}

func TestEngineNeverRunsOrdinaryShellInSandbox(t *testing.T) {
	s := sandboxedStore()
	ApplyUpdate(s, PermissionUpdate{Type: UpdateSetMode, Mode: ModeNever})
	d := NewEngine().Evaluate(s, "Bash", "mkdir tmpdir")
	if d.Behavior != BehaviorAllow {
		t.Fatalf("expected never policy to rely on sandbox enforcement, got %s", d.Behavior)
	}
}

func TestEngineNeverAllowsReadOnlyTools(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{Type: UpdateSetMode, Mode: ModeNever})
	for _, tool := range []string{"read_file", "list_directory"} {
		d := NewEngine().Evaluate(s, tool, "")
		if d.Behavior != BehaviorAllow {
			t.Fatalf("never policy should allow %s, got %s (%s)", tool, d.Behavior, d.Reason)
		}
	}
}

func TestEngineNeverStillHonorsDenyRule(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{Type: UpdateSetMode, Mode: ModeNever})
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationSession,
		SessionID:   permissionTestSessionID,
		Behavior:    BehaviorDeny,
		Rules:       []PermissionRuleValue{{ToolName: "read_file", RuleContent: "/secret/*"}},
	})
	d := NewEngine().EvaluateForSession(s, permissionTestSessionID, "read_file", "/secret/keys")
	if d.Behavior != BehaviorDeny {
		t.Fatalf("never policy must honor deny rule, got %s (%s)", d.Behavior, d.Reason)
	}
}

func TestEngineDefaultModeAllowsReadOnlyTools(t *testing.T) {
	s := NewStore()
	for _, tool := range []string{"read_file", "list_directory"} {
		d := NewEngine().Evaluate(s, tool, "")
		if d.Behavior != BehaviorAllow {
			t.Fatalf("default mode should allow %s, got %s (%s)", tool, d.Behavior, d.Reason)
		}
		if d.Reason != "safe_readonly_default_allow" {
			t.Fatalf("expected safe_readonly_default_allow reason for %s, got %s", tool, d.Reason)
		}
	}
}

func TestEngineDefaultModeReadOnlyRespectsAskRule(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationSession,
		SessionID:   permissionTestSessionID,
		Behavior:    BehaviorAsk,
		Rules:       []PermissionRuleValue{{ToolName: "read_file", RuleContent: "/etc/*"}},
	})
	d := NewEngine().EvaluateForSession(s, permissionTestSessionID, "read_file", "/etc/passwd")
	if d.Behavior != BehaviorAsk {
		t.Fatalf("ask rule must override readonly fastpath, got %s (%s)", d.Behavior, d.Reason)
	}
}

func TestEngineExplainIncludesMatchedRules(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationSession,
		SessionID:   permissionTestSessionID,
		Behavior:    BehaviorAllow,
		Rules:       []PermissionRuleValue{{ToolName: "Bash", RuleContent: "npm run:*"}},
	})
	ex := NewEngine().ExplainForSession(s, permissionTestSessionID, "Bash", "npm run build")
	if ex.Decision.Behavior != BehaviorAllow {
		t.Fatalf("expected allow decision, got %s", ex.Decision.Behavior)
	}
	matched := 0
	for _, r := range ex.Rules {
		if r.Matched {
			matched++
		}
	}
	if matched == 0 {
		t.Fatalf("expected at least one matched rule in explain result")
	}
}

func TestEngineSourcePriorityUsesHighestPriorityRule(t *testing.T) {
	s := NewStore()
	for _, src := range []PermissionSource{
		SourceProjectSettings,
		SourceLocalSettings,
		SourceSession,
	} {
		s.AddRule(src, BehaviorAllow, PermissionRuleValue{
			ToolName:    "Bash",
			RuleContent: "git status",
		})
	}
	d := NewEngine().Evaluate(s, "Bash", "git status")
	if d.Behavior != BehaviorAllow {
		t.Fatalf("expected allow, got %s (%s)", d.Behavior, d.Reason)
	}
	if d.Matched == nil {
		t.Fatalf("expected matched rule")
	}
	// Session rules only apply to their own session, so with no session in
	// play the strongest remaining source wins.
	if d.Matched.Source != SourceLocalSettings {
		t.Fatalf("expected the localSettings rule to win, got %s", d.Matched.Source)
	}
}

func TestMCPToolRuleDoesNotApplyToSiblingTool(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationLocalSettings,
		Behavior:    BehaviorAllow,
		Rules: []PermissionRuleValue{{
			ToolName:    "mcp__code_review_graph__get_minimal_context_tool",
			RuleContent: "*",
		}},
	})
	d1 := NewEngine().Evaluate(s, "mcp__code_review_graph__get_minimal_context_tool", `{"repo_root":"/tmp/repo"}`)
	if d1.Behavior != BehaviorAllow {
		t.Fatalf("expected current mcp tool allowed, got %+v", d1)
	}
	d2 := NewEngine().Evaluate(s, "mcp__code_review_graph__list_flows_tool", `{}`)
	if d2.Behavior == BehaviorAllow {
		t.Fatalf("expected sibling mcp tool NOT auto-allowed, got %+v", d2)
	}
}

func TestMCPServerScopeRuleAllowsAllToolsForServer(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationLocalSettings,
		Behavior:    BehaviorAllow,
		Rules: []PermissionRuleValue{{
			ToolName:    "mcp__github",
			RuleContent: "",
		}},
	})
	for _, tool := range []string{
		"mcp__github",
		"mcp__github__list_issues",
		"mcp__github__create_pr",
	} {
		d := NewEngine().Evaluate(s, tool, "")
		if d.Behavior != BehaviorAllow {
			t.Fatalf("server scope must allow %s, got %s (%s)", tool, d.Behavior, d.Reason)
		}
	}
	d := NewEngine().Evaluate(s, "mcp__gitlab__list_issues", "")
	if d.Behavior == BehaviorAllow {
		t.Fatalf("server scope must not leak across servers, got %+v", d)
	}
}

func TestMCPServerScopeTrailingWildcardAllowsServerPrefix(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationLocalSettings,
		Behavior:    BehaviorAllow,
		Rules: []PermissionRuleValue{{
			ToolName:    "mcp__github*",
			RuleContent: "",
		}},
	})
	for _, tool := range []string{
		"mcp__github",
		"mcp__github__list",
		"mcp__github_enterprise__list",
	} {
		d := NewEngine().Evaluate(s, tool, "")
		if d.Behavior != BehaviorAllow {
			t.Fatalf("trailing wildcard must allow %s, got %s", tool, d.Behavior)
		}
	}
	d := NewEngine().Evaluate(s, "mcp__gitlab__list", "")
	if d.Behavior == BehaviorAllow {
		t.Fatalf("trailing wildcard must not match other servers, got %+v", d)
	}
}

func TestMCPSpecificToolRuleStillRequiresExactToolMatch(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationLocalSettings,
		Behavior:    BehaviorAllow,
		Rules: []PermissionRuleValue{{
			ToolName:    "mcp__github__list_issues",
			RuleContent: "*",
		}},
	})
	d := NewEngine().Evaluate(s, "mcp__github__list_issues", "{}")
	if d.Behavior != BehaviorAllow {
		t.Fatalf("expected explicit tool rule to allow, got %+v", d)
	}
	d = NewEngine().Evaluate(s, "mcp__github__create_pr", "{}")
	if d.Behavior == BehaviorAllow {
		t.Fatalf("specific tool rule must not leak to sibling, got %+v", d)
	}
}

func TestEngineCompoundShellAllowEachClauseMustMatch(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationLocalSettings,
		Behavior:    BehaviorAllow,
		Rules: []PermissionRuleValue{
			{ToolName: "Bash", RuleContent: "go test:*"},
			{ToolName: "Bash", RuleContent: "go build:*"},
		},
	})
	// An unmatched clause does not imply host execution; on-request runs the
	// command under the configured sandbox.
	d := NewEngine().Evaluate(s, "Bash", "cd /project && go test ./...")
	if d.Behavior != BehaviorAllow || d.BypassSandbox {
		t.Fatalf("expected sandboxed allow when not all clauses are covered, got %+v", d)
	}
}

func TestEngineCompoundShellAllowAllClausesCovered(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationLocalSettings,
		Behavior:    BehaviorAllow,
		Rules: []PermissionRuleValue{
			{ToolName: "Bash", RuleContent: "go test:*", BypassSandbox: true},
		},
	})
	// Single clause - should still ask (no allow rule matches "go test ./..." exactly
	// unless the prefix rule is checked through ShellRuleMatches which splits internally).
	// Actually ShellRuleMatches splits internally, so "go test ./..." should match "go test:*".
	d := NewEngine().Evaluate(s, "Bash", "go test ./...")
	if d.Behavior != BehaviorAllow {
		t.Fatalf("expected allow for single clause matching prefix rule, got %s (%s)", d.Behavior, d.Reason)
	}
	if !d.BypassSandbox {
		t.Fatalf("expected single-clause allow rule to propagate sandbox bypass: %+v", d)
	}

	// Compound with both clauses matching the same prefix rule.
	d2 := NewEngine().Evaluate(s, "Bash", "go test ./... && go test ./pkg/...")
	if d2.Behavior != BehaviorAllow {
		t.Fatalf("expected allow when all clauses match same rule, got %s (%s)", d2.Behavior, d2.Reason)
	}
	if !d2.BypassSandbox {
		t.Fatalf("expected compound allow to preserve sandbox bypass when every matched rule allows it: %+v", d2)
	}
	if d2.Matched == nil {
		t.Fatalf("expected compound allow to report that policy rules participated: %+v", d2)
	}
}

func TestEngineCompoundShellAllowDifferentRules(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationLocalSettings,
		Behavior:    BehaviorAllow,
		Rules: []PermissionRuleValue{
			{ToolName: "Bash", RuleContent: "go test:*"},
			{ToolName: "Bash", RuleContent: "go build:*"},
		},
	})
	// Each clause matches a different allow rule.
	d := NewEngine().Evaluate(s, "Bash", "go build ./... && go test ./...")
	if d.Behavior != BehaviorAllow {
		t.Fatalf("expected allow when each clause matches some rule, got %s (%s)", d.Behavior, d.Reason)
	}
}

func TestEngineCompoundShellDenyStillCheckedFirst(t *testing.T) {
	s := NewStore()
	// The deny path is evaluated before allow rules.
	// A deny rule targeting an exact command takes priority even for compound commands.
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationSession,
		SessionID:   permissionTestSessionID,
		Behavior:    BehaviorDeny,
		Rules: []PermissionRuleValue{
			{ToolName: "Bash", RuleContent: "go test ./... && rm -rf /tmp/x"},
		},
	})
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationLocalSettings,
		Behavior:    BehaviorAllow,
		Rules: []PermissionRuleValue{
			{ToolName: "Bash", RuleContent: "go test:*"},
		},
	})
	d := NewEngine().EvaluateForSession(s, permissionTestSessionID, "Bash", "go test ./... && rm -rf /tmp/x")
	if d.Behavior != BehaviorDeny {
		t.Fatalf("expected deny when exact deny rule matches full command, got %s (%s)", d.Behavior, d.Reason)
	}
}

func TestEngineCompoundShellNotCompound(t *testing.T) {
	s := sandboxedStore()
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationLocalSettings,
		Behavior:    BehaviorAllow,
		Rules: []PermissionRuleValue{
			{ToolName: "Bash", RuleContent: "go test:*"},
		},
	})
	// Single clause - normal evaluation should work.
	d := NewEngine().Evaluate(s, "Bash", "mkdir tmpdir")
	if d.Behavior != BehaviorAllow || d.BypassSandbox {
		t.Fatalf("expected sandboxed allow for non-matching single clause, got %+v", d)
	}
}

func TestEngineCompoundShellEmptyClause(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationLocalSettings,
		Behavior:    BehaviorAllow,
		Rules: []PermissionRuleValue{
			{ToolName: "Bash", RuleContent: "go test:*"},
		},
	})
	// Trailing && produces empty clause which should be skipped.
	d := NewEngine().Evaluate(s, "Bash", "go test ./... && ")
	if d.Behavior != BehaviorAllow {
		t.Fatalf("expected allow when non-empty clauses all match, got %s (%s)", d.Behavior, d.Reason)
	}
}

// Memory queries are confined to the memories root by the backend and their
// summary is injected into every request anyway, so they run unprompted like
// the other read-only tools. The writer stays gated, and an explicit ask/deny
// rule still outranks the default because both are evaluated before it.
func TestMemoryQueriesAreSafeReadOnlyButTheWriterIsNot(t *testing.T) {
	engine := NewEngine()
	for _, tool := range []string{"memories_list", "memories_read", "memories_search"} {
		d := engine.EvaluateForSession(NewStore(), "s1", tool, `{"path":"MEMORY.md"}`)
		if d.Behavior != BehaviorAllow || d.Reason != "safe_readonly_default_allow" {
			t.Fatalf("%s should run unprompted, got %s (%s)", tool, d.Behavior, d.Reason)
		}
	}
	writer := engine.EvaluateForSession(NewStore(), "s1", "memories_add_ad_hoc_note", `{"filename":"n.md"}`)
	if writer.Behavior != BehaviorAsk {
		t.Fatalf("memory writes must stay gated, got %s (%s)", writer.Behavior, writer.Reason)
	}
	for _, behavior := range []PermissionBehavior{BehaviorAsk, BehaviorDeny} {
		s := NewStore()
		ApplyUpdate(s, PermissionUpdate{
			Type:        UpdateAddRules,
			Destination: DestinationLocalSettings,
			Behavior:    behavior,
			Rules:       []PermissionRuleValue{{ToolName: "memories_read"}},
		})
		if d := engine.EvaluateForSession(s, "s1", "memories_read", `{"path":"MEMORY.md"}`); d.Behavior != behavior {
			t.Fatalf("configured %s rule must outrank the default allow, got %s (%s)", behavior, d.Behavior, d.Reason)
		}
	}
}

// retrieve_output reads back the uncompressed original of a result the caller
// already received, out of Forebrain Harness's own store. Prompting for it would gate a
// value the user has already seen, so it runs unprompted — while an explicit
// rule still outranks the default.
func TestRetrieveOutputIsSafeReadOnly(t *testing.T) {
	engine := NewEngine()
	d := engine.EvaluateForSession(NewStore(), "s1", "retrieve_output", `{"id":7}`)
	if d.Behavior != BehaviorAllow || d.Reason != "safe_readonly_default_allow" {
		t.Fatalf("retrieve_output should run unprompted, got %s (%s)", d.Behavior, d.Reason)
	}
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationLocalSettings,
		Behavior:    BehaviorAsk,
		Rules:       []PermissionRuleValue{{ToolName: "retrieve_output"}},
	})
	if d := engine.EvaluateForSession(s, "s1", "retrieve_output", `{"id":7}`); d.Behavior != BehaviorAsk {
		t.Fatalf("configured ask rule must outrank the default allow, got %s (%s)", d.Behavior, d.Reason)
	}
}

func TestShellIsNotSafeReadOnly(t *testing.T) {
	if IsSafeReadOnlyTool("shell") {
		t.Fatal("shell must be governed by command policy and sandbox profile")
	}
	if IsSafeReadOnlyTool("readonly_shell") {
		t.Fatal("removed readonly_shell must not be considered safe")
	}
}

// Deny is checked across every source before ask, and ask before allow, so the
// source order never lets a weaker layer's allow survive a stronger layer's
// deny — nor the reverse. This is the property that actually bounds what a
// committed project policy can do to an operator's own approvals.
func TestEngineDenyWinsAcrossSourcesInBothDirections(t *testing.T) {
	cases := []struct {
		name        string
		denySource  PermissionSource
		allowSource PermissionSource
	}{
		{"project denies what local allows", SourceProjectSettings, SourceLocalSettings},
		{"local denies what project allows", SourceLocalSettings, SourceProjectSettings},
		{"project denies what the session allowed", SourceProjectSettings, SourceSession},
		{"local denies what the session allowed", SourceLocalSettings, SourceSession},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStore()
			allow := PermissionRuleValue{ToolName: "Bash", RuleContent: "rm -rf build"}
			if tc.allowSource == SourceSession {
				allow.SessionID = "sess-1"
			}
			s.AddRule(tc.allowSource, BehaviorAllow, allow)
			deny := PermissionRuleValue{ToolName: "Bash", RuleContent: "rm -rf build"}
			if tc.denySource == SourceSession {
				deny.SessionID = "sess-1"
			}
			s.AddRule(tc.denySource, BehaviorDeny, deny)

			d := NewEngine().EvaluateForSession(s, "sess-1", "Bash", "rm -rf build")

			if d.Behavior != BehaviorDeny {
				t.Fatalf("behavior = %s (%s), want deny", d.Behavior, d.Reason)
			}
			if d.BypassSandbox {
				t.Error("a denied command must not report sandbox bypass")
			}
		})
	}
}

// A repository can allow a command but must never lift the sandbox for it:
// otherwise any repo the operator trusted could escalate itself out of the
// sandbox simply by committing a rule.
func TestEngineProjectRuleCannotGrantSandboxBypass(t *testing.T) {
	s := NewStore()
	s.AddRule(SourceProjectSettings, BehaviorAllow, PermissionRuleValue{
		ToolName: "Bash", RuleContent: "npm run build", BypassSandbox: true,
	})
	s.AddRule(SourceLocalSettings, BehaviorAllow, PermissionRuleValue{
		ToolName: "Bash", RuleContent: "npm run build", BypassSandbox: false,
	})

	d := NewEngine().Evaluate(s, "Bash", "npm run build")

	if d.Behavior != BehaviorAllow {
		t.Fatalf("behavior = %s (%s)", d.Behavior, d.Reason)
	}
	if d.BypassSandbox {
		t.Errorf("a project rule granted sandbox bypass: %#v", d.Matched)
	}

	// The same grant from the operator's own agent settings is honoured.
	s.AddRule(SourceLocalSettings, BehaviorAllow, PermissionRuleValue{
		ToolName: "Bash", RuleContent: "npm run build", BypassSandbox: true,
	})
	if d := NewEngine().Evaluate(s, "Bash", "npm run build"); !d.BypassSandbox {
		t.Errorf("the agent's own bypass grant was dropped: %#v", d.Matched)
	}
}

// A repository can restrict what happens in it, for everyone working in it,
// but it can never decide that something is routine on someone else's machine.
func TestEngineProjectRulesNarrowButNeverPermit(t *testing.T) {
	t.Run("deny is honoured", func(t *testing.T) {
		s := NewStore()
		s.AddRule(SourceProjectSettings, BehaviorDeny, PermissionRuleValue{
			ToolName: "Bash", RuleContent: "terraform apply",
		})
		if d := NewEngine().Evaluate(s, "Bash", "terraform apply"); d.Behavior != BehaviorDeny {
			t.Fatalf("decision = %+v, want deny", d)
		}
	})

	t.Run("ask is honoured", func(t *testing.T) {
		s := NewStore()
		s.AddRule(SourceProjectSettings, BehaviorAsk, PermissionRuleValue{
			ToolName: "Bash", RuleContent: "git push",
		})
		if d := NewEngine().Evaluate(s, "Bash", "git push"); d.Behavior != BehaviorAsk {
			t.Fatalf("decision = %+v, want ask", d)
		}
	})

	t.Run("allow is ignored", func(t *testing.T) {
		s := NewStore()
		s.AddRule(SourceProjectSettings, BehaviorAllow, PermissionRuleValue{
			ToolName: "apply_patch", RuleContent: "src/**",
		})
		d := NewEngine().Evaluate(s, "apply_patch", "src/main.go")
		if d.Behavior == BehaviorAllow && d.Matched != nil &&
			d.Matched.Source == SourceProjectSettings {
			t.Fatalf("a project rule permitted an action: %+v", d)
		}
	})
}

// The widening whitelist is what every sandbox-facing consumer consults, so it
// is pinned directly: sources the operator controls may widen, anything else
// may only narrow. A source added later must be listed deliberately.
func TestMayWidenSandboxIsAWhitelist(t *testing.T) {
	for _, src := range []PermissionSource{SourceSession, SourceLocalSettings} {
		if !MayWidenSandbox(src) {
			t.Errorf("%s should be allowed to widen the sandbox", src)
		}
	}
	for _, src := range []PermissionSource{SourceProjectSettings, PermissionSource("somethingNew"), ""} {
		if MayWidenSandbox(src) {
			t.Errorf("%s must not be allowed to widen the sandbox", src)
		}
	}
}

// Writing an allow rule to a source that cannot permit is rejected outright,
// rather than stored and quietly ignored later.
func TestApplyUpdateRejectsAllowForNonPermittingSource(t *testing.T) {
	s := NewStore()
	rule := PermissionRuleValue{ToolName: "Bash", RuleContent: "npm test"}

	for _, typ := range []PermissionUpdateType{UpdateAddRules, UpdateReplaceRules} {
		ApplyUpdate(s, PermissionUpdate{
			Type: typ, Destination: DestinationProjectSettings,
			Behavior: BehaviorAllow, Rules: []PermissionRuleValue{rule},
		})
		if got := s.Rules(SourceProjectSettings, BehaviorAllow); len(got) != 0 {
			t.Fatalf("%s stored an allow rule for projectSettings: %#v", typ, got)
		}
	}

	// The same destination still accepts restrictions.
	ApplyUpdate(s, PermissionUpdate{
		Type: UpdateAddRules, Destination: DestinationProjectSettings,
		Behavior: BehaviorDeny, Rules: []PermissionRuleValue{rule},
	})
	if got := s.Rules(SourceProjectSettings, BehaviorDeny); len(got) != 1 {
		t.Fatalf("a project deny rule was rejected: %#v", got)
	}
}

// The behaviour of a tool call that no rule matches is decided by a chain of
// fall-through branches. Nothing covered the chain as a whole, which is how a
// gap in the `never` policy went unnoticed, so the matrix is pinned here.
func TestDefaultsWithNoRules(t *testing.T) {
	cases := []struct {
		policy  AskForApproval
		tool    string
		command string
		sandbox bool
		want    PermissionBehavior
		wantWhy string
	}{
		// A sandbox is what justifies running an unmatched shell command
		// unattended, so with one present the ordinary command just runs.
		{ApprovalOnRequest, "Bash", "mkdir tmpdir", true, BehaviorAllow, "shell_default_allow"},
		{ApprovalNever, "Bash", "mkdir tmpdir", true, BehaviorAllow, "shell_default_allow"},
		{ApprovalGranular, "Bash", "mkdir tmpdir", true, BehaviorAllow, "shell_default_allow"},
		// unless-trusted is the one policy that still asks about ordinary shell.
		{ApprovalUnlessTrusted, "Bash", "mkdir tmpdir", true, BehaviorAsk, "default_ask"},

		// Without a sandbox the justification is gone: ask, or refuse outright
		// when the operator has turned prompting off.
		{ApprovalOnRequest, "Bash", "mkdir tmpdir", false, BehaviorAsk, "shell_unsafe_default_ask"},
		{ApprovalNever, "Bash", "mkdir tmpdir", false, BehaviorDeny, "shell_unsafe_without_prompt_deny"},

		// A dangerous command never takes the unattended path, sandbox or not.
		{ApprovalOnRequest, "Bash", "rm -rf /", true, BehaviorAsk, "shell_unsafe_default_ask"},
		{ApprovalNever, "Bash", "rm -rf /", true, BehaviorDeny, "shell_unsafe_without_prompt_deny"},

		// Read-only tools are allowed under every policy.
		{ApprovalNever, "Read", "", false, BehaviorAllow, "safe_readonly_default_allow"},
		{ApprovalUnlessTrusted, "Read", "", false, BehaviorAllow, "safe_readonly_default_allow"},

		// Mutating and unknown tools are gated by approval, since the sandbox
		// permits writes to the workspace and so cannot stand in for consent.
		{ApprovalOnRequest, "Write", "/tmp/x", true, BehaviorAsk, "default_ask"},
		{ApprovalNever, "Write", "/tmp/x", true, BehaviorDeny, "never_policy_default_deny"},
		{ApprovalOnRequest, "some_mcp__tool", "", true, BehaviorAsk, "default_ask"},
	}
	for _, tc := range cases {
		name := string(tc.policy) + "/" + tc.tool + "/sandbox=" + map[bool]string{true: "yes", false: "no"}[tc.sandbox]
		t.Run(name, func(t *testing.T) {
			s := NewStore()
			s.SetApprovalPolicy(ApprovalPolicy{Mode: tc.policy})
			s.SetSandboxAvailable(tc.sandbox)

			d := NewEngine().Evaluate(s, tc.tool, tc.command)

			if d.Behavior != tc.want || d.Reason != tc.wantWhy {
				t.Fatalf("got %s (%s), want %s (%s)", d.Behavior, d.Reason, tc.want, tc.wantWhy)
			}
			if d.BypassSandbox {
				t.Error("a default decision must never lift the sandbox")
			}
		})
	}
}
