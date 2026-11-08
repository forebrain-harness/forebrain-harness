package google

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/google/uuid"
)

// executeStreaming runs one streamGenerateContent call, forwarding text, calls,
// usage and the finish reason to the stream sink as they arrive.
func (c *agentLLM) executeStreaming(ctx context.Context, raw []byte) (*llm.Result, error) {
	endpoint := c.baseURL + "/v1beta/models/" + url.PathEscape(c.model) + ":streamGenerateContent?alt=sse"
	ctx = llm.WithStreamingRequest(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("google: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	sink := llm.StreamSinkFrom(ctx)
	// The sink's OnEnd is not fired here: Execute owns it for the whole
	// continuation loop, so it fires once per call however many rounds ran.
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google: send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		if err != nil {
			return nil, fmt.Errorf("google: read response: %w", err)
		}
		return nil, newAPIError(resp.StatusCode, payload, resp.Header.Get("Retry-After"))
	}
	if sink != nil && sink.Streamed != nil {
		*sink.Streamed = true
	}

	st := streamState{}
	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		line, eof, err := readSSELine(reader, maxResponseBytes)
		if err != nil {
			// The attempt was already billed for whatever usageMetadata it
			// delivered, so that billing rides the error into the retry
			// loop's accounting.
			return nil, llm.NewExecuteError(fmt.Errorf("google: read stream: %w", err), st.usage)
		}
		if payload, ok := sseDataLine(line); ok {
			if payload == "[DONE]" {
				break
			}
			if payload != "" {
				if err := consumeSSEEvent(sink, &st, []byte(payload), c.model); err != nil {
					return nil, llm.NewExecuteError(err, st.usage)
				}
			}
		}
		if eof {
			break
		}
	}

	// A block still open when the stream ended had no finish frame to seal it,
	// so it is collected for the truncation decision below without another
	// OnTextBlockDone — the finish path already sealed its own block.
	st.endThought(sink, "")
	if st.text.Len() > 0 {
		st.sealTextBlock("")
	}
	if st.finish == "" && (len(st.parts) > 0 || len(st.calls) > 0) {
		return nil, llm.NewExecuteError(fmt.Errorf("google: %w", llm.ErrStreamTruncated), st.usage)
	}
	if len(st.parts) == 0 && len(st.calls) == 0 {
		if st.finish == "" {
			// The stream closed cleanly before a single event: no part, no
			// finish frame. The request is re-sent without streaming, not
			// finished as an empty answer.
			return nil, llm.NewExecuteError(fmt.Errorf("google: %w", llm.ErrStreamNoEvents), st.usage)
		}
		if llm.OutputLimitReached(st.finish) {
			// MAX_TOKENS before any content is a cut, not a refusal: the
			// round produced an output-limit fragment (here an empty one —
			// the thinking spent the budget), and Execute's recovery loop
			// decides whether to re-ask. Every other finish reason really
			// did end the answer with nothing said.
			return &llm.Result{
				Message:    &llm.Message{Role: llm.RoleAssistant},
				StopReason: llm.StopReasonOutputLimit,
				Usage:      st.usage,
			}, nil
		}
		// A STOP that billed output tokens yet delivered nothing is a blank
		// output to re-send (plans/273), not a refusal: STOP is the model's
		// own "done", so the provider charged for an answer it never handed
		// over (typically the thinking spent the tokens).
		if st.usage != nil && st.usage.OutputTokens > 0 && strings.EqualFold(st.finish, "STOP") {
			return nil, llm.NewExecuteError(fmt.Errorf("google: response was empty (finish reason %q): %w", st.finish, llm.ErrBlankModelOutput), st.usage)
		}
		return nil, llm.NewExecuteError(fmt.Errorf("google: response was empty (finish reason %q)", st.finish), st.usage)
	}
	return &llm.Result{
		Message: st.message(c.model),
		// Why the stream stopped, normalized: MAX_TOKENS maps onto the
		// output-limit reason the engine reads.
		StopReason: llm.NormalizeStopReason(st.finish),
		Usage:      st.usage,
	}, nil
}

// message assembles the round's reply. The layout rides the reasoning carry
// when it holds a signature or a thought summary, so the next request gives the
// model its own reasoning back (geminiReplayLayout); model is recorded as the
// producer that may receive it.
func (st *streamState) message(model string) *llm.Message {
	msg := &llm.Message{Role: llm.RoleAssistant, Parts: st.parts, ToolCalls: st.calls}
	if geminiLayoutHasReasoning(st.layout) {
		msg.Name = llm.EncodeGeminiParts(st.layout)
		msg.ReasoningModel = strings.TrimSpace(model)
	}
	return msg
}

// streamState accumulates one streamGenerateContent response.
type streamState struct {
	parts []llm.ContentPart
	calls []llm.ToolCall
	text  strings.Builder
	// layout is the reply's parts in arrival order, one entry per part that
	// carried something to give back: answer text, a call, a thought summary, or
	// a bare signature. Gemini checks a signature against the part it arrived
	// in, so this is kept apart from parts (which holds answer text only).
	layout []llm.GeminiPart
	// thought accumulates the thought summary being streamed, and inThought
	// says whether one is open: a signature can arrive on a part with no text,
	// and the flag is what tells that block from no block at all.
	thought   strings.Builder
	inThought bool
	finish    string
	usage     *llm.Usage
	// reportedIn/reportedOut are the cumulative usage already reported as
	// deltas, so each event reports only its increment.
	reportedIn, reportedOut int
	started                 bool
}

// consumeSSEEvent folds one decoded stream event into the state, forwarding
// every piece to the sink as it arrives. model is the model that wrote the
// response: every part the state seals into the layout is published as that
// reply's reasoning, stamped with its producer.
func consumeSSEEvent(sink *llm.StreamSink, st *streamState, eventBytes []byte, model string) error {
	var event generateContentResponse
	if err := json.Unmarshal(eventBytes, &event); err != nil {
		return fmt.Errorf("google: decode stream event: %w", err)
	}
	if len(event.Error) > 0 && string(event.Error) != "null" {
		return newAPIError(http.StatusOK, eventBytes, "")
	}
	if usage := event.UsageMetadata; usage.PromptTokenCount > 0 || usage.CandidatesTokenCount > 0 || usage.ThoughtsTokenCount > 0 {
		// usageMetadata is cumulative across the stream: each event reports
		// only its increment, and every snapshot replaces the running figure.
		in := usage.PromptTokenCount
		out := usage.CandidatesTokenCount + usage.ThoughtsTokenCount
		if sink != nil && sink.OnUsage != nil {
			deltaIn, deltaOut := in-st.reportedIn, out-st.reportedOut
			if deltaIn > 0 || deltaOut > 0 {
				sink.OnUsage(deltaIn, deltaOut)
				st.reportedIn, st.reportedOut = in, out
			}
		}
		if sink != nil && sink.OnUsageSnapshot != nil {
			sink.OnUsageSnapshot(in, out)
		}
		st.usage = &llm.Usage{InputTokens: in, OutputTokens: out}
	}
	if len(event.Candidates) == 0 {
		return nil
	}
	candidate := event.Candidates[0]
	if st.finish != "" {
		// Everything after the finish frame is bookkeeping, not output.
		return nil
	}
	for _, p := range candidate.Content.Parts {
		switch {
		case p.FunctionCall != nil:
			llm.NotifyResponseStarted(sink, &st.started)
			// A call ends the answer text that preceded it, the same seal the
			// finish frame applies to the final block, and any thought summary
			// still open: the model decided to act instead of finishing the
			// sentence or the thought.
			st.endThought(sink, "")
			if st.text.Len() > 0 {
				st.endTextBlock(sink)
			}
			call := googleToolCallFromPart(p)
			st.calls = append(st.calls, call)
			st.layout = append(st.layout, llm.GeminiPart{Kind: "call", Call: len(st.calls) - 1, Signature: p.ThoughtSignature})
			// A function-call part arrives complete, so the layout now covers
			// the reply up to this call. It goes out before the call is
			// reported ready: that notice is what makes the engine start the
			// call and write a provisional message from the sink, and the
			// provisional message has to know this reply's reasoning already.
			st.publishCarry(sink, model)
			if sink != nil && sink.OnToolCallReady != nil {
				sink.OnToolCallReady(call)
			}
		case p.Thought:
			// A thought summary is reasoning, not the answer: it is collected
			// apart from the answer text, so it never reaches the reply.
			st.endTextBlockIfOpen(sink)
			st.inThought = true
			if p.Text != "" {
				llm.NotifyResponseStarted(sink, &st.started)
				st.thought.WriteString(p.Text)
				if sink != nil && sink.OnReasoningDelta != nil {
					sink.OnReasoningDelta(p.Text)
				}
			}
			if p.ThoughtSignature != "" {
				// The signature closes the thought summary it arrived with, and
				// stays on that one part: a signature cannot be merged onto text
				// it did not come with.
				st.endThought(sink, p.ThoughtSignature)
				// A thought summary sealed together with its signature is
				// complete reasoning: the layout holds it from here on, so a
				// reply cut off later can still replay it.
				st.publishCarry(sink, model)
			}
		case p.Text != "":
			llm.NotifyResponseStarted(sink, &st.started)
			// Answer text ends an open thought summary: the model has stopped
			// reasoning and started answering.
			st.endThought(sink, "")
			st.text.WriteString(p.Text)
			if sink != nil && sink.OnDelta != nil {
				sink.OnDelta(p.Text)
			}
			if p.ThoughtSignature != "" {
				// The signature closes this text block, so the block ends here
				// rather than absorbing the text that follows it.
				st.endSignedTextBlock(sink, p.ThoughtSignature)
				// A signed answer block is complete, signature and all: the
				// layout covers the reply up to it, so a cut from here on keeps
				// that much of it.
				st.publishCarry(sink, model)
			}
		case p.ThoughtSignature != "":
			// A part carrying only a signature still belongs to this reply and
			// must come back, but it says nothing to the answer or the surface.
			st.endThought(sink, "")
			st.endTextBlockIfOpen(sink)
			st.layout = append(st.layout, llm.GeminiPart{Kind: "text", Signature: p.ThoughtSignature})
			// The signature is complete on its own part, so the layout is as
			// far as this reply can be replayed.
			st.publishCarry(sink, model)
		}
	}
	if candidate.FinishReason != "" {
		st.endThought(sink, "")
		if st.text.Len() > 0 {
			st.endTextBlock(sink)
		}
		if sink != nil && sink.OnStopReason != nil {
			sink.OnStopReason(candidate.FinishReason)
		}
		if sink != nil && sink.OnMessageStop != nil {
			sink.OnMessageStop()
		}
		st.finish = candidate.FinishReason
	}
	return nil
}

// publishCarry hands the layout the stream has built so far to the sink's
// OnReasoningCarry, once it holds a signature or a thought summary. Every part
// in the layout is closed, so it is what a reply cut off from here on keeps of
// its reasoning; a layout holding neither says nothing reasoning does, and the
// finished message carries nothing for it either.
func (st *streamState) publishCarry(sink *llm.StreamSink, model string) {
	if sink == nil || sink.OnReasoningCarry == nil || !geminiLayoutHasReasoning(st.layout) {
		return
	}
	if carry := llm.EncodeGeminiParts(st.layout); carry != "" {
		sink.OnReasoningCarry(carry, strings.TrimSpace(model))
	}
}

// sealTextBlock records the open answer text as one block: one llm part, and one
// layout part carrying signature — empty when the text arrived unsigned.
func (st *streamState) sealTextBlock(signature string) {
	text := st.text.String()
	st.parts = append(st.parts, llm.Text(text))
	st.layout = append(st.layout, llm.GeminiPart{Kind: "text", Text: text, Signature: signature})
	st.text.Reset()
}

// endTextBlock seals the answer text accumulated so far as one block.
func (st *streamState) endTextBlock(sink *llm.StreamSink) {
	st.sealTextBlock("")
	if sink != nil && sink.OnTextBlockDone != nil {
		sink.OnTextBlockDone()
	}
}

// endTextBlockIfOpen seals the open answer text, if any.
func (st *streamState) endTextBlockIfOpen(sink *llm.StreamSink) {
	if st.text.Len() > 0 {
		st.endTextBlock(sink)
	}
}

// endSignedTextBlock seals the open answer text with the signature that arrived
// on it. A signed block stays its own part: Gemini cannot merge two signatures,
// and a signature moved onto text it did not come with is invalid.
func (st *streamState) endSignedTextBlock(sink *llm.StreamSink, signature string) {
	st.sealTextBlock(signature)
	if sink != nil && sink.OnTextBlockDone != nil {
		sink.OnTextBlockDone()
	}
}

// endThought seals the open thought summary as one reasoning block, carrying
// signature when one arrived with it. Sealing it reports the thinking as done;
// nothing about it enters the answer.
func (st *streamState) endThought(sink *llm.StreamSink, signature string) {
	if !st.inThought {
		return
	}
	st.layout = append(st.layout, llm.GeminiPart{Kind: "thought", Text: st.thought.String(), Signature: signature})
	st.thought.Reset()
	st.inThought = false
	if sink != nil && sink.OnReasoningDone != nil {
		sink.OnReasoningDone()
	}
}

// googleToolCallFromPart mints the llm form of one functionCall part. The id
// is minted here exactly once: the same value feeds OnToolCallReady and the
// final Result, so the tool result pairs with what the sink observed.
func googleToolCallFromPart(p part) llm.ToolCall {
	args := "{}"
	if raw, err := json.Marshal(p.FunctionCall.Args); err == nil && p.FunctionCall.Args != nil {
		args = string(raw)
	}
	return llm.ToolCall{
		// generateContent does not name its calls, so one is minted: the
		// id is what pairs the tool result with this call.
		ID:       uuid.NewString(),
		Type:     llm.ToolTypeFunction,
		Function: llm.FunctionCall{Name: p.FunctionCall.Name, Arguments: args},
	}
}

// readSSELine returns the next complete line without its terminator, reading
// through buffer boundaries so a line may exceed the reader's buffer up to
// limit. eof reports the stream ended; an unterminated trailing line is
// discarded, because a stream cut mid-event is a truncation, not a partial
// event to decode.
func readSSELine(r *bufio.Reader, limit int) (line string, eof bool, err error) {
	var acc []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			acc = append(acc, chunk...)
			if len(acc) > limit {
				return "", false, fmt.Errorf("google: stream event exceeds %d bytes", limit)
			}
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return "", false, err
		}
		if errors.Is(err, io.EOF) {
			return "", true, nil
		}
		if acc != nil {
			acc = append(acc, chunk...)
			chunk = acc
		}
		return strings.TrimRight(string(chunk), "\r\n"), false, nil
	}
}

// sseDataLine reports whether the line carries a data field and returns its
// value with the prefix and one optional space removed. Blank lines, comments
// and the event/id fields carry nothing this client reads.
func sseDataLine(line string) (string, bool) {
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	value := strings.TrimPrefix(line, "data:")
	value = strings.TrimPrefix(value, " ")
	return value, true
}
