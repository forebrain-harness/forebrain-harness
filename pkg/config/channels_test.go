package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A stored channel section is read back with its kind defaults filled in, so a
// channel written by one surface renders the same everywhere.
func TestParseChannelsSectionJSONFillsDefaults(t *testing.T) {
	sec, err := ParseChannelsSectionJSON(`{"slack":{"enabled":true,"inbound_path":"/acme/slack"}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !sec.Slack.Enabled || sec.Slack.InboundPath != "/acme/slack" {
		t.Fatalf("slack = %+v", sec.Slack)
	}
	// A kind the section did not name still gets its own defaults, so reading
	// one channel does not depend on another having been set.
	if sec.Email.InboundPath != "/channels/email/inbound" {
		t.Fatalf("email default not filled: %q", sec.Email.InboundPath)
	}
}

// A stored section's secret fields hold ${VAR} references; reading it back
// resolves them from the process environment the loader merged ~/.forebrain/.env
// into.
func TestParseChannelsSectionJSONResolvesEnvReferences(t *testing.T) {
	t.Setenv("FOREBRAIN_CH_ACME_TELEGRAM_BOT_TOKEN", "acme-secret")
	sec, err := ParseChannelsSectionJSON(`{"telegram":{"enabled":true,"bot_token":"${FOREBRAIN_CH_ACME_TELEGRAM_BOT_TOKEN}"}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := sec.Telegram.BotToken; got != "acme-secret" {
		t.Fatalf("telegram token = %q", got)
	}
}

// MarshalChannelsSection round-trips a section unchanged, so a channel written
// from a section read back is the same channel.
func TestMarshalChannelsSectionRoundTrips(t *testing.T) {
	in := ChannelsSection{Telegram: Telegram{Enabled: true, BotToken: "plain-token"}}
	raw, err := MarshalChannelsSection(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := ParseChannelsSectionJSON(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Telegram.BotToken != "plain-token" || !out.Telegram.Enabled {
		t.Fatalf("round trip changed the section: %+v", out.Telegram)
	}
}

// Load still rejects a plaintext channel secret wherever it appears: the scan
// walks keys, not a fixed list of sections.
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

// A legacy primary-agent-level channels section is stripped from the file, with
// the original bytes kept in a backup, and the channels that were enabled are
// reported so the caller can warn that they stopped.
func TestMigrateLegacyAgentChannelsStripsAndReportsEnabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forebrain.yaml")
	body := `agents:
  definitions:
    main:
      channels:
        telegram:
          enabled: true
        slack:
          enabled: false
    acme:
      primary: true
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	mig, err := MigrateLegacyAgentChannels(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !mig.Removed {
		t.Fatal("expected the channels section to be removed")
	}
	if len(mig.Enabled) != 1 || mig.Enabled[0] != "telegram" {
		t.Fatalf("enabled = %v, want [telegram]", mig.Enabled)
	}
	if mig.Backup == "" {
		t.Fatal("expected a backup path")
	}
	backup, err := os.ReadFile(mig.Backup)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backup) != body {
		t.Fatalf("backup does not match the original file")
	}
	rewritten, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rewritten), "channels:") {
		t.Fatalf("channels section survived the rewrite:\n%s", rewritten)
	}

	// Idempotent: a second run finds nothing to remove.
	again, err := MigrateLegacyAgentChannels(path)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if again.Removed {
		t.Fatal("a second run removed something")
	}
}

// A configuration with no channels section is left byte for byte as it is.
func TestMigrateLegacyAgentChannelsLeavesCleanConfigAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forebrain.yaml")
	body := "agents:\n  definitions:\n    main: {}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	mig, err := MigrateLegacyAgentChannels(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if mig.Removed {
		t.Fatal("a clean config was reported as removed")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != body {
		t.Fatalf("a clean config was rewritten:\n%s", after)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".bak-") {
			t.Fatalf("a clean config got a backup: %s", e.Name())
		}
	}
}

// A missing configuration is not an error: nothing to migrate.
func TestMigrateLegacyAgentChannelsMissingFile(t *testing.T) {
	mig, err := MigrateLegacyAgentChannels(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if mig.Removed {
		t.Fatal("a missing file reported a removal")
	}
}
