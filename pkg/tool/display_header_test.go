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

// TestStoppedCardSaysNothing pins the second of the three card rules: a call a
// stop settled as canceled whose head names no subject and whose body carries
// no output of its own would repeat the surface's "You stopped this run" line
// and nothing else, so no surface draws it. A card that names its call
// ("Canceled sleep 30") or carries real output stays.
func TestStoppedCardSaysNothing(t *testing.T) {
	frame := func(title string, meta ToolMeta, content string) DisplayFrame {
		meta.ToolName = title
		return DisplayFrame{Title: title, Meta: meta, Content: content, Final: true, Duration: 1200 * time.Millisecond}
	}

	// user_interaction's head carries the verb alone; a canceled ask has no
	// body, so the card would say only what the run's stop line already said.
	require.True(t, StoppedCardSaysNothing(frame("user_interaction", ToolMeta{Status: "canceled"}, "")))
	require.True(t, StoppedCardSaysNothing(frame("session_todo", ToolMeta{Status: "canceled"}, "   ")))

	// The generic stop sentence is about the stop, not about this call: a head
	// that names no subject carrying only it still says nothing.
	require.True(t, StoppedCardSaysNothing(frame("user_interaction", ToolMeta{Status: "canceled"}, StoppedCallDisplayBody)))

	// A card that names its call keeps standing, with or without a body.
	require.False(t, StoppedCardSaysNothing(frame("shell", ToolMeta{Status: "canceled", Input: map[string]any{"command": "sleep 30"}}, StoppedCallDisplayBody)))
	require.False(t, StoppedCardSaysNothing(frame("shell", ToolMeta{Status: "canceled", Input: map[string]any{"command": "sleep 30"}}, "")))

	// Real output is content, whatever the head says.
	require.False(t, StoppedCardSaysNothing(frame("user_interaction", ToolMeta{Status: "canceled"}, "Which database?")))

	// Only a canceled call is this rule's business: a completed or failed ask
	// is a record of what the user was asked, not a repeat of the stop.
	require.False(t, StoppedCardSaysNothing(frame("user_interaction", ToolMeta{Status: "completed"}, "")))
	require.False(t, StoppedCardSaysNothing(frame("user_interaction", ToolMeta{Status: "failed"}, "")))
	require.False(t, StoppedCardSaysNothing(frame("user_interaction", ToolMeta{Status: "awaiting approval"}, "")))
}

// TestDisplayPartsNamesTheProcessCommandAndID pins the second of the card
// rules for the process tool: the head names what the call acts on — the
// command a start runs, the id a wait/log/kill drives, the registry a list
// reads — instead of a head that named only the tool and left the user
// watching a timer for minutes.
func TestDisplayPartsNamesTheProcessCommandAndID(t *testing.T) {
	startRunning := DisplayFrame{Title: "process", Meta: ToolMeta{Status: "running", Input: map[string]any{
		"action": "start", "command": "go test ./pkg/tool", "wait": "log",
	}}}
	action, target, suffix := DisplayParts(startRunning, t.TempDir())
	require.Equal(t, "Starting background process", action)
	require.Equal(t, "go test ./pkg/tool", target)
	require.Equal(t, "wait log", suffix)

	startDone := DisplayFrame{Title: "process", Final: true, Meta: ToolMeta{Status: "completed", Input: map[string]any{
		"action": "start", "command": "go test ./pkg/tool",
	}}}
	action, target, _ = DisplayParts(startDone, t.TempDir())
	require.Equal(t, "Started background process", action)
	require.Equal(t, "go test ./pkg/tool", target)

	waitRunning := DisplayFrame{Title: "process", Meta: ToolMeta{Status: "running", Input: map[string]any{
		"action": "wait", "id": float64(7),
	}}}
	action, target, _ = DisplayParts(waitRunning, t.TempDir())
	require.Equal(t, "Waiting for", action)
	require.Equal(t, "process #7", target)

	killDone := DisplayFrame{Title: "process", Final: true, Meta: ToolMeta{Status: "completed", Input: map[string]any{
		"action": "kill", "id": float64(7),
	}}}
	action, target, _ = DisplayParts(killDone, t.TempDir())
	require.Equal(t, "Killed", action)
	require.Equal(t, "process #7", target)

	listRunning := DisplayFrame{Title: "process", Meta: ToolMeta{Status: "running", Input: map[string]any{
		"action": "list",
	}}}
	action, target, _ = DisplayParts(listRunning, t.TempDir())
	require.Equal(t, "Listing processes", action)
	require.Equal(t, "", target)

	// A failed start words the failure; an action this build does not name keeps
	// the generic verb (never a guessed one).
	startFailed := DisplayFrame{Title: "process", Final: true, Meta: ToolMeta{Status: "failed", Input: map[string]any{
		"action": "start", "command": "go test ./pkg/tool",
	}}}
	action, target, _ = DisplayParts(startFailed, t.TempDir())
	require.Equal(t, "Failed to start background process", action)
	require.Equal(t, "go test ./pkg/tool", target)

	readLog := DisplayFrame{Title: "process", Final: true, Meta: ToolMeta{Status: "completed", Input: map[string]any{
		"action": "log", "id": float64(7),
	}}}
	action, target, _ = DisplayParts(readLog, t.TempDir())
	require.Equal(t, "Read log of", action)
	require.Equal(t, "process #7", target)

	unknown := DisplayFrame{Title: "process", Final: true, Meta: ToolMeta{Status: "completed", Input: map[string]any{
		"action": "teleport", "id": float64(7),
	}}}
	action, target, _ = DisplayParts(unknown, t.TempDir())
	require.Equal(t, "Called", action)
	require.Equal(t, "process #7", target)
}
