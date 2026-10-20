package llm

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // decode-only: a source gif is downscaled and re-encoded as PNG
	"image/jpeg"
	"image/png"
	"os"
	"strconv"
	"strings"
)

// Image-reading budgets. A single image is a large object to put on the wire
// and in the transcript, so read_file hands a model at most DefaultMaxImageBytes
// of encoded image, downscaling a larger one first. MaxImageDecodeBytes is the
// ceiling above which we do not even try: decoding a multi-hundred-megabyte
// image to shrink it would cost more than the read is worth, so the reader is
// told to resize it instead.
const (
	// DefaultMaxImageBytes is the default budget for one encoded image part.
	DefaultMaxImageBytes = 3 << 20
	// MaxImageDecodeBytes is the largest original we will decode to downscale.
	MaxImageDecodeBytes = 48 << 20
	// MaxImageDecodePixels is the pixel ceiling on any image we decode to
	// downscale: decode allocates w*h*4 bytes from the header's claim before
	// validating pixel data, so an encoded-size cap alone cannot stop a
	// crafted header from allocating gigabytes. ~64 megapixels (256 MiB of
	// RGBA) is far above any real photo, and far below the bombs.
	MaxImageDecodePixels = 64 << 20
	// MaxImageSidePixels is the per-side pixel ceiling of an image part we put
	// on the wire. A provider rejects the whole request when it is exceeded,
	// and its error names only image formats, so oversize pixels look like a
	// corrupt file: a 2640x11576 screenshot answers
	// 400 ".messages[N].image[0]: You have uploaded an unsupported image…"
	// (measured 2026-10-20: 2640x8192 accepted, 2640x8193 refused, regardless
	// of byte size and content). 8000 is the ceiling every provider in the
	// catalog accepts — Anthropic documents the same hard limit.
	MaxImageSidePixels = 8000
	// ImageMaxBytesEnv overrides the per-image budget with a decimal byte
	// count. An unset, non-numeric or non-positive value falls back to
	// DefaultMaxImageBytes.
	ImageMaxBytesEnv = "FOREBRAIN_IMAGE_MAX_BYTES"
)

// ImageReadResult describes one image read: the MIME type we decided to send,
// the byte sizes before and after any downscale, the pixel dimensions before
// and after, and the integer factor a downscale used (1 = sent verbatim).
type ImageReadResult struct {
	MIMEType                  string
	SourceBytes               int
	SentBytes                 int
	SourceWidth, SourceHeight int
	Width, Height             int
	Scaled                    bool
	Scale                     int
}

// withinSideLimit reports whether encoded dimensions are ones a provider
// accepts. A header that does not parse (w or h 0) is not judged here: the
// decode that follows reports it.
func withinSideLimit(w, h int) bool {
	return w <= MaxImageSidePixels && h <= MaxImageSidePixels
}

// ImagePartWithin encodes data as an image ContentPart no larger than maxBytes.
// A maxBytes <= 0 falls back to ImageMaxBytesEnv, then DefaultMaxImageBytes.
//
// The encoding ladder is fixed so the same input always produces the same
// part (the model-visible prefix never jitters): an image already within
// budget is sent verbatim; otherwise it is downscaled by integer block
// averaging at factors 2, 3, 4, 6, 8 in turn (PNG for a png/gif source, JPEG
// q85 otherwise), and if every step still overflows the largest reduction is
// re-tried as JPEG q75. A source format the standard library cannot decode
// (webp) or an image larger than MaxImageDecodeBytes is refused with an
// actionable error rather than silently truncated.
//
// Every step of that ladder has to satisfy two criteria at once: the encoded
// size stays within the byte budget AND both sides stay within
// MaxImageSidePixels. The pixel ceiling is not a decode guard like
// MaxImageDecodePixels — it is what the provider will accept, so a screenshot
// far under the byte budget is still downscaled (or refused) when one side is
// too long.
func ImagePartWithin(path, mime string, data []byte, maxBytes int) (ContentPart, ImageReadResult, error) {
	budget := resolveImageBudget(maxBytes)
	res := ImageReadResult{MIMEType: mime, SourceBytes: len(data)}

	if len(data) > MaxImageDecodeBytes {
		return ContentPart{}, res, fmt.Errorf("image is too large to read (%d MiB); resize it first", len(data)>>20)
	}
	switch mime {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		// supported: continue
	default:
		return ContentPart{}, res, fmt.Errorf("image type %q cannot be sent to a provider; convert it to png or jpeg first", mime)
	}

	// The header is read before the verbatim decision: a screenshot can sit far
	// under the byte budget and still be refused for one long side, so
	// dimensions are a sendability criterion, not only a decode guard.
	srcW, srcH := DecodeImageConfig(data)
	res.SourceWidth, res.SourceHeight = srcW, srcH

	if len(data) <= budget && withinSideLimit(srcW, srcH) {
		res.Width, res.Height = srcW, srcH
		res.Scale = 1
		res.SentBytes = len(data)
		return withSourcePath(ImageBase64(mime, base64.StdEncoding.EncodeToString(data)), path), res, nil
	}

	// Standard-library Go has no webp decoder, so a webp that cannot go verbatim
	// cannot be shrunk here either; name the limit it broke instead of handing
	// the provider an image it will reject.
	if mime == "image/webp" {
		if len(data) > budget {
			return ContentPart{}, res, fmt.Errorf("image is %d MiB and webp cannot be downscaled here; convert it to png first", len(data)>>20)
		}
		return ContentPart{}, res, fmt.Errorf("image is %dx%d and webp cannot be downscaled here; convert it to png first", srcW, srcH)
	}

	// The decode below allocates from the header's dimensions before it
	// validates pixel data, so the pixel budget must be judged from the
	// header, before decode: a crafted IHDR claiming 60000x60000 would
	// otherwise allocate gigabytes before the decoder ever fails. A header
	// that does not parse (w or h 0) falls through and fails at decode with
	// the decoder's own reason.
	if srcW > 0 && srcH > 0 && srcW*srcH > MaxImageDecodePixels {
		return ContentPart{}, res, fmt.Errorf(
			"image is %dx%d (%d MP); the decode limit is %d MP — crop or resize it first",
			srcW, srcH, srcW*srcH>>20, MaxImageDecodePixels>>20)
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		if srcW > 0 && srcH > 0 && !withinSideLimit(srcW, srcH) {
			return ContentPart{}, res, fmt.Errorf(
				"image is %dx%d, past the %d-pixel per-side limit, and could not be decoded to downscale it; crop or resize it first: %w",
				srcW, srcH, MaxImageSidePixels, err)
		}
		return ContentPart{}, res, fmt.Errorf("image could not be decoded to downscale it; crop or resize it first: %w", err)
	}
	srcBounds := src.Bounds()
	res.SourceWidth, res.SourceHeight = srcBounds.Dx(), srcBounds.Dy()

	// A png/gif source re-encodes losslessly as PNG; everything else is JPEG.
	scaleMime := "image/jpeg"
	if mime == "image/png" || mime == "image/gif" {
		scaleMime = "image/png"
	}

	var lastImg image.Image
	for _, s := range []int{2, 3, 4, 6, 8} {
		dst := downscaleBlockAverage(src, s)
		if !withinSideLimit(dst.Bounds().Dx(), dst.Bounds().Dy()) {
			continue
		}
		lastImg = dst
		encoded, err := encodeImage(dst, scaleMime, 85)
		if err != nil {
			continue
		}
		candidate := res
		candidate.MIMEType = scaleMime
		candidate.Width, candidate.Height = dst.Bounds().Dx(), dst.Bounds().Dy()
		candidate.Scaled = true
		candidate.Scale = s
		candidate.SentBytes = len(encoded)
		if len(encoded) <= budget {
			return withSourcePath(ImageBase64(scaleMime, base64.StdEncoding.EncodeToString(encoded)), path), candidate, nil
		}
	}

	// Every reduction still overflows: the largest one re-encoded as JPEG q75
	// is the last thing we can offer.
	if lastImg != nil {
		if encoded, err := encodeImage(lastImg, "image/jpeg", 75); err == nil {
			candidate := res
			candidate.MIMEType = "image/jpeg"
			candidate.Width, candidate.Height = lastImg.Bounds().Dx(), lastImg.Bounds().Dy()
			candidate.Scaled = true
			candidate.Scale = 8
			candidate.SentBytes = len(encoded)
			if len(encoded) <= budget {
				return withSourcePath(ImageBase64("image/jpeg", base64.StdEncoding.EncodeToString(encoded)), path), candidate, nil
			}
		}
	}
	if lastImg == nil {
		// The source is wider or taller than any provider accepts and the ladder
		// never produced a candidate under the ceiling.
		return ContentPart{}, res, fmt.Errorf(
			"image is %dx%d and cannot be reduced below the %d-pixel per-side limit; crop or resize it first",
			srcBounds.Dx(), srcBounds.Dy(), MaxImageSidePixels)
	}
	return ContentPart{}, res, fmt.Errorf("image is %d MiB and cannot be reduced below %d MiB; crop or resize it first", len(data)>>20, budget>>20)
}

// withSourcePath stamps the on-disk origin onto an image part so persistence
// can store a file_reference instead of the bytes (see ContentPart.SourcePath).
// A part built without a path keeps its zero value.
func withSourcePath(part ContentPart, path string) ContentPart {
	if path != "" {
		part.SourcePath = path
	}
	return part
}

// resolveImageBudget is the budget for one image part: an explicit maxBytes,
// else $FOREBRAIN_IMAGE_MAX_BYTES, else DefaultMaxImageBytes.
func resolveImageBudget(maxBytes int) int {
	if maxBytes > 0 {
		return maxBytes
	}
	if v := strings.TrimSpace(os.Getenv(ImageMaxBytesEnv)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return DefaultMaxImageBytes
}

// DecodeImageConfig returns the pixel dimensions of encoded image data by
// reading only its header, or (0, 0) when the header cannot be read — a
// failure is not an error here. It never decodes pixels; callers use it to
// state file facts about an image that was not sent (a refused or degraded
// block), where the dimensions are metadata, not something to pay for.
//
// A WebP is read by webpDimensions, which costs nothing for the formats the
// standard library does handle: the magic number decides first.
func DecodeImageConfig(data []byte) (int, int) {
	if w, h, ok := webpDimensions(data); ok {
		return w, h
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// encodeImage encodes img in the named format: PNG for "image/png", JPEG at
// the given quality otherwise.
func encodeImage(img image.Image, mime string, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if mime == "image/png" {
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// downscaleBlockAverage shrinks src by factor s, each destination pixel the
// average of the s×s block of source pixels at the same position (edge blocks
// average only the pixels that exist). It is an integer reducer, so the same
// input and factor always produce the same output.
func downscaleBlockAverage(src image.Image, s int) image.Image {
	if s < 1 {
		s = 1
	}
	b := src.Bounds()
	w := (b.Dx() + s - 1) / s
	h := (b.Dy() + s - 1) / s
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var rSum, gSum, bSum, aSum, n uint64
			for dy := 0; dy < s; dy++ {
				for dx := 0; dx < s; dx++ {
					sx := b.Min.X + x*s + dx
					sy := b.Min.Y + y*s + dy
					if sx >= b.Max.X || sy >= b.Max.Y {
						continue
					}
					r, g, bl, a := src.At(sx, sy).RGBA()
					rSum += uint64(r)
					gSum += uint64(g)
					bSum += uint64(bl)
					aSum += uint64(a)
					n++
				}
			}
			if n == 0 {
				continue
			}
			dst.SetNRGBA(x, y, color.NRGBA{
				R: uint8(rSum / n >> 8),
				G: uint8(gSum / n >> 8),
				B: uint8(bSum / n >> 8),
				A: uint8(aSum / n >> 8),
			})
		}
	}
	return dst
}
