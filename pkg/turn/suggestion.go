// Prompt suggestion: the engine-side state machine behind the idle composer's
// ghost text. Generation is injected (the surface builds it from the assembly
// service's cached-prefix fork); this file only decides when to offer, when to
// stay silent, and when to throttle after unused offers.
//
// The unused-offer backoff is a strict port of Claude Code 2.1.322's algorithm:
// a session counts its own ignores (unusedInARow); twenty in a row persist a
// global, install-wide streak of 20; while that streak stands (and the install
// is past its grace period and no env override forces the feature on) the offer
// is throttled to one turn in ten rather than paused outright. Accepting an
// offer — submitting it, or taking it into the draft with Tab — or re-enabling
// the feature clears the global streak and resumes every turn. There is no
// notice: a throttled offer is simply quieter, never announced.
package turn

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

// The backoff constants, recovered from CC 2.1.322: r=20 unused offers in a row
// arm the global streak, Sgi=10 is the throttle period once armed, and the
// 14-day grace (CC's _=1209600000ms) keeps a fresh install from throttling at
// all. CC's f=21 notice sentinel and its kill-switch stub N3t() (always false)
// are omitted: the former only dedupes a one-time hint this surface does not
// show, the latter is a no-op.
const (
	defaultSuggestionBackoffThreshold = 20
	defaultSuggestionThrottlePeriod   = 10
	defaultSuggestionBackoffGrace     = 14 * 24 * time.Hour
)

// suggestionEnvOverride forces the feature on regardless of the backoff, the
// way CC's CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=true bypasses v7r(). Any other
// value (including unset) leaves the backoff in force.
const suggestionEnvOverride = "FOREBRAIN_ENABLE_PROMPT_SUGGESTION"

// PromptSuggestionFunc predicts the session's next user message. It returns ""
// (no error) when there is nothing acceptable to offer.
type PromptSuggestionFunc func(ctx context.Context, sessionID, runID string) (string, error)

// SuggestionBackoffStore is the install-wide persistence the backoff reads and
// writes: the global unused-offer streak (shared across every session and
// process, CC's fe().promptSuggestionUnusedStreak) and the install's first-start
// time (CC's firstStartTime, which the grace period measures against). The turn
// package owns the algorithm; the store only holds the two facts, so a surface
// injects a database-backed implementation without the engine importing one.
// A nil store leaves the streak at zero — the backoff never arms.
type SuggestionBackoffStore interface {
	// Streak is the persisted global unused-offer streak (0 when none).
	Streak() int
	// SetStreak persists the global streak.
	SetStreak(int)
	// FirstStart is when the install first started; the zero time means unknown
	// and reads as old enough to throttle.
	FirstStart() time.Time
}

// PromptSuggestionConfig installs prompt suggestion on a Service.
type PromptSuggestionConfig struct {
	// Generate runs the prediction. Nil disables suggestion.
	Generate PromptSuggestionFunc
	// Events receives the prompt_suggested lifecycle. Generation happens outside
	// any Submit call, so it cannot use Submit's sink.
	Events EventSink
	// Surfaces limits suggestion to turns from these surfaces; empty means every
	// surface. A surface with no idle composer to draw the ghost should not list.
	Surfaces []Surface
	// Enabled is the resolved features.prompt_suggestions gate.
	Enabled bool
	// Backoff persists the install-wide unused-offer streak and first-start time.
	// Nil leaves the backoff inert (the streak never arms).
	Backoff SuggestionBackoffStore

	// Overridable in tests.
	backoffThreshold int
	throttlePeriod   int
	backoffGrace     time.Duration
	now              func() time.Time
}

// WithPromptSuggestions offers a next-step prediction after a turn ends, for
// turns from a listed surface.
func WithPromptSuggestions(cfg PromptSuggestionConfig) Option {
	return func(s *Service) {
		if s == nil || cfg.Generate == nil {
			return
		}
		s.suggestions = newSuggester(cfg)
	}
}

// SetPromptSuggestions installs suggestion after construction, for a surface
// whose Generate port closes over the surface itself (see SetAutoContinue).
func (s *Service) SetPromptSuggestions(cfg PromptSuggestionConfig) {
	if s == nil || cfg.Generate == nil {
		return
	}
	s.suggestions = newSuggester(cfg)
}

// SetPromptSuggestionsEnabled moves the features gate of an installed suggester
// without replacing it: replacing would drop the session counters on every
// config sync. Re-enabling is CC's bgi config-toggle path — it clears the global
// streak and every session's unused count, so a re-enable is a fresh start.
func (s *Service) SetPromptSuggestionsEnabled(enabled bool) {
	if s == nil || s.suggestions == nil {
		return
	}
	s.suggestions.setEnabled(enabled)
}

// AcceptSuggestion records the reader taking the offer into the draft (Tab /
// Right / End): the unused-offer backoff clears, so every-turn generation resumes
// at once. A surface calls it the moment the ghost becomes the draft.
func (s *Service) AcceptSuggestion(sessionID string) {
	if s == nil || s.suggestions == nil {
		return
	}
	s.suggestions.Accept(sessionID)
}

// DismissSuggestion hides the offer because the reader started typing their own
// message; generation stays off until the next turn boundary.
func (s *Service) DismissSuggestion(sessionID string) {
	if s == nil || s.suggestions == nil {
		return
	}
	s.suggestions.Dismiss(sessionID)
}

func (g *suggester) setEnabled(enabled bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cfg.Enabled == enabled {
		return
	}
	g.cfg.Enabled = enabled
	if !enabled {
		return
	}
	for _, st := range g.sessions {
		st.unusedInARow = 0
		st.eligibleTurns = 0
	}
	g.clearGlobalStreakLocked()
}

type suggester struct {
	cfg PromptSuggestionConfig

	mu       sync.Mutex
	sessions map[string]*suggestionState
}

type suggestionState struct {
	// current is the ghost text waiting to be drawn; lastOffered survives a
	// dismissal or a Tab-accept so the next submit can still be matched against
	// it (CC keeps its suggestion object until the next submit reads it).
	current     string
	lastOffered string
	// unusedInARow is CC's session le.unusedInARow: ignores since the last accept.
	unusedInARow int
	// eligibleTurns is CC's session le.eligibleTurns: turns counted while the
	// backoff is active, which the throttle period divides.
	eligibleTurns int
	dismissed     bool
	// gen invalidates in-flight generations: any turn boundary or dismissal
	// bumps it, so a prediction that arrives late is dropped rather than shown
	// over a newer state.
	gen uint64
}

func newSuggester(cfg PromptSuggestionConfig) *suggester {
	if cfg.backoffThreshold <= 0 {
		cfg.backoffThreshold = defaultSuggestionBackoffThreshold
	}
	if cfg.throttlePeriod <= 0 {
		cfg.throttlePeriod = defaultSuggestionThrottlePeriod
	}
	if cfg.backoffGrace <= 0 {
		cfg.backoffGrace = defaultSuggestionBackoffGrace
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	return &suggester{cfg: cfg, sessions: map[string]*suggestionState{}}
}

func (g *suggester) stateLocked(sessionID string) *suggestionState {
	st, ok := g.sessions[sessionID]
	if !ok {
		st = &suggestionState{}
		g.sessions[sessionID] = st
	}
	return st
}

func (g *suggester) accepts(origin Origin) bool {
	if len(g.cfg.Surfaces) == 0 {
		return true
	}
	for _, surface := range g.cfg.Surfaces {
		if surface == origin.Surface {
			return true
		}
	}
	return false
}

// suppressed reports turns that must never generate: unattended/batch turns and
// subagent runs have no idle human composer, and a disabled or unlisted surface
// has nowhere to draw the ghost.
func (g *suggester) suppressed(req TurnRequest) bool {
	return !g.cfg.Enabled || req.Unattended || req.BatchInput != nil || req.ParentRunID != "" || !g.accepts(req.Origin)
}

// backoffActiveLocked is CC's v7r(): the global streak has reached the threshold,
// no env override forces the feature on, and the install is past its grace
// period. Caller holds g.mu.
func (g *suggester) backoffActiveLocked() bool {
	if g.cfg.Backoff == nil || g.cfg.Backoff.Streak() < g.cfg.backoffThreshold {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv(suggestionEnvOverride)), "true") {
		return false
	}
	if first := g.cfg.Backoff.FirstStart(); !first.IsZero() {
		if g.cfg.now().Sub(first) < g.cfg.backoffGrace {
			return false
		}
	}
	return true
}

// clearGlobalStreakLocked is CC's m(): a non-zero global streak is reset to zero
// (the resume path an accept or a re-enable takes). Caller holds g.mu.
func (g *suggester) clearGlobalStreakLocked() {
	if g.cfg.Backoff != nil && g.cfg.Backoff.Streak() != 0 {
		g.cfg.Backoff.SetStreak(0)
	}
}

// recordOutcomeLocked is CC's pvn(accepted): an accept clears the session count
// and the global streak; an ignore grows the session count and, at the
// threshold, persists the global streak and starts the session count afresh.
// Caller holds g.mu.
func (g *suggester) recordOutcomeLocked(st *suggestionState, accepted bool) {
	if accepted {
		st.unusedInARow = 0
		g.clearGlobalStreakLocked()
		return
	}
	if g.cfg.Backoff != nil && g.cfg.Backoff.Streak() >= g.cfg.backoffThreshold {
		return
	}
	st.unusedInARow++
	if st.unusedInARow >= g.cfg.backoffThreshold {
		st.unusedInARow = 0
		if g.cfg.Backoff != nil {
			g.cfg.Backoff.SetStreak(g.cfg.backoffThreshold)
		}
	}
}

// turnStarting clears the offer and records whether the previous one was used:
// submitting exactly the suggested text is an acceptance, anything else an
// ignore (CC's onSubmit pvn). The outcome only counts when an offer was actually
// made — a turn with no preceding suggestion records nothing.
func (g *suggester) turnStarting(ctx context.Context, req TurnRequest) {
	if g == nil {
		return
	}
	sid := strings.TrimSpace(req.SessionID)
	g.mu.Lock()
	st := g.stateLocked(sid)
	if st.lastOffered != "" {
		accepted := strings.EqualFold(strings.TrimSpace(req.UserText), strings.TrimSpace(st.lastOffered))
		g.recordOutcomeLocked(st, accepted)
	}
	st.current = ""
	st.lastOffered = ""
	st.dismissed = false
	st.gen++
	g.mu.Unlock()
}

// turnEnded generates a prediction asynchronously after a completed or failed
// turn. Waiting and cancelled turns never reach here. While the backoff is
// active the offer is throttled to one turn in throttlePeriod (CC's Bzo gate):
// eligibleTurns counts the turns since the backoff armed, and only a multiple of
// the period generates.
func (g *suggester) turnEnded(ctx context.Context, req TurnRequest, runID string) {
	if g == nil || g.suppressed(req) {
		return
	}
	sid := strings.TrimSpace(req.SessionID)
	g.mu.Lock()
	st := g.stateLocked(sid)
	if st.dismissed {
		g.mu.Unlock()
		return
	}
	if g.backoffActiveLocked() {
		eligible := st.eligibleTurns%g.cfg.throttlePeriod == 0
		st.eligibleTurns++
		if !eligible {
			g.mu.Unlock()
			return
		}
	}
	st.gen++
	gen := st.gen
	g.mu.Unlock()
	go func() {
		sugg, err := g.cfg.Generate(ctx, sid, runID)
		if err != nil || sugg == "" {
			return
		}
		g.mu.Lock()
		st := g.stateLocked(sid)
		if st.gen != gen || st.dismissed || st.current != "" {
			g.mu.Unlock()
			return
		}
		st.current = sugg
		st.lastOffered = sugg
		g.mu.Unlock()
		g.publishSuggested(ctx, sid, runID, sugg)
	}()
}

// Accept records a Tab/Right/End take of the offer into the draft (CC's
// onTabOnEmpty pvn(true)): the session count and the global streak clear and the
// next turn is eligible again, so taking an offer resumes every-turn generation
// at once. The ghost itself stays until the surface fills the draft; the submit
// that follows still matches lastOffered.
func (g *suggester) Accept(sessionID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	st := g.stateLocked(strings.TrimSpace(sessionID))
	st.unusedInARow = 0
	st.eligibleTurns = 0
	g.clearGlobalStreakLocked()
	g.mu.Unlock()
}

// Dismiss hides the current offer because the user started typing; clearing the
// draft back to empty does not restore it. It does not touch the unused-offer
// count — a dismissal is not an ignore until the next submit says so.
func (g *suggester) Dismiss(sessionID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	st := g.stateLocked(strings.TrimSpace(sessionID))
	st.dismissed = true
	st.current = ""
	st.gen++
	g.mu.Unlock()
}

// Current returns the offer waiting for a session, if any (surface render gate).
func (g *suggester) Current(sessionID string) string {
	if g == nil {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stateLocked(strings.TrimSpace(sessionID)).current
}

func (g *suggester) publishSuggested(ctx context.Context, sessionID, runID, sugg string) {
	if g.cfg.Events == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithoutCancel(ctx)
	id := "prompt-suggestion-" + sessionID + "-" + runID
	_ = g.cfg.Events.Publish(ctx, event.NewRunEvent(id, runID, sessionID, event.RunEventPromptSuggested, event.PromptSuggestedPayload{Suggestion: sugg}, time.Time{}))
}
