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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

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
// wearing the official client's headers; the body is the client's, unchanged —
// except for the one field that says which id the request is for, when the free
// tier's allowance is spent and this account chose its paid twin.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := readRequestBody(req)
	if err != nil {
		return nil, err
	}
	model := modelIDFromBody(body)

	resp, creds, sent, err := t.send(req, body, model)
	if err != nil {
		return nil, err
	}
	if isAuthStatus(resp.StatusCode) {
		// The token expired between the freshness check and the call: refresh
		// once, re-sign, and send again. A second refusal travels up as-is, so
		// the surface can say the login is gone.
		resp.Body.Close()
		if _, refreshErr := ForceRefreshCredentials(req.Context(), t.DB, t.Home); refreshErr == nil {
			resp, creds, sent, err = t.send(req, body, model)
			if err != nil {
				return nil, err
			}
		}
	}
	return t.classifyCap(creds, sent, resp), nil
}

// send signs and performs one model request. It answers the credential it was
// signed with and the id it was actually sent as, which the caller needs to read
// the answer.
func (t *Transport) send(original *http.Request, body []byte, model string) (*http.Response, Credentials, string, error) {
	creds, err := EnsureFreshCredentials(original.Context(), t.DB, t.Home)
	if err != nil {
		return nil, Credentials{}, "", err
	}
	model, body = t.effectiveModel(creds, body, model)
	target, err := requestURL(workBuddyBaseURL, chatCompletionsPath)
	if err != nil {
		return nil, Credentials{}, "", err
	}
	out, err := http.NewRequestWithContext(original.Context(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, Credentials{}, "", err
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
	resp, err := t.roundTripper().RoundTrip(out)
	if err != nil {
		return nil, Credentials{}, "", err
	}
	return resp, creds, model, nil
}

// effectiveModel answers the id one request is sent as. A model that is the free
// tier of a declared pair — whose product flag allows the switch, and whose paid
// twin this catalog carries — is sent as the paid id while this account has
// opted into it; every other request keeps the id the row names. It is the
// client's resolvePaidLineRuntimeId without its reverse direction: a paid id is
// never a row of this plugin's picker (Offers collapses the pair), so there is
// no window to reopen and no code for it here.
func (t *Transport) effectiveModel(creds Credentials, body []byte, model string) (string, []byte) {
	pair, ok := pairForFree(model)
	if !ok || !pair.AllowPaidSwitch || !catalogHasModel(pair.PaidID) {
		return model, body
	}
	if !paidSwitchActive(t.Home, creds, pair.FreeID) {
		return model, body
	}
	rewritten, err := replaceBodyModel(body, pair.PaidID)
	if err != nil {
		return model, body
	}
	return pair.PaidID, rewritten
}

// replaceBodyModel swaps one Chat Completions document's "model" for id. Every
// other member is carried as the bytes it arrived as, so nothing else about the
// request is re-encoded.
func replaceBodyModel(body []byte, id string) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if _, ok := payload["model"]; !ok {
		return nil, errors.New("workbuddy: the request body names no model")
	}
	encoded, err := json.Marshal(id)
	if err != nil {
		return nil, err
	}
	payload["model"] = encoded
	return json.Marshal(payload)
}

// maxErrorBody caps how much of a refused answer the transport reads to classify
// it. The envelope is a few hundred bytes; a body past this is not one this
// plugin can read, and it is passed through untouched rather than buffered.
const maxErrorBody = 64 << 10

// capCode is the product's own code for "this tier's allowance is spent": the
// CLI's CraftRateTPDLimit, and what the official client's
// isConversationRateLimitCapError looks for on a failed model request.
const capCode = 6004

// capCodeString is that code as the transport compares and spells it.
const capCodeString = "6004"

// classifyCap reads a refused model request for that code and, when the request
// was for a free tier whose paid twin may be switched, records the spent window
// and hands the layers above an OpenAI-shaped error they can classify.
//
// The gate follows the envelope Step 0b recorded: the product refuses with a 4xx
// whose body is JSON ({"code":6004,"msg":"…"}). A 2xx answer and a 2xx SSE stream
// pass through unread — the stream would break, and a successful document can be
// large. Anything else is passed through with its bytes intact.
func (t *Transport) classifyCap(creds Credentials, sentModel string, resp *http.Response) *http.Response {
	if resp == nil || resp.Body == nil {
		return resp
	}
	if resp.StatusCode < http.StatusBadRequest || resp.StatusCode >= http.StatusInternalServerError {
		return resp
	}
	if contentType := strings.ToLower(resp.Header.Get("Content-Type")); !strings.HasPrefix(contentType, "application/json") {
		return resp
	}
	head, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	rest := resp.Body
	resp.Body = replayBody{Reader: io.MultiReader(bytes.NewReader(head), rest), Closer: rest}
	if err != nil {
		return resp
	}
	if os.Getenv("WB_DEBUG_CAP") != "" {
		fmt.Printf("DEBUG head=%q ct=%q code=%d sent=%q\n", string(head), resp.Header.Get("Content-Type"), resp.StatusCode, sentModel)
	}
	refusal, ok := readCapRefusal(head)
	if !ok || !refusal.has(capCodeString) {
		return resp
	}
	pair, ok := pairForFree(sentModel)
	if !ok || pair.FreeID != sentModel || !pair.AllowPaidSwitch {
		return resp
	}
	reset, stamp := parseCapResetAt(refusal.prose)
	recordCapHit(t.Home, creds, pair.FreeID, reset)
	text := refusal.prose
	if !reset.IsZero() && stamp != "" {
		// The product's sentence with its own stamp, spelled with the zone it is
		// written in: the same instant, unambiguous to every layer above.
		text = strings.Replace(text, stamp, reset.Format(time.RFC3339), 1)
	}
	rewritten, err := json.Marshal(map[string]any{
		"error": map[string]string{
			"message": text,
			"type":    "quota_exhausted",
			"code":    capCodeString,
		},
	})
	if err != nil {
		return resp
	}
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(rewritten))
	resp.ContentLength = int64(len(rewritten))
	resp.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
	return resp
}

// replayBody puts a read-ahead body back together: the bytes already read, then
// the rest of the original stream, closed with the original body.
type replayBody struct {
	io.Reader
	io.Closer
}

// capRefusal is one answer read for the product's business code and prose.
type capRefusal struct {
	// codes are the business codes the answer carries, in the client's own
	// order (parseResponseBodyObject then extractRateLimitBizCode):
	// error.data.code, error.code, then the body's own code — and, one document
	// deeper, the same three inside a message field that carries the original
	// answer as JSON.
	codes []string
	// prose is the human sentence, the deepest one the answer holds. This repo's
	// own provider-error normalizer wraps a refused answer it does not recognize
	// as {"error":{"message":"<original body>","type":"provider_error"}}, so the
	// original's sentence is the one a person should read.
	prose string
}

// has reports whether the answer carries one business code anywhere in it. The
// code is matched across every place the client reads it, because a body that
// arrived through a normalizer carries the transport's status as `code` and the
// product's own code one document deeper.
func (r capRefusal) has(code string) bool {
	for _, candidate := range r.codes {
		if candidate == code {
			return true
		}
	}
	return false
}

// maxCapEnvelopeDepth bounds the walk into a nested message document: the
// normalizer nests one level, and the client reads message/details the same way
// (matchesBusinessCode), so one step is enough and two is the guard.
const maxCapEnvelopeDepth = 2

// readCapRefusal reads one response body for the product's business code.
func readCapRefusal(body []byte) (capRefusal, bool) {
	return readCapEnvelope(body, 0)
}

func readCapEnvelope(body []byte, depth int) (capRefusal, bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return capRefusal{}, false
	}
	var envelope capErrorBody
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return capRefusal{}, false
	}
	var refusal capRefusal
	if envelope.Error != nil && envelope.Error.Data != nil && envelope.Error.Data.Code != "" {
		refusal.codes = append(refusal.codes, string(envelope.Error.Data.Code))
	}
	if envelope.Error != nil && envelope.Error.Code != "" {
		refusal.codes = append(refusal.codes, string(envelope.Error.Code))
	}
	if envelope.Code != "" {
		refusal.codes = append(refusal.codes, string(envelope.Code))
	}
	prose := strings.TrimSpace(envelope.Msg)
	if prose == "" && envelope.Error != nil {
		prose = strings.TrimSpace(envelope.Error.Message)
	}
	if depth < maxCapEnvelopeDepth && isJSONObject(prose) {
		if inner, ok := readCapEnvelope([]byte(prose), depth+1); ok {
			refusal.codes = append(refusal.codes, inner.codes...)
			if inner.prose != "" {
				prose = inner.prose
			}
		}
	}
	refusal.prose = prose
	return refusal, len(refusal.codes) > 0
}

// isJSONObject reports whether text is a JSON object rather than a sentence.
func isJSONObject(text string) bool {
	text = strings.TrimSpace(text)
	return len(text) > 1 && text[0] == '{'
}

// capErrorBody is that envelope as Step 0b recorded it — the family whose
// unknown-model answer is {"code":11102,"msg":"model […] service info not
// found","requestId":…,"retry_policy":{…}} — plus the OpenAI-shaped spelling a
// gateway in front of it may use, and the shape this repo's own normalizer
// produces ({"error":{"message":"<original>","type":"provider_error"}}).
type capErrorBody struct {
	Code  bizCode `json:"code"`
	Msg   string  `json:"msg"`
	Error *struct {
		Code    bizCode `json:"code"`
		Message string  `json:"message"`
		Data    *struct {
			Code bizCode `json:"code"`
		} `json:"data"`
	} `json:"error"`
}

// bizCode is one business code, spelled the ways these answers spell it: a JSON
// number (what the product sends), a quoted number (what a gateway in front of
// it passes on), or the original answer as JSON inside a message. Anything that
// is not a whole number reads as no code at all, so the walk simply tries the
// next place a code may live.
type bizCode string

func (c *bizCode) UnmarshalJSON(raw []byte) error {
	text := strings.TrimSpace(string(raw))
	text = strings.Trim(text, `"`)
	text = strings.TrimSpace(text)
	if text == "" || text == "null" {
		return nil
	}
	if _, err := strconv.ParseInt(text, 10, 64); err != nil {
		return nil
	}
	*c = bizCode(text)
	return nil
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
