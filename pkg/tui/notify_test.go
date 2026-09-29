package tui

import (
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
	if evt.ID != "mcp:"+generation+":docs:error" {
		t.Fatalf("event id = %q, want the derived, stable identity", evt.ID)
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

	// One failure per server per generation: the same one repeats its identity,
	// a new generation does not.
	again := mcpStartupErrorEvent("s1", generation, mcpFailureRecord("docs", "boom"), time.Now())
	if again.ID != evt.ID {
		t.Fatalf("the same failure in one generation must have one identity: %q vs %q", again.ID, evt.ID)
	}
	nextGen := mcpStartupErrorEvent("s1", "999999", mcpFailureRecord("docs", "boom"), time.Now())
	if nextGen.ID == evt.ID {
		t.Fatal("a failure in a new generation must be a new record")
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

	// Two servers failing at once are two independent records, and a later
	// generation adds its own — which is what makes a retry visible.
	if err := session.publishRunEvent(ctx, mcpStartupErrorEvent("s1", "gen-1", mcpFailureRecord("slow", "startup timed out after 30s: context deadline exceeded"), time.Now())); err != nil {
		t.Fatalf("publish other server: %v", err)
	}
	if err := session.publishRunEvent(ctx, mcpStartupErrorEvent("s1", "gen-2", mcpFailureRecord("docs", "connection refused"), time.Now())); err != nil {
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

	// A migration outranks the working line but not the MCP startup. The MCP
	// source has to be finished first, or it is still the line on screen.
	r.FinishTransientStatus(transientSourceMCP)
	r.RenderTransientStatus(transientSourceWorking, "Working (9s • esc to interrupt)")
	r.RenderTransientStatus(transientSourceMigrate, "migrate: importing…")
	got = painted()
	if !strings.Contains(got, "migrate:") || strings.Contains(got, "Working (9s") {
		t.Fatalf("migrate must outrank the working line while MCP is quiet:\n%s", got)
	}
	r.RenderTransientStatus(transientSourceMCP, "mcp 1/2 · connecting slow")
	got = painted()
	if !strings.Contains(got, "mcp 1/2") || strings.Contains(got, "migrate:") {
		t.Fatalf("the MCP line must outrank a migration:\n%s", got)
	}

	// Finishing one source leaves the others, and the session-switch clear takes
	// them all.
	r.FinishTransientStatus(transientSourceMigrate)
	if got := painted(); !strings.Contains(got, "mcp 1/2") {
		t.Fatalf("finishing the migration cleared the MCP line:\n%s", got)
	}
	r.FinishAllTransientStatus()
	got = painted()
	if strings.Contains(got, "mcp 1/2") || strings.Contains(got, "Working (") || strings.Contains(got, "migrate:") {
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
	pending   bool
	cancelled []string
}

func (s *cancellingSession) CancelAutoContinue(sessionID string) bool {
	s.cancelled = append(s.cancelled, sessionID)
	was := s.pending
	s.pending = false
	return was
}

func scheduledMsg(at time.Time) AutoContinueScheduledMsg {
	return AutoContinueScheduledMsg{SessionID: "s1", RunID: "run-1", ContinueAt: at, Code: string(llm.ExplainRateLimitQuota), Attempt: 1}
}

// statusLines is the transcript's status frames, in order.
func statusLines(r *Renderer) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	lines := []string{}
	for _, block := range r.vm.blocks {
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

func TestAutoContinueNoticeAndTranscriptLine(t *testing.T) {
	renderer := NewRenderer(nil, nil)
	state := &streamState{sessionID: "s1", session: &cancellingSession{fakeSession: &fakeSession{}}}
	at := time.Now().Add(3 * time.Hour)

	if !handleAutoContinueNotification(renderer, state, scheduledMsg(at)) {
		t.Fatal("scheduled message not handled")
	}
	if state.autoContinue == nil || !state.autoContinue.announced {
		t.Fatalf("an idle loop announces at once: %+v", state.autoContinue)
	}
	clock := autoContinueClock(at, time.Now())
	transcript := statusLines(renderer)
	if want := "Usage limit reached · continuing automatically at " + clock + " · esc or type to cancel"; !strings.Contains(transcript, want) {
		t.Fatalf("transcript = %q, want %q", transcript, want)
	}
	if want := "⚠ Usage limit reached · continuing automatically at " + clock + " · esc to cancel"; !strings.Contains(composerText(renderer), want) {
		t.Fatalf("composer = %q, want the notice %q", composerText(renderer), want)
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
	if state.autoContinue != nil {
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
	// The engine's own cancellation event is what clears the notice.
	handleAutoContinueNotification(renderer, state, AutoContinueCancelledMsg{SessionID: "s1", Reason: turn.AutoContinueCancelledByUser})
	if state.autoContinue != nil {
		t.Fatal("still pending after the cancellation event")
	}
	if strings.Contains(composerText(renderer), "continuing automatically") {
		t.Fatal("notice still under the composer")
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
	if state.autoContinue != nil {
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
	if state.autoContinue != nil || strings.Contains(composerText(renderer), "continuing automatically") {
		t.Fatal("notice outlived the continuation")
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
	state.leaveAutoContinue(renderer)
	if len(session.cancelled) != 1 || state.autoContinue != nil || state.autoContinueWake() {
		t.Fatalf("cancels %v, pending %+v", session.cancelled, state.autoContinue)
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
	got := autoContinueFooterText(string(llm.ExplainRateLimitThrottle), now.Add(2*time.Minute), now)
	if got != "Rate limited · continuing automatically at 11:02pm · esc to cancel" {
		t.Fatalf("footer = %q", got)
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
