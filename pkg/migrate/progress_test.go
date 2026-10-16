package migrate

import (
	"reflect"
	"testing"
)

// TestPercentTrackerSlicesEnabledStages pins the static slicing: sessions owns
// [0,90), the other enabled categories share [90,100) in execution order, and
// an excluded category occupies nothing.
func TestPercentTrackerSlicesEnabledStages(t *testing.T) {
	tracker := newPercentTracker([]string{"sessions", "memories", "skills", "plans", "mcp", "lsp", "history", "consolidating"})
	want := []progressSlice{
		{"sessions", 0, 90},
		{"memories", 90, 91.42857142857143},
		{"skills", 91.42857142857143, 92.85714285714286},
		{"plans", 92.85714285714286, 94.28571428571429},
		{"mcp", 94.28571428571429, 95.71428571428572},
		{"lsp", 95.71428571428572, 97.14285714285715},
		{"history", 97.14285714285715, 98.57142857142858},
		{"consolidating", 98.57142857142858, 100},
	}
	if !reflect.DeepEqual(tracker.slices, want) {
		t.Fatalf("slices = %+v, want %+v", tracker.slices, want)
	}

	// Only sessions: one slice, and nothing else to hand the tail to — so the
	// count-driven stage fills the bar instead of stopping at the 90 the
	// ticks-tail would have left it (TestPercentTrackerSessionsAloneFillsTheBar
	// is the focused regression for this).
	only := newPercentTracker([]string{"sessions"})
	if len(only.slices) != 1 || only.slices[0].stage != "sessions" || only.slices[0].hi != 100 {
		t.Fatalf("sessions-only slices = %+v", only.slices)
	}

	// Sessions skipped with Only: the remaining categories share the WHOLE
	// bar, not the tenth a sessions tail would have left them.
	noSessions := newPercentTracker(enabledStages(&Options{Only: []string{"memories", "plans"}}, []string{"sessions", "memories", "skills", "plans"}))
	wantRestricted := []progressSlice{{"memories", 0, 50}, {"plans", 50, 100}}
	if !reflect.DeepEqual(noSessions.slices, wantRestricted) {
		t.Fatalf("restricted slices = %+v, want %+v", noSessions.slices, wantRestricted)
	}
}

// TestPercentTrackerStageTransitions pins the state machine: discovering is
// pinned to 0, sessions interpolates by its real counts, a stage change snaps
// to the new slice's start, and the value never reaches 100 while running.
func TestPercentTrackerStageTransitions(t *testing.T) {
	tracker := newPercentTracker([]string{"sessions", "memories", "plans"})
	if got := tracker.observe("discovering", 0, 0); got != 0 {
		t.Fatalf("discovering = %d, want 0", got)
	}
	if got := tracker.observe("sessions", 0, 10); got != 0 {
		t.Fatalf("sessions 0/10 = %d, want 0", got)
	}
	if got := tracker.observe("sessions", 5, 10); got != 45 {
		t.Fatalf("sessions 5/10 = %d, want 45", got)
	}
	if got := tracker.observe("sessions", 10, 10); got != 90 {
		t.Fatalf("sessions 10/10 = %d, want 90 (its slice top)", got)
	}
	if got := tracker.observe("memories", 0, 0); got != 90 {
		t.Fatalf("memories opens at %d, want 90", got)
	}
	if got := tracker.observe("plans", 0, 0); got != 95 {
		t.Fatalf("plans opens at %d, want 95", got)
	}
	// An unknown stage keeps the last position instead of guessing.
	if got := tracker.observe("not-a-stage", 3, 7); got != 95 {
		t.Fatalf("unknown stage moved the bar: %d", got)
	}
}

// TestPercentTrackerClampsBelow100 pins that the live bar never claims a
// finished run: the last slice's positions clamp at 99.
func TestPercentTrackerClampsBelow100(t *testing.T) {
	tracker := newPercentTracker([]string{"sessions", "memories"})
	if got := tracker.observe("sessions", 4, 4); got != 90 {
		t.Fatalf("sessions top = %d, want 90", got)
	}
	if got := tracker.observe("memories", 0, 0); got != 90 {
		t.Fatalf("memories opens at %d, want 90", got)
	}
	// A single tail category spans [90,100); its checkpoints sit at its
	// start, so the clamp is the guard that holds even if slicing changes.
	tight := newPercentTracker([]string{"sessions", "memories"})
	if got := tight.observe("memories", 0, 0); got >= 100 {
		t.Fatalf("bar reached %d while the run is live", got)
	}
}

// TestWithPercentProgressFillsEvents pins the wrapper: every checkpoint the
// surface receives carries the derived percent, and a nil callback stays nil.
func TestWithPercentProgressFillsEvents(t *testing.T) {
	var percents []int
	callback := withPercentProgress(func(event Progress) {
		percents = append(percents, event.Percent)
	}, claudeStageOrder(&Options{}))
	callback(Progress{Stage: "discovering"})
	callback(Progress{Stage: "sessions", Done: 1, Total: 4})
	callback(Progress{Stage: "memories"})
	if !reflect.DeepEqual(percents, []int{0, 22, 90}) {
		t.Fatalf("percents = %v", percents)
	}
	if withPercentProgress(nil, nil) != nil {
		t.Fatal("nil callback must stay nil")
	}
}

// TestPercentTrackerSessionsAloneFillsTheBar pins that a count-driven stage
// that is the whole run fills the bar: with no tail to hand the remainder to,
// sessions owns [0,100) and the live clamp reads 99, not the share a tail
// would have taken.
func TestPercentTrackerSessionsAloneFillsTheBar(t *testing.T) {
	tracker := newPercentTracker([]string{"sessions"})
	if len(tracker.slices) != 1 || tracker.slices[0].stage != "sessions" || tracker.slices[0].hi != 100 {
		t.Fatalf("sessions-only slices = %+v, want one slice over [0,100)", tracker.slices)
	}
	if got := tracker.observe("sessions", 1, 1); got != 99 {
		t.Fatalf("sessions-only top = %d, want 99 (clamped below 100)", got)
	}
}

// TestPercentTrackerSessionsAbsentSpansTheWholeBar pins that the tick-driven
// categories share the whole bar when the count-driven stage does not run:
// their slices are not squeezed into the tail a sessions stage would leave.
func TestPercentTrackerSessionsAbsentSpansTheWholeBar(t *testing.T) {
	tracker := newPercentTracker([]string{"memories", "plans"})
	want := []progressSlice{{"memories", 0, 50}, {"plans", 50, 100}}
	if !reflect.DeepEqual(tracker.slices, want) {
		t.Fatalf("slices = %+v, want %+v", tracker.slices, want)
	}
	if got := tracker.observe("plans", 0, 0); got != 50 {
		t.Fatalf("plans opens at %d, want 50", got)
	}
}

// TestPercentTrackerKeepsPositionOnUnknownStage pins the fallback the type's
// own doc comment promises: a stage outside the plan holds the bar where it
// stands instead of snapping back to the current slice's start.
func TestPercentTrackerKeepsPositionOnUnknownStage(t *testing.T) {
	tracker := newPercentTracker([]string{"sessions", "memories"})
	if got := tracker.observe("sessions", 5, 10); got != 45 {
		t.Fatalf("sessions 5/10 = %d, want 45", got)
	}
	if got := tracker.observe("not-a-stage", 0, 0); got != 45 {
		t.Fatalf("unknown stage = %d, want 45 (the last published position)", got)
	}
}
