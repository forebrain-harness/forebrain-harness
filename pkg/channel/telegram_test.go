package channel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/testutil"
)

func TestTelegramNewIDStartStopAndRegister(t *testing.T) {
	tg := NewTelegram(TelegramConfig{})
	if tg == nil || tg.ID() != "telegram" || tg.httpClient == nil || tg.httpClient.Timeout == 0 {
		t.Fatalf("telegram=%#v", tg)
	}

	if err := NewTelegram(TelegramConfig{}).Start(context.Background(), nil, nil); err != nil {
		t.Fatalf("disabled Start error: %v", err)
	}
	if err := NewTelegram(TelegramConfig{Enabled: true}).Start(context.Background(), nil, nil); err == nil {
		t.Fatal("expected missing token error")
	}

}

func TestStartStopPollLoop(t *testing.T) {
	orig := telegramAPIBase
	t.Cleanup(func() { telegramAPIBase = orig })
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer srv.Close()
	telegramAPIBase = srv.URL + "/bot"

	tg := NewTelegram(TelegramConfig{Enabled: true, BotToken: "token"})
	if err := tg.Start(context.Background(), nil, &telegramBus{}); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if tg.cancel == nil || tg.done == nil {
		t.Fatalf("cancel/done not set")
	}
	if err := tg.Stop(context.Background()); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
	if err := tg.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop error: %v", err)
	}
}

func TestTelegramDeliverOutbound(t *testing.T) {
	if err := NewTelegram(TelegramConfig{}).DeliverOutbound(context.Background(), Outbound{SessionID: " "}); err == nil {
		t.Fatal("expected invalid session error")
	}

	orig := telegramAPIBase
	t.Cleanup(func() { telegramAPIBase = orig })
	var gotPath string
	var gotBody map[string]any
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content-type=%q", r.Header.Get("Content-Type"))
		}
		_, _ = rw.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	telegramAPIBase = srv.URL + "/bot"

	err := NewTelegram(TelegramConfig{BotToken: "token"}).DeliverOutbound(context.Background(), Outbound{SessionID: " 123:456 ", Text: " hi "})
	if err != nil {
		t.Fatalf("DeliverOutbound error: %v", err)
	}
	if gotPath != "/bottoken/sendMessage" || gotBody["chat_id"] != "123" || gotBody["text"] != "hi" || gotBody["reply_to_message_id"].(float64) != 456 {
		t.Fatalf("path=%q body=%v", gotPath, gotBody)
	}
}

func TestPollOnce(t *testing.T) {
	orig := telegramAPIBase
	t.Cleanup(func() { telegramAPIBase = orig })
	var gotBody map[string]any
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_, _ = rw.Write([]byte(`{"ok":true,"result":[
			{"update_id":10},
			{"update_id":11,"message":{"message_id":7,"text":" hello ","chat":{"id":123},"from":{"id":9}}},
			{"update_id":12,"message":{"message_id":8,"text":"   ","chat":{"id":124},"from":{"id":9}}}
		]}`))
	}))
	defer srv.Close()
	telegramAPIBase = srv.URL + "/bot"

	bus := &telegramBus{err: errors.New("ignored")}
	tg := NewTelegram(TelegramConfig{BotToken: "token"})
	tg.bus = bus
	tg.offset = 5
	if err := tg.pollOnce(context.Background()); err != nil {
		t.Fatalf("pollOnce error: %v", err)
	}
	if gotBody["offset"].(float64) != 5 || gotBody["timeout"].(float64) != 25 {
		t.Fatalf("request body=%v", gotBody)
	}
	if tg.offset != 13 {
		t.Fatalf("offset=%d", tg.offset)
	}
	if len(bus.events) != 1 || bus.events[0].SessionID != "123:7" || bus.events[0].Text != "hello" {
		t.Fatalf("events=%+v", bus.events)
	}

	tg.bus = nil
	if err := tg.pollOnce(context.Background()); err != nil {
		t.Fatalf("pollOnce nil bus error: %v", err)
	}
}

func TestCallTelegramAPIErrors(t *testing.T) {
	orig := telegramAPIBase
	t.Cleanup(func() { telegramAPIBase = orig })

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	telegramAPIBase = "http://127.0.0.1:1/bot"
	if err := NewTelegram(TelegramConfig{BotToken: "token"}).callTelegramAPI(cancelCtx, "method", map[string]any{}, nil); err == nil {
		t.Fatal("expected client error")
	}
	if err := NewTelegram(TelegramConfig{BotToken: "token"}).callTelegramAPI(context.Background(), "bad\nmethod", map[string]any{}, nil); err == nil {
		t.Fatal("expected request error")
	}
	if err := NewTelegram(TelegramConfig{BotToken: "token"}).callTelegramAPI(context.Background(), "method", make(chan int), nil); err == nil {
		t.Fatal("expected marshal error")
	}

	statusSrv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, strings.Repeat("x", 3000), http.StatusBadGateway)
	}))
	defer statusSrv.Close()
	telegramAPIBase = statusSrv.URL + "/bot"
	err := NewTelegram(TelegramConfig{BotToken: "token"}).callTelegramAPI(context.Background(), "method", map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "telegram api status=502") || len(err.Error()) > 2200 {
		t.Fatalf("status err=%v", err)
	}

	decodeSrv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(rw, `{`)
	}))
	defer decodeSrv.Close()
	telegramAPIBase = decodeSrv.URL + "/bot"
	var out map[string]any
	if err := NewTelegram(TelegramConfig{BotToken: "token"}).callTelegramAPI(context.Background(), "method", map[string]any{}, &out); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestPollOnceAPIErrors(t *testing.T) {
	orig := telegramAPIBase
	t.Cleanup(func() { telegramAPIBase = orig })
	telegramAPIBase = "http://127.0.0.1:1/bot"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewTelegram(TelegramConfig{BotToken: "token"}).pollOnce(ctx); err == nil {
		t.Fatal("expected pollOnce api error")
	}
}

func TestParseTelegramSession(t *testing.T) {
	tests := []struct {
		in        string
		wantChat  string
		wantReply int64
	}{
		{in: " ", wantChat: "", wantReply: 0},
		{in: "123", wantChat: "123", wantReply: 0},
		{in: " 123 : 456 ", wantChat: "123", wantReply: 456},
		{in: "123:bad", wantChat: "123", wantReply: 0},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			chat, reply := parseTelegramSession(tt.in)
			if chat != tt.wantChat || reply != tt.wantReply {
				t.Fatalf("chat=%q reply=%d", chat, reply)
			}
		})
	}
}

func TestPollLoopBackoffAndCancel(t *testing.T) {
	origBase := telegramAPIBase
	origAfter := pollAfter
	t.Cleanup(func() {
		telegramAPIBase = origBase
		pollAfter = origAfter
	})

	var calls atomic.Int64
	// The second half of this test swaps the server's behaviour mid-run. That
	// swap has to go through an atomic indirection installed before the server
	// starts: assigning srv.Config.Handler on a serving httptest.Server races
	// every in-flight connection goroutine reading it, which made this test
	// fail under -race roughly one run in six.
	var handler atomic.Value
	handler.Store(http.Handler(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			http.Error(rw, "nope", http.StatusBadGateway)
			return
		}
		_, _ = rw.Write([]byte(`{"ok":true,"result":[]}`))
	})))
	srv := testutil.NewLocalServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		handler.Load().(http.Handler).ServeHTTP(rw, r)
	}))
	defer srv.Close()
	telegramAPIBase = srv.URL + "/bot"
	var afterCalls atomic.Int64
	pollAfter = func(time.Duration) <-chan time.Time {
		afterCalls.Add(1)
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}

	tg := NewTelegram(TelegramConfig{BotToken: "token"})
	tg.done = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go tg.pollLoop(ctx)
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if calls.Load() >= 3 && afterCalls.Load() >= 2 {
			cancel()
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-tg.done:
	case <-time.After(time.Second):
		t.Fatal("pollLoop did not close done")
	}
	if calls.Load() < 3 || afterCalls.Load() < 2 {
		t.Fatalf("calls=%d afterCalls=%d", calls.Load(), afterCalls.Load())
	}

	tg = NewTelegram(TelegramConfig{})
	tg.done = make(chan struct{})
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	tg.pollLoop(ctx)
	select {
	case <-tg.done:
	default:
		t.Fatal("cancelled pollLoop did not close done")
	}

	calls.Store(0)
	handler.Store(http.Handler(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(rw, "nope", http.StatusBadGateway)
	})))
	block := make(chan time.Time)
	pollAfter = func(time.Duration) <-chan time.Time { return block }
	tg = NewTelegram(TelegramConfig{BotToken: "token"})
	tg.done = make(chan struct{})
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	go tg.pollLoop(ctx)
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if calls.Load() > 0 {
			cancel()
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-tg.done:
	case <-time.After(time.Second):
		t.Fatal("pollLoop did not exit after cancel during backoff")
	}
}

type telegramBus struct {
	events []Inbound
	err    error
}

func (t *telegramBus) PublishInbound(_ context.Context, m Inbound) error {
	t.events = append(t.events, m)
	return t.err
}
