// The weixin (WeChat) channel: transport, capabilities, session and context stores.
package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	ilink "github.com/openilink/openilink-sdk-go"
)

var (
	weixinStopAfter  = time.After
	weixinSilkDecode func(data []byte, sampleRate int) ([]byte, error)
)

type WeixinConfig struct {
	Enabled         bool
	BaseURL         string
	CDNBaseURL      string
	Token           string
	AccountID       string
	BotType         string
	ChannelVersion  string
	RouteTag        string
	SilkVoiceDecode bool
	// StateRoot is the workspace root of the primary agent that owns this
	// channel. The iLink login credentials and per-user context tokens are
	// that agent's data, so they are written under its workspace rather than
	// a shared location every agent can read.
	StateRoot string
}

type Weixin struct {
	cfg            WeixinConfig
	api            *ilink.Client
	bus            Bus
	cancel         context.CancelFunc
	done           chan struct{}
	bufMu          sync.Mutex
	getUpdatesBuf  string
	contextTokenMu sync.RWMutex
	contextTokens  map[string]string
	disk           *contextDisk
}

func NewWeixin(cfg WeixinConfig) *Weixin {
	return &Weixin{
		cfg:           cfg,
		done:          make(chan struct{}),
		contextTokens: map[string]string{},
	}
}

func (w *Weixin) ID() string { return "weixin" }

func buildILinkOptions(cfg WeixinConfig) []ilink.Option {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = ilink.DefaultBaseURL
	}
	opts := []ilink.Option{ilink.WithBaseURL(strings.TrimRight(base, "/"))}
	if u := strings.TrimSpace(cfg.CDNBaseURL); u != "" {
		opts = append(opts, ilink.WithCDNBaseURL(strings.TrimRight(u, "/")))
	}
	if bt := strings.TrimSpace(cfg.BotType); bt != "" {
		opts = append(opts, ilink.WithBotType(bt))
	}
	if v := strings.TrimSpace(cfg.ChannelVersion); v != "" {
		opts = append(opts, ilink.WithVersion(v))
	}
	if rt := strings.TrimSpace(cfg.RouteTag); rt != "" {
		opts = append(opts, ilink.WithRouteTag(rt))
	}
	if cfg.SilkVoiceDecode {
		opts = append(opts, ilink.WithSILKDecoder(func(data []byte, sampleRate int) ([]byte, error) {
			return weixinSilkDecode(data, sampleRate)
		}))
	}
	return opts
}

func (w *Weixin) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	_ = add
	w.bus = bus
	if !w.cfg.Enabled {
		return nil
	}
	root := strings.TrimSpace(w.cfg.StateRoot)
	if root == "" {
		return fmt.Errorf("weixin: StateRoot required")
	}
	w.disk = &contextDisk{root: root}
	users, syncBuf := loadWeixinSyncState(root)
	for k, v := range users {
		w.contextTokens[k] = v
	}
	w.bufMu.Lock()
	w.getUpdatesBuf = syncBuf
	w.bufMu.Unlock()
	baseURL := strings.TrimSpace(w.cfg.BaseURL)
	if baseURL == "" {
		baseURL = ilink.DefaultBaseURL
	}
	w.cfg.BaseURL = strings.TrimRight(baseURL, "/")
	token := strings.TrimSpace(w.cfg.Token)
	if token == "" {
		return fmt.Errorf("weixin: enabled but missing token; configure it during first-run setup or set weixin.token / WEIXIN_TOKEN")
	}
	w.api = ilink.NewClient(token, buildILinkOptions(w.cfg)...)
	for k, v := range users {
		if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
			w.api.SetContextToken(k, v)
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.runMonitor(runCtx)
	return nil
}

func (w *Weixin) Stop(ctx context.Context) error {
	_ = ctx
	if w.cancel != nil {
		w.cancel()
		select {
		case <-w.done:
		case <-weixinStopAfter(3 * time.Second):
		}
	}
	return nil
}

func (w *Weixin) DeliverOutbound(ctx context.Context, o Outbound) error {
	if w.api == nil {
		return fmt.Errorf("weixin client not started")
	}
	toUser := parseWeixinSession(o.SessionID)
	if toUser == "" {
		return fmt.Errorf("weixin session_id invalid")
	}
	text := strings.TrimSpace(o.Text)
	if text == "" {
		return fmt.Errorf("weixin: empty outbound text")
	}
	_, err := w.api.Push(ctx, toUser, text)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ilink.ErrNoContextToken) {
		return err
	}
	ctxTok := w.getContextToken(toUser)
	if ctxTok == "" {
		return fmt.Errorf("weixin: missing context_token for user %s", toUser)
	}
	_, err = w.api.SendText(ctx, toUser, text, ctxTok)
	return err
}

func (w *Weixin) runMonitor(ctx context.Context) {
	defer close(w.done)
	initial := strings.TrimSpace(w.currentBuf())
	_ = w.api.Monitor(ctx, w.dispatchInbound, &ilink.MonitorOptions{
		InitialBuf: initial,
		OnBufUpdate: func(buf string) {
			w.updateBuf(strings.TrimSpace(buf))
			w.persistSyncFromState()
		},
	})
}

func (w *Weixin) persistSyncFromState() {
	if w.disk == nil {
		return
	}
	w.contextTokenMu.RLock()
	cp := make(map[string]string, len(w.contextTokens))
	for k, v := range w.contextTokens {
		cp[k] = v
	}
	w.contextTokenMu.RUnlock()
	w.disk.persistFull(cp, w.currentBuf())
}

func (w *Weixin) dispatchInbound(msg ilink.WeixinMessage) {
	pubCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	from := strings.TrimSpace(msg.FromUserID)
	if from == "" {
		return
	}
	if token := strings.TrimSpace(msg.ContextToken); token != "" {
		w.storeContextToken(from, token)
	}
	text := ilink.ExtractText(&msg)
	if strings.TrimSpace(text) == "" {
		text = w.syntheticMediaSummary(&msg)
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	m := msg
	_ = w.bus.PublishInbound(pubCtx, Inbound{
		ChannelID: "weixin",
		SessionID: "user:" + from,
		Text:      strings.TrimSpace(text),
		Raw:       &m,
	})
}

func (w *Weixin) syntheticMediaSummary(msg *ilink.WeixinMessage) string {
	if msg == nil {
		return ""
	}
	var parts []string
	for i := range msg.ItemList {
		switch msg.ItemList[i].Type {
		case ilink.ItemImage:
			parts = append(parts, "[weixin:image]")
		case ilink.ItemVoice:
			parts = append(parts, "[weixin:voice]")
		case ilink.ItemFile:
			parts = append(parts, "[weixin:file]")
		case ilink.ItemVideo:
			parts = append(parts, "[weixin:video]")
		}
	}
	return strings.Join(parts, " ")
}

func (w *Weixin) currentBuf() string {
	w.bufMu.Lock()
	defer w.bufMu.Unlock()
	return w.getUpdatesBuf
}

func (w *Weixin) updateBuf(v string) {
	w.bufMu.Lock()
	w.getUpdatesBuf = v
	w.bufMu.Unlock()
}

func (w *Weixin) storeContextToken(toUser, token string) {
	w.contextTokenMu.Lock()
	w.contextTokens[toUser] = token
	cp := make(map[string]string, len(w.contextTokens))
	for k, v := range w.contextTokens {
		cp[k] = v
	}
	w.contextTokenMu.Unlock()
	if w.api != nil {
		w.api.SetContextToken(toUser, token)
	}
	if w.disk != nil {
		w.disk.persistFull(cp, w.currentBuf())
	}
}

func (w *Weixin) getContextToken(toUser string) string {
	toUser = strings.TrimSpace(toUser)
	if toUser == "" {
		return ""
	}
	w.contextTokenMu.RLock()
	v := strings.TrimSpace(w.contextTokens[toUser])
	w.contextTokenMu.RUnlock()
	if v != "" {
		return v
	}
	if w.api != nil {
		if t, ok := w.api.GetContextToken(toUser); ok {
			return strings.TrimSpace(t)
		}
	}
	return ""
}

func parseWeixinSession(sessionID string) string {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return ""
	}
	if strings.HasPrefix(sid, "user:") {
		return strings.TrimSpace(strings.TrimPrefix(sid, "user:"))
	}
	return sid
}

func (w *Weixin) ILClient() *ilink.Client {
	return w.api
}

func (w *Weixin) parseToUser(sessionID string) (string, error) {
	if w.api == nil {
		return "", fmt.Errorf("weixin client not started")
	}
	u := parseWeixinSession(sessionID)
	if u == "" {
		return "", fmt.Errorf("weixin session_id invalid")
	}
	return u, nil
}

func (w *Weixin) sessionContext(sessionID string) (toUser, ctxTok string, err error) {
	if w.api == nil {
		return "", "", fmt.Errorf("weixin client not started")
	}
	toUser = parseWeixinSession(sessionID)
	if toUser == "" {
		return "", "", fmt.Errorf("weixin session_id invalid")
	}
	ctxTok = strings.TrimSpace(w.getContextToken(toUser))
	if ctxTok == "" {
		return "", "", fmt.Errorf("weixin: missing context_token for user %s", toUser)
	}
	return toUser, ctxTok, nil
}

func (w *Weixin) GetConfig(ctx context.Context, sessionID string) (*ilink.GetConfigResp, error) {
	u, ct, err := w.sessionContext(sessionID)
	if err != nil {
		return nil, err
	}
	cfg, err := w.api.GetConfig(ctx, u, ct)
	if err != nil {
		return nil, err
	}
	if cfg != nil && cfg.Ret != 0 {
		return cfg, fmt.Errorf("ilink getconfig: ret=%d errmsg=%s", cfg.Ret, cfg.ErrMsg)
	}
	return cfg, nil
}

func (w *Weixin) SendTyping(ctx context.Context, sessionID string, active bool) error {
	u, ct, err := w.sessionContext(sessionID)
	if err != nil {
		return err
	}
	cfg, err := w.api.GetConfig(ctx, u, ct)
	if err != nil {
		return err
	}
	if cfg.Ret != 0 {
		return fmt.Errorf("ilink getconfig: ret=%d errmsg=%s", cfg.Ret, cfg.ErrMsg)
	}
	ticket := strings.TrimSpace(cfg.TypingTicket)
	if ticket == "" {
		return fmt.Errorf("weixin: empty typing_ticket from getconfig")
	}
	st := ilink.CancelTyping
	if active {
		st = ilink.Typing
	}
	return w.api.SendTyping(ctx, u, ticket, st)
}

func (w *Weixin) WithTyping(ctx context.Context, sessionID string, fn func() error) error {
	_ = w.SendTyping(ctx, sessionID, true)
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = w.SendTyping(stopCtx, sessionID, false)
	}()
	return fn()
}

func (w *Weixin) SendMediaFile(ctx context.Context, sessionID string, data []byte, fileName, caption string) error {
	u, ct, err := w.sessionContext(sessionID)
	if err != nil {
		return err
	}
	return w.api.SendMediaFile(ctx, u, ct, data, fileName, caption)
}

func (w *Weixin) UploadFile(ctx context.Context, sessionID string, plaintext []byte, mediaType ilink.UploadMediaType) (*ilink.UploadResult, error) {
	u, err := w.parseToUser(sessionID)
	if err != nil {
		return nil, err
	}
	return w.api.UploadFile(ctx, plaintext, u, mediaType)
}

func (w *Weixin) GetUploadURL(ctx context.Context, sessionID string, req *ilink.GetUploadURLReq) (*ilink.GetUploadURLResp, error) {
	if w.api == nil {
		return nil, fmt.Errorf("weixin client not started")
	}
	if req == nil {
		return nil, fmt.Errorf("weixin: nil getuploadurl req")
	}
	u, err := w.parseToUser(sessionID)
	if err != nil {
		return nil, err
	}
	r := *req
	if strings.TrimSpace(r.ToUserID) == "" {
		r.ToUserID = u
	} else if strings.TrimSpace(r.ToUserID) != u {
		return nil, fmt.Errorf("weixin: getuploadurl to_user_id mismatch session")
	}
	return w.api.GetUploadURL(ctx, &r)
}

func (w *Weixin) SendImage(ctx context.Context, sessionID string, uploaded *ilink.UploadResult) (string, error) {
	u, ct, err := w.sessionContext(sessionID)
	if err != nil {
		return "", err
	}
	return w.api.SendImage(ctx, u, ct, uploaded)
}

func (w *Weixin) SendVideo(ctx context.Context, sessionID string, uploaded *ilink.UploadResult) (string, error) {
	u, ct, err := w.sessionContext(sessionID)
	if err != nil {
		return "", err
	}
	return w.api.SendVideo(ctx, u, ct, uploaded)
}

func (w *Weixin) SendFileAttachment(ctx context.Context, sessionID, fileName string, uploaded *ilink.UploadResult) (string, error) {
	u, ct, err := w.sessionContext(sessionID)
	if err != nil {
		return "", err
	}
	return w.api.SendFileAttachment(ctx, u, ct, fileName, uploaded)
}

func (w *Weixin) SendMessage(ctx context.Context, msg *ilink.SendMessageReq) error {
	if w.api == nil {
		return fmt.Errorf("weixin client not started")
	}
	if msg == nil || msg.Msg == nil {
		return fmt.Errorf("weixin: nil sendmessage payload")
	}
	return w.api.SendMessage(ctx, msg)
}

func (w *Weixin) SendText(ctx context.Context, sessionID, text string) (string, error) {
	u, ct, err := w.sessionContext(sessionID)
	if err != nil {
		return "", err
	}
	return w.api.SendText(ctx, u, strings.TrimSpace(text), ct)
}

func (w *Weixin) PushText(ctx context.Context, sessionID, text string) (string, error) {
	if w.api == nil {
		return "", fmt.Errorf("weixin client not started")
	}
	u, err := w.parseToUser(sessionID)
	if err != nil {
		return "", err
	}
	return w.api.Push(ctx, u, strings.TrimSpace(text))
}

func (w *Weixin) DownloadMedia(ctx context.Context, media *ilink.CDNMedia) ([]byte, error) {
	if w.api == nil {
		return nil, fmt.Errorf("weixin client not started")
	}
	return w.api.DownloadMedia(ctx, media)
}

func (w *Weixin) DownloadMediaRaw(ctx context.Context, media *ilink.CDNMedia) ([]byte, error) {
	if w.api == nil {
		return nil, fmt.Errorf("weixin client not started")
	}
	return w.api.DownloadMediaRaw(ctx, media)
}

func (w *Weixin) DownloadVoice(ctx context.Context, voice *ilink.VoiceItem) ([]byte, error) {
	if w.api == nil {
		return nil, fmt.Errorf("weixin client not started")
	}
	return w.api.DownloadVoice(ctx, voice)
}

func (w *Weixin) DownloadInboundItem(ctx context.Context, item *ilink.MessageItem) ([]byte, error) {
	if w.api == nil {
		return nil, fmt.Errorf("weixin client not started")
	}
	if item == nil {
		return nil, fmt.Errorf("weixin: nil message item")
	}
	var media *ilink.CDNMedia
	switch item.Type {
	case ilink.ItemImage:
		if item.ImageItem != nil {
			media = item.ImageItem.Media
		}
	case ilink.ItemFile:
		if item.FileItem != nil {
			media = item.FileItem.Media
		}
	case ilink.ItemVideo:
		if item.VideoItem != nil {
			media = item.VideoItem.Media
		}
	case ilink.ItemVoice:
		if item.VoiceItem != nil {
			media = item.VoiceItem.Media
		}
	default:
		return nil, fmt.Errorf("weixin: item type %d has no cdn media", item.Type)
	}
	if media == nil {
		return nil, fmt.Errorf("weixin: missing cdn media for item type %d", item.Type)
	}
	return w.api.DownloadMedia(ctx, media)
}

type ClawbotSession struct {
	BotToken    string `json:"bot_token"`
	BaseURL     string `json:"base_url,omitempty"`
	ILinkBotID  string `json:"ilink_bot_id,omitempty"`
	ILinkUserID string `json:"ilink_user_id,omitempty"`
}

// clawbotSessionPath resolves the login file inside one primary agent's
// workspace (<workspaceRoot>/state/weixin_clawbot.json).
func clawbotSessionPath(root string) string {
	return filepath.Join(root, "state", "weixin_clawbot.json")
}

func LoadClawbotSession(root string) (*ClawbotSession, error) {
	p := clawbotSessionPath(root)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var s ClawbotSession
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s.BotToken) == "" {
		return nil, nil
	}
	return &s, nil
}

// MergePersistedClawbot folds persisted Weixin credentials into one primary
// agent's channel. Both the session file and the
// channel belong to that agent, so workspaceRoot is the agent's workspace and
// the merge target is its own section — a login performed under one agent
// never configures another's bot.
func MergePersistedClawbot(ch *appcfg.ChannelsSection, workspaceRoot string) {
	if ch == nil || strings.TrimSpace(workspaceRoot) == "" {
		return
	}
	s, err := LoadClawbotSession(workspaceRoot)
	if err != nil || s == nil {
		return
	}
	fromFile := false
	if strings.TrimSpace(ch.Weixin.Token) == "" && strings.TrimSpace(s.BotToken) != "" {
		ch.Weixin.Token = strings.TrimSpace(s.BotToken)
		fromFile = true
	}
	if fromFile && strings.TrimSpace(s.BaseURL) != "" {
		ch.Weixin.BaseURL = strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	}
	if fromFile && strings.TrimSpace(s.ILinkBotID) != "" && strings.TrimSpace(ch.Weixin.AccountID) == "" {
		ch.Weixin.AccountID = strings.TrimSpace(s.ILinkBotID)
	}
}

type weixinSyncFile struct {
	Users         map[string]string `json:"users"`
	GetUpdatesBuf string            `json:"get_updates_buf,omitempty"`
}

type legacyContextTokenFile struct {
	Users map[string]string `json:"users"`
}

func weixinSyncPath(root string) string {
	return filepath.Join(root, "state", "weixin_context_tokens.json")
}

func loadWeixinSyncState(root string) (users map[string]string, getUpdatesBuf string) {
	users = map[string]string{}
	if root == "" {
		return users, ""
	}
	b, err := os.ReadFile(weixinSyncPath(root))
	if err != nil {
		return users, ""
	}
	var f weixinSyncFile
	if json.Unmarshal(b, &f) == nil && f.Users != nil {
		for k, v := range f.Users {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if k != "" && v != "" {
				users[k] = v
			}
		}
		return users, strings.TrimSpace(f.GetUpdatesBuf)
	}
	var leg legacyContextTokenFile
	if json.Unmarshal(b, &leg) == nil && leg.Users != nil {
		for k, v := range leg.Users {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if k != "" && v != "" {
				users[k] = v
			}
		}
	}
	return users, ""
}

type contextDisk struct {
	root string
	mu   sync.Mutex
}

func (d *contextDisk) persistFull(users map[string]string, getUpdatesBuf string) {
	if d == nil || d.root == "" || users == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(d.root, "state"), 0o755); err != nil {
		return
	}
	f := weixinSyncFile{Users: map[string]string{}, GetUpdatesBuf: strings.TrimSpace(getUpdatesBuf)}
	for k, v := range users {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k != "" && v != "" {
			f.Users[k] = v
		}
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	_ = os.WriteFile(weixinSyncPath(d.root), b, 0o600)
}
