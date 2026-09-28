package channel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/testutil"
	"github.com/gorilla/websocket"
)

func TestNewStartStopAndConnectionState(t *testing.T) {
	b := NewWSBridge(WSConfig{ChannelID: "ws"})
	if b == nil {
		t.Fatal("New returned nil")
	}
	if err := b.Start(context.Background(), nil); err != nil {
		t.Fatalf("disabled Start error: %v", err)
	}
	if err := NewWSBridge(WSConfig{ChannelID: "ws", Enabled: true}).Start(context.Background(), nil); err == nil {
		t.Fatal("expected missing url error")
	}
	// This bridge has to be stopped, not just started: its resolver hands back
	// an empty handshake, so its loop spins on the retry path forever, reading
	// the package-level loopAfter every time. Abandoning it leaks a goroutine
	// that outlives this test and races the loopAfter write in
	// TestLoopBackoffAndStop, which made the whole package fail under -race.
	resolverOnly := NewWSBridge(WSConfig{ChannelID: "ws", Enabled: true, Resolver: func(context.Context) (WSHandshake, error) {
		return WSHandshake{}, nil
	}})
	if err := resolverOnly.Start(context.Background(), nil); err != nil {
		t.Fatalf("resolver-only Start error: %v", err)
	}
	t.Cleanup(resolverOnly.Stop)

	b.Stop()
	if b.activeConn() != nil {
		t.Fatal("activeConn should be nil")
	}
	b.setConn(nil)

	srv := wsServer(t, func(conn *websocket.Conn) {
		<-time.After(200 * time.Millisecond)
		_ = conn.Close()
	})
	defer srv.Close()
	running := NewWSBridge(WSConfig{ChannelID: "ws", Enabled: true, WSURL: httpToWS(srv.URL)})
	if err := running.Start(context.Background(), nil); err != nil {
		t.Fatalf("running Start error: %v", err)
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if running.activeConn() != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	running.Stop()
	if running.activeConn() != nil {
		t.Fatal("running Stop should clear active conn")
	}
}

func TestWSDeliverOutbound(t *testing.T) {
	if err := NewWSBridge(WSConfig{ChannelID: "ws"}).DeliverOutbound(context.Background(), Outbound{}); err == nil {
		t.Fatal("expected not connected error")
	}

	serverConnCh := make(chan *websocket.Conn, 1)
	srv := wsServer(t, func(conn *websocket.Conn) {
		serverConnCh <- conn
	})
	defer srv.Close()
	client := dialWS(t, srv.URL, nil)
	defer client.Close()
	serverConn := <-serverConnCh
	defer serverConn.Close()

	b := NewWSBridge(WSConfig{ChannelID: "ws"})
	b.setConn(client)
	if err := b.DeliverOutbound(context.Background(), Outbound{SessionID: " s ", Text: " hi "}); err != nil {
		t.Fatalf("DeliverOutbound error: %v", err)
	}
	var got map[string]any
	if err := serverConn.ReadJSON(&got); err != nil {
		t.Fatalf("ReadJSON: %v", err)
	}
	if got["cmd"] != "send" || got["channel_id"] != "ws" || got["session_id"] != "s" || got["text"] != "hi" {
		t.Fatalf("payload=%v", got)
	}

	_ = client.Close()
	if err := b.DeliverOutbound(context.Background(), Outbound{}); err == nil {
		t.Fatal("expected write error after close")
	}
}

func TestConnectAndConsume(t *testing.T) {
	var gotHeader string
	srv := wsServer(t, func(conn *websocket.Conn) {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"session_id":"s1","text":" hello "}`))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"text":"default session"}`))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`   `))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`plain text`))
		_ = conn.Close()
	})
	defer srv.Close()
	srv.Config.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Test")
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(rw, r, nil)
		if err != nil {
			return
		}
		func(conn *websocket.Conn) {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"session_id":"s1","text":" hello "}`))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"text":"default session"}`))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`   `))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`plain text`))
			_ = conn.Close()
		}(conn)
	})

	bus := &wsBus{err: errors.New("ignored")}
	b := NewWSBridge(WSConfig{
		ChannelID:     "ws",
		WSURL:         "ws://unused",
		SessionPrefix: "pref",
		Header:        http.Header{"X-Test": []string{"base"}},
		Resolver: func(context.Context) (WSHandshake, error) {
			return WSHandshake{WSURL: httpToWS(srv.URL), Header: http.Header{"X-Test": []string{"resolved"}}}, nil
		},
	})
	b.bus = bus
	if err := b.connectAndConsume(context.Background()); err == nil {
		t.Fatal("expected close read error")
	}
	if gotHeader != "resolved" {
		t.Fatalf("header=%q", gotHeader)
	}
	if len(bus.events) != 3 {
		t.Fatalf("events=%+v", bus.events)
	}
	if bus.events[0].SessionID != "s1" || bus.events[0].Text != "hello" {
		t.Fatalf("event0=%+v", bus.events[0])
	}
	if bus.events[1].SessionID != "pref-default" || bus.events[1].Text != "default session" {
		t.Fatalf("event1=%+v", bus.events[1])
	}
	if bus.events[2].SessionID != "pref-default" || bus.events[2].Text != "plain text" {
		t.Fatalf("event2=%+v", bus.events[2])
	}
	if b.activeConn() != nil {
		t.Fatal("connection should be cleared after consume")
	}
}

func TestConnectAndConsumeErrors(t *testing.T) {
	if err := NewWSBridge(WSConfig{ChannelID: "ws", Resolver: func(context.Context) (WSHandshake, error) {
		return WSHandshake{}, errors.New("resolve failed")
	}}).connectAndConsume(context.Background()); err == nil {
		t.Fatal("expected resolver error")
	}
	if err := NewWSBridge(WSConfig{ChannelID: "ws"}).connectAndConsume(context.Background()); err == nil {
		t.Fatal("expected empty url error")
	}
	if err := NewWSBridge(WSConfig{ChannelID: "ws", WSURL: "ws://127.0.0.1:1"}).connectAndConsume(context.Background()); err == nil {
		t.Fatal("expected dial error")
	}

	srv := wsServer(t, func(conn *websocket.Conn) {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"text":"stop"}`))
		<-time.After(50 * time.Millisecond)
		_ = conn.Close()
	})
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	b := NewWSBridge(WSConfig{ChannelID: "ws", WSURL: httpToWS(srv.URL), SessionPrefix: "p"})
	b.bus = &wsBus{onPublish: cancel}
	if err := b.connectAndConsume(ctx); err != nil {
		t.Fatalf("cancelled context after message should return nil, got %v", err)
	}
}

func TestLoopBackoffAndStop(t *testing.T) {
	orig := loopAfter
	t.Cleanup(func() { loopAfter = orig })
	var afterCalls atomic.Int64
	loopAfter = func(time.Duration) <-chan time.Time {
		afterCalls.Add(1)
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}

	b := NewWSBridge(WSConfig{ChannelID: "ws", Enabled: true, WSURL: "ws://127.0.0.1:1"})
	b.done = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go b.loop(ctx)
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if afterCalls.Load() >= 2 {
			cancel()
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-b.done:
	case <-time.After(time.Second):
		t.Fatal("loop did not exit")
	}

	block := make(chan time.Time)
	loopAfter = func(time.Duration) <-chan time.Time { return block }
	b = NewWSBridge(WSConfig{ChannelID: "ws", Enabled: true, WSURL: "ws://127.0.0.1:1"})
	b.done = make(chan struct{})
	ctx, cancel = context.WithCancel(context.Background())
	go b.loop(ctx)
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-b.done:
	case <-time.After(time.Second):
		t.Fatal("loop did not exit while backing off")
	}

	b = NewWSBridge(WSConfig{})
	b.done = make(chan struct{})
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	b.loop(ctx)
	select {
	case <-b.done:
	default:
		t.Fatal("cancelled loop did not close done")
	}

	loopAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	var successes atomic.Int64
	srv := wsServer(t, func(conn *websocket.Conn) {
		successes.Add(1)
		_ = conn.Close()
	})
	defer srv.Close()
	b = NewWSBridge(WSConfig{ChannelID: "ws", Enabled: true, WSURL: httpToWS(srv.URL)})
	b.done = make(chan struct{})
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	go b.loop(ctx)
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if successes.Load() >= 2 {
			cancel()
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-b.done:
	case <-time.After(time.Second):
		t.Fatal("success loop did not exit")
	}
	if successes.Load() < 2 {
		t.Fatalf("successes=%d", successes.Load())
	}
}

func TestCloneHeaderDecodeInboundAndStringValue(t *testing.T) {
	if got := cloneHeader(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil clone=%v", got)
	}
	h := http.Header{"A": []string{"x"}}
	cp := cloneHeader(h)
	cp.Set("A", "y")
	if h.Get("A") != "x" || cp.Get("A") != "y" {
		t.Fatalf("header clone original=%v copy=%v", h, cp)
	}

	tests := []struct {
		body     string
		wantText string
		wantSID  string
	}{
		{body: "", wantText: "", wantSID: ""},
		{body: "plain", wantText: "plain", wantSID: ""},
		{body: `{"sessionId":"s","content":"c"}`, wantText: "c", wantSID: "s"},
		{body: `{"chat_id":"c1","message":"m"}`, wantText: "m", wantSID: "c1"},
		{body: `{"conversation_id":"c2","body":"b"}`, wantText: "b", wantSID: "c2"},
		{body: `{"sender_id":"u","unknown":"x"}`, wantText: `{"sender_id":"u","unknown":"x"}`, wantSID: "u"},
		{body: `{"from":"f","text":" "}`, wantText: `{"from":"f","text":" "}`, wantSID: "f"},
	}
	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			text, sid := decodeInbound([]byte(tt.body))
			if text != tt.wantText || sid != tt.wantSID {
				t.Fatalf("text=%q sid=%q", text, sid)
			}
		})
	}
	if got := stringValue(map[string]any{"a": nil, "b": 1, "c": " value "}, "a", "b", "c"); got != " value " {
		t.Fatalf("stringValue=%q", got)
	}
	if got := stringValue(map[string]any{}, "missing"); got != "" {
		t.Fatalf("missing stringValue=%q", got)
	}
}

func wsServer(t *testing.T, fn func(*websocket.Conn)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	return testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(rw, r, nil)
		if err != nil {
			return
		}
		fn(conn)
	}))
}

func dialWS(t *testing.T, httpURL string, header http.Header) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(httpToWS(httpURL), header)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	return conn
}

func httpToWS(raw string) string {
	u, _ := url.Parse(raw)
	u.Scheme = strings.Replace(u.Scheme, "http", "ws", 1)
	return u.String()
}

type wsBus struct {
	events    []Inbound
	err       error
	onPublish func()
}

func (w *wsBus) PublishInbound(_ context.Context, m Inbound) error {
	w.events = append(w.events, m)
	if w.onPublish != nil {
		w.onPublish()
	}
	return w.err
}
