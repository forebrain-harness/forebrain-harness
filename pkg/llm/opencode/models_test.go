package opencode

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// redirectTransport sends a request whose URL is fixed at production to a
// httptest server, so the test exercises the real endpoint path and the real
// HTTP semantics of the response.
type redirectTransport struct {
	target *url.URL
	last   *http.Request
}

func (r *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.last = req
	clone := req.Clone(req.Context())
	u := *req.URL
	u.Scheme = r.target.Scheme
	u.Host = r.target.Host
	clone.URL = &u
	clone.Host = ""
	clone.RequestURI = ""
	return http.DefaultTransport.RoundTrip(clone)
}

func redirectTo(t *testing.T, serverURL string) *redirectTransport {
	t.Helper()
	target, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	return &redirectTransport{target: target}
}

// testConsoleKey stands in for a Console API key: every catalog read carries a
// real credential.
const testConsoleKey = "sk-console-test"

func TestFetchModelsConsoleCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference/v1/models" {
			t.Errorf("path = %q, want the Console catalog", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testConsoleKey {
			t.Errorf("Authorization = %q, want the caller's token", got)
		}
		if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "forebrain-harness/") {
			t.Errorf("User-Agent = %q", got)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"id": "big-pickle", "object": "model", "owned_by": "opencode"},
				{"id": "glm-5.3", "object": "model", "owned_by": "opencode"},
			},
		})
	}))
	defer server.Close()

	transport := redirectTo(t, server.URL)
	models, err := FetchModels(context.Background(), transport, false, testConsoleKey)
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if want := InferenceBase + "/v1/models"; transport.last.URL.String() != want {
		t.Fatalf("request URL = %q, want %q", transport.last.URL.String(), want)
	}
	if len(models) != 2 || models[0].ID != "big-pickle" || models[1].ID != "glm-5.3" {
		t.Fatalf("models = %+v", models)
	}
}

func TestFetchModelsGoCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference/go/v1/models" {
			t.Errorf("path = %q, want the subscription catalog", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer subscription-token" {
			t.Errorf("Authorization = %q, want the subscription token", got)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"object": "list",
			"data":   []map[string]any{{"id": "glm-5.3"}},
		})
	}))
	defer server.Close()

	transport := redirectTo(t, server.URL)
	if _, err := FetchModels(context.Background(), transport, true, "subscription-token"); err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if want := GoInferenceBase + "/v1/models"; transport.last.URL.String() != want {
		t.Fatalf("request URL = %q, want %q", transport.last.URL.String(), want)
	}
}

// The gateway answers in two error shapes: its own envelope and the upstream
// one it passes through. Both have to produce the same actionable error, since a
// caller decides from the status whether to log in again, back off, or give up.
func TestFetchModelsErrorBodies(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantText   string
	}{
		{
			name:       "gateway envelope",
			status:     http.StatusUnauthorized,
			body:       `{"type":"error","error":{"type":"AuthError","message":"Invalid API key"}}`,
			wantStatus: http.StatusUnauthorized,
			wantText:   "Invalid API key",
		},
		{
			name:       "upstream passthrough",
			status:     http.StatusTooManyRequests,
			body:       `{"error":{"code":429,"status":"UNKNOWN","message":"rate limited"}}`,
			wantStatus: http.StatusTooManyRequests,
			wantText:   "rate limited",
		},
		{
			name:       "plain body",
			status:     http.StatusBadGateway,
			body:       "upstream unavailable",
			wantStatus: http.StatusBadGateway,
			wantText:   "upstream unavailable",
		},
	}
	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		transport := redirectTo(t, server.URL)
		_, err := FetchModels(context.Background(), transport, false, testConsoleKey)
		server.Close()
		if err == nil {
			t.Fatalf("%s: FetchModels succeeded", tc.name)
		}
		var modelsErr *ModelsError
		if !errors.As(err, &modelsErr) {
			t.Fatalf("%s: error = %v, want a *ModelsError", tc.name, err)
		}
		if modelsErr.StatusCode != tc.wantStatus {
			t.Errorf("%s: status = %d, want %d", tc.name, modelsErr.StatusCode, tc.wantStatus)
		}
		if !strings.Contains(modelsErr.Message, tc.wantText) {
			t.Errorf("%s: message = %q, want it to contain %q", tc.name, modelsErr.Message, tc.wantText)
		}
	}
}

func TestFetchModelsRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[`))
		_, _ = w.Write(make([]byte, maxModelsBytes+1))
	}))
	defer server.Close()

	transport := redirectTo(t, server.URL)
	_, err := FetchModels(context.Background(), transport, false, testConsoleKey)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want an over-limit rejection", err)
	}
}

func TestFetchModelsRequiresTheDataField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{"object": "list"})
	}))
	defer server.Close()

	transport := redirectTo(t, server.URL)
	if _, err := FetchModels(context.Background(), transport, false, testConsoleKey); err == nil {
		t.Fatal("FetchModels accepted a response with no data field")
	}
}
