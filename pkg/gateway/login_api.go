// Web login for the subscriptions the models page signs in to.
//
// pkg/subscription owns the flows (plan 264): which sign-ins exist, how each one
// is driven, and what a finished one unlocks. This file is the gateway's half of
// it — start a flow, let the page poll it, take a pasted callback, cancel it —
// and it names no subscription of its own. A browser page can neither print a
// status line nor open a browser on the machine the user is sitting in front of,
// so the flow runs here and the page renders what the handle reports.
//
// The credentials themselves are not touched here: a flow saves them the way the
// terminal does, so one login serves both surfaces and the last login wins.
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/subscription"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
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

// The subscriptions this gateway offers, and the one a path or a stored row
// names. They are pkg/subscription's own registry in production; a variable
// because a test drives these endpoints with sign-ins of its own rather than
// reaching a vendor.
var (
	gatewaySubscriptions    = subscription.Registry
	gatewaySubscriptionByID = subscription.ByID
)

// errAuthLoginInProgress is the answer to a second login for the same provider.
// Both flows own fixed resources — a device code, and the loopback ports 1455
// and 1457 — so two at once would fight over them.
var errAuthLoginInProgress = errors.New("another login is in progress")

// gatewayAuthLogin is one web login as the page sees it. It is the login
// analogue of gatewayMcpPKCE: per-subscription state, guarded by the server's own
// mutex, and dropped once it can no longer be read.
type gatewayAuthLogin struct {
	provider string
	status   string
	// flow is the flow kind this login runs, so the page knows whether it shows
	// a device code or asks for a pasted address.
	flow string
	// errText is the failure reason phrase, empty unless status is failed.
	errText string
	// baseURL is the model row's base URL, set when the login completes: the
	// endpoint the subscription's provider serves.
	baseURL string
	// keepUntil is when the login stops being readable. Zero while pending: the
	// flow's own timeout already bounds that.
	keepUntil time.Time
	cancel    context.CancelFunc
	// login is the running sign-in. Every answer it learns — the authorization
	// URL, a device code — is read from it while it runs, and a pasted callback
	// is handed to it. Nil only for a login registered by a test double.
	login subscription.Login
}

// authLoginStart records a new pending login for provider and returns its id.
// cancel stops the flow and is stored with the login before the caller can
// observe the id, so a cancel request can never arrive before there is something
// to cancel.
//
// One login per provider: a device code and the loopback ports are fixed
// resources, so two at once would fight over them.
func (s *Server) authLoginStart(provider string, keepUntil time.Time, cancel context.CancelFunc,
	login subscription.Login) (string, error) {
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
	for _, running := range s.authLogins {
		if running.provider == provider && running.status == authLoginPending {
			return "", errAuthLoginInProgress
		}
	}
	if s.authLogins == nil {
		s.authLogins = make(map[string]*gatewayAuthLogin)
	}
	flow := ""
	if sub, ok := gatewaySubscriptionByID(provider); ok {
		flow, _ = authLoginFlow(sub)
	}
	s.authLogins[id] = &gatewayAuthLogin{
		provider:  provider,
		status:    authLoginPending,
		flow:      flow,
		keepUntil: keepUntil,
		cancel:    cancel,
		login:     login,
	}
	return id, nil
}

// authLoginFinish records the end of a flow. It never rewrites a login that
// already ended: a page's cancel and the goroutine's own error race here, and the
// answer the page already saw is the one that counts.
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

// authLoginAbort ends a pending login as canceled and returns the flow's cancel,
// so a handler can stop the goroutine after the state is already what the page
// will read. An already finished login is left alone, and its cancel is still
// returned (a completed flow has nothing left to stop).
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

// authLoginGet returns a copy of a login, or false when the id is unknown to this
// provider — a gateway restart clears the map, and a pruned id is as good as
// unknown.
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

// pruneAuthLoginsLocked drops finished logins whose retention has run out, so the
// map does not grow for the life of the server. A pending login is never pruned:
// the flow's own timeout ends it.
func (s *Server) pruneAuthLoginsLocked(now time.Time) {
	for id, login := range s.authLogins {
		if !login.keepUntil.IsZero() && now.After(login.keepUntil) {
			delete(s.authLogins, id)
		}
	}
}

// authLoginFlow picks the flow this surface can drive, best first: a device code
// it shows and polls, or the paste-a-callback pair a remote browser needs. It
// never picks browser_callback — opening a browser belongs to the surface sitting
// in front of the user, and the gateway may be running where nobody is.
func authLoginFlow(sub subscription.Subscription) (string, bool) {
	for _, flow := range sub.Flows() {
		switch flow {
		case subscription.FlowDeviceCode, subscription.FlowPasteCallback:
			return flow, true
		}
	}
	return "", false
}

// subscriptionEnv is what a subscription reads and writes: the home its
// credentials live under, and the state database its model rows go into.
func (s *Server) subscriptionEnv() subscription.Env {
	env := subscription.Env{}
	if s == nil {
		return env
	}
	env.Home = s.Home
	if s.Env != nil {
		env.DB = s.Env.SQL
	}
	return env
}

// handleAuthLoginStart begins a web login and answers 202 with the id to poll.
// The flow runs in the background — the response cannot wait for a human to
// finish in a browser — and the subscription's provider endpoint is part of the
// completion answer, ready to write into the model row.
func (s *Server) handleAuthLoginStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s == nil {
		http.Error(w, "nil", http.StatusInternalServerError)
		return
	}
	sub, ok := gatewaySubscriptionByID(pathParam(r, "provider"))
	if !ok {
		http.Error(w, "unknown login provider", http.StatusNotFound)
		return
	}
	provider := sub.ID()
	// Before starting a flow, ask whether this machine's login already answers
	// the request. Usable and Unreachable both complete: the model fetch the page
	// runs next is the same fact source and reports its own failures — a registry
	// being unreachable is not a dead login, and pushing a working login back
	// through the browser would be the very bug this fixes. Missing and dead
	// start the flow; the page is the progress display and the flow reports its
	// own failures.
	probeCtx, probeCancel := context.WithTimeout(r.Context(), modelsDiscoveryTimeout)
	check := sub.Probe(probeCtx, s.subscriptionEnv())
	probeCancel()
	if check.State == turn.SignInUsable || check.State == turn.SignInUnreachable {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"provider": provider,
			"status":   authLoginComplete,
			"base_url": authLoginBaseURL(provider),
		})
		return
	}
	flow, ok := authLoginFlow(sub)
	if !ok {
		http.Error(w, "this sign-in has no flow a gateway can drive", http.StatusNotFound)
		return
	}
	now := time.Now()
	keepUntil := now.Add(authLoginTimeout + authLoginRetention)
	// The flow outlives this request: its context is the login's own timeout, so
	// a page that walks away still ends the goroutine and its listener.
	ctx, cancel := context.WithTimeout(context.Background(), authLoginTimeout)
	login, err := sub.Start(ctx, s.subscriptionEnv(), flow)
	if err != nil {
		cancel()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := s.authLoginStart(provider, keepUntil, cancel, login)
	if err != nil {
		cancel()
		status := http.StatusConflict
		if !errors.Is(err, errAuthLoginInProgress) {
			status = http.StatusInternalServerError
		}
		http.Error(w, err.Error(), status)
		return
	}
	// The goroutine keeps its own reference to the server: it is the owner of the
	// login state, and the handler's request may end long before the human
	// finishes in a browser.
	owner := s
	go func() {
		defer cancel()
		owner.authLoginFinish(id, login.Wait(ctx), authLoginBaseURL(provider), keepUntil)
	}()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"login_id": id, "provider": provider})
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
	// Flow is the flow kind being driven, so the page knows whether to show a
	// device code or ask for a pasted address.
	Flow string `json:"flow"`
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
	sub, ok := gatewaySubscriptionByID(pathParam(r, "provider"))
	if !ok {
		http.Error(w, "unknown login provider", http.StatusNotFound)
		return
	}
	login, ok := s.authLoginGet(sub.ID(), pathParam(r, "loginId"))
	if !ok {
		// The login is gone, not unfinished: the page starts a new one rather
		// than polling forever.
		http.Error(w, "unknown login", http.StatusNotFound)
		return
	}
	body := authLoginStatusBody{
		Status:  login.status,
		Error:   login.errText,
		BaseURL: login.baseURL,
		Flow:    login.flow,
	}
	// What the flow has learned so far is read from the handle itself, outside
	// the server's lock: the handle is the owner of those two answers and keeps
	// them readable after the flow ends, so a late poll still shows the code the
	// user was asked to confirm.
	if login.login != nil {
		body.URL = login.login.AuthorizeURL()
		body.UserCode = login.login.UserCode()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// handleAuthLoginCallback takes the address a user pasted from their browser's
// address bar after approving a sign-in whose redirect could not reach this
// machine. The flow decides whether it is theirs; the answer is a short
// machine-readable reason the page words itself. The address carries a one-time
// authorization code, so it is never logged or echoed.
func (s *Server) handleAuthLoginCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s == nil {
		http.Error(w, "nil", http.StatusInternalServerError)
		return
	}
	sub, ok := gatewaySubscriptionByID(pathParam(r, "provider"))
	if !ok {
		http.Error(w, "unknown login provider", http.StatusNotFound)
		return
	}
	login, ok := s.authLoginGet(sub.ID(), pathParam(r, "loginId"))
	if !ok {
		http.Error(w, "unknown login", http.StatusNotFound)
		return
	}
	// Only the flow that asks for a pasted address has anywhere to put one.
	if login.flow != subscription.FlowPasteCallback {
		http.Error(w, "unknown login provider", http.StatusNotFound)
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil || strings.TrimSpace(body.URL) == "" {
		http.Error(w, "bad_request", http.StatusBadRequest)
		return
	}
	if login.status != authLoginPending || login.login == nil {
		http.Error(w, "login not pending", http.StatusConflict)
		return
	}
	if err := login.login.Submit(r.Context(), body.URL); err != nil {
		http.Error(w, callbackReason(err), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// callbackReason maps the flow's refusal to the short code the page words.
func callbackReason(err error) string {
	switch {
	case errors.Is(err, openai.ErrCallbackStateMismatch):
		return "state_mismatch"
	case errors.Is(err, openai.ErrCallbackNoCode):
		return "no_code"
	default:
		return "not_a_callback"
	}
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
	sub, ok := gatewaySubscriptionByID(pathParam(r, "provider"))
	if !ok {
		http.Error(w, "unknown login provider", http.StatusNotFound)
		return
	}
	now := time.Now()
	cancel, ok := s.authLoginAbort(sub.ID(), pathParam(r, "loginId"), now.Add(authLoginRetention))
	if !ok {
		http.Error(w, "unknown login", http.StatusNotFound)
		return
	}
	if cancel != nil {
		cancel()
	}
	w.WriteHeader(http.StatusNoContent)
}

// authLoginBaseURL is the base URL the page writes into the model row when a
// login completes: the endpoint the subscription's provider serves. A login and
// the provider its rows carry are the same registry entry (plan 264), so this is
// the one place that value is decided and the page writes the answer rather than
// one of its own — OpenCode's Console root, for instance, is not a substitute for
// the Go subscription's own inference root, and a row carrying the wrong root
// keeps routing while describing a surface other than the one it talks to.
func authLoginBaseURL(provider string) string {
	builtin, ok := config.BuiltinProviderByID(provider)
	if !ok || len(builtin.Endpoints) == 0 {
		return ""
	}
	return builtin.Endpoints[0].BaseURL
}

// authLoginReason turns a flow error into the phrase the page shows. The OAuth
// vocabulary the flows report is kept as it is: "canceled", "denied" and
// "expired" say what happened, and the page renders them as a sentence. An error
// that fits none of them is passed through, because a wrapped message still tells
// the user more than a generic one.
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
