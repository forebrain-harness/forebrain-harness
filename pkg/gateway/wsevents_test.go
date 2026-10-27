package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestCanonicalRunEventFromWS(t *testing.T) {
	tests := []struct {
		name      string
		msg       wsServerMsg
		wantType  string
		wantOK    bool
		needField string
		wantValue any
	}{
		{
			name:     "run started",
			msg:      wsServerMsg{Op: "run_started", RunID: "r1", SessionID: "s1"},
			wantType: event.RunEventTurnStarted,
			wantOK:   true,
		},
		{
			name:      "run completed carries text and elapsed time",
			msg:       wsServerMsg{Op: "run_completed", Text: "done", Data: map[string]any{"elapsed_ms": int64(1250)}},
			wantType:  event.RunEventTurnCompleted,
			wantOK:    true,
			needField: "elapsed_ms",
			wantValue: float64(1250),
		},
		{
			name:      "requires action carries message",
			msg:       wsServerMsg{Op: "requires_action", Data: map[string]any{"id": "a1"}, Message: "Awaiting approval"},
			wantType:  event.RunEventApprovalReq,
			wantOK:    true,
			needField: "message",
			wantValue: "Awaiting approval",
		},
		{
			name:      "requires action names the gated call",
			msg:       wsServerMsg{Op: "requires_action", Data: map[string]any{"id": "a1", "kind": "exit_plan_mode", "tool_step_id": "call-7"}},
			wantType:  event.RunEventApprovalReq,
			wantOK:    true,
			needField: "tool_step_id",
			wantValue: "call-7",
		},
		{
			name: "requires action carries permission suggestion",
			msg: wsServerMsg{
				Op: "requires_action",
				Data: map[string]any{
					"id":   "a1",
					"kind": "shell",
					"permission_suggestion": map[string]any{
						"permission_tool_name": "Bash",
						"permission_input":     "npm run build",
						"exact_rule_content":   "npm run build",
						"prefix_rule_content":  "npm run:*",
					},
				},
				Message: "Awaiting approval",
			},
			wantType:  event.RunEventApprovalReq,
			wantOK:    true,
			needField: "permission_suggestion",
			wantValue: nil,
		},
		{
			name: "mode changed",
			msg: wsServerMsg{
				Op:   "mode_changed",
				Data: map[string]any{"mode": "agent", "phase": "execute"},
			},
			wantType:  event.RunEventModeChanged,
			wantOK:    true,
			needField: "mode",
			wantValue: "agent",
		},
		{
			name: "pending input updated",
			msg: wsServerMsg{
				Op: "pending_input_updated",
				Data: map[string]any{
					"pending_steers":  []string{"steer"},
					"rejected_steers": []string{"later"},
					"queued_messages": []string{"follow"},
				},
			},
			wantType:  event.RunEventPendingInputUpdated,
			wantOK:    true,
			needField: "queued_messages",
			wantValue: nil,
		},
		{
			name: "step result",
			msg: wsServerMsg{
				Op: "step",
				Data: map[string]any{
					"kind":             event.RunEventToolCompleted,
					"step_id":          "step-1",
					"description":      "read file",
					"duration_seconds": 1.5,
					"data":             "done",
					"tool_name":        "read_file",
					"display_body":     "body",
				},
			},
			wantType:  event.RunEventToolCompleted,
			wantOK:    true,
			needField: "tool_name",
			wantValue: "read_file",
		},
		{
			name: "step output delta",
			msg: wsServerMsg{
				Op: "step",
				Data: map[string]any{
					"kind": event.RunEventToolOutputDelta, "step_id": "step-1", "tool_name": "shell", "content": "partial",
				},
			},
			wantType:  event.RunEventToolOutputDelta,
			wantOK:    true,
			needField: "text",
			wantValue: "partial",
		},
		{
			name: "step result emits turn diff event",
			msg: wsServerMsg{
				Op: "step",
				Data: map[string]any{
					"kind":      event.RunEventToolCompleted,
					"step_id":   "step-2",
					"tool_name": "edit_file",
					"data":      `{"output":{"turn_diff":{"path":"a.go","added":2,"deleted":1,"unified_diff":"@@"}}}`,
				},
			},
			wantType:  event.RunEventToolCompleted,
			wantOK:    true,
			needField: "",
		},
		{
			name:     "unknown op",
			msg:      wsServerMsg{Op: "heartbeat"},
			wantType: "",
			wantOK:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := canonicalRunEventFromWS(tc.msg)
			if ok != tc.wantOK {
				t.Fatalf("want ok=%v, got %v", tc.wantOK, ok)
			}
			if !tc.wantOK {
				return
			}
			if got.Type != tc.wantType {
				t.Fatalf("want type=%q, got %q", tc.wantType, got.Type)
			}
			if tc.name == "step result emits turn diff event" {
				events := canonicalRunEventsFromWS(tc.msg)
				if len(events) != 2 || events[1].Type != event.DiffEventTurnUpdated {
					t.Fatalf("expected turn diff companion event, got %+v", events)
				}
			}
			if tc.needField != "" {
				var p map[string]any
				if err := json.Unmarshal(got.Payload, &p); err != nil {
					t.Fatalf("unmarshal payload: %v", err)
				}
				if gotValue, exists := p[tc.needField]; !exists {
					t.Fatalf("expected payload key %q in %#v", tc.needField, p)
				} else if tc.wantValue != nil && gotValue != tc.wantValue {
					t.Fatalf("expected payload[%q]=%#v, got %#v", tc.needField, tc.wantValue, gotValue)
				} else if tc.needField == "permission_suggestion" {
					ps, ok := gotValue.(map[string]any)
					if !ok {
						t.Fatalf("unexpected permission_suggestion payload: %#v", gotValue)
					}
					if ps["prefix_rule_content"] != "npm run:*" {
						t.Fatalf("unexpected prefix rule content: %#v", ps["prefix_rule_content"])
					}
				} else if tc.wantType == event.CompactEventContextCompacted {
					if p["strategy"] != "remote_v2" || p["summary_source"] != "remote_compaction" {
						t.Fatalf("unexpected compact payload: %#v", p)
					}
					if p["boundary_id"] != "window-2" || p["window_number"] != float64(2) {
						t.Fatalf("checkpoint fields dropped: %#v", p)
					}
				}
			}
		})
	}
}

func TestRunEventWSMessageAndPayloadFallbacks(t *testing.T) {
	src := wsServerMsg{RequestID: "req", RunID: "run", SessionID: "s", TraceID: "trace"}
	evt := event.NewRunEvent("evt", "run", "s", event.RunEventModeChanged, event.ModeChangedPayload{Mode: "agent"}, time.Now())
	msg := runEventWSMessage(src, evt)
	if msg.Op != "run_event" || msg.RequestID != "req" || msg.TraceID != "trace" || msg.Data == nil {
		t.Fatalf("ws msg=%+v", msg)
	}
	if raw := mustJSONRaw(map[string]any{"x": "y"}); len(raw) == 0 {
		t.Fatal("expected json raw")
	}
	if raw := mustJSONRaw(func() {}); raw != nil {
		t.Fatalf("unsupported json raw=%s", raw)
	}
	if got := stringOr(123); got != "" {
		t.Fatalf("stringOr non-string=%q", got)
	}
	if got := int64Field(map[string]any{"x": int32(3)}, "x"); got != 3 {
		t.Fatalf("int32 field=%d", got)
	}
	if got := int64Field(map[string]any{"x": float32(4.9)}, "x"); got != 4 {
		t.Fatalf("float32 field=%d", got)
	}
	if got := float64OrZero(int32(7)); got != 7 {
		t.Fatalf("float64 int32=%f", got)
	}
	if _, ok := modeChangedPayload("bad"); ok {
		t.Fatal("non-map mode payload should not parse")
	}
}

func TestPlanUpdatedUsesRunEventWSMessage(t *testing.T) {
	src := wsServerMsg{RequestID: "req", RunID: "run", SessionID: "s", TraceID: "trace"}
	payload := event.PlanUpdatedPayload{
		Title:       "Updated Plan",
		Explanation: "Writing tests",
		Completed:   1,
		Total:       2,
		Items: []event.PlanUpdateItem{
			{ID: "1", Content: "Write tests", Status: "in_progress", Active: "Writing tests"},
		},
	}
	evt := event.NewRunEvent("evt-plan", "run", "s", event.RunEventPlanUpdated, payload, time.Now())
	msg := runEventWSMessage(src, evt)
	if msg.Op != "run_event" || msg.RequestID != "req" || msg.TraceID != "trace" || msg.Data == nil {
		t.Fatalf("ws msg=%+v", msg)
	}
	raw, err := json.Marshal(msg.Data)
	if err != nil {
		t.Fatalf("marshal run event: %v", err)
	}
	var got event.RunEvent
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal run event: %v", err)
	}
	if got.Type != event.RunEventPlanUpdated {
		t.Fatalf("event type=%q want %q", got.Type, event.RunEventPlanUpdated)
	}
	var gotPayload event.PlanUpdatedPayload
	if err := json.Unmarshal(got.Payload, &gotPayload); err != nil {
		t.Fatalf("unmarshal plan payload: %v", err)
	}
	if gotPayload.Explanation != "Writing tests" || gotPayload.Completed != 1 || gotPayload.Total != 2 {
		t.Fatalf("unexpected plan payload: %+v", gotPayload)
	}
}

// TestBudgetEventRidesTheRunEventFrame pins how the web receives the
// conversation's turn-end budget now that the engine files it: not a
// dedicated socket op, but the same run_event frame every canonical event
// rides, so the page's existing projection is the only consumer.
func TestBudgetEventRidesTheRunEventFrame(t *testing.T) {
	src := wsServerMsg{RequestID: "req", RunID: "run", SessionID: "s", TraceID: "trace"}
	payload := event.TokenBudgetUpdatedPayload{
		Model: "glm-5.3", TokenUsage: 152500, PercentLeft: 83,
		ContextWindow: 1000000, EffectiveWindow: 1000000, AutoCompactThreshold: 900000,
	}
	evt := event.NewRunEvent("evt-budget", "run", "s", event.CompactEventBudgetUpdated, payload, time.Now())
	msg := runEventWSMessage(src, evt)
	if msg.Op != "run_event" || msg.RequestID != "req" || msg.TraceID != "trace" || msg.Data == nil {
		t.Fatalf("ws msg=%+v", msg)
	}
	raw, err := json.Marshal(msg.Data)
	if err != nil {
		t.Fatalf("marshal run event: %v", err)
	}
	var got event.RunEvent
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal run event: %v", err)
	}
	if got.Type != event.CompactEventBudgetUpdated {
		t.Fatalf("event type=%q want %q", got.Type, event.CompactEventBudgetUpdated)
	}
	var gotPayload event.TokenBudgetUpdatedPayload
	if err := json.Unmarshal(got.Payload, &gotPayload); err != nil {
		t.Fatalf("unmarshal budget payload: %v", err)
	}
	if gotPayload != payload {
		t.Fatalf("budget payload=%+v want %+v", gotPayload, payload)
	}
}

func TestCanonicalRunEventsFromWSEmitsTurnDiffEvent(t *testing.T) {
	events := canonicalRunEventsFromWS(wsServerMsg{
		Op: "step",
		Data: map[string]any{
			"kind":             event.RunEventToolCompleted,
			"step_id":          "write_file-1",
			"description":      "tool write_file",
			"duration_seconds": 0,
			"data":             `{"output":{"turn_diff":{"path":"/tmp/demo.txt","added":1,"deleted":1,"unified_diff":"--- a\n+++ b\n@@ -1 +1 @@\n-old\n+new\n"}},"error":""}`,
		},
	})
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != event.RunEventToolCompleted {
		t.Fatalf("unexpected tool event type: %q", events[0].Type)
	}
	if events[1].Type != event.DiffEventTurnUpdated {
		t.Fatalf("unexpected diff event type: %q", events[1].Type)
	}
}

// TestCanonicalRunEventsKeepTheSubagentCallFacts pins the conversion hop a
// live web request's tool step takes: the ws "step" op becomes the canonical
// run event the page's cards are built from, and the subagent_* card facts
// (plan 012) ride its tool_meta. A rebuild that drops them leaves the web
// rendering a failed subagent_run as a generic tool card instead of the
// subagent card the terminal shows from the same event.
func TestCanonicalRunEventsKeepTheSubagentCallFacts(t *testing.T) {
	events := canonicalRunEventsFromWS(wsServerMsg{
		Op: "step",
		Data: map[string]any{
			"kind":        event.RunEventToolCompleted,
			"step_id":     "call-1",
			"description": "tool subagent_run",
			"tool_name":   "subagent_run",
			"error":       "unexpected EOF",
			"tool_meta": map[string]any{
				"tool_name":  "subagent_run",
				"status":     "failed",
				"invocation": "run Summarize README.md",
				"subagent_call": map[string]any{
					"verb": "run",
					"tasks": []any{map[string]any{
						"index":      0,
						"title":      "Summarize README.md",
						"agent_type": "general-purpose",
						"status":     "failed",
					}},
				},
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	var completed event.ToolCallCompletedPayload
	if err := json.Unmarshal(events[0].Payload, &completed); err != nil {
		t.Fatalf("decode completed payload: %v", err)
	}
	call := completed.ToolMeta.SubagentCall
	if call == nil {
		t.Fatalf("canonical tool event lost the subagent_call facts: %+v", completed.ToolMeta)
	}
	if call.Verb != "run" || len(call.Tasks) != 1 || call.Tasks[0].AgentType != "general-purpose" || call.Tasks[0].Status != "failed" {
		t.Fatalf("subagent_call facts came back wrong: %+v", call)
	}
}

// TestCanonicalRunEventsKeepTheDenyFeedback pins the conversion hop a refused
// primary-agent call's step takes on the web: the gateway writes the ws "step"
// op, canonicalRunEventsFromWS rebuilds the canonical event from it, and the
// refusal's own words must ride its tool_meta. A rebuild that drops them
// leaves the web card showing the refusal without the sentence the user wrote
// (plan 154).
func TestCanonicalRunEventsKeepTheDenyFeedback(t *testing.T) {
	events := canonicalRunEventsFromWS(wsServerMsg{
		Op: "step",
		Data: map[string]any{
			"kind":        event.RunEventToolCompleted,
			"step_id":     "call-1",
			"description": "tool shell",
			"tool_name":   "shell",
			"tool_meta": map[string]any{
				"tool_name":     "shell",
				"status":        "denied",
				"deny_feedback": "改成移到 trash/",
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	var completed event.ToolCallCompletedPayload
	if err := json.Unmarshal(events[0].Payload, &completed); err != nil {
		t.Fatalf("decode completed payload: %v", err)
	}
	if completed.ToolMeta.DenyFeedback != "改成移到 trash/" {
		t.Fatalf("canonical tool meta lost the refusal's words: %+v", completed.ToolMeta)
	}
}

func TestRunEventBusDeliversOnlyToTheBoundSession(t *testing.T) {
	bus := newRunEventBus()
	mine := make(chan event.RunEvent, 4)
	theirs := make(chan event.RunEvent, 4)

	subMine := bus.Subscribe(func(evt event.RunEvent) { mine <- evt })
	defer subMine.Close()
	subTheirs := bus.Subscribe(func(evt event.RunEvent) { theirs <- evt })
	defer subTheirs.Close()

	// An unbound connection is watching nothing yet.
	require.NoError(t, bus.Publish(context.Background(), event.NewRunEvent("", "run-1", "session-a", event.RunEventAssistantDelta, event.AssistantDeltaPayload{Text: "before bind"}, time.Now())))
	require.Len(t, mine, 0)

	subMine.Bind("session-a")
	subTheirs.Bind("session-b")
	require.NoError(t, bus.Publish(context.Background(), event.NewRunEvent("", "run-1", "session-a", event.RunEventAssistantDelta, event.AssistantDeltaPayload{Text: "for a", AgentID: "task-1"}, time.Now())))

	select {
	case got := <-mine:
		require.Equal(t, "session-a", got.SessionID)
	default:
		t.Fatal("the connection bound to session-a received nothing")
	}
	require.Len(t, theirs, 0, "another session's connection must not see these events")

	// An event that names no session cannot be routed and must not be
	// broadcast to every connection instead.
	require.NoError(t, bus.Publish(context.Background(), event.NewRunEvent("", "run-1", "", event.RunEventAssistantDelta, event.AssistantDeltaPayload{Text: "unattributed"}, time.Now())))
	require.Len(t, mine, 0)
	require.Len(t, theirs, 0)

	subMine.Close()
	require.NoError(t, bus.Publish(context.Background(), event.NewRunEvent("", "run-1", "session-a", event.RunEventAssistantDelta, event.AssistantDeltaPayload{Text: "after close"}, time.Now())))
	require.Len(t, mine, 0)
}

func TestRunEventSubscriptionBuffersSnapshotTailInSequenceOrder(t *testing.T) {
	bus := newRunEventBus()
	var got []int64
	sub := bus.Subscribe(func(evt event.RunEvent) { got = append(got, evt.Sequence) })
	defer sub.Close()
	sub.BindBuffered("session-a")

	for _, sequence := range []int64{4, 3, 2} {
		evt := event.NewRunEvent(fmt.Sprintf("evt-%d", sequence), "run-1", "session-a", event.RunEventAssistantDelta, nil, time.Now())
		evt.Sequence = sequence
		require.NoError(t, bus.Publish(context.Background(), evt))
	}
	require.Empty(t, got)
	sub.ResumeAfter(2)
	require.Equal(t, []int64{3, 4}, got)

	evt := event.NewRunEvent("evt-5", "run-1", "session-a", event.RunEventAssistantDelta, nil, time.Now())
	evt.Sequence = 5
	require.NoError(t, bus.Publish(context.Background(), evt))
	require.Equal(t, []int64{3, 4, 5}, got)
}

func TestRunEventSubscriptionOverflowRequiresResyncAndDoesNotStayPaused(t *testing.T) {
	bus := newRunEventBus()
	var got []int64
	sub := bus.Subscribe(func(evt event.RunEvent) { got = append(got, evt.Sequence) })
	defer sub.Close()
	sub.BindBuffered("session-a")

	for sequence := int64(1); sequence <= runEventBufferedTailLimit+1; sequence++ {
		evt := event.NewRunEvent(fmt.Sprintf("evt-%d", sequence), "run-1", "session-a", event.RunEventAssistantDelta, nil, time.Now())
		evt.Sequence = sequence
		require.NoError(t, bus.Publish(context.Background(), evt))
	}
	require.False(t, sub.ResumeAfter(0), "overflow must force a durable-cursor resync")
	require.Empty(t, got, "an incomplete buffered tail must never be presented as complete")

	// ResumeAfter releases the paused state even on overflow. The websocket
	// owner closes this connection immediately; proving the release here keeps
	// a failed handoff from leaking an ever-growing paused subscription.
	next := event.NewRunEvent("evt-next", "run-1", "session-a", event.RunEventAssistantDelta, nil, time.Now())
	next.Sequence = runEventBufferedTailLimit + 2
	require.NoError(t, bus.Publish(context.Background(), next))
	require.Equal(t, []int64{runEventBufferedTailLimit + 2}, got)
}

func TestRunEventSubscriptionFailedSnapshotDiscardsTailAndUnbinds(t *testing.T) {
	bus := newRunEventBus()
	var got []int64
	sub := bus.Subscribe(func(evt event.RunEvent) { got = append(got, evt.Sequence) })
	defer sub.Close()
	sub.BindBuffered("session-a")

	buffered := event.NewRunEvent("evt-buffered", "run-1", "session-a", event.RunEventAssistantDelta, nil, time.Now())
	buffered.Sequence = 2
	require.NoError(t, bus.Publish(context.Background(), buffered))
	sub.Unbind()

	live := event.NewRunEvent("evt-live", "run-1", "session-a", event.RunEventAssistantDelta, nil, time.Now())
	live.Sequence = 3
	require.NoError(t, bus.Publish(context.Background(), live))
	require.Empty(t, got, "a failed snapshot cannot expose either its buffered suffix or later live events")
}

// The web surface learns what a subagent is doing the same way the terminal
// does: from the runtime's run-event stream. This is the wire that carries it —
// a subagent's assistant text, tagged with its roster key, reaching the browser
// while the turn is still running, so the surface can file it under that
// subagent instead of appending it to the primary answer.
func TestHandleChatWSStreamsSubagentRunEvents(t *testing.T) {
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()

	s := &Server{Home: home, Sessions: state.NewSessionStore(db, "main")}
	require.NoError(t, s.Sessions.Ensure(context.Background(), "session-ws", "session-ws"))
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()

	var connected wsServerMsg
	require.NoError(t, conn.ReadJSON(&connected))
	require.Equal(t, "connected", connected.Op)

	require.NoError(t, conn.WriteJSON(wsClientMsg{Op: "bind_session", RequestID: "req-bind", SessionID: "session-ws"}))
	var bound wsServerMsg
	require.NoError(t, conn.ReadJSON(&bound))
	require.Equal(t, "session_bound", bound.Op)

	// What the runtime publishes while a subagent works.
	for _, evt := range []event.RunEvent{
		event.NewRunEvent("", "parent-run", "session-ws", event.RunEventSubagentSpawned, event.SubagentSpawnedPayload{
			AgentID: "task-explore-1", AgentType: "explore", TaskID: "task-explore-1", Task: "find the callers",
		}, time.Now()),
		event.NewRunEvent("", "child-run", "session-ws", event.RunEventAssistantDelta, event.AssistantDeltaPayload{
			Text: "Three callers, all in pkg/run.", AgentID: "task-explore-1",
		}, time.Now()),
	} {
		require.NoError(t, s.RunEvents().Publish(context.Background(), evt))
	}

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	spawned := readRunEventOfType(t, conn, event.RunEventSubagentSpawned)
	var spawnPayload event.SubagentSpawnedPayload
	require.NoError(t, json.Unmarshal(spawned.Payload, &spawnPayload))
	require.Equal(t, "task-explore-1", spawnPayload.AgentID)
	require.Equal(t, "find the callers", spawnPayload.Task)

	delta := readRunEventOfType(t, conn, event.RunEventAssistantDelta)
	var deltaPayload event.AssistantDeltaPayload
	require.NoError(t, json.Unmarshal(delta.Payload, &deltaPayload))
	require.Equal(t, "task-explore-1", deltaPayload.AgentID,
		"the subagent's text must reach the browser tagged, or the web surface cannot tell it apart from the primary answer")
	require.Equal(t, "Three callers, all in pkg/run.", deltaPayload.Text)
}

// A subagent execution's first-token wait reaches the browser on the same
// first_token op the conversation's own wait uses, with the agent id naming
// whose execution waited — and the push-only event itself never travels as a
// run_event, exactly like the conversation's wait it stays out of the session
// log.
func TestFirstTokenPushCarriesAgentIDForSubagents(t *testing.T) {
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()

	s := &Server{Home: home, Sessions: state.NewSessionStore(db, "main")}
	require.NoError(t, s.Sessions.Ensure(context.Background(), "session-ws", "session-ws"))
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()

	var connected wsServerMsg
	require.NoError(t, conn.ReadJSON(&connected))
	require.Equal(t, "connected", connected.Op)

	require.NoError(t, conn.WriteJSON(wsClientMsg{Op: "bind_session", RequestID: "req-bind", SessionID: "session-ws"}))
	var bound wsServerMsg
	require.NoError(t, conn.ReadJSON(&bound))
	require.Equal(t, "session_bound", bound.Op)

	// What the engine stamps when a subagent's model first begins answering.
	require.NoError(t, s.RunEvents().Publish(context.Background(), event.NewRunEvent("",
		"child-run", "session-ws", event.RunEventFirstToken,
		event.FirstTokenPayload{AgentID: "task-ft-1", FirstTokenMs: 1234}, time.Now())))

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	var frame wsServerMsg
	for {
		require.NoError(t, conn.ReadJSON(&frame))
		if frame.Op == "first_token" {
			break
		}
		if frame.Op == "run_event" {
			t.Fatalf("the subagent's first-token wait must ride the first_token op, not a run_event frame: %#v", frame)
		}
	}
	require.Equal(t, "child-run", frame.RunID)
	require.Equal(t, "session-ws", frame.SessionID)
	data, ok := frame.Data.(map[string]any)
	require.True(t, ok, "first_token data = %#v", frame.Data)
	require.Equal(t, float64(1234), data["first_token_ms"])
	require.Equal(t, "task-ft-1", data["agent_id"],
		"without the agent id the client would file the wait under the conversation instead of the subagent's execution")
}

func TestHandleChatWSRejectsSessionOwnedByAnotherPrimaryAgent(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, state.NewSessionStore(db, "other-agent").Ensure(ctx, "private-session", "private"))
	s := &Server{Home: home, Sessions: state.NewSessionStore(db, "current-agent")}
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	var connected wsServerMsg
	require.NoError(t, conn.ReadJSON(&connected))
	require.Equal(t, "connected", connected.Op)
	require.NoError(t, conn.WriteJSON(wsClientMsg{Op: "bind_session", RequestID: "cross-bind", SessionID: "private-session"}))
	var rejected wsServerMsg
	require.NoError(t, conn.ReadJSON(&rejected))
	require.Equal(t, "run_warning", rejected.Op)
	require.Equal(t, "session_event_snapshot_unavailable", rejected.Message)

	// A rejected bind must not leave either canonical or legacy observers
	// attached to the private conversation.
	require.NoError(t, s.RunEvents().Publish(ctx, event.NewRunEvent("private-event", "run-private", "private-session", event.RunEventAssistantDelta, event.AssistantDeltaPayload{Text: "secret"}, time.Now())))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(100*time.Millisecond)))
	var leaked wsServerMsg
	err = conn.ReadJSON(&leaked)
	require.Error(t, err)
}

// A resume choice names a session id the client sent, so the switch it asks
// for is refused when the conversation belongs to another primary agent: the
// socket never binds to it, that conversation's run events stay unseen, and
// this agent's own conversation keeps arriving.
func TestHandleChatWSResumeChoiceOfAnotherAgentsSessionIsRefused(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, state.NewSessionStore(db, "other-agent").Ensure(ctx, "their-session", "their-session"))
	s := &Server{Home: home, Sessions: state.NewSessionStore(db, "main")}
	require.NoError(t, s.Sessions.Ensure(ctx, "my-session", "my-session"))
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	var connected wsServerMsg
	require.NoError(t, conn.ReadJSON(&connected))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))

	choice := turn.SlashChoice{Command: "resume", Value: "their-session"}
	require.NoError(t, conn.WriteJSON(wsClientMsg{
		RequestID: "req-resume-theirs",
		SessionID: "my-session",
		Message:   wsClientMessage{Content: "/resume", Choice: &choice},
	}))
	for {
		var msg wsServerMsg
		require.NoError(t, conn.ReadJSON(&msg), "a message may stop arriving, never carry another agent's session: %+v", msg)
		require.NotEqual(t, "their-session", msg.SessionID, "the socket adopted another agent's session: %+v", msg)
		if msg.Op == "slash_reply" && msg.RequestID == "req-resume-theirs" {
			require.Contains(t, msg.Text, "another primary agent")
			break
		}
	}

	// The refused switch left the socket on its own conversation: events
	// published for the other agent's session never arrive, its own still do.
	require.NoError(t, s.RunEvents().Publish(ctx, event.NewRunEvent("", "their-run", "their-session", event.RunEventAssistantDelta, event.AssistantDeltaPayload{Text: "secret"}, time.Now())))
	require.NoError(t, s.RunEvents().Publish(ctx, event.NewRunEvent("", "my-run", "my-session", event.RunEventAssistantDelta, event.AssistantDeltaPayload{Text: "still mine"}, time.Now())))
	for {
		var msg wsServerMsg
		require.NoError(t, conn.ReadJSON(&msg))
		if msg.Op != "run_event" {
			continue
		}
		require.NotEqual(t, "their-session", msg.SessionID, "another agent's run event reached this socket: %+v", msg)
		if msg.SessionID == "my-session" {
			break
		}
	}
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(150*time.Millisecond)))
	var leaked wsServerMsg
	require.Error(t, conn.ReadJSON(&leaked), "nothing more may arrive, least of all another agent's event")
}

func readRunEventOfType(t *testing.T, conn *websocket.Conn, eventType string) event.RunEvent {
	t.Helper()
	for {
		var msg wsServerMsg
		require.NoError(t, conn.ReadJSON(&msg))
		if msg.Op != "run_event" {
			continue
		}
		raw, err := json.Marshal(msg.Data)
		require.NoError(t, err)
		var evt event.RunEvent
		require.NoError(t, json.Unmarshal(raw, &evt))
		if evt.Type == eventType {
			return evt
		}
	}
}

// codexUsageLimitBody is a ChatGPT-subscription 429 body as it reaches
// llm.APIError once the provider-error normalizer has run.
const codexUsageLimitBody = `{"eligible_promo":null,"message":"{\"error\":{\"type\":\"usage_limit_reached\",\"message\":\"The usage limit has been reached\",\"plan_type\":\"plus\",\"resets_at\":1788543035,\"eligible_promo\":null,\"resets_in_seconds\":8899}}","plan_type":"plus","resets_at":1788543035,"resets_in_seconds":8899,"type":"usage_limit_reached"}`

func codexUsageLimitError() error {
	return &llm.APIError{
		StatusCode: http.StatusTooManyRequests,
		Type:       "usage_limit_reached",
		Message:    codexUsageLimitBody,
		Err:        errors.New(`POST "https://chatgpt.com/backend-api/codex/responses": 429 Too Many Requests ` + codexUsageLimitBody),
		RateLimit: llm.ParseRateLimit(
			http.StatusTooManyRequests,
			codexUsageLimitBody,
			"",
			time.Unix(1788543035-8899, 0),
		),
	}
}

// The webchat writes its own wording, so the failure has to reach it as facts
// and not only as the English sentence the runtime rendered.
func TestNewTurnErrorDetailClassifiesProviderRefusal(t *testing.T) {
	detail := newTurnErrorDetail(codexUsageLimitError())
	require.NotNil(t, detail)
	require.Equal(t, string(llm.ExplainRateLimitQuota), detail.Code)
	require.Equal(t, http.StatusTooManyRequests, detail.Status)
	require.Equal(t, "plus", detail.Plan)
	require.Equal(t, "The usage limit has been reached", detail.ProviderMessage)
	require.Equal(t, time.Unix(1788543035, 0).UTC().Format(time.RFC3339), detail.ResetAt)
	require.Equal(t, 8899, detail.RetryAfterSeconds)
}

// A failure the provider layer cannot classify has no facts to send; the
// runtime's own sentence is all a surface can show, and inventing a code for it
// would make the web UI phrase a failure it does not understand.
func TestNewTurnErrorDetailIsNilForOrdinaryErrors(t *testing.T) {
	require.Nil(t, newTurnErrorDetail(errors.New("user prompt hook failed")))
	require.Nil(t, newTurnErrorDetail(nil))
}

func TestCanonicalRunErrorCarriesTurnErrorDetail(t *testing.T) {
	detail := newTurnErrorDetail(codexUsageLimitError())
	got, ok := canonicalRunEventFromWS(wsServerMsg{
		Op:    "run_error",
		Error: "Usage limit reached on your plus plan",
		Data:  detail,
	})
	require.True(t, ok)
	require.Equal(t, event.RunEventTurnError, got.Type)

	var payload event.TurnErrorPayload
	require.NoError(t, json.Unmarshal(got.Payload, &payload))
	require.Equal(t, "Usage limit reached on your plus plan", payload.Error)
	require.NotNil(t, payload.Detail)
	require.Equal(t, "rate_limit_quota", payload.Detail.Code)
	require.Equal(t, "plus", payload.Detail.Plan)
	require.Equal(t, 8899, payload.Detail.RetryAfterSeconds)
}

// The helpers below were production functions that only the tests in this
// package ever called. They live here so the production files carry no
// unused code while the tests keep exercising the live code they wrap.

func canonicalRunEventFromWS(m wsServerMsg) (event.RunEvent, bool) {
	events := canonicalRunEventsFromWS(m)
	if len(events) == 0 {
		return event.RunEvent{}, false
	}
	return events[0], true
}

func runEventWSMessage(src wsServerMsg, evt event.RunEvent) wsServerMsg {
	return wsServerMsg{
		Op:        "run_event",
		RequestID: src.RequestID,
		RunID:     src.RunID,
		SessionID: src.SessionID,
		TraceID:   src.TraceID,
		Data:      evt,
	}
}

func mustJSONRaw(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return json.RawMessage(b)
}

// TestMCPStatusOpIsNeverPersisted pins the guarantee that makes MCP status safe
// to push at any rate: it is live state, so it reaches the socket and never the
// session's event log.
//
// The property is structural — canonicalRunEventsFromWS is a whitelist of
// inbound ops — but it is asserted for the outbound op too, because "we would
// never persist this" is the kind of claim that quietly stops being true when
// somebody reuses an existing op name for a new notification.
func TestMCPStatusOpIsNeverPersisted(t *testing.T) {
	msgs := []wsServerMsg{
		{Op: "mcp_status_event", SessionID: "s1", Data: mcpStatusEventData(run.MCPSnapshot{})},
		{
			Op:        "mcp_status_event",
			SessionID: "s1",
			Data: mcpStatusEventData(run.MCPSnapshot{
				Generation: "gen-1",
				Pending:    true,
				Servers: []mcp.ServerRecord{{
					Name:       "docs",
					ConnStatus: mcp.ConnStatusError,
					Error:      "boom",
				}},
			}),
		},
	}
	for _, msg := range msgs {
		if events := canonicalRunEventsFromWS(msg); len(events) != 0 {
			t.Fatalf("op %q produced %d canonical run events; live state must not enter the transcript",
				msg.Op, len(events))
		}
	}
	// The op is registered as a live-state op, which is what makes the assertion
	// above hold rather than merely being the current behaviour.
	if _, ok := wsOutboundOps["mcp_status_event"]; !ok {
		t.Fatal("mcp_status_event must be declared as a live-state op")
	}
	// And it is a distinct op from the inbound request: a client matching a reply
	// to its own request cannot tell one name from the other.
	if _, ok := wsInboundOps["mcp_status_event"]; ok {
		t.Fatal("the outbound status op must not also be an inbound request op")
	}
	if _, ok := wsOutboundOps["mcp_status"]; ok {
		t.Fatal("mcp_status is the inbound request; the notification needs its own name")
	}
}

// TestMCPStatusEventPayloadCarriesTheFourStates pins the notification's shape:
// the same state names the REST projection uses, in configuration order, with a
// failed server's own text.
func TestMCPStatusEventPayloadCarriesTheFourStates(t *testing.T) {
	data := mcpStatusEventData(run.MCPSnapshot{
		Generation: "gen-1",
		Pending:    true,
		Servers: []mcp.ServerRecord{
			{Name: "docs", ConnStatus: mcp.ConnStatusConnected, ToolCount: 3},
			{Name: "slow", ConnStatus: mcp.ConnStatusConnecting, Required: true},
			{Name: "skip", ConnStatus: mcp.ConnStatusCancelled},
			{Name: "broken", ConnStatus: mcp.ConnStatusError, Error: "spawn npx: not found"},
		},
	})
	servers, ok := data["servers"].([]map[string]any)
	if !ok || len(servers) != 4 {
		t.Fatalf("servers = %#v", data["servers"])
	}
	want := []string{"connected", "connecting", "cancelled", "error"}
	for i, row := range servers {
		if got := row["conn_status"]; got != want[i] {
			t.Fatalf("servers[%d].conn_status = %v, want %q", i, got, want[i])
		}
	}
	if servers[1]["required"] != true {
		t.Fatal("a required server must be marked required")
	}
	if servers[3]["error"] != "spawn npx: not found" {
		t.Fatalf("the failure text must travel verbatim: %v", servers[3]["error"])
	}
	if data["generation"] != "gen-1" || data["pending"] != true {
		t.Fatalf("envelope = %#v", data)
	}
}

// TestHandleChatWSCompactStreamsItsLifecycleAndCanBeCancelled pins the web's
// /compact end to end: the socket that sent it hears the compaction start
// while it runs — it listens before the command starts, not after — and a
// cancel_command on that same socket, read while the command still holds the
// loop, stops it and ends the card as cancelled.
func TestHandleChatWSCompactStreamsItsLifecycleAndCanBeCancelled(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()

	entered := make(chan struct{})
	testOver := make(chan struct{})
	var once sync.Once
	summarizer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reading the body lets the server notice the client hanging up.
		_, _ = io.ReadAll(r.Body)
		once.Do(func() { close(entered) })
		select {
		case <-r.Context().Done():
		case <-testOver:
		}
	}))
	defer summarizer.Close()
	// A failed assertion must not leave the held summary request blocking the
	// server's Close.
	defer close(testOver)

	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(ctx, "session-ws", "session-ws"))
	_, err = sessions.Append(ctx, "session-ws", "user", "a question worth compacting")
	require.NoError(t, err)
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{"main": {
		Primary:      true,
		LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "gpt-main", APIKey: "test-key", BaseURL: summarizer.URL + "/v1"}},
	}}
	runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: cfg, SessionStore: sessions}}
	require.NoError(t, runner.Load())
	s := &Server{Home: home, Sessions: sessions, Runner: runner, RunRT: &state.RunStore{DB: db}}
	runner.Events = s.RunEvents()
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	var connected wsServerMsg
	require.NoError(t, conn.ReadJSON(&connected))

	var send wsClientMsg
	send.RequestID = "req-compact"
	send.SessionID = "session-ws"
	send.Message.Content = "/compact"
	require.NoError(t, conn.WriteJSON(send))

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	started := readRunEventOfType(t, conn, event.RunEventContextCompacting)
	var startedPayload event.ContextCompactingPayload
	require.NoError(t, json.Unmarshal(started.Payload, &startedPayload))
	require.Equal(t, "manual", startedPayload.Trigger)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the summary request never reached the model")
	}

	require.NoError(t, conn.WriteJSON(wsClientMsg{Op: "cancel_command"}))
	ended := readRunEventOfType(t, conn, event.RunEventContextCompactError)
	var endedPayload event.ContextCompactFailedPayload
	require.NoError(t, json.Unmarshal(ended.Payload, &endedPayload))
	require.True(t, endedPayload.Cancelled, "a stopped compaction is cancelled, not failed: %+v", endedPayload)
	require.Equal(t, startedPayload.CompactionID, endedPayload.CompactionID)

	for {
		var msg wsServerMsg
		require.NoError(t, conn.ReadJSON(&msg))
		if msg.Op == "slash_reply" {
			require.Empty(t, msg.Text, "the web draws the compaction from its events and needs no second account")
			break
		}
	}
	boundary, _, err := sessions.LatestCompactBoundary(ctx, "session-ws")
	require.NoError(t, err)
	require.Zero(t, boundary, "a cancelled compaction leaves the history as it was")
}

// TestHandleChatWSStoppingPreTurnCompactionWithdrawsTheTurn pins the web's
// version of Esc before a turn starts, for every way a client stops while the
// conversation is being compacted for its turn: the compaction stops, writes
// nothing, and the message it was making room for is neither recorded nor
// answered.
func TestHandleChatWSStoppingPreTurnCompactionWithdrawsTheTurn(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(t *testing.T, conn *websocket.Conn)
	}{
		{
			name: "client leaves",
			stop: func(t *testing.T, conn *websocket.Conn) {
				require.NoError(t, conn.Close())
			},
		},
		{
			// A client that says something on its way out must still be seen
			// leaving: the loop is busy with the compaction and cannot take the
			// message, so the reader has to keep reading past it.
			name: "client leaves after sending a message",
			stop: func(t *testing.T, conn *websocket.Conn) {
				require.NoError(t, conn.WriteJSON(wsClientMsg{Op: "ping"}))
				require.NoError(t, conn.Close())
			},
		},
		{
			// The web's stop: the same socket hears the compaction end as
			// cancelled, and then that its message was withdrawn.
			name: "client sends cancel_command",
			stop: func(t *testing.T, conn *websocket.Conn) {
				require.NoError(t, conn.WriteJSON(wsClientMsg{Op: "cancel_command"}))
				require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
				ended := readRunEventOfType(t, conn, event.RunEventContextCompactError)
				var endedPayload event.ContextCompactFailedPayload
				require.NoError(t, json.Unmarshal(ended.Payload, &endedPayload))
				require.True(t, endedPayload.Cancelled, "a stopped compaction is cancelled, not failed: %+v", endedPayload)
				for {
					var msg wsServerMsg
					require.NoError(t, conn.ReadJSON(&msg))
					require.NotContains(t, []string{"run_started", "run_error", "run_completed"}, msg.Op, "the withdrawn message started a turn: %+v", msg)
					if msg.Op == "turn_withdrawn" {
						require.Equal(t, "req-turn", msg.RequestID)
						require.Equal(t, "session-ws", msg.SessionID)
						break
					}
				}
				require.NoError(t, conn.Close())
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			home := t.TempDir()
			db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
			require.NoError(t, err)
			defer db.Close()

			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			var turnRequests atomic.Int32
			var compactionStopped atomic.Bool
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), "CONTEXT CHECKPOINT COMPACTION") {
					turnRequests.Add(1)
					http.Error(w, "the withdrawn turn must not reach the model", http.StatusBadRequest)
					return
				}
				once.Do(func() { close(entered) })
				select {
				case <-r.Context().Done():
					compactionStopped.Store(true)
					return
				case <-release:
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, chunk := range []string{
					`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-main","choices":[{"index":0,"delta":{"role":"assistant","content":"SUMMARY"},"finish_reason":null}]}`,
					`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-main","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				} {
					_, _ = io.WriteString(w, "data: "+chunk+"\n\n")
				}
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer model.Close()
			// A failed assertion must not leave the held summary request
			// blocking the model server's Close.
			var releaseOnce sync.Once
			releaseSummary := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseSummary()

			sessions := state.NewSessionStore(db, "main")
			for i := 0; i < 6; i++ {
				_, err = sessions.Append(ctx, "session-ws", "user", strings.Repeat("an earlier question with enough words to count. ", 6))
				require.NoError(t, err)
				_, err = sessions.Append(ctx, "session-ws", "assistant", strings.Repeat("an earlier answer with enough words to count. ", 6))
				require.NoError(t, err)
			}
			before, err := sessions.ListAllMessages(ctx, "session-ws", 0)
			require.NoError(t, err)
			cfg := &appcfg.Root{}
			cfg.Compact.ModelAutoCompactTokenLimit = 100
			cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{"main": {
				Primary:      true,
				LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "gpt-main", APIKey: "test-key", BaseURL: model.URL + "/v1"}},
			}}
			runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: cfg, SessionStore: sessions}}
			require.NoError(t, runner.Load())
			s := &Server{Home: home, Sessions: sessions, Runner: runner, RunRT: &state.RunStore{DB: db}}
			runner.Events = s.RunEvents()
			server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
			defer server.Close()

			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer conn.Close()
			var connected wsServerMsg
			require.NoError(t, conn.ReadJSON(&connected))
			var send wsClientMsg
			send.RequestID = "req-turn"
			send.SessionID = "session-ws"
			send.Message.Content = "the question that needs room"
			require.NoError(t, conn.WriteJSON(send))
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("the pre-turn compaction never started")
			}

			tc.stop(t, conn)

			require.Eventually(t, compactionStopped.Load, 10*time.Second, 20*time.Millisecond, "the compaction outlived the client that stopped it")
			releaseSummary()
			// Give a turn that should not exist the time it would need to be recorded.
			time.Sleep(300 * time.Millisecond)
			boundary, _, err := sessions.LatestCompactBoundary(ctx, "session-ws")
			require.NoError(t, err)
			require.Zero(t, boundary, "a stopped compaction writes no checkpoint")
			after, err := sessions.ListAllMessages(ctx, "session-ws", 0)
			require.NoError(t, err)
			for _, row := range after[len(before):] {
				require.NotContains(t, row.Content, "the question that needs room", "the withdrawn message was recorded")
			}
			require.Zero(t, turnRequests.Load(), "the withdrawn turn reached the model")
		})
	}
}

// TestHandleChatWSMintedSessionExistsWhenAnnounced pins that a session the
// gateway mints for a client's first message exists by the time the client
// hears of it: the client asks for its approvals the moment the session is
// bound, and a session that only existed once its first message was recorded
// answered that with a 404 the user saw as an error.
func TestHandleChatWSMintedSessionExistsWhenAnnounced(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()
	s := &Server{Home: home, Sessions: state.NewSessionStore(db, "main"), Actions: &state.ActionService{DB: db}}
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	var connected wsServerMsg
	require.NoError(t, conn.ReadJSON(&connected))
	var send wsClientMsg
	send.RequestID = "req-first"
	send.Message.Content = "/compact"
	require.NoError(t, conn.WriteJSON(send))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	var bound wsServerMsg
	for bound.Op != "session_bound" {
		require.NoError(t, conn.ReadJSON(&bound))
	}
	require.NotEmpty(t, bound.SessionID)

	owned, err := s.Sessions.HasSession(ctx, bound.SessionID)
	require.NoError(t, err)
	require.True(t, owned, "the announced session does not exist yet")
	req := httptest.NewRequest(http.MethodGet, "/api/actions?status=pending&session_id="+bound.SessionID, nil)
	rec := httptest.NewRecorder()
	s.handleActions(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// blockingRunExecutor is a turn that runs until the test releases it, then
// completes or fails.
type blockingRunExecutor struct {
	entered chan struct{}
	release chan struct{}
	fail    bool
	once    sync.Once
}

func (x *blockingRunExecutor) Run(ctx context.Context, req turn.TurnRequest, started func(string)) (*agent.Result, error) {
	if started != nil {
		started(req.ExistingRunID)
	}
	x.once.Do(func() { close(x.entered) })
	select {
	case <-x.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if x.fail {
		return nil, errors.New("the provider refused the request")
	}
	return &agent.Result{Parts: []llm.ContentPart{llm.Text("done")}}, nil
}

// TestHandleChatWSHandsQueuedMessagesBackBeforeTheTurnEnds pins that a message
// queued while a turn runs is never dropped with the run: however the turn
// ends, the socket that started it hears the message handed back, whole,
// before it hears the turn is over — so the client can send it next or take it
// back. The queue used to die with the run, and the client's later request for
// it always found nothing.
func TestHandleChatWSHandsQueuedMessagesBackBeforeTheTurnEnds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fail     bool
		terminal string
	}{
		{name: "completed", terminal: "run_completed"},
		{name: "failed", fail: true, terminal: "run_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			home := t.TempDir()
			db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
			require.NoError(t, err)
			defer db.Close()

			executor := &blockingRunExecutor{entered: make(chan struct{}), release: make(chan struct{}), fail: tc.fail}
			var releaseOnce sync.Once
			releaseTurn := func() { releaseOnce.Do(func() { close(executor.release) }) }
			defer releaseTurn()

			sessions := state.NewSessionStore(db, "main")
			require.NoError(t, sessions.Ensure(ctx, "session-ws", "session-ws"))
			cfg := &appcfg.Root{}
			cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{"main": {
				Primary:      true,
				LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "gpt-main", APIKey: "test-key", BaseURL: "http://127.0.0.1:9/v1"}},
			}}
			runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: cfg, SessionStore: sessions}}
			require.NoError(t, runner.Load())
			runStore := &state.RunStore{DB: db}
			s := &Server{
				Home: home, Sessions: sessions, Runner: runner, RunRT: runStore,
				Core: turn.New(turn.WithRunEventStore(runStore), turn.WithSessionStore(sessions), turn.WithRunExecutor(executor)),
			}
			runner.Events = s.RunEvents()
			server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
			defer server.Close()

			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer conn.Close()
			var connected wsServerMsg
			require.NoError(t, conn.ReadJSON(&connected))
			var send wsClientMsg
			send.RequestID = "req-turn"
			send.SessionID = "session-ws"
			send.Message.Content = "the first question"
			require.NoError(t, conn.WriteJSON(send))

			require.NoError(t, conn.SetReadDeadline(time.Now().Add(15*time.Second)))
			runID := ""
			for runID == "" {
				var msg wsServerMsg
				require.NoError(t, conn.ReadJSON(&msg))
				require.NotEqual(t, "run_error", msg.Op, "%+v", msg)
				if msg.Op == "run_started" {
					runID = msg.RunID
				}
			}
			select {
			case <-executor.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("the turn never started running")
			}

			// A message queued while the turn runs, as the web queues one.
			req := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/queued-input",
				strings.NewReader(`{"message":"and then this","attachments":["file-1"],"mention_images":["shots/a.png"]}`))
			req.SetPathValue("id", runID)
			rec := httptest.NewRecorder()
			s.handleRunQueuedInput(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), `"accepted":true`)
			releaseTurn()

			var released *event.QueuedInputReleasedPayload
			for {
				var msg wsServerMsg
				require.NoError(t, conn.ReadJSON(&msg))
				if msg.Op == tc.terminal {
					break
				}
				require.NotContains(t, []string{"run_completed", "run_error", "run_cancelled"}, msg.Op, "the turn ended another way: %+v", msg)
				if msg.Op != "run_event" {
					continue
				}
				raw, err := json.Marshal(msg.Data)
				require.NoError(t, err)
				var evt event.RunEvent
				require.NoError(t, json.Unmarshal(raw, &evt))
				if evt.Type != event.RunEventQueuedInputReleased {
					continue
				}
				released = &event.QueuedInputReleasedPayload{}
				require.NoError(t, json.Unmarshal(evt.Payload, released))
			}
			require.NotNil(t, released, "the turn ended without handing back the message queued for it")
			want := []event.ReleasedInput{{Text: "and then this", Attachments: []string{"file-1"}, MentionImages: []string{"shots/a.png"}}}
			if tc.fail {
				// A failed turn is interrupted: the message returns to the
				// composer instead of running as the next turn.
				require.Equal(t, want, released.Inputs)
				require.Empty(t, released.Next)
			} else {
				// A completed turn runs the message next: the engine's boundary
				// decision says so, and the client only obeys.
				require.Equal(t, want, released.Next)
				require.Empty(t, released.Inputs)
			}
			require.Zero(t, s.runController().Active())
		})
	}
}

// TestHandleChatWSTurnThatCannotRecordItsRunIsWithdrawn pins that a web turn
// whose run row cannot be recorded does not start under an invented run id:
// every event and transcript row written under one would be refused by its
// reference to fb_runs, and the user would see nothing at all. The message is
// withdrawn back to the composer with the store's own error instead.
func TestHandleChatWSTurnThatCannotRecordItsRunIsWithdrawn(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()
	// The run store is a database the conversation is not in, so recording
	// the run is refused the way any failed insert would be.
	elsewhere, err := state.OpenStateForTest(ctx, filepath.Join(home, "elsewhere.db"))
	require.NoError(t, err)
	defer elsewhere.Close()

	var modelCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		http.Error(w, "a turn without a run must not reach the model", http.StatusBadRequest)
	}))
	defer model.Close()
	sessions := state.NewSessionStore(db, "main")
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{"main": {
		Primary:      true,
		LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "gpt-main", APIKey: "test-key", BaseURL: model.URL + "/v1"}},
	}}
	runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: cfg, SessionStore: sessions}}
	require.NoError(t, runner.Load())
	s := &Server{Home: home, Sessions: sessions, Runner: runner, RunRT: &state.RunStore{DB: elsewhere}}
	runner.Events = s.RunEvents()
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	var connected wsServerMsg
	require.NoError(t, conn.ReadJSON(&connected))
	var send wsClientMsg
	send.RequestID = "req-turn"
	send.SessionID = "session-ws"
	send.Message.Content = "hello"
	require.NoError(t, conn.WriteJSON(send))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	for {
		var msg wsServerMsg
		require.NoError(t, conn.ReadJSON(&msg))
		require.NotContains(t, []string{"run_started", "run_error", "run_completed"}, msg.Op, "a turn without a run started: %+v", msg)
		if msg.Op == "turn_withdrawn" {
			require.Equal(t, "req-turn", msg.RequestID)
			require.Contains(t, msg.Error, "FOREIGN KEY")
			break
		}
	}
	rows, err := sessions.ListAllMessages(ctx, "session-ws", 0)
	require.NoError(t, err)
	require.Empty(t, rows, "the withdrawn message was recorded")
	require.Zero(t, modelCalls.Load(), "the withdrawn turn reached the model")
}

// TestHandleChatWSSessionBusyWithdrawsTheMessage pins the cross-process rule
// at the web's own entry: a session another live process holds a turn in
// refuses the message before anything of it is written, and the withdrawal
// carries the refusal's stable code beside its sentence, so the page can say
// it in the viewer's language.
func TestHandleChatWSSessionBusyWithdrawsTheMessage(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(ctx, "session-ws", "session-ws"))
	// Another live process holds the session's turn: a lease it renews and a
	// run it left in flight.
	other := &state.RunStore{DB: db, Owner: "other-process"}
	stop, err := other.HoldOwnerLease(ctx)
	require.NoError(t, err)
	defer stop()
	_, err = other.CreateRun(ctx, "session-ws", "in flight")
	require.NoError(t, err)

	s := &Server{Home: home, Sessions: sessions, RunRT: &state.RunStore{DB: db, Owner: "gateway-test"}}
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	var connected wsServerMsg
	require.NoError(t, conn.ReadJSON(&connected))
	var send wsClientMsg
	send.RequestID = "req-turn"
	send.SessionID = "session-ws"
	send.Message.Content = "hello"
	require.NoError(t, conn.WriteJSON(send))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	var withdrawn *wsServerMsg
	for {
		var msg wsServerMsg
		require.NoError(t, conn.ReadJSON(&msg))
		require.NotContains(t, []string{"run_started", "run_error", "run_completed"}, msg.Op, "a refused turn must not start: %+v", msg)
		if msg.Op == "turn_withdrawn" {
			withdrawn = &msg
			break
		}
	}
	require.Equal(t, "req-turn", withdrawn.RequestID)
	// The run the gateway refused beside belongs to another process, so the
	// sentence names the other surface and the code says so.
	require.Equal(t, "This conversation is running on another surface (the terminal or the web); send again when it finishes there.", withdrawn.Error)
	require.Equal(t, map[string]any{"code": "session_running_elsewhere"}, withdrawn.Data)
	rows, err := sessions.ListAllMessages(ctx, "session-ws", 0)
	require.NoError(t, err)
	require.Empty(t, rows, "no user row may be written for a turn that never started")
}

// seedWebImageConversation gives one conversation a user row that carries an
// image, written the way the composer writes one, so the turn gate sees an
// image the next request would replay.
func seedWebImageConversation(t *testing.T, ctx context.Context, sessions *state.SessionStore, sid string) {
	t.Helper()
	dir := t.TempDir()
	img := filepath.Join(dir, "clip.png")
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(img, data, 0o600))
	text := "这个 401 是哪一步报的？\n[Image #1]"
	parts := state.MessagePartsJSON(llm.UserMessage(llm.Text(text)), text)
	parts = state.AppendRawPartJSON(parts, state.FileReferencePartJSON(img, "clip.png", "image/png"))
	_, err = sessions.AppendStructuredMessage(ctx, sid, "user", text, "", parts, "", "", "", "", state.MessageExecTiming{})
	require.NoError(t, err)
}

// refuseWebTurn drives one message through the chat socket and returns the
// reply that ended it, failing on any reply that says a run started.
func refuseWebTurn(t *testing.T, conn *websocket.Conn, requestID, content string) wsServerMsg {
	t.Helper()
	var send wsClientMsg
	send.RequestID = requestID
	send.SessionID = "session-ws"
	send.Message.Content = content
	require.NoError(t, conn.WriteJSON(send))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	for {
		var msg wsServerMsg
		require.NoError(t, conn.ReadJSON(&msg))
		require.NotContains(t, []string{"run_started", "run_error", "run_completed"}, msg.Op,
			"a refused message must not start a run: %+v", msg)
		if msg.Op == "turn_refused" || msg.Op == "turn_withdrawn" {
			return msg
		}
	}
}

// TestHandleChatWSKeepsAMessageTheModelCannotTakeAndRefusesIt pins the web's
// side of the terminal's rule: a message whose request would carry an image
// into a text-only session model is what the user said, so it is stored and
// kept — only the request is withheld. The reply names the model and its
// stable code; no run exists for it; and a resend under the same request id is
// the cached reply, not a second row.
func TestHandleChatWSKeepsAMessageTheModelCannotTakeAndRefusesIt(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(ctx, "session-ws", "session-ws"))
	seedWebImageConversation(t, ctx, sessions, "session-ws")

	cfg := appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{"main": {
		Primary:      true,
		LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "deepseek", Model: "deepseek-v4-pro", APIKey: "test-key", BaseURL: "http://model.invalid/v1"}},
	}}
	runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: &cfg, SessionStore: sessions}, FileResolver: run.PathResolver{}}
	s := &Server{Home: home, Sessions: sessions, Runner: runner, RunRT: &state.RunStore{DB: db, Owner: "gateway-test"}}
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	var connected wsServerMsg
	require.NoError(t, conn.ReadJSON(&connected))

	refused := refuseWebTurn(t, conn, "req-refused", "continue")
	require.Equal(t, "turn_refused", refused.Op)
	require.Equal(t, map[string]any{"code": "image_input_unsupported", "provider": "deepseek", "model": "deepseek-v4-pro"}, refused.Data)
	require.Contains(t, refused.Error, "does not accept image input")

	rows, err := sessions.ListAllMessages(ctx, "session-ws", 0)
	require.NoError(t, err)
	require.Len(t, rows, 2, "the refused message must be kept: %+v", rows)
	require.Equal(t, "continue", rows[1].Content)
	var runCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM fb_runs`).Scan(&runCount))
	require.Zero(t, runCount, "a refused message starts no run")

	// The same request id again is the cached reply, and writes nothing.
	again := refuseWebTurn(t, conn, "req-refused", "continue")
	require.Equal(t, "turn_refused", again.Op)
	rows, err = sessions.ListAllMessages(ctx, "session-ws", 0)
	require.NoError(t, err)
	require.Len(t, rows, 2, "a resent request id must not store the message twice")
}

// TestHandleChatWSRefusalNamesTheRememberedRejection pins the second code: when
// the catalog says nothing about the model, but this conversation already paid
// for it refusing an image, the reply says that instead — and carries the
// provider's own sentence, which is the part the reader can act on. The recovery
// is the same one the catalog refusal points at.
func TestHandleChatWSRefusalNamesTheRememberedRejection(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(ctx, "session-ws", "session-ws"))
	seedWebImageConversation(t, ctx, sessions, "session-ws")
	// A model the catalog does not know: nothing but the record can refuse it.
	require.NoError(t, sessions.RecordImageRefusal(ctx, "session-ws", "acme", "vision-model", "unsupported image"))

	cfg := appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{"main": {
		Primary:      true,
		LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "acme", Model: "vision-model", APIKey: "test-key", BaseURL: "http://model.invalid/v1"}},
	}}
	runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: &cfg, SessionStore: sessions}, FileResolver: run.PathResolver{}}
	s := &Server{Home: home, Sessions: sessions, Runner: runner, RunRT: &state.RunStore{DB: db, Owner: "gateway-test"}}
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	var connected wsServerMsg
	require.NoError(t, conn.ReadJSON(&connected))

	refused := refuseWebTurn(t, conn, "req-remembered", "continue")
	require.Equal(t, "turn_refused", refused.Op)
	data, ok := refused.Data.(map[string]any)
	require.True(t, ok, "the refusal carries its facts: %+v", refused.Data)
	require.Equal(t, "image_rejected_by_provider", data["code"])
	require.Equal(t, "acme", data["provider"])
	require.Equal(t, "vision-model", data["model"])
	require.Equal(t, "unsupported image", data["reason"])
	require.Contains(t, refused.Error, "already refused an image")
	require.Contains(t, refused.Error, "provider said: unsupported image")
	require.Contains(t, refused.Error, "/model")
}

// TestHandleChatWSRefusedImageMessageInABusySessionIsWithdrawn pins that a
// refused message still cannot land in a conversation another surface is
// running: the keep-without-a-run path asks the store first, and withdraws the
// message instead of writing it into someone else's turn.
func TestHandleChatWSRefusedImageMessageInABusySessionIsWithdrawn(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(ctx, "session-ws", "session-ws"))
	seedWebImageConversation(t, ctx, sessions, "session-ws")
	other := &state.RunStore{DB: db, Owner: "other-process"}
	stop, err := other.HoldOwnerLease(ctx)
	require.NoError(t, err)
	defer stop()
	_, err = other.CreateRun(ctx, "session-ws", "in flight")
	require.NoError(t, err)

	cfg := appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{"main": {
		Primary:      true,
		LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "deepseek", Model: "deepseek-v4-pro", APIKey: "test-key", BaseURL: "http://model.invalid/v1"}},
	}}
	runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: &cfg, SessionStore: sessions}, FileResolver: run.PathResolver{}}
	s := &Server{Home: home, Sessions: sessions, Runner: runner, RunRT: &state.RunStore{DB: db, Owner: "gateway-test"}}
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	var connected wsServerMsg
	require.NoError(t, conn.ReadJSON(&connected))

	withdrawn := refuseWebTurn(t, conn, "req-busy", "continue")
	require.Equal(t, "turn_withdrawn", withdrawn.Op)
	require.Equal(t, map[string]any{"code": "session_running_elsewhere"}, withdrawn.Data)
	rows, err := sessions.ListAllMessages(ctx, "session-ws", 0)
	require.NoError(t, err)
	require.Len(t, rows, 1, "the busy session must not keep the refused message: %+v", rows)
}

// TestWebMessageAttachesImage pins what the turn gate reads before the turn
// exists: an @-picked image, or an upload recorded as an image, is an image
// the request would carry; a document is not, and an upload that cannot be
// read is not this predicate's to report.
func TestWebMessageAttachesImage(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	defer db.Close()
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(ctx, "session-ws", "session-ws"))
	s := &Server{Sessions: sessions, Files: &state.FileStore{DB: db, Home: t.TempDir(), Cfg: state.LoadConfigFromEnv()}}

	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	require.NoError(t, err)
	shot, err := s.Files.CreateFromReader(ctx, "session-ws", "shot.png", bytes.NewReader(png), int64(len(png)), "image/png")
	require.NoError(t, err)
	report, err := s.Files.CreateFromReader(ctx, "session-ws", "report.pdf", strings.NewReader("%PDF-1.4"), 8, "application/pdf")
	require.NoError(t, err)

	require.True(t, s.webMessageAttachesImage(ctx, nil, []string{"notes/picture.png"}), "an @-picked image")
	require.True(t, s.webMessageAttachesImage(ctx, []string{shot.ID}, nil), "an upload stored as an image")
	require.False(t, s.webMessageAttachesImage(ctx, []string{report.ID}, nil), "a document shows the model no image")
	require.False(t, s.webMessageAttachesImage(ctx, []string{"missing-file-id"}, nil), "an upload that cannot be read")
	require.False(t, s.webMessageAttachesImage(ctx, nil, nil), "a message that attaches nothing")
}

// TestHandleChatWSEstablishesANamedSessionBeforeItsSlashCommand pins that a
// session the client names is established when its message arrives, before
// any slash command writes into it: /plan records its mode change in the
// session's log, which references the session row. A session another primary
// agent owns is refused before the command runs, so nothing lands in its log.
func TestHandleChatWSEstablishesANamedSessionBeforeItsSlashCommand(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, state.NewSessionStore(db, "other").Ensure(ctx, "theirs", "theirs"))
	s := &Server{Home: home, Sessions: sessions, RunRT: &state.RunStore{DB: db}}
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	var connected wsServerMsg
	require.NoError(t, conn.ReadJSON(&connected))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))

	send := func(requestID, sessionID string) {
		var msg wsClientMsg
		msg.RequestID = requestID
		msg.SessionID = sessionID
		msg.Message.Content = "/plan"
		require.NoError(t, conn.WriteJSON(msg))
	}
	send("req-named", "client-named")
	for {
		var msg wsServerMsg
		require.NoError(t, conn.ReadJSON(&msg))
		require.NotEqual(t, "session_event_persistence_failed", msg.Message, "the mode change could not be recorded: %+v", msg)
		if msg.RequestID == "req-named" && msg.Op == "mode_changed" {
			break
		}
	}
	owned, err := sessions.HasSession(ctx, "client-named")
	require.NoError(t, err)
	require.True(t, owned)

	send("req-theirs", "theirs")
	for {
		var msg wsServerMsg
		require.NoError(t, conn.ReadJSON(&msg))
		if msg.RequestID != "req-theirs" {
			continue
		}
		require.NotEqual(t, "mode_changed", msg.Op, "a slash command ran in another agent's session")
		if msg.Op == "turn_withdrawn" {
			require.Contains(t, msg.Error, "another primary agent")
			break
		}
	}
	var written int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_session_events WHERE session_id='theirs'`).Scan(&written))
	require.Zero(t, written, "another agent's session log was written")
}

type stubPlanEventStore struct {
	events []state.SessionEvent
	err    error
}

func (s *stubPlanEventStore) ListRunEventsOfTypes(_ context.Context, runID string, types ...string) ([]state.SessionEvent, error) {
	if s.err != nil {
		return nil, s.err
	}
	var out []state.SessionEvent
	for _, evt := range s.events {
		if evt.RunID != runID {
			continue
		}
		for _, want := range types {
			if evt.Type == want {
				out = append(out, evt)
				break
			}
		}
	}
	return out, nil
}

func planEvent(t *testing.T, runID string, payload event.PlanUpdatedPayload) state.SessionEvent {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return state.SessionEvent{ID: "evt-" + runID, RunID: runID, Type: event.RunEventPlanUpdated, Payload: raw, CreatedAt: time.Now()}
}

func TestLastPlanProgressOfRun(t *testing.T) {
	t.Parallel()
	store := &stubPlanEventStore{events: []state.SessionEvent{
		planEvent(t, "run-1", event.PlanUpdatedPayload{Completed: 1, Total: 3, Explanation: "first"}),
		planEvent(t, "run-1", event.PlanUpdatedPayload{Completed: 2, Total: 3, Items: []event.PlanUpdateItem{
			{ID: "1", Content: "done", Status: "completed"},
			{ID: "2", Content: "short", Status: "in_progress"},
		}}),
		planEvent(t, "run-2", event.PlanUpdatedPayload{Completed: 9, Total: 9}),
	}}
	got := lastPlanProgressOfRun(context.Background(), store, "run-1")
	if got.Done != 2 || got.Total != 3 || got.Active != "short" {
		t.Fatalf("progress = %+v, want {2 3 short}", got)
	}
	// A run with no checklist reports zeros, not a stale other-run value.
	empty := lastPlanProgressOfRun(context.Background(), store, "run-3")
	if empty != (event.PlanProgress{}) {
		t.Fatalf("empty run progress = %+v", empty)
	}
	if none := lastPlanProgressOfRun(context.Background(), nil, "run-1"); none != (event.PlanProgress{}) {
		t.Fatalf("nil store progress = %+v", none)
	}
}

// A subagent's checklist lands in its parent's run but never stands in for
// the conversation's own plan on the worked line.
func TestLastPlanProgressOfRunIgnoresSubagentChecklists(t *testing.T) {
	t.Parallel()
	store := &stubPlanEventStore{events: []state.SessionEvent{
		planEvent(t, "run-1", event.PlanUpdatedPayload{Completed: 1, Total: 2}),
		planEvent(t, "run-1", event.PlanUpdatedPayload{Completed: 4, Total: 5, AgentID: "worker-1"}),
	}}
	if got := lastPlanProgressOfRun(context.Background(), store, "run-1"); got.Done != 1 || got.Total != 2 {
		t.Fatalf("progress = %+v, want the conversation's {1 2}", got)
	}
	only := &stubPlanEventStore{events: []state.SessionEvent{
		planEvent(t, "run-1", event.PlanUpdatedPayload{Completed: 4, Total: 5, AgentID: "worker-1"}),
	}}
	if got := lastPlanProgressOfRun(context.Background(), only, "run-1"); got != (event.PlanProgress{}) {
		t.Fatalf("subagent-only progress = %+v, want none", got)
	}
}

// The socket's three run endings hand their checklist facts to the canonical
// event they mirror, so a replayed session reads the same line the live one
// showed.
func TestCanonicalRunEndingsCarryPlanFacts(t *testing.T) {
	t.Parallel()
	facts := event.RunPlanFacts{PlanDone: 1, PlanTotal: 3, PlanActive: "short"}
	for _, op := range []string{"run_completed", "run_cancelled", "run_error"} {
		events := canonicalRunEventsFromWS(wsServerMsg{Op: op, RunID: "run-1", SessionID: "s1", RunPlanFacts: facts})
		require.NotEmpty(t, events, op)
		raw, err := json.Marshal(events[0].Payload)
		require.NoError(t, err)
		var got event.RunPlanFacts
		require.NoError(t, json.Unmarshal(raw, &got))
		require.Equal(t, facts, got, op)
	}
}

// headerOf runs one tool event payload through the enrichment and reads back
// the header the page will draw.
func headerOf(t *testing.T, root, eventType string, payload map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var out struct {
		Header string `json:"header"`
	}
	require.NoError(t, json.Unmarshal(toolEventPayloadWithHeader(root, eventType, raw), &out))
	return out.Header
}

// TestToolHeadersSayWhatTheTerminalSays pins the web's card header to the
// terminal's wording (tool.DisplayHeader): only a shell call is "Ran ...", every
// other tool names what it did in its own words, and a path inside the
// conversation's directory is relative while one outside it stays absolute.
func TestToolHeadersSayWhatTheTerminalSays(t *testing.T) {
	root := "/work/proj"
	read := func(path string) map[string]any {
		return map[string]any{
			"tool_name": "read_file",
			"summary":   "ran read " + path + " · lines 1-96",
			"tool_meta": map[string]any{
				"tool_name": "read_file", "status": "completed",
				"input": map[string]any{"file_path": path}, "result_lines": 96,
			},
			"duration_seconds": 0.01,
		}
	}

	// A file in the project reads relative to it, with the lines it showed.
	require.Equal(t, "Read plans/100.md lines 1-96 · <0.1s",
		headerOf(t, root, event.RunEventToolCompleted, read("/work/proj/plans/100.md")))
	// One outside the project keeps its absolute path.
	require.Equal(t, "Read /etc/hosts lines 1-96 · <0.1s",
		headerOf(t, root, event.RunEventToolCompleted, read("/etc/hosts")))

	// "Ran" belongs to the shell alone.
	require.Equal(t, "Ran ls plans/ 1.2s", headerOf(t, root, event.RunEventToolCompleted, map[string]any{
		"tool_name": "shell", "summary": "ran ls plans/",
		"tool_meta":        map[string]any{"tool_name": "shell", "status": "completed", "input": map[string]any{"command": "ls plans/"}},
		"duration_seconds": 1.2,
	}))
	require.Equal(t, "Running ls plans/", headerOf(t, root, event.RunEventToolStarted, map[string]any{
		"tool_name": "shell",
		"tool_meta": map[string]any{"tool_name": "shell", "status": "running", "input": map[string]any{"command": "ls plans/"}},
	}))
	require.Equal(t, "Notes", headerOf(t, root, event.RunEventToolCompleted, map[string]any{
		"tool_name": "intermediate_tool", "summary": "saved intermediate notes",
		"tool_meta": map[string]any{"tool_name": "intermediate_tool", "status": "completed", "input": map[string]any{"action": ""}},
	}))
	require.Equal(t, "Edited main.go", headerOf(t, root, event.RunEventToolCompleted, map[string]any{
		"tool_name": "edit_file",
		"tool_meta": map[string]any{"tool_name": "edit_file", "status": "completed", "input": map[string]any{"file_path": "/work/proj/main.go"}},
	}))
	require.Equal(t, "Fetched https://example.com", headerOf(t, root, event.RunEventToolCompleted, map[string]any{
		"tool_name": "web_fetch",
		"tool_meta": map[string]any{"tool_name": "web_fetch", "status": "completed", "input": map[string]any{"url": "https://example.com"}},
	}))
}

func TestToolEventPayloadLeavesOtherEventsAlone(t *testing.T) {
	raw := json.RawMessage(`{"text":"hello"}`)
	require.Equal(t, string(raw), string(toolEventPayloadWithHeader("/p", event.RunEventTurnError, raw)))
	require.Equal(t, "not json", string(toolEventPayloadWithHeader("/p", event.RunEventToolStarted, json.RawMessage("not json"))))
}

// TestToolHeadersCarryTheirParts: a tool event payload carries the header in
// its four parts as well, and they are DisplayHeaderParts' own — so a surface
// that lays the header out itself draws the same words the joined header
// spells.
func TestToolHeadersCarryTheirParts(t *testing.T) {
	root := "/work/proj"
	payload := map[string]any{
		"tool_name": "shell", "summary": "ran ls plans/",
		"tool_meta":        map[string]any{"tool_name": "shell", "status": "completed", "input": map[string]any{"command": "ls plans/"}},
		"duration_seconds": 1.2,
	}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var out struct {
		Header string `json:"header"`
		Parts  struct {
			Action   string `json:"action"`
			Target   string `json:"target"`
			Suffix   string `json:"suffix"`
			Duration string `json:"duration"`
		} `json:"header_parts"`
	}
	require.NoError(t, json.Unmarshal(toolEventPayloadWithHeader(root, event.RunEventToolCompleted, raw), &out))
	require.Equal(t, "Ran ls plans/ 1.2s", out.Header)
	want := tool.DisplayHeaderParts(tool.DisplayFrame{
		Title: "shell", Summary: "ran ls plans/",
		Meta:  tool.ToolMeta{ToolName: "shell", Status: "completed", Input: map[string]any{"command": "ls plans/"}},
		Final: true, Duration: 1200 * time.Millisecond,
	}, root)
	require.Equal(t, want, tool.HeaderParts{
		Action: out.Parts.Action, Target: out.Parts.Target,
		Suffix: out.Parts.Suffix, Duration: out.Parts.Duration,
	})
	require.Equal(t, "Ran", out.Parts.Action)
	require.Equal(t, "ls plans/", out.Parts.Target)
	require.Equal(t, "1.2s", out.Parts.Duration)
}

// TestCanceledCallHeaderListKeepsTheCallIDs: the per-call headers of an
// assistant row ride as a list with the call id verbatim — the web
// camel-cases response keys deeply, which would rewrite "call_a_1" used as a
// map key into "callA1" and strand the header.
func TestCanceledCallHeaderListKeepsTheCallIDs(t *testing.T) {
	assistant := `[{"type":"tool_calls","tool_calls":[{"id":"call_a_1","type":"function","function":{"name":"shell","arguments":"{\"command\":\"go test ./...\"}"}},{"id":"toolu_01X","type":"function","function":{"name":"read_file","arguments":"{\"file_path\":\"/work/proj/x.go\"}"}}]}]`
	got := canceledCallHeaderList(func() string { return "/work/proj" }, assistant)
	require.Len(t, got, 2)
	require.Equal(t, "call_a_1", got[0].CallID)
	require.Equal(t, "Canceled go test ./...", got[0].Header)
	require.Equal(t, tool.HeaderParts{Action: "Canceled", Target: "go test ./..."}, got[0].Parts)
	require.Equal(t, "toolu_01X", got[1].CallID)
	require.Equal(t, "Canceled x.go", got[1].Header)
	require.Nil(t, canceledCallHeaderList(func() string { return "/work/proj" }, `[{"text":"hi","type":"text"}]`))
}

// TestStoredToolRowsAndCanceledCallsGetTheSameWording: a reloaded tool row is
// worded from what it kept, and a call the run never answered reads "Canceled
// ..." — never "denied", nobody was asked.
func TestStoredToolRowsAndCanceledCallsGetTheSameWording(t *testing.T) {
	parts := `[{"text":"{}","type":"text"},{"type":"tool_display","body":"x","summary":"ran read /work/proj/a.md · lines 1-9","tool_meta_json":"{\"tool_name\":\"read_file\",\"status\":\"completed\",\"input\":{\"file_path\":\"/work/proj/a.md\"},\"result_lines\":9}"}]`
	require.Equal(t, "Read a.md lines 1-9 · 2.0s", storedToolRowHeader("/work/proj", parts, "", 2*time.Second))
	header, headerParts, body, toolName := storedToolRowHeaderParts("/work/proj", parts, "", 2*time.Second)
	require.Equal(t, "Read a.md lines 1-9 · 2.0s", header)
	require.Equal(t, tool.HeaderParts{Action: "Read", Target: "a.md", Suffix: "lines 1-9", Duration: "2.0s"}, headerParts)
	require.Equal(t, "x", body)
	require.Equal(t, "read_file", toolName)
	require.Equal(t, "", storedToolRowHeader("/work/proj", `[{"text":"x","type":"text"}]`, "", 0), "a row with no tool facts has no header of its own")

	assistant := `[{"type":"tool_calls","tool_calls":[{"id":"c1","type":"function","function":{"name":"shell","arguments":"{\"command\":\"go test ./...\"}"}},{"id":"c2","type":"function","function":{"name":"read_file","arguments":"{\"file_path\":\"/work/proj/x.go\"}"}}]}]`
	got := canceledCallHeaderList(func() string { return "/work/proj" }, assistant)
	require.Len(t, got, 2)
	require.Equal(t, "c1", got[0].CallID)
	require.Equal(t, "Canceled go test ./...", got[0].Header)
	require.Equal(t, "c2", got[1].CallID)
	require.Equal(t, "Canceled x.go", got[1].Header)
	require.Nil(t, canceledCallHeaderList(func() string { return "/work/proj" }, `[{"text":"hi","type":"text"}]`))
}

// seedToolRow writes one stored tool row carrying a tool_display part with the
// given body and meta, the shape the engine persists for a completed call.
func seedToolRow(ctx context.Context, sessions *state.SessionStore, sessionID, toolName, body string, meta tool.ToolMeta) error {
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	type part struct {
		Type         string `json:"type"`
		Body         string `json:"body"`
		Summary      string `json:"summary"`
		ToolMetaJSON string `json:"tool_meta_json"`
	}
	partsJSON, err := json.Marshal([]part{{
		Type: "tool_display", Body: body, Summary: "ran " + toolName, ToolMetaJSON: string(metaJSON),
	}})
	if err != nil {
		return err
	}
	_, err = sessions.AppendStructuredMessageForRun(ctx, sessionID, "", "tool", "", "",
		string(partsJSON), "", "", "", string(metaJSON), state.MessageExecTiming{DurationMs: 1000})
	return err
}

// TestShellCardRidesTheStepTheEventAndTheStoredRow: one shell completion body
// yields one set of card facts on all three roads it travels — the live step,
// the canonical tool event, and the reloaded history row — because all three
// read the one body back; a row that is not a shell card carries none.
func TestShellCardRidesTheStepTheEventAndTheStoredRow(t *testing.T) {
	evt := tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "shell",
		Input:    map[string]any{"command": "go test ./..."},
		Output:   map[string]any{"stdout": "ok  pkg/tool", "exit_code": 0},
	}
	body, _ := tool.FormatToolStepResult(evt, tool.DefaultMaxFormattedBody)
	meta := tool.BuildToolMeta(evt)

	// The live step.
	stepData := gatewayToolCompletedStepData("call-1", "tool shell", "shell", evt.Output, "", body, "", "", meta)
	stepCard, ok := stepData["shell_card"].(*tool.ShellCard)
	require.True(t, ok, "the live step carries no shell_card: %v", stepData)
	want, err := json.Marshal(stepCard)
	require.NoError(t, err)

	// The canonical tool event.
	raw, err := json.Marshal(map[string]any{
		"tool_name": "shell", "summary": "ran go test ./...", "display_body": body,
		"duration_seconds": 1.0,
		"tool_meta": map[string]any{"tool_name": "shell", "status": "completed",
			"input": map[string]any{"command": "go test ./..."}},
	})
	require.NoError(t, err)
	var eventOut struct {
		ShellCard json.RawMessage `json:"shell_card"`
	}
	require.NoError(t, json.Unmarshal(toolEventPayloadWithHeader("/w", event.RunEventToolCompleted, raw), &eventOut))
	require.JSONEq(t, string(want), string(eventOut.ShellCard))

	// The reloaded history row.
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(ctx, "s1", "s1"))
	require.NoError(t, seedToolRow(ctx, sessions, "s1", "shell", body, meta))
	s := &Server{Sessions: sessions}
	rec := cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s1/messages", nil, s.handleChatMessages, "id", "s1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var rows []struct {
		Role        string          `json:"role"`
		ShellCard   json.RawMessage `json:"shell_card"`
		Header      string          `json:"header"`
		HeaderParts *struct {
			Action   string `json:"action"`
			Target   string `json:"target"`
			Duration string `json:"duration"`
		} `json:"header_parts"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	require.Equal(t, "tool", rows[0].Role)
	require.JSONEq(t, string(want), string(rows[0].ShellCard))
	// The row's header arrives in parts as well (this seeded row carries no
	// execution timing, so no duration rides either form).
	require.NotNil(t, rows[0].HeaderParts)
	require.Equal(t, "Ran go test ./...", rows[0].Header)
	require.Equal(t, "Ran", rows[0].HeaderParts.Action)
	require.Equal(t, "go test ./...", rows[0].HeaderParts.Target)
	require.Equal(t, "", rows[0].HeaderParts.Duration)

	// A card that is not a shell card carries none.
	other := tool.StepEvent{
		Kind: event.RunEventToolCompleted, ToolName: "read_file",
		Input:  map[string]any{"file_path": "/w/a.go"},
		Output: map[string]any{"content": "x"},
	}
	otherBody, _ := tool.FormatToolStepResult(other, tool.DefaultMaxFormattedBody)
	otherData := gatewayToolCompletedStepData("call-2", "tool read_file", "read_file", other.Output, "", otherBody, "", "", tool.BuildToolMeta(other))
	require.NotContains(t, otherData, "shell_card")
}

// TestFileDiffRidesTheStepTheEventAndTheStoredRow: an edit's turn diff rides
// the same three roads with the same facts, and a shell row carries none.
func TestFileDiffRidesTheStepTheEventAndTheStoredRow(t *testing.T) {
	evt := tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "edit_file",
		Input:    map[string]any{"file_path": "/w/a.go"},
		Output: map[string]any{
			"turn_diff": event.Summary{Path: "a.go", Added: 2, Deleted: 1, UnifiedDiff: "@@ -1 +1 @@\n-a\n+b"},
		},
	}
	body, _ := tool.FormatToolStepResult(evt, tool.DefaultMaxFormattedBody)
	meta := tool.BuildToolMeta(evt)

	stepData := gatewayToolCompletedStepData("call-1", "tool edit_file", "edit_file", evt.Output, "", body, "", "", meta)
	stepDiff, ok := stepData["file_diff"].(*tool.FileDiff)
	require.True(t, ok, "the live step carries no file_diff: %v", stepData)
	want, err := json.Marshal(stepDiff)
	require.NoError(t, err)

	raw, err := json.Marshal(map[string]any{
		"tool_name": "edit_file", "summary": "edited a.go", "display_body": body,
		"tool_meta": map[string]any{"tool_name": "edit_file", "status": "completed",
			"input": map[string]any{"file_path": "/w/a.go"}},
	})
	require.NoError(t, err)
	var eventOut struct {
		FileDiff json.RawMessage `json:"file_diff"`
	}
	require.NoError(t, json.Unmarshal(toolEventPayloadWithHeader("/w", event.RunEventToolCompleted, raw), &eventOut))
	require.JSONEq(t, string(want), string(eventOut.FileDiff))

	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(ctx, "s1", "s1"))
	require.NoError(t, seedToolRow(ctx, sessions, "s1", "edit_file", body, meta))
	s := &Server{Sessions: sessions}
	rec := cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s1/messages", nil, s.handleChatMessages, "id", "s1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var rows []struct {
		Role     string          `json:"role"`
		FileDiff json.RawMessage `json:"file_diff"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	require.JSONEq(t, string(want), string(rows[0].FileDiff))

	// A shell row carries a shell_card, never a file_diff.
	shellEvt := tool.StepEvent{
		Kind: event.RunEventToolCompleted, ToolName: "shell",
		Input:  map[string]any{"command": "ls"},
		Output: map[string]any{"stdout": "x", "exit_code": 0},
	}
	shellBody, _ := tool.FormatToolStepResult(shellEvt, tool.DefaultMaxFormattedBody)
	shellData := gatewayToolCompletedStepData("call-2", "tool shell", "shell", shellEvt.Output, "", shellBody, "", "", tool.BuildToolMeta(shellEvt))
	require.NotContains(t, shellData, "file_diff")
	require.Contains(t, shellData, "shell_card")
}

// TestGatewayToolMetaPayloadCarriesTheRefusalWords: the live step's tool_meta
// is a field whitelist, so the refusal's own words must be named in it — the
// reloaded rows carry the whole meta and would otherwise know more than the
// live card does.
func TestGatewayToolMetaPayloadCarriesTheRefusalWords(t *testing.T) {
	out := gatewayToolMetaPayload(tool.ToolMeta{
		ToolName: "write_file", Status: "denied", DenyFeedback: "复用 http_server.go",
	})
	require.Equal(t, "复用 http_server.go", out["deny_feedback"])
	require.NotContains(t, gatewayToolMetaPayload(tool.ToolMeta{ToolName: "shell", Status: "completed"}), "deny_feedback")
}
