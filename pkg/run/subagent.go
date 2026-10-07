// Subagents: the tool surface, dispatch, model override, output, and host port.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/google/uuid"
)

// newSubagentTaskID returns a globally unique subagent task id. The roster and
// on-disk history dedup on task_id, so this must be collision-free even when
// many fanout tasks are dispatched in the same instant. A previous
// time.Now().UnixNano() scheme collided under concurrent dispatch (the clock
// can return the same nanosecond to simultaneous goroutines), collapsing
// distinct subagents onto one row; uuid removes that failure mode.
func newSubagentTaskID() string {
	return "subagent-" + uuid.NewString()
}

// getSubagentFlight returns a buffered channel that acts as a concurrency
// semaphore to limit the number of simultaneously running agent.
//
// Mechanism:
//  1. The channel has capacity = maxParallelSubagentsFromFactory(fac). It is used
//     purely as a counter; the concrete values sent through it are irrelevant.
//  2. Sending (flight <- struct{}{}) acquires a slot. If the current concurrency
//     is below the cap the send succeeds immediately; if at the cap it blocks.
//  3. The caller simultaneously selects on ctx.Done() so that a blocked send
//     can be aborted on context cancellation (timeout / user interrupt).
//  4. The caller defers a receive (<-flight) to release the slot when the
//     subagent finishes, letting the next waiter proceed.
//
// As a safety net, a capacity ≤ 0 (which is separately guarded by
// guardSubagentTool) is clamped to 1 to avoid a zero-buffer channel that would
// deadlock when there is no receiver.
func getSubagentFlight(fac Factory) chan struct{} {
	r := fac.Owner
	if r == nil {
		cap := maxParallelSubagentsFromFactory(fac)
		if cap <= 0 {
			cap = 1
		}
		return make(chan struct{}, cap)
	}
	return r.subagentFlight.get(func() int { return maxParallelSubagentsFromFactory(fac) })
}

// subagentSemaphore is the lazily-sized concurrency limit for one Runner's
// subagents. It bundles the channel with the sync.Once that sizes it, so the
// two cannot be separated — a Runner holding the channel without its Once
// would re-make it per call and stop limiting anything.
type subagentSemaphore struct {
	once sync.Once
	ch   chan struct{}
}

// get returns the semaphore, sizing it on first use from capacity. A capacity
// of zero or less is clamped to 1: a zero-buffer channel would deadlock on the
// first send, since nothing receives until a subagent finishes.
func (s *subagentSemaphore) get(capacity func() int) chan struct{} {
	s.once.Do(func() {
		n := capacity()
		if n <= 0 {
			n = 1
		}
		s.ch = make(chan struct{}, n)
	})
	return s.ch
}

type SubagentInput struct {
	Title        string `json:"title" jsonschema:"description=Short 3-6 word title naming this task. Shown in the agent roster and on the task card; the prompt itself is never shown there."`
	Task         string `json:"task" jsonschema:"description=Task for an isolated agent run with the same model configuration."`
	SubagentType string `json:"subagent_type,omitempty" jsonschema:"description=Optional subtype. Omit for fork-style run; set for fresh typed subagent."`
}

type SubagentSendInput struct {
	Title        string `json:"title" jsonschema:"description=Short 3-6 word title naming this task. Shown in the agent roster and on the task card; the prompt itself is never shown there."`
	Task         string `json:"task" jsonschema:"description=Task for an isolated agent run that should continue asynchronously."`
	SubagentType string `json:"subagent_type,omitempty" jsonschema:"description=Optional subtype. Omit for fork-style run; set for fresh typed subagent."`
}

type SubagentContinueInput struct {
	TaskID  string `json:"task_id,omitempty" jsonschema:"description=Async subagent task id returned by subagent_send or subagent_run."`
	RunID   string `json:"run_id,omitempty" jsonschema:"description=Child run id when available."`
	Message string `json:"message" jsonschema:"description=Self-contained follow-up instructions for the selected subagent."`
}

type SubagentLookupInput struct {
	TaskID    string `json:"task_id,omitempty" jsonschema:"description=Async subagent task id returned by subagent_send or subagent_run."`
	RunID     string `json:"run_id,omitempty" jsonschema:"description=Child run id when available."`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"description=Optional wait timeout in milliseconds for subagent_wait."`
}

type SubagentListInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"description=Maximum number of subagents to return. Default 20."`
}

type FanoutInput struct {
	Tasks       []SubagentTask `json:"tasks" jsonschema:"minItems=1" jsonschema_description:"List of tasks to run concurrently in isolated agents."`
	MaxParallel int            `json:"max_parallel" jsonschema:"minimum=1" jsonschema_description:"Required positive number of tasks to run in parallel. Must be greater than 0. Capped by agents.defaults.execution.max_parallel_subagents."`
	FailFast    bool           `json:"fail_fast,omitempty" jsonschema:"description=If true stop all tasks on first failure. Default false (partial failure allowed)."`
}

type SubagentTask struct {
	Title        string `json:"title" jsonschema:"description=Short 3-6 word title naming this task. Shown in the agent roster and on the task card; the prompt itself is never shown there."`
	Prompt       string `json:"prompt" jsonschema:"description=Task prompt for the subagent."`
	SubagentType string `json:"subagent_type,omitempty" jsonschema:"description=Optional subtype for this task."`
}

func subagentTypeEnumValues() []any {
	names := agent.PublicTypeNames()
	out := make([]any, 0, len(names))
	for _, name := range names {
		out = append(out, name)
	}
	return out
}

func patchSubagentTypeEnum(schema map[string]any, fieldPath []string) error {
	prop, err := schemaObjectAt(schema, fieldPath...)
	if err != nil {
		return err
	}
	prop["enum"] = subagentTypeEnumValues()
	prop["description"] = "Optional public typed subagent. general-purpose: bounded independent implementation or general delegated work. explore: read-only investigation and locating facts. plan: read-only implementation design, sequencing, risks, and verification planning. verification: falsify or verify an implementation with concrete checks. Omit subagent_type to fork the current agent. Fork inherits the parent conversation context, runs in the background, and keeps intermediate tool output out of the main context."
	removeRequiredAtPath(schema, ownerPathForFieldPath(fieldPath), "subagent_type")
	return nil
}

func schemaObjectAt(schema map[string]any, path ...string) (map[string]any, error) {
	cur := any(schema)
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("subagent schema path %v reached non-object", path)
		}
		if _, exists := m[key]; !exists {
			if err := expandLocalSchemaRef(schema, m); err != nil {
				return nil, err
			}
		}
		cur = m[key]
	}
	prop, ok := cur.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("subagent schema path %v is not an object", path)
	}
	return prop, nil
}

func expandLocalSchemaRef(root, target map[string]any) error {
	raw, ok := target["$ref"].(string)
	if !ok || !strings.HasPrefix(raw, "#/") {
		return nil
	}
	resolved, err := resolveLocalSchemaRef(root, raw)
	if err != nil {
		return err
	}
	for k, v := range resolved {
		if _, exists := target[k]; !exists {
			target[k] = v
		}
	}
	delete(target, "$ref")
	return nil
}

func resolveLocalSchemaRef(root map[string]any, ref string) (map[string]any, error) {
	cur := any(root)
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("subagent schema ref %q reached non-object", ref)
		}
		next, ok := m[part]
		if !ok {
			return nil, fmt.Errorf("subagent schema ref %q missing %q", ref, part)
		}
		cur = next
	}
	resolved, ok := cur.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("subagent schema ref %q is not an object", ref)
	}
	return resolved, nil
}

func ownerPathForFieldPath(fieldPath []string) []string {
	if len(fieldPath) >= 2 && fieldPath[len(fieldPath)-2] == "properties" {
		return append([]string(nil), fieldPath[:len(fieldPath)-2]...)
	}
	return nil
}

func removeRequiredAtPath(schema map[string]any, ownerPath []string, field string) {
	owner, err := schemaObjectAt(schema, ownerPath...)
	if err != nil {
		return
	}
	required, ok := owner["required"].([]any)
	if !ok || len(required) == 0 {
		return
	}
	next := required[:0]
	for _, item := range required {
		if fmt.Sprint(item) != field {
			next = append(next, item)
		}
	}
	owner["required"] = next
}

func subagentSchemaRaw(t reflect.Type, fieldPath []string) json.RawMessage {
	raw := tool.SchemaForInputType(t)
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return raw
	}
	if err := patchSubagentTypeEnum(schema, fieldPath); err != nil {
		return raw
	}
	out, err := json.Marshal(schema)
	if err != nil {
		return raw
	}
	return out
}

func applySubagentTypeEnumToTool(t *llm.Tool, fieldPath []string) error {
	if t == nil {
		return nil
	}
	raw, err := json.Marshal(t.InputSchema())
	if err != nil {
		return err
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return err
	}
	if err := patchSubagentTypeEnum(schema, fieldPath); err != nil {
		return err
	}
	if err := t.SetInputSchema(schema); err != nil {
		return err
	}
	removeRequiredAtPath(t.InputSchema(), ownerPathForFieldPath(fieldPath), "subagent_type")
	return nil
}

func lifecycleToolInputSchema(name string, t reflect.Type) json.RawMessage {
	if name == "subagent_send" {
		return subagentSchemaRaw(t, []string{"properties", "subagent_type"})
	}
	return tool.SchemaForInputType(t)
}

type FanoutResult struct {
	Index        int    `json:"index"`
	Task         string `json:"task"`
	SubagentType string `json:"subagent_type,omitempty"`
	Output       string `json:"output,omitempty"`
	Error        string `json:"error,omitempty"`
	OK           bool   `json:"ok"`
}

type subagentExecResult struct {
	AgentID     string
	RunID       string
	ParentRunID string
	SessionID   string
	Output      string
}

type preparedSubagent struct {
	ctx             context.Context
	cancel          context.CancelFunc
	detach          func()
	entry           agent.HistoryEntry
	superviseRunID  string
	parentRunID     string
	sessionID       string
	workerSessionID string
	task            string
	agentType       string
	subagentType    string
	agentKind       string
	runtimeKind     string
	oneShot         bool
	continuable     bool
	defSource       string
}

func subagentQuerySource(agentKind, agentType, defSource string) string {
	kind := strings.ToLower(strings.TrimSpace(agentKind))
	typ := strings.ToLower(strings.TrimSpace(agentType))
	src := strings.ToLower(strings.TrimSpace(defSource))
	switch {
	case kind == "fork":
		return "agent:builtin:fork"
	case kind == "typed" && typ != "" && src == "built-in":
		return "agent:builtin:" + typ
	case kind == "typed":
		return "agent:custom"
	default:
		return "agent:default"
	}
}

func RegisterSubagentTool(a *agent.Agent, fac Factory) error {
	if a == nil {
		return nil
	}
	// agents.defaults.enable_subagent is the switch for subagent capability,
	// and the tool surface is the first thing it governs: with subagents off
	// the subagent_* family is never registered, so the model is never offered
	// a tool whose only outcome would be a refusal, and the tool table it is
	// prompted with matches what the session can actually do.
	if !appcfg.SubagentsEnabled(fac.appConfig()) {
		return nil
	}
	t, err := llm.NewTool(
		"subagent_run",
		"Run a bounded isolated agent for a subtask and return its final text output.",
		func(ctx context.Context, in *SubagentInput) (string, error) {
			if err := guardSubagentTool(ctx, fac, "subagent_run"); err != nil {
				return "", err
			}
			if err := guardForkChildNoSubagentTools(ctx); err != nil {
				return "", err
			}
			if in != nil {
				if err := guardForkChildImplicitFork(ctx, in.SubagentType); err != nil {
					return "", err
				}
			} else {
				if err := guardForkChildImplicitFork(ctx, ""); err != nil {
					return "", err
				}
			}
			task := ""
			subType := ""
			title := ""
			if in != nil {
				task = strings.TrimSpace(in.Task)
				subType = strings.TrimSpace(in.SubagentType)
				title = strings.TrimSpace(in.Title)
			}
			if task == "" {
				b, _ := json.Marshal(map[string]any{
					"status": "skipped",
					"error":  "skipped: empty prompt",
				})
				return string(b), nil
			}
			taskID := newSubagentTaskID()
			execRes, err := execGeneralSubagent(ctx, fac, taskID, title, task, subType)
			if err != nil {
				return "", err
			}
			governed := governSubagentOutput(fac, "subagent_run", taskID, execRes.Output)
			agentKind := "fork"
			agentType := "fork"
			runtimeKind := "fork_subagent"
			defSource := "parent"
			oneShot := false
			continuable := true
			if subType != "" {
				def, derr := agent.ResolvePublicSubtype(subType)
				if derr != nil {
					return "", derr
				}
				agentType = def.Name
				agentKind = "typed"
				runtimeKind = "typed_subagent"
				defSource = def.Source
				oneShot = def.OneShot
				continuable = def.Continuable
			}
			resp := map[string]any{
				"agent_id":          execRes.AgentID,
				"task_id":           taskID,
				"run_id":            execRes.RunID,
				"parent_run_id":     execRes.ParentRunID,
				"session_id":        execRes.SessionID,
				"query_source":      subagentQuerySource(agentKind, agentType, defSource),
				"status":            agent.StatusOK,
				"output":            governed.Text,
				"finished_at":       time.Now().Unix(),
				"agent_type":        agentType,
				"agent_kind":        agentKind,
				"runtime_kind":      runtimeKind,
				"definition_source": defSource,
				"one_shot":          oneShot,
				"continuable":       continuable,
			}
			b, _ := json.Marshal(resp)
			return string(b), nil
		},
	)
	if err != nil {
		return err
	}
	if err := applySubagentTypeEnumToTool(t, []string{"properties", "subagent_type"}); err != nil {
		return err
	}
	if err := fac.Tools.Register(a, t); err != nil {
		return err
	}
	if fac.Tools != nil {
		fac.Tools.RegisterToolMeta(event.ToolMeta{
			Name:        "subagent_run",
			Description: "Use when delegating one self-contained subtask and you want to block until it finishes: runs a bounded isolated agent and returns its final text output.",
			Category:    "subagent",
			ReadOnly:    true,
			InputSchema: subagentSchemaRaw(reflect.TypeOf(SubagentInput{}), []string{"properties", "subagent_type"}),
		})
	}
	return registerFanoutTool(a, fac)
}

func subagentToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case "subagent_run", "subagent_fanout", "subagent_send", "subagent_status", "subagent_wait", "subagent_continue", "subagent_close", "subagent_list":
		return true
	default:
		return false
	}
}

func maxParallelSubagentsFromAppConfig(cfg *appcfg.Root) int {
	if cfg == nil {
		return appcfg.DefaultMaxParallelSubagents()
	}
	return cfg.Agents.Defaults.Execution.MaxParallelSubagentsValue()
}

func guardSubagentTool(ctx context.Context, fac Factory, name string) error {
	if maxParallelSubagentsFromFactory(fac) <= 0 && subagentToolName(name) {
		return fmt.Errorf("subagent capacity is 0; main agent must execute this step directly")
	}
	if fac.Tools != nil {
		return fac.Tools.GuardTool(ctx, name)
	}
	return tool.GuardToolNameOnly(ctx, name)
}

func guardForkChildImplicitFork(ctx context.Context, subagentType string) error {
	if tool.IsForkChildFromContext(ctx) && strings.TrimSpace(subagentType) == "" {
		return fmt.Errorf("fork is not available inside a forked worker")
	}
	return nil
}

func guardForkChildNoSubagentTools(ctx context.Context) error {
	if tool.IsForkChildFromContext(ctx) {
		return fmt.Errorf("subagent tools are not available inside a forked worker")
	}
	return nil
}

// subagentUserTurnFromContext returns the hook a channel installed for the
// execution running under ctx, or nil. It carries the worker-session row id of
// the user message an execution wrote, from the executor back to the channel.
func subagentUserTurnFromContext(ctx context.Context) func(rowID int64) {
	if ctx == nil {
		return nil
	}
	fn, _ := ctx.Value(subagentUserTurnKey{}).(func(int64))
	return fn
}

type subagentUserTurnKey struct{}

func withSubagentUserTurn(ctx context.Context, fn func(rowID int64)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, subagentUserTurnKey{}, fn)
}

// subagentResponseStartedFromContext returns the callback a channel installed
// for the execution running under ctx, or nil: it fires the moment that
// execution's model begins producing output, which is the point its message can
// no longer be withdrawn.
func subagentResponseStartedFromContext(ctx context.Context) func() {
	if ctx == nil {
		return nil
	}
	fn, _ := ctx.Value(subagentResponseStartedKey{}).(func())
	return fn
}

type subagentResponseStartedKey struct{}

func withSubagentResponseStarted(ctx context.Context, fn func()) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, subagentResponseStartedKey{}, fn)
}

func executeSubagent(ctx context.Context, fac Factory, record agent.HistoryEntry, task string, parts []llm.ContentPart, superviseExistingRunID, parentRunID, sessionID, workerSessionID, subagentType string) (string, error) {
	own := fac.Owner
	if own == nil || own.SubagentExecutor == nil {
		return "", fmt.Errorf("subagent requires an in-process executor")
	}
	telemetry.IncSubagentEnter()
	defer telemetry.IncSubagentExit()
	flight := getSubagentFlight(fac)
	select {
	case flight <- struct{}{}:
		defer func() { <-flight }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	// Every executor dispatch — typed, continued, plan review, and the fork path's
	// own fallback — passes through here, so this is where the child stops
	// speaking with the dispatching agent's voice. The record carries the
	// child's identity — the model it runs on above all, which its own context
	// gauge is sized by — and the supervised run id is the child's own run.
	ctx = subagentRunContext(ctx, own, own.Events, superviseExistingRunID, sessionID, record)
	// The user-row hook is installed by the channel that owns this execution:
	// a user-driven dispatch reports the row it writes so the channel can
	// withdraw it before the model answers.
	return own.SubagentExecutor.RunSubagentExec(ctx, SubagentExecRequest{
		Task:            task,
		Parts:           parts,
		SuperviseRunID:  superviseExistingRunID,
		ParentRunID:     parentRunID,
		SessionID:       sessionID,
		WorkerSessionID: workerSessionID,
		SubagentType:    subagentType,
		OnUserTurn:      subagentUserTurnFromContext(ctx),
	})
}

// errSubagentInputWithdrawn marks a user-driven execution the user took back
// before it answered. The execution that receives it publishes no ended event
// and writes no result, the way an Esc withdrawal on the primary conversation
// leaves no trace.
var errSubagentInputWithdrawn = errors.New("subagent input withdrawn")

// errSubagentInterruptedToSend marks an execution stopped only so the steers
// queued behind it could be sent now; the channel's boundary carries that
// decision and the surface sends what Next released.
var errSubagentInterruptedToSend = errors.New("subagent interrupted to send")

// SubagentSurface is how the surface the user is talking to a subagent from
// takes part in that conversation.
type SubagentSurface struct {
	// Frame is applied to the context of every execution the user starts: the
	// step hook and approval hooks the surface uses for its own turns.
	Frame func(ctx context.Context) context.Context
	// OnBoundary receives, when an execution of the subagent ends, what its
	// queue decided: send is the user's queued input to run next (the surface
	// merges it and calls SendToSubagent), restore is the input to give back
	// to the subagent's composer.
	OnBoundary func(agentKey string, send, restore []Input)
}

// subagentChannel is the user's side of one subagent's conversation: the queue
// of what the user sent it, the one execution at a time that consumes it, and
// the surface the user is talking to it from. It is engine state — a surface
// reaches it only through the Subagent* functions below — and it lives for the
// life of the process, keyed by the subagent's worker session, the same way a
// conversation's queue lives in the run controller.
type subagentChannel struct {
	workerSessionID string
	agentKey        string
	// runID and sessionID are what the subagent's own events are published
	// on, read from the record the channel was built from.
	runID     string
	sessionID string
	store     *state.SessionStore
	queue     *InputQueue

	// exec is a one-slot semaphore: one execution of this subagent runs at a
	// time. A second caller — the model's subagent_continue colliding with a
	// user's message, or two messages the user sent — waits here rather than
	// writing the same worker session concurrently.
	exec chan struct{}

	mu      sync.Mutex
	running *subagentExecution
	surface SubagentSurface
}

// subagentExecution is one run of a subagent while it is in flight.
type subagentExecution struct {
	executionID string
	// byUser is true when a message the user sent started this execution: only
	// such an execution may be withdrawn before it answers.
	byUser bool
	// responded is set once the execution's model has produced output; past
	// that point its message can no longer be withdrawn.
	responded bool
	// withdrawn is set when the user took the message back before it answered.
	withdrawn bool
	cancel    context.CancelCauseFunc
	// boundary is how the execution is being stopped, when it is; the zero
	// value means it ran to its end.
	boundary  Boundary
	input     Input
	userRowID int64
}

var (
	subagentChannelsMu sync.Mutex
	subagentChannels   = map[string]*subagentChannel{}
)

// subagentChannelFor returns the channel of one subagent, creating it on first
// use. It is keyed by the worker session, which is unique to the subagent; the
// queue is the run controller's for that session, so the channel and the
// controller agree on one queue.
func subagentChannelFor(fac Factory, record agent.HistoryEntry) *subagentChannel {
	workerSessionID := strings.TrimSpace(record.WorkerSessionID)
	subagentChannelsMu.Lock()
	defer subagentChannelsMu.Unlock()
	ch := subagentChannels[workerSessionID]
	if ch == nil {
		ch = &subagentChannel{
			workerSessionID: workerSessionID,
			agentKey:        subagentRosterKey(record),
			runID:           strings.TrimSpace(record.RunID),
			sessionID:       strings.TrimSpace(record.SessionID),
			exec:            make(chan struct{}, 1),
		}
		if fac.Owner != nil {
			ch.store = fac.Owner.SessionStore
		}
		if control := fac.ownerControl(); control != nil {
			ch.queue = control.SessionQueue(workerSessionID)
		}
		subagentChannels[workerSessionID] = ch
	}
	return ch
}

func (ch *subagentChannel) setSurface(surface SubagentSurface) {
	ch.mu.Lock()
	ch.surface = surface
	ch.mu.Unlock()
}

func (ch *subagentChannel) surfaceFor() SubagentSurface {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.surface
}

func (ch *subagentChannel) setRunning(ex *subagentExecution) {
	ch.mu.Lock()
	ch.running = ex
	ch.mu.Unlock()
}

// markResponded records that the execution's model has begun answering.
func (ch *subagentChannel) markResponded(ex *subagentExecution) {
	ch.mu.Lock()
	if ch.running == ex {
		ex.responded = true
	}
	ch.mu.Unlock()
}

// setUserRow records the worker-session row of the user message the execution
// wrote, so a withdrawal can name the exact row to hide.
func (ch *subagentChannel) setUserRow(ex *subagentExecution, rowID int64) {
	ch.mu.Lock()
	if ch.running == ex {
		ex.userRowID = rowID
	}
	ch.mu.Unlock()
}

// publishInputDelivered draws each steer the runtime handed the model as a
// user message in the subagent's own view.
func (ch *subagentChannel) publishInputDelivered(fac Factory, ex *subagentExecution, delivered []TurnInputEntry) {
	own := fac.Owner
	if own == nil || own.Events == nil {
		return
	}
	for _, entry := range delivered {
		if entry.Mode != TurnInputModeSteer {
			continue
		}
		text := strings.Join(strings.Fields(llm.TextContent(entry.Parts...)), " ")
		if text == "" {
			continue
		}
		publishEvent(context.Background(), own.Events, ch.runID, ch.sessionID, event.RunEventSubagentInputDelivered, event.SubagentInputDeliveredPayload{
			AgentID:     ch.agentKey,
			ExecutionID: ex.executionID,
			Text:        text,
		})
	}
}

// interruptToSend stops a running execution precisely to flush the steers
// queued behind it: the boundary says the surface sends those next. It does
// nothing unless an execution is running and a steer is waiting to be
// delivered, so a surface can tell Esc's two meanings apart.
func (ch *subagentChannel) interruptToSend() bool {
	ch.mu.Lock()
	ex := ch.running
	if ex == nil || len(ch.queue.Preview().Steers) == 0 {
		ch.mu.Unlock()
		return false
	}
	ex.boundary = BoundaryInterruptToSend
	cancel := ex.cancel
	ch.mu.Unlock()
	if cancel != nil {
		cancel(errSubagentInterruptedToSend)
	}
	return true
}

// withdraw takes a user's just-sent message back out of the subagent before
// its model answered: it stops the execution, hides the message's worker-
// session row, and returns that message plus everything queued after it, in
// the order the user wrote them. Past the answer boundary, or for an execution
// the user did not start, it does nothing.
func (ch *subagentChannel) withdraw() ([]Input, bool) {
	ch.mu.Lock()
	ex := ch.running
	if ex == nil || !ex.byUser || ex.responded || ex.withdrawn {
		ch.mu.Unlock()
		return nil, false
	}
	ex.withdrawn = true
	ex.boundary = BoundaryInterrupted
	rest := ch.queue.TakeAll()
	withdrawn := ex.input
	userRowID := ex.userRowID
	cancel := ex.cancel
	ch.mu.Unlock()
	if ch.store != nil && userRowID > 0 {
		_ = ch.store.WithdrawUserTurn(context.Background(), ch.workerSessionID, userRowID)
	}
	if cancel != nil {
		cancel(errSubagentInputWithdrawn)
	}
	out := make([]Input, 0, len(rest)+1)
	out = append(out, withdrawn)
	out = append(out, rest...)
	return out, true
}

// runSubagentExecution runs one execution of a subagent through its channel: it
// waits for the subagent to be free, attaches the channel's steer runtime for
// the execution's duration, and settles the channel's queue at the end. Every
// path that executes a subagent goes through it, so a user's steer always
// reaches the subagent — this installs the subagent's own runtime where the
// dispatching agent's would otherwise be inherited — and the model and the user
// never write the same worker session at once.
func runSubagentExecution(ctx context.Context, fac Factory, record agent.HistoryEntry, byUser bool, input Input,
	run func(execCtx context.Context) (string, error)) (string, error) {
	ch := subagentChannelFor(fac, record)
	select {
	case ch.exec <- struct{}{}:
		defer func() { <-ch.exec }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	execCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	executionID := strings.TrimSpace(record.ExecutionID)
	if executionID == "" {
		executionID = uuid.NewString()
	}
	ex := &subagentExecution{executionID: executionID, byUser: byUser, cancel: cancel, input: input}
	rt := NewTurnInputRuntime()
	ch.queue.Attach(rt)
	execCtx = WithTurnInputRuntime(execCtx, rt)
	execCtx = withSubagentResponseStarted(execCtx, func() { ch.markResponded(ex) })
	execCtx = withSubagentUserTurn(execCtx, func(rowID int64) { ch.setUserRow(ex, rowID) })
	rt.AddChangeHook(func(delivered []TurnInputEntry) { ch.publishInputDelivered(fac, ex, delivered) })
	ch.setRunning(ex)
	// The execution has taken the slot: any continuation the subagent was
	// waiting on is superseded, because the conversation has moved on.
	reportSubagentExecutionStarting(fac, record)
	out, err := run(execCtx)
	ch.queue.Detach()
	// Settle under the lock so a concurrent withdrawal sees either a running
	// execution or none, never both: whichever reaches the lock first wins.
	ch.mu.Lock()
	withdrawn := ex.withdrawn
	boundary := ex.boundary
	surface := ch.surface
	if ch.running == ex {
		ch.running = nil
	}
	ch.mu.Unlock()
	if withdrawn {
		// A withdrawn execution ended without a usage limit, so it closes the
		// subagent's run of continuations like any other non-limit ending.
		reportSubagentExecutionEnded(fac, record, errSubagentInputWithdrawn)
		return out, errSubagentInputWithdrawn
	}
	send, restore := ch.queue.Next(boundary)
	if surface.OnBoundary != nil {
		surface.OnBoundary(ch.agentKey, send, restore)
	}
	reportSubagentExecutionEnded(fac, record, err)
	return out, err
}

// reportSubagentExecutionStarting tells the composition root that one execution
// of the subagent in record has begun. It is a no-op when no executor is wired
// (a bare test factory), and runs on a detached context because the report
// outlives nothing the execution context owns.
func reportSubagentExecutionStarting(fac Factory, record agent.HistoryEntry) {
	exec := subagentExecutorOf(fac)
	if exec == nil {
		return
	}
	exec.SubagentExecutionStarting(context.Background(), strings.TrimSpace(record.WorkerSessionID))
}

// reportSubagentExecutionEnded tells the composition root how one execution of
// the subagent in record finished. err is the execution's own error, so a
// usage-limit failure is recognized by the scheduler the same way a primary
// turn's is.
func reportSubagentExecutionEnded(fac Factory, record agent.HistoryEntry, err error) {
	exec := subagentExecutorOf(fac)
	if exec == nil {
		return
	}
	exec.SubagentExecutionEnded(context.Background(), SubagentExecutionEnd{
		ConversationSessionID: strings.TrimSpace(record.SessionID),
		WorkerSessionID:       strings.TrimSpace(record.WorkerSessionID),
		AgentKey:              subagentRosterKey(record),
		RunID:                 strings.TrimSpace(record.RunID),
		Err:                   err,
	})
}

func subagentExecutorOf(fac Factory) SubagentExecutor {
	if fac.Owner == nil {
		return nil
	}
	return fac.Owner.SubagentExecutor
}

// subagentRunContext makes a subagent run its own: its own model-usage scope
// and its own foreground stream sink.
//
// Usage first. The child is a separate run with its own persisted row, so its
// calls must not roll up into the dispatching run's accumulator as well: the
// parent's persisted total would then include the child, and every sum over a
// run tree — /status's session usage, the cache hit rate — would count the
// child twice. Surfaces that show a turn's work including its subagents add
// the subagents' usage on top from the child's own tagged usage events.
//
// Then the stream sink: a subagent must never inherit the dispatching surface's sink. That sink
// belongs to the primary conversation and carries no AgentID, so every
// assistant and reasoning delta the child streams is attributed to the primary
// agent and retained in the main transcript instead of the subagent's own
// view. Its closing answer is the most visible casualty: nothing follows it
// inside the child to flush the surface's text buffer, so the child's whole
// spoken output surfaces in the parent conversation as one block the moment
// the parent's next tool call or turn end flushes that buffer — which is
// exactly where a user looking for the subagent's conclusion cannot see it.
//
// The replacement republishes the child's stream as roster-key-tagged run
// events, the only form the surfaces route per agent. The parent-owned
// callbacks are deliberately dropped rather than forwarded: Streamed (a child
// marking the parent's turn as streamed suppresses the parent's own final
// message) and OnResponseCompleted (the child does not own the text the parent
// has buffered). OnResponseStarted is replaced, not dropped: the child's
// channel installs its own acknowledgement boundary, which is how it learns an
// execution has begun answering and can no longer be withdrawn.
// OnUsageSnapshot is replaced, not dropped: the child's own usage
// snapshot is published tagged with its roster key, so the gauge in its view
// shows its context — the parent's footer never sees it.
func subagentRunContext(ctx context.Context, own *Runner, sink event.Sink, runID, sessionID string, record agent.HistoryEntry) context.Context {
	ctx = llm.WithoutUsageAccumulator(ctx)
	rosterKey := subagentRosterKey(record)
	if sink == nil || rosterKey == "" {
		return ctx
	}
	var streamed bool
	stream := EventStreamSink(ctx, sink, runID, sessionID, rosterKey, &streamed, subagentResponseStartedFromContext(ctx))
	stream.OnUsage = func(inputTokens, outputTokens int) {
		if inputTokens <= 0 && outputTokens <= 0 {
			return
		}
		publishEvent(ctx, sink, runID, sessionID, event.RunEventUsageDelta, event.UsageDeltaPayload{AgentID: rosterKey, InputTokens: inputTokens, OutputTokens: outputTokens})
	}
	// The child's context gauge, live: each response's absolute occupancy,
	// budgeted on the model the record says this subagent runs on. The event
	// carries the roster key and the child's run id, so it lands in the
	// subagent's view and nowhere else.
	stream.OnUsageSnapshot = func(inputTokens, outputTokens int) {
		if inputTokens <= 0 && outputTokens <= 0 {
			return
		}
		if payload, ok := ContextBudget(own, sessionID, &record, inputTokens+outputTokens); ok {
			publishEvent(ctx, sink, runID, sessionID, event.CompactEventBudgetUpdated, payload)
		}
	}
	return llm.WithStreamSink(ctx, stream)
}

// EventStreamSink streams what the model says as run events on the
// conversation — its text, its reasoning, and the searches a provider runs
// for it — as it says them. It is how a surface that shows the conversation
// through its event log sees an answer being written: a subagent's view, and
// the web's conversation. agentID tags a subagent's stream and is empty for
// the primary agent's. streamed is set once anything was streamed.
// onResponseStarted, when given, fires the moment this sink's model begins
// producing output: a subagent's channel uses it to learn the execution has
// answered and its message can no longer be withdrawn.
func EventStreamSink(ctx context.Context, sink event.Sink, runID, sessionID, agentID string, streamed *bool, onResponseStarted ...func()) *llm.StreamSink {
	agentID = strings.TrimSpace(agentID)
	stream := &llm.StreamSink{
		Streamed: streamed,
		OnDelta: func(text string) {
			if text != "" {
				publishEvent(ctx, sink, runID, sessionID, event.RunEventAssistantDelta, event.AssistantDeltaPayload{Text: text, AgentID: agentID})
			}
		},
		OnReasoningDelta: func(text string) {
			if text != "" {
				publishEvent(ctx, sink, runID, sessionID, event.RunEventReasoningDelta, event.ReasoningDeltaPayload{Text: text, AgentID: agentID})
			}
		},
		OnReasoningDone: func() {
			publishEvent(ctx, sink, runID, sessionID, event.RunEventReasoningDone, event.ReasoningDonePayload{AgentID: agentID})
		},
		// Provider-executed web search reaches a surface only through this
		// callback, so it is published as the tool step every surface already
		// renders.
		OnWebSearch: func(id, detail string, completed bool) {
			stepID := tool.ProviderWebSearchStepID(id)
			summary := tool.ProviderWebSearchSummary(detail, completed)
			meta := webSearchEventMeta(tool.ProviderWebSearchMeta(detail, completed, agentID))
			if !completed {
				publishEvent(ctx, sink, runID, sessionID, event.RunEventToolStarted, event.ToolCallStartedPayload{
					Kind:        event.RunEventToolStarted,
					StepID:      stepID,
					Description: summary,
					Summary:     summary,
					ToolName:    tool.ProviderWebSearchToolName,
					ToolMeta:    meta,
				})
				return
			}
			publishEvent(ctx, sink, runID, sessionID, event.RunEventToolCompleted, event.ToolCallCompletedPayload{
				Kind:        event.RunEventToolCompleted,
				StepID:      stepID,
				Description: summary,
				Summary:     summary,
				ToolName:    tool.ProviderWebSearchToolName,
				ToolMeta:    meta,
			})
		},
	}
	if len(onResponseStarted) > 0 && onResponseStarted[0] != nil {
		stream.OnResponseStarted = onResponseStarted[0]
	}
	return stream
}

// webSearchEventMeta carries a tool.ToolMeta across the event boundary, which
// has its own copy of the same shape.
func webSearchEventMeta(meta tool.ToolMeta) event.ToolCallMeta {
	return event.ToolCallMeta{
		ToolName:   meta.ToolName,
		Status:     meta.Status,
		Purpose:    meta.Purpose,
		Invocation: meta.Invocation,
		Input:      meta.Input,
		AgentID:    meta.AgentID,
		AgentType:  meta.AgentType,
		AgentKind:  meta.AgentKind,
	}
}

func runForkSubagent(ctx context.Context, fac Factory, prep preparedSubagent) (subagentExecResult, error) {
	own := fac.Owner
	if own == nil {
		return subagentExecResult{}, fmt.Errorf("fork subagent requires runner")
	}
	telemetry.IncSubagentEnter()
	defer telemetry.IncSubagentExit()
	flight := getSubagentFlight(fac)
	select {
	case flight <- struct{}{}:
		defer func() { <-flight }()
	case <-ctx.Done():
		return subagentExecResult{}, ctx.Err()
	}
	started := time.Now()
	llmClient := own.ForkLLM()
	if llmClient == nil {
		return subagentExecResult{}, fmt.Errorf("fork llm unavailable")
	}
	cacheSafe, err := own.CacheSafeParamsForRun(ctx)
	if err != nil {
		return subagentExecResult{}, err
	}

	// State the file access scope in the fork subagent's rendered system prompt
	// so it knows where its default scope is, and how to leave it, from the
	// start. The project root is the user's cwd (first allowed root from the
	// tools state), NOT the forebrain home directory — using Home here would name
	// ~/.forebrain as the scope, hiding the actual project from the subagent.
	if projectRoot := fac.projectRoot(); projectRoot != "" {
		if wsRoot := strings.TrimSpace(fac.ActiveWorkspaceRoot()); wsRoot != "" {
			constraint := agent.FileAccessScopeSystemPrompt(projectRoot, wsRoot)
			cacheSafe.RenderedSystemPrompt = strings.TrimSpace(cacheSafe.RenderedSystemPrompt) + "\n\n" + constraint
			cacheSafe.SystemPrompt = strings.TrimSpace(cacheSafe.SystemPrompt) + "\n\n" + constraint
		}
	}
	parentMsgs, ok := forkRuntimeSnapshotFromContext(ctx)
	if ok && len(parentMsgs) > 0 {
		cacheSafe.ParentMessages = deepCloneMessages(parentMsgs)
		if len(parentMsgs) > 0 {
			last := parentMsgs[len(parentMsgs)-1]
			if last.Role == llm.RoleAssistant && len(last.ToolCalls) > 0 {
				cacheSafe.ParentAssistantToolMessage = cloneMessagePtr(&last)
				forked, ferr := BuildForkedMessages(prep.task, last)
				if ferr == nil {
					// Build per-agent execution context: child runID for step
					// recording, agent identity for step-hook tagging, and an
					// StreamSink so assistant/reasoning deltas stream to the
					// TUI tagged by the subagent's roster key.
					agentCtx := ctx
					agentCtx = tool.WithConversationSessionID(agentCtx, prep.sessionID)
					// Give each fork agent a distinct intermediate_tool session key
					// so concurrent subagents write to separate files instead of
					// sharing the parent session file.
					if prep.workerSessionID != "" {
						agentCtx = llm.WithAgentSessionID(agentCtx, prep.workerSessionID)
					}
					if entryRunID := strings.TrimSpace(prep.entry.RunID); entryRunID != "" {
						agentCtx = tool.WithRunID(agentCtx, entryRunID)
					}
					// Thread project root and workspace root through context so
					// typedsubagent_llm.go can state the file access scope in the
					// system prompt, and so the permission layer (permissions.go,
					// runner.go) can detect subagent context. The project root is
					// the user's cwd (first allowed root), not the forebrain home
					// directory.
					agentCtx = tool.WithProjectRoot(agentCtx, fac.projectRoot())
					agentCtx = tool.WithWorkspaceRoot(agentCtx, fac.ActiveWorkspaceRoot())
					rosterKey := subagentRosterKey(prep.entry)
					if rosterKey != "" {
						agentCtx = tool.WithHookAgentID(agentCtx, rosterKey)
					}
					// Same detachment the executor path performs in
					// executeSubagent: the in-process fork agent speaks with
					// its own voice, not the dispatching agent's.
					agentCtx = subagentRunContext(agentCtx, own, own.Events, prep.entry.RunID, prep.sessionID, prep.entry)
					// The fork's system is frozen at birth and replays byte
					// for byte on every continuation (decision D6): the
					// frozen value is the system of the first request, so the
					// cached prefix survives. The store answers with the
					// already-frozen value when a sibling won the race; a
					// freeze that did not land (a store that cannot record
					// it) leaves the continuation to the legacy branch.
					_, _, _ = own.SessionStore.FreezeSessionPromptState(agentCtx, prep.workerSessionID, forkSystemPromptKey, cacheSafe.RenderedSystemPrompt)
					// The worker session's own partial capture: a cancelled
					// execution persists what it did, and installing it here
					// keeps the dispatching run's capture from collecting
					// the fork's rows.
					capture := NewPartialSessionCapture()
					agentCtx = WithPartialSessionCapture(agentCtx, capture)
					forkModel := forkAgentModel(own, prep)
					// The initial list lands in the worker session the moment
					// it exists, so a mid-run failure still leaves the full
					// prefix behind; the store's own tail-compare then skips
					// these rows when the finished execution persists.
					outcome, err := RunFork(agentCtx, RunParams{
						LLM:            llmClient,
						CacheSafe:      cacheSafe,
						PromptMessages: forked,
						CanUseTool:     func(string) bool { return true },
						Tools:          own.tools,
						AgentBaseName:  "subagent",
						AgentType:      "fork",
						AgentID:        prep.entry.AgentID,
						// Same root the SubagentStop hook reads back through
						// hook.SidechainTranscriptPath; both must resolve to
						// this agent's workspace or the hook gets a dead path.
						WorkspaceRoot: fac.workspaceRoot(),
						SessionID:     prep.sessionID,
						ParentRunID:   prep.parentRunID,
						ForkLabel:     "subagent",
						QuerySource:   prep.entry.QuerySource,
						OnInitialMessages: func(initial []llm.Message) {
							_ = own.SessionStore.AppendMessageSequenceForRun(context.Background(), prep.workerSessionID, prep.entry.RunID, initial, forkModel, "")
						},
						SidechainFrom: 0,
						RegisterTools: forkSubagentToolRegistrar(own),
					})
					// An executor-less runner is a legal assembly — the fork
					// fallback path is exactly what refuses one — so the
					// persistence rides on the same presence check the
					// fallback uses, not on a second convention.
					if own.SubagentExecutor != nil {
						own.SubagentExecutor.PersistSubagentTurn(agentCtx, SubagentTurn{
							WorkerSessionID: prep.workerSessionID,
							RunID:           prep.entry.RunID,
							Model:           forkModel,
							Result:          forkOutcomeResult(outcome),
							Partial:         capture.Snapshot(),
							Err:             err,
							Started:         started,
						})
					}
					if err != nil {
						return subagentExecResult{}, err
					}
					out := ""
					if outcome != nil && outcome.Result != nil {
						out = outcome.Result.TextContent()
					}
					return subagentExecResult{
						AgentID:     prep.entry.AgentID,
						RunID:       prep.entry.RunID,
						ParentRunID: prep.entry.ParentRunID,
						SessionID:   prep.entry.SessionID,
						Output:      out,
					}, nil
				}
			}
		}
	}
	// The fallback dispatches through the executor, which reads the roster key off
	// the context to route the child's frames; prep.ctx has not been tagged yet
	// on this branch.
	fallbackCtx := tool.WithConversationSessionID(ctx, prep.sessionID)
	fallbackCtx = tool.WithHookAgentID(tool.WithForkChild(fallbackCtx, true), subagentRosterKey(prep.entry))
	outText, err := executeSubagent(fallbackCtx, fac, prep.entry, prep.task, nil, prep.superviseRunID, prep.parentRunID, prep.sessionID, prep.workerSessionID, "")
	if err != nil {
		return subagentExecResult{}, err
	}
	return subagentExecResult{
		AgentID:     prep.entry.AgentID,
		RunID:       prep.entry.RunID,
		ParentRunID: prep.entry.ParentRunID,
		SessionID:   prep.entry.SessionID,
		Output:      outText,
	}, nil
}

// ensureSubagentSession opens the subagent's worker session in the state
// store, exactly once per dispatch: born a subagent conversation of the
// conversation that dispatched it, never renamed, never re-parented. The cwd
// and branch take the store defaults — a subagent works where its
// conversation works, and the conversation was born there.
func ensureSubagentSession(ctx context.Context, fac Factory, entry agent.HistoryEntry) error {
	if fac.Owner == nil || fac.Owner.SessionStore == nil {
		return nil
	}
	title := strings.TrimSpace(entry.Title)
	if title == "" {
		title = strings.TrimSpace(entry.AgentType)
	}
	return fac.Owner.SessionStore.EnsureAt(ctx, entry.WorkerSessionID, title, state.SessionBirth{
		Source:          state.SessionSourceSubagent,
		ParentSessionID: entry.SessionID,
	})
}

// forkSystemPromptKey is the frozen system of a fork subagent's worker
// session: the exact system of its first request, kept for the life of the
// fork so a continuation replays the same prefix byte for byte (decision
// D6). Rewriting it would invalidate the cached prefix every continuation
// shares, which is why later changes to how a fork's system is composed
// reach only forks born after them.
const forkSystemPromptKey = "fork_system_prompt"

// forkSubagentToolRegistrar is the tool set a fork subagent runs with: the
// dispatching runtime's own tools, cloned per registry.
//
// The clone is load-bearing: LoadedTools returns the parent agent's shared
// *llm.Tool pointers, and both ToolRegistry.Add and tool.Register append
// middlewares to the receiver. Without cloning, concurrent fork subagents
// (subagent_fanout) race on the shared tools' middleware slices, corrupting
// registration and leaving subagents without usable tools. The clone shares
// the handler closure (same tool.State / allowed roots as the parent) but
// owns a fresh middleware slice.
func forkSubagentToolRegistrar(own *Runner) func(*ToolRegistry) error {
	return func(reg *ToolRegistry) error {
		for _, t := range own.LoadedTools() {
			if t == nil {
				continue
			}
			if err := reg.Add(t.Clone()); err != nil {
				return err
			}
		}
		return nil
	}
}

// forkOutcomeResult is the finished fork's result, nil on a failed one.
func forkOutcomeResult(outcome *RunOutcome) *agent.Result {
	if outcome == nil {
		return nil
	}
	return outcome.Result
}

// forkAgentModel names the model a fork ran on. A fork has no provider chain
// of its own and takes no dispatch-time override, so it is the conversation's
// model — resolved here through the same function the primary session's rows
// use.
func forkAgentModel(own *Runner, prep preparedSubagent) string {
	if own == nil {
		return ""
	}
	_, model, _ := AgentModel(own, prep.sessionID, &agent.HistoryEntry{AgentKind: "fork"})
	return model
}

// prepareSubagentExecutionResolved is the shared body. It takes the dispatch
// whole, so a child's identity — its task id, the title it was named with, the
// prompt, the subtype — reaches the history entry in one piece instead of as a
// row of positional strings. d.resolve decides which definitions the caller may
// dispatch: the tool path passes the public resolver, while an internal
// dispatcher (plan review) passes the full one so it can run a built-in subtype
// that is deliberately absent from the enum the model sees.
func prepareSubagentExecutionResolved(baseCtx context.Context, fac Factory, d subagentDispatch) (preparedSubagent, error) {
	own := fac.Owner
	task := d.task
	rawSubtype := strings.TrimSpace(d.subtype)
	if own == nil {
		return preparedSubagent{}, fmt.Errorf("subagent requires runner")
	}
	if rawSubtype != "" && own.SubagentExecutor == nil {
		return preparedSubagent{}, fmt.Errorf("subagent requires an in-process executor")
	}
	parent := strings.TrimSpace(tool.RunIDFromContext(baseCtx))
	sid := strings.TrimSpace(llm.AgentSessionIDFromContext(baseCtx))
	if sid == "" {
		sid = "default"
	}
	runCtx := baseCtx
	childRunID := ""
	parentRunID := ""
	if own.RunRT != nil && parent != "" {
		// Clipped without a marker, as before — but on a rune boundary: this
		// preview becomes the subagent run's title in the roster.
		preview := llm.TruncateBytes(strings.TrimSpace(task), 512, "")
		cr, createErr := own.RunRT.CreateSubagentRun(runCtx, parent, sid, preview)
		if createErr != nil {
			return preparedSubagent{}, fmt.Errorf("persist subagent run: %w", createErr)
		}
		if cr == nil || strings.TrimSpace(cr.ID) == "" {
			return preparedSubagent{}, fmt.Errorf("persist subagent run: empty run record")
		}
		childRunID = cr.ID
	} else if parent != "" {
		parentRunID = parent
	}
	// The child's own input runtime is attached by runSubagentExecution when it
	// runs; nothing here inherits the dispatching agent's.
	cctx, cancel := context.WithCancel(runCtx)
	cctx = tool.WithConversationSessionID(cctx, sid)
	cacheKey := strings.TrimSpace(llm.PromptCacheKeyFromContext(baseCtx))
	if cacheKey == "" {
		cacheKey = sid
	}
	cctx = llm.WithPromptCacheKey(cctx, cacheKey)
	// Thread project root and workspace root through the subagent context so
	// the typed-subagent prompt middleware and the permission layer
	// (permissions.go, runner.go) can detect subagent boundaries. The
	// project root is the user's cwd (first allowed root from the tools
	// state), not the forebrain home directory — using Home here would name
	// ~/.forebrain as the subagent's scope, hiding the real project.
	if pr := fac.projectRoot(); pr != "" {
		cctx = tool.WithProjectRoot(cctx, pr)
	}
	if wsRoot := strings.TrimSpace(fac.ActiveWorkspaceRoot()); wsRoot != "" {
		cctx = tool.WithWorkspaceRoot(cctx, wsRoot)
	}
	now := time.Now().Unix()
	resolvedSubagentType := ""
	if t := strings.TrimSpace(rawSubtype); t != "" {
		resolvedSubagentType = t
	}
	agentID := uuid.NewString()
	agentType := "fork"
	agentKind := "fork"
	runtimeKind := "fork_subagent"
	oneShot := false
	continuable := true
	defSource := "parent"
	workerSessionID := fac.AgentName + ":" + sid + ":" + agentID + ":" + uuid.NewString()
	if t := resolvedSubagentType; t != "" {
		def, derr := d.resolve(t)
		if derr != nil {
			cancel()
			return preparedSubagent{}, derr
		}
		agentType = def.Name
		agentKind = "typed"
		runtimeKind = "typed_subagent"
		oneShot = def.OneShot
		continuable = def.Continuable
		defSource = def.Source

	}
	// Register the child's run against its worker session — the session whose
	// queue is this subagent's input channel — so the controller and the
	// channel resolve the same queue for it.
	detach := func() {}
	if childRunID != "" && own.Control != nil {
		own.Control.Track(childRunID, workerSessionID, cancel)
		detach = func() { own.Control.Finish(childRunID) }
	}
	// If the parent agent is in plan mode, the child inherits it: its modestore
	// entry is set to plan mode so sessionctx.AgentContextForProject (called by
	// the subagent bridge) reads plan mode, wires AllowedPlanPath, and
	// planModeLLM (wrapped on the runner's LLM chain) injects plan reminders.
	// Typed workers whose own policy already forbids write_file/edit_file
	// (explore/plan/verification) are exempt — they cannot edit anything
	// anyway. A write-capable type such as general-purpose is NOT exempt, or
	// spawning one would be a way to edit the codebase mid-planning.
	//
	// The entry must be written to the workspace root, the per-agent state root
	// every reader resolves (Runner.StateRoot / primaryagent.ActiveStateRoot).
	// Writing it under fac.Home instead lands in ~/.forebrain/state/modes while the
	// bridge reads ~/.forebrain/workspace/state/modes, and the child silently runs
	// in agent mode.
	if tool.ModeFromContext(baseCtx) == string(state.ModePlan) &&
		!tool.TypedSubagentBlocksFileWrites(rawSubtype) {
		_ = state.Set(fac.workspaceRoot(), workerSessionID, state.State{
			Mode:          state.ModePlan,
			Phase:         "plan",
			PrePlanMode:   state.ModeAgent,
			PlanTurnCount: 0,
		})
	}
	taskText := strings.TrimSpace(task)
	entry := agent.HistoryEntry{
		AgentID:          agentID,
		AgentKind:        agentKind,
		TaskID:           strings.TrimSpace(d.taskID),
		RunID:            childRunID,
		ParentRunID:      parent,
		SessionID:        sid,
		WorkerSessionID:  workerSessionID,
		ParentToolCallID: strings.TrimSpace(tool.ToolUseIDFromContext(baseCtx)),
		TaskIndex:        subagentTaskIndexFromContext(baseCtx),
		ExecutionID:      childRunID,
		QuerySource:      subagentQuerySource(agentKind, agentType, defSource),
		Title:            tool.SubagentTaskTitle(d.title, taskText),
		Task:             taskText,
		Status:           agent.StatusRunning,
		StartedAt:        now,
		UpdatedAt:        now,
		AgentType:        agentType,
		RuntimeKind:      runtimeKind,
		OneShot:          oneShot,
		Continuable:      continuable,
		DefSource:        defSource,
	}
	// The model a dispatch-time override pinned this run to is a property of
	// the execution: recorded with the entry, so its spawned event and every
	// surface reading that event name the model the run actually uses instead
	// of deriving one from the agent's type.
	if override, ok := SubagentModelOverrideFromContext(baseCtx); ok {
		entry.ModelProvider = override.Provider
		entry.Model = override.Model
	}
	// The worker session is born before anything can reference it. Its row is
	// what turns "a string in the ledger" into a session: every message row
	// this execution writes carries it as a foreign key, and a dispatch THIS
	// subagent itself makes names it as its session for CreateSubagentRun —
	// which is how a nested typed dispatch used to die on the foreign key
	// (plan 013's handoff): the outer worker session had no row. Birth is
	// also what carries the session-purpose identity plan 002 reads on: the
	// worker conversation is private to this subagent, its parent is the
	// conversation that dispatched it, and every conversation list filters it
	// out in SQL.
	if err := ensureSubagentSession(baseCtx, fac, entry); err != nil {
		cancel()
		return preparedSubagent{}, fmt.Errorf("persist subagent session: %w", err)
	}
	return preparedSubagent{
		ctx:             cctx,
		cancel:          cancel,
		detach:          detach,
		entry:           entry,
		superviseRunID:  childRunID,
		parentRunID:     parentRunID,
		sessionID:       sid,
		workerSessionID: workerSessionID,
		task:            taskText,
		agentType:       agentType,
		subagentType:    resolvedSubagentType,
		agentKind:       agentKind,
		runtimeKind:     runtimeKind,
		oneShot:         oneShot,
		continuable:     continuable,
		defSource:       defSource,
	}, nil
}

func finishSubagentExecution(fac Factory, handle *agent.Handle, entry agent.HistoryEntry) {
	entry = normalizeSubagentFinal(entry)
	if handle != nil {
		handle.Finish(entry)
	}
	if root := fac.subagentScopeRoot(); root != "" {
		_ = agent.AppendHistory(root, entry)
	}
}

func normalizeSubagentFinal(entry agent.HistoryEntry) agent.HistoryEntry {
	now := time.Now().Unix()
	if entry.UpdatedAt <= 0 {
		entry.UpdatedAt = now
	}
	if entry.StartedAt <= 0 {
		entry.StartedAt = entry.UpdatedAt
	}
	if entry.Status != agent.StatusRunning && entry.FinishedAt <= 0 {
		entry.FinishedAt = entry.UpdatedAt
	}
	if entry.Status == "" {
		entry.Status = agent.StatusFailed
	}
	return entry
}

// SubagentApprovalBudget bounds how many tool approvals one subagent run may
// ask the user for before the run is stopped. A worker that needs more than
// this has stopped making progress on its task and is only spending the
// operator's attention.
const SubagentApprovalBudget = 12

// runAcrossApprovals keeps one subagent run alive across its own tool
// approvals.
//
// An approval gate is a suspension point, not a failure: the child's run is
// parked, its pending action is on the queue, and the only thing missing is an
// answer. Everywhere a subagent is dispatched from inside a live tool call —
// subagent_run, subagent_fanout, the plan reviewer — that answer has to be
// obtained here, because the dispatcher cannot unwind. subagent_fanout is the
// clearest case: it is holding the in-flight results of every sibling task, and
// nothing above it can replay the dispatch, so unwinding one child's gate
// discards all of the fanout's work.
//
// approve puts the request to the user and returns the context the run resumes
// under. When the surface offers no such hook the error is returned unchanged,
// so the caller can fall back to the unwind-and-resume path the primary agent
// uses; a subagent is never refused a tool for lack of an approval path.
func runAcrossApprovals(
	ctx context.Context,
	approve tool.SubagentApprovalHook,
	initialSuperviseRunID string,
	label string,
	run func(runCtx context.Context, superviseRunID string) (string, error),
) (string, error) {
	runCtx := ctx
	superviseRunID := strings.TrimSpace(initialSuperviseRunID)
	for approvals := 0; ; approvals++ {
		out, err := run(runCtx, superviseRunID)
		if err == nil {
			return out, nil
		}
		var rae *tool.RequiresActionError
		if !errors.As(err, &rae) || rae == nil || approve == nil {
			return "", err
		}
		if approvals >= SubagentApprovalBudget {
			return "", fmt.Errorf("%s asked for more than %d tool approvals", label, SubagentApprovalBudget)
		}
		resumeCtx, aerr := approve(runCtx, rae)
		if aerr != nil {
			return "", aerr
		}
		if resumeCtx == nil {
			return "", fmt.Errorf("%s approval returned no resume context", label)
		}
		// The fence this resume must cross is crossed by the replay itself,
		// inside the child's orchestration loop. Crossing it here instead
		// covered the child's whole restart — prompt assembly, pre-hooks, a
		// possible compaction — with a phase that says the approved tool may
		// already have run.
		runCtx = resumeCtx
		// The gate was raised inside the child's own run, so that is the run the
		// replay must continue; run.Run tags the error with it before the id
		// reaches any caller.
		if rid := strings.TrimSpace(rae.RunID); rid != "" {
			superviseRunID = rid
		}
	}
}

func execGeneralSubagent(ctx context.Context, fac Factory, taskID, title, task, subagentType string) (subagentExecResult, error) {
	prep, output, err := dispatchSubagent(ctx, fac, subagentDispatch{
		taskID:     taskID,
		title:      title,
		task:       task,
		subtype:    subagentType,
		resolve:    agent.ResolvePublicSubtype,
		governTool: "subagent_run",
		run: func(runCtx context.Context, fac Factory, prep preparedSubagent) (string, error) {
			// A gate this child hits is answered in place, while its run and its
			// dispatcher stay alive. Without the hook the error travels on
			// unchanged and the caller decides how to suspend.
			approve := tool.SubagentApprovalHookFromContext(runCtx, factoryToolsState(fac))
			label := "subagent " + strings.TrimSpace(prep.entry.AgentType)
			// The exec context carries this execution's channel runtime; the
			// wrapper below installs it in place of the parent's.
			execCtx := typedSubagentExecContext(prep)
			if prep.agentKind == "fork" {
				// The fork path reads the parent's runtime snapshot from the
				// dispatching context, so it starts from runCtx rather than
				// prep.ctx.
				execCtx = tool.WithHookAgentID(runCtx, subagentRosterKey(prep.entry))
			}
			return runSubagentExecution(execCtx, fac, prep.entry, false, Input{}, func(execCtx context.Context) (string, error) {
				if prep.agentKind == "fork" {
					return runAcrossApprovals(execCtx, approve, prep.superviseRunID, label,
						func(attemptCtx context.Context, _ string) (string, error) {
							execRes, forkErr := runForkSubagent(attemptCtx, fac, prep)
							return execRes.Output, forkErr
						})
				}
				return runAcrossApprovals(execCtx, approve, prep.superviseRunID, label,
					func(attemptCtx context.Context, superviseRunID string) (string, error) {
						return executeSubagent(
							attemptCtx, fac, prep.entry, prep.task, nil, superviseRunID, prep.parentRunID,
							prep.sessionID, prep.workerSessionID, prep.subagentType,
						)
					})
			})
		},
	})
	if err != nil {
		return subagentExecResult{}, err
	}
	return subagentExecResult{
		AgentID:     prep.entry.AgentID,
		RunID:       prep.entry.RunID,
		ParentRunID: prep.entry.ParentRunID,
		SessionID:   prep.entry.SessionID,
		Output:      output,
	}, nil
}

func spawnAsyncSubagent(ctx context.Context, fac Factory, taskID, title, task, subagentType string) (agent.HistoryEntry, error) {
	d := subagentDispatch{
		taskID:     taskID,
		title:      title,
		task:       task,
		subtype:    subagentType,
		resolve:    agent.ResolvePublicSubtype,
		governTool: "subagent_run",
	}
	prep, handle, err := startSubagent(ctx, fac, d)
	if err != nil {
		return agent.HistoryEntry{}, err
	}
	go func() {
		defer prep.cancel()
		defer prep.detach()
		finished := false
		defer func() {
			if recovered := recover(); recovered != nil {
				telemetry.Log(fac.Home, "run.spawnAsyncSubagent", recovered)
				if !finished {
					finishSubagent(fac, d, prep, handle, "", fmt.Errorf("subagent panic: %v", recovered))
				}
			}
		}()
		// The caller has already returned, so the run builds from prep.ctx —
		// the context that outlives this call — never from the dispatching one.
		hostCtx := prep.ctx
		if prep.agentKind == "fork" {
			hostCtx = tool.WithForkChild(hostCtx, true)
			hostCtx = tool.WithHookAgentID(hostCtx, subagentRosterKey(prep.entry))
		} else {
			hostCtx = typedSubagentExecContext(prep)
		}
		approve := tool.SubagentApprovalHookFromContext(hostCtx, factoryToolsState(fac))
		label := "subagent " + strings.TrimSpace(prep.entry.AgentType)
		outText, runErr := runSubagentExecution(hostCtx, fac, prep.entry, false, Input{}, func(execCtx context.Context) (string, error) {
			return runAcrossApprovals(execCtx, approve, prep.superviseRunID, label,
				func(attemptCtx context.Context, superviseRunID string) (string, error) {
					if prep.agentKind == "fork" {
						result, forkErr := runForkSubagent(attemptCtx, fac, prep)
						return result.Output, forkErr
					}
					return executeSubagent(
						attemptCtx, fac, prep.entry, prep.task, nil, superviseRunID, prep.parentRunID,
						prep.sessionID, prep.workerSessionID, prep.subagentType,
					)
				})
		})
		finished = true
		finishSubagent(fac, d, prep, handle, outText, runErr)
	}()
	return prep.entry, nil
}

func syncGeneralSubagentRunState(fac Factory, ctx context.Context, entry agent.HistoryEntry) {
	if fac.Owner == nil || fac.Owner.RunRT == nil || strings.TrimSpace(entry.RunID) == "" {
		return
	}
	status := state.RunStatusFailed
	switch entry.Status {
	case agent.StatusOK:
		status = state.RunStatusDone
	case agent.StatusCancelled:
		status = state.RunStatusCancelled
	}
	// A terminal child cannot leave an approval behind. This matters when the
	// approval budget is exhausted (or a panic/error occurs after a gate): the
	// last supervised attempt returned RequiresAction and deliberately retained
	// its wait, but the outer child lifecycle has now decided not to continue.
	bg := context.Background()
	if wait, _ := fac.Owner.RunRT.GetWaitForRun(bg, entry.RunID); wait != nil {
		var resolvedAction *state.Action
		if fac.Owner.Actions != nil && strings.TrimSpace(wait.ActionID) != "" {
			resolvedAction, _ = fac.Owner.Actions.Cancel(bg, wait.ActionID, "subagent ended before the approval could continue")
			if resolvedAction == nil {
				resolvedAction, _ = fac.Owner.Actions.Get(bg, wait.ActionID)
			}
		}
		if resolvedAction != nil && resolvedAction.Status != state.ActionPending {
			publishEventWithID(bg, fac.Owner.Events,
				"approval-resolved:"+resolvedAction.ID+":"+string(resolvedAction.Status), entry.RunID, entry.SessionID,
				event.RunEventApprovalResolved, event.ApprovalResolvedPayload{
					ActionID: resolvedAction.ID, ActionKind: resolvedAction.Kind, Decision: string(resolvedAction.Status),
					Reason: resolvedAction.Error, AgentID: subagentRosterKey(entry), SubagentType: entry.AgentType,
				})
		}
		_ = fac.Owner.RunRT.ClearWait(bg, entry.RunID)
	}
	_ = fac.Owner.RunRT.SetStatus(bg, entry.RunID, status)
}

// notifySubagentSpawnedDirect publishes a subagent execution starting. origin
// is who started it — "user" for a message the user sent the subagent, empty
// for a dispatch the agent made.
func notifySubagentSpawnedDirect(fac Factory, entry agent.HistoryEntry, origin string) {
	if fac.Owner == nil {
		return
	}
	provider, model, effort := AgentModel(fac.Owner, entry.SessionID, &entry)
	publishEventWithID(context.Background(), fac.Owner.Events, subagentLifecycleEventID(entry, "spawned"), entry.RunID, entry.SessionID, event.RunEventSubagentSpawned, event.SubagentSpawnedPayload{
		AgentID: subagentRosterKey(entry), AgentType: strings.TrimSpace(entry.AgentType),
		TaskID: strings.TrimSpace(entry.TaskID), Title: strings.TrimSpace(entry.Title), Task: strings.TrimSpace(entry.Task),
		WorkerSessionID: entry.WorkerSessionID, ParentRunID: entry.ParentRunID,
		ParentToolCallID: entry.ParentToolCallID, TaskIndex: entry.TaskIndex, ExecutionID: entry.ExecutionID,
		ModelProvider: provider, Model: model, ReasoningEffort: effort,
		Origin: origin,
	})
}

func notifySubagentEndedDirect(fac Factory, entry agent.HistoryEntry) {
	if fac.Owner == nil {
		return
	}
	publishEventWithID(context.Background(), fac.Owner.Events, subagentLifecycleEventID(entry, "ended"), entry.RunID, entry.SessionID, event.RunEventSubagentEnded, event.SubagentEndedPayload{
		AgentID: subagentRosterKey(entry), AgentType: strings.TrimSpace(entry.AgentType),
		TaskID: strings.TrimSpace(entry.TaskID), Status: string(entry.Status), Error: strings.TrimSpace(entry.Error), Output: entry.Output,
		WorkerSessionID: entry.WorkerSessionID, ParentRunID: entry.ParentRunID,
		ParentToolCallID: entry.ParentToolCallID, TaskIndex: entry.TaskIndex, ExecutionID: entry.ExecutionID,
		FinishedAtMs: time.Now().UnixMilli(),
	})
}

func publishEvent(ctx context.Context, sink event.Sink, runID, sessionID, typ string, payload any) {
	publishEventWithID(ctx, sink, "", runID, sessionID, typ, payload)
}

func publishEventWithID(ctx context.Context, sink event.Sink, eventID, runID, sessionID, typ string, payload any) {
	if sink == nil {
		return
	}
	_ = sink.Publish(ctx, event.NewRunEvent(strings.TrimSpace(eventID), strings.TrimSpace(runID), strings.TrimSpace(sessionID), typ, payload, time.Now()))
}

func subagentLifecycleEventID(entry agent.HistoryEntry, phase string) string {
	executionID := strings.TrimSpace(entry.ExecutionID)
	if executionID == "" {
		executionID = strings.TrimSpace(entry.TaskID)
	}
	return "subagent-lifecycle:" + strings.TrimSpace(entry.RunID) + ":" + executionID + ":" + strings.TrimSpace(phase)
}

// subagentRosterKey is the identity every surface files this subagent under —
// its roster row, its own transcript, and the HookAgentID its nested tool calls
// are tagged with. The rule itself lives in pkg/agent because a persisted step
// replayed after a reload has to resolve to the very same key.
func subagentRosterKey(entry agent.HistoryEntry) string {
	return agent.RosterKey(entry.TaskID, entry.AgentType)
}

// subagentRecordContext rebuilds the context a subagent's own calls run
// under from its record: its kind, type, definition source, roster key and
// model override, its worker session, and the conversation it belongs to.
// Everything that shapes the prefix of the subagent's requests is here, so a
// compaction or a continuation built from it reuses that prefix.
func subagentRecordContext(ctx context.Context, fac Factory, record agent.HistoryEntry) context.Context {
	hostCtx := ctx
	if strings.TrimSpace(record.AgentKind) == "fork" {
		hostCtx = tool.WithForkChild(hostCtx, true)
	} else if strings.TrimSpace(record.AgentKind) == "typed" {
		hostCtx = tool.WithSubagentType(hostCtx, strings.TrimSpace(record.AgentType))
		hostCtx = tool.WithSubagentDefinitionSource(hostCtx, strings.TrimSpace(record.DefSource))
	}
	// A dispatch-time model choice is a property of the record now: the
	// continuation runs on the same model the user picked for it, not on the
	// conversation's default.
	if strings.TrimSpace(record.Model) != "" {
		hostCtx = WithSubagentModelOverride(hostCtx, SubagentModelOverride{Provider: record.ModelProvider, Model: record.Model})
	}
	hostCtx = tool.WithHookAgentID(hostCtx, subagentRosterKey(record))
	// The permission layer scopes to the conversation, the model context to
	// the worker session — the same split a first dispatch prepares.
	if sid := strings.TrimSpace(record.SessionID); sid != "" {
		hostCtx = tool.WithConversationSessionID(hostCtx, sid)
	}
	if ws := strings.TrimSpace(record.WorkerSessionID); ws != "" {
		hostCtx = llm.WithAgentSessionID(hostCtx, ws)
	}
	// The same roots a first dispatch threads through its context: the
	// typed-prompt middleware states the file access scope with them and the
	// permission layer detects the subagent boundary with them.
	if pr := fac.projectRoot(); pr != "" {
		hostCtx = tool.WithProjectRoot(hostCtx, pr)
	}
	if wsRoot := strings.TrimSpace(fac.ActiveWorkspaceRoot()); wsRoot != "" {
		hostCtx = tool.WithWorkspaceRoot(hostCtx, wsRoot)
	}
	// The cache key the dispatch carried, else the conversation id: the same
	// rule prepareSubagentExecutionResolved resolves it with.
	cacheKey := strings.TrimSpace(llm.PromptCacheKeyFromContext(ctx))
	if cacheKey == "" {
		cacheKey = strings.TrimSpace(record.SessionID)
	}
	hostCtx = llm.WithPromptCacheKey(hostCtx, cacheKey)
	// The query source the subagent's own calls run under, so a model
	// resolution from this context reads the same routing they read: the
	// dispatching turn's main-thread source would make the typed wrapper's
	// dispatch look like a main thread's.
	source := strings.TrimSpace(record.QuerySource)
	if source == "" {
		source = subagentQuerySource(record.AgentKind, record.AgentType, record.DefSource)
	}
	if source != "" {
		hostCtx = WithQuerySource(hostCtx, source)
	}
	return hostCtx
}

// ErrSubagentRunning refuses an operation that rewrites a subagent's history
// while one of its executions is still appending to it.
var ErrSubagentRunning = errors.New("subagent is running")

// ErrSubagentNotFound refuses an operation on an agent key this conversation
// does not name. Its tenancy is the lookup's: another conversation's subagent
// is not found here.
var ErrSubagentNotFound = errors.New("subagent not found")

// SubagentCompactTarget names what /compact compacts when it is run from a
// subagent's view: that subagent's worker session, under the context its own
// requests are made in. It refuses while the subagent is running, the way
// /compact is refused while the conversation's own run is.
func SubagentCompactTarget(ctx context.Context, r *Runner, conversationSessionID, agentKey string) (context.Context, string, error) {
	fac := r.subagentFactory()
	scopeRoot := fac.subagentScopeRoot()
	record, ok, err := agent.GetMerged(scopeRoot, agent.Query{SessionID: conversationSessionID, TaskID: agentKey})
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", ErrSubagentNotFound
	}
	// "Running" has one definition now: the channel is executing. It covers a
	// user-driven execution the registry handle would not show.
	if SubagentRunning(r, conversationSessionID, agentKey) {
		return nil, "", ErrSubagentRunning
	}
	return subagentRecordContext(ctx, fac, record), record.WorkerSessionID, nil
}

// subagentChannelForConversation resolves an agent key to its channel in this
// conversation, or ErrSubagentNotFound. The lookup filters by conversation, so
// another conversation's subagent — and another tenant's — is not found.
func subagentChannelForConversation(r *Runner, conversationSessionID, agentKey string) (*subagentChannel, agent.HistoryEntry, error) {
	if r == nil {
		return nil, agent.HistoryEntry{}, ErrSubagentNotFound
	}
	fac := r.subagentFactory()
	record, ok, err := agent.GetMerged(fac.subagentScopeRoot(), agent.Query{SessionID: conversationSessionID, TaskID: agentKey})
	if err != nil {
		return nil, agent.HistoryEntry{}, err
	}
	if !ok {
		return nil, agent.HistoryEntry{}, ErrSubagentNotFound
	}
	return subagentChannelFor(fac, record), record, nil
}

// SubagentDelivery says where a message the user sent a subagent went.
type SubagentDelivery string

const (
	// SubagentDeliveryStarted: the subagent was idle; the message started its
	// next execution.
	SubagentDeliveryStarted SubagentDelivery = "started"
	// SubagentDeliverySteered: it is running; the message reaches it at its
	// next tool boundary.
	SubagentDeliverySteered SubagentDelivery = "steered"
	// SubagentDeliveryQueued: it is running and the message waits for the
	// execution after this one.
	SubagentDeliveryQueued SubagentDelivery = "queued"
)

// SendToSubagent delivers a message the user sent a subagent through its
// channel. When the subagent is executing, the message steers (mode steer) or
// queues behind this execution (mode follow_up); when it is idle, the message
// starts a fresh user-driven execution in the background and the call returns
// Started at once. The surface is the one the user is talking from; the first
// call installs it, and a later call replaces it so a reconnected surface
// takes over.
func SendToSubagent(ctx context.Context, r *Runner, surface SubagentSurface, conversationSessionID, agentKey string, in Input, mode TurnInputMode) (SubagentDelivery, error) {
	ch, record, err := subagentChannelForConversation(r, conversationSessionID, agentKey)
	if err != nil {
		return "", err
	}
	ch.setSurface(surface)
	ch.mu.Lock()
	running := ch.running
	ch.mu.Unlock()
	if running != nil {
		if mode == TurnInputModeSteer && ch.queue.Steer(in) {
			return SubagentDeliverySteered, nil
		}
		ch.queue.FollowUp(in)
		return SubagentDeliveryQueued, nil
	}
	fac := r.subagentFactory()
	go startUserSubagentExecution(ctx, fac, ch, record, in)
	return SubagentDeliveryStarted, nil
}

// SubagentInputPreview is the subagent's queued input as its view shows it.
func SubagentInputPreview(r *Runner, conversationSessionID, agentKey string) QueuePreview {
	ch, _, err := subagentChannelForConversation(r, conversationSessionID, agentKey)
	if err != nil {
		return QueuePreview{}
	}
	return ch.queue.Preview()
}

// RecallSubagentInput pulls the newest queued message back out for editing,
// exactly as Recall does for the primary conversation's queue.
func RecallSubagentInput(r *Runner, conversationSessionID, agentKey string) (Input, bool) {
	ch, _, err := subagentChannelForConversation(r, conversationSessionID, agentKey)
	if err != nil {
		return Input{}, false
	}
	return ch.queue.Recall()
}

// InterruptSubagentToSend stops the running execution precisely to send the
// steers queued behind it — Esc's second meaning in a subagent's view. It
// returns false when no execution is running or no steer is waiting, so the
// surface can fall back to Esc's other meanings.
func InterruptSubagentToSend(r *Runner, conversationSessionID, agentKey string) bool {
	ch, _, err := subagentChannelForConversation(r, conversationSessionID, agentKey)
	if err != nil {
		return false
	}
	return ch.interruptToSend()
}

// WithdrawSubagentInput takes a just-sent user message back before the subagent
// answers — Esc's first meaning. It returns the withdrawn message plus
// everything queued after it, or false when the window has closed.
func WithdrawSubagentInput(r *Runner, conversationSessionID, agentKey string) ([]Input, bool) {
	ch, _, err := subagentChannelForConversation(r, conversationSessionID, agentKey)
	if err != nil {
		return nil, false
	}
	return ch.withdraw()
}

// DiscardSubagentInput empties the subagent's queue and returns how many
// messages were dropped.
func DiscardSubagentInput(r *Runner, conversationSessionID, agentKey string) int {
	ch, _, err := subagentChannelForConversation(r, conversationSessionID, agentKey)
	if err != nil {
		return 0
	}
	return ch.queue.Discard()
}

// SubagentRunning reports whether this subagent has an execution in flight. It
// is the one definition of "running" for a subagent: the registry handle that
// used to answer it does not cover a user-driven execution.
func SubagentRunning(r *Runner, conversationSessionID, agentKey string) bool {
	ch, _, err := subagentChannelForConversation(r, conversationSessionID, agentKey)
	if err != nil || ch == nil {
		return false
	}
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.running != nil
}

// cancelRunning stops the running execution, restoring everything it never
// took. It is what a registry-handle cancel (the surface's cancel-by-row)
// resolves to for a subagent.
func (ch *subagentChannel) cancelRunning(cause error) bool {
	ch.mu.Lock()
	ex := ch.running
	if ex == nil {
		ch.mu.Unlock()
		return false
	}
	ex.boundary = BoundaryInterrupted
	cancel := ex.cancel
	ch.mu.Unlock()
	if cancel != nil {
		cancel(cause)
	}
	return true
}

// startUserSubagentExecution runs one message the user sent a subagent as its
// next execution: on the surface's own context, through the channel, with the
// lifecycle of any other execution. It publishes the spawn so the roster and
// the card exist while it runs, and — unless the user withdrew the message —
// writes its result and publishes its end. It never injects anything into the
// primary conversation (decision D3): the dispatching agent reads the result
// back through subagent_status/wait/list.
func startUserSubagentExecution(ctx context.Context, fac Factory, ch *subagentChannel, record agent.HistoryEntry, in Input) {
	own := fac.Owner
	if own == nil || own.SubagentExecutor == nil {
		return
	}
	// The surface's own hooks frame this execution, then the record rebuilds
	// the identity its requests run under. A fresh background context: the
	// message outlives the request that delivered it.
	base := context.Background()
	if surface := ch.surfaceFor(); surface.Frame != nil {
		base = surface.Frame(base)
	}
	hostCtx := subagentRecordContext(base, fac, record)
	// A skill command the user typed in this subagent's view carries its
	// trusted explicit selection here; the execution activates that skill the
	// same way a dispatched turn does. It is threaded before the model call,
	// so the skill's activation is injected ahead of the first request.
	if name := strings.TrimSpace(in.SkillName); name != "" || strings.TrimSpace(in.SkillPath) != "" {
		hostCtx = WithExplicitSkillSelection(hostCtx, name, strings.TrimSpace(in.SkillPath))
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		text = strings.Join(strings.Fields(llm.TextContent(in.Parts...)), " ")
	}
	executionID := uuid.NewString()
	lifecycle := record
	lifecycle.ExecutionID = executionID
	lifecycle.Status = agent.StatusRunning
	lifecycle.Error = ""
	lifecycle.Task = text
	// A user-driven execution has no dispatching tool call and one task.
	lifecycle.ParentToolCallID = ""
	lifecycle.TaskIndex = 0
	now := time.Now().Unix()
	lifecycle.StartedAt = now
	lifecycle.UpdatedAt = now
	scopeRoot := fac.subagentScopeRoot()
	if scopeRoot != "" {
		_ = agent.AppendHistory(scopeRoot, lifecycle)
	}
	handle := agent.RegistryFor(scopeRoot).Start(lifecycle, func() { ch.cancelRunning(context.Canceled) })
	notifySubagentSpawnedDirect(fac, lifecycle, "user")
	if own.RunRT != nil && strings.TrimSpace(record.RunID) != "" {
		_ = own.RunRT.SetStatus(ctx, record.RunID, state.RunStatusRunning)
	}
	subagentType := ""
	if strings.TrimSpace(record.AgentKind) == "typed" {
		subagentType = strings.TrimSpace(record.AgentType)
	}
	approve := tool.SubagentApprovalHookFromContext(hostCtx, factoryToolsState(fac))
	var runFork func(attemptCtx context.Context, _ string) (string, error)
	if strings.TrimSpace(record.AgentKind) == "fork" {
		runFork = func(attemptCtx context.Context, _ string) (string, error) {
			return continueForkSubagent(attemptCtx, fac, record, text)
		}
	}
	outText, runErr := runSubagentExecution(hostCtx, fac, lifecycle, true, in, func(execCtx context.Context) (string, error) {
		return runAcrossApprovals(execCtx, approve, record.RunID, "subagent", func(attemptCtx context.Context, superviseRunID string) (string, error) {
			if runFork != nil {
				return runFork(attemptCtx, superviseRunID)
			}
			return executeSubagent(attemptCtx, fac, record, text, in.Parts, superviseRunID, record.ParentRunID, record.SessionID, record.WorkerSessionID, subagentType)
		})
	})
	if errors.Is(runErr, errSubagentInputWithdrawn) {
		// The user took the message back before it answered: write no result —
		// the way Esc does on the primary conversation — and return the record
		// to the state it had before the message, so the ledger keeps no
		// execution that is forever running.
		//
		// The execution still reports its end, as a cancellation. The spawn
		// announced a lifecycle card and a roster row before the message was
		// taken back; without an end event the surface would show that subagent
		// running forever. A cancelled end writes no output and injects nothing
		// into the conversation — it only retires what the spawn opened.
		restored := record
		restored.ExecutionID = executionID
		restored.UpdatedAt = time.Now().Unix()
		if restored.FinishedAt <= 0 {
			restored.FinishedAt = restored.UpdatedAt
		}
		restored = normalizeSubagentFinal(restored)
		if handle != nil {
			handle.Finish(restored)
		}
		if scopeRoot != "" {
			_ = agent.AppendHistory(scopeRoot, restored)
		}
		syncGeneralSubagentRunState(fac, hostCtx, restored)
		// The event carries the spawn's identity, not the original record's: a
		// user-driven execution has no dispatching tool call, so the surface's
		// card binding retires the card the spawn opened and not the one the
		// original subagent_run/send call owns.
		ended := lifecycle
		ended.Status = agent.StatusCancelled
		ended.UpdatedAt = restored.UpdatedAt
		ended.FinishedAt = restored.FinishedAt
		notifySubagentEndedDirect(fac, ended)
		return
	}
	now = time.Now().Unix()
	// The ended event must carry the identity the spawn announced, not the
	// original record's: a user-driven execution has no dispatching tool call,
	// so its ParentToolCallID is empty and its TaskIndex is 0. Reusing the
	// original record here would name the subagent_send/run call that first
	// dispatched this subagent, and the surface's card binding would settle that
	// (already finished) call instead — leaving this execution's own card stuck
	// running in the conversation.
	final := lifecycle
	final.UpdatedAt = now
	final.FinishedAt = now
	switch {
	case runErr == nil:
		final.Status = agent.StatusOK
		final.Output = mergeContinuationOutput(record.Output, outText)
		final.Error = ""
	default:
		final.Status = agent.StatusFailed
		final.Error = llm.ExplainError(runErr)
	}
	finishSubagentExecution(fac, handle, final)
	syncGeneralSubagentRunState(fac, hostCtx, final)
	notifySubagentEndedDirect(fac, final)
	publishSubagentContextBudget(fac, final)
}

// mergedSubagentRecord resolves an agent key to the record it names in this
// conversation — the ledger's merged view, the same lookup a /compact from
// that subagent's view resolves its target with. A nil runner has no ledger:
// the agent key names nothing there.
func mergedSubagentRecord(r *Runner, conversationSessionID, agentKey string) (agent.HistoryEntry, bool, error) {
	if r == nil {
		return agent.HistoryEntry{}, false, fmt.Errorf("subagent %s not found", agentKey)
	}
	fac := r.subagentFactory()
	record, ok, err := agent.GetMerged(fac.subagentScopeRoot(), agent.Query{SessionID: conversationSessionID, TaskID: agentKey})
	if err != nil {
		return agent.HistoryEntry{}, false, err
	}
	if !ok {
		return agent.HistoryEntry{}, false, fmt.Errorf("subagent %s not found", agentKey)
	}
	return record, true, nil
}

// SubagentContextBudget is the gauge a subagent's view opens with: the
// subagent's own context, measured from its worker session, budgeted on the
// model that subagent runs on. A worker session that has recorded no usage
// yet reads as a fresh context — the whole window, the same read the primary
// footer gives a new conversation.
func SubagentContextBudget(ctx context.Context, r *Runner, conversationSessionID, agentKey string) (event.TokenBudgetUpdatedPayload, bool) {
	record, ok, err := mergedSubagentRecord(r, conversationSessionID, agentKey)
	if err != nil || !ok {
		return event.TokenBudgetUpdatedPayload{}, false
	}
	var store *state.SessionStore
	if r != nil && r.Deps != nil {
		store = r.Deps.SessionStore
	}
	usage, _ := ContextOccupancy(ctx, store, record.WorkerSessionID)
	return ContextBudget(r, conversationSessionID, &record, usage)
}

// SubagentContextGauge is what /context reports about a subagent's context
// when it is run from that subagent's view: the worker session the report
// reads, the model the gauge is sized by, the measured occupancy, and the
// configured explicit limit — the inputs the shared report builder and the
// primary session's HandleContextSlash are fed with.
func SubagentContextGauge(ctx context.Context, r *Runner, conversationSessionID, agentKey string) (workerSessionID, provider, model string, used, explicitLimit int, err error) {
	record, ok, err := mergedSubagentRecord(r, conversationSessionID, agentKey)
	if err != nil {
		return "", "", "", 0, 0, err
	}
	if !ok {
		return "", "", "", 0, 0, fmt.Errorf("subagent %s not found", agentKey)
	}
	provider, model, _ = AgentModel(r, conversationSessionID, &record)
	var store *state.SessionStore
	if r != nil && r.Deps != nil {
		store = r.Deps.SessionStore
		if r.AppCfg != nil {
			explicitLimit = r.AppCfg.Compact.ModelAutoCompactTokenLimit
		}
	}
	used, _ = ContextOccupancy(ctx, store, record.WorkerSessionID)
	return record.WorkerSessionID, provider, model, used, explicitLimit, nil
}

// publishSubagentContextBudget pushes the gauge of a subagent's own context
// once an execution of it has finished appending to its worker session — the
// subagent's counterpart of the turn-end notifyTokenBudget the primary
// session's footer gets. The event carries the roster key and the child's run
// id, so it lands in the subagent's view and nowhere else.
func publishSubagentContextBudget(fac Factory, record agent.HistoryEntry) {
	own := fac.Owner
	if own == nil || own.SessionStore == nil {
		return
	}
	// The turn-end refresh reports a measured context; a worker session with
	// nothing recorded yet has nothing to say that its view's opening gauge
	// has not already said.
	usage, ok := ContextOccupancy(context.Background(), own.SessionStore, record.WorkerSessionID)
	if !ok || usage <= 0 {
		return
	}
	payload, ok := ContextBudget(own, record.SessionID, &record, usage)
	if !ok {
		return
	}
	publishEvent(context.Background(), own.Events, record.RunID, record.SessionID, event.CompactEventBudgetUpdated, payload)
}

func continueSubagentExecution(ctx context.Context, fac Factory, in *SubagentContinueInput) (agent.HistoryEntry, error) {
	msg := ""
	if in != nil {
		msg = strings.TrimSpace(in.Message)
	}
	if msg == "" {
		return agent.HistoryEntry{}, fmt.Errorf("message required")
	}
	lookup := &SubagentLookupInput{}
	if in != nil {
		lookup.TaskID = strings.TrimSpace(in.TaskID)
		lookup.RunID = strings.TrimSpace(in.RunID)
	}
	record, ok, err := lookupSubagentRecord(fac.subagentScopeRoot(), ctx, lookup)
	if err != nil {
		return agent.HistoryEntry{}, err
	}
	if !ok {
		return agent.HistoryEntry{}, fmt.Errorf("subagent not found")
	}
	if record.OneShot && !record.Continuable {
		return agent.HistoryEntry{}, fmt.Errorf("subagent_type %q is one-shot and cannot continue", strings.TrimSpace(record.AgentType))
	}
	// The subagent's conversation lives in its worker session now. When the
	// session holds history — every subagent this runtime births — the
	// follow-up goes to that conversation as the message verbatim, and the
	// request the continuation sends is the previous execution's request
	// plus one user message. The stitched prompt below remains only for a
	// record born before persistence, or whose worker session an external
	// cause emptied: that prompt becomes the session's first user message,
	// and the subagent is on the new footing from then on. It is the
	// treatment of existing data, not a fallback on the live path.
	input := msg
	legacyConversation := false
	own := fac.Owner
	if own == nil || own.SessionStore == nil {
		input = buildSubagentContinuePrompt(record, msg)
		legacyConversation = true
	} else {
		entries, lerr := own.SessionStore.ListTranscriptMessages(ctx, record.WorkerSessionID, 1)
		if lerr != nil {
			return agent.HistoryEntry{}, lerr
		}
		if len(entries) == 0 {
			input = buildSubagentContinuePrompt(record, msg)
			legacyConversation = true
			// A record born before persistence has no worker session row,
			// and the stitched prompt needs the row to land in. EnsureAt is
			// an upsert, so an already-born session is only touched.
			if err := ensureSubagentSession(ctx, fac, record); err != nil {
				return agent.HistoryEntry{}, fmt.Errorf("persist subagent session: %w", err)
			}
		}
	}
	// Everything that shapes the subagent's own requests — its kind flags,
	// its model, its roster key, its two session ids, the roots, the cache
	// key and the query source its model resolution routes on — comes from
	// the record, once.
	// The subagent's own input runtime is attached by runSubagentExecution; the
	// dispatching agent's is not inherited.
	hostCtx := subagentRecordContext(ctx, fac, record)
	executionID := uuid.NewString()
	lifecycle := record
	lifecycle.ExecutionID = executionID
	// ParentToolCallID and TaskIndex together say "which call's which task";
	// the continue call is a dispatch of its own with a single task, so both
	// speak of it, never of the original dispatch the record came from.
	lifecycle.ParentToolCallID = strings.TrimSpace(tool.ToolUseIDFromContext(ctx))
	lifecycle.TaskIndex = 0
	lifecycle.Task = msg
	lifecycle.Status = agent.StatusRunning
	lifecycle.Error = ""
	notifySubagentSpawnedDirect(fac, lifecycle, "")
	if fac.Owner != nil && fac.Owner.RunRT != nil && strings.TrimSpace(record.RunID) != "" {
		_ = fac.Owner.RunRT.SetStatus(ctx, record.RunID, state.RunStatusRunning)
	}
	subagentType := ""
	if strings.TrimSpace(record.AgentKind) == "typed" {
		subagentType = strings.TrimSpace(record.AgentType)
	}
	approve := tool.SubagentApprovalHookFromContext(hostCtx, factoryToolsState(fac))
	var runFork func(context.Context, string) (string, error)
	if strings.TrimSpace(record.AgentKind) == "fork" && !legacyConversation {
		// A fork with its own conversation continues on it: the system it
		// was born with and the messages it holds, replayed as the inherited
		// prefix so the continuation's cached bytes are the previous
		// request's (decision D6). It runs in-process here, bypassing the
		// executor the way its first run did.
		runFork = func(attemptCtx context.Context, _ string) (string, error) {
			return continueForkSubagent(attemptCtx, fac, record, input)
		}
	}
	outText, runErr := runSubagentExecution(hostCtx, fac, lifecycle, false, Input{}, func(execCtx context.Context) (string, error) {
		return runAcrossApprovals(execCtx, approve, record.RunID, "subagent continuation",
			func(attemptCtx context.Context, superviseRunID string) (string, error) {
				if runFork != nil {
					return runFork(attemptCtx, superviseRunID)
				}
				return executeSubagent(attemptCtx, fac, record, input, nil, superviseRunID, record.ParentRunID, record.SessionID, record.WorkerSessionID, subagentType)
			})
	})
	now := time.Now().Unix()
	final := record
	final.ExecutionID = executionID
	final.ParentToolCallID = lifecycle.ParentToolCallID
	final.TaskIndex = lifecycle.TaskIndex
	final.UpdatedAt = now
	final.FinishedAt = now
	switch {
	case runErr == nil:
		final.Status = agent.StatusOK
		final.Output = mergeContinuationOutput(record.Output, outText)
		final.Error = ""
	default:
		final.Status = agent.StatusFailed
		// Described rather than dumped, for the same reason finishSubagent
		// explains its own failure text.
		final.Error = llm.ExplainError(runErr)
	}
	if root := fac.subagentScopeRoot(); root != "" {
		_ = agent.AppendHistory(root, final)
	}
	syncGeneralSubagentRunState(fac, hostCtx, final)
	ended := final
	ended.Task = msg
	ended.Output = outText
	notifySubagentEndedDirect(fac, ended)
	publishSubagentContextBudget(fac, final)
	if runErr != nil {
		return final, runErr
	}
	return final, nil
}

func buildSubagentContinuePrompt(record agent.HistoryEntry, message string) string {
	var b strings.Builder
	b.WriteString("Continue the existing worker task with the follow-up instructions below.")
	if task := strings.TrimSpace(record.Task); task != "" {
		b.WriteString("\n\nOriginal task:\n")
		b.WriteString(task)
	}
	if out := strings.TrimSpace(record.Output); out != "" {
		b.WriteString("\n\nPrevious result:\n")
		b.WriteString(truncatePreviewText(out, 6000))
	}
	b.WriteString("\n\nFollow-up instructions:\n")
	b.WriteString(strings.TrimSpace(message))
	return b.String()
}

// continueForkSubagent continues a fork on its own conversation: the system
// frozen at its birth, and the worker session's transcript, replayed as the
// inherited prefix of one RunFork whose only new message is the follow-up.
// The store's tail-compare persists just that message, and the finished
// execution persists the rest — so the sidechain log keeps one copy per
// message (SidechainFrom skips the inherited history) and the next
// continuation's cached prefix is this request byte for byte.
func continueForkSubagent(ctx context.Context, fac Factory, record agent.HistoryEntry, input string) (string, error) {
	own := fac.Owner
	if own == nil || own.SubagentExecutor == nil {
		return "", fmt.Errorf("fork subagent requires runner")
	}
	llmClient := own.ForkLLM()
	if llmClient == nil {
		return "", fmt.Errorf("fork llm unavailable")
	}
	store := own.SessionStore
	frozen, ok, err := store.SessionPromptState(ctx, record.WorkerSessionID, forkSystemPromptKey)
	if err != nil {
		return "", err
	}
	if !ok || strings.TrimSpace(frozen) == "" {
		// Unreachable through continueSubagentExecution, which routes a
		// fork without a frozen system to the legacy branch; kept total so
		// the function states its own contract.
		return "", fmt.Errorf("fork continuation requires a frozen system prompt")
	}
	// The fork's pre-turn compaction runs before its new user message is
	// written, the way the conversation's own pre-turn compaction does. A
	// failure only logs — it must not stop the continuation it was making
	// room for, which is how chat_session's maybeAutoCompactBeforeAppend
	// treats a failure too.
	if _, _, cerr := CompactionService(own, store).AutoCompactSession(ctx, record.WorkerSessionID, ""); cerr != nil {
		slog.Warn("fork subagent pre-turn compaction skipped", "session_id", record.WorkerSessionID, "err", cerr)
	}
	ts := transcriptSession{store: store, systemPrompt: frozen, resolver: own.FileResolver}
	history, err := ts.history(ctx, record.WorkerSessionID)
	if err != nil {
		return "", err
	}
	model := forkAgentModel(own, preparedSubagent{sessionID: record.SessionID})
	started := time.Now()
	agentCtx := ctx
	capture := NewPartialSessionCapture()
	agentCtx = WithPartialSessionCapture(agentCtx, capture)
	outcome, runErr := RunFork(agentCtx, RunParams{
		LLM: llmClient,
		CacheSafe: &CacheSafeParams{
			SystemPrompt:         frozen,
			RenderedSystemPrompt: frozen,
			ParentMessages:       history,
		},
		PromptMessages: []llm.Message{llm.UserMessage(llm.Text(input))},
		CanUseTool:     func(string) bool { return true },
		Tools:          own.tools,
		AgentBaseName:  "subagent",
		AgentType:      "fork",
		AgentID:        record.AgentID,
		WorkspaceRoot:  fac.workspaceRoot(),
		SessionID:      record.SessionID,
		ParentRunID:    record.ParentRunID,
		ForkLabel:      "subagent",
		QuerySource:    record.QuerySource,
		OnInitialMessages: func(initial []llm.Message) {
			_ = store.AppendMessageSequenceForRun(context.Background(), record.WorkerSessionID, record.RunID, initial, model, "")
		},
		SidechainFrom: len(history),
		RegisterTools: forkSubagentToolRegistrar(own),
	})
	own.SubagentExecutor.PersistSubagentTurn(agentCtx, SubagentTurn{
		WorkerSessionID: record.WorkerSessionID,
		RunID:           record.RunID,
		Model:           model,
		Result:          forkOutcomeResult(outcome),
		Partial:         capture.Snapshot(),
		Err:             runErr,
		Started:         started,
	})
	if runErr != nil {
		return "", runErr
	}
	if outcome == nil || outcome.Result == nil {
		return "", nil
	}
	return outcome.Result.TextContent(), nil
}

func mergeContinuationOutput(previous, next string) string {
	prev := strings.TrimSpace(previous)
	cur := strings.TrimSpace(next)
	switch {
	case prev == "":
		return truncatePreviewText(cur, 24000)
	case cur == "":
		return truncatePreviewText(prev, 24000)
	default:
		return truncatePreviewText(prev+"\n\nContinuation result:\n"+cur, 24000)
	}
}

func registerFanoutTool(a *agent.Agent, fac Factory) error {
	t, err := llm.NewTool(
		"subagent_fanout",
		"Run multiple subtasks concurrently in isolated agents and aggregate results. Returns a JSON array of results.",
		func(ctx context.Context, in *FanoutInput) (string, error) {
			if err := guardSubagentTool(ctx, fac, "subagent_fanout"); err != nil {
				return "", err
			}
			if err := guardForkChildNoSubagentTools(ctx); err != nil {
				return "", err
			}
			if in == nil || len(in.Tasks) == 0 {
				return "", fmt.Errorf("tasks required")
			}
			for _, item := range in.Tasks {
				if err := guardForkChildImplicitFork(ctx, item.SubagentType); err != nil {
					return "", err
				}
			}
			maxP := in.MaxParallel
			if maxP <= 0 {
				return "", fmt.Errorf("subagent_fanout max_parallel must be positive; main agent must execute this fanout directly")
			}
			globalLimit := maxParallelSubagentsFromFactory(fac)
			if globalLimit <= 0 {
				return "", fmt.Errorf("subagent capacity is 0; main agent must execute this fanout directly")
			}
			if maxP > globalLimit {
				maxP = globalLimit
			}

			results := make([]FanoutResult, len(in.Tasks))
			sem := make(chan struct{}, maxP)
			var mu sync.Mutex
			var wg sync.WaitGroup
			var firstErr error
			// suspended holds an approval gate no surface answered in place.
			// It is not a task failure and must never be reported as one: the
			// fanout stops and the gate travels to a caller that can suspend.
			var suspended error
			fanoutCtx, fanoutCancel := context.WithCancel(ctx)
			defer fanoutCancel()

			taskIDs := make([]string, len(in.Tasks))
			for i := range taskIDs {
				taskIDs[i] = newSubagentTaskID()
			}
			for i, item := range in.Tasks {
				task := strings.TrimSpace(item.Prompt)
				subType := strings.TrimSpace(item.SubagentType)
				title := strings.TrimSpace(item.Title)
				if task == "" {
					results[i] = FanoutResult{Index: i, Task: task, SubagentType: subType, Error: "skipped: empty prompt", OK: false}
					continue
				}

				select {
				case sem <- struct{}{}:
				case <-fanoutCtx.Done():
					results[i] = FanoutResult{Index: i, Task: task, SubagentType: subType, Error: "skipped due to fail_fast", OK: false}
					continue
				}

				mu.Lock()
				if in.FailFast && firstErr != nil {
					mu.Unlock()
					<-sem
					results[i] = FanoutResult{Index: i, Task: task, SubagentType: subType, Error: "skipped due to fail_fast", OK: false}
					continue
				}
				mu.Unlock()

				wg.Add(1)
				go func(i int, task, subType, title, taskID string) {
					defer wg.Done()
					defer func() { <-sem }()

					execRes, err := execGeneralSubagent(withSubagentTaskIndex(fanoutCtx, i), fac, taskID, title, task, subType)
					if err != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = err
						}
						gate := isRequiresActionError(err)
						if gate && suspended == nil {
							suspended = err
						} else if gate {
							// Only one gate can be answered, so the rest are
							// withdrawn instead of being left pending on the
							// queue with nobody coming for them.
							cancelPendingAction(fac, err)
						}
						if in.FailFast || gate {
							fanoutCancel()
						}
						mu.Unlock()
						results[i] = FanoutResult{Index: i, Task: task, SubagentType: subType, Error: err.Error(), OK: false}
						return
					}
					results[i] = FanoutResult{
						Index:        i,
						Task:         task,
						SubagentType: subType,
						Output:       governSubagentOutput(fac, "subagent_fanout", taskID, execRes.Output).Text,
						OK:           true,
					}
				}(i, task, subType, title, taskIDs[i])
			}
			wg.Wait()

			if suspended != nil {
				// The dispatching agent's own tool loop treats this as the
				// control-flow signal it is and suspends the turn, the same way
				// a gate raised by subagent_run does.
				return "", suspended
			}
			if in.FailFast && firstErr != nil {
				return buildFanoutSummary(results), firstErr
			}
			return buildFanoutSummary(results), nil
		},
	)
	if err != nil {
		return err
	}
	if err := applySubagentTypeEnumToTool(t, []string{"properties", "tasks", "items", "properties", "subagent_type"}); err != nil {
		return err
	}
	if err := fac.Tools.Register(a, t); err != nil {
		return err
	}
	if fac.Tools != nil {
		fac.Tools.RegisterToolMeta(event.ToolMeta{
			Name:        "subagent_fanout",
			Description: "Use when running several independent subtasks at once: dispatches them concurrently in isolated agents and aggregates results.",
			Category:    "subagent",
			ReadOnly:    true,
			InputSchema: subagentSchemaRaw(reflect.TypeOf(FanoutInput{}), []string{"properties", "tasks", "items", "properties", "subagent_type"}),
		})
	}
	return registerSubagentLifecycleTools(a, fac)
}

func maxParallelSubagentsFromFactory(fac Factory) int {
	return maxParallelSubagentsFromAppConfig(fac.appConfig())
}

func registerSubagentLifecycleTools(a *agent.Agent, fac Factory) error {
	if err := addSubagentLifecycleTool(a, fac, "subagent_send",
		"Use when starting a subagent fire-and-forget so you can keep working: launches an asynchronous isolated subagent and returns immediately with task and run identifiers for later wait/status/close.",
		func(ctx context.Context, in *SubagentSendInput) (string, error) {
			if err := guardSubagentTool(ctx, fac, "subagent_send"); err != nil {
				return "", err
			}
			if err := guardForkChildNoSubagentTools(ctx); err != nil {
				return "", err
			}
			task := ""
			subType := ""
			title := ""
			if in != nil {
				task = strings.TrimSpace(in.Task)
				subType = strings.TrimSpace(in.SubagentType)
				title = strings.TrimSpace(in.Title)
			}
			if task == "" {
				b, _ := json.Marshal(map[string]any{
					"status": "skipped",
					"error":  "skipped: empty prompt",
				})
				return string(b), nil
			}
			if err := guardForkChildImplicitFork(ctx, subType); err != nil {
				return "", err
			}
			baseCtx := context.Background()
			if sid := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx)); sid != "" {
				baseCtx = llm.WithAgentSessionID(baseCtx, sid)
				baseCtx = tool.WithConversationSessionID(baseCtx, sid)
			}
			if cacheKey := strings.TrimSpace(llm.PromptCacheKeyFromContext(ctx)); cacheKey != "" {
				baseCtx = llm.WithPromptCacheKey(baseCtx, cacheKey)
			}
			if parent := strings.TrimSpace(tool.RunIDFromContext(ctx)); parent != "" {
				baseCtx = tool.WithRunID(baseCtx, parent)
			}
			// The dispatching call's id is the only fact a surface has to draw
			// this agent into that call's card; the async execution leaves the
			// dispatching context behind, so it has to be carried over
			// explicitly.
			baseCtx = tool.WithToolUseID(baseCtx, tool.ToolUseIDFromContext(ctx))
			taskID := newSubagentTaskID()
			entry, err := spawnAsyncSubagent(baseCtx, fac, taskID, title, task, subType)
			if err != nil {
				return "", err
			}
			b, _ := json.Marshal(map[string]any{
				"agent_id":          entry.AgentID,
				"task_id":           entry.TaskID,
				"run_id":            entry.RunID,
				"parent_run_id":     entry.ParentRunID,
				"session_id":        entry.SessionID,
				"worker_session_id": entry.WorkerSessionID,
				"query_source":      entry.QuerySource,
				"status":            entry.Status,
				"started_at":        entry.StartedAt,
				"agent_kind":        entry.AgentKind,
				"agent_type":        entry.AgentType,
				"runtime_kind":      entry.RuntimeKind,
			})
			return string(b), nil
		},
	); err != nil {
		return err
	}
	if err := addSubagentLifecycleTool(a, fac, "subagent_list",
		"List recent subagents scoped to the current session and parent run when available.",
		func(ctx context.Context, in *SubagentListInput) (string, error) {
			if err := guardSubagentTool(ctx, fac, "subagent_list"); err != nil {
				return "", err
			}
			if err := guardForkChildNoSubagentTools(ctx); err != nil {
				return "", err
			}
			limit := 20
			if in != nil && in.Limit > 0 {
				limit = in.Limit
			}
			list, err := agent.ListMerged(fac.subagentScopeRoot(), agent.Query{
				SessionID:   strings.TrimSpace(llm.AgentSessionIDFromContext(ctx)),
				ParentRunID: strings.TrimSpace(tool.RunIDFromContext(ctx)),
				Limit:       limit,
			})
			if err != nil {
				return "", err
			}
			b, _ := json.Marshal(map[string]any{"records": list})
			return string(b), nil
		},
	); err != nil {
		return err
	}
	if err := addSubagentLifecycleTool(a, fac, "subagent_status",
		"Use when polling a sent subagent without blocking: gets its latest status by task_id or run_id. If both are empty, returns the latest scoped subagent.",
		func(ctx context.Context, in *SubagentLookupInput) (string, error) {
			if err := guardSubagentTool(ctx, fac, "subagent_status"); err != nil {
				return "", err
			}
			if err := guardForkChildNoSubagentTools(ctx); err != nil {
				return "", err
			}
			record, ok, err := lookupSubagentRecord(fac.subagentScopeRoot(), ctx, in)
			if err != nil {
				return "", err
			}
			if !ok {
				return "", fmt.Errorf("subagent not found")
			}
			b, _ := json.Marshal(record)
			return string(b), nil
		},
	); err != nil {
		return err
	}
	if err := addSubagentLifecycleTool(a, fac, "subagent_wait",
		"Use when blocking until a previously-sent subagent finishes: waits by task_id or run_id. Returns the latest status even when the wait times out.",
		func(ctx context.Context, in *SubagentLookupInput) (string, error) {
			if err := guardSubagentTool(ctx, fac, "subagent_wait"); err != nil {
				return "", err
			}
			if err := guardForkChildNoSubagentTools(ctx); err != nil {
				return "", err
			}
			timeout := 60 * time.Second
			if in != nil && in.TimeoutMS > 0 {
				timeout = time.Duration(in.TimeoutMS) * time.Millisecond
			}
			query := lookupQueryFromContext(ctx, in)
			if handle, ok := agent.RegistryFor(fac.subagentScopeRoot()).Get(query); ok && handle != nil {
				waitCtx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				record, waitErr := handle.Wait(waitCtx)
				resp := map[string]any{
					"record": record,
				}
				if waitErr != nil {
					resp["timed_out"] = true
				}
				b, _ := json.Marshal(resp)
				return string(b), nil
			}
			record, ok, err := lookupSubagentRecord(fac.subagentScopeRoot(), ctx, in)
			if err != nil {
				return "", err
			}
			if !ok {
				return "", fmt.Errorf("subagent not found")
			}
			b, _ := json.Marshal(map[string]any{"record": record})
			return string(b), nil
		},
	); err != nil {
		return err
	}
	if err := addSubagentLifecycleTool(a, fac, "subagent_continue",
		"Use when giving an existing async subagent follow-up instructions: continues it by task_id or run_id with a self-contained brief.",
		func(ctx context.Context, in *SubagentContinueInput) (string, error) {
			if err := guardSubagentTool(ctx, fac, "subagent_continue"); err != nil {
				return "", err
			}
			if err := guardForkChildNoSubagentTools(ctx); err != nil {
				return "", err
			}
			record, err := continueSubagentExecution(ctx, fac, in)
			if err != nil {
				return "", err
			}
			b, _ := json.Marshal(map[string]any{"record": record})
			return string(b), nil
		},
	); err != nil {
		return err
	}
	return addSubagentLifecycleTool(a, fac, "subagent_close",
		"Use when cancelling a running async subagent you no longer need: closes it by task_id or run_id (one is required — unlike status/wait there is no latest-scoped fallback). Returns not-found if the subagent has already finished.",
		func(ctx context.Context, in *SubagentLookupInput) (string, error) {
			if err := guardSubagentTool(ctx, fac, "subagent_close"); err != nil {
				return "", err
			}
			if err := guardForkChildNoSubagentTools(ctx); err != nil {
				return "", err
			}
			query := lookupQueryFromContext(ctx, in)
			if !agent.RegistryFor(fac.subagentScopeRoot()).Cancel(query) {
				return "", fmt.Errorf("running subagent not found")
			}
			record, ok, err := lookupSubagentRecord(fac.subagentScopeRoot(), ctx, in)
			if err != nil {
				return "", err
			}
			if !ok {
				b, _ := json.Marshal(map[string]any{"status": "cancel_requested"})
				return string(b), nil
			}
			b, _ := json.Marshal(map[string]any{"status": "cancel_requested", "record": record})
			return string(b), nil
		},
	)
}

func addSubagentLifecycleTool[T any](a *agent.Agent, fac Factory, name, desc string, fn func(context.Context, *T) (string, error)) error {
	t, err := llm.NewTool(name, desc, fn)
	if err != nil {
		return err
	}
	if name == "subagent_send" {
		if err := applySubagentTypeEnumToTool(t, []string{"properties", "subagent_type"}); err != nil {
			return err
		}
	}
	if err := fac.Tools.Register(a, t); err != nil {
		return err
	}
	if fac.Tools != nil {
		var zero T
		fac.Tools.RegisterToolMeta(event.ToolMeta{
			Name:        name,
			Description: desc,
			Category:    "subagent",
			ReadOnly:    true,
			InputSchema: lifecycleToolInputSchema(name, reflect.TypeOf(zero)),
		})
	}
	return nil
}

func lookupQueryFromContext(ctx context.Context, in *SubagentLookupInput) agent.Query {
	// An explicit task_id or run_id is the address. The lookup stays scoped to
	// the conversation — an entry of another conversation is not this one to
	// continue — but not to the turn that is asking: a continuation of a
	// subagent dispatched in an earlier turn would otherwise never be found,
	// because the asking turn's run id is not that subagent's parent run. The
	// current-run scoping is for the addressless lookups, where "the
	// subagents of this run" is exactly the question.
	query := agent.Query{
		SessionID: strings.TrimSpace(llm.AgentSessionIDFromContext(ctx)),
		Limit:     1,
	}
	if in == nil || (strings.TrimSpace(in.TaskID) == "" && strings.TrimSpace(in.RunID) == "") {
		query.ParentRunID = strings.TrimSpace(tool.RunIDFromContext(ctx))
		return query
	}
	query.TaskID = strings.TrimSpace(in.TaskID)
	query.RunID = strings.TrimSpace(in.RunID)
	return query
}

func lookupSubagentRecord(workspaceRoot string, ctx context.Context, in *SubagentLookupInput) (agent.HistoryEntry, bool, error) {
	query := lookupQueryFromContext(ctx, in)
	if query.TaskID == "" && query.RunID == "" {
		list, err := agent.ListMerged(workspaceRoot, query)
		if err != nil {
			return agent.HistoryEntry{}, false, err
		}
		if len(list) == 0 {
			return agent.HistoryEntry{}, false, nil
		}
		return list[0], true, nil
	}
	return agent.GetMerged(workspaceRoot, query)
}

func buildFanoutSummary(results []FanoutResult) string {
	ok, failed := 0, 0
	for _, r := range results {
		if r.OK {
			ok++
		} else {
			failed++
		}
	}
	resp := map[string]any{
		"summary": map[string]any{
			"total":    len(results),
			"succeed":  ok,
			"failed":   failed,
			"finished": time.Now().Unix(),
		},
		"results": results,
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

func truncatePreviewText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return llm.TruncateBytes(s, n, "…")
}

// subagentDispatch describes one subagent run: which definitions it may adopt,
// how its executor is invoked, and how its output is bounded. That is the whole of
// what differs between the three ways a subagent starts — the tool's blocking
// run, the tool's background spawn, and plan review — so everything else
// happens once, in startSubagent and finishSubagent.
type subagentDispatch struct {
	taskID string
	// title is the short name the dispatching agent gave this task: what the
	// roster row and the task card show, as opposed to task, the whole prompt
	// that only the subagent's own view prints.
	title   string
	task    string
	subtype string
	// resolve decides which definitions this dispatcher may name. The tool
	// paths pass the public resolver so a model cannot reach a reserved
	// subtype; an internal dispatcher passes the full one.
	resolve func(string) (agent.Definition, error)
	// run executes the prepared subagent and returns its final text. It
	// receives the dispatching context (the fork path reads the parent's
	// runtime snapshot from it); a background run must build from prep.ctx
	// instead, which outlives the call that started it.
	run func(ctx context.Context, fac Factory, prep preparedSubagent) (string, error)
	// governTool names the tool whose output budget bounds the stored result.
	// Empty stores the output as produced.
	governTool string
}

// startSubagent prepares a subagent and announces it: history entry, live
// registry handle (what makes the roster's stop work), and the spawn
// notification the roster row and the agent's own view are built from.
//
// The caller owns prep.cancel and prep.detach from here on.
func startSubagent(ctx context.Context, fac Factory, d subagentDispatch) (preparedSubagent, *agent.Handle, error) {
	prep, err := prepareSubagentExecutionResolved(ctx, fac, d)
	if err != nil {
		return preparedSubagent{}, nil, err
	}
	if root := fac.subagentScopeRoot(); root != "" {
		_ = agent.AppendHistory(root, prep.entry)
	}
	handle := agent.RegistryFor(fac.subagentScopeRoot()).Start(prep.entry, prep.cancel)
	notifySubagentSpawnedDirect(fac, prep.entry, "")
	return prep, handle, nil
}

type subagentTaskIndexKey struct{}

func withSubagentTaskIndex(ctx context.Context, index int) context.Context {
	return context.WithValue(ctx, subagentTaskIndexKey{}, index)
}

func subagentTaskIndexFromContext(ctx context.Context) int {
	if index, ok := ctx.Value(subagentTaskIndexKey{}).(int); ok {
		return index
	}
	return 0
}

// finishSubagent turns a run's outcome into the subagent's terminal state:
// status and output on the history entry, the run-state sync that closes its
// child run, the registry handle's completion, and the end notification that
// removes its roster row. Returns the final entry so a caller can report it.
func finishSubagent(
	fac Factory, d subagentDispatch, prep preparedSubagent, handle *agent.Handle, output string, runErr error,
) agent.HistoryEntry {
	final := prep.entry
	final.UpdatedAt = time.Now().Unix()
	switch {
	case runErr == nil:
		final.Status = agent.StatusOK
		final.Output = output
		if tool := strings.TrimSpace(d.governTool); tool != "" {
			final.Output = governSubagentOutput(fac, tool, prep.entry.TaskID, output).Text
		}
	case errors.Is(runErr, context.Canceled) || errors.Is(context.Cause(prep.ctx), context.Canceled):
		final.Status = agent.StatusCancelled
		final.Error = "cancelled"
	default:
		final.Status = agent.StatusFailed
		// This text is what every surface shows for the failure — the fanout
		// card, the roster row, the closing frame in the subagent's own view,
		// the run record — so a provider refusal is described rather than
		// dumped as the raw HTTP body it arrives as. The untouched error still
		// reaches the log.
		final.Error = llm.ExplainError(runErr)
	}
	syncGeneralSubagentRunState(fac, prep.ctx, final)
	finishSubagentExecution(fac, handle, final)
	notifySubagentEndedDirect(fac, final)
	publishSubagentContextBudget(fac, final)
	return final
}

// dispatchSubagent runs a subagent to completion in the caller's goroutine and
// returns its governed output. A panic in the subagent becomes an error rather
// than taking the calling turn down with it.
func dispatchSubagent(
	ctx context.Context, fac Factory, d subagentDispatch,
) (prep preparedSubagent, output string, err error) {
	var handle *agent.Handle
	// This outer guard also covers a panic while resolving/preparing the child,
	// before a registry handle exists. Once started, the inner guard below runs
	// before cancel/detach so panic cleanup is recorded as failed, not cancelled.
	defer func() {
		if recovered := recover(); recovered != nil {
			telemetry.Log(fac.Home, "run.dispatchSubagent", recovered)
			output = ""
			err = fmt.Errorf("subagent panic: %v", recovered)
		}
	}()
	prep, handle, err = startSubagent(ctx, fac, d)
	if err != nil {
		return preparedSubagent{}, "", err
	}
	defer prep.cancel()
	defer prep.detach()
	defer func() {
		if recovered := recover(); recovered != nil {
			telemetry.Log(fac.Home, "run.dispatchSubagent", recovered)
			panicErr := fmt.Errorf("subagent panic: %v", recovered)
			finishSubagent(fac, d, prep, handle, "", panicErr)
			output = ""
			err = panicErr
		}
	}()

	output, runErr := d.run(ctx, fac, prep)
	final := finishSubagent(fac, d, prep, handle, output, runErr)
	if runErr != nil {
		return prep, "", runErr
	}
	return prep, final.Output, nil
}

// typedSubagentExecContext tags a prepared subagent's context the way every
// typed run needs it: the subtype and its definition source drive the system
// prompt and tool policy, and the roster key routes the run's frames into that
// agent's own view.
func typedSubagentExecContext(prep preparedSubagent) context.Context {
	hostCtx := tool.WithSubagentType(prep.ctx, prep.agentType)
	hostCtx = tool.WithSubagentDefinitionSource(hostCtx, prep.defSource)
	return tool.WithHookAgentID(hostCtx, subagentRosterKey(prep.entry))
}

// SubagentModelOverride names the model one subagent run must use, chosen at
// dispatch time rather than from configuration. Plan review is the caller that
// needs it: the user picks the reviewing model in the approval overlay, so the
// choice cannot come from agents.definitions[<type>].llm_providers the way a
// typed subagent's dedicated provider does.
type SubagentModelOverride struct {
	Provider string
	Model    string
}

func (o SubagentModelOverride) valid() bool {
	return strings.TrimSpace(o.Model) != ""
}

type subagentModelOverrideKey struct{}

// WithSubagentModelOverride pins the model for the run started under ctx. It
// applies to every LLM call that run makes, including the ones its own tool
// loop issues.
func WithSubagentModelOverride(ctx context.Context, override SubagentModelOverride) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if !override.valid() {
		return ctx
	}
	return context.WithValue(ctx, subagentModelOverrideKey{}, override)
}

// SubagentModelOverrideFromContext reports the dispatch-time model override
// pinned on ctx, if any. It is the same fact the run's LLM chain routes on;
// surfaces and persistence read it to name the model a run actually uses.
func SubagentModelOverrideFromContext(ctx context.Context) (SubagentModelOverride, bool) {
	if ctx == nil {
		return SubagentModelOverride{}, false
	}
	override, ok := ctx.Value(subagentModelOverrideKey{}).(SubagentModelOverride)
	if !ok || !override.valid() {
		return SubagentModelOverride{}, false
	}
	return override, true
}

// subagentModelOverrideLLM routes a run pinned by WithSubagentModelOverride to
// that model's client. It sits above typedSubagentProviderLLM so an explicit
// per-run choice wins over the per-type provider configuration, and below the
// prompt and tool-filter wrappers so the subagent's system prompt and tool
// policy still apply on top of the substituted model.
type subagentModelOverrideLLM struct {
	inner llm.LLM
	build func(SubagentModelOverride) (llm.LLM, error)

	mu      sync.Mutex
	clients map[string]llm.LLM
}

func wrapSubagentModelOverrideLLM(inner llm.LLM, build func(SubagentModelOverride) (llm.LLM, error)) llm.LLM {
	if inner == nil || build == nil {
		return inner
	}
	return &subagentModelOverrideLLM{inner: inner, build: build, clients: map[string]llm.LLM{}}
}

func (w *subagentModelOverrideLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	override, ok := SubagentModelOverrideFromContext(ctx)
	if !ok {
		return w.inner.Execute(ctx, messages, tools)
	}
	client, err := w.clientFor(override)
	if err != nil {
		// The user picked this model on purpose. Silently answering with the
		// session's own model would return a "second opinion" from the model
		// that wrote the plan, so the run fails instead.
		return nil, err
	}
	return client.Execute(ctx, messages, tools)
}

// clientFor reuses one client per model for the life of the runner: a review is
// a whole agent run, and building a fresh HTTP client for every step of its
// tool loop would throw away connection reuse for no reason.
func (w *subagentModelOverrideLLM) clientFor(override SubagentModelOverride) (llm.LLM, error) {
	key := strings.ToLower(strings.TrimSpace(override.Provider) + "/" + strings.TrimSpace(override.Model))
	w.mu.Lock()
	defer w.mu.Unlock()
	if client, ok := w.clients[key]; ok && client != nil {
		return client, nil
	}
	client, err := w.build(override)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("model %s is not configured for this session", subagentModelDisplay(override))
	}
	w.clients[key] = client
	return client, nil
}

func subagentModelDisplay(override SubagentModelOverride) string {
	return llm.FormatProviderModel(override.Provider, override.Model)
}

// ConfiguredModelClient builds the client for one provider/model pair out of an
// agent's configured provider chain. The pair must be configured for that
// agent: a model the session holds no credentials for is an error, never a
// silent fallback to a different model — the user picked this one on purpose.
func ConfiguredModelClient(cfg *appcfg.Root, agentType, provider, model string) (llm.LLM, error) {
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("a model is required")
	}
	_, client, err := resolveAgentLLM(cfg, agentType, modelSelector{provider: provider, model: model})
	if errors.Is(err, errModelNotConfigured) {
		return nil, fmt.Errorf("model %s is not configured for this session",
			subagentModelDisplay(SubagentModelOverride{Provider: provider, Model: model}))
	}
	return client, err
}

func governSubagentOutput(fac Factory, toolName, callID, output string) tool.GovernedOutput {
	governor := newToolOutputGovernor(factoryToolsState(fac))
	if governor == nil {
		return tool.GovernedOutput{Text: output}
	}
	return governor.GovernDetailed(strings.TrimSpace(toolName), strings.TrimSpace(callID), output)
}

// cancelPendingAction withdraws the approval request behind err. A fanout can
// only carry one gate to the surface, and an approval left pending would sit on
// the queue for a run that has already been cancelled.
func cancelPendingAction(fac Factory, err error) {
	var rae *tool.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		return
	}
	actionID := strings.TrimSpace(rae.ActionID)
	if fac.Owner == nil || fac.Owner.Actions == nil || actionID == "" {
		return
	}
	_, _ = fac.Owner.Actions.Cancel(context.Background(), actionID, "the fanout that requested it was stopped at another approval")
}

func factoryToolsState(fac Factory) *tool.State {
	if fac.Tools != nil {
		return fac.Tools
	}
	if fac.Owner != nil && fac.Owner.tools != nil {
		return fac.Owner.tools
	}
	return nil
}

// SubagentExecRequest is one subagent execution's dispatch: its input, the
// run it belongs to, and the sessions it runs in. It replaced a row of
// positional string parameters so a user's message can travel with the parts
// it was written with — an image the surface attached included — instead of
// as text alone.
type SubagentExecRequest struct {
	// Task is the input's text, as the dispatch always carried it.
	Task string
	// Parts is the input as parts (images included) when the surface sent
	// more than text; Task is its text. Empty for a dispatch the model made
	// from text alone.
	Parts []llm.ContentPart
	// SuperviseRunID continues an existing child run instead of starting a
	// new one; ParentRunID names the parent when no child run exists yet.
	SuperviseRunID string
	ParentRunID    string
	// SessionID is the conversation the subagent belongs to; WorkerSessionID
	// is the subagent's own.
	SessionID       string
	WorkerSessionID string
	SubagentType    string
	// OnUserTurn, when set, receives the row id of the user message this
	// execution wrote to its worker session, as soon as it is written. It is
	// how a user-driven execution tells its channel which row to withdraw
	// before the model answers. A dispatch the model made leaves it nil.
	OnUserTurn func(rowID int64)
}

type SubagentExecutor interface {
	RunSubagentExec(ctx context.Context, req SubagentExecRequest) (string, error)

	// PersistSubagentTurn writes what one subagent execution produced into
	// the subagent's own worker session, exactly as a primary turn is
	// written: the rows of the run's message list the session does not hold
	// yet. Err is the execution's error; a failed or cancelled execution
	// writes the partial session it captured and answers the calls it
	// interrupted.
	PersistSubagentTurn(ctx context.Context, turn SubagentTurn)

	// SubagentExecutionStarting and SubagentExecutionEnded report one
	// execution of a subagent to whatever schedules its continuations. Every
	// execution goes through runSubagentExecution, so a start supersedes a
	// continuation the subagent was waiting on (the conversation moved on)
	// and an end arms a new one when the execution stopped on a usage limit.
	SubagentExecutionStarting(ctx context.Context, workerSessionID string)
	SubagentExecutionEnded(ctx context.Context, end SubagentExecutionEnd)
}

// SubagentExecutionEnd reports one finished subagent execution. The
// composition root translates it into whatever its scheduler takes; the
// surface the subagent's conversation belongs to is supplied there, since the
// engine has no reliable source for it on the execution's context.
type SubagentExecutionEnd struct {
	// ConversationSessionID is the conversation the subagent belongs to: the
	// session the continuation's lifecycle events are filed under.
	ConversationSessionID string
	// WorkerSessionID is the subagent's own session, which keys its
	// continuation.
	WorkerSessionID string
	// AgentKey is the subagent's roster key, carried on the events so the
	// surface draws the notice in that subagent's view.
	AgentKey string
	// RunID is the run this execution ran as.
	RunID string
	// Err is how the execution ended; nil when it succeeded.
	Err error
}

// SubagentTurn is what one subagent execution produced, handed to the
// composition root for persistence into the subagent's own worker session.
// The execution's own view of the conversation — the same list the
// orchestration loop assembled — is what gets written, so a continuation
// rebuilds the exact context this execution had.
type SubagentTurn struct {
	// WorkerSessionID is the session the execution ran in: the one its
	// dispatch birthed for it.
	WorkerSessionID string
	// RunID is the supervised run of this execution; the rows it writes are
	// bound to it, and the run's clock is stamped from Started.
	RunID string
	// Model names the model the execution ran on, the way the primary
	// session's rows do — a dispatch-time override wins, then a typed
	// subagent's own provider chain, then the conversation's model.
	Model string
	// Result is the finished execution's result. Nil when Err is set.
	Result *agent.Result
	// Partial is the session the execution captured up to its failure or
	// cancellation. Empty on success, where Result.Session carries
	// everything.
	Partial []llm.Message
	// Err is the execution's outcome: nil for finished, an approval-gate
	// error while the run is parked (nothing is written), anything else for
	// failed or cancelled.
	Err error
	// Started is when the execution began. A zero value reads as "the whole
	// window is unknown"; the store stamps what it can.
	Started time.Time
}

// PlanReviewSubagentType is the built-in definition the plan reviewer runs as.
// It is reserved, never offered in the subagent_run enum: the reviewer is
// dispatched by the exit-plan approval, not by the model.
const PlanReviewSubagentType = "plan-reviewer"

// PlanReviewApprovalFunc is called when the review run suspends on a tool
// approval. It must resolve the pending action and return the context to resume
// with — carrying the approved action id and the resume state — or an error to
// abort the review. Returning an error leaves the run cancelled and reported.
//
// It is an alias of the general seam every subagent's approvals travel on, so
// the plan reviewer and a model-dispatched subagent are resolved by one
// implementation on the surface side rather than two.
type PlanReviewApprovalFunc = tool.SubagentApprovalHook

// PlanReviewApprovalBudget bounds how many tool approvals one review may ask the
// user for. A review that needs more than this is not reviewing any more.
const PlanReviewApprovalBudget = SubagentApprovalBudget

// RunPlanReviewSubagent dispatches the plan reviewer through dispatchSubagent,
// the same path the subagent tools use: history entry, live registry handle,
// the spawn/end notifications the agent roster is built from, and a context
// tagged with the roster key so the surface routes the reviewer's assistant
// text, reasoning and tool frames into that agent's own view.
//
// What differs from a model-dispatched subagent is only who asks for it and
// which model answers: the approval overlay asks, and override pins the model
// the user picked. Everything the UI keys off — roster row, per-agent screen,
// cancel-by-row — therefore behaves exactly as it does for subagent_run.
func (r *Runner) RunPlanReviewSubagent(
	ctx context.Context, task string, override SubagentModelOverride, approve PlanReviewApprovalFunc,
) (string, error) {
	if r == nil {
		return "", fmt.Errorf("plan review requires a runner")
	}
	if r.SubagentExecutor == nil {
		return "", fmt.Errorf("plan review requires the in-process subagent executor")
	}
	// The override is pinned before the dispatch — not inside the run
	// callback — so the execution's own record reads it from the context it
	// derives from: the history entry and the spawned event name the model
	// the user picked, the way they name the dispatch's tool-use id. Every
	// context below (the prepared execution's, the reviewer's calls) derives
	// from this one, so the pinning reaches the whole run.
	ctx = WithSubagentModelOverride(ctx, override)
	// agents.defaults.enable_subagent governs the subagents the model may
	// spawn; the reviewer is dispatched only because the user asked for it
	// from the approval overlay, so that switch does not apply here.
	_, output, err := dispatchSubagent(ctx, r.subagentFactory(), subagentDispatch{
		taskID:  newSubagentTaskID(),
		title:   "Plan review",
		task:    task,
		subtype: PlanReviewSubagentType,
		// The reviewer is a reserved definition, absent from the enum the model
		// sees, so it resolves against every built-in rather than the public set.
		resolve: agent.ResolveSubtype,
		run: func(_ context.Context, fac Factory, prep preparedSubagent) (string, error) {
			return r.runPlanReviewAcrossApprovals(typedSubagentExecContext(prep), fac, prep, approve)
		},
	})
	if err != nil {
		return "", err
	}
	return output, nil
}

// runGoalCheck dispatches a goal's check as a subagent of the turn working
// toward it, and returns the check's final answer. It is dispatched the way
// the subagent_run tool dispatches one — on the roster, with a view of its own,
// its tool approvals answered in place — except that it is reserved (absent
// from the enum the model sees) and runs whether or not the model may spawn
// subagents, because the user asked for it with /goal.
func (r *Runner) runGoalCheck(ctx context.Context, task string) (output, agentID string, err error) {
	if r.SubagentExecutor == nil {
		return "", "", fmt.Errorf("the goal check requires the in-process subagent executor")
	}
	prep, output, err := dispatchSubagent(ctx, r.subagentFactory(), subagentDispatch{
		taskID:  newSubagentTaskID(),
		title:   "Goal check",
		task:    task,
		subtype: GoalCheckSubagentType,
		resolve: agent.ResolveSubtype,
		run: func(runCtx context.Context, fac Factory, prep preparedSubagent) (string, error) {
			approve := tool.SubagentApprovalHookFromContext(runCtx, factoryToolsState(fac))
			return runSubagentExecution(typedSubagentExecContext(prep), fac, prep.entry, false, Input{}, func(execCtx context.Context) (string, error) {
				return runAcrossApprovals(execCtx, approve, prep.superviseRunID, "goal check",
					func(attemptCtx context.Context, superviseRunID string) (string, error) {
						return executeSubagent(
							attemptCtx, fac, prep.entry, prep.task, nil, superviseRunID, prep.parentRunID,
							prep.sessionID, prep.workerSessionID, prep.subagentType,
						)
					})
			})
		},
	})
	return output, subagentRosterKey(prep.entry), err
}

// runPlanReviewAcrossApprovals keeps the review run alive across its own tool approvals.
// The reviewer investigates the repository, so it hits the same gates any agent
// hits; the plan approval that started it is off screen while it runs, so its
// request can be put to the user in that place and the run resumed with the
// answer. A reviewer is never refused a tool for lack of an approval path.
func (r *Runner) runPlanReviewAcrossApprovals(
	ctx context.Context, fac Factory, prep preparedSubagent, approve PlanReviewApprovalFunc,
) (string, error) {
	return runSubagentExecution(ctx, fac, prep.entry, false, Input{}, func(execCtx context.Context) (string, error) {
		return runAcrossApprovals(execCtx, approve, prep.superviseRunID, "plan review",
			func(runCtx context.Context, superviseRunID string) (string, error) {
				return executeSubagent(
					runCtx, fac, prep.entry, prep.task, nil, superviseRunID, prep.parentRunID,
					prep.sessionID, prep.workerSessionID, prep.subagentType,
				)
			})
	})
}
