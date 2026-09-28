package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/joho/godotenv"
)

func mergeEnvFileInto(dst map[string]string, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	m, err := godotenv.Unmarshal(strings.ReplaceAll(string(b), "\r\n", "\n"))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for k, v := range m {
		dst[k] = v
	}
	return nil
}

func mergeDotEnvFromForebrainHome() (map[string]string, string, error) {
	out := make(map[string]string)
	root, err := home.Root()
	if err != nil {
		return nil, "", err
	}
	p := home.DefaultEnvPath(root)
	if err := mergeEnvFileInto(out, p); err != nil {
		return nil, "", err
	}
	return out, p, nil
}

func applyMergedEnvToProcess(m map[string]string) error {
	var firstErr error
	for k, v := range m {
		if err := os.Setenv(k, v); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// MergeDotEnvToProcess merges ~/.forebrain/.env into the process environment
// (os.Setenv for each key) and returns the merged map. It is idempotent: each
// call re-reads the on-disk .env so later edits (e.g. a user adding a new
// OPENAI_API_KEY assignment) are picked up. Missing file is not an error.
//
// This is the single entry point every code path that resolves ${ENV_VAR}
// placeholders via os.Getenv at build time (notably the agent LLM client in
// run.Runner.loadLocked) MUST call, because config.LoadPersisted - used by
// read-modify-write flows such as the /model slash command - intentionally does
// NOT merge .env or expand placeholders. Without this call, a ${OPENAI_API_KEY}
// reference resolves to empty and the LLM call fails with an auth error unless
// some earlier config.Load already populated os.Environ as a side effect.
func MergeDotEnvToProcess() (map[string]string, error) {
	m, _, err := mergeDotEnvFromForebrainHome()
	if err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return m, nil
	}
	if err := applyMergedEnvToProcess(m); err != nil {
		return m, err
	}
	return m, nil
}

func fromMergedMap(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(m[k]); v != "" {
			return v
		}
	}
	return ""
}

func applyLLMProvidersFromMap(chain []AgentLLMProviderConfig, m map[string]string) []AgentLLMProviderConfig {
	get := func(k string) string {
		if m == nil {
			return ""
		}
		return m[k]
	}
	for i := range chain {
		// Only fill in api_key/base_url when the config did not specify one.
		// An explicit value (including a ${ENV_VAR} placeholder, which
		// expandRootEnvReferences resolves later) must NOT be overwritten by a
		// provider-name-derived env var. Otherwise a provider "deepseek" with
		// api_key: ${OPENAI_API_KEY} would be clobbered by DEEPSEEK_API_KEY
		// whenever both are present in ~/.forebrain/.env, silently substituting
		// the wrong key and causing an LLM auth error at runtime.
		if strings.TrimSpace(chain[i].APIKey) == "" {
			if v := ProviderAPIKeyLookup(get, chain[i].Provider); v != "" {
				chain[i].APIKey = v
			}
		}
		if strings.TrimSpace(chain[i].BaseURL) == "" {
			if v := ProviderBaseURLLookup(get, chain[i].Provider); v != "" {
				chain[i].BaseURL = v
			}
		}
	}
	return chain
}

func applyMergedEnvOverrides(r *Root, m map[string]string) {
	if r == nil || len(m) == 0 {
		return
	}
	set := func(dst *string, keys ...string) {
		if v := fromMergedMap(m, keys...); v != "" {
			*dst = v
		}
	}
	set(&r.Gateway.Auth.Token, "FOREBRAIN_GATEWAY_TOKEN", "GATEWAY_TOKEN")
	// Channels are deliberately absent here. A bare TELEGRAM_BOT_TOKEN in the
	// environment would configure "the" Telegram channel, but channels belong
	// to a named primary agent (agents.definitions.<id>.channels), so there is
	// no such thing. Channel secrets reach the config as ${ENV_NAME}
	// references, which expandRootEnvReferences resolves wherever they appear,
	// and onboarding writes agent-scoped names so two agents configuring the
	// same channel cannot collide on one variable.
	if v := fromMergedMap(m, "TAVILY_API_KEY"); v != "" {
		if r.Agents.Defaults.WebSearch.Tavily == nil {
			r.Agents.Defaults.WebSearch.Tavily = &WebSearchTavily{}
		}
		r.Agents.Defaults.WebSearch.Tavily.APIKey = v
	}
	if v := fromMergedMap(m, "BRAVE_SEARCH_API_KEY"); v != "" {
		if r.Agents.Defaults.WebSearch.Brave == nil {
			r.Agents.Defaults.WebSearch.Brave = &WebSearchBrave{}
		}
		r.Agents.Defaults.WebSearch.Brave.APIKey = v
	}
	if v := fromMergedMap(m, "BAIDU_WEB_SEARCH_API_KEY"); v != "" {
		if r.Agents.Defaults.WebSearch.Baidu == nil {
			r.Agents.Defaults.WebSearch.Baidu = &WebSearchBaidu{}
		}
		r.Agents.Defaults.WebSearch.Baidu.APIKey = v
	}
	for k, def := range r.Agents.Definitions {
		def.LLMProviders = applyLLMProvidersFromMap(def.LLMProviders, m)
		r.Agents.Definitions[k] = def
	}
}

func applyDotEnvAfterNormalize(_ string, r *Root) error {
	m, envPath, err := mergeDotEnvFromForebrainHome()
	if err != nil {
		return err
	}
	if len(m) == 0 {
		return nil
	}
	// The .env took part in this configuration, so it is one of the files the
	// configuration was read from.
	r.SourceFiles = append(r.SourceFiles, envPath)
	return applyMergedEnvForLoad(r, m, applyMergedEnvToProcess)
}

func applyMergedEnvForLoad(r *Root, m map[string]string, applyProcess func(map[string]string) error) error {
	applyMergedEnvOverrides(r, m)
	if applyProcess == nil {
		return nil
	}
	return applyProcess(m)
}
