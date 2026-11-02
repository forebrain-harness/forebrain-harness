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

// EffectiveInputWindow is the single denominator the compaction threshold and
// the context-pressure warning share, so its three sources are pinned here:
// the model's input limit wins, then its context window, then the configured
// window, and only then the built-in default.
func TestEffectiveInputWindowSources(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limits   llm.Limits
		fallback int
		want     int
	}{
		{name: "input limit", limits: llm.Limits{Context: 200000, Input: 180000}, fallback: 128000, want: 180000},
		{name: "context window", limits: llm.Limits{Context: 200000}, fallback: 128000, want: 200000},
		{name: "configured window", limits: llm.Limits{}, fallback: 128000, want: 128000},
		{name: "built-in default", limits: llm.Limits{}, want: 200000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveInputWindow(tc.limits, tc.fallback); got != tc.want {
				t.Fatalf("window = %d, want %d", got, tc.want)
			}
		})
	}
}

// The threshold is a percentage of that window. The configured percent is
// clamped to the range the setting accepts, and an unset one selects the
// default.
func TestAutoCompactThresholdIsAPercentOfTheWindow(t *testing.T) {
	limits := llm.Hit{ContextWindow: 1000}
	for _, tc := range []struct {
		percent int
		want    int
	}{
		{percent: 50, want: 500},
		{percent: 90, want: 900},
		{percent: 95, want: 900},
		{percent: 30, want: 500},
		{percent: 0, want: 900},
	} {
		got := CalculateTokenBudgetWithOptions(0, "test-model", limits, TokenBudgetOptions{AutoCompactPercent: tc.percent})
		if got.AutoCompactThreshold != tc.want {
			t.Fatalf("percent %d: threshold = %d, want %d", tc.percent, got.AutoCompactThreshold, tc.want)
		}
		if got.EffectiveContextWindow != 1000 {
			t.Fatalf("percent %d: effective window = %d, want 1000", tc.percent, got.EffectiveContextWindow)
		}
	}
}

// A model the catalog does not know is measured against the configured context
// window rather than a hardcoded 200000 — the same number hook.go warns on.
func TestUncataloguedModelUsesTheConfiguredWindow(t *testing.T) {
	got := CalculateTokenBudgetWithOptions(0, "uncatalogued", llm.Hit{}, TokenBudgetOptions{
		AutoCompactPercent:    50,
		FallbackContextTokens: 128000,
	})
	if got.EffectiveContextWindow != 128000 || got.ContextWindow != 128000 {
		t.Fatalf("window = %d/%d, want the configured 128000", got.ContextWindow, got.EffectiveContextWindow)
	}
	if got.AutoCompactThreshold != 64000 {
		t.Fatalf("threshold = %d, want 64000", got.AutoCompactThreshold)
	}
}
