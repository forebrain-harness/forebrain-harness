package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"golang.org/x/text/encoding/simplifiedchinese"
)

const docsPage = `<!doctype html>
<html>
<head>
  <title>CreateInstance - API 文档</title>
  <style>.nav{color:red}</style>
  <script>window.__DATA__={"a":1}</script>
</head>
<body>
  <nav><a href="/x">Home</a><a href="/y">Products</a></nav>
  <main>
    <h1>CreateInstance</h1>
    <p>Creates an <strong>instance</strong> in the target zone. See
       <a href="/docs/limits">quota limits</a> before calling.</p>
    <h2>Request</h2>
    <ul>
      <li>Zone — the availability zone</li>
      <li>InstanceType — the machine size</li>
    </ul>
    <pre><code>curl -X POST https://api.example.com/v1/instances</code></pre>
    <table>
      <tr><th>Field</th><th>Type</th></tr>
      <tr><td>Zone</td><td>string</td></tr>
    </table>
  </main>
  <footer>© 2026 Example</footer>
</body>
</html>`

func TestWebFetchMarkdownExtractsMainContent(t *testing.T) {
	got := webFetchRenderBody(docsPage, "text/html; charset=utf-8")

	for _, want := range []string{
		"# CreateInstance",
		"## Request",
		"Creates an **instance** in the target zone.",
		"[quota limits](/docs/limits)",
		"- Zone — the availability zone",
		"```\ncurl -X POST https://api.example.com/v1/instances\n```",
		"| Field | Type |",
		"| --- | --- |",
		"| Zone | string |",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"window.__DATA__", ".nav{color:red}", "Products", "© 2026", "<p>", "<div"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("chrome %q leaked into:\n%s", unwanted, got)
		}
	}
}

func TestWebFetchMarkdownShrinksMarkupHeavyPages(t *testing.T) {
	got := webFetchRenderBody(docsPage, "text/html")
	if len(got) >= len(docsPage) {
		t.Fatalf("markdown %d bytes should be smaller than source %d", len(got), len(docsPage))
	}
}

func TestWebFetchMarkdownFallsBackToBodyWithoutMain(t *testing.T) {
	page := `<html><body><div><h2>Notes</h2><p>Body only page.</p></div></body></html>`
	got := webFetchRenderBody(page, "text/html")
	if !strings.Contains(got, "## Notes") || !strings.Contains(got, "Body only page.") {
		t.Fatalf("body fallback lost content: %q", got)
	}
}

func TestWebFetchMarkdownIgnoresEmptyMainShell(t *testing.T) {
	// A client-rendered shell must not win over the server-rendered body.
	page := `<html><body><main></main><div><p>` +
		strings.Repeat("Server rendered prose. ", 30) + `</p></div></body></html>`
	got := webFetchRenderBody(page, "text/html")
	if !strings.Contains(got, "Server rendered prose.") {
		t.Fatalf("empty <main> shell swallowed the content: %q", got)
	}
}

func TestWebFetchRenderBodyLeavesNonHTMLAlone(t *testing.T) {
	for _, tt := range []struct {
		name        string
		contentType string
		payload     string
	}{
		{name: "json", contentType: "application/json", payload: `{"a":"<b>"}`},
		{name: "plain", contentType: "text/plain", payload: "line 1\nline 2"},
		{name: "markdown", contentType: "text/markdown", payload: "# Title\n\ntext"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := webFetchRenderBody(tt.payload, tt.contentType); got != tt.payload {
				t.Fatalf("payload rewritten: %q -> %q", tt.payload, got)
			}
		})
	}
}

func TestWebFetchIsHTMLSniffsMissingContentType(t *testing.T) {
	if !webFetchIsHTML("", "<!DOCTYPE html><html><body>hi</body></html>") {
		t.Fatalf("should sniff html without a content type")
	}
	if webFetchIsHTML("", `{"not":"html"}`) {
		t.Fatalf("json must not be sniffed as html")
	}
}

func TestWebFetchMarkdownKeepsBlockquotesAndNestedLists(t *testing.T) {
	page := `<html><body><main>` +
		strings.Repeat(`<p>padding prose here.</p>`, 20) +
		`<blockquote><p>Note: this is important.</p></blockquote>` +
		`<ol start="3"><li>Third<ul><li>nested</li></ul></li></ol>` +
		`</main></body></html>`
	got := webFetchRenderBody(page, "text/html")
	if !strings.Contains(got, "> Note: this is important.") {
		t.Fatalf("blockquote lost: %q", got)
	}
	if !strings.Contains(got, "3. Third") {
		t.Fatalf("ordered list start ignored: %q", got)
	}
	if !strings.Contains(got, "- nested") {
		t.Fatalf("nested list lost: %q", got)
	}
}

// longArticle renders to well over the inline cap once markup is stripped.
func longArticle() string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><title>Long API Reference</title></head><body><main>`)
	b.WriteString(`<h1>Long API Reference</h1>`)
	for i := 0; i < 900; i++ {
		fmt.Fprintf(&b, `<h2>Section %d</h2><p>Paragraph %d describes the behaviour of parameter %d in detail.</p>`, i, i, i)
	}
	b.WriteString(`<p>THE-VERY-LAST-LINE-OF-THE-PAGE</p></main></body></html>`)
	return b.String()
}

// fetchLongArticle wires state the way the runner does: the tool-result
// directory lives inside the workspace root, so spilled pages are reachable by
// the same read_file the model would use.
func fetchLongArticle(t *testing.T) (map[string]any, string, *State) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, longArticle())
	}))
	defer srv.Close()

	workspace := t.TempDir()
	st := NewState(workspace)
	st.SetToolResultDir(filepath.Join(workspace, "state", "tool-outputs"))

	tool, err := NewWebFetchTool(st, privateAccessRuntime())
	if err != nil {
		t.Fatalf("NewWebFetchTool: %v", err)
	}
	args, _ := json.Marshal(WebFetchInput{URL: srv.URL})
	out, err := tool.Handle(context.Background(), string(args))
	if err != nil {
		t.Fatalf("web_fetch: %v", err)
	}
	raw := fmt.Sprint(out)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return decoded, raw, st
}

func TestWebFetchReportsRemainderWhenTruncated(t *testing.T) {
	decoded, raw, _ := fetchLongArticle(t)

	if decoded["truncated"] != true {
		t.Fatalf("long page should report truncation: %v", decoded["truncated"])
	}
	contentBytes := intFromJSON(t, decoded, "content_bytes")
	omitted := intFromJSON(t, decoded, "omitted_bytes")
	if contentBytes <= webFetchMaxMarkdownBytes {
		t.Fatalf("content_bytes=%d should exceed the inline cap", contentBytes)
	}
	if omitted != contentBytes-webFetchMaxMarkdownBytes {
		t.Fatalf("omitted_bytes=%d inconsistent with content_bytes=%d", omitted, contentBytes)
	}

	// The envelope still has to fit the tool-output budget.
	if len(raw) > 32*1024 {
		t.Fatalf("envelope=%d bytes exceeds the spill threshold", len(raw))
	}

	body, _ := decoded["body"].(string)
	if !strings.Contains(body, "continue with read_file") {
		t.Fatalf("body should tell the model how to reach the rest: %q", body[len(body)-200:])
	}
}

// The remainder is only genuinely retrievable if read_file can open the file we
// point at, so exercise that path rather than trusting the path string.
func TestWebFetchRemainderIsReadableWithReadFile(t *testing.T) {
	decoded, _, st := fetchLongArticle(t)

	path, _ := decoded["full_path"].(string)
	if path == "" {
		t.Fatalf("truncated fetch must expose full_path: %v", decoded)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("spilled page unreadable: %v", err)
	}
	if !strings.Contains(string(saved), "THE-VERY-LAST-LINE-OF-THE-PAGE") {
		t.Fatalf("spilled page is missing the tail that was cut from the reply")
	}

	readFile, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}
	readPage := func(offset, limit int) string {
		t.Helper()
		args, _ := json.Marshal(FileReadInput{FilePath: path, Offset: offset, Limit: limit})
		out, err := readFile.Handle(context.Background(), string(args))
		if err != nil {
			t.Fatalf("read_file(offset=%d) failed: %v", offset, err)
		}
		return fmt.Sprint(out)
	}

	if first := readPage(0, 5); !strings.Contains(first, "Long API Reference") {
		t.Fatalf("first page unexpected: %s", truncateForLog(first))
	}

	// The point of the whole feature: the tail that did not fit in the reply is
	// reachable by paging, instead of being lost until someone re-fetches (and
	// gets the same prefix back).
	totalLines := strings.Count(string(saved), "\n") + 1
	tail := readPage(totalLines-3, 10)
	if !strings.Contains(tail, "THE-VERY-LAST-LINE-OF-THE-PAGE") {
		t.Fatalf("could not page to the end of the article: %s", truncateForLog(tail))
	}
}

func TestWebFetchSkipsSpillWhenPageFits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><main><h1>Short</h1><p>Fits inline.</p></main></body></html>`)
	}))
	defer srv.Close()

	var decoded map[string]any
	if err := json.Unmarshal([]byte(fetchForTest(t, srv.URL)), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"full_path", "content_bytes", "omitted_bytes"} {
		if _, ok := decoded[key]; ok {
			t.Fatalf("%s should be absent when the page fits inline: %v", key, decoded)
		}
	}
	if body, _ := decoded["body"].(string); strings.Contains(body, "read_file") {
		t.Fatalf("no continuation note expected: %q", body)
	}
}

func intFromJSON(t *testing.T, m map[string]any, key string) int {
	t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		t.Fatalf("%s missing or not a number: %v", key, m[key])
	}
	return int(v)
}

func truncateForLog(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func TestWebFetchRedirectCovered(t *testing.T) {
	for _, tt := range []struct {
		name     string
		from     string
		to       string
		approved bool
		want     bool
	}{
		{
			name:     "same host after approval",
			from:     "https://example.com/a",
			to:       "https://example.com/b",
			approved: true,
			want:     true,
		},
		{
			name:     "scheme upgrade on same host",
			from:     "http://example.com/a",
			to:       "https://example.com/a",
			approved: true,
			want:     true,
		},
		{
			// The whole point: a domain:example.com grant says nothing at all
			// about evil.com.
			name:     "cross host after approval",
			from:     "https://example.com/a",
			to:       "https://evil.com/a",
			approved: true,
			want:     false,
		},
		{
			name: "preapproved host redirecting away",
			from: "https://go.dev/doc",
			to:   "https://evil.com/",
			want: false,
		},
		{
			name: "preapproved host redirecting within itself",
			from: "https://go.dev/doc",
			to:   "https://go.dev/ref/spec",
			want: true,
		},
		{
			// vercel.com is preapproved only under /docs, so an unapproved
			// path on the same host still needs a decision.
			name: "path scoped preapproval leaving its prefix",
			from: "https://vercel.com/docs/frameworks",
			to:   "https://vercel.com/account",
			want: false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := webFetchRedirectCovered(mustParseURL(t, tt.from), mustParseURL(t, tt.to), tt.approved)
			if got != tt.want {
				t.Fatalf("webFetchRedirectCovered(%s -> %s, approved=%v) = %v want %v",
					tt.from, tt.to, tt.approved, got, tt.want)
			}
		})
	}
}

func TestWebFetchStopsAtCrossHostRedirect(t *testing.T) {
	secret := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("INTERNAL-CONTENT-THAT-WAS-NEVER-APPROVED"))
	}))
	defer secret.Close()

	// "localhost" and "127.0.0.1" are distinct hosts for a domain: rule even
	// though they resolve to the same place, which makes them a faithful stand
	// -in for a redirect off an approved host.
	target := strings.Replace(secret.URL, "127.0.0.1", "localhost", 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer origin.Close()

	var decoded map[string]any
	if err := json.Unmarshal([]byte(fetchForTest(t, origin.URL)), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["redirect_blocked"] != true {
		t.Fatalf("cross-host redirect should be reported as blocked: %v", decoded)
	}
	body, _ := decoded["body"].(string)
	if strings.Contains(body, "INTERNAL-CONTENT-THAT-WAS-NEVER-APPROVED") {
		t.Fatalf("redirect target content leaked into the result: %q", body)
	}
	if !strings.Contains(body, "Call web_fetch again") {
		t.Fatalf("result should tell the model how to proceed: %q", body)
	}
	if got, _ := decoded["redirect_to"].(string); !strings.HasPrefix(got, target) {
		t.Fatalf("redirect_to=%q want %q", got, target)
	}
	if got, _ := decoded["rule_content"].(string); got != "domain:localhost" {
		t.Fatalf("rule_content=%q should name the host needing approval", got)
	}
}

func TestWebFetchFollowsSameHostRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "/final", http.StatusMovedPermanently)
			return
		}
		w.Write([]byte("SAME HOST CONTENT"))
	}))
	defer srv.Close()

	var decoded map[string]any
	if err := json.Unmarshal([]byte(fetchForTest(t, srv.URL+"/moved")), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, blocked := decoded["redirect_blocked"]; blocked {
		t.Fatalf("same-host redirect must be followed: %v", decoded)
	}
	if body, _ := decoded["body"].(string); !strings.Contains(body, "SAME HOST CONTENT") {
		t.Fatalf("body=%q", body)
	}
	if final, _ := decoded["final_url"].(string); !strings.HasSuffix(final, "/final") {
		t.Fatalf("final_url=%q should reflect the followed hop", final)
	}
}

// allow_private_ip is model-settable, so it must never ride in on the
// preapproved list — reaching private space always faces the user.
func TestWebFetchAlwaysPromptsForPrivateAccess(t *testing.T) {
	st := NewState(t.TempDir())
	var kinds []string
	st.SetActionHook(func(ctx context.Context, kind string, payload any) (string, bool, error) {
		encoded, _ := json.Marshal(payload)
		kinds = append(kinds, string(encoded))
		return "act-1", true, nil
	})
	tool, err := NewWebFetchTool(st, privateAccessRuntime())
	if err != nil {
		t.Fatalf("NewWebFetchTool: %v", err)
	}

	_, err = tool.Handle(context.Background(), `{"url":"https://go.dev/"}`)
	var required *RequiresActionError
	if !errors.As(err, &required) {
		t.Fatalf("private access to a preapproved host should still prompt, got %v", err)
	}
	if len(kinds) != 1 || !strings.Contains(kinds[0], `"allow_private_ip":true`) {
		t.Fatalf("approval payload must disclose private access: %v", kinds)
	}
}

// serveForTest fetches one canned response and returns the decoded envelope
// alongside the raw JSON, which is what the output governor actually measures.
func serveForTest(t *testing.T, contentType string, status int, payload []byte) (map[string]any, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		w.Write(payload)
	}))
	defer srv.Close()

	raw := fetchForTest(t, srv.URL)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return decoded, raw
}

func TestWebFetchRefusesBinaryContentTypes(t *testing.T) {
	// A PDF decodes to a wall of replacement runes: it tells the model nothing
	// and costs a full body budget in tokens.
	pdf := append([]byte("%PDF-1.4\n"), make([]byte, 4096)...)
	decoded, raw := serveForTest(t, "application/pdf", http.StatusOK, pdf)

	if decoded["skipped"] != "unsupported_content_type" {
		t.Fatalf("binary payload should be reported as skipped: %v", decoded)
	}
	body, _ := decoded["body"].(string)
	if !strings.Contains(body, "not a text format") {
		t.Fatalf("body should explain why nothing came back: %q", body)
	}
	if strings.Contains(raw, "%PDF") || strings.Contains(raw, "�") {
		t.Fatalf("binary content must not reach the envelope: %q", truncateForLog(raw))
	}
}

func TestWebFetchContentTypeGate(t *testing.T) {
	for _, tt := range []struct {
		contentType string
		want        bool
	}{
		{contentType: "text/html; charset=utf-8", want: true},
		{contentType: "text/plain", want: true},
		{contentType: "application/json", want: true},
		{contentType: "application/vnd.github+json", want: true},
		{contentType: "image/svg+xml", want: true},
		{contentType: "application/pdf", want: false},
		{contentType: "image/png", want: false},
		{contentType: "application/zip", want: false},
		{contentType: "video/mp4", want: false},
	} {
		t.Run(tt.contentType, func(t *testing.T) {
			if got := webFetchIsTextualBody(tt.contentType, []byte("hello")); got != tt.want {
				t.Fatalf("webFetchIsTextualBody(%q)=%v want %v", tt.contentType, got, tt.want)
			}
		})
	}
}

// Servers that decline to declare a type are common enough that refusing them
// outright would cost real pages, so the bytes decide.
func TestWebFetchSniffsUndeclaredContentType(t *testing.T) {
	decoded, _ := serveForTest(t, "application/octet-stream", http.StatusOK, []byte("PLAIN TEXT PAYLOAD"))
	if body, _ := decoded["body"].(string); !strings.Contains(body, "PLAIN TEXT PAYLOAD") {
		t.Fatalf("undeclared text should still be returned: %v", decoded)
	}

	decoded, _ = serveForTest(t, "application/octet-stream", http.StatusOK, append([]byte("MZ"), make([]byte, 512)...))
	if decoded["skipped"] != "unsupported_content_type" {
		t.Fatalf("undeclared binary should be refused: %v", decoded)
	}
}

func TestWebFetchReportsHTTPErrorStatus(t *testing.T) {
	page := `<html><body><main><h1>Page not found</h1><p>` +
		strings.Repeat("This site has no such document. ", 400) + `</p></main></body></html>`
	decoded, _ := serveForTest(t, "text/html", http.StatusNotFound, []byte(page))

	if decoded["error"] != "http_404" {
		t.Fatalf("404 must be surfaced as an error: %v", decoded["error"])
	}
	body, _ := decoded["body"].(string)
	if !strings.HasPrefix(body, "Request failed:") {
		t.Fatalf("body must lead with the failure, not the error page: %q", truncateForLog(body))
	}
	if !strings.Contains(body, "404 Not Found") {
		t.Fatalf("body should name the status: %q", truncateForLog(body))
	}
	// An error page is site chrome; it must not spend the full body budget.
	if len(body) > webFetchErrorBodyMaxBytes+len(webFetchStatusNote(http.StatusNotFound, ""))+200 {
		t.Fatalf("error body=%d bytes is not capped", len(body))
	}
	if _, ok := decoded["full_path"]; ok {
		t.Fatalf("error pages should not be spilled for paging: %v", decoded)
	}
}

// API endpoints put the actionable detail in the error body, so the slice that
// is kept has to be the response itself rather than a bare status line.
func TestWebFetchKeepsErrorResponseDetail(t *testing.T) {
	decoded, _ := serveForTest(t, "application/json", http.StatusBadRequest,
		[]byte(`{"error":"invalid_parameter","message":"page_size must be <= 100"}`))

	if decoded["error"] != "http_400" {
		t.Fatalf("error=%v want http_400", decoded["error"])
	}
	if body, _ := decoded["body"].(string); !strings.Contains(body, "page_size must be <= 100") {
		t.Fatalf("API error detail must survive: %q", body)
	}
}

func TestWebFetchSuccessHasNoErrorField(t *testing.T) {
	decoded, _ := serveForTest(t, "text/html", http.StatusOK,
		[]byte(`<html><body><main><p>All good.</p></main></body></html>`))
	if _, ok := decoded["error"]; ok {
		t.Fatalf("2xx must not carry an error field: %v", decoded)
	}
	if _, ok := decoded["skipped"]; ok {
		t.Fatalf("text response must not be skipped: %v", decoded)
	}
}

// The body cap counts source bytes, but the governor counts marshalled ones.
// A page dense in characters that JSON escapes drives those apart, and an
// envelope over the threshold is spilled, trimmed, and no longer parses.
func TestWebFetchEnvelopeFitsBudgetWhenEscapingInflates(t *testing.T) {
	quoted := strings.Repeat(`"`, 4*webFetchMaxMarkdownBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><head><title>Quotes</title></head><body><main><p>%s</p></main></body></html>`, quoted)
	}))
	defer srv.Close()

	workspace := t.TempDir()
	st := NewState(workspace)
	st.SetToolResultDir(filepath.Join(workspace, "state", "tool-outputs"))
	tool, err := NewWebFetchTool(st, privateAccessRuntime())
	if err != nil {
		t.Fatalf("NewWebFetchTool: %v", err)
	}
	args, _ := json.Marshal(WebFetchInput{URL: srv.URL})
	out, err := tool.Handle(t.Context(), string(args))
	if err != nil {
		t.Fatalf("web_fetch: %v", err)
	}

	raw := fmt.Sprint(out)
	if len(raw) > SpillThresholdBytes {
		t.Fatalf("envelope=%d bytes exceeds spill threshold %d", len(raw), SpillThresholdBytes)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("envelope must stay parseable: %v", err)
	}
	body, _ := decoded["body"].(string)
	if !strings.Contains(body, `"`) {
		t.Fatalf("shrinking must not empty the body: %q", body)
	}
	// Shrinking is a display decision; the page itself still has to be reachable.
	if path, _ := decoded["full_path"].(string); path == "" {
		t.Fatalf("shrunk body must still point at the full page: %v", decoded)
	}
}

func TestWebFetchRuleContentUsesDomain(t *testing.T) {
	if got := WebFetchPermissionRuleContent("https://docs.example.com/a/b"); got != "domain:docs.example.com" {
		t.Fatalf("rule content mismatch: %q", got)
	}
}

func TestWebFetchPrivateURLWarningStable(t *testing.T) {
	if !strings.Contains(WebFetchPrompt(), "WebFetch WILL FAIL for authenticated or private URLs") {
		t.Fatalf("missing stable auth warning")
	}
}

func TestWebFetchPermissionPayloadIncludesDomainRule(t *testing.T) {
	got := WebFetchPermissionPayload("https://docs.example.com/a/b")
	if got["url"] != "https://docs.example.com/a/b" {
		t.Fatalf("payload url mismatch: %#v", got)
	}
	if got["rule_content"] != "domain:docs.example.com" {
		t.Fatalf("payload rule mismatch: %#v", got)
	}
}

func TestWebFetchPreapprovedURLSkipsPermission(t *testing.T) {
	if !IsWebFetchPreapprovedURL("https://pkg.go.dev/testing") {
		t.Fatalf("expected pkg.go.dev to be preapproved")
	}
	if !IsWebFetchPreapprovedURL("https://vercel.com/docs/frameworks") {
		t.Fatalf("expected vercel docs path to be preapproved")
	}
	if IsWebFetchPreapprovedURL("https://vercel.com/account") {
		t.Fatalf("path-scoped preapproval must not allow other paths")
	}
	if !ShouldWebFetchRequestPermission("https://example.com") {
		t.Fatalf("unlisted host should request permission")
	}
	if ShouldWebFetchRequestPermission("https://pkg.go.dev/testing") {
		t.Fatalf("preapproved host should bypass permission")
	}
}

// escapedAngleBracket is the JSON \uXXXX form of '<'. Go's default encoder
// emits it for every tag character, which is what used to inflate fetched
// pages past the tool-output budget.
const escapedAngleBracket = "\\u003c"

func TestWebFetchOutputCarriesContentOnce(t *testing.T) {
	page := strings.Repeat(`<div class="doc"><p>Tencent Cloud &amp; API 文档</p></div>`+"\n", 2000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	}))
	defer srv.Close()

	out := fetchForTest(t, srv.URL)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("web_fetch output must be valid JSON: %v", err)
	}
	if _, ok := decoded["result"]; ok {
		t.Fatalf("output must not duplicate the body under result: %v", decoded["result"])
	}
	body, _ := decoded["body"].(string)
	if len(body) != webFetchMaxMarkdownBytes {
		t.Fatalf("body len=%d want %d", len(body), webFetchMaxMarkdownBytes)
	}
	if strings.Contains(out, escapedAngleBracket) {
		t.Fatalf("angle brackets must not be JSON-unicode-escaped in the envelope")
	}
	if !strings.Contains(body, "Tencent Cloud & API 文档") {
		t.Fatalf("prose should survive markdown conversion: %q", body[:120])
	}
	// The envelope has to stay inside the tool-output budget, or the governor
	// spills it and the model receives truncated, unparseable JSON.
	if len(out) > SpillThresholdBytes {
		t.Fatalf("envelope=%d bytes exceeds spill threshold %d", len(out), SpillThresholdBytes)
	}
}

func TestWebFetchDecodesDeclaredCharset(t *testing.T) {
	gbk, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("你好世界，这是腾讯云文档。"))
	if err != nil {
		t.Fatalf("encode gbk: %v", err)
	}
	for _, tt := range []struct {
		name        string
		contentType string
		page        []byte
	}{
		{name: "header", contentType: "text/html; charset=gbk", page: gbk},
		{
			name:        "meta prescan",
			contentType: "text/html",
			page:        append([]byte(`<meta charset="gbk">`), gbk...),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.Write(tt.page)
			}))
			defer srv.Close()

			var decoded map[string]any
			if err := json.Unmarshal([]byte(fetchForTest(t, srv.URL)), &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			body, _ := decoded["body"].(string)
			if !strings.Contains(body, "你好世界，这是腾讯云文档。") {
				t.Fatalf("gbk body not decoded: %q", body)
			}
		})
	}
}

// privateAccessRuntime is the operator opt-in that lets tests reach the
// loopback servers they start.
func privateAccessRuntime() *AgentToolRuntime {
	cfg := &appcfg.Root{}
	cfg.Agents.Defaults.WebFetch.AllowPrivateIP = true
	return &AgentToolRuntime{Cfg: cfg}
}

func fetchForTest(t *testing.T, rawURL string) string {
	t.Helper()
	tool, err := NewWebFetchTool(NewState(t.TempDir()), privateAccessRuntime())
	if err != nil {
		t.Fatalf("NewWebFetchTool: %v", err)
	}
	args, err := json.Marshal(WebFetchInput{URL: rawURL})
	if err != nil {
		t.Fatalf("marshal web_fetch args: %v", err)
	}
	out, err := tool.Handle(context.Background(), string(args))
	if err != nil {
		t.Fatalf("web_fetch: %v", err)
	}
	text, ok := out.(string)
	if !ok {
		t.Fatalf("web_fetch returned %T want string", out)
	}
	return text
}

func TestWebFetchInputIsJustTheURL(t *testing.T) {
	schema := SchemaForInputType(reflect.TypeOf(WebFetchInput{}))
	if len(schema) == 0 {
		t.Fatalf("missing schema")
	}
	var decoded map[string]any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		t.Fatalf("schema json: %v", err)
	}
	props, _ := decoded["properties"].(map[string]any)
	if len(props) != 1 {
		t.Fatalf("web_fetch must expose the URL alone, got %v: %s", props, string(schema))
	}
	if _, ok := props["url"]; !ok {
		t.Fatalf("web_fetch schema missing url property: %s", string(schema))
	}
	required, _ := decoded["required"].([]any)
	if len(required) != 1 || required[0] != "url" {
		t.Fatalf("web_fetch required must be [url], got %v: %s", required, string(schema))
	}
	// Private network access is an operator decision. Exposing it in the schema
	// would let a fetched page talk the model into reaching internal hosts.
	if _, ok := props["allow_private_ip"]; ok {
		t.Fatalf("allow_private_ip must not be model-settable: %s", string(schema))
	}
}

func TestWebFetchPrivateAccessRequiresOperatorConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("LOCAL DEV SERVER"))
	}))
	defer srv.Close()

	// A model asking for it in the arguments gets nowhere.
	denied, err := NewWebFetchTool(NewState(t.TempDir()), &AgentToolRuntime{Cfg: &appcfg.Root{}})
	if err != nil {
		t.Fatalf("NewWebFetchTool: %v", err)
	}
	args := fmt.Sprintf(`{"url":%q,"allow_private_ip":true}`, srv.URL)
	if _, err := denied.Handle(context.Background(), args); err == nil {
		t.Fatalf("loopback fetch must fail without the operator opt-in")
	}

	// The operator setting is what opens it.
	if body := fetchForTest(t, srv.URL); !strings.Contains(body, "LOCAL DEV SERVER") {
		t.Fatalf("operator opt-in should allow loopback: %q", body)
	}
}

func TestWebFetchSkipsPermissionHookWithApprovedActionID(t *testing.T) {
	st := NewState(t.TempDir())
	called := false
	st.SetActionHook(func(ctx context.Context, kind string, payload any) (string, bool, error) {
		called = true
		return "act-1", true, nil
	})
	tool, err := NewWebFetchTool(st, privateAccessRuntime())
	if err != nil {
		t.Fatalf("NewWebFetchTool: %v", err)
	}
	ctx := WithApprovedActionID(context.Background(), "act-1")
	_, err = tool.Handle(ctx, `{"url":"http://127.0.0.1:0"}`)
	if err == nil {
		t.Fatalf("expected network error without local server")
	}
	var req *RequiresActionError
	if errors.As(err, &req) {
		t.Fatalf("approved action should bypass permission hook, got %+v", req)
	}
	if called {
		t.Fatalf("action hook should not be called for approved action")
	}
}

type noopLLM struct{}

func (noopLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	return &llm.Result{}, nil
}

func TestTrimTextToUTF8BytesCapsOutput(t *testing.T) {
	got, truncated := trimTextToUTF8Bytes(strings.Repeat("x", webFetchMaxMarkdownBytes+1), webFetchMaxMarkdownBytes)
	if !truncated {
		t.Fatalf("expected truncation")
	}
	if len(got) != webFetchMaxMarkdownBytes {
		t.Fatalf("trimmed length=%d want %d", len(got), webFetchMaxMarkdownBytes)
	}
}

func TestTrimTextToUTF8BytesPreservesUTF8(t *testing.T) {
	got, truncated := trimTextToUTF8Bytes(strings.Repeat("x", webFetchMaxMarkdownBytes-1)+"あ", webFetchMaxMarkdownBytes)
	if !truncated {
		t.Fatalf("expected truncation")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("trimmed markdown must stay valid UTF-8")
	}
}

func TestTrimTextToUTF8BytesKeepsIllFormedInput(t *testing.T) {
	// Undecodable bytes must degrade to replacement runes rather than walk the
	// whole body off the end, which used to yield an empty body.
	got, truncated := trimTextToUTF8Bytes(strings.Repeat("\xc4\xe3\xba\xc3", webFetchMaxMarkdownBytes), webFetchMaxMarkdownBytes)
	if !truncated {
		t.Fatalf("expected truncation")
	}
	if got == "" {
		t.Fatalf("ill-formed body must not be discarded entirely")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("trimmed markdown must stay valid UTF-8")
	}
}

func TestWebSearchPermissionPayloadIncludesQueryRule(t *testing.T) {
	got := WebSearchPermissionPayload(" golang testing ")
	if got["query"] != "golang testing" {
		t.Fatalf("payload query mismatch: %#v", got)
	}
	if got["rule_content"] != "query:golang testing" {
		t.Fatalf("payload rule mismatch: %#v", got)
	}
}

func TestNormalizeWebSearchLimit(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{name: "default", in: 0, want: 5},
		{name: "negative", in: -1, want: 5},
		{name: "explicit", in: 7, want: 7},
		{name: "cap", in: 99, want: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeWebSearchLimit(tt.in); got != tt.want {
				t.Fatalf("NormalizeWebSearchLimit(%d)=%d want %d", tt.in, got, tt.want)
			}
		})
	}
}

// searchResultBody builds a provider response whose results each carry roughly
// `contentBytes` of prose, so the envelope overflows the tool budget.
func searchResultBody(t *testing.T, results, contentBytes int) string {
	t.Helper()
	var body strings.Builder
	body.WriteString(`{"query":"go release","results":[`)
	for i := 0; i < results; i++ {
		if i > 0 {
			body.WriteString(",")
		}
		fmt.Fprintf(&body, `{"title":"Result %d","url":"https://go.dev/doc/%d","score":%d.5,"content":%q}`,
			i+1, i+1, i+1, strings.Repeat("x", contentBytes))
	}
	body.WriteString("]}")
	return body.String()
}

func governSearchBody(t *testing.T, body string) (envelope string, decoded map[string]any) {
	t.Helper()
	envelope = governWebSearchBody([]byte(body), func(bodyText string, truncated bool) string {
		return webSearchEnvelope(200, "application/json", 10, "query:go release", bodyText, truncated)
	})
	if len(envelope) > webSearchMaxEnvelopeBytes {
		t.Fatalf("envelope=%d bytes exceeds the tool budget %d", len(envelope), webSearchMaxEnvelopeBytes)
	}
	// The governor trims head/tail past SpillThresholdBytes, which no caller can
	// undo: the search envelope has to stay below it.
	if len(envelope) > SpillThresholdBytes {
		t.Fatalf("envelope=%d bytes reaches the governor spill threshold %d", len(envelope), SpillThresholdBytes)
	}
	if governed := NewOutputGovernor(t.TempDir()).GovernDetailed("web_search", "call-1", envelope); governed.Text != envelope {
		t.Fatalf("governor rewrote a fitted search envelope (truncated=%v)", governed.Truncated)
	}
	if err := json.Unmarshal([]byte(envelope), &decoded); err != nil {
		t.Fatalf("envelope is not parseable JSON: %v", err)
	}
	return envelope, decoded
}

// decodeSearchProviderBody parses the delivered provider body, keeping numbers
// as their literal text so the test sees exactly what the model receives.
func decodeSearchProviderBody(t *testing.T, decoded map[string]any) map[string]any {
	t.Helper()
	bodyText, _ := decoded["body"].(string)
	decoder := json.NewDecoder(strings.NewReader(bodyText))
	decoder.UseNumber()
	var provider map[string]any
	if err := decoder.Decode(&provider); err != nil {
		t.Fatalf("provider body is not parseable JSON: %v\n%.600s", err, bodyText)
	}
	return provider
}

// An oversized provider response must be governed, not cut: the results at the
// tail are dropped, everything kept stays byte-identical, and both the envelope
// and the body remain parseable JSON.
func TestWebSearchOverflowDropsTrailingResultsAndKeepsValidJSON(t *testing.T) {
	t.Parallel()
	const results = 10
	_, decoded := governSearchBody(t, searchResultBody(t, results, 4000))
	provider := decodeSearchProviderBody(t, decoded)

	if decoded["truncated"] != true {
		t.Fatalf("envelope must report that results were dropped: %#v", decoded["truncated"])
	}
	kept, _ := provider["results"].([]any)
	if len(kept) == 0 || len(kept) >= results {
		t.Fatalf("kept %d of %d results; the tail must be dropped, not the whole list", len(kept), results)
	}
	for i, raw := range kept {
		item, _ := raw.(map[string]any)
		if want := fmt.Sprintf("Result %d", i+1); item["title"] != want {
			t.Fatalf("result %d title = %#v, want %q: the leading results must survive in order", i+1, item["title"], want)
		}
		if want := fmt.Sprintf("https://go.dev/doc/%d", i+1); item["url"] != want {
			t.Fatalf("result %d url = %#v, want %q", i+1, item["url"], want)
		}
		if content, _ := item["content"].(string); content != strings.Repeat("x", 4000) {
			t.Fatalf("result %d content was altered: %d bytes", i+1, len(content))
		}
		if score, _ := item["score"].(json.Number); score.String() != fmt.Sprintf("%d.5", i+1) {
			t.Fatalf("result %d score = %#v, numbers must keep their literal form", i+1, item["score"])
		}
	}

	// The card shows exactly what the model received.
	evt := StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "web_search",
		Input:    map[string]any{"query": "go release", "limit": results},
		Output:   map[string]any{"output": marshalWebToolValue(decoded)},
	}
	card, _ := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if want := fmt.Sprintf("**%d search results**", len(kept)); !strings.Contains(card, want) {
		t.Fatalf("card does not match the delivered results (%q):\n%.400s", want, card)
	}
	if !strings.Contains(card, "Some provider response data was omitted because it was too large.") {
		t.Fatalf("card must say data was omitted:\n%.400s", card)
	}
}

// A single result larger than the whole budget still reaches the model, with its
// prose shortened instead of its JSON broken.
func TestWebSearchSingleOversizedResultKeepsValidJSON(t *testing.T) {
	t.Parallel()
	_, decoded := governSearchBody(t, searchResultBody(t, 1, 200_000))
	provider := decodeSearchProviderBody(t, decoded)

	kept, _ := provider["results"].([]any)
	if len(kept) != 1 {
		t.Fatalf("kept %d results, want the single result", len(kept))
	}
	item, _ := kept[0].(map[string]any)
	if item["title"] != "Result 1" || item["url"] != "https://go.dev/doc/1" {
		t.Fatalf("result identity lost: %#v", item)
	}
	content, _ := item["content"].(string)
	if len(content) < webSearchMaxEnvelopeBytes/2 || len(content) >= 200_000 {
		t.Fatalf("oversized content = %d bytes, want it trimmed to what the budget allows", len(content))
	}
	if decoded["truncated"] != true {
		t.Fatalf("envelope must report truncation: %#v", decoded["truncated"])
	}
}

// A body that is not JSON at all (an HTML error page) is capped as text; the
// envelope stays parseable either way.
func TestWebSearchNonJSONBodyIsCappedNotBroken(t *testing.T) {
	t.Parallel()
	page := "<html><body>" + strings.Repeat("<p>service temporarily unavailable</p>", 4000) + "</body></html>"
	_, decoded := governSearchBody(t, page)

	bodyText, _ := decoded["body"].(string)
	if bodyText == "" || len(bodyText) >= len(page) {
		t.Fatalf("non-JSON body = %d of %d bytes, want it capped", len(bodyText), len(page))
	}
	if decoded["truncated"] != true || !utf8.ValidString(bodyText) {
		t.Fatalf("capped body must stay valid UTF-8 and say it was truncated: %#v", decoded["truncated"])
	}
}

// A response that already fits is passed through byte for byte.
func TestWebSearchResponseUnderBudgetIsUnchanged(t *testing.T) {
	t.Parallel()
	body := searchResultBody(t, 3, 40)
	envelope, decoded := governSearchBody(t, body)
	if decoded["truncated"] != false {
		t.Fatalf("small response reported truncation: %#v", decoded["truncated"])
	}
	if decoded["body"] != body {
		t.Fatalf("small response body was rewritten:\n%s", envelope)
	}
}

// --- DeepSeek providers ---

// clearSearchProviderEnv neutralizes ambient provider keys so the absence
// cases below are real rather than inherited from the running shell.
func clearSearchProviderEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"TAVILY_API_KEY", "BRAVE_SEARCH_API_KEY", "BAIDU_WEB_SEARCH_API_KEY", "DEEPSEEK_API_KEY",
	} {
		t.Setenv(k, "")
	}
}

func searchRuntime(prov, deepSeekKey string) *AgentToolRuntime {
	cfg := &appcfg.Root{}
	cfg.Agents.Defaults.WebSearch.Provider = prov
	if deepSeekKey != "" {
		cfg.Agents.Defaults.WebSearch.DeepSeek = &appcfg.WebSearchDeepSeek{APIKey: deepSeekKey}
	}
	return &AgentToolRuntime{Cfg: cfg}
}

func TestResolveWebSearchExecDeepSeek(t *testing.T) {
	t.Run("explicit provider with env key", func(t *testing.T) {
		clearSearchProviderEnv(t)
		t.Setenv("DEEPSEEK_API_KEY", "k")
		e, err := resolveWebSearchExec(searchRuntime("deepseek", ""))
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if e.kind != webSearchKindDeepSeek {
			t.Fatalf("kind=%d want %d", e.kind, webSearchKindDeepSeek)
		}
		if e.deepSeekURL != deepSeekSearchURL || e.deepSeekKey != "k" {
			t.Fatalf("exec=%+v", e)
		}
	})
	t.Run("explicit provider with config key", func(t *testing.T) {
		clearSearchProviderEnv(t)
		e, err := resolveWebSearchExec(searchRuntime("deepseek", "cfg-k"))
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if e.kind != webSearchKindDeepSeek || e.deepSeekKey != "cfg-k" {
			t.Fatalf("exec=%+v", e)
		}
	})
	t.Run("explicit provider without key", func(t *testing.T) {
		clearSearchProviderEnv(t)
		_, err := resolveWebSearchExec(searchRuntime("deepseek", ""))
		if err == nil || !strings.Contains(err.Error(), "DEEPSEEK_API_KEY") {
			t.Fatalf("want missing-key error naming DEEPSEEK_API_KEY, got %v", err)
		}
	})
	t.Run("auto falls through to deepseek", func(t *testing.T) {
		clearSearchProviderEnv(t)
		t.Setenv("DEEPSEEK_API_KEY", "k")
		e, err := resolveWebSearchExec(searchRuntime("", ""))
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if e.kind != webSearchKindDeepSeek {
			t.Fatalf("auto should reach deepseek at the chain tail, got kind=%d", e.kind)
		}
	})
	t.Run("auto keeps tavily ahead of deepseek", func(t *testing.T) {
		clearSearchProviderEnv(t)
		t.Setenv("DEEPSEEK_API_KEY", "k")
		t.Setenv("TAVILY_API_KEY", "tk")
		e, err := resolveWebSearchExec(searchRuntime("auto", ""))
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if e.kind != webSearchKindTavily {
			t.Fatalf("tavily must keep its place ahead of deepseek, got kind=%d", e.kind)
		}
	})
	t.Run("auto without any key", func(t *testing.T) {
		clearSearchProviderEnv(t)
		_, err := resolveWebSearchExec(searchRuntime("auto", ""))
		if err == nil || !strings.Contains(err.Error(), "DEEPSEEK_API_KEY") {
			t.Fatalf("want error naming DEEPSEEK_API_KEY, got %v", err)
		}
	})
}

func TestWebSearchDeepSeekPOST(t *testing.T) {
	var gotMethod, gotAuth, gotContentType string
	var gotPayload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotPayload); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[{"title":"t","url":"https://example.com/a","summary":"s"}]}`)
	}))
	defer srv.Close()

	e := webSearchExec{kind: webSearchKindDeepSeek, deepSeekURL: srv.URL, deepSeekKey: "k"}
	status, contentType, body, err := webSearchDeepSeekPOST(context.Background(), srv.Client(), e, "deepseek 测试", 5)
	if err != nil {
		t.Fatalf("webSearchDeepSeekPOST: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method=%q want POST", gotMethod)
	}
	if gotAuth != "Bearer k" {
		t.Fatalf("Authorization=%q", gotAuth)
	}
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Fatalf("Content-Type=%q", gotContentType)
	}
	// map[string]any payloads marshal with sorted keys, so assert the decoded
	// fields rather than the raw bytes.
	if gotPayload["query"] != "deepseek 测试" {
		t.Fatalf("query=%v", gotPayload["query"])
	}
	if gotPayload["count"] != float64(5) {
		t.Fatalf("count=%v want 5", gotPayload["count"])
	}
	if gotPayload["need_content"] != false {
		t.Fatalf("need_content=%v want false", gotPayload["need_content"])
	}
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	if !strings.Contains(string(body), "results") {
		t.Fatalf("body missing results: %.200s", body)
	}
	if !strings.Contains(contentType, "application/json") {
		t.Fatalf("contentType=%q", contentType)
	}
}

func TestWebFetchDeepSeekPOST(t *testing.T) {
	newServer := func(t *testing.T, status int, respond string, got *map[string]any, gotAuth *string) *httptest.Server {
		t.Helper()
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got != nil {
				body, _ := io.ReadAll(r.Body)
				if err := json.Unmarshal(body, got); err != nil {
					t.Errorf("request body is not JSON: %v", err)
				}
			}
			if gotAuth != nil {
				*gotAuth = r.Header.Get("Authorization")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			fmt.Fprint(w, respond)
		}))
	}
	t.Run("ok", func(t *testing.T) {
		var got map[string]any
		var gotAuth string
		srv := newServer(t, http.StatusOK, `{"title":"Example","content":"# Example\n\n正文"}`, &got, &gotAuth)
		defer srv.Close()

		client := &http.Client{Timeout: 30 * time.Second}
		res, truncated, err := webFetchDeepSeekPOST(context.Background(), client, srv.URL, "k", "https://example.com/page", defaultWebFetchMaxBytes)
		if err != nil {
			t.Fatalf("webFetchDeepSeekPOST: %v", err)
		}
		if res.status != http.StatusOK {
			t.Fatalf("status=%d", res.status)
		}
		if res.title != "Example" {
			t.Fatalf("title=%q", res.title)
		}
		if res.content != "# Example\n\n正文" {
			t.Fatalf("content=%q", res.content)
		}
		if truncated {
			t.Fatalf("unexpected truncation")
		}
		if gotAuth != "Bearer k" {
			t.Fatalf("Authorization=%q", gotAuth)
		}
		if got["url"] != "https://example.com/page" {
			t.Fatalf("payload=%v", got)
		}
	})
	t.Run("caps content to maxB", func(t *testing.T) {
		// Pure ASCII so the rune-boundary trim lands exactly on the cap.
		srv := newServer(t, http.StatusOK, `{"title":"x","content":"0123456789abcdefghij"}`, nil, nil)
		defer srv.Close()

		client := &http.Client{Timeout: 30 * time.Second}
		res, truncated, err := webFetchDeepSeekPOST(context.Background(), client, srv.URL, "k", "https://example.com/", 16)
		if err != nil {
			t.Fatalf("webFetchDeepSeekPOST: %v", err)
		}
		if res.status != http.StatusOK || len(res.errBody) != 0 {
			t.Fatalf("res=%+v", res)
		}
		if !truncated {
			t.Fatalf("expected truncation at maxB=16")
		}
		if len(res.content) != 16 || res.content != "0123456789abcdef" {
			t.Fatalf("content=%q len=%d", res.content, len(res.content))
		}
	})
	t.Run("error body passthrough", func(t *testing.T) {
		srv := newServer(t, http.StatusNotFound,
			`{"error":{"message":"fetch failed","type":"invalid_request_error","param":null,"code":"not_found"}}`, nil, nil)
		defer srv.Close()

		client := &http.Client{Timeout: 30 * time.Second}
		res, truncated, err := webFetchDeepSeekPOST(context.Background(), client, srv.URL, "k", "https://example.com/", defaultWebFetchMaxBytes)
		if err != nil {
			t.Fatalf("webFetchDeepSeekPOST: %v", err)
		}
		if res.status != http.StatusNotFound {
			t.Fatalf("status=%d", res.status)
		}
		if len(res.errBody) == 0 || !strings.Contains(string(res.errBody), "not_found") {
			t.Fatalf("errBody=%q", res.errBody)
		}
		if truncated {
			t.Fatalf("error responses are not download-truncated")
		}
	})
}

func TestResolveWebFetchDeepSeek(t *testing.T) {
	fetchRuntime := func(prov, key string) *AgentToolRuntime {
		cfg := &appcfg.Root{}
		cfg.Agents.Defaults.WebFetch.Provider = prov
		if key != "" {
			cfg.Agents.Defaults.WebFetch.DeepSeek = &appcfg.WebFetchDeepSeek{APIKey: key}
		}
		return &AgentToolRuntime{Cfg: cfg}
	}
	t.Run("empty provider stays direct", func(t *testing.T) {
		t.Setenv("DEEPSEEK_API_KEY", "")
		_, _, ok, err := resolveWebFetchDeepSeek(fetchRuntime("", "k"))
		if ok || err != nil {
			t.Fatalf("ok=%v err=%v, want the built-in direct fetcher", ok, err)
		}
	})
	t.Run("other provider stays direct", func(t *testing.T) {
		t.Setenv("DEEPSEEK_API_KEY", "")
		_, _, ok, err := resolveWebFetchDeepSeek(fetchRuntime("builtin", "k"))
		if ok || err != nil {
			t.Fatalf("ok=%v err=%v, want the built-in direct fetcher", ok, err)
		}
	})
	t.Run("deepseek without key", func(t *testing.T) {
		t.Setenv("DEEPSEEK_API_KEY", "")
		_, _, _, err := resolveWebFetchDeepSeek(fetchRuntime("deepseek", ""))
		if err == nil || !strings.Contains(err.Error(), "agents.defaults.web_fetch.deepseek.api_key") {
			t.Fatalf("want error naming agents.defaults.web_fetch.deepseek.api_key, got %v", err)
		}
	})
	t.Run("deepseek with config key", func(t *testing.T) {
		t.Setenv("DEEPSEEK_API_KEY", "")
		endpoint, key, ok, err := resolveWebFetchDeepSeek(fetchRuntime("deepseek", "cfg-k"))
		if err != nil || !ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		if endpoint != deepSeekFetchURL || key != "cfg-k" {
			t.Fatalf("endpoint=%q key=%q", endpoint, key)
		}
	})
	t.Run("deepseek with env key", func(t *testing.T) {
		t.Setenv("DEEPSEEK_API_KEY", "env-k")
		endpoint, key, ok, err := resolveWebFetchDeepSeek(fetchRuntime("deepseek", ""))
		if err != nil || !ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		if endpoint != deepSeekFetchURL || key != "env-k" {
			t.Fatalf("endpoint=%q key=%q", endpoint, key)
		}
	})
}

// TestDeepSeekSearchFetchIntegration hits the real DeepSeek Search and Fetch
// endpoints once each. It self-skips without FOREBRAIN_DEEPSEEK_KEY so the
// default suite never spends money; each successful call bills per request.
func TestDeepSeekSearchFetchIntegration(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("FOREBRAIN_DEEPSEEK_KEY"))
	if key == "" {
		t.Skip("FOREBRAIN_DEEPSEEK_KEY not set; skipping DeepSeek Search/Fetch integration test")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	t.Run("search", func(t *testing.T) {
		e := webSearchExec{kind: webSearchKindDeepSeek, deepSeekURL: deepSeekSearchURL, deepSeekKey: key}
		status, _, body, err := webSearchDeepSeekPOST(context.Background(), client, e, "DeepSeek", 5)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if status != http.StatusOK {
			t.Fatalf("search status=%d body=%.400s", status, body)
		}
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("search body is not JSON: %v", err)
		}
		if _, ok := parsed["results"]; !ok {
			t.Fatalf("search response missing results: %.400s", body)
		}
	})
	t.Run("fetch", func(t *testing.T) {
		res, _, err := webFetchDeepSeekPOST(context.Background(), client, deepSeekFetchURL, key, "https://example.com", defaultWebFetchMaxBytes)
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		if res.status != http.StatusOK {
			t.Fatalf("fetch status=%d body=%.400s", res.status, res.errBody)
		}
	})
}
