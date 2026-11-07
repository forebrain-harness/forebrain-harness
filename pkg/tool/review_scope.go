package tool

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ReviewWriteScope is the narrow write access a coding conversation's plan
// reviewer holds so it can reproduce what a plan claims instead of judging it
// from reading: new test files inside the project, anything inside the
// review's own scratch directory — which is where a git worktree of the
// project lives when the reviewer wants to change code freely — and nothing
// else. Production code and every file that existed before the review stay
// read-only.
//
// The static tool table keeps plan-reviewer read-only, so everything that asks
// whether the type writes files (plan-mode inheritance, write-grant requests,
// the tier the pages show) keeps its answer. The scope travels only on the
// context of a coding review: an office or batch reviewer, which never gets
// one, cannot write at all.
type ReviewWriteScope struct {
	projectRoot string
	scratch     string
	// gitWorktrees is <git common dir>/worktrees when the project is a git
	// repository, "" otherwise. The shell may write there so `git worktree add`
	// can register a worktree under scratch; the user's checkout stays
	// read-only.
	gitWorktrees string

	mu sync.Mutex
	// created holds the test files this review created, so it may keep
	// editing its own work without being able to touch anything older.
	created map[string]struct{}
}

// NewReviewWriteScope builds the scope of one review. projectRoot is the
// project the plan is about, scratch the review's own directory, and
// gitWorktrees the repository's worktrees directory or "" outside a repository.
func NewReviewWriteScope(projectRoot, scratch, gitWorktrees string) *ReviewWriteScope {
	return &ReviewWriteScope{
		projectRoot:  filepath.Clean(projectRoot),
		scratch:      filepath.Clean(scratch),
		gitWorktrees: strings.TrimSpace(gitWorktrees),
		created:      map[string]struct{}{},
	}
}

// PrepareReviewWriteScope builds a review's scope for the project at
// projectRoot: a fresh scratch directory under the OS temp directory, and — in
// a git repository — the repository's worktrees registry, created if git has
// not made it yet, so `git worktree add` can run from a shell that may not
// write anything else of the repository.
func PrepareReviewWriteScope(ctx context.Context, projectRoot string) (*ReviewWriteScope, error) {
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		return nil, fmt.Errorf("plan review: no project root")
	}
	scratch, err := os.MkdirTemp("", "forebrain-plan-review-")
	if err != nil {
		return nil, err
	}
	worktrees := ""
	if out, gitErr := shellDiffRun(ctx, shellDiffDiscoveryEnv(), 2*time.Second, nil,
		"-C", projectRoot, "rev-parse", "--path-format=absolute", "--git-common-dir"); gitErr == nil {
		if common := strings.TrimSpace(out); common != "" {
			dir := filepath.Join(filepath.Clean(common), "worktrees")
			if os.MkdirAll(dir, 0o755) == nil {
				worktrees = dir
			}
		}
	}
	return NewReviewWriteScope(projectRoot, scratch, worktrees), nil
}

// Release tidies a finished review's scratch directory. Its tmp/ only ever
// held temporary files, and a directory the reviewer never wrote to is removed,
// so a review that reproduced nothing leaves nothing behind. What the reviewer
// did write - tests, scripts, a worktree - stays, as its report lists.
func (s *ReviewWriteScope) Release() {
	if s == nil || s.scratch == "" {
		return
	}
	_ = os.RemoveAll(filepath.Join(s.scratch, "tmp"))
	// Remove only succeeds on an empty directory, which is exactly the case
	// to clean up.
	_ = os.Remove(s.scratch)
}

// Scratch is the review's own directory.
func (s *ReviewWriteScope) Scratch() string {
	if s == nil {
		return ""
	}
	return s.scratch
}

// GitWorktrees is the repository's worktrees directory, "" outside one.
func (s *ReviewWriteScope) GitWorktrees() string {
	if s == nil {
		return ""
	}
	return s.gitWorktrees
}

// shellWritableRoots are the only places the reviewer's shell may write: its
// scratch directory and, in a repository, the worktrees registry `git worktree
// add` writes to.
func (s *ReviewWriteScope) shellWritableRoots() []string {
	if s == nil {
		return nil
	}
	roots := []string{s.scratch}
	if s.gitWorktrees != "" {
		roots = append(roots, s.gitWorktrees)
	}
	return roots
}

// admitWrite answers one write: anything under scratch, a new test file in the
// project, or a test file this review created. A test file it admits as new
// is remembered as the review's own.
func (s *ReviewWriteScope) admitWrite(absPath string) error {
	absPath = filepath.Clean(absPath)
	if pathWithinRoot(absPath, s.scratch) {
		return nil
	}
	if !pathWithinRoot(absPath, s.projectRoot) {
		return fmt.Errorf("plan review: %s is outside the project and the review's scratch directory %s", absPath, s.scratch)
	}
	rel, err := filepath.Rel(normalizeRootPath(s.projectRoot), normalizeRootPath(absPath))
	if err != nil {
		return fmt.Errorf("plan review: %s: %w", absPath, err)
	}
	if !isTestFilePath(rel) {
		return fmt.Errorf("plan review: %s is not a test file; production code is read-only — write new test files in the project, or anything under %s (a git worktree there may change any file)", rel, s.scratch)
	}
	key := normalizeRootPath(absPath)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, own := s.created[key]; own {
		return nil
	}
	if _, statErr := os.Lstat(absPath); statErr == nil {
		return fmt.Errorf("plan review: %s already exists; only test files this review created can be changed", rel)
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("plan review: %s: %w", rel, statErr)
	}
	s.created[key] = struct{}{}
	return nil
}

// isTestFilePath reports whether a project-relative path names a test file by
// the naming conventions test runners themselves use: Go's _test.go, the
// JavaScript family's .test./.spec. infix, Python's test_*.py and *_test.py,
// Ruby's _spec.rb/_test.rb, the JVM and .NET Test/Tests suffix, or any file
// under a test, tests, __tests__, testdata, spec or e2e directory. Dependency
// and metadata trees never count, whatever their file names say.
func isTestFilePath(rel string) bool {
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") {
		return false
	}
	segments := strings.Split(rel, "/")
	inTestDir := false
	for _, dir := range segments[:len(segments)-1] {
		switch strings.ToLower(dir) {
		case ".git", "node_modules", "vendor", "third_party":
			return false
		case "test", "tests", "__tests__", "testdata", "spec", "e2e":
			inTestDir = true
		}
	}
	if inTestDir {
		return true
	}
	base := segments[len(segments)-1]
	ext := strings.ToLower(path.Ext(base))
	stem := strings.TrimSuffix(base, path.Ext(base))
	lowerStem := strings.ToLower(stem)
	switch ext {
	case ".go":
		return strings.HasSuffix(lowerStem, "_test")
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts", ".vue":
		return strings.HasSuffix(lowerStem, ".test") || strings.HasSuffix(lowerStem, ".spec")
	case ".py":
		return strings.HasPrefix(lowerStem, "test_") || strings.HasSuffix(lowerStem, "_test")
	case ".rb":
		return strings.HasSuffix(lowerStem, "_spec") || strings.HasSuffix(lowerStem, "_test")
	case ".java", ".kt", ".scala", ".cs", ".swift":
		return strings.HasSuffix(stem, "Test") || strings.HasSuffix(stem, "Tests")
	}
	return false
}

// reviewScopeAdmitsTool reports whether a coding review's scope lifts the
// static table's refusal of a file-writing tool for this run. The scope then
// confines every write through GuardWrite.
func reviewScopeAdmitsTool(ctx context.Context, toolName string) bool {
	if ReviewWriteScopeFromContext(ctx) == nil {
		return false
	}
	switch strings.TrimSpace(toolName) {
	case "write_file", "edit_file":
		return true
	}
	return false
}

// WithReviewWriteScope attaches a plan review's write scope to its context.
func WithReviewWriteScope(ctx context.Context, scope *ReviewWriteScope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if scope == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyReviewWriteScope, scope)
}

// ReviewWriteScopeFromContext returns the review write scope on ctx, nil when
// the run is not a coding plan review.
func ReviewWriteScopeFromContext(ctx context.Context) *ReviewWriteScope {
	if ctx == nil {
		return nil
	}
	scope, _ := ctx.Value(ctxKeyReviewWriteScope).(*ReviewWriteScope)
	return scope
}
