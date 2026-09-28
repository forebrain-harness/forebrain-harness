// HTTP serving: the REST layer, router, static SPA, rate limiting, and warnings.
package gateway

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"golang.org/x/time/rate"
)

// Route wraps the config for a single route.
type Route struct {
	Name        string
	Method      string
	Pattern     string
	HandlerFunc http.HandlerFunc
}

// Routes is the fluent registration surface shared by RestServer and grouped
// route scopes.
type Routes interface {
	Handle(method, path string, hf http.HandlerFunc)
	Get(path string, hf http.HandlerFunc)
	Post(path string, hf http.HandlerFunc)
	Put(path string, hf http.HandlerFunc)
	Patch(path string, hf http.HandlerFunc)
	Delete(path string, hf http.HandlerFunc)
	Options(path string, hf http.HandlerFunc)
	Head(path string, hf http.HandlerFunc)
	Group(prefix string) *RouteGroup
	RouteAdder() func(method, path string, hf http.HandlerFunc)
}

// RestServer wraps an Router and the standard *http.Server. The
// embedded *http.Server exposes the Handler field that the gateway swaps out
// to install its own middleware chain.
type RestServer struct {
	router      *Router
	middlewares []func(http.Handler) http.Handler
	*http.Server
}

// NewRestServer creates a RestServer instance listening on addr. The router is
// installed as the server Handler; callers may override Server.Handler to wrap
// it.
func NewRestServer(addr string) *RestServer {
	if addr == "" {
		addr = "127.0.0.1:6060"
	}
	router := New()
	router.NotFound = http.HandlerFunc(http.NotFound)
	return &RestServer{
		router: router,
		Server: &http.Server{
			Addr: addr,
			// No write timeout: streaming endpoints (SSE, websockets) must
			// not be cut off mid-response.
			ReadHeaderTimeout: 60 * time.Second,
			IdleTimeout:       120 * time.Second,
			Handler:           router,
		},
	}
}

// Handle registers a route using the fluent Forebrain Harness REST API.
func (srv *RestServer) Handle(method, path string, hf http.HandlerFunc) {
	srv.AddRoute(Route{
		Name:        routeName(method, path),
		Method:      method,
		Pattern:     cleanRoutePath(path),
		HandlerFunc: hf,
	})
}

func (srv *RestServer) Get(path string, hf http.HandlerFunc) {
	srv.Handle(http.MethodGet, path, hf)
}

func (srv *RestServer) Post(path string, hf http.HandlerFunc) {
	srv.Handle(http.MethodPost, path, hf)
}

func (srv *RestServer) Put(path string, hf http.HandlerFunc) {
	srv.Handle(http.MethodPut, path, hf)
}

func (srv *RestServer) Patch(path string, hf http.HandlerFunc) {
	srv.Handle(http.MethodPatch, path, hf)
}

func (srv *RestServer) Delete(path string, hf http.HandlerFunc) {
	srv.Handle(http.MethodDelete, path, hf)
}

func (srv *RestServer) Options(path string, hf http.HandlerFunc) {
	srv.Handle(http.MethodOptions, path, hf)
}

func (srv *RestServer) Head(path string, hf http.HandlerFunc) {
	srv.Handle(http.MethodHead, path, hf)
}

// Group returns a route scope with a shared path prefix.
func (srv *RestServer) Group(prefix string) *RouteGroup {
	return &RouteGroup{srv: srv, prefix: cleanRoutePath(prefix)}
}

// RouteAdder returns the legacy channel route-adder shape while keeping route
// registration backed by the fluent API.
func (srv *RestServer) RouteAdder() func(method, path string, hf http.HandlerFunc) {
	return srv.Handle
}

// Use appends middlewares applied to every route, in registration order
// (first registered runs outermost).
func (srv *RestServer) Use(mwf ...func(http.Handler) http.Handler) {
	srv.middlewares = append(srv.middlewares, mwf...)
}

// AddRoute registers one or more routes, wrapping each handler with the
// middleware chain registered via Use.
func (srv *RestServer) AddRoute(routes ...Route) {
	for _, rt := range routes {
		var h http.Handler = rt.HandlerFunc
		for i := len(srv.middlewares) - 1; i >= 0; i-- {
			h = srv.middlewares[i](h)
		}
		srv.router.Handle(rt.Method, rt.Pattern, h)
	}
}

// RouteGroup registers routes under a shared path prefix.
type RouteGroup struct {
	srv    *RestServer
	prefix string
}

func (g *RouteGroup) Handle(method, path string, hf http.HandlerFunc) {
	if g == nil || g.srv == nil {
		return
	}
	g.srv.Handle(method, joinRoutePath(g.prefix, path), hf)
}

func (g *RouteGroup) Get(path string, hf http.HandlerFunc) {
	g.Handle(http.MethodGet, path, hf)
}

func (g *RouteGroup) Post(path string, hf http.HandlerFunc) {
	g.Handle(http.MethodPost, path, hf)
}

func (g *RouteGroup) Put(path string, hf http.HandlerFunc) {
	g.Handle(http.MethodPut, path, hf)
}

func (g *RouteGroup) Patch(path string, hf http.HandlerFunc) {
	g.Handle(http.MethodPatch, path, hf)
}

func (g *RouteGroup) Delete(path string, hf http.HandlerFunc) {
	g.Handle(http.MethodDelete, path, hf)
}

func (g *RouteGroup) Options(path string, hf http.HandlerFunc) {
	g.Handle(http.MethodOptions, path, hf)
}

func (g *RouteGroup) Head(path string, hf http.HandlerFunc) {
	g.Handle(http.MethodHead, path, hf)
}

func (g *RouteGroup) Group(prefix string) *RouteGroup {
	if g == nil {
		return &RouteGroup{prefix: cleanRoutePath(prefix)}
	}
	return &RouteGroup{srv: g.srv, prefix: joinRoutePath(g.prefix, prefix)}
}

func (g *RouteGroup) RouteAdder() func(method, path string, hf http.HandlerFunc) {
	return g.Handle
}

func routeName(method, path string) string {
	return method + " " + cleanRoutePath(path)
}

func cleanRoutePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "/" {
		return "/"
	}
	path = "/" + strings.Trim(path, "/")
	if strings.HasPrefix(strings.TrimSpace(path), "/*") {
		return path
	}
	return path
}

func joinRoutePath(prefix, path string) string {
	prefix = cleanRoutePath(prefix)
	path = cleanRoutePath(path)
	if prefix == "/" {
		return path
	}
	if path == "/" {
		return prefix
	}
	return prefix + path
}

// Run starts the HTTP server and blocks until an interrupt or terminate signal
// is received, then shuts down gracefully.
func (srv *RestServer) Run() {
	addr := srv.Addr
	if addr == "" {
		addr = "127.0.0.1:6060"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		panic(err)
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			panic(err)
		}
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// Param is a single URL parameter, consisting of a key and a value.
type Param struct {
	Key   string
	Value string
}

// Params is a Param-slice, as returned by the router. The slice is ordered,
// the first URL parameter is also the first slice value.
type Params []Param

// ByName returns the value of the first Param whose key matches the given name.
// If no matching Param is found, an empty string is returned.
func (ps Params) ByName(name string) string {
	for _, p := range ps {
		if p.Key == name {
			return p.Value
		}
	}
	return ""
}

type paramsKey struct{}

// ParamsKey is the request context key under which URL params are stored.
var ParamsKey = paramsKey{}

// ParamsFromContext pulls the URL parameters from a request context, or
// returns nil if none are present.
func ParamsFromContext(ctx context.Context) Params {
	p, _ := ctx.Value(ParamsKey).(Params)
	return p
}

type segment struct {
	literal  string // static segment text (when not a param)
	param    string // parameter name for ":name" or "*name"
	catchAll bool   // true for "*name"
}

type route struct {
	method  string
	segs    []segment
	handler http.Handler
}

// Router matches incoming requests by method and path. The zero value is not
// usable; create one with New.
type Router struct {
	routes []route

	// NotFound is called when no route matches. Defaults to http.NotFound.
	NotFound http.Handler
	// MethodNotAllowed is called when the path matches a route registered
	// under a different method.
	MethodNotAllowed http.Handler
}

// New returns a ready-to-use Router.
func New() *Router { return &Router{} }

func parsePattern(p string) []segment {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return nil
	}
	parts := strings.Split(p, "/")
	segs := make([]segment, 0, len(parts))
	for _, part := range parts {
		switch {
		case strings.HasPrefix(part, ":"):
			segs = append(segs, segment{param: part[1:]})
		case strings.HasPrefix(part, "*"):
			segs = append(segs, segment{param: part[1:], catchAll: true})
		default:
			segs = append(segs, segment{literal: part})
		}
	}
	return segs
}

// Handle registers a handler for the given method and path pattern.
func (r *Router) Handle(method, path string, h http.Handler) {
	r.routes = append(r.routes, route{method: method, segs: parsePattern(path), handler: h})
}

func splitPath(p string) []string {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// match attempts to match the route against the request path segments,
// returning the captured params on success.
func (rt *route) match(parts []string) (Params, bool) {
	var params Params
	for si, seg := range rt.segs {
		if seg.catchAll {
			params = append(params, Param{Key: seg.param, Value: "/" + strings.Join(parts[si:], "/")})
			return params, true
		}
		if si >= len(parts) {
			return nil, false
		}
		if seg.param != "" {
			if parts[si] == "" {
				return nil, false
			}
			params = append(params, Param{Key: seg.param, Value: parts[si]})
			continue
		}
		if seg.literal != parts[si] {
			return nil, false
		}
	}
	if len(rt.segs) != len(parts) {
		return nil, false
	}
	return params, true
}

// score ranks a matching route's specificity so the most specific route wins:
// static segments outrank params, which outrank catch-alls.
func score(rt *route) int {
	s := 0
	hasCatch := false
	for _, seg := range rt.segs {
		switch {
		case seg.catchAll:
			hasCatch = true
		case seg.param != "":
			s += 10
		default:
			s += 100
		}
	}
	if !hasCatch {
		s++ // prefer exact/specific matches over catch-alls of equal literal weight
	}
	return s
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	parts := splitPath(req.URL.Path)
	var best *route
	var bestParams Params
	bestScore := -1
	pathExistsOtherMethod := false
	for i := range r.routes {
		rt := &r.routes[i]
		params, ok := rt.match(parts)
		if !ok {
			continue
		}
		if rt.method != req.Method {
			pathExistsOtherMethod = true
			continue
		}
		if s := score(rt); s > bestScore {
			bestScore, best, bestParams = s, rt, params
		}
	}
	if best != nil {
		ctx := context.WithValue(req.Context(), ParamsKey, bestParams)
		best.handler.ServeHTTP(w, req.WithContext(ctx))
		return
	}
	if pathExistsOtherMethod {
		if r.MethodNotAllowed != nil {
			r.MethodNotAllowed.ServeHTTP(w, req)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte("405 method not allowed"))
		return
	}
	if r.NotFound != nil {
		r.NotFound.ServeHTTP(w, req)
		return
	}
	http.NotFound(w, req)
}

type getRoutes interface {
	Get(path string, h http.HandlerFunc)
}

// attachStaticUI registers the SPA file server. It resolves a single fs.FS to
// serve from: a disk directory (StaticDist) overrides the embedded build
// (StaticFS), so developers can hot-swap the UI without rebuilding the binary.
// When neither is available no static routes are registered.
func (s *Server) attachStaticUI(routes getRoutes) {
	fsys := s.resolveStaticFS()
	if fsys == nil {
		return
	}
	serve := func(w http.ResponseWriter, r *http.Request) {
		serveSPA(w, r, fsys)
	}
	routes.Get("/", serve)
	routes.Get("/*filepath", serve)
}

// resolveStaticFS returns the filesystem to serve the UI from, or nil.
func (s *Server) resolveStaticFS() fs.FS {
	if root := strings.TrimSpace(s.StaticDist); root != "" {
		if abs, err := filepath.Abs(root); err == nil {
			if st, err := os.Stat(abs); err == nil && st.IsDir() {
				return os.DirFS(abs)
			}
		}
		return nil
	}
	return s.StaticFS
}

// serveSPA serves a static file from fsys when it exists, otherwise falls back
// to index.html for client-side routing. API and websocket paths never fall
// back so they keep returning 404 when unmatched.
func serveSPA(w http.ResponseWriter, r *http.Request, fsys fs.FS) {
	name := cleanFSPath(r.URL.Path)
	if name != "" {
		if f, err := fsys.Open(name); err == nil {
			st, statErr := f.Stat()
			_ = f.Close()
			if statErr == nil && !st.IsDir() {
				http.ServeFileFS(w, r, fsys, name)
				return
			}
		}
	}
	if strings.HasPrefix(r.URL.Path, "/api") || strings.HasPrefix(r.URL.Path, "/ws") {
		http.NotFound(w, r)
		return
	}
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(index)
}

// cleanFSPath converts a request path to an fs.FS path ("api/x"), rejecting
// traversal and empty/root requests (which return "" so callers fall back).
func cleanFSPath(urlPath string) string {
	p := path.Clean("/" + strings.TrimPrefix(urlPath, "/"))
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "." || !fs.ValidPath(p) {
		return ""
	}
	return p
}

type ipRateLimiter struct {
	mu      sync.Mutex
	lim     map[string]*rate.Limiter
	burst   int
	every   rate.Limit
	ttl     time.Duration
	lastHit map[string]time.Time
}

func newIPRateLimiter(rps float64, burst int) *ipRateLimiter {
	if rps <= 0 {
		rps = 40
	}
	if burst <= 0 {
		burst = 80
	}
	return &ipRateLimiter{
		lim:     map[string]*rate.Limiter{},
		burst:   burst,
		every:   rate.Limit(rps),
		lastHit: map[string]time.Time{},
		ttl:     10 * time.Minute,
	}
}

func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func (b *ipRateLimiter) allow(ip string) bool {
	if b == nil {
		return true
	}
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lim == nil {
		b.lim = map[string]*rate.Limiter{}
	}
	if b.lastHit == nil {
		b.lastHit = map[string]time.Time{}
	}
	for k, t := range b.lastHit {
		if now.Sub(t) > b.ttl {
			delete(b.lastHit, k)
			delete(b.lim, k)
		}
	}
	lim, ok := b.lim[ip]
	if !ok {
		lim = rate.NewLimiter(b.every, b.burst)
		b.lim[ip] = lim
	}
	b.lastHit[ip] = now
	return lim.Allow()
}

func gatewayControlPlaneAuthToken(rt process.Context) string {
	return strings.TrimSpace(rt.Config.Gateway.Auth.Token)
}

func GatewayHealthText(ctx context.Context, w io.Writer) error {
	rt, err := process.Resolve()
	if err != nil {
		return err
	}
	addr := strings.TrimSpace(rt.Config.Gateway.HTTPAddr)
	if addr == "" {
		addr = "127.0.0.1:6060"
	}
	url := "http://" + addr + "/healthz"
	client := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if tok := gatewayControlPlaneAuthToken(rt); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Info("gateway health probe skipped", "addr", addr, "err", err)
		_, _ = fmt.Fprintf(w, "gateway=not_running (optional for TUI) addr=%s err=%v\n", addr, err)
		return nil
	}
	defer resp.Body.Close()
	_, _ = fmt.Fprintf(w, "gateway=up addr=%s status=%d\n", addr, resp.StatusCode)
	return nil
}

func GatewayShutdownRequest(ctx context.Context, w io.Writer) error {
	rt, err := process.Resolve()
	if err != nil {
		return err
	}
	addr := strings.TrimSpace(rt.Config.Gateway.HTTPAddr)
	if addr == "" {
		addr = "127.0.0.1:6060"
	}
	url := "http://" + addr + "/admin/shutdown"
	client := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if tok := gatewayControlPlaneAuthToken(rt); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Info("gateway shutdown skipped", "addr", addr, "err", err)
		_, _ = fmt.Fprintf(w, "gateway stop skipped (not listening) addr=%s err=%v\n", addr, err)
		return nil
	}
	defer resp.Body.Close()
	_, _ = fmt.Fprintf(w, "gateway stop requested addr=%s status=%d\n", addr, resp.StatusCode)
	return nil
}

func (s *Server) runtimeDangerWarning() (string, map[string]any, bool) {
	if s == nil || s.Runner == nil || !safety.RuntimeYOLOEnabled() {
		return "", nil, false
	}
	return "YOLO / danger-full-access is enabled; approvals are bypassed and executable tools run on the host.", map[string]any{
		"yolo":                      true,
		"sandbox_mode":              "danger-full-access",
		"approval_bypassed_by_yolo": true,
	}, true
}
