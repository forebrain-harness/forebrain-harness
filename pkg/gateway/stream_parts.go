package gateway

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// streamPartMaxBytes caps how much of one running tool call's output the
// in-flight registry keeps. A call that streams for a long time would otherwise
// hold its whole output in memory for the run's duration, when all a page that
// arrives mid-turn needs is the tail it missed; the frontend shows what is
// kept.
const streamPartMaxBytes = 256 << 10

// streamParts is the gateway's in-memory record of what the runs this process
// is executing are producing right now: each run's unfinished answer and
// reasoning block and each running tool call's output so far. Stream deltas are
// pushed and never stored (event.IsPushOnlyStreamEvent) — a run's words live in
// its transcript, written as each block ends, and a tool's output on its
// completed step — so a page that arrives mid-turn, by a reconnect or a fresh
// binding, is shown what it missed from here rather than from the log.
//
// It serves only this process's runs. A run another surface owns (a terminal's
// own turn) is not here, and a fresh binding sees only its completed blocks,
// which is the deliberate "no cross-surface live push" rule.
type streamParts struct {
	mu   sync.Mutex
	runs map[string]*streamPartRun
}

type streamPartRun struct {
	runID     string
	sessionID string
	// partial is the primary agent's in-flight block when the run streams
	// through withAnswerStream. It knows its own block boundaries —
	// ResponseCompleted clears the finished answer — so its current content is
	// read from it directly. A child run has none and accumulates from events.
	partial   *turn.StreamPartial
	text      map[string]*streamPartText // child runs: agentID -> unfinished block
	tools     map[string]*streamPartTool // agentID + "\x00" + stepID -> tool
	toolOrder []string
}

type streamPartText struct {
	content   string
	reasoning string
}

type streamPartTool struct {
	agentID string
	stepID  string
	text    string
}

func newStreamParts() *streamParts {
	return &streamParts{runs: map[string]*streamPartRun{}}
}

// registerPrimary records the primary agent's in-flight block for a run. The
// entry is dropped when the run ends (observe sees its terminal event).
func (s *streamParts) registerPrimary(runID, sessionID string, partial *turn.StreamPartial) {
	if s == nil {
		return
	}
	runID = strings.TrimSpace(runID)
	if runID == "" || partial == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[runID]
	if run == nil {
		run = &streamPartRun{runID: runID, text: map[string]*streamPartText{}, tools: map[string]*streamPartTool{}}
		s.runs[runID] = run
	}
	run.partial = partial
	if sid := strings.TrimSpace(sessionID); sid != "" {
		run.sessionID = sid
	}
}

// observe folds one run event into the registry: the pieces it is still
// producing are accumulated, and the run's entry is dropped when it ends. It is
// called for every event the bus carries, push-only or not, so a child run's
// running tool is forgotten the moment its completion arrives.
func (s *streamParts) observe(evt event.RunEvent) {
	if s == nil {
		return
	}
	runID := strings.TrimSpace(evt.RunID)
	if runID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch evt.Type {
	case event.RunEventTurnCompleted, event.RunEventTurnError,
		event.RunEventTurnCancelled, event.RunEventSubagentEnded:
		delete(s.runs, runID)
		return
	case event.RunEventAssistantDelta, event.RunEventReasoningDelta,
		event.RunEventReasoningDone, event.RunEventToolOutputDelta,
		event.RunEventToolStarted, event.RunEventToolCompleted,
		event.RunEventTransportRetry:
	default:
		return
	}
	run := s.runs[runID]
	if run == nil {
		run = &streamPartRun{runID: runID, text: map[string]*streamPartText{}, tools: map[string]*streamPartTool{}}
		s.runs[runID] = run
	}
	if sid := strings.TrimSpace(evt.SessionID); sid != "" {
		run.sessionID = sid
	}
	switch evt.Type {
	case event.RunEventAssistantDelta:
		var p event.AssistantDeltaPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return
		}
		if run.ownsPrimaryText(p.AgentID) {
			return
		}
		run.textFor(p.AgentID).content += p.Text
	case event.RunEventReasoningDelta:
		var p event.ReasoningDeltaPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return
		}
		if run.ownsPrimaryText(p.AgentID) {
			return
		}
		run.textFor(p.AgentID).reasoning += p.Text
	case event.RunEventReasoningDone:
		var p event.ReasoningDonePayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return
		}
		if run.ownsPrimaryText(p.AgentID) {
			return
		}
		run.textFor(p.AgentID).reasoning = ""
	case event.RunEventTransportRetry:
		var p event.TransportRetryPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return
		}
		if run.ownsPrimaryText(p.AgentID) {
			return
		}
		// Every notice clears the agent's in-flight text, Cleared included:
		// the catch-up Cleared at a finish or an exhaustion is the only
		// announcement the final dead attempt ever gets.
		text := run.textFor(p.AgentID)
		text.content = ""
		text.reasoning = ""
	case event.RunEventToolStarted:
		var p event.ToolCallStartedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return
		}
		agentID := strings.TrimSpace(p.ToolMeta.AgentID)
		// A call ends the response that preceded it: the text so far is its own
		// block, already written to the transcript, so the in-flight buffer
		// starts over rather than carrying a finished block into the next one.
		if !run.ownsPrimaryText(agentID) {
			run.textFor(agentID).content = ""
		}
	case event.RunEventToolOutputDelta:
		var p event.ToolCallOutputDeltaPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return
		}
		run.appendToolOutput(strings.TrimSpace(p.StepID), strings.TrimSpace(p.AgentID), p.Text)
	case event.RunEventToolCompleted:
		var p event.ToolCallCompletedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return
		}
		run.dropTool(strings.TrimSpace(p.StepID), strings.TrimSpace(p.ToolMeta.AgentID))
	}
}

// ownsPrimaryText reports whether the primary agent's text is read from the
// registered StreamPartial — when it is, accumulating the same text from events
// would double it.
func (r *streamPartRun) ownsPrimaryText(agentID string) bool {
	return r.partial != nil && strings.TrimSpace(agentID) == ""
}

func (r *streamPartRun) textFor(agentID string) *streamPartText {
	agentID = strings.TrimSpace(agentID)
	buf := r.text[agentID]
	if buf == nil {
		buf = &streamPartText{}
		r.text[agentID] = buf
	}
	return buf
}

func (r *streamPartRun) appendToolOutput(stepID, agentID, text string) {
	if stepID == "" || text == "" {
		return
	}
	key := agentID + "\x00" + stepID
	tool := r.tools[key]
	if tool == nil {
		tool = &streamPartTool{agentID: agentID, stepID: stepID}
		r.tools[key] = tool
		r.toolOrder = append(r.toolOrder, key)
	}
	tool.text += text
	if len(tool.text) > streamPartMaxBytes {
		tool.text = tool.text[len(tool.text)-streamPartMaxBytes:]
	}
}

func (r *streamPartRun) dropTool(stepID, agentID string) {
	if stepID == "" {
		return
	}
	key := strings.TrimSpace(agentID) + "\x00" + stepID
	if _, ok := r.tools[key]; !ok {
		return
	}
	delete(r.tools, key)
	for i, k := range r.toolOrder {
		if k == key {
			r.toolOrder = append(r.toolOrder[:i], r.toolOrder[i+1:]...)
			break
		}
	}
}

// streamPartialSnapshot is one view's in-flight pieces: the run's unfinished
// answer and reasoning block, and each running tool call's output so far.
type streamPartialSnapshot struct {
	RunID     string                `json:"run_id,omitempty"`
	AgentID   string                `json:"agent_id,omitempty"`
	Content   string                `json:"content,omitempty"`
	Reasoning string                `json:"reasoning,omitempty"`
	Tools     []streamPartialOutput `json:"tool_outputs,omitempty"`
}

type streamPartialOutput struct {
	StepID  string `json:"step_id"`
	AgentID string `json:"agent_id,omitempty"`
	Text    string `json:"text"`
}

// present reports whether a snapshot says anything a page is missing: an empty
// one is not sent.
func (s streamPartialSnapshot) present() bool {
	return s.Content != "" || s.Reasoning != "" || len(s.Tools) > 0
}

// snapshotRun returns the in-flight pieces of one run, for one agent. ok is
// false when the run is not registered on this process.
func (s *streamParts) snapshotRun(runID, agentID string) (streamPartialSnapshot, bool) {
	if s == nil {
		return streamPartialSnapshot{}, false
	}
	runID = strings.TrimSpace(runID)
	agentID = strings.TrimSpace(agentID)
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[runID]
	if run == nil {
		return streamPartialSnapshot{}, false
	}
	return run.snapshot(agentID), true
}

// sessionSnapshots returns every in-flight stream of one conversation: the
// primary agent's run and each child run it dispatched. A binding pushes one
// stream_partial per entry.
func (s *streamParts) sessionSnapshots(sessionID string) []streamPartialSnapshot {
	if s == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]streamPartialSnapshot, 0, len(s.runs))
	for _, run := range s.runs {
		if run.sessionID != sessionID {
			continue
		}
		agents := run.agents()
		for _, agentID := range agents {
			if snap := run.snapshot(agentID); snap.present() {
				out = append(out, snap)
			}
		}
	}
	return out
}

// agents lists the views with something in flight in this run: the primary
// agent (""), and every child agent with buffered text, running tool or both.
func (r *streamPartRun) agents() []string {
	seen := map[string]bool{}
	var out []string
	add := func(agentID string) {
		agentID = strings.TrimSpace(agentID)
		if seen[agentID] {
			return
		}
		seen[agentID] = true
		out = append(out, agentID)
	}
	if r.partial != nil {
		add("")
	}
	for _, tool := range r.tools {
		add(tool.agentID)
	}
	for agentID, buf := range r.text {
		if buf.content != "" || buf.reasoning != "" {
			add(agentID)
		}
	}
	return out
}

func (r *streamPartRun) snapshot(agentID string) streamPartialSnapshot {
	agentID = strings.TrimSpace(agentID)
	snap := streamPartialSnapshot{RunID: r.runID, AgentID: agentID}
	if agentID == "" && r.partial != nil {
		snap.Content = r.partial.Content()
		snap.Reasoning = r.partial.Reasoning()
	} else if buf := r.text[agentID]; buf != nil {
		snap.Content = buf.content
		snap.Reasoning = buf.reasoning
	}
	for _, key := range r.toolOrder {
		tool := r.tools[key]
		if tool == nil || tool.agentID != agentID {
			continue
		}
		snap.Tools = append(snap.Tools, streamPartialOutput{StepID: tool.stepID, AgentID: tool.agentID, Text: tool.text})
	}
	return snap
}
