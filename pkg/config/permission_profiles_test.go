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
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
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

func TestProviderLookups(t *testing.T) {
	t.Parallel()
	get := func(k string) string {
		switch k {
		case "OPENAI_API_KEY":
			return " openai "
		case "ANTHROPIC_API_KEY":
			return " anthropic "
		case "DEEPSEEK_API_KEY":
			return " deepseek "
		case "OPENAI_BASE_URL":
			return " https://openai.example "
		case "ANTHROPIC_BASE_URL":
			return " https://anthropic.example "
		case "DEEPSEEK_BASE_URL":
			return " https://deepseek.example "
		default:
			return ""
		}
	}
	if got := ProviderAPIKeyLookup(get, "openai"); got != "openai" {
		t.Fatalf("ProviderAPIKeyLookup openai = %q", got)
	}
	if got := ProviderAPIKeyLookup(get, "anthropic"); got != "anthropic" {
		t.Fatalf("ProviderAPIKeyLookup anthropic = %q", got)
	}
	if got := ProviderAPIKeyLookup(get, "deepseek"); got != "deepseek" {
		t.Fatalf("ProviderAPIKeyLookup deepseek = %q", got)
	}
	if got := ProviderBaseURLLookup(get, "openai"); got != "https://openai.example" {
		t.Fatalf("ProviderBaseURLLookup openai = %q", got)
	}
	if got := ProviderBaseURLLookup(get, "anthropic"); got != "https://anthropic.example" {
		t.Fatalf("ProviderBaseURLLookup anthropic = %q", got)
	}
	if got := ProviderBaseURLLookup(get, "deepseek"); got != "https://deepseek.example" {
		t.Fatalf("ProviderBaseURLLookup deepseek = %q", got)
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
	if got := PrimaryLLM(def); got == nil || got.Provider != "a" {
		t.Fatalf("PrimaryLLM = %#v", got)
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

func TestGoCacheModeDefaultsAndValidation(t *testing.T) {
	if got := (SandboxWorkspaceWrite{}).EffectiveGoCacheMode(); got != GoCacheModeIsolated {
		t.Fatalf("default mode=%q want=%q", got, GoCacheModeIsolated)
	}
	for _, mode := range []string{GoCacheModeIsolated, GoCacheModeShared, GoCacheModeDisabled, " SHARED "} {
		cfg := &Root{SandboxWorkspaceWrite: SandboxWorkspaceWrite{GoCacheMode: mode}}
		if err := validateSandboxConfig(cfg); err != nil {
			t.Fatalf("mode %q rejected: %v", mode, err)
		}
	}
	if err := validateSandboxConfig(&Root{SandboxWorkspaceWrite: SandboxWorkspaceWrite{GoCacheMode: "host"}}); err == nil {
		t.Fatal("invalid Go cache mode was accepted")
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
	if _, err := Load(path); err == nil {
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
	loaded, err := Load(path)
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
		"features:", "memories:", "command_rewrite:", "compact:",
		"memories: true", "generate_memories: true", "enabled: true", "remote_compaction_v2: true",
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
	for _, c := range []struct {
		name      string
		got, want bool
	}{
		{"assembly.remote_compaction", loaded.Compact.UseRemoteCompaction(), unset.Compact.UseRemoteCompaction()},
		{"assembly.remote_compaction_v2", loaded.Compact.UseRemoteV2(), unset.Compact.UseRemoteV2()},
		{"tools.command_rewrite", loaded.Tools.CommandRewrite.UseRewrite(), unset.Tools.CommandRewrite.UseRewrite()},
		{"windows.sandbox_private_desktop", loaded.Windows.UseSandboxPrivateDesktop(), unset.Windows.UseSandboxPrivateDesktop()},
	} {
		if c.got != c.want {
			t.Errorf("%s: persisted %v, runtime %v\n%s", c.name, c.got, c.want, text)
		}
	}
}

// Materializing defaults must never overwrite a choice the user made, in
// particular an explicit false on a setting that defaults to true.
func TestSaveKeepsExplicitFalseOverDefaultTrue(t *testing.T) {
	r := Root{}
	r.Features.Memories = BoolPtr(false)
	r.Memories.GenerateMemories = BoolPtr(false)
	r.Tools.CommandRewrite.Enabled = BoolPtr(false)
	r.Windows.SandboxPrivateDesktop = BoolPtr(false)

	loaded, text := saveAndRead(t, r)
	if loaded.EffectiveFeatures().Memories {
		t.Errorf("features.memories reverted to its default:\n%s", text)
	}
	if loaded.EffectiveMemories().GenerateMemories {
		t.Errorf("memories.generate_memories reverted to its default:\n%s", text)
	}
	if loaded.Tools.CommandRewrite.UseRewrite() {
		t.Errorf("tools.command_rewrite.enabled reverted to its default:\n%s", text)
	}
	if loaded.Windows.UseSandboxPrivateDesktop() {
		t.Errorf("windows.sandbox_private_desktop reverted to its default:\n%s", text)
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

func TestPermissionProfileLoadsAndResolvesInheritance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	data := []byte(`
default_permissions: locked
permissions:
  locked:
    description: Restricted workspace
    extends: ":workspace"
    workspace_roots:
      ../shared: true
    filesystem:
      glob_scan_max_depth: 4
      ":workspace_roots":
        ".": write
        "**/.env": deny
      "/var/cache/**": write
    network:
      enabled: true
      mode: limited
      domains:
        "example.com": allow
        "blocked.example": deny
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadPersisted(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SandboxMode != SandboxModeWorkspaceWrite {
		t.Fatalf("sandbox mode=%q", cfg.SandboxMode)
	}
	profile, active, err := cfg.ResolvePermissionProfile()
	if err != nil || !active {
		t.Fatalf("active=%t err=%v", active, err)
	}
	if profile.Description != "Restricted workspace" || profile.FileSystem == nil || profile.FileSystem.GlobScanMaxDepth == nil || *profile.FileSystem.GlobScanMaxDepth != 4 {
		t.Fatalf("profile=%+v", profile)
	}
	if _, ok := profile.FileSystem.Entries[":root"]; !ok {
		t.Fatalf("built-in parent entries were not inherited: %+v", profile.FileSystem.Entries)
	}
	if profile.Network == nil || profile.Network.Enabled == nil || !*profile.Network.Enabled || profile.Network.Domains["example.com"] != NetworkAccessAllow {
		t.Fatalf("network=%+v", profile.Network)
	}
}

func TestPermissionProfileValidation(t *testing.T) {
	for name, body := range map[string]string{
		"missing selection": "permissions:\n  custom: {}\n",
		"reserved name":     "default_permissions: ':read-only'\npermissions:\n  ':custom': {}\n",
		"cycle":             "default_permissions: a\npermissions:\n  a: {extends: b}\n  b: {extends: a}\n",
		"bad glob":          "default_permissions: a\npermissions:\n  a:\n    filesystem:\n      '/tmp/*/x': write\n",
		"bad depth":         "default_permissions: a\npermissions:\n  a:\n    filesystem:\n      glob_scan_max_depth: 0\n",
		"parent subpath":    "default_permissions: a\npermissions:\n  a:\n    filesystem:\n      ':workspace_roots':\n        '..': deny\n",
		"dot subpath":       "default_permissions: a\npermissions:\n  a:\n    filesystem:\n      ':workspace_roots':\n        'dir/./file': deny\n",
		"empty action":      "default_permissions: a\npermissions:\n  a:\n    network:\n      mitm:\n        actions:\n          redact: {}\n",
		"empty hook action": "default_permissions: a\npermissions:\n  a:\n    network:\n      mitm:\n        hooks:\n          api:\n            host: example.com\n            methods: [GET]\n            path_prefixes: ['/']\n            action: []\n",
		"inactive bad mode": "default_permissions: a\npermissions:\n  a: {}\n  b:\n    network:\n      mode: invalid\n",
		"profile field":     "default_permissions: a\npermissions:\n  a:\n    unexpected: true\n",
		"network field":     "default_permissions: a\npermissions:\n  a:\n    network:\n      unexpected: true\n",
		"mitm field":        "default_permissions: a\npermissions:\n  a:\n    network:\n      mitm:\n        unexpected: true\n",
		"hook field":        "default_permissions: a\npermissions:\n  a:\n    network:\n      mitm:\n        hooks:\n          api:\n            host: example.com\n            methods: [GET]\n            path_prefixes: ['/']\n            action: [redact]\n            unexpected: true\n",
		"missing hook host": "default_permissions: a\npermissions:\n  a:\n    network:\n      mitm:\n        hooks:\n          api:\n            methods: [GET]\n            path_prefixes: ['/']\n            action: [redact]\n",
		"unknown subpath":   "default_permissions: a\npermissions:\n  a:\n    filesystem:\n      ':future':\n        '../secret': deny\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "forebrain.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadPersisted(path); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestPermissionProfileNetworkInheritance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	data := []byte(`
default_permissions: child
permissions:
  parent:
    network:
      proxy_url: http://127.0.0.1:3128
      domains:
        "EXAMPLE.COM.:443": deny
      mitm:
        actions:
          redact:
            strip_request_headers: [authorization]
        hooks:
          api:
            host: api.example.com
            methods: [GET]
            path_prefixes: ['/v1']
            query:
              source: [parent]
            action: [redact]
  child:
    extends: parent
    network:
      proxy_url: ""
      domains:
        "example.com": allow
      mitm:
        actions:
          inject:
            inject_request_headers:
              - name: x-secret
                secret_env_var: API_SECRET
        hooks:
          api:
            host: api.example.com
            methods: [POST]
            path_prefixes: ['/v2']
            headers:
              x-client: [forebrain]
            action: [inject]
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadPersisted(path)
	if err != nil {
		t.Fatal(err)
	}
	profile, active, err := cfg.ResolvePermissionProfile()
	if err != nil || !active || profile.Network == nil {
		t.Fatalf("active=%t profile=%+v err=%v", active, profile, err)
	}
	network := profile.Network
	if network.ProxyURL == nil || *network.ProxyURL != "" {
		t.Fatalf("proxy_url=%v", network.ProxyURL)
	}
	if len(network.Domains) != 1 || network.Domains["example.com"] != NetworkAccessAllow {
		t.Fatalf("domains=%+v", network.Domains)
	}
	if network.MITM == nil || len(network.MITM.Actions) != 2 {
		t.Fatalf("mitm=%+v", network.MITM)
	}
	hook := network.MITM.Hooks["api"]
	if len(hook.Methods) != 1 || hook.Methods[0] != "POST" || hook.Body != nil || hook.Query["source"][0] != "parent" || hook.Headers["x-client"][0] != "forebrain" {
		t.Fatalf("hook=%+v", hook)
	}
}

func TestBuiltInPermissionProfileSelectionSetsSandboxMode(t *testing.T) {
	for profile, mode := range map[string]SandboxMode{
		PermissionProfileReadOnly:         SandboxModeReadOnly,
		PermissionProfileWorkspace:        SandboxModeWorkspaceWrite,
		PermissionProfileDangerFullAccess: SandboxModeDangerFullAccess,
	} {
		cfg := Root{DefaultPermissions: profile}
		normalizeSandboxConfig(&cfg)
		if cfg.SandboxMode != mode {
			t.Fatalf("%s selected mode %s", profile, cfg.SandboxMode)
		}
	}
}

func TestDangerFullAccessUsesSelectedPermissionConfiguration(t *testing.T) {
	if (&Root{SandboxMode: SandboxModeDangerFullAccess, DefaultPermissions: "restricted"}).DangerFullAccessEnabled() {
		t.Fatal("a named permission configuration must take precedence over sandbox_mode")
	}
	if !(&Root{SandboxMode: SandboxModeReadOnly, DefaultPermissions: PermissionProfileDangerFullAccess}).DangerFullAccessEnabled() {
		t.Fatal("the unrestricted built-in permission configuration must disable the sandbox")
	}
	if !(&Root{SandboxMode: SandboxModeDangerFullAccess}).DangerFullAccessEnabled() {
		t.Fatal("danger-full-access sandbox mode must disable the sandbox when no permission configuration is selected")
	}
}

func TestExplicitBuiltInWorkspaceIgnoresWorkspaceSettings(t *testing.T) {
	cfg := Root{
		DefaultPermissions: PermissionProfileWorkspace,
		SandboxWorkspaceWrite: SandboxWorkspaceWrite{
			WritableRoots:       []string{"/configured/root"},
			NetworkAccess:       BoolPtr(true),
			ExcludeTmpdirEnvVar: true,
			ExcludeSlashTmp:     true,
		},
	}
	profile, active, err := cfg.ResolvePermissionProfile()
	if err != nil || !active {
		t.Fatalf("active=%t err=%v", active, err)
	}
	if profile.FileSystem == nil {
		t.Fatal("missing filesystem profile")
	}
	if _, ok := profile.FileSystem.Entries[":tmpdir"]; !ok {
		t.Fatal("explicit workspace profile must include tmpdir")
	}
	if _, ok := profile.FileSystem.Entries[":slash_tmp"]; !ok {
		t.Fatal("explicit workspace profile must include /tmp")
	}
	if _, ok := profile.FileSystem.Entries["/configured/root"]; ok {
		t.Fatal("configured writable roots must not become explicit profile entries")
	}
	if profile.Network != nil {
		t.Fatalf("network=%+v", profile.Network)
	}
}

func TestFilesystemDepthRejectsFractionalJSON(t *testing.T) {
	var filesystem FileSystemPermissions
	if err := json.Unmarshal([]byte(`{"glob_scan_max_depth":1.5}`), &filesystem); err == nil {
		t.Fatal("fractional glob_scan_max_depth was accepted")
	}
}

func TestResolvePermissionProfileErrorsNameTheSettingAtFault(t *testing.T) {
	for _, tc := range []struct {
		name     string
		selected string
		profiles PermissionProfiles
		want     string
	}{
		{
			name:     "undefined selection blames default_permissions",
			selected: "missing",
			want:     `default_permissions refers to undefined profile "missing"`,
		},
		{
			name:     "unknown built-in selection is rejected as built-in",
			selected: ":nope",
			want:     `default_permissions refers to unknown built-in profile ":nope"`,
		},
		{
			// default_permissions is correct here; the typo is in the parent.
			name:     "undefined parent blames the profile that extends it",
			selected: "team",
			profiles: PermissionProfiles{"team": {Extends: "bse"}},
			want:     `permissions profile "team" extends undefined profile "bse"`,
		},
		{
			name:     "a built-in that cannot be extended says so",
			selected: "team",
			profiles: PermissionProfiles{"team": {Extends: PermissionProfileDangerFullAccess}},
			want:     `permissions profile "team" cannot extend unsupported built-in profile ":danger-full-access"`,
		},
		{
			name:     "a cycle reports the path that closes it",
			selected: "a",
			profiles: PermissionProfiles{"a": {Extends: "b"}, "b": {Extends: "a"}},
			want:     "permissions profile inheritance cycle detected: a -> b -> a",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := &Root{DefaultPermissions: tc.selected, Permissions: tc.profiles}
			_, ok, err := root.ResolvePermissionProfile()
			if err == nil {
				t.Fatalf("expected an error, got ok=%v", ok)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}
}

// Only the two profiles that describe a sandbox may be extended; the
// full-access profile describes the absence of one.
func TestResolvePermissionProfileExtendsBuiltIns(t *testing.T) {
	for _, parent := range []string{PermissionProfileReadOnly, PermissionProfileWorkspace} {
		root := &Root{
			DefaultPermissions: "team",
			Permissions:        PermissionProfiles{"team": {Extends: parent}},
		}
		profile, ok, err := root.ResolvePermissionProfile()
		if err != nil || !ok {
			t.Fatalf("extends %s: ok=%v err=%v", parent, ok, err)
		}
		if profile.FileSystem == nil || profile.FileSystem.Entries[":root"].Access != FileSystemAccessRead {
			t.Fatalf("extends %s did not inherit the parent filesystem: %+v", parent, profile.FileSystem)
		}
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

	if _, err := Load(path); err != nil {
		t.Fatalf("Load error = %v, want env reference secret allowed", err)
	}
}

func TestLoadExpandsYAMLEnvReferenceFromProcess(t *testing.T) {
	// Isolate FOREBRAIN_HOME so Load does not merge the developer's real
	// ~/.forebrain/.env, which would override the process env set below.
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

	cfg, err := Load(path)
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

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	want := "token expanded $UNBRACED_TOKEN"
	if got := cfg.Gateway.HTTPAddr; got != want {
		t.Fatalf("gateway.http_addr = %q, want %q", got, want)
	}
}

func TestLoadExpandsYAMLEnvReferenceFromForebrainDotEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("CUSTOM_GATEWAY_ADDR=127.0.0.1:9191\n"), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	path := filepath.Join(home, "forebrain.yaml")
	body := strings.Join([]string{
		"gateway:",
		"  http_addr: ${CUSTOM_GATEWAY_ADDR}",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if got := cfg.Gateway.HTTPAddr; got != "127.0.0.1:9191" {
		t.Fatalf("gateway http_addr = %q, want dotenv value", got)
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

	cfg, err := LoadPersisted(path)
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

	if _, err := Load(path); err != nil {
		t.Fatalf("Load error = %v, want non-secret gateway auth fields allowed", err)
	}
}

func TestLoadPersistedSkipsRuntimeEnvSecretsForReadModifyWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", home)
	originalAPIKey := os.Getenv("OPENAI_API_KEY")
	t.Cleanup(func() {
		if originalAPIKey == "" {
			_ = os.Unsetenv("OPENAI_API_KEY")
			return
		}
		_ = os.Setenv("OPENAI_API_KEY", originalAPIKey)
	})

	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("OPENAI_API_KEY=runtime-only-not-for-yaml\n"), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	path := filepath.Join(home, "forebrain.yaml")
	body := "agents:\n  definitions:\n    main:\n      llm_providers:\n        - provider: openai\n          model: gpt-test\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtimeCfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if got := runtimeCfg.Agents.Definitions["main"].LLMProviders[0].APIKey; got != "runtime-only-not-for-yaml" {
		t.Fatalf("runtime Load API key = %q, want env override", got)
	}

	persistedCfg, err := LoadPersisted(path)
	if err != nil {
		t.Fatalf("LoadPersisted error: %v", err)
	}
	if got := persistedCfg.Agents.Definitions["main"].LLMProviders[0].APIKey; got != "" {
		t.Fatalf("LoadPersisted API key = %q, want empty persisted value", got)
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

func TestSaveDoesNotPersistRuntimeEnvSecretsLoadedFromDotEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("FOREBRAIN_GATEWAY_TOKEN=runtime-gateway-token\nTAVILY_API_KEY=runtime-tavily-key\n"), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	path := filepath.Join(home, "forebrain.yaml")
	if err := os.WriteFile(path, []byte("sandbox_mode: read-only\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
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
	if !strings.Contains(text, "enabled: false") {
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
			Auth: GatewayAuth{
				Mode:  "token",
				Token: "gw-token",
			},
		},
		Agents: AgentsSection{Definitions: map[string]AgentDefinition{
			"main": {Channels: ChannelsSection{
				Webhook: Webhook{Enabled: true, InboundPath: "/webhook"},
				Slack:   Slack{Enabled: true, InboundPath: "/slack"},
				Email:   Email{Enabled: true, InboundPath: "/email"},
			}},
			"acme": {Primary: true},
		}},
	}
	if !GatewayControlPlaneAuthExemptPath("/health", root, "main") {
		t.Fatal("health path should be exempt")
	}
	if !GatewayControlPlaneAuthExemptPath("/channels/x/health", root, "main") {
		t.Fatal("channel health path should be exempt")
	}
	if !GatewayControlPlaneAuthExemptPath("/webhook", root, "main") {
		t.Fatal("webhook inbound should be exempt")
	}
	if GatewayControlPlaneAuthExemptPath("/other", root, "main") {
		t.Fatal("unexpected exemption")
	}
	// Only the named agent's channels are mounted, so only its inbound paths
	// bypass control-plane auth: another agent's endpoint is not open.
	if GatewayControlPlaneAuthExemptPath("/webhook", root, "acme") {
		t.Fatal("one agent's inbound path exempted a request while another agent is active")
	}
	if GatewayAllowsAnonymousGET("") || GatewayAllowsAnonymousGET("/api/v1") || GatewayAllowsAnonymousGET("/ws/chat") {
		t.Fatal("anonymous GET helpers returned unexpected true")
	}
	if !GatewayAllowsAnonymousGET("/docs") {
		t.Fatal("docs path should allow anonymous GET")
	}
	if root.Gateway.Auth.Mode != "token" || root.Gateway.Auth.Token != "gw-token" {
		t.Fatalf("gateway auth helpers returned unexpected config: %+v", root.Gateway.Auth)
	}
}
