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
