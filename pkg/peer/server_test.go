package peer

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// collector records every frame a server hands over.
type collector struct {
	mu     sync.Mutex
	frames []Inbound
	ch     chan Inbound
}

func newCollector() *collector {
	return &collector{ch: make(chan Inbound, 16)}
}

func (c *collector) HandleFrame(_ context.Context, in Inbound) {
	c.mu.Lock()
	c.frames = append(c.frames, in)
	c.mu.Unlock()
	select {
	case c.ch <- in:
	default:
	}
}

// next waits for the next frame, failing the test when none arrives.
func (c *collector) next(t *testing.T) Inbound {
	t.Helper()
	select {
	case in := <-c.ch:
		return in
	case <-time.After(5 * time.Second):
		t.Fatal("no frame arrived")
		return Inbound{}
	}
}

// startServer listens and serves for the duration of the test.
func startServer(t *testing.T, path, token string, handler Handler) *Server {
	t.Helper()
	return startServerWith(t, &Server{Path: path, Token: token, Handler: handler})
}

// startServerWith serves an already configured server, so a test that changes
// FirstLineDeadline or MaxLine does it before the accept loop reads them.
func startServerWith(t *testing.T, server *Server) *Server {
	t.Helper()
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("start server on %s: %v", server.Path, err)
	}
	t.Cleanup(func() { server.Close() })
	return server
}

// peerSocketPath is a socket path in the isolated directory.
func peerSocketPath(t *testing.T, dirs testDirs, name string) string {
	t.Helper()
	return filepath.Join(dirs.sockets, name+".sock")
}

func TestServerReceivesAnUnauthenticatedFrame(t *testing.T) {
	dirs := isolate(t)
	collector := newCollector()
	server := startServer(t, peerSocketPath(t, dirs, "collection"), "secret-token", collector)

	frame := NewUserFrame(UserFrameOptions{From: EncodeUDS(server.Path), Content: "hello peer"})
	if err := Send(context.Background(), server.Path, frame, SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	in := collector.next(t)
	if in.Authenticated {
		t.Fatal("an unauthenticated frame was marked authenticated")
	}
	if in.Frame.Message.Content != "hello peer" || in.Frame.Type != FrameTypeUser {
		t.Fatalf("frame = %#v", in.Frame)
	}
	if in.PeerPID != os.Getpid() {
		t.Fatalf("peer pid = %d, want %d", in.PeerPID, os.Getpid())
	}
	if in.PeerUID != selfUID() {
		t.Fatalf("peer uid = %d, want %d", in.PeerUID, selfUID())
	}
	if in.Local != server.Path {
		t.Fatalf("local socket = %q, want %q", in.Local, server.Path)
	}
}

func TestServerAuthentication(t *testing.T) {
	dirs := isolate(t)
	collector := newCollector()
	server := startServer(t, peerSocketPath(t, dirs, "auth"), "0123456789abcdef0123456789abcdef", collector)

	// The right token: the key file is found, sent first, and accepted.
	if err := WriteKey(dirs.registry, os.Getpid(), server.Path, server.Token); err != nil {
		t.Fatalf("WriteKey: %v", err)
	}
	frame := NewUserFrame(UserFrameOptions{From: EncodeUDS(server.Path), Content: "with token"})
	if err := Send(context.Background(), server.Path, frame, SendOptions{KeyDir: dirs.registry}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if in := collector.next(t); !in.Authenticated {
		t.Fatal("a correctly authenticated frame was not marked authenticated")
	}

	// A wrong token: the frame behind it is still served, unauthenticated.
	wrongDir := t.TempDir()
	if err := WriteKey(wrongDir, os.Getpid(), server.Path, "ffffffffffffffffffffffffffffffff"); err != nil {
		t.Fatalf("WriteKey: %v", err)
	}
	second := NewUserFrame(UserFrameOptions{From: EncodeUDS(server.Path), Content: "wrong token"})
	if err := Send(context.Background(), server.Path, second, SendOptions{KeyDir: wrongDir}); err != nil {
		t.Fatalf("Send with a wrong token: %v", err)
	}
	in := collector.next(t)
	if in.Authenticated {
		t.Fatal("a wrong token was accepted")
	}
	if in.Frame.Message.Content != "wrong token" {
		t.Fatalf("the frame after a wrong token was lost: %#v", in.Frame)
	}
}

func TestInboundLocalIdentifiesTheSocket(t *testing.T) {
	dirs := isolate(t)
	first, second := newCollector(), newCollector()
	serverA := startServer(t, peerSocketPath(t, dirs, "a"), "", first)
	serverB := startServer(t, peerSocketPath(t, dirs, "b"), "", second)
	if serverA.Path == serverB.Path {
		t.Fatalf("both servers listen on %q", serverA.Path)
	}

	for _, server := range []*Server{serverA, serverB} {
		frame := NewUserFrame(UserFrameOptions{From: EncodeUDS(server.Path), Content: "hi " + server.Path})
		if err := Send(context.Background(), server.Path, frame, SendOptions{}); err != nil {
			t.Fatalf("Send to %s: %v", server.Path, err)
		}
	}
	if in := first.next(t); in.Local != serverA.Path {
		t.Fatalf("first server's frame says %q, want %q", in.Local, serverA.Path)
	}
	if in := second.next(t); in.Local != serverB.Path {
		t.Fatalf("second server's frame says %q, want %q", in.Local, serverB.Path)
	}
}

func TestServerFirstLineDeadline(t *testing.T) {
	dirs := isolate(t)
	server := startServerWith(t, &Server{
		Path:              peerSocketPath(t, dirs, "deadline"),
		Handler:           newCollector(),
		FirstLineDeadline: 200 * time.Millisecond,
	})

	conn, err := net.Dial("unix", server.Path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Nothing is written: the server must drop the connection on its own.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	start := time.Now()
	buffer := make([]byte, 1)
	if _, err := conn.Read(buffer); err == nil {
		t.Fatal("the connection stayed open past the first-line deadline")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the connection was dropped after %v", elapsed)
	}
}

func TestServerDropsAnOverlongLine(t *testing.T) {
	dirs := isolate(t)
	collector := newCollector()
	server := startServerWith(t, &Server{
		Path:    peerSocketPath(t, dirs, "overlong"),
		Handler: collector,
		MaxLine: 128,
	})

	conn, err := net.Dial("unix", server.Path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	overlong := make([]byte, 512)
	for i := range overlong {
		overlong[i] = 'x'
	}
	overlong = append(overlong, '\n')
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	conn.Write(overlong)

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("the connection survived an overlong line")
	}
	select {
	case in := <-collector.ch:
		t.Fatalf("an overlong line was delivered: %#v", in)
	case <-time.After(100 * time.Millisecond):
	}
}

// gateHandler blocks a user frame until the test releases it, so the serial
// queue can be shown to be a queue.
type gateHandler struct {
	blocked chan Inbound
	release chan struct{}
	control chan Inbound
}

func newGateHandler() *gateHandler {
	return &gateHandler{
		blocked: make(chan Inbound, 1),
		release: make(chan struct{}),
		control: make(chan Inbound, 1),
	}
}

func (h *gateHandler) HandleFrame(_ context.Context, in Inbound) {
	if in.Frame.Type == FrameTypeControl {
		select {
		case h.control <- in:
		default:
		}
		return
	}
	h.blocked <- in
	<-h.release
}

func TestControlFramesSkipTheSerialQueue(t *testing.T) {
	dirs := isolate(t)
	handler := newGateHandler()
	server := startServer(t, peerSocketPath(t, dirs, "queue"), "", handler)

	userFrame := NewUserFrame(UserFrameOptions{From: EncodeUDS(server.Path), Content: "queued"})
	if err := Send(context.Background(), server.Path, userFrame, SendOptions{}); err != nil {
		t.Fatalf("Send user frame: %v", err)
	}
	select {
	case <-handler.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("the user frame never reached the handler")
	}

	receipt := PeerMessageStatus{Status: StatusDelivered, From: EncodeUDS(server.Path), OrigMsgID: userFrame.MsgID}.Frame()
	if err := Send(context.Background(), server.Path, receipt, SendOptions{}); err != nil {
		t.Fatalf("Send control frame: %v", err)
	}
	select {
	case in := <-handler.control:
		if in.Frame.Action != ActionPeerMessageStatus {
			t.Fatalf("control frame = %#v", in.Frame)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a control frame waited behind a blocked user frame")
	}
	close(handler.release)
}

func TestListenUsesASidecarForALiveSocket(t *testing.T) {
	dirs := isolate(t)
	path := peerSocketPath(t, dirs, "live")
	server := startServer(t, path, "", newCollector())
	if server.Path != path {
		t.Fatalf("the first server listens on %q, want %q", server.Path, path)
	}

	listener, actual, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen beside a live socket: %v", err)
	}
	defer listener.Close()
	if actual == path {
		t.Fatal("the live socket was taken over")
	}
	if filepath.Dir(actual) != filepath.Dir(path) {
		t.Fatalf("the sidecar %q left the socket directory", actual)
	}
	if !sidecarPattern.MatchString(filepath.Base(actual)) {
		t.Fatalf("sidecar name %q has the wrong shape", filepath.Base(actual))
	}
}

func TestListenRebindsADeadSocket(t *testing.T) {
	dirs := isolate(t)
	path := peerSocketPath(t, dirs, "dead")
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	listener, actual, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen over a dead socket: %v", err)
	}
	defer listener.Close()
	if actual != path {
		t.Fatalf("listen landed on %q, want the requested %q", actual, path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("%s is not a socket after listening", path)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestListenRefusesAnOpenSocketDirectory(t *testing.T) {
	dirs := isolate(t)
	openDir := filepath.Join(dirs.sockets, "open")
	if err := os.Mkdir(openDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(openDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Listen(filepath.Join(openDir, strconv.Itoa(os.Getpid())+".sock")); err == nil {
		t.Fatal("Listen accepted a world-writable socket directory")
	}
}
