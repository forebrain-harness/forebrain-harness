package opencode

import "strings"

// Static model metadata for the two OpenCode surfaces.
//
// The ids come from the 2026-10-21 official model table; the live
// GET /v1/models endpoint reports the same ids (85 on 2026-10-21) but carries
// no protocol, context window or capability metadata at all, which is why the
// metadata is transcribed here instead of discovered.
//
// The tables are deliberately not exhaustive. Console lists whole families
// ("all gpt-*", "all claude-*", "all gemini-*") whose members are not named in
// the table, so those ids get their protocol from ProtocolFor and their
// metadata from DefaultModelMeta. Go's model set is small enough that the
// official table names every member, and GoModels lists exactly those.

const (
	// defaultContextWindow is the window assumed for an id whose real window is
	// not documented here: conservative, so a picker never promises more room
	// than the model has.
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
	// ContextWindow is the model's prompt window in tokens.
	ContextWindow int64
	// Reasoning reports whether the model exposes thinking/reasoning output.
	Reasoning bool
	// Efforts lists the thinking levels the model offers, in the order the
	// official catalog names them; empty means the model either does not
	// reason at all (Reasoning false) or reasons with nothing to pick between
	// (a toggle or a token budget).
	//
	// This column is what the model offers, not what this build can send: a
	// protocol whose client drops the parameter trims the list through
	// EffortsForProtocol before any picker sees it.
	Efforts []string
	// Attachments reports whether the model accepts image input.
	Attachments bool
}

// The effort columns below are transcribed from the official catalog
// (models.dev entries opencode / opencode-go, pulled 2026-10-21), with two
// rules:
//
//   - reasoning_options entries of type "effort" contribute their values in
//     order (several such entries merge in order, de-duplicated);
//   - a model whose options are only a toggle, a token budget or nothing is
//     Reasoning:true with no Efforts — it thinks, but the surface has no level
//     to choose;
//   - an id the catalog does not carry (qwen3.7-max, qwen3.7-plus on Console)
//     stays Reasoning:false with no Efforts: no evidence, no invention.

// ConsoleModels is the Console (free and pay-as-you-go) table. The systemone
// endpoint (jev-*) is excluded: it is not a chat model.
var ConsoleModels = []ModelMeta{
	// Free family: usable with the literal "public" bearer.
	{ID: "big-pickle", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "beam-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "glm-5.3-flash-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"low", "high", "max"}},
	{ID: "deepseek-v4.1-flash-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"low", "high", "max"}},
	{ID: "minimax-m3.1-flash-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "ll-alpha-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "longcat-2.5-preview-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "exo-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"high"}},
	{ID: "mimo-v2.6-flash-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "mimo-v2.5-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "nemotron-3-ultra-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "nemotron-3.5-lightning-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "nemotron-3.5-super-vl-free", Protocol: ProtocolChatCompletions, Free: true, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"none", "minimal", "low", "medium", "high", "xhigh"}, Attachments: true},

	// Models the official table names individually on Console.
	{ID: "qwen3.8-max", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true, Attachments: true},
	{ID: "qwen3.8-flash", Protocol: ProtocolAnthropic, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"low", "medium", "xhigh"}, Attachments: true},
	{ID: "qwen3.7-max", Protocol: ProtocolAnthropic, ContextWindow: defaultContextWindow},
	{ID: "qwen3.7-plus", Protocol: ProtocolAnthropic, ContextWindow: qwenPlusContextWindow},
	{ID: "qwen3.6-plus", Protocol: ProtocolAnthropic, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "qwen3.5-plus", Protocol: ProtocolAnthropic, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "mistral-large-4", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"none", "high"}},
	{ID: "grok-build-0.1", Protocol: ProtocolResponses, ContextWindow: grokContextWindow, Reasoning: true},
}

// GoModels is the Go subscription table: every model the 2026-10-21 official
// table lists for the subscription, with the protocol that surface actually
// serves. Nothing here is Free: the subscription token pays for all of it, so
// the free family's "public" bearer does not apply.
var GoModels = []ModelMeta{
	{ID: "grok-4.7", Protocol: ProtocolResponses, ContextWindow: grokContextWindow, Reasoning: true, Efforts: []string{"low", "medium", "high", "xhigh"}},
	{ID: "gpt-6-luna", Protocol: ProtocolResponses, ContextWindow: gptContextWindow, Reasoning: true, Efforts: []string{"none", "low", "medium", "high", "xhigh", "max"}, Attachments: true},
	{ID: "gpt-5.6-luna", Protocol: ProtocolResponses, ContextWindow: gptContextWindow, Reasoning: true, Efforts: []string{"none", "low", "medium", "high", "xhigh", "max"}, Attachments: true},
	{ID: "muse-spark-1.3-contributor", Protocol: ProtocolResponses, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"minimal", "low", "medium", "high", "xhigh"}},
	{ID: "muse-spark-1.2-contributor", Protocol: ProtocolResponses, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"minimal", "low", "medium", "high", "xhigh"}},

	{ID: "claude-haiku-5-5", Protocol: ProtocolAnthropic, ContextWindow: claudeHaikuContextWindow, Reasoning: true, Efforts: []string{"low", "medium", "high", "xhigh", "max"}, Attachments: true},
	{ID: "qwen3.8-max", Protocol: ProtocolAnthropic, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"low", "medium", "xhigh"}, Attachments: true},
	{ID: "qwen3.8-flash", Protocol: ProtocolAnthropic, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"low", "medium", "xhigh"}, Attachments: true},
	{ID: "qwen3.7-plus", Protocol: ProtocolAnthropic, ContextWindow: qwenPlusContextWindow, Reasoning: true},
	{ID: "minimax-m3", Protocol: ProtocolAnthropic, ContextWindow: defaultContextWindow, Reasoning: true},

	{ID: "gemini-4-argon-contributor", Protocol: ProtocolGoogle, ContextWindow: geminiContextWindow, Reasoning: true, Efforts: []string{"low", "medium", "high"}, Attachments: true},

	{ID: "glm-5.3-flash", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"low", "high", "max"}},
	{ID: "glm-5.3", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"low", "high", "max"}},
	{ID: "glm-5.2", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"high", "max"}},
	{ID: "kimi-k3", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"max"}},
	{ID: "kimi-k2.7-code", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "longcat-2.0", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "deepseek-v4.1-flash", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"low", "high", "max"}},
	{ID: "deepseek-v4-pro", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"high", "max"}},
	{ID: "mimo-v2.6-flash", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "mimo-v2.6-pro", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "mimo-v2.5", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "mimo-v2.5-pro", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "hy4-preview", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"none", "high"}},
	{ID: "hy3", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"none", "low", "high"}},
	{ID: "step-5-preview", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"low", "medium", "high"}},
	{ID: "minimax-m3.1-flash-free", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "longcat-2.5-preview-free", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true},
	{ID: "beam-free", Protocol: ProtocolChatCompletions, ContextWindow: defaultContextWindow, Reasoning: true, Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
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

// EffortsForProtocol keeps the levels the client a protocol routes to can
// actually send. The table records what each model offers; this says what this
// build can transmit, so a picker never offers a level that would be silently
// dropped. chat/completions and responses pass the params through verbatim;
// the Anthropic client accepts low/medium/high/max; the Google generateContent
// client carries no effort parameter at all.
//
// The Anthropic whitelist is a copy of the switch in
// pkg/llm/anthropic.newAgentLLM; a test holds the two in agreement so neither
// can drift.
func EffortsForProtocol(protocol Protocol, efforts []string) []string {
	if len(efforts) == 0 {
		return nil
	}
	switch protocol {
	case ProtocolChatCompletions, ProtocolResponses:
		return append([]string(nil), efforts...)
	case ProtocolAnthropic:
		out := make([]string, 0, len(efforts))
		for _, e := range efforts {
			if anthropicEfforts[e] {
				out = append(out, e)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

// anthropicEfforts is the set the Anthropic client forwards
// (pkg/llm/anthropic/agent_llm.go).
var anthropicEfforts = map[string]bool{
	"low":    true,
	"medium": true,
	"high":   true,
	"max":    true,
}

// trimModelID normalizes an id for lookup: the gateway's ids are lower case
// with no surrounding space, but a picker or a config file can hand over
// either.
func trimModelID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}
