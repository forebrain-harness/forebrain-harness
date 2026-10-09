// Provider client construction from agent config.
package run

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/anthropic"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"gopkg.in/yaml.v3"
)

type LLMProviderYAML struct {
	Provider string                  `yaml:"provider" json:"provider"`
	Model    string                  `yaml:"model" json:"model"`
	APIKey   string                  `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	BaseURL  string                  `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	APIPath  string                  `yaml:"api_path,omitempty" json:"api_path,omitempty"`
	Params   appcfg.LLMRequestParams `yaml:"params,omitempty" json:"params,omitempty"`
	Models   appcfg.StringList       `yaml:"-" json:"-"`
}

type AgentConfigYAML struct {
	Description  string             `yaml:"description" json:"description"`
	LLMProviders []LLMProviderYAML  `yaml:"llm_providers,omitempty" json:"llm_providers,omitempty"`
	LLMChain     []*LLMProviderYAML `yaml:"-" json:"-"`
}

func (p *LLMProviderYAML) UnmarshalYAML(n *yaml.Node) error {
	var w struct {
		Provider string                  `yaml:"provider"`
		Model    appcfg.StringList       `yaml:"model"`
		APIKey   string                  `yaml:"api_key"`
		BaseURL  string                  `yaml:"base_url"`
		APIPath  string                  `yaml:"api_path"`
		Params   appcfg.LLMRequestParams `yaml:"params,omitempty"`
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "model" {
			if err := w.Model.UnmarshalYAMLNode(n.Content[i+1]); err != nil {
				return err
			}
			continue
		}
		switch n.Content[i].Value {
		case "provider":
			if err := n.Content[i+1].Decode(&w.Provider); err != nil {
				return err
			}
		case "api_key":
			if err := n.Content[i+1].Decode(&w.APIKey); err != nil {
				return err
			}
		case "base_url":
			if err := n.Content[i+1].Decode(&w.BaseURL); err != nil {
				return err
			}
		case "api_path":
			if err := n.Content[i+1].Decode(&w.APIPath); err != nil {
				return err
			}
		case "params":
			if err := n.Content[i+1].Decode(&w.Params); err != nil {
				return err
			}
		}
	}
	*p = LLMProviderYAML{
		Provider: w.Provider,
		Model:    w.Model.First(),
		APIKey:   w.APIKey,
		BaseURL:  w.BaseURL,
		APIPath:  w.APIPath,
		Models:   append(appcfg.StringList(nil), w.Model...),
	}
	if len(w.Params) > 0 {
		p.Params = append(appcfg.LLMRequestParams(nil), w.Params...)
	}
	return nil
}

var envPlaceholderRe = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// LoadAgentConfigsFromRoot builds one entry per agent definition, resolving each
// chain through resolvedAgentProviders so an agent that declares no providers
// of its own inherits main's — the same fallback NewLLMForAgentType applies for
// auxiliary agents. Without it, binding the runner to a primary agent that has
// no llm_providers yet fails to load rather than running on main's provider
// until the user configures one with /connect.
func LoadAgentConfigsFromRoot(cfg *appcfg.Root) (map[string]AgentConfigYAML, error) {
	if cfg == nil || len(cfg.Agents.Definitions) == 0 {
		return nil, fmt.Errorf("no agent definitions in config")
	}
	result := make(map[string]AgentConfigYAML, len(cfg.Agents.Definitions))
	for name := range cfg.Agents.Definitions {
		entry := AgentConfigYAML{}
		chain := llmYAMLsFromResolved(resolvedAgentProviders(cfg, name))
		if len(chain) > 0 {
			entry.LLMChain = chain
		}
		result[name] = entry
	}
	return result, nil
}

// modelSelector picks one entry out of an agent's configured provider chain.
// The zero value picks the chain's primary entry; naming a model picks that
// model wherever it sits in the chain; naming both halves requires the pair to
// match, which is what a user's explicit choice deserves.
type modelSelector struct {
	provider string
	model    string
}

// errModelNotConfigured is the one answer every resolution failure gives:
// the requested model is not something this session holds credentials for.
// Callers that treat an absent model as "feature disabled" rather than as an
// error say so at their own call site.
var errModelNotConfigured = errors.New("model is not configured for this agent")

// resolveAgentProvider finds the provider-chain entry a selector names.
//
// Matching by model alone also accepts a chain entry that shares the model's
// provider but names a different model — that is how a mid-session model switch
// reaches a client for a model the chain does not list verbatim; the catalog
// supplies the provider. Both halves given is an exact-pair match with no such
// widening.
func resolveAgentProvider(cfg *appcfg.Root, agentType string, sel modelSelector) (appcfg.AgentLLMProviderConfig, error) {
	if cfg == nil {
		return appcfg.AgentLLMProviderConfig{}, errModelNotConfigured
	}
	providers := resolvedAgentProviders(cfg, agentType)
	if len(providers) == 0 {
		return appcfg.AgentLLMProviderConfig{}, errModelNotConfigured
	}
	wantProvider := strings.TrimSpace(sel.provider)
	wantModel := strings.TrimSpace(sel.model)
	if wantModel == "" {
		if wantProvider != "" {
			return appcfg.AgentLLMProviderConfig{}, errModelNotConfigured
		}
		return providers[0], nil
	}
	for _, provider := range providers {
		if !strings.EqualFold(strings.TrimSpace(provider.Model), wantModel) {
			continue
		}
		if wantProvider != "" && !strings.EqualFold(strings.TrimSpace(provider.Provider), wantProvider) {
			continue
		}
		return provider, nil
	}
	if wantProvider != "" {
		return appcfg.AgentLLMProviderConfig{}, errModelNotConfigured
	}
	providerName := ProviderForAgentModel(cfg, agentType, wantModel)
	if providerName == "" {
		return appcfg.AgentLLMProviderConfig{}, errModelNotConfigured
	}
	for _, provider := range providers {
		if !strings.EqualFold(strings.TrimSpace(provider.Provider), providerName) {
			continue
		}
		provider.Model = wantModel
		return provider, nil
	}
	return appcfg.AgentLLMProviderConfig{}, errModelNotConfigured
}

// resolveAgentLLM builds the client for the entry a selector names.
func resolveAgentLLM(cfg *appcfg.Root, agentType string, sel modelSelector) (appcfg.AgentLLMProviderConfig, llm.LLM, error) {
	provider, err := resolveAgentProvider(cfg, agentType, sel)
	if err != nil {
		return appcfg.AgentLLMProviderConfig{}, nil, err
	}
	chain := llmYAMLsFromResolved([]appcfg.AgentLLMProviderConfig{provider})
	if len(chain) == 0 || chain[0] == nil {
		return provider, nil, errModelNotConfigured
	}
	client, err := NewLLMFromYAML(chain[0])
	if err != nil {
		return provider, nil, err
	}
	return provider, client, nil
}

// NewLLMForAgentType builds the client for an agent's primary model, resolving
// agents.definitions[agentType] and falling back to the main agent's providers.
//
// It returns (nil, nil) when nothing resolves: its callers are auxiliary model
// users (the goal evaluator, the guardian reviewer, context compaction) that
// treat an unconfigured model as "this feature is off" rather than as a failure.
func NewLLMForAgentType(cfg *appcfg.Root, agentType string) (llm.LLM, error) {
	_, client, err := resolveAgentLLM(cfg, agentType, modelSelector{})
	if errors.Is(err, errModelNotConfigured) {
		return nil, nil
	}
	return client, err
}

// NewLLMForAgentModel builds the configured client for a specific model in an
// agent's provider chain, and reports which provider serves it. Used when a
// model switch requires pre-turn compaction with the previous model, which is
// likewise skipped rather than failed when the model is gone: ("", nil, nil).
func NewLLMForAgentModel(cfg *appcfg.Root, agentType, model string) (string, llm.LLM, error) {
	provider, client, err := resolveAgentLLM(cfg, agentType, modelSelector{model: model})
	if errors.Is(err, errModelNotConfigured) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	return strings.TrimSpace(provider.Provider), client, nil
}

// ProviderForAgentModel names the provider that serves a model for this agent:
// the chain entry that lists it, or — when the chain does not list it verbatim —
// the catalog's unambiguous provider for that model id, provided the agent has
// that provider configured. Empty when neither answers.
func ProviderForAgentModel(cfg *appcfg.Root, agentType, model string) string {
	model = strings.TrimSpace(model)
	if cfg == nil || model == "" {
		return ""
	}
	for _, provider := range resolvedAgentProviders(cfg, agentType) {
		if strings.EqualFold(strings.TrimSpace(provider.Model), model) {
			return strings.TrimSpace(provider.Provider)
		}
	}
	matchedProvider := ""
	for _, candidate := range llm.AllModels() {
		if !strings.EqualFold(strings.TrimSpace(candidate.APIModel), model) {
			continue
		}
		if matchedProvider != "" && !strings.EqualFold(matchedProvider, candidate.Provider) {
			return ""
		}
		matchedProvider = strings.TrimSpace(candidate.Provider)
	}
	if matchedProvider == "" {
		return ""
	}
	for _, provider := range resolvedAgentProviders(cfg, agentType) {
		if strings.EqualFold(strings.TrimSpace(provider.Provider), matchedProvider) {
			return matchedProvider
		}
	}
	return ""
}

// resolvedAgentProviders returns the resolved provider chain for the named agent
// type, falling back to the main agent's providers when the definition is
// absent or has no providers.
func resolvedAgentProviders(cfg *appcfg.Root, agentType string) []appcfg.AgentLLMProviderConfig {
	if def, ok := cfg.Agents.Definitions[agentType]; ok {
		if p := appcfg.ResolvedLLMConfigs(def); len(p) > 0 {
			return p
		}
	}
	if def, ok := cfg.Agents.Definitions["main"]; ok {
		return appcfg.ResolvedLLMConfigs(def)
	}
	return nil
}

func llmYAMLsFromResolved(cfgs []appcfg.AgentLLMProviderConfig) []*LLMProviderYAML {
	if len(cfgs) == 0 {
		return nil
	}
	out := make([]*LLMProviderYAML, len(cfgs))
	for i := range cfgs {
		c := cfgs[i]
		out[i] = &LLMProviderYAML{
			Provider: c.Provider,
			Model:    c.Model,
			APIKey:   c.APIKey,
			BaseURL:  c.BaseURL,
			APIPath:  c.APIPath,
			Params:   append(appcfg.LLMRequestParams(nil), c.Params...),
		}
	}
	return out
}

func prepareAgentLLMChain(y *AgentConfigYAML) error {
	if len(y.LLMProviders) == 0 {
		return fmt.Errorf("missing llm_providers")
	}
	chain := make([]*LLMProviderYAML, 0, len(y.LLMProviders))
	for i := range y.LLMProviders {
		for _, p := range expandLLMProviderYAML(y.LLMProviders[i]) {
			cp := p
			chain = append(chain, &cp)
		}
	}
	y.LLMChain = chain
	return nil
}

func expandLLMProviderYAML(p LLMProviderYAML) []LLMProviderYAML {
	models := append(appcfg.StringList(nil), p.Models...)
	if len(models) == 0 && strings.TrimSpace(p.Model) != "" {
		models = appcfg.StringList{strings.TrimSpace(p.Model)}
	}
	if len(models) == 0 {
		return []LLMProviderYAML{{
			Provider: p.Provider,
			APIKey:   p.APIKey,
			BaseURL:  p.BaseURL,
			APIPath:  p.APIPath,
			Params:   append(appcfg.LLMRequestParams(nil), p.Params...),
		}}
	}
	out := make([]LLMProviderYAML, 0, len(models))
	for _, model := range models {
		out = append(out, LLMProviderYAML{
			Provider: p.Provider,
			Model:    strings.TrimSpace(model),
			APIKey:   p.APIKey,
			BaseURL:  p.BaseURL,
			APIPath:  p.APIPath,
			Params:   append(appcfg.LLMRequestParams(nil), p.Params...),
		})
	}
	return out
}

func NewLLMFromYAML(cfg *LLMProviderYAML) (llm.LLM, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil llm provider")
	}
	prov := strings.ToLower(strings.TrimSpace(cfg.Provider))
	model := strings.TrimSpace(cfg.Model)
	apiKey := resolveConfigValue(cfg.APIKey)
	baseURL := resolveConfigValue(cfg.BaseURL)
	if apiKey == "" {
		apiKey = appcfg.ProviderAPIKeyLookup(os.Getenv, prov)
	}
	if baseURL == "" {
		baseURL = appcfg.ProviderBaseURLLookup(os.Getenv, prov)
	}
	if prov == "" {
		return nil, fmt.Errorf("llm provider is required")
	}
	if model == "" {
		return nil, fmt.Errorf("llm model is required for provider %s", prov)
	}
	if prov == "chatgpt" {
		home, err := home.Root()
		if err != nil {
			return nil, err
		}
		// credentials.chatgpt in forebrain.yaml wins; otherwise <FOREBRAIN_HOME>/auth.json.
		authPath := openai.CredentialsPath(home)
		if _, err := openai.Load(authPath); err != nil {
			return nil, fmt.Errorf("ChatGPT login required; put a Codex auth.json at %s (or set credentials.chatgpt in forebrain.yaml), or run /connect: %w", authPath, err)
		}
		maxTokens := openai.DefaultMaxCompletionTokens
		if hit, ok := llm.Lookup("openai", model); ok && hit.DefaultMaxTokens > 0 {
			maxTokens = int(hit.DefaultMaxTokens)
		}
		transport := &openai.Transport{Path: authPath}
		return wrapUsageAccountingLLM(openai.NewResponsesLLMWithWebSearch(
			"oauth", openai.CodexBaseURL, model, maxTokens, cfg.Params.Bytes(), "live", transport,
		)), nil
	}
	if apiKey == "" {
		return nil, fmt.Errorf("llm api_key is required for provider %s", prov)
	}
	if baseURL == "" {
		return nil, fmt.Errorf("llm base_url is required for provider %s", prov)
	}
	maxTokens := openai.DefaultMaxCompletionTokens
	if hit, ok := llm.Lookup(prov, model); ok && hit.DefaultMaxTokens > 0 {
		maxTokens = int(hit.DefaultMaxTokens)
	}
	switch prov {
	case "anthropic", "minimax":
		return wrapUsageAccountingLLM(anthropic.NewAgentLLM(apiKey, baseURL, model, int64(maxTokens), requestReasoningEffort(cfg.Params))), nil
	default:
		if !llm.KnownProvider(prov) {
			return nil, fmt.Errorf("unsupported llm provider: %s (not found in models.json)", prov)
		}
		apiPath := strings.TrimSpace(cfg.APIPath)
		if strings.Contains(apiPath, "/responses") {
			return wrapUsageAccountingLLM(openai.NewResponsesLLMWithCache(apiKey, baseURL, model, maxTokens, cfg.Params.Bytes(), openAIPromptCaching(prov))), nil
		}
		return wrapUsageAccountingLLM(openai.NewCompatLLMWithPromptCaching(
			apiKey,
			baseURL,
			model,
			0,
			maxTokens,
			cfg.APIPath,
			cfg.Params.Bytes(),
			// Same gate as the /responses path above. Spelling it out here
			// again would leave this call site outside the reach of
			// TestOpenAIPromptCachingOnlyTargetsOpenAI, which is the test that
			// keeps prompt_cache_key from being sent to third-party endpoints
			// that never asked for it.
			openAIPromptCaching(prov),
		)), nil
	}
}

func openAIPromptCaching(provider string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), "openai")
}

func requestReasoningEffort(params appcfg.LLMRequestParams) string {
	if len(params) == 0 {
		return ""
	}
	var values map[string]any
	if json.Unmarshal(params.Bytes(), &values) != nil {
		return ""
	}
	reasoning, _ := values["reasoning"].(map[string]any)
	effort, _ := reasoning["effort"].(string)
	return strings.TrimSpace(effort)
}

func resolveConfigValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if m := envPlaceholderRe.FindStringSubmatch(v); len(m) == 2 {
		return strings.TrimSpace(os.Getenv(m[1]))
	}
	return v
}

const planModeFullReminderEveryNTurns = 5

const planModeTurnsBetweenReminders = 5

// reminderRunCacheCap bounds a reminder anchor's per-run cache to prevent
// unbounded growth across long-lived agent sessions.
const reminderRunCacheCap = 512

// reminderRunAnchor pins each run's injected reminder to the exact history
// boundary where it first entered the model's context.
//
// Within one tool-orchestrated run the wrapper's Execute is invoked once per
// LLM iteration. The reminder must sit at the same index in every request, and
// the history before that index must be byte-identical, so the provider keeps
// its cached prefix and only the appended tail is re-sent. The anchor caches
// the reminder text first sent in a run together with the length and hash of
// the history it was appended to; later iterations re-insert at that boundary
// instead of re-deriving a position. Plan mode and the skill-offer gate share
// the type because they need the identical guarantee for different reminders.
type reminderRunAnchor struct {
	mu       sync.Mutex
	runCache map[string]reminderAnchorEntry
	order    []string
}

// reminderAnchorEntry is one run's pinned reminder and its history boundary.
type reminderAnchorEntry struct {
	reminder   string
	anchorLen  int
	anchorHash [sha256.Size]byte
	anchored   bool
}

func newReminderRunAnchor() *reminderRunAnchor {
	return &reminderRunAnchor{runCache: make(map[string]reminderAnchorEntry)}
}

// anchor returns the reminder text first sent in this run and the boundary
// where it was first appended. Together they guarantee that every later tool
// iteration sends the previous request history as an exact prefix and only
// appends new assistant/tool/user messages. Rebuilding the text is not safe:
// dynamic details such as planExists can change after a write tool executes.
func (a *reminderRunAnchor) anchor(runID string, msgs []llm.Message, candidate string) (string, int) {
	if runID == "" {
		return candidate, len(msgs)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if entry, ok := a.runCache[runID]; ok && entry.anchored {
		if entry.anchorLen <= len(msgs) && planMessagePrefixHash(msgs[:entry.anchorLen]) == entry.anchorHash {
			return entry.reminder, entry.anchorLen
		}
		// The prefix can only legitimately disappear when the live context was
		// replaced (for example by an explicit compaction checkpoint). Caching
		// across that replacement is impossible, so establish a new append-only
		// boundary for the replacement history.
		entry.reminder = candidate
		entry.anchorLen = len(msgs)
		entry.anchorHash = planMessagePrefixHash(msgs)
		a.runCache[runID] = entry
		return entry.reminder, entry.anchorLen
	}

	entry := a.runCache[runID]
	entry.reminder = candidate
	entry.anchorLen = len(msgs)
	entry.anchorHash = planMessagePrefixHash(msgs)
	entry.anchored = true
	a.runCache[runID] = entry
	a.order = append(a.order, runID)
	if len(a.order) > reminderRunCacheCap {
		oldest := a.order[0]
		a.order = a.order[1:]
		delete(a.runCache, oldest)
	}
	return entry.reminder, entry.anchorLen
}

type planModeLLM struct {
	inner llm.LLM
	// stateRoot is the per-agent state root (a workspace root) onto which the
	// mode/plan stores join "state". It must match the root every other
	// per-agent seam resolves for this agent, or the plan path the model is
	// told to edit won't match the path GuardWrite allows.
	stateRoot  string
	projectKey string
	// anchor keeps the injected reminder at an immutable history boundary for
	// the life of a run. The kind is derived from transcript-visible msgs; the
	// completed text is cached because dynamic details such as planExists can
	// change after a write tool executes.
	anchor *reminderRunAnchor
}

func wrapPlanModeLLM(inner llm.LLM, stateRoot string, projectKeys ...string) llm.LLM {
	if inner == nil {
		return nil
	}
	projectKey := ""
	if len(projectKeys) > 0 {
		projectKey = strings.TrimSpace(projectKeys[0])
	}
	return &planModeLLM{
		inner:      inner,
		stateRoot:  strings.TrimSpace(stateRoot),
		projectKey: strings.TrimSpace(projectKey),
		anchor:     newReminderRunAnchor(),
	}
}

func (w *planModeLLM) Execute(ctx context.Context, msgs []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if w.stateRoot == "" {
		return w.inner.Execute(ctx, msgs, tools)
	}
	sid := llm.AgentSessionIDFromContext(ctx)
	if sid == "" {
		return w.inner.Execute(ctx, msgs, tools)
	}
	st, err := state.Get(w.stateRoot, sid)
	if err != nil {
		return w.inner.Execute(ctx, msgs, tools)
	}
	projectKey := w.projectKey
	if ctxProjectKey := tool.ProjectKeyFromContext(ctx); ctxProjectKey != "" {
		projectKey = ctxProjectKey
	}
	if st.Mode != state.ModePlan {
		return w.executeImplementationPhase(ctx, msgs, tools, projectKey, st)
	}
	// Plans belong to the conversation (PlanSessionIDFromContext falls back to
	// sid when no explicit conversation id is set), never to the agent session
	// of a subagent: a worker inheriting plan mode writes into its parent
	// conversation's directory so the parent's exit_plan_mode sees the plan.
	planSID := tool.PlanSessionIDFromContext(ctx)
	planDir := state.PlanDirForSession(w.stateRoot, projectKey, planSID)
	planFile := state.PlanPathForSession(w.stateRoot, projectKey, planSID)
	planExists := false
	if content, err := state.GetPlanForSession(w.stateRoot, projectKey, planSID); err == nil && strings.TrimSpace(content) != "" {
		planExists = true
	}
	// Stateless throttle + kind, derived from the transcript-visible msgs.
	// PlanTurnCount is deprecated for this decision: counting IsMeta
	// plan-reminders and human turns directly from the message slice avoids
	// drift after compaction and makes the accounting resume-safe (directions
	// 1 + 2). The persisted reminders are read back from the transcript by
	// transcriptSession.build, so they appear here as IsMeta user messages.
	reminderCount, turnsSinceReminder, exitExists := analyzePlanReminders(msgs)
	inject := reminderCount == 0 || turnsSinceReminder >= planModeTurnsBetweenReminders
	ctx = tool.WithAllowedPlanPath(ctx, planDir)
	if !inject {
		// Throttled: a recent persisted reminder (read back from the
		// transcript) already provides plan context. AllowedPlanPath is still
		// wired so GuardWrite keeps enforcing the plan-only write policy.
		return w.inner.Execute(ctx, msgs, tools)
	}
	kind := planModeKindForCount(reminderCount)
	isReentry := exitExists && reminderCount == 0
	// Any child gets the subagent variant: a typed worker that inherits plan
	// mode (one that can write files, e.g. general-purpose) is no more able to
	// end the parent's planning phase than a fork child is, so it must not be
	// told to call exit_plan_mode — doing so would only flip its own worker
	// state.
	isSubagent := tool.IsForkChildFromContext(ctx) || strings.TrimSpace(tool.SubagentTypeFromContext(ctx)) != ""
	reminder := buildPlanModeReminder(planDir, planFile, planExists, kind, isReentry, isSubagent)
	reminder, insertAt := w.stableReminderForRun(ctx, msgs, reminder)
	msgs = injectAndRecordPlanReminder(ctx, msgs, reminder, insertAt)
	return w.inner.Execute(ctx, msgs, tools)
}

// injectAndRecordPlanReminder puts the reminder into the request and publishes
// it to the enclosing orchestration loop, which adopts it into the live session
// so it is persisted in the position the model saw it.
func injectAndRecordPlanReminder(ctx context.Context, msgs []llm.Message, reminder string, insertAt int) []llm.Message {
	if strings.TrimSpace(reminder) == "" {
		return msgs
	}
	insertAt = clampPlanReminderIndex(msgs, insertAt)
	reminderMessage := planReminderMessage(reminder)
	// The anchor may already hold this exact reminder, because the loop adopted
	// it into the live session on an earlier iteration while the throttle's
	// count restarted behind a fresh exit_plan_mode boundary. An anchor that is
	// already occupied by this reminder is this reminder: inserting a second
	// copy would send the model the same paragraph twice and break the
	// append-only prefix the anchor exists to preserve.
	if insertAt < len(msgs) && planReminderMessagesEqual(msgs[insertAt], reminderMessage) {
		return msgs
	}
	recordReminderAdoption(ctx, reminderMessage, insertAt)
	return insertPlanReminderMessage(msgs, reminderMessage, insertAt)
}

func planReminderMessagesEqual(a, b llm.Message) bool {
	if a.Role != b.Role || a.IsMeta != b.IsMeta {
		return false
	}
	return llm.TextContent(a.Parts...) == llm.TextContent(b.Parts...)
}

// stableReminderForRun returns both the exact reminder text first sent in this
// run and the boundary where it was first appended. See reminderRunAnchor for
// why the pair guarantees an append-only history within a run.
func (w *planModeLLM) stableReminderForRun(ctx context.Context, msgs []llm.Message, candidate string) (string, int) {
	return w.anchor.anchor(strings.TrimSpace(tool.RunIDFromContext(ctx)), msgs, candidate)
}

func planMessagePrefixHash(msgs []llm.Message) [sha256.Size]byte {
	// Hash the complete serializable representation rather than a text-only
	// projection. If any provider-relevant message field before the reminder
	// changes, it must not be mistaken for the append-only history whose cache
	// entry we are preserving.
	raw, err := json.Marshal(msgs)
	if err != nil {
		return sha256.Sum256(nil)
	}
	return sha256.Sum256(raw)
}

// executeImplementationPhase handles the half of the plan loop that follows
// exit_plan_mode: the session is back in a working mode and is carrying out an
// approved plan. Without this the loop was open-ended - the agent implemented
// the plan and never returned to the file, so the plan on disk stayed a
// statement of intent with no record of what actually landed.
//
// The phase is derived, not stored: the session has exited plan mode, a plan
// file exists, and that file does not yet carry state.ImplementationHeading.
// Writing the implementation record is therefore what ends the reminders, which
// is precisely the loop closing.
func (w *planModeLLM) executeImplementationPhase(ctx context.Context, msgs []llm.Message, tools []*llm.Tool, projectKey string, st state.State) (*llm.Result, error) {
	planSID := tool.PlanSessionIDFromContext(ctx)
	planDir := state.PlanDirForSession(w.stateRoot, projectKey, planSID)
	// The plan directory stays writable outside plan mode: it is the only path
	// under the agent state root the implementation record may be written to.
	ctx = tool.WithAllowedPlanPath(ctx, planDir)
	if !st.HasExitedPlan {
		return w.inner.Execute(ctx, msgs, tools)
	}
	// A subagent must not close the parent's plan loop: it does not own the
	// plan, and its own session mode is a copy of the parent's.
	if tool.IsForkChildFromContext(ctx) || strings.TrimSpace(tool.SubagentTypeFromContext(ctx)) != "" {
		return w.inner.Execute(ctx, msgs, tools)
	}
	content, err := state.GetPlanForSession(w.stateRoot, projectKey, planSID)
	if err != nil || strings.TrimSpace(content) == "" || state.HasImplementationRecord(content) {
		return w.inner.Execute(ctx, msgs, tools)
	}
	reminderCount, turnsSinceReminder := analyzeImplementationReminders(msgs)
	if !(reminderCount == 0 || turnsSinceReminder >= planModeTurnsBetweenReminders) {
		return w.inner.Execute(ctx, msgs, tools)
	}
	planFile := state.PlanPathForSession(w.stateRoot, projectKey, planSID)
	reminder := buildPlanImplementationReminder(planFile, planModeKindForCount(reminderCount))
	reminder, insertAt := w.stableReminderForRun(ctx, msgs, reminder)
	msgs = injectAndRecordPlanReminder(ctx, msgs, reminder, insertAt)
	return w.inner.Execute(ctx, msgs, tools)
}

// planImplementationReminderMarker is the phrase every implementation reminder
// carries, so persisted reminders read back from the transcript can be counted
// the same stateless way plan-mode reminders are.
const planImplementationReminderMarker = "Plan follow-through required"

// analyzeImplementationReminders counts the implementation reminders already
// visible in the current implementation session - the messages after the last
// exit_plan_mode tool call - and the human turns since the most recent one.
func analyzeImplementationReminders(msgs []llm.Message) (reminderCount int, turnsSinceReminder int) {
	boundary := 0
	for i, m := range msgs {
		if m.Role != llm.RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			if strings.TrimSpace(tc.Function.Name) == "exit_plan_mode" {
				boundary = i + 1
			}
		}
	}
	lastReminderIdx := -1
	for i := boundary; i < len(msgs); i++ {
		if isImplementationReminderMessage(msgs[i]) {
			reminderCount++
			lastReminderIdx = i
		}
	}
	if lastReminderIdx < 0 {
		return reminderCount, 0
	}
	for i := lastReminderIdx + 1; i < len(msgs); i++ {
		if msgs[i].Role == llm.RoleUser && !msgs[i].IsMeta {
			turnsSinceReminder++
		}
	}
	return reminderCount, turnsSinceReminder
}

func isImplementationReminderMessage(m llm.Message) bool {
	if m.Role != llm.RoleUser || !m.IsMeta {
		return false
	}
	text := strings.ToLower(llm.TextContent(m.Parts...))
	return strings.Contains(text, strings.ToLower(planImplementationReminderMarker))
}

func buildPlanImplementationReminder(planFile, kind string) string {
	if kind == "sparse" {
		return fmt.Sprintf(
			"%s: before you report the work finished, update the plan file %s - tick off the completed tasks and append a '%s' section describing what actually landed.",
			planImplementationReminderMarker, planFile, state.ImplementationHeading,
		)
	}
	return fmt.Sprintf(`%s - read carefully.

You are implementing an approved plan. The plan file is:

  %s

It is writable in this mode (it is the one path under the agent state root you may write outside plan mode).

## Closing the plan loop

When the plan's tasks are done - or when you stop working on the plan for any reason, including it being abandoned or superseded - you MUST update the plan file BEFORE giving your final answer:

1. Mark each task in the plan as done, partially done, or not done (e.g. tick its checkbox).
2. Append a final section with exactly this heading:

   %s

3. Under that heading, record:
   - What actually landed, and where (files, packages, commands).
   - Any deviation from the plan, and why the plan was wrong or incomplete.
   - Anything planned but not done, and why.
   - How it was verified (build, tests, manual run) and the result.

Use edit_file to append the section rather than rewriting the plan: the plan's original intent must stay readable next to what happened.

A plan is complete only once this record exists in the file. This reminder stops appearing as soon as the file contains the heading above.`,
		planImplementationReminderMarker, planFile, state.ImplementationHeading,
	)
}

// analyzePlanReminders inspects the LLM-bound message slice to derive the
// stateless plan-reminder accounting used by the throttle (direction 1) and
// the kind selection (direction 2). It is the replacement for the persisted
// PlanTurnCount, which could drift after compaction.
//
// The current "plan session" is scoped to the messages after the LAST
// exit_plan_mode tool call (an assistant message carrying a tool_call with
// Function.Name == "exit_plan_mode"). If no such call exists, the whole slice
// is the current state. This makes exit_plan_mode a counting boundary so
// that re-entering plan mode restarts the reminder cadence from zero.
//
// It returns:
//   - reminderCount: number of IsMeta plan-reminder user messages in the
//     current plan session (i.e. before the about-to-be-injected reminder).
//   - turnsSinceReminder: number of human turns (user, non-meta) after the
//     last plan-reminder in the current state. It is 0 when no reminder
//     exists in the session; the caller treats reminderCount==0 as "always
//     inject" regardless.
//   - exitExists: whether an exit_plan_mode tool call is present anywhere in
//     msgs, used to detect re-entry into plan mode.
func analyzePlanReminders(msgs []llm.Message) (reminderCount int, turnsSinceReminder int, exitExists bool) {
	// Boundary: index after the last exit_plan_mode assistant tool call.
	boundary := 0
	for i, m := range msgs {
		if m.Role != llm.RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			if strings.TrimSpace(tc.Function.Name) == "exit_plan_mode" {
				boundary = i + 1
				exitExists = true
			}
		}
	}
	lastReminderIdx := -1
	for i := boundary; i < len(msgs); i++ {
		if isPlanReminderMessage(msgs[i]) {
			reminderCount++
			lastReminderIdx = i
		}
	}
	if lastReminderIdx < 0 {
		return reminderCount, 0, exitExists
	}
	for i := lastReminderIdx + 1; i < len(msgs); i++ {
		if msgs[i].Role == llm.RoleUser && !msgs[i].IsMeta {
			turnsSinceReminder++
		}
	}
	return reminderCount, turnsSinceReminder, exitExists
}

// isPlanReminderMessage reports whether m is a persisted plan-mode reminder
// (an IsMeta user message whose text carries the plan-mode system-reminder).
func isPlanReminderMessage(m llm.Message) bool {
	if m.Role != llm.RoleUser || !m.IsMeta {
		return false
	}
	text := strings.ToLower(llm.TextContent(m.Parts...))
	return strings.Contains(text, "plan mode active")
}

// planModeKindForCount maps the number of already-injected reminders in the
// current plan session to the kind for the next injection. The 1st, 6th,
// 11th, ... reminder is "full".
func planModeKindForCount(count int) string {
	if count%planModeFullReminderEveryNTurns == 0 {
		return "full"
	}
	return "sparse"
}

func buildPlanModeReminder(planDir, planFile string, planExists bool, kind string, isReentry bool, isSubagent bool) string {
	// Fork-subagent variant: simplified reminder for child agents that inherit
	// plan mode from their parent. The subagent is confined to writing plan
	// files only and should use AskUserQuestion for clarification instead of
	// calling exit_plan_mode (which would only affect the child session).
	if isSubagent {
		if kind == "sparse" {
			return fmt.Sprintf(
				"Plan mode active (subagent). Write/edit plan files only inside %s - all other writes blocked. Use AskUserQuestion to clarify the approach.",
				planDir,
			)
		}
		return fmt.Sprintf(`PLAN MODE ACTIVE (subagent) - read carefully.

You are a subagent operating in plan mode. You MUST NOT make any edits to the codebase except to plan files inside this dedicated plan directory:

  %s

Files directly in this directory are the ONLY files you are allowed to edit. All write_file/edit_file calls to paths outside this directory are blocked.

## Tool guidance in subagent plan mode

- read_file, code_search, references, definitions, list_directory, and shell (under the subagent's sandbox profile): free use
- user_interaction (AskUserQuestion): free use to clarify the approach
- write_file, edit_file: ALLOWED only for files inside the plan directory above
- read-only shell commands: free use, no prompt
- shell commands that modify files, and write_file/edit_file to other paths: BLOCKED
- shell commands whose effect cannot be proven read-only: you will be asked to approve them before they run
- exit_plan_mode: NOT available (only the parent agent can exit plan mode)
- subagent_run, subagent_fanout: NOT available within a subagent

## Important

- Your work is scoped to the plan directory. You are exploring, researching, and contributing to the planning process.
- When done, summarize your findings. The parent agent will decide how to proceed.`,
			planDir,
		)
	}
	if kind == "sparse" {
		if planExists {
			return fmt.Sprintf(
				"Plan mode active. Write/edit plan files only inside %s (current: %s) - all other writes blocked. Call exit_plan_mode when plan ready.",
				planDir, planFile,
			)
		}
		return fmt.Sprintf(
			"Plan mode active. Write/edit plan files only inside %s - all other writes blocked. No plan file yet: create a NEW <descriptive-name>.md file (do NOT use plan.md). Call exit_plan_mode when plan ready.",
			planDir,
		)
	}
	exists := "(no plan file exists yet - create a NEW <descriptive-name>.md file, e.g. 'add-auth-validation.md'. Do NOT name it plan.md)"
	if planExists {
		exists = fmt.Sprintf("(current plan file %s already has content - edit_file to refine it, or create a NEW .md file to start a fresh plan)", planFile)
	}
	reentrySection := ""
	if isReentry && planExists {
		reentrySection = `
## Re-entering Plan Mode

You are returning to plan mode after having previously exited it. A plan file exists from your previous planning state.

**Before proceeding with any new planning, you should:**
1. Read the existing plan file to understand what was previously planned
2. Evaluate the user's current request against that plan
3. Decide how to proceed:
   - **Different task**: If the user's request is for a different task, start fresh by creating a NEW plan file
   - **Same task, continuing**: If this is a continuation or refinement, modify the existing plan while cleaning up outdated sections
4. Always edit/create the plan file before calling exit_plan_mode

`
	}
	return fmt.Sprintf(`PLAN MODE ACTIVE - read carefully.

You are in plan mode. You MUST NOT make any edits to the codebase except to plan files inside this dedicated plan directory:

  %s
%s
%sFiles directly in this directory are the ONLY files you are allowed to edit. Author them with the normal file tools - write_file (create/replace) or edit_file (targeted change). All write_file/edit_file calls to paths outside this directory are blocked by GuardWrite.

## Plan files preserve history

- Each plan is its own file with a human-readable, semantic name you choose based on the task (e.g. "add-s3-skill-sync.md", "fix-memory-leak.md", "refactor-auth-module.md").
- You MUST choose a descriptive name that reflects the task. Do NOT use the generic name "plan.md" - it causes every new plan to overwrite the previous one and lose history.
- When you start a substantially new or revised plan, CREATE A NEW <descriptive-name>.md file in the directory above. Do NOT overwrite an existing plan - historical plans are preserved.
- To refine the current plan, edit_file it in place instead.
- The most-recently-modified .md file is treated as the "current" plan - it is the one surfaced for approval, /plan show, and status. Producing a new plan makes it current; the previous one stays on disk.

## Plan-mode workflow (Phase 1-5)

Phase 1 - Initial Understanding: explore the codebase. Use read_file for file contents, code_search, shell, and subagent_run subtype=explore (or subagent_fanout subtype=explore for parallel work). Do not write code.

Phase 2 - Design: form an architectural approach. Identify files that will change, data flow, edge cases, error handling, test strategy.

Phase 3 - Review: pressure-test the plan. Identify failure modes, missing tests, and unresolved decisions.

Phase 4 - Final Plan: write the plan to a new (or the current) plan file. Use clear sections (Context, Approach, Files to change, Verification, Tasks).

Phase 5 - Exit: call exit_plan_mode when the plan is complete and ready for user approval.

## Tool guidance in plan mode

- read_file, code_search, references, definitions, list_directory, and shell (under the subagent's sandbox profile): free use
- subagent_run subtype=explore, subagent_fanout subtype=explore: free use for research
- user_interaction (canonical AskUserQuestion): free use to clarify approach
- write_file, edit_file: ALLOWED only for files directly inside the plan directory above; all other paths blocked
- read-only shell commands: free use, no prompt
- shell commands that modify files: BLOCKED until exit_plan_mode
- shell commands whose effect cannot be proven read-only: you will be asked to approve them; proceed if approved

## Important

- MUST NOT make codebase edits outside the plan directory. This is a hard guarantee.
- Do not use user_interaction to ask "is this plan okay?" - that's exit_plan_mode's job.
- When ready, call exit_plan_mode to request user approval.`,
		planDir, exists, reentrySection,
	)
}

// reminderAdoptionSink is a context-scoped channel that lets the reminder
// wrappers (plan mode, plan implementation, skill offer) hand the reminders
// they injected into a request back to the enclosing tool-orchestration loop,
// so the live session adopts them.
//
// The reminder must enter the run's own message sequence rather than being
// written to the transcript on its own, because the transcript has exactly one
// other writer -- the run's persistence path -- and that writer runs later. A
// run parked at an approval gate has already stored its assistant tool_calls
// row (persistRequiresActionSnapshot) while the results answering it are still
// only in the orchestration's in-memory session; they reach the store when the
// turn finishes, is cancelled, or hits its next gate. A reminder written in the
// middle of a model call therefore landed *between* the call and its result,
// even though the model saw it *after* the result. RepairDanglingToolResults
// then read that user row as the end of an unanswered assistant batch and
// excluded both the assistant tool_calls row and the now-orphaned result -- so
// an exit_plan_mode denial lost the feedback the user had typed into it.
//
// Adopting it into the session instead keeps one writer and one ordering: the
// reminder is persisted by the same append that persists the messages it
// followed, in the position the model actually saw it.
//
// A context value is used instead of a Result field for the same reason
// compactionAdoptionSink uses one: passthrough wrappers (fork capture) sit
// between the orchestration loop and the reminder wrappers, and a context
// holder survives them all without every wrapper forwarding a new field.
type reminderAdoptionSink struct {
	mu      sync.Mutex
	pending []reminderAdoption
}

// reminderAdoption is one injected reminder awaiting adoption: the exact
// message the model was sent, and the index it was inserted at in the slice
// the recording wrapper received.
type reminderAdoption struct {
	message  llm.Message
	insertAt int
}

func (s *reminderAdoptionSink) record(message llm.Message, insertAt int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = append(s.pending, reminderAdoption{message: message, insertAt: insertAt})
}

// take drains every reminder recorded since the last take, in record order,
// clearing the sink so the next inner call starts fresh.
//
// Record order — not insert order — is the order the loop must adopt in. Each
// wrapper records the index in the coordinate space of the slice it received,
// and the wrappers run outermost first: the first insertion shifts the history
// into exactly the coordinate space the next wrapper computed its own index
// against. Adopting in record order therefore replays each insertion in the
// space its index was computed in. (Indices from different wrappers are not in
// one shared space, so sorting the drain by insertAt could move a reminder
// ahead of the insertion its own index depends on.)
func (s *reminderAdoptionSink) take() []reminderAdoption {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return nil
	}
	pending := s.pending
	s.pending = nil
	return pending
}

// discard drops recorded reminders without adopting them. A mid-turn
// compaction replaces the live history with the messages the inner call
// actually sent, which already carry the reminders; adopting them a second
// time would duplicate them inside the replacement.
func (s *reminderAdoptionSink) discard() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = nil
}

type reminderAdoptionSinkKey struct{}

func withReminderAdoptionSink(ctx context.Context, sink *reminderAdoptionSink) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, reminderAdoptionSinkKey{}, sink)
}

func reminderAdoptionSinkFromContext(ctx context.Context) *reminderAdoptionSink {
	if ctx == nil {
		return nil
	}
	sink, _ := ctx.Value(reminderAdoptionSinkKey{}).(*reminderAdoptionSink)
	return sink
}

// recordReminderAdoption publishes an injected reminder to the enclosing
// orchestration loop, if one installed a sink. No-op otherwise (e.g. direct LLM
// callers that keep no long-lived session).
func recordReminderAdoption(ctx context.Context, message llm.Message, insertAt int) {
	reminderAdoptionSinkFromContext(ctx).record(message, insertAt)
}

// planReminderMessage renders one reminder as the IsMeta user message the model
// sees. The orchestration loop adopts this exact message into its live session,
// so it is built once and shared rather than rendered twice.
func planReminderMessage(reminder string) llm.Message {
	wrapped := "<system-reminder>\n" + reminder + "\n</system-reminder>"
	return llm.Message{
		Role:   llm.RoleUser,
		Parts:  []llm.ContentPart{llm.Text(wrapped)},
		IsMeta: true,
	}
}

// lspDiagnosticsReminderLLM appends the language-server diagnostics that
// arrived after an edit's wait window (spec §8.4). The text is appended at
// the end of the request, so the cached prefix is untouched; it is delivered
// once, acknowledged only after the model call succeeds.
type lspDiagnosticsReminderLLM struct {
	inner llm.LLM
	ci    tool.CodeIntelligence
}

func wrapLSPDiagnosticsReminderLLM(inner llm.LLM, ci tool.CodeIntelligence) llm.LLM {
	if inner == nil || ci == nil {
		return inner
	}
	return lspDiagnosticsReminderLLM{inner: inner, ci: ci}
}

func (w lspDiagnosticsReminderLLM) Execute(ctx context.Context, msgs []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	sid := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
	if sid == "" {
		return w.inner.Execute(ctx, msgs, tools)
	}
	text, token := w.ci.PeekLate(sid)
	if text == "" {
		return w.inner.Execute(ctx, msgs, tools)
	}
	message := planReminderMessage(text)
	recordReminderAdoption(ctx, message, len(msgs))
	res, err := w.inner.Execute(ctx, append(append([]llm.Message(nil), msgs...), message), tools)
	if err == nil {
		w.ci.AckLate(sid, token)
	}
	return res, err
}

// clampPlanReminderIndex resolves where a reminder goes in msgs. An
// out-of-range anchor means the history it was anchored to is gone, so the
// reminder appends at the end.
func clampPlanReminderIndex(msgs []llm.Message, insertAt int) int {
	if insertAt < 0 || insertAt > len(msgs) {
		return len(msgs)
	}
	return insertAt
}

func insertPlanReminderMessage(msgs []llm.Message, reminderMessage llm.Message, insertAt int) []llm.Message {
	insertAt = clampPlanReminderIndex(msgs, insertAt)

	// Keep the reminder at the run-scoped point where it first became visible
	// to the model, rather than moving it to the end after every tool iteration.
	// The tool orchestrator owns its live session and calls this wrapper repeatedly:
	//
	//   request N:     ... enter_plan_mode result, reminder
	//   request N + 1: ... enter_plan_mode result, assistant tool call, result
	//
	// Appending again in request N+1 rewrites that history as
	// "... result, tool call, result, reminder". Besides destroying the stable
	// prompt prefix (and therefore cache hits), Responses-compatible providers
	// may treat the trailing user item as a new turn and reject the preceding
	// function call with "No tool output found" even though its output was
	// serialized immediately before the reminder.
	//
	// Later assistant/tool/user messages stay after this stable anchor, making
	// the complete prior request history an exact prefix of the next request.
	out := make([]llm.Message, 0, len(msgs)+1)
	out = append(out, msgs[:insertAt]...)
	out = append(out, reminderMessage)
	out = append(out, msgs[insertAt:]...)
	return out
}

// runnerPrimaryState bundles the published effective primary-model snapshot
// with the runtime-health gate. Both are atomics on a value type, so a Runner
// assembled by literal construction (tests) is usable without setup.
type runnerPrimaryState struct {
	snapshot  atomic.Pointer[primaryModelRuntime]
	unhealthy atomic.Bool
}

// primaryModelRuntime is the one published effective primary-model snapshot.
// provider is a deep clone: its Params already carry the concrete pinned
// reasoning effort, and credentials inside it stay internal to the Runner.
type primaryModelRuntime struct {
	agent    string
	provider appcfg.AgentLLMProviderConfig
}

// PrimaryModelState is the exported, secret-free view of the effective
// primary model. Set is false only for a Runner that never completed a load,
// where the values fall back to the config's first resolved entry the way
// PrimaryModel does for unloaded test fixtures.
type PrimaryModelState struct {
	Provider string
	Model    string
	Effort   string
	Set      bool
}

// runtimeUncertainError reports that building the candidate runtime failed
// and rebuilding the previously published runtime failed too — or that a
// reload after an agent switch failed, where the surrounding tenant boundary
// has already moved. The Runner is marked unhealthy until one of the four
// complete build operations succeeds; a still-published snapshot is not
// proof that load-owned fields were restored.
type runtimeUncertainError struct {
	original error
	rollback error
}

func (e *runtimeUncertainError) Error() string {
	if e.rollback != nil {
		return fmt.Sprintf("runner runtime is uncertain: %v (rebuilding the previous runtime also failed: %v)", e.original, e.rollback)
	}
	return fmt.Sprintf("runner runtime is uncertain: %v", e.original)
}

func (e *runtimeUncertainError) Unwrap() error { return e.original }

// errRuntimeNeedsReload is what run entry points return while the Runner is
// unhealthy: work is refused until a complete Load, LoadConfig,
// SetPrimaryModel or ResetPrimaryModel succeeds.
var errRuntimeNeedsReload = errors.New("runner runtime needs a reload: the previous model switch or config reload could not be rolled back")

// IsPrimaryModelNotConfigured classifies the exact-pair miss an explicit user
// selection deserves. Ordinary reloads fall back instead of failing; only
// callers that asked for this exact pair should see it.
func IsPrimaryModelNotConfigured(err error) bool {
	return errors.Is(err, errModelNotConfigured)
}

// IsRunnerClosed reports whether err is the answer a shut-down Runner gives a
// load. The runner pool treats it as teardown, not a propagation failure.
func IsRunnerClosed(err error) bool {
	return errors.Is(err, errRunnerClosed)
}

// IsPrimaryModelRuntimeUncertain is true only when the candidate build failed
// and rebuilding the previously published runtime failed as well, or when an
// agent-boundary rebuild failed. Surfaces must not claim any model is healthy
// in that state.
func IsPrimaryModelRuntimeUncertain(err error) bool {
	var uncertain *runtimeUncertainError
	return errors.As(err, &uncertain)
}

// NewRuntimeUncertainError wraps cause as a runtime-uncertain failure whose
// own restore step then failed with restoreErr. Surfaces use it for their
// restoration failures, so one classifier answers for Runner and surface
// errors alike.
func NewRuntimeUncertainError(cause, restoreErr error) error {
	return &runtimeUncertainError{original: cause, rollback: restoreErr}
}

type primaryModelIntent uint8

const (
	// primaryModelOrdinary keeps the current snapshot when its agent and
	// exact provider/model still resolve, refreshing provider details and
	// overlaying the pinned effort; it falls back to the new configured
	// default when the pin disappeared.
	primaryModelOrdinary primaryModelIntent = iota
	// primaryModelExplicit requires the exact provider+model pair and never
	// falls back.
	primaryModelExplicit
	// primaryModelReset intentionally adopts the config's first resolved
	// entry.
	primaryModelReset
)

// resolvePrimaryModelCandidateLocked computes the effective provider entry
// this load should run on. Callers hold r.mu.load.
func (r *Runner) resolvePrimaryModelCandidateLocked(
	intent primaryModelIntent, provider, model string, effort *string,
) (appcfg.AgentLLMProviderConfig, error) {
	name := r.activeAgentNameForModel()
	snap := r.primaryState.snapshot.Load()
	switch intent {
	case primaryModelExplicit:
		entry, err := resolveAgentProvider(r.AppCfg, name, modelSelector{provider: provider, model: model})
		if err != nil {
			return appcfg.AgentLLMProviderConfig{}, err
		}
		if effort != nil {
			// An explicit effort, including "", is overlaid exactly.
			entry.Params = appcfg.WithReasoningEffort(entry.Params, *effort)
		}
		return entry, nil
	case primaryModelReset:
		return resolveAgentProvider(r.AppCfg, name, modelSelector{})
	default:
		if snap == nil || snap.agent != name {
			// First load, or the active agent changed: adopt the config's
			// first resolved entry and pin its current concrete effort.
			return resolveAgentProvider(r.AppCfg, name, modelSelector{})
		}
		entry, err := resolveAgentProvider(r.AppCfg, name, modelSelector{
			provider: snap.provider.Provider,
			model:    snap.provider.Model,
		})
		if err == nil {
			// Refresh every provider field from the file, then overlay the
			// pinned effort — even when it is an explicitly empty one.
			entry.Params = appcfg.WithReasoningEffort(entry.Params, appcfg.ReasoningEffort(snap.provider.Params))
			return entry, nil
		}
		if !errors.Is(err, errModelNotConfigured) {
			return appcfg.AgentLLMProviderConfig{}, err
		}
		fallback, ferr := resolveAgentProvider(r.AppCfg, name, modelSelector{})
		if ferr != nil {
			return appcfg.AgentLLMProviderConfig{}, ferr
		}
		// Only ordinary reload falls back when the pin is gone. The warning
		// carries no credentials.
		slog.Warn("pinned primary model is no longer configured; using the configured default",
			"old_provider", strings.TrimSpace(snap.provider.Provider),
			"old_model", strings.TrimSpace(snap.provider.Model),
			"new_provider", strings.TrimSpace(fallback.Provider),
			"new_model", strings.TrimSpace(fallback.Model))
		return fallback, nil
	}
}

// loadWithIntentLocked resolves the candidate for intent and rebuilds the
// Runner around it, publishing the snapshot only after the build succeeds.
// On failure it rebuilds the previously effective snapshot; a failed rebuild
// is joined into the returned error and marks the Runner unhealthy. Callers
// hold r.mu.load (it is a non-reentrant RWMutex — never call the public Load
// from here).
func (r *Runner) loadWithIntentLocked(
	intent primaryModelIntent, provider, model string, effort *string,
) error {
	// The closed gate must answer before any resolution or rollback work: a
	// shut-down Runner that first fails candidate resolution would otherwise
	// be classified runtime-uncertain (and marked unhealthy) for what is
	// really just teardown.
	if r.closed {
		return errRunnerClosed
	}
	name := r.activeAgentNameForModel()
	snap := r.primaryState.snapshot.Load()
	agentChanged := snap != nil && snap.agent != name
	if intent == primaryModelExplicit && effort != nil && snap != nil && snap.agent == name {
		// An exact repeat of the published selection (including effort) is a
		// no-op: session activation and fork materialization both reach this
		// path, and rebuilding would churn the load-owned runtime for
		// nothing. A reset or ordinary load still rebuilds.
		pinned := appcfg.ReasoningEffort(snap.provider.Params)
		if strings.EqualFold(strings.TrimSpace(snap.provider.Provider), strings.TrimSpace(provider)) &&
			strings.EqualFold(strings.TrimSpace(snap.provider.Model), strings.TrimSpace(model)) &&
			pinned == strings.ToLower(strings.TrimSpace(*effort)) {
			return nil
		}
	}
	candidate, err := r.resolvePrimaryModelCandidateLocked(intent, provider, model, effort)
	if err != nil {
		if agentChanged {
			// The surrounding tenant boundary (stores, workspace) has already
			// moved to the new agent; rebuilding only the old model snapshot
			// cannot repair that, so the Runner is fail-closed until the
			// target boundary rebuilds successfully.
			r.primaryState.unhealthy.Store(true)
			return &runtimeUncertainError{original: err}
		}
		return err
	}
	if err := r.loadLocked(&candidate); err != nil {
		if agentChanged {
			// Same rule as above: the old agent's snapshot must not be
			// rebuilt on top of the new agent's tenant stores.
			r.primaryState.unhealthy.Store(true)
			return &runtimeUncertainError{original: err}
		}
		if rollbackErr := r.rebuildPreviousRuntimeLocked(snap); rollbackErr != nil {
			r.primaryState.unhealthy.Store(true)
			return &runtimeUncertainError{original: err, rollback: rollbackErr}
		}
		return err
	}
	r.primaryState.snapshot.Store(&primaryModelRuntime{
		agent:    name,
		provider: appcfg.CloneAgentLLMProviderConfig(candidate),
	})
	r.primaryState.unhealthy.Store(false)
	return nil
}

// rebuildPreviousRuntimeLocked rebuilds the snapshot a failed switch was
// rolling back to. With no previous snapshot the config's first resolved
// entry is the previous effective runtime.
func (r *Runner) rebuildPreviousRuntimeLocked(snap *primaryModelRuntime) error {
	if snap != nil {
		return r.loadLocked(&snap.provider)
	}
	return r.loadLocked(nil)
}

// SetPrimaryModel makes the exact provider+model pair this session's
// effective model. The pair must still resolve; an explicit effort pointer,
// including "", is overlaid exactly, while nil derives the entry's configured
// effort once. A failed switch does not publish candidate identity: the
// previously effective runtime is rebuilt first.
func (r *Runner) SetPrimaryModel(provider, model string, effort *string) error {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return fmt.Errorf("select primary model: provider and model are required")
	}
	r.mu.load.Lock()
	defer r.mu.load.Unlock()
	return r.loadWithIntentLocked(primaryModelExplicit, provider, model, effort)
}

// ResetPrimaryModel intentionally adopts the config's first resolved entry as
// the effective model — the state a session with no stored choice runs on.
func (r *Runner) ResetPrimaryModel() error {
	r.mu.load.Lock()
	defer r.mu.load.Unlock()
	return r.loadWithIntentLocked(primaryModelReset, "", "", nil)
}

// LoadConfig is the one config-pointer transaction: the pointer is swapped
// only while r.mu.load is held, an ordinary pinned reload runs against the
// new config, and a failure restores the old pointer and rebuilds the old
// effective runtime before returning. Callers never write Runner.AppCfg
// first. Filesystem policy is refreshed only after unlocking, for the runtime
// that ultimately won — RefreshFilesystemPolicy takes r.mu.load.RLock itself.
func (r *Runner) LoadConfig(next *appcfg.Root) error {
	if next == nil {
		return fmt.Errorf("load config: nil root")
	}
	r.mu.load.Lock()
	defer r.mu.load.Unlock()
	prev := r.AppCfg
	r.AppCfg = next
	err := r.loadWithIntentLocked(primaryModelOrdinary, "", "", nil)
	if err == nil {
		return nil
	}
	r.AppCfg = prev
	// The authoritative rollback: rebuild the previously effective runtime
	// under the restored pointer. Only its failure leaves the runner
	// uncertain; when it succeeds, surfaces see the load error, not an
	// uncertainty their own recovery already resolved.
	if rollbackErr := r.loadWithIntentLocked(primaryModelOrdinary, "", "", nil); rollbackErr != nil {
		r.primaryState.unhealthy.Store(true)
		return &runtimeUncertainError{original: primaryModelPlainError(err), rollback: rollbackErr}
	}
	return primaryModelPlainError(err)
}

// primaryModelPlainError unwraps an inner runtime-uncertain classification
// when the caller's own rollback succeeded: the error a surface should see is
// the load failure, not a health verdict that no longer holds.
func primaryModelPlainError(err error) error {
	var uncertain *runtimeUncertainError
	if errors.As(err, &uncertain) {
		return uncertain.original
	}
	return err
}

// PrimaryModelSelection is the secret-free effective selection a surface
// draws. Loaded runners read the published snapshot; an unloaded test Runner
// falls back to the config's first resolved entry.
func PrimaryModelSelection(r *Runner) PrimaryModelState {
	if r == nil {
		return PrimaryModelState{}
	}
	if snap := r.primaryState.snapshot.Load(); snap != nil {
		return PrimaryModelState{
			Provider: strings.TrimSpace(snap.provider.Provider),
			Model:    strings.TrimSpace(snap.provider.Model),
			Effort:   appcfg.ReasoningEffort(snap.provider.Params),
			Set:      true,
		}
	}
	provider, model := PrimaryModel(r)
	state := PrimaryModelState{Provider: provider, Model: model}
	if entry, err := resolveAgentProvider(r.AppCfg, r.activeAgentNameForModel(), modelSelector{}); err == nil {
		state.Effort = appcfg.ReasoningEffort(entry.Params)
	}
	return state
}

// PrimaryReasoningEffort is the concrete reasoning effort the effective
// primary model runs at — "" is an explicitly pinned no-effort value on a
// loaded runner, never "re-read the file".
func PrimaryReasoningEffort(r *Runner) string {
	return PrimaryModelSelection(r).Effort
}

// runtimeHealthErr is the run entry-point gate: an unhealthy Runner refuses
// work until a complete build operation succeeds.
func (r *Runner) runtimeHealthErr() error {
	if r != nil && r.primaryState.unhealthy.Load() {
		return errRuntimeNeedsReload
	}
	return nil
}

// sessionModelSelectionKey carries one conversation's own model choice for
// the life of a run.
type sessionModelSelectionKey struct{}

// WithSessionModelSelection carries one session's effective model choice for
// the life of a run. RunContent injects it once per turn from the session's
// stored row; children (forks, subagents) inherit it with the context, which
// is what keeps "runs on whatever the primary runs on" true per session. The
// value is immutable for the run: a concurrent selection by another session
// cannot move an in-flight turn.
func WithSessionModelSelection(ctx context.Context, sel PrimaryModelState) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	sel.Set = true
	return context.WithValue(ctx, sessionModelSelectionKey{}, sel)
}

// SessionModelSelectionFromContext reports the selection this run carries.
func SessionModelSelectionFromContext(ctx context.Context) (PrimaryModelState, bool) {
	sel, ok := ctx.Value(sessionModelSelectionKey{}).(PrimaryModelState)
	if !ok || !sel.Set {
		return PrimaryModelState{}, false
	}
	return sel, true
}

// ResolveSessionModelChoice validates an exact provider+model pair against
// the runner's config and overlays the effort, without touching the runner.
// The pointer carries the same semantics SetPrimaryModel gives an explicit
// selection: nil derives the entry's configured effort once, and an explicit
// pointer — including "" — pins that exact value. It is the one resolution
// every surface shares: a gateway selection validates through it, and turn
// entry resolves the stored row through it.
func ResolveSessionModelChoice(r *Runner, provider, model string, effort *string) (PrimaryModelState, error) {
	if r == nil || r.AppCfg == nil {
		return PrimaryModelState{}, fmt.Errorf("select model: runner has no configuration")
	}
	entry, err := resolveAgentProvider(r.AppCfg, r.activeAgentNameForModel(), modelSelector{
		provider: strings.TrimSpace(provider),
		model:    strings.TrimSpace(model),
	})
	if err != nil {
		return PrimaryModelState{}, err
	}
	if effort != nil {
		entry.Params = appcfg.WithReasoningEffort(entry.Params, *effort)
	}
	return PrimaryModelState{
		Provider: strings.TrimSpace(entry.Provider),
		Model:    strings.TrimSpace(entry.Model),
		Effort:   appcfg.ReasoningEffort(entry.Params),
		Set:      true,
	}, nil
}

// sessionModelClient builds the client one carried selection names: the exact
// configured entry with the selection's concrete effort overlaid, constructed
// the same way the explicit load intent constructs its candidate. The test
// override hook is honored first for the same reason loadLocked honors it:
// a stubbed suite must observe the routed call rather than dial a provider.
func (r *Runner) sessionModelClient(sel PrimaryModelState) (llm.LLM, error) {
	if llmOverrideForTest != nil {
		return llmOverrideForTest, nil
	}
	entry, err := resolveAgentProvider(r.AppCfg, r.activeAgentNameForModel(), modelSelector{
		provider: sel.Provider,
		model:    sel.Model,
	})
	if err != nil {
		return nil, err
	}
	entry.Params = appcfg.WithReasoningEffort(entry.Params, sel.Effort)
	chain := llmYAMLsFromResolved([]appcfg.AgentLLMProviderConfig{entry})
	if len(chain) == 0 || chain[0] == nil {
		return nil, fmt.Errorf("session model %s/%s did not resolve to a chain entry", sel.Provider, sel.Model)
	}
	return NewLLMFromYAML(chain[0])
}

// sessionModelLLM routes a run's LLM calls to the model its context selected.
// It wraps only the base client: every prompt-shaping wrapper above it is
// model-agnostic, so the provider's cached prefix never moves when a session
// picks a different model.
type sessionModelLLM struct {
	inner   llm.LLM
	build   func(PrimaryModelState) (llm.LLM, error)
	mu      sync.Mutex
	clients map[string]llm.LLM
}

func wrapSessionModelLLM(inner llm.LLM, build func(PrimaryModelState) (llm.LLM, error)) llm.LLM {
	if inner == nil || build == nil {
		return inner
	}
	return &sessionModelLLM{inner: inner, build: build, clients: map[string]llm.LLM{}}
}

func (w *sessionModelLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	sel, ok := SessionModelSelectionFromContext(ctx)
	if !ok {
		return w.inner.Execute(ctx, messages, tools)
	}
	client, err := w.clientFor(sel)
	if err != nil {
		// The pair vanished under a mid-run reload. The ordinary-reload
		// semantics apply: fall back to the runner's current model and say
		// so, rather than failing a turn whose conversation is still healthy.
		slog.Warn("session model no longer configured; the call runs on the runner's current model",
			"provider", sel.Provider, "model", sel.Model, "err", err)
		return w.inner.Execute(ctx, messages, tools)
	}
	return client.Execute(ctx, messages, tools)
}

// clientFor reuses one client per selection for the life of the wrapper: a
// turn's tool loop calls repeatedly, and rebuilding the HTTP client every
// step would throw away connection reuse for no reason. The wrapper is
// rebuilt on every load, so the cache never outlives its config generation.
func (w *sessionModelLLM) clientFor(sel PrimaryModelState) (llm.LLM, error) {
	key := strings.ToLower(strings.TrimSpace(sel.Provider) + "/" + strings.TrimSpace(sel.Model) + "/" + strings.TrimSpace(sel.Effort))
	w.mu.Lock()
	defer w.mu.Unlock()
	if client, ok := w.clients[key]; ok && client != nil {
		return client, nil
	}
	client, err := w.build(sel)
	if err != nil {
		return nil, err
	}
	w.clients[key] = client
	return client, nil
}

// injectSessionModelSelection resolves the conversation's stored model choice
// once at turn entry and carries it in the context. A context that already
// carries one (a child inheriting its parent session's) keeps it. A stored
// pair the config no longer offers heals the row to the runner's current
// selection and runs on it — the same ordinary-reload fallback the TUI's
// activation applies, per turn.
func (r *Runner) injectSessionModelSelection(ctx context.Context) context.Context {
	if r == nil || r.Deps == nil || r.Deps.SessionStore == nil {
		return ctx
	}
	if _, ok := SessionModelSelectionFromContext(ctx); ok {
		return ctx
	}
	sid := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
	if sid == "" {
		return ctx
	}
	row, hasRow, err := r.Deps.SessionStore.SessionModelSelection(ctx, sid)
	if err != nil || !hasRow {
		return ctx
	}
	effort := row.Effort
	resolved, err := ResolveSessionModelChoice(r, row.Provider, row.Model, &effort)
	if err == nil {
		return WithSessionModelSelection(ctx, resolved)
	}
	// The stored pair is gone: run on the runner's current selection and
	// rewrite the stale row so the next turn and every marker agree.
	fallback := PrimaryModelSelection(r)
	slog.Warn("session model no longer configured; the session runs on the configured default",
		"session_id", sid,
		"old_provider", strings.TrimSpace(row.Provider),
		"old_model", strings.TrimSpace(row.Model),
		"new_provider", fallback.Provider,
		"new_model", fallback.Model)
	if _, saveErr := r.Deps.SessionStore.SaveSessionModelSelection(ctx, sid, state.SessionModelSelection{
		Provider: fallback.Provider,
		Model:    fallback.Model,
		Effort:   fallback.Effort,
	}); saveErr != nil {
		slog.Warn("rewrite the stale session model row", "session_id", sid, "err", saveErr)
	}
	return ctx
}

// effectiveModelFor names the model this context's conversation runs on: the
// run's carried selection first, then (for callers outside a turn, e.g. an
// explicit /compact) the session's stored row resolved against the live
// config, then the runner's published snapshot, then the config-order
// fallback an unloaded test fixture uses.
func (r *Runner) effectiveModelFor(ctx context.Context) (provider, model string) {
	if sel, ok := SessionModelSelectionFromContext(ctx); ok {
		return sel.Provider, sel.Model
	}
	if r != nil && r.Deps != nil && r.Deps.SessionStore != nil && ctx != nil {
		if sid := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx)); sid != "" {
			if row, hasRow, err := r.Deps.SessionStore.SessionModelSelection(ctx, sid); err == nil && hasRow {
				effort := row.Effort
				if resolved, err := ResolveSessionModelChoice(r, row.Provider, row.Model, &effort); err == nil {
					return resolved.Provider, resolved.Model
				}
			}
		}
	}
	return PrimaryModel(r)
}

// agentModelFor is the model the call in ctx is routed to: the dispatch-time
// override of the subagent making it, else that subagent type's own chain,
// else the conversation's model. It reads exactly what the routing wrappers
// read (subagentModelOverrideLLM, typedSubagentProviderLLM), so a compaction
// sizes itself by the window of the model that will actually receive the
// request.
func (r *Runner) agentModelFor(ctx context.Context) (provider, model string) {
	if !explicitMainThreadQuerySource(QuerySourceFromContext(ctx)) {
		if override, ok := SubagentModelOverrideFromContext(ctx); ok {
			return strings.TrimSpace(override.Provider), strings.TrimSpace(override.Model)
		}
		if p, m, _, ok := SubagentOwnModel(r, tool.SubagentTypeFromContext(ctx)); ok {
			return p, m
		}
	}
	return r.effectiveModelFor(ctx)
}

// agentCompactClientFor is the compaction client for the model agentModelFor
// names: the conversation's client on the main thread, and the client the
// subagent's own requests run on otherwise — built the way the routing
// wrappers build theirs. It serves the compaction paths that have no "send it
// as the conversation would" summarizer: a reactive compaction after the
// provider refused the request as too long, and a remote compaction.
func (r *Runner) agentCompactClientFor(ctx context.Context) llm.LLM {
	if !explicitMainThreadQuerySource(QuerySourceFromContext(ctx)) {
		if override, ok := SubagentModelOverrideFromContext(ctx); ok {
			client, err := ConfiguredModelClient(r.AppCfg, r.activeAgentNameForModel(), override.Provider, override.Model)
			if err != nil {
				slog.Warn("build subagent compaction client", "err", err)
				return nil
			}
			return client
		}
		if subtype := strings.TrimSpace(tool.SubagentTypeFromContext(ctx)); subtype != "" {
			if provider, model, _, ok := SubagentOwnModel(r, subtype); ok {
				client, err := ConfiguredModelClient(r.AppCfg, subtype, provider, model)
				if err != nil {
					slog.Warn("build typed subagent compaction client", "type", subtype, "err", err)
					return nil
				}
				return client
			}
		}
	}
	return r.sessionClientFor(ctx)
}

// sessionClientFor builds the client this context's conversation should run
// on, mirroring effectiveModelFor's resolution order. It serves the explicit
// compaction path, which runs outside a turn's context.
func (r *Runner) sessionClientFor(ctx context.Context) llm.LLM {
	if sel, ok := SessionModelSelectionFromContext(ctx); ok {
		if client, err := r.sessionModelClient(sel); err == nil {
			return client
		}
	}
	if r != nil && r.Deps != nil && r.Deps.SessionStore != nil && ctx != nil {
		if sid := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx)); sid != "" {
			if row, hasRow, err := r.Deps.SessionStore.SessionModelSelection(ctx, sid); err == nil && hasRow {
				effort := row.Effort
				if resolved, err := ResolveSessionModelChoice(r, row.Provider, row.Model, &effort); err == nil {
					if client, err := r.sessionModelClient(resolved); err == nil {
						return client
					}
				}
			}
		}
	}
	return r.effectivePrimaryClient()
}

// PrimaryModelForSession names the model one conversation runs on, resolving
// its stored choice against the runner's live config before falling back to
// the runner's published selection. It is the exported resolution for
// surface-held closures that know a session id but hold no context.
func PrimaryModelForSession(r *Runner, store *state.SessionStore, sessionID string) (string, string) {
	if r != nil && store != nil {
		if sid := strings.TrimSpace(sessionID); sid != "" {
			if row, hasRow, err := store.SessionModelSelection(context.Background(), sid); err == nil && hasRow {
				effort := row.Effort
				if resolved, err := ResolveSessionModelChoice(r, row.Provider, row.Model, &effort); err == nil {
					return resolved.Provider, resolved.Model
				}
			}
		}
	}
	return PrimaryModel(r)
}

// AgentModel names the model one agent of a conversation runs on, and the
// reasoning effort its configuration gives that model, the way the runtime
// routes its calls. subagent is nil for the primary agent. A subagent
// dispatched with a model override runs on it; a typed subagent whose
// definition has llm_providers runs on the first of them; the primary
// agent, a fork and a typed subagent without a chain of its own run on the
// conversation's model.
func AgentModel(r *Runner, conversationSessionID string, subagent *agent.HistoryEntry) (provider, model, effort string) {
	if subagent != nil && strings.TrimSpace(subagent.Model) != "" {
		provider = strings.TrimSpace(subagent.ModelProvider)
		model = strings.TrimSpace(subagent.Model)
		if r != nil {
			// The override's effort comes from the provider entry that model
			// resolves to — the same entry ConfiguredModelClient builds the
			// client from, so the footer states what the run actually uses.
			if entry, err := resolveAgentProvider(r.AppCfg, r.activeAgentNameForModel(), modelSelector{provider: provider, model: model}); err == nil {
				effort = reasoningEffortFromParams(entry.Params)
			}
		}
		return provider, model, effort
	}
	if subagent != nil {
		if p, m, e, ok := SubagentOwnModel(r, subagent.AgentType); ok {
			return p, m, e
		}
	}
	var store *state.SessionStore
	if r != nil && r.Deps != nil {
		store = r.Deps.SessionStore
	}
	p, m := PrimaryModelForSession(r, store, conversationSessionID)
	return p, m, PrimaryReasoningEffort(r)
}

// ContextOccupancy is how much of the context window a session fills right
// now: the whole prompt of its last API response. Every gauge reads it — the
// footer of either surface, /status, /context — so they never disagree.
func ContextOccupancy(ctx context.Context, store *state.SessionStore, sessionID string) (int, bool) {
	if store == nil {
		return 0, false
	}
	turns, err := store.ListRecentMessages(ctx, sessionID, 400)
	if err != nil {
		return 0, false
	}
	return state.TokenCountFromLastAPIResponse(turns), true
}

// ContextBudget is the context gauge of one agent of a conversation holding
// usage tokens: the window of the model that agent runs on, and how much of
// it is left before auto-compaction. subagent is nil for the primary agent.
// Usage 0 is a fresh context, which shows the whole window.
func ContextBudget(r *Runner, conversationSessionID string, subagent *agent.HistoryEntry, usage int) (event.TokenBudgetUpdatedPayload, bool) {
	provider, model, _ := AgentModel(r, conversationSessionID, subagent)
	limits, _ := llm.Lookup(provider, model)
	explicitLimit := 0
	if r != nil && r.AppCfg != nil {
		explicitLimit = r.AppCfg.Compact.ModelAutoCompactTokenLimit
	}
	budget := state.CalculateTokenBudgetWithOptions(usage, model, limits, state.TokenBudgetOptions{ExplicitLimit: explicitLimit})
	if usage > 0 {
		// A measured context reports unless the calculation itself produced
		// nothing to show — the same condition the surface footers used
		// before the computation moved here.
		if budget.PercentLeft <= 0 && budget.TokenUsage <= 0 {
			return event.TokenBudgetUpdatedPayload{}, false
		}
	} else if budget.ContextWindow <= 0 {
		return event.TokenBudgetUpdatedPayload{}, false
	}
	payload := event.TokenBudgetUpdatedPayload{
		Model:                budget.Model,
		TokenUsage:           budget.TokenUsage,
		PercentLeft:          budget.PercentLeft,
		ContextWindow:        budget.ContextWindow,
		EffectiveWindow:      budget.EffectiveContextWindow,
		AutoCompactThreshold: budget.AutoCompactThreshold,
	}
	if subagent != nil {
		payload.AgentID = agent.RosterKey(subagent.TaskID, subagent.AgentType)
	}
	return payload, true
}
