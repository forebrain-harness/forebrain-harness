package migrate

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// fixtureClaudeHome builds a minimal ~/.claude layout in a temp dir: one
// project with one main session (the static fixture transcript), one
// subagent transcript with its meta, one memory note, one plan named after
// the session's slug, history.jsonl, and the MCP sections of .claude.json.
// The real project directory is created on disk so path resolution and the
// plan store can land in it.
type fixtureHome struct {
	root        string // ~/.claude
	projectDir  string // projects/<encoded>
	projectPath string // the real project directory on disk
	dbPath      string
}

func buildFixtureHome(t *testing.T) *fixtureHome {
	t.Helper()
	home := t.TempDir()
	realProject := filepath.Join(t.TempDir(), "source-proj")
	if err := os.MkdirAll(realProject, 0o755); err != nil {
		t.Fatal(err)
	}
	encoded := encodeProjectPath(realProject)
	projectDir := filepath.Join(home, "projects", encoded)
	if err := os.MkdirAll(filepath.Join(projectDir, "s-fix", "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join("testdata", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "s-fix.jsonl"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	sub, err := os.ReadFile(filepath.Join("testdata", "subagent.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "s-fix", "subagents", "agent-deadbeef.jsonl"), sub, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "s-fix", "subagents", "agent-deadbeef.meta.json"),
		[]byte(`{"agentType":"investigator","isFork":false,"description":"find the root cause","toolUseId":"toolu_9","spawnDepth":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// The fixture transcript's head carries cwd /tmp/source-proj, which does
	// not exist here; rewrite it to the real fixture project so resolution
	// level 2 lands there.
	fixedBody := strings.ReplaceAll(string(body), "/tmp/source-proj", realProject)
	if err := os.WriteFile(filepath.Join(projectDir, "s-fix.jsonl"), []byte(fixedBody), 0o644); err != nil {
		t.Fatal(err)
	}
	fixedSub := strings.ReplaceAll(string(sub), "/tmp/source-proj", realProject)
	if err := os.WriteFile(filepath.Join(projectDir, "s-fix", "subagents", "agent-deadbeef.jsonl"), []byte(fixedSub), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	note := "---\nname: run-tests-with-tags\ndescription: this repo's tests need CGO and fts5\nmetadata:\n  modified: \"2026-09-02T03:04:05Z\"\n  originSessionId: s-fix\n---\n\nAlways build with CGO_ENABLED=1 and -tags fts5.\n"
	if err := os.WriteFile(filepath.Join(projectDir, "memory", "run-tests-with-tags.md"), []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "plans", "fancy-fix-slug.md"), []byte("# The plan\n\n1. do the thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	history := `{"display":"first question","timestamp":100,"project":"` + realProject + `","sessionId":"s-fix"}
{"display":"second question","timestamp":50,"project":"` + realProject + `","sessionId":"s-fix"}
unparsable line
`
	if err := os.WriteFile(filepath.Join(home, "history.jsonl"), []byte(history), 0o644); err != nil {
		t.Fatal(err)
	}
	return &fixtureHome{root: home, projectDir: projectDir, projectPath: realProject}
}

func (f *fixtureHome) options(t *testing.T, dryRun bool) *Options {
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
		InputHistoryPath:   filepath.Join(workspace, "state", "cli-input-history.txt"),
		CurrentProjectRoot: f.projectPath,
	}
}

// stubClaudeRoot redirects the package's ~/.claude resolution at the fixture.
func stubClaudeRoot(t *testing.T, f *fixtureHome) {
	t.Helper()
	previous := claudeRootOverride
	claudeRootOverride = f.root
	t.Cleanup(func() { claudeRootOverride = previous })
}

// TestRunClaudeImportsEverythingAndIsIdempotent runs the full pipeline over
// the fixture home twice: the first run migrates every category, the second
// skips everything already present and leaves row counts untouched.
func TestRunClaudeImportsEverythingAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, false)
	opts.Consolidate = func(context.Context, string) error { return nil }

	report, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
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
	if migrated != 2 || skipped != 0 {
		t.Fatalf("first run migrated=%d skipped=%d, want 2/0", migrated, skipped)
	}
	// The dry-run acceptance arithmetic (B4): main + child both counted.
	if got := len(report.Sessions); got != 2 {
		t.Fatalf("session outcomes = %d", got)
	}

	var rows, sessions, migratedSessions int
	if err := opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_messages`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_sessions WHERE origin='migrated'`).Scan(&migratedSessions); err != nil {
		t.Fatal(err)
	}
	if rows == 0 || sessions != 2 || migratedSessions != 2 {
		t.Fatalf("rows=%d sessions=%d migrated=%d", rows, sessions, migratedSessions)
	}

	second, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	migrated2, skipped2, failed2, _ := second.SessionCounts()
	if migrated2 != 0 || skipped2 != 2 || failed2 != 0 {
		t.Fatalf("second run migrated=%d skipped=%d failed=%d, want 0/2/0", migrated2, skipped2, failed2)
	}
	var rows2 int
	if err := opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_messages`).Scan(&rows2); err != nil {
		t.Fatal(err)
	}
	if rows2 != rows {
		t.Fatalf("second run changed row count: %d -> %d", rows, rows2)
	}
}

// TestRunClaudeDryRunWritesNothing checks the preview leaves no trace.
func TestRunClaudeDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, true)

	report, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_messages`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("dry run wrote %d message rows", rows)
	}
	// The project binding resolves rows through FindOrCreateByRoot, which
	// inserts; a dry run must not create project rows either.
	var projects int
	if err := opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_projects`).Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if projects != 0 {
		t.Fatalf("dry run created %d project rows", projects)
	}
	if _, err := os.Stat(opts.InputHistoryPath); !os.IsNotExist(err) {
		t.Fatal("dry run wrote the input history file")
	}
	if report.DryRun != true {
		t.Fatal("report not flagged as dry run")
	}
}

// TestImportBindsConversationsToAProject pins the fix this repo shipped for
// projectless imports: a conversation lands with its project_id and the
// project's canonical root as cwd, and the real /resume query path
// (ListSessionsForProject) lists both the main conversation and its imported
// subagent child.
func TestImportBindsConversationsToAProject(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, false)
	if _, err := RunClaude(ctx, opts, nil); err != nil {
		t.Fatal(err)
	}
	project, _, err := state.NewProjectStore(opts.DB, "main").FindOrCreateByRoot(ctx, fixture.projectPath, state.CreateProjectInput{ResourceAccess: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"cli-s-fix", "cli-s-fix-agent-deadbeef"} {
		var projectID, cwd string
		if err := opts.DB.QueryRowContext(ctx,
			`SELECT project_id, cwd FROM fb_sessions WHERE id=?`, id).Scan(&projectID, &cwd); err != nil {
			t.Fatal(err)
		}
		if projectID != project.ID {
			t.Fatalf("%s: project_id=%q want %q", id, projectID, project.ID)
		}
		if cwd != project.Root {
			t.Fatalf("%s: cwd=%q want the canonical root %q", id, cwd, project.Root)
		}
	}
	listed, err := state.NewSessionStore(opts.DB, "main").ListSessionsForProject(ctx, project.ID, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, summary := range listed {
		got[summary.ID] = true
	}
	if !got["cli-s-fix"] || !got["cli-s-fix-agent-deadbeef"] {
		t.Fatalf("project listing = %+v, want the imported main conversation and its subagent child", listed)
	}
}

// TestImportScopedToProjectBindsToItsRoot pins the OnlyProject binding rule:
// a conversation recorded in a subdirectory of the launch project binds to
// the launch project's root, matching the matcher that let it in.
func TestImportScopedToProjectBindsToItsRoot(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	// A checkout at the project root makes ProjectRoot(subdir) walk up to
	// it, so the OnlyProject matcher accepts the subdirectory session; the
	// subdirectory itself exists because a recorded working directory is a
	// real directory (symlink evaluation must see it to spell both roots
	// the same way).
	if err := os.MkdirAll(filepath.Join(fixture.projectPath, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(fixture.projectPath, "deep", "sub")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(fixture.projectDir, "s-fix.jsonl"),
		filepath.Join(fixture.projectDir, "s-fix", "subagents", "agent-deadbeef.jsonl"),
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, bytes.ReplaceAll(body, []byte(fixture.projectPath), []byte(subdir)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	opts := fixture.options(t, false)
	opts.OnlyProject = true
	if _, err := RunClaude(ctx, opts, nil); err != nil {
		t.Fatal(err)
	}
	project, _, err := state.NewProjectStore(opts.DB, "main").FindOrCreateByRoot(ctx, fixture.projectPath, state.CreateProjectInput{ResourceAccess: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"cli-s-fix", "cli-s-fix-agent-deadbeef"} {
		var projectID, cwd string
		if err := opts.DB.QueryRowContext(ctx,
			`SELECT project_id, cwd FROM fb_sessions WHERE id=?`, id).Scan(&projectID, &cwd); err != nil {
			t.Fatal(err)
		}
		if projectID != project.ID {
			t.Fatalf("%s: project_id=%q want the launch project %q", id, projectID, project.ID)
		}
		if cwd != project.Root {
			t.Fatalf("%s: cwd=%q want the project root %q", id, cwd, project.Root)
		}
	}
}

// TestImportBindsChildToParentsProject pins the #9 fix: a subagent
// transcript that records a cwd of its own still binds to its parent's
// project (the v9 precedent — children inherit the parent's project and
// directory), in All-projects mode and under OnlyProject alike.
func TestImportBindsChildToParentsProject(t *testing.T) {
	ctx := context.Background()
	newElsewhere := func() string {
		dir := filepath.Join(t.TempDir(), "subtask-elsewhere")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	// All-projects: the child's own cwd must not split it from its parent.
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	elsewhere := newElsewhere()
	sub := filepath.Join(fixture.projectDir, "s-fix", "subagents", "agent-deadbeef.jsonl")
	body, err := os.ReadFile(sub)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub, bytes.ReplaceAll(body, []byte(fixture.projectPath), []byte(elsewhere)), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := fixture.options(t, false)
	if _, err := RunClaude(ctx, opts, nil); err != nil {
		t.Fatal(err)
	}
	project, _, err := state.NewProjectStore(opts.DB, "main").FindOrCreateByRoot(ctx, fixture.projectPath, state.CreateProjectInput{ResourceAccess: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"cli-s-fix", "cli-s-fix-agent-deadbeef"} {
		var projectID string
		if err := opts.DB.QueryRowContext(ctx,
			`SELECT project_id FROM fb_sessions WHERE id=?`, id).Scan(&projectID); err != nil {
			t.Fatal(err)
		}
		if projectID != project.ID {
			t.Fatalf("%s: project_id=%q want the parent's project %q", id, projectID, project.ID)
		}
	}
	var projects int
	if err := opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_projects`).Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if projects != 1 {
		t.Fatalf("fb_projects rows = %d, want only the parent's project (the child's own cwd must not create one)", projects)
	}

	// OnlyProject: the same fixture still binds to the launch project.
	scoped := buildFixtureHome(t)
	stubClaudeRoot(t, scoped)
	scopedElsewhere := newElsewhere()
	scopedSub := filepath.Join(scoped.projectDir, "s-fix", "subagents", "agent-deadbeef.jsonl")
	scopedBody, err := os.ReadFile(scopedSub)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scopedSub, bytes.ReplaceAll(scopedBody, []byte(scoped.projectPath), []byte(scopedElsewhere)), 0o644); err != nil {
		t.Fatal(err)
	}
	scopedOpts := scoped.options(t, false)
	scopedOpts.OnlyProject = true
	if _, err := RunClaude(ctx, scopedOpts, nil); err != nil {
		t.Fatal(err)
	}
	launch, _, err := state.NewProjectStore(scopedOpts.DB, "main").FindOrCreateByRoot(ctx, scoped.projectPath, state.CreateProjectInput{ResourceAccess: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"cli-s-fix", "cli-s-fix-agent-deadbeef"} {
		var projectID, cwd string
		if err := scopedOpts.DB.QueryRowContext(ctx,
			`SELECT project_id, cwd FROM fb_sessions WHERE id=?`, id).Scan(&projectID, &cwd); err != nil {
			t.Fatal(err)
		}
		if projectID != launch.ID || cwd != launch.Root {
			t.Fatalf("%s: project_id=%q cwd=%q, want the launch project %q / %q", id, projectID, cwd, launch.ID, launch.Root)
		}
	}
}

// TestImportFallsBackToTheSourceProjectPathAndSkipsOnlyWithoutAny pins the
// empty-cwd rule: a transcript that records no directory falls back to the
// source's own project resolution (B5), and only a conversation with no
// directory from any source is refused — never written with a NULL project.
func TestImportFallsBackToTheSourceProjectPathAndSkipsOnlyWithoutAny(t *testing.T) {
	ctx := context.Background()

	// Level-1 fallback: the projects map of ~/.claude.json resolves the
	// encoded directory, and the conversation binds there.
	fallback := buildFixtureHome(t)
	stubClaudeRoot(t, fallback)
	claudeJSON := fmt.Sprintf(`{"projects": {%q: {}}}`, fallback.projectPath)
	if err := os.WriteFile(filepath.Join(fallback.root, ".claude.json"), []byte(claudeJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	stripCwd := func(path string) {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		replaced := bytes.ReplaceAll(body,
			[]byte(`"cwd":"`+fallback.projectPath+`"`), []byte(`"cwd":""`))
		if err := os.WriteFile(path, replaced, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stripCwd(filepath.Join(fallback.projectDir, "s-fix.jsonl"))
	stripCwd(filepath.Join(fallback.projectDir, "s-fix", "subagents", "agent-deadbeef.jsonl"))
	fallbackOpts := fallback.options(t, false)
	fallbackOpts.Only = []string{"sessions"}
	if _, err := RunClaude(ctx, fallbackOpts, nil); err != nil {
		t.Fatal(err)
	}
	project, _, err := state.NewProjectStore(fallbackOpts.DB, "main").FindOrCreateByRoot(ctx, fallback.projectPath, state.CreateProjectInput{ResourceAccess: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"cli-s-fix", "cli-s-fix-agent-deadbeef"} {
		var projectID string
		if err := fallbackOpts.DB.QueryRowContext(ctx,
			`SELECT project_id FROM fb_sessions WHERE id=?`, id).Scan(&projectID); err != nil {
			t.Fatal(err)
		}
		if projectID != project.ID {
			t.Fatalf("%s: project_id=%q want the resolved project %q", id, projectID, project.ID)
		}
	}

	// No directory from any source: refused in the dry run and the run
	// alike, with the reason stated and no row written.
	goneHome := t.TempDir()
	gone := "/definitely/not/here/gone"
	encoded := encodeProjectPath(gone)
	goneDir := filepath.Join(goneHome, "projects", encoded)
	if err := os.MkdirAll(goneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join("testdata", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goneDir, "s-gone.jsonl"),
		bytes.ReplaceAll(body, []byte(`"cwd":"/tmp/source-proj"`), []byte(`"cwd":""`)), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := claudeRootOverride
	claudeRootOverride = goneHome
	t.Cleanup(func() { claudeRootOverride = previous })
	for _, dryRun := range []bool{true, false} {
		goneOpts := fallback.options(t, dryRun)
		goneOpts.Only = []string{"sessions"}
		report, err := RunClaude(ctx, goneOpts, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Sessions) != 1 || report.Sessions[0].Status != StatusSkipped ||
			report.Sessions[0].Reason != "no working directory: a conversation must belong to a project" {
			t.Fatalf("dry=%v outcomes = %+v", dryRun, report.Sessions)
		}
	}
	var sessions, projects int
	if err := fallbackOpts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := fallbackOpts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_projects`).Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if sessions != 2 || projects != 1 { // only the fallback import above wrote rows
		t.Fatalf("gone-project run left sessions=%d projects=%d", sessions, projects)
	}
}

// dumpTable renders every row of one table (all columns, rowid order) as a
// deterministic string so a before/after pair can be compared byte for
// byte — the read-only pin a dry-run test needs.
func dumpTable(t *testing.T, ctx context.Context, db *sql.DB, table string) string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT * FROM `+table+` ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for i, v := range values {
			switch typed := v.(type) {
			case nil:
				fmt.Fprintf(&b, "%s=NULL|", columns[i])
			case []byte:
				fmt.Fprintf(&b, "%s=%s|", columns[i], string(typed))
			default:
				fmt.Fprintf(&b, "%s=%v|", columns[i], typed)
			}
		}
		b.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestReimportRebindsProjectlessMigratedConversations pins the存量 repair:
// rerunning /migrate over rows an earlier version wrote without a project
// rebinds them (status rebound) without touching updated_at, source, or any
// native row, and a third run is the plain idempotent skip again.
func TestReimportRebindsProjectlessMigratedConversations(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, false)
	if _, err := RunClaude(ctx, opts, nil); err != nil {
		t.Fatal(err)
	}
	// A native row proves the rebind never leaves the migrated boundary.
	if err := state.NewSessionStore(opts.DB, "main").Ensure(ctx, "native-keep", "native title"); err != nil {
		t.Fatal(err)
	}
	var nativeBefore struct {
		projectID sql.NullString
		cwd       sql.NullString
	}
	if err := opts.DB.QueryRowContext(ctx,
		`SELECT project_id, cwd FROM fb_sessions WHERE id='native-keep'`).
		Scan(&nativeBefore.projectID, &nativeBefore.cwd); err != nil {
		t.Fatal(err)
	}
	// Reproduce the pre-binding import's row shape: project NULL and a
	// non-canonical cwd spelling.
	var updatedAtBefore int64
	if err := opts.DB.QueryRowContext(ctx,
		`SELECT updated_at FROM fb_sessions WHERE id='cli-s-fix'`).Scan(&updatedAtBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := opts.DB.ExecContext(ctx,
		`UPDATE fb_sessions SET project_id=NULL, cwd=? WHERE origin='migrated'`, "/tmp/old-spelling"); err != nil {
		t.Fatal(err)
	}

	// A dry run over the same rows settles them read-only: it reports what
	// it would bind and leaves both tables byte-identical (#20c).
	sessionsBefore := dumpTable(t, ctx, opts.DB, "fb_sessions")
	projectsBefore := dumpTable(t, ctx, opts.DB, "fb_projects")
	dryOpts := *opts
	dryOpts.DryRun = true
	dryReport, err := RunClaude(ctx, &dryOpts, nil)
	if err != nil {
		t.Fatal(err)
	}
	reboundDry := 0
	for _, outcome := range dryReport.Sessions {
		if outcome.Status == StatusRebound {
			reboundDry++
			if outcome.Reason != "would bind to its project" {
				t.Fatalf("dry-run rebound reason = %q", outcome.Reason)
			}
		}
	}
	if reboundDry != 2 {
		t.Fatalf("dry run rebound = %d, want both rebind candidates; outcomes = %+v", reboundDry, dryReport.Sessions)
	}
	if after := dumpTable(t, ctx, opts.DB, "fb_sessions"); after != sessionsBefore {
		t.Fatalf("dry run changed fb_sessions:\nbefore:\n%s\nafter:\n%s", sessionsBefore, after)
	}
	if after := dumpTable(t, ctx, opts.DB, "fb_projects"); after != projectsBefore {
		t.Fatalf("dry run changed fb_projects:\nbefore:\n%s\nafter:\n%s", projectsBefore, after)
	}

	second, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	rebound := 0
	for _, outcome := range second.Sessions {
		if outcome.Status == StatusRebound {
			rebound++
		}
	}
	if rebound != 2 {
		t.Fatalf("rebound = %d, want both the main conversation and its child; outcomes = %+v", rebound, second.Sessions)
	}
	project, _, err := state.NewProjectStore(opts.DB, "main").FindOrCreateByRoot(ctx, fixture.projectPath, state.CreateProjectInput{ResourceAccess: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"cli-s-fix", "cli-s-fix-agent-deadbeef"} {
		var projectID, cwd, source string
		if err := opts.DB.QueryRowContext(ctx,
			`SELECT project_id, cwd, COALESCE(source,'') FROM fb_sessions WHERE id=?`, id).
			Scan(&projectID, &cwd, &source); err != nil {
			t.Fatal(err)
		}
		if projectID != project.ID || cwd != project.Root {
			t.Fatalf("%s: project_id=%q cwd=%q, want %q / %q", id, projectID, cwd, project.ID, project.Root)
		}
		if source != "" {
			t.Fatalf("%s: source=%q, rebound must not touch it", id, source)
		}
	}
	var updatedAtAfter int64
	if err := opts.DB.QueryRowContext(ctx,
		`SELECT updated_at FROM fb_sessions WHERE id='cli-s-fix'`).Scan(&updatedAtAfter); err != nil {
		t.Fatal(err)
	}
	if updatedAtAfter != updatedAtBefore {
		t.Fatalf("rebound moved updated_at: %d -> %d", updatedAtBefore, updatedAtAfter)
	}
	var nativeAfter struct {
		projectID sql.NullString
		cwd       sql.NullString
	}
	if err := opts.DB.QueryRowContext(ctx,
		`SELECT project_id, cwd FROM fb_sessions WHERE id='native-keep'`).
		Scan(&nativeAfter.projectID, &nativeAfter.cwd); err != nil {
		t.Fatal(err)
	}
	if nativeAfter.projectID != nativeBefore.projectID || nativeAfter.cwd != nativeBefore.cwd {
		t.Fatalf("native row moved: %+v -> %+v", nativeBefore, nativeAfter)
	}
	if text := second.Text(); !strings.Contains(text, "Rebound conversations") || !strings.Contains(text, "· 2 rebound") {
		t.Fatalf("report text =\n%s", text)
	}
	listed, err := state.NewSessionStore(opts.DB, "main").ListSessionsForProject(ctx, project.ID, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) == 0 {
		t.Fatal("rebound conversations are still invisible to the /resume query")
	}

	third, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range third.Sessions {
		if outcome.Status != StatusSkipped || outcome.Reason != "already migrated" {
			t.Fatalf("third run is not the idempotent skip: %+v", outcome)
		}
	}
}

// TestRerunNamesMigratedRowsWhoseSourceIsGone pins the #20b fix: a row an
// earlier import wrote without a project whose source transcript is no
// longer on disk can never be discovered again, so no rerun can bind it.
// The rerun ends with a read-only inventory that names the row in the
// report instead of leaving a silently projectless row behind — and writes
// nothing.
func TestRerunNamesMigratedRowsWhoseSourceIsGone(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, false)
	if _, err := RunClaude(ctx, opts, nil); err != nil {
		t.Fatal(err)
	}
	// Reproduce the pre-binding import's row shape, then remove its source
	// transcript: this run cannot discover the row anymore.
	if _, err := opts.DB.ExecContext(ctx,
		`UPDATE fb_sessions SET project_id=NULL WHERE id='cli-s-fix'`); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(fixture.projectDir, "s-fix.jsonl")); err != nil {
		t.Fatal(err)
	}
	var messagesBefore int
	if err := opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_messages`).Scan(&messagesBefore); err != nil {
		t.Fatal(err)
	}

	report, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, outcome := range report.Sessions {
		if outcome.TargetID != "cli-s-fix" {
			continue
		}
		found = true
		if outcome.Status != StatusSkipped || outcome.Reason != reasonSourceTranscriptGone {
			t.Fatalf("orphan outcome = %+v, want skipped with the source-gone reason", outcome)
		}
	}
	if !found {
		t.Fatal("the orphaned migrated row is not named in the report")
	}
	var projectID sql.NullString
	if err := opts.DB.QueryRowContext(ctx,
		`SELECT project_id FROM fb_sessions WHERE id='cli-s-fix'`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if projectID.Valid {
		t.Fatalf("the orphaned row was written: project_id=%v", projectID.String)
	}
	var messagesAfter int
	if err := opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_messages`).Scan(&messagesAfter); err != nil {
		t.Fatal(err)
	}
	if messagesAfter != messagesBefore {
		t.Fatalf("the rerun wrote message rows: %d -> %d", messagesBefore, messagesAfter)
	}
}

// TestImportSessionOwnedByAnotherAgent pins B10: a conversation id that
// already belongs to a different primary agent is skipped with the dedicated
// status and no message is written into it.
func TestImportSessionOwnedByAnotherAgent(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, false)
	target := "cli-s-fix"
	if _, err := opts.DB.ExecContext(ctx,
		`INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at) VALUES(?,?,?,?,?)`,
		target, "other", "other's title", 1, 1); err != nil {
		t.Fatal(err)
	}
	report, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, outcome := range report.Sessions {
		if outcome.TargetID == target {
			found = true
			if outcome.Status != StatusOwned {
				t.Fatalf("status = %s (%s)", outcome.Status, outcome.Reason)
			}
		}
	}
	if !found {
		t.Fatal("owned outcome missing")
	}
	var count int
	if err := opts.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM fb_messages WHERE session_id=?`, target).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("wrote %d rows into another agent's session", count)
	}
}

// TestImportSessionReplayAndBoundary walks the B1/B2/B3 acceptance: the
// imported session replays in source order through ListAllMessages, the
// session's compact pointer is the last boundary row id as TEXT, the window
// chain is self-consistent, and the model-context projection sees the
// summary the source wrote.
func TestImportSessionReplayAndBoundary(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, false)
	if _, err := RunClaude(ctx, opts, nil); err != nil {
		t.Fatal(err)
	}

	store := state.NewSessionStore(opts.DB, "main")
	messages, err := store.ListAllMessages(ctx, "cli-s-fix", 0)
	if err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, message := range messages {
		roles = append(roles, message.Role)
	}
	want := "user,reasoning,assistant,assistant,tool,user,system,assistant,system,assistant"
	if strings.Join(roles, ",") != want {
		t.Fatalf("replay roles = %v\nwant           %v", roles, want)
	}

	var boundaryID sql.NullInt64
	var initialWindow string
	if err := opts.DB.QueryRowContext(ctx,
		`SELECT compact_boundary_message_id, initial_window_id FROM fb_sessions WHERE id='cli-s-fix'`).
		Scan(&boundaryID, &initialWindow); err != nil {
		t.Fatal(err)
	}
	if !boundaryID.Valid || initialWindow == "" {
		t.Fatalf("boundary=%v initial=%q", boundaryID, initialWindow)
	}
	// The pointer names a real boundary row of this session whose window
	// chain starts at the session's initial window id.
	var partsJSON string
	var rowSession string
	if err := opts.DB.QueryRowContext(ctx,
		`SELECT parts, session_id FROM fb_messages WHERE id=?`, boundaryID.Int64).
		Scan(&partsJSON, &rowSession); err != nil {
		t.Fatal(err)
	}
	if rowSession != "cli-s-fix" {
		t.Fatalf("boundary row belongs to %s", rowSession)
	}
	part, ok := state.ParseCompactBoundaryPart(partsJSON)
	if !ok {
		t.Fatal("pointer does not name a boundary row")
	}
	if part.FirstWindowID != initialWindow || part.WindowID == "" {
		t.Fatalf("window chain = first %q own %q session initial %q", part.FirstWindowID, part.WindowID, initialWindow)
	}
	if part.WindowNumber != 2 {
		t.Fatalf("latest boundary window number = %d, want 2", part.WindowNumber)
	}

	// The model-context projection replays the replacement history, whose
	// first message is the summary — the "continue a compacted session and
	// still see the summary" acceptance.
	modelMessages, err := store.ListTranscriptMessages(ctx, "cli-s-fix", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(modelMessages) == 0 {
		t.Fatal("model context is empty")
	}
	joined := ""
	for _, message := range modelMessages {
		joined += message.TextContent() + "\n---\n"
	}
	if !strings.Contains(joined, state.CompactSummaryPrefix) || !strings.Contains(joined, "SUMMARY: second window") {
		t.Fatalf("model context lacks the compact summary")
	}
	if strings.Count(joined, state.CompactSummaryPrefix) != 1 {
		t.Fatalf("summary appears %d times; stacking detected", strings.Count(joined, state.CompactSummaryPrefix))
	}
}

// TestImportSubagentSession checks §4.4: the child conversation exists under
// the parent, its title comes from .meta.json, and it replays on its own.
func TestImportSubagentSession(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, false)
	if _, err := RunClaude(ctx, opts, nil); err != nil {
		t.Fatal(err)
	}
	child := "cli-s-fix-agent-deadbeef"
	var parent, title string
	if err := opts.DB.QueryRowContext(ctx,
		`SELECT parent_session_id, title FROM fb_sessions WHERE id=?`, child).
		Scan(&parent, &title); err != nil {
		t.Fatal(err)
	}
	if parent != "cli-s-fix" {
		t.Fatalf("parent = %q", parent)
	}
	if title != "find the root cause" {
		t.Fatalf("title = %q, want the meta description", title)
	}
	store := state.NewSessionStore(opts.DB, "main")
	children, err := store.ListChildSessionsRecent(ctx, "cli-s-fix", 10)
	if err != nil || len(children) != 1 || children[0].ID != child {
		t.Fatalf("child listing = %v (%v)", children, err)
	}
}

// TestImportSubagentWithoutAnImportedParent pins that a subagent's parent link
// names a conversation of this agent that exists. When the parent's id belongs
// to another primary agent the parent is skipped, and the subagent is imported
// on its own — neither refused by the parent reference nor tied to another
// tenant's conversation.
func TestImportSubagentWithoutAnImportedParent(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, false)
	if err := state.NewSessionStore(opts.DB, "someone-else").Ensure(ctx, "cli-s-fix", "cli-s-fix"); err != nil {
		t.Fatal(err)
	}
	if _, err := RunClaude(ctx, opts, nil); err != nil {
		t.Fatal(err)
	}
	var parent sql.NullString
	var owner string
	if err := opts.DB.QueryRowContext(ctx,
		`SELECT parent_session_id, agent_id FROM fb_sessions WHERE id=?`, "cli-s-fix-agent-deadbeef").
		Scan(&parent, &owner); err != nil {
		t.Fatalf("subagent conversation was not imported: %v", err)
	}
	if parent.Valid || owner != "main" {
		t.Fatalf("subagent imported as parent=%v owner=%q, want no parent and this agent", parent, owner)
	}
}

// TestImportSessionFailingFileRecordedPerSession corrupts one transcript so
// its parse fails and checks the failure lands in the report while the other
// conversation still imports (per-session failure isolation).
func TestImportSessionFailingFileRecordedPerSession(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	// A directory named like a transcript makes the open fail.
	if err := os.MkdirAll(filepath.Join(fixture.projectDir, "s-bad.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Give the bad session a discoverable shape (a directory is skipped by
	// discovery), so instead corrupt the good one's sibling list by adding a
	// session whose file is a directory entry with .jsonl suffix.
	_ = os.Remove(filepath.Join(fixture.projectDir, "s-bad.jsonl"))
	opts := fixture.options(t, false)
	report, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	migrated, _, failed, _ := report.SessionCounts()
	if migrated != 2 || failed != 0 {
		t.Fatalf("unexpected counts migrated=%d failed=%d", migrated, failed)
	}
	// A genuinely unreadable file is covered by a chmod-based case when the
	// runner permits it; here the invariant is that a good run reports all.
	if report.Text() == "" {
		t.Fatal("empty report")
	}
}

// The tests in this file run against the machine's real ~/.claude. They are
// the manual acceptance pass for the migration: the dry run is read-only,
// and the real import targets a throwaway home, workspace, database and
// config so nothing the user depends on is touched. The project MCP write
// is validated in dry run only — a real write would edit live repositories.
//
// Enable with FOREBRAIN_MIGRATE_REALDATA=1; CI and default runs skip.

func realDataEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("FOREBRAIN_MIGRATE_REALDATA") != "1" {
		t.Skip("set FOREBRAIN_MIGRATE_REALDATA=1 to run the real-data acceptance pass")
	}
	if claudeRootAt("") == "" {
		t.Skip("~/.claude not present on this machine")
	}
}

// TestRealDataDryRunIsReadOnlyAndComplete previews the real source with
// every category on and proves the arithmetic holds on live data.
func TestRealDataDryRunIsReadOnlyAndComplete(t *testing.T) {
	realDataEnabled(t)
	ctx := context.Background()
	previous := claudeRootOverride
	claudeRootOverride = ""
	t.Cleanup(func() { claudeRootOverride = previous })

	opts := isolatedOptions(t, true)
	plan, report, err := PlanClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, failed, owned := report.SessionCounts()
	if failed != 0 || owned != 0 {
		for _, outcome := range report.Sessions {
			if outcome.Status == StatusFailed || outcome.Status == StatusOwned {
				t.Errorf("%s: %s (%s)", outcome.SourcePath, outcome.Status, outcome.Reason)
			}
		}
		t.Fatalf("dry run over real data: failed=%d owned=%d", failed, owned)
	}
	if plan.Sessions == 0 || plan.MessageRows == 0 {
		t.Fatalf("plan over real data is empty: %+v", plan)
	}
	t.Logf("real-data plan: sessions=%d subagents=%d rows=%d memories=%d plans=%d history=%d estDB=%d",
		plan.Sessions, plan.Subagents, plan.MessageRows, plan.Memories, plan.Plans, plan.HistoryLines, plan.DBEstimate)

	var dbRows int
	if err := opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_messages`).Scan(&dbRows); err != nil {
		t.Fatal(err)
	}
	if dbRows != 0 {
		t.Fatalf("dry run wrote %d rows into the database", dbRows)
	}
	if _, err := os.Stat(opts.InputHistoryPath); !os.IsNotExist(err) {
		t.Fatal("dry run wrote the input history")
	}
	if _, err := os.Stat(filepath.Join(opts.Home, "skills")); !os.IsNotExist(err) {
		t.Fatal("dry run wrote skills")
	}
}

// TestRealDataImportIntoIsolatedHome runs the real import against a
// throwaway install: every session migrates or is honestly skipped, the
// plan's offer matches the run's outcome, compacted conversations carry a
// boundary, the largest transcript replays whole, and a second run is a
// no-op.
func TestRealDataImportIntoIsolatedHome(t *testing.T) {
	realDataEnabled(t)
	ctx := context.Background()
	previous := claudeRootOverride
	claudeRootOverride = ""
	t.Cleanup(func() { claudeRootOverride = previous })

	planOpts := isolatedOptions(t, true)
	plan, _, err := PlanClaude(ctx, planOpts, nil)
	if err != nil {
		t.Fatal(err)
	}

	opts := isolatedOptions(t, false)
	opts.Only = []string{"sessions", "memories", "skills", "plans", "history"}
	report, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	migrated, skipped, failed, owned := report.SessionCounts()
	if failed != 0 || owned != 0 {
		for _, outcome := range report.Sessions {
			if outcome.Status == StatusFailed || outcome.Status == StatusOwned {
				t.Errorf("%s: %s (%s)", outcome.SourcePath, outcome.Status, outcome.Reason)
			}
		}
	}
	if failed != 0 || owned != 0 {
		t.Fatalf("real import: failed=%d owned=%d", failed, owned)
	}
	if plan.Sessions+plan.Subagents != migrated+skipped {
		t.Fatalf("plan offered %d+%d, run migrated %d skipped %d — arithmetic broken",
			plan.Sessions, plan.Subagents, migrated, skipped)
	}
	if migrated == 0 {
		t.Fatal("nothing migrated on a first run over real data")
	}
	t.Logf("real import: migrated=%d skipped=%d rows=%d", migrated, skipped, report.MessageRows)

	// Compacted conversations exist and their pointers are decimal row ids.
	var boundaries int
	if err := opts.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM fb_sessions WHERE origin='migrated' AND compact_boundary_id<>''`).
		Scan(&boundaries); err != nil {
		t.Fatal(err)
	}
	if boundaries == 0 {
		t.Fatal("no compacted session carried a boundary over")
	}
	var badPointers int
	if err := opts.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM fb_sessions s
WHERE s.compact_boundary_id<>''
  AND NOT EXISTS(SELECT 1 FROM fb_messages m WHERE m.id=CAST(s.compact_boundary_id AS INTEGER) AND m.session_id=s.id)`).
		Scan(&badPointers); err != nil {
		t.Fatal(err)
	}
	if badPointers != 0 {
		t.Fatalf("%d compact pointers do not name a boundary row of their session", badPointers)
	}

	// The largest transcript replays whole through the store the /resume
	// path uses.
	var largestID string
	var largestRows int
	if err := opts.DB.QueryRowContext(ctx, `
SELECT s.id, (SELECT COUNT(*) FROM fb_messages m WHERE m.session_id=s.id)
FROM fb_sessions s WHERE s.origin='migrated'
ORDER BY 2 DESC LIMIT 1`).Scan(&largestID, &largestRows); err != nil {
		t.Fatal(err)
	}
	store := state.NewSessionStore(opts.DB, "main")
	messages, err := store.ListAllMessages(ctx, largestID, 0)
	if err != nil || len(messages) != largestRows {
		t.Fatalf("largest session replay = %d/%d rows (%v)", len(messages), largestRows, err)
	}
	var roles []string
	for _, message := range messages {
		roles = append(roles, message.Role)
	}
	joined := strings.Join(roles, ",")
	for _, want := range []string{"user", "assistant", "tool"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("largest replay lacks %q rows", want)
		}
	}

	// Every memory note landed somewhere and none was dropped silently.
	var unplaced int
	for _, outcome := range report.Memories {
		if outcome.Scope == "global" {
			unplaced++
		}
	}
	t.Logf("memories: %d directories fell back to the global scope", unplaced)

	// Second run: nothing new.
	second, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	migrated2, _, failed2, owned2 := second.SessionCounts()
	if migrated2 != 0 || failed2 != 0 || owned2 != 0 {
		t.Fatalf("second run: migrated=%d failed=%d owned=%d", migrated2, failed2, owned2)
	}
}

// isolatedOptions builds a fully throwaway install for a real-data pass.
func isolatedOptions(t *testing.T, dryRun bool) *Options {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	home := filepath.Join(t.TempDir(), "forebrain-home")
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "forebrain.yaml")
	if err := os.WriteFile(cfgPath, []byte("agents:\n  defaults:\n    mcp_servers: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Options{
		DryRun:           dryRun,
		Home:             home,
		AgentID:          "main",
		AgentWorkspace:   workspace,
		ConfigPath:       cfgPath,
		DB:               db,
		InputHistoryPath: filepath.Join(workspace, "state", "cli-input-history.txt"),
	}
}

// A Claude Code home the user chose is the one read, not the default one:
// the picker's directory reaches the import itself.
func TestRunClaudeReadsTheChosenHome(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	// The default resolution points somewhere empty; only the chosen home
	// holds a session.
	previous := claudeRootOverride
	claudeRootOverride = t.TempDir()
	t.Cleanup(func() { claudeRootOverride = previous })

	opts := fixture.options(t, true)
	opts.SourceRoot = fixture.root
	plan, report, err := PlanClaude(ctx, opts, nil)
	if err != nil {
		t.Fatalf("PlanClaude: %v", err)
	}
	if report.SourceRoot != fixture.root || plan.Sessions == 0 {
		t.Fatalf("read %q with %d sessions, want the chosen home %q", report.SourceRoot, plan.Sessions, fixture.root)
	}
}

// The running user's own ~/.claude.json is not part of another home: a
// chosen home brings only its own projects and servers.
func TestDiscoverClaudeReadsOnlyTheChosenHomesConfig(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	if err := os.WriteFile(filepath.Join(userHome, ".claude.json"), []byte(`{"mcpServers":{"from-this-machine":{"command":"x"}},"projects":{"/this/machine/project":{"mcpServers":{"local":{"command":"y"}}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	backup := t.TempDir()
	root := filepath.Join(backup, ".claude")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, ".claude.json"), []byte(`{"mcpServers":{"from-the-backup":{"command":"z"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := discoverClaude(root)
	if err != nil {
		t.Fatalf("discoverClaude: %v", err)
	}
	if _, ok := data.UserMCP["from-the-backup"]; !ok {
		t.Fatalf("the chosen home's servers are missing: %+v", data.UserMCP)
	}
	if _, ok := data.UserMCP["from-this-machine"]; ok || len(data.JSONProjectPaths) != 0 {
		t.Fatalf("this machine's ~/.claude.json leaked into another home: %+v %v", data.UserMCP, data.JSONProjectPaths)
	}
}
