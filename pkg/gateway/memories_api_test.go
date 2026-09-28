package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	state "github.com/forebrain-harness/forebrain-harness/pkg/state"
)

func TestMemoriesResetRequiresStoreAndLeavesFilesUntouched(t *testing.T) {
	home := t.TempDir()
	marker := filepath.Join(home, memory.MemoryDirName, "MEMORY.md")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{Home: home}
	recorder := httptest.NewRecorder()
	server.handleMemoriesReset(recorder, httptest.NewRequest(http.MethodPost, "/memories/reset", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if body, err := os.ReadFile(marker); err != nil || string(body) != "keep" {
		t.Fatalf("memory file changed: body=%q err=%v", body, err)
	}
}

func TestMemoriesResetClearsStoreAndFiles(t *testing.T) {
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := memory.NewStore(db, "main")
	// Memories are per primary agent, so the reset resolves the calling agent's
	// own root rather than a home-derived one shared by every tenant.
	const projectKey = "-Test-project"
	runner := &run.Runner{Deps: &run.Deps{Home: home, WorkspaceRoot: filepath.Join(home, "workspace"), ProjectKey: projectKey}}
	roots, err := memory.ResolveRootsForAgent(runner.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	projectRoot := roots.Scope(memory.Scope{Kind: memory.ScopeProject, Key: projectKey})
	marker := filepath.Join(projectRoot.MemoryRoot, "MEMORY.md")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("clear"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{Home: home, MemoryStore: store, Runner: runner}
	recorder := httptest.NewRecorder()
	// The default reset (no ?scope=all) clears only the calling session's own
	// project — the common "forget what you learned about this repo" ask.
	server.handleMemoriesReset(recorder, httptest.NewRequest(http.MethodPost, "/memories/reset", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	entries, err := os.ReadDir(projectRoot.MemoryRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("project memory root entries=%#v err=%v", entries, err)
	}
}

func TestMemoriesSettingsValidRequestUpdatesConfig(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "forebrain.yaml")
	if err := os.WriteFile(configPath, []byte("features:\n  memories: true\nmemories:\n  use_memories: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := &process.Environment{Deps: run.Deps{AppCfg: &appcfg.Root{}}, Root: home, ConfigPath: configPath}
	server := &Server{Home: home, Env: env}
	recorder := httptest.NewRecorder()
	server.handleMemoriesSettings(recorder, httptest.NewRequest(http.MethodPost, "/memories/settings", strings.NewReader(`{"use_memories":false}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	loaded, err := appcfg.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.EffectiveMemories().UseMemories {
		t.Fatal("valid request did not update use_memories")
	}
}

func TestMemoriesSettingsStrictJSONAndStorePrecondition(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "forebrain.yaml")
	original := "features:\n  memories: true\nmemories:\n  generate_memories: true\n"
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{Home: home, Env: &process.Environment{Root: home, ConfigPath: configPath}}
	for _, body := range []string{`{"unknown":true}`, `{} {}`} {
		recorder := httptest.NewRecorder()
		server.handleMemoriesSettings(recorder, httptest.NewRequest(http.MethodPost, "/memories/settings", strings.NewReader(body)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body=%q status=%d response=%q", body, recorder.Code, recorder.Body.String())
		}
	}
	recorder := httptest.NewRecorder()
	server.handleMemoriesSettings(recorder, httptest.NewRequest(http.MethodPost, "/memories/settings", strings.NewReader(`{"thread_id":"thread","generate_memories":false}`)))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing store status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != original {
		t.Fatalf("config changed before store precondition: %q", body)
	}
}
