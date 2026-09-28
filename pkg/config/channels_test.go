package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two primary agents each run their own accounts. Reading one agent's channels
// must never surface another's, and an agent that configured none has none —
// there is no shared set to inherit.
func TestChannelsAreIsolatedBetweenPrimaryAgents(t *testing.T) {
	cfg := &Root{}
	SetChannelsForAgent(cfg, "main", ChannelsSection{
		Telegram: Telegram{Enabled: true, BotToken: "main-token"},
	})
	SetChannelsForAgent(cfg, "acme", ChannelsSection{
		Feishu: Feishu{Enabled: true, AppID: "cli_acme"},
	})
	cfg.Agents.Definitions["acme"] = AgentDefinition{
		Primary:  true,
		Channels: cfg.Agents.Definitions["acme"].Channels,
	}

	main := ChannelsForAgent(cfg, "main")
	acme := ChannelsForAgent(cfg, "acme")
	if main.Telegram.BotToken != "main-token" || main.Feishu.Enabled {
		t.Fatalf("main channels = %+v", main)
	}
	if acme.Feishu.AppID != "cli_acme" || acme.Telegram.Enabled {
		t.Fatalf("acme channels = %+v", acme)
	}
	if got := ChannelsForAgent(cfg, "unknown"); !got.IsZero() {
		t.Fatalf("an undefined agent inherited channels: %+v", got)
	}
	if got := ChannelsForAgent(nil, "main"); !got.IsZero() {
		t.Fatalf("nil config produced channels: %+v", got)
	}
}

// A channel binds an external account to one tenant's sessions. Subagents are
// not tenants, so a definition that is not an effective primary agent must be
// rejected outright rather than carrying configuration that can never run.
func TestLoadRejectsChannelsOnNonPrimaryAgentDefinition(t *testing.T) {
	t.Setenv("FOREBRAIN_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	body := `
agents:
  definitions:
    explore:
      channels:
        telegram:
          enabled: true
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected a subagent definition with channels to be rejected")
	}
	if !strings.Contains(err.Error(), "only primary agents may configure channels") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadAcceptsChannelsOnEveryPrimaryAgent(t *testing.T) {
	t.Setenv("FOREBRAIN_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	body := `
agents:
  definitions:
    main:
      channels:
        slack:
          enabled: true
    acme:
      primary: true
      channels:
        slack:
          enabled: true
          inbound_path: "/acme/slack"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Defaults are filled in per agent, so each agent's unset fields resolve
	// on their own rather than borrowing main's.
	if got := ChannelsForAgent(&cfg, "main").Slack.InboundPath; got != "/channels/slack/inbound" {
		t.Fatalf("main slack inbound path = %q", got)
	}
	if got := ChannelsForAgent(&cfg, "acme").Slack.InboundPath; got != "/acme/slack" {
		t.Fatalf("acme slack inbound path = %q", got)
	}
}

// Only primary agents get channel defaults materialized. Filling them in on a
// subagent definition would write configuration into forebrain.yaml that can never
// take effect, and would defeat the check above by making every definition look
// like it declared channels.
func TestNormalizeLeavesSubagentDefinitionChannelsUntouched(t *testing.T) {
	r := &Root{}
	r.Agents.Definitions = map[string]AgentDefinition{
		"main":    {},
		"explore": {},
	}
	normalize(r)

	if got := ChannelsForAgent(r, "main").Slack.InboundPath; got != "/channels/slack/inbound" {
		t.Fatalf("main did not get channel defaults: %q", got)
	}
	if got := ChannelsForAgent(r, "explore"); !got.IsZero() {
		t.Fatalf("subagent definition got channel defaults: %+v", got)
	}
}

// Secrets are stored per agent in ~/.forebrain/.env and referenced from the
// agent's own section, so resolving them must not depend on any shared name.
func TestEnvReferencesResolveInsideAgentChannels(t *testing.T) {
	t.Setenv("FOREBRAIN_HOME", t.TempDir())
	t.Setenv("FOREBRAIN_MAIN_TELEGRAM_BOT_TOKEN", "main-secret")
	t.Setenv("FOREBRAIN_ACME_TELEGRAM_BOT_TOKEN", "acme-secret")
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	body := `
agents:
  definitions:
    main:
      channels:
        telegram:
          enabled: true
          bot_token: "${FOREBRAIN_MAIN_TELEGRAM_BOT_TOKEN}"
    acme:
      primary: true
      channels:
        telegram:
          enabled: true
          bot_token: "${FOREBRAIN_ACME_TELEGRAM_BOT_TOKEN}"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := ChannelsForAgent(&cfg, "main").Telegram.BotToken; got != "main-secret" {
		t.Fatalf("main token = %q", got)
	}
	if got := ChannelsForAgent(&cfg, "acme").Telegram.BotToken; got != "acme-secret" {
		t.Fatalf("acme token = %q", got)
	}
}

// A plaintext channel secret is rejected wherever it appears, including inside
// an agent definition: the scan walks keys, not a fixed list of sections.
func TestLoadRejectsPlaintextSecretInsideAgentChannels(t *testing.T) {
	t.Setenv("FOREBRAIN_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	body := `
agents:
  definitions:
    main:
      channels:
        telegram:
          enabled: true
          bot_token: "1234:plaintext"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected a plaintext channel secret under an agent to be rejected")
	}
}
