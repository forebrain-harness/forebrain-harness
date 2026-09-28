package hook

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPlanModeNew(t *testing.T) {
	t.Parallel()
	l := NewPlanMode(" /tmp/x ")
	if l.StateRoot != filepath.Join("/tmp/x", "workspace") {
		t.Fatalf("unexpected loader: %#v", l)
	}
}

func TestPlanModeHookReturnsRun(t *testing.T) {
	home := t.TempDir()
	l := NewPlanMode(home)
	out, err := l.Hook()(context.Background(), "post", HookContext{Trigger: "user"}, "hello")
	if err != nil {
		t.Fatalf("hook returned error: %v", err)
	}
	if out.Text != "hello" {
		t.Fatalf("post hook changed text: %q", out.Text)
	}
}

func TestPlanModeRunLeavesNonInteractiveAndCommandTextUnchanged(t *testing.T) {
	home := t.TempDir()
	l := NewPlanMode(home)
	for _, tc := range []struct {
		name  string
		phase string
		hc    HookContext
		text  string
	}{
		{name: "post phase", phase: "post", hc: HookContext{Trigger: "user"}, text: "hello"},
		{name: "non interactive", phase: "pre", hc: HookContext{Trigger: "system"}, text: "hello"},
		{name: "blank", phase: "pre", hc: HookContext{Trigger: "user"}, text: "  "},
		{name: "slash command", phase: "pre", hc: HookContext{Trigger: "user"}, text: "/help"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := l.run(context.Background(), tc.phase, tc.hc, tc.text)
			if err != nil {
				t.Fatalf("run returned error: %v", err)
			}
			if out.Text != tc.text {
				t.Fatalf("text changed to %q, want %q", out.Text, tc.text)
			}
		})
	}
}

// The helpers below were production functions that only these tests called:
// each is a thin wrapper over the live function underneath. They live here
// so the production files carry no unused code.

// New is the home-based shorthand for the main agent (workspace <home>/workspace).
func NewPlanMode(home string) *PlanModeLoader {
	return NewPlanModeForWorkspace(home, "")
}
