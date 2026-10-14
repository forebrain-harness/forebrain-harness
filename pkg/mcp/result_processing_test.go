package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// resultsDirFor builds the directory a Session would inject: the primary agent
// workspace's state/mcp-results. The result-processing helpers take this
// directory directly (persistMCPResult uses it as the directory itself), so a
// test that used the home root here would no longer exercise production.
func resultsDirFor(home string) string {
	return filepath.Join(home, "state", "mcp-results")
}

// onePixelPNG is a valid 1x1 PNG, generated in the test so no binary fixture is
// committed.
func onePixelPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestProcessToolResultJSONPersistsLargeTextOutput(t *testing.T) {
	t.Setenv("MAX_MCP_OUTPUT_TOKENS", "1")
	home := t.TempDir()
	dir := resultsDirFor(home)
	raw := `{"content":[{"type":"text","text":"` + strings.Repeat("x", 128) + `"}]}`

	got, err := ProcessToolResultJSON(dir, "docs server", "search", raw)
	if err != nil {
		t.Fatalf("ProcessToolResultJSON error: %v", err)
	}
	if !strings.Contains(got, "saved to") || !strings.Contains(got, "state/mcp-results") {
		t.Fatalf("expected persisted output instructions, got: %s", got)
	}
	files, err := filepath.Glob(filepath.Join(dir, "mcp-docs_server-search-*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one persisted result file, got %v", files)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read persisted result: %v", err)
	}
	if !strings.Contains(string(b), strings.Repeat("x", 64)) {
		t.Fatalf("persisted file missing result content: %q", string(b))
	}
}

func TestProcessToolResultJSONTruncatesLargeOutputWhenPersistenceDisabled(t *testing.T) {
	t.Setenv("MAX_MCP_OUTPUT_TOKENS", "1")
	t.Setenv("ENABLE_MCP_LARGE_OUTPUT_FILES", "0")
	home := t.TempDir()
	raw := `{"content":[{"type":"text","text":"` + strings.Repeat("y", 128) + `"}]}`

	got, err := ProcessToolResultJSON(resultsDirFor(home), "docs", "search", raw)
	if err != nil {
		t.Fatalf("ProcessToolResultJSON error: %v", err)
	}
	if strings.Contains(got, "saved to") {
		t.Fatalf("expected truncation, got persisted instructions: %s", got)
	}
	if !strings.Contains(got, "OUTPUT TRUNCATED") {
		t.Fatalf("expected truncation marker, got: %s", got)
	}
}

func TestProcessToolResultJSONPersistsBinaryResourceContent(t *testing.T) {
	home := t.TempDir()
	dir := resultsDirFor(home)
	raw := `{"content":[{"type":"resource","resource":{"uri":"file://report.pdf","mimeType":"application/pdf","blob":"cGRmLWJ5dGVz"}}]}`

	got, err := ProcessToolResultJSON(dir, "files", "read", raw)
	if err != nil {
		t.Fatalf("ProcessToolResultJSON error: %v", err)
	}
	if strings.Contains(got, "cGRmLWJ5dGVz") {
		t.Fatalf("base64 blob leaked into output: %s", got)
	}
	if !strings.Contains(got, "Binary content saved to") || !strings.Contains(got, ".pdf") {
		t.Fatalf("expected binary saved message, got: %s", got)
	}
	files, err := filepath.Glob(filepath.Join(dir, "mcp-files-blob-*.pdf"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one binary file, got %v", files)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read binary file: %v", err)
	}
	if string(b) != "pdf-bytes" {
		t.Fatalf("binary content=%q", string(b))
	}
}

// TestToolResultContentReturnsImageParts pins the core of the change: an MCP
// result that carries an image block reaches the model as an image part (plus
// the text envelope), while the block's base64 is replaced by a saved-path line
// so it never rides in the text. The saved path must be under the injected
// results directory, not home. It also pins Step 5.4: the surface-facing output
// captured onto ctx carries preview_kind/path/dimensions and never base64.
func TestToolResultContentReturnsImageParts(t *testing.T) {
	home := t.TempDir()
	dir := resultsDirFor(home)
	png := onePixelPNG(t)
	raw := `{"content":[{"type":"image","mimeType":"image/png","data":"` + base64.StdEncoding.EncodeToString(png) + `"}]}`

	ctx := tool.WithToolCompletionCapture(context.Background())
	parts, err := ToolResultContent(ctx, dir, "shots", "screenshot", raw, accounting{}, "")
	if err != nil {
		t.Fatalf("ToolResultContent: %v", err)
	}
	images := 0
	for _, p := range parts {
		if p.Type == llm.ContentTypeImageBase64 {
			images++
			if p.MIMEType != "image/png" {
				t.Fatalf("image mime = %q want image/png", p.MIMEType)
			}
		}
	}
	if images != 1 {
		t.Fatalf("expected one image part, got %d in %#v", images, parts)
	}
	text := llm.TextContent(parts...)
	if !strings.Contains(text, "Binary content saved to") {
		t.Fatalf("text envelope missing saved-path line: %q", text)
	}
	if strings.Contains(text, base64.StdEncoding.EncodeToString(png)) {
		t.Fatalf("base64 leaked into the text envelope")
	}
	if !strings.Contains(text, dir) {
		t.Fatalf("saved path not under the injected results dir %q: %q", dir, text)
	}
	captured, ok := tool.ToolCompletionFromContext(ctx)
	if !ok || captured.Output == nil {
		t.Fatalf("surface output not captured onto ctx")
	}
	if captured.Output["preview_kind"] != "image" {
		t.Fatalf("captured preview_kind = %v, want image", captured.Output["preview_kind"])
	}
	if got := captured.Output["abs_path"]; got == nil || !strings.HasPrefix(got.(string), dir) {
		t.Fatalf("captured abs_path = %v, want it under %q", got, dir)
	}
	encoded, err := json.Marshal(captured.Output)
	if err != nil {
		t.Fatalf("marshal captured output: %v", err)
	}
	if strings.Contains(string(encoded), base64.StdEncoding.EncodeToString(png)) {
		t.Fatalf("base64 leaked into the captured surface output")
	}
}

// TestToolResultContentKeepsBase64OutOfText is the leak guard: no form of the
// returned content may carry the block's base64.
func TestToolResultContentKeepsBase64OutOfText(t *testing.T) {
	dir := resultsDirFor(t.TempDir())
	data := base64.StdEncoding.EncodeToString(onePixelPNG(t))
	raw := `{"content":[{"type":"image","mimeType":"image/png","data":"` + data + `"}]}`

	parts, err := ToolResultContent(context.Background(), dir, "shots", "screenshot", raw, accounting{}, "")
	if err != nil {
		t.Fatalf("ToolResultContent: %v", err)
	}
	encoded, err := json.Marshal(parts)
	if err != nil {
		t.Fatalf("marshal parts: %v", err)
	}
	// The image part legitimately carries base64; the TEXT part must not.
	if strings.Contains(llm.TextContent(parts...), data) {
		t.Fatalf("base64 leaked into the text part")
	}
	// Sanity: the base64 does appear, because an image part carries it.
	if !strings.Contains(string(encoded), data) {
		t.Fatalf("image part did not carry the base64 payload")
	}
}

// TestToolResultContentRefusalReturnsTextOnly pins the gate's effect: a refused
// session model gets only the text envelope (with the saved-path line), never
// an image part, so the request cannot 400.
func TestToolResultContentRefusalReturnsTextOnly(t *testing.T) {
	dir := resultsDirFor(t.TempDir())
	raw := `{"content":[{"type":"image","mimeType":"image/png","data":"` + base64.StdEncoding.EncodeToString(onePixelPNG(t)) + `"}]}`

	parts, err := ToolResultContent(context.Background(), dir, "shots", "screenshot", raw, accounting{}, "the session model does not accept image input")
	if err != nil {
		t.Fatalf("ToolResultContent: %v", err)
	}
	for _, p := range parts {
		if p.Type == llm.ContentTypeImageBase64 {
			t.Fatalf("refused result still carried an image part: %#v", parts)
		}
	}
	if !strings.Contains(llm.TextContent(parts...), "Binary content saved to") {
		t.Fatalf("refused result lost its saved-path line: %q", llm.TextContent(parts...))
	}
}

// TestToolResultErrorMapsIsErrorToFailure pins parity with callToolAttempt: an
// isError result is reported as an error, not a success whose text happens to
// describe a problem.
func TestToolResultErrorMapsIsErrorToFailure(t *testing.T) {
	if err := toolResultError(&mcpsdk.CallToolResult{IsError: true}, "boom"); err == nil || err.Error() != "boom" {
		t.Fatalf("IsError result: err=%v want the text as the error", err)
	}
	if err := toolResultError(&mcpsdk.CallToolResult{}, "ok"); err != nil {
		t.Fatalf("successful result must not error: %v", err)
	}
	if err := toolResultError(nil, "ok"); err != nil {
		t.Fatalf("nil result must not error: %v", err)
	}
}

// TestSessionResultsRootFallsBackToHome pins the embedder fallback: a session
// that never had SetResultsDir called still writes under <home>/state/mcp-results
// (the historical location), and an injected results dir takes precedence — so
// the production path can never quietly regress back to home.
func TestSessionResultsRootFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	sess := &Session{home: home}
	if got, want := sess.resultsRootValue(), filepath.Join(home, "state", "mcp-results"); got != want {
		t.Fatalf("uninjected results root = %q, want %q", got, want)
	}
	injected := filepath.Join(t.TempDir(), "workspace", "state", "mcp-results")
	sess.SetResultsDir(injected)
	if got := sess.resultsRootValue(); got != injected {
		t.Fatalf("injected results root = %q, want %q", got, injected)
	}
	if got := (&Session{}).resultsRootValue(); got != "" {
		t.Fatalf("session with neither home nor results dir = %q, want empty", got)
	}
}

// The helpers below were production functions that only these tests called:
// each is a thin wrapper over the live function underneath. They live here
// so the production files carry no unused code.

func ProcessToolResultJSON(resultsDir, serverName, toolName, raw string) (string, error) {
	return processToolResultJSON(resultsDir, serverName, toolName, raw, accounting{})
}
