package process

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	state "github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// /connect is add-or-replace by provider+model identity, not an overwrite of
// the primary slot or of a same-provider entry's model. Connecting a second
// model of a provider the agent already carries adds its own entry and
// promotes it to primary; the provider's other models are preserved.
func TestExecuteConnectSecondModelSameProviderKeepsFirstEntry(t *testing.T) {
	home := t.TempDir()
	rt := Context{
		Home:       home,
		ConfigPath: home + "/forebrain.yaml",
		Config: appcfg.Root{
			Agents: appcfg.AgentsSection{
				Definitions: map[string]appcfg.AgentDefinition{
					"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{
						{Provider: "openai", Model: "gpt-old", APIKey: "${OPENAI_API_KEY}", BaseURL: "https://api.openai.com/v1"},
						{Provider: "anthropic", Model: "claude-old", APIKey: "${ANTHROPIC_API_KEY}", BaseURL: "https://api.anthropic.com"},
					}},
				},
			},
		},
	}

	if _, err := Execute(rt, Options{
		Provider: "anthropic",
		Model:    "claude-new",
		APIKey:   "sk-live-secret",
		BaseURL:  "https://api.anthropic.com",
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	cfg, err := appcfg.LoadPersisted(rt.ConfigPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	def := cfg.Agents.Definitions["main"]
	if len(def.LLMProviders) != 3 {
		t.Fatalf("provider count = %d, want 3 (openai and claude-old preserved, claude-new added)", len(def.LLMProviders))
	}
	// The configured provider+model must be primary (index 0) so /connect
	// takes effect for the next turn.
	primary := def.LLMProviders[0]
	if !strings.EqualFold(primary.Provider, "anthropic") || primary.Model != "claude-new" {
		t.Fatalf("primary = %+v, want anthropic/claude-new", primary)
	}
	// openai must still be present, unchanged.
	var openai *appcfg.AgentLLMProviderConfig
	for i := range def.LLMProviders {
		if strings.EqualFold(def.LLMProviders[i].Provider, "openai") {
			openai = &def.LLMProviders[i]
		}
	}
	if openai == nil {
		t.Fatalf("openai provider was lost; providers = %+v", def.LLMProviders)
	}
	if openai.Model != "gpt-old" || openai.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("openai entry changed: %+v", openai)
	}
	// The provider's previously configured model must not be overwritten.
	var claudeOld *appcfg.AgentLLMProviderConfig
	claudeNewCount := 0
	for i := range def.LLMProviders {
		entry := &def.LLMProviders[i]
		if strings.EqualFold(entry.Provider, "anthropic") && entry.Model == "claude-old" {
			claudeOld = entry
		}
		if entry.Model == "claude-new" {
			claudeNewCount++
		}
	}
	if claudeOld == nil {
		t.Fatalf("anthropic/claude-old was overwritten; providers = %+v", def.LLMProviders)
	}
	if claudeOld.BaseURL != "https://api.anthropic.com" {
		t.Fatalf("anthropic/claude-old lost its connection settings: %+v", claudeOld)
	}
	if claudeNewCount != 1 {
		t.Fatalf("claude-new entries = %d, want 1", claudeNewCount)
	}
}

// Connecting a provider the agent does not yet carry adds a new entry and makes
// it primary, leaving existing providers intact.
func TestExecuteConnectAddsNewProvider(t *testing.T) {
	home := t.TempDir()
	rt := Context{
		Home:       home,
		ConfigPath: home + "/forebrain.yaml",
		Config: appcfg.Root{
			Agents: appcfg.AgentsSection{
				Definitions: map[string]appcfg.AgentDefinition{
					"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{
						{Provider: "openai", Model: "gpt-old", APIKey: "${OPENAI_API_KEY}", BaseURL: "https://api.openai.com/v1"},
					}},
				},
			},
		},
	}

	if _, err := Execute(rt, Options{
		Provider: "anthropic",
		Model:    "claude-new",
		APIKey:   "sk-live-secret",
		BaseURL:  "https://api.anthropic.com",
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	cfg, err := appcfg.LoadPersisted(rt.ConfigPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	def := cfg.Agents.Definitions["main"]
	if len(def.LLMProviders) != 2 {
		t.Fatalf("provider count = %d, want 2 (openai kept, anthropic added)", len(def.LLMProviders))
	}
	if !strings.EqualFold(def.LLMProviders[0].Provider, "anthropic") || def.LLMProviders[0].Model != "claude-new" {
		t.Fatalf("primary = %+v, want anthropic/claude-new", def.LLMProviders[0])
	}
	if def.LLMProviders[1].Provider != "openai" || def.LLMProviders[1].Model != "gpt-old" {
		t.Fatalf("secondary = %+v, want openai/gpt-old preserved", def.LLMProviders[1])
	}
}

// Re-connecting the same provider+model only updates that entry in place (no
// reorder, no duplicate), so a credential refresh does not shuffle the list or
// grow it.
func TestExecuteConnectUpdatesCurrentPrimaryInPlace(t *testing.T) {
	home := t.TempDir()
	rt := Context{
		Home:       home,
		ConfigPath: home + "/forebrain.yaml",
		Config: appcfg.Root{
			Agents: appcfg.AgentsSection{
				Definitions: map[string]appcfg.AgentDefinition{
					"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{
						{Provider: "openai", Model: "gpt-old", APIKey: "${OPENAI_API_KEY}", BaseURL: "https://api.openai.com/v1"},
					}},
				},
			},
		},
	}

	if _, err := Execute(rt, Options{
		Provider: "openai",
		Model:    "gpt-old",
		APIKey:   "sk-live-secret",
		BaseURL:  "https://api.openai.com/v1",
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	cfg, err := appcfg.LoadPersisted(rt.ConfigPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	def := cfg.Agents.Definitions["main"]
	if len(def.LLMProviders) != 1 {
		t.Fatalf("provider count = %d, want 1 (updated in place)", len(def.LLMProviders))
	}
	if def.LLMProviders[0].Model != "gpt-old" {
		t.Fatalf("model = %q, want gpt-old", def.LLMProviders[0].Model)
	}
}

// Provider and model matching is case-insensitive, so reconnecting "OpenAI"
// with a case-variant of the entry's existing model updates that entry instead
// of adding a second one.
func TestExecuteConnectMatchesProviderCaseInsensitively(t *testing.T) {
	home := t.TempDir()
	rt := Context{
		Home:       home,
		ConfigPath: home + "/forebrain.yaml",
		Config: appcfg.Root{
			Agents: appcfg.AgentsSection{
				Definitions: map[string]appcfg.AgentDefinition{
					"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{
						{Provider: "anthropic", Model: "claude-old", APIKey: "${ANTHROPIC_API_KEY}", BaseURL: "https://api.anthropic.com"},
						{Provider: "openai", Model: "gpt-old", APIKey: "${OPENAI_API_KEY}", BaseURL: "https://api.openai.com/v1"},
					}},
				},
			},
		},
	}

	if _, err := Execute(rt, Options{
		Provider: "OpenAI",
		Model:    "GPT-OLD",
		APIKey:   "sk-live-secret",
		BaseURL:  "https://api.openai.com/v1",
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	cfg, err := appcfg.LoadPersisted(rt.ConfigPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	def := cfg.Agents.Definitions["main"]
	if len(def.LLMProviders) != 2 {
		t.Fatalf("provider count = %d, want 2 (openai matched, not duplicated)", len(def.LLMProviders))
	}
	if !strings.EqualFold(def.LLMProviders[0].Provider, "openai") || def.LLMProviders[0].Model != "GPT-OLD" {
		t.Fatalf("primary = %+v, want openai/GPT-OLD promoted", def.LLMProviders[0])
	}
}

// twoPrimaryConfig defines a main and a non-main primary agent, each loadable
// so Apply can actually rebuild the runner rather than failing on config.
func twoPrimaryConfig() *appcfg.Root {
	llm := []appcfg.AgentLLMProviderConfig{{
		Provider: "openai",
		Model:    "gpt-5",
		APIKey:   "test-key",
		BaseURL:  "https://api.openai.com/v1",
	}}
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main":   {LLMProviders: llm},
		"review": {Primary: true, LLMProviders: llm},
	}
	return cfg
}

func TestSwitchRebindsRunnerToTargetWorkspace(t *testing.T) {
	home := t.TempDir()
	cfg := twoPrimaryConfig()
	runner := &run.Runner{Deps: &run.Deps{Home: home, AppCfg: cfg}}

	active, err := SwitchAgent(AgentDeps{Home: home, Cfg: cfg, Runner: runner}, "review")
	if err != nil {
		t.Fatalf("switch: %v", err)
	}

	if active.ID != "review" {
		t.Fatalf("active = %q", active.ID)
	}
	want := filepath.Join(home, "workspaces", "review")
	if runner.WorkspaceRoot != want {
		t.Errorf("runner workspace = %q, want %q", runner.WorkspaceRoot, want)
	}
	if runner.AgentName != "review" {
		t.Errorf("runner agent = %q", runner.AgentName)
	}
}

// Approvals granted while one agent was active answer "may this agent do that
// in this workspace". Carrying them into another agent's workspace would widen
// its permissions beyond anything the user approved for it.
func TestApplyDropsRuntimeGrantsFromThePreviousAgent(t *testing.T) {
	home := t.TempDir()
	cfg := twoPrimaryConfig()
	runner := &run.Runner{Deps: &run.Deps{Home: home, AppCfg: cfg}}

	runner.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type:        safety.UpdateAddRules,
		Destination: safety.DestinationSession,
		SessionID:   "sess-1",
		Behavior:    safety.BehaviorAllow,
		Rules:       []safety.PermissionRuleValue{{ToolName: "bash", RuleContent: "rm -rf *"}},
	})
	before := runner.PermissionSnapshotForSession("sess-1")
	if len(before.Rules[safety.SourceSession]) == 0 {
		t.Fatal("precondition: expected a session-scoped grant")
	}

	if _, err := SwitchAgent(AgentDeps{Home: home, Cfg: cfg, Runner: runner}, "review"); err != nil {
		t.Fatalf("switch: %v", err)
	}

	after := runner.PermissionSnapshotForSession("sess-1")
	if len(after.Rules[safety.SourceSession]) != 0 {
		t.Fatalf("session grant survived the switch: %#v", after.Rules[safety.SourceSession])
	}
}

func TestApplyRunsSurfaceHookAndReportsMissingWorkspace(t *testing.T) {
	home := t.TempDir()
	cfg := twoPrimaryConfig()

	var applied string
	deps := AgentDeps{
		Home:      home,
		Cfg:       cfg,
		OnApplied: func(active appcfg.Summary) { applied = active.ID },
	}
	// A nil runner is legitimate before one is constructed: the active agent is
	// recorded and surface state still follows it.
	if err := ApplyAgent(deps, appcfg.Summary{ID: "review", WorkspaceRoot: filepath.Join(home, "workspaces", "review")}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied != "review" {
		t.Errorf("surface hook did not run, got %q", applied)
	}

	err := ApplyAgent(deps, appcfg.Summary{ID: "broken"})
	if err == nil || !strings.Contains(err.Error(), "workspace root") {
		t.Fatalf("expected a workspace-root error, got %v", err)
	}
}

func TestSwitchRejectsUnknownAgent(t *testing.T) {
	home := t.TempDir()
	cfg := twoPrimaryConfig()

	if _, err := SwitchAgent(AgentDeps{Home: home, Cfg: cfg}, "nope"); err == nil {
		t.Fatal("expected an error for an unknown primary agent")
	}
}

// The recorded active agent is what a freshly constructed runtime reads, so a
// switch must persist it even when this process has nothing bound yet.
func TestSwitchPersistsActiveAgent(t *testing.T) {
	home := t.TempDir()
	cfg := twoPrimaryConfig()

	if _, err := SwitchAgent(AgentDeps{Home: home, Cfg: cfg}, "review"); err != nil {
		t.Fatalf("switch: %v", err)
	}

	if got := appcfg.ActiveStateRoot(home, cfg); got != filepath.Join(home, "workspaces", "review") {
		t.Fatalf("active state root = %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, "state")); err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat state dir: %v", err)
	}
}

// Isolation must hold for anything the sandbox governs, not just the file
// tools: a shell command reaches the filesystem through the sandbox, so the
// other primaries' workspaces have to be on its deny lists.
func TestSwitchDeniesPeerWorkspacesInTheSandbox(t *testing.T) {
	home := t.TempDir()
	cfg := twoPrimaryConfig()
	runner := &run.Runner{Deps: &run.Deps{Home: home, AppCfg: cfg}}
	sandbox := safety.NewManager()

	if _, err := SwitchAgent(AgentDeps{Home: home, Cfg: cfg, Runner: runner, Sandbox: sandbox}, "review"); err != nil {
		t.Fatalf("switch: %v", err)
	}

	peer := filepath.Join(home, "workspace") // the main agent's workspace
	fs := sandbox.RuntimeConfig().Filesystem
	if !containsPath(fs.DenyRead, peer) {
		t.Errorf("peer workspace %q not denied for read: %v", peer, fs.DenyRead)
	}
	if !containsPath(fs.DenyWrite, peer) {
		t.Errorf("peer workspace %q not denied for write: %v", peer, fs.DenyWrite)
	}
	// The active agent's own workspace must stay reachable.
	active := filepath.Join(home, "workspaces", "review")
	if containsPath(fs.DenyRead, active) || containsPath(fs.DenyWrite, active) {
		t.Errorf("active workspace %q was denied: read=%v write=%v", active, fs.DenyRead, fs.DenyWrite)
	}
}

// A denied peer must not be reachable through an allow entry either, including
// one nested inside it.
func TestSetIsolatedPeerRootsRemovesOverlappingAllows(t *testing.T) {
	home := t.TempDir()
	peer := filepath.Join(home, "workspace")
	sandbox := safety.NewManager()
	sandbox.UpdateConfig(nil, nil, safety.Snapshot{}, home, home, t.TempDir(),
		[]string{peer, filepath.Join(peer, "nested"), filepath.Join(home, "workspaces", "review")})

	sandbox.SetIsolatedPeerRoots([]string{peer})

	fs := sandbox.RuntimeConfig().Filesystem
	for _, blocked := range []string{peer, filepath.Join(peer, "nested")} {
		if containsPath(fs.AllowWrite, blocked) || containsPath(fs.AllowRead, blocked) {
			t.Errorf("allow entry %q survived the denial: write=%v read=%v", blocked, fs.AllowWrite, fs.AllowRead)
		}
	}
}

func containsPath(list []string, want string) bool {
	want = filepath.Clean(want)
	for _, item := range list {
		if filepath.Clean(strings.TrimSpace(item)) == want {
			return true
		}
	}
	return false
}

// Persisted approvals belong to the agent that was granted them. Switching must
// leave the previous agent's settings behind rather than evaluate them against
// another agent's workspace.
func TestPersistedRulesDoNotCrossPrimaryAgents(t *testing.T) {
	home := t.TempDir()
	cfg := twoPrimaryConfig()
	runner := &run.Runner{Deps: &run.Deps{Home: home, AppCfg: cfg}}

	// Grant a persistent rule while "main" is active.
	runner.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type:        safety.UpdateAddRules,
		Destination: safety.DestinationLocalSettings,
		Behavior:    safety.BehaviorAllow,
		Rules:       []safety.PermissionRuleValue{{ToolName: "Bash", RuleContent: "npm run build"}},
	})
	mainRules := runner.PermissionSnapshot().Rules[safety.SourceLocalSettings][safety.BehaviorAllow]
	if len(mainRules) == 0 {
		t.Fatal("precondition: expected a persisted rule for the main agent")
	}
	// It landed in the main agent's workspace, not a shared home directory.
	if _, err := os.Stat(filepath.Join(home, "workspace", "state", "permissions", "local_settings.json")); err != nil {
		t.Fatalf("rule not stored under the main agent's workspace: %v", err)
	}

	if _, err := SwitchAgent(AgentDeps{Home: home, Cfg: cfg, Runner: runner}, "review"); err != nil {
		t.Fatalf("switch: %v", err)
	}

	after := runner.PermissionSnapshot().Rules[safety.SourceLocalSettings][safety.BehaviorAllow]
	if len(after) != 0 {
		t.Fatalf("main agent's persisted rule followed the switch: %#v", after)
	}

	// Switching back restores that agent's own rules from its own workspace.
	if _, err := SwitchAgent(AgentDeps{Home: home, Cfg: cfg, Runner: runner}, "main"); err != nil {
		t.Fatalf("switch back: %v", err)
	}
	restored := runner.PermissionSnapshot().Rules[safety.SourceLocalSettings][safety.BehaviorAllow]
	if len(restored) != len(mainRules) {
		t.Fatalf("main agent's rules were not restored: %#v", restored)
	}
}

// TestSwitchRebindsTenantScopedStores covers the database half of primary-agent
// isolation. Sessions and memories live in one state database keyed by agent
// id, and every surface shares these two stores, so a switch that moved the
// workspace but left the stores bound to the previous agent would keep writing
// this agent's conversations into — and reading its memories out of — the
// previous tenant's rows.
func TestSwitchRebindsTenantScopedStores(t *testing.T) {
	home := t.TempDir()
	cfg := twoPrimaryConfig()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	sessions := state.NewSessionStore(db, "main")
	mems := memory.NewStore(db, "main")
	runner := &run.Runner{Deps: &run.Deps{Home: home, AppCfg: cfg, SessionStore: sessions, MemoryStore: mems}}

	if _, err := SwitchAgent(AgentDeps{Home: home, Cfg: cfg, Runner: runner}, "review"); err != nil {
		t.Fatalf("switch: %v", err)
	}

	if got := sessions.AgentID(); got != "review" {
		t.Errorf("session store bound to %q, want review", got)
	}
	if got := mems.AgentID(); got != "review" {
		t.Errorf("memory store bound to %q, want review", got)
	}
}

func newAgentTargetRuntime(t *testing.T) Context {
	t.Helper()
	home := t.TempDir()
	return Context{
		Home:       home,
		ConfigPath: home + "/forebrain.yaml",
		Config: appcfg.Root{
			Agents: appcfg.AgentsSection{
				Definitions: map[string]appcfg.AgentDefinition{
					"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{{
						Provider: "openai", Model: "gpt-old",
						APIKey: "${OPENAI_API_KEY}", BaseURL: "https://api.openai.com/v1",
					}}},
					"review": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{{
						Provider: "openai", Model: "review-old",
						APIKey: "${OPENAI_API_KEY}", BaseURL: "https://api.openai.com/v1",
					}}},
				},
			},
		},
	}
}

// /connect configures the primary agent the session is actually running. Writing
// main instead would save a provider nothing in that session reads.
func TestExecuteConfiguresNamedAgentOnly(t *testing.T) {
	rt := newAgentTargetRuntime(t)

	rep, err := Execute(rt, Options{
		AgentName: "review",
		Provider:  "anthropic",
		Model:     "claude-opus-4-7",
		APIKey:    "sk-live-secret",
		BaseURL:   "https://api.anthropic.com",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if rep.AgentName != "review" {
		t.Fatalf("report agent = %q, want review", rep.AgentName)
	}

	cfg, err := appcfg.LoadPersisted(rt.ConfigPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	review := appcfg.PrimaryLLM(cfg.Agents.Definitions["review"])
	if review == nil || review.Model != "claude-opus-4-7" || review.Provider != "anthropic" {
		t.Fatalf("review agent not configured: %+v", review)
	}
	main := appcfg.PrimaryLLM(cfg.Agents.Definitions["main"])
	if main == nil || main.Model != "gpt-old" {
		t.Fatalf("main agent was rewritten by a setup aimed at review: %+v", main)
	}
}

// An empty agent name stays main, so onboarding and `forebrain setup` are unchanged.
func TestExecuteDefaultsToMainAgent(t *testing.T) {
	rt := newAgentTargetRuntime(t)

	rep, err := Execute(rt, Options{
		Provider: "openai",
		Model:    "gpt-new",
		APIKey:   "sk-live-secret",
		BaseURL:  "https://api.openai.com/v1",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if rep.AgentName != "main" {
		t.Fatalf("report agent = %q, want main", rep.AgentName)
	}

	cfg, err := appcfg.LoadPersisted(rt.ConfigPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if main := appcfg.PrimaryLLM(cfg.Agents.Definitions["main"]); main == nil || main.Model != "gpt-new" {
		t.Fatalf("main agent not configured: %+v", main)
	}
	if review := appcfg.PrimaryLLM(cfg.Agents.Definitions["review"]); review == nil || review.Model != "review-old" {
		t.Fatalf("review agent was rewritten: %+v", review)
	}
}

// A name no definition carries is a mistake, not an instruction to create an
// agent: silently minting one would leave the user configuring something the
// session never runs.
func TestExecuteRejectsUnknownAgent(t *testing.T) {
	rt := newAgentTargetRuntime(t)

	_, err := Execute(rt, Options{
		AgentName: "typo",
		Provider:  "openai",
		Model:     "gpt-new",
		APIKey:    "sk-live-secret",
		BaseURL:   "https://api.openai.com/v1",
	})
	if err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("err = %v, want a not-defined error", err)
	}
}
