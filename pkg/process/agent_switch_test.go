package process

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	state "github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

// executeStoredConfig reads the configuration a setup or /connect Execute
// left behind: the composed stored source over that home (plan 188 W9 — the
// agents section, not the file, is the write's destination).
//
// It opens the database the write opened — the one the configuration file names
// — rather than a scratch one of the test's own: under a MySQL or PostgreSQL
// server the write and the read have to meet in the same database, which is what
// production does too.
func executeStoredConfig(t *testing.T, rt Context) appcfg.Root {
	t.Helper()
	databaseURL, err := appcfg.FileDatabaseURL(rt.ConfigPath)
	if err != nil {
		t.Fatalf("read database.url: %v", err)
	}
	db, err := state.OpenStateFromHome(context.Background(), rt.Home, databaseURL)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer db.Close()
	env := &Environment{Root: rt.Home, ConfigPath: rt.ConfigPath, SQL: db}
	cfg, err := env.ComposedPersistedConfig(context.Background())
	if err != nil {
		t.Fatalf("read stored configuration: %v", err)
	}
	return cfg
}

// /connect stores one row per configured model: a second model under the same
// provider becomes its own row beside the first, and it never takes the default
// away from the model the agent already runs on.
func TestExecuteSecondModelOfAProviderKeepsTheFirst(t *testing.T) {
	rt := newAgentTargetRuntime(t)

	if _, err := Execute(rt, Options{
		Provider: "openai",
		Model:    "gpt-old",
		APIKey:   "sk-live-secret",
		BaseURL:  "https://api.openai.com/v1",
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	rep, err := Execute(rt, Options{
		Provider: "openai",
		Model:    "gpt-new",
		APIKey:   "sk-live-secret",
		BaseURL:  "https://api.openai.com/v1",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if rep.Provider != "openai" || rep.Model != "gpt-old" {
		t.Fatalf("report = %s/%s, want the agent's unchanged default", rep.Provider, rep.Model)
	}

	cfg := executeStoredConfig(t, rt)
	if len(cfg.LLMModels) != 2 {
		t.Fatalf("model rows = %+v, want one per configured model", cfg.LLMModels)
	}
	for _, row := range cfg.LLMModels {
		if row.APIKey != "sk-live-secret" {
			t.Fatalf("row %+v does not carry the key", row)
		}
	}
	if entry := appcfg.EffectiveDefaultLLMFor(&cfg, "main"); entry == nil || entry.Model != "gpt-old" {
		t.Fatalf("main default = %+v, want the pre-existing model kept", entry)
	}
}

// Re-connecting the same model only refreshes its row: no duplicate, and the
// row id the session may already name stays the same.
func TestExecuteConnectUpdatesCurrentPrimaryInPlace(t *testing.T) {
	rt := newAgentTargetRuntime(t)
	before := seedModelRowForTest(t, rt, "openai", "gpt-old", "https://api.openai.com/v1", "sk-old")

	rep, err := Execute(rt, Options{
		Provider: "openai",
		Model:    "gpt-old",
		APIKey:   "sk-live-secret",
		BaseURL:  "https://api.openai.com/v1",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if rep.ModelID != before.ID {
		t.Fatalf("model id = %q, want the row it updated %q", rep.ModelID, before.ID)
	}

	rows := modelRowsForTest(t, rt)
	if len(rows) != 1 {
		t.Fatalf("model rows = %+v, want exactly one", rows)
	}
	if rows[0].ID != before.ID || rows[0].APIKey != "sk-live-secret" {
		t.Fatalf("row = %+v, want the same row with the new key", rows[0])
	}
}

// The provider half of the identity is case-insensitive, so reconnecting
// "OpenAI" updates the stored openai row instead of adding a second one.
func TestExecuteConnectMatchesProviderCaseInsensitively(t *testing.T) {
	rt := newAgentTargetRuntime(t)
	seedModelRowForTest(t, rt, "openai", "gpt-old", "https://api.openai.com/v1", "sk-old")

	if _, err := Execute(rt, Options{
		Provider: "OpenAI",
		Model:    "gpt-old",
		APIKey:   "sk-live-secret",
		BaseURL:  "https://api.openai.com/v1",
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	rows := modelRowsForTest(t, rt)
	if len(rows) != 1 {
		t.Fatalf("model rows = %+v, want the case-variant to match the stored row", rows)
	}
	if rows[0].Provider != "openai" || rows[0].APIKey != "sk-live-secret" {
		t.Fatalf("row = %+v, want the stored row with the new key", rows[0])
	}
}

// modelRowsForTest reads the model table of the database the setup flow wrote.
func modelRowsForTest(t *testing.T, rt Context) []state.LLMModel {
	t.Helper()
	db, err := openStateForWrite(rt)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer db.Close()
	rows, err := (&state.LLMModelStore{DB: db}).List(context.Background())
	if err != nil {
		t.Fatalf("list the model table: %v", err)
	}
	return rows
}

// seedModelRowForTest stores one model row before the flow runs — what a
// previous /connect leaves behind, written the way that flow writes it.
func seedModelRowForTest(t *testing.T, rt Context, provider, model, baseURL, key string) state.LLMModel {
	t.Helper()
	db, err := openStateForWrite(rt)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer db.Close()
	id, err := UpsertLLMModel(context.Background(), db, LLMModelInput{
		Provider: provider, Model: model, BaseURL: baseURL, APIKey: &key,
	})
	if err != nil {
		t.Fatalf("store the seed row: %v", err)
	}
	row, err := (&state.LLMModelStore{DB: db}).Get(context.Background(), id)
	if err != nil {
		t.Fatalf("read the seed row: %v", err)
	}
	return row
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

// The primary agent's own MCP layer changes with the agent, so a switch has
// to resolve the list again: replaying the previous agent's cache would keep
// serving servers the new agent never declared.
func TestAgentSwitchReResolvesTheMCPList(t *testing.T) {
	home := t.TempDir()
	cfg := twoPrimaryConfig()
	var resolves int
	var resolvedAgent string
	runner := &run.Runner{Deps: &run.Deps{
		Home:   home,
		AppCfg: cfg,
		MCPResolve: func(agentName, agentWorkspace string, launch safety.ProjectContext) (run.MCPServersResolution, error) {
			resolves++
			resolvedAgent = agentName
			return run.MCPServersResolution{Servers: []appcfg.MCPServerConfig{{
				Name:      agentName + "-only",
				Transport: "stdio",
				// A path that does not exist: the switch must not depend on a
				// server that answers, only on the list it resolved.
				Command: "/nonexistent/" + agentName + "-mcp",
			}}}, nil
		},
	}}

	if _, err := SwitchAgent(AgentDeps{Home: home, Cfg: cfg, Runner: runner}, "review"); err != nil {
		t.Fatalf("switch: %v", err)
	}

	if resolves != 1 {
		t.Fatalf("mcp resolve calls = %d, want 1 (the switch resolves the new agent's list once)", resolves)
	}
	if resolvedAgent != "review" {
		t.Fatalf("resolved agent = %q, want the agent that was switched to", resolvedAgent)
	}
	if len(runner.MCPServers) != 1 || runner.MCPServers[0].Name != "review-only" {
		t.Fatalf("runner MCP list = %+v, want the new primary agent's layer", runner.MCPServers)
	}
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

// newAgentTargetRuntime is a home whose stored agents section carries a main
// and a review definition, each with a pre-table model list of its own — the
// shape an install has before the model table took over.
func newAgentTargetRuntime(t *testing.T) Context {
	t.Helper()
	home := t.TempDir()
	rt := Context{Home: home, ConfigPath: home + "/forebrain.yaml"}
	seedAgentsSectionForTest(t, rt, `{"definitions":{
		"main":{"primary":true,
			"llm_providers":[{"provider":"openai","model":"gpt-old","api_key":"${OPENAI_API_KEY}","base_url":"https://api.openai.com/v1"}]},
		"review":{"primary":true,
			"llm_providers":[{"provider":"openai","model":"review-old","api_key":"${OPENAI_API_KEY}","base_url":"https://api.openai.com/v1"}]}}}`)
	cfg, err := StartupConfig(context.Background(), home)
	if err != nil {
		t.Fatalf("read the stored configuration: %v", err)
	}
	rt.Config = cfg
	return rt
}

// seedAgentsSectionForTest writes the agents section straight into the state
// database: the store's write path validates candidates, and these tests need a
// pre-table list exactly the way an earlier binary left it.
func seedAgentsSectionForTest(t *testing.T, rt Context, doc string) {
	t.Helper()
	db, err := openStateForWrite(rt)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), dbx.Rebind(dbx.DialectOf(db),
		`INSERT INTO fb_config (section, value_json, updated_at_ms) VALUES ('agents', ?, 0)`), doc); err != nil {
		t.Fatalf("seed the agents section: %v", err)
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
	if rep.Model != "review-old" {
		t.Fatalf("report model = %q, want the review agent's unchanged default", rep.Model)
	}

	cfg := executeStoredConfig(t, rt)
	if len(cfg.LLMModels) != 1 {
		t.Fatalf("model rows = %+v, want only the one just configured", cfg.LLMModels)
	}
	if row := cfg.LLMModels[0]; row.Provider != "anthropic" || row.Model != "claude-opus-4-7" || row.APIKey != "sk-live-secret" {
		t.Fatalf("row = %+v, want the review agent's new model", row)
	}
	if review := appcfg.EffectiveDefaultLLMFor(&cfg, "review"); review == nil || review.Model != "review-old" {
		t.Fatalf("review default changed by an append-only write: %+v", review)
	}
	main := appcfg.EffectiveDefaultLLMFor(&cfg, "main")
	if main == nil || main.Model != "gpt-old" {
		t.Fatalf("main agent was rewritten by a setup aimed at review: %+v", main)
	}
	stored := executeStoredConfigDefinition(t, rt, "main")
	if stored.DefaultModel != "" {
		t.Fatalf("main default_model = %q, want it untouched", stored.DefaultModel)
	}
}

// executeStoredConfigDefinition reads one definition out of the stored agents
// section — the bytes, not the composed view.
func executeStoredConfigDefinition(t *testing.T, rt Context, agent string) appcfg.AgentDefinition {
	t.Helper()
	db, err := openStateForWrite(rt)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer db.Close()
	var raw string
	if err := db.QueryRowContext(context.Background(),
		dbx.Rebind(dbx.DialectOf(db), `SELECT value_json FROM fb_config WHERE section = 'agents'`)).Scan(&raw); err != nil {
		t.Fatalf("read the agents section: %v", err)
	}
	var agents appcfg.AgentsSection
	if err := json.Unmarshal([]byte(raw), &agents); err != nil {
		t.Fatalf("read the agents section: %v", err)
	}
	return agents.Definitions[agent]
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
	if rep.ModelID == "" {
		t.Fatal("the report carries no model id")
	}

	cfg := executeStoredConfig(t, rt)
	if len(cfg.LLMModels) != 1 {
		t.Fatalf("model rows = %+v, want only the one just configured", cfg.LLMModels)
	}
	if row := cfg.LLMModels[0]; row.Provider != "openai" || row.Model != "gpt-new" {
		t.Fatalf("row = %+v, want main's new model", row)
	}
	if review := appcfg.EffectiveDefaultLLMFor(&cfg, "review"); review == nil || review.Model != "review-old" {
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
