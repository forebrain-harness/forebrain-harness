// Model catalog queries, the model choice /model offers, and subagent listings.
package turn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/anthropic"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// ModelSourceChatGPTAccount marks records discovered live from the ChatGPT
// subscription backend for the logged-in account. Records from the static
// models.json catalog carry no source: nothing was fetched for them.
const ModelSourceChatGPTAccount = "chatgpt-account"

// ModelSourceOpenCodeAccount marks records discovered live from an OpenCode
// surface — Console (pay-as-you-go) or the Go subscription.
// Like the ChatGPT ones, they exist nowhere in the static catalog.
const ModelSourceOpenCodeAccount = "opencode-account"

// The two OpenCode provider ids. They differ only in the inference root and in
// the protocol a handful of models speak, but they stay separate provider
// names so a Console model and a Go subscription model can never be conflated
// in a picker or a config.
const (
	opencodeProvider   = "opencode"
	opencodeGoProvider = "opencode-go"
)

type ModelRecord struct {
	ModelID             string `json:"model_id"`
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

// OpenCodeModelsSource discovers an OpenCode surface's model catalog. The two
// methods live on one port because a listing needs both answers before it
// decides anything: what the surface serves, and whether this machine holds a
// credential at all.
type OpenCodeModelsSource interface {
	// OpenCodeModels lists a surface's catalog: Console (goSubscription
	// false) or the Go subscription (true). Production wiring adapts
	// opencode.FetchModels with a transport; tests install stubs.
	OpenCodeModels(ctx context.Context, goSubscription bool) ([]opencode.ModelInfo, error)
	// OpenCodeSignedIn reports whether a login file or OPENCODE_API_KEY
	// exists on this machine. The Go catalog needs a subscription token, so
	// asking without one only earns a 401 that reads like broken login.
	OpenCodeSignedIn() bool
}

// DiscoverOpenCodeModels returns one surface's picker-visible models: the
// systemone ids (jev-*, a structured decision endpoint rather than a chat
// model) dropped, in the catalog's order, and the first entry marked as the
// default a fresh listing preselects.
//
// The catalogs carry nothing but ids, so the rest of each record comes from
// models.json — which now carries a row for every id these surfaces serve —
// and falls back to opencode's own table for an id the catalog has never seen
// (the endpoint is the newer source).
func DiscoverOpenCodeModels(ctx context.Context, src OpenCodeModelsSource, goSubscription bool) ([]ModelRecord, error) {
	if src == nil {
		return nil, fmt.Errorf("opencode model discovery is not configured")
	}
	infos, err := src.OpenCodeModels(ctx, goSubscription)
	if err != nil {
		return nil, err
	}
	provider := opencodeProvider
	if goSubscription {
		provider = opencodeGoProvider
	}
	fetched := time.Now()
	records := make([]ModelRecord, 0, len(infos))
	seen := make(map[string]bool, len(infos))
	for _, info := range infos {
		id := strings.ToLower(strings.TrimSpace(info.ID))
		if id == "" || strings.HasPrefix(id, "jev-") || seen[id] {
			continue
		}
		seen[id] = true
		meta, known := opencode.LookupModelMeta(provider, id)
		if !known {
			meta = opencode.DefaultModelMeta(id)
		}
		records = append(records, openCodeModelRecord(provider, id, meta))
	}
	for i := range records {
		records[i].Source = ModelSourceOpenCodeAccount
		records[i].FetchedAt = fetched
	}
	if len(records) > 0 {
		records[0].IsDefault = true
	}
	return records, nil
}

// openCodeModelRecord projects one catalog id onto this package's record shape.
//
// Capabilities come from models.json first — it carries every id these surfaces
// serve, with the upstream window, image support and reasoning flag — and from
// opencode's own table only for an id the catalog has no row for.
func openCodeModelRecord(provider, id string, meta opencode.ModelMeta) ModelRecord {
	record := ModelRecord{
		ModelID:             provider + "/" + id,
		Provider:            provider,
		APIModel:            id,
		ContextWindow:       meta.ContextWindow,
		CanReason:           meta.Reasoning,
		SupportsAttachments: meta.Attachments,
	}
	if hit, ok := llm.Lookup(provider, id); ok && strings.EqualFold(hit.CatalogID, provider+"/"+id) {
		// Only this surface's own row counts: llm.Lookup falls back to an
		// unambiguous short name across providers, and the same id can be
		// served on both surfaces with a different window.
		if hit.ContextWindow > 0 {
			record.ContextWindow = hit.ContextWindow
		}
		record.CanReason = hit.CanReason
		record.SupportsAttachments = hit.SupportsAttachments
	}
	record.ReasoningEfforts = ModelReasoningEfforts(provider, id)
	return record
}

// ModelReasoningEfforts names the thinking levels a model offers, empty levels
// meaning the surface cannot pick one. models.json is the only source: its
// reasoning_options, shipped base and home overlay merged, trimmed to the
// levels this build can actually send on the protocol the model speaks. A model
// the file says nothing about has no levels — the picker asks nothing rather
// than offering a scale the model does not have — and a model whose data names
// only a toggle or a token budget has none either. ChatGPT's account data is
// the one exception and does not come through here.
func ModelReasoningEfforts(provider, model string) []string {
	levels, known := llm.ReasoningEfforts(provider, model)
	if !known {
		return nil
	}
	return trimToSendable(provider, model, levels)
}

// trimToSendable keeps the levels this build can put on the wire for a model.
//
// The deciding fact is which client pkg/run will build the request with, not
// which provider the model belongs to: that dispatch is by protocol, and an
// OpenCode model on the Anthropic path goes through the same client as the
// anthropic provider — a client that drops a level it rejects and sends no
// effort at all. Offering such a level would be exactly the fake option this
// catalog exists to remove, so the protocol decides here too. The Google
// generateContent client carries no effort parameter at all; the
// OpenAI-compatible and Responses clients pass the parameter through, so they
// send whatever the catalog names.
func trimToSendable(provider, model string, levels []string) []string {
	protocol, err := opencode.ProtocolFor(provider, model)
	sendsThroughAnthropic := anthropicClientProvider(provider) || (err == nil && protocol == opencode.ProtocolAnthropic)
	if sendsThroughAnthropic {
		out := make([]string, 0, len(levels))
		for _, level := range levels {
			if anthropic.AcceptsEffort(level) {
				out = append(out, level)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	if err == nil && protocol == opencode.ProtocolGoogle {
		// The generateContent client carries no effort parameter.
		return nil
	}
	return append([]string(nil), levels...)
}

// anthropicClientProvider reports whether provider's models are served by this
// build's Anthropic client on the strength of the provider name alone (the
// protocol check in trimToSendable covers the rest).
func anthropicClientProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic", "minimax":
		return true
	}
	return false
}

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

// SignInState is what a connect entry learns about the login this machine
// already holds for a provider.
type SignInState int

const (
	// SignInUsable: the stored login listed its models — a finished sign-in.
	SignInUsable SignInState = iota
	// SignInMissing: no login on this machine (no file, an incomplete one,
	// and for OpenCode no OPENCODE_API_KEY either).
	SignInMissing
	// SignInDead: the server rejected the login (a dead refresh token or a
	// 401 on the catalog) — only a new OAuth flow restores it.
	SignInDead
	// SignInUnreachable: the models could not be fetched for a reason that
	// is not the login (network, the npm registry lookup ChatGPT discovery
	// needs, 5xx). The login stands; no sign-in may be forced on it.
	SignInUnreachable
)

// SignInCheck is a sign-in probe's answer. Records is set only when State is
// SignInUsable, so a caller can go straight to the model picker.
type SignInCheck struct {
	State   SignInState
	Records []ModelRecord
	// Err is the discovery error behind SignInDead and SignInUnreachable,
	// for a caller that reports or retries it; nil otherwise.
	Err error
}

// ProbeChatGPTModels answers whether the ChatGPT login at path can list its
// models right now: it lists them through src, which refreshes a nearly
// expired token the way a turn would. path must come from
// openai.CredentialsPath (it honors the configured override); the login
// itself is read through the installed credential store — the state database
// from plan 188 on — with the file as the fallback.
func ProbeChatGPTModels(ctx context.Context, path string, src ChatGPTModelsSource) SignInCheck {
	if _, err := openai.LoadProcessCredentials(ctx, path); err != nil {
		return SignInCheck{State: SignInMissing}
	}
	records, err := DiscoverChatGPTModels(ctx, src)
	return signInCheckFrom(records, err)
}

// ProbeOpenCodeGoModels answers whether the OpenCode Go subscription's
// credential can list its models. OpenCodeSignedIn covers both the login file
// and OPENCODE_API_KEY, so key mode is reusable too.
func ProbeOpenCodeGoModels(ctx context.Context, src OpenCodeModelsSource) SignInCheck {
	if src == nil {
		return SignInCheck{State: SignInMissing}
	}
	if !src.OpenCodeSignedIn() {
		return SignInCheck{State: SignInMissing}
	}
	records, err := DiscoverOpenCodeModels(ctx, src, true)
	return signInCheckFrom(records, err)
}

func signInCheckFrom(records []ModelRecord, err error) SignInCheck {
	if err == nil {
		return SignInCheck{State: SignInUsable, Records: records}
	}
	if SignInRejected(err) {
		return SignInCheck{State: SignInDead, Err: err}
	}
	return SignInCheck{State: SignInUnreachable, Err: err}
}

// SignInRejected reports an error that only a new login can fix. The model
// endpoints and the OpenCode refresh already classify "log in again" as an
// HTTP 401 in typed errors; 403 is "no subscription", which a login cannot
// fix, so it stays unreachable-and-retryable. It lives here — not in
// pkg/llm — because the typed errors it reads (each provider's ModelsError)
// are defined in the provider packages this projection already imports; a
// future sign-in provider adds its type to this one place.
func SignInRejected(err error) bool {
	var api *llm.APIError
	if errors.As(err, &api) && api.StatusCode == http.StatusUnauthorized {
		return true
	}
	var oc *opencode.ModelsError
	if errors.As(err, &oc) && oc.StatusCode == http.StatusUnauthorized {
		return true
	}
	var oa *openai.ModelsError
	if errors.As(err, &oa) && oa.StatusCode == http.StatusUnauthorized {
		return true
	}
	return false
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

// modelSources are the live discovery sources a listing queries. They are
// bundled because the answer belongs to one query: an unfiltered listing asks
// every installed source at once.
type modelSources struct {
	chatgpt  ChatGPTModelsSource
	opencode OpenCodeModelsSource
}

// ListModelCatalogLive answers q from the static catalog and, for ChatGPT,
// from a live account discovery. ChatGPT records keep the backend's priority
// order and follow the static block, so the answer stays deterministic. A
// discovery failure leaves the static records intact and surfaces as status,
// never as an empty success.
//
// This form carries no OpenCode source: it is what a caller with only a
// ChatGPT port can ask. The Service method below passes both.
func ListModelCatalogLive(ctx context.Context, src ChatGPTModelsSource, q ModelCatalogQuery) ModelCatalogListing {
	return listModelCatalogLive(ctx, modelSources{chatgpt: src}, q)
}

func listModelCatalogLive(ctx context.Context, sources modelSources, q ModelCatalogQuery) ModelCatalogListing {
	provider := strings.ToLower(strings.TrimSpace(q.Provider))
	listing := ModelCatalogListing{Records: []ModelRecord{}}
	// The static catalog holds no chatgpt/*, opencode/* or opencode-go/*
	// entries, so a scoped query for one of them skips it entirely instead of
	// pulling every other provider in.
	if provider != chatgptProvider && provider != opencodeProvider && provider != opencodeGoProvider {
		listing.Records = ListModelCatalog(q)
	}
	if provider == "" || provider == chatgptProvider {
		records, err := DiscoverChatGPTModels(ctx, sources.chatgpt)
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
	// A listing that names no provider asks every source the machine could
	// have an account on. Without this guard every unscoped listing — the web
	// providers page opens one each time — would spend a request on a catalog
	// that answers 401 for users who never signed in to OpenCode. A query that
	// names "opencode" is an explicit ask and still fetches.
	if sources.opencode != nil && (provider == opencodeProvider ||
		(provider == "" && sources.opencode.OpenCodeSignedIn())) {
		// The Console catalog needs a credential, so a failed fetch is
		// reported as status rather than answered with an offline list of
		// models that would fail on the first message.
		records, err := DiscoverOpenCodeModels(ctx, sources.opencode, false)
		status := ModelCatalogStatus{Provider: opencodeProvider, Source: ModelSourceOpenCodeAccount}
		switch {
		case err != nil:
			status.Source = ""
			status.Error = err.Error()
		case len(records) > 0:
			fetched := records[0].FetchedAt
			status.FetchedAt = &fetched
		}
		kept := filterLiveRecords(records, q.Query)
		listing.Records = append(listing.Records, kept...)
		listing.Status = append(listing.Status, status)
	}
	if sources.opencode != nil && (provider == "" || provider == opencodeGoProvider) {
		// The Go catalog needs the subscription token, so asking without a
		// credential spends a request to learn what the login file already
		// says, and there is no offline fallback to answer with.
		status := ModelCatalogStatus{Provider: opencodeGoProvider, Source: ModelSourceOpenCodeAccount}
		if !sources.opencode.OpenCodeSignedIn() {
			status.Source = ""
			status.Error = "not signed in"
		} else {
			records, err := DiscoverOpenCodeModels(ctx, sources.opencode, true)
			if err != nil {
				status.Source = ""
				status.Error = err.Error()
			} else if len(records) > 0 {
				fetched := records[0].FetchedAt
				status.FetchedAt = &fetched
			}
			listing.Records = append(listing.Records, filterLiveRecords(records, q.Query)...)
		}
		listing.Status = append(listing.Status, status)
	}
	if q.Limit > 0 && len(listing.Records) > q.Limit {
		listing.Records = listing.Records[:q.Limit]
	}
	return listing
}

// filterLiveRecords applies a query's needle to live records. A needle never
// hides records that were fetched: the filter is the search the caller typed,
// not a capability gate.
func filterLiveRecords(records []ModelRecord, query string) []ModelRecord {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return records
	}
	kept := make([]ModelRecord, 0, len(records))
	for _, rec := range records {
		if modelRecordMatches(rec, needle) {
			kept = append(kept, rec)
		}
	}
	return kept
}

// ListModelCatalogLive discovers through the sources installed on the
// service, or answers from the static catalog alone when none is installed.
func (s *Service) ListModelCatalogLive(ctx context.Context, q ModelCatalogQuery) ModelCatalogListing {
	if s == nil {
		return ListModelCatalogLive(ctx, nil, q)
	}
	return listModelCatalogLive(ctx, modelSources{chatgpt: s.chatgptModels, opencode: s.opencodeModels}, q)
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
		// This is the chat projection, not the reference data: a non-chat
		// entry (image, speech, embedding, decision, …) is not a model anyone
		// can talk to, and the login-backed providers' real model sets come
		// from their account catalogs, not from the reference rows. Filtering
		// here is what keeps every surface — the TUI model page, /api/models
		// and the web picker — in agreement.
		if !m.Chat() || llm.LoginBackedProvider(m.Provider) {
			continue
		}
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
		if !strings.EqualFold(out[i].APIModel, out[j].APIModel) {
			return strings.ToLower(out[i].APIModel) < strings.ToLower(out[j].APIModel)
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
	return ModelRecord{
		ModelID:             m.ID,
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
	// Label is what a picker shows and a choice names: "provider/model".
	Label    string
	Provider string
	Model    string
	// ReasoningEfforts are the thinking levels this model offers that this
	// build can send; empty means the picker has nothing to ask, either
	// because the model does not reason or because it has no discrete level.
	ReasoningEfforts []string
	// SupportsImages reports that the catalog knows this model accepts image
	// input. Unknown models report false: a picker marks only what it knows.
	SupportsImages bool
}

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
// configured order — storage order, not priority: the one in force is the
// effective default (default_model when set, otherwise the first).
func ModelChoices(cfg *appcfg.Root, agentName string) []ModelChoice {
	var out []ModelChoice
	for _, configured := range appcfg.ResolvedLLMConfigs(agentDefinition(cfg, agentName)) {
		provider, model := strings.TrimSpace(configured.Provider), strings.TrimSpace(configured.Model)
		if provider == "" && model == "" {
			continue
		}
		choice := ModelChoice{Label: llm.FormatProviderModel(provider, model), Provider: provider, Model: model}
		if hit, ok := llm.Lookup(provider, model); ok {
			choice.SupportsImages = hit.SupportsAttachments
		}
		choice.ReasoningEfforts = ModelReasoningEfforts(provider, model)
		out = append(out, choice)
	}
	return out
}

// defaultModelIndex reports the index within choices of the effective default
// — the entry default_model names, otherwise the first — so surfaces mark and
// diff against what an empty selection actually resolves to, not blindly
// against index zero. -1 when nothing resolves (empty list, or a hot-edited
// dangling default_model the loader would refuse).
func defaultModelIndex(cfg *appcfg.Root, agentName string, choices []ModelChoice) int {
	entry := appcfg.EffectiveDefaultLLM(agentDefinition(cfg, agentName))
	if entry == nil {
		return -1
	}
	for i, choice := range choices {
		if strings.EqualFold(strings.TrimSpace(choice.Provider), strings.TrimSpace(entry.Provider)) &&
			strings.EqualFold(strings.TrimSpace(choice.Model), strings.TrimSpace(entry.Model)) {
			return i
		}
	}
	return -1
}
