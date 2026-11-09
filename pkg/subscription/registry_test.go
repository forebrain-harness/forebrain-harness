package subscription

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
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
// surfaces' own lists were before this registry existed.
func TestRegistryListsTheSignInsInDisplayOrder(t *testing.T) {
	ids := make([]string, 0, len(Registry()))
	for _, sub := range Registry() {
		ids = append(ids, sub.ID())
	}
	require.Equal(t, []string{chatGPTID, openCodeGoID}, ids)
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
