// The paid-tier choice: a request the provider refused because a free tier's
// allowance is spent, on a product that sells the same model row at two rates.
//
// It is the sibling of auto-continue and is deliberately not part of it. A spent
// allowance the runtime can wait out is a wait it resumes by itself
// (autoContinue); a spent free tier that has a paid twin is a choice only the
// reader can make — spend the paid rate now, or stop — and nothing may schedule a
// retry on their behalf. The two therefore share the one thing they must: the
// port that runs a continuation turn (autoContinue.cfg.Continue), so the resend
// is the same turn on every surface.
package turn

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// ErrPaidTierNoOffer is what UsePaidTier answers when the session has no offer
// waiting: the choice it names is not on the table (it expired, it was dismissed,
// or another surface already answered it).
var ErrPaidTierNoOffer = errors.New("turn: no paid-tier offer is waiting")

// PaidTierOffer is one failed turn a surface can satisfy again by switching the
// model row to its paid tier.
type PaidTierOffer struct {
	// SessionID is the conversation the failed turn belongs to: what the offer
	// is filed under and what a surface routes by.
	SessionID string
	// RunID is the run that was refused.
	RunID     string
	Provider  string
	FreeModel string
	PaidModel string
	// DisplayName is the product's name for the pair, "" when it named none.
	DisplayName string
	// ResetAt is when the free tier returns; zero when the product named no
	// reset, in which case the choice lives in this process only.
	ResetAt time.Time
}

// Payload is the offer as the paid_tier_offered event carries it, and as the
// gateway answers a page that asks what is waiting.
func (o PaidTierOffer) Payload() event.PaidTierOfferedPayload {
	out := event.PaidTierOfferedPayload{
		Provider:    o.Provider,
		FreeModel:   o.FreeModel,
		PaidModel:   o.PaidModel,
		DisplayName: o.DisplayName,
	}
	if !o.ResetAt.IsZero() {
		out.ResetAt = o.ResetAt.UTC().Format(time.RFC3339)
	}
	return out
}

// paidTierBoard holds the offers waiting for an answer, per conversation. It is
// memory state of the same kind as a pending continuation: an offer is worth
// nothing after a restart, and its window (when it named one) expires on its own.
type paidTierBoard struct {
	mu      sync.Mutex
	pending map[string]PaidTierOffer
}

// remember files one offer for its conversation, replacing what was there.
func (b *paidTierBoard) remember(offer PaidTierOffer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending == nil {
		b.pending = map[string]PaidTierOffer{}
	}
	b.pending[offer.SessionID] = offer
}

// waiting answers the offer a conversation is waiting on, dropping one whose
// reset has passed: the free tier is back, so the choice is moot.
func (b *paidTierBoard) waiting(sessionID string, now time.Time) (PaidTierOffer, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	offer, ok := b.pending[sessionID]
	if !ok {
		return PaidTierOffer{}, false
	}
	if !offer.ResetAt.IsZero() && !offer.ResetAt.After(now) {
		delete(b.pending, sessionID)
		return PaidTierOffer{}, false
	}
	return offer, true
}

// take removes the offer a conversation is waiting on, answering it. An expired
// offer answers nothing, exactly as it does for waiting.
func (b *paidTierBoard) take(sessionID string, now time.Time) (PaidTierOffer, bool) {
	offer, ok := b.waiting(sessionID, now)
	if !ok {
		return PaidTierOffer{}, false
	}
	b.mu.Lock()
	delete(b.pending, sessionID)
	b.mu.Unlock()
	return offer, true
}

// drop forgets a conversation's offer, reporting whether one was waiting.
func (b *paidTierBoard) drop(sessionID string, now time.Time) bool {
	if _, ok := b.take(sessionID, now); !ok {
		return false
	}
	return true
}

// paidTiers answers the Service's board, creating it on first use.
func (s *Service) paidTiers() *paidTierBoard {
	if s == nil {
		return nil
	}
	s.paidTierOnce.Do(func() {
		if s.paidTier == nil {
			s.paidTier = &paidTierBoard{}
		}
	})
	return s.paidTier
}

// now is the clock the paid-tier board reads: the auto-continue clock when the
// runtime installed one, so a test moves both by moving that one.
func (s *Service) now() time.Time {
	if s != nil && s.autoContinue != nil && s.autoContinue.cfg.now != nil {
		return s.autoContinue.cfg.now()
	}
	return time.Now().Round(0)
}

// offerPaidTier files the choice a refused turn put in front of the reader and
// publishes it. A refusal from a surface with no continuation port, an
// unattended turn, or a child run is not offered: nobody could take it up.
func (s *Service) offerPaidTier(ctx context.Context, req TurnRequest, runID string, err error) {
	if s == nil || err == nil {
		return
	}
	var paid *llm.PaidTierError
	if !errors.As(err, &paid) || paid == nil {
		return
	}
	if strings.TrimSpace(req.ParentRunID) != "" || req.Unattended {
		return
	}
	if s.autoContinue == nil || !s.autoContinue.accepts(req.Origin) {
		return
	}
	offer := PaidTierOffer{
		SessionID:   strings.TrimSpace(req.SessionID),
		RunID:       strings.TrimSpace(runID),
		Provider:    strings.TrimSpace(paid.Provider),
		FreeModel:   strings.TrimSpace(paid.FreeModel),
		PaidModel:   strings.TrimSpace(paid.PaidModel),
		DisplayName: strings.TrimSpace(paid.DisplayName),
		ResetAt:     paid.ResetAt.Round(0),
	}
	if offer.SessionID == "" || offer.Provider == "" || offer.FreeModel == "" || offer.PaidModel == "" {
		return
	}
	s.paidTiers().remember(offer)
	s.publishPaidTier(ctx, offer)
}

// publishPaidTier files the offer in the session log and pushes it to whoever is
// watching. The event id is derived from the offer, so a sink that deduplicates
// by id (the terminal's session log does) draws one offer per refusal.
func (s *Service) publishPaidTier(ctx context.Context, offer PaidTierOffer) {
	if s == nil || s.autoContinue == nil || s.autoContinue.cfg.Events == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// The event outlives the turn that produced it: that turn's context is
	// already finished by the time the reader can answer.
	ctx = context.WithoutCancel(ctx)
	id := "paidtier-" + offer.SessionID + "-" + offer.RunID + "-" + offer.FreeModel
	_ = s.autoContinue.cfg.Events.Publish(ctx, event.NewRunEvent(id, offer.RunID, offer.SessionID, event.RunEventPaidTierOffered, offer.Payload(), s.now()))
}

// PaidTierOffer answers the choice a conversation is waiting on, if any. A
// surface that binds late asks this: the offer is live state, not transcript.
func (s *Service) PaidTierOffer(sessionID string) (PaidTierOffer, bool) {
	board := s.paidTiers()
	if board == nil {
		return PaidTierOffer{}, false
	}
	return board.waiting(strings.TrimSpace(sessionID), s.now())
}

// UsePaidTier records the reader's choice of the paid twin for the offer waiting
// on sessionID, then resends the turn the refusal stopped through the runtime's
// continuation port — the same turn, with the same prompt, auto-continue uses.
//
// The recorded choice is the whole point: without it the resend would be refused
// again. A port that cannot record it (an unregistered provider, a dead login)
// leaves the offer standing so the reader can choose again.
func (s *Service) UsePaidTier(ctx context.Context, sessionID string) error {
	board := s.paidTiers()
	if board == nil {
		return ErrPaidTierNoOffer
	}
	offer, ok := board.take(strings.TrimSpace(sessionID), s.now())
	if !ok {
		return ErrPaidTierNoOffer
	}
	if err := llm.UsePaidTier(ctx, offer.Provider, offer.FreeModel, offer.ResetAt); err != nil {
		board.remember(offer)
		return err
	}
	plan := AutoContinuePlan{
		SessionID: offer.SessionID,
		RunID:     offer.RunID,
		Code:      string(llm.ExplainAllowanceSpent),
		ResetAt:   offer.ResetAt,
	}
	if err := s.autoContinue.deliver(ctx, plan, AutoContinuePrompt); err != nil {
		// The choice stands (the row is the paid tier from now until the
		// reset); only the resend failed, and the reader can send again.
		return err
	}
	return nil
}

// DismissPaidTierOffer drops the offer waiting on sessionID, reporting whether
// there was one. The choice itself is untouched: a reader who dismisses has
// decided not to spend the paid rate, and nothing about the row changes.
func (s *Service) DismissPaidTierOffer(sessionID string) bool {
	board := s.paidTiers()
	if board == nil {
		return false
	}
	return board.drop(strings.TrimSpace(sessionID), s.now())
}
