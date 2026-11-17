package subscription

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/stretchr/testify/require"
)

// testEnv is a home and a state database of its own, so a test never reads the
// real home and never writes a row into it.
func testEnv(t *testing.T) Env {
	t.Helper()
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return Env{Home: home, DB: db}
}

// The order is the display order every surface shows, and it is what the
// surfaces' own lists were before this registry existed. Volcengine Ark joins it
// third (plan 281), after the two sign-ins that store no key of their own.
func TestRegistryListsTheSignInsInDisplayOrder(t *testing.T) {
	ids := make([]string, 0, len(Registry()))
	for _, sub := range Registry() {
		ids = append(ids, sub.ID())
	}
	require.Equal(t, []string{chatGPTID, openCodeGoID, volcengineID}, ids)
}

// Every entry is a sign-in: the list reads "sign in with …", so a provider a
// user connects by pasting a key must never appear here (owner rule
// 2026-11-07 — those are pkg/config's built-in providers).
func TestRegistryHoldsOnlySubscriptionsWithASignInFlow(t *testing.T) {
	known := map[string]bool{FlowBrowserCallback: true, FlowPasteCallback: true, FlowDeviceCode: true}
	for _, sub := range Registry() {
		flows := sub.Flows()
		require.NotEmpty(t, flows,
			"%s offers no sign-in flow; a key-only provider belongs to pkg/config, not here", sub.ID())
		for _, flow := range flows {
			require.True(t, known[flow], "%s names an unknown flow %q", sub.ID(), flow)
		}
		en, zh := sub.Label()
		require.NotEmpty(t, en)
		require.NotEmpty(t, zh)
	}
}

func TestByIDFindsTheSubscriptionASurfacePersisted(t *testing.T) {
	sub, ok := ByID(" OpenCode-Go ")
	require.True(t, ok)
	require.Equal(t, openCodeGoID, sub.ID())

	_, ok = ByID("zhipuai")
	require.False(t, ok, "a provider connected with a pasted key is not a subscription")
}

// A login that ended takes no more callbacks: the person who pasted the
// address after the flow gave up gets an answer instead of a blocked handler.
func TestAFinishedLoginTakesNoMoreCallbacks(t *testing.T) {
	previous := chatGPTLogin
	chatGPTLogin = func(ctx context.Context, path string, out io.Writer, openBrowser func(string) error,
		manual <-chan openai.ManualCallback) (openai.Credentials, error) {
		return openai.Credentials{}, nil
	}
	t.Cleanup(func() { chatGPTLogin = previous })

	login, err := (chatGPT{}).Start(context.Background(), Env{Home: t.TempDir()}, FlowPasteCallback)
	require.NoError(t, err)
	require.NoError(t, login.Wait(context.Background()))
	require.ErrorIs(t, login.Submit(context.Background(), "http://localhost:1455/auth/callback?code=x"), errLoginEnded)
	// Canceling a finished login changes nothing.
	login.Cancel()
}

// ErrKeyChoice is the contract a key-backed plan (plan 281) answers with, and
// the surface renders the candidates from it.
func TestErrKeyChoiceCarriesTheCandidates(t *testing.T) {
	var err error = ErrKeyChoice{Keys: []KeyCandidate{
		{ID: "k1", Name: "default", Tail: "…7f3a"},
		{ID: "k2", Name: "ci", Tail: "…11bc"},
	}}
	require.EqualError(t, err, "this plan holds several keys (default, ci); choose one")

	var choice ErrKeyChoice
	require.True(t, errors.As(err, &choice))
	require.Len(t, choice.Keys, 2)
	require.Equal(t, "…7f3a", choice.Keys[0].Tail)
}

// pluginSubStub is one subscription a local plugin could Register: it signs in
// with a device flow and provisions rows through the shared ProvisionRows.
type pluginSubStub struct{ id string }

func (s pluginSubStub) ID() string              { return s.id }
func (s pluginSubStub) Label() (string, string) { return "Plugin " + s.id, "Plugin " + s.id }
func (s pluginSubStub) Flows() []string         { return []string{FlowDeviceCode} }
func (s pluginSubStub) Probe(context.Context, Env) turn.SignInCheck {
	return turn.SignInCheck{State: turn.SignInMissing}
}
func (s pluginSubStub) Start(context.Context, Env, string) (Login, error) {
	return nil, errors.New("not needed here")
}
func (s pluginSubStub) Offers(context.Context, Env) ([]Offer, error)  { return nil, nil }
func (s pluginSubStub) RefreshKey(context.Context, Env, string) error { return ErrNoKeyToRefresh }

func (s pluginSubStub) Provision(ctx context.Context, env Env, offers []Offer, _ ProvisionOptions) ([]string, error) {
	return ProvisionRows(ctx, env, offers, s.id)
}

// Local plugins register behind the built-ins in registration order, a
// re-registration replaces in place, ByID finds them, and ProvisionRows lands
// their rows the same way it lands the built-ins' — once the plugin has
// registered its provider as login-backed, the write path the built-ins clear
// (an empty key on a sign-in row) clears for it too.
func TestRegisterPluginSubscriptionAppendsInOrderAndDeduplicates(t *testing.T) {
	ResetPluginsForTest()
	t.Cleanup(ResetPluginsForTest)
	appcfg.ResetProviderPluginsForTest()
	t.Cleanup(appcfg.ResetProviderPluginsForTest)
	appcfg.RegisterProviderPlugin(appcfg.BuiltinProvider{
		ID: "pluga", Label: "Plug A", LabelZH: "Plug A", LoginBacked: true,
		Endpoints: []appcfg.ProviderEndpoint{{BaseURL: "https://pluga.example.test"}},
	})

	Register(pluginSubStub{id: "pluga"})
	Register(pluginSubStub{id: "plugb"})
	Register(pluginSubStub{id: "pluga"}) // replaces, does not duplicate
	Register(nil)                        // no-op

	ids := make([]string, 0, len(Registry()))
	for _, sub := range Registry() {
		ids = append(ids, sub.ID())
	}
	require.Equal(t, []string{chatGPTID, openCodeGoID, volcengineID, "pluga", "plugb"}, ids)

	found, ok := ByID(" PLUGB ")
	require.True(t, ok)
	require.Equal(t, "plugb", found.ID())

	env := testEnv(t)
	rowIDs, err := ProvisionRows(context.Background(), env, []Offer{
		{Subscription: "pluga", Provider: "pluga", Model: "pa-model",
			BaseURL: "https://pluga.example.test", APIPath: "/algo/chat", Plan: "sub"},
		{Subscription: "pluga", Provider: "pluga", Model: "pa-model-2",
			BaseURL: "https://pluga.example.test", APIPath: "/algo/chat"},
	}, "pluga")
	require.NoError(t, err)
	require.Len(t, rowIDs, 2)

	rows, err := env.DB.QueryContext(context.Background(),
		`SELECT provider, model, api_path, api_key, source FROM fb_llm_models WHERE source = 'pluga' ORDER BY model`)
	require.NoError(t, err)
	defer rows.Close()
	var got [][]string
	for rows.Next() {
		var provider, model, apiPath, source string
		var apiKey *string
		require.NoError(t, rows.Scan(&provider, &model, &apiPath, &apiKey, &source))
		require.NotNil(t, apiKey, "a login-backed row states an empty key")
		require.Empty(t, *apiKey)
		got = append(got, []string{provider, model, apiPath, source})
	}
	require.NoError(t, rows.Err())
	require.Equal(t, [][]string{
		{"pluga", "pa-model", "/algo/chat", "pluga"},
		{"pluga", "pa-model-2", "/algo/chat", "pluga"},
	}, got)
}

// What the listing said about a model lands on its row as the row's own caps:
// the window the model serves and whether it takes images. Both follow one rule
// — a listing that said nothing writes nothing, leaving the catalog to answer,
// because "no images" is only an answer when some listing gave it. A list that
// carries ids alone (Volcengine's plan listing) therefore lands as unknown
// rather than as "no images", and the image gate fails open on it.
func TestProvisionRowsWritesTheListingsCapabilities(t *testing.T) {
	appcfg.ResetProviderPluginsForTest()
	t.Cleanup(appcfg.ResetProviderPluginsForTest)
	appcfg.RegisterProviderPlugin(appcfg.BuiltinProvider{
		ID: "capsub", Label: "Cap Sub", LabelZH: "Cap Sub", LoginBacked: true,
		Endpoints: []appcfg.ProviderEndpoint{{BaseURL: "https://capsub.example.test"}},
	})
	env := testEnv(t)
	_, err := ProvisionRows(context.Background(), env, []Offer{
		{Provider: "capsub", Model: "with-caps", BaseURL: "https://capsub.example.test", APIPath: "/chat",
			ContextWindow: 1_000_000, SupportsImages: true},
		{Provider: "capsub", Model: "no-window", BaseURL: "https://capsub.example.test", APIPath: "/chat"},
		{Provider: "capsub", Model: "undeclared", BaseURL: "https://capsub.example.test", APIPath: "/chat",
			ImagesUndeclared: true},
	}, "capsub")
	require.NoError(t, err)

	rows, err := env.DB.QueryContext(context.Background(),
		`SELECT model, cap_context_window, cap_images FROM fb_llm_models ORDER BY model`)
	require.NoError(t, err)
	defer rows.Close()
	type capRow struct {
		model  string
		window *int64
		images *int64
	}
	var caps []capRow
	for rows.Next() {
		var row capRow
		require.NoError(t, rows.Scan(&row.model, &row.window, &row.images))
		caps = append(caps, row)
	}
	require.NoError(t, rows.Err())
	require.Len(t, caps, 3)
	require.Equal(t, "no-window", caps[0].model)
	require.Nil(t, caps[0].window, "a window nobody named stays the catalog's")
	require.NotNil(t, caps[0].images)
	require.EqualValues(t, 0, *caps[0].images)
	require.Equal(t, "undeclared", caps[1].model)
	require.Nil(t, caps[1].window, "a list carrying ids alone names no window")
	require.Nil(t, caps[1].images, "and must not answer for the image switch")
	require.Equal(t, "with-caps", caps[2].model)
	require.NotNil(t, caps[2].window)
	require.EqualValues(t, 1_000_000, *caps[2].window)
	require.NotNil(t, caps[2].images)
	require.EqualValues(t, 1, *caps[2].images)

	// The row is what every reader asks: the window the model serves is the
	// listing's, not a catalog guess.
	store := &state.LLMModelStore{DB: env.DB}
	row, found, err := store.FindByIdentity(context.Background(), "capsub", "with-caps", "https://capsub.example.test", "/chat")
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, row.CapContextWindow)
	require.EqualValues(t, 1_000_000, *row.CapContextWindow)
}

// A record knows its input modalities only when the listing answered the
// question: models.json for an OpenCode id, input_modalities for a ChatGPT one.
// A record that carries no answer projects to an offer that declares nothing,
// which is what keeps a locally unknown model from writing "no images".
func TestOffersFromRecordsDeclareOnlyWhatTheRecordKnows(t *testing.T) {
	offers := offersFromRecords("recordsub", []turn.ModelRecord{
		{Provider: "recordsub", APIModel: "declared", SupportsAttachments: true, AttachmentsKnown: true},
		{Provider: "recordsub", APIModel: "unknown"},
	}, "https://x.test")
	require.Len(t, offers, 2)
	require.False(t, offers[0].ImagesUndeclared, "a characterized model keeps its answer")
	require.True(t, offers[0].SupportsImages)
	require.True(t, offers[1].ImagesUndeclared, "an uncharacterized model declares nothing")
	require.False(t, offers[1].SupportsImages)
}

// The listing's thinking capabilities land on the row too: the levels it
// offers (never "none"), the level it marks as its default, and whether it
// carries a mode that turns thinking off. An offer whose listing says nothing
// leaves all three columns unset, so the catalog keeps answering.
func TestProvisionRowsWritesTheListingsThinkingCapabilities(t *testing.T) {
	appcfg.ResetProviderPluginsForTest()
	t.Cleanup(appcfg.ResetProviderPluginsForTest)
	appcfg.RegisterProviderPlugin(appcfg.BuiltinProvider{
		ID: "thinkcapsub", Label: "Think Cap Sub", LabelZH: "Think Cap Sub", LoginBacked: true,
		Endpoints: []appcfg.ProviderEndpoint{{BaseURL: "https://thinkcapsub.example.test"}},
	})
	env := testEnv(t)
	_, err := ProvisionRows(context.Background(), env, []Offer{
		{Provider: "thinkcapsub", Model: "with-levels", BaseURL: "https://thinkcapsub.example.test", APIPath: "/chat",
			Efforts: []string{"none", "low", "medium", "xhigh"}, DefaultEffort: "medium"},
		{Provider: "thinkcapsub", Model: "no-levels", BaseURL: "https://thinkcapsub.example.test", APIPath: "/chat"},
	}, "thinkcapsub")
	require.NoError(t, err)

	rows, err := env.DB.QueryContext(context.Background(),
		`SELECT model, cap_efforts_json, cap_default_effort, cap_reasoning_off FROM fb_llm_models ORDER BY model`)
	require.NoError(t, err)
	defer rows.Close()
	type thinkingRow struct {
		model   string
		efforts string
		def     string
		off     *int64
	}
	var caps []thinkingRow
	for rows.Next() {
		var row thinkingRow
		require.NoError(t, rows.Scan(&row.model, &row.efforts, &row.def, &row.off))
		caps = append(caps, row)
	}
	require.NoError(t, rows.Err())
	require.Len(t, caps, 2)
	require.Equal(t, "no-levels", caps[0].model)
	require.Equal(t, "", caps[0].efforts, "a listing with no levels leaves the catalog's")
	require.Equal(t, "", caps[0].def)
	require.Nil(t, caps[0].off, "a listing that says nothing about the off switch leaves it unknown")
	require.Equal(t, "with-levels", caps[1].model)
	require.JSONEq(t, `["low","medium","xhigh"]`, caps[1].efforts, "the off switch is not a level")
	require.Equal(t, "medium", caps[1].def)
	require.NotNil(t, caps[1].off)
	require.EqualValues(t, 1, *caps[1].off)

	// A producer that states the switch itself wins over the split: the level
	// list is where "none" is refused, not the way the answer is spelled.
	_, err = ProvisionRows(context.Background(), env, []Offer{
		{Provider: "thinkcapsub", Model: "off-said", BaseURL: "https://thinkcapsub.example.test", APIPath: "/chat",
			Efforts: []string{"low", "high"}, ReasoningOff: boolPointer(false)},
	}, "thinkcapsub")
	require.NoError(t, err)
	store := &state.LLMModelStore{DB: env.DB}
	row, found, err := store.FindByIdentity(context.Background(), "thinkcapsub", "off-said", "https://thinkcapsub.example.test", "/chat")
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, row.CapReasoningOff)
	require.False(t, *row.CapReasoningOff)
	require.Equal(t, []string{"low", "high"}, row.CapEfforts)
}

// A record a subscription's own listing produced carries its levels and default
// through the one projection, so a catalog-derived offer writes the same row a
// plugin's own listing does.
func TestOffersFromRecordsCarriesTheRecordsThinkingLevels(t *testing.T) {
	offers := offersFromRecords("recordsub", []turn.ModelRecord{
		{Provider: "recordsub", APIModel: "with-levels", ReasoningEfforts: []string{"low", "high"}, DefaultReasoningEffort: "high"},
		{Provider: "recordsub", APIModel: "no-levels"},
	}, "https://recordsub.example.test")
	require.Len(t, offers, 2)
	require.Equal(t, []string{"low", "high"}, offers[0].Efforts)
	require.Equal(t, "high", offers[0].DefaultEffort)
	require.Nil(t, offers[0].ReasoningOff, "a model record does not answer the off switch")
	require.Nil(t, offers[1].Efforts)
	require.Equal(t, "", offers[1].DefaultEffort)
}

func boolPointer(value bool) *bool { return &value }

// The device-code snapshot is one value: a page that repaints every second reads
// an address, a code and a deadline that were published together, never half of
// one answer beside half of the last.
func TestDeviceCodeInfoReadsOneSnapshot(t *testing.T) {
	l := &login{}
	stop := make(chan struct{})
	published := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			l.setDeviceCode(
				fmt.Sprintf("https://signin.example.test/%d", i),
				fmt.Sprintf("CODE-%d", i),
				time.Unix(int64(i), 0).UTC(),
			)
			select {
			case published <- struct{}{}:
			default:
			}
		}
	}()
	select {
	case <-published:
	case <-time.After(5 * time.Second):
		t.Fatal("the flow never published its first snapshot")
	}
	for i := 0; i < 2000; i++ {
		info := l.DeviceCodeInfo()
		var gen int
		if _, err := fmt.Sscanf(info.AuthorizeURL, "https://signin.example.test/%d", &gen); err != nil {
			t.Fatalf("snapshot %+v: the address did not name a publication: %v", info, err)
		}
		require.Equal(t, fmt.Sprintf("CODE-%d", gen), info.UserCode,
			"snapshot %+v pairs a code with another publication's address", info)
		require.True(t, info.ExpiresAt.Equal(time.Unix(int64(gen), 0).UTC()),
			"snapshot %+v pairs a deadline with another publication's address", info)
	}
	close(stop)
	writer.Wait()
}

// A login that has learned nothing yet answers zeros, and the two getters an
// existing plugin already reads keep answering the same values.
func TestDeviceCodeInfoIsZeroUntilTheFlowLearnsIt(t *testing.T) {
	l := &login{}
	require.Equal(t, DeviceCodeInfo{}, l.DeviceCodeInfo())

	l.setURL("https://signin.example.test/authorize")
	l.setUserCode("ABCD-1234")
	require.Equal(t, "https://signin.example.test/authorize", l.AuthorizeURL())
	require.Equal(t, "ABCD-1234", l.UserCode())
	require.Equal(t, DeviceCodeInfo{
		AuthorizeURL: "https://signin.example.test/authorize",
		UserCode:     "ABCD-1234",
	}, l.DeviceCodeInfo())

	// A device flow the console publishes no expiry for says so with a zero
	// deadline: the surface then shows no countdown instead of inventing one.
	l.setDeviceCode("https://signin.example.test/next", "", time.Time{})
	info := l.DeviceCodeInfo()
	require.Equal(t, "https://signin.example.test/next", info.AuthorizeURL)
	require.Empty(t, info.UserCode)
	require.True(t, info.ExpiresAt.IsZero())
}

// OpenCode publishes the address and the code the Console named in one answer,
// and has no expiry of its own to show: the login's token lifetime is not the
// code's life.
func TestDeviceCodeInfoOpenCodePublishesOneSnapshot(t *testing.T) {
	stubOpenCodeLogin(t, func(ctx context.Context, path string, out io.Writer, openBrowser func(string) error,
		onStart func(opencode.LoginInfo), onStage func(opencode.LoginStage)) (opencode.Credentials, error) {
		onStart(opencode.LoginInfo{VerificationURL: "https://opencode.ai/auth", UserCode: "QK7M-2PLX"})
		return opencode.Credentials{AccessToken: "token-placeholder"}, nil
	})
	login, err := (openCodeGo{}).Start(context.Background(), testEnv(t), FlowDeviceCode)
	require.NoError(t, err)
	snapshot, ok := login.(DeviceCodeLogin)
	require.True(t, ok, "the OpenCode login publishes its device-code snapshot")
	var info DeviceCodeInfo
	require.Eventually(t, func() bool {
		info = snapshot.DeviceCodeInfo()
		return info.AuthorizeURL != ""
	}, 5*time.Second, time.Millisecond, "the surface never learned the address and code")
	require.Equal(t, "https://opencode.ai/auth", info.AuthorizeURL)
	require.Equal(t, "QK7M-2PLX", info.UserCode)
	require.True(t, info.ExpiresAt.IsZero(), "the Console publishes no expiry for this code")
	require.NoError(t, login.Wait(context.Background()))
}
