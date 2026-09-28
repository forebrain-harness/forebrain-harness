package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// ScenarioResult is the stable application-level record used by cross-surface
// tests. It intentionally excludes terminal layout and wire encoding.
type ScenarioResult struct {
	Messages    []state.Message
	Runs        []state.Run
	RunEvents   []event.RunEvent
	Actions     []state.Action
	Waits       []state.Wait
	Events      []event.RunEvent
	Permissions safety.Snapshot
}

// EventRecorder records canonical events in publish order.
type EventRecorder struct {
	mu     sync.Mutex
	events []event.RunEvent
}

// Publish implements event.Sink.
func (r *EventRecorder) Publish(_ context.Context, evt event.RunEvent) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.events = append(r.events, evt)
	r.mu.Unlock()
	return nil
}

// Events returns a copy safe for assertions.
func (r *EventRecorder) Events() []event.RunEvent {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]event.RunEvent(nil), r.events...)
}

// ScenarioSource supplies persisted state for CaptureScenario.
type ScenarioSource struct {
	Sessions    *state.SessionStore
	Runs        *state.RunStore
	Actions     *state.ActionService
	Events      *EventRecorder
	Permissions safety.Snapshot
}

// CaptureScenario reads one session's persisted side effects and event stream.
func CaptureScenario(ctx context.Context, src ScenarioSource, sessionID string) (ScenarioResult, error) {
	if src.Sessions == nil || src.Runs == nil {
		return ScenarioResult{}, fmt.Errorf("scenario source requires session and run stores")
	}
	res := ScenarioResult{Permissions: src.Permissions}
	var err error
	res.Messages, err = src.Sessions.ListAllMessages(ctx, sessionID, 10_000)
	if err != nil {
		return ScenarioResult{}, err
	}
	res.Runs, err = src.Runs.ListRunsBySession(ctx, sessionID, 10_000)
	if err != nil {
		return ScenarioResult{}, err
	}
	for _, run := range res.Runs {
		events, err := src.Runs.ListRunEvents(ctx, run.ID, 10_000)
		if err != nil {
			return ScenarioResult{}, err
		}
		for _, record := range events {
			res.RunEvents = append(res.RunEvents, event.RunEvent{
				ID: record.ID, Sequence: record.Sequence, SchemaVersion: event.RunEventSchemaVersion,
				RunID: record.RunID, SessionID: record.SessionID, Type: record.Type,
				Payload: append(json.RawMessage(nil), record.Payload...), CreatedAt: record.CreatedAt,
			})
		}
		wait, err := src.Runs.GetWaitForRun(ctx, run.ID)
		if err != nil && err != state.ErrRunNotFound {
			return ScenarioResult{}, err
		}
		if wait != nil {
			res.Waits = append(res.Waits, *wait)
		}
	}
	if src.Actions != nil {
		res.Actions, err = src.Actions.List(ctx, src.Sessions.AgentID(), state.ActionFilter{SessionID: sessionID}, 10_000)
		if err != nil {
			return ScenarioResult{}, err
		}
	}
	if src.Events != nil {
		res.Events = src.Events.Events()
	}
	sort.Slice(res.Runs, func(i, j int) bool { return res.Runs[i].CreatedAt < res.Runs[j].CreatedAt })
	sort.Slice(res.RunEvents, func(i, j int) bool {
		if res.RunEvents[i].RunID == res.RunEvents[j].RunID {
			return res.RunEvents[i].Sequence < res.RunEvents[j].Sequence
		}
		return res.RunEvents[i].RunID < res.RunEvents[j].RunID
	})
	return res, nil
}
