package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type DingTalkConfig struct {
	Enabled      bool
	ClientID     string
	ClientSecret string
}

type Dingtalk struct {
	cfg          DingTalkConfig
	bridge       *WSBridge
	tokenMu      sync.Mutex
	token        string
	tokenExpires time.Time
	httpClient   *http.Client
}

var (
	dingtalkAccessTokenURL       = "https://api.dingtalk.com/v1.0/oauth2/accessToken"
	dingtalkOpenConnectionURL    = "https://api.dingtalk.com/v1.0/gateway/connections/open"
	dingtalkBatchSendURL         = "https://api.dingtalk.com/v1.0/robot/oToMessages/batchSend"
	dingtalkGroupSendURL         = "https://api.dingtalk.com/v1.0/robot/groupMessages/send"
	dingtalkPostJSON             = PostJSON
	dingtalkPostJSONWithHeaders  = PostJSONWithHeaders
	dingtalkMarshalTextPayload   = json.Marshal
	dingtalkMarshalObjectPayload = json.Marshal
)

func NewDingTalk(cfg DingTalkConfig) *Dingtalk {
	return &Dingtalk{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (d *Dingtalk) ID() string { return "dingtalk" }

func (d *Dingtalk) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	_ = add
	d.bridge = NewWSBridge(WSConfig{
		ChannelID:     "dingtalk",
		Enabled:       d.cfg.Enabled,
		Resolver:      d.resolveHandshake,
		SessionPrefix: "dingtalk",
	})
	return d.bridge.Start(ctx, bus)
}

func (d *Dingtalk) Stop(ctx context.Context) error {
	_ = ctx
	if d.bridge != nil {
		d.bridge.Stop()
	}
	return nil
}

func (d *Dingtalk) DeliverOutbound(ctx context.Context, o Outbound) error {
	targetID := strings.TrimSpace(o.SessionID)
	if targetID == "" {
		return fmt.Errorf("dingtalk session_id required")
	}
	token, err := d.getAccessToken(ctx)
	if err != nil {
		return err
	}
	isGroup := strings.HasPrefix(strings.ToLower(targetID), "cid")
	payload := map[string]any{
		"robotCode": d.cfg.ClientID,
		"msgKey":    "sampleText",
		"msgParam":  `{"content":"` + escapeJSONString(strings.TrimSpace(o.Text)) + `"}`,
	}
	if isGroup {
		payload["openConversationId"] = targetID
	} else {
		payload["userIds"] = []string{targetID}
	}
	raw, err := dingtalkMarshalObjectPayload(payload)
	if err != nil {
		return err
	}
	endpoint := dingtalkBatchSendURL
	if isGroup {
		endpoint = dingtalkGroupSendURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("dingtalk send status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (d *Dingtalk) resolveHandshake(ctx context.Context) (WSHandshake, error) {
	clientID := strings.TrimSpace(d.cfg.ClientID)
	clientSecret := strings.TrimSpace(d.cfg.ClientSecret)
	if clientID == "" || clientSecret == "" {
		return WSHandshake{}, fmt.Errorf("dingtalk client_id/client_secret required")
	}
	token, err := d.getAccessToken(ctx)
	if err != nil {
		return WSHandshake{}, err
	}
	var connResp struct {
		Endpoint string `json:"endpoint"`
		Ticket   string `json:"ticket"`
	}
	if err := dingtalkPostJSONWithHeaders(ctx, dingtalkOpenConnectionURL, map[string]any{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"subscriptions": []map[string]string{
			{"type": "EVENT", "topic": "*"},
		},
		"ua": "forebrain",
	}, http.Header{
		"Authorization": []string{"Bearer " + token},
	}, &connResp); err != nil {
		return WSHandshake{}, err
	}
	endpoint := strings.TrimSpace(connResp.Endpoint)
	if endpoint == "" {
		return WSHandshake{}, fmt.Errorf("dingtalk endpoint is empty")
	}
	if strings.TrimSpace(connResp.Ticket) != "" {
		u, err := url.Parse(endpoint)
		if err == nil {
			q := u.Query()
			q.Set("ticket", strings.TrimSpace(connResp.Ticket))
			u.RawQuery = q.Encode()
			endpoint = u.String()
		}
	}
	return WSHandshake{
		WSURL: endpoint,
		Header: http.Header{
			"Authorization": []string{"Bearer " + token},
		},
	}, nil
}

func (d *Dingtalk) getAccessToken(ctx context.Context) (string, error) {
	d.tokenMu.Lock()
	if strings.TrimSpace(d.token) != "" && time.Until(d.tokenExpires) > time.Minute {
		token := d.token
		d.tokenMu.Unlock()
		return token, nil
	}
	d.tokenMu.Unlock()
	clientID := strings.TrimSpace(d.cfg.ClientID)
	clientSecret := strings.TrimSpace(d.cfg.ClientSecret)
	if clientID == "" || clientSecret == "" {
		return "", fmt.Errorf("dingtalk client_id/client_secret required")
	}
	var accessResp struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int64  `json:"expireIn"`
	}
	if err := dingtalkPostJSON(ctx, dingtalkAccessTokenURL, map[string]string{
		"appKey":    clientID,
		"appSecret": clientSecret,
	}, &accessResp); err != nil {
		return "", err
	}
	token := strings.TrimSpace(accessResp.AccessToken)
	if token == "" {
		return "", fmt.Errorf("dingtalk access token is empty")
	}
	expire := time.Now().Add(time.Duration(accessResp.ExpireIn) * time.Second)
	d.tokenMu.Lock()
	d.token = token
	d.tokenExpires = expire
	d.tokenMu.Unlock()
	return token, nil
}

func escapeJSONString(text string) string {
	raw, err := dingtalkMarshalTextPayload(text)
	if err == nil && len(raw) >= 2 {
		return string(raw[1 : len(raw)-1])
	}
	return text
}
