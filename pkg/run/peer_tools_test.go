package run

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	toolpkg "github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// peerFixture is one conversation with its agents: a real ledger, the
// conversation's queue on a real controller, and the tool table both sides are
// handed.
type peerFixture struct {
	fac         Factory
	owner       *Runner
	root        string
	agent       *agent.Agent
	projectType string
}

// newPeerFixture builds a primary agent's tool table for one conversation.
func newPeerFixture(t *testing.T, projectType string) *peerFixture {
	t.Helper()
	root := t.TempDir()
	owner := &Runner{
		Deps:    &Deps{Home: root, WorkspaceRoot: root, ProjectType: projectType, AppCfg: subagentsEnabledConfig()},
		Control: NewController(),
	}
	fac := Factory{AgentName: "main", Home: root, WorkspaceRoot: root, Tools: toolpkg.NewState(t.TempDir()), AppCfg: subagentsEnabledConfig()}
	fac.Owner = owner
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, fac); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	return &peerFixture{fac: fac, owner: owner, root: root, agent: a, projectType: projectType}
}

// record appends one subagent to this conversation's ledger and returns it.
func (f *peerFixture) record(t *testing.T, taskID, conversationSessionID string, status agent.Status) agent.HistoryEntry {
	t.Helper()
	entry := agent.HistoryEntry{
		TaskID:          taskID,
		AgentType:       "explore",
		SessionID:       conversationSessionID,
		WorkerSessionID: "ws-" + taskID,
		Status:          status,
		Title:           "Map the router",
	}
	if err := agent.AppendHistory(f.root, entry); err != nil {
		t.Fatalf("append history: %v", err)
	}
	return entry
}

// running marks the subagent's channel as executing, the state a message may
// steer into.
func (f *peerFixture) running(t *testing.T, entry agent.HistoryEntry) *subagentChannel {
	t.Helper()
	ch := subagentChannelFor(f.fac, entry)
	ch.mu.Lock()
	ch.running = &subagentExecution{executionID: "exec-" + entry.TaskID}
	ch.mu.Unlock()
	return ch
}

// call invokes one of the fixture's registered tools the way the model does.
func (f *peerFixture) call(t *testing.T, ctx context.Context, name string, args any) (string, error) {
	t.Helper()
	registered, ok := aToolByName(f.agent, name)
	if !ok {
		t.Fatalf("%s is not registered", name)
	}
	payload := ""
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		payload = string(b)
	}
	result, err := registered.Handle(ctx, payload)
	if err != nil {
		return "", err
	}
	text, _ := result.(string)
	return text, nil
}

// primaryContext is the dispatching agent's tool-call context.
func primaryContext(conversationSessionID string) context.Context {
	return toolpkg.WithConversationSessionID(context.Background(), conversationSessionID)
}

// subagentContext is a subagent's tool-call context: a subagent run, its own
// roster key as its name, and the conversation that dispatched it.
func subagentContext(taskID, conversationSessionID string) context.Context {
	ctx := toolpkg.WithSubagentRun(context.Background(), true)
	ctx = toolpkg.WithHookAgentID(ctx, taskID)
	return toolpkg.WithConversationSessionID(ctx, conversationSessionID)
}

func decodePeerAnswer(t *testing.T, text string) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("answer %q is not JSON: %v", text, err)
	}
	return out
}

// A message from the dispatching agent reaches a subagent that is running now,
// wrapped so the recipient reads who wrote it. It is a message, not a surface
// joining the conversation: nothing about the subagent's own view changes.
func TestPeerSendReachesARunningSubagent(t *testing.T) {
	f := newPeerFixture(t, state.ProjectTypeCoding)
	entry := f.record(t, "task-peer-run", "conv-peer-run", agent.StatusRunning)
	ch := f.running(t, entry)
	rt := NewTurnInputRuntime()
	ch.queue.Attach(rt)
	surface := SubagentSurface{
		Frame:          func(ctx context.Context) context.Context { return ctx },
		OnReturned:     func(string, []Input) {},
		OnQueueChanged: func(string, QueuePreview) {},
	}
	ch.setSurface(surface)
	before := reflect.ValueOf(ch.surface.OnReturned).Pointer()

	answer, err := f.call(t, primaryContext("conv-peer-run"), "peer_send", map[string]any{
		"to": "task-peer-run", "message": "what have you found so far?",
	})
	if err != nil {
		t.Fatalf("peer_send: %v", err)
	}
	if got := decodePeerAnswer(t, answer)["status"]; got != "delivered" {
		t.Fatalf("status = %v, want delivered", got)
	}
	preview := ch.queue.Preview()
	if len(preview.Steers) != 1 {
		t.Fatalf("queued steers = %#v, want the message", preview.Steers)
	}
	text := preview.Steers[0]
	if !strings.HasPrefix(text, `<agent-message from="parent">`) || !strings.HasSuffix(text, "</agent-message>") {
		t.Fatalf("delivered text = %q, want it wrapped as a peer message", text)
	}
	if !strings.Contains(text, "what have you found so far?") {
		t.Fatalf("delivered text = %q, want the message itself", text)
	}
	ch.mu.Lock()
	after := reflect.ValueOf(ch.surface.OnReturned).Pointer()
	ch.mu.Unlock()
	if before != after {
		t.Fatal("peer_send replaced the subagent's surface: it is a message, not a view joining the conversation")
	}
}

// An idle subagent takes no messages: it handed its result back, and what it
// does next is the dispatching agent's decision.
func TestPeerSendRefusesAnIdleSubagent(t *testing.T) {
	f := newPeerFixture(t, state.ProjectTypeCoding)
	entry := f.record(t, "task-peer-idle", "conv-peer-idle", agent.StatusOK)
	ch := subagentChannelFor(f.fac, entry)
	rt := NewTurnInputRuntime()
	ch.queue.Attach(rt)

	answer, err := f.call(t, primaryContext("conv-peer-idle"), "peer_send", map[string]any{
		"to": "task-peer-idle", "message": "are you there?",
	})
	if err != nil {
		t.Fatalf("peer_send: %v", err)
	}
	decoded := decodePeerAnswer(t, answer)
	if decoded["status"] != "idle" {
		t.Fatalf("status = %v, want idle", decoded["status"])
	}
	if hint, _ := decoded["hint"].(string); !strings.Contains(hint, "subagent_continue") {
		t.Fatalf("hint = %q, want it to name subagent_continue", hint)
	}
	if preview := ch.queue.Preview(); len(preview.Steers) != 0 || len(preview.Rejected) != 0 {
		t.Fatalf("an idle subagent took the message: %#v", preview)
	}
}

// A subagent's answer reaches the conversation that dispatched it even while
// that conversation is closed to people: a batch task refuses the person
// watching it, and still hears the agent it dispatched.
func TestPeerSendFromSubagentReachesAClosedConversation(t *testing.T) {
	f := newPeerFixture(t, state.ProjectTypeBatch)
	entry := f.record(t, "task-peer-child", "conv-peer-batch", agent.StatusRunning)
	f.running(t, entry)
	queue := f.owner.Control.SessionQueue("conv-peer-batch")
	rt := NewTurnInputRuntime()
	queue.Attach(rt)
	queue.Close()

	answer, err := f.call(t, subagentContext("task-peer-child", "conv-peer-batch"), "peer_send", map[string]any{
		"to": "parent", "message": "the router lives in pkg/run",
	})
	if err != nil {
		t.Fatalf("peer_send: %v", err)
	}
	if got := decodePeerAnswer(t, answer)["status"]; got != "delivered" {
		t.Fatalf("status = %v, want delivered through the closed queue", got)
	}
	preview := queue.Preview()
	if len(preview.Steers) != 1 {
		t.Fatalf("queued steers = %#v, want the subagent's answer", preview.Steers)
	}
	if text := preview.Steers[0]; !strings.HasPrefix(text, `<agent-message from="task-peer-child">`) {
		t.Fatalf("delivered text = %q, want it wrapped with the subagent's own name", text)
	}
	if queue.Steer(Input{Text: "a person at the terminal"}) {
		t.Fatal("the same closed queue must still refuse a person's steer")
	}
}

// A batch conversation refuses the person watching it and nothing else: the
// entrance a surface uses still answers ErrBatchTakesNoInput, while the agent
// that dispatched the subagent can ask it something.
func TestPeerSendBetweenAgentsOfABatchConversation(t *testing.T) {
	f := newPeerFixture(t, state.ProjectTypeBatch)
	entry := f.record(t, "task-peer-batch", "conv-peer-batch-2", agent.StatusRunning)
	ch := f.running(t, entry)
	ch.queue.Attach(NewTurnInputRuntime())

	if _, err := SendToSubagent(context.Background(), f.owner, SubagentSurface{}, "conv-peer-batch-2", "task-peer-batch", Input{Text: "hi"}); !errors.Is(err, ErrBatchTakesNoInput) {
		t.Fatalf("SendToSubagent in a batch conversation = %v, want ErrBatchTakesNoInput", err)
	}
	answer, err := f.call(t, primaryContext("conv-peer-batch-2"), "peer_send", map[string]any{
		"to": "task-peer-batch", "message": "how far along are you?",
	})
	if err != nil {
		t.Fatalf("peer_send: %v", err)
	}
	if got := decodePeerAnswer(t, answer)["status"]; got != "delivered" {
		t.Fatalf("status = %v, want delivered", got)
	}
	if preview := ch.queue.Preview(); len(preview.Steers) != 1 {
		t.Fatalf("queued steers = %#v, want the dispatching agent's message", preview.Steers)
	}
}

// The roster is the address book: the dispatching agent sees the subagents of
// its own conversation with the ones at work first, and a subagent sees the one
// agent that dispatched it.
func TestPeerListNamesTheAgentsOneCanMessage(t *testing.T) {
	f := newPeerFixture(t, state.ProjectTypeCoding)
	finished := f.record(t, "task-peer-done", "conv-peer-list", agent.StatusOK)
	finished.UpdatedAt = time.Now().Add(time.Minute).Unix()
	if err := agent.AppendHistory(f.root, finished); err != nil {
		t.Fatalf("append history: %v", err)
	}
	running := f.record(t, "task-peer-busy", "conv-peer-list", agent.StatusRunning)
	f.running(t, running)
	f.record(t, "task-other-conversation", "conv-someone-else", agent.StatusRunning)

	text, err := f.call(t, primaryContext("conv-peer-list"), "peer_list", map[string]any{})
	if err != nil {
		t.Fatalf("peer_list: %v", err)
	}
	answer := decodePeerAnswer(t, text)
	if answer["self"] != peerParentName {
		t.Fatalf("self = %v, want %q", answer["self"], peerParentName)
	}
	agents, _ := answer["agents"].([]any)
	if len(agents) != 2 {
		t.Fatalf("agents = %#v, want this conversation's two subagents", agents)
	}
	first, _ := agents[0].(map[string]any)
	if first["name"] != "task-peer-busy" || first["status"] != "running" {
		t.Fatalf("agents[0] = %#v, want the running subagent first", first)
	}
	if first["kind"] != "explore" || first["task"] != "Map the router" {
		t.Fatalf("agents[0] = %#v, want its type and title", first)
	}
	second, _ := agents[1].(map[string]any)
	if second["name"] != "task-peer-done" || second["status"] != "finished" {
		t.Fatalf("agents[1] = %#v, want the finished subagent last", second)
	}

	text, err = f.call(t, subagentContext("task-peer-busy", "conv-peer-list"), "peer_list", map[string]any{})
	if err != nil {
		t.Fatalf("peer_list from a subagent: %v", err)
	}
	answer = decodePeerAnswer(t, text)
	if answer["self"] != "task-peer-busy" {
		t.Fatalf("self = %v, want the subagent's own name", answer["self"])
	}
	agents, _ = answer["agents"].([]any)
	if len(agents) != 1 {
		t.Fatalf("agents = %#v, want exactly the dispatcher", agents)
	}
	parent, _ := agents[0].(map[string]any)
	if parent["name"] != peerParentName || parent["kind"] != "primary" {
		t.Fatalf("agents[0] = %#v, want the dispatching agent", parent)
	}
}

// A wait stops sleeping when one of the conversation's subagents writes: the
// message is already steered into the conversation, so the dispatching model
// reads it at its next tool boundary instead of after the task it was waiting
// on.
func TestSubagentWaitReturnsWhenASubagentWrites(t *testing.T) {
	f := newPeerFixture(t, state.ProjectTypeCoding)
	entry := f.record(t, "task-peer-wait", "conv-peer-wait", agent.StatusRunning)
	handle := agent.RegistryFor(f.fac.subagentScopeRoot()).Start(entry, func() {})

	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	done := make(chan subagentWaitResult, 1)
	go func() { done <- awaitSubagent(handle, waitCtx, "conv-peer-wait") }()
	time.Sleep(20 * time.Millisecond)
	signalPeerMessage("conv-peer-wait")

	select {
	case result := <-done:
		if !result.messaged {
			t.Fatalf("wait result = %#v, want it ended by a subagent's message", result)
		}
		if result.timedOut {
			t.Fatal("the wait reported a timeout for a message that ended it")
		}
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Fatalf("wait took %v, want it to return on the message", elapsed)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("the wait did not return when a subagent wrote")
	}
}

// The wait tool itself answers with the progress and the arrival flag, and its
// four descriptions are the stable text the prompt prefix is built from.
func TestSubagentWaitReportsAMessageFromASubagent(t *testing.T) {
	f := newPeerFixture(t, state.ProjectTypeCoding)
	entry := f.record(t, "task-peer-wait-tool", "conv-peer-wait-tool", agent.StatusRunning)
	agent.RegistryFor(f.fac.subagentScopeRoot()).Start(entry, func() {})
	go func() {
		time.Sleep(20 * time.Millisecond)
		signalPeerMessage("conv-peer-wait-tool")
	}()

	text, err := f.call(t, primaryContext("conv-peer-wait-tool"), "subagent_wait", map[string]any{
		"task_id": "task-peer-wait-tool", "timeout_ms": 8000,
	})
	if err != nil {
		t.Fatalf("subagent_wait: %v", err)
	}
	answer := decodePeerAnswer(t, text)
	if answer["message_from_subagent"] != true {
		t.Fatalf("answer = %#v, want message_from_subagent", answer)
	}
	if _, ok := answer["record"]; !ok {
		t.Fatalf("answer = %#v, want the record", answer)
	}
	if _, ok := answer["progress"]; !ok {
		t.Fatalf("answer = %#v, want the progress", answer)
	}
}

func TestPeerToolDescriptionsAreTheStableText(t *testing.T) {
	f := newPeerFixture(t, state.ProjectTypeCoding)
	want := map[string]string{
		"peer_list":       "List the agents you can message with peer_send, with each one's status and task. The name is the address: pass it to peer_send exactly as printed.",
		"peer_send":       "Send a message to an agent listed by peer_list. A running agent receives it at its next tool boundary. Your idle subagent takes no messages: use subagent_continue to give it new work. A reply arrives as a message from that agent.",
		"subagent_status": "Use when checking on a subagent without interrupting it: returns its status and live progress (current step, recent steps, todos, latest message, token usage) by task_id or run_id. If both are empty, returns the latest scoped subagent.",
		"subagent_wait":   "Use when blocking until a previously-sent subagent finishes: waits by task_id or run_id. Returns the latest status and progress even when the wait times out, and returns early when one of your subagents sends you a message.",
		"subagent_list":   "List recent subagents scoped to the current session and parent run when available, each with a short progress summary.",
	}
	for name, description := range want {
		registered, ok := aToolByName(f.agent, name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if got := registered.Description(); got != description {
			t.Fatalf("%s description =\n%q\nwant\n%q", name, got, description)
		}
		meta, ok := f.fac.Tools.ToolMetaByName(name)
		if !ok {
			t.Fatalf("%s has no catalog entry", name)
		}
		if meta.Description != description {
			t.Fatalf("%s catalog description drifted from the tool's", name)
		}
	}
	peerMeta, _ := f.fac.Tools.ToolMetaByName("peer_list")
	if peerMeta.Category != "session" || !peerMeta.ReadOnly || !peerMeta.ConcurrencySafe {
		t.Fatalf("peer_list meta = %#v, want a read-only session tool", peerMeta)
	}
	sendMeta, _ := f.fac.Tools.ToolMetaByName("peer_send")
	if sendMeta.Category != "session" || !sendMeta.ReadOnly {
		t.Fatalf("peer_send meta = %#v, want a read-only session tool", sendMeta)
	}
	// The arguments a model fills in are part of the cached schema, so their
	// names are pinned here with the descriptions.
	if schema := string(sendMeta.InputSchema); !strings.Contains(schema, `"to"`) ||
		!strings.Contains(schema, `"message"`) || !strings.Contains(schema, "required") {
		t.Fatalf("peer_send schema = %s, want to and message required", schema)
	}
	if schema := string(peerMeta.InputSchema); !strings.Contains(schema, `"properties":{}`) ||
		strings.Contains(schema, `"required"`) {
		t.Fatalf("peer_list schema = %s, want no arguments", schema)
	}
}

// The two peer tools are not part of the subagent family: the family's name
// list and the one-level guard must not have taken them over, or a subagent
// could not answer at all.
func TestPeerToolsAreNotSubagentManagementTools(t *testing.T) {
	for _, name := range []string{"peer_list", "peer_send"} {
		if subagentToolName(name) {
			t.Fatalf("%s is in the subagent family's name list", name)
		}
		if toolpkg.GuardToolNameOnly(toolpkg.WithSubagentRun(context.Background(), true), name) != nil {
			t.Fatalf("%s is refused inside a subagent run: it is not a management tool", name)
		}
	}
}

// fakePeerDirectory stands in for the other processes' agents: it answers with
// whatever listing a case needs and records what the tools asked it to send.
type fakePeerDirectory struct {
	listing  PeerListing
	listErr  error
	sendErr  error
	result   PeerSendResult
	listAsk  []PeerCaller
	sentTo   []string
	sentBody []string
	sentAs   []PeerCaller

	// The same-tree guards (plan 198) ask the same fake. A case that does not
	// set these gets the quiet answers: nobody in the tree, nothing touched.
	workTree []toolpkg.WorkTreePeer
	touches  []toolpkg.PeerTouch
	noted    []string
	roots    []string
}

func (d *fakePeerDirectory) WorkTreePeers(_ context.Context, root string) []toolpkg.WorkTreePeer {
	d.roots = append(d.roots, root)
	return d.workTree
}

func (d *fakePeerDirectory) NoteWrite(_ context.Context, absPath string, _ time.Time) {
	d.noted = append(d.noted, absPath)
}

func (d *fakePeerDirectory) TouchesBetween(_ context.Context, root string, _, _ time.Time) []toolpkg.PeerTouch {
	d.roots = append(d.roots, root)
	return d.touches
}

func (d *fakePeerDirectory) List(_ context.Context, from PeerCaller) (PeerListing, error) {
	d.listAsk = append(d.listAsk, from)
	return d.listing, d.listErr
}

func (d *fakePeerDirectory) Send(_ context.Context, from PeerCaller, to, message string) (PeerSendResult, error) {
	d.sentAs = append(d.sentAs, from)
	d.sentTo = append(d.sentTo, to)
	d.sentBody = append(d.sentBody, message)
	return d.result, d.sendErr
}

// withPeers hands the fixture's runner the port the tools reach the other
// processes through, the way the composition root does.
func (f *peerFixture) withPeers(port PeerDirectory) *fakePeerDirectory {
	dir, _ := port.(*fakePeerDirectory)
	f.owner.Deps.Peers = port
	return dir
}

// The roster is one list: the conversation's own subagents and the agents of
// the other processes, with who is where and what they are doing.
func TestPeerListMergesTheAgentsOfOtherProcesses(t *testing.T) {
	f := newPeerFixture(t, state.ProjectTypeCoding)
	f.record(t, "task-peer-local", "conv-peer-merge", agent.StatusRunning)
	f.withPeers(&fakePeerDirectory{listing: PeerListing{Peers: []PeerEntry{
		{Name: "elsewhere-1", Kind: "terminal", Status: "busy", Task: "Refactoring the importer", Cwd: "/repo/shared", SameTree: true, ActiveAgo: 90 * time.Second},
		{Name: "elsewhere-2", Kind: "gateway conversation", Status: "idle", Cwd: "/elsewhere", ActiveAgo: 3 * time.Hour},
	}}})

	text, err := f.call(t, primaryContext("conv-peer-merge"), "peer_list", map[string]any{})
	if err != nil {
		t.Fatalf("peer_list: %v", err)
	}
	answer := decodePeerAnswer(t, text)
	agents, _ := answer["agents"].([]any)
	if len(agents) != 3 {
		t.Fatalf("agents = %#v, want the local subagent and both peers", agents)
	}
	local, _ := agents[0].(map[string]any)
	if local["name"] != "task-peer-local" {
		t.Fatalf("agents[0] = %#v, want the conversation's own subagent first", local)
	}
	near, _ := agents[1].(map[string]any)
	if near["name"] != "elsewhere-1" || near["kind"] != "terminal" || near["same_tree"] != true {
		t.Fatalf("agents[1] = %#v, want the same-tree peer with its kind", near)
	}
	if near["active_ago"] != "1m" || near["cwd"] != "/repo/shared" {
		t.Fatalf("agents[1] = %#v, want where it works and when it last did", near)
	}
	far, _ := agents[2].(map[string]any)
	if far["active_ago"] != "3h" || far["same_tree"] != nil {
		t.Fatalf("agents[2] = %#v, want the older peer without same_tree", far)
	}
	if _, ok := answer["note"]; ok {
		t.Fatalf("answer = %#v, want no empty-roster note", answer)
	}
}

// A name the conversation does not own is looked for in the other processes,
// and the answer names the recipient the way the listing printed it.
func TestPeerSendGoesToAnAgentOfAnotherProcess(t *testing.T) {
	f := newPeerFixture(t, state.ProjectTypeCoding)
	dir := f.withPeers(&fakePeerDirectory{result: PeerSendResult{Name: "elsewhere-1", Status: "busy"}})

	answer, err := f.call(t, primaryContext("conv-peer-cross"), "peer_send", map[string]any{
		"to": "elsewhere-1", "message": "I am about to edit pkg/run/runner.go",
	})
	if err != nil {
		t.Fatalf("peer_send: %v", err)
	}
	decoded := decodePeerAnswer(t, answer)
	if decoded["status"] != "delivered" || decoded["to"] != "elsewhere-1" || decoded["peer_status"] != "busy" {
		t.Fatalf("answer = %#v, want a delivered message to the peer's own status", decoded)
	}
	if len(dir.sentTo) != 1 || dir.sentTo[0] != "elsewhere-1" {
		t.Fatalf("sent = %#v, want one message to elsewhere-1", dir.sentTo)
	}
	if dir.sentBody[0] != "I am about to edit pkg/run/runner.go" {
		t.Fatalf("body = %q, want the message alone: the wire carries its own envelope", dir.sentBody[0])
	}
	if len(dir.sentAs) != 1 || dir.sentAs[0].SessionID != "conv-peer-cross" || dir.sentAs[0].AgentKey != "" {
		t.Fatalf("caller = %#v, want this conversation and no subagent", dir.sentAs)
	}
}

// A subagent reaches the other processes too, and says who it is: the roster
// key is what the recipient sees beside the conversation.
func TestPeerSendFromASubagentGoesToAnotherProcess(t *testing.T) {
	f := newPeerFixture(t, state.ProjectTypeCoding)
	dir := f.withPeers(&fakePeerDirectory{result: PeerSendResult{Name: "elsewhere-1", Status: "idle"}})
	f.record(t, "task-peer-cross-child", "conv-peer-cross-child", agent.StatusRunning)

	answer, err := f.call(t, subagentContext("task-peer-cross-child", "conv-peer-cross-child"), "peer_send", map[string]any{
		"to": "elsewhere-1", "message": "the importer is done",
	})
	if err != nil {
		t.Fatalf("peer_send: %v", err)
	}
	if got := decodePeerAnswer(t, answer)["status"]; got != "delivered" {
		t.Fatalf("status = %v, want delivered", got)
	}
	if len(dir.sentAs) != 1 || dir.sentAs[0].AgentKey != "task-peer-cross-child" {
		t.Fatalf("caller = %#v, want the subagent's roster key", dir.sentAs)
	}
}

// A batch task talks to its own agents and to nobody else, even though the
// process it runs in can see the machine.
func TestPeerToolsStayInsideABatchConversation(t *testing.T) {
	f := newPeerFixture(t, state.ProjectTypeBatch)
	dir := f.withPeers(&fakePeerDirectory{listing: PeerListing{Peers: []PeerEntry{
		{Name: "elsewhere-1", Kind: "terminal", Status: "busy"},
	}}})
	f.record(t, "task-peer-batch-local", "conv-peer-batch-only", agent.StatusRunning)

	text, err := f.call(t, primaryContext("conv-peer-batch-only"), "peer_list", map[string]any{})
	if err != nil {
		t.Fatalf("peer_list: %v", err)
	}
	agents, _ := decodePeerAnswer(t, text)["agents"].([]any)
	if len(agents) != 1 {
		t.Fatalf("agents = %#v, want only the task's own subagent", agents)
	}
	if len(dir.listAsk) != 0 {
		t.Fatalf("the batch task asked the machine for its agents: %#v", dir.listAsk)
	}

	_, err = f.call(t, primaryContext("conv-peer-batch-only"), "peer_send", map[string]any{
		"to": "elsewhere-1", "message": "hi",
	})
	if err == nil || !strings.Contains(err.Error(), "batch tasks talk only to their own agents") {
		t.Fatalf("peer_send to another process = %v, want the batch refusal", err)
	}
	if len(dir.sentTo) != 0 {
		t.Fatalf("the batch task sent %#v, want nothing sent", dir.sentTo)
	}
}

// Without a port — a platform that has no peer messaging — the two tools are
// still registered and still work inside the conversation; the only names that
// resolve are the conversation's own.
func TestPeerToolsNameOnlyTheirOwnAgentsWithoutAPort(t *testing.T) {
	f := newPeerFixture(t, state.ProjectTypeCoding)
	f.record(t, "task-peer-solo", "conv-peer-solo", agent.StatusRunning)

	text, err := f.call(t, primaryContext("conv-peer-solo"), "peer_list", map[string]any{})
	if err != nil {
		t.Fatalf("peer_list: %v", err)
	}
	if agents, _ := decodePeerAnswer(t, text)["agents"].([]any); len(agents) != 1 {
		t.Fatalf("agents = %#v, want the conversation's own subagent", agents)
	}
	_, err = f.call(t, primaryContext("conv-peer-solo"), "peer_send", map[string]any{
		"to": "elsewhere-1", "message": "hi",
	})
	if err == nil || !strings.Contains(err.Error(), `no agent named "elsewhere-1"; call peer_list`) {
		t.Fatalf("peer_send to an unknown name = %v, want the plain refusal", err)
	}
}

// The two descriptions are compiled in and cached: neither may name a vendor,
// a model or another agent's product.
func TestPeerToolDescriptionsNameNoVendor(t *testing.T) {
	for _, description := range []string{peerListDescription, peerSendDescription} {
		for _, brand := range []string{"Claude", "Anthropic", "OpenAI", "Codex", "GPT", "Gemini", "DeepSeek"} {
			if strings.Contains(description, brand) {
				t.Fatalf("description %q names %q", description, brand)
			}
		}
	}
}
