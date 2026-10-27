package llm

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// unsendableNoteReasonLimit bounds the provider reason quoted in a note: the
// sentence rides in the prompt on every later request of the session, so a
// multi-line provider error must not turn into a paragraph.
const unsendableNoteReasonLimit = 200

// UnsendableImageNote is the model-visible line a user attachment degrades to
// when its bytes cannot be sent. Every path that writes it — the terminal's
// live turn, the web message text, and the replay of a stored row — produces
// this exact string: a message a turn sends is the message every later request
// replays, so a note that differed per path would fork the cached prefix at the
// turn it was written (see run.UserInputParts). reason is the ladder's own
// sentence, flattened to one line and trimmed to 200 characters.
//
// An empty mimeType omits the parenthesised type; an empty label falls back to
// "image", because the note's job is to say a picture is missing, and an empty
// line would say nothing at all.
func UnsendableImageNote(label, mimeType, reason string) string {
	text := strings.TrimSpace(label)
	if text == "" {
		text = "image"
	}
	if mime := strings.TrimSpace(mimeType); mime != "" {
		text += " (" + mime + ")"
	}
	return "[attachment] " + text + " — not sent: " + oneLineReason(reason)
}

// DegradeImages returns a copy of messages with every image part replaced by a
// deterministic one-line text part naming what was dropped and why. It never
// mutates the input: the caller retries with the copy and the transcript keeps
// its images.
//
// This is the last resort — an image the provider refused for a rule the
// ladder cannot see (a per-provider limit we do not model, a format quirk).
// The retried request must still be a request the model can act on, so the
// image becomes a sentence instead of disappearing.
func DegradeImages(messages []Message, reason string) (out []Message, replaced int) {
	if len(messages) == 0 {
		return messages, 0
	}
	out = make([]Message, len(messages))
	copy(out, messages)
	for i := range out {
		if len(out[i].Parts) == 0 {
			continue
		}
		var parts []ContentPart
		for j, part := range out[i].Parts {
			if part.Type != ContentTypeImageURL && part.Type != ContentTypeImageBase64 {
				continue
			}
			if parts == nil {
				// Copy on first image, so a message that carries none keeps
				// sharing the caller's slice untouched.
				parts = make([]ContentPart, len(out[i].Parts))
				copy(parts, out[i].Parts)
			}
			parts[j] = Text(degradedImageNote(part, reason))
			replaced++
		}
		if parts != nil {
			out[i].Parts = parts
		}
	}
	return out, replaced
}

// degradedImageNote is what one dropped image becomes: its type and rough size
// (so the model can tell a screenshot from a photograph), and the provider's
// own reason. Same input, same string — a retry must not perturb the request
// beyond removing the image.
func degradedImageNote(part ContentPart, reason string) string {
	mime := strings.TrimSpace(part.MIMEType)
	if mime == "" {
		mime = "unknown type"
	}
	note := "[image omitted: " + mime
	if kb := encodedImageKB(part); kb > 0 {
		note += fmt.Sprintf(", ~%d KB", kb)
	}
	return note + " — the provider rejected it as an image: " + oneLineReason(reason) + "]"
}

// encodedImageKB estimates the byte size a part's image was sent as, from the
// base64 that carried it: 4 characters per 3 bytes. A part with no inline
// bytes (an external URL) reports 0 and the note omits the size.
func encodedImageKB(part ContentPart) int {
	encoded := part.ImageBase64
	if encoded == "" {
		if _, tail, ok := strings.Cut(part.ImageURL, ";base64,"); ok {
			encoded = tail
		}
	}
	if encoded == "" {
		return 0
	}
	kb := len(encoded) * 3 / 4 / 1024
	if kb < 1 {
		kb = 1
	}
	return kb
}

// NewImageHeal returns a middleware that retries once with the images degraded
// when the provider rejects an image. It sits below the tool-orchestration
// wrapper, so the rewritten request never becomes Result.Session (the
// transcript keeps its images).
func NewImageHeal() LLMMiddleware {
	return func(next LLM) LLM {
		return &imageHealLLM{inner: next}
	}
}

type imageHealLLM struct {
	inner LLM
}

func (h *imageHealLLM) Execute(ctx context.Context, messages []Message, tools []*Tool) (*Result, error) {
	result, err := h.inner.Execute(ctx, messages, tools)
	if err == nil || !IsImageRejection(err) {
		return result, err
	}
	if IsPartialResponse(err) || !ResendSafe(ctx) {
		// The failed request had already produced something a resend would
		// duplicate, or an inner layer already decided how it ends: the
		// caller finishes it from what completed.
		return result, err
	}
	degraded, replaced := DegradeImages(messages, err.Error())
	if replaced == 0 {
		// The rejection named an image but this request carries none we can
		// see (already textual, or in a shape this wrapper does not know):
		// retrying would ask the provider the same question twice.
		return result, err
	}
	// Before the retry, tell whoever asked: this package cannot name the
	// provider or model, but its caller can, and the refusal is a fact that
	// costs a wasted request every turn it goes unremembered.
	reportImageRefusal(ctx, oneLineReason(err.Error()))
	slog.Warn("provider rejected an image; retrying with the image degraded",
		"replaced", replaced, "err", err)
	ResetStreamProgress(ctx)
	return h.inner.Execute(ctx, degraded, tools)
}

// imageRefusalObserverKey is the context key the observer rides on. A private
// type keeps the key from colliding with any other package's.
type imageRefusalObserverKey struct{}

// WithImageRefusalObserver runs fn once per provider image refusal this call
// meets, before the degraded retry. It is how a caller that does know the
// provider and model learns a fact this package cannot name itself: the heal
// answers "what do I do about the refusal", the observer answers "who should be
// told". fn receives the request's own context, so a caller with per-call
// state on it (the attempt ledger) can record the refusal against the call.
//
// The observer is a report, not a decision: it cannot change the request or the
// result, and it runs on the request's own goroutine before the retry. It must
// return quickly — a caller that has to write something does so on a context of
// its own, without blocking the retry.
func WithImageRefusalObserver(ctx context.Context, fn func(ctx context.Context, reason string)) context.Context {
	if ctx == nil || fn == nil {
		return ctx
	}
	return context.WithValue(ctx, imageRefusalObserverKey{}, fn)
}

// ImageRefusalObserverFromContext returns the observer installed by
// WithImageRefusalObserver, or nil when nobody asked to be told.
func ImageRefusalObserverFromContext(ctx context.Context) func(ctx context.Context, reason string) {
	if ctx == nil {
		return nil
	}
	fn, _ := ctx.Value(imageRefusalObserverKey{}).(func(ctx context.Context, reason string))
	return fn
}

// reportImageRefusal hands one refusal to the installed observer, with the
// request's own context. A nil observer is the ordinary case — nothing
// installed, nothing to do — and the call is the only thing between the
// refusal and the retry.
func reportImageRefusal(ctx context.Context, reason string) {
	if fn := ImageRefusalObserverFromContext(ctx); fn != nil {
		fn(ctx, reason)
	}
}

// oneLineReason makes a provider or ladder reason safe to embed in a prompt:
// every run of whitespace becomes one space, and an over-long sentence is cut
// with an ellipsis rather than truncated silently.
func oneLineReason(reason string) string {
	flat := strings.Join(strings.Fields(reason), " ")
	if flat == "" {
		return "the image could not be sent"
	}
	if len(flat) > unsendableNoteReasonLimit {
		cut := unsendableNoteReasonLimit
		for cut > 0 && !utf8Start(flat[cut]) {
			cut--
		}
		flat = strings.TrimSpace(flat[:cut]) + "…"
	}
	return flat
}

// utf8Start reports whether b starts a UTF-8 sequence, so a cut never lands
// inside a multi-byte rune.
func utf8Start(b byte) bool {
	return b&0xC0 != 0x80
}
