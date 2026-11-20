package run

import (
	"context"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/peer"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// A letter another session wrote, handed to a running subagent, reaches that
// subagent's view as the letter it is: the delivered event keeps the origin
// that says so, and the words as the sender wrote them, newlines included. The
// surface draws its card from the first and its body from the second, so a
// flattened or unmarked payload is what made a peer message read as the
// person's own typed line.
func TestSubagentDeliveredPayloadKeepsPeerOriginAndNewlines(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system")
	owner := fix.fac.Owner
	owner.Control = NewController()
	probe := &channelEventProbe{}
	owner.Events = probe
	release := make(chan struct{})
	ex := &channelTestExecutor{store: fix.store, hold: true, release: release, answer: "continued"}
	owner.SubagentExecutor = ex
	conv := "conv-peer-origin"
	worker := "main:conv-peer-origin:worker:00000000-0000-0000-0000-0000000004f0"
	fix.seedWorker(t, conv, worker, []llm.Message{llm.UserMessage(llm.Text("seed"))})
	seedChannelRecord(t, fix.fac, agent.HistoryEntry{
		TaskID: "task-peer-origin", RunID: "seed-run", ParentRunID: "parent-1", SessionID: conv, WorkerSessionID: worker,
		Task: "work", Status: agent.StatusOK, AgentType: "explore", AgentKind: "typed", RuntimeKind: "typed_subagent",
		Continuable: true, StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
	})
	if err := StartSubagentAutoContinue(context.Background(), owner, SubagentSurface{}, conv, "task-peer-origin", Input{Text: "start"}); err != nil {
		t.Fatalf("StartSubagentAutoContinue: %v", err)
	}
	waitSubagentBusy(t, owner, conv, "task-peer-origin")
	if !PauseSubagent(owner, conv, "task-peer-origin") {
		t.Fatal("PauseSubagent = false on a running subagent")
	}
	waitProbeEvent(t, probe, event.RunEventSubagentPaused, 1)
	// Built by the protocol: the envelope a socket delivers, whose body keeps
	// the paragraphs its sender set down.
	envelope := peer.Build(peer.Envelope{From: "uds:/tmp/a.sock", FromName: "builder", Body: "please review a.go\nsecond line"})
	// The resumed attempt answers at once.
	close(release)
	if _, err := SteerLiveSubagent(owner, conv, "task-peer-origin", Input{
		Parts:  []llm.ContentPart{llm.Text(envelope)},
		Origin: state.MessageOriginPeer,
	}); err != nil {
		t.Fatalf("peer message to the paused subagent: %v", err)
	}
	waitProbeEvent(t, probe, event.RunEventSubagentEnded, 1)
	waitSubagentIdle(t, owner, conv, "task-peer-origin")

	delivered := probe.inputDelivered()
	if len(delivered) != 1 {
		t.Fatalf("subagent_input_delivered published %d times, want 1: %#v", len(delivered), delivered)
	}
	if delivered[0].Origin != state.MessageOriginPeer {
		t.Fatalf("delivered payload origin = %q, want %q: the view cannot tell a peer's letter from the person's words without it", delivered[0].Origin, state.MessageOriginPeer)
	}
	if delivered[0].Text != envelope {
		t.Fatalf("delivered payload text = %q, want the letter verbatim (%q)", delivered[0].Text, envelope)
	}
	if !strings.Contains(delivered[0].Text, "please review a.go\nsecond line") {
		t.Fatalf("delivered payload text = %q, want its newlines kept: a flattened body reads as one run-on line", delivered[0].Text)
	}
}

// The person's own message to a running subagent keeps no origin: their words
// are drawn as theirs, and only another agent's letter is marked.
func TestSubagentDeliveredPayloadLeavesTheUsersOwnMessageUnmarked(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system")
	owner := fix.fac.Owner
	owner.Control = NewController()
	probe := &channelEventProbe{}
	owner.Events = probe
	release := make(chan struct{})
	ex := &channelTestExecutor{store: fix.store, hold: true, release: release, answer: "continued"}
	owner.SubagentExecutor = ex
	conv := "conv-peer-origin-user"
	worker := "main:conv-peer-origin-user:worker:00000000-0000-0000-0000-0000000004f1"
	fix.seedWorker(t, conv, worker, []llm.Message{llm.UserMessage(llm.Text("seed"))})
	seedChannelRecord(t, fix.fac, agent.HistoryEntry{
		TaskID: "task-origin-user", RunID: "seed-run", ParentRunID: "parent-1", SessionID: conv, WorkerSessionID: worker,
		Task: "work", Status: agent.StatusOK, AgentType: "explore", AgentKind: "typed", RuntimeKind: "typed_subagent",
		Continuable: true, StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
	})
	if err := StartSubagentAutoContinue(context.Background(), owner, SubagentSurface{}, conv, "task-origin-user", Input{Text: "start"}); err != nil {
		t.Fatalf("StartSubagentAutoContinue: %v", err)
	}
	waitSubagentBusy(t, owner, conv, "task-origin-user")
	if !PauseSubagent(owner, conv, "task-origin-user") {
		t.Fatal("PauseSubagent = false on a running subagent")
	}
	waitProbeEvent(t, probe, event.RunEventSubagentPaused, 1)
	close(release)
	// What a subagent's own view sends: the person's words in the parts, the
	// preview line the queue keeps in the text.
	if _, err := SendToSubagent(context.Background(), owner, SubagentSurface{}, conv, "task-origin-user", Input{
		Text:  "go on take your time",
		Parts: []llm.ContentPart{llm.Text("go on\ntake your time")},
	}); err != nil {
		t.Fatalf("message to the paused subagent: %v", err)
	}
	waitProbeEvent(t, probe, event.RunEventSubagentEnded, 1)
	waitSubagentIdle(t, owner, conv, "task-origin-user")

	delivered := probe.inputDelivered()
	if len(delivered) != 1 {
		t.Fatalf("subagent_input_delivered published %d times, want 1: %#v", len(delivered), delivered)
	}
	if delivered[0].Origin != "" {
		t.Fatalf("delivered payload origin = %q, want empty: the person typed this message", delivered[0].Origin)
	}
	if !strings.Contains(delivered[0].Text, "go on\ntake your time") {
		t.Fatalf("delivered payload text = %q, want the person's own line breaks kept", delivered[0].Text)
	}
}
