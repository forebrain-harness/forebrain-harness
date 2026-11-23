package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

func TestZZReproCreateAgentRealStore(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	if err := os.WriteFile(cfgPath, []byte("gateway:\n  http_addr: 127.0.0.1:9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state", "forebrain.state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	models := &state.LLMModelStore{DB: db}
	_, err = models.Create(ctx, state.LLMModel{Provider: "deepseek", Model: "deepseek-flash", BaseURL: "https://api.deepseek.com", APIPath: "/v1/chat/completions"})
	if err != nil {
		t.Fatal(err)
	}
	process.InstallConfigSectionValidator(cfgPath, db)
	t.Cleanup(func() { state.SetSectionValidator(nil) })
	env := &process.Environment{Root: home, SQL: db}
	env.ConfigPath = cfgPath
	session := &ChatSession{Env: env}

	// Seed one primary agent, the way a real install has one.
	seed := `{"definitions":{"main":{"primary":true}}}`
	if err := session.UpdateConfigSection(ctx, "agents", json.RawMessage(seed)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	selector := &scriptedSelector{}
	renderer := runConfigPanelOn(t, session, selector, []richStep{
		{label: "Agent definitions", ok: true},
		{label: configAddRowLabel, ok: true},
		{label: "Name", ok: true},
		{label: "Display name", ok: true},
		{label: "Description", ok: true},
		{label: "Model", ok: true},
		{label: "deepseek/deepseek-flash", ok: true},
		{label: configCreateSaveLabel, ok: true},
		{ok: false},
		{ok: false},
		{ok: false},
	}, []selectorInput{
		{value: "frontend-expert", ok: true},
		{value: "前端专家", ok: true},
		{value: "Frontend specialist.", ok: true},
	}, "agents")

	t.Logf("frames:\n%s", frameText(renderer))
	t.Logf("pages:\n%s", pageLabels(selector))

	snapshot, err := session.ConfigPanelSections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("stored agents: %s", snapshot.Stored["agents"])
	var doc map[string]any
	if err := json.Unmarshal(snapshot.Stored["agents"], &doc); err == nil {
		if defs, ok := doc["definitions"].(map[string]any); ok {
			t.Logf("definitions keys: %v", keysOf(defs))
		}
	}
	_ = appcfg.Root{}
	if strings.Contains(pageLabels(selector), "invalid agent id") {
		t.Fatalf("reproduced: invalid agent id reached the page")
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}