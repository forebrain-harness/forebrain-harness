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
	res := MergeSessionServers(global, project, "-users-x-proj")
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
	res := MergeSessionServers(global, nil, "")
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
	res := MergeSessionServers(global, project, "pk")
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
		merged := MergeSessionServers(global, project, "-proj")
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
	fp := ServerFingerprint(MergeSessionServers(global, project, "-proj").Servers[0])
	for i := 0; i < 3; i++ {
		if got := ServerFingerprint(MergeSessionServers(global, project, "-proj").Servers[0]); got != fp {
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
	merged := MergeSessionServers(nil, project, "key")
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
	merged = MergeSessionServers(nil, project, "key")
	if got := merged.Servers[0]; got.StartupTimeout != 5 {
		t.Fatalf("startup_timeout = %v, want a shorter wait to survive", got.StartupTimeout)
	}

	// An operator's own entry keeps both: the global list is not clamped.
	global := []appcfg.MCPServerConfig{{Name: "mine", Required: true, StartupTimeout: 600}}
	merged = MergeSessionServers(global, nil, "key")
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
