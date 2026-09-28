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

type FeishuConfig struct {
	Enabled        bool
	AppID          string
	AppSecret      string
	Domain         string
	ConnectionMode string
}

type Feishu struct {
	cfg          FeishuConfig
	bridge       *WSBridge
	tokenMu      sync.Mutex
	token        string
	tokenExpires time.Time
	httpClient   *http.Client
}

var (
	feishuOpenAPIBase        = "https://open.feishu.cn/open-apis"
	feishuLarkOpenAPIBase    = "https://open.larksuite.com/open-apis"
	feishuWSURL              = "wss://open.feishu.cn/open-apis/ws/v1"
	feishuLarkWSURL          = "wss://open.larksuite.com/open-apis/ws/v1"
	feishuPostJSON           = PostJSON
	feishuMarshalTextContent = json.Marshal
	feishuMarshalSendBody    = json.Marshal
)

func NewFeishu(cfg FeishuConfig) *Feishu {
	return &Feishu{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (f *Feishu) ID() string { return "feishu" }

func (f *Feishu) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	_ = add
	if strings.TrimSpace(f.cfg.ConnectionMode) == "" {
		f.cfg.ConnectionMode = "websocket"
	}
	if !strings.EqualFold(strings.TrimSpace(f.cfg.ConnectionMode), "websocket") {
		return nil
	}
	wsURL := feishuWSURL
	if strings.EqualFold(strings.TrimSpace(f.cfg.Domain), "lark") {
		wsURL = feishuLarkWSURL
	}
	f.bridge = NewWSBridge(WSConfig{
		ChannelID:     "feishu",
		Enabled:       f.cfg.Enabled,
		WSURL:         wsURL,
		Resolver:      f.resolveHandshake,
		SessionPrefix: "feishu",
	})
	return f.bridge.Start(ctx, bus)
}

func (f *Feishu) Stop(ctx context.Context) error {
	_ = ctx
	if f.bridge != nil {
		f.bridge.Stop()
	}
	return nil
}

func (f *Feishu) DeliverOutbound(ctx context.Context, o Outbound) error {
	chatID := parseFeishuSession(o.SessionID)
	if chatID == "" {
		return fmt.Errorf("feishu session_id invalid")
	}
	token, err := f.getTenantToken(ctx)
	if err != nil {
		return err
	}
	content, err := feishuMarshalTextContent(map[string]string{"text": strings.TrimSpace(o.Text)})
	if err != nil {
		return err
	}
	base := feishuOpenAPIBase
	if strings.EqualFold(strings.TrimSpace(f.cfg.Domain), "lark") {
		base = feishuLarkOpenAPIBase
	}
	reqURL := base + "/im/v1/messages?receive_id_type=chat_id"
	body := map[string]string{
		"receive_id": chatID,
		"msg_type":   "text",
		"content":    string(content),
	}
	raw, err := feishuMarshalSendBody(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("feishu send status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var sendResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sendResp); err == nil {
		if sendResp.Code != 0 {
			return fmt.Errorf("feishu send failed code=%d msg=%s", sendResp.Code, strings.TrimSpace(sendResp.Msg))
		}
	}
	return nil
}

func (f *Feishu) resolveHandshake(ctx context.Context) (WSHandshake, error) {
	appID := strings.TrimSpace(f.cfg.AppID)
	appSecret := strings.TrimSpace(f.cfg.AppSecret)
	if appID == "" || appSecret == "" {
		return WSHandshake{}, fmt.Errorf("feishu app_id/app_secret required")
	}
	base := feishuOpenAPIBase
	if strings.EqualFold(strings.TrimSpace(f.cfg.Domain), "lark") {
		base = feishuLarkOpenAPIBase
	}
	var tokenResp struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
	}
	if err := feishuPostJSON(ctx, base+"/auth/v3/tenant_access_token/internal", map[string]string{
		"app_id":     appID,
		"app_secret": appSecret,
	}, &tokenResp); err != nil {
		return WSHandshake{}, err
	}
	if tokenResp.Code != 0 || strings.TrimSpace(tokenResp.TenantAccessToken) == "" {
		return WSHandshake{}, fmt.Errorf("feishu token request failed code=%d msg=%s", tokenResp.Code, strings.TrimSpace(tokenResp.Msg))
	}
	wsURL := feishuWSURL
	if strings.EqualFold(strings.TrimSpace(f.cfg.Domain), "lark") {
		wsURL = feishuLarkWSURL
	}
	return WSHandshake{
		WSURL: wsURL,
		Header: http.Header{
			"Authorization": []string{"Bearer " + strings.TrimSpace(tokenResp.TenantAccessToken)},
		},
	}, nil
}

func (f *Feishu) getTenantToken(ctx context.Context) (string, error) {
	f.tokenMu.Lock()
	if strings.TrimSpace(f.token) != "" && time.Until(f.tokenExpires) > time.Minute {
		token := f.token
		f.tokenMu.Unlock()
		return token, nil
	}
	f.tokenMu.Unlock()
	appID := strings.TrimSpace(f.cfg.AppID)
	appSecret := strings.TrimSpace(f.cfg.AppSecret)
	if appID == "" || appSecret == "" {
		return "", fmt.Errorf("feishu app_id/app_secret required")
	}
	base := feishuOpenAPIBase
	if strings.EqualFold(strings.TrimSpace(f.cfg.Domain), "lark") {
		base = feishuLarkOpenAPIBase
	}
	var tokenResp struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int64  `json:"expire"`
	}
	if err := feishuPostJSON(ctx, base+"/auth/v3/tenant_access_token/internal", map[string]string{
		"app_id":     appID,
		"app_secret": appSecret,
	}, &tokenResp); err != nil {
		return "", err
	}
	if tokenResp.Code != 0 || strings.TrimSpace(tokenResp.TenantAccessToken) == "" {
		return "", fmt.Errorf("feishu token request failed code=%d msg=%s", tokenResp.Code, strings.TrimSpace(tokenResp.Msg))
	}
	expire := time.Now().Add(time.Duration(tokenResp.Expire) * time.Second)
	f.tokenMu.Lock()
	f.token = strings.TrimSpace(tokenResp.TenantAccessToken)
	f.tokenExpires = expire
	f.tokenMu.Unlock()
	return strings.TrimSpace(tokenResp.TenantAccessToken), nil
}

func parseFeishuSession(sessionID string) string {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return ""
	}
	parts := strings.SplitN(sid, ":", 2)
	if len(parts) == 2 && strings.TrimSpace(parts[0]) == "chat" {
		return strings.TrimSpace(parts[1])
	}
	return strings.TrimSpace(parts[0])
}
