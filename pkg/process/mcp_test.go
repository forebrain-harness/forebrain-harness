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
	res := ResolveSessionMCP(home, ws, globalList(), launch)
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
	res := ResolveSessionMCP(home, ws, globalList(), launch)
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
	res := ResolveSessionMCP(home, ws, globalList(), launch)
	if names := serverNames(res.Servers); len(names) != 1 {
		t.Fatalf("denied entry must not load: %v", names)
	}
	if len(res.Summary.NotApplied) != 1 || !strings.Contains(res.Summary.NotApplied[0].Reason, "declined") {
		t.Fatalf("notApplied=%+v", res.Summary.NotApplied)
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
	res := ResolveSessionMCP(home, ws, globalList(), launch)
	if names := serverNames(res.Servers); len(names) != 1 {
		t.Fatalf("untrusted project entries must not load: %v", names)
	}
	if len(res.Summary.NotApplied) != 1 || !strings.Contains(res.Summary.NotApplied[0].Reason, "not trusted") {
		t.Fatalf("notApplied=%+v", res.Summary.NotApplied)
	}
}

func TestResolveSessionMCPNonVCSProjectExcludesEntries(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	dir := t.TempDir()
	launch := safety.ProjectContext{Project: safety.Project{Root: dir}}
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: projtool\n    transport: stdio\n    command: /bin/pt\n")
	res := ResolveSessionMCP(home, ws, globalList(), launch)
	if names := serverNames(res.Servers); len(names) != 1 {
		t.Fatalf("non-VCS project entries must not load: %v", names)
	}
	if len(res.Summary.NotApplied) != 1 || !strings.Contains(res.Summary.NotApplied[0].Reason, "version controlled") {
		t.Fatalf("notApplied=%+v", res.Summary.NotApplied)
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
	res := ResolveSessionMCP(home, ws, globalList(), launch)
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
	frozen := ResolveSessionMCP(home, ws, globalList(), launch)
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
	res := ResolveSessionMCP(home, ws, global, launch)
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
	res := ResolveSessionMCP(home, ws, globalList(), launch)
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
	resA := ResolveSessionMCP(home, wsA, globalList(), launch)
	if names := serverNames(resA.Servers); len(names) != 2 {
		t.Fatalf("workspace A confirmed the entry: %v", names)
	}
	resB := ResolveSessionMCP(home, wsB, globalList(), launch)
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
	res := ResolveSessionMCP(home, ws, globalList(), launch)
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
	before := ResolveSessionMCP(home, ws, globalList(), launch)
	var projtool appcfg.MCPServerConfig
	for _, srv := range before.Servers {
		if strings.EqualFold(srv.Name, "projtool") {
			projtool = srv
		}
	}
	if err := mcp.SetServerDisabled(ws, projtool, true); err != nil {
		t.Fatal(err)
	}
	res := ResolveSessionMCP(home, ws, globalList(), launch)
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
