package turn

import (
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// ResumeRejectedStop carries the snapshot and nothing else: Stopped marks the
// refusal without words, and the snapshot is copied so the wait row's own
// slice cannot be mutated under it.
func TestResumeRejectedStopCarriesSnapshotAsStopped(t *testing.T) {
	snapshot := []llm.Message{llm.UserMessage(llm.Text("run them"))}
	state := ResumeRejectedStop(snapshot)
	if state == nil {
		t.Fatal("ResumeRejectedStop returned nil")
	}
	if !state.Stopped {
		t.Fatal("ResumeRejectedStop must mark the resume Stopped")
	}
	if state.Denied || state.DeliveredReview || state.DenyReason != "" || state.DenyFeedback != "" {
		t.Fatalf("ResumeRejectedStop carried a denial it must not: %#v", state)
	}
	if len(state.Session) != 1 || state.Session[0].TextContent() != "run them" {
		t.Fatalf("ResumeRejectedStop session = %#v, want the snapshot verbatim", state.Session)
	}
	// The copy is defensive: appending to the original must not leak in.
	snapshot = append(snapshot, llm.UserMessage(llm.Text("later")))
	if len(state.Session) != 1 {
		t.Fatalf("the resume state shared the caller's slice: %#v", state.Session)
	}
}

// FirstPendingApprovalToolCall names the call the refusal lands on: the first
// call of the parked message with no result behind it, not the last same-tool
// call a name-only resolution would pick (plan 129).
func TestFirstPendingApprovalToolCallNamesTheFirstMissingCall(t *testing.T) {
	snapshot := []llm.Message{llm.AssistantMessage(nil,
		llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: `{}`}},
		llm.ToolCall{ID: "call-2", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: `{}`}},
		llm.ToolCall{ID: "call-3", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: `{}`}},
	)}
	call, ok := FirstPendingApprovalToolCall(snapshot)
	if !ok || call.ID != "call-1" {
		t.Fatalf("call = %v %v, want call-1: a name-only resolution would answer call-3", call.ID, ok)
	}

	// The calls ahead of the gate finished: the first missing one is the gate's.
	snapshot = append(snapshot,
		llm.ToolResultMessage("call-1", llm.Text("done")),
		llm.ToolResultMessage("call-2", llm.Text("done")),
	)
	call, ok = FirstPendingApprovalToolCall(snapshot)
	if !ok || call.ID != "call-3" {
		t.Fatalf("call = %v %v, want call-3 once its batch-mates have results", call.ID, ok)
	}

	// Everything answered: no pending call to name.
	snapshot = append(snapshot, llm.ToolResultMessage("call-3", llm.Text("done")))
	if _, ok := FirstPendingApprovalToolCall(snapshot); ok {
		t.Fatal("a fully answered snapshot names no pending call")
	}

	// A nested request_permissions gate parks under an outer call: that outer
	// call is the first missing one, and its card reads canceled (not refused).
	snapshot = []llm.Message{
		llm.AssistantMessage(nil,
			llm.ToolCall{ID: "read-call", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: `{}`}},
		),
	}
	call, ok = FirstPendingApprovalToolCall(snapshot)
	if !ok || call.ID != "read-call" {
		t.Fatalf("nested call = %v %v, want the outer read-call", call.ID, ok)
	}
}
