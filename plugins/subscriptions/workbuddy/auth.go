// auth.go owns the WorkBuddy credential: how it is obtained (the official
// client's account-scoped external-link sign-in — ask for a login state, let the
// browser confirm it, poll for the token, then read the account), where it lives
// (the Forebrain state store, with a 0600 file fallback the run-time transport
// can write), and how it stays fresh (a single-writer refresh under the shared
// credential lock).
//
// The WorkBuddy desktop app's own login file is never read, decrypted or
// written: this plugin signs in for itself, so a WorkBuddy window running beside
// Forebrain is never disturbed and needs no keychain access.
package workbuddy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// credentialSection is the fb_config section the login lands in.
const credentialSection = "auth.workbuddy"

// tokenPendingCode is the answer /v2/plugin/auth/token gives while the browser
// half of the sign-in is unfinished (LOGIN_TOKEN_PENDING_CODE in the official
// client).
const tokenPendingCode = 11217

// httpClient performs the plugin's own calls: the sign-in, the refresh and the
// model listings. Model requests do not go through it — the LLM client's own
// transport does, so its streaming and accounting wrappers stay in place.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// Credentials is one stored WorkBuddy login: the token pair, the account it
// belongs to (whose fields every identity header is built from), and when it
// expires.
type Credentials struct {
	UID          string `json:"uid,omitempty"`
	Name         string `json:"name,omitempty"`
	EnterpriseID string `json:"enterprise_id,omitempty"`
	IDSource     string `json:"id_source,omitempty"`
	Domain       string `json:"domain,omitempty"`
	// AccountType is /v2/plugin/account's type ("personal", "pro", …). It
	// decides whether the account is inside the product's paid line, which is
	// what collapses a free/paid model pair into one row (§3.1). A login
	// stored by an earlier release has no type in it; the model paths fill it
	// in from the account endpoint the first time they read one.
	AccountType      string    `json:"account_type,omitempty"`
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	ExpiresAt        time.Time `json:"expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
	SessionState     string    `json:"session_state,omitempty"`
	// LoginMethod is the sign-in this credential came from. It is this plugin's
	// own flow, so it is always "browser": the value X-Auth-Method carries and
	// the one X-Userinfo states as token_source.
	LoginMethod string `json:"login_method,omitempty"`
}

// credentialPath is the file fallback (and the path the shared credential lock
// is keyed on).
func credentialPath(home string) string {
	return filepath.Join(home, "workbuddy.json")
}

// loadCredentials reads the stored login. Both homes it can live in — the state
// store and the credential file — may hold one, and the fresher wins: the
// run-time transport writes the file (it has no state handle), the subscription
// surface writes the store, and a refresh from either must not leave the other's
// older token behind.
func loadCredentials(ctx context.Context, db *sql.DB, home string) (Credentials, bool, error) {
	var stored, filed Credentials
	var haveStored, haveFiled bool
	if db != nil {
		raw, err := (&state.ConfigStore{DB: db}).LoadCredential(ctx, credentialSection)
		if err != nil {
			return Credentials{}, false, err
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &stored); err != nil {
				return Credentials{}, false, fmt.Errorf("workbuddy: stored credential: %w", err)
			}
			haveStored = true
		}
	}
	if raw, err := os.ReadFile(credentialPath(home)); err == nil {
		if err := json.Unmarshal(raw, &filed); err != nil {
			return Credentials{}, false, fmt.Errorf("workbuddy: credential file: %w", err)
		}
		haveFiled = true
	} else if !os.IsNotExist(err) {
		return Credentials{}, false, err
	}
	switch {
	case haveStored && haveFiled:
		if filed.ExpiresAt.After(stored.ExpiresAt) {
			return filed, true, nil
		}
		return stored, true, nil
	case haveStored:
		return stored, true, nil
	case haveFiled:
		return filed, true, nil
	}
	return Credentials{}, false, nil
}

// saveCredentials writes the login to both places loadCredentials reads — a nil
// DB leaves only the credential file, which holds tokens and is therefore 0600.
// The file is replaced atomically, so a reader never sees half a login.
func saveCredentials(ctx context.Context, db *sql.DB, home string, creds Credentials) error {
	raw, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	if db != nil {
		if err := (&state.ConfigStore{DB: db}).SaveCredential(ctx, credentialSection, raw); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	path := credentialPath(home)
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// tokenPayload is what the sign-in's token poll and the refresh both answer
// with: the token pair and its lifetimes, the session, and the account's domain.
type tokenPayload struct {
	AccessToken      string `json:"accessToken"`
	RefreshToken     string `json:"refreshToken"`
	ExpiresIn        int64  `json:"expiresIn"`
	RefreshExpiresIn int64  `json:"refreshExpiresIn"`
	TokenType        string `json:"tokenType"`
	SessionState     string `json:"sessionState"`
	Domain           string `json:"domain"`
	LastRefreshTime  int64  `json:"lastRefreshTime"`
}

// credentials turns the payload into what is stored: a lifetime the payload did
// not state stays zero, which the freshness check reads as "unknown" rather than
// "expired".
func (p tokenPayload) credentials(now time.Time) Credentials {
	creds := Credentials{
		AccessToken:  strings.TrimSpace(p.AccessToken),
		RefreshToken: strings.TrimSpace(p.RefreshToken),
		SessionState: strings.TrimSpace(p.SessionState),
		Domain:       strings.TrimSpace(p.Domain),
		LoginMethod:  authMethod,
	}
	if p.ExpiresIn > 0 {
		creds.ExpiresAt = now.Add(time.Duration(p.ExpiresIn) * time.Second)
	}
	if p.RefreshExpiresIn > 0 {
		creds.RefreshExpiresAt = now.Add(time.Duration(p.RefreshExpiresIn) * time.Second)
	}
	return creds
}

// LoginExternalLink runs the account-scoped external-link sign-in: ask for a
// login state, hand its authorization URL to the surface (which opens it in the
// user's browser — a flow never opens one itself), poll until the browser half
// is confirmed, then read the account the token belongs to and store both.
func LoginExternalLink(ctx context.Context, db *sql.DB, home string, onURL func(string)) (Credentials, error) {
	loginState, authURL, err := requestLoginState(ctx)
	if err != nil {
		return Credentials{}, err
	}
	if onURL != nil {
		onURL(authURL)
	}
	payload, err := pollLoginToken(ctx, loginState)
	if err != nil {
		return Credentials{}, err
	}
	creds := payload.credentials(time.Now())
	if creds.AccessToken == "" {
		return Credentials{}, errors.New("workbuddy: the sign-in answer carried no access token")
	}
	account, err := fetchAccount(ctx, creds.AccessToken)
	if err != nil {
		return Credentials{}, err
	}
	account.applyTo(&creds)
	if err := saveCredentials(ctx, db, home, creds); err != nil {
		return Credentials{}, err
	}
	return creds, nil
}

// requestLoginState asks for one login state and the URL that confirms it.
func requestLoginState(ctx context.Context) (loginState, authURL string, err error) {
	endpoint := strings.TrimRight(workBuddyBaseURL, "/") + "/v2/plugin/auth/state?platform=" + url.QueryEscape(productPlatform)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("{}"))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	// The state request is the one call that carries no credential: these four
	// headers tell the gateway not to look for one.
	for _, header := range []string{"X-No-Authorization", "X-No-User-Id", "X-No-Enterprise-Id", "X-No-Department-Info"} {
		req.Header.Set(header, "true")
	}
	var answer struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			State   string `json:"state"`
			AuthURL string `json:"authUrl"`
		} `json:"data"`
	}
	if err := doJSON(req, &answer); err != nil {
		return "", "", err
	}
	if answer.Code != 0 {
		return "", "", fmt.Errorf("workbuddy: the login state request answered %d %s", answer.Code, strings.TrimSpace(answer.Msg))
	}
	if strings.TrimSpace(answer.Data.State) == "" || strings.TrimSpace(answer.Data.AuthURL) == "" {
		return "", "", errors.New("workbuddy: the login state request named no state or authorization URL")
	}
	return answer.Data.State, answer.Data.AuthURL, nil
}

// pollLoginToken polls the token endpoint until the browser half of the sign-in
// is confirmed, the deadline passes, or the context ends. The deadline is
// checked before each wait, and every wait listens to the context, so a canceled
// sign-in returns instead of polling on.
func pollLoginToken(ctx context.Context, loginState string) (tokenPayload, error) {
	deadline := time.Now().Add(loginTimeout)
	for {
		payload, pending, err := requestLoginToken(ctx, loginState)
		if err != nil {
			return tokenPayload{}, err
		}
		if !pending {
			return payload, nil
		}
		if time.Now().After(deadline) {
			return tokenPayload{}, errors.New("workbuddy: the browser sign-in did not finish in time")
		}
		if err := sleepContext(ctx, loginPollInterval); err != nil {
			return tokenPayload{}, err
		}
	}
}

// requestLoginToken asks once; pending says the browser half is unfinished.
func requestLoginToken(ctx context.Context, loginState string) (payload tokenPayload, pending bool, err error) {
	endpoint := strings.TrimRight(workBuddyBaseURL, "/") + "/v2/plugin/auth/token?state=" + url.QueryEscape(loginState)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return tokenPayload{}, false, err
	}
	req.Header.Set("Accept", "application/json")
	var answer struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := doJSON(req, &answer); err != nil {
		return tokenPayload{}, false, err
	}
	if answer.Code == tokenPendingCode {
		return tokenPayload{}, true, nil
	}
	if answer.Code != 0 {
		return tokenPayload{}, false, fmt.Errorf("workbuddy: the login token request answered %d %s", answer.Code, strings.TrimSpace(answer.Msg))
	}
	if len(answer.Data) > 0 && string(answer.Data) != "null" {
		if err := json.Unmarshal(answer.Data, &payload); err != nil {
			return tokenPayload{}, false, fmt.Errorf("workbuddy: login token answer: %w", err)
		}
	}
	return payload, false, nil
}

// account is the account a token belongs to, as /v2/plugin/account answers it.
type account struct {
	UID          string `json:"uid"`
	UIN          string `json:"uin"`
	Type         string `json:"type"`
	Nickname     string `json:"nickname"`
	EnterpriseID string `json:"enterpriseId"`
	IDSource     string `json:"idSource"`
}

// applyTo stamps the account onto the credential the identity headers are built
// from.
func (a account) applyTo(creds *Credentials) {
	creds.UID = strings.TrimSpace(a.UID)
	creds.Name = strings.TrimSpace(a.Nickname)
	creds.EnterpriseID = strings.TrimSpace(a.EnterpriseID)
	creds.IDSource = strings.TrimSpace(a.IDSource)
	creds.AccountType = strings.TrimSpace(a.Type)
}

// ensureAccountType answers a credential that says which kind of account it
// belongs to. A login stored before this field existed carries no type, and a
// missing type reads as "outside the paid line", which would leave the reader
// with the two identical rows this change exists to remove — so the account is
// asked once, and the answer is persisted under the credential lock (the same
// lock every other credential write takes).
//
// A failed read is not an error: the credential stands as it is and the model
// paths fall back to the outside-the-paid-line reading, which shows both rows
// with distinct names rather than guessing.
func ensureAccountType(ctx context.Context, db *sql.DB, home string, creds Credentials) Credentials {
	if strings.TrimSpace(creds.AccountType) != "" || strings.TrimSpace(creds.AccessToken) == "" {
		return creds
	}
	account, err := fetchAccount(ctx, creds.AccessToken)
	if err != nil {
		slog.Warn("workbuddy: the account type is unknown", "err", err)
		return creds
	}
	updated := creds
	account.applyTo(&updated)
	if strings.TrimSpace(updated.AccountType) == "" {
		slog.Warn("workbuddy: the account endpoint answered no type")
		return creds
	}
	mutex := llm.CredentialMutex(credentialPath(home))
	mutex.Lock()
	defer mutex.Unlock()
	// Re-read inside the lock: a refresh that landed while the account was
	// being read owns the newer token, and stamping this read's fields onto it
	// would put an older token back.
	stored, ok, err := loadCredentials(ctx, db, home)
	if err != nil || !ok {
		slog.Warn("workbuddy: could not re-read the login to record the account type", "err", err)
		return updated
	}
	stored.AccountType = updated.AccountType
	if err := saveCredentials(ctx, db, home, stored); err != nil {
		slog.Warn("workbuddy: could not record the account type", "err", err)
	}
	return stored
}

// fetchAccount reads the account the access token belongs to.
func fetchAccount(ctx context.Context, accessToken string) (account, error) {
	target, err := requestURL(workBuddyBaseURL, "/v2/plugin/account")
	if err != nil {
		return account{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return account{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	raw, err := doJSONBody(req)
	if err != nil {
		return account{}, err
	}
	var answer struct {
		Data json.RawMessage `json:"data"`
	}
	// The account endpoint's envelope is the family's ({code, msg, data}), but a
	// bare account object is read too: only a live login pins which one it
	// answers with.
	body := raw
	if json.Unmarshal(raw, &answer) == nil && len(answer.Data) > 0 && string(answer.Data) != "null" {
		body = answer.Data
	}
	var found account
	if err := json.Unmarshal(body, &found); err != nil {
		return account{}, fmt.Errorf("workbuddy: account answer: %w", err)
	}
	return found, nil
}

// EnsureFreshCredentials answers a credential good for the next request,
// refreshing it first when it is within a minute of expiry.
func EnsureFreshCredentials(ctx context.Context, db *sql.DB, home string) (Credentials, error) {
	return refreshCredentials(ctx, db, home, false)
}

// ForceRefreshCredentials refreshes unconditionally — the answer to a 401 from
// the gateway.
func ForceRefreshCredentials(ctx context.Context, db *sql.DB, home string) (Credentials, error) {
	return refreshCredentials(ctx, db, home, true)
}

// refreshCredentials refreshes under the shared per-path credential lock and
// re-reads the store inside it, so two concurrent turns refresh exactly once: a
// refresh token that rotates on use cannot be spent twice.
func refreshCredentials(ctx context.Context, db *sql.DB, home string, force bool) (Credentials, error) {
	mutex := llm.CredentialMutex(credentialPath(home))
	mutex.Lock()
	defer mutex.Unlock()

	creds, ok, err := loadCredentials(ctx, db, home)
	if err != nil {
		return Credentials{}, err
	}
	if !ok {
		return Credentials{}, signInRequired(nil)
	}
	if !force && !needsRefresh(creds, time.Now()) {
		return creds, nil
	}
	if strings.TrimSpace(creds.RefreshToken) == "" {
		return Credentials{}, signInRequired(nil)
	}
	fresh, err := refreshTokens(ctx, creds)
	if err != nil {
		return Credentials{}, err
	}
	if err := saveCredentials(ctx, db, home, fresh); err != nil {
		return Credentials{}, err
	}
	return fresh, nil
}

// needsRefresh reports whether the token is inside its last minute. A login
// whose expiry the server never stated is not refreshed on a guess.
func needsRefresh(creds Credentials, now time.Time) bool {
	if creds.ExpiresAt.IsZero() {
		return false
	}
	return now.After(creds.ExpiresAt.Add(-time.Minute))
}

// refreshTokens spends the refresh token on a new pair. The gateway answers 401
// or 403 when the login is gone, and both mean the same thing here.
func refreshTokens(ctx context.Context, creds Credentials) (Credentials, error) {
	target, err := requestURL(workBuddyBaseURL, "/v2/plugin/auth/token/refresh")
	if err != nil {
		return Credentials{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader("{}"))
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Refresh-Token", creds.RefreshToken)
	req.Header.Set("X-Auth-Refresh-Source", "plugin")
	req.Header.Set("X-Domain", domainOf(creds))

	var answer struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := doJSON(req, &answer); err != nil {
		return Credentials{}, err
	}
	if answer.Code != 0 {
		return Credentials{}, fmt.Errorf("workbuddy: the token refresh answered %d %s", answer.Code, strings.TrimSpace(answer.Msg))
	}
	var payload tokenPayload
	if len(answer.Data) > 0 && string(answer.Data) != "null" {
		if err := json.Unmarshal(answer.Data, &payload); err != nil {
			return Credentials{}, fmt.Errorf("workbuddy: token refresh answer: %w", err)
		}
	}
	refreshed := payload.credentials(time.Now())
	if refreshed.AccessToken == "" {
		return Credentials{}, errors.New("workbuddy: the token refresh carried no access token")
	}
	// A refresh that states no new lifetime or domain keeps the old one: the
	// pair is what a refresh sets out to replace.
	merged := creds
	merged.AccessToken = refreshed.AccessToken
	if refreshed.RefreshToken != "" {
		merged.RefreshToken = refreshed.RefreshToken
	}
	if !refreshed.ExpiresAt.IsZero() {
		merged.ExpiresAt = refreshed.ExpiresAt
	}
	if !refreshed.RefreshExpiresAt.IsZero() {
		merged.RefreshExpiresAt = refreshed.RefreshExpiresAt
	}
	if refreshed.SessionState != "" {
		merged.SessionState = refreshed.SessionState
	}
	if refreshed.Domain != "" {
		merged.Domain = refreshed.Domain
	}
	return merged, nil
}

// doJSON performs one request and decodes its answer, with 401/403 read as a
// login that is gone and any other non-200 as the gateway's own error.
func doJSON(req *http.Request, out any) error {
	raw, err := doJSONBody(req)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("workbuddy: %s answer: %w", req.URL.Path, err)
	}
	return nil
}

// doJSONBody performs one request and answers its body.
func doJSONBody(req *http.Request) ([]byte, error) {
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, modelListLimit))
	if err != nil {
		return nil, err
	}
	if isAuthStatus(resp.StatusCode) {
		return nil, signInRequired(&llm.APIError{StatusCode: resp.StatusCode, Message: "WorkBuddy sign-in expired"})
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &llm.APIError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("workbuddy: %s answered %s", req.URL.Path, strings.TrimSpace(string(raw))),
		}
	}
	return raw, nil
}

// isAuthStatus reports the statuses the gateway uses for "this login is gone":
// the WorkBuddy API answers 401, and 403 on the enterprise side.
func isAuthStatus(code int) bool {
	return code == http.StatusUnauthorized || code == http.StatusForbidden
}

// isUnauthorized reports whether err is the rejection the surfaces read as "the
// login is gone".
func isUnauthorized(err error) bool {
	var api *llm.APIError
	return errors.As(err, &api) && isAuthStatus(api.StatusCode)
}

// signInRequired is the 401 the surfaces read as "the login is gone; sign in
// again" (pkg/turn.SignInRejected).
func signInRequired(cause error) error {
	if cause != nil && isUnauthorized(cause) {
		return cause
	}
	return &llm.APIError{StatusCode: http.StatusUnauthorized, Message: "WorkBuddy sign-in required"}
}

// domainOf is the X-Domain the official client sends: the domain the login
// named, or the product host when it named none. It never routes — every request
// goes to the product endpoint — because an enterprise or SSO login's domain can
// be a sign-in host rather than the API's.
func domainOf(creds Credentials) string {
	if domain := strings.TrimSpace(creds.Domain); domain != "" {
		return domain
	}
	if parsed, err := url.Parse(strings.TrimSpace(workBuddyBaseURL)); err == nil {
		return parsed.Host
	}
	return ""
}

// sleepContext waits for d or the context's end, whichever comes first.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
