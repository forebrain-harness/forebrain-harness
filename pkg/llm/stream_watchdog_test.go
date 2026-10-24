package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// watchdogTestClient talks to srv through the watchdog transport alone, the
// way a provider client does once WrapHTTPTransport installs it innermost.
func watchdogTestClient() *http.Client {
	return &http.Client{Transport: watchdogTransport{next: http.DefaultTransport}}
}

// stallServer answers nothing: it holds the request open until the client (or
// the watchdog behind it) gives up and the server context closes.
func stallServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
}

func TestWatchdogHeadersTimeout(t *testing.T) {
	srv := stallServer(t)
	defer srv.Close()

	ctx := WithStreamingRequest(WithStreamWatchdog(context.Background(), 50*time.Millisecond))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = watchdogTestClient().Do(req)
	if !errors.Is(err, ErrStreamStalled) {
		t.Fatalf("Do error = %v, want ErrStreamStalled", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("Do error = %v, must not read as a cancellation", err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("Do took %v, want the watchdog to abort well before 1s", elapsed)
	}
}

func TestWatchdogBodyIdle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: x\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx := WithStreamingRequest(WithStreamWatchdog(context.Background(), 50*time.Millisecond))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := watchdogTestClient().Do(req)
	if err != nil {
		t.Fatalf("Do: %v (the first chunk must arrive)", err)
	}
	defer resp.Body.Close()

	var got strings.Builder
	buf := make([]byte, 64)
	for {
		n, rerr := resp.Body.Read(buf)
		got.Write(buf[:n])
		if rerr != nil {
			if !errors.Is(rerr, ErrStreamStalled) {
				t.Fatalf("idle Read error = %v, want ErrStreamStalled (after %q)", rerr, got.String())
			}
			break
		}
	}
	if !strings.Contains(got.String(), "data: x") {
		t.Fatalf("body read %q, want the chunk the stream produced before going silent", got.String())
	}
}

func TestWatchdogKeepsALiveStreamAlive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 10; i++ {
			fmt.Fprintf(w, "data: %d\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer srv.Close()

	ctx := WithStreamingRequest(WithStreamWatchdog(context.Background(), 50*time.Millisecond))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := watchdogTestClient().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v, want an alive stream to read to the end", err)
	}
	if n := strings.Count(string(body), "data:"); n != 10 {
		t.Fatalf("body carries %d data lines, want all 10 (got %q)", n, body)
	}
}

func TestWatchdogParentCancelIsCancel(t *testing.T) {
	srv := stallServer(t)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	ctx = WithStreamingRequest(WithStreamWatchdog(ctx, time.Second))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = watchdogTestClient().Do(req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do error = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrStreamStalled) {
		t.Fatalf("Do error = %v, a user cancellation must not read as a stall", err)
	}
}

func TestWatchdogSkipsNonStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	ctx := WithStreamWatchdog(context.Background(), 50*time.Millisecond) // window, but no streaming mark
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := watchdogTestClient().Do(req)
	if err != nil {
		t.Fatalf("Do: %v, want a non-streaming request to pass through untouched", err)
	}
	defer resp.Body.Close()
}

func TestWatchdogDisabledWithoutWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	ctx := WithStreamingRequest(context.Background()) // streaming, but no window
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := watchdogTestClient().Do(req)
	if err != nil {
		t.Fatalf("Do: %v, want no window to mean no watchdog", err)
	}
	defer resp.Body.Close()
}
