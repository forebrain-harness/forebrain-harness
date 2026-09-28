// Per-agent LLM provider config: models, request params, env, and validation.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// FindLLMProviderIndex returns the index of the first llm_providers entry
// whose provider matches provider (case-insensitive, trimmed) and, when model
// is non-empty, whose model also matches model, or -1 when none matches or the
// list is empty. It is the add-or-replace lookup for flows such as /connect
// that configure one provider+model connection per entry: an existing match is
// updated in place rather than overwriting the primary slot, and the absence
// of a match signals "add a new entry". Matching the model too is what lets a
// second model under the same provider become its own entry instead of
// replacing the model the provider's first entry carries. An empty model
// carries no identity and falls back to the provider's first entry.
func FindLLMProviderIndex(def AgentDefinition, provider, model string) int {
	want := strings.TrimSpace(provider)
	if want == "" {
		return -1
	}
	wantModel := strings.TrimSpace(model)
	for i, entry := range def.LLMProviders {
		if !strings.EqualFold(strings.TrimSpace(entry.Provider), want) {
			continue
		}
		if wantModel == "" || strings.EqualFold(strings.TrimSpace(entry.Model), wantModel) {
			return i
		}
	}
	return -1
}

func ResolvedLLMConfigs(def AgentDefinition) []AgentLLMProviderConfig {
	if len(def.LLMProviders) == 0 {
		return nil
	}
	out := expandLLMProviderConfigs(def.LLMProviders)
	for i := range out {
		out[i] = CloneAgentLLMProviderConfig(out[i])
	}
	return out
}

func PrimaryLLM(def AgentDefinition) *AgentLLMProviderConfig {
	if len(def.LLMProviders) == 0 {
		return nil
	}
	return &def.LLMProviders[0]
}

func NormalizeLLMProviderEntries(def *AgentDefinition) {
	if def == nil {
		return
	}
	def.LLMProviders = expandLLMProviderConfigs(def.LLMProviders)
}

func expandLLMProviderConfigs(in []AgentLLMProviderConfig) []AgentLLMProviderConfig {
	if len(in) == 0 {
		return nil
	}
	out := make([]AgentLLMProviderConfig, 0, len(in))
	for _, item := range in {
		models := append(StringList(nil), item.Models...)
		if len(models) == 0 && item.Model != "" {
			models = StringList{item.Model}
		}
		if len(models) == 0 {
			out = append(out, CloneAgentLLMProviderConfig(item))
			continue
		}
		for _, model := range models {
			cp := CloneAgentLLMProviderConfig(item)
			cp.Model = model
			cp.Models = nil
			out = append(out, cp)
		}
	}
	return out
}

type LLMSummary struct {
	Agent            string
	Provider         string
	Model            string
	BaseURL          string
	APIKey           string
	Params           string
	LLMEndpointCount int
	CatalogMatched   bool
	CatalogID        string
	CatalogName      string
	ContextWindow    int64
}

// PromoteMatchingLLMProvider finds an existing LLMProviders entry matching
// provider+model and moves it to index 0. This ensures the full config
// (including base_url, params, etc.) is used as the primary provider.
// The definition is normalized first, so an entry naming several models
// (model: [a, b]) is expanded into one entry per model and a second-item
// choice resolves instead of being reported missing.
// Returns an error if no matching entry is found.
func PromoteMatchingLLMProvider(cfg *Root, agent, provider, model string) error {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		agent = "main"
	}
	if cfg.Agents.Definitions == nil {
		return fmt.Errorf("no agent definitions")
	}
	def, ok := cfg.Agents.Definitions[agent]
	if !ok {
		return fmt.Errorf("unknown agent %q", agent)
	}
	NormalizeLLMProviderEntries(&def)
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	idx := -1
	for i, entry := range def.LLMProviders {
		if strings.EqualFold(strings.TrimSpace(entry.Provider), provider) &&
			strings.EqualFold(strings.TrimSpace(entry.Model), model) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("no matching llm_providers entry for %s/%s", provider, model)
	}
	if idx != 0 {
		// Move matched entry to front
		entry := def.LLMProviders[idx]
		copy(def.LLMProviders[1:idx+1], def.LLMProviders[0:idx])
		def.LLMProviders[0] = entry
	}
	cfg.Agents.Definitions[agent] = def
	return nil
}

// ReasoningEffort returns the normalized concrete reasoning.effort in params,
// or "" when it is absent.
func ReasoningEffort(params LLMRequestParams) string {
	if len(params) == 0 {
		return ""
	}
	var payload struct {
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if json.Unmarshal(params, &payload) != nil {
		return ""
	}
	return strings.TrimSpace(payload.Reasoning.Effort)
}

// WithReasoningEffort returns a deep-owned copy of params with
// reasoning.effort set to the normalized non-empty effort, or removes only
// reasoning.effort when effort is empty. Other params and other reasoning
// keys are preserved. An empty effort is an explicit removal, never "leave
// the old value".
func WithReasoningEffort(params LLMRequestParams, effort string) LLMRequestParams {
	payload := map[string]any{}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &payload)
	}
	effort = strings.ToLower(strings.TrimSpace(effort))
	reasoning, _ := payload["reasoning"].(map[string]any)
	if effort == "" {
		if reasoning != nil {
			delete(reasoning, "effort")
			if len(reasoning) == 0 {
				delete(payload, "reasoning")
			}
		}
	} else {
		if reasoning == nil {
			reasoning = map[string]any{}
			payload["reasoning"] = reasoning
		}
		reasoning["effort"] = effort
	}
	if len(payload) == 0 {
		return nil
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return LLMRequestParams(b)
}

// SetLLMProviderReasoningEffort updates only the exact provider+model entry's
// reasoning effort and promotes that complete entry to the default position.
// It never borrows params or credentials from another entry: the matched
// entry keeps everything else it already carried. A pair that is not
// configured is an error, and the config is left untouched in that case.
func SetLLMProviderReasoningEffort(
	cfg *Root, agent, provider, model, effort string,
) error {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		agent = "main"
	}
	if cfg.Agents.Definitions == nil {
		return fmt.Errorf("no agent definitions")
	}
	def, ok := cfg.Agents.Definitions[agent]
	if !ok {
		return fmt.Errorf("unknown agent %q", agent)
	}
	NormalizeLLMProviderEntries(&def)
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	idx := -1
	for i, entry := range def.LLMProviders {
		if strings.EqualFold(strings.TrimSpace(entry.Provider), provider) &&
			strings.EqualFold(strings.TrimSpace(entry.Model), model) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("no matching llm_providers entry for %s/%s", provider, model)
	}
	entry := def.LLMProviders[idx]
	entry.Params = WithReasoningEffort(entry.Params, effort)
	if idx != 0 {
		copy(def.LLMProviders[1:idx+1], def.LLMProviders[0:idx])
	}
	def.LLMProviders[0] = entry
	cfg.Agents.Definitions[agent] = def
	return nil
}

// CloneAgentLLMProviderConfig returns a deep copy of c, duplicating the slice
// fields (Params, Models) so callers can mutate the clone independently.
func CloneAgentLLMProviderConfig(c AgentLLMProviderConfig) AgentLLMProviderConfig {
	out := c
	if len(c.Params) > 0 {
		out.Params = append(LLMRequestParams(nil), c.Params...)
	}
	if len(c.Models) > 0 {
		out.Models = append(StringList(nil), c.Models...)
	}
	return out
}

// agentLLMProviderWire is the on-disk shape: api_key, base_url, api_path, and
// params sit at the same level as provider and model. model accepts a scalar or
// a list.
type agentLLMProviderWire struct {
	Provider string           `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model    StringList       `yaml:"model,omitempty" json:"model,omitempty"`
	APIKey   string           `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	BaseURL  string           `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	APIPath  string           `yaml:"api_path,omitempty" json:"api_path,omitempty"`
	Params   LLMRequestParams `yaml:"params,omitempty" json:"params,omitempty"`
}

func (c *AgentLLMProviderConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var w agentLLMProviderWire
	if err := unmarshal(&w); err != nil {
		return err
	}
	return c.mergeFromWire(w)
}

func (c *AgentLLMProviderConfig) UnmarshalJSON(data []byte) error {
	var w agentLLMProviderWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	return c.mergeFromWire(w)
}

func (c *AgentLLMProviderConfig) mergeFromWire(w agentLLMProviderWire) error {
	c.Provider = w.Provider
	c.Models = append(StringList(nil), w.Model...)
	c.Model = w.Model.first()
	c.APIKey = w.APIKey
	c.BaseURL = w.BaseURL
	c.APIPath = w.APIPath
	if len(w.Params) > 0 {
		c.Params = append(LLMRequestParams(nil), w.Params...)
	} else {
		c.Params = nil
	}
	return nil
}

type LLMRequestParams []byte

func (p LLMRequestParams) Bytes() []byte {
	return []byte(p)
}

func (p LLMRequestParams) MarshalJSON() ([]byte, error) {
	if len(p) == 0 {
		return []byte("null"), nil
	}
	return []byte(p), nil
}

func (p *LLMRequestParams) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*p = nil
		return nil
	}
	if !json.Valid(data) {
		return fmt.Errorf("llm params: invalid json")
	}
	var probe any
	_ = json.Unmarshal(data, &probe)
	if _, ok := probe.(map[string]interface{}); !ok {
		return fmt.Errorf("llm params: must be a json object")
	}
	cp := append(LLMRequestParams(nil), data...)
	*p = cp
	return nil
}

func (p LLMRequestParams) MarshalYAML() (interface{}, error) {
	if len(p) == 0 {
		return nil, nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(p, &m); err != nil {
		return nil, err
	}
	return toYAMLCompatibleMap(m), nil
}

func (p *LLMRequestParams) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var raw interface{}
	if err := unmarshal(&raw); err != nil {
		return err
	}
	if raw == nil {
		*p = nil
		return nil
	}
	b, err := llmParamsYAMLValueToJSONBytes(raw)
	if err != nil {
		return err
	}
	*p = b
	return nil
}

func llmParamsYAMLValueToJSONBytes(raw interface{}) (LLMRequestParams, error) {
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil, nil
		}
		if !json.Valid([]byte(s)) {
			return nil, fmt.Errorf("llm params: invalid json")
		}
		var probe any
		_ = json.Unmarshal([]byte(s), &probe)
		if _, ok := probe.(map[string]interface{}); !ok {
			return nil, fmt.Errorf("llm params: must be a json object")
		}
		return LLMRequestParams(s), nil
	default:
		j := toJSONCompatibleValue(raw)
		b, err := json.Marshal(j)
		if err != nil {
			return nil, err
		}
		var probe any
		_ = json.Unmarshal(b, &probe)
		if _, ok := probe.(map[string]interface{}); !ok {
			return nil, fmt.Errorf("llm params: must be a json object")
		}
		return LLMRequestParams(b), nil
	}
}

func toJSONCompatibleValue(v interface{}) interface{} {
	switch x := v.(type) {
	case map[interface{}]interface{}:
		m := make(map[string]interface{}, len(x))
		for k, val := range x {
			m[fmt.Sprint(k)] = toJSONCompatibleValue(val)
		}
		return m
	case map[string]interface{}:
		m := make(map[string]interface{}, len(x))
		for k, val := range x {
			m[k] = toJSONCompatibleValue(val)
		}
		return m
	case []interface{}:
		s := make([]interface{}, len(x))
		for i := range x {
			s[i] = toJSONCompatibleValue(x[i])
		}
		return s
	default:
		return x
	}
}

func toYAMLCompatibleMap(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = toYAMLCompatibleValue(v)
	}
	return out
}

func toYAMLCompatibleValue(v interface{}) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		return toYAMLCompatibleMap(x)
	case []interface{}:
		s := make([]interface{}, len(x))
		for i := range x {
			s[i] = toYAMLCompatibleValue(x[i])
		}
		return s
	default:
		return x
	}
}

func ParseLLMRequestParamsJSON(s string) (LLMRequestParams, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var p LLMRequestParams
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return nil, err
	}
	return p, nil
}

// providerEnvKey returns the environment variable name for a provider's API
// key: UPPERCASE(provider)_API_KEY. Every provider follows this uniform
// scheme, derived from the provider name — no provider-specific exceptions.
func providerEnvKey(provider string) string {
	return strings.ToUpper(strings.TrimSpace(provider)) + "_API_KEY"
}

// providerEnvBaseURL returns the environment variable name for a provider's
// base URL: UPPERCASE(provider)_BASE_URL.
func providerEnvBaseURL(provider string) string {
	return strings.ToUpper(strings.TrimSpace(provider)) + "_BASE_URL"
}

// ProviderAPIKeyLookup resolves the API key for a provider from environment
// variables using the uniform UPPERCASE(provider)_API_KEY scheme.
func ProviderAPIKeyLookup(get func(string) string, provider string) string {
	return strings.TrimSpace(get(providerEnvKey(strings.ToLower(strings.TrimSpace(provider)))))
}

// ProviderBaseURLLookup resolves the base URL for a provider from environment
// variables using the uniform UPPERCASE(provider)_BASE_URL scheme.
func ProviderBaseURLLookup(get func(string) string, provider string) string {
	return strings.TrimSpace(get(providerEnvBaseURL(strings.ToLower(strings.TrimSpace(provider)))))
}

type MainAgentLLMValidation struct {
	Provider string
	Model    string
	APIKey   string
	BaseURL  string
}

func ValidateMainAgentLLMConfigured(root *Root) MainAgentLLMValidation {
	return ValidateAgentLLMConfigured(root, "main")
}

// ValidateAgentLLMConfigured reports the LLM fields one agent definition
// carries. Every primary agent has its own definition and its own provider, so
// a flow that configures the active one has to check the definition it wrote
// rather than main's, or configuring a second primary agent would be judged by
// whether the first is set up.
func ValidateAgentLLMConfigured(root *Root, agentName string) MainAgentLLMValidation {
	var out MainAgentLLMValidation
	if root == nil {
		return out
	}
	name := strings.TrimSpace(agentName)
	if name == "" {
		name = "main"
	}
	def, ok := root.Agents.Definitions[name]
	if !ok {
		return out
	}
	p := PrimaryLLM(def)
	if p == nil {
		return out
	}
	out.Provider = strings.TrimSpace(p.Provider)
	out.Model = strings.TrimSpace(p.Model)
	out.APIKey = strings.TrimSpace(p.APIKey)
	out.BaseURL = strings.TrimSpace(p.BaseURL)
	return out
}

func (v MainAgentLLMValidation) Complete() bool {
	if v.Provider == "" || v.Model == "" || v.BaseURL == "" {
		return false
	}
	if strings.EqualFold(v.Provider, "chatgpt") {
		return true
	}
	return v.APIKey != ""
}

func (v MainAgentLLMValidation) MissingFields() []string {
	var out []string
	if v.Provider == "" {
		out = append(out, "provider")
	}
	if v.Model == "" {
		out = append(out, "model")
	}
	if v.APIKey == "" && !strings.EqualFold(v.Provider, "chatgpt") {
		out = append(out, "api_key")
	}
	if v.BaseURL == "" {
		out = append(out, "base_url")
	}
	return out
}
