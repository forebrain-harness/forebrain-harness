package turn

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

func checkpointRow(windowID string, createdAt int64) state.Message {
	part := state.CompactBoundaryPart{Trigger: "auto", Strategy: "local", ReplacementHistory: []llm.Message{llm.UserMessage(llm.Text("x"))}, WindowID: windowID}
	return state.Message{Role: "system", Content: "summary", PartsJSON: state.PartsJSONWithCompactPart("summary", state.EncodeCompactBoundaryPart(part)), CreatedAt: createdAt}
}

// TestCompactionPositionPlacesEachKindWhereItWasDrawn pins the one placement
// rule both the terminal replay and the web history use.
func TestCompactionPositionPlacesEachKindWhereItWasDrawn(t *testing.T) {
	turns := []state.Message{
		{Role: "user", Content: "first", CreatedAt: 10},
		{Role: "assistant", Content: "answer", CreatedAt: 10},
		checkpointRow("w-manual", 20),
		checkpointRow("w-preturn", 30),
		{Role: "user", Content: "second", CreatedAt: 31},
		{Role: "assistant", Content: "tool call", CreatedAt: 40},
		checkpointRow("w-midturn", 45),
		{Role: "assistant", Content: "final", CreatedAt: 50},
	}
	compacted := func(runID, trigger, boundary, agent string) event.RunEvent {
		return event.NewRunEvent("", runID, "s", event.RunEventContextCompacted,
			event.ContextCompactedPayload{Trigger: trigger, BoundaryID: boundary, AgentID: agent}, time.Unix(99, 0))
	}
	cases := []struct {
		name string
		evt  event.RunEvent
		want int
		ok   bool
	}{
		// A manual compaction is drawn where it was asked for: right after its
		// checkpoint.
		{"manual", compacted("", "manual", "w-manual", ""), 3, true},
		// A pre-turn one is drawn after the message it made room for, which was
		// on screen first.
		{"pre-turn", compacted("", "auto", "w-preturn", ""), 5, true},
		// A mid-turn one sits between the turn's work before and after it.
		{"mid-turn", compacted("run-1", "auto", "w-midturn", ""), 7, true},
		// A subagent's belongs to its own view.
		{"subagent", compacted("run-2", "auto", "w-midturn", "explorer"), 0, false},
		// One that replaced nothing falls back to the clock, and a pre-turn
		// one still follows the message it was for.
		{"failed pre-turn", event.NewRunEvent("", "", "s", event.RunEventContextCompactError,
			event.ContextCompactFailedPayload{Trigger: "auto", Error: "boom"}, time.Unix(30, 500_000_000)), 5, true},
		{"not a compaction", event.NewRunEvent("", "", "s", event.RunEventTurnError, event.TurnErrorPayload{}, time.Unix(1, 0)), 0, false},
	}
	for _, tc := range cases {
		got, ok := CompactionPosition(turns, tc.evt)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("%s: position = %d, %v; want %d, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

type compactionLogStub struct{ events []state.SessionEvent }

func (s compactionLogStub) ListSessionEventsOfType(_ context.Context, _ string, eventType string, _ int) ([]state.SessionEvent, error) {
	var out []state.SessionEvent
	for _, evt := range s.events {
		if evt.Type == eventType {
			out = append(out, evt)
		}
	}
	return out, nil
}

// TestContextReportReadsLikeTheConversation pins /context: the same gauge
// /status shows, what the last turn brought in besides the conversation, the
// conversation's own compactions (never a subagent's), and what was kept out
// of the window, all in words rather than internal field names.
func TestContextReportReadsLikeTheConversation(t *testing.T) {
	at := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)
	log := compactionLogStub{events: []state.SessionEvent{
		{Type: event.RunEventContextCompacted, Payload: event.EncodePayload(event.ContextCompactedPayload{Trigger: "manual", Strategy: "local"})},
		{Type: event.RunEventContextCompacted, Payload: event.EncodePayload(event.ContextCompactedPayload{Trigger: "auto", AgentID: "explorer"})},
		{Type: event.RunEventContextCompacted, Payload: event.EncodePayload(event.ContextCompactedPayload{
			Trigger: "auto", Strategy: "local", CreatedAtUTC: at.Format(time.RFC3339), TokensBefore: 182_000, TokensAfter: 12_300,
		})},
	}}
	got, err := ConversationCompactions(context.Background(), log, "s")
	if err != nil || len(got) != 2 || got[0].Trigger != "manual" || got[1].Trigger != "auto" {
		t.Fatalf("compactions = %+v err=%v", got, err)
	}
	st := snapshotStub{raw: `{"budget":{"limit_tokens":1800},"provenance":[
		{"source_id":"working_set_source","included":false,"reason":"empty"},
		{"source_id":"recent_files_source","included":true,"reason":"selected","estimated_tokens":1200},
		{"source_id":"git_context_source","included":false,"reason":"budget","estimated_tokens":2400}
	],"tool_result_spills":[{"call_id":"c1"},{"call_id":"c2"}]}`}

	reply := ContextReport(context.Background(), ContextSources{
		Snapshots:   st,
		Compactions: log,
		Gauge:       StatusContext{PercentLeft: 88, UsedTokens: 24_100, WindowTokens: 200_000, AutoCompactAt: 180_000},
	}, "s")

	for _, want := range []string{
		"Context window\n  " + ContextGaugeBar(88) + " 88% left · 24k of 200k\n  Compacts automatically at 180k",
		"Brought into the last turn\n  Files read recently · 1.2k tokens\n  Git status and changes · left out, over the 1.8k-token allowance",
		"Compactions\n  2 compactions in this conversation; the latest, at " + at.Local().Format("Jan 2 15:04") + ", was automatic and took it from 182k to 12k tokens.",
		"Kept out of the window\n  2 tool results too large for the window were saved to files the model reads as needed.",
	} {
		if !strings.Contains(reply, want) {
			t.Fatalf("report misses %q:\n%s", want, reply)
		}
	}
	for _, internal := range []string{"working_set_source", "Pressure", "Items:", "provenance", "{"} {
		if strings.Contains(reply, internal) {
			t.Fatalf("report shows the internal %q:\n%s", internal, reply)
		}
	}
}

// Before any turn there is no snapshot and nothing compacted: the report says
// so in words instead of showing empty counters.
func TestContextReportBeforeTheFirstTurn(t *testing.T) {
	reply := ContextReport(context.Background(), ContextSources{
		Compactions: compactionLogStub{},
		Gauge:       StatusContext{PercentLeft: 100, WindowTokens: 1_000_000, AutoCompactAt: 900_000},
	}, "s")
	want := "Context window\n  Empty so far · 1M\n  Compacts automatically at 900k\n\n" +
		"Brought into the last turn\n  Nothing yet: this is recorded when a turn starts.\n\n" +
		"Compactions\n  None in this conversation yet."
	if reply != want {
		t.Fatalf("report =\n%s\nwant\n%s", reply, want)
	}
}

// snapshotStub folds the compactions it is given into the snapshot the way
// tool.State does.
type snapshotStub struct{ raw string }

func (s snapshotStub) GetContextSnapshot(string) (json.RawMessage, bool) {
	return json.RawMessage(s.raw), true
}

func (s snapshotStub) GetContextSnapshotForRun(_ string, _ string, compactions []event.ContextCompactedPayload) (json.RawMessage, bool) {
	raw := strings.TrimSuffix(s.raw, "}") + `,"context_timeline":[`
	for i := len(compactions) - 1; i >= 0; i-- {
		c := compactions[i]
		raw += `{"kind":"compact","strategy":"` + c.Strategy + `","trigger":"` + c.Trigger + `","reason":"` + c.Reason + `"}`
		if i > 0 {
			raw += ","
		}
	}
	return json.RawMessage(raw + "]}"), true
}

func (s snapshotStub) ClearContextSnapshot(string) {}

// goalTranscript is a /goal turn of two rounds followed by the user's next
// message: the rounds' rows, the continuation prompt the runtime wrote between
// them (meta, which no surface shows), and the times they were stored at.
func goalTranscript(base time.Time) []state.Message {
	continuation := llm.UserMessage(llm.Text("Continue working toward this objective"))
	continuation.IsMeta = true
	row := func(id int64, role string, msg llm.Message, at time.Duration) state.Message {
		return state.Message{RowID: id, Role: role, PartsJSON: state.MessagePartsJSON(msg, msg.TextContent()), CreatedAt: base.Add(at).Unix()}
	}
	return []state.Message{
		row(1, "user", llm.UserMessage(llm.Text("/goal ship")), 0),
		row(2, "assistant", llm.AssistantMessage([]llm.ContentPart{llm.Text("round 1")}), 2*time.Second),
		row(3, "user", continuation, 4*time.Second),
		row(4, "assistant", llm.AssistantMessage([]llm.ContentPart{llm.Text("round 2")}), 6*time.Second),
		row(5, "user", llm.UserMessage(llm.Text("thanks")), 60*time.Second),
	}
}

func TestGoalPositionPlacesEachLineWhereItWasDrawn(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	turns := goalTranscript(base)
	at := func(evt event.RunEvent) int {
		t.Helper()
		pos, ok := GoalPosition(turns, evt)
		if !ok {
			t.Fatalf("%s was not placed", evt.Type)
		}
		return pos
	}
	started := event.NewRunEvent("goal-started:r1", "r1", "s1", event.RunEventGoalStarted, event.GoalStartedPayload{Objective: "ship", AfterRowID: 1}, base)
	if got := at(started); got != 1 {
		t.Fatalf("goal opens before row %d, want right after the message that asked for it", got)
	}
	round := event.NewRunEvent("goal-round:r1:2", "r1", "s1", event.RunEventGoalRoundStarted, event.GoalRoundStartedPayload{Round: 2, AfterRowID: 2}, base.Add(3*time.Second))
	if got := at(round); got != 2 {
		t.Fatalf("round 2 opens before row %d, want after round 1's rows", got)
	}
	completed := event.NewRunEvent("goal-completed:r1", "r1", "s1", event.RunEventGoalCompleted, event.GoalCompletedPayload{Status: event.GoalStatusDone, Rounds: 2, DurationMs: 8_000}, base.Add(8*time.Second))
	if got := at(completed); got != 4 {
		t.Fatalf("goal closes before row %d, want after its last round and before the next message", got)
	}
	// A goal still closing when the transcript ends closes at its end.
	if got, _ := GoalPosition(turns[:4], completed); got != 4 {
		t.Fatalf("goal closes before row %d of a transcript that ends with it", got)
	}
	if _, ok := GoalPosition(turns, event.NewRunEvent("x", "r1", "s1", event.RunEventContextCompacted, event.ContextCompactedPayload{}, base)); ok {
		t.Fatal("GoalPosition placed an event that is not a goal's")
	}
}

func timedRow(runID, role string, workedMs int64) state.Message {
	return state.Message{RunID: runID, Role: role, RunStartedAtMs: 1_000, RunFinishedAtMs: 1_000 + workedMs, RunWorkedMs: workedMs}
}

// Each run closes after the last row it wrote, whatever that row's role and
// whatever unbound rows sit between its own; a run that never ended closes
// nothing.
func TestRunWorkedLinesCloseEachRunAfterItsLastRow(t *testing.T) {
	turns := []state.Message{
		timedRow("r1", "user", 3_000),
		{Role: "assistant"}, // written at an approval gate, before rows were bound
		timedRow("r1", "assistant", 3_000),
		timedRow("r1", "tool", 3_000),
		// A run that failed before it wrote anything: only its message.
		timedRow("r2", "user", 1_500),
		// A run still going.
		{RunID: "r3", Role: "user"},
		{Role: "user"},
	}
	lines := RunWorkedLines(turns)
	if len(lines) != 2 {
		t.Fatalf("lines = %+v, want one per ended run", lines)
	}
	if got := lines[3]; got.RunID != "r1" || got.Worked != 3*time.Second || got.FinishedAt != time.UnixMilli(4_000) {
		t.Fatalf("r1 line = %+v", got)
	}
	if got, ok := lines[4]; !ok || got.RunID != "r2" || got.Worked != 1500*time.Millisecond {
		t.Fatalf("r2 line = %+v %v, want it after the run's only row", got, ok)
	}
	if _, ok := lines[0]; ok {
		t.Fatal("a run closes after its last row, not its first")
	}
}
