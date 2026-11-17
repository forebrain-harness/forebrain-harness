package tool

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDisplayHeaderIsTheTerminalsWording pins the words every surface draws on a
// tool card: only a shell call is "Ran", every other tool names what it did, a
// path inside the working directory is shown relative to it and one outside it
// stays absolute, and a call that never ran is "Canceled" — never "Denied".
func TestDisplayHeaderIsTheTerminalsWording(t *testing.T) {
	cwd := "/work/proj"
	frame := func(title string, meta ToolMeta) DisplayFrame {
		meta.ToolName = title
		return DisplayFrame{Title: title, Meta: meta, Final: true}
	}
	header := func(f DisplayFrame) string { return DisplayHeader(f, cwd) }

	read := frame("read_file", ToolMeta{Status: "completed", Input: map[string]any{"file_path": "/work/proj/a/b.go"}, ResultLines: 96})
	require.Equal(t, "Read a/b.go lines 1-96", header(read))
	read.Meta.Input = map[string]any{"file_path": "/etc/hosts"}
	require.Equal(t, "Read /etc/hosts lines 1-96", header(read))

	// A call under 100ms has no time worth reading: the header says nothing
	// about it rather than "<0.1s".
	read.Duration = 40 * time.Millisecond
	require.Equal(t, "Read /etc/hosts lines 1-96", header(read))
	read.Duration = 1200 * time.Millisecond
	require.Equal(t, "Read /etc/hosts lines 1-96 1.2s", header(read))
	// The time itself is still a fact where one is stated for the model.
	require.Equal(t, "<0.1s", FormatDuration(40*time.Millisecond))
	require.Equal(t, "", FormatCardDuration(40*time.Millisecond))

	shell := frame("shell", ToolMeta{Status: "completed", Input: map[string]any{"command": "ls plans/"}})
	require.Equal(t, "Ran ls plans/", header(shell))
	shell.Meta.Status = "running"
	require.Equal(t, "Running ls plans/", header(shell))

	// The notes tool has three operations, and the head says which one ran: a
	// settled card is never a bare noun. A call that named no operation (or one
	// this build has no words for) is the tool's own default, append.
	require.Equal(t, "Saved notes", header(frame("intermediate_tool", ToolMeta{Status: "completed"})))
	require.Equal(t, "Saved notes", header(frame("intermediate_tool", ToolMeta{Status: "completed", Input: map[string]any{"action": ""}})))
	require.Equal(t, "Saved notes", header(frame("intermediate_tool", ToolMeta{Status: "completed", Input: map[string]any{"action": "append"}})))
	require.Equal(t, "Read notes", header(frame("intermediate_tool", ToolMeta{Status: "completed", Input: map[string]any{"action": "read"}})))
	require.Equal(t, "Cleared notes", header(frame("intermediate_tool", ToolMeta{Status: "completed", Input: map[string]any{"action": "clear"}})))
	require.Equal(t, "Saved notes", header(frame("intermediate_tool", ToolMeta{Status: "completed", Input: map[string]any{"action": "publish"}})))
	require.Equal(t, "Saving notes", header(frame("intermediate_tool", ToolMeta{Status: "running", Input: map[string]any{"action": "append"}})))
	require.Equal(t, "Reading notes", header(frame("intermediate_tool", ToolMeta{Status: "running", Input: map[string]any{"action": "read"}})))
	require.Equal(t, "Clearing notes", header(frame("intermediate_tool", ToolMeta{Status: "running", Input: map[string]any{"action": "clear"}})))
	require.Equal(t, "Failed to save notes", header(frame("intermediate_tool", ToolMeta{Status: "failed"})))
	require.Equal(t, "Failed to read notes", header(frame("intermediate_tool", ToolMeta{Status: "failed", Input: map[string]any{"action": "read"}})))
	require.Equal(t, "Failed to clear notes", header(frame("intermediate_tool", ToolMeta{Status: "failed", Input: map[string]any{"action": "clear"}})))
	require.Equal(t, "Exited plan mode", header(frame("exit_plan_mode", ToolMeta{Status: "completed"})))
	require.Equal(t, "Fetched https://example.com", header(frame("web_fetch", ToolMeta{Status: "completed", Input: map[string]any{"url": "https://example.com"}})))

	canceled := frame("shell", ToolMeta{Status: "canceled", Input: map[string]any{"command": "go test ./..."}})
	require.Equal(t, "Canceled go test ./...", header(canceled))
	denied := frame("shell", ToolMeta{Status: "denied", Input: map[string]any{"command": "rm -rf x"}})
	require.Equal(t, "Denied rm -rf x", header(denied))
}
