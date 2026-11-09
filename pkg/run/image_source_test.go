package run

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

const sourceTestPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="

// An image a user message carried inline (a row from before attachments were
// kept by reference, an imported conversation) still gets a file the model
// can open: written from its bytes, named by them, so every replay names the
// same file. The part itself is not pointed at that file — it would then be
// persisted by reference to a temporary file instead of its bytes.
func TestInlineUserImageIsFollowedByAFileWrittenFromItsBytes(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	msg := llm.UserMessage(llm.Text("like this"), llm.ImageBase64("image/png", sourceTestPNG))

	first := rehydrateMessageImages(context.Background(), PathResolver{}, msg, nil)
	if len(first.Parts) != 3 || first.Parts[1].Type != llm.ContentTypeImageBase64 {
		t.Fatalf("parts = %#v", first.Parts)
	}
	if first.Parts[1].SourcePath != "" {
		t.Fatalf("the inline part was pointed at %q", first.Parts[1].SourcePath)
	}
	note := first.Parts[2].Text
	path := strings.TrimSuffix(strings.TrimPrefix(note, "[Image: source: "), "]")
	if filepath.Dir(path) != filepath.Join(os.TempDir(), "forebrain-images") || !strings.HasSuffix(path, ".png") {
		t.Fatalf("source line %q", note)
	}
	raw, _ := base64.StdEncoding.DecodeString(sourceTestPNG)
	if got, err := os.ReadFile(path); err != nil || string(got) != string(raw) {
		t.Fatalf("file holds the image's bytes: err=%v", err)
	}

	// A replay after the system cleaned the temporary directory names the
	// same file and writes it again.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	again := rehydrateMessageImages(context.Background(), PathResolver{}, msg, nil)
	if again.Parts[2].Text != note {
		t.Fatalf("replay named %q, first named %q", again.Parts[2].Text, note)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file was not written again: %v", err)
	}
}

// Only what a user sent gets the line: a tool's image is the tool's output,
// already named by the call that read it. A web image is named by its URL.
func TestImageSourceLinesAreForUserImagesOnly(t *testing.T) {
	img := filepath.Join(t.TempDir(), "shot.png")
	raw, _ := base64.StdEncoding.DecodeString(sourceTestPNG)
	if err := os.WriteFile(img, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	refs := []state.FileRefInfo{{FileID: img, Label: "shot.png", MIMEType: "image/png"}}
	toolRow := llm.Message{Role: llm.RoleTool, Parts: []llm.ContentPart{llm.Text("[attachment] shot.png (image/png)")}}
	got := rehydrateMessageImages(context.Background(), PathResolver{}, toolRow, refs)
	if len(got.Parts) != 1 || got.Parts[0].Type != llm.ContentTypeImageBase64 {
		t.Fatalf("tool row parts = %#v", got.Parts)
	}

	web := llm.UserMessage(llm.ImageURL("https://example.com/a.png"))
	got = rehydrateMessageImages(context.Background(), PathResolver{}, web, nil)
	if len(got.Parts) != 2 || got.Parts[1].Text != "[Image: source: https://example.com/a.png]" {
		t.Fatalf("web image parts = %#v", got.Parts)
	}
}

// A message the run itself persists — a steer, a message to a subagent —
// holds the image's source line while it is sent. The row keeps the image
// only, and the replay derives the line again, so the transcript shows no
// path line and the next request sends exactly what this one did.
func TestImageSourceLineIsSentNotStored(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	if err := store.Ensure(ctx, "s1", "s1"); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(t.TempDir(), "shot.png")
	raw, _ := base64.StdEncoding.DecodeString(sourceTestPNG)
	if err := os.WriteFile(img, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	image, err := llm.ImageFile(img)
	if err != nil {
		t.Fatal(err)
	}
	live := []llm.ContentPart{llm.Text("look at this"), image, llm.Text(llm.ImageSourceNote(img))}
	if err := store.AppendNewMessages(ctx, "s1", "", []llm.Message{llm.UserMessage(live...)}, "", ""); err != nil {
		t.Fatal(err)
	}

	stored, err := store.ListVisibleUserMessages(ctx, "s1", 1)
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored: %v %v", stored, err)
	}
	if strings.Contains(stored[0].Content, "[Image: source:") || strings.Contains(stored[0].PartsJSON, "[Image: source:") {
		t.Fatalf("the source line was stored: content=%q parts=%s", stored[0].Content, stored[0].PartsJSON)
	}

	entries, err := store.ListTranscriptMessagesWithRefs(ctx, "s1", 10)
	if err != nil {
		t.Fatal(err)
	}
	replayed := rehydrateImageReferences(ctx, PathResolver{}, entries)
	if len(replayed) != 1 || len(replayed[0].Parts) != 3 || replayed[0].Parts[2].Text != live[2].Text ||
		replayed[0].Parts[1].ImageBase64 != image.ImageBase64 {
		t.Fatalf("replay = %#v, want what was sent", replayed)
	}

	// A message that already carries the line is not given a second one.
	if again := withImageSourceNotes(live); len(again) != 3 {
		t.Fatalf("parts = %#v, want the line once", again)
	}

	// And the run writing its message list back recognises the stored turn
	// as the one it sent: no second copy of the message.
	if err := store.AppendMessageSequence(ctx, "s1", []llm.Message{
		llm.UserMessage(live...),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("seen")}),
	}, "", ""); err != nil {
		t.Fatal(err)
	}
	users, err := store.ListVisibleUserMessages(ctx, "s1", 0)
	if err != nil || len(users) != 1 {
		t.Fatalf("user rows = %d (err=%v), want the one", len(users), err)
	}
}

// Steers merged into the one message a paused or interrupted subagent resumes
// with keep everything each of them carried: the words as the user wrote them
// — not the queue's one-line preview — and every image with the line naming
// its file. The merge used to keep the words only.
func TestMergedSteersKeepTheirImagesAndTheirFiles(t *testing.T) {
	first := Input{
		Text:  "look at [Image #1]",
		Parts: []llm.ContentPart{llm.Text("look at\nthis"), llm.ImageBase64("image/png", sourceTestPNG), llm.Text(llm.ImageSourceNote("/tmp/a.png"))},
	}
	second := Input{Text: "and fix it"}
	imageOnly := Input{
		Text:  "[Image #1]",
		Parts: []llm.ContentPart{llm.ImageBase64("image/png", sourceTestPNG), llm.Text(llm.ImageSourceNote("/tmp/b.png"))},
	}

	merged, _ := mergeSteers([]Input{first, second, imageOnly})
	if merged.Text != "look at\nthis\n\nand fix it\n\n[Image #1]" {
		t.Fatalf("text = %q", merged.Text)
	}
	want := []llm.ContentPart{
		llm.Text(merged.Text),
		first.Parts[1], first.Parts[2],
		imageOnly.Parts[0], imageOnly.Parts[1],
	}
	if len(merged.Parts) != len(want) {
		t.Fatalf("parts = %#v", merged.Parts)
	}
	for i := range want {
		if merged.Parts[i] != want[i] {
			t.Fatalf("part %d = %#v, want %#v", i, merged.Parts[i], want[i])
		}
	}
}
