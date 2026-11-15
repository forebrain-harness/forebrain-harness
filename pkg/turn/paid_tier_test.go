package turn

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// capRefusal is the failure a product that sells a free tier and its paid twin
// answers with: the plugin has already recognized it, so what reaches the turn is
// a PaidTierError.
func capRefusal(resetAt time.Time) error {
	return &llm.PaidTierError{
		Provider:    "paidturn",
		FreeModel:   "hy4free",
		PaidModel:   "hy4std",
		DisplayName: "Hy4",
		ResetAt:     resetAt,
		Err: &llm.APIError{
			StatusCode: 400,
			Code:       "6004",
			Type:       "quota_exhausted",
			Message:    "免费额度已用尽，可在 " + resetAt.Format(time.RFC3339) + " 重置",
		},
	}
}

// paidTierStub records what the switch was asked to write.
type paidTierStub struct {
	mu        chan struct{}
	freeModel string
	resetAt   time.Time
	calls     int
	err       error
}

func newPaidTierStub() *paidTierStub {
	stub := &paidTierStub{mu: make(chan struct{}, 1)}
	stub.mu <- struct{}{}
	return stub
}

func (s *paidTierStub) UsePaidTier(_ context.Context, freeModel string, resetAt time.Time) error {
	<-s.mu
	defer func() { s.mu <- struct{}{} }()
	s.calls++
	s.freeModel, s.resetAt = freeModel, resetAt
	return s.err
}

func (s *paidTierStub) state() (int, string, time.Time, error) {
	<-s.mu
	defer func() { s.mu <- struct{}{} }()
	return s.calls, s.freeModel, s.resetAt, s.err
}

// TestPaidTierOfferIsAnnouncedInsteadOfAWait is the core of the port: a cap
// refusal that names a paid twin is a choice, so nothing schedules a retry — the
// offer is what a surface gets.
func TestPaidTierOfferIsAnnouncedInsteadOfAWait(t *testing.T) {
	h := newAutoContinueHarness(t)
	resetAt := h.clock.Now().Add(3 * time.Hour)
	h.submit(t, capRefusal(resetAt), SurfaceTUI)

	if _, ok := h.svc.PendingAutoContinue("s1"); ok {
		t.Fatal("a spent free tier with a paid twin must not be continued automatically: only the reader can decide")
	}
	offer, ok := h.svc.PaidTierOffer("s1")
	if !ok {
		t.Fatal("no paid-tier offer waiting after the refusal")
	}
	if offer.Provider != "paidturn" || offer.FreeModel != "hy4free" || offer.PaidModel != "hy4std" || offer.DisplayName != "Hy4" {
		t.Fatalf("offer = %+v", offer)
	}
	if !offer.ResetAt.Equal(resetAt) || offer.RunID != "run-1" || offer.SessionID != "s1" {
		t.Fatalf("offer times/ids = %+v", offer)
	}

	var payload event.PaidTierOfferedPayload
	evt := h.sink.last(t, event.RunEventPaidTierOffered, &payload)
	if evt.Type != "paid_tier_offered" {
		t.Fatalf("event name = %q, want paid_tier_offered", evt.Type)
	}
	if evt.RunID != "run-1" || evt.SessionID != "s1" {
		t.Fatalf("offer filed under run %q session %q", evt.RunID, evt.SessionID)
	}
	if payload.FreeModel != "hy4free" || payload.PaidModel != "hy4std" || payload.DisplayName != "Hy4" ||
		payload.ResetAt != resetAt.UTC().Format(time.RFC3339) {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestAutoContinueDoesNotTakeACapRefusal(t *testing.T) {
	h := newAutoContinueHarness(t)
	// The same refusal without the plugin's wrapper: the classification alone
	// must stay out of auto-continue's switch.
	resetAt := h.clock.Now().Add(2 * time.Hour)
	h.submit(t, &llm.APIError{
		StatusCode: 400,
		Code:       "6004",
		Type:       "quota_exhausted",
		Message:    "免费额度已用尽，可在 " + resetAt.Format(time.RFC3339) + " 重置",
	}, SurfaceTUI)
	if _, ok := h.svc.PendingAutoContinue("s1"); ok {
		t.Fatal("an allowance-spent refusal is a choice, not a wait: auto-continue must not arm")
	}
}

func TestUsePaidTierRecordsTheChoiceAndResends(t *testing.T) {
	stub := newPaidTierStub()
	llm.RegisterPaidTier("paidturn", stub)
	t.Cleanup(func() { llm.RegisterPaidTier("paidturn", nil) })

	h := newAutoContinueHarness(t)
	resetAt := h.clock.Now().Add(3 * time.Hour)
	h.submit(t, capRefusal(resetAt), SurfaceWebChat)

	if err := h.svc.UsePaidTier(context.Background(), "s1"); err != nil {
		t.Fatalf("use paid tier: %v", err)
	}
	calls, free, reset, _ := stub.state()
	if calls != 1 || free != "hy4free" || !reset.Equal(resetAt) {
		t.Fatalf("port called %d times with %q/%s, want hy4free at the offer's reset", calls, free, reset)
	}
	continued := h.continuations()
	if len(continued) != 1 {
		t.Fatalf("continuations = %d, want the turn resent once", len(continued))
	}
	if continued[0].prompt != AutoContinuePrompt || continued[0].plan.SessionID != "s1" || continued[0].plan.RunID != "run-1" {
		t.Fatalf("resend = %+v, want the shared continuation prompt for the same session", continued[0])
	}
	if _, ok := h.svc.PaidTierOffer("s1"); ok {
		t.Fatal("the offer stands after it was answered")
	}
	if err := h.svc.UsePaidTier(context.Background(), "s1"); !errors.Is(err, ErrPaidTierNoOffer) {
		t.Fatalf("second answer = %v, want ErrPaidTierNoOffer", err)
	}
}

// TestPaidTierOfferExpiresWithItsReset pins the window: once the free tier is
// back the choice is moot, so the offer is gone before anyone can take it.
func TestPaidTierOfferExpiresWithItsReset(t *testing.T) {
	h := newAutoContinueHarness(t)
	resetAt := h.clock.Now().Add(time.Hour)
	h.submit(t, capRefusal(resetAt), SurfaceTUI)

	h.clock.Advance(2 * time.Hour)
	if _, ok := h.svc.PaidTierOffer("s1"); ok {
		t.Fatal("the offer outlived the reset it was made for")
	}
	if err := h.svc.UsePaidTier(context.Background(), "s1"); !errors.Is(err, ErrPaidTierNoOffer) {
		t.Fatalf("use after the reset = %v, want ErrPaidTierNoOffer", err)
	}
	if got := h.continuations(); len(got) != 0 {
		t.Fatalf("resent %+v after the reset, want nothing", got)
	}
}

func TestDismissPaidTierOfferDropsTheChoice(t *testing.T) {
	h := newAutoContinueHarness(t)
	h.submit(t, capRefusal(h.clock.Now().Add(time.Hour)), SurfaceTUI)

	if !h.svc.DismissPaidTierOffer("s1") {
		t.Fatal("dismiss must report the offer it dropped")
	}
	if h.svc.DismissPaidTierOffer("s1") {
		t.Fatal("a second dismiss drops nothing")
	}
	if _, ok := h.svc.PaidTierOffer("s1"); ok {
		t.Fatal("the dismissed offer is still on the table")
	}
	if got := h.continuations(); len(got) != 0 {
		t.Fatalf("dismissing resent the turn: %+v", got)
	}
}

// TestUsePaidTierKeepsTheOfferWhenThePortRefuses pins the failure path: a choice
// that could not be written must stay on the table rather than vanish.
func TestUsePaidTierKeepsTheOfferWhenThePortRefuses(t *testing.T) {
	stub := newPaidTierStub()
	stub.err = errors.New("the login is gone")
	llm.RegisterPaidTier("paidturn", stub)
	t.Cleanup(func() { llm.RegisterPaidTier("paidturn", nil) })

	h := newAutoContinueHarness(t)
	h.submit(t, capRefusal(h.clock.Now().Add(time.Hour)), SurfaceTUI)

	if err := h.svc.UsePaidTier(context.Background(), "s1"); err == nil {
		t.Fatal("a refusing port must report its failure")
	}
	if _, ok := h.svc.PaidTierOffer("s1"); !ok {
		t.Fatal("the offer must stay on the table when the choice could not be recorded")
	}
	if got := h.continuations(); len(got) != 0 {
		t.Fatalf("resent %+v although the choice was not recorded", got)
	}
}
