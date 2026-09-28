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
	AgentName      string
	Provider       string
	APIKey         string
	BaseURL        string
	APIPath        string
	Model          string
	ParamsJSON     string
	UpdateParams   bool
	UpdateChannels bool
	EnableWeixin   bool
	EnableFeishu   bool
	EnableDingtalk bool
	EnableQQ       bool
}

type Report struct {
	Home         string
	ConfigPath   string
	AgentName    string
	Provider     string
	Model        string
	PlatformList []string
}

func Execute(rt Context, opts Options) (Report, error) {
	rep := Report{
		Home:       rt.Home,
		ConfigPath: rt.ConfigPath,
	}
	logSetupEvent(rt.Home, "setup.execute.start", opts, map[string]any{
		"config_path":     rt.ConfigPath,
		"update_params":   opts.UpdateParams,
		"update_channels": opts.UpdateChannels,
	})
	if strings.TrimSpace(opts.Provider) == "" {
		logSetupError(rt.Home, "setup.execute.validation_error", opts, "provider required in non-interactive mode")
		return rep, fmt.Errorf("provider required in non-interactive mode")
	}
	if strings.TrimSpace(opts.Model) == "" {
		logSetupError(rt.Home, "setup.execute.validation_error", opts, "model required in non-interactive mode")
		return rep, fmt.Errorf("model required in non-interactive mode")
	}
	if strings.TrimSpace(opts.APIKey) == "" && !strings.EqualFold(strings.TrimSpace(opts.Provider), "chatgpt") {
		logSetupError(rt.Home, "setup.execute.validation_error", opts, "api_key required in non-interactive mode")
		return rep, fmt.Errorf("api_key required in non-interactive mode")
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		logSetupError(rt.Home, "setup.execute.validation_error", opts, "base_url required in non-interactive mode")
		return rep, fmt.Errorf("base_url required in non-interactive mode")
	}
	// Channels belong to the agent being configured, so the toggles land on
	// that agent's section and nowhere else.
	channelAgent := agentNameOrMain(opts.AgentName)
	if opts.UpdateChannels {
		channels := appcfg.ChannelsForAgent(&rt.Config, channelAgent)
		channels.Weixin.Enabled = opts.EnableWeixin
		channels.Feishu.Enabled = opts.EnableFeishu
		channels.Dingtalk.Enabled = opts.EnableDingtalk
		channels.QQ.Enabled = opts.EnableQQ
		appcfg.SetChannelsForAgent(&rt.Config, channelAgent, channels)
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
	apiKey, err := apiKeyConfigReference(rt.Home, opts.Provider, opts.APIKey)
	if err != nil {
		logSetupError(rt.Home, "setup.execute.api_key_error", opts, err.Error())
		return rep, err
	}
	opts.APIKey = apiKey
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
	reportChannels := appcfg.ChannelsForAgent(&rt.Config, channelAgent)
	if reportChannels.Weixin.Enabled {
		rep.PlatformList = append(rep.PlatformList, "weixin")
	}
	if reportChannels.Feishu.Enabled {
		rep.PlatformList = append(rep.PlatformList, "feishu")
	}
	if reportChannels.Dingtalk.Enabled {
		rep.PlatformList = append(rep.PlatformList, "dingtalk")
	}
	if reportChannels.QQ.Enabled {
		rep.PlatformList = append(rep.PlatformList, "qq")
	}
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
