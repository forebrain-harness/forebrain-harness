package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stubPaidTier records what the port was asked to do.
type stubPaidTier struct {
	freeModel string
	resetAt   time.Time
	calls     int
}

func (s *stubPaidTier) UsePaidTier(_ context.Context, freeModel string, resetAt time.Time) error {
	s.calls++
	s.freeModel, s.resetAt = freeModel, resetAt
	return nil
}

func TestPaidTierErrorIsRecognisedThroughItsWrapper(t *testing.T) {
	reset := time.Now().Add(time.Hour).Round(0)
	inner := &APIError{StatusCode: 400, Code: "6004", Type: "quota_exhausted", Message: "spent"}
	err := error(&PaidTierError{
		Provider: "workbuddy", FreeModel: "hy4free", PaidModel: "hy4std",
		DisplayName: "Hy4", ResetAt: reset, Err: inner,
	})

	var paid *PaidTierError
	if !errors.As(err, &paid) {
		t.Fatal("a PaidTierError must be found through the chain it wraps")
	}
	if paid.FreeModel != "hy4free" || paid.PaidModel != "hy4std" || paid.DisplayName != "Hy4" {
		t.Fatalf("offer = %+v", paid)
	}
	if !paid.ResetAt.Equal(reset) {
		t.Fatalf("reset = %s, want %s", paid.ResetAt, reset)
	}
	// The surfaces' own text and the classification still reach the provider
	// failure underneath.
	var api *APIError
	if !errors.As(err, &api) || api.Code != "6004" {
		t.Fatalf("underlying failure = %v, want the provider's own error", api)
	}
	if err.Error() != inner.Error() {
		t.Fatalf("text = %q, want the provider failure's own", err.Error())
	}
}

func TestPaidTierRegistryAnswersWhoRegistered(t *testing.T) {
	if HasPaidTier("nobody-registered-this") {
		t.Fatal("an unregistered provider has no paid tier")
	}
	if err := UsePaidTier(context.Background(), "nobody-registered-this", "free", time.Time{}); !errors.Is(err, ErrPaidTierUnregistered) {
		t.Fatalf("err = %v, want ErrPaidTierUnregistered", err)
	}

	stub := &stubPaidTier{}
	RegisterPaidTier("PaidTest", stub)
	t.Cleanup(func() { RegisterPaidTier("paidtest", nil) })
	if !HasPaidTier("paidtest") {
		t.Fatal("the registry is keyed by the lowercased provider")
	}
	reset := time.Now().Add(2 * time.Hour).Round(0)
	if err := UsePaidTier(context.Background(), "paidtest", " free ", reset); err != nil {
		t.Fatalf("use: %v", err)
	}
	if stub.calls != 1 || stub.freeModel != "free" || !stub.resetAt.Equal(reset) {
		t.Fatalf("port called %d times with %q/%s", stub.calls, stub.freeModel, stub.resetAt)
	}
}

// TestPaidTierZeroResetIsAProcessOnlyChoice pins the semantic the persistent half
// depends on: a zero reset is a valid port call (the product named no reset), so
// the port — not the registry — decides that nothing may be written.
func TestPaidTierZeroResetIsAProcessOnlyChoice(t *testing.T) {
	stub := &stubPaidTier{}
	RegisterPaidTier("paidtest-zero", stub)
	t.Cleanup(func() { RegisterPaidTier("paidtest-zero", nil) })
	if err := UsePaidTier(context.Background(), "paidtest-zero", "hy4free", time.Time{}); err != nil {
		t.Fatalf("use: %v", err)
	}
	if stub.calls != 1 || !stub.resetAt.IsZero() {
		t.Fatalf("port called %d times with %s, want one call and a zero reset", stub.calls, stub.resetAt)
	}
}
