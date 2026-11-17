package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/subscription"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

func TestStartupConfigErrorReportsUnconfiguredModel(t *testing.T) {
	process.ResetResolve()
	home := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", home)
	cfg := "agents:\n  definitions:\n    main:\n      llm_providers:\n        - provider: openai\n          model: gpt-5\n          api_key: ${OPENAI_API_KEY}\n          base_url: https://api.openai.com/v1\n"
	if err := os.WriteFile(filepath.Join(home, "forebrain.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := os.Stat(filepath.Join(home, ".env")); !os.IsNotExist(err) {
		t.Fatalf("expected missing .env, got err=%v", err)
	}
	needs, err := NeedsFirstSetup()
	if err != nil {
		t.Fatalf("NeedsFirstSetup: %v", err)
	}
	if !needs {
		// The ${OPENAI_API_KEY} reference expands to an empty key, so the
		// main agent LLM is not fully configured: first setup is not done.
		t.Fatal("NeedsFirstSetup = false for copied config whose env secret is missing, want true")
	}
	startupErr := StartupConfigError()
	if startupErr == nil {
		t.Fatal("StartupConfigError = nil, want diagnostic")
	}
	msg := startupErr.Error()
	if !strings.Contains(msg, "api_key") || !strings.Contains(msg, "Add a model with /connect") {
		t.Fatalf("diagnostic should name the missing field and where to add a model, got %q", msg)
	}
	if strings.Contains(msg, ".env") {
		t.Fatalf("diagnostic must not name a retired storage location, got %q", msg)
	}
}

// The helpers below were production functions that only the tests ever
// called: each is a thin composition of live code. They live here so the
// production files carry no unused code while the tests keep exercising
// the live functions underneath.

func MainAgentLLMMissingFields() ([]string, error) {
	rt, err := process.Resolve()
	if err != nil {
		return nil, err
	}
	return MainAgentLLMMissingFieldsWithConfig(rt.Config), nil
}

func MainAgentLLMConfigured() (bool, error) {
	rt, err := process.Resolve()
	if err != nil {
		return false, err
	}
	return MainAgentLLMConfiguredWithConfig(rt.Config), nil
}

// MainAgentLLMConfiguredWithConfig is the config-parameterised variant of
// MainAgentLLMConfigured that avoids an extra process.Resolve() call
// when the caller already has a loaded config.
func MainAgentLLMConfiguredWithConfig(cfg appcfg.Root) bool {
	return appcfg.ValidateMainAgentLLMConfigured(&cfg).Complete()
}

func TestMainAgentLLMConfiguredTrueWhenMainLLMFullyConfigured(t *testing.T) {
	process.ResetResolve()
	home := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-test")
	cfg := "agents:\n  definitions:\n    main:\n      llm_providers:\n        - provider: openai\n          model: gpt-5\n          api_key: ${OPENAI_API_KEY}\n          base_url: https://api.openai.com/v1\n"
	if err := os.WriteFile(filepath.Join(home, "forebrain.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ok, err := MainAgentLLMConfigured()
	if err != nil {
		t.Fatalf("MainAgentLLMConfigured: %v", err)
	}
	if !ok {
		t.Fatal("MainAgentLLMConfigured = false, want true")
	}
	needs, err := NeedsFirstSetup()
	if err != nil {
		t.Fatalf("NeedsFirstSetup: %v", err)
	}
	if needs {
		t.Fatal("NeedsFirstSetup = true, want false")
	}
}

func TestMainAgentLLMConfiguredFalseWhenMainLLMFieldsMissing(t *testing.T) {
	process.ResetResolve()
	home := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "forebrain.yaml"), []byte("agents: {}\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ok, err := MainAgentLLMConfigured()
	if err != nil {
		t.Fatalf("MainAgentLLMConfigured: %v", err)
	}
	if ok {
		t.Fatal("MainAgentLLMConfigured = true, want false")
	}
	missing, err := MainAgentLLMMissingFields()
	if err != nil {
		t.Fatalf("MainAgentLLMMissingFields: %v", err)
	}
	want := []string{"provider", "model", "api_key", "base_url"}
	if !reflect.DeepEqual(missing, want) {
		t.Fatalf("missing = %#v, want %#v", missing, want)
	}
	needs, err := NeedsFirstSetup()
	if err != nil {
		t.Fatalf("NeedsFirstSetup: %v", err)
	}
	if !needs {
		// An existing but incomplete config means first setup never finished;
		// a seeded skeleton and a hand-written partial config are the same
		// state to this predicate.
		t.Fatal("NeedsFirstSetup = false for existing incomplete config, want true")
	}
	if err := StartupConfigError(); err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("StartupConfigError = %v, want api_key diagnostic", err)
	}
}

// TestNeedsFirstSetupTrueAfterHomeEnsureSeedsSkeleton is the regression test
// for the fresh-home launch dead end: the interactive launch resolves the
// runtime (project-MCP consent) before it checks for first setup, and that
// resolve seeds an incomplete forebrain.yaml skeleton into the new home. A
// file-existence predicate saw the skeleton and skipped onboarding forever;
// the predicate must still report first setup after the seed.
func TestNeedsFirstSetupTrueAfterHomeEnsureSeedsSkeleton(t *testing.T) {
	process.ResetResolve()
	t.Cleanup(process.ResetResolve)
	home := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", home)
	rt, err := process.Resolve()
	if err != nil {
		t.Fatalf("process.Resolve: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rt.Home, "forebrain.yaml")); err != nil {
		t.Fatalf("expected home.Ensure to seed the config skeleton: %v", err)
	}
	needs, err := NeedsFirstSetup()
	if err != nil {
		t.Fatalf("NeedsFirstSetup: %v", err)
	}
	if !needs {
		t.Fatal("NeedsFirstSetup = false for seeded skeleton config, want true")
	}
}

// TestNeedsFirstSetupReportsAnUnreachableDatabase pins plan 236 step 2 at the
// surface an operator actually sees: a home whose forebrain.yaml names a MySQL
// server this process cannot reach must report that failure, not read the file
// — which holds little more than the URL — and conclude that a configured
// install still has to be set up. The gateway reaches this same predicate
// through cmd/forebrain's gatewayNeedsFirstSetup, which is this function.
func TestNeedsFirstSetupReportsAnUnreachableDatabase(t *testing.T) {
	process.ResetResolve()
	t.Cleanup(process.ResetResolve)
	home := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", home)
	cfg := "gateway:\n  http_addr: 127.0.0.1:6060\ndatabase:\n  url: mysql://fb:s3cret-pw@127.0.0.1:1/forebrain\n"
	if err := os.WriteFile(filepath.Join(home, "forebrain.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	needs, err := NeedsFirstSetup()
	if err == nil {
		t.Fatalf("NeedsFirstSetup = (%v, nil) for an unreachable database, want the error", needs)
	}
	if needs {
		t.Fatalf("NeedsFirstSetup = (true, %v), want (false, err)", err)
	}
	if cfgPath := filepath.Join(home, "forebrain.yaml"); !strings.Contains(err.Error(), cfgPath) {
		t.Fatalf("NeedsFirstSetup error %q does not name the config file %s", err, cfgPath)
	}
	if strings.Contains(err.Error(), "s3cret-pw") {
		t.Fatalf("NeedsFirstSetup error %q echoes the password", err)
	}
}

// useSetupTestCatalog pins the shared catalog to a small deterministic set so
// provider-option assertions do not depend on the embedded asset's ordering.
// The reasoning_options mirror each model's real shipped row: the levels a
// picker offers are catalog data now, so a fixture without them would describe
// a model with nothing to choose.
func useSetupTestCatalog(t *testing.T) {
	t.Helper()
	cat, err := llm.Parse([]byte(`{
		"anthropic/claude-sonnet-4-6": {"name": "Claude Sonnet 4.6", "reasoning": true,
			"reasoning_options": [{"type": "effort", "values": ["low", "medium", "high", "max"]}, {"type": "budget_tokens", "min": 1024}],
			"limit": {"context": 200000, "output": 64000}},
		"anthropic/claude-haiku-lite": {"name": "Claude Haiku Lite", "reasoning": false,
			"limit": {"context": 200000, "output": 32000}},
		"alibaba/qwen3.5-plus": {"name": "Qwen3.5 Plus", "reasoning": true,
			"reasoning_options": [{"type": "toggle"}, {"type": "budget_tokens"}],
			"limit": {"context": 131072, "output": 8192}},
		"google/gemini-3.1-pro-preview": {"name": "Gemini 3.1 Pro Preview", "reasoning": true,
			"reasoning_options": [{"type": "effort", "values": ["low", "medium", "high"]}],
			"limit": {"context": 1000000, "output": 65536}},
		"minimax/MiniMax-M2.7": {"name": "MiniMax M2.7", "reasoning": true,
			"reasoning_options": [],
			"limit": {"context": 204800, "output": 24576}},
		"openai/gpt-5.5": {"name": "GPT-5.5", "reasoning": true,
			"reasoning_options": [{"type": "effort", "values": ["none", "low", "medium", "high", "xhigh"]}],
			"limit": {"context": 272000, "output": 128000}}
	}`))
	if err != nil {
		t.Fatalf("parse test catalog: %v", err)
	}
	llm.SetGlobalCatalogForTest(cat)
	if data, err := llm.RepoModelsJSONForTest(); err == nil {
		if embedded, err := llm.Parse(data); err == nil {
			t.Cleanup(func() { llm.SetGlobalCatalogForTest(embedded) })
		}
	}
}

// fakeLogin is one running sign-in the setup drives: the registry hands it back
// before the flow has finished, and every answer it learns is readable while it
// runs, exactly as pkg/subscription's own handle behaves.
type fakeLogin struct {
	url     string
	code    string
	waitErr error
	// submitErr rejects the next pasted address, so a test can prove a rejected
	// paste leaves the sign-in running.
	submitErr error
	// waitRelease, when a test installs one, is the sign-in still waiting for the
	// person in the browser: Wait blocks on it until the test ends the flow (or
	// the wait context is cancelled).
	waitRelease chan error

	submitted []string
	canceled  bool
}

func newFakeLogin(url, code string) *fakeLogin {
	return &fakeLogin{url: url, code: code}
}

func (l *fakeLogin) AuthorizeURL() string { return l.url }
func (l *fakeLogin) UserCode() string     { return l.code }

func (l *fakeLogin) Submit(_ context.Context, pasted string) error {
	l.submitted = append(l.submitted, pasted)
	if l.submitErr != nil {
		err := l.submitErr
		l.submitErr = nil
		return err
	}
	return nil
}

func (l *fakeLogin) Wait(ctx context.Context) error {
	if l.waitRelease != nil {
		select {
		case err := <-l.waitRelease:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// The test's flow is already finished: nothing waits for a person here.
	if err := ctx.Err(); err != nil {
		return nil
	}
	return l.waitErr
}

func (l *fakeLogin) Cancel() { l.canceled = true }

// fakeSubscription is one registry entry the setup lists and drives: the probe,
// the flow, the listing and the write are all the test's to answer.
type fakeSubscription struct {
	id    string
	label string
	flows []string
	check turn.SignInCheck
	login *fakeLogin
	// panelLogin is a sign-in a test drives from another goroutine: the values it
	// publishes are read under a lock. When it is set, Start hands it back
	// instead of login.
	panelLogin *panelLogin

	offers      []subscription.Offer
	offersFn    func(call int) ([]subscription.Offer, error)
	provisionFn func(call int) error

	probeCalls  int
	startCalls  int
	startedFlow string
	offerCalls  int
	provisioned [][]subscription.Offer
	options     []subscription.ProvisionOptions
	envs        []subscription.Env
}

func (s *fakeSubscription) ID() string              { return s.id }
func (s *fakeSubscription) Label() (string, string) { return s.label, s.label }
func (s *fakeSubscription) Flows() []string         { return s.flows }

func (s *fakeSubscription) Probe(context.Context, subscription.Env) turn.SignInCheck {
	s.probeCalls++
	return s.check
}

func (s *fakeSubscription) Start(_ context.Context, _ subscription.Env, flow string) (subscription.Login, error) {
	s.startCalls++
	s.startedFlow = flow
	if s.panelLogin != nil {
		return s.panelLogin, nil
	}
	if s.login == nil {
		s.login = newFakeLogin("https://signin.example.test/authorize", "")
	}
	return s.login, nil
}

func (s *fakeSubscription) Offers(context.Context, subscription.Env) ([]subscription.Offer, error) {
	s.offerCalls++
	if s.offersFn != nil {
		return s.offersFn(s.offerCalls)
	}
	return s.offers, nil
}

func (s *fakeSubscription) Provision(_ context.Context, env subscription.Env, offers []subscription.Offer, opts subscription.ProvisionOptions) ([]string, error) {
	s.envs = append(s.envs, env)
	s.provisioned = append(s.provisioned, offers)
	s.options = append(s.options, opts)
	if s.provisionFn != nil {
		if err := s.provisionFn(len(s.provisioned)); err != nil {
			return nil, err
		}
	}
	// Write the rows the way a real subscription's provisionRows does: the
	// flow that saves the subagent switch addresses them by id, so the ids have
	// to name rows that exist (a login-backed row holds no key; a key-backed plan
	// carries the one the user pasted, plan 281).
	ids := make([]string, len(offers))
	key := strings.TrimSpace(opts.Key)
	for i, offer := range offers {
		id, err := process.UpsertLLMModel(context.Background(), env.DB, process.LLMModelInput{
			Provider:   offer.Provider,
			Model:      offer.Model,
			BaseURL:    offer.BaseURL,
			APIPath:    offer.APIPath,
			APIKey:     &key,
			Source:     s.id,
			SourcePlan: offer.Plan,
		})
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	return ids, nil
}

func (s *fakeSubscription) RefreshKey(context.Context, subscription.Env, string) error {
	return subscription.ErrNoKeyToRefresh
}

// chatGPTLike is the ChatGPT shape: a loopback callback flow, with the paste flow
// as its headless backup, and the Codex endpoint for every model it unlocks.
func chatGPTLike() *fakeSubscription {
	return &fakeSubscription{
		id: "chatgpt", label: "ChatGPT",
		flows: []string{subscription.FlowBrowserCallback, subscription.FlowPasteCallback},
		check: turn.SignInCheck{State: turn.SignInMissing},
		login: newFakeLogin("https://auth.openai.com/authorize?state=abc", ""),
		offers: []subscription.Offer{
			{Subscription: "chatgpt", Provider: "chatgpt", Model: "gpt-future", BaseURL: openai.CodexBaseURL, APIPath: "/responses", PlanLabel: "ChatGPT plan", Default: true},
			{Subscription: "chatgpt", Provider: "chatgpt", Model: "gpt-second", BaseURL: openai.CodexBaseURL, APIPath: "/responses", PlanLabel: "ChatGPT plan"},
		},
	}
}

// qoderCNLike is a listing-driven sign-in's shape: the wire id the row is
// written with, the vendor's own name for it, and — on one row — no name at
// all, which is the case the wire id has to cover.
func qoderCNLike() *fakeSubscription {
	return &fakeSubscription{
		id: "qodercn", label: "Qoder CN",
		flows: []string{subscription.FlowBrowserCallback, subscription.FlowDeviceCode},
		check: turn.SignInCheck{State: turn.SignInUsable},
		login: newFakeLogin("https://qoder.com.cn/authorize", ""),
		offers: []subscription.Offer{
			{Subscription: "qodercn", Provider: "qodercn", Model: "auto", Name: "Auto", BaseURL: "https://gateway.qoder.com.cn", APIPath: "/algo/api/v2/service/pro/sse/agent_chat_generation", ContextWindow: 1000000, Default: true},
			{Subscription: "qodercn", Provider: "qodercn", Model: "qmodel_38max", Name: "Qwen3.8-Max", BaseURL: "https://gateway.qoder.com.cn", APIPath: "/algo/api/v2/service/pro/sse/agent_chat_generation", ContextWindow: 1000000},
			{Subscription: "qodercn", Provider: "qodercn", Model: "dmodel", BaseURL: "https://gateway.qoder.com.cn", APIPath: "/algo/api/v2/service/pro/sse/agent_chat_generation", ContextWindow: 1000000},
		},
	}
}

// openCodeLike is the OpenCode Go shape: a device code and nothing else.
func openCodeLike() *fakeSubscription {
	base := opencode.ProviderBaseURL("opencode-go")
	return &fakeSubscription{
		id: "opencode-go", label: "OpenCode",
		flows: []string{subscription.FlowDeviceCode},
		check: turn.SignInCheck{State: turn.SignInMissing},
		login: newFakeLogin("https://opencode.ai/auth", "QK7M-2PLX"),
		offers: []subscription.Offer{
			{Subscription: "opencode-go", Provider: "opencode-go", Model: "glm-5.3", BaseURL: base, APIPath: "/chat/completions", Plan: "go", PlanLabel: "Go", Default: true},
			{Subscription: "opencode-go", Provider: "opencode-go", Model: "kimi-k3", BaseURL: base, APIPath: "/chat/completions", Plan: "go", PlanLabel: "Go"},
		},
	}
}

// volcengineLike is the shape plan 281's Volcengine Ark has: a device code, offers
// that ask for a key the vendor never returns, and a write that refuses until it
// is given one.
func volcengineLike() *fakeSubscription {
	sub := &fakeSubscription{
		id: "volcengine", label: "Volcengine Ark",
		flows: []string{subscription.FlowDeviceCode},
		check: turn.SignInCheck{State: turn.SignInMissing},
		login: newFakeLogin("https://signin.volcengine.com/device?user_code=ABCD-1234", "ABCD-1234"),
		offers: []subscription.Offer{
			{Subscription: "volcengine", Provider: "volcengine", Model: "doubao-seed-2.1-pro",
				BaseURL: "https://ark.cn-beijing.volces.com/api/coding", APIPath: "/v1/messages",
				Plan: "coding-plan", PlanLabel: "Coding Plan", KeyRequired: true, Default: true},
		},
	}
	sub.provisionFn = func(attempt int) error {
		if attempt == 1 {
			return subscription.ErrPlanKeyRequired{
				Plan: "coding-plan", PlanLabel: "Coding Plan",
				ConsoleURL: "https://console.volcengine.com/ark/region:ark+cn-beijing/apiKey",
			}
		}
		return nil
	}
	return sub
}

// withSubscriptions replaces the sign-in registry the setup lists and drives,
// for one test.
func withSubscriptions(t *testing.T, subs ...subscription.Subscription) {
	t.Helper()
	previous := subscriptions
	subscriptions = func() []subscription.Subscription { return subs }
	t.Cleanup(func() { subscriptions = previous })
}

// browserMachine makes this machine one with a browser the user can see, and
// records every URL handed to it instead of opening anything.
func browserMachine(t *testing.T) *[]string {
	t.Helper()
	opened := []string{}
	previousOpener, previousBrowser := browserOpener, localBrowser
	browserOpener = func(url string, _ func(error)) error {
		opened = append(opened, url)
		return nil
	}
	localBrowser = func() bool { return true }
	t.Cleanup(func() { browserOpener, localBrowser = previousOpener, previousBrowser })
	return &opened
}

// headlessMachine makes this machine one with no browser of its own — an SSH
// session — and records anything that would have been opened.
func headlessMachine(t *testing.T) *[]string {
	t.Helper()
	opened := browserMachine(t)
	localBrowser = func() bool { return false }
	return opened
}

// setupTestConfig is a throwaway install: the home the setup reads, and the
// config whose state database the rows land in. Every flow that reaches the save
// needs one — the review asks that database whether the row already exists.
func setupTestConfig(t *testing.T, selector SetupSelector) PromptConfig {
	t.Helper()
	home := t.TempDir()
	return PromptConfig{Selector: selector, Output: io.Discard, Home: home, ConfigPath: filepath.Join(home, "forebrain.yaml")}
}

// runSubscriptionSetup runs the model setup from its sign-in page with a
// subscription already chosen, saving at the offers page.
func runSubscriptionSetup(t *testing.T, cfg PromptConfig, sub *fakeSubscription) (process.Options, bool, error) {
	t.Helper()
	withSubscriptions(t, sub)
	setup := newLLMSetup(cfg)
	setup.setChoice(subscriptionChoice(sub))
	setup.page = setupPageCredentials
	return setup.run()
}

// dismissingSelector hands the first multi-select page one dismissal — the Esc
// a person presses to step back — and answers everything after it like the plain
// fake.
type dismissingSelector struct {
	*fakeSelector
	dismissed bool
}

func (s *dismissingSelector) MultiSelectRichTabbed(label string, items []SelectItem, opts TabbedSelectOptions) ([]int, bool, error) {
	if s.dismissed {
		return s.fakeSelector.MultiSelectRichTabbed(label, items, opts)
	}
	s.dismissed = true
	s.multiLabels = append(s.multiLabels, label)
	options := make([]string, len(items))
	for i, item := range items {
		options[i] = item.Label
	}
	s.multiOptions = append(s.multiOptions, options)
	s.multiDefaults = append(s.multiDefaults, append([]int(nil), opts.Checked...))
	return nil, false, nil
}

// indexSelector answers the provider page by row index and records the row the
// page opened on, which is what a label cannot say when two rows share one.
type indexSelector struct {
	*fakeSelector
	answers  []int
	at       int
	defaults []int
}

func (s *indexSelector) SelectRich(label string, items []SelectItem, defaultIdx int) (int, bool, error) {
	s.defaults = append(s.defaults, defaultIdx)
	if s.at >= len(s.answers) {
		return -1, false, nil
	}
	answer := s.answers[s.at]
	s.at++
	if answer < 0 {
		return -1, false, nil
	}
	return answer, true, nil
}

// The first page is three groups in this order: the sign-ins the registry
// lists, the providers reached with a key, and the custom form. The sign-in
// group is the registry's alone — a provider that only takes a key never
// appears in it.
func TestProviderPageListsThreeGroups(t *testing.T) {
	sub := chatGPTLike()
	withSubscriptions(t, sub)
	setup := newLLMSetup(PromptConfig{Selector: &fakeSelector{}, Output: io.Discard, Home: t.TempDir()})
	rows := setup.providerRows()

	var categories []string
	for _, row := range rows {
		if len(categories) == 0 || categories[len(categories)-1] != row.item.Category {
			categories = append(categories, row.item.Category)
		}
	}
	want := []string{providerSignInCategory, providerAPIKeyCategory, providerCustomCategory}
	if !reflect.DeepEqual(categories, want) {
		t.Fatalf("first page groups = %#v, want %#v", categories, want)
	}
	if rows[0].item.Label != "ChatGPT" {
		t.Fatalf("first sign-in row = %q", rows[0].item.Label)
	}
	// The row says what signing in gets the user, in the registry's own words.
	if got := rows[0].item.Description; got != "Use Codex included with your ChatGPT plan" {
		t.Fatalf("sign-in row description = %q", got)
	}
	last := rows[len(rows)-1]
	if last.item.Label != "Custom model" || last.choice.Provider != appcfg.CustomProvider || !last.choice.Custom {
		t.Fatalf("custom row = %+v", last)
	}
	if got := last.item.Description; got != "OpenAI- or Anthropic-compatible, or custom protocol" {
		t.Fatalf("custom row description = %q", got)
	}
	// No sign-in provider is reachable from the API key group.
	for _, row := range rows {
		if row.item.Category != providerAPIKeyCategory {
			continue
		}
		options := []providerChoice{row.choice}
		if row.group != nil {
			options = row.group.Options
		}
		for _, option := range options {
			if appcfg.CredentialsBackedProvider(option.Provider) {
				t.Fatalf("api key group offers the sign-in-backed provider %q", option.Provider)
			}
			if option.APIKeyLabel == "" {
				t.Fatalf("api key choice %+v asks for no key", option)
			}
		}
	}
}

// A subscription this machine is already signed in to says so on its row, and
// the answer is asked once: the sign-in page reads it again from the same probe.
func TestProviderPageMarksASubscriptionAlreadySignedIn(t *testing.T) {
	sub := chatGPTLike()
	sub.check = turn.SignInCheck{State: turn.SignInUsable}
	withSubscriptions(t, sub)
	setup := newLLMSetup(PromptConfig{Selector: &fakeSelector{}, Output: io.Discard, Home: t.TempDir()})
	rows := setup.providerRows()
	if !strings.HasSuffix(rows[0].item.Description, "Signed in") {
		t.Fatalf("signed-in row = %q, want the badge", rows[0].item.Description)
	}
	if sub.probeCalls != 1 {
		t.Fatalf("probe calls = %d, want one", sub.probeCalls)
	}
	// The page shown again does not pay for the vendor's listing twice.
	setup.providerRows()
	if sub.probeCalls != 1 {
		t.Fatalf("probe calls = %d, want the cached answer", sub.probeCalls)
	}
}

// Choosing a subscription signs in, shows the models it unlocked with the
// endpoint each one sends to, and writes only the rows the user ticks. The first
// ticked row is the model the agent takes.
func TestConfigureSubscriptionAddsTheModelsTheUserTicks(t *testing.T) {
	sub := chatGPTLike()
	opened := browserMachine(t)
	selector := &fakeSelector{confirms: []bool{true}, multiSelects: [][]int{{1}}}
	opts, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if sub.startCalls != 1 || sub.startedFlow != subscription.FlowBrowserCallback {
		t.Fatalf("starts = %d flow %q, want the browser callback", sub.startCalls, sub.startedFlow)
	}
	if len(*opened) != 1 || (*opened)[0] != sub.login.url {
		t.Fatalf("opened %v, want the flow's authorize URL %q", *opened, sub.login.url)
	}
	if len(sub.provisioned) != 1 || len(sub.provisioned[0]) != 1 || sub.provisioned[0][0].Model != "gpt-second" {
		t.Fatalf("provisioned %+v, want the one row ticked", sub.provisioned)
	}
	if sub.envs[0].DB == nil || sub.envs[0].Home == "" {
		t.Fatalf("provision env = %+v, want the home and its state database", sub.envs[0])
	}
	if opts.Provider != "chatgpt" || opts.Model != "gpt-second" || opts.BaseURL != openai.CodexBaseURL || opts.APIPath != "/responses" {
		t.Fatalf("options = %+v", opts)
	}
	// The offers page names each model with the request URL its plan sends to.
	if got := selector.multiOptions[0]; !reflect.DeepEqual(got, []string{"gpt-future", "gpt-second"}) {
		t.Fatalf("offer rows = %#v", got)
	}
	// It opens on the row the registry marks as the plan's default.
	if got := selector.multiDefaults[0]; !reflect.DeepEqual(got, []int{0}) {
		t.Fatalf("checked rows = %#v, want the plan's default", got)
	}
	if !strings.HasPrefix(selector.multiLabels[0], "ChatGPT\nChoose the models to add; each row shows where requests go.") {
		t.Fatalf("offers page title = %q", selector.multiLabels[0])
	}
}

// Both pages that list one batch of offers — the models to add, then the ones
// the agent may pick for subagents on its own — call the models what the vendor
// calls them, and fall back to the wire id only where the listing named none.
func TestOffersPageLabelsModelsByVendorName(t *testing.T) {
	sub := qoderCNLike()
	// The rows are login-backed: a provider nothing registered would need an API
	// key the page never asks for, and the write would fail on every retry.
	appcfg.ResetProviderPluginsForTest()
	t.Cleanup(appcfg.ResetProviderPluginsForTest)
	appcfg.RegisterProviderPlugin(appcfg.BuiltinProvider{
		ID: "qodercn", Label: "Qoder CN", LabelZH: "Qoder CN", LoginBacked: true,
		Endpoints: []appcfg.ProviderEndpoint{{BaseURL: "https://gateway.qoder.com.cn"}},
	})
	selector := &fakeSelector{multiSelects: [][]int{{0, 1}}}
	opts, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	want := []string{"Auto", "Qwen3.8-Max", "dmodel"}
	if got := selector.multiOptions[0]; !reflect.DeepEqual(got, want) {
		t.Fatalf("offer rows = %#v, want %#v", got, want)
	}
	if len(selector.multiOptions) < 2 {
		t.Fatalf("pages drawn = %d, want the models list and the subagent list", len(selector.multiOptions))
	}
	if got := selector.multiOptions[1]; !reflect.DeepEqual(got, []string{"Auto", "Qwen3.8-Max"}) {
		t.Fatalf("subagent rows = %#v, want the same names for the ticked models", got)
	}
	// What is written is still the wire id: a display name never becomes a model.
	if opts.Model != "auto" || len(sub.provisioned) != 1 || sub.provisioned[0][1].Model != "qmodel_38max" {
		t.Fatalf("options = %+v provisioned = %+v", opts, sub.provisioned)
	}
}

// A sign-in page whose answer the probe already gave is not a step: the flow is
// the list of sign-ins and the list of models, so Esc on the models goes back to
// the page that asks. Only a page that will ask for a sign-in stays in the flow.
func TestSubscriptionFlowDropsSettledSignInPage(t *testing.T) {
	threePages := []int{setupPageProvider, setupPageCredentials, setupPageOffers}
	for _, tc := range []struct {
		name  string
		check *turn.SignInCheck
		want  []int
	}{
		{name: "a login that lists its models", check: &turn.SignInCheck{State: turn.SignInUsable}, want: []int{setupPageProvider, setupPageOffers}},
		{name: "a login whose listing failed", check: &turn.SignInCheck{State: turn.SignInUnreachable}, want: []int{setupPageProvider, setupPageOffers}},
		{name: "no login", check: &turn.SignInCheck{State: turn.SignInMissing}, want: threePages},
		{name: "a dead login", check: &turn.SignInCheck{State: turn.SignInDead}, want: threePages},
		{name: "not probed yet", want: threePages},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub := qoderCNLike()
			withSubscriptions(t, sub)
			setup := newLLMSetup(PromptConfig{Selector: &fakeSelector{}, Output: io.Discard, Home: t.TempDir()})
			setup.setChoice(subscriptionChoice(sub))
			if tc.check != nil {
				setup.probes = map[string]turn.SignInCheck{sub.ID(): *tc.check}
			}
			if got := setup.flowPages(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("pages = %v, want %v", got, tc.want)
			}
		})
	}
}

// Esc on the list of models goes back to the list of sign-ins. It used to land
// on the sign-in page, which answers itself for a login this machine holds and
// steps straight forward again — the page never moved.
func TestEscOnOffersPageReturnsToProviderPage(t *testing.T) {
	sub := qoderCNLike()
	selector := &dismissingSelector{fakeSelector: &fakeSelector{}}
	opts, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub)
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if ok {
		t.Fatal("the setup saved itself after Esc on the model list")
	}
	if len(selector.multiLabels) != 1 {
		t.Fatalf("the model list was drawn %d times, want once", len(selector.multiLabels))
	}
	if len(selector.selectLabels) != 1 || !strings.HasPrefix(selector.selectLabels[0], "Connect a model") {
		t.Fatalf("the page Esc landed on = %#v, want the list of sign-ins", selector.selectLabels)
	}
	if sub.startCalls != 0 {
		t.Fatalf("sign-ins = %d, a login this machine holds must not sign in again", sub.startCalls)
	}
	if opts.Provider != "" {
		t.Fatalf("options = %+v, want none written", opts)
	}
}

// The sign-in row and the API-key group row are both called "Volcengine Ark".
// Coming back from the sign-in must land on the sign-in row, not on the later
// row that shares its label.
func TestProviderPageComesBackToTheSubscriptionRow(t *testing.T) {
	sub := volcengineLike()
	withSubscriptions(t, sub)
	selector := &indexSelector{fakeSelector: &fakeSelector{}}
	setup := newLLMSetup(PromptConfig{Selector: selector, Output: io.Discard, Home: t.TempDir()})

	subRow, keyRow := -1, -1
	for i, row := range setup.providerRows() {
		if row.choice.Subscription == sub.ID() {
			subRow = i
		}
		if row.group != nil && row.item.Label == sub.label {
			keyRow = i
		}
	}
	if subRow < 0 || keyRow < 0 || subRow == keyRow {
		t.Fatalf("the page no longer holds two Volcengine Ark rows: sign-in=%d key=%d", subRow, keyRow)
	}

	selector.answers = []int{subRow, -1}
	if _, err := setup.pickProvider(); err != nil {
		t.Fatalf("pick the sign-in row: %v", err)
	}
	if setup.choice.Subscription != sub.ID() {
		t.Fatalf("choice = %+v, want the %s sign-in", setup.choice, sub.ID())
	}
	if _, err := setup.pickProvider(); err != nil {
		t.Fatalf("come back: %v", err)
	}
	if len(selector.defaults) != 2 || selector.defaults[1] != subRow {
		t.Fatalf("the page reopened on row %v, want the sign-in row %d", selector.defaults, subRow)
	}
}

// The other direction: a key endpoint's row is the group row, and coming back
// must not be pulled onto the sign-in row by the shared label.
func TestProviderPageComesBackToTheAPIKeyRow(t *testing.T) {
	sub := volcengineLike()
	withSubscriptions(t, sub)
	selector := &indexSelector{fakeSelector: &fakeSelector{}}
	setup := newLLMSetup(PromptConfig{Selector: selector, Output: io.Discard, Home: t.TempDir()})

	keyRow := -1
	for i, row := range setup.providerRows() {
		if row.group != nil && row.item.Label == sub.label {
			keyRow = i
		}
	}
	if keyRow < 0 {
		t.Fatalf("no API-key group row for %s", sub.label)
	}

	selector.answers = []int{keyRow, 0, -1}
	if _, err := setup.pickProvider(); err != nil {
		t.Fatalf("pick the key group: %v", err)
	}
	if setup.choice.Provider != sub.ID() || setup.choice.Subscription != "" {
		t.Fatalf("choice = %+v, want a %s key endpoint", setup.choice, sub.ID())
	}
	if _, err := setup.pickProvider(); err != nil {
		t.Fatalf("come back: %v", err)
	}
	if len(selector.defaults) != 3 || selector.defaults[2] != keyRow {
		t.Fatalf("the page reopened on row %v, want the API-key group row %d", selector.defaults, keyRow)
	}
}

// A saved sign-in that can list its models IS the finished sign-in: no
// confirmation panel, no browser, and the offers page opens directly.
func TestConfigureSubscriptionReusesTheSavedSignIn(t *testing.T) {
	sub := chatGPTLike()
	sub.check = turn.SignInCheck{State: turn.SignInUsable}
	opened := browserMachine(t)
	selector := &fakeSelector{}
	opts, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if sub.startCalls != 0 {
		t.Fatalf("sign-ins = %d; a usable saved login must not sign in again", sub.startCalls)
	}
	if len(*opened) != 0 {
		t.Fatalf("opened %v, want no browser for a saved login", *opened)
	}
	if selector.confirmIdx != 0 {
		t.Fatalf("%d confirmation panels appeared, want none", selector.confirmIdx)
	}
	if opts.Model != "gpt-future" {
		t.Fatalf("model = %q, want the row the offers page opened on", opts.Model)
	}
}

// No login on this machine means the flow runs: the confirmation first, then the
// browser.
func TestConfigureSubscriptionMissingLoginRunsTheFlow(t *testing.T) {
	sub := chatGPTLike()
	opened := browserMachine(t)
	selector := &fakeSelector{confirms: []bool{true}}
	_, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if sub.startCalls != 1 || len(*opened) != 1 {
		t.Fatalf("starts = %d opened %v, want exactly one of each", sub.startCalls, *opened)
	}
	if !strings.Contains(selector.confirmLabels[0], "Sign in with ChatGPT") ||
		!strings.Contains(selector.confirmLabels[0], "opens your browser to sign in") {
		t.Fatalf("sign-in prompt = %q", selector.confirmLabels[0])
	}
}

// Declining the confirmation never starts a sign-in.
func TestConfigureSubscriptionDeclinedNeverSignsIn(t *testing.T) {
	sub := chatGPTLike()
	opened := browserMachine(t)
	selector := &fakeSelector{confirms: []bool{false}}
	_, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub)
	if err != nil {
		t.Fatalf("configure err=%v", err)
	}
	if ok {
		t.Fatal("the setup saved after the sign-in was declined")
	}
	if sub.startCalls != 0 || len(*opened) != 0 {
		t.Fatalf("starts = %d opened %v, want nothing started", sub.startCalls, *opened)
	}
}

// A dead login runs the flow too: only a new sign-in restores what the refresh
// token lost.
func TestConfigureSubscriptionDeadLoginRunsTheFlow(t *testing.T) {
	sub := chatGPTLike()
	sub.check = turn.SignInCheck{State: turn.SignInDead, Err: errors.New("ChatGPT login expired; sign in again")}
	browserMachine(t)
	selector := &fakeSelector{confirms: []bool{true}}
	if _, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub); err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if sub.startCalls != 1 {
		t.Fatalf("sign-ins = %d, want the flow to run", sub.startCalls)
	}
}

// A listing that failed never pushes a working login back through a browser: the
// retry reuses the saved login, and declining it aborts with an actionable error
// and nothing written.
func TestConfigureSubscriptionUnreachableKeepsSavedLogin(t *testing.T) {
	sub := chatGPTLike()
	sub.check = turn.SignInCheck{State: turn.SignInUnreachable, Err: errors.New("resolve client version: registry unreachable")}
	sub.offersFn = func(int) ([]subscription.Offer, error) {
		return nil, errors.New("fetch ChatGPT models: connection refused")
	}
	opened := browserMachine(t)
	selector := &fakeSelector{confirms: []bool{false}, failUnexpectedSelect: true}
	opts, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub)
	if err == nil || ok {
		t.Fatalf("err=%v ok=%t, want the declined retry to abort", err, ok)
	}
	if !strings.Contains(err.Error(), "could not be fetched") {
		t.Fatalf("err = %v, want the actionable fetch failure", err)
	}
	if sub.startCalls != 0 || len(*opened) != 0 {
		t.Fatalf("starts = %d opened %v, an unreachable listing must not sign in", sub.startCalls, *opened)
	}
	if selector.confirmIdx != 1 {
		t.Fatalf("confirmations = %d, want only the retry panel", selector.confirmIdx)
	}
	if opts.Provider != "" {
		t.Fatalf("options = %+v, want none written", opts)
	}
	if len(sub.provisioned) != 0 {
		t.Fatalf("provisioned %+v, want nothing written", sub.provisioned)
	}
}

// Accepting the retry refetches on the saved login — no second sign-in — and the
// flow then continues with the fresh listing.
func TestConfigureSubscriptionFetchRetrySkipsRelogin(t *testing.T) {
	sub := chatGPTLike()
	first := chatGPTLike()
	sub.offersFn = func(call int) ([]subscription.Offer, error) {
		if call == 1 {
			return nil, errors.New("fetch ChatGPT models: http 429: slow down")
		}
		return first.offers, nil
	}
	browserMachine(t)
	selector := &fakeSelector{confirms: []bool{true, true}}
	opts, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if sub.startCalls != 1 {
		t.Fatalf("sign-ins = %d, want the retry to reuse the saved login", sub.startCalls)
	}
	if sub.offerCalls != 2 {
		t.Fatalf("listing calls = %d, want exactly one retry", sub.offerCalls)
	}
	if opts.Model != "gpt-future" {
		t.Fatalf("model = %q", opts.Model)
	}
}

// A terminal with no browser of its own cannot finish a loopback callback, so it
// takes the flow that pastes the address back, opens nothing, and hands the
// address to the flow.
func TestConfigureSubscriptionPastesTheCallbackWhenHeadless(t *testing.T) {
	sub := chatGPTLike()
	opened := headlessMachine(t)
	const pasted = "http://127.0.0.1:54623/auth/callback?code=abc"
	selector := &fakeSelector{confirms: []bool{true}, inputs: []string{pasted}}
	_, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if sub.startedFlow != subscription.FlowPasteCallback {
		t.Fatalf("flow = %q, want the paste flow on a headless machine", sub.startedFlow)
	}
	if len(*opened) != 0 {
		t.Fatalf("opened %v, want no browser on a headless machine", *opened)
	}
	if len(sub.login.submitted) != 1 || sub.login.submitted[0] != pasted {
		t.Fatalf("pasted %v, want the address handed to the flow", sub.login.submitted)
	}
}

// A rejected paste leaves the sign-in running: the person who pasted it tries
// again.
func TestConfigureSubscriptionRejectedPasteTriesAgain(t *testing.T) {
	sub := chatGPTLike()
	headlessMachine(t)
	sub.login.submitErr = errors.New("that address did not belong to this sign-in")
	selector := &fakeSelector{confirms: []bool{true}, inputs: []string{"http://localhost/wrong", "http://127.0.0.1:54623/auth/callback?code=abc"}}
	if _, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub); err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if len(sub.login.submitted) != 2 {
		t.Fatalf("submitted %v, want the rejected address and the corrected one", sub.login.submitted)
	}
}

// The device code is the whole page: the address to open and the code to confirm,
// and on a machine with a browser the flow opens that address — a device flow is
// no exception, and Volcengine's page carries the code already filled in.
func TestConfigureSubscriptionDeviceFlowShowsTheCode(t *testing.T) {
	sub := openCodeLike()
	opened := browserMachine(t)
	out := &bytes.Buffer{}
	cfg := setupTestConfig(t, &fakeSelector{confirms: []bool{true}})
	cfg.Output = out
	opts, ok, err := runSubscriptionSetup(t, cfg, sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if sub.startedFlow != subscription.FlowDeviceCode {
		t.Fatalf("flow = %q", sub.startedFlow)
	}
	if len(*opened) != 1 || (*opened)[0] != "https://opencode.ai/auth" {
		t.Fatalf("opened %v, want the address the page shows", *opened)
	}
	for _, want := range []string{
		"Open https://opencode.ai/auth and enter this code.",
		"QK7M-2PLX",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("sign-in page = %q, want it to carry %q", out.String(), want)
		}
	}
	if opts.Provider != "opencode-go" || opts.BaseURL != opencode.ProviderBaseURL("opencode-go") || opts.APIKey != "" {
		t.Fatalf("options = %+v", opts)
	}
}

// A plan whose key nobody can fetch asks for it on a masked page — naming the
// console it is copied from — and writes the rows only once it has one: the key
// reaches the write and is never repeated on the page (plan 281).
func TestConfigureSubscriptionAsksForTheKeyAPlanCannotFetch(t *testing.T) {
	sub := volcengineLike()
	const pasted = "ark-pasted-placeholder-key"
	out := &bytes.Buffer{}
	selector := &fakeSelector{confirms: []bool{true}, inputs: []string{pasted}}
	cfg := setupTestConfig(t, selector)
	cfg.Output = out
	opts, ok, err := runSubscriptionSetup(t, cfg, sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if sub.startedFlow != subscription.FlowDeviceCode {
		t.Fatalf("flow = %q, want the console's device code", sub.startedFlow)
	}
	if len(sub.provisioned) != 2 {
		t.Fatalf("writes = %d, want the refused one and the retry", len(sub.provisioned))
	}
	if sub.options[0].Key != "" {
		t.Fatalf("first write carried key %q, want none", sub.options[0].Key)
	}
	if sub.options[1].Key != pasted {
		t.Fatalf("retry carried key %q, want the pasted one", sub.options[1].Key)
	}
	labels := strings.Join(selector.inputLabels, "\n")
	if !strings.Contains(labels, "Paste the Coding Plan API key") {
		t.Fatalf("key page = %q, want it to name the plan", labels)
	}
	if !strings.Contains(labels, "https://console.volcengine.com/ark") {
		t.Fatalf("key page = %q, want the console it is copied from", labels)
	}
	if strings.Contains(out.String(), pasted) {
		t.Fatalf("output = %q, must never repeat the key", out.String())
	}
	if opts.Provider != "volcengine" {
		t.Fatalf("options = %+v", opts)
	}
}

// The wizard's answers are saved by the call the surface makes next: the rows
// are already written with the pasted key, and the options carry none of their
// own. Asking the save for a key the wizard never had refused a completed
// sign-in after every row had been stored (Volcengine's Coding Plan), so the
// whole ride — wizard, write, save — is one test.
func TestVolcengineSubscriptionSetupSavesThroughExecute(t *testing.T) {
	t.Cleanup(process.ResetResolve)
	// The process installs the registry's plan tables when it adopts a
	// configuration at startup; this test binary runs no startup, and the
	// catalog it installs is models.json, which has no volcengine rows. The
	// save's provider check reads that layer, so it is installed here.
	llm.SetBuiltinModels("volcengine", []llm.Model{{ID: "doubao-seed-2.1-pro"}})
	t.Cleanup(func() { llm.SetBuiltinModels("volcengine", nil) })
	sub := volcengineLike()
	const pasted = "ark-pasted-placeholder-key"
	selector := &fakeSelector{confirms: []bool{true}, inputs: []string{pasted}}
	cfg := setupTestConfig(t, selector)
	opts, ok, err := runSubscriptionSetup(t, cfg, sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	// What runConnect calls the moment the wizard returns.
	rt := process.Context{Home: cfg.Home, ConfigPath: cfg.ConfigPath, Config: appcfg.Root{}}
	rep, err := process.Execute(rt, opts)
	if err != nil {
		t.Fatalf("Execute after the wizard: %v", err)
	}
	if rep.Provider != "volcengine" || rep.ModelID == "" {
		t.Fatalf("report = %+v, want the model the sign-in unlocked", rep)
	}
	// The save keeps the key the write stored instead of demanding another.
	db, err := process.StateDB(rt)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer db.Close()
	row, err := (&state.LLMModelStore{DB: db}).Get(context.Background(), rep.ModelID)
	if err != nil {
		t.Fatalf("read the saved row: %v", err)
	}
	if row.APIKey != pasted {
		t.Fatalf("row key = %q, want the pasted one kept", row.APIKey)
	}
}

// Every sign-in call reads the same environment the write does, database
// included: a subscription's credential is a database row, so a probe without it
// would answer "not signed in" on a machine that is.
func TestSignInEnvCarriesTheStateDatabase(t *testing.T) {
	setup := newLLMSetup(setupTestConfig(t, &fakeSelector{}))
	t.Cleanup(setup.closeState)
	env, err := setup.subscriptionEnv()
	if err != nil {
		t.Fatalf("subscriptionEnv: %v", err)
	}
	if env.DB == nil || env.Home == "" {
		t.Fatalf("subscriptionEnv = %+v, want a home and a database", env)
	}
	signIn := setup.signInEnv()
	if signIn.DB != env.DB || signIn.Home != env.Home {
		t.Fatalf("signInEnv = %+v, want the write's own environment", signIn)
	}
}

// A sign-in that unlocks no model this build can run says so instead of saving
// nothing silently.
func TestConfigureSubscriptionWithoutModelsSaysSo(t *testing.T) {
	sub := chatGPTLike()
	sub.offers = nil
	browserMachine(t)
	_, ok, err := runSubscriptionSetup(t, setupTestConfig(t, &fakeSelector{confirms: []bool{true}}), sub)
	if err == nil || ok {
		t.Fatalf("err=%v ok=%t, want the empty listing refused", err, ok)
	}
	if !strings.Contains(err.Error(), "no models this build can run") {
		t.Fatalf("err = %v", err)
	}
}

// A refused write shows the registry's own sentence on the page and writes
// nothing; the user can then choose again.
func TestConfigureSubscriptionProvisionFailureKeepsThePage(t *testing.T) {
	sub := chatGPTLike()
	sub.provisionFn = func(call int) error {
		if call == 1 {
			return subscription.ErrNoKey
		}
		return nil
	}
	browserMachine(t)
	out := &bytes.Buffer{}
	// Two check lists per attempt now: the models to add, then which of them the
	// agent may pick for subagents (283). The first attempt ticks row 0, the
	// second row 1; neither opens the subagent switch.
	cfg := setupTestConfig(t, &fakeSelector{confirms: []bool{true}, multiSelects: [][]int{{0}, {}, {1}, {}}})
	cfg.Output = out
	opts, ok, err := runSubscriptionSetup(t, cfg, sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if len(sub.provisioned) != 2 {
		t.Fatalf("provision calls = %d, want the second attempt after the refusal", len(sub.provisioned))
	}
	if !strings.Contains(out.String(), subscription.ErrNoKey.Error()) {
		t.Fatalf("page = %q, want the registry's sentence", out.String())
	}
	if opts.Model != "gpt-second" {
		t.Fatalf("model = %q, want the row chosen on the second attempt", opts.Model)
	}
}

// connectProgressSelector is a fakeSelector whose ConnectProgress delegates to a
// real rawSelector wired to a renderer, so a test can prove both that the line is
// installed through the production path and that it is taken off the screen at
// the end.
type connectProgressSelector struct {
	*fakeSelector
	raw *rawSelector

	installs int
	stage    func() string
	ended    bool
}

func (s *connectProgressSelector) ConnectProgress(stage func() string) func() {
	s.installs++
	s.stage = stage
	rawEnd := s.raw.ConnectProgress(stage)
	return func() {
		s.ended = true
		rawEnd()
	}
}

// The sign-in installs one live line, and it is gone once the flow returns —
// including its renderer-side source, so no orphaned row keeps counting.
func TestSignInShowsALiveProgressLine(t *testing.T) {
	sub := openCodeLike()
	browserMachine(t)
	renderer := NewRenderer(nil, nil)
	sel := &connectProgressSelector{
		fakeSelector: &fakeSelector{confirms: []bool{true}},
		raw:          newRawSelector(nil, renderer),
	}
	cfg := setupTestConfig(t, sel)
	if _, ok, err := runSubscriptionSetup(t, cfg, sub); err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if sel.installs != 1 {
		t.Fatalf("ConnectProgress installs = %d, want exactly one line for the whole sign-in", sel.installs)
	}
	if text := sel.stage(); !strings.Contains(text, "Waiting for you to confirm the code in OpenCode") {
		t.Fatalf("live line = %q", text)
	}
	if !sel.ended {
		t.Fatal("the live line was never ended after the sign-in")
	}
	if renderer.TransientStatusSourceLive(transientSourceConnect) {
		t.Fatal("the connect source outlived its line: the row would keep counting on screen")
	}
}

// stubLLMProbe replaces the connection test: each call answers the next scripted
// error, and a call past the script succeeds.
func stubLLMProbe(t *testing.T, errs ...error) *int {
	t.Helper()
	calls := 0
	previous := probeLLMModel
	probeLLMModel = func(context.Context, process.LLMModelInput) error {
		calls++
		if calls <= len(errs) {
			return errs[calls-1]
		}
		return nil
	}
	t.Cleanup(func() { probeLLMModel = previous })
	return &calls
}

// customAnswers is one pass through the custom form: the address, the protocol,
// the key and the model name.
type customAnswers struct {
	baseURL         string
	protocol        string
	key             string
	model           string
	catalogProvider string
}

// The custom form asks for the address, the protocol, the key and the model name,
// and saves the model it describes: provider custom, the protocol's own path, and
// the name sent verbatim.
func TestCustomModelFlowSavesTheProviderItDescribes(t *testing.T) {
	useSetupTestCatalog(t)
	probes := stubLLMProbe(t)
	selector := &fakeSelector{confirms: []bool{false}, multiSelects: [][]int{{2, 3}}}
	cfg := setupTestConfig(t, selector)
	opts, ok, err := PromptMainLLMSetup(withCustomAnswers(cfg, customAnswers{
		baseURL:  "https://api.example.com/v1",
		protocol: "OpenAI Chat Completions",
		key:      "sk-custom",
		model:    "gpt-4o-vendor",
	}))
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if *probes != 0 {
		t.Fatalf("probes = %d, want the declined test to send nothing", *probes)
	}
	if opts.Provider != appcfg.CustomProvider || opts.Model != "gpt-4o-vendor" || opts.APIKey != "sk-custom" {
		t.Fatalf("options = %+v", opts)
	}
	if opts.BaseURL != "https://api.example.com/v1" || opts.APIPath != appcfg.APIPathChatCompletions || opts.URLVerbatim {
		t.Fatalf("endpoint = %+v", opts)
	}
	if !reflect.DeepEqual(opts.CapEfforts, []string{"medium", "high"}) {
		t.Fatalf("levels = %#v (pages: %v)", opts.CapEfforts, selector.multiLabels)
	}
	// The key page names where the requests go before the key is typed, and says
	// only the fact that matters here.
	keyPage := selector.inputLabels[1]
	if !strings.Contains(keyPage, "Requests go to https://api.example.com/v1/chat/completions") ||
		!strings.Contains(keyPage, "Saved on this machine only.") {
		t.Fatalf("key page = %q", keyPage)
	}
	// The review lists what the row will be, provider and protocol included.
	facts := map[string]string{}
	for _, fact := range selector.reviewFacts[0] {
		facts[fact.Label] = fact.Value
	}
	want := map[string]string{
		"Provider": appcfg.CustomProvider,
		"Model":    "gpt-4o-vendor",
		"Protocol": "OpenAI Chat Completions",
		"Endpoint": "https://api.example.com/v1/chat/completions",
		"Thinking": "medium, high",
	}
	for label, value := range want {
		if facts[label] != value {
			t.Fatalf("review %s = %q, want %q (all: %+v)", label, facts[label], value, facts)
		}
	}
	if !strings.HasPrefix(selector.reviewTitles[0], "Review\nCheck the details, then save.") {
		t.Fatalf("review title = %q", selector.reviewTitles[0])
	}
}

// 「自定义协议」 sends the request to the address as stored: the path only names
// the wire protocol, and the key page shows the address alone.
func TestCustomModelCustomProtocolSendsToTheURLAsIs(t *testing.T) {
	stubLLMProbe(t)
	selector := &fakeSelector{confirms: []bool{false}, multiSelects: [][]int{{}}}
	cfg := setupTestConfig(t, selector)
	opts, ok, err := PromptMainLLMSetup(withCustomAnswers(cfg, customAnswers{
		baseURL:  "http://127.0.0.1:8731/v1",
		protocol: "Custom protocol",
		key:      "sk-local",
		model:    "qwen3-coder",
	}))
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if !opts.URLVerbatim || opts.APIPath != appcfg.APIPathChatCompletions {
		t.Fatalf("options = %+v, want the verbatim protocol", opts)
	}
	if got := selector.inputLabels[1]; !strings.Contains(got, "Requests go to http://127.0.0.1:8731/v1\n") {
		t.Fatalf("key page = %q, want the address alone", got)
	}
	facts := map[string]string{}
	for _, fact := range selector.reviewFacts[0] {
		facts[fact.Label] = fact.Value
	}
	if facts["Endpoint"] != "http://127.0.0.1:8731/v1" || facts["Protocol"] != "Custom protocol" {
		t.Fatalf("review = %+v", facts)
	}
	if facts["Thinking"] != "Not set" {
		t.Fatalf("review thinking = %q, want Not set", facts["Thinking"])
	}
}

// A model a source already documents carries its own levels, so the form never
// asks for them.
func TestCustomModelKnownLevelsSkipTheQuestion(t *testing.T) {
	useSetupTestCatalog(t)
	stubLLMProbe(t)
	selector := &fakeSelector{confirms: []bool{false}}
	cfg := setupTestConfig(t, selector)
	opts, ok, err := PromptMainLLMSetup(withCustomAnswers(cfg, customAnswers{
		baseURL:  "https://api.openai.com/v1",
		protocol: "OpenAI Chat Completions",
		key:      "sk-known",
		model:    "openai/gpt-5.5",
	}))
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if len(selector.multiLabels) != 0 {
		t.Fatalf("multi-select pages = %v, want no thinking-levels page", selector.multiLabels)
	}
	if len(opts.CapEfforts) != 0 {
		t.Fatalf("levels = %#v, want none written for a documented model", opts.CapEfforts)
	}
}

// useSetupCatalogWith installs one test catalog and puts the build's own back
// when the test ends, so a test can name the entries its case needs.
func useSetupCatalogWith(t *testing.T, raw string) {
	t.Helper()
	cat, err := llm.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse test catalog: %v", err)
	}
	llm.SetGlobalCatalogForTest(cat)
	if data, err := llm.RepoModelsJSONForTest(); err == nil {
		if embedded, err := llm.Parse(data); err == nil {
			t.Cleanup(func() { llm.SetGlobalCatalogForTest(embedded) })
		}
	}
}

// The custom form asks whose official data describes the model after the model
// name (owner 2026-11-16). The answer is optional, is saved on the row, and the
// review shows what it matched before anything is written.
func TestCustomModelFlowAsksForAnOptionalProvider(t *testing.T) {
	useSetupCatalogWith(t, `{
		"alibaba/qwen4-max-preview": {"id":"alibaba/qwen4-max-preview","reasoning":true,
			"reasoning_options":[{"type":"effort","values":["low","high"]}],
			"limit":{"context":1000000,"output":262144}}
	}`)
	stubLLMProbe(t)

	// Empty answer: the page is there, and the row names no provider.
	blank := &fakeSelector{confirms: []bool{false}, multiSelects: [][]int{{}}}
	opts, ok, err := PromptMainLLMSetup(withCustomAnswers(setupTestConfig(t, blank), customAnswers{
		baseURL:  "https://api.example.com/v1",
		protocol: "OpenAI Chat Completions",
		key:      "sk-blank",
		model:    "qwen4-max-preview",
	}))
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if opts.CatalogProvider != "" {
		t.Fatalf("catalog provider = %q, want empty", opts.CatalogProvider)
	}
	if len(blank.inputLabels) != 4 {
		t.Fatalf("input pages = %v, want the address, the key, the model and the provider", blank.inputLabels)
	}
	if !strings.HasPrefix(blank.inputLabels[3], "Model provider (optional)") {
		t.Fatalf("fourth input page = %q, want the optional provider", blank.inputLabels[3])
	}
	if !strings.Contains(blank.inputLabels[3], "Leave empty to match the model name alone.") {
		t.Fatalf("provider page = %q", blank.inputLabels[3])
	}

	// A typed provider is saved as typed, and the review names the entry the
	// catalog matched.
	typed := &fakeSelector{confirms: []bool{false}, multiSelects: [][]int{{}}}
	opts, ok, err = PromptMainLLMSetup(withCustomAnswers(setupTestConfig(t, typed), customAnswers{
		baseURL:         "https://api.example.com/v1",
		protocol:        "OpenAI Chat Completions",
		key:             "sk-typed",
		model:           "qwen4-max-preview",
		catalogProvider: "alibaba",
	}))
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if opts.CatalogProvider != "alibaba" {
		t.Fatalf("catalog provider = %q, want alibaba", opts.CatalogProvider)
	}
	facts := map[string]string{}
	for _, fact := range typed.reviewFacts[0] {
		facts[fact.Label] = fact.Value
	}
	if got := facts["Model data"]; got != "alibaba/qwen4-max-preview 1M window" {
		t.Fatalf("review Model data = %q, want the matched entry and its window (all: %+v)", got, facts)
	}
}

// A bare custom model a catalog entry names by its last segment carries that
// entry's levels, so the form does not ask for evidence it already has — even
// when the person left the optional provider empty.
func TestCustomModelFlowSkipsTheLevelsPageWhenTheCatalogKnowsTheModel(t *testing.T) {
	useSetupCatalogWith(t, `{
		"alibaba/qwen4-max-preview": {"id":"alibaba/qwen4-max-preview","reasoning":true,
			"reasoning_options":[{"type":"effort","values":["low","high"]}],
			"limit":{"context":1000000,"output":262144}}
	}`)
	stubLLMProbe(t)
	selector := &fakeSelector{confirms: []bool{false}}
	if _, ok, err := PromptMainLLMSetup(withCustomAnswers(setupTestConfig(t, selector), customAnswers{
		baseURL:  "https://api.example.com/v1",
		protocol: "OpenAI Chat Completions",
		key:      "sk-known",
		model:    "qwen4-max-preview",
	})); err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if len(selector.multiLabels) != 0 {
		t.Fatalf("multi-select pages = %v, want no thinking-levels page", selector.multiLabels)
	}
}

// withCustomAnswers scripts a custom run's answers onto a config, from the
// /connect first page: the provider list's «Custom model» row, then the
// protocol, the address, the key, the model name, and the optional provider
// whose official data describes it (an unanswered page takes "").
func withCustomAnswers(cfg PromptConfig, answers customAnswers) PromptConfig {
	selector := cfg.Selector.(*fakeSelector)
	selector.inputs = append([]string{answers.baseURL, answers.key, answers.model, answers.catalogProvider}, selector.inputs...)
	selector.selects = append([]string{"Custom model", answers.protocol}, selector.selects...)
	return cfg
}

// The connection test sends one real request before anything is saved: a failure
// says so in a short sentence, saves nothing, and goes back a page so the answer
// can be fixed.
func TestCustomModelTestConnectionFailureSavesNothing(t *testing.T) {
	stubLLMProbe(t, errors.New("the API key was refused"))
	out := &bytes.Buffer{}
	cfg := setupTestConfig(t, &fakeSelector{multiSelects: [][]int{{}}})
	cfg.Output = out
	opts, ok, err := PromptMainLLMSetup(withCustomAnswers(cfg, customAnswers{
		baseURL:  "https://api.example.com/v1",
		protocol: "OpenAI Chat Completions",
		key:      "sk-wrong",
		model:    "gpt-4o",
	}))
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	for _, want := range []string{"Connection failed", "the API key was refused", "Nothing was saved."} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("report = %q, want it to carry %q", out.String(), want)
		}
	}
	if opts.Model != "gpt-4o" {
		t.Fatalf("options = %+v", opts)
	}
}

// A connection that answers says how long it took, once, and then the review
// follows.
func TestCustomModelTestConnectionSuccess(t *testing.T) {
	probes := stubLLMProbe(t)
	out := &bytes.Buffer{}
	cfg := setupTestConfig(t, &fakeSelector{multiSelects: [][]int{{}}})
	cfg.Output = out
	if _, ok, err := PromptMainLLMSetup(withCustomAnswers(cfg, customAnswers{
		baseURL:  "https://api.example.com/v1",
		protocol: "Anthropic Messages",
		key:      "sk-good",
		model:    "claude-sonnet-4-6",
	})); err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if *probes != 1 {
		t.Fatalf("probes = %d, want one", *probes)
	}
	if !strings.Contains(out.String(), "claude-sonnet-4-6 answered in") {
		t.Fatalf("report = %q, want the answering model and its time", out.String())
	}
}

// Saving an identity this install already holds asks before replacing its key —
// and answering no writes nothing and comes back to the review.
func TestSaveAsksBeforeReplacingAKey(t *testing.T) {
	cfg := setupTestConfig(t, &fakeSelector{
		confirms:     []bool{false, false, true}, // no test, decline, replace
		multiSelects: [][]int{{}},
		reviews:      []string{"Save", "Save"},
	})
	db, err := process.StateDB(process.Context{Home: cfg.Home, ConfigPath: cfg.ConfigPath})
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	store := &state.LLMModelStore{DB: db}
	existing, err := store.Create(context.Background(), state.LLMModel{
		Provider: appcfg.CustomProvider, Model: "gpt-4o", BaseURL: "https://api.example.com/v1",
		APIPath: appcfg.APIPathChatCompletions, APIKey: "sk-old",
	})
	if err != nil {
		t.Fatalf("seed the row: %v", err)
	}
	_ = db.Close()

	_, ok, err := PromptMainLLMSetup(withCustomAnswers(cfg, customAnswers{
		baseURL:  "https://api.example.com/v1",
		protocol: "OpenAI Chat Completions",
		key:      "sk-new",
		model:    "gpt-4o",
	}))
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	selector := cfg.Selector.(*fakeSelector)
	if len(selector.confirmLabels) != 3 {
		t.Fatalf("confirmations = %v, want the test, the decline and the replacement", selector.confirmLabels)
	}
	if !strings.Contains(selector.confirmLabels[1], "already configured") ||
		!strings.Contains(selector.confirmLabels[1], "Replace its key?") {
		t.Fatalf("duplicate prompt = %q", selector.confirmLabels[1])
	}
	if len(selector.reviewFacts) != 2 {
		t.Fatalf("review pages = %d, want the page shown again after the decline", len(selector.reviewFacts))
	}

	// The row the install held was never touched by the setup: the write is the
	// caller's, and it only happens after the yes.
	db, err = process.StateDB(process.Context{Home: cfg.Home, ConfigPath: cfg.ConfigPath})
	if err != nil {
		t.Fatalf("reopen state database: %v", err)
	}
	defer db.Close()
	row, found, err := (&state.LLMModelStore{DB: db}).FindByIdentity(context.Background(),
		appcfg.CustomProvider, "gpt-4o", "https://api.example.com/v1", appcfg.APIPathChatCompletions)
	if err != nil || !found {
		t.Fatalf("find the row: found=%t err=%v", found, err)
	}
	if row.ID != existing.ID || row.APIKey != "sk-old" {
		t.Fatalf("row = %+v, want the untouched one %q", row, existing.ID)
	}
}

// Other providers list their models from the shared catalog, with the typed ID
// appended by the prompt itself.
func TestCatalogModelOptionsComeFromTheSharedCatalog(t *testing.T) {
	useSetupTestCatalog(t)
	options := catalogModelOptions("anthropic")
	if len(options) != 2 {
		t.Fatalf("options = %#v", options)
	}
	if options[0].Label != "claude-haiku-lite" || options[0].Value != "claude-haiku-lite" {
		t.Fatalf("first option = %+v, want the exact id, id-sorted", options[0])
	}
	for _, option := range options {
		if option.Label == manualModelInput {
			t.Fatal("manual input belongs to the prompt, not the catalog list")
		}
	}
}

// Every model the catalog holds for the provider is listed: the picker's filter
// finds one among them, so nothing is left out to keep the list short.
func TestCatalogModelOptionsListTheWholeCatalog(t *testing.T) {
	raw := map[string]any{}
	for i := 0; i < 45; i++ {
		raw[fmt.Sprintf("openai/gpt-test-%02d", i)] = map[string]any{
			"name": fmt.Sprintf("GPT Test %02d", i), "reasoning": true,
			"limit": map[string]any{"context": 1000, "output": 100},
		}
	}
	blob, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := llm.Parse(blob)
	if err != nil {
		t.Fatal(err)
	}
	llm.SetGlobalCatalogForTest(cat)
	if data, err := llm.RepoModelsJSONForTest(); err == nil {
		if embedded, err := llm.Parse(data); err == nil {
			t.Cleanup(func() { llm.SetGlobalCatalogForTest(embedded) })
		}
	}
	if got := len(catalogModelOptions("openai")); got != 45 {
		t.Fatalf("options = %d, want all 45", got)
	}
}

// installRepoCatalog points the shared catalog at the repository's own
// assets/models.json: the effort page a configured provider gets must be the
// one /model and the gateway read, not a fixture's idea of it.
func installRepoCatalog(t *testing.T) {
	t.Helper()
	data, err := llm.RepoModelsJSONForTest()
	if err != nil {
		t.Fatalf("read repo models.json: %v", err)
	}
	cat, err := llm.Parse(data)
	if err != nil {
		t.Fatalf("parse repo models.json: %v", err)
	}
	llm.SetGlobalCatalogForTest(cat)
	t.Cleanup(func() { llm.SetGlobalCatalogForTest(cat) })
}

// TestConfigureEffortPageUsesTheModelsLevels pins onboarding and /connect to
// the same levels /model offers: the model's own catalog row, plus the
// "Provider default" escape hatch — and no page at all for a model whose row
// names no discrete level.
func TestConfigureEffortPageUsesTheModelsLevels(t *testing.T) {
	installRepoCatalog(t)
	for _, tc := range []struct {
		provider    string
		model       string
		want        []string
		wantDefault string
		selects     []string
		wantJSON    string
	}{
		{
			provider: "openai", model: "gpt-5",
			want:        []string{"minimal", "low", "medium", "high", reasoningEffortUnset},
			wantDefault: reasoningEffortProviderDefault,
			selects:     []string{"medium"},
			wantJSON:    `{"reasoning":{"effort":"medium"}}`,
		},
		{
			// The page opens on "medium" only when the model offers it;
			// otherwise it opens on the provider's own default.
			provider: "zhipuai", model: "glm-5.3-flash",
			want:        []string{"low", "high", "max", reasoningEffortUnset},
			wantDefault: reasoningEffortUnset,
			selects:     []string{"max"},
			wantJSON:    `{"reasoning":{"effort":"max"}}`,
		},
		{
			// A toggle-only model: nothing to choose, so no page and no level.
			provider: "zhipuai", model: "glm-4.5",
		},
		{
			// Not in the catalog at all: nothing to choose either.
			provider: "openai", model: "not-a-model",
		},
	} {
		t.Run(tc.provider+"/"+tc.model, func(t *testing.T) {
			selector := &fakeSelector{selects: tc.selects}
			got, ok, err := promptReasoningEffort(PromptConfig{Selector: selector}, providerChoice{Provider: tc.provider}, tc.model, "")
			if err != nil || !ok {
				t.Fatalf("promptReasoningEffort ok=%t err=%v", ok, err)
			}
			if tc.want == nil {
				if len(selector.selectOptions) != 0 {
					t.Fatalf("selection pages = %#v, want no effort page", selector.selectOptions)
				}
				if got != "" {
					t.Fatalf("params = %q, want no pinned effort", got)
				}
				return
			}
			if len(selector.selectOptions) != 1 {
				t.Fatalf("selection pages = %#v, want one effort page", selector.selectOptions)
			}
			if !reflect.DeepEqual(selector.selectOptions[0], tc.want) {
				t.Fatalf("effort options = %#v, want %#v", selector.selectOptions[0], tc.want)
			}
			if selector.selectDefaults[0] != tc.wantDefault {
				t.Fatalf("effort default = %q, want %q", selector.selectDefaults[0], tc.wantDefault)
			}
			if got != tc.wantJSON {
				t.Fatalf("params = %q, want %q", got, tc.wantJSON)
			}
		})
	}
}

// providerGroupsBeforeTheRegistry is the API-key half of the list this screen
// offered while it kept its own copy of every provider, endpoint and label. It
// is the expectation for the generated list: generating must change nothing a
// person already sees, because the list is where they recognize their provider.
// Only the four providers owner added on 2026-11-07 may appear beyond it. The two
// sign-ins are not here: they are pkg/subscription's, and the first page lists
// them under their own group.
var providerGroupsBeforeTheRegistry = []providerGroup{
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
				Label:           "Standard China",
				Hint:            "dashscope.aliyuncs.com",
				Provider:        "alibaba",
				DefaultBaseURL:  "https://dashscope.aliyuncs.com/compatible-mode/v1",
				SupportsAPIPath: true,
				APIKeyLabel:     "Qwen API key",
			},
			{
				Group:           "Qwen Cloud",
				Label:           "Standard Global",
				Hint:            "dashscope-intl.aliyuncs.com",
				Provider:        "alibaba",
				DefaultBaseURL:  "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
				SupportsAPIPath: true,
				APIKeyLabel:     "Qwen API key",
			},
			{
				Group:           "Qwen Cloud",
				Label:           "Coding Plan China",
				Hint:            "coding.dashscope.aliyuncs.com",
				Provider:        "alibaba",
				DefaultBaseURL:  "https://coding.dashscope.aliyuncs.com/v1",
				SupportsAPIPath: true,
				APIKeyLabel:     "Qwen API key",
			},
			{
				Group:           "Qwen Cloud",
				Label:           "Coding Plan Global",
				Hint:            "coding-intl.dashscope.aliyuncs.com",
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
			DefaultBaseURL:  "https://api.minimax.io/anthropic",
			SupportsAPIPath: false,
			APIKeyLabel:     "MiniMax API key",
		}, {
			Group:           "MiniMax",
			Label:           "MiniMax API key (CN)",
			Hint:            "Endpoint: api.minimaxi.com",
			Provider:        "minimax",
			DefaultBaseURL:  "https://api.minimaxi.com/anthropic",
			SupportsAPIPath: false,
			APIKeyLabel:     "MiniMax API key",
		}},
	},
}

// TestProviderGroupsGeneratedFromTheRegistryKeepTheOldList is the regression
// guard for the generated provider list: every group and choice a person sees
// today must come out identical, field by field. The only additions allowed are
// the providers owner added on 2026-11-07.
func TestProviderGroupsGeneratedFromTheRegistryKeepTheOldList(t *testing.T) {
	old := providerGroupsBeforeTheRegistry
	if len(providerGroups) < len(old) {
		t.Fatalf("generated %d groups, the old list had %d", len(providerGroups), len(old))
	}
	for i, want := range old {
		got := providerGroups[i]
		if got.Label != want.Label || got.Hint != want.Hint {
			t.Fatalf("group %d header = %q/%q, want %q/%q", i, got.Label, got.Hint, want.Label, want.Hint)
		}
		if len(got.Options) < len(want.Options) {
			t.Fatalf("group %d offers %d choices, the old list had %d", i, len(got.Options), len(want.Options))
		}
		for j, wantChoice := range want.Options {
			if !reflect.DeepEqual(got.Options[j], wantChoice) {
				t.Fatalf("group %d choice %d differs from the list users know:\n got: %+v\nwant: %+v", i, j, got.Options[j], wantChoice)
			}
		}
		// A provider may gain endpoints (Z.AI's Coding Plan hosts, 2026-11-07);
		// nothing else may grow.
		for _, extra := range got.Options[len(want.Options):] {
			if extra.Provider != "zhipuai" {
				t.Fatalf("group %d gained an endpoint nobody asked for: %+v", i, extra)
			}
		}
	}
	// The four new providers, in the registry's order.
	wantNew := []string{"meituan", "tencent", "volcengine", "byteplus"}
	generated := providerGroups[len(old):]
	if len(generated) != len(wantNew) {
		t.Fatalf("generated %d new groups %+v, want %v", len(generated), generated, wantNew)
	}
	for i, provider := range wantNew {
		group := generated[i]
		if len(group.Options) == 0 || group.Options[0].Provider != provider {
			t.Fatalf("new group %d = %+v, want provider %q", i, group, provider)
		}
		for _, option := range group.Options {
			if option.Provider != provider || option.DefaultBaseURL == "" || option.APIKeyLabel == "" {
				t.Fatalf("%s choice is incomplete: %+v", provider, option)
			}
			if option.Subscription != "" || option.Custom {
				t.Fatalf("%s is a key provider, not a sign-in: %+v", provider, option)
			}
		}
	}
	// Z.AI's Coding Plan endpoints are offered (owner 2026-11-07), with the
	// protocol each host speaks.
	var zhipuaiChoices []providerChoice
	for _, group := range providerGroups {
		for _, option := range group.Options {
			if option.Provider == "zhipuai" {
				zhipuaiChoices = append(zhipuaiChoices, option)
			}
		}
	}
	if len(zhipuaiChoices) != 6 {
		t.Fatalf("zhipuai choices = %d, want 6", len(zhipuaiChoices))
	}
	anthropicProtocol := 0
	for _, option := range zhipuaiChoices {
		if !option.SupportsAPIPath {
			anthropicProtocol++
		}
	}
	if anthropicProtocol != 2 {
		t.Fatalf("zhipuai Messages endpoints = %d, want 2", anthropicProtocol)
	}
}

// TestRegistryCarriesTheClientConstants pins the two endpoint values pkg/config
// had to copy (it may not import the provider packages): the sign-in endpoints
// must stay the ones the clients build against.
func TestRegistryCarriesTheClientConstants(t *testing.T) {
	chatgpt, ok := appcfg.BuiltinProviderByID("chatgpt")
	if !ok || chatgpt.Endpoints[0].BaseURL != openai.CodexBaseURL {
		t.Fatalf("chatgpt endpoint = %+v, want %q", chatgpt.Endpoints, openai.CodexBaseURL)
	}
	openCode, ok := appcfg.BuiltinProviderByID("opencode-go")
	if !ok || openCode.Endpoints[0].BaseURL != opencode.ProviderBaseURL("opencode-go") {
		t.Fatalf("opencode-go endpoint = %+v, want %q", openCode.Endpoints, opencode.ProviderBaseURL("opencode-go"))
	}
	// The first page's sign-in rows are generated from those same registry
	// endpoints: the wording under each one is the endpoint's own.
	chatGPTRow := subscriptionChoice(chatGPTLike())
	if chatGPTRow.Provider != "chatgpt" || chatGPTRow.Hint != "Use Codex included with your ChatGPT plan" {
		t.Fatalf("generated chatgpt row = %+v", chatGPTRow)
	}
	openCodeRow := subscriptionChoice(openCodeLike())
	if openCodeRow.Provider != "opencode-go" || openCodeRow.Hint != "Device-code login; use your Go/Go Plus quota" {
		t.Fatalf("generated opencode-go row = %+v", openCodeRow)
	}
	// A sign-in's endpoint comes from the login, never from the row, so the row
	// itself names no base url (the offers page shows the real one per model).
	if chatGPTRow.DefaultBaseURL != "" || openCodeRow.DefaultBaseURL != "" {
		t.Fatalf("sign-in rows carry %q/%q, want no base url of their own", chatGPTRow.DefaultBaseURL, openCodeRow.DefaultBaseURL)
	}
}

// The subscription flow asks twice: which models to add, and then which of them
// the agent may pick for subagents on its own (283, owner 2026-11-10). The
// second answer is written on the rows the sign-in just wrote, addressed by the
// ids Provision returned, in the order the offers were ticked.
func TestConfigureSubscriptionWritesTheSubagentSwitchByRowID(t *testing.T) {
	sub := chatGPTLike()
	browserMachine(t)
	cfg := setupTestConfig(t, &fakeSelector{
		confirms:     []bool{true},
		multiSelects: [][]int{{0, 1}, {1}},
	})
	opts, ok, err := runSubscriptionSetup(t, cfg, sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	selector := cfg.Selector.(*fakeSelector)
	if len(selector.multiLabels) != 2 ||
		!strings.HasPrefix(selector.multiLabels[0], "ChatGPT\n") ||
		!strings.HasPrefix(selector.multiLabels[1], "Subagents\n") {
		t.Fatalf("check lists = %#v, want the models and then the subagent authorization", selector.multiLabels)
	}
	if len(sub.provisioned) != 1 || len(sub.provisioned[0]) != 2 {
		t.Fatalf("provisioned = %#v, want one write of the two ticked models", sub.provisioned)
	}
	db, err := process.StateDB(process.Context{Home: cfg.Home, ConfigPath: cfg.ConfigPath})
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	defer db.Close()
	store := &state.LLMModelStore{DB: db}
	for i, offer := range sub.provisioned[0] {
		provider, model, baseURL, apiPath := appcfg.NormalizeLLMModelIdentity(offer.Provider, offer.Model, offer.BaseURL, offer.APIPath)
		row, found, err := store.FindByIdentity(context.Background(), provider, model, baseURL, apiPath)
		if err != nil || !found {
			t.Fatalf("row %d: found=%t err=%v", i, found, err)
		}
		want := i == 1
		if row.SubagentAllowed != want {
			t.Fatalf("row %d (%s) SubagentAllowed = %t, want %t", i, row.Model, row.SubagentAllowed, want)
		}
	}
	// The session switches to the first model ticked, and the switch it carries
	// is that row's own answer.
	if opts.Model != "gpt-future" || opts.SubagentAllowed {
		t.Fatalf("options = %+v, want the first offer with its own (closed) switch", opts)
	}
}

// An installation that already holds the row opens the page on that row's
// switch, so stepping through a save keeps a user's earlier answer; a row that
// predates the switch opens closed.
func TestSubagentPageDefaultsToTheStoredSwitch(t *testing.T) {
	cfg := setupTestConfig(t, &fakeSelector{})
	db, err := process.StateDB(process.Context{Home: cfg.Home, ConfigPath: cfg.ConfigPath})
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	if _, err := (&state.LLMModelStore{DB: db}).Create(context.Background(), state.LLMModel{
		Provider: appcfg.CustomProvider, Model: "gpt-4o", BaseURL: "https://api.example.com/v1",
		APIPath: appcfg.APIPathChatCompletions, APIKey: "sk-old", SubagentAllowed: true,
	}); err != nil {
		t.Fatalf("seed the row: %v", err)
	}
	_ = db.Close()

	setup := newLLMSetup(cfg)
	setup.setChoice(customChoice())
	setup.model = "gpt-4o"
	setup.baseURL = "https://api.example.com/v1"
	setup.apiPath = appcfg.APIPathChatCompletions
	allowed, err := setup.pickSubagents()
	if err != nil {
		t.Fatalf("pickSubagents: %v", err)
	}
	if !allowed || !setup.subagentAllowed {
		t.Fatalf("answer = %t/%t, want the stored yes", allowed, setup.subagentAllowed)
	}
	selector := cfg.Selector.(*fakeSelector)
	if got := selector.selectDefaults[len(selector.selectDefaults)-1]; got != subagentsYes {
		t.Fatalf("page default = %q, want the stored yes", got)
	}
	if got := setup.reviewFacts(); !containsFact(got, "Subagents", subagentsYes) {
		t.Fatalf("review facts = %#v, want the Subagents line", got)
	}

	// A row the install never authorized opens on No, and so does a row that
	// does not exist yet.
	other := newLLMSetup(setupTestConfig(t, &fakeSelector{}))
	other.setChoice(customChoice())
	other.model = "gpt-4o-mini"
	other.baseURL = "https://api.example.com/v1"
	other.apiPath = appcfg.APIPathChatCompletions
	if _, err := other.pickSubagents(); err != nil {
		t.Fatalf("pickSubagents: %v", err)
	}
	if other.subagentAllowed {
		t.Fatalf("subagentAllowed = true, want the closed default")
	}
}

// containsFact reports whether the review lists the given label with the value.
func containsFact(facts []turn.StatusFact, label, value string) bool {
	for _, fact := range facts {
		if fact.Label == label {
			return fact.Value == value
		}
	}
	return false
}

// A device flow whose login carries no short code (a PKCE browser-confirm
// flow, like a subscription plugin's) shows the address as the whole
// instruction: an empty code box, or "enter this code" wording, would send the
// user hunting for a code that does not exist.
func TestConfigureSubscriptionDeviceFlowWithoutAUserCode(t *testing.T) {
	sub := openCodeLike()
	sub.login = newFakeLogin("https://opencode.ai/auth", "")
	opened := browserMachine(t)
	out := &bytes.Buffer{}
	cfg := setupTestConfig(t, &fakeSelector{confirms: []bool{true}})
	cfg.Output = out
	opts, ok, err := runSubscriptionSetup(t, cfg, sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if sub.startedFlow != subscription.FlowDeviceCode {
		t.Fatalf("flow = %q", sub.startedFlow)
	}
	if len(*opened) != 1 || (*opened)[0] != "https://opencode.ai/auth" {
		t.Fatalf("opened %v, want the address the page shows", *opened)
	}
	page := out.String()
	if !strings.Contains(page, "Open https://opencode.ai/auth to sign in.") {
		t.Fatalf("sign-in page = %q, want it to name the address as the instruction", page)
	}
	if strings.Contains(page, "enter this code") {
		t.Fatalf("sign-in page = %q, must not mention a code", page)
	}
	if opts.Provider != "opencode-go" || opts.APIKey != "" {
		t.Fatalf("options = %+v", opts)
	}
}

// offerRowSeed writes the row an earlier /connect run would have left for one of
// the subscription's offers.
func offerRowSeed(t *testing.T, cfg PromptConfig, sub *fakeSubscription, offer subscription.Offer, key string) string {
	t.Helper()
	db, err := process.StateDB(process.Context{Home: cfg.Home, ConfigPath: cfg.ConfigPath})
	if err != nil {
		t.Fatalf("state db: %v", err)
	}
	defer db.Close()
	id, err := process.UpsertLLMModel(context.Background(), db, process.LLMModelInput{
		Provider: offer.Provider, Model: offer.Model, BaseURL: offer.BaseURL, APIPath: offer.APIPath,
		APIKey: &key, Source: sub.id, SourcePlan: offer.Plan,
	})
	if err != nil {
		t.Fatalf("seed row: %v", err)
	}
	return id
}

// The models page opens on what /model already runs: the rows a previous sign-in
// wrote are ticked, across both of Ark's plans. (The fake plan still refuses its
// first write and takes the pasted key, so the flow completes; whether a held row
// asks for a key at all is the subscription's own answer, pinned in
// pkg/subscription's tests.)
func TestOffersPageOpensOnTheModelsAlreadyAdded(t *testing.T) {
	sub := volcengineLike()
	sub.check = turn.SignInCheck{State: turn.SignInUsable}
	sub.offers = append(sub.offers, subscription.Offer{
		Subscription: "volcengine", Provider: "volcengine", Model: "glm-5.3",
		BaseURL: "https://ark.cn-beijing.volces.com/api/plan", APIPath: "/v1/messages",
		Plan: "agent-plan", PlanLabel: "Agent Plan",
	})
	selector := &fakeSelector{inputs: []string{"placeholder-key"}, multiSelects: [][]int{{1}}}
	cfg := setupTestConfig(t, selector)
	offerRowSeed(t, cfg, sub, sub.offers[1], "placeholder-plan-key")

	opts, ok, err := runSubscriptionSetup(t, cfg, sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if opts.Model != "glm-5.3" {
		t.Fatalf("options = %+v, want the model that was already added", opts)
	}
	if got := selector.multiDefaults[0]; !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("checked rows = %v, want the already added row [1]", got)
	}
}

// A login-backed row carries the endpoint its model speaks when a legacy
// configuration wrote it, and none when /connect writes it: the login decides
// the endpoint either way, so both are the same model in /model and both are
// ticked. The offer below is the production shape — a login listing names no
// API path (`opencode.go` goes through `offersFromRecords`) — while the row a
// pre-table configuration left behind carries the path it displays.
func TestOffersPageOpensOnALoginRowWhateverPathItShows(t *testing.T) {
	sub := openCodeLike()
	sub.check = turn.SignInCheck{State: turn.SignInUsable}
	// openCodeLike's offers carry a path for the rows they write; a real login
	// listing carries none.
	sub.offers[1].APIPath = ""
	selector := &fakeSelector{multiSelects: [][]int{{1}}}
	cfg := setupTestConfig(t, selector)
	legacy := sub.offers[1]
	legacy.APIPath = "/chat/completions"
	offerRowSeed(t, cfg, sub, legacy, "")

	opts, ok, err := runSubscriptionSetup(t, cfg, sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if opts.Model != "kimi-k3" {
		t.Fatalf("options = %+v, want the model that was already added", opts)
	}
	if got := selector.multiDefaults[0]; !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("checked rows = %v, want the already added row [1]", got)
	}
}

// setupWaitSelector is a selector that can own the terminal during a sign-in
// wait: it records that the setup asked, and answers either the flow's outcome
// or the abandonment a person pressing Esc gives. Embedded in *fakeSelector so
// every other page answers as before.
type setupWaitSelector struct {
	*fakeSelector
	// awaitErr is what the wait answers instead of the flow's outcome:
	// ErrCancelled for an abandoned sign-in, nil to deliver the outcome.
	awaitErr error
	// release, when set, ends the held login before the outcome is consumed, so
	// a case that delivers the flow's outcome has one to deliver.
	release func()

	calls    int
	doneLive bool
}

func (s *setupWaitSelector) AwaitSignIn(done <-chan error) error {
	s.calls++
	s.doneLive = done != nil
	if s.release != nil {
		s.release()
	}
	if s.awaitErr != nil {
		return s.awaitErr
	}
	return <-done
}

// Esc on the wait page abandons the sign-in: the page stays in the transcript,
// the flow is cancelled, nothing is written, and the wizard steps back to the
// provider list instead of carrying on to the offers page.
func TestConfigureSubscriptionWaitOwnsTheInputAndStepsBackOnEscape(t *testing.T) {
	sub := openCodeLike()
	headlessMachine(t)
	out := &bytes.Buffer{}
	selector := &setupWaitSelector{fakeSelector: &fakeSelector{confirms: []bool{true}}, awaitErr: ErrCancelled}
	cfg := setupTestConfig(t, selector)
	cfg.Output = out
	// The flow is still waiting for the person in the browser: the wait must
	// resolve it, not find it already finished.
	sub.login.waitRelease = make(chan error, 1)

	opts, ok, err := runSubscriptionSetup(t, cfg, sub)
	if err != nil {
		t.Fatalf("configure err=%v", err)
	}
	if ok {
		t.Fatal("the setup saved after the sign-in was abandoned")
	}
	if selector.calls != 1 || !selector.doneLive {
		t.Fatalf("wait calls = %d done live = %t, want one wait on a live channel", selector.calls, selector.doneLive)
	}
	for _, want := range []string{
		"Open https://opencode.ai/auth and enter this code.",
		"QK7M-2PLX",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("sign-in page = %q, want it to carry %q", out.String(), want)
		}
	}
	if !sub.login.canceled {
		t.Fatal("the abandoned sign-in was left running")
	}
	if sub.offerCalls != 0 || len(sub.provisioned) != 0 {
		t.Fatalf("offers = %d provisioned = %d, want the wizard to step back", sub.offerCalls, len(sub.provisioned))
	}
	if opts.Provider != "" || opts.Model != "" {
		t.Fatalf("options = %+v, want none", opts)
	}
}

// A sign-in that finishes in the browser while the wait owns the input carries
// on to the offers page exactly as Login.Wait did.
func TestConfigureSubscriptionWaitDeliversTheOutcome(t *testing.T) {
	sub := openCodeLike()
	headlessMachine(t)
	selector := &setupWaitSelector{fakeSelector: &fakeSelector{confirms: []bool{true}}}
	opts, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if selector.calls != 1 || !selector.doneLive {
		t.Fatalf("wait calls = %d done live = %t, want one wait on a live channel", selector.calls, selector.doneLive)
	}
	if opts.Provider != "opencode-go" || opts.Model != "glm-5.3" {
		t.Fatalf("options = %+v, want the plan's default offer", opts)
	}
}

// A flow that failed on its own terms reaches the caller as that error: only the
// person abandoning the sign-in is a step back.
func TestConfigureSubscriptionWaitPropagatesTheFlowError(t *testing.T) {
	sub := openCodeLike()
	headlessMachine(t)
	expired := errors.New("code expired")
	sub.login.waitErr = expired
	selector := &setupWaitSelector{fakeSelector: &fakeSelector{confirms: []bool{true}}}
	_, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub)
	if !errors.Is(err, expired) {
		t.Fatalf("configure err=%v, want the flow's error", err)
	}
	if ok {
		t.Fatal("the setup saved after the sign-in failed")
	}
}

// A selector that cannot own the terminal (a line selector, a non-TTY run) waits
// for the flow's outcome instead, exactly as before.
func TestConfigureSubscriptionPlainSelectorStillWaits(t *testing.T) {
	sub := openCodeLike()
	headlessMachine(t)
	opts, ok, err := runSubscriptionSetup(t, setupTestConfig(t, &fakeSelector{confirms: []bool{true}}), sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if opts.Provider != "opencode-go" || opts.Model != "glm-5.3" {
		t.Fatalf("options = %+v, want the plan's default offer", opts)
	}
}

// --- /connect panels: the surface a real terminal drives (plan 312) ---

// connectPanel is /connect as a real terminal has it: the raw selector's own
// pages, reading the keys a test types into its pipe, over a viewport renderer
// the test reads the screen from. A sign-in's own address, code, callback field
// and fetch state belong on that screen, never in the transcript.
type connectPanel struct {
	*rawSelector
	keys   *os.File
	render *Renderer
}

func newConnectPanel(t *testing.T, width, height int) *connectPanel {
	t.Helper()
	render := newComposerRenderer(t, width, height)
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		pr.Close()
		pw.Close()
	})
	return &connectPanel{rawSelector: newRawSelector(pr, render), keys: pw, render: render}
}

// typeKeys types what a person presses into the terminal the selector holds.
func (p *connectPanel) typeKeys(t *testing.T, keys string) {
	t.Helper()
	if _, err := p.keys.WriteString(keys); err != nil {
		t.Fatalf("type %q: %v", keys, err)
	}
}

// setupAnswer is what one setup run returned.
type setupAnswer struct {
	opts process.Options
	ok   bool
	err  error
}

// start runs the setup on this subscription from its sign-in page.
func startConnectSetup(t *testing.T, cfg PromptConfig, sub *fakeSubscription) <-chan setupAnswer {
	t.Helper()
	withSubscriptions(t, sub)
	setup := newLLMSetup(cfg)
	setup.setChoice(subscriptionChoice(sub))
	setup.page = setupPageCredentials
	answer := make(chan setupAnswer, 1)
	go func() {
		opts, ok, err := setup.run()
		answer <- setupAnswer{opts: opts, ok: ok, err: err}
	}()
	return answer
}

// finish ends a run: Esc is the person leaving the page they are on, and closing
// the pipe is the terminal going away, which ends whatever page the setup steps
// back to.
func (p *connectPanel) finish(t *testing.T, answer <-chan setupAnswer) setupAnswer {
	t.Helper()
	p.typeKeys(t, "\x1b")
	p.keys.Close()
	select {
	case got := <-answer:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("the setup did not return after the sign-in page was closed")
		return setupAnswer{}
	}
}

// transcriptText is everything the retained transcript holds: the container a
// sign-in's own pages must stay out of.
func transcriptText(r *Renderer) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, block := range r.vm.blocks {
		b.WriteString(stripAnsi(block.frame.Content))
		b.WriteByte('\n')
	}
	return b.String()
}

// paintedPanelText is the panel the overlay area is showing, empty when none is.
func paintedPanelText(r *Renderer) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.overlayPanel == nil {
		return ""
	}
	var b strings.Builder
	for _, line := range r.vpLastComposer.lines {
		b.WriteString(stripAnsi(line))
		b.WriteByte('\n')
	}
	return b.String()
}

// waitForPanelText waits until the painted panel carries needle and answers the
// panel's text either way, so the caller decides what a miss means.
func waitForPanelText(r *Renderer, needle string) string {
	deadline := time.Now().Add(5 * time.Second)
	for {
		text := paintedPanelText(r)
		if strings.Contains(text, needle) || time.Now().After(deadline) {
			return text
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// confirmPageHints are the key hints the confirmation a sign-in asks before it
// starts draws, one per answer it opens on. Its text also names the provider
// ("Sign in with <label>"), so a test that waits for a needle the confirmation
// page happens to carry gets that page back instead of the sign-in page — and
// then asserts on the wrong one. It is a race, so it only shows up sometimes.
var confirmPageHints = []string{
	"Y to confirm N or Enter to decline",
	"Y or Enter to confirm N to decline",
}

// waitForSignInPage is waitForPanelText for a page that comes after that
// confirmation: it refuses to hand the confirmation back, so a needle that is not
// unique to the sign-in page fails loudly with the reason instead of being
// asserted on.
func waitForSignInPage(t *testing.T, r *Renderer, needle string) string {
	t.Helper()
	text := waitForPanelText(r, needle)
	for _, hint := range confirmPageHints {
		if strings.Contains(text, hint) {
			t.Fatalf("the panel is still the confirmation page; pass a needle only the sign-in page carries (waiting for %q):\n%s", needle, text)
		}
	}
	return text
}

// waitUntil waits until cond holds, and reports whether it did.
func waitUntil(cond func() bool) bool {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// panelLogin is a sign-in the setup reads while a test drives it: the flow runs
// in the setup's goroutine and the test publishes what it learns, so every field
// is behind the lock.
type panelLogin struct {
	mu        sync.Mutex
	url       string
	code      string
	expires   time.Time
	submitted []string
	canceled  bool
	release   chan error
}

func newPanelLogin(url, code string) *panelLogin {
	return &panelLogin{url: url, code: code, release: make(chan error, 1)}
}

// publish is the flow learning what its provider answered, later than it
// started.
func (l *panelLogin) publish(url, code string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.url, l.code = url, code
}

func (l *panelLogin) AuthorizeURL() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.url
}

func (l *panelLogin) UserCode() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.code
}

// DeviceCodeInfo is the whole snapshot a page reads to show an expiry, read under
// one lock so a late address and its deadline cannot be paired with each other's
// stale half.
func (l *panelLogin) DeviceCodeInfo() subscription.DeviceCodeInfo {
	l.mu.Lock()
	defer l.mu.Unlock()
	return subscription.DeviceCodeInfo{AuthorizeURL: l.url, UserCode: l.code, ExpiresAt: l.expires}
}

func (l *panelLogin) Submit(_ context.Context, pasted string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.submitted = append(l.submitted, pasted)
	return nil
}

func (l *panelLogin) Wait(ctx context.Context) error {
	select {
	case err := <-l.release:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *panelLogin) Cancel() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.canceled = true
}

func (l *panelLogin) sent() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.submitted...)
}

// A device-code sign-in's address and code are the panel's: printed into the
// transcript they scroll away from the wait that needs them, which is the
// reported bug — and it is the same for every subscription, not just Ark's.
func TestConnectSetupPanelDeviceSignInStaysInThePanel(t *testing.T) {
	sub := openCodeLike()
	opened := browserMachine(t)
	sub.panelLogin = newPanelLogin("https://opencode.ai/auth", "QK7M-2PLX")
	panel := newConnectPanel(t, 100, 40)
	cfg := setupTestConfig(t, panel)
	cfg.Output = panel.render.OutputWriter()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg.Context = ctx
	answer := startConnectSetup(t, cfg, sub)

	panel.typeKeys(t, "y\r") // the confirmation page opens the sign-in
	shown := waitForSignInPage(t, panel.render, "QK7M-2PLX")
	failure := ""
	if !strings.Contains(shown, "QK7M-2PLX") {
		failure = fmt.Sprintf("the sign-in panel never showed the device code; the page went to the transcript instead:\n%s", transcriptText(panel.render))
	} else if text := transcriptText(panel.render); strings.Contains(text, "QK7M-2PLX") || strings.Contains(text, "https://opencode.ai/auth") {
		failure = fmt.Sprintf("the sign-in page was also written into the transcript:\n%s", text)
	}
	got := panel.finish(t, answer)
	if failure != "" {
		t.Fatal(failure)
	}
	if len(*opened) != 1 || (*opened)[0] != "https://opencode.ai/auth" {
		t.Fatalf("opened %v, want the sign-in page opened once", *opened)
	}
	if sub.offerCalls != 0 || len(sub.provisioned) != 0 || got.ok {
		t.Fatalf("offers = %d provisioned = %d ok = %t, want the abandoned sign-in to write nothing", sub.offerCalls, len(sub.provisioned), got.ok)
	}
}

// The paste flow keeps the address, the instruction and the field in one panel:
// the address in the transcript is what the person has to copy back by hand, so
// splitting them is the same bug in another flow.
func TestConnectSetupPanelPasteSignInStaysInThePanel(t *testing.T) {
	sub := chatGPTLike()
	opened := headlessMachine(t)
	sub.panelLogin = newPanelLogin("https://auth.openai.com/authorize?state=abc", "")
	panel := newConnectPanel(t, 100, 40)
	cfg := setupTestConfig(t, panel)
	cfg.Output = panel.render.OutputWriter()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg.Context = ctx
	answer := startConnectSetup(t, cfg, sub)

	panel.typeKeys(t, "y\r")
	shown := waitForSignInPage(t, panel.render, "auth.openai.com")
	failure := ""
	if !strings.Contains(shown, "auth.openai.com") {
		failure = fmt.Sprintf("the paste panel never showed the address to open:\n%s", transcriptText(panel.render))
	} else if text := transcriptText(panel.render); strings.Contains(text, "auth.openai.com") {
		failure = fmt.Sprintf("the address went to the transcript as well:\n%s", text)
	}
	if failure == "" {
		const callback = "http://127.0.0.1:9/auth/callback?code=synthetic"
		panel.typeKeys(t, callback+"\r")
		if !waitUntil(func() bool { return len(sub.panelLogin.sent()) == 1 }) {
			failure = fmt.Sprintf("the panel never handed the pasted address to the flow; it shows:\n%s", paintedPanelText(panel.render))
		} else if sent := sub.panelLogin.sent(); sent[0] != callback {
			failure = fmt.Sprintf("submitted %q, want the pasted address", sent)
		} else if text := paintedPanelText(panel.render); text == "" {
			failure = "the panel closed on submit; the sign-in is still waiting for its turnaround"
		}
	}
	got := panel.finish(t, answer)
	if failure != "" {
		t.Fatal(failure)
	}
	if len(*opened) != 0 {
		t.Fatalf("opened %v, want no browser on a machine that pastes the callback", *opened)
	}
	if sub.offerCalls != 0 || len(sub.provisioned) != 0 || got.ok {
		t.Fatalf("offers = %d provisioned = %d ok = %t, want no model picked before the flow finishes", sub.offerCalls, len(sub.provisioned), got.ok)
	}
}

// Fetching a saved account's models is a /connect step with a command panel's
// status, and Esc cancels the request without signing in again.
func TestConnectSetupPanelFetchStaysInThePanel(t *testing.T) {
	sub := openCodeLike()
	sub.check = turn.SignInCheck{State: turn.SignInUsable}
	browserMachine(t)
	panel := newConnectPanel(t, 100, 40)
	cfg := setupTestConfig(t, panel)
	cfg.Output = panel.render.OutputWriter()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg.Context = ctx
	release := make(chan struct{})
	var releaseOnce sync.Once
	// attempts counts the requests this test let the flow make. It is a channel
	// because the request runs beside the setup's goroutine, which a cancelled
	// fetch never joins: only an edge like this can observe it without a race.
	attempts := make(chan struct{}, 4)
	sub.offersFn = func(int) ([]subscription.Offer, error) {
		attempts <- struct{}{}
		<-release
		return nil, errors.New("the listing was refused")
	}
	answer := startConnectSetup(t, cfg, sub)

	shown := waitForPanelText(panel.render, "Fetching this account's available models")
	failure := ""
	if !strings.Contains(shown, "Fetching this account's available models") {
		failure = fmt.Sprintf("the fetch never reached a panel; it was written to the transcript:\n%s", transcriptText(panel.render))
	} else if text := transcriptText(panel.render); strings.Contains(text, "Fetching this account's") {
		failure = fmt.Sprintf("the fetch state was written into the transcript as well:\n%s", text)
	}
	panel.typeKeys(t, "\x1b")
	// Esc cancels this request and steps back to the list of sign-ins — not into a
	// retry (which would fetch again) and not towards a page that would sign in
	// again. The provider page's title is pickProvider's, for the onboarding this
	// config is not (setupTestConfig leaves PromptConfig.Onboarding false).
	if back := waitForPanelText(panel.render, "Connect a model"); !strings.Contains(back, "Connect a model") {
		failure = fmt.Sprintf("Esc during the fetch did not return to the sign-in list; the panel shows:\n%s", back)
	}
	releaseOnce.Do(func() { close(release) })
	panel.keys.Close()
	var got setupAnswer
	select {
	case got = <-answer:
	case <-time.After(10 * time.Second):
		t.Fatal("the setup did not return after the model request was cancelled")
	}
	if failure != "" {
		t.Fatal(failure)
	}
	// Nothing was fetched again, nothing signed in again and nothing was written:
	// a cancelled request is not a failure to retry and not a success to act on.
	if got := len(attempts); got != 1 {
		t.Fatalf("listing calls = %d, want the cancelled request alone", got)
	}
	if sub.startCalls != 0 {
		t.Fatalf("sign-ins = %d, want none for a login this machine holds", sub.startCalls)
	}
	if got.ok || got.opts.Provider != "" {
		t.Fatalf("configure ok=%t options=%+v, want the cancelled flow to write nothing", got.ok, got.opts)
	}
	if len(sub.provisioned) != 0 {
		t.Fatalf("provisioned %+v, want nothing written", sub.provisioned)
	}
}

// A selector with no panel cannot pretend the callback arrived: Esc on its paste
// page is the person stepping back, not a sign-in that then waits out the setup
// window.
func TestConnectSetupPanelPlainPasteEscapeCancels(t *testing.T) {
	sub := chatGPTLike()
	headlessMachine(t)
	sub.panelLogin = newPanelLogin("https://auth.openai.com/authorize?state=abc", "")
	selector := &cancellingPasteSelector{fakeSelector: &fakeSelector{confirms: []bool{true}}}
	cfg := setupTestConfig(t, selector)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cfg.Context = ctx
	_, ok, err := runSubscriptionSetup(t, cfg, sub)
	if err != nil || ok {
		t.Fatalf("configure err=%v ok=%t, want the abandoned paste page to step back", err, ok)
	}
	if sent := sub.panelLogin.sent(); len(sent) != 0 {
		t.Fatalf("submitted %v, want nothing handed to the flow", sent)
	}
	if sub.offerCalls != 0 || len(sub.provisioned) != 0 {
		t.Fatalf("offers = %d provisioned = %d, want nothing fetched or written", sub.offerCalls, len(sub.provisioned))
	}
}

// cancellingPasteSelector is a selector whose paste page the person leaves with
// Esc: the plain contract's ok=false, and no error.
type cancellingPasteSelector struct {
	*fakeSelector
}

func (s *cancellingPasteSelector) Input(label string, defaultValue string) (string, bool, error) {
	s.inputLabels = append(s.inputLabels, label)
	return "", false, nil
}

// brokenPasteSelector is a selector whose paste page failed to read: the
// terminal is gone and the setup has to report that, not call a progress stop it
// never installed.
type brokenPasteSelector struct {
	*fakeSelector
	err error
}

func (s *brokenPasteSelector) Input(string, string) (string, bool, error) {
	return "", false, s.err
}

func TestConnectSetupPanelPlainPasteReadErrorIsReported(t *testing.T) {
	sub := chatGPTLike()
	headlessMachine(t)
	sub.panelLogin = newPanelLogin("https://auth.openai.com/authorize?state=abc", "")
	broken := errors.New("the terminal went away")
	selector := &brokenPasteSelector{fakeSelector: &fakeSelector{confirms: []bool{true}}, err: broken}
	_, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub)
	if !errors.Is(err, broken) || ok {
		t.Fatalf("configure err=%v ok=%t, want the read failure reported", err, ok)
	}
}

// failingOverlaySelector is a selector that has an overlay and cannot paint it:
// a report it cannot show is the caller's error, never a second copy written into
// the transcript.
type failingOverlaySelector struct {
	*fakeSelector
	err error
}

func (s *failingOverlaySelector) ShowInfo(string, string) error { return s.err }

func (s *failingOverlaySelector) ConfirmInfo(string, string, string) (bool, error) {
	return false, s.err
}

// A connection test whose report cannot be painted reports that failure instead
// of downgrading the same words into the transcript.
func TestConnectSetupPanelConnectionNoteErrorIsReported(t *testing.T) {
	stubLLMProbe(t, errors.New("the API key was refused"))
	paintErr := errors.New("the report could not be painted")
	out := &bytes.Buffer{}
	answers := &fakeSelector{multiSelects: [][]int{{}}}
	cfg := withCustomAnswers(setupTestConfig(t, answers), customAnswers{
		baseURL:  "https://api.example.com/v1",
		protocol: "OpenAI Chat Completions",
		key:      "sk-wrong",
		model:    "gpt-4o",
	})
	cfg.Selector = &failingOverlaySelector{fakeSelector: answers, err: paintErr}
	cfg.Output = out
	_, _, err := PromptMainLLMSetup(cfg)
	if !errors.Is(err, paintErr) {
		t.Fatalf("configure err=%v, want the overlay's own failure", err)
	}
	if out.String() != "" {
		t.Fatalf("output = %q, want the report not downgraded into it", out.String())
	}
}

// The models page's own reports propagate the same way: a failed write is
// reported, not retried in a loop while its explanation goes to the transcript.
func TestConnectSetupPanelProvisionNoteErrorIsReported(t *testing.T) {
	sub := openCodeLike()
	sub.check = turn.SignInCheck{State: turn.SignInUsable}
	browserMachine(t)
	sub.provisionFn = func(call int) error {
		if call == 1 {
			return errors.New("the console refused the write")
		}
		return nil
	}
	paintErr := errors.New("the report could not be painted")
	out := &bytes.Buffer{}
	cfg := setupTestConfig(t, &failingOverlaySelector{fakeSelector: &fakeSelector{}, err: paintErr})
	cfg.Output = out
	_, _, err := runSubscriptionSetup(t, cfg, sub)
	if !errors.Is(err, paintErr) {
		t.Fatalf("configure err=%v, want the overlay's own failure", err)
	}
	// The fetch line is that surface's plain text and belongs there; the report
	// the overlay could not paint is what must not be downgraded into it.
	if text := out.String(); strings.Contains(text, "Adding the models failed") || strings.Contains(text, "console refused") {
		t.Fatalf("output = %q, want the unpaintable report kept out of it", text)
	}
}

// A device-code page's countdown is the code's own life, to the second and never
// negative: repainting it cannot extend it and an expired code does not become a
// negative time.
func TestConnectSetupPanelCountdownIsItsOwnDeadline(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		left time.Duration
		want string
	}{
		{10 * time.Minute, "10:00"},
		{9*time.Minute + 59*time.Second, "9:59"},
		{time.Minute, "1:00"},
		{59 * time.Second, "0:59"},
		{time.Second, "0:01"},
		{time.Millisecond, "0:01"},
		{0, "0:00"},
		{-time.Minute, "0:00"},
	}
	for _, tc := range cases {
		if got := signInCountdown(now.Add(tc.left), now); got != tc.want {
			t.Fatalf("countdown at %v = %q, want %q", tc.left, got, tc.want)
		}
	}
}

// A sign-in that issues no code is never described as one that asks for a code:
// the same device flow is a plain browser confirmation for the plugins that use
// it that way, and the page cannot know which it is before it has learned a code.
func TestConnectSetupPanelNoCodePluginWordsNameNoCode(t *testing.T) {
	prompt := signInPrompt("Qoder CN", subscription.FlowDeviceCode)
	if strings.Contains(strings.ToLower(prompt), "code") {
		t.Fatalf("sign-in prompt = %q, want no code it cannot know about", prompt)
	}
	if !strings.Contains(prompt, "opens the sign-in page for you to confirm") {
		t.Fatalf("sign-in prompt = %q, want the code-neutral confirmation", prompt)
	}
	if got := signInWaitingText("Qoder CN", subscription.FlowDeviceCode, false); strings.Contains(got, "code") {
		t.Fatalf("waiting text = %q, want no code it never showed", got)
	}
	if got := signInWaitingText("OpenCode", subscription.FlowDeviceCode, true); !strings.Contains(got, "confirm the code") {
		t.Fatalf("waiting text = %q, want the code the flow issued", got)
	}
}

// A login that publishes no snapshot still has the two getters every login has,
// and a page built from them shows what they answer.
func TestConnectSetupPanelReadsALoginWithoutASnapshot(t *testing.T) {
	login := newFakeLogin("https://signin.example.test/authorize", "ABCD-1234")
	if got := signInSnapshot(login); got.AuthorizeURL != "https://signin.example.test/authorize" || got.UserCode != "ABCD-1234" {
		t.Fatalf("snapshot = %+v, want the getters' answers", got)
	}
	page := &signInPanel{label: "Example", flow: subscription.FlowDeviceCode, login: login, browser: &connectBrowser{}}
	body := page.page().Body
	for _, want := range []string{"https://signin.example.test/authorize", "Code: ABCD-1234"} {
		if !strings.Contains(body, want) {
			t.Fatalf("panel body = %q, want it to carry %q", body, want)
		}
	}
	if strings.Contains(body, "expires in") {
		t.Fatalf("panel body = %q, want no countdown for a flow that published no expiry", body)
	}
}

// noCodeDeviceLike is a plugin-shaped device sign-in: the address to open, no
// code at all, so nothing on the page may ask for one.
func noCodeDeviceLike() *fakeSubscription {
	sub := &fakeSubscription{
		id: "plugin-device", label: "WorkBuddy",
		flows: []string{subscription.FlowDeviceCode},
		check: turn.SignInCheck{State: turn.SignInMissing},
		offers: []subscription.Offer{
			{Subscription: "plugin-device", Provider: "plugin-device", Model: "wb-model", BaseURL: "https://plugin.example.test", APIPath: "/v1/chat/completions", Default: true},
		},
	}
	sub.panelLogin = newPanelLogin("https://plugin.example.test/authorize", "")
	return sub
}

// devicePanelAnswers is what the panel showed — waiting for needle first — before
// the sign-in was abandoned, for the device flows a test drives with the same
// keys.
func devicePanelAnswers(t *testing.T, sub *fakeSubscription, cfg PromptConfig, keys, needle string) string {
	t.Helper()
	panel, ok := cfg.Selector.(*connectPanel)
	if !ok {
		t.Fatalf("selector = %T, want the terminal panel", cfg.Selector)
	}
	answer := startConnectSetup(t, cfg, sub)
	panel.typeKeys(t, keys)
	shown := waitForSignInPage(t, panel.render, needle)
	got := panel.finish(t, answer)
	if got.ok {
		t.Fatal("the abandoned sign-in saved a configuration")
	}
	if text := transcriptText(panel.render); text != "" {
		t.Fatalf("the sign-in wrote into the transcript:\n%s", text)
	}
	return shown
}

// Ark's page is the address, the code and the countdown, and the same flow on a
// machine with no browser is the address and the code alone — never a paste box,
// never a fabricated code, and the browser opens at most once.
func TestConnectSetupPanelArkPageAcrossMachines(t *testing.T) {
	t.Run("local", func(t *testing.T) {
		sub := volcengineLike()
		opened := browserMachine(t)
		sub.panelLogin = newPanelLogin("https://signin.volcengine.com/device?user_code=ABCD-1234", "ABCD-1234")
		sub.panelLogin.expires = time.Now().Add(10 * time.Minute)
		panel := newConnectPanel(t, 100, 40)
		cfg := setupTestConfig(t, panel)
		cfg.Output = panel.render.OutputWriter()
		// The confirmation page this sign-in asks first also says "Sign in with
		// Volcengine Ark", so the needle is a line only the page after it draws.
		shown := devicePanelAnswers(t, sub, cfg, "y\r", "If it did not open, visit:")
		for _, want := range []string{
			"Sign in with Volcengine Ark", "Opening your browser…",
			"https://signin.volcengine.com/device?user_code=ABCD-1234", "Code: ABCD-1234",
			"Waiting for you to confirm in the browser expires in ",
		} {
			if !strings.Contains(shown, want) {
				t.Fatalf("panel = %q, want it to carry %q", shown, want)
			}
		}
		if strings.Contains(shown, "Paste callback") {
			t.Fatalf("panel = %q, a device flow asks for no pasted address", shown)
		}
		if len(*opened) != 1 {
			t.Fatalf("opened %v, want the page opened once", *opened)
		}
	})
	t.Run("headless", func(t *testing.T) {
		sub := volcengineLike()
		opened := headlessMachine(t)
		sub.panelLogin = newPanelLogin("https://signin.volcengine.com/device?user_code=ABCD-1234", "ABCD-1234")
		sub.panelLogin.expires = time.Now().Add(10 * time.Minute)
		panel := newConnectPanel(t, 100, 40)
		cfg := setupTestConfig(t, panel)
		cfg.Output = panel.render.OutputWriter()
		shown := devicePanelAnswers(t, sub, cfg, "y\r", "Open this URL on any device:")
		for _, want := range []string{
			"Open this URL on any device: https://signin.volcengine.com/device?user_code=ABCD-1234",
			"Code: ABCD-1234", "Waiting for you to confirm expires in ",
		} {
			if !strings.Contains(shown, want) {
				t.Fatalf("panel = %q, want it to carry %q", shown, want)
			}
		}
		for _, unwanted := range []string{"Opening your browser", "Paste callback"} {
			if strings.Contains(shown, unwanted) {
				t.Fatalf("panel = %q, want no %q on a machine with no browser", shown, unwanted)
			}
		}
		if len(*opened) != 0 {
			t.Fatalf("opened %v, want no browser on a remote machine", *opened)
		}
	})
}

// A plugin's device sign-in issues no code, so its page shows none and asks for
// none: the user confirms in the browser and the flow finishes on its own.
func TestConnectSetupPanelNoCodeDeviceAsksForNoCode(t *testing.T) {
	sub := noCodeDeviceLike()
	browserMachine(t)
	panel := newConnectPanel(t, 100, 40)
	cfg := setupTestConfig(t, panel)
	cfg.Output = panel.render.OutputWriter()
	// The confirmation page also says "Sign in with WorkBuddy", so the needle is
	// the address this fixture publishes, which only the page after it carries.
	shown := devicePanelAnswers(t, sub, cfg, "y\r", "https://plugin.example.test/authorize")
	if !strings.Contains(shown, "https://plugin.example.test/authorize") {
		t.Fatalf("panel = %q, want the address to open", shown)
	}
	for _, unwanted := range []string{"Code:", "enter this code", "confirm the code", "Paste callback"} {
		if strings.Contains(shown, unwanted) {
			t.Fatalf("panel = %q, want no %q without a code", shown, unwanted)
		}
	}
	if !strings.Contains(shown, "Waiting for you to finish signing in…") {
		t.Fatalf("panel = %q, want the code-neutral wait", shown)
	}
}

// A browser callback's panel carries the title, the address and the wait, and it
// finishes without a key press: the callback is the flow's own news.
func TestConnectSetupPanelBrowserCallbackFinishesWithoutAKey(t *testing.T) {
	sub := chatGPTLike()
	opened := browserMachine(t)
	sub.panelLogin = newPanelLogin("https://auth.openai.com/authorize?state=synthetic", "")
	panel := newConnectPanel(t, 100, 40)
	cfg := setupTestConfig(t, panel)
	cfg.Output = panel.render.OutputWriter()
	answer := startConnectSetup(t, cfg, sub)
	panel.typeKeys(t, "y\r")

	shown := waitForSignInPage(t, panel.render, "auth.openai.com")
	for _, want := range []string{
		"Sign in with ChatGPT", "Opening your browser…", "If it did not open, visit: https://auth.openai.com/authorize?state=synthetic",
		"Waiting for you to finish signing in…",
	} {
		if !strings.Contains(shown, want) {
			t.Fatalf("panel = %q, want it to carry %q", shown, want)
		}
	}
	for _, unwanted := range []string{"Code:", "127.0.0.1", "expires in"} {
		if strings.Contains(shown, unwanted) {
			t.Fatalf("panel = %q, want no invented %q", shown, unwanted)
		}
	}
	// The browser finishes; no key is pressed for the sign-in itself, and the
	// wizard carries on to the models the account unlocked.
	sub.panelLogin.release <- nil
	models := waitForPanelText(panel.render, "Choose the models to add")
	if !strings.Contains(models, "ChatGPT") {
		t.Fatalf("models page = %q, want the sign-in carried on to it", models)
	}
	if text := transcriptText(panel.render); text != "" {
		t.Fatalf("the sign-in wrote into the transcript:\n%s", text)
	}
	panel.finish(t, answer)
	if len(*opened) != 1 || (*opened)[0] != "https://auth.openai.com/authorize?state=synthetic" {
		t.Fatalf("opened %v, want the page opened once", *opened)
	}
}

// A browser this machine could not open leaves the address usable: the failure is
// said on the page, whether it comes back at once or after the fact.
func TestConnectSetupPanelBrowserOpenFailureStaysInThePanel(t *testing.T) {
	cases := []struct {
		name string
		open func(url string, report func(error)) error
	}{
		{"sync", func(string, func(error)) error { return errors.New("no handler for https") }},
		{"async", func(_ string, report func(error)) error {
			report(errors.New("launch failed"))
			return nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := openCodeLike()
			previousOpener, previousBrowser := browserOpener, localBrowser
			browserOpener, localBrowser = tc.open, func() bool { return true }
			t.Cleanup(func() { browserOpener, localBrowser = previousOpener, previousBrowser })
			sub.panelLogin = newPanelLogin("https://opencode.ai/auth", "QK7M-2PLX")
			panel := newConnectPanel(t, 100, 40)
			cfg := setupTestConfig(t, panel)
			cfg.Output = panel.render.OutputWriter()
			shown := devicePanelAnswers(t, sub, cfg, "y\r", "Could not open your browser; the link above still works.")
			if !strings.Contains(shown, "Could not open your browser; the link above still works.") {
				t.Fatalf("panel = %q, want the failure said on the page", shown)
			}
			if !strings.Contains(shown, "https://opencode.ai/auth") {
				t.Fatalf("panel = %q, want the address still usable", shown)
			}
		})
	}
}

// A sign-in whose address has not arrived yet says so, and the person can leave
// at once: the page must not first block for the address.
func TestConnectSetupPanelPreparingPageCanBeLeftAtOnce(t *testing.T) {
	sub := openCodeLike()
	browserMachine(t)
	sub.panelLogin = newPanelLogin("", "")
	panel := newConnectPanel(t, 100, 40)
	cfg := setupTestConfig(t, panel)
	cfg.Output = panel.render.OutputWriter()
	answer := startConnectSetup(t, cfg, sub)
	panel.typeKeys(t, "y\r")
	shown := waitForSignInPage(t, panel.render, "Preparing sign-in…")
	if !strings.Contains(shown, "Sign in with OpenCode") {
		t.Fatalf("panel = %q, want the title before the flow names an address", shown)
	}
	if strings.Contains(shown, "Code:") {
		t.Fatalf("panel = %q, want no empty code", shown)
	}
	// Esc leaves at once: the page is not held behind the wait for an address.
	started := time.Now()
	panel.typeKeys(t, "\x1b")
	panel.keys.Close()
	select {
	case <-answer:
	case <-time.After(5 * time.Second):
		t.Fatal("the page could not be left while the address was missing")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("leaving the page took %v, want at once", elapsed)
	}
}

// An address and a code that arrive after the page opened are painted in place:
// the page does not have to be reopened, and the browser is opened once.
func TestConnectSetupPanelLateAddressRepaintsInPlace(t *testing.T) {
	sub := openCodeLike()
	opened := browserMachine(t)
	sub.panelLogin = newPanelLogin("", "")
	sub.panelLogin.expires = time.Now().Add(9*time.Minute + 41*time.Second)
	panel := newConnectPanel(t, 100, 40)
	cfg := setupTestConfig(t, panel)
	cfg.Output = panel.render.OutputWriter()
	answer := startConnectSetup(t, cfg, sub)
	panel.typeKeys(t, "y\r")
	waitForSignInPage(t, panel.render, "Preparing sign-in…")

	sub.panelLogin.publish("https://opencode.ai/auth", "QK7M-2PLX")
	shown := waitForSignInPage(t, panel.render, "QK7M-2PLX")
	if !strings.Contains(shown, "Code: QK7M-2PLX") || !strings.Contains(shown, "expires in ") {
		t.Fatalf("panel = %q, want the late code and its countdown", shown)
	}
	got := panel.finish(t, answer)
	if got.ok {
		t.Fatal("the abandoned sign-in saved a configuration")
	}
	if len(*opened) != 1 {
		t.Fatalf("opened %v, want exactly one browser for one sign-in", *opened)
	}
	if text := transcriptText(panel.render); text != "" {
		t.Fatalf("the sign-in wrote into the transcript:\n%s", text)
	}
}

// A login this machine already holds never starts a flow: the models page follows
// directly, and no sign-in page is painted at all.
func TestConnectSetupPanelSignedInLoginSkipsTheSignIn(t *testing.T) {
	sub := openCodeLike()
	sub.check = turn.SignInCheck{State: turn.SignInUsable}
	browserMachine(t)
	panel := newConnectPanel(t, 100, 40)
	cfg := setupTestConfig(t, panel)
	cfg.Output = panel.render.OutputWriter()
	sub.provisionFn = func(call int) error {
		if call == 1 {
			return errors.New("the console refused the write")
		}
		return nil
	}
	sub.panelLogin = newPanelLogin("https://opencode.ai/auth", "QK7M-2PLX")
	answer := startConnectSetup(t, cfg, sub)
	// The models page follows the probe directly, with no sign-in page before it.
	models := waitForPanelText(panel.render, "Choose the models to add")
	if strings.Contains(models, "Sign in with") {
		t.Fatalf("panel = %q, want no sign-in page for a login this machine holds", models)
	}
	panel.finish(t, answer)
	if sub.startCalls != 0 {
		t.Fatalf("sign-ins = %d, want a login this machine holds to be reused", sub.startCalls)
	}
	if text := transcriptText(panel.render); strings.Contains(text, "Sign in with") {
		t.Fatalf("transcript = %q, want no sign-in page for a held login", text)
	}
}

// A refused listing is retried on the saved login, and its page is released first:
// the retry is the existing confirmation, not a second sign-in.
func TestConnectSetupPanelFetchRetryReusesTheLogin(t *testing.T) {
	sub := openCodeLike()
	sub.check = turn.SignInCheck{State: turn.SignInUsable}
	browserMachine(t)
	sub.offersFn = func(call int) ([]subscription.Offer, error) {
		if call == 1 {
			return nil, errors.New("fetch models: http 429")
		}
		return openCodeLike().offers, nil
	}
	panel := newConnectPanel(t, 100, 40)
	cfg := setupTestConfig(t, panel)
	cfg.Output = panel.render.OutputWriter()
	answer := startConnectSetup(t, cfg, sub)
	waitForPanelText(panel.render, "Fetching this account's available models")
	// The failure releases the fetch page and asks the retry question.
	waitForPanelText(panel.render, "Retry without signing in again?")
	panel.typeKeys(t, "y")
	waitForPanelText(panel.render, "Choose the models to add")
	panel.finish(t, answer)
	if sub.startCalls != 0 {
		t.Fatalf("sign-ins = %d, want the retry to reuse the saved login", sub.startCalls)
	}
	if sub.offerCalls != 2 {
		t.Fatalf("listing calls = %d, want exactly one retry", sub.offerCalls)
	}
	if text := transcriptText(panel.render); text != "" {
		t.Fatalf("transcript = %q, want the fetch state kept out of it", text)
	}
}
