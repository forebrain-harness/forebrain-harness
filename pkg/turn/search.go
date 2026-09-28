package turn

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Candidate is one row in the @ mention typeahead. Directory paths
// carry a trailing "/" so acceptance drills down instead of closing.
type Candidate struct {
	Path  string
	IsDir bool
}

// Upstream parity constants (see spec "Key Parity Constants").
const (
	maxSuggestions = 15
	indexRefresh   = 5 * time.Second
	walkEntryCap   = 20000
	gitTimeout     = 5 * time.Second
)

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, ".idea": true, ".vscode": true, "vendor": true,
}

type indexEntry struct {
	candidates []Candidate
	builtAt    time.Time
}

// candidateIndex caches one file listing per root. It is keyed rather than
// single-slot because a server process answers for many workspaces at once,
// and a single slot would make two sessions on different roots rebuild the
// index on every keystroke.
var candidateIndex struct {
	mu    sync.Mutex
	roots map[string]indexEntry
}

// Prewarm builds the index for root off the critical path, so the first
// keystroke after "@" does not pay for the whole walk.
func Prewarm(root string) {
	if strings.TrimSpace(root) == "" {
		return
	}
	_ = ensureIndex(root)
}

func ensureIndex(root string) []Candidate {
	candidateIndex.mu.Lock()
	defer candidateIndex.mu.Unlock()
	if entry, ok := candidateIndex.roots[root]; ok && time.Since(entry.builtAt) < indexRefresh {
		return entry.candidates
	}
	files, err := gitFiles(root)
	if err != nil {
		files = walkFiles(root, walkEntryCap)
	}
	entry := indexEntry{candidates: candidatesFromFiles(files), builtAt: time.Now()}
	if candidateIndex.roots == nil {
		candidateIndex.roots = map[string]indexEntry{}
	}
	candidateIndex.roots[root] = entry
	return entry.candidates
}

func gitFiles(root string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	tracked, err := gitLsFiles(ctx, root, "--recurse-submodules")
	if err != nil {
		return nil, err
	}
	tracked = presentOnDisk(root, tracked)
	untracked, err := gitLsFiles(ctx, root, "--others", "--exclude-standard")
	if err != nil {
		untracked = nil
	}
	return append(tracked, untracked...), nil
}

// presentOnDisk drops the tracked paths that no longer exist in the working
// tree. "git ls-files" reports the index, which keeps naming a file until its
// deletion is staged, so a directory removed with plain "rm -rf" would go on
// being offered by the @ picker. The untracked listing needs no such pass:
// git finds those by walking the tree, so they exist by construction.
func presentOnDisk(root string, files []string) []string {
	kept := files[:0]
	for _, f := range files {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(f))); err != nil {
			continue
		}
		kept = append(kept, f)
	}
	return kept
}

func gitLsFiles(ctx context.Context, root string, args ...string) ([]string, error) {
	cmdArgs := append([]string{"-c", "core.quotepath=false", "ls-files"}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

func walkFiles(root string, limit int) []string {
	var files []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		if len(files) >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	return files
}

func candidatesFromFiles(files []string) []Candidate {
	dirSet := map[string]bool{}
	fileSet := map[string]bool{}
	for _, f := range files {
		f = filepath.ToSlash(strings.TrimSpace(f))
		if f == "" || fileSet[f] {
			continue
		}
		fileSet[f] = true
		for d := filepath.ToSlash(filepath.Dir(f)); d != "." && d != "/" && d != ""; d = filepath.ToSlash(filepath.Dir(d)) {
			dirSet[d] = true
		}
	}
	out := make([]Candidate, 0, len(fileSet)+len(dirSet))
	for d := range dirSet {
		out = append(out, Candidate{Path: d + "/", IsDir: true})
	}
	for f := range fileSet {
		out = append(out, Candidate{Path: f})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func Search(root, query string) []Candidate {
	query = strings.TrimSpace(query)
	switch {
	case query == "" || query == "." || query == "./":
		return topLevelCandidates(root)
	case pathLikeQuery(query):
		return scanDir(root, query)
	}
	cands := ensureIndex(root)
	paths := make([]string, len(cands))
	for i, c := range cands {
		paths[i] = c.Path
	}
	ranked := fuzzyRank(paths, query)
	if len(ranked) > maxSuggestions {
		ranked = ranked[:maxSuggestions]
	}
	out := make([]Candidate, 0, len(ranked))
	for _, idx := range ranked {
		out = append(out, cands[idx])
	}
	return out
}

func pathLikeQuery(q string) bool {
	return strings.HasPrefix(q, "./") || strings.HasPrefix(q, "../") ||
		strings.HasPrefix(q, "~/") || strings.HasPrefix(q, "/") ||
		q == ".." || q == "~"
}

func topLevelCandidates(root string) []Candidate {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var dirs, files []Candidate
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || skipDirs[name] {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, Candidate{Path: name + "/", IsDir: true})
		} else {
			files = append(files, Candidate{Path: name})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Path < dirs[j].Path })
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	out := append(dirs, files...)
	if len(out) > maxSuggestions {
		out = out[:maxSuggestions]
	}
	return out
}

func scanDir(root, query string) []Candidate {
	q := query
	if q == ".." {
		q = "../"
	}
	if q == "~" {
		q = "~/"
	}
	trailingSlash := strings.HasSuffix(q, "/")
	expanded := q
	if strings.HasPrefix(q, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		expanded = filepath.Join(home, strings.TrimPrefix(q, "~/"))
	} else if !filepath.IsAbs(q) {
		expanded = filepath.Join(root, q)
	} else {
		expanded = filepath.Clean(q)
	}
	dir := expanded
	prefix := ""
	if !trailingSlash {
		dir = filepath.Dir(expanded)
		prefix = strings.ToLower(filepath.Base(expanded))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	qd := q
	if !trailingSlash {
		if i := strings.LastIndex(q, "/"); i >= 0 {
			qd = q[:i+1]
		} else {
			qd = ""
		}
	}
	var dirs, files []Candidate
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if prefix != "" && !strings.HasPrefix(strings.ToLower(name), prefix) {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, Candidate{Path: qd + name + "/", IsDir: true})
		} else {
			files = append(files, Candidate{Path: qd + name})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Path < dirs[j].Path })
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	out := append(dirs, files...)
	if len(out) > maxSuggestions {
		out = out[:maxSuggestions]
	}
	return out
}
