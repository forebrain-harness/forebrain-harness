package run

import (
	"context"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
)

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
		Every(llm.NativeMediaForProvider(provider))
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
