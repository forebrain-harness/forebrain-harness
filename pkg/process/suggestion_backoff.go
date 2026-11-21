// The install-wide persistence behind the prompt-suggestion backoff (plan 344):
// the global unused-offer streak and the install's first-start time, kept in one
// fb_config meta row and cached in memory for the process's lifetime. The turn
// package owns the algorithm and reads these two facts through its
// SuggestionBackoffStore port; this is the database-backed implementation the
// surfaces inject, the way CC keeps promptSuggestionUnusedStreak and
// firstStartTime in its global config.
package process

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// suggestionBackoffSection is the fb_config row holding the backoff's two facts.
// A meta.* row, not a Root section: it is runtime state the configuration never
// marshals, written through ConfigStore.SaveCredential like the legacy-import
// marker.
const suggestionBackoffSection = "meta.prompt_suggestion_backoff"

type suggestionBackoffRow struct {
	UnusedStreak int   `json:"unused_streak"`
	FirstStartMS int64 `json:"first_start_ms"`
}

// suggestionBackoffStore caches the row in memory and writes it through on every
// change. The cache is authoritative within a process — a turn reads the streak
// without a database round-trip — and the row carries it across restarts and
// between the processes that share one install's database.
type suggestionBackoffStore struct {
	store *state.ConfigStore

	mu         sync.Mutex
	loaded     bool
	streak     int
	firstStart time.Time
}

var _ turn.SuggestionBackoffStore = (*suggestionBackoffStore)(nil)

func (s *suggestionBackoffStore) ensureLoaded() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return
	}
	s.loaded = true
	ctx := context.Background()
	raw, err := s.store.LoadCredential(ctx, suggestionBackoffSection)
	if err != nil {
		slog.Warn("prompt-suggestion backoff: read stored state failed; starting fresh", "err", err)
	}
	var row suggestionBackoffRow
	if len(raw) > 0 && json.Unmarshal(raw, &row) == nil {
		s.streak = row.UnusedStreak
		if row.FirstStartMS > 0 {
			s.firstStart = time.UnixMilli(row.FirstStartMS)
		}
	}
	if s.firstStart.IsZero() {
		// The install's first start is seeded once, the first time the backoff
		// state is touched, and never moves afterwards: the grace period measures
		// the install's age from here.
		s.firstStart = time.Now()
		s.persistLocked()
	}
}

func (s *suggestionBackoffStore) persistLocked() {
	body, err := json.Marshal(suggestionBackoffRow{
		UnusedStreak: s.streak,
		FirstStartMS: s.firstStart.UnixMilli(),
	})
	if err != nil {
		return
	}
	if err := s.store.SaveCredential(context.Background(), suggestionBackoffSection, body); err != nil {
		slog.Warn("prompt-suggestion backoff: persist state failed", "err", err)
	}
}

func (s *suggestionBackoffStore) Streak() int {
	s.ensureLoaded()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streak
}

func (s *suggestionBackoffStore) SetStreak(v int) {
	s.ensureLoaded()
	s.mu.Lock()
	defer s.mu.Unlock()
	if v < 0 {
		v = 0
	}
	if s.streak == v {
		return
	}
	s.streak = v
	s.persistLocked()
}

func (s *suggestionBackoffStore) FirstStart() time.Time {
	s.ensureLoaded()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstStart
}

// SuggestionBackoff returns the process-wide prompt-suggestion backoff store,
// building it on first use. A process with no database (a hand-built test
// environment) returns nil, which leaves the engine's backoff inert.
func (env *Environment) SuggestionBackoff() turn.SuggestionBackoffStore {
	if env == nil || env.SQL == nil {
		return nil
	}
	env.suggestionBackoffOnce.Do(func() {
		env.suggestionBackoff = &suggestionBackoffStore{store: &state.ConfigStore{DB: env.SQL}}
	})
	return env.suggestionBackoff
}
