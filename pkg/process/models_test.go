package process

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/stretchr/testify/require"
)

func jwtForTest(t *testing.T, exp time.Time) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"exp": exp.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func writeAuthFile(t *testing.T, dir string) {
	t.Helper()
	raw := `{"auth_mode":"chatgpt","tokens":{"id_token":"id","access_token":` +
		`"` + jwtForTest(t, time.Now().Add(2*time.Hour)) + `","refresh_token":"refresh","account_id":"acct_123"}}`
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The source must authenticate exactly like the Responses client does — same
// credentials path resolution, same transport — so the listed models are the
// models a turn would actually run on.
func TestChatGPTModelsSourceAuthenticatesAndFetches(t *testing.T) {
	var gotAuth, gotAccount, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("ChatGPT-Account-ID")
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-live","display_name":"GPT Live","visibility":"list","priority":1,
			"supported_reasoning_levels":[{"effort":"low"},{"effort":"max"}]}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	writeAuthFile(t, dir)
	src := newChatGPTModelsSource(filepath.Join(dir, "auth.json"), server.URL)
	src.resolveVersion = func(context.Context) (string, error) { return "0.156.1", nil }

	models, err := src.ChatGPTModels(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Equal(t, "gpt-live", models[0].Slug)
	require.Equal(t, "/models?client_version=0.156.1", gotPath, "the live-resolved version is what the request declares")
	require.Equal(t, "Bearer "+jwtPrefix(t, dir), gotAuth)
	require.Equal(t, "acct_123", gotAccount)
}

// A version that cannot be resolved is a discovery failure with its cause
// attached — never a request with a guessed version.
func TestChatGPTModelsSourceSurfacesVersionResolutionFailure(t *testing.T) {
	dir := t.TempDir()
	writeAuthFile(t, dir)
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()
	src := newChatGPTModelsSource(filepath.Join(dir, "auth.json"), server.URL)
	src.resolveVersion = func(context.Context) (string, error) {
		return "", errors.New("resolve codex client version: npm dist-tags for @openai/codex: http 503")
	}
	_, err := src.ChatGPTModels(context.Background())
	require.ErrorContains(t, err, "npm dist-tags")
	require.Zero(t, requests, "no models request may leave without a resolved version")
}

func jwtPrefix(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	return stored.Tokens.AccessToken
}

// DiscoverChatGPTModels is the single entry the surfaces call: the projection
// happens once, in turn, for everyone.
func TestDiscoverChatGPTModelsProjectsThroughTheSharedPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[
			{"slug":"gpt-hidden","visibility":"hide","priority":0},
			{"slug":"gpt-live","display_name":"GPT Live","visibility":"list","priority":2,
			 "default_reasoning_level":"low","supported_reasoning_levels":[{"effort":"low"},{"effort":"max"}]}
		]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	writeAuthFile(t, dir)
	src := newChatGPTModelsSource(filepath.Join(dir, "auth.json"), server.URL)
	src.resolveVersion = func(context.Context) (string, error) { return "0.156.1", nil }
	records, err := turn.DiscoverChatGPTModels(context.Background(), src)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "gpt-live", records[0].APIModel)
	require.True(t, records[0].IsDefault)
	require.Equal(t, []string{"low", "max"}, records[0].ReasoningEfforts)
	require.Equal(t, turn.ModelSourceChatGPTAccount, records[0].Source)
}

// Missing credentials surface as an error naming the login requirement, not as
// an empty catalog.
func TestDiscoverChatGPTModelsWithoutCredentialsErrors(t *testing.T) {
	src := newChatGPTModelsSource(filepath.Join(t.TempDir(), "auth.json"), "https://example.invalid")
	src.resolveVersion = func(context.Context) (string, error) { return "0.156.1", nil }
	_, err := turn.DiscoverChatGPTModels(context.Background(), src)
	require.ErrorContains(t, err, "ChatGPT login required")
}

// The public constructor resolves the same path the run client uses, so the
// credentials.chatgpt override is honored by both or neither.
func TestNewChatGPTModelsSourceUsesTheResolvedCredentialsPath(t *testing.T) {
	dir := t.TempDir()
	writeAuthFile(t, dir)
	src, ok := NewChatGPTModelsSource(dir).(chatGPTModelsSource)
	require.True(t, ok)
	require.Equal(t, filepath.Join(dir, "auth.json"), src.path)
	require.Equal(t, openai.CodexBaseURL, src.baseURL)
	require.NotNil(t, src.resolveVersion, "the default source resolves the version live")
}

// capturedOpenCodeFetch is what one catalog call resolved: which surface, which
// bearer, and through which transport.
type capturedOpenCodeFetch struct {
	goSubscription bool
	token          string
	transport      http.RoundTripper
}

func stubOpenCodeFetch(t *testing.T, captured *capturedOpenCodeFetch, ids ...string) func(context.Context, http.RoundTripper, bool, string) ([]opencode.ModelInfo, error) {
	t.Helper()
	return func(_ context.Context, base http.RoundTripper, goSubscription bool, token string) ([]opencode.ModelInfo, error) {
		*captured = capturedOpenCodeFetch{goSubscription: goSubscription, token: token, transport: base}
		models := make([]opencode.ModelInfo, 0, len(ids))
		for _, id := range ids {
			models = append(models, opencode.ModelInfo{ID: id})
		}
		return models, nil
	}
}

// The Console catalog is readable without an account — the free family is
// served to the literal public bearer — so discovery sends exactly that, on the
// transport a login-less turn would use.
func TestOpenCodeModelsSourceReadsTheConsoleCatalogWithThePublicBearer(t *testing.T) {
	src := newOpenCodeModelsSource(filepath.Join(t.TempDir(), opencode.CredentialsFile))
	var got capturedOpenCodeFetch
	src.fetch = stubOpenCodeFetch(t, &got, "big-pickle")

	models, err := src.OpenCodeModels(context.Background(), false)
	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Equal(t, "big-pickle", models[0].ID)
	require.False(t, got.goSubscription)
	require.Equal(t, opencode.PublicBearer, got.token)
	transport, ok := got.transport.(*opencode.Transport)
	require.True(t, ok, "the Console catalog is read through the provider transport")
	require.Empty(t, transport.Path, "the public bearer reads no login file")
}

// The Go catalog needs the real subscription credential, and reading it through
// the login-file transport is what keeps a nearly expired token refreshed.
func TestOpenCodeModelsSourceReadsTheGoCatalogThroughTheLogin(t *testing.T) {
	path := filepath.Join(t.TempDir(), opencode.CredentialsFile)
	require.NoError(t, opencode.Save(path, opencode.Credentials{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().Add(time.Hour),
	}))
	src := newOpenCodeModelsSource(path)
	var got capturedOpenCodeFetch
	src.fetch = stubOpenCodeFetch(t, &got, "glm-5.3")

	_, err := src.OpenCodeModels(context.Background(), true)
	require.NoError(t, err)
	require.True(t, got.goSubscription)
	require.Equal(t, "access-1", got.token)
	transport, ok := got.transport.(*opencode.Transport)
	require.True(t, ok)
	require.Equal(t, path, transport.Path)
}

// A machine with no login file can still read the Go catalog with an exported
// API key.
func TestOpenCodeModelsSourceFallsBackToTheAPIKeyEnvironment(t *testing.T) {
	t.Setenv(opencode.APIKeyEnv, "sk-env")
	src := newOpenCodeModelsSource(filepath.Join(t.TempDir(), opencode.CredentialsFile))
	var got capturedOpenCodeFetch
	src.fetch = stubOpenCodeFetch(t, &got, "glm-5.3")

	_, err := src.OpenCodeModels(context.Background(), true)
	require.NoError(t, err)
	require.True(t, got.goSubscription)
	require.Equal(t, "sk-env", got.token)
	transport, ok := got.transport.(*opencode.Transport)
	require.True(t, ok)
	require.Empty(t, transport.Path, "a static key reads no login file")
}

func TestOpenCodeModelsSourceReportsWhetherThisMachineIsSignedIn(t *testing.T) {
	t.Setenv(opencode.APIKeyEnv, "")
	path := filepath.Join(t.TempDir(), opencode.CredentialsFile)
	require.False(t, newOpenCodeModelsSource(path).OpenCodeSignedIn(),
		"neither a login file nor a key means no Go catalog is asked for")

	require.NoError(t, opencode.Save(path, opencode.Credentials{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().Add(time.Hour),
	}))
	require.True(t, newOpenCodeModelsSource(path).OpenCodeSignedIn())

	require.NoError(t, os.Remove(path))
	t.Setenv(opencode.APIKeyEnv, "sk-env")
	require.True(t, newOpenCodeModelsSource(path).OpenCodeSignedIn())
}

// Asking for Go models with no credential at all is an error naming the login
// requirement, and it costs no request.
func TestOpenCodeModelsSourceNamesTheLoginWhenTheGoSurfaceHasNoCredential(t *testing.T) {
	t.Setenv(opencode.APIKeyEnv, "")
	src := newOpenCodeModelsSource(filepath.Join(t.TempDir(), opencode.CredentialsFile))
	called := false
	src.fetch = func(context.Context, http.RoundTripper, bool, string) ([]opencode.ModelInfo, error) {
		called = true
		return nil, nil
	}

	_, err := src.OpenCodeModels(context.Background(), true)
	require.Error(t, err)
	require.ErrorContains(t, err, "/connect")
	require.ErrorContains(t, err, opencode.APIKeyEnv)
	require.False(t, called, "no request may leave without a credential")
}

// The public constructor resolves the login file inside the home the gateway
// runs on, which is the same file /connect and the web login write.
func TestNewOpenCodeModelsSourceUsesTheLoginFileInHome(t *testing.T) {
	home := t.TempDir()
	src, ok := NewOpenCodeModelsSource(home).(openCodeModelsSource)
	require.True(t, ok)
	require.Equal(t, filepath.Join(home, opencode.CredentialsFile), src.path)
	require.NotNil(t, src.fetch, "the default source fetches the live catalog")
}
