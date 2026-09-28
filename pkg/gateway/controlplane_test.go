package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
)

func TestControlPlaneTokenFromRequest(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer abc")
	if got := ControlPlaneTokenFromRequest(r, ""); got != "abc" {
		t.Fatalf("got %q", got)
	}
	r.Header.Del("Authorization")
	r.Header.Set("X-API-Key", "def")
	if got := ControlPlaneTokenFromRequest(r, ""); got != "def" {
		t.Fatalf("got %q", got)
	}
	if got := ControlPlaneTokenFromRequest(nil, "ghi"); got != "" {
		t.Fatalf("nil request should return empty, got %q", got)
	}
}

func TestControlPlaneTokenFromRequestFallbacks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		auth       string
		apiKey     string
		queryToken string
		want       string
	}{
		{
			name:       "query token when headers empty",
			queryToken: " query-secret ",
			want:       "query-secret",
		},
		{
			name:       "non bearer auth falls through to api key",
			auth:       "Basic abc",
			apiKey:     " key-secret ",
			queryToken: "query-secret",
			want:       "key-secret",
		},
		{
			name:       "blank bearer falls through to query",
			auth:       "Bearer   ",
			queryToken: "query-secret",
			want:       "query-secret",
		},
		{
			name: "empty query remains empty",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.auth != "" {
				r.Header.Set("Authorization", tt.auth)
			}
			if tt.apiKey != "" {
				r.Header.Set("X-API-Key", tt.apiKey)
			}
			if got := ControlPlaneTokenFromRequest(r, tt.queryToken); got != tt.want {
				t.Fatalf("ControlPlaneTokenFromRequest = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestControlPlaneAuthorized(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer secret")
	if !ControlPlaneAuthorized(r, "secret", "") {
		t.Fatal("expected authorized")
	}
	if ControlPlaneAuthorized(r, "other", "") {
		t.Fatal("expected unauthorized")
	}
	if !ControlPlaneAuthorized(nil, "", "") {
		t.Fatal("empty expected should authorize")
	}
}

func TestControlPlaneHTTPMiddlewareUsesGatewayAuthToken(t *testing.T) {
	env := &process.Environment{Deps: run.Deps{AppCfg: &appcfg.Root{
		Gateway: appcfg.Gateway{
			Auth: appcfg.GatewayAuth{
				Mode:  "token",
				Token: "gw-secret",
			},
		},
	}}}
	called := false
	handler := ControlPlaneHTTPMiddleware(env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/resources", nil)
	req.Header.Set("Authorization", "Bearer gw-secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if !called {
		t.Fatal("expected wrapped handler to be called")
	}
}

func TestControlPlaneHTTPMiddlewareRejectsWrongGatewayToken(t *testing.T) {
	env := &process.Environment{Deps: run.Deps{AppCfg: &appcfg.Root{
		Gateway: appcfg.Gateway{
			Auth: appcfg.GatewayAuth{
				Mode:  "token",
				Token: "gw-secret",
			},
		},
	}}}
	handler := ControlPlaneHTTPMiddleware(env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/resources", nil)
	req.Header.Set("Authorization", "Bearer wrong-secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestControlPlaneHTTPMiddlewareRejectsMissingGatewayToken(t *testing.T) {
	env := &process.Environment{Deps: run.Deps{AppCfg: &appcfg.Root{
		Gateway: appcfg.Gateway{
			Auth: appcfg.GatewayAuth{
				Mode: "token",
			},
		},
	}}}
	handler := ControlPlaneHTTPMiddleware(env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/resources", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}
