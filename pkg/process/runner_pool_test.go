package process

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/stretchr/testify/require"
)

func newPoolTestEnv(t *testing.T) (*Environment, *state.ProjectStore, *sql.DB) {
	t.Helper()
	t.Setenv("FOREBRAIN_POOL_TEST_KEY", "test-key")
	home := t.TempDir()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	const body = "agents:\n" +
		"  definitions:\n" +
		"    main:\n" +
		"      llm_providers:\n" +
		"        - provider: openai\n" +
		"          model: gpt-test\n" +
		"          api_key: ${FOREBRAIN_POOL_TEST_KEY}\n" +
		"          base_url: http://localhost:0/v1\n"
	if err := osWriteFile(cfgPath, body); err != nil {
		t.Fatal(err)
	}
	cfg, err := appcfg.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	live := &cfg
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: live, WorkspaceRoot: filepath.Join(home, "workspace")}}
	if err := runner.Load(); err != nil {
		t.Fatalf("base runner load: %v", err)
	}
	sessStore := state.NewSessionStore(db, "main")
	env := &Environment{Root: home, Deps: run.Deps{Home: home, AgentName: "main", AppCfg: live, WorkspaceRoot: filepath.Join(home, "workspace"), SessionStore: sessStore}, ConfigPath: cfgPath, Runner: runner, SQL: db}
	projects := state.NewProjectStore(db, "main")
	return env, projects, db
}

func osWriteFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}

func TestRunnerPoolDifferentProjectsGetDifferentRunners(t *testing.T) {
	ctx := context.Background()
	env, projects, _ := newPoolTestEnv(t)
	pool := NewRunnerPool(env, 4)
	defer pool.Close()

	projA, err := projects.Create(ctx, state.CreateProjectInput{Name: "a", Root: "/tmp/pool-a", ProjectKey: "-tmp-pool-a"})
	if err != nil {
		t.Fatal(err)
	}
	projB, err := projects.Create(ctx, state.CreateProjectInput{Name: "b", Root: "/tmp/pool-b", ProjectKey: "-tmp-pool-b"})
	if err != nil {
		t.Fatal(err)
	}
	ensurePoolSessions(t, env, "sess-a", "sess-b")
	if err := projects.BindSession(ctx, "sess-a", projA.ID); err != nil {
		t.Fatal(err)
	}
	if err := projects.BindSession(ctx, "sess-b", projB.ID); err != nil {
		t.Fatal(err)
	}
	runnerA := pool.RunnerForSession(ctx, "sess-a")
	runnerB := pool.RunnerForSession(ctx, "sess-b")
	if runnerA == nil || runnerB == nil {
		t.Fatalf("runners missing")
	}
	if runnerA == runnerB {
		t.Fatalf("two projects must get different runners")
	}
	if runnerA == env.Runner || runnerB == env.Runner {
		t.Fatalf("project sessions must not run on the base runner")
	}
	if runnerA.ProjectRoot != "/tmp/pool-a" || runnerB.ProjectRoot != "/tmp/pool-b" {
		t.Fatalf("project roots not applied: %q %q", runnerA.ProjectRoot, runnerB.ProjectRoot)
	}
	// Same session resolves to the same runner repeatedly.
	if again := pool.RunnerForSession(ctx, "sess-a"); again != runnerA {
		t.Fatalf("session must keep its runner")
	}
	// A session with no project runs on the base runner, unchanged behavior.
	if got := pool.RunnerForSession(ctx, "sess-plain"); got != env.Runner {
		t.Fatalf("no-project session must use the base runner")
	}
}

// TestRunnerPoolRunnersPublishToTheSurface pins that a project runner speaks
// to the same surface as the environment's own. Without the sink a session
// bound to a project never showed its subagents' streams or its compactions.
func TestRunnerPoolRunnersPublishToTheSurface(t *testing.T) {
	ctx := context.Background()
	env, projects, _ := newPoolTestEnv(t)
	var got []string
	env.Runner.Events = event.SinkFunc(func(_ context.Context, evt event.RunEvent) error {
		got = append(got, evt.Type)
		return nil
	})
	pool := NewRunnerPool(env, 4)
	defer pool.Close()
	proj, err := projects.Create(ctx, state.CreateProjectInput{Name: "a", Root: "/tmp/pool-events", ProjectKey: "-tmp-pool-events"})
	if err != nil {
		t.Fatal(err)
	}
	ensurePoolSessions(t, env, "sess-a")
	if err := projects.BindSession(ctx, "sess-a", proj.ID); err != nil {
		t.Fatal(err)
	}
	pooled := pool.RunnerForSession(ctx, "sess-a")
	if pooled == env.Runner || pooled.Events == nil {
		t.Fatal("the project runner has no surface to publish to")
	}
	if err := pooled.Events.Publish(ctx, event.NewRunEvent("", "", "sess-a", event.RunEventContextCompacting, event.ContextCompactingPayload{}, time.Now())); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != event.RunEventContextCompacting {
		t.Fatalf("surface saw %v", got)
	}
}

// TestRunnerPoolKeepsBoundEntriesAndClosesEvictedOnes pins both halves of the
// eviction contract.
//
// A session bound to an entry must keep it even when the pool is over its soft
// bound: re-resolving would build a fresh Runner, restart its MCP servers, and
// hand that session a different tool table — and with it a different prompt
// prefix — in the middle of a conversation. The entry that is evictable is the
// one nothing is bound to any more, and evicting it must close its Runner,
// because an evicted Runner's servers must not keep running as orphans.
func TestRunnerPoolKeepsBoundEntriesAndClosesEvictedOnes(t *testing.T) {
	ctx := context.Background()
	env, projects, _ := newPoolTestEnv(t)
	pool := NewRunnerPool(env, 1)
	defer pool.Close()

	newProject := func(name string) state.Project {
		t.Helper()
		root := "/tmp/pool-evict-" + name
		p, err := projects.Create(ctx, state.CreateProjectInput{Name: root, Root: root, ProjectKey: "-tmp-pool-evict"})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	resolve := func(sid string, p state.Project) *run.Runner {
		t.Helper()
		ensurePoolSessions(t, env, sid)
		if err := projects.BindSession(ctx, sid, p.ID); err != nil {
			t.Fatal(err)
		}
		return pool.RunnerForSession(ctx, sid)
	}

	var runners []*run.Runner
	var sessions []string
	for i, name := range []string{"a", "b", "c"} {
		sid := "sess-evict-" + name
		sessions = append(sessions, sid)
		r := resolve(sid, newProject(name))
		if i > 0 && r == runners[i-1] {
			t.Fatalf("session %s reused the previous project's runner", sid)
		}
		runners = append(runners, r)
	}
	pool.mu.Lock()
	live := len(pool.entries)
	pool.mu.Unlock()
	if live != 3 {
		t.Fatalf("every entry is bound to a session, so none may be evicted: live=%d", live)
	}

	// Unbinding the oldest session makes its entry evictable; creating one more
	// entry is what runs the eviction.
	if err := pool.BindSession(ctx, sessions[0], ""); err != nil {
		t.Fatal(err)
	}
	resolve("sess-evict-d", newProject("d"))
	pool.mu.Lock()
	live = len(pool.entries)
	pool.mu.Unlock()
	if live != 3 {
		t.Fatalf("eviction must drop exactly the unbound entry: live=%d", live)
	}
	if runners[0].MCPRegistry() != nil {
		t.Fatal("evicting an entry must close its runner; its MCP registry is still live")
	}
	for _, r := range runners[1:] {
		if r.MCPRegistry() == nil {
			t.Fatal("a runner a session is still bound to must not be closed")
		}
	}
}

func TestRunnerPoolNoProjectSessionMatchesBase(t *testing.T) {
	ctx := context.Background()
	env, _, _ := newPoolTestEnv(t)
	pool := NewRunnerPool(env, 4)
	defer pool.Close()
	if got := pool.RunnerForSession(ctx, "unknown-session"); got != env.Runner {
		t.Fatalf("unknown session must fall back to the base runner")
	}
	if got := pool.RunnerForSession(ctx, ""); got != env.Runner {
		t.Fatalf("empty session id must fall back to the base runner")
	}
}

func TestRunnerPoolProjectEditFreezesExistingSessions(t *testing.T) {
	ctx := context.Background()
	env, projects, _ := newPoolTestEnv(t)
	pool := NewRunnerPool(env, 4)
	defer pool.Close()

	proj, err := projects.Create(ctx, state.CreateProjectInput{Name: "edit", Root: "/tmp/pool-edit", ProjectKey: "-tmp-pool-edit", Instructions: "first"})
	if err != nil {
		t.Fatal(err)
	}
	ensurePoolSessions(t, env, "sess-edit", "sess-edit-2")
	if err := projects.BindSession(ctx, "sess-edit", proj.ID); err != nil {
		t.Fatal(err)
	}
	bound := pool.RunnerForSession(ctx, "sess-edit")
	if bound == nil || bound == env.Runner {
		t.Fatalf("project runner not built")
	}
	if got := bound.ProjectInstructionsFor("sess-edit"); got != "first" {
		t.Fatalf("instructions snapshot: %q", got)
	}
	// Edit the project. New sessions get the new instructions; the bound
	// session keeps its frozen snapshot.
	newInstructions := "second"
	if _, err := projects.Update(ctx, proj.ID, state.UpdateProjectInput{Instructions: &newInstructions}); err != nil {
		t.Fatal(err)
	}
	if err := projects.BindSession(ctx, "sess-edit-2", proj.ID); err != nil {
		t.Fatal(err)
	}
	fresh := pool.RunnerForSession(ctx, "sess-edit-2")
	if fresh == nil || fresh == env.Runner {
		t.Fatalf("second project runner not built")
	}
	if got := fresh.ProjectInstructionsFor("sess-edit-2"); got != "second" {
		t.Fatalf("new session should see updated instructions: %q", got)
	}
	if got := bound.ProjectInstructionsFor("sess-edit"); got != "first" {
		t.Fatalf("bound session's instructions moved mid-session: %q", got)
	}
	if got := pool.RunnerForSession(ctx, "sess-edit"); got != bound {
		t.Fatalf("bound session must keep its entry after the edit")
	}
}

func TestRunnerPoolMemoryScopeReachesRunner(t *testing.T) {
	ctx := context.Background()
	env, projects, _ := newPoolTestEnv(t)
	pool := NewRunnerPool(env, 4)
	defer pool.Close()
	proj, err := projects.Create(ctx, state.CreateProjectInput{Name: "scoped", Root: "/tmp/pool-scope", ProjectKey: "-tmp-pool-scope", MemoryScope: state.ProjectMemoryProjectOnly})
	if err != nil {
		t.Fatal(err)
	}
	ensurePoolSessions(t, env, "sess-scope")
	if err := projects.BindSession(ctx, "sess-scope", proj.ID); err != nil {
		t.Fatal(err)
	}
	r := pool.RunnerForSession(ctx, "sess-scope")
	if !r.ProjectMemoryOnly() {
		t.Fatalf("project_only memory scope did not reach the runner")
	}
}

func TestRunnerPoolRebindAgentClearsEntries(t *testing.T) {
	ctx := context.Background()
	env, projects, _ := newPoolTestEnv(t)
	pool := NewRunnerPool(env, 4)
	defer pool.Close()
	proj, _ := projects.Create(ctx, state.CreateProjectInput{Name: "x", Root: "/tmp/pool-rebind", ProjectKey: "-tmp-pool-rebind"})
	ensurePoolSessions(t, env, "sess-rebind")
	if err := projects.BindSession(ctx, "sess-rebind", proj.ID); err != nil {
		t.Fatal(err)
	}
	if r := pool.RunnerForSession(ctx, "sess-rebind"); r == env.Runner {
		t.Fatalf("project runner expected")
	}
	pool.RebindAgent("other")
	pool.mu.Lock()
	live := len(pool.entries)
	pool.mu.Unlock()
	if live != 0 {
		t.Fatalf("rebind must tear down the previous agent's runners, got %d", live)
	}
}

func TestRunnerPoolProjectMCPServesProjectServers(t *testing.T) {
	ctx := context.Background()
	env, projects, _ := newPoolTestEnv(t)
	pool := NewRunnerPool(env, 4)
	defer pool.Close()
	proj, err := projects.Create(ctx, state.CreateProjectInput{Name: "mcpproj", Root: "/tmp/pool-mcp", ProjectKey: "-tmp-pool-mcp"})
	if err != nil {
		t.Fatal(err)
	}
	ensurePoolSessions(t, env, "sess-mcp")
	if err := projects.BindSession(ctx, "sess-mcp", proj.ID); err != nil {
		t.Fatal(err)
	}
	r := pool.RunnerForSession(ctx, "sess-mcp")
	if r.MCPProject != "/tmp/pool-mcp" {
		t.Fatalf("project runner MCPProject=%q", r.MCPProject)
	}
	if r.LaunchProject.Project.Root == "" {
		t.Fatalf("launch context not captured")
	}
	if !strings.Contains(poolKey("main", proj), "main") {
		t.Fatalf("pool key must include the agent")
	}
	// The untrusted project contributes no project entries to the list.
	for _, srv := range r.MCPServers {
		if mcp.IsProjectScope(srv) {
			t.Fatalf("unconfirmed project server should not be in the list: %+v", srv)
		}
	}
}

func ensurePoolSessions(t *testing.T, env *Environment, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := env.Deps.SessionStore.Ensure(context.Background(), id, id); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSweepReleasesIdleEntriesAndKeepsThemBound pins what the idle sweeper is
// allowed to take: the connections of an entry nobody has resolved through
// lately, and nothing else. The entry, its runner and its binding all stay —
// a session that comes back must resolve to the very Runner it froze with.
//
// The release call is substituted with a recorder because what is being pinned
// here is the sweeper's selection and its restraint; the release itself — that
// it reclaims a child and grows the connection back — is pinned against real
// processes in pkg/run and pkg/mcp.
func TestSweepReleasesIdleEntriesAndKeepsThemBound(t *testing.T) {
	ctx := context.Background()
	env, projects, _ := newPoolTestEnv(t)
	pool := NewRunnerPool(env, 4)
	defer pool.Close()

	proj, err := projects.Create(ctx, state.CreateProjectInput{Name: "idle", Root: "/tmp/pool-idle", ProjectKey: "-tmp-pool-idle"})
	if err != nil {
		t.Fatal(err)
	}
	// The gateway binds both halves before the first turn resolves: the store
	// owns session→project, the pool owns session→entry. A session with only
	// the second resolves to the base runner, which is not what this test is
	// about.
	ensurePoolSessions(t, env, "s-idle")
	if err := projects.BindSession(ctx, "s-idle", proj.ID); err != nil {
		t.Fatal(err)
	}
	if err := pool.BindSession(ctx, "s-idle", proj.ID); err != nil {
		t.Fatal(err)
	}
	runner := pool.RunnerForSession(ctx, "s-idle")
	if runner == nil {
		t.Fatal("binding did not resolve a runner")
	}

	var released []*run.Runner
	pool.sweepMu.Lock()
	pool.releaseIdleMCP = func(r *run.Runner) bool {
		released = append(released, r)
		return true
	}
	pool.sweepMu.Unlock()

	// Fresh entries are left alone: an active conversation resolves on every
	// turn, so a lastUsed within the threshold means somebody is using it.
	pool.sweepIdleEntries(time.Now())
	if len(released) != 0 {
		t.Fatalf("a fresh entry was released: %d calls", len(released))
	}

	// Past the threshold the release happens — through the runner the entry
	// holds, which is where the real release checks its own quiet period.
	old := time.Now().Add(-2 * runnerPoolIdleRelease)
	pool.mu.Lock()
	for _, entry := range pool.entries {
		entry.lastUsed = old
	}
	pool.mu.Unlock()
	pool.sweepIdleEntries(time.Now())
	if len(released) != 1 || released[0] != runner {
		t.Fatalf("the idle entry was not released through its own runner: %d calls", len(released))
	}

	// And nothing was taken: same runner, same binding, same entry count.
	if got := pool.RunnerForSession(ctx, "s-idle"); got != runner {
		t.Fatal("the idle entry's runner changed after a release — the freeze was broken")
	}
	pool.mu.Lock()
	entries := len(pool.entries)
	pool.mu.Unlock()
	if entries != 1 {
		t.Fatalf("the sweeper evicted an entry: %d remain", entries)
	}
}

// TestPoolSweepStopsWithThePool pins the sweeper's lifecycle: Close ends it,
// and a pool that is reused after a teardown (RebindAgent) runs exactly one
// sweeper again. A goroutine per abandoned pool is the leak this exists to
// prevent, one sweeper per live pool is the fix.
func TestPoolSweepStopsWithThePool(t *testing.T) {
	env, _, _ := newPoolTestEnv(t)
	pool := NewRunnerPool(env, 1)

	pool.sweepMu.Lock()
	first := pool.sweepStop
	pool.sweepMu.Unlock()
	if first == nil {
		t.Fatal("NewRunnerPool must start the sweeper")
	}

	pool.RebindAgent("other")
	pool.sweepMu.Lock()
	second := pool.sweepStop
	pool.sweepMu.Unlock()
	if second == nil {
		t.Fatal("RebindAgent reuses the pool, so the sweeper must come back with it")
	}

	pool.Close()
	pool.sweepMu.Lock()
	afterClose := pool.sweepStop
	pool.sweepMu.Unlock()
	if afterClose != nil {
		t.Fatal("Close must stop the sweeper")
	}
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("the first sweeper goroutine did not exit after RebindAgent's teardown")
	}
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("the second sweeper goroutine did not exit after Close")
	}
	// Stopping twice must not panic on a double channel close.
	pool.stopSweep()
	pool.stopSweep()
}

// TestEvictStaleEntriesOnlyWhenEverySessionIsQuiet pins the eviction rule this
// whole design turns on: liveness, not recency of the entry.
//
// An entry is evictable when every session bound to it has been quiet past the
// threshold — and only then, because that is the point at which the frozen tool
// table the entry protects has no live provider cache to match any more. One
// conversation that talked within the threshold pins its runner however stale
// the entry itself looks, which is the opposite of an LRU: the busy entry of an
// unpopular project survives a full pool.
func TestEvictStaleEntriesOnlyWhenEverySessionIsQuiet(t *testing.T) {
	ctx := context.Background()
	env, projects, _ := newPoolTestEnv(t)
	pool := NewRunnerPool(env, 4)
	defer pool.Close()
	store := env.Deps.SessionStore

	projPin, err := projects.Create(ctx, state.CreateProjectInput{Name: "pin", Root: "/tmp/ev-pin", ProjectKey: "-tmp-ev-pin"})
	if err != nil {
		t.Fatal(err)
	}
	projGone, err := projects.Create(ctx, state.CreateProjectInput{Name: "gone", Root: "/tmp/ev-gone", ProjectKey: "-tmp-ev-gone"})
	if err != nil {
		t.Fatal(err)
	}
	ensurePoolSessions(t, env, "s-pin", "s-quiet-a", "s-quiet-b", "s-gone")
	for _, pair := range []struct {
		sid string
		pid string
	}{{"s-pin", projPin.ID}, {"s-quiet-a", projPin.ID}, {"s-quiet-b", projGone.ID}, {"s-gone", projGone.ID}} {
		if err := projects.BindSession(ctx, pair.sid, pair.pid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Append(ctx, "s-pin", "user", "talked within the window"); err != nil {
		t.Fatal(err)
	}
	pinned := pool.RunnerForSession(ctx, "s-pin")
	quietA := pool.RunnerForSession(ctx, "s-quiet-a")
	if pinned == nil || quietA == nil {
		t.Fatal("bindings did not resolve runners")
	}
	// s-gone's conversation is deleted before the sweep: a deleted session has
	// no liveness to contribute and must not hold its entry open.
	if err := evictTestDeleteSession(t, env, "s-gone"); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-2 * runnerPoolSessionEviction)
	pool.mu.Lock()
	for _, entry := range pool.entries {
		entry.lastUsed = old
	}
	pool.mu.Unlock()
	pool.evictStaleEntries(ctx, time.Now())

	pool.mu.Lock()
	entries := len(pool.entries)
	pool.mu.Unlock()
	// The pinned project keeps its entry (s-quiet-a is quiet but s-pin talked);
	// the gone project's every session is quiet or deleted, so it goes.
	if entries != 1 {
		t.Fatalf("%d entries survive, want the pinned project's one", entries)
	}
	if got := pool.RunnerForSession(ctx, "s-pin"); got != pinned {
		t.Fatal("the live session's runner was evicted from under it")
	}
	if got := pool.RunnerForSession(ctx, "s-quiet-b"); got == quietA || got == nil {
		t.Fatal("the stale session must re-resolve to a fresh runner, not the evicted one")
	}
}

// TestEvictStaleEntriesRefusesRecentEntries pins the guard that keeps a
// just-resolved entry alive even when its sessions have no rows yet: a session
// that bound to a project but has not spoken has no updated_at to vouch for it,
// and the entry's own lastUsed is what stands in until it does.
func TestEvictStaleEntriesRefusesRecentEntries(t *testing.T) {
	ctx := context.Background()
	env, projects, _ := newPoolTestEnv(t)
	pool := NewRunnerPool(env, 4)
	defer pool.Close()

	proj, err := projects.Create(ctx, state.CreateProjectInput{Name: "fresh", Root: "/tmp/ev-fresh", ProjectKey: "-tmp-ev-fresh"})
	if err != nil {
		t.Fatal(err)
	}
	ensurePoolSessions(t, env, "s-new")
	if err := projects.BindSession(ctx, "s-new", proj.ID); err != nil {
		t.Fatal(err)
	}
	if pool.RunnerForSession(ctx, "s-new") == nil {
		t.Fatal("binding did not resolve a runner")
	}
	// No session activity, no stale lastUsed: eviction must refuse.
	pool.evictStaleEntries(ctx, time.Now())
	pool.mu.Lock()
	entries := len(pool.entries)
	pool.mu.Unlock()
	if entries != 1 {
		t.Fatalf("%d entries after an eviction of a fresh entry, want 1", entries)
	}
}

// evictTestDeleteSession removes a conversation row the way the store's own
// delete path would, so a test can make a session's liveness unanswerable.
func evictTestDeleteSession(t *testing.T, env *Environment, sessionID string) error {
	t.Helper()
	_, err := env.SQL.ExecContext(context.Background(), `DELETE FROM fb_sessions WHERE id=? AND agent_id=?`, sessionID, env.Deps.AgentName)
	return err
}

// TestEntryEvictableLockedIsTheRuleItself pins each leg of the eviction
// decision in isolation, including the one a flow test cannot reach
// deterministically: a resolution that arrived between the candidate pass and
// the removal must un-candidate the entry, because the activity map read in
// between does not know a session merely resolved — only the store knows when
// a conversation talked.
func TestEntryEvictableLockedIsTheRuleItself(t *testing.T) {
	env, _, _ := newPoolTestEnv(t)
	pool := NewRunnerPool(env, 1)
	defer pool.Close()
	stale := time.Now().Add(-2 * runnerPoolSessionEviction)
	fresh := time.Now()
	entry := &poolEntry{key: "k", runner: env.Runner, lastUsed: stale}
	oldActivity := map[string]int64{"s": stale.Add(-time.Hour).Unix()}
	freshActivity := map[string]int64{"s": time.Now().Unix()}

	if !entryEvictableLocked(entry, []string{"s"}, oldActivity, time.Now()) {
		t.Fatal("a quiet entry with only quiet sessions is evictable")
	}
	if entryEvictableLocked(entry, []string{"s"}, freshActivity, time.Now()) {
		t.Fatal("a session that talked within the threshold pins its entry")
	}
	// The mid-race save: lastUsed moved after the candidate pass.
	recentlyUsed := &poolEntry{key: entry.key, runner: entry.runner, lastUsed: fresh}
	if entryEvictableLocked(recentlyUsed, []string{"s"}, oldActivity, time.Now()) {
		t.Fatal("an entry resolved through during the activity read must be spared")
	}
	// A binding with no conversation row cannot hold a runner open forever;
	// the entry's own lastUsed is what covered it until now.
	if !entryEvictableLocked(entry, []string{"never-created"}, oldActivity, time.Now()) {
		t.Fatal("a rowless binding must not veto eviction of a stale entry")
	}
	// The busy-runner leg of the rule is the probe itself (MCPStartup.IdleFor),
	// which pkg/run pins against a real foreground turn; here the runner is
	// untouched and reads idle, which is the leg this test exercises above.
}

// TestSweepToolStateCleansBaseAndPooledRunners pins the third leg of the sweep:
// the per-session state a runner accumulates is collected when the conversation
// it belonged to has gone quiet, on the base runner as well as on pooled ones.
//
// The base runner carries every project-less session — the pool never sees
// those conversations, so cleaning only entries would leak exactly the ones
// the gateway serves the most of.
func TestSweepToolStateCleansBaseAndPooledRunners(t *testing.T) {
	ctx := context.Background()
	env, projects, _ := newPoolTestEnv(t)
	pool := NewRunnerPool(env, 4)
	defer pool.Close()
	store := env.Deps.SessionStore

	// A quiet session on the base runner and an active one beside it.
	ensurePoolSessions(t, env, "base-quiet", "base-live")
	if _, err := store.Append(ctx, "base-live", "user", "still talking"); err != nil {
		t.Fatal(err)
	}
	// A pooled entry with its own quiet session.
	proj, err := projects.Create(ctx, state.CreateProjectInput{Name: "state", Root: "/tmp/st-proj", ProjectKey: "-tmp-st-proj"})
	if err != nil {
		t.Fatal(err)
	}
	ensurePoolSessions(t, env, "pool-quiet")
	if err := projects.BindSession(ctx, "pool-quiet", proj.ID); err != nil {
		t.Fatal(err)
	}
	pooled := pool.RunnerForSession(ctx, "pool-quiet")
	if pooled == nil {
		t.Fatal("binding did not resolve a runner")
	}

	// Both runners' states know about their sessions (and the base runner also
	// knows the live one), with data the sweep should take or keep.
	baseState, poolState := env.Runner.Tools(), pooled.Tools()
	if baseState == nil || poolState == nil {
		t.Fatal("a loaded runner has no tool state")
	}
	// Append and session creation stamp now; age both quiet rows past the
	// threshold only after every binding has resolved, so nothing re-stamps
	// them.
	if _, err := env.SQL.ExecContext(ctx,
		`UPDATE fb_sessions SET updated_at=? WHERE id IN (?,?)`, time.Now().Add(-2*runnerPoolSessionEviction).Unix(), "base-quiet", "pool-quiet"); err != nil {
		t.Fatal(err)
	}
	baseState.RecordToolResultSpill(tool.ToolResultSpill{SessionID: "base-quiet", RunID: "r-bq", Path: "/tmp/bq"})
	baseState.RecordToolResultSpill(tool.ToolResultSpill{SessionID: "base-live", RunID: "r-bl", Path: "/tmp/bl"})
	poolState.RecordToolResultSpill(tool.ToolResultSpill{SessionID: "pool-quiet", RunID: "r-pq", Path: "/tmp/pq"})

	pool.sweepToolState(ctx, time.Now())

	if ids := baseState.SessionIDs(); len(ids) != 1 || ids[0] != "base-live" {
		t.Fatalf("base runner sessions after sweep = %v, want only the live one", ids)
	}
	if ids := poolState.SessionIDs(); len(ids) != 0 {
		t.Fatalf("pooled runner sessions after sweep = %v, want none", ids)
	}
	if spills := baseState.ToolResultSpills("base-live", ""); len(spills) == 0 {
		t.Fatal("the live session's data was collected with the quiet ones")
	}
}

// reorderedPoolConfig writes a config whose provider entry is rotated onto a
// new credential and base URL, then loads it: the pointer a reload would
// propagate.
func reorderedPoolConfig(t *testing.T, cfgPath string) *appcfg.Root {
	t.Helper()
	t.Setenv("FOREBRAIN_POOL_ROTATED_TEST_KEY", "rotated-key")
	body := "agents:\n" +
		"  definitions:\n" +
		"    main:\n" +
		"      llm_providers:\n" +
		"        - provider: openai\n" +
		"          model: gpt-test\n" +
		"          api_key: ${FOREBRAIN_POOL_ROTATED_TEST_KEY}\n" +
		"          base_url: http://rotated.test/v1\n"
	if err := osWriteFile(cfgPath, body); err != nil {
		t.Fatal(err)
	}
	cfg, err := appcfg.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return &cfg
}

func boundPoolEntry(t *testing.T, env *Environment, projects *state.ProjectStore, session, name, root string) *run.Runner {
	t.Helper()
	ctx := context.Background()
	proj, err := projects.Create(ctx, state.CreateProjectInput{Name: name, Root: root, ProjectKey: strings.ReplaceAll(root, "/", "-")})
	if err != nil {
		t.Fatal(err)
	}
	ensurePoolSessions(t, env, session)
	if err := projects.BindSession(ctx, session, proj.ID); err != nil {
		t.Fatal(err)
	}
	r := env.pool.RunnerForSession(ctx, session)
	if r == nil || r == env.Runner {
		t.Fatalf("session %s did not resolve to a pooled runner", session)
	}
	return r
}

// TestRunnerPoolPropagateConfigReachesEveryEntry pins the propagation
// contract: one reload offers every live entry the same config through its
// own LoadConfig transaction, so a pooled runner's provider details refresh
// while its published selection survives.
func TestRunnerPoolPropagateConfigReachesEveryEntry(t *testing.T) {
	ctx := context.Background()
	env, projects, _ := newPoolTestEnv(t)
	env.poolOnce.Do(func() { env.pool = NewRunnerPool(env, 4) })
	defer env.pool.Close()
	pooled := boundPoolEntry(t, env, projects, "sess-a", "prop-a", "/tmp/pool-prop-a")
	pinnedBefore := run.PrimaryModelSelection(pooled)
	require.True(t, pinnedBefore.Set)

	next := reorderedPoolConfig(t, env.ConfigPath)
	require.NoError(t, env.pool.PropagateConfig(ctx, next))

	if pooled.AppCfg != next {
		t.Fatalf("pooled runner still on the old config pointer")
	}
	pinned := run.PrimaryModelSelection(pooled)
	require.True(t, pinned.Set)
	require.Equal(t, pinnedBefore.Model, pinned.Model, "an ordinary reload must keep the published selection")
	if got := run.PrimaryEndpoint(pooled); got != "http://rotated.test/v1" {
		t.Fatalf("provider details did not refresh: %q", got)
	}
	// The session keeps resolving to the same entry, which now runs the new
	// config: the freeze protects identity, not staleness.
	if again := env.pool.RunnerForSession(ctx, "sess-a"); again != pooled {
		t.Fatalf("propagation must not rebind the session")
	}
}

// TestRunnerPoolPropagateConfigKeepsFailingEntryOnOldConfig pins the
// failure half: an entry that cannot adopt the new config keeps its previous
// configuration and runtime (LoadConfig's own rollback), and is named in the
// returned error rather than silently skipped.
func TestRunnerPoolPropagateConfigKeepsFailingEntryOnOldConfig(t *testing.T) {
	ctx := context.Background()
	env, projects, _ := newPoolTestEnv(t)
	env.poolOnce.Do(func() { env.pool = NewRunnerPool(env, 4) })
	defer env.pool.Close()
	pooled := boundPoolEntry(t, env, projects, "sess-a", "prop-fail", "/tmp/pool-prop-fail")
	before := pooled.AppCfg
	pinnedBefore := run.PrimaryModelSelection(pooled)

	// Parses, but no agent definition resolves, so the entry's LoadConfig
	// fails and its own rollback keeps the old runtime.
	broken := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {Primary: true},
	}}}
	err := env.pool.PropagateConfig(ctx, broken)
	if err == nil {
		t.Fatal("an entry that cannot adopt the config must be reported")
	}
	if pooled.AppCfg != before {
		t.Fatalf("failing entry's config pointer moved: %p", pooled.AppCfg)
	}
	pinned := run.PrimaryModelSelection(pooled)
	require.True(t, pinned.Set)
	require.Equal(t, pinnedBefore.Model, pinned.Model, "failing entry must keep its previous selection")
}

// TestRunnerPoolPropagateConfigSkipsClosedRunners pins the teardown rule: a
// runner evicted concurrently with the propagation reports the closed
// sentinel, which is classified as "gone", not as a propagation failure.
func TestRunnerPoolPropagateConfigSkipsClosedRunners(t *testing.T) {
	closed := &run.Runner{Deps: &run.Deps{Home: t.TempDir()}}
	if cerr := closed.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	err := closed.LoadConfig(&appcfg.Root{})
	if !run.IsRunnerClosed(err) {
		t.Fatalf("closed runner load = %v, want the closed sentinel", err)
	}
	if run.IsRunnerClosed(nil) {
		t.Fatal("nil must not classify as closed")
	}

	env, _, _ := newPoolTestEnv(t)
	env.poolOnce.Do(func() { env.pool = NewRunnerPool(env, 4) })
	env.pool.Close()
	// The pool is empty after Close: propagating is a no-op, not an error.
	if err := env.pool.PropagateConfig(context.Background(), &appcfg.Root{}); err != nil {
		t.Fatalf("empty pool propagation = %v, want nil", err)
	}
}
