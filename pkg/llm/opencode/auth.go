package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// maxAuthBytes bounds every response this package reads from the console. The
// account and token responses are a few hundred bytes; the bound exists only so
// a broken or hostile response cannot exhaust memory.
const maxAuthBytes = 1 << 20

// The two hosts the device flow talks to. They are package state rather than
// parameters because LoginDeviceFlow's signature is fixed by its callers (the
// TUI, the gateway and a test all call the same function), and a test needs the
// whole flow — including the verification URL's base — to point at one httptest
// server.
var (
	consoleAPIBase = ConsoleServer
	consoleWebBase = "https://opencode.ai"
)

// Polling delays are package state for the same reason: a test runs the full
// flow and must not wait the RFC's seconds for it.
var (
	// devicePollFloor is the wait used when the code endpoint reports no
	// interval, which RFC 8628 leaves to the client (the servers seen so far
	// report 5).
	devicePollFloor = 5 * time.Second
	// deviceSlowDownIncrement is what "slow_down" adds, per RFC 8628.
	deviceSlowDownIncrement = 5 * time.Second
)

// authClient is the HTTP client for the console endpoints. It has a timeout,
// unlike the provider clients: these are small request/response calls, and a
// hung console must not hang a login or a turn forever.
var authClient = &http.Client{Timeout: 60 * time.Second}

// Credentials is one OpenCode login, in the shape this package writes to
// <FOREBRAIN_HOME>/opencode.json. The names are the token endpoint's own, so the
// file is readable by anyone who knows the protocol.
type Credentials struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	AccountID    string    `json:"account_id,omitempty"`
	Email        string    `json:"email,omitempty"`
	OrgID        string    `json:"org_id,omitempty"`
	OrgName      string    `json:"org_name,omitempty"`
}

// DefaultPath is where credentials live when nothing says otherwise: next to
// forebrain.yaml in FOREBRAIN_HOME.
func DefaultPath(home string) string { return filepath.Join(home, CredentialsFile) }

// Load reads credentials from an explicit file path.
func Load(path string) (Credentials, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, err
	}
	var c Credentials
	if err := json.Unmarshal(raw, &c); err != nil {
		return Credentials{}, fmt.Errorf("parse OpenCode credentials %s: %w", path, err)
	}
	c.AccessToken = strings.TrimSpace(c.AccessToken)
	c.RefreshToken = strings.TrimSpace(c.RefreshToken)
	if c.AccessToken == "" || c.RefreshToken == "" {
		return Credentials{}, fmt.Errorf("OpenCode credentials in %s are incomplete; run /connect to log in", path)
	}
	return c, nil
}

// Save writes credentials to path with an atomic replace, mode 0600. The
// refresh token rotates on every refresh, so a half-written file would leave
// the user logged out with no way to recover the old token.
func Save(path string, c Credentials) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".opencode-*")
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

// LoginInfo is what a caller needs to show a user how to authorize: the URL to
// open and the code the console asks them to confirm.
type LoginInfo struct {
	VerificationURL string
	UserCode        string
}

// LoginDeviceFlow runs the OAuth 2.0 device authorization grant (RFC 8628)
// end to end and stores the result at path.
//
// out receives human-readable progress; a surface that shows status from
// structured data instead passes io.Discard. openBrowser is called once with
// the verification URL: nil selects the desktop default (macOS `open`, Linux
// `xdg-open`), and a server-side caller must pass an explicit no-op closure
// because there is no browser to open there. onStart fires as soon as the
// console has issued a code, which is the first moment the URL and code are
// usable; a caller that only needs the final credentials passes nil.
//
// Cancelling ctx aborts the flow and returns ctx.Err().
func LoginDeviceFlow(ctx context.Context, path string, out io.Writer,
	openBrowser func(url string) error, onStart func(LoginInfo)) (Credentials, error) {
	deviceCode, err := requestDeviceCode(ctx)
	if err != nil {
		return Credentials{}, err
	}
	verifyURL, err := absoluteVerificationURL(deviceCode.VerificationURIComplete)
	if err != nil {
		return Credentials{}, err
	}
	info := LoginInfo{VerificationURL: verifyURL, UserCode: deviceCode.UserCode}
	if onStart != nil {
		onStart(info)
	}
	if out != nil {
		fmt.Fprintf(out, "To sign in to OpenCode, open:\n%s\nand confirm the code %s\n\nWaiting for authorization…\n",
			info.VerificationURL, info.UserCode)
	}
	// Opening the browser is best effort: the URL is printed above, so a
	// headless machine still completes the flow by hand.
	if openBrowser != nil {
		_ = openBrowser(info.VerificationURL)
	} else {
		_ = openBrowserDefault(info.VerificationURL)
	}

	tokens, err := pollDeviceToken(ctx, deviceCode)
	if err != nil {
		return Credentials{}, err
	}
	c := Credentials{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    expiryFrom(tokens.ExpiresIn),
		OrgID:        strings.TrimSpace(tokens.OrgID),
	}
	// The profile calls are cosmetic. A valid token must be kept even when they
	// fail: discarding a working login because /api/user timed out would send
	// the user through the browser flow a second time for nothing.
	account, orgs := fetchAccount(ctx, tokens.AccessToken)
	c.AccountID = account.ID
	c.Email = account.Email
	c.OrgID, c.OrgName = selectOrg(c.OrgID, orgs)
	if err := Save(path, c); err != nil {
		return Credentials{}, fmt.Errorf("save OpenCode credentials: %w", err)
	}
	return c, nil
}

// deviceCodeResponse is the /auth/device/code payload.
type deviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
}

// tokenResponse is the /auth/device/token payload, for both the device grant
// and the refresh grant. An error is reported in the same body as a successful
// grant's fields, so one struct reads both.
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	OrgID            string `json:"org_id"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// requestDeviceCode asks the console for a device code and a user code.
func requestDeviceCode(ctx context.Context) (deviceCodeResponse, error) {
	payload, status, err := postJSON(ctx, consoleAPIBase+"/auth/device/code", map[string]any{
		"client_id":          ClientID,
		"supports_org_scope": true,
	})
	if err != nil {
		return deviceCodeResponse{}, fmt.Errorf("OpenCode device code request: %w", err)
	}
	if status < 200 || status >= 300 {
		return deviceCodeResponse{}, fmt.Errorf("OpenCode device code request returned %d: %s", status, authErrorMessage(payload))
	}
	var code deviceCodeResponse
	if err := json.Unmarshal(payload, &code); err != nil {
		return deviceCodeResponse{}, fmt.Errorf("decode OpenCode device code: %w", err)
	}
	if strings.TrimSpace(code.DeviceCode) == "" || strings.TrimSpace(code.UserCode) == "" {
		return deviceCodeResponse{}, errors.New("OpenCode device code response is missing the device code or user code")
	}
	return code, nil
}

// pollDeviceToken polls the token endpoint until the user authorizes, the code
// expires, or ctx ends.
func pollDeviceToken(ctx context.Context, code deviceCodeResponse) (tokenResponse, error) {
	interval := time.Duration(code.Interval) * time.Second
	if interval <= 0 {
		interval = devicePollFloor
	}
	expiresIn := time.Duration(code.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = 10 * time.Minute
	}
	deadline := time.Now().Add(expiresIn)
	for {
		payload, status, err := postJSON(ctx, consoleAPIBase+"/auth/device/token", map[string]any{
			"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
			"device_code": code.DeviceCode,
			"client_id":   ClientID,
		})
		if err != nil {
			return tokenResponse{}, fmt.Errorf("OpenCode device token request: %w", err)
		}
		var tokens tokenResponse
		// The endpoint reports "authorization_pending" as an error body, often
		// with a 4xx status, so the body's error field decides what happened —
		// not the status.
		_ = json.Unmarshal(payload, &tokens)
		switch tokens.Error {
		case "":
			if status < 200 || status >= 300 {
				return tokenResponse{}, fmt.Errorf("OpenCode device token request returned %d: %s", status, authErrorMessage(payload))
			}
			if strings.TrimSpace(tokens.AccessToken) == "" {
				return tokenResponse{}, errors.New("OpenCode device token response has no access token")
			}
			if strings.TrimSpace(tokens.RefreshToken) == "" {
				return tokenResponse{}, errors.New("OpenCode device token response has no refresh token")
			}
			return tokens, nil
		case "authorization_pending":
			// The user has not finished in the browser yet.
		case "slow_down":
			interval += deviceSlowDownIncrement
		case "access_denied":
			return tokenResponse{}, errors.New("OpenCode sign-in was denied")
		case "expired_token":
			return tokenResponse{}, errors.New("OpenCode sign-in code expired; run /connect again")
		default:
			return tokenResponse{}, fmt.Errorf("OpenCode sign-in failed: %s", oauthErrorText(tokens))
		}
		if time.Now().After(deadline) {
			return tokenResponse{}, errors.New("OpenCode sign-in code expired; run /connect again")
		}
		select {
		case <-ctx.Done():
			return tokenResponse{}, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// absoluteVerificationURL resolves the code endpoint's verification URL. It is
// a relative path ("/console/device?user_code=…"), so it is resolved against
// the console web host; an absolute one is kept as long as it is http(s), so a
// ws:// or file:// target can never be handed to a browser.
func absoluteVerificationURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("OpenCode device code response has no verification URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("OpenCode verification URL %q: %w", raw, err)
	}
	if !u.IsAbs() {
		base, err := url.Parse(consoleWebBase)
		if err != nil {
			return "", fmt.Errorf("OpenCode verification base %q: %w", consoleWebBase, err)
		}
		u = base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("OpenCode verification URL %q is not an http(s) URL", raw)
	}
	return u.String(), nil
}

// accountInfo is the /api/user payload.
type accountInfo struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// orgInfo is one /api/orgs entry.
type orgInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// fetchAccount reads the profile and the org list for a fresh login. They are
// independent, so they run together; either failing yields a zero value rather
// than an error, because the token is what the caller needs.
func fetchAccount(ctx context.Context, token string) (accountInfo, []orgInfo) {
	var (
		account accountInfo
		orgs    []orgInfo
		wg      sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		account = fetchUser(ctx, token)
	}()
	go func() {
		defer wg.Done()
		orgs = fetchOrgs(ctx, token)
	}()
	wg.Wait()
	return account, orgs
}

// fetchUser reads GET /api/user. A failure reports the zero value.
func fetchUser(ctx context.Context, token string) accountInfo {
	payload, status, err := getJSON(ctx, consoleAPIBase+"/api/user", token)
	if err != nil || status < 200 || status >= 300 {
		return accountInfo{}
	}
	var account accountInfo
	if err := json.Unmarshal(payload, &account); err != nil {
		return accountInfo{}
	}
	account.ID = strings.TrimSpace(account.ID)
	account.Email = strings.TrimSpace(account.Email)
	return account
}

// fetchOrgs reads GET /api/orgs. A failure reports no orgs.
func fetchOrgs(ctx context.Context, token string) []orgInfo {
	payload, status, err := getJSON(ctx, consoleAPIBase+"/api/orgs", token)
	if err != nil || status < 200 || status >= 300 {
		return nil
	}
	var orgs []orgInfo
	if err := json.Unmarshal(payload, &orgs); err != nil {
		return nil
	}
	return orgs
}

// selectOrg resolves which org the login belongs to and returns its id and
// name. The token grant reports the org it was signed for; when it reports none
// (a personal account), the first org by name wins, which is what the official
// client does. Both lookups are by the org list, so the id and the name cannot
// disagree.
func selectOrg(granted string, orgs []orgInfo) (string, string) {
	sorted := make([]orgInfo, len(orgs))
	copy(sorted, orgs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	granted = strings.TrimSpace(granted)
	if granted != "" {
		for _, o := range sorted {
			if o.ID == granted {
				return o.ID, o.Name
			}
		}
		return granted, ""
	}
	if len(sorted) == 0 {
		return "", ""
	}
	return sorted[0].ID, sorted[0].Name
}

// refresh exchanges a refresh token for a new pair. The server rotates the
// refresh token, so the caller must persist the result; an "invalid_grant"
// answer means the login is gone and only a new device flow can restore it.
func refresh(ctx context.Context, c Credentials) (Credentials, error) {
	payload, status, err := postJSON(ctx, consoleAPIBase+"/auth/device/token", map[string]any{
		"grant_type":    "refresh_token",
		"refresh_token": c.RefreshToken,
		"client_id":     ClientID,
	})
	if err != nil {
		return Credentials{}, fmt.Errorf("refresh OpenCode login: %w", err)
	}
	var tokens tokenResponse
	_ = json.Unmarshal(payload, &tokens)
	if tokens.Error == "invalid_grant" {
		// The refresh token is gone (revoked, or another client rotated it).
		// 401 so the classifiers upstream offer "log in again" instead of a
		// retry that can only fail the same way.
		return Credentials{}, &llm.APIError{
			StatusCode: http.StatusUnauthorized,
			Message:    loginExpiredMessage,
		}
	}
	if tokens.Error != "" || status < 200 || status >= 300 {
		return Credentials{}, fmt.Errorf("refresh OpenCode login: %s", authErrorMessage(payload))
	}
	if strings.TrimSpace(tokens.AccessToken) == "" {
		return Credentials{}, errors.New("refresh OpenCode login: token endpoint returned no access token")
	}
	updated := c
	updated.AccessToken = tokens.AccessToken
	if rt := strings.TrimSpace(tokens.RefreshToken); rt != "" {
		updated.RefreshToken = rt
	}
	updated.ExpiresAt = expiryFrom(tokens.ExpiresIn)
	if org := strings.TrimSpace(tokens.OrgID); org != "" {
		updated.OrgID = org
	}
	return updated, nil
}

// loginExpiredMessage is what a user must read after a dead refresh token: the
// one refresh failure they can act on.
const loginExpiredMessage = "OpenCode login expired; run /connect again"

// postJSON posts body to endpoint as JSON and returns the raw response body and
// its status. A non-2xx status is not an error by itself: the device token
// endpoint reports "authorization_pending" with an error status, so the caller
// decides what a status means.
func postJSON(ctx context.Context, endpoint string, body any) ([]byte, int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent())
	return doAuthRequest(req)
}

// getJSON reads endpoint with a bearer token.
func getJSON(ctx context.Context, endpoint, token string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent())
	if token = strings.TrimSpace(token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return doAuthRequest(req)
}

func doAuthRequest(req *http.Request) ([]byte, int, error) {
	resp, err := authClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxAuthBytes+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if len(payload) > maxAuthBytes {
		return nil, resp.StatusCode, fmt.Errorf("response exceeds %d bytes", maxAuthBytes)
	}
	return payload, resp.StatusCode, nil
}

// authErrorMessage extracts a short message from an error body. The console
// answers in two shapes — its own {"type":"error","error":{"message":…}} and an
// upstream {"error":{"code":…,"message":…}} — but both carry error.message, so
// one shape reads both.
func authErrorMessage(body []byte) string {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Error) > 0 {
		var detail struct {
			Message     string `json:"message"`
			Description string `json:"error_description"`
		}
		if err := json.Unmarshal(envelope.Error, &detail); err == nil {
			if msg := strings.TrimSpace(firstNonEmpty(detail.Message, detail.Description)); msg != "" {
				return truncateAuthMessage(msg)
			}
		}
		var text string
		if err := json.Unmarshal(envelope.Error, &text); err == nil && strings.TrimSpace(text) != "" {
			return truncateAuthMessage(strings.TrimSpace(text))
		}
	}
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		return truncateAuthMessage(trimmed)
	}
	return "authentication failed"
}

// oauthErrorText names an OAuth error for a human: the description when the
// console sent one, the code otherwise.
func oauthErrorText(t tokenResponse) string {
	return truncateAuthMessage(firstNonEmpty(strings.TrimSpace(t.ErrorDescription), strings.TrimSpace(t.Error)))
}

func truncateAuthMessage(s string) string {
	const limit = 300
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// expiryFrom turns the token endpoint's expires_in into an absolute deadline. A
// grant that reports no lifetime leaves the deadline zero, which every reader
// treats as already expired: refreshing immediately is recoverable, sending a
// token that may be dead is not.
func expiryFrom(seconds int64) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Now().Add(time.Duration(seconds) * time.Second).UTC()
}

// openBrowserDefault opens target with the desktop's URL handler. A failure is
// the caller's to ignore: the URL is always printed as well.
func openBrowserDefault(target string) error {
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
