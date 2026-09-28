package process

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
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
