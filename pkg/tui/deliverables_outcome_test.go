package tui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// A turn's deliverables group is the result of work that finished: a turn the
// user stopped or a request that failed says nothing about the files it wrote
// (owner 2026-11-23). These tests drive the real dispatch path — a scripted
// provider, a real store, the real reducer — because the defect was that the
// closing run-end drew the group whatever had ended the run.

// presentThenLLM answers its first call with a present_files tool call and its
// second with whatever the scenario scripts: an error (a failed turn) or a
// wait on the context (a cancelled one). entered closes when the second call
// begins, so a cancellation test stops the turn after the file was presented.
type presentThenLLM struct {
	args    string
	err     error
	entered chan struct{}
	once    sync.Once
	calls   int
	mu      sync.Mutex
}

func (m *presentThenLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.mu.Lock()
	m.calls++
	first := m.calls == 1
	m.mu.Unlock()
	if first {
		msg := llm.AssistantMessage(nil, llm.ToolCall{
			ID:       "call-present",
			Type:     llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "present_files", Arguments: m.args},
		})
		return &llm.Result{Message: &msg}, nil
	}
	m.once.Do(func() { close(m.entered) })
	if m.err != nil {
		return nil, m.err
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// uiCapture records the notifications the surface really emitted, in order.
type uiCapture struct {
	mu   sync.Mutex
	msgs []any
}

func (c *uiCapture) add(m any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, m)
}

func (c *uiCapture) snapshot() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]any(nil), c.msgs...)
}

// awaitRunEnd waits for the closing run-end notification: the dispatcher is a
// goroutine, so the last message of a turn arrives after DispatchSurfaceTurn
// returned.
func (c *uiCapture) awaitRunEnd(t *testing.T) []any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		msgs := c.snapshot()
		for _, m := range msgs {
			if _, ok := m.(RunEndedMsg); ok {
				return msgs
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no RunEndedMsg arrived; captured %d notifications", len(msgs))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// deliverableFrames reduces the captured notifications the way the surface
// does and returns the deliverables groups they drew.
func deliverableFrames(msgs []any) []Frame {
	r := &Reducer{}
	var out []Frame
	for _, m := range msgs {
		for _, f := range r.Reduce(m).Frames {
			if f.Kind == FrameDeliverables {
				out = append(out, f)
			}
		}
	}
	return out
}

// presentFilesSession wires a characterization session whose project holds one
// file the scripted provider presents before the turn ends.
func presentFilesSession(t *testing.T, client llm.LLM) (*ChatSession, string) {
	t.Helper()
	s := newCharacterizationSession(t, nil)
	project := t.TempDir()
	target := filepath.Join(project, "report.md")
	if err := os.WriteFile(target, []byte("# report\n\nthe turn's work\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	s.runner().ProjectRoot = project
	args, err := json.Marshal(map[string]any{"files": []map[string]string{{"path": target, "title": "Report"}}})
	if err != nil {
		t.Fatalf("marshal present_files args: %v", err)
	}
	if p, ok := client.(*presentThenLLM); ok {
		p.args = string(args)
	}
	restore := run.SetLLMOverrideForTest(client)
	t.Cleanup(restore)
	if err := s.runner().Load(); err != nil {
		t.Fatalf("Runner.Load: %v", err)
	}
	return s, project
}

// TestFailedTurnShowsNoDeliverables is the owner's screenshot: the provider
// refused the request (HTTP 429) after the turn had presented a file, and the
// transcript still closed with a deliverables group above the error. A failed
// turn delivers nothing.
func TestFailedTurnShowsNoDeliverables(t *testing.T) {
	client := &presentThenLLM{err: errors.New("Allocated quota exceeded (HTTP 429 Too Many Requests)"), entered: make(chan struct{})}
	s, _ := presentFilesSession(t, client)
	capture := &uiCapture{}
	s.PrependUINotify(capture.add)

	err := s.DispatchSurfaceTurn(context.Background(), turn.TurnSubmission{
		SessionID: "deliverables-failed",
		UserText:  "write the report",
	})
	if err == nil {
		t.Fatal("the scripted provider failed, so the dispatch must report the failure")
	}
	got := captureCharacterization(t, s, "deliverables-failed")
	if len(got.Runs) != 1 || got.Runs[0].Status != state.RunStatusFailed {
		t.Fatalf("Runs = %+v, want one failed run", got.Runs)
	}
	if groups := deliverableFrames(capture.awaitRunEnd(t)); len(groups) != 0 {
		t.Fatalf("a failed turn drew deliverables: %#v", groups)
	}
}

// TestCancelledTurnShowsNoDeliverables is the other half of the rule: the user
// stops the turn after it presented a file. The files it wrote are still on
// disk, but the turn says nothing about them.
func TestCancelledTurnShowsNoDeliverables(t *testing.T) {
	client := &presentThenLLM{entered: make(chan struct{})}
	s, _ := presentFilesSession(t, client)
	capture := &uiCapture{}
	s.PrependUINotify(capture.add)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- s.DispatchSurfaceTurn(ctx, turn.TurnSubmission{
			SessionID: "deliverables-cancelled",
			UserText:  "write the report",
		})
	}()
	select {
	case <-client.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the provider never reached its second call")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled dispatch never returned")
	}
	got := captureCharacterization(t, s, "deliverables-cancelled")
	if len(got.Runs) != 1 || got.Runs[0].Status != state.RunStatusCancelled {
		t.Fatalf("Runs = %+v, want one cancelled run", got.Runs)
	}
	if groups := deliverableFrames(capture.awaitRunEnd(t)); len(groups) != 0 {
		t.Fatalf("a cancelled turn drew deliverables: %#v", groups)
	}
}

// TestCompletedTurnStillShowsDeliverables guards the other side of the rule: a
// turn that finished its work still closes with its group.
func TestCompletedTurnStillShowsDeliverables(t *testing.T) {
	s := newCharacterizationSession(t, nil)
	project := t.TempDir()
	target := filepath.Join(project, "report.md")
	if err := os.WriteFile(target, []byte("# report\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	s.runner().ProjectRoot = project
	args, err := json.Marshal(map[string]any{"files": []map[string]string{{"path": target, "title": "Report"}}})
	if err != nil {
		t.Fatalf("marshal present_files args: %v", err)
	}
	final := llm.AssistantMessage([]llm.ContentPart{llm.Text("The report is ready.")})
	restore := run.SetLLMOverrideForTest(&sequencedLLM{results: []*llm.Result{
		{Message: func() *llm.Message {
			msg := llm.AssistantMessage(nil, llm.ToolCall{
				ID:       "call-present",
				Type:     llm.ToolTypeFunction,
				Function: llm.FunctionCall{Name: "present_files", Arguments: string(args)},
			})
			return &msg
		}()},
		{Message: &final},
	}})
	t.Cleanup(restore)
	if err := s.runner().Load(); err != nil {
		t.Fatalf("Runner.Load: %v", err)
	}
	capture := &uiCapture{}
	s.PrependUINotify(capture.add)
	if err := s.DispatchSurfaceTurn(context.Background(), turn.TurnSubmission{
		SessionID: "deliverables-completed",
		UserText:  "write the report",
	}); err != nil {
		t.Fatalf("DispatchSurfaceTurn: %v", err)
	}
	got := captureCharacterization(t, s, "deliverables-completed")
	if len(got.Runs) != 1 || got.Runs[0].Status != state.RunStatusDone {
		t.Fatalf("Runs = %+v, want one completed run", got.Runs)
	}
	groups := deliverableFrames(capture.awaitRunEnd(t))
	if len(groups) != 1 || len(groups[0].Deliverables) == 0 {
		t.Fatalf("a completed turn drew %#v, want its one deliverables group", groups)
	}
}

// TestReplayShowsNoDeliverablesForFailedOrCancelledTurn pins the resumed
// conversation's half of the rule: the stored terminal event of a failed or
// cancelled run still carries its artifact rows (the record of what it wrote),
// and the replay must not draw them as the turn's delivery.
func TestReplayShowsNoDeliverablesForFailedOrCancelledTurn(t *testing.T) {
	artifacts := event.RunArtifacts{Artifacts: []event.Artifact{{
		ID: "art-1", Name: "report.md", Title: "Report", Kind: "markdown", RelPath: "report.md", Size: 32,
	}}}
	for _, tc := range []struct {
		name    string
		typ     string
		payload any
	}{
		{"failed", event.RunEventTurnError, event.TurnErrorPayload{Error: "HTTP 429", RunArtifacts: artifacts}},
		{"cancelled", event.RunEventTurnCancelled, event.TurnCancelledPayload{Message: "cancelled", RunArtifacts: artifacts}},
	} {
		payload, err := json.Marshal(tc.payload)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		rows := replayFrameDeliverables([]event.RunEvent{{Type: tc.typ, RunID: "run-1", Payload: payload}})["run-1"]
		if len(rows.rows) != 0 || !rows.withheld {
			t.Fatalf("%s: replay delivery = %#v, want no rows and the group withheld", tc.name, rows)
		}
	}
	// The completed run's rows still replay.
	payload, err := json.Marshal(event.TurnCompletedPayload{RunArtifacts: artifacts})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rows := replayFrameDeliverables([]event.RunEvent{{Type: event.RunEventTurnCompleted, RunID: "run-1", Payload: payload}})["run-1"]
	if len(rows.rows) != 1 || rows.withheld {
		t.Fatalf("completed replay delivery = %#v, want the one delivered file", rows)
	}
}

// TestReplayCompletedEndingWinsOverAStrayError pins the run that recovered: a
// transient failure recorded beside the run's own completed ending does not
// withhold the delivery, whatever order the log holds the two events in.
func TestReplayCompletedEndingWinsOverAStrayError(t *testing.T) {
	artifacts := event.RunArtifacts{Artifacts: []event.Artifact{{ID: "art-1", Name: "report.md", RelPath: "report.md"}}}
	completedPayload, err := json.Marshal(event.TurnCompletedPayload{RunArtifacts: artifacts})
	if err != nil {
		t.Fatalf("marshal completed: %v", err)
	}
	errorPayload, err := json.Marshal(event.TurnErrorPayload{Error: "transient"})
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	completed := event.RunEvent{Type: event.RunEventTurnCompleted, RunID: "run-1", Payload: completedPayload}
	failed := event.RunEvent{Type: event.RunEventTurnError, RunID: "run-1", Payload: errorPayload}
	for _, order := range [][]event.RunEvent{{failed, completed}, {completed, failed}} {
		delivery := replayFrameDeliverables(order)["run-1"]
		if delivery.withheld || len(delivery.rows) != 1 {
			t.Fatalf("delivery = %#v, want the completed run's file delivered", delivery)
		}
	}
}

// TestReplayWithholdsTheGroupOfAFailedOrCancelledTurn is the resumed
// conversation's whole group: the turn presented files live and its stored
// ending was a failure or a cancel. Neither half of the group — what the
// present_files row carried, nor what the terminal event carried — is drawn.
// A completed ending still draws the union of both.
func TestReplayWithholdsTheGroupOfAFailedOrCancelledTurn(t *testing.T) {
	frameOnly := event.Artifact{ID: "art-topup", Name: "notes.txt", Title: "notes.txt", Kind: "text", RelPath: "out/notes.txt", Size: 12}
	for _, tc := range []struct {
		name       string
		typ        string
		payload    any
		wantGroups int
	}{
		{"failed", event.RunEventTurnError, event.TurnErrorPayload{Error: "HTTP 429", RunArtifacts: event.RunArtifacts{Artifacts: []event.Artifact{frameOnly}}}, 0},
		{"cancelled", event.RunEventTurnCancelled, event.TurnCancelledPayload{Message: "cancelled", RunArtifacts: event.RunArtifacts{Artifacts: []event.Artifact{frameOnly}}}, 0},
		{"completed", event.RunEventTurnCompleted, event.TurnCompletedPayload{RunArtifacts: event.RunArtifacts{Artifacts: []event.Artifact{frameOnly}}}, 1},
	} {
		payload, err := json.Marshal(tc.payload)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		turns := replayedDeliverableTurns(true, deliverableRows())
		events := []event.RunEvent{{Type: tc.typ, RunID: "run-1", Payload: payload}}
		renderer := NewRenderer(nil, nil)
		renderer.EnableViewportMode()
		replayTimelineWithWorkers(renderer, turns, events, &Reducer{}, nil, nil)
		groups := 0
		rows := 0
		for _, block := range renderer.vm.blocks {
			if block.frame.Kind == FrameDeliverables {
				groups++
				rows = len(block.frame.Deliverables)
			}
		}
		renderer.DisableViewportMode()
		if groups != tc.wantGroups {
			t.Fatalf("%s: replay drew %d deliverables groups, want %d", tc.name, groups, tc.wantGroups)
		}
		if tc.wantGroups == 1 && rows != len(deliverableRows())+1 {
			t.Fatalf("%s: replayed group holds %d rows, want the presented files plus the top-up", tc.name, rows)
		}
	}
}
