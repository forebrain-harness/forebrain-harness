package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/lsp"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

// trustedVCSProject builds a launch context for a real git repository under
// dir and records a trusted decision for it in the given home.
func trustedVCSProject(t *testing.T, home, dir string) safety.ProjectContext {
	t.Helper()
	git := filepath.Join(dir, ".git")
	if err := os.MkdirAll(git, 0o755); err != nil {
		t.Fatal(err)
	}
	project := safety.Project{Root: dir, VersionControlled: true}
	if err := safety.MarkTrusted(home, project); err != nil {
		t.Fatal(err)
	}
	return safety.ProjectContext{Project: project, TrustLevel: safety.LevelTrusted}
}

func writeProjectMCP(t *testing.T, dir, body string) {
	t.Helper()
	p := filepath.Join(dir, ".forebrain", "mcp_servers.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func globalList() []appcfg.MCPServerConfig {
	return []appcfg.MCPServerConfig{
		{Name: "codegraph", Transport: "stdio", Command: "/bin/cg"},
	}
}

// resolveMCPGlobal runs the resolution the way these tests were written: one
// shared list, no servers of any primary agent's own.
func resolveMCPGlobal(home, ws string, global []appcfg.MCPServerConfig, launch safety.ProjectContext) MCPResolution {
	agents := appcfg.AgentsSection{Defaults: appcfg.AgentDefaults{MCPServers: global}}
	return ResolveSessionMCP(home, ws, agents, "", launch)
}

func TestResolveSessionMCPTrustedConfirmedIncludesProject(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	dir := t.TempDir()
	launch := trustedVCSProject(t, home, dir)
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: projtool\n    transport: stdio\n    command: /bin/pt\n")
	// Confirm the entry.
	if err := DecideProjectMCPConsents(ws, launch, []string{"projtool"}); err != nil {
		t.Fatal(err)
	}
	res := resolveMCPGlobal(home, ws, globalList(), launch)
	names := serverNames(res.Servers)
	if len(names) != 2 || !containsFold(names, "codegraph") || !containsFold(names, "projtool") {
		t.Fatalf("servers=%v", names)
	}
	for _, srv := range res.Servers {
		if strings.EqualFold(srv.Name, "projtool") {
			if srv.Scope != appcfg.MCPServerScopeProject || srv.ProjectKey == "" {
				t.Fatalf("project entry not stamped: %+v", srv)
			}
		}
	}
	if res.ProjectRoot != dir {
		t.Fatalf("project root=%q", res.ProjectRoot)
	}
}

func TestResolveSessionMCPUnconfirmedFailsClosed(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	dir := t.TempDir()
	launch := trustedVCSProject(t, home, dir)
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: projtool\n    transport: stdio\n    command: /bin/pt\n")
	res := resolveMCPGlobal(home, ws, globalList(), launch)
	names := serverNames(res.Servers)
	if len(names) != 1 || !strings.EqualFold(names[0], "codegraph") {
		t.Fatalf("unconfirmed entry must not load: %v", names)
	}
	if len(res.Summary.NotApplied) != 1 || !strings.Contains(res.Summary.NotApplied[0].Reason, "awaiting confirmation") {
		t.Fatalf("notApplied=%+v", res.Summary.NotApplied)
	}
	if pending := PendingProjectMCPConsents(ws, launch); len(pending) != 1 || pending[0].Name != "projtool" {
		t.Fatalf("pending=%+v", pending)
	}
}

func TestResolveSessionMCPDeniedExcluded(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	dir := t.TempDir()
	launch := trustedVCSProject(t, home, dir)
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: projtool\n    transport: stdio\n    command: /bin/pt\n")
	// Empty allowed list records a denial for every pending entry.
	if err := DecideProjectMCPConsents(ws, launch, nil); err != nil {
		t.Fatal(err)
	}
	res := resolveMCPGlobal(home, ws, globalList(), launch)
	if names := serverNames(res.Servers); len(names) != 1 {
		t.Fatalf("denied entry must not load: %v", names)
	}
	if len(res.Summary.NotApplied) != 1 || !strings.Contains(res.Summary.NotApplied[0].Reason, "declined") {
		t.Fatalf("notApplied=%+v", res.Summary.NotApplied)
	}
}

// A per-entry decision from the web records the named entries alone: the
// entries it did not name keep whatever decision they had, and an entry with
// none stays pending instead of being read as declined.
func TestDecideProjectMCPConsentsForLeavesOthersUndecided(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	dir := t.TempDir()
	launch := trustedVCSProject(t, home, dir)
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: one\n    transport: stdio\n    command: /bin/one\n  - name: two\n    transport: stdio\n    command: /bin/two\n")
	if err := DecideProjectMCPConsentsFor(ws, launch, []string{"one"}, nil); err != nil {
		t.Fatal(err)
	}
	if pending := PendingProjectMCPConsents(ws, launch); len(pending) != 1 || pending[0].Name != "two" {
		t.Fatalf("pending = %+v, want only the entry the decision did not name", pending)
	}
	declined := DeclinedProjectMCPConsents(ws, launch)
	if len(declined) != 1 || declined[0].Name != "one" || declined[0].Summary == "" {
		t.Fatalf("declined = %+v", declined)
	}
}

// A named decision overwrites the one an entry already has, so a declined
// entry can be allowed from the project page without touching the file.
func TestDecideProjectMCPConsentsForAllowsADeclinedEntry(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	dir := t.TempDir()
	launch := trustedVCSProject(t, home, dir)
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: one\n    transport: stdio\n    command: /bin/one\n  - name: two\n    transport: stdio\n    command: /bin/two\n")
	// The startup prompt's answer: nothing allowed, so both entries are
	// recorded as declined.
	if err := DecideProjectMCPConsents(ws, launch, nil); err != nil {
		t.Fatal(err)
	}
	if declined := DeclinedProjectMCPConsents(ws, launch); len(declined) != 2 {
		t.Fatalf("declined = %+v", declined)
	}
	if err := DecideProjectMCPConsentsFor(ws, launch, []string{"one"}, []string{"one"}); err != nil {
		t.Fatal(err)
	}
	res := resolveMCPGlobal(home, ws, globalList(), launch)
	if names := serverNames(res.Servers); !containsFold(names, "one") {
		t.Fatalf("the allowed entry must load: %v", names)
	}
	if declined := DeclinedProjectMCPConsents(ws, launch); len(declined) != 1 || declined[0].Name != "two" {
		t.Fatalf("declined = %+v", declined)
	}
}

// Without trust the project's file is not even read, so there is nothing to
// report as declined.
func TestDeclinedProjectMCPConsentsNeedTrust(t *testing.T) {
	ws := t.TempDir()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: projtool\n    transport: stdio\n    command: /bin/pt\n")
	launch := safety.ProjectContext{Project: safety.Project{Root: dir, VersionControlled: true}}
	if declined := DeclinedProjectMCPConsents(ws, launch); len(declined) != 0 {
		t.Fatalf("untrusted launch must have no declined entries: %+v", declined)
	}
	if err := DecideProjectMCPConsentsFor(ws, launch, []string{"projtool"}, nil); err != nil {
		t.Fatalf("decide on an untrusted launch: %v", err)
	}
	if _, err := os.Stat(mcp.ProjectConsentPath(ws)); !os.IsNotExist(err) {
		t.Fatalf("an untrusted decide must not write a consent store: %v", err)
	}
}

func TestResolveSessionMCPUntrustedProjectExcludesEntries(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	launch := safety.ProjectContext{Project: safety.Project{Root: dir, VersionControlled: true}}
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: projtool\n    transport: stdio\n    command: /bin/pt\n")
	res := resolveMCPGlobal(home, ws, globalList(), launch)
	if names := serverNames(res.Servers); len(names) != 1 {
		t.Fatalf("untrusted project entries must not load: %v", names)
	}
	if len(res.Summary.NotApplied) != 1 || !strings.Contains(res.Summary.NotApplied[0].Reason, "not trusted") {
		t.Fatalf("notApplied=%+v", res.Summary.NotApplied)
	}
}

// A trusted project needs no .git for its MCP entries to load (R6): version
// control is no longer part of the gate — only trust and per-entry consent are.
// The old expectation (a non-VCS project's entries never load) was wrong: it
// hid a real project configuration behind a VCS check the project model dropped.
func TestResolveSessionMCPTrustedNonVCSProjectLoadsEntries(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	dir := t.TempDir() // deliberately NOT version controlled
	launch := safety.ProjectContext{Project: safety.Project{Root: dir}, TrustLevel: safety.LevelTrusted}
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: projtool\n    transport: stdio\n    command: /bin/pt\n")

	// Unconfirmed: the entry is not loaded yet, and the reason is consent,
	// never a missing .git.
	pending := resolveMCPGlobal(home, ws, globalList(), launch)
	if names := serverNames(pending.Servers); len(names) != 1 {
		t.Fatalf("unconfirmed project entry must not load yet: %v", names)
	}
	if len(pending.Summary.NotApplied) != 1 || !strings.Contains(pending.Summary.NotApplied[0].Reason, "awaiting confirmation") {
		t.Fatalf("notApplied=%+v", pending.Summary.NotApplied)
	}

	if err := DecideProjectMCPConsents(ws, launch, []string{"projtool"}); err != nil {
		t.Fatal(err)
	}
	res := resolveMCPGlobal(home, ws, globalList(), launch)
	if names := serverNames(res.Servers); len(names) != 2 {
		t.Fatalf("a trusted non-VCS project must load its confirmed entries: %v", names)
	}
}

func TestResolveSessionMCPSameNameOverridesGlobal(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	dir := t.TempDir()
	launch := trustedVCSProject(t, home, dir)
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: CODEGRAPH\n    transport: stdio\n    command: /bin/project-cg\n")
	if err := DecideProjectMCPConsents(ws, launch, []string{"CODEGRAPH"}); err != nil {
		t.Fatal(err)
	}
	res := resolveMCPGlobal(home, ws, globalList(), launch)
	if len(res.Servers) != 1 {
		t.Fatalf("override must replace, not append: %+v", res.Servers)
	}
	if res.Servers[0].Command != "/bin/project-cg" {
		t.Fatalf("project entry should win: %+v", res.Servers[0])
	}
	if len(res.Summary.OverriddenGlobal) != 1 || !strings.EqualFold(res.Summary.OverriddenGlobal[0], "codegraph") {
		t.Fatalf("overridden=%v", res.Summary.OverriddenGlobal)
	}
}

func TestInspectProjectMCPReportsDrift(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	dir := t.TempDir()
	launch := trustedVCSProject(t, home, dir)
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: projtool\n    transport: stdio\n    command: /bin/pt\n")
	if err := DecideProjectMCPConsents(ws, launch, []string{"projtool"}); err != nil {
		t.Fatal(err)
	}
	frozen := resolveMCPGlobal(home, ws, globalList(), launch)
	summary := InspectProjectMCP(ws, frozen.Servers, launch)
	if summary.PendingReload {
		t.Fatalf("no drift yet: %+v", summary)
	}
	// Change the file on disk: same name set, but the frozen list must not
	// have moved; the inspection reports pending.
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: projtool\n    transport: stdio\n    command: /bin/pt2\n  - name: extra\n    transport: stdio\n    command: /bin/e\n")
	summary = InspectProjectMCP(ws, frozen.Servers, launch)
	if !summary.PendingReload {
		t.Fatalf("disk drift must read as pending: %+v", summary)
	}
	var extraFound bool
	for _, item := range summary.NotApplied {
		if strings.EqualFold(item.Name, "extra") && strings.Contains(item.Reason, "awaiting confirmation") {
			extraFound = true
		}
	}
	if !extraFound {
		t.Fatalf("unconfirmed new entry should be listed: %+v", summary.NotApplied)
	}
}

func TestResolveSessionMCPNoProjectFilesMatchesGlobal(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	dir := t.TempDir()
	launch := trustedVCSProject(t, home, dir)
	global := globalList()
	res := resolveMCPGlobal(home, ws, global, launch)
	if len(res.Servers) != len(global) {
		t.Fatalf("servers=%+v", res.Servers)
	}
	if res.Servers[0].Name != global[0].Name || res.Servers[0].Command != global[0].Command {
		t.Fatalf("entry differs beyond stamps: %+v vs %+v", res.Servers[0], global[0])
	}
	if res.Servers[0].Scope != appcfg.MCPServerScopeGlobal {
		t.Fatalf("expected global stamp")
	}
}

func TestResolveSessionMCPSecretRejectionNote(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	dir := t.TempDir()
	launch := trustedVCSProject(t, home, dir)
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: leaky\n    transport: stdio\n    command: /bin/x\n    env:\n      API_KEY: abc\n")
	res := resolveMCPGlobal(home, ws, globalList(), launch)
	if names := serverNames(res.Servers); len(names) != 1 {
		t.Fatalf("secret-bearing file must not load: %v", names)
	}
	found := false
	for _, item := range res.Summary.NotApplied {
		if strings.Contains(item.Reason, "plaintext secret") {
			found = true
		}
	}
	if !found {
		t.Fatalf("notApplied=%+v", res.Summary.NotApplied)
	}
}

func TestConsentsArePerAgentWorkspace(t *testing.T) {
	home := t.TempDir()
	wsA, wsB := t.TempDir(), t.TempDir()
	dir := t.TempDir()
	launch := trustedVCSProject(t, home, dir)
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: projtool\n    transport: stdio\n    command: /bin/pt\n")
	if err := DecideProjectMCPConsents(wsA, launch, []string{"projtool"}); err != nil {
		t.Fatal(err)
	}
	resA := resolveMCPGlobal(home, wsA, globalList(), launch)
	if names := serverNames(resA.Servers); len(names) != 2 {
		t.Fatalf("workspace A confirmed the entry: %v", names)
	}
	resB := resolveMCPGlobal(home, wsB, globalList(), launch)
	if names := serverNames(resB.Servers); len(names) != 1 {
		t.Fatalf("workspace B must fail closed without its own consent: %v", names)
	}
}

func TestFingerprintChangeReasks(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	dir := t.TempDir()
	launch := trustedVCSProject(t, home, dir)
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: projtool\n    transport: stdio\n    command: /bin/pt\n")
	if err := DecideProjectMCPConsents(ws, launch, []string{"projtool"}); err != nil {
		t.Fatal(err)
	}
	// Change the command: the recorded fingerprint no longer applies.
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: projtool\n    transport: stdio\n    command: /bin/pt-v2\n")
	res := resolveMCPGlobal(home, ws, globalList(), launch)
	if names := serverNames(res.Servers); len(names) != 1 {
		t.Fatalf("changed fingerprint must re-ask: %v", names)
	}
}

func serverNames(servers []appcfg.MCPServerConfig) []string {
	var out []string
	for _, srv := range servers {
		out = append(out, strings.TrimSpace(srv.Name))
	}
	return out
}

func containsFold(names []string, want string) bool {
	for _, n := range names {
		if strings.EqualFold(n, want) {
			return true
		}
	}
	return false
}

// TestResolveSessionMCPLeavesDisabledServersOutOfANewSession pins the whole
// Disable round trip: the choice recorded in the agent's own workspace store is
// what the next session's resolution reads — whatever FOREBRAIN_HOME says — the
// server is left out of the effective list (so it neither starts nor registers
// tools) and carried separately for /mcp, and a disable toggle is not
// mistaken for on-disk drift.
func TestResolveSessionMCPLeavesDisabledServersOutOfANewSession(t *testing.T) {
	t.Setenv("FOREBRAIN_HOME", "")
	home := t.TempDir()
	ws := t.TempDir()
	dir := t.TempDir()
	launch := trustedVCSProject(t, home, dir)
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: projtool\n    transport: stdio\n    command: /bin/pt\n")
	if err := DecideProjectMCPConsents(ws, launch, []string{"projtool"}); err != nil {
		t.Fatal(err)
	}
	before := resolveMCPGlobal(home, ws, globalList(), launch)
	var projtool appcfg.MCPServerConfig
	for _, srv := range before.Servers {
		if strings.EqualFold(srv.Name, "projtool") {
			projtool = srv
		}
	}
	if err := mcp.SetServerDisabled(ws, projtool, true); err != nil {
		t.Fatal(err)
	}
	res := resolveMCPGlobal(home, ws, globalList(), launch)
	if names := serverNames(res.Servers); len(names) != 1 || !containsFold(names, "codegraph") {
		t.Fatalf("servers=%v, want projtool left out", names)
	}
	if len(res.Disabled) != 1 || !strings.EqualFold(res.Disabled[0].Name, "projtool") {
		t.Fatalf("disabled=%+v, want projtool carried for /mcp", res.Disabled)
	}
	all := append(append([]appcfg.MCPServerConfig(nil), before.Servers...), before.Disabled...)
	if summary := InspectProjectMCP(ws, all, launch); summary.PendingReload {
		t.Fatal("a disable toggle must not read as on-disk drift")
	}
}

// An untrusted launch never reads the project's lsp_servers.yaml: nothing is
// pending, and a decide is a no-op rather than a recorded decision.
func TestProjectLSPConsentsNeedTrust(t *testing.T) {
	ws := t.TempDir()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, ".forebrain", "lsp_servers.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("servers:\n  s:\n    command: /bin/s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	launch := safety.ProjectContext{Project: safety.Project{Root: dir, VersionControlled: true}}

	if pending := PendingProjectLSPConsents(ws, launch); len(pending) != 0 {
		t.Fatalf("untrusted launch must have no pending entries: %+v", pending)
	}
	if trusted, pending, _, _, _ := InspectProjectLSP(ws, launch); trusted || len(pending) != 0 {
		t.Fatalf("untrusted inspect = (%v, %+v)", trusted, pending)
	}
	if err := DecideProjectLSPConsents(ws, launch, []string{"s"}); err != nil {
		t.Fatalf("decide on an untrusted launch: %v", err)
	}
	if _, err := os.Stat(lsp.ProjectConsentPath(ws)); !os.IsNotExist(err) {
		t.Fatalf("an untrusted decide must not write a consent store: %v", err)
	}

	// Trusted, the same file has exactly the one entry pending.
	launch.TrustLevel = safety.LevelTrusted
	if pending := PendingProjectLSPConsents(ws, launch); len(pending) != 1 || pending[0].ID != "s" {
		t.Fatalf("pending = %+v", pending)
	}
}

// The language-server twin: a named allow overwrites the denial the startup
// prompt recorded, so the entry applies again.
func TestDecideProjectLSPConsentsForAllowsADeniedEntry(t *testing.T) {
	ws := t.TempDir()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, ".forebrain", "lsp_servers.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("servers:\n  one: {command: /bin/one}\n  two: {command: /bin/two}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	launch := safety.ProjectContext{Project: safety.Project{Root: dir, VersionControlled: true}, TrustLevel: safety.LevelTrusted}
	if err := DecideProjectLSPConsents(ws, launch, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, allowed, denied, _ := InspectProjectLSP(ws, launch); len(allowed) != 0 || len(denied) != 2 {
		t.Fatalf("allowed = %v denied = %v", allowed, denied)
	}
	if err := DecideProjectLSPConsentsFor(ws, launch, []string{"one"}, []string{"one"}); err != nil {
		t.Fatal(err)
	}
	_, pending, allowed, denied, _ := InspectProjectLSP(ws, launch)
	if len(allowed) != 1 || allowed[0] != "one" {
		t.Fatalf("allowed = %v", allowed)
	}
	if len(denied) != 1 || denied[0] != "two" || len(pending) != 0 {
		t.Fatalf("denied = %v pending = %+v", denied, pending)
	}
}

// TestResolveSessionMCPLayersTheAgentsOwnList pins the three-layer resolution end
// to end: an agent's own entry shadows a same-named shared one, a different name
// adds to it, another agent still sees the shared list, and the stamps say which
// layer each effective entry came from.
func TestResolveSessionMCPLayersTheAgentsOwnList(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	launch := trustedVCSProject(t, home, t.TempDir())
	agents := appcfg.AgentsSection{
		Defaults: appcfg.AgentDefaults{MCPServers: []appcfg.MCPServerConfig{
			{Name: "codegraph", Transport: "stdio", Command: "/bin/shared-cg"},
			{Name: "shared-only", Transport: "stdio", Command: "/bin/so"},
		}},
		Definitions: map[string]appcfg.AgentDefinition{
			"alice": {
				Primary: true,
				MCPServers: []appcfg.MCPServerConfig{
					{Name: "codegraph", Transport: "stdio", Command: "/bin/alice-cg", Required: true},
				},
			},
		},
	}

	res := ResolveSessionMCP(home, ws, agents, "alice", launch)
	names := serverNames(res.Servers)
	if len(names) != 2 || !containsFold(names, "codegraph") || !containsFold(names, "shared-only") {
		t.Fatalf("alice's effective list = %v", names)
	}
	for _, srv := range res.Servers {
		if !mcp.SameServerName(srv.Name, "codegraph") {
			continue
		}
		if srv.Command != "/bin/alice-cg" || srv.Scope != appcfg.MCPServerScopeAgent || srv.AgentKey != "alice" {
			t.Fatalf("the agent's entry must win and be stamped: %+v", srv)
		}
		// A primary agent's own list is the operator's, so it is not clamped.
		if !srv.Required {
			t.Fatalf("an agent's own entry keeps its required flag: %+v", srv)
		}
	}
	if len(res.Summary.OverriddenGlobal) != 1 || res.Summary.OverriddenGlobal[0] != "codegraph" {
		t.Fatalf("overridden = %v", res.Summary.OverriddenGlobal)
	}

	// Another primary agent owns nothing of its own and inherits the shared
	// entry unchanged.
	other := ResolveSessionMCP(home, ws, agents, "bob", launch)
	for _, srv := range other.Servers {
		if mcp.SameServerName(srv.Name, "codegraph") {
			if srv.Command != "/bin/shared-cg" || srv.Scope != appcfg.MCPServerScopeGlobal {
				t.Fatalf("bob's codegraph = %+v", srv)
			}
		}
	}
	if len(other.Summary.OverriddenGlobal) != 0 {
		t.Fatalf("nothing is overridden for bob: %v", other.Summary.OverriddenGlobal)
	}
}

// TestResolveSessionMCPHonoursTheConfigSwitchAndTheAgentLayerTogether pins that
// an entry the agent's own list switched off is kept out of the session, is
// reported as the thing that will not start, and still carries its scope so a
// surface can point at the right file.
func TestResolveSessionMCPHonoursTheConfigSwitchAndTheAgentLayerTogether(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	launch := trustedVCSProject(t, home, t.TempDir())
	off := false
	agents := appcfg.AgentsSection{
		Definitions: map[string]appcfg.AgentDefinition{
			"alice": {
				Primary: true,
				MCPServers: []appcfg.MCPServerConfig{
					{Name: "paused", Transport: "stdio", Command: "/bin/p", Enabled: &off},
					{Name: "live", Transport: "stdio", Command: "/bin/l"},
				},
			},
		},
	}
	res := ResolveSessionMCP(home, ws, agents, "alice", launch)
	if got := serverNames(res.Servers); len(got) != 1 || !containsFold(got, "live") {
		t.Fatalf("effective = %v", got)
	}
	if len(res.Disabled) != 1 || res.Disabled[0].Name != "paused" {
		t.Fatalf("disabled = %v", serverNames(res.Disabled))
	}
	if res.Disabled[0].Scope != appcfg.MCPServerScopeAgent || appcfg.ServerEnabled(res.Disabled[0]) {
		t.Fatalf("the disabled entry keeps its layer and its switch: %+v", res.Disabled[0])
	}
}
