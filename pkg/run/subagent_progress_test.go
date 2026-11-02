package run

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	toolpkg "github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// progressFixture is one subagent's conversation as the progress reader finds
// it: a real store to read, the workspace root its todo list joins, and the
// record naming both.
type progressFixture struct {
	fac     Factory
	store   *state.SessionStore
	record  agent.HistoryEntry
	project string
}

func newProgressFixture(t *testing.T, workerSessionID string) progressFixture {
	t.Helper()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	root := t.TempDir()
	project := t.TempDir()
	owner := &Runner{
		Deps:    &Deps{Home: root, WorkspaceRoot: root, ProjectRoot: project, SessionStore: store, RunRT: &state.RunStore{DB: db}},
		Control: NewController(),
	}
	fac := Factory{AgentName: "main", Home: root, WorkspaceRoot: root}
	fac.Owner = owner
	return progressFixture{
		fac:     fac,
		store:   store,
		record:  agent.HistoryEntry{TaskID: "task-progress", AgentType: "explore", WorkerSessionID: workerSessionID, SessionID: "conv-progress", Status: agent.StatusRunning},
		project: project,
	}
}

// appendToolRow writes one settled tool row the way the turn writes it: the
// card facts in the row's display part, the execution window in its own
// columns.
func appendToolRow(t *testing.T, store *state.SessionStore, sessionID, callID string, evt toolpkg.StepEvent, durationMs int64) {
	t.Helper()
	evt.StepID = callID
	metaJSON, err := json.Marshal(toolpkg.BuildToolMeta(evt))
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	msg := llm.ToolResultMessage(callID, llm.Text("result"))
	msg.ToolDisplay = &llm.ToolDisplayState{
		Body:         "result",
		Summary:      strings.TrimSpace(toolpkg.SummarizeToolStep(evt)),
		ToolMetaJSON: string(metaJSON),
	}
	parts := state.MessagePartsJSON(msg, "result")
	if _, err := store.AppendStructuredMessage(context.Background(), sessionID, llm.RoleTool, "result", "",
		parts, "", "", callID, "", state.MessageExecTiming{StartedAtMs: 1700000000000, FinishedAtMs: 1700000000000 + durationMs, DurationMs: durationMs}); err != nil {
		t.Fatalf("append tool row: %v", err)
	}
}

// appendAssistantRow writes the model's turn: what it said and the calls it
// made, which the transcript holds before those calls run.
func appendAssistantRow(t *testing.T, store *state.SessionStore, sessionID, text string, calls ...llm.ToolCall) {
	t.Helper()
	msg := llm.AssistantMessage(nil, calls...)
	msg.Parts = []llm.ContentPart{llm.Text(text)}
	parts := state.MessagePartsJSON(msg, text)
	if _, err := store.AppendStructuredMessage(context.Background(), sessionID, llm.RoleAssistant, text, "",
		parts, "", "", "", "", state.MessageExecTiming{}); err != nil {
		t.Fatalf("append assistant row: %v", err)
	}
}

// A running subagent's progress is read from its own conversation: the steps it
// settled, the call it is on now, its checklist and the last thing it said. The
// step names are the card headers the surface drew, read back out of the tool
// metadata the transcript kept.
func TestSubagentProgressReadsTheRunningConversation(t *testing.T) {
	const wsid = "ws-progress-running"
	f := newProgressFixture(t, wsid)
	read := toolpkg.StepEvent{
		Kind: toolpkg.StepKindToolCompleted, ToolName: "read_file",
		Input:  map[string]any{"file_path": filepath.Join(f.project, "main.go")},
		Output: map[string]any{"result_lines": 40},
	}
	shell := toolpkg.StepEvent{
		Kind: toolpkg.StepKindToolCompleted, ToolName: "shell",
		Input: map[string]any{"command": "go build ./..."},
	}
	failed := toolpkg.StepEvent{
		Kind: toolpkg.StepKindToolCompleted, ToolName: "edit_file",
		Input: map[string]any{"file_path": filepath.Join(f.project, "main.go")},
		Error: "old_string not found",
	}
	appendAssistantRow(t, f.store, wsid, "Looking at the repo.",
		llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"main.go"}`}},
		llm.ToolCall{ID: "call-2", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: `{"command":"go build ./..."}`}},
		llm.ToolCall{ID: "call-3", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "edit_file", Arguments: `{"file_path":"main.go"}`}},
	)
	appendToolRow(t, f.store, wsid, "call-1", read, 1200)
	appendToolRow(t, f.store, wsid, "call-2", shell, 3400)
	appendToolRow(t, f.store, wsid, "call-3", failed, 15)
	long := strings.Repeat("字", subagentProgressLastMessageRunes+100)
	appendAssistantRow(t, f.store, wsid, long,
		llm.ToolCall{ID: "call-4", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: `{"command":"ls -la"}`}},
	)
	if err := state.Save(f.fac.workspaceRoot(), wsid, state.List{Items: []state.Item{
		{ID: "1", Content: "read the router", Status: state.StatusCompleted},
		{ID: "2", Content: "read the handlers", Status: state.StatusCompleted},
		{ID: "3", Content: "write the report", Status: state.StatusInProgress},
		{ID: "4", Content: "run the tests", Status: state.StatusPending},
	}}); err != nil {
		t.Fatalf("save todos: %v", err)
	}
	ch := subagentChannelFor(f.fac, f.record)
	ch.mu.Lock()
	ch.running = &subagentExecution{executionID: "exec-progress-1"}
	ch.mu.Unlock()

	progress := buildSubagentProgress(context.Background(), f.fac, f.record)

	if progress.Phase != "running" {
		t.Fatalf("phase = %q, want running", progress.Phase)
	}
	if progress.Steps != 4 {
		t.Fatalf("steps = %d, want the three settled steps and the call in flight", progress.Steps)
	}
	if progress.Current != "Running ls -la" {
		t.Fatalf("current = %q, want the running card's own words", progress.Current)
	}
	if len(progress.Recent) != 3 {
		t.Fatalf("recent = %#v, want three settled steps", progress.Recent)
	}
	if progress.Recent[0].Header != "Read main.go lines 1-40" {
		t.Fatalf("recent[0].header = %q, want the read card's header rebuilt from its tool meta", progress.Recent[0].Header)
	}
	if progress.Recent[0].Outcome != "ok" || progress.Recent[0].DurationMs != 1200 {
		t.Fatalf("recent[0] = %#v, want ok after 1200ms", progress.Recent[0])
	}
	if progress.Recent[1].Header != "Ran go build ./..." {
		t.Fatalf("recent[1].header = %q, want the shell card's header", progress.Recent[1].Header)
	}
	if progress.Recent[2].Outcome != "failed" {
		t.Fatalf("recent[2].outcome = %q, want failed", progress.Recent[2].Outcome)
	}
	if progress.Todos == nil || progress.Todos.Completed != 2 || progress.Todos.Total != 4 {
		t.Fatalf("todos = %#v, want 2 of 4", progress.Todos)
	}
	if len(progress.Todos.Items) != 4 || progress.Todos.Items[2].Status != string(state.StatusInProgress) {
		t.Fatalf("todo items = %#v, want every row with its status", progress.Todos.Items)
	}
	if want := strings.Repeat("字", subagentProgressLastMessageRunes) + "…"; progress.LastMessage != want {
		t.Fatalf("last_message is %d runes, want it bounded to %d plus an ellipsis", len([]rune(progress.LastMessage)), subagentProgressLastMessageRunes)
	}
}

// A finished subagent has no step in flight, and how long it took is its
// record's own window.
func TestSubagentProgressOfAFinishedRecord(t *testing.T) {
	f := newProgressFixture(t, "ws-progress-finished")
	f.record.Status = agent.StatusOK
	f.record.StartedAt = 1700000000
	f.record.FinishedAt = 1700000060

	progress := buildSubagentProgress(context.Background(), f.fac, f.record)

	if progress.Phase != "finished" {
		t.Fatalf("phase = %q, want finished", progress.Phase)
	}
	if progress.ElapsedMs != 60000 {
		t.Fatalf("elapsed_ms = %d, want finished_at - started_at in milliseconds", progress.ElapsedMs)
	}
	if progress.Current != "" || progress.Steps != 0 || len(progress.Recent) != 0 {
		t.Fatalf("finished progress = %#v, want no step in flight", progress)
	}
	if progress.Todos != nil || progress.Usage != nil {
		t.Fatalf("finished progress = %#v, want no checklist and no usage it never spent", progress)
	}
}
