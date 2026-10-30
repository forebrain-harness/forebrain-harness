package opencode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// refreshMargin is how early a token is refreshed. A request in flight when the
// access token dies would otherwise fail with a 401 that looks like a broken
// login.
const refreshMargin = 60 * time.Second

// clientVersion is the build version announced in the User-Agent. The
// composition root installs home.Version at startup: pkg/llm is Layer 0 and
// pkg/home is Layer 1, so a provider package here cannot read it directly.
var clientVersion atomic.Pointer[string]

// SetClientVersion installs the version stamped into the User-Agent. An empty
// value restores the "dev" fallback.
func SetClientVersion(version string) {
	version = strings.TrimSpace(version)
	if version == "" {
		clientVersion.Store(nil)
		return
	}
	clientVersion.Store(&version)
}

// UserAgent is this client's identity on every request. OpenCode's Go
// subscription requires clients to identify themselves — the subscription
// admits third-party clients through the User-Agent, not through a private
// client id — so this header is part of the contract, not decoration.
func UserAgent() string {
	version := "dev"
	if p := clientVersion.Load(); p != nil {
		version = *p
	}
	return "forebrain-harness/" + version
}

// Transport attaches an OpenCode token to every request and keeps it fresh.
//
// Two modes share the type because both call sites build a client the same way:
// with Path set, each request reads the login file and refreshes it when it is
// close to expiry (writing the rotated tokens back); with a key, the token is
// static and nothing is read or written. NewKeyTransport builds the second
// kind.
type Transport struct {
	// Path is the credentials file, for an OAuth login.
	Path string
	// Base is the transport requests are handed to; nil means
	// http.DefaultTransport.
	Base http.RoundTripper

	// key is the static token of key mode: a Console API key.
	key string
}

// NewKeyTransport returns a transport that sends key as a static bearer token,
// with no refresh logic. It is the API-key path.
func NewKeyTransport(key string) *Transport {
	return &Transport{key: strings.TrimSpace(key)}
}

// OwnsUserAgent reports that this transport installs the User-Agent itself.
// OpenCode's Go subscription admits third-party clients by the agent they
// announce, so a chain that blanks the header would make every request
// anonymous. The responses client asks this before deciding to blank it.
func (t *Transport) OwnsUserAgent() bool { return true }

// RoundTrip attaches the token, this client's identity and the session header,
// then delegates.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.token(req.Context())
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	if token != "" {
		clone.Header.Set("Authorization", "Bearer "+token)
	}
	clone.Header.Set("User-Agent", UserAgent())
	// Console uses this header to route requests and to keep prompt caching
	// stable per conversation, so it is set whenever an execution scope has a
	// session id — and left off rather than invented when it does not.
	if session := strings.TrimSpace(llm.AgentSessionIDFromContext(req.Context())); session != "" {
		clone.Header.Set("x-opencode-session", session)
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

// token returns the bearer token to send, refreshing an OAuth login first when
// it is expired or about to be.
func (t *Transport) token(ctx context.Context) (string, error) {
	if t.key != "" || strings.TrimSpace(t.Path) == "" {
		return t.key, nil
	}
	// One lock per login file, not per Transport: every client of this file in
	// the process (a turn's LLM, a subagent's, a model-listing probe) shares it,
	// so the refresh token — which rotates on use — is exchanged once.
	mu := llm.CredentialMutex(t.Path)
	mu.Lock()
	defer mu.Unlock()
	c, err := LoadProcessCredentials(ctx, t.Path)
	if err != nil {
		return "", fmt.Errorf("OpenCode login required; run /connect to log in: %w", err)
	}
	if time.Until(c.ExpiresAt) > refreshMargin {
		return c.AccessToken, nil
	}
	updated, err := refresh(ctx, c)
	if err != nil {
		if adopted, ok := adoptRotatedLogin(ctx, t.Path, c, err); ok {
			return adopted.AccessToken, nil
		}
		return "", err
	}
	if err := SaveProcessCredentials(ctx, t.Path, updated); err != nil {
		return "", fmt.Errorf("save refreshed OpenCode credentials: %w", err)
	}
	return updated.AccessToken, nil
}

// adoptRotatedLogin handles a refresh the server refused as invalid_grant. The
// refresh token we sent may simply be stale: another process (the TUI beside
// the gateway) exchanged it first and wrote the rotated pair. When the file now
// holds a different refresh token and a login that is not about to expire, that
// login is the live one — use it instead of reporting a dead login.
func adoptRotatedLogin(ctx context.Context, path string, sent Credentials, refreshErr error) (Credentials, bool) {
	var api *llm.APIError
	if !errors.As(refreshErr, &api) || api.StatusCode != http.StatusUnauthorized {
		return Credentials{}, false
	}
	latest, err := LoadProcessCredentials(ctx, path)
	if err != nil || latest.RefreshToken == sent.RefreshToken {
		return Credentials{}, false
	}
	if time.Until(latest.ExpiresAt) <= refreshMargin {
		return Credentials{}, false
	}
	return latest, true
}
