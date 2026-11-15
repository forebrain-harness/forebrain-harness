// ratelimit.go owns the per-account record of a spent free tier, and the two
// decisions that record feeds: which id a request is sent as, and whether a
// surface offers the paid twin.
//
// It is the plugin's own state file rather than a row in the state database,
// for the reason the credential lives in a file: the transport is constructed
// with the home directory alone (plugin.go's factory holds no *sql.DB), and the
// two facts have the same lifetime — a login's allowance, on a login's account.
// The file is written under the same per-path credential lock every other
// credential write takes, so two processes cannot interleave a write.
//
// The names follow the official client's model-rate-limit-cap module
// (markCapHit, markPaidOptIn, isCapHit, resolveEffectiveCapHit), because the
// semantics are its own: an allowance spent until a stated reset, an opt-in that
// lives only as long as that reset, and a hit the product stated no reset for,
// which may only live in this process.
package workbuddy

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// rateLimitNow is the clock this file reads, so a test can walk past a reset
// without waiting for one. Rounded to the wall clock, like every other time
// compared against a provider's stated reset.
var rateLimitNow = func() time.Time { return time.Now().Round(0) }

// rateLimitStateFile is the state file's name under the plugin's home.
const rateLimitStateFile = "workbuddy-rate-limits.json"

// rateLimitStatePath is where the record lives: beside the credential file, in
// the home the plugin was constructed with.
func rateLimitStatePath(home string) string {
	return filepath.Join(home, rateLimitStateFile)
}

// capHitAccountKey is the bucket one account's entries live under: the client's
// own ${uid}:${enterpriseId}, with an empty enterprise for a personal account.
func capHitAccountKey(creds Credentials) string {
	return strings.TrimSpace(creds.UID) + ":" + strings.TrimSpace(creds.EnterpriseID)
}

// capHit is one free tier's record: when its allowance returns, and whether the
// reader chose to spend the paid twin until then.
type capHit struct {
	// ResetAt is the reset in unix milliseconds, the unit the client stores.
	ResetAt int64 `json:"resetAt"`
	// OptedIn is the reader's choice to use the paid twin until ResetAt.
	OptedIn bool `json:"optedIn,omitempty"`
}

// validCapReset reports whether t names a reset still ahead: a time in the past
// is not a window, and the client refuses to record one.
func validCapReset(t time.Time, now time.Time) bool {
	return !t.IsZero() && t.After(now)
}

// rateLimitCache is the last state read, keyed by the file's identity: a request
// path that asks whether an opt-in is in force must not read a file per request,
// and a write (which holds the lock) refreshes it.
var rateLimitCache struct {
	mu      sync.Mutex
	path    string
	modTime time.Time
	size    int64
	state   rateLimitState
	valid   bool
}

// rateLimitState is the file: one bucket per account, one entry per free id.
type rateLimitState map[string]map[string]capHit

// readRateLimitState answers the record on disk, from the cache when the file
// has not moved. An unreadable file reads as no record: nothing about a spent
// allowance is worth failing a request over.
func readRateLimitState(home string) rateLimitState {
	path := rateLimitStatePath(home)
	info, err := os.Stat(path)
	if err != nil {
		return rateLimitState{}
	}
	rateLimitCache.mu.Lock()
	defer rateLimitCache.mu.Unlock()
	if rateLimitCache.valid && rateLimitCache.path == path &&
		rateLimitCache.modTime.Equal(info.ModTime()) && rateLimitCache.size == info.Size() {
		return rateLimitCache.state
	}
	state := decodeRateLimitState(path)
	rateLimitCache.path, rateLimitCache.modTime, rateLimitCache.size = path, info.ModTime(), info.Size()
	rateLimitCache.state, rateLimitCache.valid = state, true
	return state
}

// decodeRateLimitState reads and decodes one state file, answering an empty
// record for anything it cannot read.
func decodeRateLimitState(path string) rateLimitState {
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("workbuddy: could not read the rate-limit record", "path", path, "err", err)
		}
		return rateLimitState{}
	}
	state := rateLimitState{}
	if err := json.Unmarshal(raw, &state); err != nil {
		slog.Warn("workbuddy: the rate-limit record is not readable", "path", path, "err", err)
		return rateLimitState{}
	}
	return state
}

// writeRateLimitState replaces the state file atomically and refreshes the
// cache. Caller holds the path's credential lock.
func writeRateLimitState(home string, state rateLimitState) error {
	path := rateLimitStatePath(home)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	rateLimitCache.mu.Lock()
	rateLimitCache.valid = false
	rateLimitCache.mu.Unlock()
	return nil
}

// updateRateLimitState applies change to the record under the credential lock,
// re-reading the file inside it so a write from another process is never
// clobbered. change reports whether it changed anything; an unchanged record is
// not rewritten.
func updateRateLimitState(home string, change func(rateLimitState) bool) error {
	path := rateLimitStatePath(home)
	mutex := llm.CredentialMutex(path)
	mutex.Lock()
	defer mutex.Unlock()
	state := decodeRateLimitState(path)
	if state == nil {
		state = rateLimitState{}
	}
	if !change(state) {
		return nil
	}
	return writeRateLimitState(home, state)
}

// markCapHit records that freeID's allowance is spent until resetAt, keeping an
// opt-in already in force for the same window (the client does the same). A
// reset that is missing or already past is not recorded: there is no window to
// keep a choice in.
func markCapHit(home, account, freeID string, resetAt time.Time) error {
	if strings.TrimSpace(account) == "" || strings.TrimSpace(freeID) == "" {
		return nil
	}
	if !validCapReset(resetAt, rateLimitNow()) {
		return nil
	}
	millis := resetAt.UnixMilli()
	return updateRateLimitState(home, func(state rateLimitState) bool {
		bucket := state[account]
		if bucket == nil {
			bucket = map[string]capHit{}
			state[account] = bucket
		}
		previous := bucket[freeID]
		if previous.ResetAt == millis {
			return false
		}
		bucket[freeID] = capHit{ResetAt: millis, OptedIn: previous.OptedIn}
		return true
	})
}

// markPaidOptIn records the reader's choice to spend the paid twin for the rest
// of the window freeID's allowance is spent for. It needs that window: an
// opt-in with no reset to expire with could never be undone, so the client
// refuses one too, and the live half of the record (setLiveCapHit) is where a
// reset-less hit keeps its choice.
func markPaidOptIn(home, account, freeID string) error {
	if strings.TrimSpace(account) == "" || strings.TrimSpace(freeID) == "" {
		return nil
	}
	now := rateLimitNow()
	return updateRateLimitState(home, func(state rateLimitState) bool {
		bucket := state[account]
		entry, ok := bucket[freeID]
		if !ok || entry.OptedIn || !validCapReset(time.UnixMilli(entry.ResetAt), now) {
			return false
		}
		entry.OptedIn = true
		bucket[freeID] = entry
		return true
	})
}

// capHitResetAt answers when freeID's allowance returns, dropping the entry when
// that moment has passed — the client's isCapHit, whose expiry deletes too.
func capHitResetAt(home, account, freeID string) (time.Time, bool) {
	if strings.TrimSpace(account) == "" || strings.TrimSpace(freeID) == "" {
		return time.Time{}, false
	}
	now := rateLimitNow()
	entry, ok := readRateLimitState(home)[account][freeID]
	if !ok || entry.ResetAt <= 0 {
		return time.Time{}, false
	}
	reset := time.UnixMilli(entry.ResetAt)
	if !reset.After(now) {
		_ = expireCapHit(home, account, freeID, now)
		return time.Time{}, false
	}
	return reset, true
}

// paidOptedIn reports whether the reader's paid-tier choice is still in force
// for freeID: an expired window is dropped as it is read.
func paidOptedIn(home, account, freeID string) bool {
	entry, ok := readRateLimitState(home)[account][freeID]
	if !ok || !entry.OptedIn {
		return false
	}
	if entry.ResetAt <= 0 || !time.UnixMilli(entry.ResetAt).After(rateLimitNow()) {
		_ = expireCapHit(home, account, freeID, rateLimitNow())
		return false
	}
	return true
}

// expireCapHit drops one entry whose window has passed.
func expireCapHit(home, account, freeID string, now time.Time) error {
	return updateRateLimitState(home, func(state rateLimitState) bool {
		bucket := state[account]
		entry, ok := bucket[freeID]
		if !ok || time.UnixMilli(entry.ResetAt).After(now) {
			return false
		}
		delete(bucket, freeID)
		if len(bucket) == 0 {
			delete(state, account)
		}
		return true
	})
}

// liveCapHits is the half of the record that never reaches the disk: a cap hit
// the product stated no reset for (the client's setLiveBannerCapHit). The
// choice it carries dies with this process, which is what keeps an undated
// window from becoming permanent.
var liveCapHits struct {
	mu   sync.RWMutex
	keys map[string]liveCapHit
}

// liveCapHit is one process-local hit: whether the tier is spent and whether the
// reader picked the paid twin.
type liveCapHit struct {
	spent   bool
	optedIn bool
}

// liveCapKey scopes a live hit to the home, the account and the free id.
func liveCapKey(home, account, freeID string) string {
	return rateLimitStatePath(home) + "|" + account + "|" + freeID
}

// setLiveCapHit records a cap hit with no reset (spent true) or clears it.
func setLiveCapHit(home, account, freeID string, spent bool) {
	if strings.TrimSpace(account) == "" || strings.TrimSpace(freeID) == "" {
		return
	}
	key := liveCapKey(home, account, freeID)
	liveCapHits.mu.Lock()
	defer liveCapHits.mu.Unlock()
	if !spent {
		delete(liveCapHits.keys, key)
		return
	}
	if liveCapHits.keys == nil {
		liveCapHits.keys = map[string]liveCapHit{}
	}
	entry := liveCapHits.keys[key]
	entry.spent = true
	liveCapHits.keys[key] = entry
}

// markLivePaidOptIn records the reader's choice for a hit that named no reset. It
// is the only place that choice may live, and nothing is written to disk.
func markLivePaidOptIn(home, account, freeID string) {
	if strings.TrimSpace(account) == "" || strings.TrimSpace(freeID) == "" {
		return
	}
	key := liveCapKey(home, account, freeID)
	liveCapHits.mu.Lock()
	defer liveCapHits.mu.Unlock()
	if liveCapHits.keys == nil {
		liveCapHits.keys = map[string]liveCapHit{}
	}
	entry := liveCapHits.keys[key]
	entry.spent = true
	entry.optedIn = true
	liveCapHits.keys[key] = entry
}

// isLiveCapHit reports whether one free tier is spent as far as this process
// knows.
func isLiveCapHit(home, account, freeID string) bool {
	if strings.TrimSpace(account) == "" || strings.TrimSpace(freeID) == "" {
		return false
	}
	liveCapHits.mu.RLock()
	defer liveCapHits.mu.RUnlock()
	return liveCapHits.keys[liveCapKey(home, account, freeID)].spent
}

// livePaidOptedIn reports whether this process holds the reader's paid-tier
// choice for a reset-less hit.
func livePaidOptedIn(home, account, freeID string) bool {
	if strings.TrimSpace(account) == "" || strings.TrimSpace(freeID) == "" {
		return false
	}
	liveCapHits.mu.RLock()
	defer liveCapHits.mu.RUnlock()
	return liveCapHits.keys[liveCapKey(home, account, freeID)].optedIn
}

// forgetLiveCapHits clears this process's half of the record: a test's cleanup,
// and a refresh of the account (a new login is a new allowance).
func forgetLiveCapHits() {
	liveCapHits.mu.Lock()
	liveCapHits.keys = nil
	liveCapHits.mu.Unlock()
}

// capResetStamp is the client's own regex for the reset the product writes into
// its sentence, with the zone it may state beside it.
var capResetStamp = regexp.MustCompile(`(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})(?:\s*(?:UTC\+8|\(UTC\+8\)))?`)

// capResetZone is the zone the product writes its reset stamps in: the client
// reads a zone-less stamp as UTC+8 (parseResetAt).
var capResetZone = time.FixedZone("UTC+8", 8*60*60)

// parseCapResetAt reads the reset a cap refusal states in its sentence.
// findStamp also answers the matched text, so a caller that rewrites the
// sentence can spell the same instant unambiguously.
func parseCapResetAt(text string) (time.Time, string) {
	match := capResetStamp.FindStringSubmatch(text)
	if len(match) < 3 {
		return time.Time{}, ""
	}
	when, err := time.ParseInLocation("2006-01-02 15:04:05", match[1]+" "+match[2], capResetZone)
	if err != nil {
		return time.Time{}, ""
	}
	return when, match[0]
}

// paidSwitchActive reports whether the paid twin is what this account's requests
// should be sent as: the persistent choice while its window lasts, or this
// process's choice for a hit that named no reset (the client's
// resolveEffectiveCapHit).
func paidSwitchActive(home string, creds Credentials, freeID string) bool {
	account := capHitAccountKey(creds)
	return paidOptedIn(home, account, freeID) || livePaidOptedIn(home, account, freeID)
}

// recordCapHit is what a 6004 on a free tier does to the record: a stated reset
// is filed on disk, an unstated one only in this process.
func recordCapHit(home string, creds Credentials, freeID string, resetAt time.Time) {
	account := capHitAccountKey(creds)
	if resetAt.IsZero() {
		setLiveCapHit(home, account, freeID, true)
		return
	}
	if err := markCapHit(home, account, freeID, resetAt); err != nil {
		slog.Warn("workbuddy: could not record the spent allowance", "err", err)
	}
}

// optInPaidTier is the switch's own record: it is what UsePaidTier writes.
func optInPaidTier(home string, creds Credentials, freeID string, resetAt time.Time) error {
	account := capHitAccountKey(creds)
	if resetAt.IsZero() {
		markLivePaidOptIn(home, account, freeID)
		return nil
	}
	if err := markCapHit(home, account, freeID, resetAt); err != nil {
		return err
	}
	return markPaidOptIn(home, account, freeID)
}
