package process

import (
	"fmt"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

type AgentLLMDefaults struct {
	Provider   string
	Model      string
	ParamsJSON string
}

type agentLLMDefaults = AgentLLMDefaults

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
	def, ok := cfg.Agents.Definitions[agentNameOrMain(agentName)]
	if !ok {
		return AgentLLMDefaults{}
	}
	p := appcfg.PrimaryLLM(def)
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

// ApplyAgentLLMToConfigForAgent writes the provider into one named agent
// definition. An empty name means main, which is the only definition this may
// create: any other name must already exist, because minting a definition from
// a typo would silently produce a new agent instead of configuring the intended
// one.
func ApplyAgentLLMToConfigForAgent(cfg *appcfg.Root, agentName, provider, model, apiKey, baseURL, apiPath string, updateParams bool, params appcfg.LLMRequestParams) error {
	if cfg == nil {
		return fmt.Errorf("nil config")
	}
	name := agentNameOrMain(agentName)
	if name != "main" {
		if _, ok := cfg.Agents.Definitions[name]; !ok {
			return fmt.Errorf("agent %q is not defined in the config", name)
		}
	}
	applyAgentLLMFields(cfg, name, provider, model, apiKey, baseURL, apiPath, updateParams, params)
	return nil
}

func agentNameOrMain(agentName string) string {
	if name := strings.TrimSpace(agentName); name != "" {
		return name
	}
	return "main"
}

func applyAgentLLMFields(cfg *appcfg.Root, agentName, provider, model, apiKey, baseURL, apiPath string, updateParams bool, params appcfg.LLMRequestParams) {
	if cfg.Agents.Definitions == nil {
		cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{}
	}
	name := agentNameOrMain(agentName)
	def := cfg.Agents.Definitions[name]
	if name == "main" {
		// Only main is primary by construction. For any other agent the flag
		// already reflects what it is, and forcing it here would promote a
		// helper agent to a primary one as a side effect of setting its model.
		def.Primary = true
	}
	appcfg.NormalizeLLMProviderEntries(&def)

	// /connect (and the setup flows that share this function) configure one
	// provider+model connection per entry, so the write is add-or-replace by
	// that pair, not an overwrite of the primary slot: mutating
	// LLMProviders[0] would discard whatever provider was primary, and
	// matching on the provider alone would replace the model an existing
	// entry for that provider carries. Find an existing entry for this
	// provider+model and update it; when none exists, add a new entry.
	p := strings.TrimSpace(provider)
	m := strings.TrimSpace(model)
	idx := appcfg.FindLLMProviderIndex(def, p, m)
	var target *appcfg.AgentLLMProviderConfig
	if idx >= 0 {
		target = &def.LLMProviders[idx]
	} else {
		def.LLMProviders = append(def.LLMProviders, appcfg.AgentLLMProviderConfig{})
		idx = len(def.LLMProviders) - 1
		target = &def.LLMProviders[idx]
	}
	if p != "" {
		target.Provider = p
	}
	if m := strings.TrimSpace(model); m != "" {
		target.Model = m
	}
	if k := strings.TrimSpace(apiKey); k != "" {
		target.APIKey = k
	}
	if u := strings.TrimSpace(baseURL); u != "" {
		target.BaseURL = u
	}
	target.APIPath = strings.TrimSpace(apiPath)
	if updateParams {
		if len(params) == 0 {
			target.Params = nil
		} else {
			target.Params = append(appcfg.LLMRequestParams(nil), params...)
		}
	}
	// Promote the configured entry to the primary slot so the change governs
	// the next turn without a restart — the same effect /model achieves with
	// PromoteMatchingLLMProvider. An empty placeholder left behind (e.g. the
	// bare entry main carries before first setup) is dropped on save by
	// dropPlaceholderLLMProviders, so this never persists a `- {}` stub.
	if idx > 0 {
		entry := def.LLMProviders[idx]
		copy(def.LLMProviders[1:idx+1], def.LLMProviders[0:idx])
		def.LLMProviders[0] = entry
	}
	appcfg.NormalizeLLMProviderEntries(&def)
	cfg.Agents.Definitions[name] = def
}
