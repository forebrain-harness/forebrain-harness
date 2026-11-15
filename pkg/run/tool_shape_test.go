package run

import (
	"context"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// TestToolCompletedPayloadPrefersTheCapturedShape pins the one seam that
// decides what a finished call's output looks like: a tool that captured a
// structured result (present_files' rows) must hand that map on untouched,
// while a tool that captured nothing keeps the text envelope. Wrapping the
// captured map in {"output": <json>} is exactly the bug that left the
// deliverable card with no rows to draw.
func TestToolCompletedPayloadPrefersTheCapturedShape(t *testing.T) {
	ctx := withToolStepCapture(context.Background(), "call_1")
	tool.CaptureToolOutput(ctx, map[string]any{"presented": []any{map[string]any{"name": "a.md"}}})
	got, _, _, _ := toolCompletedPayload(ctx, `{"presented":[]}`, nil, nil, nil)
	if _, wrapped := got["output"]; wrapped {
		t.Fatalf("captured shape still wrapped: %#v", got)
	}
	if _, ok := got["presented"].([]any); !ok {
		t.Fatalf("captured shape lost presented: %#v", got)
	}
	plain, _, _, _ := toolCompletedPayload(context.Background(), `{"presented":[]}`, nil, nil, nil)
	if _, ok := plain["output"].(string); !ok {
		t.Fatalf("no-capture shape must keep the envelope: %#v", plain)
	}
}
