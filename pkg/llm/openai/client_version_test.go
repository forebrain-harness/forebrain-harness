package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func stubDistTags(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/-/package/@openai/codex/dist-tags" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	previous := npmRegistryURL
	npmRegistryURL = server.URL
	t.Cleanup(func() {
		npmRegistryURL = previous
		server.Close()
	})
	return server
}

// Resolution is a live query: whatever the registry's latest tag says is what
// the next discovery declares. A pre-release suffix is stripped the way
// codex-cli strips its own.
func TestResolveClientVersionQueriesTheRegistryLive(t *testing.T) {
	stubDistTags(t, `{"latest":"0.156.1-rc.2","beta":"0.1.2505172116"}`, http.StatusOK)
	version, err := ResolveClientVersion(context.Background())
	if err != nil {
		t.Fatalf("ResolveClientVersion: %v", err)
	}
	if version != "0.156.1" {
		t.Fatalf("version = %q, want the latest tag with its pre-release stripped", version)
	}
}

func TestResolveClientVersionRejectsNonWholeVersions(t *testing.T) {
	for _, body := range []string{
		`{"latest":"not-a-version"}`,
		`{"latest":""}`,
		`{"latest":"0.156"}`,
		`{}`,
	} {
		stubDistTags(t, body, http.StatusOK)
		if _, err := ResolveClientVersion(context.Background()); err == nil {
			t.Fatalf("latest %s = nil error, want a refusal: the backend takes whole versions only", body)
		}
	}
}

func TestResolveClientVersionRejectsBrokenPayloads(t *testing.T) {
	stubDistTags(t, `{"latest":`, http.StatusOK)
	if _, err := ResolveClientVersion(context.Background()); err == nil {
		t.Fatal("broken JSON = nil error, want a decode error")
	}
}

// The registry carries no credentials and answers nothing else; a non-OK or
// oversized reply is a failure, never a guess.
func TestResolveClientVersionRejectsRegistryFailures(t *testing.T) {
	stubDistTags(t, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
	if _, err := ResolveClientVersion(context.Background()); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v, want the http status surfaced", err)
	}

	stubDistTags(t, strings.Repeat("x", maxDistTagsBytes+1), http.StatusOK)
	if _, err := ResolveClientVersion(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want a size error", err)
	}
}

func TestResolveClientVersionHonorsContextCancellation(t *testing.T) {
	stubDistTags(t, `{"latest":"0.156.1"}`, http.StatusOK)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ResolveClientVersion(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// Two calls must re-query rather than serve the first answer: a tag that
// moves between calls has to be visible to the second one.
func TestResolveClientVersionQueriesEveryCall(t *testing.T) {
	body := `{"latest":"0.156.1"}`
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits > 1 {
			body = `{"latest":"0.157.0"}`
		}
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	previous := npmRegistryURL
	npmRegistryURL = server.URL
	t.Cleanup(func() { npmRegistryURL = previous })

	first, err := ResolveClientVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResolveClientVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first != "0.156.1" || second != "0.157.0" {
		t.Fatalf("versions = %q then %q, want the second call to see the moved tag", first, second)
	}
	if hits != 2 {
		t.Fatalf("registry hits = %d, want one query per resolution", hits)
	}
}
