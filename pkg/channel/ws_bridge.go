package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type WSConfig struct {
	ChannelID     string
	Enabled       bool
	WSURL         string
	Header        http.Header
	Resolver      func(ctx context.Context) (WSHandshake, error)
	SessionPrefix string
}

type WSHandshake struct {
	WSURL  string
	Header http.Header
}

type WSBridge struct {
	cfg    WSConfig
	bus    Bus
	cancel context.CancelFunc
	done   chan struct{}
	conn   *websocket.Conn
	connMu sync.RWMutex
	sendMu sync.Mutex
}

var loopAfter = time.After

func NewWSBridge(cfg WSConfig) *WSBridge {
	return &WSBridge{cfg: cfg}
}

func (b *WSBridge) Start(ctx context.Context, bus Bus) error {
	if !b.cfg.Enabled {
		return nil
	}
	if strings.TrimSpace(b.cfg.WSURL) == "" && b.cfg.Resolver == nil {
		return fmt.Errorf("%s ws_url is empty", b.cfg.ChannelID)
	}
	b.bus = bus
	runCtx, cancel := context.WithCancel(ctx)
	b.cancel = cancel
	b.done = make(chan struct{})
	go b.loop(runCtx)
	return nil
}

func (b *WSBridge) Stop() {
	if b.cancel != nil {
		b.cancel()
	}
	if conn := b.activeConn(); conn != nil {
		_ = conn.Close()
	}
	if b.done != nil {
		<-b.done
	}
}

func (b *WSBridge) DeliverOutbound(ctx context.Context, o Outbound) error {
	conn := b.activeConn()
	if conn == nil {
		return fmt.Errorf("%s websocket not connected", b.cfg.ChannelID)
	}
	payload := map[string]any{
		"cmd":        "send",
		"channel_id": b.cfg.ChannelID,
		"session_id": strings.TrimSpace(o.SessionID),
		"text":       strings.TrimSpace(o.Text),
	}
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	return conn.WriteJSON(payload)
}

func (b *WSBridge) loop(ctx context.Context) {
	defer close(b.done)
	backoff := 2 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := b.connectAndConsume(ctx); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-loopAfter(backoff):
			}
			if backoff < 20*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 2 * time.Second
	}
}

func (b *WSBridge) connectAndConsume(ctx context.Context) error {
	hs := WSHandshake{
		WSURL:  strings.TrimSpace(b.cfg.WSURL),
		Header: cloneHeader(b.cfg.Header),
	}
	if b.cfg.Resolver != nil {
		resolved, err := b.cfg.Resolver(ctx)
		if err != nil {
			return err
		}
		if strings.TrimSpace(resolved.WSURL) != "" {
			hs.WSURL = strings.TrimSpace(resolved.WSURL)
		}
		if resolved.Header != nil {
			hs.Header = cloneHeader(resolved.Header)
		}
	}
	if strings.TrimSpace(hs.WSURL) == "" {
		return fmt.Errorf("%s ws_url is empty", b.cfg.ChannelID)
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, hs.WSURL, hs.Header)
	if err != nil {
		return err
	}
	b.setConn(conn)
	defer func() {
		_ = conn.Close()
		b.setConn(nil)
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		text, sid := decodeInbound(payload)
		if strings.TrimSpace(text) == "" {
			continue
		}
		if strings.TrimSpace(sid) == "" {
			sid = b.cfg.SessionPrefix + "-default"
		}
		if b.bus != nil {
			_ = b.bus.PublishInbound(ctx, Inbound{
				ChannelID: b.cfg.ChannelID,
				Text:      text,
				SessionID: sid,
				Raw:       string(payload),
			})
		}
	}
}

func (b *WSBridge) activeConn() *websocket.Conn {
	b.connMu.RLock()
	defer b.connMu.RUnlock()
	return b.conn
}

func (b *WSBridge) setConn(conn *websocket.Conn) {
	b.connMu.Lock()
	b.conn = conn
	b.connMu.Unlock()
}

func cloneHeader(in http.Header) http.Header {
	if in == nil {
		return http.Header{}
	}
	out := http.Header{}
	for k, v := range in {
		cp := make([]string, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

func decodeInbound(payload []byte) (string, string) {
	txt := strings.TrimSpace(string(payload))
	if txt == "" {
		return "", ""
	}
	var obj map[string]any
	if json.Unmarshal(payload, &obj) != nil {
		return txt, ""
	}
	session := stringValue(obj, "session_id", "sessionId", "chat_id", "conversation_id", "sender_id", "from")
	msg := stringValue(obj, "text", "content", "message", "body")
	if strings.TrimSpace(msg) == "" {
		msg = txt
	}
	return strings.TrimSpace(msg), strings.TrimSpace(session)
}

func stringValue(m map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
