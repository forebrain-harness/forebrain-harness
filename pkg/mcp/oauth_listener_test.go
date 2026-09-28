package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/testutil"
)

func TestListenForOAuthCodeCapturesCodeAndValidatesState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	code, err := ListenForOAuthCode(ctx, "/cb", "expected-state", func(callbackURL string) {
		go func() {
			resp, getErr := http.Get(callbackURL + "?code=abc123&state=expected-state")
			if getErr == nil && resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
		}()
	})
	if err != nil {
		t.Fatalf("ListenForOAuthCode error: %v", err)
	}
	if code != "abc123" {
		t.Fatalf("code=%q", code)
	}
}

func TestListenForOAuthCodeRejectsWrongState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := ListenForOAuthCode(ctx, "/cb", "expected-state", func(callbackURL string) {
		go func() {
			resp, getErr := http.Get(callbackURL + "?code=abc123&state=wrong")
			if getErr == nil && resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
		}()
	})
	if err == nil || !strings.Contains(err.Error(), "invalid state") {
		t.Fatalf("expected invalid state error, got %v", err)
	}
}

func TestOAuthRefreshPersistsAccessTokenAndPreservesRefreshToken(t *testing.T) {
	tokenServer := testutil.NewLocalServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh-old" {
			t.Fatalf("unexpected refresh form: %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-new",
			"token_type":   "Bearer",
		})
	}))
	defer tokenServer.Close()
	home := t.TempDir()

	src, _, err := oauthTokenSourceForServer(context.Background(), home, "", "docs", appcfg.MCPOAuthConfig{
		Mode:         "refresh",
		ClientID:     "client",
		TokenURL:     tokenServer.URL,
		RefreshToken: "refresh-old",
	})
	if err != nil {
		t.Fatalf("oauthTokenSourceForServer error: %v", err)
	}
	tok, err := src.Token()
	if err != nil {
		t.Fatalf("Token error: %v", err)
	}
	if tok.AccessToken != "access-new" {
		t.Fatalf("access token=%q", tok.AccessToken)
	}
	overlay, ok, err := LoadOAuthOverlay(home, "", "docs")
	if err != nil {
		t.Fatalf("LoadOAuthOverlay error: %v", err)
	}
	if !ok {
		t.Fatalf("expected overlay")
	}
	if overlay.AccessToken != "access-new" || overlay.RefreshToken != "refresh-old" || overlay.Mode != "refresh" {
		t.Fatalf("overlay=%+v", overlay)
	}
}
