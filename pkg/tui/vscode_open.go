package tui

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// deliverableExtID and deliverableExtVersion name the bundled extension; the
// version is compared against `code --list-extensions --show-versions` so a
// rebuilt VSIX actually reaches the user (a running window keeps its scanned
// version until it reloads — see the plan's maintenance notes).
const (
	deliverableExtID      = "forebrain.deliverable-open"
	deliverableExtVersion = "0.1.0"
)

//go:embed vscode_ext/deliverable-open.vsix
var deliverableExtVSIX []byte

// vscodeCLIPath is a variable so a test can point the router at a fake `code`
// (or at nothing) without touching PATH or the real installation.
var vscodeCLIPath = findVSCodeCLI

func findVSCodeCLI() string {
	if p, err := exec.LookPath("code"); err == nil {
		return p
	}
	if runtime.GOOS == "darwin" {
		const p = "/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code"
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// openDeliveredFile is the default imagePreviewOpener: text files go to VS
// Code (Source Control diff when the project is a git repo and the file is not
// ignored), everything else keeps the platform viewer. The stat and the sniff
// run here; the git probes and CLI calls run in a goroutine because the click
// sites call the opener on the input loop, and a probe that waits on git must
// not hold the terminal hostage.
func openDeliveredFile(path string, failed func(error)) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errors.New("file no longer exists")
		}
		return err
	}
	if !isTextFile(path) {
		return openWithSystemViewer(path, failed)
	}
	code := vscodeCLIPath()
	if code == "" {
		return openWithSystemViewer(path, failed)
	}
	go func() {
		if err := openTextInVSCode(code, path, failed); err != nil && failed != nil {
			failed(err)
		}
	}()
	return nil
}

// openTextInVSCode walks the fallback ladder: SCM diff via the bundled
// extension, then a plain --diff against the index/HEAD baseline, then a plain
// editor, then the platform viewer. Only the last rung's failure is worth a
// hint; the earlier rungs fall through silently because a later rung still
// opens the file.
func openTextInVSCode(code, path string, failed func(error)) error {
	if root, err := deliverableGitRoot(filepath.Dir(path)); err == nil {
		if ignored, ok := deliverableGitIgnored(root, path); ok && !ignored {
			if err := openDeliverableSCMDiff(code, root, path); err == nil {
				return nil
			}
			if err := openDeliverableDiffFallback(code, path); err == nil {
				return nil
			}
		}
	}
	if err := runVSCodeCLI(code, 10*time.Second, "-r", path); err == nil {
		return nil
	}
	return openWithSystemViewer(path, failed)
}

func openDeliverableSCMDiff(code, root, path string) error {
	if err := ensureDeliverableExt(code); err != nil {
		return err
	}
	// -r focuses the window already holding the project; --open-url then lands
	// in that window because the URI goes to the last-active one.
	if err := runVSCodeCLI(code, 15*time.Second, "-r", root); err != nil {
		return err
	}
	waitVSCodeReady(code)
	link := "vscode://" + deliverableExtID + "/change?path=" + url.QueryEscape(path) + "&root=" + url.QueryEscape(root)
	return runVSCodeCLI(code, 10*time.Second, "--open-url", link)
}

var deliverableExtMu struct {
	sync.Mutex
	installed string
}

func ensureDeliverableExt(code string) error {
	deliverableExtMu.Lock()
	defer deliverableExtMu.Unlock()
	if deliverableExtMu.installed == deliverableExtVersion {
		return nil
	}
	if out, err := runVSCodeCLIOutput(code, 20*time.Second, "--list-extensions", "--show-versions"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) == deliverableExtID+"@"+deliverableExtVersion {
				deliverableExtMu.installed = deliverableExtVersion
				return nil
			}
		}
	}
	dir, err := os.MkdirTemp("", "forebrain-deliverable-ext-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	vsix := filepath.Join(dir, "deliverable-open.vsix")
	if err := os.WriteFile(vsix, deliverableExtVSIX, 0o600); err != nil {
		return err
	}
	if err := runVSCodeCLI(code, 60*time.Second, "--install-extension", vsix, "--force"); err != nil {
		return err
	}
	deliverableExtMu.installed = deliverableExtVersion
	return nil
}

// waitVSCodeReady is best-effort: on a cold start the window may still be
// scanning extensions when the URI arrives. --status answers once an instance
// is up; exhausting the deadline proceeds anyway rather than failing the open.
func waitVSCodeReady(code string) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := runVSCodeCLIOutput(code, 3*time.Second, "--status"); err == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// openDeliverableDiffFallback reproduces the Working Tree diff without the
// extension: baseline is the index version, then HEAD, then empty (untracked).
// The base file outlives the CLI call because a cold-started instance reads it
// asynchronously; leftovers are swept by pid/age like shell_diff's temp dirs.
func openDeliverableDiffFallback(code, path string) error {
	base := deliverableBaseContent(path)
	dir, err := os.MkdirTemp("", "forebrain-deliverable-diff-")
	if err != nil {
		return err
	}
	sweepDeliverableDiffDirs()
	basePath := filepath.Join(dir, "base-"+filepath.Base(path))
	if err := os.WriteFile(basePath, base, 0o600); err != nil {
		return err
	}
	return runVSCodeCLI(code, 15*time.Second, "-r", "--diff", basePath, path)
}

var deliverableSweepOnce sync.Once

func sweepDeliverableDiffDirs() {
	deliverableSweepOnce.Do(func() {
		entries, err := os.ReadDir(os.TempDir())
		if err != nil {
			return
		}
		cutoff := time.Now().Add(-24 * time.Hour)
		prefix := fmt.Sprintf("forebrain-deliverable-diff-%d-", os.Getpid())
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), prefix) {
				continue
			}
			if !strings.HasPrefix(e.Name(), "forebrain-deliverable-diff-") {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			os.RemoveAll(filepath.Join(os.TempDir(), e.Name()))
		}
	})
}

func deliverableGitRoot(dir string) (string, error) {
	out, err := runDeliverableGit(dir, 2*time.Second, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	top := strings.TrimSpace(out)
	if top == "" {
		return "", errors.New("not a git work tree")
	}
	return top, nil
}

// deliverableGitIgnored reports (ignored, known): check-ignore exits 0 for an
// ignored path and 1 for a tracked-or-untracked-but-not-ignored one; any other
// outcome is unknown and the caller takes the plain-open rung.
func deliverableGitIgnored(root, path string) (bool, bool) {
	_, err := runDeliverableGit(root, 2*time.Second, "check-ignore", "-q", "--", path)
	if err == nil {
		return true, true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		switch ee.ExitCode() {
		case 0:
			return true, true
		case 1:
			return false, true
		}
	}
	return false, false
}

// deliverableBaseContent reads the index version, then HEAD, then nothing
// (untracked). The specs are ./-relative and run from the file's own directory
// because git resolves symlinks in the working-tree path while the clicked
// path keeps them: on macOS (/var → /private/var, /tmp → /private/tmp) a
// repo-root-relative path computed from the clicked path lands "outside
// repository" (exit 128, reproduced 2026-11-21). The ./ form lets git resolve.
func deliverableBaseContent(path string) []byte {
	dir, name := filepath.Dir(path), filepath.Base(path)
	for _, spec := range []string{":./" + name, "HEAD:./" + name} {
		if out, err := runDeliverableGit(dir, 2*time.Second, "show", spec); err == nil {
			return []byte(out)
		}
	}
	return nil
}

func runDeliverableGit(dir string, timeout time.Duration, sub ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := append([]string{"-C", dir}, sub...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	return string(out), err
}

func runVSCodeCLI(code string, timeout time.Duration, args ...string) error {
	_, err := runVSCodeCLIOutput(code, timeout, args...)
	return err
}

func runVSCodeCLIOutput(code string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, code, args...).CombinedOutput()
	if err != nil {
		if said := strings.TrimSpace(string(out)); said != "" {
			return "", errors.New(said)
		}
		return "", err
	}
	return string(out), nil
}

// deliverableTextExts names the extensions that are text by convention; the
// sniff below decides everything else (extensionless Makefile, odd suffixes).
var deliverableTextExts = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".text": true, ".log": true,
	".csv": true, ".tsv": true, ".go": true, ".mod": true, ".sum": true,
	".js": true, ".mjs": true, ".cjs": true, ".ts": true, ".tsx": true, ".jsx": true,
	".json": true, ".jsonc": true, ".yaml": true, ".yml": true, ".toml": true,
	".ini": true, ".cfg": true, ".conf": true, ".properties": true,
	".sh": true, ".bash": true, ".zsh": true, ".fish": true,
	".py": true, ".rb": true, ".rs": true, ".lua": true, ".pl": true, ".php": true,
	".c": true, ".h": true, ".cpp": true, ".hpp": true, ".cc": true, ".cxx": true,
	".java": true, ".kt": true, ".kts": true, ".swift": true, ".m": true, ".mm": true,
	".cs": true, ".fs": true, ".vb": true, ".r": true, ".jl": true, ".dart": true,
	".ex": true, ".exs": true, ".erl": true, ".hs": true, ".ml": true, ".scala": true, ".sc": true,
	".html": true, ".htm": true, ".css": true, ".scss": true, ".less": true,
	".xml": true, ".svg": true, ".vue": true, ".svelte": true, ".astro": true,
	".sql": true, ".graphql": true, ".gql": true, ".proto": true,
	".tf": true, ".hcl": true, ".nix": true, ".gradle": true, ".cmake": true, ".mk": true,
	".patch": true, ".diff": true, ".env": true, ".lock": true,
	".gitignore": true, ".gitattributes": true, ".editorconfig": true, ".dockerfile": true,
}

// deliverableBinaryExts names the extensions that are never text, whatever
// their first 8KB look like: a truncated or corrupt image still belongs in the
// image viewer, and Go's content sniffer answers text/plain for high bytes that
// are not control characters, so the payload alone cannot decide this. Keep
// .bin/.dat and other ambiguous suffixes OUT of this map — they must keep the
// sniff below, which is what TestIsTextFileSniffMatrix pins.
var deliverableBinaryExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".bmp": true, ".ico": true, ".tif": true, ".tiff": true, ".heic": true, ".avif": true,
	".pdf": true, ".zip": true, ".gz": true, ".tgz": true, ".bz2": true, ".xz": true,
	".7z": true, ".rar": true, ".tar": true,
	".docx": true, ".xlsx": true, ".pptx": true,
	".mp3": true, ".mp4": true, ".mov": true, ".avi": true, ".mkv": true, ".flac": true,
	".wav": true, ".ogg": true, ".webm": true, ".m4a": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true,
	".sqlite": true, ".db": true, ".silk": true,
}

// isTextFile decides "VS Code or the platform viewer": a known text extension
// wins outright, a known binary extension loses outright (the sniff cannot be
// trusted there — see the map above), and everything else has its first 8KB
// sniffed — a NUL byte means binary, valid UTF-8 means text, and the remainder
// goes to the content sniffer the image path already trusts
// (llm.ImageMIMEType's http.DetectContentType).
func isTextFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if deliverableTextExts[ext] {
		return true
	}
	switch strings.ToLower(filepath.Base(path)) {
	case "makefile", "dockerfile", "justfile", "rakefile", "gemfile":
		return true
	}
	if deliverableBinaryExts[ext] {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	if n == 0 {
		return true
	}
	buf = buf[:n]
	if bytes.IndexByte(buf, 0) >= 0 {
		return false
	}
	if utf8.Valid(buf) {
		return true
	}
	return strings.HasPrefix(http.DetectContentType(buf), "text/")
}
