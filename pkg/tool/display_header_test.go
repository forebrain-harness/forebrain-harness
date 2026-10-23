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

	read.Duration = 40 * time.Millisecond
	require.Equal(t, "Read /etc/hosts lines 1-96 · <0.1s", header(read))

	shell := frame("shell", ToolMeta{Status: "completed", Input: map[string]any{"command": "ls plans/"}})
	require.Equal(t, "Ran ls plans/", header(shell))
	shell.Meta.Status = "running"
	require.Equal(t, "Running ls plans/", header(shell))

	require.Equal(t, "Notes", header(frame("intermediate_tool", ToolMeta{Status: "completed"})))
	require.Equal(t, "Exited plan mode", header(frame("exit_plan_mode", ToolMeta{Status: "completed"})))
	require.Equal(t, "Fetched https://example.com", header(frame("web_fetch", ToolMeta{Status: "completed", Input: map[string]any{"url": "https://example.com"}})))

	canceled := frame("shell", ToolMeta{Status: "canceled", Input: map[string]any{"command": "go test ./..."}})
	require.Equal(t, "Canceled go test ./...", header(canceled))
	denied := frame("shell", ToolMeta{Status: "denied", Input: map[string]any{"command": "rm -rf x"}})
	require.Equal(t, "Denied rm -rf x", header(denied))
}
