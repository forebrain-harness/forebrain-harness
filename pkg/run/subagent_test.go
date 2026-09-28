package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	toolpkg "github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// dispatchProbe captures what every subagent dispatch must produce, whoever
// started it: a spawn notification the roster builds a row from, a live handle
// the roster can stop, and an end notification that removes the row.
type dispatchProbe struct {
	spawned []spawnedMsg
	ended   []endedMsg
	endedCh chan endedMsg
}

type spawnedMsg struct {
	AgentID, AgentType, TaskID, Task string
}

type endedMsg struct {
	AgentID, AgentType, TaskID, Status, Error string
}

func (p *dispatchProbe) Publish(_ context.Context, evt event.RunEvent) error {
	switch evt.Type {
	case event.RunEventSubagentSpawned:
		var m event.SubagentSpawnedPayload
		if json.Unmarshal(evt.Payload, &m) == nil {
			p.spawned = append(p.spawned, spawnedMsg{AgentID: m.AgentID, AgentType: m.AgentType, TaskID: m.TaskID, Task: m.Task})
		}
	case event.RunEventSubagentEnded:
		var m event.SubagentEndedPayload
		if json.Unmarshal(evt.Payload, &m) == nil {
			row := endedMsg{AgentID: m.AgentID, AgentType: m.AgentType, TaskID: m.TaskID, Status: m.Status, Error: m.Error}
			p.ended = append(p.ended, row)
			if p.endedCh != nil {
				p.endedCh <- row
			}
		}
	}
	return nil
}

func newDispatchFactory(t *testing.T) (Factory, *dispatchProbe) {
	t.Helper()
	probe := &dispatchProbe{}
	home := t.TempDir()
	owner := &Runner{Deps: &Deps{Home: home}, Events: probe, SubagentExecutor: &fixedSubagentExecutor{output: "done"}}
	return Factory{AgentName: "main", Home: home, Owner: owner}, probe
}

func dispatchTestCtx() context.Context {
	return llm.WithAgentSessionID(context.Background(), "session-1")
}

// The blocking path: output comes back governed, and the roster sees the run
// open and close.
func TestDispatchSubagentAnnouncesAndFinishes(t *testing.T) {
	fac, probe := newDispatchFactory(t)
	prep, out, err := dispatchSubagent(dispatchTestCtx(), fac, subagentDispatch{
		taskID:  "task-sync",
		task:    "investigate",
		subtype: "explore",
		resolve: agent.ResolvePublicSubtype,
		run: func(_ context.Context, _ Factory, prep preparedSubagent) (string, error) {
			// The run is tagged for its own view before the executor is called.
			if got := toolpkg.HookAgentIDFromContext(typedSubagentExecContext(prep)); got != prep.entry.TaskID {
				t.Errorf("roster key = %q, want %q", got, prep.entry.TaskID)
			}
			return "findings", nil
		},
	})
	if err != nil {
		t.Fatalf("dispatchSubagent: %v", err)
	}
	if out != "findings" || prep.entry.TaskID != "task-sync" {
		t.Fatalf("out=%q entry=%#v", out, prep.entry)
	}
	if len(probe.spawned) != 1 || probe.spawned[0].AgentID != "task-sync" || probe.spawned[0].Task != "investigate" {
		t.Fatalf("spawn notifications = %#v", probe.spawned)
	}
	if len(probe.ended) != 1 || probe.ended[0].AgentID != "task-sync" ||
		!strings.EqualFold(probe.ended[0].Status, string(agent.StatusOK)) {
		t.Fatalf("end notifications = %#v", probe.ended)
	}
}

// A failing run still closes its row, and reports the failure rather than an
// empty success.
func TestDispatchSubagentReportsFailureAndStillClosesTheRow(t *testing.T) {
	fac, probe := newDispatchFactory(t)
	_, _, err := dispatchSubagent(dispatchTestCtx(), fac, subagentDispatch{
		taskID:  "task-fail",
		task:    "investigate",
		subtype: "explore",
		resolve: agent.ResolvePublicSubtype,
		run: func(context.Context, Factory, preparedSubagent) (string, error) {
			return "", errors.New("upstream 429")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "upstream 429") {
		t.Fatalf("err = %v", err)
	}
	if len(probe.ended) != 1 || !strings.EqualFold(probe.ended[0].Status, string(agent.StatusFailed)) {
		t.Fatalf("end notifications = %#v", probe.ended)
	}
}

func TestDispatchSubagentDoesNotRunWhenDurableRunCreationFails(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	probe := &dispatchProbe{}
	called := false
	fac := Factory{
		AgentName: "main",
		Home:      t.TempDir(),
		Owner: &Runner{
			Deps:    &Deps{RunRT: &state.RunStore{DB: db}},
			Control: NewController(),
			Events:  probe,
		},
	}
	_, _, err = dispatchSubagent(
		toolpkg.WithRunID(dispatchTestCtx(), "parent-run"),
		fac,
		subagentDispatch{
			taskID: "task-store-failure", task: "investigate", resolve: agent.ResolvePublicSubtype,
			run: func(context.Context, Factory, preparedSubagent) (string, error) {
				called = true
				return "must not run", nil
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "persist subagent run") {
		t.Fatalf("err = %v", err)
	}
	if called || len(probe.spawned) != 0 {
		t.Fatalf("unrecoverable subagent was exposed: called=%v spawned=%#v", called, probe.spawned)
	}
}

// A panic inside a subagent must not take the calling turn down with it.
func TestDispatchSubagentTurnsAPanicIntoAnError(t *testing.T) {
	fac, probe := newDispatchFactory(t)
	_, _, err := dispatchSubagent(dispatchTestCtx(), fac, subagentDispatch{
		taskID:  "task-panic",
		task:    "investigate",
		subtype: "explore",
		resolve: agent.ResolvePublicSubtype,
		run: func(context.Context, Factory, preparedSubagent) (string, error) {
			panic("boom")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("err = %v, want the panic reported as an error", err)
	}
	if len(probe.ended) != 1 || !strings.EqualFold(probe.ended[0].Status, string(agent.StatusFailed)) {
		t.Fatalf("panic did not close the lifecycle as failed: %#v", probe.ended)
	}
	if _, ok := agent.RegistryFor(fac.subagentScopeRoot()).Get(agent.Query{TaskID: "task-panic"}); ok {
		t.Fatal("panic left a live subagent registry handle")
	}
}

// The registry handle exists for the whole run: that is what the roster's stop
// acts on.
func TestDispatchSubagentHandleIsLiveWhileRunning(t *testing.T) {
	fac, _ := newDispatchFactory(t)
	live := false
	_, _, err := dispatchSubagent(dispatchTestCtx(), fac, subagentDispatch{
		taskID:  "task-live",
		task:    "investigate",
		subtype: "explore",
		resolve: agent.ResolvePublicSubtype,
		run: func(context.Context, Factory, preparedSubagent) (string, error) {
			_, live = agent.RegistryFor(fac.subagentScopeRoot()).Get(agent.Query{TaskID: "task-live"})
			return "ok", nil
		},
	})
	if err != nil {
		t.Fatalf("dispatchSubagent: %v", err)
	}
	if !live {
		t.Fatal("no live registry handle during the run; the roster could not stop it")
	}
}

// A reserved definition is reachable by an internal dispatcher and refused for
// the tool paths, which resolve against the public set.
func TestDispatchSubagentResolverGatesReservedDefinitions(t *testing.T) {
	fac, _ := newDispatchFactory(t)
	_, _, err := dispatchSubagent(dispatchTestCtx(), fac, subagentDispatch{
		taskID:  "task-public",
		task:    "review",
		subtype: PlanReviewSubagentType,
		resolve: agent.ResolvePublicSubtype,
		run:     func(context.Context, Factory, preparedSubagent) (string, error) { return "", nil },
	})
	if err == nil {
		t.Fatal("the public resolver must refuse a reserved definition")
	}

	_, out, err := dispatchSubagent(dispatchTestCtx(), fac, subagentDispatch{
		taskID:  "task-internal",
		task:    "review",
		subtype: PlanReviewSubagentType,
		resolve: agent.ResolveSubtype,
		run:     func(context.Context, Factory, preparedSubagent) (string, error) { return "verdict", nil },
	})
	if err != nil || out != "verdict" {
		t.Fatalf("internal dispatch of a reserved definition failed: out=%q err=%v", out, err)
	}
}

// The background path announces the run before it returns and closes the row
// when the run finishes, long after its caller is gone.
func TestStartSubagentThenFinishClosesTheRowLater(t *testing.T) {
	fac, probe := newDispatchFactory(t)
	d := subagentDispatch{
		taskID:  "task-async",
		task:    "investigate",
		subtype: "explore",
		resolve: agent.ResolvePublicSubtype,
	}
	prep, handle, err := startSubagent(dispatchTestCtx(), fac, d)
	if err != nil {
		t.Fatalf("startSubagent: %v", err)
	}
	defer prep.cancel()
	if len(probe.spawned) != 1 {
		t.Fatalf("spawn must be announced before the caller returns: %#v", probe.spawned)
	}
	if len(probe.ended) != 0 {
		t.Fatalf("nothing has ended yet: %#v", probe.ended)
	}

	time.Sleep(10 * time.Millisecond)
	final := finishSubagent(fac, d, prep, handle, "findings", nil)
	if final.Status != agent.StatusOK || final.Output != "findings" {
		t.Fatalf("final entry = %#v", final)
	}
	if len(probe.ended) != 1 || probe.ended[0].AgentID != "task-async" {
		t.Fatalf("end notifications = %#v", probe.ended)
	}
}

// subagentsEnabledConfig returns the minimum configuration under which the
// subagent_* tool family exists at all. Registration is gated on
// agents.defaults.enable_subagent, so a test that exercises those tools has to
// switch them on the same way a real session does.
func subagentsEnabledConfig() *appcfg.Root {
	on := true
	return &appcfg.Root{Agents: appcfg.AgentsSection{
		Defaults: appcfg.AgentDefaults{EnableSubagent: &on},
	}}
}

// The switch governs the tool surface first. With subagents off the model must
// not be handed subagent_* at all: a registered tool that could only ever
// answer with a refusal still costs prompt tokens and still invites the call.
func TestRegisterSubagentToolOmitsTheFamilyWhenSubagentsAreDisabled(t *testing.T) {
	family := []string{
		"subagent_run", "subagent_fanout", "subagent_send", "subagent_list",
		"subagent_status", "subagent_wait", "subagent_continue", "subagent_close",
	}
	register := func(t *testing.T, cfg *appcfg.Root) (*agent.Agent, *toolpkg.State) {
		t.Helper()
		a, err := agent.New(noopLLM{}, "main", "main")
		if err != nil {
			t.Fatalf("agent.New: %v", err)
		}
		st := toolpkg.NewState(t.TempDir())
		if err := RegisterSubagentTool(a, Factory{
			Home:  t.TempDir(),
			Tools: st,
			Owner: &Runner{Deps: &Deps{AppCfg: cfg}, SubagentExecutor: &fixedSubagentExecutor{output: "ok"}},
		}); err != nil {
			t.Fatalf("RegisterSubagentTool: %v", err)
		}
		return a, st
	}

	// An absent switch is off, which is what a default configuration carries.
	a, st := register(t, &appcfg.Root{})
	for _, name := range family {
		if _, ok := aToolByName(a, name); ok {
			t.Fatalf("%s is offered to the model while enable_subagent is off", name)
		}
		if _, ok := st.ToolMetaByName(name); ok {
			t.Fatalf("%s is listed in the tool catalog while enable_subagent is off", name)
		}
	}

	a, st = register(t, subagentsEnabledConfig())
	for _, name := range family {
		if _, ok := aToolByName(a, name); !ok {
			t.Fatalf("%s is missing while enable_subagent is on", name)
		}
		if _, ok := st.ToolMetaByName(name); !ok {
			t.Fatalf("%s has no catalog entry while enable_subagent is on", name)
		}
	}
}

// Plan review is dispatched because the user asked for it, so it runs with
// agents.defaults.enable_subagent unset (which means off).
func TestRunPlanReviewSubagentRunsWithSubagentsDisabled(t *testing.T) {
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{
		Definitions: map[string]appcfg.AgentDefinition{
			"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{
				{Provider: "openai", Model: "gpt-4o", APIKey: "key", BaseURL: "https://example.invalid"},
			}},
		},
	}}
	probe := &dispatchProbe{}
	runner := &Runner{Deps: &Deps{Home: t.TempDir(), AppCfg: cfg}, Events: probe, SubagentExecutor: &fixedSubagentExecutor{output: "verdict"}}

	if _, err := runner.RunPlanReviewSubagent(dispatchTestCtx(), "review the plan", SubagentModelOverride{
		Provider: "openai", Model: "gpt-4o",
	}, nil); err != nil {
		t.Fatalf("plan review must run with subagents disabled: %v", err)
	}
	if len(probe.spawned) != 1 {
		t.Fatalf("spawn notifications = %#v", probe.spawned)
	}
}

// The subagent ledger has one writer — agentrun, through
// Factory.subagentHistoryRoot — and readers in three packages that each resolve
// the root on their own: agentrun's own lifecycle tools, appcore (handed
// Runner.StateRoot by serve_run), and gateway (Server.stateRoot). A drift
// between any of them does not error; listing just returns nothing, so the
// history quietly empties out. Pin the write and the read to one file.
func TestSubagentLedgerWriterAndReaderShareRoot(t *testing.T) {
	home := t.TempDir()
	r := &Runner{Deps: &Deps{Home: home, WorkspaceRoot: filepath.Join(home, "workspaces", "acme")}}

	writer := r.subagentFactory().subagentScopeRoot()
	// Runner.StateRoot is what serve_run hands appcore and what gateway's
	// stateRoot resolves to for the same active agent.
	reader := r.StateRoot()
	if writer != reader {
		t.Fatalf("ledger written under %s but read from %s", writer, reader)
	}

	err := agent.AppendHistory(writer, agent.HistoryEntry{
		TaskID:    "task-1",
		SessionID: "ledger-isolation-s1",
		Task:      "delegated work",
		Status:    agent.StatusOK,
		UpdatedAt: 1,
	})
	if err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	got, err := agent.ListHistory(reader, agent.Query{SessionID: "ledger-isolation-s1", Limit: 10})
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(got) != 1 || got[0].TaskID != "task-1" {
		t.Fatalf("reader did not see the writer's entry: %+v", got)
	}

	// The ledger carries delegated task text and subagent output verbatim, so
	// it must not exist in the tree every primary agent can read.
	if _, err := os.Stat(filepath.Join(home, "state", "subagent-history.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("ledger leaked into the shared home state tree (stat err=%v)", err)
	}
}

// Two primary agents sharing a home keep separate ledgers.
func TestSubagentLedgersAreDisjointAcrossAgents(t *testing.T) {
	home := t.TempDir()
	acme := (&Runner{Deps: &Deps{Home: home, WorkspaceRoot: filepath.Join(home, "workspaces", "acme")}}).subagentFactory()
	globex := (&Runner{Deps: &Deps{Home: home, WorkspaceRoot: filepath.Join(home, "workspaces", "globex")}}).subagentFactory()

	if acme.subagentScopeRoot() == globex.subagentScopeRoot() {
		t.Fatalf("both agents share the ledger root %s", acme.subagentScopeRoot())
	}

	err := agent.AppendHistory(acme.subagentScopeRoot(), agent.HistoryEntry{
		TaskID:    "acme-secret",
		SessionID: "ledger-disjoint-s1",
		Task:      "acme's confidential delegation",
		Status:    agent.StatusOK,
		UpdatedAt: 1,
	})
	if err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	leaked, err := agent.ListHistory(globex.subagentScopeRoot(), agent.Query{Limit: 10})
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(leaked) != 0 {
		t.Fatalf("globex can read acme's delegations: %+v", leaked)
	}
}

func TestSubagentModeMatrixToolVisibility(t *testing.T) {
	exploreInner := &captureToolsLLM{}
	exploreLLM := wrapTypedSubagentToolFilterLLM(exploreInner)
	if _, err := exploreLLM.Execute(toolpkg.WithSubagentType(context.Background(), "explore"), nil, []*llm.Tool{
		mustNamedTool(t, "read_file"),
		mustNamedTool(t, "shell"),
		mustNamedTool(t, "write_file"),
	}); err != nil {
		t.Fatalf("explore execute: %v", err)
	}
	exploreNames := toolNamesOnly(exploreInner.tools)
	if !containsName(exploreNames, "shell") || containsName(exploreNames, "write_file") {
		t.Fatalf("explore leaked blocked tools: %v", exploreNames)
	}

	mainInner := &captureToolsLLM{}
	mainLLM := wrapTypedSubagentToolFilterLLM(mainInner)
	_, err := mainLLM.Execute(
		WithQuerySource(context.Background(), "repl_main_thread"),
		nil,
		[]*llm.Tool{mustNamedTool(t, "read_file"), mustNamedTool(t, "shell"), mustNamedTool(t, "write_file"), mustNamedTool(t, "subagent_send"), mustNamedTool(t, "subagent_wait")},
	)
	if err != nil {
		t.Fatalf("main execute: %v", err)
	}
	mainNames := toolNamesOnly(mainInner.tools)
	for _, want := range []string{"read_file", "shell", "write_file", "subagent_send", "subagent_wait"} {
		if !containsName(mainNames, want) {
			t.Fatalf("main thread tool set missing %s: %v", want, mainNames)
		}
	}
}

func TestSubagentModeMatrixContinuationBoundary(t *testing.T) {
	home := t.TempDir()
	// Seed and read through the same resolver production uses; Factory
	// delegates to the owner, so the owner carries the home too.
	fac := Factory{Home: home, Owner: &Runner{Deps: &Deps{Home: home}, SubagentExecutor: &fixedSubagentExecutor{output: "unused"}}}
	if err := agent.AppendHistory(fac.subagentScopeRoot(), agent.HistoryEntry{
		TaskID:      "task-plan",
		RunID:       "child-plan",
		ParentRunID: "parent-1",
		SessionID:   "session-1",
		Task:        "plan work",
		Status:      agent.StatusOK,
		Output:      "done",
		StartedAt:   10,
		UpdatedAt:   20,
		FinishedAt:  20,
		AgentKind:   "typed",
		AgentType:   "plan",
		RuntimeKind: "typed_subagent",
		OneShot:     true,
		Continuable: false,
		DefSource:   "built-in",
	}); err != nil {
		t.Fatalf("append history: %v", err)
	}
	_, err := continueSubagentExecution(
		toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), "session-1"), "parent-1"),
		fac,
		&SubagentContinueInput{TaskID: "task-plan", Message: "continue"},
	)
	if err == nil || !strings.Contains(err.Error(), "one-shot") {
		t.Fatalf("expected one-shot boundary error, got %v", err)
	}
}

// collectingUINotify records all uinotify messages received by the owner runner.
type collectingUINotify struct {
	mu       sync.Mutex
	messages []any
}

type spawnMsg struct {
	AgentID, AgentType, TaskID, Title, Task string
}

type endMsg struct {
	AgentID, AgentType, TaskID, Status, Error string
}

func (c *collectingUINotify) Publish(_ context.Context, evt event.RunEvent) error {
	var msg any
	switch evt.Type {
	case event.RunEventSubagentSpawned:
		var p event.SubagentSpawnedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return nil
		}
		msg = spawnMsg{AgentID: p.AgentID, AgentType: p.AgentType, TaskID: p.TaskID, Title: p.Title, Task: p.Task}
	case event.RunEventSubagentEnded:
		var p event.SubagentEndedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return nil
		}
		msg = endMsg{AgentID: p.AgentID, AgentType: p.AgentType, TaskID: p.TaskID, Status: p.Status, Error: p.Error}
	default:
		return nil
	}
	c.mu.Lock()
	c.messages = append(c.messages, msg)
	c.mu.Unlock()
	return nil
}

func (c *collectingUINotify) spawnMsgs() []spawnMsg {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []spawnMsg
	for _, m := range c.messages {
		if s, ok := m.(spawnMsg); ok {
			out = append(out, s)
		}
	}
	return out
}

func (c *collectingUINotify) endMsgs() []endMsg {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []endMsg
	for _, m := range c.messages {
		if s, ok := m.(endMsg); ok {
			out = append(out, s)
		}
	}
	return out
}

// A dispatched subagent is named by the title its dispatcher gave it: that is
// what a roster row and a task card show, and it is the reason the title field
// is required on every subagent tool. A dispatcher that ignores the field still
// has to leave the record with a name, so the prompt's opening line stands in —
// but never the whole prompt, which belongs to the subagent's own view.
func TestSubagentSpawnCarriesTheTitleItWasNamedWith(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runSvc := &state.RunStore{DB: db}
	ensureRunSessions(t, db, "session-1")
	parent, err := runSvc.CreateRun(ctx, "session-1", "parent")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	notifier := &collectingUINotify{}
	owner := &Runner{Deps: &Deps{RunRT: runSvc, AppCfg: subagentsEnabledConfig()}, SubagentExecutor: &fixedSubagentExecutor{output: "ok"}, Control: NewController(), Events: notifier}
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, Factory{Home: t.TempDir(), Owner: owner}); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	fanout, ok := aToolByName(a, "subagent_fanout")
	if !ok {
		t.Fatal("subagent_fanout not registered")
	}

	const untitled = "Read-only investigation in /Users/doudou/workspace/forebrain.\nDetermine where RenderFrame is called."
	args, err := json.Marshal(map[string]any{
		"max_parallel": 2,
		"tasks": []map[string]string{
			{"title": "Trace RenderFrame callers", "prompt": "Find every caller of RenderFrame and report them.", "subagent_type": "explore"},
			{"prompt": untitled, "subagent_type": "explore"},
		},
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	callCtx := toolpkg.WithRunID(llm.WithAgentSessionID(ctx, "session-1"), parent.ID)
	if _, err := fanout.Handle(callCtx, string(args)); err != nil {
		t.Fatalf("tool.Handle: %v", err)
	}

	titles := map[string]bool{}
	for _, spawn := range notifier.spawnMsgs() {
		titles[spawn.Title] = true
		if strings.Contains(spawn.Title, "\n") {
			t.Fatalf("a title must be one line, got %q", spawn.Title)
		}
	}
	if !titles["Trace RenderFrame callers"] {
		t.Fatalf("the dispatcher's title did not reach the spawn event: %#v", notifier.spawnMsgs())
	}
	if !titles["Read-only investigation in /Users/doudou/workspace/forebrain."] {
		t.Fatalf("an untitled task must fall back to the prompt's opening line: %#v", notifier.spawnMsgs())
	}
}

// TestFanoutSendsDirectSpawnNotificationForEveryTask verifies that every task
// in a fanout gets a SubagentSpawnedMsg via the reliable direct path, not just
// the first maxParallel batch. This is the regression test for the bug where
// the 4th task's spawn event was lost because it only went through the
// unreliable run-event mirror (which silently dropped events when the
// subscriber buffer was full).
func TestFanoutSendsDirectSpawnNotificationForEveryTask(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runSvc := &state.RunStore{DB: db}
	ensureRunSessions(t, db, "session-1")
	parent, err := runSvc.CreateRun(ctx, "session-1", "parent")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	notifier := &collectingUINotify{}
	executor := &fixedSubagentExecutor{output: "ok"}
	owner := &Runner{Deps: &Deps{RunRT: runSvc, AppCfg: subagentsEnabledConfig()}, SubagentExecutor: executor, Control: NewController(), Events: notifier}
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, Factory{
		Home:  t.TempDir(),
		Owner: owner,
	}); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	tool, ok := aToolByName(a, "subagent_fanout")
	if !ok {
		t.Fatal("subagent_fanout not registered")
	}

	const n = 4
	tasks := make([]map[string]string, 0, n)
	for i := 0; i < n; i++ {
		tasks = append(tasks, map[string]string{
			"prompt":        "work",
			"subagent_type": "general-purpose",
		})
	}
	args, err := json.Marshal(map[string]any{"tasks": tasks, "max_parallel": n})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	callCtx := toolpkg.WithRunID(llm.WithAgentSessionID(ctx, "session-1"), parent.ID)
	if _, err := tool.Handle(callCtx, string(args)); err != nil {
		t.Fatalf("tool.Handle: %v", err)
	}

	// Every task must get a SubagentSpawnedMsg via the direct path.
	spawns := notifier.spawnMsgs()
	if len(spawns) != n {
		t.Fatalf("expected %d SubagentSpawnedMsg (one per task via direct path), got %d", n, len(spawns))
	}
	ends := notifier.endMsgs()
	if len(ends) != n {
		t.Fatalf("expected %d SubagentEndedMsg (one per task via direct path), got %d", n, len(ends))
	}
}

// TestSpawnAsyncSubagentSendsDirectSpawnNotification verifies that the
// subagent_send path also sends a SubagentSpawnedMsg via the reliable direct
// path, not just through the run-event mirror.
func TestSpawnAsyncSubagentSendsDirectSpawnNotification(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runSvc := &state.RunStore{DB: db}
	ensureRunSessions(t, db, "session-1")
	parent, err := runSvc.CreateRun(ctx, "session-1", "parent")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	notifier := &collectingUINotify{}
	owner := &Runner{Deps: &Deps{RunRT: runSvc}, SubagentExecutor: &fixedSubagentExecutor{output: "done"}, Control: NewController(), Events: notifier}
	entry, err := spawnAsyncSubagent(
		toolpkg.WithRunID(llm.WithAgentSessionID(ctx, "session-1"), parent.ID),
		Factory{AgentName: "main", Owner: owner, Home: t.TempDir()},
		"task-async-spawn",
		"short title",
		"verify direct spawn",
		"general-purpose",
	)
	if err != nil {
		t.Fatalf("spawnAsyncSubagent: %v", err)
	}

	// The direct spawn notification should arrive before the async goroutine
	// even starts running. Wait a brief moment for the end notification.
	time.Sleep(200 * time.Millisecond)

	spawns := notifier.spawnMsgs()
	if len(spawns) != 1 {
		t.Fatalf("expected 1 SubagentSpawnedMsg via direct path, got %d", len(spawns))
	}
	if spawns[0].TaskID != entry.TaskID {
		t.Fatalf("spawn TaskID=%q, want %q", spawns[0].TaskID, entry.TaskID)
	}
	ends := notifier.endMsgs()
	if len(ends) != 1 {
		t.Fatalf("expected 1 SubagentEndedMsg via direct path, got %d", len(ends))
	}
	if ends[0].TaskID != entry.TaskID {
		t.Fatalf("end TaskID=%q, want %q", ends[0].TaskID, entry.TaskID)
	}
}

func TestSpawnAsyncSubagentTurnsPanicIntoDurableFailure(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runSvc := &state.RunStore{DB: db}
	ensureRunSessions(t, db, "session-1")
	parent, err := runSvc.CreateRun(ctx, "session-1", "parent")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	probe := &dispatchProbe{endedCh: make(chan endedMsg, 1)}
	owner := &Runner{Deps: &Deps{RunRT: runSvc}, SubagentExecutor: panickingSubagentExecutor{}, Control: NewController(), Events: probe}
	fac := Factory{AgentName: "main", Owner: owner, Home: t.TempDir()}
	entry, err := spawnAsyncSubagent(
		toolpkg.WithRunID(llm.WithAgentSessionID(ctx, "session-1"), parent.ID),
		fac,
		"task-async-panic",
		"short title",
		"exercise panic cleanup",
		"general-purpose",
	)
	if err != nil {
		t.Fatalf("spawnAsyncSubagent: %v", err)
	}

	select {
	case ended := <-probe.endedCh:
		if ended.Status != string(agent.StatusFailed) || !strings.Contains(ended.Error, "panic") {
			t.Fatalf("ended = %#v", ended)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("async panic did not emit a terminal lifecycle event")
	}
	if _, ok := agent.RegistryFor(fac.subagentScopeRoot()).Get(agent.Query{TaskID: entry.TaskID}); ok {
		t.Fatal("async panic left a live subagent registry handle")
	}
	runRecord, err := runSvc.GetRun(ctx, entry.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if runRecord.Status != state.RunStatusFailed {
		t.Fatalf("run = %#v", runRecord)
	}
}

type namedScriptLLM struct {
	name  string
	calls int
}

func (n *namedScriptLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	n.calls++
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(n.name)})
	return &llm.Result{Message: &msg}, nil
}

func TestSubagentModelOverrideRoutesOnlyPinnedRuns(t *testing.T) {
	inner := &namedScriptLLM{name: "inner"}
	pinned := &namedScriptLLM{name: "pinned"}
	builds := 0
	wrapped := wrapSubagentModelOverrideLLM(inner, func(SubagentModelOverride) (llm.LLM, error) {
		builds++
		return pinned, nil
	})

	res, err := wrapped.Execute(context.Background(), nil, nil)
	if err != nil || res == nil || res.Message.TextContent() != "inner" {
		t.Fatalf("an unpinned run must use the session's own model: res=%v err=%v", res, err)
	}
	if builds != 0 {
		t.Fatal("no client should be built for an unpinned run")
	}

	ctx := WithSubagentModelOverride(context.Background(), SubagentModelOverride{Provider: "openai", Model: "gpt-4o"})
	for i := 0; i < 3; i++ {
		res, err = wrapped.Execute(ctx, nil, nil)
		if err != nil || res == nil || res.Message.TextContent() != "pinned" {
			t.Fatalf("pinned run must use the chosen model: res=%v err=%v", res, err)
		}
	}
	// Every step of the reviewer's tool loop reuses one client.
	if builds != 1 {
		t.Fatalf("client builds=%d, want the client cached across the run", builds)
	}
	if pinned.calls != 3 || inner.calls != 1 {
		t.Fatalf("call routing wrong: pinned=%d inner=%d", pinned.calls, inner.calls)
	}
}

func TestSubagentModelOverrideFailsRatherThanFallingBack(t *testing.T) {
	inner := &namedScriptLLM{name: "inner"}
	wrapped := wrapSubagentModelOverrideLLM(inner, func(SubagentModelOverride) (llm.LLM, error) {
		return nil, errors.New("no credentials")
	})
	ctx := WithSubagentModelOverride(context.Background(), SubagentModelOverride{Provider: "openai", Model: "gpt-4o"})
	if _, err := wrapped.Execute(ctx, nil, nil); err == nil || !strings.Contains(err.Error(), "no credentials") {
		t.Fatalf("err=%v, want the run to fail instead of answering with another model", err)
	}
	if inner.calls != 0 {
		t.Fatal("a failed override must not silently fall through to the session's model")
	}
}

func TestSubagentModelOverrideIgnoresAnEmptyModel(t *testing.T) {
	ctx := WithSubagentModelOverride(context.Background(), SubagentModelOverride{Provider: "openai"})
	if _, ok := subagentModelOverrideFromContext(ctx); ok {
		t.Fatal("an override without a model must not pin the run")
	}
}

func TestConfiguredModelClientRequiresTheExactPair(t *testing.T) {
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "openai", Model: "gpt-4o", APIKey: "key", BaseURL: "https://example.invalid"},
		}},
	}}}
	if _, err := ConfiguredModelClient(cfg, "main", "openai", "gpt-4o"); err != nil {
		t.Fatalf("configured pair rejected: %v", err)
	}
	if _, err := ConfiguredModelClient(cfg, "main", "anthropic", "gpt-4o"); err == nil {
		t.Fatal("a provider that is not configured for this model must be refused")
	}
	if _, err := ConfiguredModelClient(cfg, "main", "openai", "o3"); err == nil {
		t.Fatal("an unconfigured model must be refused, not swapped for another")
	}
	if _, err := ConfiguredModelClient(cfg, "main", "", ""); err == nil {
		t.Fatal("an empty model must be refused")
	}
}

// One resolver answers every model question; these are the three selectors and
// the single rule they share — a model this agent has no credentials for is
// never silently swapped for another.
func TestResolveAgentProviderSelectors(t *testing.T) {
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "openai", Model: "gpt-4o", APIKey: "key", BaseURL: "https://example.invalid"},
			{Provider: "anthropic", Model: "claude-sonnet-4", APIKey: "key", BaseURL: "https://example.invalid"},
		}},
	}}}

	primary, err := resolveAgentProvider(cfg, "main", modelSelector{})
	if err != nil || primary.Model != "gpt-4o" {
		t.Fatalf("primary = %#v err=%v, want the chain's first entry", primary, err)
	}
	byModel, err := resolveAgentProvider(cfg, "main", modelSelector{model: "claude-sonnet-4"})
	if err != nil || byModel.Provider != "anthropic" {
		t.Fatalf("by model = %#v err=%v", byModel, err)
	}
	byPair, err := resolveAgentProvider(cfg, "main", modelSelector{provider: "openai", model: "gpt-4o"})
	if err != nil || byPair.Provider != "openai" {
		t.Fatalf("by pair = %#v err=%v", byPair, err)
	}
	if _, err := resolveAgentProvider(cfg, "main", modelSelector{provider: "anthropic", model: "gpt-4o"}); !errors.Is(err, errModelNotConfigured) {
		t.Fatalf("mismatched pair err = %v, want errModelNotConfigured", err)
	}
	// Matching by model alone deliberately widens: a catalog model served by a
	// provider this agent has configured resolves even when the chain does not
	// list it, which is how a mid-session model switch reaches its client.
	widened, err := resolveAgentProvider(cfg, "main", modelSelector{model: "o3"})
	if err != nil || !strings.EqualFold(widened.Provider, "openai") || widened.Model != "o3" {
		t.Fatalf("widened = %#v err=%v, want o3 served by the configured openai entry", widened, err)
	}
	// Naming both halves does not widen.
	if _, err := resolveAgentProvider(cfg, "main", modelSelector{provider: "anthropic", model: "o3"}); !errors.Is(err, errModelNotConfigured) {
		t.Fatalf("mismatched pair err = %v, want errModelNotConfigured", err)
	}
	// A model no catalog provider serves has nowhere to widen to.
	if _, err := resolveAgentProvider(cfg, "main", modelSelector{model: "not-a-real-model"}); !errors.Is(err, errModelNotConfigured) {
		t.Fatalf("unknown model err = %v, want errModelNotConfigured", err)
	}
	// An unresolvable agent has no primary either.
	if _, err := resolveAgentProvider(&appcfg.Root{}, "main", modelSelector{}); !errors.Is(err, errModelNotConfigured) {
		t.Fatalf("empty config err = %v, want errModelNotConfigured", err)
	}
}

// The wrappers keep the degradation their callers depend on: an auxiliary model
// user treats "not configured" as "feature off", while an explicit user choice
// gets an error.
func TestModelResolverWrappersKeepTheirOwnFallbacks(t *testing.T) {
	empty := &appcfg.Root{}
	if client, err := NewLLMForAgentType(empty, "goal-evaluator"); client != nil || err != nil {
		t.Fatalf("NewLLMForAgentType = (%v,%v), want the feature reported as disabled", client, err)
	}
	if provider, client, err := NewLLMForAgentModel(empty, "main", "gpt-4o"); provider != "" || client != nil || err != nil {
		t.Fatalf("NewLLMForAgentModel = (%q,%v,%v), want compaction skipped", provider, client, err)
	}
	if _, err := ConfiguredModelClient(empty, "main", "openai", "gpt-4o"); err == nil ||
		!strings.Contains(err.Error(), "not configured") {
		t.Fatalf("ConfiguredModelClient err = %v, want an explicit refusal", err)
	}
}

func TestSubagentToolSchemasExposeSubtypeGuideToLLMAndToolMeta(t *testing.T) {
	state := toolpkg.NewState(t.TempDir())
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, Factory{
		Home:  t.TempDir(),
		Owner: &Runner{Deps: &Deps{AppCfg: subagentsEnabledConfig()}, SubagentExecutor: &fixedSubagentExecutor{output: "ok"}},
		Tools: state,
	}); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}

	for _, tt := range []struct {
		toolName  string
		path      []string
		ownerPath []string
	}{
		{
			toolName: "subagent_run",
			path:     []string{"properties", "subagent_type"},
		},
		{
			toolName: "subagent_send",
			path:     []string{"properties", "subagent_type"},
		},
		{
			toolName:  "subagent_fanout",
			path:      []string{"properties", "tasks", "items", "properties", "subagent_type"},
			ownerPath: []string{"properties", "tasks", "items"},
		},
	} {
		t.Run(tt.toolName, func(t *testing.T) {
			tool, ok := aToolByName(a, tt.toolName)
			if !ok {
				t.Fatalf("%s not registered", tt.toolName)
			}
			requireSubtypeGuide(t, tool.InputSchema(), tt.path...)
			requireSubtypeOptional(t, tool.InputSchema(), tt.ownerPath...)

			meta, ok := state.ToolMetaByName(tt.toolName)
			if !ok {
				t.Fatalf("%s meta not registered", tt.toolName)
			}
			var metaSchema map[string]any
			if err := json.Unmarshal(meta.InputSchema, &metaSchema); err != nil {
				t.Fatalf("unmarshal %s meta schema: %v", tt.toolName, err)
			}
			requireSubtypeGuide(t, metaSchema, tt.path...)
			requireSubtypeOptional(t, metaSchema, tt.ownerPath...)
		})
	}
}

func requireSubtypeOptional(t *testing.T, schema map[string]any, ownerPath ...string) {
	t.Helper()
	cur := any(schema)
	for _, key := range ownerPath {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("optional owner path %v reached non-map %#v", ownerPath, cur)
		}
		cur = m[key]
	}
	owner, ok := cur.(map[string]any)
	if !ok {
		t.Fatalf("optional owner path %v = %#v", ownerPath, cur)
	}
	raw, _ := owner["required"].([]any)
	for _, item := range raw {
		if fmt.Sprint(item) == "subagent_type" {
			t.Fatalf("required at %v unexpectedly contains subagent_type: %#v", ownerPath, raw)
		}
	}
}

func requireSubtypeGuide(t *testing.T, schema map[string]any, path ...string) {
	t.Helper()
	cur := any(schema)
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("schema path %v reached non-map %#v", path, cur)
		}
		cur = m[key]
	}
	m, ok := cur.(map[string]any)
	if !ok {
		t.Fatalf("schema path %v = %#v", path, cur)
	}
	raw, ok := m["enum"].([]any)
	if !ok {
		t.Fatalf("schema path %v enum = %#v", path, m["enum"])
	}
	wantNames := agent.PublicTypeNames()
	gotNames := make([]string, 0, len(raw))
	for _, item := range raw {
		gotNames = append(gotNames, fmt.Sprint(item))
	}
	if fmt.Sprint(gotNames) != fmt.Sprint(wantNames) {
		t.Fatalf("enum = %#v, want %#v", gotNames, wantNames)
	}
	desc, _ := m["description"].(string)
	for _, want := range []string{
		"general-purpose: bounded independent implementation",
		"explore: read-only investigation",
		"plan: read-only implementation design",
		"verification: falsify or verify",
		"Omit subagent_type to fork the current agent.",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q:\n%s", want, desc)
		}
	}
	// Spelled out here: the runtime does not enumerate the reserved subtypes,
	// so a production list would exist only for this assertion.
	for _, forbidden := range []string{"cavecrew-investigator", "cavecrew-builder", "cavecrew-reviewer"} {
		if containsSubtypeName(gotNames, forbidden) {
			t.Fatalf("public enum contains cavecrew-private subtype %q: %#v", forbidden, gotNames)
		}
		if strings.Contains(desc, forbidden) {
			t.Fatalf("public subtype description contains cavecrew-private subtype %q:\n%s", forbidden, desc)
		}
	}
}

func containsSubtypeName(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestPrepareSubagentExecutionForksWhenSubtypeIsOmitted(t *testing.T) {
	fac := Factory{
		Home:  t.TempDir(),
		Owner: &Runner{Deps: &Deps{AppCfg: subagentsEnabledConfig()}, SubagentExecutor: &fixedSubagentExecutor{output: "ok"}},
		Tools: toolpkg.NewState(t.TempDir()),
	}
	for _, subtype := range []string{"", "   "} {
		prep, err := prepareSubagentExecution(context.Background(), fac, "task-id", "inspect", subtype)
		if err != nil {
			t.Fatalf("prepareSubagentExecution(%q): %v", subtype, err)
		}
		defer prep.cancel()
		defer prep.detach()
		if prep.agentKind != "fork" || prep.runtimeKind != "fork_subagent" || prep.agentType != "fork" {
			t.Fatalf("subtype %q produced kind/type/runtime = %q/%q/%q", subtype, prep.agentKind, prep.agentType, prep.runtimeKind)
		}
	}

	typed, err := prepareSubagentExecution(context.Background(), fac, "task-id", "inspect", "explore")
	if err != nil {
		t.Fatalf("prepareSubagentExecution typed: %v", err)
	}
	defer typed.cancel()
	defer typed.detach()
	if typed.agentKind != "typed" || typed.agentType != "explore" {
		t.Fatalf("typed subtype produced kind/type = %q/%q", typed.agentKind, typed.agentType)
	}

	if _, err := prepareSubagentExecution(context.Background(), fac, "task-id", "inspect", "fork"); err == nil {
		t.Fatal(`subagent_type "fork" must be rejected; omit subagent_type to fork`)
	}
	if _, err := prepareSubagentExecution(context.Background(), fac, "task-id", "inspect", "not-real"); err == nil {
		t.Fatal("unknown typed subagent succeeded")
	}
	for _, cavecrewPrivate := range []string{"cavecrew-investigator", "cavecrew-builder", "cavecrew-reviewer"} {
		if _, err := prepareSubagentExecution(context.Background(), fac, "task-id", "inspect", cavecrewPrivate); err == nil {
			t.Fatalf("general subagent tool accepted cavecrew-private subtype %q", cavecrewPrivate)
		}
	}
}

type noopSubagentExecutor struct{}

func (noopSubagentExecutor) RunSubagentExec(ctx context.Context, task string, superviseExistingRunID string, parentRunID string, sessionID string, workerSessionID string, subagentType string) (string, error) {
	return "", nil
}

func TestSubagentToolsRejectWhenCoordinatedExecutionCapacityZero(t *testing.T) {
	limit := 0
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Defaults: appcfg.AgentDefaults{
		Execution: appcfg.ExecutionConfig{MaxParallelSubagents: &limit},
	}}}
	if err := guardSubagentTool(context.Background(), Factory{AppCfg: cfg}, "subagent_run"); err == nil {
		t.Fatal("subagent_run succeeded at zero capacity")
	}
}

func TestContinueSubagentExecutionAppendsUpdatedHistory(t *testing.T) {
	home := t.TempDir()
	// Factory.workspaceRoot delegates to the owner, so the owner has to resolve
	// to the same root or the seeded ledger is written where nothing reads it.
	fac := Factory{Home: home, Owner: &Runner{Deps: &Deps{Home: home}}}
	err := agent.AppendHistory(fac.subagentScopeRoot(), agent.HistoryEntry{
		TaskID:          "task-1",
		RunID:           "child-1",
		ParentRunID:     "parent-1",
		SessionID:       "session-1",
		WorkerSessionID: "session-1",
		Task:            "research auth",
		Status:          agent.StatusOK,
		Output:          "found auth.go:12",
		StartedAt:       10,
		UpdatedAt:       20,
		FinishedAt:      20,
		AgentType:       "fork",
		RuntimeKind:     "fork_subagent",
		Continuable:     true,
		DefSource:       "parent",
	})
	if err != nil {
		t.Fatalf("append history: %v", err)
	}
	executor := &fixedSubagentExecutor{output: "fixed auth.go"}
	fac.Owner.SubagentExecutor = executor
	ctx := toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), "session-1"), "parent-1")
	record, err := continueSubagentExecution(ctx, fac, &SubagentContinueInput{
		TaskID:  "task-1",
		Message: "apply the fix",
	})
	if err != nil {
		t.Fatalf("continueSubagentExecution: %v", err)
	}
	got, ok, err := agent.GetMerged(fac.subagentScopeRoot(), agent.Query{TaskID: "task-1", SessionID: "session-1", ParentRunID: "parent-1", Limit: 1})
	if err != nil {
		t.Fatalf("GetMerged: %v", err)
	}
	if !ok {
		t.Fatal("expected updated merged history")
	}
	if got.Output != record.Output || got.Status != agent.StatusOK {
		t.Fatalf("history not updated after continue: %+v", got)
	}
}

func TestPrepareSubagentExecutionSetsQuerySourceAndLineage(t *testing.T) {
	owner := &Runner{Deps: &Deps{}, SubagentExecutor: noopSubagentExecutor{}}
	ctx := toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), "session-1"), "parent-1")

	prep, err := prepareSubagentExecution(ctx, Factory{
		AgentName: "main",
		Owner:     owner,
	}, "task-1", "do work", "")
	if err != nil {
		t.Fatalf("prepareSubagentExecution: %v", err)
	}
	defer prep.cancel()

	if prep.entry.QuerySource != "agent:builtin:fork" {
		t.Fatalf("query source=%q", prep.entry.QuerySource)
	}
	if prep.entry.ParentRunID != "parent-1" {
		t.Fatalf("parent run id=%q", prep.entry.ParentRunID)
	}
	if prep.entry.SessionID != "session-1" {
		t.Fatalf("session id=%q", prep.entry.SessionID)
	}
	if prep.entry.WorkerSessionID == "" || !strings.Contains(prep.entry.WorkerSessionID, prep.entry.AgentID) {
		t.Fatalf("worker session id=%q (expected to contain agent id %q)", prep.entry.WorkerSessionID, prep.entry.AgentID)
	}
	if got := llm.PromptCacheKeyFromContext(prep.ctx); got != "session-1" {
		t.Fatalf("prompt cache key=%q want parent session", got)
	}
	parts := strings.Split(prep.entry.WorkerSessionID, ":")
	if len(parts) != 4 {
		t.Fatalf("worker session id=%q expected 4 colon-separated segments (<agent name>:<session id>:<subagent id>:<uuid>)", prep.entry.WorkerSessionID)
	}
	if prep.entry.AgentType != "fork" {
		t.Fatalf("agent type=%q", prep.entry.AgentType)
	}
	if prep.entry.RuntimeKind != "fork_subagent" {
		t.Fatalf("runtime kind=%q", prep.entry.RuntimeKind)
	}
	if prep.entry.AgentID == "" {
		t.Fatal("expected agent id")
	}
}

type fixedSubagentExecutor struct {
	mu              sync.Mutex
	output          string
	task            string
	superviseID     string
	parentRunID     string
	sessionID       string
	workerSessionID string
	subagentType    string
	forkChild       bool
	hookAgentID     string
}

type panickingSubagentExecutor struct{}

func (panickingSubagentExecutor) RunSubagentExec(context.Context, string, string, string, string, string, string) (string, error) {
	panic("async boom")
}

type concurrencyRecordingSubagentExecutor struct {
	mu        sync.Mutex
	active    int
	maxActive int
	block     chan struct{}
	release   chan struct{}
}

func newConcurrencyRecordingSubagentExecutor() *concurrencyRecordingSubagentExecutor {
	return &concurrencyRecordingSubagentExecutor{
		block:   make(chan struct{}, 64),
		release: make(chan struct{}),
	}
}

func (h *concurrencyRecordingSubagentExecutor) RunSubagentExec(ctx context.Context, task string, superviseExistingRunID string, parentRunID string, sessionID string, workerSessionID string, subagentType string) (string, error) {
	h.mu.Lock()
	h.active++
	if h.active > h.maxActive {
		h.maxActive = h.active
	}
	h.mu.Unlock()
	h.block <- struct{}{}
	select {
	case <-h.release:
	case <-ctx.Done():
	}
	h.mu.Lock()
	h.active--
	h.mu.Unlock()
	return "done", nil
}

type forkCaptureLLMForTest struct {
	messages []llm.Message
}

func (f *forkCaptureLLMForTest) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	f.messages = deepCloneMessages(messages)
	return &llm.Result{
		Message: &llm.Message{
			Role:  llm.RoleAssistant,
			Parts: []llm.ContentPart{llm.Text("fork done")},
		},
		Usage: &llm.Usage{InputTokens: 3, OutputTokens: 2},
	}, nil
}

func (h *fixedSubagentExecutor) RunSubagentExec(ctx context.Context, task string, superviseExistingRunID string, parentRunID string, sessionID string, workerSessionID string, subagentType string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.task = task
	h.superviseID = superviseExistingRunID
	h.parentRunID = parentRunID
	h.sessionID = sessionID
	h.workerSessionID = workerSessionID
	h.subagentType = subagentType
	h.forkChild = toolpkg.IsForkChildFromContext(ctx)
	h.hookAgentID = toolpkg.HookAgentIDFromContext(ctx)
	return h.output, nil
}

func TestExecGeneralTypedSubagentWritesLifecycleToChildRun(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runSvc := &state.RunStore{DB: db}
	ensureRunSessions(t, db, "session-1")
	parent, err := runSvc.CreateRun(ctx, "session-1", "parent")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	owner := &Runner{Deps: &Deps{RunRT: runSvc}, SubagentExecutor: &fixedSubagentExecutor{output: "child complete"}, Control: NewController()}
	// The child's lifecycle reaches the durable log through the surface the
	// runner serves; here that surface is the store itself.
	owner.Events = event.SinkFunc(func(_ context.Context, evt event.RunEvent) error {
		_, appendErr := runSvc.AppendSessionEvent(context.Background(), state.SessionEvent{
			ID: evt.ID, RunID: evt.RunID, SessionID: evt.SessionID, Type: evt.Type,
			Payload: append(json.RawMessage(nil), evt.Payload...), CreatedAt: evt.CreatedAt,
		})
		return appendErr
	})

	res, err := execGeneralSubagent(
		toolpkg.WithRunID(llm.WithAgentSessionID(ctx, "session-1"), parent.ID),
		Factory{AgentName: "main", Owner: owner, Home: t.TempDir()},
		"task-1",
		"short title",
		"do work",
		"general-purpose",
	)
	if err != nil {
		t.Fatalf("execGeneralSubagent: %v", err)
	}
	if res.RunID == "" {
		t.Fatal("expected child run id")
	}

	run, err := runSvc.GetRun(ctx, res.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != state.RunStatusDone {
		t.Fatalf("child run status=%q", run.Status)
	}

	events, err := runSvc.ListRunEvents(ctx, res.RunID, 20)
	if err != nil {
		t.Fatalf("ListRunEvents: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("expected lifecycle events, got %d", len(events))
	}
	if events[0].Type != event.RunEventSubagentSpawned {
		t.Fatalf("first event=%q", events[0].Type)
	}
	if events[len(events)-1].Type != event.RunEventSubagentEnded {
		t.Fatalf("last event=%q", events[len(events)-1].Type)
	}
}

// TestExecGeneralSubagentExecCtxCarriesRosterKeyAsHookAgentID guards the
// live-display routing for nested (executor-dispatched) subagent tool calls. The
// shared StepHook emits per-agent tool messages keyed by HookAgentID, and the
// TUI roster/fanout are keyed by the roster key, which is the task_id the spawn
// event carries. The executor dispatch path must therefore thread the task_id as
// HookAgentID, NOT the internal uuid AgentID, or the third-level tool calls
// never route to the subagent's view.
func TestExecGeneralSubagentExecCtxCarriesRosterKeyAsHookAgentID(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runSvc := &state.RunStore{DB: db}
	ensureRunSessions(t, db, "session-1")
	parent, err := runSvc.CreateRun(ctx, "session-1", "parent")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	executor := &fixedSubagentExecutor{output: "child complete"}
	owner := &Runner{Deps: &Deps{RunRT: runSvc}, SubagentExecutor: executor, Control: NewController()}

	if _, err := execGeneralSubagent(
		toolpkg.WithRunID(llm.WithAgentSessionID(ctx, "session-1"), parent.ID),
		Factory{AgentName: "main", Owner: owner, Home: t.TempDir()},
		"task-roster-key",
		"short title",
		"do work",
		"explore",
	); err != nil {
		t.Fatalf("execGeneralSubagent: %v", err)
	}

	if executor.hookAgentID != "task-roster-key" {
		t.Fatalf("executor ctx HookAgentID=%q, want the roster task id %q so nested tool calls route to the subagent view", executor.hookAgentID, "task-roster-key")
	}
}

// ctxCaptureSubagentExecutor records the context handed to RunSubagentExec so a
// test can assert on which context values crossed the subagent boundary.
type ctxCaptureSubagentExecutor struct {
	captured context.Context
}

func (h *ctxCaptureSubagentExecutor) RunSubagentExec(ctx context.Context, task string, superviseExistingRunID string, parentRunID string, sessionID string, workerSessionID string, subagentType string) (string, error) {
	h.captured = ctx
	return "ok", nil
}

// TestSubagentContextDoesNotInheritPrimarySteerRuntime reproduces the bug
// where a user steer enqueued while a subagent is running gets drained into
// the subagent's agent loop instead of staying queued for the primary agent.
// The primary agent's TurnInputRuntime must be stripped from the context
// before it crosses into any subagent execution path.
func TestSubagentContextDoesNotInheritPrimarySteerRuntime(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runSvc := &state.RunStore{DB: db}
	ensureRunSessions(t, db, "session-1")
	parent, err := runSvc.CreateRun(ctx, "session-1", "parent")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	executor := &ctxCaptureSubagentExecutor{}
	owner := &Runner{Deps: &Deps{RunRT: runSvc}, SubagentExecutor: executor, Control: NewController()}

	rt := NewTurnInputRuntime()
	rt.Enqueue(TurnInputModeSteer, []llm.ContentPart{llm.Text("steer meant for primary agent")})
	parentCtx := WithTurnInputRuntime(
		toolpkg.WithRunID(llm.WithAgentSessionID(ctx, "session-1"), parent.ID),
		rt,
	)

	if _, err := execGeneralSubagent(
		parentCtx,
		Factory{AgentName: "main", Owner: owner, Home: t.TempDir()},
		"task-steer-isolation",
		"short title",
		"do work",
		"explore",
	); err != nil {
		t.Fatalf("execGeneralSubagent: %v", err)
	}

	if executor.captured == nil {
		t.Fatal("expected executor to capture the subagent context")
	}
	if got := TurnInputRuntimeFromContext(executor.captured); got != nil {
		t.Fatalf("subagent context must not carry the primary agent's TurnInputRuntime; got %#v — user steers would be drained into the subagent loop", got)
	}
	// The steer must still be queued for the primary agent to drain after
	// the subagent tool call returns.
	if !rt.HasSteers() {
		t.Fatal("expected user steer to remain queued for the primary agent, but it was drained")
	}
}

// streamProbe records the run events a subagent's own stream sink republishes.
type streamProbe struct {
	assistant     []event.AssistantDeltaPayload
	reasoning     []event.ReasoningDeltaPayload
	usage         []event.UsageDeltaPayload
	toolStarted   []event.ToolCallStartedPayload
	toolCompleted []event.ToolCallCompletedPayload
}

func (p *streamProbe) Publish(_ context.Context, evt event.RunEvent) error {
	switch evt.Type {
	case event.RunEventAssistantDelta:
		var m event.AssistantDeltaPayload
		if json.Unmarshal(evt.Payload, &m) == nil {
			p.assistant = append(p.assistant, m)
		}
	case event.RunEventReasoningDelta:
		var m event.ReasoningDeltaPayload
		if json.Unmarshal(evt.Payload, &m) == nil {
			p.reasoning = append(p.reasoning, m)
		}
	case event.RunEventUsageDelta:
		var m event.UsageDeltaPayload
		if json.Unmarshal(evt.Payload, &m) == nil {
			p.usage = append(p.usage, m)
		}
	case event.RunEventToolStarted:
		var m event.ToolCallStartedPayload
		if json.Unmarshal(evt.Payload, &m) == nil {
			p.toolStarted = append(p.toolStarted, m)
		}
	case event.RunEventToolCompleted:
		var m event.ToolCallCompletedPayload
		if json.Unmarshal(evt.Payload, &m) == nil {
			p.toolCompleted = append(p.toolCompleted, m)
		}
	}
	return nil
}

// TestSubagentProviderWebSearchIsPublishedAsATaggedToolStep covers the one piece
// of a subagent's work that never becomes a tool step on its own: a
// provider-executed web search has no client-side call, so it reaches a surface
// only through the stream sink. It has to be published as the tool card every
// surface already renders, tagged with the roster key so it lands in the
// subagent's view rather than the conversation — or being detached from the
// dispatching agent's sink would simply lose it.
func TestSubagentProviderWebSearchIsPublishedAsATaggedToolStep(t *testing.T) {
	probe := &streamProbe{}
	ctx := subagentRunContext(context.Background(), probe, "child-run-1", "session-1", "task-search-1")
	sink := llm.StreamSinkFrom(ctx)
	if sink == nil || sink.OnWebSearch == nil {
		t.Fatal("subagent stream sink must report provider web search")
	}

	sink.OnWebSearch("ws-1", "", false)
	sink.OnWebSearch("ws-1", "forebrain subagent routing", true)

	if len(probe.toolStarted) != 1 {
		t.Fatalf("tool_call_started events = %#v, want one for the running search", probe.toolStarted)
	}
	started := probe.toolStarted[0]
	if started.ToolName != toolpkg.ProviderWebSearchToolName || started.ToolMeta.AgentID != "task-search-1" {
		t.Fatalf("started = %#v, want a web_search step tagged with the roster key", started)
	}
	if len(probe.toolCompleted) != 1 {
		t.Fatalf("tool_call_completed events = %#v, want one for the finished search", probe.toolCompleted)
	}
	done := probe.toolCompleted[0]
	if done.StepID != started.StepID {
		t.Fatalf("completed step id = %q, want the running step %q so the card is replaced, not duplicated", done.StepID, started.StepID)
	}
	if done.ToolMeta.AgentID != "task-search-1" || done.Description != "Searched the web for forebrain subagent routing" {
		t.Fatalf("completed = %#v, want the tagged, query-bearing summary", done)
	}
}

// TestTypedSubagentDoesNotStreamThroughTheDispatchingAgentsSink guards where a
// subagent's spoken output is displayed. The surface installs one StreamSink
// for the primary conversation and it carries no AgentID, so a child that
// inherits it has its assistant and reasoning text attributed to the primary
// agent — the child's closing answer most visibly, because nothing follows it
// inside the child to flush the surface's buffer, so the whole of its text
// lands in the main transcript instead of the subagent's own view. The executor
// dispatch must hand the child a sink of its own that republishes the stream
// tagged with the roster key.
func TestTypedSubagentDoesNotStreamThroughTheDispatchingAgentsSink(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runSvc := &state.RunStore{DB: db}
	ensureRunSessions(t, db, "session-1")
	parent, err := runSvc.CreateRun(ctx, "session-1", "parent")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	executor := &ctxCaptureSubagentExecutor{}
	probe := &streamProbe{}
	owner := &Runner{Deps: &Deps{RunRT: runSvc}, SubagentExecutor: executor, Control: NewController(), Events: probe}

	// The surface's sink: what the primary conversation is rendered from.
	var parentText []string
	parentStreamed := false
	parentSink := &llm.StreamSink{
		Streamed: &parentStreamed,
		OnDelta:  func(text string) { parentText = append(parentText, text) },
	}
	parentCtx := llm.WithStreamSink(
		toolpkg.WithRunID(llm.WithAgentSessionID(ctx, "session-1"), parent.ID),
		parentSink,
	)

	res, err := execGeneralSubagent(
		parentCtx,
		Factory{AgentName: "main", Owner: owner, Home: t.TempDir()},
		"task-stream-routing",
		"short title",
		"do work",
		"explore",
	)
	if err != nil {
		t.Fatalf("execGeneralSubagent: %v", err)
	}
	if executor.captured == nil {
		t.Fatal("expected executor to capture the subagent context")
	}
	childSink := llm.StreamSinkFrom(executor.captured)
	if childSink == nil {
		t.Fatal("subagent context carries no stream sink")
	}
	if childSink == parentSink {
		t.Fatal("subagent inherited the dispatching agent's stream sink; its assistant text would be retained in the primary transcript instead of its own view")
	}
	if childSink.Streamed == parentSink.Streamed {
		t.Fatal("subagent shares the parent turn's streamed flag; a streaming child would suppress the parent's own final message")
	}

	childSink.OnDelta("the subagent's conclusion")
	childSink.OnReasoningDelta("weighing the evidence")
	childSink.OnUsage(120, 30)

	if len(parentText) != 0 {
		t.Fatalf("subagent text reached the primary conversation sink: %q", parentText)
	}
	if len(probe.assistant) != 1 ||
		probe.assistant[0].Text != "the subagent's conclusion" ||
		probe.assistant[0].AgentID != "task-stream-routing" {
		t.Fatalf("assistant deltas = %#v, want one tagged with the roster key", probe.assistant)
	}
	if len(probe.reasoning) != 1 || probe.reasoning[0].AgentID != "task-stream-routing" {
		t.Fatalf("reasoning deltas = %#v, want one tagged with the roster key", probe.reasoning)
	}
	if len(probe.usage) != 1 || probe.usage[0].AgentID != "task-stream-routing" {
		t.Fatalf("usage deltas = %#v, want one tagged with the roster key", probe.usage)
	}
	// The events name the child's own run, which is what lets a surface attach
	// them to the subagent rather than to the turn that dispatched it.
	if probe.assistant[0].AgentID == "" || res.RunID == "" {
		t.Fatalf("expected a child run id and a tagged delta, got run=%q delta=%#v", res.RunID, probe.assistant[0])
	}
}

func TestContinueSubagentExecutionReusesChildRunAndPreviousOutput(t *testing.T) {
	home := t.TempDir()
	// Factory.workspaceRoot delegates to the owner, so the owner has to resolve
	// to the same root or the seeded ledger is written where nothing reads it.
	fac := Factory{Home: home, Owner: &Runner{Deps: &Deps{Home: home}}}
	err := agent.AppendHistory(fac.subagentScopeRoot(), agent.HistoryEntry{
		TaskID:          "task-1",
		RunID:           "child-1",
		ParentRunID:     "parent-1",
		SessionID:       "session-1",
		WorkerSessionID: "session-1",
		Task:            "research auth",
		Status:          agent.StatusOK,
		Output:          "found auth.go:12",
		StartedAt:       10,
		UpdatedAt:       20,
		FinishedAt:      20,
		AgentType:       "fork",
		RuntimeKind:     "fork_subagent",
		Continuable:     true,
		DefSource:       "parent",
	})
	if err != nil {
		t.Fatalf("append history: %v", err)
	}
	executor := &fixedSubagentExecutor{output: "fixed auth.go"}
	fac.Owner.SubagentExecutor = executor
	ctx := toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), "session-1"), "parent-1")

	record, err := continueSubagentExecution(ctx, fac, &SubagentContinueInput{
		TaskID:  "task-1",
		Message: "apply the fix",
	})
	if err != nil {
		t.Fatalf("continueSubagentExecution: %v", err)
	}
	if executor.superviseID != "child-1" {
		t.Fatalf("supervise id=%q", executor.superviseID)
	}
	if executor.subagentType != "" {
		t.Fatalf("subagent type=%q", executor.subagentType)
	}
	if !executor.forkChild {
		t.Fatalf("expected fork child context")
	}
	for _, want := range []string{"research auth", "found auth.go:12", "apply the fix"} {
		if !strings.Contains(executor.task, want) {
			t.Fatalf("continued prompt missing %q: %s", want, executor.task)
		}
	}
	if !strings.Contains(record.Output, "Continuation result:") || !strings.Contains(record.Output, "fixed auth.go") {
		t.Fatalf("continuation output not merged: %q", record.Output)
	}
}

func TestSubagentRunGovernedOutputUsesUnifiedContextTruncation(t *testing.T) {
	home := t.TempDir()
	toolsState := toolpkg.NewState(home)
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runSvc := &state.RunStore{DB: db}
	ensureRunSessions(t, db, "session-1")
	parent, err := runSvc.CreateRun(ctx, "session-1", "parent")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, Factory{
		Home:  home,
		Owner: &Runner{Deps: &Deps{RunRT: runSvc, AppCfg: subagentsEnabledConfig()}, SubagentExecutor: &fixedSubagentExecutor{output: strings.Repeat("x\n", toolpkg.SpillThresholdBytes)}},
		Tools: toolsState,
	}); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	tool, ok := aToolByName(a, "subagent_run")
	if !ok {
		t.Fatal("subagent_run not registered")
	}
	got, err := tool.Handle(toolpkg.WithRunID(llm.WithAgentSessionID(ctx, "session-1"), parent.ID), `{"task":"inspect","subagent_type":"general-purpose"}`)
	if err != nil {
		t.Fatalf("tool.Handle: %v", err)
	}
	payloadText, ok := got.(string)
	if !ok {
		t.Fatalf("expected string payload, got %T", got)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadText), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	output, _ := payload["output"].(string)
	if !strings.Contains(output, "[tool output truncated for context:") {
		t.Fatalf("expected governed output marker, got %q", output)
	}
	if !strings.Contains(output, "full output available via read_file:") {
		t.Fatalf("expected spill path hint, got %q", output)
	}
}

func TestSubagentFanoutGovernedOutputUsesUnifiedContextTruncation(t *testing.T) {
	home := t.TempDir()
	toolsState := toolpkg.NewState(home)
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, Factory{
		Home:  home,
		Owner: &Runner{Deps: &Deps{AppCfg: subagentsEnabledConfig()}, SubagentExecutor: &fixedSubagentExecutor{output: strings.Repeat("y\n", toolpkg.SpillThresholdBytes)}},
		Tools: toolsState,
	}); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	tool, ok := aToolByName(a, "subagent_fanout")
	if !ok {
		t.Fatal("subagent_fanout not registered")
	}
	got, err := tool.Handle(context.Background(), `{"tasks":[{"prompt":"inspect","subagent_type":"general-purpose"}],"max_parallel":1}`)
	if err != nil {
		t.Fatalf("tool.Handle: %v", err)
	}
	payloadText, ok := got.(string)
	if !ok {
		t.Fatalf("expected string payload, got %T", got)
	}
	var payload struct {
		Results []FanoutResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(payloadText), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(payload.Results) != 1 {
		t.Fatalf("results len=%d", len(payload.Results))
	}
	output := payload.Results[0].Output
	if !strings.Contains(output, "[tool output truncated for context:") {
		t.Fatalf("expected governed output marker, got %q", output)
	}
	if !strings.Contains(output, "full output available via read_file:") {
		t.Fatalf("expected spill path hint, got %q", output)
	}
}

func TestSubagentFanoutRejectsMissingOrNonPositiveMaxParallel(t *testing.T) {
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, Factory{Owner: &Runner{Deps: &Deps{AppCfg: subagentsEnabledConfig()}, SubagentExecutor: noopSubagentExecutor{}}}); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	tool, ok := aToolByName(a, "subagent_fanout")
	if !ok {
		t.Fatal("subagent_fanout not registered")
	}
	for _, args := range []string{
		`{"tasks":[{"prompt":"inspect","subagent_type":"general-purpose"}]}`,
		`{"tasks":[{"prompt":"inspect","subagent_type":"general-purpose"}],"max_parallel":0}`,
		`{"tasks":[{"prompt":"inspect","subagent_type":"general-purpose"}],"max_parallel":-2}`,
	} {
		if _, err := tool.Handle(context.Background(), args); err == nil {
			t.Fatalf("tool.Handle(%s) succeeded, want max_parallel error", args)
		}
	}
}

func TestNewSubagentTaskIDIsUniqueUnderSimultaneousGeneration(t *testing.T) {
	// Regression: task_id was generated with time.Now().UnixNano() at each
	// dispatch site. The roster and on-disk history dedup on task_id, so two
	// subagents sharing an id collapse onto one row and the extras appear
	// stranded. Under simultaneous fanout dispatch the nanosecond clock can
	// return the same value to distinct goroutines (empirically ~every burst of
	// 8+), so the old scheme was not collision-free. This barrier releases all
	// goroutines at once — the worst case for a clock-based id — and asserts the
	// generator still hands out distinct ids.
	const n = 512
	ids := make([]string, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ids[i] = newSubagentTaskID()
		}(i)
	}
	close(start)
	wg.Wait()

	seen := make(map[string]struct{}, n)
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			t.Fatal("newSubagentTaskID returned empty id")
		}
		if !strings.HasPrefix(id, "subagent-") {
			t.Fatalf("task id %q missing subagent- prefix", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate task id generated: %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestSubagentFanoutAssignsDistinctTaskIDPerTask(t *testing.T) {
	// End-to-end guard: a fanout of N tasks must persist N distinct task_ids in
	// history (which dedups on task_id). Complements the barrier test above,
	// which stresses the generator directly.
	home := t.TempDir()
	executor := &fixedSubagentExecutor{output: "ok"}
	fac := Factory{Home: home, Owner: &Runner{Deps: &Deps{Home: home, AppCfg: subagentsEnabledConfig()}, SubagentExecutor: executor}}
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, fac); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	tool, ok := aToolByName(a, "subagent_fanout")
	if !ok {
		t.Fatal("subagent_fanout not registered")
	}

	const n = 8
	tasks := make([]map[string]string, 0, n)
	for i := 0; i < n; i++ {
		tasks = append(tasks, map[string]string{
			"prompt":        "work",
			"subagent_type": "general-purpose",
		})
	}
	args, err := json.Marshal(map[string]any{"tasks": tasks, "max_parallel": n})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	if _, err := tool.Handle(context.Background(), string(args)); err != nil {
		t.Fatalf("tool.Handle: %v", err)
	}

	entries, err := agent.ListHistory(fac.subagentScopeRoot(), agent.Query{Limit: 1000})
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	ids := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if strings.TrimSpace(e.TaskID) == "" {
			t.Fatalf("history entry has empty task_id: %+v", e)
		}
		ids[e.TaskID] = struct{}{}
	}
	if len(ids) != n {
		t.Fatalf("distinct task_ids=%d want %d (collisions collapsed rows)", len(ids), n)
	}
}

func TestSubagentFanoutClampsLLMParallelismToGlobalMaxParallelSubagents(t *testing.T) {
	limit := 1
	executor := newConcurrencyRecordingSubagentExecutor()
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	cfg := subagentsEnabledConfig()
	cfg.Agents.Defaults.Execution = appcfg.ExecutionConfig{MaxParallelSubagents: &limit}
	if err := RegisterSubagentTool(a, Factory{Owner: &Runner{Deps: &Deps{AppCfg: cfg}, SubagentExecutor: executor}}); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	tool, ok := aToolByName(a, "subagent_fanout")
	if !ok {
		t.Fatal("subagent_fanout not registered")
	}
	done := make(chan error, 1)
	go func() {
		_, err := tool.Handle(context.Background(), `{"tasks":[{"prompt":"one","subagent_type":"general-purpose"},{"prompt":"two","subagent_type":"general-purpose"}],"max_parallel":4}`)
		done <- err
	}()
	<-executor.block
	executor.mu.Lock()
	maxActiveWhileBlocked := executor.maxActive
	executor.mu.Unlock()
	if maxActiveWhileBlocked != 1 {
		t.Fatalf("max active while first task blocked=%d want 1", maxActiveWhileBlocked)
	}
	close(executor.release)
	if err := <-done; err != nil {
		t.Fatalf("tool.Handle: %v", err)
	}
	if executor.maxActive != 1 {
		t.Fatalf("max active=%d want 1", executor.maxActive)
	}
}

func TestSubagentFanoutDoesNotApplyFixedSixteenConcurrencyCap(t *testing.T) {
	limit := 20
	executor := newConcurrencyRecordingSubagentExecutor()
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	cfg := subagentsEnabledConfig()
	cfg.Agents.Defaults.Execution = appcfg.ExecutionConfig{MaxParallelSubagents: &limit}
	if err := RegisterSubagentTool(a, Factory{Owner: &Runner{Deps: &Deps{AppCfg: cfg}, SubagentExecutor: executor}}); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	tool, ok := aToolByName(a, "subagent_fanout")
	if !ok {
		t.Fatal("subagent_fanout not registered")
	}
	tasks := make([]map[string]string, 0, limit)
	for i := 0; i < limit; i++ {
		tasks = append(tasks, map[string]string{
			"prompt":        "work",
			"subagent_type": "general-purpose",
		})
	}
	args, err := json.Marshal(map[string]any{
		"tasks":        tasks,
		"max_parallel": limit,
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := tool.Handle(context.Background(), string(args))
		done <- err
	}()
	if got := waitForRecordedSubagentConcurrency(t, executor, limit); got != limit {
		t.Fatalf("max active=%d want %d", got, limit)
	}
	close(executor.release)
	if err := <-done; err != nil {
		t.Fatalf("tool.Handle: %v", err)
	}
}

func waitForRecordedSubagentConcurrency(t *testing.T, executor *concurrencyRecordingSubagentExecutor, want int) int {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		executor.mu.Lock()
		got := executor.maxActive
		executor.mu.Unlock()
		if got >= want {
			return got
		}
		select {
		case <-deadline:
			return got
		case <-ticker.C:
		}
	}
}

func aToolByName(a *agent.Agent, name string) (*llm.Tool, bool) {
	for _, candidate := range debuglogToolsView(a) {
		if candidate != nil && candidate.Name() == name {
			return candidate, true
		}
	}
	return nil, false
}

func TestExecGeneralForkSubagentUsesParentSnapshotAndStableForkMessages(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runSvc := &state.RunStore{DB: db}
	ensureRunSessions(t, db, "session-1")
	parent, err := runSvc.CreateRun(ctx, "session-1", "parent")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	forkLLM := &forkCaptureLLMForTest{}
	owner := &Runner{Deps: &Deps{RunRT: runSvc}, forkLLM: forkLLM, mainCfg: AgentConfigYAML{Description: "main system"}, Control: NewController()}
	parentSnapshot := []llm.Message{
		llm.SystemMessage("rendered system"),
		llm.UserMessage(llm.Text("original user request")),
		llm.AssistantMessage(
			[]llm.ContentPart{llm.Text("assistant planning")},
			llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}},
			llm.ToolCall{ID: "call-2", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "web_search", Arguments: `{"query":"auth"}`}},
		),
	}

	res, err := execGeneralSubagent(
		withForkRuntimeSnapshot(toolpkg.WithRunID(llm.WithAgentSessionID(ctx, "session-1"), parent.ID), parentSnapshot),
		Factory{AgentName: "main", Owner: owner, Home: t.TempDir()},
		"task-fork",
		"short title",
		"inspect auth flow",
		"",
	)
	if err != nil {
		t.Fatalf("execGeneralSubagent fork: %v", err)
	}
	if res.Output != "fork done" {
		t.Fatalf("output=%q", res.Output)
	}
	if len(forkLLM.messages) < 4 {
		t.Fatalf("fork llm messages len=%d", len(forkLLM.messages))
	}
	foundUser := false
	for _, msg := range forkLLM.messages {
		if msg.Role == llm.RoleUser && msg.TextContent() == "original user request" {
			foundUser = true
			break
		}
	}
	if !foundUser {
		t.Fatalf("original user request missing from fork snapshot: %#v", forkLLM.messages)
	}
	assistantIdx := -1
	for i, msg := range forkLLM.messages {
		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) == 2 {
			assistantIdx = i
			break
		}
	}
	if assistantIdx < 0 {
		t.Fatalf("assistant tool call snapshot missing: %#v", forkLLM.messages)
	}
	// Tool-result messages (one per tool call) follow the assistant with placeholder text.
	foundCall1 := false
	foundCall2 := false
	for _, msg := range forkLLM.messages {
		if msg.Role == llm.RoleTool {
			switch msg.ToolCallID {
			case "call-1":
				if msg.TextContent() == "Fork started - processing in background" {
					foundCall1 = true
				}
			case "call-2":
				if msg.TextContent() == "Fork started - processing in background" {
					foundCall2 = true
				}
			}
		}
	}
	if !foundCall1 || !foundCall2 {
		t.Fatalf("fork child tool results missing: call-1=%v call-2=%v in %#v", foundCall1, foundCall2, forkLLM.messages)
	}
	// Last message is the user directive (boilerplate + task, no tool placeholders).
	last := forkLLM.messages[len(forkLLM.messages)-1].TextContent()
	if !strings.Contains(last, "Assigned directive:\ninspect auth flow") {
		t.Fatalf("fork child directive missing %q in %q", "Assigned directive:\ninspect auth flow", last)
	}
}

func TestPrepareSubagentExecutionTypedSubtype(t *testing.T) {
	owner := &Runner{Deps: &Deps{}, SubagentExecutor: noopSubagentExecutor{}}
	ctx := toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), "session-1"), "parent-1")

	prep, err := prepareSubagentExecution(ctx, Factory{AgentName: "main", Owner: owner}, "task-typed", "verify project", "general-purpose")
	if err != nil {
		t.Fatalf("prepareSubagentExecution typed: %v", err)
	}
	defer prep.cancel()
	if prep.entry.AgentType != "general-purpose" {
		t.Fatalf("agent type=%q", prep.entry.AgentType)
	}
	if prep.entry.RuntimeKind != "typed_subagent" {
		t.Fatalf("runtime kind=%q", prep.entry.RuntimeKind)
	}
	if prep.entry.DefSource != "built-in" {
		t.Fatalf("definition source=%q", prep.entry.DefSource)
	}
	if prep.entry.QuerySource != "agent:builtin:general-purpose" {
		t.Fatalf("query source=%q", prep.entry.QuerySource)
	}
	if prep.entry.WorkerSessionID == "" || !strings.Contains(prep.entry.WorkerSessionID, prep.entry.AgentID) {
		t.Fatalf("worker session id=%q (expected to contain agent id %q)", prep.entry.WorkerSessionID, prep.entry.AgentID)
	}
	parts := strings.Split(prep.entry.WorkerSessionID, ":")
	if len(parts) != 4 {
		t.Fatalf("worker session id=%q expected 4 colon-separated segments (<agent name>:<session id>:<subagent id>:<uuid>)", prep.entry.WorkerSessionID)
	}
}

func TestPrepareSubagentExecutionCoordinatorDoesNotDefaultToGeneralPurposeTyped(t *testing.T) {
	owner := &Runner{Deps: &Deps{}, SubagentExecutor: noopSubagentExecutor{}}
	ctx := toolpkg.WithMode(toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), "session-1"), "parent-1"), "coordinator")

	prep, err := prepareSubagentExecution(ctx, Factory{AgentName: "main", Owner: owner}, "task-coord", "delegate work", "")
	if err != nil {
		t.Fatalf("prepareSubagentExecution: %v", err)
	}
	defer prep.cancel()
	if prep.entry.AgentKind != "fork" {
		t.Fatalf("agent kind=%q", prep.entry.AgentKind)
	}
	if prep.entry.AgentType != "fork" {
		t.Fatalf("agent type=%q", prep.entry.AgentType)
	}
	if prep.entry.RuntimeKind != "fork_subagent" {
		t.Fatalf("runtime kind=%q", prep.entry.RuntimeKind)
	}
}

func TestContinueSubagentExecutionRejectsOneShotSubtype(t *testing.T) {
	home := t.TempDir()
	// Factory.workspaceRoot delegates to the owner, so the owner has to resolve
	// to the same root or the seeded ledger is written where nothing reads it.
	fac := Factory{Home: home, Owner: &Runner{Deps: &Deps{Home: home}}}
	err := agent.AppendHistory(fac.subagentScopeRoot(), agent.HistoryEntry{
		TaskID:      "task-oneshot",
		RunID:       "child-oneshot",
		ParentRunID: "parent-1",
		SessionID:   "session-1",
		Task:        "plan work",
		Status:      agent.StatusOK,
		Output:      "done",
		StartedAt:   10,
		UpdatedAt:   20,
		FinishedAt:  20,
		AgentType:   "plan",
		RuntimeKind: "typed_subagent",
		OneShot:     true,
		Continuable: false,
		DefSource:   "built-in",
	})
	if err != nil {
		t.Fatalf("append history: %v", err)
	}
	executor := &fixedSubagentExecutor{output: "unused"}
	fac.Owner.SubagentExecutor = executor
	ctx := toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), "session-1"), "parent-1")
	_, err = continueSubagentExecution(ctx, fac, &SubagentContinueInput{
		TaskID:  "task-oneshot",
		Message: "continue anyway",
	})
	if err == nil {
		t.Fatalf("expected one-shot rejection")
	}
	if !strings.Contains(err.Error(), "one-shot") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestForkChildRejectsImplicitForkSubagentRun(t *testing.T) {
	ctx := toolpkg.WithForkChild(context.Background(), true)
	err := guardForkChildImplicitFork(ctx, "")
	if err == nil {
		t.Fatalf("expected implicit fork rejection")
	}
	if !strings.Contains(err.Error(), "fork is not available inside a forked worker") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestForkChildRejectsSubagentLifecycleTools(t *testing.T) {
	ctx := toolpkg.WithForkChild(context.Background(), true)
	if err := guardForkChildNoSubagentTools(ctx); err == nil {
		t.Fatalf("expected subagent lifecycle rejection")
	}
}

func TestForkChildRejectsSubagentToolsEvenWithExplicitSubtype(t *testing.T) {
	ctx := toolpkg.WithForkChild(context.Background(), true)
	if err := guardForkChildNoSubagentTools(ctx); err == nil {
		t.Fatalf("expected rejection for subagent tools in fork child")
	}
	if err := guardForkChildImplicitFork(ctx, "general-purpose"); err != nil {
		t.Fatalf("implicit-fork guard should allow explicit subtype path: %v", err)
	}
}

func TestSubagentQuerySourceClassification(t *testing.T) {
	if got := subagentQuerySource("fork", "fork", "parent"); got != "agent:builtin:fork" {
		t.Fatalf("fork query source=%q", got)
	}
	if got := subagentQuerySource("typed", "verification", "built-in"); got != "agent:builtin:verification" {
		t.Fatalf("typed built-in query source=%q", got)
	}
	if got := subagentQuerySource("typed", "custom-x", "user"); got != "agent:custom" {
		t.Fatalf("typed custom query source=%q", got)
	}
	if got := subagentQuerySource("", "", ""); got != "agent:default" {
		t.Fatalf("default query source=%q", got)
	}
}

func toolMiddlewareCount(t *llm.Tool) int {
	if t == nil {
		return -1
	}
	v := reflect.ValueOf(t).Elem().FieldByName("middlewares")
	if !v.IsValid() || v.Kind() != reflect.Slice || !v.CanAddr() {
		return -1
	}
	mws, _ := reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem().Interface().([]llm.ToolMiddleware)
	return len(mws)
}

// TestForkSubagentDoesNotMutateParentTools is a regression test for the
// subagent_fanout data race: runForkSubagent's RegisterTools callback re-used
// the parent agent's shared *llm.Tool pointers, and ToolRegistry.Add
// / toolpkg.Register append middlewares to the receiver. Concurrent fork
// subagents (subagent_fanout) therefore raced on the shared tools' middleware
// slices and corrupt tool registration, leaving subagents with no usable
// tools. The fix clones each tool before registering it with the fork agent.
func TestForkSubagentDoesNotMutateParentTools(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runSvc := &state.RunStore{DB: db}
	ensureRunSessions(t, db, "session-1")
	parent, err := runSvc.CreateRun(ctx, "session-1", "parent")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Build a couple of real tools and register them through toolreg so they
	// carry the default telemetry middlewares, just like the parent's
	// loadedTools do in production.
	parentAgent, err := agent.New(&forkCaptureLLMForTest{}, "parent", "parent agent for tool registration")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	readTool, err := llm.NewTool("read_file", "read", func(_ context.Context, in *struct {
		Path string `json:"path"`
	}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewTool read_file: %v", err)
	}
	searchTool, err := llm.NewTool("web_search", "search", func(_ context.Context, in *struct {
		Query string `json:"query"`
	}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewTool web_search: %v", err)
	}
	if err := toolpkg.NewState(t.TempDir()).Register(parentAgent, readTool); err != nil {
		t.Fatalf("register read_file: %v", err)
	}
	if err := toolpkg.NewState(t.TempDir()).Register(parentAgent, searchTool); err != nil {
		t.Fatalf("register web_search: %v", err)
	}
	tools := []*llm.Tool{readTool, searchTool}
	before := map[string]int{}
	for _, tl := range tools {
		before[tl.Name()] = toolMiddlewareCount(tl)
	}

	owner := &Runner{Deps: &Deps{RunRT: runSvc}, forkLLM: &forkCaptureLLMForTest{}, mainCfg: AgentConfigYAML{Description: "main system"}, main: parentAgent, Control: NewController()}
	parentSnapshot := []llm.Message{
		llm.UserMessage(llm.Text("investigate")),
		llm.AssistantMessage(
			[]llm.ContentPart{llm.Text("planning")},
			llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "subagent_fanout", Arguments: `{}`}},
		),
	}

	if _, err := execGeneralSubagent(
		withForkRuntimeSnapshot(toolpkg.WithRunID(llm.WithAgentSessionID(ctx, "session-1"), parent.ID), parentSnapshot),
		Factory{AgentName: "main", Owner: owner, Home: t.TempDir()},
		"task-fork",
		"short title",
		"investigate auth flow",
		"",
	); err != nil {
		t.Fatalf("execGeneralSubagent fork: %v", err)
	}

	for _, tl := range tools {
		after := toolMiddlewareCount(tl)
		if after != before[tl.Name()] {
			t.Fatalf("%s middlware count changed: before=%d after=%d (parent tool mutated by fork subagent)", tl.Name(), before[tl.Name()], after)
		}
	}
}

// The subagent semaphore is what bounds parallel subagents, and nothing
// covered it. Its two invariants are that the size is decided once — a
// per-call resize would stop limiting anything — and that a non-positive
// capacity clamps to 1 rather than producing a zero-buffer channel, which
// would deadlock on the first send since nothing receives until a subagent
// finishes.
func TestSubagentSemaphoreSizesOnceAndClampsToOne(t *testing.T) {
	var s subagentSemaphore
	calls := 0
	ch := s.get(func() int { calls++; return 3 })
	if cap(ch) != 3 {
		t.Fatalf("capacity = %d, want 3", cap(ch))
	}
	// A later call must return the same channel without re-sizing: a fresh
	// channel per call would let unlimited subagents run.
	again := s.get(func() int { calls++; return 99 })
	if again != ch {
		t.Fatal("a second get returned a different channel; the limit would not hold across calls")
	}
	if calls != 1 {
		t.Fatalf("capacity was computed %d times, want once", calls)
	}

	for _, capacity := range []int{0, -1} {
		var z subagentSemaphore
		got := z.get(func() int { return capacity })
		if cap(got) != 1 {
			t.Fatalf("capacity %d produced a channel of size %d, want it clamped to 1 (a zero-buffer channel deadlocks)", capacity, cap(got))
		}
	}
}

// The semaphore has to actually block past its capacity, which is the whole
// point of holding it on the Runner rather than making one per call.
func TestSubagentSemaphoreBlocksBeyondCapacity(t *testing.T) {
	var s subagentSemaphore
	ch := s.get(func() int { return 2 })
	ch <- struct{}{}
	ch <- struct{}{}
	select {
	case ch <- struct{}{}:
		t.Fatal("a third slot was granted against a capacity of 2")
	default:
	}
	<-ch
	select {
	case ch <- struct{}{}:
	default:
		t.Fatal("releasing a slot did not let the next waiter through")
	}
}

// gatedSubagentExecutor suspends its first dispatch of each task the way a real
// subagent does when one of its tools hits an approval gate, and completes once
// the resume context carries the approved action id.
type gatedSubagentExecutor struct {
	mu    sync.Mutex
	calls map[string]int
}

func (h *gatedSubagentExecutor) actionIDFor(task string) string { return "action-" + task }

func (h *gatedSubagentExecutor) RunSubagentExec(ctx context.Context, task string, _ string, _ string, _ string, _ string, _ string) (string, error) {
	h.mu.Lock()
	if h.calls == nil {
		h.calls = map[string]int{}
	}
	h.calls[task]++
	h.mu.Unlock()
	actionID := h.actionIDFor(task)
	if toolpkg.ApprovedActionIDFromContext(ctx) == actionID {
		return "done:" + task, nil
	}
	return "", &toolpkg.RequiresActionError{
		RunID: "child-run-" + task, ActionID: actionID, ActionKind: "shell", ToolName: "shell",
	}
}

func (h *gatedSubagentExecutor) callCount(task string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls[task]
}

func fanoutToolForGatedExecutor(t *testing.T, executor *gatedSubagentExecutor, st *toolpkg.State) *llm.Tool {
	t.Helper()
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, Factory{
		Home:  t.TempDir(),
		Owner: &Runner{Deps: &Deps{AppCfg: subagentsEnabledConfig()}, SubagentExecutor: executor},
		Tools: st,
	}); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	tl, ok := aToolByName(a, "subagent_fanout")
	if !ok {
		t.Fatal("subagent_fanout not registered")
	}
	return tl
}

// The reported bug: three fanout children each hit a sandbox escalation, and
// every one of them was reported back as a failed task carrying the gate's
// internal error string ("tool shell: requires action: <uuid>"). Nobody was
// ever asked, and eight minutes of work was thrown away. An approval gate is a
// suspension, so with a surface able to answer it the children are resumed in
// place and the fanout completes.
func TestSubagentFanoutResolvesChildApprovalsInPlace(t *testing.T) {
	executor := &gatedSubagentExecutor{}
	st := toolpkg.NewState(t.TempDir())
	var mu sync.Mutex
	asked := []string{}
	st.SetSubagentApprovalHook(func(ctx context.Context, rae *toolpkg.RequiresActionError) (context.Context, error) {
		mu.Lock()
		asked = append(asked, rae.ActionID)
		mu.Unlock()
		return toolpkg.WithApprovedActionID(ctx, rae.ActionID), nil
	})
	tl := fanoutToolForGatedExecutor(t, executor, st)

	got, err := tl.Handle(dispatchTestCtx(),
		`{"tasks":[{"prompt":"one","subagent_type":"general-purpose"},{"prompt":"two","subagent_type":"general-purpose"}],"max_parallel":2}`)
	if err != nil {
		t.Fatalf("tool.Handle: %v", err)
	}
	var payload struct {
		Results []FanoutResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(got.(string)), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(payload.Results) != 2 {
		t.Fatalf("results len=%d, want 2", len(payload.Results))
	}
	for _, res := range payload.Results {
		if !res.OK {
			t.Fatalf("task %q reported failed: %q", res.Task, res.Error)
		}
		if !strings.HasPrefix(res.Output, "done:") {
			t.Fatalf("task %q output = %q, want the resumed run's output", res.Task, res.Output)
		}
	}
	mu.Lock()
	askedCount := len(asked)
	mu.Unlock()
	if askedCount != 2 {
		t.Fatalf("approvals asked = %d, want one per gated child", askedCount)
	}
	if executor.callCount("one") != 2 || executor.callCount("two") != 2 {
		t.Fatalf("each child must run once more after its approval, got one=%d two=%d",
			executor.callCount("one"), executor.callCount("two"))
	}
}

// With no surface able to answer in place, the gate must still travel as the
// control-flow error it is, so the dispatching agent's own tool loop suspends
// the turn. Reporting it as a task result would tell the model its subtask
// failed while an approval nobody will ever see sits on the queue.
func TestSubagentFanoutPropagatesUnansweredChildApproval(t *testing.T) {
	executor := &gatedSubagentExecutor{}
	tl := fanoutToolForGatedExecutor(t, executor, toolpkg.NewState(t.TempDir()))

	got, err := tl.Handle(dispatchTestCtx(),
		`{"tasks":[{"prompt":"one","subagent_type":"general-purpose"}],"max_parallel":1}`)
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("err = %v, want the child's requires-action error", err)
	}
	if rae.ActionID != executor.actionIDFor("one") {
		t.Fatalf("action id = %q, want the child's", rae.ActionID)
	}
	if text, ok := got.(string); ok && strings.Contains(text, "requires action") {
		t.Fatalf("the gate must not be reported as a task result: %q", text)
	}
}

// subagent_run dispatches through the same path, so a single child's gate is
// answered in place too rather than unwinding the dispatching turn.
func TestSubagentRunResolvesChildApprovalInPlace(t *testing.T) {
	executor := &gatedSubagentExecutor{}
	st := toolpkg.NewState(t.TempDir())
	st.SetSubagentApprovalHook(func(ctx context.Context, rae *toolpkg.RequiresActionError) (context.Context, error) {
		return toolpkg.WithApprovedActionID(ctx, rae.ActionID), nil
	})
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, Factory{
		Home:  t.TempDir(),
		Owner: &Runner{Deps: &Deps{AppCfg: subagentsEnabledConfig()}, SubagentExecutor: executor},
		Tools: st,
	}); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	tl, ok := aToolByName(a, "subagent_run")
	if !ok {
		t.Fatal("subagent_run not registered")
	}
	got, err := tl.Handle(dispatchTestCtx(), `{"task":"one","subagent_type":"general-purpose"}`)
	if err != nil {
		t.Fatalf("tool.Handle: %v", err)
	}
	var resp struct {
		Status string `json:"status"`
		Output string `json:"output"`
	}
	if err := json.Unmarshal([]byte(got.(string)), &resp); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if resp.Output != "done:one" {
		t.Fatalf("output = %q, want the resumed run's output", resp.Output)
	}
}

// The retry carries the fence instead of crossing it: the child restarts with
// prompt assembly, pre-hooks and possibly a compaction ahead of the replay, and
// a process that dies in that preamble has executed nothing. Crossing it here
// labelled all of that as a tool call that may already have run.
func TestRunAcrossApprovalsLeavesTheFenceToTheReplay(t *testing.T) {
	attempts := 0
	fenced := false
	approve := func(ctx context.Context, rae *toolpkg.RequiresActionError) (context.Context, error) {
		return toolpkg.WithToolApprovalResume(ctx, &toolpkg.ToolApprovalResumeState{
			BeginContinuation: func(context.Context) error {
				fenced = true
				return nil
			},
		}), nil
	}
	out, err := runAcrossApprovals(context.Background(), approve, "child-1", "subagent", func(retryCtx context.Context, _ string) (string, error) {
		attempts++
		if attempts == 1 {
			return "", &toolpkg.RequiresActionError{RunID: "child-1", ActionID: "action-1", ToolName: "shell"}
		}
		if fenced {
			t.Fatal("the durable execution fence was crossed before the replay")
		}
		if toolpkg.ToolApprovalResumeFromContext(retryCtx) == nil {
			t.Fatal("the resumed attempt lost the continuation the replay must fence")
		}
		return "done", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" || attempts != 2 {
		t.Fatalf("out=%q attempts=%d", out, attempts)
	}
}

// prepareSubagentExecution was a production wrapper that only these tests
// called: it fills in an empty dispatch for the live
// prepareSubagentExecutionResolved. It lives here so the production file
// carries no unused code while the tests keep exercising the live path.

func prepareSubagentExecution(baseCtx context.Context, fac Factory, taskID, task, subagentType string) (preparedSubagent, error) {
	// Model-driven spawns may only name a public subtype; the enum on the tool
	// schema is the first gate and this is the second.
	return prepareSubagentExecutionResolved(baseCtx, fac, subagentDispatch{
		taskID:  taskID,
		task:    task,
		subtype: subagentType,
		resolve: agent.ResolvePublicSubtype,
	})
}

// usageObservingSubagentExecutor stands in for a child run's model calls: it
// opens the accumulator scope agent.Run would and records one response in it.
type usageObservingSubagentExecutor struct{}

func (usageObservingSubagentExecutor) RunSubagentExec(ctx context.Context, _, _, _, _, _, _ string) (string, error) {
	child := llm.NewUsageAccumulator()
	_ = llm.WithUsageAccumulator(ctx, child)
	child.ObserveUsage(llm.Usage{InputTokens: 100, OutputTokens: 10, CacheReadInputTokens: 900})
	return "done", nil
}

// TestSubagentUsageStaysOutOfTheDispatchingRunsAccumulator pins the run
// boundary for usage: a subagent is a run with its own persisted row, so its
// model calls must not also roll up into the accumulator of the run that
// dispatched it. Otherwise the parent's persisted total includes the child and
// every sum over the run tree — /status's session usage and cache hit rate —
// counts the child twice.
func TestSubagentUsageStaysOutOfTheDispatchingRunsAccumulator(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runSvc := &state.RunStore{DB: db}
	ensureRunSessions(t, db, "session-1")
	parent, err := runSvc.CreateRun(ctx, "session-1", "parent")
	if err != nil {
		t.Fatal(err)
	}
	owner := &Runner{Deps: &Deps{RunRT: runSvc}, SubagentExecutor: usageObservingSubagentExecutor{}, Control: NewController()}
	parentAcc := llm.NewUsageAccumulator()
	parentCtx := llm.WithUsageAccumulator(toolpkg.WithRunID(llm.WithAgentSessionID(ctx, "session-1"), parent.ID), parentAcc)
	parentAcc.ObserveUsage(llm.Usage{InputTokens: 7, OutputTokens: 3})

	if _, err := execGeneralSubagent(parentCtx, Factory{AgentName: "main", Owner: owner, Home: t.TempDir()}, "task-1", "title", "do work", "general-purpose"); err != nil {
		t.Fatal(err)
	}
	if got := parentAcc.Snapshot(); got != (llm.Usage{InputTokens: 7, OutputTokens: 3}) {
		t.Fatalf("parent accumulator = %+v, want only the parent's own call", got)
	}
}
