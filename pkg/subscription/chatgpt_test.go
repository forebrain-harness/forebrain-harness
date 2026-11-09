package subscription

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/stretchr/testify/require"
)

// chatGPTLoginFunc is the shape of the flow the tests replace.
type chatGPTLoginFunc = func(ctx context.Context, path string, out io.Writer,
	openBrowser func(url string) error, manual <-chan openai.ManualCallback) (openai.Credentials, error)

// stubChatGPTLogin installs a flow for one test. A test that starts one always
// ends it before returning (Wait or Cancel + Wait), so the flow's goroutine is
// gone before the variable is put back — rewriting it underneath a running flow
// is a data race.
func stubChatGPTLogin(t *testing.T, run chatGPTLoginFunc) {
	t.Helper()
	previous := chatGPTLogin
	chatGPTLogin = run
	t.Cleanup(func() { chatGPTLogin = previous })
}

func TestChatGPTStartHandsTheAuthorizationURLOverWhileTheFlowWaits(t *testing.T) {
	const url = "https://auth.openai.com/oauth/authorize?state=xyz"
	paths := make(chan string, 1)
	stubChatGPTLogin(t, func(ctx context.Context, path string, out io.Writer,
		openBrowser func(string) error, manual <-chan openai.ManualCallback) (openai.Credentials, error) {
		paths <- path
		if openBrowser == nil {
			return openai.Credentials{}, errors.New("the flow must hand the URL over instead of opening a browser")
		}
		_ = openBrowser(url)
		<-ctx.Done()
		return openai.Credentials{}, ctx.Err()
	})

	env := testEnv(t)
	login, err := (chatGPT{}).Start(context.Background(), env, FlowPasteCallback)
	require.NoError(t, err)
	require.Equal(t, openai.CredentialsPath(env.Home), <-paths)
	require.Eventually(t, func() bool { return login.AuthorizeURL() == url },
		5*time.Second, time.Millisecond, "the surface never learned the authorization URL")

	login.Cancel()
	require.ErrorIs(t, login.Wait(context.Background()), context.Canceled)
}

// The address a user pastes goes to the flow, and the flow's own answer comes
// back: a rejected address is a reason the person can act on, not a failure of
// the login.
func TestChatGPTPasteGoesToTheFlowAndItsAnswerComesBack(t *testing.T) {
	const pasted = "http://localhost:1455/auth/callback?code=abc&state=xyz"
	handed := make(chan string, 1)
	stubChatGPTLogin(t, func(ctx context.Context, path string, out io.Writer,
		openBrowser func(string) error, manual <-chan openai.ManualCallback) (openai.Credentials, error) {
		select {
		case callback := <-manual:
			handed <- callback.URL
			callback.Reply <- openai.ErrCallbackStateMismatch
		case <-ctx.Done():
			return openai.Credentials{}, ctx.Err()
		}
		<-ctx.Done()
		return openai.Credentials{}, ctx.Err()
	})

	login, err := (chatGPT{}).Start(context.Background(), testEnv(t), FlowPasteCallback)
	require.NoError(t, err)
	require.ErrorIs(t, login.Submit(context.Background(), pasted), openai.ErrCallbackStateMismatch)
	require.Equal(t, pasted, <-handed)

	login.Cancel()
	require.ErrorIs(t, login.Wait(context.Background()), context.Canceled)
}

// A signed-in flow is a successful Wait, and the two flow kinds ChatGPT offers
// are the only ones it can start.
func TestChatGPTWaitAnswersTheFlowsResultAndRejectsAnUnknownFlow(t *testing.T) {
	stubChatGPTLogin(t, func(ctx context.Context, path string, out io.Writer,
		openBrowser func(string) error, manual <-chan openai.ManualCallback) (openai.Credentials, error) {
		return openai.Credentials{}, nil
	})

	env := testEnv(t)
	for _, flow := range (chatGPT{}).Flows() {
		login, err := (chatGPT{}).Start(context.Background(), env, flow)
		require.NoError(t, err, flow)
		require.NoError(t, login.Wait(context.Background()), flow)
	}

	_, err := (chatGPT{}).Start(context.Background(), env, FlowDeviceCode)
	require.Error(t, err, "ChatGPT has no device code")
}

// The probe is the answer every surface asks before starting a flow: a login
// this machine already holds is the finished sign-in.
func TestChatGPTProbeReadsTheHomesLogin(t *testing.T) {
	want := turn.SignInCheck{State: turn.SignInUsable, Records: []turn.ModelRecord{{Provider: chatGPTID, APIModel: "gpt-6"}}}
	homes := make(chan string, 1)
	previous := chatGPTProbe
	chatGPTProbe = func(ctx context.Context, home string) turn.SignInCheck {
		homes <- home
		return want
	}
	t.Cleanup(func() { chatGPTProbe = previous })

	env := Env{Home: "/home/of/the/sign-in"}
	require.Equal(t, want, (chatGPT{}).Probe(context.Background(), env))
	require.Equal(t, env.Home, <-homes)
}

func TestChatGPTOffersCarryTheEndpointTheLoginFixes(t *testing.T) {
	previous := chatGPTList
	chatGPTList = func(ctx context.Context, home string) ([]turn.ModelRecord, error) {
		return []turn.ModelRecord{
			{Provider: chatGPTID, APIModel: "gpt-6", ContextWindow: 400000, SupportsAttachments: true, IsDefault: true},
			{Provider: chatGPTID, APIModel: "gpt-5-mini"},
		}, nil
	}
	t.Cleanup(func() { chatGPTList = previous })

	offers, err := (chatGPT{}).Offers(context.Background(), Env{Home: t.TempDir()})
	require.NoError(t, err)
	require.Equal(t, []Offer{
		{
			Subscription: chatGPTID, Provider: chatGPTID, Model: "gpt-6", BaseURL: openai.CodexBaseURL,
			ContextWindow: 400000, SupportsImages: true, Default: true,
		},
		{Subscription: chatGPTID, Provider: chatGPTID, Model: "gpt-5-mini", BaseURL: openai.CodexBaseURL},
	}, offers)
}

// A ChatGPT row holds no key: the login is the credential. Writing the same
// offers again keeps their rows, so signing in twice does not leave two.
func TestChatGPTProvisionWritesKeylessRowsOfItsOwnSource(t *testing.T) {
	env := testEnv(t)
	offers := []Offer{
		{Subscription: chatGPTID, Provider: chatGPTID, Model: "gpt-6", BaseURL: openai.CodexBaseURL, Default: true},
		{Subscription: chatGPTID, Provider: chatGPTID, Model: "gpt-5-mini", BaseURL: openai.CodexBaseURL},
	}

	ids, err := (chatGPT{}).Provision(context.Background(), env, offers, ProvisionOptions{})
	require.NoError(t, err)
	require.Len(t, ids, 2)

	store := &state.LLMModelStore{DB: env.DB}
	for i, id := range ids {
		row, err := store.Get(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, chatGPTID, row.Provider)
		require.Equal(t, offers[i].Model, row.Model)
		require.Equal(t, openai.CodexBaseURL, row.BaseURL)
		require.Empty(t, row.APIPath, "the Codex transport owns the path")
		require.Empty(t, row.APIKey, "the login is the credential")
		require.Equal(t, chatGPTID, row.Source)
	}

	again, err := (chatGPT{}).Provision(context.Background(), env, offers, ProvisionOptions{})
	require.NoError(t, err)
	require.Equal(t, ids, again, "the same offers are the same rows")
	rows, err := store.List(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, len(offers))
}

func TestChatGPTRefreshKeyHasNothingToRefresh(t *testing.T) {
	err := (chatGPT{}).RefreshKey(context.Background(), Env{Home: t.TempDir()}, "some-row-id")
	require.ErrorIs(t, err, ErrNoKeyToRefresh)
}
