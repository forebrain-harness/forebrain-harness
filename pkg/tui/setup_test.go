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
func useSetupTestCatalog(t *testing.T) {
	t.Helper()
	cat, err := llm.Parse([]byte(`{
		"anthropic/claude-sonnet-4-6": {"name": "Claude Sonnet 4.6", "reasoning": true,
			"limit": {"context": 200000, "output": 64000}},
		"anthropic/claude-haiku-lite": {"name": "Claude Haiku Lite", "reasoning": false,
			"limit": {"context": 200000, "output": 32000}},
		"alibaba/qwen3.5-plus": {"name": "Qwen3.5 Plus", "reasoning": true,
			"limit": {"context": 131072, "output": 8192}},
		"google/gemini-3.1-pro-preview": {"name": "Gemini 3.1 Pro Preview", "reasoning": true,
			"limit": {"context": 1000000, "output": 65536}},
		"minimax/MiniMax-M2.7": {"name": "MiniMax M2.7", "reasoning": true,
			"limit": {"context": 204800, "output": 24576}},
		"openai/gpt-5.5": {"name": "GPT-5.5", "reasoning": true,
			"limit": {"context": 272000, "output": 128000}}
	}`))
	if err != nil {
		t.Fatalf("parse test catalog: %v", err)
	}
	llm.SetGlobalCatalogForTest(cat)
	if embedded, err := llm.Parse(llm.EmbeddedModelsJSON()); err == nil {
		t.Cleanup(func() { llm.SetGlobalCatalogForTest(embedded) })
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

// Manual input stays reachable and stores the typed id; with no verified
// record the effort prompt defaults to unset instead of guessing a level.
func TestConfigureChatGPTProviderManualInputDefaultsEffortUnset(t *testing.T) {
	stubChatGPTBrowserLogin(t)
	stubChatGPTModels(t, discoveredFixture(), nil)
	selector := &fakeSelector{
		selects:  []string{manualModelInput, reasoningEffortUnset},
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
	if got := selector.selectDefaults[1]; got != reasoningEffortUnset {
		t.Fatalf("effort default = %q, want unset for an unverified model", got)
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
	if embedded, err := llm.Parse(llm.EmbeddedModelsJSON()); err == nil {
		t.Cleanup(func() { llm.SetGlobalCatalogForTest(embedded) })
	}
	if got := len(catalogModelOptions("openai")); got != 45 {
		t.Fatalf("options = %d, want all 45", got)
	}
}
