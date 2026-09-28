package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type MatrixConfig struct {
	Enabled     bool
	InboundPath string
	OutboundURL string
	Token       string
	Secret      string
}

type Matrix struct {
	cfg        MatrixConfig
	bus        Bus
	httpClient *http.Client
	cancel     context.CancelFunc
	done       chan struct{}
	mu         sync.Mutex
	nextBatch  string
}

var matrixLoopAfter = time.After

func NewMatrix(cfg MatrixConfig) *Matrix {
	return &Matrix{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 40 * time.Second},
	}
}

func (m *Matrix) ID() string { return "matrix" }

func (m *Matrix) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	_ = add
	m.bus = bus
	if !m.cfg.Enabled {
		return nil
	}
	if strings.TrimSpace(m.cfg.OutboundURL) == "" || strings.TrimSpace(m.cfg.Token) == "" {
		return fmt.Errorf("matrix outbound_url/token required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.done = make(chan struct{})
	go m.syncLoop(runCtx)
	return nil
}

func (m *Matrix) Stop(ctx context.Context) error {
	_ = ctx
	if m.cancel != nil {
		m.cancel()
	}
	if m.done != nil {
		<-m.done
	}
	return nil
}

func (m *Matrix) DeliverOutbound(ctx context.Context, o Outbound) error {
	roomID := parseMatrixSession(o.SessionID)
	if roomID == "" {
		return fmt.Errorf("matrix session_id invalid")
	}
	txnID := strconv.FormatInt(time.Now().UnixNano(), 10)
	base := strings.TrimRight(strings.TrimSpace(m.cfg.OutboundURL), "/")
	reqURL := base + "/_matrix/client/v3/rooms/" + url.PathEscape(roomID) + "/send/m.room.message/" + txnID
	payload := map[string]any{
		"msgtype": "m.text",
		"body":    strings.TrimSpace(o.Text),
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(m.cfg.Token))
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("matrix send status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (m *Matrix) syncLoop(ctx context.Context) {
	defer close(m.done)
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := m.syncOnce(ctx); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-matrixLoopAfter(backoff):
			}
			if backoff < 20*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

func (m *Matrix) syncOnce(ctx context.Context) error {
	base := strings.TrimRight(strings.TrimSpace(m.cfg.OutboundURL), "/")
	reqURL := base + "/_matrix/client/v3/sync?timeout=30000"
	m.mu.Lock()
	nb := strings.TrimSpace(m.nextBatch)
	m.mu.Unlock()
	if nb != "" {
		reqURL += "&since=" + url.QueryEscape(nb)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(m.cfg.Token))
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("matrix sync status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		NextBatch string `json:"next_batch"`
		Rooms     struct {
			Join map[string]struct {
				Timeline struct {
					Events []map[string]any `json:"events"`
				} `json:"timeline"`
			} `json:"join"`
		} `json:"rooms"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if strings.TrimSpace(out.NextBatch) != "" {
		m.mu.Lock()
		m.nextBatch = strings.TrimSpace(out.NextBatch)
		m.mu.Unlock()
	}
	if m.bus == nil {
		return nil
	}
	for roomID, room := range out.Rooms.Join {
		for _, ev := range room.Timeline.Events {
			if strings.TrimSpace(matrixString(ev["type"])) != "m.room.message" {
				continue
			}
			content, _ := ev["content"].(map[string]any)
			text := strings.TrimSpace(matrixString(content["body"]))
			if text == "" {
				continue
			}
			_ = m.bus.PublishInbound(ctx, Inbound{
				ChannelID: "matrix",
				SessionID: roomID,
				Text:      text,
				Raw:       ev,
			})
		}
	}
	return nil
}

func parseMatrixSession(sessionID string) string {
	return strings.TrimSpace(sessionID)
}

func matrixString(v any) string {
	switch vv := v.(type) {
	case string:
		return vv
	case float64:
		return strconv.FormatFloat(vv, 'f', -1, 64)
	default:
		return ""
	}
}
