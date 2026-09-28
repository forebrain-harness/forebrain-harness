// Package openai implements the ChatGPT/Codex client and the browser OAuth
// flow used to access Codex through a developer's ChatGPT subscription.
package openai

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	Issuer       = "https://auth.openai.com"
	ClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	CodexBaseURL = "https://chatgpt.com/backend-api/codex"
	UsageURL     = "https://chatgpt.com/backend-api/wham/usage"

	// CredentialsFile is the default name of the credentials file, resolved
	// against FOREBRAIN_HOME so it sits next to forebrain.yaml. The name and the
	// on-disk shape are deliberately the same as the Codex CLI's, so one login
	// serves both tools and neither has to be told about the other.
	CredentialsFile = "auth.json"
)

type Credentials struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
	Email        string    `json:"email,omitempty"`
	Plan         string    `json:"plan,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	// AuthMode carries the Codex file's auth_mode through a refresh so a
	// round-trip does not silently rewrite it.
	AuthMode string `json:"auth_mode,omitempty"`
}

// storedAuth is the on-disk shape. It mirrors the Codex CLI's auth.json
// exactly — nested tokens, snake_case, an auth_mode string and a last_refresh
// stamp — because the whole point is that the same file works for both. Note
// there is no expiry field: Codex does not write one, so the expiry is read
// from the access token's own exp claim instead of being stored.
type storedAuth struct {
	AuthMode    string       `json:"auth_mode,omitempty"`
	APIKey      *string      `json:"OPENAI_API_KEY"`
	Tokens      storedTokens `json:"tokens"`
	LastRefresh string       `json:"last_refresh,omitempty"`
}

type storedTokens struct {
	IDToken      string `json:"id_token,omitempty"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id,omitempty"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// DefaultPath is where credentials live when the config does not say
// otherwise: next to forebrain.yaml in FOREBRAIN_HOME.
func DefaultPath(home string) string { return filepath.Join(home, CredentialsFile) }

// ResolvePath picks the credentials file: the configured path when set,
// otherwise DefaultPath. A relative configured path is taken relative to
// FOREBRAIN_HOME, so a config file stays portable between machines. A leading ~
// is expanded, since this is a path a human types.
func ResolvePath(home, configured string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return DefaultPath(home)
	}
	if configured == "~" || strings.HasPrefix(configured, "~/") {
		if dir, err := os.UserHomeDir(); err == nil {
			return filepath.Join(dir, strings.TrimPrefix(strings.TrimPrefix(configured, "~"), "/"))
		}
	}
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured)
	}
	return filepath.Join(home, configured)
}

// configuredPath holds the credentials.chatgpt override from forebrain.yaml. It
// is installed once by process.Open and read whenever a ChatGPT client is
// built. It is package state rather than a parameter because the path is
// process-wide — one config, one home — and the alternative is threading it
// through every LLMProviderYAML construction site, four of which exist purely
// to convert config shapes.
var configuredPath atomic.Pointer[string]

// SetCredentialsPath installs the configured override. An empty value clears
// it, restoring the default of <FOREBRAIN_HOME>/auth.json.
func SetCredentialsPath(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		configuredPath.Store(nil)
		return
	}
	configuredPath.Store(&path)
}

// CredentialsPath resolves the credentials file for this process: the
// configured override when one is installed, otherwise <home>/auth.json.
func CredentialsPath(home string) string {
	configured := ""
	if p := configuredPath.Load(); p != nil {
		configured = *p
	}
	return ResolvePath(home, configured)
}

// Load reads credentials from an explicit file path.
func Load(path string) (Credentials, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, err
	}
	var stored storedAuth
	if err := json.Unmarshal(raw, &stored); err != nil {
		return Credentials{}, fmt.Errorf("parse ChatGPT credentials %s: %w", path, err)
	}
	c := Credentials{
		AccessToken:  strings.TrimSpace(stored.Tokens.AccessToken),
		RefreshToken: strings.TrimSpace(stored.Tokens.RefreshToken),
		IDToken:      strings.TrimSpace(stored.Tokens.IDToken),
		AccountID:    strings.TrimSpace(stored.Tokens.AccountID),
		AuthMode:     strings.TrimSpace(stored.AuthMode),
	}
	if c.AccessToken == "" || c.RefreshToken == "" {
		return Credentials{}, fmt.Errorf("ChatGPT credentials in %s are incomplete; run /connect to log in", path)
	}
	// The file carries no expiry, so it comes from the token itself. A token
	// whose exp cannot be read is treated as already expired, which makes the
	// transport refresh rather than send something the server will reject.
	c.ExpiresAt = accessTokenExpiry(c.AccessToken)
	return c, nil
}

// accessTokenExpiry reads the exp claim from a JWT access token. It returns
// the zero time when the token is not a readable JWT or has no exp, which
// callers treat as expired.
func accessTokenExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0).UTC()
}

// Save writes credentials back in the Codex on-disk shape, to the same path
// they were read from. Writing the same format matters: a refresh rotates the
// refresh token, so a forebrain refresh that wrote a different shape — or a
// different file — would leave the Codex CLI holding a token the server has
// already invalidated.
func Save(path string, c Credentials) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mode := strings.TrimSpace(c.AuthMode)
	if mode == "" {
		mode = "chatgpt"
	}
	stored := storedAuth{
		AuthMode: mode,
		Tokens: storedTokens{
			IDToken:      c.IDToken,
			AccessToken:  c.AccessToken,
			RefreshToken: c.RefreshToken,
			AccountID:    c.AccountID,
		},
		LastRefresh: time.Now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".auth-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Login runs a loopback PKCE flow and stores the result at path. The URL is
// always printed so the flow also works when the browser cannot be opened
// automatically.
func Login(ctx context.Context, path string, out io.Writer) (Credentials, error) {
	ln, redirectURI, err := listenLoopback()
	if err != nil {
		return Credentials{}, err
	}
	defer ln.Close()

	state, err := randomURLToken(32)
	if err != nil {
		return Credentials{}, err
	}
	verifier, err := randomURLToken(64)
	if err != nil {
		return Credentials{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type":              {"code"},
		"client_id":                  {ClientID},
		"redirect_uri":               {redirectURI},
		"scope":                      {"openid profile email offline_access"},
		"code_challenge":             {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method":      {"S256"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"originator":                 {"forebrain"},
		"state":                      {state},
	}
	authURL := Issuer + "/oauth/authorize?" + q.Encode()
	if out != nil {
		fmt.Fprintf(out, "Open this URL to sign in with ChatGPT:\n%s\n\nWaiting for browser authentication…\n", authURL)
	}
	_ = openBrowser(authURL)

	type result struct {
		code string
		err  error
	}
	resultCh := make(chan result, 1)
	server := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/callback" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("state") != state {
			http.Error(w, "State mismatch. Return to Forebrain Harness and try again.", http.StatusBadRequest)
			resultCh <- result{err: errors.New("OAuth state mismatch")}
			return
		}
		if oauthErr := r.URL.Query().Get("error"); oauthErr != "" {
			msg := strings.TrimSpace(r.URL.Query().Get("error_description"))
			if msg == "" {
				msg = oauthErr
			}
			http.Error(w, "Sign-in failed. Return to Forebrain Harness.", http.StatusBadRequest)
			resultCh <- result{err: fmt.Errorf("ChatGPT sign-in failed: %s", msg)}
			return
		}
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		if code == "" {
			http.Error(w, "Missing authorization code.", http.StatusBadRequest)
			resultCh <- result{err: errors.New("ChatGPT callback did not include an authorization code")}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html><title>Forebrain Harness signed in</title><h1>Signed in to Forebrain Harness</h1><p>You can close this window and return to your terminal.</p>")
		resultCh <- result{code: code}
	})
	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			resultCh <- result{err: err}
		}
	}()

	var callback result
	select {
	case callback = <-resultCh:
	case <-ctx.Done():
		callback.err = ctx.Err()
	}
	_ = server.Shutdown(context.Background())
	if callback.err != nil {
		return Credentials{}, callback.err
	}
	tokens, err := exchange(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {callback.code},
		"redirect_uri":  {redirectURI},
		"client_id":     {ClientID},
		"code_verifier": {verifier},
	})
	if err != nil {
		return Credentials{}, err
	}
	if tokens.RefreshToken == "" {
		return Credentials{}, errors.New("ChatGPT token endpoint returned no refresh token")
	}
	c := credentialsFromTokens(tokens)
	if err := Save(path, c); err != nil {
		return Credentials{}, fmt.Errorf("save ChatGPT credentials: %w", err)
	}
	return c, nil
}

func listenLoopback() (net.Listener, string, error) {
	for _, port := range []int{1455, 1457} {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return ln, fmt.Sprintf("http://localhost:%d/auth/callback", port), nil
		}
	}
	return nil, "", errors.New("cannot start ChatGPT login callback on localhost ports 1455 or 1457")
}

func exchange(ctx context.Context, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Issuer+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("ChatGPT token exchange: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tokenResponse{}, fmt.Errorf("ChatGPT token endpoint returned %s: %s", resp.Status, safeOAuthError(body))
	}
	var tokens tokenResponse
	if err := json.Unmarshal(body, &tokens); err != nil {
		return tokenResponse{}, fmt.Errorf("decode ChatGPT tokens: %w", err)
	}
	if tokens.AccessToken == "" {
		return tokenResponse{}, errors.New("ChatGPT token endpoint returned no access token")
	}
	return tokens, nil
}

func credentialsFromTokens(t tokenResponse) Credentials {
	claims := jwtClaims(t.IDToken)
	expires := time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	if t.ExpiresIn <= 0 {
		if exp, ok := numberClaim(jwtClaims(t.AccessToken), "exp"); ok {
			expires = time.Unix(exp, 0)
		}
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	profile, _ := claims["https://api.openai.com/profile"].(map[string]any)
	return Credentials{
		AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, IDToken: t.IDToken,
		AccountID: stringClaim(auth, "chatgpt_account_id"),
		Email:     firstNonEmpty(stringClaim(claims, "email"), stringClaim(profile, "email")),
		Plan:      stringClaim(auth, "chatgpt_plan_type"), ExpiresAt: expires,
	}
}

func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func numberClaim(m map[string]any, key string) (int64, bool) {
	v, ok := m[key].(float64)
	return int64(v), ok
}

func stringClaim(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func randomURLToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func safeOAuthError(body []byte) string {
	var v struct {
		Error any    `json:"error"`
		Desc  string `json:"error_description"`
	}
	if json.Unmarshal(body, &v) == nil {
		if v.Desc != "" {
			return v.Desc
		}
		switch e := v.Error.(type) {
		case string:
			return e
		case map[string]any:
			if msg, _ := e["message"].(string); msg != "" {
				return msg
			}
			if code, _ := e["code"].(string); code != "" {
				return code
			}
		}
	}
	return "authentication failed"
}

func openBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}

// Transport refreshes credentials when needed and attaches ChatGPT routing
// headers to Codex backend requests.
type Transport struct {
	// Path is the credentials file this transport reads and refreshes.
	Path string
	Base http.RoundTripper
	mu   sync.Mutex
}

func (t *Transport) SetBase(base http.RoundTripper) { t.Base = base }

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	c, err := t.current(req.Context())
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+c.AccessToken)
	clone.Header.Set("OpenAI-Beta", "codex-1")
	clone.Header.Set("originator", "forebrain")
	if c.AccountID != "" {
		clone.Header.Set("ChatGPT-Account-ID", c.AccountID)
	}
	// The ChatGPT backend routes a request to the machine holding its prompt
	// cache by these headers, not by the body's prompt_cache_key: without them
	// every response is cached under a fresh random key and a conversation's
	// growing prefix is almost never found again. They carry the very key the
	// body carries, so header and body name one cache.
	if key := openAIPromptCacheKey(req.Context()); key != "" {
		clone.Header.Set("session-id", key)
		clone.Header.Set("thread-id", key)
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

func (t *Transport) current(ctx context.Context) (Credentials, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, err := Load(t.Path)
	if err != nil {
		return Credentials{}, fmt.Errorf("ChatGPT login required: %w", err)
	}
	if time.Until(c.ExpiresAt) > 2*time.Minute {
		return c, nil
	}
	tokens, err := exchange(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {c.RefreshToken},
		"client_id":     {ClientID},
	})
	if err != nil {
		return Credentials{}, fmt.Errorf("refresh ChatGPT login: %w", err)
	}
	updated := credentialsFromTokens(tokens)
	if updated.RefreshToken == "" {
		updated.RefreshToken = c.RefreshToken
	}
	if updated.AccountID == "" {
		updated.AccountID = c.AccountID
	}
	if updated.Email == "" {
		updated.Email = c.Email
	}
	if updated.Plan == "" {
		updated.Plan = c.Plan
	}
	if updated.IDToken == "" {
		updated.IDToken = c.IDToken
	}
	if err := Save(t.Path, updated); err != nil {
		return Credentials{}, err
	}
	return updated, nil
}
