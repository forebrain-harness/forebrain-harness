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
	"strconv"
	"strings"
	"testing"

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

	offers      []subscription.Offer
	offersFn    func(call int) ([]subscription.Offer, error)
	provisionFn func(call int) error

	probeCalls  int
	startCalls  int
	startedFlow string
	offerCalls  int
	provisioned [][]subscription.Offer
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

func (s *fakeSubscription) Provision(_ context.Context, env subscription.Env, offers []subscription.Offer, _ subscription.ProvisionOptions) ([]string, error) {
	s.envs = append(s.envs, env)
	s.provisioned = append(s.provisioned, offers)
	if s.provisionFn != nil {
		if err := s.provisionFn(len(s.provisioned)); err != nil {
			return nil, err
		}
	}
	ids := make([]string, len(offers))
	for i := range offers {
		ids[i] = s.id + "-row-" + strconv.Itoa(i)
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
	if !strings.HasSuffix(rows[0].item.Description, "· Signed in") {
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

// The device code is the whole page: the address to open and the code to confirm.
// A device flow opens no browser — the plan's browser-opened device flow arrives
// with the first key-backed plan, not here.
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
	if len(*opened) != 0 {
		t.Fatalf("opened %v, want no browser for a device code", *opened)
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
	cfg := setupTestConfig(t, &fakeSelector{confirms: []bool{true}, multiSelects: [][]int{{0}, {1}}})
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
	baseURL  string
	protocol string
	key      string
	model    string
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

// withCustomAnswers scripts a custom run's answers onto a config, from the
// /connect first page: the provider list's «Custom model» row, then the
// protocol, the address, the key and the model name.
func withCustomAnswers(cfg PromptConfig, answers customAnswers) PromptConfig {
	selector := cfg.Selector.(*fakeSelector)
	selector.inputs = append([]string{answers.baseURL, answers.key, answers.model}, selector.inputs...)
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
				Label:           "Standard · China",
				Hint:            "dashscope.aliyuncs.com",
				Provider:        "alibaba",
				DefaultBaseURL:  "https://dashscope.aliyuncs.com/compatible-mode/v1",
				SupportsAPIPath: true,
				APIKeyLabel:     "Qwen API key",
			},
			{
				Group:           "Qwen Cloud",
				Label:           "Standard · Global",
				Hint:            "dashscope-intl.aliyuncs.com",
				Provider:        "alibaba",
				DefaultBaseURL:  "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
				SupportsAPIPath: true,
				APIKeyLabel:     "Qwen API key",
			},
			{
				Group:           "Qwen Cloud",
				Label:           "Coding Plan · China",
				Hint:            "coding.dashscope.aliyuncs.com",
				Provider:        "alibaba",
				DefaultBaseURL:  "https://coding.dashscope.aliyuncs.com/v1",
				SupportsAPIPath: true,
				APIKeyLabel:     "Qwen API key",
			},
			{
				Group:           "Qwen Cloud",
				Label:           "Coding Plan · Global",
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
