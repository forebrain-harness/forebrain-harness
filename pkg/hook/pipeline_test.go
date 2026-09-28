package hook

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestPipelineOrder(t *testing.T) {
	p := NewAgentPipeline()
	var order []string
	p.Add("b", func(ctx context.Context, phase string, hc HookContext, text string) (PreHookResult, error) {
		order = append(order, "b:"+text)
		return PreHookResult{Text: text + "B"}, nil
	})
	p.Add("a", func(ctx context.Context, phase string, hc HookContext, text string) (PreHookResult, error) {
		order = append(order, "a:"+text)
		return PreHookResult{Text: text + "A"}, nil
	})
	out, err := p.Run(context.Background(), "pre", HookContext{}, "x")
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "xAB" {
		t.Fatalf("got %q", out.Text)
	}
	if len(order) != 2 || order[0][0:2] != "a:" || order[1][0:2] != "b:" {
		t.Fatalf("sort order %v", order)
	}
}

func TestPipelineAddIgnoresInvalidEntries(t *testing.T) {
	p := NewAgentPipeline()
	p.Add("", func(ctx context.Context, phase string, hc HookContext, text string) (PreHookResult, error) {
		return PreHookResult{Text: "bad"}, nil
	})
	p.Add("nil", nil)
	p.Add("meta1", func(ctx context.Context, phase string, hc HookContext, text string) (PreHookResult, error) {
		return PreHookResult{
			Text: text + "1",
		}, nil
	})
	p.Add("meta2", func(ctx context.Context, phase string, hc HookContext, text string) (PreHookResult, error) {
		return PreHookResult{
			Text: text + "2",
		}, nil
	})
	out, err := p.Run(context.Background(), "pre", HookContext{}, "x")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if out.Text != "x12" {
		t.Fatalf("unexpected merged result: %+v", out)
	}
	if got := p.Names(); !reflect.DeepEqual(got, []string{"meta1", "meta2"}) {
		t.Fatalf("pipeline names = %#v", got)
	}
}

func TestPipelineAddCorePreHooksUsesCurrentMainlineNames(t *testing.T) {
	hook := func(context.Context, string, HookContext, string) (PreHookResult, error) {
		return PreHookResult{Text: "ok"}, nil
	}
	p := NewAgentPipeline()
	p.AddCorePreHooks(CorePreHooks{
		ForebrainRules: hook,
		PlanMode:       hook,
		ContextEngine:  hook,
	})
	want := []string{
		HookContextEngine,
		HookPlanMode,
		HookForebrainRules,
	}
	if got := p.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("core pre-hook names = %#v, want %#v", got, want)
	}
	for _, stale := range []string{"session-history", "memory-flush"} {
		for _, name := range p.Names() {
			if name == stale {
				t.Fatalf("core pre-hooks should not include stale hook %q", stale)
			}
		}
	}
}

func TestPipelineRunStopsOnError(t *testing.T) {
	wantErr := errors.New("hook failed")
	p := NewAgentPipeline()
	p.Add("a", func(context.Context, string, HookContext, string) (PreHookResult, error) {
		return PreHookResult{}, wantErr
	})
	if _, err := p.Run(context.Background(), "pre", HookContext{}, "x"); !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
}

func TestInteractiveTrigger(t *testing.T) {
	tests := []struct {
		trigger string
		want    bool
	}{
		{trigger: "user", want: true},
		{trigger: "heartbeat", want: false},
		{trigger: "system", want: false},
		{trigger: "", want: false},
	}
	for _, tt := range tests {
		if got := InteractiveTrigger(tt.trigger); got != tt.want {
			t.Fatalf("InteractiveTrigger(%q)=%v want %v", tt.trigger, got, tt.want)
		}
	}
}
