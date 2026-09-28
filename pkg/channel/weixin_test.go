package channel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/testutil"
	ilink "github.com/openilink/openilink-sdk-go"
)

func TestWeixinNewIDRegisterStartStopAndOptions(t *testing.T) {
	w := NewWeixin(WeixinConfig{})
	if w == nil || w.ID() != "weixin" || w.done == nil || w.contextTokens == nil {
		t.Fatalf("weixin=%#v", w)
	}
	opts := buildILinkOptions(WeixinConfig{
		BaseURL:         " http://base/ ",
		CDNBaseURL:      " http://cdn/ ",
		BotType:         " bot ",
		ChannelVersion:  " v1 ",
		RouteTag:        " tag ",
		SilkVoiceDecode: true,
	})
	c := ilink.NewClient("tok", opts...)
	if c.BaseURL() != "http://base" {
		t.Fatalf("BaseURL=%q", c.BaseURL())
	}
	oldSilk := weixinSilkDecode
	_, _ = oldSilk([]byte("not silk"), 24000)
	weixinSilkDecode = func(data []byte, sampleRate int) ([]byte, error) {
		if sampleRate != 24000 || string(data) != "silk" {
			t.Fatalf("silk args data=%q sampleRate=%d", data, sampleRate)
		}
		return []byte{0, 0}, nil
	}
	key := []byte("0123456789abcdef")
	encryptedVoice, err := ilink.EncryptAESECB([]byte("silk"), key)
	if err != nil {
		t.Fatalf("encrypt voice: %v", err)
	}
	voiceSrv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write(encryptedVoice)
	}))
	defer voiceSrv.Close()
	voiceClient := ilink.NewClient("tok", buildILinkOptions(WeixinConfig{SilkVoiceDecode: true})...)
	if wav, err := voiceClient.DownloadVoice(context.Background(), &ilink.VoiceItem{
		SampleRate: 24000,
		Media: &ilink.CDNMedia{
			FullURL: voiceSrv.URL,
			AESKey:  base64.StdEncoding.EncodeToString(key),
		},
	}); err != nil || len(wav) == 0 {
		t.Fatalf("DownloadVoice wavLen=%d err=%v", len(wav), err)
	}
	weixinSilkDecode = oldSilk
	defaultClient := ilink.NewClient("tok", buildILinkOptions(WeixinConfig{})...)
	if defaultClient.BaseURL() != ilink.DefaultBaseURL {
		t.Fatalf("default base=%q", defaultClient.BaseURL())
	}

	if err := NewWeixin(WeixinConfig{}).Start(context.Background(), nil, &weixinBus{}); err != nil {
		t.Fatalf("disabled Start error: %v", err)
	}
	// An enabled weixin channel with no owning agent's workspace has nowhere
	// to keep its login and context tokens, so it must refuse to start rather
	// than fall back to a location shared with other primary agents.
	if err := NewWeixin(WeixinConfig{Enabled: true, Token: "tok"}).Start(context.Background(), nil, nil); err == nil {
		t.Fatal("expected StateRoot error")
	}
	if err := NewWeixin(WeixinConfig{Enabled: true, StateRoot: t.TempDir()}).Start(context.Background(), nil, nil); err == nil {
		t.Fatal("expected missing token error")
	}
	if err := NewWeixin(WeixinConfig{}).Stop(context.Background()); err != nil {
		t.Fatalf("Stop without cancel error: %v", err)
	}
	done := make(chan struct{})
	w.done = done
	w.cancel = func() {}
	oldAfter := weixinStopAfter
	t.Cleanup(func() { weixinStopAfter = oldAfter })
	weixinStopAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	if err := w.Stop(context.Background()); err != nil {
		t.Fatalf("Stop with cancel error: %v", err)
	}
	weixinStopAfter = oldAfter

}

func TestWeixinStartEnabled(t *testing.T) {
	root := t.TempDir()
	d := &contextDisk{root: root}
	d.persistFull(map[string]string{" u ": " ctx "}, " buf ")
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(rw).Encode(map[string]any{"ret": 0, "msgs": []any{}, "get_updates_buf": "next"})
	}))
	defer srv.Close()
	w := NewWeixin(WeixinConfig{Enabled: true, Token: " tok ", BaseURL: srv.URL, StateRoot: root})
	ctx, cancel := context.WithCancel(context.Background())
	if err := w.Start(ctx, nil, &weixinBus{}); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if w.api == nil || w.disk == nil || w.cfg.BaseURL != srv.URL || w.currentBuf() != "buf" || w.getContextToken("u") != "ctx" {
		t.Fatalf("api=%v disk=%v base=%q buf=%q ctx=%q", w.api, w.disk, w.cfg.BaseURL, w.currentBuf(), w.getContextToken("u"))
	}
	cancel()
	if err := w.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
}

func TestWeixinSessionPersistence(t *testing.T) {
	root := t.TempDir()
	if got := ClawbotSessionFileForDisplay(root); got != clawbotSessionPath(root) {
		t.Fatalf("display path=%q", got)
	}
	if s, err := LoadClawbotSession(root); err != nil || s != nil {
		t.Fatalf("missing session=%v err=%v", s, err)
	}
	if err := SaveClawbotSession(root, nil); err != nil {
		t.Fatalf("nil save error: %v", err)
	}
	if err := SaveClawbotSession(root, &ClawbotSession{}); err != nil {
		t.Fatalf("blank save error: %v", err)
	}
	if err := SaveClawbotSession(root, &ClawbotSession{BotToken: " tok ", BaseURL: " http://base/ ", ILinkBotID: " bot "}); err != nil {
		t.Fatalf("save error: %v", err)
	}
	s, err := LoadClawbotSession(root)
	if err != nil || s == nil || s.BotToken != " tok " {
		t.Fatalf("session=%+v err=%v", s, err)
	}
	if err := os.WriteFile(clawbotSessionPath(root), []byte(`{`), 0o600); err != nil {
		t.Fatalf("write bad session: %v", err)
	}
	if _, err := LoadClawbotSession(root); err == nil {
		t.Fatal("expected bad json error")
	}
	if err := os.WriteFile(clawbotSessionPath(root), []byte(`{"bot_token":" "}`), 0o600); err != nil {
		t.Fatalf("write blank session: %v", err)
	}
	if s, err := LoadClawbotSession(root); err != nil || s != nil {
		t.Fatalf("blank session=%v err=%v", s, err)
	}
	readErrRoot := t.TempDir()
	if err := os.RemoveAll(filepath.Join(readErrRoot, "state")); err != nil {
		t.Fatalf("remove read state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(readErrRoot, "state"), []byte("file"), 0o600); err != nil {
		t.Fatalf("write read state file: %v", err)
	}
	// Reading state/weixin_clawbot.json when "state" is a regular file is an
	// IO error on Unix (ENOTDIR), but Windows maps the same condition to
	// ERROR_PATH_NOT_FOUND, which os.IsNotExist treats as "missing" — so
	// LoadClawbotSession legitimately returns (nil, nil) there.
	if _, err := LoadClawbotSession(readErrRoot); err == nil && runtime.GOOS != "windows" {
		t.Fatal("expected read file-through-directory error")
	}
	if err := os.RemoveAll(filepath.Join(root, "state")); err != nil {
		t.Fatalf("remove state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "state"), []byte("file"), 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}
	if err := SaveClawbotSession(root, &ClawbotSession{BotToken: "tok"}); err == nil {
		t.Fatal("expected mkdir state error")
	}
	if err := os.Remove(filepath.Join(root, "state")); err != nil {
		t.Fatalf("remove state file: %v", err)
	}

	ch := &appcfg.ChannelsSection{}
	MergePersistedClawbot(nil, root)
	MergePersistedClawbot(ch, "")
	if err := SaveClawbotSession(root, &ClawbotSession{BotToken: "tok", BaseURL: " http://base/ ", ILinkBotID: "bot"}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	MergePersistedClawbot(ch, root)
	if ch.Weixin.Token != "tok" || ch.Weixin.BaseURL != "http://base" || ch.Weixin.AccountID != "bot" {
		t.Fatalf("merged channels=%+v", ch.Weixin)
	}
	ch.Weixin.Token = "existing"
	MergePersistedClawbot(ch, root)
	if ch.Weixin.Token != "existing" {
		t.Fatalf("existing token overwritten: %+v", ch.Weixin)
	}
	badRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(badRoot, "state"), 0o755); err != nil {
		t.Fatalf("mkdir bad state: %v", err)
	}
	if err := os.WriteFile(clawbotSessionPath(badRoot), []byte(`{`), 0o600); err != nil {
		t.Fatalf("write bad clawbot: %v", err)
	}
	MergePersistedClawbot(&appcfg.ChannelsSection{}, badRoot)
	noIDRoot := t.TempDir()
	if err := SaveClawbotSession(noIDRoot, &ClawbotSession{BotToken: "tok"}); err != nil {
		t.Fatalf("save no id: %v", err)
	}
	ch = &appcfg.ChannelsSection{}
	ch.Weixin.AccountID = "existing"
	MergePersistedClawbot(ch, noIDRoot)
	if ch.Weixin.AccountID != "existing" {
		t.Fatalf("account id overwritten: %+v", ch.Weixin)
	}
}

// A weixin login belongs to the primary agent that performed it: the session
// file lives in that agent's workspace, so merging under another agent's
// workspace finds nothing and leaves its channel unconfigured.
func TestWeixinClawbotSessionIsScopedToOnePrimaryAgent(t *testing.T) {
	mainWorkspace := t.TempDir()
	peerWorkspace := t.TempDir()
	if err := SaveClawbotSession(mainWorkspace, &ClawbotSession{BotToken: "main-tok", ILinkBotID: "main-bot"}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	mainCh := &appcfg.ChannelsSection{}
	MergePersistedClawbot(mainCh, mainWorkspace)
	if mainCh.Weixin.Token != "main-tok" {
		t.Fatalf("owning agent token = %q, want main-tok", mainCh.Weixin.Token)
	}

	peerCh := &appcfg.ChannelsSection{}
	MergePersistedClawbot(peerCh, peerWorkspace)
	if peerCh.Weixin.Token != "" {
		t.Fatalf("peer agent picked up another agent's weixin login: %+v", peerCh.Weixin)
	}
}

func TestWeixinContextDisk(t *testing.T) {
	users, buf := loadWeixinSyncState("")
	if len(users) != 0 || buf != "" {
		t.Fatalf("empty root users=%v buf=%q", users, buf)
	}
	root := t.TempDir()
	d := &contextDisk{root: root}
	d.persistFull(map[string]string{" u1 ": " tok1 ", " ": "skip", "u2": " "}, " buf ")
	users, buf = loadWeixinSyncState(root)
	if len(users) != 1 || users["u1"] != "tok1" || buf != "buf" {
		t.Fatalf("users=%v buf=%q", users, buf)
	}
	if err := os.WriteFile(weixinSyncPath(root), []byte(`{"users":{" u ":" t "},"get_updates_buf":123}`), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	users, buf = loadWeixinSyncState(root)
	if len(users) != 1 || users["u"] != "t" || buf != "" {
		t.Fatalf("legacy users=%v buf=%q", users, buf)
	}
	if err := os.WriteFile(weixinSyncPath(root), []byte(`{`), 0o600); err != nil {
		t.Fatalf("write bad: %v", err)
	}
	users, buf = loadWeixinSyncState(root)
	if len(users) != 0 || buf != "" {
		t.Fatalf("bad users=%v buf=%q", users, buf)
	}
	(&contextDisk{}).persistFull(map[string]string{"u": "t"}, "")
	(*contextDisk)(nil).persistFull(map[string]string{"u": "t"}, "")
	d.persistFull(nil, "")

	fileRoot := filepath.Join(root, "file")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file root: %v", err)
	}
	(&contextDisk{root: fileRoot}).persistFull(map[string]string{"u": "t"}, "")

	if err := os.WriteFile(weixinSyncPath(root), []byte(`{"users":{" ":"x","u":" "},"get_updates_buf":123}`), 0o600); err != nil {
		t.Fatalf("write filtered legacy: %v", err)
	}
	users, buf = loadWeixinSyncState(root)
	if len(users) != 0 || buf != "" {
		t.Fatalf("filtered users=%v buf=%q", users, buf)
	}
}

func TestWeixinDispatchInboundAndHelpers(t *testing.T) {
	bus := &weixinBus{err: errors.New("ignored")}
	root := t.TempDir()
	w := NewWeixin(WeixinConfig{})
	w.bus = bus
	w.disk = &contextDisk{root: root}
	w.api = ilink.NewClient("tok")
	w.dispatchInbound(ilink.WeixinMessage{})
	w.dispatchInbound(ilink.WeixinMessage{FromUserID: " user1 ", ContextToken: " ctx ", ItemList: []ilink.MessageItem{{Type: ilink.ItemText, TextItem: &ilink.TextItem{Text: " hi "}}}})
	if len(bus.events) != 1 || bus.events[0].ChannelID != "weixin" || bus.events[0].SessionID != "user:user1" || bus.events[0].Text != "hi" || bus.events[0].Raw == nil {
		t.Fatalf("events=%+v", bus.events)
	}
	if got := w.getContextToken(" user1 "); got != "ctx" {
		t.Fatalf("context token=%q", got)
	}
	users, _ := loadWeixinSyncState(root)
	if users["user1"] != "ctx" {
		t.Fatalf("persisted users=%v", users)
	}
	w.updateBuf(" buf ")
	if w.currentBuf() != " buf " {
		t.Fatalf("currentBuf=%q", w.currentBuf())
	}
	w.persistSyncFromState()
	_, buf := loadWeixinSyncState(root)
	if buf != "buf" {
		t.Fatalf("persisted buf=%q", buf)
	}

	w = NewWeixin(WeixinConfig{})
	w.bus = bus
	w.dispatchInbound(ilink.WeixinMessage{FromUserID: "u2", ItemList: []ilink.MessageItem{{Type: ilink.ItemImage}, {Type: ilink.ItemVoice}, {Type: ilink.ItemFile}, {Type: ilink.ItemVideo}}})
	if len(bus.events) != 2 || bus.events[1].Text != "[weixin:image] [weixin:voice] [weixin:file] [weixin:video]" {
		t.Fatalf("media events=%+v", bus.events)
	}
	if NewWeixin(WeixinConfig{}).syntheticMediaSummary(nil) != "" {
		t.Fatal("nil media summary should be empty")
	}
	w.dispatchInbound(ilink.WeixinMessage{FromUserID: "u3"})
	if len(bus.events) != 2 {
		t.Fatalf("empty inbound should not publish: %+v", bus.events)
	}
	if parseWeixinSession(" user:u ") != "u" || parseWeixinSession("u") != "u" || parseWeixinSession(" ") != "" {
		t.Fatal("parseWeixinSession mismatch")
	}
	if NewWeixin(WeixinConfig{}).getContextToken(" ") != "" {
		t.Fatal("blank context token should be empty")
	}
	w = NewWeixin(WeixinConfig{})
	w.api = ilink.NewClient("tok")
	w.api.SetContextToken("u", " api-token ")
	if got := w.getContextToken("u"); got != "api-token" {
		t.Fatalf("api context token=%q", got)
	}
}

func TestWeixinClientGuards(t *testing.T) {
	w := NewWeixin(WeixinConfig{})
	if w.ILClient() != nil {
		t.Fatal("new client should be nil")
	}
	if err := w.DeliverOutbound(context.Background(), Outbound{}); err == nil {
		t.Fatal("expected DeliverOutbound not started")
	}
	if _, err := w.parseToUser("u"); err == nil {
		t.Fatal("expected parseToUser not started")
	}
	if _, _, err := w.sessionContext("u"); err == nil {
		t.Fatal("expected sessionContext not started")
	}
	if _, err := w.GetConfig(context.Background(), "u"); err == nil {
		t.Fatal("expected GetConfig not started")
	}
	if err := w.SendTyping(context.Background(), "u", true); err == nil {
		t.Fatal("expected SendTyping not started")
	}
	err := w.WithTyping(context.Background(), "u", func() error { return errors.New("fn failed") })
	if err == nil || err.Error() != "fn failed" {
		t.Fatalf("WithTyping err=%v", err)
	}
	if err := w.SendMediaFile(context.Background(), "u", nil, "a.txt", ""); err == nil {
		t.Fatal("expected SendMediaFile not started")
	}
	if _, err := w.UploadFile(context.Background(), "u", nil, 0); err == nil {
		t.Fatal("expected UploadFile not started")
	}
	if _, err := w.GetUploadURL(context.Background(), "u", nil); err == nil {
		t.Fatal("expected GetUploadURL not started/nil req")
	}
	if _, err := w.SendImage(context.Background(), "u", nil); err == nil {
		t.Fatal("expected SendImage not started")
	}
	if _, err := w.SendVideo(context.Background(), "u", nil); err == nil {
		t.Fatal("expected SendVideo not started")
	}
	if _, err := w.SendFileAttachment(context.Background(), "u", "a.txt", nil); err == nil {
		t.Fatal("expected SendFileAttachment not started")
	}
	if err := w.SendMessage(context.Background(), nil); err == nil {
		t.Fatal("expected SendMessage not started")
	}
	if _, err := w.SendText(context.Background(), "u", "hi"); err == nil {
		t.Fatal("expected SendText not started")
	}
	if _, err := w.PushText(context.Background(), "u", "hi"); err == nil {
		t.Fatal("expected PushText not started")
	}
	if _, err := w.DownloadMedia(context.Background(), nil); err == nil {
		t.Fatal("expected DownloadMedia not started")
	}
	if _, err := w.DownloadMediaRaw(context.Background(), nil); err == nil {
		t.Fatal("expected DownloadMediaRaw not started")
	}
	if _, err := w.DownloadVoice(context.Background(), nil); err == nil {
		t.Fatal("expected DownloadVoice not started")
	}
	if _, err := w.DownloadInboundItem(context.Background(), nil); err == nil {
		t.Fatal("expected DownloadInboundItem not started")
	}

	w.api = ilink.NewClient("tok")
	if err := w.DeliverOutbound(context.Background(), Outbound{SessionID: " "}); err == nil {
		t.Fatal("expected invalid outbound session")
	}
	if err := w.DeliverOutbound(context.Background(), Outbound{SessionID: "u", Text: " "}); err == nil {
		t.Fatal("expected empty outbound text")
	}
	if err := w.DeliverOutbound(context.Background(), Outbound{SessionID: "u", Text: "hi"}); err == nil {
		t.Fatal("expected missing context token")
	}
	if _, err := w.parseToUser(" "); err == nil {
		t.Fatal("expected invalid session")
	}
	if _, _, err := w.sessionContext(" "); err == nil {
		t.Fatal("expected invalid session context")
	}
	if _, _, err := w.sessionContext("u"); err == nil {
		t.Fatal("expected missing context token")
	}
	if _, err := w.GetUploadURL(context.Background(), "u", nil); err == nil {
		t.Fatal("expected nil getuploadurl req")
	}
	if err := w.SendMessage(context.Background(), &ilink.SendMessageReq{}); err == nil {
		t.Fatal("expected nil sendmessage payload")
	}
	if _, err := w.DownloadInboundItem(context.Background(), nil); err == nil {
		t.Fatal("expected nil item")
	}
	if _, err := w.DownloadInboundItem(context.Background(), &ilink.MessageItem{Type: ilink.ItemText}); err == nil {
		t.Fatal("expected unsupported item")
	}
	if _, err := w.DownloadInboundItem(context.Background(), &ilink.MessageItem{Type: ilink.ItemImage}); err == nil {
		t.Fatal("expected missing image media")
	}
	if _, err := w.DownloadInboundItem(context.Background(), &ilink.MessageItem{Type: ilink.ItemFile}); err == nil {
		t.Fatal("expected missing file media")
	}
	if _, err := w.DownloadInboundItem(context.Background(), &ilink.MessageItem{Type: ilink.ItemVideo}); err == nil {
		t.Fatal("expected missing video media")
	}
	if _, err := w.DownloadInboundItem(context.Background(), &ilink.MessageItem{Type: ilink.ItemVoice}); err == nil {
		t.Fatal("expected missing voice media")
	}
}

func TestWeixinHTTPBackedClientMethods(t *testing.T) {
	var paths []string
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/ilink/bot/sendmessage":
			_ = json.NewEncoder(rw).Encode(map[string]any{"ret": 0})
		case "/ilink/bot/getconfig":
			_ = json.NewEncoder(rw).Encode(map[string]any{"ret": 0, "typing_ticket": "ticket"})
		case "/ilink/bot/sendtyping":
			_ = json.NewEncoder(rw).Encode(map[string]any{"ret": 0})
		case "/ilink/bot/getuploadurl":
			_ = json.NewEncoder(rw).Encode(map[string]any{"ret": 0})
		default:
			http.NotFound(rw, r)
		}
	}))
	defer srv.Close()

	w := NewWeixin(WeixinConfig{})
	w.api = ilink.NewClient("tok", ilink.WithBaseURL(srv.URL))
	w.storeContextToken("u", "ctx")
	if _, err := w.GetConfig(context.Background(), "user:u"); err != nil {
		t.Fatalf("GetConfig error: %v", err)
	}
	if err := w.SendTyping(context.Background(), "u", true); err != nil {
		t.Fatalf("SendTyping true error: %v", err)
	}
	if err := w.SendTyping(context.Background(), "u", false); err != nil {
		t.Fatalf("SendTyping false error: %v", err)
	}
	if err := w.SendMediaFile(context.Background(), "u", []byte("x"), "a.txt", "cap"); err == nil {
		t.Fatal("expected SendMediaFile upload error due incomplete upload response")
	}
	if _, err := w.GetUploadURL(context.Background(), "u", &ilink.GetUploadURLReq{}); err != nil {
		t.Fatalf("GetUploadURL error: %v", err)
	}
	if _, err := w.GetUploadURL(context.Background(), " ", &ilink.GetUploadURLReq{}); err == nil {
		t.Fatal("expected GetUploadURL invalid session")
	}
	if _, err := w.UploadFile(context.Background(), "u", []byte("x"), ilink.MediaFile); err == nil {
		t.Fatal("expected UploadFile error")
	}
	if _, err := w.GetUploadURL(context.Background(), "u", &ilink.GetUploadURLReq{ToUserID: "other"}); err == nil {
		t.Fatal("expected to_user_id mismatch")
	}
	uploaded := &ilink.UploadResult{DownloadEncryptedQueryParam: "q", AESKey: "00112233445566778899aabbccddeeff", FileSize: 1, CiphertextSize: 16}
	if _, err := w.SendImage(context.Background(), "u", uploaded); err != nil {
		t.Fatalf("SendImage error: %v", err)
	}
	if _, err := w.SendVideo(context.Background(), "u", uploaded); err != nil {
		t.Fatalf("SendVideo error: %v", err)
	}
	if _, err := w.SendFileAttachment(context.Background(), "u", "a.txt", uploaded); err != nil {
		t.Fatalf("SendFileAttachment error: %v", err)
	}
	if err := w.SendMessage(context.Background(), &ilink.SendMessageReq{Msg: &ilink.WeixinMessage{ToUserID: "u"}}); err != nil {
		t.Fatalf("SendMessage error: %v", err)
	}
	if _, err := w.SendText(context.Background(), "u", " hi "); err != nil {
		t.Fatalf("SendText error: %v", err)
	}
	if _, err := w.PushText(context.Background(), "u", " hi "); err != nil {
		t.Fatalf("PushText error: %v", err)
	}
	if _, err := w.PushText(context.Background(), " ", "hi"); err == nil {
		t.Fatal("expected PushText invalid session")
	}
	if err := w.DeliverOutbound(context.Background(), Outbound{SessionID: "u", Text: " hi "}); err != nil {
		t.Fatalf("DeliverOutbound error: %v", err)
	}
	w = NewWeixin(WeixinConfig{})
	w.api = ilink.NewClient("tok", ilink.WithBaseURL("http://127.0.0.1:1"))
	w.api.SetContextToken("u", "ctx")
	if err := w.DeliverOutbound(context.Background(), Outbound{SessionID: "u", Text: "hi"}); err == nil {
		t.Fatal("expected DeliverOutbound push transport error")
	}
	fallbackSrv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ilink/bot/sendmessage" {
			_ = json.NewEncoder(rw).Encode(map[string]any{"ret": 0})
			return
		}
		http.NotFound(rw, r)
	}))
	defer fallbackSrv.Close()
	w = NewWeixin(WeixinConfig{})
	w.api = ilink.NewClient("tok", ilink.WithBaseURL(fallbackSrv.URL))
	w.contextTokens["u"] = "ctx"
	if err := w.DeliverOutbound(context.Background(), Outbound{SessionID: "u", Text: "hi"}); err != nil {
		t.Fatalf("DeliverOutbound fallback error: %v", err)
	}
	w.api = ilink.NewClient("tok", ilink.WithBaseURL("http://127.0.0.1:1"))
	w.contextTokens["u"] = "ctx"
	if err := w.DeliverOutbound(context.Background(), Outbound{SessionID: "u", Text: "hi"}); err == nil {
		t.Fatal("expected DeliverOutbound send error")
	}
	if len(paths) == 0 {
		t.Fatal("server was not called")
	}

	retErr := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ilink/bot/getconfig" {
			_ = json.NewEncoder(rw).Encode(map[string]any{"ret": 5, "errmsg": "bad"})
			return
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"ret": 0})
	}))
	defer retErr.Close()
	w = NewWeixin(WeixinConfig{})
	w.api = ilink.NewClient("tok", ilink.WithBaseURL(retErr.URL))
	w.storeContextToken("u", "ctx")
	if _, err := w.GetConfig(context.Background(), "u"); err == nil {
		t.Fatal("expected GetConfig ret error")
	}
	if err := w.SendTyping(context.Background(), "u", true); err == nil {
		t.Fatal("expected SendTyping getconfig ret error")
	}

	noTicket := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(rw).Encode(map[string]any{"ret": 0})
	}))
	defer noTicket.Close()
	w = NewWeixin(WeixinConfig{})
	w.api = ilink.NewClient("tok", ilink.WithBaseURL(noTicket.URL))
	w.storeContextToken("u", "ctx")
	if err := w.SendTyping(context.Background(), "u", true); err == nil {
		t.Fatal("expected empty typing ticket")
	}

	w = NewWeixin(WeixinConfig{})
	w.api = ilink.NewClient("tok", ilink.WithBaseURL("http://127.0.0.1:1"))
	w.storeContextToken("u", "ctx")
	if _, err := w.GetConfig(context.Background(), "u"); err == nil {
		t.Fatal("expected GetConfig transport error")
	}
	if err := w.SendTyping(context.Background(), "u", true); err == nil {
		t.Fatal("expected SendTyping transport error")
	}
}

func TestWeixinDownloadInboundItemMedia(t *testing.T) {
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write([]byte("raw"))
	}))
	defer srv.Close()
	media := &ilink.CDNMedia{FullURL: srv.URL, AESKey: "", EncryptQueryParam: "q"}
	w := NewWeixin(WeixinConfig{})
	w.api = ilink.NewClient("tok")
	if _, err := w.DownloadMedia(context.Background(), media); err == nil {
		t.Fatal("expected DownloadMedia error")
	}
	if got, err := w.DownloadMediaRaw(context.Background(), media); err != nil || string(got) != "raw" {
		t.Fatalf("DownloadMediaRaw got=%q err=%v", got, err)
	}
	if _, err := w.DownloadVoice(context.Background(), &ilink.VoiceItem{Media: media}); err == nil {
		t.Fatal("expected DownloadVoice error")
	}
	for _, item := range []*ilink.MessageItem{
		{Type: ilink.ItemImage, ImageItem: &ilink.ImageItem{Media: media}},
		{Type: ilink.ItemFile, FileItem: &ilink.FileItem{Media: media}},
		{Type: ilink.ItemVideo, VideoItem: &ilink.VideoItem{Media: media}},
		{Type: ilink.ItemVoice, VoiceItem: &ilink.VoiceItem{Media: media}},
	} {
		if _, err := w.DownloadInboundItem(context.Background(), item); err == nil {
			t.Fatal("expected raw download/decrypt error for media")
		}
	}
}

func TestWeixinRunMonitor(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		w := NewWeixin(WeixinConfig{})
		w.api = ilink.NewClient("tok", ilink.WithBaseURL("http://127.0.0.1:1"))
		w.updateBuf("initial")
		w.done = make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		w.runMonitor(ctx)
		select {
		case <-w.done:
		default:
			t.Fatal("runMonitor did not close done")
		}
	})

	t.Run("buf update and dispatch", func(t *testing.T) {
		root := t.TempDir()
		var calls int
		srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			calls++
			_ = json.NewEncoder(rw).Encode(map[string]any{
				"ret":             0,
				"get_updates_buf": " next ",
				"msgs": []map[string]any{
					{
						"from_user_id":  "user",
						"context_token": "ctx",
						"item_list": []map[string]any{
							{"type": ilink.ItemText, "text_item": map[string]any{"text": "hi"}},
						},
					},
				},
			})
		}))
		defer srv.Close()
		bus := &weixinBus{}
		w := NewWeixin(WeixinConfig{})
		w.api = ilink.NewClient("tok", ilink.WithBaseURL(srv.URL))
		w.bus = bus
		w.disk = &contextDisk{root: root}
		w.done = make(chan struct{})
		ctx, c := context.WithCancel(context.Background())
		go w.runMonitor(ctx)
		for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
			if w.currentBuf() == "next" {
				break
			}
			time.Sleep(time.Millisecond)
		}
		c()
		select {
		case <-w.done:
		case <-time.After(time.Second):
			t.Fatal("runMonitor did not stop")
		}
		if w.currentBuf() != "next" || len(bus.events) == 0 {
			t.Fatalf("buf=%q events=%+v", w.currentBuf(), bus.events)
		}
	})
}

func TestWeixinPersistSyncNoDisk(t *testing.T) {
	NewWeixin(WeixinConfig{}).persistSyncFromState()
}

type weixinBus struct {
	events []Inbound
	err    error
}

func (b *weixinBus) PublishInbound(_ context.Context, in Inbound) error {
	b.events = append(b.events, in)
	return b.err
}

var _ = time.Second

// Nothing in production writes the Weixin login file — only the reader
// (LoadClawbotSession, via MergePersistedClawbot) is wired. These two lived
// in the production file with no caller; they stay here as the fixture that
// produces a session file for the live loader's tests.

func ClawbotSessionFileForDisplay(root string) string {
	return clawbotSessionPath(root)
}

func SaveClawbotSession(root string, s *ClawbotSession) error {
	if s == nil || strings.TrimSpace(s.BotToken) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(root, "state"), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(clawbotSessionPath(root), b, 0o600)
}
