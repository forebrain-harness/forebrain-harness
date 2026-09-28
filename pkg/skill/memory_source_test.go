package skill_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

func testWorkspaceService(home, workspace string) *skill.Service {
	svc := skill.NewServiceForWorkspace(home, workspace)
	svc.IsBuiltin = turn.IsBuiltinName
	return svc
}

const testMemoryProjectKey = "-Test-project"

func newMemoryPromotionService(t *testing.T) *skill.Service {
	t.Helper()
	// process normally registers this once at startup (C7: skill must not
	// import memory, so it reaches memory only through this port). Repeating
	// the registration across tests is harmless: it is always the same
	// function.
	skill.SetAuthoredSkillSource(memory.ListAuthoredSkillsForWorkspace)
	home := t.TempDir()
	// An untrusted working directory keeps the project skill root out of the
	// hub listing, so assertions speak only about the workspace root.
	t.Chdir(t.TempDir())
	svc := testWorkspaceService(home, filepath.Join(home, "workspace"))
	svc.ProjectKey = testMemoryProjectKey
	return svc
}

func memorySkillsRoot(t *testing.T, svc *skill.Service) memory.Root {
	t.Helper()
	roots, err := memory.ResolveRootsForAgent(svc.WorkspaceRoot)
	if err != nil {
		t.Fatalf("resolve memory roots: %v", err)
	}
	return roots.Scope(memory.Scope{Kind: memory.ScopeProject, Key: testMemoryProjectKey})
}

func writeMemorySkill(t *testing.T, svc *skill.Service, dirName, frontmatter, body string) string {
	t.Helper()
	dir := filepath.Join(memorySkillsRoot(t, svc).AuthoredSkillsRoot(), dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir memory skill: %v", err)
	}
	content := "---\n" + frontmatter + "\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write memory skill: %v", err)
	}
	return dir
}

func findMemorySkill(t *testing.T, items []skill.MemorySkill, name string) skill.MemorySkill {
	t.Helper()
	for _, item := range items {
		if item.Name == name {
			return item
		}
	}
	t.Fatalf("no memory skill %q in %+v", name, items)
	return skill.MemorySkill{}
}

func TestPromoteMemorySkillCopiesIntoWorkspaceRoot(t *testing.T) {
	svc := newMemoryPromotionService(t)
	writeMemorySkill(t, svc, "release-check", "name: release-check\ndescription: Verify a release", "Body")

	items, err := svc.ListMemorySkills()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	item := findMemorySkill(t, items, "release-check")
	if item.Status != skill.MemorySkillNew || !item.Promotable() {
		t.Fatalf("item=%+v", item)
	}
	if item.SlashCommand != "/release-check" {
		t.Fatalf("slash command=%q", item.SlashCommand)
	}

	promoted, err := svc.PromoteMemorySkills([]string{"release-check"})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if len(promoted) != 1 {
		t.Fatalf("promoted=%+v", promoted)
	}
	want := filepath.Join(svc.WorkspaceRoot, "skills", "release-check", "SKILL.md")
	if promoted[0].Path != want {
		t.Fatalf("path=%q want %q", promoted[0].Path, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("promoted skill not on disk: %v", err)
	}

	// The source stays put: consolidation owns that folder and keeps rewriting
	// it, so promotion copies rather than moves.
	if _, err := os.Stat(filepath.Join(item.SourceDir, "SKILL.md")); err != nil {
		t.Fatalf("source skill was removed: %v", err)
	}

	after := findMemorySkill(t, mustListMemorySkills(t, svc), "release-check")
	if after.Status != skill.MemorySkillUpToDate {
		t.Fatalf("status after promote=%q", after.Status)
	}
}

// The memory folder is a diff-tracked workspace the consolidation agent owns,
// so promotion must leave no trace inside it.
func TestPromoteMemorySkillWritesNothingIntoTheMemoryFolder(t *testing.T) {
	svc := newMemoryPromotionService(t)
	writeMemorySkill(t, svc, "note-taking", "name: note-taking\ndescription: Take notes", "Body")
	root := memorySkillsRoot(t, svc)
	before := snapshotTree(t, root.MemoryRoot)

	if _, err := svc.PromoteMemorySkills([]string{"note-taking"}); err != nil {
		t.Fatalf("promote: %v", err)
	}

	if after := snapshotTree(t, root.MemoryRoot); after != before {
		t.Fatalf("memory folder changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if _, ok := skill.LookupLockEntry(svc.WorkspaceRoot, "note-taking"); !ok {
		t.Fatal("promotion was not recorded in the workspace lock file")
	}
}

func TestListMemorySkillsReportsOutdatedAfterConsolidationRewrite(t *testing.T) {
	svc := newMemoryPromotionService(t)
	writeMemorySkill(t, svc, "triage", "name: triage\ndescription: Triage bugs", "First body")
	if _, err := svc.PromoteMemorySkills([]string{"triage"}); err != nil {
		t.Fatalf("promote: %v", err)
	}

	writeMemorySkill(t, svc, "triage", "name: triage\ndescription: Triage bugs", "Rewritten body")

	if got := findMemorySkill(t, mustListMemorySkills(t, svc), "triage").Status; got != skill.MemorySkillOutdated {
		t.Fatalf("status=%q want outdated", got)
	}
	// Re-promoting resolves it and carries the new body across.
	if _, err := svc.PromoteMemorySkills([]string{"triage"}); err != nil {
		t.Fatalf("re-promote: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(svc.WorkspaceRoot, "skills", "triage", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Rewritten body") {
		t.Fatalf("promoted copy is stale: %q", string(body))
	}
	if got := findMemorySkill(t, mustListMemorySkills(t, svc), "triage").Status; got != skill.MemorySkillUpToDate {
		t.Fatalf("status=%q want up-to-date", got)
	}
}

// Re-promoting overwrites the promoted copy, so a hand-edited copy has to be
// visible as such before the user confirms.
func TestListMemorySkillsReportsDivergedAfterLocalEdit(t *testing.T) {
	svc := newMemoryPromotionService(t)
	writeMemorySkill(t, svc, "handoff", "name: handoff\ndescription: Hand off work", "Body")
	if _, err := svc.PromoteMemorySkills([]string{"handoff"}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	installed := filepath.Join(svc.WorkspaceRoot, "skills", "handoff", "SKILL.md")
	if err := os.WriteFile(installed, []byte("---\nname: handoff\ndescription: Hand off work\n---\nEdited by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := findMemorySkill(t, mustListMemorySkills(t, svc), "handoff").Status; got != skill.MemorySkillDiverged {
		t.Fatalf("status=%q want diverged", got)
	}
}

// A dynamic slash command shadows a builtin of the same name, so a
// model-authored skill must never be able to claim one.
func TestPromoteRefusesNameCollidingWithBuiltinCommand(t *testing.T) {
	svc := newMemoryPromotionService(t)
	writeMemorySkill(t, svc, "compact", "name: compact\ndescription: Squeeze things", "Body")

	item := findMemorySkill(t, mustListMemorySkills(t, svc), "compact")
	if item.Promotable() || !strings.Contains(item.Blocked, "/compact") {
		t.Fatalf("item=%+v", item)
	}
	if _, err := svc.PromoteMemorySkills([]string{"compact"}); err == nil {
		t.Fatal("expected promotion to be refused")
	}
	if _, err := os.Stat(filepath.Join(svc.WorkspaceRoot, "skills", "compact")); !os.IsNotExist(err) {
		t.Fatalf("refused skill was copied anyway: %v", err)
	}
}

func TestPromoteRefusesUnsafeNamesAndOversizedSkills(t *testing.T) {
	svc := newMemoryPromotionService(t)
	writeMemorySkill(t, svc, "shouty", "name: Shouty_Name\ndescription: Bad name", "Body")
	writeMemorySkill(t, svc, "nodesc", "name: nodesc", "Body")
	writeMemorySkill(t, svc, "huge", "name: huge\ndescription: Too long", strings.Repeat("line\n", 500+10))

	items := mustListMemorySkills(t, svc)
	for name, want := range map[string]string{
		"Shouty_Name": "lowercase",
		"nodesc":      "description",
		"huge":        "line limit",
	} {
		item := findMemorySkill(t, items, name)
		if item.Promotable() || !strings.Contains(item.Blocked, want) {
			t.Fatalf("%s: blocked=%q want mention of %q", name, item.Blocked, want)
		}
	}
}

// One refusal fails the whole call: a half-applied batch leaves the user to
// work out which skills landed.
func TestPromoteRefusesTheWholeBatchWhenOneEntryIsBlocked(t *testing.T) {
	svc := newMemoryPromotionService(t)
	writeMemorySkill(t, svc, "fine", "name: fine\ndescription: Perfectly fine", "Body")
	writeMemorySkill(t, svc, "clear", "name: clear\ndescription: Collides", "Body")

	if _, err := svc.PromoteMemorySkills([]string{"fine", "clear"}); err == nil {
		t.Fatal("expected the batch to be refused")
	}
	if _, err := os.Stat(filepath.Join(svc.WorkspaceRoot, "skills", "fine")); !os.IsNotExist(err) {
		t.Fatalf("blocked batch still promoted an entry: %v", err)
	}
}

func TestPromoteRejectsUnknownName(t *testing.T) {
	svc := newMemoryPromotionService(t)
	writeMemorySkill(t, svc, "known", "name: known\ndescription: Known", "Body")

	if _, err := svc.PromoteMemorySkills([]string{"missing"}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err=%v", err)
	}
	if _, err := svc.PromoteMemorySkills(nil); err == nil {
		t.Fatal("expected an empty selection to be refused")
	}
}

func mustListMemorySkills(t *testing.T, svc *skill.Service) []skill.MemorySkill {
	t.Helper()
	items, err := svc.ListMemorySkills()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return items
}

func snapshotTree(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if entry.IsDir() {
			lines = append(lines, "d "+filepath.ToSlash(rel))
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		lines = append(lines, "f "+filepath.ToSlash(rel)+" "+string(body))
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return strings.Join(lines, "\n")
}
