package llm

import "encoding/binary"

// webpDimensions returns the pixel dimensions recorded in an encoded WebP
// header, or ok=false when data is not a WebP this reader understands. It reads
// headers only — the same contract as DecodeImageConfig — because nothing here
// decodes pixels, and a WebP we hand to a provider is sent verbatim (the
// standard library cannot downscale one), so its size is a fact the model note
// and every surface have to state.
//
// WebP lives in a RIFF container, so the file is a prelude followed by chunks
// of which exactly one carries the picture. All three layouts are covered:
// "VP8 " states the size in the lossy keyframe header, "VP8L" packs both
// dimensions into the four bytes after its signature, and "VP8X" (which also
// fronts every extended/animated file) states the canvas size minus one.
func webpDimensions(data []byte) (int, int, bool) {
	if len(data) < 20 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0, false
	}
	// The RIFF size bounds the walk: a well-formed file names its picture chunk
	// inside it, and a payload whose header declares nothing (the synthetic
	// "RIFF…WEBP" plus filler the degraded-block paths are tested with) fails at
	// once instead of scanning megabytes of zeros.
	end := min(len(data), 8+int(binary.LittleEndian.Uint32(data[4:8])))
	for off := 12; off+8 <= end; {
		kind := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		if off+8+size > end {
			return 0, 0, false // a chunk that runs past the container: truncated
		}
		if w, h, ok := webpPictureDimensions(kind, data[off+8:off+8+size]); ok {
			return w, h, true
		}
		off += 8 + size + size&1 // RIFF pads every chunk to an even length
	}
	return 0, 0, false
}

// webpPictureDimensions reads one chunk payload, reporting ok=false for a chunk
// that is not a picture or whose header does not match the format it claims.
func webpPictureDimensions(kind string, body []byte) (int, int, bool) {
	switch kind {
	case "VP8 ":
		// A lossy keyframe: a 3-byte frame tag, then the 0x9d012a start code,
		// then the two 14-bit dimensions.
		if len(body) < 10 || body[3] != 0x9d || body[4] != 0x01 || body[5] != 0x2a {
			return 0, 0, false
		}
		return int(binary.LittleEndian.Uint16(body[6:8]) & 0x3fff),
			int(binary.LittleEndian.Uint16(body[8:10]) & 0x3fff), true
	case "VP8L":
		// A lossless stream: the 0x2f signature, then both dimensions minus
		// one, 14 bits each.
		if len(body) < 5 || body[0] != 0x2f {
			return 0, 0, false
		}
		bits := binary.LittleEndian.Uint32(body[1:5])
		return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1, true
	case "VP8X":
		// Flags, three reserved bytes, then the canvas size minus one as two
		// 24-bit values — the same dimensions in a still and an animation.
		if len(body) < 10 {
			return 0, 0, false
		}
		return int(le24(body[4:7])) + 1, int(le24(body[7:10])) + 1, true
	}
	return 0, 0, false
}

// le24 reads a 24-bit little-endian unsigned integer.
func le24(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
}
