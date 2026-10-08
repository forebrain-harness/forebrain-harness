// First-run setup: the wizard, prompts, connect flow, and resolution.
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	homepkg "github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
)

type Wizard struct {
	rt          process.Context
	out         io.Writer
	selector    Selector
	readLine    func(context.Context) (string, error)
	interactive bool
	rawInput    bool
}

func RunOnboarding(ctx context.Context, in io.Reader, out io.Writer) error {
	rt, err := process.Resolve()
	if err != nil {
		return err
	}
	readLine := tuiReadLine(ctx, in)
	w := &Wizard{
		rt:       rt,
		out:      ensureWriter(out),
		readLine: readLine,
	}
	restore := func() {}
	if f, ok := in.(*os.File); ok {
		if sel, wrapped, restorePages, ready := NewStandaloneRawSelector(f, out); ready {
			restore = restorePages
			w.out = ensureWriter(wrapped)
			w.selector = sel
			w.rawInput = true
		}
	}
	if w.selector == nil {
		w.selector = NewSelector(out, readLine)
	}
	err = w.run(ctx)
	// The pages end before anything is printed for good: what the setup did
	// belongs to the terminal's own screen, above the conversation it opens.
	restore()
	if err == nil {
		w.printSummary(ensureWriter(out))
	}
	return err
}

func restoreInteractiveTerminalStyle(out io.Writer) {
	if out == nil {
		return
	}
	_, _ = fmt.Fprint(out, "\x1b[0m\x1b[?25h")
}

// onboardPages is how many pages the onboarding has: the model setup's, then
// the channels.
const onboardPages = llmSetupPages + 1

// run walks the onboarding's pages: the model setup when no model is
// configured yet, then the channels. Esc on the channels goes back to the
// model setup's review.
func (w *Wizard) run(ctx context.Context) error {
	w.out = ensureWriter(w.out)
	defer restoreInteractiveTerminalStyle(w.out)
	w.log("onboard.start", map[string]any{
		"config_path":     w.rt.ConfigPath,
		"main_configured": mainLLMConfigured(&w.rt.Config),
	})
	var setup *llmSetup
	if mainLLMConfigured(&w.rt.Config) {
		w.log("onboard.llm.already_configured", map[string]any{"summary": mainLLMSummary(&w.rt.Config)})
	} else {
		w.log("onboard.llm.configure_start", nil)
		setup = newLLMSetup(PromptConfig{
			Context:        ctx,
			Output:         w.out,
			Selector:       w.selector,
			Defaults:       process.ReadAgentLLMDefaults(w.rt.Home),
			CurrentBaseURL: mainLLMBaseURL(&w.rt.Config),
			CurrentAPIPath: mainLLMAPIPath(&w.rt.Config),
			Home:           w.rt.Home,
			ConfigPath:     w.rt.ConfigPath,
			Onboarding:     true,
			Pages:          onboardPages,
		})
	}
	for {
		if setup != nil {
			if err := w.configureMainLLM(setup); err != nil {
				return err
			}
		}
		back, err := w.configureChannels(setup != nil)
		if err != nil {
			w.log("onboard.error", map[string]any{"stage": "channels", "error": err.Error()})
			return err
		}
		if !back {
			break
		}
		setup.resumeAt(setupPageReview)
	}
	w.log("onboard.success", map[string]any{
		"main_llm":        mainLLMSummary(&w.rt.Config),
		"channel_summary": w.channelSummary(),
	})
	return nil
}

func ensureWriter(out io.Writer) io.Writer {
	if out == nil {
		return io.Discard
	}
	return out
}

// configureMainLLM runs the model setup and saves what it answers.
func (w *Wizard) configureMainLLM(setup *llmSetup) error {
	opts, ok, err := setup.run()
	if err != nil || !ok {
		w.log("onboard.llm.configure_cancelled", map[string]any{"error": wizardErr(err)})
		return cancelledErr(err)
	}
	w.log("onboard.llm.execute_start", map[string]any{
		"provider":    opts.Provider,
		"model":       opts.Model,
		"base_url":    opts.BaseURL,
		"api_path":    opts.APIPath,
		"api_key_set": strings.TrimSpace(opts.APIKey) != "",
	})
	if _, err := process.Execute(w.rt, opts); err != nil {
		w.log("onboard.llm.execute_error", map[string]any{"error": err.Error()})
		return err
	}
	rt, err := process.Resolve()
	if err != nil {
		w.log("onboard.llm.reload_error", map[string]any{"error": err.Error()})
		return err
	}
	w.rt = rt
	w.log("onboard.llm.configured", map[string]any{"summary": mainLLMSummary(&w.rt.Config)})
	return nil
}

// channelsFinish is the channels page's first row, the one that ends the
// setup.
const channelsFinish = "Finish setup"

// configureChannels is the onboarding's last page: the chat apps the agent can
// be reached from, each optional. It reports back=true when the user pressed
// Esc to return to the model setup, which only happens when canGoBack says
// there is one to return to; otherwise Esc finishes like channelsFinish.
func (w *Wizard) configureChannels(canGoBack bool) (back bool, err error) {
	agentID := w.channelAgentID()
	chosen := -1 // the channel handled last, so the list shows again on it
	for {
		// The page names the agent because a channel is configured for one
		// primary agent, not for the installation: the same setup run under a
		// different active agent configures a different set of bots.
		setSetupPage(w.selector, onboardPages, onboardPages)
		channels := w.agentChannels(agentID)
		items := []SelectItem{{Label: channelsFinish, Description: "Start using Forebrain Harness"}}
		for _, id := range channelOrder {
			meta := channelMetaByID[id]
			blurb := strings.TrimSpace(meta.Blurb)
			if meta.IsConfigured(&channels) {
				blurb = "Connected · " + blurb
			}
			items = append(items, SelectItem{Label: meta.Label, Description: blurb, Category: "Chat apps"})
		}
		idx, ok, err := w.selector.SelectRich("Channels\nOptional: let people reach the "+agentID+" agent from their chat apps.", items, chosen)
		if err != nil {
			w.log("onboard.channels.cancelled", map[string]any{"error": wizardErr(err)})
			return false, err
		}
		if !ok {
			return canGoBack, nil
		}
		if idx <= 0 || idx >= len(items) {
			w.log("onboard.channels.finished", nil)
			return false, nil
		}
		chosen = idx
		id := channelOrder[idx-1]
		w.log("onboard.channels.choice", map[string]any{"agent": agentID, "channel": id})
		if err := w.handleChannelChoice(agentID, id); err != nil {
			return false, err
		}
	}
}

// channelKeep, channelUpdate and channelDisable are what can be done with a
// channel that is already connected.
const (
	channelKeep    = "Keep as is"
	channelUpdate  = "Update its settings"
	channelDisable = "Disable it"
)

func (w *Wizard) handleChannelChoice(agentID string, id string) error {
	meta, ok := channelMetaByID[id]
	if !ok {
		w.log("onboard.channel.unknown", map[string]any{"channel": id})
		return nil
	}
	channels := w.agentChannels(agentID)
	if meta.IsConfigured(&channels) {
		action, ok, err := w.selector.Select(
			meta.Label+"\nAlready connected. What would you like to do?",
			[]string{channelKeep, channelUpdate, channelDisable},
			channelKeep,
		)
		if err != nil {
			w.log("onboard.channel.cancelled", map[string]any{"channel": id, "error": wizardErr(err)})
			return err
		}
		w.log("onboard.channel.existing_action", map[string]any{"channel": id, "action": action})
		switch {
		case !ok || action == channelKeep:
			return nil
		case action == channelDisable:
			meta.Disable(&channels)
			appcfg.SetChannelsForAgent(&w.rt.Config, agentID, channels)
			w.log("onboard.channel.disable", map[string]any{"agent": agentID, "channel": id})
			return process.SaveConfig(w.rt)
		}
	}
	if err := meta.Configure(w.selector, &channels, w.rt.Home, agentID); err != nil {
		w.log("onboard.channel.configure_error", map[string]any{"agent": agentID, "channel": id, "error": err.Error()})
		return err
	}
	appcfg.SetChannelsForAgent(&w.rt.Config, agentID, channels)
	w.log("onboard.channel.save", map[string]any{"agent": agentID, "channel": id})
	return process.SaveConfig(w.rt)
}

// channelAgentID is the primary agent whose channels this wizard run
// configures — the active one, which on a first-time setup is main.
func (w *Wizard) channelAgentID() string {
	return appcfg.ActiveID(w.rt.Home, &w.rt.Config)
}

func (w *Wizard) agentChannels(agentID string) appcfg.ChannelsSection {
	return appcfg.ChannelsForAgent(&w.rt.Config, agentID)
}

// printSummary tells the user, once the pages are gone, what the setup left
// them with.
func (w *Wizard) printSummary(out io.Writer) {
	facts := []turn.StatusFact{
		{Label: "Model", Value: mainLLMSummary(&w.rt.Config)},
		{Label: "Channels", Value: w.channelSummary()},
	}
	b := newPanelBuilder()
	b.facts(facts, nil)
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, panelIndent+panelOKStyle.Render("✓")+" "+panelTitleStyle.Render("Forebrain Harness is ready"))
	for _, line := range b.panel().layout(maxInt(20, termWidthOrDefault()-viewportRightPadding), 0, 0, false).lines {
		if strings.TrimSpace(stripAnsi(line)) != "" {
			_, _ = fmt.Fprintln(out, panelIndent+line)
		}
	}
	_, _ = fmt.Fprintln(out)
}

func mainLLMConfigured(cfg *appcfg.Root) bool {
	if cfg == nil {
		return false
	}
	return appcfg.ValidateMainAgentLLMConfigured(cfg).Complete()
}

func mainLLMSummary(cfg *appcfg.Root) string {
	if cfg == nil {
		return "Not configured."
	}
	v := appcfg.ValidateMainAgentLLMConfigured(cfg)
	if !v.Complete() {
		return "Not configured."
	}
	switch {
	case v.Provider != "" && v.Model != "":
		return v.Provider + " · " + v.Model
	case v.Model != "":
		return v.Model
	default:
		return "Configured."
	}
}

func mainLLMBaseURL(cfg *appcfg.Root) string {
	if cfg == nil {
		return ""
	}
	def, ok := cfg.Agents.Definitions["main"]
	if !ok {
		return ""
	}
	p := appcfg.PrimaryLLM(def)
	if p == nil {
		return ""
	}
	return strings.TrimSpace(p.BaseURL)
}

func mainLLMAPIPath(cfg *appcfg.Root) string {
	if cfg == nil {
		return ""
	}
	def, ok := cfg.Agents.Definitions["main"]
	if !ok {
		return ""
	}
	p := appcfg.PrimaryLLM(def)
	if p == nil {
		return ""
	}
	return strings.TrimSpace(p.APIPath)
}

func selectedChannelSummary(ch *appcfg.ChannelsSection) string {
	selected := configuredChannelLabels(ch)
	if len(selected) == 0 {
		return "Skipped."
	}
	return strings.Join(selected, ", ")
}

func (w *Wizard) channelSummary() string {
	channels := w.agentChannels(w.channelAgentID())
	return selectedChannelSummary(&channels)
}

func (w *Wizard) log(event string, fields map[string]any) {
	if w == nil {
		return
	}
	telemetry.SetupEvent(w.rt.Home, event, fields)
}

func wizardErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func cancelledErr(err error) error {
	if err != nil {
		return err
	}
	return ErrCancelled
}

type SetupSelector interface {
	Select(label string, options []string, defaultOption string) (string, bool, error)
	SelectRich(label string, items []SelectItem, defaultIdx int) (int, bool, error)
	Input(label string, defaultValue string) (string, bool, error)
	Secret(label string, defaultValue string) (string, bool, error)
	Confirm(label string, defaultValue bool) (bool, bool, error)
	Review(label string, facts []turn.StatusFact, actions []string, defaultIdx int) (int, bool, error)
}

type PromptConfig struct {
	Context        context.Context
	Output         io.Writer
	Selector       SetupSelector
	Defaults       process.AgentLLMDefaults
	CurrentBaseURL string
	CurrentAPIPath string
	Home           string
	// ConfigPath is the file the setup saves to, named on the review page.
	ConfigPath string
	// Onboarding is the first-run setup: it welcomes the user and continues
	// past the review to the channels.
	Onboarding bool
	// Pages is how many pages the whole flow has, for numbering them; the
	// model setup's own when zero.
	Pages int
}

type providerChoice struct {
	Group           string
	Label           string
	Hint            string
	Provider        string
	DefaultBaseURL  string
	SupportsAPIPath bool
	APIKeyLabel     string
	ChatGPTOAuth    bool
}

type providerGroup struct {
	Label   string
	Hint    string
	Options []providerChoice
}

var chatGPTLogin = openai.Login

// discoverChatGPTModels is a package var so tests can stub account discovery
// without a network. Production resolves the account's models through the
// shared projection every surface uses.
var discoverChatGPTModels = process.DiscoverChatGPTModels

// chatGPTModelsTimeout bounds one account model fetch. The browser login has
// its own long budget because a human is typing in another window; the fetch
// is one HTTP call and must not hold the input loop for nearly as long.
const chatGPTModelsTimeout = 10 * time.Second

var providerGroups = []providerGroup{
	{
		Label: "ChatGPT",
		Hint:  "OAuth subscription login",
		Options: []providerChoice{{
			Group: "ChatGPT", Label: "Sign in with ChatGPT", Hint: "Use Codex included with your ChatGPT plan",
			Provider: "chatgpt", DefaultBaseURL: openai.CodexBaseURL, ChatGPTOAuth: true,
		}},
	},
	{
		Label: "OpenAI",
		Hint:  "API key",
		Options: []providerChoice{{
			Group:           "OpenAI",
			Label:           "OpenAI API key",
			Hint:            "GPT models via OpenAI API",
			Provider:        "openai",
			DefaultBaseURL:  "https://api.openai.com/v1",
			SupportsAPIPath: true,
			APIKeyLabel:     "OpenAI API key",
		}},
	},
	{
		Label: "Anthropic",
		Hint:  "API key",
		Options: []providerChoice{{
			Group:           "Anthropic",
			Label:           "Anthropic API key",
			Hint:            "Claude models via Anthropic API",
			Provider:        "anthropic",
			DefaultBaseURL:  "https://api.anthropic.com",
			SupportsAPIPath: false,
			APIKeyLabel:     "Anthropic API key",
		}},
	},
	{
		Label: "Qwen Cloud",
		Hint:  "API key",
		Options: []providerChoice{
			{
				Group:           "Qwen Cloud",
				Label:           "Standard API Key for China (pay-as-you-go)",
				Hint:            "Endpoint: dashscope.aliyuncs.com",
				Provider:        "alibaba",
				DefaultBaseURL:  "https://dashscope.aliyuncs.com/compatible-mode/v1",
				SupportsAPIPath: true,
				APIKeyLabel:     "Qwen API key",
			},
			{
				Group:           "Qwen Cloud",
				Label:           "Standard API Key for Global/Intl (pay-as-you-go)",
				Hint:            "Endpoint: dashscope-intl.aliyuncs.com",
				Provider:        "alibaba",
				DefaultBaseURL:  "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
				SupportsAPIPath: true,
				APIKeyLabel:     "Qwen API key",
			},
			{
				Group:           "Qwen Cloud",
				Label:           "Coding Plan API Key for China (subscription)",
				Hint:            "Endpoint: coding.dashscope.aliyuncs.com",
				Provider:        "alibaba",
				DefaultBaseURL:  "https://coding.dashscope.aliyuncs.com/v1",
				SupportsAPIPath: true,
				APIKeyLabel:     "Qwen API key",
			},
			{
				Group:           "Qwen Cloud",
				Label:           "Coding Plan API Key for Global/Intl (subscription)",
				Hint:            "Endpoint: coding-intl.dashscope.aliyuncs.com",
				Provider:        "alibaba",
				DefaultBaseURL:  "https://coding-intl.dashscope.aliyuncs.com/v1",
				SupportsAPIPath: true,
				APIKeyLabel:     "Qwen API key",
			},
		},
	},
	{
		Label: "Google",
		Hint:  "Gemini API key",
		Options: []providerChoice{{
			Group:           "Google",
			Label:           "Google Gemini API key",
			Hint:            "Gemini via OpenAI-compatible endpoint",
			Provider:        "google",
			DefaultBaseURL:  "https://generativelanguage.googleapis.com/v1beta/openai",
			SupportsAPIPath: true,
			APIKeyLabel:     "Google Gemini API key",
		}},
	},
	{
		Label: "DeepSeek",
		Hint:  "API key",
		Options: []providerChoice{{
			Group:           "DeepSeek",
			Label:           "DeepSeek API key",
			Hint:            "DeepSeek chat/reasoner models",
			Provider:        "deepseek",
			DefaultBaseURL:  "https://api.deepseek.com",
			SupportsAPIPath: true,
			APIKeyLabel:     "DeepSeek API key",
		}},
	},
	{
		Label: "xAI",
		Hint:  "API key",
		Options: []providerChoice{{
			Group:           "xAI",
			Label:           "xAI API key",
			Hint:            "Grok via OpenAI-compatible endpoint",
			Provider:        "xai",
			DefaultBaseURL:  "https://api.x.ai/v1",
			SupportsAPIPath: true,
			APIKeyLabel:     "xAI API key",
		}},
	},
	{
		Label: "Mistral AI",
		Hint:  "API key",
		Options: []providerChoice{{
			Group:           "Mistral AI",
			Label:           "Mistral API key",
			Hint:            "Mistral via OpenAI-compatible endpoint",
			Provider:        "mistral",
			DefaultBaseURL:  "https://api.mistral.ai/v1",
			SupportsAPIPath: true,
			APIKeyLabel:     "Mistral API key",
		}},
	},
	{
		Label: "Moonshot AI",
		Hint:  "Kimi API key",
		Options: []providerChoice{{
			Group:           "Moonshot AI",
			Label:           "Kimi API key",
			Hint:            "Moonshot/Kimi OpenAI-compatible endpoint",
			Provider:        "moonshotai",
			DefaultBaseURL:  "https://api.moonshot.ai/v1",
			SupportsAPIPath: true,
			APIKeyLabel:     "Moonshot API key",
		}},
	},
	{
		Label: "Z.AI",
		Hint:  "API key",
		Options: []providerChoice{{
			Group:           "Z.AI",
			Label:           "Z.AI API key",
			Hint:            "GLM via OpenAI-compatible endpoint",
			Provider:        "zhipuai",
			DefaultBaseURL:  "https://api.z.ai/api/paas/v4",
			SupportsAPIPath: true,
			APIKeyLabel:     "Z.AI API key",
		}},
	},
	{
		Label: "MiniMax",
		Hint:  "API key",
		Options: []providerChoice{{
			Group:           "MiniMax",
			Label:           "MiniMax API key (Global)",
			Hint:            "Endpoint: api.minimax.io",
			Provider:        "minimax",
			DefaultBaseURL:  "https://api.minimax.io/anthropic/",
			SupportsAPIPath: false,
			APIKeyLabel:     "MiniMax API key",
		}, {
			Group:           "MiniMax",
			Label:           "MiniMax API key (CN)",
			Hint:            "Endpoint: api.minimaxi.com",
			Provider:        "minimax",
			DefaultBaseURL:  "https://api.minimaxi.com/anthropic/",
			SupportsAPIPath: false,
			APIKeyLabel:     "MiniMax API key",
		}},
	},
}

// The model setup's pages, in order. Esc on any of them goes back one page;
// the first one closes the setup.
const (
	setupPageProvider = iota + 1
	setupPageCredentials
	setupPageModel
	setupPageReview
	llmSetupPages = setupPageReview
)

// setupPager is a selector that shows where in a setup flow its prompts
// stand. The onboarding numbers its pages with it, and every selector that
// implements it says that Esc goes back once there is a page to go back to.
type setupPager interface {
	SetupPage(page, total int)
}

// setupStatusWriter is a selector that shows what a setup step does without
// the user as a page of its own; StatusWriter is nil when it does not.
type setupStatusWriter interface {
	StatusWriter(title string) io.Writer
}

func setSetupPage(selector SetupSelector, page, total int) {
	if pager, ok := selector.(setupPager); ok {
		pager.SetupPage(page, total)
	}
}

// PromptMainLLMSetup asks which provider and model the agent runs on and
// returns the options to save, or ok=false when the user closed the setup.
func PromptMainLLMSetup(cfg PromptConfig) (process.Options, bool, error) {
	if cfg.Selector == nil {
		return process.Options{}, false, fmt.Errorf("nil selector")
	}
	return newLLMSetup(cfg).run()
}

// llmSetup is the model setup as pages the user walks forward and back
// through — provider, credentials, model, review — keeping every answer, so
// stepping back to a page shows what was chosen there.
type llmSetup struct {
	cfg     PromptConfig
	page    int
	back    bool // the last move was back a page
	choice  providerChoice
	apiKey  string
	records []turn.ModelRecord // the ChatGPT account's models, once fetched
	model   string
	params  string
	baseURL string
	apiPath string
}

func newLLMSetup(cfg PromptConfig) *llmSetup {
	if cfg.Pages == 0 {
		cfg.Pages = llmSetupPages
	}
	return &llmSetup{cfg: cfg, page: setupPageProvider}
}

func (s *llmSetup) run() (process.Options, bool, error) {
	defer setSetupPage(s.cfg.Selector, 0, 0)
	logPromptEvent(s.cfg, "llmsetup.prompt.start", nil)
	for {
		// A ChatGPT account already signed in during this setup is not asked
		// to sign in again on the way back.
		if s.back && s.page == setupPageCredentials && s.choice.ChatGPTOAuth && s.records != nil {
			s.page--
		}
		setSetupPage(s.cfg.Selector, s.page, s.cfg.Pages)
		var forward bool
		var err error
		switch s.page {
		case setupPageProvider:
			forward, err = s.pickProvider()
		case setupPageCredentials:
			forward, err = s.credentials()
		case setupPageModel:
			forward, err = s.pickModel()
		case setupPageReview:
			var action setupReviewAction
			action, err = s.review()
			switch action {
			case setupReviewSave:
				logPromptChoice(s.cfg, "llmsetup.options_ready", s.choice, map[string]any{
					"model": s.model, "base_url": s.baseURL, "api_path": s.apiPath, "api_key_set": s.apiKey != "",
				})
				return s.options(), true, nil
			case setupReviewStartOver:
				s.page, s.back = setupPageProvider, false
				continue
			}
		}
		if err != nil {
			return process.Options{}, false, err
		}
		if forward {
			s.page, s.back = s.page+1, false
			continue
		}
		if s.page == setupPageProvider {
			logPromptEvent(s.cfg, "llmsetup.prompt.provider_cancelled", nil)
			return process.Options{}, false, nil
		}
		s.page, s.back = s.page-1, true
	}
}

// effortAnswer is the reasoning effort answered on the model page before, as
// the option it was picked from; empty before the page was answered.
func (s *llmSetup) effortAnswer() string {
	if s.model == "" {
		return ""
	}
	if effort := reasoningEffortFromParamsJSON(s.params); effort != "" {
		return effort
	}
	return reasoningEffortUnset
}

// resumeAt reopens the setup at page, keeping every answer given.
func (s *llmSetup) resumeAt(page int) {
	s.page, s.back = page, true
}

// options are the answers as what the setup saves.
func (s *llmSetup) options() process.Options {
	if s.choice.ChatGPTOAuth {
		return process.Options{
			Provider: s.choice.Provider, Model: s.model, BaseURL: s.choice.DefaultBaseURL,
			APIPath: "/responses", ParamsJSON: s.params, UpdateParams: true,
		}
	}
	return process.Options{
		Provider:     strings.TrimSpace(s.choice.Provider),
		APIKey:       s.apiKey,
		BaseURL:      s.baseURL,
		APIPath:      s.apiPath,
		Model:        s.model,
		ParamsJSON:   s.params,
		UpdateParams: true,
	}
}

// setChoice takes the provider the user picked. Picking another one drops
// every answer given for the last, and starts its endpoint where the saved
// configuration has it when the provider is the configured one.
func (s *llmSetup) setChoice(choice providerChoice) {
	if choice.Group == s.choice.Group && choice.Label == s.choice.Label {
		return
	}
	*s = llmSetup{cfg: s.cfg, page: s.page, choice: choice}
	s.baseURL = firstNonEmpty(managedProviderCurrentBaseURL(s.cfg, choice), choice.DefaultBaseURL)
	if choice.SupportsAPIPath && strings.TrimSpace(choice.Provider) == strings.TrimSpace(s.cfg.Defaults.Provider) {
		s.apiPath = strings.TrimSpace(s.cfg.CurrentAPIPath)
	}
	logPromptChoice(s.cfg, "llmsetup.prompt.provider_selected", choice, nil)
}

// providerSignInCategory and providerAPIKeyCategory group the providers by
// how Forebrain Harness reaches them.
const (
	providerSignInCategory = "Sign in with a subscription"
	providerAPIKeyCategory = "Use an API key"
)

// pickProvider asks which provider to connect, then — for a provider offered
// several ways, such as by plan and region — which way.
func (s *llmSetup) pickProvider() (bool, error) {
	title := "Connect a model\nChoose how Forebrain Harness reaches a model; /connect changes it any time."
	if s.cfg.Onboarding {
		title = "Welcome to Forebrain Harness\nFirst, connect the model it runs on. /connect changes it any time."
	}
	items := make([]SelectItem, 0, len(providerGroups))
	chosen := -1 // the provider picked before, when the user came back here
	for i, group := range providerGroups {
		category := providerAPIKeyCategory
		if hasChatGPTOAuth(group) {
			category = providerSignInCategory
		}
		items = append(items, SelectItem{Label: group.Label, Description: providerGroupBlurb(group), Category: category})
		if group.Label == s.choice.Group {
			chosen = i
		}
	}
	for {
		idx, ok, err := s.cfg.Selector.SelectRich(title, items, chosen)
		if err != nil || !ok || idx < 0 || idx >= len(providerGroups) {
			return false, err
		}
		group := providerGroups[idx]
		chosen = idx
		if len(group.Options) == 1 {
			s.setChoice(group.Options[0])
			return true, nil
		}
		methods := make([]SelectItem, 0, len(group.Options))
		method := -1
		for i, option := range group.Options {
			methods = append(methods, SelectItem{Label: option.Label, Description: option.Hint})
			if option.Group == s.choice.Group && option.Label == s.choice.Label {
				method = i
			}
		}
		idx, ok, err = s.cfg.Selector.SelectRich(group.Label+"\nWhich plan and region?", methods, method)
		if err != nil {
			return false, err
		}
		if ok && idx >= 0 && idx < len(group.Options) {
			s.setChoice(group.Options[idx])
			return true, nil
		}
		// Esc on the plans goes back to the providers.
	}
}

func hasChatGPTOAuth(group providerGroup) bool {
	for _, option := range group.Options {
		if option.ChatGPTOAuth {
			return true
		}
	}
	return false
}

// providerGroupBlurb is what a provider's row says about it: its one way in,
// or how many it offers.
func providerGroupBlurb(group providerGroup) string {
	if len(group.Options) == 1 {
		return group.Options[0].Hint
	}
	return fmt.Sprintf("%d plans by region and billing", len(group.Options))
}

// credentials asks for the provider's API key, or signs in to ChatGPT.
func (s *llmSetup) credentials() (bool, error) {
	if s.choice.ChatGPTOAuth {
		return s.signInChatGPT()
	}
	label := s.choice.APIKeyLabel + "\n" + s.apiKeyNote()
	for {
		key, ok, err := s.cfg.Selector.Secret(label, s.apiKey)
		if err != nil || !ok {
			if err == nil {
				logPromptChoice(s.cfg, "llmsetup.managed.api_key_cancelled", s.choice, nil)
			}
			return false, err
		}
		// An empty key cannot connect anything, so the page stays.
		if key = strings.TrimSpace(key); key != "" {
			s.apiKey = key
			return true, nil
		}
	}
}

// apiKeyNote says where the key is kept.
func (s *llmSetup) apiKeyNote() string {
	if home := strings.TrimSpace(s.cfg.Home); home != "" {
		return "Saved to " + displayHomePath(homepkg.DefaultEnvPath(home)) + " on this machine, never in forebrain.yaml."
	}
	return "Saved in Forebrain Harness's .env file on this machine, never in forebrain.yaml."
}

// displayHomePath shortens a path under the user's home directory to ~.
func displayHomePath(path string) string {
	if dir, err := os.UserHomeDir(); err == nil && dir != "" && strings.HasPrefix(path, dir+string(os.PathSeparator)) {
		return "~" + strings.TrimPrefix(path, dir)
	}
	return path
}

// signInChatGPT signs in through the browser and fetches the models the
// account may run.
func (s *llmSetup) signInChatGPT() (bool, error) {
	ctx := s.cfg.Context
	if ctx == nil {
		ctx = context.Background()
	}
	// Browser OAuth is the only provider option with an irreversible,
	// out-of-band side effect (it opens a browser and then blocks waiting for
	// the callback), so require an explicit opt-in first. Every other provider
	// asks for an API key before doing anything, which is itself a
	// confirmation step.
	start, ok, err := s.cfg.Selector.Confirm("Sign in with ChatGPT\nForebrain Harness opens your browser to sign in, then offers the models your ChatGPT plan includes.", false)
	if err != nil || !ok || !start {
		logPromptChoice(s.cfg, "llmsetup.chatgpt.login_declined", s.choice, map[string]any{"error": errString(err)})
		return false, err
	}
	status := s.statusOutput("Sign in with ChatGPT")
	if _, err := chatGPTLogin(ctx, openai.CredentialsPath(s.cfg.Home), status); err != nil {
		return false, err
	}
	// The login is valid but the account's models are not: fetch them now, on
	// the same credentials, so the picker offers what this account may
	// actually run — never a baked-in list. A failed fetch can be retried
	// here without going through the browser login again.
	for {
		fetchCtx, cancel := context.WithTimeout(ctx, chatGPTModelsTimeout)
		if status != nil {
			fmt.Fprintln(status, "Fetching this account's available models…")
		}
		records, err := discoverChatGPTModels(fetchCtx, s.cfg.Home)
		cancel()
		if err == nil {
			s.records = records
			if s.records == nil {
				s.records = []turn.ModelRecord{}
			}
			logPromptChoice(s.cfg, "llmsetup.chatgpt.models_fetched", s.choice, map[string]any{"count": len(records)})
			return true, nil
		}
		logPromptChoice(s.cfg, "llmsetup.chatgpt.models_failed", s.choice, map[string]any{"error": errString(err)})
		retry, ok, confirmErr := s.cfg.Selector.Confirm("Fetching this account's models failed\nRetry without signing in again?", true)
		if confirmErr != nil || !ok || !retry {
			return false, fmt.Errorf(
				"login saved, but the account's models could not be fetched: %w\nNo provider configuration was written; run /connect again later, or choose another provider", err)
		}
	}
}

// statusOutput is where the setup reports what it does without the user: a
// page of its own when the selector shows pages, the configured output
// otherwise.
func (s *llmSetup) statusOutput(title string) io.Writer {
	if sw, ok := s.cfg.Selector.(setupStatusWriter); ok {
		if w := sw.StatusWriter(title); w != nil {
			return w
		}
	}
	return s.cfg.Output
}

// pickModel asks which model to run and, when it reasons, how hard.
func (s *llmSetup) pickModel() (bool, error) {
	options := catalogModelOptions(s.choice.Provider)
	note := ""
	if s.choice.ChatGPTOAuth {
		options = discoveredModelOptions(s.records)
		if len(options) == 0 {
			note = "This account returned no selectable models; enter a model ID."
		}
	}
	for {
		model, ok, err := promptModel(s.cfg.Selector, options, firstNonEmpty(s.model, strings.TrimSpace(s.cfg.Defaults.Model)), note)
		if err != nil || !ok {
			if err == nil {
				logPromptChoice(s.cfg, "llmsetup.model_cancelled", s.choice, nil)
			}
			return false, err
		}
		var record *turn.ModelRecord
		if s.choice.ChatGPTOAuth {
			record = discoveredRecord(s.records, model)
		}
		params, ok, err := promptReasoningEffort(s.cfg, s.choice, model, record, s.effortAnswer())
		if err != nil {
			return false, err
		}
		if ok {
			s.model, s.params = model, params
			return true, nil
		}
		// Esc on the effort goes back to the models.
	}
}

type setupReviewAction int

const (
	setupReviewBack setupReviewAction = iota
	setupReviewSave
	setupReviewStartOver
)

// review shows every answer before anything is saved, and is where the
// endpoint is changed: most setups never need to.
func (s *llmSetup) review() (setupReviewAction, error) {
	save := "Save"
	if s.cfg.Onboarding {
		save = "Save and continue"
	}
	const changeEndpoint, startOver = "Change endpoint…", "Start over"
	actions := []string{save}
	if !s.choice.ChatGPTOAuth {
		actions = append(actions, changeEndpoint)
	}
	actions = append(actions, startOver)
	title := "Review"
	if path := strings.TrimSpace(s.cfg.ConfigPath); path != "" {
		title += "\nForebrain Harness saves this to " + displayHomePath(path) + "."
	}
	chosen := -1 // the action taken last, when the page shows again after it
	for {
		idx, ok, err := s.cfg.Selector.Review(title, s.reviewFacts(), actions, chosen)
		if err != nil || !ok || idx < 0 || idx >= len(actions) {
			return setupReviewBack, err
		}
		chosen = idx
		switch actions[idx] {
		case save:
			return setupReviewSave, nil
		case startOver:
			return setupReviewStartOver, nil
		case changeEndpoint:
			if err := s.editEndpoint(); err != nil {
				return setupReviewBack, err
			}
		}
	}
}

// reviewFacts are the answers as the review lists them.
func (s *llmSetup) reviewFacts() []turn.StatusFact {
	provider := s.choice.Group
	if s.choice.Group != "" && !strings.HasPrefix(s.choice.Label, s.choice.Group) {
		provider += " · " + s.choice.Label
	}
	effort := reasoningEffortFromParamsJSON(s.params)
	if effort == "" {
		effort = "Provider default"
	}
	endpoint := s.baseURL
	if s.choice.ChatGPTOAuth {
		endpoint = "ChatGPT account"
	} else if s.apiPath != "" {
		endpoint += " · " + s.apiPath
	}
	return []turn.StatusFact{
		{Label: "Provider", Value: provider},
		{Label: "Model", Value: s.model},
		{Label: "Reasoning", Value: effort},
		{Label: "Endpoint", Value: endpoint},
	}
}

// editEndpoint changes the base URL and, where the provider takes one, the
// API path. Esc keeps what was there.
func (s *llmSetup) editEndpoint() error {
	baseURL, ok, err := s.cfg.Selector.Input("Base URL\nLeave empty for "+s.choice.DefaultBaseURL+".", s.baseURL)
	if err != nil || !ok {
		return err
	}
	s.baseURL = firstNonEmpty(strings.TrimSpace(baseURL), s.choice.DefaultBaseURL)
	if !s.choice.SupportsAPIPath {
		return nil
	}
	apiPath, ok, err := s.cfg.Selector.Input("API path\nLeave empty for the provider's default.", s.apiPath)
	if err != nil || !ok {
		return err
	}
	s.apiPath = strings.TrimSpace(apiPath)
	return nil
}

// discoveredRecord returns the account record for a chosen model, or nil when
// the model was typed in manually and is therefore unverified.
func discoveredRecord(records []turn.ModelRecord, model string) *turn.ModelRecord {
	model = strings.TrimSpace(model)
	for i := range records {
		if strings.EqualFold(records[i].APIModel, model) {
			return &records[i]
		}
	}
	return nil
}

func managedProviderCurrentBaseURL(cfg PromptConfig, choice providerChoice) string {
	current := strings.TrimSpace(cfg.CurrentBaseURL)
	if current == "" {
		return ""
	}
	if strings.TrimSpace(choice.Provider) == strings.TrimSpace(cfg.Defaults.Provider) {
		return current
	}
	return ""
}

// modelOption pairs what the picker shows with what gets saved. The label can
// be a display name; the value is always the exact API model id, so nothing is
// ever parsed back out of a display string.
type modelOption struct {
	Label     string
	Value     string
	IsDefault bool
}

// manualModelInput is the selectable escape hatch appended to every option
// list: a model the sources did not offer can still be typed in verbatim.
const manualModelInput = "Enter a model ID…"

// catalogModelOptions lists every model the shared catalog — the same data
// /model and the web picker read — holds for a provider; the picker's filter
// finds one among them.
func catalogModelOptions(provider string) []modelOption {
	records := turn.ListModelCatalog(turn.ModelCatalogQuery{Provider: provider})
	options := make([]modelOption, 0, len(records)+1)
	for _, rec := range records {
		options = append(options, modelOption{Label: modelOptionLabel(rec), Value: rec.APIModel})
	}
	return options
}

// discoveredModelOptions renders the account's ChatGPT models in backend
// priority order. The list is never capped and never merged with static
// guesses: it is exactly what the account returned.
func discoveredModelOptions(records []turn.ModelRecord) []modelOption {
	options := make([]modelOption, 0, len(records)+1)
	for _, rec := range records {
		options = append(options, modelOption{Label: modelOptionLabel(rec), Value: rec.APIModel, IsDefault: rec.IsDefault})
	}
	return options
}

// modelOptionLabel shows the display name and the exact id together when they
// differ, so the value being saved is always visible in the prompt.
func modelOptionLabel(rec turn.ModelRecord) string {
	name := strings.TrimSpace(rec.ModelName)
	apiModel := strings.TrimSpace(rec.APIModel)
	if name == "" || name == apiModel {
		return apiModel
	}
	return name + " · " + apiModel
}

// promptModel asks for the model: from the options, or typed as an ID when
// none fits — or when there are none, in which case note says why. Esc on the
// typed ID goes back to the options.
func promptModel(selector SetupSelector, options []modelOption, defaultValue string, note string) (string, bool, error) {
	defaultValue = strings.TrimSpace(defaultValue)
	typed := func() (string, bool, error) {
		label := "Model ID"
		if note != "" {
			label += "\n" + note
		}
		for {
			value, ok, err := selector.Input(label, defaultValue)
			if err != nil || !ok {
				return "", ok, err
			}
			if value = strings.TrimSpace(value); value != "" {
				return value, true, nil
			}
		}
	}
	if len(options) == 0 {
		return typed()
	}
	labels := make([]string, 0, len(options)+1)
	values := make(map[string]string, len(options)+1)
	defaultOption := ""
	for _, option := range options {
		labels = append(labels, option.Label)
		values[option.Label] = option.Value
		if option.Value != "" && option.Value == defaultValue {
			defaultOption = option.Label
		}
	}
	labels = append(labels, manualModelInput)
	if defaultOption == "" {
		if defaultValue != "" {
			defaultOption = manualModelInput
		}
		for _, option := range options {
			if option.IsDefault {
				defaultOption = option.Label
				break
			}
		}
		if defaultOption == "" {
			defaultOption = labels[0]
		}
	}
	for {
		selected, ok, err := selector.Select("Model\nThe model Forebrain Harness runs on.", labels, defaultOption)
		if err != nil || !ok {
			return "", false, err
		}
		if selected = strings.TrimSpace(selected); selected != manualModelInput {
			if value, ok := values[selected]; ok && value != "" {
				return value, true, nil
			}
			return "", false, fmt.Errorf("unknown model selection %q", selected)
		}
		value, ok, err := typed()
		if err != nil || ok {
			return value, ok, err
		}
	}
}

// reasoningEffortOptions are the same levels /model offers, so a provider
// configured through onboarding or /connect starts on the identical scale the
// in-session switcher later shows.
var reasoningEffortOptions = []string{"low", "medium", "high", "xhigh"}

// reasoningEffortProviderDefault is used when neither the existing config nor
// the catalog says otherwise.
const reasoningEffortProviderDefault = "medium"

// reasoningEffortUnset lets the operator keep the provider's own default
// instead of pinning a level into the config.
const reasoningEffortUnset = "Provider default"

// promptReasoningEffort asks for the reasoning effort of the model just chosen
// and returns the params JSON to persist with the provider entry. The levels
// come from the record when the model was discovered on this account — the
// backend decides which a model supports, including levels a static list has
// never heard of — and fall back to catalog knowledge, or to no question at
// all for models known not to reason, so the request never carries a parameter
// the provider would reject.
func promptReasoningEffort(cfg PromptConfig, choice providerChoice, model string, record *turn.ModelRecord, previous string) (string, bool, error) {
	existing := currentProviderParamsJSON(cfg, choice)
	if record != nil {
		if len(record.ReasoningEfforts) == 0 {
			logPromptChoice(cfg, "llmsetup.reasoning_effort.not_supported", choice, map[string]any{"model": model})
			return applyReasoningEffort(existing, ""), true, nil
		}
		return promptEffortChoice(cfg, choice, model, record.ReasoningEfforts, record.DefaultReasoningEffort, previous)
	}
	hit, found := llm.Lookup(choice.Provider, model)
	if found && !hit.CanReason {
		logPromptChoice(cfg, "llmsetup.reasoning_effort.not_supported", choice, map[string]any{"model": model})
		return applyReasoningEffort(existing, ""), true, nil
	}
	// A model neither the account nor the catalog verified gets no guessed
	// default level — unset lets the provider apply its own.
	providerDefault := ""
	if found {
		providerDefault = reasoningEffortProviderDefault
	}
	return promptEffortChoice(cfg, choice, model, reasoningEffortOptions, providerDefault, previous)
}

// promptEffortChoice runs the shared effort prompt. defaultEffort is the
// fallback only when neither the already-configured effort nor the record's
// own default is usable for this model.
func promptEffortChoice(cfg PromptConfig, choice providerChoice, model string, efforts []string, defaultEffort string, previous string) (string, bool, error) {
	existing := currentProviderParamsJSON(cfg, choice)
	options := append(append([]string{}, efforts...), reasoningEffortUnset)
	current := reasoningEffortFromParamsJSON(existing)
	defaultOption := ""
	for _, effort := range efforts {
		if effort == current {
			defaultOption = effort
			break
		}
		if defaultOption == "" && effort == strings.TrimSpace(defaultEffort) {
			defaultOption = effort
		}
	}
	if defaultOption == "" {
		defaultOption = reasoningEffortUnset
	}
	// Back on this page, the answer given here before wins, when this model
	// offers it.
	for _, option := range options {
		if option == previous {
			defaultOption = previous
		}
	}
	selected, ok, err := cfg.Selector.Select("Reasoning effort\nHow hard "+model+" thinks before it answers.", options, defaultOption)
	if err != nil || !ok {
		return "", false, err
	}
	effort := strings.ToLower(strings.TrimSpace(selected))
	if effort == strings.ToLower(reasoningEffortUnset) {
		effort = ""
	}
	logPromptChoice(cfg, "llmsetup.reasoning_effort.selected", choice, map[string]any{"model": model, "effort": effort})
	return applyReasoningEffort(existing, effort), true, nil
}

// currentProviderParamsJSON returns the params already configured, but only
// when the provider being configured is the one they belong to: carrying
// another provider's params over would attach settings the new endpoint never
// agreed to.
func currentProviderParamsJSON(cfg PromptConfig, choice providerChoice) string {
	if strings.TrimSpace(choice.Provider) != strings.TrimSpace(cfg.Defaults.Provider) {
		return ""
	}
	return strings.TrimSpace(cfg.Defaults.ParamsJSON)
}

func reasoningEffortFromParamsJSON(paramsJSON string) string {
	payload := decodeParamsJSON(paramsJSON)
	reasoning, _ := payload["reasoning"].(map[string]any)
	if reasoning == nil {
		return ""
	}
	effort, _ := reasoning["effort"].(string)
	return strings.ToLower(strings.TrimSpace(effort))
}

// applyReasoningEffort writes the chosen effort into the existing params,
// preserving every other request parameter already configured. An empty effort
// removes the key so the provider default applies again.
func applyReasoningEffort(paramsJSON string, effort string) string {
	payload := decodeParamsJSON(paramsJSON)
	if effort == "" {
		if reasoning, ok := payload["reasoning"].(map[string]any); ok && reasoning != nil {
			delete(reasoning, "effort")
			if len(reasoning) == 0 {
				delete(payload, "reasoning")
			}
		}
	} else {
		reasoning, _ := payload["reasoning"].(map[string]any)
		if reasoning == nil {
			reasoning = map[string]any{}
			payload["reasoning"] = reasoning
		}
		reasoning["effort"] = effort
	}
	if len(payload) == 0 {
		return ""
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func decodeParamsJSON(paramsJSON string) map[string]any {
	payload := map[string]any{}
	if trimmed := strings.TrimSpace(paramsJSON); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
			return map[string]any{}
		}
	}
	return payload
}

func logPromptChoice(cfg PromptConfig, event string, choice providerChoice, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["provider"] = strings.TrimSpace(choice.Provider)
	fields["group"] = strings.TrimSpace(choice.Group)
	fields["label"] = strings.TrimSpace(choice.Label)
	telemetry.SetupEvent(cfg.Home, event, fields)
}

func logPromptEvent(cfg PromptConfig, event string, fields map[string]any) {
	telemetry.SetupEvent(cfg.Home, event, fields)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// connectSetupTimeout bounds the whole setup flow. It exists for the ChatGPT
// option, which blocks on a browser callback that may never arrive: /connect
// runs on the input loop, so an unbounded wait is a TUI that stops responding
// with no way back. It matches the window /mcp auth allows for the same flow.
const connectSetupTimeout = 10 * time.Minute

func (c *commandController) handleConnect(ctx context.Context, sessionID string) bool {
	if c == nil || c.selector == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, connectSetupTimeout)
	defer cancel()
	rt, err := connectRuntimeContext()
	if err != nil {
		c.renderer.PrintError(fmt.Errorf("connect: failed to load config: %w", err))
		return true
	}
	// The session runs one primary agent, and each carries its own provider.
	// Configuring main from a session running another agent would write a
	// definition nothing in this session reads, so the change would look like
	// it had no effect.
	agentName := appcfg.ActiveID(c.home, &rt.Config)
	opts, ok, err := PromptMainLLMSetup(PromptConfig{
		Context:        ctx,
		Output:         c.renderer.OutputWriter(),
		Selector:       c.selector,
		Defaults:       process.ReadAgentLLMDefaultsForAgent(c.home, agentName),
		CurrentBaseURL: connectCurrentBaseURL(&rt.Config, agentName),
		CurrentAPIPath: connectCurrentAPIPath(&rt.Config, agentName),
		Home:           c.home,
		ConfigPath:     rt.ConfigPath,
	})
	if err != nil {
		c.renderer.PrintError(fmt.Errorf("connect: %w", err))
		return true
	}
	if !ok {
		return true // cancelled
	}
	opts.AgentName = agentName
	rep, err := process.Execute(rt, opts)
	if err != nil {
		c.renderer.PrintError(fmt.Errorf("connect: failed to save config: %w", err))
		return true
	}
	process.ResetResolve()
	if c.session == nil {
		c.renderer.PrintError(fmt.Errorf("connect: active session unavailable; config was saved but is not active"))
		return true
	}
	if err := c.session.ReloadConfig(); err != nil {
		c.renderer.PrintError(fmt.Errorf("connect: config saved but active session reload failed: %w", err))
		return true
	}
	// The connected provider is now this session's own model, not just the
	// file default: apply and persist it for the conversation the user is in.
	if strings.TrimSpace(rep.Provider) != "" && strings.TrimSpace(rep.Model) != "" {
		selectErr := c.session.SelectModel(ctx, sessionID, turn.ModelChoice{
			Provider: rep.Provider,
			Model:    rep.Model,
			CanReason: func() bool {
				if hit, ok := llm.Lookup(rep.Provider, rep.Model); ok {
					return hit.CanReason
				}
				return false
			}(),
		}, nil)
		if selectErr != nil {
			switch turn.SelectionApplyOutcome(selectErr) {
			case turn.ModelRuntimeUncertain:
				c.renderer.RenderFrame(Frame{Kind: FrameError, Title: "connect", Content: "The configuration was saved, but adopting its model left the runtime in an uncertain state: " + selectErr.Error() + "\nReload the configuration or restart before sending another message.", Final: true})
			case turn.ModelAppliedNotDurable:
				c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "connect", Content: "The configuration was saved and the model adopted, but the choice could not be saved for this session: " + selectErr.Error(), Final: true})
			default:
				c.renderer.RenderFrame(Frame{Kind: FrameError, Title: "connect", Content: "The configuration was saved, but this session could not adopt its model: " + selectErr.Error(), Final: true})
			}
			c.refreshComposerModelFooter(sessionID)
			return true
		}
	}
	summary := strings.TrimSpace(rep.Provider)
	if m := strings.TrimSpace(rep.Model); m != "" {
		if summary != "" {
			summary += " · " + m
		} else {
			summary = m
		}
	}
	msg := "Provider configured"
	if summary != "" {
		msg = "Provider configured: " + summary
	}
	// Name the agent when it is not main: with several primary agents the user
	// needs to see which one this configured.
	if agent := strings.TrimSpace(rep.AgentName); agent != "" && agent != "main" {
		msg += "\nagent: " + agent
	}
	msg += "\n\nConfig saved. Changes take effect immediately for your next message."
	c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "connect", Content: msg, Final: true})
	// The footer names the model every turn runs on and the token stats are
	// sized by its context window, so both are stale the moment the provider
	// changes. /model refreshes them for the same reason; a connect that left
	// the old model on screen would read as "it did not take effect".
	c.refreshComposerModelFooter(sessionID)
	return true
}

// connectRuntimeContext builds the read-modify-write context /connect saves
// back.
//
// It deliberately does not reuse the config Resolve() cached. That copy is the
// file as it was when this process started, and setup writes the whole document
// back: everything changed since — /model, /sandbox, an edit made in another
// terminal — would be reverted by a command the user ran to change one
// provider. It reads the persisted form for the same reason /model does, since
// Load expands ${ENV} references and writing the expansion back would bake one
// machine's environment into the file.
func connectRuntimeContext() (process.Context, error) {
	rt, err := process.Resolve()
	if err != nil {
		return rt, err
	}
	persisted, err := appcfg.LoadPersisted(rt.ConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			// No file yet (first run): the resolved empty config is the base.
			return rt, nil
		}
		return rt, err
	}
	rt.Config = persisted
	return rt, nil
}

// refreshComposerModelFooter republishes the model half of the composer footer
// from the session's current config, keeping the directory the footer already
// shows.
func (c *commandController) refreshComposerModelFooter(sessionID string) {
	if c == nil || c.renderer == nil || c.session == nil {
		return
	}
	c.renderer.SetComposerFooter(ComposerFooter{
		Model:           summarizeComposerModel(c.session),
		ReasoningEffort: summarizeComposerReasoningEffort(c.session),
		Directory:       c.renderer.footer.Directory,
	})
	c.renderer.SetComposerTokenStats("", initialComposerTokenStats(c.session, sessionID))
}

func connectCurrentBaseURL(cfg *appcfg.Root, agentName string) string {
	p := connectCurrentLLM(cfg, agentName)
	if p == nil {
		return ""
	}
	return strings.TrimSpace(p.BaseURL)
}

func connectCurrentAPIPath(cfg *appcfg.Root, agentName string) string {
	p := connectCurrentLLM(cfg, agentName)
	if p == nil {
		return ""
	}
	return strings.TrimSpace(p.APIPath)
}

func connectCurrentLLM(cfg *appcfg.Root, agentName string) *appcfg.AgentLLMProviderConfig {
	if cfg == nil {
		return nil
	}
	name := strings.TrimSpace(agentName)
	if name == "" {
		name = "main"
	}
	def, ok := cfg.Agents.Definitions[name]
	if !ok {
		return nil
	}
	return appcfg.PrimaryLLM(def)
}

// NeedsFirstSetup reports whether this home still has to be set up.
//
// The criterion is the main agent's LLM configuration, not the config file's
// existence: home.Ensure seeds an incomplete forebrain.yaml (empty provider
// and model) the first time anything resolves the runtime — inside this very
// launch, before the first-setup check, the project-MCP consent already does.
// File existence therefore cannot tell a fresh home from a configured one;
// the LLM fields can, and they are the same fields onboarding itself and
// StartupConfigError judge by.
func NeedsFirstSetup() (bool, error) {
	rt, err := process.Resolve()
	if err != nil {
		return false, err
	}
	return !mainLLMConfigured(&rt.Config), nil
}

var ErrStartupConfigIncomplete = errors.New("startup config incomplete")

func StartupConfigError() error {
	home, err := homepkg.Root()
	if err != nil {
		return err
	}
	cfgPath, err := homepkg.ResolveConfigPath(home)
	if err != nil {
		return fmt.Errorf("no forebrain config found in %s; run `forebrain` or `forebrain gateway start` in a TTY to create one", home)
	}
	rt, err := process.Resolve()
	if err != nil {
		return err
	}
	missing := MainAgentLLMMissingFieldsWithConfig(rt.Config)
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrStartupConfigIncomplete, formatStartupConfigError(home, cfgPath, missing))
}

func formatStartupConfigError(home, cfgPath string, missing []string) string {
	envPath := homepkg.DefaultEnvPath(home)
	var b strings.Builder
	b.WriteString("main agent LLM is not fully configured; set agents.definitions.main.llm_providers[0].")
	b.WriteString(strings.Join(missing, ", "))
	b.WriteString(" in ")
	b.WriteString(cfgPath)
	if st, err := os.Stat(envPath); err != nil || st.IsDir() {
		b.WriteString("; missing or unreadable env file: ")
		b.WriteString(envPath)
	} else {
		b.WriteString("; check ")
		b.WriteString(envPath)
		b.WriteString(" for required secret/env values")
	}
	b.WriteString(". If this is the first setup, run `forebrain` or `forebrain gateway start` in a TTY.")
	return b.String()
}

// MainAgentLLMMissingFieldsWithConfig is the config-parameterised variant
// of MainAgentLLMMissingFields that avoids an extra process.Resolve()
// call when the caller already has a loaded config.
func MainAgentLLMMissingFieldsWithConfig(cfg appcfg.Root) []string {
	return appcfg.ValidateMainAgentLLMConfigured(&cfg).MissingFields()
}

// The startup banner is one card: the mascot on the left and, beside it, the
// product name with its version, the workspace, and the few keys a first
// prompt needs. The card is never narrower than bannerMinWidth and grows with
// its content, so only a path wider than the terminal wraps, at its
// separators. It is laid out afresh for the width it is painted at, so a
// terminal resize reflows it rather than leaving it clipped or ragged.

// forebrainMascot is the brand mascot — a small brain wearing its harness
// node — as a pixel grid: a rounded crown, the node, eyes with their shine,
// a smile and two feet. Each letter names a forebrainMascotPalette entry;
// '.' is clear. Each pixel is drawn as forebrainMascotPixelCols blank cells
// on one terminal row (see forebrainMascotRows). The first and last rows both
// carry ink, so centring the mascot by row count centres its ink. The grid is
// the owner-approved design, 10×6 pixels, 20 columns by 6 rows, taken from
// the startup-card design sheet (docs/design/STARTUP_CARD.html); it replaces
// the 14×8 grid of docs/plan/MASCOT_COMPACT_GRID_PLAN.md. Changing the grid
// needs the owner's sign-off, and TestForebrainMascotIsTheApprovedDesign
// pins it.
var forebrainMascot = [...]string{
	".FFFFFFFF.",
	"FFFFAAFFFF",
	"FFWPFFWPFF",
	"FFPPFFPPFF",
	".FFFPPFFF.",
	"..FF..FF..",
}

// forebrainMascotPalette colours the mascot. Every colour is an exact
// xterm-256 entry, so a 256-colour terminal shows the same colour a
// true-colour one does instead of a nearest-match quantisation.
var forebrainMascotPalette = map[byte]lipgloss.Color{
	'F': "#5fafd7", // body (xterm 74)
	'P': "#080808", // pupils, mouth (xterm 232)
	'W': "#ffffff", // eye shine (xterm 231)
	'A': "#ffaf5f", // harness node (xterm 215)
}

// forebrainMascotPixelCols is how many terminal columns one pixel spans. A
// terminal cell is about twice as tall as it is wide, so two cells side by
// side on one row make a square pixel.
const forebrainMascotPixelCols = 2

// forebrainMascotWidth is the mascot's width in terminal columns.
var forebrainMascotWidth = forebrainMascotPixelCols * len(forebrainMascot[0])

// The startup card's colours, one per role. Each is a
// lipgloss.CompleteAdaptiveColor — an exact value for every colour profile —
// because the design sheet's colours (docs/design/STARTUP_CARD.html) are
// true-colour hexes that are not xterm-256 entries: a plain AdaptiveColor in a
// 256-colour terminal quantises them to the nearest entry, which is how the
// frame turned grey and the keys stopped matching the title. The ANSI256
// values are xterm-256 indices — the CIEDE2000 nearest entry of the design hex,
// except the frame, which the sheet names — so a 256-colour terminal shows the
// design colour instead of a quantised cousin. The ANSI (16-colour) values keep
// the design hex, so a 16-colour terminal quantises the sheet's own colour
// rather than carrying a palette this file invents.
// TestForebrainBannerPaletteIsTheDesignSheet pins every entry.
var (
	// bannerBorderColor draws the card's frame and the rule under the product
	// name. Its 256 entry is 60 rather than the nearest match (24): the design
	// sheet names 60 for this role, and its low-saturation navy keeps the
	// frame as quiet as the dashed hairline the sheet draws.
	bannerBorderColor = lipgloss.CompleteAdaptiveColor{
		Light: lipgloss.CompleteColor{TrueColor: "#b7c6d4", ANSI256: "251", ANSI: "#b7c6d4"},
		Dark:  lipgloss.CompleteColor{TrueColor: "#35506a", ANSI256: "60", ANSI: "#35506a"},
	}
	// bannerTitleColor is the product name and, deliberately, the shortcut
	// keys: the design sheet gives both the same blue, so the keys read as
	// part of the heading. One value serves both, so they cannot drift apart.
	bannerTitleColor = lipgloss.CompleteAdaptiveColor{
		Light: lipgloss.CompleteColor{TrueColor: "#0d6e9c", ANSI256: "24", ANSI: "#0d6e9c"},
		Dark:  lipgloss.CompleteColor{TrueColor: "#87c3ea", ANSI256: "117", ANSI: "#87c3ea"},
	}
	// bannerMutedColor is the version and the shortcut labels.
	bannerMutedColor = lipgloss.CompleteAdaptiveColor{
		Light: lipgloss.CompleteColor{TrueColor: "#6f8494", ANSI256: "67", ANSI: "#6f8494"},
		Dark:  lipgloss.CompleteColor{TrueColor: "#7d93a6", ANSI256: "67", ANSI: "#7d93a6"},
	}
	// bannerPathColor is the workspace path, the card's main body text. Its
	// light value follows the sheet's own --ink-2, the role the dark card's
	// path colour plays there.
	bannerPathColor = lipgloss.CompleteAdaptiveColor{
		Light: lipgloss.CompleteColor{TrueColor: "#3d4750", ANSI256: "238", ANSI: "#3d4750"},
		Dark:  lipgloss.CompleteColor{TrueColor: "#cfd9e2", ANSI256: "188", ANSI: "#cfd9e2"},
	}
)

var (
	forebrainLogoStyle = lipgloss.NewStyle().
				Foreground(bannerTitleColor).
				Bold(true)
	bannerBorderStyle = lipgloss.NewStyle().
				Foreground(bannerBorderColor)
	bannerMutedStyle = lipgloss.NewStyle().
				Foreground(bannerMutedColor)
	bannerKeyStyle = lipgloss.NewStyle().
			Foreground(bannerTitleColor).
			Bold(true)
	bannerPathStyle = lipgloss.NewStyle().
			Foreground(bannerPathColor)
)

const (
	bannerProductName = "Forebrain Harness"
	// bannerMinWidth keeps the card a card when the content is narrow; long
	// paths grow past it.
	bannerMinWidth = 78
	// bannerMascotMinWidth is the narrowest card width that still has room
	// for the mascot beside a readable text column. It is the frame (2×1
	// column), the padding (2×2), the 20-column mascot, the gutter (3) and
	// the 29 columns the text then gets:
	// 2 + 4 + 20 + 3 + 29 = 58. It must stay at or below 78, the width the
	// viewport paints an 80-column terminal at (80 less
	// viewportRightPadding), so the common default window keeps the mascot.
	// Recompute it as 6 + forebrainMascotWidth + bannerGutter + 29 whenever
	// the grid changes.
	bannerMascotMinWidth = 58
	// bannerBorderMinWidth is the narrowest terminal worth a frame; below it
	// the text is printed bare.
	bannerBorderMinWidth = 32
	bannerGutter         = 3
	bannerPadding        = 2
	// bannerFrameH and bannerFrameV draw the card's dashed frame and the
	// rule under the product name. A dashed glyph is a font shape like any
	// other, but these two have the same 1200-unit advance in Fira Code as
	// ─ (U+2500) and │ (U+2502), so the frame keeps its column maths, and
	// any font with U+2504/U+2506 renders the dashes (Fira Code does).
	bannerFrameH = "┄" // U+2504, horizontal dashed
	bannerFrameV = "┆" // U+2506, vertical dashed
)

// bannerShortcuts are the keys a newcomer needs before the first prompt; the
// rest are listed under /.
var bannerShortcuts = [...]struct{ key, label string }{
	{"/", "commands"},
	{"@", "mention"},
	{"esc", "interrupt"},
	{"ctrl+j", "newline"},
}

func RenderForebrainBanner(out io.Writer, info StartupInfo) {
	if out == nil {
		return
	}
	for _, line := range forebrainBannerLines(info.Version, info.Directory, termWidthOrDefault()) {
		_, _ = fmt.Fprintln(out, line)
	}
}

// forebrainBannerLines lays the startup card out for width columns: a leading
// blank line, then the card. The card is at least bannerMinWidth columns wide
// and grows with its content — the longer the path, the wider the card —
// capped only by the terminal. The directory is never truncated; only a path
// wider than the terminal wraps, at its separators.
func forebrainBannerLines(version, dir string, width int) []string {
	version = bannerVersion(version)
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = "~"
	}
	framed := width >= bannerBorderMinWidth
	mascot := width >= bannerMascotMinWidth

	chrome := 0
	if framed {
		chrome += 2 * (1 + bannerPadding)
	}
	if mascot {
		chrome += forebrainMascotWidth + bannerGutter
	}
	natural := bannerTextWidth(version, dir) + chrome
	cardW := min(max(natural, bannerMinWidth), width)
	textW := max(cardW-chrome, 1)

	text := bannerTextColumn(version, dir, textW)
	var art []string
	if mascot {
		art = forebrainMascotRows()
	}
	rows := max(len(text), len(art))
	// Centre each block on the card: the mascot's first and last grid rows
	// both carry ink, so centring by row count is centring by ink. When the
	// height difference is odd, the mascot's offset rounds up and the text's
	// rounds down, so the mascot sinks half a row and the text never sits low.
	artTop := (rows - len(art) + 1) / 2
	textTop := (rows - len(text)) / 2

	lines := []string{""}
	if framed {
		lines = append(lines, bannerBorderStyle.Render("╭"+strings.Repeat(bannerFrameH, cardW-2)+"╮"))
		lines = append(lines, bannerFramedRow("", cardW))
	}
	for i := 0; i < rows; i++ {
		row := ""
		if mascot {
			cell := strings.Repeat(" ", forebrainMascotWidth)
			if a := i - artTop; a >= 0 && a < len(art) {
				cell = art[a]
			}
			row = cell + strings.Repeat(" ", bannerGutter)
		}
		if t := i - textTop; t >= 0 && t < len(text) {
			row += text[t]
		}
		if framed {
			lines = append(lines, bannerFramedRow(row, cardW))
		} else {
			lines = append(lines, row)
		}
	}
	if framed {
		lines = append(lines, bannerFramedRow("", cardW))
		lines = append(lines, bannerBorderStyle.Render("╰"+strings.Repeat(bannerFrameH, cardW-2)+"╯"))
	}
	return lines
}

// bannerFramedRow pads content to the card's inner width and closes it with
// the dashed frame on both sides.
func bannerFramedRow(content string, cardW int) string {
	inner := cardW - 2 - 2*bannerPadding
	pad := max(inner-displayLineWidth(content), 0)
	side := strings.Repeat(" ", bannerPadding)
	return bannerBorderStyle.Render(bannerFrameV) + side + content + strings.Repeat(" ", pad) + side + bannerBorderStyle.Render(bannerFrameV)
}

// bannerTextColumn is the card's text: name and version, a rule, the
// workspace, and the shortcuts, each line at most width columns wide.
func bannerTextColumn(version, dir string, width int) []string {
	var lines []string
	name := forebrainLogoStyle.Render(bannerProductName)
	ver := bannerMutedStyle.Render(version)
	nameW, verW := displayLineWidth(bannerProductName), displayLineWidth(version)
	if nameW+2+verW <= width {
		lines = append(lines, name+strings.Repeat(" ", width-nameW-verW)+ver)
	} else {
		// A development build's pseudo-version can be wider than the
		// column on its own; it wraps at its separators like the path.
		lines = append(lines, name)
		for _, piece := range wrapBannerText(version, width, "-+") {
			lines = append(lines, bannerMutedStyle.Render(piece))
		}
	}
	// The rule under the name is dashed, the same shape as the card frame.
	lines = append(lines, bannerBorderStyle.Render(strings.Repeat(bannerFrameH, width)))
	for _, piece := range wrapBannerText(dir, width, `/\`) {
		lines = append(lines, bannerPathStyle.Render(piece))
	}
	lines = append(lines, "")
	return append(lines, bannerShortcutRows(width)...)
}

// bannerShortcutItemWidth is the widest "key label" pair among
// bannerShortcuts.
func bannerShortcutItemWidth() int {
	itemW := 0
	for _, s := range bannerShortcuts {
		itemW = max(itemW, len(s.key)+1+len(s.label))
	}
	return itemW
}

// bannerShortcutRows lays the shortcuts out in two aligned columns when the
// text column has room for them, otherwise one per row.
func bannerShortcutRows(width int) []string {
	itemW := bannerShortcutItemWidth()
	perRow := 1
	if 2*itemW+bannerGutter+1 <= width {
		perRow = 2
	}
	var rows []string
	for i := 0; i < len(bannerShortcuts); i += perRow {
		var b strings.Builder
		for j := i; j < min(i+perRow, len(bannerShortcuts)); j++ {
			s := bannerShortcuts[j]
			if j > i {
				b.WriteString(strings.Repeat(" ", bannerGutter+1))
			}
			b.WriteString(bannerKeyStyle.Render(s.key) + " " + bannerMutedStyle.Render(s.label))
			if j < min(i+perRow, len(bannerShortcuts))-1 {
				b.WriteString(strings.Repeat(" ", itemW-len(s.key)-1-len(s.label)))
			}
		}
		rows = append(rows, b.String())
	}
	return rows
}

// bannerTextWidth is the natural width of the card's text column: the widest
// of the name-and-version line, the workspace path, and the two shortcut
// columns. It is where the card's natural width comes from — the longer the
// path, the wider the card. The card itself is floored at bannerMinWidth and
// capped only by the terminal width.
func bannerTextWidth(version, dir string) int {
	return max(max(
		displayLineWidth(bannerProductName)+2+displayLineWidth(version),
		displayLineWidth(dir)),
		2*bannerShortcutItemWidth()+bannerGutter+1)
}

// wrapBannerText splits text into pieces no wider than width, breaking before
// one of the separators in seps where it can and inside a segment only when
// the segment alone is wider than the column.
func wrapBannerText(text string, width int, seps string) []string {
	if width < 1 || displayLineWidth(text) <= width {
		return []string{text}
	}
	var pieces []string
	cur := ""
	for _, seg := range splitBannerText(text, seps) {
		if cur != "" && displayLineWidth(cur+seg) > width {
			pieces = append(pieces, cur)
			cur = ""
		}
		for displayLineWidth(seg) > width {
			cut := 0
			w := 0
			for i, r := range seg {
				rw := runewidth.RuneWidth(r)
				if w+rw > width {
					break
				}
				w += rw
				cut = i + len(string(r))
			}
			pieces = append(pieces, seg[:cut])
			seg = seg[cut:]
		}
		cur += seg
	}
	if cur != "" {
		pieces = append(pieces, cur)
	}
	return pieces
}

// splitBannerText splits text before each byte in seps, keeping the
// separators.
func splitBannerText(text, seps string) []string {
	var segs []string
	start := 0
	for i := 1; i < len(text); i++ {
		if strings.IndexByte(seps, text[i]) >= 0 {
			segs = append(segs, text[start:i])
			start = i
		}
	}
	return append(segs, text[start:])
}

// bannerVersion normalises the build version for display: "dev" for an
// unversioned build, a leading "v" for a bare semantic version.
func bannerVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "dev"
	}
	if version[0] >= '0' && version[0] <= '9' {
		return "v" + version
	}
	return version
}

// forebrainMascotRows renders the mascot one pixel row per terminal row, each
// pixel as forebrainMascotPixelCols blank cells painted with its background
// colour. No glyph is used: a background colour fills its whole cell on every
// terminal, while a block glyph is a font shape, and a terminal that rounds
// its cell up past the glyph (macOS Terminal with Fira Code at 14pt, SF Mono
// at 11pt, ...) leaves the remainder showing through as a seam. A terminal
// without colour gets the silhouette instead, with the face cut out of it, so
// the shape still reads.
func forebrainMascotRows() []string {
	mono := lipgloss.ColorProfile() == termenv.Ascii
	rows := make([]string, 0, len(forebrainMascot))
	for _, line := range forebrainMascot {
		var b strings.Builder
		for x := 0; x < len(line); x++ {
			if mono {
				b.WriteString(monoPixel(line[x]))
			} else {
				b.WriteString(colorPixel(line[x]))
			}
		}
		rows = append(rows, b.String())
	}
	return rows
}

func colorPixel(p byte) string {
	cells := strings.Repeat(" ", forebrainMascotPixelCols)
	if c, ok := forebrainMascotPalette[p]; ok {
		return lipgloss.NewStyle().Background(c).Render(cells)
	}
	return cells
}

// monoPixel draws the silhouette: every coloured pixel is ink except the
// eyes and mouth, which are left clear.
func monoPixel(p byte) string {
	if p == '.' || p == 'P' || p == 'W' {
		return strings.Repeat(" ", forebrainMascotPixelCols)
	}
	return strings.Repeat("█", forebrainMascotPixelCols)
}
