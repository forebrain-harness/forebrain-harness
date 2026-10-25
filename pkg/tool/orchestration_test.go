package tool

import (
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func TestPartitionToolCallsBatchesConsecutiveReadOnlyTools(t *testing.T) {
	metas := ToolMetaMap([]event.ToolMeta{
		{Name: "read_file", ReadOnly: true, ConcurrencySafe: true},
		{Name: "web_search", ReadOnly: true, ConcurrencySafe: true},
		{Name: "edit_file", Destructive: true},
		{Name: "shell", Destructive: true},
	})
	got := PartitionToolCalls([]string{"read_file", "web_search", "edit_file", "shell"}, metas)
	if len(got) != 3 {
		t.Fatalf("batch count=%d", len(got))
	}
	if !got[0].ConcurrencySafe || len(got[0].ToolNames) != 2 {
		t.Fatalf("first batch mismatch: %+v", got[0])
	}
	if got[1].ConcurrencySafe || got[1].ToolNames[0] != "edit_file" {
		t.Fatalf("write batch mismatch: %+v", got[1])
	}
	if got[2].ConcurrencySafe || got[2].ToolNames[0] != "shell" {
		t.Fatalf("shell batch must be serial: %+v", got[2])
	}
}

func TestPartitionToolCallsUnknownToolsAreSerial(t *testing.T) {
	got := PartitionToolCalls([]string{"missing", "also_missing"}, nil)
	if len(got) != 2 || got[0].ConcurrencySafe || got[1].ConcurrencySafe {
		t.Fatalf("unknown tools must be serial: %+v", got)
	}
}

func TestMaxToolConcurrencyFromEnv(t *testing.T) {
	t.Setenv("FOREBRAIN_MAX_TOOL_USE_CONCURRENCY", "4")
	if got := MaxToolConcurrencyFromEnv(); got != 4 {
		t.Fatalf("concurrency=%d", got)
	}
	t.Setenv("FOREBRAIN_MAX_TOOL_USE_CONCURRENCY", "bad")
	if got := MaxToolConcurrencyFromEnv(); got != 10 {
		t.Fatalf("default concurrency=%d", got)
	}
}

func TestStatePartitionToolCallsUsesRegisteredMetadata(t *testing.T) {
	st := NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})
	st.RegisterToolMeta(event.ToolMeta{Name: "write_file", Destructive: true})
	got := st.PartitionToolCalls([]string{"read_file", "write_file"})
	if len(got) != 2 || !got[0].ConcurrencySafe || got[1].ConcurrencySafe {
		t.Fatalf("state partition mismatch: %+v", got)
	}
}

func shellCall(arguments string) llm.ToolCall {
	return llm.ToolCall{ID: "call-shell", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: arguments}}
}

func TestCallConcurrencySafeJudgesShellByItsInput(t *testing.T) {
	st := NewState(t.TempDir())
	safe := []llm.ToolCall{
		shellCall(`{"command":"ls -la"}`),
		shellCall(`{"command":"cat a.txt"}`),
		shellCall(`{"command":"git status"}`),
		shellCall(`{"command":"ls","sandbox_permissions":"use_default"}`),
	}
	for _, call := range safe {
		if !st.CallConcurrencySafe(call) {
			t.Fatalf("call %s must be concurrency-safe", call.Function.Arguments)
		}
	}
	unsafe := []llm.ToolCall{
		shellCall(`{"command":"rm a"}`),
		shellCall(`{"command":"echo x > f"}`),
		shellCall(`{"command":"ls; rm a"}`),
		shellCall(`{"command":"ls","sandbox_permissions":"require_escalated"}`),
		shellCall(`{"command":"ls","additional_permissions":{"fs":{"write":["/tmp"]}}}`),
		shellCall(`not json`),
	}
	for _, call := range unsafe {
		if st.CallConcurrencySafe(call) {
			t.Fatalf("call %s must not be concurrency-safe", call.Function.Arguments)
		}
	}
}

// A tool whose metadata says read-only and concurrency-safe is safe whatever
// its arguments; an unknown tool never is.
func TestCallConcurrencySafeFollowsMetadataFirst(t *testing.T) {
	st := NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})
	reading := llm.ToolCall{ID: "call-read", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"a.txt"}`}}
	if !st.CallConcurrencySafe(reading) {
		t.Fatal("read_file is always concurrency-safe")
	}
	st.RegisterToolMeta(event.ToolMeta{Name: "shell", Destructive: true})
	if !st.CallConcurrencySafe(shellCall(`{"command":"ls -la"}`)) {
		t.Fatal("shell safety is judged by input even with destructive metadata")
	}
	unknown := llm.ToolCall{ID: "call-x", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "mystery", Arguments: `{}`}}
	if st.CallConcurrencySafe(unknown) {
		t.Fatal("an unknown tool is never concurrency-safe")
	}
}

func TestPartitionToolCallsForGroupsAdjacentSafeCalls(t *testing.T) {
	st := NewState(t.TempDir())
	calls := []llm.ToolCall{
		shellCall(`{"command":"ls -la"}`),
		{ID: "c2", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: `{"command":"cat b.txt"}`}},
		{ID: "c3", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: `{"command":"echo x > f"}`}},
		{ID: "c4", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: `{"command":"git status"}`}},
	}
	got := st.PartitionToolCallsFor(calls)
	if len(got) != 3 {
		t.Fatalf("groups = %d, want 3 (read, read | write | read): %+v", len(got), got)
	}
	if !got[0].ConcurrencySafe || len(got[0].ToolNames) != 2 {
		t.Fatalf("first group must join the two read-only shells: %+v", got[0])
	}
	if got[1].ConcurrencySafe || len(got[1].ToolNames) != 1 {
		t.Fatalf("the write command stands alone: %+v", got[1])
	}
	if !got[2].ConcurrencySafe || len(got[2].ToolNames) != 1 {
		t.Fatalf("the trailing read-only shell closes the batch: %+v", got[2])
	}
}

func TestStateTracksInProgressToolIDs(t *testing.T) {
	st := NewState(t.TempDir())
	st.MarkToolInProgress(" call-1 ")
	st.MarkToolInProgress("call-2")
	st.ClearToolInProgress("call-1")
	got := st.InProgressToolIDs()
	if len(got) != 1 || got[0] != "call-2" {
		t.Fatalf("in-progress ids=%v", got)
	}
}
