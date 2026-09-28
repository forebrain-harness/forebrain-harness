// Copyright 2026 Simone Vellei
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
)

// --- Event interface compliance ---

func TestEventTypes_ImplementEvent(t *testing.T) {
	now := time.Now()
	// Each concrete type must be assignable to agent.Event.
	events := []agent.Event{
		agent.AgentStartEvent{Timestamp: now},
		agent.AgentIterationEvent{Timestamp: now},
		agent.AgentRunSummaryEvent{Timestamp: now},
		agent.AgentEndEvent{Timestamp: now},
		agent.LLMRequestEvent{Timestamp: now},
		agent.LLMResponseEvent{Timestamp: now},
		agent.ToolCallEvent{Timestamp: now},
		agent.ToolResultEvent{Timestamp: now},
	}
	if len(events) != 8 {
		t.Fatalf("expected 8 event types, got %d", len(events))
	}
}

// --- NoopTracer ---

func TestNoopTracer_DiscardEvents(t *testing.T) {
	// Should not panic for any event type.
	n := agent.Noop
	n.Trace(agent.AgentStartEvent{AgentName: "x", Timestamp: time.Now()})
	n.Trace(agent.ToolCallEvent{AgentName: "x", ToolName: "y", Timestamp: time.Now()})
}

// --- Context propagation ---

func TestWithTracer_FromContext(t *testing.T) {
	var got []agent.Event
	spy := &spyTracer{fn: func(e agent.Event) { got = append(got, e) }}

	ctx := agent.WithTracer(context.Background(), spy)
	tracer := agent.FromContext(ctx)
	tracer.Trace(agent.AgentStartEvent{AgentName: "a"})

	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	if _, ok := got[0].(agent.AgentStartEvent); !ok {
		t.Errorf("expected AgentStartEvent, got %T", got[0])
	}
}

func TestFromContext_NoTracer_ReturnsNoop(t *testing.T) {
	tracer := agent.FromContext(context.Background())
	// Should not panic.
	tracer.Trace(agent.AgentEndEvent{AgentName: "a"})
}

// helpers

type spyTracer struct {
	fn func(agent.Event)
}

func (s *spyTracer) Trace(e agent.Event) { s.fn(e) }
