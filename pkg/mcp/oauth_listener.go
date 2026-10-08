package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
)

func ListenForOAuthCode(
	ctx context.Context,
	callbackPath string,
	expectedState string,
	onReady func(callbackURL string),
) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	path := strings.TrimSpace(callbackPath)
	if path == "" {
		path = "/callback"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer ln.Close()

	type result struct {
		code string
		err  error
	}
	ch := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		state := r.URL.Query().Get("state")
		switch {
		case code == "":
			http.Error(w, "authorization code not found", http.StatusBadRequest)
			ch <- result{err: fmt.Errorf("authorization code not found")}
		case state != expectedState:
			http.Error(w, "invalid state", http.StatusBadRequest)
			ch <- result{err: fmt.Errorf("invalid state")}
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("authorization received"))
			ch <- result{code: code}
		}
	})
	srv := &http.Server{
		Handler:           telemetry.AccessLogMiddleware(mux),
		ErrorLog:          telemetry.SlogErrorLog(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		_ = srv.Serve(ln)
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	callbackURL := "http://" + ln.Addr().String() + path
	if onReady != nil {
		onReady(callbackURL)
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case res := <-ch:
		return res.code, res.err
	}
}
