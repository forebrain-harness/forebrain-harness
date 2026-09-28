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
	if plan.Sessions+plan.Subagents != migrated+skipped {
		t.Fatalf("plan offers %d+%d, run migrated %d and skipped %d",
			plan.Sessions, plan.Subagents, migrated, skipped)
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
	if strings.Join(report.SkippedCategories, ",") != "memories,skills,plans,mcp,history" {
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
