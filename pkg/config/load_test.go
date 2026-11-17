package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v2"
)

// TestExampleConfigLoads is a smoke test for the repository's example
// forebrain.yaml: if it stops loading cleanly, every fresh install inherits
// the break.
func TestExampleConfigLoads(t *testing.T) {
	// Isolate from the real home so only the file under test contributes.
	t.Setenv("FOREBRAIN_HOME", t.TempDir())
	if _, err := loadDocumentForTest("../../forebrain.yaml"); err != nil {
		t.Fatalf("example forebrain.yaml failed to load: %v", err)
	}
}

// TestParseRootYAMLCronRetentionRange pins the one validation behind every
// write path: the YAML editor's verdict is the loader's verdict, so a
// retention outside 1..3650 is rejected before anything reaches disk.
func TestParseRootYAMLCronRetentionRange(t *testing.T) {
	for _, bad := range []string{"0", "3651", "-3"} {
		_, err := ParseRootYAML([]byte("cron:\n  retention_days: " + bad + "\n"))
		if err == nil || !strings.Contains(err.Error(), "cron.retention_days") {
			t.Fatalf("retention_days %s = %v, want a cron.retention_days error", bad, err)
		}
	}
	for _, ok := range []string{"1", "3650"} {
		r, err := ParseRootYAML([]byte("cron:\n  retention_days: " + ok + "\n"))
		if err != nil {
			t.Fatalf("retention_days %s rejected: %v", ok, err)
		}
		if r.Cron.RetentionDays == nil {
			t.Fatalf("retention_days %s parsed to nil", ok)
		}
	}
}

// TestForebrainYamlExampleStaysValid keeps the repo-root example loadable: it
// documents current settings, so it must pass the same validation as a real
// config, including the lsp and cron sections it demonstrates.
func TestForebrainYamlExampleStaysValid(t *testing.T) {
	data, err := os.ReadFile("../../forebrain.yaml")
	if err != nil {
		t.Skip("example not found")
	}
	r, err := ParseRootYAML(data)
	if err != nil {
		t.Fatalf("forebrain.yaml example rejected: %v", err)
	}
	if got := r.CronRetentionDays(); got != 30 {
		t.Fatalf("cron.retention_days example = %d, want 30", got)
	}
	if eff := r.EffectiveLSP(); eff.MaxServers != 6 || eff.IdleTimeout != 600*time.Second || eff.RequestTimeout != 30*time.Second {
		t.Fatalf("lsp example resolved unexpectedly: %+v", eff)
	}
	if len(r.LSP.Servers) != 2 || r.LSP.Servers["gopls"].Command != "gopls" || r.LSP.Servers["pyright-diagnostics"].Role != "diagnostics" {
		t.Fatalf("lsp.servers example wrong: %+v", r.LSP.Servers)
	}
	if r.Credentials.ChatGPT != "" {
		t.Fatalf("credentials example wrong: %+v", r.Credentials)
	}
	f := r.EffectiveFeatures()
	if !f.Memories && r.Features.Memories == nil || !f.LSP {
		t.Fatalf("features example resolved unexpectedly: %+v", f)
	}
	if srv := r.Agents.Defaults.MCPServers; len(srv) > 0 {
		if string(srv[0].DefaultToolsApprovalMode) != "auto" || srv[0].StartupTimeout != 30 || srv[0].Required {
			t.Fatalf("mcp demo_stdio example wrong: mode=%q startup=%v required=%v", srv[0].DefaultToolsApprovalMode, srv[0].StartupTimeout, srv[0].Required)
		}
	}
	if r.Agents.Defaults.EnableSubagent != nil && *r.Agents.Defaults.EnableSubagent {
		t.Fatal("agents.defaults.enable_subagent example should stay off")
	}
	if r.Compact.AutoCompactPercent != DefaultAutoCompactPercent {
		t.Fatalf("compact example wrong: %+v", r.Compact)
	}
}

func TestLoadRejectsInvalidTokenEstimate(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown hub",
			body: "agents:\n  defaults:\n    token_estimate:\n      hubs: [\"gitlab\"]\n",
			want: "unknown hub",
		},
		{
			name: "tokenizer and path",
			body: "agents:\n  defaults:\n    token_estimate:\n      tokenizers:\n        - model: \"x\"\n          tokenizer: \"qwen-3.8\"\n          path: \"/tmp/tokenizer.json\"\n",
			want: "exactly one of",
		},
		{
			name: "neither tokenizer nor path",
			body: "agents:\n  defaults:\n    token_estimate:\n      tokenizers:\n        - model: \"x\"\n",
			want: "exactly one of",
		},
		{
			name: "missing model",
			body: "agents:\n  defaults:\n    token_estimate:\n      tokenizers:\n        - tokenizer: \"qwen-3.8\"\n",
			want: "model is required",
		},
		{
			name: "empty hub",
			body: "agents:\n  defaults:\n    token_estimate:\n      hubs: [\"\"]\n",
			want: "unknown hub",
		},
		{
			name: "relative path",
			body: "agents:\n  defaults:\n    token_estimate:\n      tokenizers:\n        - model: \"x\"\n          path: \"relative/tokenizer.json\"\n",
			want: "path must be absolute",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "forebrain.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := loadDocumentForTest(path)
			if err == nil {
				t.Fatalf("Load accepted an invalid token_estimate")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestLoadAcceptsValidTokenEstimate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forebrain.yaml")
	// An absolute path on every OS; single-quoted so a Windows path's
	// backslashes stay literal.
	tokenizerPath := filepath.Join(dir, "tokenizer.json")
	body := "agents:\n  defaults:\n    token_estimate:\n      hubs: [\"modelscope\"]\n      default_tokenizer: \"kimi-k3\"\n      tokenizers:\n        - model: \"my-\"\n          tokenizer: \"qwen-3.8\"\n        - model: \"other-\"\n          path: '" + tokenizerPath + "'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := loadDocumentForTest(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := r.Agents.Defaults.TokenEstimate.DefaultTokenizer; got != "kimi-k3" {
		t.Fatalf("default_tokenizer = %q, want kimi-k3", got)
	}
	if got := len(r.Agents.Defaults.TokenEstimate.Tokenizers); got != 2 {
		t.Fatalf("tokenizers = %d entries, want 2", got)
	}
	if got := r.Agents.Defaults.TokenEstimate.Tokenizers[1].Path; got != tokenizerPath {
		t.Fatalf("path = %q, want %q", got, tokenizerPath)
	}
}

// download: false is the one way to turn downloading off, and it survives a
// Save; hubs: [] means the default hubs with downloading on, before and after a
// Save (which drops the empty list).
func TestTokenEstimateDownloadAndEmptyHubsSurviveSave(t *testing.T) {
	// Load then Save back to the same file, the way the config editor does.
	load := func(t *testing.T, body string) (TokenEstimateConfig, string) {
		t.Helper()
		dir := t.TempDir()
		// Isolate the data-dir home so Load reads nothing of the developer's
		// own home (see TestSavePreservesSecretEnvRefs).
		t.Setenv("FOREBRAIN_HOME", dir)
		path := filepath.Join(dir, "forebrain.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := loadDocumentForTest(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if err := Save(path, r); err != nil {
			t.Fatalf("Save: %v", err)
		}
		return r.Agents.Defaults.TokenEstimate, path
	}
	reload := func(t *testing.T, path string) TokenEstimateConfig {
		t.Helper()
		r, err := loadDocumentForTest(path)
		if err != nil {
			t.Fatalf("Load after Save: %v", err)
		}
		return r.Agents.Defaults.TokenEstimate
	}

	t.Run("download false", func(t *testing.T) {
		first, saved := load(t, "agents:\n  defaults:\n    token_estimate:\n      download: false\n")
		if raw, err := os.ReadFile(saved); err != nil || !strings.Contains(string(raw), "download: false") {
			t.Fatalf("saved file lost download: false (err=%v):\n%s", err, raw)
		}
		for name, cfg := range map[string]TokenEstimateConfig{"loaded": first, "saved and reloaded": reload(t, saved)} {
			if cfg.Download == nil || *cfg.Download {
				t.Fatalf("%s: download = %v, want a non-nil false", name, cfg.Download)
			}
		}
	})
	t.Run("empty hubs", func(t *testing.T) {
		first, saved := load(t, "agents:\n  defaults:\n    token_estimate:\n      hubs: []\n")
		for name, cfg := range map[string]TokenEstimateConfig{"loaded": first, "saved and reloaded": reload(t, saved)} {
			if cfg.Download != nil && !*cfg.Download {
				t.Fatalf("%s: download turned off by hubs: []", name)
			}
			if len(cfg.Hubs) != 0 {
				t.Fatalf("%s: hubs = %v, want empty (the default order)", name, cfg.Hubs)
			}
		}
	})
}

// TestSaveLeavesNoTempFileAndKeepsMode pins Save's on-disk behaviour now that
// it writes via writeFileAtomic: saving twice leaves exactly the target file
// in the directory (no temp-file residue) with 0600 permissions, for both the
// YAML and the JSON path.
func TestSaveLeavesNoTempFileAndKeepsMode(t *testing.T) {
	for _, name := range []string{"forebrain.yaml", "forebrain.json"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("FOREBRAIN_HOME", dir)
			path := filepath.Join(dir, name)
			if err := Save(path, Root{}); err != nil {
				t.Fatalf("first Save: %v", err)
			}
			if err := Save(path, Root{}); err != nil {
				t.Fatalf("second Save: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("ReadDir: %v", err)
			}
			if len(entries) != 1 || entries[0].Name() != name {
				t.Fatalf("directory must hold only %s, got %v", name, entries)
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatalf("Stat: %v", err)
				}
				if info.Mode().Perm() != 0o600 {
					t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
				}
			}
		})
	}
}

// TestLegacyDefaultModelLabelLoadsAsUnset pins the no-migration rule (owner
// 2026-11-16, plan 315): a configuration written before the model table existed
// still loads — its retired provider list is ignored, not refused — and a
// "provider/model" label nothing resolves to means "unset", so the agent
// follows the global default instead of failing the start.
func TestLegacyDefaultModelLabelLoadsAsUnset(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", dir)
	path := filepath.Join(dir, "forebrain.yaml")
	seed := `agents:
  definitions:
    main:
      default_model: openai/gone
      llm_providers:
      - provider: openai
        model: gpt-4
        api_key: ${OPENAI_API_KEY}
        base_url: https://api.openai.com/v1
`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	cfg, err := loadPersistedDocumentForTest(path)
	if err != nil {
		t.Fatalf("a configuration carrying a retired provider list must still load: %v", err)
	}
	if got := cfg.Agents.Definitions["main"].DefaultModel; got != "" {
		t.Fatalf("default_model = %q, want the unresolvable legacy label cleared", got)
	}
	if _, ok := cfg.Agents.Definitions["main"]; !ok {
		t.Fatalf("the main definition was dropped: %+v", cfg.Agents.Definitions)
	}

	// A row id nothing carries is kept: the runtime skips a dangling id, and
	// only a legacy label means "unset".
	rowIDPath := filepath.Join(dir, "row-id.yaml")
	rowIDSeed := "agents:\n  definitions:\n    main:\n      default_model: llm-gone\n"
	if err := os.WriteFile(rowIDPath, []byte(rowIDSeed), 0o600); err != nil {
		t.Fatalf("write row-id seed: %v", err)
	}
	rowIDCfg, err := loadPersistedDocumentForTest(rowIDPath)
	if err != nil {
		t.Fatalf("a config naming a row id must load: %v", err)
	}
	if got := rowIDCfg.Agents.Definitions["main"].DefaultModel; got != "llm-gone" {
		t.Fatalf("default_model = %q, want the stored row id kept", got)
	}

	// A label the table still carries keeps naming that row: the install runs
	// on it rather than falling back.
	inMemory := Root{
		LLMModels: []LLMModelConfig{{ID: "llm-4", Provider: "openai", Model: "gpt-4o", BaseURL: "https://api.openai.com/v1"}},
		Agents: AgentsSection{Definitions: map[string]AgentDefinition{
			"main": {DefaultModel: "openai/gpt-4o"},
		}},
	}
	if err := ValidateAgentRoot(&inMemory); err != nil {
		t.Fatalf("a resolvable legacy label was refused: %v", err)
	}
	if got := EffectiveDefaultLLMFor(&inMemory, "main"); got == nil || got.ID != "llm-4" {
		t.Fatalf("effective default = %+v, want the row the label names (llm-4)", got)
	}
}

// TestLoadIgnoresUnsupportedFileKeys pins plan 207 §3.6 on the read path: the
// file carries the gateway's startup sections and the credentials pointer, and
// every other key in it — including one whose value the section's own type
// cannot hold, which used to fail the whole load — is ignored without an error.
func TestLoadIgnoresUnsupportedFileKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", dir)
	path := filepath.Join(dir, "forebrain.yaml")
	body := `gateway:
  http_addr: 127.0.0.1:6099
  auth:
    token: ${FOREBRAIN_FILE_SCHEMA_TEST_TOKEN}
credentials:
  chatgpt: auth.json
memories: 5
approval_policy: never
sandbox_mode: read-only
agents:
  defaults:
    netwrok_retry_max: 3
    web_search:
      provider: tavily
`
	t.Setenv("FOREBRAIN_FILE_SCHEMA_TEST_TOKEN", "tok-kept")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("a file carrying unsupported keys must still load: %v", err)
	}
	if loaded.Gateway.HTTPAddr != "127.0.0.1:6099" {
		t.Fatalf("gateway.http_addr = %q, want the file's value", loaded.Gateway.HTTPAddr)
	}
	if loaded.Gateway.Auth.Token != "tok-kept" {
		t.Fatalf("gateway.auth.token = %q, want the file's expanded reference", loaded.Gateway.Auth.Token)
	}
	if loaded.Credentials.ChatGPT != "auth.json" {
		t.Fatalf("credentials.chatgpt = %q, want the file's value", loaded.Credentials.ChatGPT)
	}
	// Nothing else came from the file: the stored sections read as unset, and
	// what the loader's own normalization puts there — the main agent's slot —
	// is all the agents section holds.
	if loaded.Memories != (MemorySection{}) {
		t.Fatalf("memories came from the file: %+v", loaded.Memories)
	}
	if loaded.ApprovalPreset != "" {
		t.Fatalf("approval_preset came from the file: %q", loaded.ApprovalPreset)
	}
	if loaded.SandboxWorkspaceWrite.WritableRoots != nil {
		t.Fatalf("sandbox_workspace_write came from the file: %+v", loaded.SandboxWorkspaceWrite)
	}
	if _, ok := loaded.Agents.Definitions["main"]; !ok || len(loaded.Agents.Definitions) != 1 {
		t.Fatalf("agents.definitions = %v, want only the loader's main slot", loaded.Agents.Definitions)
	}
	mainDef := loaded.Agents.Definitions["main"]
	if mainDef.DefaultModel != "" || len(mainDef.Subagents) != 0 || mainDef.Primary {
		t.Fatalf("agents came from the file: %+v", mainDef)
	}
	if loaded.Agents.Defaults.NetworkRetryMax != nil || loaded.Agents.Defaults.WebSearch.Provider != "" {
		t.Fatalf("agents.defaults came from the file: %+v", loaded.Agents.Defaults)
	}
}

// loadDocumentForTest reads a whole configuration document the way Load did
// before plan 207 narrowed the file to its supported keys: every stored section
// is decoded, then Load's tail runs. A test that pins how a stored section's
// YAML reads — a field's spelling, its unit, a rejected value — goes through
// here, because forebrain.yaml itself no longer carries those sections.
func loadDocumentForTest(path string) (Root, error) {
	r, err := loadPersistedDocumentForTest(path)
	if err != nil {
		return Root{}, err
	}
	if err := FinishLoad(&r); err != nil {
		return Root{}, err
	}
	return r, nil
}

// loadPersistedDocumentForTest is the same read without Load's tail: no .env
// merge, no ${ENV} expansion, no re-validation. It is what LoadPersisted did
// before plan 207 narrowed the file.
func loadPersistedDocumentForTest(path string) (Root, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Root{}, err
	}
	var r Root
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(body, &r); err != nil {
			return Root{}, err
		}
	case ".json":
		if err := json.Unmarshal(body, &r); err != nil {
			return Root{}, err
		}
	default:
		if err := json.Unmarshal(body, &r); err != nil {
			if err2 := yaml.Unmarshal(body, &r); err2 != nil {
				return Root{}, err
			}
		}
	}
	normalize(&r)
	if err := validateLoadedRoot(&r); err != nil {
		return Root{}, err
	}
	return r, nil
}

// A token the gateway process generated for itself stands in only where the
// configuration's own token is empty — after the references are expanded, so a
// reference nothing resolves counts as empty — and never over a configured one.
func TestGeneratedGatewayTokenFillsOnlyAnEmptyToken(t *testing.T) {
	t.Cleanup(func() { SetGeneratedGatewayToken("") })
	finish := func(token string) string {
		t.Helper()
		var r Root
		r.Gateway.Auth.Token = token
		lookup := func(name string) (string, bool) {
			if name == "SET_TOKEN" {
				return "from-lookup", true
			}
			return "", false
		}
		if err := FinishLoadWith(&r, lookup); err != nil {
			t.Fatalf("FinishLoadWith: %v", err)
		}
		return r.Gateway.Auth.Token
	}

	// Nothing generated: an empty token stays empty.
	if got := finish(""); got != "" {
		t.Fatalf("token = %q, want empty with nothing generated", got)
	}

	SetGeneratedGatewayToken("generated")
	for in, want := range map[string]string{
		"":               "generated",
		"${UNSET_TOKEN}": "generated",
		"hand-written":   "hand-written",
		"${SET_TOKEN}":   "from-lookup",
	} {
		if got := finish(in); got != want {
			t.Fatalf("token %q loaded as %q, want %q", in, got, want)
		}
	}

	SetGeneratedGatewayToken("")
	if got := finish(""); got != "" {
		t.Fatalf("token = %q, want empty after the generated one is removed", got)
	}
}
