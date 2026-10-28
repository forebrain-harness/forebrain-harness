package assembly

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// TestRulesHookReadsWorkingDirFromContext pins that the FOREBRAIN.md rules are
// resolved against the session project root on the context, not the process
// directory a gateway serves many projects from.
func TestRulesHookReadsWorkingDirFromContext(t *testing.T) {
	home := t.TempDir()
	wsRoot := t.TempDir()

	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "FOREBRAIN.md"), []byte("PROJECT RULES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, safety.Project{Root: project, VersionControlled: true}); err != nil {
		t.Fatal(err)
	}

	// A different project the process happens to be sitting in.
	processDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(processDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(processDir, "FOREBRAIN.md"), []byte("PROCESS RULES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, safety.Project{Root: processDir, VersionControlled: true}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(processDir)

	p := NewPreHook(nil, home, wsRoot)
	ctx := tool.WithProjectRoot(context.Background(), project)
	out, err := p.run(ctx, "pre", hook.HookContext{Trigger: "user", SessionID: "s"}, "hello")
	if err != nil {
		t.Fatalf("rules hook: %v", err)
	}
	if !strings.Contains(out.SystemAddendum, "PROJECT RULES") {
		t.Fatalf("rules injection did not read the session working dir: %q", out.SystemAddendum)
	}
	if strings.Contains(out.SystemAddendum, "PROCESS RULES") {
		t.Fatalf("rules injection read the process directory: %q", out.SystemAddendum)
	}
}

// A rule file whose name starts with ".." is inside the working directory, so
// the rules list names it relatively — the absolute path never reaches the
// prompt for a file that lives in the project.
func TestDisplayRulePathKeepsDotDotNamesRelative(t *testing.T) {
	wd := t.TempDir()
	path := filepath.Join(wd, "..env")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := displayRulePath(path, wd); got != "..env" {
		t.Fatalf("displayRulePath(..env) = %q, want %q", got, "..env")
	}
}

// TestBatchConversationsGetNoWorkspacePersonaFiles pins the batch half of the
// rules injection: a batch conversation inherits the project rules but not
// the workspace persona files (AGENTS/SOUL/USER.md), which are personal
// collaboration preferences. The same working directory read as office keeps
// both, which also proves the cache key tells the two types apart.
func TestBatchConversationsGetNoWorkspacePersonaFiles(t *testing.T) {
	home := t.TempDir()
	wsRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsRoot, "SOUL.md"), []byte("# SOUL\n\npersonal preferences"), 0o644); err != nil {
		t.Fatal(err)
	}

	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "FOREBRAIN.md"), []byte("PROJECT RULES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, safety.Project{Root: project, VersionControlled: true}); err != nil {
		t.Fatal(err)
	}

	p := NewPreHook(nil, home, wsRoot)
	ctxBase := tool.WithProjectRoot(context.Background(), project)

	out, err := p.run(tool.WithProjectType(ctxBase, state.ProjectTypeBatch), "pre", hook.HookContext{Trigger: "user", SessionID: "s"}, "run the batch")
	if err != nil {
		t.Fatalf("batch rules hook: %v", err)
	}
	if strings.Contains(out.SystemAddendum, "## SOUL.md") {
		t.Fatalf("batch addendum injects the workspace persona: %q", out.SystemAddendum)
	}
	if !strings.Contains(out.SystemAddendum, "PROJECT RULES") {
		t.Fatalf("batch addendum lost the project rules: %q", out.SystemAddendum)
	}

	// Same hook, same working directory, office conversation: both sources.
	out, err = p.run(tool.WithProjectType(ctxBase, state.ProjectTypeOffice), "pre", hook.HookContext{Trigger: "user", SessionID: "s"}, "draft the report")
	if err != nil {
		t.Fatalf("office rules hook: %v", err)
	}
	if !strings.Contains(out.SystemAddendum, "## SOUL.md") || !strings.Contains(out.SystemAddendum, "PROJECT RULES") {
		t.Fatalf("office addendum lost a source: %q", out.SystemAddendum)
	}
}

// TestTaskContextReachesTheTurnAddendum pins the batch turn's environment: the
// task context is injected with the rules, still injected when the turn
// carries no text of its own (a first message that was only an attachment),
// and injected even when the project has no rules at all.
func TestTaskContextReachesTheTurnAddendum(t *testing.T) {
	home := t.TempDir()
	wsRoot := t.TempDir()
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "FOREBRAIN.md"), []byte("PROJECT RULES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, safety.Project{Root: project, VersionControlled: true}); err != nil {
		t.Fatal(err)
	}
	p := NewPreHook(nil, home, wsRoot)
	const taskContext = "task_id: tsk-1\nexecution: 1\ntask_workspace: /repo/tasks/tsk-1/1\ninput_files:\n- inputs/a.csv\ndeliverables: outputs/\nscratch: .work/"
	base := tool.WithProjectType(tool.WithProjectRoot(context.Background(), project), state.ProjectTypeBatch)
	ctx := tool.WithTaskContext(base, taskContext)

	out, err := p.run(ctx, "pre", hook.HookContext{Trigger: "user", SessionID: "s"}, "count the rows")
	if err != nil {
		t.Fatalf("batch rules hook: %v", err)
	}
	want := "<forebrain_task_context>\n" + taskContext + "\n</forebrain_task_context>"
	if !strings.Contains(out.SystemAddendum, want) {
		t.Fatalf("addendum = %q, want it to carry %q", out.SystemAddendum, want)
	}
	if !strings.Contains(out.SystemAddendum, "PROJECT RULES") {
		t.Fatalf("addendum lost the project rules: %q", out.SystemAddendum)
	}

	// A first turn that carried only an attachment has no text: the task
	// context still reaches the model.
	out, err = p.run(ctx, "pre", hook.HookContext{Trigger: "user", SessionID: "s"}, "")
	if err != nil {
		t.Fatalf("attachment-only batch turn: %v", err)
	}
	if !strings.Contains(out.SystemAddendum, want) {
		t.Fatalf("an attachment-only first turn lost the task context: %q", out.SystemAddendum)
	}

	// Without a task context, the empty-text early return is unchanged — for a
	// batch conversation and for every other one.
	for _, projectType := range []string{state.ProjectTypeBatch, state.ProjectTypeOffice, state.ProjectTypeCoding} {
		plain := tool.WithProjectType(tool.WithProjectRoot(context.Background(), project), projectType)
		out, err = p.run(plain, "pre", hook.HookContext{Trigger: "user", SessionID: "s"}, "")
		if err != nil {
			t.Fatalf("%s empty turn: %v", projectType, err)
		}
		if out.SystemAddendum != "" {
			t.Fatalf("%s empty turn injected %q", projectType, out.SystemAddendum)
		}
		out, err = p.run(plain, "pre", hook.HookContext{Trigger: "user", SessionID: "s"}, "/status")
		if err != nil {
			t.Fatalf("%s slash turn: %v", projectType, err)
		}
		if out.SystemAddendum != "" {
			t.Fatalf("%s slash turn injected %q", projectType, out.SystemAddendum)
		}
	}
}

// TestTaskWorkspaceStillResolvesRulesFromTheProject pins that a batch
// execution working in <project>/tasks/<id>/<n> still loads the project's own
// FOREBRAIN.md and its trust decision: the workspace is where the tools act,
// never what the project is.
func TestTaskWorkspaceStillResolvesRulesFromTheProject(t *testing.T) {
	home := t.TempDir()
	wsRoot := t.TempDir()
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "FOREBRAIN.md"), []byte("PROJECT RULES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, safety.Project{Root: project, VersionControlled: true}); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(project, "tasks", "tsk-1", "1")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	p := NewPreHook(nil, home, wsRoot)
	ctx := tool.WithProjectRoot(context.Background(), project)
	ctx = tool.WithProjectType(ctx, state.ProjectTypeBatch)
	ctx = tool.WithTaskWorkspace(ctx, workspace)

	out, err := p.run(ctx, "pre", hook.HookContext{Trigger: "user", SessionID: "s"}, "count the rows")
	if err != nil {
		t.Fatalf("rules hook: %v", err)
	}
	if !strings.Contains(out.SystemAddendum, "PROJECT RULES") {
		t.Fatalf("a task workspace lost the project rules: %q", out.SystemAddendum)
	}
	if strings.Contains(out.SystemAddendum, "project not trusted") {
		t.Fatalf("a task workspace was resolved as an untrusted project: %q", out.SystemAddendum)
	}
}
