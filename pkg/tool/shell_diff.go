package tool

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

// ShellEditDiff is the per-file diff of the working-tree changes one shell
// command made (Claude Code's bashEditDiff). It rides the completion output the
// surfaces render from and never reaches the model.
type ShellEditDiff struct {
	Files        []ShellEditDiffFile `json:"files"`
	MoreFiles    int                 `json:"more_files"`
	ChangedFiles []string            `json:"changed_files,omitempty"` // absolute, at most 200
	Unavailable  bool                `json:"unavailable,omitempty"`
	Skipped      bool                `json:"skipped,omitempty"`
	Shared       bool                `json:"shared,omitempty"`
	TreeMoved    bool                `json:"tree_moved,omitempty"`
}

type ShellEditDiffFile struct {
	Path        string `json:"path"`         // absolute
	DisplayPath string `json:"display_path"` // relative to the command's working directory when inside it
	Status      string `json:"status"`       // "created", "deleted" or "updated"
	Added       int    `json:"added"`
	Deleted     int    `json:"deleted"`
	UnifiedDiff string `json:"unified_diff,omitempty"`
}

// DecodeShellEditDiff normalizes the "shell_edit_diff" output value: the struct
// on a live step, its JSON map after a replay.
func DecodeShellEditDiff(v any) (ShellEditDiff, bool) {
	switch x := v.(type) {
	case nil:
		return ShellEditDiff{}, false
	case ShellEditDiff:
		return x, true
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return ShellEditDiff{}, false
		}
		var out ShellEditDiff
		if err := json.Unmarshal(b, &out); err != nil {
			return ShellEditDiff{}, false
		}
		return out, true
	}
}

// HookToolResponse is the tool_response a PostToolUse hook receives (Claude
// Code's shape): for shell, the result object the model was given, decoded,
// plus the working-tree diff the command made; any other tool's result passes
// through unchanged.
func HookToolResponse(ctx context.Context, toolName string, out any) any {
	if toolName != "shell" {
		return out
	}
	encoded, ok := out.(string)
	if !ok {
		return out
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(encoded), &obj); err != nil {
		return out
	}
	if completion, ok := ToolCompletionFromContext(ctx); ok {
		if diff, ok := DecodeShellEditDiff(completion.Output["shell_edit_diff"]); ok {
			obj["shell_edit_diff"] = diff
		}
	}
	return obj
}

// The knobs Claude Code's bashEditDiff carries as constants; they stay vars so
// tests can shrink them.
var (
	shellDiffMaxFiles        = 5                      // files shown with hunks
	shellDiffMaxHunkLines    = 400                    // per file
	shellDiffMaxHunkChars    = 64000                  // per file
	shellDiffMaxChangedFiles = 200                    // changed_files cap (for hooks, plan 131)
	shellDiffMaxHashBytes    = int64(20_000_000)      // bigger files are tracked by size+mtime, never hashed
	shellDiffMaxBlobBytes    = int64(5_000_000)       // a side bigger than this is not read for a diff
	shellDiffMaxExamined     = 50                     // candidates read before the rest is only counted
	shellDiffFirstTimeout    = 10 * time.Second       // the first snapshot also builds the private repo
	shellDiffTimeout         = 750 * time.Millisecond // 2s on windows
	shellDiffGiveUpAfter     = 2                      // consecutive failures per repository per process
	shellDiffArgBatchBytes   = 28000
	shellDiffGit             = "git" // the executable; tests point it at a missing binary to inject failures
)

// The empty blob in both object formats: a file created or deleted with no
// bytes shows as a header line with no hunks.
const (
	shellDiffEmptyBlobSHA1   = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"
	shellDiffEmptyBlobSHA256 = "473a0f4c3be8a93681a267e3b1e9a7dcda1185436fe141f7749120a303721813"
)

var shellDiffHashRe = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

var shellDiffGitWorktreeName = regexp.MustCompile(`^git~\d+$`)

// shellDiffPathValid mirrors git's worktree path rules closely enough for
// snapshot purposes: no absolute paths, control characters (including the
// Unicode spaces git itself rejects), dot segments, or .git-lookalike names.
func shellDiffPathValid(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") {
		return false
	}
	if runtime.GOOS == "windows" && strings.Contains(p, ":") {
		return false
	}
	for _, ch := range p {
		if ch < 0x20 || ch == 0x7f || (ch >= 0x80 && ch <= 0x9f) || ch == 0xa0 || ch == 0x3000 {
			return false
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		seg = strings.ToLower(strings.TrimRight(seg, ". "))
		if seg == ".git" || shellDiffGitWorktreeName.MatchString(seg) {
			return false
		}
	}
	return true
}

// shellDiffRepos is the process-level registry of repositories being watched,
// keyed by work tree root. The registry mutex also guards every repo.inflight
// map: a snapshot holds repo.mu for its whole duration, so inflight membership
// must not depend on it or a second concurrent command could only register
// after the first snapshot finished, and "shared" would be undetectable.
var shellDiffRepos = struct {
	sync.Mutex
	m map[string]*shellDiffRepo
}{m: map[string]*shellDiffRepo{}}

type shellDiffRepo struct {
	mu           sync.Mutex // serializes every git call against the private index
	top          string     // work tree root
	seedIndex    string     // <git-dir>/index — the user's index, copied once as the seed
	excludeFile  string     // <git-common-dir>/info/exclude
	objects      string     // <git-common-dir>/objects
	sha256       bool
	private      string // private GIT_DIR; "" until set up
	lastTree     string
	large        map[string]string // rel path -> "size mtimeUnixNano" of files not hashed
	largeChanged []string          // rel paths whose signature changed in the latest snapshot
	failures     int
	inflight     map[*shellDiffToken]bool // value: true once the token has seen another run
}

// shellDiffScrubEnv returns os.Environ() without every GIT_* variable, so no
// ambient git configuration leaks into the private repository.
func shellDiffScrubEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func shellDiffDiscoveryEnv() []string {
	return append(shellDiffScrubEnv(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
}

// privateEnv is the environment every git call against the private repository
// runs with: isolated configuration, no system files, no filters, no prompts.
// Only status asks for optional locks, so it can refresh the private index's
// stat cache on its way through.
func (r *shellDiffRepo) privateEnv(status bool) []string {
	env := append(shellDiffScrubEnv(),
		"GIT_DIR="+r.private,
		"GIT_WORK_TREE="+r.top,
		"GIT_CONFIG_GLOBAL="+filepath.Join(r.private, "config.global"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_TERMINAL_PROMPT=0",
	)
	if status {
		env = append(env, "GIT_OPTIONAL_LOCKS=1")
	} else {
		env = append(env, "GIT_OPTIONAL_LOCKS=0")
	}
	return env
}

// shellDiffRun runs one git invocation with a hard timeout, stdout and stderr
// kept apart so a warning can never corrupt parsed output.
func shellDiffRun(ctx context.Context, env []string, timeout time.Duration, stdin []byte, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = shellDiffTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, shellDiffGit, args...)
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if cctx.Err() != nil {
		return "", cctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (r *shellDiffRepo) gitEnv(ctx context.Context, env []string, timeout time.Duration, stdin []byte, sub ...string) (string, error) {
	args := append([]string{
		"-C", r.top, "--literal-pathspecs",
		"-c", "core.quotePath=false", "-c", "core.safecrlf=false", "-c", "core.askPass=",
	}, sub...)
	return shellDiffRun(ctx, env, timeout, stdin, args...)
}

func (r *shellDiffRepo) git(ctx context.Context, timeout time.Duration, stdin []byte, sub ...string) (string, error) {
	return r.gitEnv(ctx, r.privateEnv(false), timeout, stdin, sub...)
}

func shellDiffWriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

var shellDiffSweepOnce sync.Once

// shellDiffSweep removes private directories left behind by processes that
// died: anything not owned by this pid and untouched for a day.
func shellDiffSweep(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	prefix := fmt.Sprintf("%d-", os.Getpid())
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		os.RemoveAll(filepath.Join(base, e.Name()))
	}
}

func shellDiffHash12(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

func shellDiffRandHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(b)
}

// shellDiffRepoFor discovers the git repository cwd sits in and returns this
// process's watcher for it, or nil when cwd is not a work tree (or the
// repository has already failed too often).
func shellDiffRepoFor(ctx context.Context, cwd string) *shellDiffRepo {
	if strings.TrimSpace(cwd) == "" {
		return nil
	}
	out, err := shellDiffRun(ctx, shellDiffDiscoveryEnv(), 2*time.Second, nil,
		"-C", cwd, "rev-parse", "--path-format=absolute",
		"--show-toplevel", "--git-dir", "--git-common-dir", "--show-object-format")
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		return nil
	}
	top := strings.TrimSpace(lines[0])
	gitDir := strings.TrimSpace(lines[1])
	commonDir := strings.TrimSpace(lines[2])
	objectFormat := strings.TrimSpace(lines[3])
	if top == "" || gitDir == "" || commonDir == "" {
		return nil
	}
	shellDiffRepos.Lock()
	defer shellDiffRepos.Unlock()
	r := shellDiffRepos.m[top]
	if r == nil {
		r = &shellDiffRepo{
			top:      top,
			objects:  filepath.Join(commonDir, "objects"),
			sha256:   objectFormat == "sha256",
			inflight: map[*shellDiffToken]bool{},
		}
		if fi, err := os.Stat(filepath.Join(gitDir, "index")); err == nil && fi.Size() <= 256<<20 {
			r.seedIndex = filepath.Join(gitDir, "index")
		}
		r.excludeFile = filepath.Join(commonDir, "info", "exclude")
		shellDiffRepos.m[top] = r
	}
	if r.failures >= shellDiffGiveUpAfter {
		return nil
	}
	return r
}

// setup builds the private GIT_DIR under the system temp directory: an
// alternates link to the user's object store (reads shared, writes private),
// every filter and hook neutralized, and the user's index copied in as the
// seed so the first diff is against the user's own baseline rather than an
// empty tree.
func (r *shellDiffRepo) setup(ctx context.Context, timeout time.Duration) error {
	base := filepath.Join(os.TempDir(), "forebrain-shell-diff")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	shellDiffSweepOnce.Do(func() { shellDiffSweep(base) })
	private := filepath.Join(base, fmt.Sprintf("%d-%s-%s", os.Getpid(), shellDiffHash12(r.top), shellDiffRandHex(4)))
	for _, dir := range []string{
		filepath.Join(private, "objects", "info"),
		filepath.Join(private, "refs", "heads"),
		filepath.Join(private, "info"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	r.private = private
	if err := shellDiffWriteFile(filepath.Join(private, "HEAD"), []byte("ref: refs/heads/main\n")); err != nil {
		return err
	}
	format := "0"
	if r.sha256 {
		format = "1"
	}
	config := "[core]\n\trepositoryformatversion = " + format + "\n\tbare = false\n\tlogallrefupdates = false\n\tuntrackedCache = true\n"
	if r.sha256 {
		config += "[extensions]\n\tobjectformat = sha256\n"
	}
	if err := shellDiffWriteFile(filepath.Join(private, "config"), []byte(config)); err != nil {
		return err
	}
	if err := shellDiffWriteFile(filepath.Join(private, "objects", "info", "alternates"), []byte(r.objects+"\n")); err != nil {
		return err
	}
	// No working-tree transformation may ever run on snapshot content.
	if err := shellDiffWriteFile(filepath.Join(private, "info", "attributes"), []byte("* -filter -working-tree-encoding -text -eol -ident !diff\n")); err != nil {
		return err
	}
	var exclude []byte
	if data, err := os.ReadFile(r.excludeFile); err == nil && len(data) <= 64<<10 {
		exclude = data
	}
	if err := shellDiffWriteFile(filepath.Join(private, "info", "exclude"), exclude); err != nil {
		return err
	}
	var global []byte
	if path, err := shellDiffRun(ctx, shellDiffDiscoveryEnv(), 750*time.Millisecond, nil,
		"config", "--global", "--get", "--path", "core.excludesFile"); err == nil {
		path = strings.TrimSpace(path)
		if path != "" && filepath.IsAbs(path) && !strings.ContainsAny(path, "\n\r\x00") {
			quoted := strings.ReplaceAll(path, `\`, `\\`)
			quoted = strings.ReplaceAll(quoted, `"`, `\"`)
			global = []byte("[core]\n\texcludesFile = \"" + quoted + "\"\n")
		}
	}
	if err := shellDiffWriteFile(filepath.Join(private, "config.global"), global); err != nil {
		return err
	}
	if r.seedIndex != "" {
		if data, err := os.ReadFile(r.seedIndex); err == nil {
			indexPath := filepath.Join(private, "index")
			if err := shellDiffWriteFile(indexPath, data); err == nil {
				if bad, err := r.seedIndexValid(ctx, timeout); err != nil || bad {
					os.Remove(indexPath)
				}
			}
		}
	}
	if _, err := r.git(ctx, timeout, nil, "update-index", "--untracked-cache"); err != nil {
		return err
	}
	treeOut, err := r.git(ctx, timeout, nil, "write-tree")
	if err != nil {
		return err
	}
	tree := strings.TrimSpace(treeOut)
	if !shellDiffHashRe.MatchString(tree) {
		return fmt.Errorf("shell diff: write-tree returned %q", tree)
	}
	if _, err := r.commitTree(ctx, timeout, tree); err != nil {
		return err
	}
	r.lastTree = tree
	return nil
}

// seedIndexValid checks the copied index only holds plain stage-0 entries with
// usable paths; anything else restarts from an empty index.
func (r *shellDiffRepo) seedIndexValid(ctx context.Context, timeout time.Duration) (bool, error) {
	out, err := r.git(ctx, timeout, nil, "ls-files", "-z", "--stage")
	if err != nil {
		return false, err
	}
	for _, rec := range strings.Split(strings.TrimRight(out, "\x00"), "\x00") {
		if rec == "" {
			continue
		}
		tab := strings.IndexByte(rec, '\t')
		if tab < 0 {
			return false, nil
		}
		fields := strings.Fields(rec[:tab])
		if len(fields) < 3 || fields[len(fields)-1] != "0" {
			return false, nil
		}
		if !shellDiffPathValid(rec[tab+1:]) {
			return false, nil
		}
	}
	return true, nil
}

// commitTree pins a snapshot tree on the private refs/heads/main with fixed
// authorship, so nothing here ever depends on (or writes) the user's config.
func (r *shellDiffRepo) commitTree(ctx context.Context, timeout time.Duration, tree string) (string, error) {
	env := append(r.privateEnv(false),
		"GIT_AUTHOR_NAME=forebrain-shell-diff",
		"GIT_AUTHOR_EMAIL=forebrain-shell-diff@localhost",
		"GIT_AUTHOR_DATE=1000000000 +0000",
		"GIT_COMMITTER_NAME=forebrain-shell-diff",
		"GIT_COMMITTER_EMAIL=forebrain-shell-diff@localhost",
		"GIT_COMMITTER_DATE=1000000000 +0000",
	)
	commit, err := r.gitEnv(ctx, env, timeout, nil, "commit-tree", tree, "-m", "snapshot")
	if err != nil {
		return "", err
	}
	commit = strings.TrimSpace(commit)
	if !shellDiffHashRe.MatchString(commit) {
		return "", fmt.Errorf("shell diff: commit-tree returned %q", commit)
	}
	if err := shellDiffWriteFile(filepath.Join(r.private, "refs", "heads", "main"), []byte(commit+"\n")); err != nil {
		return "", err
	}
	return commit, nil
}

// snapshotFailed is the single recovery path: the private directory goes away,
// the next snapshot rebuilds it from scratch, and two consecutive failures in
// one repository end the watch for this process.
func (r *shellDiffRepo) snapshotFailed(err error) {
	os.RemoveAll(r.private)
	r.private = ""
	r.lastTree = ""
	r.large = nil
	r.largeChanged = nil
	r.failures++
	slog.Debug("shell diff: snapshot failed", "repo", r.top, "err", err)
	if r.failures == shellDiffGiveUpAfter {
		slog.Info("shell diff: giving up on repository for this process", "repo", r.top)
	}
}

func shellDiffTimeoutFor(first bool) time.Duration {
	if first {
		return shellDiffFirstTimeout
	}
	if runtime.GOOS == "windows" {
		return 2 * time.Second
	}
	return shellDiffTimeout
}

// snapshot records the work tree's state as a tree in the private repository
// and returns the tree hash. The caller holds r.mu.
func (r *shellDiffRepo) snapshot(ctx context.Context) (string, error) {
	timeout := shellDiffTimeoutFor(r.lastTree == "")
	if r.private == "" {
		if err := r.setup(ctx, timeout); err != nil {
			r.snapshotFailed(err)
			return "", err
		}
	}
	// A stale lock (this process died mid-update once) blocks every write.
	os.Remove(filepath.Join(r.private, "index.lock"))
	statusOut, err := r.gitEnv(ctx, r.privateEnv(true), timeout, nil,
		"status", "--porcelain=v2", "-z", "--untracked-files=normal", "--ignored=no", "--no-renames", "--ignore-submodules=all")
	if err != nil {
		r.snapshotFailed(err)
		return "", err
	}
	changed := map[string]struct{}{}
	deleted := map[string]struct{}{}
	var untrackedDirs []string
	// ? <path> / 1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path> /
	// u <XY> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>
	for _, rec := range strings.Split(statusOut, "\x00") {
		if len(rec) < 2 {
			continue
		}
		switch rec[0] {
		case '?':
			path := rec[2:]
			if strings.HasSuffix(path, "/") {
				untrackedDirs = append(untrackedDirs, strings.TrimSuffix(path, "/"))
			} else if shellDiffPathValid(path) {
				changed[path] = struct{}{}
			}
		case '1':
			// 1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path> — the path is
			// everything after the 8th space.
			fields := strings.SplitN(rec, " ", 9)
			if len(fields) < 9 || len(fields[1]) != 2 {
				continue
			}
			switch fields[1][1] {
			case '.':
				// index-only change; the work tree copy did not move
			case 'D':
				if shellDiffPathValid(fields[8]) {
					deleted[fields[8]] = struct{}{}
				}
			default:
				if shellDiffPathValid(fields[8]) {
					changed[fields[8]] = struct{}{}
				}
			}
		case 'u':
			// u <XY> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path> — the
			// path is everything after the 10th space.
			fields := strings.SplitN(rec, " ", 11)
			if len(fields) < 11 || len(fields[1]) != 2 {
				continue
			}
			switch fields[1][1] {
			case '.':
			case 'D':
				if shellDiffPathValid(fields[10]) {
					deleted[fields[10]] = struct{}{}
				}
			default:
				if shellDiffPathValid(fields[10]) {
					changed[fields[10]] = struct{}{}
				}
			}
		}
	}
	if len(changed) == 0 && len(deleted) == 0 && len(untrackedDirs) == 0 && r.lastTree != "" {
		r.largeChanged = nil
		return r.lastTree, nil
	}
	// A deleted path that is a directory again is an untracked tree, not a
	// removal.
	for path := range deleted {
		if fi, err := os.Lstat(filepath.Join(r.top, filepath.FromSlash(path))); err == nil && fi.IsDir() {
			delete(deleted, path)
			untrackedDirs = append(untrackedDirs, path)
		}
	}
	if err := r.expandUntrackedDirs(ctx, timeout, untrackedDirs, changed); err != nil {
		r.snapshotFailed(err)
		return "", err
	}
	if len(changed) == 0 && len(deleted) == 0 && r.lastTree != "" {
		r.largeChanged = nil
		return r.lastTree, nil
	}
	kept, newLarge, largeChanged := r.classifyChanged(changed)
	r.large = newLarge
	r.largeChanged = largeChanged
	if len(deleted) > 0 {
		paths := shellDiffSortedKeys(deleted)
		if _, err := r.git(ctx, timeout, shellDiffNullJoin(paths), "update-index", "--force-remove", "-z", "--stdin"); err != nil {
			r.snapshotFailed(err)
			return "", err
		}
	}
	if len(kept) > 0 {
		if _, err := r.git(ctx, timeout, shellDiffNullJoin(kept), "update-index", "--add", "--remove", "-z", "--stdin"); err != nil {
			r.snapshotFailed(err)
			return "", err
		}
	}
	treeOut, err := r.git(ctx, timeout, nil, "write-tree")
	if err != nil {
		r.snapshotFailed(err)
		return "", err
	}
	tree := strings.TrimSpace(treeOut)
	if !shellDiffHashRe.MatchString(tree) {
		err := fmt.Errorf("shell diff: write-tree returned %q", tree)
		r.snapshotFailed(err)
		return "", err
	}
	if tree != r.lastTree {
		if _, err := r.commitTree(ctx, timeout, tree); err != nil {
			r.snapshotFailed(err)
			return "", err
		}
	}
	r.lastTree = tree
	r.failures = 0
	return tree, nil
}

func shellDiffSortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func shellDiffNullJoin(paths []string) []byte {
	var b bytes.Buffer
	for _, p := range paths {
		b.WriteString(p)
		b.WriteByte(0)
	}
	return b.Bytes()
}

// expandUntrackedDirs lists the files under untracked directories in batches
// bounded by the argument size, and folds them into the changed set.
func (r *shellDiffRepo) expandUntrackedDirs(ctx context.Context, timeout time.Duration, dirs []string, changed map[string]struct{}) error {
	if len(dirs) == 0 {
		return nil
	}
	sort.Strings(dirs)
	var batch []string
	batchBytes := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		args := append([]string{"ls-files", "-z", "--others", "--exclude-standard", "--"}, batch...)
		out, err := r.git(ctx, timeout, nil, args...)
		if err != nil {
			return err
		}
		for _, p := range strings.Split(strings.TrimRight(out, "\x00"), "\x00") {
			if p == "" || strings.HasSuffix(p, "/") || !shellDiffPathValid(p) {
				continue
			}
			changed[p] = struct{}{}
		}
		batch = nil
		batchBytes = 0
		return nil
	}
	for _, dir := range dirs {
		arg := dir + "/"
		if batchBytes+len(arg)+1 > shellDiffArgBatchBytes {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, arg)
		batchBytes += len(arg) + 1
	}
	return flush()
}

// classifyChanged decides which changed paths enter the private index, which
// are too big to hash (tracked by size+mtime instead), and which are dropped
// (non-files under invalid parents). It returns the kept rel paths, the new
// large-file signature map, and the large paths whose signature moved.
func (r *shellDiffRepo) classifyChanged(changed map[string]struct{}) (kept []string, newLarge map[string]string, largeChanged []string) {
	newLarge = map[string]string{}
	dirCache := map[string]bool{}
	kept = make([]string, 0, len(changed))
	for _, rel := range shellDiffSortedKeys(changed) {
		abs := filepath.Join(r.top, filepath.FromSlash(rel))
		if !shellDiffParentDirsReal(abs, r.top, dirCache) {
			continue
		}
		fi, err := os.Lstat(abs)
		switch {
		case os.IsNotExist(err):
			// vanished between status and now; --remove settles it
			kept = append(kept, rel)
		case err != nil:
			// unreadable metadata: not something an index can represent
		case fi.Mode()&os.ModeSymlink != 0:
			kept = append(kept, rel)
		case fi.Mode().IsRegular():
			if fi.Size() > shellDiffMaxHashBytes {
				r.noteLarge(newLarge, rel, fmt.Sprintf("%d %d", fi.Size(), fi.ModTime().UnixNano()), &largeChanged)
				continue
			}
			f, openErr := os.Open(abs)
			if openErr != nil {
				if os.IsPermission(openErr) {
					r.noteLarge(newLarge, rel, "unreadable", &largeChanged)
				}
				continue
			}
			f.Close()
			kept = append(kept, rel)
		default:
			// directory, FIFO, device, socket: no snapshot meaning
		}
	}
	return kept, newLarge, largeChanged
}

func (r *shellDiffRepo) noteLarge(newLarge map[string]string, rel, sig string, largeChanged *[]string) {
	newLarge[rel] = sig
	if old, ok := r.large[rel]; !ok || old != sig {
		*largeChanged = append(*largeChanged, rel)
	}
}

// shellDiffParentDirsReal reports whether every directory between top and
// abs's parent is a real directory (not a symlink), caching per snapshot.
func shellDiffParentDirsReal(abs, top string, cache map[string]bool) bool {
	dir := filepath.Dir(abs)
	for dir != top {
		if dir == string(filepath.Separator) || len(dir) <= len(top) {
			return dir == top
		}
		ok, cached := cache[dir]
		if !cached {
			fi, err := os.Lstat(dir)
			ok = err == nil && fi.IsDir()
			cache[dir] = ok
		}
		if !ok {
			return false
		}
		dir = filepath.Dir(dir)
	}
	return true
}

type shellDiffTreeRecord struct {
	sha1   string
	sha2   string
	status string
	path   string
}

func shellDiffAbsPath(top, rel string) string {
	return filepath.Join(top, filepath.FromSlash(rel))
}

// unavailableDiff is what a run reports when only the count is known: the
// trees are unchanged (or hold no readable records) but large files moved.
func (r *shellDiffRepo) unavailableDiff() *ShellEditDiff {
	changed := shellDiffAbsPaths(r.top, r.largeChanged)
	return &ShellEditDiff{
		MoreFiles:    len(r.largeChanged),
		ChangedFiles: changed,
		Unavailable:  true,
	}
}

func shellDiffAbsPaths(top string, rels []string) []string {
	out := make([]string, 0, len(rels))
	for _, rel := range rels {
		out = append(out, shellDiffAbsPath(top, rel))
	}
	if len(out) > shellDiffMaxChangedFiles {
		out = out[:shellDiffMaxChangedFiles]
	}
	return out
}

// diffSince snapshots again and reports what moved since before. The caller
// holds r.mu.
func (r *shellDiffRepo) diffSince(ctx context.Context, before, cwd string) (*ShellEditDiff, error) {
	after, err := r.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if after == before {
		if len(r.largeChanged) > 0 {
			return r.unavailableDiff(), nil
		}
		return nil, nil
	}
	timeout := shellDiffTimeoutFor(false)
	out, err := r.git(ctx, timeout, nil,
		"diff-tree", "-r", "-z", "--raw", "--no-renames", "--no-ext-diff", "--no-textconv", before, after)
	if err != nil {
		return nil, err
	}
	// :<mode1> <mode2> <sha1> <sha2> <status>\x00<path>\x00
	var records []shellDiffTreeRecord
	fields := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		meta, path := fields[i], fields[i+1]
		parts := strings.Fields(strings.TrimPrefix(meta, ":"))
		if len(parts) < 5 || parts[0] == "" || !shellDiffPathValid(path) {
			continue
		}
		records = append(records, shellDiffTreeRecord{
			sha1: parts[2], sha2: parts[3],
			status: string(parts[4][0]), path: path,
		})
	}
	if len(records) == 0 {
		if len(r.largeChanged) > 0 {
			return r.unavailableDiff(), nil
		}
		return nil, nil
	}
	emptyBlob := shellDiffEmptyBlobSHA1
	if r.sha256 {
		emptyBlob = shellDiffEmptyBlobSHA256
	}
	var files []ShellEditDiffFile
	var rest []shellDiffTreeRecord
	for _, rec := range records {
		if len(files) >= shellDiffMaxFiles {
			rest = append(rest, rec)
			continue
		}
		// Empty files first: they carry no hunks at all.
		if rec.status == "A" && rec.sha2 == emptyBlob {
			abs := shellDiffAbsPath(r.top, rec.path)
			files = append(files, ShellEditDiffFile{
				Path: abs, DisplayPath: displayPathRelativeTo(cwd, abs), Status: "created",
			})
			continue
		}
		if rec.status == "D" && rec.sha1 == emptyBlob {
			abs := shellDiffAbsPath(r.top, rec.path)
			files = append(files, ShellEditDiffFile{
				Path: abs, DisplayPath: displayPathRelativeTo(cwd, abs), Status: "deleted",
			})
			continue
		}
		rest = append(rest, rec)
	}
	examined := 0
	for _, rec := range rest {
		if len(files) >= shellDiffMaxFiles || examined >= shellDiffMaxExamined {
			break
		}
		examined++
		file, ok, err := r.selectRecord(ctx, timeout, rec, cwd)
		if err != nil {
			return nil, err
		}
		if ok {
			files = append(files, file)
		}
	}
	changed := make([]string, 0, len(records)+len(r.largeChanged))
	for _, rec := range records {
		changed = append(changed, shellDiffAbsPath(r.top, rec.path))
	}
	changed = append(changed, shellDiffAbsPaths(r.top, r.largeChanged)...)
	if len(changed) > shellDiffMaxChangedFiles {
		changed = changed[:shellDiffMaxChangedFiles]
	}
	return &ShellEditDiff{
		Files:        files,
		MoreFiles:    len(records) - len(files) + len(r.largeChanged),
		ChangedFiles: changed,
		Unavailable:  len(r.largeChanged) > 0,
	}, nil
}

// selectRecord reads one record's two blob sides and turns them into a diff
// file, or reports that the record must only be counted (binary, mode-only,
// oversized, over the hunk limits).
func (r *shellDiffRepo) selectRecord(ctx context.Context, timeout time.Duration, rec shellDiffTreeRecord, cwd string) (ShellEditDiffFile, bool, error) {
	var oldBytes, newBytes []byte
	if rec.status != "A" {
		data, ok, err := r.readBlob(ctx, timeout, rec.sha1)
		if err != nil || !ok {
			return ShellEditDiffFile{}, false, err
		}
		oldBytes = data
	}
	if rec.status != "D" {
		data, ok, err := r.readBlob(ctx, timeout, rec.sha2)
		if err != nil || !ok {
			return ShellEditDiffFile{}, false, err
		}
		newBytes = data
	}
	if shellDiffLooksBinary(oldBytes) || shellDiffLooksBinary(newBytes) || bytes.Equal(oldBytes, newBytes) {
		return ShellEditDiffFile{}, false, nil
	}
	abs := shellDiffAbsPath(r.top, rec.path)
	sum, err := event.Build(abs, oldBytes, newBytes)
	if err != nil {
		return ShellEditDiffFile{}, false, err
	}
	lines, chars := diffHunkUsage(sum.UnifiedDiff)
	if lines > shellDiffMaxHunkLines || chars > shellDiffMaxHunkChars {
		return ShellEditDiffFile{}, false, nil
	}
	status := "updated"
	switch rec.status {
	case "A":
		status = "created"
	case "D":
		status = "deleted"
	}
	return ShellEditDiffFile{
		Path: abs, DisplayPath: displayPathRelativeTo(cwd, abs), Status: status,
		Added: sum.Added, Deleted: sum.Deleted, UnifiedDiff: sum.UnifiedDiff,
	}, true, nil
}

func (r *shellDiffRepo) readBlob(ctx context.Context, timeout time.Duration, sha string) ([]byte, bool, error) {
	sizeOut, err := r.git(ctx, timeout, nil, "cat-file", "-s", sha)
	if err != nil {
		return nil, false, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(sizeOut), 10, 64)
	if err != nil {
		return nil, false, err
	}
	if size > shellDiffMaxBlobBytes {
		return nil, false, nil
	}
	blob, err := r.git(ctx, timeout, nil, "cat-file", "blob", sha)
	if err != nil {
		return nil, false, err
	}
	return []byte(blob), true, nil
}

func shellDiffLooksBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	return bytes.IndexByte(b, 0) >= 0
}

// shellDiffToken identifies one in-flight run. The dummy byte keeps it from
// being zero-sized: Go hands every zero-size allocation the same address, and
// two runs would then share one inflight-map key.
type shellDiffToken struct{ _ byte }

// shellEditDiffRun is one shell call's watch over the repository it runs in.
type shellEditDiffRun struct {
	repo        *shellDiffRepo
	token       *shellDiffToken
	before      string
	unavailable bool
	skipped     bool
	cwd         string
	left        bool
	shared      bool
	startedAt   time.Time
	// externalWrites is the seam a test injects to name the sibling-tool
	// writes inside its window; nil means the process-wide tool-write ledger.
	externalWrites func(from, to time.Time) map[string]struct{}
}

// enter registers a token as in flight; a second registration marks every
// in-flight run shared.
func (r *shellDiffRepo) enter(t *shellDiffToken) {
	shellDiffRepos.Lock()
	defer shellDiffRepos.Unlock()
	if r.inflight == nil {
		r.inflight = map[*shellDiffToken]bool{}
	}
	r.inflight[t] = false
	if len(r.inflight) > 1 {
		for k := range r.inflight {
			r.inflight[k] = true
		}
	}
}

// release is idempotent and safe on nil receivers and skipped runs.
func (r *shellEditDiffRun) release() {
	if r == nil || r.repo == nil || r.left {
		return
	}
	r.left = true
	shellDiffRepos.Lock()
	shared := r.repo.inflight[r.token]
	delete(r.repo.inflight, r.token)
	shellDiffRepos.Unlock()
	if shared {
		r.shared = true
	}
}

// beginShellEditDiff starts watching the repository a mutating command is
// about to run in: one snapshot before, one after, the pair compared for the
// card. Read-only commands and whole-command git tree moves never snapshot.
func beginShellEditDiff(ctx context.Context, command, cwd string, readOnly bool) *shellEditDiffRun {
	if readOnly {
		return nil
	}
	if shellCommandMovesGitTreeAlone(command) {
		return &shellEditDiffRun{skipped: true}
	}
	repo := shellDiffRepoFor(ctx, cwd)
	if repo == nil {
		return nil
	}
	run := &shellEditDiffRun{repo: repo, token: &shellDiffToken{}, cwd: cwd, startedAt: time.Now()}
	repo.enter(run.token)
	repo.mu.Lock()
	before, err := repo.snapshot(ctx)
	repo.mu.Unlock()
	if err != nil {
		run.unavailable = true
	} else {
		run.before = before
	}
	return run
}

// finish compares against the before snapshot once the command completed
// successfully (per the exit-code semantics below); anything else releases the
// run and reports nothing.
func (r *shellEditDiffRun) finish(ctx context.Context, command string, exitCode int, runErr error) *ShellEditDiff {
	if r == nil {
		return nil
	}
	if ctx.Err() != nil || runErr != nil || shellExitIsFailure(command, exitCode) {
		r.release()
		return nil
	}
	r.release()
	if r.skipped {
		return &ShellEditDiff{Files: []ShellEditDiffFile{}, Skipped: true}
	}
	if r.unavailable {
		return &ShellEditDiff{Files: []ShellEditDiffFile{}, Unavailable: true, Shared: r.shared}
	}
	r.repo.mu.Lock()
	diff, err := r.repo.diffSince(ctx, r.before, r.cwd)
	r.repo.mu.Unlock()
	if err != nil {
		return &ShellEditDiff{Files: []ShellEditDiffFile{}, Unavailable: true, Shared: r.shared}
	}
	if diff != nil {
		external := r.externalWrites
		if external == nil {
			external = toolWritesBetween
		}
		// The snapshot pair answers "what changed on disk during the window",
		// not "what did this command change": subtract the paths sibling
		// tools announced inside it (plan 180).
		diff = dropExternalWrites(diff, external(r.startedAt, time.Now()))
	}
	if diff == nil {
		if r.shared {
			return &ShellEditDiff{Files: []ShellEditDiffFile{}, Shared: true}
		}
		return nil
	}
	diff.Shared = r.shared
	diff.TreeMoved = shellCommandMovesGitTree(command)
	return diff
}

// dropExternalWrites removes the paths other tools wrote from a shell diff. A
// snapshot pair cannot tell who made a change, so anything the tool-write
// ledger claims from the window is not this command's work. A diff left with
// no file of its own becomes nil — the card then shows the command output
// alone, which is the truth of a command like `go test`.
func dropExternalWrites(diff *ShellEditDiff, external map[string]struct{}) *ShellEditDiff {
	if diff == nil || len(external) == 0 {
		return diff
	}
	droppedFromFiles := 0
	files := make([]ShellEditDiffFile, 0, len(diff.Files))
	for _, f := range diff.Files {
		if _, hit := external[resolveForLedger(f.Path)]; hit {
			droppedFromFiles++
			continue
		}
		files = append(files, f)
	}
	changed := make([]string, 0, len(diff.ChangedFiles))
	dropped := 0
	for _, p := range diff.ChangedFiles {
		if _, hit := external[resolveForLedger(p)]; hit {
			dropped++
			continue
		}
		changed = append(changed, p)
	}
	// MoreFiles counts the records that did not fit in Files, so only the
	// dropped paths that were not already shown add to it.
	more := diff.MoreFiles - (dropped - droppedFromFiles)
	if more < 0 {
		more = 0
	}
	if len(files) == 0 && len(changed) == 0 && more == 0 {
		return nil
	}
	diff.Files = files
	diff.ChangedFiles = changed
	diff.MoreFiles = more
	return diff
}

// shellExitIsFailure maps an exit code to "did this command fail" using the
// semantics of its last segment: search tools report findings through code 1,
// so only 2+ is failure for them (and for git diff / git grep).
func shellExitIsFailure(command string, code int) bool {
	segs := safety.SplitTopLevelWithSeparators(command)
	last := ""
	if len(segs) > 0 {
		last = segs[len(segs)-1].Text
	}
	fields := strings.Fields(last)
	if len(fields) == 0 {
		return code != 0
	}
	switch fields[0] {
	case "grep", "rg", "egrep", "fgrep", "find", "diff", "test", "[":
		return code >= 2
	}
	if fields[0] == "git" {
		switch shellDiffGitSubcommand(fields) {
		case "diff", "grep":
			return code >= 2
		}
	}
	return code != 0
}

// shellDiffGitSubcommand returns the first word after git that is not an
// option, skipping the value of -C/-c.
func shellDiffGitSubcommand(fields []string) string {
	for i := 1; i < len(fields); i++ {
		w := fields[i]
		if w == "-C" || w == "-c" {
			i++
			continue
		}
		if strings.HasPrefix(w, "-") {
			continue
		}
		return w
	}
	return ""
}

// The git subcommands that move the working tree themselves; a diff under
// them would attribute git's moves to the command.
var shellDiffGitTreeMovers = map[string]bool{
	"checkout": true, "switch": true, "stash": true, "pull": true, "merge": true,
	"rebase": true, "reset": true, "restore": true, "clean": true,
	"cherry-pick": true, "revert": true,
}

// shellDiffTreeMoveAlone matches a command that is nothing but one of those
// git subcommands (with an optional sudo).
var shellDiffTreeMoveAlone = regexp.MustCompile("^[ \t]*(?:sudo[ \t]+)?git[ \t]+(?:checkout|switch|stash|pull|merge|rebase|reset|restore|clean|cherry-pick|revert)(?:[ \t]+[A-Za-z0-9._/@~^][A-Za-z0-9._/@~^-]*){0,16}\\s*$")

func shellCommandMovesGitTreeAlone(command string) bool {
	return shellDiffTreeMoveAlone.MatchString(command)
}

// shellCommandMovesGitTree reports whether any top-level segment of the
// command is one of the tree-moving git subcommands.
func shellCommandMovesGitTree(command string) bool {
	for _, seg := range safety.SplitTopLevelWithSeparators(command) {
		fields := safety.SplitShellWords(seg.Text)
		if len(fields) > 0 && fields[0] == "sudo" {
			fields = fields[1:]
		}
		if len(fields) > 0 && fields[0] == "git" && shellDiffGitTreeMovers[shellDiffGitSubcommand(fields)] {
			return true
		}
	}
	return false
}

// CloseShellDiffRepos detaches every watcher and removes its private repository
// directory, then leaves the registry empty. It runs once as the process exits
// (cmd/forebrain's Execute): without it a run that captured one shell command
// leaves a private git dir — with a copy of the user's index — behind until some
// other process's 24-hour sweep finds it. Safe to call more than once.
func CloseShellDiffRepos() {
	shellDiffRepos.Lock()
	tops := make([]string, 0, len(shellDiffRepos.m))
	for top := range shellDiffRepos.m {
		tops = append(tops, top)
	}
	shellDiffRepos.Unlock()
	for _, top := range tops {
		forgetShellDiffRepo(top)
	}
}

// forgetShellDiffRepo detaches one repository's watcher and removes its private
// directory. The test fixtures call it between cases; CloseShellDiffRepos calls
// it for every repository at exit.
func forgetShellDiffRepo(top string) {
	shellDiffRepos.Lock()
	r := shellDiffRepos.m[top]
	delete(shellDiffRepos.m, top)
	shellDiffRepos.Unlock()
	if r != nil {
		os.RemoveAll(r.private)
	}
}
