package process

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/lsp"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

func activeAgentTestConfig() appcfg.Root {
	return appcfg.Root{
		Agents: appcfg.AgentsSection{
			Definitions: map[string]appcfg.AgentDefinition{
				"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{{
					Provider: "openai", Model: "gpt-main",
					APIKey: "${OPENAI_API_KEY}", BaseURL: "http://localhost:0/v1",
				}}},
				"review": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{{
					Provider: "openai", Model: "gpt-review",
					APIKey: "${OPENAI_API_KEY}", BaseURL: "http://localhost:0/v1",
				}}},
			},
		},
	}
}

// TestActivePrimaryAgentBindsTheSwitchedAgent pins that the process serves the
// primary agent the user switched to, not main.
//
// Open resolves this once and feeds it to the session store, the memory store
// and the Runner's AgentName and WorkspaceRoot, so getting it wrong answers
// with main's model out of another agent's workspace — and makes /connect and
// /model, which both target the active agent, write a definition nothing reads.
//
// P2-3 moved the runner assembly into this package, so the assertion lives
// here, against the function Open actually resolves the tenant with.
func TestActivePrimaryAgentBindsTheSwitchedAgent(t *testing.T) {
	home := t.TempDir()
	cfg := activeAgentTestConfig()

	resolver, err := appcfg.NewResolver(home, &cfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	switched, err := resolver.Switch("review")
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if switched.ID != "review" {
		t.Fatalf("Switch returned %q, want review", switched.ID)
	}

	active := activePrimaryAgent(home, &cfg)
	if active.ID != "review" {
		t.Fatalf("activePrimaryAgent = %q, want the switched agent %q", active.ID, "review")
	}
	if got, want := filepath.Clean(active.WorkspaceRoot), filepath.Clean(switched.WorkspaceRoot); got != want {
		t.Fatalf("WorkspaceRoot = %q, want %q", got, want)
	}
}

// TestActivePrimaryAgentFallsBackToMainWhenUnresolvable covers the other half
// of the contract in activePrimaryAgent's comment: the tenant it names must
// always be the workspace the runtime actually reads and writes, so an
// unresolvable config falls back to main's rather than to an empty id.
func TestActivePrimaryAgentFallsBackToMainWhenUnresolvable(t *testing.T) {
	home := t.TempDir()
	empty := appcfg.Root{}

	active := activePrimaryAgent(home, &empty)
	if active.ID != appcfg.ActiveID(home, &empty) {
		t.Fatalf("ID = %q, want the same fallback ActiveID reports (%q)", active.ID, appcfg.ActiveID(home, &empty))
	}
	if active.WorkspaceRoot != appcfg.ActiveStateRoot(home, &empty) {
		t.Fatalf("WorkspaceRoot = %q, want the same fallback ActiveStateRoot reports (%q)",
			active.WorkspaceRoot, appcfg.ActiveStateRoot(home, &empty))
	}
}

func TestContextSnapshotResolvesConfiguredPrimaryModel(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "models.json"), []byte(`{
		"openai/compact-test": {"id":"openai/compact-test", "limit":{"context":1000,"input":900,"output":100}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{{
			Provider: "openai", Models: appcfg.StringList{"compact-test", "another-model"},
		}}},
		"review": {Primary: true},
	}}}
	for _, agentName := range []string{"main", "review"} {
		t.Run(agentName, func(t *testing.T) {
			runner := &run.Runner{Deps: &run.Deps{AppCfg: cfg, AgentName: agentName}}
			h := assembly.NewRuntimeHook(assembly.HookParams{
				Cfg: cfg, Home: home,
				PrimaryModel: func(string) (string, string) { return run.PrimaryModel(runner) },
			})
			snapshot, ok, err := h.Assemble(hook.HookContext{SessionID: "s1", Trigger: "user"}, "continue")
			if err != nil || !ok {
				t.Fatalf("Assemble: ok=%v err=%v", ok, err)
			}
			if snapshot.ModelContextTokens != 1000 || snapshot.EffectiveInputTokens != 900 {
				t.Fatalf("snapshot window=%d effective input=%d, want 1000/900", snapshot.ModelContextTokens, snapshot.EffectiveInputTokens)
			}
		})
	}
}

// The resolver a process installs has to answer both file_id namespaces its
// two surfaces write. When it only understood upload IDs, every replayed
// terminal attachment resolved to nothing and rehydration left the text
// placeholder, so the model never received the attached image.
func TestFileReferenceResolverAnswersTerminalAttachmentPaths(t *testing.T) {
	ctx := context.Background()
	resolver := fileReferenceResolver(nil, "main")

	attachment := filepath.Join(t.TempDir(), "shot.png")
	if got, ok := resolver.ResolveFilePath(ctx, attachment); !ok || got != attachment {
		t.Fatalf("terminal attachment path resolved to %q ok=%v, want %q", got, ok, attachment)
	}
	if _, ok := resolver.ResolveFilePath(ctx, "upload-1"); ok {
		t.Fatal("an upload id must not resolve without a file service")
	}
	if _, ok := resolver.ResolveFilePath(ctx, ""); ok {
		t.Fatal("a blank file_id must not resolve")
	}
}

// A session is filed under the project it was launched in, and a config reload
// cannot move it.
//
// fb_sessions.cwd is the only input the memory pipeline resolves a thread's
// project scope from (memory.ProjectScopeForCwd), while the memory a turn
// recalls comes from Runner.ProjectKey. Open once stamped the agent workspace
// root here instead of the launch directory, so the two disagreed: every
// memory a session produced was consolidated into a scope
// (projects/-Users-...-forebrain-workspace) that no session ever searched, and the
// real project's MEMORY.md stopped growing. The reload path repeated the same
// derivation, so a config change re-broke a session that Open had got right.
func TestOpenFilesSessionsUnderTheLaunchProjectScope(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The raw config below references ${OPENAI_API_KEY}; the test must set it
	// itself rather than lean on the developer's shell having it exported
	// (CI has not), or Open fails before the assertion is reached.
	t.Setenv("OPENAI_API_KEY", "test-key")
	cfg := activeAgentTestConfig()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	raw := "agents:\n  definitions:\n    main:\n      primary: true\n      llm_providers:\n" +
		"        - provider: openai\n          model: gpt-main\n          api_key: ${OPENAI_API_KEY}\n          base_url: http://localhost:0/v1\n"
	if err := os.WriteFile(cfgPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := Open(ctx, OpenOptions{Home: home, ConfigPath: cfgPath, LaunchDir: project, Config: &cfg, SessionSource: memory.SessionSourceTUI})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer env.Close()

	sessionCwd := func(id string) string {
		t.Helper()
		if err := env.Deps.SessionStore.Ensure(ctx, id, id); err != nil {
			t.Fatalf("Ensure %s: %v", id, err)
		}
		var cwd string
		if err := env.SQL.QueryRowContext(ctx, `SELECT cwd FROM fb_sessions WHERE id=?`, id).Scan(&cwd); err != nil {
			t.Fatalf("read cwd for %s: %v", id, err)
		}
		return cwd
	}
	assertScope := func(label, cwd string) {
		t.Helper()
		if cwd == env.Runner.WorkspaceRoot {
			t.Fatalf("%s: cwd is the agent workspace root %q; the workspace is state, not a project", label, cwd)
		}
		scope, ok := memory.ProjectScopeForCwd(cwd)
		if !ok {
			t.Fatalf("%s: cwd %q carries no project scope", label, cwd)
		}
		if scope.Key != env.Runner.ProjectKey {
			t.Fatalf("%s: writes go to scope %q but recall reads %q", label, scope.Key, env.Runner.ProjectKey)
		}
	}

	assertScope("after Open", sessionCwd("s-open"))

	if err := env.ReloadConfig(); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	assertScope("after a config reload", sessionCwd("s-reload"))
}

// TestOpenWiresCodeIntelligence pins the composition-root contract of task 03:
// Open builds the process-wide language-server pool, hands the primary Runner
// its view through the Deps ports, and Close releases both. The skeleton
// registers no tool, so the frozen decision must read false.
func TestOpenWiresCodeIntelligence(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "test-key")
	cfg := activeAgentTestConfig()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	raw := "agents:\n  definitions:\n    main:\n      primary: true\n      llm_providers:\n" +
		"        - provider: openai\n          model: gpt-main\n          api_key: ${OPENAI_API_KEY}\n          base_url: http://localhost:0/v1\n"
	if err := os.WriteFile(cfgPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := Open(ctx, OpenOptions{Home: home, ConfigPath: cfgPath, LaunchDir: project, Config: &cfg, SessionSource: memory.SessionSourceTUI})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if env.LSP == nil {
		t.Fatal("Open built no language-server pool")
	}
	if env.Deps.CodeIntel == nil || env.Deps.CodeIntelControl == nil {
		t.Fatal("the primary runner's Deps carry no language-server runtime")
	}
	if env.Deps.CodeIntelTool {
		t.Fatal("the skeleton must not register the lsp tool")
	}
	// The launch project's root is stored symlink-resolved, which on macOS
	// turns t.TempDir()'s /var/... into /private/var/...; compare against
	// the resolved form.
	wantRoot := project
	if resolved, rerr := filepath.EvalSymlinks(project); rerr == nil {
		wantRoot = resolved
	}
	if got := env.Deps.CodeIntelControl.Snapshot().ProjectRoot; got != wantRoot {
		t.Fatalf("snapshot project root = %q, want the launch project %q", got, wantRoot)
	}

	env.Close()
	late := env.LSP.NewManager(lsp.ManagerOptions{})
	if snap := late.Snapshot(); snap.Servers == nil || len(snap.Servers) != 0 {
		t.Fatalf("a manager built after Close answered with %v, want an empty snapshot", snap.Servers)
	}
	cancel := late.Subscribe(func(event.LSPSnapshot) {})
	if cancel == nil {
		t.Fatal("nil cancel from a closed manager's Subscribe")
	}
	cancel()
}

// TestLSPRecommendationPublishedToSurface pins the composition-root wiring
// of task 13: a recommendation the manager makes becomes one run event on
// the runner's sink, carrying the session and run of the edit that earned
// it, and a runner no surface has attached yet is simply skipped.
func TestLSPRecommendationPublishedToSurface(t *testing.T) {
	// The real catalog's gopls entry stands in for a recommendable server:
	// a fake gopls on PATH makes detection answer "installed", so the
	// recommendation is the enable kind and no install recipe runs.
	bin := t.TempDir()
	gopls := filepath.Join(bin, "gopls")
	if err := os.WriteFile(gopls, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	project := t.TempDir()
	file := filepath.Join(project, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pool := lsp.NewPool(&appcfg.Root{})
	t.Cleanup(func() { _ = pool.Close() })

	var mu sync.Mutex
	var events []event.RunEvent
	runner := &run.Runner{Events: event.SinkFunc(func(_ context.Context, evt event.RunEvent) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, evt)
		return nil
	})}
	mgr := pool.NewManager(lsp.ManagerOptions{
		Home:           t.TempDir(),
		AgentWorkspace: t.TempDir(),
		ProjectRoot:    project,
		Trusted:        true,
	})
	publishLSPRecommendations(mgr, runner)

	// The first edit only probes; retries keep editing until the probe has
	// answered and the recommendation lands on the sink.
	ctx := tool.WithRunID(tool.WithConversationSessionID(context.Background(), "sess-7"), "run-9")
	deadline := time.Now().Add(5 * time.Second)
	for {
		_ = mgr.DidWrite(ctx, "sess-7", []tool.FileChange{{AbsPath: file, After: []byte("package main\n")}})
		mu.Lock()
		got := len(events)
		mu.Unlock()
		if got > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("events = %d, want exactly one recommendation", len(events))
	}
	evt := events[0]
	if evt.Type != event.RunEventLSPRecommendation {
		t.Fatalf("type = %q, want %q", evt.Type, event.RunEventLSPRecommendation)
	}
	if evt.SessionID != "sess-7" || evt.RunID != "run-9" {
		t.Fatalf("session/run = %q/%q, want sess-7/run-9 from the edit's context", evt.SessionID, evt.RunID)
	}
	if !strings.HasPrefix(evt.ID, "lsprec-") {
		t.Fatalf("event id = %q, want the recommendation's own id", evt.ID)
	}
	var rec event.LSPRecommendation
	if err := json.Unmarshal(evt.Payload, &rec); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if rec.ServerID != "gopls" || rec.Mode != "enable" || rec.TriggerExtension != ".go" {
		t.Fatalf("recommendation = %+v, want gopls enable-mode on .go", rec)
	}

	// A runner no surface has attached (Events nil) publishes nothing and
	// must not panic doing it.
	bareRunner := &run.Runner{}
	bareMgr := pool.NewManager(lsp.ManagerOptions{
		Home:           t.TempDir(),
		AgentWorkspace: t.TempDir(),
		ProjectRoot:    project,
		Trusted:        true,
	})
	publishLSPRecommendations(bareMgr, bareRunner)
	_ = bareMgr.DidWrite(ctx, "sess-bare", []tool.FileChange{{AbsPath: file, After: []byte("package main\n")}})
	time.Sleep(200 * time.Millisecond)
}
