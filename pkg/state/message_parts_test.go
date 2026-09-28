package state

import (
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func TestMessagePartsRoundTripPreservesToolDisplay(t *testing.T) {
	original := llm.ToolResultMessage("call-edit", llm.Text("ok"))
	original.ToolDisplay = &llm.ToolDisplayState{
		Body:         "turn diff: `internal/demo.go` (+1/-1)\n\n```diff\n-old\n+new\n```",
		Summary:      "edited internal/demo.go",
		ToolMetaJSON: `{"tool_name":"edit_file","status":"completed"}`,
	}
	partsJSON := MessagePartsJSON(original, original.TextContent())
	if !strings.Contains(partsJSON, PartTypeToolDisplay) {
		t.Fatalf("parts missing tool display: %s", partsJSON)
	}
	parsed, ok := ParseMessage(original.Role, original.TextContent(), partsJSON)
	if !ok || parsed.ToolDisplay == nil {
		t.Fatal("tool display did not survive persistence round trip")
	}
	if *parsed.ToolDisplay != *original.ToolDisplay {
		t.Fatalf("tool display=%+v want %+v", *parsed.ToolDisplay, *original.ToolDisplay)
	}
}

func TestMessagePartsRoundTripPreservesIsMeta(t *testing.T) {
	// A plan-mode reminder meta message
	original := llm.Message{
		Role:   llm.RoleUser,
		Parts:  []llm.ContentPart{llm.Text("<system-reminder>\nPLAN MODE ACTIVE\n</system-reminder>")},
		IsMeta: true,
	}
	json := MessagePartsJSON(original, original.TextContent())
	parsed, ok := ParseMessage(original.Role, original.TextContent(), json)
	if !ok {
		t.Fatalf("ParseMessage failed")
	}
	if parsed.Role != llm.RoleUser {
		t.Fatalf("role=%s want user", parsed.Role)
	}
	if !parsed.IsMeta {
		t.Fatalf("IsMeta not preserved through round-trip")
	}
	if parsed.TextContent() != original.TextContent() {
		t.Fatalf("content changed: %q vs %q", parsed.TextContent(), original.TextContent())
	}
}

func TestMessagePartsRoundTripNonMeta(t *testing.T) {
	original := llm.UserMessage(llm.Text("hello world"))
	json := MessagePartsJSON(original, original.TextContent())
	parsed, ok := ParseMessage(original.Role, original.TextContent(), json)
	if !ok {
		t.Fatalf("ParseMessage failed")
	}
	if parsed.IsMeta {
		t.Fatalf("IsMeta should be false for a non-meta message")
	}
	if parsed.TextContent() != original.TextContent() {
		t.Fatalf("content changed: %q vs %q", parsed.TextContent(), original.TextContent())
	}
}

// TestMessagePartsRoundTripPreservesImageBase64Data verifies that a
// ContentTypeImageBase64 part survives a full serialize -> parse round-trip
// with its pixel data intact. This is a regression test for a bug where
// contentBlocksJSON omitted the "data" field, causing the image to be stored
// with an empty base64 payload. On read-back the empty data produced an
// image_url of "data:image/png;base64," (no data), which providers like
// Volcengine Kimi reject with 400 InvalidParameter.
func TestMessagePartsRoundTripPreservesImageBase64Data(t *testing.T) {
	original := llm.UserMessage(
		llm.Text("Here is a screenshot"),
		llm.ImageBase64("image/png", "iVBORw0KGgoAAAANSUhEUg=="),
	)
	partsJSON := MessagePartsJSON(original, original.TextContent())
	parsed, ok := ParseMessage(original.Role, original.TextContent(), partsJSON)
	if !ok {
		t.Fatalf("ParseMessage failed")
	}
	if len(parsed.Parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parsed.Parts))
	}
	img, ok := findImageBase64Part(parsed.Parts)
	if !ok {
		t.Fatalf("no ContentTypeImageBase64 part found after round-trip; parts=%v", parsed.Parts)
	}
	if img.ImageBase64 != "iVBORw0KGgoAAAANSUhEUg==" {
		t.Fatalf("base64 data lost in round-trip: got %q, want %q", img.ImageBase64, "iVBORw0KGgoAAAANSUhEUg==")
	}
	if img.MIMEType != "image/png" {
		t.Fatalf("mime type changed: got %q, want %q", img.MIMEType, "image/png")
	}
}

func TestMessagePartsRoundTripPreservesMemoryCitation(t *testing.T) {
	original := llm.AssistantMessage([]llm.ContentPart{llm.Text("answer")})
	original.MemoryCitation = &llm.MemoryCitation{
		Entries:    []llm.MemoryCitationEntry{{Path: "MEMORY.md", LineStart: 2, LineEnd: 5, Note: "preferences"}},
		RolloutIDs: []string{"thread-1"},
	}
	partsJSON := MessagePartsJSON(original, original.TextContent())
	parsed, ok := ParseMessage(original.Role, original.TextContent(), partsJSON)
	if !ok || parsed.MemoryCitation == nil {
		t.Fatalf("memory citation missing after round trip: %s", partsJSON)
	}
	if got := parsed.MemoryCitation; len(got.Entries) != 1 || got.Entries[0].LineEnd != 5 ||
		len(got.RolloutIDs) != 1 || got.RolloutIDs[0] != "thread-1" {
		t.Fatalf("memory citation = %#v", got)
	}
}

func findImageBase64Part(parts []llm.ContentPart) (llm.ContentPart, bool) {
	for _, p := range parts {
		if p.Type == llm.ContentTypeImageBase64 {
			return p, true
		}
	}
	return llm.ContentPart{}, false
}
