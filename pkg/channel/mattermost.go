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

	"github.com/gorilla/websocket"
)

type MattermostConfig struct {
	Enabled     bool
	InboundPath string
	OutboundURL string
	Token       string
	Secret      string
}

type Mattermost struct {
	cfg        MattermostConfig
	bus        Bus
	httpClient *http.Client
	cancel     context.CancelFunc
	done       chan struct{}
	wsMu       sync.Mutex
	wsConn     *websocket.Conn
	botUserID  string
	seenMu     sync.Mutex
	seen       map[string]time.Time
}

var (
	mattermostAfter     = time.After
	mattermostWriteJSON = func(conn *websocket.Conn, v any) error { return conn.WriteJSON(v) }
)

func NewMattermost(cfg MattermostConfig) *Mattermost {
	return &Mattermost{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 20 * time.Second},
		seen:       map[string]time.Time{},
	}
}

func (m *Mattermost) ID() string { return "mattermost" }
func (m *Mattermost) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	_ = add
	m.bus = bus
	if !m.cfg.Enabled {
		return nil
	}
	base := normalizeMattermostBase(m.cfg.OutboundURL)
	if base == "" {
		return fmt.Errorf("mattermost outbound_url required")
	}
	m.cfg.OutboundURL = base
	botID, err := m.fetchBotUserID(ctx)
	if err != nil {
		return err
	}
	m.botUserID = botID
	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	// done is created with the loop it belongs to; see WhatsApp.Start.
	m.done = make(chan struct{})
	go m.loop(runCtx)
	return nil
}
func (m *Mattermost) Stop(ctx context.Context) error {
	_ = ctx
	if m.cancel != nil {
		m.cancel()
	}
	m.wsMu.Lock()
	if m.wsConn != nil {
		_ = m.wsConn.Close()
		m.wsConn = nil
	}
	m.wsMu.Unlock()
	// done exists only once Start launched the loop; see WhatsApp.Stop.
	if m.done == nil {
		return nil
	}
	select {
	case <-m.done:
	case <-mattermostAfter(3 * time.Second):
	}
	return nil
}
func (m *Mattermost) DeliverOutbound(ctx context.Context, o Outbound) error {
	channelID := strings.TrimSpace(o.SessionID)
	if strings.HasPrefix(channelID, "channel:") {
		channelID = strings.TrimSpace(strings.TrimPrefix(channelID, "channel:"))
	}
	if channelID == "" {
		return fmt.Errorf("mattermost session_id invalid")
	}
	payload := map[string]string{
		"channel_id": channelID,
		"message":    strings.TrimSpace(o.Text),
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.OutboundURL+"/api/v4/posts", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(m.cfg.Token))
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("mattermost send status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (m *Mattermost) fetchBotUserID(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.cfg.OutboundURL+"/api/v4/users/me", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(m.cfg.Token))
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("mattermost users/me status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var me struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		return "", err
	}
	if strings.TrimSpace(me.ID) == "" {
		return "", fmt.Errorf("mattermost bot user id empty")
	}
	return strings.TrimSpace(me.ID), nil
}

func (m *Mattermost) loop(ctx context.Context) {
	defer close(m.done)
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := m.connectAndConsume(ctx); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-mattermostAfter(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

func (m *Mattermost) connectAndConsume(ctx context.Context) error {
	u, err := url.Parse(m.cfg.OutboundURL)
	if err != nil {
		return err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/api/v4/websocket"
	header := http.Header{
		"Authorization": []string{"Bearer " + strings.TrimSpace(m.cfg.Token)},
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), header)
	if err != nil {
		return err
	}
	m.wsMu.Lock()
	m.wsConn = conn
	m.wsMu.Unlock()
	defer func() {
		m.wsMu.Lock()
		if m.wsConn != nil {
			_ = m.wsConn.Close()
		}
		m.wsConn = nil
		m.wsMu.Unlock()
	}()
	authMsg := map[string]any{
		"seq":    1,
		"action": "authentication_challenge",
		"data": map[string]string{
			"token": strings.TrimSpace(m.cfg.Token),
		},
	}
	if err := mattermostWriteJSON(conn, authMsg); err != nil {
		return err
	}
	for {
		_, p, err := conn.ReadMessage()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			return err
		}
		var evt struct {
			Event string `json:"event"`
			Data  struct {
				Post        string `json:"post"`
				ChannelType string `json:"channel_type"`
			} `json:"data"`
		}
		if err := json.Unmarshal(p, &evt); err != nil {
			continue
		}
		if evt.Event != "posted" || strings.TrimSpace(evt.Data.Post) == "" {
			continue
		}
		var post struct {
			ID        string `json:"id"`
			UserID    string `json:"user_id"`
			ChannelID string `json:"channel_id"`
			Message   string `json:"message"`
			Type      string `json:"type"`
		}
		if err := json.Unmarshal([]byte(evt.Data.Post), &post); err != nil {
			continue
		}
		if post.UserID == m.botUserID || strings.TrimSpace(post.ChannelID) == "" || strings.TrimSpace(post.Message) == "" || strings.TrimSpace(post.Type) != "" {
			continue
		}
		if m.isDuplicate(post.ID) {
			continue
		}
		_ = m.bus.PublishInbound(ctx, Inbound{
			ChannelID: "mattermost",
			SessionID: post.ChannelID,
			Text:      strings.TrimSpace(post.Message),
		})
	}
}

func (m *Mattermost) isDuplicate(id string) bool {
	key := strings.TrimSpace(id)
	if key == "" {
		return false
	}
	now := time.Now()
	cutoff := now.Add(-5 * time.Minute)
	m.seenMu.Lock()
	defer m.seenMu.Unlock()
	for k, t := range m.seen {
		if t.Before(cutoff) {
			delete(m.seen, k)
		}
	}
	if _, ok := m.seen[key]; ok {
		return true
	}
	m.seen[key] = now
	return false
}

func normalizeMattermostBase(v string) string {
	s := strings.TrimSpace(v)
	s = strings.TrimRight(s, "/")
	if strings.HasSuffix(s, "/api/v4") {
		s = strings.TrimSuffix(s, "/api/v4")
	}
	return s
}
