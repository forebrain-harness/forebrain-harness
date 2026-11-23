package run

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// newCaptureRunnerForTest builds the smallest Runner a capture needs: a home,
// a workspace root and a config. The tool is registered through the same
// collector path the skill tool uses, so what these tests exercise is the
// registered tool, not a helper.
func newCaptureRunnerForTest(t *testing.T, cfg *appcfg.Root) (*Runner, *tool.State, *llm.Tool) {
	t.Helper()
	// The state root is deliberately outside $TMPDIR: macOS's screencapture
	// refuses to write into some /var/folders/... subdirectories a test binary
	// has just created ("cannot write file to intended destination", exit 0),
	// which turns a working capture into a false failure. A production state
	// root lives in the Forebrain home, not in a temp directory.
	home, err := os.MkdirTemp("/tmp", "fb-capture-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	if cfg == nil {
		cfg = &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite}
	}
	r := &Runner{Deps: (&Deps{Home: home}).WithConfig(cfg)}
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	r.WorkspaceRoot = workspace
	st := tool.NewState(workspace)
	collector := &toolCollector{}
	if err := RegisterCaptureTool(collector, st, r); err != nil {
		t.Fatal(err)
	}
	tl := collector.byName("capture")
	if tl == nil {
		t.Fatal("capture tool was not registered")
	}
	return r, st, tl
}

// authorizeWithoutAsking is the approval hook of a policy that already allows
// the escalation: the surface answered "no approval needed", which is what a
// remembered rule or an approvals-bypassed run reports.
func authorizeWithoutAsking(st *tool.State) {
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) { return "", false, nil })
}

func captureResult(t *testing.T, tl *llm.Tool, ctx context.Context, in CaptureInput) ([]llm.ContentPart, error) {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := tl.Execute(ctx, string(raw))
	if err != nil {
		return nil, err
	}
	parts, ok := execution.Result.([]llm.ContentPart)
	if !ok {
		t.Fatalf("result is %T, want content parts", execution.Result)
	}
	return parts, nil
}

// captureShares is the note/part split every successful capture returns.
func captureShares(t *testing.T, parts []llm.ContentPart, wantPath string) (string, llm.ContentPart) {
	t.Helper()
	var note string
	var imagePart llm.ContentPart
	for _, part := range parts {
		if part.Type == llm.ContentTypeText {
			note += part.Text
		}
		if part.Type == llm.ContentTypeImageBase64 {
			imagePart = part
		}
	}
	if note == "" || imagePart.Type == "" {
		t.Fatalf("a capture must return text and an image part, got %+v", parts)
	}
	if wantPath != "" && !strings.Contains(note, wantPath) {
		t.Fatalf("the note does not name %q:\n%s", wantPath, note)
	}
	return note, imagePart
}

// pngDimensions decodes the header of a PNG on disk.
func pngDimensions(t *testing.T, path string) (int, int) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	cfg, err := png.DecodeConfig(file)
	if err != nil {
		t.Fatalf("%s is not a png: %v", path, err)
	}
	return cfg.Width, cfg.Height
}

// assertCaptureNote pins what a capture's note promises whatever the screen
// produced: the path, the mode, the bytes the file holds, and a dimension line
// that describes that file — its own dimensions when the capture fits the image
// budget, and as the "downscaled from" source when it does not. Whether this
// display's capture is big enough to be downscaled is the environment's
// business, not the test's.
func assertCaptureNote(t *testing.T, note, mode, path string, width, height int) {
	t.Helper()
	if !strings.HasPrefix(note, path+"\n"+mode+" capture ") {
		t.Fatalf("the note does not open with the path and the mode %q:\n%s", mode, note)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the note names %s but nothing is there: %v", path, err)
	}
	if !strings.Contains(note, fmt.Sprintf(", %d bytes on disk", info.Size())) {
		t.Fatalf("the note does not carry the %d bytes the file holds:\n%s", info.Size(), note)
	}
	if strings.Contains(note, "downscaled from") {
		want := fmt.Sprintf("(downscaled from %dx%d to fit the image budget)", width, height)
		if !strings.Contains(note, want) {
			t.Fatalf("the note's downscale clause does not name the file's %dx%d:\n%s", width, height, note)
		}
		return
	}
	if want := fmt.Sprintf("%s capture %dx%d,", mode, width, height); !strings.Contains(note, want) {
		t.Fatalf("the note does not describe a %dx%d capture:\n%s", width, height, note)
	}
}

// 1. mode screen: the capture exists, decodes, and comes back as pixels plus a
// note that describes it. On macOS this also pins the owner's 2026-11-23 ruling
// end to end — the seatbelt profile refuses the display service, the escalation
// is asked about, and the approved replay captures on the host.
func TestCaptureScreenReturnsAPngAndItsPixels(t *testing.T) {
	if _, err := exec.LookPath("/usr/sbin/screencapture"); err != nil && runtime.GOOS == "darwin" {
		t.Skip("no screencapture on this machine, so a screen capture cannot be produced")
	}
	if runtime.GOOS != "darwin" {
		if _, err := captureCommandFor("screen", runtime.GOOS, "/tmp/x.png", &CaptureInput{}).argv, error(nil); err != nil {
			t.Skip("no capture binary for this platform")
		}
		if shape := captureCommandFor("screen", runtime.GOOS, "/tmp/x.png", &CaptureInput{}); shape.binary != "" {
			if _, err := exec.LookPath(shape.binary); err != nil {
				t.Skipf("no %s on this machine, so a screen capture cannot be produced", shape.binary)
			}
		}
	}
	r, st, tl := newCaptureRunnerForTest(t, nil)
	authorizeWithoutAsking(st)
	parts, err := captureResult(t, tl, tool.WithProjectRoot(context.Background(), r.workspaceRoot()), CaptureInput{Mode: "screen"})
	if err != nil {
		t.Fatalf("screen capture: %v", err)
	}
	note, part := captureShares(t, parts, filepath.Join(r.captureDir(), ""))
	if part.Type != llm.ContentTypeImageBase64 || part.ImageBase64 == "" {
		t.Fatalf("the capture came back as %+v, want base64 png pixels", part)
	}
	path := strings.SplitN(note, "\n", 2)[0]
	width, height := pngDimensions(t, path)
	if width <= 0 || height <= 0 {
		t.Fatalf("capture decoded to %dx%d", width, height)
	}
	assertCaptureNote(t, note, "screen", path, width, height)
}

// 2. mode region: the rectangle is honoured. A Retina display doubles it, so
// the assertion is the integer scale rather than the exact pixel count.
func TestCaptureRegionHonoursTheRect(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the region assertion is written against screencapture's -R")
	}
	if _, err := exec.LookPath("/usr/sbin/screencapture"); err != nil {
		t.Skip("no screencapture on this machine")
	}
	r, st, tl := newCaptureRunnerForTest(t, nil)
	authorizeWithoutAsking(st)
	parts, err := captureResult(t, tl, tool.WithProjectRoot(context.Background(), r.workspaceRoot()), CaptureInput{
		Mode: "region", Rect: "10,20,80,60",
	})
	if err != nil {
		t.Fatalf("region capture: %v", err)
	}
	note, _ := captureShares(t, parts, "")
	path := strings.SplitN(note, "\n", 2)[0]
	width, height := pngDimensions(t, path)
	if width%80 != 0 || height%60 != 0 || width/80 != height/60 {
		t.Fatalf("region 80x60 captured as %dx%d, which is not an integer scale of the rect", width, height)
	}
}

// 3. mode browser with nothing installed: the explicit error names the browsers
// and the fallback, and nothing panics.
func TestCaptureBrowserWithoutABrowserExplainsItself(t *testing.T) {
	previous := captureLookPath
	captureLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { captureLookPath = previous })

	r, st, tl := newCaptureRunnerForTest(t, nil)
	authorizeWithoutAsking(st)
	_, err := captureResult(t, tl, tool.WithProjectRoot(context.Background(), r.workspaceRoot()), CaptureInput{
		Mode: "browser", URL: "http://127.0.0.1:1/",
	})
	if err == nil {
		t.Fatal("a browser capture with no browser must fail")
	}
	for _, want := range []string{"no headless browser", "chrome", "process tool"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// 4. A refusal is a typed outcome with a sentence that says what to do: the
// worst failure this tool can have is a blank frame reported as success.
func TestCaptureClassifiesRefusalsAndNamesThePermissionPath(t *testing.T) {
	sandboxed := safety.SandboxDecision{UseSandbox: true}
	host := safety.SandboxDecision{UseSandbox: false}
	cases := []struct {
		name     string
		res      safety.CommandResult
		decision safety.SandboxDecision
		want     captureOutcome
	}{
		{"seatbelt refuses the display", safety.CommandResult{ExitCode: 1, Stderr: "could not create image from display 0\n"}, sandboxed, captureSandboxDenied},
		{"tcc refuses the display", safety.CommandResult{ExitCode: 1, Stderr: "could not create image from display 0\n"}, host, captureDenied},
		{"linux display", safety.CommandResult{ExitCode: 1, Stderr: "Unable to open display\n"}, host, captureDenied},
		{"windows token", safety.CommandResult{ExitCode: 1, Stderr: "Access is denied.\n"}, host, captureDenied},
		{"success", safety.CommandResult{ExitCode: 0}, sandboxed, captureCaptured},
	}
	for _, tc := range cases {
		if got := classifyCapture(tc.res, tc.decision, nil, "screen"); got != tc.want {
			t.Fatalf("%s: classifyCapture = %q, want %q", tc.name, got, tc.want)
		}
	}
	if runtime.GOOS == "darwin" {
		if err := capturePermissionError("screen"); !strings.Contains(err.Error(), "Screen Recording") || !strings.Contains(err.Error(), "System Settings") {
			t.Fatalf("the refusal does not name the permission path: %v", err)
		}
	}
}

// 5. A uniform frame is a warning, not a failure: a genuinely blank page is a
// legal thing to capture.
func TestCaptureWarnsOnAUniformFrameButStillReturns(t *testing.T) {
	r, st, _ := newCaptureRunnerForTest(t, &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess})
	authorizeWithoutAsking(st)
	path := filepath.Join(r.captureDir(), "uniform.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	writeUniformPNG(t, path, 120, 80, color.RGBA{R: 30, G: 30, B: 30, A: 255})
	if !uniformCapture(mustRead(t, path)) {
		t.Fatal("a single-colour frame was not detected")
	}
	parts, err := r.captureParts(context.Background(), st, path, "screen", captureCaptured)
	if err != nil {
		t.Fatalf("a uniform frame must still return: %v", err)
	}
	note, _ := captureShares(t, parts, path)
	if !strings.Contains(note, "single uniform colour") {
		t.Fatalf("the uniform-frame warning is missing:\n%s", note)
	}
	// Two colours are not uniform, so an ordinary screenshot does not warn.
	writeTwoTonePNG(t, filepath.Join(r.captureDir(), "twotone.png"))
	if uniformCapture(mustRead(t, filepath.Join(r.captureDir(), "twotone.png"))) {
		t.Fatal("a two-colour frame was reported as uniform")
	}
}

// 6. The bytes land under the state root's capture spool, and that directory
// (or the file's own directory) travels with the command as a writable root.
func TestCaptureWritesUnderTheStateRootAndPassesItAsWritable(t *testing.T) {
	r, _, _ := newCaptureRunnerForTest(t, nil)
	dir := r.captureDir()
	if want := filepath.Join(r.workspaceRoot(), "state", "capture"); dir != want {
		t.Fatalf("capture dir = %q, want %q", dir, want)
	}
	cfg := &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite}
	request := r.captureRequest(context.Background(), cfg, captureCommandFor("screen", runtime.GOOS, filepath.Join(dir, "a.png"), &CaptureInput{}), r.workspaceRoot(), dir, "screen", safety.SandboxPermissionsUseDefault)
	found := false
	for _, path := range request.AdditionalWritablePaths {
		if path == dir {
			found = true
		}
	}
	if !found {
		t.Fatalf("the capture dir is not a writable root of the command: %+v", request.AdditionalWritablePaths)
	}
	if request.ToolKind != safety.ToolKindShell {
		t.Fatalf("a capture must run through the shell's own decision path, got %q", request.ToolKind)
	}
}

// 7. An oversized capture is downscaled by the budget read_file already uses,
// not refused.
func TestCaptureOversizedImageIsDownscaled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.png")
	// The budget is the product's own knob, resolved at call time; setting it
	// low is what makes a modest capture oversized, without inventing an image
	// no real screen produces.
	t.Setenv(llm.ImageMaxBytesEnv, "40000")
	writePatternPNG(t, path, 2400, 1600)
	data := mustRead(t, path)
	part, res, err := llm.ImagePartWithin(path, "image/png", data, 0)
	if err != nil {
		t.Fatalf("an oversized capture must downscale, not fail: %v", err)
	}
	if !res.Scaled {
		t.Fatalf("an oversized capture was not scaled: %+v", res)
	}
	if part.Type != llm.ContentTypeImageBase64 || res.SentBytes >= res.SourceBytes {
		t.Fatalf("downscale produced %+v", res)
	}
}

// 8. pkg/tool gained no file for this tool: it is registered from pkg/run.
func TestCaptureToolAddsNoToolPackageFile(t *testing.T) {
	entries, err := os.ReadDir("../tool")
	if err != nil {
		t.Skip("cannot read the tool package directory from here")
	}
	count := 0
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			count++
		}
	}
	if count != 34 {
		t.Fatalf("pkg/tool has %d production files, want 34 (the cap, with the process tool taking the last slot)", count)
	}
}

// 9. The downscale clause in the note, pinned without the screen's cooperation:
// the budget is the product's own knob, so setting it low is what makes a
// modest frame oversized. This is the clause the screen case used to depend on
// by accident — its verdict flipped with the size of the display.
func TestCaptureNoteNamesTheDownscaleUnderATightBudget(t *testing.T) {
	t.Setenv(llm.ImageMaxBytesEnv, "40000")
	r, st, _ := newCaptureRunnerForTest(t, &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess})
	authorizeWithoutAsking(st)
	path := filepath.Join(r.captureDir(), "oversized.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	writePatternPNG(t, path, 2400, 1600)
	parts, err := r.captureParts(context.Background(), st, path, "screen", captureCaptured)
	if err != nil {
		t.Fatalf("an oversized capture must downscale, not fail: %v", err)
	}
	note, part := captureShares(t, parts, path)
	if part.Type != llm.ContentTypeImageBase64 || part.ImageBase64 == "" {
		t.Fatalf("the downscaled capture came back as %+v, want base64 png pixels", part)
	}
	if !strings.Contains(note, "downscaled from 2400x1600") {
		t.Fatalf("the note does not say the frame was downscaled:\n%s", note)
	}
	assertCaptureNote(t, note, "screen", path, 2400, 1600)
}

func writeUniformPNG(t *testing.T, path string, width, height int, fill color.RGBA) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, fill)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeTwoTonePNG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			if x < 4 {
				img.Set(x, y, color.RGBA{R: 255, A: 255})
			} else {
				img.Set(x, y, color.RGBA{B: 255, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writePatternPNG writes a large frame whose pixels differ, so it is a real
// image rather than a uniform one the downscaler could special-case.
func writePatternPNG(t *testing.T, path string, width, height int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 251), G: uint8(y % 241), B: uint8((x * y) % 239), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
