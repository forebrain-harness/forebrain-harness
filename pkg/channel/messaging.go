// Direct-messaging channels: email, SMS, and Signal.
package channel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type EmailConfig struct {
	Enabled     bool
	InboundPath string
	OutboundURL string
	Token       string
	Secret      string
}

type Email struct {
	cfg EmailConfig
	bus Bus
}

var sendMail = smtp.SendMail

func NewEmail(cfg EmailConfig) *Email {
	return &Email{cfg: cfg}
}

func (e *Email) ID() string { return "email" }
func (e *Email) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	_ = ctx
	e.bus = bus
	if !e.cfg.Enabled {
		return nil
	}
	path := strings.TrimSpace(e.cfg.InboundPath)
	if path != "" {
		add(http.MethodPost, path, e.handleInbound)
	}
	return nil
}
func (e *Email) Stop(ctx context.Context) error { return nil }
func (e *Email) DeliverOutbound(ctx context.Context, o Outbound) error {
	toAddr := strings.TrimSpace(o.SessionID)
	if strings.HasPrefix(toAddr, "mailto:") {
		toAddr = strings.TrimSpace(strings.TrimPrefix(toAddr, "mailto:"))
	}
	if toAddr == "" {
		return fmt.Errorf("email session_id invalid")
	}
	host, port, user, pass, fromAddr, err := parseSMTPConfig(e.cfg)
	if err != nil {
		return err
	}
	auth := smtp.PlainAuth("", user, pass, host)
	subject := "Forebrain Harness Reply"
	body := strings.TrimSpace(o.Text)
	msg := "From: " + fromAddr + "\r\n" +
		"To: " + toAddr + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n" +
		body + "\r\n"
	return sendMail(host+":"+port, auth, fromAddr, []string{toAddr}, []byte(msg))
}

func (e *Email) handleInbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	from := strAny(body, "from", "sender", "email", "session_id")
	subj := strAny(body, "subject")
	content := strAny(body, "text", "body", "content")
	text := strings.TrimSpace(strings.TrimSpace(subj) + "\n" + strings.TrimSpace(content))
	text = strings.TrimSpace(text)
	if strings.TrimSpace(from) == "" || text == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	_ = e.bus.PublishInbound(r.Context(), Inbound{
		ChannelID: "email",
		SessionID: from,
		Text:      text,
		Raw:       body,
	})
	w.WriteHeader(http.StatusNoContent)
}

func parseSMTPConfig(cfg EmailConfig) (host, port, user, pass, from string, err error) {
	addr := strings.TrimSpace(cfg.OutboundURL)
	if !strings.Contains(addr, "://") {
		addr = "smtp://" + addr
	}
	u, err := mail.ParseAddress(strings.TrimSpace(cfg.Token))
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("email token must be a valid from address")
	}
	from = u.Address
	parsed, _ := parseURL(addr)
	host = parsed.Host
	port = parsed.Port
	if host == "" || port == "" {
		return "", "", "", "", "", fmt.Errorf("email outbound_url must be smtp://host:port")
	}
	user = choose(parsed.User, from)
	pass = strings.TrimSpace(cfg.Secret)
	if pass == "" {
		return "", "", "", "", "", fmt.Errorf("email secret must be smtp password")
	}
	return host, port, user, pass, from, nil
}

type smtpURL struct {
	Host string
	Port string
	User string
}

func parseURL(raw string) (smtpURL, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "smtp://")
	if s == "" {
		return smtpURL{}, fmt.Errorf("email outbound_url empty")
	}
	user := ""
	hostPort := s
	if strings.Contains(s, "@") {
		parts := strings.SplitN(s, "@", 2)
		user = strings.TrimSpace(parts[0])
		hostPort = parts[1]
	}
	host := ""
	port := ""
	if strings.Contains(hostPort, ":") {
		p := strings.SplitN(hostPort, ":", 2)
		host = strings.TrimSpace(p[0])
		port = strings.TrimSpace(p[1])
	}
	return smtpURL{Host: host, Port: port, User: user}, nil
}

func choose(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return strings.TrimSpace(b)
}

func strAny(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

type SMSConfig struct {
	Enabled     bool
	InboundPath string
	OutboundURL string
	Token       string
	Secret      string
}

type SMS struct {
	cfg        SMSConfig
	bus        Bus
	httpClient *http.Client
}

func NewSMS(cfg SMSConfig) *SMS {
	return &SMS{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 20 * time.Second},
	}
}

func (s *SMS) ID() string { return "sms" }
func (s *SMS) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	_ = ctx
	s.bus = bus
	if !s.cfg.Enabled {
		return nil
	}
	path := strings.TrimSpace(s.cfg.InboundPath)
	if path != "" {
		add(http.MethodPost, path, s.handleInbound)
	}
	return nil
}
func (s *SMS) Stop(ctx context.Context) error { return nil }
func (s *SMS) DeliverOutbound(ctx context.Context, o Outbound) error {
	to := strings.TrimSpace(o.SessionID)
	if strings.HasPrefix(to, "sms:") {
		to = strings.TrimSpace(strings.TrimPrefix(to, "sms:"))
	}
	if to == "" {
		return fmt.Errorf("sms session_id invalid")
	}
	base, accountSID := resolveTwilioBase(s.cfg.OutboundURL)
	if strings.TrimSpace(accountSID) == "" {
		return fmt.Errorf("sms outbound_url must include twilio account sid")
	}
	from := strings.TrimSpace(s.cfg.Secret)
	if from == "" {
		return fmt.Errorf("sms secret must be twilio from number")
	}
	authToken := strings.TrimSpace(s.cfg.Token)
	if authToken == "" {
		return fmt.Errorf("sms token must be twilio auth token")
	}
	form := url.Values{}
	form.Set("From", from)
	form.Set("To", to)
	form.Set("Body", strings.TrimSpace(o.Text))
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/Messages.json", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+basicAuth(accountSID, authToken))
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("sms send status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (s *SMS) handleInbound(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(rw, "invalid form", http.StatusBadRequest)
		return
	}
	from := strings.TrimSpace(r.FormValue("From"))
	body := strings.TrimSpace(r.FormValue("Body"))
	if from == "" || body == "" {
		rw.WriteHeader(http.StatusNoContent)
		return
	}
	_ = s.bus.PublishInbound(r.Context(), Inbound{
		ChannelID: "sms",
		SessionID: from,
		Text:      body,
		Raw:       r.Form,
	})
	rw.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_, _ = rw.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Response></Response>`))
}

func resolveTwilioBase(v string) (string, string) {
	raw := strings.TrimSpace(v)
	if raw == "" {
		return "", ""
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		sid := strings.Trim(raw, "/")
		if sid == "" {
			return "", ""
		}
		return "https://api.twilio.com/2010-04-01/Accounts/" + sid, sid
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ""
	}
	base := strings.TrimRight(u.String(), "/")
	parts := strings.Split(strings.Trim(base, "/"), "/")
	sid := parts[len(parts)-1]
	return base, sid
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

type SignalConfig struct {
	Enabled     bool
	InboundPath string
	OutboundURL string
	Token       string
	Secret      string
}

type Signal struct {
	cfg        SignalConfig
	bus        Bus
	httpClient *http.Client
	cancel     context.CancelFunc
	done       chan struct{}
}

var signalAfter = time.After

func NewSignal(cfg SignalConfig) *Signal {
	return &Signal{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 65 * time.Second},
	}
}

func (s *Signal) ID() string { return "signal" }

func (s *Signal) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	_ = add
	s.bus = bus
	if !s.cfg.Enabled {
		return nil
	}
	if strings.TrimSpace(s.cfg.OutboundURL) == "" || strings.TrimSpace(s.cfg.Token) == "" {
		return fmt.Errorf("signal outbound_url/token(account) required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.done = make(chan struct{})
	go s.eventLoop(runCtx)
	return nil
}

func (s *Signal) Stop(ctx context.Context) error {
	_ = ctx
	if s.cancel != nil {
		s.cancel()
	}
	if s.done != nil {
		<-s.done
	}
	return nil
}

func (s *Signal) DeliverOutbound(ctx context.Context, o Outbound) error {
	recipient := parseSignalSession(o.SessionID)
	if recipient == "" {
		return fmt.Errorf("signal session_id invalid")
	}
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      strconv.FormatInt(time.Now().UnixNano(), 10),
		"method":  "send",
		"params": map[string]any{
			"account":   strings.TrimSpace(s.cfg.Token),
			"recipient": []string{recipient},
			"message":   strings.TrimSpace(o.Text),
		},
	}
	raw, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(strings.TrimSpace(s.cfg.OutboundURL), "/")+"/api/v1/rpc", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("signal send status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out map[string]any
	if json.NewDecoder(resp.Body).Decode(&out) == nil {
		if e := out["error"]; e != nil {
			return fmt.Errorf("signal send error: %v", e)
		}
	}
	return nil
}

func (s *Signal) eventLoop(ctx context.Context) {
	defer close(s.done)
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := s.consumeSSE(ctx); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-signalAfter(backoff):
			}
			if backoff < 20*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

func (s *Signal) consumeSSE(ctx context.Context) error {
	base := strings.TrimRight(strings.TrimSpace(s.cfg.OutboundURL), "/")
	account := url.QueryEscape(strings.TrimSpace(s.cfg.Token))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/events?account="+account, nil)
	if err != nil {
		return err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("signal events status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		s.handleEvent(ctx, payload)
	}
	return sc.Err()
}

func (s *Signal) handleEvent(ctx context.Context, payload string) {
	if strings.TrimSpace(payload) == "" || s.bus == nil {
		return
	}
	var obj map[string]any
	if json.Unmarshal([]byte(payload), &obj) != nil {
		return
	}
	envelope, _ := obj["envelope"].(map[string]any)
	dataMessage, _ := envelope["dataMessage"].(map[string]any)
	text := strings.TrimSpace(asString(dataMessage["message"]))
	source := strings.TrimSpace(asString(envelope["sourceNumber"]))
	if source == "" {
		source = strings.TrimSpace(asString(envelope["source"]))
	}
	if source == "" || text == "" {
		return
	}
	_ = s.bus.PublishInbound(ctx, Inbound{
		ChannelID: "signal",
		SessionID: source,
		Text:      text,
		Raw:       obj,
	})
}

func parseSignalSession(sessionID string) string {
	return strings.TrimSpace(strings.SplitN(strings.TrimSpace(sessionID), ":", 2)[0])
}

func asString(v any) string {
	switch vv := v.(type) {
	case string:
		return vv
	case float64:
		return strconv.FormatFloat(vv, 'f', -1, 64)
	default:
		return ""
	}
}
