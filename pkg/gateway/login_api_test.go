package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/subscription"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/stretchr/testify/require"
)

// gatewayDo issues one request through the real route table, so path parameters
// and the registration itself are exercised. A body is optional; a test that
// names one posts it as JSON.
type gatewayDo func(method, target string, body ...string) *httptest.ResponseRecorder

func loginAPIServer(t *testing.T) (*Server, gatewayDo) {
	t.Helper()
	s := &Server{Home: t.TempDir()}
	srv := NewRestServer("")
	s.AttachExtraRoutes(srv)
	return s, func(method, target string, body ...string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		var reader io.Reader
		if len(body) > 0 {
			reader = strings.NewReader(body[0])
		}
		req := httptest.NewRequest(method, target, reader)
		if reader != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		srv.Server.Handler.ServeHTTP(rec, req)
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

// The two subscription ids the login endpoints are driven with: pkg/subscription's
// own ids, so the base URL a completed login answers with is the registry's.
const (
	authLoginChatGPT  = "chatgpt"
	authLoginOpenCode = "opencode-go"
)

// loginSubscription is one stand-in sign-in. The real flows are covered by
// pkg/subscription's own tests; what these tests drive is the gateway's half —
// start, poll, cancel, hand a callback over — so the sign-in itself never reaches
// a vendor here.
type loginSubscription struct {
	id    string
	label string
	flows []string
	// state is the probe's answer: what this machine's login can do.
	state turn.SignInCheck
	// url and code are what the page reads while the sign-in runs.
	url  string
	code string
	// answer decides a pasted callback: nil accepts it, an error refuses it.
	answer func(pasted string) error
	// offers is what a finished sign-in unlocked, and provisionErr the answer a
	// provision attempt gets instead of writing them. refreshErr is what
	// RefreshKey answers; nil reports a key fetched again.
	offers       []subscription.Offer
	provisionErr error
	refreshErr   error
	// handle is the sign-in this subscription hands the gateway. A test sets url,
	// code and answer before starting it.
	handle *loginHandle

	mu sync.Mutex
	// starts counts the flows begun, refreshed the rows RefreshKey was asked
	// about.
	starts    int
	refreshed []string
}

// newLoginSubscription builds one stand-in at its defaults: a sign-in with no
// login saved (Missing), whose flow the test ends itself.
func newLoginSubscription(id string, flows ...string) *loginSubscription {
	return &loginSubscription{
		id:    id,
		flows: flows,
		state: turn.SignInCheck{State: turn.SignInMissing},
		// Both real subscriptions are sign-in-backed, so their rows carry no key
		// to fetch again until a plan with a key pool (plan 281) says otherwise.
		refreshErr: subscription.ErrNoKeyToRefresh,
		handle:     &loginHandle{release: make(chan struct{}), stopped: make(chan struct{})},
	}
}

func (sub *loginSubscription) ID() string { return sub.id }

func (sub *loginSubscription) Label() (en, zh string) { return sub.label, sub.label }

func (sub *loginSubscription) Flows() []string { return sub.flows }

func (sub *loginSubscription) Probe(context.Context, subscription.Env) turn.SignInCheck {
	return sub.state
}

func (sub *loginSubscription) Start(context.Context, subscription.Env, string) (subscription.Login, error) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	sub.starts++
	sub.handle.url, sub.handle.code, sub.handle.answer = sub.url, sub.code, sub.answer
	return sub.handle, nil
}

// startCount is how many flows this stand-in began.
func (sub *loginSubscription) startCount() int {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	return sub.starts
}

func (sub *loginSubscription) Offers(context.Context, subscription.Env) ([]subscription.Offer, error) {
	return sub.offers, nil
}

// Provision writes the chosen offers the way a real subscription does — one row
// per offer, under this sign-in's source — so the default-model rule and the row
// shape are exercised rather than mocked.
func (sub *loginSubscription) Provision(ctx context.Context, env subscription.Env,
	offers []subscription.Offer, _ subscription.ProvisionOptions) ([]string, error) {
	if sub.provisionErr != nil {
		return nil, sub.provisionErr
	}
	ids := make([]string, 0, len(offers))
	for _, offer := range offers {
		empty := ""
		id, err := process.UpsertLLMModel(ctx, env.DB, process.LLMModelInput{
			Provider:   offer.Provider,
			Model:      offer.Model,
			BaseURL:    offer.BaseURL,
			APIPath:    offer.APIPath,
			APIKey:     &empty,
			Source:     sub.id,
			SourcePlan: offer.Plan,
		})
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (sub *loginSubscription) RefreshKey(_ context.Context, _ subscription.Env, modelID string) error {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	sub.refreshed = append(sub.refreshed, modelID)
	return sub.refreshErr
}

// loginHandle is one running stand-in sign-in: what the page polls, the address
// it pastes back, and the two ways a test ends it.
type loginHandle struct {
	url    string
	code   string
	answer func(pasted string) error

	release chan struct{}
	stopped chan struct{}
	once    sync.Once

	mu     sync.Mutex
	pasted string
}

// releaseOnce ends the sign-in as completed. Safe from the test and from Submit.
func (h *loginHandle) releaseOnce() { h.once.Do(func() { close(h.release) }) }

func (h *loginHandle) AuthorizeURL() string { return h.url }

func (h *loginHandle) UserCode() string { return h.code }

// Submit hands over the address a user pasted: the test's verdict decides
// whether it was this sign-in's, and an accepted one ends the sign-in the way a
// real callback would.
func (h *loginHandle) Submit(_ context.Context, pasted string) error {
	h.mu.Lock()
	h.pasted = pasted
	answer := h.answer
	h.mu.Unlock()
	if answer != nil {
		if err := answer(pasted); err != nil {
			return err
		}
	}
	h.releaseOnce()
	return nil
}

// Wait ends when the test releases the sign-in, or with the gateway's own
// context — its timeout, or a page that canceled.
func (h *loginHandle) Wait(ctx context.Context) error {
	select {
	case <-h.release:
		return nil
	case <-ctx.Done():
		close(h.stopped)
		return ctx.Err()
	}
}

func (h *loginHandle) Cancel() { h.releaseOnce() }

// loginStubs installs one test's stand-in sign-ins into the registry the login
// endpoints read: the ChatGPT-style flow (a pasted callback) and the
// OpenCode-style one (a device code). The real registry goes back when the test
// ends, and both stand-ins end so no flow outlives it.
func loginStubs(t *testing.T) (chatGPT, openCode *loginSubscription) {
	t.Helper()
	chatGPT = newLoginSubscription(authLoginChatGPT, subscription.FlowBrowserCallback, subscription.FlowPasteCallback)
	chatGPT.label = "ChatGPT"
	chatGPT.url = "https://auth.openai.com/oauth/authorize?state=st-1"
	openCode = newLoginSubscription(authLoginOpenCode, subscription.FlowDeviceCode)
	openCode.label = "OpenCode"
	openCode.url = "https://opencode.ai/console/device?user_code=ABCD-EFGH"
	openCode.code = "ABCD-EFGH"
	previous := gatewaySubscriptionByID
	byID := map[string]subscription.Subscription{authLoginChatGPT: chatGPT, authLoginOpenCode: openCode}
	gatewaySubscriptionByID = func(id string) (subscription.Subscription, bool) {
		sub, ok := byID[strings.ToLower(strings.TrimSpace(id))]
		return sub, ok
	}
	t.Cleanup(func() {
		chatGPT.handle.releaseOnce()
		openCode.handle.releaseOnce()
		gatewaySubscriptionByID = previous
	})
	return chatGPT, openCode
}

// The whole flow a page drives: start, read the device code while the user is
// still in the browser, then read the completion with the base URL the page
// writes into the provider row.
func TestAuthLoginOpenCodeRunsFromStartToComplete(t *testing.T) {
	_, stub := loginStubs(t)
	_, do := loginAPIServer(t)

	id := startLogin(t, do, authLoginOpenCode)

	pending := awaitPending(t, do, authLoginOpenCode, id)
	require.Equal(t, authLoginPending, pending.Status)
	require.Equal(t, "ABCD-EFGH", pending.UserCode)
	require.Equal(t, stub.url, pending.URL)
	require.Empty(t, pending.Error)

	stub.handle.releaseOnce()

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
	require.Equal(t, subscription.FlowDeviceCode, done.Flow, "the page has to know it shows a device code")
}

// The ChatGPT flow reports its authorization URL through the browser callback,
// and completes with the Codex base URL.
func TestAuthLoginChatGPTReportsTheAuthorizationURLAndCompletes(t *testing.T) {
	const authURL = "https://auth.openai.com/oauth/authorize?client_id=test&state=abc"
	chatGPT, _ := loginStubs(t)
	chatGPT.url = authURL
	_, do := loginAPIServer(t)

	id := startLogin(t, do, authLoginChatGPT)

	pending := awaitPending(t, do, authLoginChatGPT, id)
	require.Equal(t, authURL, pending.URL)
	require.Empty(t, pending.UserCode, "the loopback flow has no user code")

	chatGPT.handle.releaseOnce()

	done := awaitTerminal(t, do, authLoginChatGPT, id)
	require.Equal(t, authLoginComplete, done.Status)
	require.Equal(t, openai.CodexBaseURL, done.BaseURL)
	require.Equal(t, authURL, done.URL)
	require.Equal(t, subscription.FlowPasteCallback, done.Flow,
		"the gateway drives the paste flow, never the browser one")
}

// Two logins for one provider would fight over the same device code and the
// same loopback ports, so the second is refused while the first is running.
func TestAuthLoginRefusesASecondLoginForTheSameProvider(t *testing.T) {
	loginStubs(t)
	_, do := loginAPIServer(t)

	startLogin(t, do, authLoginOpenCode)
	rec := do(http.MethodPost, "/api/auth/login/"+authLoginOpenCode)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "another login is in progress")
}

// Closing the dialog cancels the login: the session reads canceled straight
// away, and the flow's goroutine is told to stop.
func TestAuthLoginCancelEndsTheLogin(t *testing.T) {
	_, stub := loginStubs(t)
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
		case <-stub.handle.stopped:
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
	loginStubs(t)
	_, do := loginAPIServer(t)

	id := startLogin(t, do, authLoginOpenCode)

	done := awaitTerminal(t, do, authLoginOpenCode, id)
	require.Equal(t, authLoginFailed, done.Status)
	require.Equal(t, "expired", done.Error)
}

// A session the gateway no longer has — a restart, a pruned id — is not
// "still pending": the page has to start over.
func TestAuthLoginUnknownIDsAreNotFound(t *testing.T) {
	loginStubs(t)
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
	nilServer.authLoginFinish("anything", nil, "", time.Now())

	// A bare server starts with no map, records its first login into one, and
	// reads it back.
	bare := &Server{}
	require.Nil(t, bare.authLogins)
	id, err := bare.authLoginStart(authLoginOpenCode, time.Now().Add(time.Hour), func() {}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, id)
	got, ok := bare.authLoginGet(authLoginOpenCode, id)
	require.True(t, ok)
	require.Equal(t, authLoginPending, got.status)

	// A finished login is readable until its retention runs out and is pruned
	// after that, so the map does not grow for the life of the server.
	bare.authLoginFinish(id, nil, authLoginBaseURL(authLoginOpenCode), time.Now().Add(-time.Second))
	require.False(t, func() bool {
		_, ok := bare.authLoginGet(authLoginOpenCode, id)
		return ok
	}(), "an expired login is pruned on the next read")
}

// A login this machine already holds answers the start request the way a
// finished flow's poll would — complete with the base URL — and neither flow
// starts: no login id exists to poll.
func TestAuthLoginStartCompletesOnAUsableSavedLogin(t *testing.T) {
	for _, provider := range []string{authLoginChatGPT, authLoginOpenCode} {
		t.Run(provider, func(t *testing.T) {
			chatGPT, openCode := loginStubs(t)
			chatGPT.state = turn.SignInCheck{State: turn.SignInUsable}
			openCode.state = turn.SignInCheck{State: turn.SignInUsable}
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
			require.Zero(t, chatGPT.startCount()+openCode.startCount(),
				"a usable saved login must not start a flow")
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
			chatGPT, openCode := loginStubs(t)
			unreachable := turn.SignInCheck{State: turn.SignInUnreachable, Err: errors.New("registry unreachable")}
			chatGPT.state, openCode.state = unreachable, unreachable
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
			require.Zero(t, chatGPT.startCount()+openCode.startCount(),
				"an unreachable listing must not start a flow")
		})
	}
}

// Missing keeps the flow exactly as it was: 202 with a login id to poll, and
// the flow running in the background.
func TestAuthLoginStartRunsTheFlowWhenNoLoginIsSaved(t *testing.T) {
	_, stub := loginStubs(t)
	_, do := loginAPIServer(t)

	id := startLogin(t, do, authLoginOpenCode)

	awaitPending(t, do, authLoginOpenCode, id)
	stub.handle.releaseOnce()
	done := awaitTerminal(t, do, authLoginOpenCode, id)
	require.Equal(t, authLoginComplete, done.Status)
	require.Equal(t, "https://opencode.ai/inference/go", done.BaseURL)
}

// Dead keeps the flow too: only a new sign-in restores a login the server
// refused.
func TestAuthLoginStartRunsTheFlowWhenTheLoginIsDead(t *testing.T) {
	chatGPT, openCode := loginStubs(t)
	dead := turn.SignInCheck{State: turn.SignInDead, Err: errors.New("OpenCode login expired; run /connect again")}
	chatGPT.state, openCode.state = dead, dead
	s, do := loginAPIServer(t)

	rec := do(http.MethodPost, "/api/auth/login/"+authLoginOpenCode)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.NotEmpty(t, s.authLogins, "the flow must be registered and pollable")
}

func pastedCallback(do gatewayDo, provider, id, pasted string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"url": pasted})
	return do(http.MethodPost, "/api/auth/login/"+provider+"/"+id+"/callback", string(body))
}

// The page hands the address the browser could not open back to the gateway,
// which takes it as this sign-in's answer: the login finishes exactly as a
// loopback callback would have finished it.
func TestAuthLoginChatGPTAcceptsAPastedCallback(t *testing.T) {
	loginStubs(t)
	_, do := loginAPIServer(t)

	id := startLogin(t, do, authLoginChatGPT)
	awaitPending(t, do, authLoginChatGPT, id)

	rec := pastedCallback(do, authLoginChatGPT, id, "http://localhost:1455/auth/callback?code=c&state=st-1")
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	done := awaitTerminal(t, do, authLoginChatGPT, id)
	require.Equal(t, authLoginComplete, done.Status)
}

// A refused paste is reported to the person who pasted it, and the login goes
// on: they can copy the right address and try again.
func TestAuthLoginPastedCallbackRejectionIsReportedAndKeepsTheLoginPending(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply error
		want  string
	}{
		{"state mismatch", openai.ErrCallbackStateMismatch, "state_mismatch"},
		{"not a callback", openai.ErrCallbackNotACallback, "not_a_callback"},
		{"no code", openai.ErrCallbackNoCode, "no_code"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chatGPT, _ := loginStubs(t)
			chatGPT.answer = func(string) error { return tc.reply }
			_, do := loginAPIServer(t)

			id := startLogin(t, do, authLoginChatGPT)
			awaitPending(t, do, authLoginChatGPT, id)

			rec := pastedCallback(do, authLoginChatGPT, id, "http://localhost:1455/auth/callback?code=c&state=WRONG")
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Equal(t, tc.want, strings.TrimSpace(rec.Body.String()))

			code, status := loginStatus(t, do, authLoginChatGPT, id)
			require.Equal(t, http.StatusOK, code)
			require.Equal(t, authLoginPending, status.Status, "a refused paste must not end the login")
		})
	}
}

// The endpoint only ever feeds a pending ChatGPT login: there is nothing to
// hand a callback to otherwise, and saying which of those it is keeps the page
// from guessing.
func TestAuthLoginPastedCallbackNeedsAPendingChatGPTLogin(t *testing.T) {
	t.Run("another provider's login", func(t *testing.T) {
		loginStubs(t)
		_, do := loginAPIServer(t)

		openCodeID := startLogin(t, do, authLoginOpenCode)
		rec := pastedCallback(do, authLoginOpenCode, openCodeID, "http://localhost:1455/auth/callback?code=c&state=s")
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	})

	t.Run("a login that already ended", func(t *testing.T) {
		loginStubs(t)
		_, do := loginAPIServer(t)

		id := startLogin(t, do, authLoginChatGPT)
		awaitPending(t, do, authLoginChatGPT, id)
		require.Equal(t, http.StatusAccepted, pastedCallback(do, authLoginChatGPT, id,
			"http://localhost:1455/auth/callback?code=c&state=st-1").Code)
		done := awaitTerminal(t, do, authLoginChatGPT, id)
		require.Equal(t, authLoginComplete, done.Status)

		rec := pastedCallback(do, authLoginChatGPT, id, "http://localhost:1455/auth/callback?code=c&state=st-1")
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	})

	t.Run("an unknown id", func(t *testing.T) {
		loginStubs(t)
		_, do := loginAPIServer(t)
		rec := pastedCallback(do, authLoginChatGPT, "nosuchlogin", "http://localhost:1455/auth/callback?code=c&state=s")
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	})

	t.Run("not a POST", func(t *testing.T) {
		loginStubs(t)
		_, do := loginAPIServer(t)
		id := startLogin(t, do, authLoginChatGPT)
		awaitPending(t, do, authLoginChatGPT, id)
		rec := do(http.MethodGet, "/api/auth/login/"+authLoginChatGPT+"/"+id+"/callback")
		require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	})

	t.Run("a body with no url", func(t *testing.T) {
		loginStubs(t)
		_, do := loginAPIServer(t)
		id := startLogin(t, do, authLoginChatGPT)
		awaitPending(t, do, authLoginChatGPT, id)

		for _, body := range []string{`not json`, `{}`, `{"url":"   "}`} {
			rec := do(http.MethodPost, "/api/auth/login/"+authLoginChatGPT+"/"+id+"/callback", body)
			require.Equal(t, http.StatusBadRequest, rec.Code, body)
			require.Equal(t, "bad_request", strings.TrimSpace(rec.Body.String()))
		}
	})

	t.Run("a body past the limit", func(t *testing.T) {
		loginStubs(t)
		_, do := loginAPIServer(t)
		id := startLogin(t, do, authLoginChatGPT)
		awaitPending(t, do, authLoginChatGPT, id)

		big, err := json.Marshal(map[string]string{"url": strings.Repeat("x", 9<<10)})
		require.NoError(t, err)
		rec := do(http.MethodPost, "/api/auth/login/"+authLoginChatGPT+"/"+id+"/callback", string(big))
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "bad_request", strings.TrimSpace(rec.Body.String()))
	})
}

// The pasted address carries a one-time authorization code. It is a login
// input, so it must not come back out in a response the page or its log keeps,
// and it must not appear in what a poll reports either.
func TestAuthLoginPastedCallbackNeverLeaksTheAddress(t *testing.T) {
	chatGPT, _ := loginStubs(t)
	chatGPT.answer = func(string) error { return openai.ErrCallbackStateMismatch }
	_, do := loginAPIServer(t)

	id := startLogin(t, do, authLoginChatGPT)
	awaitPending(t, do, authLoginChatGPT, id)

	rec := pastedCallback(do, authLoginChatGPT, id, "http://localhost:1455/auth/callback?code=SECRET-MARKER&state=WRONG")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.NotContains(t, rec.Body.String(), "SECRET-MARKER")

	statusRec := do(http.MethodGet, "/api/auth/login/"+authLoginChatGPT+"/"+id)
	require.Equal(t, http.StatusOK, statusRec.Code)
	require.NotContains(t, statusRec.Body.String(), "SECRET-MARKER")

	chatGPT.handle.releaseOnce()
	final := awaitTerminal(t, do, authLoginChatGPT, id)
	require.NotContains(t, final.URL, "SECRET-MARKER")
	require.NotContains(t, final.Error, "SECRET-MARKER")
}
