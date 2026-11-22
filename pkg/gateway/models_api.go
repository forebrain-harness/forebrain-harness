// The models page's API (plan 265): the install-wide model table as the web
// surface edits it — one row per configured model, addressed by row id — plus
// the reference lists the page's form is built from (the built-in providers,
// their endpoints and the protocols) and the live lookup that decides a model's
// thinking levels.
//
// The table is shared by every primary agent (plan 260), so nothing here names
// an agent except the default: agents.definitions.<agent>.default_model is the
// model an empty selection runs on. A row's stored key is answered in plaintext
// to the page that edits it (owner 2026-11-07: the edit drawer can reveal it)
// and nowhere else — never in a log, an error, /choices or an event.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// The protocols a row speaks, as the page names them. The three paths are
// pkg/config's own constants; "path" is any other API path, and "login" a
// subscription row whose endpoint comes from its sign-in.
const (
	llmProtocolChatCompletions   = "chat_completions"
	llmProtocolResponses         = "responses"
	llmProtocolAnthropicMessages = "anthropic_messages"
	llmProtocolPath              = "path"
	llmProtocolLogin             = "login"
)

// llmEffortLevels are the thinking levels a user may pick by hand, the set the
// catalog itself accepts (pkg/llm's reasoning_options whitelist without
// "none", which is not a level to choose but the absence of thinking).
var llmEffortLevels = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// llmModelCaps is one model's capability data, as the form's advanced section
// edits it. A nil pointer, a nil slice and "" all mean "the catalog decides" —
// the same rule the row's columns and the override layer follow.
type llmModelCaps struct {
	Images        *bool    `json:"images,omitempty"`
	PDF           *bool    `json:"pdf,omitempty"`
	Video         *bool    `json:"video,omitempty"`
	Audio         *bool    `json:"audio,omitempty"`
	Reasoning     *bool    `json:"reasoning,omitempty"`
	ReasoningOff  *bool    `json:"reasoning_off,omitempty"`
	Efforts       []string `json:"efforts,omitempty"`
	DefaultEffort string   `json:"default_effort,omitempty"`
	ContextWindow *int64   `json:"context_window,omitempty"`
	MaxOutput     *int64   `json:"max_output,omitempty"`
}

// llmModelRow is one row of the models table.
type llmModelRow struct {
	ID           string `json:"id"`
	Provider     string `json:"provider"`
	ProviderName string `json:"provider_name"`
	Custom       bool   `json:"custom"`
	Model        string `json:"model"`
	// ModelName is the vendor listing's readable name for Model, the wire id
	// when the listing named none — mirroring provider/provider_name. The
	// table shows it; identity and edits keep Model.
	ModelName string `json:"model_name,omitempty"`
	BaseURL   string `json:"base_url"`
	APIPath   string `json:"api_path"`
	// URLVerbatim is 「自定义协议」: requests go to BaseURL as stored.
	URLVerbatim bool `json:"url_verbatim"`
	// SubagentAllowed is the row's authorization switch (owner 2026-11-10):
	// may the agent pick it for a subagent on its own? Closed by default, and
	// a user's own pick never needs it.
	SubagentAllowed bool   `json:"subagent_allowed"`
	Protocol        string `json:"protocol"`
	Endpoint        string `json:"endpoint"`
	// CatalogProvider is the provider this row says its model's official data
	// lives under (owner 2026-11-16); "" matches the model name alone. It is
	// never sent with a request — it only decides which catalog entry
	// CatalogID names.
	CatalogProvider string `json:"catalog_provider,omitempty"`
	// CatalogID is the catalog entry this answer came from, so the drawer can
	// say which row its capabilities belong to.
	CatalogID   string `json:"catalog_id,omitempty"`
	APIKeySet   bool   `json:"api_key_set"`
	APIKeyLast4 string `json:"api_key_last4,omitempty"`
	// APIKey is the stored key in plaintext (owner 2026-11-07: the edit drawer
	// must be able to reveal it). Empty for a row with no key. Never logged,
	// never in an error, never in /choices or an event.
	APIKey      string `json:"api_key,omitempty"`
	LoginBacked bool   `json:"login_backed"`
	// Source is the subscription that wrote the row ("" = added by hand), and
	// SourceName its label; SourceState is how that sign-in stands right now.
	Source      string `json:"source,omitempty"`
	SourcePlan  string `json:"source_plan,omitempty"`
	SourceName  string `json:"source_name,omitempty"`
	SourceState string `json:"source_state,omitempty"`
	// Params is the row's request-parameter JSON text, verbatim: its keys are
	// the provider's request fields, and an object would have them re-cased by
	// a client that normalizes response keys.
	Params string `json:"params,omitempty"`
	// Caps is this row's own overrides, CatalogCaps what the catalog says
	// without them.
	Caps llmModelCaps `json:"caps"`
	// EffortsKnown reports that a source listed this model's thinking levels,
	// marking the catalog-sourced answer for display and reference. The row's
	// own caps stay editable either way and are stored as the user's assertion
	// (owner 2026-11-19): catalog-sourced levels are not immutable.
	EffortsKnown bool         `json:"efforts_known"`
	CatalogCaps  llmModelCaps `json:"catalog_caps"`
	IsDefault    bool         `json:"is_default"`
	// DefaultFor names the agents that name this row as their default model.
	DefaultFor []string `json:"default_for,omitempty"`
	// PinnedBy names the settings whose reference the loader requires to
	// resolve: a subagent type's row, the plan reviewer, the goal evaluator, a
	// subagent definition's own model (plan 345). Such a row cannot be
	// deleted either — the composed configuration would stop loading. A machine
	// kind plus the name it applies to, so each surface writes its own
	// language (plan 347).
	PinnedBy  []llmModelPin `json:"pinned_by,omitempty"`
	CreatedAt int64         `json:"created_at"`
}

// llmModelPin is one pin on the wire: the machine kind of the setting that
// pins the row, and the name it applies to (empty for the two install-wide
// rows, which omit it).
type llmModelPin struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

// llmModelPins projects the config pins into the wire shape; nil in, nil out.
func llmModelPins(pins []config.ModelPin) []llmModelPin {
	if len(pins) == 0 {
		return nil
	}
	out := make([]llmModelPin, 0, len(pins))
	for _, pin := range pins {
		out = append(out, llmModelPin{Kind: pin.Kind, Name: pin.Name})
	}
	return out
}

// llmModelWrite is one create, update or connection-test body. A nil pointer
// leaves the stored value alone on an update and means "nothing" on a create;
// an empty string clears it.
type llmModelWrite struct {
	Provider    string        `json:"provider"`
	Model       string        `json:"model"`
	BaseURL     string        `json:"base_url"`
	APIPath     string        `json:"api_path"`
	APIKey      *string       `json:"api_key"`
	Params      *string       `json:"params"`
	Caps        *llmModelCaps `json:"caps"`
	URLVerbatim *bool         `json:"url_verbatim"`
	// SubagentAllowed is the authorization switch; nil leaves it alone.
	SubagentAllowed *bool `json:"subagent_allowed"`
	// CatalogProvider names the provider a custom model's official data lives
	// under (owner 2026-11-16); nil leaves it alone, "" clears it.
	CatalogProvider *string `json:"catalog_provider"`
}

// llmModelIdWrite is a body that addresses a stored row as well: the
// connection test and the capability lookup take the key and the subscription
// of the row the form is editing.
type llmModelIdWrite struct {
	llmModelWrite
	ID string `json:"id"`
}

// llmModelCapsBody is what the capability lookup answers: the catalog's values
// plus which source answered for the thinking levels.
type llmModelCapsBody struct {
	llmModelCaps
	EffortsKnown  bool   `json:"efforts_known"`
	EffortsSource string `json:"efforts_source,omitempty"`
	// CatalogID is the catalog entry the values came from ("" for a name the
	// catalog does not describe), so the drawer shows what it matched.
	CatalogID string `json:"catalog_id,omitempty"`
}

// llmProviderOption is one entry of the form's provider picker.
type llmProviderOption struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	LoginBacked bool                  `json:"login_backed"`
	Endpoints   []llmProviderEndpoint `json:"endpoints"`
}

// llmProviderEndpoint is one endpoint a provider serves, as a choice.
type llmProviderEndpoint struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	APIPath string `json:"api_path"`
}

// llmModelChoice is one entry of the one list every model picker reads: a
// subagent's model, the memory settings, a new conversation's default.
type llmModelChoice struct {
	ID           string `json:"id"`
	Provider     string `json:"provider"`
	ProviderName string `json:"provider_name"`
	Model        string `json:"model"`
	// ModelName is the vendor listing's readable name, the wire id when the
	// listing named none — the name-only half of Label, mirroring
	// provider/provider_name.
	ModelName string   `json:"model_name"`
	BaseURL   string   `json:"base_url"`
	APIPath   string   `json:"api_path"`
	Endpoint  string   `json:"endpoint"`
	Label     string   `json:"label"`
	Efforts   []string `json:"efforts"`
	// DefaultEffort is the level this row itself is configured with (plan 300's
	// ModelChoice.ConfiguredEffort, read as-is). A picker that lets one type
	// override a row's effort shows what the row gets when it names none, so it
	// reads the row's own value rather than guessing from the catalog
	// (plan 299 §2).
	DefaultEffort  string `json:"default_effort"`
	SupportsImages bool   `json:"supports_images"`
}

// llmModelRefusal is one refused request: the sentence the page shows under the
// form and the status it belongs to, plus any fields the page reads
// structurally (the row a duplicate names, the agents a default belongs to).
type llmModelRefusal struct {
	status int
	msg    string
	extra  map[string]any
}

func (e *llmModelRefusal) Error() string { return e.msg }

// llmRefuse builds a refusal in the request's language.
func llmRefuse(r *http.Request, status int, zh, en string) error {
	return &llmModelRefusal{status: status, msg: llmText(r, zh, en)}
}

// prefersEnglish reports whether the request asks for English first. The web
// surface is Chinese-first: no header at all, or one that names no language,
// answers Chinese.
func prefersEnglish(r *http.Request) bool {
	if r == nil {
		return false
	}
	for _, tag := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag = strings.ToLower(strings.TrimSpace(strings.SplitN(tag, ";", 2)[0]))
		if tag == "" || tag == "*" {
			continue
		}
		return !strings.HasPrefix(tag, "zh")
	}
	return false
}

// llmText picks the wording for the request's language. A pair with no Chinese
// text answers the English one, which is what a provider or endpoint whose
// label is the same in both languages carries.
func llmText(r *http.Request, zh, en string) string {
	if zh == "" || prefersEnglish(r) {
		return en
	}
	return zh
}

// llmProviderName is the registry's label for a provider id, and whether it is
// the custom one. A provider id the registry does not carry — a row a
// pre-registry configuration wrote — is named by its id: the table has to be
// readable, and inventing a name for it would be worse.
func llmProviderName(r *http.Request, provider string) (name string, custom bool) {
	provider = strings.TrimSpace(provider)
	if strings.EqualFold(provider, config.CustomProvider) {
		return llmText(r, "自定义", "Custom"), true
	}
	if builtin, ok := config.BuiltinProviderByID(provider); ok {
		return llmText(r, builtin.LabelZH, builtin.Label), false
	}
	return provider, false
}

// llmModelProtocol names the wire protocol a row speaks, which is what the
// page's protocol line shows: the three protocol paths by name, any other path
// as "path", and a subscription row as its sign-in.
func llmModelProtocol(provider, apiPath string) string {
	if config.CredentialsBackedProvider(provider) {
		return llmProtocolLogin
	}
	switch strings.TrimSpace(apiPath) {
	case config.APIPathChatCompletions:
		return llmProtocolChatCompletions
	case config.APIPathResponses:
		return llmProtocolResponses
	case config.APIPathAnthropicMessages:
		return llmProtocolAnthropicMessages
	default:
		return llmProtocolPath
	}
}

// llmModelCapsOfRow is a row's own overrides.
func llmModelCapsOfRow(row state.LLMModel) llmModelCaps {
	return llmModelCaps{
		Images:        row.CapImages,
		PDF:           row.CapPDF,
		Video:         row.CapVideo,
		Audio:         row.CapAudio,
		Reasoning:     row.CapReasoning,
		ReasoningOff:  row.CapReasoningOff,
		Efforts:       row.CapEfforts,
		DefaultEffort: row.CapDefaultEffort,
		ContextWindow: row.CapContextWindow,
		MaxOutput:     row.CapMaxOutput,
	}
}

// llmModelCatalogCaps is what the catalog says a model can do, without any
// configured row's overrides (pkg/llm.LookupCatalogFor): the form's advanced
// switches start here and send null for a value that still matches it.
// catalogProvider is the row's own answer about where its data lives (owner
// 2026-11-16); "" asks the model name alone. apiPath is the protocol the row
// speaks, and the levels are reported through it: a level the client that sends
// this row's requests cannot carry is not a capability the page may offer, and
// neither is the off switch that level list would have named. The second result
// names the entry the answer came from.
func llmModelCatalogCaps(provider, model, catalogProvider, apiPath string) (llmModelCaps, string) {
	hit, ok := llm.LookupCatalogFor(provider, model, llm.JoinProviderModel(catalogProvider, model))
	if !ok {
		return llmModelCaps{}, ""
	}
	images, reasoning := hit.SupportsAttachments, hit.CanReason
	caps := llmModelCaps{Images: &images, Reasoning: &reasoning}
	if efforts, known := hit.ReasoningEfforts(); known && len(efforts) > 0 {
		caps.Efforts = llmSendableEfforts(provider, model, apiPath, efforts)
		// The catalog's off switch is its own level list naming "none": that is
		// the capability, reported apart from the levels it appears in. Leaving
		// it out made the form's own dropdown the only place the answer could
		// come from, and it had none (plan 311). A level list the protocol trims
		// "none" out of is no longer an off switch anybody can use.
		if slices.Contains(caps.Efforts, "none") {
			off := true
			caps.ReasoningOff = &off
		}
	}
	if hit.ContextWindow > 0 {
		window := hit.ContextWindow
		caps.ContextWindow = &window
	}
	if hit.DefaultMaxTokens > 0 {
		output := hit.DefaultMaxTokens
		caps.MaxOutput = &output
	}
	return caps, hit.CatalogID
}

// llmSendableEfforts keeps the thinking levels this build can actually put on the
// wire for a model, in the one order every picker draws them: the same decision
// pkg/turn makes for the terminal's default-effort page, so a level one surface
// offers is a level the other offers too (plan 311). apiPath is the protocol the
// row speaks; "" leaves the decision to the provider and model.
func llmSendableEfforts(provider, model, apiPath string, levels []string) []string {
	return turn.SetupReasoningEfforts(provider, model, apiPath, levels)
}

// llmModelAPIPathOf is the protocol a capability request is about: the row's own
// while editing, the one the form names while adding, "" when nobody said.
func llmModelAPIPathOf(write llmModelWrite, stored *state.LLMModel) string {
	if path := strings.TrimSpace(write.APIPath); path != "" {
		return path
	}
	if stored != nil {
		return strings.TrimSpace(stored.APIPath)
	}
	return ""
}

// llmModelCatalogProviderOf reads the catalog provider a capability request
// asks about: the body's answer when it names one (a pointer, so "" is an
// answer), the stored row's when the body says nothing, "" otherwise.
func llmModelCatalogProviderOf(write llmModelWrite, stored *state.LLMModel) string {
	if write.CatalogProvider != nil {
		return llmModelCatalogProvider(*write.CatalogProvider)
	}
	if stored != nil {
		return stored.CatalogProvider
	}
	return ""
}

// llmModelCatalogProvider normalizes the provider a model's official data lives
// under: trimmed, no surrounding slashes, empty for "the model name alone".
func llmModelCatalogProvider(raw string) string {
	return strings.Trim(strings.TrimSpace(raw), "/")
}

// llmModelApplyLiveCaps lays the live answer the process already holds for a
// row (plan 263's dispatch: the vendor's own list, else models.dev) over the
// catalog's levels, through the row's protocol exactly as the catalog's own are:
// a source that lists "minimal" for a Messages row lists a level this build
// drops, so the answer the page offers is the sendable one. It reports nothing —
// whether the row's levels are known is decided once, from the list the whole
// answer ends up with (llmModelRowEffortsKnown).
func llmModelApplyLiveCaps(rowID, provider, model, apiPath string, caps *llmModelCaps) {
	info, ok := process.ModelCatalogCaps(rowID)
	if !ok {
		return
	}
	caps.Efforts = llmSendableEfforts(provider, model, apiPath, info.Efforts)
	caps.DefaultEffort = info.DefaultEffort
}

// llmModelScope is one models-page request's surroundings: the table it reads
// or writes, the configuration as stored, and the primary agent the page marks
// as the row that runs today.
type llmModelScope struct {
	store   *state.LLMModelStore
	cfg     *config.Root
	agentID string
	// defaultID is the row an empty model selection runs on for this agent.
	defaultID string
}

// adoptLLMModelWrite re-reads the composed configuration after a table write, so
// the process runs on the rows the table now holds. Every picker and the
// conversation's own model read come from the runtime snapshot
// (Environment.Deps.Config()), which only moves when the composition is
// re-applied: a write that stored a row and left the snapshot alone would hide
// it until an unrelated reload, and the models page would show a model nothing
// can be switched to.
//
// The call is the turn boundary's own (Environment.SyncConfigFromDB, plan 187):
// one database read, one file read and a fingerprint comparison in the steady
// state, the full application only when the fingerprint moved — the model table
// is part of that fingerprint (plans 260/265).
//
// A failure is returned as it is rather than swallowed: the row is stored, but
// the process cannot run on it, and a page told "saved" would be lying.
func (s *Server) adoptLLMModelWrite(ctx context.Context) error {
	if s == nil || s.Env == nil {
		return nil
	}
	return s.Env.SyncConfigFromDB(ctx)
}

// llmModelScope resolves the surroundings, answering the failure itself.
func (s *Server) llmModelScope(w http.ResponseWriter, r *http.Request) (llmModelScope, bool) {
	if s == nil || s.Env == nil || s.Env.SQL == nil {
		http.Error(w, "no state database is bound", http.StatusServiceUnavailable)
		return llmModelScope{}, false
	}
	// The current agent's default is part of every answer here, so an
	// unresolvable "current" is a refusal rather than a guess (plan 265 STOP).
	active, err := s.activePrimarySummary()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return llmModelScope{}, false
	}
	cfg, err := s.persistedConfig(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return llmModelScope{}, false
	}
	scope := llmModelScope{store: &state.LLMModelStore{DB: s.Env.SQL}, cfg: &cfg, agentID: active.ID}
	if entry := config.EffectiveDefaultLLMFor(&cfg, active.ID); entry != nil {
		scope.defaultID = strings.TrimSpace(entry.ID)
	}
	return scope, true
}

// llmModelRowOf projects one stored row into the page's shape.
func (s *Server) llmModelRowOf(r *http.Request, scope llmModelScope, row state.LLMModel,
	states *subscriptionStates) llmModelRow {
	name, custom := llmProviderName(r, row.Provider)
	out := llmModelRow{
		ID:           row.ID,
		Provider:     row.Provider,
		ProviderName: name,
		Custom:       custom,
		Model:        row.Model,
		ModelName:    firstNonBlank(llm.ListingDisplayName(row.Provider, row.Model), row.Model),
		BaseURL:      row.BaseURL,
		APIPath:      row.APIPath,
		URLVerbatim:  row.URLVerbatim,
		// The switch is editable on any row, a subscription's included: it is
		// a permission, not the row's identity.
		SubagentAllowed: row.SubagentAllowed,
		Protocol:        llmModelProtocol(row.Provider, row.APIPath),
		Endpoint:        turn.ModelEndpointLabel(row.BaseURL, row.APIPath, row.URLVerbatim),
		APIKeySet:       row.APIKey != "",
		APIKeyLast4:     secretLast4(row.APIKey),
		APIKey:          row.APIKey,
		LoginBacked:     config.CredentialsBackedProvider(row.Provider),
		Source:          row.Source,
		SourcePlan:      row.SourcePlan,
		Params:          row.ParamsJSON,
		Caps:            llmModelCapsOfRow(row),
		CatalogProvider: row.CatalogProvider,
		IsDefault:       row.ID == scope.defaultID,
		DefaultFor:      config.ModelDefaultAgents(scope.cfg, row.ID),
		PinnedBy:        llmModelPins(config.ModelPins(scope.cfg, row.ID)),
		CreatedAt:       row.CreatedAt,
	}
	out.CatalogCaps, out.CatalogID = llmModelCatalogCaps(row.Provider, row.Model, row.CatalogProvider, row.APIPath)
	// The form only offers the levels this build can send on the row's protocol,
	// so the list is trimmed after the source has answered — the live answer
	// replaces the catalog's, trimmed the same way (plans 311, 318).
	llmModelApplyLiveCaps(row.ID, row.Provider, row.Model, row.APIPath, &out.CatalogCaps)
	// efforts_known is "a source listed levels this build can send", so a model
	// the catalog knows with nothing sendable — a toggle, a token budget, the
	// Google client — answers false. That is exactly the state the form renders
	// as the boxes to tick (plan 318 F4).
	out.EffortsKnown = len(out.CatalogCaps.Efforts) > 0
	// A row no source lists levels for keeps the ones it declares, trimmed the
	// same way: the picker offers only levels this row's client can send.
	// Storing an unsendable default is allowed now — the client drops it on
	// the way out — so this trimming exists for picker clarity, not to prevent
	// a refused save (plan 311; owner 2026-11-19).
	if !out.EffortsKnown && row.CapEfforts != nil {
		out.CatalogCaps.Efforts = llmSendableEfforts(row.Provider, row.Model, row.APIPath, row.CapEfforts)
	}
	// An off switch the protocol cannot send is not an answer this picker can
	// offer: the dropdown only names levels the client can send, so reporting
	// it would be the fake option the page exists to remove (plan 311 §2.1).
	// The save no longer refuses an unsendable default — it stores the user's
	// assertion as asked (owner 2026-11-19); this trimming is display only.
	if out.CatalogCaps.ReasoningOff != nil && !slices.Contains(out.CatalogCaps.Efforts, "none") {
		out.CatalogCaps.ReasoningOff = nil
	}
	if sub, ok := gatewaySubscriptionByID(row.Source); ok {
		label, labelZH := sub.Label()
		out.SourceName = llmText(r, labelZH, label)
		out.SourceState = states.of(row.Source)
	}
	return out
}

// handleLLMModels serves the table itself: one page of rows, or a new row.
func (s *Server) handleLLMModels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listLLMModels(w, r)
	case http.MethodPost:
		s.createLLMModel(w, r)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// listLLMModels answers one page of the table. The order is the table's own
// (oldest first): it is storage order, not priority, and it is the order every
// picker offers.
func (s *Server) listLLMModels(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.llmModelScope(w, r)
	if !ok {
		return
	}
	rows, err := scope.store.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// One context for the whole page: the sign-in probes below share it, so a
	// vendor that hangs delays this list by at most one discovery timeout
	// rather than one per subscription row.
	ctx, cancel := context.WithTimeout(r.Context(), modelsDiscoveryTimeout)
	defer cancel()
	states := newSubscriptionStates(s, ctx)
	out := make([]llmModelRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, s.llmModelRowOf(r, scope, row, states))
	}
	q := readListQuery(r)
	if q.Q != "" {
		out = llmModelRowsMatching(out, q.Q)
	}
	page, served := pageOf(out, q)
	writeListPage(w, "models", page, len(out), served, q, map[string]any{
		"default_model_id": scope.defaultID,
		"agent_id":         scope.agentID,
	})
}

// llmModelRowsMatching is the table's search box: the service name, the model,
// and the endpoint the requests go to.
func llmModelRowsMatching(rows []llmModelRow, q string) []llmModelRow {
	needle := strings.ToLower(strings.TrimSpace(q))
	matched := make([]llmModelRow, 0, len(rows))
	for _, row := range rows {
		for _, value := range []string{row.Provider, row.ProviderName, row.Model, row.BaseURL, row.APIPath} {
			if strings.Contains(strings.ToLower(value), needle) {
				matched = append(matched, row)
				break
			}
		}
	}
	return matched
}

// createLLMModel adds one row. A duplicate is a 409 naming the row that already
// has this identity: the table allows two rows for one model only when their
// endpoint differs.
func (s *Server) createLLMModel(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.llmModelScope(w, r)
	if !ok {
		return
	}
	var write llmModelWrite
	if !decodeLLMModelBody(w, r, &write) {
		return
	}
	row, err := s.llmModelFromWrite(r, write, nil)
	if err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	// Only a row with no key is meaningless: every API-key provider needs one,
	// and a subscription row never comes through here (its sign-in writes it).
	if row.APIKey == "" {
		s.failLLMModelWrite(w, r, llmRefuse(r, http.StatusBadRequest,
			"请填写 API Key", "an API key is required"))
		return
	}
	created, err := scope.store.Create(r.Context(), row)
	if err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	if err := s.adoptLLMModelWrite(r.Context()); err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	states := newSubscriptionStates(s, r.Context())
	writeJSONStatus(w, http.StatusCreated, s.llmModelRowOf(r, scope, created, states))
}

// handleLLMModelByID serves one row: rewrite it, or delete it.
func (s *Server) handleLLMModelByID(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPut:
		s.updateLLMModel(w, r)
	case http.MethodDelete:
		s.deleteLLMModel(w, r)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// updateLLMModel rewrites one row. A field the body leaves empty keeps the
// stored value: the drawer that edits only the advanced settings sends no
// identity at all.
func (s *Server) updateLLMModel(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.llmModelScope(w, r)
	if !ok {
		return
	}
	stored, err := scope.store.Get(r.Context(), pathParam(r, "id"))
	if err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	var write llmModelWrite
	if !decodeLLMModelBody(w, r, &write) {
		return
	}
	row, err := s.llmModelFromWrite(r, write, &stored)
	if err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	updated, err := scope.store.Update(r.Context(), row)
	if err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	if err := s.adoptLLMModelWrite(r.Context()); err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	states := newSubscriptionStates(s, r.Context())
	writeJSON(w, s.llmModelRowOf(r, scope, updated, states))
}

// deleteLLMModel removes one row through the shared delete semantics
// (process.DeleteLLMModel, plan 345). Two kinds of reference refuse it: a row
// an agent names as its default_model — the reference is an explicit choice,
// and deleting through it would move that agent to another model without
// saying so — and a row a subagent type, the plan reviewer, the goal check or a
// subagent definition pins by id, which the loader requires to resolve.
// Sessions and memory settings that named the row are not rewritten; the
// runtime refuses that turn with a re-choose diagnosis or skips the stale value
// (plans 260/261/315). The refusal carries both lists so the page can name every
// owner at once.
func (s *Server) deleteLLMModel(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.llmModelScope(w, r)
	if !ok {
		return
	}
	err := process.DeleteLLMModel(r.Context(), s.Env.SQL, scope.cfg, pathParam(r, "id"))
	var refusal *process.LLMModelRefusal
	if errors.As(err, &refusal) {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error":       llmRefusalSentence(r, refusal),
			"default_for": refusal.Defaults,
			"pinned_by":   llmModelPins(refusal.Pins),
		})
		return
	}
	if err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	if err := s.adoptLLMModelWrite(r.Context()); err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// llmRefusalSentence words a refusal in the request's language: the default
// case names the agents (the sentence the page has always shown), the pin case
// names the settings, and a row that is both names both. The English branch is
// the one sentence every English surface says (process.LLMModelRefusal.Error);
// only the Chinese wording is written here (plan 347).
func llmRefusalSentence(r *http.Request, refusal *process.LLMModelRefusal) string {
	switch {
	case len(refusal.Defaults) > 0 && len(refusal.Pins) > 0:
		return llmText(r,
			"它是「"+strings.Join(refusal.Defaults, "、")+"」的默认模型，也被「"+strings.Join(modelPinLabelsZH(refusal.Pins), "、")+"」引用；先把这些引用改成别的",
			refusal.Error())
	case len(refusal.Pins) > 0:
		return llmText(r,
			"它仍被「"+strings.Join(modelPinLabelsZH(refusal.Pins), "、")+"」引用；先把这些引用改成别的",
			refusal.Error())
	default:
		return llmText(r,
			"它是「"+strings.Join(refusal.Defaults, "、")+"」的默认模型，请先把默认改成别的",
			refusal.Error())
	}
}

// modelPinLabelZH is the Chinese label for one pin (plan 347). The page
// localizes from the payload with its own copy; this is for the API's Chinese
// sentence alone.
func modelPinLabelZH(pin config.ModelPin) string {
	switch pin.Kind {
	case config.ModelPinPlanReview:
		return "计划评审"
	case config.ModelPinGoalEvaluator:
		return "目标评估"
	default:
		if name := strings.TrimSpace(pin.Name); name != "" {
			return "子代理 " + name
		}
		return "子代理"
	}
}

// modelPinLabelsZH is modelPinLabelZH over a list, in the payload's order.
func modelPinLabelsZH(pins []config.ModelPin) []string {
	out := make([]string, 0, len(pins))
	for _, pin := range pins {
		out = append(out, modelPinLabelZH(pin))
	}
	return out
}

// handleLLMModelChoices serves the one list every model picker reads: the
// install's models in the display order every picker uses (provider, then the
// model's readable name, then the endpoint), each with the endpoint its
// requests go to and the levels it offers. It carries nothing about a key.
func (s *Server) handleLLMModelChoices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	scope, ok := s.llmModelScope(w, r)
	if !ok {
		return
	}
	choices := turn.ModelChoices(scope.cfg, scope.agentID)
	out := make([]llmModelChoice, 0, len(choices))
	for _, choice := range choices {
		name, _ := llmProviderName(r, choice.Provider)
		out = append(out, llmModelChoice{
			ID:             choice.ID,
			Provider:       choice.Provider,
			ProviderName:   name,
			Model:          choice.Model,
			ModelName:      choice.ModelName,
			BaseURL:        choice.BaseURL,
			APIPath:        choice.APIPath,
			Endpoint:       turn.ModelEndpointLabel(choice.BaseURL, choice.APIPath, choice.URLVerbatim),
			Label:          choice.Label,
			Efforts:        choice.ReasoningEfforts,
			DefaultEffort:  choice.ConfiguredEffort,
			SupportsImages: choice.SupportsImages,
		})
	}
	writeJSON(w, out)
}

// handleLLMModelRefreshKey fetches a plan's key again for one subscription row
// — the user rotated it in the provider's console. A row that came from a
// sign-in without a key has nothing to fetch, which its subscription answers.
func (s *Server) handleLLMModelRefreshKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	scope, ok := s.llmModelScope(w, r)
	if !ok {
		return
	}
	stored, err := scope.store.Get(r.Context(), pathParam(r, "id"))
	if err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	sub, found := gatewaySubscriptionByID(stored.Source)
	if !found {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{
			"error": llmText(r, "这个模型不是订阅登录来的，没有可以重新获取的 Key",
				"this model did not come from a sign-in, so there is no key to fetch again"),
		})
		return
	}
	if err := sub.RefreshKey(r.Context(), s.subscriptionEnv(), stored.ID); err != nil {
		s.failSubscriptionWrite(w, r, err)
		return
	}
	if err := s.adoptLLMModelWrite(r.Context()); err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleLLMModelSetDefault makes one row the current agent's default model.
func (s *Server) handleLLMModelSetDefault(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	scope, ok := s.llmModelScope(w, r)
	if !ok {
		return
	}
	stored, err := scope.store.Get(r.Context(), pathParam(r, "id"))
	if err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	if err := process.SetAgentDefaultModel(r.Context(), s.Env.SQL, scope.agentID, stored.ID); err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	// The default is part of the composed configuration: without adopting it the
	// agent's own default would still be the row it ran on before this request.
	if err := s.adoptLLMModelWrite(r.Context()); err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"agent_id": scope.agentID, "default_model_id": stored.ID})
}

// handleLLMModelTest sends the smallest real request a configured model accepts
// and reports whether it came back (process.ProbeLLMModel). A failed probe is
// an answer, not an HTTP error: the page shows the sentence beside the button.
func (s *Server) handleLLMModelTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s == nil || s.Env == nil || s.Env.SQL == nil {
		http.Error(w, "no state database is bound", http.StatusServiceUnavailable)
		return
	}
	var body llmModelIdWrite
	if !decodeLLMModelBody(w, r, &body) {
		return
	}
	var stored *state.LLMModel
	if id := strings.TrimSpace(body.ID); id != "" {
		row, err := (&state.LLMModelStore{DB: s.Env.SQL}).Get(r.Context(), id)
		if err != nil {
			s.failLLMModelWrite(w, r, err)
			return
		}
		stored = &row
	}
	// The key stays in this process: a body that names no key tests with the
	// row's own, which never travels back out.
	if body.APIKey == nil && stored != nil {
		key := stored.APIKey
		body.APIKey = &key
	}
	row, err := s.llmModelFromWrite(r, body.llmModelWrite, stored)
	if err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	in := process.LLMModelInput{
		Provider:    row.Provider,
		Model:       row.Model,
		BaseURL:     row.BaseURL,
		APIPath:     row.APIPath,
		APIKey:      &row.APIKey,
		URLVerbatim: row.URLVerbatim,
	}
	if params := row.ParamsJSON; params != "" {
		in.ParamsJSON = &params
	}
	if err := process.ProbeLLMModel(r.Context(), in); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleLLMModelCatalogCaps answers what a model can do by default, for a form
// whose values may not be stored yet: the catalog's own values (the same ones a
// row's catalog_caps carries) plus the thinking levels the vendor's list or
// models.dev gives. efforts_known=false means no source lists them and the page
// offers the checkboxes instead.
//
// The form's endpoint travels with the request and is read for one thing only:
// the protocol decides which thinking levels can actually be sent, so the
// dropdown never offers a level the client drops (plan 311). Which *source*
// answers is still the subscription that owns the row and the provider and
// model, never the URL. The key is only ever in a body, because a key in a URL
// would land in an access log.
func (s *Server) handleLLMModelCatalogCaps(w http.ResponseWriter, r *http.Request) {
	var write llmModelWrite
	var id string
	switch r.Method {
	case http.MethodGet:
		query := r.URL.Query()
		write = llmModelWrite{
			Provider: query.Get("provider"),
			Model:    query.Get("model"),
			BaseURL:  query.Get("base_url"),
			APIPath:  query.Get("api_path"),
		}
		if value, present := query["catalog_provider"]; present {
			named := ""
			if len(value) > 0 {
				named = value[0]
			}
			write.CatalogProvider = &named
		}
		id = strings.TrimSpace(query.Get("id"))
	case http.MethodPost:
		var body llmModelIdWrite
		if !decodeLLMModelBody(w, r, &body) {
			return
		}
		write, id = body.llmModelWrite, strings.TrimSpace(body.ID)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	provider := strings.ToLower(strings.TrimSpace(write.Provider))
	model := strings.TrimSpace(write.Model)
	if provider == "" || model == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{
			"error": llmText(r, "请先填写服务商与模型名", "fill in the provider and the model first"),
		})
		return
	}
	var stored *state.LLMModel
	if id != "" {
		if s == nil || s.Env == nil || s.Env.SQL == nil {
			http.Error(w, "no state database is bound", http.StatusServiceUnavailable)
			return
		}
		row, err := (&state.LLMModelStore{DB: s.Env.SQL}).Get(r.Context(), id)
		if err != nil {
			s.failLLMModelWrite(w, r, err)
			return
		}
		stored = &row
	}
	source, key := "", ""
	if stored != nil {
		source, key = stored.Source, stored.APIKey
	}
	if write.APIKey != nil {
		key = strings.TrimSpace(*write.APIKey)
	}
	body := llmModelCapsBody{}
	apiPath := llmModelAPIPathOf(write, stored)
	body.llmModelCaps, body.CatalogID = llmModelCatalogCaps(provider, model, llmModelCatalogProviderOf(write, stored), apiPath)
	// The dropdown offers only what the row's protocol can send (plans 311, 318):
	// a custom Messages model must not show the levels its client drops.
	effortsSource := ""
	if info, err := process.ResolveModelInfo(r.Context(), provider, model, source, key); err == nil {
		body.Efforts = llmSendableEfforts(provider, model, apiPath, info.Efforts)
		body.DefaultEffort = info.DefaultEffort
		effortsSource = info.Origin
	}
	// efforts_known is "a source listed levels this build can send": a model
	// nothing lists levels for — or one whose levels this protocol cannot carry —
	// answers false, and the form offers the boxes to tick instead of a dropdown
	// built from an empty list (plan 318 F4).
	body.EffortsKnown = len(body.Efforts) > 0
	if body.EffortsKnown {
		body.EffortsSource = effortsSource
	}
	// A row no source lists but which declares its own levels answers from that
	// declaration, trimmed the same way, so the form's dropdown and the
	// terminal's page agree.
	if !body.EffortsKnown && stored != nil && stored.CapEfforts != nil {
		body.Efforts = llmSendableEfforts(provider, model, apiPath, stored.CapEfforts)
	}
	// Same rule as a row's own answer: an off switch the protocol cannot send is
	// not offered, so the dropdown never shows a level the save would refuse.
	if body.ReasoningOff != nil && !slices.Contains(body.Efforts, "none") {
		body.ReasoningOff = nil
	}
	writeJSON(w, body)
}

// handleLLMProviders serves the form's provider picker: the built-in registry in
// its display order, the protocols any model can be configured with, and the id
// the custom entry carries.
func (s *Server) handleLLMProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	registry := config.BuiltinProviders()
	providers := make([]llmProviderOption, 0, len(registry))
	for _, provider := range registry {
		option := llmProviderOption{
			ID:          provider.ID,
			Name:        llmText(r, provider.LabelZH, provider.Label),
			LoginBacked: provider.LoginBacked,
			Endpoints:   make([]llmProviderEndpoint, 0, len(provider.Endpoints)),
		}
		for _, endpoint := range provider.Endpoints {
			option.Endpoints = append(option.Endpoints, llmProviderEndpoint{
				Name:    llmText(r, endpoint.LabelZH, endpoint.Label),
				BaseURL: endpoint.BaseURL,
				APIPath: endpoint.APIPath,
			})
		}
		providers = append(providers, option)
	}
	writeJSON(w, map[string]any{
		"providers": providers,
		"protocols": []map[string]string{
			{"api_path": config.APIPathChatCompletions, "name": "OpenAI Chat Completions"},
			{"api_path": config.APIPathResponses, "name": "OpenAI Responses"},
			{"api_path": config.APIPathAnthropicMessages, "name": "Anthropic Messages"},
		},
		"custom_id": config.CustomProvider,
	})
}

// llmModelFromWrite validates one write and returns the row to store. stored is
// the row the request edits (nil on a create): a field the body leaves empty
// keeps its stored value, which is how the drawer that edits only the advanced
// settings sends no identity.
func (s *Server) llmModelFromWrite(r *http.Request, write llmModelWrite, stored *state.LLMModel) (state.LLMModel, error) {
	var row state.LLMModel
	if stored != nil {
		row = *stored
	}
	pick := func(in string, kept string) string {
		if value := strings.TrimSpace(in); value != "" {
			return value
		}
		return kept
	}
	provider := strings.ToLower(pick(write.Provider, row.Provider))
	model := pick(write.Model, row.Model)
	baseURL := pick(write.BaseURL, row.BaseURL)
	apiPath := pick(write.APIPath, row.APIPath)
	verbatim := row.URLVerbatim
	if write.URLVerbatim != nil {
		verbatim = *write.URLVerbatim
	}
	subagentAllowed := row.SubagentAllowed
	if write.SubagentAllowed != nil {
		subagentAllowed = *write.SubagentAllowed
	}

	// A sign-in-backed provider is added by signing in, never typed in
	// (plan 264/265). Everything else — a provider the form offers, the
	// custom one, or an id this build has no data for — is accepted as
	// typed: knowledge is a capability reference, not a permission gate
	// (owner 2026-11-19).
	custom := provider == config.CustomProvider
	if stored == nil && config.CredentialsBackedProvider(provider) {
		return row, llmRefuse(r, http.StatusBadRequest,
			"请通过订阅登录添加", "add this model by signing in to its subscription")
	}
	// 1. The identity a row cannot be without.
	if model == "" {
		return row, llmRefuse(r, http.StatusBadRequest, "请填写模型名", "a model name is required")
	}
	if baseURL == "" {
		return row, llmRefuse(r, http.StatusBadRequest, "请填写接口地址", "a base URL is required")
	}
	if !llmBaseURLLooksHTTP(baseURL) {
		return row, llmRefuse(r, http.StatusBadRequest,
			"接口地址必须是 http(s) 地址", "the base URL must be an http(s) URL")
	}
	// 2. The protocol. 「自定义协议」 sends to base_url exactly as stored, so
	// its path only names the request shape and is always Chat Completions;
	// only a custom model may use it. A custom model otherwise names one of the
	// three protocols; a built-in provider's path may be any path it serves.
	if verbatim {
		if !custom {
			return row, llmRefuse(r, http.StatusBadRequest,
				"只有自定义模型可以用自定义协议", "only a custom model can use a custom protocol")
		}
		apiPath = config.APIPathChatCompletions
	}
	// A custom model names its protocol: normalization fills a default path in,
	// so an absent one has to be refused before it can be completed.
	if custom && !verbatim && strings.TrimSpace(apiPath) == "" {
		return row, llmRefuse(r, http.StatusBadRequest,
			"请选择接口协议", "choose the API protocol")
	}
	provider, model, baseURL, apiPath = config.NormalizeLLMModelIdentity(provider, model, baseURL, apiPath)
	if custom && !verbatim && !isLLMProtocolPath(apiPath) {
		return row, llmRefuse(r, http.StatusBadRequest,
			"请选择接口协议", "choose the API protocol")
	}
	// 3. Request parameters are the provider's own fields, merged into the
	// request: anything but a JSON object would be a body nothing can send.
	params := row.ParamsJSON
	if write.Params != nil {
		params = strings.TrimSpace(*write.Params)
	}
	if _, err := config.ParseLLMRequestParamsJSON(params); err != nil {
		return row, llmRefuse(r, http.StatusBadRequest,
			"请求参数必须是 JSON 对象", "the request parameters must be a JSON object")
	}
	// 4. The capability overrides. The levels, the default level and the off
	// switch are the user's assertion, stored as asked apart from the
	// vocabulary whitelist above and the positive-number checks below (owner
	// 2026-11-19). A level this row's client cannot send is silently dropped
	// on the way out; the remote endpoint decides.
	caps := llmModelCapsOfRow(row)
	if write.Caps != nil {
		caps = *write.Caps
	}
	if err := s.llmModelCapsValid(r, provider, model, apiPath, caps, row); err != nil {
		return row, err
	}
	// 5. The provider a custom model's official data lives under (owner
	// 2026-11-16). Only a custom row may name one — a built-in row's data is
	// its own pair — and a row that is not custom carries none.
	catalogProvider := row.CatalogProvider
	if write.CatalogProvider != nil {
		catalogProvider = llmModelCatalogProvider(*write.CatalogProvider)
		if catalogProvider != "" && !custom {
			return row, llmRefuse(r, http.StatusBadRequest,
				"只有自定义模型可以指定数据服务商", "only a custom model names the provider its official data lives under")
		}
	}
	if !custom {
		catalogProvider = ""
	}

	key := row.APIKey
	if write.APIKey != nil {
		key = strings.TrimSpace(*write.APIKey)
	}
	row = state.LLMModel{
		ID:               row.ID,
		Provider:         provider,
		Model:            model,
		BaseURL:          baseURL,
		APIPath:          apiPath,
		APIKey:           key,
		ParamsJSON:       params,
		CapImages:        caps.Images,
		CapPDF:           caps.PDF,
		CapVideo:         caps.Video,
		CapAudio:         caps.Audio,
		CapReasoning:     caps.Reasoning,
		CapReasoningOff:  caps.ReasoningOff,
		CapEfforts:       caps.Efforts,
		CapDefaultEffort: caps.DefaultEffort,
		CapContextWindow: caps.ContextWindow,
		CapMaxOutput:     caps.MaxOutput,
		Source:           row.Source,
		SourcePlan:       row.SourcePlan,
		URLVerbatim:      verbatim,
		SubagentAllowed:  subagentAllowed,
		CatalogProvider:  catalogProvider,
		CreatedAt:        row.CreatedAt,
	}
	// A row a subscription wrote is recognised by its sign-in: its model, its
	// endpoint and its key come from that plan, so only the advanced settings
	// are editable here. Changing them means signing in and choosing again.
	if stored != nil && strings.TrimSpace(stored.Source) != "" {
		if row.Provider != stored.Provider || row.Model != stored.Model ||
			row.BaseURL != stored.BaseURL || row.APIPath != stored.APIPath ||
			row.APIKey != stored.APIKey || row.URLVerbatim != stored.URLVerbatim ||
			row.CatalogProvider != stored.CatalogProvider {
			return row, llmRefuse(r, http.StatusBadRequest,
				"订阅来的模型只能改高级配置；要换模型或协议请在订阅登录里重新选择",
				"a model that came from a sign-in can only change its advanced settings; pick another model or protocol by signing in again")
		}
	}
	return row, nil
}

// llmModelCapsValid checks one row's capability overrides (rule 6 of plan 265).
func (s *Server) llmModelCapsValid(r *http.Request, provider, model, apiPath string, caps llmModelCaps, stored state.LLMModel) error {
	for _, level := range caps.Efforts {
		if !slices.Contains(llmEffortLevels, strings.ToLower(strings.TrimSpace(level))) {
			return llmRefuse(r, http.StatusBadRequest,
				"思考强度只能是极低、低、中、高、最高、极致",
				"a thinking level must be minimal, low, medium, high, xhigh or max")
		}
	}
	// Levels and the default level are the user's assertion, not the
	// catalog's (owner 2026-11-19): apart from the vocabulary check above
	// and the numeric checks below, this build stores what the write asks
	// for. A level the client cannot send is silently dropped on the way
	// out; the remote endpoint decides whether a request succeeds.
	if caps.ContextWindow != nil && *caps.ContextWindow <= 0 {
		return llmRefuse(r, http.StatusBadRequest, "上下文窗口必须是正整数", "the context window must be a positive number")
	}
	if caps.MaxOutput != nil && *caps.MaxOutput <= 0 {
		return llmRefuse(r, http.StatusBadRequest, "最大输出必须是正整数", "the output limit must be a positive number")
	}
	return nil
}

// isLLMProtocolPath reports whether apiPath is one of the three protocols a
// custom model can be configured with.
func isLLMProtocolPath(apiPath string) bool {
	switch apiPath {
	case config.APIPathChatCompletions, config.APIPathResponses, config.APIPathAnthropicMessages:
		return true
	default:
		return false
	}
}

// llmBaseURLLooksHTTP reports whether raw is an absolute http(s) URL naming a
// host: an endpoint the runtime cannot dial is a mistake worth catching while
// the person is looking at the form.
func llmBaseURLLooksHTTP(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	return strings.TrimSpace(parsed.Host) != ""
}

// decodeLLMModelBody reads one JSON body, answering a malformed one itself.
func decodeLLMModelBody(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(into); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{
			"error": llmText(r, "请求格式不正确", "the request body is not valid JSON"),
		})
		return false
	}
	return true
}

// failLLMModelWrite answers a failed models-page write the way its cause reads:
// the refusal's own status and sentence, a duplicate as 409 naming the row that
// already has this identity, a missing row as 404, anything else as the store
// failing.
func (s *Server) failLLMModelWrite(w http.ResponseWriter, r *http.Request, err error) {
	var refusal *llmModelRefusal
	if errors.As(err, &refusal) {
		body := map[string]any{"error": refusal.msg}
		for field, value := range refusal.extra {
			body[field] = value
		}
		writeJSONStatus(w, refusal.status, body)
		return
	}
	var exists state.ErrLLMModelExists
	if errors.As(err, &exists) {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error": llmText(r, "相同的服务商、模型、接口地址与路径已经配置过",
				"this provider, model, base URL and path are already configured"),
			"existing_id": exists.ID,
		})
		return
	}
	if errors.Is(err, state.ErrLLMModelNotFound) {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{
			"error": llmText(r, "这个模型已经不在配置里了", "this model is no longer configured"),
		})
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
