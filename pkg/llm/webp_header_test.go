package llm

import (
	"encoding/hex"
	"testing"
)

// The three real WebP files below cover every layout the format uses: a lossy
// keyframe ("VP8 "), a lossless stream ("VP8L"), and an extended container
// ("VP8X", here with a trailing EXIF chunk). Their dimensions were read back
// from the encoded files with macOS `sips -g pixelWidth -g pixelHeight`, which
// reports 7×3, 9×5 and 11×4 — the numbers asserted here are the files' own
// facts, not a restatement of the reader.
const (
	lossyWebpHex = "52494646300000005745425056503820240000005001009d012a07000300" +
		"01402625004e802ea000fefb2e4bc5dd88e327875553d7dfa000"
	losslessWebpHex = "5249464620000000574542505650384c140000002f08000100075081540820000a9afec78888fe07"
	extendedWebpHex = "524946465200000057454250565038580a000000080000000a00000300005650" +
		"3820180000003001009d012a0b00040001402625a400037000fefccab0004558" +
		"4946140000000000000000000000000000000000000000000000"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("fixture is not valid hex: %v", err)
	}
	return b
}

// TestWebpDimensionsReadsEveryWebpLayout pins the whole point of the header
// reader: a WebP is sent to a provider verbatim, so its true size has to come
// out of the file even though no WebP decoder exists here.
func TestWebpDimensionsReadsEveryWebpLayout(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		hex  string
		w, h int
	}{
		{name: "lossy VP8", hex: lossyWebpHex, w: 7, h: 3},
		{name: "lossless VP8L", hex: losslessWebpHex, w: 9, h: 5},
		{name: "extended VP8X", hex: extendedWebpHex, w: 11, h: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := mustHex(t, tc.hex)
			w, h, ok := webpDimensions(data)
			if !ok || w != tc.w || h != tc.h {
				t.Fatalf("webpDimensions = (%d, %d, %v), want (%d, %d, true)", w, h, ok, tc.w, tc.h)
			}
			if dw, dh := DecodeImageConfig(data); dw != tc.w || dh != tc.h {
				t.Fatalf("DecodeImageConfig = %dx%d, want %dx%d", dw, dh, tc.w, tc.h)
			}
		})
	}
}

// TestWebpDimensionsDeclinesWhatItCannotRead pins that the reader claims only
// what it actually understood: a non-WebP must fall through to the standard
// library, and a truncated or corrupt header must report no dimensions at all
// rather than a made-up pair.
func TestWebpDimensionsDeclinesWhatItCannotRead(t *testing.T) {
	t.Parallel()
	lossy := mustHex(t, lossyWebpHex)
	badStartCode := mustHex(t, lossyWebpHex)
	badStartCode[23] = 0x00 // the VP8 keyframe start code is 0x9d012a
	// A "RIFF…WEBP" header that declares an empty container, as built by the
	// degraded-block paths: it must be refused without walking the filler.
	fakeHeader := make([]byte, 4<<20)
	copy(fakeHeader, "RIFF\x00\x00\x00\x00WEBP")

	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "nothing", data: nil},
		{name: "png", data: pngBytes(t, 5, 7)},
		{name: "prose", data: []byte("RIFF and then some words, but not a WEBP file at all")},
		{name: "truncated prelude", data: lossy[:10]},
		{name: "truncated picture chunk", data: lossy[:30]},
		{name: "no start code", data: badStartCode},
		{name: "empty container", data: fakeHeader},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if w, h, ok := webpDimensions(tc.data); ok {
				t.Fatalf("webpDimensions = (%d, %d, true), want ok=false", w, h)
			}
		})
	}
}

// TestDecodeImageConfigStillDecodesTheStandardLibraryFormats pins that the
// WebP shortcut does not hijack the formats the standard library does handle.
func TestDecodeImageConfigStillDecodesTheStandardLibraryFormats(t *testing.T) {
	t.Parallel()
	if w, h := DecodeImageConfig(pngBytes(t, 5, 7)); w != 5 || h != 7 {
		t.Fatalf("DecodeImageConfig(png) = %dx%d, want 5x7", w, h)
	}
	if w, h := DecodeImageConfig([]byte("not an image at all")); w != 0 || h != 0 {
		t.Fatalf("DecodeImageConfig(prose) = %dx%d, want 0x0", w, h)
	}
}
