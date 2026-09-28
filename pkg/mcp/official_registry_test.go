package mcp

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/testutil"
)

func TestOfficialRegistryNormalizesURLs(t *testing.T) {
	reg, err := NewOfficialRegistryFromJSON([]byte(`{
		"servers": [
			{"server": {"remotes": [{"url": "https://example.com/mcp/?token=secret"}]}}
		]
	}`))
	if err != nil {
		t.Fatalf("NewOfficialRegistryFromJSON error: %v", err)
	}
	if !reg.IsOfficialURL("https://example.com/mcp") {
		t.Fatalf("expected normalized url to be official")
	}
	if reg.IsOfficialURL("https://evil.example/mcp") {
		t.Fatalf("unexpected official url")
	}
}

func TestLoadOfficialRegistryFetchesAndCachesRemote(t *testing.T) {
	home := t.TempDir()
	srv := testutil.NewLocalServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"servers": [
				{"server": {"remotes": [{"url": "https://example.com/mcp?token=secret"}]}}
			]
		}`))
	}))
	defer srv.Close()

	reg, configured, err := LoadOfficialRegistry(context.Background(), OfficialRegistryOptions{
		Home: home,
		URL:  srv.URL,
	})
	if err != nil {
		t.Fatalf("LoadOfficialRegistry error: %v", err)
	}
	if !configured {
		t.Fatalf("expected registry to be configured")
	}
	if !reg.IsOfficialURL("https://example.com/mcp") {
		t.Fatalf("expected remote registry url to match")
	}
	if _, err := os.Stat(filepath.Join(home, "state", "mcp-registry", "official.json")); err != nil {
		t.Fatalf("expected registry cache: %v", err)
	}
}

func TestLoadOfficialRegistryFallsBackToCacheWhenRemoteFails(t *testing.T) {
	home := t.TempDir()
	cacheDir := filepath.Join(home, "state", "mcp-registry")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "official.json"), []byte(`{
		"servers": [
			{"server": {"remotes": [{"url": "https://cached.example/mcp"}]}}
		]
	}`), 0o600); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	reg, configured, err := LoadOfficialRegistry(context.Background(), OfficialRegistryOptions{
		Home: home,
		URL:  "http://127.0.0.1:1/unreachable",
	})
	if err != nil {
		t.Fatalf("LoadOfficialRegistry fallback error: %v", err)
	}
	if !configured {
		t.Fatalf("expected registry to be configured")
	}
	if !reg.IsOfficialURL("https://cached.example/mcp") {
		t.Fatalf("expected cached registry url to match")
	}
}

func TestLoadOfficialRegistryRespectsNonessentialTrafficDisable(t *testing.T) {
	home := t.TempDir()
	called := false
	srv := testutil.NewLocalServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	reg, configured, err := LoadOfficialRegistry(context.Background(), OfficialRegistryOptions{
		Home:                       home,
		URL:                        srv.URL,
		DisableNonessentialTraffic: true,
	})
	if err != nil {
		t.Fatalf("LoadOfficialRegistry error: %v", err)
	}
	if configured || reg != nil {
		t.Fatalf("disabled registry should be unconfigured")
	}
	if called {
		t.Fatalf("disabled registry must not fetch remote")
	}
}
