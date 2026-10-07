// The run controller: lifecycle, cancellation, and resume claiming.
package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// Phase is a run lifecycle state.
type Phase string

const (
	Running   Phase = "running"
	Waiting   Phase = "waiting_approval"
	Finishing Phase = "finishing"
	Finished  Phase = "finished"
)

var (
	ErrRunID     = errors.New("run: empty run ID")
	ErrSessionID = errors.New("run: empty session ID")
	ErrActive    = errors.New("run: active foreground run")
	ErrMissing   = errors.New("run: not found")
)

// Input is a queued user message.
type Input struct {
	Text        string
	Parts       []llm.ContentPart
	Attachments []string
	// MentionImages are the workspace-relative image paths the surface's @
	// picker attached. They stay paths while the message waits, so it can be
	// handed back to the composer or sent exactly as the user attached it;
	// the surface resolves them into image parts when it sends the message.
	MentionImages []string
	Rejected      bool
	// Seq orders this message against everything else queued in the same
	// conversation. The queue stamps it from its shared enqueue clock; the
	// lanes are separate FIFOs, so without it "newest queued" is only a
	// guess. Meaningless (zero) for a message that never sat in a queue.
	Seq int
	// Payload is the surface's own record of the message — what it shows and
	// what it hands back to the composer. The queue never reads it; it
	// travels with the message so a recalled, released or restored message
	// comes back whole.
	Payload any
	// SkillName and SkillPath carry a trusted explicit skill selection when
	// the message is a skill command the user typed in a subagent's view: the
	// execution that consumes it activates that skill. Empty for an ordinary
	// message. They are the subagent counterpart of the skill fields on a
	// dispatched turn.
	SkillName string
	SkillPath string
}

// Preview is the input state visible to adapters.
type Preview struct {
	Steers   []Input
	Rejected []Input
	FollowUp []Input
}

// RunState is the concurrency-safe state of one foreground run.
type RunState struct {
	ID        string
	SessionID string

	mu     sync.Mutex
	ctx    context.Context
	phase  Phase
	cancel context.CancelCauseFunc
	stop   func(error)
	locked bool
	input  *InputQueue
}

// Phase returns the current lifecycle phase.
func (s *RunState) Phase() Phase {
	if s == nil {
		return Finished
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase
}

// Context is cancelled when the run is cancelled.
func (s *RunState) Context() context.Context {
	if s == nil || s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// Controller coordinates foreground runs. A session holds at most one.
type Controller struct {
	mu       sync.Mutex
	runs     map[string]*RunState
	sessions map[string]chan struct{}
	inputs   map[string]*InputQueue
}

// NewController creates an empty controller.
func NewController() *Controller {
	return &Controller{runs: make(map[string]*RunState), sessions: make(map[string]chan struct{}), inputs: make(map[string]*InputQueue)}
}

// sessionQueue returns the conversation's input queue, creating it on first
// use. The queue outlives any one run: it is what the conversation's user
// queued while turns come and go, so a run only ever references it.
func (c *Controller) sessionQueue(sessionID string) *InputQueue {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionQueueLocked(sessionID)
}

func (c *Controller) sessionQueueLocked(sessionID string) *InputQueue {
	q := c.inputs[sessionID]
	if q == nil {
		q = NewInputQueue()
		c.inputs[sessionID] = q
	}
	return q
}

// Begin acquires the session foreground slot and creates a running state.
func (c *Controller) Begin(ctx context.Context, runID, sessionID string) (*RunState, error) {
	if c == nil {
		return nil, ErrMissing
	}
	if runID == "" {
		return nil, ErrRunID
	}
	if sessionID == "" {
		return nil, ErrSessionID
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if _, ok := c.runs[runID]; ok {
		c.mu.Unlock()
		return nil, ErrActive
	}
	lock := c.sessionLock(sessionID)
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-lock:
	}
	ctx, cancel := context.WithCancelCause(ctx)
	state := &RunState{ID: runID, SessionID: sessionID, ctx: ctx, phase: Running, cancel: cancel, stop: func(err error) { cancel(err) }, locked: true, input: c.sessionQueue(sessionID)}
	state.input.Attach(NewTurnInputRuntime())
	c.mu.Lock()
	if _, ok := c.runs[runID]; ok {
		c.mu.Unlock()
		lock <- struct{}{}
		return nil, ErrActive
	}
	c.runs[runID] = state
	c.mu.Unlock()
	return state, nil
}

// Track registers an adapter-owned execution for cancellation. It does not
// acquire a session slot because the adapter already owns that lifecycle.
func (c *Controller) Track(runID, sessionID string, cancel context.CancelFunc) bool {
	return c.track(runID, sessionID, cancel, nil)
}

// TrackRuntime registers an adapter-owned execution and its input runtime.
func (c *Controller) TrackRuntime(runID, sessionID string, cancel context.CancelFunc, rt *TurnInputRuntime) bool {
	return c.track(runID, sessionID, cancel, rt)
}

func (c *Controller) track(runID, sessionID string, cancel context.CancelFunc, rt *TurnInputRuntime) bool {
	if c == nil || runID == "" || cancel == nil {
		return false
	}
	c.mu.Lock()
	state := c.runs[runID]
	if state == nil {
		state = &RunState{ID: runID, SessionID: sessionID, ctx: context.Background(), phase: Running}
		c.runs[runID] = state
	}
	c.mu.Unlock()
	state.mu.Lock()
	if state.phase == Finished {
		state.mu.Unlock()
		return false
	}
	if id := strings.TrimSpace(sessionID); id != "" {
		state.SessionID = id
	}
	state.phase = Running
	state.stop = func(error) { cancel() }
	if state.input == nil {
		state.input = c.sessionQueue(state.SessionID)
	}
	if rt == nil {
		rt = NewTurnInputRuntime()
	}
	state.input.Attach(rt)
	state.mu.Unlock()
	return true
}

// Untrack removes an adapter-owned execution without releasing a session slot.
func (c *Controller) Untrack(runID string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	state, ok := c.runs[runID]
	if ok {
		delete(c.runs, runID)
	}
	c.mu.Unlock()
	if !ok {
		return false
	}
	state.mu.Lock()
	state.phase = Finished
	state.mu.Unlock()
	return true
}

func (c *Controller) sessionLock(sessionID string) chan struct{} {
	lock := c.sessions[sessionID]
	if lock != nil {
		return lock
	}
	lock = make(chan struct{}, 1)
	lock <- struct{}{}
	c.sessions[sessionID] = lock
	return lock
}

// Get returns the active run state.
func (c *Controller) Get(runID string) (*RunState, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state, ok := c.runs[runID]
	return state, ok
}

// Queue returns the input queue and session for an active run.
func (c *Controller) Queue(runID string) (*InputQueue, string, bool) {
	state, ok := c.Get(runID)
	if !ok {
		return nil, "", false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase == Finished || state.input == nil {
		return nil, "", false
	}
	return state.input, state.SessionID, true
}

// SessionQueue returns the conversation's input queue, creating it on first
// use. Surfaces operate the conversation's queue semantics through it.
func (c *Controller) SessionQueue(sessionID string) *InputQueue {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionQueueLocked(sessionID)
}

// Release closes a finishing run's input and decides what follows it: the
// run's turn runtime is detached, then the boundary b is applied to the
// conversation's queue. The run stops owning the queue in the same step, so
// nothing can arrive for the run after the decision and be dropped with it —
// a late steer or queued message is refused, and its sender keeps it.
func (c *Controller) Release(runID string, b Boundary) (send, restore []Input) {
	state, ok := c.Get(runID)
	if !ok {
		return nil, nil
	}
	state.mu.Lock()
	q := state.input
	state.input = nil
	state.mu.Unlock()
	if q == nil {
		return nil, nil
	}
	q.Detach()
	return q.Next(b)
}

// Active returns the number of foreground runs.
func (c *Controller) Active() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.runs)
}

// Current returns one active run. Adapters with more than one foreground run
// should address runs by ID instead.
func (c *Controller) Current() (runID, sessionID string, phase Phase, ok bool) {
	if c == nil {
		return "", "", Finished, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, state := range c.runs {
		state.mu.Lock()
		sid, p := state.SessionID, state.phase
		state.mu.Unlock()
		return id, sid, p, true
	}
	return "", "", Finished, false
}

// Find returns the active run for sessionID.
func (c *Controller) Find(sessionID string) (runID string, phase Phase, ok bool) {
	sessionID = strings.TrimSpace(sessionID)
	if c == nil || sessionID == "" {
		return "", Finished, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, state := range c.runs {
		state.mu.Lock()
		sid, p := state.SessionID, state.phase
		state.mu.Unlock()
		if sid == sessionID {
			return id, p, true
		}
	}
	return "", Finished, false
}

// WaitApproval moves a running state behind its approval gate.
func (c *Controller) WaitApproval(runID string) bool { return c.setPhase(runID, Running, Waiting) }

// Resume moves an approved state back to running.
func (c *Controller) Resume(runID string) bool { return c.setPhase(runID, Waiting, Running) }

// ClaimResume atomically claims an approval resume. A missing state is restored
// as running so a persisted run can resume after a process restart.
func (c *Controller) ClaimResume(runID, sessionID string) bool {
	if c == nil || strings.TrimSpace(runID) == "" {
		return false
	}
	sid := strings.TrimSpace(sessionID)
	c.mu.Lock()
	state := c.runs[runID]
	if state == nil {
		state = &RunState{ID: runID, SessionID: sid, ctx: context.Background(), phase: Running, input: c.sessionQueueLocked(sid)}
		state.input.Attach(NewTurnInputRuntime())
		c.runs[runID] = state
		c.mu.Unlock()
		return true
	}
	c.mu.Unlock()
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase != Waiting {
		return false
	}
	state.phase = Running
	return true
}

func (c *Controller) setPhase(runID string, from, to Phase) bool {
	state, ok := c.Get(runID)
	if !ok {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase != from {
		return false
	}
	state.phase = to
	return true
}

// Cancel cancels a run once. A repeated cancel is a no-op.
func (c *Controller) Cancel(runID string, cause error) bool {
	state, ok := c.Get(runID)
	if !ok {
		return false
	}
	state.mu.Lock()
	if state.phase == Finishing || state.phase == Finished {
		state.mu.Unlock()
		return false
	}
	state.phase = Finishing
	cancel := state.cancel
	stop := state.stop
	state.mu.Unlock()
	if stop != nil {
		stop(cause)
	} else if cancel != nil {
		cancel(cause)
	}
	return true
}

// Finish releases the foreground slot and removes the active state.
func (c *Controller) Finish(runID string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	state, ok := c.runs[runID]
	if !ok {
		c.mu.Unlock()
		return false
	}
	delete(c.runs, runID)
	c.mu.Unlock()
	state.mu.Lock()
	state.phase = Finished
	state.input = nil
	sessionID := state.SessionID
	locked := state.locked
	state.mu.Unlock()
	c.mu.Lock()
	lock := c.sessions[sessionID]
	c.mu.Unlock()
	if locked && lock != nil {
		lock <- struct{}{}
	}
	return true
}

// Steer queues an interrupting message while the model is running.
func (c *Controller) Steer(runID string, in Input) bool {
	state, ok := c.Get(runID)
	if !ok {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase != Running {
		if state.input != nil {
			rejected := cloneInput(in)
			rejected.Rejected = true
			state.input.FollowUp(rejected)
		}
		return false
	}
	return state.input != nil && state.input.Steer(in)
}

// FollowUp queues a message to execute after the current run.
func (c *Controller) FollowUp(runID string, in Input) bool {
	state, ok := c.Get(runID)
	if !ok {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase == Finishing || state.phase == Finished {
		return false
	}
	return state.input != nil && state.input.FollowUp(in)
}

type RunOverrides struct {
	UserContext    map[string]string
	SystemContext  map[string]string
	ToolUseContext map[string]string
	Messages       []llm.Message
}

type CreateSubagentContextParams struct {
	CacheSafe      *CacheSafeParams
	PromptMessages []llm.Message
	Overrides      *RunOverrides
}

type SubagentContext struct {
	SystemPrompt    string
	UserContext     map[string]string
	SystemContext   map[string]string
	ToolUseContext  map[string]string
	InitialMessages []llm.Message
}

func CreateSubagentContext(p CreateSubagentContextParams) (*SubagentContext, error) {
	if p.CacheSafe == nil {
		return nil, fmt.Errorf("nil cache safe params")
	}
	systemPrompt := strings.TrimSpace(p.CacheSafe.RenderedSystemPrompt)
	if systemPrompt == "" {
		systemPrompt = strings.TrimSpace(p.CacheSafe.SystemPrompt)
	}
	if systemPrompt == "" {
		return nil, fmt.Errorf("empty system prompt")
	}
	initialMessages := cloneMessages(p.CacheSafe.ParentMessages)
	if len(initialMessages) == 0 {
		initialMessages = cloneMessages(p.CacheSafe.ForkContextMessages)
	}
	if len(initialMessages) == 0 && p.CacheSafe.ParentAssistantToolMessage != nil {
		initialMessages = cloneMessages([]llm.Message{*p.CacheSafe.ParentAssistantToolMessage})
	}
	ctx := &SubagentContext{
		SystemPrompt:    systemPrompt,
		UserContext:     cloneStringMap(p.CacheSafe.UserContext),
		SystemContext:   cloneStringMap(p.CacheSafe.SystemContext),
		ToolUseContext:  cloneStringMap(p.CacheSafe.ToolUseContext),
		InitialMessages: initialMessages,
	}
	if len(p.PromptMessages) > 0 {
		ctx.InitialMessages = append(ctx.InitialMessages, cloneMessages(p.PromptMessages)...)
	}
	if p.Overrides != nil {
		if p.Overrides.UserContext != nil {
			ctx.UserContext = cloneStringMap(p.Overrides.UserContext)
		}
		if p.Overrides.SystemContext != nil {
			ctx.SystemContext = cloneStringMap(p.Overrides.SystemContext)
		}
		if p.Overrides.ToolUseContext != nil {
			ctx.ToolUseContext = cloneStringMap(p.Overrides.ToolUseContext)
		}
		if p.Overrides.Messages != nil {
			ctx.InitialMessages = cloneMessages(p.Overrides.Messages)
		}
	}
	ctx.InitialMessages = stripOrphanedToolCalls(ctx.InitialMessages)
	return ctx, nil
}

// stripOrphanedToolCalls removes ToolCalls from any assistant message that
// is not followed by matching tool-result messages. The fork runtime snapshot
// captures the parent session after the assistant message is appended but
// before tool results are written, leaving orphaned tool_calls that the API
// rejects. This sanitizes the tail so every assistant tool_calls entry has
// corresponding tool messages after it.
func stripOrphanedToolCalls(msgs []llm.Message) []llm.Message {
	if len(msgs) == 0 {
		return msgs
	}
	satisfied := make(map[string]bool, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- {
		switch msgs[i].Role {
		case llm.RoleTool:
			if msgs[i].ToolCallID != "" {
				satisfied[msgs[i].ToolCallID] = true
			}
		case llm.RoleAssistant:
			if len(msgs[i].ToolCalls) > 0 {
				allSatisfied := true
				for _, tc := range msgs[i].ToolCalls {
					if !satisfied[tc.ID] {
						allSatisfied = false
						break
					}
				}
				if !allSatisfied {
					msgs[i].ToolCalls = nil
				}
			}
		}
	}
	return msgs
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneMessages(in []llm.Message) []llm.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]llm.Message, len(in))
	copy(out, in)
	return out
}

func effectiveToolsForContext(ctx context.Context, tools []*llm.Tool) []*llm.Tool {
	if len(tools) == 0 {
		return nil
	}
	out := tools
	if subtype := strings.TrimSpace(tool.SubagentTypeFromContext(ctx)); subtype != "" {
		return filterToolsForSubagentSubtype(subtype, out)
	}
	return out
}

// PartialSessionCapture holds the orchestration LLM's accumulated session at
// the point a run is cancelled. The orchestration loop updates it after every
// session mutation; a cancelled run's dispatcher reads the snapshot and
// persists the already-completed messages (assistant tool_calls + tool
// results) so they are not lost.
//
// Only the captured messages that completed BEFORE the cancellation are
// present - a response that was mid-stream when the context was cancelled is
// never appended to the orchestration session, so it does not appear here as a
// half-finished assistant message. The streamed partial text is accumulated
// separately by the StreamSink (see prepareTUIAgentBase).
type PartialSessionCapture struct {
	mu      sync.Mutex
	session []llm.Message
}

// NewPartialSessionCapture creates an empty capture.
func NewPartialSessionCapture() *PartialSessionCapture {
	return &PartialSessionCapture{}
}

// Set stores a defensive copy of the given session as the current partial
// snapshot. Safe for concurrent use.
func (c *PartialSessionCapture) Set(session []llm.Message) {
	if c == nil {
		return
	}
	cp := make([]llm.Message, len(session))
	copy(cp, session)
	c.mu.Lock()
	c.session = cp
	c.mu.Unlock()
}

// Snapshot returns a defensive copy of the captured session.
func (c *PartialSessionCapture) Snapshot() []llm.Message {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.session) == 0 {
		return nil
	}
	out := make([]llm.Message, len(c.session))
	copy(out, c.session)
	return out
}

type partialSessionCaptureKey struct{}

// WithPartialSessionCapture injects a capture into the context so the
// orchestration LLM can update it. Returns ctx unchanged when capture is nil.
func WithPartialSessionCapture(ctx context.Context, capture *PartialSessionCapture) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if capture == nil {
		return ctx
	}
	return context.WithValue(ctx, partialSessionCaptureKey{}, capture)
}

// PartialSessionCaptureFromContext returns the capture injected into ctx, or
// nil when none is present.
func PartialSessionCaptureFromContext(ctx context.Context) *PartialSessionCapture {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(partialSessionCaptureKey{}).(*PartialSessionCapture)
	return v
}
