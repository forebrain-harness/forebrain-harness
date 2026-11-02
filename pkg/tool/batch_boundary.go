// The batch task's filesystem boundary, in one place (plans/216).
//
// A batch execution is unattended, so its boundary is a deny list rather than
// an allow list: the whole filesystem is readable — a package manager's
// configuration, the toolchain, the language runtimes all live outside the
// boundary and have to be — while the Forebrain home and, when it lives
// elsewhere, the project root it was carved out of are not. Two exceptions
// carve the task's own work back out: its execution workspace (read and write)
// and the project's loadable skill directories (read).
//
// The same value feeds both enforcement layers, and that is the point: the OS
// sandbox (pkg/safety) is asked to enforce exactly what the in-process file
// tools refuse, so a task can never read a path through one door that the
// other closed.
package tool

import (
	"context"
	"path/filepath"
	"strings"
)

// BatchBoundary is the whole filesystem boundary of one batch execution.
type BatchBoundary struct {
	// Workspace is the execution's own directory — <project root>/tasks/<id>/<n>.
	// Empty means "not a batch turn", which is what every predicate below
	// answers to.
	Workspace string
	// Writable is what the execution may write. It is the workspace, and
	// nothing else.
	Writable []string
	// Readable are the subpaths of Denied that stay readable: the workspace and
	// the project's loadable skill directories.
	Readable []string
	// Denied are the roots the execution may neither read nor write.
	Denied []string
}

// NewBatchBoundary computes the boundary of one execution. workspace is the
// execution's own directory, home is FOREBRAIN_HOME, projectRoot is the project
// the workspace was carved out of, and skillRoots are the directories the
// project's skills load from (pkg/skill's one root table, which is also what
// the skill catalog in the prompt prefix is built from).
//
// A skill root outside every denied root is not listed as an exception: it is
// readable anyway, and a rule that says so twice invites exactly the drift
// between the two lists this type exists to prevent.
func NewBatchBoundary(workspace, projectRoot, home string, skillRoots []string) BatchBoundary {
	ws := strings.TrimSpace(workspace)
	if ws == "" {
		return BatchBoundary{}
	}
	ws = filepath.Clean(ws)
	denied := make([]string, 0, 2)
	if h := strings.TrimSpace(home); h != "" {
		denied = append(denied, filepath.Clean(h))
	}
	// Denying the project root only matters when it is its own tree: a root
	// inside the Forebrain home is already covered, and a root outside it is
	// what makes the sibling task directories and the rest of the project
	// invisible.
	if root := strings.TrimSpace(projectRoot); root != "" && !pathWithinAny(filepath.Clean(root), denied) {
		denied = append(denied, filepath.Clean(root))
	}
	readable := MergeAllowedRootPaths([]string{ws}, nil)
	for _, root := range skillRoots {
		root = strings.TrimSpace(root)
		if root == "" || !pathWithinAny(filepath.Clean(root), denied) {
			continue
		}
		readable = MergeAllowedRootPaths(readable, []string{root})
	}
	return BatchBoundary{Workspace: ws, Writable: []string{ws}, Readable: readable, Denied: denied}
}

// DeniesRead reports whether the boundary refuses to read path: inside a denied
// root, and not inside one of the exceptions.
func (b BatchBoundary) DeniesRead(path string) bool {
	if b.Workspace == "" {
		return false
	}
	if !b.inside(path, b.Denied) {
		return false
	}
	return !b.inside(path, b.Readable)
}

// PrunesReadDir reports whether a read walk may skip dir wholesale: it is
// denied, and no exception lives at or beneath it, so descending could only
// find refusals. A directory that merely *contains* an exception — the
// workspace's project root, the Forebrain home — is walked.
func (b BatchBoundary) PrunesReadDir(dir string) bool {
	if b.Workspace == "" {
		return false
	}
	if !b.inside(dir, b.Denied) || b.inside(dir, b.Readable) {
		return false
	}
	for _, root := range append(append([]string{}, b.Readable...), b.Writable...) {
		if pathWithinRoot(root, dir) {
			return false
		}
	}
	return true
}

func (b BatchBoundary) inside(path string, roots []string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	for _, root := range roots {
		if pathWithinRoot(path, root) {
			return true
		}
	}
	return false
}

// batchBoundaryKey carries a turn's boundary to the tools that enforce it.
type batchBoundaryKey struct{}

// WithBatchBoundary records one batch execution's boundary on a turn's
// context. It is set where the turn's environment is built — the single place
// that knows the execution, its project and its home — and it travels to
// subagents with the task workspace and task context.
func WithBatchBoundary(ctx context.Context, boundary BatchBoundary) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, batchBoundaryKey{}, boundary)
}

// BatchBoundaryFromContext is the boundary WithBatchBoundary recorded, and
// false on a turn that is not a batch execution.
func BatchBoundaryFromContext(ctx context.Context) (BatchBoundary, bool) {
	if ctx == nil {
		return BatchBoundary{}, false
	}
	boundary, ok := ctx.Value(batchBoundaryKey{}).(BatchBoundary)
	if !ok || strings.TrimSpace(boundary.Workspace) == "" {
		return BatchBoundary{}, false
	}
	return boundary, true
}
