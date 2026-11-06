package channel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/testutil"
)

func TestWhatsAppNewIDStartStopAndRegister(t *testing.T) {
	w := NewWhatsApp(WhatsAppConfig{})
	if w == nil || w.ID() != "whatsapp" || w.httpClient == nil || w.httpClient.Timeout == 0 || w.seen == nil {
		t.Fatalf("whatsapp=%#v", w)
	}
	routes := map[string]http.HandlerFunc{}
	add := func(method, path string, hf http.HandlerFunc) {
		routes[method+" "+path] = hf
	}
	w.MountRoutes(add, &whatsAppBus{})
	if err := w.Start(context.Background(), add, &whatsAppBus{}); err != nil {
		t.Fatalf("disabled Start error: %v", err)
	}
	if w.inboundAdd == nil || w.bus == nil || len(routes) != 0 {
		t.Fatalf("inboundAdd=%v bus=%v routes=%v", w.inboundAdd, w.bus, routes)
	}

	oldTicker := whatsappNewTicker
	oldAfter := whatsappAfter
	t.Cleanup(func() {
		whatsappNewTicker = oldTicker
		whatsappAfter = oldAfter
	})
	whatsappNewTicker = func(time.Duration) *time.Ticker { return time.NewTicker(time.Hour) }
	enabled := NewWhatsApp(WhatsAppConfig{Enabled: true, InboundPath: " /wa ", Secret: "fixture-webhook-secret"})
	enabled.MountRoutes(add, &whatsAppBus{})
	if err := enabled.Start(context.Background(), add, &whatsAppBus{}); err != nil {
		t.Fatalf("enabled Start error: %v", err)
	}
	if enabled.cfg.OutboundURL != "http://127.0.0.1:3000" || routes["POST /wa"] == nil {
		t.Fatalf("outbound=%q routes=%v", enabled.cfg.OutboundURL, routes)
	}
	// Start runs the poll loop only: a Start that mounted too would give the
	// lease holder two routes for one callback URL.
	startOnly := NewWhatsApp(WhatsAppConfig{Enabled: true, InboundPath: "/start-only"})
	if err := startOnly.Start(context.Background(), add, &whatsAppBus{}); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if startOnly.cfg.OutboundURL != "http://127.0.0.1:3000" || routes["POST /start-only"] != nil {
		t.Fatalf("outbound=%q routes=%v", startOnly.cfg.OutboundURL, routes)
	}
	if err := startOnly.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
	if err := enabled.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}

	if err := NewWhatsApp(WhatsAppConfig{}).Stop(context.Background()); err != nil {
		t.Fatalf("nil cancel Stop error: %v", err)
	}
}

func TestWhatsAppDeliverOutbound(t *testing.T) {
	if err := NewWhatsApp(WhatsAppConfig{}).DeliverOutbound(context.Background(), Outbound{SessionID: " "}); err == nil {
		t.Fatal("expected invalid session error")
	}
	if err := NewWhatsApp(WhatsAppConfig{OutboundURL: "://bad"}).DeliverOutbound(context.Background(), Outbound{SessionID: "chat:id"}); err == nil {
		t.Fatal("expected request error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewWhatsApp(WhatsAppConfig{OutboundURL: "http://127.0.0.1:1"}).DeliverOutbound(ctx, Outbound{SessionID: "id"}); err == nil {
		t.Fatal("expected client error")
	}
	bad := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, strings.Repeat("x", 2000), http.StatusBadGateway)
	}))
	defer bad.Close()
	err := NewWhatsApp(WhatsAppConfig{OutboundURL: bad.URL}).DeliverOutbound(context.Background(), Outbound{SessionID: "id"})
	if err == nil || !strings.Contains(err.Error(), "whatsapp send status=502") || len(err.Error()) > 1150 {
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
	if err := NewWhatsApp(WhatsAppConfig{OutboundURL: srv.URL, Token: " tok "}).DeliverOutbound(context.Background(), Outbound{SessionID: " chat:id ", Text: " hi "}); err != nil {
		t.Fatalf("DeliverOutbound error: %v", err)
	}
	if gotPath != "/send" || gotAuth != "Bearer tok" || gotContentType != "application/json" || gotBody["chatId"] != "id" || gotBody["message"] != "hi" {
		t.Fatalf("path=%q auth=%q contentType=%q body=%v", gotPath, gotAuth, gotContentType, gotBody)
	}
	noToken := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization=%q", got)
		}
		rw.WriteHeader(http.StatusOK)
	}))
	defer noToken.Close()
	if err := NewWhatsApp(WhatsAppConfig{OutboundURL: noToken.URL}).DeliverOutbound(context.Background(), Outbound{SessionID: "id"}); err != nil {
		t.Fatalf("no-token DeliverOutbound error: %v", err)
	}
}

func TestWhatsAppHandleInbound(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		body       string
		wantCode   int
		wantEvents int
		wantSID    string
		wantText   string
	}{
		{name: "method", method: http.MethodGet, body: `{}`, wantCode: http.StatusMethodNotAllowed},
		{name: "bad json", method: http.MethodPost, body: `{`, wantCode: http.StatusBadRequest},
		{name: "missing", method: http.MethodPost, body: `{"id":"m1"}`, wantCode: http.StatusNoContent},
		{name: "message", method: http.MethodPost, body: `{"chatId":" chat1 ","body":" hi ","messageId":"m1"}`, wantCode: http.StatusNoContent, wantEvents: 1, wantSID: "chat1", wantText: "hi"},
		{name: "alt fields", method: http.MethodPost, body: `{"session_id":"chat2","content":"hello","id":"m2"}`, wantCode: http.StatusNoContent, wantEvents: 1, wantSID: "chat2", wantText: "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bus := &whatsAppBus{}
			w := NewWhatsApp(WhatsAppConfig{})
			w.bus = bus
			req := httptest.NewRequest(tt.method, "/wa", strings.NewReader(tt.body))
			rr := httptest.NewRecorder()
			w.handleInbound(rr, req)
			if rr.Code != tt.wantCode {
				t.Fatalf("code=%d body=%q", rr.Code, rr.Body.String())
			}
			if len(bus.events) != tt.wantEvents {
				t.Fatalf("events=%+v", bus.events)
			}
			if tt.wantEvents == 1 {
				if bus.events[0].ChannelID != "whatsapp" || bus.events[0].SessionID != tt.wantSID || bus.events[0].Text != tt.wantText || bus.events[0].Raw == nil {
					t.Fatalf("event=%+v", bus.events[0])
				}
			}
		})
	}

	bus := &whatsAppBus{err: io.ErrClosedPipe}
	w := NewWhatsApp(WhatsAppConfig{})
	w.bus = bus
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/wa", strings.NewReader(`{"chatId":"chat","text":"hi","id":"dup"}`))
		rr := httptest.NewRecorder()
		w.handleInbound(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("dup code=%d", rr.Code)
		}
	}
	if len(bus.events) != 1 {
		t.Fatalf("duplicate events=%+v", bus.events)
	}
}

// signWhatsAppBody is the bridge's side of the contract: Meta's own
// `sha256=<hex>` HMAC over the body exactly as sent.
func signWhatsAppBody(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestWhatsAppInboundSignature(t *testing.T) {
	const secret = "fixture-webhook-secret"
	body := `{"chatId":" chat1 ","body":" hi ","messageId":"m1"}`
	tests := []struct {
		name     string
		body     string
		sig      string
		wantCode int
		want     int
	}{
		{name: "valid", body: body, sig: signWhatsAppBody(body, secret), wantCode: http.StatusNoContent, want: 1},
		{name: "wrong signature", body: body, sig: signWhatsAppBody(body, "other-fixture-secret"), wantCode: http.StatusForbidden},
		{name: "missing header", body: body, wantCode: http.StatusForbidden},
		{name: "wrong prefix", body: body, sig: "md5=deadbeef", wantCode: http.StatusForbidden},
		// The signature covers the bytes as sent, not the decoded payload: an
		// implementation that re-marshals the JSON before hashing lets this one
		// through.
		{name: "tampered", body: `{"chatId":" chat1 ","body":" evil ","messageId":"m1"}`, sig: signWhatsAppBody(body, secret), wantCode: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bus := &whatsAppBus{}
			w := NewWhatsApp(WhatsAppConfig{Secret: secret})
			w.bus = bus
			req := httptest.NewRequest(http.MethodPost, "/wa", strings.NewReader(tt.body))
			if tt.sig != "" {
				req.Header.Set("X-Hub-Signature-256", tt.sig)
			}
			rr := httptest.NewRecorder()
			w.handleInbound(rr, req)
			if rr.Code != tt.wantCode {
				t.Fatalf("code=%d body=%q events=%+v", rr.Code, rr.Body.String(), bus.events)
			}
			if len(bus.events) != tt.want {
				t.Fatalf("events=%+v", bus.events)
			}
			if tt.want == 1 && (bus.events[0].SessionID != "chat1" || bus.events[0].Text != "hi") {
				t.Fatalf("event=%+v", bus.events[0])
			}
		})
	}
}

// An auth-exempt route (pkg/config/gateway.go) that cannot authenticate its
// caller is not mounted at all; the poll loop is this channel's real inbound
// path and must keep working without a webhook secret.
func TestWhatsAppNoSecretMountsNoRouteAndStillPolls(t *testing.T) {
	oldTicker := whatsappNewTicker
	t.Cleanup(func() { whatsappNewTicker = oldTicker })
	whatsappNewTicker = func(time.Duration) *time.Ticker { return time.NewTicker(time.Millisecond) }

	routes := map[string]http.HandlerFunc{}
	add := func(method, path string, hf http.HandlerFunc) {
		routes[method+" "+path] = hf
	}
	polled := make(chan struct{})
	var once sync.Once
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/messages" {
			once.Do(func() { close(polled) })
		}
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := &whatsAppBus{}
	w := NewWhatsApp(WhatsAppConfig{Enabled: true, InboundPath: " /wa ", OutboundURL: srv.URL, Token: "tok"})
	w.MountRoutes(add, bus)
	if routes["POST /wa"] != nil {
		t.Fatalf("route mounted without a secret: %v", routes)
	}
	if err := w.Start(ctx, add, bus); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	select {
	case <-polled:
	case <-time.After(2 * time.Second):
		t.Fatal("no-secret adapter did not poll the bridge")
	}
	if err := w.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
}

func TestWhatsAppPollLoop(t *testing.T) {
	oldTicker := whatsappNewTicker
	t.Cleanup(func() { whatsappNewTicker = oldTicker })
	whatsappNewTicker = func(time.Duration) *time.Ticker { return time.NewTicker(time.Millisecond) }

	var calls int
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/messages" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("Authorization=%q", got)
		}
		switch calls {
		case 1:
			_, _ = rw.Write([]byte(`[
				{"chatId":"chat1","body":" hi ","messageId":"m1"},
				{"chatId":"chat1","body":"dupe","messageId":"m1"},
				{"chatId":"","body":"skip","messageId":"m2"},
				{"chatId":"chat3","body":" ","messageId":"m3"}
			]`))
		case 2:
			http.Error(rw, "bad", http.StatusBadGateway)
		default:
			_, _ = rw.Write([]byte(`{`))
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	bus := &whatsAppBus{onPublish: cancel}
	w := NewWhatsApp(WhatsAppConfig{OutboundURL: srv.URL, Token: "tok"})
	w.bus = bus
	w.done = make(chan struct{})
	go w.pollLoop(ctx)
	select {
	case <-w.done:
	case <-time.After(time.Second):
		t.Fatal("pollLoop did not exit")
	}
	if len(bus.events) != 1 || bus.events[0].SessionID != "chat1" || bus.events[0].Text != "hi" {
		t.Fatalf("events=%+v", bus.events)
	}

	ctx, cancel = context.WithCancel(context.Background())
	w = NewWhatsApp(WhatsAppConfig{OutboundURL: "://bad"})
	w.done = make(chan struct{})
	go w.pollLoop(ctx)
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case <-w.done:
	case <-time.After(time.Second):
		t.Fatal("pollLoop request-error case did not exit")
	}

	ctx, cancel = context.WithCancel(context.Background())
	w = NewWhatsApp(WhatsAppConfig{OutboundURL: "http://127.0.0.1:1"})
	w.done = make(chan struct{})
	go w.pollLoop(ctx)
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case <-w.done:
	case <-time.After(time.Second):
		t.Fatal("pollLoop client-error case did not exit")
	}
}

func TestWhatsAppHelpers(t *testing.T) {
	sessionID, text, msgID := parseWhatsAppInbound(map[string]any{"from": " chat:id ", "message": " hi ", "message_id": " m1 "})
	if sessionID != "id" || text != "hi" || msgID != "m1" {
		t.Fatalf("parse inbound sid=%q text=%q id=%q", sessionID, text, msgID)
	}
	if parseWhatsAppSession(" chat:id ") != "id" || parseWhatsAppSession(" id ") != "id" || parseWhatsAppSession(" ") != "" {
		t.Fatal("parseWhatsAppSession mismatch")
	}
	if strv(map[string]any{"a": " ", "b": 1, "c": " value "}, "a", "b", "c") != "value" {
		t.Fatal("strv mismatch")
	}
	if strv(map[string]any{}, "missing") != "" {
		t.Fatal("strv missing mismatch")
	}

	w := NewWhatsApp(WhatsAppConfig{})
	if w.isDuplicate(" ") {
		t.Fatal("blank id should not be duplicate")
	}
	if w.isDuplicate(" m1 ") {
		t.Fatal("first id should not be duplicate")
	}
	if !w.isDuplicate("m1") {
		t.Fatal("second id should be duplicate")
	}
	w.seen["old"] = time.Now().Add(-10 * time.Minute)
	if w.isDuplicate("m2") {
		t.Fatal("first m2 should not be duplicate")
	}
	if _, ok := w.seen["old"]; ok {
		t.Fatal("old duplicate entry was not pruned")
	}
}

type whatsAppBus struct {
	events    []Inbound
	err       error
	onPublish func()
}

func (b *whatsAppBus) PublishInbound(_ context.Context, in Inbound) error {
	b.events = append(b.events, in)
	if b.onPublish != nil {
		b.onPublish()
	}
	return b.err
}
