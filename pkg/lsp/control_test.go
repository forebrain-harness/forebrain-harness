package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

func TestSnapshotBasics(t *testing.T) {
	enabled := &appcfg.Root{Features: appcfg.FeaturesSection{LSP: boolPtr(true)}}
	disabled := &appcfg.Root{Features: appcfg.FeaturesSection{LSP: boolPtr(false)}}
	opts := ManagerOptions{ProjectRoot: "/p/proj", Trusted: true, ToolRegistered: true, AgentWorkspace: t.TempDir()}

	on := NewPool(enabled).NewManager(opts)
	snap := on.Snapshot()
	if snap.ProjectRoot != "/p/proj" || !snap.Trusted || !snap.ToolRegistered {
		t.Fatalf("snapshot = %+v, want the options reflected", snap)
	}
	if !snap.FeatureEnabled {
		t.Fatal("FeatureEnabled must follow features.lsp")
	}
	// The catalog's servers are all listed, disabled, ordered by id.
	entries, err := Catalog()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if len(snap.Servers) != len(entries) {
		t.Fatalf("Servers lists %d of %d catalog entries", len(snap.Servers), len(entries))
	}
	for i, s := range snap.Servers {
		if s.Enabled {
			t.Fatalf("server %s is enabled by default", s.ID)
		}
		if i > 0 && snap.Servers[i-1].ID > s.ID {
			t.Fatalf("servers are not sorted by id: %s after %s", s.ID, snap.Servers[i-1].ID)
		}
	}

	off := NewPool(disabled).NewManager(opts)
	if off.Snapshot().FeatureEnabled {
		t.Fatal("FeatureEnabled must follow features.lsp")
	}
	waitForBackgroundWriters(t, on)
	waitForBackgroundWriters(t, off)
}

func TestSnapshotShowsRunningInstance(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: diagScript(map[string]any{"bad": []any{diagRule("bad thing", 0)}}),
	})
	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "contains bad\n")
	if delta := env.manager.DidWrite(context.Background(), "s1", []tool.FileChange{{AbsPath: file, After: []byte("contains bad\n")}}); delta.Text == "" {
		t.Fatal("DidWrite reported nothing; the server did not run")
	}
	waitForPoll(t, "the snapshot to show a ready instance", func() bool {
		for _, s := range env.manager.Snapshot().Servers {
			if s.ID == "fake" {
				return s.State == event.LSPStateReady
			}
		}
		return false
	})
	var status event.LSPServerStatus
	for _, s := range env.manager.Snapshot().Servers {
		if s.ID == "fake" {
			status = s
		}
	}
	if !status.Enabled || status.State != event.LSPStateReady {
		t.Fatalf("fake status = %+v", status)
	}
	resolvedProject, err := filepath.EvalSymlinks(env.project)
	if err != nil {
		resolvedProject = env.project
	}
	if len(status.Roots) != 1 || status.Roots[0] != resolvedProject {
		t.Errorf("roots = %v, want [%s]", status.Roots, resolvedProject)
	}
	if len(status.PIDs) != 1 || status.PIDs[0] == 0 {
		t.Errorf("pids = %v", status.PIDs)
	}
	if status.OpenDocuments != 1 {
		t.Errorf("open documents = %d, want 1", status.OpenDocuments)
	}
	if status.LogPath == "" {
		t.Error("the instance log path is missing")
	}
	if status.Errors != 1 {
		t.Errorf("errors = %d, want the one published problem", status.Errors)
	}
	waitForBackgroundWriters(t, env.manager)
}

func TestSetEnabledPersistsAndStopsInstance(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: diagScript(map[string]any{"bad": []any{diagRule("bad thing", 0)}}),
	})
	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "contains bad\n")
	if delta := env.manager.DidWrite(context.Background(), "s1", []tool.FileChange{{AbsPath: file, After: []byte("contains bad\n")}}); delta.Text == "" {
		t.Fatal("DidWrite reported nothing; the server did not run")
	}
	pid := pidOf(t, env.records["fake"])

	if err := env.manager.SetEnabled("nope", true); err == nil || err.Error() != `unknown language server "nope"` {
		t.Fatalf("unknown id error = %v", err)
	}
	if err := env.manager.SetEnabled("fake", false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	waitForProcessGone(t, pid)

	// The switch persisted and the cache dropped: nothing starts anymore.
	state, err := os.ReadFile(filepath.Join(StateDir(env.manager.opts.AgentWorkspace), "enabled.json"))
	if err != nil {
		t.Fatalf("read enabled.json: %v", err)
	}
	var saved struct {
		Enabled map[string]bool `json:"enabled"`
	}
	if err := json.Unmarshal(state, &saved); err != nil {
		t.Fatalf("enabled.json: %v", err)
	}
	if saved.Enabled["fake"] {
		t.Fatalf("enabled.json = %s, want fake disabled", state)
	}
	if delta := env.manager.DidWrite(context.Background(), "s1", []tool.FileChange{{AbsPath: file, After: []byte("contains bad\n")}}); !delta.Empty() {
		t.Fatalf("a disabled server still reported:\n%s", delta.Text)
	}

	if err := env.manager.SetEnabled("fake", true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	if delta := env.manager.DidWrite(context.Background(), "s1", []tool.FileChange{{AbsPath: file, After: []byte("contains bad\n")}}); delta.Text == "" {
		t.Fatal("after re-enabling, DidWrite reported nothing")
	}
	waitForBackgroundWriters(t, env.manager)
}

func TestRestartRestartsInstance(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: diagScript(map[string]any{"bad": []any{diagRule("bad thing", 0)}}),
	})
	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "contains bad\n")
	if delta := env.manager.DidWrite(context.Background(), "s1", []tool.FileChange{{AbsPath: file, After: []byte("contains bad\n")}}); delta.Text == "" {
		t.Fatal("DidWrite reported nothing; the server did not run")
	}
	if err := env.manager.Restart("nope"); err == nil || err.Error() != `unknown language server "nope"` {
		t.Fatalf("unknown id error = %v", err)
	}
	if err := env.manager.Restart("fake"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	waitForPoll(t, "a second initialize after Restart", func() bool {
		return countRecv(readRecord(t, env.records["fake"]), "initialize") == 2
	})
	waitForBackgroundWriters(t, env.manager)
}

// overrideRecCatalog replaces the catalog with the one entry the
// recommendation tests edit into: fake, primary, keyed to .fk, a command
// that is on PATH only when a test puts it there. The override bypasses
// loadCatalog, so the primary role is spelled out.
func overrideRecCatalog(t *testing.T, install []InstallRecipe) {
	t.Helper()
	orig := catalogOverride
	catalogOverride = []CatalogEntry{{
		ID:                  "fake",
		DisplayName:         "fake",
		Languages:           []string{"Fake"},
		Role:                appcfg.LSPRolePrimary,
		ExtensionToLanguage: map[string]string{".fk": "fake"},
		Command:             "fakels-rec",
		Install:             install,
	}}
	t.Cleanup(func() { catalogOverride = orig })
}

// putRecBinaryOnPath makes the fake server's command look installed: a
// script on PATH that answers its version probe successfully.
func putRecBinaryOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	writeExecutableScript(t, dir, "fakels-rec", "exit 0")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// recListener records everything the recommendation listener received,
// including the run and conversation identities the context carried — the
// values the RunEvent publisher forwards.
type recListener struct {
	mu         sync.Mutex
	recs       []event.LSPRecommendation
	runIDs     []string
	sessionIDs []string
}

func (l *recListener) on(ctx context.Context, rec event.LSPRecommendation) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recs = append(l.recs, rec)
	l.runIDs = append(l.runIDs, tool.RunIDFromContext(ctx))
	l.sessionIDs = append(l.sessionIDs, tool.ConversationSessionIDFromContext(ctx))
}

// identities returns the run and conversation ids the listener's contexts
// carried, in publish order.
func (l *recListener) identities() (runIDs, sessionIDs []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.runIDs...), append([]string(nil), l.sessionIDs...)
}

func (l *recListener) all() []event.LSPRecommendation {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]event.LSPRecommendation(nil), l.recs...)
}

// newRecManager builds a trusted manager over the overridden catalog with
// the listener attached.
func newRecManager(t *testing.T, lsp appcfg.LSPSection, install []InstallRecipe) (*Manager, *recListener, string) {
	t.Helper()
	overrideRecCatalog(t, install)
	p := NewPool(&appcfg.Root{LSP: lsp})
	t.Cleanup(func() { _ = p.Close() })
	project := t.TempDir()
	m := p.NewManager(ManagerOptions{
		Home:           t.TempDir(),
		AgentWorkspace: t.TempDir(),
		ProjectRoot:    project,
		Trusted:        true,
	})
	l := &recListener{}
	m.SetRecommendationListener(l.on)
	return m, l, project
}

// recEdit writes one .fk file and reports it as an edit of session sid.
func recEdit(t *testing.T, m *Manager, project, name, sid string, wrap ...func(context.Context) context.Context) {
	t.Helper()
	file := filepath.Join(project, name)
	mustWrite(t, file, "content\n")
	ctx := tool.WithConversationSessionID(context.Background(), sid)
	for _, w := range wrap {
		ctx = w(ctx)
	}
	_ = m.DidWrite(ctx, sid, []tool.FileChange{{AbsPath: file, After: []byte("content\n")}})
}

// plantRec puts one answered-pending recommendation into the manager, the
// state considerRecommendation leaves behind.
func plantRec(m *Manager, id, project string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingRecs == nil {
		m.pendingRecs = map[string]pendingRec{}
	}
	m.pendingRecs[id] = pendingRec{
		rec: event.LSPRecommendation{
			ID: id, ServerID: "fake", DisplayName: "fake",
			Languages: []string{"Fake"}, Mode: "enable",
		},
		sessionID:   "s1",
		triggerFile: filepath.Join(project, "a.fk"),
	}
}

// waitRecDetected blocks until the fake server's probe has answered and is
// cached — the state a later edit's recommendation is built from.
func waitRecDetected(t *testing.T, m *Manager) {
	t.Helper()
	waitForPoll(t, "the fake server's probe to land", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, ok := m.detected["fake"]
		return ok
	})
}

// One recommendation per conversation session: the first edit that finds a
// candidate publishes, later edits of the same session do not, and another
// session gets its own.
func TestRecommendOncePerSession(t *testing.T) {
	m, l, project := newRecManager(t, appcfg.LSPSection{}, nil)
	putRecBinaryOnPath(t)

	recEdit(t, m, project, "a.fk", "s1")
	waitRecDetected(t, m) // the first edit only probes
	if got := l.all(); len(got) != 0 {
		t.Fatalf("recommendations before detection = %d, want 0", len(got))
	}
	recEdit(t, m, project, "b.fk", "s1")
	waitForPoll(t, "the session's one recommendation", func() bool { return len(l.all()) == 1 })

	// A second edit of the same session stays quiet, and a new session is
	// recommended for on its own first detected edit.
	recEdit(t, m, project, "c.fk", "s1")
	recEdit(t, m, project, "d.fk", "s2")
	waitForPoll(t, "the second session's recommendation", func() bool { return len(l.all()) == 2 })
	time.Sleep(150 * time.Millisecond) // let any wrongful third publish land
	if got := l.all(); len(got) != 2 {
		t.Fatalf("recommendations = %d, want exactly 2", len(got))
	}
	second := l.all()[1]
	if second.TriggerExtension != ".fk" || second.ServerID != "fake" || second.DisplayName != "fake" {
		t.Fatalf("second recommendation = %+v", second)
	}
	waitForBackgroundWriters(t, m)
}

// The mode follows detection: installed means enable, a usable recipe means
// install, and neither means no recommendation at all.
func TestRecommendModes(t *testing.T) {
	t.Run("enable", func(t *testing.T) {
		m, l, project := newRecManager(t, appcfg.LSPSection{}, nil)
		putRecBinaryOnPath(t)
		recEdit(t, m, project, "a.fk", "s1")
		waitRecDetected(t, m)
		recEdit(t, m, project, "a.fk", "s1")
		waitForPoll(t, "the enable-mode recommendation", func() bool { return len(l.all()) == 1 })
		rec := l.all()[0]
		if rec.Mode != "enable" {
			t.Fatalf("mode = %q, want enable", rec.Mode)
		}
		if rec.BinaryPath == "" || rec.InstallCommand != "" {
			t.Fatalf("recommendation = %+v, want the binary path only", rec)
		}
		waitForBackgroundWriters(t, m)
	})
	t.Run("install", func(t *testing.T) {
		recipe := []InstallRecipe{{Requires: "fake-installer", Argv: []string{"fake-installer", "fake"}}}
		m, l, project := newRecManager(t, appcfg.LSPSection{}, recipe)
		dir := t.TempDir()
		writeExecutableScript(t, dir, "fake-installer", "exit 0")
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		recEdit(t, m, project, "a.fk", "s1")
		waitRecDetected(t, m)
		recEdit(t, m, project, "a.fk", "s1")
		waitForPoll(t, "the install-mode recommendation", func() bool { return len(l.all()) == 1 })
		rec := l.all()[0]
		if rec.Mode != "install" {
			t.Fatalf("mode = %q, want install", rec.Mode)
		}
		if rec.InstallCommand != "fake-installer fake" || rec.BinaryPath != "" {
			t.Fatalf("recommendation = %+v, want the install command only", rec)
		}
		waitForBackgroundWriters(t, m)
	})
	t.Run("neither", func(t *testing.T) {
		m, l, project := newRecManager(t, appcfg.LSPSection{}, nil)
		recEdit(t, m, project, "a.fk", "s1")
		waitRecDetected(t, m)
		recEdit(t, m, project, "a.fk", "s1")
		time.Sleep(150 * time.Millisecond)
		if got := l.all(); len(got) != 0 {
			t.Fatalf("recommendations without binary or recipe = %v, want none", got)
		}
		waitForBackgroundWriters(t, m)
	})
}

// An edit before detection completed recommends nothing; the next one, with
// the probe answered, does (spec §10.1).
func TestRecommendWaitsForDetection(t *testing.T) {
	m, l, project := newRecManager(t, appcfg.LSPSection{}, nil)
	putRecBinaryOnPath(t)
	recEdit(t, m, project, "a.fk", "s1")
	time.Sleep(150 * time.Millisecond) // the probe is still running
	if got := l.all(); len(got) != 0 {
		t.Fatalf("recommendations while probing = %d, want 0", len(got))
	}
	waitRecDetected(t, m)
	recEdit(t, m, project, "a.fk", "s1")
	waitForPoll(t, "the recommendation after the probe", func() bool { return len(l.all()) == 1 })
	waitForBackgroundWriters(t, m)
}

// Every gate of spec §10.1 stays shut: an untrusted project, the config
// switch, a disabled state file, a "never" entry, and a server that is
// already enabled. Fork children and typed subagents are deliberately not
// gates anymore — their edits trigger like the main agent's (owner ruling;
// see TestRecommendTriggersForSubagentEdits).
func TestRecommendSkips(t *testing.T) {
	cases := map[string]func(t *testing.T, m *Manager, project string){
		"untrusted": func(t *testing.T, m *Manager, project string) {
			m.opts.Trusted = false
			recEdit(t, m, project, "a.fk", "s1")
		},
		"recommendations off": func(t *testing.T, m *Manager, project string) {
			m.pool.cfg.Store(&appcfg.Root{LSP: appcfg.LSPSection{Recommendations: boolPtr(false)}})
			recEdit(t, m, project, "a.fk", "s1")
		},
		"disabled": func(t *testing.T, m *Manager, project string) {
			if err := saveRecState(m.opts.AgentWorkspace, recState{Disabled: true, DisabledReason: "turned off by the user"}); err != nil {
				t.Fatal(err)
			}
			recEdit(t, m, project, "a.fk", "s1")
		},
		"never": func(t *testing.T, m *Manager, project string) {
			if err := saveRecState(m.opts.AgentWorkspace, recState{Never: []string{"fake"}}); err != nil {
				t.Fatal(err)
			}
			recEdit(t, m, project, "a.fk", "s1")
		},
		"already enabled": func(t *testing.T, m *Manager, project string) {
			if err := SaveEnabled(m.opts.AgentWorkspace, "fake", true); err != nil {
				t.Fatal(err)
			}
			recEdit(t, m, project, "a.fk", "s1")
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			m, l, project := newRecManager(t, appcfg.LSPSection{}, nil)
			putRecBinaryOnPath(t)
			run(t, m, project) // first gated edit: probes, recommends nothing yet
			waitForBackgroundWriters(t, m)
			run(t, m, project) // and again with the gate shut and detection cached
			time.Sleep(150 * time.Millisecond)
			if got := l.all(); len(got) != 0 {
				t.Fatalf("recommendations = %v, want none", got)
			}
			waitForBackgroundWriters(t, m)
		})
	}
}

// Subagent and fork edits trigger the recommendation on the conversation
// session they belong to (owner ruling: subagent writes must trigger the LSP
// recommendation too). The listener's context keeps the child run id and the
// parent conversation session — the identity the RunEvent publisher forwards
// — and one conversation gets one recommendation however many agents edit
// in it.
func TestRecommendTriggersForSubagentEdits(t *testing.T) {
	m, l, project := newRecManager(t, appcfg.LSPSection{}, nil)
	putRecBinaryOnPath(t)
	sub := func(runID string) func(context.Context) context.Context {
		return func(ctx context.Context) context.Context {
			return tool.WithSubagentType(tool.WithRunID(ctx, runID), "general-purpose")
		}
	}
	fork := func(runID string) func(context.Context) context.Context {
		return func(ctx context.Context) context.Context {
			return tool.WithForkChild(tool.WithRunID(ctx, runID), true)
		}
	}

	recEdit(t, m, project, "a.fk", "s1", sub("run-sub-1")) // first edit only probes
	waitRecDetected(t, m)

	// A typed subagent's next edit publishes, and so does a fork child's
	// edit in another conversation.
	recEdit(t, m, project, "b.fk", "s1", sub("run-sub-2"))
	recEdit(t, m, project, "c.fk", "s2", fork("run-fork-1"))
	waitForPoll(t, "the subagent and fork recommendations", func() bool { return len(l.all()) == 2 })

	// Another subagent and a parent edit of the first conversation stay
	// quiet: that session was recommended for already.
	recEdit(t, m, project, "d.fk", "s1", sub("run-sub-3"))
	recEdit(t, m, project, "e.fk", "s1")
	time.Sleep(150 * time.Millisecond) // let any wrongful third publish land
	if got := l.all(); len(got) != 2 {
		t.Fatalf("recommendations = %d, want exactly 2", len(got))
	}

	runs, sessions := l.identities()
	bySession := map[string]string{} // conversation id -> the run id it published under
	for i := range sessions {
		bySession[sessions[i]] = runs[i]
	}
	if len(bySession) != 2 || bySession["s1"] != "run-sub-2" || bySession["s2"] != "run-fork-1" {
		t.Fatalf("listener identities = %v, want s1→run-sub-2 and s2→run-fork-1 (the child runs that edited)", bySession)
	}
	waitForBackgroundWriters(t, m)
}

// A context with only llm.WithAgentSessionID — no explicit conversation
// session id — still identifies the conversation (ConversationSessionIDFrom
// Context falls back), so the TUI main agent's edits recommend too. This is
// the real main-path contract; recEdit always sets the explicit id, so
// without this pin a regression of the fallback would look like "the TUI
// never recommends" again.
func TestRecommendFallsBackToAgentSessionID(t *testing.T) {
	m, l, project := newRecManager(t, appcfg.LSPSection{}, nil)
	putRecBinaryOnPath(t)
	agentOnlyEdit := func(name string) {
		file := filepath.Join(project, name)
		mustWrite(t, file, "content\n")
		ctx := llm.WithAgentSessionID(context.Background(), "s1")
		_ = m.DidWrite(ctx, "s1", []tool.FileChange{{AbsPath: file, After: []byte("content\n")}})
	}

	agentOnlyEdit("a.fk")
	waitRecDetected(t, m) // the first edit only probes
	agentOnlyEdit("b.fk")
	waitForPoll(t, "the recommendation on the agent-session fallback", func() bool { return len(l.all()) == 1 })
	if rec := l.all()[0]; rec.ServerID != "fake" || rec.Mode != "enable" {
		t.Fatalf("recommendation = %+v", rec)
	}
	_, sessions := l.identities()
	if len(sessions) != 1 || sessions[0] != "s1" {
		t.Fatalf("conversation ids = %v, want [s1] via the fallback", sessions)
	}

	// The dedup key is the same fallback identity: a second edit stays quiet.
	agentOnlyEdit("c.fk")
	time.Sleep(150 * time.Millisecond)
	if got := l.all(); len(got) != 1 {
		t.Fatalf("recommendations = %d, want the one the fallback earned", len(got))
	}
	waitForBackgroundWriters(t, m)
}

// The recommendation path never blocks the edit that triggered it, not even
// behind a listener that stalls.
func TestRecommendDoesNotBlockDidWrite(t *testing.T) {
	m, _, project := newRecManager(t, appcfg.LSPSection{}, nil)
	putRecBinaryOnPath(t)
	recEdit(t, m, project, "a.fk", "s1")
	waitRecDetected(t, m) // detection is cached now
	m.SetRecommendationListener(func(context.Context, event.LSPRecommendation) { time.Sleep(time.Second) })
	file := filepath.Join(project, "b.fk")
	mustWrite(t, file, "content\n")
	start := time.Now()
	_ = m.DidWrite(tool.WithConversationSessionID(context.Background(), "s2"), "s2", []tool.FileChange{{AbsPath: file, After: []byte("content\n")}})
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Fatalf("DidWrite took %s behind a stalled listener, want <100ms", took)
	}
	waitForBackgroundWriters(t, m)
}

// Each answer persists what spec §10.2 says it does.
func TestDecideRecommendation(t *testing.T) {
	readState := func(t *testing.T, m *Manager) recState {
		st := loadRecState(m.opts.AgentWorkspace)
		return st
	}
	t.Run("invalid choice", func(t *testing.T) {
		m, _, project := newRecManager(t, appcfg.LSPSection{}, nil)
		plantRec(m, "rec-1", project)
		err := m.DecideRecommendation("rec-1", event.LSPRecommendationChoice("bogus"))
		if err == nil || err.Error() != `invalid choice "bogus"` {
			t.Fatalf("error = %v, want the invalid-choice text", err)
		}
		waitForBackgroundWriters(t, m)
	})
	t.Run("enable", func(t *testing.T) {
		m, _, project := newRecManager(t, appcfg.LSPSection{}, nil)
		if err := saveRecState(m.opts.AgentWorkspace, recState{DismissedStreak: 3}); err != nil {
			t.Fatal(err)
		}
		plantRec(m, "rec-1", project)
		if err := m.DecideRecommendation("rec-1", event.LSPChoiceEnable); err != nil {
			t.Fatalf("DecideRecommendation(enable): %v", err)
		}
		enabled, err := LoadEnabled(m.opts.AgentWorkspace)
		if err != nil || !enabled["fake"] {
			t.Fatalf("enabled.json = %v (err %v), want fake: true", enabled, err)
		}
		if st := readState(t, m); st.DismissedStreak != 0 || st.Disabled {
			t.Fatalf("recommendations.json = %+v, want the streak reset and still on", st)
		}
		waitForBackgroundWriters(t, m)
	})
	t.Run("not_now five times", func(t *testing.T) {
		m, _, project := newRecManager(t, appcfg.LSPSection{}, nil)
		for i := 0; i < recDismissLimit; i++ {
			id := fmt.Sprintf("rec-%d", i)
			plantRec(m, id, project)
			if err := m.DecideRecommendation(id, event.LSPChoiceNotNow); err != nil {
				t.Fatalf("DecideRecommendation #%d: %v", i, err)
			}
		}
		st := readState(t, m)
		if !st.Disabled || st.DismissedStreak != recDismissLimit {
			t.Fatalf("state = %+v, want disabled after 5 dismissals", st)
		}
		if st.DisabledReason != "dismissed 5 times in a row" {
			t.Fatalf("reason = %q", st.DisabledReason)
		}
		if enabled, _ := LoadEnabled(m.opts.AgentWorkspace); enabled["fake"] {
			t.Fatal("not_now must not enable anything")
		}
		waitForBackgroundWriters(t, m)
	})
	t.Run("never", func(t *testing.T) {
		m, _, project := newRecManager(t, appcfg.LSPSection{}, nil)
		plantRec(m, "rec-1", project)
		if err := m.DecideRecommendation("rec-1", event.LSPChoiceNever); err != nil {
			t.Fatalf("DecideRecommendation(never): %v", err)
		}
		st := readState(t, m)
		if len(st.Never) != 1 || st.Never[0] != "fake" {
			t.Fatalf("never = %v, want [fake]", st.Never)
		}
		if st.Disabled || st.DismissedStreak != 0 {
			t.Fatalf("state = %+v, want still on and streak reset", st)
		}
		waitForBackgroundWriters(t, m)
	})
	t.Run("disable_all", func(t *testing.T) {
		m, _, project := newRecManager(t, appcfg.LSPSection{}, nil)
		plantRec(m, "rec-1", project)
		if err := m.DecideRecommendation("rec-1", event.LSPChoiceDisableAll); err != nil {
			t.Fatalf("DecideRecommendation(disable_all): %v", err)
		}
		st := readState(t, m)
		if !st.Disabled || st.DisabledReason != "turned off by the user" {
			t.Fatalf("state = %+v, want off by the user", st)
		}
		waitForBackgroundWriters(t, m)
	})
}

// An id the runtime never made — or already answered — is the sentinel the
// surfaces test for.
func TestDecideUnknownRecommendation(t *testing.T) {
	m, _, project := newRecManager(t, appcfg.LSPSection{}, nil)
	if err := m.DecideRecommendation("nope", event.LSPChoiceEnable); !errors.Is(err, tool.ErrUnknownLSPRecommendation) {
		t.Fatalf("unknown id error = %v", err)
	}
	plantRec(m, "rec-1", project)
	if err := m.DecideRecommendation("rec-1", event.LSPChoiceNotNow); err != nil {
		t.Fatalf("DecideRecommendation: %v", err)
	}
	if err := m.DecideRecommendation("rec-1", event.LSPChoiceNotNow); !errors.Is(err, tool.ErrUnknownLSPRecommendation) {
		t.Fatalf("second answer error = %v, want the sentinel", err)
	}
	waitForBackgroundWriters(t, m)
}

// The install answer returns at once; the install runs in the background
// and the switch lands when it succeeds.
func TestDecideInstallRunsInBackground(t *testing.T) {
	m, _ := installManager(t, "printf '#!/bin/sh\\ntrue\\n' > \"$TARGET_DIR/fakels-mgr\"\nchmod +x \"$TARGET_DIR/fakels-mgr\"\necho done")
	project := t.TempDir()
	plantRec(m, "rec-i", project)
	if err := m.DecideRecommendation("rec-i", event.LSPChoiceInstall); err != nil {
		t.Fatalf("DecideRecommendation(install): %v", err)
	}
	waitForPoll(t, "the install to finish and the switch to land", func() bool {
		for _, s := range m.Snapshot().Servers {
			if s.ID == "fake" {
				return s.Enabled && !s.Installing && s.InstallError == ""
			}
		}
		return false
	})
	waitForBackgroundWriters(t, m)
}

// The reset clears every field the state file carries and the snapshot
// reports recommendations as back on.
func TestResetRecommendations(t *testing.T) {
	m, _, _ := newRecManager(t, appcfg.LSPSection{}, nil)
	if err := saveRecState(m.opts.AgentWorkspace, recState{
		Disabled: true, DisabledReason: "turned off by the user",
		DismissedStreak: 3, Never: []string{"fake", "gopls"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.ResetRecommendations(); err != nil {
		t.Fatalf("ResetRecommendations: %v", err)
	}
	st := loadRecState(m.opts.AgentWorkspace)
	if st.Disabled || st.DisabledReason != "" || st.DismissedStreak != 0 || len(st.Never) != 0 {
		t.Fatalf("state after reset = %+v, want the zero value", st)
	}
	if m.Snapshot().RecommendationsDisabled {
		t.Fatal("snapshot still reports recommendations disabled")
	}
	waitForBackgroundWriters(t, m)
}

// overrideInstallCatalog replaces the catalog with one installable server so
// Manager.Install tests control the recipe.
func overrideInstallCatalog(t *testing.T) {
	t.Helper()
	orig := catalogOverride
	catalogOverride = []CatalogEntry{{
		ID:                  "fake",
		DisplayName:         "fake",
		Languages:           []string{"Fake"},
		ExtensionToLanguage: map[string]string{".fk": "fake"},
		Command:             "fakels-mgr",
		Install:             []InstallRecipe{{Requires: "fake-installer", Argv: []string{"fake-installer"}}},
	}}
	t.Cleanup(func() { catalogOverride = orig })
}

// installManager builds a manager over the overridden catalog, puts the body
// installer on the PATH and returns it with the installer's TARGET_DIR.
func installManager(t *testing.T, body string) (*Manager, string) {
	t.Helper()
	overrideInstallCatalog(t)
	dir := t.TempDir()
	writeExecutableScript(t, dir, "fake-installer", body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TARGET_DIR", dir)
	p := NewPool(nil)
	t.Cleanup(func() { _ = p.Close() })
	return p.NewManager(ManagerOptions{Trusted: true, AgentWorkspace: t.TempDir(), Home: t.TempDir()}), dir
}

// installStatus picks one server out of a snapshot.
func installStatus(t *testing.T, m *Manager) event.LSPServerStatus {
	t.Helper()
	for _, s := range m.Snapshot().Servers {
		if s.ID == "fake" {
			return s
		}
	}
	t.Fatal("the fake server is missing from the snapshot")
	return event.LSPServerStatus{}
}

func TestManagerInstallUnknownServer(t *testing.T) {
	m := NewPool(nil).NewManager(ManagerOptions{Trusted: true})
	if err := m.Install(context.Background(), "nope", nil); err == nil || err.Error() != `unknown language server "nope"` {
		t.Fatalf("unknown id error = %v", err)
	}
	waitForBackgroundWriters(t, m)
}

// installGate is the blocked installer the concurrent tests walk through:
// one line of output, then it waits for the gate file before finishing.
const installGateBody = `
echo installing fake
GATE="$TARGET_DIR/gate"
while [ ! -f "$GATE" ]; do sleep 0.05; done
printf '#!/bin/sh\ntrue\n' > "$TARGET_DIR/fakels-mgr"
chmod +x "$TARGET_DIR/fakels-mgr"
echo done
`

func TestManagerInstallNotifiesSubscribers(t *testing.T) {
	m, targetDir := installManager(t, installGateBody)
	gated := make(chan error, 1)
	go func() { gated <- m.Install(context.Background(), "fake", nil) }()

	// While it runs, the snapshot shows it, whoever may be watching.
	waitForPoll(t, "the snapshot to show the running install", func() bool {
		st := installStatus(t, m)
		return st.Installing && len(st.InstallLog) > 0 && st.InstallLog[0] == "installing fake" && st.InstallError == ""
	})
	// A subscriber wired before the install hears about it too.
	heard := make(chan event.LSPSnapshot, 4)
	cancel := m.Subscribe(func(snap event.LSPSnapshot) { heard <- snap })
	defer cancel()

	if err := os.WriteFile(filepath.Join(targetDir, "gate"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := <-gated; err != nil {
		t.Fatalf("Install: %v", err)
	}
	waitForPoll(t, "the snapshot to show the finished install", func() bool {
		st := installStatus(t, m)
		return !st.Installing && st.InstallError == ""
	})
	if last := installStatus(t, m); len(last.InstallLog) == 0 || last.InstallLog[len(last.InstallLog)-1] != "done" {
		t.Fatalf("install log = %v, want the last line", last.InstallLog)
	}
	select {
	case snap := <-heard:
		if len(snap.Servers) == 0 {
			t.Fatal("the subscriber saw an empty snapshot")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no subscriber notification arrived")
	}
	waitForBackgroundWriters(t, m)
}

func TestManagerInstallRejectsConcurrent(t *testing.T) {
	m, targetDir := installManager(t, installGateBody)
	gated := make(chan error, 1)
	go func() { gated <- m.Install(context.Background(), "fake", nil) }()
	waitForPoll(t, "the snapshot to show the running install", func() bool {
		return installStatus(t, m).Installing
	})
	if err := m.Install(context.Background(), "fake", nil); err == nil || err.Error() != "fake is already being installed" {
		t.Fatalf("concurrent install error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "gate"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := <-gated; err != nil {
		t.Fatalf("Install: %v", err)
	}
	waitForBackgroundWriters(t, m)
}

func TestManagerInstallFailureInSnapshot(t *testing.T) {
	m, _ := installManager(t, "echo it broke\nexit 3")
	if err := m.Install(context.Background(), "fake", nil); err == nil || !strings.Contains(err.Error(), "failed:") {
		t.Fatalf("Install error = %v", err)
	}
	st := installStatus(t, m)
	if st.Installing {
		t.Error("the install still runs in the snapshot")
	}
	if st.InstallError == "" || !strings.Contains(st.InstallError, "it broke") {
		t.Fatalf("InstallError = %q, want the failure tail", st.InstallError)
	}
	if len(st.InstallLog) != 1 || st.InstallLog[0] != "it broke" {
		t.Fatalf("InstallLog = %v", st.InstallLog)
	}
	waitForBackgroundWriters(t, m)
}

// fakeInstallStatus picks the fake server out of a delivered snapshot.
func fakeInstallStatus(t *testing.T, snap event.LSPSnapshot) event.LSPServerStatus {
	t.Helper()
	for _, s := range snap.Servers {
		if s.ID == "fake" {
			return s
		}
	}
	t.Fatal("the fake server is missing from the snapshot")
	return event.LSPServerStatus{}
}

// An install that fails within one debounce window still shows its running
// phase: watchers edge-detect Installing, so the start of the install must be
// delivered immediately instead of merging into the already-finished snapshot.
func TestManagerInstallFastFailureNotifiesRunning(t *testing.T) {
	m, _ := installManager(t, "echo it broke\nexit 3")
	heard := make(chan event.LSPSnapshot, 8)
	cancel := m.Subscribe(func(snap event.LSPSnapshot) { heard <- snap })
	defer cancel()
	// The subscription's first message is the seed: the current snapshot
	// before the install exists. The install's own notifications follow it.
	select {
	case <-heard:
	case <-time.After(5 * time.Second):
		t.Fatal("the subscription never delivered its seed snapshot")
	}

	if err := m.Install(context.Background(), "fake", nil); err == nil || !strings.Contains(err.Error(), "failed:") {
		t.Fatalf("Install error = %v", err)
	}
	select {
	case snap := <-heard:
		if st := fakeInstallStatus(t, snap); !st.Installing || st.InstallError != "" {
			t.Fatalf("first snapshot: installing=%v error=%q, want the running install", st.Installing, st.InstallError)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the install start never reached the subscriber")
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case snap := <-heard:
			if st := fakeInstallStatus(t, snap); !st.Installing && st.InstallError != "" {
				return // the failure reached the subscriber too
			}
		case <-deadline:
			t.Fatal("the install failure never reached the subscriber")
		}
	}
}

// A subscription's first message is the current snapshot: a watcher that
// subscribes after a change already happened (here: an install that already
// failed) must not wait for the next change to learn about it.
func TestSubscribeDeliversCurrentSnapshotFirst(t *testing.T) {
	m, _ := installManager(t, "echo it broke\nexit 3")
	if err := m.Install(context.Background(), "fake", nil); err == nil || !strings.Contains(err.Error(), "failed:") {
		t.Fatalf("Install error = %v, want the failure", err)
	}
	heard := make(chan event.LSPSnapshot, 4)
	cancel := m.Subscribe(func(snap event.LSPSnapshot) { heard <- snap })
	defer cancel()
	select {
	case snap := <-heard:
		st := fakeInstallStatus(t, snap)
		if st.Installing || st.InstallError == "" {
			t.Fatalf("first snapshot: installing=%v error=%q, want the already-failed install", st.Installing, st.InstallError)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Subscribe did not deliver the current snapshot as the first message")
	}
	waitForBackgroundWriters(t, m)
}

// detectInfo cannot start a background writer once Close has drained the
// manager's background writers: the bg.Add shares the closed flag's critical
// section, so a late detection cannot resurrect files under a torn-down
// agent workspace.
func TestDetectInfoAfterCloseDoesNotSpawn(t *testing.T) {
	m, _ := installManager(t, "echo done")
	m.Close()
	m.drainBackground()
	m.detectInfo(ServerConfig{ID: "fake"})
	if _, running := m.detecting["fake"]; running {
		t.Fatal("detectInfo started a background detection after Close drained the writers")
	}
}

// The Add-after-Wait shape of the race: concurrent detections and Close
// must not misuse the wait group (a panic here) and must leave no writer
// running after Close returned. Run under -race this is the plan's stress.
func TestDetectInfoConcurrentWithClose(t *testing.T) {
	m, _ := installManager(t, "echo done")
	srv := ServerConfig{ID: "fake"}
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					m.detectInfo(srv)
				}
			}
		}()
	}
	m.Close()
	m.drainBackground()
	close(done)
	wg.Wait()
}

// The install answer marks the install running before it returns, so a
// watcher subscribing right after the answer sees Installing=true in its
// first snapshot, never a previous attempt's terminal state.
func TestDecideInstallMarksRunningBeforeReturning(t *testing.T) {
	m, targetDir := installManager(t, installGateBody)
	project := t.TempDir()
	plantRec(m, "rec-sync", project)
	if err := m.DecideRecommendation("rec-sync", event.LSPChoiceInstall); err != nil {
		t.Fatalf("DecideRecommendation(install): %v", err)
	}
	if st := installStatus(t, m); !st.Installing {
		t.Fatal("the install is not marked running when the answer returns")
	}
	if err := os.WriteFile(filepath.Join(targetDir, "gate"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitForBackgroundWriters(t, m)
}

func TestSnapshotSurvivesConcurrentUse(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: diagScript(map[string]any{"bad": []any{diagRule("bad thing", 0)}}),
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			_ = env.manager.Snapshot()
		}
	}()
	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "contains bad\n")
	_ = env.manager.DidWrite(context.Background(), "s1", []tool.FileChange{{AbsPath: file, After: []byte("contains bad\n")}})
	<-done
	waitForBackgroundWriters(t, env.manager)
}

// Project entries without a decision surface as blocked: a new server gets
// its own blocked row, an override of a known server flags that server's row
// without changing its state (spec §5.2, §11.1).
func TestSnapshotShowsPendingProjectEntry(t *testing.T) {
	project := t.TempDir()
	writeProjectLSP(t, project, `servers:
  brandnew:
    command: /bin/brandnew
    extension_to_language: {".bn": brandnew}
  gopls:
    settings: {gopls: {buildFlags: ["-tags=integration"]}}
`)
	ws := t.TempDir()
	p := NewPool(&appcfg.Root{})
	t.Cleanup(func() { _ = p.Close() })
	m := p.NewManager(ManagerOptions{
		Home:           t.TempDir(),
		AgentWorkspace: ws,
		ProjectRoot:    project,
		ProjectKey:     "projkey",
		Trusted:        true,
	})
	waitForBackgroundWriters(t, m)

	snap := m.Snapshot()
	var pendingNew, flagged *event.LSPServerStatus
	for i := range snap.Servers {
		switch snap.Servers[i].ID {
		case "brandnew":
			pendingNew = &snap.Servers[i]
		case "gopls":
			flagged = &snap.Servers[i]
		}
	}
	if pendingNew == nil {
		t.Fatalf("no row for the pending project server: %+v", snap.Servers)
	}
	if pendingNew.Scope != "project" || pendingNew.State != event.LSPStateBlocked {
		t.Fatalf("pending row = %+v", pendingNew)
	}
	if want := "project entry awaiting confirmation: start forebrain in this project, or confirm it on the project page"; pendingNew.Note != want {
		t.Fatalf("pending note = %q", pendingNew.Note)
	}
	if flagged == nil {
		t.Fatal("gopls missing from the snapshot")
	}
	if flagged.State == event.LSPStateBlocked {
		t.Fatalf("an undecided override must not change the server's state: %+v", flagged)
	}
	if !strings.HasPrefix(flagged.Note, "project settings await confirmation") {
		t.Fatalf("gopls note = %q", flagged.Note)
	}

	// A declined entry the merged set does not know is listed as blocked
	// until the file changes.
	if err := DecideProjectServers(ws, project, "projkey", nil); err != nil {
		t.Fatal(err)
	}
	snap = m.Snapshot()
	var denied *event.LSPServerStatus
	for i := range snap.Servers {
		if snap.Servers[i].ID == "brandnew" {
			denied = &snap.Servers[i]
		}
	}
	if denied == nil || denied.State != event.LSPStateBlocked {
		t.Fatalf("declined row = %+v", denied)
	}
	if want := "project entry declined; it asks again when .forebrain/lsp_servers.yaml changes"; denied.Note != want {
		t.Fatalf("declined note = %q", denied.Note)
	}
	// gopls keeps its plain row once the override was declined.
	for i := range snap.Servers {
		if snap.Servers[i].ID == "gopls" && strings.Contains(snap.Servers[i].Note, "await confirmation") {
			t.Fatalf("declined override still flagged: %+v", snap.Servers[i])
		}
	}
}

// The project file's notes reach the snapshot's ProjectNotes, which the three
// renderers show as "Project file: <note>".
func TestSnapshotProjectNotes(t *testing.T) {
	project := t.TempDir()
	writeProjectLSP(t, project, "servers:\n  s:\n    command: x\n    env_passthrough: [HOME]\nextras: 1\n")
	p := NewPool(&appcfg.Root{})
	t.Cleanup(func() { _ = p.Close() })
	m := p.NewManager(ManagerOptions{
		Home:           t.TempDir(),
		AgentWorkspace: t.TempDir(),
		ProjectRoot:    project,
		ProjectKey:     "projkey",
		Trusted:        true,
	})
	waitForBackgroundWriters(t, m)

	snap := m.Snapshot()
	joined := strings.Join(snap.ProjectNotes, "\n")
	if !strings.Contains(joined, `lsp_servers.yaml: unknown key "extras"`) {
		t.Fatalf("notes missing the unknown key: %q", snap.ProjectNotes)
	}
	if !strings.Contains(joined, "s: env_passthrough and priority are ignored in project files") {
		t.Fatalf("notes missing the env_passthrough note: %q", snap.ProjectNotes)
	}
}
