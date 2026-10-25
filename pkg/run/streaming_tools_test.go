package run

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	toolpkg "github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// ---- plan 124: tool calls execute as they complete in the stream ----

// streamTestSink is a foreground sink with the boundary callbacks a provider
// needs to report a streaming response.
func streamTestSink() *llm.StreamSink {
	return &llm.StreamSink{
		OnDelta:          func(string) {},
		OnTextBlockDone:  func() {},
		OnToolCallReady:  func(llm.ToolCall) {},
		OnMessageStop:    func() {},
		OnResponseNotice: func(llm.ResponseNotice) {},
	}
}

// streamHookTool builds a tool that consults the action hook before its work,
// the way the product's gated tools do, and counts its real executions.
func streamHookTool(t *testing.T, st *toolpkg.State, name string, fn func(ctx context.Context) (string, error)) (*llm.Tool, *int32) {
	t.Helper()
	var runs int32
	toolObj, err := llm.NewTool(name, "test tool", func(ctx context.Context, _ *struct{}) (string, error) {
		if hook := st.ActionHook(); hook != nil {
			id, pending, hookErr := hook(ctx, name, map[string]any{"tool": name})
			if hookErr != nil {
				return "", hookErr
			}
			if pending && id != "" {
				return "", &toolpkg.RequiresActionError{ActionID: id, ActionKind: name, ToolName: name}
			}
		}
		atomic.AddInt32(&runs, 1)
		return fn(ctx)
	})
	if err != nil {
		t.Fatalf("new tool %s: %v", name, err)
	}
	return toolObj, &runs
}

// streamHookRunner mounts a real action hook over st: the named tools are
// allowed by rule, everything else asks. It returns the action service the
// hook creates pending approvals in.
func streamHookRunner(t *testing.T, sid string, st *toolpkg.State, allow ...string) *state.ActionService {
	t.Helper()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	actions := &state.ActionService{DB: db}
	if err := state.NewSessionStore(db, "main").Ensure(ctx, sid, sid); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	permissionStore := safety.NewStore()
	for _, name := range allow {
		permissionStore.AddRule(safety.SourceSession, safety.BehaviorAllow, safety.PermissionRuleValue{
			ToolName: name, SessionID: sid,
		})
	}
	r := &Runner{Deps: &Deps{Actions: actions}, permRuntime: safety.NewRuntimeWithStore(permissionStore)}
	st.SetActionHook(r.actionHook)
	return actions
}

func streamCall(id, name string) llm.ToolCall {
	return llm.ToolCall{ID: id, Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: name, Arguments: "{}"}}
}

// A call dispatched while its response streams defers an ask-policy approval
// to the end of the stream: no one is asked and no pending action is created.
func TestStreamingDispatchDefersAskPolicyApprovalToStreamEnd(t *testing.T) {
	ctx := llm.WithAgentSessionID(context.Background(), "session-stream-defer")
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	defer db.Close()
	ensureRunSessions(t, db, "session-stream-defer")

	r := &Runner{Deps: &Deps{Actions: &state.ActionService{DB: db}}, permRuntime: safety.NewRuntimeWithStore(safety.NewStore())}
	id, pending, err := r.actionHook(toolpkg.WithStreamingDispatch(ctx), "shell", map[string]any{
		"command": "rm -rf tmp",
	})
	if !errors.Is(err, toolpkg.ErrDeferToStreamEnd) {
		t.Fatalf("actionHook err = %v, want ErrDeferToStreamEnd", err)
	}
	if pending || id != "" {
		t.Fatalf("deferred call must create nothing: id=%q pending=%v", id, pending)
	}
	pendingActions, listErr := r.Actions.List(ctx, "", state.ActionFilter{
		SessionID: "session-stream-defer",
		Status:    string(state.ActionPending),
	}, 10)
	if listErr != nil {
		t.Fatalf("list actions: %v", listErr)
	}
	if len(pendingActions) != 0 {
		t.Fatalf("pending actions = %d, want 0", len(pendingActions))
	}
}

// An allow rule still answers before the deferral point: a streaming call the
// policy already permits runs on without waiting for the stream to end.
func TestStreamingDispatchStillHonorsAllowRules(t *testing.T) {
	ctx := llm.WithAgentSessionID(context.Background(), "session-stream-allow")
	permissionStore := safety.NewStore()
	permissionStore.AddRule(safety.SourceSession, safety.BehaviorAllow, safety.PermissionRuleValue{
		ToolName: "Bash", RuleContent: "go test:*", SessionID: "session-stream-allow",
	})
	r := &Runner{Deps: &Deps{}, permRuntime: safety.NewRuntimeWithStore(permissionStore)}
	id, pending, err := r.actionHook(toolpkg.WithStreamingDispatch(ctx), "shell", map[string]any{
		"command": "go test ./...",
	})
	if err != nil {
		t.Fatalf("allow rule must pass through: %v", err)
	}
	if pending || id != "" {
		t.Fatalf("allowed call creates nothing: id=%q pending=%v", id, pending)
	}
}

// A tool call starts executing the moment the model has written it: the model
// is still blocked inside its own response when the tool runs, the exchange
// lands in order, and the call executes exactly once.
func TestToolStartsBeforeTheResponseEnds(t *testing.T) {
	ctx := context.Background()
	const sid = "streamtools-start-early"
	store, runID := newProgressStore(t, sid)
	st := toolpkg.NewState(t.TempDir())

	started := make(chan struct{})
	var startedOnce sync.Once
	var runs int32
	probe, err := llm.NewTool("probe_a", "records its start", func(context.Context, *struct{}) (string, error) {
		atomic.AddInt32(&runs, 1)
		startedOnce.Do(func() { close(started) })
		return "probe a ok", nil
	})
	if err != nil {
		t.Fatalf("probe tool: %v", err)
	}
	callA := streamCall("call-a", "probe_a")
	releaseModel := make(chan struct{})
	inner := &streamFailLLM{attempts: []func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error){
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			sink.OnDelta("running the probe")
			sink.OnTextBlockDone()
			sink.OnToolCallReady(callA)
			// The response stays open until the test lets it end; the tool
			// must already have run.
			select {
			case <-releaseModel:
			case <-ctx.Done():
			}
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("running the probe")}, callA)
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
	}}
	w := wrapToolOrchestrationLLMWithProgress(inner, st, "", store, func(context.Context) string { return "gpt-main" })
	execCtx := llm.WithStreamSink(llm.WithAgentSessionID(toolpkg.WithRunID(ctx, runID), sid), streamTestSink())
	go func() {
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			t.Error("the tool did not start before the response ended")
		}
		close(releaseModel)
	}()
	res, err := w.Execute(execCtx, []llm.Message{llm.UserMessage(llm.Text("run the probe"))}, []*llm.Tool{probe})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "done" {
		t.Fatalf("result = %+v, want the follow-up answer", res)
	}
	if runs != 1 {
		t.Fatalf("tool runs = %d, want exactly 1", runs)
	}
	second := inner.received[1]
	results := toolResultMessages(second)
	if len(results) != 1 || results[0].TextContent() != "probe a ok" || results[0].ToolCallID != "call-a" {
		t.Fatalf("second request tool results = %+v, want the one in-order answer", results)
	}
	assistantSeen := false
	for _, msg := range second {
		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) == 1 && msg.ToolCalls[0].ID == "call-a" {
			assistantSeen = true
		}
	}
	if !assistantSeen {
		t.Fatalf("second request = %+v, want the assistant call before its result", second)
	}
}

// A call that is not concurrency-safe blocks a concurrency-safe call readied
// behind it: the safe call starts only after the unsafe one ends.
func TestUnsafeCallBlocksTheOnesBehindIt(t *testing.T) {
	ctx := context.Background()
	const sid = "streamtools-unsafe-blocks"
	store, runID := newProgressStore(t, sid)
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_b", ReadOnly: true, ConcurrencySafe: true})

	aRunning := make(chan struct{})
	var aRunningOnce sync.Once
	releaseA := make(chan struct{})
	var aFinished int32
	var bStartedWhileARan int32
	var bDoneOnce sync.Once
	bDone := make(chan struct{})
	writeTool, err := llm.NewTool("write_a", "serial test tool", func(c context.Context, _ *struct{}) (string, error) {
		aRunningOnce.Do(func() { close(aRunning) })
		select {
		case <-releaseA:
		case <-c.Done():
			return "", c.Err()
		}
		atomic.StoreInt32(&aFinished, 1)
		return "a done", nil
	})
	if err != nil {
		t.Fatalf("write tool: %v", err)
	}
	readTool, err := llm.NewTool("read_b", "safe test tool", func(context.Context, *struct{}) (string, error) {
		if atomic.LoadInt32(&aFinished) == 0 {
			atomic.AddInt32(&bStartedWhileARan, 1)
		}
		bDoneOnce.Do(func() { close(bDone) })
		return "b done", nil
	})
	if err != nil {
		t.Fatalf("read tool: %v", err)
	}
	inner := &streamFailLLM{attempts: []func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error){
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			sink.OnToolCallReady(streamCall("call-a", "write_a"))
			<-aRunning
			// B is readied while the unsafe A still executes.
			sink.OnToolCallReady(streamCall("call-b", "read_b"))
			time.Sleep(150 * time.Millisecond)
			close(releaseA)
			select {
			case <-bDone:
			case <-time.After(10 * time.Second):
				return nil, fmt.Errorf("the safe call never started after the unsafe one ended")
			}
			msg := llm.AssistantMessage(nil, streamCall("call-a", "write_a"), streamCall("call-b", "read_b"))
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
	}}
	w := wrapToolOrchestrationLLMWithProgress(inner, st, "", store, func(context.Context) string { return "gpt-main" })
	execCtx := llm.WithStreamSink(llm.WithAgentSessionID(toolpkg.WithRunID(ctx, runID), sid), streamTestSink())
	if _, err := w.Execute(execCtx, []llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{writeTool, readTool}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := atomic.LoadInt32(&bStartedWhileARan); got != 0 {
		t.Fatalf("the safe call started %d times while the unsafe one still ran", got)
	}
	results := toolResultMessages(inner.received[1])
	if len(results) != 2 || results[0].TextContent() != "a done" || results[1].TextContent() != "b done" {
		t.Fatalf("tool results = %+v, want a then b in order", results)
	}
}

// Two concurrency-safe calls readied back to back run at the same time: each
// waits for the other to be running before it finishes.
func TestSafeCallsRunTogether(t *testing.T) {
	ctx := context.Background()
	const sid = "streamtools-safe-together"
	store, runID := newProgressStore(t, sid)
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_a", ReadOnly: true, ConcurrencySafe: true})
	st.RegisterToolMeta(event.ToolMeta{Name: "read_b", ReadOnly: true, ConcurrencySafe: true})

	var running int32
	both := make(chan struct{})
	var bothOnce sync.Once
	newRead := func(name string) *llm.Tool {
		toolObj, err := llm.NewTool(name, "safe test tool", func(c context.Context, _ *struct{}) (string, error) {
			if atomic.AddInt32(&running, 1) == 2 {
				bothOnce.Do(func() { close(both) })
			}
			select {
			case <-both:
			case <-time.After(10 * time.Second):
				return "", fmt.Errorf("%s ran alone", name)
			}
			return name + " done", nil
		})
		if err != nil {
			t.Fatalf("new tool %s: %v", name, err)
		}
		return toolObj
	}
	inner := &streamFailLLM{attempts: []func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error){
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			sink.OnToolCallReady(streamCall("call-a", "read_a"))
			sink.OnToolCallReady(streamCall("call-b", "read_b"))
			msg := llm.AssistantMessage(nil, streamCall("call-a", "read_a"), streamCall("call-b", "read_b"))
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
	}}
	w := wrapToolOrchestrationLLMWithProgress(inner, st, "", store, func(context.Context) string { return "gpt-main" })
	execCtx := llm.WithStreamSink(llm.WithAgentSessionID(toolpkg.WithRunID(ctx, runID), sid), streamTestSink())
	if _, err := w.Execute(execCtx, []llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{newRead("read_a"), newRead("read_b")}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := atomic.LoadInt32(&running); got != 2 {
		t.Fatalf("concurrent executions = %d, want 2", got)
	}
	results := toolResultMessages(inner.received[1])
	if len(results) != 2 || results[0].TextContent() != "read_a done" || results[1].TextContent() != "read_b done" {
		t.Fatalf("tool results = %+v", results)
	}
}

// A call that would ask for approval defers in-stream: nothing is created, the
// call behind it never starts, and once the stream ends the ordinary path asks
// and the turn stops on that approval. Calls ahead of it keep their results.
func TestGatedCallIsDeferredToStreamEnd(t *testing.T) {
	ctx := context.Background()
	const sid = "streamtools-gated-defer"
	store, runID := newProgressStore(t, sid)
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "probe_allowed", ReadOnly: true, ConcurrencySafe: true})
	actions := streamHookRunner(t, sid, st, "probe_allowed")

	allowedTool, allowedRuns := streamHookTool(t, st, "probe_allowed", func(context.Context) (string, error) {
		return "allowed ok", nil
	})
	askTool, askRuns := streamHookTool(t, st, "probe_ask", func(context.Context) (string, error) {
		return "ask ok", nil
	})
	behindTool, behindRuns := streamHookTool(t, st, "probe_behind", func(context.Context) (string, error) {
		return "behind ok", nil
	})
	inner := &streamFailLLM{attempts: []func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error){
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			sink.OnToolCallReady(streamCall("call-allowed", "probe_allowed"))
			time.Sleep(50 * time.Millisecond)
			sink.OnToolCallReady(streamCall("call-ask", "probe_ask"))
			time.Sleep(50 * time.Millisecond)
			sink.OnToolCallReady(streamCall("call-behind", "probe_behind"))
			msg := llm.AssistantMessage(nil,
				streamCall("call-allowed", "probe_allowed"),
				streamCall("call-ask", "probe_ask"),
				streamCall("call-behind", "probe_behind"))
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
	}}
	w := wrapToolOrchestrationLLMWithProgress(inner, st, "", store, func(context.Context) string { return "gpt-main" })
	execCtx := llm.WithStreamSink(llm.WithAgentSessionID(toolpkg.WithRunID(ctx, runID), sid), streamTestSink())
	_, err := w.Execute(execCtx, []llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{allowedTool, askTool, behindTool})
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) {
		t.Fatalf("turn error = %v, want the approval gate", err)
	}
	if atomic.LoadInt32(allowedRuns) != 1 {
		t.Fatalf("allowed runs = %d, want 1", *allowedRuns)
	}
	if atomic.LoadInt32(askRuns) != 0 {
		t.Fatalf("the gated call ran its work %d times, want 0 (deferred in-stream, stopped on approval)", *askRuns)
	}
	if atomic.LoadInt32(behindRuns) != 0 {
		t.Fatalf("the call behind the gate ran %d times, want 0", *behindRuns)
	}
	pending, listErr := actions.List(ctx, "", state.ActionFilter{SessionID: sid, Status: string(state.ActionPending)}, 10)
	if listErr != nil {
		t.Fatalf("list actions: %v", listErr)
	}
	if len(pending) != 1 || pending[0].Kind != "probe_ask" {
		t.Fatalf("pending actions = %+v, want exactly the post-stream gate", pending)
	}
	var sawAllowedResult, sawBehindResult bool
	for _, msg := range rae.SessionSnapshot {
		if msg.Role == llm.RoleTool && msg.ToolCallID == "call-allowed" && msg.TextContent() == "allowed ok" {
			sawAllowedResult = true
		}
		if msg.Role == llm.RoleTool && msg.ToolCallID == "call-behind" {
			sawBehindResult = true
		}
	}
	if !sawAllowedResult {
		t.Fatalf("snapshot = %+v, want the call ahead of the gate answered", rae.SessionSnapshot)
	}
	if sawBehindResult {
		t.Fatalf("snapshot = %+v, the call behind the gate must have no result", rae.SessionSnapshot)
	}
}

// A concurrency-safe gated call readied beside a concurrency-safe allowed one
// defers while its neighbour finishes; after the stream only the deferred call
// runs again — the finished one is never re-executed.
func TestDeferredSafeCallDoesNotRerunItsFinishedNeighbour(t *testing.T) {
	ctx := context.Background()
	const sid = "streamtools-defer-no-rerun"
	store, runID := newProgressStore(t, sid)
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "gated_read", ReadOnly: true, ConcurrencySafe: true})
	st.RegisterToolMeta(event.ToolMeta{Name: "free_read", ReadOnly: true, ConcurrencySafe: true})
	streamHookRunner(t, sid, st, "free_read")

	gatedTool, gatedRuns := streamHookTool(t, st, "gated_read", func(context.Context) (string, error) {
		return "gated ok", nil
	})
	freeTool, freeRuns := streamHookTool(t, st, "free_read", func(context.Context) (string, error) {
		return "free ok", nil
	})
	inner := &streamFailLLM{attempts: []func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error){
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			sink.OnToolCallReady(streamCall("call-gated", "gated_read"))
			sink.OnToolCallReady(streamCall("call-free", "free_read"))
			msg := llm.AssistantMessage(nil, streamCall("call-gated", "gated_read"), streamCall("call-free", "free_read"))
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
	}}
	w := wrapToolOrchestrationLLMWithProgress(inner, st, "", store, func(context.Context) string { return "gpt-main" })
	execCtx := llm.WithStreamSink(llm.WithAgentSessionID(toolpkg.WithRunID(ctx, runID), sid), streamTestSink())
	_, err := w.Execute(execCtx, []llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{gatedTool, freeTool})
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) {
		t.Fatalf("turn error = %v, want the approval gate", err)
	}
	if atomic.LoadInt32(freeRuns) != 1 {
		t.Fatalf("free runs = %d, want exactly 1 (a finished call is never re-run)", *freeRuns)
	}
	if atomic.LoadInt32(gatedRuns) != 0 {
		t.Fatalf("gated runs = %d, want 0 (deferred in-stream, stopped on approval)", *gatedRuns)
	}
	var sawFreeResult bool
	for _, msg := range rae.SessionSnapshot {
		if msg.Role == llm.RoleTool && msg.ToolCallID == "call-free" && msg.TextContent() == "free ok" {
			sawFreeResult = true
		}
	}
	if !sawFreeResult {
		t.Fatalf("snapshot = %+v, want the finished neighbour's result", rae.SessionSnapshot)
	}
}

// A steer queued while a call executes stops every later ready call: they are
// answered "not run" and the steer rides on the next request.
func TestQueuedSteerStopsFurtherStarts(t *testing.T) {
	ctx := context.Background()
	const sid = "streamtools-steer-stops"
	store, runID := newProgressStore(t, sid)
	st := toolpkg.NewState(t.TempDir())
	rt := NewTurnInputRuntime()

	var runs int32
	toolObj, err := llm.NewTool("write_note", "serial test tool", func(context.Context, *struct{}) (string, error) {
		atomic.AddInt32(&runs, 1)
		rt.Enqueue(TurnInputModeSteer, []llm.ContentPart{llm.Text("stop and say hi")})
		return "wrote", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	inner := &streamFailLLM{attempts: []func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error){
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			sink.OnToolCallReady(streamCall("call-a", "write_note"))
			time.Sleep(100 * time.Millisecond) // the call runs and queues the steer
			sink.OnToolCallReady(streamCall("call-b", "write_note"))
			sink.OnToolCallReady(streamCall("call-c", "write_note"))
			msg := llm.AssistantMessage(nil,
				streamCall("call-a", "write_note"), streamCall("call-b", "write_note"), streamCall("call-c", "write_note"))
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("steer received")})
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
	}}
	w := wrapToolOrchestrationLLMWithProgress(inner, st, "", store, func(context.Context) string { return "gpt-main" })
	execCtx := llm.WithStreamSink(
		WithTurnInputRuntime(llm.WithAgentSessionID(toolpkg.WithRunID(ctx, runID), sid), rt),
		streamTestSink(),
	)
	res, err := w.Execute(execCtx, []llm.Message{llm.UserMessage(llm.Text("run the notes"))}, []*llm.Tool{toolObj})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "steer received" {
		t.Fatalf("result = %+v", res)
	}
	if runs != 1 {
		t.Fatalf("tool runs = %d, want 1 (the steer takes the turn at the next start)", runs)
	}
	second := inner.received[1]
	results := toolResultMessages(second)
	if len(results) != 3 {
		t.Fatalf("tool results = %d, want 3: %+v", len(results), second)
	}
	if results[0].TextContent() != "wrote" {
		t.Fatalf("first result = %q, want the call that ran", results[0].TextContent())
	}
	for i, result := range results[1:] {
		if result.TextContent() != steerPreemptedResultText {
			t.Fatalf("result %d = %q, want the not-run answer", i+2, result.TextContent())
		}
	}
	last := second[len(second)-1]
	if last.Role != llm.RoleUser || last.TextContent() != "stop and say hi" {
		t.Fatalf("last message = role %q content %q, want the steer", last.Role, last.TextContent())
	}
}

// A network failure after a tool started keeps the executed exchange and never
// re-sends the request: the response is finished from what completed, the
// tool's result is in the conversation, and the turn continues.
func TestStreamFailureAfterDispatchKeepsPartialAndNeverResends(t *testing.T) {
	ctx := context.Background()
	const sid = "streamtools-fail-after-dispatch"
	store, runID := newProgressStore(t, sid)
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "probe_dispatch"})

	aStarted := make(chan struct{})
	var startedOnce sync.Once
	var runs int32
	probe, err := llm.NewTool("probe_dispatch", "records its start", func(context.Context, *struct{}) (string, error) {
		atomic.AddInt32(&runs, 1)
		startedOnce.Do(func() { close(aStarted) })
		return "probe ok", nil
	})
	if err != nil {
		t.Fatalf("probe tool: %v", err)
	}
	callA := streamCall("call-dispatch", "probe_dispatch")
	var notices []llm.ResponseNotice
	base := streamTestSink()
	base.OnResponseNotice = func(n llm.ResponseNotice) { notices = append(notices, n) }
	inner := &streamFailLLM{attempts: []func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error){
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			sink.OnDelta("running the probe")
			sink.OnTextBlockDone()
			sink.OnToolCallReady(callA)
			<-aStarted // the dispatch happened before the failure
			return nil, connDropErr()
		},
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("after the failure")})
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
	}}
	w := wrapToolOrchestrationLLMWithProgress(wrapNetworkRetryLLM(inner, 3, 0), st, "", store, func(context.Context) string { return "gpt-main" })
	execCtx := llm.WithStreamSink(llm.WithAgentSessionID(toolpkg.WithRunID(ctx, runID), sid), base)
	res, err := w.Execute(execCtx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, []*llm.Tool{probe})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "after the failure" {
		t.Fatalf("result = %+v, want the follow-up answer", res)
	}
	if inner.calls != 2 {
		t.Fatalf("model calls = %d, want 2 (the failed request was never re-sent, only the follow-up ran)", inner.calls)
	}
	if runs != 1 {
		t.Fatalf("tool runs = %d, want 1", runs)
	}
	second := inner.received[1]
	results := toolResultMessages(second)
	if len(results) != 1 || results[0].ToolCallID != "call-dispatch" || results[0].TextContent() != "probe ok" {
		t.Fatalf("second request results = %+v, want the dispatched call's result in the conversation", results)
	}
	if len(notices) != 1 || notices[0].Kind != llm.ResponseNoticeIncomplete {
		t.Fatalf("notices = %+v, want one incomplete notice", notices)
	}
}

// Cancelling while a streamed tool runs returns the cancellation with the
// exchange intact: the assistant message carries the call, and the call's
// interrupted result is written behind it.
func TestCancelWhileToolsRunWritesInterruptedResults(t *testing.T) {
	const sid = "streamtools-cancel-mid-tool"
	store, runID := newProgressStore(t, sid)
	st := toolpkg.NewState(t.TempDir())

	aStarted := make(chan struct{})
	var startedOnce sync.Once
	probe, err := llm.NewTool("probe_cancel", "blocks until cancelled", func(c context.Context, _ *struct{}) (string, error) {
		startedOnce.Do(func() { close(aStarted) })
		<-c.Done()
		return "", c.Err()
	})
	if err != nil {
		t.Fatalf("probe tool: %v", err)
	}
	callA := streamCall("call-cancel", "probe_cancel")
	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inner := &streamFailLLM{attempts: []func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error){
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			sink.OnDelta("working")
			sink.OnTextBlockDone()
			sink.OnToolCallReady(callA)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}}
	w := wrapToolOrchestrationLLMWithProgress(inner, st, "", store, func(context.Context) string { return "gpt-main" })
	execCtx := llm.WithStreamSink(llm.WithAgentSessionID(toolpkg.WithRunID(baseCtx, runID), sid), streamTestSink())
	go func() {
		<-aStarted
		cancel()
	}()
	res, err := w.Execute(execCtx, []llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{probe})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("turn error = %v, want the cancellation", err)
	}
	if res != nil {
		t.Fatalf("result = %+v, want nil", res)
	}
	got, listErr := store.ListTranscriptMessages(context.Background(), sid, 100)
	if listErr != nil {
		t.Fatalf("list rows: %v", listErr)
	}
	var assistantRow, toolRow int
	for _, msg := range got {
		switch msg.Role {
		case llm.RoleAssistant:
			assistantRow++
			if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call-cancel" {
				t.Fatalf("assistant row = %+v, want the cancelled call on it", msg)
			}
		case llm.RoleTool:
			toolRow++
		}
	}
	if assistantRow != 1 || toolRow != 1 {
		t.Fatalf("rows = %+v, want one assistant row and one interrupted result", got)
	}
}

// A provider that never reports a call ready - the compat stream synthesizing
// calls from its final frame - has its calls executed after the stream, the
// way they always were.
func TestCallsNotSeenAsReadyRunAfterTheStream(t *testing.T) {
	ctx := context.Background()
	const sid = "streamtools-never-ready"
	store, runID := newProgressStore(t, sid)
	st := toolpkg.NewState(t.TempDir())

	var runs int32
	probe, err := llm.NewTool("probe_late", "runs after the stream", func(context.Context, *struct{}) (string, error) {
		atomic.AddInt32(&runs, 1)
		return "late ok", nil
	})
	if err != nil {
		t.Fatalf("probe tool: %v", err)
	}
	callA := streamCall("call-late", "probe_late")
	inner := &streamFailLLM{attempts: []func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error){
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			// The call appears only in the final result; OnToolCallReady never fires.
			msg := llm.AssistantMessage(nil, callA)
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
	}}
	w := wrapToolOrchestrationLLMWithProgress(inner, st, "", store, func(context.Context) string { return "gpt-main" })
	execCtx := llm.WithStreamSink(llm.WithAgentSessionID(toolpkg.WithRunID(ctx, runID), sid), streamTestSink())
	res, err := w.Execute(execCtx, []llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{probe})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "done" {
		t.Fatalf("result = %+v", res)
	}
	if runs != 1 {
		t.Fatalf("tool runs = %d, want 1", runs)
	}
	results := toolResultMessages(inner.received[1])
	if len(results) != 1 || results[0].TextContent() != "late ok" {
		t.Fatalf("tool results = %+v", results)
	}
}

// The provisional assistant row written before each in-stream call is
// completed in place as the response grows: one assistant row carries both
// calls, the results sit behind it, and nothing is duplicated.
func TestProvisionalAssistantRowIsCompletedNotDuplicated(t *testing.T) {
	ctx := context.Background()
	const sid = "streamtools-prov-row"
	store, runID := newProgressStore(t, sid)
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_a", ReadOnly: true, ConcurrencySafe: true})
	st.RegisterToolMeta(event.ToolMeta{Name: "read_b", ReadOnly: true, ConcurrencySafe: true})

	newRead := func(name string) *llm.Tool {
		toolObj, err := llm.NewTool(name, "safe test tool", func(context.Context, *struct{}) (string, error) {
			return name + " done", nil
		})
		if err != nil {
			t.Fatalf("new tool %s: %v", name, err)
		}
		return toolObj
	}
	inner := &streamFailLLM{attempts: []func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error){
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			sink.OnDelta("working")
			sink.OnTextBlockDone()
			sink.OnToolCallReady(streamCall("call-a", "read_a"))
			sink.OnToolCallReady(streamCall("call-b", "read_b"))
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("working")},
				streamCall("call-a", "read_a"), streamCall("call-b", "read_b"))
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
	}}
	w := wrapToolOrchestrationLLMWithProgress(inner, st, "", store, func(context.Context) string { return "gpt-main" })
	execCtx := llm.WithStreamSink(llm.WithAgentSessionID(toolpkg.WithRunID(ctx, runID), sid), streamTestSink())
	if _, err := w.Execute(execCtx, []llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{newRead("read_a"), newRead("read_b")}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got, err := store.ListTranscriptMessages(context.Background(), sid, 100)
	if err != nil {
		t.Fatalf("list rows: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("rows = %d, want 5 (user, one assistant, two results, final answer): %+v", len(got), got)
	}
	if got[1].Role != llm.RoleAssistant || got[1].TextContent() != "working" || len(got[1].ToolCalls) != 2 ||
		got[1].ToolCalls[0].ID != "call-a" || got[1].ToolCalls[1].ID != "call-b" {
		t.Fatalf("assistant row = %+v, want one row with both calls", got[1])
	}
	if got[2].Role != llm.RoleTool || got[2].ToolCallID != "call-a" || got[2].TextContent() != "read_a done" {
		t.Fatalf("first result row = %+v", got[2])
	}
	if got[3].Role != llm.RoleTool || got[3].ToolCallID != "call-b" || got[3].TextContent() != "read_b done" {
		t.Fatalf("second result row = %+v", got[3])
	}
	if got[4].Role != llm.RoleAssistant || got[4].TextContent() != "done" {
		t.Fatalf("final row = %+v", got[4])
	}
}
