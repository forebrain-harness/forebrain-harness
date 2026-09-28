package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

type captureTransport struct {
	request *http.Request
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.request = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(http.NoBody),
		Request:    req,
	}, nil
}

// jwtWithExp builds a token whose payload carries exp. Only the payload is
// ever read — nothing here verifies a signature — so the header and signature
// are placeholders.
func quote(s string) string { return strconv.Quote(s) }

func jwtWithExp(t *testing.T, exp time.Time) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"exp": exp.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

// The file forebrain reads is the Codex CLI's, byte for byte. This is the whole
// point of the feature: a developer who has run `codex login` can point forebrain
// at that file — or drop it in FOREBRAIN_HOME — and it works, with no conversion
// step and no second login.
func TestLoadReadsTheCodexAuthFileFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	access := jwtWithExp(t, time.Now().Add(2*time.Hour))
	raw := `{
	  "auth_mode": "chatgpt",
	  "OPENAI_API_KEY": null,
	  "tokens": {
	    "id_token": "id-token",
	    "access_token": ` + quote(access) + `,
	    "refresh_token": "refresh-token",
	    "account_id": "acct_123"
	  },
	  "last_refresh": "2026-09-02T00:00:00.000000000Z"
	}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.AccessToken != access || got.RefreshToken != "refresh-token" {
		t.Fatalf("tokens = %+v, want them read from the nested tokens object", got)
	}
	if got.AccountID != "acct_123" || got.IDToken != "id-token" || got.AuthMode != "chatgpt" {
		t.Fatalf("credentials = %+v, want account id, id token and auth mode carried through", got)
	}
	// The Codex file has no expiry field, so it has to come from the token.
	// Getting this wrong means either refusing a valid login or sending an
	// expired token on every request.
	if time.Until(got.ExpiresAt) < time.Hour {
		t.Fatalf("ExpiresAt = %v, want it read from the access token's exp claim", got.ExpiresAt)
	}
}

// A token whose expiry cannot be read is treated as expired, so the transport
// refreshes rather than sending something the server will reject.
func TestLoadTreatsAnUnreadableExpiryAsExpired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(`{"tokens":{"access_token":"not-a-jwt","refresh_token":"r"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.ExpiresAt.IsZero() {
		t.Fatalf("ExpiresAt = %v, want the zero time so the transport refreshes", got.ExpiresAt)
	}
}

func TestLoadRejectsIncompleteCredentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(`{"tokens":{"access_token":"a"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load with no refresh token = nil error, want a refusal: a login that cannot refresh is not usable")
	}
}

// Save must write the Codex shape back to the same file. A refresh rotates the
// refresh token, so writing a different shape or a different path would leave
// the Codex CLI holding a token the server has already invalidated.
func TestSaveRoundTripsInTheCodexShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	access := jwtWithExp(t, time.Now().Add(time.Hour))
	want := Credentials{
		AccessToken: access, RefreshToken: "refresh-secret",
		IDToken: "id-secret", AccountID: "acct_123", AuthMode: "chatgpt",
	}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %o, want 600: this file holds OAuth tokens", info.Mode().Perm())
	}

	var onDisk map[string]any
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(blob, &onDisk); err != nil {
		t.Fatal(err)
	}
	tokens, ok := onDisk["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("on-disk shape = %v, want a nested tokens object like Codex writes", onDisk)
	}
	if tokens["access_token"] != access || tokens["refresh_token"] != "refresh-secret" {
		t.Fatalf("tokens on disk = %v", tokens)
	}
	if onDisk["auth_mode"] != "chatgpt" {
		t.Fatalf("auth_mode on disk = %v, want it preserved", onDisk["auth_mode"])
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken || got.AccountID != want.AccountID {
		t.Fatalf("round-tripped credentials = %+v", got)
	}
}

func TestTransportSendsTheCodexHeaders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	access := jwtWithExp(t, time.Now().Add(time.Hour))
	if err := Save(path, Credentials{AccessToken: access, RefreshToken: "r", AccountID: "acct_123"}); err != nil {
		t.Fatal(err)
	}

	capture := &captureTransport{}
	transport := &Transport{Path: path, Base: capture}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, CodexBaseURL+"/responses", nil)
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got := capture.request.Header.Get("Authorization"); got != "Bearer "+access {
		t.Fatalf("Authorization header does not carry the access token")
	}
	if got := capture.request.Header.Get("ChatGPT-Account-ID"); got != "acct_123" {
		t.Fatalf("ChatGPT-Account-ID = %q", got)
	}
	if got := capture.request.Header.Get("OpenAI-Beta"); got != "codex-1" {
		t.Fatalf("OpenAI-Beta = %q", got)
	}
	if got := capture.request.Header.Get("session-id"); got != "" {
		t.Fatalf("a request with no cache key must not invent a session: %q", got)
	}
}

// TestTransportSendsTheCacheAffinityHeaders pins the headers the ChatGPT
// backend keys prompt-cache routing on: session-id and thread-id, carrying
// exactly the prompt_cache_key the body carries. The body key alone does not
// route a request to the machine holding the conversation's cache.
func TestTransportSendsTheCacheAffinityHeaders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := Save(path, Credentials{AccessToken: jwtWithExp(t, time.Now().Add(time.Hour)), RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	capture := &captureTransport{}
	transport := &Transport{Path: path, Base: capture}
	ctx := llm.WithPromptCacheKey(context.Background(), "cli-6d1c")
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, CodexBaseURL+"/responses", nil)
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	want := openAIPromptCacheKey(ctx)
	if want != "cli-6d1c" {
		t.Fatalf("body key = %q", want)
	}
	for _, name := range []string{"session-id", "thread-id"} {
		if got := capture.request.Header.Get(name); got != want {
			t.Fatalf("%s = %q, want the body's prompt_cache_key %q", name, got, want)
		}
	}
}

// The path rules are what a user configures, so each branch is asserted.
func TestResolvePathRules(t *testing.T) {
	home := filepath.Join("/srv", "forebrain-home")
	userHome, _ := os.UserHomeDir()
	for _, tc := range []struct {
		name       string
		configured string
		want       string
	}{
		{"default is auth.json in FOREBRAIN_HOME", "", filepath.Join(home, "auth.json")},
		{"absolute path is used as given", "/etc/forebrain/creds.json", "/etc/forebrain/creds.json"},
		{"relative path resolves against FOREBRAIN_HOME", "secrets/auth.json", filepath.Join(home, "secrets/auth.json")},
		{"tilde expands to the user's home", "~/.codex/auth.json", filepath.Join(userHome, ".codex/auth.json")},
		{"blank is treated as unset", "   ", filepath.Join(home, "auth.json")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolvePath(home, tc.configured); got != tc.want {
				t.Fatalf("ResolvePath(%q) = %q, want %q", tc.configured, got, tc.want)
			}
		})
	}
}

func TestCredentialsPathUsesTheInstalledOverride(t *testing.T) {
	t.Cleanup(func() { SetCredentialsPath("") })
	home := filepath.Join("/srv", "forebrain-home")

	if got, want := CredentialsPath(home), filepath.Join(home, "auth.json"); got != want {
		t.Fatalf("with no override CredentialsPath = %q, want %q", got, want)
	}
	SetCredentialsPath("/etc/forebrain/creds.json")
	if got := CredentialsPath(home); got != "/etc/forebrain/creds.json" {
		t.Fatalf("with an override CredentialsPath = %q", got)
	}
	SetCredentialsPath("")
	if got, want := CredentialsPath(home), filepath.Join(home, "auth.json"); got != want {
		t.Fatalf("after clearing CredentialsPath = %q, want the default %q", got, want)
	}
}
