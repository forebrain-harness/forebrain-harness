package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type stubTransport struct {
	status   int
	body     string
	headers  http.Header
	requests []*http.Request
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// A RoundTripper honors the request context the way the real transport
	// does; without this the stub would answer even a canceled request.
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	s.requests = append(s.requests, req)
	return &http.Response{
		StatusCode: s.status,
		Header:     s.headers,
		Body:       newStringBody(s.body),
		Request:    req,
	}, nil
}

func newStringBody(s string) stubBody { return stubBody{strings.NewReader(s)} }

type stubBody struct{ r *strings.Reader }

func (b stubBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b stubBody) Close() error               { return nil }

// The request shape is contract: a fixed path, the caller-resolved
// client_version sent verbatim, and no body. The backend rejects a missing
// client_version with a 400.
func TestFetchModelsSendsTheVerifiedRequestShape(t *testing.T) {
	stub := &stubTransport{status: http.StatusOK, body: `{"models":[]}`}
	if _, err := FetchModels(context.Background(), stub, "https://example.invalid/backend-api/codex/", "0.156.1"); err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(stub.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(stub.requests))
	}
	got := stub.requests[0]
	wantURL := "https://example.invalid/backend-api/codex/models?client_version=0.156.1"
	if got.URL.String() != wantURL {
		t.Fatalf("url = %q, want %q", got.URL.String(), wantURL)
	}
	if got.Method != http.MethodGet {
		t.Fatalf("method = %q, want GET", got.Method)
	}
	if got.Body != nil {
		t.Fatalf("FetchModels sent a body; the models request must be bodyless")
	}
}

// A model the source has never seen must survive decoding: the picker shows
// whatever the account returns, including not-yet-released slugs.
func TestFetchModelsDecodesModelsAndIgnoresUnknownFields(t *testing.T) {
	body := `{"models":[
		{"slug":"gpt-future","display_name":"GPT Future","description":"new",
		 "default_reasoning_level":"low",
		 "supported_reasoning_levels":[{"effort":"low"},{"effort":"max","description":"deepest"}],
		 "visibility":"list","supported_in_api":false,"priority":1,
		 "input_modalities":["text","image"],
		 "context_window":272000,"max_context_window":872000,"comp_hash":"3000",
		 "future_field":{"nested":true}},
		{"slug":"gpt-hidden","display_name":"Hidden","visibility":"hide","priority":2,
		 "supported_reasoning_levels":[{"effort":"medium"}]}
	]}`
	stub := &stubTransport{status: http.StatusOK, body: body}
	models, err := FetchModels(context.Background(), stub, "https://example.invalid", "0.153.4")
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %d, want 2 (hidden entries stay in the raw list)", len(models))
	}
	first := models[0]
	if first.Slug != "gpt-future" || first.DisplayName != "GPT Future" {
		t.Fatalf("first model = %+v", first)
	}
	if !first.PickerVisible() {
		t.Fatal("visibility=list must be picker visible")
	}
	if models[1].PickerVisible() {
		t.Fatal("visibility=hide must not be picker visible")
	}
	if len(first.SupportedReasoningEfforts) != 2 || first.SupportedReasoningEfforts[1].Effort != "max" {
		t.Fatalf("efforts = %+v", first.SupportedReasoningEfforts)
	}
	if first.DefaultReasoningEffort != "low" || first.Priority != 1 || first.CompHash != "3000" {
		t.Fatalf("first model = %+v", first)
	}
	if first.ContextWindow == nil || *first.ContextWindow != 272000 {
		t.Fatalf("context window = %v, want 272000", first.ContextWindow)
	}
	if first.MaxContextWindow == nil || *first.MaxContextWindow != 872000 {
		t.Fatalf("max context window = %v, want 872000", first.MaxContextWindow)
	}
	if !first.SupportsAttachments() {
		t.Fatal("input_modalities with image should report attachments")
	}
	if models[1].SupportsAttachments() {
		t.Fatal("no modalities should not report attachments")
	}
	// supported_in_api=false must still decode present: ChatGPT-authenticated
	// pickers do not filter on it.
	if first.SupportedInAPI {
		t.Fatal("supported_in_api should decode as false for a subscription-only model")
	}
}

// An empty list is a valid answer (an account with nothing to show); a missing
// models member is a protocol break, not an empty catalog.
func TestFetchModelsDistinguishesEmptyFromMissing(t *testing.T) {
	stub := &stubTransport{status: http.StatusOK, body: `{"models":[]}`}
	models, err := FetchModels(context.Background(), stub, "https://example.invalid", "0.153.4")
	if err != nil {
		t.Fatalf("empty list: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("models = %d, want 0", len(models))
	}

	stub = &stubTransport{status: http.StatusOK, body: `{"unexpected":[]}`}
	if _, err := FetchModels(context.Background(), stub, "https://example.invalid", "0.153.4"); err == nil {
		t.Fatal("missing models member = nil error, want a protocol error")
	}
}

func TestFetchModelsRejectsAnEmptySlug(t *testing.T) {
	stub := &stubTransport{status: http.StatusOK, body: `{"models":[{"slug":"   ","display_name":"Broken"}]}`}
	if _, err := FetchModels(context.Background(), stub, "https://example.invalid", "0.153.4"); err == nil {
		t.Fatal("empty slug = nil error, want a protocol error: a model without identity cannot be selected or configured")
	}
}

func TestFetchModelsClassifiesHTTPFailures(t *testing.T) {
	cases := []struct {
		status int
		body   string
	}{
		{http.StatusUnauthorized, `{"error":{"message":"token expired"}}`},
		{http.StatusForbidden, ``},
		{http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`},
		{http.StatusInternalServerError, `{"error":{"message":"upstream"}}`},
		{http.StatusBadRequest, `{"error":{"message":"[{'type': 'missing', 'loc': ('query', 'client_version')}]","type":"invalid_request_error"}}`},
	}
	for _, tc := range cases {
		stub := &stubTransport{status: tc.status, body: tc.body}
		_, err := FetchModels(context.Background(), stub, "https://example.invalid", "0.153.4")
		var me *ModelsError
		if !errors.As(err, &me) {
			t.Fatalf("status %d: err = %v, want *ModelsError", tc.status, err)
		}
		if me.StatusCode != tc.status {
			t.Fatalf("status %d: ModelsError.StatusCode = %d", tc.status, me.StatusCode)
		}
		if tc.body != "" && me.Message == "" {
			t.Fatalf("status %d: safe message dropped", tc.status)
		}
	}
}

func TestFetchModelsRejectsBrokenJSON(t *testing.T) {
	stub := &stubTransport{status: http.StatusOK, body: `{"models":[`}
	if _, err := FetchModels(context.Background(), stub, "https://example.invalid", "0.153.4"); err == nil {
		t.Fatal("broken JSON = nil error, want a decode error")
	}
}

func TestFetchModelsBoundsTheResponse(t *testing.T) {
	stub := &stubTransport{status: http.StatusOK, body: strings.Repeat("x", maxModelsBytes+1)}
	if _, err := FetchModels(context.Background(), stub, "https://example.invalid", "0.153.4"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized body err = %v, want a size error", err)
	}
}

func TestFetchModelsHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stub := &stubTransport{status: http.StatusOK, body: `{"models":[]}`}
	if _, err := FetchModels(ctx, stub, "https://example.invalid", "0.153.4"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// Redirects are answered, not followed: the transport's credentials must never
// be re-sent to whatever location the backend names.
func TestFetchModelsDoesNotFollowRedirects(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Redirect(w, r, "https://example.invalid/elsewhere", http.StatusFound)
	}))
	defer server.Close()
	_, err := FetchModels(context.Background(), http.DefaultTransport, server.URL, "0.153.4")
	var me *ModelsError
	if !errors.As(err, &me) || me.StatusCode != http.StatusFound {
		t.Fatalf("err = %v, want *ModelsError with 302", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want exactly 1 (no follow-up)", requests)
	}
}

// Authentication is the transport's job; the models client only supplies the
// endpoint. This is the wiring proof that the two compose without a second
// implementation of the header rules.
func TestFetchModelsAuthenticatesThroughTheTransport(t *testing.T) {
	capture := &stubTransport{status: http.StatusOK, body: `{"models":[]}`}
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	access := jwtWithExp(t, time.Now().Add(2*time.Hour))
	raw := `{"auth_mode":"chatgpt","tokens":{"id_token":"id","access_token":` +
		quote(access) + `,"refresh_token":"refresh","account_id":"acct_123"}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	transport := &Transport{Path: path, Base: capture}
	if _, err := FetchModels(context.Background(), transport, "https://example.invalid", "0.153.4"); err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(capture.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(capture.requests))
	}
	header := capture.requests[0].Header
	if got := header.Get("Authorization"); got != "Bearer "+access {
		t.Fatalf("Authorization = %q", got)
	}
	if got := header.Get("ChatGPT-Account-ID"); got != "acct_123" {
		t.Fatalf("ChatGPT-Account-ID = %q", got)
	}
	if got := header.Get("OpenAI-Beta"); got != "codex-1" {
		t.Fatalf("OpenAI-Beta = %q", got)
	}
}
