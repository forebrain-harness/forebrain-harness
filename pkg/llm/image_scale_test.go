package llm

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
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

// TestImageMIMETypeSniffIsAuthoritative pins that the content sniff, not the
// file extension, decides: real image bytes are named however the file is
// spelled, and bytes that are not one of the four sendable formats report ""
// however they are named.
func TestImageMIMETypeSniffIsAuthoritative(t *testing.T) {
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
		// The sniff outranks the extension: real PNG bytes stay image/png
		// under a name that promises no image.
		{name: "png content, txt ext", path: "note.txt", data: png, want: "image/png"},
		{name: "png content, bin ext", path: "shot.bin", data: png, want: "image/png"},
		// Text bytes are not an image however they are named: a .png name
		// must not relabel them image/png and send them to a provider or
		// past an image-only gate.
		{name: "text content, png ext", path: "fake.png", data: text, want: ""},
		{name: "text content, txt ext", path: "note.txt", data: text, want: ""},
		// The sniff is authoritative for images we cannot send: BMP bytes in a
		// .png-named file must report "" (not image/png), so read_file gives its
		// "convert it" error instead of sending BMP bytes as PNG.
		{name: "bmp content, png ext", path: "shot.png", data: bmpBytes(), want: ""},
		// A truncated PNG whose magic bytes are present still names itself
		// image/png: whether the pixels decode is the provider's call, not a
		// reason to fall back to the extension.
		{name: "truncated png, png ext", path: "shot.png", data: headerOnlyPNG(2, 2, 64), want: "image/png"},
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

// headerOnlyPNG builds a PNG whose IHDR claims w×h but whose body is padding:
// only the signature and a CRC-valid IHDR chunk are real. The pixel data is
// never there — the point is what the header claims, judged before decode.
func headerOnlyPNG(w, h uint32, total int) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], w)
	binary.BigEndian.PutUint32(ihdr[4:8], h)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 6 // color type: RGBA
	var buf bytes.Buffer
	buf.Write([]byte("\x89PNG\r\n\x1a\n"))
	var b4 [4]byte
	binary.BigEndian.PutUint32(b4[:], uint32(len(ihdr)))
	buf.Write(b4[:])
	buf.WriteString("IHDR")
	buf.Write(ihdr)
	binary.BigEndian.PutUint32(b4[:], crc32.ChecksumIEEE(append([]byte("IHDR"), ihdr...)))
	buf.Write(b4[:])
	if pad := total - buf.Len(); pad > 0 {
		buf.Write(bytes.Repeat([]byte{0}, pad))
	}
	return buf.Bytes()
}

// TestImagePartWithinRejectsPixelBomb pins the pre-decode pixel ceiling: a
// 3.5 MiB PNG whose IHDR claims 60000×60000 would allocate ~14 GB inside
// image.Decode (the header is paid for before any pixel is validated), so it
// must be refused by the header check — the error names the decode limit, not
// a decode failure, proving the guard fired before Decode.
func TestImagePartWithinRejectsPixelBomb(t *testing.T) {
	t.Parallel()
	bomb := headerOnlyPNG(60000, 60000, 3500*1024)
	if len(bomb) > MaxImageDecodeBytes {
		t.Fatalf("bomb is %d bytes, want it under the decode-bytes cap so the pixel guard is what fires", len(bomb))
	}
	_, _, err := ImagePartWithin("bomb.png", "image/png", bomb, 1<<20)
	if err == nil {
		t.Fatal("a 60000x60000 header bomb was accepted")
	}
	if !strings.Contains(err.Error(), "decode limit") {
		t.Fatalf("err=%q want it to name the decode limit", err)
	}
	if strings.Contains(err.Error(), "could not be decoded") {
		t.Fatalf("err=%q reached image.Decode; the pixel guard must fire before decode", err)
	}
}

// TestImagePartWithinVerbatimPathHasNoPixelCap pins that the direct-send path
// (data already within budget, sent verbatim without decoding) has no pixel
// ceiling: a tiny file claiming 60000×60000 is sent as-is, because those
// bytes cost the process nothing locally.
func TestImagePartWithinVerbatimPathHasNoPixelCap(t *testing.T) {
	t.Parallel()
	data := headerOnlyPNG(60000, 60000, 4<<10)
	part, res, err := ImagePartWithin("claimed.png", "image/png", data, DefaultMaxImageBytes)
	if err != nil {
		t.Fatalf("a within-budget file must be sent verbatim regardless of its header claim: %v", err)
	}
	if res.Scaled || res.SentBytes != len(data) {
		t.Fatalf("res=%+v want verbatim send", res)
	}
	if res.SourceWidth != 60000 || res.SourceHeight != 60000 {
		t.Fatalf("reported %dx%d want the claimed 60000x60000", res.SourceWidth, res.SourceHeight)
	}
	decoded, err := base64.StdEncoding.DecodeString(part.ImageBase64)
	if err != nil || !bytes.Equal(decoded, data) {
		t.Fatal("verbatim image bytes changed")
	}
}

// writeTempFile writes data to a fresh file under t.TempDir and returns its
// path, so ImageFile tests read from a real on-disk file like production.
func writeTempFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// TestImageFileKeepsSmallFilesVerbatim pins the lossless half of the
// attachment budget: a user attachment within the default budget is sent
// byte-for-byte, its base64 exactly the encoding of the file's bytes.
func TestImageFileKeepsSmallFilesVerbatim(t *testing.T) {
	t.Setenv(ImageMaxBytesEnv, "") // unset: the default budget, not a stray env value
	data := pngBytes(t, 64, 64)
	part, err := ImageFile(writeTempFile(t, "shot.png", data))
	if err != nil {
		t.Fatalf("ImageFile: %v", err)
	}
	if part.MIMEType != "image/png" {
		t.Fatalf("mime=%q want image/png", part.MIMEType)
	}
	if want := base64.StdEncoding.EncodeToString(data); part.ImageBase64 != want {
		t.Fatal("a within-budget attachment must be sent byte-for-byte")
	}
}

// TestImageFileAppliesTheImageBudget pins the cost half: an attachment over
// the budget is downscaled by the ladder (sent bytes below the source, still
// a png), and FOREBRAIN_IMAGE_MAX_BYTES tightens the attachment path exactly
// as it tightens the tool paths.
func TestImageFileAppliesTheImageBudget(t *testing.T) {
	t.Setenv(ImageMaxBytesEnv, "")       // default 3 MiB
	data := noisePNGBytes(t, 1024, 1024) // ~4 MiB encoded: over the default budget
	if len(data) <= DefaultMaxImageBytes {
		t.Fatalf("test image too small to exercise downscaling: %d bytes", len(data))
	}
	path := writeTempFile(t, "shot.png", data)

	part, err := ImageFile(path)
	if err != nil {
		t.Fatalf("ImageFile: %v", err)
	}
	if part.MIMEType != "image/png" {
		t.Fatalf("mime=%q want image/png (a png source re-encodes as png)", part.MIMEType)
	}
	decoded, err := base64.StdEncoding.DecodeString(part.ImageBase64)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	if len(decoded) >= len(data) {
		t.Fatalf("over-budget attachment sent %d bytes, want fewer than the %d-byte source", len(decoded), len(data))
	}
	defaultSend := len(decoded)

	tight := 300 << 10
	t.Setenv(ImageMaxBytesEnv, strconv.Itoa(tight))
	part, err = ImageFile(path)
	if err != nil {
		t.Fatalf("ImageFile with a tightened budget: %v", err)
	}
	decoded, err = base64.StdEncoding.DecodeString(part.ImageBase64)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	if len(decoded) > tight || len(decoded) >= defaultSend {
		t.Fatalf("tightened budget sent %d bytes, want ≤%d and below the default-budget %d", len(decoded), tight, defaultSend)
	}
}

// TestImageFileRefusesOverCeilingFiles pins the refusal half: an attachment
// over the decode ceiling gets the same actionable resize-it-first error the
// tool paths give, and no part comes back.
func TestImageFileRefusesOverCeilingFiles(t *testing.T) {
	// A real PNG header so the sniff names the file; the size alone refuses.
	big := make([]byte, MaxImageDecodeBytes+1)
	copy(big, pngBytes(t, 2, 2))
	part, err := ImageFile(writeTempFile(t, "huge.png", big))
	if err == nil || !strings.Contains(err.Error(), "resize") {
		t.Fatalf("err=%v want an actionable resize-it-first refusal", err)
	}
	if part.ImageBase64 != "" {
		t.Fatal("a refused image must not return a part")
	}
}

// TestImageFileRejectsNonImageBytesByName pins the type refusal for user
// attachments: bytes the sniff cannot send, however the file is named, get an
// error that names the file and says what to do — never a part.
func TestImageFileRejectsNonImageBytesByName(t *testing.T) {
	path := writeTempFile(t, "fake.png", []byte("this is not an image at all, just words"))
	_, err := ImageFile(path)
	if err == nil || !strings.Contains(err.Error(), "convert") || !strings.Contains(err.Error(), "fake.png") {
		t.Fatalf("err=%v want a convert-it refusal that names the file", err)
	}
}
