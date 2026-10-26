package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/stretchr/testify/require"
)

// TestCardCorpus pins the card facts both surfaces draw from. The file in
// testdata is what the web's cardCorpus.test.ts reads, so a change on either
// side that the other cannot reproduce fails here — the same contract
// header_actions.json already holds for the action vocabulary.
//
// Every entry is the payload shape the gateway sends over /ws/chat for a
// settled tool call (server.go gatewayToolCompletedStepData + wsevents.go
// withStepHeader/withStepSummary): snake_case keys, the card facts read back
// from the very body the card shows. The cases are all synthetic — no real
// session ids, paths or secrets may ever enter this file.
func TestCardCorpus(t *testing.T) {
	golden := filepath.Join("testdata", "cards.json")
	cases := cardCorpusCases()
	got := make(map[string]any, len(cases))
	for name, c := range cases {
		got[name] = c.facts()
	}
	if os.Getenv("UPDATE_CARD_CORPUS") == "1" {
		b, err := json.MarshalIndent(got, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(golden, append(b, '\n'), 0o644))
	}
	raw, err := os.ReadFile(golden)
	require.NoError(t, err, "run with UPDATE_CARD_CORPUS=1 to write the corpus")
	var want any
	require.NoError(t, json.Unmarshal(raw, &want))
	require.Equal(t, want, normalizeJSON(t, got),
		"card facts drifted from testdata/cards.json — 更新语料（UPDATE_CARD_CORPUS=1）并确认 frontend/src/lib/cardCorpus.test.ts 同步变红")
}

// normalizeJSON round-trips a value through JSON so a struct-laden map and the
// map decoded from the golden file compare as the same JSON tree: numbers on
// both sides become float64 and absent-vs-empty no longer fabricates a diff.
func normalizeJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var out any
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

// cardCorpusInput is one corpus case's engine-side input: the step event the
// card is drawn from, plus the three facts only the caller knows — the working
// directory paths display relative to, the runtime's one-line summary and how
// long the call took. Everything else (meta, body, header, card facts) is
// derived, never hand-written, so the corpus can only hold what the engine
// really produces.
type cardCorpusInput struct {
	evt      StepEvent
	cwd      string
	summary  string
	duration time.Duration
}

// facts renders the case the way the gateway renders a completed tool step:
// BuildToolMeta for the card's structured facts, FormatToolStepResult for the
// display body, DisplayHeader/DisplayHeaderParts for the header, and the card
// facts read back from that very body — shell_card for a shell call,
// file_diff for an edit_file/write_file call (wsevents.go shellCardFor /
// fileDiffFor).
func (c cardCorpusInput) facts() map[string]any {
	meta := BuildToolMeta(c.evt)
	body, _ := FormatToolStepResult(c.evt, DefaultMaxFormattedBody)
	frame := DisplayFrame{
		Title:    c.evt.ToolName,
		Summary:  c.summary,
		Meta:     meta,
		Final:    true,
		Content:  body,
		Duration: c.duration,
	}
	data := map[string]any{
		"kind":      event.RunEventToolCompleted,
		"step_id":   c.evt.StepID,
		"tool_name": c.evt.ToolName,
		"tool_meta": meta,
	}
	if body != "" {
		data["display_body"] = body
	}
	if s := c.summary; s != "" {
		data["summary"] = s
	}
	if header := DisplayHeader(frame, c.cwd); header != "" {
		data["header"] = header
	}
	if parts := DisplayHeaderParts(frame, c.cwd); parts.Action != "" {
		data["header_parts"] = parts
	}
	if c.duration > 0 {
		data["duration_seconds"] = c.duration.Seconds()
	}
	if strings.EqualFold(strings.TrimSpace(c.evt.ToolName), "shell") {
		if card, ok := ParseShellCard(body); ok {
			data["shell_card"] = card
		}
	}
	if name := strings.ToLower(strings.TrimSpace(c.evt.ToolName)); name == "edit_file" || name == "write_file" {
		if diff, ok := ParseFileDiff(body); ok {
			data["file_diff"] = diff
		}
	}
	return data
}

// cardCorpusCases is the corpus itself: five settled cards spanning the shell
// family (ok, failed, truncated), the file-edit family, and a refusal with the
// user's own words (plan 154's regression). Inputs are synthetic; the golden
// file holds only what these functions derive from them.
func cardCorpusCases() map[string]cardCorpusInput {
	shellDone := func(stepID string, output map[string]any) StepEvent {
		return StepEvent{
			Kind:     event.RunEventToolCompleted,
			StepID:   stepID,
			ToolName: "shell",
			Input:    map[string]any{"command": "make"},
			Output:   output,
		}
	}
	return map[string]cardCorpusInput{
		"shell_local": {
			evt:      shellDone("step-shell-ok", map[string]any{"stdout": "hello\n", "exit_code": 0}),
			cwd:      "/w",
			summary:  "ran make",
			duration: 1500 * time.Millisecond,
		},
		"shell_failed": {
			// A non-zero exit is a settled call whose failure rides the card
			// facts themselves (exit_code, stderr) — the runtime reports no
			// Error for it, so the card stays a shell card.
			evt: shellDone("step-shell-fail", map[string]any{
				"stdout": "partial\n", "stderr": "no rule to make target 'x'\n", "exit_code": 2,
			}),
			cwd:      "/w",
			summary:  "ran make",
			duration: 300 * time.Millisecond,
		},
		"shell_truncated": {
			evt: shellDone("step-shell-trunc", map[string]any{
				"stdout": "x", "exit_code": 0,
				"stdout_omitted_bytes": 412000, "retrieve_id": 7,
			}),
			cwd:      "/w",
			summary:  "ran make",
			duration: 2 * time.Second,
		},
		"file_edit": {
			evt: StepEvent{
				Kind:     event.RunEventToolCompleted,
				StepID:   "step-edit-a",
				ToolName: "edit_file",
				Input:    map[string]any{"file_path": "/w/a.go"},
				Output: map[string]any{
					"turn_diff": map[string]any{
						"path": "a.go", "added": 2, "deleted": 1,
						"unified_diff": "@@ -1 +1 @@\n-a\n+b",
					},
				},
			},
			cwd:      "/w",
			summary:  "edited a.go",
			duration: 400 * time.Millisecond,
		},
		"denied_with_words": {
			evt: DeniedToolStep(
				"step-deny-1", "shell",
				map[string]any{"command": "rm -rf vendor"},
				"use git clean instead",
			),
			cwd:     "/w",
			summary: "refused rm -rf vendor",
		},
	}
}
