package safety

import (
	"os"
	"path/filepath"
	"testing"
)

// projectOutsideRoots is a plain absolute path outside the scratch regions. It
// need not exist: the proof places a path by resolving the deepest ancestor that
// does. t.TempDir() would NOT do - it lives inside $TMPDIR, one of the roots.
var projectOutsideRoots = filepath.Join(string(filepath.Separator), "workspace", "forebrain-harness")

// TestShellScratchWriteVerdict pins the proof plan mode's scratch admission
// rests on: a shell command runs unasked only when every file it writes lands
// strictly inside the OS temp regions, and every answer says which of the four
// states it reached. The positives are the research flows a plan-mode agent
// needs (download, unpack, draft, copy out a file to inspect); the negatives are
// the wall - one of them going Clean would put the project's read-only promise
// at the mercy of the whitelist.
func TestShellScratchWriteVerdict(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("the OS temp pair ($TMPDIR, /tmp) is a unix shape")
	}
	// Rule 2's relative-cd answer depends on the environment: a developer shell
	// that exports CDPATH must not be able to flip a row.
	t.Setenv("CDPATH", "")
	roots := []string{os.TempDir(), "/tmp"}
	project := projectOutsideRoots
	const vsix = "https://marketplace.visualstudio.com/_apis/public/gallery/publishers/anthropic/vsextensions/claude-code/2.1.0/vspackage"

	// The transcript command from the owner's report, which plan mode refused as
	// a proven write: download into /tmp, then unpack and inspect there.
	transcript := `cd /tmp && curl -s --max-time 60 -o claude-code.vsix '` + vsix + `' && ls -la && rm -rf claude-x && mkdir claude-x && cd claude-x && unzip -q ../claude-code.vsix && ls -la`

	tests := []struct {
		name string
		cmd  string
		want ShellScratchWriteState
		why  string
	}{
		// Every write lands in the temp regions.
		{name: "the reported transcript command", cmd: transcript, want: ShellScratchWritesClean, why: "every target is /tmp or below it"},
		{name: "mkdir in /tmp", cmd: "mkdir /tmp/x", want: ShellScratchWritesClean, why: "the operand is the target and it is in a root"},
		{name: "rm -rf in /tmp", cmd: "rm -rf /tmp/x", want: ShellScratchWritesClean, why: "removing scratch is scratch"},
		{name: "redirect into /tmp", cmd: "echo hi > /tmp/x.txt", want: ShellScratchWritesClean, why: "the only write is a confined redirect"},
		{name: "curl output into /tmp", cmd: `curl -s -o /tmp/x.zip 'https://example.com/x.zip'`, want: ShellScratchWritesClean, why: "-o names the one file"},
		{name: "unzip into /tmp", cmd: "unzip -q /tmp/x.zip -d /tmp/claude-x", want: ShellScratchWritesClean, why: "-d names the directory"},
		{name: "cd then relative scratch writes", cmd: `cd /tmp && curl -s -o x.zip 'https://example.com/x.zip' && unzip -q x.zip`, want: ShellScratchWritesClean, why: "the tracked directory follows the cd"},
		{name: "copy a project file out to scratch", cmd: "cp pkg/tool/state.go /tmp/copy.go", want: ShellScratchWritesClean, why: "only the destination is written"},
		{name: "tee two scratch files", cmd: "tee /tmp/a /tmp/b", want: ShellScratchWritesClean, why: "both operands are written and both are scratch"},
		{name: "dd into scratch", cmd: "dd if=/dev/zero of=/tmp/z bs=1k count=1", want: ShellScratchWritesClean, why: "of= is the only destination"},
		{name: "tar into scratch", cmd: "tar -xzf /tmp/a.tgz -C /tmp/x", want: ShellScratchWritesClean, why: "-C names the directory"},
		{name: "tar creating an archive in scratch", cmd: "tar -czf /tmp/a.tgz -C /tmp/x .", want: ShellScratchWritesClean, why: "-f names the archive; -C only moves the read base"},
		{name: "tar creating an archive in scratch without -C", cmd: "tar -cf /tmp/a.tar .", want: ShellScratchWritesClean, why: "a creating tar writes only -f, so its cwd is read rather than written"},
		{name: "tar listing a scratch archive", cmd: "tar -tzf /tmp/a.tgz", want: ShellScratchWritesClean, why: "a listing writes nothing, so its cwd is not a write"},
		{name: "tar listing an uncompressed archive", cmd: "tar -tf /tmp/a.tar", want: ShellScratchWritesClean, why: "the long and short list modes are the same mode"},
		{name: "tar listing with a project -C", cmd: "tar -tzf /tmp/a.tgz -C pkg/tool", want: ShellScratchWritesClean, why: "-C only moves where the listing reads, and it writes nothing"},
		{name: "grep -c on a scratch file", cmd: "grep -c x /tmp/f", want: ShellScratchWritesClean, why: "grep's -c counts matches; it is not a shell's -c"},
		{name: "unzip -c writes to stdout", cmd: "unzip -c /tmp/a.zip", want: ShellScratchWritesClean, why: "-c extracts to stdout, so no directory has to be inside a region"},
		{name: "git clone into scratch", cmd: "git clone https://example.com/x.git /tmp/repo", want: ShellScratchWritesClean, why: "clone's last operand is the destination"},
		{name: "no redirect and no output: writes nothing", cmd: `curl -s 'https://example.com/x.json'`, want: ShellScratchWritesClean, why: "no -o and no -O saves nothing"},
		{name: "mkdir in /tmp after a cd", cmd: "cd /tmp && mkdir x", want: ShellScratchWritesClean, why: "the tracked directory is scratch"},
		{name: "the reported research pipeline", cmd: `cd /tmp && curl -s -o cc.vsix 'https://example.com/cc.vsix' && rm -rf claude-x && mkdir claude-x && cd claude-x && unzip -q ../cc.vsix`, want: ShellScratchWritesClean, why: "every step stays under /tmp"},
		{name: "git clone with literal operands", cmd: "git clone /tmp/a /tmp/b", want: ShellScratchWritesClean, why: "the destination is scratch"},
		{name: "a wrapper around a scratch writer", cmd: "nohup tar -czf /tmp/a.tgz .", want: ShellScratchWritesClean, why: "the wrapper is unwrapped and the tar clause writes scratch"},

		// Command substitution is classified, not merely doubted: a source that
		// writes nothing leaves the line clean.
		{name: "command substitution of a read-only command", cmd: "echo $(date) > /tmp/x", want: ShellScratchWritesClean, why: "date writes nothing and the redirect is scratch"},
		{name: "backticks of a read-only command", cmd: "echo `date` > /tmp/x", want: ShellScratchWritesClean, why: "same substitution, different spelling"},
		{name: "arithmetic expansion", cmd: "echo $((1+2)) > /tmp/x", want: ShellScratchWritesClean, why: "arithmetic is a value, not a command"},
		{name: "parameter expansion in the text", cmd: `printf '%s' "$PWD" > /tmp/x`, want: ShellScratchWritesClean, why: "a parameter expansion is a value, and the redirect is scratch"},

		// Grouping and read-only shapes the owner ruled keep running.
		{name: "subshell around a scratch write", cmd: "(cd /tmp && echo hi > x)", want: ShellScratchWritesClean, why: "the tracked directory and the target are both scratch"},
		{name: "brace group around a reader", cmd: "{ ls; }", want: ShellScratchWritesClean, why: "nothing writes"},
		{name: "home path through a reader", cmd: "ls ~/.forebrain", want: ShellScratchWritesClean, why: "ls writes nothing whatever it is pointed at"},
		{name: "subshell around a read-only test", cmd: "(cd pkg && go test ./...)", want: ShellScratchWritesUnread, why: "go test writes an unnamed place, so the answer is a question, not a refusal"},
		{name: "cd into a home expansion then read", cmd: "cd $HOME && ls", want: ShellScratchWritesClean, why: "the cd leaves the directory unknown and ls writes nothing"},

		// Heredoc bodies are data when the whole line only feeds a reader.
		{name: "quoted heredoc into scratch", cmd: "cat > /tmp/x <<'EOF'\nrm -rf pkg/tool\nEOF", want: ShellScratchWritesClean, why: "a quoted delimiter makes the body literal"},
		{name: "heredoc body listing scratch writes", cmd: "cat <<EOF > /tmp/x\nmkdir /tmp/y\nEOF", want: ShellScratchWritesClean, why: "the body only feeds cat, and it holds no expansion"},
		{name: "heredoc after a cd", cmd: "cd /tmp && cat <<'EOF'\nbody\nEOF", want: ShellScratchWritesClean, why: "data feeding cat writes nothing"},
		{name: "heredoc with no redirect", cmd: "cat <<'EOF'\nbody\nEOF", want: ShellScratchWritesClean, why: "cat writes nothing"},
		{name: "herestring into scratch", cmd: "cat <<< hi > /tmp/x", want: ShellScratchWritesClean, why: "a herestring has no body and its operand is data"},

		// sh -c is classified by the script it runs.
		{name: "sh -c running a reader", cmd: "sh -c 'ls -la'", want: ShellScratchWritesClean, why: "the script writes nothing"},
		{name: "sh -c writing scratch", cmd: "sh -c 'echo hi > /tmp/x'", want: ShellScratchWritesClean, why: "the script's redirect is scratch"},
		{name: "env prefix before sh -c", cmd: "env X=1 sh -c 'echo hi > /tmp/x'", want: ShellScratchWritesClean, why: "the wrapper is unwrapped and the script is scratch"},
		{name: "timeout before sh -c", cmd: "timeout 5 sh -c 'tar -xzf /tmp/a.tgz -C /tmp/x'", want: ShellScratchWritesClean, why: "the duration is not the command; the script is scratch"},

		// An unreadable command: nothing proven to write, but nothing proven
		// either.
		{name: "an interpreter the proof cannot read", cmd: "python3 /tmp/x.py", want: ShellScratchWritesUnread, why: "python3 is not in any table"},
		{name: "an unknown command", cmd: "frobnicate /tmp/x", want: ShellScratchWritesUnread, why: "unknown name, no redirect"},
		{name: "go test alone", cmd: "go test ./pkg/tool", want: ShellScratchWritesUnread, why: "the tier is unproven and no redirect names a file"},
		{name: "a pager is not read-only", cmd: "less /tmp/f", want: ShellScratchWritesUnread, why: "less can hand the user a shell, so its name proves nothing"},
		{name: "a provable scratch write beside an unreadable command", cmd: "mkdir -p /tmp/x && go test ./pkg/tool", want: ShellScratchWritesUnread, why: "the worst clause answers for the line, and go test is only a question"},
		{name: "an exported expansion", cmd: `export A="$PWD/$(go env GOOS)"`, want: ShellScratchWritesUnread, why: "export writes nothing but is not in the read-only table"},
		{name: "an interpreter reading a heredoc", cmd: "python3 - <<'EOF'\nprint(1)\nEOF", want: ShellScratchWritesUnread, why: "python3 is not a data reader, so the body parses as commands"},

		// Project writes: the wall.
		{name: "sed -i in the project", cmd: "sed -i '' s/a/b/ main.go", want: ShellScratchWritesUnplaceable, why: "the tier proves a write and no operand says where it lands"},
		{name: "rm -rf a project directory", cmd: "rm -rf pkg/tool", want: ShellScratchWritesOutside, why: "the operand resolves inside the project"},
		{name: "redirect into the project", cmd: "echo hi > pkg/tool/x.txt", want: ShellScratchWritesOutside, why: "the redirect target is in the project"},
		{name: "move a project file out", cmd: "mv pkg/tool/state.go /tmp/x", want: ShellScratchWritesOutside, why: "mv writes both operands"},
		{name: "copy into the project", cmd: "cp /tmp/x.zip pkg/tool/x.zip", want: ShellScratchWritesOutside, why: "the destination is in the project"},
		{name: "copy inside the project behind a scratch redirect", cmd: "cp pkg/tool/state.go main.go > /tmp/log", want: ShellScratchWritesOutside, why: "a redirect in the clause makes every operand read as a destination"},
		{name: "install inside the project behind a scratch redirect", cmd: "install -m 755 a b > /tmp/log", want: ShellScratchWritesOutside, why: "same rule for install"},
		{name: "the expected root itself", cmd: "rm -rf /tmp", want: ShellScratchWritesOutside, why: "removing a region is not confined research"},
		{name: "the expected root itself via TMPDIR", cmd: "rm -rf " + os.TempDir(), want: ShellScratchWritesOutside, why: "the same rule for $TMPDIR"},
		{name: "the filesystem root", cmd: "rm -rf /", want: ShellScratchWritesOutside, why: "the root is not inside anything"},
		{name: "an ancestor of the root", cmd: "rm -rf /tmp/..", want: ShellScratchWritesOutside, why: "stepping up leaves the region"},
		{name: "a scratch path escaping upwards", cmd: "echo hi > /tmp/../etc/x", want: ShellScratchWritesOutside, why: "resolved, the target is not in a region"},
		{name: "curl's cookie jar is another write", cmd: `curl -o /tmp/x --cookie-jar /tmp/c 'https://example.com/x'`, want: ShellScratchWritesUnplaceable, why: "the flag is not in the table, so the proof stops reading"},
		{name: "curl --output-dir decides where -O writes", cmd: `curl --output-dir /tmp/x -O 'https://example.com/x.zip'`, want: ShellScratchWritesUnplaceable, why: "an unread flag can decide a write"},
		{name: "parameter expansion hides the path", cmd: "echo hi > /tmp/$NAME", want: ShellScratchWritesUnplaceable, why: "the target is not literal"},
		{name: "home expansion", cmd: "rm -rf ~/Documents", want: ShellScratchWritesUnplaceable, why: "a word-start ~ puts the target outside the proof's reach"},
		{name: "a shell wrapper onto the project", cmd: "sh -c 'rm -rf pkg'", want: ShellScratchWritesOutside, why: "the script's operand resolves inside the project"},
		{name: "an env prefix does not hide the writer", cmd: "env FOO=1 rm -rf pkg", want: ShellScratchWritesOutside, why: "the wrapper is unwrapped and rm's operand resolves inside the project"},
		{name: "a second clause still writes the project", cmd: "rm -rf /tmp/x; rm -rf pkg", want: ShellScratchWritesOutside, why: "the worst clause answers for the line"},
		{name: "tar -C escaping the region", cmd: "tar -xzf /tmp/a.tgz -C /tmp/../../workspace/forebrain-harness", want: ShellScratchWritesOutside, why: "the resolved -C is the project"},
		{name: "mv -t naming the project", cmd: "mv -t pkg/tool /tmp/x", want: ShellScratchWritesOutside, why: "-t is the destination"},
		{name: "ln -t naming the project", cmd: "ln -t pkg/tool /tmp/x", want: ShellScratchWritesOutside, why: "ln honours -t like mv and cp do"},
		{name: "dd of= naming the project", cmd: "dd if=/tmp/a of=main.go", want: ShellScratchWritesOutside, why: "of= is the destination"},
		{name: "chmod on a project file", cmd: "chmod +x main.go", want: ShellScratchWritesOutside, why: "the target is in the project"},
		{name: "wget unknown flag", cmd: "wget -O /tmp/x -a /tmp/log 'https://example.com/x'", want: ShellScratchWritesUnplaceable, why: "an unread flag can decide a write"},
		{name: "git subcommand beyond clone", cmd: "git clone https://example.com/x.git /tmp/repo && git -C /tmp/repo checkout .", want: ShellScratchWritesOutside, why: "only clone's destination is in the text; checkout writes a repository this proof does not read"},
		{name: "unzip without -d extracts into the cwd", cmd: "unzip /tmp/x.zip", want: ShellScratchWritesOutside, why: "the cwd is the project"},
		{name: "tar without -C extracts into the cwd", cmd: "tar -xzf /tmp/a.tgz", want: ShellScratchWritesOutside, why: "the cwd is the project"},
		{name: "tar extracting into a project -C", cmd: "tar -xzf /tmp/a.tgz -C pkg/tool", want: ShellScratchWritesOutside, why: "extraction still lands where -C names"},
		{name: "tar creating an archive in the project", cmd: "tar -czf pkg/tool/x.tar .", want: ShellScratchWritesOutside, why: "-f names the archive and it is in the project"},
		{name: "tee into the project", cmd: "tee pkg/tool/x.txt", want: ShellScratchWritesOutside, why: "the operand is in the project"},
		{name: "unknown flag on a writer", cmd: "rm --frobnicate /tmp/x", want: ShellScratchWritesUnplaceable, why: "an unread flag can decide a write"},
		{name: "chmod with no target", cmd: "chmod +x", want: ShellScratchWritesUnplaceable, why: "the proof cannot read where it writes"},
		{name: "a basename outside the table", cmd: "/bin/rm -rf pkg/tool", want: ShellScratchWritesOutside, why: "the command word is normalised to its basename"},
		{name: "an upper-case command name", cmd: "RM -rf pkg/tool", want: ShellScratchWritesOutside, why: "the command word is lowercased"},
		{name: "a relative write after a tracked cd", cmd: "cd /tmp && pushd /etc && echo hi > x", want: ShellScratchWritesUnplaceable, why: "pushd leaves the directory unknown, so a relative target cannot be placed"},
		{name: "a cd back to the previous directory", cmd: "cd /tmp && cd - && echo hi > state.go", want: ShellScratchWritesUnplaceable, why: "cd - leaves the directory unknown"},
		{name: "a relative cd under CDPATH", cmd: "cd /tmp && CDPATH=" + project + "/pkg cd tool && echo pwned > x.go", want: ShellScratchWritesUnplaceable, why: "bash searches CDPATH before the tracked directory"},
		{name: "a subshell cd does not bound the write", cmd: "(cd pkg && echo hi > state.go)", want: ShellScratchWritesOutside, why: "the tracked directory is the project's pkg"},
		{name: "a closed subshell restores the tracked directory", cmd: "(cd /tmp) && echo hi > x", want: ShellScratchWritesOutside, why: "the cd ran in a subshell, so the second clause writes the project"},
		{name: "a closed subshell restore before a writer", cmd: "(cd pkg && rm -rf tool)", want: ShellScratchWritesOutside, why: "the write is inside the subshell's directory, which is the project's pkg"},
		{name: "a writer after a closed subshell cd", cmd: "(cd /tmp); rm -rf pkg/tool", want: ShellScratchWritesOutside, why: "the cd left with the subshell, so the rm runs in the project"},
		{name: "a writer inside a subshell in scratch", cmd: "(cd /tmp && rm -rf pkg/tool)", want: ShellScratchWritesClean, why: "the cd and the rm are both inside the subshell, so the target is /tmp/pkg/tool"},
		{name: "a closing token with nothing to close", cmd: ") && echo hi > x", want: ShellScratchWritesUnplaceable, why: "a ) whose ( the walk never saw leaves the directory unknown"},

		// Command substitution that writes.
		{name: "substitution writing the project behind a scratch redirect", cmd: "echo $(rm -rf pkg/tool) > /tmp/x", want: ShellScratchWritesUnplaceable, why: "a substitution source that writes cannot be placed"},
		{name: "substitution in a herestring", cmd: "touch /tmp/f <<<$(rm -rf pkg)", want: ShellScratchWritesUnplaceable, why: "rule 1 reads every clause's substitutions"},
		{name: "substitution in a flag value", cmd: "mkdir -m $(rm -rf pkg) /tmp/x", want: ShellScratchWritesUnplaceable, why: "same"},
		{name: "substitution inside a parameter expansion", cmd: `echo "${x:-$(rm -rf pkg)}" > /tmp/y`, want: ShellScratchWritesUnplaceable, why: "the nested substitution is read too"},
		{name: "a trap action", cmd: "trap 'rm -rf pkg/tool' EXIT", want: ShellScratchWritesUnplaceable, why: "the trap action is a substitution source"},
		{name: "a substitution as the cd operand", cmd: "cd $(rm -rf pkg/tool)", want: ShellScratchWritesUnplaceable, why: "rule 1 answers before the cd rule"},
		{name: "a non-literal writer operand", cmd: "mkdir /tmp/$X", want: ShellScratchWritesUnplaceable, why: "a written operand must be literal"},
		{name: "a computed writer operand", cmd: "mkdir /tmp/x-$(date +%s)", want: ShellScratchWritesUnplaceable, why: "the operand is not literal"},
		{name: "a non-literal redirect target", cmd: "echo hi > $OUT", want: ShellScratchWritesUnplaceable, why: "the target is not literal"},
		{name: "a non-literal redirect beside a scratch script", cmd: "sh -c 'true' > $OUT", want: ShellScratchWritesUnplaceable, why: "the clause's own redirect is placed before the script is judged"},
		{name: "a project redirect beside a harmless script", cmd: "> pkg/tool/x.txt sh -c 'true'", want: ShellScratchWritesOutside, why: "the redirect names the project"},
		{name: "a scratch redirect before a wrapper", cmd: "> /tmp/x sh -c 'rm -rf pkg/tool'", want: ShellScratchWritesOutside, why: "the redirect is scratch, and the script it feeds writes the project"},
		{name: "a scratch redirect spelled like a command name", cmd: "> /tmp/cat sh -c 'rm -rf pkg/tool'", want: ShellScratchWritesOutside, why: "a target that looks like cat is still a target, so the script is what decides"},
		{name: "a scratch redirect before a writer", cmd: "> /tmp/cat rm -rf pkg/tool", want: ShellScratchWritesOutside, why: "rm is read from the words after the target"},

		// sh -c that writes.
		{name: "sh -c writing the project", cmd: "sh -c 'rm -rf pkg/tool'", want: ShellScratchWritesOutside, why: "the script is classified as if it were the line"},
		{name: "bash -lc writing the project", cmd: "bash -lc 'rm -rf pkg/tool'", want: ShellScratchWritesOutside, why: "-c inside a cluster is the same form"},
		{name: "sh -ec writing the project", cmd: "sh -ec 'rm -rf pkg/tool'", want: ShellScratchWritesOutside, why: "same"},
		{name: "nested sh -c", cmd: `sh -c "sh -c 'rm -rf pkg/tool'"`, want: ShellScratchWritesOutside, why: "each layer is unwrapped"},

		// eval runs its operand as shell source, so a literal operand is read as
		// the line it is - the same way a `sh -c` script is.
		{name: "eval writing the project", cmd: "eval 'rm -rf pkg/tool'", want: ShellScratchWritesOutside, why: "the operand is the line that runs"},
		{name: "eval cd then a project write", cmd: "eval 'cd pkg && rm -rf tool'", want: ShellScratchWritesOutside, why: "the operand is walked as its own line"},
		{name: "eval redirecting into the project", cmd: "eval 'echo hi > state.go'", want: ShellScratchWritesOutside, why: "the redirect lands in the tracked directory"},
		{name: "eval writing scratch", cmd: "eval 'echo hi > /tmp/x'", want: ShellScratchWritesClean, why: "the operand's only write is scratch"},
		{name: "eval reading", cmd: "eval 'ls -la'", want: ShellScratchWritesClean, why: "the operand writes nothing"},
		{name: "eval of an unreadable operand", cmd: `eval "$X"`, want: ShellScratchWritesUnread, why: "the operand is not literal, so the line is a question rather than a refusal"},

		// Writers behind grouping, negation, exec and reserved words (owner
		// ruling 2026-11-26: these are refused).
		{name: "a writer in a subshell", cmd: "(rm -rf pkg/tool)", want: ShellScratchWritesUnplaceable, why: "a grouping token can move where the write lands"},
		{name: "a writer in a brace group", cmd: "{ rm -rf pkg/tool; }", want: ShellScratchWritesUnplaceable, why: "same"},
		{name: "a writer behind negation", cmd: "! sed -i '' s/a/b/ main.go", want: ShellScratchWritesUnplaceable, why: "the tier proves the write and nothing bounds it"},
		{name: "a writer behind exec", cmd: "exec go test -c ./pkg/tool", want: ShellScratchWritesUnplaceable, why: "same"},
		{name: "a find action in a subshell", cmd: "(find . -delete)", want: ShellScratchWritesUnplaceable, why: "-delete is a proven write"},
		{name: "a writer behind a reserved word", cmd: "if true; then rm -rf pkg/tool; fi", want: ShellScratchWritesOutside, why: "the reserved word is skipped and rm names the project"},

		// Heredoc bodies that are not data.
		{name: "an unquoted heredoc body that expands", cmd: "cat >> /tmp/x <<EOF\n$(rm -rf pkg/tool)\nEOF", want: ShellScratchWritesUnplaceable, why: "the shell expands an unquoted body before cat sees it"},
		{name: "a quoted heredoc body piped to a shell", cmd: "cat <<'EOF' | sh\nrm -rf pkg/tool\nEOF", want: ShellScratchWritesOutside, why: "sh is not a data reader, so the body runs"},
		{name: "a shell reading a heredoc", cmd: "sh <<'EOF'\nrm -rf \"$d\"/pkg/tool\nEOF", want: ShellScratchWritesUnplaceable, why: "the body runs and its operand is not literal"},

		// Nothing to run, and nothing to prove.
		{name: "empty command", cmd: "   ", want: ShellScratchWritesClean, why: "an empty line runs nothing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShellScratchWriteVerdict(tt.cmd, roots, project); got != tt.want {
				t.Fatalf("ShellScratchWriteVerdict(%q) = %v, want %v (%s)", tt.cmd, got, tt.want, tt.why)
			}
		})
	}
}

// The subshell counts are what let the walk put a directory back after a ")",
// and a substitution's own parentheses are not one of them: "$(cd /tmp)" is a
// value the clause expands, not a subshell the clause is inside.
func TestSplitShellClassifyClausesSubshellCounts(t *testing.T) {
	cases := []struct {
		cmd         string
		open, close int
	}{
		{"(cd /tmp) && echo hi > x", 1, 1},
		{"(cd /tmp && echo hi > x)", 1, 1},
		{"{ ls; }", 1, 1},
		{"echo $(cd /tmp) > /tmp/x", 0, 0},
		{"echo $((1+2)) > /tmp/x", 0, 0},
		{"echo hi > /tmp/x", 0, 0},
	}
	for _, tt := range cases {
		clauses, _, _ := splitShellClassifyClauses(tt.cmd)
		open, closing := 0, 0
		for _, clause := range clauses {
			open += clause.SubshellOpen
			closing += clause.SubshellClose
		}
		if open != tt.open || closing != tt.close {
			t.Fatalf("%q = %d open, %d close; want %d, %d", tt.cmd, open, closing, tt.open, tt.close)
		}
	}
}

// A write the proof cannot place must never come back Clean: Clean is the answer
// that lets the gate run the command unasked under danger-full-access, so an
// admission here is a project write plan mode promised not to make. This is the
// load-bearing half of the table above - it fails on any relaxation that turns a
// placeable-only proof into "cannot be refuted, so allow".
func TestShellScratchWriteVerdictNeverCleanForUnplacedWrites(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("the OS temp pair is a unix shape")
	}
	t.Setenv("CDPATH", "")
	roots := ShellScratchRoots()
	project := projectOutsideRoots
	for _, cmd := range []string{
		`> /tmp/x sh -c 'rm -rf pkg/tool'`,
		`> /tmp/cat sh -c 'rm -rf pkg/tool'`,
		`> /tmp/cat rm -rf pkg/tool`,
		`> pkg/tool/x.txt sh -c 'true'`,
		`sh -c 'true' > $OUT`,
		"cat >> /tmp/x <<EOF\n$(rm -rf pkg/tool)\nEOF",
		"echo $(rm -rf pkg/tool) > /tmp/x",
		"trap 'rm -rf pkg/tool' EXIT",
		"cd $(rm -rf pkg/tool)",
		"cd /tmp && cd - && echo hi > state.go",
		"cd /tmp && pushd /etc && echo hi > x",
		"cd /tmp && CDPATH=" + project + "/pkg cd tool && echo pwned > x.go",
		"ln -t pkg/tool /tmp/x",
		"echo hi > $OUT",
		"mkdir /tmp/$X",
		"mkdir /tmp/x-$(date +%s)",
		"sed -i '' s/a/b/ main.go",
		"rm -rf pkg/tool",
		"/bin/rm -rf pkg/tool",
		"echo hi > pkg/tool/x.txt",
		"sh <<'EOF'\nrm -rf \"$d\"/pkg/tool\nEOF",
		"cat <<'EOF' | sh\nrm -rf pkg/tool\nEOF",
		"sh -c 'rm -rf pkg/tool'",
		"bash -lc 'rm -rf pkg/tool'",
		`sh -c "sh -c 'rm -rf pkg/tool'"`,
		"(rm -rf pkg/tool)",
		"{ rm -rf pkg/tool; }",
		"! sed -i '' s/a/b/ main.go",
		"exec go test -c ./pkg/tool",
		"(find . -delete)",
		"if true; then rm -rf pkg/tool; fi",
	} {
		if got := ShellScratchWriteVerdict(cmd, roots, project); got == ShellScratchWritesClean {
			t.Fatalf("%q = Clean; a write the proof cannot place must not be admitted", cmd)
		}
	}
	// The other half of the promise: these really do stay inside the regions, so
	// a proof that refused them would be a wall rather than a boundary.
	for _, cmd := range []string{
		"git clone /tmp/a /tmp/b",
		"mkdir /tmp/x",
		"sh -c 'ls -la'",
		"sh -c 'echo hi > /tmp/x'",
		"env X=1 sh -c 'echo hi > /tmp/x'",
		"timeout 5 sh -c 'tar -xzf /tmp/a.tgz -C /tmp/x'",
		`printf '%s' "$PWD" > /tmp/x`,
		"cat > /tmp/x <<'EOF'\nrm -rf pkg/tool\nEOF",
		"cd /tmp && curl -s -o x.zip 'https://example.com/x.zip' && unzip -q x.zip",
		"cd /tmp && mkdir x",
		"cd /tmp && cat <<'EOF'\nbody\nEOF",
		"cd /tmp && curl -s -o cc.vsix 'https://example.com/cc.vsix' && rm -rf claude-x && mkdir claude-x && cd claude-x && unzip -q ../cc.vsix",
	} {
		if got := ShellScratchWriteVerdict(cmd, roots, project); got != ShellScratchWritesClean {
			t.Fatalf("%q = %v, want Clean", cmd, got)
		}
	}
}

// A link inside the scratch region carries a write back into the project. The
// proof resolves it, which is what keeps "confined to /tmp" from being a claim
// about spelling rather than about the file that gets written.
func TestShellScratchWriteVerdictRefusesSymlinkEscape(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("symlink layout is unix-specific here")
	}
	t.Setenv("CDPATH", "")
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
		if got := ShellScratchWriteVerdict(cmd, roots, project); got == ShellScratchWritesClean {
			t.Fatalf("%q writes through a link into the project and must not be admitted (got %v)", cmd, got)
		}
	}
	// The same shapes spelled inside the region stay admitted.
	for _, cmd := range []string{
		"echo hi > " + filepath.Join(root, "x.txt"),
		"mkdir " + filepath.Join(root, "nested"),
	} {
		if got := ShellScratchWriteVerdict(cmd, roots, project); got != ShellScratchWritesClean {
			t.Fatalf("%q writes inside the region and must be admitted (got %v)", cmd, got)
		}
	}
}

// A workspace that lives inside $TMPDIR is still the project: the region never
// outranks it, or plan mode would stop protecting the one thing it protects.
func TestShellScratchWriteVerdictKeepsTheProjectOutOfScratch(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("the OS temp pair is a unix shape")
	}
	t.Setenv("CDPATH", "")
	inside := filepath.Join(os.TempDir(), "fb-387-checkout")
	for _, cmd := range []string{
		"mkdir " + inside + "/x",
		"echo hi > " + inside + "/x.txt",
		"rm -rf " + inside + "/pkg",
		"cd " + inside + " && echo hi > x.txt",
	} {
		if got := ShellScratchWriteVerdict(cmd, []string{os.TempDir(), "/tmp"}, inside); got == ShellScratchWritesClean {
			t.Fatalf("%q writes the project and must not be admitted (got %v)", cmd, got)
		}
	}
	// The regions the project does not sit in keep working.
	if got := ShellScratchWriteVerdict("mkdir /tmp/fb-387-x", []string{os.TempDir(), "/tmp"}, inside); got != ShellScratchWritesClean {
		t.Fatalf("a region beside the project must stay writable (got %v)", got)
	}
}

// The proof admits a command from the classifier's read-only table with
// unrestricted operands, which is only sound while every name in that table
// writes nothing whatever its arguments. A name the classifier dispatches by
// argument (find, git, sed, sort...) is not one of those, and adding it to the
// table would open the wall silently - so this pins the invariant instead.
func TestShellReadOnlyTableAdmitsNoArgumentAwareCommand(t *testing.T) {
	t.Setenv("CDPATH", "")
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
func TestShellScratchWriteVerdictResolvesDotDotThroughLinks(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("symlink layout is unix-specific here")
	}
	t.Setenv("CDPATH", "")
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
		if got := ShellScratchWriteVerdict(cmd, roots, project); got == ShellScratchWritesClean {
			t.Fatalf("%q steps through a link with .. and writes the project; it must not be admitted (got %v)", cmd, got)
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
		if got := ShellScratchWriteVerdict(cmd, roots, project); got != ShellScratchWritesClean {
			t.Fatalf("%q writes inside the region and must be admitted (got %v)", cmd, got)
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
