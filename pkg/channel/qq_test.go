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

func TestNewIDStartStopAndRegister(t *testing.T) {
	q := NewQQ(QQConfig{})
	if q == nil || q.ID() != "qq" || q.httpClient == nil || q.httpClient.Timeout == 0 {
		t.Fatalf("qq=%#v", q)
	}
	if err := q.Start(context.Background(), nil, &qqBus{}); err != nil {
		t.Fatalf("disabled Start error: %v", err)
	}
	if q.bridge == nil {
		t.Fatal("Start did not initialize bridge")
	}
	if err := q.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
	if err := NewQQ(QQConfig{}).Stop(context.Background()); err != nil {
		t.Fatalf("nil bridge Stop error: %v", err)
	}
}

func TestDeliverOutbound(t *testing.T) {
	resetQQGlobals(t)

	if err := NewQQ(QQConfig{}).DeliverOutbound(context.Background(), Outbound{SessionID: " "}); err == nil {
		t.Fatal("expected invalid session error")
	}
	if err := NewQQ(QQConfig{}).DeliverOutbound(context.Background(), Outbound{SessionID: "user:u1"}); err == nil {
		t.Fatal("expected token error")
	}

	q := NewQQ(QQConfig{})
	q.token = "tok"
	q.tokenExpires = time.Now().Add(time.Hour)
	qqMarshalBody = func(any) ([]byte, error) { return nil, errors.New("marshal failed") }
	if err := q.DeliverOutbound(context.Background(), Outbound{SessionID: "user:u1"}); err == nil {
		t.Fatal("expected marshal error")
	}
	resetQQGlobals(t)

	q = NewQQ(QQConfig{})
	q.token = "tok"
	q.tokenExpires = time.Now().Add(time.Hour)
	qqAPIBase = "://bad"
	if err := q.DeliverOutbound(context.Background(), Outbound{SessionID: "user:u1"}); err == nil {
		t.Fatal("expected request error")
	}
	resetQQGlobals(t)

	q = NewQQ(QQConfig{})
	q.token = "tok"
	q.tokenExpires = time.Now().Add(time.Hour)
	qqAPIBase = "http://127.0.0.1:1"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.DeliverOutbound(ctx, Outbound{SessionID: "user:u1"}); err == nil {
		t.Fatal("expected client error")
	}
	resetQQGlobals(t)

	bad := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, strings.Repeat("x", 2000), http.StatusBadGateway)
	}))
	defer bad.Close()
	q = NewQQ(QQConfig{})
	q.token = "tok"
	q.tokenExpires = time.Now().Add(time.Hour)
	qqAPIBase = bad.URL
	err := q.DeliverOutbound(context.Background(), Outbound{SessionID: "user:u1"})
	if err == nil || !strings.Contains(err.Error(), "qq send status=502") || len(err.Error()) > 1150 {
		t.Fatalf("status error=%v", err)
	}
	resetQQGlobals(t)

	var requests []qqSendRequest
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		requests = append(requests, qqSendRequest{
			path:        r.URL.Path,
			auth:        r.Header.Get("Authorization"),
			contentType: r.Header.Get("Content-Type"),
			body:        body,
		})
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	q = NewQQ(QQConfig{AppID: "appid"})
	q.token = "tok"
	q.tokenExpires = time.Now().Add(time.Hour)
	qqAPIBase = srv.URL
	if err := q.DeliverOutbound(context.Background(), Outbound{SessionID: " user:u1 ", Text: " hi "}); err != nil {
		t.Fatalf("user DeliverOutbound error: %v", err)
	}
	if err := q.DeliverOutbound(context.Background(), Outbound{SessionID: " group:g1 ", Text: " group "}); err != nil {
		t.Fatalf("group DeliverOutbound error: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests=%+v", requests)
	}
	if requests[0].path != "/v2/users/u1/messages" || requests[0].auth != "QQBot appid.tok" || requests[0].contentType != "application/json" || requests[0].body["content"] != "hi" || requests[0].body["msg_type"].(float64) != 0 {
		t.Fatalf("request0=%+v", requests[0])
	}
	if requests[1].path != "/v2/groups/g1/messages" || requests[1].body["content"] != "group" {
		t.Fatalf("request1=%+v", requests[1])
	}
}

func TestResolveHandshake(t *testing.T) {
	resetQQGlobals(t)

	qqPostJSON = func(context.Context, string, any, any) error {
		return errors.New("token failed")
	}
	if _, err := NewQQ(QQConfig{AppID: "id", ClientSecret: "secret"}).resolveHandshake(context.Background()); err == nil {
		t.Fatal("expected token error")
	}

	q := NewQQ(QQConfig{ClientSecret: "secret"})
	q.token = "tok"
	q.tokenExpires = time.Now().Add(time.Hour)
	if _, err := q.resolveHandshake(context.Background()); err == nil {
		t.Fatal("expected missing app id error")
	}

	qqGetJSON = func(context.Context, string, http.Header, any) error {
		return errors.New("gateway failed")
	}
	q = NewQQ(QQConfig{AppID: "id"})
	q.token = "tok"
	q.tokenExpires = time.Now().Add(time.Hour)
	if _, err := q.resolveHandshake(context.Background()); err == nil {
		t.Fatal("expected gateway error")
	}

	qqGetJSON = func(context.Context, string, http.Header, any) error {
		return nil
	}
	if _, err := q.resolveHandshake(context.Background()); err == nil {
		t.Fatal("expected empty websocket url error")
	}

	qqGetJSON = func(_ context.Context, gotURL string, headers http.Header, out any) error {
		if gotURL != qqAPIBase+"/gateway/bot" || headers.Get("Authorization") != "QQBot id.tok" {
			t.Fatalf("gateway url=%q auth=%q", gotURL, headers.Get("Authorization"))
		}
		target := out.(*struct {
			URL string `json:"url"`
		})
		target.URL = "wss://gateway"
		return nil
	}
	hs, err := q.resolveHandshake(context.Background())
	if err != nil {
		t.Fatalf("resolveHandshake error: %v", err)
	}
	if hs.WSURL != "wss://gateway" || hs.Header.Get("Authorization") != "QQBot id.tok" {
		t.Fatalf("handshake=%+v", hs)
	}
}

func TestGetAccessToken(t *testing.T) {
	resetQQGlobals(t)

	q := NewQQ(QQConfig{})
	q.token = " cached "
	q.tokenExpires = time.Now().Add(time.Hour)
	token, err := q.getAccessToken(context.Background())
	if err != nil || token != " cached " {
		t.Fatalf("cached token=%q err=%v", token, err)
	}

	if _, err := NewQQ(QQConfig{}).getAccessToken(context.Background()); err == nil {
		t.Fatal("expected missing credentials error")
	}
	qqPostJSON = func(context.Context, string, any, any) error {
		return errors.New("post failed")
	}
	if _, err := NewQQ(QQConfig{AppID: "id", ClientSecret: "secret"}).getAccessToken(context.Background()); err == nil {
		t.Fatal("expected post error")
	}
	qqPostJSON = func(context.Context, string, any, any) error {
		return nil
	}
	if _, err := NewQQ(QQConfig{AppID: "id", ClientSecret: "secret"}).getAccessToken(context.Background()); err == nil {
		t.Fatal("expected empty token error")
	}
	qqPostJSON = func(_ context.Context, gotURL string, body any, out any) error {
		if gotURL != qqAccessTokenURL {
			t.Fatalf("token url=%q", gotURL)
		}
		req := body.(map[string]string)
		if req["appId"] != "id" || req["clientSecret"] != "secret" {
			t.Fatalf("token body=%v", req)
		}
		target := out.(*struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int64  `json:"expires_in"`
		})
		target.AccessToken = " fresh "
		target.ExpiresIn = 120
		return nil
	}
	q = NewQQ(QQConfig{AppID: "id", ClientSecret: "secret"})
	token, err = q.getAccessToken(context.Background())
	if err != nil || token != "fresh" || q.token != "fresh" || time.Until(q.tokenExpires) <= time.Minute {
		t.Fatalf("fresh token=%q cached=%q expires=%v err=%v", token, q.token, q.tokenExpires, err)
	}
}

func TestParseQQSession(t *testing.T) {
	tests := []struct {
		in        string
		want      string
		wantGroup bool
	}{
		{in: " group:g1 ", want: "g1", wantGroup: true},
		{in: " user:u1 ", want: "u1"},
		{in: "plain", want: "plain"},
		{in: " ", want: ""},
	}
	for _, tt := range tests {
		got, group := parseQQSession(tt.in)
		if got != tt.want || group != tt.wantGroup {
			t.Fatalf("parseQQSession(%q)=(%q,%v) want (%q,%v)", tt.in, got, group, tt.want, tt.wantGroup)
		}
	}
}

func resetQQGlobals(t *testing.T) {
	t.Helper()
	qqAPIBase = "https://api.sgroup.qq.com"
	qqAccessTokenURL = "https://bots.qq.com/app/getAppAccessToken"
	qqPostJSON = func(context.Context, string, any, any) error {
		return errors.New("unexpected qqPostJSON call")
	}
	qqGetJSON = func(context.Context, string, http.Header, any) error {
		return errors.New("unexpected qqGetJSON call")
	}
	qqMarshalBody = json.Marshal
	t.Cleanup(func() {
		qqAPIBase = "https://api.sgroup.qq.com"
		qqAccessTokenURL = "https://bots.qq.com/app/getAppAccessToken"
		qqPostJSON = func(context.Context, string, any, any) error {
			return errors.New("unexpected qqPostJSON call")
		}
		qqGetJSON = func(context.Context, string, http.Header, any) error {
			return errors.New("unexpected qqGetJSON call")
		}
		qqMarshalBody = json.Marshal
	})
}

type qqSendRequest struct {
	path        string
	auth        string
	contentType string
	body        map[string]any
}

type qqBus struct{}

func (q *qqBus) PublishInbound(context.Context, Inbound) error {
	return nil
}
