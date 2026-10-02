// Control-plane authentication and its HTTP middleware.
package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
)

// Sentinel failures of authorizeControlPlane; each maps to the 401 body the
// control plane has always answered with.
var (
	errGatewayAuthModeUnsupported = errors.New("unsupported gateway auth mode")
	errGatewayTokenMissing        = errors.New("gateway token missing")
	errGatewayUnauthorized        = errors.New("unauthorized")
)

func gatewayAuthMode(cfg *appcfg.Root) string {
	mode := strings.ToLower(strings.TrimSpace(cfg.Gateway.Auth.Mode))
	if mode == "" {
		mode = "token"
	}
	return mode
}

func ControlPlaneTokenFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	token := ""
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		token = strings.TrimSpace(auth[7:])
	}
	if token == "" {
		token = strings.TrimSpace(r.Header.Get("X-API-Key"))
	}
	return token
}

func ControlPlaneAuthorized(r *http.Request, expected string) bool {
	exp := strings.TrimSpace(expected)
	if exp == "" {
		return true
	}
	got := ControlPlaneTokenFromRequest(r)
	if len(got) != len(exp) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(exp)) == 1
}

// authorizeControlPlane is the single decision point for control-plane
// access: the HTTP middleware and the chat WebSocket handshake both call it.
// A request passes when its header credentials (Authorization: Bearer,
// X-API-Key) or its web session cookie match the configured token. Query
// strings never carry credentials — they end up in access logs and browser
// history.
func authorizeControlPlane(r *http.Request, cfg *appcfg.Root) error {
	switch mode := gatewayAuthMode(cfg); mode {
	case "none":
		return nil
	case "token":
	default:
		return errGatewayAuthModeUnsupported
	}
	expected := strings.TrimSpace(cfg.Gateway.Auth.Token)
	if expected == "" {
		return errGatewayTokenMissing
	}
	if ControlPlaneAuthorized(r, expected) {
		return nil
	}
	if webSessionCookieValid(r, expected) {
		return nil
	}
	return errGatewayUnauthorized
}

func writeControlPlaneUnauthorized(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(body))
}

func writeControlPlaneAuthError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errGatewayAuthModeUnsupported):
		writeControlPlaneUnauthorized(w, `{"error":"unsupported gateway auth mode"}`)
	case errors.Is(err, errGatewayTokenMissing):
		slog.Error("gateway control plane token missing", "path", r.URL.Path)
		writeControlPlaneUnauthorized(w, `{"error":"gateway token missing"}`)
	default:
		slog.Error("gateway control plane auth failed", "path", r.URL.Path, "auth", telemetry.RedactLogLine(r.Header.Get("Authorization")))
		writeControlPlaneUnauthorized(w, `{"error":"unauthorized"}`)
	}
}

func ControlPlaneHTTPMiddleware(env *process.Environment, inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if env == nil || env.Deps.AppCfg == nil {
			inner.ServeHTTP(w, r)
			return
		}
		cfg := env.Deps.AppCfg
		path := strings.TrimSpace(r.URL.Path)
		// The exempt inbound paths are those of the agent whose channels are
		// mounted, which is the agent the runner is bound to.
		if appcfg.GatewayControlPlaneAuthExemptPath(path, cfg, envAgentID(env)) {
			inner.ServeHTTP(w, r)
			return
		}
		// The sign-in exchange itself is the one request that legitimately
		// arrives without credentials: it is how a browser obtains them.
		if r.Method == http.MethodPost && path == "/api/auth/session" {
			inner.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodOptions {
			inner.ServeHTTP(w, r)
			return
		}
		if appcfg.GatewayAllowsAnonymousGET(path) && r.Method == http.MethodGet {
			inner.ServeHTTP(w, r)
			return
		}
		if err := authorizeControlPlane(r, cfg); err != nil {
			writeControlPlaneAuthError(w, r, err)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

func ServeHTTPChain(env *process.Environment, gw *Server, inner http.Handler) http.Handler {
	rl := newIPRateLimiter(40, 120)
	// The channel dispatcher is built once and consults the registry per
	// request, so it always reflects the currently bound agent.
	withChannels := ControlPlaneHTTPMiddleware(env, gw.channelHandler(inner))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(clientIP(r)) {
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		if r.URL.Path == "/ws/chat" {
			if gw != nil {
				gw.HandleChatWS(w, r)
			} else {
				http.NotFound(w, r)
			}
			return
		}
		withChannels.ServeHTTP(w, r)
	})
}

// envAgentID names the primary agent this process is serving. The runner is
// rebound to the active agent on every switch, so it is the live answer.
func envAgentID(env *process.Environment) string {
	if env == nil || env.Runner == nil {
		return ""
	}
	return strings.TrimSpace(env.Runner.AgentName)
}

const webSessionMessage = "forebrain-harness web session v1"

// webSessionCookieName derives the cookie name from the token: two gateways
// on one machine (different homes, different tokens) never overwrite each
// other's cookie, while replicas sharing a token agree on the name.
func webSessionCookieName(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return "forebrain_session_" + hex.EncodeToString(sum[:4])
}

// webSessionValue derives the cookie value from the token with no stored
// state: any replica that knows the token can verify it, rotating the token
// invalidates every cookie at once, and a leaked cookie does not reveal the
// token it was derived from.
func webSessionValue(token string) string {
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(token)))
	_, _ = mac.Write([]byte(webSessionMessage))
	return hex.EncodeToString(mac.Sum(nil))
}

// webSessionCookieValid reports whether the request carries the session
// cookie this token issues. The comparison is constant-time on a fixed-length
// digest, so timing does not leak how much of the value matched.
func webSessionCookieValid(r *http.Request, token string) bool {
	cookie, err := r.Cookie(webSessionCookieName(token))
	if err != nil {
		return false
	}
	got := strings.TrimSpace(cookie.Value)
	want := webSessionValue(token)
	return len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func webSessionCookie(token string, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     webSessionCookieName(token),
		Value:    webSessionValue(token),
		Path:     "/",
		MaxAge:   2592000,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
	}
}

// requestIsHTTPS reports whether the connection the browser actually used is
// TLS, either directly or behind a proxy that forwards the protocol.
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

type webSessionRequest struct {
	Token string `json:"token"`
}

// handleWebSessionCreate exchanges the gateway token for the session cookie.
// It is the one control-plane endpoint reachable without credentials: the
// browser has nothing else to present yet. The token itself never becomes a
// browser-readable value.
func (s *Server) handleWebSessionCreate(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	mode := gatewayAuthMode(cfg)
	if mode == "none" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if mode != "token" {
		writeControlPlaneUnauthorized(w, `{"error":"unsupported gateway auth mode"}`)
		return
	}
	var req webSessionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		http.Error(w, "trailing JSON content", http.StatusBadRequest)
		return
	}
	expected := strings.TrimSpace(cfg.Gateway.Auth.Token)
	if expected == "" {
		slog.Error("gateway control plane token missing", "path", r.URL.Path)
		writeControlPlaneUnauthorized(w, `{"error":"gateway token missing"}`)
		return
	}
	got := strings.TrimSpace(req.Token)
	if len(got) != len(expected) || subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
		slog.Warn("gateway web sign-in rejected", "remote", clientIP(r))
		writeControlPlaneUnauthorized(w, `{"error":"invalid gateway token"}`)
		return
	}
	http.SetCookie(w, webSessionCookie(expected, requestIsHTTPS(r)))
	w.WriteHeader(http.StatusNoContent)
}

// handleWebSessionProbe answers whether the caller already holds a valid
// session: reaching it at all means the control-plane middleware let the
// request through, so the body only reports the configured auth mode.
func (s *Server) handleWebSessionProbe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"auth_mode":"` + gatewayAuthMode(s.liveCfg()) + `"}`))
}
