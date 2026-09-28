package event

import (
	"encoding/json"
	"os"
	"testing"
)

func TestEncodePayload(t *testing.T) {
	raw := EncodePayload(TurnCompletedPayload{
		Text: "done",
	})
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got["text"] != "done" {
		t.Fatalf("unexpected text: %#v", got["text"])
	}
}

func TestEncodePayload_StripsProtoKeysRecursively(t *testing.T) {
	raw := EncodePayload(map[string]any{
		"ok":            1,
		"_PROTO_secret": "drop",
		"nested": map[string]any{
			"_PROTO_more": "drop2",
			"keep":        true,
		},
	})
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if _, exists := got["_PROTO_secret"]; exists {
		t.Fatalf("proto key leaked: %#v", got)
	}
	nested, _ := got["nested"].(map[string]any)
	if _, exists := nested["_PROTO_more"]; exists {
		t.Fatalf("nested proto key leaked: %#v", nested)
	}
}

func TestCanonicalLifecycleEventNames(t *testing.T) {
	for _, typ := range []string{
		RunEventTurnStarted,
		RunEventTurnCompleted,
		RunEventTurnCancelled,
		RunEventTurnFailed,
		RunEventApprovalReq,
		RunEventApprovalResolved,
		RunEventContextCompacting,
		RunEventContextCompacted,
		RunEventContextCompactError,
		RunEventConfigReloaded,
		RunEventSessionSwitched,
	} {
		if typ == "" {
			t.Fatal("canonical event name is empty")
		}
	}
}

func TestSharedSubagentLiveResumeFixtureDecodesWithStableIdentity(t *testing.T) {
	raw, err := os.ReadFile("testdata/subagent_live_resume_events.json")
	if err != nil {
		t.Fatal(err)
	}
	var events []RunEvent
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("events=%d want 5", len(events))
	}
	for index, evt := range events {
		if evt.Sequence != int64(41+index) || evt.SchemaVersion != RunEventSchemaVersion {
			t.Fatalf("event %d cursor/version = %d/%d", index, evt.Sequence, evt.SchemaVersion)
		}
		if evt.SessionID != "conversation-1" || evt.RunID != "child-run-1" || evt.ID == "" || evt.CreatedAt.IsZero() {
			t.Fatalf("event %d lost stable identity: %+v", index, evt)
		}
	}
	var spawned SubagentSpawnedPayload
	if err := json.Unmarshal(events[0].Payload, &spawned); err != nil {
		t.Fatal(err)
	}
	if spawned.AgentID != "agent-1" || spawned.WorkerSessionID != "worker-1" || spawned.ParentToolCallID != "fanout-call-1" || spawned.TaskIndex != 2 || spawned.ExecutionID != "execution-1" {
		t.Fatalf("spawn identity = %+v", spawned)
	}
	var resolved ApprovalResolvedPayload
	if err := json.Unmarshal(events[3].Payload, &resolved); err != nil {
		t.Fatal(err)
	}
	if resolved.ActionID != "action-1" || resolved.AgentID != "agent-1" || resolved.Decision != "approved" {
		t.Fatalf("approval identity = %+v", resolved)
	}
}

// A plan-emitting tool's card is the plan it produces. Every path that decides
// whether to also draw a tool card — the terminal hook, the gateway hook, and
// the projection that rebuilds a run from persisted steps — asks this one
// function, because when they each decided for themselves a subagent's view
// showed the raw JSON result while the conversation showed the checklist.
func TestToolStepRendersAsPlan(t *testing.T) {
	for _, tc := range []struct {
		name, tool, kind, err string
		want                  bool
	}{
		{"todo started", "session_todo", RunEventToolStarted, "", true},
		{"todo completed", "session_todo", RunEventToolCompleted, "", true},
		{"todo output delta", "session_todo", RunEventToolOutputDelta, "", true},
		{"todo failed keeps its own card", "session_todo", RunEventToolCompleted, "disk full", false},
		{"todo plan step is not a tool card", "session_todo", RunEventPlanUpdated, "", false},
		{"other tool", "shell", RunEventToolCompleted, "", false},
		{"no tool", "", RunEventToolCompleted, "", false},
	} {
		if got := ToolStepRendersAsPlan(tc.tool, tc.kind, tc.err); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
