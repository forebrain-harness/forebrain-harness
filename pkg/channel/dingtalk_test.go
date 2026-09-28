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

func TestDingTalkNewIDStartStopAndRegister(t *testing.T) {
	d := NewDingTalk(DingTalkConfig{})
	if d == nil || d.ID() != "dingtalk" || d.httpClient == nil || d.httpClient.Timeout == 0 {
		t.Fatalf("dingtalk=%#v", d)
	}
	if err := d.Start(context.Background(), nil, &dingtalkBus{}); err != nil {
		t.Fatalf("disabled Start error: %v", err)
	}
	if d.bridge == nil {
		t.Fatal("Start did not initialize bridge")
	}
	if err := d.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
	if err := NewDingTalk(DingTalkConfig{}).Stop(context.Background()); err != nil {
		t.Fatalf("nil bridge Stop error: %v", err)
	}

}

func TestDingTalkDeliverOutbound(t *testing.T) {
	resetDingtalkGlobals(t)

	if err := NewDingTalk(DingTalkConfig{}).DeliverOutbound(context.Background(), Outbound{SessionID: " "}); err == nil {
		t.Fatal("expected missing session error")
	}
	if err := NewDingTalk(DingTalkConfig{}).DeliverOutbound(context.Background(), Outbound{SessionID: "user1"}); err == nil {
		t.Fatal("expected token error")
	}

	d := NewDingTalk(DingTalkConfig{})
	d.token = "tok"
	d.tokenExpires = time.Now().Add(time.Hour)
	dingtalkMarshalObjectPayload = func(any) ([]byte, error) {
		return nil, errors.New("marshal failed")
	}
	if err := d.DeliverOutbound(context.Background(), Outbound{SessionID: "user1"}); err == nil {
		t.Fatal("expected marshal error")
	}
	resetDingtalkGlobals(t)

	d = NewDingTalk(DingTalkConfig{})
	d.token = "tok"
	d.tokenExpires = time.Now().Add(time.Hour)
	dingtalkBatchSendURL = "://bad"
	if err := d.DeliverOutbound(context.Background(), Outbound{SessionID: "user1"}); err == nil {
		t.Fatal("expected request creation error")
	}
	resetDingtalkGlobals(t)

	d = NewDingTalk(DingTalkConfig{})
	d.token = "tok"
	d.tokenExpires = time.Now().Add(time.Hour)
	dingtalkBatchSendURL = "http://127.0.0.1:1"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.DeliverOutbound(ctx, Outbound{SessionID: "user1"}); err == nil {
		t.Fatal("expected client error")
	}
	resetDingtalkGlobals(t)

	bad := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, strings.Repeat("x", 2000), http.StatusBadGateway)
	}))
	defer bad.Close()
	d = NewDingTalk(DingTalkConfig{})
	d.token = "tok"
	d.tokenExpires = time.Now().Add(time.Hour)
	dingtalkBatchSendURL = bad.URL
	err := d.DeliverOutbound(context.Background(), Outbound{SessionID: "user1"})
	if err == nil || !strings.Contains(err.Error(), "dingtalk send status=502") || len(err.Error()) > 1150 {
		t.Fatalf("status error=%v", err)
	}
	resetDingtalkGlobals(t)

	var requests []dingtalkSendRequest
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode send body: %v", err)
		}
		requests = append(requests, dingtalkSendRequest{
			path:        r.URL.Path,
			contentType: r.Header.Get("Content-Type"),
			token:       r.Header.Get("x-acs-dingtalk-access-token"),
			body:        body,
		})
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	dingtalkBatchSendURL = srv.URL + "/batch"
	dingtalkGroupSendURL = srv.URL + "/group"
	d = NewDingTalk(DingTalkConfig{ClientID: "robot"})
	d.token = "tok"
	d.tokenExpires = time.Now().Add(time.Hour)
	if err := d.DeliverOutbound(context.Background(), Outbound{SessionID: " user1 ", Text: ` hi "there" `}); err != nil {
		t.Fatalf("user DeliverOutbound error: %v", err)
	}
	if err := d.DeliverOutbound(context.Background(), Outbound{SessionID: " cid-group ", Text: " group "}); err != nil {
		t.Fatalf("group DeliverOutbound error: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests=%+v", requests)
	}
	if requests[0].path != "/batch" || requests[0].contentType != "application/json" || requests[0].token != "tok" {
		t.Fatalf("request0 headers/path=%+v", requests[0])
	}
	if requests[0].body["robotCode"] != "robot" || requests[0].body["msgKey"] != "sampleText" || requests[0].body["msgParam"] != `{"content":"hi \"there\""}` {
		t.Fatalf("request0 body=%v", requests[0].body)
	}
	userIDs, ok := requests[0].body["userIds"].([]any)
	if !ok || len(userIDs) != 1 || userIDs[0] != "user1" {
		t.Fatalf("userIds=%v", requests[0].body["userIds"])
	}
	if requests[1].path != "/group" || requests[1].body["openConversationId"] != "cid-group" {
		t.Fatalf("request1=%+v", requests[1])
	}
}

func TestDingTalkResolveHandshake(t *testing.T) {
	resetDingtalkGlobals(t)

	if _, err := NewDingTalk(DingTalkConfig{}).resolveHandshake(context.Background()); err == nil {
		t.Fatal("expected missing credentials error")
	}

	dingtalkPostJSON = func(context.Context, string, any, any) error {
		return errors.New("token failed")
	}
	if _, err := NewDingTalk(DingTalkConfig{ClientID: "id", ClientSecret: "secret"}).resolveHandshake(context.Background()); err == nil {
		t.Fatal("expected token error")
	}
	resetDingtalkGlobals(t)

	dingtalkPostJSON = func(_ context.Context, gotURL string, body any, out any) error {
		if gotURL != dingtalkAccessTokenURL {
			t.Fatalf("token url=%q", gotURL)
		}
		req := body.(map[string]string)
		if req["appKey"] != "id" || req["appSecret"] != "secret" {
			t.Fatalf("token body=%v", req)
		}
		target := out.(*struct {
			AccessToken string `json:"accessToken"`
			ExpireIn    int64  `json:"expireIn"`
		})
		target.AccessToken = " tok "
		target.ExpireIn = 3600
		return nil
	}
	dingtalkPostJSONWithHeaders = func(context.Context, string, any, http.Header, any) error {
		return errors.New("connection failed")
	}
	if _, err := NewDingTalk(DingTalkConfig{ClientID: "id", ClientSecret: "secret"}).resolveHandshake(context.Background()); err == nil {
		t.Fatal("expected connection error")
	}

	dingtalkPostJSONWithHeaders = func(_ context.Context, gotURL string, body any, headers http.Header, out any) error {
		if gotURL != dingtalkOpenConnectionURL || headers.Get("Authorization") != "Bearer tok" {
			t.Fatalf("connection url=%q auth=%q", gotURL, headers.Get("Authorization"))
		}
		req := body.(map[string]any)
		if req["clientId"] != "id" || req["clientSecret"] != "secret" || req["ua"] != "forebrain" {
			t.Fatalf("connection body=%v", req)
		}
		target := out.(*struct {
			Endpoint string `json:"endpoint"`
			Ticket   string `json:"ticket"`
		})
		target.Endpoint = " "
		return nil
	}
	if _, err := NewDingTalk(DingTalkConfig{ClientID: "id", ClientSecret: "secret"}).resolveHandshake(context.Background()); err == nil {
		t.Fatal("expected empty endpoint error")
	}

	dingtalkPostJSONWithHeaders = func(_ context.Context, _ string, _ any, _ http.Header, out any) error {
		target := out.(*struct {
			Endpoint string `json:"endpoint"`
			Ticket   string `json:"ticket"`
		})
		target.Endpoint = "wss://example.test/ws?x=1"
		target.Ticket = " ticket "
		return nil
	}
	hs, err := NewDingTalk(DingTalkConfig{ClientID: "id", ClientSecret: "secret"}).resolveHandshake(context.Background())
	if err != nil {
		t.Fatalf("resolveHandshake error: %v", err)
	}
	if hs.Header.Get("Authorization") != "Bearer tok" || !strings.Contains(hs.WSURL, "ticket=ticket") || !strings.Contains(hs.WSURL, "x=1") {
		t.Fatalf("handshake=%+v", hs)
	}

	dingtalkPostJSONWithHeaders = func(_ context.Context, _ string, _ any, _ http.Header, out any) error {
		target := out.(*struct {
			Endpoint string `json:"endpoint"`
			Ticket   string `json:"ticket"`
		})
		target.Endpoint = "%"
		target.Ticket = "ticket"
		return nil
	}
	d := NewDingTalk(DingTalkConfig{ClientID: "id", ClientSecret: "secret"})
	d.token = "tok"
	d.tokenExpires = time.Now().Add(time.Hour)
	hs, err = d.resolveHandshake(context.Background())
	if err != nil || hs.WSURL != "%" {
		t.Fatalf("invalid endpoint handshake=%+v err=%v", hs, err)
	}
}

func TestDingTalkGetAccessToken(t *testing.T) {
	resetDingtalkGlobals(t)

	d := NewDingTalk(DingTalkConfig{})
	d.token = " cached "
	d.tokenExpires = time.Now().Add(time.Hour)
	token, err := d.getAccessToken(context.Background())
	if err != nil || token != " cached " {
		t.Fatalf("cached token=%q err=%v", token, err)
	}

	if _, err := NewDingTalk(DingTalkConfig{}).getAccessToken(context.Background()); err == nil {
		t.Fatal("expected missing credentials error")
	}

	dingtalkPostJSON = func(context.Context, string, any, any) error {
		return errors.New("post failed")
	}
	if _, err := NewDingTalk(DingTalkConfig{ClientID: "id", ClientSecret: "secret"}).getAccessToken(context.Background()); err == nil {
		t.Fatal("expected post error")
	}

	dingtalkPostJSON = func(context.Context, string, any, any) error {
		return nil
	}
	if _, err := NewDingTalk(DingTalkConfig{ClientID: "id", ClientSecret: "secret"}).getAccessToken(context.Background()); err == nil {
		t.Fatal("expected empty token error")
	}

	dingtalkPostJSON = func(_ context.Context, _ string, _ any, out any) error {
		target := out.(*struct {
			AccessToken string `json:"accessToken"`
			ExpireIn    int64  `json:"expireIn"`
		})
		target.AccessToken = " fresh "
		target.ExpireIn = 120
		return nil
	}
	d = NewDingTalk(DingTalkConfig{ClientID: "id", ClientSecret: "secret"})
	token, err = d.getAccessToken(context.Background())
	if err != nil || token != "fresh" || d.token != "fresh" || time.Until(d.tokenExpires) <= time.Minute {
		t.Fatalf("fresh token=%q cached=%q expires=%v err=%v", token, d.token, d.tokenExpires, err)
	}
}

func TestDingTalkEscapeJSONString(t *testing.T) {
	if got := escapeJSONString(` hi "there"\ `); got != ` hi \"there\"\\ ` {
		t.Fatalf("escapeJSONString=%q", got)
	}
	oldMarshal := dingtalkMarshalTextPayload
	t.Cleanup(func() { dingtalkMarshalTextPayload = oldMarshal })
	dingtalkMarshalTextPayload = func(any) ([]byte, error) {
		return []byte("x"), nil
	}
	if got := escapeJSONString("raw"); got != "raw" {
		t.Fatalf("short marshal fallback=%q", got)
	}
	dingtalkMarshalTextPayload = func(any) ([]byte, error) {
		return nil, errors.New("marshal failed")
	}
	if got := escapeJSONString("raw"); got != "raw" {
		t.Fatalf("marshal error fallback=%q", got)
	}
}

func resetDingtalkGlobals(t *testing.T) {
	t.Helper()
	dingtalkAccessTokenURL = "https://api.dingtalk.com/v1.0/oauth2/accessToken"
	dingtalkOpenConnectionURL = "https://api.dingtalk.com/v1.0/gateway/connections/open"
	dingtalkBatchSendURL = "https://api.dingtalk.com/v1.0/robot/oToMessages/batchSend"
	dingtalkGroupSendURL = "https://api.dingtalk.com/v1.0/robot/groupMessages/send"
	dingtalkPostJSON = func(ctx context.Context, url string, body any, out any) error {
		return errors.New("unexpected dingtalkPostJSON call")
	}
	dingtalkPostJSONWithHeaders = func(ctx context.Context, url string, body any, headers http.Header, out any) error {
		return errors.New("unexpected dingtalkPostJSONWithHeaders call")
	}
	dingtalkMarshalTextPayload = json.Marshal
	dingtalkMarshalObjectPayload = json.Marshal
	t.Cleanup(func() {
		dingtalkAccessTokenURL = "https://api.dingtalk.com/v1.0/oauth2/accessToken"
		dingtalkOpenConnectionURL = "https://api.dingtalk.com/v1.0/gateway/connections/open"
		dingtalkBatchSendURL = "https://api.dingtalk.com/v1.0/robot/oToMessages/batchSend"
		dingtalkGroupSendURL = "https://api.dingtalk.com/v1.0/robot/groupMessages/send"
		dingtalkPostJSON = func(ctx context.Context, url string, body any, out any) error {
			return errors.New("unexpected dingtalkPostJSON call")
		}
		dingtalkPostJSONWithHeaders = func(ctx context.Context, url string, body any, headers http.Header, out any) error {
			return errors.New("unexpected dingtalkPostJSONWithHeaders call")
		}
		dingtalkMarshalTextPayload = json.Marshal
		dingtalkMarshalObjectPayload = json.Marshal
	})
}

type dingtalkSendRequest struct {
	path        string
	contentType string
	token       string
	body        map[string]any
}

type dingtalkBus struct{}

func (d *dingtalkBus) PublishInbound(context.Context, Inbound) error {
	return nil
}
