package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

type staticTranscriptSource struct{ messages []llm.Message }

func (s staticTranscriptSource) ListMemoryTranscriptMessages(context.Context, string, int) ([]llm.Message, error) {
	return append([]llm.Message(nil), s.messages...), nil
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
	owned, err := store.CompleteStage1WithEvidence(context.Background(), "thread", testProjectKey, "stale-token", now-100, "stale", "stale summary", nil, staged, stagedCanonical)
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

func (identifyingTranscriptSource) ListMemoryTranscriptMessages(_ context.Context, threadID string, _ int) ([]llm.Message, error) {
	return []llm.Message{llm.UserMessage(llm.Text("transcript of " + threadID))}, nil
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
