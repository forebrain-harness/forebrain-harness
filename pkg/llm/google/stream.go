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
	if sink != nil && sink.OnEnd != nil {
		defer sink.OnEnd()
	}
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
			return nil, fmt.Errorf("google: read stream: %w", err)
		}
		if payload, ok := sseDataLine(line); ok {
			if payload == "[DONE]" {
				break
			}
			if payload != "" {
				if err := consumeSSEEvent(sink, &st, []byte(payload)); err != nil {
					return nil, err
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
	if st.text.Len() > 0 {
		st.parts = append(st.parts, llm.Text(st.text.String()))
		st.text.Reset()
	}
	if st.finish == "" && (len(st.parts) > 0 || len(st.calls) > 0) {
		return nil, fmt.Errorf("google: %w", llm.ErrStreamTruncated)
	}
	if len(st.parts) == 0 && len(st.calls) == 0 {
		if st.finish == "" {
			// The stream closed cleanly before a single event: no part, no
			// finish frame. The request is re-sent without streaming, not
			// finished as an empty answer.
			return nil, fmt.Errorf("google: %w", llm.ErrStreamNoEvents)
		}
		return nil, fmt.Errorf("google: response was empty (finish reason %q)", st.finish)
	}
	return &llm.Result{
		Message: &llm.Message{Role: llm.RoleAssistant, Parts: st.parts, ToolCalls: st.calls},
		Usage:   st.usage,
	}, nil
}

// streamState accumulates one streamGenerateContent response.
type streamState struct {
	parts  []llm.ContentPart
	calls  []llm.ToolCall
	text   strings.Builder
	finish string
	usage  *llm.Usage
	// reportedIn/reportedOut are the cumulative usage already reported as
	// deltas, so each event reports only its increment.
	reportedIn, reportedOut int
	started                 bool
}

// consumeSSEEvent folds one decoded stream event into the state, forwarding
// every piece to the sink as it arrives.
func consumeSSEEvent(sink *llm.StreamSink, st *streamState, eventBytes []byte) error {
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
			// finish frame applies to the final block.
			if st.text.Len() > 0 {
				st.endTextBlock(sink)
			}
			call := googleToolCallFromPart(p)
			st.calls = append(st.calls, call)
			if sink != nil && sink.OnToolCallReady != nil {
				sink.OnToolCallReady(call)
			}
		case p.Text != "":
			llm.NotifyResponseStarted(sink, &st.started)
			st.text.WriteString(p.Text)
			if sink != nil && sink.OnDelta != nil {
				sink.OnDelta(p.Text)
			}
		}
	}
	if candidate.FinishReason != "" {
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

// endTextBlock seals the answer text accumulated so far as one block.
func (st *streamState) endTextBlock(sink *llm.StreamSink) {
	st.parts = append(st.parts, llm.Text(st.text.String()))
	st.text.Reset()
	if sink != nil && sink.OnTextBlockDone != nil {
		sink.OnTextBlockDone()
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
