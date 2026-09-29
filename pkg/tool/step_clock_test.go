package tool

import (
	"context"
	"testing"
	"time"
)

func TestStepHookStampsStartedAtAcrossACallsEvents(t *testing.T) {
	var got []StepEvent
	hook := withStepClock(func(_ context.Context, evt StepEvent) { got = append(got, evt) })
	ctx := context.Background()
	hook(ctx, StepEvent{Kind: StepKindToolStarted, StepID: "s1"})
	time.Sleep(5 * time.Millisecond)
	hook(ctx, StepEvent{Kind: StepKindToolOutputDelta, StepID: "s1"})
	hook(ctx, StepEvent{Kind: StepKindToolCompleted, StepID: "s1"})
	if got[0].StartedAt.IsZero() || !got[1].StartedAt.Equal(got[0].StartedAt) || !got[2].StartedAt.Equal(got[0].StartedAt) {
		t.Fatalf("start not shared across the call's events: %v", got)
	}
	hook(ctx, StepEvent{Kind: StepKindToolStarted, StepID: "s1"})
	if got[3].StartedAt.Equal(got[0].StartedAt) {
		t.Fatal("a new attempt reused the settled call's start")
	}
}

func TestBuildToolMetaCarriesStartOnlyWhileRunning(t *testing.T) {
	start := time.Now().Add(-90 * time.Second)
	run := BuildToolMeta(StepEvent{Kind: StepKindToolOutputDelta, StepID: "s2", ToolName: "shell", StartedAt: start})
	d, ok := run.RunningFor(time.Now())
	if !ok || d < 89*time.Second {
		t.Fatalf("running meta lacks elapsed: %v %v", d, ok)
	}
	done := BuildToolMeta(StepEvent{Kind: StepKindToolCompleted, StepID: "s2", ToolName: "shell", StartedAt: start})
	if done.StartedAtMs != 0 {
		t.Fatalf("settled meta still carries a start: %d", done.StartedAtMs)
	}
}
