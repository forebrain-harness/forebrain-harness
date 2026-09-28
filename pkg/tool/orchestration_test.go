package tool

import (
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
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
