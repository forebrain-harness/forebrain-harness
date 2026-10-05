package run

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// A session the database already holds a live primary run in refuses a second
// one — from any process — and the refusal reaches the caller as the sentence
// the store wrote for the person. Wrapping it in a "create run:" prefix would
// bury both the sentence and the code a surface localises it by.
func TestRunReturnsTheSessionBusyRefusalAsItIs(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenStateForTest error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := state.NewSessionStore(db, "main").Ensure(ctx, "sid", "sid"); err != nil {
		t.Fatal(err)
	}
	// Another live process: it holds a lease and its run is in flight.
	other := &state.RunStore{DB: db, Owner: "other-process"}
	stop, err := other.HoldOwnerLease(ctx)
	if err != nil {
		t.Fatalf("HoldOwnerLease error: %v", err)
	}
	defer stop()
	if _, err := other.CreateRun(ctx, "sid", "in flight"); err != nil {
		t.Fatalf("CreateRun error: %v", err)
	}

	mine := &state.RunStore{DB: db, Owner: "this-process"}
	_, _, err = Run(Options{
		RunRT:  mine,
		Runner: &Runner{},
		Hooks:  hook.NewAgentPipeline(),
		HC:     hook.HookContext{SessionID: "sid"},
		Input:  "hello",
	})
	if !errors.Is(err, state.ErrSessionBusy) {
		t.Fatalf("error = %v, want the session-busy family", err)
	}
	if !errors.Is(err, state.ErrSessionRunning) {
		t.Fatalf("error = %v, want the running member", err)
	}
	const sentence = "This conversation is already running a turn; send again when it finishes."
	if err.Error() != sentence {
		t.Fatalf("error text = %q, want the store's own sentence %q", err.Error(), sentence)
	}
}
