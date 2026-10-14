package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

func writeProjectMCPYAML(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, ".forebrain", "mcp_servers.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSameServerNameIsCaseInsensitive(t *testing.T) {
	if !SameServerName("CodeGraph", " codegraph ") {
		t.Fatalf("expected case-insensitive match after trimming")
	}
	if SameServerName("a", "b") {
		t.Fatalf("distinct names must not match")
	}
}

func TestLoadProjectMCPServersSortsByName(t *testing.T) {
	dir := t.TempDir()
	writeProjectMCPYAML(t, dir, `mcp_servers:
  - name: zeta
    transport: stdio
    command: /bin/z
  - name: Alpha
    transport: stdio
    command: /bin/a
  - name: mid
    transport: stdio
    command: /bin/m
`)
	servers, notes := LoadProjectMCPServers(dir)
	if len(notes) != 0 {
		t.Fatalf("unexpected notes: %+v", notes)
	}
	want := []string{"Alpha", "mid", "zeta"}
	if len(servers) != len(want) {
		t.Fatalf("servers=%+v", servers)
	}
	for i, name := range want {
		if servers[i].Name != name {
			t.Fatalf("position %d: got %q want %q", i, servers[i].Name, name)
		}
	}
}

// Forebrain Harness reads only its own project file. A repository carrying another
// agent's .mcp.json gets nothing loaded natively — importing that file is
// /migrate's job, and this is the regression test for that boundary.
func TestLoadProjectMCPServersIgnoresForeignMCPJSON(t *testing.T) {
	dir := t.TempDir()
	body := `{"mcpServers": {"zebra": {"type": "stdio", "command": "/bin/z"}, "apple": {"type": "http", "url": "http://127.0.0.1:1"}}}`
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	servers, notes := LoadProjectMCPServers(dir)
	if len(servers) != 0 {
		t.Fatalf(".mcp.json must not be read natively: %+v", servers)
	}
	if len(notes) != 0 {
		t.Fatalf("unexpected notes: %+v", notes)
	}
	if paths := ProjectMCPPaths(dir); len(paths) != 1 || !strings.HasSuffix(paths[0], filepath.Join(".forebrain", "mcp_servers.yaml")) {
		t.Fatalf("ProjectMCPPaths must list exactly one candidate, got %v", paths)
	}
}

func TestLoadProjectMCPServersForebrainFileOnly(t *testing.T) {
	dir := t.TempDir()
	writeProjectMCPYAML(t, dir, `mcp_servers:
  - name: shared
    transport: stdio
    command: /bin/from-forebrain
`)
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{"mcpServers": {"shared": {"type": "stdio", "command": "/bin/from-json"}, "only-json": {"type": "stdio", "command": "/bin/j"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	servers, _ := LoadProjectMCPServers(dir)
	if len(servers) != 1 || servers[0].Name != "shared" || servers[0].Command != "/bin/from-forebrain" {
		t.Fatalf("expected only the .forebrain entry, got %+v", servers)
	}
}

func TestLoadProjectMCPServersRejectsPlaintextSecrets(t *testing.T) {
	dir := t.TempDir()
	writeProjectMCPYAML(t, dir, "mcp_servers:\n  - name: leaky\n    transport: stdio\n    command: /bin/x\n    env:\n      API_KEY: abc123\n")
	servers, notes := LoadProjectMCPServers(dir)
	if len(servers) != 0 {
		t.Fatalf("plaintext secret must drop all entries: %+v", servers)
	}
	if len(notes) != 1 || !strings.Contains(notes[0].Reason, "plaintext secret") {
		t.Fatalf("notes=%+v", notes)
	}
}

func TestLoadProjectMCPServersParseFailureIsOneLine(t *testing.T) {
	dir := t.TempDir()
	writeProjectMCPYAML(t, dir, "mcp_servers: [this: is: not: valid")
	servers, notes := LoadProjectMCPServers(dir)
	if len(servers) != 0 {
		t.Fatalf("unparseable file must yield no entries")
	}
	if len(notes) != 1 {
		t.Fatalf("notes=%+v", notes)
	}
	if strings.Contains(notes[0].Reason, "[") || strings.Contains(notes[0].Reason, "yaml") {
		t.Fatalf("reason must be a one-line semantic sentence, got %q", notes[0].Reason)
	}
}

func TestMergeSessionServersStampsDeepCopies(t *testing.T) {
	global := []appcfg.MCPServerConfig{{Name: "Graph", Transport: "stdio", Command: "/bin/g", Env: map[string]string{"A": "1"}}}
	project := []appcfg.MCPServerConfig{{Name: "graph", Transport: "stdio", Command: "/bin/pg", Env: map[string]string{"B": "2"}, DefaultToolsApprovalMode: appcfg.MCPToolApprovalAuto}}
	res := MergeSessionServers(global, nil, project, "", "-users-x-proj")
	if len(res.Servers) != 1 {
		t.Fatalf("same-named entry must replace, not append: %+v", res.Servers)
	}
	got := res.Servers[0]
	if got.Command != "/bin/pg" || got.Scope != appcfg.MCPServerScopeProject || got.ProjectKey != "-users-x-proj" {
		t.Fatalf("merged=%+v", got)
	}
	if got.DefaultToolsApprovalMode != appcfg.MCPToolApprovalPrompt {
		t.Fatalf("project approval mode must clamp auto -> prompt, got %q", got.DefaultToolsApprovalMode)
	}
	// Deep copy: mutating the result must not touch the caller's slices.
	got.Env["B"] = "mutated"
	if global[0].Env["A"] != "1" || len(global[0].Env) != 1 {
		t.Fatalf("global list polluted: %+v", global[0].Env)
	}
	if res.OverriddenGlobal[0] != "Graph" {
		t.Fatalf("overridden=%v", res.OverriddenGlobal)
	}
}

func TestMergeSessionServersNoProjectFilesEqualsGlobal(t *testing.T) {
	global := []appcfg.MCPServerConfig{
		{Name: "a", Transport: "stdio", Command: "/bin/a", Env: map[string]string{"K": "v"}},
		{Name: "b", URL: "http://x"},
	}
	res := MergeSessionServers(global, nil, nil, "", "")
	if len(res.Servers) != 2 {
		t.Fatalf("servers=%+v", res.Servers)
	}
	for i := range res.Servers {
		orig := global[i]
		got := res.Servers[i]
		if got.Name != orig.Name || got.Transport != orig.Transport || got.Command != orig.Command || got.URL != orig.URL || got.Env["K"] != orig.Env["K"] {
			t.Fatalf("entry %d differs beyond scope stamps: %+v vs %+v", i, got, orig)
		}
		if got.Scope != appcfg.MCPServerScopeGlobal || got.ProjectKey != "" {
			t.Fatalf("expected global stamp: %+v", got)
		}
	}
	if len(res.OverriddenGlobal) != 0 {
		t.Fatalf("overridden=%v", res.OverriddenGlobal)
	}
}

func TestMergeSessionServersDuplicateGlobalsAllOverridden(t *testing.T) {
	global := []appcfg.MCPServerConfig{
		{Name: "dup", Command: "/bin/1"},
		{Name: "other", Command: "/bin/o"},
		{Name: "Dup", Command: "/bin/2"},
	}
	project := []appcfg.MCPServerConfig{{Name: "DUP", Command: "/bin/p"}}
	res := MergeSessionServers(global, nil, project, "", "pk")
	if len(res.Servers) != 2 {
		t.Fatalf("servers=%+v", res.Servers)
	}
	if len(res.OverriddenGlobal) != 2 {
		t.Fatalf("both duplicate globals count as overridden: %v", res.OverriddenGlobal)
	}
}

func TestMCPServerConfigRuntimeStampsDoNotSerialize(t *testing.T) {
	srv := appcfg.MCPServerConfig{Name: "x", Scope: appcfg.MCPServerScopeProject, ProjectKey: "pk"}
	b, err := json.Marshal(srv)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(b))
	if strings.Contains(lower, "scope") || strings.Contains(lower, "project_key") || strings.Contains(lower, "projectkey") {
		t.Fatalf("runtime stamps must not serialize: %s", b)
	}
}

func TestClampProjectRuntimePolicy(t *testing.T) {
	cases := []struct {
		in   appcfg.MCPToolApprovalMode
		want appcfg.MCPToolApprovalMode
	}{
		{"", appcfg.MCPToolApprovalPrompt},
		{appcfg.MCPToolApprovalAuto, appcfg.MCPToolApprovalPrompt},
		{appcfg.MCPToolApprovalPrompt, appcfg.MCPToolApprovalPrompt},
		{appcfg.MCPToolApprovalWrites, appcfg.MCPToolApprovalWrites},
		{appcfg.MCPToolApprovalApprove, appcfg.MCPToolApprovalApprove},
	}
	for _, tc := range cases {
		srv := appcfg.MCPServerConfig{Name: "n", DefaultToolsApprovalMode: tc.in,
			Tools: map[string]appcfg.MCPServerToolConfig{"t": {ApprovalMode: tc.in}}}
		ClampProjectRuntimePolicy(&srv)
		if srv.DefaultToolsApprovalMode != tc.want {
			t.Fatalf("default %q: got %q want %q", tc.in, srv.DefaultToolsApprovalMode, tc.want)
		}
		if srv.Tools["t"].ApprovalMode != tc.want {
			t.Fatalf("tool %q: got %q want %q", tc.in, srv.Tools["t"].ApprovalMode, tc.want)
		}
	}
}

func TestProjectEnvReferencesDoNotReadHostEnv(t *testing.T) {
	t.Setenv("MCP_HOST_ONLY_SECRET", "host-value")
	// A .env file under a fake home carries the operator's value.
	fakeHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeHome, ".env"), []byte("MCP_DOTENV_ONLY=dotenv-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := appcfg.MCPServerConfig{
		Name: "p", Transport: "stdio", Command: "/bin/x",
		Scope: appcfg.MCPServerScopeProject, ProjectKey: "pk",
		Env: map[string]string{
			"FROM_HOST":   "${MCP_HOST_ONLY_SECRET}",
			"FROM_DOTENV": "${MCP_DOTENV_ONLY}",
		},
	}
	env := mcpStdioEnv(fakeHome, srv)
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "host-value") {
		t.Fatalf("project entry must not read host environment: %s", joined)
	}
	if !strings.Contains(joined, "FROM_DOTENV=dotenv-value") {
		t.Fatalf("project entry should resolve from ~/.forebrain/.env: %s", joined)
	}
}

func TestProjectInheritParentEnvForcedOff(t *testing.T) {
	// A host variable outside the subprocess allowlist must never reach a
	// project entry's subprocess, even though its file asked for
	// inherit_parent_env: the request is repository-controlled text.
	t.Setenv("MCP_PARENT_VISIBLE", "leak-me")
	inherit := true
	srv := appcfg.MCPServerConfig{Name: "p", Transport: "stdio", Command: "/bin/x",
		Scope: appcfg.MCPServerScopeProject, ProjectKey: "pk", InheritParentEnv: &inherit}
	env := mcpStdioEnv(t.TempDir(), srv)
	if strings.Contains(strings.Join(env, "\n"), "leak-me") {
		t.Fatalf("project entry must not inherit parent env even when the file asks: %s", strings.Join(env, "\n"))
	}
}

func TestSameProjectEntryDetectsDrift(t *testing.T) {
	frozen := []appcfg.MCPServerConfig{
		{Name: "g", Scope: appcfg.MCPServerScopeGlobal},
		{Name: "p1", Scope: appcfg.MCPServerScopeProject},
	}
	same := []appcfg.MCPServerConfig{
		{Name: "other", Scope: appcfg.MCPServerScopeGlobal},
		{Name: "P1", Scope: appcfg.MCPServerScopeProject},
	}
	if !SameProjectEntry(frozen, same) {
		t.Fatalf("same project entries (case-insensitive) should match")
	}
	drift := []appcfg.MCPServerConfig{
		{Name: "other", Scope: appcfg.MCPServerScopeGlobal},
		{Name: "P1", Scope: appcfg.MCPServerScopeProject},
		{Name: "p2", Scope: appcfg.MCPServerScopeProject},
	}
	if SameProjectEntry(frozen, drift) {
		t.Fatalf("added entry must read as drift")
	}
}

func TestProjectEntryMissingFilesYieldNothing(t *testing.T) {
	servers, notes := LoadProjectMCPServers(t.TempDir())
	if len(servers) != 0 || len(notes) != 0 {
		t.Fatalf("servers=%+v notes=%+v", servers, notes)
	}
	if paths := ProjectMCPPaths(""); paths != nil {
		t.Fatalf("empty root has no paths")
	}
}

func TestLoadProjectMCPServersInvalidApprovalMode(t *testing.T) {
	dir := t.TempDir()
	writeProjectMCPYAML(t, dir, "mcp_servers:\n  - name: bad\n    transport: stdio\n    command: /bin/x\n    default_tools_approval_mode: sometimes\n")
	servers, notes := LoadProjectMCPServers(dir)
	if len(servers) != 0 {
		t.Fatalf("invalid approval mode must drop entries")
	}
	if len(notes) != 1 || !strings.Contains(notes[0].Reason, "approval mode") {
		t.Fatalf("notes=%+v", notes)
	}
}

func TestFingerprintChangesWithCommandAndURL(t *testing.T) {
	base := appcfg.MCPServerConfig{Name: "n", Transport: "stdio", Command: "/bin/x"}
	fp1 := ServerFingerprint(base)
	changed := base
	changed.Command = "/bin/y"
	if ServerFingerprint(changed) == fp1 {
		t.Fatalf("command change must change fingerprint")
	}
	urlChanged := appcfg.MCPServerConfig{Name: "n", Transport: "http", URL: "http://a"}
	urlChanged2 := appcfg.MCPServerConfig{Name: "n", Transport: "http", URL: "http://b"}
	if ServerFingerprint(urlChanged) == ServerFingerprint(urlChanged2) {
		t.Fatalf("url change must change fingerprint")
	}
	// Scope stamps do not participate.
	stamped := base
	stamped.Scope = appcfg.MCPServerScopeProject
	stamped.ProjectKey = "pk"
	if ServerFingerprint(stamped) != fp1 {
		t.Fatalf("scope stamps must not change the fingerprint")
	}
}

// TestServerFingerprintIgnoresStartupPolicy pins the split between the two
// fingerprints. This one is the identity a project MCP consent was granted for:
// "this entry runs this command and reaches this URL". The startup policy — how
// long the runtime waits, whether the server is required — is a local patience
// dial, and folding it in here would revoke every stored consent the moment the
// operator changed a timeout, asking them to authorize a server that had not
// changed at all.
func TestServerFingerprintIgnoresStartupPolicy(t *testing.T) {
	base := appcfg.MCPServerConfig{Name: "n", Transport: "stdio", Command: "/bin/x"}
	fp := ServerFingerprint(base)

	longer := base
	longer.StartupTimeout = 300
	if got := ServerFingerprint(longer); got != fp {
		t.Fatalf("startup_timeout changed the consent identity: %s vs %s", got, fp)
	}
	required := base
	required.Required = true
	if got := ServerFingerprint(required); got != fp {
		t.Fatalf("required changed the consent identity: %s vs %s", got, fp)
	}
	both := base
	both.StartupTimeout = 0.5
	both.Required = true
	if got := ServerFingerprint(both); got != fp {
		t.Fatalf("startup policy changed the consent identity: %s vs %s", got, fp)
	}

	// The stored-consent consequence, spelled out: a decision recorded for the
	// original entry still answers for the entry with a different policy.
	consents := ProjectConsents{}
	consents.Decide("proj", "n", fp, ProjectConsentAllow, time.Now())
	decision, decided := consents.Decision("proj", "n", ServerFingerprint(both))
	if !decided || decision != ProjectConsentAllow {
		t.Fatalf("changing startup_timeout revoked a stored consent: decided=%v decision=%q", decided, decision)
	}
}

func TestProjectConsentStoreRoundTrip(t *testing.T) {
	ws := t.TempDir()
	c, err := LoadProjectConsents(ws)
	if err != nil || len(c) != 0 {
		t.Fatalf("initial store should be empty: %v %+v", err, c)
	}
	fp := ServerFingerprint(appcfg.MCPServerConfig{Name: "n", Command: "/bin/x"})
	c.Decide("proj-a", "n", fp, ProjectConsentAllow, time.Now())
	if err := SaveProjectConsents(ws, c); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadProjectConsents(ws)
	if err != nil {
		t.Fatal(err)
	}
	if decision, decided := reloaded.Decision("proj-a", "N", fp); !decided || decision != ProjectConsentAllow {
		t.Fatalf("decision=%q decided=%v", decision, decided)
	}
	// Another project has no say.
	if _, decided := reloaded.Decision("proj-b", "n", fp); decided {
		t.Fatalf("decisions must not cross projects")
	}
	// A changed fingerprint re-asks.
	if _, decided := reloaded.Decision("proj-a", "n", "other-fp"); decided {
		t.Fatalf("changed fingerprint must not count as decided")
	}
	// Deny round-trips too.
	c.Decide("proj-a", "n", "other-fp", ProjectConsentDeny, time.Now())
	if err := SaveProjectConsents(ws, c); err != nil {
		t.Fatal(err)
	}
	reloaded, _ = LoadProjectConsents(ws)
	if decision, decided := reloaded.Decision("proj-a", "n", "other-fp"); !decided || decision != ProjectConsentDeny {
		t.Fatalf("deny decision lost: %q %v", decision, decided)
	}
}

func TestProjectConsentStoresArePerWorkspace(t *testing.T) {
	wsA, wsB := t.TempDir(), t.TempDir()
	fp := ServerFingerprint(appcfg.MCPServerConfig{Name: "n", Command: "/bin/x"})
	a, _ := LoadProjectConsents(wsA)
	a.Decide("p", "n", fp, ProjectConsentAllow, time.Now())
	if err := SaveProjectConsents(wsA, a); err != nil {
		t.Fatal(err)
	}
	b, _ := LoadProjectConsents(wsB)
	if _, decided := b.Decision("p", "n", fp); decided {
		t.Fatalf("another agent workspace must not see this workspace's consents")
	}
	if ProjectConsentPath(wsA) == ProjectConsentPath(wsB) {
		t.Fatalf("workspaces must map to different stores")
	}
}

func TestConsentSummaryDescribesCommandAndURL(t *testing.T) {
	if s := ConsentSummary(appcfg.MCPServerConfig{Name: "cmd", Command: "/bin/run", Args: []string{"--fast"}}); !strings.Contains(s, "/bin/run --fast") {
		t.Fatalf("summary=%q", s)
	}
	if s := ConsentSummary(appcfg.MCPServerConfig{Name: "url", URL: "http://127.0.0.1:9"}); !strings.Contains(s, "http://127.0.0.1:9") {
		t.Fatalf("summary=%q", s)
	}
}

func TestOverlayProjectKey(t *testing.T) {
	if OverlayProjectKey(appcfg.MCPServerConfig{Name: "g"}) != "" {
		t.Fatalf("global entry has no overlay project key")
	}
	if OverlayProjectKey(appcfg.MCPServerConfig{Name: "p", Scope: appcfg.MCPServerScopeProject, ProjectKey: "pk"}) != "pk" {
		t.Fatalf("project entry names its overlay namespace")
	}
}

func TestOAuthOverlayScopeNamespacing(t *testing.T) {
	home := t.TempDir()
	globalSrv := appcfg.MCPServerConfig{Name: "shared", Transport: "http", URL: "http://g"}
	projectSrv := appcfg.MCPServerConfig{Name: "shared", Transport: "http", URL: "http://p", Scope: appcfg.MCPServerScopeProject, ProjectKey: "-proj-key"}

	// Store a credential for the global server.
	if err := SaveOAuthOverlayMerge(home, "", "shared", appcfg.MCPOAuthConfig{AccessToken: "global-token"}); err != nil {
		t.Fatal(err)
	}
	merged := MergeMCPServerOAuth(home, projectSrv)
	if merged.OAuth.AccessToken != "" {
		t.Fatalf("project entry must not read the global entry's token: %q", merged.OAuth.AccessToken)
	}
	mergedGlobal := MergeMCPServerOAuth(home, globalSrv)
	if mergedGlobal.OAuth.AccessToken != "global-token" {
		t.Fatalf("global entry must still read its token")
	}
	// Project credential lands under projects/<key>/.
	if err := SaveOAuthOverlayMerge(home, "-proj-key", "shared", appcfg.MCPOAuthConfig{AccessToken: "project-token"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "mcp-oauth", "projects", "-proj-key", "shared.json")); err != nil {
		t.Fatalf("project overlay missing at namespaced path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "mcp-oauth", "shared.json")); err != nil {
		t.Fatalf("global overlay path must be unchanged: %v", err)
	}
	merged = MergeMCPServerOAuth(home, projectSrv)
	if merged.OAuth.AccessToken != "project-token" {
		t.Fatalf("project entry must read its own namespaced token: %q", merged.OAuth.AccessToken)
	}
	// The global token did not move.
	mergedGlobal = MergeMCPServerOAuth(home, globalSrv)
	if mergedGlobal.OAuth.AccessToken != "global-token" {
		t.Fatalf("global token unchanged, got %q", mergedGlobal.OAuth.AccessToken)
	}
}

func TestEnvLookupForServerRestrictsProjectScope(t *testing.T) {
	t.Setenv("PROBE_HOST", "host-value")
	fakeHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeHome, ".env"), []byte("PROBE_DOTENV=dotenv-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	globalLookup := envLookupForServer(fakeHome, appcfg.MCPServerConfig{Name: "g"})
	if v, ok := globalLookup("PROBE_HOST"); !ok || v != "host-value" {
		t.Fatalf("global lookup should read host env")
	}
	projectLookup := envLookupForServer(fakeHome, appcfg.MCPServerConfig{Name: "p", Scope: appcfg.MCPServerScopeProject, ProjectKey: "pk"})
	if _, ok := projectLookup("PROBE_HOST"); ok {
		t.Fatalf("project lookup must not read host env")
	}
	if v, ok := projectLookup("PROBE_DOTENV"); !ok || v != "dotenv-value" {
		t.Fatalf("project lookup should read ~/.forebrain/.env")
	}
}

func TestLoadProjectMCPServersJSONWithMissingFiles(t *testing.T) {
	dir := t.TempDir()
	servers, notes := LoadProjectMCPServers(dir)
	if len(servers) != 0 || len(notes) != 0 {
		t.Fatalf("empty project dir: servers=%+v notes=%+v", servers, notes)
	}
}

func TestDeepCopyMCPServerIsolatesReferences(t *testing.T) {
	srv := appcfg.MCPServerConfig{Name: "n", Args: []string{"a"}, Env: map[string]string{"k": "v"},
		Tools: map[string]appcfg.MCPServerToolConfig{"t": {ApprovalMode: appcfg.MCPToolApprovalWrites}}}
	cp := DeepCopyMCPServer(srv)
	cp.Args[0] = "b"
	cp.Env["k"] = "mut"
	cp.Tools["t"] = appcfg.MCPServerToolConfig{ApprovalMode: appcfg.MCPToolApprovalAuto}
	if srv.Args[0] != "a" || srv.Env["k"] != "v" || srv.Tools["t"].ApprovalMode != appcfg.MCPToolApprovalWrites {
		t.Fatalf("deep copy shares state with original")
	}
}

func TestProjectMCPPathsOrder(t *testing.T) {
	paths := ProjectMCPPaths("/proj")
	if len(paths) != 1 || !strings.HasSuffix(paths[0], filepath.Join(".forebrain", "mcp_servers.yaml")) {
		t.Fatalf("paths=%v, want exactly forebrain's own file", paths)
	}
}

// TestEffectiveListSerializationIsByteStable pins the cache red line end to
// end at the config layer: the serialized effective list — the input the
// tools+system prefix is derived from — must be byte-identical across loads,
// whatever order a map-shaped project file happened to iterate in, and
// whatever order its entries were written in.
func TestEffectiveListSerializationIsByteStable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{"mcpServers":{"z":{"type":"stdio","command":"/bin/z"},"a":{"type":"http","url":"http://a"},"m":{"type":"stdio","command":"/bin/m"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	global := []appcfg.MCPServerConfig{{Name: "g1", Transport: "stdio", Command: "/bin/g"}}
	var first []byte
	for i := 0; i < 3; i++ {
		project, notes := LoadProjectMCPServers(dir)
		if len(notes) != 0 {
			t.Fatalf("notes: %+v", notes)
		}
		merged := MergeSessionServers(global, nil, project, "", "-proj")
		b, err := json.MarshalIndent(merged.Servers, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = b
			continue
		}
		if string(b) != string(first) {
			t.Fatalf("effective list serialization is not byte-stable:\n%s\n---\n%s", first, b)
		}
	}
	project, _ := LoadProjectMCPServers(dir)
	fp := ServerFingerprint(MergeSessionServers(global, nil, project, "", "-proj").Servers[0])
	for i := 0; i < 3; i++ {
		if got := ServerFingerprint(MergeSessionServers(global, nil, project, "", "-proj").Servers[0]); got != fp {
			t.Fatalf("fingerprint not stable: %s vs %s", got, fp)
		}
	}
}

// TestProjectEntriesCannotSetTheirOwnStartupPolicy pins the other half of the
// clamp: a repository decides what its server is, not how much of the
// operator's session it may spend.
//
// "required" and a long startup timeout are both patience, and neither is
// covered by the consent the operator gave — consent answers "may this server
// run", not "may it hold the first turn and refuse to be skipped". Left
// unclamped, a checked-in file could make every unattended run fail and every
// interactive Escape decline to skip.
func TestProjectEntriesCannotSetTheirOwnStartupPolicy(t *testing.T) {
	project := []appcfg.MCPServerConfig{{
		Name:           "repo-server",
		Transport:      "stdio",
		Command:        "/bin/true",
		Required:       true,
		StartupTimeout: 86400,
	}}
	merged := MergeSessionServers(nil, nil, project, "", "key")
	if len(merged.Servers) != 1 {
		t.Fatalf("merged = %+v", merged.Servers)
	}
	got := merged.Servers[0]
	if got.Required {
		t.Fatal("a project entry must not be able to mark itself required")
	}
	if got.StartupTimeout != 0 {
		t.Fatalf("startup_timeout = %v, want the runtime default", got.StartupTimeout)
	}
	if timeout := StartupTimeoutFor(got); timeout != DefaultStartupTimeout {
		t.Fatalf("effective timeout = %s, want %s", timeout, DefaultStartupTimeout)
	}

	// A patience shorter than the default is the entry's own business: it can
	// only give the session back time, never take more.
	project[0].StartupTimeout = 5
	merged = MergeSessionServers(nil, nil, project, "", "key")
	if got := merged.Servers[0]; got.StartupTimeout != 5 {
		t.Fatalf("startup_timeout = %v, want a shorter wait to survive", got.StartupTimeout)
	}

	// An operator's own entry keeps both: the global list is not clamped.
	global := []appcfg.MCPServerConfig{{Name: "mine", Required: true, StartupTimeout: 600}}
	merged = MergeSessionServers(global, nil, nil, "", "key")
	if got := merged.Servers[0]; !got.Required || got.StartupTimeout != 600 {
		t.Fatalf("global entry = %+v, want its own startup policy untouched", got)
	}
}

// TestDisableStoreKeysEachServerByScopeAndProject pins the disable store's
// identity: a project's server, a same-named global one and another project's
// same-named server are toggled independently; a server the configuration
// requires is never left out; and a store that exists but cannot be read is
// reported rather than silently treated as empty — a toggle must never
// overwrite choices it could not read.
func TestDisableStoreKeysEachServerByScopeAndProject(t *testing.T) {
	root := t.TempDir()
	global := appcfg.MCPServerConfig{Name: "docs"}
	projA := appcfg.MCPServerConfig{Name: "docs", Scope: appcfg.MCPServerScopeProject, ProjectKey: "proj-a"}
	projB := appcfg.MCPServerConfig{Name: "Docs", Scope: appcfg.MCPServerScopeProject, ProjectKey: "proj-b"}
	required := appcfg.MCPServerConfig{Name: "must", Required: true}

	if err := SetServerDisabled(root, projA, true); err != nil {
		t.Fatal(err)
	}
	effective, disabled, err := FilterDisabledServers(root, []appcfg.MCPServerConfig{global, projA, projB, required})
	if err != nil {
		t.Fatal(err)
	}
	if len(disabled) != 1 || disabled[0].ProjectKey != "proj-a" || len(effective) != 3 {
		t.Fatalf("effective=%+v disabled=%+v, want only project A's docs left out", effective, disabled)
	}
	if err := SetServerDisabled(root, required, true); err == nil || !strings.Contains(err.Error(), "required by config") {
		t.Fatalf("disabling a required server: %v", err)
	}
	next, err := DisabledForNextSession(root)
	if err != nil || !next(projA) || next(projB) || next(global) {
		t.Fatalf("next-session marks wrong: %v", err)
	}
	if err := SetServerDisabled(root, projA, false); err != nil {
		t.Fatal(err)
	}
	if _, disabled, _ := FilterDisabledServers(root, []appcfg.MCPServerConfig{projA}); len(disabled) != 0 {
		t.Fatalf("enable left %+v disabled", disabled)
	}

	if err := os.WriteFile(disabledStorePath(root), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SetServerDisabled(root, global, true); err == nil {
		t.Fatal("a toggle over an unreadable store must fail, not overwrite it")
	}
	if raw, _ := os.ReadFile(disabledStorePath(root)); string(raw) != "{not json" {
		t.Fatalf("store was rewritten: %q", raw)
	}
}

func stdioEntry(name, command string) appcfg.MCPServerConfig {
	return appcfg.MCPServerConfig{Name: name, Transport: "stdio", Command: command}
}

func mergedNames(servers []appcfg.MCPServerConfig) []string {
	out := make([]string, 0, len(servers))
	for _, srv := range servers {
		out = append(out, srv.Name)
	}
	return out
}

// TestMergeSessionServersDifferentNamesAddUp pins the inheritance itself: an
// agent that configures a server of its own does not lose the shared ones, and a
// project adds its entries on top of both.
func TestMergeSessionServersDifferentNamesAddUp(t *testing.T) {
	shared := []appcfg.MCPServerConfig{stdioEntry("codegraph", "/bin/shared-cg")}
	agent := []appcfg.MCPServerConfig{stdioEntry("smart-doc", "/bin/agent-doc")}
	project := []appcfg.MCPServerConfig{stdioEntry("projtool", "/bin/pt")}

	res := MergeSessionServers(shared, agent, project, "alice", "pk")
	names := mergedNames(res.Servers)
	if len(names) != 3 || names[0] != "codegraph" || names[1] != "smart-doc" || names[2] != "projtool" {
		t.Fatalf("effective list = %v, want shared then agent then project", names)
	}
	if len(res.OverriddenGlobal) != 0 || len(res.OverriddenAgent) != 0 {
		t.Fatalf("nothing is overridden: %+v", res)
	}
	scopes := []string{res.Servers[0].Scope, res.Servers[1].Scope, res.Servers[2].Scope}
	want := []string{appcfg.MCPServerScopeGlobal, appcfg.MCPServerScopeAgent, appcfg.MCPServerScopeProject}
	for i := range want {
		if scopes[i] != want[i] {
			t.Fatalf("scope[%d] = %q, want %q", i, scopes[i], want[i])
		}
	}
	if res.Servers[1].AgentKey != "alice" {
		t.Fatalf("an agent entry carries its agent: %+v", res.Servers[1])
	}
	if res.Servers[2].ProjectKey != "pk" || res.Servers[2].AgentKey != "" {
		t.Fatalf("a project entry carries its project and no agent: %+v", res.Servers[2])
	}
}

// TestMergeSessionServersAgentShadowsTheSharedEntry pins the other half: a
// same-named entry is replaced whole by the higher layer, and the shared one is
// reported as overridden.
func TestMergeSessionServersAgentShadowsTheSharedEntry(t *testing.T) {
	shared := []appcfg.MCPServerConfig{stdioEntry("codegraph", "/bin/shared-cg")}
	agent := []appcfg.MCPServerConfig{{
		Name:      "codegraph",
		Transport: "stdio",
		Command:   "/bin/agent-cg",
		Env:       map[string]string{"FROM": "agent"},
	}}

	res := MergeSessionServers(shared, agent, nil, "alice", "")
	if len(res.Servers) != 1 {
		t.Fatalf("effective list = %+v", res.Servers)
	}
	got := res.Servers[0]
	if got.Command != "/bin/agent-cg" || got.Scope != appcfg.MCPServerScopeAgent {
		t.Fatalf("the agent's entry must win whole: %+v", got)
	}
	if len(res.OverriddenGlobal) != 1 || res.OverriddenGlobal[0] != "codegraph" {
		t.Fatalf("overridden shared entry = %v", res.OverriddenGlobal)
	}
	if len(res.OverriddenAgent) != 0 {
		t.Fatalf("nothing shadows the agent layer here: %v", res.OverriddenAgent)
	}
}

// TestMergeSessionServersProjectOutranksTheAgentLayer pins the top of the order:
// project > agent > shared, with each shadow recorded on the layer it replaced.
func TestMergeSessionServersProjectOutranksTheAgentLayer(t *testing.T) {
	shared := []appcfg.MCPServerConfig{stdioEntry("codegraph", "/bin/shared-cg")}
	agent := []appcfg.MCPServerConfig{stdioEntry("codegraph", "/bin/agent-cg")}
	project := []appcfg.MCPServerConfig{{
		Name:                     "codegraph",
		Transport:                "stdio",
		Command:                  "/bin/project-cg",
		DefaultToolsApprovalMode: appcfg.MCPToolApprovalAuto,
	}}

	res := MergeSessionServers(shared, agent, project, "alice", "pk")
	if len(res.Servers) != 1 {
		t.Fatalf("effective list = %+v", res.Servers)
	}
	got := res.Servers[0]
	if got.Command != "/bin/project-cg" || got.Scope != appcfg.MCPServerScopeProject {
		t.Fatalf("the project's entry must win whole: %+v", got)
	}
	if got.DefaultToolsApprovalMode != appcfg.MCPToolApprovalPrompt {
		t.Fatalf("a project entry is still clamped: %+v", got)
	}
	if len(res.OverriddenGlobal) != 1 || res.OverriddenGlobal[0] != "codegraph" {
		t.Fatalf("overridden shared entry = %v", res.OverriddenGlobal)
	}
	if len(res.OverriddenAgent) != 1 || res.OverriddenAgent[0] != "codegraph" {
		t.Fatalf("overridden agent entry = %v", res.OverriddenAgent)
	}
}

// TestMergeSessionServersClampsOnlyProjectEntries pins who may spend the
// session's patience: a repository cannot, the operator's own lists can.
func TestMergeSessionServersClampsOnlyProjectEntries(t *testing.T) {
	shared := []appcfg.MCPServerConfig{{
		Name:           "shared",
		Command:        "/bin/s",
		Required:       true,
		StartupTimeout: 600,
		RequestTimeout: 600,
	}}
	agent := []appcfg.MCPServerConfig{{
		Name:           "agent",
		Command:        "/bin/a",
		Required:       true,
		StartupTimeout: 600,
		RequestTimeout: 600,
	}}
	project := []appcfg.MCPServerConfig{{
		Name:           "project",
		Command:        "/bin/p",
		Required:       true,
		StartupTimeout: 600,
		RequestTimeout: 600,
	}}

	res := MergeSessionServers(shared, agent, project, "alice", "pk")
	byName := map[string]appcfg.MCPServerConfig{}
	for _, srv := range res.Servers {
		byName[srv.Name] = srv
	}
	for _, name := range []string{"shared", "agent"} {
		srv := byName[name]
		if !srv.Required || srv.StartupTimeout != 600 || srv.RequestTimeout != 600 {
			t.Fatalf("%s is the operator's own entry and keeps its policy: %+v", name, srv)
		}
	}
	clamped := byName["project"]
	if clamped.Required || clamped.StartupTimeout != 0 || clamped.RequestTimeout != 0 {
		t.Fatalf("a project entry may not widen its own reach: %+v", clamped)
	}
}

// TestDisabledKeySeparatesTheThreeScopes pins that a shared, an agent-owned and
// a project entry with one name are three independently toggled servers, while
// the historical shared key keeps its spelling so old stores still work.
func TestDisabledKeySeparatesTheThreeScopes(t *testing.T) {
	shared := appcfg.MCPServerConfig{Name: "Codegraph", Scope: appcfg.MCPServerScopeGlobal}
	agent := appcfg.MCPServerConfig{Name: "Codegraph", Scope: appcfg.MCPServerScopeAgent, AgentKey: "alice"}
	project := appcfg.MCPServerConfig{Name: "Codegraph", Scope: appcfg.MCPServerScopeProject, ProjectKey: "pk"}

	keys := map[string]string{
		"shared":  disabledKey(shared),
		"agent":   disabledKey(agent),
		"project": disabledKey(project),
	}
	if keys["shared"] != "global\x00codegraph" {
		t.Fatalf("shared key = %q, want the historical spelling", keys["shared"])
	}
	if keys["agent"] != "agent\x00codegraph" {
		t.Fatalf("agent key = %q", keys["agent"])
	}
	if keys["project"] != "project\x00codegraph\x00pk" {
		t.Fatalf("project key = %q", keys["project"])
	}
	if keys["shared"] == keys["agent"] || keys["agent"] == keys["project"] || keys["shared"] == keys["project"] {
		t.Fatalf("the three scopes must not share a key: %v", keys)
	}
}

// TestOverlayProjectKeyNamespacesAgentEntries pins credential storage: two
// primary agents that configure the same MCP server authenticate separately,
// and only shared entries use the common store.
func TestOverlayProjectKeyNamespacesAgentEntries(t *testing.T) {
	shared := appcfg.MCPServerConfig{Name: "docs", Scope: appcfg.MCPServerScopeGlobal}
	alice := appcfg.MCPServerConfig{Name: "docs", Scope: appcfg.MCPServerScopeAgent, AgentKey: "alice"}
	bob := appcfg.MCPServerConfig{Name: "docs", Scope: appcfg.MCPServerScopeAgent, AgentKey: "bob"}
	project := appcfg.MCPServerConfig{Name: "docs", Scope: appcfg.MCPServerScopeProject, ProjectKey: "pk"}

	if got := OverlayProjectKey(shared); got != "" {
		t.Fatalf("a shared entry uses the shared store, got %q", got)
	}
	if got := OverlayProjectKey(alice); got != "agent:alice" {
		t.Fatalf("alice's entry key = %q", got)
	}
	if OverlayProjectKey(alice) == OverlayProjectKey(bob) {
		t.Fatalf("two agents must not share a credential namespace")
	}
	if got := OverlayProjectKey(project); got != "pk" {
		t.Fatalf("a project entry keeps its project namespace, got %q", got)
	}
}

// TestFilterDisabledServersDropsEntriesTheConfigSwitchedOff pins the
// configuration's own switch: enabled: false keeps an entry out of a new session
// without touching the user's file, and it is reported alongside the store's own
// marks so a surface can say which is which.
func TestFilterDisabledServersDropsEntriesTheConfigSwitchedOff(t *testing.T) {
	ws := t.TempDir()
	off := false
	servers := []appcfg.MCPServerConfig{
		{Name: "on", Command: "/bin/on"},
		{Name: "off", Command: "/bin/off", Enabled: &off},
	}
	effective, disabled, err := FilterDisabledServers(ws, servers)
	if err != nil {
		t.Fatal(err)
	}
	if len(effective) != 1 || effective[0].Name != "on" {
		t.Fatalf("effective = %+v", mergedNames(effective))
	}
	if len(disabled) != 1 || disabled[0].Name != "off" {
		t.Fatalf("disabled = %+v", mergedNames(disabled))
	}
	if appcfg.ServerEnabled(disabled[0]) {
		t.Fatalf("the disabled entry must still say it is switched off: %+v", disabled[0])
	}

	// A store that cannot be read does not resurrect a server the file switched
	// off: the file was read, and it says no.
	if err := saveDisabledStore(ws, disabledStore{Disabled: map[string]bool{}}); err != nil {
		t.Fatal(err)
	}
	effective, disabled, err = FilterDisabledServers(ws, servers)
	if err != nil {
		t.Fatal(err)
	}
	if len(effective) != 1 || len(disabled) != 1 {
		t.Fatalf("effective=%v disabled=%v", mergedNames(effective), mergedNames(disabled))
	}
}

// TestFilterDisabledServersKeepsTheStoresChoice pins that the store's own mark
// still works, and that a required server is never filtered by it.
func TestFilterDisabledServersKeepsTheStoresChoice(t *testing.T) {
	ws := t.TempDir()
	off := false
	servers := []appcfg.MCPServerConfig{
		{Name: "paused", Command: "/bin/p"},
		{Name: "must", Command: "/bin/m", Required: true},
		{Name: "switched-off", Command: "/bin/s", Enabled: &off},
	}
	if err := SetServerDisabled(ws, servers[0], true); err != nil {
		t.Fatal(err)
	}
	if err := SetServerDisabled(ws, servers[1], true); err == nil {
		t.Fatal("a required server cannot be disabled")
	}
	effective, disabled, err := FilterDisabledServers(ws, servers)
	if err != nil {
		t.Fatal(err)
	}
	if len(effective) != 1 || effective[0].Name != "must" {
		t.Fatalf("effective = %v", mergedNames(effective))
	}
	if len(disabled) != 2 {
		t.Fatalf("disabled = %v", mergedNames(disabled))
	}
}

// TestSetServerDisabledRefusesToEnableAConfigDisabledEntry pins the one refusal
// that keeps the two switches from contradicting each other: the file is the
// authority on enabled: false, so a local "enable" has to be refused with the
// reason rather than recorded as a choice nothing honors.
func TestSetServerDisabledRefusesToEnableAConfigDisabledEntry(t *testing.T) {
	ws := t.TempDir()
	off := false
	srv := appcfg.MCPServerConfig{Name: "off", Command: "/bin/off", Enabled: &off}
	err := SetServerDisabled(ws, srv, false)
	if err == nil {
		t.Fatal("enabling an entry the configuration switched off must be refused")
	}
	if !strings.Contains(err.Error(), "enabled: false") {
		t.Fatalf("the refusal must name the file's own switch: %v", err)
	}
	// And the refused call must not have recorded anything.
	st, err := loadDisabledStore(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Disabled) != 0 {
		t.Fatalf("store = %+v", st.Disabled)
	}
}

// TestServerFingerprintIgnoresRequestTimeoutAndEnabled pins what a project
// consent is keyed by. Both new fields are the same kind of local dial
// startup_timeout is: turning a server off, or giving a call a bound, must not
// read as "this is a different server now, confirm it again".
func TestServerFingerprintIgnoresRequestTimeoutAndEnabled(t *testing.T) {
	off := false
	base := appcfg.MCPServerConfig{Name: "docs", Transport: "streamable_http", URL: "https://example.test/mcp"}
	fp := ServerFingerprint(base)

	tuned := base
	tuned.RequestTimeout = 30
	tuned.StartupTimeout = 10
	tuned.Enabled = &off
	if got := ServerFingerprint(tuned); got != fp {
		t.Fatalf("patience and the on/off switch are not identity: %s vs %s", got, fp)
	}

	changed := base
	changed.URL = "https://other.test/mcp"
	if got := ServerFingerprint(changed); got == fp {
		t.Fatal("a different endpoint must be a different server")
	}
}

func projectFile(t *testing.T, root string) string {
	t.Helper()
	paths := ProjectMCPPaths(root)
	if len(paths) == 0 {
		t.Fatal("no project MCP path")
	}
	return paths[0]
}

// TestSaveProjectMCPServersRoundTrips pins the write path: what is saved is what
// the reader loads back, sorted by name, with no runtime stamp leaking into the
// file.
func TestSaveProjectMCPServersRoundTrips(t *testing.T) {
	root := t.TempDir()
	off := false
	servers := []appcfg.MCPServerConfig{
		{Name: "zeta", Transport: "streamable_http", URL: "https://z.test/mcp"},
		{Name: "alpha", Transport: "stdio", Command: "/bin/a", Args: []string{"--root", "/tmp/a b"}, RequestTimeout: 30, Enabled: &off},
	}
	for i := range servers {
		servers[i].Scope = appcfg.MCPServerScopeProject
		servers[i].ProjectKey = "pk"
	}
	if err := SaveProjectMCPServers(root, servers); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(projectFile(t, root))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"scope", "project_key", "projectKey", "agent_key"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("runtime stamps must not be written (%s):\n%s", forbidden, text)
		}
	}
	if strings.Index(text, "alpha") > strings.Index(text, "zeta") {
		t.Fatalf("entries are sorted by name:\n%s", text)
	}

	loaded, notes := LoadProjectMCPServers(root)
	if len(notes) != 0 {
		t.Fatalf("notes = %+v", notes)
	}
	if len(loaded) != 2 || loaded[0].Name != "alpha" || loaded[1].Name != "zeta" {
		t.Fatalf("loaded = %+v", loaded)
	}
	if loaded[0].RequestTimeout != 30 || appcfg.ServerEnabled(loaded[0]) {
		t.Fatalf("the new fields must survive the round trip: %+v", loaded[0])
	}
	if len(loaded[0].Args) != 2 || loaded[0].Args[1] != "/tmp/a b" {
		t.Fatalf("args = %v", loaded[0].Args)
	}
}

// TestSaveProjectMCPServersRefusesAPlaintextSecret pins that the write path
// applies the rule the reader applies: a project file may reference a secret,
// never carry one. Writing one would produce a file the next load refuses to
// read, which is the one outcome a save must not cause.
func TestSaveProjectMCPServersRefusesAPlaintextSecret(t *testing.T) {
	root := t.TempDir()
	servers := []appcfg.MCPServerConfig{{
		Name:      "secretive",
		Transport: "streamable_http",
		URL:       "https://example.test/mcp",
		Headers:   map[string]string{"x-api-key": "sk-live-plaintext"},
	}}
	err := SaveProjectMCPServers(root, servers)
	if err == nil {
		t.Fatal("a plaintext secret-like header must be refused")
	}
	if !strings.Contains(err.Error(), "plaintext secret") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(projectFile(t, root)); !os.IsNotExist(statErr) {
		t.Fatalf("nothing may be written when the write is refused: %v", statErr)
	}
	// An ${ENV} reference is the allowed form.
	servers[0].Headers["x-api-key"] = "${MCP_API_KEY}"
	if err := SaveProjectMCPServers(root, servers); err != nil {
		t.Fatalf("a reference must be accepted: %v", err)
	}
}

// TestLoadProjectMCPServersForWriteIsStrict pins the difference between reading
// a file to run it and reading it to rewrite it: the reader drops what it cannot
// use and carries on, the writer refuses, because a write that proceeded would
// delete the entries it could not see.
func TestLoadProjectMCPServersForWriteIsStrict(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "plaintext secret in the file",
			body: "mcp_servers:\n  - name: bad\n    url: https://x.test/mcp\n    headers:\n      x-api-key: sk-live\n  - name: good\n    command: /bin/g\n",
			want: "plaintext secret",
		},
		{
			name: "an invalid approval mode",
			body: "mcp_servers:\n  - name: bad\n    command: /bin/g\n    default_tools_approval_mode: sometimes\n",
			want: "approval_mode",
		},
		{
			name: "an entry with no name",
			body: "mcp_servers:\n  - command: /bin/g\n    transport: stdio\n",
			want: "no name",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := projectFile(t, root)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProjectMCPServersForWrite(root); err == nil {
				t.Fatalf("want an error mentioning %q", tc.want)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			// The lenient reader still reports something usable for the same
			// file: the two are deliberately different questions.
			if _, notes := LoadProjectMCPServers(root); tc.name == "plaintext secret in the file" && len(notes) == 0 {
				t.Fatalf("the reader reports the file's own problem")
			}
		})
	}
}

// TestLoadProjectMCPServersForWriteReadsAWholeFile pins the opposite case: a
// file that is fine is returned entry for entry, and a file that does not exist
// yet is an empty list rather than an error — the first write creates it.
func TestLoadProjectMCPServersForWriteReadsAWholeFile(t *testing.T) {
	root := t.TempDir()
	servers, err := LoadProjectMCPServersForWrite(root)
	if err != nil {
		t.Fatalf("a missing file is an empty list: %v", err)
	}
	if len(servers) != 0 {
		t.Fatalf("servers = %+v", servers)
	}

	path := projectFile(t, root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "mcp_servers:\n  - name: a\n    command: /bin/a\n  - name: b\n    url: https://b.test/mcp\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	servers, err = LoadProjectMCPServersForWrite(root)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(servers) != 2 || servers[0].Name != "a" || servers[1].Name != "b" {
		t.Fatalf("servers = %+v", servers)
	}
}
