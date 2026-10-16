package migrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

// fixedClock hands Options its injectable now() as a fixed instant.
func fixedClock() time.Time { return fixedNow }

// TestPlanMatchesRun pins B4's relative acceptance: the dry run's session
// offer equals what the real run migrates plus what it skips, without any
// absolute number that source churn would break.
func TestPlanMatchesRun(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	planOpts := fixture.options(t, true)
	plan, _, err := PlanClaude(ctx, planOpts, nil)
	if err != nil {
		t.Fatal(err)
	}
	runOpts := fixture.options(t, false)
	report, err := RunClaude(ctx, runOpts, nil)
	if err != nil {
		t.Fatal(err)
	}
	migrated, skipped, failed, owned := report.SessionCounts()
	if failed != 0 || owned != 0 {
		t.Fatalf("run failed=%d owned=%d", failed, owned)
	}
	updatedUnits, updatedMessages := report.countUpdated()
	if plan.Sessions+plan.Subagents+plan.Updated != migrated+skipped+updatedUnits {
		t.Fatalf("plan offers %d+%d+%d updated, run migrated %d skipped %d updated %d",
			plan.Sessions, plan.Subagents, plan.Updated, migrated, skipped, updatedUnits)
	}
	if plan.UpdatedMessages != updatedMessages {
		t.Fatalf("plan updated messages %d != run %d", plan.UpdatedMessages, updatedMessages)
	}
	if plan.MessageRows != report.MessageRows {
		t.Fatalf("plan rows %d != run rows %d", plan.MessageRows, report.MessageRows)
	}
	if !strings.Contains(plan.Text(), "Conversations:") || !strings.Contains(plan.Text(), "Database: grows by about") {
		t.Fatalf("plan text =\n%s", plan.Text())
	}
}

// TestReportTextListsMigratedSessions checks the report names every imported
// conversation so results stay identifiable and reversible (B9).
func TestReportTextListsMigratedSessions(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, false)
	opts.Consolidate = func(context.Context, string) error { return nil }
	report, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := report.Text()
	for _, want := range []string{
		"cli-s-fix", "cli-s-fix-agent-deadbeef", "My Fixed Title",
		"/resume lists them", "consolidated",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %q:\n%s", want, text)
		}
	}
}

// TestReportConsolidationHonesty checks both honesty paths: a nil callback
// and a disabled-feature error are reported as not-run, never as success.
func TestReportConsolidationHonesty(t *testing.T) {
	plain := consolidateNow(context.Background(), &Options{}, nil)
	if !strings.Contains(plain, "not available") {
		t.Fatalf("nil callback wording = %q", plain)
	}
	failing := consolidateNow(context.Background(), &Options{
		Consolidate: func(context.Context, string) error { return context.DeadlineExceeded },
	}, nil)
	if !strings.Contains(failing, "did not run") || !strings.Contains(failing, "deadline") {
		t.Fatalf("failing callback wording = %q", failing)
	}
}

// TestOnlyFilterRestrictsCategories checks --only keeps the other categories
// untouched and names them in the report.
func TestOnlyFilterRestrictsCategories(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, false)
	opts.Only = []string{"sessions"}
	report, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Sessions) == 0 {
		t.Fatal("sessions category did not run")
	}
	if len(report.Memories) != 0 || len(report.Skills) != 0 || len(report.Plans) != 0 || len(report.MCP) != 0 {
		t.Fatal("an excluded category ran")
	}
	if strings.Join(report.SkippedCategories, ",") != "memories,skills,plans,mcp,lsp,history" {
		t.Fatalf("skipped categories = %v", report.SkippedCategories)
	}
	var memories int
	if err := opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_messages`).Scan(&memories); err != nil {
		t.Fatal(err)
	}
	if memories == 0 {
		t.Fatal("no messages imported under --only sessions")
	}
	if _, err := os.Stat(filepath.Join(opts.AgentWorkspace, "memories")); !os.IsNotExist(err) {
		t.Fatal("memories were written despite --only sessions")
	}
}

// TestOnlyProjectFilterRestrictsSessions keeps conversations whose resolved
// project is not the current one out of the import.
func TestOnlyProjectFilterRestrictsSessions(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, false)
	opts.Only = []string{"sessions"}
	opts.OnlyProject = true
	opts.CurrentProjectRoot = t.TempDir() // some other project
	report, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Sessions) != 0 {
		t.Fatalf("sessions imported for a foreign project: %+v", report.Sessions)
	}
}

// TestOnlyProjectWithoutARootIsRefusedAtTheEntry pins the #20a fix: an
// import scoped to the current project with no project root is refused at
// the Run entry for both sources alike, instead of claude silently importing
// everything while codex skips every rollout for having "no working
// directory".
func TestOnlyProjectWithoutARootIsRefusedAtTheEntry(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		kind Kind
		run  func(*Options) error
	}{
		{KindClaude, func(o *Options) error {
			_, err := RunClaude(ctx, o, nil)
			return err
		}},
		{KindCodex, func(o *Options) error {
			_, err := RunCodex(ctx, o, nil)
			return err
		}},
	} {
		opts := &Options{OnlyProject: true, AgentID: "main"}
		err := tc.run(opts)
		if err == nil || !strings.Contains(err.Error(), "current project root is empty") {
			t.Fatalf("%s: err = %v, want the empty-root refusal", tc.kind, err)
		}
	}
}

// A preview over a home that was already imported says so: it offers only
// what a run would write, and counts the rest as already here.
func TestPlanAfterAnImportCountsWhatIsAlreadyHere(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, false)
	opts.Consolidate = func(context.Context, string) error { return nil }
	if _, err := RunClaude(ctx, opts, nil); err != nil {
		t.Fatal(err)
	}
	again := *opts
	again.DryRun = true
	plan, _, err := PlanClaude(ctx, &again, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Sessions != 0 || plan.Subagents != 0 || plan.MessageRows != 0 || plan.SourceBytes != 0 || plan.AlreadyHere == 0 {
		t.Fatalf("plan after the import = %+v", plan)
	}
	if !strings.Contains(plan.Text(), "already here") {
		t.Fatalf("plan text =\n%s", plan.Text())
	}
}

func TestPlanWithNothingToImportGrowsNothing(t *testing.T) {
	plan := &Plan{Source: KindClaude, AlreadyHere: 3}
	if text := plan.Text(); !strings.Contains(text, "Database: no new conversations to store") {
		t.Fatalf("plan text =\n%s", text)
	}
}

// TestPlanAndReportTextCarryLanguageServers checks the language-server counts
// in the preview and the detail section in the report when an import brings
// language servers along.
func TestPlanAndReportTextCarryLanguageServers(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	official := lspPluginVersionDir(t, fixture.root, "gopls-lsp@claude-plugins-official", "1.0.0")
	writePluginFile(t, official, ".lsp.json", `{"go": {"command": "gopls", "extensionToLanguage": {".go": "go"}}}`)
	custom := lspPluginVersionDir(t, fixture.root, "mylang@acme", "1.0.0")
	writePluginFile(t, custom, ".lsp.json", `{"mylang": {"command": "mylang-ls", "extensionToLanguage": {".ml2": "mylang"}}}`)
	enableClaudePluginsInHome(t, fixture.root, "gopls-lsp@claude-plugins-official", "mylang@acme")
	stubClaudeRoot(t, fixture)
	plan, report, err := PlanClaude(ctx, fixture.options(t, true), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Text(), "Language servers: 1 enabled · 1 added") {
		t.Fatalf("plan text =\n%s", plan.Text())
	}
	if !strings.Contains(report.Text(), "Language servers: 1 enabled · 1 added · 0 skipped") {
		t.Fatalf("report text =\n%s", report.Text())
	}
	if !strings.Contains(report.Text(), "\nLanguage servers\n") {
		t.Fatalf("report text =\n%s", report.Text())
	}
}

// TestPlanAndReportTextOmitLanguageServersWithoutPlugins checks neither text
// mentions language servers when no enabled plugin declares any.
func TestPlanAndReportTextOmitLanguageServersWithoutPlugins(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	plan, report, err := PlanClaude(ctx, fixture.options(t, true), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.Text(), "Language servers") || strings.Contains(report.Text(), "Language servers") {
		t.Fatalf("plan text =\n%s\nreport text =\n%s", plan.Text(), report.Text())
	}
}

// ---- migrated from consolidation_gate_test.go (tests for this file's production code) ----

// consolidation_gate_test.go pins the third hard requirement: the
// post-import memory consolidation runs only for data the import actually
// wrote. A rerun that wrote nothing pays no pass, a new note pays exactly
// one, and — the gate-move fix — a run whose only new write is the changed
// global instruction file still pays its one pass.

// countingConsolidate returns a callback that counts its calls.
func countingConsolidate(calls *int) func(context.Context, string) error {
	return func(context.Context, string) error {
		*calls++
		return nil
	}
}

func TestConsolidationRunsOnlyForNewNotes(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, false)

	var calls int
	opts.Consolidate = countingConsolidate(&calls)

	// First run: the fixture's memory note is written, so one pass is owed
	// and taken.
	first, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Consolidation != "consolidated" || calls != 1 {
		t.Fatalf("first run consolidation = %q calls = %d, want consolidated/1", first.Consolidation, calls)
	}

	// Second run over unchanged sources: nothing written, no pass — the
	// call count from the first run stands still.
	afterFirst := calls
	second, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != afterFirst {
		t.Fatalf("zero-new-note rerun consolidated again: %d → %d calls", afterFirst, calls)
	}
	if second.Consolidation != "not run — no new notes" {
		t.Fatalf("second run consolidation = %q", second.Consolidation)
	}
	if text := second.Text(); !strings.Contains(text, "Memory consolidation: not run") {
		t.Fatalf("report hid the not-run decision:\n%s", text)
	}

	// A new memory note pays exactly one pass.
	newNote := "---\nname: later-note\ndescription: added after the import\nmetadata:\n  modified: \"2026-09-05T03:04:05Z\"\n  originSessionId: s-fix\n---\n\nA note the source gained later.\n"
	if err := os.WriteFile(filepath.Join(fixture.projectDir, "memory", "later-note.md"), []byte(newNote), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != afterFirst+1 || third.Consolidation != "consolidated" {
		t.Fatalf("new-note run: calls = %d consolidation = %q", calls, third.Consolidation)
	}
}

// TestConsolidationTriggersOnGlobalInstructionsAlone pins the gate-move fix:
// the global instruction note lands after the project memories in the run,
// and the gate sits after it — a run whose only new write is a changed
// CLAUDE.md still consolidates exactly once.
func TestConsolidationTriggersOnGlobalInstructionsAlone(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, false)

	var calls int
	opts.Consolidate = countingConsolidate(&calls)
	if _, err := RunClaude(ctx, opts, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("first run calls = %d, want 1", calls)
	}

	// The only change in the source home is CLAUDE.md appearing.
	if err := os.WriteFile(filepath.Join(fixture.root, "CLAUDE.md"), []byte("Always answer in Chinese.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(fixture.root, "CLAUDE.md"), later, later); err != nil {
		t.Fatal(err)
	}
	second, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || second.Consolidation != "consolidated" {
		t.Fatalf("CLAUDE.md-only run: calls = %d consolidation = %q, want 2 total/consolidated", calls, second.Consolidation)
	}

	// And a third run with it untouched owes nothing again.
	third, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || third.Consolidation != "not run — no new notes" {
		t.Fatalf("settled CLAUDE.md run: calls = %d consolidation = %q", calls, third.Consolidation)
	}
}
