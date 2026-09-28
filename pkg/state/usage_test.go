package state

import (
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// A tool row is persisted with a tool_display part that the request builder
// drops. Occupancy must reflect the request, not the stored row: charging the
// display block read the conversation several times larger than it is, which
// tripped the pre-turn compaction checkpoint long before the mid-turn one
// could ever be reached.
func TestEstimateMessageIgnoresDisplayOnlyParts(t *testing.T) {
	output := stringsOfLen(4000)
	msg := llm.ToolResultMessage("call-1", llm.Text(output))
	msg.ToolDisplay = &llm.ToolDisplayState{
		Body:         stringsOfLen(12000),
		Summary:      "ran a command",
		ToolMetaJSON: `{"name":"run_shell_command"}`,
	}
	row := Message{Role: "tool", Content: output, PartsJSON: MessagePartsJSON(msg, output)}
	if !strings.Contains(row.PartsJSON, PartTypeToolDisplay) {
		t.Fatalf("test row is missing the display part it is meant to exercise")
	}

	got := TokenCountWithEstimation([]Message{row})
	want := llm.EstimateMessages(messagesFromStoredRows([]storedMessage{{
		Role: row.Role, Content: row.Content, PartsJSON: row.PartsJSON,
	}}))
	if got != want {
		t.Fatalf("stored-row occupancy=%d, request occupancy=%d: the two compaction checkpoints must measure the same conversation", got, want)
	}
}

// A compact boundary is a marker row, never part of a request.
func TestEstimateMessageSkipsCompactBoundaryRow(t *testing.T) {
	row := Message{Role: "system", Content: "", PartsJSON: PartsJSONWithCompactPart("", EncodeCompactBoundaryPart(CompactBoundaryPart{
		WindowID: "b-1", WindowNumber: 1,
	}))}
	if got := TokenCountWithEstimation([]Message{row}); got != 0 {
		t.Fatalf("boundary row occupancy=%d, want 0", got)
	}
}
