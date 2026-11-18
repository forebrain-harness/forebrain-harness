package turn

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// deliverableEnv is one project conversation with its real stores, so the
// top-up is exercised against the same reads and writes production uses.
type deliverableEnv struct {
	projects *state.ProjectStore
	sessions *state.SessionStore
	runs     *state.RunStore
	root     string
	session  string
	runID    string
}

func newDeliverableEnv(t *testing.T, projectType string) *deliverableEnv {
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
	p, err := projects.Create(ctx, state.CreateProjectInput{Name: "proj", Root: root, ProjectType: projectType})
	if err != nil {
		t.Fatal(err)
	}
	const sid = "s-dl"
	if err := sessions.EnsureAt(ctx, sid, sid, state.SessionBirth{ProjectID: p.ID}); err != nil {
		t.Fatal(err)
	}
	run, err := (&state.RunStore{DB: db}).CreateRun(ctx, sid, "do work")
	if err != nil {
		t.Fatal(err)
	}
	return &deliverableEnv{projects: projects, sessions: sessions, runs: &state.RunStore{DB: db}, root: root, session: sid, runID: run.ID}
}

func (e *deliverableEnv) write(t *testing.T, rel, content string) string {
	t.Helper()
	path := filepath.Join(e.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func (e *deliverableEnv) recordChange(t *testing.T, runID, path string, deleted bool) {
	t.Helper()
	if err := e.sessions.RecordFileChange(context.Background(), state.FileChange{
		SessionID: e.session, RunID: runID, Path: path, Deleted: deleted,
	}); err != nil {
		t.Fatal(err)
	}
}

// shellStepWithChangedPaths records one completed shell step whose raw output
// carries a shell_edit_diff with the given changed paths — the shape a
// successful AND (plan 324 rev 4) a failed command's step both carry.
func (e *deliverableEnv) shellStepWithChangedPaths(t *testing.T, runID string, paths ...string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"tool": "shell",
		"output": map[string]any{
			"shell_edit_diff": map[string]any{
				"files":         []any{},
				"changed_files": paths,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.runs.AppendSessionEvent(context.Background(), state.SessionEvent{
		ID: "evt-" + runID + "-" + filepath.Base(paths[0]), SessionID: e.session, RunID: runID,
		Type: event.RunEventToolCompleted, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
}

func (e *deliverableEnv) service() *Service {
	return New(
		WithSessionStore(e.sessions),
		WithRunEventStore(e.runs),
		WithBatchTasks(nil, e.sessions, nil),
	)
}

func (e *deliverableEnv) runArtifacts(t *testing.T, runID string) []state.Artifact {
	t.Helper()
	rows, err := e.sessions.ListArtifactsForRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestTurnTopUpRegistersUnpresentedFileToolWrite is the root case (a): a
// file-tool change the model never presented is registered as the run's
// fallback deliverable, root-relative, hash-stamped.
func TestTurnTopUpRegistersUnpresentedFileToolWrite(t *testing.T) {
	env := newDeliverableEnv(t, state.ProjectTypeCoding)
	path := env.write(t, "src/main.go", "package main\n")
	env.recordChange(t, env.runID, path, false)
	env.service().collectTurnDeliverables(context.Background(), env.session, env.runID, state.ProjectTypeCoding, env.root)
	rows := env.runArtifacts(t, env.runID)
	if len(rows) != 1 {
		t.Fatalf("run artifacts = %+v, want one fallback row", rows)
	}
	row := rows[0]
	if row.Origin != state.ArtifactOriginFallback || row.RelPath != "src/main.go" || row.Name != "main.go" {
		t.Fatalf("fallback row = %+v", row)
	}
	if row.SHA256 == "" || row.Size != int64(len("package main\n")) {
		t.Fatalf("fallback row not hash-stamped: %+v", row)
	}
}

// TestTurnTopUpSkipsModelPresentedSameBytes pins the session-level version
// check (b): a path already delivered with these bytes — by the model or by
// an earlier turn of the conversation — is not delivered again.
func TestTurnTopUpSkipsModelPresentedSameBytes(t *testing.T) {
	env := newDeliverableEnv(t, state.ProjectTypeCoding)
	path := env.write(t, "report.pdf", "bytes")
	env.recordChange(t, env.runID, path, false)
	if _, err := env.sessions.AddArtifact(context.Background(), state.Artifact{
		SessionID: env.session, RunID: env.runID, Path: path, RelPath: "report.pdf",
		Name: "report.pdf", Kind: "document", SHA256: rowsOf(t, path), Origin: state.ArtifactOriginModel,
	}); err != nil {
		t.Fatal(err)
	}
	env.service().collectTurnDeliverables(context.Background(), env.session, env.runID, state.ProjectTypeCoding, env.root)
	if rows := env.runArtifacts(t, env.runID); len(rows) != 1 || rows[0].Origin != state.ArtifactOriginModel {
		t.Fatalf("artifacts = %+v, want the single model row untouched", rows)
	}
}

// TestTurnTopUpRegistersReEditedFileAsNewVersion pins (c): a file edited
// again after it was presented is re-delivered — a path with new bytes is a
// new version, exactly what presenting it again would record.
func TestTurnTopUpRegistersReEditedFileAsNewVersion(t *testing.T) {
	env := newDeliverableEnv(t, state.ProjectTypeCoding)
	path := env.write(t, "report.pdf", "old")
	if _, err := env.sessions.AddArtifact(context.Background(), state.Artifact{
		SessionID: env.session, RunID: env.runID, Path: path, RelPath: "report.pdf",
		Name: "report.pdf", Kind: "document", SHA256: rowsOf(t, path), Origin: state.ArtifactOriginModel,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	env.recordChange(t, env.runID, path, false)
	env.service().collectTurnDeliverables(context.Background(), env.session, env.runID, state.ProjectTypeCoding, env.root)
	rows := env.runArtifacts(t, env.runID)
	if len(rows) != 2 {
		t.Fatalf("artifacts = %+v, want a model row and a new fallback version", rows)
	}
	if rows[1].Origin != state.ArtifactOriginFallback || rows[1].SHA256 == rows[0].SHA256 {
		t.Fatalf("new version = %+v", rows[1])
	}
}

// TestTurnTopUpExcludesDeletedOutsideAndEmpty pins (d) and (j): a deleted
// ledger row is not a deliverable, a path outside the project root is not
// either, and a turn with no run id has nothing to key the read on.
func TestTurnTopUpExcludesDeletedOutsideAndEmpty(t *testing.T) {
	env := newDeliverableEnv(t, state.ProjectTypeCoding)
	gone := env.write(t, "gone.txt", "x")
	env.recordChange(t, env.runID, gone, true)
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env.recordChange(t, env.runID, outside, false)
	env.service().collectTurnDeliverables(context.Background(), env.session, env.runID, state.ProjectTypeCoding, env.root)
	if rows := env.runArtifacts(t, env.runID); len(rows) != 0 {
		t.Fatalf("artifacts = %+v, want none", rows)
	}
	// A missing file since the write is skipped, not fatal.
	env.recordChange(t, env.runID, filepath.Join(env.root, "vanished.txt"), false)
	// No run id: no-op, whatever the ledger holds.
	env.service().collectTurnDeliverables(context.Background(), env.session, "", state.ProjectTypeCoding, env.root)
	if rows := env.runArtifacts(t, ""); len(rows) != 0 {
		t.Fatalf("empty run artifacts = %+v, want none", rows)
	}
}

// TestTurnTopUpRegistersShellChangedFilesIncludingBinary pins (f): the shell
// union reads the raw completion payloads' ChangedFiles, where binary and
// large files live — files that never appear in the diff's file records.
func TestTurnTopUpRegistersShellChangedFilesIncludingBinary(t *testing.T) {
	env := newDeliverableEnv(t, state.ProjectTypeCoding)
	bin := env.write(t, "assets/logo.png", "\x89PNG")
	env.shellStepWithChangedPaths(t, env.runID, bin)
	env.service().collectTurnDeliverables(context.Background(), env.session, env.runID, state.ProjectTypeCoding, env.root)
	rows := env.runArtifacts(t, env.runID)
	if len(rows) != 1 || rows[0].RelPath != "assets/logo.png" || rows[0].Origin != state.ArtifactOriginFallback {
		t.Fatalf("artifacts = %+v, want the binary as a fallback row", rows)
	}
}

// TestTurnTopUpCoversFailedShellSteps pins (k): a failed command's step
// carries the same raw payload shape (rev 4), so its writes are topped up
// too.
func TestTurnTopUpCoversFailedShellSteps(t *testing.T) {
	env := newDeliverableEnv(t, state.ProjectTypeCoding)
	path := env.write(t, "out/partial.csv", "a,b\n")
	env.shellStepWithChangedPaths(t, env.runID, path)
	env.service().collectTurnDeliverables(context.Background(), env.session, env.runID, state.ProjectTypeCoding, env.root)
	if rows := env.runArtifacts(t, env.runID); len(rows) != 1 || rows[0].RelPath != "out/partial.csv" {
		t.Fatalf("artifacts = %+v, want the failed command's write", rows)
	}
}

// TestTurnTopUpCoversSubagentWrites pins (g): a subagent run hangs off the
// primary's tree, so the ledger rows it recorded under its own run id are
// the turn's to deliver.
func TestTurnTopUpCoversSubagentWrites(t *testing.T) {
	env := newDeliverableEnv(t, state.ProjectTypeCoding)
	child, err := env.runs.CreateSubagentRun(context.Background(), env.runID, env.session, "sub work")
	if err != nil {
		t.Fatal(err)
	}
	path := env.write(t, "src/child.go", "package child\n")
	env.recordChange(t, child.ID, path, false)
	env.service().collectTurnDeliverables(context.Background(), env.session, env.runID, state.ProjectTypeCoding, env.root)
	if rows := env.runArtifacts(t, env.runID); len(rows) != 1 {
		t.Fatalf("artifacts = %+v, want the subagent's write under the primary run", rows)
	}
}

// TestTurnTopUpRunsOnACancelledTurn pins (h): a cancelled turn's writes
// already landed and are still delivered — the top-up detaches from the
// turn's cancellation, and Submit's cancelled branch still reaches it.
func TestTurnTopUpRunsOnACancelledTurn(t *testing.T) {
	env := newDeliverableEnv(t, state.ProjectTypeCoding)
	path := env.write(t, "src/main.go", "package main\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The direct call survives the cancelled context…
	env.recordChange(t, env.runID, path, false)
	env.service().collectTurnDeliverables(ctx, env.session, env.runID, state.ProjectTypeCoding, env.root)
	if rows := env.runArtifacts(t, env.runID); len(rows) != 1 {
		t.Fatalf("direct call on a cancelled ctx delivered %+v, want one row", rows)
	}
	// …and so does Submit's cancelled branch, end to end.
	second := env.write(t, "src/other.go", "package other\n")
	svc := env.service()
	svc.runner = runFunc(func(runCtx context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
		started(env.runID)
		env.recordChange(t, env.runID, second, false)
		return nil, runCtx.Err()
	})
	out, err := svc.Submit(ctx, TurnRequest{SessionID: env.session, UserText: "go"}, nil)
	if out.Status != TurnCancelled || !errors.Is(err, context.Canceled) {
		t.Fatalf("outcome = %+v, err = %v", out, err)
	}
	if rows := env.runArtifacts(t, env.runID); len(rows) != 2 {
		t.Fatalf("artifacts after a cancelled turn = %+v, want both writes", rows)
	}
}

// TestTurnTopUpBatchScratchExcluded pins (i): batch mirrors the outputs
// scan's scratch rule — nothing under a dot directory is a deliverable —
// while coding and office deliver any file of the project.
func TestTurnTopUpBatchScratchExcluded(t *testing.T) {
	for _, tc := range []struct {
		projectType string
		want        int
	}{
		{state.ProjectTypeBatch, 0},
		// Coding's rule is the owner's: every file the turn changed, whatever
		// its name or directory — scratch included.
		{state.ProjectTypeCoding, 2},
	} {
		env := newDeliverableEnv(t, tc.projectType)
		env.write(t, "tasks/t1/1/.work/scratch.tmp", "x")
		hidden := env.write(t, "tasks/t1/1/outputs/.hidden/report.pdf", "x")
		env.recordChange(t, env.runID, hidden, false)
		env.recordChange(t, env.runID, filepath.Join(env.root, "tasks", "t1", "1", ".work", "scratch.tmp"), false)
		env.service().collectTurnDeliverables(context.Background(), env.session, env.runID, tc.projectType, env.root)
		if rows := env.runArtifacts(t, env.runID); len(rows) != tc.want {
			t.Fatalf("%s artifacts = %+v, want %d rows", tc.projectType, rows, tc.want)
		}
	}
}

// TestTurnTopUpNoScopeIsNoOp pins the boundary: a conversation whose session
// store cannot name a project root (a workshop task, a service built without
// the scope read) delivers nothing the engine did not already — the prompt
// keeps the delivery duty, the guarantee simply has no anchor.
func TestTurnTopUpNoScopeIsNoOp(t *testing.T) {
	env := newDeliverableEnv(t, state.ProjectTypeCoding)
	path := env.write(t, "src/main.go", "package main\n")
	env.recordChange(t, env.runID, path, false)
	env.service().collectTurnDeliverables(context.Background(), env.session, env.runID, state.ProjectTypeCoding, "")
	if rows := env.runArtifacts(t, env.runID); len(rows) != 0 {
		t.Fatalf("artifacts = %+v, want none without a root", rows)
	}
}

// rowsOf hashes a file the way the store's rows do.
func rowsOf(t *testing.T, path string) string {
	t.Helper()
	sum, _, _, _, _, _, err := hashArtifactFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}
