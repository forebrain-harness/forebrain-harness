// Web login for the OAuth providers the model settings page can sign in to.
//
// The terminal's /connect runs these flows in-process: it prints a status line
// and opens a browser on the machine it is sitting in front of. A browser page
// can do neither, so the flow runs here instead and the page polls it. What the
// page needs — the authorization URL, a device flow's user code — is handed over
// as soon as the flow learns it, and the ending (complete, or failed with a
// reason) plus the base URL to write into the provider row comes back from the
// same endpoint.
//
// The credentials themselves are not touched here: the flows save them the same
// way the terminal does, so one login serves both surfaces and the last login
// wins.
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
)

// The two providers with a web login, spelled as the provider ids in
// configuration so the page can write the row without translating.
const (
	authLoginOpenCode = "opencode"
	authLoginChatGPT  = "chatgpt"
)

// Login states. A login only ever moves from pending to one of the two terminal
// states, never back and never sideways: a page that saw "complete" must not be
// able to read "failed" a moment later.
const (
	authLoginPending  = "pending"
	authLoginComplete = "complete"
	authLoginFailed   = "failed"

	// authLoginCanceled is the failure reason a page's own cancel produces.
	authLoginCanceled = "canceled"
)

// authLoginTimeout bounds one web login. A device code or an authorization code
// nobody completes is not worth keeping a goroutine and a loopback port for; it
// is a variable so a test can drive the ending without waiting ten minutes.
var authLoginTimeout = 10 * time.Minute

// authLoginRetention is how long a finished login stays readable after its flow
// ends, so the page's last poll gets the answer instead of a 404.
const authLoginRetention = 5 * time.Minute

// The two flows, as variables so a test can drive these endpoints without
// talking to opencode.ai or auth.openai.com.
var (
	openCodeLoginRun = opencode.LoginDeviceFlow
	chatGPTLoginRun  = openai.LoginWithBrowser
)

// errAuthLoginInProgress is the answer to a second login for the same provider.
// Both flows own fixed resources — a device code, and the loopback ports 1455
// and 1457 — so two at once would fight over them.
var errAuthLoginInProgress = errors.New("another login is in progress")

// gatewayAuthLogin is one web login as the page sees it. It is the login
// analogue of gatewayMcpPKCE: per-server state, guarded by the server's own
// mutex, and dropped once it can no longer be read.
type gatewayAuthLogin struct {
	provider string
	status   string
	// url is the authorization URL: a device verification page, or the ChatGPT
	// authorize link the loopback flow will accept a callback for.
	url string
	// userCode is the device flow's short code the user confirms in the browser.
	userCode string
	// errText is the failure reason phrase, empty unless status is failed.
	errText string
	// baseURL is the provider base URL to write into the provider row, set when
	// the login completes.
	baseURL string
	// keepUntil is when the login stops being readable. Zero while pending: the
	// flow's own timeout already bounds that.
	keepUntil time.Time
	cancel    context.CancelFunc
}

// authLoginStart records a new pending login for provider and returns its id.
// cancel stops the flow and is stored with the login before the caller can
// observe the id, so a cancel request can never arrive before there is
// something to cancel.
//
// One login per provider: both flows own fixed resources — a device code, and
// the loopback ports 1455 and 1457 — so two at once would fight over them.
func (s *Server) authLoginStart(provider string, keepUntil time.Time, cancel context.CancelFunc) (string, error) {
	if s == nil {
		return "", errors.New("no server")
	}
	id, err := newAuthLoginID()
	if err != nil {
		return "", err
	}
	s.authLoginMu.Lock()
	defer s.authLoginMu.Unlock()
	s.pruneAuthLoginsLocked(time.Now())
	for _, login := range s.authLogins {
		if login.provider == provider && login.status == authLoginPending {
			return "", errAuthLoginInProgress
		}
	}
	if s.authLogins == nil {
		s.authLogins = make(map[string]*gatewayAuthLogin)
	}
	s.authLogins[id] = &gatewayAuthLogin{
		provider:  provider,
		status:    authLoginPending,
		keepUntil: keepUntil,
		cancel:    cancel,
	}
	return id, nil
}

// authLoginUpdate records what the flow has learned so far. A login that
// already ended keeps its answer.
func (s *Server) authLoginUpdate(id string, apply func(*gatewayAuthLogin)) {
	if s == nil {
		return
	}
	s.authLoginMu.Lock()
	defer s.authLoginMu.Unlock()
	login, ok := s.authLogins[id]
	if !ok || login.status != authLoginPending {
		return
	}
	apply(login)
}

// authLoginFinish records the end of a flow. It never rewrites a login that
// already ended: a page's cancel and the goroutine's own error race here, and
// the answer the page already saw is the one that counts.
func (s *Server) authLoginFinish(id string, runErr error, baseURL string, keepUntil time.Time) {
	if s == nil {
		return
	}
	s.authLoginMu.Lock()
	defer s.authLoginMu.Unlock()
	login, ok := s.authLogins[id]
	if !ok || login.status != authLoginPending {
		return
	}
	login.keepUntil = keepUntil
	if runErr != nil {
		login.status = authLoginFailed
		login.errText = authLoginReason(runErr)
		return
	}
	login.status = authLoginComplete
	login.baseURL = baseURL
}

// authLoginAbort ends a pending login as canceled and returns the flow's
// cancel, so a handler can stop the goroutine after the state is already what
// the page will read. An already finished login is left alone, and its cancel
// is still returned (a completed flow has nothing left to stop).
func (s *Server) authLoginAbort(provider, id string, keepUntil time.Time) (context.CancelFunc, bool) {
	if s == nil {
		return nil, false
	}
	s.authLoginMu.Lock()
	defer s.authLoginMu.Unlock()
	login, ok := s.authLogins[id]
	if !ok || login.provider != provider {
		return nil, false
	}
	if login.status == authLoginPending {
		login.status = authLoginFailed
		login.errText = authLoginCanceled
		login.keepUntil = keepUntil
	}
	return login.cancel, true
}

// authLoginGet returns a copy of a login, or false when the id is unknown to
// this provider — a gateway restart clears the map, and a pruned id is as good
// as unknown.
func (s *Server) authLoginGet(provider, id string) (gatewayAuthLogin, bool) {
	if s == nil {
		return gatewayAuthLogin{}, false
	}
	s.authLoginMu.Lock()
	defer s.authLoginMu.Unlock()
	s.pruneAuthLoginsLocked(time.Now())
	login, ok := s.authLogins[id]
	if !ok || login.provider != provider {
		return gatewayAuthLogin{}, false
	}
	return *login, true
}

// pruneAuthLoginsLocked drops finished logins whose retention has run out, so
// the map does not grow for the life of the server. A pending login is never
// pruned: the flow's own timeout ends it.
func (s *Server) pruneAuthLoginsLocked(now time.Time) {
	for id, login := range s.authLogins {
		if !login.keepUntil.IsZero() && now.After(login.keepUntil) {
			delete(s.authLogins, id)
		}
	}
}

// handleAuthLoginStart begins a web login and answers 202 with the id to poll.
// The flow runs in the background — the response cannot wait for a human to
// finish in a browser — and the provider's canonical base URL is part of the
// completion answer, ready to write into the provider row.
func (s *Server) handleAuthLoginStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s == nil {
		http.Error(w, "nil", http.StatusInternalServerError)
		return
	}
	provider := authLoginProvider(pathParam(r, "provider"))
	if provider == "" {
		http.Error(w, "unknown login provider", http.StatusNotFound)
		return
	}
	now := time.Now()
	keepUntil := now.Add(authLoginTimeout + authLoginRetention)
	ctx, cancel := context.WithTimeout(context.Background(), authLoginTimeout)
	id, err := s.authLoginStart(provider, keepUntil, cancel)
	if err != nil {
		cancel()
		status := http.StatusConflict
		if !errors.Is(err, errAuthLoginInProgress) {
			status = http.StatusInternalServerError
		}
		http.Error(w, err.Error(), status)
		return
	}
	// The goroutine keeps its own reference to the server: it is the owner of
	// the login state, and the handler's request may end long before the human
	// finishes in a browser.
	owner := s
	go func() {
		defer cancel()
		runErr := owner.runAuthLogin(ctx, provider, id)
		owner.authLoginFinish(id, runErr, authLoginBaseURL(provider), keepUntil)
	}()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"login_id": id, "provider": provider})
}

// runAuthLogin runs one provider's flow to completion. The URL and the device
// user code reach the page through the login as soon as the flow knows them;
// out is discarded because the page is the progress display, and the browser is
// never opened here — the gateway may be running where nobody can see one, and
// opening it there would be a window onto a terminal nobody is watching.
func (s *Server) runAuthLogin(ctx context.Context, provider, id string) error {
	if provider == authLoginChatGPT {
		_, err := chatGPTLoginRun(ctx, openai.CredentialsPath(s.Home), io.Discard, func(url string) error {
			s.authLoginUpdate(id, func(login *gatewayAuthLogin) { login.url = url })
			return nil
		})
		return err
	}
	_, err := openCodeLoginRun(ctx, opencode.DefaultPath(s.Home), io.Discard,
		// Explicitly not nil: nil would mean "open the system browser", which is
		// the one thing this path must not do.
		func(string) error { return nil },
		func(info opencode.LoginInfo) {
			s.authLoginUpdate(id, func(login *gatewayAuthLogin) {
				login.url = info.VerificationURL
				login.userCode = info.UserCode
			})
		},
		// The web has its own progress UI, so it asks for no stage reports.
		nil,
	)
	return err
}

// authLoginStatusBody is the poll answer. Every field is always present so the
// page can read it without guessing what an absent one meant; an empty url or
// user_code during the first moment of a flow is expected.
type authLoginStatusBody struct {
	Status   string `json:"status"`
	URL      string `json:"url"`
	UserCode string `json:"user_code"`
	Error    string `json:"error"`
	BaseURL  string `json:"base_url"`
}

// handleAuthLoginStatus answers one poll.
func (s *Server) handleAuthLoginStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s == nil {
		http.Error(w, "nil", http.StatusInternalServerError)
		return
	}
	provider := authLoginProvider(pathParam(r, "provider"))
	if provider == "" {
		http.Error(w, "unknown login provider", http.StatusNotFound)
		return
	}
	login, ok := s.authLoginGet(provider, pathParam(r, "loginId"))
	if !ok {
		// The login is gone, not unfinished: the page starts a new one rather
		// than polling forever.
		http.Error(w, "unknown login", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(authLoginStatusBody{
		Status:   login.status,
		URL:      login.url,
		UserCode: login.userCode,
		Error:    login.errText,
		BaseURL:  login.baseURL,
	})
}

// handleAuthLoginCancel ends a login the page walked away from. The state is
// written before the goroutine is stopped, so a poll that races the cancel sees
// "canceled" rather than a login that looks alive.
func (s *Server) handleAuthLoginCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s == nil {
		http.Error(w, "nil", http.StatusInternalServerError)
		return
	}
	provider := authLoginProvider(pathParam(r, "provider"))
	if provider == "" {
		http.Error(w, "unknown login provider", http.StatusNotFound)
		return
	}
	now := time.Now()
	cancel, ok := s.authLoginAbort(provider, pathParam(r, "loginId"), now.Add(authLoginRetention))
	if !ok {
		http.Error(w, "unknown login", http.StatusNotFound)
		return
	}
	if cancel != nil {
		cancel()
	}
	w.WriteHeader(http.StatusNoContent)
}

// authLoginProvider normalizes the provider path segment. An empty answer means
// this endpoint does not serve that provider, which is a 404 like an unknown
// login id.
func authLoginProvider(raw string) string {
	switch provider := strings.ToLower(strings.TrimSpace(raw)); provider {
	case authLoginOpenCode, authLoginChatGPT:
		return provider
	default:
		return ""
	}
}

// authLoginBaseURL is the provider base URL the page writes into the provider
// row when a login completes: configuration validation requires one, and the
// login decides which surface it belongs to — so this is the only place that
// value is decided, and the page writes the answer rather than one of its own.
//
// The OpenCode device flow runs against the Console, which is why the login is
// started as "opencode", but what it unlocks is the Go subscription's catalog:
// the row it writes is "opencode-go" and the base URL has to be that
// subscription's own root. The Console root is not a substitute. A non-empty
// base_url is the inference root as far as pkg/run/config.go is concerned, and
// the Go models are served under /go — opencode.ClientBaseURL strips and
// re-adds that segment per provider, so a row carrying the wrong root keeps
// routing while describing a surface other than the one it talks to.
//
// ChatGPT's endpoint is fixed by its transport, so it stays the one the openai
// package owns.
func authLoginBaseURL(provider string) string {
	if provider == authLoginChatGPT {
		return openai.CodexBaseURL
	}
	return opencode.ProviderBaseURL("opencode-go")
}

// authLoginReason turns a flow error into the phrase the page shows. The OAuth
// vocabulary the flows report is kept as it is: "canceled", "denied" and
// "expired" say what happened, and the page renders them as a sentence. An
// error that fits none of them is passed through, because a wrapped message
// still tells the user more than a generic one.
func authLoginReason(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return authLoginCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "expired"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "denied"):
		return "denied"
	case strings.Contains(message, "expired"), strings.Contains(message, "invalid_grant"):
		return "expired"
	}
	return err.Error()
}

// newAuthLoginID is an unguessable id for one login. It is a capability: anyone
// holding it can read the login's status, so it must come from a CSPRNG.
func newAuthLoginID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
