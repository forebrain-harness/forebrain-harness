package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// captureTransport records the request a provider client produced and answers
// with an empty 200, so a header assertion never touches the network.
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

// headerRecorder records the Authorization header of every request it answers,
// so a shared transport can be observed from concurrent goroutines.
type headerRecorder struct {
	headers chan string
}

func (h *headerRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	h.headers <- req.Header.Get("Authorization")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(http.NoBody),
		Request:    req,
	}, nil
}

func writeCredentials(t *testing.T, path string, c Credentials) {
	t.Helper()
	if err := Save(path, c); err != nil {
		t.Fatalf("Save credentials: %v", err)
	}
}

// refreshServer fakes the token endpoint for a refresh grant and counts the
// calls, so a test can prove that concurrent requests refresh once.
func refreshServer(t *testing.T, access, refresh string, calls *int, onBody func(map[string]any)) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/device/token" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		*calls++
		mu.Unlock()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode refresh body: %v", err)
		}
		if onBody != nil {
			onBody(body)
		}
		// Hold the response briefly so concurrent callers really overlap.
		time.Sleep(20 * time.Millisecond)
		writeJSON(t, w, http.StatusOK, map[string]any{
			"access_token":  access,
			"refresh_token": refresh,
			"expires_in":    3600,
		})
	}))
}

func TestTransportRefreshesExpiredCredentialsAndRewritesTheFile(t *testing.T) {
	calls := 0
	var body map[string]any
	server := refreshServer(t, "access-2", "refresh-2", &calls, func(got map[string]any) { body = got })
	defer server.Close()
	stubConsole(t, server.URL)

	path := filepath.Join(t.TempDir(), "opencode.json")
	writeCredentials(t, path, Credentials{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().Add(-time.Minute),
		Email:        "dev@example.com",
		OrgID:        "org_a",
		OrgName:      "Alpha",
	})

	captured := &captureTransport{}
	transport := &Transport{Path: path, Base: captured}
	req, err := http.NewRequest(http.MethodPost, "https://opencode.ai/inference/openai/v1/chat/completions", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := llm.WithAgentSessionID(context.Background(), "session-1")
	resp, err := transport.RoundTrip(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()

	if calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls)
	}
	if body["grant_type"] != "refresh_token" || body["refresh_token"] != "refresh-1" || body["client_id"] != ClientID {
		t.Fatalf("refresh body = %v", body)
	}
	if got := captured.request.Header.Get("Authorization"); got != "Bearer access-2" {
		t.Fatalf("Authorization = %q, want the refreshed token", got)
	}
	if got := captured.request.Header.Get("User-Agent"); !strings.HasPrefix(got, "forebrain-harness/") {
		t.Fatalf("User-Agent = %q, want a forebrain-harness/<version> identity", got)
	}
	if got := captured.request.Header.Get("x-opencode-session"); got != "session-1" {
		t.Fatalf("x-opencode-session = %q, want the session id from ctx", got)
	}
	// The refresh token rotated; the file has to carry the new one or the next
	// refresh would present a token the server has already invalidated.
	saved, err := Load(path)
	if err != nil {
		t.Fatalf("Load refreshed credentials: %v", err)
	}
	if saved.AccessToken != "access-2" || saved.RefreshToken != "refresh-2" {
		t.Fatalf("saved tokens = %q/%q, want the rotated pair", saved.AccessToken, saved.RefreshToken)
	}
	if saved.Email != "dev@example.com" || saved.OrgID != "org_a" {
		t.Fatalf("profile fields were lost by the refresh: %+v", saved)
	}
}

// Several goroutines noticing the same expired token must produce one refresh,
// not one each: a refresh rotates the token, so a second concurrent refresh is
// answering with a token the server has already retired and would log the user
// out.
func TestTransportRefreshesOnceForConcurrentRequests(t *testing.T) {
	calls := 0
	server := refreshServer(t, "access-2", "refresh-2", &calls, nil)
	defer server.Close()
	stubConsole(t, server.URL)

	path := filepath.Join(t.TempDir(), "opencode.json")
	writeCredentials(t, path, Credentials{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().Add(-time.Minute),
	})
	// One transport shared by every goroutine: the mutex that makes the refresh
	// single is per transport, so this is the shape production has.
	shared := &Transport{Path: path, Base: &headerRecorder{headers: make(chan string, 16)}}
	const workers = 5
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodGet, "https://opencode.ai/inference/v1/models", nil)
			if err != nil {
				errs <- err
				return
			}
			resp, err := shared.RoundTrip(req)
			if err != nil {
				errs <- err
				return
			}
			resp.Body.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent RoundTrip: %v", err)
	}
	if calls != 1 {
		t.Fatalf("refresh calls = %d, want exactly 1", calls)
	}
	recorder := shared.Base.(*headerRecorder)
	close(recorder.headers)
	sent := 0
	for got := range recorder.headers {
		sent++
		if got != "Bearer access-2" {
			t.Fatalf("Authorization = %q, want the refreshed token", got)
		}
	}
	if sent != workers {
		t.Fatalf("header captures = %d, want %d", sent, workers)
	}
}

// A dead refresh token is the one failure a user can fix themselves, so it has
// to arrive as a 401-shaped error a surface can turn into "log in again".
func TestTransportInvalidGrantRequiresLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
	}))
	defer server.Close()
	stubConsole(t, server.URL)

	path := filepath.Join(t.TempDir(), "opencode.json")
	writeCredentials(t, path, Credentials{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().Add(-time.Minute),
	})
	transport := &Transport{Path: path, Base: &captureTransport{}}
	req, err := http.NewRequest(http.MethodPost, "https://opencode.ai/inference/openai/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(req); err == nil {
		t.Fatal("RoundTrip succeeded with a dead refresh token")
	} else {
		var apiErr *llm.APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("error = %v, want a *llm.APIError", err)
		}
		if apiErr.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", apiErr.StatusCode)
		}
		if !strings.Contains(apiErr.Error(), "/connect") {
			t.Fatalf("message = %q, want it to point at /connect", apiErr.Error())
		}
	}
}

func TestTransportKeyModeSendsAStaticBearer(t *testing.T) {
	// A key transport must never call the console: point the console at a
	// server that fails the test if it is reached.
	stubConsole(t, "http://127.0.0.1:1")
	captured := &captureTransport{}
	transport := NewKeyTransport(PublicBearer)
	transport.Base = captured

	req, err := http.NewRequest(http.MethodPost, "https://opencode.ai/inference/openai/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	// No session id in ctx: the header must be absent rather than empty.
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()
	if got := captured.request.Header.Get("Authorization"); got != "Bearer "+PublicBearer {
		t.Fatalf("Authorization = %q, want the static bearer", got)
	}
	if got := captured.request.Header.Get("x-opencode-session"); got != "" {
		t.Fatalf("x-opencode-session = %q, want it omitted when ctx carries no session", got)
	}
	if got := captured.request.Header.Get("User-Agent"); !strings.HasPrefix(got, "forebrain-harness/") {
		t.Fatalf("User-Agent = %q", got)
	}
}

func TestTransportWithoutCredentialsExplainsTheLogin(t *testing.T) {
	transport := &Transport{Path: filepath.Join(t.TempDir(), "missing.json"), Base: &captureTransport{}}
	req, err := http.NewRequest(http.MethodPost, "https://opencode.ai/inference/openai/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(req); err == nil {
		t.Fatal("RoundTrip succeeded without a credentials file")
	} else if !strings.Contains(err.Error(), "/connect") {
		t.Fatalf("error = %v, want it to point at /connect", err)
	}
}

func TestUserAgentCarriesTheInstalledVersion(t *testing.T) {
	SetClientVersion("v1.2.3")
	t.Cleanup(func() { SetClientVersion("") })
	if got := UserAgent(); got != "forebrain-harness/v1.2.3" {
		t.Fatalf("UserAgent = %q", got)
	}
	SetClientVersion("")
	if got := UserAgent(); got != "forebrain-harness/dev" {
		t.Fatalf("UserAgent without a version = %q, want the dev fallback", got)
	}
}

// The responses client blanks the User-Agent by default because the
// ChatGPT/Codex backend expects none. OpenCode's Go subscription works the
// other way round — it admits a third-party client by the agent that client
// announces — so the transport has to say that it owns the header, or every
// request would reach the gateway anonymous.
func TestTransportOwnsTheUserAgent(t *testing.T) {
	var owner interface{ OwnsUserAgent() bool } = NewKeyTransport(PublicBearer)
	if !owner.OwnsUserAgent() {
		t.Fatal("Transport reported it does not own the User-Agent; the responses client would blank it")
	}
	oauth := &Transport{Path: filepath.Join(t.TempDir(), CredentialsFile)}
	if !oauth.OwnsUserAgent() {
		t.Fatal("the login-file transport reported it does not own the User-Agent")
	}
}
