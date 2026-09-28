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

type BlueBubblesConfig struct {
	Enabled     bool
	InboundPath string
	OutboundURL string
	Token       string
	Secret      string
}

type BlueBubbles struct {
	cfg        BlueBubblesConfig
	bus        Bus
	httpClient *http.Client
	seenMu     sync.Mutex
	seen       map[string]time.Time
}

func NewBlueBubbles(cfg BlueBubblesConfig) *BlueBubbles {
	return &BlueBubbles{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 20 * time.Second},
		seen:       map[string]time.Time{},
	}
}

func (b *BlueBubbles) ID() string { return "bluebubbles" }
func (b *BlueBubbles) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	_ = ctx
	b.bus = bus
	if !b.cfg.Enabled {
		return nil
	}
	path := strings.TrimSpace(b.cfg.InboundPath)
	if path != "" {
		add(http.MethodPost, path, b.handleInbound)
	}
	return nil
}
func (b *BlueBubbles) Stop(ctx context.Context) error { return nil }
func (b *BlueBubbles) DeliverOutbound(ctx context.Context, o Outbound) error {
	chatGuid := parseBlueBubblesSession(o.SessionID)
	if chatGuid == "" {
		return fmt.Errorf("bluebubbles session_id invalid")
	}
	base := strings.TrimRight(strings.TrimSpace(b.cfg.OutboundURL), "/")
	if base == "" {
		return fmt.Errorf("bluebubbles outbound_url required")
	}
	password := strings.TrimSpace(b.authToken())
	if password == "" {
		return fmt.Errorf("bluebubbles token required")
	}
	payload := map[string]string{
		"chatGuid": chatGuid,
		"tempGuid": fmt.Sprintf("temp-%d", time.Now().UnixNano()),
		"message":  strings.TrimSpace(o.Text),
	}
	raw, _ := json.Marshal(payload)
	reqURL := base + "/api/v1/message/text?password=" + url.QueryEscape(password)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("bluebubbles send status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (b *BlueBubbles) handleInbound(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !b.authorized(r) {
		http.Error(rw, "unauthorized", http.StatusUnauthorized)
		return
	}
	defer r.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
		http.Error(rw, "invalid json", http.StatusBadRequest)
		return
	}
	item := unwrapBlueBubblesData(payload)
	sessionID := blueString(item, "chatGuid", "chat_guid", "chatIdentifier", "chat_identifier", "handle")
	text := blueString(item, "text", "message", "body")
	msgID := blueString(item, "guid", "messageGuid", "message_guid", "id")
	if sessionID == "" || text == "" || b.isDuplicate(msgID) {
		rw.WriteHeader(http.StatusNoContent)
		return
	}
	_ = b.bus.PublishInbound(r.Context(), Inbound{
		ChannelID: "bluebubbles",
		SessionID: sessionID,
		Text:      strings.TrimSpace(text),
		Raw:       payload,
	})
	rw.WriteHeader(http.StatusNoContent)
}

func (b *BlueBubbles) authToken() string {
	if strings.TrimSpace(b.cfg.Token) != "" {
		return strings.TrimSpace(b.cfg.Token)
	}
	return strings.TrimSpace(b.cfg.Secret)
}

func (b *BlueBubbles) authorized(r *http.Request) bool {
	expect := b.authToken()
	if expect == "" {
		return true
	}
	got := strings.TrimSpace(r.URL.Query().Get("password"))
	if got == "" {
		got = strings.TrimSpace(r.Header.Get("x-password"))
	}
	if got == "" {
		got = strings.TrimSpace(r.Header.Get("x-guid"))
	}
	if got == "" {
		got = strings.TrimSpace(r.Header.Get("x-bluebubbles-guid"))
	}
	return got == expect
}

func unwrapBlueBubblesData(payload map[string]any) map[string]any {
	if v, ok := payload["data"]; ok {
		if m, ok := v.(map[string]any); ok {
			return m
		}
	}
	return payload
}

func parseBlueBubblesSession(s string) string {
	v := strings.TrimSpace(s)
	if strings.HasPrefix(v, "chat:") {
		v = strings.TrimSpace(strings.TrimPrefix(v, "chat:"))
	}
	return v
}

func blueString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func (b *BlueBubbles) isDuplicate(id string) bool {
	key := strings.TrimSpace(id)
	if key == "" {
		return false
	}
	now := time.Now()
	cutoff := now.Add(-5 * time.Minute)
	b.seenMu.Lock()
	defer b.seenMu.Unlock()
	for k, t := range b.seen {
		if t.Before(cutoff) {
			delete(b.seen, k)
		}
	}
	if _, ok := b.seen[key]; ok {
		return true
	}
	b.seen[key] = now
	return false
}
