// The install-wide model view: the fb_llm_models rows plus the entries a
// pre-table agents.definitions.<id>.llm_providers list still carries, merged
// into the one list every agent reads (plan 260).
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"
	"sync"
)

// LLMModelConfig is one configured model as the runtime reads it: a row of the
// install-wide fb_llm_models table, or an entry lifted from a pre-table
// agents.definitions.<id>.llm_providers list (LegacyLLMModelID names those).
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
}

// LLMModelCaps overrides the model catalog for one row; a nil pointer, a nil
// slice or "" means "the catalog decides". plan 263 wires them into the runtime.
type LLMModelCaps struct {
	Images, PDF, Video, Audio, Reasoning, ReasoningOff *bool
	Efforts                                            []string
	DefaultEffort                                      string
	ContextWindow, MaxOutput                           *int64
}

// LegacyLLMModelIDPrefix marks an id ConfiguredLLMs lifted out of a pre-table
// list rather than read from fb_llm_models.
const LegacyLLMModelIDPrefix = "llm-legacy-"

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

// LegacyLLMModelID is the id a pre-table entry gets: stable across processes
// and restarts, so a reference recorded against the lifted entry still names
// the same row once plan 262's migration writes it into fb_llm_models.
func LegacyLLMModelID(provider, model, baseURL, apiPath string) string {
	provider, model, baseURL, apiPath = NormalizeLLMModelIdentity(provider, model, baseURL, apiPath)
	sum := sha256.Sum256([]byte(provider + "\x00" + model + "\x00" + baseURL + "\x00" + apiPath))
	return LegacyLLMModelIDPrefix + hex.EncodeToString(sum[:])[:16]
}

// ConfiguredLLMs is every model the install has configured, in display order:
// the table's rows (root.LLMModels, already in created_at order), then entries
// lifted from legacy llm_providers lists — main's first, then every other
// definition by name, each list in stored order, `model: [a, b]` expanded.
// Rows are de-duplicated by normalized identity; the first wins, so a table
// row beats a legacy entry naming the same endpoint.
func ConfiguredLLMs(root *Root) []AgentLLMProviderConfig {
	if root == nil {
		return nil
	}
	var out []AgentLLMProviderConfig
	seen := make(map[string]bool)
	add := func(entry AgentLLMProviderConfig) {
		key := strings.Join([]string{entry.Provider, entry.Model, entry.BaseURL, entry.APIPath}, "\x00")
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, CloneAgentLLMProviderConfig(entry))
	}
	for _, row := range root.LLMModels {
		add(llmProviderFromModelRow(row))
	}
	for _, name := range legacyListAgentOrder(root.Agents.Definitions) {
		for _, entry := range ownLegacyEntries(root.Agents.Definitions[name]) {
			add(entry)
		}
	}
	return out
}

// ResolveModelRef finds the row a stored reference names. A reference is a
// row id (what every write stores from plan 262 on) or a legacy
// "provider/model" label, split on the first slash. A label resolves inside
// agentName's own legacy list first — the list it was written against — and
// then against the whole view by provider+model (case-insensitive), first
// match wins. ok is false when nothing matches.
func ResolveModelRef(root *Root, agentName, ref string) (AgentLLMProviderConfig, bool) {
	if root == nil {
		return AgentLLMProviderConfig{}, false
	}
	return resolveModelRef(root, ConfiguredLLMs(root), agentName, ref)
}

// EffectiveDefaultLLMFor resolves the model an empty selection runs on for an
// agent. In order: the agent's default_model reference; the first entry of the
// agent's own legacy list; main's default_model reference; main's first own
// legacy entry; the first row of the view. A reference that is a row id no
// longer present is skipped (logged once per process at Warn) — a deleted row
// must not stop the install; a legacy label that matches nothing returns nil,
// the loader's existing verdict (validatePrimaryAgents) for a dangling label.
func EffectiveDefaultLLMFor(root *Root, agentName string) *AgentLLMProviderConfig {
	if root == nil {
		return nil
	}
	view := ConfiguredLLMs(root)
	name := agentDefinitionKey(agentName)
	if entry, decided := ownDefaultModel(root, view, name); decided {
		return entry
	}
	// An agent with nothing of its own runs on main's choice, the way the
	// runtime resolved it before the table existed.
	if name != "main" {
		if entry, decided := ownDefaultModel(root, view, "main"); decided {
			return entry
		}
	}
	if len(view) > 0 {
		entry := CloneAgentLLMProviderConfig(view[0])
		return &entry
	}
	return nil
}

// HasOwnModelChoice reports whether a definition picks its own model: a
// default_model reference, or a legacy list of its own.
func HasOwnModelChoice(root *Root, agentName string) bool {
	if root == nil {
		return false
	}
	def, ok := root.Agents.Definitions[agentDefinitionKey(agentName)]
	if !ok {
		return false
	}
	if strings.TrimSpace(def.DefaultModel) != "" {
		return true
	}
	return len(ownLegacyEntries(def)) > 0
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

// ownDefaultModel resolves the model a definition names for itself: its
// default_model reference, else the first entry of its own legacy list.
// decided reports that the answer is final — including a nil entry, which a
// dangling legacy label produces (never a quiet fall-back to something else).
func ownDefaultModel(root *Root, view []AgentLLMProviderConfig, name string) (*AgentLLMProviderConfig, bool) {
	def, ok := root.Agents.Definitions[name]
	if !ok {
		return nil, false
	}
	if ref := strings.TrimSpace(def.DefaultModel); ref != "" {
		if entry, found := resolveModelRef(root, view, name, ref); found {
			entry = CloneAgentLLMProviderConfig(entry)
			return &entry, true
		}
		if strings.Contains(ref, "/") {
			return nil, true
		}
		// A row the model page deleted: the install keeps running on the next
		// choice rather than refusing to start.
		warnDanglingModelRefOnce(name, ref)
	}
	if entries := ownLegacyEntries(def); len(entries) > 0 {
		entry := CloneAgentLLMProviderConfig(entries[0])
		return &entry, true
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
	if def, ok := root.Agents.Definitions[agentDefinitionKey(agentName)]; ok {
		for _, entry := range ownLegacyEntries(def) {
			if equalFoldPair(entry.Provider, entry.Model, provider, model) {
				return CloneAgentLLMProviderConfig(entry), true
			}
		}
	}
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

// legacyListAgentOrder is the order ConfiguredLLMs lifts legacy lists in:
// main's first, then every other definition by name, so the view does not
// depend on map iteration.
func legacyListAgentOrder(defs map[string]AgentDefinition) []string {
	ids := sortedDefinitionIDs(defs)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "main" {
			continue
		}
		out = append(out, id)
	}
	if _, ok := defs["main"]; ok {
		out = append([]string{"main"}, out...)
	}
	return out
}

// ownLegacyEntries lifts a definition's pre-table list into view entries:
// `model: [a, b]` expanded, the four identity fields normalized, and each
// entry named by its deterministic LegacyLLMModelID. The empty placeholder
// entry normalize() keeps so a primary agent has a slot to merge into carries
// no identity and is skipped.
func ownLegacyEntries(def AgentDefinition) []AgentLLMProviderConfig {
	expanded := expandLLMProviderConfigs(def.LLMProviders)
	out := make([]AgentLLMProviderConfig, 0, len(expanded))
	for _, entry := range expanded {
		if strings.TrimSpace(entry.Provider) == "" && strings.TrimSpace(entry.Model) == "" {
			continue
		}
		out = append(out, llmProviderFromLegacyEntry(entry))
	}
	return out
}

// llmProviderFromLegacyEntry projects one pre-table list entry into the view.
func llmProviderFromLegacyEntry(entry AgentLLMProviderConfig) AgentLLMProviderConfig {
	out := CloneAgentLLMProviderConfig(entry)
	out.Provider, out.Model, out.BaseURL, out.APIPath = NormalizeLLMModelIdentity(
		entry.Provider, entry.Model, entry.BaseURL, entry.APIPath)
	out.Models = nil
	out.ID = LegacyLLMModelID(entry.Provider, entry.Model, entry.BaseURL, entry.APIPath)
	return out
}

// llmProviderFromModelRow projects one fb_llm_models row into the view.
func llmProviderFromModelRow(row LLMModelConfig) AgentLLMProviderConfig {
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
