package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

func TestPerRunHooksOverrideSharedStateWithoutCrossRouting(t *testing.T) {
	st := NewState(t.TempDir())
	var fallbackSteps, firstSteps, secondSteps atomic.Int32
	st.SetStepHook(func(context.Context, StepEvent) { fallbackSteps.Add(1) })
	st.SetNetworkApprovalPromptHook(func(context.Context, string, map[string]any, safety.NetworkApprovalRequest) (safety.NetworkApprovalDecision, error) {
		return safety.NetworkApprovalDeny, nil
	})
	st.SetSubagentApprovalHook(func(ctx context.Context, _ *RequiresActionError) (context.Context, error) {
		return context.WithValue(ctx, ctxKey("hook_result"), "fallback"), nil
	})

	first := WithStepHook(context.Background(), func(context.Context, StepEvent) { firstSteps.Add(1) })
	first = WithNetworkApprovalPromptHook(first, func(context.Context, string, map[string]any, safety.NetworkApprovalRequest) (safety.NetworkApprovalDecision, error) {
		return safety.NetworkApprovalAllowOnce, nil
	})
	first = WithSubagentApprovalHook(first, func(ctx context.Context, _ *RequiresActionError) (context.Context, error) {
		return context.WithValue(ctx, ctxKey("hook_result"), "first"), nil
	})
	second := WithStepHook(context.Background(), func(context.Context, StepEvent) { secondSteps.Add(1) })
	second = WithNetworkApprovalPromptHook(second, func(context.Context, string, map[string]any, safety.NetworkApprovalRequest) (safety.NetworkApprovalDecision, error) {
		return safety.NetworkApprovalAllowForSession, nil
	})
	second = WithSubagentApprovalHook(second, func(ctx context.Context, _ *RequiresActionError) (context.Context, error) {
		return context.WithValue(ctx, ctxKey("hook_result"), "second"), nil
	})

	StepHookFromContext(first, st)(first, StepEvent{})
	StepHookFromContext(second, st)(second, StepEvent{})
	StepHookFromContext(context.Background(), st)(context.Background(), StepEvent{})
	if firstSteps.Load() != 1 || secondSteps.Load() != 1 || fallbackSteps.Load() != 1 {
		t.Fatalf("step routing first=%d second=%d fallback=%d", firstSteps.Load(), secondSteps.Load(), fallbackSteps.Load())
	}
	if got, _ := NetworkApprovalPromptHookFromContext(first, st)(first, "", nil, safety.NetworkApprovalRequest{}); got != safety.NetworkApprovalAllowOnce {
		t.Fatalf("first network decision = %q", got)
	}
	if got, _ := NetworkApprovalPromptHookFromContext(second, st)(second, "", nil, safety.NetworkApprovalRequest{}); got != safety.NetworkApprovalAllowForSession {
		t.Fatalf("second network decision = %q", got)
	}
	if got, _ := NetworkApprovalPromptHookFromContext(context.Background(), st)(context.Background(), "", nil, safety.NetworkApprovalRequest{}); got != safety.NetworkApprovalDeny {
		t.Fatalf("fallback network decision = %q", got)
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want string
	}{{"first", first, "first"}, {"second", second, "second"}, {"fallback", context.Background(), "fallback"}} {
		resolved, err := SubagentApprovalHookFromContext(tc.ctx, st)(tc.ctx, nil)
		if err != nil || resolved.Value(ctxKey("hook_result")) != tc.want {
			t.Fatalf("%s subagent routing value=%v err=%v", tc.name, resolved.Value(ctxKey("hook_result")), err)
		}
	}
}

func TestForkChildNotebookStateCopiesReadCache(t *testing.T) {
	tmp := t.TempDir()
	nb := filepath.Join(tmp, "note.md")
	if err := os.WriteFile(nb, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := NewState(tmp)
	fi, err := os.Stat(nb)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(nb)
	if err != nil {
		t.Fatal(err)
	}
	parent.RememberRead(nb, fi.ModTime(), fi.Size(), raw)

	child := ForkChildNotebookState(parent, nb)
	if child == nil {
		t.Fatal("nil child")
	}
	rs, ok := child.GetReadState(nb)
	if !ok {
		t.Fatal("expected read state on child")
	}
	if rs.Size != fi.Size() {
		t.Fatalf("size=%d want %d", rs.Size, fi.Size())
	}
}

func TestForkChildNotebookStateEmptyPath(t *testing.T) {
	st := NewState(t.TempDir())
	if x := ForkChildNotebookState(st, "  "); x == nil {
		t.Fatal("expected non-nil")
	}
}

func TestForkChildNotebookStateNoParentEntry(t *testing.T) {
	tmp := t.TempDir()
	nb := filepath.Join(tmp, "nope.md")
	parent := NewState(tmp)
	ch := ForkChildNotebookState(parent, nb)
	if len(ch.AllowedRoots()) != 1 {
		t.Fatalf("roots=%d", len(ch.AllowedRoots()))
	}
	if _, ok := ch.GetReadState(nb); ok {
		t.Fatal("unexpected read state")
	}
}

// The helpers below were production functions that only the tests in this
// package ever called: each is a thin composition of live code. They live
// here so the production files carry no unused code while the tests keep
// exercising the live functions underneath.

func ForkChildNotebookState(parent *State, notebookAbs string) *State {
	notebookAbs = strings.TrimSpace(notebookAbs)
	if notebookAbs == "" {
		return NewState()
	}
	notebookAbs = filepath.Clean(notebookAbs)
	child := NewState(notebookAbs)
	if parent == nil {
		return child
	}
	parent.mu.Lock()
	rs, ok := parent.readFiles[notebookAbs]
	parent.mu.Unlock()
	if !ok {
		return child
	}
	child.mu.Lock()
	child.readFiles[notebookAbs] = rs
	child.mu.Unlock()
	return child
}

// ---- session-lifecycle cleanup ----
// The tests below pin the cleanup half of the state's session lifecycle: the
// maps a long-lived gateway accumulates forever unless something collects them,
// and the collection rules that keep it from taking something a live
// conversation still reads.

func newLifecycleTestState(t *testing.T) *State {
	t.Helper()
	st := NewState()
	if st == nil {
		t.Fatal("NewState")
	}
	return st
}

// The helpers below write the unexported maps directly (same package) with the
// timestamps a real writer would have recorded, so the tests pin the cleanup
// rules without reproducing each writer's call path.
func setContextShotForTest(st *State, sid string) {
	st.mu.Lock()
	st.contextShots[sid] = json.RawMessage(`{}`)
	st.mu.Unlock()
}

func setNetworkDecisionForTest(st *State, sid string) {
	st.mu.Lock()
	if st.networkSessionDecisions == nil {
		st.networkSessionDecisions = make(map[string]map[networkSessionKey]bool)
	}
	st.networkSessionDecisions[sid] = make(map[networkSessionKey]bool)
	st.mu.Unlock()
}

func setRuntimeModeForTest(st *State, sid string) {
	st.mu.Lock()
	if st.sessionRuntimeModes == nil {
		st.sessionRuntimeModes = make(map[string]runtimeSessionMode)
	}
	st.sessionRuntimeModes[sid] = runtimeSessionMode{mode: "agent"}
	st.mu.Unlock()
}

func recordSpillForTest(st *State, runID string, at time.Time) {
	st.mu.Lock()
	if st.toolSpills == nil {
		st.toolSpills = make(map[string][]ToolResultSpill)
	}
	st.toolSpills[runID] = append(st.toolSpills[runID], ToolResultSpill{RunID: runID, Path: "/tmp/x", CreatedAt: at})
	st.mu.Unlock()
}

func expireApprovedWriteForTest(st *State, path string) {
	st.mu.Lock()
	st.approvedWrites[path] = time.Now().Add(-2 * approvedWriteTTL)
	st.mu.Unlock()
}

func rememberTouchForTest(st *State, path string, at time.Time) {
	st.mu.Lock()
	st.readFiles[path] = ReadState{AbsPath: path, ReadAt: at}
	st.mu.Unlock()
}

func TestSessionIDsListsEverySessionKeyedMap(t *testing.T) {
	st := newLifecycleTestState(t)
	if ids := st.SessionIDs(); len(ids) != 0 {
		t.Fatalf("a fresh state reports %v", ids)
	}
	setContextShotForTest(st, "s-shot")
	st.RecordToolResultSpill(ToolResultSpill{SessionID: "s-spill", RunID: "r-2", Path: "/tmp/x"})
	setNetworkDecisionForTest(st, "s-net")
	setRuntimeModeForTest(st, "s-mode")

	got := map[string]bool{}
	for _, sid := range st.SessionIDs() {
		got[sid] = true
	}
	for _, want := range []string{"s-shot", "s-spill", "s-net", "s-mode"} {
		if !got[want] {
			t.Fatalf("SessionIDs = %v, missing %s", got, want)
		}
	}
	if len(got) != 4 {
		t.Fatalf("SessionIDs = %v, want exactly the four sessions", got)
	}
}

func TestDropSessionsForgetsEverythingTheSessionLeft(t *testing.T) {
	st := newLifecycleTestState(t)
	setContextShotForTest(st, "gone")
	st.RecordToolResultSpill(ToolResultSpill{SessionID: "gone", RunID: "r-gone", Path: "/tmp/g"})
	st.RecordToolResultSpill(ToolResultSpill{SessionID: "kept", RunID: "r-kept", Path: "/tmp/k"})
	setNetworkDecisionForTest(st, "gone")
	setRuntimeModeForTest(st, "gone")

	if dropped := st.DropSessions([]string{"gone"}); dropped != 1 {
		t.Fatalf("DropSessions reported %d, want 1", dropped)
	}
	// Idempotent, and an unknown id is a no-op.
	if dropped := st.DropSessions([]string{"gone", "never-here"}); dropped != 0 {
		t.Fatalf("second DropSessions reported %d, want 0", dropped)
	}
	if ids := st.SessionIDs(); len(ids) != 1 || ids[0] != "kept" {
		t.Fatalf("SessionIDs after drop = %v, want only the kept session", ids)
	}
	// The run bucket of the dropped session went with it; the live one's stayed.
	if spills := st.ToolResultSpills("kept", ""); len(spills) != 1 {
		t.Fatalf("the kept session's spills = %d, want 1", len(spills))
	}
}

func TestTrimRunKeyedDropsStaleBucketsAndExpiredWriteGrants(t *testing.T) {
	st := newLifecycleTestState(t)
	old := time.Now().Add(-2 * time.Hour)
	recordSpillForTest(st, "r-old", old)
	recordSpillForTest(st, "r-fresh", time.Now())
	st.RememberApprovedWrite("/approved/path")
	// A grant nobody has read since it expired: the read path cleans its own,
	// the trim is for the ones between reads.
	st.RememberApprovedWrite("/expired/path")
	expireApprovedWriteForTest(st, "/expired/path")

	if st.ApprovedWrite("/approved/path") != true {
		t.Fatal("a fresh write grant must hold")
	}
	dropped := st.TrimRunKeyed(time.Hour)
	if dropped != 2 {
		t.Fatalf("TrimRunKeyed dropped %d, want exactly the stale spill bucket and the expired grant", dropped)
	}
	if st.ApprovedWrite("/expired/path") {
		t.Fatal("the expired grant survived the trim")
	}
	if spills := st.ToolResultSpills("", "r-old"); len(spills) != 0 {
		t.Fatal("the stale run bucket survived")
	}
	if spills := st.ToolResultSpills("", "r-fresh"); len(spills) == 0 {
		t.Fatal("the fresh run bucket was trimmed")
	}
}

func TestApprovedWriteExpiresWithItsTurn(t *testing.T) {
	st := newLifecycleTestState(t)
	st.RememberApprovedWrite("/p")
	if !st.ApprovedWrite("/p") {
		t.Fatal("a grant must be valid immediately")
	}
	expireApprovedWriteForTest(st, "/p")
	if st.ApprovedWrite("/p") {
		t.Fatal("a grant older than the turn horizon must have expired")
	}
	// Expiry reads as absence, not as a poisoned entry.
	if st.ApprovedWrite("/never-granted") {
		t.Fatal("an ungranted path read as granted")
	}
}

func TestTrimStaleReadStatesKeepsFreshBaselines(t *testing.T) {
	st := newLifecycleTestState(t)
	rememberTouchForTest(st, "/old/file", time.Now().Add(-2*time.Hour))
	rememberTouchForTest(st, "/new/file", time.Now())
	if dropped := st.TrimStaleReadStates(time.Hour); dropped != 1 {
		t.Fatalf("TrimStaleReadStates dropped %d, want 1", dropped)
	}
	if _, ok := st.GetReadState("/new/file"); !ok {
		t.Fatal("the fresh baseline was trimmed")
	}
	if _, ok := st.GetReadState("/old/file"); ok {
		t.Fatal("the stale baseline survived")
	}
}

func TestToolStepStartedAtIsAbsentWithoutADispatcher(t *testing.T) {
	if got := ToolStepStartedAtFromContext(context.Background()); !got.IsZero() {
		t.Fatalf("start without WithToolStepStartedAt = %v, want zero", got)
	}
}

func TestNamedReadObservers(t *testing.T) {
	st := NewState(t.TempDir())
	if st.ReadObserver() != nil {
		t.Fatal("no observer installed, ReadObserver must be nil")
	}
	var order []string
	st.SetNamedReadObserver("zeta", func(context.Context, string, []byte) {
		order = append(order, "zeta")
	})
	st.SetNamedReadObserver("alpha", func(context.Context, string, []byte) {
		order = append(order, "alpha")
	})
	observer := st.ReadObserver()
	if observer == nil {
		t.Fatal("observers installed, ReadObserver must not be nil")
	}
	observer(context.Background(), "/x", []byte("body"))
	if len(order) != 2 || order[0] != "alpha" || order[1] != "zeta" {
		t.Fatalf("observer order=%v want [alpha zeta] (name order)", order)
	}
	// A fan-out already in hand is not affected by a later removal.
	st.SetNamedReadObserver("alpha", nil)
	observer(context.Background(), "/x", []byte("body"))
	if len(order) != 4 || order[2] != "alpha" || order[3] != "zeta" {
		t.Fatalf("held fan-out order=%v want [alpha zeta alpha zeta]", order)
	}
	if st.ReadObserver() == nil {
		t.Fatal("one observer still installed, ReadObserver must not be nil")
	}
	// Removing the last observer returns ReadObserver to nil.
	st.SetNamedReadObserver("zeta", nil)
	if st.ReadObserver() != nil {
		t.Fatal("all observers removed, ReadObserver must be nil")
	}
	// The legacy single-observer front door still works and joins the fan-out.
	var legacy int
	st.SetReadObserver(func(context.Context, string, []byte) { legacy++ })
	if observer := st.ReadObserver(); observer == nil {
		t.Fatal("legacy SetReadObserver must install an observer")
	} else {
		observer(context.Background(), "/x", []byte("body"))
	}
	if legacy != 1 {
		t.Fatalf("legacy observer calls=%d want 1", legacy)
	}
	// ...and clears it again with nil.
	st.SetReadObserver(nil)
	if st.ReadObserver() != nil {
		t.Fatal("legacy nil must remove the observer")
	}
}

// A typed run whose definition is custom — configuration, not a type name — has
// its tool policy on the context. One that carries the source without the
// policy is a run that lost it, and the static per-type table knows nothing
// about a custom name, so it must be recognised and refused.
func TestCustomSubagentPolicyMissing(t *testing.T) {
	typed := WithSubagentType(context.Background(), "repo-scout")
	custom := WithSubagentDefinitionSource(typed, SubagentDefinitionSourceCustom)
	cases := []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"not a subagent", context.Background(), false},
		{"built-in typed run", WithSubagentDefinitionSource(WithSubagentType(context.Background(), "explore"), "built-in"), false},
		{"typed run, no source", typed, false},
		{"custom source, no type", WithSubagentDefinitionSource(context.Background(), SubagentDefinitionSourceCustom), false},
		{"custom source, no policy", custom, true},
		{"custom source with policy", WithSubagentToolPolicy(custom, SubagentToolPolicy{Mode: appcfg.SubagentToolsReadOnly}), false},
	}
	for _, tc := range cases {
		if got := CustomSubagentPolicyMissing(tc.ctx); got != tc.want {
			t.Errorf("%s: CustomSubagentPolicyMissing = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsWithinDirAcceptsDotDotNames(t *testing.T) {
	dir := t.TempDir()
	if !isWithinDir(filepath.Join(dir, "..idea.md"), dir) {
		t.Fatal("a file named ..idea.md directly inside the plan directory is a sanctioned plan write")
	}
	if isWithinDir(filepath.Join(dir, "sub", "..idea.md"), dir) {
		t.Fatal("a subdirectory must stay out")
	}
	if isWithinDir(dir, dir) {
		t.Fatal("the directory itself is not a file inside it")
	}
}

// TestWorkingDirPrefersTheTaskWorkspace pins where a batch turn's tools act:
// the execution's workspace, while the project root stays the conversation's
// identity for everything that resolves against it.
func TestWorkingDirPrefersTheTaskWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "tasks", "tsk-1", "1")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := WithProjectRoot(context.Background(), root)
	ctx = WithTaskWorkspace(ctx, workspace)

	got, err := WorkingDir(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != workspace {
		t.Fatalf("WorkingDir = %q, want the task workspace %q", got, workspace)
	}
	if ProjectRootFromContext(ctx) != root {
		t.Fatalf("the project root changed to %q", ProjectRootFromContext(ctx))
	}
	resolved, err := ResolvePath(ctx, "outputs/report.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(workspace, "outputs", "report.xlsx"); resolved != want {
		t.Fatalf("ResolvePath = %q, want %q", resolved, want)
	}

	// A conversation without a task workspace is unchanged.
	plain, err := WorkingDir(WithProjectRoot(context.Background(), root))
	if err != nil {
		t.Fatal(err)
	}
	if plain != root {
		t.Fatalf("WorkingDir without a task workspace = %q, want the project root %q", plain, root)
	}
	if TaskWorkspaceFromContext(context.Background()) != "" {
		t.Fatal("a plain context carries a task workspace")
	}
}

// TestGuardWriteConfinesBatchWrites pins the file-tool half of a batch task's
// write boundary (plans/150): the task's own directory is writable, everything
// else — the project root it sits in, a traversal, a symlink out of it — is not,
// and a turn without a task workspace keeps today's behavior exactly.
func TestGuardWriteConfinesBatchWrites(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "project")
	workspace := filepath.Join(project, "tasks", "T", "1")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{filepath.Join(workspace, "outputs"), outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(workspace, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Errorf("symlink: %v", err)
	}
	st := NewState(project)
	batchCtx := WithTaskWorkspace(context.Background(), workspace)

	for _, path := range []string{
		filepath.Join(workspace, "outputs", "a.txt"),
		filepath.Join(workspace, "new.txt"),
	} {
		if err := st.GuardWrite(batchCtx, path); err != nil {
			t.Fatalf("write inside the task workspace must pass: %v", err)
		}
	}
	for name, path := range map[string]string{
		"the project root":        filepath.Join(project, "shared.txt"),
		"a sibling task":          filepath.Join(project, "tasks", "T2", "1", "x.txt"),
		"a traversal":             filepath.Join(workspace, "..", "..", "..", "x.txt"),
		"a symlink out of the ws": filepath.Join(link, "x.txt"),
	} {
		if err := st.GuardWrite(batchCtx, path); err == nil {
			t.Fatalf("write to %s must be refused", name)
		}
	}
	// No task workspace: unchanged behavior, whatever the path.
	ordinaryCtx := WithProjectRoot(context.Background(), project)
	if err := st.GuardWrite(ordinaryCtx, filepath.Join(project, "shared.txt")); err != nil {
		t.Fatalf("a non-batch turn keeps its ordinary write guard: %v", err)
	}
}

// TestReadPathDeniedInForBatch pins the file-tool half of a batch task's read
// boundary (plans/150): the task's workspace and the shared skill roots are
// readable, the project root it sits in, a sibling task and the Forebrain home
// are not, and a turn without a task workspace falls back to the runner's own
// deny list.
func TestReadPathDeniedInForBatch(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "project")
	workspace := filepath.Join(project, "tasks", "T", "1")
	sibling := filepath.Join(project, "tasks", "T2", "1")
	home := filepath.Join(base, "home")
	skillRoot := filepath.Join(home, "skills", "demo")
	for _, dir := range []string{workspace, sibling, skillRoot, filepath.Join(home, "other")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	st := NewState(project)
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "demo", RootDir: skillRoot}})
	batchCtx := WithTaskWorkspace(context.Background(), workspace)

	for name, path := range map[string]string{
		"the workspace":  filepath.Join(workspace, "inputs", "a.txt"),
		"a shared skill": filepath.Join(skillRoot, "SKILL.md"),
	} {
		if st.ReadPathDeniedIn(batchCtx, path) {
			t.Fatalf("%s must stay readable", name)
		}
	}
	for name, path := range map[string]string{
		"the project root": filepath.Join(project, "shared.txt"),
		"a sibling task":   filepath.Join(sibling, "secret.txt"),
		"the home":         filepath.Join(home, "other", "private.txt"),
	} {
		if !st.ReadPathDeniedIn(batchCtx, path) {
			t.Fatalf("reading %s must be denied", name)
		}
	}
	if !st.ReadDirPrunableIn(batchCtx, sibling) {
		t.Fatal("a sibling task's directory is prunable in a batch walk")
	}
	if st.ReadDirPrunableIn(batchCtx, project) || st.ReadDirPrunableIn(batchCtx, workspace) {
		t.Fatal("the project root is walkable down to the workspace, and the workspace itself is not prunable")
	}

	// Without a task workspace the runner's deny list answers, exactly as before.
	denied := filepath.Join(project, "private")
	st.SetReadPolicy([]string{denied}, nil, nil, project)
	ordinaryCtx := WithProjectRoot(context.Background(), project)
	if !st.ReadPathDeniedIn(ordinaryCtx, filepath.Join(denied, "a.txt")) {
		t.Fatal("a non-batch turn must fall back to the runner's deny list")
	}
	if st.ReadPathDeniedIn(ordinaryCtx, filepath.Join(project, "shared.txt")) {
		t.Fatal("a non-batch turn must not deny an undeniied path")
	}
}
