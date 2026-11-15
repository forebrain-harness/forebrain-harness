package tui

import (
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// The subagent run footer names the model the way the composer footer does:
// the vendor listing's display name, not the opaque subscription wire id
// (qodercn/gfmodel -> qodercn/GLM-5.3-Flash). A spawn that named no model
// names nothing rather than borrowing the conversation's.
func TestSubagentModelFooterLabelUsesDisplayName(t *testing.T) {
	llm.SetBuiltinModels("qodercn", []llm.Model{
		{ID: "gfmodel", Name: "GLM-5.3-Flash"},
		{ID: "noname"},
	})
	t.Cleanup(func() { llm.SetBuiltinModels("qodercn", nil) })
	for _, tc := range []struct {
		provider, model, want string
	}{
		{"qodercn", "gfmodel", "qodercn/GLM-5.3-Flash"},
		{"qodercn", "noname", "qodercn/noname"}, // wire id when the listing names none
		{"", "gfmodel", "gfmodel"},
		{"qodercn", "", ""},
		{"", "", ""},
	} {
		if got := subagentModelFooterLabel(tc.provider, tc.model); got != tc.want {
			t.Fatalf("subagentModelFooterLabel(%q, %q) = %q, want %q", tc.provider, tc.model, got, tc.want)
		}
	}
}

// The image-rejection notice names the refusing model with the vendor's
// display name — the same label the footer uses — not the opaque wire id.
func TestImageRejectedLineUsesDisplayName(t *testing.T) {
	llm.SetBuiltinModels("qodercn", []llm.Model{{ID: "gfmodel", Name: "GLM-5.3-Flash"}})
	t.Cleanup(func() { llm.SetBuiltinModels("qodercn", nil) })
	line := imageRejectedLine(event.ImageRejectedPayload{Provider: "qodercn", Model: "gfmodel", Reason: "unsupported image"})
	if !strings.Contains(line, "qodercn/GLM-5.3-Flash") || strings.Contains(line, "qodercn/gfmodel") {
		t.Fatalf("line = %q, want the display name not the wire id", line)
	}
	// A model the listing names none keeps the wire id.
	plain := imageRejectedLine(event.ImageRejectedPayload{Provider: "acme", Model: "vision-model"})
	if !strings.Contains(plain, "acme/vision-model") {
		t.Fatalf("line = %q, want the wire id when no display name", plain)
	}
}