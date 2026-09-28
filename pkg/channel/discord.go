package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type DiscordConfig struct {
	Enabled     bool
	InboundPath string
	OutboundURL string
	BotToken    string
	Secret      string
}

type Discord struct {
	cfg           DiscordConfig
	bus           Bus
	httpClient    *http.Client
	cancel        context.CancelFunc
	done          chan struct{}
	connMu        sync.RWMutex
	conn          *websocket.Conn
	heartbeatMu   sync.Mutex
	seq           *int64
	sessionID     string
	resumeGateway string
}

var (
	discordAPIBase     = "https://discord.com/api/v10"
	discordGatewayURL  = "wss://gateway.discord.gg/?v=10&encoding=json"
	discordLoopAfter   = time.After
	newHeartbeatTicker = time.NewTicker
	identifyConn       = (*Discord).identify
)

func NewDiscord(cfg DiscordConfig) *Discord {
	return &Discord{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (d *Discord) ID() string { return "discord" }

func (d *Discord) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	_ = add
	d.bus = bus
	if !d.cfg.Enabled {
		return nil
	}
	if strings.TrimSpace(d.cfg.BotToken) == "" {
		return fmt.Errorf("discord bot_token required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	d.done = make(chan struct{})
	go d.gatewayLoop(runCtx)
	return nil
}

func (d *Discord) Stop(ctx context.Context) error {
	_ = ctx
	if d.cancel != nil {
		d.cancel()
	}
	if conn := d.activeConn(); conn != nil {
		_ = conn.Close()
	}
	if d.done != nil {
		<-d.done
	}
	return nil
}

func (d *Discord) DeliverOutbound(ctx context.Context, o Outbound) error {
	channelID := strings.TrimSpace(parseDiscordSession(o.SessionID))
	if channelID == "" {
		return fmt.Errorf("discord session_id invalid")
	}
	url := discordAPIBase + "/channels/" + channelID + "/messages"
	payload := map[string]any{"content": strings.TrimSpace(o.Text)}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+strings.TrimSpace(d.cfg.BotToken))
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("discord send status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (d *Discord) gatewayLoop(ctx context.Context) {
	defer close(d.done)
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := d.connectAndRead(ctx); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-discordLoopAfter(backoff):
			}
			if backoff < 20*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

func (d *Discord) connectAndRead(ctx context.Context) error {
	wsURL := discordGatewayURL
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return err
	}
	d.setConn(conn)
	defer func() {
		_ = conn.Close()
		d.setConn(nil)
	}()

	var hello struct {
		Op int `json:"op"`
		D  struct {
			HeartbeatInterval float64 `json:"heartbeat_interval"`
		} `json:"d"`
	}
	if err := conn.ReadJSON(&hello); err != nil {
		return err
	}
	if hello.Op != 10 {
		return fmt.Errorf("discord hello opcode invalid")
	}
	interval := time.Duration(hello.D.HeartbeatInterval) * time.Millisecond
	if interval <= 0 {
		interval = 40 * time.Second
	}
	if err := identifyConn(d, conn); err != nil {
		return err
	}
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go d.heartbeatLoop(hbCtx, conn, interval)

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		var frame map[string]any
		if err := conn.ReadJSON(&frame); err != nil {
			return err
		}
		d.trackSequence(frame)
		op, _ := frame["op"].(float64)
		if int(op) == 0 {
			t, _ := frame["t"].(string)
			if t == "READY" {
				d.cacheReady(frame["d"])
			}
			if t == "MESSAGE_CREATE" {
				d.handleMessageCreate(ctx, frame["d"])
			}
		}
	}
}

func (d *Discord) identify(conn *websocket.Conn) error {
	payload := map[string]any{
		"op": 2,
		"d": map[string]any{
			"token":   strings.TrimSpace(d.cfg.BotToken),
			"intents": 513 | 32768,
			"properties": map[string]string{
				"os":      "linux",
				"browser": "forebrain",
				"device":  "forebrain",
			},
		},
	}
	return conn.WriteJSON(payload)
}

func (d *Discord) heartbeatLoop(ctx context.Context, conn *websocket.Conn, interval time.Duration) {
	ticker := newHeartbeatTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.heartbeatMu.Lock()
			var seq any
			if d.seq != nil {
				seq = *d.seq
			}
			_ = conn.WriteJSON(map[string]any{"op": 1, "d": seq})
			d.heartbeatMu.Unlock()
		}
	}
}

func (d *Discord) handleMessageCreate(ctx context.Context, raw any) {
	if d.bus == nil {
		return
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return
	}
	content := strings.TrimSpace(discordString(m["content"]))
	if content == "" {
		return
	}
	author, _ := m["author"].(map[string]any)
	if discordBool(author["bot"]) {
		return
	}
	channelID := strings.TrimSpace(discordString(m["channel_id"]))
	msgID := strings.TrimSpace(discordString(m["id"]))
	if channelID == "" {
		return
	}
	sessionID := channelID
	if msgID != "" {
		sessionID = channelID + ":" + msgID
	}
	_ = d.bus.PublishInbound(ctx, Inbound{
		ChannelID: "discord",
		SessionID: sessionID,
		Text:      content,
		Raw:       m,
	})
}

func (d *Discord) trackSequence(frame map[string]any) {
	if frame == nil {
		return
	}
	v, ok := frame["s"]
	if !ok || v == nil {
		return
	}
	f, ok := v.(float64)
	if !ok {
		return
	}
	i := int64(f)
	d.heartbeatMu.Lock()
	d.seq = &i
	d.heartbeatMu.Unlock()
}

func (d *Discord) cacheReady(raw any) {
	m, ok := raw.(map[string]any)
	if !ok {
		return
	}
	d.sessionID = strings.TrimSpace(discordString(m["session_id"]))
	d.resumeGateway = strings.TrimSpace(discordString(m["resume_gateway_url"]))
}

func (d *Discord) activeConn() *websocket.Conn {
	d.connMu.RLock()
	defer d.connMu.RUnlock()
	return d.conn
}

func (d *Discord) setConn(conn *websocket.Conn) {
	d.connMu.Lock()
	d.conn = conn
	d.connMu.Unlock()
}

func parseDiscordSession(sessionID string) string {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return ""
	}
	parts := strings.SplitN(sid, ":", 2)
	return strings.TrimSpace(parts[0])
}

func discordString(v any) string {
	switch vv := v.(type) {
	case string:
		return vv
	case float64:
		return strconv.FormatInt(int64(vv), 10)
	default:
		return ""
	}
}

func discordBool(v any) bool {
	b, ok := v.(bool)
	return ok && b
}
