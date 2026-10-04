package lsp

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

func intPtr(v int) *int { return &v }

func findServer(t *testing.T, servers []ServerConfig, id string) ServerConfig {
	t.Helper()
	for _, sc := range servers {
		if sc.ID == id {
			return sc
		}
	}
	t.Fatalf("server %s not resolved", id)
	return ServerConfig{}
}

func TestResolveServersDefaults(t *testing.T) {
	servers := ResolveServers(ResolveInput{Config: &appcfg.Root{}, GOOS: "linux"})
	if len(servers) < len(requiredCatalogIDs) {
		t.Fatalf("resolved %d servers; the catalog alone holds %d", len(servers), len(requiredCatalogIDs))
	}
	for _, sc := range servers {
		if sc.Enabled {
			t.Errorf("%s: catalog servers default to disabled", sc.ID)
		}
		if sc.Scope != "catalog" {
			t.Errorf("%s: scope is %q, want catalog", sc.ID, sc.Scope)
		}
		if len(sc.Fingerprint) != 12 {
			t.Errorf("%s: fingerprint %q is not 12 characters", sc.ID, sc.Fingerprint)
		}
		for _, c := range sc.Fingerprint {
			if !('0' <= c && c <= '9') && !('a' <= c && c <= 'f') {
				t.Errorf("%s: fingerprint %q is not hex", sc.ID, sc.Fingerprint)
				break
			}
		}
		if sc.StartupTimeout == 0 {
			t.Errorf("%s: startup timeout defaults to 60s, not zero", sc.ID)
		}
		if sc.ShutdownTimeout != 5*time.Second {
			t.Errorf("%s: shutdown timeout is %v", sc.ID, sc.ShutdownTimeout)
		}
		if !sc.RestartOnCrash || sc.MaxRestarts != 3 || !sc.Diagnostics {
			t.Errorf("%s: lifecycle defaults wrong (restart=%v max=%d diagnostics=%v)", sc.ID, sc.RestartOnCrash, sc.MaxRestarts, sc.Diagnostics)
		}
	}
}

func TestResolveServersOverlay(t *testing.T) {
	settings := appcfg.LSPJSONObject(`{"gopls":{"staticcheck":true}}`)
	cfg := &appcfg.Root{LSP: appcfg.LSPSection{Servers: map[string]appcfg.LSPServerConfig{
		"gopls": {Enabled: boolPtr(true), Settings: settings},
	}}}
	servers := ResolveServers(ResolveInput{Config: cfg, GOOS: "linux"})
	gopls := findServer(t, servers, "gopls")
	if gopls.Scope != "global" {
		t.Errorf("scope is %q, want global", gopls.Scope)
	}
	if !gopls.Enabled {
		t.Error("global enabled: true must enable gopls")
	}
	if string(gopls.Settings) != `{"gopls":{"staticcheck":true}}` {
		t.Errorf("settings are %s", gopls.Settings)
	}
	if gopls.Command != "gopls" || len(gopls.Args) != 1 || gopls.Args[0] != "serve" {
		t.Errorf("catalog command must stand: %q %v", gopls.Command, gopls.Args)
	}
	plain := findServer(t, ResolveServers(ResolveInput{Config: &appcfg.Root{}, GOOS: "linux"}), "gopls")
	if gopls.Fingerprint == plain.Fingerprint {
		t.Error("overlaid settings must change the fingerprint")
	}

	// enabled.json has the final say, even against an explicit true.
	withState := ResolveServers(ResolveInput{Config: cfg, Enabled: map[string]bool{"gopls": false}, GOOS: "linux"})
	if findServer(t, withState, "gopls").Enabled {
		t.Error("enabled.json false must disable gopls")
	}
}

func TestResolveServersProjectOverlay(t *testing.T) {
	project := map[string]appcfg.LSPServerConfig{
		"gopls": {
			Args:           []string{"serve", "-rpc.trace"},
			Env:            map[string]string{"GOFLAGS": "-tags=integration"},
			EnvPassthrough: []string{"SECRET_TOKEN"},
			Priority:       intPtr(99),
		},
	}
	plain := findServer(t, ResolveServers(ResolveInput{Config: &appcfg.Root{}, GOOS: "linux"}), "gopls")
	servers := ResolveServers(ResolveInput{Config: &appcfg.Root{}, ProjectServers: project, GOOS: "linux"})
	gopls := findServer(t, servers, "gopls")
	if gopls.Scope != "project" {
		t.Errorf("scope is %q, want project", gopls.Scope)
	}
	if !gopls.EnvFromProject {
		t.Error("project entries set EnvFromProject")
	}
	if len(gopls.Args) != 2 || gopls.Args[1] != "-rpc.trace" {
		t.Errorf("args were not overlaid: %v", gopls.Args)
	}
	if gopls.Env["GOFLAGS"] != "-tags=integration" {
		t.Errorf("env was not overlaid: %v", gopls.Env)
	}
	if len(gopls.EnvPassthrough) != len(plain.EnvPassthrough) {
		t.Errorf("project entries must not extend env_passthrough: %v", gopls.EnvPassthrough)
	}
	if gopls.Priority != plain.Priority {
		t.Errorf("project priority must be ignored: %d", gopls.Priority)
	}
}

func TestResolveServersCustom(t *testing.T) {
	cfg := &appcfg.Root{LSP: appcfg.LSPSection{Servers: map[string]appcfg.LSPServerConfig{
		"broken": {Command: "/opt/bin/broken-ls"},
		"mylang": {Command: "/opt/mylang/bin/mylang-ls", Args: []string{"--stdio"}, ExtensionToLanguage: map[string]string{".ml2": "mylang"}},
	}}}
	servers := ResolveServers(ResolveInput{Config: cfg, GOOS: "linux"})
	broken := findServer(t, servers, "broken")
	if broken.Invalid == "" {
		t.Error("a custom server without extensions must be invalid")
	}
	mylang := findServer(t, servers, "mylang")
	if mylang.Invalid != "" {
		t.Errorf("complete custom server must be valid: %q", mylang.Invalid)
	}
	if !mylang.Enabled {
		t.Error("custom servers default to enabled")
	}
	if mylang.Priority != 50 {
		t.Errorf("custom priority is %d, want 50", mylang.Priority)
	}
}

func TestResolveServersDarwinCommand(t *testing.T) {
	darwin := findServer(t, ResolveServers(ResolveInput{Config: &appcfg.Root{}, GOOS: "darwin"}), "sourcekit-lsp")
	if darwin.Command != "xcrun" || len(darwin.Args) != 1 || darwin.Args[0] != "sourcekit-lsp" {
		t.Errorf("darwin command is %q %v", darwin.Command, darwin.Args)
	}
	linux := findServer(t, ResolveServers(ResolveInput{Config: &appcfg.Root{}, GOOS: "linux"}), "sourcekit-lsp")
	if linux.Command != "sourcekit-lsp" {
		t.Errorf("linux command is %q", linux.Command)
	}
}

func TestServersForFile(t *testing.T) {
	disabled := ResolveServers(ResolveInput{Config: &appcfg.Root{}, GOOS: "linux"})
	if primary, _ := ServersForFile(disabled, "/p/main.go", "/p"); primary != nil {
		t.Fatalf("a disabled catalog must answer no primary server, got %s", primary.ID)
	}
	enabled := ResolveServers(ResolveInput{Config: &appcfg.Root{}, Enabled: map[string]bool{"gopls": true}, GOOS: "linux"})
	primary, _ := ServersForFile(enabled, "/p/main.go", "/p")
	if primary == nil || primary.ID != "gopls" {
		t.Fatalf("primary for main.go is %v", primary)
	}
	if primary, _ := ServersForFile(enabled, "/p/go.mod", "/p"); primary == nil || primary.ID != "gopls" {
		t.Fatalf("filenames must match too, got %v", primary)
	}

	// Priority orders two primaries sharing an extension.
	two := []ServerConfig{
		{ID: "a", Role: "primary", Priority: 10, Enabled: true, ExtensionToLanguage: map[string]string{".x": "x"}},
		{ID: "b", Role: "primary", Priority: 20, Enabled: true, ExtensionToLanguage: map[string]string{".x": "x"}},
	}
	primary, _ = ServersForFile(two, "/p/f.x", "/p")
	if primary.ID != "b" {
		t.Fatalf("higher priority must win, got %s", primary.ID)
	}

	// A conflicts rule flips the choice when its marker sits on the path up
	// to the project root (spec §6.3).
	root := t.TempDir()
	if err := writeMarker(filepath.Join(root, "x.marker")); err != nil {
		t.Fatal(err)
	}
	two[0].Conflicts = []Conflict{{With: "b", WhenRootMarker: []string{"x.marker"}}}
	primary, _ = ServersForFile(two, filepath.Join(root, "f.x"), root)
	if primary.ID != "a" {
		t.Fatalf("the conflicts rule must win, got %s", primary.ID)
	}

	// Diagnostics-role matches land in the second return value.
	mixed := []ServerConfig{
		{ID: "p1", Role: "primary", Priority: 10, Enabled: true, ExtensionToLanguage: map[string]string{".x": "x"}},
		{ID: "d1", Role: "diagnostics", Enabled: true, ExtensionToLanguage: map[string]string{".x": "x"}},
	}
	primary, diagnostics := ServersForFile(mixed, "/p/f.x", "/p")
	if primary.ID != "p1" || len(diagnostics) != 1 || diagnostics[0].ID != "d1" {
		t.Fatalf("primary=%v diagnostics=%v", primary, diagnostics)
	}
}

// TestRequireRootMarker: a require_root_marker diagnostics server only
// serves files below a directory holding one of its markers (task 17).
func TestRequireRootMarker(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.ts": "", "web/a.ts": ""})
	servers := ResolveServers(ResolveInput{
		Config:  &appcfg.Root{},
		Enabled: map[string]bool{"eslint": true},
		GOOS:    "linux",
	})
	_, diagnostics := ServersForFile(servers, filepath.Join(root, "a.ts"), root)
	if len(diagnostics) != 0 {
		t.Fatalf("no ESLint config in the project: diagnostics = %v", diagnostics)
	}
	writeTree(t, root, map[string]string{"web/eslint.config.js": ""})
	_, diagnostics = ServersForFile(servers, filepath.Join(root, "web", "a.ts"), root)
	if len(diagnostics) != 1 || diagnostics[0].ID != "eslint" {
		t.Fatalf("eslint.config.js in web/ must serve web/a.ts: diagnostics = %v", diagnostics)
	}
	_, diagnostics = ServersForFile(servers, filepath.Join(root, "a.ts"), root)
	if len(diagnostics) != 0 {
		t.Fatalf("a.ts at the root must not gain eslint from web/: %v", diagnostics)
	}
}

// TestRootMarkerWalkStopsAtProjectRoot: when the file path and the project
// root use different path forms (a symlink), the marker walk must still stop
// at the project root instead of treating markers above it as inside
// (spec §7.4 EvalSymlinks rule).
func TestRootMarkerWalkStopsAtProjectRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	writeTree(t, root, map[string]string{"src/a.ts": ""})
	link := root + "-link"
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	// A stray marker one level above the real project root.
	stray := filepath.Join(base, "eslint.config.js")
	if err := writeMarker(stray); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(stray) })
	srv := ServerConfig{ID: "eslint", RequireRootMarker: true, RootMarkers: []string{"eslint.config.js"}}
	if hasRootMarker(filepath.Join(root, "src", "a.ts"), link, srv) {
		t.Fatal("a marker above the project root must not open the gate")
	}
	if markersExistBetween(filepath.Join(root, "src"), link, srv.RootMarkers) {
		t.Fatal("a marker above the project root must not win a conflict")
	}
	writeTree(t, root, map[string]string{"eslint.config.js": ""})
	if !hasRootMarker(filepath.Join(root, "src", "a.ts"), link, srv) {
		t.Fatal("a marker at the project root must open the gate")
	}
	if !markersExistBetween(filepath.Join(root, "src"), link, srv.RootMarkers) {
		t.Fatal("a marker at the project root must win a conflict")
	}
}

// TestDenoConflictWins: deno.json flips the TypeScript primary to deno
// (spec §6.3).
func TestDenoConflictWins(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"main.ts": ""})
	servers := ResolveServers(ResolveInput{
		Config:  &appcfg.Root{},
		Enabled: map[string]bool{"deno": true, "typescript-language-server": true},
		GOOS:    "linux",
	})
	primary, _ := ServersForFile(servers, filepath.Join(root, "main.ts"), root)
	if primary == nil || primary.ID != "typescript-language-server" {
		t.Fatalf("without deno.json the TypeScript server must win, got %v", primary)
	}
	writeTree(t, root, map[string]string{"deno.json": ""})
	primary, _ = ServersForFile(servers, filepath.Join(root, "main.ts"), root)
	if primary == nil || primary.ID != "deno" {
		t.Fatalf("deno.json must flip the primary to deno, got %v", primary)
	}
}

// TestAlternatesLoseByPriority: with both enabled, the default server keeps
// the extension (spec §6.3).
func TestAlternatesLoseByPriority(t *testing.T) {
	servers := ResolveServers(ResolveInput{
		Config:  &appcfg.Root{},
		Enabled: map[string]bool{"pyright": true, "basedpyright": true},
		GOOS:    "linux",
	})
	primary, _ := ServersForFile(servers, "/p/main.py", "/p")
	if primary == nil || primary.ID != "pyright" {
		t.Fatalf("pyright must win over basedpyright, got %v", primary)
	}
}

func writeMarker(path string) error {
	return os.WriteFile(path, []byte(""), 0o644)
}

// writeTree creates files (content, may be empty) under base.
func writeTree(t *testing.T, base string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolveRoot(t *testing.T) {
	tmp := t.TempDir()
	root, err := filepath.EvalSymlinks(tmp) // ResolveRoot compares evaled paths
	if err != nil {
		root = tmp
	}
	writeTree(t, root, map[string]string{
		"go.mod":        "",
		"a/go.mod":      "",
		"a/b/f.go":      "",
		"a/src/f.rs":    "",
		"ws/inside.txt": "",
	})
	gopls := findServer(t, ResolveServers(ResolveInput{Config: &appcfg.Root{}, GOOS: "linux"}), "gopls")

	// Nearest marker from the file upward.
	if got, ok := ResolveRoot(filepath.Join(root, "a", "b", "f.go"), root, gopls); !ok || got != filepath.Join(root, "a") {
		t.Fatalf("nearest marker: got %q ok=%v", got, ok)
	}
	// No marker between file and root: the root itself.
	if got, ok := ResolveRoot(filepath.Join(root, "a", "b", "f.go"), filepath.Join(root, "a", "b"), gopls); !ok || got != filepath.Join(root, "a", "b") {
		t.Fatalf("no marker: got %q ok=%v", got, ok)
	}
	// A relative workspace folder resolves against the project root.
	folder := gopls
	folder.WorkspaceFolder = "ws"
	if got, ok := ResolveRoot(filepath.Join(root, "a", "b", "f.go"), root, folder); !ok || got != filepath.Join(root, "ws") {
		t.Fatalf("workspace folder: got %q ok=%v", got, ok)
	}
	// A workspace folder outside the project root is rejected.
	folder.WorkspaceFolder = "../outside"
	if _, ok := ResolveRoot(filepath.Join(root, "a", "b", "f.go"), root, folder); ok {
		t.Fatal("workspace folder outside the project must not resolve")
	}
	// A file outside the project root is rejected.
	if _, ok := ResolveRoot(filepath.Join(root, "..", "elsewhere.go"), root, gopls); ok {
		t.Fatal("files outside the project must not resolve")
	}
	// *.sln markers glob (spec §5.1).
	writeTree(t, root, map[string]string{"sharp/My.sln": "", "sharp/n/f.cs": ""})
	csharp := findServer(t, ResolveServers(ResolveInput{Config: &appcfg.Root{}, GOOS: "linux"}), "csharp-ls")
	if got, ok := ResolveRoot(filepath.Join(root, "sharp", "n", "f.cs"), root, csharp); !ok || got != filepath.Join(root, "sharp") {
		t.Fatalf("glob marker: got %q ok=%v", got, ok)
	}

	// cargo-workspace climbs to the topmost [workspace] Cargo.toml.
	writeTree(t, root, map[string]string{
		"Cargo.toml":    "[workspace]\nmembers = [\"a\"]\n",
		"a/Cargo.toml":  "[workspace]\n",
		"a/src/main.rs": "",
	})
	rust := findServer(t, ResolveServers(ResolveInput{Config: &appcfg.Root{}, GOOS: "linux"}), "rust-analyzer")
	if got, ok := ResolveRoot(filepath.Join(root, "a", "src", "main.rs"), root, rust); !ok || got != root {
		t.Fatalf("cargo workspace: got %q ok=%v", got, ok)
	}
}

func TestEnabledStateRoundTrip(t *testing.T) {
	workspace := t.TempDir()
	enabled, err := LoadEnabled(workspace)
	if err != nil {
		t.Fatalf("LoadEnabled on a fresh workspace: %v", err)
	}
	if len(enabled) != 0 {
		t.Fatalf("a missing enabled.json is empty, got %v", enabled)
	}
	if err := SaveEnabled(workspace, "gopls", true); err != nil {
		t.Fatalf("SaveEnabled: %v", err)
	}
	if err := SaveEnabled(workspace, "jdtls", false); err != nil {
		t.Fatalf("SaveEnabled: %v", err)
	}
	enabled, err = LoadEnabled(workspace)
	if err != nil {
		t.Fatalf("LoadEnabled: %v", err)
	}
	if !enabled["gopls"] || enabled["jdtls"] {
		t.Fatalf("round trip mismatch: %v", enabled)
	}
}

func TestToolEnabled(t *testing.T) {
	workspace := t.TempDir()
	cfg := &appcfg.Root{Features: appcfg.FeaturesSection{LSP: boolPtr(true)}}
	opts := ManagerOptions{AgentWorkspace: workspace, ProjectRoot: "/p/proj", Trusted: true}
	if ToolEnabled(cfg, opts) {
		t.Fatal("no enabled server means no tool")
	}
	if err := SaveEnabled(workspace, "gopls", true); err != nil {
		t.Fatal(err)
	}
	if !ToolEnabled(cfg, opts) {
		t.Fatal("an enabled catalog primary must register the tool")
	}
	untrusted := opts
	untrusted.Trusted = false
	if ToolEnabled(cfg, untrusted) {
		t.Fatal("untrusted projects never register the tool")
	}
	noRoot := opts
	noRoot.ProjectRoot = ""
	if ToolEnabled(cfg, noRoot) {
		t.Fatal("a runner without a project never registers the tool")
	}
	off := &appcfg.Root{Features: appcfg.FeaturesSection{LSP: boolPtr(false)}}
	if ToolEnabled(off, opts) {
		t.Fatal("features.lsp: false must not register the tool")
	}
}

// A consented project entry counts toward the "one enabled primary" the tool
// decision needs — the answer is taken at the freeze point, when the recorded
// decisions are the ones that apply (spec §8.5, §5.2).
func TestToolEnabledCountsConsentedProjectPrimary(t *testing.T) {
	project := t.TempDir()
	ws := t.TempDir()
	writeProjectLSP(t, project, "servers:\n  projectls:\n    command: /bin/projectls\n    extension_to_language: {\".pl\": projectls}\n")
	cfg := &appcfg.Root{Features: appcfg.FeaturesSection{LSP: boolPtr(true)}}
	opts := ManagerOptions{AgentWorkspace: ws, ProjectRoot: project, ProjectKey: "projkey", Trusted: true}
	if ToolEnabled(cfg, opts) {
		t.Fatal("an unconsented project entry must not register the tool")
	}
	if err := DecideProjectServers(ws, project, "projkey", []string{"projectls"}); err != nil {
		t.Fatal(err)
	}
	if !ToolEnabled(cfg, opts) {
		t.Fatal("a consented project primary must register the tool")
	}
	// The same entry in an untrusted project never applies.
	untrusted := opts
	untrusted.Trusted = false
	if ToolEnabled(cfg, untrusted) {
		t.Fatal("untrusted projects never register the tool")
	}
}
