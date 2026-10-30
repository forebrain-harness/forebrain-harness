package process

import (
	"fmt"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
)

type Options struct {
	// AgentName is the agent definition this setup configures. Empty means the
	// main agent, which is what onboarding and `forebrain setup` configure. The
	// TUI's /connect passes the active primary agent so the session it is
	// running configures itself rather than main.
	AgentName    string
	Provider     string
	APIKey       string
	BaseURL      string
	APIPath      string
	Model        string
	ParamsJSON   string
	UpdateParams bool
}

type Report struct {
	Home       string
	ConfigPath string
	AgentName  string
	Provider   string
	Model      string
}

func Execute(rt Context, opts Options) (Report, error) {
	rep := Report{
		Home:       rt.Home,
		ConfigPath: rt.ConfigPath,
	}
	logSetupEvent(rt.Home, "setup.execute.start", opts, map[string]any{
		"config_path":   rt.ConfigPath,
		"update_params": opts.UpdateParams,
	})
	if strings.TrimSpace(opts.Provider) == "" {
		logSetupError(rt.Home, "setup.execute.validation_error", opts, "provider required in non-interactive mode")
		return rep, fmt.Errorf("provider required in non-interactive mode")
	}
	if strings.TrimSpace(opts.Model) == "" {
		logSetupError(rt.Home, "setup.execute.validation_error", opts, "model required in non-interactive mode")
		return rep, fmt.Errorf("model required in non-interactive mode")
	}
	if strings.TrimSpace(opts.APIKey) == "" && !appcfg.CredentialsBackedProvider(opts.Provider) {
		logSetupError(rt.Home, "setup.execute.validation_error", opts, "api_key required in non-interactive mode")
		return rep, fmt.Errorf("api_key required in non-interactive mode")
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		logSetupError(rt.Home, "setup.execute.validation_error", opts, "base_url required in non-interactive mode")
		return rep, fmt.Errorf("base_url required in non-interactive mode")
	}
	var parsed appcfg.LLMRequestParams
	if opts.UpdateParams {
		var err error
		parsed, err = appcfg.ParseLLMRequestParamsJSON(opts.ParamsJSON)
		if err != nil {
			logSetupError(rt.Home, "setup.execute.params_error", opts, err.Error())
			return rep, err
		}
	}
	// The key is stored as the plaintext value the user typed, directly in
	// the agents section (owner ruling 2026-10-30, plan 188 W14): the home's
	// .env is the user's own file and no code path writes it for a provider
	// key anymore.
	agentName := agentNameOrMain(opts.AgentName)
	if err := ApplyAgentLLMToConfigForAgent(&rt.Config, agentName, opts.Provider, opts.Model, opts.APIKey, opts.BaseURL, opts.APIPath, opts.UpdateParams, parsed); err != nil {
		logSetupError(rt.Home, "setup.execute.agent_error", opts, err.Error())
		return rep, err
	}
	if missing := appcfg.ValidateAgentLLMConfigured(&rt.Config, agentName).MissingFields(); len(missing) > 0 {
		logSetupError(rt.Home, "setup.execute.main_llm_incomplete", opts, strings.Join(missing, ", "))
		return rep, fmt.Errorf("%s agent llm requires %s", agentName, strings.Join(missing, ", "))
	}
	if err := SaveConfig(rt); err != nil {
		logSetupError(rt.Home, "setup.execute.save_error", opts, err.Error())
		return rep, err
	}
	ResetResolve()
	if err := VerifyProvider(strings.TrimSpace(opts.Provider)); err != nil {
		logSetupError(rt.Home, "setup.execute.provider_error", opts, err.Error())
		return rep, err
	}
	updated := readDefaultAgentLLMDefaults(rt.Home, agentName)
	rep.AgentName = agentName
	rep.Provider = updated.Provider
	rep.Model = updated.Model
	_ = home.EnsureWorkspace(rt.Home, home.EnsureOptions{})
	logSetupEvent(rt.Home, "setup.execute.success", opts, map[string]any{
		"config_path": rt.ConfigPath,
		"provider":    rep.Provider,
		"model":       rep.Model,
	})
	return rep, nil
}

func logSetupError(home string, event string, opts Options, errText string) {
	logSetupEvent(home, event, opts, map[string]any{"error": errText})
}

func logSetupEvent(home string, event string, opts Options, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["provider"] = strings.TrimSpace(opts.Provider)
	fields["model"] = strings.TrimSpace(opts.Model)
	fields["base_url"] = strings.TrimSpace(opts.BaseURL)
	fields["api_path"] = strings.TrimSpace(opts.APIPath)
	fields["api_key_set"] = strings.TrimSpace(opts.APIKey) != ""
	telemetry.SetupEvent(home, event, fields)
}
