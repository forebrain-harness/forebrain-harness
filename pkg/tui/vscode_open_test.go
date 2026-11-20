package tui

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeCodeCLI installs a script the vscodeCLIPath seam points at. It records
// every invocation's argv (one arg per line, invocations separated by "---")
// and answers --list-extensions / --install-extension from env knobs so a test
// can steer the install rung without a real VS Code. FB_CODE_ALL_EXIT makes
// every invocation fail (the dead-ladder test). The process-wide install cache
// is reset here and on cleanup so tests cannot inherit each other's state.
func fakeCodeCLI(t *testing.T, listBody string, installExit string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake code CLI is a /bin/sh script")
	}
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	script := filepath.Join(dir, "code")
	body := "#!/bin/sh\n[ -n \"$FB_CODE_ALL_EXIT\" ] && exit \"$FB_CODE_ALL_EXIT\"\n" +
		"printf '%s\\n' \"$@\" >> \"$FB_CODE_ARGV\"\nprintf '%s\\n' '---' >> \"$FB_CODE_ARGV\"\n" +
		"case \"$1\" in\n" +
		"  --list-extensions) printf '%s' \"$FB_CODE_LIST\" ;;\n" +
		"  --install-extension) exit \"$FB_CODE_INSTALL_EXIT\" ;;\n" +
		"esac\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FB_CODE_ARGV", argv)
	t.Setenv("FB_CODE_LIST", listBody)
	t.Setenv("FB_CODE_INSTALL_EXIT", installExit)
	t.Setenv("FB_CODE_ALL_EXIT", "")
	resetExtCache := func() {
		deliverableExtMu.Lock()
		deliverableExtMu.installed = ""
		deliverableExtMu.Unlock()
	}
	resetExtCache()
	prev := vscodeCLIPath
	vscodeCLIPath = func() string { return script }
	t.Cleanup(func() { vscodeCLIPath = prev; resetExtCache() })
	return script, argv
}

func codeInvocations(t *testing.T, argv string) [][]string {
	t.Helper()
	data, err := os.ReadFile(argv)
	if err != nil {
		return nil
	}
	var out [][]string
	for _, chunk := range strings.Split(string(data), "---\n") {
		chunk = strings.TrimSuffix(chunk, "\n")
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		out = append(out, strings.Split(chunk, "\n"))
	}
	return out
}

func waitForCodeArg(t *testing.T, argv, needle string) [][]string {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		inv := codeInvocations(t, argv)
		for _, args := range inv {
			if strings.Contains(strings.Join(args, " "), needle) {
				return inv
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no code invocation containing %q; got %v", needle, codeInvocations(t, argv))
	return nil
}

// deliverableTestRepo builds a git repo with one committed-then-modified file,
// one untracked file and one ignored file — the three routing cases.
func deliverableTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@example.invalid")
	git("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "a.md", ".gitignore")
	git("commit", "-qm", "base")
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("one\nTWO\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.md"), []byte("fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.md"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// waitForCodeCall waits until the fake has recorded a complete invocation that
// satisfies match. The script writes an invocation's argv and the "---"
// separator in two writes, so a read can land between them: only a whole call
// may be asserted on.
func waitForCodeCall(t *testing.T, argv string, match func([]string) bool) [][]string {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		inv := codeInvocations(t, argv)
		for _, args := range inv {
			if match(args) {
				return inv
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no matching code invocation; got %v", codeInvocations(t, argv))
	return nil
}

// waitForViewer waits for the fake platform viewer to record that it ran. The
// marker is the viewer program's own name plus ".ran", so a test observes the
// real launch path (imageViewerCommand → startDetached), not an injected stub.
func waitForViewer(t *testing.T, viewerDir string) {
	t.Helper()
	program, _ := imageViewerCommand("x.png")
	marker := filepath.Join(viewerDir, program+".ran")
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("the platform viewer never ran: no %s", marker)
}

// gitToplevel is what git itself calls the repository root: on macOS the
// physical path with symlinks resolved, which is not the t.TempDir() string
// (/var/folders/… is /private/var/folders/…). The deep link has to carry the
// physical root, because that is what the extension compares repo roots with.
func gitToplevel(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel in %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}

// hasArg reports whether args contains want.
func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// TestOpenDeliveredFileGitRouting pins the routing a click makes inside a git
// repo: a text file git does not ignore goes to the SCM deep link served by the
// bundled extension, while an ignored file and a file outside any repo keep the
// plain editor that used to be the only behaviour.
func TestOpenDeliveredFileGitRouting(t *testing.T) {
	repo := deliverableTestRepo(t)
	root := gitToplevel(t, repo)
	link := func(path string) string {
		return "vscode://" + deliverableExtID + "/change?path=" + url.QueryEscape(path) + "&root=" + url.QueryEscape(root)
	}

	// scmLeg: the bundled version is already installed, so a click must not
	// spend a --force reinstall on itself; it focuses the project's window and
	// hands the URI over.
	scmLeg := func(t *testing.T, path string) {
		t.Helper()
		_, argv := fakeCodeCLI(t, deliverableExtID+"@"+deliverableExtVersion+"\n", "0")
		if err := openDeliveredFile(path, nil); err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		waitForCodeArg(t, argv, "--open-url")
		want := link(path)
		inv := waitForCodeCall(t, argv, func(args []string) bool {
			return len(args) == 2 && args[0] == "--open-url" && args[1] == want
		})
		last := inv[len(inv)-1]
		if len(last) != 2 || last[0] != "--open-url" || last[1] != want {
			t.Fatalf("last invocation = %v, want the SCM deep link %q", last, want)
		}
		// --open-url lands in the last-active window, so the window holding the
		// project has to be focused first.
		focusAt, openAt := -1, -1
		for i, args := range inv {
			if len(args) == 2 && args[0] == "-r" && args[1] == root {
				focusAt = i
			}
			if hasArg(args, "--open-url") {
				openAt = i
			}
			if hasArg(args, "--install-extension") {
				t.Fatalf("the bundled version is already installed; got %v", inv)
			}
		}
		if focusAt < 0 || openAt < 0 || focusAt > openAt {
			t.Fatalf("want `-r %s` before --open-url; got %v", root, inv)
		}
		u, err := url.Parse(last[1])
		if err != nil {
			t.Fatalf("deep link %q: %v", last[1], err)
		}
		if got := u.Query().Get("path"); got != path {
			t.Fatalf("deep link path = %q, want the clicked path %q", got, path)
		}
		if got := u.Query().Get("root"); got != root {
			t.Fatalf("deep link root = %q, want git's own root %q", got, root)
		}
	}

	// plainLeg: no SCM, no diff — just the editor at the clicked path.
	plainLeg := func(t *testing.T, path string) {
		t.Helper()
		_, argv := fakeCodeCLI(t, "", "0")
		if err := openDeliveredFile(path, nil); err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		inv := waitForCodeCall(t, argv, func(args []string) bool {
			return len(args) == 2 && args[0] == "-r" && args[1] == path
		})
		for _, args := range inv {
			if hasArg(args, "--open-url") || hasArg(args, "--diff") {
				t.Fatalf("a file with no Working Tree diff must not ask for one; got %v", inv)
			}
		}
		if last := inv[len(inv)-1]; len(last) != 2 || last[0] != "-r" || last[1] != path {
			t.Fatalf("last invocation = %v, want a plain open of %s", last, path)
		}
	}

	t.Run("tracked change opens the SCM deep link", func(t *testing.T) {
		scmLeg(t, filepath.Join(repo, "a.md"))
	})
	t.Run("untracked file opens the SCM deep link", func(t *testing.T) {
		scmLeg(t, filepath.Join(repo, "new.md"))
	})
	t.Run("ignored file opens plain", func(t *testing.T) {
		plainLeg(t, filepath.Join(repo, "ignored.md"))
	})
	t.Run("non-git dir opens plain", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "notes.md")
		if err := os.WriteFile(path, []byte("# notes\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		plainLeg(t, path)
	})
}

// TestOpenDeliveredFileInstallFailureFallsBackToDiff pins the second rung: when
// the extension cannot be installed (a locked-down or offline VS Code), the
// click still has to show what changed, so the router writes the index version
// out and opens a two-pane diff against it.
func TestOpenDeliveredFileInstallFailureFallsBackToDiff(t *testing.T) {
	repo := deliverableTestRepo(t)
	path := filepath.Join(repo, "a.md")
	_, argv := fakeCodeCLI(t, "", "1") // nothing listed, install refused
	if err := openDeliveredFile(path, nil); err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	waitForCodeArg(t, argv, "--diff")
	inv := waitForCodeCall(t, argv, func(args []string) bool {
		return len(args) == 4 && args[0] == "-r" && args[1] == "--diff" && args[3] == path
	})
	var diff []string
	for _, args := range inv {
		if len(args) == 4 && args[0] == "-r" && args[1] == "--diff" {
			diff = args
		}
	}
	base, err := os.ReadFile(diff[2])
	if err != nil {
		t.Fatalf("baseline file %q: %v", diff[2], err)
	}
	if string(base) != "one\ntwo\nthree\n" {
		t.Fatalf("baseline = %q, want the index version the diff has to show", base)
	}
	if diff[3] != path {
		t.Fatalf("diff target = %q, want %q", diff[3], path)
	}
}

// TestOpenDeliveredFileBinaryKeepsSystemViewer pins that the text route is not a
// blanket replacement: a binary keeps going to the platform's own viewer, and
// no VS Code process is started for it.
func TestOpenDeliveredFileBinaryKeepsSystemViewer(t *testing.T) {
	viewerDir := fakeViewer(t, ": > \"$0.ran\"\n")
	_, argv := fakeCodeCLI(t, "", "0")
	path := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nIHDR garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := openDeliveredFile(path, nil); err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	waitForViewer(t, viewerDir)
	if inv := codeInvocations(t, argv); len(inv) != 0 {
		t.Fatalf("a binary must not start the text route; got %v", inv)
	}
}

// TestOpenDeliveredFileNoVSCodeKeepsSystemViewer pins the machine without VS
// Code: the router finds no CLI and falls straight through to the platform
// viewer, without running any `code` that happens to be on PATH.
func TestOpenDeliveredFileNoVSCodeKeepsSystemViewer(t *testing.T) {
	viewerDir := fakeViewer(t, ": > \"$0.ran\"\n")
	dir := t.TempDir()
	ran := filepath.Join(dir, "code.ran")
	decoy := "#!/bin/sh\n: > \"" + ran + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "code"), []byte(decoy), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	prev := vscodeCLIPath
	vscodeCLIPath = func() string { return "" }
	t.Cleanup(func() { vscodeCLIPath = prev })

	path := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(path, []byte("# notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := openDeliveredFile(path, nil); err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	waitForViewer(t, viewerDir)
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("no `code` may run when the router found none")
	}
}

// TestOpenDeliveredFileMissingFileReportsSynchronously pins the seam contract
// the click sites rely on: a launch that never happened comes back as the
// return value, so the hint is painted in the click's own turn, while failed —
// which reports a launch that started and then ended badly — stays untouched.
func TestOpenDeliveredFileMissingFileReportsSynchronously(t *testing.T) {
	calls := make(chan error, 1)
	err := openDeliveredFile(filepath.Join(t.TempDir(), "gone.md"), func(e error) { calls <- e })
	if err == nil || err.Error() != "file no longer exists" {
		t.Fatalf("err = %v, want the synchronous missing-file error", err)
	}
	select {
	case e := <-calls:
		t.Fatalf("a launch that never happened must not report through failed; got %v", e)
	default:
	}
}

// TestOpenDeliveredFileEveryRungDeadFallsBackToSystemViewer pins the bottom of
// the ladder: the SCM link, the diff and the plain editor all failing must still
// open the file with the platform viewer instead of doing nothing.
func TestOpenDeliveredFileEveryRungDeadFallsBackToSystemViewer(t *testing.T) {
	repo := deliverableTestRepo(t)
	path := filepath.Join(repo, "a.md")
	viewerDir := fakeViewer(t, ": > \"$0.ran\"\n")
	fakeCodeCLI(t, "", "1")
	t.Setenv("FB_CODE_ALL_EXIT", "1") // every invocation fails
	if err := openDeliveredFile(path, nil); err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	waitForViewer(t, viewerDir)
}

// TestDeliverableExtVSIXManifestMatchesConstant pins the embedded artefact to
// the identity the code depends on: the deep link carries the id and the install
// check compares the version, so a VSIX built from a drifted package.json would
// either never install or never be found installed.
func TestDeliverableExtVSIXManifestMatchesConstant(t *testing.T) {
	zr, err := zip.NewReader(bytes.NewReader(deliverableExtVSIX), int64(len(deliverableExtVSIX)))
	if err != nil {
		t.Fatalf("the embedded VSIX is not a zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != "extension/package.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open the embedded manifest: %v", err)
		}
		var m struct{ Publisher, Name, Version string }
		err = json.NewDecoder(rc).Decode(&m)
		rc.Close()
		if err != nil {
			t.Fatalf("decode the embedded manifest: %v", err)
		}
		if got := m.Publisher + "." + m.Name; got != deliverableExtID {
			t.Fatalf("manifest id = %q, want %q", got, deliverableExtID)
		}
		if m.Version != deliverableExtVersion {
			t.Fatalf("manifest version = %q, want %q", m.Version, deliverableExtVersion)
		}
		return
	}
	t.Fatal("the embedded VSIX carries no extension/package.json")
}

// TestIsTextFileSniffMatrix pins both halves of the text decision: a known text
// extension needs no sniffing at all, and the sniff itself has to call a PNG, a
// NUL-bearing file and a binary only the content sniffer recognises binary —
// while an empty file and an extensionless Makefile stay text.
func TestIsTextFileSniffMatrix(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
		want    bool
	}{
		{"note.md", []byte("# note\n"), true},
		{"main.go", []byte("package main\n"), true},
		{"Makefile", []byte("all:\n\techo hi\n"), true},
		{"empty", nil, true},
		{"shot.png", []byte("\x89PNG\r\n\x1a\nIHDR garbage"), false},
		// Nothing here sniffs as binary (Go answers text/plain for this 4-byte
		// prefix), so only the extension can keep a truncated image out of the
		// text editor.
		{"truncated.png", []byte("\x89PNG"), false},
		{"nul.bin", []byte("hello\x00world"), false},
		// Invalid UTF-8 is not enough on its own: Go's sniffer answers
		// text/plain for high bytes that are no control characters, so the
		// binary verdict comes from a payload it calls octet-stream.
		{"control.bin", []byte("\x01\x02\xfe\xfd"), false},
		{"utf16-ish.bin", []byte("\xff\xfe\xfa"), true},
	}
	dir := t.TempDir()
	for _, c := range cases {
		path := filepath.Join(dir, c.name)
		if err := os.WriteFile(path, c.content, 0o644); err != nil {
			t.Fatal(err)
		}
		if got := isTextFile(path); got != c.want {
			t.Errorf("isTextFile(%s, %q) = %v, want %v", c.name, c.content, got, c.want)
		}
	}
}
