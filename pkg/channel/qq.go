package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type QQConfig struct {
	Enabled      bool
	AppID        string
	ClientSecret string
}

type QQ struct {
	cfg          QQConfig
	bridge       *WSBridge
	tokenMu      sync.Mutex
	token        string
	tokenExpires time.Time
	httpClient   *http.Client
}

var (
	qqAPIBase        = "https://api.sgroup.qq.com"
	qqAccessTokenURL = "https://bots.qq.com/app/getAppAccessToken"
	qqPostJSON       = PostJSON
	qqGetJSON        = GetJSON
	qqMarshalBody    = json.Marshal
)

func NewQQ(cfg QQConfig) *QQ {
	return &QQ{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (q *QQ) ID() string { return "qq" }

func (q *QQ) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	_ = add
	q.bridge = NewWSBridge(WSConfig{
		ChannelID:     "qq",
		Enabled:       q.cfg.Enabled,
		Resolver:      q.resolveHandshake,
		SessionPrefix: "qq",
	})
	return q.bridge.Start(ctx, bus)
}

func (q *QQ) Stop(ctx context.Context) error {
	_ = ctx
	if q.bridge != nil {
		q.bridge.Stop()
	}
	return nil
}

func (q *QQ) DeliverOutbound(ctx context.Context, o Outbound) error {
	target, isGroup := parseQQSession(o.SessionID)
	if target == "" {
		return fmt.Errorf("qq session_id invalid")
	}
	token, err := q.getAccessToken(ctx)
	if err != nil {
		return err
	}
	path := "/v2/users/" + target + "/messages"
	if isGroup {
		path = "/v2/groups/" + target + "/messages"
	}
	body := map[string]any{
		"msg_type": 0,
		"content":  strings.TrimSpace(o.Text),
	}
	raw, err := qqMarshalBody(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, qqAPIBase+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "QQBot "+strings.TrimSpace(q.cfg.AppID)+"."+token)
	resp, err := q.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("qq send status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (q *QQ) resolveHandshake(ctx context.Context) (WSHandshake, error) {
	appID := strings.TrimSpace(q.cfg.AppID)
	token, err := q.getAccessToken(ctx)
	if err != nil {
		return WSHandshake{}, err
	}
	if appID == "" {
		return WSHandshake{}, fmt.Errorf("qq app_id/client_secret required")
	}
	var gatewayResp struct {
		URL string `json:"url"`
	}
	if err := qqGetJSON(ctx, qqAPIBase+"/gateway/bot", http.Header{
		"Authorization": []string{"QQBot " + appID + "." + token},
	}, &gatewayResp); err != nil {
		return WSHandshake{}, err
	}
	if strings.TrimSpace(gatewayResp.URL) == "" {
		return WSHandshake{}, fmt.Errorf("qq websocket url is empty")
	}
	return WSHandshake{
		WSURL: gatewayResp.URL,
		Header: http.Header{
			"Authorization": []string{"QQBot " + appID + "." + token},
		},
	}, nil
}

func (q *QQ) getAccessToken(ctx context.Context) (string, error) {
	q.tokenMu.Lock()
	if strings.TrimSpace(q.token) != "" && time.Until(q.tokenExpires) > time.Minute {
		token := q.token
		q.tokenMu.Unlock()
		return token, nil
	}
	q.tokenMu.Unlock()
	appID := strings.TrimSpace(q.cfg.AppID)
	clientSecret := strings.TrimSpace(q.cfg.ClientSecret)
	if appID == "" || clientSecret == "" {
		return "", fmt.Errorf("qq app_id/client_secret required")
	}
	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := qqPostJSON(ctx, qqAccessTokenURL, map[string]string{
		"appId":        appID,
		"clientSecret": clientSecret,
	}, &tokenResp); err != nil {
		return "", err
	}
	token := strings.TrimSpace(tokenResp.AccessToken)
	if token == "" {
		return "", fmt.Errorf("qq access token is empty")
	}
	expire := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	q.tokenMu.Lock()
	q.token = token
	q.tokenExpires = expire
	q.tokenMu.Unlock()
	return token, nil
}

func parseQQSession(sessionID string) (string, bool) {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return "", false
	}
	if strings.HasPrefix(sid, "group:") {
		return strings.TrimSpace(strings.TrimPrefix(sid, "group:")), true
	}
	if strings.HasPrefix(sid, "user:") {
		return strings.TrimSpace(strings.TrimPrefix(sid, "user:")), false
	}
	return sid, false
}
