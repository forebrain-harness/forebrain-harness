// transport.go dresses every model request as the official WorkBuddy client
// makes it: the product's own host and path, the identity headers the client
// sends, and the login's bearer token — refreshed once when the gateway answers
// 401. The Chat Completions body and the SSE answer pass through untouched,
// because WorkBuddy's model gateway speaks the standard protocol.
package workbuddy

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// chatCompletionsPath is where a model request lands: the official client's
// resolveModelBaseURL answers "<product endpoint>/v2" and its Chat Completions
// client appends "/chat/completions".
const chatCompletionsPath = "/v2/chat/completions"

// maxRequestBody caps the request body the transport reads: a conversation with
// attachments is megabytes; anything larger is not one.
const maxRequestBody = 32 << 20

// Transport is the disguise: Home and DB say where the login lives (a nil DB
// falls back to the credential file under Home).
type Transport struct {
	Home string
	DB   *sql.DB

	base http.RoundTripper
}

// SetBase receives the client's own transport chain (reasoning normalization,
// usage accounting, …); the plugin sits outside all of it.
func (t *Transport) SetBase(base http.RoundTripper) { t.base = base }

// OwnsUserAgent says the transport installs the client's User-Agent itself.
func (t *Transport) OwnsUserAgent() bool { return true }

func (t *Transport) roundTripper() http.RoundTripper {
	if t.base != nil {
		return t.base
	}
	return http.DefaultTransport
}

// RoundTrip re-sends the request the client built to the product endpoint,
// wearing the official client's headers; the body is the client's, unchanged.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := readRequestBody(req)
	if err != nil {
		return nil, err
	}
	model := modelIDFromBody(body)

	resp, err := t.send(req, body, model)
	if err != nil {
		return nil, err
	}
	if isAuthStatus(resp.StatusCode) {
		// The token expired between the freshness check and the call: refresh
		// once, re-sign, and send again. A second refusal travels up as-is, so
		// the surface can say the login is gone.
		resp.Body.Close()
		if _, refreshErr := ForceRefreshCredentials(req.Context(), t.DB, t.Home); refreshErr == nil {
			resp, err = t.send(req, body, model)
			if err != nil {
				return nil, err
			}
		}
	}
	return resp, nil
}

// send signs and performs one model request.
func (t *Transport) send(original *http.Request, body []byte, model string) (*http.Response, error) {
	creds, err := EnsureFreshCredentials(original.Context(), t.DB, t.Home)
	if err != nil {
		return nil, err
	}
	target, err := requestURL(workBuddyBaseURL, chatCompletionsPath)
	if err != nil {
		return nil, err
	}
	out, err := http.NewRequestWithContext(original.Context(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	out.Header = disguiseHeaders(creds, model)
	// The client's own answer format requests travel with it: Accept selects the
	// SSE stream, and the body is the client's JSON either way.
	if accept := original.Header.Get("Accept"); accept != "" {
		out.Header.Set("Accept", accept)
	}
	if contentType := original.Header.Get("Content-Type"); contentType != "" {
		out.Header.Set("Content-Type", contentType)
	}
	out.ContentLength = int64(len(body))
	return t.roundTripper().RoundTrip(out)
}

// requestURL is where one request goes: always the product endpoint, whatever
// host the client was configured with and whatever domain the credential names
// (that one only feeds X-Domain). The official client resolves its model base
// URL from the product endpoint the same way, so an enterprise login's
// sign-in domain can never become the model host.
func requestURL(base, path string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "", err
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("workbuddy: the endpoint %q names no host", base)
	}
	parsed.Path = path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

// disguiseHeaders is the header set the official client sends on a model
// request. The identity headers a personal account does not have are left out
// entirely — the client sends no empty ones either.
func disguiseHeaders(creds Credentials, model string) http.Header {
	header := http.Header{}
	header.Set("Accept", "application/json")
	header.Set("Content-Type", "application/json")
	header.Set("Authorization", "Bearer "+creds.AccessToken)
	// The agent CLI's build rides in the User-Agent beside the desktop
	// version: the gateway selects the product config generation by that
	// "CLI/<version>" segment alone (cliVersion).
	header.Set("User-Agent", productName+"/"+productVersion+" CLI/"+cliVersion)
	header.Set("X-Product", deploymentType)
	header.Set("X-Product-Version", productVersion)
	header.Set("X-IDE-Type", productName)
	header.Set("X-IDE-Name", productName)
	header.Set("X-IDE-Version", productVersion)
	header.Set("X-Agent-Intent", "craft")
	header.Set("X-Agent-Purpose", "conversation")
	header.Set("X-Agent-Runtime", "local-agent")
	header.Set("X-Requested-With", "XMLHttpRequest")
	// The client spells this one lowercase; header names are case-insensitive,
	// and net/http stores (and writes) the canonical form.
	header.Set("X-Codebuddy-Request", "1")
	header.Set("X-Private-Data", "true")
	header.Set("X-Auth-Method", authMethod)
	header.Set("X-Domain", domainOf(creds))
	requestID := uuid.NewString()
	conversationID := uuid.NewString()
	header.Set("X-Request-ID", requestID)
	header.Set("X-Conversation-ID", conversationID)
	// This plugin holds no conversation of its own, so the two ids the client
	// derives from one request state the same values: the conversation's
	// request id is its X-Conversation-ID, and the message id is the request's.
	header.Set("X-Conversation-Request-ID", conversationID)
	header.Set("X-Conversation-Message-ID", requestID)
	uid := strings.TrimSpace(creds.UID)
	enterprise := strings.TrimSpace(creds.EnterpriseID)
	idSource := strings.TrimSpace(creds.IDSource)
	method := strings.TrimSpace(creds.LoginMethod)
	if uid != "" {
		// product.json's attributes.usernameHeader, URL-encoded as its
		// usernameEncode says.
		header.Set("X-User-Id", url.QueryEscape(uid))
	}
	if enterprise != "" {
		header.Set("X-Enterprise-Id", enterprise)
		header.Set("X-Tenant-Id", enterprise)
	}
	// X-Userinfo is the identity quadruple the client states, and it is a
	// conditional header: a personal account with no enterprise, id source or
	// sign-in method sends none at all.
	if uid != "" && (enterprise != "" || idSource != "" || method != "") {
		header.Set("X-Userinfo", userInfo(uid, enterprise, idSource, method))
	}
	if model = strings.TrimSpace(model); model != "" {
		header.Set("X-Model-ID", model)
	}
	return header
}

// userInfo is X-Userinfo: base64 of the account's identity quadruple.
func userInfo(uid, enterprise, idSource, method string) string {
	raw, err := json.Marshal(map[string]string{
		"uin":          uid,
		"owner_uin":    enterprise,
		"id_source":    idSource,
		"token_source": method,
	})
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// modelIDFromBody reads the request's model id (the X-Model-ID header the
// official client sends); the body itself is never rewritten.
func modelIDFromBody(body []byte) string {
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Model)
}

// readRequestBody reads and closes the client's body. A body past the cap is
// refused rather than cut short: half a JSON document is not a request.
func readRequestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxRequestBody+1))
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(body) > maxRequestBody {
		return nil, fmt.Errorf("workbuddy: the request body is larger than %d bytes", maxRequestBody)
	}
	return body, nil
}
