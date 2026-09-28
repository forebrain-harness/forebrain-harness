// Memory scopes, roots, and project resolution.
package memory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ScopeKind distinguishes the two kinds of memory a session can read or write:
// the project it is checked out in, and the user's cross-project preferences.
type ScopeKind string

const (
	ScopeProject ScopeKind = "project"
	ScopeGlobal  ScopeKind = "global"
)

// Scope is the partition key for every memory artifact: which project's
// MEMORY.md a line belongs to, or the user's own global preferences. It is the
// one thing that must be threaded through storage, retrieval, consolidation
// input, and the consolidation agent's filesystem confinement — every place
// that used to operate on "the agent's memories" now operates on one Scope at
// a time, which is what makes cross-project leakage structurally impossible
// rather than a matter of prompt discipline.
type Scope struct {
	Kind ScopeKind
	// Key is the project identity (memories.ProjectKey) for ScopeProject, and
	// unused for ScopeGlobal.
	Key string
}

// GlobalScope is the single, agent-wide scope for cross-project user
// preferences (tone, "always run tests", collaboration style — the kind of
// rule that would otherwise have to be relearned in every project).
func GlobalScope() Scope { return Scope{Kind: ScopeGlobal} }

// ProjectScope builds the scope of one project from its identity
// (memories.ProjectKey). It is the only way a project scope should be
// constructed: ok is false for an empty key, and every caller must turn
// project-scoped behaviour off in that case rather than substitute a shared
// scope.
//
// The check lives here because an empty key does not fail loudly downstream —
// it collapses: the scope directory "projects/" + "" joins to the projects
// parent directory itself, so an unchecked empty key would quietly write one
// session's memory on top of the folder that holds every project's.
func ProjectScope(key string) (Scope, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Scope{}, false
	}
	return Scope{Kind: ScopeProject, Key: key}, true
}

// ProjectScopeForCwd resolves the project scope of a session's working
// directory. ok is false when cwd carries no project identity (a blank cwd,
// which is the gateway/channel case with no launch directory). Callers must
// treat that as "project-scoped memory does not apply to this session" —
// there is no shared fallback bucket to fall into; a bucket is exactly the
// cross-project mixing this design removes.
func ProjectScopeForCwd(cwd string) (Scope, bool) {
	return ProjectScope(ProjectKey(cwd))
}

// ID is the scope's identity for logs, job keys, and consolidation-marker
// filenames: "global" or "project:<key>".
func (s Scope) ID() string {
	if s.Kind == ScopeProject {
		return "project:" + s.Key
	}
	return "global"
}

// dir is the scope's subdirectory under the memories root.
func (s Scope) dir() string {
	if s.Kind == ScopeProject {
		return "projects/" + s.Key
	}
	return "global"
}

// stateSegment is the scope's subdirectory name under per-scope state
// directories (rollout evidence, the consolidation marker). Unlike dir it is a
// single path segment: state layout does not mirror the memories/projects/
// nesting, it only needs one folder per scope.
func (s Scope) stateSegment() string {
	if s.Kind == ScopeProject {
		return s.Key
	}
	return "global"
}

const (
	MemoryDirName        = "memories"
	rolloutEvidenceDir   = "memory-rollouts"
	rolloutEvidenceState = "state"
	consolidationMarker  = "memory-consolidation"
)

// Root is the filesystem footprint of ONE scope: the memory folder the model
// reads and writes, and the state folder its rollout evidence and
// consolidation watermark live under. Every memory operation takes a Root, not
// a bare workspace path, so which scope it is touching is always explicit at
// the call site.
type Root struct {
	MemoryRoot string
	StateRoot  string
	scope      Scope
}

// Roots is a primary agent's memory footprint: the workspace root every scope
// hangs off. It resolves to a concrete Root only once a Scope is named, which
// is what makes "operate on the agent's memories without saying which
// project" impossible to write.
type Roots struct {
	workspaceRoot string
}

// ResolveRootsForAgent resolves the memory roots of ONE primary agent from its
// workspace root — the same per-tenant boundary the mode/plan/todo/permission
// stores hang off (see tool.AgentToolRuntime.StateRoot).
//
// A primary agent is a tenant. Its memories are tenant data and must not be
// reachable from any other agent's root, so callers pass that agent's workspace
// root; the shared home is never a valid argument.
func ResolveRootsForAgent(workspaceRoot string) (Roots, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return Roots{}, fmt.Errorf("primary agent workspace root is required")
	}
	abs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return Roots{}, fmt.Errorf("resolve primary agent workspace root: %w", err)
	}
	return Roots{workspaceRoot: filepath.Clean(abs)}, nil
}

// Scope resolves the concrete Root for one scope within this agent's memory
// footprint.
func (r Roots) Scope(scope Scope) Root {
	return Root{
		MemoryRoot: filepath.Join(r.workspaceRoot, MemoryDirName, filepath.FromSlash(scope.dir())),
		StateRoot:  filepath.Join(r.workspaceRoot, rolloutEvidenceState),
		scope:      scope,
	}
}

// Base is the agent's whole memories directory, spanning every scope. Callers
// outside this package must not read or write under it directly — it exists
// only for scope enumeration and whole-agent cleanup (Reset, clear-memories).
func (r Roots) Base() string {
	return filepath.Join(r.workspaceRoot, MemoryDirName)
}

// stateBase is the parent of every scope's state subtree
// (state/memories/<scopeSegment>/), used only for enumeration during
// housekeeping (evidence pruning across every scope this agent has data for).
func (r Roots) stateBase() string {
	return filepath.Join(r.workspaceRoot, rolloutEvidenceState, stateMemoriesDir)
}

// ListProjectScopes enumerates the project scopes this agent already has a
// memory folder for.
func (r Roots) ListProjectScopes() ([]Scope, error) {
	entries, err := os.ReadDir(filepath.Join(r.Base(), "projects"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	scopes := make([]Scope, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		scope, ok := ProjectScope(entry.Name())
		if !ok {
			continue
		}
		scopes = append(scopes, scope)
	}
	return scopes, nil
}

// stateMemoriesDir namespaces every scope's state (rollout evidence, the
// consolidation marker) under its own subtree of the shared state root, the
// same way every other per-agent store namespaces itself there (state/skills/,
// state/todos/, state/modes/, ...). Without it a project key would sit as a
// bare top-level directory next to those stores' own directory names.
const stateMemoriesDir = "memories"

func (r Root) stateScopeRoot() string {
	return filepath.Join(strings.TrimSpace(r.StateRoot), stateMemoriesDir, r.scope.stateSegment())
}

func (r Root) rolloutEvidenceRoot() string {
	return filepath.Join(r.stateScopeRoot(), rolloutEvidenceDir)
}

// consolidationMarkerPath records when this scope's last consolidation pass
// started, so the turn instruction can tell which ad-hoc notes it has already
// seen.
//
// It lives under the state root rather than the memory root because the memory
// root is the consolidation agent's whole filesystem and a tracked git
// workspace: a marker there would show up in the very diff it describes, and
// the agent could rewrite or delete it.
func (r Root) consolidationMarkerPath() string {
	return filepath.Join(r.stateScopeRoot(), consolidationMarker)
}

func (r Root) rolloutEvidencePath(threadID string, sourceUpdatedAt int64) string {
	name := fmt.Sprintf("%s-%d.jsonl", rolloutEvidenceID(threadID), sourceUpdatedAt)
	return filepath.Join(r.rolloutEvidenceRoot(), name)
}

func rolloutEvidenceID(threadID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(threadID)))
	return fmt.Sprintf("%x", digest[:16])
}

func ProjectRoot(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	if eval, err := filepath.EvalSymlinks(abs); err == nil && strings.TrimSpace(eval) != "" {
		abs = eval
	}
	cur := abs
	for {
		if hasGitMarker(cur) {
			return mainWorktreeRoot(cur)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return filepath.Clean(abs)
}

// mainWorktreeRoot maps a linked worktree back to the checkout it belongs to.
//
// A worktree's .git is a regular file holding "gitdir: <main>/.git/worktrees/<name>".
// Every worktree of a repository is the same project: the code, the conventions
// and the failures learned in one apply in the others. Treating each worktree as
// its own project would give it an empty memory folder and its own consolidation
// pass, so the identity resolves to the main checkout instead.
func mainWorktreeRoot(dir string) string {
	dir = filepath.Clean(dir)
	marker := filepath.Join(dir, ".git")
	info, err := os.Stat(marker)
	if err != nil || !info.Mode().IsRegular() {
		return dir
	}
	body, err := os.ReadFile(marker)
	if err != nil {
		return dir
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(body)), "gitdir:"))
	index := strings.Index(filepath.ToSlash(gitDir), "/.git/worktrees/")
	if index <= 0 {
		return dir
	}
	main := filepath.FromSlash(filepath.ToSlash(gitDir)[:index])
	if !filepath.IsAbs(main) {
		main = filepath.Join(dir, main)
	}
	if eval, err := filepath.EvalSymlinks(main); err == nil && strings.TrimSpace(eval) != "" {
		main = eval
	}
	return filepath.Clean(main)
}

// ProjectKey is the identity of the project containing path: its absolute root
// with every path separator turned into a dash, so
// /Users/ada/work/openclaw becomes -Users-ada-work-openclaw.
//
// The whole root is encoded, which is what makes the key safe to use as a
// directory name for per-project state: two checkouts that share a base name
// ("api" under two different parents) get different keys instead of sharing one
// folder. It is a pure function of the path — no registry, no allocation, no
// collision handling — and an empty result means path has no project identity,
// which callers must treat as "this feature does not apply", never as a bucket
// of its own.
func ProjectKey(path string) string {
	root := ProjectRoot(path)
	if root == "" {
		return ""
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':':
			return '-'
		default:
			return r
		}
	}, filepath.ToSlash(root))
}

// GitBranch reports the branch checked out at path.
//
// The command runs against path itself rather than the project root: a linked
// worktree resolves to the main checkout for identity purposes, but it has its
// own HEAD, and reporting the main checkout's branch for it would be wrong.
func GitBranch(path string) string {
	root := ProjectRoot(path)
	if root == "" || !hasGitMarker(root) {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "git", "-C", strings.TrimSpace(path), "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func hasGitMarker(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		return false
	}
	return st.IsDir() || st.Mode().IsRegular()
}
