package run

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// withImageSourceNotes follows every image of a user message with the line
// naming where it is stored (llm.ImageSourceNote), so the model can open the
// file itself — crop or enlarge it with a script — when the picture as sent is
// too small to read. The live terminal turn writes the same line from the same
// path (turn.SurfaceTurnContentParts), and the web turn's live input is this
// function, so a turn and every replay of it carry the same text.
func withImageSourceNotes(parts []llm.ContentPart) []llm.ContentPart {
	text := llm.TextContent(parts...)
	var out []llm.ContentPart
	for i, part := range parts {
		if part.Type != llm.ContentTypeImageBase64 && part.Type != llm.ContentTypeImageURL {
			if out != nil {
				out = append(out, part)
			}
			continue
		}
		if out == nil {
			out = append(make([]llm.ContentPart, 0, len(parts)+2), parts[:i]...)
		}
		out = append(out, part)
		// A message that already carries the line (a compaction's
		// replacement history keeps the request's own copy) gets no second.
		if i+1 < len(parts) && llm.IsImageSourceNote(parts[i+1]) {
			continue
		}
		if source := ImageSource(part); source != "" && !llm.ImageFileNamedInText(text, source) {
			out = append(out, llm.Text(llm.ImageSourceNote(source)))
		}
	}
	if out == nil {
		return parts
	}
	return out
}

// ImageSource is where an image a user sent is stored: the file it was read
// from; for one a message carried inline (rows written before attachments were
// kept by reference, and imported conversations), a file written from its
// bytes; for a web image, its URL. "" when it has none.
//
// The inline image's file is named by its bytes, so every replay names the
// same file and writes it at most once; a file the system's temporary-file
// cleanup removed is written again by the next replay. The part itself is not
// pointed at that file: a part that names a file is persisted by reference,
// and a temporary file must never stand in for bytes the row still holds.
func ImageSource(part llm.ContentPart) string {
	if source := strings.TrimSpace(part.SourcePath); source != "" {
		return source
	}
	mime, data := strings.TrimSpace(part.MIMEType), strings.TrimSpace(part.ImageBase64)
	if part.Type == llm.ContentTypeImageURL {
		url := strings.TrimSpace(part.ImageURL)
		header, payload, ok := strings.Cut(url, ",")
		if !strings.HasPrefix(url, "data:") {
			return url
		}
		if !ok || !strings.HasSuffix(header, ";base64") {
			return ""
		}
		mime = strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
		data = payload
	}
	if data == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(data))
	path := filepath.Join(inlineImageDir(), hex.EncodeToString(sum[:8])+imageExtension(mime))
	if _, err := os.Stat(path); err == nil {
		return path
	}
	if err := writeImageFile(path, data); err != nil {
		slog.Debug("write an inline user image to a file", "path", path, "err", err)
		return ""
	}
	return path
}

// inlineImageDir is where the images user messages carried inline get their
// files.
func inlineImageDir() string {
	return filepath.Join(os.TempDir(), "forebrain-images")
}

func writeImageFile(path, data string) error {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".image-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(raw)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
	return werr
}

func imageExtension(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}
