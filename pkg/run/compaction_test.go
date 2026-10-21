package run

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

func TestCompactModelLimitDoesNotRequireSnapshot(t *testing.T) {
	const provider, model = "openai", "gpt-5.6-sol"
	hit, ok := llm.Lookup(provider, model)
	if !ok {
		t.Fatal("test model missing from catalog")
	}
	for _, tc := range []struct {
		name, provider, model string
		snapshot              json.RawMessage
		want                  int
	}{
		{name: "catalog without snapshot", provider: provider, model: model, want: hit.Limits().EffectiveInputLimit()},
		{name: "current model overrides stale snapshot", provider: provider, model: model, snapshot: json.RawMessage(`{"effective_input_tokens":9999999}`), want: hit.Limits().EffectiveInputLimit()},
		{name: "snapshot fallback", model: "uncatalogued-compact-test", snapshot: json.RawMessage(`{"effective_input_tokens":12345}`), want: 12345},
		{name: "same fallback as pre-turn", model: "uncatalogued-compact-test", want: 200000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := compactModelLimit(tc.snapshot, tc.provider, tc.model); got != tc.want {
				t.Fatalf("window=%d want %d", got, tc.want)
			}
		})
	}
}

// Exercise the real recovery and tool-loop wrappers together: a turn must
// compact as tool results grow, even when no pre-turn snapshot was published.
func TestOrchestrationAutoCompactsDuringTurnWithoutSnapshot(t *testing.T) {
	for _, snapshot := range []struct {
		name string
		read func(context.Context) (json.RawMessage, bool)
	}{
		{name: "no reader"},
		{name: "missing", read: func(context.Context) (json.RawMessage, bool) { return nil, false }},
		{name: "no model limits", read: func(context.Context) (json.RawMessage, bool) {
			return json.RawMessage(`{"budget":{"used_tokens":10}}`), true
		}},
		{name: "valid snapshot", read: func(context.Context) (json.RawMessage, bool) {
			return json.RawMessage(`{"effective_input_tokens":1000}`), true
		}},
	} {
		t.Run(snapshot.name, func(t *testing.T) {
			st := tool.NewState(t.TempDir())
			st.RegisterToolMeta(event.ToolMeta{Name: "read_chunk", ReadOnly: true, ConcurrencySafe: true})
			readTool, err := llm.NewTool("read_chunk", "read a chunk", func(context.Context, *struct{}) (string, error) {
				return strings.Repeat("large tool result\n", 300), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			compactions := 0
			inner := &growingCompactTestLLM{t: t, compactions: &compactions}
			recoverable := WrapRecoverableLLM(inner, snapshot.read, &CompactChainDeps{
				ExplicitLimit: 500,
				TryCompact: func(_ context.Context, messages []llm.Message, _ []*llm.Tool, _ bool) ([]llm.Message, bool, error) {
					compactions++
					if messages[len(messages)-1].Role != llm.RoleTool {
						t.Fatal("compact must see the completed tool result")
					}
					return []llm.Message{llm.UserMessage(llm.Text("checkpoint"))}, true, nil
				},
			})
			ctx := tool.WithRunID(context.Background(), "one-continuous-turn")
			res, err := wrapToolOrchestrationLLM(recoverable, st).Execute(ctx,
				[]llm.Message{llm.UserMessage(llm.Text("start"))}, []*llm.Tool{readTool})
			if err != nil {
				t.Fatal(err)
			}
			if compactions != 2 || inner.calls != 3 {
				t.Fatalf("compactions=%d calls=%d, want 2 compactions within 3 calls of one turn", compactions, inner.calls)
			}
			if len(res.Session) != 2 || res.Session[0].TextContent() != "checkpoint" || res.Session[1].TextContent() != "done" {
				t.Fatalf("live session did not adopt the last checkpoint: %v", roles(res.Session))
			}
		})
	}
}

type growingCompactTestLLM struct {
	t           *testing.T
	calls       int
	compactions *int
}

func (m *growingCompactTestLLM) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	if *m.compactions != m.calls-1 {
		m.t.Fatalf("call %d: compactions=%d, want %d before sending the next request", m.calls, *m.compactions, m.calls-1)
	}
	if m.calls > 1 && (len(messages) != 1 || messages[0].TextContent() != "checkpoint") {
		m.t.Fatalf("call %d still contains uncompacted history", m.calls)
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	if m.calls < 3 {
		msg = llm.AssistantMessage(nil, llm.ToolCall{ID: "chunk", Type: llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "read_chunk", Arguments: `{}`}})
	}
	return &llm.Result{Message: &msg}, nil
}

// compactingInnerLLM simulates recoverableLLM: on its first call it "compacts"
// the model context (records a small replacement history via the adoption sink)
// and emits a tool call; on later calls it finishes. It returns the message
// slice it was handed so the test can confirm which history the orchestration
// forwarded.
type compactingInnerLLM struct {
	compacted []llm.Message
	calls     int
	sawOnCall [][]llm.Message
}

func (m *compactingInnerLLM) Execute(ctx context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	m.sawOnCall = append(m.sawOnCall, append([]llm.Message(nil), messages...))
	switch m.calls {
	case 1:
		// Mid-turn compaction replaces the (large) input with a tiny handoff base.
		recordCompactionAdoption(ctx, m.compacted)
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-note", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "note", Arguments: `{}`}},
		)
		return &llm.Result{Message: &msg}, nil
	default:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
		return &llm.Result{Message: &msg}, nil
	}
}

// TestOrchestrationAdoptsCompactedSession is the regression guard for the
// auto-compact "new round re-inflates" bug: after a mid-turn compaction the
// orchestration's Result.Session (which is what the transcript persister writes)
// must be the compacted history plus only the messages produced afterwards —
// never the pre-compaction history, and never a stray handoff summary.
func TestOrchestrationAdoptsCompactedSession(t *testing.T) {
	st := tool.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "note", ReadOnly: true, ConcurrencySafe: true})

	noteTool, err := llm.NewTool("note", "note", func(context.Context, *struct{}) (string, error) {
		return "noted", nil
	})
	if err != nil {
		t.Fatalf("note tool: %v", err)
	}

	summary := llm.UserMessage(llm.Text(codetoolsSummaryText()))
	inner := &compactingInnerLLM{compacted: []llm.Message{summary}}
	wrapped := wrapToolOrchestrationLLM(inner, st)

	// A large pre-compaction input the orchestration would otherwise keep growing.
	input := []llm.Message{
		llm.SystemMessage("sys"),
		llm.UserMessage(llm.Text("old-1")),
		llm.UserMessage(llm.Text("old-2")),
		llm.UserMessage(llm.Text("old-3")),
	}
	res, err := wrapped.Execute(context.Background(), input, []*llm.Tool{noteTool})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res == nil {
		t.Fatalf("nil result")
	}
	if inner.calls != 2 {
		t.Fatalf("expected 2 inner calls, got %d", inner.calls)
	}

	// The second inner call must have been handed the adopted (compacted)
	// history, not the original large input.
	second := inner.sawOnCall[1]
	if len(second) == 0 || second[0].TextContent() != summary.TextContent() {
		t.Fatalf("second inner call did not start from the compacted base: %+v", roles(second))
	}
	for _, m := range second {
		if m.Role == llm.RoleUser && strings.HasPrefix(m.TextContent(), "old-") {
			t.Fatalf("pre-compaction message leaked into adopted session: %q", m.TextContent())
		}
	}

	// Result.Session (persisted verbatim) must be bounded: compacted base +
	// assistant tool call + tool result + final answer.
	if res.Session == nil {
		t.Fatalf("expected Result.Session to be set")
	}
	if got := res.Session[0].TextContent(); got != summary.TextContent() {
		t.Fatalf("Result.Session[0] should be the compacted base, got role=%s text=%q", res.Session[0].Role, got)
	}
	for _, m := range res.Session {
		if m.Role == llm.RoleUser && strings.HasPrefix(m.TextContent(), "old-") {
			t.Fatalf("pre-compaction message leaked into persisted session: %q", m.TextContent())
		}
	}
	// Exactly one summary — never stacked.
	summaries := 0
	for _, m := range res.Session {
		if strings.HasPrefix(m.TextContent(), codetoolsSummaryText()[:20]) {
			summaries++
		}
	}
	if summaries != 1 {
		t.Fatalf("expected exactly 1 handoff summary in persisted session, got %d (roles=%v)", summaries, roles(res.Session))
	}
}

func roles(msgs []llm.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Role+":"+truncateForTest(m.TextContent()))
	}
	return out
}

func truncateForTest(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 24 {
		return s[:24]
	}
	return s
}

func codetoolsSummaryText() string {
	return "Another language model started to solve this problem and produced a summary of its thinking process. Use this to build on the work.\nSummary body here."
}

// recordingTextLLM answers every request with text and keeps each request it
// was sent, as the provider would see it after every wrapper.
type recordingTextLLM struct {
	mu       sync.Mutex
	requests [][]llm.Message
	tools    [][]*llm.Tool
}

func (m *recordingTextLLM) Execute(_ context.Context, msgs []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	m.mu.Lock()
	m.requests = append(m.requests, append([]llm.Message(nil), msgs...))
	m.tools = append(m.tools, append([]*llm.Tool(nil), tools...))
	m.mu.Unlock()
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("summary of the conversation")})
	return &llm.Result{Message: &msg}, nil
}

// TestConversationSummaryRequestStartsLikeTheConversation pins why a
// compaction's summary request reuses the provider's cache: it reaches the
// model exactly as the conversation's own next request would — the same system
// prompt, injected context, history and tools, byte for byte — with the
// summary instruction where the turn's input would be.
func TestConversationSummaryRequestStartsLikeTheConversation(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sessions := state.NewSessionStore(db, "main")
	const sid = "cli-summary-prefix"
	_, _ = sessions.Append(ctx, sid, "user", "what does the service do?")
	_, _ = sessions.Append(ctx, sid, "assistant", "it compacts conversations")

	model := &recordingTextLLM{}
	restore := SetLLMOverrideForTest(model)
	defer restore()
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "gpt-main", APIKey: "k", BaseURL: "http://127.0.0.1:9/v1"}}},
	}}}
	r := &Runner{Deps: &Deps{Home: home, AgentName: "main", AppCfg: cfg, SessionStore: sessions}, FileResolver: PathResolver{}}
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}

	// A turn of the conversation, through the whole chain.
	turnCtx := llm.WithAgentSessionID(ctx, sid)
	if _, err := RunText(r, turnCtx, "and how fast is it?"); err != nil {
		t.Fatal(err)
	}
	if len(model.requests) == 0 {
		t.Fatal("the turn never reached the model")
	}
	turn, turnTools := model.requests[0], model.tools[0]

	// The compaction's summary request for the same conversation.
	summarize := CompactionService(r, sessions).ConversationSummary(sid)
	if summarize == nil {
		t.Fatal("a loaded runner has no conversation summarizer")
	}
	instruction := llm.UserMessage(llm.Text("summarize"))
	if _, err := summarize(ctx, instruction); err != nil {
		t.Fatal(err)
	}
	summary, summaryTools := model.requests[len(model.requests)-1], model.tools[len(model.tools)-1]

	// Everything the turn's request carried, the summary request carries too:
	// the same system prompt, injected context and history, byte for byte.
	// The engine writes the turn's rows as each block ends, so by the time the
	// summary runs the conversation already holds the answer the turn produced;
	// the summary request therefore carries that answer after the turn's
	// request, with the instruction where the conversation's next input would
	// go.
	if len(summary) != len(turn)+2 {
		t.Fatalf("summary request has %d messages, want the turn's %d plus the turn's stored answer and the instruction", len(summary), len(turn))
	}
	for i := 0; i < len(turn); i++ {
		want, _ := json.Marshal(turn[i])
		got, _ := json.Marshal(summary[i])
		if string(want) != string(got) {
			t.Fatalf("message %d differs:\nturn:    %s\nsummary: %s", i, want, got)
		}
	}
	if answer := summary[len(turn)]; answer.Role != llm.RoleAssistant || answer.TextContent() != "summary of the conversation" {
		t.Fatalf("summary request message %d = (%s, %q), want the turn's stored answer", len(turn), answer.Role, answer.TextContent())
	}
	if last := summary[len(summary)-1]; last.TextContent() != "summarize" {
		t.Fatalf("the instruction must end the summary request, got %q", last.TextContent())
	}
	if strings.Join(toolNames(turnTools), ",") != strings.Join(toolNames(summaryTools), ",") || len(summaryTools) == 0 {
		t.Fatalf("summary tools %v differ from the turn's %v", toolNames(summaryTools), toolNames(turnTools))
	}
}

// refusingImageSummaryLLM is a provider that will not take an image: every
// request carrying one is refused with the field error a real provider sent
// (pkg/llm/image_heal_test.go records it verbatim), and every image-free
// request answers with text. It keeps what each call carried so a test can say
// which attempt saw the picture.
type refusingImageSummaryLLM struct {
	mu       sync.Mutex
	requests [][]llm.Message
	images   []int
	omitted  []bool
}

// summaryImageRejection is the provider's answer to an image it will not take.
// The wording matters: pkg/llm.IsImageRejection recognizes it by these phrases.
const summaryImageRejection = ".messages[3].image[0]: You have uploaded an unsupported image. " +
	"Please make sure your image is valid and has one of the following formats: webp, png, jpeg, and gif."

func (m *refusingImageSummaryLLM) Execute(_ context.Context, msgs []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	images := 0
	omitted := false
	for _, msg := range msgs {
		for _, part := range msg.Parts {
			switch part.Type {
			case llm.ContentTypeImageURL, llm.ContentTypeImageBase64:
				images++
			}
			if strings.Contains(part.Text, "[image omitted") {
				omitted = true
			}
		}
	}
	m.mu.Lock()
	m.requests = append(m.requests, append([]llm.Message(nil), msgs...))
	m.images = append(m.images, images)
	m.omitted = append(m.omitted, omitted)
	m.mu.Unlock()
	if images > 0 {
		return nil, &llm.APIError{StatusCode: 400, Message: summaryImageRejection}
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("the conversation's summary")})
	return &llm.Result{Message: &msg}, nil
}

func (m *refusingImageSummaryLLM) calls() (images []int, omitted []bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]int(nil), m.images...), append([]bool(nil), m.omitted...)
}

// TestSummaryChainHealsRefusedImage pins the dead end the compaction chain used
// to walk into: a conversation carrying one image its provider refuses could
// never be summarized, so its history kept growing until nothing fit. The
// summary request goes through the same heal as a conversation's own request —
// refused once with the image, retried once with it degraded — while the stored
// row keeps its image.
//
// Its twin is TestConversationSummaryRequestStartsLikeTheConversation, which
// pins the other half of the same contract: a summary request that succeeds is
// byte for byte the request the chain would have built without the heal.
func TestSummaryChainHealsRefusedImage(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sessions := state.NewSessionStore(db, "main")
	const sid = "cli-summary-refused-image"
	if err := sessions.Ensure(ctx, sid, sid); err != nil {
		t.Fatalf("ensure session: %v", err)
	}

	// A user row that carries a real image, stored the way a surface stores an
	// attachment: the pixels live in the file the row references, and every
	// later request rehydrates them from there.
	img := filepath.Join(t.TempDir(), "shot.png")
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png fixture: %v", err)
	}
	if err := os.WriteFile(img, data, 0o600); err != nil {
		t.Fatalf("write png fixture: %v", err)
	}
	text := "what is this\n[Image #1]"
	parts := state.MessagePartsJSON(llm.UserMessage(llm.Text(text)), text)
	parts = state.AppendRawPartJSON(parts, state.FileReferencePartJSON(img, "shot.png", "image/png"))
	if _, err := sessions.AppendStructuredMessage(ctx, sid, "user", text, "", parts, "", "", "", "", state.MessageExecTiming{}); err != nil {
		t.Fatalf("append user row: %v", err)
	}

	provider := &refusingImageSummaryLLM{}
	restore := SetLLMOverrideForTest(provider)
	defer restore()
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "gpt-main", APIKey: "k", BaseURL: "http://127.0.0.1:9/v1"}}},
	}}}
	r := &Runner{Deps: &Deps{Home: home, AgentName: "main", AppCfg: cfg, SessionStore: sessions}, FileResolver: PathResolver{}}
	if err := r.Load(); err != nil {
		t.Fatalf("load runner: %v", err)
	}

	summarize := CompactionService(r, sessions).ConversationSummary(sid)
	if summarize == nil {
		t.Fatal("a loaded runner has no conversation summarizer")
	}
	result, err := summarize(ctx, llm.UserMessage(llm.Text("summarize this conversation")))
	if err != nil {
		t.Fatalf("the compaction's summary request failed on an image the provider refuses: %v", err)
	}
	if result == nil || result.Message == nil || result.Message.TextContent() == "" {
		t.Fatalf("result = %+v, want the summary text", result)
	}

	images, omitted := provider.calls()
	if len(images) != 2 {
		t.Fatalf("provider was called %d times, want 2 (one refused, one degraded): images per call %v", len(images), images)
	}
	if images[0] == 0 {
		t.Fatal("the first summary request carried no image, so nothing was healed; the transcript did not rehydrate it")
	}
	if images[1] != 0 {
		t.Fatalf("the retried summary request still carried %d images, want 0", images[1])
	}
	if !omitted[1] {
		t.Fatal("the retried request must say which image was dropped, not silently lose it")
	}
	// The transcript keeps its image: the degraded copy belongs to that call.
	rows, err := sessions.ListTranscriptMessages(ctx, sid, 50)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if len(rows) == 0 || !strings.Contains(rows[0].Parts[0].Text, "[Image #1]") {
		t.Fatalf("the stored row lost the image reference: %+v", rows)
	}
}

// TestCompactionSizesATypedSubagentByItsOwnModel pins the fix for the
// mis-sized threshold: a compaction must size itself by the model the call in
// context is actually routed to. An explore subagent whose definition carries
// its own 32k chain would otherwise have its threshold computed from the main
// agent's 200k window and hit the provider's "context too long" before any
// compaction ran.
func TestCompactionSizesATypedSubagentByItsOwnModel(t *testing.T) {
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "zhipuai", Model: "glm-5.3", APIKey: "k", BaseURL: "http://127.0.0.1:9/v1"},
		}},
		"explore": {LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "deepseek", Model: "deepseek-v4-flash", APIKey: "k", BaseURL: "http://127.0.0.1:9/v1"},
		}},
	}}}
	r := &Runner{Deps: &Deps{AppCfg: cfg}}

	// The context an explore subagent's own calls run under: its subtype, a
	// query source that is not the main thread's.
	typedCtx := tool.WithSubagentType(WithQuerySource(context.Background(), "agent:builtin:explore"), "explore")
	provider, model := r.agentModelFor(typedCtx)
	if provider != "deepseek" || model != "deepseek-v4-flash" {
		t.Fatalf("agentModelFor = %s/%s, want the explore subagent's own 32k model deepseek/deepseek-v4-flash", provider, model)
	}

	// The main thread stays on the conversation's model.
	mainProvider, mainModel := r.agentModelFor(WithQuerySource(context.Background(), "repl_main_thread"))
	if mainProvider != "zhipuai" || mainModel != "glm-5.3" {
		t.Fatalf("agentModelFor on the main thread = %s/%s, want the conversation's model zhipuai/glm-5.3", mainProvider, mainModel)
	}
}

// steerCompactLLM answers the first sampling with a call for a large chunk and
// finishes on every later one, recording what each sampling it served was sent.
// A sampling whose context is already cancelled is refused unrecorded, the way
// a provider refuses an aborted request.
type steerCompactLLM struct {
	calls [][]llm.Message
}

func (m *steerCompactLLM) Execute(ctx context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.calls = append(m.calls, append([]llm.Message(nil), messages...))
	if len(m.calls) == 1 {
		msg := llm.AssistantMessage(nil, llm.ToolCall{ID: "chunk", Type: llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "read_chunk", Arguments: `{}`}})
		return &llm.Result{Message: &msg}, nil
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	return &llm.Result{Message: &msg}, nil
}

func steerCompactFixture(t *testing.T, steer string) (*tool.State, *llm.Tool, *InputQueue, *TurnInputRuntime) {
	t.Helper()
	st := tool.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_chunk", ReadOnly: true, ConcurrencySafe: true})
	readTool, err := llm.NewTool("read_chunk", "read a chunk", func(context.Context, *struct{}) (string, error) {
		return strings.Repeat("large tool result\n", 300), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: steer, Parts: []llm.ContentPart{llm.Text(steer)}})
	return st, readTool, q, rt
}

// A checkpoint is durable and a queued steer riding on the call is not settled
// yet, so a mid-turn compaction summarizes only the history before the steer
// and the request carries the steer verbatim after the checkpoint.
func TestCompactionKeepsInFlightSteersOutOfTheCheckpoint(t *testing.T) {
	st, readTool, q, rt := steerCompactFixture(t, "also check the tests")
	var compacted [][]llm.Message
	inner := &steerCompactLLM{}
	recoverable := WrapRecoverableLLM(inner, nil, &CompactChainDeps{
		ExplicitLimit: 500,
		TryCompact: func(_ context.Context, messages []llm.Message, _ []*llm.Tool, _ bool) ([]llm.Message, bool, error) {
			compacted = append(compacted, append([]llm.Message(nil), messages...))
			return []llm.Message{llm.UserMessage(llm.Text("checkpoint"))}, true, nil
		},
	})
	ctx := WithTurnInputRuntime(tool.WithRunID(context.Background(), "steer-compaction"), rt)
	res, err := wrapToolOrchestrationLLM(recoverable, st).Execute(ctx,
		[]llm.Message{llm.UserMessage(llm.Text("start"))}, []*llm.Tool{readTool})
	if err != nil {
		t.Fatal(err)
	}
	if len(compacted) != 1 {
		t.Fatalf("compactions=%d want 1", len(compacted))
	}
	if last := compacted[0][len(compacted[0])-1]; last.Role != llm.RoleTool {
		t.Fatalf("the checkpoint covered the in-flight steer: compaction input %v", roles(compacted[0]))
	}
	if second := inner.calls[1]; len(second) != 2 || second[0].TextContent() != "checkpoint" || second[1].TextContent() != "also check the tests" {
		t.Fatalf("second request = %v, want the checkpoint followed by the steer", roles(second))
	}
	if len(res.Session) != 3 || res.Session[1].TextContent() != "also check the tests" || res.Session[2].TextContent() != "done" {
		t.Fatalf("session = %v, want checkpoint, steer, answer", roles(res.Session))
	}
	if delivered := q.TakeDelivered(); len(delivered) != 1 {
		t.Fatalf("delivered = %#v, want the answered steer", delivered)
	}
}

// Recalling the steer while the checkpoint is being written must neither cancel
// the compaction — it never included the steer — nor let the steer reach the
// model: the checkpoint is kept and the call is re-sent without it.
func TestRetractionDuringCompactionKeepsTheCheckpoint(t *testing.T) {
	st, readTool, q, rt := steerCompactFixture(t, "never mind")
	compactions := 0
	var compactCtxErr error
	inner := &steerCompactLLM{}
	recoverable := WrapRecoverableLLM(inner, nil, &CompactChainDeps{
		ExplicitLimit: 500,
		TryCompact: func(ctx context.Context, _ []llm.Message, _ []*llm.Tool, _ bool) ([]llm.Message, bool, error) {
			compactions++
			if _, ok := q.Recall(); !ok {
				t.Error("recall refused a steer that is still in the queue")
			}
			compactCtxErr = ctx.Err()
			return []llm.Message{llm.UserMessage(llm.Text("checkpoint"))}, true, nil
		},
	})
	ctx := WithTurnInputRuntime(tool.WithRunID(context.Background(), "steer-compaction-retract"), rt)
	res, err := wrapToolOrchestrationLLM(recoverable, st).Execute(ctx,
		[]llm.Message{llm.UserMessage(llm.Text("start"))}, []*llm.Tool{readTool})
	if err != nil {
		t.Fatal(err)
	}
	if compactCtxErr != nil {
		t.Fatalf("the retraction cancelled the compaction: %v", compactCtxErr)
	}
	if compactions != 1 {
		t.Fatalf("compactions=%d want 1: the kept checkpoint must not be redone", compactions)
	}
	if len(inner.calls) != 2 || len(inner.calls[1]) != 1 || inner.calls[1][0].TextContent() != "checkpoint" {
		t.Fatalf("requests = %d, last %v; want the re-sent call to carry only the checkpoint", len(inner.calls), roles(inner.calls[len(inner.calls)-1]))
	}
	if len(res.Session) != 2 || res.Session[0].TextContent() != "checkpoint" || res.Session[1].TextContent() != "done" {
		t.Fatalf("session = %v, want checkpoint and answer", roles(res.Session))
	}
	if delivered := q.TakeDelivered(); len(delivered) != 0 {
		t.Fatalf("a retracted steer was rendered as sent: %#v", delivered)
	}
}

// scaledEstimator counts 100 tokens per byte, so a test can move one fixed
// history across a threshold by switching the estimator alone.
type scaledEstimator struct{}

func (scaledEstimator) Count(text string) int { return len(text) * 100 }
func (scaledEstimator) Name() string          { return "scaled-100" }

// The mid-turn checkpoint must count with the active call's own model's
// estimator. The same rows must cross the threshold under that model and stay
// under it with the default — which is the regression guard against this
// checkpoint silently falling back to the global encoder.
func TestShouldCompactMessagesUsesActiveModelEstimator(t *testing.T) {
	llm.ResetObservedForTest()
	const modelLimit = 180000
	const provider, model = "test-provider", "model-x"
	msgs := []llm.Message{llm.UserMessage(llm.Text(strings.Repeat("x", 3000)))}

	if shouldCompactMessages(msgs, modelLimit, nil, -1, nil, provider, model) {
		t.Fatal("default estimator compacted below the threshold")
	}

	restore := llm.SetEstimatorForTest(provider, model, scaledEstimator{})
	should := shouldCompactMessages(msgs, modelLimit, nil, -1, nil, provider, model)
	restore()
	if !should {
		t.Fatal("shouldCompactMessages did not use the active model's estimator")
	}
	if shouldCompactMessages(msgs, modelLimit, nil, -1, nil, provider, model) {
		t.Fatal("restored estimator still compacted")
	}
}

// observedUsageLLM accepts every request and reports input tokens as the
// provider's own prompt count, the ground truth the overhead is learned from.
type observedUsageLLM struct{ input int }

func (m *observedUsageLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("ok")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{InputTokens: m.input}}, nil
}

// The overhead the model observes (server-real minus local estimate) must be
// measured against the SAME estimate the compaction decision counts with. If
// the sent estimate were sized by the global encoder while the decision used
// the session model's tokenizer, the learned overhead would be added to a base
// it was never measured against.
func TestObservedEstimateMatchesDecisionEstimator(t *testing.T) {
	llm.ResetObservedForTest()
	// This test records an observation; remove it so it cannot leak into others.
	t.Cleanup(llm.ResetObservedForTest)

	const provider, model = "test-provider", "observed"
	const real = 500000
	restore := llm.SetEstimatorForTest(provider, model, scaledEstimator{})
	t.Cleanup(restore)

	noteTool, err := llm.NewTool("note", "note tool", func(context.Context, *struct{}) (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	tools := []*llm.Tool{noteTool}
	msgs := []llm.Message{llm.UserMessage(llm.Text(strings.Repeat("y", 3000)))}

	recoverable := WrapRecoverableLLM(&observedUsageLLM{input: real}, nil, &CompactChainDeps{
		ActiveModel: func(context.Context) (string, string) { return provider, model },
	})
	if _, err := recoverable.Execute(context.Background(), msgs, tools); err != nil {
		t.Fatal(err)
	}

	if _, ok := llm.Get(provider, model); !ok {
		t.Fatal("no observation recorded for the active model")
	}
	est := scaledEstimator{}
	sent := llm.EstimateMessagesWith(est, msgs) + estimateToolsTokens(est, tools)
	want := real - sent
	// The overhead is learned under the decision estimator's name; read it
	// back the way the decision projects with it.
	const probe = 1000
	overhead := llm.ProjectFor(provider, model, est.Name(), probe) - probe
	if overhead != want {
		t.Fatalf("Overhead=%d want %d (real %d - decision-estimator estimate %d)", overhead, want, real, sent)
	}
	// Guard the test itself: the global estimator must size the same request
	// differently, or the assertion above proves nothing.
	if global := llm.EstimateMessages(msgs) + estimateToolsTokens(llm.DefaultEstimator(), tools); global == sent {
		t.Fatal("fixture: the model estimator must differ from the global one")
	} else if overhead == real-global {
		t.Fatal("Overhead was measured against the global estimator, not the decision estimator")
	}
}

// fakeNamedEstimator counts 3 tokens per byte under the name "fake".
type fakeNamedEstimator struct{}

func (fakeNamedEstimator) Count(text string) int { return len(text) * 3 }
func (fakeNamedEstimator) Name() string          { return "fake" }

// An accepted call's overhead is filed under the estimator that sized the
// request — the active model's — and not under the unnamed estimator, whose
// projection stays the bare estimate.
func TestObservationRecordedUnderActiveEstimator(t *testing.T) {
	llm.ResetObservedForTest()
	t.Cleanup(llm.ResetObservedForTest)

	const provider, model = "test-provider", "observed-fake"
	const real = 90000
	t.Cleanup(llm.SetEstimatorForTest(provider, model, fakeNamedEstimator{}))

	msgs := []llm.Message{llm.UserMessage(llm.Text(strings.Repeat("z", 2000)))}
	recoverable := WrapRecoverableLLM(&observedUsageLLM{input: real}, nil, &CompactChainDeps{
		ActiveModel: func(context.Context) (string, string) { return provider, model },
	})
	if _, err := recoverable.Execute(context.Background(), msgs, nil); err != nil {
		t.Fatal(err)
	}

	sent := llm.EstimateMessagesWith(fakeNamedEstimator{}, msgs)
	if sent <= 0 || sent >= real {
		t.Fatalf("fixture: fake estimate %d must be positive and below the real %d", sent, real)
	}
	const x = 1000
	if got := llm.ProjectFor(provider, model, "fake", x) - x; got != real-sent {
		t.Fatalf("overhead under \"fake\" = %d, want %d (real %d - fake estimate %d)", got, real-sent, real, sent)
	}
	if got := llm.ProjectFor(provider, model, "", x) - x; got != 0 {
		t.Fatalf("overhead under the unnamed estimator = %d, want 0", got)
	}
	if got := llm.ProjectFor(provider, model, llm.DefaultEstimator().Name(), x) - x; got != 0 {
		t.Fatalf("overhead under the default estimator (%s) = %d, want 0", llm.DefaultEstimator().Name(), got)
	}
}

// One run identity does not name one history: a fork dispatched while its
// dispatcher has no run row of its own runs under the dispatching run's id
// (pkg/run/subagent.go:1326-1341), and its history is its own worker session's.
// Each history keeps its own checkpoint, so neither request is spliced with the
// other's.
func TestCompactionGuardKeepsAHistorysCheckpointToItself(t *testing.T) {
	inner := &steerCompactLLM{}
	compactions := 0
	recoverable := WrapRecoverableLLM(inner, nil, &CompactChainDeps{
		ExplicitLimit: 500,
		TryCompact: func(_ context.Context, _ []llm.Message, _ []*llm.Tool, _ bool) ([]llm.Message, bool, error) {
			compactions++
			return []llm.Message{llm.UserMessage(llm.Text(fmt.Sprintf("checkpoint-%d", compactions)))}, true, nil
		},
	})
	child := []llm.Message{
		llm.UserMessage(llm.Text("child task")),
		llm.UserMessage(llm.Text(strings.Repeat("large child history\n", 300))),
	}
	childCtx := llm.WithAgentSessionID(tool.WithRunID(context.Background(), "shared-run"), "worker-child")
	if _, err := recoverable.Execute(childCtx, child, nil); err != nil {
		t.Fatal(err)
	}
	parent := []llm.Message{
		llm.UserMessage(llm.Text("parent start")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("parent answer")}),
	}
	parentCtx := llm.WithAgentSessionID(tool.WithRunID(context.Background(), "shared-run"), "conversation-parent")
	if _, err := recoverable.Execute(parentCtx, parent, nil); err != nil {
		t.Fatal(err)
	}
	last := inner.calls[len(inner.calls)-1]
	if len(last) != 2 || last[0].TextContent() != "parent start" {
		t.Fatalf("parent request = %v: another history's checkpoint replaced the parent's", roles(last))
	}
}

// A call that names no run caches nothing, exactly as before: the guard is
// keyed by the history a run identifies, and a call without one has nothing to
// cache under. Its slice is compacted whole, never spliced with a stale base.
func TestCompactionGuardCachesNothingWithoutARunID(t *testing.T) {
	inner := &steerCompactLLM{}
	var compacted [][]llm.Message
	recoverable := WrapRecoverableLLM(inner, nil, &CompactChainDeps{
		ExplicitLimit: 500,
		TryCompact: func(_ context.Context, messages []llm.Message, _ []*llm.Tool, _ bool) ([]llm.Message, bool, error) {
			compacted = append(compacted, append([]llm.Message(nil), messages...))
			return []llm.Message{llm.UserMessage(llm.Text("checkpoint"))}, true, nil
		},
	})
	history := []llm.Message{
		llm.UserMessage(llm.Text("first")),
		llm.UserMessage(llm.Text("second")),
		llm.UserMessage(llm.Text(strings.Repeat("large history\n", 300))),
	}
	for i := 0; i < 2; i++ {
		if _, err := recoverable.Execute(context.Background(), history, nil); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if len(compacted) != 2 {
		t.Fatalf("compactions=%d want 2", len(compacted))
	}
	for i, input := range compacted {
		if len(input) != len(history) || input[0].TextContent() != "first" {
			t.Fatalf("compaction %d saw %v, want the sent slice unchanged", i, roles(input))
		}
	}
}

// reshapingLLM stands in for a wrapper between the orchestration loop and
// recoverableLLM that changes the message slice, so the provisional tail the
// loop described no longer matches what recoverableLLM is handed.
type reshapingLLM struct{ inner llm.LLM }

func (w reshapingLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	extra := llm.UserMessage(llm.Text("reshaped"))
	return w.inner.Execute(ctx, append(append([]llm.Message(nil), messages...), extra), tools)
}

// A call whose provisional steers can no longer be told apart from settled
// history is not compacted at all: a checkpoint never folds in a steer the user
// can still take back, and the steer reaches the model verbatim.
func TestCompactionSkipsACallWhoseProvisionalTailCannotBeHonored(t *testing.T) {
	st, readTool, _, rt := steerCompactFixture(t, "also check the tests")
	var compacted [][]llm.Message
	inner := &steerCompactLLM{}
	recoverable := WrapRecoverableLLM(inner, nil, &CompactChainDeps{
		ExplicitLimit: 500,
		TryCompact: func(_ context.Context, messages []llm.Message, _ []*llm.Tool, _ bool) ([]llm.Message, bool, error) {
			compacted = append(compacted, append([]llm.Message(nil), messages...))
			return []llm.Message{llm.UserMessage(llm.Text("checkpoint"))}, true, nil
		},
	})
	ctx := WithTurnInputRuntime(tool.WithRunID(context.Background(), "steer-tail-reshaped"), rt)
	if _, err := wrapToolOrchestrationLLM(reshapingLLM{inner: recoverable}, st).Execute(ctx,
		[]llm.Message{llm.UserMessage(llm.Text("start"))}, []*llm.Tool{readTool}); err != nil {
		t.Fatal(err)
	}
	for i, input := range compacted {
		for _, msg := range input {
			if strings.Contains(msg.TextContent(), "also check the tests") {
				t.Fatalf("compaction %d folded the provisional steer into its checkpoint: %v", i, roles(input))
			}
		}
	}
	if len(inner.calls) != 2 {
		t.Fatalf("requests = %d want 2", len(inner.calls))
	}
	carried := false
	for _, msg := range inner.calls[1] {
		if msg.TextContent() == "also check the tests" {
			carried = true
		}
	}
	if !carried {
		t.Fatalf("the steer did not reach the model verbatim: %v", roles(inner.calls[1]))
	}
}

// A call that inherits a tail recorded for another history — a subagent
// dispatched during a steered call runs under the dispatching call's context —
// carries no provisional messages of its own, so it compacts as usual. Reading
// that as "cannot be told apart" would stop every child of a steered turn from
// compacting for the whole execution.
func TestCompactionOfAHistoryThatCarriesAnotherHistorysTail(t *testing.T) {
	inner := &steerCompactLLM{}
	var compacted [][]llm.Message
	recoverable := WrapRecoverableLLM(inner, nil, &CompactChainDeps{
		ExplicitLimit: 500,
		TryCompact: func(_ context.Context, messages []llm.Message, _ []*llm.Tool, _ bool) ([]llm.Message, bool, error) {
			compacted = append(compacted, append([]llm.Message(nil), messages...))
			return []llm.Message{llm.UserMessage(llm.Text("checkpoint"))}, true, nil
		},
	})
	history := []llm.Message{
		llm.UserMessage(llm.Text("start")),
		llm.UserMessage(llm.Text(strings.Repeat("large history\n", 300))),
	}
	parentCtx := llm.WithAgentSessionID(tool.WithRunID(context.Background(), "steered-run"), "conversation-parent")
	// The dispatching call's context: a provisional tail of one message, marked
	// for the parent's history.
	parentCall := withProvisionalTail(parentCtx, parentCtx, len(history)+1, 1)
	// The child's own history: the same run (that is what its calls inherit),
	// its own worker session, its own messages.
	childCtx := llm.WithAgentSessionID(parentCall, "worker-child")
	if _, err := recoverable.Execute(childCtx, history, nil); err != nil {
		t.Fatal(err)
	}
	if len(compacted) != 1 || len(compacted[0]) != len(history) {
		t.Fatalf("compactions=%d, first input=%v; want the child's own history compacted whole", len(compacted), roles(compacted[0]))
	}
}

// The prefill baseline counts the history only when the guard has none for it:
// a later call on the same history, or one after a checkpoint was set, reuses
// the recorded value without counting again.
func TestPrefillEstimateIsLazy(t *testing.T) {
	guard := newCompactRunGuard()
	calls := 0
	count := func(n int) func() int {
		return func() int {
			calls++
			return n
		}
	}

	if got := guard.prefill("run\x00a", count(120)); got != 120 {
		t.Fatalf("first prefill = %d, want 120", got)
	}
	if got := guard.prefill("run\x00a", count(999)); got != 120 {
		t.Fatalf("second prefill = %d, want the recorded 120", got)
	}
	if calls != 1 {
		t.Fatalf("count ran %d times for two prefills of one history, want 1", calls)
	}

	guard.set("run\x00b", []llm.Message{llm.UserMessage(llm.Text("checkpoint"))}, 42)
	if got := guard.prefill("run\x00b", count(999)); got != 42 {
		t.Fatalf("prefill after set = %d, want the checkpoint's 42", got)
	}
	if got := guard.prefill("", count(999)); got != -1 {
		t.Fatalf("prefill without a history key = %d, want -1", got)
	}
	var nilGuard *compactRunGuard
	if got := nilGuard.prefill("run\x00c", count(999)); got != -1 {
		t.Fatalf("prefill on a nil guard = %d, want -1", got)
	}
	if calls != 1 {
		t.Fatalf("count ran %d times, want only the first history's one", calls)
	}
}
