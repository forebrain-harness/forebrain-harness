package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// newTaskStoreForTest opens one store pair over a batch project the caller can
// create tasks in: the project store, the session store and the project row.
func newTaskStoreForTest(t *testing.T) (*ProjectStore, *SessionStore, Project) {
	t.Helper()
	ctx := context.Background()
	projects, sessions := newProjectStoreForTest(t, "main")
	root := t.TempDir()
	p, err := projects.Create(ctx, CreateProjectInput{
		Name:        "batch",
		Root:        root,
		ProjectKey:  ProjectKeyForRoot(root),
		ProjectType: ProjectTypeBatch,
	})
	if err != nil {
		t.Fatal(err)
	}
	return projects, sessions, p
}

// addExecutionForTest opens one conversation and files it as an execution of
// taskID, the way both create endpoints do.
func addExecutionForTest(t *testing.T, projects *ProjectStore, sessions *SessionStore, taskID, sessionID, root string) TaskExecution {
	t.Helper()
	ctx := context.Background()
	if err := sessions.Ensure(ctx, sessionID, sessionID); err != nil {
		t.Fatal(err)
	}
	exec, err := projects.AddTaskExecution(ctx, taskID, sessionID, root)
	if err != nil {
		t.Fatal(err)
	}
	return exec
}

func TestCreateTaskRequiresBatchProject(t *testing.T) {
	ctx := context.Background()
	projects, _, batch := newTaskStoreForTest(t)
	task, err := projects.CreateTask(ctx, Task{ProjectID: batch.ID, Title: "monthly report", ExternalRef: "ext-1"})
	if err != nil {
		t.Fatal(err)
	}
	if task.ID == "" || task.Origin != TaskOriginAPI || task.InputSet || task.InputText != "" {
		t.Fatalf("created task = %+v, want a fresh api task with no input yet", task)
	}

	coding, err := projects.Create(ctx, CreateProjectInput{Name: "coding", Root: t.TempDir(), ProjectKey: ProjectKeyForRoot("x"), ProjectType: ProjectTypeCoding})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.CreateTask(ctx, Task{ProjectID: coding.ID, Title: "nope"}); !errors.Is(err, ErrNotBatchProject) {
		t.Fatalf("task on a coding project err = %v, want ErrNotBatchProject", err)
	}
	if _, err := projects.CreateTask(ctx, Task{ProjectID: "prj-missing"}); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("task on a missing project err = %v, want ErrProjectNotFound", err)
	}
}

func TestAddTaskExecutionNumbersThemAndIsConcurrencySafe(t *testing.T) {
	ctx := context.Background()
	projects, sessions, batch := newTaskStoreForTest(t)
	task, err := projects.CreateTask(ctx, Task{ProjectID: batch.ID, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	const n = 2
	var wg sync.WaitGroup
	seqs := make([]int, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		sid := "s-" + string(rune('a'+i))
		if err := sessions.Ensure(ctx, sid, sid); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func(i int, sid string) {
			defer wg.Done()
			exec, err := projects.AddTaskExecution(context.Background(), task.ID, sid, batch.Root)
			seqs[i], errs[i] = exec.Seq, err
		}(i, sid)
	}
	wg.Wait()
	seen := map[int]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("execution %d: %v", i, err)
		}
		if seen[seqs[i]] {
			t.Fatalf("two executions got seq %d: %v", seqs[i], seqs)
		}
		seen[seqs[i]] = true
	}
	if !seen[1] || !seen[2] {
		t.Fatalf("concurrent executions = %v, want 1 and 2", seqs)
	}
	if err := sessions.Ensure(ctx, "s-c", "s-c"); err != nil {
		t.Fatal(err)
	}
	exec, err := projects.AddTaskExecution(ctx, task.ID, "s-c", batch.Root)
	if err != nil {
		t.Fatal(err)
	}
	if exec.Seq != 3 {
		t.Fatalf("third execution seq = %d, want 3", exec.Seq)
	}
	if want := TaskWorkspaceDir(batch.Root, task.ID, 3); exec.Workspace != want {
		t.Fatalf("workspace = %q, want %q", exec.Workspace, want)
	}
	if one, _, ok, err := projects.ExecutionForSession(ctx, "s-c"); err != nil || !ok || one.TaskID != task.ID {
		t.Fatalf("ExecutionForSession = %+v, %v", one, err)
	}
	if _, _, ok, err := projects.ExecutionForSession(ctx, "s-nope"); err != nil || ok {
		t.Fatalf("an ordinary conversation read as an execution: %v %v", ok, err)
	}
}

func TestStartTaskExecutionRecordsInputAndRuns(t *testing.T) {
	ctx := context.Background()
	projects, _, batch := newTaskStoreForTest(t)
	task, err := projects.CreateTask(ctx, Task{ProjectID: batch.ID, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	exec := addExecutionForTest(t, projects, newSessionStoreFor(projects), task.ID, "s-1", batch.Root)

	started, err := projects.StartTaskExecution(ctx, exec.SessionID, TaskStatusPending, "count the rows", true)
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != TaskStatusRunning || started.StartedAt == 0 {
		t.Fatalf("started = %+v, want running with a start time", started)
	}
	got, gotTask, ok, err := projects.ExecutionForSession(ctx, exec.SessionID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got.Status != TaskStatusRunning || !gotTask.InputSet || gotTask.InputText != "count the rows" {
		t.Fatalf("stored execution = %+v, task = %+v", got, gotTask)
	}

	// A second starter loses: the execution is already running.
	if _, err := projects.StartTaskExecution(ctx, exec.SessionID, TaskStatusPending, "again", true); !errors.Is(err, ErrTaskExecutionState) {
		t.Fatalf("second start err = %v, want ErrTaskExecutionState", err)
	}
	var stateErr *TaskExecutionStateError
	if _, err := projects.StartTaskExecution(ctx, exec.SessionID, TaskStatusPending, "again", true); !errors.As(err, &stateErr) || stateErr.Status != TaskStatusRunning {
		t.Fatalf("second start err = %v, want a state error naming running", err)
	}
	// The refused start changed nothing.
	_, gotTask, _, _ = projects.ExecutionForSession(ctx, exec.SessionID)
	if gotTask.InputText != "count the rows" {
		t.Fatalf("a refused start rewrote the input: %q", gotTask.InputText)
	}

	// Stopped by a cancel, then continued in the same conversation.
	if err := projects.MarkExecutionEnded(ctx, exec.SessionID, TaskStatusStopped, "", ""); err != nil {
		t.Fatal(err)
	}
	stopped, stoppedTask, _, _ := projects.ExecutionForSession(ctx, exec.SessionID)
	if stopped.Status != TaskStatusStopped || stopped.FinishedAt == 0 {
		t.Fatalf("stopped = %+v, want stopped with a finish time", stopped)
	}
	resumed, err := projects.StartTaskExecution(ctx, exec.SessionID, TaskStatusStopped, "ignored", false)
	if err != nil {
		t.Fatalf("continue err = %v", err)
	}
	if resumed.Status != TaskStatusRunning || resumed.FinishedAt != 0 {
		t.Fatalf("continued = %+v, want running with no finish time", resumed)
	}
	_, stillTask, _, _ := projects.ExecutionForSession(ctx, exec.SessionID)
	if stillTask.InputText != stoppedTask.InputText {
		t.Fatalf("a continuation rewrote the input: %q vs %q", stillTask.InputText, stoppedTask.InputText)
	}
	if finished := got.FinishedAt; finished != 0 && stopped.FinishedAt < finished {
		t.Fatalf("stopped finished_at went backwards")
	}
}

func TestStartTaskExecutionFixesInputOnce(t *testing.T) {
	ctx := context.Background()
	projects, sessions, batch := newTaskStoreForTest(t)
	task, err := projects.CreateTask(ctx, Task{ProjectID: batch.ID, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	first := addExecutionForTest(t, projects, sessions, task.ID, "s-1", batch.Root)
	second := addExecutionForTest(t, projects, sessions, task.ID, "s-2", batch.Root)

	// The first turn of the task fixes its input.
	if _, err := projects.StartTaskExecution(ctx, first.SessionID, TaskStatusPending, "the task text", true); err != nil {
		t.Fatal(err)
	}
	// A second execution that computed "the input is not recorded yet" from
	// before that commit must not overwrite it, and must not run on input it
	// failed to record: the whole start rolls back.
	if _, err := projects.StartTaskExecution(ctx, second.SessionID, TaskStatusPending, "another text", true); !errors.Is(err, ErrTaskInputFixed) {
		t.Fatalf("second execution start err = %v, want ErrTaskInputFixed", err)
	}
	gotExec, gotTask, _, err := projects.ExecutionForSession(ctx, second.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if gotExec.Status != TaskStatusPending || gotExec.StartedAt != 0 {
		t.Fatalf("a refused start left the execution %+v, want it still pending", gotExec)
	}
	if gotTask.InputText != "the task text" || !gotTask.InputSet {
		t.Fatalf("task input = %q (set %v), want the first turn's text", gotTask.InputText, gotTask.InputSet)
	}
	// Reading the input as already fixed is what the next execution does.
	if _, err := projects.StartTaskExecution(ctx, second.SessionID, TaskStatusPending, "", false); err != nil {
		t.Fatalf("second execution with a fixed input err = %v", err)
	}

	// Two turns racing to start the same execution: exactly one runs.
	third := addExecutionForTest(t, projects, sessions, task.ID, "s-3", batch.Root)
	var wg sync.WaitGroup
	var mu sync.Mutex
	okCount, stateCount := 0, 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := projects.StartTaskExecution(context.Background(), third.SessionID, TaskStatusPending, "", false)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				okCount++
			case errors.Is(err, ErrTaskExecutionState), errors.Is(err, ErrTaskInputFixed):
				stateCount++
			}
		}()
	}
	wg.Wait()
	if okCount != 1 || stateCount != 1 {
		t.Fatalf("racers: %d succeeded, %d refused; want 1 and 1", okCount, stateCount)
	}
	_, finalTask, _, _ := projects.ExecutionForSession(ctx, third.SessionID)
	if !finalTask.InputSet {
		t.Fatal("the task input was left unrecorded")
	}
	running := 0
	for _, sid := range []string{first.SessionID, "s-2", third.SessionID} {
		exec, _, _, err := projects.ExecutionForSession(ctx, sid)
		if err != nil {
			t.Fatal(err)
		}
		if exec.Status == TaskStatusRunning {
			running++
		}
	}
	if running != 3 {
		t.Fatalf("%d of the three executions are running, want all: each started once", running)
	}
}

func TestMarkExecutionEndedStampsAndTouches(t *testing.T) {
	ctx := context.Background()
	projects, sessions, batch := newTaskStoreForTest(t)
	task, err := projects.CreateTask(ctx, Task{ProjectID: batch.ID, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	exec := addExecutionForTest(t, projects, sessions, task.ID, "s-1", batch.Root)
	if _, err := projects.StartTaskExecution(ctx, exec.SessionID, TaskStatusPending, "go", true); err != nil {
		t.Fatal(err)
	}
	if err := projects.MarkExecutionEnded(ctx, exec.SessionID, TaskStatusFailed, "rate_limit_quota", "spent"); err != nil {
		t.Fatal(err)
	}
	got, gotTask, _, err := projects.ExecutionForSession(ctx, exec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != TaskStatusFailed || got.ErrorCode != "rate_limit_quota" || got.ErrorText != "spent" || got.FinishedAt == 0 {
		t.Fatalf("ended execution = %+v", got)
	}
	if gotTask.UpdatedAt < got.CreatedAt {
		t.Fatalf("task updated_at %d went before created_at %d", gotTask.UpdatedAt, gotTask.CreatedAt)
	}
	if err := projects.MarkExecutionEnded(ctx, exec.SessionID, TaskStatusRunning, "", ""); err == nil {
		t.Fatal("an execution was ended as running")
	}
}

func TestListTasksPageLatestExecutionsAndFilters(t *testing.T) {
	ctx := context.Background()
	projects, sessions, batch := newTaskStoreForTest(t)
	run := func(title, externalRef, sessionID string, stop bool) Task {
		t.Helper()
		task, err := projects.CreateTask(ctx, Task{ProjectID: batch.ID, Title: title, ExternalRef: externalRef})
		if err != nil {
			t.Fatal(err)
		}
		exec := addExecutionForTest(t, projects, sessions, task.ID, sessionID, batch.Root)
		if _, err := projects.StartTaskExecution(ctx, exec.SessionID, TaskStatusPending, title, true); err != nil {
			t.Fatal(err)
		}
		if stop {
			if err := projects.MarkExecutionEnded(ctx, exec.SessionID, TaskStatusStopped, "", ""); err != nil {
				t.Fatal(err)
			}
		}
		return task
	}
	run("alpha", "ext-alpha", "s-1", false)
	stoppedTask := run("beta", "ext-beta", "s-2", true)

	rows, total, err := projects.ListTasksPage(ctx, batch.ID, ListTasksOptions{}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("list = %d rows (total %d), want 2", len(rows), total)
	}
	// Tasks created in the same second tie on updated_at and are then ordered
	// by id, so the assertions find their row instead of assuming a position.
	byID := map[string]TaskRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	if got := byID[stoppedTask.ID]; got.Latest.Status != TaskStatusStopped || got.Latest.Seq != 1 || got.Executions != 1 {
		t.Fatalf("stopped task row = %+v (latest %+v, %d executions)", got.Task, got.Latest, got.Executions)
	}

	// The status filter reads the latest execution, so it is asserted before
	// the stopped task gains another — pending — one.
	stopped, _, err := projects.ListTasksPage(ctx, batch.ID, ListTasksOptions{Status: TaskStatusStopped}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(stopped) != 1 || stopped[0].ID != stoppedTask.ID {
		t.Fatalf("status filter = %+v, want only the stopped task", stopped)
	}

	// A second execution of the stopped task makes it latest again.
	second := addExecutionForTest(t, projects, sessions, stoppedTask.ID, "s-3", batch.Root)
	rows, _, err = projects.ListTasksPage(ctx, batch.ID, ListTasksOptions{Search: "ext-beta"}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Latest.Seq != second.Seq || rows[0].Executions != 2 {
		t.Fatalf("search by external_ref = %+v", rows)
	}

	// Paging: one per page, and an offset past the end reads the last page.
	page, pageTotal, err := projects.ListTasksPage(ctx, batch.ID, ListTasksOptions{}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if pageTotal != 2 || len(page) != 1 {
		t.Fatalf("second page = %+v (total %d), want one row and a total of 2", page, pageTotal)
	}
	last, _, err := projects.ListTasksPage(ctx, batch.ID, ListTasksOptions{}, 1, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(last) != 1 {
		t.Fatalf("an offset past the end returned %d rows, want the last page", len(last))
	}

	// Another tenant sees nothing.
	other := NewProjectStore(projects.db, "other")
	if rows, total, err := other.ListTasksPage(ctx, batch.ID, ListTasksOptions{}, 50, 0); err != nil || len(rows) != 0 || total != 0 {
		t.Fatalf("another agent's list = %d rows (total %d), %v", len(rows), total, err)
	}
}

func TestListTaskExecutionsPage(t *testing.T) {
	ctx := context.Background()
	projects, sessions, batch := newTaskStoreForTest(t)
	task, err := projects.CreateTask(ctx, Task{ProjectID: batch.ID, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		addExecutionForTest(t, projects, sessions, task.ID, "s-"+string(rune('a'+i)), batch.Root)
	}
	rows, total, err := projects.ListTaskExecutionsPage(ctx, task.ID, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(rows) != 3 {
		t.Fatalf("executions = %d rows (total %d), want 3", len(rows), total)
	}
	if rows[0].Seq != 3 || rows[2].Seq != 1 {
		t.Fatalf("executions are not newest first: %d..%d", rows[0].Seq, rows[2].Seq)
	}
	if _, _, err := projects.ListTaskExecutionsPage(ctx, "tsk-missing", 50, 0); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("missing task err = %v, want ErrTaskNotFound", err)
	}
}

func TestDeletingProjectCascadesTasksAndExecutions(t *testing.T) {
	ctx := context.Background()
	projects, sessions, batch := newTaskStoreForTest(t)
	task, err := projects.CreateTask(ctx, Task{ProjectID: batch.ID, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	exec := addExecutionForTest(t, projects, sessions, task.ID, "s-1", batch.Root)
	workspace := filepath.Join(exec.Workspace, "outputs")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := projects.Delete(ctx, batch.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := projects.GetTask(ctx, task.ID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("task survived its project: %v", err)
	}
	var executions, tasks int
	if err := projects.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_task_executions`).Scan(&executions); err != nil {
		t.Fatal(err)
	}
	if err := projects.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_tasks`).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if executions != 0 || tasks != 0 {
		t.Fatalf("after deleting the project: %d tasks, %d executions", tasks, executions)
	}
	// The directories stay: deleting a project never deletes files.
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("the task workspace went with the project: %v", err)
	}
}

// newSessionStoreFor reuses the project store's database for a session store
// under the same agent, the way production wires the two.
func newSessionStoreFor(projects *ProjectStore) *SessionStore {
	return NewSessionStore(projects.db, projects.AgentID())
}
