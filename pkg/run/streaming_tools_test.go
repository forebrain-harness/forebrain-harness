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

// ---- plan 127: streaming dispatch with an in-place approval hook ----

// A streamed call whose surface answers the primary agent's gates in place
// asks now instead of deferring: the pending action exists the moment the
// gate fires.
func TestStreamingDispatchWithInlineHookAsksInPlace(t *testing.T) {
	ctx := llm.WithAgentSessionID(context.Background(), "session-stream-inline")
	db, err := state.OpenStateForTest(ctx, t.TempDir()+"/state.sqlite")
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	defer db.Close()
	ensureRunSessions(t, db, "session-stream-inline")

	r := &Runner{Deps: &Deps{Actions: &state.ActionService{DB: db}}, permRuntime: safety.NewRuntimeWithStore(safety.NewStore())}
	hooked := toolpkg.WithInlineApprovalHook(toolpkg.WithStreamingDispatch(ctx), func(context.Context, *toolpkg.RequiresActionError) (context.Context, error) {
		t.Fatal("the hook is the orchestration's to call, not the action hook's")
		return nil, nil
	})
	id, pending, err := r.actionHook(hooked, "shell", map[string]any{"command": "echo hi"})
	if err != nil {
		t.Fatalf("actionHook err = %v, want a pending approval", err)
	}
	if !pending || id == "" {
		t.Fatalf("actionHook id=%q pending=%v, want the in-place approval", id, pending)
	}
}

// exit_plan_mode keeps deferring even with an in-place hook: its approval
// carries options only the unwind path can honor.
func TestStreamingDispatchWithInlineHookStillDefersExitPlanMode(t *testing.T) {
	ctx := llm.WithAgentSessionID(toolpkg.WithStreamingDispatch(context.Background()), "session-stream-epm-defer")
	hooked := toolpkg.WithInlineApprovalHook(ctx, func(context.Context, *toolpkg.RequiresActionError) (context.Context, error) {
		return nil, nil
	})
	r := &Runner{Deps: &Deps{}, permRuntime: safety.NewRuntimeWithStore(safety.NewStore())}
	_, pending, err := r.actionHook(hooked, "exit_plan_mode", map[string]any{})
	if !errors.Is(err, toolpkg.ErrDeferToStreamEnd) {
		t.Fatalf("actionHook err = %v, want ErrDeferToStreamEnd", err)
	}
	if pending {
		t.Fatal("a deferred exit_plan_mode must create nothing")
	}
}

// Two gates of one run serialize on the run's approval slot: the second
// actionHook blocks until the first call released the slot.
func TestInPlaceSlotSerializesTwoGatesOfOneRun(t *testing.T) {
	ctx := llm.WithAgentSessionID(context.Background(), "session-stream-slot")
	db, err := state.OpenStateForTest(ctx, t.TempDir()+"/state.sqlite")
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	defer db.Close()
	ensureRunSessions(t, db, "session-stream-slot")

	r := &Runner{Deps: &Deps{Actions: &state.ActionService{DB: db}}, permRuntime: safety.NewRuntimeWithStore(safety.NewStore())}
	// Each call carries its own holder for the same run, the way
	// runCallInPlace installs one per call.
	firstHolder := &toolpkg.ApprovalSlotHolder{RunID: "run-slot-hook"}
	secondHolder := &toolpkg.ApprovalSlotHolder{RunID: "run-slot-hook"}
	runCtx := toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), "session-stream-slot"), "run-slot-hook")
	first, pending, err := r.actionHook(toolpkg.WithApprovalSlotHolder(runCtx, firstHolder), "shell", map[string]any{"command": "echo one"})
	if err != nil || !pending || first == "" {
		t.Fatalf("first gate: id=%q pending=%v err=%v", first, pending, err)
	}
	secondDone := make(chan string, 1)
	go func() {
		id, p, gerr := r.actionHook(toolpkg.WithApprovalSlotHolder(runCtx, secondHolder), "shell", map[string]any{"command": "echo two"})
		if gerr != nil || !p {
			t.Errorf("second gate: pending=%v err=%v", p, gerr)
		}
		secondDone <- id
	}()
	select {
	case id := <-secondDone:
		t.Fatalf("second gate created action %q while the first held the slot", id)
	case <-time.After(150 * time.Millisecond):
	}
	firstHolder.Release()
	select {
	case id := <-secondDone:
		if id == "" {
			t.Fatal("second gate created no action after the slot was released")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second gate never asked after the slot was released")
	}
	secondHolder.Release()
}

// ---- plan 127: gates asked in place while the stream continues ----

// inlineGateHook is a controllable in-place approval hook: every gate is
// reported on gates and blocks until the test answers it.
type inlineGate struct {
	rae    *toolpkg.RequiresActionError
	answer chan string
}

func inlineGateHook() (toolpkg.InlineApprovalHook, chan inlineGate) {
	gates := make(chan inlineGate, 4)
	return func(ctx context.Context, rae *toolpkg.RequiresActionError) (context.Context, error) {
		g := inlineGate{rae: rae, answer: make(chan string, 1)}
		gates <- g
		switch <-g.answer {
		case "approve":
			return toolpkg.WithApprovedActionID(ctx, rae.ActionID), nil
		case "deny":
			return toolpkg.WithToolApprovalResume(ctx, &toolpkg.ToolApprovalResumeState{
				Denied: true, DenyReason: "not now", DenyFeedback: "not now",
			}), nil
		case "refuse":
			return nil, toolpkg.ErrApprovalRejectedStop
		}
		return nil, fmt.Errorf("unknown gate answer")
	}, gates
}

func waitInlineGate(t *testing.T, gates chan inlineGate, what string) inlineGate {
	t.Helper()
	select {
	case g := <-gates:
		return g
	case <-time.After(5 * time.Second):
		t.Fatalf("the %s gate never reached the hook", what)
		return inlineGate{}
	}
}

func answerInlineGate(g inlineGate, answer string) {
	g.answer <- answer
}

// A refusal without words on a streamed call cancels the stream itself: the
// model call dies with the refusal as its cause, the refused call reads the
// stop sentence, and the calls queued behind it never start.
func TestInPlaceRejectionWithoutWordsStopsTheStream(t *testing.T) {
	const sid = "streamtools-refuse-stop"
	store, runID := newProgressStore(t, sid)
	st := toolpkg.NewState(t.TempDir())
	streamHookRunner(t, sid, st)
	hook, gates := inlineGateHook()

	var aRuns int32
	gated, err := llm.NewTool("gated_a", "asks in place", func(ctx context.Context, _ *struct{}) (string, error) {
		if hookFn := st.ActionHook(); hookFn != nil {
			id, pending, hookErr := hookFn(ctx, "gated_a", map[string]any{"tool": "gated_a"})
			if hookErr != nil {
				return "", hookErr
			}
			if pending && id != "" {
				return "", &toolpkg.RequiresActionError{ActionID: id, ActionKind: "gated_a", ToolName: "gated_a"}
			}
		}
		atomic.AddInt32(&aRuns, 1)
		return "a ok", nil
	})
	if err != nil {
		t.Fatalf("gated tool: %v", err)
	}
	var cRuns int32
	queued, err := llm.NewTool("queued_c", "waits behind the unsafe call", func(context.Context, *struct{}) (string, error) {
		atomic.AddInt32(&cRuns, 1)
		return "c ok", nil
	})
	if err != nil {
		t.Fatalf("queued tool: %v", err)
	}

	inner := &streamFailLLM{attempts: []func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error){
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			sink.OnDelta("checking")
			sink.OnTextBlockDone()
			sink.OnToolCallReady(streamCall("call-a", "gated_a"))
			g := waitInlineGate(t, gates, "streamed gated")
			// The stream keeps writing while the question is open, and the
			// queued call is readied behind the unsafe gated one.
			sink.OnDelta(" still going")
			sink.OnToolCallReady(streamCall("call-c", "queued_c"))
			time.Sleep(150 * time.Millisecond)
			if got := atomic.LoadInt32(&cRuns); got != 0 {
				t.Errorf("queued call ran %d times behind the unsafe gated call", got)
			}
			answerInlineGate(g, "refuse")
			// The refusal cancels this very stream.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(10 * time.Second):
				return nil, fmt.Errorf("the refusal never cancelled the stream")
			}
		},
	}}
	w := wrapToolOrchestrationLLMWithProgress(inner, st, "", store, func(context.Context) string { return "gpt-main" })
	capture := NewPartialSessionCapture()
	base := toolpkg.WithInlineApprovalHook(llm.WithAgentSessionID(toolpkg.WithRunID(context.Background(), runID), sid), hook)
	execCtx := WithPartialSessionCapture(llm.WithStreamSink(base, streamTestSink()), capture)
	_, err = w.Execute(execCtx, []llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{gated, queued})
	if !errors.Is(err, ErrTurnStoppedByRejection) {
		t.Fatalf("execute err = %v, want ErrTurnStoppedByRejection", err)
	}
	if inner.calls != 1 {
		t.Fatalf("model calls = %d, want 1", inner.calls)
	}
	if got := atomic.LoadInt32(&cRuns); got != 0 {
		t.Fatalf("queued call ran %d times, want 0", got)
	}
	snap := capture.Snapshot()
	refused, notRun := 0, 0
	for _, msg := range snap {
		if msg.Role != llm.RoleTool {
			continue
		}
		switch {
		case msg.TextContent() == toolpkg.RejectedStopText && msg.ToolDisplay != nil && msg.ToolDisplay.Body == toolpkg.RejectionStoppedDisplayKey:
			notRun++
		case msg.TextContent() == toolpkg.RejectedStopText:
			refused++
		}
	}
	if refused != 1 || notRun != 1 {
		t.Fatalf("stopped results: refused=%d notRun=%d, want 1 and 1", refused, notRun)
	}
}

// A refusal without words can land after the response body already ended:
// the streamed call was still waiting on its approval when the stream
// finished, so the stream-end refusal read had already passed with
// refused=false. The settlement after the stream is the last place that can
// report the refusal: the turn still ends on it, nothing behind the refused
// call starts, and the model is never asked again.
func TestRejectionAfterTheStreamEndsStillStopsTheTurn(t *testing.T) {
	const sid = "streamtools-refuse-after-stream-end"
	store, runID := newProgressStore(t, sid)
	st := toolpkg.NewState(t.TempDir())
	streamHookRunner(t, sid, st)
	hook, gates := inlineGateHook()

	var aRuns int32
	gated, err := llm.NewTool("gated_a", "asks in place", func(ctx context.Context, _ *struct{}) (string, error) {
		if hookFn := st.ActionHook(); hookFn != nil {
			id, pending, hookErr := hookFn(ctx, "gated_a", map[string]any{"tool": "gated_a"})
			if hookErr != nil {
				return "", hookErr
			}
			if pending && id != "" {
				return "", &toolpkg.RequiresActionError{ActionID: id, ActionKind: "gated_a", ToolName: "gated_a"}
			}
		}
		atomic.AddInt32(&aRuns, 1)
		return "a ok", nil
	})
	if err != nil {
		t.Fatalf("gated tool: %v", err)
	}
	var bRuns int32
	queued, err := llm.NewTool("queued_b", "waits behind the unsafe call", func(context.Context, *struct{}) (string, error) {
		atomic.AddInt32(&bRuns, 1)
		return "b ok", nil
	})
	if err != nil {
		t.Fatalf("queued tool: %v", err)
	}

	// The response ends with the gated call still waiting on its approval;
	// streamDone tells the test body the stream is over.
	streamDone := make(chan struct{})
	inner := &streamFailLLM{attempts: []func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error){
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			sink.OnDelta("checking")
			sink.OnTextBlockDone()
			sink.OnToolCallReady(streamCall("call-a", "gated_a"))
			// The stream keeps writing while the question is open, and the
			// queued call is readied behind the unsafe gated one.
			sink.OnDelta(" still going")
			sink.OnToolCallReady(streamCall("call-b", "queued_b"))
			close(streamDone)
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("checking still going")},
				streamCall("call-a", "gated_a"), streamCall("call-b", "queued_b"))
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
	}}
	w := wrapToolOrchestrationLLMWithProgress(inner, st, "", store, func(context.Context) string { return "gpt-main" })
	capture := NewPartialSessionCapture()
	base := toolpkg.WithInlineApprovalHook(llm.WithAgentSessionID(toolpkg.WithRunID(context.Background(), runID), sid), hook)
	execCtx := WithPartialSessionCapture(llm.WithStreamSink(base, streamTestSink()), capture)
	execDone := make(chan error, 1)
	go func() {
		_, err := w.Execute(execCtx, []llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{gated, queued})
		execDone <- err
	}()
	// The gated call started and is waiting on its approval.
	g := waitInlineGate(t, gates, "streamed gated")
	<-streamDone
	// The refusal lands only after the stream ended and the loop walked past
	// the stream-end refusal read — the user thought about it for a few
	// seconds.
	time.Sleep(100 * time.Millisecond)
	answerInlineGate(g, "refuse")
	err = <-execDone
	if !errors.Is(err, ErrTurnStoppedByRejection) {
		t.Fatalf("execute err = %v, want ErrTurnStoppedByRejection", err)
	}
	if inner.calls != 1 {
		t.Fatalf("model calls = %d, want 1", inner.calls)
	}
	if got := atomic.LoadInt32(&bRuns); got != 0 {
		t.Fatalf("queued call ran %d times, want 0", got)
	}
	if got := atomic.LoadInt32(&aRuns); got != 0 {
		t.Fatalf("refused call ran %d times, want 0", got)
	}
	snap := capture.Snapshot()
	refused, notRun := 0, 0
	for _, msg := range snap {
		if msg.Role != llm.RoleTool {
			continue
		}
		switch {
		case msg.TextContent() == toolpkg.RejectedStopText && msg.ToolDisplay != nil && msg.ToolDisplay.Body == toolpkg.RejectionStoppedDisplayKey:
			notRun++
		case msg.TextContent() == toolpkg.RejectedStopText:
			refused++
		}
	}
	if refused != 1 || notRun != 1 {
		t.Fatalf("stopped results: refused=%d notRun=%d, want 1 and 1", refused, notRun)
	}
}

// A refusal without words can land inside the cancel settlement window: the
// model call already returned its cancellation while the streamed call was
// still waiting on its approval, so the refusal read that decided to settle
// had already passed with refused=false. abort joins the in-flight call —
// that wait is exactly where the refusal lands — so the settlement reads the
// marker again after the join and the turn still ends on the refusal, not as
// an ordinary cancellation.
func TestRefusalRecordedWhileAbortJoinsStillStopsTheTurn(t *testing.T) {
	const sid = "streamtools-refuse-in-abort-join"
	store, runID := newProgressStore(t, sid)
	st := toolpkg.NewState(t.TempDir())
	streamHookRunner(t, sid, st)
	hook, gates := inlineGateHook()

	gated, aRuns := streamHookTool(t, st, "gated_a", func(context.Context) (string, error) {
		return "a ok", nil
	})

	streamReturned := make(chan struct{})
	var g inlineGate
	inner := &streamFailLLM{attempts: []func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error){
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			sink.OnDelta("checking")
			sink.OnTextBlockDone()
			sink.OnToolCallReady(streamCall("call-a", "gated_a"))
			// The streamed call started and is waiting on its approval.
			g = waitInlineGate(t, gates, "streamed gated")
			close(streamReturned)
			// The stream dies as a plain cancellation while the gate is still
			// open — the refusal has not landed yet.
			return nil, context.Canceled
		},
	}}
	w := wrapToolOrchestrationLLMWithProgress(inner, st, "", store, func(context.Context) string { return "gpt-main" })
	capture := NewPartialSessionCapture()
	base := toolpkg.WithInlineApprovalHook(llm.WithAgentSessionID(toolpkg.WithRunID(context.Background(), runID), sid), hook)
	execCtx := WithPartialSessionCapture(llm.WithStreamSink(base, streamTestSink()), capture)
	// Let the settling loop walk past its first refusal read (the buggy one)
	// before the refusal lands: it arrives while abort is joining the call.
	go func() {
		<-streamReturned
		time.Sleep(300 * time.Millisecond)
		answerInlineGate(g, "refuse")
	}()
	_, err := w.Execute(execCtx, []llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{gated})
	if !errors.Is(err, ErrTurnStoppedByRejection) {
		t.Fatalf("execute err = %v, want ErrTurnStoppedByRejection", err)
	}
	if inner.calls != 1 {
		t.Fatalf("model calls = %d, want 1", inner.calls)
	}
	if got := atomic.LoadInt32(aRuns); got != 0 {
		t.Fatalf("refused call ran %d times, want 0", got)
	}
}

// A gate asked in place does not stop the stream: the model keeps writing,
// and a concurrency-safe sibling readied behind the gated call starts only
// once the gated call is answered — an unsafe gated call blocks the queue.
func TestInPlaceGateIsAskedWhileTheStreamContinues(t *testing.T) {
	const sid = "streamtools-gate-streams"
	store, runID := newProgressStore(t, sid)
	st := toolpkg.NewState(t.TempDir())
	streamHookRunner(t, sid, st, "read_b")
	st.RegisterToolMeta(event.ToolMeta{Name: "read_b", ReadOnly: true, ConcurrencySafe: true})
	hook, gates := inlineGateHook()

	gated, aRuns := streamHookTool(t, st, "write_a", func(context.Context) (string, error) { return "a ok", nil })
	var bRuns int32
	bDone := make(chan struct{})
	var bOnce sync.Once
	safe, err := llm.NewTool("read_b", "safe sibling", func(context.Context, *struct{}) (string, error) {
		atomic.AddInt32(&bRuns, 1)
		bOnce.Do(func() { close(bDone) })
		return "b ok", nil
	})
	if err != nil {
		t.Fatalf("safe tool: %v", err)
	}

	inner := &streamFailLLM{attempts: []func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error){
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			sink.OnDelta("checking")
			sink.OnTextBlockDone()
			sink.OnToolCallReady(streamCall("call-a", "write_a"))
			g := waitInlineGate(t, gates, "streamed gated")
			// The stream keeps going while the gate is open.
			sink.OnDelta(" still going")
			sink.OnToolCallReady(streamCall("call-b", "read_b"))
			time.Sleep(200 * time.Millisecond)
			if got := atomic.LoadInt32(&bRuns); got != 0 {
				t.Errorf("the safe sibling started %d times while the unsafe gated call waited", got)
			}
			answerInlineGate(g, "approve")
			select {
			case <-bDone:
			case <-time.After(5 * time.Second):
				return nil, fmt.Errorf("the safe sibling never started after the gated call ended")
			}
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("checking still going")},
				streamCall("call-a", "write_a"), streamCall("call-b", "read_b"))
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("after approval")})
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
	}}
	w := wrapToolOrchestrationLLMWithProgress(inner, st, "", store, func(context.Context) string { return "gpt-main" })
	base := toolpkg.WithInlineApprovalHook(llm.WithAgentSessionID(toolpkg.WithRunID(context.Background(), runID), sid), hook)
	execCtx := llm.WithStreamSink(base, streamTestSink())
	res, err := w.Execute(execCtx, []llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{gated, safe})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "after approval" {
		t.Fatalf("result = %+v, want the turn to continue", res)
	}
	if *aRuns != 1 || atomic.LoadInt32(&bRuns) != 1 {
		t.Fatalf("runs: gated=%d safe=%d, want 1 and 1", *aRuns, atomic.LoadInt32(&bRuns))
	}
}

// A gate asked in place does not hold back its concurrency-safe siblings: a
// safe gated call and a safe ungated call run together, the question open.
func TestInPlaceSafeGateLetsSafeSiblingsRun(t *testing.T) {
	const sid = "streamtools-safe-gate"
	store, runID := newProgressStore(t, sid)
	st := toolpkg.NewState(t.TempDir())
	streamHookRunner(t, sid, st, "read_b")
	st.RegisterToolMeta(event.ToolMeta{Name: "read_a", ReadOnly: true, ConcurrencySafe: true})
	st.RegisterToolMeta(event.ToolMeta{Name: "read_b", ReadOnly: true, ConcurrencySafe: true})
	hook, gates := inlineGateHook()

	gated, _ := streamHookTool(t, st, "read_a", func(context.Context) (string, error) { return "a ok", nil })
	var bRuns int32
	bDone := make(chan struct{})
	var bOnce sync.Once
	safe, err := llm.NewTool("read_b", "safe sibling", func(context.Context, *struct{}) (string, error) {
		atomic.AddInt32(&bRuns, 1)
		bOnce.Do(func() { close(bDone) })
		return "b ok", nil
	})
	if err != nil {
		t.Fatalf("safe tool: %v", err)
	}

	inner := &streamFailLLM{attempts: []func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error){
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			sink.OnToolCallReady(streamCall("call-a", "read_a"))
			g := waitInlineGate(t, gates, "safe gated")
			sink.OnToolCallReady(streamCall("call-b", "read_b"))
			// The sibling runs while the question is still open.
			select {
			case <-bDone:
			case <-time.After(5 * time.Second):
				return nil, fmt.Errorf("the safe sibling never ran beside the open gate")
			}
			answerInlineGate(g, "approve")
			msg := llm.AssistantMessage(nil, streamCall("call-a", "read_a"), streamCall("call-b", "read_b"))
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
	}}
	w := wrapToolOrchestrationLLMWithProgress(inner, st, "", store, func(context.Context) string { return "gpt-main" })
	base := toolpkg.WithInlineApprovalHook(llm.WithAgentSessionID(toolpkg.WithRunID(context.Background(), runID), sid), hook)
	execCtx := llm.WithStreamSink(base, streamTestSink())
	if _, err := w.Execute(execCtx, []llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{gated, safe}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if atomic.LoadInt32(&bRuns) != 1 {
		t.Fatalf("safe sibling runs = %d, want 1", bRuns)
	}
}

// One question per run: with two gated concurrency-safe calls, the hook sees
// the first alone; the second asks only after the first was answered and its
// call finished.
func TestOnlyOneInPlaceQuestionPerRun(t *testing.T) {
	const sid = "streamtools-one-question"
	store, runID := newProgressStore(t, sid)
	st := toolpkg.NewState(t.TempDir())
	streamHookRunner(t, sid, st)
	st.RegisterToolMeta(event.ToolMeta{Name: "read_a", ReadOnly: true, ConcurrencySafe: true})
	st.RegisterToolMeta(event.ToolMeta{Name: "read_b", ReadOnly: true, ConcurrencySafe: true})
	hook, gates := inlineGateHook()

	aTool, aRuns := streamHookTool(t, st, "read_a", func(context.Context) (string, error) { return "a ok", nil })
	bTool, _ := streamHookTool(t, st, "read_b", func(context.Context) (string, error) { return "b ok", nil })

	inner := &streamFailLLM{attempts: []func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error){
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			sink.OnToolCallReady(streamCall("call-a", "read_a"))
			first := waitInlineGate(t, gates, "first gated")
			sink.OnToolCallReady(streamCall("call-b", "read_b"))
			// B is up and running, but its gate cannot ask yet.
			time.Sleep(250 * time.Millisecond)
			select {
			case second := <-gates:
				t.Errorf("the second gate asked before the first was answered: %+v", second.rae)
			default:
			}
			answerInlineGate(first, "approve")
			second := waitInlineGate(t, gates, "second gated")
			if got := atomic.LoadInt32(aRuns); got != 1 {
				t.Errorf("first call runs = %d when the second gate asked, want 1", got)
			}
			answerInlineGate(second, "approve")
			msg := llm.AssistantMessage(nil, streamCall("call-a", "read_a"), streamCall("call-b", "read_b"))
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
	}}
	w := wrapToolOrchestrationLLMWithProgress(inner, st, "", store, func(context.Context) string { return "gpt-main" })
	base := toolpkg.WithInlineApprovalHook(llm.WithAgentSessionID(toolpkg.WithRunID(context.Background(), runID), sid), hook)
	execCtx := llm.WithStreamSink(base, streamTestSink())
	res, err := w.Execute(execCtx, []llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{aTool, bTool})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "done" {
		t.Fatalf("result = %+v, want the turn to continue", res)
	}
}
