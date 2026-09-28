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

	"github.com/gorilla/websocket"
)

type HomeAssistantConfig struct {
	Enabled     bool
	InboundPath string
	OutboundURL string
	Token       string
	Secret      string
}

type HomeAssistant struct {
	cfg        HomeAssistantConfig
	bus        Bus
	httpClient *http.Client
	cancel     context.CancelFunc
	done       chan struct{}
	wsMu       sync.Mutex
	wsConn     *websocket.Conn
}

type wsID struct {
	mu sync.Mutex
	v  int
}

var (
	haAfter     = time.After
	haWriteJSON = func(conn *websocket.Conn, v any) error { return conn.WriteJSON(v) }
)

func (w *wsID) Next() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.v++
	return w.v
}

func NewHomeAssistant(cfg HomeAssistantConfig) *HomeAssistant {
	return &HomeAssistant{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (h *HomeAssistant) ID() string { return "homeassistant" }
func (h *HomeAssistant) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	_ = add
	h.bus = bus
	if !h.cfg.Enabled {
		return nil
	}
	base := normalizeHAURL(h.cfg.OutboundURL)
	if base == "" {
		return fmt.Errorf("homeassistant outbound_url required")
	}
	h.cfg.OutboundURL = base
	runCtx, cancel := context.WithCancel(ctx)
	h.cancel = cancel
	// done is created with the loop it belongs to; see WhatsApp.Start.
	h.done = make(chan struct{})
	go h.loop(runCtx)
	return nil
}
func (h *HomeAssistant) Stop(ctx context.Context) error {
	_ = ctx
	if h.cancel != nil {
		h.cancel()
	}
	h.wsMu.Lock()
	if h.wsConn != nil {
		_ = h.wsConn.Close()
		h.wsConn = nil
	}
	h.wsMu.Unlock()
	// done exists only once Start launched the loop; see WhatsApp.Stop.
	if h.done == nil {
		return nil
	}
	select {
	case <-h.done:
	case <-haAfter(3 * time.Second):
	}
	return nil
}
func (h *HomeAssistant) DeliverOutbound(ctx context.Context, o Outbound) error {
	payload := map[string]string{
		"title":   "Forebrain Harness",
		"message": truncateHAText(strings.TrimSpace(o.Text), 4096),
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.cfg.OutboundURL+"/api/services/persistent_notification/create", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(h.cfg.Token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("homeassistant send status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (h *HomeAssistant) loop(ctx context.Context) {
	defer close(h.done)
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := h.connectAndConsume(ctx); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-haAfter(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

func (h *HomeAssistant) connectAndConsume(ctx context.Context) error {
	wsURL := strings.TrimPrefix(h.cfg.OutboundURL, "http://")
	wsURL = strings.TrimPrefix(wsURL, "https://")
	if strings.HasPrefix(h.cfg.OutboundURL, "https://") {
		wsURL = "wss://" + wsURL
	} else {
		wsURL = "ws://" + wsURL
	}
	wsURL = strings.TrimRight(wsURL, "/") + "/api/websocket"
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return err
	}
	h.wsMu.Lock()
	h.wsConn = conn
	h.wsMu.Unlock()
	defer func() {
		h.wsMu.Lock()
		if h.wsConn != nil {
			_ = h.wsConn.Close()
		}
		h.wsConn = nil
		h.wsMu.Unlock()
	}()
	msgID := &wsID{}
	var authReq struct {
		Type string `json:"type"`
	}
	if err := conn.ReadJSON(&authReq); err != nil {
		return err
	}
	if authReq.Type != "auth_required" {
		return fmt.Errorf("homeassistant expected auth_required got %s", authReq.Type)
	}
	if err := haWriteJSON(conn, map[string]string{
		"type":         "auth",
		"access_token": strings.TrimSpace(h.cfg.Token),
	}); err != nil {
		return err
	}
	var authResp struct {
		Type string `json:"type"`
	}
	if err := conn.ReadJSON(&authResp); err != nil {
		return err
	}
	if authResp.Type != "auth_ok" {
		return fmt.Errorf("homeassistant auth failed type=%s", authResp.Type)
	}
	subID := msgID.Next()
	if err := haWriteJSON(conn, map[string]any{
		"id":         subID,
		"type":       "subscribe_events",
		"event_type": "state_changed",
	}); err != nil {
		return err
	}
	for {
		var evt struct {
			Type  string `json:"type"`
			Event struct {
				Data struct {
					EntityID string `json:"entity_id"`
					NewState struct {
						State      string `json:"state"`
						Attributes struct {
							FriendlyName string `json:"friendly_name"`
						} `json:"attributes"`
					} `json:"new_state"`
					OldState struct {
						State string `json:"state"`
					} `json:"old_state"`
				} `json:"data"`
			} `json:"event"`
		}
		if err := conn.ReadJSON(&evt); err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			return err
		}
		if evt.Type != "event" {
			continue
		}
		entityID := strings.TrimSpace(evt.Event.Data.EntityID)
		if entityID == "" {
			continue
		}
		oldState := strings.TrimSpace(evt.Event.Data.OldState.State)
		newState := strings.TrimSpace(evt.Event.Data.NewState.State)
		if oldState == newState {
			continue
		}
		name := strings.TrimSpace(evt.Event.Data.NewState.Attributes.FriendlyName)
		if name == "" {
			name = entityID
		}
		text := fmt.Sprintf("[Home Assistant] %s (%s): changed from '%s' to '%s'", name, entityID, oldState, newState)
		_ = h.bus.PublishInbound(ctx, Inbound{
			ChannelID: "homeassistant",
			SessionID: "ha_events",
			Text:      text,
		})
	}
}

func normalizeHAURL(v string) string {
	s := strings.TrimSpace(v)
	s = strings.TrimRight(s, "/")
	return s
}

func truncateHAText(v string, max int) string {
	if len(v) <= max {
		return v
	}
	return v[:max]
}
