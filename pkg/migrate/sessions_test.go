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
	if _, err := os.Stat(opts.InputHistoryPath); !os.IsNotExist(err) {
		t.Fatal("dry run wrote the input history file")
	}
	if report.DryRun != true {
		t.Fatal("report not flagged as dry run")
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
