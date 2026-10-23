package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// A web login hands the authorization URL to its own page instead of opening a
// browser on whatever host the gateway runs on. The flow is otherwise the same
// one Login runs; this proves the URL — with the PKCE parameters the callback
// will be checked against — reaches the injected callback.
func TestLoginWithBrowserHandsTheAuthorizationURLToTheCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got string
	calls := 0
	_, err := LoginWithBrowser(ctx, filepath.Join(t.TempDir(), "auth.json"), io.Discard, func(url string) error {
		calls++
		got = url
		cancel()
		return nil
	})
	if calls == 0 {
		t.Skipf("the loopback login listener is unavailable on this machine: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the canceled flow", err)
	}
	for _, want := range []string{Issuer + "/oauth/authorize?", "client_id=" + ClientID, "code_challenge_method=S256"} {
		if !strings.Contains(got, want) {
			t.Fatalf("authorization URL %q is missing %q", got, want)
		}
	}
}

// stubTokenEndpoint stands the token endpoint up locally, the way the OpenCode
// tests stub their console base: what the endpoint answers decides whether a
// login is dead, so the classification tests need to serve the answers.
func stubTokenEndpoint(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	old := tokenEndpoint
	tokenEndpoint = server.URL + "/oauth/token"
	t.Cleanup(func() { tokenEndpoint = old })
	return server.URL
}

// exchange itself never judges a login dead: it reports the endpoint's status
// and the OAuth machine code untouched, because the same invalid_grant answer
// to an authorization code (the login flow's call site) means the code
// expired, not the login. Only the refresh call site reclassifies.
func TestExchangeTypesTokenEndpointRefusals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		wantCode string
	}{
		{"invalid grant keeps its status and code", http.StatusBadRequest, `{"error":"invalid_grant","error_description":"refresh token revoked"}`, "invalid_grant"},
		{"other refusals keep their status and code", http.StatusForbidden, `{"error":"unauthorized_client"}`, "unauthorized_client"},
		{"object-shaped errors yield their code", http.StatusBadRequest, `{"error":{"code":"invalid_grant","message":"revoked"}}`, "invalid_grant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := exchange(context.Background(), url.Values{"grant_type": {"refresh_token"}})
			var api *llm.APIError
			if !errors.As(err, &api) {
				t.Fatalf("err = %v, want *llm.APIError", err)
			}
			if api.StatusCode != tc.status {
				t.Fatalf("StatusCode = %d, want %d: exchange must not reclassify", api.StatusCode, tc.status)
			}
			if api.Code != tc.wantCode {
				t.Fatalf("Code = %q, want %q", api.Code, tc.wantCode)
			}
			if !strings.Contains(api.Message, "ChatGPT token endpoint returned") {
				t.Fatalf("Message = %q, want the endpoint's answer in it", api.Message)
			}
		})
	}
}

// A dead refresh token is the login being gone, so the refresh path answers
// 401 in the typed shape the OpenCode refresh uses — classifiers upstream then
// offer "sign in again" instead of retrying a grant that cannot succeed.
func TestTransportRefreshTreatsInvalidGrantAsExpiredLogin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	// A non-JWT access token reads as already expired, so current() refreshes.
	if err := os.WriteFile(path, []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"not-a-jwt","refresh_token":"dead"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stubTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	transport := &Transport{Path: path}
	_, err := transport.current(context.Background())
	var api *llm.APIError
	if !errors.As(err, &api) {
		t.Fatalf("err = %v, want *llm.APIError", err)
	}
	if api.StatusCode != http.StatusUnauthorized {
		t.Fatalf("StatusCode = %d, want 401", api.StatusCode)
	}
	if !strings.Contains(api.Message, "sign in again") {
		t.Fatalf("Message = %q, want the sign-in-again wording", api.Message)
	}
	if strings.Contains(api.Message, "/connect") {
		t.Fatalf("Message = %q, must stay surface-neutral: the web login renders it verbatim", api.Message)
	}
}

// The token endpoint retires a refresh token as soon as it exchanges it, so two
// clients of one login file must not both exchange it: the loser presents a
// token the endpoint has already rotated and would report a dead login that is
// in fact alive. Every turn, subagent and codex probe builds its own Transport
// on the same file, so the serialization cannot be per instance.
func TestTransportsSharingAFileRefreshOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	// A non-JWT-free shape: the initial access token is a real-looking JWT
	// whose exp is already behind us, so current() refreshes.
	raw := `{"auth_mode":"chatgpt","tokens":{"access_token":` + quote(jwtWithExp(t, time.Now().Add(-time.Minute))) + `,"refresh_token":"refresh-1"}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	renewed := jwtWithExp(t, time.Now().Add(time.Hour))
	var mu sync.Mutex
	calls := 0
	current := "refresh-1"
	stubTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse token form: %v", err)
		}
		sent := r.PostForm.Get("refresh_token")
		mu.Lock()
		calls++
		rotated := sent == current
		if rotated {
			current = "refresh-2"
		}
		mu.Unlock()
		// Hold the response briefly so the two exchanges really overlap.
		time.Sleep(20 * time.Millisecond)
		if !rotated {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":` + quote(renewed) + `,"refresh_token":"refresh-2","expires_in":3600}`))
	})

	transports := []*Transport{{Path: path}, {Path: path}}
	var wg sync.WaitGroup
	errs := make(chan error, len(transports))
	for _, transport := range transports {
		wg.Add(1)
		go func(transport *Transport) {
			defer wg.Done()
			if _, err := transport.current(context.Background()); err != nil {
				errs <- err
			}
		}(transport)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent current(): %v", err)
	}
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Fatalf("refresh calls = %d, want exactly 1: the second transport exchanged a refresh token the endpoint had already rotated", got)
	}
}

// The TUI and the gateway are two processes on one login file, so one of them
// can rotate the refresh token and persist it while the other is mid-exchange.
// The refused exchange is then not a dead login: the file already holds a live
// one, and making the user sign in again would be busywork.
func TestTransportAdoptsALoginAnotherProcessRotated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	// A non-JWT access token reads as already expired, so current() refreshes.
	if err := os.WriteFile(path, []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"not-a-jwt","refresh_token":"refresh-1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rotated := jwtWithExp(t, time.Now().Add(time.Hour))
	stubTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		// The other process got there first and wrote the pair it received,
		// which retires the token this request is still presenting.
		if err := Save(path, Credentials{AccessToken: rotated, RefreshToken: "refresh-other"}); err != nil {
			t.Errorf("save the rotated login: %v", err)
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})

	transport := &Transport{Path: path}
	got, err := transport.current(context.Background())
	if err != nil {
		t.Fatalf("current(): %v, want the login the other process wrote", err)
	}
	if got.AccessToken != rotated || got.RefreshToken != "refresh-other" {
		t.Fatalf("credentials = %+v, want the rotated pair already on disk", got)
	}
}

// A failing endpoint is not a dead login: no OAuth code, no 401 — the error
// stays a wrapped failure so "log in again" is never offered for it.
func TestTransportRefreshKeepsOtherFailturesUnjudged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"not-a-jwt","refresh_token":"live"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"server errors stay unreachable", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}},
		{"other oauth refusals stay unreclassified", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"temporarily_unavailable"}`))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubTokenEndpoint(t, tc.handler)
			transport := &Transport{Path: path}
			_, err := transport.current(context.Background())
			var api *llm.APIError
			if !errors.As(err, &api) || api.StatusCode == http.StatusUnauthorized {
				t.Fatalf("err = %v, want a failure that does not read as an expired login", err)
			}
		})
	}

	// A transport failure never reaches the token endpoint at all: it must
	// not carry an API refusal shape, or a broken network would read as a
	// dead login.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	old := tokenEndpoint
	tokenEndpoint = deadURL + "/oauth/token"
	t.Cleanup(func() { tokenEndpoint = old })
	transport := &Transport{Path: path}
	_, err := transport.current(context.Background())
	var api *llm.APIError
	if errors.As(err, &api) {
		t.Fatalf("err = %v, a transport failure must not read as an API refusal", err)
	}
}
