package opencode

import "strings"

// Static model metadata for the two OpenCode surfaces.
//
// The ids come from the official model table; the live GET /v1/models endpoint
// reports the same ids but carries no protocol or context window at all, which
// is why the protocol is transcribed here instead of discovered.
//
// What this table does NOT carry any more is the reasoning levels: they come
// from models.json (the shipped catalog plus the user's overlay), which has a
// row for every id below and is refreshed with scripts/update-models-asset.sh.
// The window, image capability and levels are only fallbacks here for an id
// models.json has never heard of — a model the live catalog reports after this
// table was written. Two facts stay in code because they are code, not data:
// the protocol a surface serves a model on (it picks the client) and whether
// the Console free family works without a credential.
//
// The tables are deliberately not exhaustive. Console lists whole families
// ("all gpt-*", "all claude-*", "all gemini-*") whose members are not named in
// the table, so those ids get their protocol from ProtocolFor and their
// metadata from DefaultModelMeta. Go's model set is small enough that the
// official table names every member, and GoModels lists exactly those.

const (
	// defaultContextWindow is the window assumed for an id models.json has no
	// row for: conservative, so a picker never promises more room than the
	// model has.
	defaultContextWindow int64 = 128_000
	// grokContextWindow is the Grok family's documented window.
	grokContextWindow int64 = 200_000
	// gptContextWindow is the gpt-* family's documented window.
	gptContextWindow int64 = 272_000
	// claudeHaikuContextWindow is the one Anthropic window that differs from
	// the rest (Sonnet and Opus are 200_000).
	claudeHaikuContextWindow int64 = 100_000
	// qwenPlusContextWindow is qwen3.7-plus's documented window.
	qwenPlusContextWindow int64 = 256_000
	// geminiContextWindow is the Gemini family's documented window.
	geminiContextWindow int64 = 1_000_000
)

// ModelMeta is the static metadata of one opencode model.
type ModelMeta struct {
	// ID is the model id as the inference gateway spells it.
	ID string
	// Protocol is the wire protocol the model speaks on the surface that lists
	// it. It is stored redundantly with ProtocolFor; a test holds the two in
	// agreement so a protocol edit that misses one of them fails loudly.
	Protocol Protocol
	// Free marks the Console family that works without any credential, with
	// the literal "public" bearer.
	Free bool
	// ContextWindow is the model's prompt window in tokens. It is a fallback
	// for an id models.json does not carry; the catalog's value wins.
	ContextWindow int64
	// Reasoning reports whether the model exposes thinking/reasoning output.
	// Like ContextWindow it is a fallback, and it says only that the model
	// thinks — which levels it offers is models.json's answer.
	Reasoning bool
	// Attachments reports whether the model accepts image input. Fallback, as
	// above.
	Attachments bool
}

// ConsoleModels is the Console (free and pay-as-you-go) table. The systemone
// endpoint (jev-*) is excluded: it is a structured decision API, not a chat
// model, and ProtocolFor refuses it by prefix.
var ConsoleModels = []ModelMeta{
	// Free family: usable with the literal "public" bearer.
	{ID: "big-pickle", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "beam-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "glm-5.3-flash-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "deepseek-v4.1-flash-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "minimax-m3.1-flash-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "ll-alpha-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "longcat-2.5-preview-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "exo-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "mimo-v2.6-flash-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "mimo-v2.5-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "nemotron-3-ultra-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "nemotron-3.5-lightning-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "nemotron-3.5-super-vl-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true, Attachments: true},

	// Models the official table names individually on Console.
	{ID: "qwen3.8-max", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true, Attachments: true},
	{ID: "qwen3.8-flash", Protocol: ProtocolAnthropic, ContextWindow: defaultContextWindow, Reasoning: true, Attachments: true},
	{ID: "qwen3.7-max", Protocol: ProtocolAnthropic, ContextWindow: defaultContextWindow},
	{ID: "qwen3.7-plus", Protocol: ProtocolAnthropic, ContextWindow: qwenPlusContextWindow},
	{ID: "qwen3.6-plus", Protocol: ProtocolAnthropic, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "qwen3.5-plus", Protocol: ProtocolAnthropic, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "mistral-large-4", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "grok-build-0.1", Protocol: ProtocolResponses, ContextWindow: grokContextWindow, Reasoning: true},
}

// GoModels is the Go subscription table: every model the official table lists
// for the subscription, with the protocol that surface actually serves.
// Nothing here is Free: the subscription token pays for all of it, so the free
// family's "public" bearer does not apply.
var GoModels = []ModelMeta{
	{ID: "grok-4.7", Protocol: ProtocolResponses, ContextWindow: grokContextWindow, Reasoning: true},
	{ID: "gpt-6-luna", Protocol: ProtocolResponses, ContextWindow: gptContextWindow, Reasoning: true, Attachments: true},
	{ID: "gpt-5.6-luna", Protocol: ProtocolResponses, ContextWindow: gptContextWindow, Reasoning: true, Attachments: true},
	{ID: "muse-spark-1.3-contributor", Protocol: ProtocolResponses, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "muse-spark-1.2-contributor", Protocol: ProtocolResponses, ContextWindow: defaultContextWindow, Reasoning: true},

	{ID: "claude-haiku-5-5", Protocol: ProtocolAnthropic, ContextWindow: claudeHaikuContextWindow, Reasoning: true, Attachments: true},
	{ID: "qwen3.8-max", Protocol: ProtocolAnthropic, ContextWindow: defaultContextWindow, Reasoning: true, Attachments: true},
	{ID: "qwen3.8-flash", Protocol: ProtocolAnthropic, ContextWindow: defaultContextWindow, Reasoning: true, Attachments: true},
	{ID: "qwen3.7-plus", Protocol: ProtocolAnthropic, ContextWindow: qwenPlusContextWindow, Reasoning: true},
	{ID: "minimax-m3", Protocol: ProtocolAnthropic, ContextWindow: defaultContextWindow, Reasoning: true},

	{ID: "gemini-4-argon-contributor", Protocol: ProtocolGoogle, ContextWindow: geminiContextWindow, Reasoning: true, Attachments: true},

	{ID: "glm-5.3-flash", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "glm-5.3", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "glm-5.2", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "kimi-k3", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "kimi-k2.7-code", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "longcat-2.0", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "deepseek-v4.1-flash", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "deepseek-v4-pro", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "mimo-v2.6-flash", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "mimo-v2.6-pro", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "mimo-v2.5", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "mimo-v2.5-pro", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "hy4-preview", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "hy3", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "step-5-preview", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "minimax-m3.1-flash-free", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "longcat-2.5-preview-free", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "beam-free", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
}

// LookupModelMeta finds a model's metadata on a provider's surface.
func LookupModelMeta(provider, id string) (ModelMeta, bool) {
	id = trimModelID(id)
	if id == "" {
		return ModelMeta{}, false
	}
	models := ConsoleModels
	if isGoSubscription(provider) {
		models = GoModels
	}
	for _, m := range models {
		if m.ID == id {
			return m, true
		}
	}
	return ModelMeta{}, false
}

// DefaultModelMeta describes an id no table lists — a model the live catalog
// reported after this table was written. The protocol is the documented
// default, chat/completions, and the window is the conservative one.
func DefaultModelMeta(id string) ModelMeta {
	return ModelMeta{ID: trimModelID(id), Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow}
}

// EffortsForProtocol says which levels of a model's own set survive the client
// the protocol routes to. chat/completions and responses pass the parameter
// through verbatim; the Google generateContent client carries no effort
// parameter at all, so none of them can be sent (nil), and any other protocol
// falls in with it.
//
// The levels themselves are models.json's data (pkg/llm.ReasoningEfforts); this
// question is only about the transport, and the Anthropic client's own
// capability lives with that client (pkg/llm/anthropic.AcceptsEffort).
func EffortsForProtocol(protocol Protocol, efforts []string) []string {
	if len(efforts) == 0 {
		return nil
	}
	switch protocol {
	case ProtocolChatCompletions, ProtocolResponses:
		return append([]string(nil), efforts...)
	default:
		return nil
	}
}

// trimModelID normalizes an id for lookup: the gateway's ids are lower case
// with no surrounding space, but a picker or a config file can hand over
// either.
func trimModelID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}
