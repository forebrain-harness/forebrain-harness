package assembly

import (
	"context"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
)

func TestRunPreflightUsesOnlyAutoCheckpoint(t *testing.T) {
	calls := 0
	out, err := RunPreflight(context.Background(), PreflightConfig{HookContext: hook.HookContext{SessionID: "s"}, AutoCompact: func(context.Context, string, string) (Result, bool, error) {
		calls++
		return Result{Trigger: "auto", Strategy: "local"}, true, nil
	}})
	if err != nil || !out.Did || calls != 1 {
		t.Fatalf("out=%+v calls=%d err=%v", out, calls, err)
	}
}

func TestRunPreflightCompactsBeforeAssemblingPendingInput(t *testing.T) {
	var order []string
	out, err := RunPreflight(context.Background(), PreflightConfig{
		HookContext: hook.HookContext{SessionID: "s"},
		Input:       "incoming user turn",
		AutoCompact: func(_ context.Context, sessionID, pendingInput string) (Result, bool, error) {
			order = append(order, "compact")
			if sessionID != "s" || pendingInput != "incoming user turn" {
				t.Fatalf("auto compact args session=%q input=%q", sessionID, pendingInput)
			}
			return Result{Trigger: "auto", Strategy: "remote_v2"}, true, nil
		},
		Assemble: func(_ context.Context, _ hook.HookContext, input string) (AssemblyResult, bool, error) {
			order = append(order, "assemble")
			if input != "incoming user turn" {
				t.Fatalf("assemble input=%q", input)
			}
			return AssemblyResult{}, true, nil
		},
	})
	if err != nil || !out.Did || !out.HasSnap {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if got := strings.Join(order, ","); got != "compact,assemble" {
		t.Fatalf("preflight order=%q", got)
	}
}
