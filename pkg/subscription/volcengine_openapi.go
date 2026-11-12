// Volcengine Ark's control plane: the actions that say which plans an account
// holds, which models each plan runs, and which team seats exist. One POST per
// action, signed with Volcengine's V4 scheme, which the standard library can
// compute in full.
//
// Only the actions the public API reference documents are called here. The
// vendor publishes no way to read a personal plan's API key back, so nothing in
// this file looks for one: plan 281 takes that key from the user (see
// volcengine.go), and a team seat's key from GetTeamSeatApiKey.
package subscription

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// volcengineControlEndpoint is the Ark control plane every action of this
	// file is posted to (Ark's API reference: Service=ark, Region=cn-beijing).
	volcengineControlEndpoint = "https://ark.cn-beijing.volcengineapi.com"
	volcengineRegion          = "cn-beijing"
	volcengineService         = "ark"
	volcengineAPIVersion      = "2024-01-01"
	// volcengineDateLayout is the X-Date the signature signs: UTC, seconds.
	volcengineDateLayout = "20060102T150405Z"
	// volcengineMaxBody bounds one control-plane answer: they are small, and a
	// hostile one must not exhaust memory.
	volcengineMaxBody = 1 << 20
)

// volcengineCreds is one signed request's credential: the STS triple the console
// sign-in hands back. It never leaves the request it signs — no log line, no
// error text and no stored record other than the credential row itself.
type volcengineCreds struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

func (c volcengineCreds) empty() bool {
	return strings.TrimSpace(c.AccessKeyID) == "" || strings.TrimSpace(c.SecretAccessKey) == ""
}

// volcengineClient calls one control-plane action. The transport, the endpoint
// and the clock are fields so a test drives the same code against an
// httptest.Server, with a fixed date for a byte-exact signature.
type volcengineClient struct {
	Transport http.RoundTripper // nil = http.DefaultTransport
	Endpoint  string            // "" = volcengineControlEndpoint
	Now       func() time.Time  // nil = time.Now
}

func (c *volcengineClient) transport() http.RoundTripper {
	if c != nil && c.Transport != nil {
		return c.Transport
	}
	return http.DefaultTransport
}

func (c *volcengineClient) endpoint() string {
	if c != nil && strings.TrimSpace(c.Endpoint) != "" {
		return strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	}
	return volcengineControlEndpoint
}

func (c *volcengineClient) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// volcengineEnvelope is the control plane's answer wrapper: whether the action
// ran, and the Result the caller asked for.
type volcengineEnvelope struct {
	ResponseMetadata struct {
		RequestID string `json:"RequestId"`
		Error     *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	} `json:"ResponseMetadata"`
	Result json.RawMessage `json:"Result"`
}

// volcengineError is one refused action as a short sentence: "ark <action>:
// <code>". The vendor's message rides along only when it cannot quote this
// request's credential.
type volcengineError struct {
	Action  string
	Status  int
	Code    string
	Message string
}

func (e *volcengineError) Error() string {
	reason := e.Code
	if reason == "" {
		reason = fmt.Sprintf("HTTP %d", e.Status)
	}
	text := "ark " + e.Action + ": " + reason
	if e.Message != "" {
		text += " (" + e.Message + ")"
	}
	return text
}

// rejected reports whether the vendor refused the credential itself, which only
// a new sign-in repairs. The vendor does not publish a closed list of those
// codes, so this matches the status codes it uses and the families a credential
// refusal is named after.
func (e *volcengineError) rejected() bool {
	if e == nil {
		return false
	}
	if e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden {
		return true
	}
	code := strings.ToLower(e.Code)
	for _, family := range []string{"signature", "accesskey", "secretkey", "credential", "securitytoken", "tokenexpired", "tokeninvalid", "authfailure", "authenticationerror"} {
		if strings.Contains(code, family) {
			return true
		}
	}
	return false
}

// unreachable reports whether the action failed for a reason that is not the
// sign-in: the login stands and the caller may retry.
func (e *volcengineError) unreachable() bool {
	return e != nil && e.Status >= http.StatusInternalServerError
}

// notFound reports the vendor's answer for "the account holds no such plan",
// which is an answer rather than a failure.
func (e *volcengineError) notFound() bool {
	return e != nil && strings.HasPrefix(strings.ToLower(e.Code), "resourcenotfound")
}

// Do posts one action and decodes its Result into out. The body is JSON,
// `{}` when in is nil, and the signature covers the four headers Ark's
// documentation names: content-type, host, x-content-sha256, x-date.
func (c *volcengineClient) Do(ctx context.Context, action string, creds volcengineCreds, in, out any) error {
	if strings.TrimSpace(action) == "" {
		return fmt.Errorf("ark: no action")
	}
	if creds.empty() {
		return fmt.Errorf("ark %s: this machine holds no access key", action)
	}
	body := []byte("{}")
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("ark %s: %w", action, err)
		}
		body = encoded
	}
	endpoint := c.endpoint()
	query := volcengineCanonicalQuery(action)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/?"+query, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("ark %s: %w", action, err)
	}
	xDate := c.now().UTC().Format(volcengineDateLayout)
	bodyHash := volcengineHexSHA256(body)
	headers := [][2]string{
		{"content-type", "application/json"},
		{"host", volcengineHost(endpoint)},
		{"x-content-sha256", bodyHash},
		{"x-date", xDate},
	}
	canonical := volcengineCanonicalRequest(http.MethodPost, "/", query, headers, bodyHash)
	date := xDate[:8]
	signature := volcengineSignature(
		volcengineSigningKey(creds.SecretAccessKey, date, volcengineRegion, volcengineService),
		volcengineStringToSign(xDate, volcengineRegion, volcengineService, volcengineHexSHA256([]byte(canonical))),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Date", xDate)
	req.Header.Set("X-Content-Sha256", bodyHash)
	if token := strings.TrimSpace(creds.SessionToken); token != "" {
		req.Header.Set("X-Security-Token", token)
	}
	req.Header.Set("Authorization", "HMAC-SHA256 Credential="+strings.TrimSpace(creds.AccessKeyID)+"/"+
		date+"/"+volcengineRegion+"/"+volcengineService+"/request"+
		", SignedHeaders="+volcengineSignedHeaders(headers)+
		", Signature="+signature)

	resp, err := c.transport().RoundTrip(req)
	if err != nil {
		return fmt.Errorf("ark %s: %w", action, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, volcengineMaxBody))
	if err != nil {
		return fmt.Errorf("ark %s: %w", action, err)
	}
	var envelope volcengineEnvelope
	decodeErr := json.Unmarshal(raw, &envelope)
	if decodeErr != nil || envelope.ResponseMetadata.Error != nil {
		failure := &volcengineError{Action: action, Status: resp.StatusCode}
		if envelope.ResponseMetadata.Error != nil {
			failure.Code = envelope.ResponseMetadata.Error.Code
			failure.Message = volcengineSafeMessage(envelope.ResponseMetadata.Error.Message, creds)
		}
		if failure.Code != "" || resp.StatusCode/100 != 2 {
			return failure
		}
		return fmt.Errorf("ark %s: unreadable answer", action)
	}
	if resp.StatusCode/100 != 2 {
		return &volcengineError{Action: action, Status: resp.StatusCode}
	}
	if out == nil || len(envelope.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("ark %s: unreadable result", action)
	}
	return nil
}

// volcengineSafeMessage keeps a vendor message only when it cannot carry this
// request's credential: one that quotes the access key, the secret or the
// session token is dropped in favour of the code alone.
func volcengineSafeMessage(message string, creds volcengineCreds) string {
	message = strings.TrimSpace(message)
	if message == "" || len(message) > 200 {
		return ""
	}
	for _, secret := range []string{creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken} {
		if secret = strings.TrimSpace(secret); secret != "" && strings.Contains(message, secret) {
			return ""
		}
	}
	return message
}

// volcengineCanonicalQuery is the signature's query string: names and values
// RFC3986-encoded, sorted by name, joined with '&'. The two parameters every
// action carries are the whole of it.
func volcengineCanonicalQuery(action string) string {
	params := [][2]string{{"Action", action}, {"Version", volcengineAPIVersion}}
	return volcengineEscape(params[0][0]) + "=" + volcengineEscape(params[0][1]) + "&" +
		volcengineEscape(params[1][0]) + "=" + volcengineEscape(params[1][1])
}

// volcengineCanonicalRequest is the canonical request the signature hashes:
// method, URI, query, the signed headers each "name:value\n", the signed header
// names, then the body's hash.
func volcengineCanonicalRequest(method, uri, query string, headers [][2]string, bodyHash string) string {
	var builder strings.Builder
	builder.WriteString(method + "\n")
	builder.WriteString(uri + "\n")
	builder.WriteString(query + "\n")
	for _, header := range headers {
		builder.WriteString(strings.ToLower(header[0]) + ":" + strings.TrimSpace(header[1]) + "\n")
	}
	builder.WriteString("\n")
	builder.WriteString(volcengineSignedHeaders(headers) + "\n")
	builder.WriteString(bodyHash)
	return builder.String()
}

// volcengineSignedHeaders names the headers the signature covers, in the order
// the canonical request lists them.
func volcengineSignedHeaders(headers [][2]string) string {
	names := make([]string, 0, len(headers))
	for _, header := range headers {
		names = append(names, strings.ToLower(header[0]))
	}
	return strings.Join(names, ";")
}

// volcengineStringToSign is the string the signing key signs.
func volcengineStringToSign(xDate, region, service, canonicalHash string) string {
	return "HMAC-SHA256\n" + xDate + "\n" + xDate[:8] + "/" + region + "/" + service + "/request\n" + canonicalHash
}

// volcengineSigningKey derives the day's signing key from the secret access key:
// date, then region, then service, then the fixed "request" suffix.
func volcengineSigningKey(secret, date, region, service string) []byte {
	key := volcengineHMAC([]byte(secret), date)
	key = volcengineHMAC(key, region)
	key = volcengineHMAC(key, service)
	return volcengineHMAC(key, "request")
}

// volcengineSignature is the lower-case hex HMAC of the string to sign.
func volcengineSignature(key []byte, stringToSign string) string {
	return hex.EncodeToString(volcengineHMAC(key, stringToSign))
}

func volcengineHMAC(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return mac.Sum(nil)
}

func volcengineHexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// volcengineEscape encodes one query parameter the way the signature expects:
// RFC3986's unreserved set verbatim, everything else %XX upper-case.
func volcengineEscape(value string) string {
	var builder strings.Builder
	for i := 0; i < len(value); i++ {
		ch := value[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9',
			ch == '-', ch == '_', ch == '.', ch == '~':
			builder.WriteByte(ch)
		default:
			fmt.Fprintf(&builder, "%%%02X", ch)
		}
	}
	return builder.String()
}

// volcengineHost is the Host header value the signature covers, taken from the
// endpoint the request goes to.
func volcengineHost(endpoint string) string {
	host := endpoint
	if index := strings.Index(host, "://"); index >= 0 {
		host = host[index+3:]
	}
	if index := strings.IndexByte(host, '/'); index >= 0 {
		host = host[:index]
	}
	return host
}
