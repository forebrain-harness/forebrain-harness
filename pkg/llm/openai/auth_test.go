package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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

// The callback port is open to the whole machine while a login is pending, so
// anything can knock on it: a prefetcher, another page, another program. A
// request whose state does not match is not this sign-in's answer and must not
// be able to end it — only the authorization server's own reply may.
func TestLoginWithBrowserIgnoresACallbackWithTheWrongState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stubTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"`+jwtWithExp(t, time.Now().Add(time.Hour))+`","refresh_token":"refresh-1","expires_in":3600}`)
	})
	path := filepath.Join(t.TempDir(), "auth.json")

	attempted := make(chan struct{})
	browserCalled := make(chan struct{})
	_, err := LoginWithBrowser(ctx, path, io.Discard, func(authURL string) error {
		close(browserCalled)
		parsed, parseErr := url.Parse(authURL)
		if parseErr != nil {
			t.Errorf("authorization URL: %v", parseErr)
			return nil
		}
		redirect := parsed.Query().Get("redirect_uri")
		state := parsed.Query().Get("state")
		go func() {
			defer close(attempted)
			stray, strayErr := http.Get(redirect + "?code=stray&state=WRONG")
			if strayErr != nil {
				t.Errorf("stray callback: %v", strayErr)
				return
			}
			_ = stray.Body.Close()
			if stray.StatusCode != http.StatusBadRequest {
				t.Errorf("stray callback = %d, want 400", stray.StatusCode)
			}
			real, realErr := http.Get(redirect + "?code=real-code&state=" + url.QueryEscape(state))
			if realErr != nil {
				t.Errorf("real callback: %v", realErr)
				return
			}
			_ = real.Body.Close()
		}()
		return nil
	})
	select {
	case <-browserCalled:
	default:
		t.Skipf("the loopback login listener is unavailable on this machine: %v", err)
	}
	select {
	case <-attempted:
	case <-time.After(3 * time.Second):
		t.Fatal("the stray and the real callback never both completed")
	}
	if err != nil {
		t.Fatalf("LoginWithBrowser: %v, want the login to survive a stranger's callback", err)
	}
	saved, loadErr := Load(path)
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	if saved.RefreshToken != "refresh-1" {
		t.Fatalf("RefreshToken = %q, want refresh-1", saved.RefreshToken)
	}
}

// A browser that retries or refreshes the callback address sends several
// requests, and only the first one decides the login. The others must not
// block a handler that the shutdown then waits for: the flow has to return.
func TestLoginWithBrowserSurvivesRepeatedCallbacks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stubTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"`+jwtWithExp(t, time.Now().Add(time.Hour))+`","refresh_token":"refresh-1","expires_in":3600}`)
	})
	path := filepath.Join(t.TempDir(), "auth.json")

	started := make(chan struct{})
	browserCalled := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := LoginWithBrowser(ctx, path, io.Discard, func(authURL string) error {
			close(browserCalled)
			parsed, parseErr := url.Parse(authURL)
			if parseErr != nil {
				t.Errorf("authorization URL: %v", parseErr)
				return nil
			}
			redirect := parsed.Query().Get("redirect_uri")
			state := parsed.Query().Get("state")
			go func() {
				defer close(started)
				var wg sync.WaitGroup
				for i := 0; i < 4; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						resp, getErr := http.Get(redirect + "?code=real-code&state=" + url.QueryEscape(state))
						if getErr != nil {
							return
						}
						_ = resp.Body.Close()
					}()
				}
				wg.Wait()
			}()
			return nil
		})
		done <- err
	}()
	select {
	case <-browserCalled:
	case <-time.After(3 * time.Second):
		t.Skip("the loopback login listener is unavailable on this machine")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("LoginWithBrowser: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("repeated callbacks left the login waiting: the flow never returned")
	}
}

// manualLogin drives one LoginWithManualCallback from a test. submit hands the
// flow a pasted address the way the page does and returns the flow's answer.
type manualLogin struct {
	redirectURI string
	state       string
	manual      chan ManualCallback
	creds       chan Credentials
	done        chan error
}

// startManualLogin begins a flow and waits until it has handed its
// authorization URL out, so the caller can build callbacks the flow will
// recognise. occupyPorts takes 1455 and 1457 first, which is how a gateway on a
// machine where the loopback is unusable still gets a flow to carry by hand.
func startManualLogin(t *testing.T, path string, occupyPorts bool) *manualLogin {
	t.Helper()
	if occupyPorts {
		for _, port := range []int{1455, 1457} {
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				t.Skipf("cannot occupy localhost:%d on this machine: %v", port, err)
			}
			t.Cleanup(func() { _ = ln.Close() })
		}
	}
	m := &manualLogin{
		manual: make(chan ManualCallback),
		creds:  make(chan Credentials, 1),
		done:   make(chan error, 1),
	}
	opened := make(chan string, 1)
	go func() {
		creds, err := LoginWithManualCallback(context.Background(), path, io.Discard, func(authURL string) error {
			select {
			case opened <- authURL:
			default:
			}
			return nil
		}, m.manual)
		m.creds <- creds
		m.done <- err
	}()
	select {
	case authURL := <-opened:
		parsed, err := url.Parse(authURL)
		if err != nil {
			t.Fatalf("authorization URL: %v", err)
		}
		m.redirectURI = parsed.Query().Get("redirect_uri")
		m.state = parsed.Query().Get("state")
	case <-time.After(5 * time.Second):
		t.Fatal("the flow never handed its authorization URL out")
	}
	return m
}

func (m *manualLogin) submit(t *testing.T, pasted string) error {
	t.Helper()
	reply := make(chan error, 1)
	select {
	case m.manual <- ManualCallback{URL: pasted, Reply: reply}:
	case <-time.After(2 * time.Second):
		t.Fatalf("the flow took no callback: %q", pasted)
	}
	select {
	case err := <-reply:
		return err
	case <-time.After(2 * time.Second):
		t.Fatalf("the flow answered no callback: %q", pasted)
	}
	return nil
}

func (m *manualLogin) wait(t *testing.T) (Credentials, error) {
	t.Helper()
	select {
	case creds := <-m.creds:
		return creds, <-m.done
	case <-time.After(10 * time.Second):
		t.Fatal("the manual login never finished")
	}
	return Credentials{}, nil
}

func stubValidTokenGrant(t *testing.T) {
	t.Helper()
	stubTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"`+jwtWithExp(t, time.Now().Add(time.Hour))+`","refresh_token":"refresh-1","expires_in":3600}`)
	})
}

// A gateway that cannot be reached by the browser's redirect — a server, a
// container, another machine — still finishes the login when the user copies
// the address out of the browser and pastes it back. The loopback listener is
// not just unused here: both of its ports are taken, so the flow has to run
// without one at all.
func TestManualCallbackCompletesTheLogin(t *testing.T) {
	stubValidTokenGrant(t)
	path := filepath.Join(t.TempDir(), "auth.json")
	m := startManualLogin(t, path, true)

	if want := "http://localhost:1455/auth/callback"; m.redirectURI != want {
		t.Fatalf("redirect_uri = %q, want the fixed %q", m.redirectURI, want)
	}
	if err := m.submit(t, m.redirectURI+"?code=real&state="+m.state); err != nil {
		t.Fatalf("a real callback was rejected: %v", err)
	}
	creds, err := m.wait(t)
	if err != nil {
		t.Fatalf("LoginWithManualCallback: %v", err)
	}
	if creds.RefreshToken != "refresh-1" {
		t.Fatalf("credentials = %+v", creds)
	}
	saved, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.RefreshToken != "refresh-1" {
		t.Fatalf("saved RefreshToken = %q, want refresh-1", saved.RefreshToken)
	}
}

// Whatever the user pastes is text from a person, so a wrong paste is a try
// again, not the end: the flow says why it is not its callback and keeps
// waiting for the right one.
func TestManualCallbackRejectsWithoutEndingTheLogin(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input func(m *manualLogin) string
		want  error
	}{
		{
			name:  "plain text",
			input: func(*manualLogin) string { return "hello" },
			want:  ErrCallbackNotACallback,
		},
		{
			name:  "another sign-in's state",
			input: func(m *manualLogin) string { return m.redirectURI + "?code=x&state=WRONG" },
			want:  ErrCallbackStateMismatch,
		},
		{
			name: "some other site",
			input: func(m *manualLogin) string {
				return "http://evil.example.com:1455/auth/callback?code=x&state=" + m.state
			},
			want: ErrCallbackNotACallback,
		},
		{
			name: "another port",
			input: func(m *manualLogin) string {
				return "http://localhost:9999/auth/callback?code=x&state=" + m.state
			},
			want: ErrCallbackNotACallback,
		},
		{
			name: "another path",
			input: func(m *manualLogin) string {
				return "http://localhost:1455/other?code=x&state=" + m.state
			},
			want: ErrCallbackNotACallback,
		},
		{
			name: "no code",
			input: func(m *manualLogin) string {
				return m.redirectURI + "?state=" + m.state
			},
			want: ErrCallbackNoCode,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubValidTokenGrant(t)
			m := startManualLogin(t, filepath.Join(t.TempDir(), "auth.json"), true)
			got := m.submit(t, tc.input(m))
			if !errors.Is(got, tc.want) {
				t.Fatalf("rejection = %v, want %v", got, tc.want)
			}
			// Still waiting: the login has not ended, and the right address
			// still completes it.
			if err := m.submit(t, m.redirectURI+"?code=real&state="+m.state); err != nil {
				t.Fatalf("the login no longer takes a callback after a rejected one: %v", err)
			}
			if _, err := m.wait(t); err != nil {
				t.Fatalf("LoginWithManualCallback: %v", err)
			}
		})
	}
}

// The address bar carries what the browser put there, not what a parser would
// like: whitespace from a careless copy, 127.0.0.1 instead of localhost, no
// scheme at all once a chat client has trimmed it, and query parameters the
// request never named.
func TestManualCallbackAcceptsWhatABrowserShows(t *testing.T) {
	const (
		redirect = "http://localhost:1455/auth/callback"
		state    = "st-1"
	)
	for _, tc := range []struct {
		name  string
		input string
	}{
		{name: "padded with whitespace", input: "  \n" + redirect + "?code=c&state=" + state + "\t "},
		{name: "loopback address instead of a name", input: "http://127.0.0.1:1455/auth/callback?code=c&state=" + state},
		{name: "no scheme", input: "localhost:1455/auth/callback?code=c&state=" + state},
		{name: "extra parameters", input: redirect + "?code=c&scope=openid%20profile&state=" + state + "&session_state=s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, providerErr, rejected := parseCallback(tc.input, redirect, state)
			if rejected != nil {
				t.Fatalf("parseCallback(%q) rejected a callback a browser can show: %v", tc.input, rejected)
			}
			if providerErr != nil {
				t.Fatalf("providerErr = %v, want none", providerErr)
			}
			if code != "c" {
				t.Fatalf("code = %q, want c", code)
			}
		})
	}
}

// The authorization server's own refusal is an answer, not a bad paste: it
// belongs to this sign-in, so it ends the login with the reason it gave — the
// same wording the loopback listener produces for it.
func TestManualCallbackCarriesTheProvidersOwnRefusal(t *testing.T) {
	stubValidTokenGrant(t)
	m := startManualLogin(t, filepath.Join(t.TempDir(), "auth.json"), true)

	if err := m.submit(t, m.redirectURI+"?error=access_denied&error_description=nope&state="+m.state); err != nil {
		t.Fatalf("the provider's own refusal was rejected as a bad paste: %v", err)
	}
	_, err := m.wait(t)
	if err == nil || !strings.Contains(err.Error(), "ChatGPT sign-in failed: nope") {
		t.Fatalf("err = %v, want the provider's refusal", err)
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

type fakeCredentialsStore struct {
	loaded  int
	saved   int
	bytes   json.RawMessage
	loadErr error
}

func (f *fakeCredentialsStore) LoadCredentials(context.Context) (json.RawMessage, error) {
	f.loaded++
	return f.bytes, f.loadErr
}

func (f *fakeCredentialsStore) SaveCredentials(_ context.Context, raw json.RawMessage) error {
	f.saved++
	f.bytes = raw
	return nil
}

// TestStoredCredentialsDefaultToFile pins the port's default: with no store
// installed, the stored credentials are the file at CredentialsPath(home) —
// absent reads as "no login", a write lands in the file, and nothing else
// changes.
func TestStoredCredentialsDefaultToFile(t *testing.T) {
	SetCredentialsStore(nil)
	ctx := context.Background()
	home := t.TempDir()

	if raw, err := LoadStoredCredentials(ctx, home); err != nil || raw != nil {
		t.Fatalf("absent credentials = (%q, %v), want (nil, nil)", raw, err)
	}
	if err := SaveStoredCredentials(ctx, home, json.RawMessage(`{"tokens":{"access_token":"a"}}`)); err != nil {
		t.Fatal(err)
	}
	raw, err := LoadStoredCredentials(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"tokens":{"access_token":"a"}}` {
		t.Fatalf("roundtrip = %q", raw)
	}
	if _, err := os.Stat(filepath.Join(home, CredentialsFile)); err != nil {
		t.Fatalf("the default backend did not write the file: %v", err)
	}
	info, err := os.Stat(filepath.Join(home, CredentialsFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file mode = %v, want 0600", info.Mode())
	}
}

// TestStoredCredentialsGoThroughInstalledStore pins the installed half: once
// a store is installed, loads and saves reach it and the file is not touched.
func TestStoredCredentialsGoThroughInstalledStore(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	fake := &fakeCredentialsStore{bytes: json.RawMessage(`{"tokens":{"access_token":"from-store"}}`)}
	SetCredentialsStore(fake)
	t.Cleanup(func() { SetCredentialsStore(nil) })

	raw, err := LoadStoredCredentials(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	if fake.loaded != 1 {
		t.Fatalf("store loads = %d, want 1", fake.loaded)
	}
	if string(raw) != `{"tokens":{"access_token":"from-store"}}` {
		t.Fatalf("load = %q, want the store's bytes", raw)
	}

	if err := SaveStoredCredentials(ctx, home, json.RawMessage(`{"tokens":{"access_token":"next"}}`)); err != nil {
		t.Fatal(err)
	}
	if fake.saved != 1 || string(fake.bytes) != `{"tokens":{"access_token":"next"}}` {
		t.Fatalf("store save = (%d, %q)", fake.saved, fake.bytes)
	}
	if _, err := os.Stat(filepath.Join(home, CredentialsFile)); !os.IsNotExist(err) {
		t.Fatal("the file default ran although a store is installed")
	}
}
