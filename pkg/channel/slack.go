package channel

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type SlackConfig struct {
	Enabled     bool
	InboundPath string
	OutboundURL string
	BotToken    string
	Secret      string
}

type Slack struct {
	cfg        SlackConfig
	bus        Bus
	httpClient *http.Client
}

var (
	slackPostMessageURL = "https://slack.com/api/chat.postMessage"
	slackNow            = time.Now
)

func NewSlack(cfg SlackConfig) *Slack {
	return &Slack{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 12 * time.Second},
	}
}

func (s *Slack) ID() string { return "slack" }

func (s *Slack) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	_ = ctx
	s.bus = bus
	if !s.cfg.Enabled {
		return nil
	}
	path := strings.TrimSpace(s.cfg.InboundPath)
	if path == "" {
		path = "/channels/slack/events"
	}
	add(http.MethodPost, path, s.handleEvents)
	add(http.MethodGet, "/channels/slack/health", func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
	})
	return nil
}

func (s *Slack) Stop(ctx context.Context) error {
	_ = ctx
	return nil
}

func (s *Slack) DeliverOutbound(ctx context.Context, o Outbound) error {
	channelID, threadTS := parseSlackSession(o.SessionID)
	if channelID == "" {
		return fmt.Errorf("slack session_id invalid")
	}
	form := url.Values{}
	form.Set("channel", channelID)
	form.Set("text", strings.TrimSpace(o.Text))
	if threadTS != "" {
		form.Set("thread_ts", threadTS)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, slackPostMessageURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(s.cfg.BotToken))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("slack send failed: %s", strings.TrimSpace(out.Error))
	}
	return nil
}

func (s *Slack) handleEvents(rw http.ResponseWriter, r *http.Request) {
	if !s.cfg.Enabled {
		http.Error(rw, "disabled", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(rw, "bad body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(s.cfg.Secret) != "" && !verifySlackSignature(r, body, s.cfg.Secret) {
		http.Error(rw, "forbidden", http.StatusForbidden)
		return
	}
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil {
		http.Error(rw, "bad payload", http.StatusBadRequest)
		return
	}
	if typ := strings.TrimSpace(slackString(envelope["type"])); typ == "url_verification" {
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write(body)
		return
	}
	event, _ := envelope["event"].(map[string]any)
	if event != nil && strings.TrimSpace(slackString(event["type"])) == "message" && !slackBool(event["bot_id"] != nil) {
		channelID := strings.TrimSpace(slackString(event["channel"]))
		text := strings.TrimSpace(slackString(event["text"]))
		threadTS := strings.TrimSpace(slackString(event["thread_ts"]))
		if threadTS == "" {
			threadTS = strings.TrimSpace(slackString(event["ts"]))
		}
		if channelID != "" && text != "" && s.bus != nil {
			sessionID := channelID
			if threadTS != "" {
				sessionID = channelID + ":" + threadTS
			}
			_ = s.bus.PublishInbound(r.Context(), Inbound{
				ChannelID: "slack",
				SessionID: sessionID,
				Text:      text,
				Raw:       envelope,
			})
		}
	}
	rw.Header().Set("Content-Type", "application/json")
	_, _ = rw.Write([]byte(`{"ok":true}`))
}

func verifySlackSignature(r *http.Request, body []byte, secret string) bool {
	ts := strings.TrimSpace(r.Header.Get("X-Slack-Request-Timestamp"))
	sig := strings.TrimSpace(r.Header.Get("X-Slack-Signature"))
	if ts == "" || sig == "" {
		return false
	}
	tUnix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	if slackNow().Sub(time.Unix(tUnix, 0)) > 5*time.Minute {
		return false
	}
	base := "v0:" + ts + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	_, _ = mac.Write([]byte(base))
	expect := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expect), []byte(sig))
}

func parseSlackSession(sessionID string) (string, string) {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return "", ""
	}
	parts := strings.SplitN(sid, ":", 2)
	ch := strings.TrimSpace(parts[0])
	if len(parts) == 1 {
		return ch, ""
	}
	return ch, strings.TrimSpace(parts[1])
}

func slackString(v any) string {
	if v == nil {
		return ""
	}
	switch vv := v.(type) {
	case string:
		return vv
	case float64:
		return strconv.FormatFloat(vv, 'f', -1, 64)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(bytes.Trim(b, `"`)))
	}
}

func slackBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}
