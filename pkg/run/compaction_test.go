package run

import (
	"context"
	"encoding/json"
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

	// Everything before the turn's input is the summary request's too.
	prefix := len(turn) - 1
	if len(summary) != prefix+1 {
		t.Fatalf("summary request has %d messages, want the turn's %d before its input and the instruction", len(summary), prefix)
	}
	for i := 0; i < prefix; i++ {
		want, _ := json.Marshal(turn[i])
		got, _ := json.Marshal(summary[i])
		if string(want) != string(got) {
			t.Fatalf("message %d differs:\nturn:    %s\nsummary: %s", i, want, got)
		}
	}
	if last := summary[len(summary)-1]; last.TextContent() != "summarize" {
		t.Fatalf("the instruction must end the summary request, got %q", last.TextContent())
	}
	if strings.Join(toolNames(turnTools), ",") != strings.Join(toolNames(summaryTools), ",") || len(summaryTools) == 0 {
		t.Fatalf("summary tools %v differ from the turn's %v", toolNames(summaryTools), toolNames(turnTools))
	}
}
