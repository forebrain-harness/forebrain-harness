// Execution of tool calls while their response is still streaming.
package run

import (
	"context"
	"log/slog"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// The lifecycle states of one streamed tool call.
const (
	streamedCallQueued = iota
	streamedCallExecuting
	streamedCallDone
	streamedCallDeferred
	streamedCallNotRun
)

// streamingToolExecutor starts tool calls while their response streams. It is
// fed by OnToolCallReady and finished by finish once the response is over.
type streamingToolExecutor struct {
	w        *toolOrchestrationLLM
	toolMap  map[string]*llm.Tool
	progress *llm.StreamProgress
	// ctx is the turn's context: the steer preemption reads it, the way
	// executeToolCalls reads its own.
	ctx context.Context
	// beforeStart runs, under no lock, right before a call starts: it commits
	// the steers riding on the request, persists the partial assistant message
	// and returns the context the call runs under (nil = do not start).
	beforeStart func(readyUpTo []llm.ToolCall) context.Context
	// stopTurn cancels the model call with ErrTurnStoppedByRejection: the
	// user refused a streamed call without words, and the stream itself is
	// what has to stop.
	stopTurn func()
	limit    int

	mu     sync.Mutex
	calls  []*streamedCall // ready order
	halted bool            // a deferred, preempted, refused or not-startable-in-stream call stops further starts
	closed bool            // the stream ended; finish took over
	// refused records that a call was refused without words: the model call
	// is being cancelled with the refusal as its cause, and the settlement
	// after it ends the turn on ErrTurnStoppedByRejection rather than as an
	// ordinary cancellation.
	refused bool
	wg      sync.WaitGroup
}

type streamedCall struct {
	call llm.ToolCall
	safe bool
	// started is set once, under the executor's lock, when the call moves to
	// executing: dispatched reports it even after the call settles.
	started bool
	state   int // queued, executing, done, deferred, notRun
	result  orchestratedToolResult
	// cancel interrupts this one call's context when a refusal without words
	// stops the turn; nil once the call has settled.
	cancel context.CancelCauseFunc
}

// add takes one call whose arguments completed inside the stream. Calls after
// the stream ended are ignored: the response's own list is authoritative by
// then.
func (e *streamingToolExecutor) add(call llm.ToolCall) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.calls = append(e.calls, &streamedCall{
		call:  call,
		safe:  e.w.state.CallConcurrencySafe(call),
		state: streamedCallQueued,
	})
	e.mu.Unlock()
	e.pump()
}

// steerWaitingLocked reports whether a queued user message waits to take the
// turn, the same way executeToolCalls' precheck does.
func (e *streamingToolExecutor) steerWaitingLocked() bool {
	if e.ctx == nil || e.ctx.Err() != nil {
		return false
	}
	return TurnInputRuntimeFromContext(e.ctx).HasSteers()
}

// canStartLocked answers why-#1: a call may start when nothing executes, or
// when it and everything executing is concurrency-safe, within the cap.
func (e *streamingToolExecutor) canStartLocked(first *streamedCall) bool {
	executing := 0
	for _, c := range e.calls {
		if c.state != streamedCallExecuting {
			continue
		}
		executing++
		if !c.safe {
			return false
		}
	}
	if executing > 0 && !first.safe {
		return false
	}
	limit := e.limit
	if limit <= 0 {
		limit = 1
	}
	return executing < limit
}

// readyUpToLocked lists the calls reported ready so far, in ready order, up to
// and including target: the tool calls a provisional assistant message may
// already own.
func (e *streamingToolExecutor) readyUpToLocked(target *streamedCall) []llm.ToolCall {
	out := make([]llm.ToolCall, 0, len(e.calls))
	for _, c := range e.calls {
		out = append(out, c.call)
		if c == target {
			break
		}
	}
	return out
}

// pump starts every call that may start now. It picks under the lock and
// starts outside it, so a slow beforeStart never blocks add. One pump may
// start several concurrency-safe calls in a row.
func (e *streamingToolExecutor) pump() {
	for {
		e.mu.Lock()
		if e.closed || e.halted {
			e.mu.Unlock()
			return
		}
		first := -1
		for i, c := range e.calls {
			if c.state == streamedCallQueued {
				first = i
				break
			}
		}
		if first < 0 {
			e.mu.Unlock()
			return
		}
		if e.steerWaitingLocked() {
			// A queued steer takes the turn at this call boundary: it and
			// every call behind it are answered "not run".
			for i, c := range e.calls {
				if c.state != streamedCallQueued {
					continue
				}
				c.state = streamedCallNotRun
				c.result = steerPreemptedResult(i, c.call)
			}
			e.halted = true
			e.mu.Unlock()
			return
		}
		call := e.calls[first]
		if !e.canStartLocked(call) {
			// A call that cannot start blocks every call behind it.
			e.mu.Unlock()
			return
		}
		call.state = streamedCallExecuting
		call.started = true
		readyUpTo := e.readyUpToLocked(call)
		// The waiter is registered where the state changes: a finish that
		// observes no goroutine must not be able to pass this call.
		e.wg.Add(1)
		e.mu.Unlock()
		e.progress.MarkToolDispatched()
		baseCtx := e.beforeStart(readyUpTo)
		if baseCtx == nil {
			e.mu.Lock()
			call.state = streamedCallQueued
			e.halted = true
			e.mu.Unlock()
			e.wg.Done()
			return
		}
		// The call's own cancellation, so a refusal without words elsewhere in
		// the batch can interrupt this call without touching the turn.
		callCtx, cancelCall := context.WithCancelCause(baseCtx)
		e.mu.Lock()
		call.cancel = cancelCall
		e.mu.Unlock()
		index := first
		go func() {
			defer e.wg.Done()
			defer cancelCall(nil)
			// The crash-recovery snapshot of this call's in-place gates: the
			// provisional session the call started under, plus every result
			// this executor has already settled.
			snapshot := func() []llm.Message {
				base, _ := forkRuntimeSnapshotFromContext(callCtx)
				e.mu.Lock()
				defer e.mu.Unlock()
				return appendResultsToSnapshot(base, e.calls)
			}
			res, stop := e.w.runCallInPlace(tool.WithStreamingDispatch(callCtx), index, call.call, e.toolMap, toolUISuppression{}, snapshot)
			e.mu.Lock()
			call.state = streamedCallDone
			call.result = res
			call.cancel = nil
			if res.deferred {
				call.state = streamedCallDeferred
				// An approval or question defers to the end of the stream,
				// and so does everything queued behind it.
				e.halted = true
			}
			if stop {
				// The user refused this call without words: the queued calls
				// never start, the executing ones are interrupted, and the
				// model call still streaming is cancelled with the refusal as
				// its cause.
				e.refused = true
				e.halted = true
				for i, c := range e.calls {
					if c.state == streamedCallExecuting && c != call && c.cancel != nil {
						c.cancel(ErrTurnStoppedByRejection)
					}
					if c.state != streamedCallQueued {
						continue
					}
					c.state = streamedCallNotRun
					c.result = rejectionStoppedResult(i, c.call)
				}
				stopTurn := e.stopTurn
				e.mu.Unlock()
				if stopTurn != nil {
					stopTurn()
				}
				return
			}
			e.mu.Unlock()
			e.pump()
		}()
	}
}

// appendResultsToSnapshot adds the executor's settled results, in ready
// order, to a provisional session. The caller holds e.mu.
func appendResultsToSnapshot(base []llm.Message, calls []*streamedCall) []llm.Message {
	out := append([]llm.Message(nil), base...)
	for _, c := range calls {
		if c.state != streamedCallDone {
			continue
		}
		if isRequiresActionError(c.result.err) {
			continue
		}
		out = append(out, toolResultMessage(c.result))
	}
	return out
}

// refusedStop reports whether a streamed call was refused without words and
// its refusal cancelled the model call.
func (e *streamingToolExecutor) refusedStop() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.refused
}

// dispatched reports whether any call was ever started in the stream.
func (e *streamingToolExecutor) dispatched() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, c := range e.calls {
		if c.started {
			return true
		}
	}
	return false
}

// hold freezes the executor: no further call can start, and every started
// call has settled. The caller may then mutate the session beforeStart closed
// over.
func (e *streamingToolExecutor) hold() {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	e.wg.Wait()
}

// reconcile aligns the response's final call list with what the executor ran:
// every executed call must appear in it, so a call the stream reported ready
// but the finished response omits is appended, keeping every executed call
// answerable. It holds the executor first.
func (e *streamingToolExecutor) reconcile(final []llm.ToolCall) []llm.ToolCall {
	e.hold()
	e.mu.Lock()
	defer e.mu.Unlock()
	known := make(map[string]bool, len(final))
	for _, call := range final {
		known[call.ID] = true
	}
	for _, c := range e.calls {
		if !c.started && c.state != streamedCallNotRun {
			continue
		}
		if known[c.call.ID] {
			continue
		}
		slog.Warn("streaming tool execution outran the response's tool calls; appending the call it ran",
			"tool_name", c.call.Function.Name, "call_id", c.call.ID)
		final = append(final, c.call)
		known[c.call.ID] = true
	}
	return final
}

// finish settles the batch once the response is over: the calls that already
// ran keep their results — none is ever re-run — and the rest (deferred,
// still queued, or never reported ready) run through executeToolCalls in
// their original order, approvals included. Its second return reports a
// refusal without words stopping the batch.
func (e *streamingToolExecutor) finish(ctx context.Context, finalCalls []llm.ToolCall) ([]orchestratedToolResult, bool) {
	finalCalls = e.reconcile(finalCalls)
	results := make([]orchestratedToolResult, len(finalCalls))
	byID := make(map[string]*streamedCall, len(e.calls))
	e.mu.Lock()
	for _, c := range e.calls {
		byID[c.call.ID] = c
	}
	rest := make([]llm.ToolCall, 0, len(finalCalls))
	restAt := make([]int, 0, len(finalCalls))
	for i, call := range finalCalls {
		c, ok := byID[call.ID]
		if ok && (c.state == streamedCallDone || c.state == streamedCallNotRun) {
			result := c.result
			result.index = i
			results[i] = result
			continue
		}
		rest = append(rest, call)
		restAt = append(restAt, i)
	}
	e.mu.Unlock()
	// A refusal without words can land after the response body ended: the
	// call was streamed, it was started in-stream, and it waited on its
	// approval while the rest of the stream arrived. By the time it answers,
	// the stream-end check above (orchestration_llm's refusedStop read) has
	// already run, so this is the only place left that can report it. Nothing
	// behind the refused call may start — that is what "refuses and stops"
	// means (plan 129) — and the caller ends the turn on this bool without
	// asking the model again.
	if e.refusedStop() {
		for i, call := range finalCalls {
			c, ok := byID[call.ID]
			if ok && (c.state == streamedCallDone || c.state == streamedCallNotRun) {
				result := c.result
				result.index = i
				results[i] = result
				continue
			}
			results[i] = rejectionStoppedResult(i, call)
		}
		return results, true
	}
	if len(rest) > 0 {
		restResults, stopped := e.w.executeToolCalls(ctx, rest, e.toolMap)
		for j, result := range restResults {
			if j >= len(restAt) {
				break
			}
			result.index = restAt[j]
			results[restAt[j]] = result
		}
		if stopped {
			return results, true
		}
	}
	return results, false
}

// abort settles a cancelled request: every readied call gets a result — the
// ones that finished their real one, the rest an interruption — so they are
// written with the assistant message before the cancellation returns.
func (e *streamingToolExecutor) abort(ctx context.Context) []orchestratedToolResult {
	e.hold()
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]orchestratedToolResult, 0, len(e.calls))
	for _, c := range e.calls {
		if c.state == streamedCallDone || c.state == streamedCallNotRun {
			// Not-run calls keep their own answer: a refusal without words
			// already wrote the not-run sentence on them.
			out = append(out, c.result)
			continue
		}
		out = append(out, orchestratedToolResult{
			index: len(out),
			call:  c.call,
			err:   context.Canceled,
		})
	}
	return out
}
