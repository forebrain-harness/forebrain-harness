package memory

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

type structuredTestLLM struct {
	result   *llm.Result
	spec     llm.StructuredOutputSpec
	messages []llm.Message
}

func (s *structuredTestLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	return nil, nil
}

func (s *structuredTestLLM) ExecuteStructured(_ context.Context, messages []llm.Message, spec llm.StructuredOutputSpec) (*llm.Result, error) {
	s.messages = messages
	s.spec = spec
	return s.result, nil
}

type unstructuredTestLLM struct{}

func (unstructuredTestLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	return nil, nil
}

// staticTranscriptSource serves a fixed transcript, honouring the row cursor an
// extraction resumes from: message i carries row id i+1, so a test can tell an
// incremental read (only the rows after the cursor) from a full one (all of
// them) by what the model was handed.
type staticTranscriptSource struct{ messages []llm.Message }

func (s staticTranscriptSource) ListMemoryTranscriptMessagesAfter(_ context.Context, _ string, afterRowID int64) ([]llm.Message, int64, error) {
	lastRowID := int64(len(s.messages))
	if afterRowID >= lastRowID {
		return nil, afterRowID, nil
	}
	out := make([]llm.Message, 0, lastRowID-afterRowID)
	for index, message := range s.messages {
		if int64(index+1) <= afterRowID {
			continue
		}
		out = append(out, message)
	}
	return out, lastRowID, nil
}

// testRoots resolves a fresh agent Roots for a pipeline test, so runStage1's
// per-thread scope resolution (memories.ProjectKey of claim.Thread.Cwd) always
// has somewhere real, under t.TempDir(), to write.
func testRoots(t *testing.T) Roots {
	t.Helper()
	roots, err := ResolveRootsForAgent(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return roots
}

// The reasoning a stored answer carries back to its model — encrypted items
// included — is not part of the conversation the memory agent reads.
func TestSerializeTranscriptForMemoriesLeavesOutReasoningCarry(t *testing.T) {
	answer := llm.AssistantMessage([]llm.ContentPart{llm.Text("the answer")})
	answer.Name = llm.Prefix + "private chain of thought"
	answer.ReasoningModel = "glm-5.3-flash"
	out, err := serializeTranscriptForMemories([]llm.Message{llm.UserMessage(llm.Text("question")), answer})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "private chain of thought") || strings.Contains(out, llm.Prefix) || strings.Contains(out, "glm-5.3-flash") {
		t.Fatalf("reasoning carry reached the memory transcript: %s", out)
	}
	if !strings.Contains(out, "the answer") {
		t.Fatalf("the answer itself is missing: %s", out)
	}
}

func TestParseStage1IsStrict(t *testing.T) {
	valid := `{"raw_memory":"raw","rollout_summary":"summary","rollout_slug":null}`
	if _, err := parseStage1(valid); err != nil {
		t.Fatalf("valid output rejected: %v", err)
	}
	for _, input := range []string{
		"```json\n" + valid + "\n```",
		"result: " + valid,
		valid + " trailing",
		`{"raw_memory":"raw","rollout_summary":"summary","rollout_slug":null,"extra":true}`,
		`{"raw_memory":"raw","rollout_summary":"summary"}`,
	} {
		if _, err := parseStage1(input); err == nil {
			t.Fatalf("invalid output accepted: %q", input)
		}
	}
}

func TestRunStage1RequiresStructuredOutput(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().Unix()
	insertMemoryThread(t, store, "thread", ThreadMemoryEnabled, SessionSourceTUI, now-100)
	outcome, token, err := store.TryClaimStage1Job(context.Background(), "thread", now-100, 3600, 2)
	if err != nil || outcome != Stage1Claimed {
		t.Fatalf("claim=%q err=%v", outcome, err)
	}
	pipeline := &Pipeline{Store: store, Roots: testRoots(t), Transcripts: staticTranscriptSource{messages: []llm.Message{llm.UserMessage(llm.Text("remember"))}}, ExtractLLM: unstructuredTestLLM{}}
	err = pipeline.runStage1(context.Background(), Stage1JobClaim{Thread: SessionCandidate{ThreadID: "thread", ProjectKey: testProjectKey, UpdatedAt: now - 100, Cwd: testCwd}, OwnershipToken: token})
	if err == nil {
		t.Fatal("unstructured provider accepted")
	}
}

func TestRunStage1FiltersTranscriptAndPersistsStructuredOutput(t *testing.T) {
	now := time.Now().Unix()
	message := llm.AssistantMessage([]llm.ContentPart{llm.Text(`{"raw_memory":"raw","rollout_summary":"summary","rollout_slug":"work"}`)})
	model := &structuredTestLLM{result: &llm.Result{Message: &message}}
	store := newTestStore(t)
	insertMemoryThread(t, store, "thread", ThreadMemoryEnabled, SessionSourceTUI, now-100)
	outcome, token, err := store.TryClaimStage1Job(context.Background(), "thread", now-100, 3600, 2)
	if err != nil || outcome != Stage1Claimed {
		t.Fatalf("claim=%q err=%v", outcome, err)
	}
	roots := testRoots(t)
	pipeline := &Pipeline{
		Store: store,
		Roots: roots,
		Transcripts: staticTranscriptSource{messages: []llm.Message{
			llm.SystemMessage("system secret"),
			{Role: llm.RoleDeveloper, Parts: []llm.ContentPart{llm.Text("developer secret")}},
			{Role: llm.RoleUser, IsMeta: true, Parts: []llm.ContentPart{llm.Text("<forebrain_environment_context>\nproject=/work\n</forebrain_environment_context>")}},
			{Role: llm.RoleUser, IsMeta: true, Parts: []llm.ContentPart{llm.Text("<system-reminder>\ninternal instruction\n</system-reminder>")}},
			llm.UserMessage(llm.Text("remember this sk-12345678901234567890")),
		}},
		ExtractLLM: model,
	}
	claim := Stage1JobClaim{Thread: SessionCandidate{ThreadID: "thread", ProjectKey: testProjectKey, UpdatedAt: now - 100, Cwd: testCwd}, OwnershipToken: token}
	if err := pipeline.runStage1(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if !model.spec.Strict || model.spec.Schema["additionalProperties"] != false {
		t.Fatalf("schema = %#v", model.spec)
	}
	requestText := model.messages[1].TextContent()
	if strings.Contains(requestText, "developer secret") || strings.Contains(requestText, "sk-test-") {
		t.Fatalf("unfiltered transcript = %q", requestText)
	}
	if !strings.Contains(requestText, "system secret") || !strings.Contains(requestText, "project=/work") || !strings.Contains(requestText, "internal instruction") {
		t.Fatalf("required transcript context missing: %q", requestText)
	}
	rows, err := store.SelectStage1ForPhase2(context.Background(), testProjectKey, 10, 30)
	if err != nil || len(rows) != 1 || rows[0].RawMemory != "raw" || rows[0].RolloutSlug.String != "work" {
		t.Fatalf("rows=%#v err=%v", rows, err)
	}
	root := roots.Scope(Scope{Kind: ScopeProject, Key: testProjectKey})
	evidencePath := root.rolloutEvidencePath("thread", now-100)
	evidence, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(evidence), `"type":"session_meta"`) ||
		!strings.Contains(string(evidence), `"type":"message"`) ||
		!strings.Contains(string(evidence), "developer secret") {
		t.Fatalf("evidence = %q", evidence)
	}
	info, err := os.Stat(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("evidence mode=%v", info.Mode().Perm())
	}
}

func TestRunStage1StaleOwnershipCannotChangeEvidenceOrOutput(t *testing.T) {
	now := time.Now().Unix()
	store := newTestStore(t)
	insertMemoryThread(t, store, "thread", ThreadMemoryEnabled, SessionSourceTUI, now-100)
	outcome, _, err := store.TryClaimStage1Job(context.Background(), "thread", now-100, 3600, 2)
	if err != nil || outcome != Stage1Claimed {
		t.Fatalf("claim=%q err=%v", outcome, err)
	}
	root := testRoots(t).Scope(Scope{Kind: ScopeProject, Key: testProjectKey})
	canonical := root.rolloutEvidencePath("thread", now-100)
	older := root.rolloutEvidencePath("older", now-200)
	for path, body := range map[string]string{canonical: "accepted evidence\n", older: "older evidence\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpsertStage1Output(context.Background(), Stage1Output{ThreadID: "thread", ProjectKey: testProjectKey, SourceUpdatedAt: now - 100, RawMemory: "accepted", RolloutSummary: "accepted summary"}); err != nil {
		t.Fatal(err)
	}
	staged, stagedCanonical, err := stageRolloutEvidence(root, SessionCandidate{ThreadID: "thread", UpdatedAt: now - 100}, "stale-token", []llm.Message{llm.UserMessage(llm.Text("stale"))})
	if err != nil {
		t.Fatal(err)
	}
	owned, err := store.CompleteStage1WithEvidence(context.Background(), "thread", testProjectKey, "stale-token", now-100, "stale", "stale summary", nil, 0, staged, stagedCanonical)
	if err != nil || owned {
		t.Fatalf("stale completion owned=%v err=%v", owned, err)
	}
	for path, want := range map[string]string{canonical: "accepted evidence\n", older: "older evidence\n"} {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != want {
			t.Fatalf("evidence %s body=%q err=%v", path, body, err)
		}
	}
	if _, err := os.Lstat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged evidence was not removed: %v", err)
	}
	rows, err := store.SelectStage1ForPhase2(context.Background(), testProjectKey, 10, 30)
	if err != nil || len(rows) != 1 || rows[0].RawMemory != "accepted" || rows[0].RolloutSummary != "accepted summary" {
		t.Fatalf("rows=%#v err=%v", rows, err)
	}
}

func TestRunStage1EmptyFieldMeansNoOutput(t *testing.T) {
	now := time.Now().Unix()
	message := llm.AssistantMessage([]llm.ContentPart{llm.Text(`{"raw_memory":"","rollout_summary":"summary","rollout_slug":null}`)})
	store := newTestStore(t)
	insertMemoryThread(t, store, "thread", ThreadMemoryEnabled, SessionSourceTUI, now-100)
	if err := store.UpsertStage1Output(context.Background(), Stage1Output{ThreadID: "thread", ProjectKey: testProjectKey, SourceUpdatedAt: now - 200, RawMemory: "old"}); err != nil {
		t.Fatal(err)
	}
	outcome, token, err := store.TryClaimStage1Job(context.Background(), "thread", now-100, 3600, 2)
	if err != nil || outcome != Stage1Claimed {
		t.Fatalf("claim=%q err=%v", outcome, err)
	}
	pipeline := &Pipeline{Store: store, Roots: testRoots(t), Transcripts: staticTranscriptSource{}, ExtractLLM: &structuredTestLLM{result: &llm.Result{Message: &message}}}
	if err := pipeline.runStage1(context.Background(), Stage1JobClaim{Thread: SessionCandidate{ThreadID: "thread", ProjectKey: testProjectKey, UpdatedAt: now - 100, Cwd: testCwd}, OwnershipToken: token}); err != nil {
		t.Fatal(err)
	}
	status, err := store.Status(context.Background())
	if err != nil || status.Stage1Count != 0 {
		t.Fatalf("status=%#v err=%v", status, err)
	}
}

// gateLLM records the order stage-1 extractions start and finish, and can
// hold the first call until the test releases it.
type gateLLM struct {
	threads []string
	mu      sync.Mutex
	events  []string
	hold    chan struct{}
}

func (g *gateLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	return nil, nil
}

func (g *gateLLM) ExecuteStructured(_ context.Context, messages []llm.Message, _ llm.StructuredOutputSpec) (*llm.Result, error) {
	var text strings.Builder
	for _, m := range messages {
		text.WriteString(m.TextContent())
		text.WriteString("\n")
	}
	thread := "unknown"
	for _, candidate := range g.threads {
		if strings.Contains(text.String(), candidate) {
			thread = candidate
			break
		}
	}
	g.mu.Lock()
	g.events = append(g.events, "start "+thread)
	g.mu.Unlock()
	if g.hold != nil {
		<-g.hold
	}
	message := llm.AssistantMessage([]llm.ContentPart{llm.Text(`{"raw_memory":"raw","rollout_summary":"summary","rollout_slug":null}`)})
	result := &llm.Result{Message: &message}
	g.mu.Lock()
	g.events = append(g.events, "finish "+thread)
	g.mu.Unlock()
	return result, nil
}

func (g *gateLLM) snapshot() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.events...)
}

func after(events []string, a, b string) bool {
	posA, posB := -1, -1
	for i, e := range events {
		if e == a {
			posA = i
		}
		if e == b {
			posB = i
		}
	}
	return posA >= 0 && posB >= 0 && posA < posB
}

// identifyingTranscriptSource returns one message naming the thread it was
// asked for, so a recording LLM can tell concurrent extractions apart.
type identifyingTranscriptSource struct{}

func (identifyingTranscriptSource) ListMemoryTranscriptMessagesAfter(_ context.Context, threadID string, afterRowID int64) ([]llm.Message, int64, error) {
	if afterRowID > 0 {
		return nil, afterRowID, nil
	}
	return []llm.Message{llm.UserMessage(llm.Text("transcript of " + threadID))}, 1, nil
}

// The first stage-1 claim must run to completion before any other claim
// starts: its request is what warms the shared extraction system prefix in
// the provider's cache, and launching the fan-out concurrently would have
// every worker pay its own cold miss on those same bytes. The remaining
// claims still run concurrently.
func TestRunStage1JobsWarmsPrefixBeforeFanOut(t *testing.T) {
	now := time.Now().Unix()
	store := newTestStore(t)
	model := &gateLLM{threads: []string{"thread-alpha", "thread-beta", "thread-gamma"}, hold: make(chan struct{})}
	threads := []string{"thread-alpha", "thread-beta", "thread-gamma"}
	var claims []Stage1JobClaim
	for _, id := range threads {
		insertMemoryThread(t, store, id, ThreadMemoryEnabled, SessionSourceTUI, now-100)
		outcome, token, err := store.TryClaimStage1Job(context.Background(), id, now-100, 3600, 8)
		if err != nil || outcome != Stage1Claimed {
			t.Fatalf("claim %s: %q err=%v", id, outcome, err)
		}
		claims = append(claims, Stage1JobClaim{Thread: SessionCandidate{ThreadID: id, ProjectKey: testProjectKey, UpdatedAt: now - 100, Cwd: testCwd}, OwnershipToken: token})
	}

	pipeline := &Pipeline{
		Store:       store,
		Roots:       testRoots(t),
		Transcripts: identifyingTranscriptSource{},
		ExtractLLM:  model,
	}

	done := make(chan struct{})
	go func() {
		pipeline.runStage1Jobs(context.Background(), claims)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for len(model.snapshot()) < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	events := model.snapshot()
	if len(events) != 1 || !strings.HasPrefix(events[0], "start ") {
		t.Fatalf("first claim did not start alone: %v", events)
	}
	// Give a wrongly-concurrent pipeline every chance to betray itself before
	// releasing the gate.
	time.Sleep(100 * time.Millisecond)
	if got := model.snapshot(); len(got) != 1 {
		t.Fatalf("fan-out started before the warm-up claim finished: %v", got)
	}

	close(model.hold)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runStage1Jobs did not finish")
	}
	final := model.snapshot()
	if len(final) != 2*len(threads) {
		t.Fatalf("expected %d events, got %v", 2*len(threads), final)
	}
	for _, other := range threads[1:] {
		if !after(final, "finish "+threads[0], "start "+other) {
			t.Fatalf("%s started before the warm-up claim finished: %v", other, final)
		}
	}
}

// A phase-2 pass is a fresh conversation every time, so the only prefix it can
// be served from cache is one an earlier pass — in any project — already wrote
// there. That works exactly while the instruction is byte-identical, which it
// stops being the moment a project path or an extensions block is rendered into
// it: the first divergence lands a few hundred bytes in and costs the entire
// ~55 KB prompt, on every pass of every project.
func TestConsolidationInstructionIsIdenticalAcrossProjects(t *testing.T) {
	projectA := t.TempDir()
	projectB := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectB, extensionsDir), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, scope := range []Scope{{Kind: ScopeProject, Key: "a"}, {Kind: ScopeGlobal}} {
		first := buildConsolidationPrompt(projectA, scope)
		second := buildConsolidationPrompt(projectB, scope)
		if first.Instruction != second.Instruction {
			t.Fatalf("scope %s: instruction differs between projects", scope.Kind)
		}
		if strings.Contains(first.Instruction, projectA) || strings.Contains(second.Instruction, projectB) {
			t.Fatalf("scope %s: a project path was rendered into the instruction", scope.Kind)
		}
		if !strings.Contains(first.Context, projectA) {
			t.Fatalf("scope %s: the memory root never reached the run context: %q", scope.Kind, first.Context)
		}
		if !strings.Contains(second.Context, filepath.Join(projectB, extensionsDir)) {
			t.Fatalf("scope %s: memory extensions never reached the run context: %q", scope.Kind, second.Context)
		}
	}
}

// ---- migrated from pipeline_workspace_test.go (tests for this file's production code) ----

// pipeline_workspace_test.go pins what ConsolidateWorkspace is: the
// workspace-consolidation entry a migration injects. It prepares the scopes
// and honors the rate-limit guard, consolidates the workspace's own input,
// and neither prunes stage-1 outputs nor claims native rollouts for
// extraction — those are the background pipeline's data, not the
// migration's.

type recordingConsolidator struct {
	mu    sync.Mutex
	roots []Root
}

func (r *recordingConsolidator) RunMemoryConsolidation(_ context.Context, root Root, _ ConsolidationPrompt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.roots = append(r.roots, root)
	return nil
}

func (r *recordingConsolidator) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.roots)
}

// TestConsolidateWorkspaceIntegratesOnlyTheWorkspace runs the workspace pass
// over a store that holds everything the full pipeline would touch: a
// claimable native rollout and a stale stage-1 output. The pass consolidates
// the ad-hoc note the migration just wrote (the recording consolidator sees
// the global scope) and leaves both stage-1 concerns untouched.
func TestConsolidateWorkspaceIntegratesOnlyTheWorkspace(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	now := time.Now().Unix()

	// A native rollout the full Run would claim for stage-1 extraction:
	// memory enabled, TUI-sourced, idle long enough. The extraction LLM is
	// wired and working, so a pass that claimed it would leave a stage-1
	// output row behind.
	insertMemoryThread(t, store, "thread-claimable", ThreadMemoryEnabled, SessionSourceTUI, now-72*3600)
	message := llm.AssistantMessage([]llm.ContentPart{llm.Text(`{"raw_memory":"raw","rollout_summary":"summary","rollout_slug":"work"}`)})
	extract := &structuredTestLLM{result: &llm.Result{Message: &message}}

	// A stage-1 output old and unused enough that the full Run's prune would
	// delete it (last touched months past the 30-day window).
	insertMemoryThread(t, store, "thread-stale", ThreadMemoryEnabled, SessionSourceTUI, now-400*24*3600)
	if err := store.UpsertStage1Output(ctx, Stage1Output{
		ThreadID: "thread-stale", ProjectKey: testProjectKey,
		SourceUpdatedAt: now - 400*24*3600, RawMemory: "stale", RolloutSummary: "stale",
		GeneratedAt: now - 400*24*3600,
	}); err != nil {
		t.Fatal(err)
	}

	roots := testRoots(t)
	consolidator := &recordingConsolidator{}
	pipeline := &Pipeline{
		Store: store, Roots: roots, ExtractLLM: extract, Consolidator: consolidator,
		Cfg: &appcfg.Root{}, SessionProjectKey: "",
	}

	// The migration's own write: an ad-hoc note in the global workspace.
	globalRoot := roots.Scope(GlobalScope())
	notesDir := filepath.Join(globalRoot.MemoryRoot, "extensions", "ad_hoc", "notes")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notesDir, "2026-09-01T00-00-00-migrated.md"), []byte("a note the migration wrote\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := pipeline.ConsolidateWorkspace(ctx); err != nil {
		t.Fatal(err)
	}
	if consolidator.calls() != 1 {
		t.Fatalf("consolidator calls = %d, want exactly the global scope's pass", consolidator.calls())
	}
	if consolidator.roots[0].MemoryRoot != globalRoot.MemoryRoot {
		t.Fatalf("consolidated %q, want the global scope", consolidator.roots[0].MemoryRoot)
	}

	// The claimable rollout was never extracted.
	var claimableRows int
	if err := store.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM fb_memory_stage1_outputs WHERE thread_id='thread-claimable'`).Scan(&claimableRows); err != nil {
		t.Fatal(err)
	}
	if claimableRows != 0 {
		t.Fatalf("workspace pass claimed a native rollout: %d stage-1 rows", claimableRows)
	}
	// The stale output was not pruned.
	var staleRows int
	if err := store.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM fb_memory_stage1_outputs WHERE thread_id='thread-stale'`).Scan(&staleRows); err != nil {
		t.Fatal(err)
	}
	if staleRows != 1 {
		t.Fatalf("workspace pass pruned stage-1 outputs: %d rows", staleRows)
	}
	// Scope preparation ran even for a pass that skips claims.
	if info, err := os.Stat(filepath.Join(globalRoot.MemoryRoot, "extensions", "ad_hoc", "instructions.md")); err != nil || info.IsDir() {
		t.Fatalf("global scope was not prepared: %v", err)
	}
}

// recordingTranscriptSource serves a transcript that grows between passes, with
// message i carrying row id i+1, and records the cursor each read started from.
type recordingTranscriptSource struct {
	messages []llm.Message
	cursors  []int64
}

func (r *recordingTranscriptSource) ListMemoryTranscriptMessagesAfter(_ context.Context, _ string, afterRowID int64) ([]llm.Message, int64, error) {
	r.cursors = append(r.cursors, afterRowID)
	return staticTranscriptSource{messages: r.messages}.ListMemoryTranscriptMessagesAfter(context.Background(), "", afterRowID)
}

// countingStructuredLLM answers with one fixed result and counts its calls.
type countingStructuredLLM struct {
	message llm.Message
	calls   int
	inputs  []string
}

func (c *countingStructuredLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	return nil, nil
}

func (c *countingStructuredLLM) ExecuteStructured(_ context.Context, messages []llm.Message, _ llm.StructuredOutputSpec) (*llm.Result, error) {
	c.calls++
	if len(messages) > 1 {
		c.inputs = append(c.inputs, messages[1].TextContent())
	}
	result := &llm.Result{Message: &c.message}
	return result, nil
}

// stage1IncrementalSetup holds what the incremental tests share: a store with
// one claimable thread, the growing transcript, the counting model and the
// pipeline wired to both.
type stage1IncrementalSetup struct {
	store     *Store
	thread    *recordingTranscriptSource
	model     *countingStructuredLLM
	pipeline  *Pipeline
	claimedAt int64
	token     string
}

func newStage1IncrementalSetup(t *testing.T, reply string, initial ...llm.Message) *stage1IncrementalSetup {
	t.Helper()
	now := time.Now().Unix()
	store := newTestStore(t)
	insertMemoryThread(t, store, "thread", ThreadMemoryEnabled, SessionSourceTUI, now-200)
	thread := &recordingTranscriptSource{messages: initial}
	model := &countingStructuredLLM{message: llm.AssistantMessage([]llm.ContentPart{llm.Text(reply)})}
	setup := &stage1IncrementalSetup{
		store:     store,
		thread:    thread,
		model:     model,
		pipeline:  &Pipeline{Store: store, Roots: testRoots(t), Transcripts: thread, ExtractLLM: model},
		claimedAt: now - 200,
	}
	setup.claim(t)
	return setup
}

// claim takes the thread's next stage-1 claim at its next updated_at, which is
// how a later pass over the same thread starts.
func (s *stage1IncrementalSetup) claim(t *testing.T) {
	t.Helper()
	outcome, token, err := s.store.TryClaimStage1Job(context.Background(), "thread", s.claimedAt, 3600, 2)
	if err != nil || outcome != Stage1Claimed {
		t.Fatalf("claim=%q err=%v", outcome, err)
	}
	s.token = token
}

// run executes the claimed stage-1 pass and then moves the thread's updated_at
// forward, which is what lets the next pass claim it again.
func (s *stage1IncrementalSetup) run(t *testing.T) {
	t.Helper()
	if err := s.pipeline.runStage1(context.Background(), Stage1JobClaim{
		Thread:         SessionCandidate{ThreadID: "thread", ProjectKey: testProjectKey, UpdatedAt: s.claimedAt, Cwd: testCwd},
		OwnershipToken: s.token,
	}); err != nil {
		t.Fatal(err)
	}
}

func (s *stage1IncrementalSetup) stage1Row(t *testing.T) Stage1Output {
	t.Helper()
	row, found, err := s.store.Stage1OutputForThread(context.Background(), "thread")
	if err != nil || !found {
		t.Fatalf("stage-one row found=%v err=%v", found, err)
	}
	return row
}

// TestRunStage1ContinuedExtractionReadsOnlyNewMessages is the core of plan 205
// §3.4: after a full extraction, a later pass over the same thread asks the
// model for the new turns only, hands it the previous result to merge into, and
// stores the merged result in place with the advanced cursor.
func TestRunStage1ContinuedExtractionReadsOnlyNewMessages(t *testing.T) {
	setup := newStage1IncrementalSetup(t, `{"raw_memory":"merged raw","rollout_summary":"merged summary","rollout_slug":"work"}`,
		llm.UserMessage(llm.Text("the first instruction")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("the first answer")}),
	)
	setup.run(t)
	first := setup.stage1Row(t)
	if !first.LastMessageRowID.Valid || first.LastMessageRowID.Int64 != 2 {
		t.Fatalf("cursor after the full extraction = %#v, want 2", first.LastMessageRowID)
	}

	setup.thread.messages = append(setup.thread.messages,
		llm.UserMessage(llm.Text("the second instruction")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("the second answer")}),
		llm.UserMessage(llm.Text("the third instruction")),
	)
	setup.claimedAt++
	setup.claim(t)
	setup.run(t)

	if setup.model.calls != 2 {
		t.Fatalf("model calls = %d, want the full extraction and the continued one", setup.model.calls)
	}
	if got := setup.thread.cursors; len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("read cursors = %v, want a full read then a read after row 2", got)
	}
	second := setup.model.inputs[1]
	for _, old := range []string{"the first instruction", "the first answer"} {
		if strings.Contains(second, old) {
			t.Fatalf("the continued request re-read %q:\n%s", old, second)
		}
	}
	for _, fresh := range []string{"the second instruction", "the second answer", "the third instruction"} {
		if !strings.Contains(second, fresh) {
			t.Fatalf("the continued request is missing %q:\n%s", fresh, second)
		}
	}
	if !strings.Contains(second, "merged raw") || !strings.Contains(second, "merged summary") {
		t.Fatalf("the continued request did not carry the previous result's content:\n%s", second)
	}
	if !strings.Contains(second, "previous extraction") {
		t.Fatalf("the continued request has no previous-extraction block:\n%s", second)
	}

	row := setup.stage1Row(t)
	if row.RawMemory != "merged raw" || row.RolloutSummary != "merged summary" {
		t.Fatalf("row after the continued extraction = %#v, want the merged result in the original row", row)
	}
	if !row.LastMessageRowID.Valid || row.LastMessageRowID.Int64 != 5 {
		t.Fatalf("cursor after the continued extraction = %#v, want 5", row.LastMessageRowID)
	}
}

// TestRunStage1RowWithoutCursorReadsEverything pins the transition: a row
// written before the cursor existed costs one more full read and then becomes
// incremental, instead of being skipped as up to date.
func TestRunStage1RowWithoutCursorReadsEverything(t *testing.T) {
	setup := newStage1IncrementalSetup(t, `{"raw_memory":"fresh raw","rollout_summary":"fresh summary","rollout_slug":null}`,
		llm.UserMessage(llm.Text("an early instruction")),
		llm.UserMessage(llm.Text("a later instruction")),
	)
	if err := setup.store.UpsertStage1Output(context.Background(), Stage1Output{
		ThreadID: "thread", ProjectKey: testProjectKey, SourceUpdatedAt: setup.claimedAt - 100,
		RawMemory: "legacy raw", RolloutSummary: "legacy summary",
	}); err != nil {
		t.Fatal(err)
	}
	setup.run(t)
	if got := setup.thread.cursors; len(got) != 1 || got[0] != 0 {
		t.Fatalf("read cursors = %v, want one full read", got)
	}
	if len(setup.model.inputs) != 1 || strings.Contains(setup.model.inputs[0], "previous extraction") {
		t.Fatalf("a row without a cursor took the continued path:\n%v", setup.model.inputs)
	}
	if !strings.Contains(setup.model.inputs[0], "an early instruction") {
		t.Fatalf("the full read dropped the early messages:\n%s", setup.model.inputs[0])
	}
	row := setup.stage1Row(t)
	if !row.LastMessageRowID.Valid || row.LastMessageRowID.Int64 != 2 {
		t.Fatalf("cursor after the full read = %#v, want 2", row.LastMessageRowID)
	}
}

// TestRunStage1WithNoNewMessagesSkipsTheModel pins the boundary: a re-claimed
// thread that gained nothing costs no model call, and the pass still moves its
// timestamp and cursor forward so the same idle thread is not claimed again.
func TestRunStage1WithNoNewMessagesSkipsTheModel(t *testing.T) {
	setup := newStage1IncrementalSetup(t, `{"raw_memory":"raw","rollout_summary":"summary","rollout_slug":null}`,
		llm.UserMessage(llm.Text("only instruction")),
	)
	setup.run(t)
	before := setup.stage1Row(t)

	setup.claimedAt++
	setup.claim(t)
	setup.run(t)

	if setup.model.calls != 1 {
		t.Fatalf("model calls = %d, want the first pass only", setup.model.calls)
	}
	after := setup.stage1Row(t)
	if after.RawMemory != before.RawMemory || after.RolloutSummary != before.RolloutSummary {
		t.Fatalf("the skipped pass rewrote the result: %#v -> %#v", before, after)
	}
	if after.SourceUpdatedAt != setup.claimedAt {
		t.Fatalf("source_updated_at = %d, want the claim's %d", after.SourceUpdatedAt, setup.claimedAt)
	}
	if !after.LastMessageRowID.Valid || after.LastMessageRowID.Int64 != 1 {
		t.Fatalf("cursor after the skipped pass = %#v, want 1", after.LastMessageRowID)
	}
	outcome, _, err := setup.store.TryClaimStage1Job(context.Background(), "thread", setup.claimedAt, 3600, 2)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != Stage1UpToDate {
		t.Fatalf("claim after the skipped pass = %q, want up to date", outcome)
	}
}

// TestStageOneIncrementalInputCarriesAnEmptyPreviousResult pins the last
// boundary of §3.4: when the thread has no earlier result to merge — the no-op
// pass deleted it — the continued request still carries an empty
// previous-extraction block rather than omitting the section.
func TestStageOneIncrementalInputCarriesAnEmptyPreviousResult(t *testing.T) {
	input := renderStageOneIncrementalInput(SessionCandidate{Cwd: testCwd}, "/evidence/rollout.jsonl",
		renderStageOnePreviousExtraction(Stage1Output{}), "the new messages")
	if !strings.Contains(input, "previous extraction") {
		t.Fatalf("the request has no previous-extraction section:\n%s", input)
	}
	if !strings.Contains(input, `{"raw_memory":"","rollout_summary":"","rollout_slug":null}`) {
		t.Fatalf("the previous-extraction block is not the empty result:\n%s", input)
	}
	if !strings.Contains(input, "the new messages") || !strings.Contains(input, testCwd) || !strings.Contains(input, "/evidence/rollout.jsonl") {
		t.Fatalf("the request lost part of its context:\n%s", input)
	}
}

// TestRunStage1EmptyMergeKeepsTheAccumulatedResult pins what an empty answer
// means on a continued extraction: the model is saying the NEW turns carry
// nothing, not that the earlier extraction never happened. The row keeps its
// content and only its cursors move; deleting it would forget the conversation
// over a quiet stretch.
func TestRunStage1EmptyMergeKeepsTheAccumulatedResult(t *testing.T) {
	setup := newStage1IncrementalSetup(t, `{"raw_memory":"","rollout_summary":"","rollout_slug":null}`,
		llm.UserMessage(llm.Text("the first instruction")),
	)
	if err := setup.store.UpsertStage1Output(context.Background(), Stage1Output{
		ThreadID: "thread", ProjectKey: testProjectKey, SourceUpdatedAt: setup.claimedAt - 100,
		RawMemory: "kept raw", RolloutSummary: "kept summary", LastMessageRowID: sql.NullInt64{Int64: 1, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	setup.thread.messages = append(setup.thread.messages, llm.UserMessage(llm.Text("a later remark")))
	setup.run(t)

	row := setup.stage1Row(t)
	if row.RawMemory != "kept raw" || row.RolloutSummary != "kept summary" {
		t.Fatalf("an empty merge result replaced the accumulated row: %#v", row)
	}
	if !row.LastMessageRowID.Valid || row.LastMessageRowID.Int64 != 2 {
		t.Fatalf("cursor after the empty merge = %#v, want 2", row.LastMessageRowID)
	}
}
