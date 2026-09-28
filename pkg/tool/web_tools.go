// Web tools: fetch, search providers, markdown extraction, and SSRF guards.
package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"
)

type WebFetchInput struct {
	URL      string `json:"url" jsonschema:"description=HTTP or HTTPS URL to fetch."`
	Prompt   string `json:"prompt" jsonschema:"description=Processing instruction for the fetched content. Required by the model contract; Forebrain Harness returns raw capped text when empty."`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema_description:"Maximum response body bytes to download (default 2097152, max 8388608). Bounds how much of a long page is retrievable; the reply itself is always capped and the remainder is written to a file you can read_file."`
}

// webFetchAllowPrivateIP reports whether the operator has opened private and
// loopback space to web_fetch. It is read from config rather than from the tool
// input on purpose — see config.WebFetchConfig.
func webFetchAllowPrivateIP(rt *AgentToolRuntime) bool {
	if rt == nil || rt.Cfg == nil {
		return false
	}
	return rt.Cfg.Agents.Defaults.WebFetch.AllowPrivateIP
}

const (
	defaultWebFetchMaxBytes  = 2 << 20
	absoluteWebFetchMaxBytes = 8 << 20
	webFetchMaxRedirects     = 10
	webFetchMaxMarkdownBytes = 28000
	// webFetchMinMarkdownBytes floors the shrinking in marshalWebFetchResult, so
	// a page behind an absurdly long URL still comes back with some prose rather
	// than metadata alone.
	webFetchMinMarkdownBytes = 2000
	// webFetchMaxEnvelopeBytes is the marshalled size the envelope aims to stay
	// under, with headroom below the governor's spill threshold: past it the
	// governor replaces the result with a head/tail trim, and trimmed JSON no
	// longer parses.
	webFetchMaxEnvelopeBytes = SpillThresholdBytes - 2048
	// webFetchErrorBodyMaxBytes is all an error response gets. A 404 or 502 page
	// is site chrome the model did not ask for, but API endpoints do put the
	// useful detail in the body, so keep a slice rather than dropping it.
	webFetchErrorBodyMaxBytes = 2000
	webFetchAuthWarningStable = "WebFetch WILL FAIL for authenticated or private URLs"
)

func WebFetchPrompt() string {
	return "Use when you need the contents of a specific public URL: fetches HTTP(S) content and returns response metadata with a capped body, where HTML is reduced to the page's main content as markdown. " + webFetchAuthWarningStable + "."
}

func WebFetchPermissionRuleContent(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return ""
	}
	return "domain:" + host
}

func WebFetchPermissionPayload(raw string) map[string]any {
	urlText := strings.TrimSpace(raw)
	return map[string]any{
		"url":          urlText,
		"rule_content": WebFetchPermissionRuleContent(urlText),
	}
}

var webFetchPreapprovedHosts = map[string]struct{}{
	"docs.python.org":           {},
	"en.cppreference.com":       {},
	"docs.oracle.com":           {},
	"learn.microsoft.com":       {},
	"developer.mozilla.org":     {},
	"go.dev":                    {},
	"pkg.go.dev":                {},
	"www.php.net":               {},
	"docs.swift.org":            {},
	"kotlinlang.org":            {},
	"ruby-doc.org":              {},
	"doc.rust-lang.org":         {},
	"www.typescriptlang.org":    {},
	"react.dev":                 {},
	"vuejs.org":                 {},
	"nextjs.org":                {},
	"expressjs.com":             {},
	"nodejs.org":                {},
	"bun.sh":                    {},
	"tailwindcss.com":           {},
	"threejs.org":               {},
	"docs.djangoproject.com":    {},
	"flask.palletsprojects.com": {},
	"fastapi.tiangolo.com":      {},
	"pandas.pydata.org":         {},
	"numpy.org":                 {},
	"pytorch.org":               {},
	"scikit-learn.org":          {},
	"docs.spring.io":            {},
	"maven.apache.org":          {},
	"gradle.org":                {},
	"kubernetes.io":             {},
	"docs.docker.com":           {},
	"git-scm.com":               {},
}

var webFetchPreapprovedPathPrefixes = map[string][]string{
	"vercel.com": {"/docs"},
}

func IsWebFetchPreapprovedURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return false
	}
	if _, ok := webFetchPreapprovedHosts[host]; ok {
		return true
	}
	prefixes := webFetchPreapprovedPathPrefixes[host]
	path := strings.TrimSpace(u.EscapedPath())
	if path == "" {
		path = "/"
	}
	for _, prefix := range prefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func ShouldWebFetchRequestPermission(raw string) bool {
	return !IsWebFetchPreapprovedURL(raw)
}

func NewWebFetchTool(st *State, rt *AgentToolRuntime) (*llm.Tool, error) {
	return llm.NewTool(
		"web_fetch",
		WebFetchPrompt(),
		func(ctx context.Context, in *WebFetchInput) (string, error) {
			if st == nil {
				return "", fmt.Errorf("nil state")
			}
			if in == nil {
				in = &WebFetchInput{}
			}
			if err := st.GuardTool(ctx, "web_fetch"); err != nil {
				return "", err
			}
			raw := strings.TrimSpace(in.URL)
			if raw == "" {
				return "", fmt.Errorf("url required")
			}
			allowPrivate := webFetchAllowPrivateIP(rt)
			u, err := fetchURLAllowed(raw, allowPrivate)
			if err != nil {
				return "", err
			}
			// Private space is never covered by the preapproved list: those
			// entries vouch for a public documentation host, not for whatever
			// address one might resolve to.
			gated := ShouldWebFetchRequestPermission(u.String()) || allowPrivate
			if hook := st.ActionHook(); hook != nil && gated {
				if ApprovedActionIDFromContext(ctx) == "" && !PolicyApprovedFromContext(ctx) {
					payload := WebFetchPermissionPayload(u.String())
					if allowPrivate {
						payload["allow_private_ip"] = true
					}
					id, ok, err := hook(ctx, "web_fetch", payload)
					if err != nil {
						return "", err
					}
					if ok && id != "" {
						return "", &RequiresActionError{ActionID: id, ActionKind: "web_fetch", ToolName: "web_fetch", ToolInput: in}
					}
				}
			}
			maxB := in.MaxBytes
			if maxB <= 0 {
				maxB = defaultWebFetchMaxBytes
			}
			if maxB > absoluteWebFetchMaxBytes {
				maxB = absoluteWebFetchMaxBytes
			}
			// The transport carries a dial guard bound to the resolved policy,
			// so drop its pooled connections rather than leaving them idle
			// behind a client that is about to go away.
			transport := webFetchTransport(allowPrivate)
			defer transport.CloseIdleConnections()
			client := &http.Client{
				Timeout:   30 * time.Second,
				Transport: transport,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					if len(via) >= webFetchMaxRedirects {
						return fmt.Errorf("stopped after %d redirects", webFetchMaxRedirects)
					}
					next, err := fetchURLAllowed(req.URL.String(), allowPrivate)
					if err != nil {
						return err
					}
					if !webFetchRedirectCovered(via[len(via)-1].URL, next, gated) {
						// Stop and hand the 3xx back, so the hop can be
						// reported and re-decided rather than followed on the
						// strength of the previous host's approval.
						return http.ErrUseLastResponse
					}
					return nil
				},
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
			if err != nil {
				return "", err
			}
			req.Header.Set("User-Agent", "forebrain-web_fetch/1")
			resp, err := client.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			if target := webFetchBlockedRedirect(resp); target != "" {
				return marshalWebToolOutput(webFetchBlockedRedirectResult(resp, target)), nil
			}
			lim := io.LimitReader(resp.Body, int64(maxB)+1)
			body, err := io.ReadAll(lim)
			if err != nil {
				return "", err
			}
			truncated := len(body) > maxB
			if truncated {
				body = body[:maxB]
			}
			ct := resp.Header.Get("Content-Type")
			finalURL := resp.Request.URL.String()
			out := map[string]any{
				"final_url":    finalURL,
				"status":       resp.StatusCode,
				"content_type": ct,
				"bytes":        len(body),
				"truncated":    truncated,
				"prompt":       strings.TrimSpace(in.Prompt),
				"preapproved":  IsWebFetchPreapprovedURL(finalURL),
				"rule_content": WebFetchPermissionRuleContent(finalURL),
			}
			if !webFetchIsTextualBody(ct, body) {
				out["skipped"] = "unsupported_content_type"
				out["body"] = webFetchUnsupportedTypeNote(ct, len(body))
				return marshalWebToolOutput(out), nil
			}
			rendered := webFetchRenderBody(decodeWebFetchBody(body, ct), ct)
			if resp.StatusCode >= http.StatusBadRequest {
				// Reporting the failure matters more than the page: without it
				// the model reads a 404 shell as the answer and passes the site's
				// error copy off as the documentation it was asked for.
				text, cut := trimTextToUTF8Bytes(rendered, webFetchErrorBodyMaxBytes)
				out["error"] = fmt.Sprintf("http_%d", resp.StatusCode)
				out["truncated"] = truncated || cut
				out["body"] = webFetchStatusNote(resp.StatusCode, finalURL) + text
				return marshalWebToolOutput(out), nil
			}
			return marshalWebFetchResult(ctx, st, out, rendered, truncated), nil
		},
	)
}

// storeWebFetchContent saves the full rendered page next to the other tool
// result spills and returns its path, or "" when it could not be written —
// pagination is a convenience, never a reason to fail the fetch.
func storeWebFetchContent(ctx context.Context, st *State, content string) string {
	// Only the orchestrator's tool-result directory is used. A temp-dir
	// fallback would land outside the workspace roots, where a deny_read
	// policy or a configured permission profile can refuse the path — a
	// pointer the model cannot follow is worse than admitting the remainder
	// was dropped.
	dir := strings.TrimSpace(st.ToolResultDir())
	if dir == "" {
		return ""
	}
	callID := strings.TrimSpace(ToolUseIDFromContext(ctx))
	path, err := NewOutputGovernor(dir).Store("web_fetch", callID, content)
	if err != nil {
		return ""
	}
	st.RecordToolResultSpill(ToolResultSpill{
		SessionID:     strings.TrimSpace(llm.AgentSessionIDFromContext(ctx)),
		RunID:         strings.TrimSpace(RunIDFromContext(ctx)),
		ToolName:      "web_fetch",
		CallID:        callID,
		Path:          path,
		OriginalBytes: len(content),
		TotalLines:    strings.Count(content, "\n") + 1,
		CreatedAt:     time.Now().UTC(),
	})
	return path
}

// marshalWebFetchResult fills in the body and its continuation metadata,
// shrinking the inline slice until the marshalled envelope fits
// webFetchMaxEnvelopeBytes.
//
// A byte cap on the text alone cannot promise that: JSON escaping expands
// quotes and control characters, and final_url carries whatever the redirect
// chain ended on. So the budget is enforced against what the envelope actually
// marshals to, which is the number the governor will measure.
func marshalWebFetchResult(ctx context.Context, st *State, out map[string]any, rendered string, downloadTruncated bool) string {
	limit := webFetchMaxMarkdownBytes
	spilled := ""
	for {
		text, cut := trimTextToUTF8Bytes(rendered, limit)
		out["truncated"] = downloadTruncated || cut
		if cut {
			// The inline cap keeps the envelope inside the tool-output budget,
			// so the remainder has to live somewhere the model can still reach —
			// otherwise re-fetching just returns this same prefix and the rest of
			// the page is unreachable for good.
			out["content_bytes"] = len(rendered)
			out["omitted_bytes"] = len(rendered) - len(text)
			if spilled == "" {
				spilled = storeWebFetchContent(ctx, st, rendered)
			}
			if spilled != "" {
				out["full_path"] = spilled
				text += webFetchContinuationNote(spilled, len(text), len(rendered))
			}
		}
		out["body"] = text
		encoded := marshalWebToolOutput(out)
		// Dropping n bytes of source text drops at least n from the envelope,
		// so subtracting the overflow converges in a pass or two.
		over := len(encoded) - webFetchMaxEnvelopeBytes
		if over <= 0 || limit <= webFetchMinMarkdownBytes {
			return encoded
		}
		if limit -= over; limit < webFetchMinMarkdownBytes {
			limit = webFetchMinMarkdownBytes
		}
	}
}

func webFetchStatusNote(status int, finalURL string) string {
	label := strings.TrimSpace(fmt.Sprintf("%d %s", status, http.StatusText(status)))
	return fmt.Sprintf(
		"Request failed: %s returned HTTP %s. Anything below is that error response, not the page you asked for.\n\n",
		finalURL, label)
}

func webFetchUnsupportedTypeNote(contentType string, n int) string {
	ct := strings.TrimSpace(contentType)
	if ct == "" {
		ct = "the response"
	}
	return fmt.Sprintf(
		"Not returned: %s is not a text format, so the %d bytes downloaded were discarded. web_fetch only returns text.",
		ct, n)
}

// webFetchTextualMediaTypes carry text despite not being under text/*.
var webFetchTextualMediaTypes = map[string]bool{
	"application/json":       true,
	"application/xml":        true,
	"application/xhtml+xml":  true,
	"application/javascript": true,
	"application/ecmascript": true,
	"application/x-ndjson":   true,
	"application/graphql":    true,
	"application/yaml":       true,
	"application/x-yaml":     true,
	"application/toml":       true,
}

// webFetchIsTextualBody reports whether a response is worth spending the body
// budget on. A PDF, image or archive decodes to a wall of replacement runes:
// useless to the model and expensive in tokens, so it is reported rather than
// returned.
func webFetchIsTextualBody(contentType string, body []byte) bool {
	if mt, _, err := mime.ParseMediaType(contentType); err == nil {
		mt = strings.ToLower(strings.TrimSpace(mt))
		switch {
		case mt == "application/octet-stream":
			// The server declined to say; fall through to sniffing.
		case strings.HasPrefix(mt, "text/"),
			webFetchTextualMediaTypes[mt],
			strings.HasSuffix(mt, "+json"),
			strings.HasSuffix(mt, "+xml"),
			strings.HasSuffix(mt, "+yaml"):
			return true
		default:
			return false
		}
	}
	// Nothing usable was declared. A NUL byte is the classic tell of a binary
	// payload, and text never contains one.
	head := body
	if len(head) > 1024 {
		head = head[:1024]
	}
	return !bytes.Contains(head, []byte{0})
}

func webFetchContinuationNote(path string, shown int, total int) string {
	return fmt.Sprintf(
		"\n\n[web_fetch: showing the first %d of %d bytes. The full page is saved at %s — continue with read_file on that path, using offset and limit to page through it.]",
		shown, total, path)
}

// webFetchTransport dials through webFetchDialControl so every resolved
// destination address is re-checked.
//
// A request the environment routes through a proxy (http_proxy/https_proxy)
// is dialed to the proxy instead, and the proxy resolves and connects to the
// destination on our behalf. The proxy's address is the operator's choice —
// commonly one on loopback, which is where local proxy clients listen — and
// the guard is for destinations, so that hop is dialed without it: guarding
// it refused every proxied fetch. Proxied requests are covered by
// webFetchHostAllowed alone, which is why the metadata-host bans there are
// unconditional rather than folded into the address policy.
func webFetchTransport(allowPrivate bool) *http.Transport {
	return newWebFetchTransport(allowPrivate, http.ProxyFromEnvironment)
}

func newWebFetchTransport(allowPrivate bool, proxy func(*http.Request) (*url.URL, error)) *http.Transport {
	proxies := &webFetchProxies{proxyFor: proxy}
	toDestination := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: webFetchDialControl(allowPrivate)}
	toProxy := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy: proxies.route,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if proxies.isProxy(address) {
				return toProxy.DialContext(ctx, network, address)
			}
			return toDestination.DialContext(ctx, network, address)
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// webFetchProxies routes a transport's requests and remembers the proxies it
// routed them through, so the dialer can tell the hop to a proxy from a
// connection to a destination.
type webFetchProxies struct {
	proxyFor func(*http.Request) (*url.URL, error)
	mu       sync.Mutex
	dialed   map[string]bool
}

func (p *webFetchProxies) route(req *http.Request) (*url.URL, error) {
	proxy, err := p.proxyFor(req)
	if err == nil && proxy != nil {
		p.mu.Lock()
		if p.dialed == nil {
			p.dialed = map[string]bool{}
		}
		p.dialed[proxyDialAddress(proxy)] = true
		p.mu.Unlock()
	}
	return proxy, err
}

func (p *webFetchProxies) isProxy(address string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dialed[address]
}

// proxyDialAddress is the address the transport dials to reach proxy: its host
// and port, the scheme's default port when the URL names none.
func proxyDialAddress(proxy *url.URL) string {
	port := proxy.Port()
	if port == "" {
		switch strings.ToLower(proxy.Scheme) {
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	return net.JoinHostPort(proxy.Hostname(), port)
}

// webFetchRedirectCovered reports whether the permission that cleared the
// previous hop also covers the next one.
//
// A granted rule is `domain:<host>`, and the preapproved list is host-scoped
// (sometimes path-scoped too). Neither says anything about a different host, so
// a cross-host redirect must not inherit the first hop's clearance — otherwise
// any preapproved host that can be made to redirect becomes an unapproved fetch
// of somewhere else.
func webFetchRedirectCovered(prev *url.URL, next *url.URL, approvalGranted bool) bool {
	if prev == nil || next == nil {
		return false
	}
	if !strings.EqualFold(prev.Hostname(), next.Hostname()) {
		return false
	}
	if approvalGranted {
		return true
	}
	// Cleared by the preapproved list rather than by the user, and those
	// entries can be scoped to a path prefix.
	return IsWebFetchPreapprovedURL(next.String())
}

// webFetchBlockedRedirect returns the absolute target of a redirect the client
// declined to follow, or "" when the response is not such a redirect.
func webFetchBlockedRedirect(resp *http.Response) string {
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return ""
	}
	location := strings.TrimSpace(resp.Header.Get("Location"))
	if location == "" {
		return ""
	}
	target, err := url.Parse(location)
	if err != nil {
		return location
	}
	if resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.ResolveReference(target).String()
	}
	return target.String()
}

func webFetchBlockedRedirectResult(resp *http.Response, target string) map[string]any {
	from := ""
	if resp.Request != nil && resp.Request.URL != nil {
		from = resp.Request.URL.String()
	}
	return map[string]any{
		"final_url":        from,
		"status":           resp.StatusCode,
		"content_type":     resp.Header.Get("Content-Type"),
		"bytes":            0,
		"truncated":        false,
		"redirect_blocked": true,
		"redirect_to":      target,
		"body": "Not followed: " + from + " redirects to a different host (" + target + "). " +
			"Access was granted for the original host only. Call web_fetch again with " + target +
			" to request access to that host.",
		"rule_content": WebFetchPermissionRuleContent(target),
	}
}

// marshalWebToolOutput encodes a web tool envelope without Go's default HTML
// escaping. Fetched pages are dense in `<`, `>` and `&`, and escaping each one
// to a six-byte \u sequence inflated the payload past the tool-output budget,
// so the governor spilled it and left the model with truncated, unparseable
// JSON.
func marshalWebToolOutput(out map[string]any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		b, _ := json.Marshal(out)
		return string(b)
	}
	return strings.TrimRight(buf.String(), "\n")
}

var webFetchMetaCharsetRe = regexp.MustCompile(`(?i)<meta[^>]+charset\s*=\s*["']?\s*([a-z0-9_:.\-]+)`)

// webFetchCharset resolves the body encoding from the Content-Type header,
// falling back to an HTML meta prescan over the first 1KB the way browsers do.
// Chinese documentation sites in particular often declare GB18030/GBK only in
// the markup.
func webFetchCharset(contentType string, body []byte) string {
	if _, params, err := mime.ParseMediaType(contentType); err == nil {
		if name := strings.TrimSpace(params["charset"]); name != "" {
			return name
		}
	}
	head := body
	if len(head) > 1024 {
		head = head[:1024]
	}
	if m := webFetchMetaCharsetRe.FindSubmatch(head); len(m) == 2 {
		return string(m[1])
	}
	return ""
}

// decodeWebFetchBody converts a response body to UTF-8 text. Bytes that survive
// as ill-formed (a wrong charset declaration, or binary content) collapse to
// U+FFFD per run rather than defeating the caller.
func decodeWebFetchBody(body []byte, contentType string) string {
	if name := webFetchCharset(contentType, body); name != "" {
		if enc, err := htmlindex.Get(name); err == nil && enc != nil {
			if decoded, _, err := transform.Bytes(enc.NewDecoder(), body); err == nil {
				return strings.ToValidUTF8(string(decoded), "�")
			}
		}
	}
	return strings.ToValidUTF8(string(body), "�")
}

// trimTextToUTF8Bytes caps text at maxBytes, dropping only a partial trailing
// rune. It must not walk further back than one rune: the earlier whole-string
// utf8.ValidString loop discarded the entire body whenever any interior byte
// was ill-formed, which silently emptied non-UTF-8 pages.
func trimTextToUTF8Bytes(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, false
	}
	out := s[:maxBytes]
	for i := 0; i < utf8.UTFMax-1 && len(out) > 0; i++ {
		if r, size := utf8.DecodeLastRuneInString(out); r == utf8.RuneError && size <= 1 {
			out = out[:len(out)-1]
			continue
		}
		break
	}
	return strings.ToValidUTF8(out, "�"), true
}

type WebSearchInput struct {
	Query string `json:"query" jsonschema:"description=Search query text."`
	Limit int    `json:"limit,omitempty" jsonschema_description:"Max results hint (default 5, max 10)."`
}

const webSearchMaxOutputBytes = 20000

func NormalizeWebSearchLimit(limit int) int {
	if limit <= 0 {
		return 5
	}
	if limit > 10 {
		return 10
	}
	return limit
}

func WebSearchPermissionPayload(query string) map[string]any {
	q := strings.TrimSpace(query)
	return map[string]any{
		"query":        q,
		"rule_content": "query:" + q,
	}
}

func trimWebSearchOutput(s string) (string, bool) {
	return trimTextToUTF8Bytes(s, webSearchMaxOutputBytes)
}

func NewWebSearchTool(st *State, rt *AgentToolRuntime) (*llm.Tool, error) {
	return llm.NewTool(
		"web_search",
		// Provider selection and API keys are operator configuration the
		// model cannot act on, so the description carries only the trigger
		// and what the tool does.
		"Use when you need current information from the public web: multi-provider search (Tavily, Brave, or Baidu Qianfan as configured at startup).",
		func(ctx context.Context, in *WebSearchInput) (string, error) {
			if st == nil {
				return "", fmt.Errorf("nil state")
			}
			if in == nil {
				in = &WebSearchInput{}
			}
			if err := st.GuardTool(ctx, "web_search"); err != nil {
				return "", err
			}
			q := strings.TrimSpace(in.Query)
			if q == "" {
				return "", fmt.Errorf("query required")
			}
			if hook := st.ActionHook(); hook != nil {
				if ApprovedActionIDFromContext(ctx) == "" && !PolicyApprovedFromContext(ctx) {
					id, ok, err := hook(ctx, "web_search", WebSearchPermissionPayload(q))
					if err != nil {
						return "", err
					}
					if ok && id != "" {
						return "", &RequiresActionError{ActionID: id, ActionKind: "web_search", ToolName: "web_search", ToolInput: in}
					}
				}
			}
			limit := NormalizeWebSearchLimit(in.Limit)
			exec, err := resolveWebSearchExec(rt)
			if err != nil {
				return "", err
			}
			status, ct, body, err := runWebSearchRequest(ctx, exec, q, limit)
			if err != nil {
				return "", err
			}
			bodyText, truncated := trimWebSearchOutput(string(body))
			out := map[string]any{
				"status":       status,
				"content_type": ct,
				"limit":        limit,
				"body":         bodyText,
				"truncated":    truncated,
				"rule_content": WebSearchPermissionPayload(q)["rule_content"],
			}
			return marshalWebToolOutput(out), nil
		},
	)
}

const (
	tavilyAPIBaseURL     = "https://api.tavily.com"
	tavilySearchPath     = "/search"
	baiduWebSearchURL    = "https://qianfan.baidubce.com/v2/ai_search/web_search"
	braveWebSearchAPIURL = "https://api.search.brave.com/res/v1/web/search"
)

const (
	webSearchKindTavily = iota
	webSearchKindBrave
	webSearchKindBaidu
)

type webSearchExec struct {
	kind int

	tavilyPostURL string
	tavilyKey     string
	tavilyDepth   string

	braveKey string

	baiduURL string
	baiduKey string
}

func webSearchDefaults(rt *AgentToolRuntime) appcfg.WebSearchConfig {
	if rt == nil || rt.Cfg == nil {
		return appcfg.WebSearchConfig{}
	}
	return rt.Cfg.Agents.Defaults.WebSearch
}

func webSearchTavilyKey(cfg appcfg.WebSearchConfig) string {
	if cfg.Tavily != nil {
		return strings.TrimSpace(cfg.Tavily.APIKey)
	}
	return ""
}

func webSearchBraveKey(cfg appcfg.WebSearchConfig) string {
	if cfg.Brave != nil {
		return strings.TrimSpace(cfg.Brave.APIKey)
	}
	return ""
}

func webSearchBaiduKey(cfg appcfg.WebSearchConfig) string {
	if cfg.Baidu != nil {
		return strings.TrimSpace(cfg.Baidu.APIKey)
	}
	return ""
}

func webSearchTavilyDepth(cfg appcfg.WebSearchConfig) string {
	var d string
	if cfg.Tavily != nil {
		d = cfg.Tavily.SearchDepth
	}
	d = strings.ToLower(strings.TrimSpace(d))
	if d == "advanced" {
		return "advanced"
	}
	return "basic"
}

func resolveWebSearchExec(rt *AgentToolRuntime) (webSearchExec, error) {
	cfg := webSearchDefaults(rt)
	prov := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if prov == "" || prov == "auto" {
		if k := firstNonEmpty(os.Getenv("TAVILY_API_KEY"), webSearchTavilyKey(cfg)); k != "" {
			return buildTavilyExec(cfg, k)
		}
		if k := firstNonEmpty(os.Getenv("BRAVE_SEARCH_API_KEY"), webSearchBraveKey(cfg)); k != "" {
			return webSearchExec{kind: webSearchKindBrave, braveKey: k}, nil
		}
		if k := firstNonEmpty(os.Getenv("BAIDU_WEB_SEARCH_API_KEY"), webSearchBaiduKey(cfg)); k != "" {
			return buildBaiduExec(k)
		}
		return webSearchExec{}, fmt.Errorf("web_search: set TAVILY_API_KEY (default), BRAVE_SEARCH_API_KEY, BAIDU_WEB_SEARCH_API_KEY, or agents.defaults.web_search.{tavily,brave,baidu}.api_key with provider auto/tavily/brave/baidu")
	}
	switch prov {
	case "tavily":
		k := firstNonEmpty(os.Getenv("TAVILY_API_KEY"), webSearchTavilyKey(cfg))
		if k == "" {
			return webSearchExec{}, fmt.Errorf("web_search: provider tavily requires TAVILY_API_KEY or agents.defaults.web_search.tavily.api_key")
		}
		return buildTavilyExec(cfg, k)
	case "brave":
		k := firstNonEmpty(os.Getenv("BRAVE_SEARCH_API_KEY"), webSearchBraveKey(cfg))
		if k == "" {
			return webSearchExec{}, fmt.Errorf("web_search: provider brave requires BRAVE_SEARCH_API_KEY or agents.defaults.web_search.brave.api_key")
		}
		return webSearchExec{kind: webSearchKindBrave, braveKey: k}, nil
	case "baidu":
		k := firstNonEmpty(os.Getenv("BAIDU_WEB_SEARCH_API_KEY"), webSearchBaiduKey(cfg))
		if k == "" {
			return webSearchExec{}, fmt.Errorf("web_search: provider baidu requires BAIDU_WEB_SEARCH_API_KEY or agents.defaults.web_search.baidu.api_key")
		}
		return buildBaiduExec(k)
	default:
		return webSearchExec{}, fmt.Errorf("web_search: unknown provider %q (use tavily, brave, baidu, or auto)", prov)
	}
}

func buildTavilyExec(cfg appcfg.WebSearchConfig, key string) (webSearchExec, error) {
	postURL := strings.TrimSuffix(tavilyAPIBaseURL, "/") + tavilySearchPath
	if _, err := fetchURLAllowed(postURL, false); err != nil {
		return webSearchExec{}, fmt.Errorf("web_search tavily endpoint: %w", err)
	}
	return webSearchExec{
		kind:          webSearchKindTavily,
		tavilyPostURL: postURL,
		tavilyKey:     key,
		tavilyDepth:   webSearchTavilyDepth(cfg),
	}, nil
}

func buildBaiduExec(key string) (webSearchExec, error) {
	if _, err := fetchURLAllowed(baiduWebSearchURL, false); err != nil {
		return webSearchExec{}, fmt.Errorf("web_search baidu endpoint: %w", err)
	}
	return webSearchExec{kind: webSearchKindBaidu, baiduURL: baiduWebSearchURL, baiduKey: key}, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func runWebSearchRequest(ctx context.Context, e webSearchExec, query string, limit int) (int, string, []byte, error) {
	if limit <= 0 {
		limit = 5
	}
	client := &http.Client{Timeout: 25 * time.Second}
	switch e.kind {
	case webSearchKindTavily:
		return webSearchTavilyPOST(ctx, client, e, query, limit)
	case webSearchKindBrave:
		return webSearchBraveGET(ctx, client, e, query, limit)
	case webSearchKindBaidu:
		return webSearchBaiduPOST(ctx, client, e, query, limit)
	default:
		return 0, "", nil, fmt.Errorf("web_search: internal configuration error")
	}
}

func webSearchTavilyPOST(ctx context.Context, client *http.Client, e webSearchExec, query string, limit int) (int, string, []byte, error) {
	n := limit
	if n > 20 {
		n = 20
	}
	if n < 1 {
		n = 1
	}
	payload := map[string]any{
		"query":          query,
		"max_results":    n,
		"search_depth":   e.tavilyDepth,
		"include_answer": false,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, "", nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.tavilyPostURL, bytes.NewReader(raw))
	if err != nil {
		return 0, "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "forebrain-web_search/1")
	req.Header.Set("Authorization", "Bearer "+e.tavilyKey)
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, "", nil, err
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), body, nil
}

func webSearchBraveGET(ctx context.Context, client *http.Client, e webSearchExec, query string, limit int) (int, string, []byte, error) {
	n := limit
	if n > 20 {
		n = 20
	}
	if n < 1 {
		n = 1
	}
	u, err := url.Parse(braveWebSearchAPIURL)
	if err != nil {
		return 0, "", nil, err
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("count", fmt.Sprintf("%d", n))
	u.RawQuery = q.Encode()
	if _, err := fetchURLAllowed(u.String(), false); err != nil {
		return 0, "", nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, "", nil, err
	}
	req.Header.Set("User-Agent", "forebrain-web_search/1")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("X-Subscription-Token", e.braveKey)
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, "", nil, err
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), body, nil
}

func webSearchBaiduPOST(ctx context.Context, client *http.Client, e webSearchExec, query string, limit int) (int, string, []byte, error) {
	topK := limit
	if topK > 50 {
		topK = 50
	}
	if topK < 1 {
		topK = 5
	}
	payload := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": query},
		},
		"search_source": "baidu_search_v2",
		"resource_type_filter": []any{
			map[string]any{"type": "web", "top_k": topK},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, "", nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baiduURL, bytes.NewReader(raw))
	if err != nil {
		return 0, "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "forebrain-web_search/1")
	bearer := "Bearer " + e.baiduKey
	req.Header.Set("Authorization", bearer)
	req.Header.Set("X-Appbuilder-Authorization", bearer)
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, "", nil, err
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), body, nil
}

// A <main> or <article> container only wins over <body> when it carries enough
// prose to look like the real content well, rather than an empty shell that a
// client-side framework fills in later.
const webFetchMinExtractedChars = 200

// webFetchDropElements are subtrees that never carry document prose. Dropping
// them is what makes the body budget worth spending: on a typical docs page the
// scripts, styles and site chrome outweigh the article several times over.
var webFetchDropElements = map[atom.Atom]bool{
	atom.Script:   true,
	atom.Style:    true,
	atom.Noscript: true,
	atom.Template: true,
	atom.Svg:      true,
	atom.Canvas:   true,
	atom.Iframe:   true,
	atom.Object:   true,
	atom.Embed:    true,
	atom.Head:     true,
	atom.Nav:      true,
	atom.Footer:   true,
	atom.Aside:    true,
	atom.Button:   true,
	atom.Input:    true,
	atom.Select:   true,
	atom.Textarea: true,
}

// webFetchBlockElements render as their own Markdown block without adding any
// syntax of their own.
var webFetchBlockElements = map[atom.Atom]bool{
	atom.P:          true,
	atom.Div:        true,
	atom.Section:    true,
	atom.Article:    true,
	atom.Main:       true,
	atom.Header:     true,
	atom.Address:    true,
	atom.Dl:         true,
	atom.Dt:         true,
	atom.Dd:         true,
	atom.Figure:     true,
	atom.Figcaption: true,
	atom.Details:    true,
	atom.Summary:    true,
	atom.Form:       true,
}

// webFetchRenderBody converts an HTML response into Markdown so the capped body
// spends its budget on prose instead of markup. Non-HTML payloads pass through
// untouched.
func webFetchRenderBody(text string, contentType string) string {
	if !webFetchIsHTML(contentType, text) {
		return text
	}
	md := webFetchMarkdown(text)
	if strings.TrimSpace(md) == "" {
		return text
	}
	return md
}

func webFetchIsHTML(contentType string, text string) bool {
	if mt, _, err := mime.ParseMediaType(contentType); err == nil {
		switch strings.ToLower(strings.TrimSpace(mt)) {
		case "text/html", "application/xhtml+xml":
			return true
		case "", "application/octet-stream":
			// Fall through to sniffing.
		default:
			return false
		}
	}
	head := text
	if len(head) > 1024 {
		head = head[:1024]
	}
	head = strings.ToLower(head)
	return strings.Contains(head, "<!doctype html") || strings.Contains(head, "<html")
}

func webFetchMarkdown(doc string) string {
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return ""
	}
	content := webFetchContentRoot(root)
	if content == nil {
		return ""
	}
	w := &markdownWriter{}
	renderHTMLChildren(content, w)
	out := strings.TrimSpace(normalizeBlankLines(w.String()))
	if title := webFetchDocumentTitle(root); title != "" && !strings.HasPrefix(out, "# ") {
		out = strings.TrimSpace("# " + title + "\n\n" + out)
	}
	return out
}

// webFetchContentRoot prefers the largest <main>/<article> subtree and falls
// back to <body>, which keeps navigation-heavy layouts from crowding out the
// document.
func webFetchContentRoot(doc *html.Node) *html.Node {
	body := findHTMLElement(doc, atom.Body)
	var best *html.Node
	bestLen := 0
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && webFetchDropElements[n.DataAtom] {
			return
		}
		if n.Type == html.ElementNode && (n.DataAtom == atom.Main || n.DataAtom == atom.Article) {
			if size := visibleTextLen(n); size > bestLen {
				best, bestLen = n, size
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	search := body
	if search == nil {
		search = doc
	}
	walk(search)
	if best != nil && bestLen >= webFetchMinExtractedChars {
		return best
	}
	if body != nil {
		return body
	}
	return doc
}

func webFetchDocumentTitle(doc *html.Node) string {
	title := findHTMLElement(doc, atom.Title)
	if title == nil {
		return ""
	}
	return strings.TrimSpace(collapseHTMLWhitespace(rawTextContent(title)))
}

// markdownWriter accumulates Markdown while tracking just enough trailing state
// to know whether a separator is still owed.
type markdownWriter struct {
	b     strings.Builder
	last  byte
	inPre bool
}

func (w *markdownWriter) String() string { return w.b.String() }

func (w *markdownWriter) put(s string) {
	if s == "" {
		return
	}
	w.b.WriteString(s)
	w.last = s[len(s)-1]
}

func (w *markdownWriter) trailingNewlines(limit int) int {
	s := w.b.String()
	n := 0
	for n < limit && n < len(s) && s[len(s)-1-n] == '\n' {
		n++
	}
	return n
}

func (w *markdownWriter) ensureBlock() {
	if w.b.Len() == 0 {
		return
	}
	w.put(strings.Repeat("\n", 2-w.trailingNewlines(2)))
}

func (w *markdownWriter) ensureLine() {
	if w.b.Len() == 0 {
		return
	}
	w.put(strings.Repeat("\n", 1-w.trailingNewlines(1)))
}

func (w *markdownWriter) text(s string) {
	if w.inPre {
		w.put(s)
		return
	}
	collapsed := collapseHTMLWhitespace(s)
	if collapsed == "" {
		return
	}
	if strings.HasPrefix(collapsed, " ") && (w.b.Len() == 0 || w.last == '\n' || w.last == ' ') {
		collapsed = strings.TrimLeft(collapsed, " ")
		if collapsed == "" {
			return
		}
	}
	w.put(collapsed)
}

func (w *markdownWriter) wrapInline(n *html.Node, marker string) {
	sub := &markdownWriter{}
	renderHTMLChildren(n, sub)
	raw := sub.String()
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return
	}
	if strings.HasPrefix(raw, " ") {
		w.text(" ")
	}
	w.put(marker + trimmed + marker)
	if strings.HasSuffix(raw, " ") {
		w.text(" ")
	}
}

func renderHTMLChildren(n *html.Node, w *markdownWriter) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		renderHTMLNode(c, w)
	}
}

func renderHTMLNode(n *html.Node, w *markdownWriter) {
	switch n.Type {
	case html.TextNode:
		w.text(n.Data)
		return
	case html.DocumentNode:
		renderHTMLChildren(n, w)
		return
	case html.ElementNode:
	default:
		return
	}
	if webFetchDropElements[n.DataAtom] {
		return
	}
	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		w.ensureBlock()
		w.put(strings.Repeat("#", int(n.Data[1]-'0')) + " ")
		renderHTMLChildren(n, w)
		w.ensureBlock()
	case atom.Br:
		w.ensureLine()
	case atom.Hr:
		w.ensureBlock()
		w.put("---")
		w.ensureBlock()
	case atom.Ul, atom.Ol:
		w.ensureBlock()
		renderHTMLList(n, w)
		w.ensureBlock()
	case atom.Li:
		renderHTMLListItem(n, w, "- ")
	case atom.Pre:
		w.ensureBlock()
		w.put("```\n" + strings.Trim(rawTextContent(n), "\n") + "\n```")
		w.ensureBlock()
	case atom.Code, atom.Kbd, atom.Samp:
		if w.inPre {
			renderHTMLChildren(n, w)
			return
		}
		code := strings.TrimSpace(collapseHTMLWhitespace(rawTextContent(n)))
		if code == "" {
			return
		}
		w.put("`" + code + "`")
	case atom.Strong, atom.B:
		w.wrapInline(n, "**")
	case atom.Em, atom.I:
		w.wrapInline(n, "*")
	case atom.A:
		renderHTMLLink(n, w)
	case atom.Img:
		if alt := strings.TrimSpace(htmlAttr(n, "alt")); alt != "" {
			w.put("![" + alt + "](" + htmlAttr(n, "src") + ")")
		}
	case atom.Table:
		w.ensureBlock()
		renderHTMLTable(n, w)
		w.ensureBlock()
	case atom.Blockquote:
		w.ensureBlock()
		sub := &markdownWriter{}
		renderHTMLChildren(n, sub)
		quoted := strings.TrimSpace(normalizeBlankLines(sub.String()))
		if quoted != "" {
			w.put(prefixEveryLine(quoted, "> "))
		}
		w.ensureBlock()
	default:
		if webFetchBlockElements[n.DataAtom] {
			w.ensureBlock()
			renderHTMLChildren(n, w)
			w.ensureBlock()
			return
		}
		renderHTMLChildren(n, w)
	}
}

func renderHTMLList(list *html.Node, w *markdownWriter) {
	ordered := list.DataAtom == atom.Ol
	index := 1
	if ordered {
		if start, err := strconv.Atoi(strings.TrimSpace(htmlAttr(list, "start"))); err == nil {
			index = start
		}
	}
	for c := list.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.DataAtom != atom.Li {
			continue
		}
		marker := "- "
		if ordered {
			marker = strconv.Itoa(index) + ". "
			index++
		}
		renderHTMLListItem(c, w, marker)
	}
}

func renderHTMLListItem(li *html.Node, w *markdownWriter, marker string) {
	sub := &markdownWriter{}
	renderHTMLChildren(li, sub)
	body := strings.TrimSpace(normalizeBlankLines(sub.String()))
	if body == "" {
		return
	}
	w.ensureLine()
	indent := strings.Repeat(" ", len(marker))
	for i, line := range strings.Split(body, "\n") {
		if i == 0 {
			w.put(marker + line)
			continue
		}
		w.put("\n")
		if strings.TrimSpace(line) != "" {
			w.put(indent + line)
		}
	}
	w.ensureLine()
}

func renderHTMLLink(a *html.Node, w *markdownWriter) {
	sub := &markdownWriter{}
	renderHTMLChildren(a, sub)
	label := strings.TrimSpace(collapseHTMLWhitespace(sub.String()))
	if label == "" {
		return
	}
	href := strings.TrimSpace(htmlAttr(a, "href"))
	// In-page and scripted links carry no destination worth spending bytes on.
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(strings.ToLower(href), "javascript:") {
		w.text(label)
		return
	}
	w.put("[" + label + "](" + href + ")")
}

func renderHTMLTable(table *html.Node, w *markdownWriter) {
	var rows []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			if c.DataAtom == atom.Tr {
				rows = append(rows, c)
				continue
			}
			walk(c)
		}
	}
	walk(table)

	separatorDone := false
	for _, row := range rows {
		var cells []string
		for c := row.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode || (c.DataAtom != atom.Td && c.DataAtom != atom.Th) {
				continue
			}
			sub := &markdownWriter{}
			renderHTMLChildren(c, sub)
			cell := strings.TrimSpace(collapseHTMLWhitespace(sub.String()))
			cells = append(cells, strings.ReplaceAll(cell, "|", "\\|"))
		}
		if len(cells) == 0 {
			continue
		}
		w.ensureLine()
		w.put("| " + strings.Join(cells, " | ") + " |")
		w.ensureLine()
		if separatorDone {
			continue
		}
		separators := make([]string, len(cells))
		for i := range separators {
			separators[i] = "---"
		}
		w.put("| " + strings.Join(separators, " | ") + " |")
		w.ensureLine()
		separatorDone = true
	}
}

func htmlAttr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}

func findHTMLElement(n *html.Node, want atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == want {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findHTMLElement(c, want); found != nil {
			return found
		}
	}
	return nil
}

func rawTextContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			return
		}
		if n.Type == html.ElementNode && webFetchDropElements[n.DataAtom] {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func visibleTextLen(n *html.Node) int {
	return len(strings.TrimSpace(collapseHTMLWhitespace(rawTextContent(n))))
}

func collapseHTMLWhitespace(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	pendingSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			pendingSpace = true
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		b.WriteRune(r)
	}
	if pendingSpace {
		b.WriteByte(' ')
	}
	return b.String()
}

func normalizeBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blanks := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			blanks++
			if blanks > 1 {
				continue
			}
			out = append(out, "")
			continue
		}
		blanks = 0
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func prefixEveryLine(s string, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(prefix+line, " ")
	}
	return strings.Join(lines, "\n")
}

// webFetchBlockedHostNames are refused by name whatever they resolve to and
// whatever allow_private_ip says: they front credential-serving metadata
// services, and a name check is the only thing that still applies when an
// HTTP proxy is doing the resolution for us.
var webFetchBlockedHostNames = map[string]bool{
	"metadata.google.internal": true,
	"metadata.goog":            true,
	"metadata":                 true,
}

// webFetchHostAllowed applies the policy that can be decided from the URL alone.
// It is the only check that survives a proxied request, so the unconditional
// bans live here rather than in the dial guard.
func webFetchHostAllowed(host string, allowPrivate bool) error {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return fmt.Errorf("invalid url")
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if webFetchBlockedHostNames[host] {
		return fmt.Errorf("cloud metadata hosts are not allowed")
	}
	if ip := net.ParseIP(host); ip != nil {
		return webFetchIPAllowed(ip, allowPrivate)
	}
	if allowPrivate {
		return nil
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("private or loopback hosts are not allowed")
	}
	return nil
}

// webFetchIPAllowed decides whether a resolved address may be dialed.
//
// Judging the hostname alone is not a defense: a name the model controls can
// resolve to loopback or to internal space, and alternate literal forms
// (decimal, hex) never parse as an IP at all. This runs against the address the
// dialer is about to connect to, which is the only form that cannot be spoofed.
func webFetchIPAllowed(ip net.IP, allowPrivate bool) error {
	switch {
	case ip == nil:
		return fmt.Errorf("unresolvable address")
	case ip.IsUnspecified():
		return fmt.Errorf("unspecified addresses are not allowed")
	case ip.IsInterfaceLocalMulticast() || ip.IsLinkLocalMulticast() || ip.IsMulticast():
		return fmt.Errorf("multicast addresses are not allowed")
	case ip.IsLinkLocalUnicast():
		// 169.254.0.0/16 and fe80::/10 carry the cloud instance metadata
		// services, so this stays blocked even when private access is allowed:
		// no legitimate fetch needs credentials-bearing endpoints.
		return fmt.Errorf("link-local addresses (including cloud metadata) are not allowed")
	}
	if allowPrivate {
		return nil
	}
	if ip.IsLoopback() || ip.IsPrivate() || isSharedAddressSpace(ip) {
		return fmt.Errorf("private or loopback hosts are not allowed")
	}
	return nil
}

// isSharedAddressSpace reports RFC 6598 carrier-grade NAT space (100.64.0.0/10),
// which reaches provider infrastructure rather than the public internet.
func isSharedAddressSpace(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1]&0xc0 == 64
}

// webFetchDialControl re-checks every address the dialer resolves, including
// each redirect hop and every address of a multi-homed name. Control runs after
// resolution and immediately before connect, so a name that changes answers
// between the check and the dial cannot slip past.
func webFetchDialControl(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	return func(_ string, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			host = address
		}
		return webFetchIPAllowed(net.ParseIP(host), allowPrivate)
	}
}

func fetchURLAllowed(raw string, allowPrivate bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid url")
	}
	if strings.ToLower(u.Scheme) != "http" && strings.ToLower(u.Scheme) != "https" {
		return nil, fmt.Errorf("only http/https allowed")
	}
	if err := webFetchHostAllowed(u.Hostname(), allowPrivate); err != nil {
		return nil, err
	}
	return u, nil
}
