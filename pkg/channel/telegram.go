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
)

type TelegramConfig struct {
	Enabled     bool
	InboundPath string
	OutboundURL string
	BotToken    string
	Secret      string
}

type Telegram struct {
	cfg        TelegramConfig
	bus        Bus
	httpClient *http.Client
	cancel     context.CancelFunc
	done       chan struct{}
	mu         sync.Mutex
	offset     int64
}

var (
	telegramAPIBase = "https://api.telegram.org/bot"
	pollAfter       = time.After
)

func NewTelegram(cfg TelegramConfig) *Telegram {
	return &Telegram{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 35 * time.Second},
	}
}

func (t *Telegram) ID() string { return "telegram" }

func (t *Telegram) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	_ = add
	t.bus = bus
	if !t.cfg.Enabled {
		return nil
	}
	if strings.TrimSpace(t.cfg.BotToken) == "" {
		return fmt.Errorf("telegram bot_token required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	t.done = make(chan struct{})
	go t.pollLoop(runCtx)
	return nil
}

func (t *Telegram) Stop(ctx context.Context) error {
	_ = ctx
	if t.cancel != nil {
		t.cancel()
	}
	if t.done != nil {
		<-t.done
	}
	return nil
}

func (t *Telegram) DeliverOutbound(ctx context.Context, o Outbound) error {
	chatID, replyTo := parseTelegramSession(o.SessionID)
	if chatID == "" {
		return fmt.Errorf("telegram session_id invalid")
	}
	payload := map[string]any{
		"chat_id": chatID,
		"text":    strings.TrimSpace(o.Text),
	}
	if replyTo > 0 {
		payload["reply_to_message_id"] = replyTo
	}
	return t.callTelegramAPI(ctx, "sendMessage", payload, nil)
}

func (t *Telegram) pollLoop(ctx context.Context) {
	defer close(t.done)
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := t.pollOnce(ctx); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-pollAfter(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

func (t *Telegram) pollOnce(ctx context.Context) error {
	t.mu.Lock()
	offset := t.offset
	t.mu.Unlock()
	req := map[string]any{
		"timeout": 25,
		"offset":  offset,
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result []struct {
			UpdateID int64 `json:"update_id"`
			Message  *struct {
				MessageID int64  `json:"message_id"`
				Text      string `json:"text"`
				Chat      struct {
					ID int64 `json:"id"`
				} `json:"chat"`
				From struct {
					ID int64 `json:"id"`
				} `json:"from"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := t.callTelegramAPI(ctx, "getUpdates", req, &resp); err != nil {
		return err
	}
	maxUpdateID := offset - 1
	for _, upd := range resp.Result {
		if upd.UpdateID > maxUpdateID {
			maxUpdateID = upd.UpdateID
		}
		if upd.Message == nil {
			continue
		}
		text := strings.TrimSpace(upd.Message.Text)
		if text == "" || t.bus == nil {
			continue
		}
		sessionID := strconv.FormatInt(upd.Message.Chat.ID, 10) + ":" + strconv.FormatInt(upd.Message.MessageID, 10)
		_ = t.bus.PublishInbound(ctx, Inbound{
			ChannelID: "telegram",
			SessionID: sessionID,
			Text:      text,
		})
	}
	if maxUpdateID >= 0 {
		t.mu.Lock()
		t.offset = maxUpdateID + 1
		t.mu.Unlock()
	}
	return nil
}

func (t *Telegram) callTelegramAPI(ctx context.Context, method string, payload any, out any) error {
	token := strings.TrimSpace(t.cfg.BotToken)
	url := telegramAPIBase + token + "/" + strings.TrimSpace(method)
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("telegram api status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func parseTelegramSession(sessionID string) (string, int64) {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return "", 0
	}
	parts := strings.SplitN(sid, ":", 2)
	chatID := strings.TrimSpace(parts[0])
	if len(parts) == 1 {
		return chatID, 0
	}
	replyTo, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	return chatID, replyTo
}
