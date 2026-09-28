// Model catalog queries, the model choice /model offers, and subagent listings.
package turn

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// ModelSourceChatGPTAccount marks records discovered live from the ChatGPT
// subscription backend for the logged-in account. Records from the static
// models.json catalog carry no source: nothing was fetched for them.
const ModelSourceChatGPTAccount = "chatgpt-account"

type ModelRecord struct {
	ModelID             string `json:"model_id"`
	ModelName           string `json:"model_name"`
	Provider            string `json:"provider"`
	APIModel            string `json:"api_model"`
	ContextWindow       int64  `json:"context_window"`
	DefaultMaxTokens    int64  `json:"default_max_tokens"`
	CanReason           bool   `json:"can_reason"`
	SupportsAttachments bool   `json:"supports_attachments"`
	// ReasoningEfforts lists the reasoning levels the backend reported for the
	// model. Empty means the model is not known to reason.
	ReasoningEfforts []string `json:"supported_reasoning_efforts,omitempty"`
	// DefaultReasoningEffort is the backend's own default level, when it
	// reported one.
	DefaultReasoningEffort string `json:"default_reasoning_effort,omitempty"`
	// IsDefault marks the entry a fresh listing should preselect: the first
	// picker-visible model in the backend's priority order.
	IsDefault bool `json:"is_default,omitempty"`
	// Source and FetchedAt are set only for records fetched live.
	Source    string    `json:"source,omitempty"`
	FetchedAt time.Time `json:"fetched_at,omitempty"`
}

type ModelCatalogQuery struct {
	Query    string
	Provider string
	Limit    int
}

// ChatGPTModelsSource discovers the ChatGPT subscription account's available
// models. Production wiring adapts the Codex backend client; tests install
// stubs. The single-method port lives here because this package owns the
// catalog projection every surface displays.
type ChatGPTModelsSource interface {
	ChatGPTModels(ctx context.Context) ([]openai.ModelInfo, error)
}

// chatgptProvider is the internal provider name for ChatGPT subscription
// models. It stays distinct from API-key "openai" so a subscription model and
// a platform model can never be conflated in a picker or a config.
const chatgptProvider = "chatgpt"

// DiscoverChatGPTModels returns the account's picker-visible subscription
// models: hidden entries dropped, backend priority order preserved (stable for
// equal priorities), and the first entry marked as the default a fresh
// listing preselects.
func DiscoverChatGPTModels(ctx context.Context, src ChatGPTModelsSource) ([]ModelRecord, error) {
	if src == nil {
		return nil, fmt.Errorf("chatgpt model discovery is not configured")
	}
	infos, err := src.ChatGPTModels(ctx)
	if err != nil {
		return nil, err
	}
	fetched := time.Now()
	priorities := make(map[string]int32, len(infos))
	records := make([]ModelRecord, 0, len(infos))
	for _, info := range infos {
		slug := strings.TrimSpace(info.Slug)
		if !info.PickerVisible() || slug == "" {
			continue
		}
		priorities[slug] = info.Priority
		records = append(records, chatGPTModelRecord(info, fetched))
	}
	sort.SliceStable(records, func(i, j int) bool {
		return priorities[records[i].APIModel] < priorities[records[j].APIModel]
	})
	if len(records) > 0 {
		records[0].IsDefault = true
	}
	return records, nil
}

func chatGPTModelRecord(info openai.ModelInfo, fetched time.Time) ModelRecord {
	apiModel := strings.TrimSpace(info.Slug)
	name := strings.TrimSpace(info.DisplayName)
	if name == "" {
		name = apiModel
	}
	efforts := make([]string, 0, len(info.SupportedReasoningEfforts))
	for _, preset := range info.SupportedReasoningEfforts {
		if effort := strings.TrimSpace(preset.Effort); effort != "" {
			efforts = append(efforts, effort)
		}
	}
	var contextWindow int64
	if info.ContextWindow != nil {
		// context_window is the window the backend runs the model with by
		// default; max_context_window is a config-override ceiling, not the
		// number a picker should display.
		contextWindow = *info.ContextWindow
	}
	return ModelRecord{
		ModelID:                chatgptProvider + "/" + apiModel,
		ModelName:              name,
		Provider:               chatgptProvider,
		APIModel:               apiModel,
		ContextWindow:          contextWindow,
		CanReason:              len(efforts) > 0,
		SupportsAttachments:    info.SupportsAttachments(),
		ReasoningEfforts:       efforts,
		DefaultReasoningEffort: strings.TrimSpace(info.DefaultReasoningEffort),
		Source:                 ModelSourceChatGPTAccount,
		FetchedAt:              fetched,
	}
}

// ModelCatalogStatus reports, per provider, where a listing's records came
// from and why a provider block may be missing. It exists so a failed ChatGPT
// discovery never reads as "the account has no models". FetchedAt is carried
// only for a successful fetch — an error status must not claim a fetch time.
type ModelCatalogStatus struct {
	Provider  string     `json:"provider"`
	Source    string     `json:"source,omitempty"`
	FetchedAt *time.Time `json:"fetched_at,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// ModelCatalogListing is a catalog query answer together with per-provider
// status for the providers that were queried live.
type ModelCatalogListing struct {
	Records []ModelRecord        `json:"records"`
	Status  []ModelCatalogStatus `json:"status,omitempty"`
}

// ListModelCatalogLive answers q from the static catalog and, for ChatGPT,
// from a live account discovery. ChatGPT records keep the backend's priority
// order and follow the static block, so the answer stays deterministic. A
// discovery failure leaves the static records intact and surfaces as status,
// never as an empty success.
func ListModelCatalogLive(ctx context.Context, src ChatGPTModelsSource, q ModelCatalogQuery) ModelCatalogListing {
	provider := strings.ToLower(strings.TrimSpace(q.Provider))
	listing := ModelCatalogListing{Records: []ModelRecord{}}
	// The static catalog holds no chatgpt/* entries, so a chatgpt-scoped
	// query skips it entirely instead of pulling every other provider in.
	if provider != chatgptProvider {
		listing.Records = ListModelCatalog(q)
	}
	if provider == "" || provider == chatgptProvider {
		records, err := DiscoverChatGPTModels(ctx, src)
		status := ModelCatalogStatus{Provider: chatgptProvider, Source: ModelSourceChatGPTAccount}
		if err != nil {
			status.Source = ""
			status.Error = err.Error()
		} else {
			needle := strings.ToLower(strings.TrimSpace(q.Query))
			kept := make([]ModelRecord, 0, len(records))
			for _, rec := range records {
				if needle == "" || modelRecordMatches(rec, needle) {
					kept = append(kept, rec)
				}
			}
			if len(records) > 0 {
				fetched := records[0].FetchedAt
				status.FetchedAt = &fetched
				listing.Records = append(listing.Records, kept...)
			}
		}
		listing.Status = append(listing.Status, status)
	}
	if q.Limit > 0 && len(listing.Records) > q.Limit {
		listing.Records = listing.Records[:q.Limit]
	}
	return listing
}

// ListModelCatalogLive discovers through the source installed on the service,
// or answers from the static catalog alone when none is installed.
func (s *Service) ListModelCatalogLive(ctx context.Context, q ModelCatalogQuery) ModelCatalogListing {
	if s == nil {
		return ListModelCatalogLive(ctx, nil, q)
	}
	return ListModelCatalogLive(ctx, s.chatgptModels, q)
}

func (s *Service) ListModelCatalog(q ModelCatalogQuery) []ModelRecord {
	return ListModelCatalog(q)
}

func ListModelCatalog(q ModelCatalogQuery) []ModelRecord {
	needle := strings.ToLower(strings.TrimSpace(q.Query))
	provider := strings.ToLower(strings.TrimSpace(q.Provider))
	models := llm.AllModels()
	out := make([]ModelRecord, 0, len(models))
	for _, m := range models {
		rec := modelRecordFromCatalog(m)
		if provider != "" && strings.ToLower(rec.Provider) != provider {
			continue
		}
		if needle != "" && !modelRecordMatches(rec, needle) {
			continue
		}
		out = append(out, rec)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !strings.EqualFold(out[i].Provider, out[j].Provider) {
			return strings.ToLower(out[i].Provider) < strings.ToLower(out[j].Provider)
		}
		if !strings.EqualFold(out[i].ModelName, out[j].ModelName) {
			return strings.ToLower(out[i].ModelName) < strings.ToLower(out[j].ModelName)
		}
		return strings.ToLower(out[i].ModelID) < strings.ToLower(out[j].ModelID)
	})
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out
}

func modelRecordFromCatalog(m llm.Model) ModelRecord {
	apiModel := strings.TrimSpace(m.APIModel)
	if apiModel == "" {
		apiModel = m.ID
	}
	name := strings.TrimSpace(m.Name)
	if name == "" {
		name = apiModel
	}
	return ModelRecord{
		ModelID:             m.ID,
		ModelName:           name,
		Provider:            m.Provider,
		APIModel:            apiModel,
		ContextWindow:       m.ContextWindow,
		DefaultMaxTokens:    m.DefaultMaxTokens,
		CanReason:           m.CanReason,
		SupportsAttachments: m.SupportsAttachments,
	}
}

func modelRecordMatches(rec ModelRecord, needle string) bool {
	return strings.Contains(strings.ToLower(rec.ModelID), needle) ||
		strings.Contains(strings.ToLower(rec.ModelName), needle) ||
		strings.Contains(strings.ToLower(rec.Provider), needle) ||
		strings.Contains(strings.ToLower(rec.APIModel), needle)
}

type SubagentHistoryQuery struct {
	SessionID   string `json:"session_id,omitempty"`
	ParentRunID string `json:"parent_run_id,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

type SubagentRunListing struct {
	ParentRunID string               `json:"parent_run_id"`
	Runs        []state.Run          `json:"runs"`
	Records     []agent.HistoryEntry `json:"records"`
}

func (s *Service) ListSubagentHistory(q SubagentHistoryQuery) ([]agent.HistoryEntry, error) {
	query := agent.Query{
		SessionID:   strings.TrimSpace(q.SessionID),
		ParentRunID: strings.TrimSpace(q.ParentRunID),
		TaskID:      strings.TrimSpace(q.TaskID),
		RunID:       strings.TrimSpace(q.RunID),
		Limit:       q.Limit,
	}
	// An unset root is not a special case: ListMerged reads the live index for
	// that (empty) scope and skips the ledger, which is what the two explicit
	// registry fallbacks used to do by hand.
	if s == nil {
		return agent.ListMerged("", query)
	}
	return agent.ListMerged(strings.TrimSpace(s.workspaceRoot), query)
}

func (s *Service) ListRunSubagents(ctx context.Context, parentRunID string, limit int, statuses ...state.RunStatus) (SubagentRunListing, error) {
	parentRunID = strings.TrimSpace(parentRunID)
	listing := SubagentRunListing{
		ParentRunID: parentRunID,
		Runs:        []state.Run{},
		Records:     []agent.HistoryEntry{},
	}
	if parentRunID == "" {
		return listing, nil
	}
	if s != nil && s.childRunStore != nil {
		runs, err := s.childRunStore.ListChildRuns(ctx, parentRunID, limit, statuses...)
		if err != nil {
			return SubagentRunListing{}, err
		}
		listing.Runs = runs
	}
	records, err := s.ListSubagentHistory(SubagentHistoryQuery{
		ParentRunID: parentRunID,
		Limit:       limit,
	})
	if err != nil {
		return SubagentRunListing{}, err
	}
	listing.Records = records
	return listing, nil
}

// ModelChoice is one model an agent is configured with, as /model offers it.
type ModelChoice struct {
	// Label is what a picker shows and a choice names: "provider / model",
	// with the catalog's display name when it has one.
	Label    string
	Provider string
	Model    string
	// Name is the catalog's display name for the model, "" when it has none.
	Name      string
	CanReason bool
}

// ReasoningEffortLevels are the reasoning efforts /model offers for a model
// that reasons.
var ReasoningEffortLevels = []string{"low", "medium", "high", "xhigh"}

// agentDefinition is the configured definition of agentName, falling back to
// main the way the runtime does.
func agentDefinition(cfg *appcfg.Root, agentName string) appcfg.AgentDefinition {
	if cfg == nil || cfg.Agents.Definitions == nil {
		return appcfg.AgentDefinition{}
	}
	if def, ok := cfg.Agents.Definitions[strings.TrimSpace(agentName)]; ok {
		return def
	}
	return cfg.Agents.Definitions["main"]
}

// ModelChoices lists the models agentName is configured with, in their
// configured order; the first is the one in force.
func ModelChoices(cfg *appcfg.Root, agentName string) []ModelChoice {
	var out []ModelChoice
	for _, configured := range appcfg.ResolvedLLMConfigs(agentDefinition(cfg, agentName)) {
		provider, model := strings.TrimSpace(configured.Provider), strings.TrimSpace(configured.Model)
		if provider == "" && model == "" {
			continue
		}
		choice := ModelChoice{Label: llm.FormatProviderModel(provider, model), Provider: provider, Model: model}
		if hit, ok := llm.Lookup(provider, model); ok {
			if name := strings.TrimSpace(hit.DisplayName); name != "" {
				choice.Name = name
				choice.Label += " - " + name
			}
			choice.CanReason = hit.CanReason
		}
		out = append(out, choice)
	}
	return out
}

// ChooseModel makes choice agentName's model in the config file at cfgPath.
// The surface takes the file back into its live config afterwards.
func ChooseModel(cfgPath, agentName string, choice ModelChoice) error {
	cfg, err := appcfg.LoadPersisted(cfgPath)
	if err != nil {
		return err
	}
	if err := appcfg.PromoteMatchingLLMProvider(&cfg, agentName, choice.Provider, choice.Model); err != nil {
		// A choice missing from the freshly loaded file means the
		// configuration changed underneath the picker; synthesizing an entry
		// from index zero would relabel another provider's credentials. Leave
		// the file untouched and report the conflict.
		return err
	}
	return appcfg.Save(cfgPath, cfg)
}

// ChooseReasoningEffort sets the reasoning effort choice runs at for the
// exact provider+model entry it names in the config file at cfgPath, and
// promotes that entry to the default position. It must not read params from
// config index zero: the live session choice and the shared file default can
// legitimately differ.
func ChooseReasoningEffort(cfgPath, agentName string, choice ModelChoice, effort string) error {
	cfg, err := appcfg.LoadPersisted(cfgPath)
	if err != nil {
		return err
	}
	if !choice.CanReason {
		// A model that cannot reason carries no effort at all, which is an
		// explicit removal rather than "keep whatever is there".
		effort = ""
	}
	if err := appcfg.SetLLMProviderReasoningEffort(&cfg, agentName, choice.Provider, choice.Model, effort); err != nil {
		return err
	}
	return appcfg.Save(cfgPath, cfg)
}
