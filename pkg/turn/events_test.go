package turn

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestExtractTurnDiffPayloadIncludesStatsAndHunks(t *testing.T) {
	payload, ok := ExtractTurnDiffPayload(map[string]any{
		"kind":      event.RunEventToolCompleted,
		"step_id":   "write_file-1",
		"tool_name": "write_file",
		"output": map[string]any{
			"status": "ok",
			"turn_diff": map[string]any{
				"path":         "/tmp/demo.txt",
				"added":        1,
				"deleted":      1,
				"unified_diff": "--- a\n+++ b\n@@ -1 +1 @@\n-old\n+new\n",
			},
		},
	})
	require.True(t, ok)
	require.Len(t, payload.Files, 1)
	f := payload.Files[0]
	require.Equal(t, "/tmp/demo.txt", f.Path)
	require.Equal(t, 1, f.Added)
	require.Equal(t, 1, f.Deleted)
	require.NotEmpty(t, payload.Summary)
	require.NotEmpty(t, f.Hunks)
	var hasAdd, hasDel bool
	for _, l := range f.Hunks[0].Lines {
		switch l.Kind {
		case event.DiffLineAdd:
			hasAdd = true
		case event.DiffLineDelete:
			hasDel = true
		}
	}
	require.True(t, hasAdd && hasDel)
}

func TestExtractTurnDiffPayloadHasNoRawDiffKey(t *testing.T) {
	payload, ok := ExtractTurnDiffPayload(map[string]any{
		"output": map[string]any{
			"turn_diff": map[string]any{
				"path":         "file.go",
				"added":        2,
				"deleted":      1,
				"unified_diff": "diff --git a/file.go b/file.go\nindex a..b 100644\n--- a/file.go\n+++ b/file.go\n@@ -1,3 +1,4 @@\n ctx\n-old\n+new\n+extra\n",
			},
		},
	})
	require.True(t, ok)
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	_, present := decoded["diff"]
	require.False(t, present, "raw diff string must not appear in payload")
}

func TestExtractTurnDiffPayloadRejectsPayloadWithoutTurnDiff(t *testing.T) {
	_, ok := ExtractTurnDiffPayload(map[string]any{"output": map[string]any{"status": "ok"}})
	require.False(t, ok)
}

type stubRunEventStore struct {
	run    *state.Run
	events []state.SessionEvent
}

func (s stubRunEventStore) GetRun(ctx context.Context, id string) (*state.Run, error) {
	return s.run, nil
}

func (s stubRunEventStore) ListRunEvents(ctx context.Context, runID string, limit int) ([]state.SessionEvent, error) {
	if limit <= 0 || limit >= len(s.events) {
		return append([]state.SessionEvent(nil), s.events...), nil
	}
	return append([]state.SessionEvent(nil), s.events[:limit]...), nil
}

func (s stubRunEventStore) ListRunsBySession(ctx context.Context, sessionID string, limit int) ([]state.Run, error) {
	if s.run == nil || s.run.SessionID != sessionID {
		return nil, nil
	}
	return []state.Run{*s.run}, nil
}

// The run events API reads the durable log, not a projection of a step ledger:
// every stored row of the run reaches the wire in order, stamped with the
// current schema version the row no longer carries.
func TestListRunEventsReturnsStoredEventsInOrder(t *testing.T) {
	svc := New(WithRunEventStore(stubRunEventStore{
		run: &state.Run{ID: "r1", SessionID: "s1"},
		events: []state.SessionEvent{
			{ID: "evt-1", Sequence: 1, RunID: "r1", SessionID: "s1", Type: event.RunEventToolStarted, Payload: json.RawMessage(`{"tool_name":"Bash"}`)},
			{ID: "evt-2", Sequence: 2, RunID: "r1", SessionID: "s1", Type: event.RunEventAssistantDelta, Payload: json.RawMessage(`{"text":"hello"}`)},
			{ID: "evt-3", Sequence: 3, RunID: "r1", SessionID: "s1", Type: event.RunEventApprovalReq, Payload: json.RawMessage(`{"action_id":"a1"}`)},
		},
	}))

	events, err := svc.ListRunEvents(context.Background(), "r1", 100)
	require.NoError(t, err)
	require.Len(t, events, 3)
	require.Equal(t, event.RunEventToolStarted, events[0].Type)
	require.Equal(t, event.RunEventAssistantDelta, events[1].Type)
	require.Equal(t, event.RunEventApprovalReq, events[2].Type)
	require.Equal(t, "s1", events[0].SessionID)
	require.Equal(t, "r1", events[0].RunID)
	require.Equal(t, event.RunEventSchemaVersion, events[0].SchemaVersion)
	require.EqualValues(t, 2, events[1].Sequence)
}
