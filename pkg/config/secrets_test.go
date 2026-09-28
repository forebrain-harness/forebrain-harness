package config

import "testing"

// A settings screen must be able to show that a secret is set without being
// able to read it, and to send the form back without wiping it.
func TestRedactSecretsRoundTripsThroughASettingsForm(t *testing.T) {
	section := ChannelsSection{
		Telegram: Telegram{Enabled: true, BotToken: "real-token"},
		Slack:    Slack{Enabled: true, InboundPath: "/slack"},
	}
	RedactSecrets(&section)
	if section.Telegram.BotToken != RedactedSecretPlaceholder {
		t.Fatalf("a set secret must be hidden, got %q", section.Telegram.BotToken)
	}
	if section.Slack.BotToken != "" {
		t.Fatalf("an unset secret must stay empty so the screen can tell them apart, got %q", section.Slack.BotToken)
	}
	if section.Slack.InboundPath != "/slack" || !section.Telegram.Enabled {
		t.Fatalf("redaction must leave non-secret settings alone: %#v", section)
	}

	ClearRedactedSecrets(&section)
	if section.Telegram.BotToken != "" {
		t.Fatalf("a returned placeholder must become empty so the saved file keeps its own value, got %q", section.Telegram.BotToken)
	}
}

// Provider keys live in a slice inside a map value, which is the shape the
// walker has to reach for the settings screens to be safe.
func TestRedactSecretsReachesProvidersInsideAgentDefinitions(t *testing.T) {
	root := Root{Agents: AgentsSection{Definitions: map[string]AgentDefinition{
		"main": {Primary: true, LLMProviders: []AgentLLMProviderConfig{{Provider: "openai", APIKey: "sk-live"}}},
	}}}
	RedactSecrets(&root)
	if got := root.Agents.Definitions["main"].LLMProviders[0].APIKey; got != RedactedSecretPlaceholder {
		t.Fatalf("provider key = %q, want it hidden", got)
	}
	if got := root.Agents.Definitions["main"].LLMProviders[0].Provider; got != "openai" {
		t.Fatalf("provider name must survive redaction, got %q", got)
	}
}
