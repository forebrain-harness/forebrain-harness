package channel

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/testutil"
)

const testAESKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"

func TestWeComNewIDStartStopAndRegister(t *testing.T) {
	w := NewWeCom(WeComConfig{})
	if w == nil || w.ID() != "wecom" || w.cfg.ChannelID != "wecom" || w.cfg.CallbackPath != "/wecom/callback" || w.httpClient == nil || w.seenMsg == nil {
		t.Fatalf("wecom=%#v", w)
	}
	custom := NewWeCom(WeComConfig{ChannelID: " corp ", CallbackPath: " /cb "})
	if custom.ID() != "corp" || custom.cfg.CallbackPath != "/cb" {
		t.Fatalf("custom id/path=%q %q", custom.ID(), custom.cfg.CallbackPath)
	}
	routes := map[string]http.HandlerFunc{}
	add := func(method, path string, hf http.HandlerFunc) {
		routes[method+" "+path] = hf
	}
	if err := custom.Start(context.Background(), add, &wecomBus{}); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if custom.bus == nil || routes["GET /cb"] == nil || routes["POST /cb"] == nil || routes["GET /channels/corp/health"] == nil {
		t.Fatalf("routes=%v bus=%v", routes, custom.bus)
	}
	rr := httptest.NewRecorder()
	routes["GET /channels/corp/health"](rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("disabled health code=%d", rr.Code)
	}
	enabled := NewWeCom(WeComConfig{ChannelID: "corp", Enabled: true})
	routes = map[string]http.HandlerFunc{}
	if err := enabled.Start(context.Background(), add, nil); err != nil {
		t.Fatalf("enabled Start error: %v", err)
	}
	rr = httptest.NewRecorder()
	routes["GET /channels/corp/health"](rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("health code=%d body=%q", rr.Code, rr.Body.String())
	}
	if err := enabled.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
}

func TestWeComHandleVerify(t *testing.T) {
	cfg := WeComConfig{Enabled: true, Token: "tok", EncodingAESKey: testAESKey, CorpID: "corp"}
	encrypted := encryptWeCom(t, testAESKey, "corp", []byte("echo-ok"))
	sig := wecomSignature("tok", "1", "nonce", encrypted)
	w := NewWeCom(cfg)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/cb?msg_signature="+url.QueryEscape(sig)+"&timestamp=1&nonce=nonce&echostr="+url.QueryEscape(encrypted), nil)
	w.handleVerify(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != "echo-ok" {
		t.Fatalf("verify code=%d body=%q", rr.Code, rr.Body.String())
	}

	tests := []struct {
		name string
		cfg  WeComConfig
		raw  string
		want int
	}{
		{name: "disabled", cfg: WeComConfig{}, raw: "/cb", want: http.StatusNotFound},
		{name: "missing params", cfg: cfg, raw: "/cb", want: http.StatusBadRequest},
		{name: "bad crypt config", cfg: WeComConfig{Enabled: true}, raw: "/cb?msg_signature=x&timestamp=1&nonce=n&echostr=e", want: http.StatusInternalServerError},
		{name: "bad signature", cfg: cfg, raw: "/cb?msg_signature=bad&timestamp=1&nonce=nonce&echostr=" + url.QueryEscape(encrypted), want: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			NewWeCom(tt.cfg).handleVerify(rr, httptest.NewRequest(http.MethodGet, tt.raw, nil))
			if rr.Code != tt.want {
				t.Fatalf("code=%d body=%q", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestWeComHandleMsg(t *testing.T) {
	cfg := WeComConfig{ChannelID: "wecom", Enabled: true, Token: "tok", EncodingAESKey: testAESKey, CorpID: "corp"}
	msgXML := []byte(`<xml><ToUserName>corp</ToUserName><FromUserName>user1</FromUserName><CreateTime>123</CreateTime><MsgType>text</MsgType><MsgId>m1</MsgId><Content> hi </Content></xml>`)
	encrypted := encryptWeCom(t, testAESKey, "corp", msgXML)
	sig := wecomSignature("tok", "1", "nonce", encrypted)
	bus := &wecomBus{err: io.ErrClosedPipe}
	w := NewWeCom(cfg)
	w.bus = bus
	body := `<xml><Encrypt>` + encrypted + `</Encrypt></xml>`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/cb?msg_signature="+url.QueryEscape(sig)+"&timestamp=1&nonce=nonce", strings.NewReader(body))
	w.handleMsg(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != "success" || rr.Header().Get("Content-Type") != "text/plain" {
		t.Fatalf("code=%d body=%q contentType=%q", rr.Code, rr.Body.String(), rr.Header().Get("Content-Type"))
	}
	if len(bus.events) != 1 || bus.events[0].ChannelID != "wecom" || bus.events[0].SessionID != "corp:user1" || bus.events[0].Text != "hi" || bus.events[0].Raw == nil {
		t.Fatalf("events=%+v", bus.events)
	}
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/cb?msg_signature="+url.QueryEscape(sig)+"&timestamp=1&nonce=nonce", strings.NewReader(body))
	w.handleMsg(rr, req)
	if rr.Code != http.StatusOK || len(bus.events) != 1 {
		t.Fatalf("duplicate code=%d events=%+v", rr.Code, bus.events)
	}

	eventXML := []byte(`<xml><ToUserName>corp</ToUserName><FromUserName>user2</FromUserName><CreateTime>124</CreateTime><MsgType>event</MsgType><Event>subscribe</Event></xml>`)
	encrypted = encryptWeCom(t, testAESKey, "corp", eventXML)
	sig = wecomSignature("tok", "2", "nonce", encrypted)
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/cb?msg_signature="+url.QueryEscape(sig)+"&timestamp=2&nonce=nonce", strings.NewReader(`<xml><Encrypt>`+encrypted+`</Encrypt></xml>`))
	w.handleMsg(rr, req)
	if rr.Code != http.StatusOK || len(bus.events) != 2 || bus.events[1].Text != "/start" {
		t.Fatalf("event code=%d events=%+v", rr.Code, bus.events)
	}

	tests := []struct {
		name string
		cfg  WeComConfig
		raw  string
		body io.Reader
		want int
	}{
		{name: "disabled", cfg: WeComConfig{}, raw: "/cb", body: strings.NewReader(``), want: http.StatusNotFound},
		{name: "missing params", cfg: cfg, raw: "/cb", body: strings.NewReader(``), want: http.StatusBadRequest},
		{name: "bad body", cfg: cfg, raw: "/cb?msg_signature=s&timestamp=t&nonce=n", body: wecomErrReader{}, want: http.StatusBadRequest},
		{name: "bad xml", cfg: cfg, raw: "/cb?msg_signature=s&timestamp=t&nonce=n", body: strings.NewReader(`{`), want: http.StatusBadRequest},
		{name: "bad crypt config", cfg: WeComConfig{Enabled: true}, raw: "/cb?msg_signature=s&timestamp=t&nonce=n", body: strings.NewReader(`<xml/>`), want: http.StatusInternalServerError},
		{name: "decrypt fail", cfg: cfg, raw: "/cb?msg_signature=bad&timestamp=t&nonce=n", body: strings.NewReader(`<xml><Encrypt>x</Encrypt></xml>`), want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			NewWeCom(tt.cfg).handleMsg(rr, httptest.NewRequest(http.MethodPost, tt.raw, tt.body))
			if rr.Code != tt.want {
				t.Fatalf("code=%d body=%q", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestWeComDeliverOutbound(t *testing.T) {
	resetWeComGlobals(t)
	if err := NewWeCom(WeComConfig{}).DeliverOutbound(context.Background(), Outbound{}); err == nil {
		t.Fatal("expected disabled error")
	}
	if err := NewWeCom(WeComConfig{Enabled: true}).DeliverOutbound(context.Background(), Outbound{}); err == nil {
		t.Fatal("expected config error")
	}
	if err := NewWeCom(WeComConfig{Enabled: true, CorpID: "corp", CorpSecret: "sec", AgentID: 1}).DeliverOutbound(context.Background(), Outbound{SessionID: " "}); err == nil {
		t.Fatal("expected invalid session error")
	}
	wecomAPIBase = "http://127.0.0.1:1"
	if err := NewWeCom(WeComConfig{Enabled: true, CorpID: "corp", CorpSecret: "sec", AgentID: 1}).DeliverOutbound(context.Background(), Outbound{SessionID: "corp:user"}); err == nil {
		t.Fatal("expected token error")
	}
	resetWeComGlobals(t)

	w := NewWeCom(WeComConfig{Enabled: true, CorpID: "corp", CorpSecret: "sec", AgentID: 1})
	w.accessToken = "tok"
	w.tokenExpires = time.Now().Add(time.Hour)
	wecomMarshalJSON = func(any) ([]byte, error) { return nil, errors.New("marshal failed") }
	if err := w.DeliverOutbound(context.Background(), Outbound{SessionID: "corp:user"}); err == nil {
		t.Fatal("expected marshal error")
	}
	resetWeComGlobals(t)

	w = NewWeCom(WeComConfig{Enabled: true, CorpID: "corp", CorpSecret: "sec", AgentID: 1})
	w.accessToken = "tok"
	w.tokenExpires = time.Now().Add(time.Hour)
	wecomAPIBase = "://bad"
	if err := w.DeliverOutbound(context.Background(), Outbound{SessionID: "corp:user"}); err == nil {
		t.Fatal("expected request error")
	}
	resetWeComGlobals(t)

	w = NewWeCom(WeComConfig{Enabled: true, CorpID: "corp", CorpSecret: "sec", AgentID: 1})
	w.accessToken = "tok"
	w.tokenExpires = time.Now().Add(time.Hour)
	wecomAPIBase = "http://127.0.0.1:1"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.DeliverOutbound(ctx, Outbound{SessionID: "corp:user"}); err == nil {
		t.Fatal("expected client error")
	}
	resetWeComGlobals(t)

	badJSON := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write([]byte(`{`))
	}))
	defer badJSON.Close()
	w = NewWeCom(WeComConfig{Enabled: true, CorpID: "corp", CorpSecret: "sec", AgentID: 1})
	w.accessToken = "tok"
	w.tokenExpires = time.Now().Add(time.Hour)
	wecomAPIBase = badJSON.URL
	if err := w.DeliverOutbound(context.Background(), Outbound{SessionID: "corp:user"}); err == nil {
		t.Fatal("expected decode error")
	}
	resetWeComGlobals(t)

	apiErr := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(rw).Encode(map[string]any{"errcode": 400, "errmsg": "bad"})
	}))
	defer apiErr.Close()
	w = NewWeCom(WeComConfig{Enabled: true, CorpID: "corp", CorpSecret: "sec", AgentID: 1})
	w.accessToken = "tok"
	w.tokenExpires = time.Now().Add(time.Hour)
	wecomAPIBase = apiErr.URL
	err := w.DeliverOutbound(context.Background(), Outbound{SessionID: "corp:user"})
	if err == nil || !strings.Contains(err.Error(), "wecom send failed errcode=400") {
		t.Fatalf("api error=%v", err)
	}
	resetWeComGlobals(t)

	var gotPath, gotToken, gotContentType string
	var gotBody map[string]any
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.URL.Query().Get("access_token")
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode send body: %v", err)
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"errcode": 0})
	}))
	defer srv.Close()
	w = NewWeCom(WeComConfig{Enabled: true, CorpID: "corp", CorpSecret: "sec", AgentID: 42})
	w.accessToken = "tok"
	w.tokenExpires = time.Now().Add(time.Hour)
	wecomAPIBase = srv.URL
	if err := w.DeliverOutbound(context.Background(), Outbound{SessionID: "corp:user", Text: " hi "}); err != nil {
		t.Fatalf("DeliverOutbound error: %v", err)
	}
	if gotPath != "/cgi-bin/message/send" || gotToken != "tok" || gotContentType != "application/json" || gotBody["touser"] != "user" || gotBody["msgtype"] != "text" || gotBody["agentid"].(float64) != 42 || gotBody["safe"].(float64) != 0 {
		t.Fatalf("path=%q token=%q contentType=%q body=%v", gotPath, gotToken, gotContentType, gotBody)
	}
	text := gotBody["text"].(map[string]any)
	if text["content"] != "hi" {
		t.Fatalf("text=%v", text)
	}
}

func TestWeComGetAccessToken(t *testing.T) {
	resetWeComGlobals(t)
	w := NewWeCom(WeComConfig{})
	w.accessToken = " cached "
	w.tokenExpires = time.Now().Add(time.Hour)
	token, err := w.getAccessToken(context.Background())
	if err != nil || token != " cached " {
		t.Fatalf("cached token=%q err=%v", token, err)
	}

	wecomAPIBase = "://bad"
	if _, err := NewWeCom(WeComConfig{CorpID: "corp", CorpSecret: "sec"}).getAccessToken(context.Background()); err == nil {
		t.Fatal("expected request error")
	}
	resetWeComGlobals(t)

	wecomAPIBase = "http://127.0.0.1:1"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewWeCom(WeComConfig{CorpID: "corp", CorpSecret: "sec"}).getAccessToken(ctx); err == nil {
		t.Fatal("expected client error")
	}
	resetWeComGlobals(t)

	badJSON := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write([]byte(`{`))
	}))
	defer badJSON.Close()
	wecomAPIBase = badJSON.URL
	if _, err := NewWeCom(WeComConfig{CorpID: "corp", CorpSecret: "sec"}).getAccessToken(context.Background()); err == nil {
		t.Fatal("expected decode error")
	}
	resetWeComGlobals(t)

	apiErr := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(rw).Encode(map[string]any{"errcode": 400, "errmsg": "bad"})
	}))
	defer apiErr.Close()
	wecomAPIBase = apiErr.URL
	if _, err := NewWeCom(WeComConfig{CorpID: "corp", CorpSecret: "sec"}).getAccessToken(context.Background()); err == nil {
		t.Fatal("expected api error")
	}
	resetWeComGlobals(t)

	var gotCorpID, gotSecret string
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		gotCorpID = r.URL.Query().Get("corpid")
		gotSecret = r.URL.Query().Get("corpsecret")
		_ = json.NewEncoder(rw).Encode(map[string]any{"errcode": 0, "access_token": " fresh ", "expires_in": 120})
	}))
	defer srv.Close()
	wecomAPIBase = srv.URL
	w = NewWeCom(WeComConfig{CorpID: " corp ", CorpSecret: " sec "})
	token, err = w.getAccessToken(context.Background())
	if err != nil || token != "fresh" || w.accessToken != "fresh" || time.Until(w.tokenExpires) <= time.Minute || gotCorpID != " corp " || gotSecret != " sec " {
		t.Fatalf("token=%q cached=%q expires=%v corp=%q secret=%q err=%v", token, w.accessToken, w.tokenExpires, gotCorpID, gotSecret, err)
	}
}

func TestWeComCryptoAndHelpers(t *testing.T) {
	if _, err := NewWXBizMsgCrypt("", testAESKey, "corp"); err == nil {
		t.Fatal("expected token required")
	}
	if _, err := NewWXBizMsgCrypt("tok", "", "corp"); err == nil {
		t.Fatal("expected key required")
	}
	if _, err := NewWXBizMsgCrypt("tok", "short", "corp"); err == nil {
		t.Fatal("expected key length error")
	}
	if _, err := NewWXBizMsgCrypt("tok", strings.Repeat("!", 43), "corp"); err == nil {
		t.Fatal("expected base64 error")
	}
	if _, err := NewWXBizMsgCrypt("tok", testAESKey, ""); err == nil {
		t.Fatal("expected receive id error")
	}
	crypt, err := NewWXBizMsgCrypt("tok", testAESKey, "corp")
	if err != nil {
		t.Fatalf("NewWXBizMsgCrypt error: %v", err)
	}
	encrypted := encryptWeCom(t, testAESKey, "corp", []byte("plain"))
	sig := wecomSignature("tok", "1", "n", encrypted)
	plain, err := crypt.VerifyURL(sig, "1", "n", encrypted)
	if err != nil || plain != "plain" {
		t.Fatalf("VerifyURL plain=%q err=%v", plain, err)
	}
	if _, err := crypt.Decrypt("bad", "1", "n", encrypted); err == nil {
		t.Fatal("expected signature mismatch")
	}
	if _, err := crypt.Decrypt(wecomSignature("tok", "1", "n", "bad"), "1", "n", "bad"); err == nil {
		t.Fatal("expected base64 error")
	}
	invalidKey := &WXBizMsgCrypt{token: "tok", receiveID: "corp", key: []byte("bad"), iv: make([]byte, aes.BlockSize)}
	encryptedForInvalidKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{byte(aes.BlockSize)}, aes.BlockSize))
	if _, err := invalidKey.Decrypt(wecomSignature("tok", "1", "n", encryptedForInvalidKey), "1", "n", encryptedForInvalidKey); err == nil {
		t.Fatal("expected cipher key error")
	}
	badSize := base64.StdEncoding.EncodeToString([]byte("short"))
	if _, err := crypt.Decrypt(wecomSignature("tok", "1", "n", badSize), "1", "n", badSize); err == nil {
		t.Fatal("expected ciphertext size error")
	}
	badPadding := encryptRawWeCom(t, testAESKey, []byte(strings.Repeat("x", aes.BlockSize-1)+string([]byte{2})))
	if _, err := crypt.Decrypt(wecomSignature("tok", "1", "n", badPadding), "1", "n", badPadding); err == nil {
		t.Fatal("expected unpad error")
	}
	shortPlain := encryptRawWeCom(t, testAESKey, pkcs7Pad([]byte("short"), 32))
	if _, err := crypt.Decrypt(wecomSignature("tok", "1", "n", shortPlain), "1", "n", shortPlain); err == nil {
		t.Fatal("expected plaintext size error")
	}
	invalidLenBody := make([]byte, 16+4)
	binary.BigEndian.PutUint32(invalidLenBody[16:20], 100)
	invalidLen := encryptRawWeCom(t, testAESKey, pkcs7Pad(invalidLenBody, 32))
	if _, err := crypt.Decrypt(wecomSignature("tok", "1", "n", invalidLen), "1", "n", invalidLen); err == nil {
		t.Fatal("expected xml length error")
	}
	wrongCorp := encryptWeCom(t, testAESKey, "other", []byte("plain"))
	if _, err := crypt.Decrypt(wecomSignature("tok", "1", "n", wrongCorp), "1", "n", wrongCorp); err == nil {
		t.Fatal("expected receive id mismatch")
	}

	if _, err := wecomUnpad(nil, 32); err == nil {
		t.Fatal("expected empty unpad error")
	}
	if _, err := wecomUnpad([]byte{0}, 32); err == nil {
		t.Fatal("expected invalid padding")
	}
	if _, err := wecomUnpad([]byte{1, 2}, 32); err == nil {
		t.Fatal("expected malformed padding")
	}
	if got, err := wecomUnpad([]byte{'a', 1}, 32); err != nil || string(got) != "a" {
		t.Fatalf("unpad got=%q err=%v", got, err)
	}

	msg, err := parseWeComMessage([]byte(`<xml><ToUserName>corp</ToUserName><FromUserName>user</FromUserName><CreateTime>99</CreateTime><MsgType>Event</MsgType><Event>ENTER_AGENT</Event></xml>`))
	if err != nil || msg.MsgID != "user:99" || msg.MsgType != "event" || msg.Event != "enter_agent" {
		t.Fatalf("msg=%+v err=%v", msg, err)
	}
	if _, err := parseWeComMessage([]byte(`{`)); err == nil {
		t.Fatal("expected parse xml error")
	}
	if normalizeInboundText(nil) != "" || normalizeInboundText(&wecomMessage{MsgType: "image"}) != "" || normalizeInboundText(&wecomMessage{MsgType: "event", Event: "click"}) != "" {
		t.Fatal("normalize empty cases mismatch")
	}
	if normalizeInboundText(&wecomMessage{MsgType: "event", Event: "subscribe"}) != "/start" || normalizeInboundText(&wecomMessage{MsgType: "event", Event: "enter_agent"}) != "/start" || normalizeInboundText(&wecomMessage{MsgType: "text", Content: " hi "}) != "hi" {
		t.Fatal("normalize positive cases mismatch")
	}
	if wecomSession("", "user") != "user" || wecomSession("corp", "") != "corp" || wecomSession("corp", "user") != "corp:user" {
		t.Fatal("wecomSession mismatch")
	}
	if wecomUser("corp:user") != "user" || wecomUser("user") != "user" || wecomUser(" ") != "" {
		t.Fatal("wecomUser mismatch")
	}
	if wecomTruncateUTF8(" abc ", 0) != "" || wecomTruncateUTF8(" abc ", 10) != "abc" || wecomTruncateUTF8("こんにちはさようなら", 2) != "こん" {
		t.Fatal("wecomTruncateUTF8 mismatch")
	}

	w := NewWeCom(WeComConfig{})
	if !w.acceptMessage("") {
		t.Fatal("blank msg id should be accepted")
	}
	if !w.acceptMessage("m1") || w.acceptMessage("m1") {
		t.Fatal("dedup mismatch")
	}
	w.seenMsg["old"] = time.Now().Add(-10 * time.Minute)
	if !w.acceptMessage("m2") {
		t.Fatal("m2 should be accepted")
	}
	if _, ok := w.seenMsg["old"]; ok {
		t.Fatal("old seen message was not pruned")
	}
}

func resetWeComGlobals(t *testing.T) {
	t.Helper()
	wecomAPIBase = "https://qyapi.weixin.qq.com"
	wecomMarshalJSON = json.Marshal
	t.Cleanup(func() {
		wecomAPIBase = "https://qyapi.weixin.qq.com"
		wecomMarshalJSON = json.Marshal
	})
}

func encryptWeCom(t *testing.T, encodingAESKey string, receiveID string, xmlBody []byte) string {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodingAESKey) + "=")
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}
	plain := make([]byte, 16+4+len(xmlBody)+len(receiveID))
	if _, err := rand.Read(plain[:16]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	binary.BigEndian.PutUint32(plain[16:20], uint32(len(xmlBody)))
	copy(plain[20:], xmlBody)
	copy(plain[20+len(xmlBody):], receiveID)
	plain = pkcs7Pad(plain, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	mode := cipher.NewCBCEncrypter(block, key[:16])
	out := make([]byte, len(plain))
	mode.CryptBlocks(out, plain)
	return base64.StdEncoding.EncodeToString(out)
}

func encryptRawWeCom(t *testing.T, encodingAESKey string, plain []byte) string {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodingAESKey) + "=")
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	if len(plain)%aes.BlockSize != 0 {
		t.Fatalf("plain len %d is not block aligned", len(plain))
	}
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, key[:16]).CryptBlocks(out, plain)
	return base64.StdEncoding.EncodeToString(out)
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	return append(data, bytes.Repeat([]byte{byte(pad)}, pad)...)
}

type wecomErrReader struct{}

func (wecomErrReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

type wecomBus struct {
	events []Inbound
	err    error
}

func (b *wecomBus) PublishInbound(_ context.Context, in Inbound) error {
	b.events = append(b.events, in)
	return b.err
}

var _ = xml.Name{}
