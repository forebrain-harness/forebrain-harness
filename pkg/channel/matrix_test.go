package channel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/testutil"
)

type matrixBus struct {
	mu        sync.Mutex
	inbound   []Inbound
	publishFn func(context.Context, Inbound) error
}

func (b *matrixBus) PublishInbound(ctx context.Context, in Inbound) error {
	if b.publishFn != nil {
		return b.publishFn(ctx, in)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.inbound = append(b.inbound, in)
	return nil
}

func (b *matrixBus) messages() []Inbound {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Inbound, len(b.inbound))
	copy(out, b.inbound)
	return out
}

func TestMatrixNewIDRegisterAndStartValidation(t *testing.T) {
	m := NewMatrix(MatrixConfig{})
	if m.ID() != "matrix" {
		t.Fatalf("ID() = %q, want matrix", m.ID())
	}
	if m.httpClient == nil {
		t.Fatal("New did not initialize http client")
	}

	disabled := NewMatrix(MatrixConfig{Enabled: false})
	if err := disabled.Start(context.Background(), nil, &matrixBus{}); err != nil {
		t.Fatalf("disabled Start returned error: %v", err)
	}
	if err := disabled.Stop(context.Background()); err != nil {
		t.Fatalf("disabled Stop returned error: %v", err)
	}

	tests := []struct {
		name string
		cfg  MatrixConfig
	}{
		{name: "missing outbound url", cfg: MatrixConfig{Enabled: true, Token: "tok"}},
		{name: "missing token", cfg: MatrixConfig{Enabled: true, OutboundURL: "https://matrix.example"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := NewMatrix(tt.cfg).Start(context.Background(), nil, nil); err == nil {
				t.Fatal("Start returned nil error, want validation error")
			}
		})
	}
}

func TestMatrixMatrix_DeliverOutbound(t *testing.T) {
	t.Run("invalid session", func(t *testing.T) {
		m := NewMatrix(MatrixConfig{OutboundURL: "https://matrix.example", Token: "tok"})
		if err := m.DeliverOutbound(context.Background(), Outbound{SessionID: "  "}); err == nil {
			t.Fatal("DeliverOutbound returned nil error, want invalid session error")
		}
	})

	t.Run("request creation error", func(t *testing.T) {
		m := NewMatrix(MatrixConfig{OutboundURL: "://bad", Token: "tok"})
		err := m.DeliverOutbound(context.Background(), Outbound{SessionID: "!room:server", Text: "hello"})
		if err == nil {
			t.Fatal("DeliverOutbound returned nil error, want request error")
		}
	})

	t.Run("client error", func(t *testing.T) {
		m := NewMatrix(MatrixConfig{OutboundURL: "http://127.0.0.1:1", Token: "tok"})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := m.DeliverOutbound(ctx, Outbound{SessionID: "!room:server", Text: "hello"})
		if err == nil {
			t.Fatal("DeliverOutbound returned nil error, want client error")
		}
	})

	t.Run("status error", func(t *testing.T) {
		srv := testutil.NewLocalServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, strings.Repeat("x", 3000), http.StatusBadGateway)
		}))
		defer srv.Close()

		m := NewMatrix(MatrixConfig{OutboundURL: srv.URL, Token: "tok"})
		err := m.DeliverOutbound(context.Background(), Outbound{SessionID: "!room:server", Text: "hello"})
		if err == nil || !strings.Contains(err.Error(), "matrix send status=502") {
			t.Fatalf("DeliverOutbound error = %v, want status error", err)
		}
	})

	t.Run("success", func(t *testing.T) {
		var gotPath, gotAuth, gotContentType string
		var gotBody map[string]any
		srv := testutil.NewLocalServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.EscapedPath()
			gotAuth = r.Header.Get("Authorization")
			gotContentType = r.Header.Get("Content-Type")
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			if err := json.Unmarshal(raw, &gotBody); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		roomID := " !room with/slash:server "
		m := NewMatrix(MatrixConfig{OutboundURL: srv.URL + "/", Token: " tok "})
		err := m.DeliverOutbound(context.Background(), Outbound{SessionID: roomID, Text: " hi "})
		if err != nil {
			t.Fatalf("DeliverOutbound returned error: %v", err)
		}
		wantPrefix := "/_matrix/client/v3/rooms/" + url.PathEscape(strings.TrimSpace(roomID)) + "/send/m.room.message/"
		if !strings.HasPrefix(gotPath, wantPrefix) {
			t.Fatalf("request path = %q, want prefix %q", gotPath, wantPrefix)
		}
		if gotAuth != "Bearer tok" {
			t.Fatalf("Authorization = %q, want Bearer tok", gotAuth)
		}
		if gotContentType != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", gotContentType)
		}
		if gotBody["msgtype"] != "m.text" || gotBody["body"] != "hi" {
			t.Fatalf("request body = %#v, want text message body", gotBody)
		}
	})
}

func TestMatrixMatrixSyncOnce(t *testing.T) {
	t.Run("success publishes message events and updates next batch", func(t *testing.T) {
		bus := &matrixBus{}
		var gotPath, gotQuery, gotAuth string
		srv := testutil.NewLocalServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotQuery = r.URL.RawQuery
			gotAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{
				"next_batch":" nb2 ",
				"rooms":{"join":{
					"!room:server":{"timeline":{"events":[
						{"type":"m.room.message","content":{"body":" hi "}},
						{"type":"m.room.message","content":{"body":" "}},
						{"type":"m.room.topic","content":{"body":"skip"}},
						{"type":"m.room.message","content":{}}
					]}}
				}}
			}`))
		}))
		defer srv.Close()

		m := NewMatrix(MatrixConfig{OutboundURL: srv.URL, Token: " tok "})
		m.bus = bus
		m.nextBatch = " old token "
		if err := m.syncOnce(context.Background()); err != nil {
			t.Fatalf("syncOnce returned error: %v", err)
		}
		if gotPath != "/_matrix/client/v3/sync" {
			t.Fatalf("sync path = %q, want /_matrix/client/v3/sync", gotPath)
		}
		values, err := url.ParseQuery(gotQuery)
		if err != nil {
			t.Fatalf("parse sync query: %v", err)
		}
		if values.Get("timeout") != "30000" || values.Get("since") != "old token" {
			t.Fatalf("sync query = %q, want timeout and since", gotQuery)
		}
		if gotAuth != "Bearer tok" {
			t.Fatalf("Authorization = %q, want Bearer tok", gotAuth)
		}
		if m.nextBatch != "nb2" {
			t.Fatalf("nextBatch = %q, want nb2", m.nextBatch)
		}
		msgs := bus.messages()
		if len(msgs) != 1 {
			t.Fatalf("published %d messages, want 1: %#v", len(msgs), msgs)
		}
		if msgs[0].ChannelID != "matrix" || msgs[0].SessionID != "!room:server" || msgs[0].Text != "hi" || msgs[0].Raw == nil {
			t.Fatalf("published message = %#v, want matrix room text with raw event", msgs[0])
		}
	})

	t.Run("nil bus", func(t *testing.T) {
		srv := testutil.NewLocalServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"next_batch":"nb","rooms":{"join":{}}}`))
		}))
		defer srv.Close()

		m := NewMatrix(MatrixConfig{OutboundURL: srv.URL, Token: "tok"})
		if err := m.syncOnce(context.Background()); err != nil {
			t.Fatalf("syncOnce returned error: %v", err)
		}
	})

	t.Run("request creation error", func(t *testing.T) {
		m := NewMatrix(MatrixConfig{OutboundURL: "://bad", Token: "tok"})
		if err := m.syncOnce(context.Background()); err == nil {
			t.Fatal("syncOnce returned nil error, want request error")
		}
	})

	t.Run("client error", func(t *testing.T) {
		m := NewMatrix(MatrixConfig{OutboundURL: "http://127.0.0.1:1", Token: "tok"})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := m.syncOnce(ctx); err == nil {
			t.Fatal("syncOnce returned nil error, want client error")
		}
	})

	t.Run("status error", func(t *testing.T) {
		srv := testutil.NewLocalServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "bad sync", http.StatusTooManyRequests)
		}))
		defer srv.Close()

		m := NewMatrix(MatrixConfig{OutboundURL: srv.URL, Token: "tok"})
		err := m.syncOnce(context.Background())
		if err == nil || !strings.Contains(err.Error(), "matrix sync status=429") {
			t.Fatalf("syncOnce error = %v, want status error", err)
		}
	})

	t.Run("decode error", func(t *testing.T) {
		srv := testutil.NewLocalServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{`))
		}))
		defer srv.Close()

		m := NewMatrix(MatrixConfig{OutboundURL: srv.URL, Token: "tok"})
		if err := m.syncOnce(context.Background()); err == nil {
			t.Fatal("syncOnce returned nil error, want decode error")
		}
	})

	t.Run("publish error is ignored", func(t *testing.T) {
		bus := &matrixBus{publishFn: func(context.Context, Inbound) error {
			return errors.New("publish failed")
		}}
		srv := testutil.NewLocalServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{
				"rooms":{"join":{"!room:server":{"timeline":{"events":[
					{"type":"m.room.message","content":{"body":"hi"}}
				]}}}}
			}`))
		}))
		defer srv.Close()

		m := NewMatrix(MatrixConfig{OutboundURL: srv.URL, Token: "tok"})
		m.bus = bus
		if err := m.syncOnce(context.Background()); err != nil {
			t.Fatalf("syncOnce returned error: %v", err)
		}
	})
}

func TestMatrixMatrixSyncLoop(t *testing.T) {
	t.Run("returns when context already cancelled", func(t *testing.T) {
		m := NewMatrix(MatrixConfig{OutboundURL: "http://127.0.0.1:1", Token: "tok"})
		m.done = make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		m.syncLoop(ctx)
		select {
		case <-m.done:
		default:
			t.Fatal("syncLoop did not close done")
		}
	})

	t.Run("returns when cancelled during backoff", func(t *testing.T) {
		oldAfter := matrixLoopAfter
		defer func() { matrixLoopAfter = oldAfter }()
		waiting := make(chan struct{})
		release := make(chan time.Time)
		matrixLoopAfter = func(time.Duration) <-chan time.Time {
			close(waiting)
			return release
		}

		m := NewMatrix(MatrixConfig{OutboundURL: "://bad", Token: "tok"})
		m.done = make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		go m.syncLoop(ctx)
		<-waiting
		cancel()
		select {
		case <-m.done:
		case <-time.After(time.Second):
			t.Fatal("syncLoop did not stop after cancellation")
		}
	})

	t.Run("continues after backoff timer", func(t *testing.T) {
		oldAfter := matrixLoopAfter
		defer func() { matrixLoopAfter = oldAfter }()
		waiting := make(chan struct{})
		var afterCalls int
		matrixLoopAfter = func(time.Duration) <-chan time.Time {
			afterCalls++
			if afterCalls == 1 {
				ch := make(chan time.Time, 1)
				ch <- time.Now()
				return ch
			}
			close(waiting)
			return make(chan time.Time)
		}

		m := NewMatrix(MatrixConfig{OutboundURL: "://bad", Token: "tok"})
		m.done = make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		go m.syncLoop(ctx)
		<-waiting
		cancel()
		select {
		case <-m.done:
		case <-time.After(time.Second):
			t.Fatal("syncLoop did not stop after cancellation")
		}
		if afterCalls != 2 {
			t.Fatalf("matrixLoopAfter called %d times, want 2", afterCalls)
		}
	})

	t.Run("success resets backoff and stop cancels loop", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var calls int
		srv := testutil.NewLocalServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls == 2 {
				cancel()
			}
			_, _ = w.Write([]byte(`{"rooms":{"join":{}}}`))
		}))
		defer srv.Close()

		m := NewMatrix(MatrixConfig{Enabled: true, OutboundURL: srv.URL, Token: "tok"})
		if err := m.Start(ctx, nil, nil); err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
		select {
		case <-m.done:
		case <-time.After(time.Second):
			t.Fatal("syncLoop did not stop after cancellation")
		}
		if calls < 2 {
			t.Fatalf("syncLoop made %d calls, want at least 2", calls)
		}
	})

	t.Run("Stop cancels started loop", func(t *testing.T) {
		oldAfter := matrixLoopAfter
		defer func() { matrixLoopAfter = oldAfter }()
		matrixLoopAfter = func(time.Duration) <-chan time.Time {
			return make(chan time.Time)
		}

		m := NewMatrix(MatrixConfig{Enabled: true, OutboundURL: "://bad", Token: "tok"})
		if err := m.Start(context.Background(), nil, nil); err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
		if err := m.Stop(context.Background()); err != nil {
			t.Fatalf("Stop returned error: %v", err)
		}
	})
}

func TestMatrixParseMatrixSessionAndAsString(t *testing.T) {
	if got := parseMatrixSession(" !room:server "); got != "!room:server" {
		t.Fatalf("parseMatrixSession = %q, want !room:server", got)
	}
	if got := parseMatrixSession("  "); got != "" {
		t.Fatalf("parseMatrixSession blank = %q, want empty", got)
	}

	tests := []struct {
		name string
		in   any
		want string
	}{
		{name: "string", in: "hello", want: "hello"},
		{name: "float", in: float64(12.5), want: "12.5"},
		{name: "nil", in: nil, want: ""},
		{name: "map", in: map[string]any{"x": "y"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matrixString(tt.in); got != tt.want {
				t.Fatalf("matrixString(%#v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
