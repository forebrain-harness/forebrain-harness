package subscription

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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
			{Provider: chatGPTID, APIModel: "gpt-6", ContextWindow: 400000, SupportsAttachments: true, AttachmentsKnown: true, IsDefault: true},
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
		// A listing that carried no input_modalities said nothing about the
		// model's modalities, so its offer declares no image answer at all.
		{Subscription: chatGPTID, Provider: chatGPTID, Model: "gpt-5-mini", BaseURL: openai.CodexBaseURL, ImagesUndeclared: true},
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

// idTokenFor builds the login's id token, the only place the account's
// identity rides in the stored file. Just the payload's claims are ever read
// (nothing verifies a signature), so the header and signature are placeholders.
func idTokenFor(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

// The account is the login's own identity, read locally: the email with the
// plan beside it, the account id when the login carried no email, and no
// account at all when nothing is stored or the bytes cannot be read. The
// sign-in never becomes an error a surface would have to block on.
func TestChatGPTAccountNamesTheStoredLogin(t *testing.T) {
	ctx := context.Background()

	env := testEnv(t)
	require.NoError(t, openai.Save(openai.CredentialsPath(env.Home), openai.Credentials{
		AccessToken: "at", RefreshToken: "rt", AccountID: "acct-1",
		IDToken: idTokenFor(t, map[string]any{
			"email":                          "dev@example.test",
			"https://api.openai.com/auth":    map[string]any{"chatgpt_plan_type": "plus"},
			"https://api.openai.com/profile": map[string]any{"email": "profile@example.test"},
		}),
	}))
	account, found, err := (chatGPT{}).Account(ctx, env)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, Account{Name: "dev@example.test", Detail: "plus"}, account)

	// No id token claims: the account id is the name.
	env = testEnv(t)
	require.NoError(t, openai.Save(openai.CredentialsPath(env.Home), openai.Credentials{
		AccessToken: "at", RefreshToken: "rt", AccountID: "acct-2",
	}))
	account, found, err = (chatGPT{}).Account(ctx, env)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, Account{Name: "acct-2"}, account)

	// No login stored: absent is an answer, not an error.
	account, found, err = (chatGPT{}).Account(ctx, testEnv(t))
	require.NoError(t, err)
	require.False(t, found)
	require.Equal(t, Account{}, account)

	// Stored but unreadable: the sign-in stands, there is just no name.
	env = testEnv(t)
	require.NoError(t, os.WriteFile(openai.CredentialsPath(env.Home), []byte("not json"), 0o600))
	account, found, err = (chatGPT{}).Account(ctx, env)
	require.NoError(t, err)
	require.False(t, found)
	require.Equal(t, Account{}, account)
}

// A credentials.chatgpt override that points outside the install's own home
// is the Codex CLI's file: the account says so, so a surface can warn before
// the logout removes another program's login.
func TestChatGPTAccountNamesASharedCredentialFile(t *testing.T) {
	ctx := context.Background()
	shared := filepath.Join(t.TempDir(), "auth.json")
	openai.SetCredentialsPath(shared)
	t.Cleanup(func() { openai.SetCredentialsPath("") })
	require.NoError(t, openai.Save(shared, openai.Credentials{
		AccessToken: "at", RefreshToken: "rt", AccountID: "acct-3",
		IDToken: idTokenFor(t, map[string]any{"email": "codex@example.test"}),
	}))

	env := testEnv(t)
	account, found, err := (chatGPT{}).Account(ctx, env)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, Account{Name: "codex@example.test", SharedPath: shared}, account)
}

// Logout removes the login from this machine. The default file is this
// install's own; a shared one is removed too, because the same login signs
// that client in.
func TestChatGPTLogoutRemovesTheStoredLogin(t *testing.T) {
	ctx := context.Background()

	env := testEnv(t)
	path := openai.CredentialsPath(env.Home)
	require.NoError(t, openai.Save(path, openai.Credentials{AccessToken: "at", RefreshToken: "rt"}))
	require.NoError(t, (chatGPT{}).Logout(ctx, env))
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "the default file survived the logout")
	raw, err := openai.LoadStoredCredentials(ctx, env.Home)
	require.NoError(t, err)
	require.Nil(t, raw)

	shared := filepath.Join(t.TempDir(), "auth.json")
	openai.SetCredentialsPath(shared)
	t.Cleanup(func() { openai.SetCredentialsPath("") })
	require.NoError(t, openai.Save(shared, openai.Credentials{AccessToken: "at", RefreshToken: "rt"}))
	require.NoError(t, (chatGPT{}).Logout(ctx, env))
	_, err = os.Stat(shared)
	require.True(t, os.IsNotExist(err), "the shared file survived the logout")
}
