package gateway

import (
	"context"
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

func tokenAuthEnv(mode, token string) *process.Environment {
	return &process.Environment{Deps: run.Deps{AppCfg: &appcfg.Root{
		Gateway: appcfg.Gateway{
			Auth: appcfg.GatewayAuth{
				Mode:  mode,
				Token: token,
			},
		},
	}}}
}

func TestControlPlaneHTTPMiddlewareUsesGatewayAuthToken(t *testing.T) {
	env := tokenAuthEnv("token", "gw-secret")
	called := false
	handler := ControlPlaneHTTPMiddleware(env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	env := tokenAuthEnv("token", "gw-secret")
	handler := ControlPlaneHTTPMiddleware(env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	env := tokenAuthEnv("token", "")
	handler := ControlPlaneHTTPMiddleware(env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	env := tokenAuthEnv("token", "gw-secret")
	called := false
	handler := ControlPlaneHTTPMiddleware(env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	env := tokenAuthEnv("token", "gw-secret")
	handler := ControlPlaneHTTPMiddleware(env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	env := tokenAuthEnv("token", "gw-secret")
	handler := ControlPlaneHTTPMiddleware(env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	env := tokenAuthEnv("token", "gw-secret")
	handler := ControlPlaneHTTPMiddleware(env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/resources?token=gw-secret", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: credentials must not travel in the query string", rr.Code)
	}
}

// TestHandleChatWSFollowsAuthModeNone pins that the WebSocket handshake and
// the HTTP middleware apply the same auth rules: with mode none the socket is
// reachable even though a token is configured.
func TestHandleChatWSFollowsAuthModeNone(t *testing.T) {
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()

	s := &Server{
		Home:     home,
		Sessions: state.NewSessionStore(db, "main"),
		Env:      tokenAuthEnv("none", "gw-secret"),
	}
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if resp != nil && resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("status = 401: mode none must not gate the websocket")
	}
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	var connected wsServerMsg
	require.NoError(t, conn.ReadJSON(&connected))
	require.Equal(t, "connected", connected.Op)
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
		Env:      tokenAuthEnv("token", "gw-secret"),
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

func webSessionTestServer(mode, token string) (*Server, http.HandlerFunc) {
	s := &Server{Env: tokenAuthEnv(mode, token)}
	return s, s.handleWebSessionCreate
}

func TestWebSessionCreateSetsCookieForValidToken(t *testing.T) {
	_, handler := webSessionTestServer("token", "gw-secret")

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
	_, handler := webSessionTestServer("token", "gw-secret")

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
	_, handler := webSessionTestServer("token", "gw-secret")

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

func TestWebSessionCreateModeNoneSetsNoCookie(t *testing.T) {
	_, handler := webSessionTestServer("none", "gw-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/auth/session", strings.NewReader(`{"token":"anything"}`))
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if len(rr.Result().Cookies()) != 0 {
		t.Fatal("mode none must not set a cookie")
	}
}

func TestWebSessionCreateRejectsOversizedBody(t *testing.T) {
	_, handler := webSessionTestServer("token", "gw-secret")

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
	s := &Server{Env: tokenAuthEnv("token", "gw-secret")}
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

	// With the cookie the probe answers the auth mode.
	req = httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: webSessionCookieName("gw-secret"), Value: webSessionValue("gw-secret")})
	rr = httptest.NewRecorder()
	chain.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("probe status = %d, want 200", rr.Code)
	}
	if body := rr.Body.String(); body != `{"auth_mode":"token"}` {
		t.Fatalf("probe body = %q", body)
	}
}

// The owner's page is never throttled: one load of it is more requests than
// the burst allowed to strangers, so a few reloads used to earn the signed-in
// owner a 429. Requests without the credentials are still held to the limit.
func TestRateLimitHoldsStrangersNotTheSignedInOwner(t *testing.T) {
	s := &Server{Env: tokenAuthEnv("token", "gw-secret")}
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
