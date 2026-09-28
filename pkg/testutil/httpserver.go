package testutil

import (
	"net"
	"net/http"
	"net/http/httptest"
)

// NewLocalServer forces IPv4 loopback to avoid environments where httptest's
// default IPv6 listener on ::1 is unavailable.
func NewLocalServer(handler http.Handler) *httptest.Server {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	srv := &httptest.Server{
		Listener: ln,
		Config:   &http.Server{Handler: handler},
	}
	srv.Start()
	return srv
}
