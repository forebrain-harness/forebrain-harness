package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/stretchr/testify/require"
)

// gatewayDo issues one request through the real route table, so path parameters
// and the registration itself are exercised.
type gatewayDo func(method, target string) *httptest.ResponseRecorder

func loginAPIServer(t *testing.T) (*Server, gatewayDo) {
	t.Helper()
	s := &Server{Home: t.TempDir()}
	srv := NewRestServer("")
	s.AttachExtraRoutes(srv)
	return s, func(method, target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.Server.Handler.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
		return rec
	}
}

func startLogin(t *testing.T, do gatewayDo, provider string) string {
	t.Helper()
	rec := do(http.MethodPost, "/api/auth/login/"+provider)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var body struct {
		LoginID  string `json:"login_id"`
		Provider string `json:"provider"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotEmpty(t, body.LoginID)
	require.Equal(t, provider, body.Provider)
	return body.LoginID
}

func loginStatus(t *testing.T, do gatewayDo, provider, id string) (int, authLoginStatusBody) {
	t.Helper()
	rec := do(http.MethodGet, "/api/auth/login/"+provider+"/"+id)
	var body authLoginStatusBody
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	}
	return rec.Code, body
}

// awaitPending waits for the first poll that already carries what the flow
// knows: a session starts pending with an empty URL, and the URL arrives a beat
// later.
func awaitPending(t *testing.T, do gatewayDo, provider, id string) authLoginStatusBody {
	t.Helper()
	var body authLoginStatusBody
	require.Eventually(t, func() bool {
		code, got := loginStatus(t, do, provider, id)
		if code != http.StatusOK || got.Status != authLoginPending || got.URL == "" {
			return false
		}
		body = got
		return true
	}, 5*time.Second, 5*time.Millisecond, "the login never reached a pending session with a URL")
	return body
}

// awaitTerminal waits for the login to end and returns its final state.
func awaitTerminal(t *testing.T, do gatewayDo, provider, id string) authLoginStatusBody {
	t.Helper()
	var body authLoginStatusBody
	require.Eventually(t, func() bool {
		code, got := loginStatus(t, do, provider, id)
		if code != http.StatusOK || got.Status == authLoginPending {
			return false
		}
		body = got
		return true
	}, 5*time.Second, 5*time.Millisecond, "the login never reached a terminal state")
	return body
}

// openCodeLoginStub stands in for the device flow. It reports the device code
// the way the real flow does, then waits for the test to release it or for the
// flow's context to end, so a test can observe the session while it is pending.
type openCodeLoginStub struct {
	release chan struct{}
	stopped chan struct{}

	mu       sync.Mutex
	path     string
	browser  func(string) error
	noopOK   bool
	reported opencode.LoginInfo
}

func newOpenCodeLoginStub(t *testing.T) *openCodeLoginStub {
	t.Helper()
	stub := &openCodeLoginStub{release: make(chan struct{}), stopped: make(chan struct{})}
	info := opencode.LoginInfo{
		VerificationURL: "https://opencode.ai/console/device?user_code=ABCD-EFGH",
		UserCode:        "ABCD-EFGH",
	}
	previous := openCodeLoginRun
	openCodeLoginRun = func(ctx context.Context, path string, out io.Writer,
		openBrowser func(url string) error, onStart func(opencode.LoginInfo), onStage func(opencode.LoginStage)) (opencode.Credentials, error) {
		stub.mu.Lock()
		stub.path = path
		stub.browser = openBrowser
		stub.reported = info
		// The flow must not be handed nil: nil means "open the system browser",
		// which a gateway on a headless host must never do. A no-op that answers
		// nil proves the endpoint passed one.
		stub.noopOK = openBrowser != nil && openBrowser(info.VerificationURL) == nil
		stub.mu.Unlock()
		if onStart != nil {
			onStart(info)
		}
		select {
		case <-stub.release:
			return opencode.Credentials{AccessToken: "pc-access", RefreshToken: "pc-refresh"}, nil
		case <-ctx.Done():
			close(stub.stopped)
			return opencode.Credentials{}, ctx.Err()
		}
	}
	t.Cleanup(func() { openCodeLoginRun = previous })
	return stub
}

// chatGPTLoginStub stands in for the loopback PKCE flow, whose only outward
// step a web login cares about is handing over the authorization URL.
type chatGPTLoginStub struct {
	release chan struct{}
	stopped chan struct{}

	mu      sync.Mutex
	path    string
	auth    string
	browser func(string) error
}

func newChatGPTLoginStub(t *testing.T, authURL string) *chatGPTLoginStub {
	t.Helper()
	stub := &chatGPTLoginStub{release: make(chan struct{}), stopped: make(chan struct{}), auth: authURL}
	previous := chatGPTLoginRun
	chatGPTLoginRun = func(ctx context.Context, path string, out io.Writer, openBrowser func(url string) error) (openai.Credentials, error) {
		stub.mu.Lock()
		stub.path = path
		stub.browser = openBrowser
		open := openBrowser
		stub.mu.Unlock()
		if open != nil {
			_ = open(authURL)
		}
		select {
		case <-stub.release:
			return openai.Credentials{AccessToken: "web-access", RefreshToken: "web-refresh", ExpiresAt: time.Now().Add(time.Hour)}, nil
		case <-ctx.Done():
			close(stub.stopped)
			return openai.Credentials{}, ctx.Err()
		}
	}
	t.Cleanup(func() { chatGPTLoginRun = previous })
	return stub
}

// The whole flow a page drives: start, read the device code while the user is
// still in the browser, then read the completion with the base URL the page
// writes into the provider row.
func TestAuthLoginOpenCodeRunsFromStartToComplete(t *testing.T) {
	stub := newOpenCodeLoginStub(t)
	s, do := loginAPIServer(t)

	id := startLogin(t, do, authLoginOpenCode)

	pending := awaitPending(t, do, authLoginOpenCode, id)
	require.Equal(t, authLoginPending, pending.Status)
	require.Equal(t, "ABCD-EFGH", pending.UserCode)
	require.Equal(t, stub.reported.VerificationURL, pending.URL)
	require.Empty(t, pending.Error)

	close(stub.release)

	done := awaitTerminal(t, do, authLoginOpenCode, id)
	require.Equal(t, authLoginComplete, done.Status)
	// The Go subscription's root, spelled out rather than compared against
	// ProviderBaseURL: the flow is named after the Console, but the row it
	// unlocks is "opencode-go", and its models are served under /go and nowhere
	// else. An answer of the Console root would pass a comparison against
	// ProviderBaseURL("opencode") and let the page write a row that names a
	// surface its models do not answer on.
	require.Equal(t, "https://opencode.ai/inference/go", done.BaseURL)
	require.Empty(t, done.Error)
	require.Equal(t, "ABCD-EFGH", done.UserCode, "the code stays readable so a late page still shows it")

	stub.mu.Lock()
	defer stub.mu.Unlock()
	require.Equal(t, filepath.Join(s.Home, opencode.CredentialsFile), stub.path,
		"the web login writes the same login file /connect does")
	require.True(t, stub.noopOK, "the endpoint must pass an explicit no-op browser opener, never nil")
}

// The ChatGPT flow reports its authorization URL through the browser callback,
// and completes with the Codex base URL.
func TestAuthLoginChatGPTReportsTheAuthorizationURLAndCompletes(t *testing.T) {
	const authURL = "https://auth.openai.com/oauth/authorize?client_id=test&state=abc"
	stub := newChatGPTLoginStub(t, authURL)
	s, do := loginAPIServer(t)

	id := startLogin(t, do, authLoginChatGPT)

	pending := awaitPending(t, do, authLoginChatGPT, id)
	require.Equal(t, authURL, pending.URL)
	require.Empty(t, pending.UserCode, "the loopback flow has no user code")

	close(stub.release)

	done := awaitTerminal(t, do, authLoginChatGPT, id)
	require.Equal(t, authLoginComplete, done.Status)
	require.Equal(t, openai.CodexBaseURL, done.BaseURL)
	require.Equal(t, authURL, done.URL)

	stub.mu.Lock()
	defer stub.mu.Unlock()
	require.Equal(t, filepath.Join(s.Home, openai.CredentialsFile), stub.path)
	require.NotNil(t, stub.browser, "the URL has to reach the page somehow")
}

// Two logins for one provider would fight over the same device code and the
// same loopback ports, so the second is refused while the first is running.
func TestAuthLoginRefusesASecondLoginForTheSameProvider(t *testing.T) {
	newOpenCodeLoginStub(t)
	_, do := loginAPIServer(t)

	startLogin(t, do, authLoginOpenCode)
	rec := do(http.MethodPost, "/api/auth/login/"+authLoginOpenCode)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "another login is in progress")
}

// Closing the dialog cancels the login: the session reads canceled straight
// away, and the flow's goroutine is told to stop.
func TestAuthLoginCancelEndsTheLogin(t *testing.T) {
	stub := newOpenCodeLoginStub(t)
	_, do := loginAPIServer(t)

	id := startLogin(t, do, authLoginOpenCode)
	awaitPending(t, do, authLoginOpenCode, id)

	rec := do(http.MethodPost, "/api/auth/login/"+authLoginOpenCode+"/"+id+"/cancel")
	require.Equal(t, http.StatusNoContent, rec.Code)

	code, body := loginStatus(t, do, authLoginOpenCode, id)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, authLoginFailed, body.Status)
	require.Equal(t, authLoginCanceled, body.Error)
	require.Empty(t, body.BaseURL)

	require.Eventually(t, func() bool {
		select {
		case <-stub.stopped:
			return true
		default:
			return false
		}
	}, 5*time.Second, 5*time.Millisecond, "the flow goroutine was never stopped")
}

// A login nobody finishes ends as failed("expired") rather than pending
// forever. The timeout is shortened here; the endpoint reads the same variable.
func TestAuthLoginTimesOutAsExpired(t *testing.T) {
	previous := authLoginTimeout
	authLoginTimeout = 80 * time.Millisecond
	t.Cleanup(func() { authLoginTimeout = previous })
	newOpenCodeLoginStub(t)
	_, do := loginAPIServer(t)

	id := startLogin(t, do, authLoginOpenCode)

	done := awaitTerminal(t, do, authLoginOpenCode, id)
	require.Equal(t, authLoginFailed, done.Status)
	require.Equal(t, "expired", done.Error)
}

// A session the gateway no longer has — a restart, a pruned id — is not
// "still pending": the page has to start over.
func TestAuthLoginUnknownIDsAreNotFound(t *testing.T) {
	newOpenCodeLoginStub(t)
	_, do := loginAPIServer(t)

	require.Equal(t, http.StatusNotFound, do(http.MethodGet, "/api/auth/login/opencode/does-not-exist").Code)
	require.Equal(t, http.StatusNotFound, do(http.MethodPost, "/api/auth/login/opencode/does-not-exist/cancel").Code)
	// A provider this endpoint does not serve is unknown for the same reason.
	require.Equal(t, http.StatusNotFound, do(http.MethodPost, "/api/auth/login/gemini").Code)
	require.Equal(t, http.StatusNotFound, do(http.MethodGet, "/api/auth/login/gemini/whatever").Code)

	// And a login id only answers under the provider that started it.
	id := startLogin(t, do, authLoginOpenCode)
	require.Equal(t, http.StatusNotFound, do(http.MethodGet, "/api/auth/login/chatgpt/"+id).Code)
}

// The login state lives on the server, like the MCP PKCE state beside it, and
// every accessor tolerates a nil or bare server the way mcpPKCEPut does: a
// Server assembled field by field has no map yet, and neither a test double nor
// a fault path may panic on it.
func TestAuthLoginStateLivesOnTheServerAndToleratesAnEmptyOne(t *testing.T) {
	var nilServer *Server
	_, ok := nilServer.authLoginGet(authLoginOpenCode, "anything")
	require.False(t, ok)
	_, ok = nilServer.authLoginAbort(authLoginOpenCode, "anything", time.Now())
	require.False(t, ok)
	nilServer.authLoginUpdate("anything", func(*gatewayAuthLogin) {})
	nilServer.authLoginFinish("anything", nil, "", time.Now())

	// A bare server starts with no map, records its first login into one, and
	// reads it back.
	bare := &Server{}
	require.Nil(t, bare.authLogins)
	id, err := bare.authLoginStart(authLoginOpenCode, time.Now().Add(time.Hour), func() {})
	require.NoError(t, err)
	require.NotEmpty(t, id)
	got, ok := bare.authLoginGet(authLoginOpenCode, id)
	require.True(t, ok)
	require.Equal(t, authLoginPending, got.status)

	// A finished login is readable until its retention runs out and is pruned
	// after that, so the map does not grow for the life of the server.
	bare.authLoginFinish(id, nil, opencode.ProviderBaseURL("opencode-go"), time.Now().Add(-time.Second))
	require.False(t, func() bool {
		_, ok := bare.authLoginGet(authLoginOpenCode, id)
		return ok
	}(), "an expired login is pruned on the next read")
}

// stubAuthLoginProbe pins the probe's answer, so a start request can be driven
// through one branch without a login file or a network.
func stubAuthLoginProbe(t *testing.T, check turn.SignInCheck) {
	t.Helper()
	previous := authLoginProbe
	authLoginProbe = func(context.Context, string, string) turn.SignInCheck {
		return check
	}
	t.Cleanup(func() { authLoginProbe = previous })
}

// countingBothLogins replaces both flows with counters, so a probe test can
// prove neither one started.
func countingBothLogins(t *testing.T) *int {
	t.Helper()
	calls := 0
	previousOC := openCodeLoginRun
	openCodeLoginRun = func(context.Context, string, io.Writer, func(string) error, func(opencode.LoginInfo), func(opencode.LoginStage)) (opencode.Credentials, error) {
		calls++
		return opencode.Credentials{}, nil
	}
	previousCG := chatGPTLoginRun
	chatGPTLoginRun = func(context.Context, string, io.Writer, func(string) error) (openai.Credentials, error) {
		calls++
		return openai.Credentials{}, nil
	}
	t.Cleanup(func() {
		openCodeLoginRun = previousOC
		chatGPTLoginRun = previousCG
	})
	return &calls
}

// A login this machine already holds answers the start request the way a
// finished flow's poll would — complete with the base URL — and neither flow
// starts: no login id exists to poll.
func TestAuthLoginStartCompletesOnAUsableSavedLogin(t *testing.T) {
	for _, provider := range []string{authLoginChatGPT, authLoginOpenCode} {
		t.Run(provider, func(t *testing.T) {
			loginCalls := countingBothLogins(t)
			stubAuthLoginProbe(t, turn.SignInCheck{State: turn.SignInUsable})
			s, do := loginAPIServer(t)
			rec := do(http.MethodPost, "/api/auth/login/"+provider)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var body struct {
				Provider string `json:"provider"`
				Status   string `json:"status"`
				BaseURL  string `json:"base_url"`
				LoginID  string `json:"login_id"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, provider, body.Provider)
			require.Equal(t, authLoginComplete, body.Status)
			require.Equal(t, authLoginBaseURL(provider), body.BaseURL)
			require.Empty(t, body.LoginID)
			require.Zero(t, *loginCalls, "a usable saved login must not start a flow")
			require.Empty(t, s.authLogins, "no login session may be registered")
		})
	}
}

// Unreachable completes too: a listing that fails for a reason that is not the
// login must not push a working login back through the browser. The page's own
// model fetch is the fact source that reports the failure.
func TestAuthLoginStartCompletesOnAnUnreachableListing(t *testing.T) {
	for _, provider := range []string{authLoginChatGPT, authLoginOpenCode} {
		t.Run(provider, func(t *testing.T) {
			loginCalls := countingBothLogins(t)
			stubAuthLoginProbe(t, turn.SignInCheck{
				State: turn.SignInUnreachable,
				Err:   errors.New("registry unreachable"),
			})
			_, do := loginAPIServer(t)
			rec := do(http.MethodPost, "/api/auth/login/"+provider)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var body struct {
				Status  string `json:"status"`
				BaseURL string `json:"base_url"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, authLoginComplete, body.Status)
			require.Equal(t, authLoginBaseURL(provider), body.BaseURL)
			require.Zero(t, *loginCalls, "an unreachable listing must not start a flow")
		})
	}
}

// Missing keeps the flow exactly as it was: 202 with a login id to poll, and
// the flow running in the background.
func TestAuthLoginStartRunsTheFlowWhenNoLoginIsSaved(t *testing.T) {
	stub := newOpenCodeLoginStub(t)
	stubAuthLoginProbe(t, turn.SignInCheck{State: turn.SignInMissing})
	_, do := loginAPIServer(t)

	id := startLogin(t, do, authLoginOpenCode)

	awaitPending(t, do, authLoginOpenCode, id)
	close(stub.release)
	done := awaitTerminal(t, do, authLoginOpenCode, id)
	require.Equal(t, authLoginComplete, done.Status)
	require.Equal(t, opencode.ProviderBaseURL("opencode-go"), done.BaseURL)
}

// Dead keeps the flow too: only a new sign-in restores a login the server
// refused.
func TestAuthLoginStartRunsTheFlowWhenTheLoginIsDead(t *testing.T) {
	stubAuthLoginProbe(t, turn.SignInCheck{
		State: turn.SignInDead,
		Err:   errors.New("OpenCode login expired; run /connect again"),
	})
	newOpenCodeLoginStub(t)
	s, do := loginAPIServer(t)

	rec := do(http.MethodPost, "/api/auth/login/"+authLoginOpenCode)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.NotEmpty(t, s.authLogins, "the flow must be registered and pollable")
}
