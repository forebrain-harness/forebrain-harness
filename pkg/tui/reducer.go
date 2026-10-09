// The UI state reducer and viewport.
package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/mattn/go-runewidth"
)

type FrameKind string

// intermediateToolName is the tool whose results (saved investigation notes)
// are exempt from line-based output truncation in the compact renderer.
const intermediateToolName = "intermediate_tool"

const (
	FrameAssistant     FrameKind = "assistant"
	FrameThinking      FrameKind = "thinking"
	FrameUser          FrameKind = "user"
	FrameTool          FrameKind = "tool"
	FrameSystem        FrameKind = "system"
	FrameError         FrameKind = "error"
	FramePlan          FrameKind = "plan"
	FrameStatus        FrameKind = "status"
	FrameMemoryCompact FrameKind = "memory_compact"
	// FrameSkillInstall is the live card a background skill install owns: one
	// block that reports the phase while the package is fetched and becomes
	// the install's result when it finishes.
	FrameSkillInstall FrameKind = "skill_install"
	// FrameGoal is a /goal's own line in the transcript: the objective it
	// opened with, each continuation round, and how it ended.
	FrameGoal FrameKind = "goal"
	// FrameRaw carries already-styled, pre-wrapped lines rendered verbatim. Used
	// to seed status text such as the OAuth URL as retained viewport blocks so
	// it lives inside the alt-screen transcript instead of the hidden normal
	// buffer.
	FrameRaw    FrameKind = "raw"
	FrameFanout FrameKind = "fanout"
	// FrameBannerInfo is the startup banner card: Title carries the version
	// and Content the working directory. Its render branch lays the card out
	// for the current terminal width on every repaint, so a resize reflows
	// it; the directory is wrapped, never truncated.
	FrameBannerInfo FrameKind = "banner_info"
)

type Frame struct {
	turn    *foregroundTurn // Exact retained submission identity, never serialized.
	Kind    FrameKind
	Title   string
	Content string
	// MaxDisplayLines caps the rendered visual lines for this frame. When the
	// content exceeds the cap, the final line is truncated with an ellipsis.
	// Zero leaves rendering unrestricted.
	MaxDisplayLines int
	// InsertBeforeLastTool is a tui-only layout hint for an approval-exchange
	// frame — a confirmation status, or the plan-review card the confirmation
	// asked for. Such a frame lands above the parked tool block it belongs to,
	// not at the transcript tail. Ordinary status and fanout frames must leave
	// it false so they retain their producer order.
	InsertBeforeLastTool bool
	StepID               string
	RunID                string
	Final                bool
	// AgentID names the subagent this frame belongs to, and is empty for the
	// conversation's own frames. It is the routing key: a frame that names an
	// agent is retained in that agent's view, never in the conversation.
	AgentID string
	// SubagentLifecycleCard marks the one class of frame that names an agent
	// yet belongs to the conversation: its own account of something done with
	// that agent — a goal line opening the view of the check that decided it.
	// The subagent card itself needs no marker: it is a fanout frame, whose
	// owner is the dispatching agent and whose task rows carry the agents.
	// Everything else a subagent produces — including the confirmation of an
	// approval answered on its behalf — is retained in the subagent's own view.
	SubagentLifecycleCard bool
	// FanoutLineAgents maps each line of a fanout frame's Content to the roster
	// key of the subagent that line describes ("" when a line belongs to none).
	// A fanout block shows many subagents at once, so ownership cannot live on
	// the frame's single AgentID; this is what lets a click on one task row open
	// that task's subagent view.
	FanoutLineAgents []string
	// FanoutLineClocks aligns with the Content lines the same way
	// FanoutLineAgents does, and carries a task row's execution clock: the
	// running clock while the task runs (rendered from "now" at paint time),
	// the final elapsed once it ended. Only task title lines carry a clock.
	FanoutLineClocks []fanoutLineClock
	// FanoutQuery marks a fanout frame as a query card (subagent_status, wait,
	// close, list): a snapshot of the call that must settle when its turn or
	// its agent ends, unlike a dispatch card whose agents outlive the call.
	FanoutQuery bool
	// FanoutCallError is the dispatch or query call's own failure text, drawn
	// as the first line under the card header: the one place it belongs,
	// instead of a copy on every task row.
	FanoutCallError string
	Summary         string // Phase 1 redesign: pre-rendered one-line summary.
	ToolMeta        tool.ToolMeta
	// FilePath is the file path extracted from tool input (e.g. read_file file_path).
	// Carried separately from ToolMeta.Input so the renderer always has a reliable
	// path for syntax highlighting even when ToolMeta.Input is nil or malformed.
	FilePath string
	// Duration is executor supplied for tool/fanout frames and reducer supplied
	// only for non-tool features such as finalized thinking blocks. A skill
	// install card carries the time the install has been running so far.
	Duration time.Duration
	// Progress is a running compaction's percentage, 0 to 99.
	Progress int
	// StreamingOutput marks Content as accumulated live shell stdout/stderr.
	StreamingOutput bool
	// RetainAsHistory keeps a completed attempt immutable when the logical tool
	// call continues with an approval-gated retry using the same StepID.
	RetainAsHistory bool
}

// Counters captures the running tally for the active session.
type Counters struct {
	Tools        int
	InputTokens  int
	OutputTokens int
	Files        int
	Agents       int
	Duration     time.Duration
	PlanDone     int
	PlanTotal    int
	PlanActive   string
	// GoalRound is the round a running /goal is in, 0 when none is running.
	GoalRound int
	// GoalChecking is set while the goal's check reads the workspace.
	GoalChecking bool
}

type EventResult struct {
	Frames                []Frame
	WorkedStatus          string
	SwitchSessionID       string
	RequestPicker         *turn.Picker
	RequestSessionSelect  bool
	RequestPermissionMgmt bool
	RequestSkillSelect    bool
	QuitRequested         bool
	ComposerTokenStats    *ComposerTokenStats
	// ComposerTokenStatsAgent names the view the budget belongs to ("" for the
	// conversation's own, a roster key for a subagent's). The caller routes
	// the stats to that view's footer.
	ComposerTokenStatsAgent string
}

// composerTokenStatsFromBudget is the footer's reading of a budget: how much
// of the auto-compact budget is left, of which window.
func composerTokenStatsFromBudget(m TokenBudgetUpdatedMsg) ComposerTokenStats {
	return ComposerTokenStats{
		Active:        m.TokenUsage > 0 || m.ContextWindow > 0,
		PercentLeft:   m.PercentLeft,
		ContextWindow: m.ContextWindow,
	}
}

func compactionStepID(compactionID string) string {
	return "compact-" + strings.TrimSpace(compactionID)
}

// runningCompactionFrame is a running compaction's card: its phase and the
// size of the history being compacted, and how far it has come.
func (r *Reducer) runningCompactionFrame(compactionID string, card *compactionCard) Frame {
	detail := []string{compactPhaseLabel(card.phase)}
	if card.tokensBefore > 0 {
		detail = append(detail, assembly.FormatTokenCount(card.tokensBefore)+" tokens")
	}
	return Frame{
		Kind:     FrameMemoryCompact,
		StepID:   compactionStepID(compactionID),
		AgentID:  card.agentID,
		RunID:    r.activeRunID,
		Title:    compactTitleRunning,
		Summary:  strings.Join(detail, " · "),
		Progress: max(card.percent, 0),
	}
}

func compactPhaseLabel(phase string) string {
	switch phase {
	case event.CompactPhaseReading:
		return "reading history"
	case event.CompactPhaseSummarizing:
		return "summarizing"
	case event.CompactPhaseSaving:
		return "saving checkpoint"
	default:
		return "starting"
	}
}

// agentBuffer holds the per-agent streaming state for buffering
// assistant/reasoning deltas and deduplicating tool running/completed frames.
type agentBuffer struct {
	agentID            string
	assistant          strings.Builder
	reasoning          strings.Builder
	reasoningStartedAt time.Time
	runningStepIDs     map[string]bool
	sawAssistant       bool
}

// maxFanoutRecentTools caps the per-task ring buffer of recent tool labels
// shown in the fanout third layer.
const maxFanoutRecentTools = 1

// fanoutToolEntry is one recent tool invocation for a fanout task
// live third-layer display.
type fanoutToolEntry struct {
	StepID string
	Label  string
}

// fanoutLineClock is one task row's execution clock, aligned with the fanout
// content lines. Start is when the execution bound to the row began (its
// subagent_spawned), End when it stopped (its subagent_ended). A zero End with
// a non-zero Start is a clock still running; both zero is a row that never
// started (waiting or skipped) and shows no time at all.
type fanoutLineClock struct {
	Start time.Time
	End   time.Time
}

// FanoutTaskState is one task of a subagent card: a dispatched execution, the
// execution a query looked at, or the single task of a plan review. Dispatch
// tasks bind to their agent through the lifecycle events; query tasks carry
// the snapshot the call's facts reported.
type FanoutTaskState struct {
	Title        string // display title (SubagentTaskTitle's answer, one line by construction)
	SubagentType string // e.g. "explore"; "fork" when the dispatch named none
	AgentID      string // roster key of the execution this row follows, "" while unbound
	Status       string // "waiting" | "running" | "done" | "failed" | "cancelled" | "skipped" | ""
	Error        string // error or skip reason, shown below the task title
	ExecutionID  string // the execution this row's clock follows
	StartedAt    time.Time
	EndedAt      time.Time
	// TimedOut and StopRequested are a query row's phrases: the wait ended
	// while the task was still running, and the close asked it to stop.
	TimedOut      bool
	StopRequested bool
	ToolTotal     int               // total subagent tool calls (deduped by StepID)
	RecentTools   []fanoutToolEntry // recent tool labels (ring buffer, cap maxFanoutRecentTools)
}

// fanoutState is one subagent card: the subagent_* call (or the review
// request, or an execution dispatched with no call at all) and the tasks it is
// about. The names stay "fanout" — the card kind is FrameFanout — but every
// subagent_* tool draws through here.
type fanoutState struct {
	StepID string
	// Verb is what the call does to its tasks: "run" (subagent_run,
	// subagent_fanout and dispatches with no call), "send", "continue",
	// "status", "wait", "close", "list", or "review" (the plan review card).
	Verb string
	// Query marks the snapshot verbs (status/wait/close/list): their card is
	// the call's answer and settles when the call, its turn or its agent ends,
	// unlike a dispatch card whose agents outlive the call.
	Query   bool
	Tasks   []FanoutTaskState
	Running bool
	// Duration is the call's own execution time.
	Duration time.Duration
	RunID    string
	// OwnerAgentID is the subagent whose view owns this card: the agent that
	// made the call. Empty — the conversation — for the primary agent's calls
	// and for executions dispatched with no call.
	OwnerAgentID string
	// CallError is the call's own failure, shown once under the header.
	CallError string
	// CallStatus is "canceled" when the call never ran to completion.
	CallStatus string
	// ReviewTarget names the model a plan review runs on ("provider/model"),
	// for the canceled review's label.
	ReviewTarget string
}

// cardTaskRef locates the card task an agent's current execution belongs to.
type cardTaskRef struct {
	stepID string
	index  int
}

type Reducer struct {
	activeTurn         *foregroundTurn
	activeRunID        string
	activeRunStartedAt time.Time
	// buffers holds per-agent streaming state keyed by AgentID.
	// The empty string key is the primary agent. Subagent keys are their
	// roster key (TaskID). Created lazily via agentBuf().
	buffers          map[string]*agentBuffer
	tracker          *Tracker
	agentRoster      map[string]AgentRosterRow
	agentRosterOrder []string
	// fanoutStates tracks live subagent cards keyed by StepID: the dispatching
	// or querying call's id, a review's ReviewID, or "subagent-exec:<id>" for
	// an execution dispatched with no call.
	fanoutStates map[string]*fanoutState
	// cardTaskOfAgent maps a roster key to the card task its current execution
	// belongs to. It is written when a subagent_spawned binds a task and
	// cleared when that execution ends, and it is the only way a subagent's
	// tool messages and a query's rows find their card.
	cardTaskOfAgent map[string]cardTaskRef
	// queryRowsOfExecution holds the query-card rows that reported an
	// execution, keyed by that execution: the execution's ended event settles
	// those rows with it, so a snapshot taken while a task ran ends the way
	// the task did.
	queryRowsOfExecution map[string][]cardTaskRef
	// endedExecutions remembers how each execution ended. A replayed
	// transcript flushes a run's rows after its subagent events, so a query
	// row can arrive after the execution's end already did; the row settles
	// from this record the way the live order settles from the event.
	endedExecutions map[string]endedExecution
	// lastAgentPrompt holds the dispatch prompt most recently shown in each
	// subagent's view, keyed by roster key. Spawn notifications can arrive twice
	// for one subagent (the direct call and the run-step mirror), so the text is
	// compared rather than the key: a repeat is skipped, while a genuinely new
	// instruction — a continued subagent — is appended as the next message.
	lastAgentPrompt map[string]string
	// toolOutput holds bounded, transient output for in-flight shell cards.
	toolOutput map[string]string
	// compactions holds each running compaction's card state, keyed by
	// compaction id, until its terminal event replaces the card.
	compactions map[string]*compactionCard
}

// compactionCard is what a running compaction's card shows between events.
type compactionCard struct {
	agentID      string
	tokensBefore int
	percent      int
	phase        string
}

const maxLiveToolOutputBytes = 256 * 1024

func liveToolOutputKey(msg Message) string {
	return strings.TrimSpace(msg.AgentID) + "\x00" + strings.TrimSpace(msg.StepID)
}

func appendLiveToolOutput(current, chunk string) string {
	chunk = strings.ToValidUTF8(chunk, "\uFFFD")
	chunk = strings.ReplaceAll(chunk, "\r\n", "\n")
	chunk = strings.ReplaceAll(chunk, "\r", "\n")
	current += chunk
	if len(current) <= maxLiveToolOutputBytes {
		return current
	}
	current = current[len(current)-maxLiveToolOutputBytes:]
	if i := strings.IndexByte(current, '\n'); i >= 0 && i+1 < len(current) {
		current = current[i+1:]
	}
	return "[earlier output omitted]\n" + current
}

// WithTracker plugs a counter Tracker into the reducer so tui can update
// the transient working line. nil tracker is safe.
func (r *Reducer) WithTracker(t *Tracker) *Reducer {
	if r == nil {
		return r
	}
	r.tracker = t
	return r
}

// agentBuf returns the per-agent buffer for the given AgentID.
// Empty string is the primary agent. Created lazily.
func (r *Reducer) agentBuf(agentID string) *agentBuffer {
	if r == nil {
		return nil
	}
	id := strings.TrimSpace(agentID)
	if r.buffers == nil {
		r.buffers = make(map[string]*agentBuffer)
	}
	buf, ok := r.buffers[id]
	if !ok {
		buf = &agentBuffer{agentID: id}
		r.buffers[id] = buf
	}
	return buf
}

// findFanoutByStepID returns the card state for the given StepID.
func (r *Reducer) findFanoutByStepID(stepID string) *fanoutState {
	if r == nil || r.fanoutStates == nil {
		return nil
	}
	return r.fanoutStates[strings.TrimSpace(stepID)]
}

// fanoutTaskForLifecycle resolves the exact card task a lifecycle event names:
// the dispatching call's StepID and the task's index in it. Anything else — a
// missing call, an index past the call's tasks — is not this card's task.
func (r *Reducer) fanoutTaskForLifecycle(parentToolCallID string, taskIndex int) (*fanoutState, *FanoutTaskState) {
	stepID := strings.TrimSpace(parentToolCallID)
	if stepID == "" {
		return nil, nil
	}
	fs := r.findFanoutByStepID(stepID)
	if fs == nil || taskIndex < 0 || taskIndex >= len(fs.Tasks) {
		return nil, nil
	}
	return fs, &fs.Tasks[taskIndex]
}

// appendFanoutTaskTool folds a subagent tool event into a task ring buffer.
func appendFanoutTaskTool(t *FanoutTaskState, stepID, label, phase string) {
	if t == nil {
		return
	}
	stepID = strings.TrimSpace(stepID)
	label = strings.TrimSpace(label)
	for i := range t.RecentTools {
		if t.RecentTools[i].StepID != "" && t.RecentTools[i].StepID == stepID {
			if label != "" {
				t.RecentTools[i].Label = label
			}
			return
		}
	}
	if phase == "tool_call_completed" {
		return
	}
	t.ToolTotal++
	t.RecentTools = append(t.RecentTools, fanoutToolEntry{StepID: stepID, Label: label})
	if len(t.RecentTools) > maxFanoutRecentTools {
		t.RecentTools = t.RecentTools[len(t.RecentTools)-maxFanoutRecentTools:]
	}
}

// fanoutToolLabel builds a display label for a subagent tool event.
func fanoutToolLabel(msg Message) string {
	if inv := strings.TrimSpace(msg.ToolMeta.Invocation); inv != "" {
		return inv
	}
	if name := strings.TrimSpace(msg.ToolName); name != "" {
		return name
	}
	return "tool"
}

// removeSubagentRoster removes a subagent from the agent roster.
func (r *Reducer) removeSubagentRoster(agentID string) {
	if r == nil || agentID == "" || r.agentRoster == nil {
		return
	}
	delete(r.agentRoster, agentID)
	filtered := r.agentRosterOrder[:0]
	for _, id := range r.agentRosterOrder {
		if id != agentID {
			filtered = append(filtered, id)
		}
	}
	r.agentRosterOrder = filtered
}

// standaloneExecStepID is the card id of an execution dispatched with no call:
// the reviewer of a plan the user asked about, an engine-dispatched helper. It
// is keyed by the execution, so the second delivery of one spawn (the direct
// notification and the run-step mirror) finds the same card.
func standaloneExecStepID(executionID, taskID string) string {
	id := strings.TrimSpace(executionID)
	if id == "" {
		id = strings.TrimSpace(taskID)
	}
	return "subagent-exec:" + id
}

// setCardTaskOfAgent records which card task an agent's current execution
// belongs to.
func (r *Reducer) setCardTaskOfAgent(agentID string, ref cardTaskRef) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return
	}
	if r.cardTaskOfAgent == nil {
		r.cardTaskOfAgent = make(map[string]cardTaskRef)
	}
	r.cardTaskOfAgent[agentID] = ref
}

// dropCardTaskOfAgent forgets an agent's execution binding: the execution
// ended, and the next one names the card it belongs to.
func (r *Reducer) dropCardTaskOfAgent(agentID string) {
	delete(r.cardTaskOfAgent, strings.TrimSpace(agentID))
}

// cardTaskOf looks up the card task an agent's current execution belongs to.
func (r *Reducer) cardTaskOf(agentID string) (*fanoutState, *FanoutTaskState) {
	ref, ok := r.cardTaskOfAgent[strings.TrimSpace(agentID)]
	if !ok {
		return nil, nil
	}
	fs := r.findFanoutByStepID(ref.stepID)
	if fs == nil || ref.index < 0 || ref.index >= len(fs.Tasks) {
		return nil, nil
	}
	return fs, &fs.Tasks[ref.index]
}

// adoptReplayedCards takes over the card state a resume's replay built. The
// replay renders through its own reducer, which is then discarded, while the
// events that still arrive for the resumed conversation — this process's
// reaper settling a subagent the killed one left running, first among them —
// reach the live reducer, which never saw the dispatch. Handing the replayed
// cards over keeps one binding: the ended settles the card on screen, with the
// stop time its payload stamps, exactly as the full replay settles it from the
// log. The replayed conversation is everything on screen after the switch, so
// its state replaces whatever an earlier conversation left behind.
func (r *Reducer) adoptReplayedCards(from *Reducer) {
	r.fanoutStates = from.fanoutStates
	r.cardTaskOfAgent = from.cardTaskOfAgent
	r.queryRowsOfExecution = from.queryRowsOfExecution
	r.endedExecutions = from.endedExecutions
}

// bindFanoutTaskToSpawn binds a spawned execution to a card task: the task now
// runs as this agent, its clock starts at the spawn, and the agent's tool
// messages find the row through cardTaskOfAgent. A task already settled by its
// own end — a re-delivered spawn arriving after it — keeps its outcome.
func (r *Reducer) bindFanoutTaskToSpawn(fs *fanoutState, index int, m SubagentSpawnedMsg) {
	if fs == nil || index < 0 || index >= len(fs.Tasks) {
		return
	}
	t := &fs.Tasks[index]
	started := m.Timestamp
	if started.IsZero() {
		started = time.Now()
	}
	t.AgentID = strings.TrimSpace(m.AgentID)
	t.ExecutionID = strings.TrimSpace(m.ExecutionID)
	t.StartedAt = started
	if fanoutTaskTerminal(t.Status) {
		// The task's outcome already arrived — a re-delivered spawn after its
		// end, or a replay whose completed row settled it before the events.
		// The identity is still the fact this message carries.
		return
	}
	t.Status = "running"
	t.EndedAt = time.Time{}
	if t.Title == "" {
		t.Title = firstNonEmpty(strings.TrimSpace(m.Title), tool.SubagentTaskTitle("", strings.TrimSpace(m.Task)))
	}
	if t.SubagentType == "" {
		t.SubagentType = strings.TrimSpace(m.AgentType)
	}
	r.setCardTaskOfAgent(m.AgentID, cardTaskRef{stepID: fs.StepID, index: index})
}

// settleFanoutTaskOnEnd writes an execution's outcome into the card task it
// was bound to. The clock stops at the payload's own stop time — a reaped
// execution was already dead when the reaper said so.
func settleFanoutTaskOnEnd(t *FanoutTaskState, m SubagentEndedMsg, status string) {
	t.Status = status
	t.Error = strings.TrimSpace(m.Error)
	ended := m.FinishedAt
	if ended.IsZero() {
		ended = m.Timestamp
	}
	if !ended.IsZero() && !t.StartedAt.IsZero() {
		t.EndedAt = ended
	}
}

// fanoutTaskTerminal reports whether a task's outcome is final.
func fanoutTaskTerminal(status string) bool {
	switch status {
	case "done", "failed", "cancelled", "skipped":
		return true
	default:
		return false
	}
}

// subagentQueryVerb reports whether a verb's card is a snapshot of its call.
func subagentQueryVerb(verb string) bool {
	switch strings.TrimSpace(verb) {
	case "status", "wait", "close", "list":
		return true
	default:
		return false
	}
}

// fanoutTaskFromFacts copies one task's facts into a card task. The facts'
// seconds become times; a running clock needs only its start.
func fanoutTaskFromFacts(task event.SubagentCallTask) FanoutTaskState {
	out := FanoutTaskState{
		Title:         strings.TrimSpace(task.Title),
		SubagentType:  strings.TrimSpace(task.AgentType),
		AgentID:       strings.TrimSpace(task.Key),
		Status:        strings.TrimSpace(task.Status),
		Error:         strings.TrimSpace(task.Error),
		ExecutionID:   strings.TrimSpace(task.ExecutionID),
		TimedOut:      task.TimedOut,
		StopRequested: task.StopRequested,
	}
	if task.StartedAt > 0 {
		out.StartedAt = time.Unix(task.StartedAt, 0).UTC()
	}
	if task.FinishedAt > 0 {
		out.EndedAt = time.Unix(task.FinishedAt, 0).UTC()
	}
	return out
}

// applyFanoutTaskFacts settles one dispatch task from the call's facts,
// keeping what the lifecycle events already said where they said more.
func applyFanoutTaskFacts(t *FanoutTaskState, task event.SubagentCallTask) {
	from := fanoutTaskFromFacts(task)
	t.Status = from.Status
	if from.Error != "" {
		t.Error = from.Error
	}
	if from.Title != "" {
		t.Title = from.Title
	}
	if from.SubagentType != "" {
		t.SubagentType = from.SubagentType
	}
	if from.AgentID != "" {
		t.AgentID = from.AgentID
	}
	if from.ExecutionID != "" {
		t.ExecutionID = from.ExecutionID
	}
	if !from.StartedAt.IsZero() {
		t.StartedAt = from.StartedAt
	}
	if !from.EndedAt.IsZero() {
		t.EndedAt = from.EndedAt
	}
}

// resolveQueryTaskTitles names a query call's tasks while only their ids are
// known: the title the reducer already has for the agent behind the key. Rows
// it cannot name stay titleless and are not drawn until the result names them.
func (r *Reducer) resolveQueryTaskTitles(fs *fanoutState) {
	for i := range fs.Tasks {
		t := &fs.Tasks[i]
		if t.Title != "" || t.AgentID == "" {
			continue
		}
		if _, bound := r.cardTaskOf(t.AgentID); bound != nil {
			t.Title = bound.Title
			if t.SubagentType == "" {
				t.SubagentType = bound.SubagentType
			}
		}
	}
}

// handleSubagentToolMessage is the card path every subagent_* call takes. The
// start opens the card from the call's facts; the completion settles it — a
// dispatch card takes the facts only for the tasks no lifecycle event ever
// bound, a query card is redrawn from them whole.
func (r *Reducer) handleSubagentToolMessage(msg Message) []Frame {
	stepID := strings.TrimSpace(msg.StepID)
	call := msg.ToolMeta.SubagentCall
	owner := strings.TrimSpace(msg.AgentID)
	buf := r.agentBuf(owner)
	if fs := r.findFanoutByStepID(stepID); fs != nil {
		fs.Running = false
		fs.Duration = msg.Duration
		r.applySubagentCallCompletion(fs, msg)
		return r.withFlushedBuffers(buf, r.emitFanoutFrame(fs), msg.Timestamp)
	}
	runID := strings.TrimSpace(msg.RunID)
	if runID == "" {
		runID = r.activeRunID
	}
	fs := r.fanoutStateFromCall(stepID, call, msg, runID)
	fs.Duration = msg.Duration
	if r.fanoutStates == nil {
		r.fanoutStates = make(map[string]*fanoutState)
	}
	r.fanoutStates[stepID] = fs
	r.resolveQueryTaskTitles(fs)
	if !fs.Running {
		r.applySubagentCallCompletion(fs, msg)
	}
	return r.withFlushedBuffers(buf, r.emitFanoutFrame(fs), msg.Timestamp)
}

// fanoutStateFromCall opens a card from a call's facts: which tasks, named
// how, and what the call does to them.
func (r *Reducer) fanoutStateFromCall(stepID string, call *event.SubagentCall, msg Message, runID string) *fanoutState {
	fs := &fanoutState{
		StepID:       stepID,
		Verb:         strings.TrimSpace(call.Verb),
		Query:        subagentQueryVerb(call.Verb),
		OwnerAgentID: strings.TrimSpace(msg.AgentID),
		RunID:        runID,
		Tasks:        make([]FanoutTaskState, 0, len(call.Tasks)),
	}
	for _, task := range call.Tasks {
		fs.Tasks = append(fs.Tasks, fanoutTaskFromFacts(task))
	}
	fs.Running = msg.ToolPhase != event.RunEventToolCompleted &&
		(strings.TrimSpace(msg.ToolMeta.Status) == "" || strings.EqualFold(strings.TrimSpace(msg.ToolMeta.Status), "running"))
	return fs
}

// applySubagentCallCompletion settles a card from its completed call. The
// call's own failure or cancellation is the header's story; the tasks take
// their outcomes from the facts.
func (r *Reducer) applySubagentCallCompletion(fs *fanoutState, msg Message) {
	status := strings.TrimSpace(msg.ToolMeta.Status)
	call := msg.ToolMeta.SubagentCall
	switch {
	case strings.EqualFold(status, "canceled"):
		fs.CallStatus = "canceled"
	case strings.EqualFold(status, "failed"):
		fs.CallError = normalizeFailedToolContent(strings.TrimSpace(msg.Content))
	}
	if call == nil {
		return
	}
	if fs.Query {
		// The result replaces the rows: a snapshot reports the tasks as the
		// record described them, titled by the record itself.
		fs.Tasks = make([]FanoutTaskState, 0, len(call.Tasks))
		for _, task := range call.Tasks {
			fs.Tasks = append(fs.Tasks, fanoutTaskFromFacts(task))
		}
		r.followQueryRowsOfExecution(fs)
		return
	}
	for i := range fs.Tasks {
		if i >= len(call.Tasks) {
			break
		}
		if fs.Tasks[i].AgentID != "" {
			// Bound by a lifecycle event: its own end says how it went.
			continue
		}
		applyFanoutTaskFacts(&fs.Tasks[i], call.Tasks[i])
	}
}

// endedExecution is how one execution ended, as its ended event said.
type endedExecution struct {
	status string
	at     time.Time
}

// followQueryRowsOfExecution remembers, per execution, the query rows that
// reported it: when that execution ends in this session, the rows end with
// it, so a snapshot taken while a task ran closes the way the task did.
func (r *Reducer) followQueryRowsOfExecution(fs *fanoutState) {
	for i := range fs.Tasks {
		t := &fs.Tasks[i]
		if t.ExecutionID == "" || t.Status != "running" {
			continue
		}
		// A replayed transcript delivers a run's rows after its subagent
		// events, so the end may already have been seen: the row settles from
		// that record now, exactly as the live order settles from the event.
		if ended, ok := r.endedExecutions[t.ExecutionID]; ok {
			t.Status = ended.status
			if !ended.at.IsZero() {
				t.EndedAt = ended.at
			}
			continue
		}
		if r.queryRowsOfExecution == nil {
			r.queryRowsOfExecution = make(map[string][]cardTaskRef)
		}
		ref := cardTaskRef{stepID: fs.StepID, index: i}
		rows := r.queryRowsOfExecution[t.ExecutionID]
		for _, existing := range rows {
			if existing == ref {
				continue
			}
		}
		r.queryRowsOfExecution[t.ExecutionID] = append(rows, ref)
	}
}

// settleQueryRowsOnExecutionEnd closes the query rows that reported an
// execution when that execution ends. Every card a settled row belongs to is
// re-emitted: the row on screen is the conversation's account of the task, and
// state no frame carries never reaches it — the row's running clock would walk
// past the execution's own end.
func (r *Reducer) settleQueryRowsOnExecutionEnd(executionID string, status string, ended time.Time) []Frame {
	if executionID == "" {
		return nil
	}
	rows := r.queryRowsOfExecution[executionID]
	delete(r.queryRowsOfExecution, executionID)
	var out []Frame
	for _, ref := range rows {
		fs := r.findFanoutByStepID(ref.stepID)
		if fs == nil || ref.index < 0 || ref.index >= len(fs.Tasks) {
			continue
		}
		t := &fs.Tasks[ref.index]
		t.Status = status
		if !ended.IsZero() {
			t.EndedAt = ended
		}
		out = append(out, r.emitFanoutFrame(fs))
	}
	return out
}

// emitFanoutFrame builds the FrameFanout card from the current state.
func (r *Reducer) emitFanoutFrame(fs *fanoutState) Frame {
	if fs == nil {
		return Frame{}
	}
	content, lineAgents, clocks := renderFanoutContent(fs)
	// The header names a duration only when the rows do not already: a
	// query's own call, and a multi-task batch's wall clock. A single
	// dispatched task carries its final elapsed on its row.
	duration := fs.Duration
	if !fs.Query && len(fs.Tasks) <= 1 {
		duration = 0
	}
	return Frame{
		Kind:   FrameFanout,
		StepID: fs.StepID,
		// A plan review is part of the approval exchange that asked for it:
		// it lands above the parked exit-plan call, between that approval's
		// two confirmation lines, not at the transcript tail. Every other
		// fanout card keeps its producer order.
		InsertBeforeLastTool: fs.Verb == "review",
		RunID:                r.activeRunID,
		AgentID:              fs.OwnerAgentID,
		Final:                fanoutCardSettled(fs),
		Content:              content,
		FanoutLineAgents:     lineAgents,
		FanoutLineClocks:     clocks,
		Summary:              fanoutCardHeader(fs),
		Duration:             duration,
		FanoutQuery:          fs.Query,
		FanoutCallError:      fs.CallError,
	}
}

// fanoutCardSettled reports whether the card has nothing left in flight: its
// call finished and none of its tasks is running or waiting. A query card
// settles with its call — the rows it leaves running describe a snapshot.
func fanoutCardSettled(fs *fanoutState) bool {
	switch fanoutCardPhase(fs) {
	case "running", "starting":
		return false
	default:
		return true
	}
}

// fanoutCardPhase is which header row the card shows: starting, running, the
// settled done/failed forms, or the canceled label.
func fanoutCardPhase(fs *fanoutState) string {
	if fs.CallStatus == "canceled" {
		return "canceled"
	}
	if fs.CallError != "" {
		return "failed"
	}
	if fs.Query {
		if fs.Running {
			return "running"
		}
		return "done"
	}
	if fs.Running || fanoutAnyTaskLive(fs) {
		// A send or a review whose agent has not begun — and none has ended —
		// is starting one; every other dispatch is already going.
		if (fs.Verb == "send" || fs.Verb == "review") &&
			!fanoutAnyTaskRunning(fs) && !fanoutAnyTaskTerminal(fs) {
			return "starting"
		}
		return "running"
	}
	return "done"
}

// fanoutAnyTaskTerminal reports whether any task already reached an outcome.
func fanoutAnyTaskTerminal(fs *fanoutState) bool {
	for _, t := range fs.Tasks {
		if fanoutTaskTerminal(t.Status) {
			return true
		}
	}
	return false
}

func fanoutAnyTaskRunning(fs *fanoutState) bool {
	for _, t := range fs.Tasks {
		if t.Status == "running" {
			return true
		}
	}
	return false
}

func fanoutAnyTaskLive(fs *fanoutState) bool {
	for _, t := range fs.Tasks {
		if t.Status == "running" || t.Status == "waiting" {
			return true
		}
	}
	return false
}

// fanoutTaskCountLabel is the "<N> [<type>] task(s)" a subagent card header
// counts its tasks with: the type appears when every task runs as the same
// known type. The rule is pkg/tool's subagentTaskCountLabel restated, so the
// card header and the transcript summary cannot disagree.
func fanoutTaskCountLabel(fs *fanoutState, withType bool) string {
	n := len(fs.Tasks)
	noun := "tasks"
	if n == 1 {
		noun = "task"
	}
	if !withType || n == 0 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	first := ""
	same := true
	for i, t := range fs.Tasks {
		typ := strings.TrimSpace(t.SubagentType)
		if typ == "" {
			same = false
			break
		}
		if i == 0 {
			first = typ
		} else if typ != first {
			same = false
			break
		}
	}
	if !same {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %s %s", n, first, noun)
}

// fanoutCardHeader is the card's header row: the verb's phrase for the phase
// the card is in, its task count, and — for a settled dispatch whose tasks
// did not all succeed — the outcome tally. One table, one place; the lowercase
// forms are what pkg/tool's step summaries say.
func fanoutCardHeader(fs *fanoutState) string {
	phase := fanoutCardPhase(fs)
	if phase == "canceled" {
		return "Canceled " + fanoutCanceledLabel(fs)
	}
	count := func(withType bool) string { return fanoutTaskCountLabel(fs, withType) }
	var head string
	switch fs.Verb {
	case "send":
		switch phase {
		case "starting":
			head = "Starting " + count(true) + " in background…"
		case "running":
			head = "Running " + count(true) + " in background…"
		case "failed":
			head = "Failed to start " + count(true) + " in background"
		default:
			head = "Ran " + count(true) + " in background"
		}
	case "continue":
		switch phase {
		case "running":
			head = "Continuing " + count(true) + "…"
		case "failed":
			head = "Failed to continue " + count(true)
		default:
			head = "Continued " + count(true)
		}
	case "status":
		switch phase {
		case "running":
			head = "Checking " + count(false) + "…"
		case "failed":
			head = "Failed to check " + count(false)
		default:
			head = "Checked " + count(true)
		}
	case "wait":
		switch phase {
		case "running":
			head = "Waiting for " + count(true) + "…"
		case "failed":
			head = "Failed to wait for " + count(false)
		default:
			head = "Waited for " + count(true)
		}
	case "close":
		switch phase {
		case "running":
			head = "Stopping " + count(false) + "…"
		case "failed":
			head = "Failed to stop " + count(false)
		default:
			head = "Stopped " + count(true)
		}
	case "list":
		switch phase {
		case "running":
			head = "Listing tasks…"
		case "failed":
			head = "Failed to list tasks"
		default:
			head = "Listed " + count(false)
		}
	case "review":
		switch phase {
		case "starting":
			head = "Starting " + count(true) + "…"
		case "running":
			head = "Running " + count(true) + "…"
		case "failed":
			head = "Failed to start " + count(true)
		default:
			head = "Ran " + count(true)
		}
	default: // "run": subagent_run, subagent_fanout, dispatches with no call
		switch phase {
		case "running":
			head = "Running " + count(true) + "…"
		case "failed":
			head = "Failed to run " + count(true)
		default:
			head = "Ran " + count(true)
		}
	}
	if phase == "done" && !fs.Query {
		if breakdown := fanoutOutcomeBreakdown(fs); breakdown != "" {
			head += " · " + breakdown
		}
	}
	return head
}

// fanoutOutcomeBreakdown counts the outcomes a settled dispatch card reports
// on its header. All-done needs no arithmetic; anything else lists every
// non-zero count, done included.
func fanoutOutcomeBreakdown(fs *fanoutState) string {
	var done, failed, cancelled, skipped int
	for _, t := range fs.Tasks {
		switch t.Status {
		case "done":
			done++
		case "failed":
			failed++
		case "cancelled":
			cancelled++
		case "skipped":
			skipped++
		}
	}
	if failed+cancelled+skipped == 0 {
		return ""
	}
	parts := make([]string, 0, 4)
	parts = append(parts, fmt.Sprintf("%d done", done))
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if cancelled > 0 {
		parts = append(parts, fmt.Sprintf("%d cancelled", cancelled))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", skipped))
	}
	return strings.Join(parts, ", ")
}

// fanoutCanceledLabel is what a canceled call says it was doing, in the voice
// the invocation labels use ("wait for X", "run 3 tasks").
func fanoutCanceledLabel(fs *fanoutState) string {
	name := ""
	for _, t := range fs.Tasks {
		if t.Title != "" {
			name = t.Title
			break
		}
	}
	switch fs.Verb {
	case "send":
		return "send " + name
	case "continue":
		return "continue " + name
	case "status":
		return "check " + name
	case "wait":
		return "wait for " + name
	case "close":
		return "stop " + name
	case "list":
		return "list tasks"
	case "review":
		if fs.ReviewTarget != "" {
			return "review the plan with " + fs.ReviewTarget
		}
		return "review the plan"
	default:
		if len(fs.Tasks) > 1 {
			return "run " + fanoutTaskCountLabel(fs, true)
		}
		return "run " + name
	}
}

// cancelInFlightQueryCards settles the query cards a run or an agent leaves
// behind: their call never completed, so the card says the call was canceled
// rather than waiting forever. Dispatch cards are left alone — their agents
// outlive the call and the turn.
func (r *Reducer) cancelInFlightQueryCards(runID, ownerAgentID string) []Frame {
	var out []Frame
	for _, fs := range r.fanoutStates {
		if !fs.Query || !fs.Running {
			continue
		}
		if runID != "" && fs.RunID != "" && fs.RunID != runID {
			continue
		}
		if ownerAgentID != "" && fs.OwnerAgentID != ownerAgentID {
			continue
		}
		fs.Running = false
		fs.CallStatus = "canceled"
		out = append(out, r.emitFanoutFrame(fs))
	}
	return out
}

// openPlanReviewCard opens the plan review's card: one task, waiting, named
// for what the review is of. The review request is the dispatch — the
// reviewer's spawned event names the ReviewID as its parent tool call.
func (r *Reducer) openPlanReviewCard(reviewID, provider, model, label string) *fanoutState {
	stepID := strings.TrimSpace(reviewID)
	if fs := r.findFanoutByStepID(stepID); fs != nil {
		return fs
	}
	target := strings.TrimSpace(provider) + "/" + strings.TrimSpace(model)
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "" {
		target = strings.TrimSpace(label)
	}
	fs := &fanoutState{
		StepID:       stepID,
		Verb:         "review",
		RunID:        r.activeRunID,
		ReviewTarget: target,
		Tasks: []FanoutTaskState{{
			Title:        "Plan review",
			SubagentType: run.PlanReviewSubagentType,
			Status:       "waiting",
		}},
	}
	if r.fanoutStates == nil {
		r.fanoutStates = make(map[string]*fanoutState)
	}
	r.fanoutStates[stepID] = fs
	return fs
}

func (r *Reducer) AgentRosterSnapshot() AgentRosterSnapshot {
	if r == nil || len(r.agentRosterOrder) == 0 {
		return AgentRosterSnapshot{Rows: []AgentRosterRow{}}
	}
	rows := make([]AgentRosterRow, 0, len(r.agentRosterOrder))
	for _, id := range r.agentRosterOrder {
		if row, ok := r.agentRoster[id]; ok {
			rows = append(rows, row)
		}
	}
	return AgentRosterSnapshot{Rows: rows}
}

func (r *Reducer) Reduce(msg any) EventResult {
	switch m := msg.(type) {
	case RunStartedMsg:
		if m.turn.isWithdrawn() {
			return EventResult{}
		}
		r.activeTurn = m.turn
		r.activeRunID = strings.TrimSpace(m.RunID)
		if r.tracker != nil {
			r.tracker.StartRun(r.activeRunID)
		}
		r.activeRunStartedAt = time.Now()
		if buf := r.agentBuf(""); buf != nil {
			buf.assistant.Reset()
			buf.reasoning.Reset()
		}
		return EventResult{}
	case NewMessageMsg:
		if m.turn.isWithdrawn() {
			return EventResult{}
		}
		return EventResult{Frames: r.reduceMessage(m.Msg)}
	case ReasoningDoneMsg:
		if m.turn.isWithdrawn() {
			return EventResult{}
		}
		return EventResult{Frames: r.flushReasoningFinal(r.agentBuf(strings.TrimSpace(m.AgentID)), m.Timestamp)}
	case RunEndedMsg:
		runID := strings.TrimSpace(m.RunID)
		if m.turn.isWithdrawn() {
			// A withdrawn turn is presented as a turn that never happened, so it
			// is the one case that does not close with a "Worked for …" line.
			// This exception is scoped to withdrawal and nothing else: an
			// interrupted turn, a failed turn and a turn cancelled after the
			// model answered all keep that line, because those did happen.
			//
			// Accounting is not part of the exception. The request went out and
			// was billed, so its tokens are still folded into the session totals
			// the composer footer and the prompt-cache hit-rate numbers are
			// computed from — only the conversation residue is dropped.
			if r.tracker != nil && runID != "" {
				if m.InputTokens > 0 || m.OutputTokens > 0 {
					r.tracker.ObserveRunEndForRun(runID, m.InputTokens, m.OutputTokens)
				}
				r.tracker.EndRun(runID)
			}
			if r.activeTurn == m.turn {
				r.activeTurn = nil
				r.activeRunID = ""
				r.activeRunStartedAt = time.Time{}
				if r.tracker != nil {
					r.tracker.StartRun("")
				}
				if buf := r.agentBuf(""); buf != nil {
					buf.assistant.Reset()
					buf.reasoning.Reset()
					buf.runningStepIDs = nil
				}
			}
			return EventResult{}
		}
		if runID == "" {
			runID = r.activeRunID
		}
		if r.tracker != nil && (m.InputTokens > 0 || m.OutputTokens > 0) {
			r.tracker.ObserveRunEndForRun(runID, m.InputTokens, m.OutputTokens)
		}
		workDuration := m.WorkedDuration
		if workDuration <= 0 && !r.activeRunStartedAt.IsZero() {
			workDuration = time.Since(r.activeRunStartedAt)
		}
		var out []Frame
		if buf := r.agentBuf(""); buf != nil {
			out = append(out, r.flushReasoningFinal(buf)...)
			if text := strings.TrimSpace(buf.assistant.String()); text != "" {
				out = append(out, Frame{Kind: FrameAssistant, Content: text, RunID: runID, Final: true})
			}
		}
		if errText := strings.TrimSpace(m.Error); errText != "" {
			out = append(out, Frame{Kind: FrameError, Title: "run error", Content: errText, RunID: runID, Final: true})
		}
		// Every run is closed by its "Worked for" line, whatever ended it and
		// however short it was: a normal completion, the user interrupting, or
		// a failure. The line is the transcript's boundary between one turn and
		// the next and the only place a turn's own duration and spend are
		// stated, so a run that ends without one leaves the reader unable to
		// tell where it stopped or what it cost.
		worked := r.formatWorkedStatusTitle(workDuration, runID)
		if r.tracker != nil {
			r.tracker.EndRun(runID)
			r.tracker.StartRun("")
		}
		r.activeRunID = ""
		r.activeTurn = nil
		r.activeRunStartedAt = time.Time{}
		if buf := r.agentBuf(""); buf != nil {
			buf.assistant.Reset()
			buf.reasoning.Reset()
			buf.runningStepIDs = nil
		}
		// The turn's own query calls (subagent_status and friends) that never
		// completed are canceled with it. A dispatch's agents outlive the
		// turn, so those cards are deliberately left running.
		out = append(out, r.cancelInFlightQueryCards(runID, "")...)
		return EventResult{Frames: out, WorkedStatus: worked}
	case TokenUsageDeltaMsg:
		if r.tracker != nil && (m.InputTokens > 0 || m.OutputTokens > 0) {
			runID := strings.TrimSpace(m.RunID)
			if runID == "" {
				runID = r.activeRunID
			}
			// Deltas tagged with a subagent AgentID are accumulated in the
			// dedicated subagent counters so they survive the parent run's
			// end-of-run reconciliation (ObserveRunEndForRun overwrites
			// runUsage with the parent's authoritative Summary.Usage, which
			// does not include subagent LLM calls).
			if saID := strings.TrimSpace(m.AgentID); saID != "" {
				// Use the parent's activeRunID for the per-run subagent
				// accumulator so SnapshotRun(parentRunID) includes subagent
				// tokens in the "Worked for" message and turn separator.
				r.tracker.ObserveSubagentUsage(r.activeRunID, saID, m.InputTokens, m.OutputTokens)
				return EventResult{}
			}
			r.tracker.ObserveUsageDelta(runID, m.InputTokens, m.OutputTokens)
		}
		return EventResult{}
	case StreamResetMsg:
		if m.turn.isWithdrawn() {
			return EventResult{}
		}
		if buf := r.agentBuf(""); buf != nil {
			buf.assistant.Reset()
			buf.reasoning.Reset()
		}
		// An aborted run takes its still-running query calls with it: the
		// calls never complete, so their cards settle as canceled here the
		// way the tool cards do below them (Renderer.FinalizePendingTools).
		return EventResult{Frames: r.cancelInFlightQueryCards(r.activeRunID, "")}
	case GoalStartedMsg:
		if r.tracker != nil {
			r.tracker.ObserveGoalRound(1)
		}
		return EventResult{Frames: r.withFlushedBuffers(r.agentBuf(""), goalStartedFrame(m.RunID, m.Objective))}
	case GoalRoundStartedMsg:
		if r.tracker != nil {
			r.tracker.ObserveGoalRound(m.Round)
		}
		// The round's answer is its own block: whatever the last round said is
		// closed here, so rounds never run together into one paragraph.
		return EventResult{Frames: r.withFlushedBuffers(r.agentBuf(""), goalRoundFrame(m.RunID, m.Round, m.Why, m.CheckAgentID))}
	case GoalCompletedMsg:
		if r.tracker != nil {
			r.tracker.ObserveGoalRound(0)
		}
		return EventResult{Frames: r.withFlushedBuffers(r.agentBuf(""), goalCompletedFrame(m.RunID, m.Payload))}
	case ContextCompactingMsg:
		card := &compactionCard{agentID: strings.TrimSpace(m.AgentID), tokensBefore: m.TokensBefore}
		if r.compactions == nil {
			r.compactions = make(map[string]*compactionCard)
		}
		r.compactions[m.CompactionID] = card
		return EventResult{Frames: r.withFlushedBuffers(r.agentBuf(card.agentID), r.runningCompactionFrame(m.CompactionID, card))}
	case ContextCompactProgressMsg:
		card := r.compactions[m.CompactionID]
		if card == nil {
			// The card's start was drawn before this reducer existed — a
			// surface that attached mid-compaction — so the progress starts it.
			card = &compactionCard{agentID: strings.TrimSpace(m.AgentID)}
			if r.compactions == nil {
				r.compactions = make(map[string]*compactionCard)
			}
			r.compactions[m.CompactionID] = card
		}
		card.percent, card.phase = m.Percent, m.Phase
		return EventResult{Frames: []Frame{r.runningCompactionFrame(m.CompactionID, card)}}
	case ContextCompactFailedMsg:
		delete(r.compactions, m.CompactionID)
		agentID := strings.TrimSpace(m.AgentID)
		ended := Frame{
			Kind: FrameMemoryCompact, StepID: compactionStepID(m.CompactionID), AgentID: agentID,
			RunID: r.activeRunID, Final: true, Title: compactTitleFailed, Content: strings.TrimSpace(m.Error),
		}
		if m.Cancelled {
			ended.Title, ended.Content = compactTitleCancelled, ""
		}
		return EventResult{Frames: r.withFlushedBuffers(r.agentBuf(agentID), ended)}
	case ContextCompactedMsg:
		p := m.Payload
		delete(r.compactions, p.CompactionID)
		agentID := strings.TrimSpace(p.AgentID)
		duration, _ := time.ParseDuration(strings.TrimSpace(p.Duration))
		banner := Frame{
			Kind:    FrameMemoryCompact,
			StepID:  compactionStepID(p.CompactionID),
			AgentID: agentID,
			RunID:   r.activeRunID,
			Final:   true,
			Title:   compactTitleDone,
			Summary: assembly.FormatCompactBanner(p, duration),
			Content: assembly.FormatCompactOutput(p),
		}
		frames := r.withFlushedBuffers(r.agentBuf(agentID), banner)
		if p.Strategy == "local" {
			frames = append(frames, Frame{Kind: FrameSystem, Title: "warning", Content: assembly.WarningMessage, AgentID: agentID, RunID: r.activeRunID, Final: true})
		}
		return EventResult{Frames: frames}
	case TokenBudgetUpdatedMsg:
		// A budget tagged with an agent is that subagent's own gauge; it
		// belongs to that subagent's view footer, never to the conversation's.
		stats := composerTokenStatsFromBudget(m)
		return EventResult{ComposerTokenStats: &stats, ComposerTokenStatsAgent: strings.TrimSpace(m.AgentID)}
	case PlanUpdatedMsg:
		// A subagent's plan is its own: it belongs in that subagent's view, its
		// progress belongs on that view's working line, and the composer's
		// progress line keeps tracking the conversation's plan rather than
		// being overwritten by a worker's checklist.
		agentID := strings.TrimSpace(m.Payload.AgentID)
		if r.tracker != nil {
			// The task in flight is the payload's own derivation, which the
			// engine computed once; events persisted before it carried one
			// still replay through the same rule, where it lives.
			active := strings.TrimSpace(m.Payload.Active)
			if active == "" {
				active = shortestActiveTaskTitle(m.Payload.Items, m.Payload.Explanation)
			}
			if agentID == "" {
				r.tracker.ObservePlanProgress(m.Payload.Completed, m.Payload.Total, active)
			} else {
				r.tracker.ObserveAgentPlanProgress(agentID, m.Payload.Completed, m.Payload.Total, active)
			}
		}
		frame := Frame{Kind: FramePlan, Title: planTitle(m.Payload), Content: planText(m.Payload), RunID: r.activeRunID, Final: true, AgentID: agentID}
		return EventResult{Frames: r.withFlushedBuffers(r.agentBuf(agentID), frame)}
	case SubagentInputDeliveredMsg:
		// A message the user sent this subagent reached its model: it is a user
		// message in that subagent's own view, the way a delivered steer is one
		// in the conversation. It never appears in the primary transcript.
		text := strings.TrimSpace(m.Text)
		agentID := strings.TrimSpace(m.AgentID)
		if text == "" || agentID == "" {
			return EventResult{}
		}
		return EventResult{Frames: []Frame{{
			Kind:    FrameUser,
			Title:   "you",
			Content: text,
			RunID:   strings.TrimSpace(m.RunID),
			AgentID: agentID,
			Final:   true,
		}}}
	case SubagentSpawnedMsg:
		if r.tracker != nil {
			r.tracker.ObserveAgent(strings.TrimSpace(m.AgentID), strings.TrimSpace(m.ExecutionID), m.Timestamp)
		}
		r.upsertSubagentRoster(m.AgentID, m.AgentType, m.Title, m.Task, "running")
		// The dispatching agent's prompt opens the subagent's own view, ahead of
		// anything the subagent produces, so that view reads as a conversation
		// from its first line.
		frames := r.subagentPromptFrames(m)
		// A goal's check belongs to the goal: the round or closing line it
		// produces opens its view, so it draws no card of its own.
		if strings.TrimSpace(m.AgentType) == run.GoalCheckSubagentType {
			if r.tracker != nil {
				r.tracker.ObserveGoalCheck(true)
			}
			return EventResult{Frames: frames}
		}
		// Exact binding first: the dispatching call's id and the task's index
		// in it. A dispatch the transcript cannot account for — a runtime
		// dispatch, an unmigrated old send — opens a card of its own keyed by
		// its execution, which the spawn's own re-delivery finds again.
		if fs, _ := r.fanoutTaskForLifecycle(m.ParentToolCallID, m.TaskIndex); fs != nil {
			r.bindFanoutTaskToSpawn(fs, m.TaskIndex, m)
			return EventResult{Frames: append(frames, r.emitFanoutFrame(fs))}
		}
		stepID := standaloneExecStepID(m.ExecutionID, m.TaskID)
		fs := r.findFanoutByStepID(stepID)
		if fs == nil {
			fs = &fanoutState{
				StepID: stepID,
				Verb:   "run",
				RunID:  r.activeRunID,
				Tasks: []FanoutTaskState{{
					Title: firstNonEmpty(strings.TrimSpace(m.Title),
						tool.SubagentTaskTitle("", strings.TrimSpace(m.Task)),
						strings.TrimSpace(m.AgentType)),
					SubagentType: strings.TrimSpace(m.AgentType),
					Status:       "waiting",
				}},
			}
			if r.fanoutStates == nil {
				r.fanoutStates = make(map[string]*fanoutState)
			}
			r.fanoutStates[stepID] = fs
		}
		r.bindFanoutTaskToSpawn(fs, 0, m)
		return EventResult{Frames: append(frames, r.emitFanoutFrame(fs))}
	case SubagentEndedMsg:
		status := strings.TrimSpace(m.Status)
		if status == "" {
			status = "done"
		}
		endStatus := status
		if endStatus != "cancelled" {
			if endStatus == "failed" || strings.TrimSpace(m.Error) != "" {
				endStatus = "failed"
			} else {
				endStatus = "done"
			}
		}
		agentID := strings.TrimSpace(m.AgentID)
		if agentID == "" {
			agentID = strings.TrimSpace(m.TaskID)
		}
		r.removeSubagentRoster(agentID)
		// Flush buffered assistant/reasoning text as AgentID-tagged final
		// frames so the subagent's per-agent view retains its full transcript
		// (these route only to perAgentVM, never the primary surface).
		buf := r.agentBuf(agentID)
		hadStreamedAnswer := buf.sawAssistant
		flushed := r.flushBufferedText(buf, m.Timestamp)
		if !hadStreamedAnswer && strings.TrimSpace(m.Output) != "" {
			flushed = append(flushed, Frame{Kind: FrameAssistant, Content: strings.TrimSpace(m.Output), RunID: r.activeRunID, AgentID: agentID, Final: true})
		}
		buf.sawAssistant = false
		// The closing frames belong to the subagent's own view; the card is
		// the conversation's account of the same event. Both are needed: a
		// user who opens the agent to read what happened must find the ending
		// there, not only on the card they clicked to get in.
		if r.tracker != nil {
			r.tracker.ObserveAgentEnded(agentID, m.Timestamp)
		}
		closing := r.subagentClosingFrames(agentID, m)
		closing = append(closing, r.subagentWorkedFrames(agentID)...)
		if strings.TrimSpace(m.AgentType) == run.GoalCheckSubagentType {
			if r.tracker != nil {
				r.tracker.ObserveGoalCheck(false)
			}
			return EventResult{Frames: append(flushed, closing...)}
		}
		// The card task this end settles: the one its dispatching call names,
		// else the one this agent's execution was bound to. An end whose start
		// was never seen (a replay window that begins mid-execution) closes a
		// card of its own, already ended.
		fs, task := r.fanoutTaskForLifecycle(m.ParentToolCallID, m.TaskIndex)
		if task == nil {
			fs, task = r.cardTaskOf(agentID)
		}
		if task == nil {
			stepID := standaloneExecStepID(m.ExecutionID, m.TaskID)
			fs = r.findFanoutByStepID(stepID)
			if fs == nil {
				fs = &fanoutState{
					StepID: stepID,
					Verb:   "run",
					RunID:  r.activeRunID,
					Tasks: []FanoutTaskState{{
						Title:        firstNonEmpty(strings.TrimSpace(m.AgentType), "(empty task)"),
						SubagentType: strings.TrimSpace(m.AgentType),
						AgentID:      agentID,
						ExecutionID:  strings.TrimSpace(m.ExecutionID),
					}},
				}
				if r.fanoutStates == nil {
					r.fanoutStates = make(map[string]*fanoutState)
				}
				r.fanoutStates[stepID] = fs
			}
			task = &fs.Tasks[0]
		}
		settleFanoutTaskOnEnd(task, m, endStatus)
		card := r.emitFanoutFrame(fs)
		// The event's own clock settles everything this end touches: a task
		// with no start of its own (a dispatch never bound) still knows when
		// it stopped, and the rows following this execution end at that time.
		ended := m.FinishedAt
		if ended.IsZero() {
			ended = m.Timestamp
		}
		executionKey := firstNonEmpty(strings.TrimSpace(m.ExecutionID), agentID)
		if executionKey != "" {
			if r.endedExecutions == nil {
				r.endedExecutions = make(map[string]endedExecution)
			}
			r.endedExecutions[executionKey] = endedExecution{status: endStatus, at: ended}
		}
		// The query rows that reported this execution end with it, and the
		// agent's own in-flight query calls are canceled by its end.
		extra := r.settleQueryRowsOnExecutionEnd(executionKey, endStatus, ended)
		extra = append(extra, r.cancelInFlightQueryCards("", agentID)...)
		r.dropCardTaskOfAgent(agentID)
		return EventResult{Frames: append(append(append(flushed, closing...), card), extra...)}
	case PlanReviewStartedMsg:
		fs := r.openPlanReviewCard(m.ReviewID, m.Provider, m.Model, m.Label)
		return EventResult{Frames: []Frame{r.emitFanoutFrame(fs)}}
	case PlanReviewReviewedMsg:
		fs := r.openPlanReviewCard(m.ReviewID, m.Provider, m.Model, m.Label)
		task := &fs.Tasks[0]
		outcome := strings.TrimSpace(m.Outcome)
		switch outcome {
		case "done":
			if !fanoutTaskTerminal(task.Status) {
				task.Status = "done"
			}
		case "stopped":
			if !fanoutTaskTerminal(task.Status) {
				// The review was stopped before its reviewer started; once
				// one ran, its own ended event settles the task.
				fs.CallStatus = "canceled"
			}
		default: // "failed", "timed_out"
			if fanoutTaskTerminal(task.Status) {
				// The reviewer ran and ended; this only adds the review's own
				// account of why it did not produce one.
				task.Error = firstNonEmpty(strings.TrimSpace(m.Error), task.Error)
				break
			}
			// The reviewer never started: the request itself is what failed,
			// and the card says so without naming a subject.
			fs.CallError = strings.TrimSpace(m.Error)
			task.Status = "failed"
		}
		return EventResult{Frames: []Frame{r.emitFanoutFrame(fs)}}
	case SessionSwitchedMsg:
		frames := []Frame{}
		content := sessionSummary(m)
		// The reply a switched command produced is carried here — the
		// standalone NewMessageMsg for it was suppressed in the surface — so
		// exactly one frame carries it, and only after the switch succeeds
		// (the main loop holds EventResult frames until then).
		if reason := strings.TrimSpace(m.Reason); reason != "" && reason != content {
			content = reason
		}
		if !suppressStatusFrame("session switched", content) {
			frames = append(frames, Frame{Kind: FrameStatus, Title: "session switched", Content: content})
		}
		return EventResult{
			Frames:          frames,
			SwitchSessionID: strings.TrimSpace(m.SessionID),
		}
	// A picker a command asked for opens at once; it needs no line of its
	// own announcing it.
	case SessionSelectionRequestedMsg:
		return EventResult{RequestSessionSelect: true}
	case SlashPickerRequestedMsg:
		return EventResult{RequestPicker: m.Picker}
	case PermissionManagementRequestedMsg:
		return EventResult{RequestPermissionMgmt: true}
	case SkillSelectionRequestedMsg:
		return EventResult{RequestSkillSelect: true}
	case QuitRequestedMsg:
		return EventResult{
			Frames:        []Frame{{Kind: FrameStatus, Title: "quit", Content: strings.TrimSpace(m.Reason), Final: true}},
			QuitRequested: true,
		}
	default:
		return EventResult{}
	}
}

// subagentPromptFrames renders the dispatching agent's prompt in the
// subagent's own view. New writes deduplicate by execution identity, not prompt
// text: repeating the same continue instruction is still a distinct message.
func (r *Reducer) subagentPromptFrames(m SubagentSpawnedMsg) []Frame {
	agentID := strings.TrimSpace(m.AgentID)
	task := strings.TrimSpace(m.Task)
	if agentID == "" || task == "" {
		return nil
	}
	if r.lastAgentPrompt == nil {
		r.lastAgentPrompt = make(map[string]string)
	}
	promptKey := agentID + "\x00" + strings.TrimSpace(m.ExecutionID)
	if strings.TrimSpace(m.ExecutionID) == "" {
		// Legacy notifications had no execution id. Text is the only available
		// best-effort retransmission key for those records.
		promptKey = agentID + "\x00legacy\x00" + task
	}
	if r.lastAgentPrompt[promptKey] == task {
		return nil
	}
	r.lastAgentPrompt[promptKey] = task
	return []Frame{{
		Kind:    FrameUser,
		Content: task,
		RunID:   r.activeRunID,
		AgentID: agentID,
		Final:   true,
	}}
}

func (r *Reducer) upsertSubagentRoster(agentID, agentType, title, task, status string) {
	if r == nil {
		return
	}
	id := strings.TrimSpace(agentID)
	if id == "" {
		return
	}
	if r.agentRoster == nil {
		r.agentRoster = make(map[string]AgentRosterRow)
	}
	if _, seen := r.agentRoster[id]; !seen {
		r.agentRosterOrder = append(r.agentRosterOrder, id)
	}
	label := strings.TrimSpace(agentType)
	if label == "" {
		label = "subagent"
	}
	row := r.agentRoster[id]
	row.ID = id
	row.Kind = "subagent"
	row.Label = label
	if t := strings.TrimSpace(title); t != "" {
		row.Title = t
	}
	if t := strings.TrimSpace(task); t != "" {
		row.Task = t
	}
	if nextStatus := strings.TrimSpace(status); nextStatus != "" {
		row.Status = nextStatus
	}
	if row.Status == "" {
		row.Status = "running"
	}
	r.agentRoster[id] = row
}

func (r *Reducer) updateSubagentStatus(agentID, status string) {
	if r == nil || r.agentRoster == nil {
		return
	}
	id := strings.TrimSpace(agentID)
	row, ok := r.agentRoster[id]
	if !ok {
		return
	}
	row.Status = strings.TrimSpace(status)
	r.agentRoster[id] = row
}

func (r *Reducer) formatWorkedStatusTitle(workDuration time.Duration, runID string) string {
	parts := []string{workedForLabel(workDuration)}
	if r != nil && r.tracker != nil {
		if counters := formatWorkingCounters(r.tracker.SnapshotRun(runID)); counters != "" {
			parts = append(parts, counters)
		}
	}
	parts = append(parts, workedCompletionTime(time.Now()))
	return strings.Join(parts, " · ")
}

// workedCompletionTime renders the local wall-clock minute at which a run
// closed. A "Worked for" line says how long the turn took and what it spent;
// the hour it finished in is the one fact about the moment that neither of
// those figures carries, and the next line down the transcript may start hours
// later.
func workedCompletionTime(t time.Time) string {
	return t.Format("15:04")
}

// shortestActiveTaskTitle picks the title the working line reports for a
// checklist with tasks in flight. Several items can be in progress at once,
// but the line has room for one title, not a list, and the shortest is the
// most legible stand-in; ties keep the first. A checklist with no in-progress
// item falls back to the payload's own summary line. The rule itself —
// including an item's in-progress label taking precedence over its checklist
// text — lives in pkg/event, so the web surface's worked line reports the
// same title; this is the terminal's forwarding to it.
func shortestActiveTaskTitle(items []event.PlanUpdateItem, fallback string) string {
	return event.PlanProgressOf(items, 0, 0, fallback).Active
}

// workedForLabel renders a finished run's duration. The renderer paints a
// status whose title starts with it as a full-width rule, which is what closes
// a transcript.
func workedForLabel(elapsed time.Duration) string {
	return "Worked for " + formatElapsedDuration(elapsed)
}

// formatThoughtDurationLabel renders the elapsed thinking time for the "Thought"
// disclosure header, e.g. "Thought for 1m 10s". Returns empty when elapsed is
// zero or negative so the header falls back to plain "Thought".
func formatThoughtDurationLabel(elapsed time.Duration) string {
	if elapsed <= 0 {
		return ""
	}
	return "Thought for " + formatElapsedDuration(elapsed)
}

// formatElapsedDuration renders a duration as "42s", "9m 09s", or "1h 02m 03s".
// Sub-second durations round up to 1s so the label never reads "0s".
func formatElapsedDuration(elapsed time.Duration) string {
	totalSeconds := int(elapsed / time.Second)
	if totalSeconds == 0 && elapsed > 0 {
		totalSeconds = 1
	}
	seconds := totalSeconds % 60
	totalMinutes := totalSeconds / 60
	if totalMinutes == 0 {
		return strconv.Itoa(seconds) + "s"
	}
	minutes := totalMinutes % 60
	hours := totalMinutes / 60
	if hours == 0 {
		return strconv.Itoa(totalMinutes) + "m " + fmt.Sprintf("%02ds", seconds)
	}
	return strconv.Itoa(hours) + "h " + fmt.Sprintf("%02dm %02ds", minutes, seconds)
}

func suppressStatusFrame(title, content string) bool {
	title = strings.ToLower(strings.TrimSpace(title))
	content = strings.ToLower(strings.TrimSpace(content))
	switch title {
	case "run started", "run ended", "token budget", "context compacted", "stream reset":
		return true
	}
	if strings.Contains(content, "input tokens updated") {
		return true
	}
	return false
}

func (r *Reducer) reduceMessage(msg Message) []Frame {
	switch msg.Kind {
	case MsgKindAssistant:
		// Assistant messages arrive as incremental chunks during a run, and the
		// answer has to appear as it streams rather than all at once when the
		// run ends. Same technique as reasoning below: accumulate, then emit the
		// FULL accumulated text as a live (non-final) frame so the renderer
		// re-wraps and redraws one block in place. Emitting the delta alone
		// would print one fragment per line.
		//
		// The buffer is still what flushBufferedText seals into the final frame,
		// which replaces this live block rather than adding a second one.
		content := strings.ReplaceAll(msg.Content, "\r", "")
		if content == "" {
			return nil
		}
		agentID := strings.TrimSpace(msg.AgentID)
		// Subagent assistant text (including fanout tasks) buffers under its own
		// AgentID key; flushed on SubagentEndedMsg to the per-agent view.
		buf := r.agentBuf(agentID)
		out := r.flushReasoningFinal(buf, msg.Timestamp)
		buf.assistant.WriteString(content)
		buf.sawAssistant = true
		text := strings.TrimSpace(buf.assistant.String())
		if text == "" {
			return out
		}
		runID := r.activeRunID
		if rid := strings.TrimSpace(msg.RunID); rid != "" {
			runID = rid
		}
		return append(out, Frame{
			Kind:    FrameAssistant,
			Content: text,
			RunID:   runID,
			AgentID: agentID,
		})
	case MsgKindReasoning:
		content := strings.ReplaceAll(msg.Content, "\r", "")
		if content == "" {
			return nil
		}
		agentID := strings.TrimSpace(msg.AgentID)
		buf := r.agentBuf(agentID)
		// A block of one kind ends when a block of the other kind starts, in
		// either direction; the tool frame that usually sits between them is
		// not what ends the text. Sealing the streamed answer here is what
		// keeps the next text block its own block instead of a repeat of
		// everything said so far.
		out := r.flushAssistantFinal(buf)
		runID := r.activeRunID
		if rid := strings.TrimSpace(msg.RunID); rid != "" {
			runID = rid
		}
		if buf.reasoning.Len() == 0 {
			buf.reasoningStartedAt = msg.Timestamp
			if buf.reasoningStartedAt.IsZero() {
				buf.reasoningStartedAt = time.Now()
			}
		}
		buf.reasoning.WriteString(content)
		text := strings.TrimSpace(buf.reasoning.String())
		if text == "" {
			return out
		}
		// Pass the full accumulated reasoning as Content (not just this delta)
		// so the renderer re-wraps and redraws the whole thinking block. Sending
		// only the delta makes token-level streaming print one fragment per line.
		return append(out, Frame{
			Kind:    FrameThinking,
			Content: text,
			RunID:   runID,
			AgentID: agentID,
		})
	case MsgKindUser:
		buf := r.agentBuf("")
		return r.withFlushedBuffers(buf, r.buildFrame(FrameUser, "you", msg), msg.Timestamp)
	case MsgKindTool:
		stepID := strings.TrimSpace(msg.StepID)
		outputKey := liveToolOutputKey(msg)
		if msg.ToolOutputDelta {
			if r.toolOutput == nil {
				r.toolOutput = make(map[string]string)
			}
			r.toolOutput[outputKey] = appendLiveToolOutput(r.toolOutput[outputKey], msg.Content)
			msg.Content = r.toolOutput[outputKey]
		} else if msg.ToolPhase == event.RunEventToolStarted {
			delete(r.toolOutput, outputKey)
		} else if msg.ToolPhase == event.RunEventToolCompleted ||
			!strings.EqualFold(strings.TrimSpace(msg.ToolMeta.Status), "running") {
			delete(r.toolOutput, outputKey)
		}
		if r.tracker != nil && !msg.ToolOutputDelta {
			r.tracker.ObserveToolStep(strings.TrimSpace(msg.AgentID), stepID, strings.TrimSpace(msg.ToolName), strings.TrimSpace(msg.FilePath))
		}
		toolName := strings.TrimSpace(msg.ToolName)
		// A subagent_* call is a card, whatever its verb and whoever called it —
		// a nested dispatch from inside a subagent included. The facts on the
		// meta are the card; a call with no task facts yet (subagent_list at its
		// start) draws the header alone.
		if msg.ToolMeta.SubagentCall != nil {
			return r.handleSubagentToolMessage(msg)
		}
		agentID := strings.TrimSpace(msg.AgentID)
		if agentID != "" && strings.EqualFold(strings.TrimSpace(msg.ToolMeta.Category), "approval") {
			decision := strings.ToLower(strings.TrimSpace(msg.ToolMeta.Status))
			switch decision {
			case "waiting approval", "pending":
				r.updateSubagentStatus(agentID, "waiting_approval")
			case "cancelled":
				r.updateSubagentStatus(agentID, "cancelled")
			case "error", "expired":
				r.updateSubagentStatus(agentID, "failed")
			default:
				r.updateSubagentStatus(agentID, "running")
			}
			// An approval is the gate on a call, not a call of its own. It moves
			// the roster row and nothing else: the subagent's view already shows
			// the gated call as running, and the answer arrives there as the same
			// "✔ You approved …" confirmation the conversation gets for its own
			// approvals. A card of its own carried no tool name or input, so it
			// painted a nameless "Ran" block beside the call it was gating.
			return nil
		}
		// A subagent's own tool calls fold into the card task its execution is
		// bound to. Emit AgentID-tagged FrameTool for the per-agent view, then
		// re-emit the card for the surface that shows it.
		if fs, task := r.cardTaskOf(agentID); task != nil {
			appendFanoutTaskTool(task, stepID, fanoutToolLabel(msg), strings.TrimSpace(msg.ToolPhase))
			frames := r.withFlushedBuffers(r.agentBuf(agentID), r.buildFrame(FrameTool, toolName, msg), msg.Timestamp)
			return append(frames, r.emitFanoutFrame(fs))
		}
		buf := r.agentBuf(agentID)
		return r.withFlushedBuffers(buf, r.buildFrame(FrameTool, toolName, msg), msg.Timestamp)
	case MsgKindError:
		buf := r.agentBuf("")
		return r.withFlushedBuffers(buf, r.buildFrame(FrameError, firstNonEmpty(msg.Title, "error"), msg), msg.Timestamp)
	case MsgKindPlan:
		buf := r.agentBuf("")
		return r.withFlushedBuffers(buf, r.buildFrame(FramePlan, "plan", msg), msg.Timestamp)
	default:
		buf := r.agentBuf("")
		return r.withFlushedBuffers(buf, r.buildFrame(FrameSystem, firstNonEmpty(msg.Title, "system"), msg), msg.Timestamp)
	}
}

// subagentClosingFrames renders how a subagent's run ended, tagged with its
// roster key so it lands in that subagent's own view.
//
// Without it a failed subagent's view simply stops after its last assistant
// message: the reason it stopped lives only on the fanout card or the
// lifecycle card in the primary transcript, which is the one place a user who
// has just opened the agent to find out what went wrong is not looking. A run
// that ended cleanly needs no such frame — its own final answer is the ending.
func (r *Reducer) subagentClosingFrames(agentID string, m SubagentEndedMsg) []Frame {
	agentID = strings.TrimSpace(agentID)
	errText := strings.TrimSpace(m.Error)
	status := strings.ToLower(strings.TrimSpace(m.Status))
	if agentID == "" || (errText == "" && status != "failed" && status != "cancelled") {
		return nil
	}
	title := "subagent failed"
	if status == "cancelled" {
		title = "subagent cancelled"
	}
	content := errText
	if content == "" {
		content = "The subagent stopped before it produced an answer."
	}
	return []Frame{{
		Kind:    FrameError,
		Title:   title,
		Content: content,
		RunID:   r.activeRunID,
		AgentID: agentID,
		Final:   true,
	}}
}

// subagentWorkedFrames closes a subagent's own view the way the turn-final
// status closes the conversation's: how long that agent worked and what it
// spent doing so. It is that view's only account of its own run — the footer
// names the agent, and the live "Working" line is gone the moment the run ends
// — so unlike the conversation's, it is not suppressed for a short run. Empty
// for an agent whose start was never observed, because there is nothing true to
// say about one.
func (r *Reducer) subagentWorkedFrames(agentID string) []Frame {
	if r == nil || r.tracker == nil || strings.TrimSpace(agentID) == "" {
		return nil
	}
	snap, ok := r.tracker.SnapshotAgentRun(agentID)
	if !ok {
		return nil
	}
	title := workedForLabel(snap.Elapsed)
	if counters := formatWorkingCounters(snap.Counters); counters != "" {
		title += " · " + counters
	}
	title += " · " + workedCompletionTime(time.Now())
	return []Frame{{
		Kind:    FrameStatus,
		Title:   title,
		RunID:   r.activeRunID,
		AgentID: agentID,
		Final:   true,
	}}
}

// buildFrame assembles a Frame from a Message.
func (r *Reducer) buildFrame(kind FrameKind, title string, msg Message) Frame {
	runID := r.activeRunID
	if rid := strings.TrimSpace(msg.RunID); rid != "" {
		runID = rid
	}
	stepID := strings.TrimSpace(msg.StepID)
	summary := strings.TrimSpace(msg.Summary)
	f := Frame{
		Kind:            kind,
		Title:           title,
		Content:         strings.TrimSpace(msg.Content),
		StepID:          stepID,
		RunID:           runID,
		Final:           true,
		AgentID:         strings.TrimSpace(msg.AgentID),
		Summary:         summary,
		ToolMeta:        msg.ToolMeta,
		FilePath:        strings.TrimSpace(msg.FilePath),
		Duration:        msg.Duration,
		StreamingOutput: msg.ToolOutputDelta,
		RetainAsHistory: msg.RetainAsHistory,
	}
	return f
}

// flushBufferedText returns buffered reasoning and assistant frames for the
// given buffer, then resets it. Returns nil when nothing has accumulated.
func (r *Reducer) flushBufferedText(buf *agentBuffer, at ...time.Time) []Frame {
	if buf == nil {
		return nil
	}
	out := make([]Frame, 0, 3)
	out = append(out, r.flushReasoningFinal(buf, at...)...)
	out = append(out, r.flushAssistantFinal(buf)...)
	return out
}

// withFlushedBuffers returns the buffered assistant text (if any) as a final
// FrameAssistant immediately preceding the supplied frame, then resets the
// buffer. Preserves the order in which the LLM streamed text vs other
// interleaved messages (tool calls, errors, plans).
func (r *Reducer) withFlushedBuffers(buf *agentBuffer, next Frame, at ...time.Time) []Frame {
	return append(r.flushBufferedText(buf, at...), next)
}

func (r *Reducer) flushReasoningFinal(buf *agentBuffer, at ...time.Time) []Frame {
	if buf == nil {
		return nil
	}
	text := strings.TrimSpace(buf.reasoning.String())
	if text == "" {
		return nil
	}
	buf.reasoning.Reset()
	duration := time.Duration(0)
	if !buf.reasoningStartedAt.IsZero() {
		endedAt := time.Now()
		if len(at) > 0 && !at[0].IsZero() {
			endedAt = at[0]
		}
		duration = endedAt.Sub(buf.reasoningStartedAt)
		if duration < 0 {
			duration = 0
		}
		buf.reasoningStartedAt = time.Time{}
	}
	return []Frame{{
		Kind:     FrameThinking,
		Content:  text,
		RunID:    r.activeRunID,
		Final:    true,
		Duration: duration,
		AgentID:  buf.agentID,
	}}
}

// flushAssistantFinal seals buffered assistant text into one final frame and
// clears it — the mirror of flushReasoningFinal. A block of one kind ends
// when a block of the other kind starts, in either direction; the tool frame
// that usually sits between them is not what ends the text.
func (r *Reducer) flushAssistantFinal(buf *agentBuffer) []Frame {
	if buf == nil {
		return nil
	}
	text := strings.TrimSpace(buf.assistant.String())
	if text == "" {
		return nil
	}
	buf.assistant.Reset()
	return []Frame{{
		Kind:    FrameAssistant,
		Content: text,
		RunID:   r.activeRunID,
		Final:   true,
		AgentID: buf.agentID,
	}}
}

// renderFanoutContent builds the card's body and, alongside it, which
// subagent owns each content line and where each task row's clock sits. The
// three are built in one pass on purpose: the ownership map is what turns a
// click on a task row into "open that subagent's view", and the clock is what
// the renderer turns into the row's live or final elapsed — deriving either
// separately would let it drift out of step with the text the user is actually
// looking at.
//
// lineAgents[i] is the roster key of the subagent whose task produced content
// line i, or "" for a line that belongs to no single agent. FanoutLineClocks
// aligns the same way; only task title lines carry a clock.
func renderFanoutContent(fs *fanoutState) (string, []string, []fanoutLineClock) {
	lines := make([]string, 0, len(fs.Tasks)*2)
	lineAgents := make([]string, 0, len(fs.Tasks)*2)
	clocks := make([]fanoutLineClock, 0, len(fs.Tasks)*2)
	for i := range fs.Tasks {
		t := &fs.Tasks[i]
		agentID := strings.TrimSpace(t.AgentID)
		if fs.Query && strings.TrimSpace(t.Title) == "" {
			// A query row the call could not name yet: the result will, and a
			// row that says only an id says nothing.
			continue
		}
		// Every element of lines is exactly one content line, and lineAgents
		// and clocks are indexed by that same line number: the renderer splits
		// the joined content on newlines, reads each line's role from its
		// leading whitespace, and resolves a click through that index. Text
		// that carries its own line breaks — a described provider failure is
		// several sentences — therefore becomes one prefixed line per break
		// here. Added whole, its later sentences reached the renderer without
		// the indent that marks a continuation, so they were drawn as new "└"
		// task rows and every following line reported the wrong owner.
		add := func(prefix, text string) {
			for _, line := range strings.Split(text, "\n") {
				lines = append(lines, prefix+strings.TrimRight(line, "\r"))
				lineAgents = append(lineAgents, agentID)
				clocks = append(clocks, fanoutLineClock{})
			}
		}
		head := strings.Builder{}
		switch t.Status {
		case "done":
			head.WriteString("\x1b[32m✓\x1b[0m ")
		case "failed":
			head.WriteString("\x1b[31m✗\x1b[0m ")
		case "cancelled":
			head.WriteString("\x1b[31m×\x1b[0m ")
		case "skipped":
			head.WriteString("\x1b[33m!\x1b[0m ")
		}
		// The task's name is SubagentTaskTitle's answer — one line by
		// construction — so the head row writes it as it stands. A dispatch
		// task with no prompt was skipped before it had one.
		head.WriteString(firstNonEmpty(strings.TrimSpace(t.Title), "(empty task)"))
		lines = append(lines, head.String())
		lineAgents = append(lineAgents, agentID)
		clocks = append(clocks, fanoutLineClock{Start: t.StartedAt, End: t.EndedAt})
		if fs.Query {
			if phrase := fanoutQueryPhrase(fs, t); phrase != "" {
				add("  · ", phrase)
			}
		}
		// Reason line. The indent is repeated on every sentence so the whole
		// reason reads as one continuation of the task above it.
		if t.Error != "" && fanoutTaskTerminal(t.Status) {
			add("  ", t.Error)
		}
		// Live tool progress (third layer): the most recent tool label.
		overflow := t.ToolTotal - len(t.RecentTools)
		for _, tool := range t.RecentTools {
			add("  └ ", singleDisplayLine(tool.Label))
		}
		if t.ToolTotal > 0 && overflow > 0 {
			add("    ", fmt.Sprintf("… +%d tool uses", overflow))
		}
	}
	if len(lines) == 0 {
		return "", nil, nil
	}
	// The trailing newline the previous formatting emitted is preserved so the
	// rendered block keeps its final empty content line.
	return strings.Join(lines, "\n") + "\n", lineAgents, clocks
}

// fanoutQueryPhrase is the one-line status phrase under a query card's task
// row: only what the call's moment can mean — a wait that ended with the task
// still running, a stop that was asked for. The icon and the clock already say
// running, done, failed and cancelled.
func fanoutQueryPhrase(fs *fanoutState, t *FanoutTaskState) string {
	parts := make([]string, 0, 2)
	if fs.Verb == "list" && strings.TrimSpace(t.SubagentType) != "" {
		parts = append(parts, strings.TrimSpace(t.SubagentType))
	}
	if t.TimedOut {
		parts = append(parts, "still running when the wait ended")
	}
	if t.StopRequested {
		parts = append(parts, "stop requested")
	}
	return strings.Join(parts, " · ")
}

// singleDisplayLine folds text that arrives with its own line breaks onto one
// line, joining what the breaks separated with a single space.
//
// Every caller renders into a row of a card, and those rows are line-oriented:
// a row's role is encoded in its leading whitespace and per-row data is indexed
// by line number, so a value that is semantically one row must not carry a
// newline into it. A column budget is meaningless across a line break as well —
// the second line starts over at column zero — so folding first is what makes
// truncation measure the text the reader actually sees.
func singleDisplayLine(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
	out := parts[:0]
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, " ")
}

// truncateForDisplay shortens s to at most maxLen terminal columns, ending it
// with an ellipsis when it had to cut.
//
// Every caller spends maxLen as a column budget (a task title beside a fixed
// label, a query inside a card), so columns — not bytes — is the unit that
// makes those budgets come out right. Counting bytes went wrong twice: it cut
// multi-byte characters mid-rune, which the terminal draws as a replacement
// glyph, and it under-filled the budget for wide text, since CJK costs three
// bytes per two columns — a title got barely a third of the width it was given.
//
// Input that is already invalid UTF-8 is repaired rather than passed along:
// these strings come from tool output and model text that something upstream
// may have byte-sliced, and runewidth measures a stray byte as zero columns,
// which would silently skew the budget it is asked to enforce.
// displayLine normalizes a value for a tool header: one logical line, valid
// UTF-8, and no length cap.
//
// A header segment that carries the call's own arguments must use this rather
// than truncateForDisplay. wrapToolDisplayLine already folds a long header onto
// continuation rows, so there is nothing to gain from cutting the tail off —
// and a reader who cannot see the last of the arguments cannot tell what the
// call actually asked for.
func displayLine(s string) string {
	s = singleDisplayLine(strings.TrimSpace(s))
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return s
}

func truncateForDisplay(s string, maxLen int) string {
	return runewidth.Truncate(displayLine(s), maxLen, "…")
}

func sessionSummary(m SessionSwitchedMsg) string {
	if title := strings.TrimSpace(m.Title); title != "" {
		return title
	}
	if id := strings.TrimSpace(m.SessionID); id != "" {
		return id
	}
	return strings.TrimSpace(m.Reason)
}

// viewport.go turns the retained viewModel into screen lines for the virtual
// viewport: it renders each block to its full set of lines (reusing the legacy
// renderers verbatim — only the OUTPUT SHAPE changes, from "print to terminal"
// to "return []string"), applies collapse/expand folding, concatenates the
// whole transcript, and slices out the window visible at the current scroll
// offset. It also maps a clicked screen row back to the block under it
// (blockAtRow), the 1-D equivalent of CC's ink/hit-test.ts DOM walk.

const liveToolOutputMaxLines = 12

const (
	// Status markers for tool blocks.
	toolRunning  = "○"
	toolComplete = "●"
	// Status markers for thinking blocks.
	thinkRunning  = "△"
	thinkComplete = "▲"
)

// lineSpan records, in ABSOLUTE row coordinates over the full transcript, the
// rows one block occupies. It is the 1-D analogue of CC's per-element rect in
// nodeCache: blockAtRow maps a row to the owning block via these spans.
type lineSpan struct {
	blockID   int
	startRow  int // absolute index of the block's first row in the full list
	rowCount  int // number of rows the block occupies
	headerRow int // absolute row of the clickable header, or -1 if not clickable
}

// viewportRender is one frame's worth of viewport output: the visible line
// slice plus the metadata needed to hit-test clicks and clamp scrolling.
type viewportRender struct {
	lines      []string   // visible, already scrolled + height-clamped rows
	allLines   []string   // full transcript rows (index == absolute row)
	spans      []lineSpan // per-block absolute-row spans (full transcript)
	totalLines int        // full transcript height (scroll range)
	firstRow   int        // absolute index of lines[0] (clamped scrollOffset)
	height     int        // viewport body height these lines were sliced to
}

// renderViewport renders every block to its (cached) full lines, folds each per
// its expand state, concatenates the transcript, and returns the window
// [scrollOffset, scrollOffset+height). scrollOffset is clamped to a valid range;
// the effective top is returned as firstRow so the caller can keep its own
// offset in sync (e.g. after content growth shrinks the max offset).
//
// Heavy per-block rendering (markdown, diff cards) is memoized in each block's
// cache, so a redraw triggered by scrolling, a click on another block, or a new
// frame only re-renders blocks whose width / content / fold state changed.
// Concatenating cached string slices for the off-screen blocks is cheap at the
// block counts a single session produces.
func renderViewport(m *viewModel, width, height, scrollOffset int, theme DiffTheme, cwdOpt ...string) viewportRender {
	cwd := ""
	if len(cwdOpt) > 0 {
		cwd = strings.TrimSpace(cwdOpt[0])
	}
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 1
	}
	// Pre-allocate 'all' slice based on block count to reduce reallocation.
	// Typical block averages ~8 lines. Start with a reasonable estimate
	// capped at 8192 to balance memory vs reallocation for very large sessions.
	estimatedLines := len(m.blocks) * 8
	if estimatedLines < 256 {
		estimatedLines = 256
	} else if estimatedLines > 8192 {
		estimatedLines = 8192
	}
	all := make([]string, 0, estimatedLines)
	spans := make([]lineSpan, 0, len(m.blocks))
	visibleBlocks := 0
	// The painter stamps the current spinner phase on the model each repaint;
	// thread it through so running-tool markers animate.
	spinnerPhase := m.spinnerPhase
	for _, b := range m.blocks {
		// User message cards (the "›"-prefixed shared block) are full-bleed:
		// they span the true terminal width so the card background and its
		// top/bottom rules reach the right edge. Other transcript content
		// keeps viewportRightPadding so readable text stays off the edge.
		blockWidth := width
		if b.frame.Kind == FrameUser {
			blockWidth = width + viewportRightPadding
		}
		full := renderBlockFull(b, blockWidth, theme, spinnerPhase, cwd)
		displayed, headerLocal := foldBlock(b, full, blockWidth, spinnerPhase, cwd)
		if len(displayed) == 0 {
			continue
		}
		// Add blank separator between blocks for visual breathing room.
		if visibleBlocks > 0 {
			all = append(all, "")
		}
		visibleBlocks++
		start := len(all)
		headerRow := -1
		if headerLocal >= 0 {
			headerRow = start + headerLocal
		}
		spans = append(spans, lineSpan{
			blockID:   b.id,
			startRow:  start,
			rowCount:  len(displayed),
			headerRow: headerRow,
		})
		all = append(all, displayed...)
	}

	total := len(all)
	maxOffset := maxInt(0, total-height)
	if scrollOffset > maxOffset {
		scrollOffset = maxOffset
	}
	if scrollOffset < 0 {
		scrollOffset = 0
	}
	end := scrollOffset + height
	if end > total {
		end = total
	}
	visible := []string{}
	if scrollOffset < total {
		visible = all[scrollOffset:end]
	}
	return viewportRender{
		lines:      visible,
		allLines:   all,
		spans:      spans,
		totalLines: total,
		firstRow:   scrollOffset,
		height:     height,
	}
}

// blockAtRow maps a 0-based SCREEN row (row within the rendered viewport body)
// to the block under it. isHeader reports whether that exact row is the block's
// clickable disclosure header (so the caller toggles expand only on a header
// click, leaving body rows free for text selection). Returns (-1,false) for a
// click on a blank gap or below the content.
func (vr viewportRender) blockAtRow(screenRow int) (blockID int, isHeader bool) {
	if screenRow < 0 {
		return -1, false
	}
	abs := vr.firstRow + screenRow
	for _, s := range vr.spans {
		if abs >= s.startRow && abs < s.startRow+s.rowCount {
			return s.blockID, s.headerRow >= 0 && abs == s.headerRow
		}
	}
	return -1, false
}

// maxScrollOffset is the largest valid scrollOffset for this render (top of the
// transcript's last full screen). Used to clamp wheel / follow scrolling.
func (vr viewportRender) maxScrollOffset() int {
	return maxInt(0, vr.totalLines-vr.height)
}

// toggleAtScreenRow resolves a 0-based screen row against this render and, when
// it lands on a collapsible block, flips that block's expand state in m.
// Returns true if a block was toggled (the caller redraws).
//
// Short blocks (≤ toolOutputMaxLines rows) render inline without a header and
// are NOT toggleable — their full content is already visible. Long tool,
// finalized thinking, and compact blocks toggle on ANY row within the block —
// the whole preview area is clickable, matching the "click preview to expand"
// interaction promised by the truncation hint. Blank gaps always return false.
func (m *viewModel) toggleAtScreenRow(vr viewportRender, screenRow int) bool {
	blockID, isHeader := vr.blockAtRow(screenRow)
	if blockID < 0 {
		return false
	}
	b := m.blockByID(blockID)
	if b == nil || !b.collapsible {
		return false
	}
	span, ok := vr.spanForBlock(blockID)
	if !ok || span.headerRow < 0 {
		return false
	}
	if isHeader {
		// Header is a static status indicator for tool/thinking/memory-recall blocks.
		if b.frame.Kind == FrameTool || b.frame.Kind == FrameThinking {
			return false
		}
	} else {
		kind := b.frame.Kind
		fullBodyClickable := kind == FrameTool ||
			kind == FrameMemoryCompact ||
			(kind == FrameThinking && b.frame.Final)
		if !fullBodyClickable {
			return false
		}
	}
	b.expanded = !b.expanded
	return true
}

// agentAtScreenRow reports which subagent the block under this row belongs to.
//
// A subagent card is the conversation's durable account of that agent — the
// roster row disappears when the subagent finishes, but the card stays — and a
// nested card inside a subagent's view is that view's account of a
// grandchild. So a fanout block answers by the row that was hit first (which
// grandchild's task it is) and only then by the block's own owner.
func (m *viewModel) agentAtScreenRow(vr viewportRender, screenRow int) (string, bool) {
	blockID, _ := vr.blockAtRow(screenRow)
	if blockID < 0 {
		return "", false
	}
	b := m.blockByID(blockID)
	if b == nil {
		return "", false
	}
	if b.frame.Kind == FrameFanout {
		if agentID, ok := b.agentAtLocalRow(vr, screenRow); ok {
			return agentID, true
		}
	}
	if agentID := strings.TrimSpace(b.frame.AgentID); agentID != "" {
		return agentID, true
	}
	return "", false
}

// agentAtLocalRow resolves a screen row to the subagent owning that row within
// this block. Only multi-agent blocks (fanout) carry the per-line map; blocks
// without one report nothing.
func (b *viewBlock) agentAtLocalRow(vr viewportRender, screenRow int) (string, bool) {
	if len(b.cache.lineAgents) == 0 {
		return "", false
	}
	span, ok := vr.spanForBlock(b.id)
	if !ok {
		return "", false
	}
	local := vr.firstRow + screenRow - span.startRow
	if local < 0 || local >= len(b.cache.lineAgents) {
		return "", false
	}
	agentID := strings.TrimSpace(b.cache.lineAgents[local])
	if agentID == "" {
		return "", false
	}
	return agentID, true
}

// spanForBlock returns the lineSpan for the given block id, or false if not
// found in this render.
func (vr viewportRender) spanForBlock(blockID int) (lineSpan, bool) {
	for _, s := range vr.spans {
		if s.blockID == blockID {
			return s, true
		}
	}
	return lineSpan{}, false
}

// isClickableRow reports whether the given 0-based screen row is part of a
// clickable block, i.e. hovering it should show a highlight. Returns true for
// any row inside a tool, finalized thinking, or compact block that has a
// disclosure header (long content).
func (vr viewportRender) isClickableRow(screenRow int, vm *viewModel) bool {
	blockID, isHeader := vr.blockAtRow(screenRow)
	if blockID < 0 {
		return false
	}
	// A row that opens a subagent's view is clickable regardless of folding: the
	// lifecycle card for a single subagent, or one task row inside a fanout.
	if b := vm.blockByID(blockID); b != nil {
		if strings.TrimSpace(b.frame.AgentID) != "" && b.frame.Kind == FrameStatus {
			return true
		}
		if _, ok := b.agentAtLocalRow(vr, screenRow); ok {
			return true
		}
	}
	span, ok := vr.spanForBlock(blockID)
	if !ok || span.headerRow < 0 {
		return false
	}
	b := vm.blockByID(blockID)
	if b == nil || !b.collapsible {
		return false
	}
	kind := b.frame.Kind
	if kind == FrameMemoryCompact {
		// Both the header and the truncated checkpoint preview toggle.
		return true
	}
	if kind == FrameTool || (kind == FrameThinking && b.frame.Final) {
		return !isHeader
	}
	return false
}

// foldBlock applies the block's collapse state to its full rendered lines,
// returning the lines to display and the LOCAL index (within those lines) of
// the clickable header, or -1 when the block is not clickable this frame.
//
//   - non-collapsible kinds (assistant, user, status): full lines, no header.
//   - thinking, still streaming (!Final): full lines, no header — reasoning is
//     visible live and only becomes foldable once the model finalizes it.
//   - thinking, final, at/under toolOutputMaxLines rows: "Thought" header +
//     full body inline, not clickable (full content already visible).
//   - thinking, final, over toolOutputMaxLines rows: "Thought" disclosure
//     header; preview (default) shows header + up to toolOutputMaxLines body +
//     "… +N lines" hint; expanded shows header + full body.
//   - tool blocks: renderCompactFrame already emits a "• title summary" header
//     as full[0]. At/under toolOutputMaxLines body rows the full output
//     (header + body) is shown inline, not clickable. Over toolOutputMaxLines
//     body rows: disclosure header; preview (default) shows header + up to
//     toolOutputMaxLines body + "… +N lines" hint; expanded shows header +
//     full body.
//
// toolSpecificMaxLines returns the maximum rendered body rows before a tool
// block's output gets folded behind a clickable disclosure header. Most tools
// use the global toolOutputMaxLines (3); write_file and edit_file
// use 30 so their larger diff output stays visible without a click-to-expand.
// Memory query results are rendered Markdown, whose blank separator rows would
// leave a 3-row preview showing only a heading, so they get a middle budget.
func toolSpecificMaxLines(title string) int {
	if strings.EqualFold(title, "write_file") ||
		strings.EqualFold(title, "edit_file") {
		return 30
	}
	if isMemoryQueryTool(title) {
		return 10
	}
	return toolOutputMaxLines
}

// toolPreviewUsesRenderedBody reports whether a tool's collapsed preview must
// be sliced from the already-rendered body lines instead of being reconstructed
// from logical/plain output lines. These tools have rich renderers in
// renderCompactFrame (Markdown or diff cards); rebuilding their preview with
// formatToolOutputBlock would downgrade the collapsed output to plain text.
func toolPreviewUsesRenderedBody(f Frame) bool {
	if f.Kind != FrameTool {
		return false
	}
	title := strings.TrimSpace(f.Title)
	return strings.EqualFold(title, intermediateToolName) ||
		strings.EqualFold(title, "memories_add_ad_hoc_note") ||
		strings.EqualFold(title, "retrieve_output") ||
		isMemoryQueryTool(title) ||
		isMCPToolTitle(title) || isDiffTool(title)
}

func foldBlock(b *viewBlock, full []string, width int, spinnerPhase int, cwdOpt ...string) (lines []string, headerLocal int) {
	// Memoize the fold. foldBlock runs for every block on every repaint, and
	// for a session with thousands of blocks the per-frame cost (a per-line
	// ANSI-strip regex in toolBodyLines plus a header rebuild/wrap per block)
	// dominates rendering. The output depends only on the cached full lines
	// (tracked via b.cache.gen, which bumps on any content/spinner change), the
	// width, cwd, and the expanded toggle, so it is safe to return the cached result
	// when those are unchanged. b.cache.gen was just refreshed by renderBlockFull.
	cwd := ""
	if len(cwdOpt) > 0 {
		cwd = strings.TrimSpace(cwdOpt[0])
	}
	fc := &b.foldCache
	if fc.valid && fc.width == width && fc.cwd == cwd && fc.expanded == b.expanded && fc.gen == b.cache.gen {
		return fc.lines, fc.headerLocal
	}
	defer func() {
		// The painter redraws any of these rows on its own, so every row must
		// open the style it is drawn in and end in the default state — the
		// wrapped coloured header used to leave its continuations relying on
		// the row above. Normalising here caches the fixed rows, so this runs
		// once per block per input change, not per frame.
		lines = selfContainedRows(lines)
		fc.valid = true
		fc.width = width
		fc.cwd = cwd
		fc.expanded = b.expanded
		fc.gen = b.cache.gen
		fc.lines = lines
		fc.headerLocal = headerLocal
	}()
	if !b.collapsible {
		return full, -1
	}
	if b.frame.Kind == FrameThinking {
		if !b.frame.Final {
			return full, -1
		}
		// thinkingDisplayLinesWithWidth bakes a "▸ " prefix into the first
		// line for the headerless streaming case. When we render with a
		// "Thought" header, replace it with "  " so there is no double
		// marker. Copy first to avoid mutating the cached lines.
		body := make([]string, len(full))
		copy(body, full)
		if len(body) > 0 && strings.Contains(body[0], "▸ ") {
			body[0] = strings.Replace(body[0], "▸ ", "  ", 1)
		}
		marker := statusMarker(b.frame, spinnerPhase)
		thoughtLabel := formatThoughtDurationLabel(b.frame.Duration)
		if thoughtLabel == "" {
			thoughtLabel = "Thought"
		}
		header := lipgloss.NewStyle().Foreground(lipgloss.Color(reasoningHeaderColor)).Render(marker + " " + thoughtLabel)
		if len(full) <= toolOutputMaxLines {
			// Short: header + full body, not clickable.
			out := make([]string, 0, len(body)+2)
			out = append(out, header)
			out = append(out, body...)
			return out, -1
		}
		if b.expanded {
			out := make([]string, 0, len(body)+1)
			out = append(out, header)
			out = append(out, body...)
			return out, 0
		}
		previewLines := min(len(body), toolOutputMaxLines)
		out := make([]string, 0, 1+previewLines+1)
		out = append(out, header)
		out = append(out, body[:previewLines]...)
		if len(body) > toolOutputMaxLines {
			out = append(out, truncationHint(len(body)-toolOutputMaxLines, b.frame.Kind))
		}
		return out, 0
	}
	body := toolBodyLines(full)
	// Build the collapsed header from the Style A display parts.
	// e.g. "○ Read types.go" or "○ Ran echo".
	headerSummary := ToolDisplayHeader(b.displayFrame(), cwd)
	if headerSummary == "" && b.frame.Kind != FrameMemoryCompact {
		if b.frame.Kind == FrameTool {
			title := strings.TrimSpace(b.frame.Title)
			if title == "" {
				title = defaultFrameTitle(b.frame)
			}
			headerSummary = title
		} else {
			// Fallback for non-tool frames: use title + summary.
			title := strings.TrimSpace(b.frame.Title)
			if title == "" {
				title = defaultFrameTitle(b.frame)
			}
			headerSummary = title
			summary := strings.TrimSpace(blockHeaderSummary(b.frame))
			if summary != "" {
				headerSummary += " " + summary
			}
		}
	}
	header := headerStyleForFrame(b.frame).Render(statusMarker(b.frame, spinnerPhase) + " " + headerSummary)
	if b.frame.Kind == FrameMemoryCompact {
		// A folded checkpoint keeps the very header its full card shows: the
		// state and the before → after banner are the whole point of it.
		header = compactHeaderLine(b.frame, spinnerPhase)
	}
	// Wrap the fold header so long tool summaries don't exceed the viewport
	// width and trigger terminal autowrap, which shifts the pinned layout and
	// visually duplicates content.
	header = wrapToolDisplayLineWidth(header, "  │ ", width)
	// wrapToolDisplayLineWidth may produce multiple lines joined by "\n"; split
	// them so each rendered row is a separate viewport line.
	headerLines := strings.Split(header, "\n")
	// A running shell should visibly advance as new output arrives. Show its
	// newest rows by default instead of freezing the preview on the first rows;
	// users can still expand the block to inspect everything received so far.
	if b.frame.Kind == FrameTool && b.frame.StreamingOutput && toolStatusRunning(b.frame) {
		if len(body) <= liveToolOutputMaxLines {
			return full, -1
		}
		if b.expanded {
			out := make([]string, 0, len(headerLines)+len(body))
			out = append(out, headerLines...)
			out = append(out, body...)
			return out, 0
		}
		omitted := len(body) - liveToolOutputMaxLines
		hint := lipgloss.NewStyle().Faint(true).Render(fmt.Sprintf("    … %d earlier lines", omitted))
		out := make([]string, 0, len(headerLines)+liveToolOutputMaxLines+1)
		out = append(out, headerLines...)
		out = append(out, hint)
		out = append(out, body[len(body)-liveToolOutputMaxLines:]...)
		return out, 0
	}
	maxLines := toolSpecificMaxLines(b.frame.Title)
	if b.frame.Kind == FrameMemoryCompact {
		maxLines = toolOutputMaxLines
	}
	if toolPreviewUsesRenderedBody(b.frame) {
		if len(body) <= maxLines {
			return full, -1
		}
		if b.expanded {
			out := make([]string, 0, len(headerLines)+len(body))
			out = append(out, headerLines...)
			out = append(out, body...)
			return out, 0
		}
		previewLines := min(len(body), maxLines)
		out := make([]string, 0, len(headerLines)+previewLines+1)
		out = append(out, headerLines...)
		out = append(out, body[:previewLines]...)
		if len(body) > maxLines {
			out = append(out, truncationHint(len(body)-maxLines, b.frame.Kind))
		}
		return out, 0
	}
	// Read-like cards fold in whole logical file lines. The engine
	// reports how many content lines the result actually holds
	// (ToolMeta.ResultLines) and every rendered content line carries its
	// "N|" gutter, so the preview slices whole file lines and the hint
	// counts the engine's lines — counting rendered rows instead would
	// report the terminal's line wrapping as extra file lines.
	if b.frame.Kind == FrameTool && isReadLikeTool(b.frame.Title) && b.frame.ToolMeta.ResultLines > 0 {
		preview, groups := readFileFoldPreview(body, maxLines)
		if groups <= maxLines {
			return full, -1
		}
		if b.expanded {
			out := make([]string, 0, len(headerLines)+len(body))
			out = append(out, headerLines...)
			out = append(out, body...)
			return out, 0
		}
		out := make([]string, 0, len(headerLines)+len(preview)+1)
		out = append(out, headerLines...)
		out = append(out, preview...)
		out = append(out, truncationHint(b.frame.ToolMeta.ResultLines-maxLines, b.frame.Kind))
		return out, 0
	}
	if len(body) <= maxLines {
		// Short: renderCompactFrame's header (full[0]) + body inline,
		// not clickable.
		return full, -1
	}
	if b.expanded {
		out := make([]string, 0, len(headerLines)+len(body))
		out = append(out, headerLines...)
		out = append(out, body...)
		return out, 0
	}
	previewLines := min(len(body), maxLines)
	out := make([]string, 0, len(headerLines)+previewLines+1)
	out = append(out, headerLines...)
	out = append(out, body[:previewLines]...)
	if len(body) > maxLines {
		out = append(out, truncationHint(len(body)-maxLines, b.frame.Kind))
	}
	return out, 0
}

// toolBodyLines returns the tool output body from full, skipping past every
// header line. renderCompactFrame emits the header (possibly wrapped across
// multiple lines: first line starts with the status marker, continuations
// start with "  │ ") followed by the body (lines starting with "  └ " from
//
// Every body a collapsible block draws opens on "  └ " (output blocks, the
// Markdown and diff cards, a settled checkpoint), so a block without that row
// has no body at all: a running tool with no output yet, an awaiting-approval
// or header-only card, a checkpoint still in progress. Taking the rows past
// the first as body counted a wrapped header's own continuations as output,
// so a long running command was folded under its own header and those rows
// were repeated below it as an unstyled "output" preview.
// formatToolOutputBlock). When the header wraps, full[0] is only the first
// header line and full[1:] still contains header continuations — which would
// duplicate the fold header foldBlock builds separately. This skips past
// every header line to the first body line.
func toolBodyLines(full []string) []string {
	for i, line := range full {
		plain := sgrPattern.ReplaceAllString(line, "")
		if strings.HasPrefix(plain, "  └ ") {
			return full[i:]
		}
	}
	return nil
}

// startsReadFileLogicalLine reports whether a style-stripped read_file body
// row is the first row of a logical file line: the engine prefixes every
// content line with its "N|" gutter, and rows produced by wrapping a long
// line never carry one.
func startsReadFileLogicalLine(plain string) bool {
	plain = strings.TrimPrefix(plain, "  └ ")
	plain = strings.TrimPrefix(plain, "    ")
	bar := strings.IndexByte(plain, '|')
	if bar <= 0 {
		return false
	}
	for i := 0; i < bar; i++ {
		if plain[i] < '0' || plain[i] > '9' {
			return false
		}
	}
	return true
}

// readFileFoldPreview slices a read_file card's rendered body into logical
// file lines and returns the rows covering the first maxLines of them plus
// how many logical lines the body holds. The gutter each logical line starts
// with is the group boundary, so a wrapped preview shows whole file lines and
// the fold hint can count the engine's line total against them.
func readFileFoldPreview(body []string, maxLines int) (preview []string, groups int) {
	bounds := make([]int, 0, len(body))
	for i, line := range body {
		plain := sgrPattern.ReplaceAllString(line, "")
		if i == 0 || startsReadFileLogicalLine(plain) {
			bounds = append(bounds, i)
		}
	}
	groups = len(bounds)
	if groups <= maxLines {
		return body, groups
	}
	return body[:bounds[maxLines]], groups
}

// renderBlockFull returns the block's full (expanded) rendered lines, served
// from the per-block cache when the inputs that affect output are unchanged.
// The fold state is NOT part of the cache key: folding (foldBlock) is cheap
// post-processing on these lines and is recomputed every frame (and itself
// memoized via b.foldCache).
//
// Content changes are detected via the cache validity flag rather than a
// re-derived string signature: every site that mutates b.frame sets
// cache.valid=false (see viewmodel.go append/replaceOrAppendBlock and
// renderer.go FinalizePendingTools), so a redundant per-frame signature
// computation is avoided - important when thousands of blocks are checked on
// every repaint. gen increments on each regeneration so the fold cache can
// detect that `full` changed.
func renderBlockFull(b *viewBlock, width int, theme DiffTheme, spinnerPhase int, cwdOpt ...string) []string {
	cwd := ""
	if len(cwdOpt) > 0 {
		cwd = strings.TrimSpace(cwdOpt[0])
	}
	c := &b.cache
	animated := (toolStatusRunning(b.frame) && !toolStatusAwaitingApproval(b.frame)) ||
		(b.frame.Kind == FrameMemoryCompact && !b.frame.Final) ||
		(b.frame.Kind == FrameSkillInstall && !b.frame.Final)
	// A subagent card with a running task clock depends on the second, not the
	// spinner phase: the clock walks once a second, so keying it on the tick
	// would re-render the whole card ten times for one visible change.
	clockSec := 0
	clocked := b.frame.Kind == FrameFanout && fanoutHasLiveClock(b.frame)
	if clocked {
		clockSec = int(time.Now().Unix())
	}
	if c.valid && c.width == width && c.cwd == cwd {
		if clocked {
			if c.clockSec == clockSec {
				return c.lines
			}
		} else if !animated || c.spinnerPhase == spinnerPhase {
			// Running tools and compact blocks depend on the animation phase;
			// all other blocks are phase-independent and reuse their cached
			// lines.
			return c.lines
		}
	}
	lines, lineAgents := renderFrameLinesWithAgents(b.displayFrame(), width, theme, spinnerPhase, cwd)
	c.valid = true
	c.width = width
	c.cwd = cwd
	c.spinnerPhase = spinnerPhase
	c.clockSec = clockSec
	c.gen++
	c.lines = lines
	c.lineAgents = lineAgents
	return lines
}

// fanoutHasLiveClock reports whether a subagent card holds a task row whose
// execution is still running: its clock is computed at paint time from "now",
// so the card changes once a second until the execution ends.
func fanoutHasLiveClock(f Frame) bool {
	for _, clock := range f.FanoutLineClocks {
		if !clock.Start.IsZero() && clock.End.IsZero() {
			return true
		}
	}
	return false
}

// renderFrameLinesWithAgents renders a frame and, for a fanout block, reports
// which subagent owns each rendered line. Every other kind reports nil: their
// ownership, when they have any, is the frame's own AgentID.
func renderFrameLinesWithAgents(f Frame, width int, theme DiffTheme, spinnerPhase int, cwdOpt ...string) ([]string, []string) {
	if f.Kind == FrameThinking {
		return trimTrailingBlank(thinkingDisplayLinesWithWidth(f, width)), nil
	}
	if f.Kind == FrameRaw {
		// Verbatim, already-styled lines (status text such as the OAuth URL).
		// No markdown, no re-wrap.
		return splitCapturedLines(f.Content), nil
	}
	if f.Kind == FrameBannerInfo {
		// The startup card is laid out for the painted width on every
		// repaint, so a terminal resize reflows it (renderBlockFull's cache
		// keys on width).
		return forebrainBannerLines(f.Title, f.Content, width), nil
	}

	prevW, prevH := forcedTermWidth, forcedTermHeight
	forcedTermWidth, forcedTermHeight = width, 100000
	defer func() { forcedTermWidth, forcedTermHeight = prevW, prevH }()

	var buf bytes.Buffer
	cwd := ""
	if len(cwdOpt) > 0 {
		cwd = strings.TrimSpace(cwdOpt[0])
	}
	sr := NewRenderer(&buf, &buf)
	sr.fullBodyMode = true
	sr.diffTheme = theme
	sr.cwd = cwd
	// Inherit the live spinner phase so running-tool headers render the correct
	// Braille spinner frame (renderCompactFrame reads r.spinnerPhase).
	sr.spinnerPhase = spinnerPhase

	switch f.Kind {
	case FrameAssistant:
		sr.renderAssistant(f)
	case FrameUser:
		sr.renderUserMessage(f.Content)
	case FrameTool:
		sr.renderCompactFrame(f, "tool", "214")
	case FrameError:
		sr.renderCompactFrame(f, "error", "203")
	case FramePlan:
		sr.renderCompactFrame(f, "plan", "141")
	case FrameSystem:
		sr.renderCompactFrame(f, "system", "244")
	case FrameStatus:
		sr.renderStatus(f)
	case FrameFanout:
		sr.renderFanout(f)
	case FrameMemoryCompact:
		sr.renderMemoryCompact(f)
	case FrameSkillInstall:
		sr.renderSkillInstall(f)
	case FrameGoal:
		sr.renderGoal(f)
	default:
		sr.renderCompactFrame(f, "system", "244")
	}
	lines := splitCapturedLines(buf.String())
	if f.Kind != FrameFanout {
		return lines, nil
	}
	return lines, fanoutLineAgents(f.FanoutLineAgents, sr.fanoutLineOwners, len(lines))
}

// fanoutLineAgents resolves the scratch renderer's per-line content-index map
// against the frame's per-content-line agents, producing one agent id per
// rendered line. It is clamped to the rendered line count because
// splitCapturedLines drops trailing blank rows the renderer emitted.
func fanoutLineAgents(contentAgents []string, owners []int, renderedLines int) []string {
	if len(contentAgents) == 0 || len(owners) == 0 || renderedLines <= 0 {
		return nil
	}
	out := make([]string, renderedLines)
	for i := 0; i < renderedLines && i < len(owners); i++ {
		owner := owners[i]
		if owner < 0 || owner >= len(contentAgents) {
			continue
		}
		out[i] = contentAgents[owner]
	}
	return out
}

// splitCapturedLines converts the raw byte stream a scratch renderer wrote
// (lines ended with "\r\n", styled with ANSI) into clean viewport rows: split
// on "\n", drop the carriage return each line carries, trim trailing blank
// rows so block spacing is uniform, and strip stray BEL bytes so a model or
// tool emitting 0x07 does not ring the terminal bell. Embedded styling escapes
// are preserved.
func splitCapturedLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	raw := strings.Split(s, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		// One full control-character cleaning at the capture boundary: tabs
		// expanded to tab stops, inline \r \v \f \b BEL dropped, every CSI
		// but SGR/EL dropped. Unbounded width: only cleaning happens here,
		// the canvas cuts at the physical row budget when it paints.
		out = append(out, fitPaintRow(strings.TrimRight(line, "\r"), math.MaxInt))
	}
	return trimTrailingBlank(out)
}

// statusMarker returns the status indicator for a collapsible block header.
// Tools: ⠋ (animated Braille spinner) running, ● complete, ◆ plan-mode,
// ○ awaiting-approval.  Thinking: △ streaming, ▲ finalized.
//
// spinnerPhase rotates the running-tool spinner; the viewport painter advances
// it on each repaint so in-flight tools visibly spin.
func statusMarker(f Frame, spinnerPhase int) string {
	if f.Kind == FrameThinking {
		if f.Final {
			return thinkComplete
		}
		return thinkRunning
	}
	// Plan-mode tools get a diamond marker.
	if f.Kind == FrameTool {
		lowerTitle := strings.ToLower(strings.TrimSpace(f.Title))
		if lowerTitle == "enter_plan_mode" || lowerTitle == "exit_plan_mode" {
			return "◆"
		}
		// A failed skill load is a definite failure, matching the ✗ the full
		// renderer draws for the same frame.
		if skillCardStatus(f) == "failed" {
			return "✗"
		}
	}
	// Tool, compact, and other blocks: running/suspended vs complete.
	if !f.Final || toolStatusPending(f) {
		// A running (executing, not awaiting-approval) tool shows an animated
		// Braille spinner. Awaiting-approval and non-tool live blocks keep the
		// static open circle.
		if toolStatusRunning(f) && !toolStatusAwaitingApproval(f) {
			return spinnerGlyphs[spinnerPhase%len(spinnerGlyphs)]
		}
		return toolRunning
	}
	return toolComplete
}

func truncationHint(remaining int, kind FrameKind) string {
	// Match the continuation-line prefix of the body so the hint aligns
	// with the tool output / thinking lines above it.
	prefix := "    " // tool continuation prefix
	if kind == FrameThinking {
		prefix = "  " // thinking body prefix (after "▸ " replacement)
	}
	return lipgloss.NewStyle().Faint(true).Render(prefix + fmt.Sprintf("··· %d more lines (click to expand)", remaining))
}

func defaultFrameTitle(f Frame) string {
	switch f.Kind {
	case FrameTool:
		return "tool"
	case FrameError:
		return "error"
	case FramePlan:
		return "plan"
	case FrameSystem:
		return "system"
	case FrameMemoryCompact:
		return "compact"
	default:
		return "details"
	}
}

func headerStyleForFrame(f Frame) lipgloss.Style {
	color := "244"
	switch f.Kind {
	case FrameTool:
		color = toolHeaderColor(f)
	case FrameError:
		color = "203"
	case FramePlan:
		color = "141"
	case FrameMemoryCompact:
		// Matches renderMemoryCompact's final-state colour.
		color = "75"
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color))
}

// maxFoldSummaryRunes caps the plain-text length of a fold header summary. A
// fold header is a one-line disclosure label; some replayed/error frames carry
// a multi-KB Summary that would otherwise wrap into hundreds of header lines
// (and, before wrapCardLine was made O(n), freeze the TUI on the renderer
// lock). 200 runes is well beyond any legitimate one-line summary.
const maxFoldSummaryRunes = 200

func blockHeaderSummary(f Frame) string {
	summary := strings.TrimSpace(f.Summary)
	if summary == "" {
		return ""
	}
	if runes := []rune(summary); len(runes) > maxFoldSummaryRunes {
		summary = string(runes[:maxFoldSummaryRunes]) + "…"
	}
	return lipgloss.NewStyle().Faint(true).Render(summary)
}

// maxInt returns the larger of two ints. (Previously defined in the deleted
// transcript_pager.go; relocated here as the viewport is its primary user.)
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// blankRowPattern matches the escapes a visually empty row can carry: SGR
// styling plus erase-in-line (\x1b[K, \x1b[2K), which the append-only renderers
// emit to paint a spacer row in the default background. Neither puts a glyph on
// screen, so a row consisting only of these is blank. Kept separate from
// sgrPattern, which measures display width and must not swallow non-SGR CSI.
var blankRowPattern = regexp.MustCompile(`\x1b\[[0-9;]*[mK]`)

// trimTrailingBlank removes trailing all-blank rows (after stripping ANSI) so
// every block ends on real content; renderViewport separates blocks with a
// single blank row, so a spacer row left on a block would double that gap.
func trimTrailingBlank(lines []string) []string {
	end := len(lines)
	for end > 0 && strings.TrimSpace(blankRowPattern.ReplaceAllString(lines[end-1], "")) == "" {
		end--
	}
	return lines[:end]
}

// viewport_screen.go is the interactive alt-screen compositor: it owns the whole
// terminal, painting a scrollable transcript region (rendered from the retained
// viewModel by viewport.go) above a bottom-pinned composer on every change. It
// replaces the legacy append-only stream for real terminals; the plain path in
// renderer.go survives only for non-TTY/pipe output.
//
// The composer block is built here as pure []string + cursor coordinates by
// reusing the exact leaf formatters the legacy inline composer uses
// (layoutNormalComposer, formatComposerPendingInputPreview, the shared-block
// line styles, the footer/overlay formatters) — only the OUTPUT SHAPE changes,
// from "write to terminal at a relative cursor" to "return lines for absolute
// placement". So the composer looks byte-for-byte identical; only its position
// (pinned bottom) and the transcript above it (redrawable) are new.

// composerBlock is the composer rendered to absolute lines plus the cursor's
// position within them. cursorRow/cursorCol are 0-based offsets into lines.
// When the block is taller than the allocated composer area, lines holds the
// visible slice and fullLines/scrollOffset preserve the un-scrolled source so
// selection anchors and line lookup stay stable as the user scrolls.
type composerBlock struct {
	lines        []string
	cursorRow    int
	cursorCol    int
	fullLines    []string // nil when lines is the full block
	scrollOffset int      // rows the visible slice is shifted down from fullLines
	// text records where the editable composer text sits inside the block so a
	// mouse click on one of its rows can be resolved back to a caret position
	// in the composer buffer. Zero for overlay blocks, which own their own
	// fields and hit-test them themselves.
	text composerTextArea
}

// composerTextArea records where a composerBlock renders the composer buffer.
// rows are consecutive block lines starting at firstLine, indexed in the
// block's full (un-scrolled) line space so a scrolled composer resolves clicks
// through the same table. runes is the text as displayed and trimmed is how
// many runes the display dropped from the head of the buffer, which the
// resolved offset adds back so the caret refers to the buffer the raw reader
// owns rather than to the card's rendering of it.
type composerTextArea struct {
	runes     []rune
	trimmed   int
	firstLine int
	rows      []composerTextRow
}

// composerTextRow is one rendered row of composer text: the display width of
// the prompt marker or indent that precedes the text on that row, and the
// half-open rune range of composerTextArea.runes the row shows. A soft-wrapped
// row ends before the whitespace the wrap consumed, so a click past the last
// word lands at the end of the visible text rather than inside the break.
type composerTextRow struct {
	prefixWidth int
	startRune   int
	endRune     int
}

// caretAt maps a click at 0-based display column col on the block line at index
// line to a caret position in the composer buffer. ok is false when the line
// carries no composer text (a border, the footer, the status or preview rows).
func (a composerTextArea) caretAt(line, col int) (int, bool) {
	idx := line - a.firstLine
	if idx < 0 || idx >= len(a.rows) {
		return 0, false
	}
	row := a.rows[idx]
	return a.trimmed + row.startRune + caretIndexForClick(a.runes[row.startRune:row.endRune], row.prefixWidth, col), true
}

// composerTextRowsFromSpans converts the wrap spans of the displayed composer
// text into the per-row rune ranges a click is resolved through. Spans are byte
// offsets into the text as laid out, which includes the argument hint appended
// after the buffer's own text; ranges are clamped to the buffer so a click on
// the hint lands at the end of what the user actually typed.
func composerTextRowsFromSpans(text string, spans []visualLineSpan) []composerTextRow {
	rows := make([]composerTextRow, len(spans))
	limit := utf8.RuneCountInString(text)
	runeAt := func(byteOffset int) int {
		if byteOffset > len(text) {
			return limit
		}
		return utf8.RuneCountInString(text[:byteOffset])
	}
	for i, span := range spans {
		start := runeAt(span.startByte)
		end := runeAt(span.endByte)
		if end < start {
			end = start
		}
		rows[i] = composerTextRow{
			prefixWidth: normalComposerLinePrefixWidth(i),
			startRune:   start,
			endRune:     end,
		}
	}
	return rows
}

// normalizeComposerText folds the buffer's line endings the way the composer
// card displays them. The card additionally trims leading newlines, which is
// what composerTextArea.trimmed accounts for when a click is resolved back to
// an offset in the buffer.
func normalizeComposerText(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

// composerDisplayText is the buffer as the composer card actually shows it:
// line endings folded, and the leading newlines the card never draws removed.
// It is the single definition of "what the composer currently holds", and
// everything that reads the composer as text must go through it — the
// renderer's slash-mode switch, the slash picker's activation, the argument
// hint. A draft of "\n/" draws as "/" and submits as a slash command, so a
// second reading that compares the raw buffer instead would silently disagree
// with both the screen and the executor.
func composerDisplayText(text string) string {
	return strings.TrimLeft(normalizeComposerText(text), "\n")
}

// buildComposerBlock renders the composer + transient status + pending-input
// preview + slash/roster overlay to a flat line slice. It mirrors
// renderComposerWithOverlayLocked's content exactly; the structural difference
// is that lines are returned for the painter to place at the bottom of the
// screen rather than written at a relative cursor offset.
func (r *Renderer) buildComposerBlock(cs ComposerRenderState, termWidth int) composerBlock {
	// Keep the raw input reader's visual-line computation in sync with the
	// current composer content width (soft-wrap width).
	composerContentWidth.Store(int32(normalComposerContentWidth(termWidth)))

	composerText := composerDisplayText(cs.Text)
	statusText := strings.TrimSpace(r.transientStatusTextLocked())

	footerText := r.composerFooterText(termWidth)
	previewLines := formatComposerPendingInputPreview(cs.PendingInput, termWidth)

	slashMode := strings.HasPrefix(composerText, "/")
	if slashMode {
		return r.buildSlashComposerBlock(cs, composerText, statusText, footerText, previewLines, cs.OverlayRows, termWidth)
	}
	return r.buildNormalComposerBlock(cs, composerText, statusText, footerText, previewLines, cs.OverlayRows, cs.AgentRoster, termWidth)
}

func (r *Renderer) composerFooterText(termWidth int) string {
	left, right := r.composerFooterSidesLocked()
	if left == "" {
		left = " "
	}
	footerMaxWidth := termWidth - sharedBlockFooterTruncateExtraRoom
	if footerMaxWidth > 0 {
		if clip := r.clipNotificationText(); clip != "" {
			right = clip
		}
		left = layoutComposerFooter(left, right, footerMaxWidth)
	}
	return left
}

// composerFooterSidesLocked builds the two halves of the footer for whichever
// view is on screen. Caller holds r.mu.
//
// The conversation's footer answers "what is this session running and how much
// context is left". A subagent's view answers the first question about that
// subagent instead: the model is the one its type resolved to. Its right half
// answers the second question about that subagent — how much of its own
// context window is left — the same way the conversation's does about the
// conversation (owner requirement 4, formatComposerTokenStats).
func (r *Renderer) composerFooterSidesLocked() (string, string) {
	if view := strings.TrimSpace(r.activeView); view != "" {
		return r.subagentFooterLeftLocked(view), formatComposerTokenStats(r.composerTokens.get(view))
	}
	plan := ""
	if r.planMode {
		plan = planModeFooterLabel
	}
	return joinFooterSegments(plan, r.footer.Model, r.footer.ReasoningEffort),
		formatComposerTokenStats(r.composerTokens.get(""))
}

// subagentFooterLeftLocked names the subagent whose view is open, how to leave
// it, and what it is running on. Caller holds r.mu.
//
// The roster key is shortened to the same four characters the roster panel uses
// to tell two agents of one type apart: the whole "subagent-<uuid>" is 45
// columns, which used to push the model and effort off the end of the line
// entirely.
//
// The model is the one this execution's spawn announced — a dispatch-time
// override, a type's own chain, or nothing. Nothing means the spawn predates
// executions carrying models (a replayed old session): the segment is left out
// rather than answered with the conversation's model, which the run may never
// have used.
func (r *Renderer) subagentFooterLeftLocked(agentID string) string {
	agentType := r.subagentTypes[agentID]
	model, effort := r.subagentModels[agentID].Model, r.subagentModels[agentID].ReasoningEffort
	return joinFooterSegments(
		"viewing "+joinFooterSegments(agentType, shortAgentID(agentID)),
		"esc to return", model, effort,
	)
}

// joinFooterSegments joins the non-empty segments of one footer half with the
// separator the footer uses throughout.
func joinFooterSegments(segments ...string) string {
	kept := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment = strings.TrimSpace(segment); segment != "" {
			kept = append(kept, segment)
		}
	}
	return strings.Join(kept, " · ")
}

func (r *Renderer) buildNormalComposerBlock(cs ComposerRenderState, composerText, statusText, footerText string, previewLines []string, overlayRows []OverlayRow, roster AgentRosterSnapshot, termWidth int) composerBlock {
	displayText := composerText
	if cs.ArgumentHint != "" {
		displayText = composerText + "\x1b[38;5;245m " + cs.ArgumentHint + sharedBlockReset
	}
	layout := layoutNormalComposer(displayText, cs.Cursor, termWidth)
	rosterVisible := len(overlayRows) == 0 && agentRosterHasRunningSubagent(roster)
	rosterLines := []string(nil)
	if rosterVisible {
		// The cursor is the renderer's own state — keyed by agent, kept on
		// the view by every view change — so it is resolved here, against
		// the renderer that owns it, rather than tracked by the caller.
		marked := agentRosterMarkedIndex(roster, cs.RosterFocused, r.rosterCursorIndex(roster), r.activeView)
		rosterLines = formatAgentRosterLines(roster, cs.RosterFocused, marked, termWidth)
	}

	lines := make([]string, 0, len(layout.lines)+len(previewLines)+len(rosterLines)+8)
	if statusText != "" {
		// Keep the live status visually separate from the transcript's last
		// retained row. The composer block is painted immediately after the
		// transcript region, so this leading row is the boundary spacing.
		lines = append(lines, "")
		lines = append(lines, r.renderComposerStatusLine(statusText, termWidth))
	}
	if statusText != "" && len(previewLines) > 0 {
		lines = append(lines, "")
	}
	if len(previewLines) > 0 {
		lines = append(lines, previewLines...)
	} else {
		// Without a queue preview, retain the usual single spacer before the
		// composer card. A visible preview already provides that boundary and
		// should sit directly against the card instead of leaving an empty row.
		// While the clipboard holds an image, that same row carries the paste
		// hint, right-aligned over the card's top rule.
		lines = append(lines, r.clipboardImageHintLine(termWidth))
	}
	cursorRow := len(lines) + 1 // +1 for the leading border line below
	cursorCol := layout.cursorCol
	textArea := composerTextArea{
		runes:     []rune(composerText),
		trimmed:   utf8.RuneCountInString(normalizeComposerText(cs.Text)) - utf8.RuneCountInString(composerText),
		firstLine: cursorRow,
		rows:      composerTextRowsFromSpans(composerText, layout.spans),
	}
	lines = append(lines, strings.TrimRight(renderSharedBlockBorderLine(termWidth), "\r\n"))
	for i, line := range layout.lines {
		if i == 0 {
			lines = append(lines, strings.TrimRight(renderSharedBlockPromptLine(line, termWidth), "\r\n"))
			continue
		}
		lines = append(lines, strings.TrimRight(renderSharedBlockContinuationLine(line, termWidth), "\r\n"))
	}
	cursorRow += layout.cursorRowFromTop
	lines = append(lines, strings.TrimRight(renderSharedBlockBorderLine(termWidth), "\r\n"))
	if len(overlayRows) == 0 {
		// A pending auto-continue sits between the card and the footer, where
		// the reader's eye already is when the turn that hit the limit ends.
		if notice := r.autoContinueNoticeLineLocked(termWidth); notice != "" {
			lines = append(lines, notice)
		}
		// The footer is laid out to exactly the terminal width, so its last
		// painted cell sits in the deferred-wrap column; a trailing \x1b[K
		// would erase that cell on terminals that keep the wrap pending
		// through it (macOS Terminal.app). No EL: the paint loop pre-clears
		// each row with \x1b[2K, so there is nothing left for it to do.
		lines = append(lines, sharedBlockFooterStyle+footerText+sharedBlockReset)
	}
	if rosterVisible {
		lines = append(lines, "")
		lines = append(lines, rosterLines...)
	}
	if len(overlayRows) > 0 {
		lines = append(lines, r.composerMenuLines(overlayRows, termWidth)...)
	}
	for i := range lines {
		lines[i] = stripTerminalBells(lines[i])
	}
	return composerBlock{lines: lines, cursorRow: cursorRow, cursorCol: cursorCol, text: textArea}
}

func (r *Renderer) buildSlashComposerBlock(cs ComposerRenderState, composerText, statusText, footerText string, previewLines []string, overlayRows []OverlayRow, termWidth int) composerBlock {
	displayText := composerText
	if cs.ArgumentHint != "" {
		displayText = composerText + "\x1b[38;5;245m " + cs.ArgumentHint + sharedBlockReset
	}
	// Slash mode draws the typed command without the card borders, but the
	// text still soft-wraps to the terminal width exactly as in the normal
	// composer: a long argument after "/command" used to be emitted as one
	// unwrapped row, so the terminal autowrapped it under the pinned layout,
	// shifted every row below it and put the caret in the wrong cell.
	layout := layoutNormalComposer(displayText, cs.Cursor, termWidth)

	lines := make([]string, 0, len(previewLines)+len(overlayRows)+len(layout.lines)+4)
	if statusText != "" {
		// Match the normal composer: the live status starts one row below the
		// transcript rather than touching its final retained row.
		lines = append(lines, "")
		lines = append(lines, r.renderComposerStatusLine(statusText, termWidth))
	}
	if statusText != "" && len(previewLines) > 0 {
		lines = append(lines, "")
	}
	if len(previewLines) > 0 {
		lines = append(lines, previewLines...)
	}
	if cs.SlashMenu != nil {
		// The slash menu is a slash panel whose head is the composer itself:
		// the rule above the typed command, the menu below it, all in the
		// height every slash panel takes.
		lines = append(lines, overlaySeparatorLine(termWidth-viewportRightPadding))
	}
	firstLine := len(lines)
	textArea := composerTextArea{
		runes:     []rune(composerText),
		trimmed:   utf8.RuneCountInString(normalizeComposerText(cs.Text)) - utf8.RuneCountInString(composerText),
		firstLine: firstLine,
		rows:      composerTextRowsFromSpans(composerText, layout.spans),
	}
	for i, line := range layout.lines {
		if i == 0 {
			lines = append(lines, "\x1b[38;5;33m"+composerPromptMarker+sharedBlockReset+line)
			continue
		}
		lines = append(lines, "  "+line)
	}
	cursorRow := firstLine + layout.cursorRowFromTop
	if cs.SlashMenu != nil {
		layout := cs.SlashMenu.layout(termWidth-viewportRightPadding, slashPanelHeight(termHeightOrDefault())-2, r.slashMenuTop, true)
		r.slashMenuTop = layout.top
		lines = append(lines, selfContainedRows(layout.lines)...)
	} else if len(overlayRows) > 0 {
		lines = append(lines, r.composerMenuLines(overlayRows, termWidth)...)
	} else {
		// Same no-trailing-EL rule as the normal footer above: the row spans
		// the full terminal width and must not end in an erase.
		lines = append(lines, sharedBlockFooterStyle+footerText+sharedBlockReset)
	}
	for i := range lines {
		lines[i] = stripTerminalBells(lines[i])
	}
	return composerBlock{lines: lines, cursorRow: cursorRow, cursorCol: layout.cursorCol, text: textArea}
}

// EnableViewportMode switches the renderer into the alt-screen virtual viewport.
// Called once from Run() for interactive terminals after the startup banner/info
// have been printed inline. It enters the alternate screen buffer, seeds follow
// mode (new frames keep the view pinned to the bottom), and paints the first
// frame. Non-TTY runs never call this and keep the legacy append path.
func (r *Renderer) EnableViewportMode() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.viewportMode {
		return
	}
	r.viewportMode = true
	r.vpFollow = true
	r.vpScrollOffset = 0
	r.spinnerEpoch = time.Now()
	// We have not yet emitted any DECTCEM sequence in this viewport session;
	// the caret is visible only as the terminal default. Mark it not-shown so
	// the first paint issues a single ?25h + CUP (a hidden->shown transition)
	// instead of a spurious hide/show pair, and never assumes steady.
	r.cursorShown = false
	r.cursorScreenRow = 0
	r.cursorScreenCol = 0
	// Enter alt-screen and clear it. Leaving is handled in DisableViewportMode.
	_, _ = fmt.Fprint(r.out, "\x1b[?1049h\x1b[2J\x1b[H")
	// The screen we just cleared is blank, and nothing the painter remembers
	// from a previous viewport session applies to it.
	r.dropPaintShadowLocked()
	// Terminal paints now leave the input pipeline: a terminal that stalls
	// its drain must not freeze keys and wheel with it.
	r.startAsyncPaintWriterLocked()
	r.paintViewportLocked()
}

// DisableViewportMode leaves the alternate screen buffer and returns to the
// normal terminal scrollback. Called on shutdown. Idempotent.
func (r *Renderer) DisableViewportMode() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.viewportMode {
		return
	}
	r.stopCursorBlinkLocked()
	r.stopCompactAnimationLocked()
	r.softwareCursor = false
	r.viewportMode = false
	// Drain the async painter before leaving the alt-screen: the last frame
	// must be on the wire, and the writer must not race the exit escapes.
	r.stopAsyncPaintWriterLocked()
	_, _ = fmt.Fprint(r.out, "\x1b[?25h\x1b[?1049l")
	r.dropPaintShadowLocked()
	// Leaving the alt-screen re-shows the caret above; drop the tracked state so
	// the next viewport entry starts from a known baseline.
	r.cursorShown = true
	r.cursorScreenRow = 0
	r.cursorScreenCol = 0
}

// EnableSoftwareCursor switches the composer caret from the terminal's
// hardware cursor to a software-rendered reverse-video block. This is
// necessary because macOS Terminal.app and iTerm2 restart the hardware
// blink timer on continuous output activity: during streaming the viewport
// is repainted many times per second, and each repaint's output keeps the
// hardware caret pinned in its "on" phase so it never blinks (the reported
// "cursor freezes solid during a conversation" bug). The software cursor is
// painted as part of the normal frame and toggled by a 500ms blink ticker,
// so it blinks at a steady rate regardless of terminal output activity.
//
// Called once from run.go for interactive TTY sessions, before
// EnableViewportMode so the viewport's first paint already draws the software
// caret instead of showing the hardware one. Tests that call
// EnableViewportMode directly keep the hardware cursor (softwareCursor
// defaults to false) so existing ?25h/?25l assertions are unaffected.
func (r *Renderer) EnableSoftwareCursor() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.softwareCursor {
		return
	}
	r.softwareCursor = true
	r.cursorBlinkVisible = true
	r.cursorShown = false
	r.cursorScreenRow = 0
	r.cursorScreenCol = 0
	// Hide the hardware cursor once; paintViewportLocked's software-cursor
	// path never emits ?25h/?25l afterwards.
	r.writeTTYOrderedLocked("\x1b[?25l")
	r.startCursorBlinkLocked()
	r.paintViewportLocked()
}

// DisableSoftwareCursor restores the hardware cursor and stops the blink
// ticker. Called by DisableViewportMode during shutdown.
func (r *Renderer) DisableSoftwareCursor() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.softwareCursor {
		return
	}
	r.stopCursorBlinkLocked()
	r.softwareCursor = false
	r.cursorBlinkVisible = false
	r.cursorShown = false
	r.cursorScreenRow = 0
	r.cursorScreenCol = 0
	r.writeTTYOrderedLocked("\x1b[?25h")
	r.paintViewportLocked()
}

// startCursorBlinkLocked launches the 500ms software-cursor blink ticker.
// The ticker toggles cursorBlinkVisible and triggers a repaint so the caret
// blinks even when no streaming frames are arriving (idle composer). Caller
// holds r.mu.
func (r *Renderer) startCursorBlinkLocked() {
	r.cursorBlinkVisible = true
	ticker := time.NewTicker(500 * time.Millisecond)
	r.cursorBlinkTicker = ticker
	stop := make(chan struct{})
	r.cursorBlinkStop = stop
	// The goroutine captures local channel references (ticker.C, stop) so
	// stopCursorBlinkLocked can nil out the struct fields without racing
	// on the select reads.
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				r.mu.Lock()
				// Exit if the cursor was disabled between ticks. Outside the
				// viewport the repaint below paints nothing.
				if !r.softwareCursor {
					r.mu.Unlock()
					return
				}
				r.cursorBlinkVisible = !r.cursorBlinkVisible
				r.paintViewportLocked()
				r.mu.Unlock()
			}
		}
	}()
}

// stopCursorBlinkLocked stops the software-cursor blink ticker and signals
// the goroutine to exit. Caller holds r.mu.
func (r *Renderer) stopCursorBlinkLocked() {
	if r.cursorBlinkTicker != nil {
		r.cursorBlinkTicker.Stop()
		r.cursorBlinkTicker = nil
	}
	if r.cursorBlinkStop != nil {
		close(r.cursorBlinkStop)
		r.cursorBlinkStop = nil
	}
}

func (r *Renderer) startCompactAnimationLocked() {
	if r.compactAnimationTicker != nil || !r.viewportMode {
		return
	}
	ticker := time.NewTicker(120 * time.Millisecond)
	stop := make(chan struct{})
	r.compactAnimationTicker = ticker
	r.compactAnimationStop = stop
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				r.mu.Lock()
				if !r.viewportMode || !r.hasLiveBlockLocked() {
					r.mu.Unlock()
					return
				}
				r.paintViewportLocked()
				r.mu.Unlock()
			}
		}
	}()
}

func (r *Renderer) stopCompactAnimationLocked() {
	if r.compactAnimationTicker != nil {
		r.compactAnimationTicker.Stop()
		r.compactAnimationTicker = nil
	}
	if r.compactAnimationStop != nil {
		close(r.compactAnimationStop)
		r.compactAnimationStop = nil
	}
}

// hasLiveBlockLocked reports whether the view on screen holds a block that
// changes with time on its own: a running compaction — the conversation's, or
// the subagent's whose view is open — or a subagent card with a task clock
// still walking.
func (r *Renderer) hasLiveBlockLocked() bool {
	vm := &r.vm
	if r.activeView != "" {
		vm = r.perAgentVM[r.activeView]
	}
	if vm == nil {
		return false
	}
	// Use the cached live block count instead of O(N) traversal.
	// The count is maintained by viewModel.append when blocks are added/updated.
	return vm.liveBlockCount > 0
}

func (r *Renderer) syncLiveBlockAnimationLocked() {
	if r.hasLiveBlockLocked() {
		r.startCompactAnimationLocked()
		return
	}
	r.stopCompactAnimationLocked()
}

// maxScrollShift caps how far the painter will look for a vertical shift
// between two frames. A transcript that moved further than this is a jump
// (a new tool card, a scroll-to-bottom) rather than the steady advance of a
// reply being streamed, and repainting it outright is both correct and no more
// expensive than searching for it.
const maxScrollShift = 24

// minScrollGain is how many rows a scroll has to save over repainting before
// it is worth issuing. A scroll that rescues one or two rows is not worth the
// region switch it costs.
const minScrollGain = 3

// verticalShift picks the scroll, in rows, that best turns the screen the
// painter last left (old) into the frame about to be painted (new), over the
// transcript region's first h rows. A positive result moves content up (the
// transcript grew at the bottom), a negative one moves it down (the reader
// scrolled back). Zero means no scroll is worth issuing.
//
// The point is that a scroll is the terminal's own operation on its own grid:
// rows that merely moved are never re-sent, which is what keeps a streaming
// reply from rewriting the whole transcript on every delta. Correctness does
// not depend on the choice — whatever the scroll leaves wrong, the damage loop
// repaints — so the shift is chosen purely by how many rows it gets right.
func verticalShift(old, new []string, h int) int {
	if h < minScrollGain+1 || len(old) < h || len(new) < h {
		return 0
	}
	limit := h - 1
	if limit > maxScrollShift {
		limit = maxScrollShift
	}
	base := rowsAlreadyCorrect(old, new, h, 0)
	best, bestScore := 0, base
	for k := 1; k <= limit; k++ {
		for _, shift := range [2]int{k, -k} {
			if score := rowsAlreadyCorrect(old, new, h, shift); score > bestScore {
				best, bestScore = shift, score
			}
		}
	}
	if bestScore-base < minScrollGain {
		return 0
	}
	return best
}

// rowsAlreadyCorrect counts how many of the region's h rows would already hold
// the right bytes if the screen were scrolled by shift rows first. Rows the
// scroll brings in are blank.
func rowsAlreadyCorrect(old, new []string, h, shift int) int {
	correct := 0
	for i := 0; i < h; i++ {
		was := ""
		if src := i + shift; src >= 0 && src < h {
			was = old[src]
		}
		if was == new[i] {
			correct++
		}
	}
	return correct
}

// writeRegionScroll scrolls the top h rows of the screen by shift rows. The
// scrolling region is set so the bottom-pinned composer stays where it is, and
// SGR is reset first because the rows the scroll brings in are filled with the
// background colour in effect when it runs.
func writeRegionScroll(b *strings.Builder, h, shift int) {
	fmt.Fprintf(b, "\x1b[m\x1b[1;%dr", h)
	if shift > 0 {
		fmt.Fprintf(b, "\x1b[%dS", shift)
	} else {
		fmt.Fprintf(b, "\x1b[%dT", -shift)
	}
	// Restore the full-screen region immediately: every row this paint writes
	// afterwards is addressed absolutely.
	b.WriteString("\x1b[r")
}

// scrollShadow moves the painter's record of the screen the same way the
// terminal just moved the screen itself, so the damage comparison that follows
// is made against what is really there. Rows the scroll brought in are blank.
func scrollShadow(painted []string, h, shift int) []string {
	moved := make([]string, len(painted))
	copy(moved, painted)
	for i := 0; i < h; i++ {
		moved[i] = ""
		if src := i + shift; src >= 0 && src < h {
			moved[i] = painted[src]
		}
	}
	return moved
}

// dropPaintShadowLocked forgets what the painter believes is on the physical
// screen, so the next paint addresses every row. It is called wherever
// something other than the painter has changed the screen — entering or
// leaving the alt screen, or a resize, which makes the terminal reflow its own
// grid. Caller holds r.mu.
func (r *Renderer) dropPaintShadowLocked() {
	r.vpPainted = nil
	r.vpPaintedWidth, r.vpPaintedHeight = 0, 0
	r.vpCursor = paintedCursor{row: -1}
}

// fitPaintRow forces one synthesized row into the one form the canvas can
// guarantee to occupy exactly one physical row: tabs expanded to the next tab
// stop of the current display column, every byte that moves the cursor, breaks
// the line or scrolls the screen removed, display width cut at the row budget.
// SGR (\x1b[…m) and EL (\x1b[…K) are kept — they print no glyph and move no
// cursor. The measurement is the runewidth model the whole renderer already
// uses; no second metric is introduced.
//
// The cut lands at `width` display columns, not width-1: a row of exactly the
// terminal width never wraps (the terminal's deferred autowrap is cleared by
// the next absolute CUP), so the invariant "one synthesized row = one physical
// row" holds at width. The one-column courtesy margin is each producer's own
// choice — the status and preview rows keep it, while the composer footer, the
// card rows and the hover fill paint to the full width; clipping here at
// width-1 would slice one column off the composer footer, which deliberately
// sits flush against the terminal edge.
//
// This is not a fallback: damage tracking only repaints rows whose bytes
// changed, and its correctness rests entirely on one synthesized row being one
// physical row. This function enforces that invariant at the two points where
// a source row enters the canvas.
func fitPaintRow(line string, width int) string {
	if width < 1 {
		width = 1
	}
	// Fast path: almost every row is already clean and short. One byte scan
	// rules out every control byte; the width walk then runs on plain text.
	dirty := false
	for i := 0; i < len(line); i++ {
		if line[i] < 0x20 || line[i] == 0x7f {
			dirty = true
			break
		}
	}
	if !dirty && runewidth.StringWidth(line) <= width {
		return line
	}

	var b strings.Builder
	b.Grow(len(line))
	col := 0
	cut := false
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			seq, n := paintEscapeSequence(line[i:])
			if n == 0 {
				// A stray ESC or an unrecognized sequence: dropped.
				i++
				continue
			}
			if paintKeepsSequence(seq) {
				b.WriteString(seq)
			}
			i += n
			continue
		}
		if line[i] < 0x20 || line[i] == 0x7f {
			if line[i] == '\t' {
				n := paintTabStop - col%paintTabStop
				b.WriteString(strings.Repeat(" ", n))
				col += n
			}
			// \n \r \v \f \b BEL and every other C0 byte is dropped: each
			// would move the cursor, break the row or scroll the screen.
			i++
			continue
		}
		rn, size := utf8.DecodeRuneInString(line[i:])
		w := runewidth.RuneWidth(rn)
		if col+w > width {
			cut = true
			break
		}
		b.WriteString(line[i : i+size])
		col += w
		i += size
	}
	if cut {
		// Whatever SGR the kept prefix left open must not bleed into the row
		// painted after this one; the cut already cost this row its tail.
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// paintTabStop is the tab interval terminals expand \t against by default.
const paintTabStop = 8

// paintEscapeSequence recognizes one CSI sequence at the front of s. It
// returns the raw sequence and its length, or n == 0 when s does not start
// with a well-formed CSI (including a bare \x1b at the end of the line).
func paintEscapeSequence(s string) (string, int) {
	if len(s) < 2 || s[0] != 0x1b || s[1] != '[' {
		return "", 0
	}
	i := 2
	for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3f { // parameter bytes
		i++
	}
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f { // intermediate bytes
		i++
	}
	if i >= len(s) || s[i] < 0x40 || s[i] > 0x7e { // final byte
		return "", 0
	}
	return s[:i+1], i + 1
}

// paintKeepsSequence reports whether a CSI sequence prints no glyph and moves
// no cursor, so a canvas row may carry it: SGR (…)m sets attributes and
// EL (…)K erases to the end of the row. Everything else — cursor addressing,
// scrolling, mode changes — would break the row model and is dropped.
func paintKeepsSequence(seq string) bool {
	final := seq[len(seq)-1]
	return final == 'm' || final == 'K'
}

// paintViewportLocked repaints the entire screen: the visible transcript window
// at the top, then the composer block pinned to the bottom, then positions the
// hardware cursor inside the composer. Caller holds r.mu.
//
// The transcript window height is the terminal height minus the composer block
// height, so the composer is always fully visible and the transcript fills the
// gateway. Follow mode keeps the newest content visible; an explicit scrollOffset
// (set by wheel / future keys) freezes the position until follow is re-armed.
func (r *Renderer) paintViewportLocked() {
	if !r.viewportMode || r.paintSuppressed > 0 {
		return
	}
	// The spinner phase is a function of elapsed time, not of how many times
	// the screen happened to be painted. Repaints are triggered by keystrokes,
	// streamed deltas and the caret blink as much as by the animation tickers,
	// so counting them made the spinner race with output volume — and, because
	// a changed glyph is a changed row, made every running tool's header row
	// rewrite itself on paints that had nothing to do with it.
	r.spinnerPhase = int(time.Since(r.spinnerEpoch) / spinnerAnimationTick)
	// composerSuppressed is set in non-viewport mode while a modal overlay is
	// active: skip viewport repaints (which are no-ops anyway outside viewport
	// mode). In viewport mode the composer-overlay path paints IN the viewport
	// surface below and does NOT set this flag.
	if r.composerSuppressed {
		return
	}
	// A frame the async writer dropped leaves the shadow describing a screen
	// that never happened; discard it so this paint is a full repaint.
	if w := r.asyncPaint; w != nil && w.consumeDrop() {
		r.dropPaintShadowLocked()
	}
	width, height := termWidthOrDefault(), termHeightOrDefault()
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}
	// A geometry change reflows the terminal's own grid, so nothing the shadow
	// remembers about the physical screen still holds: forget it and repaint
	// every row of this frame.
	if width != r.vpPaintedWidth || height != r.vpPaintedHeight {
		r.dropPaintShadowLocked()
	}

	// Right padding so content doesn't run flush against the terminal edge.
	contentWidth := width - viewportRightPadding
	if contentWidth < 1 {
		contentWidth = 1
	}

	// Build the composer block first; its height determines how many rows the
	// transcript region gets. While a composer overlay is active the overlay's
	// own lines replace the interactive composer (a separator rule is prepended
	// so the block reads as distinct from the transcript above), keeping the
	// conversation visible and text-selectable. Otherwise the normal composer is
	// built; it is hidden entirely only while a full-screen modal owns the
	// screen (composerSuppressed), which is handled by the early return above.
	var composer composerBlock
	composerHeight := 0
	if r.overlayActive && r.overlayPanel != nil {
		// A slash panel takes its fixed height whatever it holds and scrolls
		// its own body, so its head and key hint stay on screen.
		layout := r.overlayPanel.layout(contentWidth, slashPanelHeight(height)-1, r.overlayScrollOffset, r.overlayFocusPending)
		r.overlayScrollOffset = layout.top
		r.overlayFocusPending = false
		lines := append([]string{overlaySeparatorLine(contentWidth)}, selfContainedRows(layout.lines)...)
		r.vpOverlayRefs = make([]overlayRowRef, len(layout.lines))
		for i := range r.vpOverlayRefs {
			r.vpOverlayRefs[i] = overlayRowRef{line: i}
		}
		r.vpOverlayFirstRow = 0
		lines = append(lines, r.overlayNoteRowsLocked(contentWidth)...)
		cursorRow := -1
		if layout.caretRow >= 0 {
			cursorRow = layout.caretRow + 1 // past the separator
		}
		composer = composerBlock{lines: lines, cursorRow: cursorRow, cursorCol: layout.caretCol}
		composerHeight = len(composer.lines)
	} else if r.overlayActive {
		sep := overlaySeparatorLine(contentWidth)
		overlayLines, overlayRefs := r.overlayComposerLinesLocked(contentWidth)
		r.vpOverlayRefs = overlayRefs
		r.vpOverlayFirstRow = 0
		cursorRow := r.overlayComposer.cursorRow
		// Reserve at least 1/3 of the screen for the transcript so the
		// user can scroll to review conversation context above a tall
		// overlay. If the overlay content exceeds the remaining space it
		// becomes independently scrollable via ViewportScrollOverlay.
		minBodyHeight := height / 3
		if minBodyHeight < 3 {
			minBodyHeight = 3
		}
		maxComposerHeight := height - minBodyHeight
		// The separator is PINNED to the first row of the block: only the
		// overlay content below it scrolls, so the rule always divides the
		// overlay from the transcript above. Scrolling it with the content
		// would strip the overlay's top border the moment the user scrolls
		// down, leaving the modal visually fused to the conversation.
		// One row of the composer area is therefore permanently the rule,
		// which is why overlayMaxScrollLocked adds 1 to the content height.
		maxContentHeight := maxComposerHeight - 1
		if maxContentHeight < 1 {
			maxContentHeight = 1
		}
		visible := overlayLines
		if len(overlayLines) > maxContentHeight {
			maxOverlayScroll := len(overlayLines) - maxContentHeight

			// Auto-scroll to keep the cursor (selected option / amend row)
			// visible.  Without this the user can arrow the cursor off-screen
			// when the command string is long enough to push options past the
			// bottom of the composer area, making the bottom choices invisible
			// and unselectable. A panel has no hardware cursor; its focus row
			// is followed only when it moved, so a wheel or page scroll over
			// an unchanged selection is not snapped back on the next paint.
			focusRow := cursorRow
			if focusRow < 0 && r.overlayFocusPending {
				focusRow = r.overlayFocusRow
			}
			if focusRow >= 0 {
				overlayEnd := r.overlayScrollOffset + maxContentHeight
				if focusRow < r.overlayScrollOffset {
					r.overlayScrollOffset = focusRow
				} else if focusRow >= overlayEnd {
					r.overlayScrollOffset = focusRow - maxContentHeight + 1
				}
			}

			if r.overlayScrollOffset > maxOverlayScroll {
				r.overlayScrollOffset = maxOverlayScroll
			}
			if r.overlayScrollOffset < 0 {
				r.overlayScrollOffset = 0
			}
			end := r.overlayScrollOffset + maxContentHeight
			if end > len(overlayLines) {
				end = len(overlayLines)
			}
			visible = overlayLines[r.overlayScrollOffset:end]
			r.vpOverlayFirstRow = r.overlayScrollOffset
			if cursorRow >= r.overlayScrollOffset && cursorRow < end {
				cursorRow -= r.overlayScrollOffset
			} else {
				cursorRow = -1
			}
		}
		r.overlayFocusPending = false
		lines := make([]string, 0, len(visible)+1)
		lines = append(lines, sep)
		lines = append(lines, visible...)
		// Shift past the pinned separator row.
		if cursorRow >= 0 {
			cursorRow++
		}
		composer = composerBlock{lines: lines, cursorRow: cursorRow, cursorCol: r.overlayComposer.cursorCol}
		composerHeight = len(composer.lines)
	} else if !r.composerSuppressed {
		fullComposer := r.buildComposerBlock(r.composerState, width)
		composer = fullComposer
		// Reserve at least 1/3 of the screen for the transcript so a tall
		// composer (e.g. a large pasted JSON) does not monopolize the viewport
		// and push the caret off the visible screen.
		minBodyHeight := height / 3
		if minBodyHeight < 3 {
			minBodyHeight = 3
		}
		maxComposerHeight := height - minBodyHeight
		if maxComposerHeight < 1 {
			maxComposerHeight = 1
		}
		fullHeight := len(fullComposer.lines)
		if fullHeight > maxComposerHeight {
			maxScroll := fullHeight - maxComposerHeight

			// Auto-scroll to keep the caret visible while the user types or
			// arrows through the composer content.
			cursorRow := fullComposer.cursorRow
			if cursorRow >= 0 {
				visibleEnd := r.composerScrollOffset + maxComposerHeight
				if cursorRow < r.composerScrollOffset {
					r.composerScrollOffset = cursorRow
				} else if cursorRow >= visibleEnd {
					r.composerScrollOffset = cursorRow - maxComposerHeight + 1
				}
			}

			if r.composerScrollOffset > maxScroll {
				r.composerScrollOffset = maxScroll
			}
			if r.composerScrollOffset < 0 {
				r.composerScrollOffset = 0
			}
			end := r.composerScrollOffset + maxComposerHeight
			if end > fullHeight {
				end = fullHeight
			}
			visibleLines := fullComposer.lines[r.composerScrollOffset:end]
			visibleCursorRow := -1
			if cursorRow >= r.composerScrollOffset && cursorRow < end {
				visibleCursorRow = cursorRow - r.composerScrollOffset
			}
			composer = composerBlock{
				lines:        visibleLines,
				cursorRow:    visibleCursorRow,
				cursorCol:    fullComposer.cursorCol,
				fullLines:    fullComposer.lines,
				scrollOffset: r.composerScrollOffset,
				// firstLine indexes the full block, so the scrolled slice
				// resolves clicks through the same table.
				text: fullComposer.text,
			}
		} else {
			r.composerScrollOffset = 0
		}
		composerHeight = len(composer.lines)
	}

	bodyHeight := height - composerHeight
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	r.vpHeight = bodyHeight
	r.vpBodyHeight = bodyHeight

	offset := r.vpScrollOffset
	if r.vpFollow {
		offset = 1 << 30
	}
	activeVM := r.activeVMLocked()
	// Stamp the current spinner phase so renderViewport can rotate the
	// running-tool marker. paintViewportLocked runs every repaint (frame events
	// plus the ~200ms working-status and 500ms cursor-blink ticks), so the
	// Braille spinner advances while a tool is in flight.
	activeVM.spinnerPhase = r.spinnerPhase
	vr := renderViewport(activeVM, contentWidth, bodyHeight, offset, r.diffTheme, r.cwd)
	r.vpLastRender = vr
	r.vpScrollOffset = vr.firstRow
	r.vpLastComposer = composer

	const hoverBgColor = "236"
	hoverBgSeq := "\x1b[48;5;" + hoverBgColor + "m"
	const selectBgColor = "24"
	selectBgSeq := "\x1b[48;5;" + selectBgColor + "m"

	selStartRow, selEndRow := sortRows(r.selectStartRow, r.selectEndRow)
	selStartCol := r.selectStartCol
	selEndCol := r.selectEndCol
	if r.selectStartRow > r.selectEndRow {
		selStartCol, selEndCol = selEndCol, selStartCol
	}
	selecting := r.selectDidDrag || (r.selectStartRow >= 0 && (r.selectStartRow != r.selectEndRow || r.selectStartCol != r.selectEndCol))

	// Decide the desired hardware-cursor state for this frame before we start
	// writing, so the leading sequence can hide the caret during the repaint
	// only when its visibility/position is actually changing. When the caret is
	// "steady" (stays visible at the same cell) we leave it visible throughout
	// the batched write and only reposition it at the end - this avoids the
	// per-frame ?25l/?25h churn that would otherwise freeze the native blink.
	prevShown := r.cursorShown
	wantShown := composerHeight > 0 && !r.composerSuppressed && !r.selectDragging && composer.cursorRow >= 0
	desiredRow, desiredCol := 0, 0
	if wantShown {
		desiredRow = bodyHeight + composer.cursorRow + 1
		desiredCol = composer.cursorCol + 1
	}
	steady := wantShown && prevShown && r.cursorScreenRow == desiredRow && r.cursorScreenCol == desiredCol

	// Compose the exact bytes of every screen row BEFORE writing anything.
	// rows[i] is the content of 1-based screen row i+1 ("" for an empty row).
	// Composing first is what lets the write below address only the rows whose
	// bytes actually changed, leaving every unchanged row untouched.
	rows := make([]string, bodyHeight+composerHeight)
	for row := 0; row < bodyHeight; row++ {
		if row >= len(vr.lines) {
			continue
		}
		line := fitPaintRow(vr.lines[row], width)
		// Selection anchors are in absolute transcript-row space; map this
		// screen row to its absolute row to test membership.
		absRow := vr.firstRow + row
		inSelection := selecting && absRow >= selStartRow && absRow <= selEndRow
		if inSelection {
			startCol := 0
			if absRow == selStartRow {
				startCol = selStartCol
			}
			endCol := -1
			if absRow == selEndRow {
				endCol = selEndCol
			}
			if selStartRow == selEndRow {
				startCol = selStartCol
				endCol = selEndCol
				if startCol > endCol {
					startCol, endCol = endCol, startCol
				}
			}
			// The trailing \x1b[49m\x1b[0m closes the selection background; no
			// \x1b[K here — a full-width row (the composer footer) would have
			// its last painted cell erased on deferred-wrap terminals.
			rows[row] = applySelectionHighlight(line, selectBgSeq, startCol, endCol) + "\x1b[49m\x1b[0m"
		} else if r.vpHoverRow == row && vr.isClickableRow(row, activeVM) {
			rows[row] = applyHoverHighlight(line, width, hoverBgSeq)
		} else {
			rows[row] = line
		}
	}
	// When the user has scrolled away from the newest transcript rows, float a
	// compact clickable control over the last body row. It intentionally does
	// not consume layout height, so showing it cannot move the transcript or the
	// bottom-pinned composer. It carries its own absolute address and is kept as
	// part of that row's bytes, so the row's damage check covers the control too
	// and it is repainted exactly when it appears, changes or goes away.
	if !r.vpFollow && vr.maxScrollOffset() > 0 {
		label, startCol, _, buttonRow := jumpToBottomButtonLayout(width, bodyHeight, r.vpNewMessages)
		if label != "" && buttonRow >= 0 && buttonRow < bodyHeight {
			rows[buttonRow] += fmt.Sprintf("\x1b[%d;%dH\x1b[38;5;255;48;5;236m%s\x1b[0m", buttonRow+1, startCol+1, label)
		}
	} else {
		// Following (or nothing to scroll): the newest rows are on screen, so
		// there is nothing unseen left to announce.
		r.vpJumpToBottomPressed = false
		r.vpNewMessages = 0
	}
	for i := 0; i < composerHeight; i++ {
		// Composer rows live in the sentinel space (>= composerRowBase); the
		// composer is bottom-pinned and does not scroll with the transcript.
		// When the composer itself is vertically scrolled, add the scroll offset
		// so selection anchors map to the full un-scrolled composer lines.
		unifiedRow := composerRowBase + i + r.vpLastComposer.scrollOffset
		// Fit before decorating: the selection and hover layers only add SGR
		// and EL, and the jump-to-bottom CUP lands on body rows painted above,
		// so a composer row enters the canvas right here, already single-row.
		composerLine := fitPaintRow(composer.lines[i], width)
		inSelection := selecting && unifiedRow >= selStartRow && unifiedRow <= selEndRow
		if inSelection {
			startCol := 0
			if unifiedRow == selStartRow {
				startCol = selStartCol
			}
			endCol := -1
			if unifiedRow == selEndRow {
				endCol = selEndCol
			}
			if selStartRow == selEndRow {
				startCol = selStartCol
				endCol = selEndCol
				if startCol > endCol {
					startCol, endCol = endCol, startCol
				}
			}
			// Same no-trailing-EL rule as the transcript selection above.
			rows[bodyHeight+i] = applySelectionHighlight(composerLine, selectBgSeq, startCol, endCol) + "\x1b[49m\x1b[0m"
		} else {
			rows[bodyHeight+i] = composerLine
		}
	}

	var b strings.Builder
	// Hide the caret during the repaint only when it is currently visible AND its
	// state is changing (visibility toggle or position move). In the steady case
	// the caret stays visible; the batched write is applied atomically at the
	// terminal's next vsync so the intermediate traversal is never shown, and we
	// avoid restarting the blink timer.
	hidDuringRepaint := false
	forceHideOverlayCursor := r.overlayActive && !wantShown
	if prevShown && !steady {
		b.WriteString("\x1b[?25l")
		hidDuringRepaint = true
	} else if forceHideOverlayCursor {
		// Modal selection overlays use their own inline arrow/focus marker. Emit
		// an idempotent hide even when cursorShown says the caret is already
		// hidden, because the terminal's real cursor can be visible after external
		// writes or an earlier desync. A visible hardware cursor on a wrapped
		// overlay row looks like the option arrow started on the wrong item.
		b.WriteString("\x1b[?25l")
	}
	// A transcript that scrolled is the same content on different rows, and
	// re-sending all of it is the other half of the flicker: a reply streaming
	// in follow mode shifts every row up on every delta. Hand the shift to the
	// terminal, which moves its own grid, and let the damage loop below fill in
	// only the rows the scroll exposed.
	if shift := verticalShift(r.vpPainted, rows, bodyHeight); shift != 0 {
		writeRegionScroll(&b, bodyHeight, shift)
		r.vpPainted = scrollShadow(r.vpPainted, bodyHeight, shift)
		r.vpCursor = r.vpCursor.scrolled(bodyHeight, shift)
	}
	// With no shadow the painter knows nothing about the physical screen, so
	// the frame starts from a blank one. Erasing row by row is not enough: a
	// terminal narrowed under us (macOS Terminal) keeps each row's cells past
	// the new right edge out of sight, erasing a row drops only its visible
	// part, and the hidden tail slides into view as residue of the wider
	// layout. Erasing the display drops every row whole.
	if r.vpPainted == nil {
		b.WriteString("\x1b[2J")
	}
	// Address only the rows whose bytes differ from what is on screen. A row
	// that did not change receives nothing: it is not erased, so it cannot be
	// caught blank by the terminal's next present. Each row is addressed
	// absolutely because a full-width line leaves many terminals in
	// delayed-wrap state; CUP also resets the column before EL clears the exact
	// row we are about to redraw.
	repainted := make([]bool, len(rows))
	for i, content := range rows {
		if i < len(r.vpPainted) && r.vpPainted[i] == content {
			continue
		}
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K", i+1)
		b.WriteString(content)
		repainted[i] = true
	}
	// A shorter frame than the last one leaves its surplus rows on screen; the
	// shadow knows exactly which they are.
	for i := len(rows); i < len(r.vpPainted); i++ {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K", i+1)
	}
	r.vpPainted = rows
	r.vpPaintedWidth, r.vpPaintedHeight = width, height

	// The software caret is a one-cell object, so it is painted as one cell
	// rather than as part of its row: a blink toggle or an arrow key must not
	// erase and rewrite the line the user is typing on. paint draws it, clear
	// restores the cell underneath, and the pair is re-emitted only when the
	// caret changes or its row was repainted out from under it.
	caret := paintedCursor{row: -1}
	if wantShown && r.softwareCursor {
		caret = paintedCursor{
			row:   bodyHeight + composer.cursorRow,
			col:   desiredCol,
			paint: softwareCursorCell(composer, true),
			clear: softwareCursorCell(composer, false),
		}
		if !r.cursorBlinkVisible {
			// Off phase: the cell shows its own content, which is also what
			// restores it, so there is nothing left to undo.
			caret.paint, caret.clear = caret.clear, ""
		}
	}
	prev := r.vpCursor
	prevRepainted := prev.row >= 0 && prev.row < len(repainted) && repainted[prev.row]
	if caret.at(caret.paint) != prev.at(prev.paint) || (caret.row >= 0 && caret.row < len(repainted) && repainted[caret.row]) {
		// The previous caret cell keeps its reverse-video block until something
		// overwrites it: restore it whenever its row was not repainted and
		// neither the new caret nor the row itself already does so.
		if prev.clear != "" && !prevRepainted && prev.at(prev.clear) != caret.at(caret.paint) {
			b.WriteString(prev.at(prev.clear))
		}
		b.WriteString(caret.at(caret.paint))
	}
	r.vpCursor = caret

	// Reposition / toggle the hardware cursor. CUP (\x1b[<row>;<col>H) does not
	// reset the terminal's blink phase, so emitting it every frame is safe. We
	// only emit DECTCEM (?25h/?25l) on an actual visibility transition: in the
	// steady case (caret visible, same cell) we skip both, which lets the caret
	// keep blinking natively while the agent streams. See the cursorShown field
	// doc for why per-frame ?25h freezes the blink.
	if wantShown {
		if r.softwareCursor {
			// Software cursor: the hardware cursor is already hidden (?25l
			// issued in EnableSoftwareCursor). ALWAYS reposition it at the
			// caret cell, even on the blink-off phase. An inline IME (e.g.
			// pinyin) anchors its preedit at the hardware cursor's screen
			// position; if a frame leaves the cursor elsewhere the preedit
			// drifts there. Previously the off phase emitted no CUP, so the
			// cursor stayed at the footer's right edge (the last cell
			// written by the repaint): the IME drew the pinyin preedit
			// there, overflowed the bottom screen row and scrolled the
			// viewport up every 500ms - the "TUI jumps while typing pinyin"
			// bug, plus a stray third caret after "xxxxx tokens". CUP does
			// not reset the terminal blink phase, so emitting it every frame
			// is safe and mirrors the hardware-cursor path below.
			fmt.Fprintf(&b, "\x1b[%d;%dH", desiredRow, desiredCol)
			// Keep cursorShown false so the hardware-cursor steady/transition
			// logic (hidDuringRepaint, ?25h) is never triggered.
		} else {
			fmt.Fprintf(&b, "\x1b[%d;%dH", desiredRow, desiredCol)
			// Re-show only if we hid during the repaint (state changed) or the caret
			// was previously hidden (hidden -> shown transition). In the steady case
			// the caret never went hidden, so we must NOT re-issue ?25h.
			if hidDuringRepaint || !prevShown {
				b.WriteString("\x1b[?25h")
			}
			r.cursorShown = true
			r.cursorScreenRow = desiredRow
			r.cursorScreenCol = desiredCol
		}
	} else {
		// Caret should be hidden. If we hid it during the repaint it is already
		// hidden; if it was already hidden there is nothing to emit. Either way
		// just record the new state - never churn ?25l on an already-hidden
		// caret.
		if !r.softwareCursor {
			r.cursorShown = false
			r.cursorScreenRow = 0
			r.cursorScreenCol = 0
		}
	}

	if b.Len() == 0 {
		// Nothing on screen differs from the frame we were asked to paint.
		return
	}
	// Frame the whole write in synchronized output (DEC private mode 2026) so a
	// terminal that supports it presents the changed rows in one step instead of
	// sampling the screen somewhere between a row's erase and its rewrite.
	// Terminals that do not support it ignore the unknown private mode.
	if w := r.asyncPaint; w != nil {
		frame := make([]byte, 0, b.Len()+len("\x1b[?2026h")+len("\x1b[?2026l"))
		frame = append(frame, "\x1b[?2026h"...)
		frame = append(frame, b.String()...)
		frame = append(frame, "\x1b[?2026l"...)
		w.submit(frame)
		return
	}
	_, _ = fmt.Fprint(r.out, "\x1b[?2026h"+b.String()+"\x1b[?2026l")
}

// overlaySeparatorLine returns the horizontal rule painted above a modal
// composer overlay to delimit it from the conversation transcript. Styled to
// match the existing approval-block separator (bright-black ─ row).
func overlaySeparatorLine(width int) string {
	if width < 1 {
		width = 80
	}
	// One column of slack, like renderSharedBlockBorderLine: the rule must
	// never reach the terminal's autowrap threshold.
	if width > 1 {
		width--
	}
	return "\x1b[90m" + strings.Repeat("─", width) + "\x1b[0m"
}

// paintedCursor is the software caret the last paint left on the screen: the
// 0-based screen row and 1-based column it sits on, the cell that drew it, and
// the cell that puts it back the way it was. The painter keeps the pair so the
// caret can be moved, blinked off or removed by rewriting its one cell, instead
// of erasing and rewriting the whole line the user is typing on.
type paintedCursor struct {
	row   int
	col   int
	paint string
	clear string
}

// at is cell written at the caret's position; nothing for no cell.
func (c paintedCursor) at(cell string) string {
	if cell == "" || c.row < 0 {
		return ""
	}
	return fmt.Sprintf("\x1b[%d;%dH", c.row+1, c.col) + cell
}

// scrolled is the caret after the terminal scrolled its top h rows by shift,
// the way scrollShadow moves the rows: a caret on them moved with its cell,
// so restoring it means writing where the cell went, and one scrolled off
// them went with its cell, leaving nothing to restore.
func (c paintedCursor) scrolled(h, shift int) paintedCursor {
	if c.row < 0 || c.row >= h {
		return c
	}
	c.row -= shift
	if c.row < 0 || c.row >= h {
		return paintedCursor{row: -1}
	}
	return c
}

// softwareCursorCell returns the bytes that paint the software caret's cell,
// to be written at its position — in reverse video when reverse is true, and
// in the cell's own styling when it is false, which is exactly what restores
// the cell when the caret blinks off or moves away.
//
// The caret must cover the FULL display width of the rune sitting at the caret
// cell. Painting a single 1-cell reverse space ("\x1b[7m \x1b[0m") in front of a
// wide (CJK) rune overwrites only that rune's first cell, leaving its second as
// a dangling continuation cell. Terminals render such a cell inconsistently
// (blank / hatched / reverse), which made the caret visually drift by one CJK
// column relative to where text was actually inserted — the "paste CJK then the
// caret is off by one Chinese character" bug. Re-emitting the rune itself
// covers both cells of a wide char and preserves the glyph, so the caret is
// always drawn exactly where insertion happens. An empty cell (end of line)
// gets a 1-cell space.
func softwareCursorCell(cb composerBlock, reverse bool) string {
	style, glyph := cellAtDisplayCol(cb.lines, cb.cursorRow, cb.cursorCol)
	var b strings.Builder
	b.WriteString(style)
	if reverse {
		b.WriteString("\x1b[7m")
	}
	b.WriteString(glyph)
	b.WriteString("\x1b[0m")
	return b.String()
}

// cellAtDisplayCol returns the SGR styling in effect at display column `col`
// (0-based) of line `row` and the glyph occupying that cell, so the cell can be
// reproduced byte-exactly on its own. The styling is the ordered list of SGR
// sequences still active at that point, which replays to the same state.
//
// A column past the line's last rune is an empty cell painted by the line's
// erase-to-end-of-line, so it reports the styling that was active at that EL
// (the composer card's background, say) and a space — not the styling at the
// end of the line, which is a reset on every line the renderer produces.
func cellAtDisplayCol(lines []string, row, col int) (style, glyph string) {
	if row < 0 || row >= len(lines) || col < 0 {
		return "", " "
	}
	line := lines[row]
	var active []string
	var fill []string
	hasFill := false
	width := 0
	i := 0
	for i < len(line) {
		if line[i] == 0x1b {
			// Consume a CSI sequence: ESC [ <params/intermediates> <final>.
			// Also tolerate a bare ESC + single char.
			j := i + 1
			if j < len(line) && line[j] == '[' {
				j++
				paramStart := j
				final := byte(0)
				for j < len(line) {
					c := line[j]
					j++
					if c >= 0x40 && c <= 0x7e {
						final = c
						break
					}
				}
				switch final {
				case 'm':
					if params := line[paramStart : j-1]; params == "" || params == "0" {
						active = active[:0]
					} else {
						active = append(active, line[i:j])
					}
				case 'K':
					fill = append(fill[:0], active...)
					hasFill = true
				}
			} else if j < len(line) {
				j++
			}
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		rw := runewidth.RuneWidth(r)
		if width == col {
			return strings.Join(active, ""), string(r)
		}
		if width+rw > col {
			// col lands inside this rune's cells but not at its start (a wide
			// rune's second cell). No rune starts there.
			break
		}
		width += rw
		i += size
	}
	if hasFill {
		return strings.Join(fill, ""), " "
	}
	return strings.Join(active, ""), " "
}

// SetActiveView switches the viewport to show the transcript for the given
// subagent (by roster key / AgentID). Pass an empty string to return to the
// primary agent view. Each view keeps its own scroll/follow/unseen state.
func (r *Renderer) SetActiveView(agentID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.viewportMode {
		return
	}
	r.setActiveViewLocked(agentID)
}

// assignActiveViewLocked is the single writer of the active view: it sets the
// view and carries the roster cursor onto it, so nothing can change what is on
// screen while leaving the cursor naming an agent the user is not looking at.
// Caller holds r.mu.
func (r *Renderer) assignActiveViewLocked(view string) {
	r.activeView = view
	r.rosterCursor = view
}

// setActiveViewLocked switches the displayed view and repaints. Caller holds
// r.mu.
func (r *Renderer) setActiveViewLocked(agentID string) {
	nextView := strings.TrimSpace(agentID)
	if nextView == r.activeView {
		return
	}
	if r.viewBrowseState == nil {
		r.viewBrowseState = make(map[string]viewportBrowseState)
	}
	r.viewBrowseState[r.activeView] = viewportBrowseState{
		ScrollOffset: r.vpScrollOffset,
		Follow:       r.vpFollow,
		NewMessages:  r.vpNewMessages,
	}
	r.assignActiveViewLocked(nextView)
	// The sweep animates the view on screen, which just changed.
	r.syncLiveBlockAnimationLocked()
	if saved, ok := r.viewBrowseState[nextView]; ok {
		r.vpScrollOffset = saved.ScrollOffset
		r.vpFollow = saved.Follow
		r.vpNewMessages = saved.NewMessages
	} else {
		r.vpScrollOffset = 0
		r.vpFollow = true
		r.vpNewMessages = 0
	}
	r.vpHoverRow = -1
	r.selectClearLocked()
	r.paintViewportLocked()
}

// openAgentViewAtRowLocked switches to the subagent view for the card at this
// screen row. It is what keeps a finished subagent reachable: its roster row is
// gone, but its card is still in the transcript and its transcript is still in
// memory, so clicking the card reopens the conversation it had. Caller holds
// r.mu.
func (r *Renderer) openAgentViewAtRowLocked(row int) bool {
	agentID, ok := r.activeVMLocked().agentAtScreenRow(r.vpLastRender, row)
	if !ok || agentID == r.activeView {
		return false
	}
	// Only open a view that has something in it. A card whose agent produced no
	// retained frames would otherwise open an empty screen the user then has to
	// escape from.
	if vm := r.perAgentVM[agentID]; vm == nil || len(vm.blocks) == 0 {
		return false
	}
	r.setActiveViewLocked(agentID)
	return true
}

// ActiveView reports the AgentID of the currently displayed view.
// Empty string means the primary (main agent) view.
func (r *Renderer) ActiveView() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeView
}

// SnapshotBrowseState captures every primary/subagent viewport plus expanded
// block indexes. Block order is part of the canonical replay contract, making
// the index stable across a close/reopen of the same event prefix.
func (r *Renderer) SnapshotBrowseState() RendererBrowseState {
	if r == nil {
		return RendererBrowseState{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	views := make(map[string]RendererViewBrowseState, len(r.viewBrowseState)+1)
	for key, saved := range r.viewBrowseState {
		views[key] = RendererViewBrowseState{ScrollOffset: saved.ScrollOffset, Follow: saved.Follow, NewMessages: saved.NewMessages}
	}
	views[r.activeView] = RendererViewBrowseState{ScrollOffset: r.vpScrollOffset, Follow: r.vpFollow, NewMessages: r.vpNewMessages}
	expanded := make(map[string][]int)
	captureExpanded := func(key string, vm *viewModel) {
		if vm == nil {
			return
		}
		for index, block := range vm.blocks {
			if block != nil && block.collapsible && block.expanded {
				expanded[key] = append(expanded[key], index)
			}
		}
	}
	captureExpanded("", &r.vm)
	for key, vm := range r.perAgentVM {
		captureExpanded(key, vm)
	}
	return RendererBrowseState{ActiveView: r.activeView, Views: views, Expanded: expanded}
}

// RestoreBrowseState applies a snapshot after replay. Invalid/missing agent
// targets and stale block indexes are ignored, so an older snapshot cannot
// hide history or open an empty screen when the transcript changed.
func (r *Renderer) RestoreBrowseState(saved RendererBrowseState) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.viewportMode {
		return
	}
	vmFor := func(key string) *viewModel {
		if key == "" {
			return &r.vm
		}
		return r.perAgentVM[key]
	}
	resetAndExpand := func(key string, vm *viewModel) {
		if vm == nil {
			return
		}
		for _, block := range vm.blocks {
			if block != nil {
				block.expanded = false
			}
		}
		for _, index := range saved.Expanded[key] {
			if index >= 0 && index < len(vm.blocks) && vm.blocks[index] != nil && vm.blocks[index].collapsible {
				vm.blocks[index].expanded = true
			}
		}
	}
	resetAndExpand("", &r.vm)
	for key, vm := range r.perAgentVM {
		resetAndExpand(key, vm)
	}
	r.viewBrowseState = make(map[string]viewportBrowseState, len(saved.Views))
	for key, view := range saved.Views {
		if vmFor(key) == nil {
			continue
		}
		r.viewBrowseState[key] = viewportBrowseState{
			ScrollOffset: max(0, view.ScrollOffset),
			Follow:       view.Follow,
			NewMessages:  max(0, view.NewMessages),
		}
	}
	active := strings.TrimSpace(saved.ActiveView)
	if vm := vmFor(active); active != "" && (vm == nil || len(vm.blocks) == 0) {
		active = ""
	}
	r.assignActiveViewLocked(active)
	view, ok := r.viewBrowseState[active]
	if ok {
		r.vpScrollOffset = view.ScrollOffset
		r.vpFollow = view.Follow
		r.vpNewMessages = view.NewMessages
	} else {
		r.vpScrollOffset = 0
		r.vpFollow = true
		r.vpNewMessages = 0
	}
	r.vpHoverRow = -1
	r.selectClearLocked()
	r.paintViewportLocked()
}

// withdrawSubmission removes the retained user block belonging to a withdrawn
// turn. The block is found by the turn identity the frame was rendered with, so
// this can never take a block belonging to a different submission whose text
// happens to match. A foreground FrameUser always lands in the primary
// transcript (retainFrameLocked), so the search stays there and the repaint
// happens only when the primary transcript is what is on screen.
func (r *Renderer) withdrawSubmission(turn *foregroundTurn) {
	if r == nil || turn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.vm.removeLast(func(f Frame) bool { return f.Kind == FrameUser && f.turn == turn }) {
		return
	}
	// The block was counted as unseen if it was appended while the user was
	// scrolled away from the bottom; scrolling back clears that count outright,
	// so giving one back is only right while the viewport is still detached.
	if !r.vpFollow && r.vpNewMessages > 0 {
		r.vpNewMessages--
	}
	if r.activeView != "" {
		return
	}
	r.vpHoverRow = -1
	r.selectClearLocked()
	r.paintViewportLocked()
}

// appendFrameViewportLocked retains a frame in the view model and repaints.
// Follow mode remains as-is: if the user has scrolled up, a new frame must not
// yank the viewport back to the bottom — instead the frame is counted as an
// unseen message so the floating control can announce it. Caller holds r.mu.
func (r *Renderer) appendFrameViewportLocked(f Frame) {
	visible := r.activeVMLocked()
	blocksBefore := len(visible.blocks)
	repaint := r.retainFrameLocked(f)
	// Count BEFORE painting: the paint reads vpNewMessages for the control's
	// label, so a deferred bump would render one message behind.
	r.countNewMessageLocked(f, visible, blocksBefore)
	if repaint {
		r.paintViewportLocked()
	}
}

// countNewMessageLocked bumps the unseen-message counter when a frame added a
// NEW block to the transcript the user is currently looking at while they are
// scrolled away from the bottom. Caller holds r.mu.
//
// Two exclusions keep the count honest. In-place updates grow no block —
// streaming thinking deltas coalesce and a tool block is replaced when it moves
// from running to completed — so one long tool would otherwise inflate the badge
// by one per output chunk. Statuses ("Worked for 12s", "✔ You approved",
// subagent lifecycle) are UI annotations of the surrounding messages, not
// messages of their own.
func (r *Renderer) countNewMessageLocked(f Frame, visible *viewModel, blocksBefore int) {
	if r.vpFollow || f.Kind == FrameStatus {
		return
	}
	if len(visible.blocks) > blocksBefore {
		r.vpNewMessages++
	}
}

// retainFrameLocked routes a frame into the view model it belongs to and
// reports whether the visible transcript changed and needs a repaint. Caller
// holds r.mu.
func (r *Renderer) retainFrameLocked(f Frame) bool {
	// Terminal errors with no owner are global: retain them in the primary
	// transcript and mirror them into the currently visible subagent
	// transcript — unless that view already ends on the same failure. A
	// subagent stopped by a usage limit fails the turn that dispatched it with
	// the very same sentence, so its view would otherwise read the news twice:
	// once as its own "subagent failed" ending and again as the turn's error.
	// An error that names the subagent it belongs to is not global
	// and falls through to the per-agent routing below. A provider
	// error can reach this point after RunEndedMsg has already rendered this
	// turn's final Worked for status. Only move the error ahead of that status
	// when the status is still the transcript tail; once newer blocks exist, the
	// status belongs to an earlier turn and must remain an immutable boundary.
	if f.Kind == FrameError && f.AgentID == "" {
		if n := len(r.vm.blocks); n > 0 {
			tail := r.vm.blocks[n-1].frame
			if tail.Kind == FrameStatus && strings.HasPrefix(tail.Title, "Worked for ") {
				r.vm.insertBeforeLast(func(prev Frame) bool {
					return prev.Kind == FrameStatus && strings.HasPrefix(prev.Title, "Worked for ")
				}, f)
			} else {
				r.vm.append(f)
			}
		} else {
			r.vm.append(f)
		}
		if r.activeView != "" {
			if vm := r.ensurePerAgentVM(r.activeView); !vm.endsOnError(f.Content) {
				vm.append(f)
			}
		}
		return true
	}
	// A frame that names an agent belongs to that agent's own alt-screen view,
	// whatever its kind: its assistant, tool and thinking frames, its plan card,
	// the FrameUser holding the prompt the dispatching agent sent it — which is
	// that view's first message — the error frame naming how its run ended,
	// which is that view's last, and the "✔ You approved …" status confirming a
	// tool call the user authorised on its behalf, which belongs beside the call
	// it authorises rather than in a conversation that never showed the request.
	// SubagentLifecycleCard marks the exception: a frame that names an agent
	// but is the conversation's own account of it — a goal line opening the
	// view of the check that decided it.
	if f.AgentID != "" && !f.SubagentLifecycleCard {
		vm := r.ensurePerAgentVM(f.AgentID)
		if f.Kind == FrameStatus && f.InsertBeforeLastTool {
			vm.insertBeforeLast(func(prev Frame) bool {
				return prev.Kind == FrameTool
			}, f)
		} else {
			vm.replaceOrAppendBlock(f)
		}
		if f.Kind == FrameMemoryCompact || f.Kind == FrameFanout {
			r.syncLiveBlockAnimationLocked()
		}
		return r.activeView == f.AgentID
	}
	// Tool, fanout, memory-compact and skill-install frames replace the
	// previous block with the same StepID (compact running → compact ran,
	// installing → installed).
	if f.Kind == FrameFanout || f.Kind == FrameTool || f.Kind == FrameMemoryCompact || f.Kind == FrameSkillInstall {
		if f.Kind == FrameFanout && f.InsertBeforeLastTool {
			// A plan-review card belongs to the approval exchange that asked
			// for it, so it lands above the parked tool block the approval is
			// holding — between that approval's confirmation lines — and then
			// updates in place, exactly as the lines do.
			r.vm.replaceOrInsertBeforeLastTool(f)
		} else {
			r.vm.replaceOrAppendBlock(f)
		}
		if f.Kind == FrameMemoryCompact || f.Kind == FrameFanout {
			r.syncLiveBlockAnimationLocked()
		}
	} else if f.Kind == FrameStatus && f.InsertBeforeLastTool {
		// Approval confirmations explicitly land above the tool block they
		// authorise, so "✔ You approved" always reads before "● shell ran".
		r.vm.insertBeforeLast(func(prev Frame) bool {
			return prev.Kind == FrameTool
		}, f)
	} else {
		// Ordinary statuses retain producer order. In particular, turn-final
		// "Worked for" and transcript-final "resumed" statuses stay at the tail.
		r.vm.append(f)
	}
	return true
}

// activeVMLocked returns the view model backing the currently displayed view:
// the primary transcript, or the per-agent transcript of the subagent being
// viewed. Caller holds r.mu.
func (r *Renderer) activeVMLocked() *viewModel {
	if r.activeView != "" {
		if vm := r.perAgentVM[r.activeView]; vm != nil {
			return vm
		}
	}
	return &r.vm
}

func (r *Renderer) ensurePerAgentVM(agentID string) *viewModel {
	if r.perAgentVM == nil {
		r.perAgentVM = make(map[string]*viewModel)
	}
	vm, ok := r.perAgentVM[agentID]
	if !ok {
		vm = &viewModel{}
		r.perAgentVM[agentID] = vm
	}
	return vm
}

// ViewportScroll adjusts the viewport scroll offset by delta rows (negative =
// toward older content) and repaints. Any manual scroll disarms follow mode;
// scrolling back to the bottom re-arms it so new frames resume auto-following.
func (r *Renderer) ViewportScroll(delta int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.viewportMode {
		return
	}
	r.scrollViewportLocked(delta, false)
}

// ViewportScrollAt routes a scroll delta to either the transcript, the overlay
// composer, or the normal composer based on the mouse row. When the mouse is in
// the composer area (row >= vpBodyHeight) and a composer overlay is active, the
// overlay content scrolls; when the normal composer is taller than its
// allocated area it scrolls independently; otherwise the transcript scrolls.
//
// When the pointer is over an active overlay but the overlay has no room to
// scroll in the requested direction (a short picker that fits on screen, or the
// offset already pinned at the top/bottom edge), the notch falls through to the
// transcript. This keeps the wheel responsive instead of silently swallowing it,
// and preserves the pre-overlay-scroll behaviour where wheeling during a short
// picker scrolled the conversation above.
func (r *Renderer) ViewportScrollAt(delta, col, row int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.viewportMode {
		return
	}
	if r.overlayActive && row >= r.vpBodyHeight {
		if r.overlayCanScrollLocked(delta) {
			r.scrollViewportLocked(delta, true)
			return
		}
		r.scrollViewportLocked(delta, false)
		return
	}
	if row >= r.vpBodyHeight && r.vpLastComposer.fullLines != nil {
		r.scrollComposerLocked(delta)
		return
	}
	r.scrollViewportLocked(delta, false)
}

// ViewportScrollOverlay adjusts the overlay composer scroll offset by delta rows
// (negative = up / toward the top of the overlay, positive = down) and reports
// whether the overlay actually moved. It returns false when the overlay already
// fits on screen or is already at that end of its content, which is what lets a
// caller leave its view anchored on the row it cares about instead of releasing
// it for a scroll that changed nothing. No-op outside viewport mode.
func (r *Renderer) ViewportScrollOverlay(delta int) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.viewportMode {
		return false
	}
	// Asked before the scroll, and through the same predicate the wheel uses to
	// choose between the overlay and the transcript, so there is one definition
	// of "this overlay has somewhere to go".
	moved := r.overlayCanScrollLocked(delta)
	r.scrollViewportLocked(delta, true)
	return moved
}

// overlayMaxScrollLocked returns the maximum overlay composer scroll offset:
// how many rows the overlay content exceeds its allocated composer area. Mirrors
// the clamp computed in paintViewportLocked, where the pinned separator rule
// permanently consumes one row of that area. Returns 0 when the overlay fits on
// screen. Caller holds r.mu.
func (r *Renderer) overlayMaxScrollLocked() int {
	height := termHeightOrDefault()
	if height < 1 {
		height = 24
	}
	if r.overlayPanel != nil {
		width := maxInt(1, termWidthOrDefault()-viewportRightPadding)
		return r.overlayPanel.layout(width, slashPanelHeight(height)-1, r.overlayScrollOffset, false).maxTop
	}
	minBodyHeight := height / 3
	if minBodyHeight < 3 {
		minBodyHeight = 3
	}
	maxComposerHeight := height - minBodyHeight
	width := termWidthOrDefault() - viewportRightPadding
	if width < 1 {
		width = 1
	}
	// +1 for the separator rule pinned above the content at paint time.
	overlayLines, _ := r.overlayComposerLinesLocked(width)
	fullHeight := len(overlayLines) + 1
	if fullHeight > maxComposerHeight {
		return fullHeight - maxComposerHeight
	}
	return 0
}

// overlayRowRef maps one painted overlay row back to the logical line the
// overlay installed. line is that line's index; logicalCol is the display
// column of the logical line whose content the row resumes at; rowCol is the
// column within the painted row where that content starts, which is the
// indentation a soft-wrapped continuation row carries over from its line.
type overlayRowRef struct {
	line       int
	logicalCol int
	rowCol     int
}

// overlayComposerLinesLocked returns the visual overlay rows for the current
// viewport width, together with the map from each row back to the logical line
// it came from. Most modal overlays already produce visual rows and bypass the
// wrap; user_interaction keeps logical rows so long model-provided text can
// naturally reflow whenever the terminal is resized.
func (r *Renderer) overlayComposerLinesLocked(width int) ([]string, []overlayRowRef) {
	lines, refs := r.overlayContentLinesLocked(width)
	return append(lines, r.overlayNoteRowsLocked(width)...), refs
}

// overlayNoteRowsLocked is the queued-approval note: the renderer's own rows
// under whatever owns the overlay, so it reaches every modal and panel without
// any of them knowing about it. They have no ref: a click on them maps to no
// line.
func (r *Renderer) overlayNoteRowsLocked(width int) []string {
	note := strings.TrimSpace(r.overlayNote)
	if note == "" {
		return nil
	}
	parts := wrapMenuText(note, width-1)
	for i, part := range parts {
		parts[i] = overlayNoteStyle.Render(part)
	}
	return parts
}

// overlayNoteStyle is the queued-approval note's colour: the approval accent,
// at full brightness.
var overlayNoteStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))

func (r *Renderer) overlayContentLinesLocked(width int) ([]string, []overlayRowRef) {
	if !r.overlaySoftWrap || width < 1 {
		refs := make([]overlayRowRef, len(r.overlayComposer.lines))
		for i := range refs {
			refs[i] = overlayRowRef{line: i}
		}
		return selfContainedRows(r.overlayComposer.lines), refs
	}
	lines := make([]string, 0, len(r.overlayComposer.lines))
	refs := make([]overlayRowRef, 0, len(r.overlayComposer.lines))
	for i, line := range r.overlayComposer.lines {
		wrapped := softWrapOverlayLine(line, width)
		lines = append(lines, wrapped...)
		refs = append(refs, overlaySoftWrapRefs(line, wrapped, i)...)
	}
	// Normalise after the refs: they are derived from plain text, and the
	// inserted carry/reset sequences are zero-width, so the line↔ref mapping
	// is untouched. It must cover the WHOLE table before any window slicing,
	// or the first row a scroll exposes would miss the style opened rows
	// above the window.
	return selfContainedRows(lines), refs
}

// overlaySoftWrapRefs recovers, for each visual row a soft-wrapped logical line
// produced, the display column of the logical line its content resumes at. Word
// wrapping consumes the whitespace it breaks at and re-indents continuation
// rows, so the columns are recovered by walking the line's plain text alongside
// the rows that were emitted rather than by summing row widths.
func overlaySoftWrapRefs(logical string, rows []string, lineIdx int) []overlayRowRef {
	// The prefix softWrapOverlayLine re-adds is taken from the raw line
	// exactly as that function takes it, so a row that carries none is not
	// mistakenly stripped.
	_, continuation := overlayWrapPrefix(logical)
	plain := stripAnsi(logical)
	contents := make([]string, len(rows))
	for i, row := range rows {
		content := stripAnsi(row)
		if i > 0 && continuation != "" && strings.HasPrefix(content, continuation) {
			content = content[len(continuation):]
		}
		contents[i] = content
	}
	starts := plainWrapRowStarts(plain, contents)
	runes := []rune(plain)
	refs := make([]overlayRowRef, len(rows))
	for i := range rows {
		rowCol := 0
		if i > 0 {
			rowCol = runewidth.StringWidth(continuation)
		}
		refs[i] = overlayRowRef{line: lineIdx, logicalCol: visualColAtRuneIndex(runes, starts[i]), rowCol: rowCol}
	}
	return refs
}

// OverlayClickPosition maps 0-based screen coordinates to a position inside the
// modal overlay currently on screen: the index of the line the overlay
// installed and the display column within that line. It is what lets a modal
// place its own text caret where the user clicked — the overlay knows which of
// its lines is an input field and what precedes the text on it, and this
// answers where in that line the click landed.
func (r *Renderer) OverlayClickPosition(col, row int) (line, column int, ok bool) {
	if r == nil {
		return 0, 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.viewportMode || !r.overlayActive {
		return 0, 0, false
	}
	// The block is bottom-pinned and its first row is the pinned separator.
	idx := row - r.vpBodyHeight - 1
	if idx < 0 {
		return 0, 0, false
	}
	idx += r.vpOverlayFirstRow
	if idx >= len(r.vpOverlayRefs) {
		return 0, 0, false
	}
	ref := r.vpOverlayRefs[idx]
	column = ref.logicalCol + col - ref.rowCol
	if column < ref.logicalCol {
		column = ref.logicalCol
	}
	return ref.line, column, true
}

func softWrapOverlayLine(line string, width int) []string {
	if width < 1 {
		return []string{line}
	}
	// Expand special markers for dynamic-width content.
	if line == "\x00QSEP\x00" {
		// Question overlay separator: generate a full-width rule at the current
		// terminal width, minus right padding. This allows the separator to
		// adapt to terminal resizes without re-rendering the entire overlay.
		const viewportRightPadding = 2
		sepWidth := width - viewportRightPadding
		if sepWidth < 1 {
			sepWidth = 1
		}
		return []string{"\x1b[2m" + strings.Repeat("─", sepWidth) + "\x1b[0m"}
	}
	// Continuation rows hang under the line's text: the gutter is repeated
	// and the indentation and bullet become spaces, so a wrapped row neither
	// falls out of the gutter nor jumps back under its bullet.
	prefix, continuation := overlayWrapPrefix(line)
	contentWidth := width - runewidth.StringWidth(prefix)
	if contentWidth < 1 {
		prefix, continuation, contentWidth = "", "", width
	}

	// WrapWc is ANSI-aware, respects terminal-cell widths (including CJK),
	// prefers whitespace boundaries, and hard-wraps a single long token.
	wrapped := strings.Split(xansi.WrapWc(line[len(prefix):], contentWidth, ""), "\n")
	for i := range wrapped {
		if i == 0 {
			wrapped[i] = prefix + wrapped[i]
		} else {
			wrapped[i] = continuation + wrapped[i]
		}
	}
	return wrapped
}

// overlayWrapPrefix splits off what a wrapped overlay line's continuation
// rows hang under — its indentation and a list bullet — and returns the
// prefix those rows carry instead: spaces as wide as it. A line that is
// nothing but its prefix has none, so a blank row is left alone.
func overlayWrapPrefix(line string) (prefix, continuation string) {
	i := 0
	for i < len(line) && line[i] == ' ' {
		i++
	}
	for _, bullet := range []string{"· ", "↳ ", "• ", "- "} {
		if strings.HasPrefix(line[i:], bullet) {
			i += len(bullet)
			break
		}
	}
	if i == len(line) {
		return "", ""
	}
	prefix = line[:i]
	return prefix, strings.Repeat(" ", runewidth.StringWidth(prefix))
}

// overlayCanScrollLocked reports whether the overlay composer can move by delta
// rows (negative = up / toward older content, positive = down) from its current
// offset. Used by ViewportScrollAt to decide whether a wheel notch over the
// overlay scrolls the overlay or falls through to the transcript. Caller holds
// r.mu.
func (r *Renderer) overlayCanScrollLocked(delta int) bool {
	maxScroll := r.overlayMaxScrollLocked()
	if maxScroll <= 0 {
		return false
	}
	if delta < 0 {
		return r.overlayScrollOffset > 0
	}
	return r.overlayScrollOffset < maxScroll
}

// scrollViewportLocked applies a scroll delta to either the viewport transcript
// (scrollOverlay=false) or the overlay composer (scrollOverlay=true). Caller
// holds r.mu.
func (r *Renderer) scrollViewportLocked(delta int, scrollOverlay bool) {
	if scrollOverlay && r.overlayActive {
		r.overlayScrollOffset += delta
	} else {
		maxOffset := r.vpLastRender.maxScrollOffset()
		next := r.vpScrollOffset + delta
		if next < 0 {
			next = 0
		}
		if next > maxOffset {
			next = maxOffset
		}
		r.vpScrollOffset = next
		r.vpFollow = next >= maxOffset
		if r.vpFollow {
			// Scrolling back down to the newest rows counts as reading them.
			r.vpNewMessages = 0
		}
	}
	r.vpHoverRow = -1
	r.selectClearLocked()
	r.paintViewportLocked()
}

// scrollComposerLocked applies a scroll delta to a normal composer whose
// rendered height exceeds its allocated screen area. Caller holds r.mu.
func (r *Renderer) scrollComposerLocked(delta int) {
	if r.vpLastComposer.fullLines == nil {
		return
	}
	height := termHeightOrDefault()
	if height < 1 {
		height = 24
	}
	minBodyHeight := height / 3
	if minBodyHeight < 3 {
		minBodyHeight = 3
	}
	maxComposerHeight := height - minBodyHeight
	if maxComposerHeight < 1 {
		maxComposerHeight = 1
	}
	fullHeight := len(r.vpLastComposer.fullLines)
	maxScroll := fullHeight - maxComposerHeight
	if maxScroll <= 0 {
		r.composerScrollOffset = 0
		return
	}
	next := r.composerScrollOffset + delta
	if next < 0 {
		next = 0
	}
	if next > maxScroll {
		next = maxScroll
	}
	if next == r.composerScrollOffset {
		return
	}
	r.composerScrollOffset = next
	r.vpHoverRow = -1
	r.selectClearLocked()
	r.paintViewportLocked()
}

// ViewportClickToggle hit-tests a mouse click at the given 0-based screen
// coordinates against the last paint. When it lands on a collapsible block's
// header it toggles that block's expand state and repaints, returning true.
func (r *Renderer) ViewportClickToggle(col, row int) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.viewportMode {
		return false
	}
	if row < 0 || row >= r.vpHeight {
		return false
	}
	if r.jumpToBottomButtonHitLocked(col, row) {
		r.jumpToBottomLocked()
		return true
	}
	activeVM := &r.vm
	if r.activeView != "" {
		if vm := r.perAgentVM[r.activeView]; vm != nil {
			activeVM = vm
		}
	}
	r.selectClearLocked()
	if r.openAgentViewAtRowLocked(row) {
		return true
	}
	if activeVM.toggleAtScreenRow(r.vpLastRender, row) {
		r.paintViewportLocked()
		return true
	}
	return false
}

// ViewportHover records the 0-based screen row under the mouse cursor so the
// next paint can apply a subtle background highlight to clickable rows.
// Repaints only when the hover row actually changes (avoids thrashing on
// sub-pixel moves within the same row). col/row are screen coordinates; row
// outside the viewport body clears the hover state so the highlight disappears
// when the cursor leaves the transcript area.
func (r *Renderer) ViewportHover(col, row int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.viewportMode {
		return
	}
	_ = col
	if row < 0 || row >= r.vpHeight {
		row = -1
	}
	if r.vpHoverRow == row {
		return
	}
	r.vpHoverRow = row
	r.paintViewportLocked()
}

// ViewportResize repaints after a terminal size change (SIGWINCH). The new
// dimensions are read fresh inside the paint, and every block's line cache keys
// on width so re-wrapping happens automatically.
func (r *Renderer) ViewportResize() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.viewportMode {
		return
	}
	r.vpHoverRow = -1
	r.selectClearLocked()
	// A resize reflows the terminal's grid however it likes, so the shadow of
	// the pre-resize screen describes nothing: repaint every row. A SIGWINCH
	// that reports the same size gets the same treatment, since the terminal
	// may still have redrawn itself.
	r.dropPaintShadowLocked()
	r.paintViewportLocked()
}

// ViewportResetSession clears the retained transcript on a session switch and
// repaints an empty viewport. Caller-facing analogue of the append path's
// reliance on switchStreamSession to reset the screen.
func (r *Renderer) ViewportResetSession() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.viewportMode {
		return
	}
	r.vm.reset()
	r.syncLiveBlockAnimationLocked()
	r.perAgentVM = nil
	r.assignActiveViewLocked("")
	r.viewBrowseState = nil
	r.vpScrollOffset = 0
	r.vpFollow = true
	r.vpNewMessages = 0
	r.vpHoverRow = -1
	r.composerScrollOffset = 0
	r.overlayScrollOffset = 0
	r.selectClearLocked()
	r.paintViewportLocked()
}

func stripAnsi(s string) string {
	return sanitizeTerminalInputText(s)
}

// stripTerminalBells removes ASCII BEL (0x07) characters from strings before
// they are written to the terminal. Model/tool output can legitimately contain
// raw control bytes, and emitting a BEL causes Terminal.app (and many other
// emulators) to show a bell icon on the tab and play the alert sound.
func stripTerminalBells(s string) string {
	return strings.ReplaceAll(s, "\x07", "")
}

func extractColumnRange(plain string, startCol, endCol int) string {
	if startCol < 0 {
		startCol = 0
	}
	var result strings.Builder
	visualCol := 0
	baseInRange := false
	for _, r := range plain {
		w := lipgloss.Width(string(r))
		if w <= 0 {
			// Zero-width runes (combining marks, ZWJ) share the cell of the
			// preceding base rune; emit them iff that base cell is selected.
			if baseInRange {
				result.WriteRune(r)
			}
			continue
		}
		// A rune occupying [visualCol, visualCol+w) is selected when its cell
		// range overlaps [startCol, endCol). This keeps wide runes (CJK/emoji,
		// w=2) aligned with the terminal's actual layout instead of treating
		// every rune as a single column (which drifted selections on CJK text).
		// Using lipgloss.Width instead of runewidth.RuneWidth so that East Asian
		// Ambiguous characters (e.g. ● U+25CF) are measured at their true
		// terminal display width (1) rather than the ambiguous width (2).
		baseInRange = visualCol+w > startCol && (endCol < 0 || visualCol < endCol)
		if baseInRange {
			result.WriteRune(r)
		}
		visualCol += w
	}
	return result.String()
}

// applyHoverHighlight repaints one transcript row under the hover background.
// lipgloss emits \x1b[0m after each styled segment; the background is
// re-applied after every reset so the whole row stays highlighted. The fill to
// the right edge is a run of explicit spaces rather than \x1b[K: terminals
// without back-color-erase (macOS Terminal.app) erase to the default
// background, which would stop the highlight after the text.
func applyHoverHighlight(line string, width int, hoverBgSeq string) string {
	highlighted := strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+hoverBgSeq)
	pad := strings.Repeat(" ", max(width-lipgloss.Width(line), 0))
	return hoverBgSeq + highlighted + pad + "\x1b[0m"
}

func applySelectionHighlight(line, selectBgSeq string, startCol, endCol int) string {
	if startCol < 0 {
		startCol = 0
	}
	var result strings.Builder
	visualCol := 0
	inSelection := false
	i := 0
	for i < len(line) {
		if line[i] == '\x1b' {
			j := i + 1
			if j < len(line) && line[j] == '[' {
				for j < len(line) && !((line[j] >= 'A' && line[j] <= 'Z') || (line[j] >= 'a' && line[j] <= 'z')) {
					j++
				}
				if j < len(line) {
					j++
				}
			} else {
				for j < len(line) && line[j] != 'm' && line[j] != '\x07' && line[j] != '\\' {
					j++
				}
				if j < len(line) {
					j++
				}
			}
			code := line[i:j]
			result.WriteString(code)
			if code == "\x1b[0m" && inSelection {
				result.WriteString(selectBgSeq)
			}
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		w := lipgloss.Width(string(r))
		if w <= 0 {
			// Zero-width rune: inherit the current highlight state and do not
			// advance the column so combining marks stay with their base.
			result.WriteRune(r)
			i += size
			continue
		}
		// A rune's cells [visualCol, visualCol+w) are highlighted when they
		// overlap [startCol, endCol); wide runes are counted at their real
		// width so the highlight tracks the terminal layout, not rune count.
		wantSel := visualCol+w > startCol && (endCol < 0 || visualCol < endCol)
		if wantSel && !inSelection {
			result.WriteString(selectBgSeq)
			inSelection = true
		} else if !wantSel && inSelection {
			result.WriteString("\x1b[49m")
			inSelection = false
		}
		result.WriteRune(r)
		visualCol += w
		i += size
	}
	return result.String()
}

const doubleClickThreshold = 400 * time.Millisecond

// composerRowBase encodes composer-block rows in a sentinel space above any
// possible transcript row. Selection anchors in the transcript are stored in
// absolute transcript-row coordinates (stable across scrolling); composer rows
// are bottom-pinned, so they live in [composerRowBase, ∞). When the composer
// content is taller than the allocated area it can be vertically scrolled
// internally; the scroll offset is stored separately and added on demand.
const composerRowBase = 1 << 20

// autoScrollInterval is how often the drag-past-edge ticker scrolls the
// transcript one row and extends the selection while the mouse is held at an
// edge without moving (so no further drag events arrive).
const autoScrollInterval = 60 * time.Millisecond

const jumpToBottomButtonLabel = " Jump to bottom (ctrl+End) ↓ "

// jumpToBottomButtonText returns the control's text for the given number of
// unseen messages. With none it stays a plain "Jump to bottom" affordance; once
// messages have arrived below the viewport it reports how many, matching the
// Claude Code badge ("1 new message (ctrl+End) ↓").
func jumpToBottomButtonText(newMessages int) string {
	if newMessages < 1 {
		return jumpToBottomButtonLabel
	}
	noun := "messages"
	if newMessages == 1 {
		noun = "message"
	}
	return fmt.Sprintf(" %d new %s (ctrl+End) ↓ ", newMessages, noun)
}

// jumpToBottomButtonLayout returns the visible label and its 0-based screen
// bounds. The control floats over the last transcript row, immediately above
// the composer's spacer, and is centered across the terminal like the Claude
// Code reference interaction. Narrow terminals receive a clipped label so the
// paint never triggers an implicit terminal wrap.
func jumpToBottomButtonLayout(width, bodyHeight, newMessages int) (label string, startCol, endCol, row int) {
	if width < 1 || bodyHeight < 1 {
		return "", 0, 0, -1
	}
	maxButtonWidth := width
	if maxButtonWidth > 2 {
		maxButtonWidth -= 2
	}
	label = runewidth.Truncate(jumpToBottomButtonText(newMessages), maxButtonWidth, "")
	buttonWidth := runewidth.StringWidth(label)
	startCol = (width - buttonWidth) / 2
	return label, startCol, startCol + buttonWidth, bodyHeight - 1
}

// jumpToBottomButtonHitLocked reports whether a screen coordinate lands on
// the floating control from the last viewport paint. Caller holds r.mu.
func (r *Renderer) jumpToBottomButtonHitLocked(col, row int) bool {
	if r.vpFollow || r.vpLastRender.maxScrollOffset() <= 0 {
		return false
	}
	width := termWidthOrDefault()
	if width < 1 {
		width = 80
	}
	_, startCol, endCol, buttonRow := jumpToBottomButtonLayout(width, r.vpBodyHeight, r.vpNewMessages)
	return row == buttonRow && col >= startCol && col < endCol
}

// jumpToBottomLocked restores follow mode and repaints with the newest
// transcript rows visible. Caller holds r.mu.
func (r *Renderer) jumpToBottomLocked() {
	r.vpScrollOffset = r.vpLastRender.maxScrollOffset()
	r.vpFollow = true
	r.vpHoverRow = -1
	r.vpJumpToBottomPressed = false
	r.vpNewMessages = 0
	r.selectClearLocked()
	r.paintViewportLocked()
}

// ViewportJumpToBottom is the keyboard/public entry point for the same action
// as clicking the floating control.
func (r *Renderer) ViewportJumpToBottom() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.viewportMode {
		return
	}
	r.jumpToBottomLocked()
}

// absRowForScreenRow maps a 0-based screen row to the coordinate space
// selection anchors are stored in: absolute transcript row for body rows,
// composerRowBase-offset for composer rows. Caller holds r.mu.
func (r *Renderer) absRowForScreenRow(row int) int {
	if row < r.vpBodyHeight {
		return r.vpLastRender.firstRow + row
	}
	return composerRowBase + (row - r.vpBodyHeight) + r.vpLastComposer.scrollOffset
}

// lineForAbsRow returns the rendered line at the given absolute selection row.
// Transcript rows index the full (un-scrolled) transcript so a selection can
// span rows currently off-screen; composer rows (>= composerRowBase) index the
// bottom-pinned composer block. Out-of-range rows yield "".
func (r *Renderer) lineForAbsRow(row int) string {
	if row >= composerRowBase {
		ci := row - composerRowBase
		lines := r.vpLastComposer.lines
		if r.vpLastComposer.fullLines != nil {
			lines = r.vpLastComposer.fullLines
		}
		if ci >= 0 && ci < len(lines) {
			return lines[ci]
		}
		return ""
	}
	all := r.vpLastRender.allLines
	if row >= 0 && row < len(all) {
		return all[row]
	}
	return ""
}

func (r *Renderer) ViewportSelectStart(col, row int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.viewportMode {
		return
	}
	totalHeight := r.vpBodyHeight + len(r.vpLastComposer.lines)
	if row < 0 || row >= totalHeight {
		return
	}
	if r.jumpToBottomButtonHitLocked(col, row) {
		r.stopAutoScrollLocked()
		r.selectClearLocked()
		r.selectDragging = false
		r.vpJumpToBottomPressed = true
		return
	}
	r.vpJumpToBottomPressed = false
	r.stopAutoScrollLocked()
	r.selectDragging = true
	now := time.Now()
	// Multi-click detection: consecutive presses on the same cell within the
	// threshold advance the click count 1->2->3->1. Position match is exact-cell
	// (matching the prior double-click behavior). Click 2 selects a word, click 3
	// selects the whole line; click 1 starts a fresh char-range selection.
	continuing := !r.lastClickTime.IsZero() &&
		now.Sub(r.lastClickTime) <= doubleClickThreshold &&
		r.lastClickCol == col && r.lastClickRow == row
	r.lastClickTime = now
	r.lastClickCol = col
	r.lastClickRow = row
	if continuing {
		r.clickCount++
		if r.clickCount > 3 {
			r.clickCount = 1
		}
	} else {
		r.clickCount = 1
	}

	switch r.clickCount {
	case 2:
		r.selectWordAtLocked(col, row)
		return
	case 3:
		r.selectLineAtLocked(col, row)
		return
	}

	abs := r.absRowForScreenRow(row)
	r.selectStartCol = col
	r.selectStartRow = abs
	r.selectEndCol = col
	r.selectEndRow = abs
	r.selectDidDrag = false
	r.vpHoverRow = -1
	r.paintViewportLocked()
}

func (r *Renderer) ViewportSelectDrag(col, row int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.viewportMode {
		return
	}
	if r.vpJumpToBottomPressed {
		// Keep consuming motion until release so a drag that started on the
		// button cannot become a transcript selection/toggle underneath it. The
		// release coordinate decides whether the click still activates.
		return
	}
	// A drag extends only the END anchor; the START is fixed at the press
	// position (set in ViewportSelectStart). Ignore motion that arrives with no
	// active press (selectStartRow<0) - e.g. a stray motion report before the
	// press event, which would otherwise seed a selection from the wrong cell.
	if r.selectStartRow < 0 {
		return
	}
	totalHeight := r.vpBodyHeight + len(r.vpLastComposer.lines)
	if row < 0 {
		row = 0
	}
	if row >= totalHeight {
		row = totalHeight - 1
	}

	// Edge-driven auto-scroll: while the drag sits on the top or bottom body
	// row, run a ticker that scrolls the transcript and extends the selection,
	// so the user can select content beyond the current screen without the
	// mouse having to keep moving. Drags inside the body interior or over the
	// composer stop the ticker.
	switch {
	case r.vpBodyHeight > 0 && row <= 0:
		r.startAutoScrollLocked(-1, col)
	case r.vpBodyHeight > 0 && row == r.vpBodyHeight-1:
		r.startAutoScrollLocked(1, col)
	default:
		r.stopAutoScrollLocked()
	}

	r.updateSelectEndLocked(col, row)
}

// updateSelectEndLocked moves the selection's end anchor to the given screen
// coordinate (converted to absolute row space) and repaints if it changed.
// Caller holds r.mu.
func (r *Renderer) updateSelectEndLocked(col, row int) {
	abs := r.absRowForScreenRow(row)
	if r.selectEndCol == col && r.selectEndRow == abs {
		return
	}
	r.selectEndCol = col
	r.selectEndRow = abs
	r.selectDidDrag = true
	r.vpHoverRow = -1
	r.paintViewportLocked()
}

// startAutoScrollLocked launches (or retargets) the drag auto-scroll ticker.
// dir is -1 to scroll up, +1 to scroll down. Idempotent for a running
// direction — only the drag column is refreshed. Caller holds r.mu.
func (r *Renderer) startAutoScrollLocked(dir, col int) {
	r.autoScrollCol = col
	if r.autoScrollDir == dir && r.autoScrollStop != nil {
		return
	}
	r.stopAutoScrollLocked()
	r.autoScrollDir = dir
	stop := make(chan struct{})
	r.autoScrollStop = stop
	go r.autoScrollLoop(dir, stop)
}

// stopAutoScrollLocked halts the auto-scroll ticker if running. Caller holds
// r.mu.
func (r *Renderer) stopAutoScrollLocked() {
	if r.autoScrollStop != nil {
		close(r.autoScrollStop)
		r.autoScrollStop = nil
	}
	r.autoScrollDir = 0
}

// autoScrollLoop ticks until superseded/stopped, scrolling one row per tick and
// extending the selection edge. It re-checks ownership under the lock each tick
// so a stale goroutine (after stop/retarget) exits cleanly.
func (r *Renderer) autoScrollLoop(dir int, stop chan struct{}) {
	ticker := time.NewTicker(autoScrollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			r.mu.Lock()
			if r.autoScrollStop != stop || !r.selectDidDrag {
				r.mu.Unlock()
				return
			}
			r.autoScrollStepLocked(dir)
			r.mu.Unlock()
		}
	}
}

// autoScrollStepLocked scrolls the transcript one row in dir and pins the
// selection end to the newly-exposed edge row. Caller holds r.mu.
func (r *Renderer) autoScrollStepLocked(dir int) {
	maxOffset := r.vpLastRender.maxScrollOffset()
	next := r.vpScrollOffset + dir
	if next < 0 {
		next = 0
	}
	if next > maxOffset {
		next = maxOffset
	}
	if next == r.vpScrollOffset {
		// Already at the transcript edge — nothing new to reveal.
		return
	}
	r.vpScrollOffset = next
	r.vpFollow = next >= maxOffset
	if r.vpFollow {
		r.vpNewMessages = 0
	}

	edgeAbs := next
	if dir > 0 {
		edgeAbs = next + r.vpBodyHeight - 1
	}
	if total := r.vpLastRender.totalLines; total > 0 && edgeAbs > total-1 {
		edgeAbs = total - 1
	}
	r.selectEndRow = edgeAbs
	r.selectEndCol = r.autoScrollCol
	r.selectDidDrag = true
	r.vpHoverRow = -1
	r.paintViewportLocked()
}

// ViewportSelectEnd completes a mouse gesture. It reports whether the gesture
// was a plain click — a press and release with no drag between them — so the
// modal overlay that owns the screen can place its own text caret at the cell
// the user clicked. A click that lands on the composer's text moves the
// composer caret here, since the renderer is what knows where each rune of the
// buffer was painted.
func (r *Renderer) ViewportSelectEnd(col, row int) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	if !r.viewportMode {
		r.mu.Unlock()
		return false
	}
	r.stopAutoScrollLocked()
	r.selectDragging = false
	if r.vpJumpToBottomPressed {
		r.vpJumpToBottomPressed = false
		if r.jumpToBottomButtonHitLocked(col, row) {
			r.jumpToBottomLocked()
		}
		r.mu.Unlock()
		return false
	}
	if !r.selectDidDrag {
		r.selectClearLocked()
		r.vpHoverRow = -1
		caret, placeCaret := r.composerCaretAtLocked(col, row)
		if row >= 0 && row < r.vpHeight {
			activeVM := &r.vm
			if r.activeView != "" {
				if vm := r.perAgentVM[r.activeView]; vm != nil {
					activeVM = vm
				}
			}
			if !r.openAgentViewAtRowLocked(row) {
				if toggled := activeVM.toggleAtScreenRow(r.vpLastRender, row); toggled {
					r.paintViewportLocked()
				}
			}
		}
		r.mu.Unlock()
		if placeCaret {
			// The raw input reader owns the buffer, so the caret is requested
			// rather than written here; it answers with a draft event that
			// repaints the card with the caret on the clicked cell.
			requestComposerCaret(caret)
		}
		return true
	}
	text := r.extractSelectedTextLocked()
	// Persist the selection: keep the anchors so the highlight stays after the
	// text is captured - matching a native terminal where a double/triple-click
	// or drag selection remains highlighted until the user clicks elsewhere. The
	// hardware cursor is restored because selectDragging is now false (cursor
	// visibility is decoupled from the highlight in paintViewportLocked). The
	// anchors are cleared by the next single-click press, or by scroll/resize/
	// session-reset/toggle. A previous version cleared here to avoid stranding
	// the caret; that is no longer needed now that cursor-hide keys off
	// selectDragging instead of the selection anchors.
	r.vpHoverRow = -1
	if text != "" {
		r.setClipNotification(fmt.Sprintf("Copied %d chars to clipboard", len([]rune(text))))
	}
	r.paintViewportLocked()
	r.mu.Unlock()

	if text == "" {
		return false
	}
	go func() {
		clipErr := writeTextToClipboard(context.Background(), text)
		if clipErr != nil {
			r.mu.Lock()
			r.writeClipboardOSC52Locked(text)
			r.mu.Unlock()
		}
	}()
	return false
}

// composerCaretAtLocked maps 0-based screen coordinates to a caret position in
// the composer buffer, or false when the cell shows something other than the
// composer's own text. The composer block is bottom-pinned, so its first row is
// the one just below the transcript body; a vertically scrolled composer adds
// its scroll offset to reach the row's index in the full block. Caller holds
// r.mu.
func (r *Renderer) composerCaretAtLocked(col, row int) (int, bool) {
	if r.overlayActive || r.composerSuppressed {
		return 0, false
	}
	if row < r.vpBodyHeight {
		return 0, false
	}
	return r.vpLastComposer.text.caretAt(row-r.vpBodyHeight+r.vpLastComposer.scrollOffset, col)
}

func (r *Renderer) ViewportSelectClear() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.selectClearLocked()
}

func (r *Renderer) selectClearLocked() {
	r.selectDidDrag = false
	r.selectDragging = false
	r.vpJumpToBottomPressed = false
	r.selectStartRow = -1
	r.selectEndRow = -1
	r.selectStartCol = -1
	r.selectEndCol = -1
}

// runeIndexAtVisualCol returns the index in runes of the rune whose display
// cells contain visual column col. A wide rune (CJK/emoji) occupying cells
// [c, c+1] maps both c and c+1 to itself; zero-width runes are skipped so a
// click lands on the base rune they modify. Returns len(runes) when col is
// past the last cell. This mirrors the runewidth-based wrap math in
// renderer.go so mouse columns map to the rune the terminal actually shows.
func runeIndexAtVisualCol(runes []rune, col int) int {
	if col < 0 {
		return 0
	}
	visualCol := 0
	for i, r := range runes {
		w := lipgloss.Width(string(r))
		if w > 0 {
			if col < visualCol+w {
				return i
			}
			visualCol += w
		}
	}
	return len(runes)
}

// visualColAtRuneIndex returns the display column at the start of runes[idx],
// i.e. the summed cell width of runes[0:idx]. Used to convert rune-index word
// boundaries back into the visual-column space selection anchors are stored in.
func visualColAtRuneIndex(runes []rune, idx int) int {
	if idx < 0 {
		return 0
	}
	if idx > len(runes) {
		idx = len(runes)
	}
	visualCol := 0
	for i := 0; i < idx; i++ {
		visualCol += lipgloss.Width(string(runes[i]))
	}
	return visualCol
}

// Click-to-caret: resolving a mouse click on a rendered row back to a caret
// position inside the text field that row displays.
//
// Every editable surface in the TUI paints its text through a row of fixed
// decoration (a prompt marker, an indent, a label, an option number) followed
// by the buffer's own runes. Placing the caret where the user clicked is
// therefore always the same three steps: find the row, subtract the decoration
// that precedes the text on it, and translate the remaining display columns
// into a rune offset. The helpers below are that arithmetic, shared by the
// composer and by every modal overlay so the gesture behaves identically
// wherever the user clicks.

// caretIndexForClick maps a click at 0-based display column col on a row that
// renders prefixWidth columns of decoration followed by text to the rune offset
// in text where an insertion should happen. A click on the decoration lands at
// the start of the text; a click past the last cell lands at its end; a click on
// either cell of a wide rune lands before that rune.
func caretIndexForClick(text []rune, prefixWidth, col int) int {
	if col <= prefixWidth {
		return 0
	}
	return runeIndexAtVisualCol(text, col-prefixWidth)
}

// caretIndexAroundBlock maps a click to a rune offset in a buffer that is
// rendered as before + a one-cell caret block + after, which is how the modal
// overlays draw a software caret inside their text fields. The caret cell is
// not part of the buffer, so columns past it are shifted back by one before
// they are resolved; a click on the caret cell itself keeps the caret where it
// is.
func caretIndexAroundBlock(buf []rune, cursor, prefixWidth, caretWidth, col int) int {
	cursor = composerCursorClampRunes(buf, cursor)
	beforeWidth := visualColAtRuneIndex(buf, cursor)
	textCol := col - prefixWidth
	if textCol <= 0 {
		return 0
	}
	if textCol < beforeWidth {
		return runeIndexAtVisualCol(buf[:cursor], textCol)
	}
	if textCol < beforeWidth+caretWidth {
		return cursor
	}
	return cursor + runeIndexAtVisualCol(buf[cursor:], textCol-beforeWidth-caretWidth)
}

// plainWrapRowStarts returns, for each visual row that wrapping a plain
// (escape-free) line produced, the rune offset in that line where the row's
// content starts. Word wrapping consumes the whitespace it breaks at, so the
// offsets cannot be recovered by summing row lengths: the walk below skips
// exactly the runes the wrapper dropped by advancing until the source text
// matches the row that was emitted.
func plainWrapRowStarts(line string, rows []string) []int {
	source := []rune(line)
	starts := make([]int, len(rows))
	pos := 0
	for i, row := range rows {
		content := []rune(row)
		for pos < len(source) && !runesHavePrefixAt(source, pos, content) {
			pos++
		}
		starts[i] = pos
		pos += len(content)
	}
	return starts
}

// runesHavePrefixAt reports whether source, read from pos, begins with prefix.
func runesHavePrefixAt(source []rune, pos int, prefix []rune) bool {
	if pos+len(prefix) > len(source) {
		return false
	}
	for i, r := range prefix {
		if source[pos+i] != r {
			return false
		}
	}
	return true
}

func (r *Renderer) selectWordAtLocked(col, row int) {
	var line string
	if row < r.vpBodyHeight {
		if row < len(r.vpLastRender.lines) {
			line = r.vpLastRender.lines[row]
		}
	} else {
		ci := row - r.vpBodyHeight
		if ci < len(r.vpLastComposer.lines) {
			line = r.vpLastComposer.lines[ci]
		}
	}
	plain := stripAnsi(line)
	runes := []rune(plain)
	if len(runes) == 0 {
		return
	}

	// Map the clicked visual column to a rune index. Wide runes occupy two
	// cells, so a click on either cell selects that rune; without this the
	// mouse column was used directly as a rune index and drifted on CJK text.
	clickIdx := runeIndexAtVisualCol(runes, col)
	if clickIdx >= len(runes) {
		clickIdx = len(runes) - 1
	}
	if clickIdx < 0 {
		clickIdx = 0
	}

	startIdx := clickIdx
	endIdx := clickIdx
	isWordChar := func(r rune) bool {
		return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
	}
	clickRune := runes[clickIdx]
	clickIsWord := isWordChar(clickRune)

	if clickIsWord {
		for startIdx > 0 && isWordChar(runes[startIdx-1]) {
			startIdx--
		}
		for endIdx < len(runes)-1 && isWordChar(runes[endIdx+1]) {
			endIdx++
		}
		endIdx++
	} else {
		for startIdx > 0 && !isWordChar(runes[startIdx-1]) && !unicode.IsSpace(runes[startIdx-1]) {
			startIdx--
		}
		for endIdx < len(runes)-1 && !isWordChar(runes[endIdx+1]) && !unicode.IsSpace(runes[endIdx+1]) {
			endIdx++
		}
		endIdx++
	}

	abs := r.absRowForScreenRow(row)
	r.selectStartRow = abs
	// Store visual columns (not rune indices) so extractColumnRange and
	// applySelectionHighlight - which operate in screen-cell space - extract
	// and highlight exactly the clicked word even when it contains wide runes.
	r.selectStartCol = visualColAtRuneIndex(runes, startIdx)
	r.selectEndRow = abs
	r.selectEndCol = visualColAtRuneIndex(runes, endIdx)
	r.selectDidDrag = true
	r.vpHoverRow = -1
	r.paintViewportLocked()
}

// selectLineAtLocked selects the entire visual row at (col, row), used for a
// triple-click. It covers the whole rendered line content: startCol=0 and
// endCol=full visual width, which both extractColumnRange and
// applySelectionHighlight honor as "through the end of the line". Like
// selectWordAtLocked it sets selectDidDrag so the subsequent release copies the
// line and persists the highlight. The transcript stores wrapped visual rows
// separately, so this selects a single visual row (not the wrapped logical
// line).
func (r *Renderer) selectLineAtLocked(col, row int) {
	_ = col
	var line string
	if row < r.vpBodyHeight {
		if row < len(r.vpLastRender.lines) {
			line = r.vpLastRender.lines[row]
		}
	} else {
		ci := row - r.vpBodyHeight
		if ci < len(r.vpLastComposer.lines) {
			line = r.vpLastComposer.lines[ci]
		}
	}
	// endCol is the visual width of the line content so the selection is an
	// explicit [0, width) range that captures every rune (wide runes counted at
	// their real cell width). An empty line yields width 0 -> zero-length
	// anchor, which still copies "" and releases cleanly.
	endCol := visualColAtRuneIndex([]rune(stripAnsi(line)), len([]rune(stripAnsi(line))))
	abs := r.absRowForScreenRow(row)
	r.selectStartRow = abs
	r.selectStartCol = 0
	r.selectEndRow = abs
	r.selectEndCol = endCol
	r.selectDidDrag = true
	r.vpHoverRow = -1
	r.paintViewportLocked()
}

func (r *Renderer) extractSelectedTextLocked() string {
	startRow, endRow := sortRows(r.selectStartRow, r.selectEndRow)
	if startRow < 0 {
		return ""
	}
	selStartCol := r.selectStartCol
	selEndCol := r.selectEndCol
	if r.selectStartRow > r.selectEndRow {
		selStartCol, selEndCol = selEndCol, selStartCol
	}

	var parts []string
	for row := startRow; row <= endRow; row++ {
		line := r.lineForAbsRow(row)
		plain := stripAnsi(line)
		startCol := 0
		if row == startRow {
			startCol = selStartCol
		}
		endCol := -1
		if row == endRow {
			endCol = selEndCol
		}
		if startRow == endRow {
			startCol = selStartCol
			endCol = selEndCol
			if startCol > endCol {
				startCol, endCol = endCol, startCol
			}
		}
		extracted := extractColumnRange(plain, startCol, endCol)
		parts = append(parts, strings.TrimRight(extracted, " \t"))
	}
	return strings.Join(parts, "\n")
}

func (r *Renderer) writeClipboardOSC52Locked(text string) {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	// Terminate with ST (ESC \) rather than BEL so Terminal.app does not ring
	// the terminal bell when using OSC 52 as a clipboard fallback.
	r.writeTTYOrderedLocked("\x1b]52;c;%s\x1b\\", encoded)
}

func (r *Renderer) setClipNotification(text string) {
	r.clipNotification = text
	if r.clipNotifyTimer != nil {
		r.clipNotifyTimer.Stop()
	}
	r.clipNotifyTimer = time.AfterFunc(3*time.Second, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.clipNotification = ""
		r.paintViewportLocked()
	})
}

// clipboardImageHintText is the composer hint shown while the clipboard holds
// an image.
const clipboardImageHintText = "Image in clipboard · ctrl+v to paste"

// SetClipboardImageAvailable records whether the clipboard currently holds an
// image and repaints when that changes.
func (r *Renderer) SetClipboardImageAvailable(on bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.clipboardImage == on {
		return
	}
	r.clipboardImage = on
	if r.viewportMode {
		r.paintViewportLocked()
	}
}

// clipboardImageHintLine is the row above the composer's top rule: empty, or
// the paste hint right-aligned when the clipboard holds an image. Caller holds
// r.mu.
func (r *Renderer) clipboardImageHintLine(termWidth int) string {
	if !r.clipboardImage {
		return ""
	}
	maxWidth := termWidth - sharedBlockFooterTruncateExtraRoom
	hint := runewidth.Truncate(clipboardImageHintText, maxWidth, "…")
	pad := maxWidth - runewidth.StringWidth(hint)
	if maxWidth <= 0 || pad < 0 {
		return ""
	}
	return strings.Repeat(" ", pad) + sharedBlockFooterStyle + hint + sharedBlockReset
}

func (r *Renderer) clipNotificationText() string {
	return r.clipNotification
}

func sortRows(a, b int) (int, int) {
	if a < b {
		return a, b
	}
	return b, a
}

// Tracker accumulates per-session counters during a chat session. Producer-side
// handlers call Observe* methods on event arrival; the transient Working line
// reads SnapshotSession directly on each repaint.
//
// Concurrency: sync.RWMutex. SnapshotSession takes RLock; writers take Lock.
type Tracker struct {
	mu           sync.RWMutex
	agents       int                 // Phase 3b: incremented by ObserveAgent (locked by 2C); de-duped via agentIDs.
	agentIDs     map[string]struct{} // Phase 3b: subagent identifiers seen; dedup so re-spawn of same id doesn't double-count.
	tools        int
	toolStepIDs  map[string]struct{}
	inputTokens  int
	outputTokens int
	runUsage     map[string]Counters
	planDone     int
	planTotal    int
	planActive   string
	goalRound    int
	goalChecking bool
	activeRunID  string
	runAgentIDs  map[string]map[string]struct{} // per-run agent dedup

	// Subagent token usage lives in dedicated accumulators, separate from the
	// primary runUsage map. The primary run's authoritative Summary.Usage
	// (applied by ObserveRunEndForRun) covers only the parent agent's own LLM
	// calls, and that call *reconciles* runUsage[runID] down to the final —
	// which would wipe any subagent tokens folded into the same entry. Keeping
	// them apart lets the working/worked line and footer show parent+subagent
	// combined while the parent reconciliation stays correct. Session-lifetime
	// (not freed by EndRun) so the post-run "Worked for" line still includes
	// them; per-run map drives the live "Working" line.
	subagentIn     int
	subagentOut    int
	runSubagentIn  map[string]int
	runSubagentOut map[string]int

	// agentRuns is the same tally kept per subagent. Everything above is
	// folded into run- and session-wide totals the moment it arrives, so a
	// subagent's own view had nothing of its own to report and showed the
	// conversation's figures instead. Session-lifetime, like the views these
	// feed: a subagent's view outlives both its roster row and the run it was
	// dispatched from, and its closing "Worked for" line is read from here.
	agentRuns map[string]*agentRun
}

// agentRun is one subagent's own account of itself: the window its current
// execution ran in and what it spent inside that window. executionID is the
// run that window belongs to — the clock belongs to one execution, not to
// the agent's identity, so a continued subagent starts a fresh window and
// its Worked for line counts only the execution that just ended.
type agentRun struct {
	counters    Counters
	executionID string
	startedAt   time.Time
	endedAt     time.Time
}

// AgentRunSnapshot is one subagent's own working figures: how long it has been
// running — frozen at its finish — and what it has spent.
type AgentRunSnapshot struct {
	Counters Counters
	Elapsed  time.Duration
	Ended    bool
}

// NewTracker constructs a Tracker.
func NewTracker() *Tracker {
	return &Tracker{
		agentIDs:       make(map[string]struct{}),
		toolStepIDs:    make(map[string]struct{}),
		runUsage:       make(map[string]Counters),
		runAgentIDs:    make(map[string]map[string]struct{}),
		runSubagentIn:  make(map[string]int),
		runSubagentOut: make(map[string]int),
		agentRuns:      make(map[string]*agentRun),
	}
}

// Reset clears the current session-scoped transient counters so a new / switched
// session starts with a clean working line and no inherited tool/token totals.
func (t *Tracker) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.agents = 0
	t.agentIDs = make(map[string]struct{})
	t.tools = 0
	t.toolStepIDs = make(map[string]struct{})
	t.inputTokens = 0
	t.outputTokens = 0
	t.runUsage = make(map[string]Counters)
	t.planDone = 0
	t.planTotal = 0
	t.planActive = ""
	t.goalRound = 0
	t.goalChecking = false
	t.activeRunID = ""
	t.runAgentIDs = make(map[string]map[string]struct{})
	t.subagentIn = 0
	t.subagentOut = 0
	t.runSubagentIn = make(map[string]int)
	t.runSubagentOut = make(map[string]int)
	t.agentRuns = make(map[string]*agentRun)
	t.mu.Unlock()
}

// StartRun marks the given runID as the currently active run for per-run
// counter tracking. Call with "" to clear the active run.
func (t *Tracker) StartRun(runID string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.activeRunID = strings.TrimSpace(runID)
	t.mu.Unlock()
}

// EndRun releases the per-run dedup scratch maps for runID. The accumulated
// counts already live in runUsage[runID] (read by SnapshotRun for the "Worked
// for" line), so only the dedup sets — which are useless once the run is over —
// are freed. This bounds memory across long multi-turn sessions.
func (t *Tracker) EndRun(runID string) {
	if t == nil {
		return
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	t.mu.Lock()
	delete(t.runAgentIDs, runID)
	delete(t.runSubagentIn, runID)
	delete(t.runSubagentOut, runID)
	// No check runs while no run does. The goal's round is not the run's: a
	// run parked on an approval resumes in the same round, and a goal is only
	// over when its completion says so (ObserveGoalRound(0)), which every way
	// a goal ends reports.
	t.goalChecking = false
	t.mu.Unlock()
}

// ObserveToolStep records one logical tool call. started/completed/approval
// notifications for the same tool carry the same StepID, so a non-empty StepID
// is counted once. agentID is the subagent that made the call, empty for the
// conversation's own: a subagent's calls count towards both the run the user is
// waiting on and that subagent's own tally.
func (t *Tracker) ObserveToolStep(agentID, stepID, toolName, filePath string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	stepID = strings.TrimSpace(stepID)
	newTool := false
	if stepID == "" {
		t.tools++
		newTool = true
	} else if _, seen := t.toolStepIDs[stepID]; !seen {
		t.toolStepIDs[stepID] = struct{}{}
		t.tools++
		newTool = true
	}
	if runID := t.activeRunID; runID != "" && newTool {
		rc := t.runUsage[runID]
		rc.Tools++
		t.runUsage[runID] = rc
	}
	if agentID = strings.TrimSpace(agentID); agentID != "" && newTool {
		t.agentRunLocked(agentID).counters.Tools++
	}
	t.mu.Unlock()
}

// ObserveAgent increments the session subagent counter the first time agentID
// is observed. Re-delivery of the same execution (a re-observed spawn event)
// is a no-op so the session "agents touched" total reflects unique subagent
// identities and a running clock is not reset by a duplicate. A different
// executionID — a continue, a user-sent message in its view — opens a new
// window: the clock restarts and any earlier end is forgotten, while the
// checklist counters carry over, because the agent's list spans its
// executions. Empty agentID is a no-op. Safe for concurrent use. Phase 3b
// (locked by 2C).
//
// at is when this execution started, which is what its view's working and
// worked lines count from.
func (t *Tracker) ObserveAgent(agentID, executionID string, at time.Time) {
	if t == nil {
		return
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return
	}
	executionID = strings.TrimSpace(executionID)
	if at.IsZero() {
		at = time.Now()
	}
	t.mu.Lock()
	run := t.agentRunLocked(agentID)
	if run.executionID != executionID {
		run.executionID = executionID
		run.startedAt = at
		run.endedAt = time.Time{}
	} else if run.startedAt.IsZero() {
		run.startedAt = at
	}
	if _, seen := t.agentIDs[agentID]; !seen {
		t.agentIDs[agentID] = struct{}{}
		t.agents++
	}
	if runID := t.activeRunID; runID != "" {
		if t.runAgentIDs[runID] == nil {
			t.runAgentIDs[runID] = make(map[string]struct{})
		}
		if _, seen := t.runAgentIDs[runID][agentID]; !seen {
			t.runAgentIDs[runID][agentID] = struct{}{}
			rc := t.runUsage[runID]
			rc.Agents++
			t.runUsage[runID] = rc
		}
	}
	t.mu.Unlock()
}

// ObserveAgentEnded freezes a subagent's clock at the moment its run reached a
// terminal state, so its view stops counting and its closing "Worked for" line
// reports the run rather than the time since it was read.
func (t *Tracker) ObserveAgentEnded(agentID string, at time.Time) {
	if t == nil {
		return
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	t.mu.Lock()
	t.agentRunLocked(agentID).endedAt = at
	t.mu.Unlock()
}

// ObserveAgentPlanProgress records a subagent's own checklist progress, which
// its view reports the way the composer reports the conversation's.
func (t *Tracker) ObserveAgentPlanProgress(agentID string, done, total int, active string) {
	if t == nil {
		return
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return
	}
	t.mu.Lock()
	run := t.agentRunLocked(agentID)
	run.counters.PlanDone = done
	run.counters.PlanTotal = total
	run.counters.PlanActive = strings.TrimSpace(active)
	t.mu.Unlock()
}

// agentRunLocked returns the tally for one subagent, creating it on first
// sight. Caller holds t.mu for writing.
func (t *Tracker) agentRunLocked(agentID string) *agentRun {
	if t.agentRuns == nil {
		t.agentRuns = make(map[string]*agentRun)
	}
	run := t.agentRuns[agentID]
	if run == nil {
		run = &agentRun{}
		t.agentRuns[agentID] = run
	}
	return run
}

// SnapshotAgentRun returns one subagent's own figures. ok is false for an agent
// whose start was never observed — there is nothing to report about it, and a
// caller must say nothing rather than borrow the conversation's numbers.
func (t *Tracker) SnapshotAgentRun(agentID string) (AgentRunSnapshot, bool) {
	if t == nil {
		return AgentRunSnapshot{}, false
	}
	agentID = strings.TrimSpace(agentID)
	t.mu.RLock()
	defer t.mu.RUnlock()
	run := t.agentRuns[agentID]
	if run == nil || run.startedAt.IsZero() {
		return AgentRunSnapshot{}, false
	}
	until, ended := run.endedAt, !run.endedAt.IsZero()
	if !ended {
		until = time.Now()
	}
	return AgentRunSnapshot{Counters: run.counters, Elapsed: until.Sub(run.startedAt), Ended: ended}, true
}

// ObserveRunEnd folds provider-reported token usage into the session.
// inputTokens and outputTokens are taken from the run's Usage summary.
// Negative values are clamped to 0. Safe for concurrent use.
func (t *Tracker) ObserveRunEnd(inputTokens, outputTokens int) {
	t.ObserveRunEndForRun("", inputTokens, outputTokens)
}

// ObserveRunEndForRun folds provider-reported token usage into the session,
// de-duping against any earlier runtime deltas already attributed to the same
// run. Empty runID keeps the legacy additive behavior.
func (t *Tracker) ObserveRunEndForRun(runID string, inputTokens, outputTokens int) {
	if t == nil {
		return
	}
	if inputTokens < 0 {
		inputTokens = 0
	}
	if outputTokens < 0 {
		outputTokens = 0
	}
	t.mu.Lock()
	runID = strings.TrimSpace(runID)
	if runID == "" {
		t.inputTokens += inputTokens
		t.outputTokens += outputTokens
		t.mu.Unlock()
		return
	}
	prev := t.runUsage[runID]
	// Fold the authoritative final into the session total. The session total
	// (composer footer, shown beside "N%") is the cross-turn
	// cumulative and only ever grows by the positive part of
	// (final - streamed-so-far). Clamping the negative keeps the footer
	// monotonic across turns: a streaming overshoot that exceeds the final is
	// left in place instead of subtracting prior turns' totals away and
	// collapsing the footer onto the current run's final.
	deltaIn := inputTokens - prev.InputTokens
	deltaOut := outputTokens - prev.OutputTokens
	if deltaIn < 0 {
		deltaIn = 0
	}
	if deltaOut < 0 {
		deltaOut = 0
	}
	t.inputTokens += deltaIn
	t.outputTokens += deltaOut
	// The per-run in/out counters are NOT modified here. They are driven
	// entirely by ObserveUsageDelta streaming accumulation - the same value
	// the "Working" line shows during the turn. When a streaming LLM call
	// fails mid-stream and recoverableLLM retries it, the failed attempt's
	// OnUsage delta has already irreversibly folded into the per-run counter,
	// but its usage never reached the UsageAccumulator that feeds
	// Summary.Usage (the "final" passed in here). Overwriting - or even taking
	// max - with that potentially-lower final would make "Worked for" diverge
	// from "Working". Keeping the streamed value verbatim makes the post-turn
	// "Worked for" line reuse the exact "Working" total the user already saw.
	t.runUsage[runID] = prev
	t.mu.Unlock()
}

// ObserveUsageDelta increments transient provider usage for an active run.
// Repeated deltas for the same run are accumulated and later de-duped against
// ObserveRunEndForRun when the final total arrives.
func (t *Tracker) ObserveUsageDelta(runID string, inputTokens, outputTokens int) {
	if t == nil {
		return
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		t.ObserveRunEnd(inputTokens, outputTokens)
		return
	}
	if inputTokens < 0 {
		inputTokens = 0
	}
	if outputTokens < 0 {
		outputTokens = 0
	}
	t.mu.Lock()
	t.inputTokens += inputTokens
	t.outputTokens += outputTokens
	prev := t.runUsage[runID]
	prev.InputTokens += inputTokens
	prev.OutputTokens += outputTokens
	t.runUsage[runID] = prev
	t.mu.Unlock()
}

// ObserveSubagentUsage folds a subagent's provider usage into dedicated
// accumulators that are added on top of the parent run's tokens in every
// snapshot, and into that subagent's own tally, which is what its view reports.
// runID is the parent run the subagent belongs to (its activeRunID at spawn
// time); an empty runID contributes to the session total only. Unlike
// ObserveUsageDelta these are never reconciled away by ObserveRunEndForRun,
// because the parent's Summary.Usage does not account for subagent LLM calls.
func (t *Tracker) ObserveSubagentUsage(runID, agentID string, inputTokens, outputTokens int) {
	if t == nil {
		return
	}
	if inputTokens < 0 {
		inputTokens = 0
	}
	if outputTokens < 0 {
		outputTokens = 0
	}
	if inputTokens == 0 && outputTokens == 0 {
		return
	}
	t.mu.Lock()
	t.subagentIn += inputTokens
	t.subagentOut += outputTokens
	if runID = strings.TrimSpace(runID); runID != "" {
		t.runSubagentIn[runID] += inputTokens
		t.runSubagentOut[runID] += outputTokens
	}
	if agentID = strings.TrimSpace(agentID); agentID != "" {
		run := t.agentRunLocked(agentID)
		run.counters.InputTokens += inputTokens
		run.counters.OutputTokens += outputTokens
	}
	t.mu.Unlock()
}

// SnapshotRun returns per-run counters for the given run, with plan fields
// from the session (plan progress is current-run scoped by nature).
func (t *Tracker) SnapshotRun(runID string) Counters {
	if t == nil {
		return Counters{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	runID = strings.TrimSpace(runID)
	c := t.runUsage[runID]
	c.InputTokens += t.runSubagentIn[runID]
	c.OutputTokens += t.runSubagentOut[runID]
	c.PlanDone = t.planDone
	c.PlanTotal = t.planTotal
	c.PlanActive = t.planActive
	return c
}

// SnapshotActiveRun returns per-run counters for the currently active run.
// Used by the "Working for" transient status line.
func (t *Tracker) SnapshotActiveRun() Counters {
	if t == nil {
		return Counters{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	c := t.runUsage[t.activeRunID]
	c.InputTokens += t.runSubagentIn[t.activeRunID]
	c.OutputTokens += t.runSubagentOut[t.activeRunID]
	c.PlanDone = t.planDone
	c.PlanTotal = t.planTotal
	c.PlanActive = t.planActive
	c.GoalRound = t.goalRound
	c.GoalChecking = t.goalChecking
	return c
}

// ObserveGoalRound records the round a running /goal is in; 0 when it ended.
func (t *Tracker) ObserveGoalRound(round int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.goalRound = max(round, 0)
	t.goalChecking = false
	t.mu.Unlock()
}

// ObserveGoalCheck records whether the goal's check is running.
func (t *Tracker) ObserveGoalCheck(running bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.goalChecking = running
	t.mu.Unlock()
}

// SnapshotSession returns a copy of the session-wide totals. Taken under
// RLock so the snapshot is internally consistent across multiple fields.
func (t *Tracker) SnapshotSession() Counters {
	if t == nil {
		return Counters{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return Counters{
		Tools:        t.tools,
		InputTokens:  t.inputTokens + t.subagentIn,
		OutputTokens: t.outputTokens + t.subagentOut,
		Agents:       t.agents,
		PlanDone:     t.planDone,
		PlanTotal:    t.planTotal,
		PlanActive:   t.planActive,
	}
}

func (t *Tracker) ObservePlanProgress(done int, total int, active string) {
	if t == nil {
		return
	}
	if done < 0 {
		done = 0
	}
	if total < 0 {
		total = 0
	}
	active = strings.TrimSpace(active)
	t.mu.Lock()
	t.planDone = done
	t.planTotal = total
	t.planActive = active
	t.mu.Unlock()
}

// The titles a goal's lines carry. The round's number is part of its title.
const (
	goalTitleStarted     = "Goal"
	goalTitleDone        = "Goal met"
	goalTitleStuck       = "Goal stopped: no progress"
	goalTitleCapped      = "Goal stopped: round limit reached"
	goalTitleInterrupted = "Goal stopped"
	goalTitleFailed      = "Goal stopped: error"
)

func goalStartedFrame(runID, objective string) Frame {
	return Frame{
		Kind: FrameGoal, StepID: "goal:" + strings.TrimSpace(runID) + ":start", RunID: runID, Final: true,
		Title:   goalTitleStarted,
		Summary: "works in rounds until a check of the workspace finds it met · esc stops",
		Content: strings.TrimSpace(objective),
	}
}

func goalRoundFrame(runID string, round int, why, checkAgentID string) Frame {
	return withGoalCheck(Frame{
		Kind: FrameGoal, StepID: fmt.Sprintf("goal:%s:round:%d", strings.TrimSpace(runID), round), RunID: runID, Final: true,
		Title:   fmt.Sprintf("Round %d", round),
		Summary: "not met yet",
		Content: strings.TrimSpace(why),
	}, checkAgentID)
}

// withGoalCheck lets a goal line open the view of the check that decided it:
// the line is the conversation's account of that check, the way a subagent's
// card is the account of the subagent.
func withGoalCheck(f Frame, checkAgentID string) Frame {
	if checkAgentID = strings.TrimSpace(checkAgentID); checkAgentID == "" {
		return f
	}
	f.AgentID = checkAgentID
	f.SubagentLifecycleCard = true
	f.Summary += " · click to see the check"
	return f
}

func goalCompletedFrame(runID string, p event.GoalCompletedPayload) Frame {
	f := Frame{
		Kind: FrameGoal, StepID: "goal:" + strings.TrimSpace(runID) + ":end", RunID: runID, Final: true,
		Content: strings.TrimSpace(p.Why),
	}
	rounds := "1 round"
	if p.Rounds != 1 {
		rounds = fmt.Sprintf("%d rounds", max(p.Rounds, 0))
	}
	f.Summary = rounds + " · " + formatWorkingElapsed(time.Duration(p.DurationMs)*time.Millisecond)
	switch p.Status {
	case event.GoalStatusDone:
		f.Title = goalTitleDone
	case event.GoalStatusStuck:
		f.Title = goalTitleStuck
	case event.GoalStatusCapped:
		f.Title = goalTitleCapped
	case event.GoalStatusInterrupted:
		f.Title = goalTitleInterrupted
		f.Summary = fmt.Sprintf("interrupted in round %d", max(p.Rounds, 1))
	default:
		f.Title = goalTitleFailed
		// A reason is the check's own error: the check runs after a round.
		f.Summary = fmt.Sprintf("after round %d", max(p.Rounds, 1))
		if f.Content == "" {
			f.Summary = fmt.Sprintf("round %d failed", max(p.Rounds, 1))
		}
	}
	return withGoalCheck(f, p.CheckAgentID)
}
