package process

import (
	"context"
	"fmt"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
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
	// URLVerbatim is 「自定义协议」: the request goes to BaseURL exactly as
	// stored, and APIPath only names the wire protocol (plan 267's custom form).
	URLVerbatim bool
	// SubagentAllowed is the authorization page's answer (owner 2026-11-10):
	// may the agent pick this row for a subagent on its own? The flow always
	// asks, so it always writes the answer — also over an existing row, whose
	// user just re-answered the question.
	SubagentAllowed bool
	// CapEfforts are the thinking levels this model supports, for a model no
	// source documents — the custom form asks when nothing knows them. nil
	// leaves the stored levels alone.
	CapEfforts []string
	// CapReasoningOff is whether a mode that turns thinking off exists, as the
	// custom form declared it (plan 311): a capability the row keeps in its own
	// column, never a level. nil leaves the stored value alone.
	CapReasoningOff *bool
	// CatalogProvider is the provider whose model data describes a custom
	// model (owner 2026-11-16); "" names none. Only a custom row may carry it,
	// so Execute writes it only for provider "custom".
	CatalogProvider string
	// CapDefaultEffort is the default thinking level this flow chose for the
	// row (plan 311). nil states nothing about it, so every non-interactive
	// caller keeps its old behaviour; a non-nil value states the level, and a
	// non-nil "" states "the provider decides". Either answer also drops the
	// effort the row's own params pinned, because a stored reasoning.effort
	// outranks the row's default and the new answer would otherwise never
	// reach a request.
	CapDefaultEffort *string
}

type Report struct {
	Home       string
	ConfigPath string
	AgentName  string
	Provider   string
	Model      string
	// ModelID names the fb_llm_models row this flow stored. It is what a
	// session's own model choice carries, so the surface that ran the flow can
	// select the model just configured rather than the agent's default.
	ModelID string
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
	agentName := agentNameOrMain(opts.AgentName)
	// The model configuration is one row of the install-wide table, written
	// with the key as the plaintext value the user typed (owner ruling
	// 2026-10-30, plans 188 W14 and 262). The table is a model key's only
	// source: no environment variable supplies one (plan 268).
	db, err := openStateForWrite(rt)
	if err != nil {
		logSetupError(rt.Home, "setup.execute.save_error", opts, err.Error())
		return rep, err
	}
	defer db.Close()
	// The write below runs the same section gate every other configuration
	// write runs, bound to this call's database. The gate is process-wide and
	// this database closes with the call, so the process's own gate goes back
	// afterwards: leaving the transient one installed would make every later
	// configuration write in this process compose against a closed handle.
	previousGate := state.CurrentSectionValidator()
	InstallConfigSectionValidator(rt.ConfigPath, db)
	defer state.SetSectionValidator(previousGate)

	composed, err := StartupConfig(context.Background(), rt.Home)
	if err != nil {
		logSetupError(rt.Home, "setup.execute.read_error", opts, err.Error())
		return rep, err
	}
	if agentName != "main" {
		// Only main is created by a setup: any other name must already exist,
		// because minting a definition from a typo would silently produce a new
		// agent instead of configuring the intended one.
		if _, ok := composed.Agents.Definitions[agentName]; !ok {
			err := fmt.Errorf("agent %q is not defined in the config", agentName)
			logSetupError(rt.Home, "setup.execute.agent_error", opts, err.Error())
			return rep, err
		}
	}

	in := LLMModelInput{
		Provider:    opts.Provider,
		Model:       opts.Model,
		BaseURL:     opts.BaseURL,
		APIPath:     opts.APIPath,
		URLVerbatim: opts.URLVerbatim,
		// The flow just asked the subagent question, so the answer is a fact
		// about this configuration, not "leave the stored value alone".
		SubagentAllowed: &opts.SubagentAllowed,
	}
	if opts.CapEfforts != nil {
		efforts := append([]string{}, opts.CapEfforts...)
		in.CapEfforts = &efforts
	}
	if opts.CapReasoningOff != nil {
		off := *opts.CapReasoningOff
		in.CapReasoningOff = &off
	}
	// The catalog provider describes where a custom model's official data
	// lives; a built-in provider's row is described by its own pair, so only a
	// custom row is written with one (owner 2026-11-16).
	if strings.EqualFold(strings.TrimSpace(opts.Provider), appcfg.CustomProvider) {
		catalogProvider := strings.Trim(strings.TrimSpace(opts.CatalogProvider), "/")
		in.CatalogProvider = &catalogProvider
	}
	// The api_key requirement is the write's to judge, not a gate here: a row
	// a subscription's Provision already wrote keeps its stored key when the
	// input names none, and a row that would end up keyless is refused by
	// UpsertLLMModel with the same sentence.
	key := strings.TrimSpace(opts.APIKey)
	switch {
	case key != "":
		in.APIKey = &key
	case appcfg.CredentialsBackedProvider(opts.Provider):
		// A login-backed provider authenticates from the login on this machine,
		// so re-configuring it states "no key" rather than "leave the stored
		// key alone": a key left on the row belongs to whatever provider used
		// it before, and for OpenCode it outranks the login.
		empty := ""
		in.APIKey = &empty
	}
	if opts.UpdateParams {
		params := string(parsed)
		in.ParamsJSON = &params
	}
	if opts.CapDefaultEffort != nil {
		// The answer is about the row this save writes, so it is read back the
		// way the write recognises it (a login-backed row carries no path of
		// its own) and its params are the base the stale effort is removed
		// from. A caller that sent no params of its own must not lose the ones
		// the row already holds: only the effort goes.
		held, found, err := LLMModelHeld(context.Background(), db, opts.Provider, opts.Model, opts.BaseURL, opts.APIPath)
		if err != nil {
			logSetupError(rt.Home, "setup.execute.read_error", opts, err.Error())
			return rep, err
		}
		base := parsed
		if !opts.UpdateParams {
			base = nil
			if found {
				base, err = appcfg.ParseLLMRequestParamsJSON(held.ParamsJSON)
				if err != nil {
					// Params this build cannot read are not "no params": a
					// silent empty map would throw away every other request
					// field the row was configured with.
					logSetupError(rt.Home, "setup.execute.params_error", opts, err.Error())
					return rep, err
				}
			}
		}
		params := string(appcfg.WithReasoningEffort(base, ""))
		in.ParamsJSON = &params
		effort := strings.ToLower(strings.TrimSpace(*opts.CapDefaultEffort))
		in.CapDefaultEffort = &effort
	}
	modelID, err := UpsertLLMModel(context.Background(), db, in)
	if err != nil {
		logSetupError(rt.Home, "setup.execute.save_error", opts, err.Error())
		return rep, err
	}
	// The verdict is about the row just written, not about whatever the agent
	// resolved to before: the user configured this model, so this is the one
	// that has to be complete.
	written, err := (&state.LLMModelStore{DB: db}).Get(context.Background(), modelID)
	if err != nil {
		logSetupError(rt.Home, "setup.execute.save_error", opts, err.Error())
		return rep, err
	}
	missing := appcfg.MainAgentLLMValidation{
		Provider: written.Provider,
		Model:    written.Model,
		APIKey:   written.APIKey,
		BaseURL:  written.BaseURL,
	}.MissingFields()
	if len(missing) > 0 {
		logSetupError(rt.Home, "setup.execute.main_llm_incomplete", opts, strings.Join(missing, ", "))
		return rep, fmt.Errorf("%s agent llm requires %s", agentName, strings.Join(missing, ", "))
	}
	// The first model an agent gets is its default; a later one never takes the
	// default from the model the agent already runs on.
	if appcfg.EffectiveDefaultLLMFor(&composed, agentName) == nil {
		if err := SetAgentDefaultModel(context.Background(), db, agentName, modelID); err != nil {
			logSetupError(rt.Home, "setup.execute.save_error", opts, err.Error())
			return rep, err
		}
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
	rep.ModelID = modelID
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
