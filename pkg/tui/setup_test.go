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
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

func TestStartupConfigErrorReportsMissingDotEnvForCopiedConfig(t *testing.T) {
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
	if !strings.Contains(msg, "api_key") || !strings.Contains(msg, filepath.Join(home, ".env")) {
		t.Fatalf("diagnostic should mention api_key and custom .env path, got %q", msg)
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

// stubChatGPTModels replaces account model discovery with a fixed answer.
func stubChatGPTModels(t *testing.T, records []turn.ModelRecord, discoverErr error) {
	t.Helper()
	previous := discoverChatGPTModels
	discoverChatGPTModels = func(context.Context, string) ([]turn.ModelRecord, error) {
		return records, discoverErr
	}
	t.Cleanup(func() { discoverChatGPTModels = previous })
}

func stubChatGPTBrowserLogin(t *testing.T) {
	t.Helper()
	previous := chatGPTLogin
	chatGPTLogin = func(context.Context, string, io.Writer) (openai.Credentials, error) {
		return openai.Credentials{Email: "dev@example.com"}, nil
	}
	t.Cleanup(func() { chatGPTLogin = previous })
}

func chatGPTProviderChoice() providerChoice {
	return providerGroups[0].Options[0]
}

// runChatGPTSetup runs the model setup from its sign-in page with ChatGPT
// already chosen, saving at the review.
func runChatGPTSetup(cfg PromptConfig) (process.Options, bool, error) {
	setup := newLLMSetup(cfg)
	setup.setChoice(chatGPTProviderChoice())
	setup.page = setupPageCredentials
	return setup.run()
}

func discoveredFixture() []turn.ModelRecord {
	return []turn.ModelRecord{
		{ModelName: "GPT Future", APIModel: "gpt-future", Provider: "chatgpt", IsDefault: true,
			ReasoningEfforts: []string{"low", "medium", "max"}, DefaultReasoningEffort: "low"},
		{ModelName: "GPT Second", APIModel: "gpt-second", Provider: "chatgpt",
			ReasoningEfforts: []string{"low", "medium", "max"}, DefaultReasoningEffort: "medium"},
	}
}

// The picker must offer exactly what the account returned, in backend order,
// labeled with the display name and the exact slug that will be saved.
func TestConfigureChatGPTProviderOffersDiscoveredModels(t *testing.T) {
	stubChatGPTBrowserLogin(t)
	stubChatGPTModels(t, discoveredFixture(), nil)
	selector := &fakeSelector{
		selects:  []string{"GPT Second · gpt-second", "max"},
		confirms: []bool{true, true},
	}
	opts, ok, err := runChatGPTSetup(PromptConfig{
		Selector: selector, Output: io.Discard, Home: t.TempDir(),
	})
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	modelSelect := 0
	if got := selector.selectOptions[modelSelect]; !reflect.DeepEqual(got, []string{
		"GPT Future · gpt-future", "GPT Second · gpt-second", manualModelInput,
	}) {
		t.Fatalf("model options = %#v", got)
	}
	if got := selector.selectDefaults[modelSelect]; got != "GPT Future · gpt-future" {
		t.Fatalf("model default = %q, want the account's priority-first default", got)
	}
	if got := selector.selectOptions[1]; !reflect.DeepEqual(got, []string{"low", "medium", "max", reasoningEffortUnset}) {
		t.Fatalf("effort options = %#v, want the record's own levels", got)
	}
	if got := selector.selectDefaults[1]; got != "medium" {
		t.Fatalf("effort default = %q, want the record's backend default", got)
	}
	if opts.Model != "gpt-second" {
		t.Fatalf("model = %q, want the slug behind the chosen label", opts.Model)
	}
	if opts.ParamsJSON != `{"reasoning":{"effort":"max"}}` {
		t.Fatalf("params = %q", opts.ParamsJSON)
	}
	if opts.BaseURL != openai.CodexBaseURL || opts.APIPath != "/responses" || opts.Provider != "chatgpt" {
		t.Fatalf("options = %+v", opts)
	}
}

// A still-listed configured model stays the default; a configured model the
// account no longer offers gives way to the account's own default, not to the
// stale name and not to Manual input.
func TestConfigureChatGPTProviderCurrentModelDefaultRules(t *testing.T) {
	stubChatGPTBrowserLogin(t)
	stubChatGPTModels(t, discoveredFixture(), nil)
	selector := &fakeSelector{confirms: []bool{true, true, true}}
	_, ok, err := runChatGPTSetup(PromptConfig{
		Selector: selector, Output: io.Discard, Home: t.TempDir(),
		Defaults: process.AgentLLMDefaults{Provider: "chatgpt", Model: "gpt-second"},
	})
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if got := selector.selectDefaults[0]; got != "GPT Second · gpt-second" {
		t.Fatalf("still-listed model default = %q", got)
	}

	selector = &fakeSelector{confirms: []bool{true, true}}
	_, ok, err = runChatGPTSetup(PromptConfig{
		Selector: selector, Output: io.Discard, Home: t.TempDir(),
		Defaults: process.AgentLLMDefaults{Provider: "chatgpt", Model: "gpt-retired"},
	})
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if got := selector.selectDefaults[0]; got != "GPT Future · gpt-future" {
		t.Fatalf("retired model default = %q, want the account's default", got)
	}
}

// A configured effort the model no longer supports must not be preselected;
// the record's own default takes over.
func TestConfigureChatGPTProviderEffortMismatchFallsToRecordDefault(t *testing.T) {
	stubChatGPTBrowserLogin(t)
	stubChatGPTModels(t, discoveredFixture(), nil)
	selector := &fakeSelector{confirms: []bool{true, true}}
	_, ok, err := runChatGPTSetup(PromptConfig{
		Selector: selector, Output: io.Discard, Home: t.TempDir(),
		Defaults: process.AgentLLMDefaults{Provider: "chatgpt", Model: "gpt-future",
			ParamsJSON: `{"reasoning":{"effort":"xhigh"}}`},
	})
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if got := selector.selectDefaults[1]; got != "low" {
		t.Fatalf("effort default = %q, want the record default after an unsupported xhigh", got)
	}
}

// A failed fetch offers a retry that skips the browser login; declining it
// aborts with an actionable error — the login stands, but no model picker runs
// and no provider options are produced.
func TestConfigureChatGPTProviderFetchFailureAborts(t *testing.T) {
	stubChatGPTBrowserLogin(t)
	attempts := 0
	previous := discoverChatGPTModels
	discoverChatGPTModels = func(context.Context, string) ([]turn.ModelRecord, error) {
		attempts++
		return nil, errors.New("fetch ChatGPT models: http 401: token expired")
	}
	t.Cleanup(func() { discoverChatGPTModels = previous })
	selector := &fakeSelector{confirms: []bool{true, false}, failUnexpectedSelect: true}
	opts, ok, err := runChatGPTSetup(PromptConfig{
		Selector: selector, Output: io.Discard, Home: t.TempDir(),
	})
	if err == nil || ok {
		t.Fatalf("err=%v ok=%t, want the flow to abort", err, ok)
	}
	if !strings.Contains(err.Error(), "could not be fetched") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want an actionable fetch failure", err)
	}
	if opts.Provider != "" {
		t.Fatalf("options = %+v, want none written", opts)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want the declined retry to stop fetching", attempts)
	}
}

// Accepting the retry refetches on the saved login — no second browser flow —
// and the flow then continues with the fresh listing.
func TestConfigureChatGPTProviderFetchRetrySkipsRelogin(t *testing.T) {
	stubChatGPTBrowserLogin(t)
	loginCalls := 0
	previousLogin := chatGPTLogin
	chatGPTLogin = func(context.Context, string, io.Writer) (openai.Credentials, error) {
		loginCalls++
		return openai.Credentials{Email: "dev@example.com"}, nil
	}
	t.Cleanup(func() { chatGPTLogin = previousLogin })
	fetchCalls := 0
	previous := discoverChatGPTModels
	discoverChatGPTModels = func(context.Context, string) ([]turn.ModelRecord, error) {
		fetchCalls++
		if fetchCalls == 1 {
			return nil, errors.New("fetch ChatGPT models: http 429: slow down")
		}
		return discoveredFixture(), nil
	}
	t.Cleanup(func() { discoverChatGPTModels = previous })
	selector := &fakeSelector{
		selects:  []string{"GPT Future · gpt-future", "low"},
		confirms: []bool{true, true, true},
	}
	opts, ok, err := runChatGPTSetup(PromptConfig{
		Selector: selector, Output: io.Discard, Home: t.TempDir(),
	})
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if loginCalls != 1 {
		t.Fatalf("login calls = %d, want the retry to reuse the saved login", loginCalls)
	}
	if fetchCalls != 2 {
		t.Fatalf("fetch calls = %d, want exactly one retry", fetchCalls)
	}
	if opts.Model != "gpt-future" {
		t.Fatalf("model = %q", opts.Model)
	}
}

// Manual input stays reachable and stores the typed id; a model neither the
// account nor the catalog knows gets no effort page at all, and therefore no
// pinned level — the picker never invents a scale for it.
func TestConfigureChatGPTProviderManualInputDefaultsEffortUnset(t *testing.T) {
	stubChatGPTBrowserLogin(t)
	stubChatGPTModels(t, discoveredFixture(), nil)
	selector := &fakeSelector{
		selects:  []string{manualModelInput},
		inputs:   []string{"gpt-typed"},
		confirms: []bool{true, true},
	}
	opts, ok, err := runChatGPTSetup(PromptConfig{
		Selector: selector, Output: io.Discard, Home: t.TempDir(),
	})
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if opts.Model != "gpt-typed" {
		t.Fatalf("model = %q", opts.Model)
	}
	for _, label := range selector.selectLabels {
		if strings.HasPrefix(label, "Reasoning effort\n") {
			t.Fatalf("an unverified model must not be asked for an effort: %#v", selector.selectLabels)
		}
	}
	if opts.ParamsJSON != "" {
		t.Fatalf("params = %q, want no pinned effort", opts.ParamsJSON)
	}
}

// A successful fetch of an empty catalog says so and falls back to manual
// entry — never to a baked-in list.
func TestConfigureChatGPTProviderEmptyCatalogSaysSo(t *testing.T) {
	stubChatGPTBrowserLogin(t)
	stubChatGPTModels(t, nil, nil)
	out := &bytes.Buffer{}
	selector := &fakeSelector{
		inputs:   []string{"gpt-typed", ""},
		confirms: []bool{true, true},
	}
	opts, ok, err := runChatGPTSetup(PromptConfig{
		Selector: selector, Output: out, Home: t.TempDir(),
	})
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if len(selector.inputLabels) == 0 || !strings.Contains(selector.inputLabels[0], "no selectable models") {
		t.Fatalf("input labels = %q, want the empty catalog named", selector.inputLabels)
	}
	if opts.Model != "gpt-typed" {
		t.Fatalf("model = %q", opts.Model)
	}
}

// Other providers list their models from the shared catalog, with the typed
// ID appended by the prompt itself.
func TestCatalogModelOptionsComeFromTheSharedCatalog(t *testing.T) {
	useSetupTestCatalog(t)
	options := catalogModelOptions("anthropic")
	if len(options) != 2 {
		t.Fatalf("options = %#v", options)
	}
	if options[0].Label != "Claude Haiku Lite · claude-haiku-lite" || options[0].Value != "claude-haiku-lite" {
		t.Fatalf("first option = %+v, want catalog name plus exact id, name-sorted", options[0])
	}
	for _, option := range options {
		if option.Label == manualModelInput {
			t.Fatal("manual input belongs to the prompt, not the catalog list")
		}
	}
}

// Every model the catalog holds for the provider is listed: the picker's
// filter finds one among them, so nothing is left out to keep the list short.
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

// stubOpenCodeDeviceLogin replaces the device flow with a fixed login and
// records the path each run was asked to save to, so a test can prove a path
// never starts the flow at all.
func stubOpenCodeDeviceLogin(t *testing.T) *[]string {
	t.Helper()
	paths := []string{}
	previous := openCodeLogin
	openCodeLogin = func(_ context.Context, path string, _ io.Writer, _ func(opencode.LoginInfo), _ func(opencode.LoginStage)) (opencode.Credentials, error) {
		paths = append(paths, path)
		return opencode.Credentials{AccessToken: "go-token", RefreshToken: "go-refresh", Email: "dev@example.com"}, nil
	}
	t.Cleanup(func() { openCodeLogin = previous })
	return &paths
}

// stubOpenCodeModels replaces gateway catalog discovery with a fixed answer.
func stubOpenCodeModels(t *testing.T, records []turn.ModelRecord, discoverErr error) {
	t.Helper()
	previous := discoverOpenCodeModels
	discoverOpenCodeModels = func(_ context.Context, _ string) ([]turn.ModelRecord, error) {
		return records, discoverErr
	}
	t.Cleanup(func() { discoverOpenCodeModels = previous })
}

// tuiOpenCodeSource stands in for the gateway's Go catalog, so a test can build
// its fixture through the real projection instead of hand-writing records that
// the production path would never produce.
type tuiOpenCodeSource struct {
	models []opencode.ModelInfo
}

func (s tuiOpenCodeSource) OpenCodeModels(context.Context, bool) ([]opencode.ModelInfo, error) {
	return s.models, nil
}

func (s tuiOpenCodeSource) OpenCodeSignedIn() bool { return true }

// openCodeGoProjection projects ids through turn.DiscoverOpenCodeModels, the
// same call production makes, so the effort levels in a fixture are the ones
// the setup would really see.
func openCodeGoProjection(t *testing.T, ids ...string) []turn.ModelRecord {
	t.Helper()
	models := make([]opencode.ModelInfo, 0, len(ids))
	for _, id := range ids {
		models = append(models, opencode.ModelInfo{ID: id})
	}
	records, err := turn.DiscoverOpenCodeModels(context.Background(), tuiOpenCodeSource{models: models}, true)
	if err != nil {
		t.Fatalf("project the Go catalog: %v", err)
	}
	return records
}

// openCodeProviderChoice finds the OpenCode choice for a provider, so the tests
// do not depend on where in the group it sits.
func openCodeProviderChoice(t *testing.T, provider string) providerChoice {
	t.Helper()
	for _, group := range providerGroups {
		for _, option := range group.Options {
			if option.Provider == provider && option.OpenCodeDevice {
				return option
			}
		}
	}
	t.Fatalf("no OpenCode choice for provider %q", provider)
	return providerChoice{}
}

func openCodeGoFixture() []turn.ModelRecord {
	return []turn.ModelRecord{
		{ModelName: "glm-5.3", APIModel: "glm-5.3", Provider: "opencode-go", IsDefault: true},
		{ModelName: "kimi-k3", APIModel: "kimi-k3", Provider: "opencode-go"},
	}
}

// runOpenCodeSetup runs the model setup from its credentials page with an
// OpenCode choice already picked, saving at the review.
func runOpenCodeSetup(t *testing.T, cfg PromptConfig, provider string) (process.Options, bool, error) {
	t.Helper()
	setup := newLLMSetup(cfg)
	setup.setChoice(openCodeProviderChoice(t, provider))
	setup.page = setupPageCredentials
	return setup.run()
}

// The OpenCode group has exactly one way in — the Go subscription's device-code
// login. The Console free family is gone: the gateway only answers OpenCode's
// own client, so offering it would end in a 403 on the first message.
func TestProviderGroupsOfferOpenCodeGoOnly(t *testing.T) {
	group := providerGroups[1]
	if group.Label != "OpenCode" {
		t.Fatalf("second group = %q, want OpenCode right after ChatGPT", group.Label)
	}
	if got := providerGroupBlurb(group); got != "Device-code login; use your Go/Go Plus quota" {
		t.Fatalf("blurb = %q, want the Go login described on the row itself", got)
	}
	if !hasSubscriptionSignIn(group) {
		t.Fatal("the OpenCode group belongs under the sign-in providers")
	}
	if len(group.Options) != 1 {
		t.Fatalf("options = %+v, want only the Go subscription", group.Options)
	}
	goChoice := group.Options[0]
	if !goChoice.OpenCodeDevice {
		t.Fatalf("Go choice = %+v", goChoice)
	}
	if got := goChoice.DefaultBaseURL; got != opencode.ProviderBaseURL("opencode-go") || got == "" {
		t.Fatalf("Go base_url = %q", got)
	}
	// The Console provider must not be reachable from the provider page at all.
	for _, group := range providerGroups {
		for _, option := range group.Options {
			if option.Provider == "opencode" {
				t.Fatalf("provider page offers the OpenCode Console surface: %+v", option)
			}
		}
	}
}

// Picking the OpenCode group from the provider page goes straight into the Go
// login — one page, no plan picker, no key page.
func TestPromptMainLLMSetupReachesOpenCodeFromTheProviderPage(t *testing.T) {
	loginPaths := stubOpenCodeDeviceLogin(t)
	stubOpenCodeModels(t, openCodeGoProjection(t, "glm-5.3", "kimi-k3"), nil)
	selector := &fakeSelector{selects: []string{"OpenCode", "glm-5.3"}, confirms: []bool{true}}
	opts, ok, err := PromptMainLLMSetup(PromptConfig{
		Selector: selector, Output: io.Discard, Home: t.TempDir(),
	})
	if err != nil || !ok {
		t.Fatalf("PromptMainLLMSetup ok=%t err=%v", ok, err)
	}
	if len(*loginPaths) != 1 {
		t.Fatalf("device logins = %d, want the Go login", len(*loginPaths))
	}
	if got := selector.selectLabels; len(got) == 0 || strings.Contains(got[0], "Which plan") {
		t.Fatalf("select labels = %v, want no plan page before the login", got)
	}
	if opts.Provider != "opencode-go" || opts.Model != "glm-5.3" {
		t.Fatalf("options = %+v", opts)
	}
	if opts.BaseURL != opencode.ProviderBaseURL("opencode-go") || opts.APIKey != "" {
		t.Fatalf("options = %+v", opts)
	}
}

// The Go subscription signs in through the device flow, saves the login where
// every other surface reads it, and then offers the Go catalog.
func TestConfigureOpenCodeGoSignsInAndOffersTheGoCatalog(t *testing.T) {
	loginPaths := stubOpenCodeDeviceLogin(t)
	stubOpenCodeModels(t, openCodeGoFixture(), nil)
	home := t.TempDir()
	selector := &fakeSelector{confirms: []bool{true}}
	opts, ok, err := runOpenCodeSetup(t, PromptConfig{
		Selector: selector, Output: io.Discard, Home: home,
	}, "opencode-go")
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if len(*loginPaths) != 1 || (*loginPaths)[0] != opencode.DefaultPath(home) {
		t.Fatalf("login paths = %v, want the login file in home", *loginPaths)
	}
	if got := selector.selectOptions[0]; !reflect.DeepEqual(got, []string{
		"glm-5.3", "kimi-k3", manualModelInput,
	}) {
		t.Fatalf("model options = %#v", got)
	}
	if opts.Provider != "opencode-go" || opts.Model != "glm-5.3" {
		t.Fatalf("options = %+v", opts)
	}
	if opts.BaseURL != opencode.ProviderBaseURL("opencode-go") {
		t.Fatalf("base_url = %q, want the canonical Go endpoint", opts.BaseURL)
	}
	for _, fact := range selector.reviewFacts[0] {
		if fact.Label == "Endpoint" && fact.Value != opencode.ProviderBaseURL("opencode-go") {
			t.Fatalf("review endpoint = %q", fact.Value)
		}
	}
}

// Each stage the engine names has its own words in the TUI, so the live line
// says what changed. A stage added later without its own line falls to the
// generic wording — a fixed table like this cannot catch that, it only pins
// the three stages the engine has today as distinct and non-empty.
func TestConnectStageTextNamesEveryStage(t *testing.T) {
	stages := []opencode.LoginStage{
		opencode.LoginStageRequestingCode,
		opencode.LoginStageWaitingAuthorization,
		opencode.LoginStageFinishing,
	}
	seen := make(map[string]bool, len(stages))
	for _, stage := range stages {
		text := connectStageText(stage)
		if text == "" {
			t.Fatalf("connectStageText(%q) is empty", stage)
		}
		if seen[text] {
			t.Fatalf("connectStageText(%q) = %q, already the wording of another stage", stage, text)
		}
		seen[text] = true
	}
}

// connectProgressSelector is a fakeSelector whose ConnectProgress delegates to
// a real rawSelector wired to a renderer, so a test can prove both that the
// line is installed through the production path and that it is taken off the
// screen at the end.
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

// The sign-in installs one live line whose text follows the stage, and the
// line is gone once the login is over — including its renderer-side source, so
// no orphaned row keeps counting after the flow returns.
func TestSignInOpenCodeShowsALiveProgressLine(t *testing.T) {
	stubOpenCodeModels(t, openCodeGoFixture(), nil)
	renderer := NewRenderer(nil, nil)
	sel := &connectProgressSelector{
		fakeSelector: &fakeSelector{confirms: []bool{true}},
		raw:          newRawSelector(nil, renderer),
	}

	// The stub walks the engine's stages and samples the live stage closure
	// once per stage, the way the renderer's paint would.
	var stageTexts []string
	previous := openCodeLogin
	openCodeLogin = func(_ context.Context, path string, _ io.Writer, _ func(opencode.LoginInfo), onStage func(opencode.LoginStage)) (opencode.Credentials, error) {
		for _, stage := range []opencode.LoginStage{
			opencode.LoginStageRequestingCode,
			opencode.LoginStageWaitingAuthorization,
			opencode.LoginStageFinishing,
		} {
			onStage(stage)
			stageTexts = append(stageTexts, sel.stage())
		}
		return opencode.Credentials{AccessToken: "go-token", RefreshToken: "go-refresh"}, nil
	}
	t.Cleanup(func() { openCodeLogin = previous })

	_, ok, err := runOpenCodeSetup(t, PromptConfig{
		Selector: sel, Output: io.Discard, Home: t.TempDir(),
	}, "opencode-go")
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if sel.installs != 1 {
		t.Fatalf("ConnectProgress installs = %d, want exactly one line for the whole login", sel.installs)
	}
	if len(stageTexts) != 3 {
		t.Fatalf("stage samples = %v, want one per stage", stageTexts)
	}
	for i := 1; i < len(stageTexts); i++ {
		if stageTexts[i] == stageTexts[i-1] {
			t.Fatalf("stage samples = %v, want the wording to change with each stage", stageTexts)
		}
	}
	if !sel.ended {
		t.Fatal("the live line was never ended after the login")
	}
	if renderer.TransientStatusSourceLive(transientSourceConnect) {
		t.Fatal("the connect source outlived its line: the row would keep counting on screen")
	}
}

// A Go model that publishes thinking levels asks for one on its own scale — the
// levels this build can send for that model, not a global list — and the flow
// then saves. The save is the point: the wizard used to walk the whole OpenCode
// login and be rejected at the last step because the provider carries no
// api_key.
func TestConfigureOpenCodeGoOffersPerModelEffortLevels(t *testing.T) {
	stubOpenCodeDeviceLogin(t)
	stubOpenCodeModels(t, openCodeGoProjection(t, "glm-5.3", "minimax-m3"), nil)
	home := t.TempDir()
	selector := &fakeSelector{
		selects:  []string{"glm-5.3", "high"},
		confirms: []bool{true},
	}
	opts, ok, err := runOpenCodeSetup(t, PromptConfig{
		Selector: selector, Output: io.Discard, Home: home,
	}, "opencode-go")
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if len(selector.selectOptions) < 2 {
		t.Fatalf("select pages = %#v, want a model page and an effort page", selector.selectOptions)
	}
	if got := selector.selectOptions[1]; !reflect.DeepEqual(got, []string{"low", "high", "max", reasoningEffortUnset}) {
		t.Fatalf("effort options = %#v, want glm-5.3's own levels", got)
	}
	if got := selector.selectDefaults[1]; got != reasoningEffortUnset {
		t.Fatalf("effort default = %q, want the provider default", got)
	}
	if opts.Model != "glm-5.3" || opts.ParamsJSON != `{"reasoning":{"effort":"high"}}` {
		t.Fatalf("options = %+v", opts)
	}
	// The review shows the chosen level rather than "Provider default".
	effortShown := false
	for _, fact := range selector.reviewFacts[0] {
		if fact.Label == "Reasoning" {
			effortShown = fact.Value == "high"
		}
	}
	if !effortShown {
		t.Fatalf("review facts = %+v, want Reasoning high", selector.reviewFacts[0])
	}

	// And the answers save: the OpenCode provider writes no api_key, so the
	// gate must not demand one.
	cfgPath := filepath.Join(home, "forebrain.yaml")
	rt := process.Context{Home: home, ConfigPath: cfgPath, Config: appcfg.Root{}}
	if _, err := process.Execute(rt, opts); err != nil {
		t.Fatalf("save the OpenCode Go setup: %v", err)
	}
	saved, err := appcfg.Load(cfgPath)
	if err != nil {
		t.Fatalf("load the saved config: %v", err)
	}
	validation := appcfg.ValidateAgentLLMConfigured(&saved, "main")
	if validation.Provider != "opencode-go" || validation.Model != "glm-5.3" {
		t.Fatalf("saved provider/model = %q/%q", validation.Provider, validation.Model)
	}
	if validation.BaseURL != opencode.ProviderBaseURL("opencode-go") {
		t.Fatalf("saved base_url = %q", validation.BaseURL)
	}
	if validation.APIKey != "" {
		t.Fatalf("saved api_key = %q, want none for a login-backed provider", validation.APIKey)
	}
	if !validation.Complete() {
		t.Fatalf("saved configuration is incomplete: %+v", validation.MissingFields())
	}
}

// A model that reasons with no discrete level (a toggle or a token budget) gets
// no effort page: there is nothing to pick.
func TestConfigureOpenCodeGoSkipsEffortForToggleOnlyModel(t *testing.T) {
	stubOpenCodeDeviceLogin(t)
	stubOpenCodeModels(t, openCodeGoProjection(t, "minimax-m3"), nil)
	selector := &fakeSelector{selects: []string{"minimax-m3"}, confirms: []bool{true}}
	opts, ok, err := runOpenCodeSetup(t, PromptConfig{
		Selector: selector, Output: io.Discard, Home: t.TempDir(),
	}, "opencode-go")
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if len(selector.selectOptions) != 1 {
		t.Fatalf("select pages = %#v, want only the model page", selector.selectOptions)
	}
	if opts.Model != "minimax-m3" || opts.ParamsJSON != "" {
		t.Fatalf("options = %+v, want no effort written", opts)
	}
}

// A catalog fetch that fails after a successful login offers a retry that reuses
// the saved login; declining aborts with an actionable error and writes no
// provider configuration.
func TestConfigureOpenCodeGoFetchFailureAborts(t *testing.T) {
	loginPaths := stubOpenCodeDeviceLogin(t)
	stubOpenCodeModels(t, nil, errors.New("fetch OpenCode models: http 401: token expired"))
	selector := &fakeSelector{confirms: []bool{true, false}, failUnexpectedSelect: true}
	opts, ok, err := runOpenCodeSetup(t, PromptConfig{
		Selector: selector, Output: io.Discard, Home: t.TempDir(),
	}, "opencode-go")
	if err == nil || ok {
		t.Fatalf("err=%v ok=%t, want the flow to abort", err, ok)
	}
	if !strings.Contains(err.Error(), "could not be fetched") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want an actionable fetch failure", err)
	}
	if opts.Provider != "" {
		t.Fatalf("options = %+v, want none written", opts)
	}
	if len(*loginPaths) != 1 {
		t.Fatalf("device logins = %d, want exactly the one that succeeded", len(*loginPaths))
	}
}

// Accepting the retry refetches on the saved login — no second device flow —
// and the flow then continues with the fresh listing.
func TestConfigureOpenCodeGoFetchRetrySkipsRelogin(t *testing.T) {
	loginPaths := stubOpenCodeDeviceLogin(t)
	fetchCalls := 0
	previous := discoverOpenCodeModels
	discoverOpenCodeModels = func(_ context.Context, _ string) ([]turn.ModelRecord, error) {
		fetchCalls++
		if fetchCalls == 1 {
			return nil, errors.New("fetch OpenCode models: http 429: slow down")
		}
		return openCodeGoFixture(), nil
	}
	t.Cleanup(func() { discoverOpenCodeModels = previous })
	selector := &fakeSelector{confirms: []bool{true, true}}
	opts, ok, err := runOpenCodeSetup(t, PromptConfig{
		Selector: selector, Output: io.Discard, Home: t.TempDir(),
	}, "opencode-go")
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if len(*loginPaths) != 1 {
		t.Fatalf("device logins = %d, want the retry to reuse the saved login", len(*loginPaths))
	}
	if fetchCalls != 2 {
		t.Fatalf("fetch calls = %d, want exactly one retry", fetchCalls)
	}
	if opts.Model != "glm-5.3" {
		t.Fatalf("model = %q", opts.Model)
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
			got, ok, err := promptReasoningEffort(PromptConfig{Selector: selector}, providerChoice{Provider: tc.provider}, tc.model, nil, "")
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
