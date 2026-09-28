package channel

import (
	"context"
	"crypto/tls"
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

func TestMattermostNewIDStartStopAndRegister(t *testing.T) {
	m := NewMattermost(MattermostConfig{})
	if m == nil || m.ID() != "mattermost" || m.httpClient == nil || m.httpClient.Timeout == 0 || m.seen == nil {
		t.Fatalf("mattermost=%#v", m)
	}
	if err := NewMattermost(MattermostConfig{}).Start(context.Background(), nil, &mattermostBus{}); err != nil {
		t.Fatalf("disabled Start error: %v", err)
	}
	if err := NewMattermost(MattermostConfig{Enabled: true}).Start(context.Background(), nil, nil); err == nil {
		t.Fatal("expected missing outbound url error")
	}
	if err := NewMattermost(MattermostConfig{Enabled: true, OutboundURL: "://bad"}).Start(context.Background(), nil, nil); err == nil {
		t.Fatal("expected bot user fetch error")
	}

	oldAfter := mattermostAfter
	t.Cleanup(func() { mattermostAfter = oldAfter })
	mattermostAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	if err := NewMattermost(MattermostConfig{}).Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
}

func TestMattermostStopClosesActiveConnection(t *testing.T) {
	serverConnCh := make(chan *websocket.Conn, 1)
	srv := mattermostWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		serverConnCh <- conn
	})
	defer srv.Close()

	client := dialMattermostWS(t, srv.URL)
	defer client.Close()
	serverConn := <-serverConnCh
	defer serverConn.Close()

	m := NewMattermost(MattermostConfig{})
	m.wsConn = client
	// Stand in for a loop that already exited: Start creates done, and the
	// loop closes it on the way out.
	m.done = make(chan struct{})
	close(m.done)
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
	if m.wsConn != nil {
		t.Fatal("Stop did not clear ws connection")
	}
}

func TestMattermostStartFetchesBotAndNormalizesBase(t *testing.T) {
	oldAfter := mattermostAfter
	t.Cleanup(func() { mattermostAfter = oldAfter })
	mattermostAfter = func(time.Duration) <-chan time.Time {
		return make(chan time.Time)
	}

	var usersMeAuth string
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/users/me" {
			usersMeAuth = r.Header.Get("Authorization")
			_, _ = rw.Write([]byte(`{"id":" bot-id "}`))
			return
		}
		http.NotFound(rw, r)
	}))
	defer srv.Close()

	m := NewMattermost(MattermostConfig{Enabled: true, OutboundURL: srv.URL + "/api/v4/", Token: " tok "})
	if err := m.Start(context.Background(), nil, &mattermostBus{}); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if usersMeAuth != "Bearer tok" || m.botUserID != "bot-id" || m.cfg.OutboundURL != srv.URL {
		t.Fatalf("auth=%q bot=%q base=%q", usersMeAuth, m.botUserID, m.cfg.OutboundURL)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
}

func TestMattermostDeliverOutbound(t *testing.T) {
	if err := NewMattermost(MattermostConfig{}).DeliverOutbound(context.Background(), Outbound{SessionID: " "}); err == nil {
		t.Fatal("expected invalid session error")
	}
	if err := NewMattermost(MattermostConfig{OutboundURL: "://bad"}).DeliverOutbound(context.Background(), Outbound{SessionID: "C1"}); err == nil {
		t.Fatal("expected request creation error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewMattermost(MattermostConfig{OutboundURL: "http://127.0.0.1:1"}).DeliverOutbound(ctx, Outbound{SessionID: "C1"}); err == nil {
		t.Fatal("expected client error")
	}

	bad := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, strings.Repeat("x", 2000), http.StatusBadGateway)
	}))
	defer bad.Close()
	err := NewMattermost(MattermostConfig{OutboundURL: bad.URL, Token: "tok"}).DeliverOutbound(context.Background(), Outbound{SessionID: "C1"})
	if err == nil || !strings.Contains(err.Error(), "mattermost send status=502") || len(err.Error()) > 1150 {
		t.Fatalf("status error=%v", err)
	}

	var gotAuth, gotContentType, gotPath string
	var gotBody map[string]string
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		rw.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	err = NewMattermost(MattermostConfig{OutboundURL: srv.URL, Token: " tok "}).DeliverOutbound(context.Background(), Outbound{SessionID: " channel:C1 ", Text: " hello "})
	if err != nil {
		t.Fatalf("DeliverOutbound error: %v", err)
	}
	if gotPath != "/api/v4/posts" || gotAuth != "Bearer tok" || gotContentType != "application/json" || gotBody["channel_id"] != "C1" || gotBody["message"] != "hello" {
		t.Fatalf("path=%q auth=%q contentType=%q body=%v", gotPath, gotAuth, gotContentType, gotBody)
	}
}

func TestMattermostFetchBotUserID(t *testing.T) {
	t.Run("request creation error", func(t *testing.T) {
		if _, err := NewMattermost(MattermostConfig{OutboundURL: "://bad"}).fetchBotUserID(context.Background()); err == nil {
			t.Fatal("expected request error")
		}
	})

	t.Run("client error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := NewMattermost(MattermostConfig{OutboundURL: "http://127.0.0.1:1"}).fetchBotUserID(ctx); err == nil {
			t.Fatal("expected client error")
		}
	})

	t.Run("status error", func(t *testing.T) {
		srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			http.Error(rw, "bad auth", http.StatusUnauthorized)
		}))
		defer srv.Close()
		_, err := NewMattermost(MattermostConfig{OutboundURL: srv.URL}).fetchBotUserID(context.Background())
		if err == nil || !strings.Contains(err.Error(), "mattermost users/me status=401") {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("decode error", func(t *testing.T) {
		srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = rw.Write([]byte(`{`))
		}))
		defer srv.Close()
		if _, err := NewMattermost(MattermostConfig{OutboundURL: srv.URL}).fetchBotUserID(context.Background()); err == nil {
			t.Fatal("expected decode error")
		}
	})

	t.Run("empty id", func(t *testing.T) {
		srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = rw.Write([]byte(`{"id":" "}`))
		}))
		defer srv.Close()
		if _, err := NewMattermost(MattermostConfig{OutboundURL: srv.URL}).fetchBotUserID(context.Background()); err == nil {
			t.Fatal("expected empty id error")
		}
	})

	t.Run("success", func(t *testing.T) {
		var gotAuth string
		srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			_, _ = rw.Write([]byte(`{"id":" bot "}`))
		}))
		defer srv.Close()
		id, err := NewMattermost(MattermostConfig{OutboundURL: srv.URL, Token: " tok "}).fetchBotUserID(context.Background())
		if err != nil || id != "bot" || gotAuth != "Bearer tok" {
			t.Fatalf("id=%q auth=%q err=%v", id, gotAuth, err)
		}
	})
}

func TestMattermostConnectAndConsume(t *testing.T) {
	var gotAuth string
	var authMsg map[string]any
	srv := mattermostWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := conn.ReadJSON(&authMsg); err != nil {
			t.Errorf("read auth: %v", err)
			return
		}
		events := []string{
			`not json`,
			`{"event":"hello"}`,
			`{"event":"posted","data":{"post":" "}}`,
			`{"event":"posted","data":{"post":"{"}}`,
			mattermostPosted(`{"id":"p1","user_id":"bot","channel_id":"C1","message":"bot"}`),
			mattermostPosted(`{"id":"p2","user_id":"u","channel_id":"","message":"missing channel"}`),
			mattermostPosted(`{"id":"p3","user_id":"u","channel_id":"C1","message":" "}`),
			mattermostPosted(`{"id":"p4","user_id":"u","channel_id":"C1","message":"system","type":"system_join_channel"}`),
			mattermostPosted(`{"id":"p5","user_id":"u","channel_id":"C1","message":" hi "}`),
			mattermostPosted(`{"id":"p5","user_id":"u","channel_id":"C1","message":"dupe"}`),
		}
		for _, event := range events {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(event))
		}
		_ = conn.Close()
	})
	defer srv.Close()

	bus := &mattermostBus{err: errors.New("ignored")}
	m := NewMattermost(MattermostConfig{OutboundURL: srv.URL, Token: " tok "})
	m.bus = bus
	m.botUserID = "bot"
	err := m.connectAndConsume(context.Background())
	if err == nil {
		t.Fatal("expected read close error")
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("Authorization=%q", gotAuth)
	}
	if authMsg["action"] != "authentication_challenge" || authMsg["seq"].(float64) != 1 {
		t.Fatalf("authMsg=%v", authMsg)
	}
	if data := authMsg["data"].(map[string]any); data["token"] != "tok" {
		t.Fatalf("auth data=%v", data)
	}
	if len(bus.events) != 1 || bus.events[0].ChannelID != "mattermost" || bus.events[0].SessionID != "C1" || bus.events[0].Text != "hi" {
		t.Fatalf("events=%+v", bus.events)
	}
	if m.activeConnForTest() != nil {
		t.Fatal("connection should be cleared")
	}
}

func TestMattermostConnectAndConsumeHTTPSBase(t *testing.T) {
	oldTLS := websocket.DefaultDialer.TLSClientConfig
	t.Cleanup(func() { websocket.DefaultDialer.TLSClientConfig = oldTLS })
	websocket.DefaultDialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	var gotAuth string
	srv := mattermostTLSServer(t, func(conn *websocket.Conn, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var auth map[string]any
		_ = conn.ReadJSON(&auth)
		_ = conn.Close()
	})
	defer srv.Close()

	m := NewMattermost(MattermostConfig{OutboundURL: srv.URL, Token: " tok "})
	if err := m.connectAndConsume(context.Background()); err == nil {
		t.Fatal("expected close read error")
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("Authorization=%q", gotAuth)
	}
}

func TestMattermostConnectAndConsumeErrorsAndCancel(t *testing.T) {
	if err := NewMattermost(MattermostConfig{OutboundURL: "%"}).connectAndConsume(context.Background()); err == nil {
		t.Fatal("expected parse error")
	}
	if err := NewMattermost(MattermostConfig{OutboundURL: "http://127.0.0.1:1"}).connectAndConsume(context.Background()); err == nil {
		t.Fatal("expected dial error")
	}

	closeBeforeAuth := mattermostWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		_ = conn.Close()
	})
	defer closeBeforeAuth.Close()
	if err := NewMattermost(MattermostConfig{OutboundURL: closeBeforeAuth.URL}).connectAndConsume(context.Background()); err == nil {
		t.Fatal("expected auth write error")
	}

	oldWriteJSON := mattermostWriteJSON
	t.Cleanup(func() { mattermostWriteJSON = oldWriteJSON })
	mattermostWriteJSON = func(*websocket.Conn, any) error { return errors.New("write failed") }
	writeFail := mattermostWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		<-time.After(50 * time.Millisecond)
	})
	defer writeFail.Close()
	m := NewMattermost(MattermostConfig{OutboundURL: writeFail.URL})
	m.wsConn = nil
	if err := m.connectAndConsume(context.Background()); err == nil {
		t.Fatal("expected write json error")
	}
	mattermostWriteJSON = oldWriteJSON

	connected := make(chan struct{})
	cancelSrv := mattermostWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		var auth map[string]any
		_ = conn.ReadJSON(&auth)
		_ = conn.WriteMessage(websocket.TextMessage, []byte(mattermostPosted(`{"id":"p1","user_id":"u","channel_id":"C1","message":"hi"}`)))
		<-time.After(50 * time.Millisecond)
		_ = conn.Close()
	})
	defer cancelSrv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	m = NewMattermost(MattermostConfig{OutboundURL: cancelSrv.URL})
	m.bus = &mattermostBus{onPublish: func() {
		close(connected)
		cancel()
	}}
	errCh := make(chan error, 1)
	go func() { errCh <- m.connectAndConsume(ctx) }()
	<-connected
	if err := <-errCh; err != nil {
		t.Fatalf("cancelled connectAndConsume error: %v", err)
	}
}

func TestMattermostLoopBackoffAndStop(t *testing.T) {
	oldAfter := mattermostAfter
	t.Cleanup(func() { mattermostAfter = oldAfter })

	var afterCalls atomic.Int64
	mattermostAfter = func(time.Duration) <-chan time.Time {
		afterCalls.Add(1)
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	m := NewMattermost(MattermostConfig{OutboundURL: "http://127.0.0.1:1"})
	ctx, cancel := context.WithCancel(context.Background())
	m.done = make(chan struct{})
	go m.loop(ctx)
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if afterCalls.Load() >= 2 {
			cancel()
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-m.done:
	case <-time.After(time.Second):
		t.Fatal("loop did not exit")
	}

	block := make(chan time.Time)
	mattermostAfter = func(time.Duration) <-chan time.Time { return block }
	m = NewMattermost(MattermostConfig{OutboundURL: "http://127.0.0.1:1"})
	ctx, cancel = context.WithCancel(context.Background())
	m.done = make(chan struct{})
	go m.loop(ctx)
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-m.done:
	case <-time.After(time.Second):
		t.Fatal("loop did not exit while backing off")
	}

	m = NewMattermost(MattermostConfig{})
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	m.done = make(chan struct{})
	m.loop(ctx)
	select {
	case <-m.done:
	default:
		t.Fatal("cancelled loop did not close done")
	}

	var successes atomic.Int64
	successSrv := mattermostWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		successes.Add(1)
		var auth map[string]any
		_ = conn.ReadJSON(&auth)
		_ = conn.Close()
	})
	defer successSrv.Close()
	mattermostAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	m = NewMattermost(MattermostConfig{OutboundURL: successSrv.URL})
	ctx, cancel = context.WithCancel(context.Background())
	m.done = make(chan struct{})
	go m.loop(ctx)
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if successes.Load() >= 2 {
			cancel()
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-m.done:
	case <-time.After(time.Second):
		t.Fatal("success loop did not exit")
	}
	if successes.Load() < 2 {
		t.Fatalf("successes=%d", successes.Load())
	}

	var normalReturns atomic.Int64
	normalSrv := mattermostWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		normalReturns.Add(1)
		var auth map[string]any
		_ = conn.ReadJSON(&auth)
		_ = conn.WriteMessage(websocket.TextMessage, []byte(mattermostPosted(`{"id":"p1","user_id":"u","channel_id":"C1","message":"hi"}`)))
		<-time.After(20 * time.Millisecond)
		_ = conn.Close()
	})
	defer normalSrv.Close()
	ctx, cancel = context.WithCancel(context.Background())
	m = NewMattermost(MattermostConfig{OutboundURL: normalSrv.URL})
	m.bus = &mattermostBus{onPublish: cancel}
	m.done = make(chan struct{})
	go m.loop(ctx)
	select {
	case <-m.done:
	case <-time.After(time.Second):
		t.Fatal("normal-return loop did not exit")
	}
	if normalReturns.Load() == 0 {
		t.Fatal("normal-return loop did not connect")
	}
}

func TestMattermostIsDuplicateAndNormalizeBase(t *testing.T) {
	m := NewMattermost(MattermostConfig{})
	if m.isDuplicate(" ") {
		t.Fatal("blank id should not be duplicate")
	}
	if m.isDuplicate(" p1 ") {
		t.Fatal("first p1 should not be duplicate")
	}
	if !m.isDuplicate("p1") {
		t.Fatal("second p1 should be duplicate")
	}
	m.seen["old"] = time.Now().Add(-10 * time.Minute)
	if m.isDuplicate("p2") {
		t.Fatal("first p2 should not be duplicate")
	}
	if _, ok := m.seen["old"]; ok {
		t.Fatal("old seen entry was not pruned")
	}

	tests := []struct {
		in   string
		want string
	}{
		{in: " https://mm.example/api/v4/ ", want: "https://mm.example"},
		{in: "https://mm.example/", want: "https://mm.example"},
		{in: " ", want: ""},
	}
	for _, tt := range tests {
		if got := normalizeMattermostBase(tt.in); got != tt.want {
			t.Fatalf("normalizeMattermostBase(%q)=%q want %q", tt.in, got, tt.want)
		}
	}
}

func mattermostPosted(post string) string {
	raw, _ := json.Marshal(post)
	return `{"event":"posted","data":{"post":` + string(raw) + `}}`
}

func mattermostWSServer(t *testing.T, fn func(*websocket.Conn, *http.Request)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	return testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(rw, r, nil)
		if err != nil {
			return
		}
		fn(conn, r)
	}))
}

func mattermostTLSServer(t *testing.T, fn func(*websocket.Conn, *http.Request)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	return httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(rw, r, nil)
		if err != nil {
			return
		}
		fn(conn, r)
	}))
}

func dialMattermostWS(t *testing.T, raw string) *websocket.Conn {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	u.Scheme = strings.Replace(u.Scheme, "http", "ws", 1)
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	return conn
}

func (m *Mattermost) activeConnForTest() *websocket.Conn {
	m.wsMu.Lock()
	defer m.wsMu.Unlock()
	return m.wsConn
}

type mattermostBus struct {
	events    []Inbound
	err       error
	onPublish func()
}

func (b *mattermostBus) PublishInbound(_ context.Context, in Inbound) error {
	b.events = append(b.events, in)
	if b.onPublish != nil {
		b.onPublish()
	}
	return b.err
}

var _ = io.EOF
var _ = url.URL{}
