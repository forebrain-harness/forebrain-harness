package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

func newReproSession(t *testing.T) (*ChatSession, func() string) {
	t.Helper()
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
	t.Cleanup(func() { db.Close() })
	models := &state.LLMModelStore{DB: db}
	if _, err := models.Create(ctx, state.LLMModel{Provider: "deepseek", Model: "deepseek-flash", BaseURL: "https://api.deepseek.com", APIPath: "/v1/chat/completions"}); err != nil {
		t.Fatal(err)
	}
	process.InstallConfigSectionValidator(cfgPath, db)
	t.Cleanup(func() { state.SetSectionValidator(nil) })
	env := &process.Environment{Root: home, SQL: db}
	env.ConfigPath = cfgPath
	session := &ChatSession{Env: env}
	if err := session.UpdateConfigSection(ctx, "agents", json.RawMessage(`{"definitions":{"main":{"primary":true}}}`)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	stored := func() string {
		snapshot, err := session.ConfigPanelSections(ctx)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		return string(snapshot.Stored["agents"])
	}
	return session, stored
}

// V1: inside the agent create form, open Subagents, create a custom subagent
// entry (which saves on its own), then keep editing the agent form.
func TestZZReproSubagentInsideCreate(t *testing.T) {
	session, stored := newReproSession(t)
	selector := &scriptedSelector{}
	renderer := runConfigPanelOn(t, session, selector, []richStep{
		{label: "Agent definitions", ok: true},
		{label: configAddRowLabel, ok: true},
		{label: "Name", ok: true},
		{label: "Subagents", ok: true},
		{label: configAddRowLabel, ok: true},
		{label: "Name", ok: true},
		{label: configCreateSaveLabel, ok: true},
		{ok: false},
		{label: "Model", ok: true},
		{label: "deepseek/deepseek-flash", ok: true},
		{ok: false},
		{label: configCreateSaveLabel, ok: true},
		{ok: false},
		{ok: false},
		{ok: false},
	}, []selectorInput{
		{value: "frontend-expert", ok: true},
		{value: "helper", ok: true},
	}, "agents")

	t.Logf("pages:\n%s", pageLabels(selector))
	t.Logf("frames:\n%s", frameText(renderer))
	t.Logf("stored agents: %s", stored())
}