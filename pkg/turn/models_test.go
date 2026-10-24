package turn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
	"github.com/stretchr/testify/require"
)

func TestListModelCatalogReturnsStableRecords(t *testing.T) {
	records := ListModelCatalog(ModelCatalogQuery{Query: "gpt", Limit: 10})

	require.NotEmpty(t, records)
	require.LessOrEqual(t, len(records), 10)
	for _, rec := range records {
		require.NotEmpty(t, rec.ModelID)
		require.NotEmpty(t, rec.Provider)
		require.NotEmpty(t, rec.APIModel)
	}
}

func TestListModelCatalogFiltersProvider(t *testing.T) {
	records := New(nil).ListModelCatalog(ModelCatalogQuery{Provider: "openai", Limit: 20})

	require.NotEmpty(t, records)
	for _, rec := range records {
		require.Equal(t, "openai", rec.Provider)
	}
}

type stubChatGPTSource struct {
	models []openai.ModelInfo
	err    error
	calls  int
}

func (s *stubChatGPTSource) ChatGPTModels(ctx context.Context) ([]openai.ModelInfo, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.models, nil
}

// The projection is the shared contract every surface displays: hidden entries
// dropped, backend priority order (stable on ties), first entry preselectable,
// reasoning levels and limits carried through, subscription-only models kept.
func TestDiscoverChatGPTModelsProjectsAccountModels(t *testing.T) {
	contextWindow := int64(272000)
	src := &stubChatGPTSource{models: []openai.ModelInfo{
		{Slug: "gpt-hidden", Visibility: "hide", Priority: 0},
		{Slug: "gpt-legacy", Visibility: "list", Priority: 30,
			SupportedReasoningEfforts: []openai.ReasoningEffortPreset{{Effort: "low"}, {Effort: "medium"}}},
		{Slug: "gpt-beta", Visibility: "list", Priority: 7,
			InputModalities: []string{"text", "image"}, ContextWindow: &contextWindow,
			DefaultReasoningEffort: "max", SupportedInAPI: false,
			SupportedReasoningEfforts: []openai.ReasoningEffortPreset{
				{Effort: "low"}, {Effort: "max", Description: "deepest"}, {Effort: ""}}},
		{Slug: "gpt-alpha", Visibility: "list", Priority: 7},
	}}

	records, err := DiscoverChatGPTModels(context.Background(), src)
	require.NoError(t, err)
	require.Len(t, records, 3)

	require.Equal(t, "gpt-beta", records[0].APIModel)
	require.True(t, records[0].IsDefault, "first visible in priority order is the preselectable default")
	require.Equal(t, "chatgpt/gpt-beta", records[0].ModelID)
	require.Equal(t, "chatgpt", records[0].Provider)
	require.Equal(t, int64(272000), records[0].ContextWindow)
	require.True(t, records[0].SupportsAttachments)
	require.True(t, records[0].CanReason)
	require.Equal(t, []string{"low", "max"}, records[0].ReasoningEfforts, "empty effort labels are dropped")
	require.Equal(t, "max", records[0].DefaultReasoningEffort)
	require.Equal(t, ModelSourceChatGPTAccount, records[0].Source)
	require.False(t, records[0].FetchedAt.IsZero())

	require.Equal(t, "gpt-alpha", records[1].APIModel, "equal priorities keep backend order")
	require.False(t, records[1].IsDefault)
	require.False(t, records[1].CanReason, "no effort levels means not known to reason")

	require.Equal(t, "gpt-legacy", records[2].APIModel)
	require.Equal(t, ModelSourceChatGPTAccount, records[2].Source)
}

func TestDiscoverChatGPTModelsKeepsSubscriptionOnlyModels(t *testing.T) {
	src := &stubChatGPTSource{models: []openai.ModelInfo{
		{Slug: "gpt-subscription-only", Visibility: "list", Priority: 1, SupportedInAPI: false},
	}}
	records, err := DiscoverChatGPTModels(context.Background(), src)
	require.NoError(t, err)
	require.Len(t, records, 1, "supported_in_api=false must not hide a model from a ChatGPT-authenticated picker")
}

func TestDiscoverChatGPTModelsRequiresASource(t *testing.T) {
	_, err := DiscoverChatGPTModels(context.Background(), nil)
	require.Error(t, err)
}

func TestDiscoverChatGPTModelsPropagatesFailure(t *testing.T) {
	src := &stubChatGPTSource{err: errors.New("fetch ChatGPT models: http 401: token expired")}
	_, err := DiscoverChatGPTModels(context.Background(), src)
	require.ErrorContains(t, err, "401")
}

func useStaticCatalog(t *testing.T) {
	t.Helper()
	cat, err := llm.Parse([]byte(`{
		"openai/gpt-static": {"name": "GPT Static", "reasoning": true, "attachment": false,
			"limit": {"context": 128000, "output": 16384}}
	}`))
	require.NoError(t, err)
	llm.SetGlobalCatalogForTest(cat)
	if data, err := llm.RepoModelsJSONForTest(); err == nil {
		if embedded, err := llm.Parse(data); err == nil {
			t.Cleanup(func() { llm.SetGlobalCatalogForTest(embedded) })
		}
	}
}

// installRepoCatalog installs the repository's own assets/models.json as the
// global catalog and puts it back afterwards. A test that asserts real
// capability values uses this instead of its own fixture: the asset is the data
// every surface reads, so the assertion is about shipped behaviour.
func installRepoCatalog(t *testing.T) {
	t.Helper()
	data, err := llm.RepoModelsJSONForTest()
	require.NoError(t, err)
	cat, err := llm.Parse(data)
	require.NoError(t, err)
	llm.SetGlobalCatalogForTest(cat)
	t.Cleanup(func() { llm.SetGlobalCatalogForTest(cat) })
}

// A chatgpt-scoped query must not drag other providers' static records in.
func TestListModelCatalogLiveChatGPTScopeHasNoStaticLeakage(t *testing.T) {
	useStaticCatalog(t)
	src := &stubChatGPTSource{models: []openai.ModelInfo{
		{Slug: "gpt-live", Visibility: "list", Priority: 1},
	}}
	listing := ListModelCatalogLive(context.Background(), src, ModelCatalogQuery{Provider: "chatgpt"})
	require.Len(t, listing.Records, 1)
	require.Equal(t, "chatgpt", listing.Records[0].Provider)
	require.Len(t, listing.Status, 1)
	require.Equal(t, "chatgpt", listing.Status[0].Provider)
	require.Equal(t, ModelSourceChatGPTAccount, listing.Status[0].Source)
	require.NotNil(t, listing.Status[0].FetchedAt)
	require.False(t, listing.Status[0].FetchedAt.IsZero())
	require.Empty(t, listing.Status[0].Error)
}

// An open query merges the static catalog with live ChatGPT records; the
// needle and the limit apply across both blocks.
func TestListModelCatalogLiveMergesStaticAndDiscovered(t *testing.T) {
	useStaticCatalog(t)
	src := &stubChatGPTSource{models: []openai.ModelInfo{
		{Slug: "gpt-live", Visibility: "list", Priority: 1},
	}}
	listing := ListModelCatalogLive(context.Background(), src, ModelCatalogQuery{Query: "live", Limit: 5})
	require.Len(t, listing.Records, 1)
	require.Equal(t, "chatgpt/gpt-live", listing.Records[0].ModelID)

	listing = ListModelCatalogLive(context.Background(), src, ModelCatalogQuery{})
	require.Len(t, listing.Records, 2)
	require.Equal(t, "openai", listing.Records[0].Provider, "static block stays first")
	require.Equal(t, "chatgpt", listing.Records[1].Provider, "discovered block follows in priority order")

	listing = ListModelCatalogLive(context.Background(), src, ModelCatalogQuery{Limit: 1})
	require.Len(t, listing.Records, 1)
}

// A failed discovery must never read as an empty catalog: static records stay
// and the failure is named per provider.
func TestListModelCatalogLiveDiscoveryFailureKeepsStaticRecords(t *testing.T) {
	useStaticCatalog(t)
	src := &stubChatGPTSource{err: errors.New("fetch ChatGPT models: http 401: token expired")}
	listing := ListModelCatalogLive(context.Background(), src, ModelCatalogQuery{})
	require.Len(t, listing.Records, 1)
	require.Equal(t, "openai", listing.Records[0].Provider)
	require.Len(t, listing.Status, 1)
	require.Empty(t, listing.Status[0].Source)
	require.Contains(t, listing.Status[0].Error, "401")
}

// Other providers must never pay for ChatGPT discovery.
func TestListModelCatalogLiveOtherProvidersSkipDiscovery(t *testing.T) {
	useStaticCatalog(t)
	src := &stubChatGPTSource{}
	listing := ListModelCatalogLive(context.Background(), src, ModelCatalogQuery{Provider: "openai"})
	require.Len(t, listing.Records, 1)
	require.Equal(t, "openai", listing.Records[0].Provider)
	require.Zero(t, src.calls)
	require.Empty(t, listing.Status)
}

// Without an installed source the service still answers from the static
// catalog and reports ChatGPT as unconfigured instead of guessing.
func TestServiceListModelCatalogLiveWithoutSource(t *testing.T) {
	useStaticCatalog(t)
	svc := New(nil)
	listing := svc.ListModelCatalogLive(context.Background(), ModelCatalogQuery{Provider: "chatgpt"})
	require.Empty(t, listing.Records)
	require.Len(t, listing.Status, 1)
	require.NotEmpty(t, listing.Status[0].Error)
}

// Two sources answer for their own accounts with no shared state between
// calls: the second login must fully replace the first listing.
func TestDiscoverChatGPTModelsHasNoCrossSourceState(t *testing.T) {
	first := &stubChatGPTSource{models: []openai.ModelInfo{
		{Slug: "gpt-first", Visibility: "list", Priority: 1},
	}}
	second := &stubChatGPTSource{models: []openai.ModelInfo{
		{Slug: "gpt-second", Visibility: "list", Priority: 1},
	}}
	one, err := DiscoverChatGPTModels(context.Background(), first)
	require.NoError(t, err)
	two, err := DiscoverChatGPTModels(context.Background(), second)
	require.NoError(t, err)
	require.Equal(t, "gpt-first", one[0].APIModel)
	require.Equal(t, "gpt-second", two[0].APIModel)
}

// stubOpenCodeSource stands in for the live OpenCode catalog. It records which
// surface was asked so a listing that should not spend a request can be
// caught doing it.
type stubOpenCodeSource struct {
	console      []opencode.ModelInfo
	goCatalog    []opencode.ModelInfo
	err          error
	signedIn     bool
	consoleCalls int
	goCalls      int
}

func (s *stubOpenCodeSource) OpenCodeModels(ctx context.Context, goSubscription bool) ([]opencode.ModelInfo, error) {
	if goSubscription {
		s.goCalls++
	} else {
		s.consoleCalls++
	}
	if s.err != nil {
		return nil, s.err
	}
	if goSubscription {
		return s.goCatalog, nil
	}
	return s.console, nil
}

func (s *stubOpenCodeSource) OpenCodeSignedIn() bool { return s.signedIn }

// The OpenCode projection drops the systemone ids (not chat models), keeps the
// catalog's order and marks the first as the preselected entry. Metadata
// the live catalog never reports comes from the package's own table, with the
// conservative default for an id the table has not seen.
func TestDiscoverOpenCodeModelsProjectsConsoleCatalog(t *testing.T) {
	src := &stubOpenCodeSource{console: []opencode.ModelInfo{
		{ID: "jev-decision"},
		{ID: "deepseek-v4-pro"},
		{ID: "qwen3.8-flash"},
		{ID: "glm-5.3-flash-free"},
		{ID: "gpt-7-unseen"},
	}}

	records, err := DiscoverOpenCodeModels(context.Background(), src, false)
	require.NoError(t, err)
	require.Len(t, records, 4, "the systemone id is not a chat model")

	require.Equal(t, "deepseek-v4-pro", records[0].APIModel, "the catalog's order is kept")
	require.True(t, records[0].IsDefault)
	require.Equal(t, "opencode/deepseek-v4-pro", records[0].ModelID)
	require.Equal(t, "opencode", records[0].Provider)
	require.Equal(t, ModelSourceOpenCodeAccount, records[0].Source)
	require.False(t, records[0].FetchedAt.IsZero())

	byID := make(map[string]ModelRecord, len(records))
	for _, rec := range records {
		byID[rec.APIModel] = rec
	}
	// Capabilities come from models.json's row for this surface: qwen3.8-flash
	// has a 1M window there instead of one conservative default.
	qwen := byID["qwen3.8-flash"]
	require.Equal(t, int64(1000000), qwen.ContextWindow)
	require.True(t, qwen.CanReason, "the catalog records qwen3.8's reasoning output")
	require.False(t, qwen.IsDefault)

	unseen := byID["gpt-7-unseen"]
	require.Equal(t, int64(128000), unseen.ContextWindow, "an unknown id gets the conservative window")
	require.Equal(t, "opencode/gpt-7-unseen", unseen.ModelID)

	require.Equal(t, 1, src.consoleCalls)
	require.Zero(t, src.goCalls)
}

// The Go subscription reads its own table: the same id can carry a different
// window there, and the provider prefix keeps the two surfaces apart.
func TestDiscoverOpenCodeModelsUsesTheGoTable(t *testing.T) {
	src := &stubOpenCodeSource{goCatalog: []opencode.ModelInfo{
		{ID: "qwen3.8-max"},
		{ID: "glm-5.3"},
	}}
	records, err := DiscoverOpenCodeModels(context.Background(), src, true)
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, "opencode-go/qwen3.8-max", records[0].ModelID)
	require.Equal(t, "opencode-go", records[0].Provider)
	require.True(t, records[0].CanReason)
	require.True(t, records[0].IsDefault)
	require.False(t, records[1].IsDefault, "only the first entry is preselectable")
	require.Equal(t, 1, src.goCalls)
	require.Zero(t, src.consoleCalls)
}

func TestDiscoverOpenCodeModelsRequiresASource(t *testing.T) {
	_, err := DiscoverOpenCodeModels(context.Background(), nil, false)
	require.Error(t, err)
}

func TestDiscoverOpenCodeModelsPropagatesFailure(t *testing.T) {
	src := &stubOpenCodeSource{err: errors.New("fetch OpenCode models: http 401")}
	_, err := DiscoverOpenCodeModels(context.Background(), src, false)
	require.ErrorContains(t, err, "401")
}

// A console-scoped query answers from the live catalog alone: no static
// records leak in, and the ChatGPT source is not asked.
func TestListModelCatalogLiveOpenCodeConsoleScope(t *testing.T) {
	useStaticCatalog(t)
	chatgptSrc := &stubChatGPTSource{}
	opencodeSrc := &stubOpenCodeSource{console: []opencode.ModelInfo{{ID: "big-pickle"}, {ID: "deepseek-v4-pro"}}}
	listing := listModelCatalogLive(context.Background(),
		modelSources{chatgpt: chatgptSrc, opencode: opencodeSrc},
		ModelCatalogQuery{Provider: "opencode"})

	require.Len(t, listing.Records, 2)
	for _, rec := range listing.Records {
		require.Equal(t, "opencode", rec.Provider)
	}
	require.Len(t, listing.Status, 1)
	require.Equal(t, "opencode", listing.Status[0].Provider)
	require.Equal(t, ModelSourceOpenCodeAccount, listing.Status[0].Source)
	require.NotNil(t, listing.Status[0].FetchedAt)
	require.Empty(t, listing.Status[0].Error)
	require.Zero(t, chatgptSrc.calls, "an opencode-scoped query must not pay for ChatGPT discovery")
	require.Zero(t, opencodeSrc.goCalls)
}

// The Go catalog needs the subscription token: without a login the listing
// reports it instead of spending a request to learn the same thing from a 401.
func TestListModelCatalogLiveSkipsGoCatalogWithoutLogin(t *testing.T) {
	useStaticCatalog(t)
	opencodeSrc := &stubOpenCodeSource{signedIn: false, goCatalog: []opencode.ModelInfo{{ID: "glm-5.3"}}}
	listing := listModelCatalogLive(context.Background(),
		modelSources{opencode: opencodeSrc},
		ModelCatalogQuery{Provider: "opencode-go"})

	require.Empty(t, listing.Records)
	require.Len(t, listing.Status, 1)
	require.Equal(t, "opencode-go", listing.Status[0].Provider)
	require.Empty(t, listing.Status[0].Source)
	require.Equal(t, "not signed in", listing.Status[0].Error)
	require.Zero(t, opencodeSrc.goCalls, "no credential means no request")
}

// A listing that names no provider asks every source this machine could hold
// an account on. The Console catalog is read with a public token, so contacting
// it without a login spends a request to learn what the login file already
// says — the web providers page opens such a listing every time it is shown.
func TestListModelCatalogLiveUnscopedSkipsConsoleWithoutLogin(t *testing.T) {
	useStaticCatalog(t)
	opencodeSrc := &stubOpenCodeSource{signedIn: false, console: []opencode.ModelInfo{{ID: "big-pickle"}}}
	listing := listModelCatalogLive(context.Background(),
		modelSources{opencode: opencodeSrc},
		ModelCatalogQuery{})

	require.Zero(t, opencodeSrc.consoleCalls, "no login means no Console request")
	var goStatus *ModelCatalogStatus
	for i, status := range listing.Status {
		require.NotEqual(t, "opencode", status.Provider, "a Console block that was not asked is not reported")
		if status.Provider == "opencode-go" {
			goStatus = &listing.Status[i]
		}
	}
	for _, rec := range listing.Records {
		require.False(t, strings.HasPrefix(rec.ModelID, "opencode/"),
			"%s was answered by a catalog that was never fetched", rec.ModelID)
	}
	// The Go subscription is still reported, because it is a surface the user
	// can sign in to and the listing says why it is empty.
	require.NotNil(t, goStatus, "the Go subscription is still reported")
	require.Equal(t, "not signed in", goStatus.Error)
	require.Zero(t, opencodeSrc.goCalls)
}

// A query that names "opencode" is an explicit ask for the Console catalog, so
// it still reads it without a login: those models need no account.
func TestListModelCatalogLiveScopedConsoleStillFetchesWithoutLogin(t *testing.T) {
	useStaticCatalog(t)
	opencodeSrc := &stubOpenCodeSource{signedIn: false, console: []opencode.ModelInfo{{ID: "big-pickle"}}}
	listing := listModelCatalogLive(context.Background(),
		modelSources{opencode: opencodeSrc},
		ModelCatalogQuery{Provider: "opencode"})

	require.Equal(t, 1, opencodeSrc.consoleCalls)
	require.Len(t, listing.Records, 1)
	require.Equal(t, "opencode/big-pickle", listing.Records[0].ModelID)
}

// A signed-in Go listing answers from the subscription's catalog.
func TestListModelCatalogLiveQueriesGoCatalogWhenSignedIn(t *testing.T) {
	useStaticCatalog(t)
	opencodeSrc := &stubOpenCodeSource{signedIn: true, goCatalog: []opencode.ModelInfo{{ID: "glm-5.3"}, {ID: "kimi-k3"}}}
	listing := listModelCatalogLive(context.Background(),
		modelSources{opencode: opencodeSrc},
		ModelCatalogQuery{Provider: "opencode-go"})

	require.Len(t, listing.Records, 2)
	require.Equal(t, "opencode-go/glm-5.3", listing.Records[0].ModelID)
	require.Len(t, listing.Status, 1)
	require.Equal(t, ModelSourceOpenCodeAccount, listing.Status[0].Source)
	require.NotNil(t, listing.Status[0].FetchedAt)
	require.Equal(t, 1, opencodeSrc.goCalls)
}

// A failed Console fetch answers with nothing and says why. An offline list of
// models would only lead the user into a request that fails: every OpenCode
// model, the free ones included, needs a credential.
func TestListModelCatalogLiveReportsAFailedConsoleFetch(t *testing.T) {
	useStaticCatalog(t)
	opencodeSrc := &stubOpenCodeSource{err: errors.New("fetch OpenCode models: http 503")}
	listing := listModelCatalogLive(context.Background(),
		modelSources{opencode: opencodeSrc},
		ModelCatalogQuery{Provider: "opencode"})

	require.Empty(t, listing.Records)
	require.Len(t, listing.Status, 1)
	require.Contains(t, listing.Status[0].Error, "503")
	require.Empty(t, listing.Status[0].Source)
	require.Nil(t, listing.Status[0].FetchedAt)
}

// The OpenCode projection carries the model's thinking levels, trimmed to what
// this build can send on the protocol the model speaks. A model the table has
// evidence for and one it does not are both answered from the same place.
func TestDiscoverOpenCodeModelsCarriesReasoningEfforts(t *testing.T) {
	src := &stubOpenCodeSource{signedIn: true, goCatalog: []opencode.ModelInfo{
		{ID: "glm-5.3"},
		{ID: "minimax-m3"},
		{ID: "gemini-4-argon-contributor"},
	}}

	records, err := DiscoverOpenCodeModels(context.Background(), src, true)
	require.NoError(t, err)
	byID := make(map[string]ModelRecord, len(records))
	for _, rec := range records {
		byID[rec.APIModel] = rec
	}

	glm := byID["glm-5.3"]
	require.True(t, glm.CanReason)
	require.Equal(t, []string{"low", "high", "max"}, glm.ReasoningEfforts)

	minimax := byID["minimax-m3"]
	require.True(t, minimax.CanReason, "the model reasons, it just has no level to pick")
	require.Empty(t, minimax.ReasoningEfforts)

	// The table carries Gemini's three levels, but the Google client sends no
	// effort parameter at all, so the record must not offer one.
	gemini := byID["gemini-4-argon-contributor"]
	require.True(t, gemini.CanReason)
	require.Empty(t, gemini.ReasoningEfforts)

	console, err := DiscoverOpenCodeModels(context.Background(), &stubOpenCodeSource{
		console: []opencode.ModelInfo{{ID: "qwen3.7-plus"}},
	}, false)
	require.NoError(t, err)
	require.Len(t, console, 1)
	require.False(t, console[0].CanReason, "the catalog has no row for it, so nothing is claimed")
	require.Empty(t, console[0].ReasoningEfforts)
}

// The same capability question answers for a configured model: the levels are
// models.json's, trimmed to what the protocol's client can send.
func TestModelChoicesReasoningEffortsForOpenCode(t *testing.T) {
	installRepoCatalog(t)
	cfg := appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "opencode-go", Model: "glm-5.3", Models: appcfg.StringList{
				"glm-5.3", "minimax-m3", "qwen3.8-max", "gemini-4-argon-contributor", "qwen3.7-plus",
			}},
		}},
	}}}
	byModel := map[string][]string{}
	for _, choice := range ModelChoices(&cfg, "main") {
		byModel[choice.Model] = choice.ReasoningEfforts
	}
	require.Equal(t, []string{"low", "high", "max"}, byModel["glm-5.3"])
	require.Empty(t, byModel["minimax-m3"], "a toggle-only model has no level to pick")
	// Anthropic protocol: the levels are the model's own, and the client can
	// send every one of them (xhigh included).
	require.Equal(t, []string{"low", "medium", "xhigh"}, byModel["qwen3.8-max"])
	require.Empty(t, byModel["gemini-4-argon-contributor"], "the Google client has no effort parameter")
	require.Empty(t, byModel["qwen3.7-plus"], "models.json has no row for it, so nothing is claimed")
}

// Historical name kept for the behaviour it pins: a catalog model offers the
// levels its own catalog row names, not a scale invented here.
func TestModelChoicesKeepCatalogEffortLevels(t *testing.T) {
	installRepoCatalog(t)
	cfg := appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "gpt-5"}}},
	}}}
	choices := ModelChoices(&cfg, "main")
	require.Len(t, choices, 1)
	hit, ok := llm.Lookup("openai", "gpt-5")
	require.True(t, ok)
	require.True(t, hit.CanReason)
	require.Equal(t, []string{"minimal", "low", "medium", "high"}, choices[0].ReasoningEfforts)
}

// The levels a surface offers are the model's own, row by row: this is the
// table every surface reads (the TUI model page, /model, the setup flow and
// the gateway's model chip).
func TestModelReasoningEffortsFromTheCatalog(t *testing.T) {
	installRepoCatalog(t)
	for _, tc := range []struct {
		provider string
		model    string
		want     []string
		why      string
	}{
		{provider: "openai", model: "gpt-5", want: []string{"minimal", "low", "medium", "high"}},
		{provider: "openai", model: "gpt-5.5", want: []string{"none", "low", "medium", "high", "xhigh"}},
		{provider: "xai", model: "grok-4.3", want: []string{"none", "low", "medium", "high", "xhigh"}},
		{provider: "zhipuai", model: "glm-5.3-flash", want: []string{"low", "high", "max"}},
		{provider: "zhipuai", model: "glm-4.5", want: nil, why: "a toggle, no discrete level"},
		{provider: "deepseek", model: "deepseek-chat", want: nil, why: "the model does not reason"},
		{provider: "anthropic", model: "claude-sonnet-5", want: []string{"low", "medium", "high", "xhigh", "max"}},
		{provider: "anthropic", model: "claude-haiku-4-5", want: nil, why: "a token budget, no discrete level"},
		{provider: "alibaba", model: "qwen3.8-max", want: []string{"low", "medium", "xhigh"}},
		{provider: "opencode-go", model: "glm-5.3", want: []string{"low", "high", "max"}},
		{provider: "opencode-go", model: "gemini-4-argon-contributor", want: nil, why: "google protocol sends no effort"},
		{provider: "opencode", model: "claude-sonnet-4-5", want: nil, why: "a token budget, no discrete level"},
		{provider: "tencent", model: "hy3", want: nil, why: "the catalog has no level for it"},
		{provider: "openai", model: "not-a-model", want: nil, why: "unknown model"},
	} {
		got := ModelReasoningEfforts(tc.provider, tc.model)
		if tc.want == nil {
			require.Empty(t, got, "%s/%s: %s", tc.provider, tc.model, tc.why)
			continue
		}
		require.Equal(t, tc.want, got, "%s/%s", tc.provider, tc.model)
	}
}

// The client that will build the request decides which levels may be offered,
// for every model that rides it: an OpenCode model on the Anthropic path goes
// through the same client as the anthropic provider, and that client drops a
// level AcceptsEffort rejects (sending no effort at all). A level such a row
// names must never reach a picker — that is the fake option this catalog exists
// to remove, and it must not depend on today's rows happening to be sendable.
func TestAnthropicProtocolModelsOfferOnlySendableLevels(t *testing.T) {
	data, err := llm.RepoModelsJSONForTest()
	require.NoError(t, err)
	repoCatalog, err := llm.Parse(data)
	require.NoError(t, err)
	cat, err := llm.Parse([]byte(`{
		"opencode-go/qwen3.8-max": {"id": "opencode-go/qwen3.8-max", "reasoning": true,
			"reasoning_options": [{"type": "effort", "values": ["none", "minimal", "low", "medium", "xhigh"]}]},
		"opencode/qwen3.8-flash": {"id": "opencode/qwen3.8-flash", "reasoning": true,
			"reasoning_options": [{"type": "effort", "values": ["none", "minimal", "low", "max"]}]},
		"anthropic/claude-test-tier": {"id": "anthropic/claude-test-tier", "reasoning": true,
			"reasoning_options": [{"type": "effort", "values": ["none", "minimal", "low", "high", "max"]}]},
		"openai/gpt-test-tier": {"id": "openai/gpt-test-tier", "reasoning": true,
			"reasoning_options": [{"type": "effort", "values": ["none", "minimal", "low"]}]}
	}`))
	require.NoError(t, err)
	llm.SetGlobalCatalogForTest(cat)
	t.Cleanup(func() { llm.SetGlobalCatalogForTest(repoCatalog) })

	// The fixture has to actually ride the Anthropic path, or it proves
	// nothing: both OpenCode ids route to the Anthropic client.
	for _, tc := range []struct{ provider, model string }{
		{provider: "opencode-go", model: "qwen3.8-max"},
		{provider: "opencode", model: "qwen3.8-flash"},
	} {
		protocol, err := opencode.ProtocolFor(tc.provider, tc.model)
		require.NoError(t, err)
		require.Equal(t, opencode.ProtocolAnthropic, protocol, "%s/%s must ride the Anthropic client", tc.provider, tc.model)
	}

	// none/minimal are outside the client's set, so they are never offered —
	// on the OpenCode surfaces just as much as on the provider itself.
	require.Equal(t, []string{"low", "medium", "xhigh"}, ModelReasoningEfforts("opencode-go", "qwen3.8-max"))
	require.Equal(t, []string{"low", "max"}, ModelReasoningEfforts("opencode", "qwen3.8-flash"))
	require.Equal(t, []string{"low", "high", "max"}, ModelReasoningEfforts("anthropic", "claude-test-tier"))

	// The OpenAI-compatible client passes the parameter through, so there the
	// catalog's own values are what a picker offers.
	require.Equal(t, []string{"none", "minimal", "low"}, ModelReasoningEfforts("openai", "gpt-test-tier"))

	// A row that names nothing this client can send yields no effort page at
	// all rather than an empty one.
	only, err := llm.Parse([]byte(`{"opencode-go/qwen3.8-max": {"id": "opencode-go/qwen3.8-max", "reasoning": true,
		"reasoning_options": [{"type": "effort", "values": ["none", "minimal"]}]}}`))
	require.NoError(t, err)
	llm.SetGlobalCatalogForTest(only)
	require.Empty(t, ModelReasoningEfforts("opencode-go", "qwen3.8-max"))
}

// The static catalog is the reference data, not the chat list: non-chat entries
// and the login-backed providers' reference rows never reach a picker.
func TestListModelCatalogSkipsNonChatAndLoginProviders(t *testing.T) {
	installRepoCatalog(t)

	openai := ListModelCatalog(ModelCatalogQuery{Provider: "openai"})
	byID := make(map[string]ModelRecord, len(openai))
	for _, rec := range openai {
		byID[rec.ModelID] = rec
	}
	require.Contains(t, byID, "openai/gpt-5")
	require.NotContains(t, byID, "openai/whisper-large-v3", "a transcription model is not a chat model")
	require.NotContains(t, byID, "openai/gpt-image-1", "an image model is not a chat model")

	all := ListModelCatalog(ModelCatalogQuery{})
	entries := make(map[string]llm.Model, len(all))
	for _, m := range llm.AllModels() {
		entries[strings.ToLower(m.ID)] = m
	}
	for _, rec := range all {
		require.False(t, llm.LoginBackedProvider(rec.Provider), "%s came from a login-backed provider", rec.ModelID)
		m, ok := entries[strings.ToLower(rec.ModelID)]
		require.True(t, ok)
		require.True(t, m.Chat(), "%s is not a chat model", rec.ModelID)
	}
}

// An unfiltered listing runs every installed source once, in a stable order:
// static, ChatGPT, Console, Go. The needle applies inside each block.
func TestListModelCatalogLiveOpenCodeBlocksFollowEachOther(t *testing.T) {
	useStaticCatalog(t)
	chatgptSrc := &stubChatGPTSource{models: []openai.ModelInfo{
		{Slug: "gpt-live", Visibility: "list", Priority: 1},
	}}
	opencodeSrc := &stubOpenCodeSource{
		signedIn:  true,
		console:   []opencode.ModelInfo{{ID: "big-pickle"}},
		goCatalog: []opencode.ModelInfo{{ID: "glm-5.3"}},
	}
	listing := listModelCatalogLive(context.Background(),
		modelSources{chatgpt: chatgptSrc, opencode: opencodeSrc},
		ModelCatalogQuery{})

	providers := make([]string, 0, len(listing.Records))
	for _, rec := range listing.Records {
		providers = append(providers, rec.Provider)
	}
	require.Equal(t, []string{"openai", "chatgpt", "opencode", "opencode-go"}, providers)
	require.Len(t, listing.Status, 3)
	require.Equal(t, []string{"chatgpt", "opencode", "opencode-go"}, []string{
		listing.Status[0].Provider, listing.Status[1].Provider, listing.Status[2].Provider,
	})

	filtered := listModelCatalogLive(context.Background(),
		modelSources{opencode: &stubOpenCodeSource{signedIn: true, goCatalog: []opencode.ModelInfo{{ID: "glm-5.3"}, {ID: "kimi-k3"}}}},
		ModelCatalogQuery{Provider: "opencode-go", Query: "kimi"})
	require.Len(t, filtered.Records, 1)
	require.Equal(t, "kimi-k3", filtered.Records[0].APIModel)
}

// The service answers through the source installed on it, which is how the
// gateway's /api/models reaches the OpenCode catalogs.
func TestServiceListModelCatalogLiveUsesInstalledOpenCodeSource(t *testing.T) {
	useStaticCatalog(t)
	src := &stubOpenCodeSource{signedIn: true, console: []opencode.ModelInfo{{ID: "big-pickle"}}}
	svc := New(nil, WithOpenCodeModels(src))
	listing := svc.ListModelCatalogLive(context.Background(), ModelCatalogQuery{Provider: "opencode"})
	require.Len(t, listing.Records, 1)
	require.Equal(t, "opencode/big-pickle", listing.Records[0].ModelID)
	require.Equal(t, 1, src.consoleCalls)
}

// Without an installed OpenCode source no OpenCode block is queried: the
// static catalog has nothing to answer with for those providers, and reporting
// them as "unconfigured" would read as a broken login on every listing.
func TestListModelCatalogLiveWithoutOpenCodeSourceReportsNothing(t *testing.T) {
	useStaticCatalog(t)
	listing := listModelCatalogLive(context.Background(), modelSources{}, ModelCatalogQuery{Provider: "opencode"})
	require.Empty(t, listing.Records)
	require.Empty(t, listing.Status)
}

// saveChatGPTCredentials writes a loadable credentials file: both tokens
// present, access token readable enough that Load does not refuse it.
func saveChatGPTCredentials(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "auth.json")
	require.NoError(t, openai.Save(path, openai.Credentials{
		AccessToken:  "header.eyJleHAiOjk5OTk5OTk5OTl9.sig",
		RefreshToken: "refresh-live",
	}))
	return path
}

// The probe is the shared "is this machine's login still good" question both
// connect surfaces ask. A missing file is Missing before any network: the
// answer "no login" must not cost a round trip.
func TestProbeChatGPTModelsWithoutALoginIsMissing(t *testing.T) {
	src := &stubChatGPTSource{models: []openai.ModelInfo{
		{Slug: "gpt-5", Visibility: "list", Priority: 1},
	}}
	check := ProbeChatGPTModels(context.Background(), filepath.Join(t.TempDir(), "auth.json"), src)
	require.Equal(t, SignInMissing, check.State)
	require.Empty(t, check.Records)
	require.NoError(t, check.Err)
	require.Zero(t, src.calls, "no login means no listing attempt")
}

// A login that lists its models is the finished sign-in — the records go to
// the picker with the probe's answer.
func TestProbeChatGPTModelsUsableCarriesRecords(t *testing.T) {
	path := saveChatGPTCredentials(t, t.TempDir())
	src := &stubChatGPTSource{models: []openai.ModelInfo{
		{Slug: "gpt-5", Visibility: "list", Priority: 1},
	}}
	check := ProbeChatGPTModels(context.Background(), path, src)
	require.Equal(t, SignInUsable, check.State)
	require.Len(t, check.Records, 1)
	require.Equal(t, "chatgpt/gpt-5", check.Records[0].ModelID)
	require.NoError(t, check.Err)
}

// A typed 401 from the listing is the server refusing the login: Dead, and
// only a new browser flow changes that.
func TestProbeChatGPTModelsRejectedLoginIsDead(t *testing.T) {
	path := saveChatGPTCredentials(t, t.TempDir())
	dead := &llm.APIError{StatusCode: http.StatusUnauthorized, Message: "ChatGPT login expired; sign in again"}
	check := ProbeChatGPTModels(context.Background(), path, &stubChatGPTSource{err: dead})
	require.Equal(t, SignInDead, check.State)
	require.Empty(t, check.Records)
	require.Same(t, dead, check.Err)
}

// A listing failure that is not the login — here the npm registry lookup
// ChatGPT discovery needs — must not read as a dead login.
func TestProbeChatGPTModelsNetworkFailureIsUnreachable(t *testing.T) {
	path := saveChatGPTCredentials(t, t.TempDir())
	broken := errors.New("resolve client version: registry unreachable")
	check := ProbeChatGPTModels(context.Background(), path, &stubChatGPTSource{err: broken})
	require.Equal(t, SignInUnreachable, check.State)
	require.Empty(t, check.Records)
	require.Same(t, broken, check.Err)
}

func TestProbeOpenCodeGoModelsStates(t *testing.T) {
	models := []opencode.ModelInfo{{ID: "deepseek-v4-flash"}}
	cases := []struct {
		name      string
		src       OpenCodeModelsSource
		wantState SignInState
		wantErr   error
	}{
		{"no source is missing", nil, SignInMissing, nil},
		{"not signed in is missing", &stubOpenCodeSource{signedIn: false}, SignInMissing, nil},
		{"a listing is usable", &stubOpenCodeSource{signedIn: true, goCatalog: models}, SignInUsable, nil},
		{
			"a typed 401 is dead",
			&stubOpenCodeSource{signedIn: true, err: &opencode.ModelsError{StatusCode: http.StatusUnauthorized}},
			SignInDead,
			nil,
		},
		{
			"a network failure is unreachable",
			&stubOpenCodeSource{signedIn: true, err: errors.New("fetch: connection refused")},
			SignInUnreachable,
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check := ProbeOpenCodeGoModels(context.Background(), tc.src)
			require.Equal(t, tc.wantState, check.State)
			if tc.wantState == SignInUsable {
				require.NotEmpty(t, check.Records)
			} else {
				require.Empty(t, check.Records)
			}
			if tc.wantErr != nil {
				require.Equal(t, tc.wantErr, check.Err)
			} else if tc.wantState == SignInUsable || tc.wantState == SignInMissing {
				require.NoError(t, check.Err)
			} else {
				require.Error(t, check.Err)
			}
		})
	}
}

// The classifiers this package reads are the typed 401s of the three sign-in
// providers; 403 is "no subscription", which a login cannot fix, and a plain
// error is a network or backend fault. Wrapping must not hide a typed 401 —
// that is errors.As doing its job through the %w chain.
func TestSignInRejectedClassification(t *testing.T) {
	require.True(t, SignInRejected(&llm.APIError{StatusCode: http.StatusUnauthorized}))
	require.True(t, SignInRejected(&opencode.ModelsError{StatusCode: http.StatusUnauthorized}))
	require.True(t, SignInRejected(&openai.ModelsError{StatusCode: http.StatusUnauthorized}))
	require.True(t, SignInRejected(fmt.Errorf("list models: %w", &llm.APIError{StatusCode: http.StatusUnauthorized})),
		"a wrapped 401 still names a rejected login")

	require.False(t, SignInRejected(&llm.APIError{StatusCode: http.StatusForbidden}),
		"403 is no-subscription, which a new login cannot fix")
	require.False(t, SignInRejected(&opencode.ModelsError{StatusCode: http.StatusForbidden}))
	require.False(t, SignInRejected(&openai.ModelsError{StatusCode: http.StatusForbidden}))
	require.False(t, SignInRejected(errors.New("connection refused")))
	require.False(t, SignInRejected(fmt.Errorf("wrapped: %w", errors.New("gateway 503"))))
	require.False(t, SignInRejected(nil))
}

// A probe answer's Err is exactly what DiscoverChatGPTModels returned, so a
// surface that retries can hand it to the reporting it already has.
func TestProbeCarriesTheDiscoveryErrorUntouched(t *testing.T) {
	path := saveChatGPTCredentials(t, t.TempDir())
	_, want := DiscoverChatGPTModels(context.Background(), &stubChatGPTSource{err: errors.New("registry timeout")})
	check := ProbeChatGPTModels(context.Background(), path, &stubChatGPTSource{err: errors.New("registry timeout")})
	require.Equal(t, want, check.Err)
}
