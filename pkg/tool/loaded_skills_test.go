package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

func TestLoadedSkillCatalogAllowsReadsOutsideAllowedRoots(t *testing.T) {
	workspace := t.TempDir()
	sharedSkill := filepath.Join(t.TempDir(), "skills", "shared-review")
	resource := filepath.Join(sharedSkill, "references", "guide.md")
	if err := os.MkdirAll(filepath.Dir(resource), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resource, []byte("shared guide"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := NewState(workspace)
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "shared-review", RootDir: sharedSkill}})
	// Catalog-backed skill reads override a broader sandbox deny_read rule.
	st.SetReadPolicy([]string{filepath.Dir(sharedSkill)}, nil, nil, workspace)
	approvalCalled := false
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		approvalCalled = true
		return "approval", true, nil
	})
	tool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatal(err)
	}
	out, err := tool.Handle(context.Background(), `{"file_path":`+quoteJSON(resource)+`}`)
	if err != nil {
		t.Fatalf("read loaded skill resource: %v", err)
	}
	if approvalCalled {
		t.Fatal("loaded skill read must not request approval")
	}
	if !strings.Contains(out.(string), "shared guide") {
		t.Fatalf("unexpected read output: %q", out)
	}
}

func TestShortenedSkillPathRepairsDroppedIntermediateDirectory(t *testing.T) {
	base := t.TempDir()
	// Real system-skill layout: a bundle directory between .system and the skill.
	skillRoot := filepath.Join(base, "skills", ".system", "caveman", "caveman-commit")
	skillFile := filepath.Join(skillRoot, "SKILL.md")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillFile, []byte("# caveman-commit"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := NewState(t.TempDir())
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "caveman-commit", RootDir: skillRoot}})

	// The mistake seen in the logs: the bundle directory is dropped because the
	// skill name reads like the directory that holds it.
	shortened := filepath.Join(base, "skills", ".system", "caveman-commit", "SKILL.md")
	got, item, ok := st.resolveShortenedSkillPath(shortened)
	if !ok {
		t.Fatal("expected a repair for the dropped intermediate directory")
	}
	if got != normalizeRootPath(skillFile) {
		t.Fatalf("repaired path = %q, want %q", got, normalizeRootPath(skillFile))
	}
	if item.Name != "caveman-commit" {
		t.Fatalf("repair reported skill %q", item.Name)
	}
	// Dropping every directory down to the skills root is the same mistake.
	if _, _, ok := st.resolveShortenedSkillPath(filepath.Join(base, "skills", "caveman-commit", "SKILL.md")); !ok {
		t.Fatal("expected a repair when more directories are dropped")
	}

	// An existing path is never rewritten.
	if _, _, ok := st.resolveShortenedSkillPath(skillFile); ok {
		t.Fatal("existing path must not be rewritten")
	}
	// A missing path that matches no skill name stays unresolved.
	if _, _, ok := st.resolveShortenedSkillPath(filepath.Join(base, "skills", ".system", "no-such-skill", "SKILL.md")); ok {
		t.Fatal("path matching no skill name must stay unresolved")
	}
	// A repair that would land on a directory is not offered: read_file needs a file.
	if _, _, ok := st.resolveShortenedSkillPath(filepath.Join(base, "skills", ".system", "caveman-commit")); ok {
		t.Fatal("repair must not resolve to a directory")
	}
	// A file missing from the skill root is a missing file, not a repair.
	if _, _, ok := st.resolveShortenedSkillPath(filepath.Join(base, "skills", ".system", "caveman-commit", "NOPE.md")); ok {
		t.Fatal("repair must not invent a file the skill does not have")
	}
}

// TestShortenedSkillPathLeavesUnrelatedPathsAlone pins the boundary that makes
// the repair safe to apply silently: a path is only a shortened catalog path
// when the directory it puts the skill in really does contain that skill. A
// project file under a directory that merely shares a skill's name must fail
// as missing, never be answered with an unrelated file out of the catalog.
func TestShortenedSkillPathLeavesUnrelatedPathsAlone(t *testing.T) {
	base := t.TempDir()
	skillRoot := filepath.Join(base, "skills", ".system", "planning", "plan")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "README.md"), []byte("skill readme"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := NewState(t.TempDir())
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "plan", RootDir: skillRoot}})

	unrelated := filepath.Join(base, "project", "docs", "plan", "README.md")
	if got, _, ok := st.resolveShortenedSkillPath(unrelated); ok {
		t.Fatalf("unrelated %q must stay unresolved, got %q", unrelated, got)
	}
}

// TestShortenedSkillPathCannotLeaveTheSkillRoot pins that the repair grants no
// read the ordinary catalog read does not: a symlink inside a skill root still
// has to resolve inside that root.
func TestShortenedSkillPathCannotLeaveTheSkillRoot(t *testing.T) {
	base := t.TempDir()
	skillRoot := filepath.Join(base, "skills", ".system", "bundle", "demo")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(skillRoot, "leak.txt")); err != nil {
		t.Fatal(err)
	}
	st := NewState(t.TempDir())
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "demo", RootDir: skillRoot}})

	escape := filepath.Join(base, "skills", ".system", "demo", "leak.txt")
	if got, _, ok := st.resolveShortenedSkillPath(escape); ok {
		t.Fatalf("repair escaped the skill root: %q", got)
	}
	// The same read through the ordinary catalog path is refused too, so the
	// two resolvers agree on the boundary.
	if got, ok := st.ResolveLoadedSkillRead(filepath.Join(skillRoot, "leak.txt")); ok {
		t.Fatalf("catalog read escaped the skill root: %q", got)
	}
}

// TestShortenedSkillPathRefusesAmbiguousRepairs pins that two skills that both
// explain one shortened path is an ambiguity, not a guess between them: the
// read fails as missing instead of silently answering with one of the two.
func TestShortenedSkillPathRefusesAmbiguousRepairs(t *testing.T) {
	base := t.TempDir()
	skills := filepath.Join(base, "skills")
	// alpha lives in one bundle under skills/, beta in a bundle under
	// skills/alpha/, so the path below names both skills and both roots hold a
	// file the trailing segments could mean.
	alphaRoot := filepath.Join(skills, "pack", "alpha")
	betaRoot := filepath.Join(skills, "alpha", "pack", "beta")
	for _, file := range []string{
		filepath.Join(alphaRoot, "beta", "SKILL.md"),
		filepath.Join(betaRoot, "SKILL.md"),
	} {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("body"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st := NewState(t.TempDir())
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{
		{Name: "alpha", RootDir: alphaRoot},
		{Name: "beta", RootDir: betaRoot},
	})
	ambiguous := filepath.Join(skills, "alpha", "beta", "SKILL.md")
	if got, _, ok := st.resolveShortenedSkillPath(ambiguous); ok {
		t.Fatalf("ambiguous path must stay unresolved, got %q", got)
	}
}

// TestReadFileNamesTheSkillWhenAPathIsTooWrongToRepair covers the reads the
// repair deliberately refuses: a skill quoted under a skills root that does not
// hold it cannot be told apart from an unrelated missing file, so nothing is
// read — but the model still has to learn where that skill is, or it has no way
// back from a path the catalog is the only reason it knows.
func TestReadFileNamesTheSkillWhenAPathIsTooWrongToRepair(t *testing.T) {
	base := t.TempDir()
	skillRoot := filepath.Join(base, "forebrain", "skills", ".system", "caveman", "caveman-commit")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := NewState(t.TempDir())
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "caveman-commit", RootDir: skillRoot}})
	tool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatal(err)
	}
	// A different skills root entirely: nothing here says this path was ever
	// reaching into the skill above, so it must not be silently redirected.
	elsewhere := filepath.Join(base, "claude", "skills", "caveman-commit", "SKILL.md")
	_, err = tool.Handle(context.Background(), `{"file_path":`+quoteJSON(elsewhere)+`}`)
	if err == nil {
		t.Fatal("a path outside the skill's own tree must not be repaired")
	}
	if !strings.Contains(err.Error(), normalizeRootPath(skillRoot)) {
		t.Fatalf("error does not name the skill root: %v", err)
	}
	// An ordinary missing file says nothing about skills.
	_, err = tool.Handle(context.Background(), `{"file_path":`+quoteJSON(filepath.Join(base, "project", "notes.md"))+`}`)
	if err == nil || strings.Contains(err.Error(), "skill") {
		t.Fatalf("unrelated missing file error = %v", err)
	}
}

func TestReadFileRepairsDroppedSkillDirectoryAndSaysSo(t *testing.T) {
	base := t.TempDir()
	skillRoot := filepath.Join(base, "skills", ".system", "caveman", "caveman-commit")
	skillFile := filepath.Join(skillRoot, "SKILL.md")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillFile, []byte("# caveman-commit skill body"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := NewState(t.TempDir())
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "caveman-commit", RootDir: skillRoot}})
	tool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithToolCompletionCapture(context.Background())
	shortened := filepath.Join(base, "skills", ".system", "caveman-commit", "SKILL.md")
	out, err := tool.Handle(ctx, `{"file_path":`+quoteJSON(shortened)+`}`)
	if err != nil {
		t.Fatalf("read through the shortened skill path: %v", err)
	}
	text, _ := out.(string)
	if !strings.Contains(text, "caveman-commit skill body") {
		t.Fatalf("unexpected read output: %q", text)
	}
	// The model has to learn where the skill really lives, or it repeats the
	// same path in a shell command or a relative reference, where nothing
	// repairs it.
	if !strings.Contains(text, normalizeRootPath(skillRoot)) {
		t.Fatalf("read output does not name the skill root: %q", text)
	}
	if !strings.Contains(text, shortened) {
		t.Fatalf("read output does not name the path that was asked for: %q", text)
	}
	// The card must name the file that was read, not the one that was asked for.
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("read_file recorded no completion payload")
	}
	if got := RepairedReadPath(StepEvent{Output: completion.Output}); got != normalizeRootPath(skillFile) {
		t.Fatalf("repaired display path = %q, want %q", got, normalizeRootPath(skillFile))
	}
}

// TestShortenedSkillPathIsStillAWriteToThatSkill pins that the catalog's
// read-only boundary is not a matter of spelling. A model that quotes the
// catalog path with the bundle directory dropped is addressing that skill, and
// the write has to be refused the same way — naming the skill's real directory
// — instead of being approved into a stray directory beside it, where the edit
// changes nothing and the name is then hidden behind the skill it meant to
// change.
func TestShortenedSkillPathIsStillAWriteToThatSkill(t *testing.T) {
	base := t.TempDir()
	skillRoot := filepath.Join(base, "skills", ".system", "caveman", "caveman-commit")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("# body"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := NewState(t.TempDir())
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "caveman-commit", RootDir: skillRoot}})

	// A shortened catalog path naming a file the skill really has is repaired to
	// it, exactly as the read side repairs the same path.
	for _, target := range []string{
		filepath.Join(base, "skills", ".system", "caveman-commit", "SKILL.md"),
		filepath.Join(base, "skills", "caveman-commit", "SKILL.md"),
	} {
		repaired, item, ok := st.LoadedSkillWriteTarget(target)
		if !ok {
			t.Fatalf("write to %q was not recognized as addressing the skill", target)
		}
		if item.Name != "caveman-commit" {
			t.Fatalf("write to %q named skill %q", target, item.Name)
		}
		if !strings.HasPrefix(repaired, normalizeRootPath(skillRoot)) {
			t.Fatalf("write to %q was repaired to %q, outside the skill's real directory", target, repaired)
		}
		// The read side settles the same path the same way.
		read, _, readOK := st.resolveShortenedSkillPath(target)
		if readOK != ok || (readOK && read != repaired) {
			t.Fatalf("write and read disagree about %q: write=%q read=%q (%v)", target, repaired, read, readOK)
		}
	}

	// A file the skill does not have is not a file the catalog can point at, so
	// the repair does not fire for it — the same narrowness the read repair has,
	// and the reason neither side ever invents a location.
	absent := filepath.Join(base, "skills", ".system", "caveman-commit", "references", "new.md")
	if _, _, ok := st.LoadedSkillWriteTarget(absent); ok {
		t.Fatalf("a file the skill does not have was resolved into it: %q", absent)
	}
	if _, _, ok := st.resolveShortenedSkillPath(absent); ok {
		t.Fatalf("read and write disagree about a file the skill does not have: %q", absent)
	}

	// A shell command that writes through the same path has to be approved too,
	// and a read-only one still runs unasked.
	shortened := filepath.Join(base, "skills", ".system", "caveman-commit", "SKILL.md")
	if reason := st.LoadedSkillShellAccessReason("echo x > "+shortened, base, false); reason == "" {
		t.Fatal("shell write through a shortened skill path did not require approval")
	}
	if reason := st.LoadedSkillShellAccessReason("cat "+shortened, base, true); reason != "" {
		t.Fatalf("provably read-only shell access asked for approval: %q", reason)
	}
}

// A skill created under a root that holds no skill of that name is a new skill,
// not an edit of the existing one, and must not be refused.
func TestWritingANewSkillElsewhereIsNotBlocked(t *testing.T) {
	base := t.TempDir()
	skillRoot := filepath.Join(base, "home", "skills", ".system", "caveman", "caveman-commit")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("# body"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := NewState(t.TempDir())
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "caveman-commit", RootDir: skillRoot}})

	for _, target := range []string{
		// A different skills root entirely: nothing there holds that skill.
		filepath.Join(base, "project", ".forebrain", "skills", "caveman-commit", "SKILL.md"),
		// An ordinary new skill beside the bundle.
		filepath.Join(base, "home", "skills", "my-new-skill", "SKILL.md"),
		// An ordinary project file under a directory named after a skill.
		filepath.Join(base, "project", "docs", "caveman-commit", "notes.md"),
	} {
		if _, _, ok := st.LoadedSkillWriteTarget(target); ok {
			t.Fatalf("write to %q was treated as an edit of the existing skill", target)
		}
	}
}

func TestLoadedSkillWritesRaiseAForcedApproval(t *testing.T) {
	workspace := t.TempDir()
	skillRoot := filepath.Join(workspace, "skills", "demo")
	target := filepath.Join(skillRoot, "references", "guide.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	st := NewState(workspace)
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "demo", RootDir: skillRoot}})
	var seen map[string]any
	st.SetActionHook(func(_ context.Context, _ string, payload any) (string, bool, error) {
		seen, _ = payload.(map[string]any)
		return "approval", true, nil
	})
	tool, err := NewFileWriteTool(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tool.Handle(context.Background(), `{"file_path":`+quoteJSON(target)+`,"content":"changed"}`)
	var requires *RequiresActionError
	if !errors.As(err, &requires) {
		t.Fatalf("write error=%v want a pending approval", err)
	}
	if seen["force_tool_approval"] != true {
		t.Fatalf("loaded-skill write did not force the approval: %+v", seen)
	}
	if reason, _ := seen["justification"].(string); !strings.Contains(reason, `"demo"`) {
		t.Fatalf("approval does not say which skill is being changed: %+v", seen)
	}
	if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target was written before the approval was answered: %v", statErr)
	}

}

// A catalog path quoted with the bundle directory dropped is settled the way a
// read of that path is settled: repaired into the skill, and reported, so the
// write lands on the file the model meant instead of on a stray one beside the
// skill, and the model learns where that file actually is.
func TestShortenedSkillPathWriteIsRepairedAndReported(t *testing.T) {
	workspace := t.TempDir()
	bundled := filepath.Join(workspace, "skills", "bundle", "demo")
	if err := os.MkdirAll(filepath.Join(bundled, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundled, "references", "guide.md"), []byte("# body"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := NewState(workspace)
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "demo", RootDir: bundled}})
	approvalCalled := false
	var seen map[string]any
	st.SetActionHook(func(_ context.Context, _ string, payload any) (string, bool, error) {
		approvalCalled = true
		seen, _ = payload.(map[string]any)
		return "approval", true, nil
	})
	tool, err := NewFileWriteTool(st, nil)
	if err != nil {
		t.Fatal(err)
	}

	real := normalizeRootPath(filepath.Join(bundled, "references", "guide.md"))
	shortened := filepath.Join(workspace, "skills", "demo", "references", "guide.md")
	args := `{"file_path":` + quoteJSON(shortened) + `,"content":"changed"}`

	// It is the skill's own file, so it is approved like one, aimed at the
	// repaired path rather than at the path that is not there.
	_, err = tool.Handle(context.Background(), args)
	var requires *RequiresActionError
	if !errors.As(err, &requires) {
		t.Fatalf("shortened-path write error=%v want a pending approval", err)
	}
	if !approvalCalled {
		t.Fatal("shortened-path write did not reach the approval")
	}
	if resolved, _ := seen["resolved_file_path"].(string); resolved != real {
		t.Fatalf("approval was aimed at %q, not at the skill's real file %q", resolved, real)
	}

	// Overwriting needs a prior read, and the read of that same shortened path
	// resolves to the same file - that is what makes the two sides one rule.
	readTool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatal(err)
	}
	readOut, err := readTool.Handle(context.Background(), `{"file_path":`+quoteJSON(shortened)+`}`)
	if err != nil {
		t.Fatalf("read of the shortened path failed: %v", err)
	}
	if !strings.Contains(fmt.Sprint(readOut), "note:") {
		t.Fatalf("the read did not report its repair: %v", readOut)
	}

	raw, err := tool.Handle(WithApprovedActionID(context.Background(), requires.ActionID), args)
	if err != nil {
		t.Fatalf("approved shortened-path write failed: %v", err)
	}
	out := fmt.Sprint(raw)
	// The repair is reported, in the same shape a repaired read reports it.
	for _, want := range []string{"note:", shortened, `"demo"`, real} {
		if !strings.Contains(out, want) {
			t.Fatalf("repair note does not mention %q: %s", want, out)
		}
	}
	if _, statErr := os.Stat(shortened); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a stray file was created beside the skill: %v", statErr)
	}
	if body, readErr := os.ReadFile(real); readErr != nil || string(body) != "changed" {
		t.Fatalf("the skill's real file was not the one written: %q %v", string(body), readErr)
	}
}

func TestLoadedSkillShellAccessRequiresProvablyReadOnlyCommand(t *testing.T) {
	workspace := t.TempDir()
	skillRoot := filepath.Join(workspace, "skills", "demo")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	st := NewState(workspace)
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "demo", RootDir: skillRoot}})
	readCommand := "cat " + filepath.Join(skillRoot, "references", "guide.md")
	if got := safety.ClassifyShellCommand(readCommand, MergeAllowedRootPaths(st.AllowedRoots(), st.LoadedSkillRoots()), workspace); got != safety.ShellMutationReadOnly {
		t.Fatalf("expected skill resource read to be classified read-only, got %s: %q", got, readCommand)
	}
	if reason := st.LoadedSkillShellAccessReason(readCommand, workspace, true); reason != "" {
		t.Fatalf("provably read-only skill access asked for approval: %q", reason)
	}
	for _, command := range []string{
		"touch " + filepath.Join(skillRoot, "new.txt"),
		"printf changed > " + filepath.Join(skillRoot, "out.txt"),
		"rm -f " + filepath.Join(skillRoot, "old.txt"),
		"sed -i s/a/b/ " + filepath.Join(skillRoot, "guide.md"),
		"cp " + filepath.Join(skillRoot, "guide.md") + " " + filepath.Join(workspace, "copy.md"),
		"python " + filepath.Join(skillRoot, "scripts", "inspect.py"),
	} {
		if reason := st.LoadedSkillShellAccessReason(command, workspace, false); reason == "" {
			t.Fatalf("command %q did not require approval for its skill access", command)
		}
	}
	if reason := st.LoadedSkillShellAccessReason("go test ./...", workspace, false); reason != "" {
		t.Fatalf("non-skill shell command must retain normal policy: %q", reason)
	}
}

func TestShellRaisesAForcedApprovalForUnprovableLoadedSkillAccess(t *testing.T) {
	workspace, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	skillRoot := filepath.Join(t.TempDir(), "skills", "demo")
	if err := os.MkdirAll(filepath.Join(skillRoot, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	st := NewState(workspace)
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "demo", RootDir: skillRoot}})
	var seen map[string]any
	st.SetActionHook(func(_ context.Context, _ string, payload any) (string, bool, error) {
		seen, _ = payload.(map[string]any)
		return "approval", true, nil
	})
	tool, err := NewShellTool(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	command := "python " + filepath.Join(skillRoot, "scripts", "inspect.py")
	_, err = tool.Handle(context.Background(), `{"command":`+quoteJSON(command)+`}`)
	var requires *RequiresActionError
	if !errors.As(err, &requires) {
		t.Fatalf("shell error=%v want a pending approval", err)
	}
	if seen["force_tool_approval"] != true {
		t.Fatalf("loaded-skill shell access did not force the approval: %+v", seen)
	}
	if reason, _ := seen["justification"].(string); !strings.Contains(reason, `"demo"`) {
		t.Fatalf("approval does not say which skill the command reaches: %+v", seen)
	}
}

func TestReplaceLoadedSkillsDropsOldCatalogRoots(t *testing.T) {
	st := NewState(t.TempDir())
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "first", RootDir: first}})
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "second", RootDir: second}})
	if st.PathUnderLoadedSkillRoot(filepath.Join(first, "file.md")) {
		t.Fatal("old loaded-skill root survived catalog replacement")
	}
	if !st.PathUnderLoadedSkillRoot(filepath.Join(second, "file.md")) {
		t.Fatal("new loaded-skill root missing after catalog replacement")
	}
}

// Replacing the catalog swaps which skill directories are readable and leaves
// unrelated tool metadata alone.
func TestReplaceLoadedSkillCatalogSwapsResourceRoots(t *testing.T) {
	st := NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", Category: "file", ReadOnly: true})
	oldRoot := filepath.Join(t.TempDir(), "old")
	newRoot := filepath.Join(t.TempDir(), "new")
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "old-skill", RootDir: oldRoot}})

	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "new-skill", RootDir: newRoot}})

	metas := map[string]event.ToolMeta{}
	for _, meta := range st.ToolMetas() {
		metas[meta.Name] = meta
	}
	if _, ok := metas["new-skill"]; ok {
		t.Fatal("a skill appeared in the tool table; skills are described in the prompt catalog")
	}
	if _, ok := metas["read_file"]; !ok {
		t.Fatal("unrelated metadata was removed by skill catalog replacement")
	}
	if st.PathUnderLoadedSkillRoot(filepath.Join(oldRoot, "file.md")) {
		t.Fatal("stale skill root survived catalog replacement")
	}
	if !st.PathUnderLoadedSkillRoot(filepath.Join(newRoot, "file.md")) {
		t.Fatal("new skill root missing after catalog replacement")
	}
}

func TestLoadedSkillWriteTargetCoversSymlinkEntryInsideRoot(t *testing.T) {
	outside := t.TempDir()
	skillRoot := filepath.Join(t.TempDir(), "skill")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(skillRoot, "external-link")
	if err := os.Symlink(filepath.Join(outside, "target"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	st := NewState(t.TempDir())
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "demo", RootDir: skillRoot}})
	if _, _, ok := st.LoadedSkillWriteTarget(link); !ok {
		t.Fatal("a symlink entry inside a skill root was not recognized as addressing the skill")
	}
}

func quoteJSON(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}
