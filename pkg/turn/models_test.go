package turn

import (
	"context"
	"errors"
	"testing"

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
		require.NotEmpty(t, rec.ModelName)
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
		{Slug: "gpt-hidden", DisplayName: "Hidden", Visibility: "hide", Priority: 0},
		{Slug: "gpt-legacy", DisplayName: "GPT Legacy", Visibility: "list", Priority: 30,
			SupportedReasoningEfforts: []openai.ReasoningEffortPreset{{Effort: "low"}, {Effort: "medium"}}},
		{Slug: "gpt-beta", DisplayName: "GPT Beta", Visibility: "list", Priority: 7,
			InputModalities: []string{"text", "image"}, ContextWindow: &contextWindow,
			DefaultReasoningEffort: "max", SupportedInAPI: false,
			SupportedReasoningEfforts: []openai.ReasoningEffortPreset{
				{Effort: "low"}, {Effort: "max", Description: "deepest"}, {Effort: ""}}},
		{Slug: "gpt-alpha", DisplayName: "", Visibility: "list", Priority: 7},
	}}

	records, err := DiscoverChatGPTModels(context.Background(), src)
	require.NoError(t, err)
	require.Len(t, records, 3)

	require.Equal(t, "gpt-beta", records[0].APIModel)
	require.True(t, records[0].IsDefault, "first visible in priority order is the preselectable default")
	require.Equal(t, "GPT Beta", records[0].ModelName)
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
	require.Equal(t, "gpt-alpha", records[1].ModelName, "missing display name falls back to the slug")
	require.False(t, records[1].IsDefault)
	require.False(t, records[1].CanReason, "no effort levels means not known to reason")

	require.Equal(t, "gpt-legacy", records[2].APIModel)
	require.Equal(t, ModelSourceChatGPTAccount, records[2].Source)
}

func TestDiscoverChatGPTModelsKeepsSubscriptionOnlyModels(t *testing.T) {
	src := &stubChatGPTSource{models: []openai.ModelInfo{
		{Slug: "gpt-subscription-only", DisplayName: "Sub Only", Visibility: "list", Priority: 1, SupportedInAPI: false},
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

// A chatgpt-scoped query must not drag other providers' static records in.
func TestListModelCatalogLiveChatGPTScopeHasNoStaticLeakage(t *testing.T) {
	useStaticCatalog(t)
	src := &stubChatGPTSource{models: []openai.ModelInfo{
		{Slug: "gpt-live", DisplayName: "GPT Live", Visibility: "list", Priority: 1},
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
		{Slug: "gpt-live", DisplayName: "GPT Live", Visibility: "list", Priority: 1},
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
		{Slug: "gpt-first", DisplayName: "First", Visibility: "list", Priority: 1},
	}}
	second := &stubChatGPTSource{models: []openai.ModelInfo{
		{Slug: "gpt-second", DisplayName: "Second", Visibility: "list", Priority: 1},
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
// free family first because those run with no login, names them so a picker
// shows why they work, and marks the first as the preselected entry. Metadata
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

	require.Equal(t, "glm-5.3-flash-free", records[0].APIModel, "the free family is listed first")
	require.True(t, records[0].IsDefault)
	require.Equal(t, "opencode/glm-5.3-flash-free", records[0].ModelID)
	require.Equal(t, "opencode", records[0].Provider)
	require.Equal(t, "glm-5.3-flash-free (free)", records[0].ModelName)
	require.Equal(t, ModelSourceOpenCodeAccount, records[0].Source)
	require.False(t, records[0].FetchedAt.IsZero())

	byID := make(map[string]ModelRecord, len(records))
	for _, rec := range records {
		byID[rec.APIModel] = rec
	}
	qwen := byID["qwen3.8-flash"]
	require.Equal(t, int64(128000), qwen.ContextWindow)
	require.True(t, qwen.CanReason, "the table records qwen3.8's reasoning output")
	require.False(t, qwen.IsDefault)

	unseen := byID["gpt-7-unseen"]
	require.Equal(t, int64(128000), unseen.ContextWindow, "an unknown id gets the conservative window")
	require.Equal(t, "gpt-7-unseen", unseen.ModelName, "no free suffix outside the free family")
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

// A failed Console fetch still answers with the free family — those models
// need no credential, so an offline machine keeps the one thing it can use —
// and the failure is named instead of being swallowed.
func TestListModelCatalogLiveFallsBackToFreeFamily(t *testing.T) {
	useStaticCatalog(t)
	opencodeSrc := &stubOpenCodeSource{err: errors.New("fetch OpenCode models: http 503")}
	listing := listModelCatalogLive(context.Background(),
		modelSources{opencode: opencodeSrc},
		ModelCatalogQuery{Provider: "opencode"})

	free := opencode.FreeConsoleModels()
	require.Len(t, listing.Records, len(free))
	require.Equal(t, free[0].ID+" (free)", listing.Records[0].ModelName)
	require.True(t, listing.Records[0].IsDefault)
	require.Empty(t, listing.Records[0].Source, "nothing was fetched, so nothing is sourced live")
	require.True(t, listing.Records[0].FetchedAt.IsZero())
	require.Len(t, listing.Status, 1)
	require.Contains(t, listing.Status[0].Error, "503")
	require.Nil(t, listing.Status[0].FetchedAt)
}

// An unfiltered listing runs every installed source once, in a stable order:
// static, ChatGPT, Console, Go. The needle applies inside each block.
func TestListModelCatalogLiveOpenCodeBlocksFollowEachOther(t *testing.T) {
	useStaticCatalog(t)
	chatgptSrc := &stubChatGPTSource{models: []openai.ModelInfo{
		{Slug: "gpt-live", DisplayName: "GPT Live", Visibility: "list", Priority: 1},
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
