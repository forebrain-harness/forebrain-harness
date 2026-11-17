// The install-wide model view: the fb_llm_models rows, in the one list every
// agent reads (plan 260). Every model choice — a primary agent's default, a
// built-in subagent type's row, a subagent's own pin — is a reference to one of
// these rows; nothing else configures a provider (plan 315).
package config

import (
	"log/slog"
	"strings"
	"sync"
)

// LLMModelConfig is one configured model as the runtime reads it: a row of the
// install-wide fb_llm_models table.
type LLMModelConfig struct {
	ID       string
	Provider string
	Model    string
	BaseURL  string
	APIPath  string
	APIKey   string
	Params   LLMRequestParams
	Caps     LLMModelCaps
	// URLVerbatim (「自定义协议」): requests go to BaseURL as stored; APIPath
	// only names the protocol. plan 263 wires it into the clients.
	URLVerbatim bool
	// SubagentAllowed is the row's authorization switch (owner 2026-11-10):
	// the agent may pick this row for a subagent on its own through the model
	// argument. Projected from the table row; authorization itself reads the
	// table live.
	SubagentAllowed bool
	// CatalogProvider is the provider whose model data describes Model (owner
	// 2026-11-16): the row's own Provider/Model stay what requests use, while
	// this names the catalog entry its capabilities come from. "" means the
	// row's own pair. Only a custom row may name one, and it is never sent.
	CatalogProvider string
}

// LLMModelCaps overrides the model catalog for one row; a nil pointer, a nil
// slice or "" means "the catalog decides". plan 263 wires them into the runtime.
type LLMModelCaps struct {
	Images, PDF, Video, Audio, Reasoning, ReasoningOff *bool
	Efforts                                            []string
	DefaultEffort                                      string
	ContextWindow, MaxOutput                           *int64
}

// NormalizeLLMModelIdentity canonicalizes the four identity fields: provider
// lower-cased, model trimmed (case kept), base URL without trailing slashes,
// api_path with one leading slash and no trailing one. An empty api_path is
// filled with the protocol the provider speaks by default — /v1/messages for
// anthropic and minimax, /chat/completions for every other API-key provider —
// so "" and the explicit default are one identity. Login-backed providers
// (CredentialsBackedProvider) keep "": their endpoint comes from the login.
func NormalizeLLMModelIdentity(provider, model, baseURL, apiPath string) (string, string, string, string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	apiPath = strings.TrimSpace(apiPath)
	if apiPath != "" && !strings.HasPrefix(apiPath, "/") {
		apiPath = "/" + apiPath
	}
	apiPath = strings.TrimRight(apiPath, "/")
	if apiPath == "" && provider != "" && !CredentialsBackedProvider(provider) {
		if provider == "anthropic" || provider == "minimax" {
			apiPath = APIPathAnthropicMessages
		} else {
			apiPath = APIPathChatCompletions
		}
	}
	return provider, model, baseURL, apiPath
}

// ConfiguredLLMs is every model the install has configured, in the order the
// configuration stores it — not the display order a picker draws (turn's
// ModelChoices sorts its own projection; this order is what the default's
// first-row fallback reads): the table's rows (root.LLMModels, already in
// created_at order). Rows are de-duplicated by normalized identity; the first
// wins.
func ConfiguredLLMs(root *Root) []AgentLLMProviderConfig {
	if root == nil {
		return nil
	}
	var out []AgentLLMProviderConfig
	seen := make(map[string]bool)
	for _, row := range root.LLMModels {
		entry := ProviderFromModelRow(row)
		key := strings.Join([]string{entry.Provider, entry.Model, entry.BaseURL, entry.APIPath}, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, entry)
	}
	return out
}

// ResolveModelRef finds the row a stored reference names. A reference is a row
// id (what every write stores from plan 262 on) or a legacy "provider/model"
// label, split on the first slash, resolved against the whole view by
// provider+model (case-insensitive), first match wins. ok is false when
// nothing matches. agentName no longer narrows the search — one install-wide
// table serves every agent (plan 315) — it stays in the signature because every
// caller names the agent whose reference it is.
func ResolveModelRef(root *Root, agentName, ref string) (AgentLLMProviderConfig, bool) {
	if root == nil {
		return AgentLLMProviderConfig{}, false
	}
	return resolveModelRef(root, ConfiguredLLMs(root), agentName, ref)
}

// EffectiveDefaultLLMFor resolves the model an empty selection runs on for an
// agent. In order: the key's own choice (a built-in subagent type's
// agents.subagent_models row + effort, or a definition's default_model
// reference); main's choice; the first row of the view. A reference that is a
// row id no longer present is skipped (logged once per process at Warn), the
// way a legacy label that matches nothing is: a deleted row must not stop the
// install, and a label is an unset choice (plan 315).
func EffectiveDefaultLLMFor(root *Root, agentName string) *AgentLLMProviderConfig {
	if root == nil {
		return nil
	}
	view := ConfiguredLLMs(root)
	name := agentDefinitionKey(agentName)
	if entry, decided := ownModelChoice(root, view, name); decided {
		return entry
	}
	// An agent with nothing of its own runs on main's choice, the way the
	// runtime resolved it before the table existed.
	if name != "main" {
		if entry, decided := ownModelChoice(root, view, "main"); decided {
			return entry
		}
	}
	if len(view) > 0 {
		entry := CloneAgentLLMProviderConfig(view[0])
		return &entry
	}
	return nil
}

// HasOwnModelChoice reports whether a key picks its own model: a built-in
// subagent type with a global row of its own, or a definition with a
// default_model reference.
func HasOwnModelChoice(root *Root, agentName string) bool {
	if root == nil {
		return false
	}
	name := agentDefinitionKey(agentName)
	if row, ok := root.Agents.SubagentModels[name]; ok && strings.TrimSpace(row.Model) != "" {
		return true
	}
	def, ok := root.Agents.Definitions[name]
	if !ok {
		return false
	}
	return strings.TrimSpace(def.DefaultModel) != ""
}

// agentDefinitionKey is the definition key a caller's agent name reads as: an
// empty name is main, and keys are stored lower-cased (primaryAgentIDPattern).
func agentDefinitionKey(agentName string) string {
	name := strings.ToLower(strings.TrimSpace(agentName))
	if name == "" {
		return "main"
	}
	return name
}

// ownModelChoice resolves the model one key names for itself: for a built-in
// subagent type its agents.subagent_models row — the row id resolved against
// the view, carrying the row's own reasoning effort when the type names one —
// and for a definition its default_model reference. decided reports that the
// answer is final; a reference nothing resolves leaves the question open, so
// the caller falls back to main's choice (never a quiet answer of "no model").
func ownModelChoice(root *Root, view []AgentLLMProviderConfig, name string) (*AgentLLMProviderConfig, bool) {
	if row, ok := root.Agents.SubagentModels[name]; ok {
		ref := strings.TrimSpace(row.Model)
		if ref != "" {
			if entry, found := resolveModelRef(root, view, name, ref); found {
				entry = CloneAgentLLMProviderConfig(entry)
				if effort := strings.TrimSpace(row.ReasoningEffort); effort != "" {
					entry.Params = WithReasoningEffort(entry.Params, effort)
				}
				return &entry, true
			}
			warnDanglingModelRefOnce(name, ref)
			return nil, false
		}
	}
	def, ok := root.Agents.Definitions[name]
	if !ok {
		return nil, false
	}
	if ref := strings.TrimSpace(def.DefaultModel); ref != "" {
		if entry, found := resolveModelRef(root, view, name, ref); found {
			entry = CloneAgentLLMProviderConfig(entry)
			return &entry, true
		}
		// A row the model page deleted, or a label nothing carries any more:
		// the install keeps running on the next choice rather than refusing to
		// start.
		warnDanglingModelRefOnce(name, ref)
	}
	return nil, false
}

// resolveModelRef is ResolveModelRef over a view the caller already built.
func resolveModelRef(root *Root, view []AgentLLMProviderConfig, agentName, ref string) (AgentLLMProviderConfig, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return AgentLLMProviderConfig{}, false
	}
	for _, entry := range view {
		if entry.ID == ref {
			return CloneAgentLLMProviderConfig(entry), true
		}
	}
	// Not a row id. Only a label — "provider/model" — can still name an entry;
	// anything else is an id no row carries.
	if !strings.Contains(ref, "/") {
		return AgentLLMProviderConfig{}, false
	}
	provider, model, _ := strings.Cut(ref, "/")
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	for _, entry := range view {
		if equalFoldPair(entry.Provider, entry.Model, provider, model) {
			return CloneAgentLLMProviderConfig(entry), true
		}
	}
	return AgentLLMProviderConfig{}, false
}

func equalFoldPair(provider, model, wantProvider, wantModel string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), strings.TrimSpace(wantProvider)) &&
		strings.EqualFold(strings.TrimSpace(model), strings.TrimSpace(wantModel))
}

// ModelEndpointLabel is the request URL a configured row sends to, as a picker
// shows it: base url and api path joined exactly as the client joins them
// (https://api.deepseek.com/chat/completions). A URLVerbatim row (「自定义协议」)
// sends to its base url as stored, so the path only names the protocol; a row
// with no endpoint at all (a login-backed provider) has nothing to show.
func ModelEndpointLabel(baseURL, apiPath string, urlVerbatim bool) string {
	base := strings.TrimSpace(baseURL)
	if urlVerbatim {
		return base
	}
	return base + strings.TrimSpace(apiPath)
}

// ProviderFromModelRow projects one fb_llm_models row into the entry the
// runtime reads: a caller outside the package (283: the subagent dispatch reads
// a row live and needs the same entry the snapshot view would have projected)
// gets what ConfiguredLLMs gives, so a row read live and a row read through the
// snapshot carry the same projection.
func ProviderFromModelRow(row LLMModelConfig) AgentLLMProviderConfig {
	provider, model, baseURL, apiPath := NormalizeLLMModelIdentity(
		row.Provider, row.Model, row.BaseURL, row.APIPath)
	out := AgentLLMProviderConfig{
		ID:              row.ID,
		Provider:        provider,
		Model:           model,
		BaseURL:         baseURL,
		APIPath:         apiPath,
		APIKey:          row.APIKey,
		URLVerbatim:     row.URLVerbatim,
		SubagentAllowed: row.SubagentAllowed,
	}
	if row.Caps.MaxOutput != nil && *row.Caps.MaxOutput > 0 {
		out.MaxOutputTokens = *row.Caps.MaxOutput
	}
	if len(row.Params) > 0 {
		out.Params = append(LLMRequestParams(nil), row.Params...)
	}
	// The row's own default effort wins only when the params do not already
	// name one: an explicit reasoning.effort is a concrete choice for this
	// entry, and a row-level default is what fills its absence.
	if effort := strings.TrimSpace(row.Caps.DefaultEffort); effort != "" && ReasoningEffort(out.Params) == "" {
		out.Params = WithReasoningEffort(out.Params, effort)
	}
	return out
}

// danglingModelRefWarned keys the warn-once for a default_model reference no
// row carries: a per-turn config read must not repeat the same line for every
// turn of every conversation.
var danglingModelRefWarned sync.Map

func warnDanglingModelRefOnce(agentName, ref string) {
	key := agentName + "\x00" + ref
	if _, loaded := danglingModelRefWarned.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	// The reference names a row, never a key: nothing secret reaches the log.
	slog.Warn("default model reference names no configured model", "agent", agentName, "ref", ref)
}
