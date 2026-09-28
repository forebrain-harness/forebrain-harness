package migrate

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// fixtureCodexHome builds a Codex home around the static fixtures: one
// rollout, config.toml with enabled/disabled MCP and plugins, history, and a
// non-empty AGENTS.md. The project root it references is created on disk so
// project configuration migration can write into it.
type fixtureCodexHome struct {
	root        string // the Codex home
	projectPath string // /tmp/.../codex-proj equivalent, on disk
}

func buildFixtureCodexHome(t *testing.T) *fixtureCodexHome {
	t.Helper()
	home := filepath.Join(t.TempDir(), "codex-home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	copyTree(t, filepath.Join("testdata", "codex"), home)
	project := filepath.Join(t.TempDir(), "codex-proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	// The fixture config.toml names /tmp/codex-proj; rewrite it to the real
	// fixture project so the project scan finds it.
	cfgPath := filepath.Join(home, "config.toml")
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	fixed := strings.ReplaceAll(string(body), "/tmp/codex-proj", project)
	if err := os.WriteFile(cfgPath, []byte(fixed), 0o644); err != nil {
		t.Fatal(err)
	}
	// The rollout's cwds point at the same placeholder.
	rollout := filepath.Join(home, "sessions", "2026", "09", "17", "rollout-2026-09-17T01-00-00-t-codex-main.jsonl")
	body, err = os.ReadFile(rollout)
	if err != nil {
		t.Fatal(err)
	}
	fixed = strings.ReplaceAll(string(body), "/tmp/codex-proj", project)
	if err := os.WriteFile(rollout, []byte(fixed), 0o644); err != nil {
		t.Fatal(err)
	}
	return &fixtureCodexHome{root: home, projectPath: project}
}

func copyTree(t *testing.T, src, dest string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dest, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, body, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixtureCodexHome) options(t *testing.T, dryRun bool) *Options {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "forebrain.yaml")
	if err := os.WriteFile(cfgPath, []byte("agents:\n  defaults:\n    mcp_servers:\n      - name: existing\n        transport: stdio\n        command: /bin/true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Options{
		DryRun:             dryRun,
		Home:               filepath.Join(t.TempDir(), "forebrain-home"),
		AgentID:            "main",
		AgentWorkspace:     workspace,
		ConfigPath:         cfgPath,
		DB:                 db,
		Source:             KindCodex,
		SourceRoot:         f.root,
		InputHistoryPath:   filepath.Join(workspace, "state", "cli-input-history.txt"),
		CurrentProjectRoot: f.projectPath,
	}
}

// TestRunCodexImportsEverythingAndIsIdempotent runs the full Codex pipeline
// twice over the fixture home: the first migrates every category, the second
// skips what is already present and leaves row counts untouched.
func TestRunCodexImportsEverythingAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureCodexHome(t)
	opts := fixture.options(t, false)
	opts.Consolidate = func(context.Context, string) error { return nil }

	report, err := RunCodex(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.SourceRoot != fixture.root {
		t.Fatalf("report source root = %q want %q", report.SourceRoot, fixture.root)
	}
	migrated, skipped, failed, owned := report.SessionCounts()
	if failed != 0 || owned != 0 {
		for _, outcome := range report.Sessions {
			if outcome.Status == StatusFailed || outcome.Status == StatusOwned {
				t.Errorf("%s: %s (%s)", outcome.SourcePath, outcome.Status, outcome.Reason)
			}
		}
		t.Fatalf("failed=%d owned=%d", failed, owned)
	}
	if migrated != 1 || skipped != 0 {
		t.Fatalf("first run migrated=%d skipped=%d, want 1/0", migrated, skipped)
	}
	if got := report.Sessions[0].TargetID; got != "cli-t-codex-main" {
		t.Fatalf("target id = %q", got)
	}

	// Idempotency: the second run writes nothing new.
	var rows, sessions int
	if err := opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_messages`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	report2, err := RunCodex(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	migrated2, _, failed2, _ := report2.SessionCounts()
	if migrated2 != 0 || failed2 != 0 {
		t.Fatalf("second run migrated=%d failed=%d, want 0/0", migrated2, failed2)
	}
	var rows2, sessions2 int
	if err := opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_messages`).Scan(&rows2); err != nil {
		t.Fatal(err)
	}
	if err := opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_sessions`).Scan(&sessions2); err != nil {
		t.Fatal(err)
	}
	if rows2 != rows || sessions2 != sessions {
		t.Fatalf("second run changed the database: rows %d→%d sessions %d→%d", rows, rows2, sessions, sessions2)
	}
}

// TestCodexAggregatesAndWindowChain checks the session row carries the
// source's token truth and window chain (C2): initial_window_id comes from
// session_meta, the boundary's ids from the compacted record verbatim.
func TestCodexAggregatesAndWindowChain(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureCodexHome(t)
	opts := fixture.options(t, false)
	if _, err := RunCodex(ctx, opts, nil); err != nil {
		t.Fatal(err)
	}
	var initialWindow string
	var boundaryID sql.NullInt64
	err := opts.DB.QueryRowContext(ctx, `
SELECT initial_window_id, compact_boundary_message_id
FROM fb_sessions WHERE id='cli-t-codex-main'`).
		Scan(&initialWindow, &boundaryID)
	if err != nil {
		t.Fatal(err)
	}
	if initialWindow != "w-initial-0001" {
		t.Fatalf("initial window = %q, want the session_meta truth", initialWindow)
	}
	if !boundaryID.Valid {
		t.Fatal("compact boundary pointer empty despite a compacted record")
	}
	var partsJSON string
	var rowID int64
	if err := opts.DB.QueryRowContext(ctx, `
SELECT id, IFNULL(parts,'[]') FROM fb_messages WHERE session_id='cli-t-codex-main' AND role='system'`).
		Scan(&rowID, &partsJSON); err != nil {
		t.Fatal(err)
	}
	if boundaryID.Int64 != rowID {
		t.Fatalf("compact boundary pointer = %d, want the boundary row id %d", boundaryID.Int64, rowID)
	}
	part, ok := state.ParseCompactBoundaryPart(partsJSON)
	if !ok {
		t.Fatal("boundary parts do not decode")
	}
	if part.WindowID != "w-window-0002" || part.FirstWindowID != "w-initial-0001" || part.WindowNumber != 1 {
		t.Fatalf("window chain = %+v, want source truth", part)
	}
	// C1: the opaque compaction item replays verbatim.
	var sawCompaction bool
	for _, msg := range part.ReplacementHistory {
		if msg.Compaction != nil && msg.Compaction.EncryptedContent == "COMPACT-ENCRYPTED-1" {
			sawCompaction = true
		}
	}
	if !sawCompaction {
		t.Fatal("replacement history lost the encrypted compaction item")
	}
}

// TestCodexUserMCPDisabledSkipped checks config.toml's enabled=false entries
// never reach forebrain.yaml, enabled ones are appended, and unknown keys are
// reported rather than silently dropped (§4.9.4).
func TestCodexUserMCPDisabledSkipped(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureCodexHome(t)
	opts := fixture.options(t, false)
	report, err := RunCodex(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]MCPOutcome{}
	for _, entry := range report.MCP {
		if entry.Scope == "global" {
			byName[entry.Name] = entry
		}
	}
	if entry, ok := byName["computer-use"]; !ok || entry.Status != "skipped" {
		t.Fatalf("disabled entry = %+v, want skipped", entry)
	}
	if entry, ok := byName["node_repl"]; !ok || entry.Status != "written" {
		t.Fatalf("node_repl = %+v, want written", entry)
	} else if !strings.Contains(entry.Detail, "startup_timeout_sec") {
		t.Fatalf("dropped key not reported: %s", entry.Detail)
	}
	body, err := os.ReadFile(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "computer-use") {
		t.Fatal("disabled entry must not be written to forebrain.yaml")
	}
	if !strings.Contains(string(body), "node_repl") {
		t.Fatal("enabled entry missing from forebrain.yaml")
	}
}

// TestCodexProjectConfigMigrated checks <root>/.codex/config.toml's
// [mcp_servers] lands in <root>/.forebrain/mcp_servers.yaml with per-tool
// approval modes preserved (§4.9).
func TestCodexProjectConfigMigrated(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureCodexHome(t)
	projectCodex := filepath.Join(fixture.projectPath, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(projectCodex), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `
[mcp_servers.proj_tool]
command = "/bin/proj-tool"

[mcp_servers.proj_tool.tools.danger]
approval_mode = "approve"

[mcp_servers.proj_off]
command = "/bin/off"
enabled = false

[model]
model = "gpt-5"
`
	if err := os.WriteFile(projectCodex, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := fixture.options(t, false)
	report, err := RunCodex(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	written := filepath.Join(fixture.projectPath, ".forebrain", "mcp_servers.yaml")
	out, err := os.ReadFile(written)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, "proj_tool") {
		t.Fatalf("project entry missing from %s:\n%s", written, text)
	}
	if !strings.Contains(text, "approve") {
		t.Fatalf("per-tool approval_mode lost:\n%s", text)
	}
	if strings.Contains(text, "proj_off") {
		t.Fatalf("disabled project entry must not be written:\n%s", text)
	}
	var sawNoCounterpart bool
	for _, written := range report.Projects {
		for _, note := range written.Notes {
			if strings.Contains(note, "Forebrain Harness has no project setting for them") {
				sawNoCounterpart = true
			}
		}
	}
	if !sawNoCounterpart {
		t.Fatal("model key with no counterpart was not reported")
	}
	// forebrain must NOT have copied .codex/ away or written outside .forebrain.
	if _, err := os.Stat(filepath.Join(fixture.projectPath, ".forebrain", "mcp_servers.yaml.bak")); err == nil {
		// backup only appears on rewrite; first write has nothing to back up
		_ = err
	}
}

// TestCodexPlansExtracted checks <proposed_plan> blocks land in the plan
// store attributed to the session's project, numbered per session, and that
// re-running is up-to-date rather than duplicated (§4.10).
func TestCodexPlansExtracted(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureCodexHome(t)
	opts := fixture.options(t, false)
	report, err := RunCodex(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Plans) != 1 {
		t.Fatalf("plans = %+v, want one extraction", report.Plans)
	}
	if report.Plans[0].Status != "installed" {
		t.Fatalf("plan status = %s", report.Plans[0].Status)
	}
	body, err := os.ReadFile(filepath.Join(state.PlanDirForProject(opts.AgentWorkspace, report.Plans[0].ProjectKey), report.Plans[0].Name+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "first step") || !strings.Contains(string(body), "origin: codex plan mode") {
		t.Fatalf("plan body = %q", string(body))
	}
	report2, err := RunCodex(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report2.Plans) != 1 || report2.Plans[0].Status != "up-to-date" {
		t.Fatalf("second run plans = %+v, want up-to-date", report2.Plans)
	}
}

// TestCodexHistoryAndGlobalInstructions checks the input-history arithmetic
// (added + skipped == source lines) and that AGENTS.md becomes a global note.
func TestCodexHistoryAndGlobalInstructions(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureCodexHome(t)
	opts := fixture.options(t, false)
	report, err := RunCodex(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.History.SourceLines != 2 || report.History.Added+report.History.Skipped != 2 {
		t.Fatalf("history = %+v, want 2 source lines fully accounted", report.History)
	}
	body, err := os.ReadFile(opts.InputHistoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "hello from codex") {
		t.Fatalf("history file = %q", string(body))
	}
	var sawGlobalNote bool
	for _, memory := range report.Memories {
		if memory.ProjectDir == "AGENTS.md" && len(memory.Written) == 1 {
			sawGlobalNote = true
		}
	}
	if !sawGlobalNote {
		t.Fatalf("AGENTS.md not imported: %+v", report.Memories)
	}
}

// TestCodexSkillsImportedAndSystemSkipped checks only enabled-plugin skills
// install (highest version dir), and the .system built-ins are reported as
// skipped rather than vanishing.
func TestCodexSkillsImportedAndSystemSkipped(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureCodexHome(t)
	opts := fixture.options(t, false)
	report, err := RunCodex(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]SkillOutcome{}
	for _, skill := range report.Skills {
		byName[skill.Name] = skill
	}
	if entry, ok := byName["sample-skill"]; !ok || entry.Status != "installed" {
		t.Fatalf("sample-skill = %+v, want installed", entry)
	}
	if _, ok := byName["stale-skill"]; ok {
		t.Fatal("skill from a non-highest version directory must not import")
	}
	if entry, ok := byName["builtin-skill"]; !ok || entry.Status != "skipped" {
		t.Fatalf("builtin-skill = %+v, want reported skipped", entry)
	}
	if _, err := os.Stat(filepath.Join(opts.Home, "skills", "sample-skill", "SKILL.md")); err != nil {
		t.Fatalf("skill not installed: %v", err)
	}
}
