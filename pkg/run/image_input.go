package run

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// ImageInputRefusedCode is the stable wire code of a turn refused because the
// session model cannot take an image the request would carry. A surface that
// renders the refusal in the viewer's language keys off it.
const ImageInputRefusedCode = "image_input_unsupported"

// ImageRejectedCode is the wire code of a turn refused because this
// conversation's model already refused an image on it — the provider's own
// rejection, remembered, rather than a catalog lookup. The two say different
// things to the reader and the same thing about the way out (/model).
const ImageRejectedCode = "image_rejected_by_provider"

// ImageInputRefusedError is a turn refusal: the session model (Provider/Model)
// does not accept image input — because the catalog says so, or because it
// already refused one in this conversation — and the request would carry an
// image. The message it refused is kept; only the request is withheld.
//
// Observed marks the second case: nothing was looked up, a provider rejection on
// this same model was remembered, and Reason is the sentence it gave. Both cases
// end in the same recovery, which is why this is one type.
type ImageInputRefusedError struct {
	Provider string
	Model    string
	Reason   string
	Observed bool
}

func (e *ImageInputRefusedError) Error() string {
	name := e.Model
	if e.Provider != "" {
		name = e.Provider + "/" + e.Model
	}
	sentence := fmt.Sprintf("the session model %s does not accept image input and this conversation contains images; the message is kept, switch model with /model and continue", name)
	if e.Observed {
		sentence = fmt.Sprintf("the session model %s already refused an image in this conversation and this request would carry one; the message is kept, switch model with /model and continue", name)
	}
	if reason := strings.TrimSpace(e.Reason); reason != "" {
		sentence += fmt.Sprintf(" (provider said: %s)", reason)
	}
	return sentence
}

// Code is the stable wire code of this refusal: the catalog answer and the
// remembered rejection are different facts, so a surface can say either in the
// viewer's own language.
func (e *ImageInputRefusedError) Code() string {
	if e == nil || !e.Observed {
		return ImageInputRefusedCode
	}
	return ImageRejectedCode
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
//
// The second half of the judgement is what this conversation already learned the
// hard way: a provider that refused an image here is remembered for the model
// that refused it (see withImageRefusalRecorder), so the same request is not
// sent again to be refused again. That record is coarse — "this conversation,
// this provider+model, cannot take pictures" — because the refusal never says
// which image it meant.
func ImageTurnRefusal(ctx context.Context, r *Runner, sessionID string, attachesImage bool) error {
	sid := strings.TrimSpace(sessionID)
	if sid != "" && strings.TrimSpace(llm.AgentSessionIDFromContext(ctx)) == "" {
		ctx = llm.WithAgentSessionID(ctx, sid)
	}
	provider, model, refused := imageInputRefusedModel(ctx, r)
	if !refused {
		// The catalog has nothing against this model; the conversation's own
		// record may still. Same precondition either way: a text-only turn is
		// never withheld, because no refusal was ever paid for it.
		provider, model = r.effectiveModelFor(ctx)
		provider, model = strings.TrimSpace(provider), strings.TrimSpace(model)
		if reason, recorded, err := recordedImageRefusal(ctx, r, sid, provider, model); err == nil && recorded {
			if attachesImage || sessionHistoryCarriesImage(ctx, r, sid) {
				return &ImageInputRefusedError{Provider: provider, Model: model, Reason: reason, Observed: true}
			}
		}
		return nil
	}
	if !attachesImage && !sessionHistoryCarriesImage(ctx, r, sid) {
		return nil
	}
	return &ImageInputRefusedError{Provider: provider, Model: model}
}

// recordedImageRefusal reads what this conversation recorded about the exact
// provider+model in force. A read error is not a refusal: the record is an
// optimization, and a missing one costs one request, never a turn.
func recordedImageRefusal(ctx context.Context, r *Runner, sessionID, provider, model string) (string, bool, error) {
	if r == nil || r.Deps == nil || r.SessionStore == nil || sessionID == "" || provider == "" || model == "" {
		return "", false, nil
	}
	return r.SessionStore.ImageRefusal(ctx, sessionID, provider, model)
}

// withImageRefusalRecorder installs the one observer the run layer needs: a
// refusal the heal ladder met is (a) remembered for this conversation and the
// model that produced it, and (b) published once as this run's image_rejected
// event, so a surface can say what happened instead of leaving the reader to
// wonder why the picture was ignored. Without it a conversation carrying one
// unsendable image pays for that refusal on every turn and nobody is told.
//
// Both halves are best-effort: a lost row costs one request and a lost event
// costs one line — never a turn. The observer runs before the degraded retry, so
// the note the surfaces draw arrives before the answer it explains.
func withImageRefusalRecorder(ctx context.Context, r *Runner, sessionID string) context.Context {
	if ctx == nil || r == nil || sessionID == "" {
		return ctx
	}
	// Resolved once, here, where the turn's model selection is already in the
	// context: the same call the gate makes, so the row and the lookup that
	// reads it back cannot disagree about which model it is about.
	provider, model := r.effectiveModelFor(ctx)
	provider, model = strings.TrimSpace(provider), strings.TrimSpace(model)
	runID := strings.TrimSpace(tool.RunIDFromContext(ctx))
	return llm.WithImageRefusalObserver(ctx, func(reason string) {
		// The write outlives a cancelled turn on purpose: the refusal happened,
		// and the record is what stops the next turn paying for it again.
		storeCtx := context.WithoutCancel(ctx)
		if err := r.SessionStore.RecordImageRefusal(storeCtx, sessionID, provider, model, reason); err != nil {
			// Debug, not Warn: this repeats every turn a refused image stays in
			// the conversation, and nothing the reader could do would fix it.
			slog.Debug("remember a provider image refusal", "session", sessionID, "provider", provider, "model", model, "err", err)
		}
		if runID == "" {
			// No run, no event: an injected id would be a second line every
			// turn. The row above is what makes the next turn quiet anyway.
			return
		}
		// The fixed id is the whole of the idempotence: one run refuses once as
		// far as the surfaces are concerned, however many tool-loop requests it
		// takes. The store's (session_id, event_id) conflict drops the repeat,
		// and both surfaces dedupe on the id before drawing it.
		_ = r.publishSurfaceEvent(ctx, event.NewRunEvent(
			"image-rejected:"+runID, runID, sessionID, event.RunEventImageRejected,
			event.ImageRejectedPayload{
				Code:     ImageRejectedCode,
				Provider: provider,
				Model:    model,
				Reason:   reason,
			},
			time.Now(),
		))
	})
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
