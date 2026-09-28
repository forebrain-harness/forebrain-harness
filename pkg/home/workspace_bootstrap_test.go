package home

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOrderedBootstrapFilesAndPaths(t *testing.T) {
	if got := OrderedBootstrapFiles(); strings.Join(got, ",") != "AGENTS.md,SOUL.md,USER.md" {
		t.Fatalf("OrderedBootstrapFiles = %#v", got)
	}
	got := OrderedBootstrapFiles()
	got[0] = "mutated"
	if OrderedBootstrapFiles()[0] != "AGENTS.md" {
		t.Fatal("OrderedBootstrapFiles returned shared slice")
	}
	if got := WorkspaceDir(" /tmp/home "); got != filepath.Clean("/tmp/home/workspace") {
		t.Fatalf("WorkspaceDir = %q", got)
	}
	if got := WorkspaceDir("   "); got != "" {
		t.Fatalf("empty WorkspaceDir = %q", got)
	}
}

func TestEnsureWorkspaceRejectsEmptyHome(t *testing.T) {
	if err := EnsureWorkspace(" ", EnsureOptions{}); err == nil {
		t.Fatal("EnsureWorkspace empty home error = nil")
	} else if err.Error() != "workspace home is empty" {
		t.Fatalf("EnsureWorkspace empty home error = %q", err)
	}
	if _, err := LoadWorkspaceBootstrapFilesWithOptions(" ", LoadOptions{}); err == nil {
		t.Fatal("LoadWorkspaceBootstrapFilesWithOptions empty home error = nil")
	} else if err.Error() != "workspace home is empty" {
		t.Fatalf("LoadWorkspaceBootstrapFilesWithOptions empty home error = %q", err)
	}
	if _, err := LoadWorkspaceBootstrapFilesFromDirs(" ", nil, LoadOptions{}); err == nil {
		t.Fatal("LoadWorkspaceBootstrapFilesFromDirs empty root error = nil")
	}
}

func TestEnsureWorkspaceReturnsInjectedErrors(t *testing.T) {
	want := errors.New("boom")
	home := t.TempDir()
	withWorkspaceBootstrapHooks(t, hooksForWorkspaceBootstrapTest{
		mkdirAll: func(string, os.FileMode) error { return want },
	})
	if err := EnsureWorkspace(home, EnsureOptions{}); !errors.Is(err, want) {
		t.Fatalf("EnsureWorkspace mkdir error = %v, want %v", err, want)
	}

	withWorkspaceBootstrapHooks(t, hooksForWorkspaceBootstrapTest{
		templateBody: func(string) (string, error) { return "", want },
	})
	if err := EnsureWorkspace(home, EnsureOptions{}); !errors.Is(err, want) {
		t.Fatalf("EnsureWorkspace template error = %v, want %v", err, want)
	}

	withWorkspaceBootstrapHooks(t, hooksForWorkspaceBootstrapTest{
		writeFileInWorkspace: func(*os.File, string, string) error { return want },
	})
	if err := EnsureWorkspace(t.TempDir(), EnsureOptions{}); !errors.Is(err, want) {
		t.Fatalf("EnsureWorkspace write core error = %v, want %v", err, want)
	}
}

func TestEnsureWorkspaceCreatesCoreFiles(t *testing.T) {
	home := t.TempDir()
	if err := EnsureWorkspace(home, EnsureOptions{}); err != nil {
		t.Fatalf("EnsureWorkspace error: %v", err)
	}
	for _, name := range []string{"AGENTS.md", "SOUL.md", "USER.md"} {
		if _, err := os.Stat(filepath.Join(home, "workspace", name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
	for _, name := range []string{"BOOTSTRAP.md", "HEARTBEAT.md", "TOOLS.md", "IDENTITY.md"} {
		if _, err := os.Stat(filepath.Join(home, "workspace", name)); !os.IsNotExist(err) {
			t.Fatalf("%s should not be created, err=%v", name, err)
		}
	}
}

func TestEnsureWorkspaceRootUsesExplicitPrimaryWorkspace(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "workspaces", "review")
	err := EnsureWorkspaceRoot(ws, EnsureOptions{})
	if err != nil {
		t.Fatalf("EnsureWorkspaceRoot: %v", err)
	}
	for _, name := range OrderedBootstrapFiles() {
		if _, err := os.Stat(filepath.Join(ws, name)); err != nil {
			t.Fatalf("missing %s in explicit workspace: %v", name, err)
		}
	}
}

func TestEnsureWorkspaceRootRejectsExplicitRootSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	root := filepath.Join(base, "workspace-link")
	if err := os.Symlink(target, root); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if err := EnsureWorkspaceRoot(root, EnsureOptions{}); err == nil {
		t.Fatal("EnsureWorkspaceRoot symlink root error = nil")
	}
}

func TestEnsureWorkspaceDoesNotOverwriteCoreFiles(t *testing.T) {
	home := t.TempDir()
	ws := filepath.Join(home, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "SOUL.md"), []byte("custom identity"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := EnsureWorkspace(home, EnsureOptions{}); err != nil {
		t.Fatalf("EnsureWorkspace error: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(ws, "SOUL.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(raw) != "custom identity" {
		t.Fatalf("identity overwritten: %q", string(raw))
	}
}

func TestLoadWorkspaceBootstrapFilesReadsCoreFiles(t *testing.T) {
	home := t.TempDir()
	if err := EnsureWorkspace(home, EnsureOptions{}); err != nil {
		t.Fatalf("EnsureWorkspace error: %v", err)
	}
	files, err := LoadWorkspaceBootstrapFilesWithOptions(home, LoadOptions{CWD: filepath.Join(home, "workspace")})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	if got := strings.Join(names, ","); got != "AGENTS.md,SOUL.md,USER.md" {
		t.Fatalf("names = %q", got)
	}
}

func TestLoadWorkspaceBootstrapFilesFromRoot(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "workspaces", "research")
	if err := EnsureWorkspaceRoot(ws, EnsureOptions{}); err != nil {
		t.Fatalf("EnsureWorkspaceRoot: %v", err)
	}
	files, err := LoadWorkspaceBootstrapFilesFromRoot(ws, LoadOptions{})
	if err != nil {
		t.Fatalf("LoadWorkspaceBootstrapFilesFromRoot: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected bootstrap files from explicit root")
	}
}

func TestLoadWorkspaceBootstrapFilesFromRootRejectsExplicitRootSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	root := filepath.Join(base, "workspace-link")
	if err := os.Symlink(target, root); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if _, err := LoadWorkspaceBootstrapFilesFromRoot(root, LoadOptions{}); err == nil {
		t.Fatal("LoadWorkspaceBootstrapFilesFromRoot symlink root error = nil")
	}
}

func TestEnsureWorkspaceRootRejectsSymlinkParent(t *testing.T) {
	base := t.TempDir()
	realParent := filepath.Join(base, "real-parent")
	if err := os.MkdirAll(realParent, 0o755); err != nil {
		t.Fatalf("mkdir real parent: %v", err)
	}
	linkParent := filepath.Join(base, "link-parent")
	if err := os.Symlink(realParent, linkParent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	err := EnsureWorkspaceRoot(filepath.Join(linkParent, "child"), EnsureOptions{})
	if err == nil {
		t.Fatal("EnsureWorkspaceRoot symlink parent error = nil")
	}
}

func TestLoadWorkspaceBootstrapFilesFromDirsRejectsSymlinkParent(t *testing.T) {
	base := t.TempDir()
	realParent := filepath.Join(base, "real-parent")
	realWS := filepath.Join(realParent, "workspace")
	if err := os.MkdirAll(realWS, 0o755); err != nil {
		t.Fatalf("mkdir real workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realWS, "AGENTS.md"), []byte("# agents"), 0o644); err != nil {
		t.Fatalf("write agents: %v", err)
	}
	linkParent := filepath.Join(base, "link-parent")
	if err := os.Symlink(realParent, linkParent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := LoadWorkspaceBootstrapFilesFromDirs(filepath.Join(linkParent, "workspace"), nil, LoadOptions{})
	if err == nil {
		t.Fatal("LoadWorkspaceBootstrapFilesFromDirs symlink parent error = nil")
	}
}

func TestLoadWorkspaceBootstrapFilesFromDirsRejectsInternalSymlinkDir(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "AGENTS.md"), []byte("# outside"), 0o644); err != nil {
		t.Fatalf("write outside agents: %v", err)
	}
	link := filepath.Join(ws, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	files, err := LoadWorkspaceBootstrapFilesFromDirs(ws, []string{link}, LoadOptions{IncludeDiagnostics: true})
	if err != nil {
		t.Fatalf("LoadWorkspaceBootstrapFilesFromDirs: %v", err)
	}
	for _, f := range files {
		if f.Name == "AGENTS.md" {
			t.Fatalf("symlink dir leaked outside file: %+v", f)
		}
	}
}

func TestLoadWorkspaceBootstrapFilesOptions(t *testing.T) {
	home := t.TempDir()
	ws := filepath.Join(home, "workspace")
	child := filepath.Join(ws, "child", "grand")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	for _, item := range []struct {
		path string
		body string
	}{
		{filepath.Join(ws, "AGENTS.md"), "# ROOT AGENTS"},
		{filepath.Join(ws, "USER.md"), "# ROOT USER"},
		{filepath.Join(ws, "child", "AGENTS.md"), "# CHILD AGENTS"},
		{filepath.Join(ws, "child", "grand", "USER.md"), "# GRAND USER"},
	} {
		if err := os.MkdirAll(filepath.Dir(item.path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", item.path, err)
		}
		if err := os.WriteFile(item.path, []byte(item.body), 0o644); err != nil {
			t.Fatalf("write %s: %v", item.path, err)
		}
	}
	files, err := LoadWorkspaceBootstrapFilesWithOptions(home, LoadOptions{CWD: child, Chain: true})
	if err != nil {
		t.Fatalf("load chain: %v", err)
	}
	var joined []string
	for _, f := range files {
		joined = append(joined, f.Name+":"+f.Body)
	}
	got := strings.Join(joined, "\n")
	for _, want := range []string{
		"AGENTS.md:# ROOT AGENTS",
		"USER.md:# ROOT USER",
		"AGENTS.md:# CHILD AGENTS",
		"USER.md:# GRAND USER",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("chain load missing %q: %s", want, got)
		}
	}

	files, err = LoadWorkspaceBootstrapFilesFromDirs(ws, []string{filepath.Join(ws, "child"), filepath.Join(ws, "child")}, LoadOptions{})
	if err != nil {
		t.Fatalf("load duplicate dirs: %v", err)
	}
	if len(files) != 1 || files[0].Name != "AGENTS.md" {
		t.Fatalf("duplicate dirs should read child once, got %+v", files)
	}
}

func TestLoadWorkspaceBootstrapFilesRejectsSymlink(t *testing.T) {
	home := t.TempDir()
	if err := EnsureWorkspace(home, EnsureOptions{}); err != nil {
		t.Fatalf("EnsureWorkspace error: %v", err)
	}
	ws := filepath.Join(home, "workspace")
	outside := filepath.Join(home, "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Remove(filepath.Join(ws, "USER.md")); err != nil {
		t.Fatalf("remove user file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, "USER.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	files, err := LoadWorkspaceBootstrapFilesWithOptions(home, LoadOptions{CWD: ws, IncludeDiagnostics: true})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, f := range files {
		if f.Name == "USER.md" && f.Diagnostic != "symlink rejected" {
			t.Fatalf("USER.md diagnostic = %q", f.Diagnostic)
		}
		if f.Name == "USER.md" && strings.Contains(f.Body, "outside") {
			t.Fatalf("symlink body leaked: %+v", f)
		}
	}
}

func TestReadWorkspaceMarkdownDiagnostics(t *testing.T) {
	ws := t.TempDir()
	outside := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(outside, []byte("# outside"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if item, ok := readWorkspaceMarkdownPath(ws, "AGENTS.md", outside, 100, true); !ok || item.Diagnostic != "outside workspace" {
		t.Fatalf("outside diagnostic = %+v ok=%v", item, ok)
	}
	if _, ok := readWorkspaceMarkdownPath(ws, "AGENTS.md", outside, 100, false); ok {
		t.Fatal("outside file should not be returned without diagnostics")
	}
	if item, ok := readWorkspaceMarkdownPath(ws, "missing.md", filepath.Join(ws, "missing.md"), 100, true); ok || item.Name != "" {
		t.Fatalf("missing file = %+v ok=%v", item, ok)
	}
	dirPath := filepath.Join(ws, "AGENTS.md")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatalf("mkdir file path: %v", err)
	}
	if item, ok := readWorkspaceMarkdownPath(ws, "AGENTS.md", dirPath, 100, true); !ok || item.Diagnostic != "non-regular file rejected" {
		t.Fatalf("dir diagnostic = %+v ok=%v", item, ok)
	}
	if item, ok := readWorkspaceMarkdown(ws, ws, "bad/name.md", 100, true); ok || item.Name != "" {
		t.Fatalf("invalid markdown name = %+v ok=%v", item, ok)
	}
	emptyPath := filepath.Join(ws, "EMPTY.md")
	if err := os.WriteFile(emptyPath, []byte("---\nx: y\n---\n\n"), 0o644); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	if item, ok := readWorkspaceMarkdownPath(ws, "EMPTY.md", emptyPath, 100, true); ok || item.Name != "" {
		t.Fatalf("empty body = %+v ok=%v", item, ok)
	}
	longPath := filepath.Join(ws, "LONG.md")
	if err := os.WriteFile(longPath, []byte("abcdef"), 0o644); err != nil {
		t.Fatalf("write long: %v", err)
	}
	if item, ok := readWorkspaceMarkdownPath(ws, "LONG.md", longPath, 3, false); !ok || item.Body != "abc\n…" {
		t.Fatalf("truncated body = %+v ok=%v", item, ok)
	}
}

func TestReadWorkspaceMarkdownRejectsHardlink(t *testing.T) {
	ws := t.TempDir()
	original := filepath.Join(ws, "ORIG.md")
	hard := filepath.Join(ws, "HARD.md")
	if err := os.WriteFile(original, []byte("# original"), 0o644); err != nil {
		t.Fatalf("write original: %v", err)
	}
	if err := os.Link(original, hard); err != nil {
		t.Skipf("hardlink unavailable: %v", err)
	}
	item, ok := readWorkspaceMarkdownPath(ws, "HARD.md", hard, 100, true)
	if !ok || item.Diagnostic != "hardlink rejected" {
		t.Fatalf("hardlink diagnostic = %+v ok=%v", item, ok)
	}
}

func TestLoadWorkspaceBootstrapFilesAndHelpers(t *testing.T) {
	home := t.TempDir()
	ws := filepath.Join(home, "workspace")
	if err := EnsureWorkspace(home, EnsureOptions{}); err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	if got := StripFrontMatter("---\na: b\n---\nbody"); got != "body" {
		t.Fatalf("StripFrontMatter = %q", got)
	}
	if got := StripFrontMatter("---\na: b\nno close"); got != "---\na: b\nno close" {
		t.Fatalf("StripFrontMatter malformed = %q", got)
	}
	if got := StripFrontMatter("body"); got != "body" {
		t.Fatalf("StripFrontMatter plain = %q", got)
	}
	if got := templateBody; got == nil {
		t.Fatal("templateBody should exist")
	}
	if _, err := templateBody("MISSING.md"); err == nil {
		t.Fatal("templateBody missing error = nil")
	}
	if got := cleanWorkspaceCWD(ws, filepath.Join(ws, "child")); got != filepath.Join(ws, "child") {
		if want := normalizeDarwinSystemSymlinkPrefix(filepath.Join(ws, "child")); got != want {
			t.Fatalf("cleanWorkspaceCWD = %q, want %q", got, want)
		}
	}
	if got := cleanWorkspaceCWD("", filepath.Join(ws, "child")); got != "" {
		t.Fatalf("cleanWorkspaceCWD empty ws = %q", got)
	}
	if got := cleanWorkspaceCWD(ws, "."); got != ws {
		if want := normalizeDarwinSystemSymlinkPrefix(ws); got != want {
			t.Fatalf("cleanWorkspaceCWD dot = %q, want %q", got, want)
		}
	}
	if got := cleanWorkspaceCWD(ws, "/outside"); got != ws {
		if want := normalizeDarwinSystemSymlinkPrefix(ws); got != want {
			t.Fatalf("cleanWorkspaceCWD outside = %q, want %q", got, want)
		}
	}
	if got := dirChain(ws, filepath.Join(ws, "child")); len(got) == 0 {
		t.Fatal("dirChain returned empty")
	}
	wantWS := normalizeDarwinSystemSymlinkPrefix(ws)
	if got := dirChain(ws, ws); len(got) != 1 || got[0] != wantWS {
		t.Fatalf("dirChain root = %#v", got)
	}
	if got := dirChain("", ws); got != nil {
		t.Fatalf("dirChain empty root = %#v", got)
	}
	outsideChild := filepath.Join(t.TempDir(), "child")
	if got := dirChain(ws, outsideChild); len(got) != 1 || got[0] != wantWS {
		t.Fatalf("dirChain outside child = %#v", got)
	}
	if got := pathInside(ws, filepath.Join(ws, "AGENTS.md")); !got {
		t.Fatal("pathInside = false for inside path")
	}
	if got := pathInside(ws, "/outside"); got {
		t.Fatal("pathInside = true for outside path")
	}
	if got := pathInside("", filepath.Join(ws, "AGENTS.md")); got {
		t.Fatal("pathInside empty root = true")
	}
	files, err := LoadWorkspaceBootstrapFiles(home)
	if err != nil {
		t.Fatalf("LoadWorkspaceBootstrapFiles: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("expected core bootstrap files, got %d", len(files))
	}
}

func TestReadWorkspaceMarkdownPathEmptyPathDiagnostic(t *testing.T) {
	item, ok := readWorkspaceMarkdownPath(t.TempDir(), "EMPTY.md", " ", 100, true)
	if !ok || item.Diagnostic != "outside workspace" {
		t.Fatalf("empty path diagnostic = %+v ok=%v", item, ok)
	}
}

func TestDirChainBreaksAtParentGuard(t *testing.T) {
	ws := filepath.Clean("/tmp/work")
	cwd := filepath.Join(ws, "a")
	got := dirChain(ws, cwd)
	wantWS := normalizeDarwinSystemSymlinkPrefix(ws)
	wantCWD := normalizeDarwinSystemSymlinkPrefix(cwd)
	if len(got) == 0 || got[0] != wantWS || got[len(got)-1] != wantCWD {
		t.Fatalf("dirChain = %#v", got)
	}
}

func TestReadWorkspaceMarkdownPathInjectedReadError(t *testing.T) {
	ws := t.TempDir()
	path := filepath.Join(ws, "AGENTS.md")
	if err := os.WriteFile(path, []byte("# agents"), 0o644); err != nil {
		t.Fatalf("write agents: %v", err)
	}
	want := errors.New("read failed")
	withWorkspaceBootstrapHooks(t, hooksForWorkspaceBootstrapTest{
		readAll: func(io.Reader) ([]byte, error) { return nil, want },
	})
	if item, ok := readWorkspaceMarkdownPath(ws, "AGENTS.md", path, 100, true); ok || item.Name != "" {
		t.Fatalf("read error should suppress file, got %+v ok=%v", item, ok)
	}
	withWorkspaceBootstrapHooks(t, hooksForWorkspaceBootstrapTest{
		open: func(string) (*os.File, error) { return nil, errors.New("open failed") },
	})
	if item, ok := readWorkspaceMarkdownPath(ws, "AGENTS.md", path, 100, true); ok || item.Name != "" {
		t.Fatalf("open error should suppress file, got %+v ok=%v", item, ok)
	}
}

func TestPathInsideRelError(t *testing.T) {
	withWorkspaceBootstrapHooks(t, hooksForWorkspaceBootstrapTest{
		rel: func(string, string) (string, error) { return "", errors.New("rel failed") },
	})
	if pathInside(t.TempDir(), filepath.Join(t.TempDir(), "x")) {
		t.Fatal("pathInside should be false when filepath.Rel fails")
	}
}

type fakeFileInfo struct {
	sys any
}

func (fakeFileInfo) Name() string       { return "fake" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() os.FileMode  { return 0 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any         { return f.sys }

type hooksForWorkspaceBootstrapTest struct {
	mkdirAll             func(string, os.FileMode) error
	open                 func(string) (*os.File, error)
	openWorkspaceRoot    func(string) (*os.File, error)
	writeFileInWorkspace func(*os.File, string, string) error
	readAll              func(io.Reader) ([]byte, error)
	lstat                func(string) (os.FileInfo, error)
	evalSymlinks         func(string) (string, error)
	rel                  func(string, string) (string, error)
	templateBody         func(string) (string, error)
}

func withWorkspaceBootstrapHooks(t *testing.T, h hooksForWorkspaceBootstrapTest) {
	t.Helper()
	resetWorkspaceBootstrapHooksForTest := func() {
		osMkdirAllFn = os.MkdirAll
		osOpenFn = os.Open
		openWorkspaceRootFn = openWorkspaceRootNoFollow
		writeFileInWorkspaceFn = writeFileIfMissingInWorkspace
		osLstatFn = os.Lstat
		filepathEvalSymlinksFn = filepath.EvalSymlinks
		readAllFn = io.ReadAll
		filepathRelFn = filepath.Rel
		templateBodyFn = templateBody
	}
	resetWorkspaceBootstrapHooksForTest()
	if h.mkdirAll != nil {
		osMkdirAllFn = h.mkdirAll
	}
	if h.open != nil {
		osOpenFn = h.open
	}
	if h.openWorkspaceRoot != nil {
		openWorkspaceRootFn = h.openWorkspaceRoot
	}
	if h.writeFileInWorkspace != nil {
		writeFileInWorkspaceFn = h.writeFileInWorkspace
	}
	if h.lstat != nil {
		osLstatFn = h.lstat
	}
	if h.evalSymlinks != nil {
		filepathEvalSymlinksFn = h.evalSymlinks
	}
	if h.readAll != nil {
		readAllFn = h.readAll
	}
	if h.rel != nil {
		filepathRelFn = h.rel
	}
	if h.templateBody != nil {
		templateBodyFn = h.templateBody
	}
	t.Cleanup(resetWorkspaceBootstrapHooksForTest)
}

// The helpers below were production functions that only the tests in this
// package ever called: each is a home-scoped wrapper over the live
// workspace-root-scoped function. They live here so the production files
// carry no unused code while the tests keep exercising the live path.

func OrderedBootstrapFiles() []string {
	return append([]string(nil), orderedBootstrapFiles...)
}

func LoadWorkspaceBootstrapFiles(home string) ([]BootstrapFile, error) {
	return LoadWorkspaceBootstrapFilesWithOptions(home, LoadOptions{Chain: false})
}

func LoadWorkspaceBootstrapFilesWithOptions(home string, opts LoadOptions) ([]BootstrapFile, error) {
	if strings.TrimSpace(home) == "" {
		return nil, errors.New("workspace home is empty")
	}
	return LoadWorkspaceBootstrapFilesFromRoot(WorkspaceDir(home), opts)
}
