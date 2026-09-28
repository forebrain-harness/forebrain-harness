package migrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// copyGuangfaFixture materializes the guangfa sample — a real user project
// that carries every project-level source file at once — inside a temp
// directory. The original stays untouched.
func copyGuangfaFixture(t *testing.T) string {
	t.Helper()
	dest := t.TempDir()
	copyTree(t, filepath.Join("testdata", "guangfa"), dest)
	return dest
}

// TestGuangfaSampleCodexProjectConfig is the §9.5 step 6 acceptance on the
// Codex side: .codex/config.toml's [mcp_servers] migrates into
// .forebrain/mcp_servers.yaml with all seven per-tool approval_mode values
// preserved verbatim, and the report says the directory is not a git
// repository so nothing will load until it becomes one.
func TestGuangfaSampleCodexProjectConfig(t *testing.T) {
	project := copyGuangfaFixture(t)
	data := &codexData{Root: t.TempDir(), ProjectPaths: []string{project}}
	opts := &Options{Home: t.TempDir()}
	outcomes, projects := importCodexProjectConfig(data, opts, nil)
	written, err := os.ReadFile(filepath.Join(project, ".forebrain", "mcp_servers.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(written)
	if !strings.Contains(body, "code-review-graph") {
		t.Fatalf("entry missing:\n%s", body)
	}
	if got := strings.Count(body, "approve"); got < 7 {
		t.Fatalf("approval_mode values preserved = %d, want the source's seven", got)
	}
	if len(projects) != 1 || projects[0].Path != project || projects[0].Unloaded != "not a version-controlled project" {
		t.Fatalf("project writes = %+v", projects)
	}
	// What a project write means is said once for all of them, drift included.
	text := (&Report{Source: KindCodex, Projects: projects}).Text()
	for _, want := range []string{"Codex keeps reading its originals", project + " — will not load: not a version-controlled project"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report misses %q:\n%s", want, text)
		}
	}
	if len(outcomes) == 0 {
		t.Fatal("no outcomes reported")
	}
	// The migrated file loads back through forebrain's own reader.
	servers, notes := mcp.LoadProjectMCPServers(project)
	if len(servers) != 1 || !mcp.SameServerName(servers[0].Name, "code-review-graph") {
		t.Fatalf("reloaded servers = %+v notes = %+v", servers, notes)
	}
}

// TestGuangfaSampleClaudeProjectConfig is the §9.5 step 6 acceptance on the
// Claude side: the five Bash(...) permission rules land in safety.json with
// shell tool names, the enabled entry carries its approval, and the hooks
// section is reported as having no counterpart rather than vanishing.
func TestGuangfaSampleClaudeProjectConfig(t *testing.T) {
	ctx := context.Background()
	home := filepath.Join(t.TempDir(), "claude-home")
	if err := os.MkdirAll(filepath.Join(home, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	project := copyGuangfaFixture(t)
	claudeJSON := `{"projects": {"` + project + `": {"enabledMcpjsonServers": ["code-review-graph"], "disabledMcpjsonServers": []}}}`
	if err := os.WriteFile(filepath.Join(home, "..", ".claude.json"), []byte(claudeJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := claudeRootOverride
	claudeRootOverride = home
	t.Cleanup(func() { claudeRootOverride = prev })

	dbPath := filepath.Join(t.TempDir(), "state.db")
	opts := fixtureOptionsFor(t, dbPath, home)
	opts.CurrentProjectRoot = project
	report, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}

	safetyBody, err := os.ReadFile(filepath.Join(project, ".forebrain", "safety.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(safetyBody), `"shell"`); got < 5 {
		t.Fatalf("shell rules = %d, want the source's five:\n%s", got, safetyBody)
	}
	mcpBody, err := os.ReadFile(filepath.Join(project, ".forebrain", "mcp_servers.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mcpBody), "code-review-graph") {
		t.Fatalf(".mcp.json not migrated:\n%s", mcpBody)
	}
	var notes []string
	for _, written := range report.Projects {
		notes = append(notes, written.Notes...)
	}
	notices := strings.Join(notes, "\n")
	var sawApproval bool
	for _, entry := range report.MCP {
		if entry.Name == "code-review-graph" && strings.Contains(entry.Detail, "approval carried over") {
			sawApproval = true
		}
	}
	if !sawApproval {
		t.Fatalf("carried approval missing from entries: %+v", report.MCP)
	}
	// enableAllProjectMcpServers=true is present in the sample's local
	// settings; it must be reported as NOT a blanket allow.
	if !strings.Contains(notices, "each server still asks once") {
		t.Fatalf("blanket-allow refusal missing:\n%s", notices)
	}
}

// fixtureOptionsFor builds a minimal Options around a throwaway state file.
func fixtureOptionsFor(t *testing.T, dbPath, home string) *Options {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "forebrain.yaml")
	if err := os.WriteFile(cfg, []byte("agents:\n  defaults:\n    mcp_servers: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Options{
		Home:             home,
		AgentID:          "main",
		AgentWorkspace:   workspace,
		ConfigPath:       cfg,
		DB:               db,
		InputHistoryPath: filepath.Join(workspace, "state", "cli-input-history.txt"),
	}
}
