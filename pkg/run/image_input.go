package run

import (
	"context"
	"fmt"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// ImageInputRefusal returns the reason this session's model may not be handed
// an image, or "" when it may.
//
// The gate exists so a genuinely text-only model answers with an actionable
// sentence instead of the provider rejecting the request with a 400 after the
// pixels are already on the wire. It is deliberately fail-open: a model the
// catalog does not know (a custom base_url with a custom model name) is
// allowed, because a stale or absent catalog entry cannot be told from a
// correct "no", and refusing an endpoint that actually accepts images is worse
// than sending one request it rejects.
//
// The catalog is a static table that can lag a provider, so when the reason to
// refuse is wrong the fix is the data (assets/models.json), never this
// predicate; the refusal itself is written to be one step recoverable (it
// names the model and points at /model).
func (r *Runner) ImageInputRefusal(ctx context.Context) string {
	if r == nil {
		return ""
	}
	// The turn carries the conversation's own model choice when it has a
	// stored row; outside a turn, or for a session with no row yet, fall back
	// to the runner's current selection.
	sel, ok := SessionModelSelectionFromContext(ctx)
	if !ok {
		sel = PrimaryModelSelection(r)
	}
	provider := strings.TrimSpace(sel.Provider)
	model := strings.TrimSpace(sel.Model)
	if model == "" {
		return ""
	}
	hit, ok := llm.Lookup(provider, model)
	if !ok {
		// Unknown model: fail open.
		return ""
	}
	if hit.SupportsAttachments {
		return ""
	}
	name := model
	if provider != "" {
		name = provider + "/" + model
	}
	return fmt.Sprintf("the session model %s does not accept image input; switch model with /model, then read the image again", name)
}
