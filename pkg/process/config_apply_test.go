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
// swaps Runner.AppCfg/MCPServers/YOLO *before* it calls Runner.Load, so a
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
	cfg, err := appcfg.Load(cfgPath)
	if err != nil {
		t.Fatalf("appcfg.Load(good): %v", err)
	}
	live := &cfg

	runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: live}}
	runner.MCPServers = live.Agents.Defaults.MCPServers
	if err := runner.Load(); err != nil {
		t.Fatalf("Runner.Load(good): %v", err)
	}
	env := &Environment{Deps: run.Deps{AppCfg: live}, Root: home, ConfigPath: cfgPath, Runner: runner}

	// Refused by the loader: a provider the runtime cannot route never becomes
	// the running model, so the application fails after the pointer has already
	// been swapped and only the rollback can leave the live runtime intact. The
	// refusal comes from Runner.LoadConfig and not from the read — a
	// save-or-load-side guard would fail before anything was swapped, which is
	// the state this test exists to rule out.
	brokenCfg := cfg
	brokenDefs := make(map[string]appcfg.AgentDefinition, len(cfg.Agents.Definitions))
	for name, def := range cfg.Agents.Definitions {
		brokenDefs[name] = def
	}
	brokenMain := brokenDefs["main"]
	brokenMain.LLMProviders = []appcfg.AgentLLMProviderConfig{{
		Provider: "openai", Model: "gpt-old",
		BaseURL: "http://localhost:0/v1",
	}}
	brokenDefs["main"] = brokenMain
	brokenCfg.Agents.Definitions = brokenDefs

	if err := env.applyComposedConfigLocked(brokenCfg); err == nil {
		t.Fatal("applying an unloadable config = nil, want an error")
	}
	// The snapshot did reach the kernel — it passed the startup check and only
	// the runner refused it — so the assertions below are about the rollback,
	// not about an early return.
	if got := env.RawConfig.Agents.Definitions["main"].LLMProviders; len(got) != 1 || got[0].APIKey != "" {
		t.Fatalf("RawConfig = %+v, want the applied snapshot; the failure came before the runner was reached", got)
	}

	if env.Deps.AppCfg != live {
		t.Fatalf("Environment.Deps.AppCfg was replaced despite the failed load")
	}
	if env.Runner.AppCfg != live {
		t.Fatalf("Runner.AppCfg = %p, want it restored to the live config %p", env.Runner.AppCfg, live)
	}
	if got := env.Runner.AppCfg.Agents.Definitions["main"].LLMProviders; len(got) != 1 || got[0].Model != "gpt-old" {
		t.Fatalf("Runner.AppCfg providers = %+v, want the pre-apply gpt-old entry", got)
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
	cfg := appcfg.Root{
		SandboxMode: appcfg.SandboxModeWorkspaceWrite,
		Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
			"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{{
				Provider: "openai", Model: "gpt-x",
				APIKey: "${FOREBRAIN_SANDBOX_REFRESH_TEST_KEY}", BaseURL: "http://localhost:0/v1",
			}}},
		}},
	}
	cfg.SandboxWorkspaceWrite.WritableRoots = []string{first}

	runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: &cfg, ProjectRoot: home}}
	if err := runner.Load(); err != nil {
		t.Fatalf("Runner.Load: %v", err)
	}
	env := &Environment{Deps: run.Deps{Home: home, AppCfg: &cfg}, Root: home, Runner: runner}

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
	cfg, err := appcfg.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	live := &cfg
	runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: live}}
	// The composition root resolved the effective list once at Open time; the
	// apply path must not touch it afterwards.
	runner.MCPServers = append([]appcfg.MCPServerConfig(nil), live.Agents.Defaults.MCPServers...)
	frozen := runner.MCPServers
	if err := runner.Load(); err != nil {
		t.Fatalf("Runner.Load(first): %v", err)
	}
	env := &Environment{Deps: run.Deps{AppCfg: live, MCPServers: frozen}, Root: home, ConfigPath: cfgPath, Runner: runner}

	const second = "agents:\n" +
		"  defaults:\n" +
		"    mcp_servers:\n" +
		"      - name: second-server\n" +
		"        command: /usr/bin/false\n" +
		"  definitions:\n" +
		"    main:\n" +
		"      llm_providers:\n" +
		"        - provider: openai\n" +
		"          model: gpt-new\n" +
		"          api_key: ${FOREBRAIN_FREEZE_TEST_KEY}\n" +
		"          base_url: http://localhost:0/v1\n"
	if err := os.WriteFile(cfgPath, []byte(second), 0o600); err != nil {
		t.Fatal(err)
	}
	secondCfg, err := appcfg.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.applyComposedConfigLocked(secondCfg); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := env.Runner.AppCfg.Agents.Definitions["main"].LLMProviders[0].Model; got != "gpt-new" {
		t.Fatalf("AppCfg should reflect the applied snapshot, got %q", got)
	}
	if len(env.Runner.MCPServers) != 1 || env.Runner.MCPServers[0].Name != "first-server" {
		t.Fatalf("effective MCP list must stay frozen, got %+v", env.Runner.MCPServers)
	}
	if env.Deps.MCPServers[0].Name != "first-server" {
		t.Fatalf("Deps.MCPServers must stay frozen, got %+v", env.Deps.MCPServers)
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
	if err := env.Deps.SessionStore.Ensure(ctx, "sess-apply", "sess-apply"); err != nil {
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
	next, err := appcfg.Load(env.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.applyComposedConfigLocked(next); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if pooled.AppCfg == env.Deps.AppCfg {
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
	before := env.Deps.AppCfg

	// A file edit the old watcher would have picked up within its 400ms
	// debounce: a different provider model, with no database row behind it.
	body := "agents:\n" +
		"  definitions:\n" +
		"    main:\n" +
		"      llm_providers:\n" +
		"        - provider: openai\n" +
		"          model: gpt-hand-edited\n" +
		"          api_key: ${FOREBRAIN_IMPORT_TEST_KEY}\n" +
		"          base_url: http://hand-edited.test/v1\n"
	if err := os.WriteFile(env.ConfigPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	if applied != 0 {
		t.Fatalf("the file edit was applied %d times without a restart, want 0: nothing watches the file (plan 189)", applied)
	}
	if env.Deps.AppCfg != before {
		t.Fatal("the live config moved under a file edit")
	}
	if got := env.Deps.AppCfg.Agents.Definitions["main"].LLMProviders[0].Model; got != "gpt-import" {
		t.Fatalf("live provider model = %q, want the imported gpt-import", got)
	}

	// Even an explicit turn-boundary sync keeps serving the database's runtime
	// sections: the file's agents section is dead.
	if err := env.SyncConfigFromDB(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := env.Deps.AppCfg.Agents.Definitions["main"].LLMProviders[0].Model; got != "gpt-import" {
		t.Fatalf("provider model after a sync = %q, want the database's gpt-import (a hand edit is not a source)", got)
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
	if got := env.Deps.AppCfg.SandboxMode; got != appcfg.SandboxModeReadOnly {
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
	if level := env.Deps.LaunchProject.TrustLevel; level != safety.LevelTrusted {
		t.Fatalf("Deps trust level after AdoptLaunchTrust = %q, want trusted", level)
	}
	if got := env.Deps.AppCfg.SandboxMode; got != appcfg.SandboxModeWorkspaceWrite {
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
	before, beforeCfg := gw.LaunchProject, gw.Deps.AppCfg
	if err := gw.AdoptLaunchTrust(); err != nil {
		t.Fatalf("AdoptLaunchTrust on a no-launch-project environment: %v", err)
	}
	if gw.LaunchProject != before || gw.Deps.AppCfg != beforeCfg {
		t.Fatal("a no-launch-project environment must not change on AdoptLaunchTrust")
	}
}
