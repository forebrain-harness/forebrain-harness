package home

import (
	"embed"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	DefaultMaxBytesPerFile = 12_000
)

//go:embed templates/*.md
var templateFS embed.FS

var (
	osMkdirAllFn           = os.MkdirAll
	osOpenFn               = os.Open
	osLstatFn              = os.Lstat
	openWorkspaceRootFn    = openWorkspaceRootNoFollow
	writeFileInWorkspaceFn = writeFileIfMissingInWorkspace
	filepathEvalSymlinksFn = filepath.EvalSymlinks
	filepathRelFn          = filepath.Rel
	readAllFn              = io.ReadAll
	templateBodyFn         = templateBody
)

var (
	errWorkspaceSymlink = errors.New("workspace path contains symlink")
	errOutsideWorkspace = errors.New("path outside workspace")
)

type EnsureOptions struct{}

type LoadOptions struct {
	CWD                string
	Chain              bool
	MaxBytesPerFile    int
	IncludeDiagnostics bool
}

type BootstrapFile struct {
	Name       string
	Path       string
	Body       string
	Diagnostic string
	// Truncated reports that Body was cut to the per-file byte budget.
	Truncated bool
}

var orderedBootstrapFiles = []string{
	"AGENTS.md",
	"SOUL.md",
	"USER.md",
}

var coreBootstrapFiles = []string{
	"AGENTS.md",
	"SOUL.md",
	"USER.md",
}

func WorkspaceDir(home string) string {
	h := strings.TrimSpace(home)
	if h == "" {
		return ""
	}
	return filepath.Clean(filepath.Join(h, "workspace"))
}

func EnsureWorkspace(home string, opts EnsureOptions) error {
	if strings.TrimSpace(home) == "" {
		return errors.New("workspace home is empty")
	}
	return EnsureWorkspaceRoot(WorkspaceDir(home), opts)
}

func EnsureWorkspaceRoot(workspaceRoot string, _ EnsureOptions) error {
	ws, err := validateWorkspaceRoot(workspaceRoot)
	if err != nil {
		return err
	}
	if err := osMkdirAllFn(ws, 0o755); err != nil {
		return err
	}
	rootDir, err := openWorkspaceRootFn(ws)
	if err != nil {
		return err
	}
	defer rootDir.Close()
	for _, name := range coreBootstrapFiles {
		body, err := templateBodyFn(name)
		if err != nil {
			return err
		}
		if err := writeFileInWorkspaceFn(rootDir, name, body); err != nil {
			return err
		}
	}
	return nil
}

func LoadWorkspaceBootstrapFilesFromRoot(workspaceRoot string, opts LoadOptions) ([]BootstrapFile, error) {
	ws, err := validateWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}
	cwd := cleanWorkspaceCWD(ws, opts.CWD)
	dirs := []string{cwd}
	if opts.Chain {
		dirs = dirChain(ws, cwd)
	}
	return LoadWorkspaceBootstrapFilesFromDirs(ws, dirs, opts)
}

func validateWorkspaceRoot(workspaceRoot string) (string, error) {
	ws := strings.TrimSpace(workspaceRoot)
	if ws == "" {
		return "", errors.New("workspace root is empty")
	}
	absRoot, err := filepath.Abs(ws)
	if err != nil {
		return "", err
	}
	ws = filepath.Clean(absRoot)
	ws = normalizeDarwinSystemSymlinkPrefix(ws)
	root := ws

	if info, err := osLstatFn(ws); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("workspace root contains symlink")
		}
		resolved, err := filepathEvalSymlinksFn(ws)
		if err != nil {
			return "", err
		}
		if !symlinkResolvedEqual(filepath.Clean(resolved), ws) {
			return "", errors.New("workspace root contains symlink")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}

	if checkAncestorSymlinks {
		for parent := filepath.Dir(root); parent != root; parent = filepath.Dir(parent) {
			info, err := osLstatFn(parent)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return "", err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return "", errors.New("workspace root contains symlink")
			}
			root = parent
		}
	}

	return ws, nil
}

func LoadWorkspaceBootstrapFilesFromDirs(workspaceRoot string, dirs []string, opts LoadOptions) ([]BootstrapFile, error) {
	ws, err := validateWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}
	maxBytes := opts.MaxBytesPerFile
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytesPerFile
	}
	var out []BootstrapFile
	seenDirs := make(map[string]struct{})
	for _, dir := range dirs {
		dir = cleanWorkspaceCWD(ws, dir)
		if err := validateWorkspacePath(ws, dir); err != nil {
			if errors.Is(err, errWorkspaceSymlink) {
				continue
			}
			return nil, err
		}
		if _, ok := seenDirs[dir]; ok {
			continue
		}
		seenDirs[dir] = struct{}{}
		for _, name := range orderedBootstrapFiles {
			item, ok := readWorkspaceMarkdown(ws, dir, name, maxBytes, opts.IncludeDiagnostics)
			if ok {
				out = append(out, item)
			}
		}
	}
	return out, nil
}

func StripFrontMatter(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return s
	}
	idx := strings.Index(s[4:], "\n---\n")
	if idx < 0 {
		return s
	}
	return s[4+idx+len("\n---\n"):]
}

func templateBody(name string) (string, error) {
	raw, err := templateFS.ReadFile(filepath.ToSlash(filepath.Join("templates", name)))
	if err != nil {
		return "", err
	}
	return strings.TrimLeft(StripFrontMatter(string(raw)), "\n"), nil
}

func normalizeDarwinSystemSymlinkPrefix(path string) string {
	if runtime.GOOS != "darwin" {
		return path
	}
	switch {
	case path == "/var":
		return "/private/var"
	case strings.HasPrefix(path, "/var/"):
		return "/private" + path
	case path == "/tmp":
		return "/private/tmp"
	case strings.HasPrefix(path, "/tmp/"):
		return "/private" + path
	case path == "/etc":
		return "/private/etc"
	case strings.HasPrefix(path, "/etc/"):
		return "/private" + path
	default:
		return path
	}
}

func cleanWorkspaceCWD(ws, cwd string) string {
	ws = strings.TrimSpace(ws)
	if ws == "" {
		return ""
	}
	ws = filepath.Clean(ws)
	ws = normalizeDarwinSystemSymlinkPrefix(ws)
	wd := strings.TrimSpace(cwd)
	if wd == "" {
		return ws
	}
	wd = filepath.Clean(wd)
	wd = normalizeDarwinSystemSymlinkPrefix(wd)
	if wd == "." {
		return ws
	}
	rel, err := filepath.Rel(ws, wd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return ws
	}
	return wd
}

func dirChain(wsRoot, cwd string) []string {
	ws := strings.TrimSpace(wsRoot)
	if ws == "" {
		return nil
	}
	ws = filepath.Clean(ws)
	ws = normalizeDarwinSystemSymlinkPrefix(ws)
	wd := cleanWorkspaceCWD(ws, cwd)
	if wd == ws {
		return []string{wd}
	}
	var chain []string
	cur := wd
	for {
		chain = append([]string{cur}, chain...)
		if cur == ws {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return []string{ws}
		}
		cur = parent
	}
	return chain
}

func readWorkspaceMarkdown(ws, dir, name string, maxBytes int, diagnostics bool) (BootstrapFile, bool) {
	if strings.Contains(name, string(filepath.Separator)) || name == "." || strings.TrimSpace(name) == "" {
		return BootstrapFile{}, false
	}
	return readWorkspaceMarkdownPath(ws, name, filepath.Join(dir, name), maxBytes, diagnostics)
}

func readWorkspaceMarkdownPath(ws, name, path string, maxBytes int, diagnostics bool) (BootstrapFile, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return diagnosticFile(name, path, "outside workspace", diagnostics), diagnostics
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return BootstrapFile{}, false
	}
	path = filepath.Clean(absPath)
	if err := validateWorkspacePath(ws, path); err != nil {
		switch {
		case errors.Is(err, errOutsideWorkspace):
			return diagnosticFile(name, path, "outside workspace", diagnostics), diagnostics
		case errors.Is(err, errWorkspaceSymlink):
			return diagnosticFile(name, path, "symlink rejected", diagnostics), diagnostics
		default:
			return BootstrapFile{}, false
		}
	}
	info, err := osLstatFn(path)
	if err != nil {
		return BootstrapFile{}, false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return diagnosticFile(name, path, "symlink rejected", diagnostics), diagnostics
	}
	if !info.Mode().IsRegular() {
		return diagnosticFile(name, path, "non-regular file rejected", diagnostics), diagnostics
	}
	if hasMultipleHardLinks(path, info) {
		return diagnosticFile(name, path, "hardlink rejected", diagnostics), diagnostics
	}
	f, err := osOpenFn(path)
	if err != nil {
		return BootstrapFile{}, false
	}
	defer f.Close()
	raw, err := readAllFn(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil {
		return BootstrapFile{}, false
	}
	truncated := len(raw) > maxBytes
	if truncated {
		raw = append(raw[:maxBytes], []byte("\n…")...)
	}
	body := strings.TrimSpace(StripFrontMatter(string(raw)))
	if body == "" {
		return BootstrapFile{}, false
	}
	return BootstrapFile{Name: name, Path: path, Body: body, Truncated: truncated}, true
}

func diagnosticFile(name, path, reason string, enabled bool) BootstrapFile {
	if !enabled {
		return BootstrapFile{}
	}
	return BootstrapFile{Name: name, Path: path, Diagnostic: reason}
}

func pathInside(root, path string) bool {
	root = strings.TrimSpace(root)
	path = strings.TrimSpace(path)
	if root == "" || path == "" {
		return false
	}
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	root = normalizeDarwinSystemSymlinkPrefix(root)
	path = normalizeDarwinSystemSymlinkPrefix(path)
	rel, err := filepathRelFn(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func validateWorkspacePath(root, target string) error {
	root = filepath.Clean(strings.TrimSpace(root))
	target = filepath.Clean(strings.TrimSpace(target))
	if root == "" || target == "" {
		return errOutsideWorkspace
	}
	root = normalizeDarwinSystemSymlinkPrefix(root)
	target = normalizeDarwinSystemSymlinkPrefix(target)
	if !pathInside(root, target) {
		return errOutsideWorkspace
	}
	rel, err := filepathRelFn(root, target)
	if err != nil {
		return errOutsideWorkspace
	}
	if rel == "." {
		return nil
	}
	current := root
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := osLstatFn(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errWorkspaceSymlink
		}
	}
	return nil
}
