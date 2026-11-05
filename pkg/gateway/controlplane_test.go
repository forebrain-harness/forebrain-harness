package gateway

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestControlPlaneTokenFromRequest(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer abc")
	if got := ControlPlaneTokenFromRequest(r); got != "abc" {
		t.Fatalf("got %q", got)
	}
	r.Header.Del("Authorization")
	r.Header.Set("X-API-Key", "def")
	if got := ControlPlaneTokenFromRequest(r); got != "def" {
		t.Fatalf("got %q", got)
	}
	if got := ControlPlaneTokenFromRequest(nil); got != "" {
		t.Fatalf("nil request should return empty, got %q", got)
	}
}

func TestControlPlaneTokenFromRequestFallbacks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		auth   string
		apiKey string
		want   string
	}{
		{
			name: "non bearer auth falls through to api key",
			auth: "Basic abc",
			want: "",
		},
		{
			name: "blank bearer falls through to api key",
			auth: "Bearer   ",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.auth != "" {
				r.Header.Set("Authorization", tt.auth)
			}
			if tt.apiKey != "" {
				r.Header.Set("X-API-Key", tt.apiKey)
			}
			if got := ControlPlaneTokenFromRequest(r); got != tt.want {
				t.Fatalf("ControlPlaneTokenFromRequest = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestControlPlaneAuthorized(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer secret")
	if !ControlPlaneAuthorized(r, "secret") {
		t.Fatal("expected authorized")
	}
	if ControlPlaneAuthorized(r, "other") {
		t.Fatal("expected unauthorized")
	}
	if !ControlPlaneAuthorized(nil, "") {
		t.Fatal("empty expected should authorize")
	}
}

func tokenAuthEnv(token string) *process.Environment {
	return &process.Environment{Deps: run.Deps{AppCfg: &appcfg.Root{
		Gateway: appcfg.Gateway{
			Auth: appcfg.GatewayAuth{Token: token},
		},
	}}}
}

func TestControlPlaneHTTPMiddlewareUsesGatewayAuthToken(t *testing.T) {
	env := tokenAuthEnv("gw-secret")
	called := false
	handler := ControlPlaneHTTPMiddleware(env, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/resources", nil)
	req.Header.Set("Authorization", "Bearer gw-secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if !called {
		t.Fatal("expected wrapped handler to be called")
	}
}

func TestControlPlaneHTTPMiddlewareRejectsWrongGatewayToken(t *testing.T) {
	env := tokenAuthEnv("gw-secret")
	handler := ControlPlaneHTTPMiddleware(env, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/resources", nil)
	req.Header.Set("Authorization", "Bearer wrong-secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestControlPlaneHTTPMiddlewareRejectsMissingGatewayToken(t *testing.T) {
	env := tokenAuthEnv("")
	handler := ControlPlaneHTTPMiddleware(env, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/resources", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestControlPlaneHTTPMiddlewareAcceptsWebSessionCookie(t *testing.T) {
	env := tokenAuthEnv("gw-secret")
	called := false
	handler := ControlPlaneHTTPMiddleware(env, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/resources", nil)
	req.AddCookie(&http.Cookie{Name: webSessionCookieName("gw-secret"), Value: webSessionValue("gw-secret")})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if !called {
		t.Fatal("expected wrapped handler to be called")
	}
}

func TestControlPlaneHTTPMiddlewareRejectsForgedWebSessionCookie(t *testing.T) {
	env := tokenAuthEnv("gw-secret")
	handler := ControlPlaneHTTPMiddleware(env, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/resources", nil)
	req.AddCookie(&http.Cookie{Name: webSessionCookieName("gw-secret"), Value: "forged"})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

// A page on another port of the same host is same-site, so the browser
// attaches the Strict cookie to its POST; the Origin it stamps is what keeps
// that page from acting with the user's session. Header credentials are not
// attached by browsers and stay origin-agnostic.
func TestControlPlaneHTTPMiddlewareRefusesCrossOriginCookieWrites(t *testing.T) {
	env := tokenAuthEnv("gw-secret")
	handler := ControlPlaneHTTPMiddleware(env, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	cookie := &http.Cookie{Name: webSessionCookieName("gw-secret"), Value: webSessionValue("gw-secret")}
	cases := []struct {
		name   string
		method string
		origin string
		cookie bool
		bearer bool
		want   int
	}{
		{name: "same-origin cookie write", method: http.MethodPost, origin: "http://gateway.test", cookie: true, want: http.StatusNoContent},
		{name: "cookie write without origin", method: http.MethodPut, cookie: true, want: http.StatusNoContent},
		{name: "other-port cookie write", method: http.MethodPost, origin: "http://gateway.test:3000", cookie: true, want: http.StatusForbidden},
		{name: "opaque origin cookie write", method: http.MethodDelete, origin: "null", cookie: true, want: http.StatusForbidden},
		{name: "cross-origin cookie read", method: http.MethodGet, origin: "http://gateway.test:3000", cookie: true, want: http.StatusNoContent},
		{name: "cross-origin bearer write", method: http.MethodPost, origin: "http://elsewhere.test", bearer: true, want: http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://gateway.test/api/resources", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.cookie {
				req.AddCookie(cookie)
			}
			if tc.bearer {
				req.Header.Set("Authorization", "Bearer gw-secret")
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %q)", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestControlPlaneHTTPMiddlewareIgnoresQueryToken(t *testing.T) {
	env := tokenAuthEnv("gw-secret")
	handler := ControlPlaneHTTPMiddleware(env, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/resources?token=gw-secret", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: credentials must not travel in the query string", rr.Code)
	}
}

// TestHandleChatWSRefusesWithoutAToken pins that the WebSocket handshake and
// the HTTP middleware apply the same rule: a gateway with no token has no
// authentication to offer, so it refuses rather than running open (plan 206).
func TestHandleChatWSRefusesWithoutAToken(t *testing.T) {
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()

	s := &Server{
		Home:     home,
		Sessions: state.NewSessionStore(db, "main"),
		Env:      tokenAuthEnv(""),
	}
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		conn.Close()
		t.Fatal("a gateway with no token must not accept an unauthenticated socket")
	}
	require.NotNil(t, resp)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestHandleChatWSRejectsCrossOriginHandshake pins the default origin check:
// a valid session cookie does not authorize a page on another origin to open
// the socket.
func TestHandleChatWSRejectsCrossOriginHandshake(t *testing.T) {
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()

	s := &Server{
		Home:     home,
		Sessions: state.NewSessionStore(db, "main"),
		Env:      tokenAuthEnv("gw-secret"),
	}
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	header := http.Header{}
	header.Set("Origin", "http://evil.example")
	header.Set("Cookie", webSessionCookieName("gw-secret")+"="+webSessionValue("gw-secret"))
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		conn.Close()
		t.Fatal("cross-origin handshake must be refused")
	}
	require.NotNil(t, resp)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func webSessionTestServer(token string) (*Server, http.HandlerFunc) {
	s := &Server{Env: tokenAuthEnv(token)}
	return s, s.handleWebSessionCreate
}

func TestWebSessionCreateSetsCookieForValidToken(t *testing.T) {
	_, handler := webSessionTestServer("gw-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/auth/session", strings.NewReader(`{"token":"gw-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Set-Cookie count = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != webSessionCookieName("gw-secret") {
		t.Fatalf("cookie name = %q", cookie.Name)
	}
	if cookie.Value != webSessionValue("gw-secret") {
		t.Fatalf("cookie value = %q", cookie.Value)
	}
	for _, check := range []struct{ attr, want string }{
		{"Path", "/"},
		{"Max-Age", "2592000"},
	} {
		if got := cookie.String(); !strings.Contains(got, check.attr+"="+check.want) {
			t.Fatalf("cookie %q missing %s=%s", got, check.attr, check.want)
		}
	}
	raw := cookie.String()
	if !strings.Contains(raw, "HttpOnly") {
		t.Fatalf("cookie %q must be HttpOnly", raw)
	}
	if !strings.Contains(raw, "SameSite=Strict") {
		t.Fatalf("cookie %q must be SameSite=Strict", raw)
	}
	if strings.Contains(raw, "Secure") {
		t.Fatalf("plain-HTTP sign-in cookie %q must not be Secure", raw)
	}
}

func TestWebSessionCreateMarksCookieSecureBehindHTTPS(t *testing.T) {
	_, handler := webSessionTestServer("gw-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/auth/session", strings.NewReader(`{"token":"gw-secret"}`))
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("cookie behind https must be Secure, got %#v", cookies)
	}
}

func TestWebSessionCreateRejectsWrongToken(t *testing.T) {
	_, handler := webSessionTestServer("gw-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/auth/session", strings.NewReader(`{"token":"not-the-token"}`))
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if body := rr.Body.String(); body != `{"error":"invalid gateway token"}` {
		t.Fatalf("body = %q", body)
	}
	if len(rr.Result().Cookies()) != 0 {
		t.Fatal("rejected sign-in must not set a cookie")
	}
}

func TestWebSessionCreateRejectsOversizedBody(t *testing.T) {
	_, handler := webSessionTestServer("gw-secret")

	big := fmt.Sprintf(`{"token":"%s"}`, strings.Repeat("a", 8192))
	req := httptest.NewRequest(http.MethodPost, "/api/auth/session", strings.NewReader(big))
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestWebSessionRoutesThroughFullChain walks ServeHTTPChain: the sign-in POST
// itself must pass the middleware without credentials, the probe GET must
// still require them, and a session cookie must satisfy them.
func TestWebSessionRoutesThroughFullChain(t *testing.T) {
	s := &Server{Env: tokenAuthEnv("gw-secret")}
	chain := ServeHTTPChain(s.Env, s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/auth/session":
			s.handleWebSessionCreate(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/api/auth/session":
			s.handleWebSessionProbe(w, r)
		default:
			http.NotFound(w, r)
		}
	}))

	// Sign-in without credentials reaches the handler.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/session", strings.NewReader(`{"token":"gw-secret"}`))
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("sign-in status = %d, want 204", rr.Code)
	}

	// The probe without credentials is rejected.
	req = httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	rr = httptest.NewRecorder()
	chain.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("probe status = %d, want 401", rr.Code)
	}

	// With the cookie the probe answers that the caller is signed in.
	req = httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: webSessionCookieName("gw-secret"), Value: webSessionValue("gw-secret")})
	rr = httptest.NewRecorder()
	chain.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("probe status = %d, want 200", rr.Code)
	}
	if body := rr.Body.String(); body != `{"authenticated":true}` {
		t.Fatalf("probe body = %q", body)
	}
}

// The owner's page is never throttled: one load of it is more requests than
// the burst allowed to strangers, so a few reloads used to earn the signed-in
// owner a 429. Requests without the credentials are still held to the limit.
func TestRateLimitHoldsStrangersNotTheSignedInOwner(t *testing.T) {
	s := &Server{Env: tokenAuthEnv("gw-secret")}
	chain := ServeHTTPChain(s.Env, s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	send := func(withCookie bool) int {
		req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
		req.RemoteAddr = "203.0.113.7:5555"
		if withCookie {
			req.AddCookie(&http.Cookie{Name: webSessionCookieName("gw-secret"), Value: webSessionValue("gw-secret")})
		}
		rr := httptest.NewRecorder()
		chain.ServeHTTP(rr, req)
		return rr.Code
	}
	for i := 0; i < 400; i++ {
		if code := send(true); code != http.StatusOK {
			t.Fatalf("signed-in request %d answered %d", i, code)
		}
	}
	limited := false
	for i := 0; i < 400 && !limited; i++ {
		limited = send(false) == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("requests without credentials were never limited")
	}
}

// The middleware is the outermost layer of the chain the gateway serves, so
// the websocket branch and the rate limiter's own rejections are reported
// like any other request. What is under test here is the shared
// telemetry.AccessLogMiddleware, through the chain that installs it.
func TestServeHTTPChainLogsEveryRequest(t *testing.T) {
	logs := captureDefaultSlog(t)
	chain := ServeHTTPChain(nil, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	serve := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.RemoteAddr = "203.0.113.7:41234"
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, req)
		return rec
	}

	if rec := serve("/api/runs"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the inner handler", rec.Code)
	}
	// The websocket path is served by the chain itself, before the router.
	if rec := serve("/ws/chat"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want the chain's own 404 without a server", rec.Code)
	}
	// The limiter bounds requests that carry no credentials; the access log
	// still reports them, which is the point of sitting outermost.
	var limited bool
	for i := 0; i < 300 && !limited; i++ {
		if serve("/login").Code == http.StatusTooManyRequests {
			limited = true
		}
	}
	if !limited {
		t.Fatal("the limiter never rejected a request, so the 429 path was not exercised")
	}

	byPath := map[string]string{}
	for _, line := range strings.Split(logs.String(), "\n") {
		if !strings.Contains(line, "msg=request") {
			continue
		}
		var path, status string
		for _, field := range strings.Fields(line) {
			if v, ok := strings.CutPrefix(field, "path="); ok {
				path = v
			}
			if v, ok := strings.CutPrefix(field, "status="); ok {
				status = v
			}
		}
		byPath[path] = status
	}
	for path, want := range map[string]string{"/api/runs": "200", "/ws/chat": "404", "/login": "429"} {
		if got := byPath[path]; got != want {
			t.Errorf("logged status for %s = %q, want %q", path, got, want)
		}
	}
}

// newReplicaTestServer builds one gateway replica over a state database: its own
// owner identity and lease, the shared stores, the token its peers authenticate
// with, and the address they reach it at. Two of them over one database are what
// plan 213's forwarding is about.
func newReplicaTestServer(t *testing.T, db *sql.DB, home, owner, token, internalAddr string) *Server {
	t.Helper()
	ctx := context.Background()
	sessions := state.NewSessionStore(db, "main")
	store := &state.RunStore{DB: db, Owner: owner}
	stop, err := store.HoldOwnerLease(ctx)
	require.NoError(t, err)
	t.Cleanup(stop)
	require.NoError(t, store.RegisterReplica(ctx, state.RunOwnerKindGateway, internalAddr, "host-"+owner))
	cfg := &appcfg.Root{}
	cfg.Gateway.Auth.Token = token
	env := &process.Environment{
		Root: home,
		SQL:  db,
		Deps: run.Deps{Home: home, AppCfg: cfg, SessionStore: sessions, RunRT: store},
	}
	return &Server{
		Home: home, Sessions: sessions, RunRT: store,
		Actions: &state.ActionService{DB: db}, Env: env,
	}
}

// TestInternalEndpointsRequireTheTokenAndARegisteredReplica pins both halves of
// the peer authentication (plan 213 §3.2): the shared gateway token, and a source
// address that is a registered replica's. Neither alone opens the endpoint, and a
// browser's session cookie — the credential the web pages have — is not a peer
// credential at all.
func TestInternalEndpointsRequireTheTokenAndARegisteredReplica(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()

	s := newReplicaTestServer(t, db, home, "gateway-self", "gw-secret", "http://10.9.9.9:8787")
	chain := ServeHTTPChain(s.Env, s, http.HandlerFunc(http.NotFound))

	send := func(auth, remote, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, internalRunOpPath, strings.NewReader(body))
		req.RemoteAddr = remote
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, req)
		return rec
	}
	const op = `{"op":"run_cancel","run_id":"run-1"}`

	require.Equal(t, http.StatusUnauthorized, send("", "10.9.9.9:5000", op).Code,
		"no credentials must not open a peer endpoint")
	require.Equal(t, http.StatusUnauthorized, send("Bearer gw-secret", "10.9.9.8:5000", op).Code,
		"the token from a host that registered nothing must not open it")
	require.NotEqual(t, http.StatusUnauthorized, send("Bearer gw-secret", "10.9.9.9:5000", op).Code,
		"the token from a registered replica must reach the operation")

	// The browser's own credential: valid, and still not a peer's.
	cookieReq := httptest.NewRequest(http.MethodPost, internalRunOpPath, strings.NewReader(op))
	cookieReq.RemoteAddr = "10.9.9.9:5000"
	cookieReq.AddCookie(&http.Cookie{Name: webSessionCookieName("gw-secret"), Value: webSessionValue("gw-secret")})
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, cookieReq)
	require.Equal(t, http.StatusUnauthorized, rec.Code, "a session cookie must never open a peer endpoint")
}

// TestInternalSourceAcceptsARegisteredHostname pins the form the tested
// multi-replica deployment registers (plan 226): a compose service name, not an
// IP literal. The address compared is still the one a live replica wrote down —
// its row is resolved, never the caller's name — so the check keeps its promise
// that the caller cannot talk its way in.
func TestInternalSourceAcceptsARegisteredHostname(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()

	s := newReplicaTestServer(t, db, home, "gateway-self", "gw-secret", "http://localhost:6060")
	require.True(t, s.internalSourceRegistered(ctx, "127.0.0.1:6060"),
		"a registered hostname that resolves to the caller is a registered replica")
	require.False(t, s.internalSourceRegistered(ctx, "10.10.10.10:6060"),
		"an address no replica registered is still refused")

	// A row that does not resolve is a row that does not match: it must not fail
	// the check for the rows that do.
	broken := &state.RunStore{DB: db, Owner: "gateway-broken"}
	stop, err := broken.HoldOwnerLease(ctx)
	require.NoError(t, err)
	defer stop()
	require.NoError(t, broken.RegisterReplica(ctx, state.RunOwnerKindGateway, "http://replica-does-not-resolve.invalid:6060", "host-broken"))
	require.True(t, s.internalSourceRegistered(ctx, "127.0.0.1:6060"),
		"one unresolvable registered row must not fail the whole check")
}

// TestInternalRunOpRefusesAnUnknownOperation pins that the forwarded operation
// is a name from the table, not an arbitrary call: the peer runs the handler the
// table names and nothing else.
func TestInternalRunOpRefusesAnUnknownOperation(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()

	s := newReplicaTestServer(t, db, home, "gateway-self", "gw-secret", "http://10.9.9.9:8787")
	req := httptest.NewRequest(http.MethodPost, internalRunOpPath, strings.NewReader(`{"op":"delete_everything"}`))
	rec := httptest.NewRecorder()
	s.handleInternalRunOp(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	// The endpoint serves POST only, like every operation it carries.
	getRec := httptest.NewRecorder()
	s.handleInternalRunOp(getRec, httptest.NewRequest(http.MethodGet, internalRunOpPath, nil))
	require.Equal(t, http.StatusMethodNotAllowed, getRec.Code)
}

// newInternalWSTestServer serves the peer socket for the confinement tests.
func newInternalWSTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	s := newReplicaTestServer(t, db, home, "gateway-self", "gw-secret", "http://10.9.9.9:8787")
	require.NoError(t, s.Sessions.Ensure(ctx, "sid", "sid"))
	require.NoError(t, s.Sessions.Ensure(ctx, "other", "other"))
	server := httptest.NewServer(http.HandlerFunc(s.handleInternalChatWS))
	t.Cleanup(server.Close)
	return s, "ws" + strings.TrimPrefix(server.URL, "http") + internalWSPath + "?session=sid"
}

// TestInternalChatWSServesOnlyItsOwnSession pins what a relayed socket may do
// (plan 213 §3.2): it binds the conversation it was opened for, and neither
// starts a run of its own nor reaches a second conversation.
func TestInternalChatWSServesOnlyItsOwnSession(t *testing.T) {
	_, wsURL := newInternalWSTestServer(t)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	send := func(m wsClientMsg) wsServerMsg {
		t.Helper()
		require.NoError(t, conn.WriteJSON(m))
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		for {
			var got wsServerMsg
			require.NoError(t, conn.ReadJSON(&got))
			if got.Op == "session_bound" || got.Op == "run_error" {
				return got
			}
		}
	}

	refused := send(wsClientMsg{Op: "start_run", RequestID: "r1", SessionID: "sid", Message: wsClientMessage{Content: "hello"}})
	require.Equal(t, "run_error", refused.Op)
	require.Equal(t, "internal_scope", refused.Message)

	foreign := send(wsClientMsg{Op: "bind_session", RequestID: "r2", SessionID: "other"})
	require.Equal(t, "run_error", foreign.Op)
	require.Equal(t, "internal_scope", foreign.Message)

	bound := send(wsClientMsg{Op: "bind_session", RequestID: "r3"})
	require.Equal(t, "session_bound", bound.Op)
	require.Equal(t, "sid", bound.SessionID)
}
