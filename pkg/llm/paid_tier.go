// The port between a provider that sells one model at two rates and the runtime
// that asks the reader which rate to spend.
package llm

import (
	"context"
	"strings"
	"sync"
	"time"
)

// PaidTierError is one failed request a provider may satisfy again by switching
// the same model row to its paid tier: the pair, the display name a surface
// shows, and when the free allowance returns (zero when the product named none).
//
// It carries the provider and the model itself because the runtime holds no
// llm.LLM to ask (pkg/turn constructs none) and llm.APIError carries no model: an
// error that reaches a turn has to name what the reader is being asked about.
type PaidTierError struct {
	// Provider is the model row's provider ("workbuddy").
	Provider string
	// FreeModel is the row's own model — the free tier that ran out.
	FreeModel string
	// PaidModel is the twin the same row is sent as once the reader agrees.
	PaidModel string
	// DisplayName is the product's name for the pair ("Hy4"), what a surface
	// names in the choice; "" falls back to PaidModel.
	DisplayName string
	// ResetAt is when the free tier returns, zero when the product named no
	// reset — in which case the choice cannot outlive this process.
	ResetAt time.Time
	// Err is the underlying provider failure, for the surfaces' own text.
	Err error
}

func (e *PaidTierError) Error() string {
	if e == nil || e.Err == nil {
		return "the free tier's allowance is spent"
	}
	return e.Err.Error()
}

func (e *PaidTierError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// PaidTier is what a provider does with the reader's choice.
type PaidTier interface {
	// UsePaidTier records the opt-in for the pair's free model. A valid ResetAt
	// persists it until that time; a zero ResetAt keeps it in this process only
	// (the product named no reset, so there is nothing to write it against).
	// Idempotent.
	UsePaidTier(ctx context.Context, freeModel string, resetAt time.Time) error
}

// ErrPaidTierUnregistered is what UsePaidTier answers for a provider that never
// registered the port: the choice cannot be recorded, so the surface must not
// pretend it was.
var ErrPaidTierUnregistered = errPaidTierUnregistered{}

type errPaidTierUnregistered struct{}

func (errPaidTierUnregistered) Error() string {
	return "llm: this provider offers no paid tier"
}

var (
	paidTiersMu sync.RWMutex
	paidTiers   = map[string]PaidTier{}
)

// RegisterPaidTier installs what a provider does with the switch. Registering a
// nil tier or an empty provider is a no-op; re-registering replaces.
func RegisterPaidTier(provider string, tier PaidTier) {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "" || tier == nil {
		return
	}
	paidTiersMu.Lock()
	paidTiers[p] = tier
	paidTiersMu.Unlock()
}

// HasPaidTier reports whether a provider registered the port.
func HasPaidTier(provider string) bool {
	p := strings.ToLower(strings.TrimSpace(provider))
	paidTiersMu.RLock()
	_, ok := paidTiers[p]
	paidTiersMu.RUnlock()
	return ok
}

// UsePaidTier records the reader's choice for one pair through its provider's
// port.
func UsePaidTier(ctx context.Context, provider, freeModel string, resetAt time.Time) error {
	p := strings.ToLower(strings.TrimSpace(provider))
	paidTiersMu.RLock()
	tier := paidTiers[p]
	paidTiersMu.RUnlock()
	if tier == nil {
		return ErrPaidTierUnregistered
	}
	return tier.UsePaidTier(ctx, strings.TrimSpace(freeModel), resetAt)
}
