package peer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// defaultFirstLineDeadline is how long a connection has to produce its
	// first complete line before it is dropped.
	defaultFirstLineDeadline = 30 * time.Second
	// defaultMaxLine is the longest line a peer may send, authentication
	// included.
	defaultMaxLine = 1048576
	// socketFileMode is the mode of a listening socket: this user only.
	socketFileMode os.FileMode = 0o600
	// maxSidecarAttempts is how many alternate names a busy socket path is
	// given before listening gives up.
	maxSidecarAttempts = 3
	// connBuffer is the read buffer one connection starts with.
	connBuffer = 64 * 1024
	// queueDepth is how many arrived frames may wait for the serial worker.
	// A reader must never stall behind a busy handler: stalling it would keep
	// the connection open, and the sender — which waits for us to close so
	// that it can be identified — would have to time out instead. Ordering is
	// unaffected, because one worker drains the queue.
	queueDepth = 64
)

// Inbound is one frame that arrived on a Server, with everything the receiving
// side needs to decide about it.
type Inbound struct {
	Frame         Frame
	PeerPID       int
	PeerUID       int
	Authenticated bool
	// Local is the socket this frame arrived on. It is not always
	// Server.Path: after a registration-mode switch the old socket is closed,
	// and a frame already in flight on it belongs to the old mode.
	Local string
}

// Handler is what a Server hands frames to.
type Handler interface {
	HandleFrame(ctx context.Context, in Inbound)
}

// Server listens on one socket, reads NDJSON frames from peers, and hands them
// to a Handler. A process runs exactly one Server at a time; switching
// registration mode closes the old one before the new one listens.
type Server struct {
	// Path is the socket to listen on. After Start it is the socket actually
	// in use, which may be a sidecar name when the requested one was taken by
	// a live process.
	Path string
	// Token is the peer token this server expects; a connection that presents
	// a different one is served anyway (POSIX peers authenticate optionally)
	// but is marked unauthenticated.
	Token string
	// FirstLineDeadline and MaxLine default to 30s and 1 MiB.
	FirstLineDeadline time.Duration
	MaxLine           int
	Handler           Handler

	mu       sync.Mutex
	listener net.Listener
	conns    map[*net.UnixConn]struct{}
	queue    chan Inbound
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// Start listens and serves until Close, in background goroutines.
func (s *Server) Start(ctx context.Context) error {
	listener, actual, err := Listen(s.Path)
	if err != nil {
		return err
	}
	s.Path = actual
	serverCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.listener = listener
	s.conns = map[*net.UnixConn]struct{}{}
	s.queue = make(chan Inbound, queueDepth)
	s.cancel = cancel
	s.mu.Unlock()

	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		s.acceptLoop(serverCtx, listener)
	}()
	go func() {
		defer s.wg.Done()
		s.workLoop(serverCtx)
	}()
	return nil
}

// Close stops accepting, closes the connections still open, and removes the
// socket file.
func (s *Server) Close() error {
	s.mu.Lock()
	cancel := s.cancel
	listener := s.listener
	conns := make([]*net.UnixConn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.conns = nil
	s.listener = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	for _, conn := range conns {
		conn.Close()
	}
	if listener != nil {
		return listener.Close()
	}
	return nil
}

// acceptLoop takes connections until the listener closes. The listener is a
// parameter, not s.listener: Close nils that field to release the socket.
func (s *Server) acceptLoop(ctx context.Context, listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		unixConn, ok := conn.(*net.UnixConn)
		if !ok {
			conn.Close()
			continue
		}
		if !s.track(unixConn) {
			unixConn.Close()
			return
		}
		go func() {
			defer s.untrack(unixConn)
			s.serveConn(ctx, unixConn)
		}()
	}
}

// track registers a connection so Close can tear it down.
func (s *Server) track(conn *net.UnixConn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns == nil {
		return false
	}
	s.conns[conn] = struct{}{}
	return true
}

// untrack forgets a closed connection.
func (s *Server) untrack(conn *net.UnixConn) {
	s.mu.Lock()
	if s.conns != nil {
		delete(s.conns, conn)
	}
	s.mu.Unlock()
	conn.Close()
}

// serveConn reads one peer's frames.
func (s *Server) serveConn(ctx context.Context, conn *net.UnixConn) {
	pid, uid, ok := peerCreds(conn)
	if !ok || uid != selfUID() {
		// Another user's process, or no credentials at all: nothing on this
		// connection can be served.
		return
	}
	reader := bufio.NewReaderSize(conn, connBuffer)
	conn.SetReadDeadline(time.Now().Add(s.firstLineDeadline()))
	authenticated := false
	first := true
	for {
		line, err := readLine(reader, s.maxLine())
		if err != nil {
			return
		}
		if len(line) == 0 {
			continue
		}
		frame, err := DecodeFrame(line)
		if err != nil {
			// A line that is not a frame is not ours to interpret; the
			// connection may still carry a valid one behind it.
			continue
		}
		if first {
			first = false
			conn.SetReadDeadline(time.Time{})
			if frame.isAuth() {
				authenticated = s.tokenMatches(frame.Token)
				continue
			}
		}
		if s.Handler == nil {
			continue
		}
		in := Inbound{
			Frame:         frame,
			PeerPID:       pid,
			PeerUID:       uid,
			Authenticated: authenticated,
			Local:         s.Path,
		}
		if frame.immediate() {
			s.Handler.HandleFrame(ctx, in)
			continue
		}
		select {
		case s.queue <- in:
		case <-ctx.Done():
			return
		}
	}
}

// workLoop handles the serial queue: one frame at a time, in arrival order.
func (s *Server) workLoop(ctx context.Context) {
	for {
		select {
		case in := <-s.queue:
			if s.Handler != nil {
				s.Handler.HandleFrame(ctx, in)
			}
		case <-ctx.Done():
			return
		}
	}
}

// firstLineDeadline is the configured deadline, or the protocol's default.
func (s *Server) firstLineDeadline() time.Duration {
	if s.FirstLineDeadline > 0 {
		return s.FirstLineDeadline
	}
	return defaultFirstLineDeadline
}

// maxLine is the configured line cap, or the protocol's default.
func (s *Server) maxLine() int {
	if s.MaxLine > 0 {
		return s.MaxLine
	}
	return defaultMaxLine
}

// tokenMatches compares a presented token with ours in constant time.
func (s *Server) tokenMatches(presented string) bool {
	if s.Token == "" || presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(s.Token)) == 1
}

// errLineTooLong is reported for a line past the cap; the connection is then
// dropped.
var errLineTooLong = errors.New("peer: line is longer than the peer line limit")

// readLine reads one newline-terminated line, refusing one past max bytes
// instead of buffering it.
func readLine(reader *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > max+1 {
			return nil, errLineTooLong
		}
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return nil, err
	}
	line = bytes.TrimSuffix(line, []byte("\n"))
	return bytes.TrimSuffix(line, []byte("\r")), nil
}

// Listen creates the listening socket at path, or at a sidecar name when a live
// process already holds path. The socket directory must pass vetSocketDir: a
// directory another user could write to is not a place to listen.
func Listen(path string) (net.Listener, string, error) {
	dir := filepath.Dir(path)
	if err := vetSocketDir(dir); err != nil {
		return nil, "", err
	}
	cleanDeadSidecars(path)

	listener, err := listenUnix(path)
	if err == nil {
		return listener, path, nil
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		return nil, "", err
	}
	if !socketAlive(path) {
		// The socket file is a leftover from a process that is gone; only the
		// file, never a live listener, is removed and replaced.
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, "", err
		}
		listener, err := listenUnix(path)
		if err != nil {
			return nil, "", err
		}
		return listener, path, nil
	}
	for attempt := 0; attempt < maxSidecarAttempts; attempt++ {
		sidecar, ok := sidecarPath(path)
		if !ok {
			break
		}
		listener, err := listenUnix(sidecar)
		if err == nil {
			return listener, sidecar, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("peer: %s is held by a live peer and no alternate name was free", path)
}

// listenUnix listens and restricts the socket to this user.
func listenUnix(path string) (net.Listener, error) {
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, socketFileMode); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

// sidecarPattern is the shape of a sidecar name for the socket path in
// question: <name>-<8 hex digits>.sock.
var sidecarPattern = regexp.MustCompile(`^(.+)-[0-9a-f]{8}\.sock$`)

// sidecarPath is an unused-looking alternate name beside path.
func sidecarPath(path string) (string, bool) {
	base := strings.TrimSuffix(filepath.Base(path), ".sock")
	suffix, err := randomHex(4)
	if err != nil {
		return "", false
	}
	return filepath.Join(filepath.Dir(path), base+"-"+suffix+".sock"), true
}

// cleanDeadSidecars removes sidecar sockets beside path whose process is gone,
// so a long-lived directory does not fill with dead names.
func cleanDeadSidecars(path string) {
	dir := filepath.Dir(path)
	base := strings.TrimSuffix(filepath.Base(path), ".sock")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		match := sidecarPattern.FindStringSubmatch(entry.Name())
		if match == nil || match[1] != base {
			continue
		}
		candidate := filepath.Join(dir, entry.Name())
		if socketAlive(candidate) {
			continue
		}
		os.Remove(candidate)
	}
}
