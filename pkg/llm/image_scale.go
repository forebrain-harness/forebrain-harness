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

	if len(data) <= budget {
		w, h := decodeImageConfig(data)
		res.SourceWidth, res.SourceHeight = w, h
		res.Width, res.Height = w, h
		res.Scale = 1
		res.SentBytes = len(data)
		return ImageBase64(mime, base64.StdEncoding.EncodeToString(data)), res, nil
	}

	// Standard-library Go has no webp decoder, so a webp over budget cannot be
	// downscaled here; say so instead of failing to decode it later.
	if mime == "image/webp" {
		return ContentPart{}, res, fmt.Errorf("image is %d MiB and webp cannot be downscaled here; convert it to png first", len(data)>>20)
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
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
			return ImageBase64(scaleMime, base64.StdEncoding.EncodeToString(encoded)), candidate, nil
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
				return ImageBase64("image/jpeg", base64.StdEncoding.EncodeToString(encoded)), candidate, nil
			}
		}
	}
	return ContentPart{}, res, fmt.Errorf("image is %d MiB and cannot be reduced below %d MiB; crop or resize it first", len(data)>>20, budget>>20)
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

// decodeImageConfig returns the pixel dimensions of encoded image data, or
// (0, 0) when the header cannot be read — a failure is not an error here.
func decodeImageConfig(data []byte) (int, int) {
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
