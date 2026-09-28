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
