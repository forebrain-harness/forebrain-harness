package assembly

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

// ProjectRoot resolves the project root for a CWD:
// walk up looking for a `.git` marker and return that repo root; if
// none is found, return the cleaned absolute path of the CWD itself. Mirrors
// memory.canonicalProjectRoot / hasGitMarker (duplicated rather than shared to
// avoid an import edge for a few lines of path logic).
func ProjectRoot(path string) string {
	abs := resolvePath(path)
	if abs == "" {
		return ""
	}
	cur := abs
	for {
		if hasGitMarker(cur) {
			return filepath.Clean(cur)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return filepath.Clean(abs)
}

// resolvePath returns the cleaned, absolute, symlink-resolved form of path so
// CWD and project root are compared consistently (e.g. macOS /var vs
// /private/var). Returns "" for an empty/unresolvable path.
func resolvePath(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	if eval, err := filepath.EvalSymlinks(abs); err == nil && strings.TrimSpace(eval) != "" {
		return filepath.Clean(eval)
	}
	return filepath.Clean(abs)
}

func hasGitMarker(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		return false
	}
	return st.IsDir() || st.Mode().IsRegular()
}

// ProjectRuleFile is a single FOREBRAIN.md found under the CWD's project root.
type ProjectRuleFile struct {
	Path string
	Body string
	// Truncated reports that Body was cut to the per-file byte budget.
	Truncated bool
}

// ProjectForebrainFiles reads FOREBRAIN.md from the CWD's project root (git root, or
// the CWD itself). When chain is true it also reads any nested FOREBRAIN.md from
// the project root down to the CWD, returned root-first. Bodies have front matter stripped, are trimmed, and are
// truncated to maxBytes. Non-regular files and symlinks are skipped.
func ProjectForebrainFiles(wd string, chain bool, maxBytes int) []ProjectRuleFile {
	wd = resolvePath(wd)
	if wd == "" {
		return nil
	}
	proj := ProjectRoot(wd)
	if proj == "" {
		return nil
	}
	dirs := []string{proj}
	if chain {
		dirs = projectDirChain(proj, wd)
	}
	var out []ProjectRuleFile
	seen := make(map[string]struct{})
	for _, d := range dirs {
		path := filepath.Join(d, "FOREBRAIN.md")
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		body, truncated, ok := readProjectMarkdown(path, maxBytes)
		if !ok {
			continue
		}
		out = append(out, ProjectRuleFile{Path: path, Body: body, Truncated: truncated})
	}
	return out
}

// projectDirChain lists directories from root down to cwd inclusive (root-first).
// cwd must be root or a descendant; otherwise only root is returned.
func projectDirChain(root, cwd string) []string {
	root = filepath.Clean(strings.TrimSpace(root))
	cwd = filepath.Clean(strings.TrimSpace(cwd))
	if root == "" || cwd == "" {
		return nil
	}
	if cwd == root {
		return []string{root}
	}
	sep := string(filepath.Separator)
	if !strings.HasPrefix(cwd, root+sep) {
		return []string{root}
	}
	var chain []string
	cur := cwd
	for {
		chain = append([]string{cur}, chain...)
		if cur == root {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return chain
}

func readProjectMarkdown(path string, maxBytes int) (body string, truncated bool, ok bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", false, false
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", false, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false, false
	}
	body = strings.TrimSpace(home.StripFrontMatter(string(raw)))
	if body == "" {
		return "", false, false
	}
	return TruncateString(body, maxBytes), maxBytes > 0 && len(body) > maxBytes, true
}

func TruncateString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "\n…"
}
