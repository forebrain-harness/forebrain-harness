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

type WhatsAppConfig struct {
	Enabled     bool
	InboundPath string
	OutboundURL string
	Token       string
	Secret      string
}

type WhatsApp struct {
	cfg        WhatsAppConfig
	bus        Bus
	httpClient *http.Client
	cancel     context.CancelFunc
	done       chan struct{}
	seenMu     sync.Mutex
	seen       map[string]time.Time
	inboundAdd RouteAdder
}

var (
	whatsappAfter     = time.After
	whatsappNewTicker = time.NewTicker
)

func NewWhatsApp(cfg WhatsAppConfig) *WhatsApp {
	return &WhatsApp{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		seen:       map[string]time.Time{},
	}
}

func (w *WhatsApp) ID() string { return "whatsapp" }
func (w *WhatsApp) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	w.inboundAdd = add
	w.bus = bus
	if !w.cfg.Enabled {
		return nil
	}
	base := strings.TrimRight(strings.TrimSpace(w.cfg.OutboundURL), "/")
	if base == "" {
		base = "http://127.0.0.1:3000"
	}
	w.cfg.OutboundURL = base
	path := strings.TrimSpace(w.cfg.InboundPath)
	if path != "" {
		add(http.MethodPost, path, w.handleInbound)
	}
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	// done is created here, with the loop it belongs to, so a non-nil done
	// means there is a loop to wait for in Stop.
	w.done = make(chan struct{})
	go w.pollLoop(runCtx)
	return nil
}
func (w *WhatsApp) Stop(ctx context.Context) error {
	_ = ctx
	if w.cancel != nil {
		w.cancel()
	}
	// done exists only once Start launched the poll loop. A channel that was
	// never started (disabled in this agent's configuration) has nothing to
	// wait for, and waiting on a nil channel would burn the whole timeout on
	// every rebind.
	if w.done == nil {
		return nil
	}
	select {
	case <-w.done:
	case <-whatsappAfter(3 * time.Second):
	}
	return nil
}
func (w *WhatsApp) DeliverOutbound(ctx context.Context, o Outbound) error {
	chatID := parseWhatsAppSession(o.SessionID)
	if chatID == "" {
		return fmt.Errorf("whatsapp session_id invalid")
	}
	payload := map[string]string{
		"chatId":  chatID,
		"message": strings.TrimSpace(o.Text),
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.OutboundURL+"/send", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(w.cfg.Token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := w.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("whatsapp send status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (w *WhatsApp) pollLoop(ctx context.Context) {
	defer close(w.done)
	tk := whatsappNewTicker(time.Second)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.cfg.OutboundURL+"/messages", nil)
		if err != nil {
			continue
		}
		if token := strings.TrimSpace(w.cfg.Token); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := w.httpClient.Do(req)
		if err != nil {
			continue
		}
		var items []map[string]any
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			_ = json.NewDecoder(resp.Body).Decode(&items)
		}
		resp.Body.Close()
		for _, item := range items {
			sessionID, text, msgID := parseWhatsAppInbound(item)
			if sessionID == "" || text == "" || w.isDuplicate(msgID) {
				continue
			}
			_ = w.bus.PublishInbound(ctx, Inbound{
				ChannelID: "whatsapp",
				SessionID: sessionID,
				Text:      text,
				Raw:       item,
			})
		}
	}
}

func (w *WhatsApp) handleInbound(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
		http.Error(rw, "invalid json", http.StatusBadRequest)
		return
	}
	sessionID, text, msgID := parseWhatsAppInbound(payload)
	if sessionID == "" || text == "" || w.isDuplicate(msgID) {
		rw.WriteHeader(http.StatusNoContent)
		return
	}
	_ = w.bus.PublishInbound(r.Context(), Inbound{
		ChannelID: "whatsapp",
		SessionID: sessionID,
		Text:      text,
		Raw:       payload,
	})
	rw.WriteHeader(http.StatusNoContent)
}

func parseWhatsAppInbound(m map[string]any) (string, string, string) {
	chatID := strv(m, "chatId", "chat_id", "from", "sessionId", "session_id")
	text := strv(m, "body", "text", "message", "content")
	msgID := strv(m, "messageId", "message_id", "id")
	return parseWhatsAppSession(chatID), strings.TrimSpace(text), strings.TrimSpace(msgID)
}

func parseWhatsAppSession(s string) string {
	v := strings.TrimSpace(s)
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, "chat:") {
		return strings.TrimSpace(strings.TrimPrefix(v, "chat:"))
	}
	return v
}

func strv(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func (w *WhatsApp) isDuplicate(id string) bool {
	key := strings.TrimSpace(id)
	if key == "" {
		return false
	}
	now := time.Now()
	cutoff := now.Add(-5 * time.Minute)
	w.seenMu.Lock()
	defer w.seenMu.Unlock()
	for k, t := range w.seen {
		if t.Before(cutoff) {
			delete(w.seen, k)
		}
	}
	if _, ok := w.seen[key]; ok {
		return true
	}
	w.seen[key] = now
	return false
}
