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

// A secret nested in a slice inside a map value is the shape the walker has to
// reach for the settings screens to be safe: an agent's own MCP server carries
// the OAuth token the server was signed in with.
func TestRedactSecretsReachesNestedSettingsInsideAgentDefinitions(t *testing.T) {
	root := Root{Agents: AgentsSection{Definitions: map[string]AgentDefinition{
		"main": {Primary: true, MCPServers: []MCPServerConfig{{
			Name:  "linear",
			OAuth: MCPOAuthConfig{AccessToken: "tok-live"},
		}}},
	}}}
	RedactSecrets(&root)
	if got := root.Agents.Definitions["main"].MCPServers[0].OAuth.AccessToken; got != RedactedSecretPlaceholder {
		t.Fatalf("oauth token = %q, want it hidden", got)
	}
	if got := root.Agents.Definitions["main"].MCPServers[0].Name; got != "linear" {
		t.Fatalf("the server name must survive redaction, got %q", got)
	}
}
