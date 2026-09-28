package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	anthropicapi "github.com/anthropics/anthropic-sdk-go"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// Anthropic requires tool_use_id on every tool result. A tool message that lost
// its call id has to fail the conversion: sending it anyway is rejected by the
// API with an opaque "must be a response to a preceding message with
// tool_calls", far from the code that dropped the id.
//
// This and the test below moved here with the error values themselves, when the
// unused Anthropic compatibility package that had defined them — and had
// carried the only coverage of these branches — was deleted.
func TestAnthropicConversionRejectsToolMessageWithoutToolCallID(t *testing.T) {
	_, _, err := agentAnthropicMessagesToAPI([]llm.Message{
		{Role: llm.RoleTool, Parts: []llm.ContentPart{llm.Text("result")}},
	})
	if !errors.Is(err, errAnthropicToolMessageMissingToolCallID) {
		t.Fatalf("err = %v, want errAnthropicToolMessageMissingToolCallID", err)
	}
}

// An unknown role must name itself in the error. Silently dropping the message
// would send a transcript with a hole in it, which surfaces later as a model
// answering a question it was never shown.
func TestAnthropicConversionReportsUnsupportedRole(t *testing.T) {
	_, _, err := agentAnthropicMessagesToAPI([]llm.Message{
		{Role: "moderator", Parts: []llm.ContentPart{llm.Text("hello")}},
	})
	var unsupported *anthropicUnsupportedRoleError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %T (%v), want *anthropicUnsupportedRoleError", err, err)
	}
	if unsupported.Role != "moderator" {
		t.Fatalf("Role = %q, want the offending role", unsupported.Role)
	}
}

// The supported roles all convert: system and developer collapse into the
// system block, everything else becomes a message.
func TestAnthropicConversionSplitsSystemFromConversation(t *testing.T) {
	system, messages, err := agentAnthropicMessagesToAPI([]llm.Message{
		llm.SystemMessage("you are forebrain"),
		{Role: llm.RoleDeveloper, Parts: []llm.ContentPart{llm.Text("remember this")}},
		llm.UserMessage(llm.Text("hello")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(system) != 1 {
		t.Fatalf("system blocks = %d, want system and developer joined into one", len(system))
	}
	if system[0].Text != "you are forebrain\n\nremember this" {
		t.Fatalf("system text = %q", system[0].Text)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want only the user turn", len(messages))
	}
}

func cachedBlockPositions(messages []anthropicapi.MessageParam) []int {
	position := 0
	var marked []int
	for _, message := range messages {
		for _, block := range message.Content {
			control := block.GetCacheControl()
			if control != nil && control.Type == "ephemeral" {
				marked = append(marked, position)
			}
			position++
		}
	}
	return marked
}

func anthropicTextTurns(count int) []anthropicapi.MessageParam {
	messages := make([]anthropicapi.MessageParam, 0, count)
	for range count {
		messages = append(messages, anthropicapi.NewUserMessage(anthropicapi.NewTextBlock("turn")))
	}
	return messages
}

// Anthropic caching is opt-in: a request with no cache_control re-bills the
// system prompt, the tool definitions, and the entire transcript at full input
// price on every single call.
func TestAnthropicPromptCacheMarksSystemAndLatestTurn(t *testing.T) {
	system := []anthropicapi.TextBlockParam{{Text: "system prompt"}}
	messages := anthropicTextTurns(3)

	ApplyPromptCache(system, messages)

	if system[len(system)-1].CacheControl.Type != "ephemeral" {
		t.Fatal("system block carries no cache_control; tools and system are re-billed every call")
	}
	// An interactive session pauses for approvals and for the user reading a
	// diff; the five-minute default would go cold across exactly those gaps.
	if ttl := system[len(system)-1].CacheControl.TTL; ttl != anthropicapi.CacheControlEphemeralTTLTTL1h {
		t.Fatalf("system cache TTL = %q, want the one-hour TTL", ttl)
	}
	marked := cachedBlockPositions(messages)
	if len(marked) != 1 || marked[0] != 2 {
		t.Fatalf("conversation breakpoints at %v, want one on the final block", marked)
	}
}

// Each breakpoint searches back at most 20 content blocks for an existing cache
// entry. A single agentic turn routinely appends more than that in
// tool_use/tool_result pairs, so a lone breakpoint at the end would find
// nothing from the previous request and silently miss the whole prefix.
func TestAnthropicPromptCacheSpacesBreakpointsWithinLookbackWindow(t *testing.T) {
	const lookback = 20
	system := []anthropicapi.TextBlockParam{{Text: "system prompt"}}
	messages := anthropicTextTurns(60)

	ApplyPromptCache(system, messages)

	marked := cachedBlockPositions(messages)
	if len(marked) == 0 {
		t.Fatal("no conversation breakpoints placed")
	}
	if last := marked[len(marked)-1]; last != 59 {
		t.Fatalf("last breakpoint at block %d, want the final block", last)
	}
	for i := 1; i < len(marked); i++ {
		if gap := marked[i] - marked[i-1]; gap > lookback {
			t.Fatalf("breakpoints %d and %d are %d blocks apart, beyond the %d-block lookback",
				marked[i-1], marked[i], gap, lookback)
		}
	}
}

// The API rejects a request carrying more than four cache_control markers, so
// the budget is a hard ceiling and the system block spends one of the four.
func TestAnthropicPromptCacheStaysWithinBreakpointBudget(t *testing.T) {
	system := []anthropicapi.TextBlockParam{{Text: "system prompt"}}
	messages := anthropicTextTurns(500)

	ApplyPromptCache(system, messages)

	marked := cachedBlockPositions(messages)
	if len(marked) > BreakpointBudget-1 {
		t.Fatalf("%d conversation breakpoints, want at most %d with one spent on system",
			len(marked), BreakpointBudget-1)
	}
	if total := len(marked) + 1; total > BreakpointBudget {
		t.Fatalf("%d total breakpoints exceeds the API cap of %d", total, BreakpointBudget)
	}
}

// Tool results are the bulk of an agentic transcript. If a breakpoint could
// only land on a text block, the markers would bunch up at the few text blocks
// and leave gaps far wider than the lookback window.
func TestAnthropicPromptCacheMarksToolResultBlocks(t *testing.T) {
	system := []anthropicapi.TextBlockParam{{Text: "system prompt"}}
	messages := []anthropicapi.MessageParam{
		anthropicapi.NewUserMessage(anthropicapi.NewTextBlock("do the thing")),
		anthropicapi.NewUserMessage(anthropicapi.ContentBlockParamUnion{
			OfToolResult: &anthropicapi.ToolResultBlockParam{ToolUseID: "toolu_1"},
		}),
	}

	ApplyPromptCache(system, messages)

	if marked := cachedBlockPositions(messages); len(marked) != 1 || marked[0] != 1 {
		t.Fatalf("conversation breakpoints at %v, want one on the trailing tool result", marked)
	}
}

// With no system block the whole budget belongs to the conversation; nothing
// may be silently dropped.
func TestAnthropicPromptCacheHandlesMissingSystemBlock(t *testing.T) {
	messages := anthropicTextTurns(100)

	ApplyPromptCache(nil, messages)

	marked := cachedBlockPositions(messages)
	if len(marked) != BreakpointBudget {
		t.Fatalf("%d breakpoints without a system block, want the full budget of %d",
			len(marked), BreakpointBudget)
	}
}

// The unit tests above cover placement; this one pins what actually goes on the
// wire, because a marker that never reaches the request buys nothing. It also
// guards the sampling parameters: every Anthropic model from Opus 4.7 on
// rejects temperature outright, and the ones that still accept it reject any
// non-default value, so forebrain must send none.
func TestAnthropicRequestCarriesCacheControlAndNoSamplingParams(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")
	bodies := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		bodies <- body
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		writeAnthropicSSEEvent(t, w, "message_start", `{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"claude-opus-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`)
		writeAnthropicSSEEvent(t, w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`)
		writeAnthropicSSEEvent(t, w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		writeAnthropicSSEEvent(t, w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":1,"output_tokens":1}}`)
		writeAnthropicSSEEvent(t, w, "message_stop", `{"type":"message_stop"}`)
	}))
	t.Cleanup(server.Close)

	client := NewAgentLLM("test-key", server.URL, "claude-opus-4-6", 1024)
	if _, err := client.Execute(context.Background(), []llm.Message{
		llm.SystemMessage("you are forebrain"),
		llm.UserMessage(llm.Text("hello")),
	}, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	body := <-bodies
	if _, ok := body["temperature"]; ok {
		t.Fatalf("request carries temperature; current Anthropic models reject it: %v", body["temperature"])
	}
	for _, key := range []string{"top_p", "top_k"} {
		if _, ok := body[key]; ok {
			t.Fatalf("request carries %s; current Anthropic models reject it", key)
		}
	}

	systemBlocks, _ := body["system"].([]any)
	if len(systemBlocks) == 0 {
		t.Fatalf("no system blocks in request: %v", body["system"])
	}
	last, _ := systemBlocks[len(systemBlocks)-1].(map[string]any)
	control, _ := last["cache_control"].(map[string]any)
	if control["type"] != "ephemeral" {
		t.Fatalf("system block cache_control = %v, want an ephemeral breakpoint", last["cache_control"])
	}
	if control["ttl"] != "1h" {
		t.Fatalf("system block cache TTL = %v, want 1h", control["ttl"])
	}

	messages, _ := body["messages"].([]any)
	if len(messages) == 0 {
		t.Fatal("no messages in request")
	}
	lastMessage, _ := messages[len(messages)-1].(map[string]any)
	blocks, _ := lastMessage["content"].([]any)
	if len(blocks) == 0 {
		t.Fatalf("no content blocks in the final message: %v", lastMessage)
	}
	lastBlock, _ := blocks[len(blocks)-1].(map[string]any)
	if _, ok := lastBlock["cache_control"]; !ok {
		t.Fatalf("final content block carries no cache_control: %v", lastBlock)
	}
}

func cachePositions(messages []anthropicapi.MessageParam) []int {
	var out []int
	pos := 0
	for _, msg := range messages {
		for _, block := range msg.Content {
			if control := block.GetCacheControl(); control != nil && control.Type == "ephemeral" {
				out = append(out, pos)
			}
			pos++
		}
	}
	return out
}

func textMessages(n int) []anthropicapi.MessageParam {
	msgs := make([]anthropicapi.MessageParam, 0, n)
	for range n {
		msgs = append(msgs, anthropicapi.NewUserMessage(anthropicapi.NewTextBlock("turn")))
	}
	return msgs
}

func TestApplyPromptCacheLayout(t *testing.T) {
	system := []anthropicapi.TextBlockParam{{Text: "system"}}
	msgs := textMessages(60)

	ApplyPromptCache(system, msgs)

	if got := system[0].CacheControl; got.Type != "ephemeral" || got.TTL != anthropicapi.CacheControlEphemeralTTLTTL1h {
		t.Fatalf("system marker = %#v", got)
	}
	marked := cachePositions(msgs)
	if len(marked) == 0 || marked[len(marked)-1] != 59 {
		t.Fatalf("markers = %v", marked)
	}
	if len(marked)+1 > BreakpointBudget {
		t.Fatalf("marker count = %d, budget = %d", len(marked)+1, BreakpointBudget)
	}
	for i := 1; i < len(marked); i++ {
		if gap := marked[i] - marked[i-1]; gap != BlockStride {
			t.Fatalf("marker gap = %d, want %d", gap, BlockStride)
		}
	}
}

func TestApplyPromptCacheUsesFullBudgetWithoutSystem(t *testing.T) {
	msgs := textMessages(100)
	ApplyPromptCache(nil, msgs)
	if got := len(cachePositions(msgs)); got != BreakpointBudget {
		t.Fatalf("marker count = %d, want %d", got, BreakpointBudget)
	}
}

func TestApplyPromptCacheMarksToolResult(t *testing.T) {
	system := []anthropicapi.TextBlockParam{{Text: "system"}}
	msgs := []anthropicapi.MessageParam{
		anthropicapi.NewUserMessage(anthropicapi.NewTextBlock("go")),
		anthropicapi.NewUserMessage(anthropicapi.ContentBlockParamUnion{
			OfToolResult: &anthropicapi.ToolResultBlockParam{ToolUseID: "toolu_1"},
		}),
	}
	ApplyPromptCache(system, msgs)
	if got := cachePositions(msgs); len(got) != 1 || got[0] != 1 {
		t.Fatalf("markers = %v", got)
	}
}

// TestPromptCacheConstantsMatchTheAPILimits pins the two numbers the whole
// caching scheme rests on, because every other test in this package compares
// behaviour *against* these constants and would stay green if they changed.
// TestApplyPromptCacheLayout asserts "gap == BlockStride", so setting
// BlockStride to 25 keeps the suite passing while every request past the first
// silently misses its prefix — the exact failure that is most expensive here
// and least visible, since nothing errors and only the bill moves.
//
// BreakpointBudget is Anthropic's hard cap: a request carrying more than four
// cache_control markers is rejected outright.
//
// BlockStride has to stay strictly under the lookback window, with margin. A
// breakpoint searches back about twenty content blocks for an existing cache
// entry, and blocks that carry no cache_control field at all still consume
// that window, so a stride at or near twenty would intermittently find nothing
// and re-bill the whole prefix.
func TestPromptCacheConstantsMatchTheAPILimits(t *testing.T) {
	if BreakpointBudget != 4 {
		t.Errorf("BreakpointBudget = %d, want 4: Anthropic rejects a request with more than four cache_control markers", BreakpointBudget)
	}
	const lookback = 20
	if BlockStride >= lookback {
		t.Errorf("BlockStride = %d, want strictly less than the %d-block lookback: "+
			"consecutive breakpoints this far apart cannot see each other, so each request re-bills the prefix",
			BlockStride, lookback)
	}
	if BlockStride > lookback*3/4 {
		t.Errorf("BlockStride = %d leaves too little margin under the %d-block lookback; "+
			"blocks carrying no cache_control still consume the window", BlockStride, lookback)
	}
	if BlockStride < 1 {
		t.Errorf("BlockStride = %d, want at least 1", BlockStride)
	}
}
