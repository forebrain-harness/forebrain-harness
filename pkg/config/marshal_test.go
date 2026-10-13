package config

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v2"
)

func TestMarshalYAMLPreservesFalseBooleanConfiguration(t *testing.T) {
	disabled := false
	r := Root{
		Gateway: Gateway{HTTPAddr: "127.0.0.1:6060"},
		Agents: AgentsSection{
			Definitions: map[string]AgentDefinition{
				"main": {Primary: true, DisplayName: "Main"},
			},
			Defaults: AgentDefaults{EnableSubagent: &disabled},
		},
	}
	b, err := yaml.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(b)
	if !strings.Contains(text, "main:") {
		t.Fatalf("the agent definition is missing:\n%s", text)
	}
	// An explicit false on a pointer bool is written, so turning the switch
	// back on does not depend on an absent key meaning "off".
	if !strings.Contains(text, "enable_subagent: false") {
		t.Fatalf("explicit false was dropped:\n%s", text)
	}
	// Sections that carry content are emitted.
	if !strings.Contains(text, "gateway:") {
		t.Fatalf("section with content %q missing:\n%s", "gateway:", text)
	}
	// A section carrying nothing is dropped rather than written as `key: {}`.
	if strings.Contains(text, "{}") {
		t.Fatalf("empty mapping written instead of being omitted:\n%s", text)
	}
	if strings.Contains(text, "memories:") {
		t.Fatalf("section without content should be omitted:\n%s", text)
	}
}

// Channels are project data now, so a configuration never names one at any
// level of the file: the old per-agent section is gone.
func TestMarshalYAMLWritesNoChannelSections(t *testing.T) {
	r := Root{Agents: AgentsSection{Definitions: map[string]AgentDefinition{
		"main": {Primary: true},
	}}}
	b, err := yaml.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(b)
	if strings.Contains(text, "channels:") {
		t.Fatalf("a channels section was written to the config:\n%s", text)
	}
	for _, channel := range []string{"telegram:", "slack:", "weixin:", "wecom:", "feishu:", "qq:"} {
		for _, line := range strings.Split(text, "\n") {
			if line == channel {
				t.Fatalf("channel %q written into the config:\n%s", channel, text)
			}
		}
	}
}
