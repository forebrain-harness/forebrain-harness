package agent

import (
	"fmt"
	"testing"
)

func TestListHistoryFiltersAndCollapsesLatestRecords(t *testing.T) {
	workspace := t.TempDir()

	err := AppendHistory(workspace, HistoryEntry{
		TaskID:      "task-1",
		RunID:       "child-1",
		ParentRunID: "parent-1",
		SessionID:   "session-1",
		Task:        "first",
		Status:      StatusRunning,
		StartedAt:   10,
		UpdatedAt:   10,
	})
	if err != nil {
		t.Fatalf("append running: %v", err)
	}
	err = AppendHistory(workspace, HistoryEntry{
		TaskID:      "task-1",
		RunID:       "child-1",
		ParentRunID: "parent-1",
		SessionID:   "session-1",
		Task:        "first",
		Status:      StatusOK,
		Output:      "done",
		StartedAt:   10,
		UpdatedAt:   20,
		FinishedAt:  20,
	})
	if err != nil {
		t.Fatalf("append final: %v", err)
	}
	err = AppendHistory(workspace, HistoryEntry{
		TaskID:      "task-2",
		RunID:       "child-2",
		ParentRunID: "parent-2",
		SessionID:   "session-2",
		Task:        "second",
		Status:      StatusFailed,
		Error:       "boom",
		StartedAt:   12,
		UpdatedAt:   22,
		FinishedAt:  22,
	})
	if err != nil {
		t.Fatalf("append other session: %v", err)
	}
	err = AppendHistory(workspace, HistoryEntry{
		TaskID:      "task-3",
		RunID:       "child-3",
		ParentRunID: "parent-1",
		SessionID:   "session-1",
		Task:        "third",
		Status:      StatusFailed,
		Error:       "bad",
		StartedAt:   15,
		UpdatedAt:   25,
		FinishedAt:  25,
	})
	if err != nil {
		t.Fatalf("append sibling: %v", err)
	}

	list, err := ListHistory(workspace, Query{SessionID: "session-1", Limit: 10})
	if err != nil {
		t.Fatalf("list by session: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 session records, got %d", len(list))
	}
	if list[0].TaskID != "task-3" || list[0].Status != StatusFailed {
		t.Fatalf("unexpected newest record: %#v", list[0])
	}
	if list[1].TaskID != "task-1" || list[1].Status != StatusOK || list[1].Output != "done" {
		t.Fatalf("expected collapsed final record for task-1, got %#v", list[1])
	}

	parentList, err := ListHistory(workspace, Query{ParentRunID: "parent-1", Limit: 10})
	if err != nil {
		t.Fatalf("list by parent: %v", err)
	}
	if len(parentList) != 2 {
		t.Fatalf("expected 2 parent records, got %d", len(parentList))
	}
	if parentList[0].TaskID != "task-3" || parentList[1].TaskID != "task-1" {
		t.Fatalf("unexpected parent ordering: %#v", parentList)
	}
}

func TestListMergedUnboundedMakesOldSubagentCardsReachable(t *testing.T) {
	workspace := t.TempDir()
	for i := 0; i < 300; i++ {
		err := AppendHistory(workspace, HistoryEntry{
			TaskID:    fmt.Sprintf("task-%03d", i),
			RunID:     fmt.Sprintf("run-%03d", i),
			SessionID: "session-long",
			Task:      fmt.Sprintf("task body %03d", i),
			Status:    StatusOK,
			UpdatedAt: int64(i + 1),
		})
		if err != nil {
			t.Fatalf("AppendHistory(%d): %v", i, err)
		}
	}

	got, err := ListMerged(workspace, Query{SessionID: "session-long", Limit: -1})
	if err != nil {
		t.Fatalf("ListMerged: %v", err)
	}
	if len(got) != 300 {
		t.Fatalf("records = %d, want all 300", len(got))
	}
	if got[0].TaskID != "task-299" || got[len(got)-1].TaskID != "task-000" {
		t.Fatalf("unexpected endpoints: first=%q last=%q", got[0].TaskID, got[len(got)-1].TaskID)
	}
}
