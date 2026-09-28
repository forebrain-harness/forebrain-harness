// Control-plane authentication and its HTTP middleware.
package gateway

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
)

func ControlPlaneTokenFromRequest(r *http.Request, queryToken string) string {
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
	if token == "" && queryToken != "" {
		token = strings.TrimSpace(queryToken)
	}
	return token
}

func ControlPlaneAuthorized(r *http.Request, expected string, queryToken string) bool {
	exp := strings.TrimSpace(expected)
	if exp == "" {
		return true
	}
	got := ControlPlaneTokenFromRequest(r, queryToken)
	if len(got) != len(exp) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(exp)) == 1
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
		if r.Method == http.MethodOptions {
			inner.ServeHTTP(w, r)
			return
		}
		if appcfg.GatewayAllowsAnonymousGET(path) && r.Method == http.MethodGet {
			inner.ServeHTTP(w, r)
			return
		}
		authMode := strings.ToLower(strings.TrimSpace(cfg.Gateway.Auth.Mode))
		if authMode == "" {
			authMode = "token"
		}
		if authMode == "none" {
			inner.ServeHTTP(w, r)
			return
		}
		tok := strings.TrimSpace(cfg.Gateway.Auth.Token)
		if authMode != "token" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unsupported gateway auth mode"}`))
			return
		}
		q := r.URL.Query().Get("token")
		if tok == "" {
			slog.Error("gateway control plane token missing", "path", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"gateway token missing"}`))
			return
		}
		if !ControlPlaneAuthorized(r, tok, q) {
			slog.Error("gateway control plane auth failed", "path", r.URL.Path, "auth", telemetry.RedactLogLine(r.Header.Get("Authorization")))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
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
