package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
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

// TestToolResultContentDegradesUnsendableImageBlock pins the #3 fix: a block
// whose part cannot be built (here a webp over budget, which no standard-
// library decoder could downscale) is degraded to a text line naming the
// saved file and the reason. The text envelope survives, the good PNG block
// still yields its image part, and the surface states file facts instead of
// pretending the block was sent.
func TestToolResultContentDegradesUnsendableImageBlock(t *testing.T) {
	home := t.TempDir()
	dir := resultsDirFor(home)
	webp := make([]byte, 4<<20)
	copy(webp, "RIFF\x00\x00\x00\x00WEBP")
	raw := `{"content":[{"type":"text","text":"mixed shot"},` +
		`{"type":"image","mimeType":"image/webp","data":"` + base64.StdEncoding.EncodeToString(webp) + `"},` +
		`{"type":"image","mimeType":"image/png","data":"` + base64.StdEncoding.EncodeToString(onePixelPNG(t)) + `"}]}`

	ctx := tool.WithToolCompletionCapture(context.Background())
	parts, err := ToolResultContent(ctx, dir, "shots", "screenshot", raw, accounting{}, "")
	if err != nil {
		t.Fatalf("ToolResultContent: %v", err)
	}
	images := 0
	for _, p := range parts {
		if p.Type == llm.ContentTypeImageBase64 {
			images++
		}
	}
	if images != 1 {
		t.Fatalf("expected exactly the PNG part (1 image), got %d in %#v", images, parts)
	}
	text := llm.TextContent(parts...)
	if !strings.Contains(text, "mixed shot") {
		t.Fatalf("text block was lost with the degraded image block: %q", text)
	}
	if !strings.Contains(text, "was not sent as an image") || !strings.Contains(text, "webp cannot be downscaled") {
		t.Fatalf("degraded block line missing reason: %q", text)
	}
	if !strings.Contains(text, dir) {
		t.Fatalf("degraded line does not name the saved path: %q", text)
	}
	// The webp was persisted before the part was attempted — the degraded
	// line must point at a file that exists.
	webpFiles, err := filepath.Glob(filepath.Join(dir, "*.webp"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(webpFiles) != 1 {
		t.Fatalf("expected the webp persisted, got %v", webpFiles)
	}
	captured, ok := tool.ToolCompletionFromContext(ctx)
	if !ok || captured.Output == nil {
		t.Fatalf("surface output not captured onto ctx")
	}
	if got := captured.Output["image_bytes"]; got != len(webp) {
		t.Fatalf("surface image_bytes = %v, want the on-disk size %d", got, len(webp))
	}
	if got := captured.Output["image_scaled"]; got != false {
		t.Fatalf("surface image_scaled = %v, want false (no scale ran)", got)
	}
	if got := captured.Output["image_width"]; got != 0 {
		t.Fatalf("surface image_width = %v, want 0 (fake webp header does not parse)", got)
	}
}

// TestToolResultContentRefusalNeverFails pins the #4 fix end to end at this
// layer: a session whose model refuses images gets the text envelope for a
// result that even contains an unsendable block — the gate runs before any
// part is built, so no ImagePartWithin failure can fail a refused call.
func TestToolResultContentRefusalNeverFails(t *testing.T) {
	home := t.TempDir()
	dir := resultsDirFor(home)
	webp := make([]byte, 4<<20)
	copy(webp, "RIFF\x00\x00\x00\x00WEBP")
	raw := `{"content":[{"type":"image","mimeType":"image/webp","data":"` + base64.StdEncoding.EncodeToString(webp) + `"}]}`

	ctx := tool.WithToolCompletionCapture(context.Background())
	parts, err := ToolResultContent(ctx, dir, "shots", "screenshot", raw, accounting{}, "session model does not accept image input")
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
	captured, ok := tool.ToolCompletionFromContext(ctx)
	if !ok || captured.Output == nil {
		t.Fatalf("surface output not captured onto ctx")
	}
	if got := captured.Output["image_width"]; got != 0 {
		t.Fatalf("surface image_width = %v, want 0 for an unparseable fake webp header", got)
	}
}

// TestToolResultContentRefusalParsesHeaderForSurface proves the refused path
// states real file facts rather than zeros by convention: the header
// dimensions of a valid PNG appear on the surface even though no part was
// built.
func TestToolResultContentRefusalParsesHeaderForSurface(t *testing.T) {
	home := t.TempDir()
	dir := resultsDirFor(home)
	png := onePixelPNG(t)
	raw := `{"content":[{"type":"image","mimeType":"image/png","data":"` + base64.StdEncoding.EncodeToString(png) + `"}]}`

	ctx := tool.WithToolCompletionCapture(context.Background())
	_, err := ToolResultContent(ctx, dir, "shots", "screenshot", raw, accounting{}, "session model does not accept image input")
	if err != nil {
		t.Fatalf("ToolResultContent: %v", err)
	}
	captured, ok := tool.ToolCompletionFromContext(ctx)
	if !ok || captured.Output == nil {
		t.Fatalf("surface output not captured onto ctx")
	}
	if got := captured.Output["image_width"]; got != 1 {
		t.Fatalf("surface image_width = %v, want the parsed header width 1", got)
	}
	if got := captured.Output["image_height"]; got != 1 {
		t.Fatalf("surface image_height = %v, want the parsed header height 1", got)
	}
	if got := captured.Output["image_bytes"]; got != len(png) {
		t.Fatalf("surface image_bytes = %v, want the on-disk size %d", got, len(png))
	}
}

// TestToolResultContentRefusesOversizedBlock pins the decode-limit guard on
// both block kinds: a block whose decoded size exceeds the per-block ceiling
// is neither decoded nor written to disk — the envelope gets the limit line
// instead, and the blob/resource path behaves the same.
func TestToolResultContentRefusesOversizedBlock(t *testing.T) {
	home := t.TempDir()
	dir := resultsDirFor(home)
	// (64<<20)+4 base64 chars decode to 48 MiB+3 bytes — just over the limit.
	huge := strings.Repeat("A", (64<<20)+4)
	raw := `{"content":[{"type":"text","text":"before"},` +
		`{"type":"image","mimeType":"image/png","data":"` + huge + `"}]}`

	ctx := tool.WithToolCompletionCapture(context.Background())
	parts, err := ToolResultContent(ctx, dir, "shots", "screenshot", raw, accounting{}, "")
	if err != nil {
		t.Fatalf("ToolResultContent: %v", err)
	}
	for _, p := range parts {
		if p.Type == llm.ContentTypeImageBase64 {
			t.Fatalf("oversized block still produced an image part: %#v", parts)
		}
	}
	if !strings.Contains(llm.TextContent(parts...), "exceeds the 48 MiB per-block limit and was not saved") {
		t.Fatalf("envelope missing the per-block limit line: %q", llm.TextContent(parts...))
	}
	if !strings.Contains(llm.TextContent(parts...), "before") {
		t.Fatalf("text block lost with the refused block: %q", llm.TextContent(parts...))
	}
	files, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("oversized block must not be written to disk, got %v", files)
	}

	// Same-shaped case on the embedded-resource blob path.
	dir2 := resultsDirFor(t.TempDir())
	blobRaw := `{"content":[{"type":"resource","resource":{"uri":"file://big.bin","mimeType":"application/octet-stream","blob":"` + huge + `"}}]}`
	got, err := ProcessToolResultJSON(dir2, "files", "read", blobRaw)
	if err != nil {
		t.Fatalf("ProcessToolResultJSON error: %v", err)
	}
	if !strings.Contains(got, "exceeds the 48 MiB per-block limit and was not saved") {
		t.Fatalf("blob path missing the per-block limit line: %s", got)
	}
	files2, err := filepath.Glob(filepath.Join(dir2, "*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files2) != 0 {
		t.Fatalf("oversized blob must not be written to disk, got %v", files2)
	}
}

// TestToolResultContentDegradesUndecodableImageBlock pins the 060 fix on the
// tool-result path: an image block whose data is not valid base64 is degraded
// to a text line naming the declared MIME — never left in the envelope — so no
// image part is built, nothing lands on disk, and the truncation path
// resultJSONContainsImage would force never triggers because the repacked
// result has no image block left.
func TestToolResultContentDegradesUndecodableImageBlock(t *testing.T) {
	home := t.TempDir()
	dir := resultsDirFor(home)
	raw := `{"content":[{"type":"image","mimeType":"image/png","data":"!!!!not-base64!!!!"}]}`

	parts, err := ToolResultContent(context.Background(), dir, "shots", "screenshot", raw, accounting{}, "")
	if err != nil {
		t.Fatalf("ToolResultContent: %v", err)
	}
	for _, p := range parts {
		if p.Type == llm.ContentTypeImageBase64 {
			t.Fatalf("undecodable block produced an image part: %#v", parts)
		}
	}
	text := llm.TextContent(parts...)
	if !strings.Contains(text, "Binary content (image/png, 18 characters) could not be base64-decoded and was not sent.") {
		t.Fatalf("envelope missing the decode-failure line: %q", text)
	}
	if strings.Contains(text, "!!!!not-base64!!!!") {
		t.Fatalf("raw undecodable data reached the model as text: %q", text)
	}
	if strings.Contains(text, `"type":"image"`) {
		t.Fatalf("repacked envelope still carries an image block: %q", text)
	}
	if strings.Contains(text, "OUTPUT TRUNCATED") {
		t.Fatalf("decode failure forced the truncation path: %q", text)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("undecodable block must not be written to disk, got %v", files)
	}

	// Same-shaped case on the embedded-resource blob path (its twin fix).
	dir2 := resultsDirFor(t.TempDir())
	blobRaw := `{"content":[{"type":"resource","resource":{"uri":"file://x.bin","mimeType":"application/octet-stream","blob":"!!!!not-base64!!!!"}}]}`
	got, err := ProcessToolResultJSON(dir2, "files", "read", blobRaw)
	if err != nil {
		t.Fatalf("ProcessToolResultJSON error: %v", err)
	}
	if !strings.Contains(got, "Binary content (application/octet-stream, 18 characters) could not be base64-decoded and was not sent.") {
		t.Fatalf("blob path missing the decode-failure line: %s", got)
	}
	if strings.Contains(got, "!!!!not-base64!!!!") {
		t.Fatalf("blob path leaked raw undecodable data: %s", got)
	}
}

// TestToolResultContentDegradesUndecodableImageBlockAmongGoodOnes is the
// one-bad-block extension: the good block still lands on disk and yields its
// image part, the bad block degrades to its line, and the envelope keeps the
// plain text block — a hostile block never costs the call.
func TestToolResultContentDegradesUndecodableImageBlockAmongGoodOnes(t *testing.T) {
	home := t.TempDir()
	dir := resultsDirFor(home)
	png := onePixelPNG(t)
	raw := `{"content":[{"type":"text","text":"mixed shot"},` +
		`{"type":"image","mimeType":"image/png","data":"!!!!not-base64!!!!"},` +
		`{"type":"image","mimeType":"image/png","data":"` + base64.StdEncoding.EncodeToString(png) + `"}]}`

	parts, err := ToolResultContent(context.Background(), dir, "shots", "screenshot", raw, accounting{}, "")
	if err != nil {
		t.Fatalf("ToolResultContent: %v", err)
	}
	images := 0
	for _, p := range parts {
		if p.Type == llm.ContentTypeImageBase64 {
			images++
		}
	}
	if images != 1 {
		t.Fatalf("expected exactly the good block's part (1 image), got %d in %#v", images, parts)
	}
	text := llm.TextContent(parts...)
	if !strings.Contains(text, "mixed shot") {
		t.Fatalf("text block was lost with the degraded block: %q", text)
	}
	if !strings.Contains(text, "could not be base64-decoded") {
		t.Fatalf("envelope missing the decode-failure line: %q", text)
	}
	if !strings.Contains(text, "Binary content saved to") {
		t.Fatalf("good block lost its saved-path line: %q", text)
	}
	if strings.Contains(text, "!!!!not-base64!!!!") {
		t.Fatalf("raw undecodable data reached the model as text: %q", text)
	}
}

// TestToolResultContentRefusalDegradesUndecodableImageBlock pins the gate's
// interaction with the fix: a refused session model gets the decode-failure
// line like any other, and because nothing could be persisted, no surface
// facts are captured for the block.
func TestToolResultContentRefusalDegradesUndecodableImageBlock(t *testing.T) {
	home := t.TempDir()
	dir := resultsDirFor(home)
	raw := `{"content":[{"type":"image","mimeType":"image/png","data":"!!!!not-base64!!!!"}]}`

	ctx := tool.WithToolCompletionCapture(context.Background())
	parts, err := ToolResultContent(ctx, dir, "shots", "screenshot", raw, accounting{}, "session model does not accept image input")
	if err != nil {
		t.Fatalf("ToolResultContent: %v", err)
	}
	for _, p := range parts {
		if p.Type == llm.ContentTypeImageBase64 {
			t.Fatalf("refused result still carried an image part: %#v", parts)
		}
	}
	if !strings.Contains(llm.TextContent(parts...), "could not be base64-decoded") {
		t.Fatalf("refused result lost the decode-failure line: %q", llm.TextContent(parts...))
	}
	if _, ok := tool.ToolCompletionFromContext(ctx); ok {
		t.Fatalf("surface facts captured for a block that never produced a file")
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

// noisePNG returns an incompressible w×h PNG: random pixels keep the encoded
// size at essentially the raw size, so a test can put an image over the
// default 3 MiB part budget without committing a binary fixture. A fixed seed
// keeps it deterministic.
func noisePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	rng := rand.New(rand.NewSource(1))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(rng.Intn(256)), G: uint8(rng.Intn(256)), B: uint8(rng.Intn(256)), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode noise png: %v", err)
	}
	return buf.Bytes()
}

// TestToolResultImageRoundTripsThroughFileReference pins the replay/live 1:1
// contract for an MCP image block end to end: the block is persisted to the
// results dir, the live part is budgeted through the ImagePartWithin ladder,
// the transcript row records a file_reference (never base64), and the reload
// seam re-derives — from the persisted file, with the persisted MIME — the
// exact part the live turn sent, so a replayed request is byte-identical.
func TestToolResultImageRoundTripsThroughFileReference(t *testing.T) {
	dir := resultsDirFor(t.TempDir())
	raw := noisePNG(t, 1024, 1024)
	payload := base64.StdEncoding.EncodeToString(raw)
	block := `{"content":[{"type":"image","mimeType":"image/png","data":"` + payload + `"}]}`

	// Live: what the session holds in memory after the tool call.
	parts, err := ToolResultContent(context.Background(), dir, "shots", "screenshot", block, accounting{}, "")
	if err != nil {
		t.Fatalf("ToolResultContent: %v", err)
	}
	var live llm.ContentPart
	for _, p := range parts {
		if p.Type == llm.ContentTypeImageBase64 {
			live = p
		}
	}
	if live.ImageBase64 == "" {
		t.Fatalf("no image part in %#v", parts)
	}
	if live.ImageBase64 == payload {
		t.Fatal("live part carried the verbatim bytes; the ladder did not run")
	}
	if live.SourcePath == "" || !strings.HasPrefix(live.SourcePath, dir) {
		t.Fatalf("live part source path = %q, want one under %q", live.SourcePath, dir)
	}

	// Persist through the same seam message_sync uses, then reload through the
	// load seam: file refs out of the row, placeholder into the parts.
	liveMsg := llm.ToolResultMessage("call-1", parts...)
	partsJSON := state.MessagePartsJSON(liveMsg, "")
	if strings.Contains(partsJSON, "base64") {
		t.Fatalf("persisted parts_json carries base64: %s", previewOf(partsJSON))
	}
	if !strings.Contains(partsJSON, `"file_reference"`) {
		t.Fatalf("persisted parts_json lacks file_reference: %s", previewOf(partsJSON))
	}
	refs := state.ExtractFileRefInfos(partsJSON)
	if len(refs) != 1 || refs[0].FileID != live.SourcePath || refs[0].MIMEType != live.MIMEType {
		t.Fatalf("file refs = %#v, want the live part's source path and MIME", refs)
	}

	// Rehydrate with the tool-row semantics pkg/run applies (run.
	// rehydrateMessageImages: os.ReadFile + ImagePartWithin(path, persisted
	// MIME, bytes, 0)) and require the exact live part back.
	data, err := os.ReadFile(refs[0].FileID)
	if err != nil {
		t.Fatalf("read persisted result: %v", err)
	}
	rebuilt, _, err := llm.ImagePartWithin(refs[0].FileID, refs[0].MIMEType, data, 0)
	if err != nil {
		t.Fatalf("re-derive part: %v", err)
	}
	if rebuilt != live {
		t.Fatalf("rehydrated part != live part:\nlive    = MIME %q, %d bytes of base64, source %q\nrebuilt = MIME %q, %d bytes of base64, source %q",
			live.MIMEType, len(live.ImageBase64), live.SourcePath, rebuilt.MIMEType, len(rebuilt.ImageBase64), rebuilt.SourcePath)
	}
}

func previewOf(s string) string {
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
