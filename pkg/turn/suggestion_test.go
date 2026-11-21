package turn

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

type suggSink struct{ events chan event.RunEvent }

func (s suggSink) Publish(_ context.Context, evt event.RunEvent) error {
	select {
	case s.events <- evt:
	default:
	}
	return nil
}

// fakeBackoff is an in-memory SuggestionBackoffStore for tests: it records the
// global streak and a first-start time the grace period measures against.
type fakeBackoff struct {
	mu         sync.Mutex
	streak     int
	firstStart time.Time
}

func (f *fakeBackoff) Streak() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streak
}
func (f *fakeBackoff) SetStreak(v int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streak = v
}
func (f *fakeBackoff) FirstStart() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.firstStart
}

type suggHarness struct {
	g        *suggester
	calls    chan string
	events   chan event.RunEvent
	generate func(ctx context.Context, sessionID, runID string) (string, error)
}

func newSuggHarness(t *testing.T, surfaces []Surface, enabled bool) *suggHarness {
	t.Helper()
	h := &suggHarness{
		calls:  make(chan string, 256),
		events: make(chan event.RunEvent, 256),
	}
	h.generate = func(_ context.Context, sid, _ string) (string, error) {
		h.calls <- sid
		return "run the tests", nil
	}
	h.g = newSuggester(PromptSuggestionConfig{
		Generate: func(ctx context.Context, sid, rid string) (string, error) { return h.generate(ctx, sid, rid) },
		Events:   suggSink{events: h.events},
		Surfaces: surfaces,
		Enabled:  enabled,
	})
	return h
}

// withBackoff installs a fake store whose install is old enough to throttle and
// pins the clock, so the grace period and the throttle are deterministic.
func (h *suggHarness) withBackoff(t *testing.T, streak int, firstStart time.Time) *fakeBackoff {
	t.Helper()
	f := &fakeBackoff{streak: streak, firstStart: firstStart}
	h.g.cfg.Backoff = f
	h.g.cfg.now = func() time.Time { return time.Unix(2_000_000_000, 0) }
	return f
}

func (h *suggHarness) req() TurnRequest {
	return TurnRequest{SessionID: "s1", Origin: Origin{Surface: SurfaceTUI}, UserText: "something else"}
}

func waitCall(t *testing.T, ch chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Generate call")
		return ""
	}
}

func waitEvent(t *testing.T, ch chan event.RunEvent, kind string) event.RunEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.Type == kind {
				return e
			}
		case <-deadline:
			t.Fatalf("timed out waiting for event %s", kind)
			return event.RunEvent{}
		}
	}
}

func noCall(t *testing.T, ch chan string) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("Generate called unexpectedly for %q", v)
	case <-time.After(50 * time.Millisecond):
	}
}

// ignoreOffer drives one full ignore: a turn ends and generates an offer, then
// the next submit carries different text, so the offer goes unused.
func (h *suggHarness) ignoreOffer(t *testing.T, runID string) {
	t.Helper()
	h.g.turnEnded(context.Background(), h.req(), runID)
	waitCall(t, h.calls)
	waitEvent(t, h.events, event.RunEventPromptSuggested)
	h.g.turnStarting(context.Background(), h.req()) // UserText != suggestion => ignore
}

// TestSuggestionSuppressedTurnsNeverGenerate pins the suppression gates: a
// subagent run, a batch/unattended turn, a disabled gate and an unlisted surface
// have no idle human composer, so no prediction is even attempted.
func TestSuggestionSuppressedTurnsNeverGenerate(t *testing.T) {
	variants := []struct {
		name string
		req  TurnRequest
	}{
		{"child", TurnRequest{SessionID: "s1", ParentRunID: "parent", Origin: Origin{Surface: SurfaceTUI}}},
		{"unattended", TurnRequest{SessionID: "s1", Unattended: true, Origin: Origin{Surface: SurfaceTUI}}},
		{"batch", TurnRequest{SessionID: "s1", BatchInput: &BatchTurnInput{}, Origin: Origin{Surface: SurfaceTUI}}},
		{"unlisted", TurnRequest{SessionID: "s1", Origin: Origin{Surface: SurfaceWebChat}}},
	}
	for _, v := range variants {
		h := newSuggHarness(t, []Surface{SurfaceTUI}, true)
		h.g.turnEnded(context.Background(), v.req, "r1")
		noCall(t, h.calls)
	}
	h := newSuggHarness(t, []Surface{SurfaceTUI}, false)
	h.g.turnEnded(context.Background(), h.req(), "r1")
	noCall(t, h.calls)
}

// TestSuggestionGeneratesAndPublishes checks the happy path: a completed turn
// forks a prediction and publishes it as a push-only prompt_suggested event.
func TestSuggestionGeneratesAndPublishes(t *testing.T) {
	h := newSuggHarness(t, []Surface{SurfaceTUI}, true)
	h.g.turnEnded(context.Background(), h.req(), "r1")
	waitCall(t, h.calls)
	e := waitEvent(t, h.events, event.RunEventPromptSuggested)
	if e.SessionID != "s1" || e.RunID != "r1" {
		t.Fatalf("event routing session=%q run=%q", e.SessionID, e.RunID)
	}
	if got := h.g.Current("s1"); got != "run the tests" {
		t.Fatalf("Current=%q want run the tests", got)
	}
}

// TestSuggestionUnusedStreakThrottles drives twenty ignores to the threshold:
// the global streak persists at 20 and generation drops to one turn in ten —
// not a hard pause, and no notice is ever published.
func TestSuggestionUnusedStreakThrottles(t *testing.T) {
	h := newSuggHarness(t, []Surface{SurfaceTUI}, true)
	f := h.withBackoff(t, 0, time.Unix(0, 0))
	for i := 0; i < 20; i++ {
		h.ignoreOffer(t, "r1")
	}
	if got := f.Streak(); got != 20 {
		t.Fatalf("global streak=%d want 20", got)
	}
	// Backoff active: eligibleTurns 0 generates; the submit clears the offer,
	// then the next nine turns skip (eligibleTurns 1..9).
	h.g.turnEnded(context.Background(), h.req(), "r2")
	waitCall(t, h.calls)
	waitEvent(t, h.events, event.RunEventPromptSuggested)
	h.g.turnStarting(context.Background(), h.req())
	for i := 0; i < 9; i++ {
		h.g.turnEnded(context.Background(), h.req(), "r2")
		noCall(t, h.calls)
	}
	// eligibleTurns 10 generates again: one in ten.
	h.g.turnEnded(context.Background(), h.req(), "r2")
	waitCall(t, h.calls)
	waitEvent(t, h.events, event.RunEventPromptSuggested)
	// The throttle is silent: only prompt_suggested ever reaches the sink.
	select {
	case e := <-h.events:
		t.Fatalf("unexpected event during throttle: %s", e.Type)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestSuggestionAcceptResumesEveryTurn: submitting exactly the offered text is
// an acceptance — the global streak clears and every-turn generation resumes.
func TestSuggestionAcceptResumesEveryTurn(t *testing.T) {
	h := newSuggHarness(t, []Surface{SurfaceTUI}, true)
	f := h.withBackoff(t, 0, time.Unix(0, 0))
	for i := 0; i < 20; i++ {
		h.ignoreOffer(t, "r1")
	}
	if f.Streak() != 20 {
		t.Fatalf("streak=%d want 20 before accept", f.Streak())
	}
	// Re-offer so lastOffered is set, then submit it verbatim (case/space-folded).
	h.g.turnEnded(context.Background(), h.req(), "r2")
	waitCall(t, h.calls)
	waitEvent(t, h.events, event.RunEventPromptSuggested)
	accept := h.req()
	accept.UserText = "Run The Tests"
	h.g.turnStarting(context.Background(), accept)
	if got := f.Streak(); got != 0 {
		t.Fatalf("global streak after accept=%d want 0", got)
	}
	// Resumed: the next two turns both generate, no throttle.
	h.g.turnEnded(context.Background(), h.req(), "r3")
	waitCall(t, h.calls)
	waitEvent(t, h.events, event.RunEventPromptSuggested)
	h.g.turnStarting(context.Background(), h.req())
	h.g.turnEnded(context.Background(), h.req(), "r4")
	waitCall(t, h.calls)
	waitEvent(t, h.events, event.RunEventPromptSuggested)
}

// TestSuggestionTabAcceptResumes: taking the offer into the draft (Tab/Right)
// clears the backoff at accept time, before any submit.
func TestSuggestionTabAcceptResumes(t *testing.T) {
	h := newSuggHarness(t, []Surface{SurfaceTUI}, true)
	f := h.withBackoff(t, 20, time.Unix(0, 0))
	h.g.Accept("s1")
	if got := f.Streak(); got != 0 {
		t.Fatalf("global streak after Tab accept=%d want 0", got)
	}
	h.g.turnEnded(context.Background(), h.req(), "r1")
	waitCall(t, h.calls)
	waitEvent(t, h.events, event.RunEventPromptSuggested)
}

// TestSuggestionReEnableResumes: the config toggle path (bgi) clears the global
// streak and the session count, so re-enabling is a fresh start.
func TestSuggestionReEnableResumes(t *testing.T) {
	h := newSuggHarness(t, []Surface{SurfaceTUI}, true)
	f := h.withBackoff(t, 20, time.Unix(0, 0))
	h.g.setEnabled(false)
	h.g.setEnabled(true)
	if got := f.Streak(); got != 0 {
		t.Fatalf("global streak after re-enable=%d want 0", got)
	}
	h.g.turnEnded(context.Background(), h.req(), "r1")
	waitCall(t, h.calls)
	waitEvent(t, h.events, event.RunEventPromptSuggested)
}

// TestSuggestionEnvOverrideBypassesBackoff: FOREBRAIN_ENABLE_PROMPT_SUGGESTION=true
// forces the feature on regardless of the streak (CC's env bypass of v7r).
func TestSuggestionEnvOverrideBypassesBackoff(t *testing.T) {
	t.Setenv(suggestionEnvOverride, "true")
	h := newSuggHarness(t, []Surface{SurfaceTUI}, true)
	h.withBackoff(t, 20, time.Unix(0, 0))
	// Streak is 20 but the override lifts the throttle: every turn generates.
	for i := 0; i < 3; i++ {
		h.g.turnEnded(context.Background(), h.req(), "r1")
		waitCall(t, h.calls)
		waitEvent(t, h.events, event.RunEventPromptSuggested)
		h.g.turnStarting(context.Background(), h.req())
	}
}

// TestSuggestionGracePeriodBlocksBackoff: a fresh install (inside the 14-day
// grace) never throttles, even with the streak already at the threshold.
func TestSuggestionGracePeriodBlocksBackoff(t *testing.T) {
	h := newSuggHarness(t, []Surface{SurfaceTUI}, true)
	now := time.Unix(2_000_000_000, 0)
	// First start one day before now: well inside the grace period.
	h.withBackoff(t, 20, now.Add(-24*time.Hour))
	for i := 0; i < 3; i++ {
		h.g.turnEnded(context.Background(), h.req(), "r1")
		waitCall(t, h.calls)
		waitEvent(t, h.events, event.RunEventPromptSuggested)
		h.g.turnStarting(context.Background(), h.req())
	}
}

// TestSuggestionDismissHidesAndBlocks: typing dismisses the offer; a dismissed
// session neither shows nor regenerates until a new turn boundary.
func TestSuggestionDismissHidesAndBlocks(t *testing.T) {
	h := newSuggHarness(t, []Surface{SurfaceTUI}, true)
	h.g.turnEnded(context.Background(), h.req(), "r1")
	waitCall(t, h.calls)
	waitEvent(t, h.events, event.RunEventPromptSuggested)
	h.g.Dismiss("s1")
	if got := h.g.Current("s1"); got != "" {
		t.Fatalf("Current after dismiss=%q want empty", got)
	}
	h.g.turnEnded(context.Background(), h.req(), "r2")
	noCall(t, h.calls)
}

// TestSuggestionStaleGenerationDropped: a prediction that arrives after a newer
// turn boundary must not overwrite the newer state.
func TestSuggestionStaleGenerationDropped(t *testing.T) {
	h := newSuggHarness(t, []Surface{SurfaceTUI}, true)
	release := make(chan struct{})
	h.generate = func(_ context.Context, sid, _ string) (string, error) {
		h.calls <- sid
		<-release
		return "run the tests", nil
	}
	h.g.turnEnded(context.Background(), h.req(), "r1")
	waitCall(t, h.calls)
	h.g.turnStarting(context.Background(), h.req()) // bumps gen, invalidates r1
	close(release)
	select {
	case e := <-h.events:
		t.Fatalf("stale generation published %s", e.Type)
	case <-time.After(50 * time.Millisecond):
	}
	if got := h.g.Current("s1"); got != "" {
		t.Fatalf("stale generation set Current=%q", got)
	}
}
