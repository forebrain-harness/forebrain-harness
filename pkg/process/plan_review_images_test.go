package process

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// A 1x1 PNG: enough bytes for the image readers to accept.
const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

// Every image the reviewer is sent comes with the file it is stored in, so it
// can look closer than the picture: a referenced file by its own path, an
// inline image by a file written once under the project's attachments
// directory. Both are persisted by reference, so the review's transcript does
// not store the bytes again.
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
	runner := &run.Runner{Deps: &run.Deps{ProjectRoot: project}, FileResolver: run.PathResolver{}}
	sources := []turn.RequestImage{{Message: 2, PartsJSON: string(inline)}, {Message: 5, PartsJSON: string(referenced)}}

	parts := planReviewImageParts(ctx, runner, sources)
	if len(parts) != 4 {
		t.Fatalf("parts=%d, want a label and an image per image", len(parts))
	}
	written := parts[1].SourcePath
	if filepath.Dir(written) != filepath.Join(project, ".forebrain", "attachments") {
		t.Fatalf("inline image written to %q, want the project's attachments directory", written)
	}
	if got, err := os.ReadFile(written); err != nil || string(got) != string(raw) {
		t.Fatalf("written file: err=%v, same bytes=%v", err, string(got) == string(raw))
	}
	if parts[0].Text != "Image 1 of message 2, stored at "+written+":" {
		t.Fatalf("label=%q", parts[0].Text)
	}
	if parts[3].SourcePath != attached || !strings.Contains(parts[2].Text, "of message 5, stored at "+attached) {
		t.Fatalf("referenced image: label=%q source=%q", parts[2].Text, parts[3].SourcePath)
	}

	// Every review of the conversation names the same file and writes it once.
	again := planReviewImageParts(ctx, runner, sources[:1])
	if again[1].SourcePath != written {
		t.Fatalf("second review wrote %q, want %q", again[1].SourcePath, written)
	}
	entries, err := os.ReadDir(filepath.Dir(written))
	if err != nil || len(entries) != 1 {
		t.Fatalf("attachments dir holds %d entries (err=%v), want the one file", len(entries), err)
	}

	if got := planReviewImageParts(ctx, runner, nil); got != nil {
		t.Fatalf("no sources must hand over nothing, got %d parts", len(got))
	}
}
