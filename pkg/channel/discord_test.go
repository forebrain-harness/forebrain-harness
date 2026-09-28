package channel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

func TestDiscordNewIDStartStopAndRegister(t *testing.T) {
	d := NewDiscord(DiscordConfig{})
	if d == nil || d.ID() != "discord" || d.httpClient == nil || d.httpClient.Timeout == 0 {
		t.Fatalf("discord=%#v", d)
	}
	if err := NewDiscord(DiscordConfig{}).Start(context.Background(), nil, nil); err != nil {
		t.Fatalf("disabled Start error: %v", err)
	}
	if err := NewDiscord(DiscordConfig{Enabled: true}).Start(context.Background(), nil, nil); err == nil {
		t.Fatal("expected missing token error")
	}
	if err := d.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
}

func TestDiscordDeliverOutbound(t *testing.T) {
	if err := NewDiscord(DiscordConfig{}).DeliverOutbound(context.Background(), Outbound{SessionID: " "}); err == nil {
		t.Fatal("expected invalid session error")
	}
	orig := discordAPIBase
	t.Cleanup(func() { discordAPIBase = orig })
	discordAPIBase = "://bad"
	if err := NewDiscord(DiscordConfig{}).DeliverOutbound(context.Background(), Outbound{SessionID: "C1"}); err == nil {
		t.Fatal("expected request error")
	}

	var gotPath string
	var gotAuth string
	var gotBody map[string]any
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		rw.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	discordAPIBase = srv.URL
	err := NewDiscord(DiscordConfig{BotToken: " token "}).DeliverOutbound(context.Background(), Outbound{SessionID: " C1:msg ", Text: " hello "})
	if err != nil {
		t.Fatalf("DeliverOutbound error: %v", err)
	}
	if gotPath != "/channels/C1/messages" || gotAuth != "Bot token" || gotBody["content"] != "hello" {
		t.Fatalf("path=%q auth=%q body=%v", gotPath, gotAuth, gotBody)
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	discordAPIBase = "http://127.0.0.1:1"
	if err := NewDiscord(DiscordConfig{}).DeliverOutbound(cancelCtx, Outbound{SessionID: "C1"}); err == nil {
		t.Fatal("expected client error")
	}

	bad := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, strings.Repeat("x", 2000), http.StatusBadGateway)
	}))
	defer bad.Close()
	discordAPIBase = bad.URL
	err = NewDiscord(DiscordConfig{}).DeliverOutbound(context.Background(), Outbound{SessionID: "C1"})
	if err == nil || !strings.Contains(err.Error(), "discord send status=502") || len(err.Error()) > 1100 {
		t.Fatalf("bad status err=%v", err)
	}
}

func TestConnectAndRead(t *testing.T) {
	orig := discordGatewayURL
	origIdentify := identifyConn
	t.Cleanup(func() {
		discordGatewayURL = orig
		identifyConn = origIdentify
	})
	var identify map[string]any
	srv := discordWSServer(t, func(conn *websocket.Conn) {
		_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 5}})
		if err := conn.ReadJSON(&identify); err != nil {
			t.Errorf("read identify: %v", err)
			return
		}
		_ = conn.WriteJSON(map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]any{"session_id": "sess", "resume_gateway_url": "wss://resume"}})
		_ = conn.WriteJSON(map[string]any{"op": 0, "t": "MESSAGE_CREATE", "s": 2, "d": map[string]any{"channel_id": "C1", "id": "M1", "content": " hi ", "author": map[string]any{"bot": false}}})
		_ = conn.WriteJSON(map[string]any{"op": 0, "t": "MESSAGE_CREATE", "s": 3, "d": map[string]any{"channel_id": "C1", "id": "M2", "content": "bot", "author": map[string]any{"bot": true}}})
		_ = conn.Close()
	})
	defer srv.Close()
	discordGatewayURL = discordHTTPToWS(srv.URL)

	bus := &discordBus{err: errors.New("ignored")}
	d := NewDiscord(DiscordConfig{BotToken: " token "})
	d.bus = bus
	err := d.connectAndRead(context.Background())
	if err == nil {
		t.Fatal("expected close read error")
	}
	if identify["op"].(float64) != 2 {
		t.Fatalf("identify=%v", identify)
	}
	if d.sessionID != "sess" || d.resumeGateway != "wss://resume" || d.seq == nil || *d.seq != 3 {
		t.Fatalf("ready/seq session=%q resume=%q seq=%v", d.sessionID, d.resumeGateway, d.seq)
	}
	if len(bus.events) != 1 || bus.events[0].SessionID != "C1:M1" || bus.events[0].Text != "hi" {
		t.Fatalf("events=%+v", bus.events)
	}
	if d.activeConn() != nil {
		t.Fatal("connection should be cleared")
	}
}

func TestConnectAndReadErrorsAndCancel(t *testing.T) {
	orig := discordGatewayURL
	origIdentify := identifyConn
	t.Cleanup(func() {
		discordGatewayURL = orig
		identifyConn = origIdentify
	})
	discordGatewayURL = "ws://127.0.0.1:1"
	if err := NewDiscord(DiscordConfig{}).connectAndRead(context.Background()); err == nil {
		t.Fatal("expected dial error")
	}

	badHello := discordWSServer(t, func(conn *websocket.Conn) {
		_ = conn.WriteJSON(map[string]any{"op": 9})
	})
	defer badHello.Close()
	discordGatewayURL = discordHTTPToWS(badHello.URL)
	if err := NewDiscord(DiscordConfig{}).connectAndRead(context.Background()); err == nil {
		t.Fatal("expected bad hello error")
	}

	readHello := discordWSServer(t, func(conn *websocket.Conn) {
		_ = conn.Close()
	})
	defer readHello.Close()
	discordGatewayURL = discordHTTPToWS(readHello.URL)
	if err := NewDiscord(DiscordConfig{}).connectAndRead(context.Background()); err == nil {
		t.Fatal("expected hello read error")
	}

	identifyFail := discordWSServer(t, func(conn *websocket.Conn) {
		_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 1000}})
		select {}
	})
	defer identifyFail.Close()
	discordGatewayURL = discordHTTPToWS(identifyFail.URL)
	identifyConn = func(*Discord, *websocket.Conn) error { return errors.New("identify failed") }
	if err := NewDiscord(DiscordConfig{BotToken: "token"}).connectAndRead(context.Background()); err == nil {
		t.Fatal("expected identify write error")
	}
	identifyConn = origIdentify

	connected := make(chan struct{})
	cancelSrv := discordWSServer(t, func(conn *websocket.Conn) {
		_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 0}})
		var identify map[string]any
		_ = conn.ReadJSON(&identify)
		_ = conn.WriteJSON(map[string]any{"op": 0, "t": "MESSAGE_CREATE", "d": map[string]any{"channel_id": "C", "content": "x"}})
		<-time.After(50 * time.Millisecond)
		_ = conn.Close()
	})
	defer cancelSrv.Close()
	discordGatewayURL = discordHTTPToWS(cancelSrv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	d := NewDiscord(DiscordConfig{BotToken: "token"})
	d.bus = &discordBus{onPublish: func() {
		close(connected)
		cancel()
	}}
	errCh := make(chan error, 1)
	go func() { errCh <- d.connectAndRead(ctx) }()
	<-connected
	if err := <-errCh; err != nil {
		t.Fatalf("cancelled connectAndRead error: %v", err)
	}
}

func TestGatewayLoopBackoffAndStop(t *testing.T) {
	origURL := discordGatewayURL
	origAfter := discordLoopAfter
	t.Cleanup(func() {
		discordGatewayURL = origURL
		discordLoopAfter = origAfter
	})
	var afterCalls atomic.Int64
	discordLoopAfter = func(time.Duration) <-chan time.Time {
		afterCalls.Add(1)
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	discordGatewayURL = "ws://127.0.0.1:1"
	d := NewDiscord(DiscordConfig{})
	d.done = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go d.gatewayLoop(ctx)
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if afterCalls.Load() >= 2 {
			cancel()
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-d.done:
	case <-time.After(time.Second):
		t.Fatal("gatewayLoop did not exit")
	}

	block := make(chan time.Time)
	discordLoopAfter = func(time.Duration) <-chan time.Time { return block }
	d = NewDiscord(DiscordConfig{})
	d.done = make(chan struct{})
	ctx, cancel = context.WithCancel(context.Background())
	go d.gatewayLoop(ctx)
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-d.done:
	case <-time.After(time.Second):
		t.Fatal("gatewayLoop did not exit during backoff")
	}

	d = NewDiscord(DiscordConfig{})
	d.done = make(chan struct{})
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	d.gatewayLoop(ctx)
	select {
	case <-d.done:
	default:
		t.Fatal("cancelled gatewayLoop did not close done")
	}

	var successes atomic.Int64
	successSrv := discordWSServer(t, func(conn *websocket.Conn) {
		successes.Add(1)
		_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 1000}})
		var identify map[string]any
		_ = conn.ReadJSON(&identify)
		_ = conn.WriteJSON(map[string]any{"op": 0, "t": "MESSAGE_CREATE", "d": map[string]any{"channel_id": "C", "content": "x"}})
	})
	defer successSrv.Close()
	discordGatewayURL = discordHTTPToWS(successSrv.URL)
	discordLoopAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	ctx, cancel = context.WithCancel(context.Background())
	d = NewDiscord(DiscordConfig{BotToken: "token"})
	d.bus = &discordBus{onPublish: cancel}
	d.done = make(chan struct{})
	go d.gatewayLoop(ctx)
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if successes.Load() >= 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-d.done:
	case <-time.After(time.Second):
		t.Fatal("success gatewayLoop did not exit")
	}
	if successes.Load() < 1 {
		t.Fatalf("successes=%d", successes.Load())
	}
}

func TestHeartbeatLoop(t *testing.T) {
	serverConnCh := make(chan *websocket.Conn, 1)
	srv := discordWSServer(t, func(conn *websocket.Conn) {
		serverConnCh <- conn
	})
	defer srv.Close()
	client := dialDiscordWS(t, srv.URL)
	defer client.Close()
	serverConn := <-serverConnCh
	defer serverConn.Close()

	d := NewDiscord(DiscordConfig{})
	seq := int64(42)
	d.seq = &seq
	ctx, cancel := context.WithCancel(context.Background())
	go d.heartbeatLoop(ctx, client, time.Millisecond)
	var got map[string]any
	if err := serverConn.ReadJSON(&got); err != nil {
		t.Fatalf("read heartbeat: %v", err)
	}
	cancel()
	if got["op"].(float64) != 1 || got["d"].(float64) != 42 {
		t.Fatalf("heartbeat=%v", got)
	}

	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	d.heartbeatLoop(ctx, client, time.Millisecond)
}

func TestHandleMessageCreateTrackReadyAndHelpers(t *testing.T) {
	d := NewDiscord(DiscordConfig{})
	d.handleMessageCreate(context.Background(), map[string]any{"content": "x"})
	d.bus = &discordBus{}
	d.handleMessageCreate(context.Background(), "bad")
	d.handleMessageCreate(context.Background(), map[string]any{"content": " "})
	d.handleMessageCreate(context.Background(), map[string]any{"content": "x", "author": map[string]any{"bot": true}, "channel_id": "C"})
	d.handleMessageCreate(context.Background(), map[string]any{"content": "x"})
	d.handleMessageCreate(context.Background(), map[string]any{"content": "x", "channel_id": float64(123)})
	if len(d.bus.(*discordBus).events) != 1 || d.bus.(*discordBus).events[0].SessionID != "123" {
		t.Fatalf("events=%+v", d.bus.(*discordBus).events)
	}

	d.trackSequence(nil)
	d.trackSequence(map[string]any{})
	d.trackSequence(map[string]any{"s": "bad"})
	d.trackSequence(map[string]any{"s": float64(99)})
	if d.seq == nil || *d.seq != 99 {
		t.Fatalf("seq=%v", d.seq)
	}
	d.cacheReady("bad")
	d.cacheReady(map[string]any{"session_id": " s ", "resume_gateway_url": " r "})
	if d.sessionID != "s" || d.resumeGateway != "r" {
		t.Fatalf("ready session=%q resume=%q", d.sessionID, d.resumeGateway)
	}
	d.setConn(nil)
	if d.activeConn() != nil {
		t.Fatal("activeConn should be nil")
	}
	if parseDiscordSession(" C : M ") != "C" || parseDiscordSession(" ") != "" {
		t.Fatal("parseDiscordSession mismatch")
	}
	if discordString("x") != "x" || discordString(float64(42)) != "42" || discordString(nil) != "" {
		t.Fatal("discordString mismatch")
	}
	if !discordBool(true) || discordBool("true") {
		t.Fatal("discordBool mismatch")
	}
}

func TestStartStopRunning(t *testing.T) {
	origURL := discordGatewayURL
	t.Cleanup(func() { discordGatewayURL = origURL })
	srv := discordWSServer(t, func(conn *websocket.Conn) {
		_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 1000}})
		var identify map[string]any
		_ = conn.ReadJSON(&identify)
		<-time.After(200 * time.Millisecond)
		_ = conn.Close()
	})
	defer srv.Close()
	discordGatewayURL = discordHTTPToWS(srv.URL)
	d := NewDiscord(DiscordConfig{Enabled: true, BotToken: "token"})
	if err := d.Start(context.Background(), nil, &discordBus{}); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if d.activeConn() != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := d.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
}

func discordWSServer(t *testing.T, fn func(*websocket.Conn)) *httptest.Server {
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

func dialDiscordWS(t *testing.T, raw string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(discordHTTPToWS(raw), nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	return conn
}

func discordHTTPToWS(raw string) string {
	u, _ := url.Parse(raw)
	u.Scheme = strings.Replace(u.Scheme, "http", "ws", 1)
	return u.String()
}

type discordBus struct {
	events    []Inbound
	err       error
	onPublish func()
}

func (d *discordBus) PublishInbound(_ context.Context, m Inbound) error {
	d.events = append(d.events, m)
	if d.onPublish != nil {
		d.onPublish()
	}
	return d.err
}

var _ = io.EOF
