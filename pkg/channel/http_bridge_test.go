package channel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/testutil"
)

func TestNewStartAndStop(t *testing.T) {
	b := NewHTTPBridge(HTTPConfig{ChannelID: "httpx"})
	if b == nil || b.httpClient == nil || b.httpClient.Timeout == 0 {
		t.Fatalf("bridge=%#v", b)
	}
	if err := b.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}

	routes := map[string]http.HandlerFunc{}
	add := func(method, path string, hf http.HandlerFunc) {
		routes[method+" "+path] = hf
	}
	if err := NewHTTPBridge(HTTPConfig{ChannelID: "off"}).Start(context.Background(), add, &captureBus{}); err != nil {
		t.Fatalf("disabled start error: %v", err)
	}
	if len(routes) != 0 {
		t.Fatalf("disabled routes=%v", routes)
	}

	bridge := NewHTTPBridge(HTTPConfig{ChannelID: "httpx", Enabled: true})
	bus := &captureBus{}
	if err := bridge.Start(context.Background(), add, bus); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if bridge.bus != bus {
		t.Fatal("bus not stored")
	}
	inbound := routes["POST /channels/httpx/inbound"]
	if inbound == nil || routes["GET /channels/httpx/health"] == nil {
		t.Fatalf("routes=%v", routes)
	}
	rr := httptest.NewRecorder()
	routes["GET /channels/httpx/health"](rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("health code=%d body=%q", rr.Code, rr.Body.String())
	}

	routes = map[string]http.HandlerFunc{}
	if err := NewHTTPBridge(HTTPConfig{ChannelID: "custom", Enabled: true, InboundPath: " /custom "}).Start(context.Background(), add, nil); err != nil {
		t.Fatalf("custom Start error: %v", err)
	}
	if routes["POST /custom"] == nil {
		t.Fatalf("custom routes=%v", routes)
	}
}

func TestHTTPDeliverOutbound(t *testing.T) {
	if err := NewHTTPBridge(HTTPConfig{ChannelID: "h"}).DeliverOutbound(context.Background(), Outbound{}); err == nil {
		t.Fatal("expected empty outbound url error")
	}
	if err := NewHTTPBridge(HTTPConfig{ChannelID: "h", OutboundURL: "://bad"}).DeliverOutbound(context.Background(), Outbound{}); err == nil {
		t.Fatal("expected bad request url error")
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewHTTPBridge(HTTPConfig{ChannelID: "h", OutboundURL: "http://127.0.0.1:1"}).DeliverOutbound(cancelCtx, Outbound{}); err == nil {
		t.Fatal("expected http client error")
	}

	var gotBody string
	var gotAuth string
	var gotSecret string
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		gotAuth = r.Header.Get("Authorization")
		gotSecret = r.Header.Get("X-Forebrain-Secret")
		rw.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	err := NewHTTPBridge(HTTPConfig{ChannelID: "h", OutboundURL: srv.URL, Token: " token ", Secret: " secret "}).DeliverOutbound(
		context.Background(),
		Outbound{SessionID: " s ", Text: " hello "},
	)
	if err != nil {
		t.Fatalf("DeliverOutbound error: %v", err)
	}
	if gotAuth != "Bearer token" || gotSecret != "secret" {
		t.Fatalf("headers auth=%q secret=%q", gotAuth, gotSecret)
	}
	for _, want := range []string{`"channel_id":"h"`, `"session_id":"s"`, `"text":"hello"`} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("body missing %q in %s", want, gotBody)
		}
	}

	badStatus := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, strings.Repeat("x", 2000), http.StatusBadGateway)
	}))
	defer badStatus.Close()
	err = NewHTTPBridge(HTTPConfig{ChannelID: "h", OutboundURL: badStatus.URL}).DeliverOutbound(context.Background(), Outbound{})
	if err == nil || !strings.Contains(err.Error(), "outbound status=502") || len(err.Error()) > 1100 {
		t.Fatalf("bad status err=%v", err)
	}
}

func TestHandleInbound(t *testing.T) {
	tests := []struct {
		name       string
		cfg        HTTPConfig
		target     string
		header     string
		body       string
		wantCode   int
		wantEvents int
		wantText   string
		wantSID    string
	}{
		{
			name:     "disabled",
			cfg:      HTTPConfig{ChannelID: "h"},
			body:     "hello",
			wantCode: http.StatusNotFound,
		},
		{
			name:     "forbidden without secret",
			cfg:      HTTPConfig{ChannelID: "h", Enabled: true, Secret: "secret"},
			body:     "hello",
			wantCode: http.StatusForbidden,
		},
		{
			name:       "plain text query secret",
			cfg:        HTTPConfig{ChannelID: "h", Enabled: true, Secret: "secret"},
			target:     "/in?secret=secret",
			body:       " hello ",
			wantCode:   http.StatusOK,
			wantEvents: 1,
			wantText:   "hello",
			wantSID:    "h-default",
		},
		{
			name: "json configured keys",
			cfg: HTTPConfig{
				ChannelID:      "h",
				Enabled:        true,
				Secret:         "secret",
				SessionField:   "sid",
				TextField:      "msg",
				AltSessionKeys: []string{"session"},
				AltTextKeys:    []string{"text"},
			},
			header:     "secret",
			body:       `{"sid":" s1 ","msg":" hi "}`,
			wantCode:   http.StatusOK,
			wantEvents: 1,
			wantText:   "hi",
			wantSID:    "s1",
		},
		{
			name:       "empty text no publish",
			cfg:        HTTPConfig{ChannelID: "h", Enabled: true},
			body:       "   ",
			wantCode:   http.StatusOK,
			wantEvents: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bus := &captureBus{}
			b := NewHTTPBridge(tt.cfg)
			b.bus = bus
			target := tt.target
			if target == "" {
				target = "/in"
			}
			req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(tt.body))
			if tt.header != "" {
				req.Header.Set("X-Forebrain-Secret", tt.header)
			}
			rr := httptest.NewRecorder()
			b.handleInbound(rr, req)
			if rr.Code != tt.wantCode {
				t.Fatalf("code=%d body=%q", rr.Code, rr.Body.String())
			}
			if tt.wantCode == http.StatusOK && strings.TrimSpace(rr.Body.String()) != `{"ok":true}` {
				t.Fatalf("ok body=%q", rr.Body.String())
			}
			if len(bus.events) != tt.wantEvents {
				t.Fatalf("events=%+v", bus.events)
			}
			if tt.wantEvents > 0 {
				ev := bus.events[0]
				if ev.ChannelID != tt.cfg.ChannelID || ev.SessionID != tt.wantSID || ev.Text != tt.wantText || ev.Raw != tt.body {
					t.Fatalf("event=%+v", ev)
				}
			}
		})
	}
}

func TestHandleInboundBodyReadErrorAndBusError(t *testing.T) {
	b := NewHTTPBridge(HTTPConfig{ChannelID: "h", Enabled: true})
	b.bus = &captureBus{err: errors.New("publish failed")}
	rr := httptest.NewRecorder()
	b.handleInbound(rr, httptest.NewRequest(http.MethodPost, "/in", errReader{}))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("read error code=%d", rr.Code)
	}

	rr = httptest.NewRecorder()
	b.handleInbound(rr, httptest.NewRequest(http.MethodPost, "/in", strings.NewReader("hello")))
	if rr.Code != http.StatusOK {
		t.Fatalf("publish error should still return ok, code=%d", rr.Code)
	}
}

func TestParseInboundAndGetString(t *testing.T) {
	b := NewHTTPBridge(HTTPConfig{
		ChannelID:      "h",
		SessionField:   "missing",
		TextField:      "missing",
		AltSessionKeys: []string{"sid", "sid_num"},
		AltTextKeys:    []string{"text", "payload"},
	})
	sid, text := b.parseInbound([]byte(`{"sid_num":42,"payload":{"a":1}}`))
	if sid != "42" || text != `{"a":1}` {
		t.Fatalf("sid=%q text=%q", sid, text)
	}
	sid, text = b.parseInbound([]byte(`{"sid":"","text":""}`))
	if sid != "h-default" || text != `{"sid":"","text":""}` {
		t.Fatalf("fallback sid=%q text=%q", sid, text)
	}
	if got := getString(map[string]any{"a": nil, "b": " ", "c": []any{"x"}}, "a", "b", "c"); got != `["x"]` {
		t.Fatalf("getString=%q", got)
	}
	if got := getString(map[string]any{}, "missing"); got != "" {
		t.Fatalf("missing getString=%q", got)
	}
}

type captureBus struct {
	events []Inbound
	err    error
}

func (c *captureBus) PublishInbound(_ context.Context, m Inbound) error {
	c.events = append(c.events, m)
	return c.err
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func TestPostJSONWithHeaders(t *testing.T) {
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content-type=%q", got)
		}
		if got := r.Header.Values("X-Test"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
			t.Fatalf("X-Test=%v", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["hello"] != "world" {
			t.Fatalf("body=%v", body)
		}
		_ = json.NewEncoder(rw).Encode(map[string]string{"ok": "yes"})
	}))
	defer srv.Close()

	var out map[string]string
	headers := http.Header{"X-Test": []string{"a", "b"}}
	if err := PostJSONWithHeaders(context.Background(), srv.URL, map[string]string{"hello": "world"}, headers, &out); err != nil {
		t.Fatalf("PostJSONWithHeaders error: %v", err)
	}
	if out["ok"] != "yes" {
		t.Fatalf("out=%v", out)
	}
}

func TestPostJSON(t *testing.T) {
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(rw).Encode(map[string]bool{"ok": true})
	}))
	defer srv.Close()

	var out map[string]bool
	if err := PostJSON(context.Background(), srv.URL, map[string]string{"a": "b"}, &out); err != nil {
		t.Fatalf("PostJSON error: %v", err)
	}
	if !out["ok"] {
		t.Fatalf("out=%v", out)
	}
}

func TestGetJSON(t *testing.T) {
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method=%s", r.Method)
		}
		if got := r.Header.Get("X-Test"); got != "value" {
			t.Fatalf("X-Test=%q", got)
		}
		_ = json.NewEncoder(rw).Encode(map[string]string{"ok": "yes"})
	}))
	defer srv.Close()

	var out map[string]string
	if err := GetJSON(context.Background(), srv.URL, http.Header{"X-Test": []string{"value"}}, &out); err != nil {
		t.Fatalf("GetJSON error: %v", err)
	}
	if out["ok"] != "yes" {
		t.Fatalf("out=%v", out)
	}
}

func TestHTTPJSONErrors(t *testing.T) {
	if err := PostJSON(context.Background(), "://bad", map[string]string{}, &map[string]string{}); err == nil {
		t.Fatal("expected bad post url error")
	}
	if err := GetJSON(context.Background(), "://bad", nil, &map[string]string{}); err == nil {
		t.Fatal("expected bad get url error")
	}
	if err := PostJSON(context.Background(), "http://127.0.0.1:1", map[string]string{}, &map[string]string{}); err == nil {
		t.Fatal("expected post transport error")
	}
	if err := GetJSON(context.Background(), "http://127.0.0.1:1", nil, &map[string]string{}); err == nil {
		t.Fatal("expected get transport error")
	}
	if err := PostJSON(context.Background(), "http://example.invalid", func() {}, nil); err == nil {
		t.Fatal("expected marshal error")
	}

	statusSrv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, strings.Repeat("x", 2000), http.StatusTeapot)
	}))
	defer statusSrv.Close()
	if err := PostJSON(context.Background(), statusSrv.URL, map[string]string{}, &map[string]string{}); err == nil || !strings.Contains(err.Error(), "http 418") {
		t.Fatalf("post status err=%v", err)
	}
	if err := GetJSON(context.Background(), statusSrv.URL, nil, &map[string]string{}); err == nil || !strings.Contains(err.Error(), "http 418") {
		t.Fatalf("get status err=%v", err)
	}

	badJSONSrv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write([]byte("{"))
	}))
	defer badJSONSrv.Close()
	if err := PostJSON(context.Background(), badJSONSrv.URL, map[string]string{}, &map[string]string{}); err == nil {
		t.Fatal("expected post decode error")
	}
	if err := GetJSON(context.Background(), badJSONSrv.URL, nil, &map[string]string{}); err == nil {
		t.Fatal("expected get decode error")
	}
}
