package turn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
	"github.com/stretchr/testify/require"
)

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
		{Slug: "gpt-5", DisplayName: "GPT 5", Visibility: "list", Priority: 1},
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
		{Slug: "gpt-5", DisplayName: "GPT 5", Visibility: "list", Priority: 1},
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
