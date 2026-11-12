package run

import (
	"context"
	"log/slog"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
)

// mediaKindImage is the kind of a picture in the refusals this layer records.
// Every other kind is llm.MediaKind, whose values are what the store holds:
// images are not one of them (they have their own content type and their own
// gate), so the one string the run layer needs for them lives here.
const mediaKindImage = "image"

// NativeMediaKinds answers what this conversation's model accepts directly
// right now: a PDF, an audio clip or a video, or none of them.
//
// It is the answer the assembly asks before it builds a file part instead of
// the text an attachment has always degraded to, and the replay asks the same
// question so a message and every later request of it carry the same parts.
// Three layers narrow it, in the order the owner described the feature: the
// model's own capability — the catalog's modalities, with the configured row's
// switches laid over them, which is the switch in the model's advanced settings
// — and then what the protocol the requests are sent on has a shape for, and
// what that provider's own endpoint documents. A model the catalog does not know
// takes nothing this way (the row's switches are how a user says otherwise),
// because sending a file to an endpoint nobody documented is exactly what the
// degrade-to-text path exists to avoid.
//
// What the conversation already had refused on this exact model comes off last
// (owner 2026-11-12): a provider that turned down a PDF here is not a provider
// that cannot take pictures, so the record is per kind and only that kind is
// withheld. The read is one lookup per ask, never one per part.
//
// sessionID names the conversation when ctx does not already carry it, the way
// ImageTurnRefusal resolves it: the web and terminal surfaces call this before a
// turn's context exists.
//
// It is a function over the Runner rather than a method on it, for the reason
// ImageTurnRefusal is: a surface's question about what a conversation may carry
// is not part of the Runner's own surface, and that surface has a budget.
func NativeMediaKinds(ctx context.Context, r *Runner, sessionID string) llm.NativeMediaKinds {
	if r == nil {
		return llm.NativeMediaKinds{}
	}
	sid := strings.TrimSpace(sessionID)
	if sid != "" && strings.TrimSpace(llm.AgentSessionIDFromContext(ctx)) == "" {
		ctx = llm.WithAgentSessionID(ctx, sid)
	}
	hit, ok := r.modelHitFor(ctx)
	if !ok {
		return llm.NativeMediaKinds{}
	}
	provider, model := r.agentModelFor(ctx)
	accepted := llm.NativeMediaKinds{
		PDF:   hit.SupportsPDF,
		Audio: hit.SupportsAudio,
		Video: hit.SupportsVideo,
	}
	if !accepted.Any() {
		return llm.NativeMediaKinds{}
	}
	return accepted.
		Every(llm.NativeMediaForProtocol(protocolForMedia(provider, model))).
		Every(llm.NativeMediaForProvider(provider)).
		Without(refusedMediaKinds(ctx, r, sid, provider, model))
}

// refusedMediaKinds is the set this conversation's model already turned down, in
// one lookup keyed by the primary key: the table holds at most one row per kind.
// A read error is not a refusal — it is reported and read as "nothing was
// refused", because a record that cannot be read must never cost a turn the
// media it could have carried.
func refusedMediaKinds(ctx context.Context, r *Runner, sessionID, provider, model string) llm.NativeMediaKinds {
	if r == nil || r.Deps == nil || r.SessionStore == nil || sessionID == "" || provider == "" || model == "" {
		return llm.NativeMediaKinds{}
	}
	recorded, err := r.SessionStore.MediaRefusals(ctx, sessionID, provider, model)
	if err != nil {
		slog.WarnContext(ctx, "read this conversation's media refusals; sending every kind the model takes",
			"session", sessionID, "provider", provider, "model", model, "err", err)
		return llm.NativeMediaKinds{}
	}
	var refused llm.NativeMediaKinds
	for media := range recorded {
		// Only the kinds the assembly can send natively are subtracted; the
		// picture row is ImageTurnRefusal's business.
		switch llm.MediaKind(media) {
		case llm.MediaKindPDF:
			refused.PDF = true
		case llm.MediaKindAudio:
			refused.Audio = true
		case llm.MediaKindVideo:
			refused.Video = true
		}
	}
	return refused
}

// withMediaRefusalRecorder installs the other half of what a refused media part
// costs: the kind is remembered for this conversation and the model that
// refused it, so the next request is assembled without it instead of sending it
// to be refused again. It is the image recorder's shape (see
// withImageRefusalRecorder) applied to a kind the request names.
//
// The turn itself still ends with the provider's error: unlike an image, a
// refused PDF, clip or video is not re-sent degraded this turn — that is a
// decision of its own — so the record is all this half does. A lost write costs
// one request and never a turn.
func withMediaRefusalRecorder(ctx context.Context, r *Runner, sessionID string) context.Context {
	if ctx == nil || r == nil || r.SessionStore == nil || sessionID == "" {
		return ctx
	}
	// Resolved once, here, for the reason the image recorder resolves it once:
	// the row and the lookup that reads it back must agree about which model
	// refused what, and a subagent's requests are its own model's.
	provider, model := r.agentModelFor(ctx)
	provider, model = strings.TrimSpace(provider), strings.TrimSpace(model)
	return llm.WithMediaRefusalObserver(ctx, func(ctx context.Context, kind llm.MediaKind, reason string) {
		if provider == "" || model == "" {
			return
		}
		// The write outlives a cancelled turn on purpose: the refusal happened,
		// and the record is what stops the next turn paying for it again.
		storeCtx := context.WithoutCancel(ctx)
		if err := r.SessionStore.RecordMediaRefusal(storeCtx, sessionID, provider, model, string(kind), reason); err != nil {
			slog.DebugContext(ctx, "remember a provider media refusal",
				"session", sessionID, "provider", provider, "model", model, "media", kind, "err", err)
		}
	})
}

// protocolForMedia names the protocol this provider's requests are sent on. The
// routing itself lives in NewLLMFromYAML; this is the same decision, asked by a
// reader that has no client. OpenCode picks per model, so its models ask the
// gateway's own table.
func protocolForMedia(provider, model string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "opencode", "opencode-go":
		protocol, err := opencode.ProtocolFor(provider, model)
		if err != nil {
			return ""
		}
		switch protocol {
		case opencode.ProtocolResponses:
			return llm.ProtocolResponses
		case opencode.ProtocolAnthropic:
			return llm.ProtocolMessages
		case opencode.ProtocolGoogle:
			return llm.ProtocolGemini
		}
		return llm.ProtocolChatCompletions
	}
	return llm.ProtocolForProvider(provider)
}
