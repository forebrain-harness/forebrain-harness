// First-run setup: the wizard, prompts, connect flow, and resolution.
package tui

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	homepkg "github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/subscription"
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

// run walks the onboarding's pages: the model setup when no model is
// configured yet.
func (w *Wizard) run(ctx context.Context) error {
	w.out = ensureWriter(w.out)
	defer restoreInteractiveTerminalStyle(w.out)
	w.log("onboard.start", map[string]any{
		"config_path":     w.rt.ConfigPath,
		"main_configured": mainLLMConfigured(&w.rt.Config),
	})
	if mainLLMConfigured(&w.rt.Config) {
		w.log("onboard.llm.already_configured", map[string]any{"summary": mainLLMSummary(&w.rt.Config)})
	} else {
		w.log("onboard.llm.configure_start", nil)
		setup := newLLMSetup(PromptConfig{
			Context:        ctx,
			Output:         w.out,
			Selector:       w.selector,
			Defaults:       process.ReadAgentLLMDefaults(w.rt.Home),
			CurrentBaseURL: mainLLMBaseURL(&w.rt.Config),
			CurrentAPIPath: mainLLMAPIPath(&w.rt.Config),
			Home:           w.rt.Home,
			ConfigPath:     w.rt.ConfigPath,
			Onboarding:     true,
		})
		if err := w.configureMainLLM(setup); err != nil {
			w.log("onboard.error", map[string]any{"stage": "llm", "error": err.Error()})
			return err
		}
	}
	w.log("onboard.success", map[string]any{"main_llm": mainLLMSummary(&w.rt.Config)})
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

// printSummary tells the user, once the pages are gone, what the setup left
// them with.
func (w *Wizard) printSummary(out io.Writer) {
	facts := []turn.StatusFact{
		{Label: "Model", Value: mainLLMSummary(&w.rt.Config)},
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
	if label := llm.FormatProviderModel(v.Provider, v.Model); label != "" {
		return label
	}
	return "Configured."
}

func mainLLMBaseURL(cfg *appcfg.Root) string {
	if cfg == nil {
		return ""
	}
	if _, ok := cfg.Agents.Definitions["main"]; !ok {
		return ""
	}
	p := appcfg.EffectiveDefaultLLMFor(cfg, "main")
	if p == nil {
		return ""
	}
	return strings.TrimSpace(p.BaseURL)
}

func mainLLMAPIPath(cfg *appcfg.Root) string {
	if cfg == nil {
		return ""
	}
	if _, ok := cfg.Agents.Definitions["main"]; !ok {
		return ""
	}
	p := appcfg.EffectiveDefaultLLMFor(cfg, "main")
	if p == nil {
		return ""
	}
	return strings.TrimSpace(p.APIPath)
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
	// MultiSelect asks for several answers at once — the models a sign-in
	// unlocks — and is what multiSelectRichTabbed falls back to when the
	// selector has no check boxes.
	MultiSelect(label string, options []string, defaultOptions []string) ([]string, bool, error)
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
	// ConfigPath is the configuration the setup's writes go through: the state
	// database it names is where a model row lands.
	ConfigPath string
	// Onboarding is the first-run setup: it welcomes the user and continues
	// past the review to the channels.
	Onboarding bool
}

type providerChoice struct {
	Group           string
	Label           string
	Hint            string
	Provider        string
	DefaultBaseURL  string
	SupportsAPIPath bool
	APIKeyLabel     string
	// Subscription is the subscription registry id this choice signs in to. The
	// sign-in, its offers and the rows it writes all come from the registry, so
	// a new subscription needs no change here.
	Subscription string
	// Custom is the «Custom model» row: the provider is a fixed placeholder and
	// the protocol page decides the request shape.
	Custom bool
}

type providerGroup struct {
	Label   string
	Hint    string
	Options []providerChoice
}

// subscriptions is the sign-in registry the first page lists and the sign-in
// page drives. It is a variable so a test drives a subscription without a
// browser and without talking to its vendor.
var subscriptions = subscription.Registry

// probeLLMModel sends the smallest real request a configured model accepts. The
// custom form's «Test connection?» is this probe, and it is a variable so a test
// answers without a network.
var probeLLMModel = process.ProbeLLMModel

// modelsDiscoveryTimeout bounds one account model fetch. The sign-ins wait for a
// human in another window and have their own long budget; a listing is one HTTP
// call and must not hold the input loop for nearly as long.
const modelsDiscoveryTimeout = 10 * time.Second

// providerGroups is the API-key half of the provider page: every built-in
// provider reached with a key, generated from the built-in registry (pkg/config),
// which is the one list every surface offers. The two sign-in providers are not
// here — a subscription is what signs in, and pkg/subscription is its registry.
var providerGroups = providerGroupsFromRegistry()

// providerGroupsFromRegistry turns the registry's rows into the choices the API
// key group renders: one group per provider, one choice per endpoint.
func providerGroupsFromRegistry() []providerGroup {
	providers := appcfg.BuiltinProviders()
	groups := make([]providerGroup, 0, len(providers))
	for _, provider := range providers {
		if provider.LoginBacked {
			continue
		}
		keyLabel := provider.APIKeyLabel
		if strings.TrimSpace(keyLabel) == "" {
			keyLabel = provider.Label + " API key"
		}
		group := providerGroup{
			Label:   provider.Label,
			Hint:    providerGroupHint(provider),
			Options: make([]providerChoice, 0, len(provider.Endpoints)),
		}
		for _, endpoint := range provider.Endpoints {
			group.Options = append(group.Options, providerChoice{
				Group:           provider.Label,
				Label:           providerChoiceLabel(keyLabel, endpoint),
				Hint:            providerChoiceHint(endpoint),
				Provider:        provider.ID,
				DefaultBaseURL:  endpoint.BaseURL,
				SupportsAPIPath: endpoint.APIPath != "" && endpoint.APIPath != appcfg.APIPathAnthropicMessages,
				APIKeyLabel:     keyLabel,
			})
		}
		groups = append(groups, group)
	}
	return groups
}

// providerGroupHint is what the group row says about its provider: the
// registry's own wording, or the default "API key".
func providerGroupHint(provider appcfg.BuiltinProvider) string {
	if hint := strings.TrimSpace(provider.Hint); hint != "" {
		return hint
	}
	return "API key"
}

// providerChoiceLabel is one endpoint's row label: the wording the registry
// carries for it, the endpoint's own plan name, or — for a provider's single
// endpoint — the provider's key label.
func providerChoiceLabel(keyLabel string, endpoint appcfg.ProviderEndpoint) string {
	if label := strings.TrimSpace(endpoint.ChoiceLabel); label != "" {
		return label
	}
	if label := strings.TrimSpace(endpoint.Label); label != "" {
		return label
	}
	return keyLabel
}

// providerChoiceHint is the line under a choice: the registry's wording, or the
// endpoint's host, which is what tells two regional plans apart.
func providerChoiceHint(endpoint appcfg.ProviderEndpoint) string {
	if hint := strings.TrimSpace(endpoint.Hint); hint != "" {
		return hint
	}
	if u, err := url.Parse(strings.TrimSpace(endpoint.BaseURL)); err == nil && u.Host != "" {
		return u.Host
	}
	return strings.TrimSpace(endpoint.BaseURL)
}

// The model setup's pages. A flow walks the ones its choice needs, in the order
// flowPages lists them, so the custom form's extra steps never appear for a
// provider reached with a key, and a subscription ends on the models it offers.
// Esc on any page goes back one; the first one closes the setup.
const (
	setupPageProvider    = iota + 1
	setupPageBaseURL     // custom: the address requests go to
	setupPageProtocol    // custom: the request shape
	setupPageCredentials // the API key, or the subscription's sign-in
	setupPageModel       // a built-in provider's model and its effort
	setupPageCustomModel // custom: the model name, sent verbatim
	setupPageThinking    // custom: the levels a model no source knows supports
	setupPageConnection  // custom: send one request before saving
	setupPageOffers      // a subscription's models to add
	setupPageSubagents   // may the agent pick this row for subagents on its own?
	setupPageReview
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
// through — the provider, then whatever its way in needs — keeping every
// answer, so stepping back to a page shows what was chosen there.
type llmSetup struct {
	cfg     PromptConfig
	page    int
	back    bool // the last move was back a page
	choice  providerChoice
	apiKey  string
	model   string
	params  string
	baseURL string
	apiPath string
	// urlVerbatim is 「自定义协议」: the request goes to baseURL exactly as
	// stored, and apiPath only names the wire protocol.
	urlVerbatim bool
	// efforts are the thinking levels the user ticked for a model no source
	// documents.
	efforts []string
	// offers are the models the subscription's sign-in unlocked, and chosen are
	// the rows the user ticked among them.
	offers []subscription.Offer
	chosen []int
	// probes are the sign-in answers already given for this setup, one per
	// subscription: the first page's «· Signed in» and the sign-in page ask the
	// vendor once between them, never twice.
	probes map[string]turn.SignInCheck
	// subagentAllowed is the authorization page's answer: may the agent pick
	// this model for a subagent on its own? (owner 2026-11-10)
	subagentAllowed bool
	// subagentAnswered records that this flow already asked, so stepping back
	// to the page shows this flow's answer rather than the stored switch.
	subagentAnswered bool
	// offerSubagent is the subscription flow's answer per offer index: which
	// of the models the sign-in unlocked the agent may pick for subagents.
	offerSubagent map[int]bool
	// offerSubagentAnswered records that the subscription flow asked, so a
	// step back to the model list keeps what was ticked there.
	offerSubagentAnswered bool
	// db is the state database the rows are written to, opened on first use.
	db *sql.DB
}

func newLLMSetup(cfg PromptConfig) *llmSetup {
	return &llmSetup{cfg: cfg, page: setupPageProvider}
}

func (s *llmSetup) run() (process.Options, bool, error) {
	defer setSetupPage(s.cfg.Selector, 0, 0)
	defer s.closeState()
	logPromptEvent(s.cfg, "llmsetup.prompt.start", nil)
	for {
		pages := s.flowPages()
		setSetupPage(s.cfg.Selector, s.pageIndex(pages)+1, len(pages))
		var forward bool
		var err error
		switch s.page {
		case setupPageProvider:
			forward, err = s.pickProvider()
		case setupPageBaseURL:
			forward, err = s.customBaseURL()
		case setupPageProtocol:
			forward, err = s.customProtocol()
		case setupPageCredentials:
			forward, err = s.credentials()
		case setupPageModel:
			forward, err = s.pickModel()
		case setupPageCustomModel:
			forward, err = s.customModel()
		case setupPageThinking:
			forward, err = s.customThinkingLevels()
		case setupPageConnection:
			forward, err = s.customTestConnection()
		case setupPageOffers:
			var done bool
			done, err = s.pickOffers()
			if err != nil {
				return process.Options{}, false, err
			}
			if done {
				logPromptChoice(s.cfg, "llmsetup.options_ready", s.choice, map[string]any{
					"models": len(s.chosen), "provider": s.firstOffer().Provider,
				})
				return s.options(), true, nil
			}
		case setupPageSubagents:
			forward, err = s.pickSubagents()
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
		// The pages are read again after the step answered: a custom model's
		// name is what decides whether the levels page is part of the flow.
		pages = s.flowPages()
		if forward {
			s.page, s.back = s.step(pages, 1), false
			continue
		}
		if s.pageIndex(pages) == 0 {
			logPromptEvent(s.cfg, "llmsetup.prompt.provider_cancelled", nil)
			return process.Options{}, false, nil
		}
		s.page, s.back = s.step(pages, -1), true
	}
}

// flowPages is the pages this choice's flow walks, in order. The custom form is
// the long one — the address, the protocol, the key, the model, and only when
// nothing knows the model's levels, the levels — while a subscription ends on
// the models it offers and a provider reached with a key never sees those
// pages at all. The subagent authorization is one page of its own before the
// review (owner 2026-11-10): a subscription answers it inside the model list,
// where the rows are already being ticked.
func (s *llmSetup) flowPages() []int {
	switch {
	case s.choice.Custom:
		pages := []int{setupPageProvider, setupPageBaseURL, setupPageProtocol, setupPageCredentials, setupPageCustomModel}
		if s.asksThinkingLevels() {
			pages = append(pages, setupPageThinking)
		}
		return append(pages, setupPageConnection, setupPageSubagents, setupPageReview)
	case s.choice.Subscription != "":
		return []int{setupPageProvider, setupPageCredentials, setupPageOffers}
	default:
		return []int{setupPageProvider, setupPageCredentials, setupPageModel, setupPageSubagents, setupPageReview}
	}
}

// pageIndex is where the current page stands in the flow, for the step header.
func (s *llmSetup) pageIndex(pages []int) int {
	for i, page := range pages {
		if page == s.page {
			return i
		}
	}
	return 0
}

// step is the page one move forward or back of the current one, so the pages a
// flow does not have are skipped rather than visited.
func (s *llmSetup) step(pages []int, dir int) int {
	at := s.pageIndex(pages) + dir
	if at < 0 {
		return pages[0]
	}
	if at >= len(pages) {
		return pages[len(pages)-1]
	}
	return pages[at]
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

// options are the answers as what the setup saves. A subscription's first
// chosen model is the one the agent takes: the rows themselves are already
// written, and this is the entry the surface then switches the session to.
func (s *llmSetup) options() process.Options {
	if s.choice.Subscription != "" {
		offer := s.firstOffer()
		return process.Options{
			Provider: offer.Provider,
			Model:    offer.Model,
			BaseURL:  offer.BaseURL,
			APIPath:  offer.APIPath,
			// The save rewrites this row, so it has to carry the tick the
			// second check list took for it.
			SubagentAllowed: s.offerSubagentFor(s.chosen),
		}
	}
	if s.choice.Custom {
		opts := process.Options{
			Provider:    appcfg.CustomProvider,
			APIKey:      s.apiKey,
			BaseURL:     s.baseURL,
			APIPath:     s.apiPath,
			Model:       s.model,
			URLVerbatim: s.urlVerbatim,
			CapEfforts:  s.efforts,
			// The page asked, so its answer is this configuration's fact.
			SubagentAllowed: s.subagentAllowed,
		}
		return opts
	}
	return process.Options{
		Provider:     strings.TrimSpace(s.choice.Provider),
		APIKey:       s.apiKey,
		BaseURL:      s.baseURL,
		APIPath:      s.apiPath,
		Model:        s.model,
		ParamsJSON:   s.params,
		UpdateParams: true,
		// The page asked, so its answer is this configuration's fact.
		SubagentAllowed: s.subagentAllowed,
	}
}

// firstOffer is the model a finished subscription signs this agent in with: the
// first row the user ticked, in the registry's own order.
func (s *llmSetup) firstOffer() subscription.Offer {
	for _, idx := range s.chosen {
		if idx >= 0 && idx < len(s.offers) {
			return s.offers[idx]
		}
	}
	return subscription.Offer{}
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

// The first page's three groups, in the order they are listed.
const (
	providerSignInCategory = "Sign in with a subscription"
	providerAPIKeyCategory = "Use an API key"
	providerCustomCategory = "Custom"
)

// customChoice is the «Custom model» row: the provider is a fixed placeholder,
// the model name is free text, and the protocol page decides the request shape.
func customChoice() providerChoice {
	return providerChoice{
		Group:    providerCustomCategory,
		Label:    "Custom model",
		Hint:     "OpenAI- or Anthropic-compatible, or custom protocol",
		Provider: appcfg.CustomProvider,
		Custom:   true,
	}
}

// providerRow is one row of the first page: a subscription, a provider offered
// with a key, or the custom form. A row whose group has several endpoints opens
// the endpoint page instead of taking a choice at once.
type providerRow struct {
	item   SelectItem
	choice providerChoice
	group  *providerGroup
}

// providerRows lists the first page: the subscriptions, the providers reached
// with a key, then the custom form.
func (s *llmSetup) providerRows() []providerRow {
	rows := make([]providerRow, 0, len(subscriptions())+len(providerGroups)+1)
	for _, sub := range subscriptions() {
		choice := subscriptionChoice(sub)
		description := choice.Hint
		if s.probeSubscription(sub).State == turn.SignInUsable {
			description += "  · Signed in"
		}
		rows = append(rows, providerRow{
			item:   SelectItem{Label: choice.Label, Description: description, Category: providerSignInCategory},
			choice: choice,
		})
	}
	for _, group := range providerGroups {
		group := group
		row := providerRow{
			item:  SelectItem{Label: group.Label, Description: providerGroupBlurb(group), Category: providerAPIKeyCategory},
			group: &group,
		}
		if len(group.Options) == 1 {
			// One way in: the row is the choice, and there is no endpoint page
			// to open.
			row.choice = group.Options[0]
			row.group = nil
		}
		rows = append(rows, row)
	}
	custom := customChoice()
	rows = append(rows, providerRow{
		item:   SelectItem{Label: custom.Label, Description: custom.Hint, Category: providerCustomCategory},
		choice: custom,
	})
	return rows
}

// subscriptionChoice is one subscription's row: its name, and what signing in
// to it gets the user.
func subscriptionChoice(sub subscription.Subscription) providerChoice {
	label, _ := sub.Label()
	hint := ""
	if provider, ok := appcfg.BuiltinProviderByID(sub.ID()); ok && len(provider.Endpoints) > 0 {
		hint = providerChoiceHint(provider.Endpoints[0])
	}
	choice := providerChoice{
		Group:        label,
		Label:        label,
		Hint:         hint,
		Provider:     sub.ID(),
		Subscription: sub.ID(),
	}
	if provider, ok := appcfg.BuiltinProviderByID(sub.ID()); ok {
		choice.DefaultBaseURL = provider.DefaultBaseURL()
	}
	return choice
}

// subscription is the registry entry the current choice signs in to, nil for
// every other way in.
func (s *llmSetup) subscription() subscription.Subscription {
	id := strings.TrimSpace(s.choice.Subscription)
	if id == "" {
		return nil
	}
	for _, sub := range subscriptions() {
		if sub.ID() == id {
			return sub
		}
	}
	return nil
}

// probeSubscription is the sign-in answer this setup already has, or a fresh
// one: a login this machine holds that lists its models is the finished
// sign-in, and asking the vendor twice would pay for the listing twice.
func (s *llmSetup) probeSubscription(sub subscription.Subscription) turn.SignInCheck {
	if check, ok := s.probes[sub.ID()]; ok {
		return check
	}
	probeCtx, cancel := context.WithTimeout(s.ctx(), modelsDiscoveryTimeout)
	check := sub.Probe(probeCtx, subscription.Env{Home: s.cfg.Home})
	cancel()
	if s.probes == nil {
		s.probes = make(map[string]turn.SignInCheck, len(subscriptions()))
	}
	s.probes[sub.ID()] = check
	return check
}

// pickProvider asks how Forebrain Harness reaches a model, then — for a
// provider offered several ways, such as by plan and region — which way.
func (s *llmSetup) pickProvider() (bool, error) {
	title := "Connect a model\nChoose how Forebrain Harness reaches a model; /connect changes it any time."
	if s.cfg.Onboarding {
		title = "Welcome to Forebrain Harness\nFirst, connect the model it runs on. /connect changes it any time."
	}
	rows := s.providerRows()
	items := make([]SelectItem, len(rows))
	chosen := -1 // the provider picked before, when the user came back here
	for i, row := range rows {
		items[i] = row.item
		switch {
		case row.group != nil:
			// A provider with several endpoints: the user came from its
			// endpoint page, so its row is where they were.
			if row.item.Label == s.choice.Group {
				chosen = i
			}
		case row.choice.Group == s.choice.Group && row.choice.Label == s.choice.Label:
			chosen = i
		}
	}
	for {
		idx, ok, err := s.cfg.Selector.SelectRich(title, items, chosen)
		if err != nil || !ok || idx < 0 || idx >= len(rows) {
			return false, err
		}
		chosen = idx
		if rows[idx].group == nil {
			s.setChoice(rows[idx].choice)
			return true, nil
		}
		group := *rows[idx].group
		methods := make([]SelectItem, 0, len(group.Options))
		method := -1
		for i, option := range group.Options {
			methods = append(methods, SelectItem{Label: option.Label, Description: option.Hint})
			if option.Group == s.choice.Group && option.Label == s.choice.Label {
				method = i
			}
		}
		idx, ok, err = s.cfg.Selector.SelectRich(
			group.Label+"\nPlans are subscriptions; everything else is billed per use.\nThe endpoint is shown under each.",
			methods, method)
		if err != nil {
			return false, err
		}
		if ok && idx >= 0 && idx < len(group.Options) {
			s.setChoice(group.Options[idx])
			return true, nil
		}
		// Esc on the endpoints goes back to the providers.
	}
}

// categoryOf is the group a choice is listed under.
func categoryOf(choice providerChoice) string {
	switch {
	case choice.Custom:
		return providerCustomCategory
	case choice.Subscription != "":
		return providerSignInCategory
	default:
		return providerAPIKeyCategory
	}
}

// providerGroupBlurb is what a provider's row says about it: its one way in,
// or how many plans it offers.
func providerGroupBlurb(group providerGroup) string {
	if len(group.Options) == 1 {
		return group.Options[0].Hint
	}
	return fmt.Sprintf("%d plans by region and billing", len(group.Options))
}

// credentials asks for the provider's API key, or signs in to the subscription
// the choice runs on.
func (s *llmSetup) credentials() (bool, error) {
	if sub := s.subscription(); sub != nil {
		return s.signIn(sub)
	}
	label := s.apiKeyLabel()
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

// apiKeyLabel is the key page's head: what is being asked for, and — for a
// custom model — where the requests will go, so the address is checked before
// the key is typed.
func (s *llmSetup) apiKeyLabel() string {
	if s.choice.Custom {
		return "API key\nRequests go to " + s.endpoint() + "\nSaved on this machine only."
	}
	return s.choice.APIKeyLabel + "\nSaved on this machine only."
}

// endpoint is the URL this choice's requests would go to: the base URL the
// protocol appends its path to, or — for 「自定义协议」 — the base URL alone,
// because that shape sends the request as stored.
func (s *llmSetup) endpoint() string {
	return turn.ModelEndpointLabel(s.baseURL, s.apiPath, s.urlVerbatim)
}

// customBaseURL asks for the address requests go to, before anything else about
// the model is known: the protocol picked next decides whether a path is
// appended to it.
func (s *llmSetup) customBaseURL() (bool, error) {
	const title = "Custom model · Base URL\nThe address requests go to; the protocol you pick next decides whether a path is appended."
	for {
		value, ok, err := s.cfg.Selector.Input(title, s.baseURL)
		if err != nil || !ok {
			return false, err
		}
		if value = strings.TrimSpace(value); strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
			s.baseURL = value
			return true, nil
		}
		// An address no client can send to, so the page stays: the four
		// protocols are HTTP ones and nothing else can be validated here.
	}
}

// customProtocols are the four request shapes the custom form offers. The last
// one sends to the address exactly as stored, which is what a proxy or a
// non-standard gateway needs; the path it carries only names the wire protocol.
var customProtocols = []struct {
	Label    string
	Hint     string
	APIPath  string
	Verbatim bool
}{
	{"Anthropic Messages", appcfg.APIPathAnthropicMessages, appcfg.APIPathAnthropicMessages, false},
	{"OpenAI Chat Completions", appcfg.APIPathChatCompletions, appcfg.APIPathChatCompletions, false},
	{"OpenAI Responses", appcfg.APIPathResponses, appcfg.APIPathResponses, false},
	{"Custom protocol", "Send to the URL as is", appcfg.APIPathChatCompletions, true},
}

// customProtocol asks which request shape the endpoint speaks. It is required:
// the base URL alone says nothing about the body the model expects.
func (s *llmSetup) customProtocol() (bool, error) {
	const title = "Custom model · Protocol\nRequired. It decides the request format and whether a path is appended to the base URL."
	items := make([]SelectItem, len(customProtocols))
	chosen := -1
	for i, protocol := range customProtocols {
		items[i] = SelectItem{Label: protocol.Label, Description: protocol.Hint}
		if protocol.APIPath == s.apiPath && protocol.Verbatim == s.urlVerbatim {
			chosen = i
		}
	}
	idx, ok, err := s.cfg.Selector.SelectRich(title, items, chosen)
	if err != nil || !ok || idx < 0 || idx >= len(customProtocols) {
		return false, err
	}
	s.apiPath, s.urlVerbatim = customProtocols[idx].APIPath, customProtocols[idx].Verbatim
	return true, nil
}

// customModel asks for the model name. It is sent verbatim as the request's
// model parameter, so a name the sources do not know is fine — a vendor/ prefix
// too.
func (s *llmSetup) customModel() (bool, error) {
	const title = "Model name\nSent verbatim as the request’s model parameter; a vendor/ prefix is fine."
	for {
		value, ok, err := s.cfg.Selector.Input(title, s.model)
		if err != nil || !ok {
			return false, err
		}
		if value = strings.TrimSpace(value); value != "" {
			s.model = value
			return true, nil
		}
	}
}

// customThinkingLevels are the levels a person may tick for a model no source
// documents, in the order the wire protocol takes them. The web form offers the
// same six, so a level picked here is one that form shows too.
var customThinkingLevels = []struct {
	Level, English, Chinese string
}{
	{"minimal", "Minimal", "极低"},
	{"low", "Low", "低"},
	{"medium", "Medium", "中"},
	{"high", "High", "高"},
	{"xhigh", "Highest", "最高"},
	{"max", "Max", "极致"},
}

// asksThinkingLevels reports whether nothing documents this custom model's
// thinking levels. That is the only case the form asks for them: a model the
// catalog knows carries its own list, and asking would offer a choice the model
// never agreed to.
func (s *llmSetup) asksThinkingLevels() bool {
	if !s.choice.Custom || s.model == "" {
		return false
	}
	return len(turn.ModelReasoningEfforts(appcfg.CustomProvider, s.model)) == 0
}

// customThinkingLevels lets the user state the levels the model takes. Ticking
// none is a valid answer — the model simply gets no effort picker.
func (s *llmSetup) customThinkingLevels() (bool, error) {
	const title = "Thinking levels this model supports\nNo official levels found for this model, so tick the ones it takes. Leave empty to skip."
	items := make([]SelectItem, len(customThinkingLevels))
	checked := make([]int, 0, len(s.efforts))
	for i, level := range customThinkingLevels {
		items[i] = SelectItem{Label: level.Level + " · " + level.Chinese, Description: level.English}
		if slices.Contains(s.efforts, level.Level) {
			checked = append(checked, i)
		}
	}
	idxs, ok, err := multiSelectRichTabbed(s.cfg.Selector, title, items, TabbedSelectOptions{Checked: checked})
	if err != nil || !ok {
		return false, err
	}
	efforts := make([]string, 0, len(idxs))
	for _, idx := range idxs {
		if idx >= 0 && idx < len(customThinkingLevels) {
			efforts = append(efforts, customThinkingLevels[idx].Level)
		}
	}
	s.efforts = efforts
	return true, nil
}

// customTestConnection offers one real request before anything is saved, so a
// wrong key or address is found here rather than on the first message. A failed
// test saves nothing and goes back a page, which is where the answer to fix
// lives.
func (s *llmSetup) customTestConnection() (bool, error) {
	test, ok, err := s.cfg.Selector.Confirm("Test connection?\nSends one short request to "+s.endpoint()+" before saving.", true)
	if err != nil || !ok {
		return false, err
	}
	if !test {
		return true, nil
	}
	key := s.apiKey
	start := time.Now()
	probeErr := probeLLMModel(s.ctx(), process.LLMModelInput{
		Provider:    appcfg.CustomProvider,
		Model:       s.model,
		BaseURL:     s.baseURL,
		APIPath:     s.apiPath,
		APIKey:      &key,
		URLVerbatim: s.urlVerbatim,
	})
	if probeErr != nil {
		logPromptChoice(s.cfg, "llmsetup.connection_failed", s.choice, map[string]any{"error": errString(probeErr)})
		s.note("Connection failed\n"+probeErr.Error(), "Nothing was saved.")
		return false, nil
	}
	logPromptChoice(s.cfg, "llmsetup.connection_ok", s.choice, map[string]any{"model": s.model})
	s.note("Connection OK", s.model+" answered in "+probeElapsed(start)+".")
	return true, nil
}

// probeElapsed names how long the test request took, in the one decimal a
// person reads at a glance.
func probeElapsed(start time.Time) string {
	return fmt.Sprintf("%.1fs", time.Since(start).Seconds())
}

// note shows a report the user only has to read: an overlay when the selector
// has one, the setup's own output otherwise. Both the head and the body reach
// the text surface, because the head is where the outcome is stated.
func (s *llmSetup) note(title, body string) {
	if overlay, ok := s.cfg.Selector.(InfoOverlaySelector); ok {
		if err := overlay.ShowInfo(title, strings.TrimSpace(body)); err == nil {
			return
		}
	}
	status := s.statusOutput(title)
	if status == nil {
		return
	}
	fmt.Fprintln(status, strings.TrimSpace(title+"\n"+body))
}

// signIn signs in to the subscription this choice names, and leaves what the
// sign-in unlocked to the offers page.
//
// The registry decides the flow; this machine decides whether a browser is
// opened here, because only a surface sits in front of the user. A login this
// machine already holds that lists its models IS the finished sign-in, so the
// browser and device flows never repeat for it.
func (s *llmSetup) signIn(sub subscription.Subscription) (bool, error) {
	label, _ := sub.Label()
	check := s.probeSubscription(sub)
	switch check.State {
	case turn.SignInUsable:
		logPromptChoice(s.cfg, "llmsetup.login_reused", s.choice, nil)
		return true, nil
	case turn.SignInUnreachable:
		// The login stands; only the listing failed, and the offers page is
		// where a retry belongs — never here, where it would sign in again.
		logPromptChoice(s.cfg, "llmsetup.login_unreachable", s.choice, map[string]any{"error": errString(check.Err)})
		return true, nil
	}
	logPromptChoice(s.cfg, "llmsetup.login_missing", s.choice, map[string]any{"error": errString(check.Err)})
	flow := drivableFlow(sub)
	if flow == "" {
		return false, fmt.Errorf("%s offers no sign-in this terminal can drive", label)
	}
	// A sign-in has an irreversible, out-of-band side effect — it opens a
	// browser and then waits for a person — so it asks first. The API key page
	// asks for a key before doing anything, which is its own confirmation.
	start, ok, err := s.cfg.Selector.Confirm(signInPrompt(label, flow), false)
	if err != nil || !ok || !start {
		logPromptChoice(s.cfg, "llmsetup.sign_in_declined", s.choice, map[string]any{"error": errString(err)})
		return false, err
	}
	ctx := s.ctx()
	login, err := sub.Start(ctx, subscription.Env{Home: s.cfg.Home}, flow)
	if err != nil {
		return false, err
	}
	defer login.Cancel()
	status := s.statusOutput("Sign in with " + label)
	// The flow waits for a human in another window and prints nothing here, so
	// without a live line the screen stands still and the user cannot tell a
	// slow sign-in from a hang.
	end := s.startConnectProgress(func() string { return signInWaitingText(label, flow) })
	address := awaitSignInURL(ctx, login)
	if status != nil {
		if page := signInPage(login, address, flow); page != "" {
			fmt.Fprintln(status, page)
		}
	}
	if address != "" && localBrowser() && flow != subscription.FlowDeviceCode {
		if openErr := browserOpener(address, func(err error) {
			logPromptChoice(s.cfg, "llmsetup.browser_open_failed", s.choice, map[string]any{"error": errString(err)})
		}); openErr != nil && status != nil {
			fmt.Fprintln(status, "Could not open your browser; the link above still works.")
		}
	}
	if flow == subscription.FlowPasteCallback {
		if err := s.pasteSignInCallback(ctx, login, status); err != nil {
			end()
			return false, err
		}
	}
	err = login.Wait(ctx)
	if end != nil {
		end()
	}
	if err != nil {
		return false, err
	}
	logPromptChoice(s.cfg, "llmsetup.signed_in", s.choice, map[string]any{"flow": flow})
	return true, nil
}

// pasteSignInCallback takes the address a browser on another machine ended on
// and hands it to the flow. A rejected address leaves the sign-in running: the
// person who pasted it can try again.
func (s *llmSetup) pasteSignInCallback(ctx context.Context, login subscription.Login, status io.Writer) error {
	const title = "Finish signing in\nSign in, then paste the address your browser ended on."
	for {
		pasted, ok, err := s.cfg.Selector.Input(title, "")
		if err != nil || !ok {
			return err
		}
		if err := login.Submit(ctx, strings.TrimSpace(pasted)); err != nil {
			if status != nil {
				fmt.Fprintln(status, err.Error())
			}
			continue
		}
		return nil
	}
}

// drivableFlow is the first flow the registry lists that this terminal can
// drive. A machine with no browser of its own — a headless or SSH session —
// cannot finish a loopback callback, so it takes the flow that pastes the
// address back instead; a device code needs no browser either way.
func drivableFlow(sub subscription.Subscription) string {
	flows := sub.Flows()
	headless := !localBrowser()
	for _, flow := range flows {
		if flow == subscription.FlowBrowserCallback && headless {
			continue
		}
		return flow
	}
	if len(flows) > 0 {
		return flows[0]
	}
	return ""
}

// localBrowser answers whether this process can open a browser the user can see.
// It is the one judgement only a surface can make, and the flows never make it:
// an SSH session has the user's browser somewhere else, and a desktop machine —
// macOS and Windows included, where no DISPLAY is set — has it here. It is a
// variable so a test drives the browser flow and the paste flow on one machine.
var localBrowser = hasLocalBrowser

// hasLocalBrowser is localBrowser's production answer.
func hasLocalBrowser() bool {
	if strings.TrimSpace(os.Getenv("SSH_CONNECTION")) != "" {
		return false
	}
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	}
	return strings.TrimSpace(os.Getenv("DISPLAY")) != "" || strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")) != ""
}

// signInPrompt is what the sign-in asks before it starts, in this terminal's own
// words: the registry names the plan, the surface says what will happen on this
// machine.
func signInPrompt(label, flow string) string {
	what := "opens your browser to sign in"
	if flow == subscription.FlowPasteCallback {
		what = "opens the sign-in page, and takes back the address your browser ended on"
	}
	if flow == subscription.FlowDeviceCode {
		what = "shows a code to confirm in the browser"
	}
	return "Sign in with " + label + "\nForebrain Harness " + what + ", then offers the models your " + label + " plan includes."
}

// signInWaitingText is the live line while the sign-in runs.
func signInWaitingText(label, flow string) string {
	if flow == subscription.FlowDeviceCode {
		return "Waiting for you to confirm the code in " + label + "…"
	}
	return "Waiting for you to finish signing in…"
}

// signInPage is what the sign-in page says while the flow runs: the address to
// open, the code to confirm for a device flow, and the address to paste back for
// a callback the browser cannot reach.
func signInPage(login subscription.Login, address, flow string) string {
	lines := make([]string, 0, 3)
	switch flow {
	case subscription.FlowDeviceCode:
		if address != "" {
			lines = append(lines, "Open "+address+" and enter this code.", "")
		}
		if code := login.UserCode(); code != "" {
			lines = append(lines, code)
		}
	case subscription.FlowPasteCallback:
		if address != "" {
			lines = append(lines, "Open this URL on any device: "+address)
		}
	default:
		if address != "" {
			lines = append(lines, "Opening your browser…", "", "If it did not open, visit: "+address)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// awaitSignInURL is the address the flow hands over, as soon as it has one. The
// registry publishes it while the flow runs rather than handing it back, so it
// is polled; the wait is bounded, because a flow that never names one must not
// hold the input loop, and the sign-in itself is still bounded by the setup's
// own context.
func awaitSignInURL(ctx context.Context, login subscription.Login) string {
	const poll = 50 * time.Millisecond
	const wait = 30 * time.Second
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		if address := login.AuthorizeURL(); address != "" {
			return address
		}
		select {
		case <-ctx.Done():
			return ""
		case <-deadline.C:
			return ""
		case <-time.After(poll):
		}
	}
}

// pickOffers shows the models the sign-in unlocked and writes the ones the user
// ticks. done is true once the rows are written; the first of them is the model
// the surface then switches the session to.
func (s *llmSetup) pickOffers() (bool, error) {
	sub := s.subscription()
	if sub == nil {
		return false, fmt.Errorf("this sign-in has no subscription to add models from")
	}
	label, _ := sub.Label()
	if s.offers == nil {
		offers, err := s.fetchOffers(sub)
		if err != nil {
			return false, err
		}
		s.offers = offers
	}
	if len(s.offers) == 0 {
		return false, fmt.Errorf("%s offers no models this build can run", label)
	}
	items := make([]SelectItem, len(s.offers))
	for i, offer := range s.offers {
		items[i] = SelectItem{
			Label:       offer.Model,
			Description: turn.ModelEndpointLabel(offer.BaseURL, offer.APIPath, false),
			Category:    offer.PlanLabel,
		}
	}
	title := label + "\nChoose the models to add; each row shows where requests go."
	checked := s.chosen
	if checked == nil {
		checked = defaultOffers(s.offers)
	}
	for {
		idxs, ok, err := multiSelectRichTabbed(s.cfg.Selector, title, items, TabbedSelectOptions{Checked: checked})
		if err != nil || !ok {
			return false, err
		}
		if len(idxs) == 0 {
			s.note("Choose at least one model", "Tick the models to add with Space, then press Enter.")
			checked = nil
			continue
		}
		chosen := make([]subscription.Offer, 0, len(idxs))
		for _, idx := range idxs {
			if idx >= 0 && idx < len(s.offers) {
				chosen = append(chosen, s.offers[idx])
			}
		}
		// Which of these may the agent pick on its own? Asked before the rows
		// are written, so the switch lands with them (owner 2026-11-10). Esc
		// goes back to the model list.
		allowed, ok, err := s.pickOfferSubagents(chosen, idxs)
		if err != nil {
			return false, err
		}
		if !ok {
			checked = idxs
			continue
		}
		env, err := s.subscriptionEnv()
		if err != nil {
			return false, err
		}
		ids, err := sub.Provision(s.ctx(), env, chosen, subscription.ProvisionOptions{})
		if err != nil {
			// The registry's own sentence: it names what the user has to do and
			// never a key. Nothing it refused is written.
			logPromptChoice(s.cfg, "llmsetup.provision_failed", s.choice, map[string]any{"error": errString(err)})
			s.note("Adding the models failed", err.Error())
			checked = idxs
			continue
		}
		// provisionRows writes one row per offer, in input order, so the ids
		// line up with chosen and with the answers.
		if len(ids) != len(allowed) {
			return false, fmt.Errorf("%s wrote %d rows for %d models", label, len(ids), len(allowed))
		}
		models := &state.LLMModelStore{DB: env.DB}
		var failed error
		for i, id := range ids {
			if err := models.SetSubagentAllowed(s.ctx(), id, allowed[i]); err != nil {
				failed = err
				break
			}
		}
		if failed != nil {
			logPromptChoice(s.cfg, "llmsetup.subagent_allowed.failed", s.choice, map[string]any{"error": errString(failed)})
			s.note("Saving the subagent authorization failed", failed.Error())
			checked = idxs
			continue
		}
		s.chosen = idxs
		logPromptChoice(s.cfg, "llmsetup.provisioned", s.choice, map[string]any{"count": len(chosen)})
		return true, nil
	}
}

// defaultOffers is what the offers page opens with: the models the subscription
// marks as its default, or — when it marks none — its first row. Starting with
// nothing ticked would make Enter a no-op.
func defaultOffers(offers []subscription.Offer) []int {
	checked := make([]int, 0, len(offers))
	for i, offer := range offers {
		if offer.Default {
			checked = append(checked, i)
		}
	}
	if len(checked) == 0 && len(offers) > 0 {
		checked = append(checked, 0)
	}
	return checked
}

// fetchOffers lists what the sign-in unlocked, and is where a failed listing is
// handled: the login stands, so the offered retry reuses it instead of repeating
// the browser or device flow, and declining aborts with an actionable error — a
// half-configured provider is never written.
func (s *llmSetup) fetchOffers(sub subscription.Subscription) ([]subscription.Offer, error) {
	label, _ := sub.Label()
	status := s.statusOutput("Sign in with " + label)
	for {
		fetchCtx, cancel := context.WithTimeout(s.ctx(), modelsDiscoveryTimeout)
		if status != nil {
			fmt.Fprintln(status, "Fetching this account's available models…")
		}
		offers, err := sub.Offers(fetchCtx, subscription.Env{Home: s.cfg.Home})
		cancel()
		if err == nil {
			logPromptChoice(s.cfg, "llmsetup.models_fetched", s.choice, map[string]any{"count": len(offers)})
			return offers, nil
		}
		logPromptChoice(s.cfg, "llmsetup.models_failed", s.choice, map[string]any{"error": errString(err)})
		retry, ok, confirmErr := s.cfg.Selector.Confirm("Fetching this account's models failed\nRetry without signing in again?", true)
		if confirmErr != nil || !ok || !retry {
			return nil, fmt.Errorf(
				"login saved, but the account's models could not be fetched: %w\nNo provider configuration was written; run /connect again later, or choose another provider", err)
		}
	}
}

// subscriptionEnv is what the registry reads and writes: this home, and the
// install's state database the rows land in.
func (s *llmSetup) subscriptionEnv() (subscription.Env, error) {
	db, err := s.stateDB()
	if err != nil {
		return subscription.Env{}, err
	}
	return subscription.Env{Home: s.cfg.Home, DB: db}, nil
}

// stateDB is the database this install's configuration lives in, opened once per
// setup and closed when it ends: the same handle process.Execute opens, so the
// row the setup checks for and the row it writes are the same row.
func (s *llmSetup) stateDB() (*sql.DB, error) {
	if s.db != nil {
		return s.db, nil
	}
	db, err := process.StateDB(process.Context{Home: s.cfg.Home, ConfigPath: s.cfg.ConfigPath})
	if err != nil {
		return nil, err
	}
	s.db = db
	return db, nil
}

func (s *llmSetup) closeState() {
	if s.db == nil {
		return
	}
	_ = s.db.Close()
	s.db = nil
}

// startConnectProgress installs the sign-in's live line when the
// selector can show one. It returns nil when the selector has no such
// capability.
func (s *llmSetup) startConnectProgress(stage func() string) func() {
	cps, ok := s.cfg.Selector.(ConnectProgressSelector)
	if !ok {
		return nil
	}
	return cps.ConnectProgress(stage)
}

// ctx is the setup's context, never nil: a login or a fetch must be cancellable
// even when the caller passed none.
func (s *llmSetup) ctx() context.Context {
	if s.cfg.Context != nil {
		return s.cfg.Context
	}
	return context.Background()
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
	for {
		model, ok, err := promptModel(s.cfg.Selector, options, firstNonEmpty(s.model, strings.TrimSpace(s.cfg.Defaults.Model)))
		if err != nil || !ok {
			if err == nil {
				logPromptChoice(s.cfg, "llmsetup.model_cancelled", s.choice, nil)
			}
			return false, err
		}
		params, ok, err := promptReasoningEffort(s.cfg, s.choice, model, s.effortAnswer())
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
// endpoint is changed: most setups never need to. A model this install already
// holds is never replaced silently — the save asks first.
func (s *llmSetup) review() (setupReviewAction, error) {
	save := "Save"
	if s.cfg.Onboarding {
		save = "Save and continue"
	}
	const changeEndpoint, startOver = "Change endpoint…", "Start over"
	actions := []string{save}
	if !s.choice.Custom {
		actions = append(actions, changeEndpoint)
	}
	actions = append(actions, startOver)
	const title = "Review\nCheck the details, then save."
	chosen := -1 // the action taken last, when the page shows again after it
	for {
		idx, ok, err := s.cfg.Selector.Review(title, s.reviewFacts(), actions, chosen)
		if err != nil || !ok || idx < 0 || idx >= len(actions) {
			return setupReviewBack, err
		}
		chosen = idx
		switch actions[idx] {
		case save:
			replace, err := s.confirmReplace()
			if err != nil {
				return setupReviewBack, err
			}
			if !replace {
				continue
			}
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

// confirmReplace asks before a save that would replace the key of a row this
// install already holds. Saving is an update by identity — /connect signing in
// again is meant to replace the key — so the one thing it must never do is
// overwrite without saying so.
func (s *llmSetup) confirmReplace() (bool, error) {
	provider, model, baseURL, apiPath := s.identity()
	db, err := s.stateDB()
	if err != nil {
		return false, err
	}
	row, found, err := (&state.LLMModelStore{DB: db}).FindByIdentity(s.ctx(), provider, model, baseURL, apiPath)
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil
	}
	replace, ok, err := s.cfg.Selector.Confirm(
		"Already configured\nThis provider, model, base URL and path are already configured. Replace its key?", false)
	if err != nil || !ok || !replace {
		logPromptChoice(s.cfg, "llmsetup.duplicate_declined", s.choice, map[string]any{"row": row.ID})
		return false, err
	}
	logPromptChoice(s.cfg, "llmsetup.duplicate_replaced", s.choice, map[string]any{"row": row.ID})
	return true, nil
}

// identity is the four values a model row is recognised by, exactly as the save
// will normalize them.
func (s *llmSetup) identity() (provider, model, baseURL, apiPath string) {
	return appcfg.NormalizeLLMModelIdentity(strings.TrimSpace(s.choice.Provider), s.model, s.baseURL, s.apiPath)
}

// The subagent authorization page's two answers (owner 2026-11-10). They are
// the stored switch's wording, so the page, the review line and the model page
// say the same thing.
const (
	subagentsNo  = "No, only when you pick it"
	subagentsYes = "Yes, the agent may use it"
)

// subagentAllowedLabel is the switch as the review line states it.
func subagentAllowedLabel(allowed bool) string {
	if allowed {
		return subagentsYes
	}
	return subagentsNo
}

// storedRow is the row this flow's answers save over, when the install already
// holds it.
func (s *llmSetup) storedRow() (state.LLMModel, bool, error) {
	provider, model, baseURL, apiPath := s.identity()
	db, err := s.stateDB()
	if err != nil {
		return state.LLMModel{}, false, err
	}
	return (&state.LLMModelStore{DB: db}).FindByIdentity(s.ctx(), provider, model, baseURL, apiPath)
}

// pickSubagents asks whether the agent may pick this row for a subagent on its
// own — its own page, after the model and before the review, because the answer
// is about the row being configured and not about who the agent talks to. The
// default is this flow's earlier answer, else the row's stored switch, else No:
// every row that predates the switch, and every install that never answered,
// stays closed.
func (s *llmSetup) pickSubagents() (bool, error) {
	current := s.subagentAllowed
	if !s.subagentAnswered {
		row, found, err := s.storedRow()
		if err != nil {
			return false, err
		}
		if found {
			current = row.SubagentAllowed
		}
	}
	defaultOption := subagentsNo
	if current {
		defaultOption = subagentsYes
	}
	selected, ok, err := s.cfg.Selector.Select(
		"Subagents\nMay the agent pick this model for subagents on its own?",
		[]string{subagentsNo, subagentsYes}, defaultOption)
	if err != nil || !ok {
		return false, err
	}
	s.subagentAllowed = strings.EqualFold(strings.TrimSpace(selected), subagentsYes)
	s.subagentAnswered = true
	logPromptChoice(s.cfg, "llmsetup.subagent_allowed.selected", s.choice, map[string]any{"allowed": s.subagentAllowed})
	return true, nil
}

// offerSubagentFor is the subscription answer for the row a finished flow
// switches the session to: the first offer the user ticked.
func (s *llmSetup) offerSubagentFor(chosen []int) bool {
	for _, idx := range chosen {
		if idx >= 0 && idx < len(s.offers) {
			return s.offerSubagent[idx]
		}
	}
	return false
}

// storedOfferAllowed reports whether an already configured offer row is open to
// the agent already, which is the check list's default when this flow has not
// answered yet.
func (s *llmSetup) storedOfferAllowed(offer subscription.Offer) bool {
	provider, model, baseURL, apiPath := appcfg.NormalizeLLMModelIdentity(offer.Provider, offer.Model, offer.BaseURL, offer.APIPath)
	db, err := s.stateDB()
	if err != nil {
		return false
	}
	row, found, err := (&state.LLMModelStore{DB: db}).FindByIdentity(s.ctx(), provider, model, baseURL, apiPath)
	if err != nil || !found {
		return false
	}
	return row.SubagentAllowed
}

// pickOfferSubagents asks, for the models a sign-in unlocked, which of them the
// agent may pick for subagents on its own (owner 2026-11-10). Esc goes back to
// the model list, so a user who wants fewer rows can change them. The answer
// comes back aligned with chosen.
func (s *llmSetup) pickOfferSubagents(chosen []subscription.Offer, chosenIdxs []int) ([]bool, bool, error) {
	items := make([]SelectItem, len(chosen))
	checked := make([]int, 0, len(chosen))
	for i, offer := range chosen {
		items[i] = SelectItem{
			Label:       offer.Model,
			Description: turn.ModelEndpointLabel(offer.BaseURL, offer.APIPath, false),
			Category:    offer.PlanLabel,
		}
		allowed := false
		if s.offerSubagentAnswered {
			allowed = s.offerSubagent[chosenIdxs[i]]
		} else {
			allowed = s.storedOfferAllowed(offer)
		}
		if allowed {
			checked = append(checked, i)
		}
	}
	idxs, ok, err := multiSelectRichTabbed(s.cfg.Selector,
		"Subagents\nWhich of these may the agent pick for subagents on its own?",
		items, TabbedSelectOptions{Checked: checked})
	if err != nil || !ok {
		return nil, false, err
	}
	answers := make([]bool, len(chosen))
	if s.offerSubagent == nil {
		s.offerSubagent = make(map[int]bool, len(chosen))
	}
	for _, idx := range idxs {
		if idx >= 0 && idx < len(answers) {
			answers[idx] = true
		}
	}
	for i, offerIdx := range chosenIdxs {
		s.offerSubagent[offerIdx] = answers[i]
	}
	s.offerSubagentAnswered = true
	return answers, true, nil
}

// reviewFacts are the answers as the review lists them.
func (s *llmSetup) reviewFacts() []turn.StatusFact {
	if s.choice.Custom {
		return []turn.StatusFact{
			{Label: "Provider", Value: appcfg.CustomProvider},
			{Label: "Model", Value: s.model},
			{Label: "Protocol", Value: s.protocolLabel()},
			{Label: "Endpoint", Value: s.endpoint()},
			{Label: "Thinking", Value: thinkingLabel(s.efforts)},
			{Label: "Subagents", Value: subagentAllowedLabel(s.subagentAllowed)},
		}
	}
	provider := s.choice.Group
	if s.choice.Group != "" && !strings.HasPrefix(s.choice.Label, s.choice.Group) {
		provider += " · " + s.choice.Label
	}
	effort := reasoningEffortFromParamsJSON(s.params)
	if effort == "" {
		effort = "Provider default"
	}
	endpoint := s.baseURL
	if s.apiPath != "" {
		endpoint += " · " + s.apiPath
	}
	return []turn.StatusFact{
		{Label: "Provider", Value: provider},
		{Label: "Model", Value: s.model},
		{Label: "Reasoning", Value: effort},
		{Label: "Endpoint", Value: endpoint},
		{Label: "Subagents", Value: subagentAllowedLabel(s.subagentAllowed)},
	}
}

// protocolLabel names the request shape a custom model uses.
func (s *llmSetup) protocolLabel() string {
	for _, protocol := range customProtocols {
		if protocol.APIPath == s.apiPath && protocol.Verbatim == s.urlVerbatim {
			return protocol.Label
		}
	}
	return s.apiPath
}

// thinkingLabel is the levels a custom model was told to support, as the review
// lists them.
func thinkingLabel(efforts []string) string {
	if len(efforts) == 0 {
		return "Not set"
	}
	return strings.Join(efforts, ", ")
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
		options = append(options, modelOption{Label: rec.APIModel, Value: rec.APIModel})
	}
	return options
}

// promptModel asks for the model: from the options, or typed as an ID when none
// fits — or when there are none. Esc on the typed ID goes back to the options.
func promptModel(selector SetupSelector, options []modelOption, defaultValue string) (string, bool, error) {
	defaultValue = strings.TrimSpace(defaultValue)
	typed := func() (string, bool, error) {
		for {
			value, ok, err := selector.Input("Model ID", defaultValue)
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

// reasoningEffortProviderDefault is used when neither the existing config nor
// the model's own levels say otherwise.
const reasoningEffortProviderDefault = "medium"

// reasoningEffortUnset lets the operator keep the provider's own default
// instead of pinning a level into the config.
const reasoningEffortUnset = "Provider default"

// promptReasoningEffort asks for the reasoning effort of the model just chosen
// and returns the params JSON to persist with the provider entry. The levels
// come from turn.ModelReasoningEfforts, the same answer /model and the gateway
// read (models.json's reasoning_options, trimmed to what this build can send).
// A model with no levels gets no question at all, so the request never carries
// a parameter the provider would reject.
func promptReasoningEffort(cfg PromptConfig, choice providerChoice, model string, previous string) (string, bool, error) {
	existing := currentProviderParamsJSON(cfg, choice)
	efforts := turn.ModelReasoningEfforts(choice.Provider, model)
	defaultEffort := ""
	// A model the catalog has levels for gets a sensible preselected one; unset
	// lets the provider apply its own default otherwise.
	if len(efforts) > 0 {
		defaultEffort = reasoningEffortProviderDefault
	}
	if len(efforts) == 0 {
		logPromptChoice(cfg, "llmsetup.reasoning_effort.not_supported", choice, map[string]any{"model": model})
		return applyReasoningEffort(existing, ""), true, nil
	}
	return promptEffortChoice(cfg, choice, model, efforts, defaultEffort, previous)
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
	return c.runConnect(ctx, sessionID, PromptMainLLMSetup)
}

// runConnect runs the model setup the caller named, saves its answers and makes
// the model this session's own.
func (c *commandController) runConnect(ctx context.Context, sessionID string, ask func(PromptConfig) (process.Options, bool, error)) bool {
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
	opts, ok, err := ask(PromptConfig{
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
	if err := c.session.SyncConfig(ctx); err != nil {
		c.renderer.PrintError(fmt.Errorf("connect: config saved but the active session could not adopt it: %w", err))
		return true
	}
	// The connected model is now this session's own choice, not the file
	// default: apply and persist the model the wizard just configured for the
	// conversation the user is in. The wizard's own selection (opts) is the
	// value — rep re-reads the file's effective default, which since the
	// order-freeze lands on the first entry, not the new one — and rep.ModelID
	// is the row it stored, which is what tells twin rows apart.
	if strings.TrimSpace(opts.Provider) != "" && strings.TrimSpace(opts.Model) != "" {
		selectErr := c.session.SelectModel(ctx, sessionID, turn.ModelChoice{
			ID:               rep.ModelID,
			Provider:         opts.Provider,
			Model:            opts.Model,
			ReasoningEfforts: turn.ModelReasoningEfforts(opts.Provider, opts.Model),
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
	// Report the model the wizard configured, the one this session now runs
	// on — not the file's effective default, which the append-only write
	// leaves on the first entry.
	summary := strings.TrimSpace(opts.Provider)
	if m := strings.TrimSpace(opts.Model); m != "" {
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
// configuration as it was when this process started, and setup writes the whole
// document back: everything changed since — /model, /sandbox, an edit made in
// another terminal — would be reverted by a command the user ran to change one
// provider. It reads the persisted form for the same reason /model does, since
// Load expands ${ENV} references and writing the expansion back would bake one
// machine's environment into the configuration.
func connectRuntimeContext() (process.Context, error) {
	rt, err := process.Resolve()
	if err != nil {
		return rt, err
	}
	persisted, err := process.PersistedStartupConfig(context.Background(), rt.Home)
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
	refreshSessionFooter(c.renderer, c.session, sessionID)
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
	if _, ok := cfg.Agents.Definitions[name]; !ok {
		return nil
	}
	return appcfg.EffectiveDefaultLLMFor(cfg, name)
}

// startupJudgeConfig returns the configuration the setup gates judge by:
// the composed one when the state database holds sections (the legacy import
// ran and emptied the file — plan 187), otherwise the resolved file copy. A
// gate that judged by the file alone would send every migrated install back
// through first-run setup.
func startupJudgeConfig(rt process.Context) (appcfg.Root, error) {
	if composed, ok, err := process.ComposedStartupConfig(context.Background(), rt.Home); err != nil {
		return appcfg.Root{}, err
	} else if ok {
		return composed, nil
	}
	return rt.Config, nil
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
	cfg, err := startupJudgeConfig(rt)
	if err != nil {
		return false, err
	}
	return !mainLLMConfigured(&cfg), nil
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
	judged, err := startupJudgeConfig(rt)
	if err != nil {
		return err
	}
	missing := MainAgentLLMMissingFieldsWithConfig(judged)
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrStartupConfigIncomplete, formatStartupConfigError(home, cfgPath, missing))
}

func formatStartupConfigError(home, cfgPath string, missing []string) string {
	var b strings.Builder
	b.WriteString("main agent LLM is not fully configured; set the missing fields (")
	b.WriteString(strings.Join(missing, ", "))
	b.WriteString(") on the effective default entry — agents.definitions.main.default_model's target, or the first llm_providers entry — in ")
	b.WriteString(cfgPath)
	b.WriteString(". Add a model with /connect, or on the web Models page.")
	b.WriteString(" If this is the first setup, run `forebrain` or `forebrain gateway start` in a TTY.")
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
