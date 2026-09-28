package assembly

import (
	"os"
	"path/filepath"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

func TestNewHookWiresSharedRuntimeProviders(t *testing.T) {
	home := t.TempDir()
	modelsPath := filepath.Join(home, "models.json")
	if err := os.WriteFile(modelsPath, []byte(`{
  "openai/gpt-5.4": {
    "id": "openai/gpt-5.4",
    "limit": { "context": 128000, "output": 16000 }
  }
}`), 0o644); err != nil {
		t.Fatalf("write models.json: %v", err)
	}

	cfg := &appcfg.Root{}
	hook := NewRuntimeHook(HookParams{
		Cfg:           cfg,
		Home:          home,
		WorkspaceRoot: home,
		PrimaryModel: func(string) (string, string) {
			return "openai", "gpt-5.4"
		},
		PinsProvider: func(sessionID string) []string {
			return []string{"README.md"}
		},
		ReadStatesProvider: func() []tool.ReadState {
			return []tool.ReadState{{AbsPath: "/repo/internal/compact/service.go"}}
		},
	})

	limits, ok := hook.ModelLimitsProvider("s1")
	if !ok || limits.Context != 128000 || limits.Output != 16000 {
		t.Fatalf("ModelLimitsProvider=(%+v,%v)", limits, ok)
	}
	if got := hook.PinsProvider("s1"); len(got) != 1 || got[0] != "README.md" {
		t.Fatalf("PinsProvider=%v", got)
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
