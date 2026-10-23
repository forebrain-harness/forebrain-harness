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
	p := appcfg.EffectiveDefaultLLM(def)
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
	// that pair: an existing entry is updated in place (position unchanged),
	// a new one is appended to the end. The list's order is user data — no
	// write path reorders it or moves anything to a "primary" slot.
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
		target.APIKeyCleared = false
	} else if appcfg.CredentialsBackedProvider(p) {
		// These providers authenticate with the login on this machine
		// (opencode.json, auth.json), not with a key in the yaml. A key already
		// on the entry belongs to whatever provider used this slot before — the
		// credential the flow just replaced — and for OpenCode it outranks the
		// login in the client's resolution order, so the fresh login would never
		// be sent. ChatGPT's client ignores a key outright, which leaves only
		// misleading foreign data; both are cleared here. The marker tells Save
		// this emptiness is a decision, so it does not restore what is on disk.
		target.APIKey = ""
		target.APIKeyCleared = true
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
	// No promotion: the entry stays where it is (existing) or at the end
	// (new). An empty placeholder left behind (e.g. the bare entry main
	// carries before first setup) is dropped on save by
	// dropPlaceholderLLMProviders, so this never persists a `- {}` stub.
	appcfg.NormalizeLLMProviderEntries(&def)
	cfg.Agents.Definitions[name] = def
}
