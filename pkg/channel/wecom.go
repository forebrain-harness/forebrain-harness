// The WeCom channel and its message crypto.
package channel

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type WeComConfig struct {
	ChannelID      string
	Enabled        bool
	Token          string
	EncodingAESKey string
	CorpID         string
	CorpSecret     string
	AgentID        int64
	CallbackPath   string
}

type WeCom struct {
	cfg          WeComConfig
	bus          Bus
	dedupMu      sync.Mutex
	seenMsg      map[string]time.Time
	tokenMu      sync.Mutex
	accessToken  string
	tokenExpires time.Time
	httpClient   *http.Client
}

var (
	wecomAPIBase     = "https://qyapi.weixin.qq.com"
	wecomMarshalJSON = json.Marshal
)

func NewWeCom(cfg WeComConfig) *WeCom {
	channelID := strings.TrimSpace(cfg.ChannelID)
	if channelID == "" {
		channelID = "wecom"
	}
	cfg.ChannelID = channelID
	path := strings.TrimSpace(cfg.CallbackPath)
	if path == "" {
		path = "/wecom/callback"
	}
	cfg.CallbackPath = path
	return &WeCom{
		cfg:        cfg,
		seenMsg:    map[string]time.Time{},
		httpClient: &http.Client{Timeout: 12 * time.Second},
	}
}

func (w *WeCom) ID() string { return w.cfg.ChannelID }

func (w *WeCom) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	w.bus = bus
	add(http.MethodGet, w.cfg.CallbackPath, w.handleVerify)
	add(http.MethodPost, w.cfg.CallbackPath, w.handleMsg)
	add(http.MethodGet, "/channels/"+w.cfg.ChannelID+"/health", func(rw http.ResponseWriter, r *http.Request) {
		if !w.cfg.Enabled {
			http.Error(rw, "disabled", http.StatusNotFound)
			return
		}
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
	})
	return nil
}

func (w *WeCom) Stop(ctx context.Context) error { return nil }

func (w *WeCom) handleVerify(rw http.ResponseWriter, r *http.Request) {
	if !w.cfg.Enabled {
		http.Error(rw, "disabled", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	msgSig := strings.TrimSpace(q.Get("msg_signature"))
	timestamp := strings.TrimSpace(q.Get("timestamp"))
	nonce := strings.TrimSpace(q.Get("nonce"))
	echoStr := strings.TrimSpace(q.Get("echostr"))
	if msgSig == "" || timestamp == "" || nonce == "" || echoStr == "" {
		http.Error(rw, "invalid verify params", http.StatusBadRequest)
		return
	}
	crypt, err := NewWXBizMsgCrypt(w.cfg.Token, w.cfg.EncodingAESKey, w.cfg.CorpID)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	plain, err := crypt.VerifyURL(msgSig, timestamp, nonce, echoStr)
	if err != nil {
		http.Error(rw, "signature verification failed", http.StatusForbidden)
		return
	}
	_, _ = io.WriteString(rw, plain)
}

func (w *WeCom) handleMsg(rw http.ResponseWriter, r *http.Request) {
	if !w.cfg.Enabled {
		http.Error(rw, "disabled", http.StatusNotFound)
		return
	}
	query := r.URL.Query()
	msgSig := strings.TrimSpace(query.Get("msg_signature"))
	timestamp := strings.TrimSpace(query.Get("timestamp"))
	nonce := strings.TrimSpace(query.Get("nonce"))
	if msgSig == "" || timestamp == "" || nonce == "" {
		http.Error(rw, "invalid callback params", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(rw, "bad body", http.StatusBadRequest)
		return
	}
	var envelope struct {
		XMLName xml.Name `xml:"xml"`
		Encrypt string   `xml:"Encrypt"`
	}
	if err := xml.Unmarshal(body, &envelope); err != nil {
		http.Error(rw, "bad xml", http.StatusBadRequest)
		return
	}
	crypt, err := NewWXBizMsgCrypt(w.cfg.Token, w.cfg.EncodingAESKey, w.cfg.CorpID)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	decrypted, err := crypt.Decrypt(msgSig, timestamp, nonce, strings.TrimSpace(envelope.Encrypt))
	if err != nil {
		http.Error(rw, "decrypt failed", http.StatusBadRequest)
		return
	}
	msg, err := parseWeComMessage(decrypted)
	if err == nil && msg != nil && w.acceptMessage(msg.MsgID) {
		if text := normalizeInboundText(msg); text != "" && w.bus != nil {
			_ = w.bus.PublishInbound(r.Context(), Inbound{
				ChannelID: w.cfg.ChannelID,
				Text:      text,
				SessionID: wecomSession(msg.ToUserName, msg.FromUserName),
				Raw:       string(decrypted),
			})
		}
	}
	rw.Header().Set("Content-Type", "text/plain")
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write([]byte("success"))
}

func (w *WeCom) DeliverOutbound(ctx context.Context, o Outbound) error {
	if !w.cfg.Enabled {
		return fmt.Errorf("wecom disabled")
	}
	if strings.TrimSpace(w.cfg.CorpID) == "" || strings.TrimSpace(w.cfg.CorpSecret) == "" || w.cfg.AgentID <= 0 {
		return fmt.Errorf("wecom corp_id/corp_secret/agent_id required")
	}
	touser := wecomUser(o.SessionID)
	if touser == "" {
		return fmt.Errorf("invalid session id")
	}
	token, err := w.getAccessToken(ctx)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"touser":  touser,
		"msgtype": "text",
		"agentid": w.cfg.AgentID,
		"text": map[string]string{
			"content": wecomTruncateUTF8(o.Text, 2048),
		},
		"safe": 0,
	}
	raw, err := wecomMarshalJSON(payload)
	if err != nil {
		return err
	}
	endpoint := wecomAPIBase + "/cgi-bin/message/send?access_token=" + url.QueryEscape(token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var sendResp struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sendResp); err != nil {
		return err
	}
	if sendResp.ErrCode != 0 {
		return fmt.Errorf("wecom send failed errcode=%d errmsg=%s", sendResp.ErrCode, strings.TrimSpace(sendResp.ErrMsg))
	}
	return nil
}

func (w *WeCom) getAccessToken(ctx context.Context) (string, error) {
	w.tokenMu.Lock()
	if strings.TrimSpace(w.accessToken) != "" && time.Until(w.tokenExpires) > time.Minute {
		token := w.accessToken
		w.tokenMu.Unlock()
		return token, nil
	}
	w.tokenMu.Unlock()
	endpoint := wecomAPIBase + "/cgi-bin/gettoken?corpid=" + url.QueryEscape(w.cfg.CorpID) + "&corpsecret=" + url.QueryEscape(w.cfg.CorpSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	resp, err := w.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var tokenResp struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", err
	}
	if tokenResp.ErrCode != 0 || strings.TrimSpace(tokenResp.AccessToken) == "" {
		return "", fmt.Errorf("wecom gettoken failed errcode=%d errmsg=%s", tokenResp.ErrCode, strings.TrimSpace(tokenResp.ErrMsg))
	}
	expire := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	w.tokenMu.Lock()
	w.accessToken = strings.TrimSpace(tokenResp.AccessToken)
	w.tokenExpires = expire
	w.tokenMu.Unlock()
	return strings.TrimSpace(tokenResp.AccessToken), nil
}

func (w *WeCom) acceptMessage(msgID string) bool {
	msgID = strings.TrimSpace(msgID)
	if msgID == "" {
		return true
	}
	now := time.Now()
	cutoff := now.Add(-5 * time.Minute)
	w.dedupMu.Lock()
	for k, ts := range w.seenMsg {
		if ts.Before(cutoff) {
			delete(w.seenMsg, k)
		}
	}
	if _, ok := w.seenMsg[msgID]; ok {
		w.dedupMu.Unlock()
		return false
	}
	w.seenMsg[msgID] = now
	w.dedupMu.Unlock()
	return true
}

type wecomMessage struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   string   `xml:"CreateTime"`
	MsgType      string   `xml:"MsgType"`
	MsgID        string   `xml:"MsgId"`
	Content      string   `xml:"Content"`
	Event        string   `xml:"Event"`
}

func parseWeComMessage(raw []byte) (*wecomMessage, error) {
	var msg wecomMessage
	if err := xml.Unmarshal(raw, &msg); err != nil {
		return nil, err
	}
	msg.MsgType = strings.ToLower(strings.TrimSpace(msg.MsgType))
	msg.Event = strings.ToLower(strings.TrimSpace(msg.Event))
	if strings.TrimSpace(msg.MsgID) == "" {
		msg.MsgID = strings.TrimSpace(msg.FromUserName) + ":" + strings.TrimSpace(msg.CreateTime)
	}
	return &msg, nil
}

func normalizeInboundText(msg *wecomMessage) string {
	if msg == nil {
		return ""
	}
	switch msg.MsgType {
	case "text":
		return strings.TrimSpace(msg.Content)
	case "event":
		if msg.Event == "subscribe" || msg.Event == "enter_agent" {
			return "/start"
		}
		return ""
	default:
		return ""
	}
}

func wecomSession(corpID string, userID string) string {
	corp := strings.TrimSpace(corpID)
	user := strings.TrimSpace(userID)
	if corp == "" {
		return user
	}
	if user == "" {
		return corp
	}
	return corp + ":" + user
}

func wecomUser(sessionID string) string {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return ""
	}
	parts := strings.SplitN(sid, ":", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[1])
	}
	return sid
}

func wecomTruncateUTF8(s string, max int) string {
	if max <= 0 {
		return ""
	}
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= max {
		return string(rs)
	}
	return string(rs[:max])
}

type WXBizMsgCrypt struct {
	token     string
	receiveID string
	key       []byte
	iv        []byte
}

func NewWXBizMsgCrypt(token string, encodingAESKey string, receiveID string) (*WXBizMsgCrypt, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("token is required")
	}
	if strings.TrimSpace(encodingAESKey) == "" {
		return nil, fmt.Errorf("encoding_aes_key is required")
	}
	if len(strings.TrimSpace(encodingAESKey)) != 43 {
		return nil, fmt.Errorf("encoding_aes_key must be 43 chars")
	}
	if strings.TrimSpace(receiveID) == "" {
		return nil, fmt.Errorf("receive_id is required")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodingAESKey) + "=")
	if err != nil {
		return nil, err
	}
	iv := make([]byte, 16)
	copy(iv, key[:16])
	return &WXBizMsgCrypt{
		token:     strings.TrimSpace(token),
		receiveID: strings.TrimSpace(receiveID),
		key:       key,
		iv:        iv,
	}, nil
}

func (c *WXBizMsgCrypt) VerifyURL(msgSignature string, timestamp string, nonce string, echostr string) (string, error) {
	raw, err := c.Decrypt(msgSignature, timestamp, nonce, echostr)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (c *WXBizMsgCrypt) Decrypt(msgSignature string, timestamp string, nonce string, encryptedBase64 string) ([]byte, error) {
	if wecomSignature(c.token, timestamp, nonce, encryptedBase64) != strings.TrimSpace(msgSignature) {
		return nil, fmt.Errorf("signature mismatch")
	}
	cipherText, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encryptedBase64))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	if len(cipherText)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("invalid ciphertext size")
	}
	plainPadded := make([]byte, len(cipherText))
	mode := cipher.NewCBCDecrypter(block, c.iv)
	mode.CryptBlocks(plainPadded, cipherText)
	plain, err := wecomUnpad(plainPadded, 32)
	if err != nil {
		return nil, err
	}
	if len(plain) < 20 {
		return nil, fmt.Errorf("invalid plaintext size")
	}
	body := plain[16:]
	xmlLen := binary.BigEndian.Uint32(body[:4])
	if int(xmlLen)+4 > len(body) {
		return nil, fmt.Errorf("invalid xml length")
	}
	xmlContent := body[4 : 4+xmlLen]
	recvID := string(body[4+xmlLen:])
	if recvID != c.receiveID {
		return nil, fmt.Errorf("receive_id mismatch")
	}
	return xmlContent, nil
}

func wecomSignature(token string, timestamp string, nonce string, encrypted string) string {
	parts := []string{token, timestamp, nonce, encrypted}
	sort.Strings(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(sum[:])
}

func wecomUnpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty payload")
	}
	pad := int(data[len(data)-1])
	if pad < 1 || pad > blockSize || pad > len(data) {
		return nil, fmt.Errorf("invalid padding")
	}
	suffix := bytes.Repeat([]byte{byte(pad)}, pad)
	if !bytes.Equal(data[len(data)-pad:], suffix) {
		return nil, fmt.Errorf("malformed padding")
	}
	return data[:len(data)-pad], nil
}
