package gateway

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

// headerOf runs one tool event payload through the enrichment and reads back
// the header the page will draw.
func headerOf(t *testing.T, root, eventType string, payload map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var out struct {
		Header string `json:"header"`
	}
	require.NoError(t, json.Unmarshal(toolEventPayloadWithHeader(root, eventType, raw), &out))
	return out.Header
}

// TestToolHeadersSayWhatTheTerminalSays pins the web's card header to the
// terminal's wording (tool.DisplayHeader): only a shell call is "Ran ...", every
// other tool names what it did in its own words, and a path inside the
// conversation's directory is relative while one outside it stays absolute.
func TestToolHeadersSayWhatTheTerminalSays(t *testing.T) {
	root := "/work/proj"
	read := func(path string) map[string]any {
		return map[string]any{
			"tool_name": "read_file",
			"summary":   "ran read " + path + " · lines 1-96",
			"tool_meta": map[string]any{
				"tool_name": "read_file", "status": "completed",
				"input": map[string]any{"file_path": path}, "result_lines": 96,
			},
			"duration_seconds": 0.01,
		}
	}

	// A file in the project reads relative to it, with the lines it showed.
	require.Equal(t, "Read plans/100.md lines 1-96 · <0.1s",
		headerOf(t, root, event.RunEventToolCompleted, read("/work/proj/plans/100.md")))
	// One outside the project keeps its absolute path.
	require.Equal(t, "Read /etc/hosts lines 1-96 · <0.1s",
		headerOf(t, root, event.RunEventToolCompleted, read("/etc/hosts")))

	// "Ran" belongs to the shell alone.
	require.Equal(t, "Ran ls plans/ 1.2s", headerOf(t, root, event.RunEventToolCompleted, map[string]any{
		"tool_name": "shell", "summary": "ran ls plans/",
		"tool_meta":        map[string]any{"tool_name": "shell", "status": "completed", "input": map[string]any{"command": "ls plans/"}},
		"duration_seconds": 1.2,
	}))
	require.Equal(t, "Running ls plans/", headerOf(t, root, event.RunEventToolStarted, map[string]any{
		"tool_name": "shell",
		"tool_meta": map[string]any{"tool_name": "shell", "status": "running", "input": map[string]any{"command": "ls plans/"}},
	}))
	require.Equal(t, "Notes", headerOf(t, root, event.RunEventToolCompleted, map[string]any{
		"tool_name": "intermediate_tool", "summary": "saved intermediate notes",
		"tool_meta": map[string]any{"tool_name": "intermediate_tool", "status": "completed", "input": map[string]any{"action": ""}},
	}))
	require.Equal(t, "Edited main.go", headerOf(t, root, event.RunEventToolCompleted, map[string]any{
		"tool_name": "edit_file",
		"tool_meta": map[string]any{"tool_name": "edit_file", "status": "completed", "input": map[string]any{"file_path": "/work/proj/main.go"}},
	}))
	require.Equal(t, "Fetched https://example.com", headerOf(t, root, event.RunEventToolCompleted, map[string]any{
		"tool_name": "web_fetch",
		"tool_meta": map[string]any{"tool_name": "web_fetch", "status": "completed", "input": map[string]any{"url": "https://example.com"}},
	}))
}

func TestToolEventPayloadLeavesOtherEventsAlone(t *testing.T) {
	raw := json.RawMessage(`{"text":"hello"}`)
	require.Equal(t, string(raw), string(toolEventPayloadWithHeader("/p", event.RunEventTurnError, raw)))
	require.Equal(t, "not json", string(toolEventPayloadWithHeader("/p", event.RunEventToolStarted, json.RawMessage("not json"))))
}

// TestStoredToolRowsAndCanceledCallsGetTheSameWording: a reloaded tool row is
// worded from what it kept, and a call the run never answered reads "Canceled
// ..." — never "denied", nobody was asked.
func TestStoredToolRowsAndCanceledCallsGetTheSameWording(t *testing.T) {
	parts := `[{"text":"{}","type":"text"},{"type":"tool_display","body":"x","summary":"ran read /work/proj/a.md · lines 1-9","tool_meta_json":"{\"tool_name\":\"read_file\",\"status\":\"completed\",\"input\":{\"file_path\":\"/work/proj/a.md\"},\"result_lines\":9}"}]`
	require.Equal(t, "Read a.md lines 1-9 · 2.0s", storedToolRowHeader("/work/proj", parts, "", 2*time.Second))
	require.Equal(t, "", storedToolRowHeader("/work/proj", `[{"text":"x","type":"text"}]`, "", 0), "a row with no tool facts has no header of its own")

	assistant := `[{"type":"tool_calls","tool_calls":[{"id":"c1","type":"function","function":{"name":"shell","arguments":"{\"command\":\"go test ./...\"}"}},{"id":"c2","type":"function","function":{"name":"read_file","arguments":"{\"file_path\":\"/work/proj/x.go\"}"}}]}]`
	got := canceledCallHeaders("/work/proj", assistant)
	require.Equal(t, "Canceled go test ./...", got["c1"])
	require.Equal(t, "Canceled x.go", got["c2"])
	require.Nil(t, canceledCallHeaders("/work/proj", `[{"text":"hi","type":"text"}]`))
}
