package gateway

import (
	"bytes"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestRestServerRoutesSorted(t *testing.T) {
	srv := NewRestServer("127.0.0.1:0")
	noop := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	srv.Post("/b", noop)
	srv.Get("/a", noop)
	srv.Get("/b", noop)

	got := srv.Routes()
	want := []RouteInfo{
		{Method: "GET", Path: "/a"},
		{Method: "GET", Path: "/b"},
		{Method: "POST", Path: "/b"},
	}
	if len(got) != len(want) {
		t.Fatalf("routes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("routes[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestRestServerRunReportsBusyPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer ln.Close()

	srv := NewRestServer(ln.Addr().String())
	err = srv.Run(nil)
	if err == nil {
		t.Fatal("Run on a busy port must return an error")
	}
	if !strings.Contains(err.Error(), "gateway listen on") {
		t.Fatalf("error = %q, want it to name the listen failure", err)
	}
}

func bannerAddr(t *testing.T, s string) net.Addr {
	t.Helper()
	addr, err := net.ResolveTCPAddr("tcp", s)
	if err != nil {
		t.Fatalf("resolve %s: %v", s, err)
	}
	return addr
}

func TestWriteStartupBannerGolden(t *testing.T) {
	var buf bytes.Buffer
	writeStartupBanner(&buf, startupBanner{
		Version:       "v0.3.0",
		Routes:        []RouteInfo{{Method: "GET", Path: "/api/agents/primary"}, {Method: "POST", Path: "/api/auth/session"}},
		ChannelAgent:  "main",
		ChannelRoutes: []string{"POST /channels/telegram/webhook"},
		Addr:          bannerAddr(t, "127.0.0.1:6060"),
		AuthMode:      "token",
		SignInToken:   "tok",
		WebUI:         true,
	})

	want := bannerArt +
		"Forebrain Harness Gateway v0.3.0\n" +
		"\n" +
		"Routes (3)\n" +
		"  GET     /api/agents/primary\n" +
		"  POST    /api/auth/session\n" +
		"  WS      /ws/chat\n" +
		"Channel routes · main (1)\n" +
		"  POST    /channels/telegram/webhook\n" +
		"Web UI       http://127.0.0.1:6060/\n" +
		"Sign in      http://127.0.0.1:6060/login#token=tok\n" +
		"Auth         token\n" +
		"Listening on http://127.0.0.1:6060\n"
	if buf.String() != want {
		t.Fatalf("banner mismatch:\n--- got ---\n%s\n--- want ---\n%s", buf.String(), want)
	}
}

// A configured token is free text; the link must carry it so the sign-in
// page's decodeURIComponent (which, like PathUnescape, decodes only %XX)
// reads back exactly the token, with nothing cutting the fragment short.
func TestSignInFragmentValueRoundTripsFreeTextTokens(t *testing.T) {
	for _, token := range []string{"tok", "a&b#c", "100% sure", "x+y=z", "日本"} {
		encoded := signInFragmentValue(token)
		if strings.ContainsAny(encoded, "&# +") {
			t.Fatalf("encoded %q = %q still carries a fragment delimiter", token, encoded)
		}
		decoded, err := url.PathUnescape(encoded)
		if err != nil || decoded != token {
			t.Fatalf("round trip %q -> %q -> %q (%v)", token, encoded, decoded, err)
		}
	}
}

func TestWriteStartupBannerOmitsSignInWithoutToken(t *testing.T) {
	var buf bytes.Buffer
	writeStartupBanner(&buf, startupBanner{
		Version:  "v0.3.0",
		Routes:   []RouteInfo{{Method: "GET", Path: "/healthz"}},
		Addr:     bannerAddr(t, "127.0.0.1:6060"),
		AuthMode: "none",
	})
	if strings.Contains(buf.String(), "Sign in") {
		t.Fatalf("banner must omit the sign-in line without a token:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "Web UI") {
		t.Fatalf("banner must omit the Web UI line when no UI is served:\n%s", buf.String())
	}
}

func TestWriteStartupBannerUnspecifiedHost(t *testing.T) {
	var buf bytes.Buffer
	writeStartupBanner(&buf, startupBanner{
		Version:  "v0.3.0",
		Routes:   []RouteInfo{{Method: "GET", Path: "/healthz"}},
		Addr:     bannerAddr(t, "0.0.0.0:6060"),
		AuthMode: "token",
		WebUI:    true,
	})
	if !strings.Contains(buf.String(), "Web UI       http://127.0.0.1:6060/") {
		t.Fatalf("clickable URLs must use 127.0.0.1 for an unspecified bind:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "Listening on http://0.0.0.0:6060") {
		t.Fatalf("the listening line must name the actual bind address:\n%s", buf.String())
	}
}

func TestWriteStartupBannerNoChannels(t *testing.T) {
	var buf bytes.Buffer
	writeStartupBanner(&buf, startupBanner{
		Version:  "v0.3.0",
		Routes:   []RouteInfo{{Method: "GET", Path: "/healthz"}},
		Addr:     bannerAddr(t, "127.0.0.1:6060"),
		AuthMode: "token",
	})
	if strings.Contains(buf.String(), "Channel routes") {
		t.Fatalf("banner must omit the channel section when no channels are mounted:\n%s", buf.String())
	}
}

func TestWriteStartupBannerNoVersion(t *testing.T) {
	var buf bytes.Buffer
	writeStartupBanner(&buf, startupBanner{
		Routes:   []RouteInfo{{Method: "GET", Path: "/healthz"}},
		Addr:     bannerAddr(t, "127.0.0.1:6060"),
		AuthMode: "token",
	})
	if !strings.Contains(buf.String(), "Forebrain Harness Gateway\n") {
		t.Fatalf("version-less banner must still name the product:\n%s", buf.String())
	}
}

func TestFS(t *testing.T) {
	fsys, ok := FS()
	if fsys == nil {
		t.Fatal("FS returned nil filesystem")
	}
	if !ok {
		t.Skip("frontend not built (placeholder only); skipping embedded asset checks")
	}
	// When the frontend is built, index.html and the assets dir must be present.
	if _, err := fs.Stat(fsys, "index.html"); err != nil {
		t.Fatalf("index.html missing from embedded FS: %v", err)
	}
	entries, err := fs.ReadDir(fsys, "assets")
	if err != nil {
		t.Fatalf("assets dir missing from embedded FS: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("embedded assets dir is empty")
	}
}
