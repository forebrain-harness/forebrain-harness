package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Session is one MCP client session. Its connection is mutable: CallToolJSON
// reconnects an expired session in place. Every field that a reconnect
// replaces is guarded by mu, and callers take the live values in one step
// instead of reading fields they may race with.
type Session struct {
	mu         sync.RWMutex
	client     *mcp.Client
	session    *mcp.ClientSession
	home       string
	serverName string
	// stateRoot is the per-agent state root (NOT home) that output-compression
	// accounting joins "state" onto. It must match the root the shell tools
	// resolve, or MCP records would land in a different history database and
	// retrieve_output could never find their ids. Empty disables accounting.
	stateRoot string
	reconnect func(context.Context) (*Session, error)
	// baseCtx is the session's own lifetime, derived from the context the
	// process was started with. A reconnect rebuilds its transport under this
	// context rather than under the tool call that discovered the expiry: the
	// child process a reconnect starts outlives the single call, and tying it
	// to that call's cancellation killed a healthy server the moment the caller
	// gave up waiting.
	baseCtx context.Context
	// reconnectTimeout bounds one reconnect's handshake, so a server that
	// answers nothing cannot hold the tool call forever.
	reconnectTimeout time.Duration
	// reconnectMu serializes connection replacement. reconnectSession and
	// Release are the two writers that swap the connection wholesale, and each
	// finishes before the other starts — a reconnect interleaved with a
	// release could otherwise adopt a connection built from a session whose
	// lifetime was being torn down at that moment. Ordinary concurrent
	// reconnects need no early exit here: install displaces the loser's
	// connection and closes it, which is the same outcome, arrived at safely.
	// It is always taken before mu, never after, which is the one ordering
	// rule the whole arrangement needs.
	reconnectMu sync.Mutex
	// proc owns the stdio child process, when this session has one. HTTP and
	// SSE sessions leave it nil: there is no local process to reclaim.
	proc *stdioProcess
	// closed makes Close idempotent and stops a reconnect from installing a
	// connection into a session nobody is watching.
	closed bool
}

// SetStateRoot points output-compression accounting at the per-agent state root.
// Callers pass the same root every other per-agent seam uses (Runner.StateRoot),
// not home.
func (s *Session) SetStateRoot(stateRoot string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stateRoot = strings.TrimSpace(stateRoot)
	s.mu.Unlock()
}

// liveSession returns the current connection. Nil when the session was never
// connected or has been closed.
func (s *Session) liveSession() *mcp.ClientSession {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.session
}

// requireSession returns the live connection, or an error when the session was
// never connected (or has been closed).
func (s *Session) requireSession() (*mcp.ClientSession, error) {
	sess := s.liveSession()
	if sess == nil {
		return nil, fmt.Errorf("nil session")
	}
	return sess, nil
}

// ensureSession returns the live connection, rebuilding it first when this
// session has none but can grow one.
//
// A released session (see Release) is exactly that: its connection was torn
// down because the Runner that owns it went idle, and the session object stayed
// behind as the handle that knows how to bring the connection back. Every
// request path goes through here rather than requireSession, so "the server was
// idle long enough to lose its child process" costs one bounded reconnect on
// the first request and nothing after — the callers do not have to know that
// the connection is a cache.
func (s *Session) ensureSession() (*mcp.ClientSession, error) {
	if sess := s.liveSession(); sess != nil {
		return sess, nil
	}
	if s == nil || s.isClosed() || s.reconnectFactory() == nil {
		return nil, fmt.Errorf("nil session")
	}
	if err := s.reconnectSession(); err != nil {
		return nil, err
	}
	if sess := s.liveSession(); sess != nil {
		return sess, nil
	}
	return nil, fmt.Errorf("nil session")
}

func (s *Session) isClosed() bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}

// accounting returns the immutable half of a session: the target the response
// compression reports against, and the identity the record carries.
func (s *Session) accounting() (stateRoot, home, serverName string) {
	if s == nil {
		return "", "", ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stateRoot, s.home, s.serverName
}

// Close shuts the session down gracefully and then releases the process it owns.
//
// The order matters and is the spec's: the SDK closes stdin, waits for the
// child to exit, and escalates to SIGTERM and SIGKILL within its own bound. Only
// once that has been attempted is the process context cancelled, which is the
// backstop that guarantees the child dies even if the SDK's sequence returned an
// error early. Cancelling first would skip the graceful path entirely, and
// closing without ever cancelling would leave the child's context armed.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	sess := s.session
	s.session = nil
	s.client = nil
	proc := s.proc
	s.proc = nil
	s.mu.Unlock()
	err := closeClientSession(sess)
	if proc != nil {
		proc.stop()
	}
	return err
}

// Abort tears the session down along the fast path: the child process is
// signalled first, so the SDK's graceful wait cannot spend its terminate
// duration on a server that is already known to be unusable. Everything still
// goes through Close, which is what reaps the process — killing without closing
// leaves a zombie behind.
func (s *Session) Abort() error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	proc := s.proc
	s.mu.RUnlock()
	if proc != nil {
		proc.stop()
	}
	if err := s.Close(); !reapedAfterStop(err) {
		return err
	}
	return nil
}

// Release tears this session's connection down — for a stdio server, its child
// process — without ending the session.
//
// It is what an idle Runner calls to give back the resources a server holds
// while nobody is talking to it. Close is the wrong tool for that: it makes the
// session permanent garbage, and the tool table a long-lived session was
// built on still names this server, so the calls would fail forever. A released
// session instead keeps everything that identifies it and knows how to rebuild
// its connection (the factory, the timeout, the accounting target) and drops
// only the connection; ensureSession grows a new one on the next request.
//
// The process is signalled before the connection is closed, mirroring Abort's
// fast path: the close then reaps a child that is already dying instead of
// spending its terminate duration on one that is merely quiet. baseCtx is
// replaced with a live context for the same reason it is swapped in install —
// the old one is the lifetime of the child being torn down here, and a
// reconnect built on a cancelled context would die before it reached the
// server.
func (s *Session) Release() error {
	if s == nil {
		return nil
	}
	s.reconnectMu.Lock()
	defer s.reconnectMu.Unlock()
	s.mu.Lock()
	if s.closed || s.session == nil {
		s.mu.Unlock()
		return nil
	}
	sess := s.session
	proc := s.proc
	s.session = nil
	s.client = nil
	s.proc = nil
	s.baseCtx = context.Background()
	s.mu.Unlock()
	if proc != nil {
		proc.stop()
	}
	if err := closeClientSession(sess); !reapedAfterStop(err) {
		return err
	}
	return nil
}

// reapedAfterStop reports whether closing a session whose child was stopped
// first ended in the child being reaped. The close then reports how the child
// exited, never a failure to close: an *exec.ExitError when the stop's signal
// ended it, or the stop's own context.Canceled when it exited successfully
// after being told to stop, which is what exec.Cmd.Wait reports in that case.
func reapedAfterStop(err error) bool {
	var exited *exec.ExitError
	return err == nil || errors.As(err, &exited) || errors.Is(err, context.Canceled)
}

func closeClientSession(sess *mcp.ClientSession) error {
	if sess == nil {
		return nil
	}
	return sess.Close()
}

// stdioProcess owns one MCP stdio child process for the lifetime of a Session.
type stdioProcess struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	mu     sync.Mutex
	conn   *processConn
	// done closes when the process context is released, which is what stops the
	// caller watcher below from outliving the process it watches.
	done     chan struct{}
	doneOnce sync.Once
	// handshake closes once the connection is established. The caller watcher
	// only guards the handshake: a server that connected must outlive the call
	// that started it, however that call ends.
	handshake     chan struct{}
	handshakeOnce sync.Once
	// pid is the child's process id, captured once the handshake has settled.
	// Reading cmd.Process directly would race with the SDK starting the command,
	// so the value is taken at the one moment it is known to be final and read
	// through pidOf.
	pid int
}

// attach records the wrapped connection this process produced, so the caller
// can tear it down even when the SDK returns an error without closing it.
func (p *stdioProcess) attach(conn *processConn) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.conn = conn
	p.mu.Unlock()
}

func (p *stdioProcess) connection() *processConn {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conn
}

// stop ends the child's process context. The command was built with
// exec.CommandContext, so cancelling is what signals the child — and doing it
// this way rather than calling Process.Kill() is deliberate: the exec package
// installs the signal in a goroutine that runs after Start, so reading
// cmd.Process here would race with the SDK starting the command, while
// cancelling a context does not.
//
// It is idempotent, and it also lets the process runtime's watcher goroutine
// exit instead of waiting on a context that will never be cancelled.
func (p *stdioProcess) stop() {
	if p == nil {
		return
	}
	p.doneOnce.Do(func() {
		if p.done != nil {
			close(p.done)
		}
	})
	if p.cancel != nil {
		p.cancel()
	}
}

// watchCaller signals the child while the handshake is in flight and the
// caller's context is done.
//
// The child's lifetime is deliberately not the caller's — a connected server
// outlives the handshake that started it, and that is what the handshake
// channel marks — but a caller that *gave up* (an expired startup deadline, an
// abandoned generation) is a different case: the connection it was waiting for
// is not wanted, and the SDK's close sequence would otherwise spend its full
// terminate duration waiting on a server that is, by hypothesis, not answering.
// Killing first makes that sequence return immediately, and the close still does
// the reaping.
//
// It exits when the handshake settles or the process is released, so it cannot
// outlive either.
func (p *stdioProcess) watchCaller(ctx context.Context) {
	if p == nil || ctx == nil {
		return
	}
	select {
	case <-ctx.Done():
		p.stop()
	case <-p.handshake:
	case <-p.done:
	}
}

// handshakeSettled stops the caller watcher: from here on the connection owns
// its own lifetime. It also records the child's pid, which is the first moment
// the value is final.
func (p *stdioProcess) handshakeSettled() {
	if p == nil {
		return
	}
	p.handshakeOnce.Do(func() {
		p.mu.Lock()
		if p.cmd != nil && p.cmd.Process != nil {
			p.pid = p.cmd.Process.Pid
		}
		p.mu.Unlock()
		if p.handshake != nil {
			close(p.handshake)
		}
	})
}

// pidOf reports the child's process id, or 0 before the handshake settled.
func (p *stdioProcess) pidOf() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pid
}

// abort ends the child and closes the connection, in that order: the SDK's close
// sequence then finds a process that is already being signalled and returns
// immediately instead of spending its terminate duration, while its Wait call
// still reaps.
func (p *stdioProcess) abort() {
	if p == nil {
		return
	}
	p.stop()
	if conn := p.connection(); conn != nil {
		_ = conn.Close()
		return
	}
	// No connection was ever established, so nothing else will release it.
	p.stop()
}

// stdioTransport delegates to the SDK's command transport and keeps the
// connection it produced reachable from the process owner.
//
// The delegation exists for one case the SDK leaves open: Client.Connect
// returns an error without closing the connection when the server answers with
// an unsupported protocol version, so the child process and its pipes would
// stay alive with no handle to them. Owning the connection means Start can tear
// it down on every error path.
type stdioTransport struct {
	proc  *stdioProcess
	inner mcp.Transport
}

func (t *stdioTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	wrapped := &processConn{Connection: conn, proc: t.proc}
	t.proc.attach(wrapped)
	return wrapped, nil
}

// processConn is the connection of one stdio child. Close is the single place
// the process context is released, so every path that closes the connection
// also disarms the process watcher.
type processConn struct {
	mcp.Connection
	proc *stdioProcess
}

func (c *processConn) Close() error {
	err := c.Connection.Close()
	c.proc.stop()
	return err
}

// uriFromPath converts a file path to a file:// URI.
func uriFromPath(path string) string {
	if path == "" {
		return ""
	}
	if !isWindowsDrivePath(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	// Check the file path again, in case it became absolute.
	if isWindowsDrivePath(path) {
		path = "/" + strings.ToUpper(string(path[0])) + path[1:]
	}
	path = filepath.ToSlash(path)
	u := url.URL{
		Scheme: "file",
		Path:   path,
	}
	return u.String()
}

// isWindowsDrivePath returns true if the file path is of the form used by
// Windows (e.g., C:/x/y/z).
func isWindowsDrivePath(path string) bool {
	if len(path) < 3 {
		return false
	}
	return unicode.IsLetter(rune(path[0])) && path[1] == ':'
}

func Connect(ctx context.Context, tr mcp.Transport, workspaceRoots []string) (*Session, error) {
	cl := mcp.NewClient(&mcp.Implementation{Name: "forebrain", Version: "0.1.0"}, nil)
	cl.AddRoots(mcpRootsFromPaths(workspaceRoots)...)
	sess, err := cl.Connect(ctx, tr, nil)
	if err != nil {
		return nil, err
	}
	return &Session{client: cl, session: sess}, nil
}

func Start(ctx context.Context, home string, workspaceDir string, srv appcfg.MCPServerConfig) (*Session, error) {
	if strings.TrimSpace(home) != "" {
		srv = MergeMCPServerOAuth(home, srv)
	}
	nm := strings.TrimSpace(srv.Name)
	if nm == "" {
		return nil, fmt.Errorf("mcp server name empty")
	}
	projectKey := OverlayProjectKey(srv)
	lookup := envLookupForServer(home, srv)
	switch normalizeTransport(srv) {
	case "stdio":
		cmd := strings.TrimSpace(srv.Command)
		if cmd == "" {
			return nil, fmt.Errorf("mcp %q: stdio transport requires command", nm)
		}
		if err := validateMCPStdioCommand(cmd); err != nil {
			return nil, fmt.Errorf("mcp %q: %w", nm, err)
		}
		if err := verifyMCPCommandSHA256(cmd, srv.ExpectedCommandSHA256); err != nil {
			return nil, fmt.Errorf("mcp %q: %w", nm, err)
		}
		// The child's lifetime is its own, not the caller's: the process
		// context is derived from a context that ignores cancellation, so a
		// startup deadline (or an abandoning caller) never kills a server that
		// is already connected. Cancelling it is how the child is reclaimed.
		procCtx, cancelProc := context.WithCancel(context.WithoutCancel(ctx))
		c := exec.CommandContext(procCtx, cmd, srv.Args...)
		if wd := strings.TrimSpace(workspaceDir); wd != "" {
			c.Dir = wd
		}
		c.Env = mcpStdioEnv(home, srv)
		proc := &stdioProcess{
			cmd:       c,
			cancel:    cancelProc,
			done:      make(chan struct{}),
			handshake: make(chan struct{}),
		}
		go proc.watchCaller(ctx)
		sess, err := Connect(ctx, &stdioTransport{proc: proc, inner: &mcp.CommandTransport{Command: c}}, workspaceRootPaths(workspaceDir))
		if err != nil {
			// Every error path reclaims the child, including the one the SDK
			// returns without closing the connection it established.
			proc.abort()
			return nil, err
		}
		proc.handshakeSettled()
		sess.home = strings.TrimSpace(home)
		sess.serverName = nm
		sess.proc = proc
		sess.baseCtx = procCtx
		sess.reconnectTimeout = StartupTimeoutFor(srv)
		sess.reconnect = func(ctx context.Context) (*Session, error) { return Start(ctx, home, workspaceDir, srv) }
		return sess, nil
	case "streamable_http":
		u, missing := ExpandEnvVarsInString(strings.TrimSpace(srv.URL), lookup)
		if len(missing) > 0 {
			return nil, fmt.Errorf("mcp %q: missing environment variables: %s", nm, strings.Join(missing, ", "))
		}
		if u == "" {
			return nil, fmt.Errorf("mcp %q: streamable_http requires url", nm)
		}
		tr := &mcp.StreamableClientTransport{Endpoint: u}
		if oauthConfigured(srv.OAuth) {
			oh, err := streamableOAuthHandlerForServer(ctx, home, projectKey, nm, srv.OAuth)
			if err != nil {
				return nil, fmt.Errorf("mcp %q oauth: %w", nm, err)
			}
			tr.OAuthHandler = oh
		}
		headers, missing := ExpandEnvMap(srv.Headers, lookup)
		if len(missing) > 0 {
			return nil, fmt.Errorf("mcp %q: missing header environment variables: %s", nm, strings.Join(missing, ", "))
		}
		if len(headers) > 0 {
			tr.HTTPClient = httpClientWithHeaders(headers)
		}
		if srv.DisableStandaloneSSE != nil && *srv.DisableStandaloneSSE {
			tr.DisableStandaloneSSE = true
		}
		sess, err := Connect(ctx, tr, workspaceRootPaths(workspaceDir))
		if err != nil {
			return nil, err
		}
		sess.home = strings.TrimSpace(home)
		sess.serverName = nm
		// No local process to own here, but a reconnect still needs a lifetime
		// that outlives the call that discovered the expiry.
		sess.baseCtx = context.WithoutCancel(ctx)
		sess.reconnectTimeout = StartupTimeoutFor(srv)
		sess.reconnect = func(ctx context.Context) (*Session, error) { return Start(ctx, home, workspaceDir, srv) }
		return sess, nil
	case "sse":
		u, missing := ExpandEnvVarsInString(strings.TrimSpace(srv.URL), lookup)
		if len(missing) > 0 {
			return nil, fmt.Errorf("mcp %q: missing environment variables: %s", nm, strings.Join(missing, ", "))
		}
		if u == "" {
			return nil, fmt.Errorf("mcp %q: sse requires url", nm)
		}
		headers, missing := ExpandEnvMap(srv.Headers, lookup)
		if len(missing) > 0 {
			return nil, fmt.Errorf("mcp %q: missing header environment variables: %s", nm, strings.Join(missing, ", "))
		}
		hc, err := sseHTTPClientForServer(ctx, home, projectKey, nm, headers, srv.OAuth)
		if err != nil {
			return nil, fmt.Errorf("mcp %q: %w", nm, err)
		}
		tr := &mcp.SSEClientTransport{Endpoint: u, HTTPClient: hc}
		sess, err := Connect(ctx, tr, workspaceRootPaths(workspaceDir))
		if err != nil {
			return nil, err
		}
		sess.home = strings.TrimSpace(home)
		sess.serverName = nm
		// No local process to own here, but a reconnect still needs a lifetime
		// that outlives the call that discovered the expiry.
		sess.baseCtx = context.WithoutCancel(ctx)
		sess.reconnectTimeout = StartupTimeoutFor(srv)
		sess.reconnect = func(ctx context.Context) (*Session, error) { return Start(ctx, home, workspaceDir, srv) }
		return sess, nil
	case "websocket":
		u, missing := ExpandEnvVarsInString(strings.TrimSpace(srv.URL), lookup)
		if len(missing) > 0 {
			return nil, fmt.Errorf("mcp %q: missing environment variables: %s", nm, strings.Join(missing, ", "))
		}
		if u == "" {
			return nil, fmt.Errorf("mcp %q: websocket requires url", nm)
		}
		headers, missing := ExpandEnvMap(srv.Headers, lookup)
		if len(missing) > 0 {
			return nil, fmt.Errorf("mcp %q: missing header environment variables: %s", nm, strings.Join(missing, ", "))
		}
		hdr, err := websocketHeaderForServer(ctx, home, projectKey, nm, headers, srv.OAuth)
		if err != nil {
			return nil, fmt.Errorf("mcp %q: %w", nm, err)
		}
		sess, err := Connect(ctx, &WebSocketTransport{Endpoint: u, Header: hdr}, workspaceRootPaths(workspaceDir))
		if err != nil {
			return nil, err
		}
		sess.home = strings.TrimSpace(home)
		sess.serverName = nm
		// No local process to own here, but a reconnect still needs a lifetime
		// that outlives the call that discovered the expiry.
		sess.baseCtx = context.WithoutCancel(ctx)
		sess.reconnectTimeout = StartupTimeoutFor(srv)
		sess.reconnect = func(ctx context.Context) (*Session, error) { return Start(ctx, home, workspaceDir, srv) }
		return sess, nil
	default:
		return nil, fmt.Errorf("mcp %q: unknown transport %q", nm, strings.TrimSpace(srv.Transport))
	}
}

func workspaceRootPaths(workspaceDir string) []string {
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir == "" {
		return nil
	}
	abs, err := filepath.Abs(workspaceDir)
	if err == nil {
		workspaceDir = abs
	}
	return []string{filepath.Clean(workspaceDir)}
}

func mcpRootsFromPaths(paths []string) []*mcp.Root {
	if len(paths) == 0 {
		return nil
	}
	out := make([]*mcp.Root, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		uri := uriFromPath(p)
		if strings.TrimSpace(uri) == "" {
			continue
		}
		if _, ok := seen[uri]; ok {
			continue
		}
		seen[uri] = struct{}{}
		out = append(out, &mcp.Root{
			Name: filepath.Base(filepath.Clean(p)),
			URI:  uri,
		})
	}
	return out
}

func websocketHeaderForServer(ctx context.Context, home, projectKey, serverName string, headers map[string]string, oauth appcfg.MCPOAuthConfig) (http.Header, error) {
	hdr := http.Header{}
	for k, v := range headers {
		hdr.Set(k, v)
	}
	if !oauthConfigured(oauth) {
		return hdr, nil
	}
	src, _, err := oauthTokenSourceForServer(ctx, home, projectKey, serverName, oauth)
	if err != nil {
		return nil, err
	}
	if src == nil {
		return hdr, nil
	}
	tok, err := src.Token()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	if err != nil {
		return nil, err
	}
	tok.SetAuthHeader(req)
	if v := strings.TrimSpace(req.Header.Get("Authorization")); v != "" {
		hdr.Set("Authorization", v)
	}
	return hdr, nil
}

func normalizeTransport(srv appcfg.MCPServerConfig) string {
	t := strings.ToLower(strings.TrimSpace(srv.Transport))
	switch t {
	case "", "default", "auto":
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(srv.URL)), "ws://") ||
			strings.HasPrefix(strings.ToLower(strings.TrimSpace(srv.URL)), "wss://") {
			return "websocket"
		}
		if strings.TrimSpace(srv.URL) != "" {
			return "streamable_http"
		}
		return "stdio"
	case "stdio", "command", "subprocess":
		return "stdio"
	case "streamable_http", "streamable-http", "http", "https":
		return "streamable_http"
	case "sse", "eventsource":
		return "sse"
	case "websocket", "web-socket", "ws", "wss":
		return "websocket"
	default:
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(srv.URL)), "ws://") ||
			strings.HasPrefix(strings.ToLower(strings.TrimSpace(srv.URL)), "wss://") {
			return "websocket"
		}
		return t
	}
}

func httpClientWithHeaders(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return nil
	}
	var base http.RoundTripper = http.DefaultTransport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		base = dt.Clone()
	}
	return &http.Client{Transport: &headerRoundTripper{next: base, headers: headers}}
}

type headerRoundTripper struct {
	next    http.RoundTripper
	headers map[string]string
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	return h.next.RoundTrip(req)
}

func (s *Session) ListToolMetas(ctx context.Context) ([]map[string]any, error) {
	sess, err := s.ensureSession()
	if err != nil {
		return nil, err
	}
	var all []*mcp.Tool
	cur := ""
	for i := 0; i < 256; i++ {
		res, err := sess.ListTools(ctx, &mcp.ListToolsParams{Cursor: cur})
		if err != nil {
			return nil, err
		}
		all = append(all, res.Tools...)
		if res.NextCursor == "" {
			break
		}
		cur = res.NextCursor
	}
	out := make([]map[string]any, 0, len(all))
	for _, t := range all {
		if t == nil || strings.TrimSpace(t.Name) == "" {
			continue
		}
		b, err := json.Marshal(t)
		if err != nil {
			continue
		}
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		if desc, _ := m["description"].(string); desc != "" {
			m["description"] = CapDescription(desc)
		}
		out = append(out, m)
	}
	return out, nil
}

func (s *Session) InitResult() *mcp.InitializeResult {
	sess := s.liveSession()
	if sess == nil {
		return nil
	}
	return sess.InitializeResult()
}

// ServerCapabilities is the part of a server's initialize result that decides
// which derived tools exist. It is a narrow projection rather than the SDK's
// message so a caller outside this package can make that decision without
// depending on the SDK, and so it can be cached alongside a segment.
type ServerCapabilities struct {
	Prompts   bool `json:"prompts,omitempty"`
	Resources bool `json:"resources,omitempty"`
}

// Capabilities reports the optional capability groups this server advertised.
func (s *Session) Capabilities() ServerCapabilities {
	init := s.InitResult()
	if init == nil || init.Capabilities == nil {
		return ServerCapabilities{}
	}
	return ServerCapabilities{
		Prompts:   init.Capabilities.Prompts != nil,
		Resources: init.Capabilities.Resources != nil,
	}
}

func (s *Session) ListPromptsJSON(ctx context.Context) (string, error) {
	sess, err := s.ensureSession()
	if err != nil {
		return "", err
	}
	var prompts []*mcp.Prompt
	cur := ""
	for i := 0; i < 256; i++ {
		res, err := sess.ListPrompts(ctx, &mcp.ListPromptsParams{Cursor: cur})
		if err != nil {
			return "", err
		}
		prompts = append(prompts, res.Prompts...)
		if res.NextCursor == "" {
			break
		}
		cur = res.NextCursor
	}
	b, err := json.Marshal(map[string]any{"prompts": prompts})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (s *Session) GetPromptJSON(ctx context.Context, name string, arguments map[string]string) (string, error) {
	sess, err := s.ensureSession()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("prompt name empty")
	}
	res, err := sess.GetPrompt(ctx, &mcp.GetPromptParams{Name: name, Arguments: arguments})
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// listAllResources pages through a server's resources/list.
func (s *Session) listAllResources(ctx context.Context) ([]*mcp.Resource, error) {
	sess, err := s.ensureSession()
	if err != nil {
		return nil, err
	}
	var resources []*mcp.Resource
	cur := ""
	for i := 0; i < 256; i++ {
		res, err := sess.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cur})
		if err != nil {
			return nil, err
		}
		resources = append(resources, res.Resources...)
		if res.NextCursor == "" {
			break
		}
		cur = res.NextCursor
	}
	return resources, nil
}

// ResourceInfo is one resource a server advertises, as a person reads the
// list: its name and its URI.
type ResourceInfo struct {
	Name string
	URI  string
}

// ResourceInfos lists the resources the server advertises, in its order.
func (s *Session) ResourceInfos(ctx context.Context) ([]ResourceInfo, error) {
	resources, err := s.listAllResources(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ResourceInfo, 0, len(resources))
	for _, r := range resources {
		if r == nil {
			continue
		}
		out = append(out, ResourceInfo{Name: strings.TrimSpace(r.Name), URI: strings.TrimSpace(r.URI)})
	}
	return out, nil
}

func (s *Session) ListResourcesJSON(ctx context.Context) (string, error) {
	resources, err := s.listAllResources(ctx)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(map[string]any{"resources": resources})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (s *Session) ReadResourceJSON(ctx context.Context, uri string) (string, error) {
	sess, err := s.ensureSession()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(uri) == "" {
		return "", fmt.Errorf("resource uri empty")
	}
	res, err := sess.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (s *Session) ListResourceTemplatesJSON(ctx context.Context) (string, error) {
	sess, err := s.ensureSession()
	if err != nil {
		return "", err
	}
	var templates []*mcp.ResourceTemplate
	cur := ""
	for i := 0; i < 256; i++ {
		res, err := sess.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{Cursor: cur})
		if err != nil {
			return "", err
		}
		templates = append(templates, res.ResourceTemplates...)
		if res.NextCursor == "" {
			break
		}
		cur = res.NextCursor
	}
	b, err := json.Marshal(map[string]any{"resourceTemplates": templates})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (s *Session) CallToolJSON(ctx context.Context, toolName string, arguments json.RawMessage) (string, error) {
	var args any = map[string]any{}
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", err
		}
	}
	var reconnect func() error
	if s.reconnectFactory() != nil {
		reconnect = func() error { return s.reconnectSession() }
	}
	return RetryAfterSessionExpired(
		func() (string, error) { return s.callToolAttempt(ctx, toolName, args) },
		reconnect,
	)
}

// callToolAttempt performs one call against whatever connection this session
// holds right now.
//
// Resolving the connection per attempt is the whole point: the retry above runs
// after a reconnect replaced it, and an attempt bound to the connection the
// first try used would send the retry down the socket that just expired — which
// is exactly the failure the reconnect exists to recover from.
func (s *Session) callToolAttempt(ctx context.Context, toolName string, args any) (string, error) {
	sess, err := s.ensureSession()
	if err != nil {
		return "", err
	}
	stateRoot, home, serverName := s.accounting()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: args})
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	out, err := processToolResultJSON(home, serverName, toolName, string(b), accounting{
		stateRoot: stateRoot,
		sessionID: llm.AgentSessionIDFromContext(ctx),
	})
	if err != nil {
		return "", err
	}
	if res != nil && res.IsError {
		return out, fmt.Errorf("%s", out)
	}
	return out, nil
}

func (s *Session) reconnectFactory() func(context.Context) (*Session, error) {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reconnect
}

// reconnectSession replaces this session's connection with a fresh one.
//
// Three things about it are deliberate. It rebuilds unconditionally: the
// caller is the authority on why — an expired session reports its expiry
// through an error while its ClientSession object is still held, so "a
// connection is present" cannot mean "a rebuild is unnecessary", and a
// concurrent caller's rebuild is simply displaced and closed by install. The
// rebuild is serialized under reconnectMu, so it cannot interleave with a
// Release: the two are the only writers that replace the connection wholesale,
// and each finishes before the other starts. And the rebuild runs on the
// session's own lifetime rather than on the calling tool call's context: the
// process the reconnect starts has to outlive the call that discovered the
// expiry.
func (s *Session) reconnectSession() error {
	s.reconnectMu.Lock()
	defer s.reconnectMu.Unlock()
	factory := s.reconnectFactory()
	if factory == nil {
		return fmt.Errorf("mcp session %s: no reconnect hook", s.label())
	}
	s.mu.RLock()
	parent := s.baseCtx
	timeout := s.reconnectTimeout
	s.mu.RUnlock()
	if parent == nil {
		parent = context.Background()
	}
	if timeout <= 0 {
		timeout = DefaultStartupTimeout
	}
	handshakeCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	fresh, err := factory(handshakeCtx)
	if err != nil {
		return err
	}
	if fresh == nil {
		return fmt.Errorf("mcp reconnect returned nil session")
	}
	if err := s.install(fresh); err != nil {
		_ = fresh.Close()
		return err
	}
	return nil
}

// install swaps a rebuilt connection into this session. The displaced
// connection is closed after the swap, never before: closing it first would
// leave a window where this session has no live connection at all, and the
// process it owns is signalled before that, so the close of a stdio server
// cannot spend its terminate duration under the lock.
func (s *Session) install(fresh *Session) error {
	if s == nil || fresh == nil {
		return fmt.Errorf("mcp reconnect returned nil session")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("mcp session %s: closed", s.serverName)
	}
	oldSession, oldProc := s.session, s.proc
	// The reconnect factory rebuilds the transport, not the accounting target,
	// so carry the state root across or compression records would silently stop
	// after the first reconnect.
	if strings.TrimSpace(s.stateRoot) == "" {
		s.stateRoot = fresh.stateRoot
	}
	// The lifetime travels with the connection it belongs to. Keeping the old
	// one would leave this session holding the process context of the child that
	// is being torn down two lines below — already cancelled by then — so the
	// next reconnect would build its replacement on a dead context and fail
	// before it reached the server.
	if fresh.baseCtx != nil {
		s.baseCtx = fresh.baseCtx
	}
	if fresh.reconnectTimeout > 0 {
		s.reconnectTimeout = fresh.reconnectTimeout
	}
	s.client, s.session, s.proc = fresh.client, fresh.session, fresh.proc
	s.mu.Unlock()
	// The fresh session object is now represented by this one; it must not own
	// the connection any more, or closing it would close the live connection.
	fresh.mu.Lock()
	fresh.client, fresh.session, fresh.proc = nil, nil, nil
	fresh.closed = true
	fresh.mu.Unlock()
	if oldProc != nil {
		oldProc.stop()
	}
	if oldSession != nil {
		// The displaced connection is closed with its child already signalled,
		// so its close reports the signal the child died from. That is the
		// expected teardown of the connection being replaced, not a failed
		// reconnect, and reporting it as one would make every reconnect look
		// broken.
		_ = closeClientSession(oldSession)
	}
	return nil
}

func (s *Session) label() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.serverName
}
