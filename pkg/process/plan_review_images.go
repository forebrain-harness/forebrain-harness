package process

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// planReviewImageParts reads the images of the messages a review request
// names back the way the conversation's history reads them — inline, or from
// the file a reference points at — and labels each with its message and the
// file it is stored in, in request order. The path is what lets the reviewer
// look closer than the picture it is sent: crop or enlarge it with a script to
// read small text. An image the message carried inline is written once to the
// project's attachments directory, where the terminal keeps pasted images, so
// it has a path too; and an image that knows its file is persisted by
// reference, so the review's transcript does not store the bytes again. A
// reference whose file is gone carries no image and adds nothing, as in
// history.
func planReviewImageParts(ctx context.Context, runner *run.Runner, sources []turn.RequestImage) []llm.ContentPart {
	if runner == nil || len(sources) == 0 {
		return nil
	}
	var out []llm.ContentPart
	for _, source := range sources {
		n := 0
		for _, part := range run.UserInputParts(ctx, runner.FileResolver, source.PartsJSON) {
			if part.Type != llm.ContentTypeImageBase64 && part.Type != llm.ContentTypeImageURL {
				continue
			}
			n++
			where := strings.TrimSpace(part.SourcePath)
			if where == "" {
				path, err := writeReviewImage(planReviewAttachmentsDir(runner), part)
				switch {
				case err == nil && path != "":
					where = path
					part.SourcePath = path
				case part.Type == llm.ContentTypeImageURL && !strings.HasPrefix(part.ImageURL, "data:"):
					where = part.ImageURL
				default:
					slog.Debug("plan review: keep an inline image without a file", "err", err)
				}
			}
			label := fmt.Sprintf("Image %d of message %d", n, source.Message)
			if where != "" {
				label += ", stored at " + where
			}
			out = append(out, llm.Text(label+":"), part)
		}
	}
	return out
}

// planReviewAttachmentsDir is where an inline image gets its file: the
// project's .forebrain/attachments, as for an image pasted in the terminal,
// or the temporary directory when there is no project.
func planReviewAttachmentsDir(runner *run.Runner) string {
	if runner.Deps == nil {
		return filepath.Join(os.TempDir(), "forebrain-attachments")
	}
	if root := strings.TrimSpace(runner.ProjectRoot); root != "" {
		return filepath.Join(root, ".forebrain", "attachments")
	}
	return filepath.Join(os.TempDir(), "forebrain-attachments")
}

// writeReviewImage stores an inline image's bytes under a name derived from
// them, so every review of the conversation names the same file and none
// writes it twice. It returns "" for a part with no bytes of its own (an
// http image URL).
func writeReviewImage(dir string, part llm.ContentPart) (string, error) {
	mime, data := strings.TrimSpace(part.MIMEType), strings.TrimSpace(part.ImageBase64)
	if part.Type == llm.ContentTypeImageURL {
		header, payload, ok := strings.Cut(strings.TrimSpace(part.ImageURL), ",")
		if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") {
			return "", nil
		}
		mime = strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
		data = payload
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", err
	}
	if len(raw) == 0 {
		return "", nil
	}
	sum := sha256.Sum256(raw)
	path := filepath.Join(dir, "forebrain-review-"+hex.EncodeToString(sum[:8])+imageExtension(mime))
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".forebrain-review-*")
	if err != nil {
		return "", err
	}
	_, werr := tmp.Write(raw)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return "", werr
	}
	return path, nil
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
