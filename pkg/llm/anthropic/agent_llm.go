package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	anthropicapi "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/google/uuid"
)

const (
	// anthropicDefaultModel is the model used when a provider entry names none.
	anthropicDefaultModel = "claude-sonnet-4-6"

	// anthropicDefaultMaxTokens is the output cap used when a provider entry
	// sets none.
	anthropicDefaultMaxTokens int64 = 2048
)

// errAnthropicToolMessageMissingToolCallID is returned when an OpenAI-shaped
// tool message carries no ToolCallID, which Anthropic requires as tool_use_id.
var errAnthropicToolMessageMissingToolCallID = errors.New("anthropic: tool message missing tool_call_id")

// anthropicUnsupportedRoleError is returned when converting an OpenAI-shaped
// message whose role has no Anthropic equivalent. The roles that convert are
// system, developer, user, assistant, and tool.
type anthropicUnsupportedRoleError struct {
	Role string
}

func (e *anthropicUnsupportedRoleError) Error() string {
	return fmt.Sprintf("anthropic: unsupported role %q", e.Role)
}

// anthropicNilResponseError is returned when the SDK yields no message.
type anthropicNilResponseError struct{}

func (e *anthropicNilResponseError) Error() string {
	return "anthropic: nil response"
}

type anthropicAgentLLM struct {
	client    anthropicapi.Client
	model     string
	maxTokens int64
	effort    anthropicapi.OutputConfigEffort
}

// NewAgentLLM builds the Anthropic client used by the agent runtime.
//
// There is deliberately no temperature parameter. Every current Anthropic model
// from Opus 4.7 on rejects temperature/top_p/top_k outright, and the models that
// still accept them reject any non-default value, so a sampling knob here can
// only produce request errors; behavior is steered through the prompt and the
// effort level instead.
func NewAgentLLM(apiKey, baseURL, model string, maxTokens int64, effort ...string) llm.LLM {
	return newAgentLLM(apiKey, baseURL, model, maxTokens, nil, effort...)
}

// NewAgentLLMWithTransport is NewAgentLLM with the http client built on
// transport instead of the shared one, which is where a provider that
// authenticates outside the apiKey argument (OpenCode's gateway) stamps its
// bearer token, client identity and session header. effort keeps its meaning.
// A nil transport falls back to the shared client, so this is never a worse
// call than NewAgentLLM.
func NewAgentLLMWithTransport(apiKey, baseURL, model string, maxTokens int64, transport http.RoundTripper, effort ...string) llm.LLM {
	return newAgentLLM(apiKey, baseURL, model, maxTokens, transport, effort...)
}

func newAgentLLM(apiKey, baseURL, model string, maxTokens int64, transport http.RoundTripper, effort ...string) llm.LLM {
	c := &anthropicAgentLLM{
		model:     strings.TrimSpace(model),
		maxTokens: anthropicDefaultMaxTokens,
	}
	if c.model == "" {
		c.model = anthropicDefaultModel
	}
	if maxTokens > 0 {
		c.maxTokens = maxTokens
	}
	if len(effort) > 0 {
		if level := normalizeEffort(effort[0]); AcceptsEffort(level) {
			c.effort = anthropicapi.OutputConfigEffort(level)
		}
	}
	clientOpts := []option.RequestOption{}
	if strings.TrimSpace(apiKey) != "" {
		clientOpts = append(clientOpts, option.WithAPIKey(strings.TrimSpace(apiKey)))
	}
	if strings.TrimSpace(baseURL) != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(strings.TrimSpace(baseURL)))
	}
	clientOpts = append(clientOpts, option.WithHTTPClient(newAgentHTTPClient(transport)))
	c.client = anthropicapi.NewClient(clientOpts...)
	return c
}

// anthropicEffortLevels is every level the API accepts in
// output_config.effort, spelled by the SDK's own constants and ordered from
// least to most thinking. anthropic-sdk-go v1.80.0 names all five — including
// xhigh, which an older assumption in this repository used to hide from every
// picker — so this list is the client's capability, not a policy.
var anthropicEffortLevels = []anthropicapi.OutputConfigEffort{
	anthropicapi.OutputConfigEffortLow,
	anthropicapi.OutputConfigEffortMedium,
	anthropicapi.OutputConfigEffortHigh,
	anthropicapi.OutputConfigEffortXhigh,
	anthropicapi.OutputConfigEffortMax,
}

// AcceptsEffort reports whether this client can send level: a level outside
// the API's set would be dropped from the request body, so a surface must not
// offer it. It is the one place that answers the question for the Anthropic
// protocol and for the OpenCode models that ride it.
func AcceptsEffort(level string) bool {
	level = normalizeEffort(level)
	if level == "" {
		return false
	}
	for _, candidate := range anthropicEffortLevels {
		if string(candidate) == level {
			return true
		}
	}
	return false
}

// EffortLevels lists the levels AcceptsEffort accepts, ascending. It returns a
// copy: a caller trimming or reordering the list must not reach the source.
func EffortLevels() []string {
	out := make([]string, 0, len(anthropicEffortLevels))
	for _, level := range anthropicEffortLevels {
		out = append(out, string(level))
	}
	return out
}

func normalizeEffort(level string) string { return strings.ToLower(strings.TrimSpace(level)) }

// newAgentHTTPClient is the client every Anthropic call runs through: the
// shared one (no timeout, streaming-safe) unless a transport is injected.
func newAgentHTTPClient(transport http.RoundTripper) *http.Client {
	if transport == nil {
		return llm.NewHTTPClient()
	}
	return &http.Client{Transport: transport}
}

func (c *anthropicAgentLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (outResult *llm.Result, outErr error) {
	// Every error leaving this package is normalized here rather than at each
	// return, so a path added later cannot leak a raw SDK error to Layer 0.
	defer func() { outErr = normalizeError(outErr) }()
	// The sink's end fires once per Execute, not once per request: a
	// continuation round is the same answer still being produced, and ending
	// between rounds made the UI treat the fragment as a finished reply.
	endSink := llm.StreamSinkFrom(ctx)
	if endSink != nil && endSink.OnEnd != nil {
		defer endSink.OnEnd()
	}
	nonStreaming := llm.NonStreamingRequestFrom(ctx)
	// conversation is what the next round sends: the caller's messages plus,
	// from the second round on, the recovery's resume pair. It is rebuilt from
	// the original slice each round — the accumulated answer replaces, never
	// stacks on, the previous round's fragment, so the request grows by a
	// constant, not by the round count.
	conversation := messages
	var recovery llm.OutputLimitRecovery
	var answer strings.Builder
	var lastToolCalls []llm.ToolCall
	var stopReason string
	var usage *llm.Usage
	var billedIn, billedOut, billedCacheWrite, billedCacheRead int
	var settledIn, settledCacheWrite, settledCacheRead int
	rounds := 0
	// replayTo is the model this call's thinking is replayed to, and "" replays
	// none. Once a provider refuses the replayed thinking it stays "" for the
	// rest of this Execute: the continuation rounds would carry the same blocks
	// and collect the same rejection, one extra request each.
	replayTo := c.model
	var lastRound *llm.Message
	for {
		params, err := c.messageParams(ctx, conversation, tools, replayTo)
		if err != nil {
			return nil, err
		}
		res, err := c.send(ctx, params, nonStreaming)
		if err != nil && replayTo != "" && anthropicParamsCarryThinking(params.Messages) && isThinkingRejection(err) {
			// Claude Code does the same: a provider that refuses the replayed
			// thinking gets the request again without it, rather than a
			// conversation that fails on every request from here on.
			slog.Warn("anthropic: provider refused the replayed thinking; resending without it", "model", c.model, "err", err)
			replayTo = ""
			if params, err = c.messageParams(ctx, conversation, tools, replayTo); err != nil {
				return nil, err
			}
			res, err = c.send(ctx, params, nonStreaming)
		}
		if err != nil {
			return nil, err
		}
		rounds++
		// Occupancy is the settled round's prompt side; the output is every
		// round's, because the model really did produce it and this Result's
		// answer carries it. The billing view sums every field across rounds.
		if res.Usage != nil {
			billedIn += res.Usage.InputTokens
			billedOut += res.Usage.OutputTokens
			billedCacheWrite += res.Usage.CacheCreationInputTokens
			billedCacheRead += res.Usage.CacheReadInputTokens
			settledIn = res.Usage.InputTokens
			settledCacheWrite = res.Usage.CacheCreationInputTokens
			settledCacheRead = res.Usage.CacheReadInputTokens
			usage = &llm.Usage{
				InputTokens:              settledIn,
				OutputTokens:             billedOut,
				CacheCreationInputTokens: settledCacheWrite,
				CacheReadInputTokens:     settledCacheRead,
			}
		}
		// Unconditional: a round that carries no message must not leave the
		// previous round's thinking on offer for the final answer.
		lastRound = res.Message
		if res.Message != nil {
			answer.WriteString(res.Message.TextContent())
			lastToolCalls = res.Message.ToolCalls
		}
		stopReason = res.StopReason
		if !recovery.ShouldContinue(stopReason, answer.Len(), len(lastToolCalls)) {
			break
		}
		conversation = append(append([]llm.Message(nil), messages...), recovery.Resume(answer.String())...)
	}
	// One assistant message carries the whole answer: each round was its own
	// request, but this call is one reply. Its thinking is the last round's —
	// the reasoning behind the calls it hands back. An earlier round's thinking
	// was never sent again in the rounds after it, and a round cut by the
	// output limit may hold a block cut before its signature arrived, which a
	// provider refuses on replay.
	parts := make([]llm.ContentPart, 0)
	if answer.Len() > 0 {
		parts = append(parts, llm.Text(answer.String()))
	}
	msg := &llm.Message{Role: llm.RoleAssistant, Parts: parts, ToolCalls: lastToolCalls}
	if lastRound != nil && !llm.OutputLimitReached(stopReason) {
		msg.Name, msg.ReasoningModel = lastRound.Name, lastRound.ReasoningModel
	}
	out := &llm.Result{
		Message:    msg,
		StopReason: stopReason,
		Usage:      usage,
	}
	if billedIn != settledIn || billedCacheWrite != settledCacheWrite || billedCacheRead != settledCacheRead {
		out.BilledUsage = &llm.Usage{
			InputTokens:              billedIn,
			OutputTokens:             billedOut,
			CacheCreationInputTokens: billedCacheWrite,
			CacheReadInputTokens:     billedCacheRead,
		}
	}
	return out, nil
}

// messageParams builds one Messages request for conversation. replayTo is the
// model the conversation's thinking is replayed to; "" replays none.
func (c *anthropicAgentLLM) messageParams(ctx context.Context, conversation []llm.Message, tools []*llm.Tool, replayTo string) (anthropicapi.MessageNewParams, error) {
	system, anthropicMessages, err := agentAnthropicMessagesToAPI(conversation, replayTo)
	if err != nil {
		return anthropicapi.MessageNewParams{}, err
	}
	params := anthropicapi.MessageNewParams{
		Model:     anthropicapi.Model(strings.TrimSpace(c.model)),
		MaxTokens: c.maxTokens,
		Messages:  anthropicMessages,
		System:    system,
	}
	if c.effort != "" {
		params.OutputConfig.Effort = c.effort
	}
	if len(tools) > 0 {
		params.Tools = agentAnthropicTools(tools)
	}
	// Must run after Tools is populated: the breakpoint on the system block
	// caches the tool definitions with it, so the tools have to be part of the
	// request the markers describe.
	ApplyPromptCache(params.System, params.Messages)
	return params, nil
}

// send runs one request: streamed, or as a single unary call when the caller
// asked for that.
func (c *anthropicAgentLLM) send(ctx context.Context, params anthropicapi.MessageNewParams, nonStreaming bool) (*llm.Result, error) {
	if nonStreaming {
		return c.executeUnary(ctx, params)
	}
	return c.executeStream(ctx, params)
}

// anthropicParamsCarryThinking reports whether a request replays any thinking.
func anthropicParamsCarryThinking(messages []anthropicapi.MessageParam) bool {
	for _, m := range messages {
		for _, b := range m.Content {
			if b.OfThinking != nil || b.OfRedactedThinking != nil {
				return true
			}
		}
	}
	return false
}

// isThinkingRejection reports a 400 that names the thinking or its signature —
// the provider refusing replayed blocks. A 400 that only mentions thinking for
// another reason costs one extra request that fails the same way.
//
// The judgment itself is llm.ReasoningReplayRefused, shared with the other three
// adapters: normalizeError's Message is the response body the SDK kept, which is
// what names the refused block.
func isThinkingRejection(err error) bool {
	return llm.ReasoningReplayRefused(normalizeError(err), []int{http.StatusBadRequest}, "thinking", "signature")
}

// executeStream runs one streaming Messages request and returns its result;
// thinking blocks stream to the sink as they arrive and also reach the message
// as a reasoning carry, which is how the next request returns them to the model
// that wrote them. It is one round of Execute's output-limit loop.
func (c *anthropicAgentLLM) executeStream(ctx context.Context, params anthropicapi.MessageNewParams) (*llm.Result, error) {
	ctx = llm.WithStreamingRequest(ctx)
	stream := c.client.Messages.NewStreaming(ctx, params)
	defer func() { _ = stream.Close() }()

	sink := llm.StreamSinkFrom(ctx)
	if err := stream.Err(); err != nil {
		return nil, err
	}
	if sink != nil && sink.Streamed != nil {
		*sink.Streamed = true
	}

	var res anthropicapi.Message
	thinkingBlocks := make(map[int64]bool)
	var reportedInput, reportedOutput int64
	// The stop reason the stream named, kept for Result.StopReason: the engine
	// reads it to tell "the model said nothing" from "the budget cut it off".
	stopReason := ""
	// A stream that ends without a single content block and without a stop
	// reason produced nothing at all: it is re-sent without streaming, not
	// finished as an empty answer.
	sawContentBlock := false
	sawStopReason := false
	// Tracked independently of res.Usage: anthropic-sdk-go's Message.Accumulate
	// only copies OutputTokens out of a MessageDeltaEvent (messageutil.go),
	// silently dropping InputTokens/CacheCreationInputTokens/CacheReadInputTokens
	// from res.Usage whenever a provider reports cache accounting there rather
	// than in message_start — confirmed against a real Anthropic-compatible
	// endpoint that only ever populates cache_read/cache_creation on
	// message_delta, never message_start. Providers are free to place these
	// fields on either event, so the fix tracks the latest value seen from the
	// raw event on both message_start and message_delta ourselves, rather than
	// trusting the SDK's accumulated Usage, which real production traffic can
	// silently zero out.
	var latestUsage anthropicapi.Usage
	// attemptUsage is the usage this attempt had already been billed when it
	// failed: nil until the first usage-bearing event (message_start, or a
	// message_delta carrying the final accounting) arrived, so a failure
	// from before that point keeps its original error — the provider never
	// billed that attempt.
	attemptUsage := func() *llm.Usage {
		if latestUsage.InputTokens <= 0 && latestUsage.OutputTokens <= 0 &&
			latestUsage.CacheCreationInputTokens <= 0 && latestUsage.CacheReadInputTokens <= 0 {
			return nil
		}
		return &llm.Usage{
			InputTokens:              int(latestUsage.InputTokens),
			OutputTokens:             int(latestUsage.OutputTokens),
			CacheCreationInputTokens: int(latestUsage.CacheCreationInputTokens),
			CacheReadInputTokens:     int(latestUsage.CacheReadInputTokens),
		}
	}
	responseStarted := false
	reportUsage := func(input, output, cacheCreationInput, cacheReadInput int64) {
		if sink == nil {
			return
		}
		// Anthropic reports uncached, cache-write, and cache-read input as
		// disjoint buckets. Context occupancy must include all three; using
		// input_tokens alone makes a heavily cached prompt look nearly empty.
		totalInput := input + cacheCreationInput + cacheReadInput
		var deltaInput, deltaOutput int64
		if totalInput > reportedInput {
			deltaInput = totalInput - reportedInput
			reportedInput = totalInput
		}
		if output > reportedOutput {
			deltaOutput = output - reportedOutput
			reportedOutput = output
		}
		if sink.OnUsage != nil && (deltaInput > 0 || deltaOutput > 0) {
			sink.OnUsage(int(deltaInput), int(deltaOutput))
		}
		if sink.OnUsageSnapshot != nil && (totalInput > 0 || output > 0) {
			sink.OnUsageSnapshot(int(totalInput), int(output))
		}
	}

	for stream.Next() {
		event := stream.Current()
		switch event.Type {
		case "message_start":
			latestUsage.InputTokens = event.Message.Usage.InputTokens
			latestUsage.CacheCreationInputTokens = event.Message.Usage.CacheCreationInputTokens
			latestUsage.CacheReadInputTokens = event.Message.Usage.CacheReadInputTokens
			if event.Message.Usage.OutputTokens > latestUsage.OutputTokens {
				latestUsage.OutputTokens = event.Message.Usage.OutputTokens
			}
			reportUsage(
				event.Message.Usage.InputTokens,
				event.Message.Usage.OutputTokens,
				event.Message.Usage.CacheCreationInputTokens,
				event.Message.Usage.CacheReadInputTokens,
			)
		case "message_delta":
			// message_delta's InputTokens/cache fields, when present, are the
			// authoritative final accounting for this response (input-side
			// stats do not change mid-response) — overwrite rather than add.
			if event.Usage.InputTokens > 0 {
				latestUsage.InputTokens = event.Usage.InputTokens
			}
			if event.Usage.CacheCreationInputTokens > 0 {
				latestUsage.CacheCreationInputTokens = event.Usage.CacheCreationInputTokens
			}
			if event.Usage.CacheReadInputTokens > 0 {
				latestUsage.CacheReadInputTokens = event.Usage.CacheReadInputTokens
			}
			if event.Usage.OutputTokens > latestUsage.OutputTokens {
				latestUsage.OutputTokens = event.Usage.OutputTokens
			}
			reportUsage(
				event.Usage.InputTokens,
				event.Usage.OutputTokens,
				event.Usage.CacheCreationInputTokens,
				event.Usage.CacheReadInputTokens,
			)
			if event.Delta.StopReason != "" {
				sawStopReason = true
				stopReason = string(event.Delta.StopReason)
				if sink != nil && sink.OnStopReason != nil {
					sink.OnStopReason(stopReason)
				}
			}
		case "content_block_start":
			sawContentBlock = true
			switch event.ContentBlock.Type {
			case "text":
				if event.ContentBlock.Text != "" {
					llm.NotifyResponseStarted(sink, &responseStarted)
				}
				if event.ContentBlock.Text != "" && sink != nil && sink.OnDelta != nil {
					sink.OnDelta(event.ContentBlock.Text)
				}
			case "thinking":
				thinkingBlocks[event.Index] = true
				if event.ContentBlock.Thinking != "" {
					llm.NotifyResponseStarted(sink, &responseStarted)
				}
				if event.ContentBlock.Thinking != "" && sink != nil && sink.OnReasoningDelta != nil {
					sink.OnReasoningDelta(event.ContentBlock.Thinking)
				}
			case "tool_use":
				// A call is model output like a word is: a response that
				// answers with calls alone has started at its first call.
				llm.NotifyResponseStarted(sink, &responseStarted)
			}
		case "content_block_delta":
			switch event.Delta.Type {
			case "text_delta":
				if event.Delta.Text != "" {
					llm.NotifyResponseStarted(sink, &responseStarted)
				}
				if event.Delta.Text != "" && sink != nil && sink.OnDelta != nil {
					sink.OnDelta(event.Delta.Text)
				}
			case "thinking_delta":
				if event.Delta.Thinking != "" {
					llm.NotifyResponseStarted(sink, &responseStarted)
				}
				if event.Delta.Thinking != "" && sink != nil && sink.OnReasoningDelta != nil {
					sink.OnReasoningDelta(event.Delta.Thinking)
				}
			}
		case "content_block_stop":
			if thinkingBlocks[event.Index] {
				delete(thinkingBlocks, event.Index)
				if sink != nil && sink.OnReasoningDone != nil {
					sink.OnReasoningDone()
				}
			}
		case "message_stop":
			if sink != nil && sink.OnMessageStop != nil {
				sink.OnMessageStop()
			}
		}
		if err := res.Accumulate(event); err != nil {
			return nil, llm.NewExecuteError(err, attemptUsage())
		}
		// Accumulate has filled the block's final state by now, so a call
		// reported ready here is byte-identical to the one the final message
		// carries.
		if event.Type == "content_block_stop" && int(event.Index) < len(res.Content) {
			switch res.Content[event.Index].Type {
			case "thinking", "redacted_thinking":
				// A thinking block is only replayable once it is closed: the
				// signature arrives with its stop, and a block without one is
				// refused. Every block in res.Content has had its stop by now,
				// so this is the moment the reply's reasoning is complete as it
				// stands — a call cut off from here on is finished from a
				// message that still carries it.
				publishReasoningCarry(sink, llm.EncodeThinking(anthropicThinkingBlocks(res.Content)), strings.TrimSpace(c.model))
			case "tool_use":
				if sink != nil && sink.OnToolCallReady != nil {
					sink.OnToolCallReady(anthropicToolCallFromBlock(res.Content[event.Index]))
				}
			case "text":
				if strings.TrimSpace(res.Content[event.Index].Text) != "" && sink != nil && sink.OnTextBlockDone != nil {
					sink.OnTextBlockDone()
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, llm.NewExecuteError(err, attemptUsage())
	}
	if !sawContentBlock && !sawStopReason {
		// The stream closed cleanly having delivered neither a content block
		// nor a stop reason: no answer started, none finished. The request is
		// re-sent without streaming rather than finished as an empty one.
		// The attempt was still billed when message_start carried usage, so
		// that billing rides the error into the retry loop's accounting.
		return nil, llm.NewExecuteError(fmt.Errorf("anthropic: %w", llm.ErrStreamNoEvents), attemptUsage())
	}
	msg, err := agentAnthropicMessageFromAPI(&res, c.model)
	if err != nil {
		return nil, err
	}
	return &llm.Result{
		Message:    msg,
		StopReason: llm.NormalizeStopReason(stopReason),
		Usage: &llm.Usage{
			InputTokens:              int(latestUsage.InputTokens),
			OutputTokens:             int(latestUsage.OutputTokens),
			CacheCreationInputTokens: int(latestUsage.CacheCreationInputTokens),
			CacheReadInputTokens:     int(latestUsage.CacheReadInputTokens),
		},
	}, nil
}

// executeUnary runs the same request as one non-streaming Messages call under
// the non-streaming deadline and replays the whole response to the stream
// sink in stream order, so every consumer sees it as it would a streamed one.
// The sink's OnEnd is not fired here: Execute owns it for the whole
// continuation loop.
func (c *anthropicAgentLLM) executeUnary(ctx context.Context, params anthropicapi.MessageNewParams) (*llm.Result, error) {
	ctx, cancel := llm.WithNonStreamingDeadline(ctx)
	defer cancel()
	sink := llm.StreamSinkFrom(ctx)
	// The SDK refuses non-streaming calls whose maxTokens imply more than
	// ten minutes of work unless the caller states a request timeout;
	// NonStreamingTimeout is the same bound WithNonStreamingDeadline already
	// puts on this attempt, so stating it explicitly replaces the SDK's
	// estimate.
	msg, err := c.client.Messages.New(ctx, params, option.WithRequestTimeout(llm.NonStreamingTimeout))
	if err != nil {
		return nil, llm.NonStreamingError(ctx, err)
	}
	message, err := agentAnthropicMessageFromAPI(msg, c.model)
	if err != nil {
		return nil, err
	}
	result := &llm.Result{
		Message: message,
		// Why the provider stopped: normalized so the engine can tell a
		// cut-off response (StopReasonOutputLimit) from a chosen one.
		StopReason: llm.NormalizeStopReason(string(msg.StopReason)),
		// The same accounting the streaming path keeps from the raw events:
		// input plus both cache buckets, output as reported.
		Usage: &llm.Usage{
			InputTokens:              int(msg.Usage.InputTokens),
			OutputTokens:             int(msg.Usage.OutputTokens),
			CacheCreationInputTokens: int(msg.Usage.CacheCreationInputTokens),
			CacheReadInputTokens:     int(msg.Usage.CacheReadInputTokens),
		},
	}
	if sink != nil {
		totalInput := result.Usage.InputTokens + result.Usage.CacheCreationInputTokens + result.Usage.CacheReadInputTokens
		if totalInput > 0 || result.Usage.OutputTokens > 0 {
			if sink.OnUsage != nil {
				sink.OnUsage(totalInput, result.Usage.OutputTokens)
			}
			if sink.OnUsageSnapshot != nil {
				sink.OnUsageSnapshot(totalInput, result.Usage.OutputTokens)
			}
		}
	}
	var reasoning, texts []string
	for _, b := range msg.Content {
		switch b.Type {
		case "thinking":
			reasoning = append(reasoning, b.Thinking)
		case "text":
			texts = append(texts, b.Text)
		}
	}
	// The whole reply is finished here, so its carry is complete: hand it over
	// before the replay, whose tool-call callbacks are what can start a
	// provisional message for this reply.
	publishReasoningCarry(sink, message.Name, message.ReasoningModel)
	llm.ReplayToSink(sink, llm.ReplayedResponse{
		Reasoning:  strings.Join(reasoning, ""),
		Text:       texts,
		ToolCalls:  result.Message.ToolCalls,
		StopReason: string(msg.StopReason),
	})
	return result, nil
}

func agentAnthropicMessagesToAPI(messages []llm.Message, replayTo string) ([]anthropicapi.TextBlockParam, []anthropicapi.MessageParam, error) {
	var systemParts []string
	out := make([]anthropicapi.MessageParam, 0, len(messages))
	// appendUser adds blocks to the conversation as user content, merging them
	// into the user message that is already last. A tool round's results belong
	// in one user message — the API pairs the assistant turn with exactly the
	// one user turn after it — and steering and reminders follow those results
	// in the same turn. The official endpoint merges consecutive user turns on
	// its own, so this changes nothing there; a compatible gateway keeps the
	// pairing and drops the extra messages, which is how results went missing.
	appendUser := func(blocks []anthropicapi.ContentBlockParamUnion) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == anthropicapi.MessageParamRoleUser {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			return
		}
		out = append(out, anthropicapi.NewUserMessage(blocks...))
	}
	for _, m := range messages {
		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			text := m.TextContent()
			if strings.TrimSpace(text) != "" {
				systemParts = append(systemParts, text)
			}
		case llm.RoleUser:
			appendUser(agentAnthropicContentBlocks(m.Parts))
		case llm.RoleAssistant:
			// A reply that was only reasoning goes back to the model that
			// produced it, and to no other: the fence would hand that model's
			// reasoning to another one as an answer this conversation gave.
			// A retry that replays no reasoning at all (replayTo empty) drops
			// it too, for the same reason it replays no thinking.
			if llm.ThinkingFenceOnly(m) && !llm.ThinkingFenceGoesTo(m, replayTo) {
				continue
			}
			// Thinking comes first because the model thought before it wrote,
			// and because the provider checks the blocks in the order they
			// were returned.
			thinking := anthropicThinkingParams(m, replayTo)
			blocks := thinking
			for _, p := range m.Parts {
				if p.Type == llm.ContentTypeText && strings.TrimSpace(p.Text) != "" {
					blocks = append(blocks, anthropicapi.NewTextBlock(p.Text))
				}
			}
			for _, tc := range m.ToolCalls {
				id := strings.TrimSpace(tc.ID)
				if id == "" {
					id = uuid.NewString()
				}
				var input any
				args := strings.TrimSpace(tc.Function.Arguments)
				if args == "" || !json.Valid([]byte(args)) {
					// Malformed JSON arguments (e.g. model emitted a premature
					// closing brace) must not abort the entire LLM request.
					// Fall back to an empty object so the provider accepts the
					// message; the tool handler already reported a parse error
					// for this call.
					input = map[string]any{}
				} else {
					if err := json.Unmarshal([]byte(args), &input); err != nil {
						input = map[string]any{}
					}
				}
				blocks = append(blocks, anthropicapi.NewToolUseBlock(id, input, tc.Function.Name))
			}
			// An assistant message with nothing this protocol can express
			// contributes nothing and must not be sent: an empty content array
			// is a request error. Thinking does not count as something: it is
			// context for the answer, and a message that holds only thinking is
			// the message Claude Code drops, whose last block it would leave
			// the thinking as.
			if len(blocks) == len(thinking) {
				continue
			}
			out = append(out, anthropicapi.NewAssistantMessage(blocks...))
		case llm.RoleTool:
			toolUseID := strings.TrimSpace(m.ToolCallID)
			if toolUseID == "" {
				return nil, nil, errAnthropicToolMessageMissingToolCallID
			}
			content := agentAnthropicToolResultContent(m.Parts)
			toolBlock := anthropicapi.ToolResultBlockParam{
				ToolUseID: toolUseID,
				Content:   content,
			}
			appendUser([]anthropicapi.ContentBlockParamUnion{{OfToolResult: &toolBlock}})
		default:
			return nil, nil, &anthropicUnsupportedRoleError{Role: m.Role}
		}
	}
	joined := strings.TrimSpace(strings.Join(systemParts, "\n\n"))
	var system []anthropicapi.TextBlockParam
	if joined != "" {
		system = []anthropicapi.TextBlockParam{{Text: joined}}
	}
	return system, out, nil
}

// anthropicThinkingParams renders the thinking m carries for replayTo, in the
// order the model wrote it, or nothing. Only the model that produced the
// blocks gets them back: their signatures are bound to it. A carry recorded
// without a producer is not replayed either — this format never existed
// without one.
func anthropicThinkingParams(m llm.Message, replayTo string) []anthropicapi.ContentBlockParamUnion {
	if strings.TrimSpace(replayTo) == "" || strings.TrimSpace(m.ReasoningModel) == "" || !llm.ReplaysReasoningTo(m, replayTo) {
		return nil
	}
	blocks, ok := llm.DecodeThinking(m.Name)
	if !ok {
		return nil
	}
	out := make([]anthropicapi.ContentBlockParamUnion, 0, len(blocks))
	for _, b := range blocks {
		if b.Redacted {
			out = append(out, anthropicapi.NewRedactedThinkingBlock(b.Data))
			continue
		}
		out = append(out, anthropicapi.NewThinkingBlock(b.Signature, b.Thinking))
	}
	return out
}

func agentAnthropicContentBlocks(parts []llm.ContentPart) []anthropicapi.ContentBlockParamUnion {
	blocks := make([]anthropicapi.ContentBlockParamUnion, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case llm.ContentTypeText:
			if strings.TrimSpace(p.Text) != "" {
				blocks = append(blocks, anthropicapi.NewTextBlock(p.Text))
			}
		case llm.ContentTypeImageURL:
			blocks = append(blocks, anthropicapi.NewImageBlock(
				anthropicapi.URLImageSourceParam{URL: p.ImageURL},
			))
		case llm.ContentTypeImageBase64:
			blocks = append(blocks, anthropicapi.NewImageBlock(
				anthropicapi.Base64ImageSourceParam{
					Data:      p.ImageBase64,
					MediaType: anthropicapi.Base64ImageSourceMediaType(p.MIMEType),
				},
			))
		}
	}
	return blocks
}

func agentAnthropicToolResultContent(parts []llm.ContentPart) []anthropicapi.ToolResultBlockParamContentUnion {
	content := make([]anthropicapi.ToolResultBlockParamContentUnion, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case llm.ContentTypeText:
			text := p.Text
			content = append(content, anthropicapi.ToolResultBlockParamContentUnion{
				OfText: &anthropicapi.TextBlockParam{Text: text},
			})
		case llm.ContentTypeImageURL:
			imgBlock := anthropicapi.ImageBlockParam{
				Source: anthropicapi.ImageBlockParamSourceUnion{
					OfURL: &anthropicapi.URLImageSourceParam{URL: p.ImageURL},
				},
			}
			content = append(content, anthropicapi.ToolResultBlockParamContentUnion{
				OfImage: &imgBlock,
			})
		case llm.ContentTypeImageBase64:
			imgBlock := anthropicapi.ImageBlockParam{
				Source: anthropicapi.ImageBlockParamSourceUnion{
					OfBase64: &anthropicapi.Base64ImageSourceParam{
						Data:      p.ImageBase64,
						MediaType: anthropicapi.Base64ImageSourceMediaType(p.MIMEType),
					},
				},
			}
			content = append(content, anthropicapi.ToolResultBlockParamContentUnion{
				OfImage: &imgBlock,
			})
		}
	}
	return content
}

// anthropicToolCallFromBlock converts one accumulated tool_use block into the
// engine's representation. The mid-stream OnToolCallReady callbacks and the
// final message both go through it, so a call reported ready is byte-identical
// to the one the Result carries.
func anthropicToolCallFromBlock(b anthropicapi.ContentBlockUnion) llm.ToolCall {
	args := strings.TrimSpace(string(b.Input))
	if args == "" {
		args = "{}"
	}
	return llm.ToolCall{
		ID:   b.ID,
		Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{
			Name:      b.Name,
			Arguments: args,
		},
	}
}

// publishReasoningCarry hands the reply's reasoning, as far as it is complete
// and safe to replay, to the sink. It does nothing when nobody listens or there
// is no carry yet.
func publishReasoningCarry(sink *llm.StreamSink, carry, model string) {
	if sink == nil || sink.OnReasoningCarry == nil || carry == "" {
		return
	}
	sink.OnReasoningCarry(carry, model)
}

// anthropicThinkingBlocks collects the reply's thinking and redacted_thinking
// blocks in the order the model returned them, byte for byte.
func anthropicThinkingBlocks(content []anthropicapi.ContentBlockUnion) []llm.ThinkingBlock {
	thinking := make([]llm.ThinkingBlock, 0, len(content))
	for _, b := range content {
		switch b.Type {
		case "thinking":
			thinking = append(thinking, llm.ThinkingBlock{Thinking: b.Thinking, Signature: b.Signature})
		case "redacted_thinking":
			thinking = append(thinking, llm.ThinkingBlock{Redacted: true, Data: b.Data})
		}
	}
	return thinking
}

func agentAnthropicMessageFromAPI(m *anthropicapi.Message, model string) (*llm.Message, error) {
	if m == nil {
		return nil, &anthropicNilResponseError{}
	}
	parts := make([]llm.ContentPart, 0)
	toolCalls := make([]llm.ToolCall, 0)
	thinking := anthropicThinkingBlocks(m.Content)
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				parts = append(parts, llm.Text(b.Text))
			}
		case "tool_use":
			toolCalls = append(toolCalls, anthropicToolCallFromBlock(b))
		}
	}
	msg := &llm.Message{
		Role:      llm.RoleAssistant,
		Parts:     parts,
		ToolCalls: toolCalls,
	}
	// The thinking rides the reasoning carry, so the next request gives this
	// model its own reasoning back (agentAnthropicMessagesToAPI). Without it a
	// thinking model saw only its calls and their results, never why it made
	// them, and re-derived the same plan on every step of a tool loop.
	if carry := llm.EncodeThinking(thinking); carry != "" {
		msg.Name = carry
		msg.ReasoningModel = strings.TrimSpace(model)
	}
	return msg, nil
}

func agentAnthropicTools(tools []*llm.Tool) []anthropicapi.ToolUnionParam {
	out := make([]anthropicapi.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		if t == nil {
			continue
		}
		inputSchema := agentAnthropicToolInputSchema(t.InputSchema())
		tool := anthropicapi.ToolParam{
			Name:        t.Name(),
			Description: anthropicapi.String(t.Description()),
			// Tool arguments stay non-strict so optional fields remain
			// optional (see compatOpenAITools).
			Strict:      anthropicapi.Bool(false),
			InputSchema: inputSchema,
		}
		out = append(out, anthropicapi.ToolUnionParam{OfTool: &tool})
	}
	return out
}

func agentAnthropicToolInputSchema(schema map[string]any) anthropicapi.ToolInputSchemaParam {
	if schema == nil {
		return anthropicapi.ToolInputSchemaParam{Properties: map[string]any{}}
	}
	properties := schema["properties"]
	required := make([]string, 0)
	if raw, ok := schema["required"]; ok {
		switch v := raw.(type) {
		case []string:
			required = append(required, v...)
		case []any:
			for _, it := range v {
				s, ok := it.(string)
				if ok {
					required = append(required, s)
				}
			}
		}
	}
	extra := make(map[string]any)
	for k, v := range schema {
		if k == "type" || k == "properties" || k == "required" {
			continue
		}
		extra[k] = v
	}
	return anthropicapi.ToolInputSchemaParam{
		Properties:  properties,
		Required:    required,
		ExtraFields: extra,
	}
}

var _ llm.LLM = (*anthropicAgentLLM)(nil)
