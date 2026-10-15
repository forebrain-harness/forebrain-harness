package pdf

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rc4"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pdfium "github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
)

// zhLine is the text on both committed fixtures' single page. The ten
// ideographs after the fullwidth colon are what the non-tofu test renders,
// slices by PDFium's own rectangles, and measures.
const zhLine = "中文渲染测试：甲乙丙丁戊己庚辛壬癸"

// minDistinctGlyphSignatures is how many of the ten glyphs must have a
// different ink profile. A fallback font that is missing draws every glyph as
// the same .notdef box, which collapses the count to 1; real glyphs differ.
const minDistinctGlyphSignatures = 6

func TestMain(m *testing.M) {
	// Pin a scratch home (overriding any inherited FOREBRAIN_HOME, so a test
	// run never reads a real install) and point the font loader at this
	// repository's fonts when they are fetched, so every test shares one
	// PDFium initialisation with usable CJK fallback fonts.
	home, err := os.MkdirTemp("", "pdf-test-home")
	if err != nil {
		fmt.Fprintln(os.Stderr, "pdf test: create home:", err)
		os.Exit(1)
	}
	_ = os.Setenv("FOREBRAIN_HOME", home)
	if dir, ok := repoFontsDir(); ok {
		_ = os.Setenv(FontDirsEnv, dir)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// repoFontsDir returns assets/fonts when this repository's CJK fonts have been
// fetched, walking up to the module root.
func repoFontsDir() (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			fonts := filepath.Join(dir, "assets", "fonts")
			if _, err := os.Stat(filepath.Join(fonts, "NotoSansSC-Regular.otf")); err == nil {
				return fonts, true
			}
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func testdata(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("testdata", name)
}

// TestChineseTextIsExtracted is the guard that the text layer of the fixture is
// intact. It is not the rendering check — extraction and rendering are
// independent paths, and text can extract cleanly while the page renders as
// tofu — but a broken text layer would make the glyph-position test below
// meaningless, so it runs first.
func TestChineseTextIsExtracted(t *testing.T) {
	for _, name := range []string{"zh-notembedded.pdf", "zh-embedded.pdf"} {
		t.Run(name, func(t *testing.T) {
			instance := openInstance(t)
			defer instance.Close()
			doc := openDoc(t, instance, testdata(t, name))
			defer closeDoc(t, instance, doc)

			text, err := instance.GetPageText(&requests.GetPageText{
				Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc, Index: 0}},
			})
			if err != nil {
				t.Fatalf("GetPageText: %v", err)
			}
			if !strings.Contains(text.Text, "中文渲染测试") {
				t.Fatalf("extracted text %q does not contain 中文渲染测试", text.Text)
			}
		})
	}
}

// TestChineseGlyphsAreNotTofu is the decisive check. It renders the
// non-embedded fixture (the one that needs a fallback font), takes PDFium's own
// rectangle for each of the ten ideographs, and measures the ink in each
// character cell as a column profile. A missing fallback font draws every
// glyph as the same tofu box, which makes the profiles identical; real glyphs
// differ, so at least minDistinctGlyphSignatures of the ten must be distinct.
func TestChineseGlyphsAreNotTofu(t *testing.T) {
	instance := openInstance(t)
	defer instance.Close()
	doc := openDoc(t, instance, testdata(t, "zh-notembedded.pdf"))
	defer closeDoc(t, instance, doc)

	rendered, err := instance.RenderPageInDPI(&requests.RenderPageInDPI{
		DPI:  DefaultDPI,
		Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc, Index: 0}},
	})
	if err != nil {
		t.Fatalf("RenderPageInDPI: %v", err)
	}
	img := rendered.Result.RenderedImage
	if img == nil {
		img = rendered.Result.Image
	}
	if img == nil {
		t.Fatal("page produced no image")
	}

	structured, err := instance.GetPageTextStructured(&requests.GetPageTextStructured{
		Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc, Index: 0}},
		Mode: requests.GetPageTextStructuredModeChars,
		PixelPositions: requests.GetPageTextStructuredPixelPositions{
			Document: doc, Calculate: true, DPI: DefaultDPI,
		},
	})
	if err != nil {
		t.Fatalf("GetPageTextStructured: %v", err)
	}

	targets := map[rune]bool{}
	for _, r := range "甲乙丙丁戊己庚辛壬癸" {
		targets[r] = true
	}
	signatures := map[string]bool{}
	found := 0
	for _, ch := range structured.Chars {
		runes := []rune(ch.Text)
		if len(runes) != 1 || !targets[runes[0]] || ch.PixelPosition == nil {
			continue
		}
		found++
		signatures[fmt.Sprint(columnProfile(img, *ch.PixelPosition))] = true
	}
	if found != 10 {
		t.Fatalf("found %d of the 10 target glyphs in structured text; the fixture or the positions changed", found)
	}
	t.Logf("distinct ink profiles: %d of %d glyphs", len(signatures), found)
	if len(signatures) < minDistinctGlyphSignatures {
		t.Fatalf("only %d distinct glyph profiles out of %d; the fallback font is drawing tofu boxes",
			len(signatures), found)
	}
}

// TestColumnProfileFiresOnTofu pins the detector TestChineseGlyphsAreNotTofu
// depends on. That test's red case is a pipeline that renders CJK text as tofu
// boxes, which only happens on a machine with no CJK fallback font; this host
// has system CJK fonts, so the tofu path cannot be produced here and the
// pipeline test's failure mode would otherwise never be shown to fire. The
// detector is pinned on this host instead, over synthetic images: ten identical
// boxes — what tofu looks like — must collapse to a single signature, and ten
// visibly different ink patterns must yield ten. The signature compared is the
// same fmt.Sprint(columnProfile(...)) expression TestChineseGlyphsAreNotTofu
// groups by, so a change that broke the detector would fail here.
func TestColumnProfileFiresOnTofu(t *testing.T) {
	const (
		imgW, imgH = 1000, 100
		boxW, boxH = 60, 60
		boxY       = 20
		step       = 90
	)

	t.Run("ten identical boxes collapse to one signature", func(t *testing.T) {
		img := newCanvas(imgW, imgH)
		signatures := map[string]bool{}
		for i := 0; i < 10; i++ {
			x0 := 10 + i*step
			strokeRect(img, x0, boxY, x0+boxW, boxY+boxH)
			signatures[fmt.Sprint(columnProfile(img, charPos(imgW, imgH, x0, boxY, x0+boxW, boxY+boxH)))] = true
		}
		if len(signatures) != 1 {
			t.Fatalf("ten identical boxes gave %d distinct signatures, want 1; the detector does not fire on tofu",
				len(signatures))
		}
	})

	t.Run("ten different ink patterns give ten signatures", func(t *testing.T) {
		img := newCanvas(imgW, imgH)
		signatures := map[string]bool{}
		for i := 0; i < 10; i++ {
			x0 := 10 + i*step
			drawPattern(img, i, x0, boxY, boxW, boxH)
			signatures[fmt.Sprint(columnProfile(img, charPos(imgW, imgH, x0, boxY, x0+boxW, boxY+boxH)))] = true
		}
		if len(signatures) != 10 {
			t.Fatalf("ten different ink patterns gave %d distinct signatures, want 10", len(signatures))
		}
	})
}

// newCanvas returns an opaque white image, like a rendered page. A transparent
// canvas would read as ink everywhere, because inkedPixel judges by colour and
// a transparent pixel is (0,0,0,0).
func newCanvas(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	return img
}

// drawPattern draws one of ten visibly different ink patterns into the box whose
// top-left corner is (x0,y0). The patterns stand in for distinct glyph shapes.
func drawPattern(img *image.RGBA, pattern, x0, y0, w, h int) {
	x1, y1 := x0+w, y0+h
	switch pattern {
	case 0: // filled box
		fillRect(img, x0, y0, x1, y1)
	case 1: // hollow box: ink only on the border
		strokeRect(img, x0, y0, x1, y1)
	case 2: // left half filled
		fillRect(img, x0, y0, x0+w/2, y1)
	case 3: // right half filled
		fillRect(img, x0+w/2, y0, x1, y1)
	case 4: // left vertical bar
		fillRect(img, x0, y0, x0+w/6, y1)
	case 5: // centre vertical bar
		fillRect(img, x0+2*w/5, y0, x0+3*w/5, y1)
	case 6: // right vertical bar
		fillRect(img, x1-w/6, y0, x1, y1)
	case 7: // top half filled
		fillRect(img, x0, y0, x1, y0+h/2)
	case 8: // two vertical bars
		fillRect(img, x0, y0, x0+w/6, y1)
		fillRect(img, x1-w/6, y0, x1, y1)
	case 9: // diagonal staircase
		for x := 0; x < w; x++ {
			fillRect(img, x0+x, y1-(x+1), x0+x+1, y1)
		}
	}
}

// charPos builds the PDFium-style position for the box (x0,y0)-(x1,y1) in an
// image of the given height. PDFium's Y grows upward from the page's bottom, so
// the top-origin image rows are converted back the way pixelRect reverses it.
func charPos(imgW, imgH, x0, y0, x1, y1 int) responses.CharPosition {
	return responses.CharPosition{
		Left:   float64(x0),
		Right:  float64(x1),
		Top:    float64(imgH - y0),
		Bottom: float64(imgH - y1),
	}
}

func fillRect(img *image.RGBA, x0, y0, x1, y1 int) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.SetRGBA(x, y, color.RGBA{A: 255})
		}
	}
}

func strokeRect(img *image.RGBA, x0, y0, x1, y1 int) {
	for x := x0; x < x1; x++ {
		img.SetRGBA(x, y0, color.RGBA{A: 255})
		img.SetRGBA(x, y1-1, color.RGBA{A: 255})
	}
	for y := y0; y < y1; y++ {
		img.SetRGBA(x0, y, color.RGBA{A: 255})
		img.SetRGBA(x1-1, y, color.RGBA{A: 255})
	}
}

// TestFontDirsIncludePlatformDefaults guards the replacement semantics of
// PDFium's UserFontPaths: when we hand it our own directory, on Linux it stops
// scanning the platform directories, so FontDirs must add them back or a
// document that needs a system font breaks while Chinese starts working.
func TestFontDirsIncludePlatformDefaults(t *testing.T) {
	own := t.TempDir()
	getenv := func(key string) string {
		if key == FontDirsEnv {
			return own
		}
		return ""
	}

	t.Run("adds existing platform directories", func(t *testing.T) {
		data := t.TempDir()
		platformFonts := filepath.Join(data, "fonts")
		if err := os.MkdirAll(platformFonts, 0o755); err != nil {
			t.Fatal(err)
		}
		env := func(key string) string {
			switch key {
			case FontDirsEnv:
				return own
			case "XDG_DATA_HOME":
				return data
			}
			return ""
		}
		got := fontDirs("linux", "/nonexistent-home", env, "")
		if !contains(got, own) {
			t.Fatalf("FontDirs %v is missing our own directory %q", got, own)
		}
		if !contains(got, platformFonts) {
			t.Fatalf("FontDirs %v is missing the platform directory %q", got, platformFonts)
		}
	})

	t.Run("our directory only when no platform directory exists", func(t *testing.T) {
		got := fontDirs("linux", "/nonexistent-home", getenv, "")
		if !contains(got, own) {
			t.Fatalf("FontDirs %v is missing our own directory %q", got, own)
		}
		if len(got) != 1 {
			t.Fatalf("FontDirs %v should hold only our directory", got)
		}
	})

	t.Run("empty when nothing applies", func(t *testing.T) {
		if got := fontDirs("linux", "/nonexistent-home", func(string) string { return "" }, ""); got != nil {
			t.Fatalf("FontDirs %v should be nil when no font directory exists", got)
		}
	})
}

// TestRenderCapsPages renders a ten-page document and checks only the cap is
// rendered while the full page count is reported.
func TestRenderCapsPages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ten.pdf")
	if err := os.WriteFile(path, simplePDF(10), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := RenderFile(path, 0, 0, Options{})
	if err != nil {
		t.Fatalf("RenderFile: %v", err)
	}
	if result.PageCount != 10 {
		t.Fatalf("PageCount = %d, want 10", result.PageCount)
	}
	if len(result.Pages) != DefaultMaxPages {
		t.Fatalf("rendered %d pages, want %d", len(result.Pages), DefaultMaxPages)
	}
	if result.RenderedMax != DefaultMaxPages {
		t.Fatalf("RenderedMax = %d, want %d", result.RenderedMax, DefaultMaxPages)
	}
	for i, page := range result.Pages {
		if page.Number != i+1 || len(page.PNG) == 0 {
			t.Fatalf("page %d = %+v", i, page)
		}
	}
}

// TestEncryptedPDFIsActionable checks a password-protected document fails with
// a message the model can act on, not a bare PDFium error code.
func TestEncryptedPDFIsActionable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locked.pdf")
	if err := os.WriteFile(path, encryptedPDF("hunter2"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := RenderFile(path, 0, 0, Options{})
	if err == nil {
		t.Fatal("RenderFile on an encrypted PDF returned no error")
	}
	if !strings.Contains(err.Error(), "password-protected") {
		t.Fatalf("error = %q, want it to mention password-protected", err)
	}
}

// TestEmptyPDFIsActionable checks a zero-page document fails with a message
// the model can act on rather than an empty result.
func TestEmptyPDFIsActionable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.pdf")
	if err := os.WriteFile(path, simplePDF(0), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := RenderFile(path, 0, 0, Options{})
	if err == nil {
		t.Fatal("RenderFile on a zero-page PDF returned no error")
	}
	if !strings.Contains(err.Error(), "no pages") {
		t.Fatalf("error = %q, want it to mention no pages", err)
	}
}

// TestRenderFileWithoutFontDirsDoesNotPanic exercises the empty-options path.
func TestRenderFileWithoutFontDirsDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RenderFile panicked: %v", r)
		}
	}()
	if _, err := RenderFile(testdata(t, "zh-notembedded.pdf"), 0, 0, Options{}); err != nil {
		t.Logf("RenderFile returned an error (acceptable): %v", err)
	}
}

// TestRenderRange pins that a start page selects where rendering begins while
// the cap stays a count: start=7 on a ten-page document renders pages 7-10
// (four pages, the document's tail) under the default cap.
func TestRenderRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ten.pdf")
	if err := os.WriteFile(path, simplePDF(10), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := RenderFile(path, 7, 0, Options{})
	if err != nil {
		t.Fatalf("RenderFile: %v", err)
	}
	if result.PageCount != 10 {
		t.Fatalf("PageCount = %d, want 10", result.PageCount)
	}
	if len(result.Pages) != 4 {
		t.Fatalf("rendered %d pages, want 4", len(result.Pages))
	}
	if result.RenderedMax != DefaultMaxPages {
		t.Fatalf("RenderedMax = %d, want %d", result.RenderedMax, DefaultMaxPages)
	}
	for i, page := range result.Pages {
		if want := 7 + i; page.Number != want {
			t.Fatalf("page %d number = %d, want %d", i, page.Number, want)
		}
	}
}

// TestRenderClampsEndIntoDocument pins that a range whose end runs past the
// last page is clamped, not an error: start=8, maxPages=10 on a ten-page
// document renders exactly pages 8-10.
func TestRenderClampsEndIntoDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ten.pdf")
	if err := os.WriteFile(path, simplePDF(10), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := RenderFile(path, 8, 10, Options{})
	if err != nil {
		t.Fatalf("RenderFile: %v", err)
	}
	if len(result.Pages) != 3 {
		t.Fatalf("rendered %d pages, want 3", len(result.Pages))
	}
	if first, last := result.Pages[0].Number, result.Pages[len(result.Pages)-1].Number; first != 8 || last != 10 {
		t.Fatalf("rendered pages %d-%d, want 8-10", first, last)
	}
}

// TestRenderStartBeyondEndIsEmpty pins that a start past the last page renders
// nothing — an empty read that still reports the document's true page count,
// the way a text read past EOF returns no lines.
func TestRenderStartBeyondEndIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ten.pdf")
	if err := os.WriteFile(path, simplePDF(10), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := RenderFile(path, 99, 0, Options{})
	if err != nil {
		t.Fatalf("RenderFile: %v", err)
	}
	if result.PageCount != 10 {
		t.Fatalf("PageCount = %d, want 10", result.PageCount)
	}
	if len(result.Pages) != 0 {
		t.Fatalf("rendered %d pages, want 0", len(result.Pages))
	}
}

// TestRenderNormalizesStart pins that start <= 0 means page 1: a caller
// passing 0 gets exactly what a caller passing 1 gets.
func TestRenderNormalizesStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ten.pdf")
	if err := os.WriteFile(path, simplePDF(10), 0o644); err != nil {
		t.Fatal(err)
	}
	fromZero, err := RenderFile(path, 0, 0, Options{})
	if err != nil {
		t.Fatalf("RenderFile(start=0): %v", err)
	}
	fromOne, err := RenderFile(path, 1, 0, Options{})
	if err != nil {
		t.Fatalf("RenderFile(start=1): %v", err)
	}
	if len(fromZero.Pages) != len(fromOne.Pages) || len(fromOne.Pages) == 0 {
		t.Fatalf("start=0 rendered %d pages, start=1 rendered %d", len(fromZero.Pages), len(fromOne.Pages))
	}
	for i := range fromZero.Pages {
		if fromZero.Pages[i].Number != fromOne.Pages[i].Number {
			t.Fatalf("page %d: start=0 gave page %d, start=1 gave page %d",
				i, fromZero.Pages[i].Number, fromOne.Pages[i].Number)
		}
	}
}

// --- helpers -------------------------------------------------------------

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func openInstance(t *testing.T) pdfium.Pdfium {
	t.Helper()
	instance, err := renderPool(FontDirs(os.Getenv("FOREBRAIN_HOME"))).GetInstanceWithContext(context.Background())
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	return instance
}

func openDoc(t *testing.T, instance pdfium.Pdfium, path string) references.FPDF_DOCUMENT {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	doc, err := instance.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		t.Fatalf("OpenDocument %s: %v", path, err)
	}
	return doc.Document
}

func closeDoc(t *testing.T, instance pdfium.Pdfium, doc references.FPDF_DOCUMENT) {
	t.Helper()
	if _, err := instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc}); err != nil {
		t.Fatalf("FPDF_CloseDocument: %v", err)
	}
}

// pixelRect turns a structured char's position into image pixel bounds. PDFium
// reports the Y axis growing downward from the page's bottom edge, so it is
// flipped here into the top-origin space the rendered image uses.
func pixelRect(img image.Image, pos responses.CharPosition) (int, int, int, int) {
	bounds := img.Bounds()
	h := bounds.Dy()
	l := int(min(pos.Left, pos.Right) + 0.5)
	r := int(max(pos.Left, pos.Right) + 0.5)
	t := int(float64(h) - max(pos.Top, pos.Bottom) + 0.5)
	b := int(float64(h) - min(pos.Top, pos.Bottom) + 0.5)
	if l < bounds.Min.X {
		l = bounds.Min.X
	}
	if t < bounds.Min.Y {
		t = bounds.Min.Y
	}
	if r > bounds.Max.X {
		r = bounds.Max.X
	}
	if b > bounds.Max.Y {
		b = bounds.Max.Y
	}
	return l, t, r, b
}

// columnProfile is the ink-column projection of one glyph cell, binned into a
// fixed number of columns so glyphs of different widths are comparable. A tofu
// box yields the same profile for every glyph; distinct glyphs yield distinct
// profiles.
func columnProfile(img image.Image, pos responses.CharPosition) []int {
	const groups = 24
	l, top, r, b := pixelRect(img, pos)
	out := make([]int, groups)
	for k := 0; k < groups; k++ {
		xa := l + (r-l)*k/groups
		xb := l + (r-l)*(k+1)/groups
		if xb <= xa {
			xb = xa + 1
		}
		for x := xa; x < xb; x++ {
			for y := top; y < b; y++ {
				if inkedPixel(img, x, y) {
					out[k]++
				}
			}
		}
	}
	return out
}

func inkedPixel(img image.Image, x, y int) bool {
	r, g, b, _ := img.At(x, y).RGBA()
	return r < 0xC000 || g < 0xC000 || b < 0xC000
}

// simplePDF builds an unencrypted PDF with the given number of pages, each
// carrying a short Helvetica caption.
func simplePDF(pages int) []byte {
	var objects []string
	add := func(body string) int {
		objects = append(objects, body)
		return len(objects)
	}
	catalog := add("")
	pagesNum := add("")
	font := add("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	var kids []string
	for i := 1; i <= pages; i++ {
		content := fmt.Sprintf("BT /F1 24 Tf 72 700 Td (Page %d) Tj ET", i)
		contentNum := add(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
		pageNum := add(fmt.Sprintf(
			"<< /Type /Page /Parent %d 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
			pagesNum, font, contentNum))
		kids = append(kids, fmt.Sprintf("%d 0 R", pageNum))
	}
	objects[catalog-1] = fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesNum)
	objects[pagesNum-1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), pages)
	return assemblePDF(objects, fmt.Sprintf("%d 0 R", catalog), "")
}

// assemblePDF serialises objects (1-based) into a PDF, with extra appended to
// the trailer dictionary.
func assemblePDF(objects []string, root, extra string) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objects)+1)
	for i, body := range objects {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root %s%s >>\nstartxref\n%d\n%%%%EOF\n",
		len(objects)+1, root, extra, xref)
	return buf.Bytes()
}

// pdfPasswordPad is the fixed 32-byte string the standard security handler
// appends to a password (PDF 1.7, algorithm 2).
var pdfPasswordPad = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

// encryptedPDF builds a one-page PDF protected by the standard security
// handler, revision 2 (40-bit RC4), with the given user password, so opening it
// without a password fails.
func encryptedPDF(userPassword string) []byte {
	const ownerPassword = "forebrain-owner"
	permissions := int32(-3904)
	id0 := []byte("0123456789abcdef")

	paddedUser := padPassword(userPassword)
	paddedOwner := padPassword(ownerPassword)

	ownerKey := md5Sum(paddedOwner)[:5]
	o := rc4Crypt(ownerKey, paddedUser)

	h := md5.New()
	h.Write(paddedUser)
	h.Write(o)
	var pbuf [4]byte
	binary.LittleEndian.PutUint32(pbuf[:], uint32(permissions))
	h.Write(pbuf[:])
	h.Write(id0)
	fileKey := h.Sum(nil)[:5]

	u := rc4Crypt(fileKey, pdfPasswordPad)

	var objects []string
	add := func(body string) int {
		objects = append(objects, body)
		return len(objects)
	}
	catalog := add("")
	pagesNum := add("")
	font := add("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	content := "BT /F1 24 Tf 72 700 Td (Secret) Tj ET"
	// The content stream is encrypted with this object's key.
	contentNum := len(objects) + 1
	encrypted := rc4Crypt(objectKey(fileKey, contentNum, 0), []byte(content))
	add(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(encrypted), encrypted))
	pageNum := add(fmt.Sprintf(
		"<< /Type /Page /Parent %d 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
		pagesNum, font, contentNum))
	objects[catalog-1] = fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesNum)
	objects[pagesNum-1] = fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", pageNum)

	encrypt := add(fmt.Sprintf(
		"<< /Filter /Standard /V 1 /R 2 /Length 40 /O <%s> /U <%s> /P %d >>",
		hex.EncodeToString(o), hex.EncodeToString(u), permissions))

	extra := fmt.Sprintf(" /Encrypt %d 0 R /ID [<%s> <%s>]",
		encrypt, hex.EncodeToString(id0), hex.EncodeToString(id0))
	return assemblePDF(objects, fmt.Sprintf("%d 0 R", catalog), extra)
}

func padPassword(password string) []byte {
	out := make([]byte, 32)
	n := copy(out, password)
	copy(out[n:], pdfPasswordPad)
	return out
}

func objectKey(fileKey []byte, num, gen int) []byte {
	h := md5.New()
	h.Write(fileKey)
	h.Write([]byte{byte(num), byte(num >> 8), byte(num >> 16), byte(gen), byte(gen >> 8)})
	limit := len(fileKey) + 5
	if limit > 16 {
		limit = 16
	}
	return h.Sum(nil)[:limit]
}

func md5Sum(data []byte) []byte {
	sum := md5.Sum(data)
	return sum[:]
}

func rc4Crypt(key, data []byte) []byte {
	cipher, err := rc4.NewCipher(key)
	if err != nil {
		panic(err)
	}
	out := make([]byte, len(data))
	cipher.XORKeyStream(out, data)
	return out
}
