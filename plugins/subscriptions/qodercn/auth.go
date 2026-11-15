// auth.go owns the Qoder CN credential: where it lives (the Forebrain state
// store, with a file fallback), how it is obtained (a PKCE browser device flow
// that never disturbs the official client's own session, a personal access
// token exchange, or a read-only import of the official client's login), and
// how it stays fresh (single-writer refresh under the shared credential lock).
//
// The official client's files under ~/.qoder-cn are probed READ-ONLY: this
// plugin never writes, moves or deletes anything there, so a Qoder CN desktop
// session running beside Forebrain is never disturbed.
package qodercn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
const credentialSection = "auth.qodercn"

// personalTokenEnv is the personal access token a user may pre-provision.
const personalTokenEnv = "QODER_PERSONAL_ACCESS_TOKEN"

// Credentials is one stored Qoder CN login. AccessToken and RefreshToken are
// the device (or job) token pair; EncryptUserInfo and CosyKey are the
// per-login runtime auth fields the gateway's algo service requires on every
// request.
type Credentials struct {
	UID              string    `json:"uid"`
	Name             string    `json:"name,omitempty"`
	Email            string    `json:"email,omitempty"`
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	LoginMethod      string    `json:"login_method,omitempty"` // browser | job_token
	ExpiresAt        time.Time `json:"expires_at"`
	OrganizationID   string    `json:"organization_id,omitempty"`
	OrganizationTags []string  `json:"organization_tags,omitempty"`
	EncryptUserInfo  string    `json:"encrypt_user_info,omitempty"`
	CosyKey          string    `json:"key,omitempty"`
}

// credentialPath is the file fallback (and the path the shared credential lock
// is keyed on).
func credentialPath(home string) string {
	return filepath.Join(home, "qodercn.json")
}

// loadCredentials reads the stored login. Both homes it can live in — the
// state store and the credential file — may hold one, and the fresher wins:
// the run-time transport writes the file (it has no state handle), the
// subscription surface writes the store, and a refresh from either must not
// leave the other's older token behind.
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
				return Credentials{}, false, fmt.Errorf("qodercn: stored credential: %w", err)
			}
			haveStored = true
		}
	}
	if raw, err := os.ReadFile(credentialPath(home)); err == nil {
		if err := json.Unmarshal(raw, &filed); err != nil {
			return Credentials{}, false, fmt.Errorf("qodercn: credential file: %w", err)
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

// saveCredentials writes the login to both places loadCredentials reads — a
// nil DB leaves only the credential file (0600: it holds tokens).
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
	return os.WriteFile(credentialPath(home), raw, 0o600)
}

// ensureRuntimeAuthFields fills the per-login algo fields when they are
// missing (a login, an import, or a refresh just produced a new token).
func ensureRuntimeAuthFields(creds *Credentials) error {
	if creds.EncryptUserInfo != "" && creds.CosyKey != "" {
		return nil
	}
	info, key, err := GenerateRuntimeAuthFields(RuntimeAuthInput{
		UID:              creds.UID,
		SecurityOAuthTok: creds.AccessToken,
		OrganizationID:   creds.OrganizationID,
		OrganizationTags: creds.OrganizationTags,
		DataPolicyAgreed: true,
	})
	if err != nil {
		return err
	}
	creds.EncryptUserInfo, creds.CosyKey = info, key
	return nil
}

// tokenResponse is the flexible shape both the device poll and the PAT
// exchange answer with (field spellings vary by endpoint and client version).
type tokenResponse struct {
	Token         json.RawMessage `json:"token"`
	DeviceToken   json.RawMessage `json:"device_token"`
	AccessToken   json.RawMessage `json:"access_token"`
	RefreshToken  string          `json:"refresh_token"`
	UserID        string          `json:"user_id"`
	UserName      string          `json:"user_name"`
	ExpiresAt     json.RawMessage `json:"expires_at"`
	ExpireTime    json.RawMessage `json:"expire_time"`
	ExpiresIn     int64           `json:"expires_in"`
	RefreshExpiry json.RawMessage `json:"refresh_token_expires_at"`
}

func firstRaw(values ...json.RawMessage) json.RawMessage {
	for _, v := range values {
		if len(v) > 0 && string(v) != "null" && string(v) != `""` {
			return v
		}
	}
	return nil
}

// rawString reads a JSON value that may be a string, a number or nothing.
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

// parseTime reads the protocol's three timestamp spellings: RFC3339 text,
// seconds, or milliseconds.
func parseTime(raw json.RawMessage, fallbackIn int64, from time.Time) time.Time {
	if len(raw) > 0 {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			if t, err := time.Parse(time.RFC3339, strings.TrimSpace(text)); err == nil {
				return t
			}
		}
		var number json.Number
		if json.Unmarshal(raw, &number) == nil {
			if secs, err := number.Int64(); err == nil {
				if secs > 1_000_000_000_000 { // milliseconds
					return time.UnixMilli(secs)
				}
				if secs > 1_000_000_000 {
					return time.Unix(secs, 0)
				}
			}
		}
	}
	if fallbackIn > 0 {
		return from.Add(time.Duration(fallbackIn) * time.Second)
	}
	return time.Time{}
}

// credentialsFromTokenResponse folds a poll/exchange answer into the stored
// credential.
func credentialsFromTokenResponse(resp *tokenResponse, method string, now time.Time) (Credentials, error) {
	token := rawString(firstRaw(resp.Token, resp.DeviceToken, resp.AccessToken))
	if token == "" {
		return Credentials{}, errors.New("qodercn: the answer carried no token")
	}
	creds := Credentials{
		AccessToken:  token,
		RefreshToken: strings.TrimSpace(resp.RefreshToken),
		LoginMethod:  method,
		UID:          strings.TrimSpace(resp.UserID),
		Name:         strings.TrimSpace(resp.UserName),
		ExpiresAt:    parseTime(firstRaw(resp.ExpiresAt, resp.ExpireTime), resp.ExpiresIn, now),
	}
	if creds.UID == "" {
		creds.UID = "pending"
	}
	return creds, nil
}

// httpDoer is the transport seam (tests inject an httptest.Server by pointing
// GatewayBaseURL at it; the seam keeps the client itself fixed).
var httpClient = &http.Client{Timeout: 30 * time.Second}

// limitedJSON reads a response body as JSON, capped: an auth answer is small,
// and an unbounded read trusts the network more than it deserves.
func limitedJSON(resp *http.Response, cap int64) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, cap))
}

// LoginPKCEDeviceFlow runs the browser device flow: it hands the authorization
// URL to onURL immediately (a surface opens it — or shows it), then polls once
// a second for up to five minutes until the person confirms in the browser.
// The token it receives is Forebrain's own device token: the official client's
// session under ~/.qoder-cn is never read for write, replaced or invalidated.
func LoginPKCEDeviceFlow(ctx context.Context, db *sql.DB, home string, onURL func(string)) (Credentials, error) {
	identity := LoadOrCreateMachineIdentity(home)

	verifier, err := randomVerifier()
	if err != nil {
		return Credentials{}, err
	}
	challenge := base64.RawURLEncoding.EncodeToString(sha256Sum(verifier))
	nonce, err := newUUIDv4()
	if err != nil {
		return Credentials{}, err
	}

	query := url.Values{}
	query.Set("challenge", challenge)
	query.Set("challenge_method", "S256")
	query.Set("nonce", nonce)
	query.Set("machine_id", identity.MachineID)
	query.Set("client_id", OAuthClientID)
	if identity.MachineToken != "" && identity.MachineToken != identity.MachineID {
		query.Set("machine_token", identity.MachineToken)
	}
	authURL := strings.TrimRight(AuthPageBaseURL, "/") + "/device/selectAccounts?" + query.Encode()
	if onURL != nil {
		onURL(authURL)
	}

	pollURL := strings.TrimRight(GatewayBaseURL, "/") + "/api/v1/deviceToken/poll?" + url.Values{
		"nonce":            {nonce},
		"verifier":         {verifier},
		"challenge_method": {"S256"},
	}.Encode()

	deadline := time.Now().Add(5 * time.Minute)
	for {
		resp, err := deviceTokenPoll(ctx, pollURL, identity)
		if err != nil {
			return Credentials{}, err
		}
		switch resp.StatusCode {
		case http.StatusOK:
			body, err := limitedJSON(resp, 1<<20)
			if err != nil {
				return Credentials{}, err
			}
			var tokenResp tokenResponse
			if err := json.Unmarshal(body, &tokenResp); err != nil {
				return Credentials{}, fmt.Errorf("qodercn: device token answer: %w", err)
			}
			creds, err := credentialsFromTokenResponse(&tokenResp, "browser", time.Now())
			if err != nil {
				return Credentials{}, err
			}
			if err := completeUserInfo(ctx, &creds); err != nil {
				return Credentials{}, err
			}
			if err := ensureRuntimeAuthFields(&creds); err != nil {
				return Credentials{}, err
			}
			if err := saveCredentials(ctx, db, home, creds); err != nil {
				return Credentials{}, err
			}
			return creds, nil
		case http.StatusNotFound:
			// The person has not confirmed yet; keep waiting.
		default:
			body, _ := limitedJSON(resp, 1<<20)
			return Credentials{}, &llm.APIError{
				StatusCode: resp.StatusCode,
				Message:    fmt.Sprintf("qodercn: device token poll answered %s", strings.TrimSpace(string(body))),
			}
		}
		if time.Now().After(deadline) {
			return Credentials{}, errors.New("qodercn: the browser sign-in was not confirmed in time")
		}
		if err := sleepContext(ctx, time.Second); err != nil {
			return Credentials{}, err
		}
	}
}

// deviceTokenPoll sends one poll request wearing the client identity headers.
func deviceTokenPoll(ctx context.Context, pollURL string, identity MachineIdentity) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Cosy-Version", CosyVersion)
	req.Header.Set("Cosy-ClientType", ClientType)
	req.Header.Set("Cosy-MachineOS", MachineOS())
	req.Header.Set("Cosy-MachineId", identity.MachineID)
	req.Header.Set("Cosy-MachineToken", identity.MachineToken)
	req.Header.Set("Cosy-MachineType", identity.MachineType)
	return httpClient.Do(req)
}

// exchangePersonalToken trades QODER_PERSONAL_ACCESS_TOKEN for device tokens.
func exchangePersonalToken(ctx context.Context, identity MachineIdentity, pat string) (Credentials, error) {
	payload, err := json.Marshal(map[string]string{
		"personal_token": pat,
		"machine_id":     identity.MachineID,
		"machine_token":  identity.MachineToken,
	})
	if err != nil {
		return Credentials{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(GatewayBaseURL, "/")+"/api/v1/jobToken/exchange", strings.NewReader(string(payload)))
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Cosy-Version", CosyVersion)
	req.Header.Set("Cosy-ClientType", ClientType)
	req.Header.Set("Cosy-MachineOS", MachineOS())
	resp, err := httpClient.Do(req)
	if err != nil {
		return Credentials{}, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := limitedJSON(resp, 1<<20)
		return Credentials{}, &llm.APIError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("qodercn: token exchange answered %s", strings.TrimSpace(string(body))),
		}
	}
	body, err := limitedJSON(resp, 1<<20)
	if err != nil {
		return Credentials{}, err
	}
	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return Credentials{}, fmt.Errorf("qodercn: token exchange answer: %w", err)
	}
	return credentialsFromTokenResponse(&tokenResp, "job_token", time.Now())
}

// userInfoResponse is the subset of /api/v1/userinfo the credential keeps.
type userInfoResponse struct {
	UID  json.RawMessage `json:"uid"`
	ID   json.RawMessage `json:"id"`
	Name string          `json:"name"`
	//nolint:revive // the wire field is the client's spelling, not ours
	UserName string `json:"username"`
	Email    string `json:"email"`
	OrgID    string `json:"orgId"`
	//nolint:revive // ditto
	OrganizationID string `json:"organization_id"`
}

// completeUserInfo fills the user identity fields (uid, name, organization)
// from the gateway — the token answer alone does not carry them reliably.
func completeUserInfo(ctx context.Context, creds *Credentials) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(GatewayBaseURL, "/")+"/api/v1/userinfo", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Cosy-Version", CosyVersion)
	req.Header.Set("Cosy-ClientType", ClientType)
	req.Header.Set("Cosy-MachineOS", MachineOS())
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := limitedJSON(resp, 1<<20)
		return &llm.APIError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("qodercn: userinfo answered %s", strings.TrimSpace(string(body))),
		}
	}
	body, err := limitedJSON(resp, 1<<20)
	if err != nil {
		return err
	}
	var info userInfoResponse
	if err := json.Unmarshal(body, &info); err != nil {
		return fmt.Errorf("qodercn: userinfo answer: %w", err)
	}
	if uid := rawString(firstRaw(info.UID, info.ID)); uid != "" {
		creds.UID = uid
	}
	if info.Name != "" {
		creds.Name = info.Name
	} else if info.UserName != "" {
		creds.Name = info.UserName
	}
	creds.Email = strings.TrimSpace(info.Email)
	creds.OrganizationID = strings.TrimSpace(firstNonEmpty(info.OrgID, info.OrganizationID))
	if creds.OrganizationID != "" {
		if tags, err := organizationTags(ctx, creds.AccessToken, creds.OrganizationID); err == nil {
			creds.OrganizationTags = tags
		}
	}
	return nil
}

// organizationTags reads the org's tag list; a failure is ignored (the header
// is optional).
func organizationTags(ctx context.Context, token, orgID string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(GatewayBaseURL, "/")+"/api/v1/organizations/"+url.PathEscape(orgID)+"/tags", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", UserAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = limitedJSON(resp, 1<<20)
		return nil, fmt.Errorf("tags answered %d", resp.StatusCode)
	}
	body, err := limitedJSON(resp, 1<<20)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload.Tags, nil
}

// localQoderUser is the decrypted shape of the official client's
// ~/.qoder-cn/.auth/user. Only the fields a login needs are read; anything the
// official client keeps for itself is left untouched on disk.
type localQoderUser struct {
	Token          json.RawMessage `json:"token"`
	DeviceToken    json.RawMessage `json:"device_token"`
	AccessToken    json.RawMessage `json:"access_token"`
	RefreshToken   string          `json:"refresh_token"`
	UID            string          `json:"uid"`
	UserID         string          `json:"user_id"`
	LoginMethod    string          `json:"login_method"`
	ExpireTime     json.RawMessage `json:"expire_time"`
	ExpiresAt      json.RawMessage `json:"expires_at"`
	EncryptInfo    string          `json:"encrypt_user_info"`
	CosyKey        string          `json:"key"`
	OrganizationID string          `json:"organization_id"`
}

// tryAdoptLocalQoderCNCredentials resolves the credential Forebrain should
// use, in order: a stored Forebrain login, a configured personal access token,
// or the official client's own login imported read-only. It never writes
// anything under ~/.qoder-cn.
func tryAdoptLocalQoderCNCredentials(ctx context.Context, db *sql.DB, home string) (Credentials, bool, error) {
	if creds, ok, err := loadCredentials(ctx, db, home); err != nil || ok {
		return creds, ok, err
	}
	identity := LoadOrCreateMachineIdentity(home)
	if pat := strings.TrimSpace(os.Getenv(personalTokenEnv)); pat != "" {
		creds, err := exchangePersonalToken(ctx, identity, pat)
		if err != nil {
			return Credentials{}, false, err
		}
		if err := completeUserInfo(ctx, &creds); err != nil {
			return Credentials{}, false, err
		}
		if err := ensureRuntimeAuthFields(&creds); err != nil {
			return Credentials{}, false, err
		}
		if err := saveCredentials(ctx, db, home, creds); err != nil {
			return Credentials{}, false, err
		}
		return creds, true, nil
	}
	if creds, ok := importOfficialLogin(identity); ok {
		if err := ensureRuntimeAuthFields(&creds); err != nil {
			return Credentials{}, false, err
		}
		if err := saveCredentials(ctx, db, home, creds); err != nil {
			return Credentials{}, false, err
		}
		return creds, true, nil
	}
	return Credentials{}, false, nil
}

// importOfficialLogin decrypts the official client's credential file with its
// own key (machine id prefix) and answers whether it holds a usable login.
func importOfficialLogin(identity MachineIdentity) (Credentials, bool) {
	root := QoderHomeDir()
	if root == "" || identity.MachineID == "" {
		return Credentials{}, false
	}
	encrypted, err := os.ReadFile(filepath.Join(root, ".auth", "user"))
	if err != nil || len(encrypted) == 0 {
		return Credentials{}, false
	}
	key16 := identity.MachineID
	if len(key16) > 16 {
		key16 = key16[:16]
	}
	plain, err := CredentialDecrypt(string(encrypted), key16)
	if err != nil {
		return Credentials{}, false
	}
	var user localQoderUser
	if err := json.Unmarshal([]byte(plain), &user); err != nil {
		return Credentials{}, false
	}
	token := rawString(firstRaw(user.Token, user.DeviceToken, user.AccessToken))
	if token == "" {
		return Credentials{}, false
	}
	creds := Credentials{
		AccessToken:     token,
		RefreshToken:    strings.TrimSpace(user.RefreshToken),
		LoginMethod:     strings.TrimSpace(user.LoginMethod),
		UID:             strings.TrimSpace(firstNonEmpty(user.UID, user.UserID)),
		ExpiresAt:       parseTime(firstRaw(user.ExpiresAt, user.ExpireTime), 0, time.Now()),
		EncryptUserInfo: strings.TrimSpace(user.EncryptInfo),
		CosyKey:         strings.TrimSpace(user.CosyKey),
		OrganizationID:  strings.TrimSpace(user.OrganizationID),
	}
	// A login whose refresh token is also gone cannot be kept fresh; adopting
	// it would only produce a dead row.
	if creds.RefreshToken == "" {
		return Credentials{}, false
	}
	if creds.UID == "" {
		creds.UID = "adopted"
	}
	return creds, true
}

// refreshResponse is the answer both refresh endpoints give.
type refreshResponse struct {
	Token        json.RawMessage `json:"token"`
	DeviceToken  json.RawMessage `json:"device_token"`
	AccessToken  json.RawMessage `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	ExpiresAt    json.RawMessage `json:"expires_at"`
	ExpireTime   json.RawMessage `json:"expire_time"`
}

// EnsureFreshCredentials answers a credential good for the next request,
// refreshing it first when it is within a minute of expiry. Refreshes are
// serialized under the shared per-path credential lock, and the store is
// re-read inside the lock, so two concurrent turns refresh exactly once.
func EnsureFreshCredentials(ctx context.Context, db *sql.DB, home string) (Credentials, error) {
	return refreshCredentials(ctx, db, home, false)
}

// ForceRefreshCredentials refreshes unconditionally — the answer to a 401 from
// the gateway.
func ForceRefreshCredentials(ctx context.Context, db *sql.DB, home string) (Credentials, error) {
	return refreshCredentials(ctx, db, home, true)
}

func refreshCredentials(ctx context.Context, db *sql.DB, home string, force bool) (Credentials, error) {
	lockPath := credentialPath(home)
	llm.CredentialMutex(lockPath).Lock()
	defer llm.CredentialMutex(lockPath).Unlock()

	creds, ok, err := loadCredentials(ctx, db, home)
	if err != nil {
		return Credentials{}, err
	}
	if !ok {
		adopted, ok, err := tryAdoptLocalQoderCNCredentials(ctx, db, home)
		if err != nil || !ok {
			return Credentials{}, signInRequired(err)
		}
		creds = adopted
	}
	if !force && !needsRefresh(creds, time.Now()) {
		return creds, nil
	}
	if creds.RefreshToken == "" {
		return Credentials{}, signInRequired(nil)
	}
	fresh, err := refreshTokens(ctx, creds, LoadOrCreateMachineIdentity(home))
	if err != nil {
		return Credentials{}, err
	}
	// The runtime auth fields seal the access token, so a new token means new
	// fields.
	fresh.EncryptUserInfo, fresh.CosyKey = "", ""
	if err := ensureRuntimeAuthFields(&fresh); err != nil {
		return Credentials{}, err
	}
	if err := saveCredentials(ctx, db, home, fresh); err != nil {
		return Credentials{}, err
	}
	return fresh, nil
}

// needsRefresh reports whether the token is inside its last minute.
func needsRefresh(creds Credentials, now time.Time) bool {
	if creds.ExpiresAt.IsZero() {
		return false
	}
	return now.After(creds.ExpiresAt.Add(-time.Minute))
}

func refreshTokens(ctx context.Context, creds Credentials, identity MachineIdentity) (Credentials, error) {
	endpoint := "/api/v1/deviceToken/refresh"
	payload := map[string]string{"refresh_token": creds.RefreshToken}
	if creds.LoginMethod == "job_token" || creds.LoginMethod == "token" {
		endpoint = "/api/v1/jobToken/refresh"
	} else {
		payload["machine_id"] = identity.MachineID
		payload["machine_token"] = identity.MachineToken
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Credentials{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(GatewayBaseURL, "/")+endpoint, strings.NewReader(string(body)))
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Cosy-Version", CosyVersion)
	req.Header.Set("Cosy-ClientType", ClientType)
	req.Header.Set("Cosy-MachineOS", MachineOS())
	resp, err := httpClient.Do(req)
	if err != nil {
		return Credentials{}, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_, _ = limitedJSON(resp, 1<<20)
		return Credentials{}, signInRequired(nil)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := limitedJSON(resp, 1<<20)
		return Credentials{}, &llm.APIError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("qodercn: token refresh answered %s", strings.TrimSpace(string(raw))),
		}
	}
	raw, err := limitedJSON(resp, 1<<20)
	if err != nil {
		return Credentials{}, err
	}
	var answer refreshResponse
	if err := json.Unmarshal(raw, &answer); err != nil {
		return Credentials{}, fmt.Errorf("qodercn: token refresh answer: %w", err)
	}
	token := rawString(firstRaw(answer.Token, answer.DeviceToken, answer.AccessToken))
	if token == "" {
		return Credentials{}, errors.New("qodercn: token refresh carried no token")
	}
	fresh := creds
	fresh.AccessToken = token
	if answer.RefreshToken != "" {
		fresh.RefreshToken = strings.TrimSpace(answer.RefreshToken)
	}
	fresh.ExpiresAt = parseTime(firstRaw(answer.ExpiresAt, answer.ExpireTime), 3600, time.Now())
	return fresh, nil
}

// signInRequired is the 401 the surfaces read as "the login is gone; sign in
// again" (pkg/turn.SignInRejected).
func signInRequired(cause error) error {
	if cause != nil {
		var api *llm.APIError
		if errors.As(cause, &api) && api.StatusCode == http.StatusUnauthorized {
			return cause
		}
	}
	return &llm.APIError{StatusCode: http.StatusUnauthorized, Message: "Qoder CN sign-in required"}
}

// randomVerifier is the PKCE verifier: 64 characters of the unreserved set.
func randomVerifier() (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	raw := make([]byte, 64)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	out := make([]byte, 64)
	for i, b := range raw {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

func sha256Sum(data string) []byte {
	sum := sha256.Sum256([]byte(data))
	return sum[:]
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
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
