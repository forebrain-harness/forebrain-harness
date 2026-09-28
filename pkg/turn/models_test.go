package turn

import (
	"context"
	"errors"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
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
	embedded, err := llm.Parse(llm.EmbeddedModelsJSON())
	if err == nil {
		t.Cleanup(func() { llm.SetGlobalCatalogForTest(embedded) })
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
