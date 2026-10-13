package state

import (
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// fixedEstimator counts one token per byte, so a test can hand-compute the
// occupancy it should produce and see that the estimator actually passed in
// decided the number (not the process-wide default).
type fixedEstimator struct{}

func (fixedEstimator) Count(text string) int { return len(text) }
func (fixedEstimator) Name() string          { return "fixed" }

// TokenCountWithEstimationFor counts with the estimator it is given; the
// no-argument TokenCountWithEstimation stays on the default. Both matter: the
// occupancy the compaction card reports and the threshold it compares against
// must come from the session model's own tokenizer.
func TestTokenCountWithEstimationForUsesGivenEstimator(t *testing.T) {
	turns := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "world!"},
	}
	got := TokenCountWithEstimationFor(turns, fixedEstimator{})
	// Each row projects to one text message: byte length + 4 framing tokens.
	want := (len("hello") + 4) + (len("world!") + 4)
	if got != want {
		t.Fatalf("TokenCountWithEstimationFor=%d want %d", got, want)
	}
	if def := TokenCountWithEstimation(turns); def == got {
		t.Fatalf("the given estimator did not change the count: both %d", def)
	}
}

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

// importedUsage is the usage row a Claude Code migration writes: it measured
// Claude Code's own request — its system prompt, tools, preserved thinking
// blocks and tokenizer — none of which forebrain's next request carries.
const importedUsage = `{"input_tokens":2,"cache_read_input_tokens":898076,"cache_creation_input_tokens":608,"output_tokens":1403}`

// A row with no run was imported from another harness; its usage must not
// anchor forebrain's occupancy. Estimating it like a plain row is what keeps a
// migrated session from compacting on its first message.
func TestImportedUsageIsNotAnAnchor(t *testing.T) {
	turns := []Message{
		{Role: "user", Content: "migrated question"},
		{Role: "assistant", Content: "imported answer", UsageJSON: importedUsage},
		{Role: "user", Content: "continue"},
	}
	if got := TokenCountFromLastAPIResponse(turns); got != 0 {
		t.Fatalf("imported usage anchored the gauge: TokenCountFromLastAPIResponse=%d want 0", got)
	}
	withoutUsage := []Message{
		{Role: "user", Content: "migrated question"},
		{Role: "assistant", Content: "imported answer"},
		{Role: "user", Content: "continue"},
	}
	if got, want := TokenCountWithEstimation(turns), estimateTurnsWith(llm.DefaultEstimator(), withoutUsage); got != want {
		t.Fatalf("imported row was not estimated like a plain row: got %d want %d", got, want)
	}
	if got := TokenCountWithEstimation(turns); got >= 900089 {
		t.Fatalf("imported usage leaked into the estimate: %d", got)
	}
}

// A native usage row after imported ones anchors again: the criterion is
// whether forebrain sent the request, not whether the model is the same.
func TestNativeUsageAnchorsAfterImportedRows(t *testing.T) {
	turns := []Message{
		{Role: "user", Content: "migrated question"},
		{Role: "assistant", Content: "imported answer", UsageJSON: importedUsage},
		{Role: "user", Content: "continue"},
		{Role: "assistant", Content: "native answer", RunID: "run-1", UsageJSON: `{"input_tokens":5000,"output_tokens":100}`},
	}
	if got := TokenCountFromLastAPIResponse(turns); got != 5100 {
		t.Fatalf("native row after an imported one must anchor: got %d want 5100", got)
	}
	if got := TokenCountWithEstimation(turns); got < 5100 {
		t.Fatalf("native anchor lost to the imported row: TokenCountWithEstimation=%d want >= 5100", got)
	}
}

// BodyAfterPrefixTokenCount reads the first server-observed request input as
// the absolute prefix. An imported row's input measured another harness's
// prefix, so the baseline must come from the first forebrain-sent request.
func TestBodyAfterPrefixIgnoresImportedBaseline(t *testing.T) {
	turns := []Message{
		{Role: "assistant", Content: "imported", UsageJSON: `{"input_tokens":890000,"output_tokens":10}`},
		{Role: "assistant", Content: "native", RunID: "run-1", UsageJSON: `{"input_tokens":100,"output_tokens":25}`},
		{Role: "user", Content: stringsOfLen(80)},
	}
	// Active usage is the native 125 plus the estimated 24-token suffix; the
	// 100-token first forebrain request input is the window prefix, leaving 49.
	if got := BodyAfterPrefixTokenCount(turns, 9999); got != 49 {
		t.Fatalf("body tokens=%d want 49", got)
	}
}
