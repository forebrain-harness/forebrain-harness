package safety

import (
	"os"
	"path/filepath"
	"testing"
)

// TestShellWritesConfinedToRoots pins the proof plan mode's scratch admission
// rests on: a shell command runs unasked only when every file it writes lands
// strictly inside the OS temp regions. The positives are the research flows a
// plan-mode agent needs (download, extract, draft, copy out a file to inspect);
// the negatives are the wall - one of them going green would put the project's
// read-only promise at the mercy of the whitelist.
func TestShellWritesConfinedToRoots(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("the OS temp pair ($TMPDIR, /tmp) is a unix shape")
	}
	// The project has to be outside the temp regions for the negatives to mean
	// anything, and t.TempDir() is inside $TMPDIR on every platform, so the
	// project is a plain path outside them. It need not exist: the proof places
	// a path by resolving the deepest ancestor that does.
	project := filepath.Join(string(filepath.Separator), "workspace", "forebrain-harness")
	roots := []string{os.TempDir(), "/tmp"}
	const vsix = "https://marketplace.visualstudio.com/_apis/public/gallery/publishers/anthropic/vsextensions/claude-code/2.1.0/vspackage"

	// The transcript command from the owner's report, which plan mode refused as
	// a proven write: download into /tmp, then unpack and inspect there.
	transcript := `cd /tmp && curl -s --max-time 60 -o claude-code.vsix '` + vsix + `' && ls -la && rm -rf claude-x && mkdir claude-x && cd claude-x && unzip -q ../claude-code.vsix && ls -la`

	tests := []struct {
		name string
		cmd  string
		cwd  string
		want bool
	}{
		// Scratch research: every write lands in the temp regions.
		{name: "the reported transcript command", cmd: transcript, cwd: project, want: true},
		{name: "mkdir in /tmp", cmd: "mkdir /tmp/x", cwd: project, want: true},
		{name: "rm -rf in /tmp", cmd: "rm -rf /tmp/x", cwd: project, want: true},
		{name: "redirect into /tmp", cmd: "echo hi > /tmp/x.txt", cwd: project, want: true},
		{name: "curl output into /tmp", cmd: `curl -s -o /tmp/x.zip 'https://example.com/x.zip'`, cwd: project, want: true},
		{name: "unzip into /tmp", cmd: "unzip -q /tmp/x.zip -d /tmp/claude-x", cwd: project, want: true},
		{name: "cd then relative scratch writes", cmd: `cd /tmp && curl -s -o x.zip 'https://example.com/x.zip' && unzip -q x.zip`, cwd: project, want: true},
		{name: "copy a project file out to scratch", cmd: "cp pkg/tool/state.go /tmp/copy.go", cwd: project, want: true},
		{name: "tee two scratch files", cmd: "tee /tmp/a /tmp/b", cwd: project, want: true},
		{name: "dd into scratch", cmd: "dd if=/dev/zero of=/tmp/z bs=1k count=1", cwd: project, want: true},
		{name: "tar into scratch", cmd: "tar -xzf /tmp/a.tgz -C /tmp/x", cwd: project, want: true},
		{name: "git clone into scratch", cmd: "git clone https://example.com/x.git /tmp/repo", cwd: project, want: true},
		{name: "no redirect and no output: writes nothing", cmd: `curl -s 'https://example.com/x.json'`, cwd: project, want: true},

		// Project writes: the wall.
		{name: "sed -i in the project", cmd: "sed -i '' s/a/b/ main.go", cwd: project, want: false},
		{name: "rm -rf a project directory", cmd: "rm -rf pkg/tool", cwd: project, want: false},
		{name: "redirect into the project", cmd: "echo hi > pkg/tool/x.txt", cwd: project, want: false},
		{name: "move a project file out", cmd: "mv pkg/tool/state.go /tmp/x", cwd: project, want: false},
		{name: "copy into the project", cmd: "cp /tmp/x.zip pkg/tool/x.zip", cwd: project, want: false},
		// The destination rule reads position, and a redirection's target sits
		// in the clause text too: the last word is not the destination here, so
		// every operand has to be inside the regions instead.
		{name: "copy inside the project behind a scratch redirect", cmd: "cp pkg/tool/state.go main.go > /tmp/log", cwd: project, want: false},
		{name: "install inside the project behind a scratch redirect", cmd: "install -m 755 a b > /tmp/log", cwd: project, want: false},
		{name: "the expected root itself", cmd: "rm -rf /tmp", cwd: project, want: false},
		{name: "the expected root itself via TMPDIR", cmd: "rm -rf " + os.TempDir(), cwd: project, want: false},
		{name: "the filesystem root", cmd: "rm -rf /", cwd: project, want: false},
		{name: "an ancestor of the root", cmd: "rm -rf /tmp/..", cwd: project, want: false},
		{name: "a scratch path escaping upwards", cmd: "echo hi > /tmp/../etc/x", cwd: project, want: false},
		{name: "curl's cookie jar is another write", cmd: `curl -o /tmp/x --cookie-jar /tmp/c 'https://example.com/x'`, cwd: project, want: false},
		{name: "curl --output-dir decides where -O writes", cmd: `curl --output-dir /tmp/x -O 'https://example.com/x.zip'`, cwd: project, want: false},
		{name: "command substitution hides the write", cmd: "echo $(date) > /tmp/x", cwd: project, want: false},
		{name: "parameter expansion hides the path", cmd: "echo hi > /tmp/$NAME", cwd: project, want: false},
		{name: "backticks hide the write", cmd: "echo `date` > /tmp/x", cwd: project, want: false},
		{name: "subshell hides the write", cmd: "(cd /tmp && echo hi > x)", cwd: project, want: false},
		{name: "home expansion", cmd: "rm -rf ~/Documents", cwd: project, want: false},
		{name: "an interpreter the proof cannot read", cmd: "python3 /tmp/x.py", cwd: project, want: false},
		{name: "an unknown command", cmd: "frobnicate /tmp/x", cwd: project, want: false},
		{name: "a shell wrapper", cmd: "sh -c 'rm -rf pkg'", cwd: project, want: false},
		{name: "an env prefix does not hide the writer", cmd: "env FOO=1 rm -rf pkg", cwd: project, want: false},
		{name: "a second clause still writes the project", cmd: "rm -rf /tmp/x; rm -rf pkg", cwd: project, want: false},
		{name: "tar -C escaping the region", cmd: "tar -xzf /tmp/a.tgz -C /tmp/../../workspace/forebrain-harness", cwd: project, want: false},
		{name: "mv -t naming the project", cmd: "mv -t pkg/tool /tmp/x", cwd: project, want: false},
		{name: "dd of= naming the project", cmd: "dd if=/tmp/a of=main.go", cwd: project, want: false},
		{name: "chmod on a project file", cmd: "chmod +x main.go", cwd: project, want: false},
		{name: "wget unknown flag", cmd: "wget -O /tmp/x -a /tmp/log 'https://example.com/x'", cwd: project, want: false},
		{name: "git subcommand beyond clone", cmd: "git clone https://example.com/x.git /tmp/repo && git -C /tmp/repo checkout .", cwd: project, want: false},
		{name: "unzip without -d extracts into the cwd", cmd: "unzip /tmp/x.zip", cwd: project, want: false},
		{name: "tar without -C extracts into the cwd", cmd: "tar -xzf /tmp/a.tgz", cwd: project, want: false},
		{name: "tee into the project", cmd: "tee pkg/tool/x.txt", cwd: project, want: false},
		// The body reads like another clause whose writes are scratch-confined,
		// so only the heredoc rule keeps this out.
		{name: "heredoc body is not inspected", cmd: "cat <<EOF > /tmp/x\nmkdir /tmp/y\nEOF", cwd: project, want: false},
		{name: "empty command", cmd: "   ", cwd: project, want: false},
		{name: "unknown flag on a writer", cmd: "rm --frobnicate /tmp/x", cwd: project, want: false},
		{name: "chmod with no target", cmd: "chmod +x", cwd: project, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShellWritesConfinedToRoots(tt.cmd, roots, tt.cwd); got != tt.want {
				t.Fatalf("ShellWritesConfinedToRoots(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

// A link inside the scratch region carries a write back into the project. The
// proof resolves it, which is what keeps "confined to /tmp" from being a claim
// about spelling rather than about the file that gets written.
func TestShellWritesConfinedToRootsRefusesSymlinkEscape(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("symlink layout is unix-specific here")
	}
	root := t.TempDir()
	project := t.TempDir()
	escape := filepath.Join(root, "escape")
	if err := os.Symlink(project, escape); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	roots := []string{root}
	for _, cmd := range []string{
		"echo hi > " + escape + "/x.txt",
		"mkdir " + escape + "/nested",
		"rm -rf " + escape + "/main.go",
		"cd " + escape + " && echo hi > x.txt",
	} {
		if ShellWritesConfinedToRoots(cmd, roots, project) {
			t.Fatalf("%q writes through a link into the project and must not be admitted", cmd)
		}
	}
	// The same shapes spelled inside the region stay admitted.
	for _, cmd := range []string{
		"echo hi > " + filepath.Join(root, "x.txt"),
		"mkdir " + filepath.Join(root, "nested"),
	} {
		if !ShellWritesConfinedToRoots(cmd, roots, project) {
			t.Fatalf("%q writes inside the region and must be admitted", cmd)
		}
	}
}

// A workspace that lives inside $TMPDIR is still the project: the region never
// outranks it, or plan mode would stop protecting the one thing it protects.
func TestShellWritesConfinedToRootsKeepsTheProjectOutOfScratch(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("the OS temp pair is a unix shape")
	}
	inside := filepath.Join(os.TempDir(), "fb-387-checkout")
	for _, cmd := range []string{
		"mkdir " + inside + "/x",
		"echo hi > " + inside + "/x.txt",
		"rm -rf " + inside + "/pkg",
		"cd " + inside + " && echo hi > x.txt",
	} {
		if ShellWritesConfinedToRoots(cmd, []string{os.TempDir(), "/tmp"}, inside) {
			t.Fatalf("%q writes the project and must not be admitted", cmd)
		}
	}
	// The regions the project does not sit in keep working.
	if !ShellWritesConfinedToRoots("mkdir /tmp/fb-387-x", []string{os.TempDir(), "/tmp"}, inside) {
		t.Fatal("a region beside the project must stay writable")
	}
}

// The proof admits a command from the classifier's read-only table with
// unrestricted operands, which is only sound while every name in that table
// writes nothing whatever its arguments. A name the classifier dispatches by
// argument (find, git, sed, sort...) is not one of those, and adding it to the
// table would open the wall silently - so this pins the invariant instead.
func TestShellReadOnlyTableAdmitsNoArgumentAwareCommand(t *testing.T) {
	args := []string{"-delete", "-i", "-o", "*"}
	for name := range shellReadOnlyCommands {
		if got := classifyCommand(name, args, nil, ""); got != ShellMutationReadOnly {
			t.Fatalf("%q is in the read-only table but classifies as %s with %v - the scratch proof would admit it unread", name, got, args)
		}
	}
}

// A link stepped through with ".." is the shape that broke the first version of
// this proof: "<link>/../x" collapses lexically to "<root>/x", while the kernel
// resolves <link> and then steps to the target's parent, so the write lands in
// the project. Every form below was admitted before the walk resolved the path
// component by component, and `rm -rf` really deleted a project file.
func TestShellWritesConfinedToRootsResolvesDotDotThroughLinks(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("symlink layout is unix-specific here")
	}
	root := t.TempDir()
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(project, "src.txt")
	if err := os.WriteFile(source, []byte("src\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "zz-link")
	if err := os.Symlink(filepath.Join(project, "pkg"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	roots := []string{root}

	for _, cmd := range []string{
		"echo hi > " + link + "/../escaped.txt",
		"mkdir " + link + "/../escaped-dir",
		"rm -rf " + link + "/../gone.txt",
		"cp " + source + " " + link + "/../copied.txt",
		"tee " + link + "/../tee.txt",
		"tar -xzf " + link + "/../a.tgz -C " + link + "/..",
		// The same shape reached through a cd: tracking the spelling instead of
		// the resolved directory would put every relative write below it in the
		// link target's parent.
		"cd " + link + "/.. && echo hi > tracked.txt",
	} {
		if ShellWritesConfinedToRoots(cmd, roots, project) {
			t.Fatalf("%q steps through a link with .. and writes the project; it must not be admitted", cmd)
		}
	}

	// The fix must not survive by refusing everything: a real scratch write
	// beside the link is still admitted, and so is the link's own directory.
	for _, cmd := range []string{
		"echo hi > " + filepath.Join(root, "beside.txt"),
		"mkdir " + filepath.Join(root, "beside-dir"),
		"cp " + source + " " + filepath.Join(root, "copy.txt"),
		"cd " + root + " && echo hi > beside.txt",
	} {
		if !ShellWritesConfinedToRoots(cmd, roots, project) {
			t.Fatalf("%q writes inside the region and must be admitted", cmd)
		}
	}
}

// ShellScratchRoots is the one definition of "the disposable regions": the
// sandbox's temp policy and the plan-mode proof must not describe them twice.
func TestShellScratchRootsIsTheTempPair(t *testing.T) {
	roots := ShellScratchRoots()
	if len(roots) == 0 {
		t.Fatal("ShellScratchRoots returned nothing")
	}
	if filepath.Separator == '/' && len(roots) < 2 {
		t.Fatalf("ShellScratchRoots = %v, want $TMPDIR and /tmp", roots)
	}
	seen := map[string]struct{}{}
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			t.Fatalf("scratch root %q is not absolute", root)
		}
		if _, dup := seen[root]; dup {
			t.Fatalf("scratch root %q is listed twice: %v", root, roots)
		}
		seen[root] = struct{}{}
		if info, err := os.Stat(root); err == nil && !info.IsDir() {
			t.Fatalf("scratch root %q is not a directory", root)
		}
	}
}
