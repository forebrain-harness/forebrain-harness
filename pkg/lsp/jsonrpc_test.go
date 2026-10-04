package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"
)

func TestReadMessageFraming(t *testing.T) {
	body := `{"jsonrpc":"2.0","result":42}`
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)

	cases := []struct {
		name  string
		frame string
	}{
		{"well formed", frame},
		{"lowercase header name", fmt.Sprintf("content-length: %d\r\n\r\n%s", len(body), body)},
		{"extra headers", fmt.Sprintf("Content-Type: application/vscode-jsonrpc; charset=utf-8\r\nContent-Length: %d\r\nX-Ignored: whatever\r\n\r\n%s", len(body), body)},
		{"bare newline line endings", fmt.Sprintf("Content-Length: %d\n\n%s", len(body), body)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadMessage(bufio.NewReader(strings.NewReader(tc.frame)))
			if err != nil {
				t.Fatalf("ReadMessage: %v", err)
			}
			if string(got) != body {
				t.Fatalf("body = %q, want %q", got, body)
			}
		})
	}

	t.Run("one byte at a time", func(t *testing.T) {
		got, err := ReadMessage(bufio.NewReader(iotest.OneByteReader(strings.NewReader(frame))))
		if err != nil {
			t.Fatalf("ReadMessage: %v", err)
		}
		if string(got) != body {
			t.Fatalf("body = %q, want %q", got, body)
		}
	})

	t.Run("WriteMessage frames what ReadMessage reads", func(t *testing.T) {
		var buf strings.Builder
		if err := WriteMessage(&buf, []byte(body)); err != nil {
			t.Fatalf("WriteMessage: %v", err)
		}
		if buf.String() != frame {
			t.Fatalf("frame = %q, want %q", buf.String(), frame)
		}
	})

	t.Run("missing content length", func(t *testing.T) {
		if _, err := ReadMessage(bufio.NewReader(strings.NewReader("Content-Type: x\r\n\r\n{}"))); err == nil {
			t.Fatal("want an error when Content-Length is missing")
		}
	})

	t.Run("non numeric content length", func(t *testing.T) {
		if _, err := ReadMessage(bufio.NewReader(strings.NewReader("Content-Length: twelve\r\n\r\n{}"))); err == nil {
			t.Fatal("want an error for a non-numeric Content-Length")
		}
	})

	t.Run("message too large", func(t *testing.T) {
		// Only the header is sent: the size check fires before any allocation.
		oversized := fmt.Sprintf("Content-Length: %d\r\n\r\n", MaxMessageBytes+1)
		if _, err := ReadMessage(bufio.NewReader(strings.NewReader(oversized))); !errors.Is(err, ErrMessageTooLarge) {
			t.Fatalf("err = %v, want ErrMessageTooLarge", err)
		}
	})
}

// connPair builds two Conns over in-memory pipes: the first plays the client,
// the second the server with the given options.
func connPair(t *testing.T, serverOpts ConnOptions) (client, server *Conn) {
	t.Helper()
	clientRead, clientWrite := io.Pipe() // the server writes here
	serverRead, serverWrite := io.Pipe() // the client writes here
	client = NewConn(clientRead, serverWrite, ConnOptions{})
	server = NewConn(serverRead, clientWrite, serverOpts)
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	return client, server
}

// echoHandler answers every request with its own params.
func echoHandler(ctx context.Context, method string, params json.RawMessage) (any, error) {
	return params, nil
}

func TestConnCallAndNotify(t *testing.T) {
	const notes = 100
	notified := make(chan int, notes)
	client, _ := connPair(t, ConnOptions{
		OnRequest: echoHandler,
		OnNotify: func(method string, params json.RawMessage) {
			var p struct {
				I int `json:"i"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				t.Errorf("decoding notification params %s: %v", params, err)
				return
			}
			notified <- p.I
		},
	})

	var out struct {
		N int `json:"n"`
	}
	if err := client.Call(context.Background(), "test/echo", map[string]any{"n": 7}, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.N != 7 {
		t.Fatalf("Call decoded %d, want 7", out.N)
	}

	for i := 0; i < notes; i++ {
		if err := client.Notify("test/note", map[string]any{"i": i}); err != nil {
			t.Fatalf("Notify %d: %v", i, err)
		}
	}
	// The reply can only arrive after the read loop delivered every earlier
	// notification, so this Call is the barrier.
	if err := client.Call(context.Background(), "test/echo", nil, nil); err != nil {
		t.Fatalf("barrier Call: %v", err)
	}
	for i := 0; i < notes; i++ {
		select {
		case got := <-notified:
			if got != i {
				t.Fatalf("notification %d arrived as %d", i, got)
			}
		default:
			t.Fatalf("notification %d missing after the barrier call", i)
		}
	}
}

func TestConnConcurrentCalls(t *testing.T) {
	client, _ := connPair(t, ConnOptions{OnRequest: echoHandler})

	const calls = 50
	var wg sync.WaitGroup
	errs := make(chan error, calls)
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var out struct {
				N int `json:"n"`
			}
			if err := client.Call(context.Background(), "test/echo", map[string]any{"n": i}, &out); err != nil {
				errs <- fmt.Errorf("call %d: %w", i, err)
				return
			}
			if out.N != i {
				errs <- fmt.Errorf("call %d got result %d", i, out.N)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestConnCancelSendsCancelRequest(t *testing.T) {
	blocked := make(chan struct{})
	release := make(chan struct{})
	var cancelParams json.RawMessage
	cancelSeen := make(chan struct{}, 1)
	client, _ := connPair(t, ConnOptions{
		OnRequest: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			if method != "test/block" {
				return params, nil
			}
			close(blocked)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil, nil
		},
		OnNotify: func(method string, params json.RawMessage) {
			if method != "$/cancelRequest" {
				return
			}
			cancelParams = params
			select {
			case cancelSeen <- struct{}{}:
			default:
			}
		},
	})

	// The warm-up takes id 0, so the blocked call below is id 1.
	if err := client.Call(context.Background(), "test/echo", map[string]any{"n": 1}, nil); err != nil {
		t.Fatalf("warm-up Call: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	callErr := make(chan error, 1)
	go func() {
		callErr <- client.Call(ctx, "test/block", nil, nil)
	}()
	<-blocked // the request reached the server, so the call is pending
	cancel()
	if err := <-callErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Call error = %v, want context.Canceled", err)
	}

	select {
	case <-cancelSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("the server never received $/cancelRequest")
	}
	var got struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(cancelParams, &got); err != nil {
		t.Fatalf("decoding cancel params %s: %v", cancelParams, err)
	}
	if got.ID != 1 {
		t.Fatalf("cancel id = %d, want 1", got.ID)
	}

	// The late reply to the cancelled request is dropped and the connection
	// keeps working.
	close(release)
	var out struct {
		N int `json:"n"`
	}
	if err := client.Call(context.Background(), "test/echo", map[string]any{"n": 2}, &out); err != nil {
		t.Fatalf("Call after cancel: %v", err)
	}
	if out.N != 2 {
		t.Fatalf("Call after cancel decoded %d, want 2", out.N)
	}
}

// rawConnPair wires one Conn to raw pipes the test drives itself, so it can
// inspect the exact frames the conn writes.
func rawConnPair(t *testing.T, handler RequestHandler) (requests *io.PipeWriter, responses *bufio.Reader) {
	t.Helper()
	requestsReader, requestsWriter := io.Pipe()
	responsesReader, responsesWriter := io.Pipe()
	NewConn(requestsReader, responsesWriter, ConnOptions{OnRequest: handler})
	t.Cleanup(func() {
		requestsWriter.Close()
		responsesReader.Close()
	})
	return requestsWriter, bufio.NewReader(responsesReader)
}

func writeRaw(t *testing.T, w io.Writer, body string) {
	t.Helper()
	if err := WriteMessage(w, []byte(body)); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
}

func readRaw(t *testing.T, r *bufio.Reader) map[string]json.RawMessage {
	t.Helper()
	body, err := ReadMessage(r)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decoding response %s: %v", body, err)
	}
	return m
}

func TestServerRequestErrors(t *testing.T) {
	cases := []struct {
		name    string
		handler RequestHandler
		code    int64
		message string
	}{
		{
			name:    "method not found",
			handler: func(context.Context, string, json.RawMessage) (any, error) { return nil, ErrMethodNotFound },
			code:    CodeMethodNotFound,
			message: "method not found",
		},
		{
			name: "rpc error passes through",
			handler: func(context.Context, string, json.RawMessage) (any, error) {
				return nil, &RPCError{Code: 1, Message: "custom"}
			},
			code:    1,
			message: "custom",
		},
		{
			name: "plain error",
			handler: func(context.Context, string, json.RawMessage) (any, error) {
				return nil, errors.New("boom")
			},
			code:    CodeInternalError,
			message: "boom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests, responses := rawConnPair(t, tc.handler)
			// A string id must be echoed verbatim.
			writeRaw(t, requests, `{"jsonrpc":"2.0","id":"s-1","method":"test/ask","params":{}}`)
			resp := readRaw(t, responses)
			var id string
			if err := json.Unmarshal(resp["id"], &id); err != nil || id != "s-1" {
				t.Fatalf("response id = %s, want the string s-1", resp["id"])
			}
			var rpcErr struct {
				Code    int64  `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(resp["error"], &rpcErr); err != nil {
				t.Fatalf("decoding error %s: %v", resp["error"], err)
			}
			if rpcErr.Code != tc.code || rpcErr.Message != tc.message {
				t.Fatalf("error = %d %q, want %d %q", rpcErr.Code, rpcErr.Message, tc.code, tc.message)
			}
		})
	}

	t.Run("nil handler answers method not found", func(t *testing.T) {
		requests, responses := rawConnPair(t, nil)
		writeRaw(t, requests, `{"jsonrpc":"2.0","id":7,"method":"test/ask"}`)
		resp := readRaw(t, responses)
		var rpcErr struct {
			Code int64 `json:"code"`
		}
		if err := json.Unmarshal(resp["error"], &rpcErr); err != nil {
			t.Fatalf("decoding error %s: %v", resp["error"], err)
		}
		if rpcErr.Code != CodeMethodNotFound {
			t.Fatalf("code = %d, want %d", rpcErr.Code, CodeMethodNotFound)
		}
	})

	t.Run("handler panic keeps the connection usable", func(t *testing.T) {
		var calls atomic.Int32
		requests, responses := rawConnPair(t, func(context.Context, string, json.RawMessage) (any, error) {
			if calls.Add(1) == 1 {
				panic("kaboom")
			}
			return "still alive", nil
		})
		writeRaw(t, requests, `{"jsonrpc":"2.0","id":1,"method":"test/ask"}`)
		resp := readRaw(t, responses)
		var rpcErr struct {
			Code    int64  `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(resp["error"], &rpcErr); err != nil {
			t.Fatalf("decoding error %s: %v", resp["error"], err)
		}
		if rpcErr.Code != CodeInternalError || !strings.Contains(rpcErr.Message, "kaboom") {
			t.Fatalf("error = %d %q, want %d containing kaboom", rpcErr.Code, rpcErr.Message, CodeInternalError)
		}

		writeRaw(t, requests, `{"jsonrpc":"2.0","id":2,"method":"test/ask"}`)
		resp = readRaw(t, responses)
		if _, hasError := resp["error"]; hasError {
			t.Fatalf("second request failed: %s", resp["error"])
		}
		var result string
		if err := json.Unmarshal(resp["result"], &result); err != nil || result != "still alive" {
			t.Fatalf("result = %s, want \"still alive\"", resp["result"])
		}
		if string(resp["id"]) != "2" {
			t.Fatalf("response id = %s, want 2 verbatim", resp["id"])
		}
	})
}

func TestConnCloseFailsPendingCalls(t *testing.T) {
	entered := make(chan struct{})
	client, _ := connPair(t, ConnOptions{
		OnRequest: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			close(entered)
			<-ctx.Done() // never answer; ends when cleanup closes the server conn
			return nil, nil
		},
	})

	callErr := make(chan error, 1)
	go func() {
		callErr <- client.Call(context.Background(), "test/hang", nil, nil)
	}()
	<-entered // the request reached the server, so the call is pending

	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-callErr:
		if !errors.Is(err, ErrConnClosed) {
			t.Fatalf("pending Call error = %v, want ErrConnClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the pending Call did not return after Close")
	}
	select {
	case <-client.Done():
	default:
		t.Fatal("Done is not closed after Close")
	}
	if err := client.Err(); err != nil {
		t.Fatalf("Err after Close = %v, want nil", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
