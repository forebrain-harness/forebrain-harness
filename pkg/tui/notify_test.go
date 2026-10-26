package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/stretchr/testify/require"
)

// mcpFailureRecord is one server's failed startup as the registry reports it.
func mcpFailureRecord(name, text string) mcp.ServerRecord {
	return mcp.ServerRecord{
		Name:       name,
		Transport:  "stdio",
		ConnStatus: mcp.ConnStatusError,
		Error:      text,
	}
}

// TestMCPStartupErrorEventCarriesTheIdentityAndTheOriginalText pins the durable
// record of one server's startup failure: what it is about, what to show, and
// the identity that makes a repeat recognizable.
func TestMCPStartupErrorEventCarriesTheIdentityAndTheOriginalText(t *testing.T) {
	const generation = "abc123def456"
	evt := mcpStartupErrorEvent("s1", generation, mcpFailureRecord("docs", "spawn npx: executable file not found in $PATH"), time.Now())

	if evt.Type != event.RunEventTurnError {
		t.Fatalf("type = %q, want the canonical turn-error type", evt.Type)
	}
	if evt.SessionID != "s1" {
		t.Fatalf("session = %q", evt.SessionID)
	}
	if evt.RunID != "" {
		t.Fatalf("run id = %q, want none: no run failed, a server did", evt.RunID)
	}
	if want := mcpFailureEventID("docs", "spawn npx: executable file not found in $PATH"); evt.ID != want {
		t.Fatalf("event id = %q, want the derived, stable identity %q", evt.ID, want)
	}
	var payload event.TurnErrorPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.Title != "mcp docs" {
		t.Fatalf("title = %q, want the server named", payload.Title)
	}
	if payload.Source != event.MCPStartupErrorSource {
		t.Fatalf("source = %q", payload.Source)
	}
	if payload.Server != "docs" || payload.Generation != generation {
		t.Fatalf("payload = %+v, want the server and generation", payload)
	}
	// The underlying text is kept verbatim: it is the only copy that came from
	// the server, and a paraphrase would lose the detail a reader needs.
	if payload.Error != "spawn npx: executable file not found in $PATH" {
		t.Fatalf("error text = %q, want the server's own words", payload.Error)
	}

	// One failure per server and text per conversation: the same words repeat
	// their identity across generations (a relaunch or resume starts MCP again
	// and fails the same way), different words are a new record.
	same := mcpStartupErrorEvent("s1", "999999", mcpFailureRecord("docs", "spawn npx: executable file not found in $PATH"), time.Now())
	if same.ID != evt.ID {
		t.Fatalf("the same failure in a new generation must keep its identity: %q vs %q", same.ID, evt.ID)
	}
	different := mcpStartupErrorEvent("s1", generation, mcpFailureRecord("docs", "boom"), time.Now())
	if different.ID == evt.ID {
		t.Fatal("a failure with other words must be a new record")
	}
}

// TestMCPStartupFailureIsStoredOnceAndShownOnce pins both halves of the failure
// path where they meet: the event reaches the session's log, the UI sees it with
// its heading and its original text, and a producer that reports the same failure
// again neither stores nor shows it twice.
//
// The second half is what a resubscribing surface does: it replays the current
// snapshot, so without a store-level identity the transcript would grow a second
// error block for one failure every time the user switched away and back.
func TestMCPStartupFailureIsStoredOnceAndShownOnce(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer db.Close()
	runs := &state.RunStore{DB: db}
	session := sessionEnv{Home: home, RunSvc: runs}.session()
	require.NoError(t, state.NewSessionStore(db, "main").Ensure(ctx, "s1", "s1"))

	var shown []NewMessageMsg
	session.PrependUINotify(func(m any) {
		if msg, ok := m.(NewMessageMsg); ok && msg.Msg.Kind == MsgKindError {
			shown = append(shown, msg)
		}
	})

	if err := session.publishRunEvent(ctx, mcpStartupErrorEvent("s1", "gen-1", mcpFailureRecord("docs", "connection refused"), time.Now())); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitForQueuedNotifications(t, session)

	first, err := runs.ListSessionEvents(ctx, "s1", 0, 0, 50)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(first.Events) != 1 {
		t.Fatalf("stored events = %d, want 1", len(first.Events))
	}
	if got := len(shown); got != 1 {
		t.Fatalf("shown errors = %d, want 1", got)
	}
	// In the log before it is on screen: the ordering the transcript depends on
	// (a resumed session must show what the live one showed).
	if shown[0].Msg.Title != "mcp docs" {
		t.Fatalf("live heading = %q, want the server named", shown[0].Msg.Title)
	}
	if shown[0].Msg.Content != "connection refused" {
		t.Fatalf("live body = %q, want the original text", shown[0].Msg.Content)
	}

	// The same failure, reported again: nothing new is stored and nothing is
	// shown twice.
	if err := session.publishRunEvent(ctx, mcpStartupErrorEvent("s1", "gen-1", mcpFailureRecord("docs", "connection refused"), time.Now())); err != nil {
		t.Fatalf("re-publish: %v", err)
	}
	waitForQueuedNotifications(t, session)
	after, err := runs.ListSessionEvents(ctx, "s1", 0, 0, 50)
	if err != nil {
		t.Fatalf("list events after repeat: %v", err)
	}
	if len(after.Events) != 1 {
		t.Fatalf("a repeated failure stored %d events, want 1", len(after.Events))
	}
	if got := len(shown); got != 1 {
		t.Fatalf("a repeated failure was shown %d times, want once", got)
	}

	// Two servers failing at once are two independent records, and a retry that
	// fails with other words adds its own.
	if err := session.publishRunEvent(ctx, mcpStartupErrorEvent("s1", "gen-1", mcpFailureRecord("slow", "startup timed out after 30s: context deadline exceeded"), time.Now())); err != nil {
		t.Fatalf("publish other server: %v", err)
	}
	if err := session.publishRunEvent(ctx, mcpStartupErrorEvent("s1", "gen-2", mcpFailureRecord("docs", "connection reset"), time.Now())); err != nil {
		t.Fatalf("publish next generation: %v", err)
	}
	waitForQueuedNotifications(t, session)
	final, err := runs.ListSessionEvents(ctx, "s1", 0, 0, 50)
	if err != nil {
		t.Fatalf("list events final: %v", err)
	}
	if len(final.Events) != 3 {
		t.Fatalf("stored events = %d, want three independent records", len(final.Events))
	}
}

// TestStoredMCPFailureReplaysWithItsHeadingAndText pins the resume half: the
// block a resumed session draws is the one the live session showed, heading and
// original text included, and it comes from the event rather than being
// re-derived from anything that may no longer exist.
func TestStoredMCPFailureReplaysWithItsHeadingAndText(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer db.Close()
	runs := &state.RunStore{DB: db}
	session := sessionEnv{Home: home, RunSvc: runs}.session()
	require.NoError(t, state.NewSessionStore(db, "main").Ensure(ctx, "s1", "s1"))

	evt := mcpStartupErrorEvent("s1", "gen-1", mcpFailureRecord("docs", "spawn npx: executable file not found in $PATH"), time.Now())
	if err := session.publishRunEvent(ctx, evt); err != nil {
		t.Fatalf("publish: %v", err)
	}
	stored, err := runs.ListSessionEvents(ctx, "s1", 0, 0, 50)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(stored.Events) != 1 {
		t.Fatalf("stored events = %d, want 1", len(stored.Events))
	}

	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	reducer := &Reducer{}
	if count := replayTimelineWithReducer(renderer, nil, toRunEvents(stored.Events), reducer, nil); count != 1 {
		t.Fatalf("replayed records = %d, want the failure alone", count)
	}
	if len(renderer.vm.blocks) != 1 {
		t.Fatalf("replayed blocks = %d, want the failure alone", len(renderer.vm.blocks))
	}
	frame := renderer.vm.blocks[0].frame
	if frame.Kind != FrameError {
		t.Fatalf("replayed frame kind = %q, want an error block", frame.Kind)
	}
	if frame.Title != "mcp docs" {
		t.Fatalf("replayed heading = %q, want the server named", frame.Title)
	}
	if frame.Content != "spawn npx: executable file not found in $PATH" {
		t.Fatalf("replayed body = %q, want the original text", frame.Content)
	}
}

// TestMCPStartupFailureBeforeTheSessionExistsIsShownButNotStored pins D16: a
// startup failure observed before the conversation exists has no session to
// belong to, so it is drawn for the user and written nowhere; the same failure
// once the session exists is recorded.
func TestMCPStartupFailureBeforeTheSessionExistsIsShownButNotStored(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	runs := &state.RunStore{DB: db}
	sessions := state.NewSessionStore(db, "main")
	session := sessionEnv{Home: home, SQL: db, SessStore: sessions, RunSvc: runs}.session()

	shown := make(chan NewMessageMsg, 4)
	session.PrependUINotify(func(m any) {
		if msg, ok := m.(NewMessageMsg); ok && msg.Msg.Kind == MsgKindError {
			shown <- msg
		}
	})

	beforeSession := run.MCPSnapshot{Generation: "gen-1", Servers: []mcp.ServerRecord{mcpFailureRecord("docs", "connection refused")}}
	session.recordMCPStartupFailures("ghost", beforeSession)

	got := requireUIMessage(t, shown)
	require.Equal(t, "mcp docs", got.Msg.Title)
	require.Equal(t, "connection refused", got.Msg.Content)

	ghost, err := runs.ListSessionEvents(ctx, "ghost", 0, 0, 50)
	require.NoError(t, err)
	require.Empty(t, ghost.Events, "a failure from before the session existed belongs to no history")

	// The same failure once the conversation exists is recorded.
	require.NoError(t, sessions.Ensure(ctx, "s1", "s1"))
	session.recordMCPStartupFailures("s1", run.MCPSnapshot{
		Generation: "gen-2",
		Servers:    []mcp.ServerRecord{mcpFailureRecord("docs", "connection refused")},
	})
	require.Eventually(t, func() bool {
		page, listErr := runs.ListSessionEvents(ctx, "s1", 0, 0, 50)
		return listErr == nil && len(page.Events) == 1
	}, time.Second, 10*time.Millisecond)
}

// TestMCPStartupFailureBeforeTheSessionExistsIsShownOnceAndItsRecordPaintsNoSecondCopy
// pins the pre-session window against redelivery: the MCP subscription replays
// its snapshot on every registry transition, when the generation settles, and
// again on every later Load, and with no session row the store cannot
// arbitrate — so the surface's own once-memory is what keeps one failure to
// one card. The record that lands when the conversation finally starts is
// stored but never repainted on top of the card already on screen, which keeps
// live and replay at one card each.
func TestMCPStartupFailureBeforeTheSessionExistsIsShownOnceAndItsRecordPaintsNoSecondCopy(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	runs := &state.RunStore{DB: db}
	sessions := state.NewSessionStore(db, "main")
	session := sessionEnv{Home: home, SQL: db, SessStore: sessions, RunSvc: runs}.session()

	shown := make(chan NewMessageMsg, 4)
	session.PrependUINotify(func(m any) {
		if msg, ok := m.(NewMessageMsg); ok && msg.Msg.Kind == MsgKindError {
			shown <- msg
		}
	})
	cardWaiting := func() bool {
		select {
		case <-shown:
			return true
		default:
			return false
		}
	}

	// The failure observed before the conversation exists: drawn once.
	failed := run.MCPSnapshot{
		Generation: "gen-1",
		Servers:    []mcp.ServerRecord{mcpFailureRecord("docs", "connection refused")},
	}
	session.recordMCPStartupFailures("ghost", failed)
	got := requireUIMessage(t, shown)
	require.Equal(t, "mcp docs", got.Msg.Title)
	require.Equal(t, "connection refused", got.Msg.Content)

	// The same snapshot delivered twice more — a registry transition, the
	// settle, a later Load's replay — paints no second copy, and the fact is
	// still stored nowhere (the conversation has not started).
	session.recordMCPStartupFailures("ghost", failed)
	session.recordMCPStartupFailures("ghost", failed)
	require.Never(t, cardWaiting, 300*time.Millisecond, 10*time.Millisecond)
	stored, err := runs.ListSessionEvents(ctx, "ghost", 0, 0, 50)
	require.NoError(t, err)
	require.Empty(t, stored.Events, "a pre-session failure belongs to no history")

	// The conversation starts (a first /model or first message Ensures the
	// row): the next redelivery records the fact exactly once and paints no
	// second card over the one already shown.
	require.NoError(t, sessions.Ensure(ctx, "ghost", "ghost"))
	session.recordMCPStartupFailures("ghost", failed)
	require.Eventually(t, func() bool {
		page, listErr := runs.ListSessionEvents(ctx, "ghost", 0, 0, 50)
		return listErr == nil && len(page.Events) == 1
	}, time.Second, 10*time.Millisecond)
	page, err := runs.ListSessionEvents(ctx, "ghost", 0, 0, 50)
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	require.Equal(t, mcpFailureEventID("docs", "connection refused"), page.Events[0].ID)
	require.Never(t, cardWaiting, 300*time.Millisecond, 10*time.Millisecond)
}

// TestCancelledServerStartupIsNotAFailure pins that a server the operator
// skipped stays out of the transcript's error path: nothing failed, so there is
// nothing to report as one, and no frame is written for it.
func TestCancelledServerStartupIsNotAFailure(t *testing.T) {
	skipped := mcp.ServerRecord{Name: "optional", Transport: "stdio", ConnStatus: mcp.ConnStatusCancelled}
	if skipped.ConnStatus == mcp.ConnStatusError {
		t.Fatal("cancelled must not be an error state")
	}
	// A cancelled record carries no error text, which is what the watch keys on
	// in addition to the state: nothing failed, so nothing is recorded.
	if strings.TrimSpace(skipped.Error) != "" {
		t.Fatalf("a cancelled record carries error text: %q", skipped.Error)
	}
	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	reducer := &Reducer{}
	evt := event.RunEvent{Type: event.RunEventTurnError, Payload: json.RawMessage(`{"source":"mcp","server":"optional","generation":"g"}`)}
	if frames := replayTurnErrorEventFrames(evt); len(frames) != 0 {
		t.Fatalf("a payload with no text produced frames: %+v", frames)
	}
	if count := replayTimelineWithReducer(renderer, nil, []event.RunEvent{evt}, reducer, nil); count != 0 {
		t.Fatalf("a payload with no text replayed %d records", count)
	}
	if len(renderer.vm.blocks) != 0 {
		t.Fatalf("a payload with no text drew %d blocks", len(renderer.vm.blocks))
	}
}

// TestMCPFailureBelongsToTheConversationThatSawItsGenerationStart pins the
// attribution rule: a generation belongs to the Runner, so the conversation that
// saw it start owns its failures, and a conversation the user switches to
// afterwards records nothing — while still seeing the state on the status line.
// Without it a session switch replayed the standing failure into the new
// conversation's log and painted it there, which is how one failure became two
// cards under a resumed transcript.
func TestMCPFailureBelongsToTheConversationThatSawItsGenerationStart(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	runs := &state.RunStore{DB: db}
	sessions := state.NewSessionStore(db, "main")
	session := sessionEnv{Home: home, SQL: db, SessStore: sessions, RunSvc: runs}.session()
	require.NoError(t, sessions.Ensure(ctx, "a", "a"))
	require.NoError(t, sessions.Ensure(ctx, "b", "b"))

	var shown []NewMessageMsg
	session.PrependUINotify(func(m any) {
		if msg, ok := m.(NewMessageMsg); ok && msg.Msg.Kind == MsgKindError {
			shown = append(shown, msg)
		}
	})

	failed := run.MCPSnapshot{
		Generation: "gen-1",
		Servers:    []mcp.ServerRecord{mcpFailureRecord("docs", "connection refused")},
	}
	// The conversation that saw the generation start owns it.
	session.claimMCPGenerationOwner("a", "gen-1")
	session.recordMCPStartupFailures("a", failed)
	require.Eventually(t, func() bool {
		page, listErr := runs.ListSessionEvents(ctx, "a", 0, 0, 50)
		return listErr == nil && len(page.Events) == 1
	}, time.Second, 10*time.Millisecond)

	// Switching away and being told the same standing failure records nothing:
	// the fact belongs to "a", and "b"'s history may not claim it.
	session.recordMCPStartupFailures("b", failed)
	waitForQueuedNotifications(t, session)
	foreign, err := runs.ListSessionEvents(ctx, "b", 0, 0, 50)
	require.NoError(t, err)
	require.Empty(t, foreign.Events, "a standing failure belongs to the conversation that saw it start")
	require.Len(t, shown, 1, "the card is painted for its owner only")
	require.Equal(t, "mcp docs", shown[0].Msg.Title)
}

// TestNewMCPGenerationBelongsToWhoeverSeesItStart covers the other half: a new
// generation is a new fact about the Runner, so the conversation open when it
// starts owns it, and the earlier generation's owner keeps what it already has.
func TestNewMCPGenerationBelongsToWhoeverSeesItStart(t *testing.T) {
	session := sessionEnv{Home: t.TempDir()}.session()
	require.Equal(t, "a", session.claimMCPGenerationOwner("a", "gen-1"))
	require.Equal(t, "b", session.claimMCPGenerationOwner("b", "gen-2"))
	// A late delivery of the older generation does not take ownership back.
	require.Equal(t, "a", session.claimMCPGenerationOwner("b", "gen-1"))
}

// TestOneErrorCardPerScreenWhenLiveAndReplayBothDrawIt pins the drawing rule: one
// error event is one card per screen, whichever path asks for it first, and a
// reset screen may draw it again (a replay must be able to rebuild the history
// it owns).
func TestOneErrorCardPerScreenWhenLiveAndReplayBothDrawIt(t *testing.T) {
	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	evt := event.RunEvent{
		ID:       mcpFailureEventID("docs", "connection refused"),
		Sequence: 1,
		Type:     event.RunEventTurnError,
		Payload:  json.RawMessage(`{"error":"connection refused","title":"mcp docs","source":"mcp"}`),
	}

	// The live path paints first.
	renderer.RenderFrame(Frame{Kind: FrameError, Title: "mcp docs", Content: "connection refused", EventID: evt.ID, Final: true})
	require.Len(t, renderer.vm.blocks, 1)

	// The replay of the same event draws nothing more.
	require.Equal(t, 1, replayTimelineWithWorkers(renderer, nil, []event.RunEvent{evt}, &Reducer{}, nil, nil))
	require.Len(t, renderer.vm.blocks, 1, "the same card was painted twice on one screen")
	require.Equal(t, "connection refused", renderer.vm.blocks[0].frame.Content)

	// A fresh screen must be able to show it again, or a resumed conversation
	// would lose the failures in its history.
	renderer.ViewportResetSession()
	require.Equal(t, 1, replayTimelineWithWorkers(renderer, nil, []event.RunEvent{evt}, &Reducer{}, nil, nil))
	require.Len(t, renderer.vm.blocks, 1)
	require.Equal(t, evt.ID, renderer.vm.blocks[0].frame.EventID)
}

// TestReplayStampsItsCardsWithTheEventIdentity guards the seam itself: every
// card a replay rebuilds from an event carries that event's id, which is what
// makes the live copy and the replayed copy recognizable as one card.
func TestReplayStampsItsCardsWithTheEventIdentity(t *testing.T) {
	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	evt := event.RunEvent{
		ID:       mcpFailureEventID("docs", "spawn npx: executable file not found in $PATH"),
		Sequence: 1,
		Type:     event.RunEventTurnError,
		Payload:  json.RawMessage(`{"error":"spawn npx: executable file not found in $PATH","title":"mcp docs","source":"mcp"}`),
	}
	require.Equal(t, 1, replayTimelineWithWorkers(renderer, nil, []event.RunEvent{evt}, &Reducer{}, nil, nil))
	require.Len(t, renderer.vm.blocks, 1)
	require.Equal(t, evt.ID, renderer.vm.blocks[0].frame.EventID)
}

// TestReplayReplacesTheScreenInsteadOfAppendingToIt pins the replay contract:
// what a replay draws is the whole screen, not an addition to it. /resume only
// looked right because switchStreamSession cleared the screen first; /rewind has
// no such caller, so it pasted the whole transcript on top of the one already
// there.
func TestReplayReplacesTheScreenInsteadOfAppendingToIt(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	sessions := state.NewSessionStore(db, "main")
	runs := &state.RunStore{DB: db}
	session := sessionEnv{Home: home, SQL: db, SessStore: sessions, RunSvc: runs}.session()
	require.NoError(t, sessions.Ensure(ctx, "s1", "s1"))
	_, err = sessions.Append(ctx, "s1", "assistant", "an answer stored before the rewind")
	require.NoError(t, err)

	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	renderer.viewportMode = true
	renderer.RenderFrame(Frame{Kind: FrameTool, Title: "read_file", Content: "stale screen content", Final: true})

	cmds := newCommandController(session, renderer, &stubSelector{}, nil, "")
	resets := 0
	cmds.resetScreen = func() {
		resets++
		renderer.ViewportResetSession()
	}
	cmds.replaySession("s1", nil)
	require.Equal(t, 1, resets, "a replay must take over the screen")

	for _, block := range renderer.vm.blocks {
		require.NotContains(t, block.frame.Content, "stale screen content",
			"the replay appended to the old screen instead of replacing it")
	}
	replayed := false
	for _, block := range renderer.vm.blocks {
		if strings.Contains(block.frame.Content, "an answer stored before the rewind") {
			replayed = true
		}
	}
	require.True(t, replayed, "the replay did not draw the stored conversation: %+v", renderer.vm.blocks)

	// A second replay of the same session (the /rewind shape) must not double it.
	before := len(renderer.vm.blocks)
	cmds.replaySession("s1", nil)
	require.Equal(t, before, len(renderer.vm.blocks), "a second replay appended a second copy")
}

// TestReplayToleratesAnUnwiredReset pins the non-terminal path: a controller
// assembled without a screen to reset (tests, embeds) must still replay.
func TestReplayToleratesAnUnwiredReset(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	sessions := state.NewSessionStore(db, "main")
	session := sessionEnv{Home: home, SQL: db, SessStore: sessions, RunSvc: &state.RunStore{DB: db}}.session()
	require.NoError(t, sessions.Ensure(ctx, "s1", "s1"))
	_, err = sessions.Append(ctx, "s1", "assistant", "an answer")
	require.NoError(t, err)

	var out bytes.Buffer
	cmds := newCommandController(session, NewRenderer(&out, &out), &stubSelector{}, nil, "")
	cmds.replaySession("s1", nil)
}

// TestMCPStartupStatusLineNamesTheServersAndOnlyPromisesWhatEscapeCanDo pins the
// status line's content: how far along the startup is, which servers are still
// connecting, and the skip hint only when there is an optional server left to
// skip.
func TestMCPStartupStatusLineNamesTheServersAndOnlyPromisesWhatEscapeCanDo(t *testing.T) {
	started := time.Now().Add(-3 * time.Second)
	connecting := func(name string, required bool) mcp.ServerRecord {
		return mcp.ServerRecord{Name: name, ConnStatus: mcp.ConnStatusConnecting, Required: required}
	}

	// Two optional servers in flight: both are named, and the skip is offered.
	progress := run.MCPStartupProgress{Total: 3, InFlight: 2, OptionalInFlight: 2, Connected: 1, StartedAt: started}
	line := mcpStartupStatusText(progress, run.MCPSnapshot{Servers: []mcp.ServerRecord{
		{Name: "done", ConnStatus: mcp.ConnStatusConnected},
		connecting("docs", false),
		connecting("slow", false),
	}})
	for _, want := range []string{"mcp 1/3", "connecting docs, slow", "esc to skip"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q is missing %q", line, want)
		}
	}

	// Only a required server left: it is still named, and the skip hint is gone
	// because Escape cannot do it.
	requiredOnly := run.MCPStartupProgress{Total: 2, InFlight: 1, RequiredInFlight: 1, Connected: 1, StartedAt: started}
	line = mcpStartupStatusText(requiredOnly, run.MCPSnapshot{Servers: []mcp.ServerRecord{
		{Name: "done", ConnStatus: mcp.ConnStatusConnected},
		connecting("needed", true),
	}})
	if !strings.Contains(line, "connecting needed") {
		t.Fatalf("line %q must name the required server still connecting", line)
	}
	if strings.Contains(line, "esc to skip") {
		t.Fatalf("line %q promises a skip that cannot happen", line)
	}

	// Settled, or settled-with-a-failure: no line at all, so nothing is left on
	// screen claiming to wait.
	for _, settled := range []run.MCPStartupProgress{
		{Total: 2, Connected: 2, StartedAt: started},
		{Total: 2, Connected: 1, Errored: 1, StartedAt: started},
		{Total: 2, Connected: 1, Skipped: 1, StartedAt: started},
	} {
		if line := mcpStartupStatusText(settled, run.MCPSnapshot{}); line != "" {
			t.Fatalf("a settled generation renders %q", line)
		}
	}
}

// TestTransientStatusSourcesDoNotClearEachOther pins the reason the status slot
// became source-aware: an MCP startup and a conversation turn are true at the
// same time, and with one slot whichever finished first blanked the other.
func TestTransientStatusSourcesDoNotClearEachOther(t *testing.T) {
	r := NewRenderer(nil, nil)
	cursor := 0
	state := ComposerRenderState{Cursor: &cursor}
	painted := func() string {
		return stripANSI(strings.Join(r.buildComposerBlock(state, 120).lines, "\n"))
	}

	r.RenderTransientStatus(transientSourceWorking, "Working (5s • esc to interrupt)")
	r.RenderTransientStatus(transientSourceMCP, "mcp 0/2 · connecting docs, slow · esc to skip")
	got := painted()
	if !strings.Contains(got, "mcp 0/2") {
		t.Fatalf("the MCP line must win while it is live:\n%s", got)
	}
	if strings.Contains(got, "Working (5s") {
		t.Fatalf("both lines painted at once:\n%s", got)
	}

	// The MCP startup finishing leaves the turn's line on screen: the turn is
	// still running and the reader still needs to know it.
	r.FinishTransientStatus(transientSourceMCP)
	if got := painted(); !strings.Contains(got, "Working (5s") {
		t.Fatalf("finishing the MCP line cleared the working line too:\n%s", got)
	}

	// Symmetrically, the turn ending does not clear an MCP startup.
	r.RenderTransientStatus(transientSourceMCP, "mcp 1/2 · connecting slow · esc to skip")
	r.FinishTransientStatus(transientSourceWorking)
	if got := painted(); !strings.Contains(got, "mcp 1/2") {
		t.Fatalf("finishing the working line cleared the MCP line:\n%s", got)
	}

	// A language-server install outranks the working line but not the MCP
	// startup. The MCP source has to be finished first, or it is still the
	// line on screen.
	r.FinishTransientStatus(transientSourceMCP)
	r.RenderTransientStatus(transientSourceWorking, "Working (9s • esc to interrupt)")
	r.RenderTransientStatus(transientSourceLSP, "lsp: installing rust-analyzer · fetching")
	got = painted()
	if !strings.Contains(got, "lsp: installing") || strings.Contains(got, "Working (9s") {
		t.Fatalf("the lsp line must outrank the working line while MCP is quiet:\n%s", got)
	}
	r.RenderTransientStatus(transientSourceMCP, "mcp 1/2 · connecting slow")
	got = painted()
	if !strings.Contains(got, "mcp 1/2") || strings.Contains(got, "lsp: installing") {
		t.Fatalf("the MCP line must outrank the lsp install:\n%s", got)
	}

	// Finishing one source leaves the others, and the session-switch clear takes
	// them all.
	r.FinishTransientStatus(transientSourceLSP)
	if got := painted(); !strings.Contains(got, "mcp 1/2") {
		t.Fatalf("finishing the lsp install cleared the MCP line:\n%s", got)
	}
	r.FinishAllTransientStatus()
	got = painted()
	if strings.Contains(got, "mcp 1/2") || strings.Contains(got, "Working (") || strings.Contains(got, "lsp: installing") {
		t.Fatalf("FinishAllTransientStatus left lines behind:\n%s", got)
	}
}

// toRunEvents projects stored session events into the canonical event shape the
// replay path consumes, which is how a resumed session reads them.
func toRunEvents(events []state.SessionEvent) []event.RunEvent {
	out := make([]event.RunEvent, 0, len(events))
	for _, evt := range events {
		out = append(out, event.RunEvent{
			ID: evt.ID, Sequence: evt.Sequence, SchemaVersion: event.RunEventSchemaVersion,
			RunID: evt.RunID, SessionID: evt.SessionID, Type: evt.Type,
			Payload: evt.Payload, CreatedAt: evt.CreatedAt,
		})
	}
	return out
}

// waitForQueuedNotifications waits for the session's serialized notification
// worker to drain what producers enqueued. It watches the queue rather than
// sleeping a fixed amount, so a slow machine is not a flake.
func waitForQueuedNotifications(t *testing.T, session *ChatSession) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		session.uiDispatchMu.Lock()
		pending := len(session.uiDispatchQueue)
		session.uiDispatchMu.Unlock()
		if pending == 0 {
			// The worker takes the item off the queue before delivering it, so an
			// empty queue is not yet proof the callback ran.
			time.Sleep(5 * time.Millisecond)
			session.uiDispatchMu.Lock()
			drained := len(session.uiDispatchQueue) == 0
			session.uiDispatchMu.Unlock()
			if drained {
				return
			}
			continue
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("notification queue never drained")
}

// cancellingSession is a fakeSession with an engine behind it that can stop a
// pending continuation.
type cancellingSession struct {
	*fakeSession
	pending           bool
	pendingSubagent   bool
	cancelled         []string
	cancelledSubagent []string
	continuedSubagent []string
}

func (s *cancellingSession) CancelAutoContinue(sessionID string) bool {
	s.cancelled = append(s.cancelled, sessionID)
	was := s.pending
	s.pending = false
	return was
}

func (s *cancellingSession) CancelSubagentAutoContinue(sessionID, agentKey string) bool {
	s.cancelledSubagent = append(s.cancelledSubagent, sessionID+"|"+agentKey)
	was := s.pendingSubagent
	s.pendingSubagent = false
	return was
}

func (s *cancellingSession) ContinueSubagentAuto(plan turn.AutoContinuePlan, prompt string) error {
	s.continuedSubagent = append(s.continuedSubagent, plan.AgentKey+"|"+prompt)
	return nil
}

func scheduledMsg(at time.Time) AutoContinueScheduledMsg {
	return AutoContinueScheduledMsg{SessionID: "s1", RunID: "run-1", ContinueAt: at, Code: string(llm.ExplainRateLimitQuota), Attempt: 1}
}

// statusLines is the transcript's status frames, in order.
func statusLines(r *Renderer) string {
	return statusLinesFor(r, "")
}

// statusLinesFor is the status frames of one view's transcript ("" is the
// conversation's own, a roster key that subagent's), in order. A subagent's
// frames are retained in its own transcript, not the conversation's.
func statusLinesFor(r *Renderer, view string) string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	vm := &r.vm
	if key := strings.TrimSpace(view); key != "" {
		if vm = r.perAgentVM[key]; vm == nil {
			return ""
		}
	}
	lines := []string{}
	for _, block := range vm.blocks {
		if block.frame.Kind == FrameStatus {
			lines = append(lines, block.frame.Title)
		}
	}
	return strings.Join(lines, "\n")
}

func composerText(r *Renderer) string {
	cursor := 0
	return stripANSI(strings.Join(r.buildComposerBlock(ComposerRenderState{Cursor: &cursor}, 120).lines, "\n"))
}

func TestAutoContinueTranscriptLine(t *testing.T) {
	renderer := NewRenderer(nil, nil)
	state := &streamState{sessionID: "s1", session: &cancellingSession{fakeSession: &fakeSession{}}}
	at := time.Now().Add(3 * time.Hour)

	if !handleAutoContinueNotification(renderer, state, scheduledMsg(at)) {
		t.Fatal("scheduled message not handled")
	}
	if entry := state.autoContinueFor(""); entry == nil || !entry.announced {
		t.Fatalf("an idle loop announces at once: %+v", entry)
	}
	clock := autoContinueClock(at, time.Now())
	transcript := statusLines(renderer)
	if want := "Usage limit reached · continuing automatically at " + clock + " · esc or type to cancel"; !strings.Contains(transcript, want) {
		t.Fatalf("transcript = %q, want %q", transcript, want)
	}
	// The wait is announced in the transcript and nowhere else: the composer
	// footer carries no second copy of it.
	if strings.Contains(composerText(renderer), "continuing automatically") {
		t.Fatalf("composer repeats the notice: %q", composerText(renderer))
	}
}

func TestAutoContinueWaitsForTheTurnToEndBeforeAnnouncing(t *testing.T) {
	renderer := NewRenderer(nil, nil)
	state := &streamState{sessionID: "s1", activeForeground: &foregroundTurn{}}
	handleAutoContinueNotification(renderer, state, scheduledMsg(time.Now().Add(time.Hour)))
	if strings.Contains(statusLines(renderer), "continuing automatically") {
		t.Fatal("announced above the error of the turn still ending")
	}
	state.activeForeground = nil
	state.announceAutoContinue(renderer)
	state.announceAutoContinue(renderer)
	if n := strings.Count(statusLines(renderer), "continuing automatically"); n != 1 {
		t.Fatalf("announced %d times, want once", n)
	}
}

func TestAutoContinueIgnoresAnotherSession(t *testing.T) {
	renderer := NewRenderer(nil, nil)
	state := &streamState{sessionID: "other"}
	handleAutoContinueNotification(renderer, state, scheduledMsg(time.Now().Add(time.Hour)))
	if state.autoContinueFor("") != nil {
		t.Fatal("took on another session's continuation")
	}
}

func TestAutoContinueEscapeCancelsThroughTheEngine(t *testing.T) {
	renderer := NewRenderer(nil, nil)
	session := &cancellingSession{fakeSession: &fakeSession{}, pending: true}
	state := &streamState{sessionID: "s1", session: session}
	handleAutoContinueNotification(renderer, state, scheduledMsg(time.Now().Add(time.Hour)))

	if !state.cancelAutoContinue(renderer) {
		t.Fatal("cancel reported nothing to cancel")
	}
	if len(session.cancelled) != 1 || session.cancelled[0] != "s1" {
		t.Fatalf("engine cancels = %v", session.cancelled)
	}
	// The engine's own cancellation event is what clears the wait.
	handleAutoContinueNotification(renderer, state, AutoContinueCancelledMsg{SessionID: "s1", Reason: turn.AutoContinueCancelledByUser})
	if state.autoContinueFor("") != nil {
		t.Fatal("still pending after the cancellation event")
	}
	if !strings.Contains(statusLines(renderer), "Auto-continue cancelled") {
		t.Fatalf("transcript does not say the continuation was cancelled: %q", statusLines(renderer))
	}
	if state.cancelAutoContinue(renderer) {
		t.Fatal("a second Escape found something to cancel")
	}
}

func TestAutoContinueSupersededQuietly(t *testing.T) {
	renderer := NewRenderer(nil, nil)
	state := &streamState{sessionID: "s1"}
	handleAutoContinueNotification(renderer, state, scheduledMsg(time.Now().Add(time.Hour)))
	before := statusLines(renderer)
	handleAutoContinueNotification(renderer, state, AutoContinueCancelledMsg{SessionID: "s1", Reason: turn.AutoContinueSuperseded})
	if state.autoContinueFor("") != nil {
		t.Fatal("still pending after being superseded")
	}
	if statusLines(renderer) != before {
		t.Fatalf("a turn the reader sent is its own explanation: %q", statusLines(renderer))
	}
}

func TestAutoContinueDueBecomesTheNextSubmission(t *testing.T) {
	renderer := NewRenderer(nil, nil)
	state := &streamState{sessionID: "s1"}
	handleAutoContinueNotification(renderer, state, scheduledMsg(time.Now().Add(time.Hour)))
	handleAutoContinueNotification(renderer, state, AutoContinueDueMsg{SessionID: "s1", Prompt: turn.AutoContinuePrompt})
	if !state.autoContinueWake() {
		t.Fatal("a due continuation must wake the idle loop")
	}
	sub, ok := state.takeAutoContinueDue()
	if !ok || llm.TextContent(sub.Parts...) != turn.AutoContinuePrompt || sub.DisplayText != turn.AutoContinuePrompt {
		t.Fatalf("submission = %+v, ok %v", sub, ok)
	}
	if _, again := state.takeAutoContinueDue(); again {
		t.Fatal("the continuation was handed out twice")
	}
	if state.autoContinueFor("") != nil {
		t.Fatal("the wait outlived the continuation")
	}
}

// A reader who pressed Escape as the timer fired has cleared the wait; the
// continuation that raced the key is dropped rather than run.
func TestAutoContinueDueAfterALocalCancelIsDropped(t *testing.T) {
	renderer := NewRenderer(nil, nil)
	session := &cancellingSession{fakeSession: &fakeSession{}, pending: false}
	state := &streamState{sessionID: "s1", session: session}
	handleAutoContinueNotification(renderer, state, scheduledMsg(time.Now().Add(time.Hour)))
	if !state.cancelAutoContinue(renderer) {
		t.Fatal("Escape must be consumed while the notice is up")
	}
	handleAutoContinueNotification(renderer, state, AutoContinueDueMsg{SessionID: "s1", Prompt: turn.AutoContinuePrompt})
	if state.autoContinueWake() {
		t.Fatal("ran a continuation the reader cancelled")
	}
}

func TestAutoContinueLeftBehindOnSessionSwitch(t *testing.T) {
	renderer := NewRenderer(nil, nil)
	session := &cancellingSession{fakeSession: &fakeSession{}, pending: true}
	state := &streamState{sessionID: "s1", session: session}
	handleAutoContinueNotification(renderer, state, scheduledMsg(time.Now().Add(time.Hour)))
	state.leaveAutoContinue()
	if len(session.cancelled) != 1 || state.autoContinueFor("") != nil || state.autoContinueWake() {
		t.Fatalf("cancels %v, pending %+v", session.cancelled, state.autoContinueFor(""))
	}
}

// A subagent's continuation belongs to that subagent's view: its transcript
// line shows there and nowhere else, typing or Esc in that view cancels only
// it, and the conversation's own wait is never touched (README rule 9).
//
// The per-view composer notice this test used to read was removed by owner
// decision (a second copy of the wait, under the composer, with its own
// wording); the view a wait belongs to is now carried by the transcript frame's
// AgentID alone.
func TestAutoContinueLineBelongsToTheViewOfItsAgent(t *testing.T) {
	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)
	session := &cancellingSession{fakeSession: &fakeSession{}, pending: true, pendingSubagent: true}
	state := &streamState{sessionID: "s1", session: session}
	at := time.Now().Add(time.Hour)

	handleAutoContinueNotification(renderer, state, scheduledMsg(at))
	handleAutoContinueNotification(renderer, state, AutoContinueScheduledMsg{SessionID: "s1", AgentID: "task-a", ContinueAt: at, Code: string(llm.ExplainRateLimitQuota), Attempt: 1})

	if state.autoContinueFor("") == nil || state.autoContinueFor("task-a") == nil {
		t.Fatalf("both waits must be held: %+v", state.autoContinue)
	}
	if !strings.Contains(statusLinesFor(renderer, ""), "continuing automatically") {
		t.Fatalf("the conversation's transcript line is missing: %q", statusLinesFor(renderer, ""))
	}
	renderer.SetActiveView("task-a")
	if !strings.Contains(statusLinesFor(renderer, "task-a"), "continuing automatically") {
		t.Fatalf("the subagent's transcript line is missing: %q", statusLinesFor(renderer, "task-a"))
	}
	// Esc in the subagent's view cancels only its wait.
	if !state.cancelAutoContinue(renderer) {
		t.Fatal("Esc in the subagent's view found nothing to cancel")
	}
	if len(session.cancelledSubagent) != 1 || session.cancelledSubagent[0] != "s1|task-a" {
		t.Fatalf("subagent cancels = %v", session.cancelledSubagent)
	}
	if len(session.cancelled) != 0 {
		t.Fatalf("the conversation's wait was cancelled too: %v", session.cancelled)
	}
	// The engine's own cancellation ends that view's wait only.
	handleAutoContinueNotification(renderer, state, AutoContinueCancelledMsg{SessionID: "s1", AgentID: "task-a", Reason: turn.AutoContinueCancelledByUser})
	if state.autoContinueFor("task-a") != nil {
		t.Fatal("the subagent's wait survived its cancellation event")
	}
	if state.autoContinueFor("") == nil {
		t.Fatal("the conversation's wait was dropped by the subagent's cancel")
	}
	renderer.SetActiveView("")
	if !strings.Contains(statusLinesFor(renderer, ""), "continuing automatically") {
		t.Fatalf("the conversation's transcript line was lost: %q", statusLinesFor(renderer, ""))
	}
}

// A subagent's due continuation is delivered to that subagent, and only while
// its wait still stands: a reader who pressed Esc as the timer fired has
// cleared it, and the racing continuation is dropped.
func TestTUIContinuesASubagentOnlyWhileItsWaitStands(t *testing.T) {
	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)
	session := &cancellingSession{fakeSession: &fakeSession{}}
	state := &streamState{sessionID: "s1", session: session}
	renderer.SetActiveView("task-a")

	handleAutoContinueNotification(renderer, state, AutoContinueScheduledMsg{SessionID: "s1", AgentID: "task-a", ContinueAt: time.Now().Add(time.Hour), Code: string(llm.ExplainRateLimitQuota), Attempt: 1})
	if !state.cancelAutoContinue(renderer) {
		t.Fatal("Esc must be consumed while the wait is up")
	}
	handleAutoContinueNotification(renderer, state, AutoContinueDueMsg{SessionID: "s1", AgentKey: "task-a", Prompt: turn.AutoContinuePrompt})
	if len(session.continuedSubagent) != 0 {
		t.Fatalf("ran a continuation the reader cancelled: %v", session.continuedSubagent)
	}

	handleAutoContinueNotification(renderer, state, AutoContinueScheduledMsg{SessionID: "s1", AgentID: "task-a", ContinueAt: time.Now().Add(time.Hour), Code: string(llm.ExplainRateLimitQuota), Attempt: 1})
	handleAutoContinueNotification(renderer, state, AutoContinueDueMsg{SessionID: "s1", AgentKey: "task-a", Prompt: turn.AutoContinuePrompt})
	if len(session.continuedSubagent) != 1 || session.continuedSubagent[0] != "task-a|"+turn.AutoContinuePrompt {
		t.Fatalf("continued = %v", session.continuedSubagent)
	}
	if state.autoContinueFor("task-a") != nil {
		t.Fatal("the wait outlived the continuation")
	}
}

func TestAutoContinueUIMessageFromEvents(t *testing.T) {
	at := time.Date(2026, 9, 30, 2, 10, 5, 0, time.UTC)
	evt := event.NewRunEvent("e1", "run-1", "s1", event.RunEventAutoContinueScheduled,
		turn.AutoContinuePlan{ContinueAt: at, Code: "rate_limit_throttle", Attempt: 2}.Payload(), time.Now())
	msg, ok := autoContinueUIMessage(evt)
	scheduled, isScheduled := msg.(AutoContinueScheduledMsg)
	if !ok || !isScheduled || !scheduled.ContinueAt.Equal(at) || scheduled.Code != "rate_limit_throttle" || scheduled.SessionID != "s1" {
		t.Fatalf("message = %#v", msg)
	}
	evt = event.NewRunEvent("e2", "run-1", "s1", event.RunEventAutoContinueCancelled,
		event.AutoContinueCancelledPayload{Reason: turn.AutoContinueUnavailable, Error: "closed"}, time.Now())
	msg, _ = autoContinueUIMessage(evt)
	if cancelled, ok := msg.(AutoContinueCancelledMsg); !ok || cancelled.Reason != turn.AutoContinueUnavailable || cancelled.Error != "closed" {
		t.Fatalf("message = %#v", msg)
	}
	if _, ok := autoContinueUIMessage(event.NewRunEvent("e3", "run-1", "s1", event.RunEventAutoContinueStarted, nil, time.Now())); ok {
		t.Fatal("started is acted on through the due message, not on its own")
	}
}

func TestAutoContinueHeadlineNamesAThrottle(t *testing.T) {
	now := time.Date(2026, 9, 29, 23, 0, 0, 0, time.UTC)
	got := autoContinueTranscriptText(string(llm.ExplainRateLimitThrottle), now.Add(2*time.Minute), now, true)
	if got != "Rate limited · continuing automatically at 11:02pm · esc or type to cancel" {
		t.Fatalf("transcript line = %q", got)
	}
	// A view with no input box can only be left, not typed out of.
	got = autoContinueTranscriptText(string(llm.ExplainRateLimitThrottle), now.Add(2*time.Minute), now, false)
	if got != "Rate limited · continuing automatically at 11:02pm · esc to cancel" {
		t.Fatalf("transcript line without typing = %q", got)
	}
}

func TestAutoContinueClock(t *testing.T) {
	loc := time.FixedZone("test", 8*3600)
	now := time.Date(2026, 9, 29, 23, 0, 0, 0, loc)
	for _, tc := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 9, 29, 23, 40, 0, 0, loc), "11:40pm"},
		{time.Date(2026, 9, 30, 2, 10, 0, 0, loc), "Wed 2:10am"},
		{time.Date(2026, 10, 9, 9, 0, 0, 0, loc), "Oct 9, 9:00am"},
		{time.Date(2027, 1, 2, 9, 0, 0, 0, loc), "Jan 2 2027, 9:00am"},
		// A time given in another zone is read on the reader's clock.
		{time.Date(2026, 9, 29, 18, 10, 0, 0, time.UTC), "Wed 2:10am"},
	} {
		if got := autoContinueClock(tc.at, now); got != tc.want {
			t.Errorf("autoContinueClock(%v) = %q, want %q", tc.at, got, tc.want)
		}
	}
}

func TestContinueAfterUsageLimitNeedsAnAttachedSurface(t *testing.T) {
	s := &ChatSession{}
	plan := turn.AutoContinuePlan{SessionID: "s1"}
	if err := s.continueAfterUsageLimit(context.Background(), plan, turn.AutoContinuePrompt); err != turn.ErrAutoContinueUnavailable {
		t.Fatalf("detached surface: err = %v, want ErrAutoContinueUnavailable", err)
	}
	got := make(chan any, 1)
	s.PrependUINotify(func(m any) {
		select {
		case got <- m:
		default:
		}
	})
	defer s.stopUINotificationDispatcher()
	if err := s.continueAfterUsageLimit(context.Background(), plan, turn.AutoContinuePrompt); err != nil {
		t.Fatalf("attached surface: %v", err)
	}
	select {
	case m := <-got:
		due, ok := m.(AutoContinueDueMsg)
		if !ok || due.SessionID != "s1" || due.Prompt != turn.AutoContinuePrompt {
			t.Fatalf("notification = %#v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the continuation never reached the event loop")
	}
}

// One language-server recommendation reaches the UI once: the session log's
// identity is what makes a resubscribing surface quiet, not the surface's
// own memory (spec §10.1).
func TestPublishRunEventLSPRecommendationNotifies(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer db.Close()
	runs := &state.RunStore{DB: db}
	session := sessionEnv{Home: home, RunSvc: runs}.session()
	require.NoError(t, state.NewSessionStore(db, "main").Ensure(ctx, "s1", "s1"))

	var shown []LSPRecommendationMsg
	session.PrependUINotify(func(m any) {
		if msg, ok := m.(LSPRecommendationMsg); ok {
			shown = append(shown, msg)
		}
	})

	rec := event.LSPRecommendation{
		ID: "lsprec-9f1c2a7b", ServerID: "gopls", DisplayName: "gopls",
		Languages: []string{"Go"}, TriggerExtension: ".go", Mode: "enable",
		BinaryPath: "/usr/local/bin/gopls", Version: "v0.23.0",
	}
	// No run id: the event belongs to the conversation, not to one turn.
	evt := event.NewRunEvent(rec.ID, "", "s1", event.RunEventLSPRecommendation, rec, time.Now())
	require.NoError(t, session.publishRunEvent(ctx, evt))
	waitForQueuedNotifications(t, session)
	if len(shown) != 1 || shown[0].Rec.ID != rec.ID {
		t.Fatalf("shown = %+v, want the one recommendation", shown)
	}

	// The same event id a second time — a surface that resubscribed — is
	// already in the log and must not open a second modal.
	require.NoError(t, session.publishRunEvent(ctx, evt))
	waitForQueuedNotifications(t, session)
	if len(shown) != 1 {
		t.Fatalf("a repeat of %s was shown again: %+v", rec.ID, shown)
	}
}

// captureFirstTokenStore stands in for the state store the engine writes to,
// recording what it was asked to keep.
type captureFirstTokenStore struct {
	runID string
	wait  time.Duration
	calls int
}

func (c *captureFirstTokenStore) StampRunFirstToken(_ context.Context, runID string, wait time.Duration) error {
	c.calls++
	c.runID = runID
	c.wait = wait
	return nil
}

// The TUI's first-token store writes the wait through to the state store and
// forwards the very same value to the UI, so the live "Working" line shows
// what the run persisted — no surface measures the wait a second time.
func TestTUIFirstTokenStoreForwardsToStoreAndUI(t *testing.T) {
	delegate := &captureFirstTokenStore{}
	var gotRunID string
	var gotWait time.Duration
	store := tuiFirstTokenStore{
		store:  delegate,
		notify: func(runID string, wait time.Duration) { gotRunID, gotWait = runID, wait },
	}
	if err := store.StampRunFirstToken(context.Background(), "run-7", 1200*time.Millisecond); err != nil {
		t.Fatalf("StampRunFirstToken error: %v", err)
	}
	if delegate.calls != 1 || delegate.runID != "run-7" || delegate.wait != 1200*time.Millisecond {
		t.Fatalf("delegate = %+v, want the wait passed through unchanged", delegate)
	}
	if gotRunID != "run-7" || gotWait != 1200*time.Millisecond {
		t.Fatalf("notify = (%q, %v), want the same value the store kept", gotRunID, gotWait)
	}
}

// TestSupersededAbandonedErrorIsNotReplayed pins the resume half of a run that
// was reaped as abandoned while its process was alive and then reported its
// own ending: the real error is drawn, the reap's guess is not; a run with only
// the reap's error keeps it.
func TestSupersededAbandonedErrorIsNotReplayed(t *testing.T) {
	mk := func(runID string, p event.TurnErrorPayload) event.RunEvent {
		body, _ := json.Marshal(p)
		return event.RunEvent{RunID: runID, Type: event.RunEventTurnError, Payload: body}
	}
	abandoned := event.TurnErrorPayload{Error: turn.AbandonedRunReason, Detail: &event.TurnErrorDetail{Code: "run_abandoned"}}
	events := []event.RunEvent{
		mk("r1", abandoned),
		mk("r1", event.TurnErrorPayload{Error: "dial tcp: no such host"}),
		mk("r2", abandoned),
	}
	got := supersededAbandonedErrors(events)
	if _, ok := got[0]; !ok || len(got) != 1 {
		t.Fatalf("superseded = %v, want only the abandoned error of the run that also has a real one", got)
	}
}
