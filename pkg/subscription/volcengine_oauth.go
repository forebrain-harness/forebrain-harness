// Volcengine Console's OAuth 2.0 device-code sign-in (RFC 8628) and the STS
// credential it issues.
//
// The console hands back an `access_token` that is itself a JSON document with
// an access key, a secret and a session token — the same triple any Volcengine
// SDK signs with — plus a refresh token that renews it with the client id the
// sign-in started under. Nothing here exchanges a token for STS a second time,
// and nothing here reads an API key: the key a plan's row carries is the user's
// to paste (see volcengine.go).
package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

const (
	// volcengineSignInEndpoint is the console sign-in host, the one the official
	// CLI signs in to.
	volcengineSignInEndpoint = "https://signin.volcengine.com"
	volcengineDeviceAuthPath = "/authorize/oauth/device_authorization"
	volcengineTokenPath      = "/authorize/oauth/token"
	// volcengineClientID is the public client the device-code flow belongs to.
	// A refresh has to replay the same one the sign-in used.
	volcengineClientID = "trn:signin:::devtools/cross-device"
	volcengineScope    = "Console:All:All"
	// volcengineDeviceInfo is what the console lists this device as.
	volcengineDeviceInfo = "Forebrain Harness"
	// volcengineCredentialSection is the fb_config credential row the sign-in is
	// stored in, beside auth.chatgpt and auth.opencode.
	volcengineCredentialSection = "auth.volcengine"

	volcengineDefaultInterval   = 5 * time.Second
	volcengineSlowDownIncrement = 5 * time.Second
	// volcengineTransientBudget is how many consecutive blips (a network error,
	// a 5xx, a decode failure) the poll loop rides out before giving up.
	volcengineTransientBudget = 5
	// volcengineExpiryMargin is how long before the STS expires it is renewed:
	// the console issues about 15 minutes, and a request must not race the end.
	volcengineExpiryMargin = 60 * time.Second
)

// The two ends the poll loop reaches on its own. Their wording carries the
// words the surfaces map ("denied", "expired"), because a person reading either
// one has the same next step: sign in again, or stop.
var (
	errVolcengineDenied      = errors.New("device authorization was denied")
	errVolcengineCodeExpired = errors.New("the device code is invalid or expired; sign in again")
	// errVolcengineExpired is a login that ran out and holds nothing to renew
	// it: only a new sign-in restores it, so it is dead rather than unreachable.
	errVolcengineExpired = errors.New("the Volcengine sign-in expired and holds no refresh token; sign in again")
)

// volcengineDeviceAuth is the console's answer to a device authorization
// request: the code the person confirms, where they confirm it, and how long
// the poll has.
type volcengineDeviceAuth struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval,omitempty"`
}

// browserURL is the page to open: the prefilled one when the console issued it,
// because that signs the code in for the user, and the plain one otherwise.
func (d volcengineDeviceAuth) browserURL() string {
	if complete := strings.TrimSpace(d.VerificationURIComplete); complete != "" {
		return complete
	}
	return strings.TrimSpace(d.VerificationURI)
}

// volcengineTokenResponse is one token exchange's answer. AccessToken is a JSON
// string holding the STS triple, which parseVolcengineSTS unwraps.
type volcengineTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	IDToken      string `json:"id_token"`
}

// volcengineCredential is the stored sign-in: the STS the console issued, the
// window it is valid for, and the refresh token that renews it. It is the row
// under auth.volcengine and never leaves that row except to sign a request.
type volcengineCredential struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token"`
	// IssuedAt and ExpiresAt are unix seconds: the console states a lifetime
	// rather than an end, and both are kept so the margin is plain.
	IssuedAt     int64  `json:"issued_at"`
	ExpiresAt    int64  `json:"expires_at"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// creds is the request credential this login signs with.
func (c volcengineCredential) creds() volcengineCreds {
	return volcengineCreds{
		AccessKeyID:     c.AccessKeyID,
		SecretAccessKey: c.SecretAccessKey,
		SessionToken:    c.SessionToken,
	}
}

// valid reports whether the STS still has time to spare. The console's own CLI
// keeps a 60-second margin, and a request that starts inside it would as soon
// be refused as answered.
func (c volcengineCredential) valid(now time.Time) bool {
	if c.creds().empty() || c.ExpiresAt <= 0 {
		return false
	}
	return now.UTC().Before(time.Unix(c.ExpiresAt, 0).Add(-volcengineExpiryMargin))
}

// volcengineSignIn drives the console's two endpoints. The transport, the
// endpoint, the clock and the wait are fields so a test drives the whole flow
// against an httptest.Server without waiting out the real intervals.
type volcengineSignIn struct {
	Transport http.RoundTripper
	Endpoint  string
	Now       func() time.Time
	Sleep     func(ctx context.Context, d time.Duration) error
}

func (c *volcengineSignIn) transport() http.RoundTripper {
	if c != nil && c.Transport != nil {
		return c.Transport
	}
	return http.DefaultTransport
}

func (c *volcengineSignIn) endpoint() string {
	if c != nil && strings.TrimSpace(c.Endpoint) != "" {
		return strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	}
	return volcengineSignInEndpoint
}

func (c *volcengineSignIn) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *volcengineSignIn) sleep(ctx context.Context, d time.Duration) error {
	if c != nil && c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// volcengineOAuthRefusal is a non-2xx answer from either sign-in endpoint: the
// OAuth code the poll loop branches on, or the reason a start failed.
type volcengineOAuthRefusal struct {
	Status      int
	Code        string
	Description string
}

func (e *volcengineOAuthRefusal) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("signin.volcengine.com answered HTTP %d", e.Status)
	}
	return "signin.volcengine.com answered " + e.Code
}

// startDevice asks the console for a code to confirm.
func (c *volcengineSignIn) startDevice(ctx context.Context) (volcengineDeviceAuth, error) {
	form := url.Values{}
	form.Set("client_id", volcengineClientID)
	form.Set("scope", volcengineScope)
	form.Set("device_info", volcengineDeviceInfo)
	var device volcengineDeviceAuth
	if err := c.postForm(ctx, volcengineDeviceAuthPath, form, &device); err != nil {
		return volcengineDeviceAuth{}, err
	}
	if strings.TrimSpace(device.DeviceCode) == "" || strings.TrimSpace(device.UserCode) == "" ||
		strings.TrimSpace(device.VerificationURI) == "" || device.ExpiresIn <= 0 {
		return volcengineDeviceAuth{}, errors.New("the Volcengine sign-in answered an incomplete device code")
	}
	return device, nil
}

// poll waits for the person to confirm the code and takes the credential the
// console issues. One HTTP request per round: the loop owns the interval and
// the slow_down backpressure, so a retrying transport would only break it.
func (c *volcengineSignIn) poll(ctx context.Context, device volcengineDeviceAuth) (volcengineCredential, error) {
	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = volcengineDefaultInterval
	}
	deadline := c.now().Add(time.Duration(device.ExpiresIn) * time.Second)
	transient := 0
	for {
		now := c.now()
		if !now.Before(deadline) {
			return volcengineCredential{}, errVolcengineCodeExpired
		}
		wait := interval
		if remaining := deadline.Sub(now); wait > remaining {
			wait = remaining
		}
		if err := c.sleep(ctx, wait); err != nil {
			return volcengineCredential{}, err
		}
		token, err := c.exchange(ctx, volcengineTokenPath, volcengineTokenForm("device_code", device.DeviceCode))
		if err == nil {
			return credentialFromToken(token, c.now())
		}
		code, refused := volcengineRefusalCode(err)
		switch code {
		case "authorization_pending":
			transient = 0
			continue
		case "slow_down":
			transient = 0
			interval += volcengineSlowDownIncrement
			continue
		case "access_denied":
			return volcengineCredential{}, errVolcengineDenied
		case "expired_token", "invalid_device_code":
			return volcengineCredential{}, errVolcengineCodeExpired
		case "server_error", "temporarily_unavailable":
		default:
			if refused {
				// A code the vocabulary above does not name is the console's
				// own refusal, and waiting on it cannot change the answer.
				return volcengineCredential{}, err
			}
		}
		transient++
		if transient > volcengineTransientBudget {
			return volcengineCredential{}, err
		}
	}
}

// refresh renews an expired STS with the refresh token, replaying the client id
// and scope the sign-in used: the console issues the credential to that client.
func (c *volcengineSignIn) refresh(ctx context.Context, current volcengineCredential) (volcengineCredential, error) {
	if strings.TrimSpace(current.RefreshToken) == "" {
		return volcengineCredential{}, errVolcengineExpired
	}
	form := volcengineTokenForm("refresh_token", strings.TrimSpace(current.RefreshToken))
	// The console compares the client id the credential was issued to, so an
	// old record without one still refreshes under the public client.
	clientID := strings.TrimSpace(current.ClientID)
	if clientID == "" {
		clientID = volcengineClientID
	}
	form.Set("client_id", clientID)
	scope := strings.TrimSpace(current.Scope)
	if scope == "" {
		scope = volcengineScope
	}
	form.Set("scope", scope)
	token, err := c.exchange(ctx, volcengineTokenPath, form)
	if err != nil {
		return volcengineCredential{}, err
	}
	renewed, err := credentialFromToken(token, c.now())
	if err != nil {
		return volcengineCredential{}, err
	}
	renewed.ClientID, renewed.Scope = clientID, scope
	// The console may or may not rotate the refresh token; the one it just
	// issued wins, and the old one stays usable when it issued none.
	if strings.TrimSpace(renewed.RefreshToken) == "" {
		renewed.RefreshToken = current.RefreshToken
	}
	return renewed, nil
}

// volcengineTokenForm is the token endpoint's form for one grant: the grant, its
// one input, and the client the flow belongs to.
func volcengineTokenForm(grant, value string) url.Values {
	form := url.Values{}
	form.Set("grant_type", grant)
	form.Set("client_id", volcengineClientID)
	form.Set("scope", volcengineScope)
	if grant == "refresh_token" {
		form.Set("refresh_token", value)
		return form
	}
	form.Set("device_code", value)
	return form
}

// exchange posts one token grant with a single attempt.
func (c *volcengineSignIn) exchange(ctx context.Context, path string, form url.Values) (volcengineTokenResponse, error) {
	var token volcengineTokenResponse
	if err := c.postForm(ctx, path, form, &token); err != nil {
		return volcengineTokenResponse{}, err
	}
	if strings.TrimSpace(token.AccessToken) == "" && strings.TrimSpace(token.RefreshToken) == "" {
		return volcengineTokenResponse{}, errors.New("the Volcengine token answer was empty")
	}
	return token, nil
}

// postForm posts one form to the console and decodes the JSON answer. A non-2xx
// answer is the OAuth refusal it carries, as a value the poll loop reads.
func (c *volcengineSignIn) postForm(ctx context.Context, path string, form url.Values, out any) error {
	body := form.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint()+path, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.transport().RoundTrip(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, volcengineMaxBody))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 == 2 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("signin.volcengine.com: unreadable answer: %w", err)
		}
		return nil
	}
	refusal := &volcengineOAuthRefusal{Status: resp.StatusCode}
	var answer struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &answer); err == nil {
		refusal.Code = strings.TrimSpace(answer.Error)
		// The description is the console's own wording and is shown to the
		// person waiting; it never carries the credential (none is issued yet).
		refusal.Description = strings.TrimSpace(answer.ErrorDescription)
	}
	return refusal
}

// volcengineRefusalCode reads the OAuth code out of a failed exchange. refused
// is false for a transport error, which says nothing about the sign-in itself.
func volcengineRefusalCode(err error) (code string, refused bool) {
	var refusal *volcengineOAuthRefusal
	if errors.As(err, &refusal) {
		return refusal.Code, refusal.Code != ""
	}
	return "", false
}

// credentialFromToken turns one token answer into the stored sign-in: the STS
// the access token embeds, and the refresh token that renews it.
func credentialFromToken(token volcengineTokenResponse, now time.Time) (volcengineCredential, error) {
	creds, err := parseVolcengineSTS(token.AccessToken)
	if err != nil {
		return volcengineCredential{}, err
	}
	issued := now.UTC()
	lifetime := token.ExpiresIn
	if lifetime <= 0 {
		lifetime = 900
	}
	return volcengineCredential{
		AccessKeyID:     creds.AccessKeyID,
		SecretAccessKey: creds.SecretAccessKey,
		SessionToken:    creds.SessionToken,
		IssuedAt:        issued.Unix(),
		ExpiresAt:       issued.Add(time.Duration(lifetime) * time.Second).Unix(),
		RefreshToken:    strings.TrimSpace(token.RefreshToken),
		ClientID:        volcengineClientID,
		Scope:           volcengineScope,
	}, nil
}

// parseVolcengineSTS unwraps the access_token: a JSON string that itself holds
// the STS triple. The token is never part of an error.
func parseVolcengineSTS(accessToken string) (volcengineCreds, error) {
	raw := strings.TrimSpace(accessToken)
	if raw == "" {
		return volcengineCreds{}, errors.New("the sign-in answer carried no access token")
	}
	var embedded string
	if err := json.Unmarshal([]byte(raw), &embedded); err == nil && strings.TrimSpace(embedded) != "" {
		raw = embedded
	}
	var sts struct {
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
		SessionToken    string `json:"session_token"`
	}
	if err := json.Unmarshal([]byte(raw), &sts); err != nil {
		return volcengineCreds{}, errors.New("the sign-in answer's access token is not a credential")
	}
	creds := volcengineCreds{
		AccessKeyID:     strings.TrimSpace(sts.AccessKeyID),
		SecretAccessKey: strings.TrimSpace(sts.SecretAccessKey),
		SessionToken:    strings.TrimSpace(sts.SessionToken),
	}
	if creds.empty() {
		return volcengineCreds{}, errors.New("the sign-in answer's access token is not a credential")
	}
	return creds, nil
}

// volcengineStore is the credential row's home: the same fb_config table the
// other two sign-ins live in, so one database holds every login.
func volcengineStore(env Env) *state.ConfigStore {
	return &state.ConfigStore{DB: env.DB}
}

// loadVolcengineCredential reads this machine's sign-in. Absent is not an error:
// it is the answer "no sign-in here".
func loadVolcengineCredential(ctx context.Context, env Env) (volcengineCredential, bool, error) {
	if env.DB == nil {
		return volcengineCredential{}, false, nil
	}
	raw, err := volcengineStore(env).LoadCredential(ctx, volcengineCredentialSection)
	if err != nil {
		return volcengineCredential{}, false, err
	}
	if len(raw) == 0 {
		return volcengineCredential{}, false, nil
	}
	var credential volcengineCredential
	if err := json.Unmarshal(raw, &credential); err != nil {
		// A row this build cannot read is a sign-in it cannot use; the person
		// signs in again, which rewrites it.
		return volcengineCredential{}, false, nil
	}
	if credential.creds().empty() {
		return volcengineCredential{}, false, nil
	}
	return credential, true, nil
}

// saveVolcengineCredential writes the sign-in, replacing what was there: the
// last sign-in wins, exactly as it does for the other two.
func saveVolcengineCredential(ctx context.Context, env Env, credential volcengineCredential) error {
	if env.DB == nil {
		return errors.New("this install has no state database to store the Volcengine sign-in in")
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	return volcengineStore(env).SaveCredential(ctx, volcengineCredentialSection, raw)
}
