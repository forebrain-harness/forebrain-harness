package home

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var ErrPathNotAllowed = errors.New("path not allowed")

func NormalizeForSecurity(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	return pathCleanKeepRelative(p)
}

func ContainsTraversal(p string) bool {
	raw := strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	p = NormalizeForSecurity(p)
	if p == "" {
		return false
	}
	if strings.ContainsRune(raw, 0) {
		return true
	}
	if strings.HasPrefix(raw, "../") || strings.Contains(raw, "/../") || strings.HasSuffix(raw, "/..") || raw == ".." {
		return true
	}
	if len(raw) >= 2 && ((raw[1] == ':' && isAlpha(raw[0])) || strings.HasPrefix(raw, "//")) {
		return true
	}
	return false
}

func ValidateArchiveRelPath(p string) error {
	raw := strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	p = NormalizeForSecurity(p)
	if p == "" || p == "." {
		return errors.New("empty archive path")
	}
	if strings.HasPrefix(raw, "/") {
		return ErrPathNotAllowed
	}
	if ContainsTraversal(p) {
		return ErrPathNotAllowed
	}
	return nil
}

func ResolveWithinRoots(p string, roots []string) (string, error) {
	raw := strings.TrimSpace(p)
	if raw == "" {
		return "", errors.New("empty path")
	}
	if strings.ContainsRune(raw, 0) {
		return "", ErrPathNotAllowed
	}
	p = raw
	if p == "" {
		return "", errors.New("empty path")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if !isWithinRoots(abs, roots) {
		return "", ErrPathNotAllowed
	}
	// For existing paths, enforce realpath stays in allowed roots.
	if st, err := os.Stat(abs); err == nil {
		if st.Mode()&os.ModeSymlink != 0 {
			resolved, rerr := filepath.EvalSymlinks(abs)
			if rerr != nil {
				return "", rerr
			}
			if !isWithinRoots(filepath.Clean(resolved), roots) {
				return "", ErrPathNotAllowed
			}
		} else if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
			if !isWithinRoots(filepath.Clean(resolved), roots) {
				return "", ErrPathNotAllowed
			}
		}
		return abs, nil
	}
	// For non-existing paths, validate nearest existing parent after symlink resolve.
	parent := abs
	for {
		if _, err := os.Stat(parent); err == nil {
			resolvedParent, rerr := filepath.EvalSymlinks(parent)
			if rerr != nil {
				return "", rerr
			}
			rel, rerr := filepath.Rel(parent, abs)
			if rerr != nil {
				return "", rerr
			}
			resolvedAbs := filepath.Clean(filepath.Join(resolvedParent, rel))
			if !isWithinRoots(resolvedAbs, roots) {
				return "", ErrPathNotAllowed
			}
			break
		}
		next := filepath.Dir(parent)
		if next == parent {
			break
		}
		parent = next
	}
	return abs, nil
}

func SafeMkdirAllUnderRoot(root, target string, mode os.FileMode) error {
	root = filepath.Clean(strings.TrimSpace(root))
	target = filepath.Clean(strings.TrimSpace(target))
	if root == "" || target == "" {
		return errors.New("empty path")
	}
	if _, err := ResolveWithinRoots(target, []string{root}); err != nil {
		return err
	}
	current := root
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		if fi, err := os.Lstat(current); err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return ErrPathNotAllowed
			}
			continue
		}
		if err := os.Mkdir(current, mode); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nil
}

func isWithinRoots(abs string, roots []string) bool {
	pathCandidates := canonicalCandidates(abs)
	for _, r := range roots {
		rootCandidates := canonicalCandidates(r)
		for _, root := range rootCandidates {
			for _, path := range pathCandidates {
				if path == root {
					return true
				}
				rel, err := filepath.Rel(root, path)
				if err != nil {
					continue
				}
				if rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..") {
					return true
				}
			}
		}
	}
	return false
}

func canonicalCandidates(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	seen := map[string]struct{}{}
	add := func(v string) {
		v = filepath.Clean(strings.TrimSpace(v))
		if v == "" {
			return
		}
		seen[v] = struct{}{}
	}
	add(raw)
	if abs, err := filepath.Abs(raw); err == nil {
		add(abs)
	}
	if real, err := filepath.EvalSymlinks(raw); err == nil {
		add(real)
	}
	if viaParent, ok := canonicalViaNearestExisting(raw); ok {
		add(viaParent)
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func canonicalViaNearestExisting(raw string) (string, bool) {
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", false
	}
	abs = filepath.Clean(abs)
	parent := abs
	for {
		if _, err := os.Stat(parent); err == nil {
			resolvedParent, rerr := filepath.EvalSymlinks(parent)
			if rerr != nil {
				return "", false
			}
			rel, rerr := filepath.Rel(parent, abs)
			if rerr != nil {
				return "", false
			}
			if rel == "." {
				return filepath.Clean(resolvedParent), true
			}
			return filepath.Clean(filepath.Join(resolvedParent, rel)), true
		}
		next := filepath.Dir(parent)
		if next == parent {
			break
		}
		parent = next
	}
	return "", false
}

func pathCleanKeepRelative(p string) string {
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "/") {
		return pathLikeClean(p)
	}
	return strings.TrimPrefix(pathLikeClean("/"+p), "/")
}

func pathLikeClean(p string) string {
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			} else {
				out = append(out, "..")
			}
		default:
			out = append(out, part)
		}
	}
	if strings.HasPrefix(p, "/") {
		return "/" + strings.Join(out, "/")
	}
	return strings.Join(out, "/")
}

func isAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
