package channel

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/testutil"
	"github.com/gorilla/websocket"
)

func TestHomeAssistantNewIDStartStopRegisterAndHelpers(t *testing.T) {
	h := NewHomeAssistant(HomeAssistantConfig{})
	if h == nil || h.ID() != "homeassistant" || h.httpClient == nil || h.httpClient.Timeout == 0 {
		t.Fatalf("homeassistant=%#v", h)
	}
	if err := h.Start(context.Background(), nil, &haBus{}); err != nil {
		t.Fatalf("disabled Start error: %v", err)
	}
	if err := NewHomeAssistant(HomeAssistantConfig{Enabled: true}).Start(context.Background(), nil, nil); err == nil {
		t.Fatal("expected missing outbound url error")
	}
	if normalizeHAURL(" http://ha.local/ ") != "http://ha.local" {
		t.Fatal("normalizeHAURL mismatch")
	}
	if truncateHAText("hello", 10) != "hello" || truncateHAText("hello", 2) != "he" {
		t.Fatal("truncateHAText mismatch")
	}
	id := &wsID{}
	if id.Next() != 1 || id.Next() != 2 {
		t.Fatal("wsID did not increment")
	}

	oldAfter := haAfter
	t.Cleanup(func() { haAfter = oldAfter })
	haAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	if err := NewHomeAssistant(HomeAssistantConfig{}).Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
}

func TestHomeAssistantStartStopRunningAndCloseActiveConn(t *testing.T) {
	oldAfter := haAfter
	t.Cleanup(func() { haAfter = oldAfter })
	haAfter = func(time.Duration) <-chan time.Time {
		return make(chan time.Time)
	}

	connected := make(chan struct{})
	srv := haWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		close(connected)
		_ = conn.WriteJSON(map[string]string{"type": "auth_required"})
		var auth map[string]any
		_ = conn.ReadJSON(&auth)
		_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
		var sub map[string]any
		_ = conn.ReadJSON(&sub)
		<-time.After(200 * time.Millisecond)
		_ = conn.Close()
	})
	defer srv.Close()

	h := NewHomeAssistant(HomeAssistantConfig{Enabled: true, OutboundURL: srv.URL, Token: "tok"})
	if err := h.Start(context.Background(), nil, &haBus{}); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("server was not connected")
	}
	if err := h.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
	if h.activeConnForTest() != nil {
		t.Fatal("Stop did not clear active connection")
	}
}

func TestHomeAssistantDeliverOutbound(t *testing.T) {
	if err := NewHomeAssistant(HomeAssistantConfig{OutboundURL: "://bad"}).DeliverOutbound(context.Background(), Outbound{}); err == nil {
		t.Fatal("expected request error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewHomeAssistant(HomeAssistantConfig{OutboundURL: "http://127.0.0.1:1"}).DeliverOutbound(ctx, Outbound{}); err == nil {
		t.Fatal("expected client error")
	}

	bad := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, strings.Repeat("x", 2000), http.StatusBadGateway)
	}))
	defer bad.Close()
	err := NewHomeAssistant(HomeAssistantConfig{OutboundURL: bad.URL}).DeliverOutbound(context.Background(), Outbound{})
	if err == nil || !strings.Contains(err.Error(), "homeassistant send status=502") || len(err.Error()) > 1150 {
		t.Fatalf("status error=%v", err)
	}

	var gotPath, gotAuth, gotContentType string
	var gotBody map[string]string
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	long := strings.Repeat("x", 5000)
	if err := NewHomeAssistant(HomeAssistantConfig{OutboundURL: srv.URL, Token: " tok "}).DeliverOutbound(context.Background(), Outbound{Text: " " + long + " "}); err != nil {
		t.Fatalf("DeliverOutbound error: %v", err)
	}
	if gotPath != "/api/services/persistent_notification/create" || gotAuth != "Bearer tok" || gotContentType != "application/json" {
		t.Fatalf("path=%q auth=%q contentType=%q", gotPath, gotAuth, gotContentType)
	}
	if gotBody["title"] != "Forebrain Harness" || len(gotBody["message"]) != 4096 {
		t.Fatalf("body=%v messageLen=%d", gotBody, len(gotBody["message"]))
	}

	noToken := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization=%q, want empty", got)
		}
		rw.WriteHeader(http.StatusOK)
	}))
	defer noToken.Close()
	if err := NewHomeAssistant(HomeAssistantConfig{OutboundURL: noToken.URL}).DeliverOutbound(context.Background(), Outbound{}); err != nil {
		t.Fatalf("no-token DeliverOutbound error: %v", err)
	}
}

func TestHomeAssistantConnectAndConsume(t *testing.T) {
	var gotAuth map[string]any
	var gotSub map[string]any
	srv := haWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		_ = conn.WriteJSON(map[string]string{"type": "auth_required"})
		if err := conn.ReadJSON(&gotAuth); err != nil {
			t.Errorf("read auth: %v", err)
			return
		}
		_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
		if err := conn.ReadJSON(&gotSub); err != nil {
			t.Errorf("read subscribe: %v", err)
			return
		}
		events := []any{
			map[string]any{"type": "result"},
			map[string]any{"type": "event"},
			map[string]any{"type": "event", "event": map[string]any{"data": map[string]any{"entity_id": "sensor.temp", "old_state": map[string]any{"state": "on"}, "new_state": map[string]any{"state": "on"}}}},
			map[string]any{"type": "event", "event": map[string]any{"data": map[string]any{"entity_id": "light.kitchen", "old_state": map[string]any{"state": "off"}, "new_state": map[string]any{"state": "on", "attributes": map[string]any{"friendly_name": "Kitchen"}}}}},
			map[string]any{"type": "event", "event": map[string]any{"data": map[string]any{"entity_id": "switch.fan", "old_state": map[string]any{"state": "off"}, "new_state": map[string]any{"state": "on"}}}},
		}
		for _, event := range events {
			_ = conn.WriteJSON(event)
		}
		_ = conn.Close()
	})
	defer srv.Close()

	bus := &haBus{err: errors.New("ignored")}
	h := NewHomeAssistant(HomeAssistantConfig{OutboundURL: srv.URL, Token: " tok "})
	h.bus = bus
	err := h.connectAndConsume(context.Background())
	if err == nil {
		t.Fatal("expected read close error")
	}
	if gotAuth["type"] != "auth" || gotAuth["access_token"] != "tok" {
		t.Fatalf("auth=%v", gotAuth)
	}
	if gotSub["type"] != "subscribe_events" || gotSub["event_type"] != "state_changed" || gotSub["id"].(float64) != 1 {
		t.Fatalf("subscribe=%v", gotSub)
	}
	if len(bus.events) != 2 {
		t.Fatalf("events=%+v", bus.events)
	}
	if !strings.Contains(bus.events[0].Text, "Kitchen (light.kitchen)") || bus.events[0].SessionID != "ha_events" || bus.events[0].ChannelID != "homeassistant" {
		t.Fatalf("event0=%+v", bus.events[0])
	}
	if !strings.Contains(bus.events[1].Text, "switch.fan (switch.fan)") {
		t.Fatalf("event1=%+v", bus.events[1])
	}
	if h.activeConnForTest() != nil {
		t.Fatal("connection should be cleared")
	}
}

func TestHomeAssistantConnectAndConsumeHTTPSAndErrors(t *testing.T) {
	if err := NewHomeAssistant(HomeAssistantConfig{OutboundURL: "http://127.0.0.1:1"}).connectAndConsume(context.Background()); err == nil {
		t.Fatal("expected dial error")
	}

	closeFirst := haWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		_ = conn.Close()
	})
	defer closeFirst.Close()
	if err := NewHomeAssistant(HomeAssistantConfig{OutboundURL: closeFirst.URL}).connectAndConsume(context.Background()); err == nil {
		t.Fatal("expected auth required read error")
	}

	badAuthRequired := haWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		_ = conn.WriteJSON(map[string]string{"type": "hello"})
		_ = conn.Close()
	})
	defer badAuthRequired.Close()
	if err := NewHomeAssistant(HomeAssistantConfig{OutboundURL: badAuthRequired.URL}).connectAndConsume(context.Background()); err == nil {
		t.Fatal("expected auth_required type error")
	}

	oldWrite := haWriteJSON
	t.Cleanup(func() { haWriteJSON = oldWrite })
	haWriteJSON = func(*websocket.Conn, any) error { return errors.New("write failed") }
	writeFail := haWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		_ = conn.WriteJSON(map[string]string{"type": "auth_required"})
		<-time.After(50 * time.Millisecond)
	})
	defer writeFail.Close()
	if err := NewHomeAssistant(HomeAssistantConfig{OutboundURL: writeFail.URL}).connectAndConsume(context.Background()); err == nil {
		t.Fatal("expected auth write error")
	}
	haWriteJSON = oldWrite

	authRespClose := haWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		_ = conn.WriteJSON(map[string]string{"type": "auth_required"})
		var auth map[string]any
		_ = conn.ReadJSON(&auth)
		_ = conn.Close()
	})
	defer authRespClose.Close()
	if err := NewHomeAssistant(HomeAssistantConfig{OutboundURL: authRespClose.URL}).connectAndConsume(context.Background()); err == nil {
		t.Fatal("expected auth response read error")
	}

	authFail := haWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		_ = conn.WriteJSON(map[string]string{"type": "auth_required"})
		var auth map[string]any
		_ = conn.ReadJSON(&auth)
		_ = conn.WriteJSON(map[string]string{"type": "auth_invalid"})
	})
	defer authFail.Close()
	if err := NewHomeAssistant(HomeAssistantConfig{OutboundURL: authFail.URL}).connectAndConsume(context.Background()); err == nil {
		t.Fatal("expected auth failed error")
	}

	haWriteJSON = func(conn *websocket.Conn, v any) error {
		if body, ok := v.(map[string]any); ok && body["type"] == "subscribe_events" {
			return errors.New("subscribe failed")
		}
		return conn.WriteJSON(v)
	}
	subFail := haWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		_ = conn.WriteJSON(map[string]string{"type": "auth_required"})
		var auth map[string]any
		_ = conn.ReadJSON(&auth)
		_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
		<-time.After(50 * time.Millisecond)
	})
	defer subFail.Close()
	if err := NewHomeAssistant(HomeAssistantConfig{OutboundURL: subFail.URL}).connectAndConsume(context.Background()); err == nil {
		t.Fatal("expected subscribe write error")
	}
	haWriteJSON = oldWrite

	oldTLS := websocket.DefaultDialer.TLSClientConfig
	t.Cleanup(func() { websocket.DefaultDialer.TLSClientConfig = oldTLS })
	websocket.DefaultDialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	tlsSrv := haTLSServer(t, func(conn *websocket.Conn, r *http.Request) {
		_ = conn.WriteJSON(map[string]string{"type": "auth_required"})
		var auth map[string]any
		_ = conn.ReadJSON(&auth)
		_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
		var sub map[string]any
		_ = conn.ReadJSON(&sub)
		_ = conn.Close()
	})
	defer tlsSrv.Close()
	if err := NewHomeAssistant(HomeAssistantConfig{OutboundURL: tlsSrv.URL}).connectAndConsume(context.Background()); err == nil {
		t.Fatal("expected tls close read error")
	}
}

func TestHomeAssistantConnectAndConsumeCancelAfterEvent(t *testing.T) {
	connected := make(chan struct{})
	srv := haWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		_ = conn.WriteJSON(map[string]string{"type": "auth_required"})
		var auth map[string]any
		_ = conn.ReadJSON(&auth)
		_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
		var sub map[string]any
		_ = conn.ReadJSON(&sub)
		_ = conn.WriteJSON(map[string]any{"type": "event", "event": map[string]any{"data": map[string]any{"entity_id": "sensor.one", "old_state": map[string]any{"state": "off"}, "new_state": map[string]any{"state": "on"}}}})
		<-time.After(50 * time.Millisecond)
		_ = conn.Close()
	})
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	h := NewHomeAssistant(HomeAssistantConfig{OutboundURL: srv.URL})
	h.bus = &haBus{onPublish: func() {
		close(connected)
		cancel()
	}}
	errCh := make(chan error, 1)
	go func() { errCh <- h.connectAndConsume(ctx) }()
	<-connected
	if err := <-errCh; err != nil {
		t.Fatalf("cancelled connectAndConsume error: %v", err)
	}
}

func TestHomeAssistantLoopBackoffAndStop(t *testing.T) {
	oldAfter := haAfter
	t.Cleanup(func() { haAfter = oldAfter })

	var afterCalls atomic.Int64
	haAfter = func(time.Duration) <-chan time.Time {
		afterCalls.Add(1)
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	h := NewHomeAssistant(HomeAssistantConfig{OutboundURL: "http://127.0.0.1:1"})
	ctx, cancel := context.WithCancel(context.Background())
	h.done = make(chan struct{})
	go h.loop(ctx)
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if afterCalls.Load() >= 2 {
			cancel()
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-h.done:
	case <-time.After(time.Second):
		t.Fatal("loop did not exit")
	}

	block := make(chan time.Time)
	haAfter = func(time.Duration) <-chan time.Time { return block }
	h = NewHomeAssistant(HomeAssistantConfig{OutboundURL: "http://127.0.0.1:1"})
	ctx, cancel = context.WithCancel(context.Background())
	h.done = make(chan struct{})
	go h.loop(ctx)
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-h.done:
	case <-time.After(time.Second):
		t.Fatal("loop did not exit during backoff")
	}

	h = NewHomeAssistant(HomeAssistantConfig{})
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	h.done = make(chan struct{})
	h.loop(ctx)
	select {
	case <-h.done:
	default:
		t.Fatal("cancelled loop did not close done")
	}

	normalSrv := haWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		_ = conn.WriteJSON(map[string]string{"type": "auth_required"})
		var auth map[string]any
		_ = conn.ReadJSON(&auth)
		_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
		var sub map[string]any
		_ = conn.ReadJSON(&sub)
		_ = conn.WriteJSON(map[string]any{"type": "event", "event": map[string]any{"data": map[string]any{"entity_id": "sensor.one", "old_state": map[string]any{"state": "off"}, "new_state": map[string]any{"state": "on"}}}})
		<-time.After(20 * time.Millisecond)
		_ = conn.Close()
	})
	defer normalSrv.Close()
	ctx, cancel = context.WithCancel(context.Background())
	h = NewHomeAssistant(HomeAssistantConfig{OutboundURL: normalSrv.URL})
	h.bus = &haBus{onPublish: cancel}
	h.done = make(chan struct{})
	go h.loop(ctx)
	select {
	case <-h.done:
	case <-time.After(time.Second):
		t.Fatal("normal-return loop did not exit")
	}
}

func haWSServer(t *testing.T, fn func(*websocket.Conn, *http.Request)) *httptest.Server {
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

func haTLSServer(t *testing.T, fn func(*websocket.Conn, *http.Request)) *httptest.Server {
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

func (h *HomeAssistant) activeConnForTest() *websocket.Conn {
	h.wsMu.Lock()
	defer h.wsMu.Unlock()
	return h.wsConn
}

type haBus struct {
	events    []Inbound
	err       error
	onPublish func()
}

func (b *haBus) PublishInbound(_ context.Context, in Inbound) error {
	b.events = append(b.events, in)
	if b.onPublish != nil {
		b.onPublish()
	}
	return b.err
}

var _ = io.EOF
var _ = url.URL{}

// slowStartHandler makes the window between "detach the old handlers" and
// "swap in the new ones" wide enough to observe. Start blocks for delay, which
// is what a real handler does while it opens a connection or registers a
// webhook.
type slowStartHandler struct {
	id      string
	delay   time.Duration
	starts  atomic.Int32
	stops   atomic.Int32
	stopped chan struct{}
	once    sync.Once
}

func newSlowHandler(id string, delay time.Duration) *slowStartHandler {
	return &slowStartHandler{id: id, delay: delay, stopped: make(chan struct{})}
}

func (h *slowStartHandler) ID() string { return h.id }

func (h *slowStartHandler) Start(_ context.Context, add RouteAdder, _ Bus) error {
	time.Sleep(h.delay)
	h.starts.Add(1)
	if add != nil {
		add(http.MethodPost, "/channels/"+h.id+"/inbound", func(http.ResponseWriter, *http.Request) {})
	}
	return nil
}

func (h *slowStartHandler) Stop(context.Context) error {
	h.stops.Add(1)
	h.once.Do(func() { close(h.stopped) })
	return nil
}

func (h *slowStartHandler) wasStopped() bool {
	select {
	case <-h.stopped:
		return true
	default:
		return false
	}
}

// TestConcurrentBindLeavesNoStartedHandlerUnstopped is P8-5's race test: an
// agent switch racing a rebind.
//
// Bind's whole reason to exist is that it replaces one agent's channels
// wholesale, so that an inbound message can never reach an agent other than the
// one whose bot received it. That invariant is "every handler Start returns for
// is either currently bound or has been Stopped". Bind starts handlers outside
// the data lock, so before the fix two concurrent binds interleaved as
// detach/detach/start+swap/start+swap and left the loser's handlers running and
// unreferenced — a bot poller for a retired agent that nothing would ever stop,
// feeding that tenant's messages into the live agent. The gateway reaches Bind
// concurrently for real: startup binds while a primary agent switch binds.
func TestConcurrentBindLeavesNoStartedHandlerUnstopped(t *testing.T) {
	r := NewRegistry()
	slow := newSlowHandler("slow", 60*time.Millisecond)
	fast := newSlowHandler("fast", 0)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = r.Bind(context.Background(), "agent-slow", []Handler{slow}, nil) }()
	// Let the first bind get past its detach and into the start it will block
	// on, which is the window the interleaving needs.
	time.Sleep(10 * time.Millisecond)
	go func() { defer wg.Done(); _ = r.Bind(context.Background(), "agent-fast", []Handler{fast}, nil) }()
	wg.Wait()

	bound := map[string]bool{}
	for _, h := range r.All() {
		bound[h.ID()] = true
	}
	if len(bound) != 1 {
		t.Fatalf("registry holds %d handlers after two binds, want exactly 1 (a bind replaces wholesale)", len(bound))
	}
	for _, h := range []*slowStartHandler{slow, fast} {
		if h.starts.Load() == 0 || bound[h.id] {
			continue
		}
		if !h.wasStopped() {
			t.Fatalf("handler %q was started, is not bound, and was never stopped: it leaked", h.id)
		}
	}
	// The surviving handler must also still be routable, so serializing binds
	// did not cost the winner its routes.
	for id := range bound {
		if _, ok := r.Route(http.MethodPost, "/channels/"+id+"/inbound"); !ok {
			t.Fatalf("bound handler %q has no route: the winning bind lost its route table", id)
		}
	}
}

// Stop racing Bind is the shutdown-during-agent-switch case, and has the same
// invariant: nothing may be left started but unreferenced.
func TestConcurrentBindAndStopLeaveNothingRunning(t *testing.T) {
	r := NewRegistry()
	h := newSlowHandler("slow", 40*time.Millisecond)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = r.Bind(context.Background(), "agent-1", []Handler{h}, nil) }()
	time.Sleep(5 * time.Millisecond)
	go func() { defer wg.Done(); _ = r.Stop(context.Background()) }()
	wg.Wait()

	if len(r.All()) == 0 && h.starts.Load() > 0 && !h.wasStopped() {
		t.Fatal("handler was started, is not bound, and was never stopped: it leaked")
	}
}

func twoAgentConfig() *appcfg.Root {
	cfg := &appcfg.Root{}
	appcfg.SetChannelsForAgent(cfg, "main", appcfg.ChannelsSection{
		Slack: appcfg.Slack{Enabled: true, InboundPath: "/main/slack", BotToken: "main-token"},
	})
	appcfg.SetChannelsForAgent(cfg, "acme", appcfg.ChannelsSection{
		Slack: appcfg.Slack{Enabled: true, InboundPath: "/acme/slack", BotToken: "acme-token"},
	})
	def := cfg.Agents.Definitions["acme"]
	def.Primary = true
	cfg.Agents.Definitions["acme"] = def
	return cfg
}

// The handlers built for an agent carry that agent's settings only. Mounting
// them therefore exposes that agent's endpoints and no other's.
func TestBuildAgentChannelsUsesTheNamedAgentsConfiguration(t *testing.T) {
	cfg := twoAgentConfig()
	reg := NewRegistry()

	if err := reg.Bind(context.Background(), "main", BuildAgentChannels(cfg, "main", t.TempDir()), nil); err != nil {
		t.Fatalf("bind main: %v", err)
	}
	if _, ok := reg.Route(http.MethodPost, "/main/slack"); !ok {
		t.Fatal("main's slack inbound path is not mounted")
	}
	if _, ok := reg.Route(http.MethodPost, "/acme/slack"); ok {
		t.Fatal("another agent's slack inbound path is mounted")
	}

	if err := reg.Bind(context.Background(), "acme", BuildAgentChannels(cfg, "acme", t.TempDir()), nil); err != nil {
		t.Fatalf("bind acme: %v", err)
	}
	if _, ok := reg.Route(http.MethodPost, "/acme/slack"); !ok {
		t.Fatal("acme's slack inbound path is not mounted after the switch")
	}
	if _, ok := reg.Route(http.MethodPost, "/main/slack"); ok {
		t.Fatal("main's slack inbound path survived the switch")
	}
}

// An agent that configured nothing gets handlers that are all disabled, so it
// mounts no endpoints — rather than inheriting whatever main configured.
func TestBuildAgentChannelsForUnconfiguredAgentMountsNothing(t *testing.T) {
	cfg := twoAgentConfig()
	reg := NewRegistry()

	if err := reg.Bind(context.Background(), "quiet", BuildAgentChannels(cfg, "quiet", t.TempDir()), nil); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if _, ok := reg.Route(http.MethodPost, "/main/slack"); ok {
		t.Fatal("an unconfigured agent inherited main's endpoints")
	}
}

func TestBuildAgentChannelsRequiresConfigAndAgent(t *testing.T) {
	if got := BuildAgentChannels(nil, "main", ""); got != nil {
		t.Fatalf("nil config produced handlers: %#v", got)
	}
	if got := BuildAgentChannels(twoAgentConfig(), " ", ""); got != nil {
		t.Fatalf("blank agent produced handlers: %#v", got)
	}
}

func TestDeliveryTimeout(t *testing.T) {
	if DefaultDeliveryTimeout <= 0 {
		t.Fatal("delivery timeout must be positive")
	}
	if DefaultDeliveryTimeout > 30*time.Second {
		t.Fatal("delivery timeout too long")
	}
}

// recordingSender is an outbound-capable handler that captures exactly what the
// registry handed it, so the tests below can assert on the projection rather
// than only on whether a send happened.
type recordingSender struct {
	id       string
	got      []Outbound
	sendErr  error
	deadline bool
}

func (s *recordingSender) ID() string { return s.id }
func (s *recordingSender) Start(context.Context, RouteAdder, Bus) error {
	return nil
}
func (s *recordingSender) Stop(context.Context) error { return nil }

func (s *recordingSender) DeliverOutbound(ctx context.Context, o Outbound) error {
	s.got = append(s.got, o)
	if s.deadline {
		if _, ok := ctx.Deadline(); !ok {
			return errors.New("send context carried no deadline")
		}
	}
	return s.sendErr
}

func bindOne(t *testing.T, h Handler) *Registry {
	t.Helper()
	r := NewRegistry()
	if err := r.Bind(context.Background(), "agent-1", []Handler{h}, nil); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	return r
}

// Deliver is the canonical outbound path P8-3 moved out of the gateway. The
// registry is the only thing that knows which handlers the active agent has
// bound, so resolving a channel ID to a sender has to happen here.
func TestRegistryDeliverRoutesToTheHandlerOwningTheChannelID(t *testing.T) {
	want := &recordingSender{id: "telegram", deadline: true}
	other := &recordingSender{id: "slack"}
	r := NewRegistry()
	if err := r.Bind(context.Background(), "agent-1", []Handler{other, want}, nil); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if err := r.Deliver(context.Background(), Outbound{
		ChannelID: "telegram", SessionID: "user-1", Text: "  hello  ",
	}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if len(other.got) != 0 {
		t.Fatalf("the slack handler received %+v, want nothing: outbound must not fan out", other.got)
	}
	if len(want.got) != 1 {
		t.Fatalf("telegram handler received %d messages, want 1", len(want.got))
	}
	// Text is trimmed because every provider either rejects an empty body or
	// renders the padding; SessionID is passed through untouched because
	// unwrapping the channel prefix needs pkg/state, which this layer may not
	// import.
	if got := want.got[0]; got.Text != "hello" || got.SessionID != "user-1" || got.ChannelID != "telegram" {
		t.Fatalf("delivered %+v, want {telegram user-1 hello}", got)
	}
}

// A send failure has to reach the caller: the gateway logs it, and swallowing
// it here would make a channel that stopped delivering completely invisible.
func TestRegistryDeliverReturnsTheSenderError(t *testing.T) {
	boom := errors.New("provider rejected the message")
	h := &recordingSender{id: "telegram", sendErr: boom}
	r := bindOne(t, h)

	err := r.Deliver(context.Background(), Outbound{ChannelID: "telegram", Text: "hi"})
	if !errors.Is(err, boom) {
		t.Fatalf("Deliver error = %v, want the sender's error %v", err, boom)
	}
}

// Both of these are outcomes rather than faults, and the gateway distinguishes
// them from a real failure by these sentinels. If they collapsed into a generic
// error the gateway would start logging an error every time an agent switch
// retired a channel mid-turn.
func TestRegistryDeliverDistinguishesUnboundFromInboundOnly(t *testing.T) {
	inboundOnly := &stubHandler{id: "webhook"}
	r := bindOne(t, inboundOnly)

	if err := r.Deliver(context.Background(), Outbound{ChannelID: "webhook", Text: "hi"}); !errors.Is(err, ErrNotOutbound) {
		t.Fatalf("Deliver to an inbound-only channel = %v, want ErrNotOutbound", err)
	}
	if err := r.Deliver(context.Background(), Outbound{ChannelID: "gone", Text: "hi"}); !errors.Is(err, ErrNoSuchChannel) {
		t.Fatalf("Deliver to an unbound channel = %v, want ErrNoSuchChannel", err)
	}
}

// An empty body is dropped before a provider ever sees it, and a nil registry
// is the pre-bind state rather than a bug, so neither reaches a handler.
func TestRegistryDeliverDropsEmptyMessages(t *testing.T) {
	h := &recordingSender{id: "telegram"}
	r := bindOne(t, h)

	for _, out := range []Outbound{
		{ChannelID: "telegram", Text: "   "},
		{ChannelID: "telegram", Text: ""},
		{ChannelID: "  ", Text: "hi"},
	} {
		if err := r.Deliver(context.Background(), out); err != nil {
			t.Fatalf("Deliver(%+v) = %v, want nil", out, err)
		}
	}
	if len(h.got) != 0 {
		t.Fatalf("handler received %+v, want nothing", h.got)
	}
	if err := (*Registry)(nil).Deliver(context.Background(), Outbound{ChannelID: "telegram", Text: "hi"}); err != nil {
		t.Fatalf("Deliver on a nil registry = %v, want nil", err)
	}
}

func TestEmailNewIDStartStopAndRegister(t *testing.T) {
	e := NewEmail(EmailConfig{})
	if e == nil || e.ID() != "email" {
		t.Fatalf("email=%#v", e)
	}
	if err := e.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}

	routes := map[string]http.HandlerFunc{}
	add := func(method, path string, hf http.HandlerFunc) {
		routes[method+" "+path] = hf
	}
	bus := &emailBus{}
	if err := NewEmail(EmailConfig{Enabled: false, InboundPath: "/email"}).Start(context.Background(), add, bus); err != nil {
		t.Fatalf("disabled Start error: %v", err)
	}
	if len(routes) != 0 {
		t.Fatalf("disabled routes=%v", routes)
	}

	enabled := NewEmail(EmailConfig{Enabled: true, InboundPath: " /email "})
	if err := enabled.Start(context.Background(), add, bus); err != nil {
		t.Fatalf("enabled Start error: %v", err)
	}
	if enabled.bus != bus || routes["POST /email"] == nil {
		t.Fatalf("bus=%#v routes=%v", enabled.bus, routes)
	}

	routes = map[string]http.HandlerFunc{}
	if err := NewEmail(EmailConfig{Enabled: true}).Start(context.Background(), add, bus); err != nil {
		t.Fatalf("empty path Start error: %v", err)
	}
	if len(routes) != 0 {
		t.Fatalf("empty path routes=%v", routes)
	}

}

func TestEmailDeliverOutbound(t *testing.T) {
	if err := NewEmail(EmailConfig{}).DeliverOutbound(context.Background(), Outbound{SessionID: "mailto: "}); err == nil {
		t.Fatal("expected invalid session error")
	}
	if err := NewEmail(EmailConfig{OutboundURL: "smtp://host:25", Token: "bad", Secret: "pass"}).DeliverOutbound(context.Background(), Outbound{SessionID: "a@example.com"}); err == nil {
		t.Fatal("expected invalid from address")
	}
	if err := NewEmail(EmailConfig{OutboundURL: "smtp://host", Token: "from@example.com", Secret: "pass"}).DeliverOutbound(context.Background(), Outbound{SessionID: "a@example.com"}); err == nil {
		t.Fatal("expected missing port")
	}
	if err := NewEmail(EmailConfig{OutboundURL: "smtp://host:25", Token: "from@example.com"}).DeliverOutbound(context.Background(), Outbound{SessionID: "a@example.com"}); err == nil {
		t.Fatal("expected missing password")
	}

	orig := sendMail
	t.Cleanup(func() { sendMail = orig })
	var gotAddr string
	var gotFrom string
	var gotTo []string
	var gotMsg string
	sendMail = func(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
		if auth == nil {
			t.Fatal("auth is nil")
		}
		gotAddr = addr
		gotFrom = from
		gotTo = append([]string(nil), to...)
		gotMsg = string(msg)
		return nil
	}
	err := NewEmail(EmailConfig{OutboundURL: "smtp://user@mail.example:2525", Token: " From <from@example.com> ", Secret: " pass "}).DeliverOutbound(
		context.Background(),
		Outbound{SessionID: " mailto:to@example.com ", Text: " hello "},
	)
	if err != nil {
		t.Fatalf("DeliverOutbound error: %v", err)
	}
	if gotAddr != "mail.example:2525" || gotFrom != "from@example.com" || len(gotTo) != 1 || gotTo[0] != "to@example.com" {
		t.Fatalf("addr=%q from=%q to=%v", gotAddr, gotFrom, gotTo)
	}
	for _, want := range []string{"From: from@example.com", "To: to@example.com", "Subject: Forebrain Harness Reply", "\r\n\r\nhello\r\n"} {
		if !strings.Contains(gotMsg, want) {
			t.Fatalf("message missing %q in %q", want, gotMsg)
		}
	}

	sendMail = func(string, smtp.Auth, string, []string, []byte) error {
		return errors.New("send failed")
	}
	err = NewEmail(EmailConfig{OutboundURL: "smtp://mail.example:2525", Token: "from@example.com", Secret: "pass"}).DeliverOutbound(context.Background(), Outbound{SessionID: "to@example.com"})
	if err == nil || !strings.Contains(err.Error(), "send failed") {
		t.Fatalf("send error=%v", err)
	}
}

func TestEmailHandleInbound(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		body       string
		wantCode   int
		wantEvents int
		wantSID    string
		wantText   string
	}{
		{name: "method", method: http.MethodGet, wantCode: http.StatusMethodNotAllowed},
		{name: "bad json", method: http.MethodPost, body: "{", wantCode: http.StatusBadRequest},
		{name: "missing from", method: http.MethodPost, body: `{"subject":"s","text":"t"}`, wantCode: http.StatusNoContent},
		{name: "missing text", method: http.MethodPost, body: `{"from":"a@example.com","subject":" ","text":" "}`, wantCode: http.StatusNoContent},
		{name: "from and content", method: http.MethodPost, body: `{"from":" a@example.com ","subject":" subject ","text":" body "}`, wantCode: http.StatusNoContent, wantEvents: 1, wantSID: "a@example.com", wantText: "subject\nbody"},
		{name: "fallback keys", method: http.MethodPost, body: `{"sender":"b@example.com","body":"body"}`, wantCode: http.StatusNoContent, wantEvents: 1, wantSID: "b@example.com", wantText: "body"},
		{name: "session key and content", method: http.MethodPost, body: `{"session_id":"c@example.com","content":"content"}`, wantCode: http.StatusNoContent, wantEvents: 1, wantSID: "c@example.com", wantText: "content"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bus := &emailBus{err: errors.New("ignored")}
			e := NewEmail(EmailConfig{})
			e.bus = bus
			req := httptest.NewRequest(tt.method, "/email", strings.NewReader(tt.body))
			rr := httptest.NewRecorder()
			e.handleInbound(rr, req)
			if rr.Code != tt.wantCode {
				t.Fatalf("code=%d body=%q", rr.Code, rr.Body.String())
			}
			if len(bus.events) != tt.wantEvents {
				t.Fatalf("events=%+v", bus.events)
			}
			if tt.wantEvents == 1 {
				ev := bus.events[0]
				if ev.ChannelID != "email" || ev.SessionID != tt.wantSID || ev.Text != tt.wantText {
					t.Fatalf("event=%+v", ev)
				}
			}
		})
	}
}

func TestEmailParseSMTPConfigParseURLChooseAndStrAny(t *testing.T) {
	host, port, user, pass, from, err := parseSMTPConfig(EmailConfig{OutboundURL: "mail.example:25", Token: "from@example.com", Secret: " pass "})
	if err != nil {
		t.Fatalf("parseSMTPConfig error: %v", err)
	}
	if host != "mail.example" || port != "25" || user != "from@example.com" || pass != "pass" || from != "from@example.com" {
		t.Fatalf("smtp config host=%q port=%q user=%q pass=%q from=%q", host, port, user, pass, from)
	}
	if _, err := parseURL(" "); err == nil {
		t.Fatal("expected empty parseURL error")
	}
	parsed, err := parseURL("smtp:// user @ host : 2525 ")
	if err != nil {
		t.Fatalf("parseURL error: %v", err)
	}
	if parsed.User != "user" || parsed.Host != "host" || parsed.Port != "2525" {
		t.Fatalf("parsed=%+v", parsed)
	}
	if choose(" a ", "b") != "a" || choose(" ", " b ") != "b" {
		t.Fatal("choose returned unexpected value")
	}
	got := strAny(map[string]any{"a": 1, "b": " ", "c": " value "}, "a", "b", "c")
	if got != "value" {
		t.Fatalf("strAny=%q", got)
	}
	if got := strAny(map[string]any{"a": 1}, "a", "missing"); got != "" {
		t.Fatalf("missing strAny=%q", got)
	}
}

type emailBus struct {
	events []Inbound
	err    error
}

func (e *emailBus) PublishInbound(_ context.Context, m Inbound) error {
	e.events = append(e.events, m)
	return e.err
}

func TestSignalNewIDStartStopAndRegister(t *testing.T) {
	s := NewSignal(SignalConfig{})
	if s == nil || s.ID() != "signal" || s.httpClient == nil || s.httpClient.Timeout == 0 {
		t.Fatalf("signal=%#v", s)
	}
	if err := s.Start(context.Background(), nil, &signalBus{}); err != nil {
		t.Fatalf("disabled Start error: %v", err)
	}
	if err := NewSignal(SignalConfig{Enabled: true}).Start(context.Background(), nil, nil); err == nil {
		t.Fatal("expected missing config error")
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
}

func TestSignalDeliverOutbound(t *testing.T) {
	if err := NewSignal(SignalConfig{}).DeliverOutbound(context.Background(), Outbound{SessionID: " "}); err == nil {
		t.Fatal("expected invalid session error")
	}
	if err := NewSignal(SignalConfig{OutboundURL: "://bad"}).DeliverOutbound(context.Background(), Outbound{SessionID: "+1:thread"}); err == nil {
		t.Fatal("expected request error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewSignal(SignalConfig{OutboundURL: "http://127.0.0.1:1"}).DeliverOutbound(ctx, Outbound{SessionID: "+1"}); err == nil {
		t.Fatal("expected client error")
	}

	bad := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, strings.Repeat("x", 3000), http.StatusBadGateway)
	}))
	defer bad.Close()
	err := NewSignal(SignalConfig{OutboundURL: bad.URL}).DeliverOutbound(context.Background(), Outbound{SessionID: "+1"})
	if err == nil || !strings.Contains(err.Error(), "signal send status=502") || len(err.Error()) > 2200 {
		t.Fatalf("status error=%v", err)
	}

	apiError := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(rw).Encode(map[string]any{"error": map[string]any{"message": "failed"}})
	}))
	defer apiError.Close()
	err = NewSignal(SignalConfig{OutboundURL: apiError.URL}).DeliverOutbound(context.Background(), Outbound{SessionID: "+1"})
	if err == nil || !strings.Contains(err.Error(), "signal send error") {
		t.Fatalf("api error=%v", err)
	}

	badJSON := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write([]byte(`{`))
	}))
	defer badJSON.Close()
	if err := NewSignal(SignalConfig{OutboundURL: badJSON.URL}).DeliverOutbound(context.Background(), Outbound{SessionID: "+1"}); err != nil {
		t.Fatalf("decode errors are ignored, got %v", err)
	}

	var gotPath, gotContentType string
	var gotBody map[string]any
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"result": "ok"})
	}))
	defer srv.Close()
	err = NewSignal(SignalConfig{OutboundURL: srv.URL + "/", Token: " account "}).DeliverOutbound(context.Background(), Outbound{SessionID: " +123:chat ", Text: " hi "})
	if err != nil {
		t.Fatalf("DeliverOutbound error: %v", err)
	}
	if gotPath != "/api/v1/rpc" || gotContentType != "application/json" || gotBody["jsonrpc"] != "2.0" || gotBody["method"] != "send" {
		t.Fatalf("path=%q contentType=%q body=%v", gotPath, gotContentType, gotBody)
	}
	params := gotBody["params"].(map[string]any)
	recipients := params["recipient"].([]any)
	if params["account"] != "account" || params["message"] != "hi" || len(recipients) != 1 || recipients[0] != "+123" {
		t.Fatalf("params=%v", params)
	}
}

func TestSignalConsumeSSE(t *testing.T) {
	var gotPath, gotAccount string
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccount = r.URL.Query().Get("account")
		rw.Header().Set("Content-Type", "text/event-stream")
		_, _ = rw.Write([]byte("event: message\n"))
		_, _ = rw.Write([]byte(`data: {"envelope":{"sourceNumber":"+1","dataMessage":{"message":" hi "}}}` + "\n\n"))
		_, _ = rw.Write([]byte(`data: {"envelope":{"source":"fallback","dataMessage":{"message":"hello"}}}` + "\n\n"))
		_, _ = rw.Write([]byte(`data: {"envelope":{"source":"+2","dataMessage":{"message":" "}}}` + "\n\n"))
	}))
	defer srv.Close()

	bus := &signalBus{err: errors.New("ignored")}
	s := NewSignal(SignalConfig{OutboundURL: srv.URL, Token: " +account "})
	s.bus = bus
	if err := s.consumeSSE(context.Background()); err != nil {
		t.Fatalf("consumeSSE error: %v", err)
	}
	if gotPath != "/api/v1/events" || gotAccount != "+account" {
		t.Fatalf("path=%q account=%q", gotPath, gotAccount)
	}
	if len(bus.events) != 2 {
		t.Fatalf("events=%+v", bus.events)
	}
	if bus.events[0].ChannelID != "signal" || bus.events[0].SessionID != "+1" || bus.events[0].Text != "hi" || bus.events[0].Raw == nil {
		t.Fatalf("event0=%+v", bus.events[0])
	}
	if bus.events[1].SessionID != "fallback" || bus.events[1].Text != "hello" {
		t.Fatalf("event1=%+v", bus.events[1])
	}
}

func TestSignalConsumeSSEErrors(t *testing.T) {
	if err := NewSignal(SignalConfig{OutboundURL: "://bad"}).consumeSSE(context.Background()); err == nil {
		t.Fatal("expected request error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewSignal(SignalConfig{OutboundURL: "http://127.0.0.1:1"}).consumeSSE(ctx); err == nil {
		t.Fatal("expected client error")
	}
	bad := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, "bad events", http.StatusTooManyRequests)
	}))
	defer bad.Close()
	err := NewSignal(SignalConfig{OutboundURL: bad.URL}).consumeSSE(context.Background())
	if err == nil || !strings.Contains(err.Error(), "signal events status=429") {
		t.Fatalf("status error=%v", err)
	}

	tooLong := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write([]byte("data: " + strings.Repeat("x", 3*1024*1024) + "\n"))
	}))
	defer tooLong.Close()
	if err := NewSignal(SignalConfig{OutboundURL: tooLong.URL}).consumeSSE(context.Background()); err == nil {
		t.Fatal("expected scanner error")
	}
}

func TestSignalHandleEventAndHelpers(t *testing.T) {
	s := NewSignal(SignalConfig{})
	s.handleEvent(context.Background(), "")
	s.handleEvent(context.Background(), "{")
	s.handleEvent(context.Background(), `{"envelope":{}}`)
	s.handleEvent(context.Background(), `{"envelope":{"source":"+1","dataMessage":{"message":"hi"}}}`)
	if s.bus != nil {
		t.Fatal("nil bus should stay nil")
	}
	s.bus = &signalBus{}
	s.handleEvent(context.Background(), "{")
	s.handleEvent(context.Background(), `{"envelope":{"sourceNumber":123,"dataMessage":{"message":456}}}`)
	if len(s.bus.(*signalBus).events) != 1 || s.bus.(*signalBus).events[0].SessionID != "123" || s.bus.(*signalBus).events[0].Text != "456" {
		t.Fatalf("events=%+v", s.bus.(*signalBus).events)
	}
	if parseSignalSession(" +1:thread ") != "+1" || parseSignalSession(" ") != "" {
		t.Fatal("parseSignalSession mismatch")
	}
	if asString("x") != "x" || asString(float64(12.5)) != "12.5" || asString(nil) != "" || asString(map[string]any{}) != "" {
		t.Fatal("asString mismatch")
	}
}

func TestSignalEventLoopBackoffAndStop(t *testing.T) {
	oldAfter := signalAfter
	t.Cleanup(func() { signalAfter = oldAfter })

	var afterCalls atomic.Int64
	signalAfter = func(time.Duration) <-chan time.Time {
		afterCalls.Add(1)
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	s := NewSignal(SignalConfig{OutboundURL: "http://127.0.0.1:1"})
	s.done = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go s.eventLoop(ctx)
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if afterCalls.Load() >= 2 {
			cancel()
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("eventLoop did not exit")
	}

	block := make(chan time.Time)
	signalAfter = func(time.Duration) <-chan time.Time { return block }
	s = NewSignal(SignalConfig{OutboundURL: "http://127.0.0.1:1"})
	s.done = make(chan struct{})
	ctx, cancel = context.WithCancel(context.Background())
	go s.eventLoop(ctx)
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("eventLoop did not exit during backoff")
	}

	s = NewSignal(SignalConfig{})
	s.done = make(chan struct{})
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	s.eventLoop(ctx)
	select {
	case <-s.done:
	default:
		t.Fatal("cancelled eventLoop did not close done")
	}

	normalSrv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/event-stream")
		_, _ = rw.Write([]byte(`data: {"envelope":{"source":"+1","dataMessage":{"message":"hi"}}}` + "\n\n"))
	}))
	defer normalSrv.Close()
	ctx, cancel = context.WithCancel(context.Background())
	s = NewSignal(SignalConfig{OutboundURL: normalSrv.URL})
	s.bus = &signalBus{onPublish: cancel}
	s.done = make(chan struct{})
	go s.eventLoop(ctx)
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("normal eventLoop did not exit")
	}
}

func TestSignalStartStopRunning(t *testing.T) {
	oldAfter := signalAfter
	t.Cleanup(func() { signalAfter = oldAfter })
	signalAfter = func(time.Duration) <-chan time.Time {
		return make(chan time.Time)
	}
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		<-time.After(50 * time.Millisecond)
	}))
	defer srv.Close()
	s := NewSignal(SignalConfig{Enabled: true, OutboundURL: srv.URL, Token: "account"})
	if err := s.Start(context.Background(), nil, &signalBus{}); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
}

type signalBus struct {
	events    []Inbound
	err       error
	onPublish func()
}

func (b *signalBus) PublishInbound(_ context.Context, in Inbound) error {
	b.events = append(b.events, in)
	if b.onPublish != nil {
		b.onPublish()
	}
	return b.err
}

func TestSMSNewIDStartStopAndRegister(t *testing.T) {
	s := NewSMS(SMSConfig{})
	if s == nil || s.ID() != "sms" || s.httpClient == nil || s.httpClient.Timeout == 0 {
		t.Fatalf("sms=%#v", s)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}

	routes := map[string]http.HandlerFunc{}
	add := func(method, path string, hf http.HandlerFunc) {
		routes[method+" "+path] = hf
	}
	bus := &smsBus{}
	if err := NewSMS(SMSConfig{Enabled: false, InboundPath: "/sms"}).Start(context.Background(), add, bus); err != nil {
		t.Fatalf("disabled Start error: %v", err)
	}
	if len(routes) != 0 {
		t.Fatalf("disabled routes=%v", routes)
	}

	enabled := NewSMS(SMSConfig{Enabled: true, InboundPath: " /sms "})
	if err := enabled.Start(context.Background(), add, bus); err != nil {
		t.Fatalf("enabled Start error: %v", err)
	}
	if enabled.bus != bus || routes["POST /sms"] == nil {
		t.Fatalf("enabled bus=%#v routes=%v", enabled.bus, routes)
	}

	routes = map[string]http.HandlerFunc{}
	if err := NewSMS(SMSConfig{Enabled: true}).Start(context.Background(), add, bus); err != nil {
		t.Fatalf("empty path Start error: %v", err)
	}
	if len(routes) != 0 {
		t.Fatalf("empty path routes=%v", routes)
	}

}

func TestSMSDeliverOutboundValidationAndErrors(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		cfg  SMSConfig
		out  Outbound
	}{
		{name: "empty session", cfg: SMSConfig{}, out: Outbound{SessionID: "sms: "}},
		{name: "missing sid", cfg: SMSConfig{OutboundURL: ""}, out: Outbound{SessionID: "+1555"}},
		{name: "missing from", cfg: SMSConfig{OutboundURL: "AC123"}, out: Outbound{SessionID: "+1555"}},
		{name: "missing token", cfg: SMSConfig{OutboundURL: "AC123", Secret: "+1444"}, out: Outbound{SessionID: "+1555"}},
		{name: "bad url", cfg: SMSConfig{OutboundURL: "http://[::1", Secret: "+1444", Token: "tok"}, out: Outbound{SessionID: "+1555"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := NewSMS(tt.cfg).DeliverOutbound(ctx, tt.out); err == nil {
				t.Fatal("expected error")
			}
		})
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewSMS(SMSConfig{OutboundURL: "AC123", Secret: "+1444", Token: "tok"}).DeliverOutbound(cancelCtx, Outbound{SessionID: "+1555"}); err == nil {
		t.Fatal("expected http client error")
	}
}

func TestSMSDeliverOutboundSuccessAndBadStatus(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotForm url.Values
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		data, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(data))
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatalf("content type=%q", r.Header.Get("Content-Type"))
		}
		rw.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	base := srv.URL + "/2010-04-01/Accounts/AC123"
	err := NewSMS(SMSConfig{OutboundURL: base + "/", Secret: " +1444 ", Token: " tok "}).DeliverOutbound(
		context.Background(),
		Outbound{SessionID: " sms:+1555 ", Text: " hello "},
	)
	if err != nil {
		t.Fatalf("DeliverOutbound error: %v", err)
	}
	if gotPath != "/2010-04-01/Accounts/AC123/Messages.json" {
		t.Fatalf("path=%q", gotPath)
	}
	if gotAuth != "Basic "+basicAuth("AC123", "tok") {
		t.Fatalf("auth=%q", gotAuth)
	}
	if gotForm.Get("From") != "+1444" || gotForm.Get("To") != "+1555" || gotForm.Get("Body") != "hello" {
		t.Fatalf("form=%v", gotForm)
	}

	bad := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, strings.Repeat("x", 2000), http.StatusBadRequest)
	}))
	defer bad.Close()
	err = NewSMS(SMSConfig{OutboundURL: bad.URL + "/Accounts/AC999", Secret: "+1444", Token: "tok"}).DeliverOutbound(context.Background(), Outbound{SessionID: "+1555"})
	if err == nil || !strings.Contains(err.Error(), "sms send status=400") || len(err.Error()) > 1100 {
		t.Fatalf("bad status err=%v", err)
	}
}

func TestSMSHandleInbound(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		body       string
		contentTyp string
		wantCode   int
		wantEvents int
	}{
		{name: "method", method: http.MethodGet, wantCode: http.StatusMethodNotAllowed},
		{name: "bad form", method: http.MethodPost, body: "%zz", contentTyp: "application/x-www-form-urlencoded", wantCode: http.StatusBadRequest},
		{name: "missing fields", method: http.MethodPost, body: "From=&Body=hello", contentTyp: "application/x-www-form-urlencoded", wantCode: http.StatusNoContent},
		{name: "ok", method: http.MethodPost, body: "From=%2B1555&Body=hello", contentTyp: "application/x-www-form-urlencoded", wantCode: http.StatusOK, wantEvents: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bus := &smsBus{err: errors.New("ignored")}
			s := NewSMS(SMSConfig{})
			s.bus = bus
			req := httptest.NewRequest(tt.method, "/sms", strings.NewReader(tt.body))
			if tt.contentTyp != "" {
				req.Header.Set("Content-Type", tt.contentTyp)
			}
			rr := httptest.NewRecorder()
			s.handleInbound(rr, req)
			if rr.Code != tt.wantCode {
				t.Fatalf("code=%d body=%q", rr.Code, rr.Body.String())
			}
			if len(bus.events) != tt.wantEvents {
				t.Fatalf("events=%+v", bus.events)
			}
			if tt.wantEvents == 1 {
				ev := bus.events[0]
				if ev.ChannelID != "sms" || ev.SessionID != "+1555" || ev.Text != "hello" {
					t.Fatalf("event=%+v", ev)
				}
				if !strings.Contains(rr.Header().Get("Content-Type"), "text/xml") || !strings.Contains(rr.Body.String(), "<Response>") {
					t.Fatalf("response headers=%v body=%q", rr.Header(), rr.Body.String())
				}
			}
		})
	}
}

func TestSMSResolveTwilioBaseAndBasicAuth(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantBase string
		wantSID  string
	}{
		{name: "empty", in: "", wantBase: "", wantSID: ""},
		{name: "slash only", in: " / ", wantBase: "", wantSID: ""},
		{name: "sid only", in: " /AC123/ ", wantBase: "https://api.twilio.com/2010-04-01/Accounts/AC123", wantSID: "AC123"},
		{name: "url", in: "https://example.test/Accounts/AC456/", wantBase: "https://example.test/Accounts/AC456", wantSID: "AC456"},
		{name: "bad url", in: "http://[::1", wantBase: "", wantSID: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, sid := resolveTwilioBase(tt.in)
			if base != tt.wantBase || sid != tt.wantSID {
				t.Fatalf("base=%q sid=%q", base, sid)
			}
		})
	}
	if got := basicAuth("u", "p"); got != base64.StdEncoding.EncodeToString([]byte("u:p")) {
		t.Fatalf("basicAuth=%q", got)
	}
}

type smsBus struct {
	events []Inbound
	err    error
}

func (s *smsBus) PublishInbound(_ context.Context, m Inbound) error {
	s.events = append(s.events, m)
	return s.err
}

// collectingBus records inbound messages from any goroutine: the bot-token
// chain publishes from its poll loop, not from the test's goroutine.
type collectingBus struct {
	mu  sync.Mutex
	got []Inbound
}

func (b *collectingBus) PublishInbound(_ context.Context, m Inbound) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.got = append(b.got, m)
	return nil
}

func (b *collectingBus) waitFor(t *testing.T, want string) Inbound {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		b.mu.Lock()
		for _, m := range b.got {
			if m.Text == want {
				b.mu.Unlock()
				return m
			}
		}
		b.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	t.Fatalf("no inbound message with text %q arrived; got %+v", want, b.got)
	return Inbound{}
}

// TestStandaloneWebhookRoundTrip is the webhook half of P8-7: one inbound and
// one outbound leg with only the channel service running — no gateway, no web
// UI, no WS chat. The registry serves its own routes, so nothing above Layer 1
// is constructed anywhere in this test.
func TestStandaloneWebhookRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var delivered []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		payload["_auth"] = r.Header.Get("Authorization")
		mu.Lock()
		delivered = append(delivered, payload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main": {Channels: appcfg.ChannelsSection{Webhook: appcfg.Webhook{
			Enabled:     true,
			InboundPath: "/hook",
			OutboundURL: upstream.URL,
			Token:       "tok",
		}}},
	}

	r := NewRegistry()
	bus := &collectingBus{}
	if err := r.BindAgent(context.Background(), cfg, "main", t.TempDir(), bus); err != nil {
		t.Fatalf("BindAgent: %v", err)
	}
	t.Cleanup(func() { _ = r.Stop(context.Background()) })

	srv := httptest.NewServer(r.HTTPHandler(nil))
	defer srv.Close()

	// Inbound leg.
	resp, err := http.Post(srv.URL+"/hook", "application/json",
		strings.NewReader(`{"session_id":"user-7","text":"ping"}`))
	if err != nil {
		t.Fatalf("inbound POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("inbound POST = %d, want 200", resp.StatusCode)
	}
	if got := bus.waitFor(t, "ping"); got.SessionID != "user-7" || got.ChannelID != "webhook" {
		t.Fatalf("inbound = %+v, want session user-7 on channel webhook", got)
	}

	// Outbound leg, through the same canonical Registry.Deliver the gateway uses.
	if err := r.Deliver(context.Background(), Outbound{
		ChannelID: "webhook", SessionID: "user-7", Text: "pong",
	}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != 1 {
		t.Fatalf("upstream received %d outbound posts, want 1: %+v", len(delivered), delivered)
	}
	if got := delivered[0]; got["text"] != "pong" || got["session_id"] != "user-7" || got["_auth"] != "Bearer tok" {
		t.Fatalf("outbound payload = %+v, want text pong for user-7 with the configured bearer token", got)
	}
}

// TestStandaloneBotTokenRoundTrip is the bot-token half of P8-7. A bot channel
// has no inbound HTTP route at all — it polls — so this is the chain that
// proves the channel service needs no HTTP surface of its own to receive.
func TestStandaloneBotTokenRoundTrip(t *testing.T) {
	origBase, origAfter := telegramAPIBase, pollAfter
	t.Cleanup(func() { telegramAPIBase, pollAfter = origBase, origAfter })
	// Keep the retry path from sleeping for real if a poll ever fails.
	pollAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}

	var mu sync.Mutex
	var sent []map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			// One message, then nothing, so the loop keeps polling harmlessly.
			mu.Lock()
			first := len(sent) == 0
			mu.Unlock()
			if first {
				_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":1,"message":{"message_id":9,"text":"ping","chat":{"id":4242}}}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var payload map[string]any
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &payload)
			mu.Lock()
			sent = append(sent, payload)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	telegramAPIBase = api.URL + "/bot"

	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main": {Channels: appcfg.ChannelsSection{Telegram: appcfg.Telegram{
			Enabled: true, BotToken: "bot-token",
		}}},
	}

	r := NewRegistry()
	bus := &collectingBus{}
	if err := r.BindAgent(context.Background(), cfg, "main", t.TempDir(), bus); err != nil {
		t.Fatalf("BindAgent: %v", err)
	}
	t.Cleanup(func() { _ = r.Stop(context.Background()) })

	// Inbound leg: arrives by polling, with no HTTP surface on our side.
	got := bus.waitFor(t, "ping")
	if got.ChannelID != "telegram" || !strings.HasPrefix(got.SessionID, "4242:") {
		t.Fatalf("inbound = %+v, want telegram session for chat 4242", got)
	}

	// Outbound leg.
	if err := r.Deliver(context.Background(), Outbound{
		ChannelID: "telegram", SessionID: got.SessionID, Text: "pong",
	}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 {
		t.Fatalf("bot API received %d sendMessage calls, want 1: %+v", len(sent), sent)
	}
	if got := sent[0]; got["text"] != "pong" {
		t.Fatalf("sendMessage payload = %+v, want text pong", got)
	}
}

// recordingBus captures what a channel published, so the test can assert the
// message actually crossed the inbound boundary rather than only that the
// endpoint returned 200.
type recordingBus struct{ got []Inbound }

func (b *recordingBus) PublishInbound(_ context.Context, m Inbound) error {
	b.got = append(b.got, m)
	return nil
}

// TestWebhookIsServedWithNoGatewayRouter is P8-4's acceptance criterion:
// "receives a webhook without loading the web UI or WS chat".
//
// The registry owns the route table precisely so it does not need the
// gateway's router — HTTPHandler(nil) is the whole HTTP surface a channel
// deployment needs. If this ever requires a gateway to be constructed first,
// the route table has drifted back onto the router and a primary agent switch
// can no longer withdraw a retired agent's inbound endpoint.
func TestWebhookIsServedWithNoGatewayRouter(t *testing.T) {
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main": {Channels: appcfg.ChannelsSection{
			Webhook: appcfg.Webhook{Enabled: true, InboundPath: "/main/hook"},
		}},
	}

	r := NewRegistry()
	bus := &recordingBus{}
	if err := r.BindAgent(context.Background(), cfg, "main", t.TempDir(), bus); err != nil {
		t.Fatalf("BindAgent: %v", err)
	}
	t.Cleanup(func() { _ = r.Stop(context.Background()) })

	// nil fallthrough: nothing but the channels is mounted.
	srv := httptest.NewServer(r.HTTPHandler(nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/main/hook", "application/json",
		strings.NewReader(`{"session_id":"user-1","text":"hello"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /main/hook = %d %s, want 200: the webhook is not reachable without a gateway", resp.StatusCode, body)
	}
	if len(bus.got) != 1 || bus.got[0].Text != "hello" || bus.got[0].SessionID != "user-1" {
		t.Fatalf("bus received %+v, want one {text:hello session:user-1}: the request never crossed the inbound boundary", bus.got)
	}

	// An unmounted path 404s rather than reaching anything, which is what the
	// nil fallthrough means.
	resp, err = http.Post(srv.URL+"/other/hook", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST unmounted: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST /other/hook = %d, want 404", resp.StatusCode)
	}
}

// A blank agent ID means "no active agent named", which is a startup state
// rather than a fault — and crucially it must leave the currently bound agent
// alone. Asserting only that nothing new gets bound would be vacuous, since
// BuildAgentChannels already returns no handlers for a blank ID; the property
// that actually needs a guard is that the blank call is not a teardown. Without
// it, one caller that has not resolved its active agent yet would silently
// unbind the running agent's bots.
func TestBindAgentWithNoAgentNamedLeavesTheBoundAgentAlone(t *testing.T) {
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main": {Channels: appcfg.ChannelsSection{
			Webhook: appcfg.Webhook{Enabled: true, InboundPath: "/main/hook"},
		}},
	}
	r := NewRegistry()
	if err := r.BindAgent(context.Background(), cfg, "main", t.TempDir(), &recordingBus{}); err != nil {
		t.Fatalf("BindAgent(main): %v", err)
	}
	t.Cleanup(func() { _ = r.Stop(context.Background()) })

	if err := r.BindAgent(context.Background(), cfg, "  ", t.TempDir(), nil); err != nil {
		t.Fatalf("BindAgent(blank) = %v, want nil", err)
	}

	if got := r.AgentID(); got != "main" {
		t.Fatalf("AgentID = %q after a blank bind, want main still bound", got)
	}
	if _, ok := r.Route(http.MethodPost, "/main/hook"); !ok {
		t.Fatal("main's inbound path was withdrawn by a bind that named no agent")
	}
}

func TestWebhookStartInboundStopAndOutbound(t *testing.T) {
	var outbound map[string]any
	outboundSrv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("authorization=%q", got)
		}
		if got := r.Header.Get("X-Forebrain-Secret"); got != "secret" {
			t.Fatalf("secret=%q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&outbound); err != nil {
			t.Fatalf("decode outbound: %v", err)
		}
		rw.WriteHeader(http.StatusNoContent)
	}))
	defer outboundSrv.Close()

	w := NewWebhook(WebhookConfig{Enabled: true, Token: " token ", Secret: " secret ", OutboundURL: outboundSrv.URL})
	if w.ID() != "webhook" {
		t.Fatalf("id=%q", w.ID())
	}
	routes := map[string]http.HandlerFunc{}
	add := func(method, path string, hf http.HandlerFunc) {
		routes[method+" "+path] = hf
	}
	bus := &webhookBus{}
	if err := w.Start(context.Background(), add, bus); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("routes=%v", routes)
	}

	req := httptest.NewRequest(http.MethodPost, "/channels/webhook/inbound", bytes.NewBufferString(`{"sessionId":"s1","message":"hello"}`))
	req.Header.Set("X-Forebrain-Secret", "secret")
	rw := httptest.NewRecorder()
	routes["POST /channels/webhook/inbound"](rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("inbound status=%d body=%s", rw.Code, rw.Body.String())
	}
	if len(bus.messages) != 1 || bus.messages[0].ChannelID != "webhook" || bus.messages[0].SessionID != "s1" || bus.messages[0].Text != "hello" {
		t.Fatalf("messages=%+v", bus.messages)
	}

	rw = httptest.NewRecorder()
	routes["GET /channels/webhook/health"](rw, httptest.NewRequest(http.MethodGet, "/channels/webhook/health", nil))
	if rw.Code != http.StatusOK || rw.Body.String() != "ok" {
		t.Fatalf("health status=%d body=%q", rw.Code, rw.Body.String())
	}

	if err := w.DeliverOutbound(context.Background(), Outbound{SessionID: " s2 ", Text: " hi "}); err != nil {
		t.Fatalf("DeliverOutbound error: %v", err)
	}
	if outbound["channel_id"] != "webhook" || outbound["session_id"] != "s2" || outbound["text"] != "hi" {
		t.Fatalf("outbound=%v", outbound)
	}
	if err := w.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
}

func TestWebhookDisabledAndRegister(t *testing.T) {
	w := NewWebhook(WebhookConfig{})
	routes := map[string]http.HandlerFunc{}
	if err := w.Start(context.Background(), func(method, path string, hf http.HandlerFunc) {
		routes[method+" "+path] = hf
	}, &webhookBus{}); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if len(routes) != 0 {
		t.Fatalf("disabled routes=%v", routes)
	}
	if err := w.DeliverOutbound(context.Background(), Outbound{}); err == nil {
		t.Fatal("expected empty outbound url error")
	}

}

type webhookBus struct {
	messages []Inbound
}

func (b *webhookBus) PublishInbound(ctx context.Context, m Inbound) error {
	_ = ctx
	b.messages = append(b.messages, m)
	return nil
}
