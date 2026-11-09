package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestActiveStateRootMainAgent(t *testing.T) {
	home := t.TempDir()
	cfg := Root{}
	cfg.Agents.Definitions = map[string]AgentDefinition{}

	// Main agent's per-agent state root is <home>/workspace.
	require.Equal(t, filepath.Join(home, "workspace"), ActiveStateRoot(home, &cfg))
}

func TestActiveStateRootNonMainAgentIsIsolated(t *testing.T) {
	home := t.TempDir()
	cfg := Root{}
	cfg.Agents.Definitions = map[string]AgentDefinition{
		"main":   {},
		"review": {Primary: true},
	}
	r, err := NewResolver(home, &cfg)
	require.NoError(t, err)

	// Switch the persisted active agent to "review"; a fresh ActiveStateRoot
	// resolution (which builds its own resolver) must follow it.
	_, err = r.Switch("review")
	require.NoError(t, err)

	got := ActiveStateRoot(home, &cfg)
	require.Equal(t, filepath.Join(home, "workspaces", "review"), got)
	// Crucially, NOT the main agent's workspace — that is the isolation guarantee.
	require.NotEqual(t, filepath.Join(home, "workspace"), got)
}

func TestActiveStateRootFallsBackOnEmptyHome(t *testing.T) {
	// NewResolver fails on empty home; ActiveStateRoot must not panic and must
	// return the documented <home>/workspace fallback (here: "workspace").
	require.Equal(t, "workspace", ActiveStateRoot("", &Root{}))
}

func TestActiveStateRootFallsBackOnNilCfg(t *testing.T) {
	home := t.TempDir()
	// nil cfg is tolerated (treated as empty config → main agent).
	require.Equal(t, filepath.Join(home, "workspace"), ActiveStateRoot(home, nil))
}

func TestProjectExampleForebrainYAMLLoads(t *testing.T) {
	t.Setenv("FOREBRAIN_HOME", t.TempDir())

	path := filepath.Clean(filepath.Join("..", "..", "forebrain.yaml"))
	cfg, err := loadDocumentForTest(path)
	if err != nil {
		t.Fatalf("loadDocumentForTest(%s): %v", path, err)
	}

	if len(cfg.Agents.Defaults.MCPServers) < 2 {
		t.Fatalf("agents.defaults.mcp_servers len = %d, want at least 2 examples", len(cfg.Agents.Defaults.MCPServers))
	}
	if got := cfg.Agents.Defaults.MCPServers[0].OAuth.Mode; got != "refresh" {
		t.Fatalf("first mcp oauth mode = %q, want refresh", got)
	}
	if got := cfg.Agents.Defaults.MCPServers[1].OAuth.Mode; got != "none" {
		t.Fatalf("second mcp oauth mode = %q, want none", got)
	}
}

func TestLLMRequestParamsAndProviderHelpers(t *testing.T) {
	t.Parallel()
	var p LLMRequestParams
	if err := p.UnmarshalJSON([]byte(`{"temperature":0.1}`)); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if got := string(p.Bytes()); got != `{"temperature":0.1}` {
		t.Fatalf("Bytes = %q", got)
	}
	if _, err := p.MarshalYAML(); err != nil {
		t.Fatalf("MarshalYAML: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"provider":"openai","model":"gpt","params":{"temperature":0.1}}`), &AgentLLMProviderConfig{}); err != nil {
		t.Fatalf("json.Unmarshal provider config should not fail: %v", err)
	}
	def := AgentDefinition{LLMProviders: []AgentLLMProviderConfig{{Provider: "a"}, {Provider: "b"}}}
	if got := ResolvedLLMConfigs(def); len(got) != 2 || got[0].Provider != "a" {
		t.Fatalf("ResolvedLLMConfigs = %#v", got)
	}
	if got := EffectiveDefaultLLM(def); got == nil || got.Provider != "a" {
		t.Fatalf("EffectiveDefaultLLM = %#v", got)
	}
	cfg := AgentLLMProviderConfig{}
	if err := json.Unmarshal([]byte(`{"provider":"openai","model":"gpt","api_key":"k","params":{"temperature":0.1}}`), &cfg); err != nil {
		t.Fatalf("AgentLLMProviderConfig UnmarshalJSON: %v", err)
	}
	if cfg.APIKey != "k" || string(cfg.Params) != `{"temperature":0.1}` {
		t.Fatalf("unexpected provider config: %+v", cfg)
	}
	if got := CloneAgentLLMProviderConfig(cfg); !reflect.DeepEqual(got, cfg) {
		t.Fatalf("CloneAgentLLMProviderConfig = %+v", got)
	}
	if got, err := ParseLLMRequestParamsJSON(`{"a":1}`); err != nil || len(got) == 0 {
		t.Fatalf("ParseLLMRequestParamsJSON = %q err=%v", string(got), err)
	}
	if got := ResolvedLLMConfigs(AgentDefinition{}); got != nil {
		t.Fatalf("ResolvedLLMConfigs empty = %#v", got)
	}
}

func TestChatGPTProviderDoesNotRequireAPIKeyInConfig(t *testing.T) {
	cfg := &Root{Agents: AgentsSection{Definitions: map[string]AgentDefinition{
		"main": {LLMProviders: []AgentLLMProviderConfig{{
			Provider: "chatgpt",
			Model:    "gpt-5.5",
			BaseURL:  "https://chatgpt.com/backend-api/codex",
			APIPath:  "/responses",
		}}},
	}}}
	validation := ValidateMainAgentLLMConfigured(cfg)
	if !validation.Complete() {
		t.Fatalf("ChatGPT provider should be complete, missing %v", validation.MissingFields())
	}
}

func TestMCPToolApprovalModeResolution(t *testing.T) {
	server := MCPServerConfig{
		DefaultToolsApprovalMode: MCPToolApprovalWrites,
		Tools: map[string]MCPServerToolConfig{
			"read": {ApprovalMode: MCPToolApprovalApprove},
		},
	}
	if got := server.ToolApprovalMode("read"); got != MCPToolApprovalApprove {
		t.Fatalf("tool mode=%q", got)
	}
	if got := server.ToolApprovalMode("other"); got != MCPToolApprovalWrites {
		t.Fatalf("server mode=%q", got)
	}
	if got := (MCPServerConfig{}).ToolApprovalMode("other"); got != MCPToolApprovalAuto {
		t.Fatalf("default mode=%q", got)
	}
}

func TestMCPToolApprovalModeValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`agents:
  defaults:
    mcp_servers:
      - name: demo
        default_tools_approval_mode: sometimes
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDocumentForTest(path); err == nil {
		t.Fatal("invalid MCP tool approval mode was accepted")
	}
}

func saveAndRead(t *testing.T, r Root) (Root, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	if err := Save(path, r); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	loaded, err := loadDocumentForTest(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return loaded, string(raw)
}

// `key: {}` documents neither the setting nor the value in force. Optional
// settings are written with their resolved value, and a section that has
// nothing to say is omitted.
func TestSaveWritesNoEmptyMappings(t *testing.T) {
	_, text := saveAndRead(t, Root{})
	for _, line := range strings.Split(text, "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), "{}") || strings.HasSuffix(strings.TrimSpace(line), "[]") {
			t.Errorf("empty value written: %q\n%s", line, text)
		}
	}
	// The sections that used to be written as `{}` now carry their defaults.
	for _, want := range []string{
		"features:", "memories:", "compact:",
		"memories: true", "max_unused_days: 30",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("persisted config missing %q:\n%s", want, text)
		}
	}
}

// The values written to forebrain.yaml must be the values the runtime uses. Both
// resolve through the same accessors, so a saved default can never describe
// behavior that differs from the running configuration.
func TestSavedDefaultsMatchRuntimeDefaults(t *testing.T) {
	unset := Root{}
	loaded, text := saveAndRead(t, unset)

	if got, want := loaded.EffectiveFeatures(), unset.EffectiveFeatures(); !reflect.DeepEqual(got, want) {
		t.Errorf("features: persisted %+v, runtime %+v\n%s", got, want, text)
	}
	if got, want := loaded.EffectiveMemories(), unset.EffectiveMemories(); !reflect.DeepEqual(got, want) {
		t.Errorf("memories: persisted %+v, runtime %+v\n%s", got, want, text)
	}
}

// Materializing defaults must never overwrite a choice the user made, in
// particular an explicit false on a setting that defaults to true.
func TestSaveKeepsExplicitFalseOverDefaultTrue(t *testing.T) {
	r := Root{}
	r.Features.Memories = BoolPtr(false)
	r.Features.LSP = BoolPtr(false)

	loaded, text := saveAndRead(t, r)
	if loaded.EffectiveFeatures().Memories {
		t.Errorf("features.memories reverted to its default:\n%s", text)
	}
	if loaded.EffectiveFeatures().LSP {
		t.Errorf("features.lsp reverted to its default:\n%s", text)
	}
}

// The placeholder provider slot normalize keeps in memory is not persisted, and
// Load puts it back, so a save/load cycle is stable.
func TestSaveDropsPlaceholderLLMProviderAndLoadRestoresIt(t *testing.T) {
	loaded, text := saveAndRead(t, Root{})
	if strings.Contains(text, "llm_providers:") {
		t.Errorf("placeholder provider persisted:\n%s", text)
	}
	if got := len(loaded.Agents.Definitions["main"].LLMProviders); got != 1 {
		t.Errorf("main provider slot not restored on load: got %d entries", got)
	}
}

func TestSaveKeepsConfiguredLLMProvider(t *testing.T) {
	r := Root{}
	normalize(&r)
	main := r.Agents.Definitions["main"]
	main.LLMProviders = []AgentLLMProviderConfig{{Model: "gpt-5"}}
	r.Agents.Definitions["main"] = main

	loaded, text := saveAndRead(t, r)
	if !strings.Contains(text, "gpt-5") {
		t.Fatalf("configured provider dropped:\n%s", text)
	}
	if got := loaded.Agents.Definitions["main"].LLMProviders[0].Model; got != "gpt-5" {
		t.Fatalf("provider model = %q, want gpt-5", got)
	}
}

// DangerFullAccessEnabled reads the derived sandbox mode and nothing else: a
// named permission profile used to outrank it, and profiles are gone (plan
// 203), so the mode the preset derives is the single answer.
func TestDangerFullAccessEnabledFollowsSandboxMode(t *testing.T) {
	if !(&Root{SandboxMode: SandboxModeDangerFullAccess}).DangerFullAccessEnabled() {
		t.Fatal("danger-full-access sandbox mode must disable the sandbox")
	}
	if (&Root{SandboxMode: SandboxModeWorkspaceWrite}).DangerFullAccessEnabled() {
		t.Fatal("workspace-write must keep the sandbox")
	}
	if (&Root{}).DangerFullAccessEnabled() {
		t.Fatal("an unset mode must not report full access")
	}
}

func TestResolverDefaultsMainWorkspaceAndPrivatePaths(t *testing.T) {
	home := t.TempDir()
	cfg := Root{}
	cfg.Agents.Definitions = map[string]AgentDefinition{}
	r, err := NewResolver(home, &cfg)
	require.NoError(t, err)

	active, err := r.Active()
	require.NoError(t, err)
	require.Equal(t, "main", active.ID)
	require.Equal(t, filepath.Join(home, "workspace"), active.WorkspaceRoot)
	require.Equal(t, filepath.Join(home, "workspace", "skills"), active.PrivateSkillsRoot)
	require.Contains(t, active.SharedSkillsRoots, filepath.Join(home, ".forebrain", "skills"))
	require.Contains(t, active.SharedSkillsRoots, filepath.Join(home, "skills"))
	require.Contains(t, active.SharedSkillsRoots, filepath.Join(home, ".agents", "skills"))
	require.Contains(t, active.SharedSkillsRoots, filepath.Join(home, "skills", ".system"))
}

func TestSwitchPersistsActivePrimary(t *testing.T) {
	home := t.TempDir()
	cfg := Root{}
	cfg.Agents.Definitions = map[string]AgentDefinition{
		"main":   {},
		"review": {Primary: true},
	}
	r, err := NewResolver(home, &cfg)
	require.NoError(t, err)

	switched, err := r.Switch("rev")
	require.NoError(t, err)
	require.Equal(t, "review", switched.ID)

	raw, err := os.ReadFile(filepath.Join(home, "state", "primary-agent.json"))
	require.NoError(t, err)
	require.Contains(t, string(raw), `"active":"review"`)

	reloaded, err := NewResolver(home, &cfg)
	require.NoError(t, err)

	active, err := reloaded.Active()
	require.NoError(t, err)
	require.Equal(t, "review", active.ID)
}

func TestResolveSupportsAliasesAndUniquePrefixes(t *testing.T) {
	home := t.TempDir()
	cfg := Root{}
	cfg.Agents.Definitions = map[string]AgentDefinition{
		"main":     {},
		"review":   {Primary: true},
		"research": {Primary: true},
	}
	r, err := NewResolver(home, &cfg)
	require.NoError(t, err)

	got, err := r.Resolve("default")
	require.NoError(t, err)
	require.Equal(t, "main", got.ID)

	got, err = r.Resolve("agent")
	require.NoError(t, err)
	require.Equal(t, "main", got.ID)

	got, err = r.Resolve("rev")
	require.NoError(t, err)
	require.Equal(t, "review", got.ID)

	_, err = r.Resolve("")
	require.ErrorContains(t, err, "empty")

	_, err = r.Resolve("re")
	require.ErrorContains(t, err, "ambiguous")

	_, err = r.Resolve("missing")
	require.ErrorContains(t, err, "not found")
}

func TestPathsReturnsSiblingWorkspacesAndDefensiveCopies(t *testing.T) {
	home := t.TempDir()
	cfg := Root{}
	cfg.Agents.Definitions = map[string]AgentDefinition{
		"main":   {},
		"review": {Primary: true},
	}
	r, err := NewResolver(home, &cfg)
	require.NoError(t, err)

	paths, err := r.Paths()
	require.NoError(t, err)
	require.Equal(t, "main", paths.Active.ID)
	require.Equal(t, []string{filepath.Join(home, "workspaces", "review")}, paths.SiblingWorkspaces)

	paths.Active.SharedSkillsRoots[0] = "mutated"
	paths.All[0].SharedSkillsRoots[0] = "mutated-too"

	active, err := r.Active()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".forebrain", "skills"), active.SharedSkillsRoots[0])
}

func TestResolverTreatsReservedDefinitionsAsHardCoded(t *testing.T) {
	home := t.TempDir()
	cfg := Root{}
	cfg.Agents.Definitions = map[string]AgentDefinition{
		"main":    {Primary: false},
		"review":  {Primary: true},
		"explore": {Primary: true},
	}
	r, err := NewResolver(home, &cfg)
	require.NoError(t, err)

	active, err := r.Active()
	require.NoError(t, err)
	require.Equal(t, "main", active.ID)

	var ids []string
	for _, item := range r.All() {
		ids = append(ids, item.ID)
	}
	require.ElementsMatch(t, []string{"main", "review"}, ids)
}

func TestLoadRejectsPlaintextSecretBeforeRuntimeUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	body := strings.Join([]string{
		"unknown:",
		"  webhook_secret: plaintext",
		"agents:",
		"  definitions: {}",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load error = nil, want plaintext secret rejection")
	}
	msg := err.Error()
	if !strings.Contains(msg, path) || !strings.Contains(msg, "unknown.webhook_secret") {
		t.Fatalf("error must include config path and key path, got %q", msg)
	}
}

func TestLoadAllowsEnvReferenceSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	body := strings.Join([]string{
		"webhook:",
		"  token: ${FOREBRAIN_WEBHOOK_TOKEN}",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := loadDocumentForTest(path); err != nil {
		t.Fatalf("Load error = %v, want env reference secret allowed", err)
	}
}

func TestLoadExpandsYAMLEnvReferenceFromProcess(t *testing.T) {
	// Isolate FOREBRAIN_HOME so Load reads nothing of the developer's own home.
	t.Setenv("FOREBRAIN_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	body := strings.Join([]string{
		"gateway:",
		"  auth:",
		"    token: ${FOREBRAIN_GATEWAY_TOKEN}",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("FOREBRAIN_GATEWAY_TOKEN", "from-process-env")

	cfg, err := loadDocumentForTest(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if got := cfg.Gateway.Auth.Token; got != "from-process-env" {
		t.Fatalf("gateway token = %q, want process env value", got)
	}
}

func TestLoadExpandsOnlyBracedYAMLEnvReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	body := strings.Join([]string{
		"gateway:",
		"  http_addr: token ${BRACED_TOKEN} $UNBRACED_TOKEN",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("BRACED_TOKEN", "expanded")
	t.Setenv("UNBRACED_TOKEN", "must-not-expand")

	cfg, err := loadDocumentForTest(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	want := "token expanded $UNBRACED_TOKEN"
	if got := cfg.Gateway.HTTPAddr; got != want {
		t.Fatalf("gateway.http_addr = %q, want %q", got, want)
	}
}

func TestLoadExpandsYAMLEnvReferenceFromTheProcessEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", home)
	t.Setenv("CUSTOM_GATEWAY_ADDR", "127.0.0.1:9191")
	path := filepath.Join(home, "forebrain.yaml")
	body := strings.Join([]string{
		"gateway:",
		"  http_addr: ${CUSTOM_GATEWAY_ADDR}",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := loadDocumentForTest(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if got := cfg.Gateway.HTTPAddr; got != "127.0.0.1:9191" {
		t.Fatalf("gateway http_addr = %q, want the environment's value", got)
	}
}

func TestLoadPersistedKeepsYAMLEnvReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	body := strings.Join([]string{
		"gateway:",
		"  auth:",
		"    token: ${FOREBRAIN_GATEWAY_TOKEN}",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("FOREBRAIN_GATEWAY_TOKEN", "runtime-secret")

	cfg, err := loadPersistedDocumentForTest(path)
	if err != nil {
		t.Fatalf("LoadPersisted error: %v", err)
	}
	if got := cfg.Gateway.Auth.Token; got != "${FOREBRAIN_GATEWAY_TOKEN}" {
		t.Fatalf("persisted gateway token = %q, want original env reference", got)
	}
}

func TestLoadAllowsGatewayAuthModeBoolFreeConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	body := strings.Join([]string{
		"gateway:",
		"  auth:",
		"    mode: token",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := loadDocumentForTest(path); err != nil {
		t.Fatalf("Load error = %v, want non-secret gateway auth fields allowed", err)
	}
}

func TestLoadPersistedSkipsRuntimeEnvSecretsForReadModifyWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", home)
	t.Setenv("OPENAI_API_KEY", "runtime-only-not-for-yaml")

	path := filepath.Join(home, "forebrain.yaml")
	body := "agents:\n  definitions:\n    main:\n      llm_providers:\n        - provider: openai\n          model: gpt-test\n          api_key: ${OPENAI_API_KEY}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtimeCfg, err := loadDocumentForTest(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if got := runtimeCfg.Agents.Definitions["main"].LLMProviders[0].APIKey; got != "runtime-only-not-for-yaml" {
		t.Fatalf("runtime Load API key = %q, want the expansion of the reference", got)
	}

	persistedCfg, err := loadPersistedDocumentForTest(path)
	if err != nil {
		t.Fatalf("LoadPersisted error: %v", err)
	}
	if got := persistedCfg.Agents.Definitions["main"].LLMProviders[0].APIKey; got != "${OPENAI_API_KEY}" {
		t.Fatalf("LoadPersisted API key = %q, want the reference as written", got)
	}
	if err := Save(path, persistedCfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if strings.Contains(string(saved), "runtime-only-not-for-yaml") {
		t.Fatalf("saved config leaked runtime env secret: %s", saved)
	}
}

func TestSaveDoesNotPersistRuntimeEnvSecretsLoadedFromTheEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", home)
	t.Setenv("FOREBRAIN_GATEWAY_TOKEN", "runtime-gateway-token")
	t.Setenv("TAVILY_API_KEY", "runtime-tavily-key")
	path := filepath.Join(home, "forebrain.yaml")
	body := strings.Join([]string{
		"sandbox_mode: read-only",
		"gateway:",
		"  auth:",
		"    token: ${FOREBRAIN_GATEWAY_TOKEN}",
		"agents:",
		"  defaults:",
		"    web_search:",
		"      tavily:",
		"        api_key: ${TAVILY_API_KEY}",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := loadDocumentForTest(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Gateway.Auth.Token != "runtime-gateway-token" || cfg.Agents.Defaults.WebSearch.Tavily == nil || cfg.Agents.Defaults.WebSearch.Tavily.APIKey != "runtime-tavily-key" {
		t.Fatalf("Load did not apply runtime secrets: %+v", cfg)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	text := string(saved)
	if strings.Contains(text, "runtime-gateway-token") || strings.Contains(text, "runtime-tavily-key") {
		t.Fatalf("Save leaked runtime secret: %s", text)
	}
	if !strings.Contains(text, "primary: false") {
		t.Fatalf("Save did not preserve explicit false: %s", text)
	}
}

// Restored after the unused helpers these tests also touched were removed:
// the assertions below cover live code and would otherwise have gone with
// them.

func TestGatewayHelpers(t *testing.T) {
	t.Parallel()
	root := &Root{
		Gateway: Gateway{
			Auth: GatewayAuth{Token: "gw-token"},
		},
		Agents: AgentsSection{Definitions: map[string]AgentDefinition{
			"main": {},
			"acme": {Primary: true},
		}},
	}
	mainChannels := []ChannelsSection{{
		Webhook: Webhook{Enabled: true, InboundPath: "/webhook"},
		Slack:   Slack{Enabled: true, InboundPath: "/slack"},
		Email:   Email{Enabled: true, InboundPath: "/email"},
	}}
	if !GatewayControlPlaneAuthExemptPath("/health", mainChannels) {
		t.Fatal("health path should be exempt")
	}
	if !GatewayControlPlaneAuthExemptPath("/channels/x/health", mainChannels) {
		t.Fatal("channel health path should be exempt")
	}
	if !GatewayControlPlaneAuthExemptPath("/webhook", mainChannels) {
		t.Fatal("webhook inbound should be exempt")
	}
	if GatewayControlPlaneAuthExemptPath("/other", mainChannels) {
		t.Fatal("unexpected exemption")
	}
	// Only the active agent's channels are mounted, so only its inbound paths
	// bypass control-plane auth: another agent's endpoint is not open.
	if GatewayControlPlaneAuthExemptPath("/webhook", nil) {
		t.Fatal("one agent's inbound path exempted a request while another agent is active")
	}
	if GatewayAllowsAnonymousGET("") || GatewayAllowsAnonymousGET("/api/v1") || GatewayAllowsAnonymousGET("/ws/chat") {
		t.Fatal("anonymous GET helpers returned unexpected true")
	}
	if !GatewayAllowsAnonymousGET("/docs") {
		t.Fatal("docs path should allow anonymous GET")
	}
	if root.Gateway.Auth.Token != "gw-token" {
		t.Fatalf("gateway auth helpers returned unexpected config: %+v", root.Gateway.Auth)
	}
}
