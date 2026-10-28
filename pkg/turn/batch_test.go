package turn

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// batchEnv is one batch execution to run turns against: the real stores the
// engine uses, so the admission and the epilogue are exercised end to end
// rather than through a stub that could disagree with them.
type batchEnv struct {
	projects *state.ProjectStore
	sessions *state.SessionStore
	task     state.Task
	exec     state.TaskExecution
	root     string
}

func newBatchEnv(t *testing.T) *batchEnv {
	t.Helper()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := state.NewProjectStore(db, "main")
	sessions := state.NewSessionStore(db, "main")
	root := t.TempDir()
	p, err := projects.Create(ctx, state.CreateProjectInput{
		Name:        "batch",
		Root:        root,
		ProjectType: state.ProjectTypeBatch,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := projects.CreateTask(ctx, state.Task{ProjectID: p.ID, Title: "count rows"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.Ensure(ctx, "s1", "s1"); err != nil {
		t.Fatal(err)
	}
	exec, err := projects.AddTaskExecution(ctx, task.ID, "s1", p.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"inputs", "outputs", ".work"} {
		if err := os.MkdirAll(filepath.Join(exec.Workspace, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &batchEnv{projects: projects, sessions: sessions, task: task, exec: exec, root: root}
}

// recordingTaskStore records the context each store call saw, so a test can
// prove the epilogue ran on a context that outlived the turn's cancellation.
type recordingTaskStore struct {
	*state.ProjectStore
	mu      sync.Mutex
	ctxErrs []error
}

func (r *recordingTaskStore) note(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctxErrs = append(r.ctxErrs, ctx.Err())
}

func (r *recordingTaskStore) scopes() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.ctxErrs...)
}

func (r *recordingTaskStore) MarkExecutionEnded(ctx context.Context, sessionID, status, code, text string) error {
	r.note(ctx)
	return r.ProjectStore.MarkExecutionEnded(ctx, sessionID, status, code, text)
}

type recordingArtifactStore struct {
	*state.SessionStore
	mu      sync.Mutex
	ctxErrs []error
}

func (r *recordingArtifactStore) note(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctxErrs = append(r.ctxErrs, ctx.Err())
}

func (r *recordingArtifactStore) scopes() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.ctxErrs...)
}

func (r *recordingArtifactStore) ListArtifactsPage(ctx context.Context, sessionID string, limit, offset int) ([]state.Artifact, int, error) {
	r.note(ctx)
	return r.SessionStore.ListArtifactsPage(ctx, sessionID, limit, offset)
}

func (r *recordingArtifactStore) AddArtifact(ctx context.Context, a state.Artifact) (state.Artifact, error) {
	r.note(ctx)
	return r.SessionStore.AddArtifact(ctx, a)
}

func TestBatchPromptsGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "batch_prompts.golden.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden map[string]string
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parse golden: %v", err)
	}

	// A fixed task and a placeholder workspace: the golden pins the paragraph
	// itself, not a machine-specific temp path, so each rendering replaces its
	// own workspace with <workspace> before the byte comparison. Nothing else
	// is normalized — the file list, the escaping and the line breaks are
	// compared exactly as the model would read them.
	task := state.Task{ID: "tsk-1", Title: "count rows"}
	render := func(files []string) string {
		workspace := t.TempDir()
		inputs := filepath.Join(workspace, "inputs")
		if err := os.MkdirAll(inputs, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range files {
			if err := os.WriteFile(filepath.Join(inputs, name), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		exec := state.TaskExecution{SessionID: "s1", TaskID: task.ID, Seq: 1, Workspace: workspace}
		return strings.ReplaceAll(BatchTaskContext(task, exec), workspace, "<workspace>")
	}

	if got := render(nil); got != golden["BatchTaskContextEmpty"] {
		t.Fatalf("empty inputs render:\n got %q\nwant %q", got, golden["BatchTaskContextEmpty"])
	}
	escaped := []string{`a & b<1>.csv`, `quote"s'.txt`, "zebra.csv"}
	if got := render(escaped); got != golden["BatchTaskContextSortedAndEscaped"] {
		t.Fatalf("sorted/escaped render:\n got %q\nwant %q", got, golden["BatchTaskContextSortedAndEscaped"])
	}
	// 51 inputs: the paragraph names the first 50 in byte order and says how
	// many it left out.
	truncated := []string{"z.csv"}
	for i := 0; i < 26; i++ {
		truncated = append(truncated, string(rune('a'+i))+".csv", string(rune('a'+i))+"0.csv")
	}
	truncated = truncated[:51]
	if got := render(truncated); got != golden["BatchTaskContextTruncated"] {
		t.Fatalf("truncated render:\n got %q\nwant %q", got, golden["BatchTaskContextTruncated"])
	}

	if BatchContinuePrompt != golden["BatchContinuePrompt"] {
		t.Fatalf("BatchContinuePrompt:\n got %q\nwant %q", BatchContinuePrompt, golden["BatchContinuePrompt"])
	}
}

func TestBatchTaskContextNamesUnreadableInputs(t *testing.T) {
	task := state.Task{ID: "tsk-1"}
	workspace := t.TempDir()
	// inputs as a file rather than a folder: the paragraph must say so rather
	// than claim the task has no input.
	if err := os.WriteFile(filepath.Join(workspace, "inputs"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec := state.TaskExecution{TaskID: task.ID, Seq: 2, Workspace: workspace}
	got := BatchTaskContext(task, exec)
	if !strings.Contains(got, "input_files:\n- none\n") {
		t.Fatalf("a non-directory inputs/ rendered as %q, want an empty list", got)
	}
	if strings.Contains(got, "input_files_error") {
		t.Fatalf("a non-directory inputs/ reported an error: %q", got)
	}
}

func TestAdmitBatchTurnTable(t *testing.T) {
	ctx := context.Background()
	type want struct {
		kind  BatchTurnKind
		input string
		code  string
	}
	tests := []struct {
		name       string
		status     string
		inputSet   bool
		content    string
		attachment bool
		want       want
	}{
		{"pending/no input, content", state.TaskStatusPending, false, "count rows", false, want{kind: BatchTurnFirst, input: "count rows"}},
		{"pending/no input, attachment only", state.TaskStatusPending, false, "", true, want{kind: BatchTurnFirst}},
		{"pending/no input, nothing", state.TaskStatusPending, false, "", false, want{code: BatchCodeInputMissing}},
		{"pending/input fixed, empty", state.TaskStatusPending, true, "", false, want{kind: BatchTurnRerun, input: "the fixed task text"}},
		{"pending/input fixed, content", state.TaskStatusPending, true, "more", false, want{code: BatchCodeInputFixed}},
		{"stopped, empty", state.TaskStatusStopped, true, "", false, want{kind: BatchTurnContinue, input: BatchContinuePrompt}},
		{"stopped, content", state.TaskStatusStopped, true, "more", false, want{code: BatchCodeTakesNoInput}},
		{"running, anything", state.TaskStatusRunning, true, "more", true, want{code: BatchCodeRunning}},
		{"completed", state.TaskStatusCompleted, true, "", false, want{code: BatchCodeFinished}},
		{"failed", state.TaskStatusFailed, true, "", false, want{code: BatchCodeFinished}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newBatchEnv(t)
			// The execution under test. When the task's input is already
			// fixed, a first execution fixed it and this one is the new
			// execution that runs on it — exactly the state a re-run reaches.
			subject := env.exec
			if tt.inputSet {
				if _, err := env.projects.StartTaskExecution(ctx, env.exec.SessionID, state.TaskStatusPending, "the fixed task text", true); err != nil {
					t.Fatal(err)
				}
				if err := env.sessions.Ensure(ctx, "s2", "s2"); err != nil {
					t.Fatal(err)
				}
				second, err := env.projects.AddTaskExecution(ctx, env.task.ID, "s2", env.root)
				if err != nil {
					t.Fatal(err)
				}
				subject = second
			}
			switch tt.status {
			case state.TaskStatusPending:
			case state.TaskStatusRunning:
				if _, err := env.projects.StartTaskExecution(ctx, subject.SessionID, state.TaskStatusPending, "", false); err != nil {
					t.Fatal(err)
				}
			default:
				if _, err := env.projects.StartTaskExecution(ctx, subject.SessionID, state.TaskStatusPending, "", false); err != nil {
					t.Fatal(err)
				}
				if err := env.projects.MarkExecutionEnded(ctx, subject.SessionID, tt.status, "", ""); err != nil {
					t.Fatal(err)
				}
			}
			before, beforeTask, _, err := env.projects.ExecutionForSession(ctx, subject.SessionID)
			if err != nil {
				t.Fatal(err)
			}

			admission, err := AdmitBatchTurn(ctx, env.projects, subject.SessionID, tt.content, tt.attachment)
			if tt.want.code != "" {
				var refusal *BatchRefusal
				if !errors.As(err, &refusal) || refusal.Code != tt.want.code {
					t.Fatalf("error = %v, want refusal %q", err, tt.want.code)
				}
			} else {
				if err != nil {
					t.Fatalf("admission error = %v", err)
				}
				if admission.Kind != tt.want.kind || admission.Input != tt.want.input {
					t.Fatalf("admission = %+v, want kind %q input %q", admission, tt.want.kind, tt.want.input)
				}
			}
			// The preview is read-only: neither the input nor the status moved.
			after, afterTask, _, err := env.projects.ExecutionForSession(ctx, subject.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Status != before.Status || after.StartedAt != before.StartedAt || after.FinishedAt != before.FinishedAt {
				t.Fatalf("preview changed the execution: %+v → %+v", before, after)
			}
			if afterTask.InputSet != beforeTask.InputSet || afterTask.InputText != beforeTask.InputText {
				t.Fatalf("preview changed the task input: %+v → %+v", beforeTask, afterTask)
			}
		})
	}
}

func TestSubmitBatchFirstTurnRecordsInput(t *testing.T) {
	ctx := context.Background()
	env := newBatchEnv(t)
	var seen TurnRequest
	var ended []BatchExecutionEnd
	svc := New(
		WithRunExecutor(runFunc(func(_ context.Context, req TurnRequest, started func(string)) (*agent.Result, error) {
			seen = req
			started("run-1")
			return &agent.Result{}, nil
		})),
		WithBatchTasks(env.projects, env.sessions, func(_ context.Context, end BatchExecutionEnd) {
			ended = append(ended, end)
		}),
	)
	out, err := svc.Submit(ctx, TurnRequest{
		SessionID:  env.exec.SessionID,
		UserText:   "count rows",
		BatchInput: &BatchTurnInput{Text: "count rows"},
	}, nil)
	if err != nil || out.Status != TurnCompleted {
		t.Fatalf("outcome = %+v, err = %v", out, err)
	}
	if !seen.Unattended {
		t.Fatal("a batch turn ran attended")
	}
	if seen.UserText != "count rows" {
		t.Fatalf("turn input = %q", seen.UserText)
	}
	exec, task, _, err := env.projects.ExecutionForSession(ctx, env.exec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if exec.Status != state.TaskStatusCompleted || exec.FinishedAt == 0 {
		t.Fatalf("execution = %+v, want completed with a finish time", exec)
	}
	if !task.InputSet || task.InputText != "count rows" {
		t.Fatalf("task input = %q (set %v)", task.InputText, task.InputSet)
	}
	if len(ended) != 1 || ended[0].Status != state.TaskStatusCompleted || ended[0].Execution.FinishedAt == 0 {
		t.Fatalf("onEnd = %+v", ended)
	}
}

func TestSubmitBatchNewExecutionRunsOnTheTaskInput(t *testing.T) {
	ctx := context.Background()
	env := newBatchEnv(t)
	if _, err := env.projects.StartTaskExecution(ctx, env.exec.SessionID, state.TaskStatusPending, "the task text", true); err != nil {
		t.Fatal(err)
	}
	if err := env.projects.MarkExecutionEnded(ctx, env.exec.SessionID, state.TaskStatusCompleted, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := env.sessions.Ensure(ctx, "s2", "s2"); err != nil {
		t.Fatal(err)
	}
	second, err := env.projects.AddTaskExecution(ctx, env.task.ID, "s2", filepath.Dir(filepath.Dir(env.exec.Workspace)))
	if err != nil {
		t.Fatal(err)
	}
	var seen TurnRequest
	svc := New(
		WithRunExecutor(runFunc(func(_ context.Context, req TurnRequest, started func(string)) (*agent.Result, error) {
			seen = req
			started("run-2")
			return &agent.Result{}, nil
		})),
		WithBatchTasks(env.projects, env.sessions, nil),
	)
	// The surface sends nothing: the engine runs the task's own text.
	out, err := svc.Submit(ctx, TurnRequest{
		SessionID:  second.SessionID,
		BatchInput: &BatchTurnInput{Text: ""},
	}, nil)
	if err != nil || out.Status != TurnCompleted {
		t.Fatalf("outcome = %+v, err = %v", out, err)
	}
	if seen.UserText != "the task text" || seen.RawInput != "the task text" {
		t.Fatalf("turn input = %q (raw %q), want the task's own text", seen.UserText, seen.RawInput)
	}
	exec, _, _, _ := env.projects.ExecutionForSession(ctx, second.SessionID)
	if exec.Status != state.TaskStatusCompleted || exec.Seq != 2 {
		t.Fatalf("second execution = %+v", exec)
	}
}

func TestSubmitBatchRequiresAdmission(t *testing.T) {
	ctx := context.Background()
	env := newBatchEnv(t)
	svc := New(
		WithRunExecutor(runFunc(func(context.Context, TurnRequest, func(string)) (*agent.Result, error) {
			t.Fatal("a batch turn without an admission reached the runner")
			return nil, nil
		})),
		WithBatchTasks(env.projects, env.sessions, nil),
	)
	_, err := svc.Submit(ctx, TurnRequest{SessionID: env.exec.SessionID, UserText: "count rows"}, nil)
	var refusal *BatchRefusal
	if !errors.As(err, &refusal) || refusal.Code != BatchCodeAdmissionRequired {
		t.Fatalf("error = %v, want %q", err, BatchCodeAdmissionRequired)
	}
	exec, task, _, _ := env.projects.ExecutionForSession(ctx, env.exec.SessionID)
	if exec.Status != state.TaskStatusPending || task.InputSet {
		t.Fatalf("a refused turn moved the execution: %+v / %+v", exec, task)
	}
}

func TestSubmitBatchCancelStopsAndKeepsTheEpilogue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	env := newBatchEnv(t)
	// A deliverable left in outputs/ with nothing presented: the epilogue has
	// to collect it even though the turn was cancelled.
	if err := os.WriteFile(filepath.Join(env.exec.Workspace, "outputs", "report.xlsx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(env.exec.Workspace, "outputs", ".work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.exec.Workspace, "outputs", ".work", "x.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.exec.Workspace, "outputs", ".hidden"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tasks := &recordingTaskStore{ProjectStore: env.projects}
	arts := &recordingArtifactStore{SessionStore: env.sessions}
	var ended []BatchExecutionEnd
	svc := New(
		WithRunExecutor(runFunc(func(runCtx context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
			started("run-3")
			cancel()
			return nil, runCtx.Err()
		})),
		WithBatchTasks(tasks, arts, func(_ context.Context, end BatchExecutionEnd) {
			ended = append(ended, end)
		}),
	)
	out, err := svc.Submit(ctx, TurnRequest{
		SessionID:  env.exec.SessionID,
		UserText:   "count rows",
		BatchInput: &BatchTurnInput{Text: "count rows"},
	}, nil)
	if out.Status != TurnCancelled || !errors.Is(err, context.Canceled) {
		t.Fatalf("outcome = %+v, err = %v", out, err)
	}
	exec, _, _, err := env.projects.ExecutionForSession(context.Background(), env.exec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if exec.Status != state.TaskStatusStopped || exec.FinishedAt == 0 {
		t.Fatalf("execution = %+v, want stopped with a finish time", exec)
	}
	if len(ended) != 1 || ended[0].Status != state.TaskStatusStopped {
		t.Fatalf("onEnd = %+v, want one stopped report", ended)
	}
	files := map[string]state.Artifact{}
	for _, a := range ended[0].Artifacts {
		files[a.Name] = a
	}
	if len(files) != 1 || files["report.xlsx"].Origin != state.ArtifactOriginFallback {
		t.Fatalf("fallback artifacts = %+v, want only report.xlsx as a fallback", ended[0].Artifacts)
	}
	if rel := files["report.xlsx"].RelPath; rel != "outputs/report.xlsx" {
		t.Fatalf("fallback rel_path = %q, want the workspace-relative path", rel)
	}
	// The epilogue ran on a context that outlived the cancellation, or the
	// store calls above would have failed with context.Canceled.
	for _, scope := range append(tasks.scopes(), arts.scopes()...) {
		if scope != nil {
			t.Fatalf("the batch epilogue ran on a cancelled context: %v", scope)
		}
	}
}

func TestSubmitBatchUsageLimitFailsWithoutContinuation(t *testing.T) {
	ctx := context.Background()
	env := newBatchEnv(t)
	clock := newFakeClock(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC))
	var continued int
	var ended []BatchExecutionEnd
	svc := New(
		WithRunExecutor(runFunc(func(_ context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
			started("run-4")
			return nil, quotaError(clock.Now().Add(3*time.Hour), "pro")
		})),
		WithAutoContinue(AutoContinueConfig{
			Events:   &recordingSink{},
			Surfaces: []Surface{SurfaceWebChat},
			Continue: func(context.Context, AutoContinuePlan, string) error {
				continued++
				return nil
			},
			now:       clock.Now,
			afterFunc: clock.AfterFunc,
		}),
		WithBatchTasks(env.projects, env.sessions, func(_ context.Context, end BatchExecutionEnd) {
			ended = append(ended, end)
		}),
	)
	out, err := svc.Submit(ctx, TurnRequest{
		SessionID:  env.exec.SessionID,
		Origin:     Origin{Surface: SurfaceWebChat},
		UserText:   "count rows",
		BatchInput: &BatchTurnInput{Text: "count rows"},
	}, nil)
	if out.Status != TurnFailed || err == nil {
		t.Fatalf("outcome = %+v, err = %v", out, err)
	}
	exec, _, _, storeErr := env.projects.ExecutionForSession(ctx, env.exec.SessionID)
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	if exec.Status != state.TaskStatusFailed || exec.ErrorCode != string(llm.ExplainRateLimitQuota) {
		t.Fatalf("execution = %+v, want failed with the quota code", exec)
	}
	if _, ok := svc.PendingAutoContinue(env.exec.SessionID); ok {
		t.Fatal("a batch turn scheduled a continuation")
	}
	clock.Advance(4 * time.Hour)
	if continued != 0 {
		t.Fatalf("a batch turn continued after the reset (%d times)", continued)
	}
	if len(ended) != 1 || ended[0].ErrorCode != string(llm.ExplainRateLimitQuota) || ended[0].ErrorText == "" {
		t.Fatalf("onEnd = %+v", ended)
	}
}

func TestBatchFallbackArtifactsOnlyWhenNothingWasDelivered(t *testing.T) {
	ctx := context.Background()
	env := newBatchEnv(t)
	outDir := filepath.Join(env.exec.Workspace, "outputs")
	if err := os.WriteFile(filepath.Join(outDir, "a.docx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := New(WithBatchTasks(env.projects, env.sessions, nil))
	svc.collectBatchFallbackArtifacts(ctx, env.exec.SessionID, "run-5", env.exec.Workspace)
	rows, total, err := env.sessions.ListArtifactsPage(ctx, env.exec.SessionID, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || rows[0].Name != "a.docx" || rows[0].Origin != state.ArtifactOriginFallback || rows[0].RunID != "run-5" {
		t.Fatalf("fallback artifacts = %+v (total %d)", rows, total)
	}
	if rows[0].SHA256 == "" || rows[0].Size == 0 {
		t.Fatalf("fallback artifact is missing its digest: %+v", rows[0])
	}
}

func TestAdmitBatchTurnWithoutATask(t *testing.T) {
	env := newBatchEnv(t)
	_, err := AdmitBatchTurn(context.Background(), env.projects, "s-other", "x", false)
	var refusal *BatchRefusal
	if !errors.As(err, &refusal) || refusal.Code != BatchCodeNoTask {
		t.Fatalf("error = %v, want %q", err, BatchCodeNoTask)
	}
}

// eventRecorder captures every run event the engine publishes, in order.
type eventRecorder struct {
	mu     sync.Mutex
	events []event.RunEvent
	err    error
}

func (r *eventRecorder) publish(_ context.Context, evt event.RunEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, evt)
	return r.err
}

func (r *eventRecorder) ofType(eventType string) []event.BatchExecutionPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []event.BatchExecutionPayload
	for _, evt := range r.events {
		if evt.Type != eventType {
			continue
		}
		var payload event.BatchExecutionPayload
		if err := json.Unmarshal(evt.Payload, &payload); err != nil {
			continue
		}
		out = append(out, payload)
	}
	return out
}

func TestSubmitBatchPublishesTheExecutionsState(t *testing.T) {
	ctx := context.Background()
	env := newBatchEnv(t)
	recorder := &eventRecorder{}
	svc := New(
		WithRunExecutor(runFunc(func(_ context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
			started("run-1")
			return &agent.Result{}, nil
		})),
		WithBatchTasks(env.projects, env.sessions, nil),
		WithRunEventPublisher(recorder.publish),
	)
	out, err := svc.Submit(ctx, TurnRequest{
		SessionID:  env.exec.SessionID,
		UserText:   "count rows",
		BatchInput: &BatchTurnInput{Text: "count rows"},
	}, nil)
	if err != nil || out.Status != TurnCompleted {
		t.Fatalf("outcome = %+v, err = %v", out, err)
	}

	states := recorder.ofType(event.RunEventBatchExecution)
	if len(states) != 2 {
		t.Fatalf("published %d execution states, want the start and the end: %+v", len(states), states)
	}
	start, end := states[0], states[1]
	if start.Status != state.TaskStatusRunning {
		t.Fatalf("first state = %q, want running", start.Status)
	}
	if start.TaskID != env.task.ID || start.SessionID != env.exec.SessionID || start.Seq != env.exec.Seq {
		t.Fatalf("first state names the wrong execution: %+v", start)
	}
	if start.StartedAt == 0 || start.FinishedAt != 0 {
		t.Fatalf("first state carries the wrong clock: %+v", start)
	}
	if end.Status != state.TaskStatusCompleted {
		t.Fatalf("last state = %q, want completed", end.Status)
	}
	if end.TaskID != env.task.ID || end.Seq != env.exec.Seq {
		t.Fatalf("last state names the wrong execution: %+v", end)
	}
	if end.StartedAt == 0 || end.FinishedAt < end.StartedAt {
		t.Fatalf("last state carries the wrong clock: %+v", end)
	}
	if end.ErrorCode != "" || end.ErrorText != "" {
		t.Fatalf("a completed execution reported an error: %+v", end)
	}
	// Every frame belongs to the conversation it reports on, or no page would
	// be routed it.
	for _, evt := range recorder.events {
		if evt.SessionID != env.exec.SessionID || evt.RunID != "run-1" {
			t.Fatalf("event routed as %s/%s: %+v", evt.SessionID, evt.RunID, evt)
		}
	}
}

func TestSubmitBatchPublishesAFailedExecutionsState(t *testing.T) {
	ctx := context.Background()
	env := newBatchEnv(t)
	recorder := &eventRecorder{}
	boom := errors.New("the spreadsheet library refused the workbook")
	svc := New(
		WithRunExecutor(runFunc(func(_ context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
			started("run-2")
			return nil, boom
		})),
		WithBatchTasks(env.projects, env.sessions, nil),
		WithRunEventPublisher(recorder.publish),
	)
	out, err := svc.Submit(ctx, TurnRequest{
		SessionID:  env.exec.SessionID,
		UserText:   "count rows",
		BatchInput: &BatchTurnInput{Text: "count rows"},
	}, nil)
	if !errors.Is(err, boom) || out.Status != TurnFailed {
		t.Fatalf("outcome = %+v, err = %v", out, err)
	}
	states := recorder.ofType(event.RunEventBatchExecution)
	if len(states) != 2 || states[0].Status != state.TaskStatusRunning {
		t.Fatalf("published states = %+v", states)
	}
	end := states[1]
	if end.Status != state.TaskStatusFailed || end.ErrorCode == "" || !strings.Contains(end.ErrorText, "spreadsheet") {
		t.Fatalf("failed state = %+v", end)
	}
	if end.FinishedAt == 0 {
		t.Fatalf("a failed execution reported no finish time: %+v", end)
	}
}

func TestSubmitOrdinaryTurnPublishesNoExecutionState(t *testing.T) {
	ctx := context.Background()
	env := newBatchEnv(t)
	if err := env.sessions.Ensure(ctx, "s-plain", "s-plain"); err != nil {
		t.Fatal(err)
	}
	recorder := &eventRecorder{}
	svc := New(
		WithRunExecutor(runFunc(func(_ context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
			started("run-3")
			return &agent.Result{}, nil
		})),
		// The batch store is installed, so a conversation that is not an
		// execution takes the ordinary path and says nothing about one.
		WithBatchTasks(env.projects, env.sessions, nil),
		WithRunEventPublisher(recorder.publish),
	)
	if _, err := svc.Submit(ctx, TurnRequest{SessionID: "s-plain", UserText: "hello"}, nil); err != nil {
		t.Fatal(err)
	}
	if states := recorder.ofType(event.RunEventBatchExecution); len(states) != 0 {
		t.Fatalf("an ordinary turn published execution states: %+v", states)
	}
}
