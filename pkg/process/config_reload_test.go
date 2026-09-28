package process

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// These two migrated here from pkg/tui when the duplicate TUI-side fsnotify
// watcher was deleted (P3-1 requires the idempotent start/stop tests to move
// with the watcher; P3-5 requires exactly one watcher implementation
// repo-wide). The behaviour asserted is unchanged — only the owner is.
//
// The watcher only needs ConfigPath to be non-empty and its directory to
// exist; it watches the directory rather than the file, so the config file
// itself never has to be written for these.

func TestStartConfigHotReloadIsIdempotent(t *testing.T) {
	env := &Environment{ConfigPath: filepath.Join(t.TempDir(), "forebrain.yaml")}
	t.Cleanup(env.stopConfigHotReload)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartConfigHotReload(ctx, env)
	env.watchMu.Lock()
	firstStop := env.watchStop
	firstID := env.watchID
	env.watchMu.Unlock()
	if firstStop == nil {
		t.Fatal("watchStop is nil after the first start, want a stop func")
	}
	if firstID == 0 {
		t.Fatal("watchID = 0 after the first start, want it incremented")
	}

	// A second start must not replace the running watcher. watchID is the
	// witness rather than the stop func: Go func values cannot be compared
	// to each other, only to nil, and watchID increments on every genuine
	// start.
	StartConfigHotReload(ctx, env)
	env.watchMu.Lock()
	secondStop := env.watchStop
	secondID := env.watchID
	env.watchMu.Unlock()
	if secondStop == nil {
		t.Fatal("watchStop is nil after the second start, want the first one retained")
	}
	if secondID != firstID {
		t.Fatalf("watchID = %d after a second start, want it unchanged at %d (the second start must be a no-op)", secondID, firstID)
	}
}

func TestStopConfigHotReloadIsIdempotent(t *testing.T) {
	env := &Environment{ConfigPath: filepath.Join(t.TempDir(), "forebrain.yaml")}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartConfigHotReload(ctx, env)

	// Stopping twice must not panic.
	env.stopConfigHotReload()
	env.stopConfigHotReload()

	env.watchMu.Lock()
	stopped := env.watchStop
	env.watchMu.Unlock()
	if stopped != nil {
		t.Fatal("watchStop is non-nil after stop, want it cleared")
	}
}

// P3-4 requires a broken-config test proving the live runtime is left
// completely unchanged, with no half-applied state. reloadConfig swaps
// Runner.AppCfg/MCPServers/YOLO *before* it calls Runner.Load, so a config
// that parses and passes the startup check but fails to load is exactly the
// case that can strand the runner describing a file nothing else is using.
// The rollback in config_reload.go had no coverage before this.
func TestReloadConfigRollsBackEveryFieldWhenRunnerLoadFails(t *testing.T) {
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

	// Parses and passes the startup check, but has no llm_providers, so
	// Runner.Load fails after the fields above have already been swapped.
	const broken = "agents:\n  definitions:\n    main: {}\n"
	if err := os.WriteFile(cfgPath, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := env.ReloadConfig(); err == nil {
		t.Fatal("ReloadConfig with an unloadable config = nil, want an error")
	}

	if env.Deps.AppCfg != live {
		t.Fatalf("Environment.Deps.AppCfg was replaced despite the failed load")
	}
	if env.Runner.AppCfg != live {
		t.Fatalf("Runner.AppCfg = %p, want it restored to the live config %p", env.Runner.AppCfg, live)
	}
	if got := env.Runner.AppCfg.Agents.Definitions["main"].LLMProviders; len(got) != 1 || got[0].Model != "gpt-old" {
		t.Fatalf("Runner.AppCfg providers = %+v, want the pre-reload gpt-old entry", got)
	}
	if len(env.Runner.MCPServers) != 1 || env.Runner.MCPServers[0].Name != "keep-me" {
		t.Fatalf("Runner.MCPServers = %+v, want the pre-reload entry restored", env.Runner.MCPServers)
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

// TestReloadConfigLeavesMCPServersFrozen pins D2: the effective MCP list is
// resolved once per session and a config reload must not replace it, even when
// the reloaded file carries a different agents.defaults.mcp_servers. The new
// list lands in the next session; Runner.Load keeps the live sessions because
// the frozen list (and its fingerprint) is unchanged.
func TestReloadConfigLeavesMCPServersFrozen(t *testing.T) {
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
	// reload path must not touch it afterwards.
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
	if err := env.ReloadConfig(); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	if got := env.Runner.AppCfg.Agents.Definitions["main"].LLMProviders[0].Model; got != "gpt-new" {
		t.Fatalf("AppCfg should reflect the reloaded file, got %q", got)
	}
	if len(env.Runner.MCPServers) != 1 || env.Runner.MCPServers[0].Name != "first-server" {
		t.Fatalf("effective MCP list must stay frozen, got %+v", env.Runner.MCPServers)
	}
	if env.Deps.MCPServers[0].Name != "first-server" {
		t.Fatalf("Deps.MCPServers must stay frozen, got %+v", env.Deps.MCPServers)
	}
}

// TestReloadConfigPropagatesToPoolEntries pins the pool half of the reload
// contract: after the base runner adopts a config, every live project runner
// is offered the same pointer through its own LoadConfig, so a session bound
// to a project never serves a config the rest of the process has left behind.
func TestReloadConfigPropagatesToPoolEntries(t *testing.T) {
	ctx := context.Background()
	env, projects, _ := newPoolTestEnv(t)
	// The environment owns its pool the way the gateway's startup does.
	env.EnsureRunnerPool()
	defer env.CloseRunnerPool()

	proj, err := projects.Create(ctx, state.CreateProjectInput{Name: "reload", Root: "/tmp/pool-reload", ProjectKey: "-tmp-pool-reload"})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.SessionStore.Ensure(ctx, "sess-reload", "sess-reload"); err != nil {
		t.Fatal(err)
	}
	if err := projects.BindSession(ctx, "sess-reload", proj.ID); err != nil {
		t.Fatal(err)
	}
	pooled := env.pool.RunnerForSession(ctx, "sess-reload")
	if pooled == nil || pooled == env.Runner {
		t.Fatal("session did not resolve to a pooled runner")
	}

	// A reordered file with a rotated provider entry.
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
	if err := env.ReloadConfig(); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}

	if pooled.AppCfg != env.Deps.AppCfg {
		t.Fatalf("pooled runner did not adopt the reloaded config")
	}
	if got := run.PrimaryEndpoint(pooled); got != "http://propagated.test/v1" {
		t.Fatalf("pooled runner provider details stale: %q", got)
	}
	// The ordinary pinned reload kept its published selection.
	if sel := run.PrimaryModelSelection(pooled); !sel.Set || sel.Model != "gpt-test" {
		t.Fatalf("pooled runner selection after propagation = %+v", sel)
	}
}
