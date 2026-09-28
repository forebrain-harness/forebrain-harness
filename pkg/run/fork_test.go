package run

import (
	"context"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func TestRunnerCacheSafeParamsForRunNilReceiver(t *testing.T) {
	var r *Runner
	if _, err := r.CacheSafeParamsForRun(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestRunnerCacheSafeParamsForRunEmptyDescription(t *testing.T) {
	r := &Runner{Deps: &Deps{}}
	if _, err := r.CacheSafeParamsForRun(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestRunnerCacheSafeParamsForRunFallsBackToTheDescription(t *testing.T) {
	r := &Runner{Deps: &Deps{}, mainCfg: AgentConfigYAML{Description: "system prompt"}}
	params, err := r.CacheSafeParamsForRun(WithForkCacheCapture(context.Background()))
	if err != nil {
		t.Fatalf("CacheSafeParamsForRun: %v", err)
	}
	if params.SystemPrompt != "system prompt" {
		t.Fatalf("system prompt=%q", params.SystemPrompt)
	}
	if params.ToolUseContext == nil {
		t.Fatal("expected tool use context map")
	}
}

func TestRunnerCacheSafeParamsForRunPrefersTheCapturedRequest(t *testing.T) {
	r := &Runner{Deps: &Deps{}, mainCfg: AgentConfigYAML{Description: "fallback system"}}
	ctx := WithForkCacheCapture(context.Background())
	slot := forkCacheSlotFromContext(ctx)
	slot.store(&CacheSafeParams{
		SystemPrompt:   "captured system",
		UserContext:    map[string]string{"u": "1"},
		SystemContext:  map[string]string{"s": "1"},
		ToolUseContext: map[string]string{"tool": "1"},
		ForkContextMessages: []llm.Message{
			llm.UserMessage(llm.Text("captured prompt")),
		},
	})
	params, err := r.CacheSafeParamsForRun(ctx)
	if err != nil {
		t.Fatalf("CacheSafeParamsForRun: %v", err)
	}
	if params.SystemPrompt != "captured system" {
		t.Fatalf("system prompt=%q", params.SystemPrompt)
	}
	if len(params.ForkContextMessages) != 1 || params.ForkContextMessages[0].TextContent() != "captured prompt" {
		t.Fatalf("unexpected captured messages: %#v", params.ForkContextMessages)
	}
	params.UserContext["u"] = "changed"
	if got, _ := r.CacheSafeParamsForRun(ctx); got.UserContext["u"] != "1" {
		t.Fatalf("stored snapshot mutated: %#v", got.UserContext)
	}
}

// TestForkCacheParamsDoNotOutliveTheirRun pins where the captured parent
// request lives. It is a cloned transcript, and it used to be held in a map on
// the Runner keyed by session id that nothing ever released — one per session
// for the life of the process. It belongs to the turn that captured it: the
// fork that inherits it runs inside that same turn.
func TestForkCacheParamsDoNotOutliveTheirRun(t *testing.T) {
	r := &Runner{Deps: &Deps{}, mainCfg: AgentConfigYAML{Description: "fallback system"}}
	wrapped := wrapForkCaptureLLM(stubLLM{})

	first := WithForkCacheCapture(llm.WithAgentSessionID(context.Background(), "session-1"))
	if _, err := wrapped.Execute(first, []llm.Message{llm.SystemMessage("first turn"), llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if params, err := r.CacheSafeParamsForRun(first); err != nil || params.SystemPrompt != "first turn" {
		t.Fatalf("the capturing run did not see its own capture: %+v err=%v", params, err)
	}

	// The next turn of the same session starts with nothing carried over.
	second := WithForkCacheCapture(llm.WithAgentSessionID(context.Background(), "session-1"))
	params, err := r.CacheSafeParamsForRun(second)
	if err != nil {
		t.Fatal(err)
	}
	if params.SystemPrompt != "fallback system" {
		t.Fatalf("a later run inherited the previous run's capture: %q", params.SystemPrompt)
	}
}

func TestForkCaptureSavesCacheSafeParamsOnlyForMainSources(t *testing.T) {
	wrapped := wrapForkCaptureLLM(stubLLM{})
	ctx := WithForkCacheCapture(llm.WithAgentSessionID(context.Background(), "session-1"))
	slot := forkCacheSlotFromContext(ctx)

	// A subagent shares its parent's slot, and must not overwrite what the
	// parent forked from.
	child := WithQuerySource(ctx, "agent:default")
	if _, err := wrapped.Execute(child, []llm.Message{llm.SystemMessage("sub"), llm.UserMessage(llm.Text("child"))}, nil); err != nil {
		t.Fatalf("Execute child: %v", err)
	}
	if _, ok := slot.load(); ok {
		t.Fatal("subagent query source must not store cache-safe params")
	}

	main := WithQuerySource(ctx, "repl_main_thread")
	if _, err := wrapped.Execute(main, []llm.Message{llm.SystemMessage("main"), llm.UserMessage(llm.Text("parent"))}, nil); err != nil {
		t.Fatalf("Execute main: %v", err)
	}
	got, ok := slot.load()
	if !ok || got.SystemPrompt != "main" {
		t.Fatalf("main query source did not store the snapshot: ok=%v got=%+v", ok, got)
	}
}

func TestShouldSaveCacheSafeParamsStopHookGate(t *testing.T) {
	cases := []struct {
		name        string
		querySource string
		agentID     string
		want        bool
	}{
		{name: "main", querySource: "repl_main_thread", want: true},
		{name: "sdk", querySource: "sdk", want: true},
		{name: "default", querySource: "", want: true},
		{name: "hook", querySource: "hook_agent", want: false},
		{name: "subagent source", querySource: "agent:default", want: false},
		{name: "subagent id", querySource: "repl_main_thread", agentID: "agent-1", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldSaveCacheSafeParams(tc.querySource, tc.agentID); got != tc.want {
				t.Fatalf("ShouldSaveCacheSafeParams(%q,%q)=%v want %v", tc.querySource, tc.agentID, got, tc.want)
			}
		})
	}
}

type stubLLM struct{}

func (stubLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	return &llm.Result{}, nil
}
