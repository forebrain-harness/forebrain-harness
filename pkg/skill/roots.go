package skill

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

// projectSkillDirs are the project-scoped skill directories forebrain honors,
// highest priority first: its own, then the tool-neutral .agents convention,
// then the two agent-specific directories a repository is most likely to
// already carry. A team that has committed skills for Claude Code or Codex has
// committed skills for this repository, and making the user copy them into
// .forebrain/skills to use them here would just create a second copy to keep in
// sync.
//
// All of them are gated on the same trust check: every one is instructions —
// and often scripts — that arrive with a checkout.
var projectSkillDirs = [][]string{
	{".forebrain", "skills"},
	{".agents", "skills"},
	{".claude", "skills"},
	{".codex", "skills"},
}

// userSkillDirs are the cross-tool user-level skill directories, relative to
// the OS user home. They are the personal counterparts of the project dirs: a
// skill library the user already installed for another agent works here too.
//
// Unlike the project dirs these are shared across agents — they follow the same
// default as $FOREBRAIN_HOME/skills, deferred until the model looks for them. A
// personal library can be large and was written for another agent's tool
// names, so putting all of it in every prompt prefix would cost tokens in every
// session for skills most sessions never use. Promote the ones worth having up
// front per skill from /skills.
var userSkillDirs = [][]string{
	{".agents", "skills"},
	{".claude", "skills"},
	{".codex", "skills"},
}

// AgentSkillRoots returns the skill roots one runtime sees, highest priority
// first. The trusted project roots come from the launch context, which the
// caller froze when the runtime was built — a gateway serves sessions in
// several projects, so the project a session's skills belong to is session
// state, never something this package may re-derive from the process working
// directory. The launch trust prompt is the gate, and it runs for every
// directory — version controlled or not — so a trusted plain directory gets
// the same project skills a trusted checkout gets.
func AgentSkillRoots(home string, workspaceRoot string, launch safety.ProjectContext) []string {
	var trusted []string
	if launch.IsTrusted() {
		if root := strings.TrimSpace(launch.Project.Root); root != "" {
			trusted = projectSkillRootsForRoot(root)
		}
	}
	return agentSkillRoots(home, workspaceRoot, trusted)
}

func agentSkillRoots(home string, workspaceRoot string, trustedProjectRoots []string) []string {
	h := strings.TrimSpace(home)
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	out := make([]string, 0, len(projectSkillDirs)+len(userSkillDirs)+3)
	// 1. Trusted project-level skills (highest priority).
	out = append(out, trustedProjectRoots...)
	// 2. Workspace skills
	if workspaceRoot != "" {
		out = append(out, filepath.Join(workspaceRoot, "skills"))
	}
	// 3. User-installed skills
	if h != "" {
		out = append(out, filepath.Join(h, "skills"))
	}
	// 4. Cross-tool user skill dirs
	out = append(out, UserSkillRoots()...)
	// 5. Built-in system skills (lowest priority)
	if h != "" {
		out = append(out, filepath.Join(h, "skills", ".system"))
	}
	return out
}

// UserSkillRoots returns the cross-tool user-level skill roots, highest
// priority first. Empty when the OS user home cannot be resolved.
func UserSkillRoots() []string {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	userHome = strings.TrimSpace(userHome)
	if userHome == "" {
		return nil
	}
	out := make([]string, 0, len(userSkillDirs))
	for _, parts := range userSkillDirs {
		segments := append([]string{userHome}, parts...)
		out = append(out, filepath.Clean(filepath.Join(segments...)))
	}
	return out
}

// TrustedProjectSkillRoots returns the project skill roots that may reach the
// model: empty unless projectRoot sits in a project the user has trusted.
// The launch trust prompt is the whole gate — it runs for every directory, version
// controlled or not — so a plain directory the user created themselves gets
// the same project skills a trusted checkout gets, and a directory nobody has
// judged gets none. The project root is treated as the exact boundary: callers
// pass an already-resolved root (a launch project's git root, or a root the
// user registered as a project), and resolving a registered subdirectory up
// to an enclosing checkout would relocate the project onto the checkout.
func TrustedProjectSkillRoots(home string, projectRoot string) []string {
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		return nil
	}
	root := CanonicalSkillPath(projectRoot)
	if root == "" {
		root = projectRoot
	}
	trusted, err := safety.IsTrusted(strings.TrimSpace(home), safety.Project{Root: root})
	if err != nil || !trusted {
		return nil
	}
	return projectSkillRootsForRoot(root)
}

// ProjectSkillRootsForDir returns the project skill roots for a project root
// treated as the exact boundary, with no trust check. It answers "which
// directories does this project hold", which is what naming and
// classification need; anything that decides what the model may load must use
// TrustedProjectSkillRoots instead. The directory is required: a skill root
// that depends on where the process happens to run is the bug this package
// exists to avoid.
func ProjectSkillRootsForDir(dir string) []string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	return projectSkillRootsForRoot(CanonicalSkillPath(dir))
}

func projectSkillRootsForRoot(projectRoot string) []string {
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		return nil
	}
	out := make([]string, 0, len(projectSkillDirs))
	for _, parts := range projectSkillDirs {
		segments := append([]string{projectRoot}, parts...)
		out = append(out, filepath.Clean(filepath.Join(segments...)))
	}
	return out
}

// CanonicalSkillPath is the one spelling of a skill directory used as a key.
// Absolute, symlink-resolved, and cleaned: the skill runtime, the toggle store,
// and the /skills picker all normalize through this, so a skill reached by two
// different paths (a symlinked workspace, a relative root) is still one skill
// with one enabled flag and one load mode.
func CanonicalSkillPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = filepath.Clean(path)
	// Resolve the deepest ancestor that exists and re-attach the gateway. Doing it
	// this way keeps the answer stable for a path that does not exist yet — a
	// skill directory being classified before it is created, or a root built
	// from configuration — which a bare EvalSymlinks cannot do: it fails on the
	// whole path and leaves /var next to an already-resolved /private/var.
	remainder := ""
	for dir := path; ; {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil && strings.TrimSpace(resolved) != "" {
			return filepath.Clean(filepath.Join(resolved, remainder))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path
		}
		remainder = filepath.Join(filepath.Base(dir), remainder)
		dir = parent
	}
}
