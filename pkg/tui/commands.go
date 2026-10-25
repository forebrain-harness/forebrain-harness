// Slash command surface: the catalog, matrix, formatting, and overlay.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/migrate"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

type commandController struct {
	session  Session
	renderer *Renderer
	selector Selector
	readLine func(context.Context) (string, error)
	home     string
	// openPanel, when wired by the main loop, opens an interactive slash
	// panel (composer state, §3.1) and reports whether it opened.
	openPanel func(kind string) bool
	// resetScreen, when wired by the main loop, replaces everything the terminal
	// is showing with the chrome a fresh screen opens with. A replay appends
	// otherwise: /resume looked right only because switchStreamSession had just
	// cleared the screen, and /rewind — which has no such caller — pasted the
	// whole transcript on top of the one already there.
	resetScreen func()
	// replayCardsTo, when wired by the main loop, is the reducer the live
	// event loop reduces every notification through. A resume's replay
	// rebuilds the conversation's cards in its own reducer; without this
	// handover, a lifecycle event arriving after the resume — the reaper
	// ending a background subagent the previous process left running —
	// finds no card to settle and opens an orphan while the replayed one
	// ticks forever.
	replayCardsTo *Reducer
}

func newCommandController(session Session, renderer *Renderer, selector Selector, readLine func(context.Context) (string, error), home string) *commandController {
	return &commandController{
		session:  session,
		renderer: renderer,
		selector: selector,
		readLine: readLine,
		home:     home,
	}
}

func (c *commandController) handleResume(ctx context.Context, current string) (string, bool) {
	selected, listed := promptRecentSession(ctx, c.session, c.selector, c.readLine, current)
	if !listed {
		c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "resume", Content: "There is no other conversation to resume.", Final: true})
		return "", false
	}
	if strings.TrimSpace(selected) == "" {
		return "", false
	}
	return strings.TrimSpace(selected), true
}

// printSessionResumeContext replays a resumed conversation and says so under
// it.
func (c *commandController) printSessionResumeContext(sessionID string) {
	c.replaySession(sessionID, func(messages int) *Frame {
		return &Frame{
			Kind:    FrameStatus,
			Title:   "resumed",
			Content: fmt.Sprintf("%s above; scroll up to review them.", plural(messages, "message")),
			Final:   true,
		}
	})
}

// replaySession replays the full session transcript as structured Frames by
// feeding each stored Turn through a fresh Reducer — every stored message
// renders through the same pipeline as a live session, with no cap on the
// number of turns replayed — then appends the frame note builds from how many
// messages there were, if any. A conversation with nothing to replay gets no
// note.
func (c *commandController) replaySession(sessionID string, note func(messages int) *Frame) {
	if c == nil || c.renderer == nil || c.session == nil {
		return
	}
	sid := strings.TrimSpace(sessionID)
	turns, err := c.session.SurfaceTranscriptMessages(context.Background(), sid)
	if err != nil {
		c.renderer.PrintError(fmt.Errorf("failed to load session transcript: %w", err))
		return
	}
	events := []event.RunEvent(nil)
	if source, ok := c.session.(interface {
		SurfaceSessionEvents(context.Context, string) ([]event.RunEvent, error)
	}); ok {
		loaded, eventErr := source.SurfaceSessionEvents(context.Background(), sid)
		if eventErr != nil {
			c.renderer.PrintError(fmt.Errorf("failed to load session events: %w", eventErr))
		} else {
			events = loaded
		}
	}
	// The conversation's own plan cards live in the run-step ledger, not in
	// the session event log, and reach the live conversation through the
	// run-step mirror. Read them through the same engine projection the web
	// history reads; replayTurnWithReducer pairs each one with the
	// session_todo row that emitted it and draws the plan card in its place.
	planUpdates := []event.PlanUpdatedPayload(nil)
	if source, ok := c.session.(interface {
		SurfaceSessionPlanUpdates(context.Context, string) ([]event.PlanUpdatedPayload, error)
	}); ok {
		loaded, planErr := source.SurfaceSessionPlanUpdates(context.Background(), sid)
		if planErr != nil {
			c.renderer.PrintError(fmt.Errorf("failed to load session plan updates: %w", planErr))
		} else {
			planUpdates = loaded
		}
	}
	if len(events) > 0 {
		turns = primaryTranscriptTurns(turns)
	}
	if len(turns) == 0 && len(events) == 0 {
		return
	}
	// A replay replaces the screen rather than adding to it: what is on screen
	// now is either the same conversation drawn from a store that has since
	// changed (/rewind) or another conversation entirely (/resume). Replaying
	// above it doubled the transcript and every card in it.
	if c.resetScreen != nil {
		c.resetScreen()
	}
	// A subagent's words are rebuilt from its own worker session's transcript
	// (plan 017): the rows the runtime wrote there are read the same way this
	// conversation's own are, and the replay feeds them into the subagent's view
	// in place of its delta events. A worker session that is gone falls back to
	// the events.
	workerTranscripts := c.loadSubagentWorkerTranscripts(events)
	c.renderer.BeginBatch()
	reducer := &Reducer{}
	replayTimelineWithWorkers(c.renderer, turns, events, reducer, planUpdates, workerTranscripts)
	flushReplayReducer(c.renderer, reducer)
	// The cards the replay built are the ones on screen now; the live reducer
	// takes them over so the events that still arrive for them settle them in
	// place.
	if c.replayCardsTo != nil {
		c.replayCardsTo.adoptReplayedCards(reducer)
	}
	c.renderer.EndBatch()
	// Refresh the footer budget from the ACTIVE model-context projection, not
	// the full visual replay. After a context clear the full transcript is still
	// replayed above, but those pre-clear turns must not restore stale token
	// usage in the footer's "N%" budget readout.
	activeTurns, activeErr := c.session.SurfaceActiveContextMessages(context.Background(), sid)
	if activeErr != nil {
		c.renderer.PrintError(fmt.Errorf("failed to load active session context: %w", activeErr))
	} else if stats := c.session.SurfaceComposerTokenStats(sid, state.TokenCountFromLastAPIResponse(activeTurns)); stats.Active {
		c.renderer.SetComposerTokenStats("", stats)
	}
	if note != nil {
		if frame := note(len(turns)); frame != nil {
			c.renderer.RenderFrame(*frame)
		}
	}
	if loader, ok := c.session.(interface {
		LoadSurfaceBrowseState(context.Context, string) (RendererBrowseState, bool, error)
	}); ok {
		if browse, found, loadErr := loader.LoadSurfaceBrowseState(context.Background(), sid); loadErr != nil {
			c.renderer.PrintError(fmt.Errorf("failed to restore session browsing state: %w", loadErr))
		} else if found {
			c.renderer.RestoreBrowseState(browse)
		}
	}
}

func primaryTranscriptTurns(turns []state.Message) []state.Message {
	out := make([]state.Message, 0, len(turns))
	for _, turn := range turns {
		if strings.TrimSpace(transcriptToolMeta(turn).AgentID) != "" {
			continue
		}
		out = append(out, turn)
	}
	return out
}

// loadSubagentWorkerTranscripts reads, once, the worker session of every
// subagent this conversation dispatched and still has rows for (plan 017). The
// rows are read the same way this conversation's own transcript is, so a
// subagent's view rebuilds from them exactly as the main conversation does. A
// spawn with no worker session id, and a worker session the reader can no longer
// see, are left out - the execution then replays from its events.
func (c *commandController) loadSubagentWorkerTranscripts(events []event.RunEvent) map[string][]state.Message {
	if c == nil || c.session == nil || len(events) == 0 {
		return nil
	}
	out := make(map[string][]state.Message)
	for i := range events {
		if events[i].Type != event.RunEventSubagentSpawned {
			continue
		}
		var p event.SubagentSpawnedPayload
		if json.Unmarshal(events[i].Payload, &p) != nil {
			continue
		}
		workerSessionID := strings.TrimSpace(p.WorkerSessionID)
		if workerSessionID == "" {
			continue
		}
		if _, done := out[workerSessionID]; done {
			continue
		}
		rows, err := c.session.SurfaceTranscriptMessages(context.Background(), workerSessionID)
		if err != nil || len(rows) == 0 {
			continue
		}
		out[workerSessionID] = rows
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func replayTurnWithReducer(renderer *Renderer, reducer *Reducer, turn state.Message, callIndex map[string]replayToolCall, subagentCalls map[string]event.SubagentCall, planUpdates *[]event.PlanUpdatedPayload) {
	if renderer == nil || reducer == nil || strings.EqualFold(strings.TrimSpace(turn.Role), "system") {
		return
	}
	meta := transcriptToolMeta(turn)
	timing := transcriptTurnTimingFromTurn(turn)
	content := strings.TrimSpace(turn.Content)
	timestamp := time.Time{}
	if turn.CreatedAt > 0 {
		timestamp = time.Unix(turn.CreatedAt, 0).UTC()
	}
	if transcriptLooksLikeUserShellRecord(turn, meta) {
		msg := Message{
			Kind:      MsgKindTool,
			Content:   transcriptUserShellDisplayBody(turn),
			ToolName:  "shell",
			ToolMeta:  meta,
			Summary:   transcriptToolSummaryWithMeta(content, meta),
			AgentID:   strings.TrimSpace(meta.AgentID),
			Duration:  timing.duration,
			Timestamp: timestamp,
		}
		renderReplayFrames(renderer, reducer, msg, "shell", meta, timing)
		return
	}
	kind := msgKindFromRole(turn.Role)
	if kind == MsgKindTool {
		msg, toolName, ok := replayToolMessage(turn, meta, callIndex, subagentCalls)
		if !ok {
			return
		}
		// A completed plan-emitting call is represented by the plan card it
		// emitted, never by a tool card: that is the one answer
		// event.ToolStepRendersAsPlan gives the live terminal hook, the live
		// gateway hook, and the event projection (which hides the step), so
		// the replayed transcript row yields to the same card. The payload
		// comes from the projected plan event the run recorded; a session with
		// no such event (recorded before its event log, or a migrated one)
		// keeps this row as its only record of the call.
		if plan, took := takeReplayPlanUpdate(toolName, meta, planUpdates); took {
			result := reducer.Reduce(PlanUpdatedMsg{Payload: plan})
			for _, frame := range result.Frames {
				renderer.RenderFrame(frame)
			}
			return
		}
		msg.Timestamp = timestamp
		renderReplayFrames(renderer, reducer, msg, toolName, meta, timing)
		return
	}
	// Non-tool rows: assistant/user/reasoning/plan/error/system. Pure
	// tool-call assistant rows carry no text and produce no visible frame
	// (their tool call renders from the correlated tool row), so skip them.
	if content == "" {
		return
	}
	// A message the person did not type — a heartbeat's prompt — replays as
	// what it is rather than as a card that says they sent it.
	if kind == MsgKindUser {
		if origin := state.MessageOrigin(turn.PartsJSON); origin != "" {
			renderReplayFrames(renderer, reducer, Message{Kind: MsgKindSystem, Title: origin, Content: content, Timestamp: timestamp}, "", meta, timing)
			return
		}
	}
	msg := Message{
		Kind: kind, Content: content, ToolMeta: meta,
		AgentID: strings.TrimSpace(meta.AgentID), Timestamp: timestamp,
	}
	// A reasoning row is one whole block, not a delta, and the row carries how
	// long the model spent on it. A duration therefore marks this as the
	// replayed shape, and the reducer seals it with that duration instead of
	// deriving one from the row's second-precision created_at (which is the
	// same second for every row of a turn, and so reads as zero).
	if kind == MsgKindReasoning {
		if spent, ok := state.ParseReasoningTiming(turn.PartsJSON); ok {
			msg.Duration = spent
		}
	}
	renderReplayFrames(renderer, reducer, msg, "", meta, timing)
}

// replayTimelineWithReducer merges the compact-independent primary transcript
// and the canonical subagent event log before reducing them. Live delivery is
// interleaved; replaying the two sources in separate batches moved every child
// card after the final parent answer and could not reproduce the live VM.
//
// Both sources are ordered structurally, never by wall clock. A run writes all
// of its transcript rows when it ends, so an entire turn shares one
// second-precision created_at while the subagent events that happened during
// that turn carry earlier millisecond stamps. Ordering the merged stream by
// time therefore shuffled whole turns - every user row of a turn was hoisted
// above the answers it followed - and pushed each subagent card ahead of the
// message that spawned it. RowID is the transcript's own insertion order and is
// authoritative, so the stored order is replayed as-is; a subagent's events are
// anchored to the parent tool call that dispatched it, and a nested subagent
// inherits its parent's anchor through its parent run.
//
// Approval records anchor by the tool call they name, and the canceled card for
// a call that never ran is drawn at the same position and first, so the
// confirmation line the user read lands above the card it answers - exactly
// where it was live.
//
// A gate whose wait holds no card is the exception on both counts: its records
// anchor right after the row that issued the call, and that row's response is
// sealed before them by the same running step live fed the reducer
// (heldGateCallsByRow), so the response that follows the gate is a block of
// its own rather than an extension of the one before it.
func replayTimelineWithReducer(renderer *Renderer, turns []state.Message, events []event.RunEvent, reducer *Reducer, planUpdates []event.PlanUpdatedPayload) int {
	return replayTimelineWithWorkers(renderer, turns, events, reducer, planUpdates, nil)
}

// replayTimelineWithWorkers merges the compact-independent primary transcript
// and the canonical subagent event log before reducing them. Live delivery is
// interleaved; replaying the two sources in separate batches moved every child
// card after the final parent answer and could not reproduce the live VM.
//
// workerTranscripts carries each subagent worker session's rows, keyed by its
// session id (plan 017): a child execution whose rows are here has its words
// rebuilt from them instead of from its per-token deltas, and the events still
// supply everything else about it.
//
// Both sources are ordered structurally, never by wall clock. A run writes all
// of its transcript rows when it ends, so an entire turn shares one
// second-precision created_at while the subagent events that happened during
// that turn carry earlier millisecond stamps. Ordering the merged stream by
// time therefore shuffled whole turns - every user row of a turn was hoisted
// above the answers it followed - and pushed each subagent card ahead of the
// message that spawned it. RowID is the transcript's own insertion order and is
// authoritative, so the stored order is replayed as-is; a subagent's events are
// anchored to the parent tool call that dispatched it, and a nested subagent
// inherits its parent's anchor through its parent run.
//
// Approval records anchor by the tool call they name, and the canceled card for
// a call that never ran is drawn at the same position and first, so the
// confirmation line the user read lands above the card it answers - exactly
// where it was live.
//
// A gate whose wait holds no card is the exception on both counts: its records
// anchor right after the row that issued the call, and that row's response is
// sealed before them by the same running step live fed the reducer
// (heldGateCallsByRow), so the response that follows the gate is a block of
// its own rather than an extension of the one before it.
func replayTimelineWithWorkers(renderer *Renderer, turns []state.Message, events []event.RunEvent, reducer *Reducer, planUpdates []event.PlanUpdatedPayload, workerTranscripts map[string][]state.Message) int {
	if renderer == nil || reducer == nil {
		return 0
	}
	workerBodies := buildReplayWorkerBodies(events, workerTranscripts)
	anchors := subagentEventAnchors(turns, events)
	// The card facts of every subagent_* call the transcript answered, derived
	// the way the live path derived them, so a row recorded before the card
	// existed replays as the card and not as the JSON it stored.
	subagentCalls := turn.SubagentCallsInTranscript(turns)
	callIndex, callRow := buildToolCallIndex(turns)
	resultRow := toolResultRows(turns)
	pending := make(map[int][]replayEventItem)
	// A plan review's events carry the approval action they answer but no
	// tool step of their own, so they are anchored to the call that action
	// names — the same position its confirmation lines replay at.
	reviewAnchors := planReviewAnchorByAction(events, callRow, resultRow)
	// Every run that ended closes with its "Worked for" line after the last
	// row it wrote. A failed run's error was the last thing it said before
	// that line live, so the error is drawn there too rather than wherever its
	// clock happens to fall among the rows.
	workedLines := turn.RunWorkedLines(turns)
	closingRuns := make(map[string]struct{}, len(workedLines))
	for _, line := range workedLines {
		closingRuns[line.RunID] = struct{}{}
	}
	runEndFrames := make(map[string][]Frame)
	supersededAbandons := supersededAbandonedErrors(events)
	eventCount := 0
	for i := range events {
		evt := events[i]
		if _, superseded := supersededAbandons[i]; superseded {
			continue
		}
		if turnErrorFrames := replayTurnErrorEventFrames(evt); len(turnErrorFrames) > 0 {
			if _, closes := closingRuns[strings.TrimSpace(evt.RunID)]; closes {
				runEndFrames[strings.TrimSpace(evt.RunID)] = append(runEndFrames[strings.TrimSpace(evt.RunID)], turnErrorFrames...)
				eventCount++
				continue
			}
		}
		msg, ok := replaySubagentRunEventMessage(evt)
		if compacted, isCompaction := replayCompactionMessage(evt); isCompaction {
			msg, ok = compacted, true
		}
		if goal, isGoal := goalEventMessage(evt); isGoal {
			msg, ok = goal, true
		}
		if rejected, isRejected := imageRejectedMessage(evt); isRejected {
			msg, ok = rejected, true
		}
		if notice, isNotice := replayResponseNoticeMessage(evt); isNotice {
			msg, ok = notice, true
		}
		frames := replayApprovalEventFrames(evt)
		if migrationFrames := replayMigrationEventFrames(evt); len(migrationFrames) > 0 {
			frames = append(frames, migrationFrames...)
			ok = true
		}
		if turnErrorFrames := replayTurnErrorEventFrames(evt); len(turnErrorFrames) > 0 {
			frames = append(frames, turnErrorFrames...)
			ok = true
		}
		if len(frames) > 0 {
			ok = true
		}
		if !ok {
			continue
		}
		eventCount++
		pos := approvalEventAnchorPosition(evt, callRow, resultRow)
		if pos < 0 {
			if id, ok := planReviewEventActionID(evt); ok {
				if reviewPos, found := reviewAnchors[id]; found {
					pos = reviewPos
				}
			}
		}
		if placed, ok := turn.CompactionPosition(turns, evt); ok {
			pos = placed
		}
		if placed, ok := turn.GoalPosition(turns, evt); ok {
			pos = placed
		}
		if pos < 0 {
			if anchored, ok := anchors[strings.TrimSpace(evt.RunID)]; ok {
				pos = anchored
			}
		}
		if pos < 0 {
			if skillPos, ok := skillEventAnchorPosition(evt, turns); ok {
				pos = skillPos
			}
		}
		if pos < 0 {
			pos = transcriptRowsBefore(turns, evt.CreatedAt.UnixMilli())
		}
		sequence := evt.Sequence
		if sequence == 0 {
			sequence = int64(i + 1)
		}
		// An execution whose words are in its worker transcript is rebuilt from
		// them: its block deltas spend the rebuilt messages one group at a time
		// and are otherwise dropped, while everything that is not a block delta -
		// an approval, a plan card, the lifecycle - keeps the place the events
		// drew it at. One worker session's executions share its run id, so the
		// bodies are left in place across a `subagent_ended`; a later execution
		// of the same subagent keeps consuming the same body.
		if body := workerBodies[strings.TrimSpace(evt.RunID)]; body != nil {
			if injected, consumed := body.consume(evt); consumed {
				if injected != nil {
					pending[pos] = append(pending[pos], replayEventItem{sequence: sequence, message: injected})
				}
				continue
			}
		}
		// One event, one identity: the message and every frame rebuilt from it
		// carry the event's own id, so the card this replay draws is recognizable
		// as the same card the live path already painted (and vice versa) instead
		// of a second copy of it.
		eventID := strings.TrimSpace(evt.ID)
		if m, isMessage := msg.(Message); isMessage {
			m.EventID = eventID
			msg = m
		}
		for i := range frames {
			if strings.TrimSpace(frames[i].EventID) == "" {
				frames[i].EventID = eventID
			}
		}
		pending[pos] = append(pending[pos], replayEventItem{sequence: sequence, message: msg, frames: frames})
	}
	// A tool call the transcript shows as issued but never as run is a call the
	// run was stopped on: the approval the user canceled, an interruption, a
	// prompt that could never be shown. Live marked exactly these cards canceled
	// when the run aborted (Renderer.FinalizePendingTools), so replay rebuilds
	// them the same way - one card per call id, placed after the assistant row
	// that issued it and before the display records about that call, because a
	// confirmation is inserted above the last tool block in the view and that
	// block has to be this card.
	orphans := orphanToolCalls(callIndex, callRow, resultRow)
	sort.SliceStable(orphans, func(i, j int) bool { return orphans[i].call.order < orphans[j].call.order })
	for i := range orphans {
		orphan := orphans[i]
		pending[orphan.row+1] = append(pending[orphan.row+1], replayEventItem{
			// Ahead of every stored record at this position, and in the order the
			// calls were issued: a batch the run never reached replays in the
			// order the model asked for it, not in map order.
			sequence:   int64(i) - int64(len(orphans)),
			tool:       &orphan.call,
			toolStepID: orphan.callID,
		})
	}
	for _, group := range pending {
		sort.SliceStable(group, func(i, j int) bool { return group[i].sequence < group[j].sequence })
	}
	flush := func(pos int) {
		for _, item := range pending[pos] {
			if item.message != nil {
				result := reducer.Reduce(item.message)
				for _, frame := range result.Frames {
					if frame.Kind != FrameThinking || frame.Final {
						renderer.RenderFrame(frame)
					}
				}
			}
			if item.tool != nil {
				msg, toolName, ok := replayOrphanToolCallMessage(item.toolStepID, *item.tool)
				if ok {
					renderReplayFrames(renderer, reducer, msg, toolName, msg.ToolMeta, transcriptTurnTiming{})
				}
			}
			for _, frame := range item.frames {
				renderer.RenderFrame(frame)
			}
		}
	}
	heldCalls := heldGateCallsByRow(callIndex, callRow)
	for i := range turns {
		flush(i)
		replayTurnWithReducer(renderer, reducer, turns[i], callIndex, subagentCalls, &planUpdates)
		// A held gate's records are parked right after this row; the response
		// they follow has to be sealed first, as live sealed it.
		if calls := heldCalls[i]; len(calls) > 0 {
			at := time.Time{}
			if turns[i].CreatedAt > 0 {
				at = time.Unix(turns[i].CreatedAt, 0).UTC()
			}
			for _, call := range calls {
				result := reducer.Reduce(NewMessageMsg{Msg: replayHeldGateStepMessage(call, at)})
				for _, frame := range result.Frames {
					if frame.Kind != FrameThinking || frame.Final {
						renderer.RenderFrame(frame)
					}
				}
			}
		}
		if line, ok := workedLines[i]; ok {
			// The run's own text is out first, then its error if it failed;
			// its line closes it, as live.
			flushReplayReducer(renderer, reducer)
			for _, frame := range runEndFrames[line.RunID] {
				renderer.RenderFrame(frame)
			}
			renderer.RenderFrame(replayWorkedFrame(line))
		}
	}
	flush(len(turns))
	return eventCount
}

// supersededAbandonedErrors finds the "process stopped" errors a run also has a
// real ending for. A run reaped as abandoned while its process was in fact
// still alive (a machine that slept past the lease) goes on to report how it
// really ended; the reap's sentence is then the guess and the real error is the
// fact, so only the fact is drawn. A run whose only error is the reap's keeps it.
func supersededAbandonedErrors(events []event.RunEvent) map[int]struct{} {
	real := make(map[string]struct{})
	for i := range events {
		evt := events[i]
		if evt.Type != event.RunEventTurnError || strings.TrimSpace(evt.RunID) == "" {
			continue
		}
		var p event.TurnErrorPayload
		if json.Unmarshal(evt.Payload, &p) != nil || isAbandonedRunError(p) {
			continue
		}
		real[strings.TrimSpace(evt.RunID)] = struct{}{}
	}
	if len(real) == 0 {
		return nil
	}
	out := make(map[int]struct{})
	for i := range events {
		evt := events[i]
		if evt.Type != event.RunEventTurnError {
			continue
		}
		if _, has := real[strings.TrimSpace(evt.RunID)]; !has {
			continue
		}
		var p event.TurnErrorPayload
		if json.Unmarshal(evt.Payload, &p) == nil && isAbandonedRunError(p) {
			out[i] = struct{}{}
		}
	}
	return out
}

func isAbandonedRunError(p event.TurnErrorPayload) bool {
	return p.Detail != nil && p.Detail.Code == "run_abandoned"
}

// replayWorkedFrame is the "Worked for" line that closed a run: how long it
// took, how long it waited for the model's first output when the run measured
// that, and the minute it finished, read from the run's own clock.
func replayWorkedFrame(line turn.RunWorkedLine) Frame {
	parts := []string{workedForLabel(line.Worked)}
	// A run that measured no first token (it produced no output, or predates
	// the measurement) drops the segment instead of showing a fake "0s".
	if wait := firstTokenLabel(line.FirstToken); wait != "" {
		parts = append(parts, wait)
	}
	parts = append(parts, workedCompletionTime(line.FinishedAt.Local()))
	return Frame{Kind: FrameStatus, Title: strings.Join(parts, " · "), RunID: line.RunID, Final: true}
}

// replayCompactionMessage rebuilds a compaction's final card from the event
// that ended it — the same message the live card was finished by, so a
// resumed session shows the card the user watched finish. The events that
// only drove the running card are superseded by that one and replay nothing.
func replayCompactionMessage(evt event.RunEvent) (any, bool) {
	switch evt.Type {
	case event.RunEventContextCompacted:
		var p event.ContextCompactedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return nil, false
		}
		return ContextCompactedMsg{Payload: p}, true
	case event.RunEventContextCompactError:
		var p event.ContextCompactFailedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return nil, false
		}
		return ContextCompactFailedMsg{CompactionID: p.CompactionID, AgentID: p.AgentID, Error: p.Error, Cancelled: p.Cancelled}, true
	default:
		return nil, false
	}
}

// replayTurnErrorEventFrames rebuilds a failure block from its durable event, so
// a resumed session shows the same explanation the user would have read live.
//
// The anchor is deliberately the timestamp fallback the caller applies: a
// startup failure has no run and no tool call to attach to, so it lands at the
// first transcript row written after it — which is exactly where the reader saw
// it — and at the very top when it happened before the session had any.
func replayTurnErrorEventFrames(evt event.RunEvent) []Frame {
	if evt.Type != event.RunEventTurnError {
		return nil
	}
	var payload event.TurnErrorPayload
	if json.Unmarshal(evt.Payload, &payload) != nil {
		return nil
	}
	text := turnErrorDisplayText(payload)
	if text == "" {
		return nil
	}
	title := strings.TrimSpace(payload.Title)
	if title == "" {
		title = "error"
	}
	// The event's identity travels with the frame: this card is the same card
	// the live path painted for the same event, and the renderer keeps one of
	// them on one screen. It is stamped here rather than at the replay seam
	// because a run that ended cleanly diverts its last error into runEndFrames
	// before that seam is reached.
	return []Frame{{Kind: FrameError, Title: title, Content: text, EventID: strings.TrimSpace(evt.ID), Final: true}}
}

// replayMigrationEventFrames rebuilds the migration report frame from its
// durable event, so a resumed session replays exactly the report the user
// read. The event carries the rendered text because the report's counters
// move on after the import; replay must not re-derive them.
func replayMigrationEventFrames(evt event.RunEvent) []Frame {
	if evt.Type != event.RunEventMigrationCompleted {
		return nil
	}
	var payload event.MigrationCompletedPayload
	if json.Unmarshal(evt.Payload, &payload) != nil {
		return nil
	}
	if strings.TrimSpace(payload.Report) == "" {
		return nil
	}
	return []Frame{migrationReportFrame(payload.Title, payload.Report)}
}

// migrationReportFrame is a finished import's report as the conversation
// shows it on replay: a plain system card, no StepID, so two migrations in
// one session each keep their own block.
func migrationReportFrame(title, report string) Frame {
	if title = strings.TrimSpace(title); title == "" {
		title = "Migration complete"
	}
	return Frame{Kind: FrameSystem, Title: title, Content: strings.TrimSpace(report), Final: true}
}

// migrationStepID is the one block a live migration owns: the preview dry
// run opens it, the run's first checkpoint retitles it, and the finished
// report replaces it in place — one card for the whole operation.
const migrationStepID = "migrate-progress"

// migrationLiveReportFrame is the live side's finished report: the same body
// the replay frame carries, delivered onto the progress card's own block so
// the bar the user watched becomes the report it was measuring.
func migrationLiveReportFrame(title, report string) Frame {
	if title = strings.TrimSpace(title); title == "" {
		title = migrationTitleDone
	}
	return Frame{
		Kind: FrameMigration, StepID: migrationStepID, Final: true,
		Title: title, Content: strings.TrimSpace(report),
	}
}

// replayEventItem is one replayed item parked at a transcript position. A
// subagent event arrives as a message the reducer turns into frames; an
// approval confirmation is a status the live surface painted straight into the
// viewport, so it is carried as the frame itself; a canceled card for a call
// that never ran is rebuilt through the same tool-card path a stored tool row
// uses.
type replayEventItem struct {
	sequence int64
	message  any
	tool     *replayToolCall
	// toolStepID is the call id the tool card is rendered under.
	toolStepID string
	frames     []Frame
}

// replayResponseNoticeMessage is a stored response notice as the message the
// live surface drew it from, so a reload produces the same frame the live
// conversation saw — the same reduction, not a parallel rendering.
func replayResponseNoticeMessage(evt event.RunEvent) (any, bool) {
	if evt.Type != event.RunEventResponseNotice {
		return nil, false
	}
	var p event.ResponseNoticePayload
	if json.Unmarshal(evt.Payload, &p) != nil {
		return nil, false
	}
	return ResponseNoticeMsg{AgentID: p.AgentID, Kind: p.Kind, Text: p.Text}, true
}

// approvalEventAnchorPosition resolves the transcript position an approval
// record belongs at from the tool call it names, as the index that must follow
// it.
//
// The position is the row that draws the card, not the row that issued the
// call: a confirmation is inserted above the last tool block already in the
// view (Renderer.PrintApprovalConfirmation), so it has to be flushed after its
// own card exists or it lands above some earlier, unrelated one. A call that
// ran draws its card from the tool row that answered it; a call that never ran
// draws a rebuilt canceled card just after the assistant row that issued it,
// and sorts ahead of the records parked there.
//
// A gate that holds no card (approvalGateHoldsNoCard) is the exception: its
// records have no card of their own to sit above, so they anchor right after
// the row that issued the call and before whatever answered it, the position
// live printed them at.
//
// It reports -1 for any other event, and for a record naming no call this
// transcript can place, which leaves the caller to fall back to the run's
// anchor and then to the clock.
func approvalEventAnchorPosition(evt event.RunEvent, callRow, resultRow map[string]int) int {
	stepID := approvalEventToolStepID(evt)
	if stepID == "" {
		return -1
	}
	if approvalGateHoldsNoCard(approvalEventActionKind(evt)) {
		if row, ok := callRow[stepID]; ok {
			return row + 1
		}
	}
	if row, ok := resultRow[stepID]; ok {
		return row + 1
	}
	row, ok := callRow[stepID]
	if !ok {
		return -1
	}
	return row + 1
}

func approvalEventToolStepID(evt event.RunEvent) string {
	switch evt.Type {
	case event.RunEventApprovalReq:
		var p event.ApprovalRequestedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return ""
		}
		return strings.TrimSpace(p.ToolStepID)
	case event.RunEventApprovalResolved:
		var p event.ApprovalResolvedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return ""
		}
		return strings.TrimSpace(p.ToolStepID)
	default:
		return ""
	}
}

// approvalEventActionKind is the gated action an approval record names, which
// is how replay recognises a gate whose call holds no card: the record's own
// action kind, with no tool name or live request to consult.
func approvalEventActionKind(evt event.RunEvent) string {
	switch evt.Type {
	case event.RunEventApprovalReq:
		var p event.ApprovalRequestedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return ""
		}
		return strings.TrimSpace(p.ActionKind)
	case event.RunEventApprovalResolved:
		var p event.ApprovalResolvedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return ""
		}
		return strings.TrimSpace(p.ActionKind)
	default:
		return ""
	}
}

// planReviewAnchorByAction maps each approval action to the transcript position
// its records replay at: the tool call the action names. A plan review's card
// then shares one position with the confirmation lines of the approval it
// answers, and the group's own sequence order puts the card between them.
func planReviewAnchorByAction(events []event.RunEvent, callRow, resultRow map[string]int) map[string]int {
	out := make(map[string]int)
	for i := range events {
		switch events[i].Type {
		case event.RunEventApprovalReq, event.RunEventApprovalResolved:
		default:
			continue
		}
		var p struct {
			ActionID string `json:"action_id"`
		}
		if json.Unmarshal(events[i].Payload, &p) != nil || strings.TrimSpace(p.ActionID) == "" {
			continue
		}
		id := strings.TrimSpace(p.ActionID)
		if _, seen := out[id]; seen {
			continue
		}
		if pos := approvalEventAnchorPosition(events[i], callRow, resultRow); pos >= 0 {
			out[id] = pos
		}
	}
	return out
}

// planReviewEventActionID is the approval action a plan-review event answers.
func planReviewEventActionID(evt event.RunEvent) (string, bool) {
	switch evt.Type {
	case event.RunEventPlanReviewStarted:
		var p event.PlanReviewStartedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return "", false
		}
		return strings.TrimSpace(p.ActionID), true
	case event.RunEventPlanReviewed:
		var p event.PlanReviewedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return "", false
		}
		return strings.TrimSpace(p.ActionID), true
	default:
		return "", false
	}
}

// isPlanReviewDeliveredDisplay reports whether a stored denial display body is
// the review-delivery drop key: a gate a delivered review closed — never the
// user — carries exactly that sentence, which is internal handoff plumbing,
// so every replay projection drops the row whole.
func isPlanReviewDeliveredDisplay(body string) bool {
	return strings.TrimSpace(body) == tool.PlanReviewDeliveredDisplayKey
}

// isHiddenToolDisplay reports whether a stored tool display is one no surface
// draws: a delivered review's handoff, or a call a queued message took the
// turn from before it started.
func isHiddenToolDisplay(body string) bool {
	body = strings.TrimSpace(body)
	return body == tool.PlanReviewDeliveredDisplayKey || body == tool.SteerPreemptedDisplayKey || body == tool.RejectionStoppedDisplayKey
}

// replayApprovalEventFrames rebuilds the frames an approval record carries that
// have no message form. Only the confirmation line does: the gate's own card is
// the tool call the record names, and replay rebuilds it as a canceled card
// rather than trusting the record - which is also why a record without a
// resolvable tool step produces a line and never a card.
func replayApprovalEventFrames(evt event.RunEvent) []Frame {
	if evt.Type != event.RunEventApprovalResolved {
		return nil
	}
	var p event.ApprovalResolvedPayload
	if json.Unmarshal(evt.Payload, &p) != nil {
		return nil
	}
	// The line was sanitized once, where it was built, and stored that way;
	// replay only strips the colour the live path adds around the glyph, so the
	// replayed frame carries the same fields PrintApprovalConfirmation set.
	line := strings.TrimSpace(sgrPattern.ReplaceAllString(p.Confirmation, ""))
	if line == "" {
		return nil
	}
	// A delivered review's record carries the drop key as its line: the
	// handoff was never user-facing, so the replayed frame is dropped whole.
	if isPlanReviewDeliveredDisplay(line) {
		return nil
	}
	return []Frame{{
		Kind:            FrameStatus,
		Content:         line,
		MaxDisplayLines: approvalConfirmationMaxLines,
		// The line sits above the gated call's parked card, unless the gate
		// holds no card at all: then it keeps the anchor position it was
		// sorted at, right after the row that issued the call.
		InsertBeforeLastTool: !approvalGateHoldsNoCard(p.ActionKind),
		AgentID:              strings.TrimSpace(p.AgentID),
		RunID:                evt.RunID,
		Final:                true,
	}}
}

// toolResultRows maps each answered tool call to the transcript row that
// answered it - the row whose card the call is drawn as. The first answer wins:
// a call id can be persisted twice, and the card a second row produces replaces
// the first in place, so the first is where the block actually sits.
func toolResultRows(turns []state.Message) map[string]int {
	rows := make(map[string]int, len(turns))
	for i := range turns {
		if !strings.EqualFold(strings.TrimSpace(turns[i].Role), "tool") {
			continue
		}
		_, _, toolCallID, _ := state.ParseMessageParts(turns[i].PartsJSON, "")
		id := strings.TrimSpace(toolCallID)
		if id == "" {
			continue
		}
		if _, seen := rows[id]; !seen {
			rows[id] = i
		}
	}
	return rows
}

// orphanToolCall is a call the transcript shows as issued but never as run,
// with the assistant row that issued it.
type orphanToolCall struct {
	callID string
	row    int
	call   replayToolCall
}

// orphanToolCalls reports those calls.
//
// It is a set subtraction over the whole transcript, not a per-row left join:
// the same call id can be persisted more than once (a duplicated or replayed
// turn), and subtracting per row would turn each duplicate into another card.
// Each call id therefore appears at most once, on its last issuing row.
//
// A dispatch call is excluded. Its card is a subagent card the event log
// builds, not a tool block, and live never stamps one canceled
// (Renderer.FinalizePendingTools only touches tool blocks) - rebuilding it here
// would invent per-task outcomes for work the run never reached. A query call
// (status/wait/close/list) is rebuilt as the canceled card it became live. An
// exit-plan gate call is excluded too: its wait holds no card on any surface,
// so a run that died on the gate replays with the pending approval overlay
// re-asking it, not with a canceled card it never drew.
func orphanToolCalls(callIndex map[string]replayToolCall, callRow, resultRow map[string]int) []orphanToolCall {
	orphans := make([]orphanToolCall, 0, len(callRow))
	for callID, row := range callRow {
		if _, ran := resultRow[callID]; ran {
			continue
		}
		call, ok := callIndex[callID]
		if !ok || isSubagentDispatchTool(call.name) || tool.ToolStepHoldsNoCard(call.name, "canceled") {
			continue
		}
		orphans = append(orphans, orphanToolCall{callID: callID, row: row, call: call})
	}
	return orphans
}

// isSubagentDispatchTool names the verbs whose card is owned by the
// subagent event log rather than by the call's own row: their agents outlive
// the call, so a call the run never reached has no outcomes to invent.
func isSubagentDispatchTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "subagent_fanout", "subagent_run", "subagent_send", "subagent_continue":
		return true
	default:
		return false
	}
}

// replayOrphanToolCallMessage rebuilds the tool card for a call whose only
// record is the assistant row that issued it. It is the same MsgKindTool
// message a stored tool row produces, with the one difference the missing row
// implies: the call was stopped before it ran, so its status is the same
// "canceled" Renderer.FinalizePendingTools stamps on a live pending card.
//
// Its wording comes from the started phase for the same reason: live built this
// card when the call was dispatched and only overwrote its status, so the card
// says "running ls" under a canceled badge. Describing it as completed would
// have it claim it ran.
func replayOrphanToolCallMessage(callID string, call replayToolCall) (Message, string, bool) {
	toolName := strings.TrimSpace(call.name)
	if strings.TrimSpace(callID) == "" || toolName == "" {
		return Message{}, "", false
	}
	toolName = displayToolName(toolName)
	meta := replayToolStepMeta(toolName, call.args, tool.ToolMeta{Status: "canceled"}, tool.StepKindToolStarted)
	meta.Status = "canceled"
	msg := Message{
		Kind:      MsgKindTool,
		StepID:    strings.TrimSpace(callID),
		ToolName:  toolName,
		ToolMeta:  meta,
		Summary:   replayToolStepSummary(toolName, call.args, tool.StepKindToolStarted),
		ToolPhase: tool.StepKindToolCompleted,
	}
	return msg, toolName, true
}

// heldGateCall is a call whose wait holds no card (tool.ToolStepHoldsNoCard):
// the exit-plan gate. Live, its running step still reached the reducer, which
// drew nothing for it but sealed the response that issued it.
type heldGateCall struct {
	callID string
	name   string
	order  int
}

// heldGateCallsByRow groups those calls by the assistant row that issued them,
// in the order the model asked for them. Replay has no row of its own for the
// running step — the wait draws nothing, and a canceled or review-delivered
// answer row is dropped — so this is where it learns the step happened.
func heldGateCallsByRow(callIndex map[string]replayToolCall, callRow map[string]int) map[int][]heldGateCall {
	out := make(map[int][]heldGateCall)
	for callID, row := range callRow {
		call, ok := callIndex[callID]
		if !ok || !tool.ToolStepHoldsNoCard(call.name, "running") {
			continue
		}
		out[row] = append(out[row], heldGateCall{callID: callID, name: call.name, order: call.order})
	}
	for row := range out {
		calls := out[row]
		sort.SliceStable(calls, func(i, j int) bool { return calls[i].order < calls[j].order })
	}
	return out
}

// replayHeldGateStepMessage is the running step live fed the reducer when the
// gate's call started. The reducer draws nothing for it and seals the response
// that issued the call (Reducer.reduceMessage), at the issuing row's time.
func replayHeldGateStepMessage(call heldGateCall, at time.Time) Message {
	return Message{
		Kind:      MsgKindTool,
		StepID:    call.callID,
		ToolName:  call.name,
		ToolMeta:  tool.ToolMeta{ToolName: call.name, Status: "running"},
		ToolPhase: event.RunEventToolStarted,
		Timestamp: at,
	}
}

// subagentEventAnchors resolves, per subagent run, the transcript position its
// events replay at: the index of the first row that must follow them. A spawn
// records the parent tool call it was dispatched from, and that call is a tool
// row of this transcript, so the child's whole stream replays immediately after
// the row for the call that created it - which is where the live surface showed
// it. A nested subagent's dispatching call belongs to its parent's transcript
// rather than this one, so it inherits its parent run's anchor.
func subagentEventAnchors(turns []state.Message, events []event.RunEvent) map[string]int {
	rowByToolCallID := make(map[string]int, len(turns))
	for i := range turns {
		if id := strings.TrimSpace(turns[i].ToolStepID); id != "" {
			rowByToolCallID[id] = i
		}
	}
	anchors := make(map[string]int)
	for i := range events {
		if events[i].Type != event.RunEventSubagentSpawned {
			continue
		}
		var p event.SubagentSpawnedPayload
		if json.Unmarshal(events[i].Payload, &p) != nil {
			continue
		}
		runID := strings.TrimSpace(p.ExecutionID)
		if runID == "" {
			runID = strings.TrimSpace(events[i].RunID)
		}
		if runID == "" {
			continue
		}
		if row, ok := rowByToolCallID[strings.TrimSpace(p.ParentToolCallID)]; ok {
			anchors[runID] = row + 1
			continue
		}
		if parent, ok := anchors[strings.TrimSpace(p.ParentRunID)]; ok {
			anchors[runID] = parent
		}
	}
	return anchors
}

// transcriptRowsBefore reports how many transcript rows were already on disk
// when an event without a dispatching call in this transcript happened. Rows are
// stamped when their run flushes, so the first row stamped after the event opens
// the block the event belongs inside.
func transcriptRowsBefore(turns []state.Message, atMillis int64) int {
	for i := range turns {
		if turns[i].CreatedAt*1000 > atMillis {
			return i
		}
	}
	return len(turns)
}

// skillEventAnchorPosition anchors a main-agent skill event inside its turn:
// after the last user row submitted no later than the event, and before that
// turn's answer. A skill step has no transcript tool row to anchor through,
// and the clocks cannot be compared directly — the event carries a
// millisecond stamp from mid-run while a turn's rows share one
// second-granularity flush stamp — so the anchor walks by role: the owning
// turn is the last user row at or before the event, and any user rows it
// precedes in the same second are that turn's context rows, not the next
// turn.
func skillEventAnchorPosition(evt event.RunEvent, turns []state.Message) (int, bool) {
	if evt.Type != event.RunEventToolStarted && evt.Type != event.RunEventToolCompleted {
		return 0, false
	}
	var p event.ToolCallCompletedPayload
	if json.Unmarshal(evt.Payload, &p) != nil {
		return 0, false
	}
	if strings.TrimSpace(p.ToolMeta.AgentID) != "" ||
		!strings.EqualFold(strings.TrimSpace(p.ToolMeta.Category), "skill") {
		return 0, false
	}
	atMillis := evt.CreatedAt.UnixMilli()
	lastUser := -1
	for i := range turns {
		if turns[i].CreatedAt*1000 > atMillis {
			break
		}
		if strings.EqualFold(strings.TrimSpace(turns[i].Role), "user") {
			lastUser = i
		}
	}
	if lastUser < 0 {
		return 0, false
	}
	pos := lastUser + 1
	for pos < len(turns) &&
		strings.EqualFold(strings.TrimSpace(turns[pos].Role), "user") &&
		turns[pos].CreatedAt*1000 <= atMillis {
		pos++
	}
	return pos, true
}

func flushReplayReducer(renderer *Renderer, reducer *Reducer) {
	if renderer == nil || reducer == nil {
		return
	}
	// Flush any buffered assistant content from the final turn/event. The
	// synthetic run end carries a "Worked for" line for a run that is over and
	// was already replayed; it is left in EventResult.WorkedStatus, which only
	// a live turn renders.
	flush := reducer.Reduce(RunEndedMsg{})
	for _, frame := range flush.Frames {
		renderer.RenderFrame(frame)
	}
}

// renderReplayFrames reduces one reconstructed message and renders its frames on
// the primary view, applying the tool title/summary fallbacks replay needs.
func renderReplayFrames(renderer *Renderer, reducer *Reducer, msg Message, toolName string, meta tool.ToolMeta, timing transcriptTurnTiming) {
	result := reducer.Reduce(NewMessageMsg{Msg: msg})
	for _, frame := range result.Frames {
		if frame.Kind == FrameThinking && !frame.Final {
			continue
		}
		if frame.Kind == FrameTool {
			frame.Title = transcriptToolDisplayTitle(toolName, meta)
			if strings.TrimSpace(frame.Summary) == "" {
				frame.Summary = strings.TrimSpace(msg.Summary)
			}
			applyReplayToolTiming(&frame, msg, timing)
		}
		renderer.RenderFrame(frame)
	}
}

func replaySubagentRunEventMessage(evt event.RunEvent) (any, bool) {
	switch evt.Type {
	case event.RunEventSubagentSpawned:
		var p event.SubagentSpawnedPayload
		if json.Unmarshal(evt.Payload, &p) != nil || strings.TrimSpace(p.AgentID) == "" {
			return nil, false
		}
		return SubagentSpawnedMsg{AgentID: p.AgentID, AgentType: p.AgentType, TaskID: p.TaskID, Title: p.Title, Task: p.Task, ParentToolCallID: p.ParentToolCallID, TaskIndex: p.TaskIndex, ExecutionID: p.ExecutionID, ModelProvider: p.ModelProvider, Model: p.Model, ReasoningEffort: p.ReasoningEffort, Timestamp: evt.CreatedAt}, true
	case event.RunEventSubagentEnded:
		var p event.SubagentEndedPayload
		if json.Unmarshal(evt.Payload, &p) != nil || strings.TrimSpace(p.AgentID) == "" {
			return nil, false
		}
		return SubagentEndedMsg{AgentID: p.AgentID, AgentType: p.AgentType, TaskID: p.TaskID, Status: p.Status, Error: p.Error, Output: p.Output, ParentToolCallID: p.ParentToolCallID, TaskIndex: p.TaskIndex, ExecutionID: p.ExecutionID, FinishedAt: time.UnixMilli(p.FinishedAtMs), Timestamp: evt.CreatedAt}, true
	case event.RunEventAssistantDelta:
		var p event.AssistantDeltaPayload
		if json.Unmarshal(evt.Payload, &p) != nil || strings.TrimSpace(p.AgentID) == "" || p.Text == "" {
			return nil, false
		}
		return NewMessageMsg{Msg: Message{Kind: MsgKindAssistant, Content: p.Text, AgentID: p.AgentID, RunID: evt.RunID, Timestamp: evt.CreatedAt}}, true
	case event.RunEventReasoningDelta:
		var p event.ReasoningDeltaPayload
		if json.Unmarshal(evt.Payload, &p) != nil || strings.TrimSpace(p.AgentID) == "" || p.Text == "" {
			return nil, false
		}
		return NewMessageMsg{Msg: Message{Kind: MsgKindReasoning, Content: p.Text, AgentID: p.AgentID, RunID: evt.RunID, Timestamp: evt.CreatedAt}}, true
	case event.RunEventReasoningDone:
		var p event.ReasoningDonePayload
		if json.Unmarshal(evt.Payload, &p) != nil || strings.TrimSpace(p.AgentID) == "" {
			return nil, false
		}
		return ReasoningDoneMsg{AgentID: p.AgentID, Timestamp: evt.CreatedAt}, true
	case event.RunEventUsageDelta:
		var p event.UsageDeltaPayload
		if json.Unmarshal(evt.Payload, &p) != nil || strings.TrimSpace(p.AgentID) == "" {
			return nil, false
		}
		return TokenUsageDeltaMsg{RunID: evt.RunID, AgentID: p.AgentID, InputTokens: p.InputTokens, OutputTokens: p.OutputTokens}, true
	case event.RunEventToolStarted:
		var p event.ToolCallStartedPayload
		if json.Unmarshal(evt.Payload, &p) != nil || replayToolEventSkipped(p.ToolMeta) {
			return nil, false
		}
		return subagentToolStepMsg(evt, p.StepID, p.ToolName, firstNonEmpty(p.Summary, p.Description), p.ToolMeta, evt.Type), true
	case event.RunEventToolCompleted:
		var p event.ToolCallCompletedPayload
		if json.Unmarshal(evt.Payload, &p) != nil || replayToolEventSkipped(p.ToolMeta) {
			return nil, false
		}
		msg := subagentToolStepMsg(evt, p.StepID, p.ToolName, firstNonEmpty(p.Summary, p.Description), p.ToolMeta, evt.Type)
		msg.Msg.Content = p.DisplayBody
		msg.Msg.Duration = time.Duration(p.DurationSeconds * float64(time.Second))
		msg.Msg.RetainAsHistory = p.RetainAsHistory
		return msg, true
	case event.RunEventToolOutputDelta:
		var p event.ToolCallOutputDeltaPayload
		if json.Unmarshal(evt.Payload, &p) != nil || strings.TrimSpace(p.AgentID) == "" || p.Text == "" {
			return nil, false
		}
		msg := subagentToolStepMsg(evt, p.StepID, p.ToolName, p.Summary, p.ToolMeta, evt.Type)
		msg.Msg.Content = p.Text
		msg.Msg.ToolOutputDelta = true
		return msg, true
	case event.RunEventPlanUpdated:
		// A subagent's plan card is rebuilt from the event the live run
		// published for it: the transcript rows of the session_todo call that
		// emitted it belong to that subagent and are not part of the primary
		// replay at all, so this event is the only record the card has. A
		// conversation's own plan update carries no AgentID and replays from
		// the transcript row of the call that emitted it, at that row's exact
		// position (takeReplayPlanUpdate) — anchoring it here instead would
		// stamp it at a turn boundary, ahead of the message that produced it.
		var p event.PlanUpdatedPayload
		if json.Unmarshal(evt.Payload, &p) != nil || strings.TrimSpace(p.AgentID) == "" {
			return nil, false
		}
		return PlanUpdatedMsg{Payload: p}, true
	case event.RunEventPlanReviewStarted:
		var p event.PlanReviewStartedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return nil, false
		}
		return PlanReviewStartedMsg{ReviewID: p.ReviewID, Provider: p.Provider, Model: p.Model}, true
	case event.RunEventPlanReviewed:
		var p event.PlanReviewedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return nil, false
		}
		return PlanReviewReviewedMsg{ReviewID: p.ReviewID, Provider: p.Provider, Model: p.Model, Outcome: p.Outcome, Error: p.Error}, true
	case event.RunEventApprovalReq:
		var p event.ApprovalRequestedPayload
		if json.Unmarshal(evt.Payload, &p) != nil || strings.TrimSpace(p.AgentID) == "" {
			return nil, false
		}
		return replayApprovalMessage(evt, p.AgentID, p.ActionID, p.ActionKind, "waiting approval", p.Message), true
	case event.RunEventApprovalResolved:
		var p event.ApprovalResolvedPayload
		if json.Unmarshal(evt.Payload, &p) != nil || strings.TrimSpace(p.AgentID) == "" {
			// The conversation's own approval has no message here: its gate card
			// is the tool call the record names, rebuilt as a canceled card when
			// that call never ran, and the confirmation line it printed comes
			// from replayApprovalEventFrames. A subagent's stays a message so it
			// keeps moving that agent's roster row the way the live decision
			// did.
			return nil, false
		}
		return replayApprovalMessage(evt, p.AgentID, p.ActionID, p.ActionKind, firstNonEmpty(p.Decision, "resolved"), p.Reason), true
	case event.RunEventSubagentInputDelivered:
		// A message the user sent this subagent, replayed as the user message it
		// was in that subagent's own view.
		var p event.SubagentInputDeliveredPayload
		if json.Unmarshal(evt.Payload, &p) != nil || strings.TrimSpace(p.AgentID) == "" || strings.TrimSpace(p.Text) == "" {
			return nil, false
		}
		return SubagentInputDeliveredMsg{AgentID: strings.TrimSpace(p.AgentID), RunID: evt.RunID, Text: strings.TrimSpace(p.Text)}, true
	case event.CompactEventBudgetUpdated:
		var p event.TokenBudgetUpdatedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return nil, false
		}
		return tokenBudgetMsgFromPayload(p), true
	default:
		return nil, false
	}
}

// replayToolEventSkipped reports whether a persisted tool event belongs to no
// view on resume. Subagent steps (AgentID-tagged) replay into that agent's
// roster; an AgentID-less step is a main-agent call whose card the transcript
// tool row already rebuilds — with one exception: an explicit skill step has no
// transcript row at all, so its card can only come from this event log.
func replayToolEventSkipped(meta event.ToolCallMeta) bool {
	if strings.TrimSpace(meta.AgentID) != "" {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(meta.Category), "skill")
}

// replayWorkerBody walks one subagent execution's rebuilt messages as its delta
// events replay (plan 017). A message is one block - a thinking block, one tool
// card, or a run of answer text - and its events name the same blocks in the
// same order, so a reload spends one message at each block boundary and drops
// the rest of that block's deltas.
type replayWorkerBody struct {
	messages []any
	cursor   int
	group    string
	stepID   string
}

// next returns the following rebuilt message, or nil when the body is spent.
func (b *replayWorkerBody) next() any {
	if b == nil || b.cursor >= len(b.messages) {
		return nil
	}
	message := b.messages[b.cursor]
	b.cursor++
	return message
}

// consume drops one delta event of a worker-backed execution, returning the
// rebuilt message a new block group starts with - nil when the delta continues
// the open group. It reports false for events that are not block deltas, which
// the caller routes as usual.
func (b *replayWorkerBody) consume(evt event.RunEvent) (any, bool) {
	switch evt.Type {
	case event.RunEventReasoningDelta:
		if b.group != "thinking" {
			b.group = "thinking"
			return b.next(), true
		}
		return nil, true
	case event.RunEventReasoningDone:
		b.group = ""
		return nil, true
	case event.RunEventAssistantDelta:
		if b.group != "assistant" {
			b.group = "assistant"
			return b.next(), true
		}
		return nil, true
	case event.RunEventToolStarted:
		var p event.ToolCallStartedPayload
		_ = json.Unmarshal(evt.Payload, &p)
		return b.startTool(p.StepID), true
	case event.RunEventToolCompleted:
		var p event.ToolCallCompletedPayload
		_ = json.Unmarshal(evt.Payload, &p)
		return b.startTool(p.StepID), true
	case event.RunEventToolOutputDelta:
		return nil, true
	default:
		return nil, false
	}
}

func (b *replayWorkerBody) startTool(stepID string) any {
	stepID = strings.TrimSpace(stepID)
	if b.group != "tool" || b.stepID != stepID {
		b.group = "tool"
		b.stepID = stepID
		return b.next()
	}
	return nil
}

// buildReplayWorkerBodies rebuilds, per execution, the messages a subagent's
// view draws from its own worker transcript. A spawn with no worker session id,
// a worker session whose rows are absent, and an execution with no rows of its
// own all leave that execution on the event path.
func buildReplayWorkerBodies(events []event.RunEvent, workerTranscripts map[string][]state.Message) map[string]*replayWorkerBody {
	if len(workerTranscripts) == 0 {
		return nil
	}
	out := make(map[string]*replayWorkerBody)
	for i := range events {
		if events[i].Type != event.RunEventSubagentSpawned {
			continue
		}
		var p event.SubagentSpawnedPayload
		if json.Unmarshal(events[i].Payload, &p) != nil {
			continue
		}
		workerSessionID := strings.TrimSpace(p.WorkerSessionID)
		runID := strings.TrimSpace(events[i].RunID)
		if workerSessionID == "" || runID == "" {
			continue
		}
		if _, done := out[runID]; done {
			continue
		}
		rows := workerTranscripts[workerSessionID]
		if len(rows) == 0 {
			continue
		}
		// Execution id and run id are the same child run, so the rows of this
		// execution are the ones carrying it.
		execRows := make([]state.Message, 0, len(rows))
		for _, row := range rows {
			if strings.TrimSpace(row.RunID) == runID {
				execRows = append(execRows, row)
			}
		}
		if len(execRows) == 0 {
			continue
		}
		messages := replayWorkerMessages(execRows, strings.TrimSpace(p.AgentID))
		if len(messages) == 0 {
			continue
		}
		out[runID] = &replayWorkerBody{messages: messages}
	}
	return out
}

// replayWorkerMessages converts one worker execution's rows into the reducer
// messages a subagent's view draws: its thinking (sealed with the duration the
// row recorded), its answer, and its tool cards, each tagged with the subagent's
// roster key. The dispatching prompt, a delivered steer's user row and the run's
// worked line stay with the events that own them, so they are skipped here.
func replayWorkerMessages(turns []state.Message, agentID string) []any {
	callIndex, _ := buildToolCallIndex(turns)
	subagentCalls := turn.SubagentCallsInTranscript(turns)
	out := make([]any, 0, len(turns))
	for i := range turns {
		turn := turns[i]
		if strings.EqualFold(strings.TrimSpace(turn.Role), "system") {
			continue
		}
		meta := transcriptToolMeta(turn)
		meta.AgentID = strings.TrimSpace(agentID)
		switch msgKindFromRole(turn.Role) {
		case MsgKindAssistant:
			content := strings.TrimSpace(turn.Content)
			if content == "" {
				continue
			}
			out = append(out, NewMessageMsg{Msg: Message{Kind: MsgKindAssistant, Content: content, AgentID: agentID}})
		case MsgKindReasoning:
			content := strings.TrimSpace(turn.Content)
			if content == "" {
				continue
			}
			msg := Message{Kind: MsgKindReasoning, Content: content, AgentID: agentID, ToolMeta: meta}
			if spent, ok := state.ParseReasoningTiming(turn.PartsJSON); ok {
				msg.Duration = spent
			}
			out = append(out, NewMessageMsg{Msg: msg})
		case MsgKindTool:
			msg, _, ok := replayToolMessage(turn, meta, callIndex, subagentCalls)
			if !ok {
				continue
			}
			msg.AgentID = strings.TrimSpace(agentID)
			out = append(out, NewMessageMsg{Msg: msg})
		}
	}
	return out
}

func replayApprovalMessage(evt event.RunEvent, agentID, actionID, actionKind, status, body string) NewMessageMsg {
	name := firstNonEmpty(actionKind, "approval")
	return NewMessageMsg{Msg: Message{
		Kind: MsgKindTool, StepID: actionID, ToolName: name, Summary: status,
		Content: body, AgentID: agentID, RunID: evt.RunID, ToolPhase: evt.Type,
		ToolMeta:  tool.ToolMeta{ToolName: name, Status: status, AgentID: agentID, Category: "approval"},
		Timestamp: evt.CreatedAt,
	}}
}

type transcriptTurnTiming struct {
	duration time.Duration
}

func transcriptTurnTimingFromTurn(turn state.Message) transcriptTurnTiming {
	// A tool card's duration is the call's own execution time; the run's
	// worked time is the Worked-for line's, reported once per run.
	return transcriptTurnTiming{
		duration: time.Duration(turn.ExecDurationMs) * time.Millisecond,
	}
}

func applyReplayToolTiming(frame *Frame, msg Message, timing transcriptTurnTiming) {
	if frame == nil || frame.Kind != FrameTool {
		return
	}
	if frame.Duration <= 0 {
		frame.Duration = msg.Duration
	}
	if frame.Duration <= 0 {
		frame.Duration = timing.duration
	}
}

// replayToolCall is the reconstructed identity of a stored tool call: the tool
// name and its raw argument JSON, both taken from the assistant row's tool_calls
// part. Keyed by tool_call_id in buildToolCallIndex.
type replayToolCall struct {
	name string
	args string
	// order is the position this call held in the transcript's stream of tool
	// calls. Replay reads the index out of a map, so this is what keeps a batch
	// of calls in the order the model asked for them.
	order int
}

// buildToolCallIndex scans every assistant turn's tool_calls part and maps each
// tool_call_id to the tool name and argument JSON it was invoked with, plus the
// last assistant row that issued it. Tool-role rows store only a tool_call_id
// back-reference, so this is the only place the tool name and arguments are
// recoverable for replay; the row index is what places a card for a call no
// tool row ever answered.
func buildToolCallIndex(turns []state.Message) (map[string]replayToolCall, map[string]int) {
	index := make(map[string]replayToolCall)
	rowByCallID := make(map[string]int)
	order := 0
	for i := range turns {
		if !strings.EqualFold(strings.TrimSpace(turns[i].Role), "assistant") {
			continue
		}
		_, toolCalls, _, _ := state.ParseMessageParts(turns[i].PartsJSON, "")
		for _, tc := range toolCalls {
			id := strings.TrimSpace(tc.ID)
			name := strings.TrimSpace(tc.Function.Name)
			if id == "" || name == "" {
				continue
			}
			index[id] = replayToolCall{name: name, args: strings.TrimSpace(tc.Function.Arguments), order: order}
			rowByCallID[id] = i
			order++
		}
	}
	return index, rowByCallID
}

// replayToolMessage reconstructs the Message for one stored tool-role
// row. The tool name and arguments come from the correlated tool_calls entry
// (via tool_call_id); the invocation label and summary are produced by the same
// agentnotify helpers the live tool-step path uses, so a replayed tool card
// reads identically to how it first appeared. ok is false when the row carries
// no output to show.
func replayToolMessage(turn state.Message, meta tool.ToolMeta, callIndex map[string]replayToolCall, subagentCalls map[string]event.SubagentCall) (Message, string, bool) {
	_, _, toolCallID, _ := state.ParseMessageParts(turn.PartsJSON, "")
	toolCallID = strings.TrimSpace(toolCallID)
	call := callIndex[toolCallID]
	toolName := strings.TrimSpace(call.name)
	if toolName == "" {
		toolName = transcriptToolName(turn, meta)
	}
	if toolName == "" {
		toolName = strings.TrimSpace(meta.ToolName)
	}
	toolName = displayToolName(toolName)
	meta.ToolName = displayToolName(meta.ToolName)
	body := strings.TrimSpace(turn.Content)
	storedDisplay, hasStoredDisplay := state.ParseToolDisplayPart(turn.PartsJSON)
	if body == "" && !hasStoredDisplay {
		return Message{}, "", false
	}
	displayBody := ""
	if hasStoredDisplay {
		displayBody = strings.TrimSpace(storedDisplay.Body)
		if storedMeta := toolMetaFromJSON(storedDisplay.ToolMetaJSON); strings.TrimSpace(storedMeta.ToolName) != "" {
			meta = storedMeta
		}
	} else {
		displayBody = replayToolDisplayBody(toolName, call.args, body)
	}
	// A delivered review closed this gate, not the user: the stored display
	// body is the delivery drop key, so the row draws nothing — the handoff
	// it recorded was never user-facing. A call a queued message took the
	// turn from never ran either: its row carries the same kind of drop key
	// and draws nothing.
	if hasStoredDisplay && isHiddenToolDisplay(storedDisplay.Body) {
		return Message{}, "", false
	}
	// The gate's wait holds no card on any surface, live included, and a
	// canceled gate was abandoned to that same wait: replay records the
	// abandonment only in the approval confirmation line, never in a card.
	if tool.ToolStepHoldsNoCard(toolName, meta.Status) {
		return Message{}, "", false
	}
	summary := strings.TrimSpace(transcriptToolSummaryWithMeta(body, meta))
	if hasStoredDisplay && strings.TrimSpace(storedDisplay.Summary) != "" {
		summary = strings.TrimSpace(storedDisplay.Summary)
	}
	if summary == "" {
		summary = replayToolSummary(toolName, call.args)
	}
	if !hasStoredDisplay || strings.TrimSpace(meta.ToolName) == "" {
		meta = replayToolMeta(toolName, call.args, meta)
	}
	// A subagent_* call's card is its facts. The stored display body is what
	// the row's own day drew — JSON for rows older than the card — and a
	// replay that showed it would put the raw record back on screen. The call's
	// error is the one thing the body still carries for a failed call.
	if facts, ok := subagentCalls[toolCallID]; ok {
		meta.SubagentCall = &facts
		if !strings.EqualFold(strings.TrimSpace(meta.Status), "failed") {
			displayBody = ""
		}
	}
	return Message{
		Kind:      MsgKindTool,
		Content:   displayBody,
		StepID:    toolCallID,
		ToolName:  toolName,
		ToolMeta:  meta,
		Summary:   summary,
		AgentID:   strings.TrimSpace(meta.AgentID),
		ToolPhase: tool.StepKindToolCompleted,
		// The row's own execution clock, the same source the shell replay
		// path reads: a settled card names how long its call took.
		Duration: time.Duration(turn.ExecDurationMs) * time.Millisecond,
		FilePath: replayToolFilePath(meta.Input, body),
	}, toolName, true
}

// replayToolFilePath reproduces the live notification's file-path derivation
// (extractFilePathFromEvent) from the same facts the stored row holds: the
// call's path argument, or a read the engine repaired into another file — the
// card has to name the file that was actually read.
func replayToolFilePath(input map[string]any, body string) string {
	var output map[string]any
	if raw := strings.TrimSpace(body); raw != "" && strings.HasPrefix(raw, "{") {
		_ = json.Unmarshal([]byte(raw), &output)
	}
	// A read_file whose path was repaired into the skill catalog read a
	// different file than the arguments name; the card has to name the file
	// that was actually read.
	if output != nil {
		if from, _ := output["repaired_from"].(string); strings.TrimSpace(from) != "" {
			if abs, _ := output["abs_path"].(string); strings.TrimSpace(abs) != "" {
				return strings.TrimSpace(abs)
			}
		}
	}
	if fp := extractFilePathFromInput(input); fp != "" {
		return fp
	}
	return ""
}

// takeReplayPlanUpdate pairs one primary session_todo transcript row with the
// plan card the live run drew in its place, consuming the card from the queue.
// The pairing is positional: each completed call emitted exactly one plan
// event while it ran, and its transcript row was written when the run
// flushed, so the events and the rows of successful calls appear in the same
// order on both sides. The row keeps its own card when it is not a
// plan-emitting tool, when the call failed (a failure has no plan card to be
// represented by — the live hook keeps the card then, and so does the event
// projection), or when the queue is empty and this row is the only record the
// session has of the call.
func takeReplayPlanUpdate(toolName string, meta tool.ToolMeta, queue *[]event.PlanUpdatedPayload) (event.PlanUpdatedPayload, bool) {
	if queue == nil || len(*queue) == 0 || !event.PlanEmittingTool(toolName) {
		return event.PlanUpdatedPayload{}, false
	}
	if strings.EqualFold(strings.TrimSpace(meta.Status), "failed") {
		return event.PlanUpdatedPayload{}, false
	}
	payload := (*queue)[0]
	*queue = (*queue)[1:]
	return payload, true
}

func toolMetaFromJSON(raw string) tool.ToolMeta {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return tool.ToolMeta{}
	}
	var meta tool.ToolMeta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return tool.ToolMeta{}
	}
	return meta
}

// replayToolDisplayBody rebuilds model-facing JSON tool results through the
// same formatter used by live tool-completion notifications. Feeding the raw
// transport JSON directly to the renderer exposes implementation details (for
// example escaped shell stdout or the request_permissions response envelope)
// that were never shown in the live conversation. Keep malformed and legacy
// payloads unchanged so replay remains lossless when reconstruction is not
// possible.
func replayToolDisplayBody(toolName string, argsJSON string, body string) string {
	body = strings.TrimSpace(body)
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	if body == "" {
		return body
	}
	switch toolName {
	case "shell", "request_permissions", "memories_add_ad_hoc_note",
		"memories_list", "memories_read", "memories_search", "web_fetch":
	default:
		return body
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(body), &output); err != nil {
		return body
	}
	var input map[string]any
	if raw := strings.TrimSpace(argsJSON); raw != "" {
		_ = json.Unmarshal([]byte(raw), &input)
	}
	if toolName == "memories_add_ad_hoc_note" {
		// The model-facing success result carries the stored path; sessions
		// recorded before that was returned carry an empty object. Rebuild the
		// notification envelope expected by the live formatter for the legacy
		// shape, which can then recover the Markdown body from the correlated
		// call arguments; a path-carrying body already formats as-is.
		if len(output) == 0 {
			output = map[string]any{"output": map[string]any{}}
		}
	} else if len(output) == 0 {
		return body
	}
	if toolName == "request_permissions" {
		prepareReplayRequestPermissionsOutput(input, output)
	}
	formatted, _ := tool.FormatToolStepResult(tool.StepEvent{
		Kind:     tool.StepKindToolCompleted,
		ToolName: toolName,
		Input:    input,
		Output:   output,
	}, tool.DefaultMaxFormattedBody)
	if formatted = strings.TrimSpace(formatted); formatted != "" {
		return formatted
	}
	return body
}

// prepareReplayRequestPermissionsOutput fills the lifecycle fields that live
// completion events carry separately from the model-facing response. A stored
// non-empty permission grant is a successfully approved request. Denied legacy
// responses contain no grants, so show the originally requested profile and a
// denied outcome instead of an empty JSON object.
func prepareReplayRequestPermissionsOutput(input map[string]any, output map[string]any) {
	if len(output) == 0 {
		return
	}
	if _, exists := output["permissions"]; !exists && input != nil {
		output["permissions"] = input["permissions"]
	}
	if scope, exists := output["scope"]; (!exists || scope == nil || strings.TrimSpace(fmt.Sprint(scope)) == "") && input != nil {
		output["scope"] = input["scope"]
	}
	if status, exists := output["approval_status"]; exists && status != nil && strings.TrimSpace(fmt.Sprint(status)) != "" {
		return
	}
	if replayPermissionGrantCount(output["permissions"]) > 0 {
		output["approval_status"] = "approved"
		return
	}
	if input != nil {
		output["permissions"] = input["permissions"]
	}
	output["approval_status"] = "denied"
}

func replayPermissionGrantCount(raw any) int {
	b, err := json.Marshal(raw)
	if err != nil {
		return 0
	}
	var profile struct {
		FileSystem *struct {
			Read    []string `json:"read"`
			Write   []string `json:"write"`
			Entries []any    `json:"entries"`
		} `json:"file_system"`
	}
	if err := json.Unmarshal(b, &profile); err != nil || profile.FileSystem == nil {
		return 0
	}
	return len(profile.FileSystem.Read) + len(profile.FileSystem.Write) + len(profile.FileSystem.Entries)
}

func replayToolMeta(toolName string, argsJSON string, meta tool.ToolMeta) tool.ToolMeta {
	return replayToolStepMeta(toolName, argsJSON, meta, tool.StepKindToolCompleted)
}

// replayToolStepMeta rebuilds a card's meta as the live step hook built it at
// the given phase. A stored tool row is a completion; a call that never ran is
// still the card its dispatch produced.
func replayToolStepMeta(toolName string, argsJSON string, meta tool.ToolMeta, kind string) tool.ToolMeta {
	toolName = displayToolName(toolName)
	meta.ToolName = displayToolName(meta.ToolName)
	var input map[string]any
	if raw := strings.TrimSpace(argsJSON); raw != "" {
		_ = json.Unmarshal([]byte(raw), &input)
	}
	base := tool.BuildToolMeta(tool.StepEvent{
		Kind:     kind,
		ToolName: toolName,
		Input:    input,
	})
	if strings.TrimSpace(meta.ToolName) != "" {
		base.ToolName = strings.TrimSpace(meta.ToolName)
	}
	if strings.TrimSpace(meta.Status) != "" {
		base.Status = strings.TrimSpace(meta.Status)
	}
	if strings.TrimSpace(meta.Purpose) != "" {
		base.Purpose = strings.TrimSpace(meta.Purpose)
	}
	if strings.TrimSpace(meta.Invocation) != "" {
		base.Invocation = strings.TrimSpace(meta.Invocation)
	}
	if len(meta.Input) > 0 {
		base.Input = meta.Input
	}
	base.AgentID = strings.TrimSpace(meta.AgentID)
	base.AgentType = strings.TrimSpace(meta.AgentType)
	base.AgentKind = strings.TrimSpace(meta.AgentKind)
	base.Category = strings.TrimSpace(meta.Category)
	base.SkillName = strings.TrimSpace(meta.SkillName)
	base.SkillPath = strings.TrimSpace(meta.SkillPath)
	return base
}

// replayToolSummary builds a completed-step summary ("ran <invocation>") for a
// replayed tool call by reconstructing a StepEvent from the stored tool name and
// argument JSON and running it through the live agentnotify summarizer. Falls
// back to the bare tool name when arguments are absent or unparseable.
func replayToolSummary(toolName string, argsJSON string) string {
	return replayToolStepSummary(toolName, argsJSON, tool.StepKindToolCompleted)
}

// replayToolStepSummary is the one line the live step hook put on a card at the
// given phase ("running ls" when it was dispatched, "ran ls · 3 files" once it
// answered).
func replayToolStepSummary(toolName string, argsJSON string, kind string) string {
	toolName = displayToolName(toolName)
	if toolName == "" {
		return ""
	}
	var input map[string]any
	if raw := strings.TrimSpace(argsJSON); raw != "" {
		_ = json.Unmarshal([]byte(raw), &input)
	}
	evt := tool.StepEvent{
		Kind:     kind,
		ToolName: toolName,
		Input:    input,
	}
	if summary := strings.TrimSpace(tool.SummarizeToolStep(evt)); summary != "" {
		return summary
	}
	return toolName
}

func transcriptLooksLikeUserShellRecord(turn state.Message, meta tool.ToolMeta) bool {
	if !strings.EqualFold(strings.TrimSpace(turn.Role), "user") {
		return false
	}
	if strings.TrimSpace(meta.ToolName) == "shell" {
		return true
	}
	content := strings.TrimSpace(turn.Content)
	return strings.HasPrefix(content, "<user_shell_command>") && strings.Contains(content, "</user_shell_command>")
}

func transcriptUserShellDisplayBody(turn state.Message) string {
	return extractUserShellResultBody(strings.TrimSpace(turn.Content))
}

func extractUserShellResultBody(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	start := strings.Index(content, "<result>\n")
	if start < 0 {
		return strings.TrimSpace(content)
	}
	body := content[start+len("<result>\n"):]
	if end := strings.Index(body, "\n</result>"); end >= 0 {
		body = body[:end]
	}
	return strings.TrimSpace(body)
}

func toolNameFromTranscriptContent(content string) string {
	return transcriptMetadataValue(content, "tool:")
}

func transcriptToolName(turn state.Message, meta tool.ToolMeta) string {
	if name := strings.TrimSpace(meta.ToolName); name != "" {
		return displayToolName(name)
	}
	return displayToolName(toolNameFromTranscriptContent(turn.Content))
}

func displayToolName(name string) string {
	return strings.TrimSpace(name)
}

func transcriptToolSummaryWithMeta(content string, meta tool.ToolMeta) string {
	status := strings.TrimSpace(meta.Status)
	invocation := strings.TrimSpace(meta.Invocation)
	if status != "" {
		switch strings.ToLower(status) {
		case "completed":
			if invocation != "" {
				return "ran " + invocation
			}
			return status
		case "running":
			if invocation != "" {
				return "running " + invocation
			}
			return status
		default:
			if invocation != "" {
				return status + " · " + invocation
			}
			return status
		}
	}
	if status := transcriptMetadataValue(content, "status:"); status != "" {
		if invocation := transcriptMetadataValue(content, "invocation:"); invocation != "" {
			switch strings.ToLower(status) {
			case "completed":
				return "ran " + invocation
			case "running":
				return "running " + invocation
			default:
				return status + " · " + invocation
			}
		}
		return status
	}
	return ""
}

func transcriptToolMeta(turn state.Message) tool.ToolMeta {
	return toolMetaFromJSON(turn.ToolMetaJSON)
}

func transcriptToolDisplayTitle(toolName string, meta tool.ToolMeta) string {
	title := strings.TrimSpace(toolName)
	if title == "" {
		title = strings.TrimSpace(meta.ToolName)
	}
	if title == "" {
		title = "tool"
	}
	return title
}

func transcriptMetadataValue(content string, label string) string {
	want := strings.ToLower(strings.TrimSpace(label))
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r", ""), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(trimmed), want) {
			continue
		}
		value := strings.TrimSpace(trimmed[len(label):])
		value = strings.Trim(value, "`")
		return strings.TrimSpace(value)
	}
	return ""
}

func msgKindFromRole(role string) MsgKind {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		return MsgKindUser
	case "assistant":
		return MsgKindAssistant
	case "reasoning":
		return MsgKindReasoning
	case "tool":
		return MsgKindTool
	case "system":
		return MsgKindSystem
	case "plan":
		return MsgKindPlan
	case "error":
		return MsgKindError
	default:
		return MsgKindSystem
	}
}

func (c *commandController) handleMemories(sessionID string) bool {
	if c == nil || c.session == nil || c.selector == nil {
		return true
	}
	memorySession, ok := c.session.(interface {
		MemorySettings() (enabled, useMemories, generateMemories bool)
		UpdateMemorySettings(sessionID string, featureEnabled, useMemories, generateMemories *bool) error
		ResetMemories(ctx context.Context, all bool) error
	})
	if !ok {
		c.renderer.PrintError(fmt.Errorf("memory settings unavailable"))
		return true
	}
	enabled, useMemories, generateMemories := memorySession.MemorySettings()
	if !enabled {
		enable, ok, err := c.selector.Confirm("Enable memories?", true)
		if err != nil {
			c.renderer.PrintError(err)
			return true
		}
		if !ok || !enable {
			return true
		}
		if err := memorySession.UpdateMemorySettings(sessionID, boolPointer(true), nil, nil); err != nil {
			c.renderer.PrintError(err)
			return true
		}
		c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "memories", Content: "Memories enabled. Start a new thread to use them.", Final: true})
		return true
	}
	const (
		useOption      = "Use memories — Use memories in following threads. Applied at next thread."
		generateOption = "Generate memories — Generate memories from following threads. Current thread included."
		resetOption    = "Reset memories — Clear local memory files and summaries. Threads remain intact."
	)
	if selector, ok := c.selector.(MemorySettingsSelector); ok {
		result, err := selector.SelectMemorySettings(useMemories, generateMemories)
		if err != nil {
			c.renderer.PrintError(err)
			return true
		}
		switch result.Action {
		case MemorySettingsSave:
			if err := memorySession.UpdateMemorySettings(sessionID, nil, &result.UseMemories, &result.GenerateMemories); err != nil {
				c.renderer.PrintError(err)
				return true
			}
			c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "memories", Content: "Memory settings saved.", Final: true})
		case MemorySettingsResetProject:
			if err := memorySession.ResetMemories(context.Background(), false); err != nil {
				c.renderer.PrintError(err)
				return true
			}
			c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "memories", Content: "Reset this project's memories.", Final: true})
		case MemorySettingsResetAll:
			if err := memorySession.ResetMemories(context.Background(), true); err != nil {
				c.renderer.PrintError(err)
				return true
			}
			c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "memories", Content: "Reset every project's memories and global preferences.", Final: true})
		}
		return true
	}
	stagedUse, stagedGenerate := useMemories, generateMemories
	const saveOption = "Save memory settings"
	for {
		selected, ok, err := c.selector.Select("Memories\nConfigure memory use and generation", []string{useOption, generateOption, resetOption, saveOption}, saveOption)
		if err != nil {
			c.renderer.PrintError(err)
			return true
		}
		if !ok {
			return true
		}
		switch strings.TrimSpace(selected) {
		case useOption:
			stagedUse = !stagedUse
		case generateOption:
			stagedGenerate = !stagedGenerate
		case saveOption:
			if err := memorySession.UpdateMemorySettings(sessionID, nil, &stagedUse, &stagedGenerate); err != nil {
				c.renderer.PrintError(err)
			}
			return true
		case resetOption:
			const (
				resetProjectOption = "Reset this project's memories"
				resetAllOption     = "Reset everything (every project + global)"
				goBackOption       = "Go back"
			)
			resetAction, accepted, selectErr := c.selector.Select("Reset memories", []string{resetProjectOption, resetAllOption, goBackOption}, goBackOption)
			if selectErr != nil {
				c.renderer.PrintError(selectErr)
				return true
			}
			if !accepted {
				continue
			}
			switch strings.TrimSpace(resetAction) {
			case resetProjectOption:
				if err := memorySession.ResetMemories(context.Background(), false); err != nil {
					c.renderer.PrintError(err)
					return true
				}
				c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "memories", Content: "Reset this project's memories.", Final: true})
				return true
			case resetAllOption:
				if err := memorySession.ResetMemories(context.Background(), true); err != nil {
					c.renderer.PrintError(err)
					return true
				}
				c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "memories", Content: "Reset every project's memories and global preferences.", Final: true})
				return true
			default:
				continue
			}
		}
	}
}

func boolPointer(value bool) *bool { return &value }

// handlePermissionsReport answers /permissions' one inline form, explain
// <tool>, as a report frame.
func (c *commandController) handlePermissionsReport(sessionID string, args []string) bool {
	reply, handled := c.session.HandlePermissionsSlash(strings.TrimSpace(sessionID), "tui", args)
	if !handled || strings.TrimSpace(reply) == "" {
		return false
	}
	c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "permissions", Content: strings.TrimSpace(reply), Final: true})
	return true
}

// handlePermissions opens the approval preset picker.
//
// The picker is the whole of the interactive permission surface: it selects a
// sandbox and an approval policy together, and nothing else. Editing individual
// rules from here was removed deliberately — rules that matter beyond one
// session belong in a settings file that can be reviewed, not in a prompt whose
// result nobody can see afterwards.
func (c *commandController) handlePermissions(sessionID string) bool {
	presets, current := c.session.PermissionPresets(sessionID)
	// The picker opens on the preset in force.
	items := make([]SelectItem, 0, len(presets))
	inForce := -1
	for i, preset := range presets {
		items = append(items, SelectItem{Label: preset.Label, Description: preset.Description})
		if preset.ID == current {
			inForce = i
		}
	}
	idx, ok, err := c.selector.SelectRich("Permissions\nWhat Forebrain Harness may do in this session without asking", items, inForce)
	if err != nil {
		c.renderer.PrintError(err)
		return true
	}
	if !ok || idx < 0 || idx >= len(presets) {
		return true
	}
	reply, applyErr := c.session.ApplyPermissionPreset(sessionID, presets[idx].ID)
	if applyErr != nil {
		c.renderer.PrintError(applyErr)
		return true
	}
	if text := strings.TrimSpace(reply); text != "" {
		c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "permissions", Content: text, Final: true})
	}
	return true
}

func (c *commandController) handleStatus(sessionID string) bool {
	// Interactive surface: the panel replaces the report frame. Non-TTY
	// callers keep the markdown report as a plain frame.
	if c.openPanel != nil && c.openPanel("status") {
		return true
	}
	reply, handled := c.session.HandleStatusSlash(strings.TrimSpace(sessionID), "tui", false)
	if !handled || strings.TrimSpace(reply) == "" {
		return false
	}
	c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "status", Content: strings.TrimSpace(reply), Final: true})
	return true
}

// handleSandbox opens the sandbox report as an overlay. The command takes no
// arguments: there is exactly one thing to show, and nothing to set — the
// sandbox is chosen with /permissions, together with the approval policy.
func (c *commandController) handleSandbox(sessionID string) bool {
	if c == nil || c.session == nil || c.renderer == nil {
		return false
	}
	reply, handled := c.session.HandleSandboxSlash(strings.TrimSpace(sessionID), "tui", nil)
	if !handled || strings.TrimSpace(reply) == "" {
		return false
	}
	if selector, ok := c.selector.(InfoOverlaySelector); ok {
		if err := selector.ShowInfo("Sandbox", strings.TrimSpace(reply)); err != nil {
			c.renderer.PrintError(err)
		}
		return true
	}
	// No overlay surface (non-TTY, tests): the report still has to arrive.
	c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "sandbox", Content: strings.TrimSpace(reply), Final: true})
	return true
}

func (c *commandController) handleMCP(sessionID string) bool {
	if c.openPanel != nil && c.openPanel("mcp") {
		return true
	}
	reply, handled := c.session.HandleMCPSlash(strings.TrimSpace(sessionID), "tui")
	if !handled || strings.TrimSpace(reply) == "" {
		return false
	}
	c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "mcp", Content: strings.TrimSpace(reply), Final: true})
	return true
}

// handleLSP opens the /lsp panel in viewport mode and falls back to the text
// inventory everywhere else, the same two-step shape /mcp uses.
func (c *commandController) handleLSP(sessionID string) bool {
	if c.openPanel != nil && c.openPanel("lsp") {
		return true
	}
	reply, handled := c.session.HandleLSPSlash(strings.TrimSpace(sessionID), "tui")
	if !handled || strings.TrimSpace(reply) == "" {
		return false
	}
	c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "lsp", Content: strings.TrimSpace(reply), Final: true})
	return true
}

// migrateSession is the slice of ChatSession the /migrate flow drives: the
// option builder wired to this environment, the async dry run whose plan the
// confirm dialog shows, and the async real run.
type migrateSession interface {
	BuildMigrateOptions(onlyProject bool, sessionID string, sourceRoot string) *migrate.Options
	PreviewMigrationAsync(ctx context.Context, opts *migrate.Options, run func())
	RunMigrationAsync(ctx context.Context, opts *migrate.Options)
}

// handleMigrate drives /migrate's picker flow: choose a source (single
// select, first row highlighted), choose a scope, review the dry-run preview,
// confirm, then the import runs on a goroutine and reports back through the
// notification queue. Fully-specified inline arguments skip the pickers —
// the scriptable form.
func (c *commandController) handleMigrate(ctx context.Context, sessionID string) bool {
	if c == nil || c.session == nil {
		return false
	}
	session, ok := c.session.(migrateSession)
	if !ok {
		c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "migrate", Content: "migrate: unavailable in this session", Final: true})
		return true
	}

	sources := migrate.DetectSources("", "")
	if len(sources) == 0 {
		c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "migrate",
			Content: "No migratable agent found: neither Claude Code (~/.claude) nor Codex (~/.codex) is installed on this machine.",
			Final:   true})
		return true
	}
	items := make([]SelectItem, 0, len(sources))
	for _, source := range sources {
		description := source.Description
		if source.Note != "" {
			description = source.Note
		}
		items = append(items, SelectItem{Label: source.Title, Description: description})
	}
	idx, okSel, selErr := c.selector.SelectRich("Migrate\nImport another agent's history, memories, skills, plans, MCP servers and input history", items, 0)
	if selErr != nil {
		c.renderer.PrintError(selErr)
		return true
	}
	if !okSel || idx < 0 || idx >= len(sources) {
		return true
	}
	chosen := sources[idx]
	if !chosen.Available {
		c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "migrate", Content: "migrate: " + chosen.Note, Final: true})
		return true
	}

	// The source directory: a non-required input prefilled with the default,
	// so the user always sees what a legal answer looks like. An invalid
	// answer shows the underlying error verbatim and asks again (§6.2).
	sourceRoot := ""
	for {
		label, def := migrate.SourceRootPrompt(chosen.Kind)
		answer, okIn, inErr := c.selector.Input(label, def)
		if inErr != nil {
			c.renderer.PrintError(inErr)
			return true
		}
		if !okIn {
			return true
		}
		resolved, err := migrate.ValidateSourceRoot(chosen.Kind, answer)
		if err == nil {
			sourceRoot = resolved
			break
		}
		c.renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "migrate", Content: err.Error()})
	}

	onlyProject := false
	scopeIdx, okScope, selErr := c.selector.SelectRich("Session scope\nWhich conversations should be imported?", []SelectItem{
		{Label: "All projects", Description: "every conversation the source holds"},
		{Label: "Current project only", Description: "conversations whose working directory belongs to this project"},
	}, 0)
	if selErr != nil {
		c.renderer.PrintError(selErr)
		return true
	}
	if !okScope {
		return true
	}
	onlyProject = scopeIdx == 1

	opts := session.BuildMigrateOptions(onlyProject, strings.TrimSpace(sessionID), sourceRoot)
	if opts == nil {
		c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "migrate", Content: "migrate: unavailable", Final: true})
		return true
	}
	opts.Source = chosen.Kind
	session.PreviewMigrationAsync(ctx, opts, func() {
		runOpts := session.BuildMigrateOptions(onlyProject, strings.TrimSpace(sessionID), sourceRoot)
		if runOpts == nil {
			return
		}
		runOpts.Source = chosen.Kind
		session.RunMigrationAsync(context.Background(), runOpts)
	})
	return true
}

// rewindSession is the slice of the active session /rewind needs: the shared
// rewind engine, built from the conversation store, the run store, and the
// tool state whose history dir holds the undo snapshots.
type rewindSession interface {
	RewindEngine() *turn.RewindStore
}

// handleRewind drives /rewind: pick a message (or undo the last rewind), pick
// how much to restore, review the files, then run the shared engine. It
// returns the anchor text so the caller can put it back in the composer.
func (c *commandController) handleRewind(ctx context.Context, sessionID string) (anchor string, applied bool) {
	if c == nil || c.session == nil || c.selector == nil {
		return "", false
	}
	rs, ok := c.session.(rewindSession)
	if !ok {
		c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "rewind", Content: "rewind: unavailable in this session", Final: true})
		return "", false
	}
	eng := rs.RewindEngine()
	if eng == nil {
		c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "rewind", Content: "rewind: unavailable", Final: true})
		return "", false
	}
	sid := strings.TrimSpace(sessionID)
	if eng.Runs != nil {
		busy, err := eng.Runs.SessionHasLiveRun(ctx, sid)
		if err != nil {
			c.renderer.PrintError(err)
			return "", false
		}
		if busy {
			c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "rewind", Content: "rewind: unavailable while the conversation is running", Final: true})
			return "", false
		}
	}
	_, hasPending, err := eng.Store.PendingRewind(ctx, sid)
	if err != nil {
		c.renderer.PrintError(err)
		return "", false
	}
	msgs, err := eng.Store.ListVisibleUserMessages(ctx, sid, 200)
	if err != nil {
		c.renderer.PrintError(err)
		return "", false
	}
	if len(msgs) == 0 && !hasPending {
		c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "rewind", Content: "rewind: no messages to rewind to", Final: true})
		return "", false
	}
	var (
		items  []SelectItem
		values []string
	)
	if hasPending {
		items = append(items, SelectItem{Label: "Undo the last rewind", Description: "put the files and the conversation back"})
		values = append(values, "")
	}
	for _, m := range msgs {
		items = append(items, SelectItem{
			Label:       truncate(strings.TrimSpace(firstLine(m.Content)), 72),
			Description: "message " + strconv.FormatInt(m.RowID, 10),
		})
		values = append(values, strconv.FormatInt(m.RowID, 10))
	}
	idx, okSel, selErr := c.selector.SelectRich(
		"Rewind\nRestore code and/or the conversation to before a message", items, 0)
	if selErr != nil {
		c.renderer.PrintError(selErr)
		return "", false
	}
	if !okSel || idx < 0 || idx >= len(items) {
		return "", false
	}
	if values[idx] == "" {
		if err := turn.UndoRewind(ctx, eng, sid); err != nil {
			c.renderer.PrintError(err)
			return "", false
		}
		c.replaySession(sid, nil)
		c.renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "rewind", Content: "Rewind undone.", Final: true})
		return "", true
	}
	anchorRow, parseErr := strconv.ParseInt(values[idx], 10, 64)
	if parseErr != nil {
		return "", false
	}

	modeIdx, okMode, modeErr := c.selector.SelectRich("Restore\nWhat should go back to before that message?", []SelectItem{
		{Label: "Code and conversation"},
		{Label: "Conversation only"},
		{Label: "Code only"},
	}, 0)
	if modeErr != nil {
		c.renderer.PrintError(modeErr)
		return "", false
	}
	if !okMode {
		return "", false
	}
	mode := []string{turn.RewindModeBoth, turn.RewindModeChat, turn.RewindModeCode}[modeIdx]

	preview, err := turn.PreviewRewind(ctx, eng, sid, anchorRow)
	if err != nil {
		c.renderer.PrintError(err)
		return "", false
	}
	var keepPaths []string
	if (mode == turn.RewindModeBoth || mode == turn.RewindModeCode) && len(preview.Files) > 0 {
		labels := make([]string, 0, len(preview.Files))
		for _, f := range preview.Files {
			label := fmt.Sprintf("%s   +%d −%d", f.Path, f.Added, f.Removed)
			if f.Created {
				label += "   (new file)"
			}
			if f.ChangedAfter {
				label += "   (changed since)"
			}
			labels = append(labels, label)
		}
		kept, okMulti, multiErr := c.selector.MultiSelect(
			"Files to restore\nUncheck a file to leave it as it is. Terminal command changes are not undone.", labels, labels)
		if multiErr != nil {
			c.renderer.PrintError(multiErr)
			return "", false
		}
		if !okMulti {
			return "", false
		}
		keptSet := make(map[string]bool, len(kept))
		for _, k := range kept {
			keptSet[k] = true
		}
		for i, label := range labels {
			if !keptSet[label] {
				keepPaths = append(keepPaths, preview.Files[i].Path)
			}
		}
	}

	res, err := turn.Rewind(ctx, eng, sid, anchorRow, mode, keepPaths)
	if err != nil {
		c.renderer.PrintError(err)
		return "", false
	}
	quoted := truncate(strings.TrimSpace(firstLine(res.AnchorText)), 60)
	var summary string
	if mode == turn.RewindModeCode {
		summary = fmt.Sprintf("Rewound %d file(s) to before “%s” · /rewind to undo", len(res.Restored), quoted)
	} else {
		summary = fmt.Sprintf("Rewound to before “%s” · restored %d file(s) · /rewind to undo", quoted, len(res.Restored))
	}
	c.replaySession(sid, nil)
	c.renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "rewind", Content: summary, Final: true})
	return res.AnchorText, true
}

// sessionFastAvailable reports whether the active session supports the /fast// command. Gated to Anthropic Opus (the only family with the priority
// service tier surfaced by Forebrain Harness's Anthropic agent LLM). Non-Opus models
// hide /fast from the tui picker.
func sessionFastAvailable(session Session) bool {
	if session == nil {
		return false
	}
	return llm.IsOpusLabel(session.CurrentModelOption())
}

type StreamCommandAction string

const (
	StreamActionTerminalControl StreamCommandAction = "terminal_control"
	StreamActionNativeUI        StreamCommandAction = "native_ui"
	StreamActionRuntimeDispatch StreamCommandAction = "runtime_dispatch"
)

type ModelPickerEntry struct {
	ID            string
	Name          string
	Provider      string
	APIModel      string
	ContextWindow int64
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// SlashOverlay is the non-modal slash-command picker state that lives on
// ComposerState. It activates when the composer DraftText is exactly "/" and
// closes (or locks into hint mode) as the user types, all without taking the
// raw input fd away from the composer.
//
// Lifecycle:
//
//	(no overlay) --typed "/"--> Active (filtering by FilterText)
//	Active --typed " " after exact/single-prefix match--> HintCommand set
//	HintCommand set --backspace removes space--> Active again
//	any state --typed non-"/" first char OR Escape OR Enter--> nil overlay
//
// All state lives in this struct; mutation happens single-threaded inside
// run.go's event loop, so no locking is required.
type SlashOverlay struct {
	// Active means the picker is visible above the composer. True while the
	// composer holds "/" or "/prefix" (no space yet).
	Active bool

	// FilterText is the lowercase substring after "/" used to narrow Visible.
	FilterText string

	// SelectedIdx is the cursor into Visible.
	SelectedIdx int

	// Visible is the filtered, registry-ordered command list driving the
	// dropdown rows.
	Visible []turn.Command

	// HintCommand is non-nil after the user has typed a space following a
	// recognized command. The dim ArgumentHint renders inline in the composer
	// while HintCommand is set and the text has no args yet.
	HintCommand *turn.Command

	// SubagentView is true when the composer belongs to a subagent's own view,
	// so the list drops the commands hidden there (D4). It is copied from the
	// composer when the overlay opens.
	SubagentView bool

	// Inline is true when the picker was opened on a "/" token that is not the
	// start of the message. Only skills can be referenced there — a built-in
	// command means something only at the start — so the list is narrowed to
	// skills and, on accept, only the token is replaced, not the whole draft.
	Inline bool

	// TokenStart is the rune offset of the "/" the inline picker is editing.
	// Unused when Inline is false (the leading command replaces the draft).
	TokenStart int
}

// newSlashOverlay returns a fresh overlay seeded with the full visible
// command list for the current session. Callers should immediately call
// rebuildFiltered if they want to apply an initial FilterText.
func newSlashOverlay() *SlashOverlay {
	return &SlashOverlay{
		Active:      true,
		Visible:     nil,
		SelectedIdx: 0,
	}
}

// rebuildFiltered narrows Visible to the commands whose name contains
// FilterText (case-insensitive). It also threads FastAvailable into the
// registry's DiscoveryOptions so /fast respects session capability (per
// design §DiscoveryOptions wiring).
func (o *SlashOverlay) rebuildFiltered(session Session) {
	if o == nil {
		return
	}
	opts := turn.DiscoveryOptions{
		FastAvailable: sessionFastAvailable(session),
		// In a subagent's own view the commands that would change the
		// conversation are hidden (D4).
		SubagentView: o.SubagentView,
	}
	// The engine's filter and order, the same the web menu lists: exact,
	// prefix and substring matches, the built-in commands and the skills as
	// two groups with the best match's group first.
	o.Visible = turn.FilterWithOptions(turn.SurfaceTUI, o.FilterText, opts)
	if o.Inline {
		// A mid-sentence "/" only ever names a skill, so the built-in
		// commands are not offered there.
		narrowed := make([]turn.Command, 0, len(o.Visible))
		for _, cmd := range o.Visible {
			if cmd.Category == turn.SkillCategory {
				narrowed = append(narrowed, cmd)
			}
		}
		o.Visible = narrowed
	}
	// Reset the cursor to the first matching command so the selection
	// highlight is always on the first option after a filter change. Without
	// this the cursor stays at its old index and is merely clamped, leaving
	// the highlight on a non-first option.
	o.SelectedIdx = 0
	if o.SelectedIdx >= len(o.Visible) {
		o.SelectedIdx = len(o.Visible) - 1
	}
	if o.SelectedIdx < 0 {
		o.SelectedIdx = 0
	}
}

// The two groups the slash menu lists commands in.
const (
	slashGroupCommands = "Commands"
	slashGroupSkills   = "Skills"
)

func slashGroupOf(cmd turn.Command) string {
	if cmd.Category == turn.SkillCategory {
		return slashGroupSkills
	}
	return slashGroupCommands
}

// lookupExactOrSinglePrefix returns the locked command for a given lowercased
// name when the overlay's Visible list contains either an exact match OR
// exactly one prefix match. Returns nil when the match is ambiguous (multiple
// prefix candidates) or absent — the picker stays open in that case.
func (o *SlashOverlay) lookupExactOrSinglePrefix(name string) *turn.Command {
	if o == nil || name == "" {
		return nil
	}
	needle := strings.ToLower(name)
	// Exact match wins.
	for i := range o.Visible {
		if strings.ToLower(o.Visible[i].Name) == needle {
			return &o.Visible[i]
		}
	}
	// Single prefix candidate.
	var match *turn.Command
	for i := range o.Visible {
		if strings.HasPrefix(strings.ToLower(o.Visible[i].Name), needle) {
			if match != nil {
				return nil // ambiguous
			}
			match = &o.Visible[i]
		}
	}
	return match
}

// leadingSlashName reads a draft the composer shows as a slash command — its
// first visible character is "/" — against the caret at rune offset cursor.
// While the caret is on the command's name, name is the part of it before the
// caret and editing is true: the user is still typing it, and the picker lists
// what it may become. Once the caret has moved past the first word, name is
// that word and the rest of the draft is its arguments. restAt is the rune
// offset in draft of the text after the name being typed, which a completed
// name is spliced in front of.
//
// A command is often typed in front of a message already in the composer, so
// what follows the caret is never part of the name being typed: "/fr" typed
// in front of "commit 1527" is the name "fr", not "frcommit", and the message
// is not the arguments of a command the user has not finished naming. A caret
// before the "/" is not on the name at all; the draft is then read as written,
// as if the caret were at its end.
//
// The draft is read as the composer draws it, so the leading newlines the card
// hides cannot make this disagree with the screen.
func leadingSlashName(draft string, cursor int) (name string, editing bool, restAt int, ok bool) {
	display := []rune(composerDisplayText(draft))
	if len(display) == 0 || display[0] != '/' {
		return "", false, 0, false
	}
	runes := []rune(draft)
	restAt = composerCursorClampRunes(runes, cursor)
	end := len(display)
	for i := 1; i < len(display); i++ {
		if unicode.IsSpace(display[i]) {
			end = i
			break
		}
	}
	caret := len([]rune(composerDisplayText(string(runes[:restAt]))))
	if caret == 0 {
		caret, restAt = len(display), len(runes)
	}
	if caret <= end {
		return string(display[1:caret]), true, restAt, true
	}
	return string(display[1:end]), false, restAt, true
}

// UpdateFromDraft transitions the overlay state in response to a new
// composer.DraftText value at the given cursor. Two modes:
//
//   - A leading "/" (the whole composer is a slash command), read by
//     leadingSlashName: while the caret is on the name, the name typed so far
//     narrows Visible; once it has moved on to the arguments, the command is
//     locked via lookupExactOrSinglePrefix — Active=false with HintCommand on
//     a hit, still Active with a "no match" footer otherwise.
//   - A "/" anywhere else (Inline) is a skill reference: the token under the
//     cursor is recomputed and Visible narrows to skills only.
//
// Returns true if the overlay should remain on ComposerState; false when the
// caller should set composer.SlashOverlay = nil.
func (o *SlashOverlay) UpdateFromDraft(draft string, cursor int, session Session) bool {
	if o == nil {
		return false
	}
	if name, editing, _, ok := leadingSlashName(draft, cursor); ok {
		o.Inline = false
		o.TokenStart = 0
		if editing {
			// "/foo" — filtering mode.
			o.Active = true
			o.HintCommand = nil
			o.FilterText = strings.ToLower(name)
			o.rebuildFiltered(session)
			return true
		}
		// "/foo " or "/foo bar" — try to lock the command.
		cmdName := strings.ToLower(name)
		// Visible may have been narrowed to a stale filter (or never populated
		// when UpdateFromDraft is called on a fresh overlay). Rebuild against the
		// full registry before locking so exact-name resolution always works.
		o.FilterText = ""
		o.rebuildFiltered(session)
		if cmd := o.lookupExactOrSinglePrefix(cmdName); cmd != nil {
			o.Active = false
			o.HintCommand = cmd
			o.FilterText = cmdName
			return true
		}
		// No exact or single-prefix match — keep picker open with footer "no match".
		o.Active = true
		o.HintCommand = nil
		o.FilterText = cmdName
		o.rebuildFiltered(session)
		return true
	}
	start, query, _, ok := turn.SlashTokenAtCursor(draft, cursor)
	if !ok {
		return false
	}
	o.Inline = true
	o.TokenStart = start
	o.Active = true
	o.HintCommand = nil
	o.FilterText = strings.ToLower(query)
	o.rebuildFiltered(session)
	return true
}

// AcceptSlashSelection completes the currently selected slash command,
// returning the new draft and the cursor offset just past what was inserted.
//
// The completed "/<command> " replaces only what was typed of it: a leading
// command everything up to the caret (leadingSlashName's restAt), an inline
// skill reference its token [TokenStart, Cursor). turn.AcceptSlash, which the
// web composer shares, keeps the rest of the message and puts the caret on it,
// so UpdateFromDraft locks HintCommand and shows the argument hint. ok is
// false when nothing is selected.
func (c *ComposerState) AcceptSlashSelection() (draft string, cursor int, ok bool) {
	if c == nil || c.SlashOverlay == nil || !c.SlashOverlay.Active || len(c.SlashOverlay.Visible) == 0 {
		return "", 0, false
	}
	o := c.SlashOverlay
	idx := o.SelectedIdx
	if idx < 0 || idx >= len(o.Visible) {
		idx = 0
	}
	// A leading command also drops the newlines the card hides in front of it.
	start, end := 0, -1
	if o.Inline {
		start, end = o.TokenStart, c.Cursor
	} else if _, _, restAt, ok := leadingSlashName(c.DraftText, c.Cursor); ok {
		end = restAt
	}
	draft, cursor = turn.AcceptSlash(c.DraftText, start, end, o.Visible[idx].Name)
	return draft, cursor, true
}

// SlashPickerSubmitLine is the line Enter submits while the leading command
// picker is open: the highlighted command in place of the name being typed,
// with the message after it kept as its arguments — AcceptSlashSelection's
// draft. A line that is not the draft the picker read keeps everything after
// its first word instead.
func (c *ComposerState) SlashPickerSubmitLine(line string) string {
	if line == c.DraftText {
		if draft, _, ok := c.AcceptSlashSelection(); ok {
			return strings.TrimRight(draft, " ")
		}
	}
	o := c.SlashOverlay
	if o == nil || len(o.Visible) == 0 {
		return line
	}
	idx := o.SelectedIdx
	if idx < 0 || idx >= len(o.Visible) {
		idx = 0
	}
	command := "/" + o.Visible[idx].Name
	trimmed := strings.TrimSpace(line)
	if i := strings.IndexFunc(trimmed, unicode.IsSpace); i >= 0 {
		return command + trimmed[i:]
	}
	return command
}

// showArgumentHint reports whether the dim ArgumentHint should be rendered
// inline in the composer for the given text + locked HintCommand. The hint
// appears for "/cmd" or "/cmd " (trailing space) and hides as soon as any
// non-space character follows.
func showArgumentHint(text string, cmd *turn.Command) bool {
	if cmd == nil {
		return false
	}
	if strings.TrimSpace(cmd.ArgumentHint) == "" {
		return false
	}
	text = composerDisplayText(text)
	if !strings.HasPrefix(text, "/") {
		return false
	}
	trimmed := strings.TrimRight(text, " ")
	return trimmed == "/"+cmd.Name
}

// MentionCandidate and MentionAcceptance are the engine's types. The picker
// semantics — what a query matches, what an accepted candidate does to the
// draft — live in pkg/turn so the terminal composer and the web composer share
// one implementation; this file is only the terminal's view of them.
type (
	MentionCandidate  = turn.Candidate
	MentionAcceptance = turn.Acceptance
)

// mentionRootDir is the directory this TUI session picks from. It is session
// state, not engine state: the engine takes a root per call so a server can
// serve many workspaces at once.
var mentionRootDir atomic.Value // string

func setMentionRoot(root string) { mentionRootDir.Store(strings.TrimSpace(root)) }

func mentionRoot() string {
	if v, ok := mentionRootDir.Load().(string); ok {
		return v
	}
	return ""
}

// MentionOverlay is the non-modal @ file-mention picker state on
// ComposerState. It mirrors SlashOverlay's lifecycle but tracks a token
// anywhere in the draft (the slash overlay only owns drafts starting "/").
// All mutation happens single-threaded in run.go's event loop.
type MentionOverlay struct {
	Active      bool
	Query       string
	TokenStart  int
	TokenEnd    int
	SelectedIdx int
	Visible     []MentionCandidate
}

func (c *ComposerState) updateMentionOverlay(draft string) {
	// A "@" token under the cursor is a file mention and wins over the command
	// picker: "see /x @pk" (or "/plan see @pk") must open the file picker, not
	// be swallowed by an open slash overlay.
	start, query, hasMention := turn.TokenAtCursor(draft, c.Cursor)
	if hasMention {
		c.SlashOverlay = nil
	} else if c.SlashOverlay != nil {
		c.MentionOverlay = nil
		return
	}
	if c.MentionDismissedDraft != "" && draft != c.MentionDismissedDraft {
		c.MentionDismissedDraft = ""
	}
	if !hasMention || c.MentionDismissedDraft == draft {
		c.MentionOverlay = nil
		return
	}
	if c.MentionOverlay == nil {
		c.MentionOverlay = &MentionOverlay{Active: true, Query: "\x00unset"}
	}
	o := c.MentionOverlay
	o.Active = true
	o.TokenStart = start
	o.TokenEnd = c.Cursor
	if o.Query != query {
		o.Query = query
		o.Visible = turn.Search(mentionRoot(), query)
		o.SelectedIdx = 0
	}
}

func (c *ComposerState) AcceptMentionSelection() (MentionAcceptance, bool) {
	if c == nil || c.MentionOverlay == nil || !c.MentionOverlay.Active || len(c.MentionOverlay.Visible) == 0 {
		return MentionAcceptance{}, false
	}
	o := c.MentionOverlay
	idx := o.SelectedIdx
	if idx < 0 || idx >= len(o.Visible) {
		idx = 0
	}
	acc, ok := turn.Accept(c.DraftText, o.TokenStart, o.TokenEnd, o.Visible[idx], mentionRoot())
	if !ok {
		return MentionAcceptance{}, false
	}
	if !acc.KeepOpen {
		c.MentionOverlay = nil
	}
	return acc, true
}

// overlayRowsFromMention lists the inline @ menu's candidates; the renderer
// lays the rows out at the terminal width.
func overlayRowsFromMention(o *MentionOverlay) []OverlayRow {
	if o == nil || len(o.Visible) == 0 {
		return []OverlayRow{{Text: "(no matching files)"}}
	}
	rows := make([]OverlayRow, 0, len(o.Visible))
	for i, cand := range o.Visible {
		path := cand.Path
		if cand.IsDir && !strings.HasSuffix(path, "/") {
			path += "/"
		}
		rows = append(rows, OverlayRow{Lead: "+ ", Text: path, Selected: i == o.SelectedIdx})
	}
	return rows
}

// MoveSelection moves the selected row by delta, wrapping at both ends.
func (o *MentionOverlay) MoveSelection(delta int) {
	if o == nil || len(o.Visible) == 0 {
		return
	}
	o.SelectedIdx += delta
	if o.SelectedIdx < 0 {
		o.SelectedIdx = len(o.Visible) - 1
	}
	if o.SelectedIdx >= len(o.Visible) {
		o.SelectedIdx = 0
	}
}
