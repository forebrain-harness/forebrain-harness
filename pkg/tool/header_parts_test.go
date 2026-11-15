package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDisplayHeaderPartsKeepTheDurationApart pins the four-part header the web
// lays out itself: the same words DisplayParts produces, with the duration held
// in its own field instead of folded into the suffix.
func TestDisplayHeaderPartsKeepTheDurationApart(t *testing.T) {
	cwd := "/work/proj"

	shellDone := DisplayFrame{
		Title: "shell", Meta: ToolMeta{
			ToolName: "shell", Status: "completed",
			Input: map[string]any{"command": "ls"},
		},
		Final: true, Duration: 1500 * time.Millisecond,
	}
	got := DisplayHeaderParts(shellDone, cwd)
	require.Equal(t, HeaderParts{Action: "Ran", Target: "ls", Duration: "1.5s"}, got)

	read := DisplayFrame{
		Title: "read_file", Meta: ToolMeta{
			ToolName: "read_file", Status: "completed",
			Input:       map[string]any{"file_path": "/work/proj/a.go"},
			ResultLines: 96,
		},
		Final: true, Duration: 300 * time.Millisecond,
	}
	got = DisplayHeaderParts(read, cwd)
	require.Equal(t, HeaderParts{Action: "Read", Target: "a.go", Suffix: "lines 1-96", Duration: "0.3s"}, got)

	running := DisplayFrame{
		Title: "shell", Meta: ToolMeta{
			ToolName: "shell", Status: "running",
			Input: map[string]any{"command": "go test ./..."},
		},
		Duration: 3 * time.Second,
	}
	got = DisplayHeaderParts(running, cwd)
	require.Equal(t, HeaderParts{Action: "Running", Target: "go test ./...", Duration: FormatWorkingElapsed(3 * time.Second)}, got)

	awaiting := DisplayFrame{
		Title: "shell", Meta: ToolMeta{
			ToolName: "shell", Status: "awaiting approval",
			Input: map[string]any{"command": "rm -rf x"},
		},
		Duration: 3 * time.Second,
	}
	got = DisplayHeaderParts(awaiting, cwd)
	require.Equal(t, "", got.Duration, "a card waiting on approval shows no duration")

	// The joined header keeps its exact wording: the third part is the suffix
	// and the duration, in that order, with " · " between them when both show.
	for _, f := range []DisplayFrame{shellDone, read, running, awaiting} {
		p := DisplayHeaderParts(f, cwd)
		_, _, suffix := DisplayParts(f, cwd)
		switch {
		case p.Suffix != "" && p.Duration != "":
			require.Equal(t, p.Suffix+" · "+p.Duration, suffix)
		case p.Duration != "":
			require.Equal(t, p.Duration, suffix)
		default:
			require.Equal(t, p.Suffix, suffix)
		}
	}
}

// TestIntermediateNotesHeaderPartsByActionAndState pins the notes card's head
// in every state the call can settle in. The tool has three operations, so the
// action carries the verb and the operation together and the target stays
// empty: a settled head that only named the tool ("Notes") left the reader
// unable to tell a save from a read, and a head that repeated the raw operation
// after the verb ("Notes read") said the same thing twice.
func TestIntermediateNotesHeaderPartsByActionAndState(t *testing.T) {
	cwd := "/work/proj"
	notes := func(status string, input map[string]any) DisplayFrame {
		return DisplayFrame{
			Title: "intermediate_tool", Final: true,
			Meta: ToolMeta{ToolName: "intermediate_tool", Status: status, Input: input},
		}
	}
	action := func(status string, input map[string]any) string {
		return DisplayHeaderParts(notes(status, input), cwd).Action
	}

	// A call that named no operation, or one this build has no words for, is
	// the tool's own default: append.
	for _, input := range []map[string]any{nil, {"action": ""}, {"action": "append"}, {"action": "publish"}} {
		require.Equal(t, "Saved notes", action("completed", input), "settled, input %v", input)
		require.Equal(t, "Saving notes", action("running", input), "running, input %v", input)
		require.Equal(t, "Saving notes", action("awaiting approval", input), "awaiting, input %v", input)
		require.Equal(t, "Failed to save notes", action("failed", input), "failed, input %v", input)
		// The operation is read off the input lowercased and trimmed.
		if a, ok := input["action"]; ok && a == "append" {
			require.Equal(t, "Saved notes", action("completed", map[string]any{"action": " APPEND "}))
		}
	}
	read := map[string]any{"action": "read"}
	require.Equal(t, "Read notes", action("completed", read))
	require.Equal(t, "Reading notes", action("running", read))
	require.Equal(t, "Reading notes", action("awaiting approval", read))
	require.Equal(t, "Failed to read notes", action("failed", read))
	clear := map[string]any{"action": "clear"}
	require.Equal(t, "Cleared notes", action("completed", clear))
	require.Equal(t, "Clearing notes", action("running", clear))
	require.Equal(t, "Clearing notes", action("awaiting approval", clear))
	require.Equal(t, "Failed to clear notes", action("failed", clear))

	// The verb carries the operation: the raw enum is never the target.
	for _, input := range []map[string]any{nil, {"action": "append"}, read, clear} {
		for _, status := range []string{"completed", "running", "awaiting approval", "failed"} {
			require.Empty(t, DisplayHeaderParts(notes(status, input), cwd).Target, "target, status %q input %v", status, input)
		}
	}

	// A call the reader refused or dismissed never touched the notes, so its
	// head is the generic refusal and names no operation at all.
	require.Equal(t, HeaderParts{Action: "Canceled"}, DisplayHeaderParts(notes("canceled", read), cwd))
	require.Equal(t, HeaderParts{Action: "Denied"}, DisplayHeaderParts(notes("denied", read), cwd))

	// The time a save took rides apart from the words, as on every other card.
	done := notes("completed", map[string]any{"action": "append"})
	done.Duration = 1500 * time.Millisecond
	require.Equal(t, HeaderParts{Action: "Saved notes", Duration: "1.5s"}, DisplayHeaderParts(done, cwd))
}

// TestDisplayHeaderPartsPeerTools pins the two peer cards' heads (plan 199
// §3.2): a send names its recipient in a sentence, a listing has nobody to
// name, and each state says its own verb — including the call that named no
// recipient at all, whose head must not trail off after "to".
func TestDisplayHeaderPartsPeerTools(t *testing.T) {
	cwd := "/work/proj"
	send := func(status string) DisplayFrame {
		return DisplayFrame{
			Title: "peer_send", Final: true,
			Meta: ToolMeta{ToolName: "peer_send", Status: status,
				Input: map[string]any{"to": "builder", "message": "hello"}},
		}
	}
	require.Equal(t, HeaderParts{Action: "Sent a message to", Target: "builder"}, DisplayHeaderParts(send("completed"), cwd))
	require.Equal(t, HeaderParts{Action: "Sending a message to", Target: "builder"}, DisplayHeaderParts(send("running"), cwd))
	require.Equal(t, HeaderParts{Action: "Sending a message to", Target: "builder"}, DisplayHeaderParts(send("awaiting approval"), cwd))
	require.Equal(t, HeaderParts{Action: "Could not send a message to", Target: "builder"}, DisplayHeaderParts(send("failed"), cwd))
	require.Equal(t, "Canceled", DisplayHeaderParts(send("canceled"), cwd).Action)

	// A call that named nobody is a failure with nothing to point at: the head
	// drops the preposition instead of ending on "to " with nothing after it.
	nobody := DisplayFrame{
		Title: "peer_send", Final: true,
		Meta: ToolMeta{ToolName: "peer_send", Status: "failed", Input: map[string]any{"message": "hello"}},
	}
	require.Equal(t, HeaderParts{Action: "Could not send a message"}, DisplayHeaderParts(nobody, cwd))

	list := func(status string) DisplayFrame {
		return DisplayFrame{
			Title: "peer_list", Final: true,
			Meta: ToolMeta{ToolName: "peer_list", Status: status},
		}
	}
	require.Equal(t, HeaderParts{Action: "Listed agents"}, DisplayHeaderParts(list("completed"), cwd))
	require.Equal(t, HeaderParts{Action: "Listing agents"}, DisplayHeaderParts(list("running"), cwd))
	require.Equal(t, HeaderParts{Action: "Failed to list agents"}, DisplayHeaderParts(list("failed"), cwd))
}

// TestHeaderActionsCoverEveryKnownTool walks every known tool through every
// status a card can carry and asserts the action is one the web's wording
// table knows: a tool that starts wording itself outside the list leaves the
// Chinese table silently missing a word.
func TestHeaderActionsCoverEveryKnownTool(t *testing.T) {
	cwd := "/work/proj"
	inputs := map[string]map[string]any{
		"read_file":                 {"file_path": "/work/proj/a.go"},
		"edit_file":                 {"file_path": "/work/proj/a.go"},
		"write_file":                {"file_path": "/work/proj/a.go"},
		"request_permissions":       {"command": "ls"},
		"shell":                     {"command": "ls"},
		"web_fetch":                 {"url": "https://example.com"},
		"web_search":                {"query": "go modules"},
		"lsp":                       {"operation": "definition", "symbol": "Foo", "file_path": "/work/proj/a.go"},
		"retrieve_output":           {"id": 7, "lines": "10-20"},
		"session_todo":              {"action": "append"},
		"enter_plan_mode":           {},
		"exit_plan_mode":            {},
		"memories_add_ad_hoc_note":  {"filename": "note.md"},
		"memories_list":             {"path": ""},
		"memories_read":             {"path": "MEMORY.md"},
		"memories_search":           {"queries": []string{"go"}},
		"intermediate_tool":         {"action": "append"},
		"user_interaction":          {"questions": []any{}},
		"peer_send":                 {"to": "builder", "message": "hello"},
		"peer_list":                 {},
		"mcp__github__create_issue": {"owner": "octocat", "repo": "hello", "title": "bug"},
	}
	retrieveWithQuery := map[string]any{"id": 7, "query": "go test"}
	statuses := []string{"running", "awaiting approval", "completed", "failed", "denied", "canceled"}
	vocab := map[string]bool{}
	for _, a := range HeaderActions() {
		vocab[a] = true
	}
	check := func(t *testing.T, f DisplayFrame) {
		t.Helper()
		parts := DisplayHeaderParts(f, cwd)
		require.NotEmpty(t, parts.Action, "frame %+v produced no action", f)
		require.True(t, vocab[parts.Action],
			"action %q (tool %q, status %q) is not in HeaderActions", parts.Action, f.Title, f.Meta.Status)
	}
	for _, name := range headerTestSortedKeys(inputs) {
		for _, status := range statuses {
			input := inputs[name]
			if name == "retrieve_output" && (status == "running" || status == "awaiting approval" || status == "completed") {
				// A query changes the retrieve_output action on its own; cover
				// both spellings.
				input = retrieveWithQuery
			}
			meta := ToolMeta{ToolName: name, Status: status, Input: input}
			check(t, DisplayFrame{Title: name, Meta: meta, Final: true})
		}
	}
	// write_file's completed card says "Wrote" or "Created" by whether the
	// write deleted anything; both spellings belong to the table.
	withDeletions := DisplayFrame{Title: "write_file", Final: true, Content: "```diff\n-a\n+b\n```",
		Meta: ToolMeta{ToolName: "write_file", Status: "completed", Input: inputs["write_file"]}}
	check(t, withDeletions)
	noDeletions := DisplayFrame{Title: "write_file", Final: true, Content: "",
		Meta: ToolMeta{ToolName: "write_file", Status: "completed", Input: inputs["write_file"]}}
	check(t, noDeletions)
	// The notes tool owns three operations, and each names its own verb: an
	// operation this build learns later still falls back to the append words,
	// but the three it has are all in the table.
	for _, operation := range []string{"append", "read", "clear"} {
		for _, status := range statuses {
			check(t, DisplayFrame{Title: "intermediate_tool", Final: true, Meta: ToolMeta{
				ToolName: "intermediate_tool", Status: status, Input: map[string]any{"action": operation},
			}})
		}
	}
	// A skill card owns three headers of its own.
	for _, status := range []string{"loading", "completed", "failed"} {
		frameStatus := status
		if status == "loading" {
			frameStatus = "running"
		}
		check(t, DisplayFrame{Title: "read_file", Final: true, Meta: ToolMeta{
			ToolName: "read_file", Status: frameStatus, Category: "skill",
			SkillName: "caveman", SkillPath: "/skills/caveman/SKILL.md",
		}})
	}
}

// TestHeaderActionsGolden pins the action vocabulary the web's Chinese table
// is keyed by: the golden file in testdata is what the frontend's test reads,
// so a new action here fails both sides until the table has it.
func TestHeaderActionsGolden(t *testing.T) {
	got := HeaderActions()
	golden := filepath.Join("testdata", "header_actions.json")
	if os.Getenv("UPDATE_HEADER_ACTIONS") == "1" {
		b, err := json.MarshalIndent(got, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(golden, append(b, '\n'), 0o644))
	}
	raw, err := os.ReadFile(golden)
	require.NoError(t, err, "run with UPDATE_HEADER_ACTIONS=1 to write the golden file")
	var want []string
	require.NoError(t, json.Unmarshal(raw, &want))
	sort.Strings(want)
	if !slices.Equal(want, got) {
		t.Fatalf("HeaderActions drifted from testdata/header_actions.json:\ngot  %v\nwant %v\n"+
			"更新 testdata/header_actions.json（UPDATE_HEADER_ACTIONS=1）并同步 frontend 的中文表", got, want)
	}
}

// headerTestSortedKeys returns the test map's keys in order, so a
// failure names reproducible inputs.
func headerTestSortedKeys(m map[string]map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// list_usable_models words its three states like peer_list: the verb alone,
// no target — the answer's rows are the card body's business.
func TestListUsableModelsHeader(t *testing.T) {
	cwd := "/work/proj"
	list := func(status string) DisplayFrame {
		return DisplayFrame{
			Title: "list_usable_models", Final: true,
			Meta: ToolMeta{ToolName: "list_usable_models", Status: status},
		}
	}
	require.Equal(t, HeaderParts{Action: "Listed usable models"}, DisplayHeaderParts(list("completed"), cwd))
	require.Equal(t, HeaderParts{Action: "Listing usable models"}, DisplayHeaderParts(list("running"), cwd))
	require.Equal(t, HeaderParts{Action: "Failed to list usable models"}, DisplayHeaderParts(list("failed"), cwd))
}

// A refused question says what happened instead of the generic "Denied": the
// body lists what was asked, so the head must not claim the call ran.
func TestDeclinedAskHeaderSaysSo(t *testing.T) {
	cwd := "/work/proj"
	ask := func(status string) DisplayFrame {
		return DisplayFrame{
			Title: "user_interaction", Final: true,
			Meta: ToolMeta{ToolName: "user_interaction", Status: status},
		}
	}
	require.Equal(t, HeaderParts{Action: "User declined to answer questions"}, DisplayHeaderParts(ask("denied"), cwd))
	require.Equal(t, HeaderParts{Action: "Asked user"}, DisplayHeaderParts(ask("completed"), cwd))
	require.Equal(t, HeaderParts{Action: "Asking user"}, DisplayHeaderParts(ask("running"), cwd))
}
