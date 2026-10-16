package llm

import (
	"strings"
	"sync"
)

// The catalog (models.json) is a static, frequently-stale description of a
// model's context window. The provider itself reports the only authoritative
// numbers Forebrain Harness ever sees:
//
//   - a successful response carries the real prompt size it accepted
//     (input_tokens + cached input tokens), which proves a lower bound on the
//     window: that many tokens fit.
//   - a context-overflow rejection proves an upper bound: whatever was sent
//     did not fit.
//
// Overflow errors in practice do not carry the limit as a number, so the limit
// is learned from these two bounds instead of parsed out of the message text.
// Observations also correct the local token estimate, which structurally
// undercounts: the occupancy check runs in the outermost LLM wrapper, above the
// wrappers that prepend the skills catalogue, the memory instruction and the
// mode reminders, and above the provider client's own request framing. None of
// that is visible to the estimate, yet all of it is billed as input.
//
// The correction is an ADDITIVE overhead, never a scale factor. What the
// estimate misses is a block of prefix that is frozen for the life of a session
// (it has to be, or the prompt cache would miss on every turn), so the shortfall
// is roughly a constant number of tokens, not a constant fraction. Modelling it
// as a ratio reads that constant off the smallest request of the session -- the
// first one, where a ~14k prefix sits behind an ~8k estimate -- and then
// multiplies it into every later request: a 300k conversation projects as 800k+
// and the 90% proactive threshold fires at barely a third of the real window,
// over and over, each time starting a fresh prompt-cache generation. An
// overhead in tokens cannot do that: it is re-measured on every response and
// stays the size of the thing it stands for.
//
// The overhead is kept per estimator, while the window bounds are not. The
// bounds are facts about the model's real window, whichever tokenizer sized the
// request. The overhead is "provider count minus local estimate", and the
// estimate depends on the estimator: one process can count a model first with
// the default tokenizer or rune/4 (its own tokenizer not yet downloaded) and
// later with its own, or switch on a config reload. A gap learned under one
// estimator is not the same quantity under another, and since the overhead only
// ever grows, mixing them would add a stale gap to every later estimate and
// compact early.

// Observation is the learned state for one provider/model pair.
type Observation struct {
	// ProvenInput is the largest prompt size the provider has accepted.
	ProvenInput int
	// RejectedInput is the smallest prompt size the provider has rejected as
	// too large. Zero when no overflow has been observed.
	RejectedInput int
	// Overhead is the largest number of tokens by which the provider's own
	// prompt count has exceeded the local estimate of the same request. It is
	// added to an estimate to project what the provider will bill. Never
	// negative: a provider count below the estimate leaves it untouched, because
	// shrinking the projection would move the compaction threshold further away
	// and re-introduce the overflow this store exists to prevent.
	//
	// It is the overhead learned under the unnamed estimator (RecordSuccess);
	// the overhead under a named one is read with ProjectFor.
	Overhead int
}

var (
	observedMu sync.RWMutex
	observed   = map[string]*Observation{}
	// overheads holds the learned overhead per observation key and estimator
	// name (see overheadKey); guarded by observedMu.
	overheads = map[string]int{}
)

// ResetObservedForTest clears all recorded observations. Tests that record
// observations must call this at the start so they don't pollute each other.
func ResetObservedForTest() {
	observedMu.Lock()
	observed = map[string]*Observation{}
	overheads = map[string]int{}
	observedMu.Unlock()
}

// overheadKey identifies the overhead learned for one provider/model pair (its
// observationKey) under one estimator.
func overheadKey(key, estimator string) string {
	return key + "\x00" + estimator
}

// observationKey identifies the provider/model pair an observation belongs to.
// ok is false when neither is named.
//
// An observation is a fact about one model: how its provider counts tokens, and
// how large a prompt it has accepted or refused. A caller that names no model
// has stated no such fact, and filing it under a shared blank key attributes it
// to every other caller that named none either — so one runtime's refusal caps
// an unrelated runtime's window, and one provider's token accounting rescales
// another's estimates. There is no bucket for "some model"; callers that cannot
// name one simply learn nothing, which is what they actually know.
func observationKey(provider, model string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(provider)) + "/" + strings.ToLower(strings.TrimSpace(model))
	if key == "/" {
		return "", false
	}
	return key, true
}

// RecordSuccess records that the provider accepted a prompt of realInput
// tokens, which an unnamed local estimator had sized at estimated tokens. It is
// for callers that have no estimator name; production callers use the For
// variants (RecordSuccessFor, ProjectFor).
func RecordSuccess(provider, model string, realInput, estimated int) {
	RecordSuccessFor(provider, model, "", realInput, estimated)
}

// RecordSuccessFor records that the provider accepted a prompt of realInput
// tokens, which the local estimator named estimator (Estimator.Name) had sized
// at estimated tokens. The accepted size is a fact about the model; the
// overhead is learned for that estimator only.
func RecordSuccessFor(provider, model, estimator string, realInput, estimated int) {
	if realInput <= 0 {
		return
	}
	key, ok := observationKey(provider, model)
	if !ok {
		return
	}
	observedMu.Lock()
	defer observedMu.Unlock()
	obs := observed[key]
	if obs == nil {
		obs = &Observation{}
		observed[key] = obs
	}
	if realInput > obs.ProvenInput {
		obs.ProvenInput = realInput
	}
	// A prompt that was accepted cannot also be over the limit. This matters
	// when the model is switched to a larger window mid-session, or when an
	// earlier rejection was caused by output reservation rather than input size.
	if obs.RejectedInput > 0 && realInput >= obs.RejectedInput {
		obs.RejectedInput = 0
	}
	if estimated > 0 {
		ok := overheadKey(key, estimator)
		if gap := realInput - estimated; gap > overheads[ok] {
			overheads[ok] = gap
		}
	}
}

// RecordOverflow records that the provider rejected a prompt the local
// estimator had sized at estimated tokens. Pass realInput when the failed
// response still reported usage; pass 0 when it did not.
func RecordOverflow(provider, model string, realInput, estimated int) {
	size := realInput
	if size <= 0 {
		size = estimated
	}
	if size <= 0 {
		return
	}
	key, ok := observationKey(provider, model)
	if !ok {
		return
	}
	observedMu.Lock()
	defer observedMu.Unlock()
	obs := observed[key]
	if obs == nil {
		obs = &Observation{}
		observed[key] = obs
	}
	if obs.RejectedInput == 0 || size < obs.RejectedInput {
		obs.RejectedInput = size
	}
	// The rejection is ground truth about the ceiling; a stale larger "proven"
	// value from a different model or a pre-switch window must not outrank it.
	if obs.ProvenInput >= obs.RejectedInput {
		obs.ProvenInput = 0
	}
}

// Get returns the learned observation for a provider/model pair.
func Get(provider, model string) (Observation, bool) {
	key, named := observationKey(provider, model)
	if !named {
		return Observation{}, false
	}
	observedMu.RLock()
	defer observedMu.RUnlock()
	obs, ok := observed[key]
	if !ok || obs == nil {
		return Observation{}, false
	}
	result := *obs
	result.Overhead = overheads[overheadKey(key, "")]
	return result, true
}

// Project converts an unnamed estimator's local estimate into what the
// provider will actually count for that request, by adding the overhead learned
// under that estimator. It is for callers that have no estimator name;
// production callers use ProjectFor.
func Project(provider, model string, estimated int) int {
	return ProjectFor(provider, model, "", estimated)
}

// ProjectFor converts a local estimate made by the estimator named estimator
// into what the provider will actually count for that request, by adding the
// overhead learned under that same estimator. Returns the estimate unchanged
// when nothing is known for it yet.
func ProjectFor(provider, model, estimator string, estimated int) int {
	if estimated <= 0 {
		return 0
	}
	key, named := observationKey(provider, model)
	if !named {
		return estimated
	}
	observedMu.RLock()
	overhead := overheads[overheadKey(key, estimator)]
	observedMu.RUnlock()
	if overhead <= 0 {
		return estimated
	}
	return estimated + overhead
}

// EffectiveWindow reconciles the catalog window with what the provider has
// actually demonstrated. catalogWindow may be 0 when the model is unknown.
func EffectiveWindow(provider, model string, catalogWindow int) int {
	obs, ok := Get(provider, model)
	if !ok {
		return catalogWindow
	}
	window := catalogWindow
	// A rejection caps the window regardless of what the catalog claims.
	if obs.RejectedInput > 0 && (window <= 0 || obs.RejectedInput <= window) {
		// The rejected size is the first size known not to fit, so the usable
		// window is strictly below it.
		window = obs.RejectedInput - 1
	}
	// A larger accepted prompt proves the catalog window is too small.
	if obs.ProvenInput > window {
		window = obs.ProvenInput
	}
	return window
}
