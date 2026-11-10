package process

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// A 1x1 PNG: enough bytes for the image readers to accept.
const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

// Every image the reviewer is sent is labelled with its message, and the label
// is the key to where that image is stored — a referenced file by its own
// path, an inline image by a file written from its bytes (run.ImageSource).
// The file is named once, in the review's prompt, so the parts carry the
// picture and no line of their own.
func TestPlanReviewImagePartsLabelEveryImageWithItsFile(t *testing.T) {
	ctx := context.Background()
	project := t.TempDir()
	attached := filepath.Join(t.TempDir(), "shot.png")
	raw, err := base64.StdEncoding.DecodeString(onePixelPNG)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attached, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	inline, _ := json.Marshal([]map[string]any{
		{"type": "text", "text": "[Image #1] like this"},
		{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": onePixelPNG}},
	})
	referenced, _ := json.Marshal([]map[string]any{
		{"type": "text", "text": "and this"},
		{"type": "file_reference", "file_id": attached, "label": "shot.png", "mime_type": "image/png"},
	})
	t.Setenv("TMPDIR", t.TempDir())
	runner := &run.Runner{Deps: &run.Deps{ProjectRoot: project}, FileResolver: run.PathResolver{}}
	sources := []turn.RequestImage{{Message: 2, PartsJSON: string(inline)}, {Message: 5, PartsJSON: string(referenced)}}

	parts, filled := planReviewImageParts(ctx, runner, sources)
	if len(parts) != 4 {
		t.Fatalf("parts=%d, want a label and the image per image, and no line after it", len(parts))
	}
	for _, part := range parts {
		if llm.IsImageSourceNote(part) {
			t.Fatalf("the prompt names the file; the parts must not carry it again: %q", part.Text)
		}
	}
	written := run.ImageSource(parts[1])
	if filepath.Dir(written) != filepath.Join(os.TempDir(), "forebrain-images") {
		t.Fatalf("inline image named %q, want a file written from its bytes", written)
	}
	if got, err := os.ReadFile(written); err != nil || string(got) != string(raw) {
		t.Fatalf("written file: err=%v, same bytes=%v", err, string(got) == string(raw))
	}
	if parts[0].Text != "Image 1 of message 2:" || parts[2].Text != "Image 1 of message 5:" {
		t.Fatalf("labels: %q, %q", parts[0].Text, parts[2].Text)
	}
	if parts[3].SourcePath != attached {
		t.Fatalf("referenced image read from %q, want %q", parts[3].SourcePath, attached)
	}
	if len(filled) != 2 {
		t.Fatalf("sources=%d, want one per message", len(filled))
	}
	if len(filled[0].Files) != 1 || filled[0].Files[0] != written {
		t.Fatalf("inline image file = %v, want %q", filled[0].Files, written)
	}
	if len(filled[1].Files) != 1 || filled[1].Files[0] != attached {
		t.Fatalf("referenced image file = %v, want %q", filled[1].Files, attached)
	}

	// Every review of the conversation names the same file.
	if _, again := planReviewImageParts(ctx, runner, sources[:1]); len(again) != 1 || len(again[0].Files) != 1 || again[0].Files[0] != written {
		t.Fatalf("second review named %v, first %v", again, filled[0].Files)
	}

	if got, files := planReviewImageParts(ctx, runner, nil); got != nil || files != nil {
		t.Fatalf("no sources must hand over nothing, got %d parts and %d sources", len(got), len(files))
	}
}
