package process

import (
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

type AgentLLMDefaults struct {
	Provider   string
	Model      string
	ParamsJSON string
}

func ReadAgentLLMDefaults(home string) AgentLLMDefaults {
	return readDefaultAgentLLMDefaults(home, "")
}

// ReadAgentLLMDefaultsForAgent reads the provider a specific agent definition
// currently carries. A setup prompt offers the current values as defaults, and
// for a session running a non-main primary agent those are that agent's values,
// not main's.
func ReadAgentLLMDefaultsForAgent(home, agentName string) AgentLLMDefaults {
	return readDefaultAgentLLMDefaults(home, agentName)
}

func readDefaultAgentLLMDefaults(home, agentName string) AgentLLMDefaults {
	cfg := loadCfg(home)
	if cfg == nil {
		return AgentLLMDefaults{}
	}
	// The model an empty selection runs on for this agent: its own default, the
	// first model of its own list, then main's choice — the one view the model
	// table and a pre-table list both feed (plans 260, 262).
	p := appcfg.EffectiveDefaultLLMFor(cfg, agentName)
	if p == nil {
		return AgentLLMDefaults{}
	}
	out := AgentLLMDefaults{
		Provider: strings.TrimSpace(p.Provider),
		Model:    strings.TrimSpace(p.Model),
	}
	if len(p.Params) > 0 {
		out.ParamsJSON = string(p.Params)
	}
	return out
}

func agentNameOrMain(agentName string) string {
	if name := strings.TrimSpace(agentName); name != "" {
		return name
	}
	return "main"
}
