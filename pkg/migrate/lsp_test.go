package migrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

// enableClaudePluginsInHome writes a settings.json enabling the given plugin
// keys ("<name>@<marketplace>").
func enableClaudePluginsInHome(t *testing.T, home string, keys ...string) {
	t.Helper()
	enabled := map[string]bool{}
	for _, key := range keys {
		enabled[key] = true
	}
	body, err := json.Marshal(map[string]interface{}{"enabledPlugins": enabled})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "settings.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// lspPluginVersionDir creates and returns the plugin's version directory
// under the home's plugin cache.
func lspPluginVersionDir(t *testing.T, home, key, version string) string {
	t.Helper()
	parts := strings.SplitN(key, "@", 2)
	dir := filepath.Join(home, "plugins", "cache", parts[1], parts[0], version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writePluginFile writes body at relPath under the plugin's version dir.
func writePluginFile(t *testing.T, dir, relPath, body string) {
	t.Helper()
	path := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// lspOptions is a minimal option set for the config-writing category: a
// forebrain.yaml and a fixed clock, nothing else.
func lspOptions(t *testing.T, configBody string) *Options {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "forebrain.yaml")
	if err := os.WriteFile(cfgPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Options{ConfigPath: cfgPath, Now: fixedClock}
}

const baseLSPConfig = `agents:
  defaults:
    mcp_servers:
      - name: existing
        transport: stdio
        command: /bin/true
`

// TestDiscoverLSPPlugins covers the discovery contract: only enabled plugins
// that declare servers come back, the highest version directory wins, all
// three declaration forms are read, .lsp.json wins a name clash, and a path
// that escapes the plugin directory is reported instead of followed.
func TestDiscoverLSPPlugins(t *testing.T) {
	home := t.TempDir()
	enableClaudePluginsInHome(t, home,
		"mylang-lsp@acme", "inline-lsp@acme", "path-lsp@acme", "escaping-lsp@acme",
		"skill-plugin@acme", "no-cache@acme")

	// Two cached versions; only the highest may be read. The same server
	// name is also declared inline in plugin.json with a different command,
	// so .lsp.json precedence is pinned too.
	old := lspPluginVersionDir(t, home, "mylang-lsp@acme", "1.0.0")
	writePluginFile(t, old, ".lsp.json", `{"MyLang Server": {"command": "old-mylang", "extensionToLanguage": {".old": "old"}}}`)
	dir := lspPluginVersionDir(t, home, "mylang-lsp@acme", "2.0.0")
	writePluginFile(t, dir, ".lsp.json", `{"MyLang Server": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/mylang-ls", "extensionToLanguage": {".ml2": "mylang"}}}`)
	writePluginFile(t, dir, filepath.Join(".claude-plugin", "plugin.json"),
		`{"name": "mylang-lsp", "lspServers": {"MyLang Server": {"command": "inline-override", "extensionToLanguage": {".inline": "x"}}}}`)

	inline := lspPluginVersionDir(t, home, "inline-lsp@acme", "1.0.0")
	writePluginFile(t, inline, filepath.Join(".claude-plugin", "plugin.json"),
		`{"name": "inline-lsp", "lspServers": {"inline-server": {"command": "inline-ls", "extensionToLanguage": {".inl": "inline"}}}}`)

	path := lspPluginVersionDir(t, home, "path-lsp@acme", "1.0.0")
	writePluginFile(t, path, filepath.Join(".claude-plugin", "plugin.json"),
		`{"name": "path-lsp", "lspServers": "./servers/lsp.json"}`)
	writePluginFile(t, path, filepath.Join("servers", "lsp.json"),
		`{"file-server": {"command": "file-ls", "extensionToLanguage": {".fil": "file"}}}`)

	escaping := lspPluginVersionDir(t, home, "escaping-lsp@acme", "1.0.0")
	writePluginFile(t, escaping, filepath.Join(".claude-plugin", "plugin.json"),
		`{"name": "escaping-lsp", "lspServers": "../../outside.json"}`)
	writePluginFile(t, escaping, filepath.Join("..", "..", "outside.json"),
		`{"outside-server": {"command": "outside-ls", "extensionToLanguage": {".out": "out"}}}`)

	// A skills-only plugin declares nothing and must not come back.
	skills := lspPluginVersionDir(t, home, "skill-plugin@acme", "1.0.0")
	writePluginFile(t, skills, filepath.Join("skills", "a-skill", "SKILL.md"), "---\nname: a-skill\n---\nbody\n")

	// Cached but never enabled: excluded.
	notEnabled := lspPluginVersionDir(t, home, "not-enabled-lsp@acme", "1.0.0")
	writePluginFile(t, notEnabled, ".lsp.json", `{"never": {"command": "x", "extensionToLanguage": {".x": "x"}}}`)

	plugins := discoverLSPPlugins(home)
	keys := make([]string, 0, len(plugins))
	for _, plugin := range plugins {
		keys = append(keys, plugin.Key)
	}
	want := []string{"escaping-lsp@acme", "inline-lsp@acme", "mylang-lsp@acme", "path-lsp@acme"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	byKey := map[string]claudeLSPPlugin{}
	for _, plugin := range plugins {
		byKey[plugin.Key] = plugin
	}
	if got := byKey["mylang-lsp@acme"]; got.Root != dir || got.Servers["MyLang Server"].Command != "${CLAUDE_PLUGIN_ROOT}/bin/mylang-ls" {
		t.Fatalf("mylang plugin = %+v", got)
	}
	if got := byKey["inline-lsp@acme"]; got.Servers["inline-server"].Command != "inline-ls" {
		t.Fatalf("inline plugin = %+v", got)
	}
	if got := byKey["path-lsp@acme"]; got.Servers["file-server"].Command != "file-ls" {
		t.Fatalf("path plugin = %+v", got)
	}
	if got := byKey["escaping-lsp@acme"]; got.Err == "" || !strings.Contains(got.Err, "escapes") || len(got.Servers) != 0 {
		t.Fatalf("escaping plugin = %+v", got)
	}
}

// TestImportOfficialPluginEnablesCatalogServer pins the official-plugin path:
// only the enablement is written, never the plugin's own definition.
func TestImportOfficialPluginEnablesCatalogServer(t *testing.T) {
	dir := t.TempDir()
	data := &claudeData{LSPPlugins: []claudeLSPPlugin{{
		Key:  "gopls-lsp@claude-plugins-official",
		Name: "gopls-lsp",
		Root: dir,
		Servers: map[string]claudeLSPServer{
			"go": {Command: "gopls", Args: []string{"serve"}, ExtensionToLanguage: map[string]string{".go": "go"}},
		},
	}}}
	opts := lspOptions(t, baseLSPConfig)
	outcomes, notices, err := importLSP(data, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Name != "gopls" || outcomes[0].Status != "enabled" ||
		outcomes[0].Detail != "built-in gopls server enabled (from gopls-lsp@claude-plugins-official)" {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	foundNotice := false
	for _, notice := range notices {
		if notice == "enabled language servers apply to edits from the next session; run forebrain lsp doctor to check them" {
			foundNotice = true
		}
	}
	if !foundNotice {
		t.Fatalf("notices = %v", notices)
	}
	cfg, err := appcfg.Load(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := cfg.LSP.Servers["gopls"]
	if !ok || entry.Enabled == nil || !*entry.Enabled {
		t.Fatalf("gopls entry = %+v", entry)
	}
	if entry.Command != "" || entry.Args != nil || entry.ExtensionToLanguage != nil {
		t.Fatalf("official plugin copied its definition: %+v", entry)
	}
}

// TestImportCustomPlugin pins the §5.5 field mapping of a non-official
// plugin's declaration onto a custom lsp.servers entry.
func TestImportCustomPlugin(t *testing.T) {
	dir := t.TempDir()
	off := false
	restarts := 5
	diagnostics := false
	data := &claudeData{LSPPlugins: []claudeLSPPlugin{{
		Key:  "mylang@acme",
		Name: "mylang",
		Root: dir,
		Servers: map[string]claudeLSPServer{
			"MyLang Server": {
				Command:               "${CLAUDE_PLUGIN_ROOT}/bin/mylang-ls",
				Args:                  []string{"--stdio", "${CLAUDE_PLUGIN_ROOT}/share/x"},
				Env:                   map[string]string{"MYLANG_ROOT": "${CLAUDE_PLUGIN_ROOT}"},
				ExtensionToLanguage:   map[string]string{".ML2": "mylang"},
				InitializationOptions: json.RawMessage(`{"hover": true}`),
				Settings:              json.RawMessage(`{"depth": 2}`),
				WorkspaceFolder:       "${CLAUDE_PLUGIN_ROOT}/ws",
				StartupTimeout:        1500,
				ShutdownTimeout:       500,
				RestartOnCrash:        &off,
				MaxRestarts:           &restarts,
				Diagnostics:           &diagnostics,
			},
		},
	}}}
	opts := lspOptions(t, baseLSPConfig)
	outcomes, _, err := importLSP(data, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Name != "mylang-server" || outcomes[0].Status != "written" {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	wantDetail := filepath.Join(dir, "bin/mylang-ls") + " for .ml2 (from mylang@acme)"
	if outcomes[0].Detail != wantDetail {
		t.Fatalf("detail = %q, want %q", outcomes[0].Detail, wantDetail)
	}
	cfg, err := appcfg.Load(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	entry := cfg.LSP.Servers["mylang-server"]
	if entry.Command != filepath.Join(dir, "bin/mylang-ls") {
		t.Fatalf("command = %q", entry.Command)
	}
	if !reflect.DeepEqual(entry.Args, []string{"--stdio", filepath.Join(dir, "share/x")}) {
		t.Fatalf("args = %v", entry.Args)
	}
	if entry.Env["MYLANG_ROOT"] != dir || entry.WorkspaceFolder != filepath.Join(dir, "ws") {
		t.Fatalf("env/workspace = %+v", entry)
	}
	if entry.ExtensionToLanguage[".ml2"] != "mylang" || len(entry.ExtensionToLanguage) != 1 {
		t.Fatalf("extension_to_language = %v", entry.ExtensionToLanguage)
	}
	if entry.StartupTimeout != 2 || entry.ShutdownTimeout != 1 {
		t.Fatalf("timeouts = %d/%d", entry.StartupTimeout, entry.ShutdownTimeout)
	}
	if entry.Enabled == nil || !*entry.Enabled {
		t.Fatalf("enabled = %v", entry.Enabled)
	}
	if entry.RestartOnCrash == nil || *entry.RestartOnCrash || entry.MaxRestarts != 5 {
		t.Fatalf("restart fields = %+v/%d", entry.RestartOnCrash, entry.MaxRestarts)
	}
	if entry.Diagnostics == nil || *entry.Diagnostics {
		t.Fatalf("diagnostics = %v", entry.Diagnostics)
	}
	// The YAML round-trip keeps the JSON objects verbatim in meaning; only
	// their spacing is normalized by the re-encode.
	if string(entry.InitializationOptions) != `{"hover":true}` || string(entry.Settings) != `{"depth":2}` {
		t.Fatalf("json objects = %s / %s", entry.InitializationOptions, entry.Settings)
	}
}

// TestImportSkips pins every skip reason verbatim and that a run where
// everything skips leaves forebrain.yaml untouched.
func TestImportSkips(t *testing.T) {
	configBody := baseLSPConfig + `lsp:
  servers:
    mylang:
      command: /bin/true
      extension_to_language:
        ".ml2": mylang
`
	data := &claudeData{LSPPlugins: []claudeLSPPlugin{{
		Key:  "mix@acme",
		Name: "mix",
		Root: t.TempDir(),
		Servers: map[string]claudeLSPServer{
			"mylang":       {Command: "mylang-ls", ExtensionToLanguage: map[string]string{".ml2": "mylang"}},
			"socket-srv":   {Command: "socket-ls", Transport: "socket", ExtensionToLanguage: map[string]string{".s": "s"}},
			"projdir-srv":  {Command: "proj-ls", Args: []string{"${CLAUDE_PROJECT_DIR}/x"}, ExtensionToLanguage: map[string]string{".p": "p"}},
			"userconf-srv": {Command: "uc-ls", Env: map[string]string{"TOKEN": "${user_config.token}"}, ExtensionToLanguage: map[string]string{".u": "u"}},
			"empty-srv":    {Command: "empty-ls"},
			"secret-srv":   {Command: "secret-ls", Env: map[string]string{"API_TOKEN": "abc123"}, ExtensionToLanguage: map[string]string{".sec": "sec"}},
		},
	}}}
	opts := lspOptions(t, configBody)
	before, err := os.ReadFile(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	outcomes, _, err := importLSP(data, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]LSPOutcome{}
	for _, outcome := range outcomes {
		if outcome.Status != "skipped" {
			t.Fatalf("%s = %+v, want skipped", outcome.Name, outcome)
		}
		byName[outcome.Name] = outcome
	}
	details := map[string]string{
		"mylang":       "an entry with this id is already configured; not overwritten",
		"socket-srv":   "only stdio language servers are supported",
		"projdir-srv":  "uses ${CLAUDE_PROJECT_DIR}, which forebrain does not provide",
		"userconf-srv": "uses plugin user settings, which forebrain does not provide",
		"empty-srv":    "the declaration has no command or extensionToLanguage",
		"secret-srv":   "its env holds a plaintext secret; add it by hand with ${ENV_NAME} from ~/.forebrain/.env",
	}
	for name, detail := range details {
		if byName[name].Detail != detail {
			t.Fatalf("%s detail = %q, want %q", name, byName[name].Detail, detail)
		}
	}
	if len(outcomes) != len(details) {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	after, err := os.ReadFile(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("config rewritten despite all-skipped run:\n%s", after)
	}
	entries, _ := os.ReadDir(filepath.Dir(opts.ConfigPath))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "forebrain.yaml.bak-") {
			t.Fatalf("backup written despite all-skipped run: %s", entry.Name())
		}
	}
}

// TestImportLSPDryRunWritesNothing checks the preview leaves forebrain.yaml
// byte-identical and reports the same outcomes a real run produces.
func TestImportLSPDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	data := &claudeData{LSPPlugins: []claudeLSPPlugin{
		{
			Key: "gopls-lsp@claude-plugins-official", Name: "gopls-lsp", Root: dir,
			Servers: map[string]claudeLSPServer{"go": {Command: "gopls", ExtensionToLanguage: map[string]string{".go": "go"}}},
		},
		{
			Key: "mylang@acme", Name: "mylang", Root: dir,
			Servers: map[string]claudeLSPServer{"mylang": {Command: "mylang-ls", ExtensionToLanguage: map[string]string{".ml2": "mylang"}}},
		},
	}}
	dryOpts := lspOptions(t, baseLSPConfig)
	dryOpts.DryRun = true
	before, err := os.ReadFile(dryOpts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	dryOutcomes, dryNotices, err := importLSP(data, dryOpts, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(dryOpts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("dry run rewrote the config:\n%s", after)
	}
	if len(dryOutcomes) != 2 || dryOutcomes[0].Status != "enabled" || dryOutcomes[1].Status != "written" {
		t.Fatalf("dry outcomes = %+v", dryOutcomes)
	}
	if len(dryNotices) != 1 {
		t.Fatalf("dry notices = %v", dryNotices)
	}
	realOpts := lspOptions(t, baseLSPConfig)
	realOutcomes, _, err := importLSP(data, realOpts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dryOutcomes, realOutcomes) {
		t.Fatalf("dry outcomes %+v != real outcomes %+v", dryOutcomes, realOutcomes)
	}
}

// TestImportLSPBacksUpConfig checks the real run copies forebrain.yaml aside
// before rewriting it, the same contract the MCP append patch holds.
func TestImportLSPBacksUpConfig(t *testing.T) {
	dir := t.TempDir()
	data := &claudeData{LSPPlugins: []claudeLSPPlugin{{
		Key: "gopls-lsp@claude-plugins-official", Name: "gopls-lsp", Root: dir,
		Servers: map[string]claudeLSPServer{"go": {Command: "gopls", ExtensionToLanguage: map[string]string{".go": "go"}}},
	}}}
	opts := lspOptions(t, baseLSPConfig)
	if _, _, err := importLSP(data, opts, nil); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(opts.ConfigPath))
	backups := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "forebrain.yaml.bak-") {
			backups++
			body, err := os.ReadFile(filepath.Join(filepath.Dir(opts.ConfigPath), entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != baseLSPConfig {
				t.Fatalf("backup does not hold the original config:\n%s", body)
			}
		}
	}
	if backups != 1 {
		t.Fatalf("backups = %d", backups)
	}
}

// TestImportLSPRespectsOnly checks the category gate: --only without "lsp"
// leaves the config alone and the report names the category as left out.
func TestImportLSPRespectsOnly(t *testing.T) {
	fixture := buildFixtureHome(t)
	dir := lspPluginVersionDir(t, fixture.root, "gopls-lsp@claude-plugins-official", "1.0.0")
	writePluginFile(t, dir, ".lsp.json", `{"go": {"command": "gopls", "extensionToLanguage": {".go": "go"}}}`)
	enableClaudePluginsInHome(t, fixture.root, "gopls-lsp@claude-plugins-official")
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, false)
	opts.Only = []string{"mcp"}
	report, err := RunClaude(context.Background(), opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.LSP) != 0 {
		t.Fatalf("lsp outcomes = %+v", report.LSP)
	}
	found := false
	for _, category := range report.SkippedCategories {
		if category == "lsp" {
			found = true
		}
	}
	if !found {
		t.Fatalf("skipped categories = %v", report.SkippedCategories)
	}
	after, err := os.ReadFile(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "gopls") {
		t.Fatalf("gopls written despite --only mcp:\n%s", after)
	}
}
