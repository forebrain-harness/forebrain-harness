package testutil

import (
	"context"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

func TestEventRecorderCopiesEvents(t *testing.T) {
	r := &EventRecorder{}
	if err := r.Publish(context.Background(), event.RunEvent{RunID: "r1", Type: event.RunEventTurnStarted}); err != nil {
		t.Fatal(err)
	}
	got := r.Events()
	if len(got) != 1 || got[0].RunID != "r1" {
		t.Fatalf("events = %#v", got)
	}
	got[0].RunID = "changed"
	if r.Events()[0].RunID != "r1" {
		t.Fatal("recorder returned its backing slice")
	}
}

func TestCaptureScenarioRequiresStores(t *testing.T) {
	if _, err := CaptureScenario(context.Background(), ScenarioSource{}, "s1"); err == nil {
		t.Fatal("expected missing-store error")
	}
}
