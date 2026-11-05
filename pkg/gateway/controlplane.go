// Control-plane authentication and its HTTP middleware.
package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
)

// Sentinel failures of authorizeControlPlane; each maps to the 401 body the
// control plane has always answered with, except a cross-origin cookie write,
// which is authenticated but not allowed (403).
var (
	errGatewayTokenMissing = errors.New("gateway token missing")
	errGatewayUnauthorized = errors.New("unauthorized")
	errGatewayCrossOrigin  = errors.New("cross-origin request refused")
)

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
//
// The cookie is the one credential a browser attaches on its own, so it only
// authorizes a state-changing request issued by this gateway's own pages.
// SameSite=Strict keeps it away from other sites, but a site is a host, not
// an origin: a page served from any other port of the same host still gets
// the cookie attached, and a form-encoded or text/plain POST reaches the
// handler without a CORS preflight. The Origin a browser stamps on such a
// request is what tells the two apart — the same comparison the WebSocket
// upgrade applies to its handshake.
//
// The returned origin is the address a browser reached this gateway at, for the
// cookie-session write the public-address auto-detection records (plan 206
// §3.1); every other passing request reports "".
func authorizeControlPlane(r *http.Request, cfg *appcfg.Root) (string, error) {
	expected := strings.TrimSpace(cfg.Gateway.Auth.Token)
	if expected == "" {
		return "", errGatewayTokenMissing
	}
	if ControlPlaneAuthorized(r, expected) {
		return "", nil
	}
	if webSessionCookieValid(r, expected) {
		if !requestOriginMatchesHost(r) {
			return "", errGatewayCrossOrigin
		}
		return cookieSessionOrigin(r), nil
	}
	return "", errGatewayUnauthorized
}

// cookieSessionOrigin is the Origin of a state-changing request only: a page
// load says nothing about where the person will open a link from, and a
// cross-origin write never gets this far.
func cookieSessionOrigin(r *http.Request) string {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return ""
	}
	return normalizeWebOrigin(r.Header.Get("Origin"))
}

// observedOriginSection is the fb_config row that remembers the address the
// last browser write request came from. It is deliberately not a Root section:
// it is not a setting a person chooses, it is what the control plane observed,
// and composeSectionsFrom only composes the sections Root declares.
const observedOriginSection = "gateway.observed_origin"

// normalizeWebOrigin reduces an Origin header to scheme://host[:port]. A value
// that is not an absolute http(s) URL — an opaque "null" origin included — has
// nothing a link can be built from, and reads as "".
func normalizeWebOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	return scheme + "://" + strings.ToLower(u.Host)
}

// isLoopbackOrigin reports whether origin names this machine. A login from the
// host itself is not the address other people open, so it never displaces one
// that is (plan 206 §3.1).
func isLoopbackOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := strings.Trim(strings.ToLower(u.Hostname()), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// noteWebOrigin records origin as the address batch notifications link to. An
// unchanged value is not rewritten, and a loopback address does not replace a
// stored non-loopback one. Recording never fails a request: without a stored
// origin the notification simply carries no link.
func (s *Server) noteWebOrigin(origin string) {
	if s == nil || s.Env == nil || s.Env.SQL == nil {
		return
	}
	origin = normalizeWebOrigin(origin)
	if origin == "" {
		return
	}
	s.observedOriginMu.Lock()
	defer s.observedOriginMu.Unlock()
	if origin == s.observedOrigin {
		return
	}
	store := &state.ConfigStore{DB: s.Env.SQL}
	ctx := context.Background()
	stored, err := readObservedOrigin(ctx, store)
	if err != nil {
		slog.Warn("gateway observed origin: read", "err", err)
		return
	}
	if stored == origin {
		s.observedOrigin = origin
		return
	}
	if isLoopbackOrigin(origin) && stored != "" && !isLoopbackOrigin(stored) {
		s.observedOrigin = stored
		return
	}
	encoded, err := json.Marshal(origin)
	if err != nil {
		return
	}
	if err := store.SaveCredential(ctx, observedOriginSection, encoded); err != nil {
		slog.Warn("gateway observed origin: save", "err", err)
		return
	}
	s.observedOrigin = origin
}

// observedWebOrigin is the address links are built from, or "" when no browser
// has reached this gateway yet.
func (s *Server) observedWebOrigin(ctx context.Context) string {
	if s == nil || s.Env == nil || s.Env.SQL == nil {
		return ""
	}
	s.observedOriginMu.Lock()
	defer s.observedOriginMu.Unlock()
	if s.observedOrigin != "" {
		return s.observedOrigin
	}
	origin, err := readObservedOrigin(ctx, &state.ConfigStore{DB: s.Env.SQL})
	if err != nil {
		slog.Warn("gateway observed origin: read", "err", err)
		return ""
	}
	s.observedOrigin = origin
	return origin
}

func readObservedOrigin(ctx context.Context, store *state.ConfigStore) (string, error) {
	raw, err := store.LoadCredential(ctx, observedOriginSection)
	if err != nil || len(raw) == 0 {
		return "", err
	}
	var origin string
	if err := json.Unmarshal(raw, &origin); err != nil {
		return "", err
	}
	return normalizeWebOrigin(origin), nil
}

// requestOriginMatchesHost reports whether a cookie-carrying request may act.
// Safe methods change nothing and a browser keeps their cross-origin answers
// from the calling page, so only the others are checked; for those, an Origin
// header — which browsers set on every cross-origin write and scripts cannot
// forge — must name the host the request was sent to. A request without one
// did not come from a page of another origin.
func requestOriginMatchesHost(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

func writeControlPlaneUnauthorized(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(body))
}

func writeControlPlaneAuthError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errGatewayTokenMissing):
		slog.Error("gateway control plane token missing", "path", r.URL.Path)
		writeControlPlaneUnauthorized(w, `{"error":"gateway token missing"}`)
	case errors.Is(err, errGatewayCrossOrigin):
		slog.Warn("gateway control plane refused a cross-origin request", "path", r.URL.Path, "origin", r.Header.Get("Origin"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"cross-origin request refused"}`))
	default:
		slog.Error("gateway control plane auth failed", "path", r.URL.Path, "auth", telemetry.RedactLogLine(r.Header.Get("Authorization")))
		writeControlPlaneUnauthorized(w, `{"error":"unauthorized"}`)
	}
}

// The two endpoints a replica exposes to the other replicas (plan 213 §3.2):
// the WebSocket a relayed browser connection speaks through, and the one-shot
// operation a run-bound HTTP request is forwarded as. Both carry the shared
// gateway token and are checked against the replicas registered in the database,
// so a browser that merely owns the sign-in cookie cannot reach them.
const (
	internalWSPath    = "/internal/v1/ws"
	internalRunOpPath = "/internal/v1/run-op"
)

// internalRequestAuthorized is the whole of the peer authentication, and both
// halves must hold: the shared gateway token — the same forebrain.yaml every
// replica reads — and a TCP peer that is the host of a replica whose heartbeat
// is fresh. The token alone would let anyone who reaches the port drive every
// run; the source check alone would authenticate nobody.
func (s *Server) internalRequestAuthorized(r *http.Request) bool {
	if s == nil || r == nil {
		return false
	}
	cfg := s.liveCfg()
	if cfg == nil {
		return false
	}
	// The bearer header only: a browser's session cookie is exactly the
	// credential that must never open a replica-to-replica endpoint.
	token := strings.TrimSpace(cfg.Gateway.Auth.Token)
	if token == "" || !ControlPlaneAuthorized(r, token) {
		return false
	}
	return s.internalSourceRegistered(r.Context(), r.RemoteAddr)
}

// internalSourceRegistered reports whether the request's TCP peer is the host of
// a replica that is heartbeating right now. X-Forwarded-For is deliberately not
// consulted: it is a header the caller writes, and this check exists to be the
// half the caller cannot talk its way out of.
func (s *Server) internalSourceRegistered(ctx context.Context, remoteAddr string) bool {
	if s == nil || s.RunRT == nil {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	addrs, err := s.RunRT.LiveReplicaAddrs(ctx)
	if err != nil {
		slog.Warn("gateway internal auth: read registered replicas", "err", err)
		return false
	}
	for _, addr := range addrs {
		u, parseErr := url.Parse(strings.TrimSpace(addr))
		if parseErr != nil || u.Host == "" {
			continue
		}
		for _, peer := range replicaHostIPs(ctx, u.Hostname()) {
			if peer.Equal(ip) {
				return true
			}
		}
	}
	return false
}

// replicaHostIPs is the set of addresses one registered internal address stands
// for. A literal is itself; a name is whatever the resolver answers — the
// compose form registers a service name (`http://gateway-a:6060`), which is not
// an IP literal and never was, so the row it wrote has to be resolved before it
// can be compared. The direction is row → IP and never caller → IP: only the
// replicas' own rows are looked up, so a caller still cannot name its way in.
// A row that cannot be resolved is a row that does not match; it is logged and
// skipped, never allowed to fail the check for the others. The request's
// context bounds the lookup, so a hung resolver cannot outlive the request.
func replicaHostIPs(ctx context.Context, host string) []net.IP {
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}
	}
	answers, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		slog.Warn("gateway internal auth: resolve registered replica", "host", host, "err", err)
		return nil
	}
	out := make([]net.IP, 0, len(answers))
	for _, answer := range answers {
		if ip := net.ParseIP(strings.Trim(answer, "[]")); ip != nil {
			out = append(out, ip)
		}
	}
	return out
}

// refuseInternalRequest answers a peer request that failed authentication the
// way the control plane answers any other: 401 and nothing else.
func refuseInternalRequest(w http.ResponseWriter, r *http.Request) {
	slog.Warn("gateway internal endpoint refused a request", "path", r.URL.Path, "remote", r.RemoteAddr)
	writeControlPlaneUnauthorized(w, `{"error":"unauthorized"}`)
}

func ControlPlaneHTTPMiddleware(env *process.Environment, gw *Server, inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if env == nil || env.Deps.AppCfg == nil {
			inner.ServeHTTP(w, r)
			return
		}
		cfg := env.Deps.AppCfg
		path := strings.TrimSpace(r.URL.Path)
		// The exempt inbound paths are those of the agent's project channels,
		// which is the agent the runner is bound to.
		if appcfg.GatewayControlPlaneAuthExemptPath(path, controlPlaneChannelSections(env)) {
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
		origin, err := authorizeControlPlane(r, cfg)
		if err != nil {
			writeControlPlaneAuthError(w, r, err)
			return
		}
		if origin != "" {
			gw.noteWebOrigin(origin)
		}
		inner.ServeHTTP(w, r)
	})
}

// controlPlaneChannelSections reads the active agent's enabled project channels
// as the sections whose inbound webhook paths the middleware exempts from
// control-plane auth. A store error is not fatal: the request falls through to
// normal auth.
func controlPlaneChannelSections(env *process.Environment) []appcfg.ChannelsSection {
	if env == nil || env.SQL == nil {
		return nil
	}
	rows, err := state.NewProjectChannelStore(env.SQL, envAgentID(env)).ListEnabledForAgent(context.Background())
	if err != nil {
		return nil
	}
	out := make([]appcfg.ChannelsSection, 0, len(rows))
	for _, row := range rows {
		section, perr := appcfg.ParseChannelKindJSON(row.Kind, row.ConfigJSON)
		if perr == nil {
			out = append(out, section)
		}
	}
	return out
}

func ServeHTTPChain(env *process.Environment, gw *Server, inner http.Handler) http.Handler {
	rl := newIPRateLimiter(40, 120)
	// The channel dispatcher is built once and consults the registry per
	// request, so it always reflects the currently bound agent.
	withChannels := ControlPlaneHTTPMiddleware(env, gw, gw.channelHandler(inner))
	// The access log wraps the whole chain, limiter and websocket included, so
	// every request an operator could wonder about is reported.
	return telemetry.AccessLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The limiter bounds what can be sent without the gateway's
		// credentials: guesses at the token, the sign-in exchange, the login
		// page. A request that carries them is the owner's own page at work —
		// one load of it fans out into more asset and API requests than any
		// burst sized for strangers, and throttling the holder of the token
		// protects nothing the token does not already grant.
		if !requestAuthorized(env, r) && !rl.allow(telemetry.ClientIP(r)) {
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
		// Replica-to-replica traffic is mounted ahead of the control plane,
		// because it authenticates on its own terms (plan 213 §3.2): the shared
		// token plus a registered source address, never a browser cookie.
		switch r.URL.Path {
		case internalWSPath, internalRunOpPath:
			if gw == nil {
				http.NotFound(w, r)
				return
			}
			if !gw.internalRequestAuthorized(r) {
				refuseInternalRequest(w, r)
				return
			}
			if r.URL.Path == internalWSPath {
				gw.handleInternalChatWS(w, r)
			} else {
				gw.handleInternalRunOp(w, r)
			}
			return
		}
		withChannels.ServeHTTP(w, r)
	}))
}

// requestAuthorized reports whether r carries the gateway's credentials under
// the live configuration.
func requestAuthorized(env *process.Environment, r *http.Request) bool {
	if env == nil || env.Deps.AppCfg == nil {
		return false
	}
	_, err := authorizeControlPlane(r, env.Deps.AppCfg)
	return err == nil
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
	cfg := s.liveCfg() // liveCfg: runtime snapshot (plan 217) — must not be persistedConfig
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
		slog.Warn("gateway web sign-in rejected", "remote", telemetry.ClientIP(r))
		writeControlPlaneUnauthorized(w, `{"error":"invalid gateway token"}`)
		return
	}
	http.SetCookie(w, webSessionCookie(expected, requestIsHTTPS(r)))
	w.WriteHeader(http.StatusNoContent)
}

// handleWebSessionProbe answers whether the caller already holds a valid
// session: reaching it at all means the control-plane middleware let the
// request through, which is the whole answer now that token auth is the only
// authentication there is (plan 206).
func (s *Server) handleWebSessionProbe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"authenticated":true}`))
}
