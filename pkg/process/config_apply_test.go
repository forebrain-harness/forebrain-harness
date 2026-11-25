package process

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// P3-4 requires a broken-config test proving the live runtime is left
// completely unchanged, with no half-applied state. applyComposedConfigLocked
// swaps Runner.Config()/MCPServers/YOLO *before* it calls Runner.Load, so a
// config that parses and passes the startup check but fails to load is exactly
// the case that can strand the runner describing a configuration nothing else
// is using. The rollback in config_apply.go had no coverage before this.
func TestApplyComposedConfigRollsBackEveryFieldWhenRunnerLoadFails(t *testing.T) {
	t.Setenv("FOREBRAIN_ROLLBACK_TEST_KEY", "test-key")
	home := t.TempDir()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	const good = "agents:\n" +
		"  defaults:\n" +
		"    mcp_servers:\n" +
		"      - name: keep-me\n" +
		"        command: /usr/bin/true\n" +
		"  definitions:\n" +
		"    main:\n" +
		"      llm_providers:\n" +
		"        - provider: openai\n" +
		"          model: gpt-old\n" +
		"          api_key: ${FOREBRAIN_ROLLBACK_TEST_KEY}\n" +
		"          base_url: http://localhost:0/v1\n"
	if err := os.WriteFile(cfgPath, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadDocumentForTest(cfgPath)
	if err != nil {
		t.Fatalf("loadDocumentForTest(good): %v", err)
	}
	// The model table is state, not a file section (plan 315).
	cfg.LLMModels = modelRowsFromEntriesForTest(appcfg.AgentLLMProviderConfig{
		Provider: "openai", Model: "gpt-old",
		APIKey: "${FOREBRAIN_ROLLBACK_TEST_KEY}", BaseURL: "http://localhost:0/v1",
	})
	live := &cfg

	runner := &run.Runner{Deps: (&run.Deps{Home: home, AgentName: "main"}).WithConfig(live)}
	runner.MCPServers = live.Agents.Defaults.MCPServers
	if err := runner.Load(); err != nil {
		t.Fatalf("Runner.Load(good): %v", err)
	}
	env := (&Environment{Deps: &run.Deps{}, Root: home, ConfigPath: cfgPath, Runner: runner}).WithDepsConfig(live)

	// Refused by the loader: a provider the runtime cannot route never becomes
	// the running model, so the application fails after the pointer has already
	// been swapped and only the rollback can leave the live runtime intact. The
	// refusal comes from Runner.LoadConfig and not from the read — a
	// save-or-load-side guard would fail before anything was swapped, which is
	// the state this test exists to rule out.
	brokenCfg := cfg
	// The pinned row keeps its identity but loses the key it ran on: the
	// startup check only asks whether a model is configured, and building the
	// client is what refuses it ("llm api_key is required"), which is the
	// failure this test needs.
	brokenCfg.LLMModels = []appcfg.LLMModelConfig{{
		ID: "llm-1", Provider: "openai", Model: "gpt-old",
		BaseURL: "http://localhost:0/v1", APIPath: "/chat/completions",
	}}
	brokenDefs := make(map[string]appcfg.AgentDefinition, len(cfg.Agents.Definitions))
	for name, def := range cfg.Agents.Definitions {
		brokenDefs[name] = def
	}
	brokenDefs["main"] = appcfg.AgentDefinition{Primary: true, DefaultModel: "llm-1"}
	brokenCfg.Agents.Definitions = brokenDefs

	if err := env.applyComposedConfigLocked(brokenCfg); err == nil {
		t.Fatal("applying an unloadable config = nil, want an error")
	}
	// The snapshot did reach the kernel — it passed the startup check and only
	// the runner refused it — so the assertions below are about the rollback,
	// not about an early return.
	if got := env.RawConfigValue().LLMModels; len(got) != 1 || got[0].APIKey != "" {
		t.Fatalf("RawConfig models = %+v, want the applied snapshot; the failure came before the runner was reached", got)
	}

	if env.deps().Config() != live {
		t.Fatalf("Environment.Deps.Config() was replaced despite the failed load")
	}
	if env.Runner.Config() != live {
		t.Fatalf("Runner.AppCfg = %p, want it restored to the live config %p", env.Runner.Config(), live)
	}
	if got := env.Runner.Config().LLMModels; len(got) != 1 || got[0].Model != "gpt-old" {
		t.Fatalf("Runner.Config() models = %+v, want the pre-apply gpt-old row", got)
	}
	if len(env.Runner.MCPServers) != 1 || env.Runner.MCPServers[0].Name != "keep-me" {
		t.Fatalf("Runner.MCPServers = %+v, want the pre-apply entry restored", env.Runner.MCPServers)
	}
	// The rollback re-Loads the old config, so the runner must still be usable.
	// The agent is published once MCP startup for the restored generation has
	// settled — that half of a Load runs in the background so a first frame does
	// not wait for a server — so the barrier is what this waits for before
	// asking for the agent.
	_ = env.Runner.MCPStartup().Wait(context.Background())
	if env.Runner.Agent() == nil {
		t.Fatal("Runner has no agent after rollback; the runner was left unloaded")
	}
}

// TestRefreshSandboxRuntimeReappliesFilesystemPolicyToInProcessTools pins that
// the environment's sandbox refresh reaches the in-process file tools and not
// only the subprocess manager.
//
// The TUI always did both halves; the environment did only the manager, and the
// gateway calls the environment (api_extra.go, after an approval widens the
// sandbox). A refresh that stops at the manager leaves Read/Write governed by
// the pre-change policy while shell commands already honour the new one, which
// is the two surfaces disagreeing about the same setting.
func TestRefreshSandboxRuntimeReappliesFilesystemPolicyToInProcessTools(t *testing.T) {
	t.Setenv("FOREBRAIN_SANDBOX_REFRESH_TEST_KEY", "test-key")
	home := t.TempDir()
	first := filepath.Join(home, "first")
	second := filepath.Join(home, "second")
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := appcfg.Root{LLMModels: modelRowsFromEntriesForTest(appcfg.AgentLLMProviderConfig{
		Provider: "openai", Model: "gpt-x",
		APIKey: "${FOREBRAIN_SANDBOX_REFRESH_TEST_KEY}", BaseURL: "http://localhost:0/v1",
	}),
		SandboxMode: appcfg.SandboxModeWorkspaceWrite,
		Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
			"main": {Primary: true, DefaultModel: "llm-1"},
		}},
	}
	cfg.SandboxWorkspaceWrite.WritableRoots = []string{first}

	runner := &run.Runner{Deps: (&run.Deps{Home: home, AgentName: "main", ProjectRoot: home}).WithConfig(&cfg)}
	if err := runner.Load(); err != nil {
		t.Fatalf("Runner.Load: %v", err)
	}
	env := (&Environment{Deps: &run.Deps{Home: home}, Root: home, Runner: runner}).WithDepsConfig(&cfg)

	env.RefreshSandboxRuntime()
	if !slices.Contains(runner.Tools().PermissionRoots(safety.FileSystemAccessWrite), first) {
		t.Fatalf("write roots = %v, want the configured %s", runner.Tools().PermissionRoots(safety.FileSystemAccessWrite), first)
	}

	cfg.SandboxWorkspaceWrite.WritableRoots = []string{second}
	env.RefreshSandboxRuntime()
	got := runner.Tools().PermissionRoots(safety.FileSystemAccessWrite)
	if !slices.Contains(got, second) {
		t.Fatalf("write roots after refresh = %v, want the new %s; the refresh stopped at the sandbox manager", got, second)
	}
	if slices.Contains(got, first) {
		t.Fatalf("write roots after refresh = %v, still carry the withdrawn %s", got, first)
	}
}

// TestApplyComposedConfigLeavesMCPServersFrozen pins D2: the effective MCP list
// is resolved once per session and applying a configuration must not replace
// it, even when the applied snapshot carries a different
// agents.defaults.mcp_servers. The new list lands in the next session;
// Runner.Load keeps the live sessions because the frozen list (and its
// fingerprint) is unchanged.
func TestApplyComposedConfigLeavesMCPServersFrozen(t *testing.T) {
	t.Setenv("FOREBRAIN_FREEZE_TEST_KEY", "test-key")
	home := t.TempDir()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	const first = "agents:\n" +
		"  defaults:\n" +
		"    mcp_servers:\n" +
		"      - name: first-server\n" +
		"        command: /usr/bin/true\n" +
		"  definitions:\n" +
		"    main:\n" +
		"      llm_providers:\n" +
		"        - provider: openai\n" +
		"          model: gpt-old\n" +
		"          api_key: ${FOREBRAIN_FREEZE_TEST_KEY}\n" +
		"          base_url: http://localhost:0/v1\n"
	if err := os.WriteFile(cfgPath, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadDocumentForTest(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.LLMModels = modelRowsFromEntriesForTest(appcfg.AgentLLMProviderConfig{
		Provider: "openai", Model: "gpt-old",
		APIKey: "${FOREBRAIN_FREEZE_TEST_KEY}", BaseURL: "http://localhost:0/v1",
	})
	live := &cfg
	runner := &run.Runner{Deps: (&run.Deps{Home: home, AgentName: "main"}).WithConfig(live)}
	// The composition root resolved the effective list once at Open time; the
	// apply path must not touch it afterwards.
	runner.MCPServers = append([]appcfg.MCPServerConfig(nil), live.Agents.Defaults.MCPServers...)
	frozen := runner.MCPServers
	if err := runner.Load(); err != nil {
		t.Fatalf("Runner.Load(first): %v", err)
	}
	env := (&Environment{Deps: &run.Deps{MCPServers: frozen}, Root: home, ConfigPath: cfgPath, Runner: runner}).WithDepsConfig(live)

	const second = "approval_preset: full_access\nagents:\n" +
		"  defaults:\n" +
		"    mcp_servers:\n" +
		"      - name: second-server\n" +
		"        command: /usr/bin/false\n"
	if err := os.WriteFile(cfgPath, []byte(second), 0o600); err != nil {
		t.Fatal(err)
	}
	secondCfg, err := loadDocumentForTest(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	secondCfg.LLMModels = live.LLMModels
	if err := env.applyComposedConfigLocked(secondCfg); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := env.Runner.Config().ApprovalPreset; got != appcfg.ApprovalPresetFullAccess {
		t.Fatalf("AppCfg should reflect the applied snapshot, got %q", got)
	}
	if len(env.Runner.MCPServers) != 1 || env.Runner.MCPServers[0].Name != "first-server" {
		t.Fatalf("effective MCP list must stay frozen, got %+v", env.Runner.MCPServers)
	}
	if env.deps().MCPServers[0].Name != "first-server" {
		t.Fatalf("Deps.MCPServers must stay frozen, got %+v", env.deps().MCPServers)
	}
}

// TestApplyComposedConfigPropagatesToPoolEntries pins the pool half of the
// apply contract: after the base runner adopts a configuration, every live
// project runner is offered the applied raw config through its own LoadConfig
// and re-derives its effective form from it, so a session bound to a project
// never serves a config the rest of the process has left behind — while its
// sandbox stays the one its own trust decision produced, not the base
// runner's.
func TestApplyComposedConfigPropagatesToPoolEntries(t *testing.T) {
	ctx := context.Background()
	env, projects, _ := newPoolTestEnv(t)
	// The environment owns its pool the way the gateway's startup does.
	env.EnsureRunnerPool()
	defer env.CloseRunnerPool()

	proj, err := projects.Create(ctx, state.CreateProjectInput{Name: "apply", Root: "/tmp/pool-apply", ProjectKey: "-tmp-pool-apply"})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.deps().SessionStore.Ensure(ctx, "sess-apply", "sess-apply"); err != nil {
		t.Fatal(err)
	}
	if err := projects.BindSession(ctx, "sess-apply", proj.ID); err != nil {
		t.Fatal(err)
	}
	pooled := env.pool.RunnerForSession(ctx, "sess-apply")
	if pooled == nil || pooled == env.Runner {
		t.Fatal("session did not resolve to a pooled runner")
	}

	// A reordered configuration with a rotated provider entry.
	t.Setenv("FOREBRAIN_PROPAGATION_TEST_KEY", "rotated-key")
	body := "agents:\n" +
		"  definitions:\n" +
		"    main:\n" +
		"      llm_providers:\n" +
		"        - provider: openai\n" +
		"          model: gpt-test\n" +
		"          api_key: ${FOREBRAIN_PROPAGATION_TEST_KEY}\n" +
		"          base_url: http://propagated.test/v1\n"
	if err := os.WriteFile(env.ConfigPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	next, err := loadDocumentForTest(env.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	// The rotated model is a table row (plan 315): the file carries everything
	// else, and the fixture states the table the reload propagates.
	rotated := fixtureModelRows("http://propagated.test/v1")
	rotated[0].APIKey = "rotated-key"
	next.LLMModels = rotated
	if err := env.applyComposedConfigLocked(next); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if pooled.Config() == env.deps().Config() {
		t.Fatalf("pooled runner shares the base runner's derived config; its sandbox must follow its own trust")
	}
	if got := run.PrimaryEndpoint(pooled); got != "http://propagated.test/v1" {
		t.Fatalf("pooled runner provider details stale: %q", got)
	}
	// The ordinary pinned load kept its published selection.
	if sel := run.PrimaryModelSelection(pooled); !sel.Set || sel.Model != "gpt-test" {
		t.Fatalf("pooled runner selection after propagation = %+v", sel)
	}
}

// TestHandEditedConfigFileIsNotPickedUp pins plan 189's ruling: the config file
// stopped being a runtime source. A running process has no watcher, so editing
// forebrain.yaml underneath it changes nothing — not at the next turn boundary
// either, where the runtime sections are read from the state database and the
// file contributes only its startup sections (gateway, credentials). A restart
// is what a hand edit needs.
func TestHandEditedConfigFileIsNotPickedUp(t *testing.T) {
	env := syncEnvForTest(t)
	defer env.Close()

	applied := 0
	env.OnConfigReload = func(*appcfg.Root) { applied++ }
	before := env.deps().Config()

	// A file edit the old watcher would have picked up within its 400ms
	// debounce: a different provider model, with no database row behind it.
	body := "agents:\n  definitions:\n    main:\n      primary: true\n"
	if err := os.WriteFile(env.ConfigPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	if applied != 0 {
		t.Fatalf("the file edit was applied %d times without a restart, want 0: nothing watches the file (plan 189)", applied)
	}
	if env.deps().Config() != before {
		t.Fatal("the live config moved under a file edit")
	}
	// The live configuration still runs on the model the fixture stored in the
	// table: the definition names that row, and the file's dead section cannot
	// move it.
	if got := appcfg.EffectiveDefaultLLMFor(env.deps().Config(), "main"); got == nil || got.Model != "gpt-test" {
		t.Fatalf("live provider model = %+v, want the imported gpt-import", got)
	}

	// Even an explicit turn-boundary sync keeps serving the database's runtime
	// sections: the file's agents section is dead.
	if err := env.SyncConfigFromDB(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := appcfg.EffectiveDefaultLLMFor(env.deps().Config(), "main"); got == nil || got.Model != "gpt-test" {
		t.Fatalf("provider model after a sync = %+v, want the database's row (a hand edit is not a source)", got)
	}
	if applied != 0 {
		t.Fatalf("the unchanged composed configuration was applied %d times, want 0", applied)
	}
}

// TestAdoptLaunchTrustRederivesTheSandbox pins the interactive surface's
// inside-the-TUI trust question (plans/120): a process opened in a folder
// nobody has judged stands on "not decided" — a read-only sandbox — and once
// the operator trusts the folder, AdoptLaunchTrust re-reads the decision and
// re-derives everything the environment froze from it. A process with no
// launch project has nothing to re-read and stays exactly as it was.
func TestAdoptLaunchTrustRederivesTheSandbox(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	ctx := context.Background()
	home := t.TempDir()
	dir := t.TempDir()
	cfg := activeAgentTestConfig()

	env, err := Open(ctx, OpenOptions{
		Home: home, LaunchDir: dir, ExactLaunchRoot: true, Config: &cfg,
		SessionSource: memory.SessionSourceTUI,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	resetProcessWideInstalls(t)
	defer env.Close()

	// Not decided yet: the sandbox the environment froze is read-only.
	if level := env.LaunchProject.TrustLevel; level != safety.LevelUnknown {
		t.Fatalf("trust level after Open = %q, want undecided", level)
	}
	if got := env.deps().Config().SandboxMode; got != appcfg.SandboxModeReadOnly {
		t.Fatalf("sandbox after Open = %q, want read-only while undecided", got)
	}

	// The operator trusts the folder: the decision is re-read and everything
	// derived from "not decided" is derived again.
	if err := safety.MarkTrusted(home, safety.Project{Root: dir}); err != nil {
		t.Fatalf("MarkTrusted: %v", err)
	}
	if err := env.AdoptLaunchTrust(); err != nil {
		t.Fatalf("AdoptLaunchTrust: %v", err)
	}

	if level := env.LaunchProject.TrustLevel; level != safety.LevelTrusted {
		t.Fatalf("trust level after AdoptLaunchTrust = %q, want trusted", level)
	}
	if level := env.deps().LaunchProject.TrustLevel; level != safety.LevelTrusted {
		t.Fatalf("Deps trust level after AdoptLaunchTrust = %q, want trusted", level)
	}
	if got := env.deps().Config().SandboxMode; got != appcfg.SandboxModeWorkspaceWrite {
		t.Fatalf("sandbox after AdoptLaunchTrust = %q, want workspace-write", got)
	}

	// A process with no launch project (the gateway) has nothing to adopt.
	gw, err := Open(ctx, OpenOptions{
		Home: t.TempDir(), Config: &cfg,
		NoLaunchProject: true, SessionSource: memory.SessionSourceWebchat,
	})
	if err != nil {
		t.Fatalf("Open (no launch project): %v", err)
	}
	defer gw.Close()
	before, beforeCfg := gw.LaunchProject, gw.Deps.Config()
	if err := gw.AdoptLaunchTrust(); err != nil {
		t.Fatalf("AdoptLaunchTrust on a no-launch-project environment: %v", err)
	}
	if gw.LaunchProject != before || gw.Deps.Config() != beforeCfg {
		t.Fatal("a no-launch-project environment must not change on AdoptLaunchTrust")
	}
}

// The global approval preset is the posture a session is born with (plan 203
// §2b), so applying a configuration has to hand the new value to the session
// store the same way the memory mode does: sessions created from then on are
// born with it, and the ones already running keep the row they were given.
func TestApplyComposedConfigSeedsNewSessionsWithTheApprovalPreset(t *testing.T) {
	ctx := context.Background()
	env, _, _ := newPoolTestEnv(t)
	// A session that exists before the change, born under no configured preset.
	if err := env.deps().SessionStore.Ensure(ctx, "before", "before"); err != nil {
		t.Fatalf("ensure before: %v", err)
	}

	t.Setenv("FOREBRAIN_PRESET_SEED_TEST_KEY", "test-key")
	body := "approval_preset: full_access\n" +
		"agents:\n" +
		"  definitions:\n" +
		"    main:\n" +
		"      llm_providers:\n" +
		"        - provider: openai\n" +
		"          model: gpt-test\n" +
		"          api_key: ${FOREBRAIN_PRESET_SEED_TEST_KEY}\n" +
		"          base_url: http://localhost:0/v1\n"
	if err := os.WriteFile(env.ConfigPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	next, err := loadDocumentForTest(env.ConfigPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := env.applyComposedConfigLocked(next); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if err := env.deps().SessionStore.Ensure(ctx, "after", "after"); err != nil {
		t.Fatalf("ensure after: %v", err)
	}
	preset, source, _, stored, err := env.deps().SessionStore.SessionPermissionPreset(ctx, "after")
	if err != nil || !stored || preset != "full_access" || source == "" {
		t.Fatalf("session born after the apply = %q/%q stored=%v err=%v, want full_access", preset, source, stored, err)
	}
	if source != state.SessionPresetSourceDefault {
		t.Fatalf("session born after the apply has source %q, want %q", source, state.SessionPresetSourceDefault)
	}
	// The session that already existed was not re-seeded: a configuration
	// change reaches the sessions created after it, never the row a running
	// conversation was given.
	if preset, _, _, _, err := env.deps().SessionStore.SessionPermissionPreset(ctx, "before"); err != nil || preset != state.SessionPresetWorkspaceWrite {
		t.Fatalf("existing session preset = %q err=%v, want the workspace_write row it was born with", preset, err)
	}
}

// A project that becomes a batch project acquires the sandbox switch at that
// moment, from the install default, and only then: the value is the project's
// own from that point on (plans/216), so every path that changes a type has to
// seed identically — the web's PATCH and the terminal's /project both go
// through ProjectTypeInput.
func TestProjectTypeInputSeedsSandboxOnlyWhenBecomingBatch(t *testing.T) {
	cases := []struct {
		name        string
		current     string
		next        string
		seed        bool
		wantType    string
		wantSandbox *bool
	}{
		{name: "coding becomes batch seeds true", current: state.ProjectTypeCoding, next: "batch", seed: true, wantType: state.ProjectTypeBatch, wantSandbox: appcfg.BoolPtr(true)},
		{name: "office becomes batch seeds the install default", current: state.ProjectTypeOffice, next: "batch", seed: false, wantType: state.ProjectTypeBatch, wantSandbox: appcfg.BoolPtr(false)},
		{name: "batch stays batch keeps its switch", current: state.ProjectTypeBatch, next: "batch", seed: true, wantType: state.ProjectTypeBatch},
		{name: "coding becomes office", current: state.ProjectTypeCoding, next: "office", seed: true, wantType: state.ProjectTypeOffice},
		{name: "batch becomes coding", current: state.ProjectTypeBatch, next: "coding", seed: true, wantType: state.ProjectTypeCoding},
		// The seeding condition is the exact stored value, as the web's PATCH
		// always had it: a differently spelled batch type parses to batch but
		// seeds nothing.
		{name: "Batch spelled with a capital seeds nothing", current: state.ProjectTypeCoding, next: "Batch", seed: true, wantType: state.ProjectTypeBatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ProjectTypeInput(tc.current, tc.next, tc.seed)
			if err != nil {
				t.Fatalf("ProjectTypeInput: %v", err)
			}
			if got.ProjectType == nil || *got.ProjectType != tc.wantType {
				t.Fatalf("project type = %v, want %q", got.ProjectType, tc.wantType)
			}
			if tc.wantSandbox == nil {
				if got.BatchSandbox != nil {
					t.Fatalf("batch sandbox = %v, want the stored value left alone", *got.BatchSandbox)
				}
				return
			}
			if got.BatchSandbox == nil || *got.BatchSandbox != *tc.wantSandbox {
				t.Fatalf("batch sandbox = %v, want %v", got.BatchSandbox, *tc.wantSandbox)
			}
		})
	}
}

func TestProjectTypeInputRejectsAnUnknownType(t *testing.T) {
	if _, err := ProjectTypeInput(state.ProjectTypeCoding, "spreadsheet", true); err == nil {
		t.Fatal("an unknown project type must be refused")
	}
}

// TestAdoptLaunchTrustResolvesABatchLaunchFolderByItsType pins the re-read half
// of the batch exemption (owner 2026-11-25): AdoptLaunchTrust runs after a trust
// answer inside the TUI, and for a launch folder whose project is a batch
// project it must resolve by type — otherwise answering a question about an
// unrelated folder would drop the process back to the undecided read-only
// sandbox.
func TestAdoptLaunchTrustResolvesABatchLaunchFolderByItsType(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	ctx := context.Background()
	home := t.TempDir()
	dir := t.TempDir()
	cfg := activeAgentTestConfig()

	env, err := Open(ctx, OpenOptions{
		Home: home, LaunchDir: dir, ExactLaunchRoot: true, Config: &cfg,
		SessionSource: memory.SessionSourceTUI,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	resetProcessWideInstalls(t)
	defer env.Close()

	if level := env.LaunchProject.TrustLevel; level != safety.LevelUnknown {
		t.Fatalf("trust level after Open = %q, want undecided", level)
	}
	if got := env.deps().Config().SandboxMode; got != appcfg.SandboxModeReadOnly {
		t.Fatalf("sandbox after Open = %q, want read-only while undecided", got)
	}

	// The folder is registered as a batch project while the process runs.
	seedProjectRowOn(t, env.SQL, home, activePrimaryAgent(home, &cfg).ID, state.CreateProjectInput{
		Name: "batch-launch", Root: dir, ProjectType: state.ProjectTypeBatch,
	})
	if err := env.AdoptLaunchTrust(); err != nil {
		t.Fatalf("AdoptLaunchTrust: %v", err)
	}
	if !env.LaunchProject.IsTrusted() {
		t.Fatalf("trust level after AdoptLaunchTrust = %q, want trusted without a record", env.LaunchProject.TrustLevel)
	}
	if got := env.deps().Config().SandboxMode; got != appcfg.SandboxModeWorkspaceWrite {
		t.Fatalf("sandbox after AdoptLaunchTrust = %q, want workspace-write", got)
	}

	// A launch folder that is a registered non-batch project keeps the trust it
	// re-reads: the exemption does not reach it.
	plainHome := t.TempDir()
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(checkout, "services", "api")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	seedProjectRow(t, plainHome, activePrimaryAgent(plainHome, &cfg).ID, state.CreateProjectInput{
		Name: "inner", Root: inner, ProjectType: state.ProjectTypeCoding,
	})
	other, err := Open(ctx, OpenOptions{
		Home: plainHome, LaunchDir: inner, ExactLaunchRoot: false, Config: &cfg,
		SessionSource: memory.SessionSourceTUI,
	})
	if err != nil {
		t.Fatalf("Open (inner project): %v", err)
	}
	resetProcessWideInstalls(t)
	defer other.Close()
	if err := safety.MarkTrusted(plainHome, safety.Project{Root: inner}); err != nil {
		t.Fatalf("MarkTrusted: %v", err)
	}
	if err := other.AdoptLaunchTrust(); err != nil {
		t.Fatalf("AdoptLaunchTrust (inner project): %v", err)
	}
	if other.LaunchProject.IsTrusted() {
		t.Fatalf("the checkout is still undecided and must not become trusted: %q", other.LaunchProject.TrustLevel)
	}
}
