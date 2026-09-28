package config

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v2"
)

func TestMarshalYAMLPreservesFalseBooleanConfiguration(t *testing.T) {
	r := Root{
		Gateway: Gateway{HTTPAddr: "127.0.0.1:6060"},
		Agents: AgentsSection{Definitions: map[string]AgentDefinition{
			"main": {Channels: ChannelsSection{
				Telegram: Telegram{Enabled: true, BotToken: "tok"},
				Feishu:   Feishu{Enabled: false, AppID: "kept-under-its-agent"},
				Weixin:   Weixin{Enabled: false, AccountID: "acct"},
			}},
		}},
	}
	b, err := yaml.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(b)
	if !strings.Contains(text, "telegram:") {
		t.Fatalf("enabled telegram channel missing:\n%s", text)
	}
	// A channel the user disabled but still configured keeps both its settings
	// and the explicit false, so turning it back on does not lose them.
	for _, want := range []string{"feishu:", "weixin:", "enabled: false"} {
		if !strings.Contains(text, want) {
			t.Fatalf("false-valued configuration %q missing:\n%s", want, text)
		}
	}
	// Sections that carry content are emitted.
	if !strings.Contains(text, "gateway:") {
		t.Fatalf("section with content %q missing:\n%s", "gateway:", text)
	}
	// A section carrying nothing is dropped rather than written as `key: {}`.
	// Save materializes every optional default first, so the persisted file
	// still documents each setting; see TestSaveWritesNoEmptyMappings.
	if strings.Contains(text, "{}") {
		t.Fatalf("empty mapping written instead of being omitted:\n%s", text)
	}
	if strings.Contains(text, "memories:") {
		t.Fatalf("section without content should be omitted:\n%s", text)
	}
}

func TestMarshalYAMLRoundTripEnabledChannel(t *testing.T) {
	r := Root{Agents: AgentsSection{Definitions: map[string]AgentDefinition{
		"main": {Channels: ChannelsSection{Slack: Slack{Enabled: true, BotToken: "b", InboundPath: "/x"}}},
	}}}
	b, err := yaml.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Root
	if err := yaml.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	slack := ChannelsForAgent(&back, "main").Slack
	if !slack.Enabled || slack.BotToken != "b" || slack.InboundPath != "/x" {
		t.Fatalf("enabled channel did not round-trip: %+v", slack)
	}
}

// A channel section only exists inside an agent definition. Nothing at the top
// level of the file configures a channel, so a configuration that names one
// there is describing an agent that does not exist.
func TestMarshalYAMLWritesNoTopLevelChannelSections(t *testing.T) {
	r := Root{Agents: AgentsSection{Definitions: map[string]AgentDefinition{
		"main": {Channels: ChannelsSection{Telegram: Telegram{Enabled: true, BotToken: "tok"}}},
	}}}
	b, err := yaml.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		for _, channel := range []string{"telegram:", "slack:", "weixin:", "wecom:", "feishu:", "qq:"} {
			if line == channel {
				t.Fatalf("channel %q written at the top level of the config:\n%s", channel, b)
			}
		}
	}
}
