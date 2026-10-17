package run

import (
	"context"
	"fmt"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// ImageInputRefusedCode is the stable wire code of a turn refused because the
// session model cannot take an image the request would carry. A surface that
// renders the refusal in the viewer's language keys off it.
const ImageInputRefusedCode = "image_input_unsupported"

// ImageInputRefusedError is a turn refusal: the session model (Provider/Model)
// does not accept image input and the request would carry an image. The
// message it refused is kept; only the request is withheld.
type ImageInputRefusedError struct {
	Provider string
	Model    string
}

func (e *ImageInputRefusedError) Error() string {
	name := e.Model
	if e.Provider != "" {
		name = e.Provider + "/" + e.Model
	}
	return fmt.Sprintf("the session model %s does not accept image input and this conversation contains images; the message is kept, switch model with /model and continue", name)
}

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
	provider, model, refused := imageInputRefusedModel(ctx, r)
	if !refused {
		return ""
	}
	name := model
	if provider != "" {
		name = provider + "/" + model
	}
	return fmt.Sprintf("the session model %s does not accept image input; switch model with /model, then read the image again", name)
}

// ImageTurnRefusal is the image-input gate for a whole user turn. It returns
// *ImageInputRefusedError when this session's model does not accept image
// input and the request would carry an image — one the message attaches, or
// one already in the conversation, whether the user attached it or a tool read
// it — and nil when the turn may proceed.
//
// The image is never dropped to let a text-only model answer, and the
// refusal is not a lost message: the caller still stores the message and only
// withholds the request, so after /model the conversation continues with every
// image in it. The judgement is the same fail-open catalog check as
// ImageInputRefusal; a stored image whose file can no longer be read is not
// counted, since no request would carry it.
func ImageTurnRefusal(ctx context.Context, r *Runner, sessionID string, attachesImage bool) error {
	sid := strings.TrimSpace(sessionID)
	if sid != "" && strings.TrimSpace(llm.AgentSessionIDFromContext(ctx)) == "" {
		ctx = llm.WithAgentSessionID(ctx, sid)
	}
	provider, model, refused := imageInputRefusedModel(ctx, r)
	if !refused {
		return nil
	}
	if !attachesImage && !sessionHistoryCarriesImage(ctx, r, sid) {
		return nil
	}
	return &ImageInputRefusedError{Provider: provider, Model: model}
}

// sessionHistoryCarriesImage reports whether the next request of the session
// would replay an image: its history is rebuilt exactly as the request builds
// it (transcriptSession.history) and searched for image parts.
func sessionHistoryCarriesImage(ctx context.Context, r *Runner, sessionID string) bool {
	sid := strings.TrimSpace(sessionID)
	if r == nil || r.Deps == nil || r.SessionStore == nil || sid == "" {
		return false
	}
	entries, err := r.SessionStore.ListTranscriptMessagesWithRefs(ctx, sid, 5000)
	if err != nil {
		return false
	}
	for _, msg := range rehydrateImageReferences(ctx, r.FileResolver, entries) {
		for _, part := range msg.Parts {
			if part.Type == llm.ContentTypeImageBase64 || part.Type == llm.ContentTypeImageURL {
				return true
			}
		}
	}
	return false
}

// imageInputRefusedModel names this session's model when the catalog says it
// does not accept image input, and reports refused=false when it does, when
// the catalog does not know it, or when there is no model to judge.
func imageInputRefusedModel(ctx context.Context, r *Runner) (provider, model string, refused bool) {
	if r == nil {
		return "", "", false
	}
	// The turn carries the conversation's own model choice; outside a turn the
	// session's stored row decides (a web session may run on a model other
	// than the runner's default), then the runner's current selection.
	provider, model = r.effectiveModelFor(ctx)
	provider, model = strings.TrimSpace(provider), strings.TrimSpace(model)
	if model == "" {
		return "", "", false
	}
	hit, ok := llm.Lookup(provider, model)
	if !ok || hit.SupportsAttachments {
		// Unknown model: fail open.
		return "", "", false
	}
	return provider, model, true
}
