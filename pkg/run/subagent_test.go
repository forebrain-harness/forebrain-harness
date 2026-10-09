package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
	// The model this execution runs on, as the spawn announced it.
	Provider, Model, Effort string
}

type endedMsg struct {
	AgentID, AgentType, TaskID, Status, Error string
}

func (p *dispatchProbe) Publish(_ context.Context, evt event.RunEvent) error {
	switch evt.Type {
	case event.RunEventSubagentSpawned:
		var m event.SubagentSpawnedPayload
		if json.Unmarshal(evt.Payload, &m) == nil {
			p.spawned = append(p.spawned, spawnedMsg{
				AgentID: m.AgentID, AgentType: m.AgentType, TaskID: m.TaskID, Task: m.Task,
				Provider: m.ModelProvider, Model: m.Model, Effort: m.ReasoningEffort,
			})
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

// TestSubagentDispatchBirthsItsWorkerSession pins what a dispatch owes its
// subagent before the work starts: a worker session row born with the
// session-purpose "subagent" and the dispatching conversation as its parent.
// Every message row the execution writes and every run row a nested dispatch
// of its own writes carry this session as a foreign key — plan 013's
// real-device run died here ("persist subagent run: FOREIGN KEY constraint
// failed") because nested dispatches referenced a worker session that had
// never been born.
func TestSubagentDispatchBirthsItsWorkerSession(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	home := t.TempDir()
	store := state.NewSessionStore(db, "main")
	if err := store.Ensure(ctx, "session-1", "session-1"); err != nil {
		t.Fatalf("ensure conversation: %v", err)
	}
	probe := &dispatchProbe{}
	fac := Factory{
		AgentName: "main",
		Home:      home,
		Owner: &Runner{
			Deps:             &Deps{Home: home, SessionStore: store},
			Events:           probe,
			SubagentExecutor: &fixedSubagentExecutor{output: "done"},
		},
	}
	prep, _, err := dispatchSubagent(dispatchTestCtx(), fac, subagentDispatch{
		taskID:  "task-birth",
		task:    "investigate",
		subtype: "explore",
		resolve: agent.ResolvePublicSubtype,
		run: func(context.Context, Factory, preparedSubagent) (string, error) {
			return "findings", nil
		},
	})
	if err != nil {
		t.Fatalf("dispatchSubagent: %v", err)
	}
	var source, parent string
	if err := db.QueryRow(`SELECT source, parent_session_id FROM fb_sessions WHERE id = ?`, prep.entry.WorkerSessionID).Scan(&source, &parent); err != nil {
		t.Fatalf("worker session row: %v", err)
	}
	if source != state.SessionSourceSubagent || parent != "session-1" {
		t.Fatalf("worker session = source %q parent %q, want %q/session-1", source, parent, state.SessionSourceSubagent)
	}
	if len(probe.spawned) != 1 {
		t.Fatalf("spawn notifications = %#v", probe.spawned)
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

// The model a plan review runs on is a property of its execution: the user
// picked it in the approval overlay, so it is neither the reviewer type's
// configuration (it has none) nor the conversation's. The spawn announces it
// — with the effort that model's own configuration gives it — and the history
// entry carries it, which is where the announcement read it from.
func TestPlanReviewSpawnEventNamesTheChosenModel(t *testing.T) {
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{
		Definitions: map[string]appcfg.AgentDefinition{
			"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{
				{Provider: "zhipuai", Model: "glm-5.3", APIKey: "key", BaseURL: "https://example.invalid"},
				{
					Provider: "zhipuai", Model: "glm-5.3-flash", APIKey: "key", BaseURL: "https://example.invalid",
					Params: appcfg.LLMRequestParams(`{"reasoning":{"effort":"high"}}`),
				},
			}},
		},
	}}
	probe := &dispatchProbe{}
	runner := &Runner{Deps: &Deps{Home: t.TempDir(), AppCfg: cfg}, Events: probe, SubagentExecutor: &fixedSubagentExecutor{output: "verdict"}}

	if _, err := runner.RunPlanReviewSubagent(dispatchTestCtx(), "review the plan", SubagentModelOverride{
		Provider: "zhipuai", Model: "glm-5.3-flash",
	}, nil); err != nil {
		t.Fatalf("plan review: %v", err)
	}
	if len(probe.spawned) != 1 {
		t.Fatalf("spawn notifications = %#v", probe.spawned)
	}
	spawn := probe.spawned[0]
	if spawn.Model != "glm-5.3-flash" || spawn.Provider != "zhipuai" {
		t.Fatalf("spawned model = %q/%q, want the override the user picked", spawn.Provider, spawn.Model)
	}
	if spawn.Effort != "high" {
		t.Fatalf("spawned effort = %q, want the one that model's configuration gives it", spawn.Effort)
	}
	if spawn.AgentType != PlanReviewSubagentType {
		t.Fatalf("spawned type = %q, want the plan reviewer", spawn.AgentType)
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
	ParentToolCallID                        string
	TaskIndex                               int
	CreatedAt                               time.Time
}

type endMsg struct {
	AgentID, AgentType, TaskID, Status, Error string
	ParentToolCallID                          string
	TaskIndex                                 int
	FinishedAtMs                              int64
}

func (c *collectingUINotify) Publish(_ context.Context, evt event.RunEvent) error {
	var msg any
	switch evt.Type {
	case event.RunEventSubagentSpawned:
		var p event.SubagentSpawnedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return nil
		}
		msg = spawnMsg{AgentID: p.AgentID, AgentType: p.AgentType, TaskID: p.TaskID, Title: p.Title, Task: p.Task,
			ParentToolCallID: p.ParentToolCallID, TaskIndex: p.TaskIndex, CreatedAt: evt.CreatedAt}
	case event.RunEventSubagentEnded:
		var p event.SubagentEndedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return nil
		}
		msg = endMsg{AgentID: p.AgentID, AgentType: p.AgentType, TaskID: p.TaskID, Status: p.Status, Error: p.Error,
			ParentToolCallID: p.ParentToolCallID, TaskIndex: p.TaskIndex, FinishedAtMs: p.FinishedAtMs}
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

// A subagent_send dispatch must name the tool call that dispatched it: the
// call id is the only fact a surface has to draw the agent into that call's
// card, and the async execution leaves the dispatching context behind.
func TestSubagentSendLifecycleNamesItsDispatchingCall(t *testing.T) {
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
	owner := &Runner{Deps: &Deps{RunRT: runSvc, AppCfg: subagentsEnabledConfig()}, SubagentExecutor: &fixedSubagentExecutor{output: "done"}, Control: NewController(), Events: notifier}
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, Factory{Home: t.TempDir(), Owner: owner}); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	sendTool, ok := aToolByName(a, "subagent_send")
	if !ok {
		t.Fatal("subagent_send not registered")
	}
	args, err := json.Marshal(map[string]any{
		"title":         "Async dispatch check",
		"task":          "verify the dispatch carries its call id",
		"subagent_type": "general-purpose",
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	callCtx := toolpkg.WithToolUseID(toolpkg.WithRunID(llm.WithAgentSessionID(ctx, "session-1"), parent.ID), "call-send-1")
	if _, err := sendTool.Handle(callCtx, string(args)); err != nil {
		t.Fatalf("tool.Handle: %v", err)
	}

	spawns := notifier.spawnMsgs()
	if len(spawns) != 1 {
		t.Fatalf("expected 1 SubagentSpawnedMsg, got %d", len(spawns))
	}
	if spawns[0].ParentToolCallID != "call-send-1" {
		t.Fatalf("spawn ParentToolCallID=%q, want call-send-1", spawns[0].ParentToolCallID)
	}
	if spawns[0].TaskIndex != 0 {
		t.Fatalf("spawn TaskIndex=%d, want 0", spawns[0].TaskIndex)
	}
}

// A finished execution says when it stopped: the ended event carries its own
// finish time, and that time is not before the spawn it closes.
func TestSubagentEndedCarriesItsFinishTime(t *testing.T) {
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
	owner := &Runner{Deps: &Deps{RunRT: runSvc, AppCfg: subagentsEnabledConfig()}, SubagentExecutor: &fixedSubagentExecutor{output: "done"}, Control: NewController(), Events: notifier}
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, Factory{Home: t.TempDir(), Owner: owner}); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	runTool, ok := aToolByName(a, "subagent_run")
	if !ok {
		t.Fatal("subagent_run not registered")
	}
	args, err := json.Marshal(map[string]any{
		"title":         "Blocking run check",
		"task":          "verify the ended event carries its finish time",
		"subagent_type": "general-purpose",
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	callCtx := toolpkg.WithRunID(llm.WithAgentSessionID(ctx, "session-1"), parent.ID)
	if _, err := runTool.Handle(callCtx, string(args)); err != nil {
		t.Fatalf("tool.Handle: %v", err)
	}

	spawns := notifier.spawnMsgs()
	if len(spawns) != 1 {
		t.Fatalf("expected 1 SubagentSpawnedMsg, got %d", len(spawns))
	}
	ends := notifier.endMsgs()
	if len(ends) != 1 {
		t.Fatalf("expected 1 SubagentEndedMsg, got %d", len(ends))
	}
	if ends[0].FinishedAtMs <= 0 {
		t.Fatalf("ended FinishedAtMs=%d, want the execution's own stop time", ends[0].FinishedAtMs)
	}
	if ends[0].FinishedAtMs < spawns[0].CreatedAt.UnixMilli() {
		t.Fatalf("ended FinishedAtMs=%d predates the spawn at %d", ends[0].FinishedAtMs, spawns[0].CreatedAt.UnixMilli())
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
	if _, ok := SubagentModelOverrideFromContext(ctx); ok {
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

func (noopSubagentExecutor) RunSubagentExec(_ context.Context, _ SubagentExecRequest) (string, error) {
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

// A continue call's lifecycle events must speak of the continue call itself:
// its call id with its own task index (a continue dispatches one task), never
// the original dispatch's call id and index.
func TestSubagentContinueLifecycleIndexIsItsOwnCall(t *testing.T) {
	home := t.TempDir()
	notifier := &collectingUINotify{}
	fac := Factory{Home: home, Owner: &Runner{Deps: &Deps{Home: home}, SubagentExecutor: &fixedSubagentExecutor{output: "continued"}, Events: notifier}}
	err := agent.AppendHistory(fac.subagentScopeRoot(), agent.HistoryEntry{
		TaskID:          "task-cont",
		RunID:           "child-cont",
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
		// The record was dispatched as a fanout's third task; the continue
		// call that reads it is a dispatch of its own with a single task.
		TaskIndex: 2,
	})
	if err != nil {
		t.Fatalf("append history: %v", err)
	}
	ctx := toolpkg.WithToolUseID(
		toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), "session-1"), "parent-1"),
		"call-cont-1",
	)
	if _, err := continueSubagentExecution(ctx, fac, &SubagentContinueInput{
		TaskID:  "task-cont",
		Message: "apply the fix",
	}); err != nil {
		t.Fatalf("continueSubagentExecution: %v", err)
	}
	spawns := notifier.spawnMsgs()
	if len(spawns) != 1 {
		t.Fatalf("expected 1 SubagentSpawnedMsg, got %d", len(spawns))
	}
	if spawns[0].ParentToolCallID != "call-cont-1" || spawns[0].TaskIndex != 0 {
		t.Fatalf("spawn ParentToolCallID=%q TaskIndex=%d, want call-cont-1 and 0", spawns[0].ParentToolCallID, spawns[0].TaskIndex)
	}
	ends := notifier.endMsgs()
	if len(ends) != 1 {
		t.Fatalf("expected 1 SubagentEndedMsg, got %d", len(ends))
	}
	if ends[0].ParentToolCallID != "call-cont-1" || ends[0].TaskIndex != 0 {
		t.Fatalf("end ParentToolCallID=%q TaskIndex=%d, want call-cont-1 and 0", ends[0].ParentToolCallID, ends[0].TaskIndex)
	}
}

// The JSON these tools return to the model is their contract; the card facts
// are derived beside it, never reshaped through it. This pins the field sets
// so a display change cannot leak into what the model sees.
func TestSubagentToolResultsKeepTheirShape(t *testing.T) {
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
	home := t.TempDir()
	owner := &Runner{Deps: &Deps{Home: home, RunRT: runSvc, AppCfg: subagentsEnabledConfig()}, SubagentExecutor: &fixedSubagentExecutor{output: "done"}, Control: NewController(), Events: notifier}
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, Factory{Home: home, Owner: owner}); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	if err := agent.AppendHistory(Factory{Home: home}.subagentScopeRoot(), agent.HistoryEntry{
		TaskID:          "task-shape",
		RunID:           "child-shape",
		ParentRunID:     parent.ID,
		SessionID:       "session-1",
		WorkerSessionID: "session-1",
		Task:            "keep the shape",
		Status:          agent.StatusOK,
		Output:          "done",
		StartedAt:       10,
		UpdatedAt:       20,
		FinishedAt:      20,
		AgentType:       "fork",
		RuntimeKind:     "fork_subagent",
		Continuable:     true,
	}); err != nil {
		t.Fatalf("append history: %v", err)
	}
	callCtx := toolpkg.WithRunID(llm.WithAgentSessionID(ctx, "session-1"), parent.ID)

	sendTool, ok := aToolByName(a, "subagent_send")
	if !ok {
		t.Fatal("subagent_send not registered")
	}
	args, err := json.Marshal(map[string]any{
		"title":         "Shape check",
		"task":          "keep the result shape",
		"subagent_type": "general-purpose",
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	out, err := sendTool.Handle(callCtx, string(args))
	if err != nil {
		t.Fatalf("subagent_send: %v", err)
	}
	var send map[string]any
	if err := json.Unmarshal([]byte(out.(string)), &send); err != nil {
		t.Fatalf("decode subagent_send result: %v", err)
	}
	wantSend := []string{
		"agent_id", "task_id", "run_id", "parent_run_id", "session_id", "worker_session_id",
		"query_source", "status", "started_at", "agent_kind", "agent_type", "runtime_kind",
	}
	if len(send) != len(wantSend) {
		t.Fatalf("subagent_send result keys = %v, want exactly %v", mapKeys(send), wantSend)
	}
	for _, key := range wantSend {
		if _, ok := send[key]; !ok {
			t.Fatalf("subagent_send result is missing %q: %v", key, mapKeys(send))
		}
	}

	contTool, ok := aToolByName(a, "subagent_continue")
	if !ok {
		t.Fatal("subagent_continue not registered")
	}
	out, err = contTool.Handle(callCtx, `{"task_id":"task-shape","message":"keep going"}`)
	if err != nil {
		t.Fatalf("subagent_continue: %v", err)
	}
	var cont map[string]any
	if err := json.Unmarshal([]byte(out.(string)), &cont); err != nil {
		t.Fatalf("decode subagent_continue result: %v", err)
	}
	if len(cont) != 1 {
		t.Fatalf("subagent_continue result keys = %v, want exactly [record]", mapKeys(cont))
	}
	if _, ok := cont["record"]; !ok {
		t.Fatalf("subagent_continue result is missing \"record\": %v", mapKeys(cont))
	}
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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

func (panickingSubagentExecutor) RunSubagentExec(context.Context, SubagentExecRequest) (string, error) {
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

func (h *concurrencyRecordingSubagentExecutor) RunSubagentExec(ctx context.Context, _ SubagentExecRequest) (string, error) {
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

func (h *fixedSubagentExecutor) RunSubagentExec(ctx context.Context, req SubagentExecRequest) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.task = req.Task
	h.superviseID = req.SuperviseRunID
	h.parentRunID = req.ParentRunID
	h.sessionID = req.SessionID
	h.workerSessionID = req.WorkerSessionID
	h.subagentType = req.SubagentType
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

func (h *ctxCaptureSubagentExecutor) RunSubagentExec(ctx context.Context, _ SubagentExecRequest) (string, error) {
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
	// The subagent runs under its own channel runtime now, never the primary
	// agent's: inheriting the parent's would let a user steer meant for the
	// primary agent be drained into the subagent's loop.
	if got := TurnInputRuntimeFromContext(executor.captured); got == nil {
		t.Fatal("expected the subagent context to carry its own TurnInputRuntime")
	} else if got == rt {
		t.Fatal("subagent context must not carry the primary agent's TurnInputRuntime; user steers would be drained into the subagent loop")
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
	ctx := subagentRunContext(context.Background(), nil, probe, "child-run-1", "session-1", agent.HistoryEntry{TaskID: "task-search-1"})
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

func (h *gatedSubagentExecutor) RunSubagentExec(ctx context.Context, req SubagentExecRequest) (string, error) {
	task := req.Task
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

func (usageObservingSubagentExecutor) RunSubagentExec(ctx context.Context, _ SubagentExecRequest) (string, error) {
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

// The persistence port is a no-op on every stand-in executor that only needs
// the dispatch itself; the persistence tests below use recording executors.
func (noopSubagentExecutor) PersistSubagentTurn(context.Context, SubagentTurn) {}

func (panickingSubagentExecutor) PersistSubagentTurn(context.Context, SubagentTurn) {}

func (h *concurrencyRecordingSubagentExecutor) PersistSubagentTurn(context.Context, SubagentTurn) {}

func (h *fixedSubagentExecutor) PersistSubagentTurn(context.Context, SubagentTurn) {}

func (h *ctxCaptureSubagentExecutor) PersistSubagentTurn(context.Context, SubagentTurn) {}

func (h *gatedSubagentExecutor) PersistSubagentTurn(context.Context, SubagentTurn) {}

func (usageObservingSubagentExecutor) PersistSubagentTurn(context.Context, SubagentTurn) {}

// The auto-continue reporting is a no-op on every stand-in that is not the
// subject of the reporting test itself.
func (noopSubagentExecutor) SubagentExecutionStarting(context.Context, string)                    {}
func (noopSubagentExecutor) SubagentExecutionEnded(context.Context, SubagentExecutionEnd)         {}
func (panickingSubagentExecutor) SubagentExecutionStarting(context.Context, string)               {}
func (panickingSubagentExecutor) SubagentExecutionEnded(context.Context, SubagentExecutionEnd)    {}
func (h *concurrencyRecordingSubagentExecutor) SubagentExecutionStarting(context.Context, string) {}
func (h *concurrencyRecordingSubagentExecutor) SubagentExecutionEnded(context.Context, SubagentExecutionEnd) {
}
func (h *fixedSubagentExecutor) SubagentExecutionStarting(context.Context, string)                  {}
func (h *fixedSubagentExecutor) SubagentExecutionEnded(context.Context, SubagentExecutionEnd)       {}
func (h *ctxCaptureSubagentExecutor) SubagentExecutionStarting(context.Context, string)             {}
func (h *ctxCaptureSubagentExecutor) SubagentExecutionEnded(context.Context, SubagentExecutionEnd)  {}
func (h *gatedSubagentExecutor) SubagentExecutionStarting(context.Context, string)                  {}
func (h *gatedSubagentExecutor) SubagentExecutionEnded(context.Context, SubagentExecutionEnd)       {}
func (usageObservingSubagentExecutor) SubagentExecutionStarting(context.Context, string)            {}
func (usageObservingSubagentExecutor) SubagentExecutionEnded(context.Context, SubagentExecutionEnd) {}
func (e *persistingSubagentExecutor) SubagentExecutionStarting(context.Context, string)             {}
func (e *persistingSubagentExecutor) SubagentExecutionEnded(context.Context, SubagentExecutionEnd)  {}
func (e *snapshotReportingSubagentExecutor) SubagentExecutionStarting(context.Context, string)      {}
func (e *snapshotReportingSubagentExecutor) SubagentExecutionEnded(context.Context, SubagentExecutionEnd) {
}
func (e *channelTestExecutor) SubagentExecutionStarting(context.Context, string)                  {}
func (e *channelTestExecutor) SubagentExecutionEnded(context.Context, SubagentExecutionEnd)       {}
func (e *steerableSubagentExecutor) SubagentExecutionStarting(context.Context, string)            {}
func (e *steerableSubagentExecutor) SubagentExecutionEnded(context.Context, SubagentExecutionEnd) {}

// scriptedRequestsLLM records every request it is sent and answers with the
// next scripted text. It stands in for the model in the persistence and
// continuation tests, which compare whole requests, not answers.
type scriptedRequestsLLM struct {
	mu        sync.Mutex
	requests  [][]llm.Message
	tools     [][]*llm.Tool
	responses []string
}

func (m *scriptedRequestsLLM) Execute(_ context.Context, msgs []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	cp := deepCloneMessages(msgs)
	m.mu.Lock()
	m.requests = append(m.requests, cp)
	m.tools = append(m.tools, tools)
	text := ""
	if len(m.responses) > 0 {
		idx := len(m.requests) - 1
		if idx >= len(m.responses) {
			idx = len(m.responses) - 1
		}
		text = m.responses[idx]
	}
	m.mu.Unlock()
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(text)})
	return &llm.Result{Message: &msg}, nil
}

func (m *scriptedRequestsLLM) gotRequests() [][]llm.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]llm.Message(nil), m.requests...)
}

func (m *scriptedRequestsLLM) gotTools() [][]*llm.Tool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]*llm.Tool(nil), m.tools...)
}

// persistingSubagentExecutor mirrors what the composition root's executor
// does with a finished turn: it writes the result's session rows into the
// worker session and keeps the turn it was handed, so a test can assert on
// it. Its RunSubagentExec drives a real Runner over the worker session — the
// store-backed session builder and all — which is the piece pkg/process adds
// around this same call.
type persistingSubagentExecutor struct {
	runner *Runner
	llm    *scriptedRequestsLLM

	mu    sync.Mutex
	turns []SubagentTurn
}

func (e *persistingSubagentExecutor) RunSubagentExec(ctx context.Context, req SubagentExecRequest) (string, error) {
	task := req.Task
	workerSessionID := req.WorkerSessionID
	// Mirror the dispatch's user-message write: the same plain row a surface
	// persists before its run, which the session builder then replaces with
	// the enriched version. Rows bind no run here; the binding is covered by
	// the pkg/process tests, which drive real run rows.
	_ = e.runner.Deps.SessionStore.AppendMessageSequenceForRun(context.Background(), workerSessionID, "", []llm.Message{llm.UserMessage(llm.Text(task))}, "", "")
	res, err := e.runner.RunContent(llm.WithAgentSessionID(ctx, workerSessionID), []llm.ContentPart{llm.Text(task)})
	if err != nil {
		return "", err
	}
	return res.TextContent(), nil
}

func (e *persistingSubagentExecutor) PersistSubagentTurn(_ context.Context, turn SubagentTurn) {
	e.mu.Lock()
	e.turns = append(e.turns, turn)
	e.mu.Unlock()
	if turn.Err != nil || turn.Result == nil {
		return
	}
	_ = e.runner.Deps.SessionStore.AppendMessageSequenceForRun(context.Background(), turn.WorkerSessionID, "", turn.Result.Session, turn.Model, "")
}

func (e *persistingSubagentExecutor) recordedTurns() []SubagentTurn {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]SubagentTurn(nil), e.turns...)
}

// newPersistenceFixture builds a factory whose owner carries a session store,
// a real worker runner (session builder over the store), and a recording
// executor. Everything the persistence path needs, nothing it does not.
type persistenceFixture struct {
	fac      Factory
	store    *state.SessionStore
	home     string
	executor *persistingSubagentExecutor
	llm      *scriptedRequestsLLM
}

func newPersistenceFixture(t *testing.T, systemPrompt string, responses ...string) *persistenceFixture {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	scripted := &scriptedRequestsLLM{responses: responses}
	// The worker agent's description is what a fork of this runner inherits
	// as its system when its turn captured nothing, so it carries the same
	// system the session builder is built with.
	worker, err := agent.New(scripted, "worker", systemPrompt)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	builder := transcriptSession{store: store, systemPrompt: systemPrompt, resolver: nil, projectInstructions: nil}
	worker.SetSessionBuilder(builder.build)
	worker.SetTracer(agent.Noop)
	executor := &persistingSubagentExecutor{llm: scripted}
	owner := &Runner{
		Deps:             &Deps{Home: home, SessionStore: store},
		main:             worker,
		SubagentExecutor: executor,
		forkLLM:          scripted,
	}
	runner := &Runner{Deps: &Deps{Home: home, SessionStore: store}, main: worker}
	executor.runner = runner
	fac := Factory{AgentName: "main", Home: home, Owner: owner}
	return &persistenceFixture{fac: fac, store: store, home: home, executor: executor, llm: scripted}
}

func (f *persistenceFixture) seedWorker(t *testing.T, conversationID, workerID string, msgs []llm.Message) {
	t.Helper()
	ctx := context.Background()
	if err := f.store.Ensure(ctx, conversationID, conversationID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.EnsureAt(ctx, workerID, workerID, state.SessionBirth{
		Source:          state.SessionSourceSubagent,
		ParentSessionID: conversationID,
	}); err != nil {
		t.Fatal(err)
	}
	if len(msgs) > 0 {
		// The seed rows stand for a finished prior execution; the run they
		// were bound to must exist, the way every persisted row's run does.
		if _, err := f.store.DB().Exec(`INSERT INTO fb_runs(id, session_id, parent_run_id, input_text, status, owner, created_at, updated_at)
			VALUES('seed-run', ?, NULL, 'prior execution', 'done', '', 1, 1)`, conversationID); err != nil {
			t.Fatal(err)
		}
		if err := f.store.AppendMessageSequenceForRun(ctx, workerID, "seed-run", msgs, "", ""); err != nil {
			t.Fatal(err)
		}
	}
}

// ensureConversation opens the dispatching conversation. Production always
// has one — it is what dispatched the subagent — so the tests that drive a
// dispatch open it before the dispatch, exactly as a surface would.
func (f *persistenceFixture) ensureConversation(t *testing.T, conversationID string) {
	t.Helper()
	if err := f.store.Ensure(context.Background(), conversationID, conversationID); err != nil {
		t.Fatal(err)
	}
}

func (f *persistenceFixture) workerRows(t *testing.T, workerID string) []llm.Message {
	t.Helper()
	rows, err := f.store.ListTranscriptMessages(context.Background(), workerID, 200)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// armForCompaction gives the fixture's owner the three things CompactionService
// reads that a dispatch never exercises on it: the summary chain, the
// transcript a summary request is rebuilt from, and the config the threshold
// and the compact prompt come from. limit is the auto-compact threshold; zero
// keeps the default, far above anything these fixtures store.
func (f *persistenceFixture) armForCompaction(systemPrompt string, limit int, prompt string) {
	owner := f.fac.Owner
	owner.summaryLLM = f.llm
	owner.transcript = transcriptSession{store: f.store, systemPrompt: systemPrompt}
	owner.AppCfg = &appcfg.Root{
		Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
			// A constructable client for the compaction's fallback path; the
			// conversation summarizer answers before it is ever dialed.
			"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{
				{Provider: "openai", Model: "unused-model", APIKey: "k", BaseURL: "https://example.invalid"},
			}},
		}},
		Compact: appcfg.CompactSection{ModelAutoCompactTokenLimit: limit, Prompt: prompt},
	}
}

// compactEventCollector keeps the run events the owner published so a test can
// assert on a compaction's routing.
type compactEventCollector struct {
	mu     sync.Mutex
	events []event.RunEvent
}

func (c *compactEventCollector) Publish(_ context.Context, evt event.RunEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, evt)
	return nil
}

func (c *compactEventCollector) compacted() []event.ContextCompactedPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]event.ContextCompactedPayload, 0, len(c.events))
	for _, evt := range c.events {
		if evt.Type != event.RunEventContextCompacted {
			continue
		}
		var payload event.ContextCompactedPayload
		if json.Unmarshal(evt.Payload, &payload) == nil {
			out = append(out, payload)
		}
	}
	return out
}

func messageTexts(rows []llm.Message) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Role+"|"+row.TextContent())
	}
	return out
}

// TestForkSubagentWritesItsInheritedPrefixAndItsWork pins the fork half of
// the persistence: the inherited prefix and the instruction land in the
// worker session the moment the fork has them, the fork's answer lands when
// it finishes, and the system the fork ran on is the one frozen at birth —
// the exact bytes a continuation replays.
func TestForkSubagentWritesItsInheritedPrefixAndItsWork(t *testing.T) {
	fix := newPersistenceFixture(t, "parent system", "fork answer")
	parentMsgs := []llm.Message{
		llm.UserMessage(llm.Text("parent question")),
		llm.AssistantMessage(nil, llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "search", Arguments: "{}"}}),
	}
	fix.ensureConversation(t, "conv-fork")
	ctx := withForkRuntimeSnapshot(llm.WithAgentSessionID(context.Background(), "conv-fork"), parentMsgs)
	ctx = toolpkg.WithRunID(ctx, "parent-run-1")
	res, err := execGeneralSubagent(ctx, fix.fac, "task-fork", "dig", "look into it", "")
	if err != nil {
		t.Fatalf("execGeneralSubagent: %v", err)
	}
	if res.Output != "fork answer" {
		t.Fatalf("output = %q, want the scripted answer", res.Output)
	}
	var workerID string
	for _, turn := range fix.executor.recordedTurns() {
		workerID = turn.WorkerSessionID
	}
	if workerID == "" {
		t.Fatal("the fork persisted no turn")
	}
	// The worker session holds the inherited prefix (the parent request minus
	// its system), the instruction, and the fork's own answer.
	rows := fix.workerRows(t, workerID)
	// The inherited prefix is the parent snapshot minus its system: the user
	// turn and the assistant tool_calls, then the cloned call with its
	// placeholder result, the instruction, and finally the fork's answer.
	wantRoles := []string{
		llm.RoleUser,
		llm.RoleAssistant,
		llm.RoleAssistant,
		llm.RoleTool,
		llm.RoleUser,
		llm.RoleAssistant,
	}
	if len(rows) != len(wantRoles) {
		t.Fatalf("worker session rows = %v, want %d rows", messageTexts(rows), len(wantRoles))
	}
	for i, role := range wantRoles {
		if rows[i].Role != role {
			t.Fatalf("worker session row %d = %q, want role %q", i, messageTexts(rows)[i], role)
		}
	}
	if !strings.Contains(rows[4].TextContent(), "look into it") {
		t.Fatalf("directive row = %q, want the fork's instruction", rows[4].TextContent())
	}
	if rows[5].TextContent() != "fork answer" {
		t.Fatalf("answer row = %q, want the scripted answer", rows[5].TextContent())
	}
	// The system the fork's first request carried is frozen for the life of
	// the fork, and it is the request's own system text.
	frozen, ok, err := fix.store.SessionPromptState(context.Background(), workerID, forkSystemPromptKey)
	if err != nil || !ok {
		t.Fatalf("frozen system = %q ok=%v err=%v", frozen, ok, err)
	}
	if frozen != "parent system" {
		t.Fatalf("frozen system = %q, want the owning system the fork inherited", frozen)
	}
}

// TestForkSubagentFirstRequestIsUnchangedByPersistence is the fork golden
// test: persistence is an observer of RunFork, so the first request must be
// byte for byte the request the same inputs produce without any of it. The
// baseline calls RunFork directly — the same params the dispatch builds,
// minus OnInitialMessages and the post-run persist.
func TestForkSubagentFirstRequestIsUnchangedByPersistence(t *testing.T) {
	parentMsgs := []llm.Message{
		llm.UserMessage(llm.Text("parent question")),
		llm.AssistantMessage(nil, llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "search", Arguments: "{}"}}),
	}
	task := "look into it"

	// Baseline: RunFork alone, over the same captured request and directive.
	baselineLLM := &scriptedRequestsLLM{responses: []string{"fork answer"}}
	if _, err := RunFork(context.Background(), RunParams{
		LLM: baselineLLM,
		CacheSafe: &CacheSafeParams{
			SystemPrompt:         "parent system",
			RenderedSystemPrompt: "parent system",
			ParentMessages:       deepCloneMessages(parentMsgs),
		},
		PromptMessages: mustBuildForkedMessages(t, task, parentMsgs[len(parentMsgs)-1]),
		CanUseTool:     func(string) bool { return true },
		AgentBaseName:  "subagent",
		AgentType:      "fork",
		AgentID:        "agent-golden",
		WorkspaceRoot:  t.TempDir(),
		SessionID:      "conv-golden",
		ForkLabel:      "subagent",
		RegisterTools:  func(*ToolRegistry) error { return nil },
	}); err != nil {
		t.Fatalf("baseline RunFork: %v", err)
	}

	// After: the full fork dispatch, birth, freeze, OnInitialMessages and
	// persist included.
	fix := newPersistenceFixture(t, "parent system", "fork answer")
	fix.ensureConversation(t, "conv-golden")
	ctx := withForkRuntimeSnapshot(llm.WithAgentSessionID(context.Background(), "conv-golden"), parentMsgs)
	ctx = toolpkg.WithRunID(ctx, "parent-run-1")
	if _, err := execGeneralSubagent(ctx, fix.fac, "task-golden", "dig", task, ""); err != nil {
		t.Fatalf("dispatched fork: %v", err)
	}

	base := baselineLLM.gotRequests()
	after := fix.llm.gotRequests()
	if len(base) == 0 || len(after) == 0 {
		t.Fatalf("requests captured: baseline %d, dispatched %d", len(base), len(after))
	}
	before, err := json.Marshal(base[0])
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, err := json.Marshal(after[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(afterBytes) {
		t.Fatalf("first fork request changed byte for byte:\nbefore: %s\nafter:  %s", before, afterBytes)
	}
}

func mustBuildForkedMessages(t *testing.T, directive string, parentAssistant llm.Message) []llm.Message {
	t.Helper()
	msgs, err := BuildForkedMessages(directive, parentAssistant)
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

// TestTypedContinuationReplaysTheWorkerConversation pins the continuation on
// the conversation the subagent already had: the follow-up goes in verbatim
// and the request the continuation sends is the previous execution's
// conversation plus one user message — never the stitched prompt.
func TestTypedContinuationReplaysTheWorkerConversation(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system", "picking it up again")
	workerID := "main:conv-replay:worker:cccccccc-1111-2222-3333-444444444444"
	fix.seedWorker(t, "conv-replay", workerID, []llm.Message{
		llm.UserMessage(llm.Text("investigate the bug")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("it was the lock")}),
	})
	if err := agent.AppendHistory(fix.fac.subagentScopeRoot(), agent.HistoryEntry{
		TaskID:          "task-replay",
		RunID:           "seed-run",
		ParentRunID:     "parent-1",
		SessionID:       "conv-replay",
		WorkerSessionID: workerID,
		Task:            "investigate the bug",
		Status:          agent.StatusOK,
		Output:          "it was the lock",
		AgentType:       "explore",
		AgentKind:       "typed",
		RuntimeKind:     "typed_subagent",
		Continuable:     true,
		StartedAt:       10,
		UpdatedAt:       20,
		FinishedAt:      20,
	}); err != nil {
		t.Fatal(err)
	}
	ctx := toolpkg.WithToolUseID(toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), "conv-replay"), "parent-1"), "call-1")
	if _, err := continueSubagentExecution(ctx, fix.fac, &SubagentContinueInput{TaskID: "task-replay", Message: "continue"}); err != nil {
		t.Fatalf("continueSubagentExecution: %v", err)
	}
	requests := fix.llm.gotRequests()
	if len(requests) == 0 {
		t.Fatal("the continuation sent no request")
	}
	want := []string{
		llm.RoleSystem + "|worker system",
		llm.RoleUser + "|investigate the bug",
		llm.RoleAssistant + "|it was the lock",
		llm.RoleUser + "|continue",
	}
	if got := messageTexts(requests[0]); len(got) != len(want) {
		t.Fatalf("continuation request = %v, want the previous conversation plus the message", got)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("continuation request row %d = %q, want %q", i, got[i], want[i])
			}
		}
	}
	for _, row := range requests[0] {
		if strings.Contains(row.TextContent(), "Continue the existing worker task") {
			t.Fatalf("continuation request carries the stitched legacy prompt: %q", row.TextContent())
		}
	}
}

// TestContinuationFindsADispatchFromAnEarlierTurn pins the cross-turn
// continuation: a subagent dispatched in one turn is addressed by its task id
// when a later turn continues it. The lookup stays scoped to the conversation,
// never to the turn that is asking — the asking turn's run id is not the
// subagent's parent run, and filtering on it made every cross-turn
// subagent_continue answer "subagent not found" (plan 002's real-device run
// hit exactly that).
func TestContinuationFindsADispatchFromAnEarlierTurn(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system", "on it")
	workerID := "main:conv-cross:worker:ffff0000-1111-2222-3333-444444444444"
	fix.seedWorker(t, "conv-cross", workerID, []llm.Message{
		llm.UserMessage(llm.Text("investigate")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("the lock")}),
	})
	if err := agent.AppendHistory(fix.fac.subagentScopeRoot(), agent.HistoryEntry{
		TaskID:          "task-cross",
		RunID:           "seed-run",
		ParentRunID:     "turn-1-run",
		SessionID:       "conv-cross",
		WorkerSessionID: workerID,
		Task:            "investigate",
		Status:          agent.StatusOK,
		Output:          "the lock",
		AgentType:       "explore",
		AgentKind:       "typed",
		RuntimeKind:     "typed_subagent",
		Continuable:     true,
		StartedAt:       10,
		UpdatedAt:       20,
		FinishedAt:      20,
	}); err != nil {
		t.Fatal(err)
	}
	// The asking turn is a later one: its run id is not the subagent's parent.
	ctx := toolpkg.WithToolUseID(toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), "conv-cross"), "turn-2-run"), "call-1")
	if _, err := continueSubagentExecution(ctx, fix.fac, &SubagentContinueInput{TaskID: "task-cross", Message: "go on"}); err != nil {
		t.Fatalf("cross-turn continuation: %v", err)
	}
}

// TestContinuationOfALegacySubagentSeedsItsWorkerSession pins the treatment
// of existing data: a record born before persistence has an empty worker
// session, so its first continuation runs on the stitched prompt — which
// becomes the session's first user message — and its next continuation
// speaks to the conversation like any new subagent.
func TestContinuationOfALegacySubagentSeedsItsWorkerSession(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system", "on it", "done again")
	workerID := "main:conv-legacy:worker:dddddddd-1111-2222-3333-444444444444"
	// A record born before persistence: no worker session row at all.
	if err := agent.AppendHistory(fix.fac.subagentScopeRoot(), agent.HistoryEntry{
		TaskID:          "task-legacy",
		RunID:           "legacy-run",
		ParentRunID:     "parent-1",
		SessionID:       "conv-legacy",
		WorkerSessionID: workerID,
		Task:            "look into it",
		Status:          agent.StatusOK,
		Output:          "partial finding",
		AgentType:       "explore",
		AgentKind:       "typed",
		RuntimeKind:     "typed_subagent",
		Continuable:     true,
		StartedAt:       10,
		UpdatedAt:       20,
		FinishedAt:      20,
	}); err != nil {
		t.Fatal(err)
	}
	fix.ensureConversation(t, "conv-legacy")
	ctx := toolpkg.WithToolUseID(toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), "conv-legacy"), "parent-1"), "call-1")
	first, err := continueSubagentExecution(ctx, fix.fac, &SubagentContinueInput{TaskID: "task-legacy", Message: "keep going"})
	if err != nil {
		t.Fatalf("first continuation: %v", err)
	}
	// The first continuation's input is the stitched prompt, and the seeding
	// executor mirrors the composition root: that prompt is now the worker
	// session's first user row.
	requests := fix.llm.gotRequests()
	if len(requests) == 0 {
		t.Fatal("the first continuation sent no request")
	}
	stitched := requests[0][len(requests[0])-1].TextContent()
	if !strings.Contains(stitched, "Continue the existing worker task") ||
		!strings.Contains(stitched, "look into it") ||
		!strings.Contains(stitched, "partial finding") ||
		!strings.Contains(stitched, "keep going") {
		t.Fatalf("first continuation input = %q, want the stitched prompt", stitched)
	}
	rows := fix.workerRows(t, workerID)
	if len(rows) == 0 || rows[0].Role != llm.RoleUser || rows[0].TextContent() != stitched {
		t.Fatalf("worker session rows = %v, want the stitched prompt as the first row", messageTexts(rows))
	}
	// The second continuation speaks to the conversation: verbatim message.
	if _, err := continueSubagentExecution(ctx, fix.fac, &SubagentContinueInput{TaskID: "task-legacy", Message: "and then"}); err != nil {
		t.Fatalf("second continuation: %v", err)
	}
	requests = fix.llm.gotRequests()
	if len(requests) < 2 {
		t.Fatal("the second continuation sent no request")
	}
	second := requests[len(requests)-1]
	if last := second[len(second)-1]; last.Role != llm.RoleUser || last.TextContent() != "and then" {
		t.Fatalf("second continuation input = %q, want the message verbatim", last.TextContent())
	}
	if first.Output == "" {
		t.Fatal("the continuation produced no output")
	}
}

// TestForkContinuationReplaysItsOwnPrefixByteForByte pins the fork's
// continuation (decision D6): the system it was born with and the transcript
// it holds are replayed as the inherited prefix, so the continuation's
// request is the previous request plus the message — and the sidechain log
// keeps one copy per message, not a second full history.
func TestForkContinuationReplaysItsOwnPrefixByteForByte(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system", "continuing")
	workerID := "main:conv-forkcont:worker:eeeeeeee-1111-2222-3333-444444444444"
	fix.seedWorker(t, "conv-forkcont", workerID, []llm.Message{
		llm.UserMessage(llm.Text("forked directive")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("fork finding")}),
	})
	if _, _, err := fix.store.FreezeSessionPromptState(context.Background(), workerID, forkSystemPromptKey, "frozen fork system"); err != nil {
		t.Fatal(err)
	}
	if err := agent.AppendHistory(fix.fac.subagentScopeRoot(), agent.HistoryEntry{
		TaskID:          "task-forkcont",
		AgentID:         "fork-cont-agent",
		RunID:           "seed-run",
		ParentRunID:     "parent-1",
		SessionID:       "conv-forkcont",
		WorkerSessionID: workerID,
		Task:            "forked directive",
		Status:          agent.StatusOK,
		Output:          "fork finding",
		AgentType:       "fork",
		AgentKind:       "fork",
		RuntimeKind:     "fork_subagent",
		Continuable:     true,
		StartedAt:       10,
		UpdatedAt:       20,
		FinishedAt:      20,
	}); err != nil {
		t.Fatal(err)
	}
	ctx := toolpkg.WithToolUseID(toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), "conv-forkcont"), "parent-1"), "call-1")
	if _, err := continueSubagentExecution(ctx, fix.fac, &SubagentContinueInput{TaskID: "task-forkcont", Message: "go on"}); err != nil {
		t.Fatalf("continueSubagentExecution: %v", err)
	}
	requests := fix.llm.gotRequests()
	if len(requests) == 0 {
		t.Fatal("the continuation sent no request")
	}
	request := requests[len(requests)-1]
	// A fork's request is its transcript plus the message: the fork agent's
	// session builder replays the worker session alone (the frozen system is
	// the prefix the fork's client holds). What the byte-for-byte rule needs
	// is that the continuation's request is the previous request plus the
	// message, which this shape pins.
	want := []string{
		llm.RoleUser + "|forked directive",
		llm.RoleAssistant + "|fork finding",
		llm.RoleUser + "|go on",
	}
	if got := messageTexts(request); len(got) != len(want) {
		t.Fatalf("fork continuation request = %v, want the transcript and the message", got)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("fork continuation request row %d = %q, want %q", i, got[i], want[i])
			}
		}
	}
	if frozen, ok, err := fix.store.SessionPromptState(context.Background(), workerID, forkSystemPromptKey); err != nil || !ok || frozen != "frozen fork system" {
		t.Fatalf("frozen system = %q ok=%v err=%v, want the birth value still frozen", frozen, ok, err)
	}
	// The sidechain log carries the continuation's new message only: the
	// inherited history must not be written into it a second time.
	sidechain := SidechainFilePath(fix.fac.workspaceRoot(), "conv-forkcont", "subagent", "fork-cont-agent")
	raw, err := os.ReadFile(sidechain)
	if err != nil {
		t.Fatalf("sidechain log: %v", err)
	}
	// One copy per message: the continuation appends its new user message and
	// its answer, and never the inherited history a second time.
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "go on") || !strings.Contains(lines[1], "continuing") {
		t.Fatalf("sidechain log has %d lines, want the new message and the answer only: %q", len(lines), raw)
	}
}

// TestForkSubagentContinuationCompactsFirstWhenItNoLongerFits is the fork half
// of the pre-turn compaction: a fork whose worker session has crossed the
// auto-compact threshold compacts before its new user message is written, so
// the boundary predates the new turn, the new turn rides on it, and the
// compaction reports itself under the fork's roster key. The fork's own LLM
// chain carries no mid-turn checkpoint, so the one boundary in the session is
// the pre-turn one.
func TestForkSubagentContinuationCompactsFirstWhenItNoLongerFits(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system", "fork compaction summary", "continuation answer")
	fix.armForCompaction("worker system", 1000, "")
	workerID := "main:conv-forkpt:worker:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeffff"
	detail := strings.Repeat("detail ", 1200)
	fix.seedWorker(t, "conv-forkpt", workerID, []llm.Message{
		llm.UserMessage(llm.Text(detail)),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("fork finding")}),
	})
	if _, _, err := fix.store.FreezeSessionPromptState(context.Background(), workerID, forkSystemPromptKey, "frozen fork system"); err != nil {
		t.Fatal(err)
	}
	if err := agent.AppendHistory(fix.fac.subagentScopeRoot(), agent.HistoryEntry{
		TaskID:          "task-forkpt",
		RunID:           "seed-run",
		ParentRunID:     "parent-1",
		SessionID:       "conv-forkpt",
		WorkerSessionID: workerID,
		Task:            "forked directive",
		Status:          agent.StatusOK,
		Output:          "fork finding",
		AgentType:       "fork",
		AgentKind:       "fork",
		RuntimeKind:     "fork_subagent",
		Continuable:     true,
		StartedAt:       10,
		UpdatedAt:       20,
		FinishedAt:      20,
	}); err != nil {
		t.Fatal(err)
	}
	events := &compactEventCollector{}
	fix.fac.Owner.Events = events
	ctx := toolpkg.WithToolUseID(toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), "conv-forkpt"), "parent-1"), "call-1")
	if _, err := continueSubagentExecution(ctx, fix.fac, &SubagentContinueInput{TaskID: "task-forkpt", Message: "go on"}); err != nil {
		t.Fatalf("continueSubagentExecution: %v", err)
	}

	// Exactly one compaction — the pre-turn one — filed under the roster key.
	compacted := events.compacted()
	if len(compacted) != 1 {
		t.Fatalf("compaction events = %d, want the single pre-turn compaction", len(compacted))
	}
	if compacted[0].AgentID != "task-forkpt" {
		t.Fatalf("compaction AgentID = %q, want the roster key %q", compacted[0].AgentID, "task-forkpt")
	}
	boundaryID, _, err := fix.store.LatestCompactBoundary(context.Background(), workerID)
	if err != nil {
		t.Fatal(err)
	}
	if boundaryID <= 0 {
		t.Fatal("the worker session has no compaction boundary")
	}
	// The boundary predates the new user turn: the rows are the compacted
	// history, then the new turn, then the answer.
	rows := fix.workerRows(t, workerID)
	want := []string{llm.RoleUser, llm.RoleUser, llm.RoleUser, llm.RoleAssistant}
	if len(rows) != len(want) {
		t.Fatalf("worker session rows = %v, want %v", messageTexts(rows), want)
	}
	for i, role := range want {
		if rows[i].Role != role {
			t.Fatalf("worker session rows = %v, want roles %v", messageTexts(rows), want)
		}
	}
	if !strings.Contains(rows[1].TextContent(), "fork compaction summary") {
		t.Fatalf("summary row = %q, want the scripted summary", rows[1].TextContent())
	}
	if rows[2].TextContent() != "go on" {
		t.Fatalf("new turn row = %q, want the continuation message after the boundary", rows[2].TextContent())
	}
	// The continuation's request rides on the boundary: the compacted
	// history, then the new turn. (The fork's system is the agent's own, not
	// a message row — the summary request is where the frozen bytes show.)
	requests := fix.llm.gotRequests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want the summary request and the continuation request", len(requests))
	}
	got := messageTexts(requests[1])
	wantRequest := []string{
		llm.RoleUser + "|" + detail,
		llm.RoleUser + "|Another language model",
		llm.RoleUser + "|go on",
	}
	if len(got) != len(wantRequest) {
		t.Fatalf("continuation request = %v, want the compacted context plus the message", got)
	}
	for i := range wantRequest {
		if i == 1 {
			if !strings.HasPrefix(got[i], llm.RoleUser+"|Another language model") {
				t.Fatalf("continuation request row %d = %q, want the summary checkpoint", i, got[i])
			}
			continue
		}
		if got[i] != wantRequest[i] {
			t.Fatalf("continuation request row %d = %q, want %q", i, got[i], wantRequest[i])
		}
	}
}

// TestSubagentCompactTargetRefusesARunningSubagent pins the guard: a manual
// compaction rewrites history, so it is refused while an execution of that
// subagent is still appending to it — the same stance /compact takes on the
// conversation's own running turn.
func TestSubagentCompactTargetRefusesARunningSubagent(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system")
	if err := agent.AppendHistory(fix.fac.subagentScopeRoot(), agent.HistoryEntry{
		TaskID:          "task-busy",
		RunID:           "seed-run",
		ParentRunID:     "parent-1",
		SessionID:       "conv-refuse",
		WorkerSessionID: "main:conv-refuse:worker:11111111-2222-3333-4444-555555555555",
		Task:            "busy work",
		Status:          agent.StatusRunning,
		AgentType:       "explore",
		AgentKind:       "typed",
		RuntimeKind:     "typed_subagent",
		StartedAt:       10,
		UpdatedAt:       20,
	}); err != nil {
		t.Fatal(err)
	}
	// "Running" is the channel executing now: the guard reads the channel, not
	// the registry handle, so a user-driven execution is refused too.
	record, ok, err := agent.GetMerged(fix.fac.subagentScopeRoot(), agent.Query{SessionID: "conv-refuse", TaskID: "task-busy"})
	if err != nil || !ok {
		t.Fatalf("seed record: ok=%v err=%v", ok, err)
	}
	ch := subagentChannelFor(fix.fac, record)
	ex := &subagentExecution{}
	ch.setRunning(ex)
	t.Cleanup(func() { ch.mu.Lock(); ch.running = nil; ch.mu.Unlock() })

	_, _, err = SubagentCompactTarget(context.Background(), fix.fac.Owner, "conv-refuse", "task-busy")
	if !errors.Is(err, ErrSubagentRunning) {
		t.Fatalf("err = %v, want ErrSubagentRunning", err)
	}
}

// compactSummaryInstruction is the closing instruction assembly appends to
// every summary request; pinned here so the byte-for-byte comparison below
// states the whole request.
const compactSummaryInstruction = "Reply with the summary itself as plain text. Do not call any tools."

// TestSubagentManualCompactionReusesTheSubagentsPrefix pins the cache contract
// of a manual /compact run from a subagent's view: the summary request is the
// subagent's own next request — the same system, the same conversation, the
// same tools — plus the summary instruction, so it hits the prefix the
// subagent's requests already cached.
func TestSubagentManualCompactionReusesTheSubagentsPrefix(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system", "typed continuation answer", "typed compaction summary")
	fix.armForCompaction("worker system", 0, "summarize the typed conversation")
	workerID := "main:conv-tmanual:worker:aaaaaaaa-bbbb-cccc-dddd-eeeeeeee1111"
	fix.seedWorker(t, "conv-tmanual", workerID, []llm.Message{
		llm.UserMessage(llm.Text("investigate the bug")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("it was the lock")}),
	})
	if err := agent.AppendHistory(fix.fac.subagentScopeRoot(), agent.HistoryEntry{
		TaskID:          "task-tmanual",
		RunID:           "seed-run",
		ParentRunID:     "parent-1",
		SessionID:       "conv-tmanual",
		WorkerSessionID: workerID,
		Task:            "investigate the bug",
		Status:          agent.StatusOK,
		Output:          "it was the lock",
		AgentType:       "explore",
		AgentKind:       "typed",
		RuntimeKind:     "typed_subagent",
		Continuable:     true,
		StartedAt:       10,
		UpdatedAt:       20,
		FinishedAt:      20,
	}); err != nil {
		t.Fatal(err)
	}
	ctx := toolpkg.WithToolUseID(toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), "conv-tmanual"), "parent-1"), "call-1")
	if _, err := continueSubagentExecution(ctx, fix.fac, &SubagentContinueInput{TaskID: "task-tmanual", Message: "continue"}); err != nil {
		t.Fatalf("continueSubagentExecution: %v", err)
	}
	requests := fix.llm.gotRequests()
	if len(requests) == 0 {
		t.Fatal("the continuation sent no request")
	}
	lastRun := requests[len(requests)-1]

	subCtx, worker, err := SubagentCompactTarget(context.Background(), fix.fac.Owner, "conv-tmanual", "task-tmanual")
	if err != nil {
		t.Fatalf("SubagentCompactTarget: %v", err)
	}
	if worker != workerID {
		t.Fatalf("worker session = %q, want %q", worker, workerID)
	}
	if _, err := CompactionService(fix.fac.Owner, fix.store).ManualCompactSession(subCtx, worker, "manual"); err != nil {
		t.Fatalf("ManualCompactSession: %v", err)
	}

	all := fix.llm.gotRequests()
	summary := all[len(all)-1]
	if len(summary) == 0 || summary[len(summary)-1].Role != llm.RoleUser ||
		!strings.HasPrefix(summary[len(summary)-1].TextContent(), "summarize the typed conversation") {
		t.Fatalf("summary request ends with %v, want the summary instruction", summary[len(summary)-1])
	}
	// The request before the instruction is the subagent's own request, byte
	// for byte: the same system, the same conversation, the same order. (The
	// fixture's typed executor returns the run's text without persisting the
	// answer row the real executor writes, so the last request and the
	// summary share their tail exactly as the plan words it.)
	want := append(append([]llm.Message(nil), lastRun...),
		llm.UserMessage(llm.Text("summarize the typed conversation\n\n"+compactSummaryInstruction)),
	)
	assertMessagesJSONEqual(t, "typed summary request", summary, want)
	// The tools the summary request carries are the ones the run's requests
	// carried — the same table, so the same cached prefix.
	runTools := fix.llm.gotTools()[len(requests)-1]
	summaryTools := fix.llm.gotTools()[len(all)-1]
	if strings.Join(toolNames(runTools), ",") != strings.Join(toolNames(summaryTools), ",") {
		t.Fatalf("summary tools %v differ from the run's %v", toolNames(summaryTools), toolNames(runTools))
	}
}

// TestForkSubagentManualCompactionReusesItsFrozenPrefix is the fork twin: the
// summary request opens with the system frozen at the fork's birth — not the
// conversation's — and carries the fork's own conversation, so it reuses the
// prefix the fork's requests cached.
func TestForkSubagentManualCompactionReusesItsFrozenPrefix(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system", "fork continuation answer", "fork compaction summary")
	fix.armForCompaction("worker system", 0, "summarize the fork conversation")
	workerID := "main:conv-fmanual:worker:aaaaaaaa-bbbb-cccc-dddd-eeeeeeee2222"
	fix.seedWorker(t, "conv-fmanual", workerID, []llm.Message{
		llm.UserMessage(llm.Text("forked directive")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("fork finding")}),
	})
	if _, _, err := fix.store.FreezeSessionPromptState(context.Background(), workerID, forkSystemPromptKey, "frozen fork system"); err != nil {
		t.Fatal(err)
	}
	if err := agent.AppendHistory(fix.fac.subagentScopeRoot(), agent.HistoryEntry{
		TaskID:          "task-fmanual",
		RunID:           "seed-run",
		ParentRunID:     "parent-1",
		SessionID:       "conv-fmanual",
		WorkerSessionID: workerID,
		Task:            "forked directive",
		Status:          agent.StatusOK,
		Output:          "fork finding",
		AgentType:       "fork",
		AgentKind:       "fork",
		RuntimeKind:     "fork_subagent",
		Continuable:     true,
		StartedAt:       10,
		UpdatedAt:       20,
		FinishedAt:      20,
	}); err != nil {
		t.Fatal(err)
	}
	ctx := toolpkg.WithToolUseID(toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), "conv-fmanual"), "parent-1"), "call-1")
	if _, err := continueSubagentExecution(ctx, fix.fac, &SubagentContinueInput{TaskID: "task-fmanual", Message: "go on"}); err != nil {
		t.Fatalf("continueSubagentExecution: %v", err)
	}
	requests := fix.llm.gotRequests()
	if len(requests) == 0 {
		t.Fatal("the continuation sent no request")
	}
	lastRun := requests[len(requests)-1]
	// The fork's own requests carry no system row — the agent holds it — so
	// the frozen system is pinned on the summary request below, where the
	// summarizer's head carries it explicitly.

	subCtx, worker, err := SubagentCompactTarget(context.Background(), fix.fac.Owner, "conv-fmanual", "task-fmanual")
	if err != nil {
		t.Fatalf("SubagentCompactTarget: %v", err)
	}
	// The fork executor persists its answer, so the summary request is the
	// fork's last request plus that answer and the instruction. The answer
	// row is taken from the store before the compaction replaces it — the
	// re-read form the summarizer itself starts from — so the comparison is
	// byte for byte, round-trip included.
	persisted := fix.workerRows(t, workerID)
	if len(persisted) == 0 || persisted[len(persisted)-1].Role != llm.RoleAssistant ||
		persisted[len(persisted)-1].TextContent() != "fork continuation answer" {
		t.Fatalf("worker rows = %v, want the persisted continuation answer last", messageTexts(persisted))
	}
	if _, err := CompactionService(fix.fac.Owner, fix.store).ManualCompactSession(subCtx, worker, "manual"); err != nil {
		t.Fatalf("ManualCompactSession: %v", err)
	}

	all := fix.llm.gotRequests()
	summary := all[len(all)-1]
	if summary[0].Role != llm.RoleSystem || summary[0].TextContent() != "frozen fork system" {
		t.Fatalf("summary request opens with %v, want the frozen fork system", summary[0])
	}
	want := append([]llm.Message{llm.SystemMessage("frozen fork system")}, lastRun...)
	want = append(want,
		persisted[len(persisted)-1],
		llm.UserMessage(llm.Text("summarize the fork conversation\n\n"+compactSummaryInstruction)),
	)
	assertMessagesJSONEqual(t, "fork summary request", summary, want)
}

// assertMessagesJSONEqual compares two message sequences as the provider
// would see them: their JSON encoding, byte for byte.
func assertMessagesJSONEqual(t *testing.T, what string, got, want []llm.Message) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("%s differs from the expected request:\ngot:  %s\nwant: %s", what, gotJSON, wantJSON)
	}
}

// budgetEventProbe keeps the token_budget_updated events an owner published,
// whole, so a test can assert on the routing fields beside the payload.
type budgetEventProbe struct {
	mu     sync.Mutex
	events []event.RunEvent
}

func (p *budgetEventProbe) Publish(_ context.Context, evt event.RunEvent) error {
	if evt.Type != event.CompactEventBudgetUpdated {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, evt)
	return nil
}

func (p *budgetEventProbe) budgetEvents() []event.RunEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]event.RunEvent(nil), p.events...)
}

func (p *budgetEventProbe) budgets() []event.TokenBudgetUpdatedPayload {
	out := make([]event.TokenBudgetUpdatedPayload, 0, len(p.budgetEvents()))
	for _, evt := range p.budgetEvents() {
		var payload event.TokenBudgetUpdatedPayload
		if json.Unmarshal(evt.Payload, &payload) == nil {
			out = append(out, payload)
		}
	}
	return out
}

// snapshotReportingSubagentExecutor stands in for a child run's model: it
// reports one absolute usage snapshot the way a provider does mid-response.
type snapshotReportingSubagentExecutor struct {
	input, output int
}

func (e *snapshotReportingSubagentExecutor) RunSubagentExec(ctx context.Context, _ SubagentExecRequest) (string, error) {
	if sink := llm.StreamSinkFrom(ctx); sink != nil && sink.OnUsageSnapshot != nil {
		sink.OnUsageSnapshot(e.input, e.output)
	}
	return "done", nil
}

func (e *snapshotReportingSubagentExecutor) PersistSubagentTurn(context.Context, SubagentTurn) {}

// TestSubagentUsageSnapshotIsPublishedForItsOwnView pins the live half of a
// subagent's context gauge: the child's usage snapshot must become a
// token_budget_updated run event tagged with its roster key and sized by its
// own model's window — and must never reach the sink of the run that
// dispatched it, whose footer measures the parent session.
func TestSubagentUsageSnapshotIsPublishedForItsOwnView(t *testing.T) {
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
		t.Fatalf("CreateRun: %v", err)
	}
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "zhipuai", Model: "glm-5.3", APIKey: "key", BaseURL: "https://example.invalid"},
		}},
		"explore": {LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "deepseek", Model: "deepseek-v3.2", APIKey: "key", BaseURL: "https://example.invalid"},
		}},
	}}}
	probe := &budgetEventProbe{}
	owner := &Runner{
		Deps:             &Deps{RunRT: runSvc, AppCfg: cfg},
		SubagentExecutor: &snapshotReportingSubagentExecutor{input: 28000, output: 800},
		Control:          NewController(),
		Events:           probe,
	}

	// The dispatching run's own sink: its footer tracks the parent session.
	parentSnapshots := 0
	parentStreamed := false
	parentSink := &llm.StreamSink{
		Streamed: &parentStreamed,
		OnUsageSnapshot: func(int, int) {
			parentSnapshots++
		},
	}
	parentCtx := llm.WithStreamSink(toolpkg.WithRunID(llm.WithAgentSessionID(ctx, "session-1"), parent.ID), parentSink)

	if _, err := execGeneralSubagent(parentCtx, Factory{AgentName: "main", Owner: owner, Home: t.TempDir()}, "task-budget", "title", "do work", "explore"); err != nil {
		t.Fatal(err)
	}
	events := probe.budgetEvents()
	if len(events) != 1 {
		t.Fatalf("token_budget_updated events = %#v, want the child's one snapshot", events)
	}
	if runID := strings.TrimSpace(events[0].RunID); runID == "" || runID == parent.ID {
		t.Fatalf("budget event run = %q, want the child's own run", runID)
	}
	if sid := strings.TrimSpace(events[0].SessionID); sid != "session-1" {
		t.Fatalf("budget event session = %q, want the conversation session-1", sid)
	}
	got := probe.budgets()[0]
	if got.AgentID != "task-budget" {
		t.Fatalf("budget AgentID = %q, want the roster key task-budget", got.AgentID)
	}
	if got.Model != "deepseek-v3.2" || got.ContextWindow != 128000 || got.EffectiveWindow != 128000 {
		t.Fatalf("budget model/window = %s %d/%d, want the explore subagent's deepseek-v3.2 over 128000", got.Model, got.ContextWindow, got.EffectiveWindow)
	}
	if got.TokenUsage != 28800 || got.PercentLeft != 75 || got.AutoCompactThreshold != 115200 {
		t.Fatalf("budget numbers = %+v, want usage 28800 of 115200 at 75%%", got)
	}
	if parentSnapshots != 0 {
		t.Fatalf("the dispatching run's sink saw %d usage snapshots; the child's gauge must not move the parent's footer", parentSnapshots)
	}

	// The plan reviewer's view reads like any other subagent's: its gauge is
	// sized by the model the user picked for the review, never by the
	// conversation's, and the view-open entry answers the same window.
	reviewProbe := &budgetEventProbe{}
	reviewHome := t.TempDir()
	reviewer := &Runner{
		Deps:             &Deps{Home: reviewHome, AppCfg: cfg},
		Events:           reviewProbe,
		SubagentExecutor: &snapshotReportingSubagentExecutor{input: 29000, output: 491},
	}
	if _, err := reviewer.RunPlanReviewSubagent(toolpkg.WithConversationSessionID(dispatchTestCtx(), "session-1"), "review the plan", SubagentModelOverride{
		Provider: "zhipuai", Model: "glm-4.5",
	}, nil); err != nil {
		t.Fatalf("plan review: %v", err)
	}
	reviewEvents := reviewProbe.budgetEvents()
	if len(reviewEvents) != 1 {
		t.Fatalf("plan reviewer token_budget_updated events = %#v, want one", reviewEvents)
	}
	review := reviewProbe.budgets()[0]
	if review.ContextWindow != 131072 || review.Model != "glm-4.5" {
		t.Fatalf("plan reviewer budget = %+v, want the chosen glm-4.5 window 131072", review)
	}
	if review.AgentID == "" || strings.TrimSpace(reviewEvents[0].SessionID) == "" {
		t.Fatalf("plan reviewer budget must name its agent and its conversation: %#v", reviewEvents[0])
	}
	opened, ok := SubagentContextBudget(ctx, reviewer, strings.TrimSpace(reviewEvents[0].SessionID), review.AgentID)
	if !ok {
		t.Fatalf("SubagentContextBudget found nothing for the reviewer %q", review.AgentID)
	}
	if opened.ContextWindow != 131072 || opened.Model != "glm-4.5" || opened.PercentLeft != 100 || opened.TokenUsage != 0 {
		t.Fatalf("reviewer view-open budget = %+v, want the same 131072 window, fresh context", opened)
	}
}

// TestSubagentContextBudgetReadsItsWorkerSession pins the stored half of the
// gauge: what a subagent's view opens with is measured from its worker
// session, on its own model's window — and a worker session that has recorded
// no usage yet reads as a fresh context, not as nothing.
func TestSubagentContextBudgetReadsItsWorkerSession(t *testing.T) {
	fixture := newPersistenceFixture(t, "system")
	ctx := context.Background()
	store := fixture.store
	if err := store.Ensure(ctx, "conv-1", "conv-1"); err != nil {
		t.Fatal(err)
	}
	birth := state.SessionBirth{Source: state.SessionSourceSubagent, ParentSessionID: "conv-1"}
	if err := store.EnsureAt(ctx, "worker-1", "worker-1", birth); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureAt(ctx, "worker-2", "worker-2", birth); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO fb_runs(id, session_id, parent_run_id, input_text, status, owner, created_at, updated_at)
		VALUES('seed-run', 'conv-1', NULL, 'prior execution', 'done', '', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMessageSequenceForRun(ctx, "worker-1", "seed-run", []llm.Message{
		llm.AssistantMessage([]llm.ContentPart{llm.Text("answer")}),
	}, "deepseek-v3.2", `{"input_tokens":12000,"output_tokens":800}`); err != nil {
		t.Fatal(err)
	}
	fixture.fac.Owner.AppCfg = &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "zhipuai", Model: "glm-5.3", APIKey: "key", BaseURL: "https://example.invalid"},
		}},
		"explore": {LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "deepseek", Model: "deepseek-v3.2", APIKey: "key", BaseURL: "https://example.invalid"},
		}},
	}}}
	root := fixture.fac.subagentScopeRoot()
	if err := agent.AppendHistory(root, agent.HistoryEntry{
		TaskID: "task-9", SessionID: "conv-1", WorkerSessionID: "worker-1",
		AgentKind: "typed", AgentType: "explore", Status: agent.StatusOK,
	}); err != nil {
		t.Fatal(err)
	}
	if err := agent.AppendHistory(root, agent.HistoryEntry{
		TaskID: "task-10", SessionID: "conv-1", WorkerSessionID: "worker-2",
		AgentKind: "typed", AgentType: "explore", Status: agent.StatusRunning,
	}); err != nil {
		t.Fatal(err)
	}

	got, ok := SubagentContextBudget(ctx, fixture.fac.Owner, "conv-1", "task-9")
	if !ok {
		t.Fatal("SubagentContextBudget found nothing for task-9")
	}
	if got.AgentID != "task-9" || got.TokenUsage != 12800 || got.ContextWindow != 128000 || got.PercentLeft != 89 {
		t.Fatalf("budget = %+v, want task-9 at 12800 of the explore window 128000, 89%% left", got)
	}
	fresh, ok := SubagentContextBudget(ctx, fixture.fac.Owner, "conv-1", "task-10")
	if !ok || fresh.TokenUsage != 0 || fresh.PercentLeft != 100 || fresh.ContextWindow != 128000 {
		t.Fatalf("fresh worker budget = %+v, want the whole 128000 window at 100%%", fresh)
	}
	if _, ok := SubagentContextBudget(ctx, fixture.fac.Owner, "conv-1", "task-missing"); ok {
		t.Fatal("an unknown agent key must not resolve to a budget")
	}
}

// --- Plan 005: the subagent input channel ---

// channelEventProbe collects the run events a subagent's channel published.
type channelEventProbe struct {
	mu     sync.Mutex
	events []event.RunEvent
}

func (p *channelEventProbe) Publish(_ context.Context, evt event.RunEvent) error {
	p.mu.Lock()
	p.events = append(p.events, evt)
	p.mu.Unlock()
	return nil
}

func (p *channelEventProbe) count(typ string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, evt := range p.events {
		if evt.Type == typ {
			n++
		}
	}
	return n
}

func (p *channelEventProbe) inputDelivered() []event.SubagentInputDeliveredPayload {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []event.SubagentInputDeliveredPayload
	for _, evt := range p.events {
		if evt.Type != event.RunEventSubagentInputDelivered {
			continue
		}
		var payload event.SubagentInputDeliveredPayload
		if json.Unmarshal(evt.Payload, &payload) == nil {
			out = append(out, payload)
		}
	}
	return out
}

func (p *channelEventProbe) ofType(typ string) []event.RunEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []event.RunEvent
	for _, evt := range p.events {
		if evt.Type == typ {
			out = append(out, evt)
		}
	}
	return out
}

// channelTestExecutor is a SubagentExecutor double close enough to the
// composition root's executor for the channel tests: it writes the execution's
// user message as a real worker-session row and reports its id through the
// request, can answer or not, can hold until released — so a test can talk to a
// running execution — and records how many executions overlapped.
type channelTestExecutor struct {
	store     *state.SessionStore
	answer    string
	runErr    error
	hold      bool
	responded bool
	release   chan struct{}
	started   chan struct{}

	mu       sync.Mutex
	active   int
	max      int
	tasks    []string
	rows     []int64
	execCtx  context.Context
	answered chan struct{}
}

func (e *channelTestExecutor) RunSubagentExec(ctx context.Context, req SubagentExecRequest) (string, error) {
	e.mu.Lock()
	e.active++
	if e.active > e.max {
		e.max = e.active
	}
	e.tasks = append(e.tasks, req.Task)
	e.execCtx = ctx
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.active--
		e.mu.Unlock()
	}()
	if e.started != nil {
		select {
		case e.started <- struct{}{}:
		default:
		}
	}
	if e.store != nil {
		rowID, err := e.store.AppendStructuredMessageForRun(ctx, req.WorkerSessionID, req.SuperviseRunID, "user",
			req.Task, "", state.MessagePartsJSON(llm.UserMessage(llm.Text(req.Task)), req.Task), "", "", "", "", state.MessageExecTiming{})
		if err != nil {
			return "", err
		}
		if req.OnUserTurn != nil {
			req.OnUserTurn(rowID)
		}
		e.mu.Lock()
		e.rows = append(e.rows, rowID)
		e.mu.Unlock()
	}
	if e.responded {
		if sink := llm.StreamSinkFrom(ctx); sink != nil && sink.OnResponseStarted != nil {
			sink.OnResponseStarted()
		}
		if e.answered != nil {
			select {
			case e.answered <- struct{}{}:
			default:
			}
		}
	}
	if e.hold {
		select {
		case <-e.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if e.runErr != nil {
		return "", e.runErr
	}
	return e.answer, nil
}

func (e *channelTestExecutor) PersistSubagentTurn(context.Context, SubagentTurn) {}

func (e *channelTestExecutor) maxConcurrent() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.max
}

func (e *channelTestExecutor) rowIDs() []int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]int64(nil), e.rows...)
}

func (e *channelTestExecutor) contextSeen() context.Context {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.execCtx
}

// steerableSubagentExecutor drives the real tool-orchestration wrapper over the
// context it is handed, so a test can prove a steer queued on the subagent's
// channel reaches the model at a tool boundary.
type steerableSubagentExecutor struct {
	ctrl      *Controller
	st        *toolpkg.State
	readTool  *llm.Tool
	steerText string

	mu       sync.Mutex
	requests [][]llm.Message
}

func (e *steerableSubagentExecutor) RunSubagentExec(ctx context.Context, req SubagentExecRequest) (string, error) {
	if q := e.ctrl.SessionQueue(req.WorkerSessionID); q != nil {
		q.Steer(Input{Text: e.steerText})
	}
	inner := &steeringScriptLLM{}
	if _, err := wrapToolOrchestrationLLM(inner, e.st).Execute(ctx,
		[]llm.Message{llm.UserMessage(llm.Text(req.Task))}, []*llm.Tool{e.readTool}); err != nil {
		return "", err
	}
	e.mu.Lock()
	e.requests = append([][]llm.Message(nil), inner.messages...)
	e.mu.Unlock()
	return "done after steer", nil
}

func (e *steerableSubagentExecutor) PersistSubagentTurn(context.Context, SubagentTurn) {}

func waitSubagentBusy(t *testing.T, r *Runner, conversationSessionID, agentKey string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if SubagentRunning(r, conversationSessionID, agentKey) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("subagent %s never started running", agentKey)
}

func waitSubagentIdle(t *testing.T, r *Runner, conversationSessionID, agentKey string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !SubagentRunning(r, conversationSessionID, agentKey) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("subagent %s never went idle", agentKey)
}

func seedChannelRecord(t *testing.T, fac Factory, entry agent.HistoryEntry) {
	t.Helper()
	if err := agent.AppendHistory(fac.subagentScopeRoot(), entry); err != nil {
		t.Fatal(err)
	}
}

func countConversationMessages(t *testing.T, store *state.SessionStore, sessionID string) int {
	t.Helper()
	var n int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM fb_messages WHERE session_id=?`, sessionID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func waitProbeEvent(t *testing.T, p *channelEventProbe, typ string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p.count(typ) >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("event %s published %d times, want at least %d", typ, p.count(typ), want)
}

// waitSubagentRecordSettled waits until the subagent's record leaves running:
// the final status is written after the execution's channel has gone idle, so a
// test that only waits for idle can still observe the running record.
func waitSubagentRecordSettled(t *testing.T, fac Factory, conversationSessionID, agentKey string) agent.HistoryEntry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		record, ok, err := agent.GetMerged(fac.subagentScopeRoot(), agent.Query{SessionID: conversationSessionID, TaskID: agentKey})
		if err == nil && ok && record.Status != agent.StatusRunning {
			return record
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("subagent %s never settled", agentKey)
	return agent.HistoryEntry{}
}

// TestModelContinuationWaitsForTheUsersExecutionOfTheSameSubagent pins the
// channel's serialization: a model's subagent_continue never starts a second
// execution of a subagent while a user's message is driving one — it waits, so
// the two never write the worker session at once or interleave their rows.
func TestModelContinuationWaitsForTheUsersExecutionOfTheSameSubagent(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system", "model answer")
	owner := fix.fac.Owner
	owner.Control = NewController()
	conv := "conv-serial"
	worker := "main:conv-serial:worker:00000000-0000-0000-0000-0000000000aa"
	fix.seedWorker(t, conv, worker, []llm.Message{llm.UserMessage(llm.Text("seed"))})
	seedChannelRecord(t, fix.fac, agent.HistoryEntry{
		TaskID: "task-serial", RunID: "seed-run", ParentRunID: "parent-1", SessionID: conv, WorkerSessionID: worker,
		Task: "serialize", Status: agent.StatusOK, AgentType: "explore", AgentKind: "typed",
		RuntimeKind: "typed_subagent", Continuable: true, StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
	})
	release := make(chan struct{})
	ex := &channelTestExecutor{store: fix.store, hold: true, release: release, answer: "user answer"}
	owner.SubagentExecutor = ex

	delivery, err := SendToSubagent(context.Background(), owner, SubagentSurface{}, conv, "task-serial", Input{Text: "user"}, TurnInputModeSteer)
	if err != nil || delivery != SubagentDeliveryStarted {
		t.Fatalf("SendToSubagent = %q, %v; want started", delivery, err)
	}
	waitSubagentBusy(t, owner, conv, "task-serial")

	done := make(chan error, 1)
	go func() {
		_, cerr := continueSubagentExecution(context.Background(), fix.fac, &SubagentContinueInput{TaskID: "task-serial", Message: "model"})
		done <- cerr
	}()
	time.Sleep(200 * time.Millisecond)
	if got := ex.maxConcurrent(); got != 1 {
		t.Fatalf("model continuation overlapped the user's execution: concurrent=%d", got)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("continueSubagentExecution: %v", err)
	}
	waitSubagentIdle(t, owner, conv, "task-serial")
	if got := ex.maxConcurrent(); got != 1 {
		t.Fatalf("executions overlapped: concurrent=%d", got)
	}
}

// TestSubagentReceivesASteerAtItsNextToolBoundary pins the channel's steer
// path: a message queued for a running subagent reaches the model at that
// execution's next tool boundary, is published as subagent_input_delivered for
// its own view, and never touches the primary conversation's runtime.
func TestSubagentReceivesASteerAtItsNextToolBoundary(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system")
	owner := fix.fac.Owner
	owner.Control = NewController()
	probe := &channelEventProbe{}
	owner.Events = probe
	fix.ensureConversation(t, "conv-steer")

	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})
	type readInput struct {
		FilePath string `json:"file_path"`
	}
	readTool, err := llm.NewTool("read_file", "read", func(_ context.Context, in *readInput) (string, error) {
		return in.FilePath, nil
	})
	if err != nil {
		t.Fatalf("read_file tool: %v", err)
	}
	ex := &steerableSubagentExecutor{ctrl: owner.Control, st: st, readTool: readTool, steerText: "adjust approach"}
	owner.SubagentExecutor = ex

	primaryRt := NewTurnInputRuntime()
	primaryRt.Enqueue(TurnInputModeSteer, []llm.ContentPart{llm.Text("primary steer")})
	ctx := WithTurnInputRuntime(
		toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), "conv-steer"), "parent-1"),
		primaryRt,
	)
	if _, err := execGeneralSubagent(ctx, fix.fac, "task-steer", "short title", "go", "explore"); err != nil {
		t.Fatalf("execGeneralSubagent: %v", err)
	}
	if len(ex.requests) < 2 {
		t.Fatalf("model calls=%d want at least 2 (tool call then steered answer)", len(ex.requests))
	}
	second := ex.requests[1]
	last := second[len(second)-1]
	if last.Role != llm.RoleUser || last.TextContent() != "adjust approach" {
		t.Fatalf("second request tail = %q/%q, want the steer", last.Role, last.TextContent())
	}
	delivered := probe.inputDelivered()
	if len(delivered) != 1 || delivered[0].Text != "adjust approach" {
		t.Fatalf("subagent_input_delivered = %+v, want the steer once", delivered)
	}
	if delivered[0].AgentID != "task-steer" {
		t.Fatalf("delivered AgentID = %q, want the roster key", delivered[0].AgentID)
	}
	if !primaryRt.HasSteers() {
		t.Fatal("the primary agent's steer must stay queued; the subagent drained it")
	}
}

// TestUserMessageContinuesAFailedSubagent pins the whole point of the channel:
// a subagent that failed can be continued from a message the user sends it,
// and the new execution's first request is the failed execution's whole
// history plus that message.
func TestUserMessageContinuesAFailedSubagent(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system", "picked it up")
	owner := fix.fac.Owner
	owner.Control = NewController()
	probe := &channelEventProbe{}
	owner.Events = probe
	conv := "conv-failed"
	worker := "main:conv-failed:worker:00000000-0000-0000-0000-0000000000bb"
	fix.seedWorker(t, conv, worker, []llm.Message{
		llm.UserMessage(llm.Text("investigate")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("partial finding")}),
	})
	seedChannelRecord(t, fix.fac, agent.HistoryEntry{
		TaskID: "task-failed", RunID: "seed-run", ParentRunID: "parent-1", SessionID: conv, WorkerSessionID: worker,
		Task: "investigate", Status: agent.StatusFailed, Output: "partial finding", Error: "network",
		AgentType: "explore", AgentKind: "typed", RuntimeKind: "typed_subagent", Continuable: true,
		StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
	})

	delivery, err := SendToSubagent(context.Background(), owner, SubagentSurface{}, conv, "task-failed", Input{Text: "continue"}, TurnInputModeSteer)
	if err != nil || delivery != SubagentDeliveryStarted {
		t.Fatalf("SendToSubagent = %q, %v; want started", delivery, err)
	}
	waitProbeEvent(t, probe, event.RunEventSubagentEnded, 1)
	waitSubagentIdle(t, owner, conv, "task-failed")

	requests := fix.llm.gotRequests()
	if len(requests) == 0 {
		t.Fatal("the user's execution sent no request")
	}
	want := []string{
		llm.RoleSystem + "|worker system",
		llm.RoleUser + "|investigate",
		llm.RoleAssistant + "|partial finding",
		llm.RoleUser + "|continue",
	}
	if got := messageTexts(requests[0]); len(got) != len(want) {
		t.Fatalf("user execution request = %v, want the failed history plus continue", got)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("user execution request row %d = %q, want %q", i, got[i], want[i])
			}
		}
	}
	record := waitSubagentRecordSettled(t, fix.fac, conv, "task-failed")
	if record.Status != agent.StatusOK {
		t.Fatalf("record status = %q, want ok", record.Status)
	}
	if !strings.Contains(record.Output, "picked it up") {
		t.Fatalf("record output = %q, want the new conclusion", record.Output)
	}
}

// recordingSchedulerExecutor wraps the fixture's executor and records the
// start/end reports runSubagentExecution makes to whatever schedules a
// subagent's continuations.
type recordingSchedulerExecutor struct {
	*persistingSubagentExecutor
	mu     sync.Mutex
	starts []string
	ends   []SubagentExecutionEnd
}

func (e *recordingSchedulerExecutor) SubagentExecutionStarting(_ context.Context, workerSessionID string) {
	e.mu.Lock()
	e.starts = append(e.starts, workerSessionID)
	e.mu.Unlock()
}

func (e *recordingSchedulerExecutor) SubagentExecutionEnded(_ context.Context, end SubagentExecutionEnd) {
	e.mu.Lock()
	e.ends = append(e.ends, end)
	e.mu.Unlock()
}

func (e *recordingSchedulerExecutor) reports() ([]string, []SubagentExecutionEnd) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.starts...), append([]SubagentExecutionEnd(nil), e.ends...)
}

// TestEverySubagentExecutionIsReportedToItsScheduler pins that a subagent's
// continuation scheduler hears about all three ways one of its executions can
// begin and end — the model's dispatch, the model's continuation, and the
// user's own message — because that is what lets a usage limit stop one of them
// and be continued the same way the primary conversation's is.
func TestEverySubagentExecutionIsReportedToItsScheduler(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system", "first answer", "second answer", "third answer")
	owner := fix.fac.Owner
	owner.Control = NewController()
	owner.Events = &channelEventProbe{}
	rec := &recordingSchedulerExecutor{persistingSubagentExecutor: fix.executor}
	owner.SubagentExecutor = rec
	conv := "conv-report"
	fix.ensureConversation(t, conv)

	ctx := llm.WithAgentSessionID(context.Background(), conv)
	ctx = toolpkg.WithRunID(ctx, "parent-run-1")
	if _, err := execGeneralSubagent(ctx, fix.fac, "task-report", "title", "do work", "general-purpose"); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if _, err := continueSubagentExecution(context.Background(), fix.fac, &SubagentContinueInput{TaskID: "task-report", Message: "keep going"}); err != nil {
		t.Fatalf("model continuation: %v", err)
	}
	if _, err := SendToSubagent(context.Background(), owner, SubagentSurface{}, conv, "task-report", Input{Text: "and then"}, TurnInputModeSteer); err != nil {
		t.Fatalf("user message: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var starts []string
	var ends []SubagentExecutionEnd
	for time.Now().Before(deadline) {
		starts, ends = rec.reports()
		if len(starts) >= 3 && len(ends) >= 3 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(starts) != 3 || len(ends) != 3 {
		t.Fatalf("reports = %d starts, %d ends; want 3 each", len(starts), len(ends))
	}
	for i, end := range ends {
		if end.ConversationSessionID != conv {
			t.Fatalf("end %d conversation = %q, want %q", i, end.ConversationSessionID, conv)
		}
		if end.AgentKey != "task-report" {
			t.Fatalf("end %d agent key = %q, want task-report", i, end.AgentKey)
		}
		if end.WorkerSessionID == "" || end.WorkerSessionID != starts[i] {
			t.Fatalf("end %d worker = %q, want the worker the same execution started as %q", i, end.WorkerSessionID, starts[i])
		}
	}
}

// TestUserCanTalkToEveryKindOfSubagent pins decision D2: the user can drive any
// kind of subagent from its view — a one-shot type, a reserved internal type,
// or a fork — not only the ones the model may continue.
func TestUserCanTalkToEveryKindOfSubagent(t *testing.T) {
	cases := []struct {
		name      string
		agentType string
		agentKind string
		oneShot   bool
	}{
		{"one-shot explore", "explore", "typed", true},
		{"plan reviewer", "plan-reviewer", "typed", true},
		{"goal evaluator", "goal-evaluator", "typed", true},
		{"general purpose", "general-purpose", "typed", false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fix := newPersistenceFixture(t, "worker system", "answered")
			owner := fix.fac.Owner
			owner.Control = NewController()
			probe := &channelEventProbe{}
			owner.Events = probe
			owner.SubagentExecutor = &channelTestExecutor{store: fix.store, answer: "answered"}
			conv := "conv-kind-" + tc.name
			worker := fmt.Sprintf("main:%s:worker:00000000-0000-0000-0000-0000000001%02d", conv, i)
			fix.seedWorker(t, conv, worker, []llm.Message{llm.UserMessage(llm.Text("seed"))})
			seedChannelRecord(t, fix.fac, agent.HistoryEntry{
				TaskID: "task-" + tc.name, RunID: "seed-run", ParentRunID: "parent-1", SessionID: conv, WorkerSessionID: worker,
				Task: "work", Status: agent.StatusOK, Output: "old", AgentType: tc.agentType, AgentKind: tc.agentKind,
				RuntimeKind: "typed_subagent", OneShot: tc.oneShot, Continuable: !tc.oneShot,
				StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
			})
			delivery, err := SendToSubagent(context.Background(), owner, SubagentSurface{}, conv, "task-"+tc.name, Input{Text: "keep going"}, TurnInputModeSteer)
			if err != nil || delivery != SubagentDeliveryStarted {
				t.Fatalf("SendToSubagent = %q, %v; want started", delivery, err)
			}
			waitSubagentIdle(t, owner, conv, "task-"+tc.name)
			if record := waitSubagentRecordSettled(t, fix.fac, conv, "task-"+tc.name); record.Status != agent.StatusOK {
				t.Fatalf("record = %+v, want ok", record)
			}
		})
	}

	t.Run("fork", func(t *testing.T) {
		fix := newPersistenceFixture(t, "worker system", "fork answered")
		owner := fix.fac.Owner
		owner.Control = NewController()
		probe := &channelEventProbe{}
		owner.Events = probe
		conv := "conv-kind-fork"
		worker := "main:conv-kind-fork:worker:00000000-0000-0000-0000-0000000002ff"
		fix.seedWorker(t, conv, worker, []llm.Message{
			llm.UserMessage(llm.Text("fork seed")),
			llm.AssistantMessage([]llm.ContentPart{llm.Text("fork prior")}),
		})
		if _, _, err := fix.store.FreezeSessionPromptState(context.Background(), worker, forkSystemPromptKey, "frozen fork system"); err != nil {
			t.Fatalf("freeze fork system: %v", err)
		}
		seedChannelRecord(t, fix.fac, agent.HistoryEntry{
			TaskID: "task-fork", RunID: "seed-run", ParentRunID: "parent-1", SessionID: conv, WorkerSessionID: worker,
			Task: "fork work", Status: agent.StatusOK, Output: "fork prior", AgentKind: "fork",
			RuntimeKind: "fork_subagent", Continuable: true, StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
		})
		delivery, err := SendToSubagent(context.Background(), owner, SubagentSurface{}, conv, "task-fork", Input{Text: "keep forking"}, TurnInputModeSteer)
		if err != nil || delivery != SubagentDeliveryStarted {
			t.Fatalf("SendToSubagent = %q, %v; want started", delivery, err)
		}
		waitSubagentIdle(t, owner, conv, "task-fork")
		if record := waitSubagentRecordSettled(t, fix.fac, conv, "task-fork"); record.Status != agent.StatusOK {
			t.Fatalf("fork record = %+v, want ok", record)
		}
	})
}

// TestUserSubagentExecutionNeverTouchesThePrimaryConversation pins decision D3:
// a user-driven execution writes only the subagent's own worker session; the
// primary conversation gains no row, and the dispatching agent reads the result
// back through the registry.
func TestUserSubagentExecutionNeverTouchesThePrimaryConversation(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system")
	owner := fix.fac.Owner
	owner.Control = NewController()
	probe := &channelEventProbe{}
	owner.Events = probe
	owner.SubagentExecutor = &channelTestExecutor{store: fix.store, answer: "done"}
	conv := "conv-d3"
	worker := "main:conv-d3:worker:00000000-0000-0000-0000-0000000003aa"
	fix.seedWorker(t, conv, worker, []llm.Message{llm.UserMessage(llm.Text("seed"))})
	seedChannelRecord(t, fix.fac, agent.HistoryEntry{
		TaskID: "task-d3", RunID: "seed-run", ParentRunID: "parent-1", SessionID: conv, WorkerSessionID: worker,
		Task: "work", Status: agent.StatusOK, AgentType: "explore", AgentKind: "typed", RuntimeKind: "typed_subagent",
		Continuable: true, StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
	})
	before := countConversationMessages(t, fix.store, conv)
	delivery, err := SendToSubagent(context.Background(), owner, SubagentSurface{}, conv, "task-d3", Input{Text: "hello"}, TurnInputModeSteer)
	if err != nil || delivery != SubagentDeliveryStarted {
		t.Fatalf("SendToSubagent = %q, %v; want started", delivery, err)
	}
	waitProbeEvent(t, probe, event.RunEventSubagentEnded, 1)
	waitSubagentIdle(t, owner, conv, "task-d3")
	if after := countConversationMessages(t, fix.store, conv); after != before {
		t.Fatalf("primary conversation rows changed: before=%d after=%d", before, after)
	}
}

// TestInterruptSubagentToSendRunsThePendingSteersNext pins Esc's second meaning
// (D1): a running subagent with steers queued behind it is interrupted so those
// steers are sent next, and the surface is handed exactly those messages.
func TestInterruptSubagentToSendRunsThePendingSteersNext(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system")
	owner := fix.fac.Owner
	owner.Control = NewController()
	probe := &channelEventProbe{}
	owner.Events = probe
	release := make(chan struct{})
	var gotSend, gotRestore []Input
	var gotKey string
	surface := SubagentSurface{OnBoundary: func(key string, send, restore []Input) {
		gotKey, gotSend, gotRestore = key, send, restore
	}}
	owner.SubagentExecutor = &channelTestExecutor{store: fix.store, hold: true, release: release, answer: "x"}
	conv := "conv-interrupt"
	worker := "main:conv-interrupt:worker:00000000-0000-0000-0000-0000000004aa"
	fix.seedWorker(t, conv, worker, []llm.Message{llm.UserMessage(llm.Text("seed"))})
	seedChannelRecord(t, fix.fac, agent.HistoryEntry{
		TaskID: "task-int", RunID: "seed-run", ParentRunID: "parent-1", SessionID: conv, WorkerSessionID: worker,
		Task: "work", Status: agent.StatusOK, AgentType: "explore", AgentKind: "typed", RuntimeKind: "typed_subagent",
		Continuable: true, StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
	})
	delivery, err := SendToSubagent(context.Background(), owner, surface, conv, "task-int", Input{Text: "start"}, TurnInputModeSteer)
	if err != nil || delivery != SubagentDeliveryStarted {
		t.Fatalf("SendToSubagent = %q, %v; want started", delivery, err)
	}
	waitSubagentBusy(t, owner, conv, "task-int")
	for _, text := range []string{"one", "two"} {
		if got, err := SendToSubagent(context.Background(), owner, surface, conv, "task-int", Input{Text: text}, TurnInputModeSteer); err != nil || got != SubagentDeliverySteered {
			t.Fatalf("steer %q = %q, %v; want steered", text, got, err)
		}
	}
	if !InterruptSubagentToSend(owner, conv, "task-int") {
		t.Fatal("InterruptSubagentToSend = false, want true with steers pending")
	}
	waitProbeEvent(t, probe, event.RunEventSubagentEnded, 1)
	if gotKey != "task-int" {
		t.Fatalf("OnBoundary key = %q, want the roster key", gotKey)
	}
	if len(gotSend) != 2 || gotSend[0].Text != "one" || gotSend[1].Text != "two" {
		t.Fatalf("OnBoundary send = %+v, want [one two] in order", gotSend)
	}
	if len(gotRestore) != 0 {
		t.Fatalf("OnBoundary restore = %+v, want empty", gotRestore)
	}
}

// TestSubagentViewQueueQuestionsAnswerFromTheLiveChannel pins where a
// subagent view's per-repaint and per-keystroke questions are answered: from
// the channel this process holds, never from the subagent ledger. The view
// repaints its composer — and asks for the queue preview — after every input
// event, so a lookup that re-reads the whole ledger each time puts that cost on
// every wheel notch. The ledger is made unreadable mid-run here, so an answer
// that still goes through it comes back empty.
func TestSubagentViewQueueQuestionsAnswerFromTheLiveChannel(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system")
	owner := fix.fac.Owner
	owner.Control = NewController()
	owner.Events = &channelEventProbe{}
	release := make(chan struct{})
	owner.SubagentExecutor = &channelTestExecutor{store: fix.store, hold: true, release: release, answer: "x"}
	surface := SubagentSurface{OnBoundary: func(string, []Input, []Input) {}}
	conv := "conv-live-channel"
	worker := "main:conv-live-channel:worker:00000000-0000-0000-0000-0000000004bb"
	fix.seedWorker(t, conv, worker, []llm.Message{llm.UserMessage(llm.Text("seed"))})
	seedChannelRecord(t, fix.fac, agent.HistoryEntry{
		TaskID: "task-live", RunID: "seed-run", ParentRunID: "parent-1", SessionID: conv, WorkerSessionID: worker,
		Task: "work", Status: agent.StatusOK, AgentType: "explore", AgentKind: "typed", RuntimeKind: "typed_subagent",
		Continuable: true, StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
	})
	if got, err := SendToSubagent(context.Background(), owner, surface, conv, "task-live", Input{Text: "start"}, TurnInputModeSteer); err != nil || got != SubagentDeliveryStarted {
		t.Fatalf("SendToSubagent = %q, %v; want started", got, err)
	}
	waitSubagentBusy(t, owner, conv, "task-live")
	for _, text := range []string{"one", "two"} {
		if got, err := SendToSubagent(context.Background(), owner, surface, conv, "task-live", Input{Text: text}, TurnInputModeSteer); err != nil || got != SubagentDeliverySteered {
			t.Fatalf("steer %q = %q, %v; want steered", text, got, err)
		}
	}

	// From here on every ledger read fails: the file becomes a directory.
	root := fix.fac.subagentScopeRoot()
	ledger := filepath.Join(root, "state", "subagent-history.jsonl")
	saved, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ledger); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ledger, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := agent.GetMerged(root, agent.Query{SessionID: conv, TaskID: "task-live"}); err == nil {
		t.Fatal("the ledger is still readable, so this test would prove nothing")
	}

	if p := SubagentInputPreview(owner, conv, "task-live"); len(p.Steers) != 2 || p.Steers[0] != "one" || p.Steers[1] != "two" {
		t.Fatalf("SubagentInputPreview = %+v, want steers [one two] from the live channel", p)
	}
	if !SubagentRunning(owner, conv, "task-live") {
		t.Fatal("SubagentRunning = false, want true from the live channel")
	}
	if p := SubagentInputPreview(owner, "conv-other", "task-live"); p.Visible() {
		t.Fatalf("another conversation's preview = %+v, want nothing", p)
	}
	if p := SubagentInputPreview(owner, conv, "task-missing"); p.Visible() {
		t.Fatalf("unknown task's preview = %+v, want nothing", p)
	}
	if in, ok := RecallSubagentInput(owner, conv, "task-live"); !ok || in.Text != "two" {
		t.Fatalf("RecallSubagentInput = %+v, %v; want the newest steer", in, ok)
	}
	if n := DiscardSubagentInput(owner, conv, "task-live"); n != 1 {
		t.Fatalf("DiscardSubagentInput = %d, want 1", n)
	}

	// The execution's end appends to the ledger: give the file back first.
	if err := os.Remove(ledger); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledger, saved, 0o644); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitSubagentIdle(t, owner, conv, "task-live")
}

// A surface that does not read a subagent's queue on every paint — the web —
// must hear of every change to it, or its view keeps showing a message the
// model already has, or one the queue settled, as still queued and recallable.
func TestSubagentQueueChangesReachTheSurface(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system")
	owner := fix.fac.Owner
	owner.Control = NewController()
	owner.Events = &channelEventProbe{}
	release := make(chan struct{})
	owner.SubagentExecutor = &channelTestExecutor{store: fix.store, hold: true, release: release, answer: "x"}
	var mu sync.Mutex
	var previews []QueuePreview
	boundary := make(chan struct{}, 1)
	surface := SubagentSurface{
		OnBoundary: func(string, []Input, []Input) { boundary <- struct{}{} },
		OnQueueChanged: func(agentKey string, preview QueuePreview) {
			if agentKey != "task-hook" {
				t.Errorf("OnQueueChanged key = %q, want the roster key", agentKey)
			}
			mu.Lock()
			previews = append(previews, preview)
			mu.Unlock()
		},
	}
	last := func() QueuePreview {
		mu.Lock()
		defer mu.Unlock()
		if len(previews) == 0 {
			return QueuePreview{}
		}
		return previews[len(previews)-1]
	}
	conv := "conv-queue-hook"
	worker := "main:conv-queue-hook:worker:00000000-0000-0000-0000-0000000005bb"
	fix.seedWorker(t, conv, worker, []llm.Message{llm.UserMessage(llm.Text("seed"))})
	seedChannelRecord(t, fix.fac, agent.HistoryEntry{
		TaskID: "task-hook", RunID: "seed-run", ParentRunID: "parent-1", SessionID: conv, WorkerSessionID: worker,
		Task: "work", Status: agent.StatusOK, AgentType: "explore", AgentKind: "typed", RuntimeKind: "typed_subagent",
		Continuable: true, StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
	})
	send := func(text string, mode TurnInputMode, want SubagentDelivery) {
		t.Helper()
		if got, err := SendToSubagent(context.Background(), owner, surface, conv, "task-hook", Input{Text: text}, mode); err != nil || got != want {
			t.Fatalf("send %q = %q, %v; want %q", text, got, err, want)
		}
	}
	send("start", TurnInputModeSteer, SubagentDeliveryStarted)
	waitSubagentBusy(t, owner, conv, "task-hook")
	send("one", TurnInputModeSteer, SubagentDeliverySteered)
	send("two", TurnInputModeSteer, SubagentDeliverySteered)
	if got := strings.Join(last().Steers, ","); got != "one,two" {
		t.Fatalf("surface preview steers = %q, want one,two", got)
	}

	// The model takes both steers and begins answering: the view must drop them.
	delivery := owner.Control.SessionQueue(worker).Runtime().BeginSteerDelivery(context.Background())
	if delivery == nil || !delivery.Commit() {
		t.Fatal("expected the running execution to take the steers")
	}
	if p := last(); len(p.Steers) != 0 {
		t.Fatalf("after delivery the surface still shows %v as queued", p.Steers)
	}

	send("later", TurnInputModeFollowUp, SubagentDeliveryQueued)
	if got := strings.Join(last().FollowUp, ","); got != "later" {
		t.Fatalf("surface preview follow-ups = %q, want later", got)
	}
	if in, ok := RecallSubagentInput(owner, conv, "task-hook"); !ok || in.Text != "later" {
		t.Fatalf("RecallSubagentInput = %+v, %v; want later", in, ok)
	}
	if p := last(); p.Visible() {
		t.Fatalf("after recall the surface still shows %+v", p)
	}

	// A follow-up the execution's end settles leaves the view as well.
	send("after", TurnInputModeFollowUp, SubagentDeliveryQueued)
	close(release)
	select {
	case <-boundary:
	case <-time.After(5 * time.Second):
		t.Fatal("the execution never reached its boundary")
	}
	if p := last(); p.Visible() {
		t.Fatalf("after the execution ended the surface still shows %+v", p)
	}
	waitSubagentIdle(t, owner, conv, "task-hook")
}

// TestWithdrawBeforeTheSubagentAnswers pins Esc's first meaning (D1): a message
// the user just sent a subagent can be taken back before its model answers —
// the worker row is hidden and no result is written — but not after. The
// withdrawal still reports its end (as a cancellation): the spawn announced a
// lifecycle card and a roster row, and the surface has no other way to retire
// them.
func TestWithdrawBeforeTheSubagentAnswers(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system")
	owner := fix.fac.Owner
	owner.Control = NewController()
	probe := &channelEventProbe{}
	owner.Events = probe
	release := make(chan struct{})
	ex := &channelTestExecutor{store: fix.store, hold: true, release: release, answer: "x"}
	owner.SubagentExecutor = ex
	conv := "conv-withdraw"
	worker := "main:conv-withdraw:worker:00000000-0000-0000-0000-0000000005aa"
	fix.seedWorker(t, conv, worker, []llm.Message{llm.UserMessage(llm.Text("seed"))})
	seedChannelRecord(t, fix.fac, agent.HistoryEntry{
		TaskID: "task-wd", RunID: "seed-run", ParentRunID: "parent-1", SessionID: conv, WorkerSessionID: worker,
		Task: "work", Status: agent.StatusOK, AgentType: "explore", AgentKind: "typed", RuntimeKind: "typed_subagent",
		Continuable: true, StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
	})
	delivery, err := SendToSubagent(context.Background(), owner, SubagentSurface{}, conv, "task-wd", Input{Text: "hurry"}, TurnInputModeSteer)
	if err != nil || delivery != SubagentDeliveryStarted {
		t.Fatalf("SendToSubagent = %q, %v; want started", delivery, err)
	}
	waitSubagentBusy(t, owner, conv, "task-wd")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(ex.rowIDs()) == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	if len(ex.rowIDs()) == 0 {
		t.Fatal("the execution never wrote its user row")
	}
	withdrawn, ok := WithdrawSubagentInput(owner, conv, "task-wd")
	if !ok || len(withdrawn) != 1 || withdrawn[0].Text != "hurry" {
		t.Fatalf("WithdrawSubagentInput = %+v, %v; want the message back", withdrawn, ok)
	}
	waitSubagentIdle(t, owner, conv, "task-wd")
	time.Sleep(50 * time.Millisecond)
	var visibility string
	if err := fix.store.DB().QueryRow(`SELECT visibility FROM fb_messages WHERE id=?`, ex.rowIDs()[0]).Scan(&visibility); err != nil {
		t.Fatal(err)
	}
	if visibility != "withdrawn" {
		t.Fatalf("worker row visibility = %q, want withdrawn", visibility)
	}
	// The spawn opened a card and a roster row before the message was taken
	// back; the end event retires them, reported as a cancellation. The worker
	// row still carries no answer.
	ends := probe.ofType(event.RunEventSubagentEnded)
	if len(ends) != 1 {
		t.Fatalf("subagent_ended published %d times for a withdrawn execution, want 1 (a cancellation)", len(ends))
	}
	var withdrawnEnd event.SubagentEndedPayload
	if err := json.Unmarshal(ends[0].Payload, &withdrawnEnd); err != nil {
		t.Fatal(err)
	}
	if withdrawnEnd.Status != string(agent.StatusCancelled) {
		t.Fatalf("withdrawn end status = %q, want %q", withdrawnEnd.Status, agent.StatusCancelled)
	}

	// Past the answer boundary the window has closed.
	answered := make(chan struct{}, 1)
	release2 := make(chan struct{})
	ex2 := &channelTestExecutor{store: fix.store, hold: true, release: release2, responded: true, answer: "answered", answered: answered}
	owner.SubagentExecutor = ex2
	if _, err := SendToSubagent(context.Background(), owner, SubagentSurface{}, conv, "task-wd", Input{Text: "second"}, TurnInputModeSteer); err != nil {
		t.Fatalf("second SendToSubagent: %v", err)
	}
	waitSubagentBusy(t, owner, conv, "task-wd")
	<-answered
	if _, ok := WithdrawSubagentInput(owner, conv, "task-wd"); ok {
		t.Fatal("WithdrawSubagentInput succeeded after the model answered")
	}
	close(release2)
	waitProbeEvent(t, probe, event.RunEventSubagentEnded, 2)
	waitSubagentIdle(t, owner, conv, "task-wd")
}

// TestUserMessageReusesTheSubagentsPrefix pins the cache contract: the message
// the user sends a subagent appends to the worker session's own history, so the
// new execution's first request is the previous request plus the previous
// answer plus that message.
func TestUserMessageReusesTheSubagentsPrefix(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system", "follow up answer")
	owner := fix.fac.Owner
	owner.Control = NewController()
	probe := &channelEventProbe{}
	owner.Events = probe
	conv := "conv-prefix"
	worker := "main:conv-prefix:worker:00000000-0000-0000-0000-0000000006aa"
	fix.seedWorker(t, conv, worker, []llm.Message{
		llm.UserMessage(llm.Text("q1")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("a1")}),
	})
	seedChannelRecord(t, fix.fac, agent.HistoryEntry{
		TaskID: "task-prefix", RunID: "seed-run", ParentRunID: "parent-1", SessionID: conv, WorkerSessionID: worker,
		Task: "q1", Status: agent.StatusOK, Output: "a1", AgentType: "explore", AgentKind: "typed",
		RuntimeKind: "typed_subagent", Continuable: true, StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
	})
	delivery, err := SendToSubagent(context.Background(), owner, SubagentSurface{}, conv, "task-prefix", Input{Text: "follow up"}, TurnInputModeSteer)
	if err != nil || delivery != SubagentDeliveryStarted {
		t.Fatalf("SendToSubagent = %q, %v; want started", delivery, err)
	}
	waitProbeEvent(t, probe, event.RunEventSubagentEnded, 1)
	waitSubagentIdle(t, owner, conv, "task-prefix")
	requests := fix.llm.gotRequests()
	if len(requests) == 0 {
		t.Fatal("the user's execution sent no request")
	}
	want := []string{
		llm.RoleSystem + "|worker system",
		llm.RoleUser + "|q1",
		llm.RoleAssistant + "|a1",
		llm.RoleUser + "|follow up",
	}
	if got := messageTexts(requests[0]); len(got) != len(want) {
		t.Fatalf("user execution request = %v, want the previous request plus the message", got)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("user execution request row %d = %q, want %q", i, got[i], want[i])
			}
		}
	}
}

// TestSubagentOfAnotherConversationIsNotFound pins the channel's tenancy: an
// agent key is resolved inside one conversation, so another conversation's
// subagent is not found.
func TestSubagentOfAnotherConversationIsNotFound(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system")
	owner := fix.fac.Owner
	owner.Control = NewController()
	conv := "conv-tenant"
	worker := "main:conv-tenant:worker:00000000-0000-0000-0000-0000000007aa"
	fix.seedWorker(t, conv, worker, []llm.Message{llm.UserMessage(llm.Text("seed"))})
	seedChannelRecord(t, fix.fac, agent.HistoryEntry{
		TaskID: "task-tenant", RunID: "seed-run", ParentRunID: "parent-1", SessionID: conv, WorkerSessionID: worker,
		Task: "work", Status: agent.StatusOK, AgentType: "explore", AgentKind: "typed", RuntimeKind: "typed_subagent",
		StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
	})
	if _, err := SendToSubagent(context.Background(), owner, SubagentSurface{}, "conv-other", "task-tenant", Input{Text: "hi"}, TurnInputModeSteer); !errors.Is(err, ErrSubagentNotFound) {
		t.Fatalf("SendToSubagent err = %v, want ErrSubagentNotFound", err)
	}
}

// A skill command the user types in a subagent's own view rides on the message
// as a trusted explicit skill selection, so the execution the message starts
// activates that skill — the subagent's counterpart of a skill turn in the
// conversation (plan 007 §1).
func TestUserSkillCommandActivatesTheSkillInTheSubagent(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system", "done")
	owner := fix.fac.Owner
	owner.Control = NewController()
	conv := "conv-skill"
	worker := "main:conv-skill:worker:00000000-0000-0000-0000-0000000000bb"
	fix.seedWorker(t, conv, worker, []llm.Message{llm.UserMessage(llm.Text("seed"))})
	seedChannelRecord(t, fix.fac, agent.HistoryEntry{
		TaskID: "task-skill", RunID: "seed-run", SessionID: conv, WorkerSessionID: worker,
		Task: "activate", Status: agent.StatusOK, AgentType: "explore", AgentKind: "typed",
		RuntimeKind: "typed_subagent", Continuable: true, StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
	})
	ex := &channelTestExecutor{store: fix.store, answer: "done", started: make(chan struct{}, 1)}
	owner.SubagentExecutor = ex

	in := Input{Text: "/my-skill", SkillName: "my-skill", SkillPath: "/skills/my-skill/SKILL.md"}
	if _, err := SendToSubagent(context.Background(), owner, SubagentSurface{}, conv, "task-skill", in, TurnInputModeSteer); err != nil {
		t.Fatalf("SendToSubagent: %v", err)
	}
	select {
	case <-ex.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the subagent execution never started")
	}
	waitSubagentIdle(t, owner, conv, "task-skill")

	selection, ok := explicitSkillSelectionFromContext(ex.contextSeen())
	if !ok {
		t.Fatal("the execution context carried no explicit skill selection")
	}
	if selection.SkillName != "my-skill" || selection.SkillPath != "/skills/my-skill/SKILL.md" {
		t.Fatalf("skill selection = %#v, want my-skill at /skills/my-skill/SKILL.md", selection)
	}
}

// A user-driven execution's ended event must name the same dispatch its spawn
// did. A user-driven execution has no dispatching tool call, so both carry an
// empty ParentToolCallID and TaskIndex 0; reusing the original record's would
// bind the end to the subagent_run/send call that first dispatched this
// subagent, settling that (already finished) card instead and leaving this
// execution's own card stuck "Running" in the conversation (plan 007 §3,
// found on the real device).
func TestUserDrivenExecutionEndsWithTheSpawnIdentity(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system", "done")
	owner := fix.fac.Owner
	owner.Control = NewController()
	probe := &channelEventProbe{}
	owner.Events = probe
	conv := "conv-identity"
	worker := "main:conv-identity:worker:00000000-0000-0000-0000-0000000000cc"
	fix.seedWorker(t, conv, worker, []llm.Message{llm.UserMessage(llm.Text("seed"))})
	seedChannelRecord(t, fix.fac, agent.HistoryEntry{
		TaskID: "task-id", RunID: "seed-run", ParentRunID: "parent-1", SessionID: conv, WorkerSessionID: worker,
		Task: "original", Status: agent.StatusOK, AgentType: "explore", AgentKind: "typed",
		RuntimeKind: "typed_subagent", Continuable: true, StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
		ParentToolCallID: "call_original", TaskIndex: 0,
	})
	ex := &channelTestExecutor{store: fix.store, answer: "done"}
	owner.SubagentExecutor = ex

	if _, err := SendToSubagent(context.Background(), owner, SubagentSurface{}, conv, "task-id", Input{Text: "go"}, TurnInputModeSteer); err != nil {
		t.Fatalf("SendToSubagent: %v", err)
	}
	waitSubagentIdle(t, owner, conv, "task-id")
	waitProbeEvent(t, probe, event.RunEventSubagentSpawned, 1)
	waitProbeEvent(t, probe, event.RunEventSubagentEnded, 1)

	spawns := probe.ofType(event.RunEventSubagentSpawned)
	ends := probe.ofType(event.RunEventSubagentEnded)
	if len(spawns) != 1 || len(ends) != 1 {
		t.Fatalf("spawned=%d ended=%d, want one each", len(spawns), len(ends))
	}
	var spawned event.SubagentSpawnedPayload
	var ended event.SubagentEndedPayload
	if err := json.Unmarshal(spawns[0].Payload, &spawned); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(ends[0].Payload, &ended); err != nil {
		t.Fatal(err)
	}
	if spawned.ParentToolCallID != "" {
		t.Fatalf("the spawn named a dispatching call %q, want none for a user-driven execution", spawned.ParentToolCallID)
	}
	if ended.ParentToolCallID != spawned.ParentToolCallID || ended.TaskIndex != spawned.TaskIndex {
		t.Fatalf("ended identity = (%q,%d), want the spawn's (%q,%d)", ended.ParentToolCallID, ended.TaskIndex, spawned.ParentToolCallID, spawned.TaskIndex)
	}
}

// A withdrawn user-driven execution still reports its end, as a cancellation.
// The spawn announced a lifecycle card and a roster row; without an end event
// the surface would show that subagent running forever after Esc took the
// message back (plan 007 §4, found on the real device).
func TestWithdrawnUserExecutionPublishesItsEndSoTheSurfaceRetiresIt(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system", "done")
	owner := fix.fac.Owner
	owner.Control = NewController()
	probe := &channelEventProbe{}
	owner.Events = probe
	conv := "conv-withdraw"
	worker := "main:conv-withdraw:worker:00000000-0000-0000-0000-0000000000dd"
	fix.seedWorker(t, conv, worker, []llm.Message{llm.UserMessage(llm.Text("seed"))})
	seedChannelRecord(t, fix.fac, agent.HistoryEntry{
		TaskID: "task-wd", RunID: "seed-run", SessionID: conv, WorkerSessionID: worker,
		Task: "orig", Status: agent.StatusOK, AgentType: "explore", AgentKind: "typed",
		RuntimeKind: "typed_subagent", Continuable: true, StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
		ParentToolCallID: "call_original", TaskIndex: 0,
	})
	release := make(chan struct{})
	ex := &channelTestExecutor{store: fix.store, hold: true, release: release, answer: "x"}
	owner.SubagentExecutor = ex

	if _, err := SendToSubagent(context.Background(), owner, SubagentSurface{}, conv, "task-wd", Input{Text: "hurry"}, TurnInputModeSteer); err != nil {
		t.Fatalf("SendToSubagent: %v", err)
	}
	waitSubagentBusy(t, owner, conv, "task-wd")
	if _, ok := WithdrawSubagentInput(owner, conv, "task-wd"); !ok {
		t.Fatal("withdraw should have taken the message back")
	}
	close(release)
	waitSubagentIdle(t, owner, conv, "task-wd")
	waitProbeEvent(t, probe, event.RunEventSubagentEnded, 1)

	ends := probe.ofType(event.RunEventSubagentEnded)
	var ended event.SubagentEndedPayload
	if err := json.Unmarshal(ends[len(ends)-1].Payload, &ended); err != nil {
		t.Fatal(err)
	}
	if ended.Status != string(agent.StatusCancelled) {
		t.Fatalf("ended status = %q, want %q", ended.Status, agent.StatusCancelled)
	}
	if ended.ParentToolCallID != "" {
		t.Fatalf("withdrawn end named a dispatching call %q, want none", ended.ParentToolCallID)
	}
}
