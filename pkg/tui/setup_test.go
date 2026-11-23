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

// The startup config error names the effective default entry — and only it.
// The llm_providers fallback the sentence used to describe is retired with that
// config key (plan 315), so naming it told the reader to edit something the
// product no longer has; copy that outlives the feature it describes is the
// defect, not a follow-up (owner 2026-11-17).
func TestStartupConfigErrorNamesTheEffectiveDefaultEntryOnly(t *testing.T) {
	const cfgPath = "/home/someone/.forebrain/forebrain.yaml"
	msg := formatStartupConfigError("/home/someone", cfgPath, []string{"model", "api_key"})
	if !strings.Contains(msg, "set the missing fields (model, api_key)") {
		t.Fatalf("diagnostic must name the missing fields it was given, got %q", msg)
	}
	if !strings.Contains(msg, "on the effective default entry — agents.definitions.main.default_model's target — in "+cfgPath) {
		t.Fatalf("diagnostic must name the effective default entry and the config it lives in, got %q", msg)
	}
	if strings.Contains(msg, "llm_providers") {
		t.Fatalf("diagnostic names the retired llm_providers key, got %q", msg)
	}
	// The sentence is one clause with an em-dash aside; the middle dot the
	// product retired from its copy is not part of it either.
	if strings.Contains(msg, "·") {
		t.Fatalf("diagnostic carries a middle dot, got %q", msg)
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
	// The section validator is process-wide and a previous test may have left
	// one bound to its own home.
	state.SetSectionValidator(nil)
	t.Cleanup(func() { state.SetSectionValidator(nil) })
	home := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-test")
	cfg := "agents:\n  definitions:\n    main:\n      primary: true\n      default_model: llm-main\n"
	cfgPath := filepath.Join(home, "forebrain.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// The model itself is a row of the table (plan 315): /connect stores it
	// there, and the completeness check reads it back from the composed
	// configuration.
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatalf("state dir: %v", err)
	}
	seedDB, err := state.OpenStateFromHome(context.Background(), home, "")
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	if _, err := (&state.LLMModelStore{DB: seedDB}).Create(context.Background(), state.LLMModel{
		ID: "llm-main", Provider: "openai", Model: "gpt-5",
		BaseURL: "https://api.openai.com/v1", APIPath: "/chat/completions", APIKey: "sk-test",
	}); err != nil {
		t.Fatalf("store the model row: %v", err)
	}
	// The definitions live in the database too (plan 187): the composed
	// configuration is what the check reads, and the row is what it resolves.
	if err := (&state.ConfigStore{DB: seedDB}).UpdateSection(context.Background(), "agents", func(json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"definitions":{"main":{"primary":true,"default_model":"llm-main"}}}`), nil
	}); err != nil {
		t.Fatalf("store the agents section: %v", err)
	}
	if err := seedDB.Close(); err != nil {
		t.Fatalf("close state: %v", err)
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
	// Write the rows the way a real subscription's provisionRows does — the
	// capabilities its listing declared, the official default effort included —
	// so the ids name rows that exist and a test can prove the user's answer
	// lands over the official one (plan 311). A login-backed row holds no key; a
	// key-backed plan carries the one the user pasted (plan 281).
	ids := make([]string, len(offers))
	key := strings.TrimSpace(opts.Key)
	for i, offer := range offers {
		input := process.LLMModelInput{
			Provider:   offer.Provider,
			Model:      offer.Model,
			BaseURL:    offer.BaseURL,
			APIPath:    offer.APIPath,
			APIKey:     &key,
			Source:     s.id,
			SourcePlan: offer.Plan,
		}
		if levels, off := splitOfferEfforts(offer.Efforts); levels != nil || off {
			if levels != nil {
				input.CapEfforts = &levels
			}
			value := off
			if offer.ReasoningOff != nil {
				value = *offer.ReasoningOff
			}
			input.CapReasoningOff = &value
		}
		officialDefault := strings.TrimSpace(offer.DefaultEffort)
		input.CapDefaultEffort = &officialDefault
		id, err := process.UpsertLLMModel(context.Background(), env.DB, input)
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

// splitOfferEfforts separates the levels a listing names from the off switch it
// may also name, the rule pkg/subscription's own Provision follows: "none" is a
// capability the row keeps in its own column, never a level.
func splitOfferEfforts(efforts []string) ([]string, bool) {
	if efforts == nil {
		return nil, false
	}
	levels := make([]string, 0, len(efforts))
	off := false
	for _, effort := range efforts {
		level := strings.ToLower(strings.TrimSpace(effort))
		switch level {
		case "":
			continue
		case "none":
			off = true
			continue
		}
		levels = append(levels, level)
	}
	return levels, off
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
	last := rows[len(rows)-1]
	if last.item.Label != "Custom model" || last.choice.Provider != appcfg.CustomProvider || !last.choice.Custom {
		t.Fatalf("custom row = %+v", last)
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
	if rows[0].item.Status != "Signed in" {
		t.Fatalf("signed-in row = %+v, want the badge", rows[0].item)
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
// endpoint each one sends to, and writes only the model the user chose — one
// flow configures one model, and the rows it already held keep everything they
// had (plan 311).
func TestConfigureSubscriptionAddsTheModelsTheUserTicks(t *testing.T) {
	sub := chatGPTLike()
	opened := browserMachine(t)
	selector := &fakeSelector{confirms: []bool{true}, selects: []string{"gpt-second", reasoningEffortUnset, subagentsNo}}
	cfg := setupTestConfig(t, selector)
	// The other model was configured earlier and must not be written again.
	otherID := offerRowSeed(t, cfg, sub, sub.offers[0], "sk-earlier-row")
	opts, ok, err := runSubscriptionSetup(t, cfg, sub)
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
		t.Fatalf("provisioned %+v, want the one model chosen", sub.provisioned)
	}
	if sub.envs[0].DB == nil || sub.envs[0].Home == "" {
		t.Fatalf("provision env = %+v, want the home and its state database", sub.envs[0])
	}
	if opts.Provider != "chatgpt" || opts.Model != "gpt-second" || opts.BaseURL != openai.CodexBaseURL || opts.APIPath != "/responses" {
		t.Fatalf("options = %+v", opts)
	}
	// The model page names each model and nothing is ticked: one row is chosen,
	// not a batch.
	if got := selector.selectOptions[0]; !reflect.DeepEqual(got, []string{"gpt-future", "gpt-second"}) {
		t.Fatalf("offer rows = %#v", got)
	}
	// It opens on the row the registry marks as the plan's default.
	if got := selector.selectDefaults[0]; got != "gpt-future" {
		t.Fatalf("open row = %q, want the plan's default", got)
	}
	if !strings.HasPrefix(selector.selectLabels[0], "ChatGPT\nChoose the model to add; each row shows where requests go.") {
		t.Fatalf("offers page title = %q", selector.selectLabels[0])
	}
	// The earlier row is exactly as it was: same key, same model, same source.
	other := readRow(t, cfg, otherID)
	if other.Model != "gpt-future" || other.APIKey != "sk-earlier-row" || other.Source != sub.id {
		t.Fatalf("the other row = %+v, want it untouched", other)
	}
}

// The model page calls each model what the vendor calls it, falling back to the
// wire id only where the listing named none. What is written is still the wire
// id: a display name never becomes a model.
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
	selector := &fakeSelector{selects: []string{"Qwen3.8-Max", reasoningEffortUnset, subagentsNo}}
	opts, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	want := []string{"Auto", "Qwen3.8-Max", "dmodel"}
	if got := selector.selectOptions[0]; !reflect.DeepEqual(got, want) {
		t.Fatalf("offer rows = %#v, want %#v", got, want)
	}
	// What is written is still the wire id: a display name never becomes a model.
	if opts.Model != "qmodel_38max" || len(sub.provisioned) != 1 || sub.provisioned[0][0].Model != "qmodel_38max" {
		t.Fatalf("options = %+v provisioned = %+v", opts, sub.provisioned)
	}
}

// A sign-in page whose answer the probe already gave is not a step: the flow is
// the list of sign-ins and the list of models, so Esc on the models goes back to
// the page that asks. Only a page that will ask for a sign-in stays in the flow.
func TestSubscriptionFlowDropsSettledSignInPage(t *testing.T) {
	settledPages := []int{setupPageProvider, setupPageOffers, setupPageEffort, setupPageSubagents, setupPageReview}
	threePages := []int{setupPageProvider, setupPageCredentials, setupPageOffers, setupPageEffort, setupPageSubagents, setupPageReview}
	for _, tc := range []struct {
		name  string
		check *turn.SignInCheck
		want  []int
	}{
		{name: "a login that lists its models", check: &turn.SignInCheck{State: turn.SignInUsable}, want: settledPages},
		{name: "a login whose listing failed", check: &turn.SignInCheck{State: turn.SignInUnreachable}, want: settledPages},
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
	selector := &fakeSelector{}
	opts, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub)
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if ok {
		t.Fatal("the setup saved itself after Esc on the model list")
	}
	if len(selector.selectLabels) != 2 || !strings.HasPrefix(selector.selectLabels[0], "Qoder CN\nCho") {
		t.Fatalf("pages drawn = %#v, want the model list once", selector.selectLabels)
	}
	if !strings.HasPrefix(selector.selectLabels[1], "Connect a model") {
		t.Fatalf("the page Esc landed on = %#v, want the list of sign-ins", selector.selectLabels[1])
	}
	if len(selector.multiLabels) != 0 {
		t.Fatalf("check lists = %#v, one model is chosen from a list, not ticked", selector.multiLabels)
	}
	if sub.startCalls != 0 {
		t.Fatalf("sign-ins = %d, a login this machine holds must not sign in again", sub.startCalls)
	}
	if len(sub.provisioned) != 0 {
		t.Fatalf("provisioned %+v, Esc writes nothing", sub.provisioned)
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
	selector := &fakeSelector{selects: []string{"gpt-future", reasoningEffortUnset, subagentsNo}}
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
	selector := &fakeSelector{confirms: []bool{true}, selects: []string{"gpt-future", reasoningEffortUnset, subagentsNo}}
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
	selector := &fakeSelector{confirms: []bool{true}, selects: []string{"gpt-future", reasoningEffortUnset, subagentsNo}}
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
	selector := &fakeSelector{confirms: []bool{true, true}, selects: []string{"gpt-future", reasoningEffortUnset, subagentsNo}}
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
	selector := &fakeSelector{confirms: []bool{true}, inputs: []string{pasted}, selects: []string{"gpt-future", reasoningEffortUnset, subagentsNo}}
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
	selector := &fakeSelector{
		confirms: []bool{true},
		inputs:   []string{"http://localhost/wrong", "http://127.0.0.1:54623/auth/callback?code=abc"},
		selects:  []string{"gpt-future", reasoningEffortUnset, subagentsNo},
	}
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
	cfg := setupTestConfig(t, &fakeSelector{confirms: []bool{true}, selects: []string{"glm-5.3", reasoningEffortUnset, subagentsNo}})
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
	selector := &fakeSelector{
		confirms: []bool{true},
		inputs:   []string{pasted},
		selects:  []string{"doubao-seed-2.1-pro", reasoningEffortUnset, subagentsNo},
	}
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
	selector := &fakeSelector{
		confirms: []bool{true},
		inputs:   []string{pasted},
		selects:  []string{"doubao-seed-2.1-pro", reasoningEffortUnset, subagentsNo},
	}
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
// nothing; the user can then choose again. The write happens on the review's
// save, after the confirmation, and a refusal returns to the model list with the
// login and the listing kept (plan 311).
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
	cfg := setupTestConfig(t, &fakeSelector{
		confirms: []bool{true},
		selects: []string{
			"gpt-future", reasoningEffortUnset, subagentsNo,
			"gpt-second", reasoningEffortUnset, subagentsNo,
		},
	})
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
		t.Fatalf("model = %q, want the model chosen on the second attempt", opts.Model)
	}
	if sub.startCalls != 1 {
		t.Fatalf("sign-ins = %d, a refused write must not sign in again", sub.startCalls)
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
		fakeSelector: &fakeSelector{confirms: []bool{true}, selects: []string{"glm-5.3", reasoningEffortUnset, subagentsNo}},
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
		"Provider":                  appcfg.CustomProvider,
		"Model":                     "gpt-4o-vendor",
		"Protocol":                  "OpenAI Chat Completions",
		"Endpoint":                  "https://api.example.com/v1/chat/completions",
		"Supported thinking levels": "medium, high",
		"Default reasoning effort":  reasoningEffortUnset,
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
	if facts["Supported thinking levels"] != "Not set" {
		t.Fatalf("review levels = %q, want Not set", facts["Supported thinking levels"])
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

// TestConfigureEffortPageUsesTheModelsLevels pins /connect to the same levels
// /model offers: the model's own catalog row, plus the "Provider default" escape
// hatch, and no guessed level for a model whose row names none (plan 311: the
// page asks every model, and a model with nothing to pick is offered the
// provider's own default alone).
func TestConfigureEffortPageUsesTheModelsLevels(t *testing.T) {
	installRepoCatalog(t)
	for _, tc := range []struct {
		provider, model string
		baseURL         string
		want            []string
	}{
		{
			provider: "openai", model: "gpt-5", baseURL: "https://api.openai.com/v1",
			want: []string{"Minimal", "Low", "Medium", "High", reasoningEffortUnset},
		},
		{
			provider: "openai", model: "gpt-5.5", baseURL: "https://api.openai.com/v1",
			// This row's catalog entry names "none", so the off switch is part
			// of the list — first, because it is the extreme of the same scale.
			want: []string{"Off", "Low", "Medium", "High", "xHigh", reasoningEffortUnset},
		},
		{
			provider: "zhipuai", model: "glm-5.3-flash", baseURL: "https://api.z.ai/api/paas/v4",
			want: []string{"Low", "High", "Max", reasoningEffortUnset},
		},
		{
			// A toggle-only model: nothing discrete to choose, so the page is the
			// provider's own default.
			provider: "zhipuai", model: "glm-4.5", baseURL: "https://api.z.ai/api/paas/v4",
			want: []string{reasoningEffortUnset},
		},
		{
			// Not in the catalog at all: the same answer.
			provider: "openai", model: "not-a-model", baseURL: "https://api.openai.com/v1",
			want: []string{reasoningEffortUnset},
		},
	} {
		t.Run(tc.provider+"/"+tc.model, func(t *testing.T) {
			cfg := setupTestConfig(t, &fakeSelector{})
			setup := apiKeyEffortSetup(cfg, tc.provider, tc.model, tc.baseURL)
			defer setup.closeState()
			if ok, err := setup.promptDefaultEffort(); err != nil || !ok {
				t.Fatalf("promptDefaultEffort ok=%t err=%v", ok, err)
			}
			selector := cfg.Selector.(*fakeSelector)
			if len(selector.selectOptions) != 1 {
				t.Fatalf("pages = %#v, want one default-effort page", selector.selectOptions)
			}
			if !reflect.DeepEqual(selector.selectOptions[0], tc.want) {
				t.Fatalf("options = %#v, want %#v", selector.selectOptions[0], tc.want)
			}
			// No level is guessed: a fresh row starts on the provider's own
			// default, whatever the model offers.
			if got := selector.selectDefaults[0]; got != reasoningEffortUnset {
				t.Fatalf("cursor = %q, want the provider default", got)
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
	// endpoints.
	chatGPTRow := subscriptionChoice(chatGPTLike())
	if chatGPTRow.Provider != "chatgpt" {
		t.Fatalf("generated chatgpt row = %+v", chatGPTRow)
	}
	openCodeRow := subscriptionChoice(openCodeLike())
	if openCodeRow.Provider != "opencode-go" {
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
		confirms: []bool{true},
		selects:  []string{"gpt-second", reasoningEffortUnset, subagentsYes},
	})
	opts, ok, err := runSubscriptionSetup(t, cfg, sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if len(sub.provisioned) != 1 || len(sub.provisioned[0]) != 1 {
		t.Fatalf("provisioned = %#v, want one write of the one model chosen", sub.provisioned)
	}
	// The switch is one page of its own now, not a check list of models.
	selector := cfg.Selector.(*fakeSelector)
	if len(selector.multiLabels) != 0 {
		t.Fatalf("check lists = %#v, the model and the authorization are single choices", selector.multiLabels)
	}
	subagentPage := false
	for _, label := range selector.selectLabels {
		if strings.HasPrefix(label, "Subagents\n") {
			subagentPage = true
		}
	}
	if !subagentPage {
		t.Fatalf("pages = %#v, want the subagent authorization page", selector.selectLabels)
	}
	// The rows the sign-in wrote are the model's; the switch, the default and
	// the session's model are the caller's own Execute (plan 311).
	if opts.Model != "gpt-second" || !opts.SubagentAllowed {
		t.Fatalf("options = %+v, want the chosen model with this flow's answer", opts)
	}
	rt := process.Context{Home: cfg.Home, ConfigPath: cfg.ConfigPath, Config: appcfg.Root{}}
	rep, err := process.Execute(rt, opts)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	row := readRow(t, cfg, rep.ModelID)
	if !row.SubagentAllowed {
		t.Fatalf("row = %+v, want this flow's answer saved onto it", row)
	}
	if row.Source != sub.id {
		t.Fatalf("row source = %q, want the sign-in that wrote it", row.Source)
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
	cfg := setupTestConfig(t, &fakeSelector{confirms: []bool{true}, selects: []string{"glm-5.3", reasoningEffortUnset, subagentsNo}})
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
	selector := &fakeSelector{inputs: []string{"placeholder-key"}, selects: []string{"glm-5.3", reasoningEffortUnset, subagentsNo}}
	cfg := setupTestConfig(t, selector)
	offerRowSeed(t, cfg, sub, sub.offers[1], "placeholder-plan-key")

	opts, ok, err := runSubscriptionSetup(t, cfg, sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if opts.Model != "glm-5.3" {
		t.Fatalf("options = %+v, want the model that was already added", opts)
	}
	if got := selector.selectDefaults[0]; got != "glm-5.3" {
		t.Fatalf("the page opened on %q, want the already added row", got)
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
	selector := &fakeSelector{selects: []string{"kimi-k3", reasoningEffortUnset, subagentsNo}}
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
	if got := selector.selectDefaults[0]; got != "kimi-k3" {
		t.Fatalf("the page opened on %q, want the row a legacy path wrote", got)
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
	selector := &setupWaitSelector{fakeSelector: &fakeSelector{confirms: []bool{true}, selects: []string{"glm-5.3", reasoningEffortUnset, subagentsNo}}}
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
	selector := &setupWaitSelector{fakeSelector: &fakeSelector{confirms: []bool{true}, selects: []string{"glm-5.3", reasoningEffortUnset, subagentsNo}}}
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
	opts, ok, err := runSubscriptionSetup(t, setupTestConfig(t, &fakeSelector{
		confirms: []bool{true},
		selects:  []string{"glm-5.3", reasoningEffortUnset, subagentsNo},
	}), sub)
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
	"y to confirm n or enter to decline",
	"y or enter to confirm n to decline",
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
	cfg := setupTestConfig(t, &failingOverlaySelector{
		fakeSelector: &fakeSelector{selects: []string{"glm-5.3", reasoningEffortUnset, subagentsNo}},
		err:          paintErr,
	})
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

// installEffortTestCatalog pins the catalog to the models the default-effort
// page's cases need: one with discrete levels and no off switch, one that can
// turn thinking off, one a source lists nothing for, and — for the custom form —
// one whose levels only survive on a protocol that can send them.
func installEffortTestCatalog(t *testing.T) {
	t.Helper()
	cat, err := llm.Parse([]byte(`{
		"openai/levels-model": {"id": "openai/levels-model", "reasoning": true,
			"reasoning_options": [{"type": "effort", "values": ["low", "medium", "high"]}]},
		"openai/off-model": {"id": "openai/off-model", "reasoning": true,
			"reasoning_options": [{"type": "effort", "values": ["none", "low", "medium"]}]},
		"openai/toggle-model": {"id": "openai/toggle-model", "reasoning": true,
			"reasoning_options": []},
		"custom/messages-model": {"id": "custom/messages-model", "reasoning": true,
			"reasoning_options": [{"type": "effort", "values": ["none", "minimal", "low", "medium"]}]},
		"anthropic/claude-off-model": {"id": "anthropic/claude-off-model", "reasoning": true,
			"reasoning_options": [{"type": "effort", "values": ["none", "low", "high"]}]}
	}`))
	if err != nil {
		t.Fatalf("parse effort test catalog: %v", err)
	}
	llm.SetGlobalCatalogForTest(cat)
	t.Cleanup(func() { llm.SetGlobalCatalogForTest(cat) })
}

// readRow reads one stored model row back from the setup's own database: the
// row is the proof a save landed, not the options it was given.
func readRow(t *testing.T, cfg PromptConfig, id string) state.LLMModel {
	t.Helper()
	db, err := process.StateDB(process.Context{Home: cfg.Home, ConfigPath: cfg.ConfigPath})
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	defer db.Close()
	row, err := (&state.LLMModelStore{DB: db}).Get(context.Background(), id)
	if err != nil {
		t.Fatalf("read row %q: %v", id, err)
	}
	return row
}

// seedEffortRow writes the row an earlier configuration would have left.
func seedEffortRow(t *testing.T, cfg PromptConfig, in process.LLMModelInput) {
	t.Helper()
	db, err := process.StateDB(process.Context{Home: cfg.Home, ConfigPath: cfg.ConfigPath})
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	defer db.Close()
	if _, err := process.UpsertLLMModel(context.Background(), db, in); err != nil {
		t.Fatalf("seed the row: %v", err)
	}
}

// apiKeyEffortSetup is the API-key group standing on its model page: the
// provider, the model and the endpoint a save would write.
func apiKeyEffortSetup(cfg PromptConfig, provider, model, baseURL string) *llmSetup {
	setup := newLLMSetup(cfg)
	setup.choice = providerChoice{Label: provider + " API key", Provider: provider, DefaultBaseURL: baseURL, SupportsAPIPath: true}
	setup.model, setup.baseURL = model, baseURL
	return setup
}

// subscriptionEffortSetup is the sign-in group with one model chosen from its
// listing.
func subscriptionEffortSetup(t *testing.T, cfg PromptConfig, offer subscription.Offer) *llmSetup {
	t.Helper()
	sub := &fakeSubscription{
		id: "chatgpt", label: "ChatGPT",
		check:  turn.SignInCheck{State: turn.SignInUsable},
		offers: []subscription.Offer{offer},
	}
	withSubscriptions(t, sub)
	setup := newLLMSetup(cfg)
	setup.setChoice(subscriptionChoice(sub))
	setup.offers = []subscription.Offer{offer}
	setup.selectedOfferIdx = 0
	return setup
}

// The page every group passes (plan 311): its list is what the model can
// actually be given, its cursor is the truth already stored for that model and
// endpoint — the params effort a request carries before the row's own default —
// and "Provider default" is an answer like any other, not "unanswered".
func TestConnectDefaultEffortDefaultSelection(t *testing.T) {
	installEffortTestCatalog(t)

	// The page answers with the label it was scripted, and the setup records
	// the wire id behind it.
	answer := func(t *testing.T, setup *llmSetup, label string) {
		t.Helper()
		selector := setup.cfg.Selector.(*fakeSelector)
		selector.selects = append(selector.selects, label)
		if ok, err := setup.promptDefaultEffort(); err != nil || !ok {
			t.Fatalf("promptDefaultEffort ok=%t err=%v", ok, err)
		}
	}

	t.Run("a fresh row starts on the provider default", func(t *testing.T) {
		cfg := setupTestConfig(t, &fakeSelector{})
		setup := apiKeyEffortSetup(cfg, "openai", "levels-model", "https://api.openai.com/v1")
		defer setup.closeState()
		answer(t, setup, reasoningEffortUnset)
		selector := cfg.Selector.(*fakeSelector)
		if got := selector.selectOptions[0]; !reflect.DeepEqual(got, []string{"Low", "Medium", "High", reasoningEffortUnset}) {
			t.Fatalf("options = %#v", got)
		}
		if got := selector.selectDefaults[0]; got != reasoningEffortUnset {
			t.Fatalf("cursor = %q, want the provider default", got)
		}
		if !strings.HasPrefix(selector.selectLabels[0], "Default reasoning effort\nChoose the default for openai/levels-model.") {
			t.Fatalf("title = %q", selector.selectLabels[0])
		}
		// Provider default is an answer: "" here means "the provider decides",
		// not "the page was never answered".
		if !setup.effortAnswered || setup.defaultEffort != "" {
			t.Fatalf("answer = %q answered=%t", setup.defaultEffort, setup.effortAnswered)
		}
		if setup.effortIdentity != setup.effortTargetFor().key() {
			t.Fatalf("answer identity = %q, want the model's", setup.effortIdentity)
		}
	})

	t.Run("the row's params effort is where the page opens", func(t *testing.T) {
		cfg := setupTestConfig(t, &fakeSelector{})
		params := `{"reasoning":{"effort":"high"}}`
		seedEffortRow(t, cfg, process.LLMModelInput{
			Provider: "openai", Model: "levels-model", BaseURL: "https://api.openai.com/v1",
			APIKey: strPtrTUI("sk-old"), ParamsJSON: &params,
		})
		setup := apiKeyEffortSetup(cfg, "openai", "levels-model", "https://api.openai.com/v1")
		defer setup.closeState()
		answer(t, setup, "Medium")
		selector := cfg.Selector.(*fakeSelector)
		if got := selector.selectDefaults[0]; got != "High" {
			t.Fatalf("cursor = %q, want the stored effort", got)
		}
		if setup.defaultEffort != "medium" {
			t.Fatalf("answer = %q, want medium", setup.defaultEffort)
		}
	})

	t.Run("a stored effort the model no longer offers falls back with a note", func(t *testing.T) {
		cfg := setupTestConfig(t, &fakeSelector{})
		params := `{"reasoning":{"effort":"xhigh"}}`
		seedEffortRow(t, cfg, process.LLMModelInput{
			Provider: "openai", Model: "levels-model", BaseURL: "https://api.openai.com/v1",
			APIKey: strPtrTUI("sk-old"), ParamsJSON: &params,
		})
		setup := apiKeyEffortSetup(cfg, "openai", "levels-model", "https://api.openai.com/v1")
		defer setup.closeState()
		answer(t, setup, reasoningEffortUnset)
		selector := cfg.Selector.(*fakeSelector)
		if got := selector.selectDefaults[0]; got != reasoningEffortUnset {
			t.Fatalf("cursor = %q, want the provider default", got)
		}
		if title := selector.selectLabels[0]; !strings.Contains(title, "xHigh is no longer available for this model") {
			t.Fatalf("title = %q, want the note about the stale level", title)
		}
	})

	t.Run("the row's own default effort is read when its params name none", func(t *testing.T) {
		cfg := setupTestConfig(t, &fakeSelector{})
		seedEffortRow(t, cfg, process.LLMModelInput{
			Provider: "openai", Model: "levels-model", BaseURL: "https://api.openai.com/v1",
			APIKey: strPtrTUI("sk-old"), CapDefaultEffort: strPtrTUI("low"),
		})
		setup := apiKeyEffortSetup(cfg, "openai", "levels-model", "https://api.openai.com/v1")
		defer setup.closeState()
		answer(t, setup, "Low")
		if got := cfg.Selector.(*fakeSelector).selectDefaults[0]; got != "Low" {
			t.Fatalf("cursor = %q, want the row's default effort", got)
		}
	})

	t.Run("stepping back shows this flow's own answer", func(t *testing.T) {
		cfg := setupTestConfig(t, &fakeSelector{})
		setup := apiKeyEffortSetup(cfg, "openai", "levels-model", "https://api.openai.com/v1")
		defer setup.closeState()
		answer(t, setup, "High")
		answer(t, setup, "High")
		selector := cfg.Selector.(*fakeSelector)
		if got := selector.selectDefaults[1]; got != "High" {
			t.Fatalf("cursor on the page shown again = %q, want this flow's answer", got)
		}
	})

	t.Run("picking Off records none, which is not the provider default", func(t *testing.T) {
		cfg := setupTestConfig(t, &fakeSelector{})
		setup := apiKeyEffortSetup(cfg, "openai", "off-model", "https://api.openai.com/v1")
		defer setup.closeState()
		answer(t, setup, "Off")
		selector := cfg.Selector.(*fakeSelector)
		if got := selector.selectOptions[0]; !reflect.DeepEqual(got, []string{"Off", "Low", "Medium", reasoningEffortUnset}) {
			t.Fatalf("options = %#v, want the off switch first", got)
		}
		if setup.defaultEffort != "none" {
			t.Fatalf("answer = %q, want none", setup.defaultEffort)
		}
	})

	t.Run("a row that says the model cannot turn thinking off hides Off", func(t *testing.T) {
		cfg := setupTestConfig(t, &fakeSelector{})
		off := false
		seedEffortRow(t, cfg, process.LLMModelInput{
			Provider: "openai", Model: "off-model", BaseURL: "https://api.openai.com/v1",
			APIKey: strPtrTUI("sk-old"), CapReasoningOff: &off,
		})
		setup := apiKeyEffortSetup(cfg, "openai", "off-model", "https://api.openai.com/v1")
		defer setup.closeState()
		answer(t, setup, "Low")
		if got := cfg.Selector.(*fakeSelector).selectOptions[0]; !reflect.DeepEqual(got, []string{"Low", "Medium", reasoningEffortUnset}) {
			t.Fatalf("options = %#v, want no Off", got)
		}
	})

	t.Run("a model nothing lists gets the provider default alone", func(t *testing.T) {
		cfg := setupTestConfig(t, &fakeSelector{})
		setup := apiKeyEffortSetup(cfg, "openai", "toggle-model", "https://api.openai.com/v1")
		defer setup.closeState()
		answer(t, setup, reasoningEffortUnset)
		selector := cfg.Selector.(*fakeSelector)
		if got := selector.selectOptions[0]; !reflect.DeepEqual(got, []string{reasoningEffortUnset}) {
			t.Fatalf("options = %#v, want the provider default alone", got)
		}
		if title := selector.selectLabels[0]; !strings.Contains(title, "No discrete reasoning levels are available for this model.") {
			t.Fatalf("title = %q, want the sentence for a model with no levels", title)
		}
	})

	t.Run("a custom model on Messages is offered only what that client sends", func(t *testing.T) {
		cfg := setupTestConfig(t, &fakeSelector{})
		setup := newLLMSetup(cfg)
		setup.setChoice(customChoice())
		setup.model, setup.baseURL = "messages-model", "https://api.example.test"
		setup.apiPath = appcfg.APIPathAnthropicMessages
		defer setup.closeState()
		answer(t, setup, "Low")
		selector := cfg.Selector.(*fakeSelector)
		// The catalog names none and minimal; the Messages client sends
		// neither, so Off is not offered and minimal is not either.
		if got := selector.selectOptions[0]; !reflect.DeepEqual(got, []string{"Low", "Medium", reasoningEffortUnset}) {
			t.Fatalf("options = %#v", got)
		}
		if !strings.Contains(selector.selectLabels[0], "Choose the default for custom/messages-model.") {
			t.Fatalf("title = %q, want the row's own provider", selector.selectLabels[0])
		}
	})

	t.Run("an anthropic model that can turn thinking off still gets no Off", func(t *testing.T) {
		cfg := setupTestConfig(t, &fakeSelector{})
		setup := apiKeyEffortSetup(cfg, "anthropic", "claude-off-model", "https://api.anthropic.com")
		defer setup.closeState()
		answer(t, setup, "High")
		if got := cfg.Selector.(*fakeSelector).selectOptions[0]; !reflect.DeepEqual(got, []string{"Low", "High", reasoningEffortUnset}) {
			t.Fatalf("options = %#v, want no Off for the Messages client", got)
		}
	})

	t.Run("a new subscription row opens on the listing's own default", func(t *testing.T) {
		cfg := setupTestConfig(t, &fakeSelector{})
		setup := subscriptionEffortSetup(t, cfg, subscription.Offer{
			Subscription: "chatgpt", Provider: "chatgpt", Model: "gpt-future",
			BaseURL: openai.CodexBaseURL, APIPath: "/responses",
			Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium",
		})
		defer setup.closeState()
		answer(t, setup, "High")
		selector := cfg.Selector.(*fakeSelector)
		if got := selector.selectDefaults[0]; got != "Medium" {
			t.Fatalf("cursor = %q, want the listing's default", got)
		}
		if got := selector.selectOptions[0]; !reflect.DeepEqual(got, []string{"Low", "Medium", "High", reasoningEffortUnset}) {
			t.Fatalf("options = %#v", got)
		}
	})

	t.Run("a row this install already holds keeps its own answer over the listing", func(t *testing.T) {
		cfg := setupTestConfig(t, &fakeSelector{})
		seedEffortRow(t, cfg, process.LLMModelInput{
			Provider: "chatgpt", Model: "gpt-future", BaseURL: openai.CodexBaseURL, APIPath: "/responses",
		})
		setup := subscriptionEffortSetup(t, cfg, subscription.Offer{
			Subscription: "chatgpt", Provider: "chatgpt", Model: "gpt-future",
			BaseURL: openai.CodexBaseURL, APIPath: "/responses",
			Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium",
		})
		defer setup.closeState()
		answer(t, setup, reasoningEffortUnset)
		if got := cfg.Selector.(*fakeSelector).selectDefaults[0]; got != reasoningEffortUnset {
			t.Fatalf("cursor = %q, want the provider default the row states", got)
		}
	})
}

// strPtrTUI is one string's pointer, for the inputs that take an optional value.
func strPtrTUI(value string) *string { return &value }

// boolPtrTUI is one bool's pointer, for the declarations a listing may name.
func boolPtrTUI(value bool) *bool { return &value }

// stubLLMProbeInput records the request the connection test would send, so a
// test can prove which level actually travels on the wire.
func stubLLMProbeInput(t *testing.T) *[]process.LLMModelInput {
	t.Helper()
	inputs := []process.LLMModelInput{}
	previous := probeLLMModel
	probeLLMModel = func(_ context.Context, in process.LLMModelInput) error {
		inputs = append(inputs, in)
		return nil
	}
	t.Cleanup(func() { probeLLMModel = previous })
	return &inputs
}

// effortBackSelector steps back from any page whose title it names — the
// default-effort page included, which the older backtrackSelector only drove for
// the authorization page.
type effortBackSelector struct {
	fakeSelector
	backAt map[string]int
	pages  []string
}

func (s *effortBackSelector) remember(label string) bool {
	title, _, _ := strings.Cut(label, "\n")
	s.pages = append(s.pages, title)
	if s.backAt[title] > 0 {
		s.backAt[title]--
		return true
	}
	return false
}

func (s *effortBackSelector) Select(label string, options []string, defaultOption string) (string, bool, error) {
	if s.remember(label) {
		return "", false, nil
	}
	return s.fakeSelector.Select(label, options, defaultOption)
}

func (s *effortBackSelector) SelectRich(label string, items []SelectItem, defaultIdx int) (int, bool, error) {
	if s.remember(label) {
		return -1, false, nil
	}
	return s.fakeSelector.SelectRich(label, items, defaultIdx)
}

// SelectRichTabbed is the tabbed spelling selectRichTabbed prefers; it has to
// come back through this selector's own step-back.
func (s *effortBackSelector) SelectRichTabbed(label string, items []SelectItem, opts TabbedSelectOptions) (int, bool, error) {
	return s.SelectRich(label, items, opts.DefaultIdx)
}

func (s *effortBackSelector) MultiSelectRichTabbed(label string, items []SelectItem, opts TabbedSelectOptions) ([]int, bool, error) {
	if s.remember(label) {
		return nil, false, nil
	}
	return s.fakeSelector.MultiSelectRichTabbed(label, items, opts)
}

// Every group passes the one default-effort page (plan 311): a subscription's
// single model, a provider reached with a key, and the custom form whose
// capability page stays a multi-select. The page's list is what the model can be
// given — Off only where the model can turn thinking off *and* the client can
// send it — and Provider default is always there.
func TestConnectDefaultEffortAllGroups(t *testing.T) {
	installEffortTestCatalog(t)

	t.Run("a subscription passes the page and provisions one row", func(t *testing.T) {
		sub := chatGPTLike()
		sub.offers = []subscription.Offer{{
			Subscription: "chatgpt", Provider: "chatgpt", Model: "gpt-offered",
			BaseURL: openai.CodexBaseURL, APIPath: "/responses", PlanLabel: "ChatGPT plan", Default: true,
			Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium",
		}}
		selector := &fakeSelector{
			confirms: []bool{true},
			selects:  []string{"gpt-offered", "High", subagentsNo},
		}
		opts, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub)
		if err != nil || !ok {
			t.Fatalf("configure err=%v ok=%t", err, ok)
		}
		if len(sub.provisioned) != 1 || len(sub.provisioned[0]) != 1 || sub.provisioned[0][0].Model != "gpt-offered" {
			t.Fatalf("provisioned %+v, want exactly the one model chosen", sub.provisioned)
		}
		// The listing's levels are the page's, and it opens on the listing's
		// own default because this row is new.
		if got := selector.selectOptions[1]; !reflect.DeepEqual(got, []string{"Low", "Medium", "High", reasoningEffortUnset}) {
			t.Fatalf("effort page = %#v", got)
		}
		if got := selector.selectDefaults[1]; got != "Medium" {
			t.Fatalf("effort cursor = %q, want the listing's default", got)
		}
		if opts.CapDefaultEffort == nil || *opts.CapDefaultEffort != "high" {
			t.Fatalf("cap default = %v, want the level the user chose", opts.CapDefaultEffort)
		}
	})

	t.Run("an api key passes the same page and writes no params", func(t *testing.T) {
		selector := &fakeSelector{
			selects: []string{"OpenAI", "levels-model", "High", subagentsYes},
			inputs:  []string{"sk-live"},
		}
		opts, ok, err := PromptMainLLMSetup(setupTestConfig(t, selector))
		if err != nil || !ok {
			t.Fatalf("configure err=%v ok=%t", err, ok)
		}
		if opts.CapDefaultEffort == nil || *opts.CapDefaultEffort != "high" {
			t.Fatalf("cap default = %v, want high", opts.CapDefaultEffort)
		}
		if opts.UpdateParams || opts.ParamsJSON != "" {
			t.Fatalf("params = %q (update=%t), want the save to write none", opts.ParamsJSON, opts.UpdateParams)
		}
		effortPage := false
		for _, label := range selector.selectLabels {
			if strings.HasPrefix(label, "Default reasoning effort\nChoose the default for openai/levels-model.") {
				effortPage = true
			}
		}
		if !effortPage {
			t.Fatalf("pages = %#v, want the default-effort page", selector.selectLabels)
		}
	})

	t.Run("a custom model keeps its capability page and declares Off there", func(t *testing.T) {
		cfg := setupTestConfig(t, &fakeSelector{
			confirms: []bool{false},
			// minimal, low and the seventh item Off (plan 311).
			multiSelects: [][]int{{0, 1, 6}},
			selects:      []string{"Off"},
		})
		opts, ok, err := PromptMainLLMSetup(withCustomAnswers(cfg, customAnswers{
			baseURL: "https://api.example.com/v1", protocol: "OpenAI Chat Completions",
			key: "sk-vendor", model: "gpt-4o-vendor",
		}))
		if err != nil || !ok {
			t.Fatalf("configure err=%v ok=%t", err, ok)
		}
		if !reflect.DeepEqual(opts.CapEfforts, []string{"minimal", "low"}) {
			t.Fatalf("levels = %#v, want the six-level ticks and no none", opts.CapEfforts)
		}
		if opts.CapReasoningOff == nil || !*opts.CapReasoningOff {
			t.Fatalf("reasoning off = %v, want the seventh item's own bool", opts.CapReasoningOff)
		}
		if opts.CapDefaultEffort == nil || *opts.CapDefaultEffort != "none" {
			t.Fatalf("cap default = %v, want none", opts.CapDefaultEffort)
		}
		// The capability page is still a multi-select: that is a declaration of
		// what the model takes, not a choice of models.
		if len(cfg.Selector.(*fakeSelector).multiLabels) != 1 {
			t.Fatalf("capability pages = %#v, want one", cfg.Selector.(*fakeSelector).multiLabels)
		}
	})

	t.Run("a model with nothing to pick still gets the page", func(t *testing.T) {
		selector := &fakeSelector{selects: []string{"OpenAI", "toggle-model", reasoningEffortUnset, subagentsNo}, inputs: []string{"sk-live"}}
		opts, ok, err := PromptMainLLMSetup(setupTestConfig(t, selector))
		if err != nil || !ok {
			t.Fatalf("configure err=%v ok=%t", err, ok)
		}
		var effortOptions []string
		var title string
		for i, label := range selector.selectLabels {
			if strings.HasPrefix(label, "Default reasoning effort") {
				effortOptions, title = selector.selectOptions[i], label
			}
		}
		if !reflect.DeepEqual(effortOptions, []string{reasoningEffortUnset}) {
			t.Fatalf("effort page = %#v, want the provider default alone", effortOptions)
		}
		if !strings.Contains(title, "No discrete reasoning levels are available for this model.") {
			t.Fatalf("page = %q, want the sentence for a model with no levels", title)
		}
		if opts.CapDefaultEffort == nil || *opts.CapDefaultEffort != "" {
			t.Fatalf("cap default = %v, want an explicit provider default", opts.CapDefaultEffort)
		}
	})

	// Off is three states, not a toggle (plan 311): the model must be able to
	// turn thinking off, and the client that serves it must be able to send it.
	for _, tc := range []struct {
		name     string
		provider string
		model    string
		want     []string
	}{
		{
			name: "off-capable on a protocol that sends it", provider: "OpenAI", model: "off-model",
			want: []string{"Off", "Low", "Medium", reasoningEffortUnset},
		},
		{
			name: "no off capability", provider: "OpenAI", model: "levels-model",
			want: []string{"Low", "Medium", "High", reasoningEffortUnset},
		},
		{
			name: "off-capable but the Messages client cannot send it", provider: "Anthropic", model: "claude-off-model",
			want: []string{"Low", "High", reasoningEffortUnset},
		},
	} {
		t.Run("Off: "+tc.name, func(t *testing.T) {
			selector := &fakeSelector{selects: []string{tc.provider, tc.model, reasoningEffortUnset, subagentsNo}, inputs: []string{"sk-live"}}
			cfg := setupTestConfig(t, selector)
			if _, ok, err := PromptMainLLMSetup(cfg); err != nil || !ok {
				t.Fatalf("configure err=%v ok=%t", err, ok)
			}
			for i, label := range selector.selectLabels {
				if strings.HasPrefix(label, "Default reasoning effort") {
					if got := selector.selectOptions[i]; !reflect.DeepEqual(got, tc.want) {
						t.Fatalf("effort page = %#v, want %#v", got, tc.want)
					}
					return
				}
			}
			t.Fatalf("pages = %#v, want the default-effort page", selector.selectLabels)
		})
	}
}

// Esc walks back one page at a time and writes nothing: from the level to the
// model (or the custom capability page), from the authorization to the level, and
// from the model list to the sign-ins. Changing what the level belongs to asks it
// again; going back to the same one keeps the answer.
func TestConnectDefaultEffortNavigation(t *testing.T) {
	installEffortTestCatalog(t)

	t.Run("Esc on the level returns to the model page", func(t *testing.T) {
		selector := &effortBackSelector{
			fakeSelector: fakeSelector{
				selects: []string{"OpenAI", "levels-model"},
				inputs:  []string{"sk-live"},
			},
			backAt: map[string]int{"Default reasoning effort": 1},
		}
		cfg := setupTestConfig(t, selector)
		if _, _, err := PromptMainLLMSetup(cfg); err != nil {
			t.Fatalf("configure: %v", err)
		}
		if got := strings.Join(selector.pages, " → "); !strings.Contains(got, "Model → Default reasoning effort → Model") {
			t.Fatalf("pages = %s, want the level's Esc to land on the model page", got)
		}
	})

	t.Run("Esc on a subscription's level returns to the model list and writes nothing", func(t *testing.T) {
		sub := chatGPTLike()
		selector := &effortBackSelector{
			fakeSelector: fakeSelector{confirms: []bool{true}, selects: []string{"gpt-future"}},
			backAt:       map[string]int{"Default reasoning effort": 1},
		}
		cfg := setupTestConfig(t, selector)
		opts, ok, err := runSubscriptionSetup(t, cfg, sub)
		if err != nil {
			t.Fatalf("configure: %v", err)
		}
		if ok || opts.Provider != "" {
			t.Fatalf("ok=%t options=%+v, Esc writes nothing", ok, opts)
		}
		if len(sub.provisioned) != 0 {
			t.Fatalf("provisioned %+v, nothing is written before the review", sub.provisioned)
		}
		if got := strings.Join(selector.pages, " → "); !strings.Contains(got, "ChatGPT → Default reasoning effort → ChatGPT") {
			t.Fatalf("pages = %s, want the level's Esc to land on the model list", got)
		}
	})

	t.Run("a custom model's level Esc goes to the capability page", func(t *testing.T) {
		selector := &effortBackSelector{
			fakeSelector: fakeSelector{
				confirms:     []bool{false},
				multiSelects: [][]int{{1}},
				selects:      []string{"Custom model", "OpenAI Chat Completions"},
				inputs:       []string{"https://api.example.com/v1", "sk-vendor", "gpt-4o-vendor", ""},
			},
			backAt: map[string]int{"Default reasoning effort": 1},
		}
		cfg := setupTestConfig(t, selector)
		if _, _, err := PromptMainLLMSetup(cfg); err != nil {
			t.Fatalf("configure: %v", err)
		}
		pages := strings.Join(selector.pages, " → ")
		if !strings.Contains(pages, "Default reasoning effort → Thinking levels this model supports") {
			t.Fatalf("pages = %s, want the level's Esc to land on the capability page", pages)
		}
	})

	t.Run("changing the model asks the level again with that model's own list", func(t *testing.T) {
		selector := &effortBackSelector{
			fakeSelector: fakeSelector{
				selects: []string{"OpenAI", "levels-model", "off-model", "Off", subagentsNo},
				inputs:  []string{"sk-live"},
			},
			backAt: map[string]int{"Default reasoning effort": 1},
		}
		cfg := setupTestConfig(t, selector)
		opts, ok, err := PromptMainLLMSetup(cfg)
		if err != nil || !ok {
			t.Fatalf("configure err=%v ok=%t", err, ok)
		}
		if opts.Model != "off-model" || opts.CapDefaultEffort == nil || *opts.CapDefaultEffort != "none" {
			t.Fatalf("options = %+v, want the second model with its own answer", opts)
		}
		// The level was asked twice — once per model — and the page the second
		// model got lists that model's own levels, so nothing from the first
		// travelled. (The first page was stepped back from, so its list is not in
		// the answers the fake recorded.)
		if got := strings.Count(strings.Join(selector.pages, " → "), "Default reasoning effort"); got != 2 {
			t.Fatalf("pages = %s, want the level asked for each model", strings.Join(selector.pages, " → "))
		}
		at := -1
		for i, label := range selector.selectLabels {
			if strings.HasPrefix(label, "Default reasoning effort") {
				at = i
			}
		}
		if at < 0 || !reflect.DeepEqual(selector.selectOptions[at], []string{"Off", "Low", "Medium", reasoningEffortUnset}) {
			t.Fatalf("effort page = %#v, want the second model's own list", selector.selectOptions)
		}
		if got := selector.selectDefaults[at]; got != reasoningEffortUnset {
			t.Fatalf("second page cursor = %q, want the fresh model's default", got)
		}
	})
}

// The subscription's official default is capability data, not the user's answer:
// Provision writes it, and the level the user chose is saved over it by the same
// Execute that stores the authorization (plan 311).
func TestConnectDefaultEffortSubscriptionPersistsAfterExecute(t *testing.T) {
	sub := chatGPTLike()
	sub.offers = []subscription.Offer{{
		Subscription: "chatgpt", Provider: "chatgpt", Model: "gpt-official",
		BaseURL: openai.CodexBaseURL, APIPath: "/responses", PlanLabel: "ChatGPT plan", Default: true,
		Efforts: []string{"low", "medium", "high"}, ReasoningOff: boolPtrTUI(false), DefaultEffort: "medium",
	}}
	rt := func(cfg PromptConfig) process.Context {
		return process.Context{Home: cfg.Home, ConfigPath: cfg.ConfigPath, Config: appcfg.Root{}}
	}

	selector := &fakeSelector{confirms: []bool{true}, selects: []string{"gpt-official", "High", subagentsYes}}
	cfg := setupTestConfig(t, selector)
	opts, ok, err := runSubscriptionSetup(t, cfg, sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	rep, err := process.Execute(rt(cfg), opts)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	row := readRow(t, cfg, rep.ModelID)
	if row.CapDefaultEffort != "high" {
		t.Fatalf("cap_default_effort = %q, want the user's level over the official one", row.CapDefaultEffort)
	}
	// The listing's other capabilities, its source and its authorization are all
	// still the row's.
	if row.Source != sub.id || row.SourcePlan != "coding-plan" && row.SourcePlan != "" {
		t.Fatalf("row = %+v, want the sign-in's source kept", row)
	}
	if !reflect.DeepEqual(row.CapEfforts, []string{"low", "medium", "high"}) {
		t.Fatalf("levels = %#v, want the listing's own", row.CapEfforts)
	}
	if row.CapReasoningOff == nil || *row.CapReasoningOff {
		t.Fatalf("reasoning off = %v, want the listing's explicit no", row.CapReasoningOff)
	}
	if !row.SubagentAllowed {
		t.Fatalf("row = %+v, want this flow's authorization", row)
	}

	// A second pass choosing the provider's own default clears both the level and
	// the params effort, and the official default does not come back.
	selector = &fakeSelector{confirms: []bool{true}, selects: []string{"gpt-official", reasoningEffortUnset, subagentsNo}}
	cfg = setupTestConfig(t, selector)
	seedEffortRow(t, cfg, process.LLMModelInput{
		Provider: "chatgpt", Model: "gpt-official", BaseURL: openai.CodexBaseURL, APIPath: "/responses",
		CapDefaultEffort: strPtrTUI("low"),
	})
	opts, ok, err = runSubscriptionSetup(t, cfg, sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	rep, err = process.Execute(rt(cfg), opts)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	row = readRow(t, cfg, rep.ModelID)
	if row.CapDefaultEffort != "" {
		t.Fatalf("cap_default_effort = %q, want it cleared", row.CapDefaultEffort)
	}
	if row.SubagentAllowed {
		t.Fatalf("row = %+v, want the second pass's answer", row)
	}
}

// One flow configures one row, chosen by its index: two models with one display
// name stay separate, the same pair on another endpoint is its own row, and the
// wire id — never the display name — is what gets written.
func TestConnectDefaultEffortUsesSelectedIdentity(t *testing.T) {
	installEffortTestCatalog(t)

	appcfg.ResetProviderPluginsForTest()
	t.Cleanup(appcfg.ResetProviderPluginsForTest)
	appcfg.RegisterProviderPlugin(appcfg.BuiltinProvider{
		ID: "qodercn", Label: "Qoder CN", LabelZH: "Qoder CN", LoginBacked: true,
		Endpoints: []appcfg.ProviderEndpoint{{BaseURL: "https://gateway.qoder.com.cn"}},
	})

	t.Run("two models with one display name stay separate rows", func(t *testing.T) {
		offerA := subscription.Offer{Subscription: "qodercn", Provider: "qodercn", Model: "plan-a-model", Name: "Same Name",
			BaseURL: "https://gateway.qoder.com.cn", APIPath: "/plan-a", PlanLabel: "Plan A", Default: true}
		offerB := subscription.Offer{Subscription: "qodercn", Provider: "qodercn", Model: "plan-b-model", Name: "Same Name",
			BaseURL: "https://gateway.qoder.com.cn", APIPath: "/plan-b", PlanLabel: "Plan B"}
		sub := &fakeSubscription{
			id: "qodercn", label: "Qoder CN",
			check:  turn.SignInCheck{State: turn.SignInUsable},
			offers: []subscription.Offer{offerA, offerB},
		}
		selector := &fakeSelector{selects: []string{"Same Name", reasoningEffortUnset, subagentsNo}}
		opts, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub)
		if err != nil || !ok {
			t.Fatalf("configure err=%v ok=%t", err, ok)
		}
		// Both rows are offered under the same name; the one the page picked is
		// the one written, and its own endpoint travels with it.
		if got := selector.selectOptions[0]; !reflect.DeepEqual(got, []string{"Same Name", "Same Name"}) {
			t.Fatalf("offer rows = %#v", got)
		}
		if opts.Model != "plan-a-model" || opts.APIPath != "/plan-a" {
			t.Fatalf("options = %+v, want the row the page opened on", opts)
		}

		// A listing that names only the other plan writes that plan's endpoint:
		// the name is not what identifies the row.
		onlyB := &fakeSubscription{
			id: "qodercn", label: "Qoder CN",
			check:  turn.SignInCheck{State: turn.SignInUsable},
			offers: []subscription.Offer{offerB},
		}
		selector = &fakeSelector{selects: []string{"Same Name", reasoningEffortUnset, subagentsNo}}
		opts, ok, err = runSubscriptionSetup(t, setupTestConfig(t, selector), onlyB)
		if err != nil || !ok {
			t.Fatalf("configure err=%v ok=%t", err, ok)
		}
		if opts.Model != "plan-b-model" || opts.APIPath != "/plan-b" {
			t.Fatalf("options = %+v, want the other plan's row", opts)
		}
	})

	t.Run("the same pair on another endpoint keeps its own answer", func(t *testing.T) {
		sub := &fakeSubscription{
			id: "qodercn", label: "Qoder CN",
			check: turn.SignInCheck{State: turn.SignInUsable},
			offers: []subscription.Offer{
				{Subscription: "qodercn", Provider: "qodercn", Model: "shared-model",
					BaseURL: "https://gateway.qoder.com.cn", APIPath: "/primary", PlanLabel: "Plan A", Default: true},
				{Subscription: "qodercn", Provider: "qodercn", Model: "shared-model",
					BaseURL: "https://gateway.qoder.com.cn", APIPath: "/mirror", PlanLabel: "Plan B"},
			},
		}
		withSubscriptions(t, sub)
		cfg := setupTestConfig(t, &fakeSelector{selects: []string{"shared-model"}})
		// The mirror row already exists with its own default; the primary one does
		// not.
		seedEffortRow(t, cfg, process.LLMModelInput{
			Provider: "qodercn", Model: "shared-model", BaseURL: "https://gateway.qoder.com.cn",
			APIPath: "/mirror", CapEfforts: &[]string{"low", "high"}, CapDefaultEffort: strPtrTUI("low"),
		})
		selector := cfg.Selector.(*fakeSelector)
		selector.selects = []string{"shared-model"}
		setup := newLLMSetup(cfg)
		setup.setChoice(subscriptionChoice(sub))
		setup.offers = sub.offers
		setup.page = setupPageOffers
		setup.selectedOfferIdx = -1
		if ok, err := setup.pickOffers(); err != nil || !ok {
			t.Fatalf("pickOffers ok=%t err=%v", ok, err)
		}
		// The page opened on the row this install already holds (the mirror).
		if got := selector.selectDefaults[0]; got != "shared-model" {
			t.Fatalf("open row = %q", got)
		}
		setup.selectedOfferIdx = 1
		if ok, err := setup.promptDefaultEffort(); err != nil || !ok {
			t.Fatalf("promptDefaultEffort ok=%t err=%v", ok, err)
		}
		if got := selector.selectDefaults[1]; got != "Low" {
			t.Fatalf("effort cursor = %q, want the mirror row's own level", got)
		}
		setup.selectedOfferIdx = 0
		selector.selects = append(selector.selects, reasoningEffortUnset)
		if ok, err := setup.promptDefaultEffort(); err != nil || !ok {
			t.Fatalf("promptDefaultEffort ok=%t err=%v", ok, err)
		}
		if got := selector.selectDefaults[2]; got != reasoningEffortUnset {
			t.Fatalf("effort cursor = %q, want the primary row's empty answer, not the mirror's", got)
		}
		setup.closeState()
	})

	t.Run("the wire id is written, not the vendor's name", func(t *testing.T) {
		sub := qoderCNLike()
		selector := &fakeSelector{selects: []string{"Qwen3.8-Max", reasoningEffortUnset, subagentsNo}}
		opts, ok, err := runSubscriptionSetup(t, setupTestConfig(t, selector), sub)
		if err != nil || !ok {
			t.Fatalf("configure err=%v ok=%t", err, ok)
		}
		if opts.Model != "qmodel_38max" {
			t.Fatalf("model = %q, want the wire id", opts.Model)
		}
		if got := sub.provisioned[0][0].Model; got != "qmodel_38max" {
			t.Fatalf("provisioned %q, want the wire id", got)
		}
	})

	t.Run("a login row written with a display path is still the same row", func(t *testing.T) {
		sub := openCodeLike()
		sub.check = turn.SignInCheck{State: turn.SignInUsable}
		// A real listing names no path; the row a legacy configuration wrote
		// carries the one it displayed.
		legacy := sub.offers[0]
		legacy.APIPath = "/chat/completions"
		cfg := setupTestConfig(t, &fakeSelector{selects: []string{"glm-5.3", "High", subagentsNo}})
		legacyID := offerRowSeed(t, cfg, sub, legacy, "sk-legacy-row")
		seedEffortRow(t, cfg, process.LLMModelInput{
			Provider: legacy.Provider, Model: legacy.Model, BaseURL: legacy.BaseURL, APIPath: legacy.APIPath,
			CapEfforts: &[]string{"low", "high"},
		})
		// The target row is found through the same rule the save uses.
		setup := newLLMSetup(cfg)
		setup.setChoice(subscriptionChoice(sub))
		setup.offers = sub.offers
		setup.selectedOfferIdx = 0
		if row, found, err := setup.storedRow(); err != nil || !found || row.ID != legacyID {
			t.Fatalf("found=%t err=%v, want the legacy row recognised", found, err)
		}
		setup.closeState()
		opts, ok, err := runSubscriptionSetup(t, cfg, sub)
		if err != nil || !ok {
			t.Fatalf("configure err=%v ok=%t", err, ok)
		}
		rep, err := process.Execute(process.Context{Home: cfg.Home, ConfigPath: cfg.ConfigPath, Config: appcfg.Root{}}, opts)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		row := readRow(t, cfg, rep.ModelID)
		if row.ID != legacyID || row.APIPath != "/chat/completions" {
			t.Fatalf("row = %+v, want the legacy row updated in place", row)
		}
		if row.CapDefaultEffort != "high" {
			t.Fatalf("cap_default_effort = %q, want the level saved on that row", row.CapDefaultEffort)
		}
	})
}

// The Off switch is a capability, not a level: the custom form's tick writes
// cap_reasoning_off and never cap_efforts, withdrawing it removes Off from the
// page while a stale "none" default falls back, and a source that says the model
// cannot turn thinking off wins over a weaker one that says it can.
func TestConnectDefaultEffortOffSplit(t *testing.T) {
	installEffortTestCatalog(t)
	rt := func(cfg PromptConfig) process.Context {
		return process.Context{Home: cfg.Home, ConfigPath: cfg.ConfigPath, Config: appcfg.Root{}}
	}

	t.Run("a custom model's Off tick is a column, never a level", func(t *testing.T) {
		cfg := setupTestConfig(t, &fakeSelector{
			confirms:     []bool{false},
			multiSelects: [][]int{{1, 2, 6}},
			selects:      []string{"Off"},
		})
		opts, ok, err := PromptMainLLMSetup(withCustomAnswers(cfg, customAnswers{
			baseURL: "https://api.example.com/v1", protocol: "OpenAI Chat Completions",
			key: "sk-vendor", model: "gpt-4o-vendor",
		}))
		if err != nil || !ok {
			t.Fatalf("configure err=%v ok=%t", err, ok)
		}
		rep, err := process.Execute(rt(cfg), opts)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		row := readRow(t, cfg, rep.ModelID)
		if row.CapReasoningOff == nil || !*row.CapReasoningOff {
			t.Fatalf("cap_reasoning_off = %v, want true", row.CapReasoningOff)
		}
		if !reflect.DeepEqual(row.CapEfforts, []string{"low", "medium"}) {
			t.Fatalf("cap_efforts = %#v, want the levels without none", row.CapEfforts)
		}
		if row.CapDefaultEffort != "none" {
			t.Fatalf("cap_default_effort = %q, want none", row.CapDefaultEffort)
		}
	})

	t.Run("withdrawing the Off declaration removes it and the stale none falls back", func(t *testing.T) {
		cfg := setupTestConfig(t, &fakeSelector{
			// Declining the connection test, then confirming the save over the
			// row this install already holds.
			confirms:     []bool{false, true},
			multiSelects: [][]int{{1, 3}},
		})
		// The row declared Off and a none default last time; this pass ticks low
		// and high and leaves Off unticked.
		seedEffortRow(t, cfg, process.LLMModelInput{
			Provider: "custom", Model: "gpt-4o-vendor", BaseURL: "https://api.example.com/v1",
			APIPath: appcfg.APIPathChatCompletions, APIKey: strPtrTUI("sk-vendor"),
			CapEfforts: &[]string{"low", "medium"}, CapReasoningOff: boolPtrTUI(true),
			CapDefaultEffort: strPtrTUI("none"),
		})
		selector := cfg.Selector.(*fakeSelector)
		opts, ok, err := PromptMainLLMSetup(withCustomAnswers(cfg, customAnswers{
			baseURL: "https://api.example.com/v1", protocol: "OpenAI Chat Completions",
			key: "sk-vendor", model: "gpt-4o-vendor",
		}))
		if err != nil || !ok {
			t.Fatalf("configure err=%v ok=%t", err, ok)
		}
		// The capability page opened on the stored declaration; the save withdrew
		// it, so the level page has no Off and the stale none is not offered.
		if got := selector.multiDefaults[0]; !reflect.DeepEqual(got, []int{1, 2, 6}) {
			t.Fatalf("capability page opened on %v, want the row's declaration replayed", got)
		}
		var effortOptions []string
		for i, label := range selector.selectLabels {
			if strings.HasPrefix(label, "Default reasoning effort") {
				effortOptions = selector.selectOptions[i]
			}
		}
		if !reflect.DeepEqual(effortOptions, []string{"Low", "High", reasoningEffortUnset}) {
			t.Fatalf("effort page = %#v, want the levels just declared and no Off", effortOptions)
		}
		if opts.CapDefaultEffort == nil || *opts.CapDefaultEffort != "" {
			t.Fatalf("cap default = %v, want the stale none replaced by the provider default", opts.CapDefaultEffort)
		}
	})

	t.Run("an explicit no wins over the catalog's none", func(t *testing.T) {
		cfg := setupTestConfig(t, &fakeSelector{})
		seedEffortRow(t, cfg, process.LLMModelInput{
			Provider: "openai", Model: "off-model", BaseURL: "https://api.openai.com/v1",
			APIKey: strPtrTUI("sk-live"), CapReasoningOff: boolPtrTUI(false),
		})
		setup := apiKeyEffortSetup(cfg, "openai", "off-model", "https://api.openai.com/v1")
		defer setup.closeState()
		if ok, err := setup.promptDefaultEffort(); err != nil || !ok {
			t.Fatalf("promptDefaultEffort ok=%t err=%v", ok, err)
		}
		if got := cfg.Selector.(*fakeSelector).selectOptions[0]; !reflect.DeepEqual(got, []string{"Low", "Medium", reasoningEffortUnset}) {
			t.Fatalf("effort page = %#v, want the row's explicit no to hide Off", got)
		}
	})

	t.Run("a subscription never touches the listing's off switch", func(t *testing.T) {
		sub := chatGPTLike()
		sub.offers = []subscription.Offer{{
			Subscription: "chatgpt", Provider: "chatgpt", Model: "gpt-offered",
			BaseURL: openai.CodexBaseURL, APIPath: "/responses", PlanLabel: "ChatGPT plan", Default: true,
			Efforts: []string{"none", "low", "medium"},
		}}
		selector := &fakeSelector{confirms: []bool{true}, selects: []string{"gpt-offered", "Off", subagentsNo}}
		cfg := setupTestConfig(t, selector)
		opts, ok, err := runSubscriptionSetup(t, cfg, sub)
		if err != nil || !ok {
			t.Fatalf("configure err=%v ok=%t", err, ok)
		}
		if opts.CapReasoningOff != nil {
			t.Fatalf("options carry reasoning off = %v, want the listing to own it", *opts.CapReasoningOff)
		}
		if opts.CapDefaultEffort == nil || *opts.CapDefaultEffort != "none" {
			t.Fatalf("cap default = %v, want none", opts.CapDefaultEffort)
		}
		// The offer travels unmodified: the wizard never rewrites its switch.
		if sub.provisioned[0][0].ReasoningOff != nil {
			t.Fatalf("offer reasoning off = %v, want the listing's own value", *sub.provisioned[0][0].ReasoningOff)
		}
	})
}

// The connection test carries the level this flow chose — it is the one request
// that would catch a level the model refuses — and that level is still stored as
// the row's own default, never as the params the probe sent. The session /connect
// switches then receives the answer explicitly, so a level that was cleared stays
// cleared (plan 311).
func TestConnectDefaultEffortProbeAndSession(t *testing.T) {
	installEffortTestCatalog(t)

	t.Run("the probe sends the chosen level and the save stores it as a default", func(t *testing.T) {
		probes := stubLLMProbeInput(t)
		cfg := setupTestConfig(t, &fakeSelector{
			confirms:     []bool{false, true},
			multiSelects: [][]int{{1, 3}},
			selects:      []string{"High"},
		})
		// The row already carries request parameters and a level of its own.
		params := `{"temperature":0.2,"reasoning":{"effort":"low"}}`
		seedEffortRow(t, cfg, process.LLMModelInput{
			Provider: "custom", Model: "gpt-4o-vendor", BaseURL: "https://api.example.com/v1",
			APIPath: appcfg.APIPathChatCompletions, APIKey: strPtrTUI("sk-vendor"),
			ParamsJSON: &params, CapEfforts: &[]string{"low", "high"},
		})
		opts, ok, err := PromptMainLLMSetup(withCustomAnswers(cfg, customAnswers{
			baseURL: "https://api.example.com/v1", protocol: "OpenAI Chat Completions",
			key: "sk-vendor", model: "gpt-4o-vendor",
		}))
		if err != nil || !ok {
			t.Fatalf("configure err=%v ok=%t", err, ok)
		}
		// The connection test is asked last, and it is the only "no" here.
		selector := cfg.Selector.(*fakeSelector)
		if len(selector.confirmLabels) < 1 || !strings.Contains(selector.confirmLabels[0], "Test connection?") {
			t.Fatalf("confirmations = %#v, want the connection test first", selector.confirmLabels)
		}
		if len(*probes) != 0 {
			t.Fatalf("probes = %d, want the test declined", len(*probes))
		}
		// The same flow with the test accepted sends one request carrying the
		// level chosen on the page.
		cfg = setupTestConfig(t, &fakeSelector{
			confirms:     []bool{true, true},
			multiSelects: [][]int{{1, 3}},
			selects:      []string{"High"},
		})
		seedEffortRow(t, cfg, process.LLMModelInput{
			Provider: "custom", Model: "gpt-4o-vendor", BaseURL: "https://api.example.com/v1",
			APIPath: appcfg.APIPathChatCompletions, APIKey: strPtrTUI("sk-vendor"),
			ParamsJSON: &params, CapEfforts: &[]string{"low", "high"},
		})
		opts, ok, err = PromptMainLLMSetup(withCustomAnswers(cfg, customAnswers{
			baseURL: "https://api.example.com/v1", protocol: "OpenAI Chat Completions",
			key: "sk-vendor", model: "gpt-4o-vendor",
		}))
		if err != nil || !ok {
			t.Fatalf("configure err=%v ok=%t", err, ok)
		}
		if len(*probes) != 1 {
			t.Fatalf("probes = %d, want one request", len(*probes))
		}
		sent := (*probes)[0].ParamsJSON
		if sent == nil || !strings.Contains(*sent, `"effort":"high"`) {
			t.Fatalf("probe params = %v, want the level chosen on the page", sent)
		}
		if !strings.Contains(*sent, `"temperature":0.2`) || strings.Contains(*sent, `"low"`) {
			t.Fatalf("probe params = %q, want the row's other fields and no stale level", *sent)
		}
		// Nothing of the probe is what gets stored: the level is the row's
		// default and the params keep their other fields only.
		rep, err := process.Execute(process.Context{Home: cfg.Home, ConfigPath: cfg.ConfigPath, Config: appcfg.Root{}}, opts)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		row := readRow(t, cfg, rep.ModelID)
		if row.CapDefaultEffort != "high" {
			t.Fatalf("cap_default_effort = %q, want high", row.CapDefaultEffort)
		}
		if strings.Contains(row.ParamsJSON, `"effort"`) || !strings.Contains(row.ParamsJSON, `"temperature":0.2`) {
			t.Fatalf("params = %q, want the effort cleared and the rest kept", row.ParamsJSON)
		}
	})

	t.Run("the session is switched with the answer this flow gave", func(t *testing.T) {
		// The row this flow updates already carries a level the user is about to
		// replace: the session must take the new one, not the stored one.
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "forebrain.yaml"), []byte("agents: {}\n"), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		t.Setenv("FOREBRAIN_HOME", home)
		process.ResetResolve()
		t.Cleanup(process.ResetResolve)
		cfg := PromptConfig{Home: home, ConfigPath: filepath.Join(home, "forebrain.yaml")}
		seedEffortRow(t, cfg, process.LLMModelInput{
			Provider: "openai", Model: "levels-model", BaseURL: "https://api.openai.com/v1",
			APIKey: strPtrTUI("sk-live"), CapDefaultEffort: strPtrTUI("low"),
		})

		for _, tc := range []struct {
			name  string
			label string
			want  string
		}{
			{name: "a level", label: "High", want: "high"},
			{name: "the provider's own default", label: reasoningEffortUnset, want: ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				selector := &fakeSelector{
					selects:  []string{"OpenAI", "levels-model", tc.label, subagentsNo},
					inputs:   []string{"sk-live"},
					confirms: []bool{true},
				}
				session := &recordingSession{}
				var out bytes.Buffer
				c := &commandController{session: session, renderer: NewRenderer(&out, &out), selector: selector, home: home}
				ask := func(prompt PromptConfig) (process.Options, bool, error) {
					prompt.Selector = selector
					prompt.Output = io.Discard
					return PromptMainLLMSetup(prompt)
				}
				if !c.runConnect(context.Background(), "session-1", ask) {
					t.Fatalf("runConnect printed an error: %q", out.String())
				}
				if session.synced != 1 {
					t.Fatalf("SyncConfig calls = %d, want the config adopted before the model", session.synced)
				}
				if session.effort == nil {
					t.Fatal("SelectModel was given no effort: the flow's answer would be lost")
				}
				if *session.effort != tc.want {
					t.Fatalf("session effort = %q, want %q", *session.effort, tc.want)
				}
				if session.choice.Model != "levels-model" {
					t.Fatalf("session choice = %+v", session.choice)
				}
				if session.choice.ID == "" {
					t.Fatal("the session was not given the row the save wrote")
				}
				// The row the session was pointed at carries the same answer.
				row := readRow(t, cfg, session.choice.ID)
				if row.CapDefaultEffort != tc.want {
					t.Fatalf("cap_default_effort = %q, want %q", row.CapDefaultEffort, tc.want)
				}
			})
		}
	})
}

// recordingSession is the slice of the session surface /connect drives: the
// configuration adoption, the model switch, and the footer reads. Any other
// method is a bug in this test.
type recordingSession struct {
	Session
	synced int
	choice turn.ModelChoice
	effort *string
}

func (s *recordingSession) SyncConfig(context.Context) error {
	s.synced++
	return nil
}

func (s *recordingSession) SelectModel(_ context.Context, _ string, choice turn.ModelChoice, effort *string) error {
	s.choice, s.effort = choice, effort
	return nil
}

func (s *recordingSession) CurrentModelOption() string                 { return "openai/levels-model" }
func (s *recordingSession) CurrentModelRowID() string                  { return "" }
func (s *recordingSession) ModelSummaryString() string                 { return "openai/levels-model" }
func (s *recordingSession) CurrentModelReasoningEffort() string        { return "" }
func (s *recordingSession) SurfaceContextOccupancy(string) (int, bool) { return 0, false }
func (s *recordingSession) SurfaceComposerTokenStats(string, int) ComposerTokenStats {
	return ComposerTokenStats{}
}

// The model page and the default-effort page as a real terminal draws them and
// reads them (plan 311): the model list is one choice among tabs, with no
// checkbox glyph and no space-to-toggle hint, and the level page states the model
// it configures in the page itself. Enter confirms the row the page opened on and
// Esc walks back — the keys, not the fake selector's answers.
func TestConnectDefaultEffortRendersSingleChoice(t *testing.T) {
	sub := chatGPTLike()
	sub.check = turn.SignInCheck{State: turn.SignInUsable}
	// One model per plan, so the picker draws a tab bar and the choice is a row
	// among them rather than a name that could be confused with the endpoint.
	sub.offers = []subscription.Offer{
		{Subscription: "chatgpt", Provider: "chatgpt", Model: "gpt-future", BaseURL: openai.CodexBaseURL,
			APIPath: "/responses", PlanLabel: "ChatGPT plan", Default: true,
			Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium"},
		{Subscription: "chatgpt", Provider: "chatgpt", Model: "gpt-second", BaseURL: openai.CodexBaseURL,
			APIPath: "/responses", PlanLabel: "ChatGPT team", Efforts: []string{"low", "high"}},
	}
	panel := newConnectPanel(t, 100, 30)
	cfg := setupTestConfig(t, panel)
	answer := startConnectSetup(t, cfg, sub)

	models := waitForPanelText(panel.render, "Choose the model to add")
	for _, want := range []string{"ChatGPT", "ChatGPT plan 1", "ChatGPT team 1", "gpt-future", "enter to confirm", "esc to go back"} {
		if !strings.Contains(models, want) {
			t.Fatalf("model page = %q, want it to carry %q", models, want)
		}
	}
	// Tab reaches the other plan's row: the tabs are the plans, and each one's
	// model is its own row.
	panel.typeKeys(t, "\t")
	if other := waitForPanelText(panel.render, "gpt-second"); !strings.Contains(other, "gpt-second") {
		t.Fatalf("other tab = %q, want the second plan's model", other)
	}
	panel.typeKeys(t, "\t")
	waitForPanelText(panel.render, "gpt-future")
	if strings.Contains(models, "space to select") {
		t.Fatalf("model page = %q, one model is chosen, not ticked", models)
	}
	if strings.ContainsAny(models, selectorCheckedGlyph+selectorUncheckedGlyph) {
		t.Fatalf("model page = %q, want no check-box glyph on a single choice", models)
	}

	// Enter takes the row the page opened on — the plan's default — to the level
	// page.
	panel.typeKeys(t, "\r")
	effort := waitForPanelText(panel.render, "Default reasoning effort")
	for _, want := range []string{
		"Choose the default for chatgpt/gpt-future.",
		"Low", "Medium", "High", "Provider default",
		"enter to confirm", "esc to go back",
	} {
		if !strings.Contains(effort, want) {
			t.Fatalf("effort page = %q, want it to carry %q", effort, want)
		}
	}
	if strings.Contains(effort, "Off") {
		t.Fatalf("effort page = %q, this model cannot turn thinking off", effort)
	}
	// The wire ids are the levels' own; the page shows the names every other
	// picker shows.
	if strings.Contains(effort, "xHigh") && !strings.Contains(effort, "low") {
		t.Fatalf("effort page = %q, want the levels the model offers", effort)
	}

	// Esc goes back to the model list, not out of the flow: the person is one
	// page away from the answer they were giving.
	panel.typeKeys(t, "\x1b")
	back := waitForPanelText(panel.render, "Choose the model to add")
	if !strings.Contains(back, "gpt-future") {
		t.Fatalf("page after Esc = %q, want the model list", back)
	}

	got := panel.finish(t, answer)
	if got.ok || got.opts.Provider != "" {
		t.Fatalf("answer = %+v ok=%t, Esc writes nothing", got.opts, got.ok)
	}
	if len(sub.provisioned) != 0 {
		t.Fatalf("provisioned %+v, Esc writes nothing", sub.provisioned)
	}
}

// modelRowsForTest is how a fixture seeds the install's model table (plan 315):
// one fb_llm_models row per model an entry names, with the row ids llm-1,
// llm-2, … in order. A definition names one of them with default_model.
// modelRowsForTest is how a fixture seeds the install's model table (plan 315):
// one fb_llm_models row per model an entry names — `model: [a, b]` expanding
// the way a provider list used to — with the row ids llm-1, llm-2, … in order.
// A definition names one of them with default_model; a built-in subagent type
// names one in agents.subagent_models.<type>.
func modelRowsForTest(entries ...appcfg.AgentLLMProviderConfig) []appcfg.LLMModelConfig {
	var rows []appcfg.LLMModelConfig
	for _, entry := range entries {
		models := append(appcfg.StringList(nil), entry.Models...)
		if len(models) == 0 && entry.Model != "" {
			models = appcfg.StringList{entry.Model}
		}
		if len(models) == 0 {
			models = appcfg.StringList{""}
		}
		for _, model := range models {
			rows = append(rows, appcfg.LLMModelConfig{
				ID:          fmt.Sprintf("llm-%d", len(rows)+1),
				Provider:    entry.Provider,
				Model:       model,
				BaseURL:     entry.BaseURL,
				APIPath:     entry.APIPath,
				APIKey:      entry.APIKey,
				Params:      append(appcfg.LLMRequestParams(nil), entry.Params...),
				URLVerbatim: entry.URLVerbatim,
			})
		}
	}
	return rows
}
