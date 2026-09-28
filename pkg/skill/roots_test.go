package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

func TestAgentSkillRootsPriorityOrder(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	roots := AgentSkillRoots("/forebrain-home", filepath.Join("/forebrain-home", "workspace"), safety.ProjectContext{})

	idx := func(suffix string) int {
		for i, r := range roots {
			if strings.HasSuffix(filepath.Clean(r), filepath.Clean(suffix)) {
				return i
			}
		}
		return -1
	}
	project := idx(filepath.Join(".forebrain", "skills"))
	if project != -1 {
		t.Fatalf("untrusted project skill root must be omitted: %v", roots)
	}
	workspace := idx(filepath.Join("forebrain-home", "workspace", "skills"))
	user := idx(filepath.Join("forebrain-home", "skills"))
	agents := idx(filepath.Join(".agents", "skills"))
	claude := idx(filepath.Join(".claude", "skills"))
	codex := idx(filepath.Join(".codex", "skills"))
	system := idx(filepath.Join("forebrain-home", "skills", ".system"))

	if workspace == -1 || user == -1 || agents == -1 || claude == -1 || codex == -1 || system == -1 {
		t.Fatalf("missing expected root in %v", roots)
	}
	if !(workspace < user && user < agents && agents < claude && claude < codex && codex < system) {
		t.Fatalf("priority order broken: ws=%d user=%d agents=%d claude=%d codex=%d system=%d",
			workspace, user, agents, claude, codex, system)
	}
	// The cross-tool user dirs are personal libraries, not this repo's skills:
	// they follow $FOREBRAIN_HOME/skills.
	always := DefaultSkillRoots("/forebrain-home", filepath.Join("/forebrain-home", "workspace"), "")
	for _, root := range UserSkillRoots() {
		if WithinAny(filepath.Join(root, "demo"), always) {
			t.Fatalf("user skill root %q must default to deferred: %v", root, always)
		}
	}
}

func TestAgentSkillRootsIncludesTrustedGitProject(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, safety.Project{Root: repo, VersionControlled: true}); err != nil {
		t.Fatal(err)
	}
	// The launch context is explicit — the test never chdirs into the repo,
	// because a runtime must not depend on the process working directory.
	launch, err := safety.ResolveProjectContext(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	roots := AgentSkillRoots(home, filepath.Join(home, "workspace"), launch)
	wantProject, err := safety.Resolve(repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(wantProject.Root, ".forebrain", "skills"),
		filepath.Join(wantProject.Root, ".agents", "skills"),
		filepath.Join(wantProject.Root, ".claude", "skills"),
		filepath.Join(wantProject.Root, ".codex", "skills"),
	}
	if len(roots) < len(want) {
		t.Fatalf("roots=%v want the project roots first", roots)
	}
	for i, dir := range want {
		if filepath.Clean(roots[i]) != filepath.Clean(dir) {
			t.Fatalf("roots[%d]=%q want %q (all: %v)", i, roots[i], dir, roots)
		}
	}
	got := TrustedProjectSkillRoots(home, repo)
	if len(got) != len(want) {
		t.Fatalf("TrustedProjectSkillRoots=%v want %v", got, want)
	}
	for i, dir := range want {
		if filepath.Clean(got[i]) != filepath.Clean(dir) {
			t.Fatalf("TrustedProjectSkillRoots[%d]=%q want %q", i, got[i], dir)
		}
	}
	wantAgents := want[1]
	// Both project roots are discovered, for the same reason:
	// they are this repository's own skills.
	always := DefaultSkillRoots(home, filepath.Join(home, "workspace"), repo)
	if !WithinAny(filepath.Join(wantAgents, "demo"), always) {
		t.Fatalf("project .agents/skills should be a skill root: %v", always)
	}
}

// A directory the user created themselves — no checkout, no .git — is still a
// project once they have trusted it: the launch trust prompt runs for every
// directory, and its answer, not version control, is what gates project
// skills.
func TestAgentSkillRootsIncludesTrustedPlainDirectory(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	if err := safety.MarkTrusted(home, safety.Project{Root: dir}); err != nil {
		t.Fatal(err)
	}
	launch, err := safety.ResolveProjectContext(home, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !launch.IsTrusted() {
		t.Fatalf("trusted plain directory resolved as %v", launch)
	}
	// Compare against the canonical root: on macOS /var is a symlink to
	// /private/var, and every stored or returned path spells it resolved.
	root := launch.Project.Root
	roots := AgentSkillRoots(home, filepath.Join(home, "workspace"), launch)
	want := []string{
		filepath.Join(root, ".forebrain", "skills"),
		filepath.Join(root, ".agents", "skills"),
		filepath.Join(root, ".claude", "skills"),
		filepath.Join(root, ".codex", "skills"),
	}
	if len(roots) < len(want) {
		t.Fatalf("roots=%v want the project roots first", roots)
	}
	for i, projectDir := range want {
		if filepath.Clean(roots[i]) != filepath.Clean(projectDir) {
			t.Fatalf("roots[%d]=%q want %q (all: %v)", i, roots[i], projectDir, roots)
		}
	}
	got := TrustedProjectSkillRoots(home, dir)
	if len(got) != len(want) {
		t.Fatalf("TrustedProjectSkillRoots=%v want %v", got, want)
	}
	for i, projectDir := range want {
		if filepath.Clean(got[i]) != filepath.Clean(projectDir) {
			t.Fatalf("TrustedProjectSkillRoots[%d]=%q want %q", i, got[i], projectDir)
		}
	}
	// Trust is the gate, so an unjudged plain directory stays shut — the
	// state a brand-new directory is in before its first launch prompt.
	if unjudged := TrustedProjectSkillRoots(t.TempDir(), t.TempDir()); len(unjudged) != 0 {
		t.Fatalf("untrusted plain directory must expose no skill roots, got %v", unjudged)
	}
}

// The cross-tool project directory is gated exactly like .forebrain/skills: an
// untrusted checkout must not get its instructions loaded.
func TestProjectAgentsSkillsRequiresTrust(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := TrustedProjectSkillRoots(home, repo); len(got) != 0 {
		t.Fatalf("untrusted project must expose no skill roots, got %v", got)
	}
	// Location-only classification still resolves them, which is what naming
	// and source labels need.
	byLocation := ProjectSkillRootsForDir(repo)
	if len(byLocation) != 4 {
		t.Fatalf("ProjectSkillRootsForDir=%v want the four project roots", byLocation)
	}
	// An empty directory classifies nothing: there is no project to belong to,
	// and no fallback to the process working directory.
	if got := ProjectSkillRootsForDir(""); len(got) != 0 {
		t.Fatalf("ProjectSkillRootsForDir(\"\")=%v want none", got)
	}
}

// The helpers below were production functions that only the tests in this
// package ever called: each is a home-scoped wrapper over the
// workspace-scoped function that actually runs, now that skill state is
// stored per primary agent under that agent's workspace root. They live
// here so the production files carry no unused code while the tests keep
// exercising the live functions underneath.

// DefaultSkillRoots returns the skill roots that belong to the active agent
// itself: its own workspace skills and the trusted project skill roots.
// Everything else (user-installed, ~/.agents, bundled system skills) is shared
// across agents.
//
// This is the single definition of that split. The /skills picker consults it to show
// what a skill does today, so the two can never disagree about a skill whose
// load mode has not been overridden.
func DefaultSkillRoots(home string, workspaceRoot string, projectRoot string) []string {
	out := make([]string, 0, len(projectSkillDirs)+1)
	out = append(out, TrustedProjectSkillRoots(home, projectRoot)...)
	if root := strings.TrimSpace(workspaceRoot); root != "" {
		out = append(out, filepath.Clean(filepath.Join(root, "skills")))
	}
	return out
}

// Within reports whether path is root or sits underneath it.
func Within(path string, root string) bool {
	path = CanonicalSkillPath(path)
	root = CanonicalSkillPath(root)
	if path == "" || root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// WithinAny reports whether path sits under any of roots.
func WithinAny(path string, roots []string) bool {
	for _, root := range roots {
		if Within(path, root) {
			return true
		}
	}
	return false
}
