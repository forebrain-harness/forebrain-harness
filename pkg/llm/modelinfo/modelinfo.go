package modelinfo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
)

// Row is the part of a configured model a live lookup reads. pkg/process
// projects one fb_llm_models row into it; the full row type lives in
// pkg/config, which this package may not import (it sits above llm).
type Row struct {
	Provider string
	Model    string
}

// Info is one live answer about a model. A nil Efforts means "not known": the
// surface then offers the user the levels to pick from. It is the only
// fall-back — there is no third source.
type Info struct {
	Efforts       []string
	DefaultEffort string
	ContextWindow int64
	MaxOutput     int64
	Images        *bool
	// Origin names the source that answered: "chatgpt-api", "ark-api" or
	// "models.dev". A surface labels the data with it; it is never shown raw.
	Origin string
}

// ErrNotListed reports that the source answered and does not list the model (or
// lists it without reasoning levels). It is not a failure: it is the "user
// picks the levels" answer, exactly like an unreachable source.
var ErrNotListed = errors.New("model not listed")

// Source answers the reasoning levels for one configured model, live.
type Source interface {
	// Applies reports whether this source handles the row. subscription is
	// the subscription that wrote the row (fb_llm_models.source), empty for a
	// row the user typed in.
	Applies(row Row, subscription string) bool
	// Fetch asks the source. The error is for the log, never for the user.
	Fetch(ctx context.Context, row Row, apiKey string) (Info, error)
}

// SourceTimeout bounds one source's call. It is per source, not per resolve:
// a subscription API that hangs must not eat the models.dev fall-back's time.
const SourceTimeout = 10 * time.Second

// Resolver answers rows through a fixed precedence chain. The zero value is
// usable; New installs the production chain.
type Resolver struct {
	Sources []Source
	// Timeout overrides SourceTimeout; zero means SourceTimeout.
	Timeout time.Duration
}

// New returns the production resolver: the two subscription APIs first, then
// models.dev for everything they do not own.
func New() *Resolver {
	return &Resolver{Sources: []Source{ChatGPTSource{}, ArkSource{}, &ModelsDevSource{}}}
}

// Resolve answers one row. The first source that applies is asked first; a
// source that fails falls through to the next one that applies (models.dev
// applies to everything), so a subscription API outage still resolves through
// the general catalog. The returned error is the last failure, or
// ErrNotListed when every source answered without listing the model.
func (r *Resolver) Resolve(ctx context.Context, row Row, subscription, apiKey string) (Info, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(row.Provider) == "" || strings.TrimSpace(row.Model) == "" {
		return Info{}, ErrNotListed
	}
	sources := r.Sources
	if len(sources) == 0 {
		sources = New().Sources
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = SourceTimeout
	}
	var (
		err       error = ErrNotListed
		firstFail error
		asked     bool
	)
	for _, source := range sources {
		if source == nil || !source.Applies(row, subscription) {
			continue
		}
		asked = true
		sourceCtx, cancel := context.WithTimeout(ctx, timeout)
		info, ferr := source.Fetch(sourceCtx, row, apiKey)
		cancel()
		if ferr == nil {
			return info, nil
		}
		// The owning source's failure is the one worth logging: "the
		// subscription API answered 401" says more than the general
		// catalog's "not listed" that follows it.
		if firstFail == nil && !errors.Is(ferr, ErrNotListed) {
			firstFail = ferr
		}
		err = ferr
	}
	if !asked {
		return Info{}, ErrNotListed
	}
	if firstFail != nil {
		return Info{}, firstFail
	}
	return Info{}, err
}

// ChatGPTSource answers a ChatGPT subscription row from the account's own model
// list, the one the picker already reads (supported_reasoning_levels and
// default_reasoning_level).
type ChatGPTSource struct {
	// Path is the credentials path; "" means the process-wide login.
	Path string
	// BaseURL is the Codex backend root; "" means openai.CodexBaseURL.
	BaseURL string
	// List returns the account's models. nil performs the real call; a test
	// injects one so nothing reaches the network.
	List func(ctx context.Context) ([]openai.ModelInfo, error)
}

func (s ChatGPTSource) Applies(row Row, subscription string) bool {
	return matchesSubscriptionOrProvider(subscription, row.Provider, "chatgpt")
}

func (s ChatGPTSource) Fetch(ctx context.Context, row Row, apiKey string) (Info, error) {
	list := s.List
	if list == nil {
		list = s.fetchModels
	}
	models, err := list(ctx)
	if err != nil {
		return Info{}, err
	}
	model := strings.TrimSpace(row.Model)
	for _, m := range models {
		if !strings.EqualFold(strings.TrimSpace(m.Slug), model) {
			continue
		}
		info := Info{
			DefaultEffort: strings.TrimSpace(m.DefaultReasoningEffort),
			Origin:        "chatgpt-api",
		}
		for _, preset := range m.SupportedReasoningEfforts {
			if effort := strings.TrimSpace(preset.Effort); effort != "" {
				info.Efforts = append(info.Efforts, effort)
			}
		}
		if m.ContextWindow != nil {
			info.ContextWindow = *m.ContextWindow
		}
		images := m.SupportsAttachments()
		info.Images = &images
		return info, nil
	}
	return Info{}, ErrNotListed
}

// fetchModels is the real call: the account's login, the codex version the
// backend gates its list on, then the list itself.
func (s ChatGPTSource) fetchModels(ctx context.Context) ([]openai.ModelInfo, error) {
	if _, err := openai.LoadProcessCredentials(ctx, s.Path); err != nil {
		return nil, fmt.Errorf("chatgpt login required: %w", err)
	}
	version, err := openai.ResolveClientVersion(ctx)
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimSpace(s.BaseURL)
	if baseURL == "" {
		baseURL = openai.CodexBaseURL
	}
	transport := &openai.Transport{Path: s.Path}
	return openai.FetchModels(ctx, transport, baseURL, version)
}

// ArkSource answers a Volcengine Ark row from the plan data plane's model list,
// which carries each model's levels, token limits and input modalities.
type ArkSource struct {
	// BaseURL is the plan data plane's root; "" means the Coding Plan host.
	BaseURL string
}

// ArkCodingBaseURL is the Coding Plan data plane. Its /v3/models list is the
// same table the Agent Plan serves (measured 2026-11-07), and any valid plan
// key reads it. Copied rather than imported from pkg/config, which sits above
// this package.
const ArkCodingBaseURL = "https://ark.cn-beijing.volces.com/api/coding"

func (s ArkSource) Applies(row Row, subscription string) bool {
	return matchesSubscriptionOrProvider(subscription, row.Provider, "volcengine")
}

// arkModelsResponse is the subset of GET <base>/v3/models this package reads.
// Only models that reason carry effort; image/video/embedding models carry a
// null one.
type arkModelsResponse struct {
	Data []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Effort *struct {
			SupportedLevels []string `json:"supported_levels"`
			DefaultLevel    string   `json:"default_level"`
		} `json:"effort"`
		TokenLimits struct {
			ContextWindow        int64 `json:"context_window"`
			MaxOutputTokenLength int64 `json:"max_output_token_length"`
		} `json:"token_limits"`
		Modalities struct {
			InputModalities []string `json:"input_modalities"`
		} `json:"modalities"`
	} `json:"data"`
}

func (s ArkSource) Fetch(ctx context.Context, row Row, apiKey string) (Info, error) {
	if strings.TrimSpace(apiKey) == "" {
		return Info{}, fmt.Errorf("ark: no plan key on the row")
	}
	base := strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	if base == "" {
		base = ArkCodingBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v3/models", nil)
	if err != nil {
		return Info{}, fmt.Errorf("ark models: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	req.Header.Set("Accept", "application/json")
	resp, err := llm.NewHTTPClient().Do(req)
	if err != nil {
		return Info{}, fmt.Errorf("ark models: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes))
	if err != nil {
		return Info{}, fmt.Errorf("ark models: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Info{}, fmt.Errorf("ark models: http %d", resp.StatusCode)
	}
	var parsed arkModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Info{}, fmt.Errorf("ark models: decode response: %w", err)
	}
	model := strings.TrimSpace(row.Model)
	for _, entry := range parsed.Data {
		// The row's model name may be either the version id or the base
		// name, so both are matched (2026-11-07: the list carries both).
		if !strings.EqualFold(strings.TrimSpace(entry.ID), model) && !strings.EqualFold(strings.TrimSpace(entry.Name), model) {
			continue
		}
		info := Info{
			ContextWindow: entry.TokenLimits.ContextWindow,
			MaxOutput:     entry.TokenLimits.MaxOutputTokenLength,
			Origin:        "ark-api",
		}
		if entry.Effort != nil {
			info.DefaultEffort = strings.TrimSpace(entry.Effort.DefaultLevel)
			info.Efforts = normalizeLevels(entry.Effort.SupportedLevels)
		}
		images := containsFold(entry.Modalities.InputModalities, "image")
		info.Images = &images
		return info, nil
	}
	return Info{}, ErrNotListed
}

// ModelsDevSource answers every model neither subscription owns — OpenCode's
// subscriptions, the built-in vendors, and custom models — from the catalog this
// build ships beside its binary. It is the same data models.dev's document is
// synchronized from, read as a file and never fetched (owner 2026-11-09), so
// the answer is the same on every machine of a build and no row can wait on a
// network.
type ModelsDevSource struct{}

// maxCatalogBytes bounds a catalog-shaped response body. The real models.dev
// document measured 6.5MB; the bound exists so a broken or hostile response
// cannot exhaust memory. It bounds the Ark plan list, the one source that still
// reads a response body.
const maxCatalogBytes = 64 << 20

// Applies answers true for every row: this is the general source, the one a
// subscription API's failure falls back to.
func (s ModelsDevSource) Applies(row Row, subscription string) bool { return true }

func (s ModelsDevSource) Fetch(ctx context.Context, row Row, apiKey string) (Info, error) {
	hit, ok := llm.LookupCatalog(strings.TrimSpace(row.Provider), strings.TrimSpace(row.Model))
	if !ok {
		return Info{}, ErrNotListed
	}
	if strings.EqualFold(strings.TrimSpace(hit.Status), "deprecated") {
		// The catalog has retired the model: its levels are no longer a
		// promise about anything.
		return Info{}, ErrNotListed
	}
	info := Info{
		ContextWindow: hit.ContextWindow,
		MaxOutput:     hit.DefaultMaxTokens,
		Origin:        "models.dev",
	}
	if levels, known := hit.ReasoningEfforts(); known {
		// A row that reasons with nothing discrete (a toggle, a token budget)
		// answers with no levels: the surface then offers the checkboxes, exactly
		// as it did when this answer came from the upstream document.
		info.Efforts = levels
	}
	images := hit.SupportsAttachments
	info.Images = &images
	return info, nil
}

// normalizeLevels trims, lowercases and de-duplicates a provider's list,
// keeping its order.
func normalizeLevels(values []string) []string {
	var out []string
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		level := strings.ToLower(strings.TrimSpace(value))
		if level == "" || seen[level] {
			continue
		}
		seen[level] = true
		out = append(out, level)
	}
	return out
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}

// matchesSubscriptionOrProvider reports whether a source owns a row: the
// subscription that wrote it, or the provider it names.
func matchesSubscriptionOrProvider(subscription, provider, id string) bool {
	if strings.EqualFold(strings.TrimSpace(subscription), id) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(provider), id)
}
