package subscription

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/stretchr/testify/require"
)

// openCodeLoginFunc is the shape of the flow the tests replace.
type openCodeLoginFunc = func(ctx context.Context, path string, out io.Writer, openBrowser func(url string) error,
	onStart func(info opencode.LoginInfo), onStage func(stage opencode.LoginStage)) (opencode.Credentials, error)

// stubOpenCodeLogin installs a flow for one test, with the same rule as
// stubChatGPTLogin: the test ends the flow before the variable is put back.
func stubOpenCodeLogin(t *testing.T, run openCodeLoginFunc) {
	t.Helper()
	previous := openCodeLogin
	openCodeLogin = run
	t.Cleanup(func() { openCodeLogin = previous })
}

func TestOpenCodeStartReportsTheVerificationURLAndUserCode(t *testing.T) {
	const url = "https://opencode.ai/device?user_code=ABCD-1234"
	const code = "ABCD-1234"
	paths := make(chan string, 1)
	stubOpenCodeLogin(t, func(ctx context.Context, path string, out io.Writer, openBrowser func(string) error,
		onStart func(opencode.LoginInfo), onStage func(opencode.LoginStage)) (opencode.Credentials, error) {
		paths <- path
		if openBrowser == nil {
			// nil would mean "open the system browser", which only the surface
			// may decide.
			return opencode.Credentials{}, errors.New("the flow was handed a browser opener")
		}
		onStart(opencode.LoginInfo{VerificationURL: url, UserCode: code})
		return opencode.Credentials{AccessToken: "token"}, nil
	})

	env := testEnv(t)
	login, err := (openCodeGo{}).Start(context.Background(), env, FlowDeviceCode)
	require.NoError(t, err)
	require.Equal(t, opencode.DefaultPath(env.Home), <-paths)
	require.Eventually(t, func() bool { return login.AuthorizeURL() == url && login.UserCode() == code },
		5*time.Second, time.Millisecond, "the surface never learned the verification URL and code")
	require.NoError(t, login.Wait(context.Background()))
}

// A device flow has no callback to hand over, and its failure is Wait's answer.
func TestOpenCodeWaitAnswersTheFlowsFailure(t *testing.T) {
	stubOpenCodeLogin(t, func(ctx context.Context, path string, out io.Writer, openBrowser func(string) error,
		onStart func(opencode.LoginInfo), onStage func(opencode.LoginStage)) (opencode.Credentials, error) {
		return opencode.Credentials{}, errors.New("the user code expired")
	})

	login, err := (openCodeGo{}).Start(context.Background(), testEnv(t), FlowDeviceCode)
	require.NoError(t, err)
	require.EqualError(t, login.Wait(context.Background()), "the user code expired")
	require.ErrorIs(t, login.Submit(context.Background(), "http://localhost:1455/auth/callback?code=x"), errNoPasteCallback)

	if _, err := (openCodeGo{}).Start(context.Background(), testEnv(t), FlowPasteCallback); err == nil {
		t.Fatal("a device code has no pasted callback flow")
	}
}

func TestOpenCodeProbeReadsTheHomesLogin(t *testing.T) {
	want := turn.SignInCheck{State: turn.SignInUsable, Records: []turn.ModelRecord{{Provider: openCodeGoID, APIModel: "gpt-5.6-luna"}}}
	homes := make(chan string, 1)
	previous := openCodeProbe
	openCodeProbe = func(ctx context.Context, home string) turn.SignInCheck {
		homes <- home
		return want
	}
	t.Cleanup(func() { openCodeProbe = previous })

	env := Env{Home: "/home/of/the/sign-in"}
	require.Equal(t, want, (openCodeGo{}).Probe(context.Background(), env))
	require.Equal(t, env.Home, <-homes)
}

func TestOpenCodeOffersLiveUnderTheGoInferenceRoot(t *testing.T) {
	previous := openCodeList
	openCodeList = func(ctx context.Context, home string) ([]turn.ModelRecord, error) {
		return []turn.ModelRecord{
			{Provider: openCodeGoID, APIModel: "gpt-5.6-luna", ContextWindow: 400000, SupportsAttachments: true, IsDefault: true},
			{Provider: openCodeGoID, APIModel: "qwen3.8-max"},
		}, nil
	}
	t.Cleanup(func() { openCodeList = previous })

	root := opencode.ProviderBaseURL(openCodeGoID)
	require.NotEmpty(t, root, "the Go subscription's own inference root")
	offers, err := (openCodeGo{}).Offers(context.Background(), Env{Home: t.TempDir()})
	require.NoError(t, err)
	require.Equal(t, []Offer{
		{
			Subscription: openCodeGoID, Provider: openCodeGoID, Model: "gpt-5.6-luna", BaseURL: root,
			ContextWindow: 400000, SupportsImages: true, Default: true,
		},
		{Subscription: openCodeGoID, Provider: openCodeGoID, Model: "qwen3.8-max", BaseURL: root},
	}, offers)
}

// A Go subscription row holds no key — the login is the credential — and it is
// named after the subscription, not after the Console its login ran against.
func TestOpenCodeProvisionWritesKeylessRowsOfItsOwnSource(t *testing.T) {
	env := testEnv(t)
	root := opencode.ProviderBaseURL(openCodeGoID)
	offers := []Offer{
		{Subscription: openCodeGoID, Provider: openCodeGoID, Model: "gpt-5.6-luna", BaseURL: root, Default: true},
		{Subscription: openCodeGoID, Provider: openCodeGoID, Model: "qwen3.8-max", BaseURL: root},
	}

	ids, err := (openCodeGo{}).Provision(context.Background(), env, offers, ProvisionOptions{})
	require.NoError(t, err)
	require.Len(t, ids, 2)

	store := &state.LLMModelStore{DB: env.DB}
	for i, id := range ids {
		row, err := store.Get(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, openCodeGoID, row.Provider)
		require.Equal(t, offers[i].Model, row.Model)
		require.Equal(t, root, row.BaseURL)
		require.Empty(t, row.APIPath, "the model id decides the protocol, not the row")
		require.Empty(t, row.APIKey, "the login is the credential")
		require.Equal(t, openCodeGoID, row.Source)
	}
}

func TestOpenCodeRefreshKeyHasNothingToRefresh(t *testing.T) {
	err := (openCodeGo{}).RefreshKey(context.Background(), Env{Home: t.TempDir()}, "some-row-id")
	require.ErrorIs(t, err, ErrNoKeyToRefresh)
}
