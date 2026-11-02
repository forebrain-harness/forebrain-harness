package memory

import (
	"context"
	"testing"
	"time"

	state "github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// TestStage1SkipsBatchSessionsEvenWhenMemoryIsOn pins the generation half of
// §0.2 at the claim: a batch project's task conversation is not a stage-1
// candidate, so turning memory on cannot bill a model call for it.
func TestStage1SkipsBatchSessionsEvenWhenMemoryIsOn(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	projects := state.NewProjectStore(store.DB, store.AgentID())
	project, err := projects.Create(ctx, state.CreateProjectInput{Name: "batch", Root: t.TempDir(), ProjectType: state.ProjectTypeBatch})
	if err != nil {
		t.Fatal(err)
	}
	sessions := state.NewSessionStore(store.DB, store.AgentID())
	if err := sessions.EnsureAt(ctx, "batch-run", "batch-run", state.SessionBirth{ProjectID: project.ID, Source: state.SessionSourceConversation}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	// The process-wide default is "enabled"; the session is born disabled
	// because of the project it belongs to. Idle long enough to be a candidate,
	// and with a cwd so the SQL pre-filter cannot be what skips it.
	if _, err := store.DB.ExecContext(ctx, `UPDATE fb_sessions SET updated_at=?, created_at=?, memory_source=?, cwd=? WHERE id='batch-run'`,
		now-72*3600, now-73*3600, SessionSourceTUI, testCwd); err != nil {
		t.Fatal(err)
	}
	insertMemoryThread(t, store, "coding-thread", ThreadMemoryEnabled, SessionSourceTUI, now-72*3600)
	claims, err := store.ClaimStage1JobsForStartup(ctx, "", 10, 6, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].Thread.ThreadID != "coding-thread" {
		t.Fatalf("claims = %#v, want only the ordinary conversation", claims)
	}
}
