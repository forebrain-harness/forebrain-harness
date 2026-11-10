package assembly

import (
	"os"
	"path/filepath"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

const runtimeHookCatalog = `{
  "openai/gpt-5.4": {
    "id": "openai/gpt-5.4",
    "limit": { "context": 128000, "output": 16000 }
  }
}`

// writeRuntimeHookAssets publishes a catalog beside the binary the hook reads —
// the only catalog there is — and returns its directory.
func writeRuntimeHookAssets(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(runtimeHookCatalog), 0o644); err != nil {
		t.Fatalf("write models.json: %v", err)
	}
	t.Setenv(llm.AssetsDirEnv, dir)
	return dir
}

func TestNewHookWiresSharedRuntimeProviders(t *testing.T) {
	home := t.TempDir()
	writeRuntimeHookAssets(t)

	cfg := &appcfg.Root{}
	hook := NewRuntimeHook(HookParams{
		Cfg:           cfg,
		Home:          home,
		WorkspaceRoot: home,
		PrimaryModel: func(string) (string, string) {
			return "openai", "gpt-5.4"
		},
		ReadStatesProvider: func() []tool.ReadState {
			return []tool.ReadState{{AbsPath: "/repo/internal/compact/service.go"}}
		},
	})

	limits, ok := hook.ModelLimitsProvider("s1")
	if !ok || limits.Context != 128000 || limits.Output != 16000 {
		t.Fatalf("ModelLimitsProvider=(%+v,%v)", limits, ok)
	}
	if got := hook.ReadStatesProvider(); len(got) != 1 || got[0].AbsPath != "/repo/internal/compact/service.go" {
		t.Fatalf("ReadStatesProvider=%v", got)
	}

	if err := state.Set(home, "s1", state.State{Mode: state.Mode("coordinator"), Phase: "plan"}); err != nil {
		t.Fatalf("state.Set: %v", err)
	}
	mode, phase := hook.ModeProvider("s1")
	if mode != "agent" || phase != "plan" {
		t.Fatalf("ModeProvider=(%q,%q)", mode, phase)
	}
}

// The hook reads the catalog this build ships beside its binary. A home with no
// models.json is the normal state now — nothing is installed there any more
// (owner 2026-11-09) — and it must still produce the windows rather than
// leaving them unknown, without writing anything into the home.
func TestRuntimeHookBuildsLimitsWithoutHomeCatalog(t *testing.T) {
	home := t.TempDir()
	assets := writeRuntimeHookAssets(t)
	if _, err := os.Stat(filepath.Join(home, "models.json")); !os.IsNotExist(err) {
		t.Fatalf("the test home must start without a catalog, stat err=%v", err)
	}

	hook := NewRuntimeHook(HookParams{
		Cfg:  &appcfg.Root{},
		Home: home,
		PrimaryModel: func(string) (string, string) {
			return "openai", "gpt-5.4"
		},
	})
	limits, ok := hook.ModelLimitsProvider("s1")
	if !ok || limits.Context != 128000 || limits.Output != 16000 {
		t.Fatalf("ModelLimitsProvider=(%+v,%v), want the shipped catalog's windows", limits, ok)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the home gained %d entries; resolving the catalog writes nothing there", len(entries))
	}

	// A build that ships no catalog leaves the provider nil — the assembly reads
	// the windows as unknown — rather than failing to assemble.
	if err := os.Remove(filepath.Join(assets, "models.json")); err != nil {
		t.Fatal(err)
	}
	empty := NewRuntimeHook(HookParams{
		Cfg:  &appcfg.Root{},
		Home: home,
		PrimaryModel: func(string) (string, string) {
			return "openai", "gpt-5.4"
		},
	})
	if empty.ModelLimitsProvider != nil {
		t.Fatal("a build with no shipped catalog must leave ModelLimitsProvider nil")
	}
}
