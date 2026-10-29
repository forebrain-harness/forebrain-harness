package tool

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/stretchr/testify/require"
)

// TestParseShellCardReadsBackWhatFormatShellStepWrote pins the pair
// formatShellStep ↔ ParseShellCard: every fact the card's Markdown carries —
// streams, exit code, truncation counts, retrieval id, diff blocks, notes —
// reads back exactly, directly and through a JSON replay, so a reloaded card
// and a live one show the same facts.
func TestParseShellCardReadsBackWhatFormatShellStepWrote(t *testing.T) {
	code := func(n int) *int { return &n }
	shellDone := func(output map[string]any) StepEvent {
		return StepEvent{
			Kind:     event.RunEventToolCompleted,
			ToolName: "shell",
			Input:    map[string]any{"command": "make"},
			Output:   output,
		}
	}
	cases := []struct {
		name string
		evt  StepEvent
		want ShellCard
	}{
		{
			name: "stdout only",
			evt:  shellDone(map[string]any{"stdout": "hello\n", "exit_code": 0}),
			want: ShellCard{Stdout: "hello", ExitCode: code(0)},
		},
		{
			name: "stdout and stderr with a failing exit",
			evt:  shellDone(map[string]any{"stdout": "out", "stderr": "boom", "exit_code": 2}),
			want: ShellCard{Stdout: "out", Stderr: "boom", ExitCode: code(2)},
		},
		{
			name: "stdout that carries its own fence lines",
			evt:  shellDone(map[string]any{"stdout": "a\n```\nb\n````\nc", "exit_code": 0}),
			want: ShellCard{Stdout: "a\n```\nb\n````\nc", ExitCode: code(0)},
		},
		{
			name: "truncated stdout with a spool behind it",
			evt: shellDone(map[string]any{
				"stdout": "x", "exit_code": 0,
				"stdout_omitted_bytes": 412000, "retrieve_id": 7, "spool_omitted_bytes": 0,
			}),
			want: ShellCard{Stdout: "x", ExitCode: code(0), StdoutOmittedBytes: 412000, RetrieveID: 7},
		},
		{
			name: "apply_patch with two files and no input block",
			evt: StepEvent{
				Kind:     event.RunEventToolCompleted,
				ToolName: "shell",
				Input:    map[string]any{"command": "apply_patch <<'PATCH'\nPATCH"},
				Output: map[string]any{
					"apply_patch": true, "exit_code": 0,
					"files": []any{
						map[string]any{
							"operation": "update", "display_path": "a.go", "abs_path": "/w/a.go",
							"turn_diff": map[string]any{"path": "/w/a.go", "added": 2, "deleted": 1, "unified_diff": "@@ -1 +1 @@\n-a\n+b"},
						},
						map[string]any{
							"operation": "add", "display_path": "new.go", "abs_path": "/w/new.go",
							"turn_diff": map[string]any{"path": "/w/new.go", "added": 3, "deleted": 0, "unified_diff": "@@ -0,0 +1,3 @@\n+x\n+y\n+z"},
						},
					},
				},
			},
			want: ShellCard{
				ExitCode: code(0),
				Files: []ShellCardFile{
					{Verb: "Updated", Path: "a.go", Added: 2, Deleted: 1, UnifiedDiff: "@@ -1 +1 @@\n-a\n+b"},
					{Verb: "Created", Path: "new.go", Added: 3, Deleted: 0, UnifiedDiff: "@@ -0,0 +1,3 @@\n+x\n+y\n+z"},
				},
			},
		},
		{
			name: "shell_edit_diff with more files and a shared repository",
			evt: shellDone(map[string]any{
				"stdout": "done", "exit_code": 0,
				"shell_edit_diff": ShellEditDiff{
					Files: []ShellEditDiffFile{{
						Path: "/w/a.go", DisplayPath: "a.go", Status: "updated",
						Added: 2, Deleted: 1, UnifiedDiff: "@@ -1 +1 @@\n-a\n+b",
					}},
					MoreFiles: 3, Shared: true,
				},
			}),
			want: ShellCard{
				Stdout: "done", ExitCode: code(0),
				Files: []ShellCardFile{
					{Verb: "Updated", Path: "a.go", Added: 2, Deleted: 1, UnifiedDiff: "@@ -1 +1 @@\n-a\n+b"},
				},
				Notes: []string{
					"… 3 more files changed",
					"(another command ran in this repository at the same time; a change made by either may show under either result)",
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := FormatToolStepResult(tc.evt, DefaultMaxFormattedBody)
			require.NotEmpty(t, body, "the shell card rendered no body")
			card, ok := ParseShellCard(body)
			require.True(t, ok, "a shell card body must parse, got:\n%s", body)
			require.Equal(t, tc.want, card)

			// The replay path: the card rides JSON (gateway row, event payload)
			// and must come back the same.
			b, err := json.Marshal(card)
			require.NoError(t, err)
			var replayed ShellCard
			require.NoError(t, json.Unmarshal(b, &replayed))
			require.Equal(t, tc.want, replayed)
		})
	}

	_, ok := ParseShellCard("turn diff: `a.go` (+1/-1)\n\n```diff\n+a\n```")
	require.False(t, ok, "a non-shell body is not a shell card")
}

// TestParseFileDiffReadsTheTurnDiffSection pins the pair
// turnDiffSection ↔ ParseFileDiff: an edit's card opens with its turn diff,
// and the diff facts read back exactly, with whatever follows (the LSP
// section) left for the surface to render as it always has.
func TestParseFileDiffReadsTheTurnDiffSection(t *testing.T) {
	evt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "edit_file",
		Input:    map[string]any{"file_path": "/w/a.go"},
		Output: map[string]any{
			"turn_diff": event.Summary{
				Path: "a.go", Added: 2, Deleted: 1,
				UnifiedDiff: "@@ -1 +1 @@\n-a\n+b",
			},
			"lsp_diagnostics": event.LSPDiagnosticsSummary{
				New: 1, Files: 1,
				Items: []event.LSPDiagnostic{{Path: "a.go", Line: 3, Column: 1, Severity: "error", Message: "undefined: x"}},
			},
		},
	}
	body, _ := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	require.NotEmpty(t, body)
	diff, ok := ParseFileDiff(body)
	require.True(t, ok, "an edit card body must parse, got:\n%s", body)
	require.Equal(t, "a.go", diff.Path)
	require.Equal(t, 2, diff.Added)
	require.Equal(t, 1, diff.Deleted)
	require.Equal(t, "@@ -1 +1 @@\n-a\n+b", diff.UnifiedDiff)
	require.True(t, strings.HasPrefix(diff.Rest, "lsp diagnostics:"),
		"the body after the turn diff section is the diagnostics', got:\n%s", diff.Rest)

	// The replay path rides JSON and comes back the same.
	b, err := json.Marshal(diff)
	require.NoError(t, err)
	var replayed FileDiff
	require.NoError(t, json.Unmarshal(b, &replayed))
	require.Equal(t, diff, replayed)

	// A turn diff whose diff did not fit still reports its header facts.
	noHunks := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "edit_file",
		Input:    map[string]any{"file_path": "/w/a.go"},
		Output: map[string]any{
			"turn_diff": event.Summary{Path: "a.go", Added: 1, Deleted: 0},
		},
	}
	noHunksBody, _ := FormatToolStepResult(noHunks, DefaultMaxFormattedBody)
	headerOnly, ok := ParseFileDiff(noHunksBody)
	require.True(t, ok)
	require.Equal(t, FileDiff{Path: "a.go", Added: 1, Deleted: 0}, headerOnly)

	_, ok = ParseFileDiff("**shell**\n\nstdout:\n\n```text\nx\n```")
	require.False(t, ok, "a body that does not open with the turn diff is not a file diff")
}

// TestParseFileDiffReadsAnEscalatedFence pins the fence-length half of the same
// pair: when the diff quotes a code fence within the context lines around the
// change, fenceFor opens the turn-diff fence with four backticks and only a
// fence of that same length may close it.
func TestParseFileDiffReadsAnEscalatedFence(t *testing.T) {
	unified := "@@ -2,7 +2,7 @@\n # Plan\n \n ```bash\n-make build\n+make build test\n ```\n tail"
	evt := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "edit_file",
		Input:    map[string]any{"file_path": "/w/docs/plan.md"},
		Output: map[string]any{
			"turn_diff": event.Summary{Path: "/w/docs/plan.md", Added: 1, Deleted: 1, UnifiedDiff: unified},
		},
	}
	body, _ := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	require.Contains(t, body, "````diff", "the writer escalates the fence past the one the diff quotes")
	diff, ok := ParseFileDiff(body)
	require.True(t, ok, "an escalated-fence card body must parse:\n%s", body)
	require.Equal(t, unified, diff.UnifiedDiff, "the whole diff reads back, through the quoted fence:\n%s", body)

	// A stream that quotes a three-backtick fence reads back whole as well.
	shell := StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "shell",
		Output:   map[string]any{"stdout": "```bash\nmake build\n```\ntail", "exit_code": 0},
	}
	shellBody, _ := FormatToolStepResult(shell, DefaultMaxFormattedBody)
	card, ok := ParseShellCard(shellBody)
	require.True(t, ok, "a shell card body must parse:\n%s", shellBody)
	require.Equal(t, "```bash\nmake build\n```\ntail", card.Stdout)
}
