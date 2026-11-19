package turn

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// deliverableEnv is one project conversation with its real stores, so the
// delivery assertions run against the same reads and writes production uses.
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

func (e *deliverableEnv) runArtifacts(t *testing.T, runID string) []state.Artifact {
	t.Helper()
	rows, err := e.sessions.ListArtifactsForRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestSubmitInteractiveTurnRegistersNoFallbackArtifacts pins the owner's
// 2026-11-19 contract: office and coding deliver only what the model
// presents, when the model judges the work complete. The engine registers
// nothing on its own at any terminal branch — a helper script the model
// kept out of the delivery set stays out, even though the ledger tracked
// the write.
func TestSubmitInteractiveTurnRegistersNoFallbackArtifacts(t *testing.T) {
	for _, pt := range []string{state.ProjectTypeOffice, state.ProjectTypeCoding} {
		for _, outcome := range []struct {
			name string
			err  error
		}{
			{"completed", nil},
			{"cancelled", context.Canceled},
			{"failed", errors.New("boom")},
		} {
			t.Run(pt+"/"+outcome.name, func(t *testing.T) {
				env := newDeliverableEnv(t, pt)
				rel := "src/main.go"
				if pt == state.ProjectTypeOffice {
					rel = ".work/helper.py"
				}
				path := env.write(t, rel, "package main\n")
				svc := New(
					WithSessionStore(env.sessions),
					WithRunEventStore(env.runs),
					WithBatchTasks(nil, env.sessions, nil),
					WithRunExecutor(runFunc(func(runCtx context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
						run, err := env.runs.CreateRun(runCtx, env.session, "work")
						if err != nil {
							return nil, err
						}
						started(run.ID)
						if err := env.sessions.RecordFileChange(runCtx, state.FileChange{
							SessionID: env.session, RunID: run.ID, Path: path,
						}); err != nil {
							return nil, err
						}
						return &agent.Result{}, outcome.err
					})),
				)
				out, err := svc.Submit(context.Background(), TurnRequest{SessionID: env.session}, nil)
				if err != nil && outcome.err == nil {
					t.Fatalf("submit: %v", err)
				}
				if rows := env.runArtifacts(t, out.RunID); len(rows) != 0 {
					t.Fatalf("%s %s turn registered engine-side artifacts: %+v", pt, outcome.name, rows)
				}
			})
		}
	}
}

// TestSubmitKeepsModelPresentedArtifacts is the positive control: removing
// the engine-side collection must not touch what the model itself delivered.
func TestSubmitKeepsModelPresentedArtifacts(t *testing.T) {
	env := newDeliverableEnv(t, state.ProjectTypeOffice)
	deliverable := env.write(t, "2026Q3-report.docx", "final")
	scratch := env.write(t, ".work/helper.py", "print(1)\n")
	svc := New(
		WithSessionStore(env.sessions),
		WithRunEventStore(env.runs),
		WithBatchTasks(nil, env.sessions, nil),
		WithRunExecutor(runFunc(func(runCtx context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
			run, err := env.runs.CreateRun(runCtx, env.session, "work")
			if err != nil {
				return nil, err
			}
			started(run.ID)
			for _, p := range []string{deliverable, scratch} {
				if err := env.sessions.RecordFileChange(runCtx, state.FileChange{
					SessionID: env.session, RunID: run.ID, Path: p,
				}); err != nil {
					return nil, err
				}
			}
			if _, err := env.sessions.AddArtifact(runCtx, state.Artifact{
				SessionID: env.session, RunID: run.ID, Path: deliverable,
				RelPath: "2026Q3-report.docx", Name: "2026Q3-report.docx",
				Kind: "document", SHA256: rowsOf(t, deliverable), Origin: state.ArtifactOriginModel,
			}); err != nil {
				return nil, err
			}
			return &agent.Result{}, nil
		})),
	)
	out, err := svc.Submit(context.Background(), TurnRequest{SessionID: env.session}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := env.runArtifacts(t, out.RunID)
	if len(rows) != 1 || rows[0].Origin != state.ArtifactOriginModel || rows[0].Name != "2026Q3-report.docx" {
		t.Fatalf("artifacts = %+v, want exactly the model-presented deliverable", rows)
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
