// The generic HTTP inbound/outbound bridge, its JSON helpers, and the webhook channel.
package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type HTTPConfig struct {
	ChannelID      string
	Enabled        bool
	InboundPath    string
	OutboundURL    string
	Secret         string
	Token          string
	SessionField   string
	AltSessionKeys []string
	TextField      string
	AltTextKeys    []string
}

type HTTPBridge struct {
	cfg        HTTPConfig
	bus        Bus
	httpClient *http.Client
}

func NewHTTPBridge(cfg HTTPConfig) *HTTPBridge {
	return &HTTPBridge{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 12 * time.Second},
	}
}

func (b *HTTPBridge) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	_ = ctx
	b.bus = bus
	if !b.cfg.Enabled {
		return nil
	}
	path := strings.TrimSpace(b.cfg.InboundPath)
	if path == "" {
		path = "/channels/" + b.cfg.ChannelID + "/inbound"
	}
	add(http.MethodPost, path, b.handleInbound)
	add(http.MethodGet, "/channels/"+b.cfg.ChannelID+"/health", func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
	})
	return nil
}

func (b *HTTPBridge) Stop(ctx context.Context) error {
	_ = ctx
	return nil
}

func (b *HTTPBridge) DeliverOutbound(ctx context.Context, o Outbound) error {
	if strings.TrimSpace(b.cfg.OutboundURL) == "" {
		return fmt.Errorf("%s outbound_url is empty", b.cfg.ChannelID)
	}
	payload := map[string]any{
		"channel_id": b.cfg.ChannelID,
		"session_id": strings.TrimSpace(o.SessionID),
		"text":       strings.TrimSpace(o.Text),
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.OutboundURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(b.cfg.Token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(b.cfg.Token))
	}
	if strings.TrimSpace(b.cfg.Secret) != "" {
		req.Header.Set("X-Forebrain-Secret", strings.TrimSpace(b.cfg.Secret))
	}
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("outbound status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (b *HTTPBridge) handleInbound(rw http.ResponseWriter, r *http.Request) {
	if !b.cfg.Enabled {
		http.Error(rw, "disabled", http.StatusNotFound)
		return
	}
	if strings.TrimSpace(b.cfg.Secret) != "" {
		secret := strings.TrimSpace(r.Header.Get("X-Forebrain-Secret"))
		if secret == "" {
			secret = strings.TrimSpace(r.URL.Query().Get("secret"))
		}
		if secret != strings.TrimSpace(b.cfg.Secret) {
			http.Error(rw, "forbidden", http.StatusForbidden)
			return
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(rw, "bad body", http.StatusBadRequest)
		return
	}
	sessionID, text := b.parseInbound(body)
	if text != "" && b.bus != nil {
		_ = b.bus.PublishInbound(r.Context(), Inbound{
			ChannelID: b.cfg.ChannelID,
			SessionID: sessionID,
			Text:      text,
			Raw:       string(body),
		})
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write([]byte(`{"ok":true}`))
}

func (b *HTTPBridge) parseInbound(body []byte) (string, string) {
	raw := strings.TrimSpace(string(body))
	if raw == "" {
		return b.cfg.ChannelID + "-default", ""
	}
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		return b.cfg.ChannelID + "-default", raw
	}
	sessionKey := strings.TrimSpace(b.cfg.SessionField)
	textKey := strings.TrimSpace(b.cfg.TextField)
	sessionID := getString(obj, sessionKey, b.cfg.AltSessionKeys...)
	text := getString(obj, textKey, b.cfg.AltTextKeys...)
	if strings.TrimSpace(sessionID) == "" {
		sessionID = b.cfg.ChannelID + "-default"
	}
	if strings.TrimSpace(text) == "" {
		text = raw
	}
	return strings.TrimSpace(sessionID), strings.TrimSpace(text)
}

func getString(m map[string]any, key string, extra ...string) string {
	keys := make([]string, 0, len(extra)+1)
	if strings.TrimSpace(key) != "" {
		keys = append(keys, strings.TrimSpace(key))
	}
	keys = append(keys, extra...)
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch vv := v.(type) {
		case string:
			if strings.TrimSpace(vv) != "" {
				return vv
			}
		case float64:
			return fmt.Sprintf("%.0f", vv)
		default:
			b, err := json.Marshal(vv)
			if err == nil {
				s := strings.Trim(string(b), `"`)
				if strings.TrimSpace(s) != "" {
					return s
				}
			}
		}
	}
	return ""
}

func PostJSON(ctx context.Context, url string, body any, out any) error {
	return PostJSONWithHeaders(ctx, url, body, nil, out)
}

func PostJSONWithHeaders(ctx context.Context, url string, body any, headers http.Header, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, vals := range headers {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	cli := &http.Client{Timeout: 12 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func GetJSON(ctx context.Context, url string, headers http.Header, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for k, vals := range headers {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	cli := &http.Client{Timeout: 12 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type WebhookConfig struct {
	Enabled     bool
	InboundPath string
	OutboundURL string
	Token       string
	Secret      string
}

type Webhook struct {
	bridge *HTTPBridge
}

func NewWebhook(cfg WebhookConfig) *Webhook {
	return &Webhook{
		bridge: NewHTTPBridge(HTTPConfig{
			ChannelID:      "webhook",
			Enabled:        cfg.Enabled,
			InboundPath:    cfg.InboundPath,
			OutboundURL:    cfg.OutboundURL,
			Token:          cfg.Token,
			Secret:         cfg.Secret,
			SessionField:   "session_id",
			AltSessionKeys: []string{"sessionId", "chat_id", "chatId", "source"},
			TextField:      "text",
			AltTextKeys:    []string{"message", "content", "body"},
		}),
	}
}

func (w *Webhook) ID() string { return "webhook" }
func (w *Webhook) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	return w.bridge.Start(ctx, add, bus)
}
func (w *Webhook) Stop(ctx context.Context) error { return w.bridge.Stop(ctx) }
func (w *Webhook) DeliverOutbound(ctx context.Context, o Outbound) error {
	return w.bridge.DeliverOutbound(ctx, o)
}
