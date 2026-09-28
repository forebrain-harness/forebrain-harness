package channel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/testutil"
)

func TestBlueBubblesNewIDStartStopAndRegister(t *testing.T) {
	b := NewBlueBubbles(BlueBubblesConfig{})
	if b == nil || b.ID() != "bluebubbles" || b.httpClient == nil || b.httpClient.Timeout == 0 || b.seen == nil {
		t.Fatalf("bluebubbles=%#v", b)
	}
	routes := map[string]http.HandlerFunc{}
	add := func(method, path string, hf http.HandlerFunc) {
		routes[method+" "+path] = hf
	}
	if err := b.Start(context.Background(), add, &blueBubblesBus{}); err != nil {
		t.Fatalf("disabled Start error: %v", err)
	}
	if len(routes) != 0 {
		t.Fatalf("disabled routes=%v", routes)
	}
	if err := NewBlueBubbles(BlueBubblesConfig{Enabled: true}).Start(context.Background(), add, nil); err != nil {
		t.Fatalf("enabled without inbound path Start error: %v", err)
	}
	if len(routes) != 0 {
		t.Fatalf("empty path routes=%v", routes)
	}
	if err := NewBlueBubbles(BlueBubblesConfig{Enabled: true, InboundPath: " /bb "}).Start(context.Background(), add, nil); err != nil {
		t.Fatalf("enabled Start error: %v", err)
	}
	if routes["POST /bb"] == nil {
		t.Fatalf("routes=%v", routes)
	}
	if err := b.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
}

func TestBlueBubblesDeliverOutbound(t *testing.T) {
	if err := NewBlueBubbles(BlueBubblesConfig{}).DeliverOutbound(context.Background(), Outbound{SessionID: " "}); err == nil {
		t.Fatal("expected invalid session error")
	}
	if err := NewBlueBubbles(BlueBubblesConfig{}).DeliverOutbound(context.Background(), Outbound{SessionID: "chat:guid"}); err == nil {
		t.Fatal("expected outbound url error")
	}
	if err := NewBlueBubbles(BlueBubblesConfig{OutboundURL: "http://example.test"}).DeliverOutbound(context.Background(), Outbound{SessionID: "chat:guid"}); err == nil {
		t.Fatal("expected token error")
	}
	if err := NewBlueBubbles(BlueBubblesConfig{OutboundURL: "://bad", Token: "tok"}).DeliverOutbound(context.Background(), Outbound{SessionID: "chat:guid"}); err == nil {
		t.Fatal("expected request creation error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewBlueBubbles(BlueBubblesConfig{OutboundURL: "http://127.0.0.1:1", Token: "tok"}).DeliverOutbound(ctx, Outbound{SessionID: "chat:guid"}); err == nil {
		t.Fatal("expected client error")
	}

	bad := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, strings.Repeat("x", 2000), http.StatusBadGateway)
	}))
	defer bad.Close()
	err := NewBlueBubbles(BlueBubblesConfig{OutboundURL: bad.URL, Token: "tok"}).DeliverOutbound(context.Background(), Outbound{SessionID: "chat:guid"})
	if err == nil || !strings.Contains(err.Error(), "bluebubbles send status=502") || len(err.Error()) > 1150 {
		t.Fatalf("status error=%v", err)
	}

	var gotPath, gotPassword, gotContentType string
	var gotBody map[string]string
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotPassword = r.URL.Query().Get("password")
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := NewBlueBubbles(BlueBubblesConfig{OutboundURL: srv.URL + "/", Secret: " secret "}).DeliverOutbound(context.Background(), Outbound{SessionID: " chat:guid ", Text: " hello "}); err != nil {
		t.Fatalf("DeliverOutbound error: %v", err)
	}
	if gotPath != "/api/v1/message/text" || gotPassword != "secret" || gotContentType != "application/json" {
		t.Fatalf("path=%q password=%q contentType=%q", gotPath, gotPassword, gotContentType)
	}
	if gotBody["chatGuid"] != "guid" || gotBody["message"] != "hello" || !strings.HasPrefix(gotBody["tempGuid"], "temp-") {
		t.Fatalf("body=%v", gotBody)
	}
}

func TestBlueBubblesHandleInbound(t *testing.T) {
	tests := []struct {
		name       string
		cfg        BlueBubblesConfig
		method     string
		target     string
		headers    map[string]string
		body       string
		wantCode   int
		wantEvents int
		wantSID    string
		wantText   string
	}{
		{name: "method not allowed", cfg: BlueBubblesConfig{}, method: http.MethodGet, body: `{}`, wantCode: http.StatusMethodNotAllowed},
		{name: "unauthorized", cfg: BlueBubblesConfig{Token: "tok"}, method: http.MethodPost, body: `{}`, wantCode: http.StatusUnauthorized},
		{name: "bad json", cfg: BlueBubblesConfig{}, method: http.MethodPost, body: `{`, wantCode: http.StatusBadRequest},
		{name: "missing fields", cfg: BlueBubblesConfig{}, method: http.MethodPost, body: `{"guid":"m1"}`, wantCode: http.StatusNoContent},
		{name: "message query auth", cfg: BlueBubblesConfig{Token: "tok"}, method: http.MethodPost, target: "/bb?password=tok", body: `{"chatGuid":" chat1 ","text":" hi ","guid":"m1"}`, wantCode: http.StatusNoContent, wantEvents: 1, wantSID: "chat1", wantText: "hi"},
		{name: "message header auth nested", cfg: BlueBubblesConfig{Secret: "sec"}, method: http.MethodPost, headers: map[string]string{"x-password": "sec"}, body: `{"data":{"chat_guid":"chat2","message":"hello","message_guid":"m2"}}`, wantCode: http.StatusNoContent, wantEvents: 1, wantSID: "chat2", wantText: "hello"},
		{name: "message x-guid", cfg: BlueBubblesConfig{Token: "tok"}, method: http.MethodPost, headers: map[string]string{"x-guid": "tok"}, body: `{"chatIdentifier":"chat3","body":"hello","id":"m3"}`, wantCode: http.StatusNoContent, wantEvents: 1, wantSID: "chat3", wantText: "hello"},
		{name: "message bluebubbles guid", cfg: BlueBubblesConfig{Token: "tok"}, method: http.MethodPost, headers: map[string]string{"x-bluebubbles-guid": "tok"}, body: `{"handle":"chat4","text":"hello"}`, wantCode: http.StatusNoContent, wantEvents: 1, wantSID: "chat4", wantText: "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bus := &blueBubblesBus{}
			b := NewBlueBubbles(tt.cfg)
			b.bus = bus
			target := tt.target
			if target == "" {
				target = "/bb"
			}
			req := httptest.NewRequest(tt.method, target, strings.NewReader(tt.body))
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			rr := httptest.NewRecorder()
			b.handleInbound(rr, req)
			if rr.Code != tt.wantCode {
				t.Fatalf("code=%d body=%q", rr.Code, rr.Body.String())
			}
			if len(bus.events) != tt.wantEvents {
				t.Fatalf("events=%+v", bus.events)
			}
			if tt.wantEvents == 1 {
				if bus.events[0].ChannelID != "bluebubbles" || bus.events[0].SessionID != tt.wantSID || bus.events[0].Text != tt.wantText || bus.events[0].Raw == nil {
					t.Fatalf("event=%+v", bus.events[0])
				}
			}
		})
	}

	bus := &blueBubblesBus{err: io.ErrClosedPipe}
	b := NewBlueBubbles(BlueBubblesConfig{})
	b.bus = bus
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/bb", strings.NewReader(`{"chatGuid":"chat","text":"hi","guid":"dup"}`))
		rr := httptest.NewRecorder()
		b.handleInbound(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("dup request %d code=%d", i, rr.Code)
		}
	}
	if len(bus.events) != 1 {
		t.Fatalf("duplicate events=%+v", bus.events)
	}
}

func TestBlueBubblesHelpers(t *testing.T) {
	b := NewBlueBubbles(BlueBubblesConfig{Token: " tok ", Secret: "sec"})
	if b.authToken() != "tok" {
		t.Fatal("token should win over secret")
	}
	if NewBlueBubbles(BlueBubblesConfig{Secret: " sec "}).authToken() != "sec" {
		t.Fatal("secret fallback mismatch")
	}
	req := httptest.NewRequest(http.MethodPost, "/bb?password=bad", nil)
	if b.authorized(req) {
		t.Fatal("bad password should not authorize")
	}
	if !NewBlueBubbles(BlueBubblesConfig{}).authorized(req) {
		t.Fatal("empty expected auth should authorize")
	}
	if parseBlueBubblesSession(" chat:guid ") != "guid" || parseBlueBubblesSession("guid") != "guid" {
		t.Fatal("parseBlueBubblesSession mismatch")
	}
	if got := unwrapBlueBubblesData(map[string]any{"data": "not map", "x": "y"}); got["x"] != "y" {
		t.Fatalf("unwrap fallback=%v", got)
	}
	if blueString(map[string]any{"a": " ", "b": 1, "c": " value "}, "a", "b", "c") != "value" {
		t.Fatal("blueString mismatch")
	}
	if blueString(map[string]any{}, "missing") != "" {
		t.Fatal("blueString missing mismatch")
	}

	if b.isDuplicate(" ") {
		t.Fatal("blank id should not be duplicate")
	}
	if b.isDuplicate(" m1 ") {
		t.Fatal("first m1 should not be duplicate")
	}
	if !b.isDuplicate("m1") {
		t.Fatal("second m1 should be duplicate")
	}
	b.seen["old"] = time.Now().Add(-10 * time.Minute)
	if b.isDuplicate("m2") {
		t.Fatal("first m2 should not be duplicate")
	}
	if _, ok := b.seen["old"]; ok {
		t.Fatal("old duplicate entry was not pruned")
	}

	u := url.Values{}
	u.Set("password", "tok")
	req = httptest.NewRequest(http.MethodPost, "/bb?"+u.Encode(), nil)
	if !b.authorized(req) {
		t.Fatal("query password should authorize")
	}
}

type blueBubblesBus struct {
	events []Inbound
	err    error
}

func (b *blueBubblesBus) PublishInbound(_ context.Context, in Inbound) error {
	b.events = append(b.events, in)
	return b.err
}
