package process

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// A 1x1 PNG: enough bytes for the image readers to accept.
const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

// Every image the reviewer is sent comes with the file it is stored in, so it
// can look closer than the picture: a referenced file by its own path, an
// inline image by a file written from its bytes (run.ImageSource).
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

	parts := planReviewImageParts(ctx, runner, sources)
	if len(parts) != 6 {
		t.Fatalf("parts=%d, want a label, the image and its source line per image", len(parts))
	}
	written := run.ImageSource(parts[1])
	if filepath.Dir(written) != filepath.Join(os.TempDir(), "forebrain-images") {
		t.Fatalf("inline image named %q, want a file written from its bytes", written)
	}
	if got, err := os.ReadFile(written); err != nil || string(got) != string(raw) {
		t.Fatalf("written file: err=%v, same bytes=%v", err, string(got) == string(raw))
	}
	if parts[0].Text != "Image 1 of message 2:" || parts[2].Text != "[Image: source: "+written+"]" {
		t.Fatalf("inline image: label=%q source=%q", parts[0].Text, parts[2].Text)
	}
	if parts[3].Text != "Image 1 of message 5:" || parts[4].SourcePath != attached || parts[5].Text != "[Image: source: "+attached+"]" {
		t.Fatalf("referenced image: label=%q source=%q line=%q", parts[3].Text, parts[4].SourcePath, parts[5].Text)
	}

	// Every review of the conversation names the same file.
	if again := planReviewImageParts(ctx, runner, sources[:1]); again[2].Text != parts[2].Text {
		t.Fatalf("second review named %q, first %q", again[2].Text, parts[2].Text)
	}

	if got := planReviewImageParts(ctx, runner, nil); got != nil {
		t.Fatalf("no sources must hand over nothing, got %d parts", len(got))
	}
}
