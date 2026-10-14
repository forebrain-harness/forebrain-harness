package llm

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"strings"
	"testing"
)

// pngBytes encodes a small deterministic image and returns its PNG bytes.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x40, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// bmpBytes is a minimal BMP file header that net/http's sniffer recognizes as
// image/bmp: "BM" magic, then a header-size field of 40 at offset 14.
func bmpBytes() []byte {
	b := make([]byte, 15)
	b[0], b[1] = 'B', 'M'
	b[14] = 40
	return b
}

// noisePNGBytes encodes a w×h PNG of pseudo-random noise, which barely
// compresses — the case that forces ImagePartWithin to downscale.
func noisePNGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	rng := rand.New(rand.NewSource(1))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	buf := make([]byte, 4*w*h)
	if _, err := rng.Read(buf); err != nil {
		t.Fatalf("noise: %v", err)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := 4 * (y*w + x)
			img.Set(x, y, color.RGBA{R: buf[i], G: buf[i+1], B: buf[i+2], A: 0xff})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return out.Bytes()
}

func TestImageMIMETypeSniffsAndFallsBack(t *testing.T) {
	t.Parallel()
	png := pngBytes(t, 2, 2)
	text := []byte("this is not an image at all, just words")

	for _, tc := range []struct {
		name string
		path string
		data []byte
		want string
	}{
		{name: "png content, png ext", path: "shot.png", data: png, want: "image/png"},
		{name: "png content, bin ext", path: "shot.bin", data: png, want: "image/png"},
		{name: "text content, png ext", path: "fake.png", data: text, want: "image/png"},
		{name: "text content, txt ext", path: "note.txt", data: text, want: ""},
		// The sniff is authoritative for images we cannot send: BMP bytes in a
		// .png-named file must report "" (not image/png), so read_file gives its
		// "convert it" error instead of sending BMP bytes as PNG.
		{name: "bmp content, png ext", path: "shot.png", data: bmpBytes(), want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ImageMIMEType(tc.path, tc.data); got != tc.want {
				t.Fatalf("ImageMIMEType(%q)=%q want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestImagePartWithinKeepsSmallImagesVerbatim(t *testing.T) {
	t.Parallel()
	data := pngBytes(t, 16, 16)
	part, res, err := ImagePartWithin("shot.png", "image/png", data, 1<<20)
	if err != nil {
		t.Fatalf("ImagePartWithin: %v", err)
	}
	if res.Scale != 1 || res.Scaled {
		t.Fatalf("small image should be verbatim: scaled=%v scale=%d", res.Scaled, res.Scale)
	}
	if res.SentBytes != len(data) {
		t.Fatalf("SentBytes=%d want %d", res.SentBytes, len(data))
	}
	decoded, err := base64.StdEncoding.DecodeString(part.ImageBase64)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("verbatim image bytes changed")
	}
	if res.Width != 16 || res.Height != 16 {
		t.Fatalf("reported %dx%d want 16x16", res.Width, res.Height)
	}
}

func TestImagePartWithinScalesDownOversizeImages(t *testing.T) {
	t.Parallel()
	data := noisePNGBytes(t, 2000, 2000)
	if len(data) <= (1 << 20) {
		t.Fatalf("test image too small to exercise downscaling: %d bytes", len(data))
	}
	budget := 1 << 20
	part, res, err := ImagePartWithin("noise.png", "image/png", data, budget)
	if err != nil {
		t.Fatalf("ImagePartWithin: %v", err)
	}
	if !res.Scaled || res.Scale < 2 {
		t.Fatalf("expected a downscale: scaled=%v scale=%d", res.Scaled, res.Scale)
	}
	if res.SentBytes <= 0 || res.SentBytes > budget {
		t.Fatalf("SentBytes=%d want (0, %d]", res.SentBytes, budget)
	}
	decoded, err := base64.StdEncoding.DecodeString(part.ImageBase64)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(decoded))
	if err != nil {
		t.Fatalf("reported image does not decode: %v", err)
	}
	if cfg.Width != res.Width || cfg.Height != res.Height {
		t.Fatalf("reported %dx%d but decoded %dx%d", res.Width, res.Height, cfg.Width, cfg.Height)
	}
}

func TestImagePartWithinIsDeterministic(t *testing.T) {
	t.Parallel()
	data := noisePNGBytes(t, 800, 800)
	p1, _, err1 := ImagePartWithin("noise.png", "image/png", data, 200<<10)
	p2, _, err2 := ImagePartWithin("noise.png", "image/png", data, 200<<10)
	if (err1 == nil) != (err2 == nil) {
		t.Fatalf("nondeterministic success: %v vs %v", err1, err2)
	}
	if err1 == nil && p1.ImageBase64 != p2.ImageBase64 {
		t.Fatal("same input produced different encoded parts")
	}
}

func TestImagePartWithinRejectsUnsupportedMime(t *testing.T) {
	t.Parallel()
	_, _, err := ImagePartWithin("x.bmp", "image/bmp", []byte("BM"), 1<<20)
	if err == nil || !strings.Contains(err.Error(), "convert") {
		t.Fatalf("err=%v want a convert-to-png hint", err)
	}
}

func TestImagePartWithinRejectsAbsurdSize(t *testing.T) {
	t.Parallel()
	big := make([]byte, MaxImageDecodeBytes+1)
	_, _, err := ImagePartWithin("huge.png", "image/png", big, 1<<20)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err=%v want too-large refusal", err)
	}
}
