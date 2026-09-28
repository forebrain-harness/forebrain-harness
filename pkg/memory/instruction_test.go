package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	state "github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// newInstructionRoots returns an agent's Roots plus a project key with an
// already-created project memory folder, the fixture every test in this file
// exercises. The global scope is left empty throughout: these tests are about
// the project-scope mechanics (tool guidance, pending notes, budget caps),
// which apply identically to either scope, so exercising one is enough and
// keeps the fixtures small.
func newInstructionRoots(t *testing.T) (Roots, string) {
	t.Helper()
	base := t.TempDir()
	roots, err := ResolveRootsForAgent(base)
	if err != nil {
		t.Fatal(err)
	}
	const projectKey = "-Test-project"
	root := roots.Scope(Scope{Kind: ScopeProject, Key: projectKey})
	if err := os.MkdirAll(root.MemoryRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	return roots, projectKey
}

func writeSummary(t *testing.T, root Root, body string) {
	t.Helper()
	if err := os.WriteFile(SummaryPath(root), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeNote(t *testing.T, root Root, filename, body string, modified time.Time) string {
	t.Helper()
	backend := Backend{Root: root.MemoryRoot}
	path, err := backend.AddAdHocNote(AdHocNoteScopeProject, filename, body)
	if err != nil {
		t.Fatal(err)
	}
	absolute := filepath.Join(root.MemoryRoot, filepath.FromSlash(path))
	if err := os.Chtimes(absolute, modified, modified); err != nil {
		t.Fatal(err)
	}
	return path
}

// The instruction describes the memory layout with absolute paths while the
// dedicated tools address files relative to the memory root, so when those
// tools are exposed the instruction has to say how to address them — otherwise
// a model copies an absolute layout path into memories_read, or reads a note
// back by the bare filename it wrote with, and is told the path was not found.
func TestRecallInstructionExplainsToolPathsWhenToolsAreExposed(t *testing.T) {
	roots, projectKey := newInstructionRoots(t)
	root := roots.Scope(Scope{Kind: ScopeProject, Key: projectKey})
	writeSummary(t, root, "- prefers concise updates")

	withTools, err := RenderTurnInstruction(roots, projectKey, InstructionOptions{Recall: true, DedicatedTools: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"memories_read",
		"relative to the\n  project memory base path above",
		AdHocNotesDir + "/",
		"pass that path to",
	} {
		if !strings.Contains(withTools, want) {
			t.Fatalf("instruction missing %q:\n%s", want, withTools)
		}
	}
	if strings.Contains(withTools, "{{") {
		t.Fatalf("unrendered placeholder:\n%s", withTools)
	}

	withoutTools, err := RenderTurnInstruction(roots, projectKey, InstructionOptions{Recall: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(withoutTools, "memories_read") {
		t.Fatalf("tool guidance leaked without the tools:\n%s", withoutTools)
	}
	if strings.Contains(withoutTools, "{{") {
		t.Fatalf("unrendered placeholder:\n%s", withoutTools)
	}
	if !strings.Contains(withoutTools, "prefers concise updates") {
		t.Fatalf("summary missing:\n%s", withoutTools)
	}
}

// The first durable rule a user states arrives in a workspace whose memory
// folder is still empty — Phase 2 has never run, so there is no summary. If the
// capture rules waited for one, that rule could never be written down, and the
// store would stay empty for exactly the sessions that most need it.
func TestCaptureInstructionRendersBeforeAnyMemoryExists(t *testing.T) {
	roots, projectKey := newInstructionRoots(t)
	root := roots.Scope(Scope{Kind: ScopeProject, Key: projectKey})

	instruction, err := RenderTurnInstruction(roots, projectKey, InstructionOptions{Capture: true, DedicatedTools: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Capturing memory", "memories_add_ad_hoc_note", root.MemoryRoot} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("capture instruction missing %q:\n%s", want, instruction)
		}
	}
	if strings.Contains(instruction, "{{") {
		t.Fatalf("unrendered placeholder:\n%s", instruction)
	}
	if strings.Contains(instruction, "Using stored memory") {
		t.Fatalf("recall section rendered without a summary:\n%s", instruction)
	}

	withoutTools, err := RenderTurnInstruction(roots, projectKey, InstructionOptions{Capture: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(withoutTools, "memories_add_ad_hoc_note") {
		t.Fatalf("tool guidance leaked without the tools:\n%s", withoutTools)
	}
	if !strings.Contains(withoutTools, filepath.Join(root.MemoryRoot, AdHocNotesDir)) {
		t.Fatalf("file path guidance missing:\n%s", withoutTools)
	}
}

// Nothing in the instruction may describe a capability the settings withhold: a
// turn told to capture with generation off would write notes the user disabled,
// and a turn told to recall with use_memories off would surface memory they
// asked not to see.
func TestTurnInstructionHalvesFollowTheirSettings(t *testing.T) {
	roots, projectKey := newInstructionRoots(t)
	root := roots.Scope(Scope{Kind: ScopeProject, Key: projectKey})
	writeSummary(t, root, "- prefers concise updates")

	recallOnly, err := RenderTurnInstruction(roots, projectKey, InstructionOptions{Recall: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(recallOnly, "Capturing memory") {
		t.Fatalf("capture rules rendered with generation off:\n%s", recallOnly)
	}

	captureOnly, err := RenderTurnInstruction(roots, projectKey, InstructionOptions{Capture: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(captureOnly, "prefers concise updates") {
		t.Fatalf("summary rendered with recall off:\n%s", captureOnly)
	}

	if !(InstructionOptions{}).Empty() {
		t.Fatal("disabled options must render nothing")
	}
	off, err := RenderTurnInstruction(roots, projectKey, InstructionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(off) != "" {
		t.Fatalf("instruction rendered with memory off:\n%s", off)
	}
}

// A note only reaches the summary through a consolidation pass, which runs at
// startup behind a cooldown. Until then the note is the only copy of the rule,
// so the turn instruction has to carry it — otherwise the session right after a
// capture would ask the user to repeat what was just written down.
func TestPendingNotesAreSurfacedUntilConsolidated(t *testing.T) {
	roots, projectKey := newInstructionRoots(t)
	root := roots.Scope(Scope{Kind: ScopeProject, Key: projectKey})
	writeSummary(t, root, "- prefers concise updates")
	consolidated := writeNote(t, root, "2026-08-19T09-00-00-old-lesson.md",
		"# Old lesson\n\nalready folded into MEMORY.md", time.Now().Add(-2*time.Hour))
	pending := writeNote(t, root, "2026-08-20T09-00-00-root-cause-only.md",
		"# Root cause only\n\nThe user requires fixes to address the root cause.", time.Now())

	if err := markConsolidationStart(root, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	instruction, err := RenderTurnInstruction(roots, projectKey, InstructionOptions{Recall: true, DedicatedTools: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(instruction, pending) || !strings.Contains(instruction, "address the root cause") {
		t.Fatalf("pending note missing:\n%s", instruction)
	}
	if strings.Contains(instruction, consolidated) {
		t.Fatalf("already consolidated note re-injected:\n%s", instruction)
	}

	if err := markConsolidationStart(root, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	after, err := RenderTurnInstruction(roots, projectKey, InstructionOptions{Recall: true, DedicatedTools: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(after, pending) {
		t.Fatalf("note still pending after consolidation:\n%s", after)
	}
}

// With no marker no pass has ever run, so every note is still pending. Dropping
// them there would lose exactly the notes written before the first consolidation
// — the ones a brand-new store depends on.
func TestPendingNotesSurfaceWhenNoConsolidationHasRun(t *testing.T) {
	roots, projectKey := newInstructionRoots(t)
	root := roots.Scope(Scope{Kind: ScopeProject, Key: projectKey})
	writeSummary(t, root, "- prefers concise updates")
	note := writeNote(t, root, "2026-08-20T09-00-00-first-rule.md", "# First rule\n\nbody", time.Now())

	instruction, err := RenderTurnInstruction(roots, projectKey, InstructionOptions{Recall: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(instruction, note) {
		t.Fatalf("note missing before the first consolidation:\n%s", instruction)
	}
}

// Notes are surfaced newest first and capped: a store whose consolidation is
// failing keeps accumulating notes, and that must not grow the turn without
// bound or push out the rules captured most recently.
func TestPendingNotesAreCappedNewestFirst(t *testing.T) {
	roots, projectKey := newInstructionRoots(t)
	root := roots.Scope(Scope{Kind: ScopeProject, Key: projectKey})
	writeSummary(t, root, "- prefers concise updates")
	base := time.Now().Add(-time.Duration(maxPendingNotes+5) * time.Minute)
	var newest, oldest string
	for i := range maxPendingNotes + 5 {
		name := "2026-08-20T09-00-" + string(rune('0'+i/10)) + string(rune('0'+i%10)) + "-lesson-" + string(rune('a'+i)) + ".md"
		path := writeNote(t, root, name, "# Lesson\n\nbody", base.Add(time.Duration(i)*time.Minute))
		if i == 0 {
			oldest = path
		}
		newest = path
	}

	instruction, err := RenderTurnInstruction(roots, projectKey, InstructionOptions{Recall: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(instruction, newest) {
		t.Fatalf("newest note dropped:\n%s", instruction)
	}
	if strings.Contains(instruction, oldest) {
		t.Fatalf("cap not applied, oldest note still injected:\n%s", instruction)
	}
}

func TestDedicatedToolsRequireReadPath(t *testing.T) {
	truth := true
	falsy := false
	for _, test := range []struct {
		name      string
		feature   *bool
		use       *bool
		dedicated *bool
		want      bool
	}{
		{name: "all enabled", feature: &truth, use: &truth, dedicated: &truth, want: true},
		{name: "read disabled", feature: &truth, use: &falsy, dedicated: &truth, want: false},
		{name: "feature disabled", feature: &falsy, use: &truth, dedicated: &truth, want: false},
		{name: "tools disabled", feature: &truth, use: &truth, dedicated: &falsy, want: false},
		{name: "feature nil defaults to true", feature: nil, use: &truth, dedicated: &truth, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &appcfg.Root{}
			cfg.Features.Memories = test.feature
			cfg.Memories.UseMemories = test.use
			cfg.Memories.DedicatedTools = test.dedicated
			if got := DedicatedToolsEnabled(cfg); got != test.want {
				t.Fatalf("DedicatedToolsEnabled=%v want %v", got, test.want)
			}
		})
	}
}

// TestSyncGlobalPhase2InputsGathersEveryProjectsCandidates covers the
// mechanism that lets a cross-project user preference reach every project
// without the user restating it: each project's own global_candidates.md is
// gathered, tagged by project, into the global scope's promotion_candidates.md.
func TestSyncGlobalPhase2InputsGathersEveryProjectsCandidates(t *testing.T) {
	roots, err := ResolveRootsForAgent(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alpha := roots.Scope(Scope{Kind: ScopeProject, Key: "-work-alpha"})
	beta := roots.Scope(Scope{Kind: ScopeProject, Key: "-work-beta"})
	empty := roots.Scope(Scope{Kind: ScopeProject, Key: "-work-empty"})
	for _, root := range []Root{alpha, beta, empty} {
		if err := os.MkdirAll(root.MemoryRoot, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(alpha.MemoryRoot, globalCandidatesFileName),
		[]byte("v1\n\n## Candidates\n\n- always run tests before saying you're done [confidence: high]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beta.MemoryRoot, globalCandidatesFileName),
		[]byte("v1\n\n## Candidates\n\n- answer in Chinese unless asked otherwise [confidence: high]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// empty's global_candidates.md does not exist at all, and a fourth project
	// directory is absent entirely — both must be tolerated, not just an empty file.

	global := roots.Scope(GlobalScope())
	if err := os.MkdirAll(global.MemoryRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	found, err := syncGlobalPhase2Inputs(roots, global.MemoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("candidates were written but the sync reported none")
	}
	body, err := os.ReadFile(filepath.Join(global.MemoryRoot, promotionCandidatesFileName))
	if err != nil {
		t.Fatal(err)
	}
	content := string(body)
	if !strings.Contains(content, "## Project `-work-alpha`") || !strings.Contains(content, "always run tests") {
		t.Fatalf("alpha's candidates missing:\n%s", content)
	}
	if !strings.Contains(content, "## Project `-work-beta`") || !strings.Contains(content, "answer in Chinese") {
		t.Fatalf("beta's candidates missing:\n%s", content)
	}
	if strings.Contains(content, "-work-empty") {
		t.Fatalf("project with no candidates should not appear:\n%s", content)
	}
}

// globalArtifactConsolidator is a ConsolidationRunner stub for the global
// scope: it asserts the promotion_candidates.md content it was handed, then
// writes minimal valid global artifacts.
type globalArtifactConsolidator struct {
	sawPromotionCandidates string
}

func (c *globalArtifactConsolidator) RunMemoryConsolidation(_ context.Context, root Root, prompt ConsolidationPrompt) error {
	instructions := prompt.Instruction
	if instructions == "" {
		return os.ErrInvalid
	}
	if !strings.Contains(instructions, "Global Consolidation") {
		return fmt.Errorf("expected the global consolidation template, got a different prompt")
	}
	body, err := os.ReadFile(filepath.Join(root.MemoryRoot, promotionCandidatesFileName))
	if err != nil {
		return err
	}
	c.sawPromotionCandidates = string(body)
	if err := os.WriteFile(filepath.Join(root.MemoryRoot, "MEMORY.md"), []byte("# Global Memory\n\n## User preferences\n\n- always run tests before saying you're done\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root.MemoryRoot, "memory_summary.md"), []byte("v1\n\n## User preferences\n\n- always run tests before saying you're done\n"), 0o644)
}

// TestGlobalConsolidationConsolidatesCandidatesFromEveryProject runs the
// global scope's phase-2 pass end to end: two projects each declared a
// candidate, and the global pass — which never touches either project's own
// stage-1 rows or MEMORY.md — must see both through promotion_candidates.md
// and fold them into the global scope's own artifacts, which then surface
// through RenderTurnInstruction for a session in a THIRD project that never
// asserted either preference itself.
func TestGlobalConsolidationConsolidatesCandidatesFromEveryProject(t *testing.T) {
	roots, err := ResolveRootsForAgent(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alpha := roots.Scope(Scope{Kind: ScopeProject, Key: "-work-alpha"})
	if err := os.MkdirAll(alpha.MemoryRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alpha.MemoryRoot, globalCandidatesFileName),
		[]byte("v1\n\n## Candidates\n\n- always run tests before saying you're done [confidence: high]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	consolidator := &globalArtifactConsolidator{}
	pipeline := &Pipeline{Store: newTestStore(t), Roots: roots, Consolidator: consolidator}
	settings := appcfg.MemoriesConfig{MaxRawMemoriesForConsolidation: 256, MaxUnusedDays: 30}
	if err := pipeline.runStage2(context.Background(), GlobalScope(), settings); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(consolidator.sawPromotionCandidates, "-work-alpha") {
		t.Fatalf("global pass did not see alpha's candidate:\n%s", consolidator.sawPromotionCandidates)
	}

	// A different project's session, which never asserted this preference
	// itself, still recalls it — that is the point of promotion.
	instruction, err := RenderTurnInstruction(roots, "-work-gamma", InstructionOptions{Recall: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(instruction, "always run tests before saying you're done") {
		t.Fatalf("promoted preference did not reach an unrelated project's turn:\n%s", instruction)
	}
}

type artifactConsolidator struct{ called bool }

func (c *artifactConsolidator) RunMemoryConsolidation(_ context.Context, root Root, prompt ConsolidationPrompt) error {
	c.called = true
	if prompt.Instruction == "" {
		return os.ErrInvalid
	}
	if err := os.WriteFile(filepath.Join(root.MemoryRoot, "MEMORY.md"), []byte("# Memory\n\nDurable.\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root.MemoryRoot, "memory_summary.md"), []byte("v1\n\nDurable summary.\n"), 0o644)
}

func phase2Pipeline(t *testing.T, consolidator ConsolidationRunner) (*Pipeline, *Store, Scope) {
	t.Helper()
	store := newTestStore(t)
	now := time.Now().Unix()
	insertMemoryThread(t, store, "thread", ThreadMemoryEnabled, SessionSourceTUI, now-100)
	if err := store.UpsertStage1Output(context.Background(), Stage1Output{ThreadID: "thread", ProjectKey: testProjectKey, SourceUpdatedAt: now - 100, RawMemory: "raw", RolloutSummary: "summary"}); err != nil {
		t.Fatal(err)
	}
	scope := Scope{Kind: ScopeProject, Key: testProjectKey}
	if err := store.EnqueueGlobalPhase2(context.Background(), scope, now); err != nil {
		t.Fatal(err)
	}
	roots, err := ResolveRootsForAgent(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Pipeline{Store: store, Roots: roots, SessionProjectKey: testProjectKey, Consolidator: consolidator}, store, scope
}

func TestRunStage2RequiresConsolidator(t *testing.T) {
	pipeline, _, scope := phase2Pipeline(t, nil)
	if err := pipeline.runStage2(context.Background(), scope, appcfg.MemoriesConfig{MaxRawMemoriesForConsolidation: 256, MaxUnusedDays: 30}); err == nil {
		t.Fatal("missing consolidator accepted")
	}
}

func TestRunStage2ConsolidatesAndCommitsBaseline(t *testing.T) {
	consolidator := &artifactConsolidator{}
	pipeline, store, scope := phase2Pipeline(t, consolidator)
	root := pipeline.Roots.Scope(scope)
	settings := appcfg.MemoriesConfig{MaxRawMemoriesForConsolidation: 256, MaxUnusedDays: 30}
	if err := pipeline.runStage2(context.Background(), scope, settings); err != nil {
		t.Fatal(err)
	}
	if !consolidator.called {
		t.Fatal("consolidator not called")
	}
	if err := validateConsolidationArtifacts(root.MemoryRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root.MemoryRoot, phase2WorkspaceDiffFile)); !os.IsNotExist(err) {
		t.Fatalf("workspace diff remains: %v", err)
	}
	_, changed, err := memoryWorkspaceDiff(context.Background(), root.MemoryRoot)
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	rows, err := store.SelectStage1ForPhase2(context.Background(), testProjectKey, 10, 30)
	if err != nil || len(rows) != 1 || !rows[0].SelectedForPhase2 {
		t.Fatalf("rows=%#v err=%v", rows, err)
	}
}

// capturingConsolidator stands in for the real pass while a turn captures a
// memory note in parallel: the note lands after the pass snapshotted its diff.
type capturingConsolidator struct {
	artifacts artifactConsolidator
	filename  string
	notePath  string
}

func (c *capturingConsolidator) RunMemoryConsolidation(ctx context.Context, root Root, prompt ConsolidationPrompt) error {
	if err := c.artifacts.RunMemoryConsolidation(ctx, root, prompt); err != nil {
		return err
	}
	path, err := (Backend{Root: root.MemoryRoot}).AddAdHocNote(AdHocNoteScopeProject, c.filename, "# Root cause only\n\nFix causes, not symptoms.\n")
	c.notePath = path
	return err
}

// A turn can capture a note while a consolidation pass is running. That pass
// consolidated a diff snapshotted before the note existed, so committing the
// note into the fresh baseline would hide it from every later pass and the rule
// it carries would never reach MEMORY.md.
func TestNotesCapturedDuringConsolidationStayPending(t *testing.T) {
	consolidator := &capturingConsolidator{filename: "2026-08-20T09-00-00-root-cause-only.md"}
	pipeline, _, scope := phase2Pipeline(t, consolidator)
	root := pipeline.Roots.Scope(scope)
	settings := appcfg.MemoriesConfig{MaxRawMemoriesForConsolidation: 256, MaxUnusedDays: 30}
	if err := pipeline.runStage2(context.Background(), scope, settings); err != nil {
		t.Fatal(err)
	}

	diff, changed, err := memoryWorkspaceDiff(context.Background(), root.MemoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || !strings.Contains(diff, consolidator.notePath) {
		t.Fatalf("note captured mid-pass was buried in the baseline: changed=%v diff=%s", changed, diff)
	}

	instruction, err := RenderTurnInstruction(pipeline.Roots, testProjectKey, InstructionOptions{Recall: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(instruction, consolidator.notePath) || !strings.Contains(instruction, "Fix causes, not symptoms.") {
		t.Fatalf("note captured mid-pass is not surfaced to later turns:\n%s", instruction)
	}
}

var _ ConsolidationRunner = (*artifactConsolidator)(nil)
var _ ConsolidationRunner = (*capturingConsolidator)(nil)

func TestExternalContextPollutesSelectedThreadAndEnqueuesForgetting(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	now := time.Now().Unix()
	insertMemoryThread(t, store, "thread", ThreadMemoryEnabled, SessionSourceTUI, now-100)
	if err := store.UpsertStage1Output(ctx, Stage1Output{ThreadID: "thread", ProjectKey: testProjectKey, SourceUpdatedAt: now - 100, RawMemory: "raw"}); err != nil {
		t.Fatal(err)
	}
	scope := Scope{Kind: ScopeProject, Key: testProjectKey}
	if err := store.EnqueueGlobalPhase2(ctx, scope, now); err != nil {
		t.Fatal(err)
	}
	claim, err := store.TryClaimGlobalPhase2(ctx, scope)
	if err != nil || claim.Outcome != Phase2Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	if updated, err := store.MarkGlobalPhase2Succeeded(ctx, scope, claim.OwnershipToken, now, []Stage1Output{{ThreadID: "thread", ProjectKey: testProjectKey, SourceUpdatedAt: now - 100}}); err != nil || !updated {
		t.Fatalf("selection updated=%v err=%v", updated, err)
	}
	enabled := true
	cfg := &appcfg.Root{Features: appcfg.FeaturesSection{Memories: appcfg.BoolPtr(true)}, Memories: appcfg.MemorySection{DisableOnExternalContext: &enabled}}
	forget, err := MarkPollutedByExternalContext(ctx, cfg, store, "thread")
	if err != nil || !forget {
		t.Fatalf("forget=%v err=%v", forget, err)
	}
	var mode, status string
	if err := store.DB.QueryRowContext(ctx, `SELECT memory_mode FROM fb_sessions WHERE id='thread'`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRowContext(ctx, `SELECT status FROM fb_memory_jobs WHERE kind=? AND job_key=?`, JobKindConsolidate, consolidateJobKey(store.AgentID(), scope)).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if mode != threadMemoryPolluted || status != jobStatusPending {
		t.Fatalf("mode=%q status=%q", mode, status)
	}
}

// insertProjectThread inserts one session belonging to a specific project, for
// the tests below that need two projects under ONE tenant. The shared helper
// insertMemoryThread puts every thread in the same project, which is what most
// tests want and exactly what these must not use.
func insertProjectThread(t *testing.T, store *Store, id, cwd string, updatedAt int64) {
	t.Helper()
	_, err := store.DB.ExecContext(context.Background(), `INSERT INTO fb_sessions(
		id,agent_id,title,updated_at,created_at,memory_mode,memory_source,cwd,git_branch
	) VALUES(?,?,?,?,?,?,?,?,?)`, id, store.AgentID(), id, updatedAt, updatedAt-1,
		ThreadMemoryEnabled, SessionSourceTUI, cwd, "main")
	if err != nil {
		t.Fatalf("insert thread %s: %v", id, err)
	}
}

// TestPhase2InputNeverCrossesProjects is the regression test for the reported
// bug: one tenant working in two projects had both projects' raw memories fed
// into a single consolidation, so a Java project's memory could be written out
// of a Go project's sessions. Consolidation input must be filtered by project,
// not just by tenant.
func TestPhase2InputNeverCrossesProjects(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().Unix()
	javaKey := ProjectKey("/work/java-service")
	goKey := ProjectKey("/work/go-service")

	insertProjectThread(t, store, "java-thread", "/work/java-service", now-100)
	insertProjectThread(t, store, "go-thread", "/work/go-service", now-100)
	if err := store.UpsertStage1Output(ctx, Stage1Output{ThreadID: "java-thread", ProjectKey: javaKey, SourceUpdatedAt: now - 100, RawMemory: "use maven wrapper"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertStage1Output(ctx, Stage1Output{ThreadID: "go-thread", ProjectKey: goKey, SourceUpdatedAt: now - 100, RawMemory: "use go test ./..."}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ key, wantThread, wantMemory, forbidden string }{
		{javaKey, "java-thread", "use maven wrapper", "go test"},
		{goKey, "go-thread", "use go test ./...", "maven"},
	} {
		rows, err := store.SelectStage1ForPhase2(ctx, tc.key, 10, 30)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].ThreadID != tc.wantThread || rows[0].RawMemory != tc.wantMemory {
			t.Fatalf("%s consolidates %#v, want only %s", tc.key, rows, tc.wantThread)
		}
		for _, row := range rows {
			if strings.Contains(row.RawMemory, tc.forbidden) {
				t.Fatalf("%s saw the other project's memory: %q", tc.key, row.RawMemory)
			}
		}
	}
}

// TestPhase2SelectionIsClearedPerProject covers the write half of the same
// boundary: recording project A's phase-2 selection must not clear project B's,
// which is what a tenant-wide clear would do — B's in-flight consolidation
// would silently lose the inputs it was working from.
func TestPhase2SelectionIsClearedPerProject(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().Unix()
	alphaKey := ProjectKey("/work/alpha")
	betaKey := ProjectKey("/work/beta")
	insertProjectThread(t, store, "alpha-thread", "/work/alpha", now-100)
	insertProjectThread(t, store, "beta-thread", "/work/beta", now-100)
	for _, row := range []Stage1Output{
		{ThreadID: "alpha-thread", ProjectKey: alphaKey, SourceUpdatedAt: now - 100, RawMemory: "alpha"},
		{ThreadID: "beta-thread", ProjectKey: betaKey, SourceUpdatedAt: now - 100, RawMemory: "beta"},
	} {
		if err := store.UpsertStage1Output(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	alpha := Scope{Kind: ScopeProject, Key: alphaKey}
	beta := Scope{Kind: ScopeProject, Key: betaKey}

	// Beta records its selection first, then alpha records its own.
	for _, tc := range []struct {
		scope  Scope
		thread string
	}{{beta, "beta-thread"}, {alpha, "alpha-thread"}} {
		claim, err := store.TryClaimGlobalPhase2(ctx, tc.scope)
		if err != nil || claim.Outcome != Phase2Claimed {
			t.Fatalf("%s claim=%#v err=%v", tc.scope.ID(), claim, err)
		}
		selected := []Stage1Output{{ThreadID: tc.thread, ProjectKey: tc.scope.Key, SourceUpdatedAt: now - 100}}
		if updated, err := store.MarkGlobalPhase2Succeeded(ctx, tc.scope, claim.OwnershipToken, now, selected); err != nil || !updated {
			t.Fatalf("%s succeeded=%v err=%v", tc.scope.ID(), updated, err)
		}
	}

	for thread := range map[string]struct{}{"alpha-thread": {}, "beta-thread": {}} {
		var selected int
		if err := store.DB.QueryRowContext(ctx, `SELECT selected_for_phase2 FROM fb_memory_stage1_outputs WHERE thread_id=?`, thread).Scan(&selected); err != nil {
			t.Fatal(err)
		}
		if selected != 1 {
			t.Fatalf("%s selection was cleared by another project's consolidation", thread)
		}
	}
}

// TestPhase2JobsAreIndependentPerScope keeps one project's consolidation from
// gating another's: they must not share a lease, and one scope's cooldown must
// not look like a lock the other has to wait behind.
func TestPhase2JobsAreIndependentPerScope(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	alpha := Scope{Kind: ScopeProject, Key: ProjectKey("/work/alpha")}
	beta := Scope{Kind: ScopeProject, Key: ProjectKey("/work/beta")}

	alphaClaim, err := store.TryClaimGlobalPhase2(ctx, alpha)
	if err != nil || alphaClaim.Outcome != Phase2Claimed {
		t.Fatalf("alpha claim=%#v err=%v", alphaClaim, err)
	}
	// Alpha holds a running lease; beta and global must still be claimable.
	for _, scope := range []Scope{beta, GlobalScope()} {
		claim, err := store.TryClaimGlobalPhase2(ctx, scope)
		if err != nil || claim.Outcome != Phase2Claimed {
			t.Fatalf("%s claim=%#v err=%v, blocked by alpha's lease", scope.ID(), claim, err)
		}
	}
	// A second claim on alpha itself is still refused: the per-scope lock works.
	again, err := store.TryClaimGlobalPhase2(ctx, alpha)
	if err != nil || again.Outcome != Phase2Running {
		t.Fatalf("alpha second claim=%#v err=%v", again, err)
	}
}

// TestTurnInstructionRecallsOnlyTheSessionsProject is the read-path half of the
// reported bug: the summary injected ahead of every turn is what put another
// stack's memory in front of the model, so a session in one project must never
// see another project's summary text.
func TestTurnInstructionRecallsOnlyTheSessionsProject(t *testing.T) {
	roots, err := ResolveRootsForAgent(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	javaKey := ProjectKey("/work/java-service")
	goKey := ProjectKey("/work/go-service")
	for key, summary := range map[string]string{
		javaKey: "v1\n\n- build with the maven wrapper",
		goKey:   "v1\n\n- build with go build ./...",
	} {
		root := roots.Scope(Scope{Kind: ScopeProject, Key: key})
		if err := os.MkdirAll(root.MemoryRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		writeSummary(t, root, summary)
	}
	globalRoot := roots.Scope(GlobalScope())
	if err := os.MkdirAll(globalRoot.MemoryRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSummary(t, globalRoot, "v1\n\n- always run tests before saying you're done")

	instruction, err := RenderTurnInstruction(roots, javaKey, InstructionOptions{Recall: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(instruction, "maven wrapper") {
		t.Fatalf("the session's own project memory is missing:\n%s", instruction)
	}
	if strings.Contains(instruction, "go build ./...") {
		t.Fatalf("another project's memory reached the turn:\n%s", instruction)
	}
	// The global scope is the one thing that is meant to cross projects.
	if !strings.Contains(instruction, "always run tests before saying you're done") {
		t.Fatalf("global preferences missing:\n%s", instruction)
	}
	// And the other project's memory root must not even be named, or the model
	// would be told where to go looking for it.
	otherRoot := roots.Scope(Scope{Kind: ScopeProject, Key: goKey}).MemoryRoot
	if strings.Contains(instruction, otherRoot) {
		t.Fatalf("another project's memory path was advertised:\n%s", instruction)
	}
}

// TestSessionWithoutAProjectGetsGlobalOnly pins the deliberate absence of a
// fallback bucket: a session with no launch directory has no project identity,
// so it recalls and captures global only rather than sharing an "unscoped"
// folder with every other such session — which would be the same cross-project
// mixing under a different name.
func TestSessionWithoutAProjectGetsGlobalOnly(t *testing.T) {
	roots, err := ResolveRootsForAgent(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ProjectScopeForCwd(""); ok {
		t.Fatal("a blank cwd resolved to a project scope")
	}
	if _, ok := ProjectScope("   "); ok {
		t.Fatal("a blank project key resolved to a project scope")
	}
	globalRoot := roots.Scope(GlobalScope())
	if err := os.MkdirAll(globalRoot.MemoryRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSummary(t, globalRoot, "v1\n\n- answer in the user's own language")

	instruction, err := RenderTurnInstruction(roots, "", InstructionOptions{Recall: true, Capture: true, DedicatedTools: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(instruction, "answer in the user's own language") {
		t.Fatalf("global memory missing for a project-less session:\n%s", instruction)
	}
	if strings.Contains(instruction, "{{") {
		t.Fatalf("unrendered placeholder:\n%s", instruction)
	}
	if strings.Contains(instruction, filepath.Join(roots.Base(), "projects")) {
		t.Fatalf("a project path was named for a session that has no project:\n%s", instruction)
	}
	// The capture half must not tell this session to use the project scope: the
	// backend refuses it, so every capture it attempted would fail.
	if !strings.Contains(instruction, `passing `+"`scope`"+` "global"`) {
		t.Fatalf("project-less capture guidance does not name the global scope:\n%s", instruction)
	}
}

// TestClaimSkipsThreadsWithNoProjectIdentity keeps a session with no launch
// directory out of the extraction queue entirely. Claiming it would burn an
// extraction call on a thread whose memory has nowhere to be stored, and leave
// a claimed job that can never complete.
func TestClaimSkipsThreadsWithNoProjectIdentity(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().Unix()
	insertProjectThread(t, store, "with-project", "/work/alpha", now-7*3600)
	insertProjectThread(t, store, "no-project", "", now-7*3600)

	claims, err := store.ClaimStage1JobsForStartup(ctx, "current", 10, 6, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].Thread.ThreadID != "with-project" {
		t.Fatalf("claims=%#v, want only the thread that has a project", threadIDsOf(claims))
	}
	if claims[0].Thread.ProjectKey != ProjectKey("/work/alpha") {
		t.Fatalf("claim carries project key %q", claims[0].Thread.ProjectKey)
	}
	var jobs int
	if err := store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_memory_jobs WHERE kind=? AND job_key=?`, JobKindStage1, "no-project").Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("a thread with no project was claimed anyway: %d job rows", jobs)
	}
}

// TestScopeDirectoriesStayWithinTheirOwnFolder guards the path arithmetic every
// other guarantee rests on: a project scope must resolve strictly inside
// memories/projects/<key>, and the global scope must be somewhere else entirely.
// An empty project key is the dangerous case — "projects/" + "" joins to the
// projects parent itself — which is why ProjectScope refuses to build one.
func TestScopeDirectoriesStayWithinTheirOwnFolder(t *testing.T) {
	roots, err := ResolveRootsForAgent(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projectsParent := filepath.Join(roots.Base(), "projects")
	alpha := roots.Scope(Scope{Kind: ScopeProject, Key: ProjectKey("/work/alpha")})
	beta := roots.Scope(Scope{Kind: ScopeProject, Key: ProjectKey("/work/beta")})
	global := roots.Scope(GlobalScope())

	for _, root := range []Root{alpha, beta} {
		if filepath.Dir(root.MemoryRoot) != projectsParent {
			t.Fatalf("project memory root %s is not a direct child of %s", root.MemoryRoot, projectsParent)
		}
		if root.MemoryRoot == projectsParent {
			t.Fatalf("project scope collapsed onto the projects parent: %s", root.MemoryRoot)
		}
	}
	if alpha.MemoryRoot == beta.MemoryRoot || alpha.stateScopeRoot() == beta.stateScopeRoot() {
		t.Fatalf("two projects share a directory: %s / %s", alpha.MemoryRoot, beta.MemoryRoot)
	}
	if global.MemoryRoot == projectsParent || strings.HasPrefix(global.MemoryRoot, projectsParent+string(filepath.Separator)) {
		t.Fatalf("global scope resolved inside the projects tree: %s", global.MemoryRoot)
	}
	if global.stateScopeRoot() == alpha.stateScopeRoot() {
		t.Fatalf("global and project state collide: %s", global.stateScopeRoot())
	}
}

// TestProjectKeyEncodesTheWholeRoot pins the one project identity every
// per-project directory in the workspace is named after: the absolute project
// root with its separators turned into dashes.
//
// The whole root is encoded on purpose. A key built from the base name alone
// gives two checkouts that happen to share a name ("api" under two different
// parents) the same directory — which is the cross-project memory pollution
// this identity exists to prevent, in miniature.
func TestProjectKeyEncodesTheWholeRoot(t *testing.T) {
	home := realTempDir(t)
	first := filepath.Join(home, "work", "api")
	second := filepath.Join(home, "oss", "api")
	for _, dir := range []string{first, second} {
		mkGitRepo(t, dir)
	}

	firstKey, secondKey := ProjectKey(first), ProjectKey(second)
	if want := strings.ReplaceAll(first, string(filepath.Separator), "-"); firstKey != want {
		t.Fatalf("ProjectKey(%q) = %q, want %q", first, firstKey, want)
	}
	if firstKey == secondKey {
		t.Fatalf("same-named projects share key %q", firstKey)
	}
}

// TestProjectKeyResolvesFromAnySubdirectory covers the everyday case: a session
// started deep inside a repository belongs to the repository, not to the
// directory it happened to launch from.
func TestProjectKeyResolvesFromAnySubdirectory(t *testing.T) {
	root := filepath.Join(realTempDir(t), "repo")
	mkGitRepo(t, root)
	nested := filepath.Join(root, "internal", "memories")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := ProjectKey(nested), ProjectKey(root); got != want {
		t.Fatalf("ProjectKey(nested) = %q, want the repository key %q", got, want)
	}
}

// TestProjectKeyResolvesWorktreesToTheirMainCheckout covers linked worktrees.
// Every worktree of a repository is the same project, so they must share one
// memory folder; otherwise each new worktree starts with an empty memory and
// pays for its own consolidation pass.
func TestProjectKeyResolvesWorktreesToTheirMainCheckout(t *testing.T) {
	base := realTempDir(t)
	main := filepath.Join(base, "repo")
	mkGitRepo(t, main)
	worktree := filepath.Join(base, "repo-feature")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(main, ".git", "worktrees", "feature")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := ProjectKey(worktree), ProjectKey(main); got != want {
		t.Fatalf("ProjectKey(worktree) = %q, want the main checkout key %q", got, want)
	}
}

// TestProjectKeyIsEmptyWithoutACwd states the contract callers depend on: a
// session with no working directory has no project identity. Callers must turn
// project-scoped behaviour off for it rather than invent a shared bucket, which
// would be the mixed folder this design removes.
func TestProjectKeyIsEmptyWithoutACwd(t *testing.T) {
	if got := ProjectKey("   "); got != "" {
		t.Fatalf("ProjectKey(blank) = %q, want empty", got)
	}
}

// realTempDir returns a symlink-free temp dir, because ProjectRoot evaluates
// symlinks and macOS hands out /var/folders paths that are symlinks to
// /private/var.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func mkGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// twoTenantStores returns two stores over ONE state database, the way two
// primary agents actually share it. Isolation has to come from the queries, not
// from separate files.
func twoTenantStores(t *testing.T) (*Store, *Store) {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open test state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db, "acme"), NewStore(db, "globex")
}

// TestStage1ClaimNeverCrossesTenants is the regression test for the leak that
// the per-agent memory directories alone did not close: consolidation reads its
// input from the shared session table, so without a tenant filter one agent
// would extract memories from another agent's conversations and write them into
// its own memory files.
func TestStage1ClaimNeverCrossesTenants(t *testing.T) {
	acme, globex := twoTenantStores(t)
	ctx := context.Background()
	now := time.Now().Unix()

	insertMemoryThreadForAgent(t, acme, "acme", "acme-thread", ThreadMemoryEnabled, SessionSourceTUI, now-7*3600)
	insertMemoryThreadForAgent(t, globex, "globex", "globex-thread", ThreadMemoryEnabled, SessionSourceTUI, now-7*3600)

	for _, tc := range []struct {
		store *Store
		want  string
	}{
		{acme, "acme-thread"},
		{globex, "globex-thread"},
	} {
		claims, err := acmeClaims(ctx, tc.store)
		if err != nil {
			t.Fatalf("%s claim: %v", tc.store.AgentID(), err)
		}
		if len(claims) != 1 || claims[0].Thread.ThreadID != tc.want {
			t.Fatalf("%s claimed %#v, want only %s", tc.store.AgentID(), threadIDsOf(claims), tc.want)
		}
	}
}

func acmeClaims(ctx context.Context, store *Store) ([]Stage1JobClaim, error) {
	return store.ClaimStage1JobsForStartup(ctx, "current", 10, 6, 10)
}

func threadIDsOf(claims []Stage1JobClaim) []string {
	out := make([]string, 0, len(claims))
	for _, claim := range claims {
		out = append(out, claim.Thread.ThreadID)
	}
	return out
}

// TestStage1OutputsAndConsolidationAreTenantScoped covers the rest of the
// pipeline's shared state: the extracted outputs, the phase-two selection each
// agent consolidates from, and the consolidation job row itself.
func TestStage1OutputsAndConsolidationAreTenantScoped(t *testing.T) {
	acme, globex := twoTenantStores(t)
	ctx := context.Background()
	now := time.Now().Unix()

	insertMemoryThreadForAgent(t, acme, "acme", "acme-thread", ThreadMemoryEnabled, SessionSourceTUI, now-100)
	insertMemoryThreadForAgent(t, globex, "globex", "globex-thread", ThreadMemoryEnabled, SessionSourceTUI, now-100)
	if err := acme.UpsertStage1Output(ctx, Stage1Output{ThreadID: "acme-thread", ProjectKey: testProjectKey, SourceUpdatedAt: now - 100, RawMemory: "acme secret"}); err != nil {
		t.Fatal(err)
	}
	if err := globex.UpsertStage1Output(ctx, Stage1Output{ThreadID: "globex-thread", ProjectKey: testProjectKey, SourceUpdatedAt: now - 100, RawMemory: "globex secret"}); err != nil {
		t.Fatal(err)
	}

	rows, err := globex.SelectStage1ForPhase2(ctx, testProjectKey, 10, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ThreadID != "globex-thread" {
		t.Fatalf("globex consolidates %#v; another tenant's extraction reached it", rows)
	}

	// Each tenant holds its own consolidation lock: one agent consolidating
	// must not look like a lock the other has to wait behind.
	scope := Scope{Kind: ScopeProject, Key: testProjectKey}
	acmeClaim, err := acme.TryClaimGlobalPhase2(ctx, scope)
	if err != nil || acmeClaim.Outcome != Phase2Claimed {
		t.Fatalf("acme phase2 claim=%#v err=%v", acmeClaim, err)
	}
	globexClaim, err := globex.TryClaimGlobalPhase2(ctx, scope)
	if err != nil || globexClaim.Outcome != Phase2Claimed {
		t.Fatalf("globex phase2 claim=%#v err=%v, blocked by acme's lock", globexClaim, err)
	}

	// Resetting one tenant clears its rows and leaves the other's intact.
	if err := acme.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	acmeStatus, err := acme.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if acmeStatus.Stage1Count != 0 || acmeStatus.JobCount != 0 {
		t.Fatalf("acme after reset = %#v, want empty", acmeStatus)
	}
	globexStatus, err := globex.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if globexStatus.Stage1Count != 1 || globexStatus.JobCount == 0 {
		t.Fatalf("globex after acme's reset = %#v, want its own rows kept", globexStatus)
	}
}

// TestThreadMemoryModeIsTenantScoped stops one agent from toggling memory on
// another agent's conversation, which would enlist it into that agent's
// pipeline.
func TestThreadMemoryModeIsTenantScoped(t *testing.T) {
	acme, globex := twoTenantStores(t)
	ctx := context.Background()
	now := time.Now().Unix()
	insertMemoryThreadForAgent(t, acme, "acme", "acme-thread", ThreadMemoryDisabled, SessionSourceTUI, now-100)

	if err := globex.SetThreadMemoryMode(ctx, "acme-thread", ThreadMemoryEnabled); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := acme.DB.QueryRowContext(ctx, `SELECT memory_mode FROM fb_sessions WHERE id=?`, "acme-thread").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != ThreadMemoryDisabled {
		t.Fatalf("memory_mode=%q; another tenant enabled memory on this session", mode)
	}
}

// TestUnboundStoreRefusesToTouchMemories keeps a store that never resolved its
// primary agent from falling back to seeing every tenant's rows.
func TestUnboundStoreRefusesToTouchMemories(t *testing.T) {
	acme, _ := twoTenantStores(t)
	unbound := NewStore(acme.DB, "  ")
	ctx := context.Background()
	now := time.Now().Unix()
	insertMemoryThreadForAgent(t, acme, "acme", "acme-thread", ThreadMemoryEnabled, SessionSourceTUI, now-7*3600)

	if _, err := unbound.ClaimStage1JobsForStartup(ctx, "current", 10, 6, 10); err == nil {
		t.Fatal("unbound store claimed jobs instead of refusing")
	}
	if err := unbound.Reset(ctx); err == nil {
		t.Fatal("unbound store reset memories instead of refusing")
	}
	if _, err := unbound.Status(ctx); err == nil {
		t.Fatal("unbound store reported status instead of refusing")
	}
}

// TestBindPrimaryAgentFollowsTheActiveTenant covers the switch: the surfaces
// share one store, so rebinding it is what makes rows written after a switch
// belong to the agent that is now active.
func TestBindPrimaryAgentFollowsTheActiveTenant(t *testing.T) {
	acme, globex := twoTenantStores(t)
	ctx := context.Background()
	now := time.Now().Unix()
	insertMemoryThreadForAgent(t, globex, "globex", "globex-thread", ThreadMemoryEnabled, SessionSourceTUI, now-7*3600)

	acme.BindPrimaryAgent("globex")
	if acme.AgentID() != "globex" {
		t.Fatalf("AgentID=%q after rebinding", acme.AgentID())
	}
	claims, err := acmeClaims(ctx, acme)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].Thread.ThreadID != "globex-thread" {
		t.Fatalf("rebound store claimed %#v", threadIDsOf(claims))
	}
}

// TestRenderTurnInstructionProjectOnlyRecall pins the recall boundary a
// project's memory_scope=project_only sets: its sessions recall their own
// project scope and never the cross-project global one.
func TestRenderTurnInstructionProjectOnlyRecall(t *testing.T) {
	ws := t.TempDir()
	global := filepath.Join(ws, "memories", "global")
	proj := filepath.Join(ws, "memories", "projects", "-p-one")
	for _, d := range []string{global, proj} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(global, "memory_summary.md"), []byte("GLOBAL MARKER"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "memory_summary.md"), []byte("PROJECT MARKER"), 0o644); err != nil {
		t.Fatal(err)
	}
	roots, err := ResolveRootsForAgent(ws)
	if err != nil {
		t.Fatal(err)
	}
	opts := InstructionOptions{Recall: true, DedicatedTools: true}

	shared, err := RenderTurnInstruction(roots, "-p-one", opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(shared, "PROJECT MARKER") || !strings.Contains(shared, "GLOBAL MARKER") {
		t.Fatalf("shared scope should recall both:\n%s", shared)
	}

	opts.ProjectOnly = true
	projectOnly, err := RenderTurnInstruction(roots, "-p-one", opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(projectOnly, "PROJECT MARKER") {
		t.Fatalf("project_only should still recall its own scope")
	}
	if strings.Contains(projectOnly, "GLOBAL MARKER") {
		t.Fatalf("project_only must not recall the global scope:\n%s", projectOnly)
	}
}
