package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

// writeExecutableScript creates a Unix shell script with the exec bit.
func writeExecutableScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetectOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts are not executable on windows")
	}
	dir := t.TempDir()
	script := writeExecutableScript(t, dir, "fakels", "echo fakels 1.2.3")
	srv := ServerConfig{Command: "fakels", Detect: DetectSpec{VersionArgs: []string{"--version"}}}
	res := Detect(context.Background(), srv, []string{"PATH=" + dir}, runtime.GOOS, "")
	if !res.Installed {
		t.Fatalf("the binary is on PATH: %+v", res)
	}
	if res.Path != script {
		t.Errorf("path is %q, want %q", res.Path, script)
	}
	if res.Version != "fakels 1.2.3" {
		t.Errorf("version is %q", res.Version)
	}
}

func TestDetectVersionFailureMeansNotInstalled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts are not executable on windows")
	}
	dir := t.TempDir()
	writeExecutableScript(t, dir, "fakels-fail", "exit 1")
	srv := ServerConfig{Command: "fakels-fail", Detect: DetectSpec{VersionArgs: []string{"--version"}}}
	res := Detect(context.Background(), srv, []string{"PATH=" + dir}, runtime.GOOS, "")
	if res.Installed {
		t.Fatal("a failing version command means not installed")
	}
	if res.Path == "" {
		t.Error("the binary was found; Path must be set even when not installed")
	}
}

func TestDetectExtraDirsAndHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts are not executable on windows")
	}
	base := t.TempDir()
	t.Setenv("HOME", base) // ~/ in extra dirs expands through os.UserHomeDir
	envDir := filepath.Join(base, "envbin")
	homeDir := filepath.Join(base, "bin")
	os.MkdirAll(envDir, 0o755)
	os.MkdirAll(homeDir, 0o755)
	writeExecutableScript(t, envDir, "fakels-extra", "echo fakels 2.0.0")
	writeExecutableScript(t, homeDir, "fakels-home", "echo fakels 2.0.0")
	env := []string{"PATH=" + t.TempDir(), "FOO=" + base}

	// $UNSET/bin is skipped; $FOO/envbin wins after it.
	srv := ServerConfig{Command: "fakels-extra", Detect: DetectSpec{ExtraDirs: []string{"$UNSET/bin", "$FOO/envbin"}}}
	res := Detect(context.Background(), srv, env, runtime.GOOS, "")
	if !res.Installed || res.Path != filepath.Join(envDir, "fakels-extra") {
		t.Fatalf("extra dirs: %+v", res)
	}

	// ~/ expands to the home directory.
	homeSrv := ServerConfig{Command: "fakels-home", Detect: DetectSpec{ExtraDirs: []string{"~/bin"}}}
	res = Detect(context.Background(), homeSrv, env, runtime.GOOS, "")
	if !res.Installed || res.Path != filepath.Join(homeDir, "fakels-home") {
		t.Fatalf("home extra dir: %+v", res)
	}
}

func TestDetectCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts are not executable on windows")
	}
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	writeExecutableScript(t, dir, "fakels-cache", "echo x >> "+count+"\necho fakels 3.1.4")
	cachePath := filepath.Join(dir, "detect.json")
	srv := ServerConfig{Command: "fakels-cache", Detect: DetectSpec{VersionArgs: []string{"--version"}}}
	env := []string{"PATH=" + dir}

	first := Detect(context.Background(), srv, env, runtime.GOOS, cachePath)
	second := Detect(context.Background(), srv, env, runtime.GOOS, cachePath)
	if !first.Installed || !second.Installed {
		t.Fatalf("both detects must be installed: %+v %+v", first, second)
	}
	if first.Version != "fakels 3.1.4" || second.Version != first.Version {
		t.Fatalf("versions: %q then %q", first.Version, second.Version)
	}
	data, err := os.ReadFile(count)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 1 {
		t.Fatalf("the version command ran %d times; the cache must answer the second call", lines)
	}
}

func TestUsableInstallRecipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires an executable helper on PATH")
	}
	dir := t.TempDir()
	writeExecutableScript(t, dir, "fakels-mk", "true")
	env := []string{"PATH=" + dir}
	srv := ServerConfig{Install: []InstallRecipe{
		{Requires: "fakels-absent", Argv: []string{"a", "b"}},
		{Requires: "fakels-mk", Platforms: []string{"plan9"}, Argv: []string{"c", "d"}},
		{Requires: "fakels-mk", Argv: []string{"e", "f"}},
	}}
	recipe := UsableInstallRecipe(srv, env, runtime.GOOS)
	if recipe == nil {
		t.Fatal("the third recipe is usable")
	}
	if strings.Join(recipe.Argv, " ") != "e f" {
		t.Errorf("recipe argv is %v", recipe.Argv)
	}
	none := ServerConfig{Install: []InstallRecipe{{Requires: "fakels-absent", Argv: []string{"x"}}}}
	if UsableInstallRecipe(none, env, runtime.GOOS) != nil {
		t.Error("no recipe is usable when the requirement is missing")
	}

	res := Detect(context.Background(), srv, env, runtime.GOOS, "")
	if res.InstallRecipe == nil || res.InstallCommand != "e f" {
		t.Fatalf("Detect fills the install recipe: %+v", res)
	}
}

// installFixture builds a PATH directory with a fake installer and returns it
// with the server the installer installs.
func installFixture(t *testing.T, body string) (string, ServerConfig) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts are not executable on windows")
	}
	dir := t.TempDir()
	writeExecutableScript(t, dir, "fake-installer", body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TARGET_DIR", dir)
	return dir, ServerConfig{
		ID:      "fakels",
		Command: "fakels",
		Install: []InstallRecipe{{Requires: "fake-installer", Argv: []string{"fake-installer"}}},
		Notes:   "see the fake documentation",
	}
}

func TestInstallRunsRecipeAndStreamsOutput(t *testing.T) {
	dir, srv := installFixture(t, `
	echo step one
	echo step two
	mkdir -p "$TARGET_DIR"
	printf '#!/bin/sh\ntrue\n' > "$TARGET_DIR/fakels"
	chmod +x "$TARGET_DIR/fakels"
`)
	var got []string
	cachePath := filepath.Join(dir, "detect.json")
	// A stale entry for the same binary name under another directory, plus an
	// unrelated one: the drop must remove only the first kind.
	stale := filepath.Join(t.TempDir(), "fakels")
	writeJSON(t, cachePath, map[string]any{
		stale:                       map[string]any{"mtime": 1, "version": "stale", "installed": false},
		filepath.Join(dir, "other"): map[string]any{"mtime": 1, "version": "v", "installed": true},
	})
	if err := Install(context.Background(), srv, runtime.GOOS, cachePath, func(line string) { got = append(got, line) }); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if strings.Join(got, "|") != "step one|step two" {
		t.Errorf("progress lines = %v", got)
	}
	entries := readJSON(t, cachePath)
	if _, ok := entries[stale]; ok {
		t.Fatalf("the stale fakels entry survived: %v", entries)
	}
	if _, ok := entries[filepath.Join(dir, "other")].(map[string]any); !ok {
		t.Errorf("the unrelated entry was dropped: %v", entries)
	}
	if _, ok := entries[filepath.Join(dir, "fakels")].(map[string]any); !ok {
		t.Errorf("the re-probe did not cache the new binary: %v", entries)
	}
}

func TestInstallUnavailable(t *testing.T) {
	_, srv := installFixture(t, "true")
	srv.Install = nil
	srv.Notes = ""
	err := Install(context.Background(), srv, runtime.GOOS, "", nil)
	if err == nil || err.Error() != "no install recipe for fakels on this system; install it manually: see the server's documentation" {
		t.Fatalf("error = %v, want the appendix C install-unavailable text", err)
	}
}

func TestInstallFailureIncludesTail(t *testing.T) {
	_, srv := installFixture(t, `
	i=1
	while [ $i -le 30 ]; do echo line $i; i=$((i+1)); done
	exit 3
`)
	err := Install(context.Background(), srv, runtime.GOOS, "", nil)
	if err == nil {
		t.Fatal("a failing installer must fail")
	}
	if !strings.Contains(err.Error(), "failed:") {
		t.Fatalf("error misses the failure: %v", err)
	}
	if !strings.Contains(err.Error(), "line 11") || !strings.Contains(err.Error(), "line 30") {
		t.Errorf("error misses the last 20 lines:\n%v", err)
	}
	if strings.Contains(err.Error(), "line 10") {
		t.Errorf("error keeps more than the last 20 lines:\n%v", err)
	}
}

func TestInstallStillMissing(t *testing.T) {
	_, srv := installFixture(t, "echo nothing to see")
	err := Install(context.Background(), srv, runtime.GOOS, "", nil)
	if err == nil || !strings.Contains(err.Error(), "is still not found") {
		t.Fatalf("error = %v, want the still-not-found text", err)
	}
}

func TestInstallTimeout(t *testing.T) {
	_, srv := installFixture(t, "sleep 5")
	origTimeout, origGrace := installTimeout, installKillGrace
	installTimeout, installKillGrace = 200*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { installTimeout, installKillGrace = origTimeout, origGrace })
	start := time.Now()
	err := Install(context.Background(), srv, runtime.GOOS, "", nil)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Install took %v; the timeout must bound it", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "failed:") {
		t.Fatalf("error = %v, want the timeout reported as a failure", err)
	}
}

// writeJSON stores a detect.json fixture.
func writeJSON(t *testing.T, path string, doc map[string]any) {
	t.Helper()
	blob, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		t.Fatal(err)
	}
}

// readJSON reads a detect.json fixture back.
func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{}
	if err := json.Unmarshal(blob, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// doctorReportOf picks one server's report out of a Doctor run.
func doctorReportOf(t *testing.T, reports []DoctorReport, id string) DoctorReport {
	t.Helper()
	for _, rep := range reports {
		if rep.ID == id {
			return rep
		}
	}
	t.Fatalf("doctor reported no %s server", id)
	return DoctorReport{}
}

// doctorCheckOf picks one check out of a report.
func doctorCheckOf(t *testing.T, rep DoctorReport, name string) DoctorCheck {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("report for %s has no %s check", rep.ID, name)
	return DoctorCheck{}
}

// doctorFakeConfig wires the compiled fake server as a custom configuration
// entry and returns its config with the server's record path.
func doctorFakeConfig(t *testing.T, script map[string]any) (*appcfg.Root, string) {
	t.Helper()
	exe := buildFakeServer(t)
	if script == nil {
		script = map[string]any{}
	}
	record := filepath.Join(t.TempDir(), "record.jsonl")
	script["record"] = record
	blob, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &appcfg.Root{LSP: appcfg.LSPSection{Servers: map[string]appcfg.LSPServerConfig{
		"fake": {
			Command:             exe,
			ExtensionToLanguage: map[string]string{".fk": "fake"},
			Env:                 map[string]string{"FAKE_LSP_SCRIPT": string(blob)},
		},
	}}}
	return cfg, record
}

func TestDoctorStaticChecks(t *testing.T) {
	exe := buildFakeServer(t)
	orig := catalogOverride
	catalogOverride = []CatalogEntry{
		{
			ID: "absent", DisplayName: "absent", Languages: []string{"Absent"},
			ExtensionToLanguage: map[string]string{".xa": "absent"}, Command: "fakels-absent-x",
		},
		{
			ID: "present", DisplayName: "present", Languages: []string{"Fake"},
			ExtensionToLanguage: map[string]string{".xf": "fake"}, Command: exe,
		},
	}
	t.Cleanup(func() { catalogOverride = orig })
	ws := t.TempDir()
	if err := SaveEnabled(ws, "absent", true); err != nil {
		t.Fatal(err)
	}
	if err := SaveEnabled(ws, "present", true); err != nil {
		t.Fatal(err)
	}
	// A custom entry without a command: invalid, so its config check fails.
	cfg := &appcfg.Root{LSP: appcfg.LSPSection{Servers: map[string]appcfg.LSPServerConfig{
		"weird": {},
	}}}
	reports, err := Doctor(context.Background(), DoctorInput{
		Config: cfg, Home: t.TempDir(), AgentWorkspace: ws,
		ProjectRoot: t.TempDir(), Trusted: true,
	})
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if len(reports) != 3 {
		t.Fatalf("reports cover %d servers, want 3", len(reports))
	}

	absent := doctorReportOf(t, reports, "absent")
	if c := doctorCheckOf(t, absent, "config"); c.Status != "ok" || c.Detail != "catalog entry, fingerprint "+absentCheckFingerprint(t, absent.ID) {
		t.Errorf("absent config check = %+v", c)
	}
	c := doctorCheckOf(t, absent, "binary")
	if c.Status != "fail" || !strings.Contains(c.Detail, "fakels-absent-x not found") {
		t.Errorf("absent binary check = %+v", c)
	}
	for _, name := range []string{"environment", "project", "handshake"} {
		if c := doctorCheckOf(t, absent, name); c.Status != "skip" || c.Detail != "skipped: binary failed" {
			t.Errorf("absent %s check = %+v, want skipped after binary", name, c)
		}
	}

	weird := doctorReportOf(t, reports, "weird")
	if c := doctorCheckOf(t, weird, "config"); c.Status != "fail" || c.Detail == "" {
		t.Errorf("weird config check = %+v, want the invalid-entry failure", c)
	}
	for _, name := range []string{"binary", "environment", "project", "handshake"} {
		if c := doctorCheckOf(t, weird, name); c.Status != "skip" || c.Detail != "skipped: config failed" {
			t.Errorf("weird %s check = %+v, want skipped after config", name, c)
		}
	}

	present := doctorReportOf(t, reports, "present")
	if c := doctorCheckOf(t, present, "binary"); c.Status != "ok" || c.Detail != exe {
		t.Errorf("present binary check = %+v", c)
	}
	if c := doctorCheckOf(t, present, "environment"); c.Status != "ok" || !strings.HasPrefix(c.Detail, "passes 0 of 0 allowed variables") {
		t.Errorf("present environment check = %+v", c)
	}
	if c := doctorCheckOf(t, present, "project"); c.Status != "ok" {
		t.Errorf("present project check = %+v", c)
	}
	if c := doctorCheckOf(t, present, "handshake"); c.Status != "skip" || c.Detail != "skipped" {
		t.Errorf("present handshake check = %+v, want skipped: not requested", c)
	}
}

// absentCheckFingerprint re-resolves one catalog server to compare the
// fingerprint the config check reports.
func absentCheckFingerprint(t *testing.T, id string) string {
	t.Helper()
	servers := ResolveServers(ResolveInput{GOOS: runtime.GOOS})
	for _, sc := range servers {
		if sc.ID == id {
			return sc.Fingerprint
		}
	}
	t.Fatalf("server %s missing from ResolveServers", id)
	return ""
}

func TestDoctorHints(t *testing.T) {
	ws := t.TempDir()
	if err := SaveEnabled(ws, "clangd", true); err != nil {
		t.Fatal(err)
	}
	reports, err := Doctor(context.Background(), DoctorInput{
		AgentWorkspace: ws, ProjectRoot: t.TempDir(), Trusted: true,
	})
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	c := doctorCheckOf(t, doctorReportOf(t, reports, "clangd"), "project")
	if c.Status != "warn" {
		t.Fatalf("clangd project check = %+v, want warn", c)
	}
	if !strings.Contains(c.Detail, "no compile_commands.json: generate one with cmake -DCMAKE_EXPORT_COMPILE_COMMANDS=ON or bear -- make") {
		t.Errorf("project detail misses the compile_commands hint: %s", c.Detail)
	}
	if !strings.Contains(c.Detail, "writes into the project: .cache/clangd") {
		t.Errorf("project detail misses the project_writes hint: %s", c.Detail)
	}
}

// TestDoctorHintsCSharpRestore: the dotnet restore hint must accept
// project.assets.json under <project>/obj/ both at the root and one level
// deep (task 12 hint table) — .NET writes restore assets next to each
// project, not under <root>/obj/<project>/.
func TestDoctorHintsCSharpRestore(t *testing.T) {
	srv := ServerConfig{ID: "csharp-ls"}
	assertRestore := func(t *testing.T, hints []string, want bool) {
		t.Helper()
		got := false
		for _, h := range hints {
			if h == "packages are not restored: run dotnet restore" {
				got = true
			}
		}
		if got != want {
			t.Fatalf("restore hint = %v, want %v (hints: %v)", got, want, hints)
		}
	}

	root := t.TempDir()
	writeTree(t, root, map[string]string{"App.csproj": ""})
	assertRestore(t, doctorHints(srv, root), true)
	writeTree(t, root, map[string]string{"obj/project.assets.json": ""})
	assertRestore(t, doctorHints(srv, root), false)

	sln := t.TempDir()
	writeTree(t, sln, map[string]string{"Solution.sln": "", "App/App.csproj": ""})
	assertRestore(t, doctorHints(srv, sln), true)
	writeTree(t, sln, map[string]string{"App/obj/project.assets.json": ""})
	assertRestore(t, doctorHints(srv, sln), false)
}

func TestDoctorHandshake(t *testing.T) {
	cfg, record := doctorFakeConfig(t, map[string]any{
		"capabilities": map[string]any{"definitionProvider": true, "referencesProvider": true},
	})
	reports, err := Doctor(context.Background(), DoctorInput{
		Config: cfg, Home: t.TempDir(), AgentWorkspace: t.TempDir(),
		ProjectRoot: t.TempDir(), Trusted: true, Handshake: true,
	})
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	rep := doctorReportOf(t, reports, "fake")
	if !rep.Enabled {
		t.Fatal("custom entries are enabled by default")
	}
	c := doctorCheckOf(t, rep, "handshake")
	if c.Status != "ok" {
		t.Fatalf("handshake check = %+v", c)
	}
	if !strings.HasPrefix(c.Detail, "initialized in ") {
		t.Errorf("handshake detail = %q, want the elapsed time first", c.Detail)
	}
	if !strings.Contains(c.Detail, "definition, references") {
		t.Errorf("handshake detail = %q, want the capability list", c.Detail)
	}
	if rep.LogPath == "" {
		t.Error("the handshake log path is missing")
	}
	pid := pidOf(t, record)
	waitForProcessGone(t, pid)
}

func TestDoctorSkipsUntrusted(t *testing.T) {
	cfg, record := doctorFakeConfig(t, nil)
	reports, err := Doctor(context.Background(), DoctorInput{
		Config: cfg, Home: t.TempDir(), AgentWorkspace: t.TempDir(),
		ProjectRoot: t.TempDir(), Trusted: false, Handshake: true,
	})
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	rep := doctorReportOf(t, reports, "fake")
	if c := doctorCheckOf(t, rep, "project"); c.Status != "warn" || c.Detail != "project is not trusted; servers only run in trusted projects" {
		t.Errorf("project check = %+v", c)
	}
	c := doctorCheckOf(t, rep, "handshake")
	if c.Status != "skip" || c.Detail != "skipped: servers only run in trusted projects" {
		t.Fatalf("handshake check = %+v, want the trust skip", c)
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatal("doctor started a server in an untrusted project")
	}
}
