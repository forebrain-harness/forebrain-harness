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

// TestImagePartWithinRefusesAnUnreadableHeader pins the branch that used to
// pass silently: a file under the byte budget whose header does not parse got
// dimensions of (0,0), the per-side check read "0 <= 8000, 0 <= 8000" as a
// pass, and the bytes went to the provider verbatim — which then refused the
// whole request as an unsupported image. The reader now says what is wrong
// here, in terms the person holding the file can act on.
func TestImagePartWithinRefusesAnUnreadableHeader(t *testing.T) {
	t.Parallel()
	full := pngBytes(t, 16, 16)
	if len(full) < 40 {
		t.Fatalf("fixture too small to truncate: %d bytes", len(full))
	}
	// A PNG cut inside its IHDR: the signature survives (so the file still
	// sniffs as image/png) but no dimensions can be read from it. png.Encode
	// writes a 33-byte signature+IHDR header, so 20 bytes is unambiguously cut
	// mid-header.
	truncated := full[:20]
	if w, h := DecodeImageConfig(truncated); w != 0 || h != 0 {
		t.Fatalf("fixture premise broken: the cut header parsed as %dx%d", w, h)
	}
	part, res, err := ImagePartWithin("shot.png", "image/png", truncated, 1<<20)
	if err == nil {
		t.Fatalf("a %d-byte file with no readable header was accepted: res=%+v", len(truncated), res)
	}
	if part.ImageBase64 != "" || part.Type != "" {
		t.Fatalf("a refused image must not come back as a part: %+v", part)
	}
	// Actionable: it says the file is broken, not that a downscale failed.
	for _, want := range []string{"truncated or corrupt", "header"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err=%v want it to contain %q", err, want)
		}
	}
	if !strings.Contains(err.Error(), "resave") && !strings.Contains(err.Error(), "re-save") {
		t.Fatalf("err=%v want it to tell the reader what to do with the file", err)
	}
	if res.Scale != 0 || res.SentBytes != 0 {
		t.Fatalf("a refused image reports nothing sent: %+v", res)
	}

	// The same cut on a webp takes the other branch (this build cannot decode
	// one), and it must not report "0x0" as if the picture were empty.
	webp := []byte("RIFF\x00\x00\x00\x00WEBP")
	part, _, err = ImagePartWithin("shot.webp", "image/webp", webp, 1<<20)
	if err == nil {
		t.Fatalf("a %d-byte webp with no readable header was accepted: %+v", len(webp), part)
	}
	if strings.Contains(err.Error(), "0x0") {
		t.Fatalf("err=%v reports empty dimensions instead of an unreadable header", err)
	}
	if !strings.Contains(err.Error(), "truncated or corrupt") {
		t.Fatalf("err=%v want it to say the file is broken", err)
	}
}

// TestImagePartWithinKeepsTheVerbatimPathForGoodImages is the other half of the
// same condition: requiring readable dimensions must not cost a valid image its
// verbatim send — no re-encode, no byte change, no scale.
func TestImagePartWithinKeepsTheVerbatimPathForGoodImages(t *testing.T) {
	t.Parallel()
	data := pngBytes(t, 32, 24)
	part, res, err := ImagePartWithin("shot.png", "image/png", data, 1<<20)
	if err != nil {
		t.Fatalf("ImagePartWithin: %v", err)
	}
	if res.Scale != 1 || res.Scaled {
		t.Fatalf("a good small image must go verbatim: scaled=%v scale=%d", res.Scaled, res.Scale)
	}
	if res.SentBytes != len(data) || res.SourceBytes != len(data) {
		t.Fatalf("bytes sent=%d source=%d want both %d", res.SentBytes, res.SourceBytes, len(data))
	}
	decoded, err := base64.StdEncoding.DecodeString(part.ImageBase64)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("the verbatim path changed the image's bytes")
	}
	if res.Width != 32 || res.Height != 24 {
		t.Fatalf("reported %dx%d want 32x24", res.Width, res.Height)
	}
}

// TestImagePartWithinReportsTheScaleItActuallyUsed pins what a reported scale
// means: the image that comes back is the source reduced by that factor, so a
// caller (read_file's diagnostic line, a compaction note) can describe what
// happened without decoding the picture again. The last rung re-encodes an
// image it did not build itself, which is exactly where a constant would lie
// the moment the ladder stops at a different rung.
func TestImagePartWithinReportsTheScaleItActuallyUsed(t *testing.T) {
	t.Parallel()
	data := noisePNGBytes(t, 1200, 1200)
	// A sweep rather than one budget: which rung fits, and whether the final
	// JPEG re-encode is what fits, depends on how well the noise compresses.
	// Every answer that does come back has to describe itself consistently.
	lastDitch := 0
	accepted := 0
	for _, budget := range []int{120 << 10, 90 << 10, 70 << 10, 55 << 10, 40 << 10, 28 << 10, 20 << 10, 14 << 10} {
		part, res, err := ImagePartWithin("noise.png", "image/png", data, budget)
		if err != nil {
			continue // this budget cannot be met; the next one asks a different question
		}
		accepted++
		if part.ImageBase64 == "" {
			t.Fatalf("budget %d: reported success with no part", budget)
		}
		if res.SentBytes <= 0 || res.SentBytes > budget {
			t.Fatalf("budget %d: sent %d bytes, want (0, %d]", budget, res.SentBytes, budget)
		}
		if res.Scale < 1 {
			t.Fatalf("budget %d: scale=%d, want the factor that produced this image", budget, res.Scale)
		}
		// The invariant a constant cannot satisfy: the dimensions sent are the
		// source dimensions divided by the reported factor, rounded up (the
		// ladder's block average), for both sides.
		wantW := (res.SourceWidth + res.Scale - 1) / res.Scale
		wantH := (res.SourceHeight + res.Scale - 1) / res.Scale
		if res.Width != wantW || res.Height != wantH {
			t.Fatalf("budget %d: reported %dx%d at scale %d, but %dx%d divided by that factor is %dx%d",
				budget, res.Width, res.Height, res.Scale, res.SourceWidth, res.SourceHeight, wantW, wantH)
		}
		if res.MIMEType == "image/jpeg" {
			lastDitch++
		}
	}
	if accepted == 0 {
		t.Fatal("no budget in the sweep produced an image; the fixture no longer exercises the ladder")
	}
	if lastDitch == 0 {
		t.Fatalf("no budget reached the final JPEG re-encode; the sweep no longer covers the branch under test (accepted=%d)", accepted)
	}
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

// TestImagePartWithinRefusesOversizeSideItCannotDecode pins the pixel ceiling
// on the verbatim decision. The old contract here was the opposite — a file
// under the byte budget was sent as-is whatever its header claimed — and the
// provider refuted it: 2640x8192 is accepted and 2640x8193 answered
// 400 ".messages[N].image[0]: You have uploaded an unsupported image…" for any
// byte size or content (measured 2026-10-20). A header-only file that names
// such a size cannot be decoded to shrink it, so it is refused with a reason
// the caller can act on rather than poisoning every later request.
func TestImagePartWithinRefusesOversizeSideItCannotDecode(t *testing.T) {
	t.Parallel()
	data := headerOnlyPNG(9000, 5000, 4<<10) // 45 MP: under the decode ceiling, one side over the provider's
	_, res, err := ImagePartWithin("claimed.png", "image/png", data, DefaultMaxImageBytes)
	if err == nil {
		t.Fatalf("a 9000x5000 header was accepted: res=%+v", res)
	}
	for _, want := range []string{"per-side", "8000"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err=%q want it to name %q", err, want)
		}
	}
	if res.SourceWidth != 9000 || res.SourceHeight != 5000 {
		t.Fatalf("reported %dx%d want the claimed 9000x5000", res.SourceWidth, res.SourceHeight)
	}
}

// TestImagePartWithinDownscalesToProviderSideLimit pins the case the provider
// actually rejected in the field: a screenshot far under the byte budget whose
// height is past the per-side ceiling. It must go through the same fixed
// ladder a byte overflow uses, and what leaves must be within the ceiling.
func TestImagePartWithinDownscalesToProviderSideLimit(t *testing.T) {
	t.Parallel()
	data := pngBytes(t, 100, 9000)
	if len(data) > DefaultMaxImageBytes {
		t.Fatalf("test image is %d bytes; it must be under the byte budget for this case", len(data))
	}
	part, res, err := ImagePartWithin("tall.png", "image/png", data, DefaultMaxImageBytes)
	if err != nil {
		t.Fatalf("ImagePartWithin: %v", err)
	}
	if !res.Scaled || res.Scale < 2 {
		t.Fatalf("res=%+v want a scaled send (the old verbatim path is the bug)", res)
	}
	if res.Height > MaxImageSidePixels || res.Width > MaxImageSidePixels {
		t.Fatalf("res=%dx%d want both sides within %d", res.Width, res.Height, MaxImageSidePixels)
	}
	decoded, err := base64.StdEncoding.DecodeString(part.ImageBase64)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	w, h := DecodeImageConfig(decoded)
	if w > MaxImageSidePixels || h > MaxImageSidePixels {
		t.Fatalf("sent image is %dx%d, past the %d-pixel per-side limit", w, h, MaxImageSidePixels)
	}
	if w != res.Width || h != res.Height {
		t.Fatalf("sent %dx%d but reported %dx%d", w, h, res.Width, res.Height)
	}
}

// webpVP8X builds a minimal VP8X container whose canvas is w×h: RIFF header,
// then a VP8X chunk carrying flags, three reserved bytes and the two 24-bit
// dimensions (stored minus one). The chunk's own size field is part of the
// container — leaving it out makes the walk in webpDimensions stop before the
// body and report (0, 0).
func webpVP8X(w, h int) []byte {
	body := make([]byte, 10)
	body[0] = 0 // flags: no animation, no alpha, no ICC
	le := func(b []byte, v uint32) {
		b[0] = byte(v)
		b[1] = byte(v >> 8)
		b[2] = byte(v >> 16)
	}
	le(body[4:7], uint32(w-1))
	le(body[7:10], uint32(h-1))

	var buf bytes.Buffer
	buf.WriteString("RIFF")
	var b4 [4]byte
	binary.LittleEndian.PutUint32(b4[:], uint32(4+8+len(body))) // "WEBP" + chunk header + body
	buf.Write(b4[:])
	buf.WriteString("WEBP")
	buf.WriteString("VP8X")
	binary.LittleEndian.PutUint32(b4[:], uint32(len(body)))
	buf.Write(b4[:])
	buf.Write(body)
	return buf.Bytes()
}

// TestImagePartWithinRefusesOversizeSideWebP pins the one format the ladder
// cannot shrink: the standard library has no webp decoder, so a webp past the
// per-side ceiling must be named as unsendable instead of being handed to a
// provider that will refuse the whole request for it.
func TestImagePartWithinRefusesOversizeSideWebP(t *testing.T) {
	t.Parallel()
	data := webpVP8X(9000, 100)
	if w, h := DecodeImageConfig(data); w != 9000 || h != 100 {
		t.Fatalf("test container reads as %dx%d, want 9000x100", w, h)
	}
	_, _, err := ImagePartWithin("tall.webp", "image/webp", data, DefaultMaxImageBytes)
	if err == nil {
		t.Fatal("a 9000x100 webp was accepted")
	}
	if !strings.Contains(err.Error(), "webp cannot be downscaled here") {
		t.Fatalf("err=%q want it to name the webp limit", err)
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

// TestImageFileDownscalesOversizeSideAttachment pins the attachment path
// end-to-end: a screenshot under the byte budget whose long side is past the
// provider ceiling is downscaled by the same ladder before it is sent.
func TestImageFileDownscalesOversizeSideAttachment(t *testing.T) {
	t.Setenv(ImageMaxBytesEnv, "")
	path := writeTempFile(t, "tall.png", pngBytes(t, 100, 9000))
	part, err := ImageFile(path)
	if err != nil {
		t.Fatalf("ImageFile: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(part.ImageBase64)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	w, h := DecodeImageConfig(decoded)
	if w > MaxImageSidePixels || h > MaxImageSidePixels {
		t.Fatalf("sent image is %dx%d, past the %d-pixel per-side limit", w, h, MaxImageSidePixels)
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
