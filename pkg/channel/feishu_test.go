package channel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/testutil"
)

func TestFeishuNewIDStartStopAndRegister(t *testing.T) {
	f := NewFeishu(FeishuConfig{})
	if f == nil || f.ID() != "feishu" || f.httpClient == nil || f.httpClient.Timeout == 0 {
		t.Fatalf("feishu=%#v", f)
	}
	if err := f.Start(context.Background(), nil, &feishuBus{}); err != nil {
		t.Fatalf("default websocket Start error: %v", err)
	}
	if f.bridge == nil {
		t.Fatal("Start did not initialize websocket bridge")
	}
	if err := f.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}

	disabledMode := NewFeishu(FeishuConfig{ConnectionMode: "http"})
	if err := disabledMode.Start(context.Background(), nil, nil); err != nil {
		t.Fatalf("non-websocket Start error: %v", err)
	}
	if disabledMode.bridge != nil {
		t.Fatal("non-websocket mode should not initialize bridge")
	}
	if err := NewFeishu(FeishuConfig{}).Stop(context.Background()); err != nil {
		t.Fatalf("nil bridge Stop error: %v", err)
	}

}

func TestFeishuStartUsesLarkWebSocketURL(t *testing.T) {
	resetFeishuGlobals(t)
	feishuLarkWSURL = "ws://lark.test/ws"
	f := NewFeishu(FeishuConfig{Domain: " lark "})
	if err := f.Start(context.Background(), nil, nil); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if f.bridge == nil {
		t.Fatal("bridge was not initialized")
	}
}

func TestFeishuDeliverOutbound(t *testing.T) {
	resetFeishuGlobals(t)

	if err := NewFeishu(FeishuConfig{}).DeliverOutbound(context.Background(), Outbound{SessionID: " "}); err == nil {
		t.Fatal("expected invalid session error")
	}
	if err := NewFeishu(FeishuConfig{}).DeliverOutbound(context.Background(), Outbound{SessionID: "chat:c1"}); err == nil {
		t.Fatal("expected token error")
	}

	f := NewFeishu(FeishuConfig{})
	f.token = "tok"
	f.tokenExpires = time.Now().Add(time.Hour)
	feishuMarshalTextContent = func(any) ([]byte, error) { return nil, errors.New("content failed") }
	if err := f.DeliverOutbound(context.Background(), Outbound{SessionID: "chat:c1"}); err == nil {
		t.Fatal("expected content marshal error")
	}
	resetFeishuGlobals(t)

	f = NewFeishu(FeishuConfig{})
	f.token = "tok"
	f.tokenExpires = time.Now().Add(time.Hour)
	feishuMarshalSendBody = func(any) ([]byte, error) { return nil, errors.New("body failed") }
	if err := f.DeliverOutbound(context.Background(), Outbound{SessionID: "chat:c1"}); err == nil {
		t.Fatal("expected body marshal error")
	}
	resetFeishuGlobals(t)

	f = NewFeishu(FeishuConfig{})
	f.token = "tok"
	f.tokenExpires = time.Now().Add(time.Hour)
	feishuOpenAPIBase = "://bad"
	if err := f.DeliverOutbound(context.Background(), Outbound{SessionID: "chat:c1"}); err == nil {
		t.Fatal("expected request creation error")
	}
	resetFeishuGlobals(t)

	f = NewFeishu(FeishuConfig{})
	f.token = "tok"
	f.tokenExpires = time.Now().Add(time.Hour)
	feishuOpenAPIBase = "http://127.0.0.1:1"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.DeliverOutbound(ctx, Outbound{SessionID: "chat:c1"}); err == nil {
		t.Fatal("expected client error")
	}
	resetFeishuGlobals(t)

	statusSrv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, strings.Repeat("x", 2000), http.StatusBadGateway)
	}))
	defer statusSrv.Close()
	f = NewFeishu(FeishuConfig{})
	f.token = "tok"
	f.tokenExpires = time.Now().Add(time.Hour)
	feishuOpenAPIBase = statusSrv.URL
	err := f.DeliverOutbound(context.Background(), Outbound{SessionID: "chat:c1"})
	if err == nil || !strings.Contains(err.Error(), "feishu send status=502") || len(err.Error()) > 1150 {
		t.Fatalf("status error=%v", err)
	}
	resetFeishuGlobals(t)

	apiError := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(rw).Encode(map[string]any{"code": 999, "msg": "bad"})
	}))
	defer apiError.Close()
	f = NewFeishu(FeishuConfig{})
	f.token = "tok"
	f.tokenExpires = time.Now().Add(time.Hour)
	feishuOpenAPIBase = apiError.URL
	err = f.DeliverOutbound(context.Background(), Outbound{SessionID: "chat:c1"})
	if err == nil || !strings.Contains(err.Error(), "feishu send failed code=999") {
		t.Fatalf("api error=%v", err)
	}
	resetFeishuGlobals(t)

	badJSON := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write([]byte(`{`))
	}))
	defer badJSON.Close()
	f = NewFeishu(FeishuConfig{})
	f.token = "tok"
	f.tokenExpires = time.Now().Add(time.Hour)
	feishuOpenAPIBase = badJSON.URL
	if err := f.DeliverOutbound(context.Background(), Outbound{SessionID: "chat:c1"}); err != nil {
		t.Fatalf("decode errors are ignored, got %v", err)
	}
	resetFeishuGlobals(t)

	var gotPath, gotQuery, gotAuth, gotContentType string
	var gotBody map[string]string
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode send body: %v", err)
		}
		_ = json.NewEncoder(rw).Encode(map[string]int{"code": 0})
	}))
	defer srv.Close()
	f = NewFeishu(FeishuConfig{Domain: "lark"})
	f.token = "tok"
	f.tokenExpires = time.Now().Add(time.Hour)
	feishuLarkOpenAPIBase = srv.URL
	if err := f.DeliverOutbound(context.Background(), Outbound{SessionID: " chat:c1 ", Text: " hi "}); err != nil {
		t.Fatalf("DeliverOutbound error: %v", err)
	}
	if gotPath != "/im/v1/messages" || gotQuery != "receive_id_type=chat_id" || gotAuth != "Bearer tok" || gotContentType != "application/json" {
		t.Fatalf("path=%q query=%q auth=%q contentType=%q", gotPath, gotQuery, gotAuth, gotContentType)
	}
	if gotBody["receive_id"] != "c1" || gotBody["msg_type"] != "text" || gotBody["content"] != `{"text":"hi"}` {
		t.Fatalf("body=%v", gotBody)
	}
}

func TestFeishuResolveHandshake(t *testing.T) {
	resetFeishuGlobals(t)

	if _, err := NewFeishu(FeishuConfig{}).resolveHandshake(context.Background()); err == nil {
		t.Fatal("expected missing credentials error")
	}
	feishuPostJSON = func(context.Context, string, any, any) error {
		return errors.New("post failed")
	}
	if _, err := NewFeishu(FeishuConfig{AppID: "id", AppSecret: "secret"}).resolveHandshake(context.Background()); err == nil {
		t.Fatal("expected post error")
	}

	feishuPostJSON = func(context.Context, string, any, any) error {
		return nil
	}
	if _, err := NewFeishu(FeishuConfig{AppID: "id", AppSecret: "secret"}).resolveHandshake(context.Background()); err == nil {
		t.Fatal("expected token response error")
	}

	feishuPostJSON = func(_ context.Context, gotURL string, body any, out any) error {
		if gotURL != feishuLarkOpenAPIBase+"/auth/v3/tenant_access_token/internal" {
			t.Fatalf("token url=%q", gotURL)
		}
		req := body.(map[string]string)
		if req["app_id"] != "id" || req["app_secret"] != "secret" {
			t.Fatalf("token body=%v", req)
		}
		target := out.(*struct {
			Code              int    `json:"code"`
			Msg               string `json:"msg"`
			TenantAccessToken string `json:"tenant_access_token"`
		})
		target.Code = 0
		target.TenantAccessToken = " tenant "
		return nil
	}
	hs, err := NewFeishu(FeishuConfig{AppID: "id", AppSecret: "secret", Domain: "lark"}).resolveHandshake(context.Background())
	if err != nil {
		t.Fatalf("resolveHandshake error: %v", err)
	}
	if hs.WSURL != feishuLarkWSURL || hs.Header.Get("Authorization") != "Bearer tenant" {
		t.Fatalf("handshake=%+v", hs)
	}
}

func TestFeishuGetTenantToken(t *testing.T) {
	resetFeishuGlobals(t)

	f := NewFeishu(FeishuConfig{})
	f.token = " cached "
	f.tokenExpires = time.Now().Add(time.Hour)
	token, err := f.getTenantToken(context.Background())
	if err != nil || token != " cached " {
		t.Fatalf("cached token=%q err=%v", token, err)
	}

	if _, err := NewFeishu(FeishuConfig{}).getTenantToken(context.Background()); err == nil {
		t.Fatal("expected missing credentials error")
	}
	feishuPostJSON = func(context.Context, string, any, any) error {
		return errors.New("post failed")
	}
	if _, err := NewFeishu(FeishuConfig{AppID: "id", AppSecret: "secret"}).getTenantToken(context.Background()); err == nil {
		t.Fatal("expected post error")
	}
	feishuPostJSON = func(context.Context, string, any, any) error {
		return nil
	}
	if _, err := NewFeishu(FeishuConfig{AppID: "id", AppSecret: "secret"}).getTenantToken(context.Background()); err == nil {
		t.Fatal("expected response error")
	}

	feishuPostJSON = func(_ context.Context, gotURL string, body any, out any) error {
		if gotURL != feishuOpenAPIBase+"/auth/v3/tenant_access_token/internal" {
			t.Fatalf("token url=%q", gotURL)
		}
		req := body.(map[string]string)
		if req["app_id"] != "id" || req["app_secret"] != "secret" {
			t.Fatalf("token body=%v", req)
		}
		target := out.(*struct {
			Code              int    `json:"code"`
			Msg               string `json:"msg"`
			TenantAccessToken string `json:"tenant_access_token"`
			Expire            int64  `json:"expire"`
		})
		target.Code = 0
		target.TenantAccessToken = " fresh "
		target.Expire = 120
		return nil
	}
	f = NewFeishu(FeishuConfig{AppID: "id", AppSecret: "secret"})
	token, err = f.getTenantToken(context.Background())
	if err != nil || token != "fresh" || f.token != "fresh" || time.Until(f.tokenExpires) <= time.Minute {
		t.Fatalf("fresh token=%q cached=%q expires=%v err=%v", token, f.token, f.tokenExpires, err)
	}

	feishuPostJSON = func(_ context.Context, gotURL string, _ any, out any) error {
		if gotURL != feishuLarkOpenAPIBase+"/auth/v3/tenant_access_token/internal" {
			t.Fatalf("lark token url=%q", gotURL)
		}
		target := out.(*struct {
			Code              int    `json:"code"`
			Msg               string `json:"msg"`
			TenantAccessToken string `json:"tenant_access_token"`
			Expire            int64  `json:"expire"`
		})
		target.TenantAccessToken = "lark-token"
		return nil
	}
	token, err = NewFeishu(FeishuConfig{AppID: "id", AppSecret: "secret", Domain: "lark"}).getTenantToken(context.Background())
	if err != nil || token != "lark-token" {
		t.Fatalf("lark token=%q err=%v", token, err)
	}
}

func TestFeishuParseFeishuSession(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: " chat:c1 ", want: "c1"},
		{in: "open_id:u1", want: "open_id"},
		{in: "c1", want: "c1"},
		{in: " ", want: ""},
	}
	for _, tt := range tests {
		if got := parseFeishuSession(tt.in); got != tt.want {
			t.Fatalf("parseFeishuSession(%q)=%q want %q", tt.in, got, tt.want)
		}
	}
}

func resetFeishuGlobals(t *testing.T) {
	t.Helper()
	feishuOpenAPIBase = "https://open.feishu.cn/open-apis"
	feishuLarkOpenAPIBase = "https://open.larksuite.com/open-apis"
	feishuWSURL = "wss://open.feishu.cn/open-apis/ws/v1"
	feishuLarkWSURL = "wss://open.larksuite.com/open-apis/ws/v1"
	feishuPostJSON = func(context.Context, string, any, any) error {
		return errors.New("unexpected feishuPostJSON call")
	}
	feishuMarshalTextContent = json.Marshal
	feishuMarshalSendBody = json.Marshal
	t.Cleanup(func() {
		feishuOpenAPIBase = "https://open.feishu.cn/open-apis"
		feishuLarkOpenAPIBase = "https://open.larksuite.com/open-apis"
		feishuWSURL = "wss://open.feishu.cn/open-apis/ws/v1"
		feishuLarkWSURL = "wss://open.larksuite.com/open-apis/ws/v1"
		feishuPostJSON = func(context.Context, string, any, any) error {
			return errors.New("unexpected feishuPostJSON call")
		}
		feishuMarshalTextContent = json.Marshal
		feishuMarshalSendBody = json.Marshal
	})
}

type feishuBus struct{}

func (b *feishuBus) PublishInbound(context.Context, Inbound) error {
	return nil
}
