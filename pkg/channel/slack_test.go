package channel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/testutil"
)

func TestSlackNewIDStartStopAndRegister(t *testing.T) {
	s := NewSlack(SlackConfig{})
	if s == nil || s.ID() != "slack" || s.httpClient == nil || s.httpClient.Timeout == 0 {
		t.Fatalf("slack=%#v", s)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}

	routes := map[string]http.HandlerFunc{}
	add := func(method, path string, hf http.HandlerFunc) {
		routes[method+" "+path] = hf
	}
	bus := &slackBus{}
	if err := NewSlack(SlackConfig{Enabled: false}).Start(context.Background(), add, bus); err != nil {
		t.Fatalf("disabled Start error: %v", err)
	}
	if len(routes) != 0 {
		t.Fatalf("disabled routes=%v", routes)
	}

	enabled := NewSlack(SlackConfig{Enabled: true})
	if err := enabled.Start(context.Background(), add, bus); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if enabled.bus != bus || routes["POST /channels/slack/events"] == nil || routes["GET /channels/slack/health"] == nil {
		t.Fatalf("bus=%#v routes=%v", enabled.bus, routes)
	}
	rr := httptest.NewRecorder()
	routes["GET /channels/slack/health"](rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("health code=%d body=%q", rr.Code, rr.Body.String())
	}

	routes = map[string]http.HandlerFunc{}
	if err := NewSlack(SlackConfig{Enabled: true, InboundPath: " /slack "}).Start(context.Background(), add, bus); err != nil {
		t.Fatalf("custom Start error: %v", err)
	}
	if routes["POST /slack"] == nil {
		t.Fatalf("custom routes=%v", routes)
	}

}

func TestSlackDeliverOutbound(t *testing.T) {
	if err := NewSlack(SlackConfig{}).DeliverOutbound(context.Background(), Outbound{SessionID: " "}); err == nil {
		t.Fatal("expected invalid session error")
	}
	if err := NewSlack(SlackConfig{}).DeliverOutbound(context.Background(), Outbound{SessionID: "C1"}); err == nil {
		t.Fatal("expected client error for default url without token")
	}

	orig := slackPostMessageURL
	t.Cleanup(func() { slackPostMessageURL = orig })
	slackPostMessageURL = "://bad"
	if err := NewSlack(SlackConfig{BotToken: "token"}).DeliverOutbound(context.Background(), Outbound{SessionID: "C1"}); err == nil {
		t.Fatal("expected request creation error")
	}

	var gotAuth string
	var gotForm url.Values
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		data, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(data))
		_, _ = rw.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	slackPostMessageURL = srv.URL

	err := NewSlack(SlackConfig{BotToken: " token "}).DeliverOutbound(context.Background(), Outbound{SessionID: " C1 : 123.4 ", Text: " hello "})
	if err != nil {
		t.Fatalf("DeliverOutbound error: %v", err)
	}
	if gotAuth != "Bearer token" || gotForm.Get("channel") != "C1" || gotForm.Get("text") != "hello" || gotForm.Get("thread_ts") != "123.4" {
		t.Fatalf("auth=%q form=%v", gotAuth, gotForm)
	}

	slackPostMessageURL = "http://127.0.0.1:1"
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewSlack(SlackConfig{BotToken: "token"}).DeliverOutbound(cancelCtx, Outbound{SessionID: "C1"}); err == nil {
		t.Fatal("expected client do error")
	}

	badJSON := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write([]byte(`{`))
	}))
	defer badJSON.Close()
	slackPostMessageURL = badJSON.URL
	if err := NewSlack(SlackConfig{BotToken: "token"}).DeliverOutbound(context.Background(), Outbound{SessionID: "C1"}); err == nil {
		t.Fatal("expected decode error")
	}

	apiError := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write([]byte(`{"ok":false,"error":"bad_auth"}`))
	}))
	defer apiError.Close()
	slackPostMessageURL = apiError.URL
	err = NewSlack(SlackConfig{BotToken: "token"}).DeliverOutbound(context.Background(), Outbound{SessionID: "C1"})
	if err == nil || !strings.Contains(err.Error(), "bad_auth") {
		t.Fatalf("api err=%v", err)
	}
}

func TestSlackHandleEvents(t *testing.T) {
	secret := "secret"
	body := `{"event":{"type":"message","channel":"C1","text":" hi ","ts":"111.1"}}`
	signed := signSlackBody(t, body, secret, time.Unix(1000, 0))
	tests := []struct {
		name       string
		cfg        SlackConfig
		body       string
		headers    map[string]string
		reader     io.Reader
		wantCode   int
		wantEvents int
		wantSID    string
		wantText   string
	}{
		{name: "disabled", cfg: SlackConfig{}, body: body, wantCode: http.StatusNotFound},
		{name: "read error", cfg: SlackConfig{Enabled: true}, reader: slackErrReader{}, wantCode: http.StatusBadRequest},
		{name: "bad signature", cfg: SlackConfig{Enabled: true, Secret: secret}, body: body, wantCode: http.StatusForbidden},
		{name: "bad json", cfg: SlackConfig{Enabled: true}, body: "{", wantCode: http.StatusBadRequest},
		{name: "url verification", cfg: SlackConfig{Enabled: true}, body: `{"type":"url_verification","challenge":"abc"}`, wantCode: http.StatusOK},
		{name: "message", cfg: SlackConfig{Enabled: true, Secret: secret}, body: body, headers: signed, wantCode: http.StatusOK, wantEvents: 1, wantSID: "C1:111.1", wantText: "hi"},
		{name: "thread message", cfg: SlackConfig{Enabled: true}, body: `{"event":{"type":"message","channel":"C1","text":"hi","thread_ts":"222.2"}}`, wantCode: http.StatusOK, wantEvents: 1, wantSID: "C1:222.2", wantText: "hi"},
		{name: "bot ignored", cfg: SlackConfig{Enabled: true}, body: `{"event":{"type":"message","channel":"C1","text":"hi","bot_id":"B1"}}`, wantCode: http.StatusOK},
		{name: "missing bus ignored", cfg: SlackConfig{Enabled: true}, body: body, wantCode: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bus := &slackBus{err: errors.New("ignored")}
			s := NewSlack(tt.cfg)
			if tt.name != "missing bus ignored" {
				s.bus = bus
			}
			reader := tt.reader
			if reader == nil {
				reader = strings.NewReader(tt.body)
			}
			req := httptest.NewRequest(http.MethodPost, "/slack", reader)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			rr := httptest.NewRecorder()
			withSlackNow(t, time.Unix(1000, 0), func() {
				s.handleEvents(rr, req)
			})
			if rr.Code != tt.wantCode {
				t.Fatalf("code=%d body=%q", rr.Code, rr.Body.String())
			}
			if tt.name == "url verification" && !strings.Contains(rr.Body.String(), "challenge") {
				t.Fatalf("url verification body=%q", rr.Body.String())
			}
			if len(bus.events) != tt.wantEvents {
				t.Fatalf("events=%+v", bus.events)
			}
			if tt.wantEvents == 1 {
				ev := bus.events[0]
				if ev.ChannelID != "slack" || ev.SessionID != tt.wantSID || ev.Text != tt.wantText {
					t.Fatalf("event=%+v", ev)
				}
			}
		})
	}
}

func TestSlackVerifySlackSignature(t *testing.T) {
	body := []byte(`{"ok":true}`)
	secret := "secret"
	req := httptest.NewRequest(http.MethodPost, "/slack", nil)
	if verifySlackSignature(req, body, secret) {
		t.Fatal("missing headers should fail")
	}
	req.Header.Set("X-Slack-Request-Timestamp", "bad")
	req.Header.Set("X-Slack-Signature", "sig")
	if verifySlackSignature(req, body, secret) {
		t.Fatal("bad timestamp should fail")
	}
	req = httptest.NewRequest(http.MethodPost, "/slack", nil)
	req.Header.Set("X-Slack-Request-Timestamp", "1")
	req.Header.Set("X-Slack-Signature", "sig")
	withSlackNow(t, time.Unix(1000, 0), func() {
		if verifySlackSignature(req, body, secret) {
			t.Fatal("old timestamp should fail")
		}
	})
	headers := signSlackBody(t, string(body), secret, time.Unix(1000, 0))
	req = httptest.NewRequest(http.MethodPost, "/slack", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	withSlackNow(t, time.Unix(1000, 0), func() {
		if !verifySlackSignature(req, body, secret) {
			t.Fatal("valid signature should pass")
		}
	})
}

func TestSlackHelpers(t *testing.T) {
	ch, ts := parseSlackSession(" C1 : 123 ")
	if ch != "C1" || ts != "123" {
		t.Fatalf("session ch=%q ts=%q", ch, ts)
	}
	ch, ts = parseSlackSession(" ")
	if ch != "" || ts != "" {
		t.Fatalf("empty session ch=%q ts=%q", ch, ts)
	}
	ch, ts = parseSlackSession("C1")
	if ch != "C1" || ts != "" {
		t.Fatalf("single session ch=%q ts=%q", ch, ts)
	}
	if slackString(nil) != "" || slackString("x") != "x" || slackString(float64(1.5)) != "1.5" || slackString(map[string]any{"a": "b"}) != `{"a":"b"}` {
		t.Fatalf("slackString mismatch")
	}
	if slackString(make(chan int)) != "" {
		t.Fatal("slackString marshal error should be empty")
	}
	if !slackBool(true) || slackBool("true") {
		t.Fatal("slackBool mismatch")
	}
}

func signSlackBody(t *testing.T, body, secret string, ts time.Time) map[string]string {
	t.Helper()
	stamp := strconvFormat(ts.Unix())
	base := "v0:" + stamp + ":" + body
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(base))
	return map[string]string{
		"X-Slack-Request-Timestamp": stamp,
		"X-Slack-Signature":         "v0=" + hex.EncodeToString(mac.Sum(nil)),
	}
}

func strconvFormat(n int64) string {
	return strconv.FormatInt(n, 10)
}

func withSlackNow(t *testing.T, now time.Time, fn func()) {
	t.Helper()
	orig := slackNow
	slackNow = func() time.Time { return now }
	t.Cleanup(func() { slackNow = orig })
	fn()
	slackNow = orig
}

type slackBus struct {
	events []Inbound
	err    error
}

func (s *slackBus) PublishInbound(_ context.Context, m Inbound) error {
	s.events = append(s.events, m)
	return s.err
}

type slackErrReader struct{}

func (slackErrReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}
