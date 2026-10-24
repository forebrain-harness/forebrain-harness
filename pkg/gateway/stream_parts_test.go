package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// Stream fragments — the words of an answer or reasoning block, a chunk of a
// tool's output — are pushed to whoever is watching and stored nowhere: the
// words live in the transcript and a tool's output on its completed step.
func TestStreamDeltasAreDeliveredNotStored(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, state.NewSessionStore(db, "main").Ensure(ctx, "session-1", "session-1"))
	store := &state.RunStore{DB: db}
	run, err := store.CreateRun(ctx, "session-1", "a running turn")
	require.NoError(t, err)
	bus := newRunEventBus(store)
	bus.streams = newStreamParts()

	got := make(chan event.RunEvent, 8)
	sub := bus.Subscribe(func(evt event.RunEvent) { got <- evt })
	defer sub.Close()
	sub.Bind("session-1")

	require.NoError(t, bus.Publish(ctx, event.NewRunEvent("", run.ID, "session-1", event.RunEventAssistantDelta, event.AssistantDeltaPayload{Text: "hello"}, time.Now())))
	require.NoError(t, bus.Publish(ctx, event.NewRunEvent("", run.ID, "session-1", event.RunEventToolOutputDelta, event.ToolCallOutputDeltaPayload{StepID: "call-1", ToolName: "shell", Text: "out"}, time.Now())))

	for _, want := range []string{event.RunEventAssistantDelta, event.RunEventToolOutputDelta} {
		select {
		case evt := <-got:
			require.Equal(t, want, evt.Type)
			require.Zero(t, evt.Sequence, "a pushed fragment carries no durable cursor")
			require.Empty(t, evt.ID, "a pushed fragment carries no event id")
		case <-time.After(2 * time.Second):
			t.Fatalf("the subscriber never received the %s event", want)
		}
	}

	// The deltas are pushed and never stored.
	page, err := store.ListSessionEvents(ctx, "session-1", 0, 0, 100)
	require.NoError(t, err)
	require.Empty(t, page.Events, "a stream fragment must never reach the session log")

	// A completion is not a fragment: it still persists.
	require.NoError(t, bus.Publish(ctx, event.NewRunEvent("", run.ID, "session-1", event.RunEventToolCompleted, event.ToolCallCompletedPayload{StepID: "call-1", ToolName: "shell"}, time.Now())))
	page, err = store.ListSessionEvents(ctx, "session-1", 0, 0, 100)
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	require.Equal(t, event.RunEventToolCompleted, page.Events[0].Type)
}

// The in-flight registry holds what the push-only deltas carried so a page that
// arrives mid-turn can be shown it.
func TestStreamPartsHoldInFlightTextAndToolOutput(t *testing.T) {
	parts := newStreamParts()
	partial := &turn.StreamPartial{}
	partial.AppendContent("half an answer")
	partial.AppendReasoning("why")
	parts.registerPrimary("run-1", "session-1", partial)

	// A subagent's text and its running shell, both from events.
	for _, evt := range []event.RunEvent{
		event.NewRunEvent("", "child-run", "session-1", event.RunEventAssistantDelta, event.AssistantDeltaPayload{Text: "searching", AgentID: "task-1"}, time.Now()),
		event.NewRunEvent("", "child-run", "session-1", event.RunEventToolOutputDelta, event.ToolCallOutputDeltaPayload{StepID: "call-1", AgentID: "task-1", Text: "line one"}, time.Now()),
		event.NewRunEvent("", "child-run", "session-1", event.RunEventToolOutputDelta, event.ToolCallOutputDeltaPayload{StepID: "call-1", AgentID: "task-1", Text: "line two"}, time.Now()),
	} {
		parts.observe(evt)
	}

	primary, ok := parts.snapshotRun("run-1", "")
	require.True(t, ok)
	require.Equal(t, "half an answer", primary.Content)
	require.Equal(t, "why", primary.Reasoning)

	child, ok := parts.snapshotRun("child-run", "task-1")
	require.True(t, ok)
	require.Equal(t, "searching", child.Content)
	require.Len(t, child.Tools, 1)
	require.Equal(t, "line one"+"line two", child.Tools[0].Text)

	// A session snapshot names every in-flight stream of the conversation.
	snaps := parts.sessionSnapshots("session-1")
	require.Len(t, snaps, 2)

	// The completed call leaves the registry: its output is on the completed
	// step now, and holding it here would repeat it forever.
	parts.observe(event.NewRunEvent("", "child-run", "session-1", event.RunEventToolCompleted, event.ToolCallCompletedPayload{StepID: "call-1", ToolMeta: event.ToolCallMeta{AgentID: "task-1"}}, time.Now()))
	child, ok = parts.snapshotRun("child-run", "task-1")
	require.True(t, ok)
	require.Empty(t, child.Tools)

	// A run that ends leaves the registry entirely.
	parts.observe(event.NewRunEvent("", "child-run", "session-1", event.RunEventSubagentEnded, event.SubagentEndedPayload{AgentID: "task-1"}, time.Now()))
	_, ok = parts.snapshotRun("child-run", "task-1")
	require.False(t, ok)
}

// A client that reconnects mid-turn is shown the answer being written and the
// running tool's output — not just the durable lifecycle events. A blank turn
// on reconnect is the failure this rules out.
func TestHandleChatWSResumeCarriesInFlightTextAndToolOutput(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, state.NewSessionStore(db, "main").Ensure(ctx, "session-ws", "session-ws"))
	server := &Server{Home: home, Sessions: state.NewSessionStore(db, "main"), RunRT: &state.RunStore{DB: db}}
	rn, err := server.RunRT.CreateRun(ctx, "session-ws", "a running turn")
	require.NoError(t, err)

	// The primary agent is mid-answer; the gateway holds the unfinished block.
	partial := &turn.StreamPartial{}
	partial.AppendContent("half an answer")
	partial.AppendReasoning("why")
	server.streamParts().registerPrimary(rn.ID, "session-ws", partial)
	// A subagent is mid-answer with a running shell, its output only in the
	// in-flight registry.
	for _, evt := range []event.RunEvent{
		event.NewRunEvent("", "child-run", "session-ws", event.RunEventAssistantDelta, event.AssistantDeltaPayload{Text: "searching", AgentID: "task-1"}, time.Now()),
		event.NewRunEvent("", "child-run", "session-ws", event.RunEventToolOutputDelta, event.ToolCallOutputDeltaPayload{StepID: "call-1", AgentID: "task-1", ToolName: "shell", Text: "line one\n"}, time.Now()),
	} {
		require.NoError(t, server.RunEvents().Publish(ctx, evt))
	}

	httpSrv := httptest.NewServer(http.HandlerFunc(server.HandleChatWS))
	defer httpSrv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpSrv.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	var connected wsServerMsg
	require.NoError(t, conn.ReadJSON(&connected))
	require.Equal(t, "connected", connected.Op)

	require.NoError(t, conn.WriteJSON(wsClientMsg{Op: "resume_connection", RequestID: "req-resume", RunID: rn.ID, SessionID: "session-ws", Cursor: 0}))

	primary, child := collectStreamPartials(t, conn, true, 1)
	require.NotNil(t, primary, "the reconnect left the conversation's answer blank")
	require.Equal(t, "half an answer", primary.Content)
	require.Equal(t, "why", primary.Reasoning)
	require.NotNil(t, child, "the reconnect left the subagent's answer blank")
	require.Equal(t, "task-1", child.AgentID)
	require.Equal(t, "searching", child.Content)
	require.Len(t, child.Tools, 1)
	require.Equal(t, "call-1", child.Tools[0].StepID)
	require.Equal(t, "line one\n", child.Tools[0].Text)
}

// A fresh binding to a conversation with a running tool pushes the tool's
// output, and stops holding it once the call completes.
func TestHandleChatWSBindCarriesRunningToolOutput(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, state.NewSessionStore(db, "main").Ensure(ctx, "session-ws", "session-ws"))
	server := &Server{Home: home, Sessions: state.NewSessionStore(db, "main"), RunRT: &state.RunStore{DB: db}}
	childRun, err := server.RunRT.CreateRun(ctx, "session-ws", "a running turn")
	require.NoError(t, err)

	require.NoError(t, server.RunEvents().Publish(ctx, event.NewRunEvent("", childRun.ID, "session-ws", event.RunEventToolOutputDelta, event.ToolCallOutputDeltaPayload{StepID: "call-1", AgentID: "task-1", ToolName: "shell", Text: "working...\n"}, time.Now())))

	httpSrv := httptest.NewServer(http.HandlerFunc(server.HandleChatWS))
	defer httpSrv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpSrv.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	var connected wsServerMsg
	require.NoError(t, conn.ReadJSON(&connected))
	require.Equal(t, "connected", connected.Op)

	require.NoError(t, conn.WriteJSON(wsClientMsg{Op: "bind_session", RequestID: "req-bind", SessionID: "session-ws"}))
	_, child := collectStreamPartials(t, conn, false, 1)
	require.NotNil(t, child, "the binding left the running tool's output blank")
	require.Equal(t, "task-1", child.AgentID)
	require.Len(t, child.Tools, 1)
	require.Equal(t, "working...\n", child.Tools[0].Text)

	// The call completes: the registry drops it, and a later binding no longer
	// carries its output (the completed step is its durable record).
	require.NoError(t, server.RunEvents().Publish(ctx, event.NewRunEvent("", childRun.ID, "session-ws", event.RunEventToolCompleted, event.ToolCallCompletedPayload{StepID: "call-1", ToolName: "shell", ToolMeta: event.ToolCallMeta{AgentID: "task-1"}}, time.Now())))
	snaps := server.streamParts().sessionSnapshots("session-ws")
	for _, snap := range snaps {
		require.Empty(t, snap.Tools, "a completed call must not stay in the in-flight registry")
	}
}

// collectStreamPartials reads until the expected stream_partial frames have
// arrived. wantPrimary says whether the conversation's own in-flight text is
// expected; wantSubagents is how many subagent-tagged frames are expected.
func collectStreamPartials(t *testing.T, conn *websocket.Conn, wantPrimary bool, wantSubagents int) (primary *streamPartialSnapshot, first *streamPartialSnapshot) {
	t.Helper()
	seenSubagents := 0
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		require.NoError(t, conn.SetReadDeadline(deadline))
		var msg wsServerMsg
		if err := conn.ReadJSON(&msg); err != nil {
			break
		}
		if msg.Op != "stream_partial" {
			continue
		}
		snap := decodeStreamPartial(t, msg)
		if snap.AgentID == "" {
			primary = snap
		} else {
			seenSubagents++
			if first == nil {
				first = snap
			}
		}
		if (primary != nil || !wantPrimary) && seenSubagents >= wantSubagents {
			return primary, first
		}
	}
	return primary, first
}

func decodeStreamPartial(t *testing.T, msg wsServerMsg) *streamPartialSnapshot {
	t.Helper()
	raw, err := json.Marshal(msg.Data)
	require.NoError(t, err)
	var data struct {
		AgentID     string                `json:"agent_id"`
		Content     string                `json:"content"`
		Reasoning   string                `json:"reasoning"`
		ToolOutputs []streamPartialOutput `json:"tool_outputs"`
	}
	require.NoError(t, json.Unmarshal(raw, &data))
	return &streamPartialSnapshot{
		RunID:     msg.RunID,
		AgentID:   data.AgentID,
		Content:   data.Content,
		Reasoning: data.Reasoning,
		Tools:     data.ToolOutputs,
	}
}

// A transport retry drops the dead attempt's words: the subagent text this
// registry accumulated is cleared and the retried attempt's deltas start
// over, so a page reconnecting mid-retry is shown the answer from scratch,
// exactly as a page that watched the dead words be withdrawn live. The
// retry's Cleared notice changes nothing — the drop already happened.
func TestStreamPartsTransportRetryDropsTheDeadAttempt(t *testing.T) {
	parts := newStreamParts()

	parts.observe(event.NewRunEvent("", "child-run", "session-1", event.RunEventAssistantDelta, event.AssistantDeltaPayload{Text: "partial words", AgentID: "task-1"}, time.Now()))
	parts.observe(event.NewRunEvent("", "child-run", "session-1", event.RunEventTransportRetry,
		event.TransportRetryPayload{Retry: 1, Total: 10, WaitMs: 500, Error: "read tcp: i/o timeout", AgentID: "task-1"}, time.Now()))
	parts.observe(event.NewRunEvent("", "child-run", "session-1", event.RunEventAssistantDelta, event.AssistantDeltaPayload{Text: "full answer", AgentID: "task-1"}, time.Now()))

	child, ok := parts.snapshotRun("child-run", "task-1")
	require.True(t, ok)
	require.Equal(t, "full answer", child.Content)

	parts.observe(event.NewRunEvent("", "child-run", "session-1", event.RunEventTransportRetry,
		event.TransportRetryPayload{Retry: 1, Total: 10, Cleared: true, AgentID: "task-1"}, time.Now()))
	child, ok = parts.snapshotRun("child-run", "task-1")
	require.True(t, ok)
	require.Equal(t, "full answer", child.Content)
}
