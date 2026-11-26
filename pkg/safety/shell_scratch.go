package safety

import (
	"os"
	"path/filepath"
	"strings"
)

// Plan-mode scratch: what a session that is still planning may write.
//
// Plan mode's promise is that the project stays read-only until the user
// approves the plan, and that promise is the owner's plan-gated workflow. It
// was never a promise about the whole disk: disposable research - download a
// package, unpack it, read it back, write a scratch note - is investigation, not
// implementation. Refusing it made the agent improvise around the wall instead
// of investigating (owner ruling 2026-11-26: cut the restrictions that buy
// nothing, execution efficiency and result quality first).
//
// Two files own the policy: this one defines the disposable regions and proves
// that a command's writes land inside them; the seams that enforce it (the shell
// gate, the read-only sandbox profile's write exceptions, GuardWrite) consult
// them instead of describing them again.
//
// This proof is deliberately not a tier of ClassifyShellCommand. That function
// answers "does this line write at all", a three-state answer about structure;
// this one answers "does everything it writes land in the scratch regions", a
// question about named targets, and it has only two answers: confined, or not
// read.

// ShellScratchRoots returns the OS regions a planning session may write: its own
// temp directory and, on the platforms that have one, /tmp. The workspace-write
// sandbox already grants this pair by definition (plan 206), so the two seams
// read one list rather than two descriptions of the same idea.
func ShellScratchRoots() []string {
	roots := make([]string, 0, 2)
	if dir := strings.TrimSpace(os.TempDir()); dir != "" {
		roots = append(roots, filepath.Clean(dir))
	}
	if filepath.Separator == '/' {
		// Mirrors FileSystemSpecialPathSlashTmp.Resolve: the path exists and it
		// is a directory, or there is no such region on this host.
		if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
			roots = append(roots, "/tmp")
		}
	}
	seen := make(map[string]struct{}, len(roots))
	out := roots[:0]
	for _, root := range roots {
		if _, dup := seen[root]; dup {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	return out
}

// ShellWritesConfinedToRoots reports whether every file this command line writes
// lands strictly inside one of roots.
//
// The answer is false whenever the proof cannot read a write, which is most of
// the time: expansions, subshells, heredocs, an unknown command, an unknown flag
// or an operand the whitelist below does not describe all end it. That is the
// point - "cannot prove" must not become "may write".

func ShellWritesConfinedToRoots(cmd string, roots []string, cwd string) bool {
	cmd = stripDevNullRedirects(cmd)
	if strings.TrimSpace(cmd) == "" {
		return false
	}
	roots = cleanShellAllowedRoots(roots)
	if len(roots) == 0 {
		return false
	}
	// A region that contains the directory the command starts in cannot be
	// scratch for it: the project outranks the region. A checkout under $TMPDIR -
	// a test fixture, a throwaway clone - is still the project, and plan mode's
	// wall is around the project rather than around a directory name.
	roots = shellScratchRootsOutside(cwd, roots)
	if len(roots) == 0 {
		return false
	}
	clauses, _, doubt := splitShellClassifyClauses(cmd)
	if len(clauses) == 0 || doubt {
		// A redirection floors at Writes and is bounded through its recorded
		// target below. The structural doubt - an expansion, a subshell, a
		// brace, a word-start ~, an unterminated quote - is what the floor
		// cannot carry once a redirect outranks it, and every one of those can
		// hide a write this walker never saw.
		return false
	}
	dir := strings.TrimSpace(cwd)
	for _, clause := range clauses {
		if clause.Heredoc {
			// A heredoc body reads as ordinary clauses to this walker, so the
			// proof cannot tell what fed the command from what the command is.
			return false
		}
		if !shellScratchRedirectsConfined(clause, dir, roots) {
			return false
		}
		// A redirection's target word is still in the clause text, because that
		// text is what the classifier reads. The operand rules that pick a
		// destination out of position therefore run their stricter form when the
		// clause has one: which word the operator took is not recoverable from
		// the flattened text, and a guess there is exactly the kind of guess this
		// file must not make.
		name, args, ok := shellScratchCommandWords(SplitShellWords(clause.Text))
		if !ok {
			return false
		}
		if name == "cd" {
			// cd writes nothing: it only decides where the next clause's
			// relative target lands, so the proof follows it there.
			next, ok := shellScratchCDTarget(args, dir)
			if !ok {
				return false
			}
			dir = next
			continue
		}
		if !shellScratchClauseConfined(name, args, dir, roots, len(clause.Redirects) > 0) {
			return false
		}
	}
	return true
}

// shellScratchRedirectsConfined bounds the files a clause's redirections name:
// each one strictly inside the scratch regions. A target the walker could not
// read is recorded empty and fails here.
func shellScratchRedirectsConfined(clause shellClause, dir string, roots []string) bool {
	for _, raw := range clause.Redirects {
		words := SplitShellWords(raw)
		if len(words) != 1 {
			return false
		}
		abs, ok := shellScratchAbsPath(words[0], dir)
		if !ok || !shellScratchFileConfined(abs, roots) {
			return false
		}
	}
	return true
}

// shellScratchCommandWords returns the command name and its arguments, skipping
// the environment assignments a clause may carry in front of it.
func shellScratchCommandWords(words []string) (string, []string, bool) {
	i := 0
	for i < len(words) && shellEnvAssignment(words[i]) {
		i++
	}
	if i >= len(words) {
		return "", nil, false
	}
	name := strings.ToLower(strings.TrimSpace(words[i]))
	if idx := strings.LastIndexAny(name, `/\`); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" {
		return "", nil, false
	}
	return name, words[i+1:], true
}

// shellScratchCDTarget follows a cd clause: one plain path operand, and none of
// the forms whose destination is not in the text (no operand at all, "-", a
// flag, a home expansion).
func shellScratchCDTarget(args []string, dir string) (string, bool) {
	if len(args) != 1 {
		return "", false
	}
	target := strings.TrimSpace(args[0])
	if target == "" || target == "-" || strings.HasPrefix(target, "-") || strings.HasPrefix(target, "~") {
		return "", false
	}
	return shellScratchAbsPath(target, dir)
}

// shellScratchClauseConfined decides one clause: a command the classifier proves
// read-only passes whatever its operands (reads are not what this proof bounds),
// a command in the table below passes only if its own operand rules say every
// write lands in scratch, and anything else fails.
func shellScratchClauseConfined(name string, args []string, dir string, roots []string, redirected bool) bool {
	if name == "git" {
		return shellScratchGitCloneConfined(args, dir, roots)
	}
	if table, ok := shellScratchCommands[name]; ok {
		parsed, ok := scratchParseArgs(args, table)
		if !ok {
			return false
		}
		return shellScratchOperandsConfined(name, parsed, dir, roots, redirected)
	}
	// The read-only table is the classifier's own, not a copy: these commands
	// write nothing whatever their operands. The invariant test beside this file
	// pins that none of them is a name the classifier dispatches by argument.
	if shellScratchReadOnlyCommand(name) {
		return true
	}
	return false
}

// shellScratchOperandsConfined applies one table entry's operand rules.
// redirected reports that the clause also wrote a file through a redirection,
// which makes the destination-picking rules below read every operand instead.
func shellScratchOperandsConfined(name string, parsed scratchArgs, dir string, roots []string, redirected bool) bool {
	switch name {
	case "rm", "rmdir", "mkdir", "touch", "unlink", "shred", "truncate", "ln", "tee":
		return shellScratchPathsConfined(parsed.operands, dir, roots)
	case "mv":
		// Every operand is written: the source loses the entry, the target
		// gains it.
		return shellScratchPathsConfined(append(parsed.value("-t", "--target-directory"), parsed.operands...), dir, roots)
	case "chmod", "chown", "chgrp":
		// The first operand is the mode or the owner, the rest are targets.
		if len(parsed.operands) < 2 {
			return false
		}
		return shellScratchPathsConfined(parsed.operands[1:], dir, roots)
	case "cp", "install":
		// Sources are reads; only where the copy lands is bounded. The
		// destination is the last operand - unless a redirection also named a
		// file, because then the last operand may be that file rather than the
		// destination, and every operand has to be inside the regions instead.
		if targets := parsed.value("-t", "--target-directory"); len(targets) > 0 {
			if redirected && !shellScratchPathsConfined(parsed.operands, dir, roots) {
				return false
			}
			return shellScratchPathsConfined(targets, dir, roots)
		}
		if len(parsed.operands) < 2 {
			return false
		}
		if redirected {
			return shellScratchPathsConfined(parsed.operands, dir, roots)
		}
		return shellScratchPathsConfined(parsed.operands[len(parsed.operands)-1:], dir, roots)
	case "dd":
		targets := make([]string, 0, 1)
		for _, arg := range parsed.operands {
			switch {
			case strings.HasPrefix(arg, "of="):
				targets = append(targets, strings.TrimPrefix(arg, "of="))
			case strings.HasPrefix(arg, "if="):
				// The input is a read.
			case strings.Contains(arg, "="):
			default:
				// A bare operand is a form this proof cannot place.
				return false
			}
		}
		if len(targets) == 0 {
			// No of=: dd copies to stdout.
			return true
		}
		return shellScratchPathsConfined(targets, dir, roots)
	case "curl":
		// -o/--output names the file; every other option that writes a file is
		// absent from the table (--output-dir, -c/--cookie-jar,
		// -D/--dump-header, --etag-save, --hsts, --alt-svc, --stderr,
		// --trace, --trace-ascii, -K/--config, --libcurl), so an unknown flag
		// fails this clause instead of being waved through.
		outputs := parsed.value("-o", "--output")
		if len(outputs) > 0 && !shellScratchPathsConfined(outputs, dir, roots) {
			return false
		}
		if parsed.any("-O", "--remote-name") && !shellScratchCWDConfined(dir, roots) {
			// -O names the saved file after the URL, in the working directory.
			return false
		}
		return true
	case "wget":
		documents := parsed.value("-O", "--output-document")
		if len(documents) > 0 && !shellScratchPathsConfined(documents, dir, roots) {
			return false
		}
		if prefixes := parsed.value("-P", "--directory-prefix"); len(prefixes) > 0 && !shellScratchDirsConfined(prefixes, dir, roots) {
			return false
		}
		if logs := parsed.value("-o", "--output-file"); len(logs) > 0 && !shellScratchPathsConfined(logs, dir, roots) {
			return false
		}
		if len(documents) == 0 && !shellScratchCWDConfined(dir, roots) {
			// Without -O wget saves into the working directory.
			return false
		}
		return true
	case "unzip":
		if dirs := parsed.value("-d"); len(dirs) > 0 {
			return shellScratchDirsConfined(dirs, dir, roots)
		}
		// Without -d, members land in the working directory.
		return shellScratchCWDConfined(dir, roots)
	case "tar":
		creates := parsed.any("-c", "--create", "-r", "--append", "-u", "--update")
		reads := parsed.any("-x", "--extract", "-t", "--list")
		if creates == reads {
			// No mode, or two of them: the archive's role is not read.
			return false
		}
		if dirs := parsed.value("-C", "--directory"); len(dirs) > 0 {
			if !shellScratchDirsConfined(dirs, dir, roots) {
				return false
			}
		} else if !shellScratchCWDConfined(dir, roots) {
			return false
		}
		if creates {
			// Writing an archive is a write; reading one only reads it.
			return shellScratchPathsConfined(parsed.value("-f", "--file"), dir, roots)
		}
		return true
	}
	return false
}

// shellScratchGitCloneConfined reads the one git form whose writes the command
// line names: clone, whose destination is its last operand or - given none - a
// directory derived from the URL inside the working directory. Every other
// subcommand writes into a repository this proof cannot see.
func shellScratchGitCloneConfined(args []string, dir string, roots []string) bool {
	if len(args) == 0 || !strings.EqualFold(strings.TrimSpace(args[0]), "clone") {
		return false
	}
	parsed, ok := scratchParseArgs(args[1:], shellScratchGitCloneFlags)
	if !ok {
		return false
	}
	switch len(parsed.operands) {
	case 0:
		return false
	case 1:
		return shellScratchCWDConfined(dir, roots)
	}
	return shellScratchPathsConfined(parsed.operands[len(parsed.operands)-1:], dir, roots)
}

// shellScratchReadOnlyCommand reports whether the classifier's read-only table
// proves this name writes nothing, whatever its operands.
func shellScratchReadOnlyCommand(name string) bool {
	_, ok := shellReadOnlyCommands[name]
	return ok
}

// shellScratchPathsConfined bounds a list of written files: each one strictly
// inside a scratch root, never the root itself.
func shellScratchPathsConfined(paths []string, dir string, roots []string) bool {
	if len(paths) == 0 {
		return false
	}
	for _, path := range paths {
		abs, ok := shellScratchAbsPath(path, dir)
		if !ok || !shellScratchFileConfined(abs, roots) {
			return false
		}
	}
	return true
}

// shellScratchDirsConfined bounds a list of written directories: a root itself is
// inside the region, because writing "into" a directory writes its members.
func shellScratchDirsConfined(paths []string, dir string, roots []string) bool {
	if len(paths) == 0 {
		return false
	}
	for _, path := range paths {
		abs, ok := shellScratchAbsPath(path, dir)
		if !ok || !shellScratchDirConfined(abs, roots) {
			return false
		}
	}
	return true
}

// shellScratchCWDConfined bounds a command that names no target and writes into
// the directory it runs in. The directory goes through the same walk as a
// written path: a tracked cwd reached through a link is the link's target.
func shellScratchCWDConfined(dir string, roots []string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" || !filepath.IsAbs(dir) {
		return false
	}
	resolved, ok := shellScratchWalkPath(dir, "")
	if !ok {
		return false
	}
	return shellScratchDirConfined(resolved, roots)
}

// shellScratchRootsOutside drops the roots that hold dir, so nothing dir
// contains is ever admitted as scratch.
func shellScratchRootsOutside(dir string, roots []string) []string {
	dir = strings.TrimSpace(dir)
	if dir == "" || !filepath.IsAbs(dir) {
		return roots
	}
	project, ok := shellScratchWalkPath(dir, "")
	if !ok {
		return roots
	}
	kept := make([]string, 0, len(roots))
	for _, root := range roots {
		if project == root || strings.HasPrefix(project, root+string(filepath.Separator)) {
			continue
		}
		kept = append(kept, root)
	}
	return kept
}

// shellScratchAbsPath resolves a written target against the working directory the
// walk has tracked so far, in the order the command writes it.
//
// filepath.Clean must not appear anywhere on this path. "<link>/../x" collapses
// lexically to "<root>/x", while the kernel resolves <link> and then steps to the
// *target's* parent - so a cleaned path proves containment for a file the write
// never lands in, and `rm -rf <link>/../f` deletes a project file the proof
// called scratch. A link inside the regions needs no hand of the agent's: an
// admitted `tar`/`unzip`/`git clone` into scratch can plant one.
func shellScratchAbsPath(target, dir string) (string, bool) {
	return shellScratchWalkPath(target, dir)
}

// shellScratchWalkPath is shellScratchAbsPath's one implementation: a
// component-by-component walk. The base is the resolved target's directory (the
// tracked cwd for a relative target, "/" for an absolute one), each component is
// joined and then resolved when it exists, and ".." steps up from the base as
// resolved rather than from the spelling. A component that does not exist is
// joined as written: the leaf of a write is usually the file about to be
// created, and requiring it would refuse every new file.
func shellScratchWalkPath(target, dir string) (string, bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", false
	}
	var base string
	if filepath.IsAbs(target) {
		base = string(filepath.Separator)
		if volume := filepath.VolumeName(target); volume != "" {
			base = volume + string(filepath.Separator)
		}
		target = strings.TrimPrefix(target, base)
	} else {
		dir = strings.TrimSpace(dir)
		if dir == "" || !filepath.IsAbs(dir) {
			return "", false
		}
		resolved, ok := shellScratchWalkPath(dir, "")
		if !ok {
			return "", false
		}
		base = resolved
	}
	for _, component := range strings.Split(target, string(filepath.Separator)) {
		switch component {
		case "", ".":
		case "..":
			base = filepath.Dir(base)
		default:
			base = filepath.Join(base, component)
			if resolved, err := filepath.EvalSymlinks(base); err == nil {
				base = resolved
			}
		}
	}
	return base, true
}

// shellScratchFileConfined reports whether writing path stays inside the scratch
// regions.
func shellScratchFileConfined(path string, roots []string) bool {
	under, _ := shellScratchContainment(path, roots)
	return under
}

// shellScratchDirConfined reports whether path is a scratch directory.
func shellScratchDirConfined(path string, roots []string) bool {
	under, at := shellScratchContainment(path, roots)
	return under || at
}

// shellScratchContainment places an already-resolved path against the roots, in
// the location it really names: the probe pathUnderAnyShellRoot uses, used as the
// only form, because a spelling that merely starts with a root's name is not a
// file in it - a link inside /tmp that points at the project resolves out. The
// root itself is reported apart from what lies under it: removing a root is not
// confined research. roots arrive cleaned by cleanShellAllowedRoots, which also
// lists each one's resolved form.
//
// Every caller reaches this through shellScratchWalkPath (a written path) or
// shellScratchCWDConfined (a directory), so the input carries no ".." left to
// collapse: nothing here cleans a path before resolving it.
func shellScratchContainment(path string, roots []string) (under, at bool) {
	form := resolveShellPath(filepath.Clean(path))
	for _, root := range roots {
		root = filepath.Clean(root)
		switch {
		case form == root:
			at = true
		case strings.HasPrefix(form, root+string(filepath.Separator)):
			under = true
		}
	}
	return under, at
}

// scratchFlagTable lists the flags one command form may carry. A flag absent from
// both halves is refused: an unread flag can decide a write, and a proof that
// guesses is worse than none.
type scratchFlagTable struct {
	booleans map[string]bool
	values   map[string]bool
}

// scratchFlags builds one table row from two space-separated flag lists.
func scratchFlags(booleans, values string) scratchFlagTable {
	table := scratchFlagTable{booleans: map[string]bool{}, values: map[string]bool{}}
	for _, flag := range strings.Fields(booleans) {
		table.booleans[flag] = true
	}
	for _, flag := range strings.Fields(values) {
		table.values[flag] = true
	}
	return table
}

// scratchArgs is one clause's arguments once the table has read them.
type scratchArgs struct {
	// values holds each flag's value; a boolean flag's value is "".
	values map[string][]string
	// operands holds the arguments that are neither flags nor flag values.
	operands []string
}

// value returns the values the named flags carried, in flag order.
func (a scratchArgs) value(flags ...string) []string {
	out := make([]string, 0, len(flags))
	for _, flag := range flags {
		out = append(out, a.values[flag]...)
	}
	return out
}

// any reports whether any of the named flags appeared.
func (a scratchArgs) any(flags ...string) bool {
	for _, flag := range flags {
		if len(a.values[flag]) > 0 {
			return true
		}
	}
	return false
}

// scratchParseArgs reads args through the table: long flags with "=" or a
// following value, short flags in clusters ("-rf"), and "-o<value>". "--" ends
// the flags.
func scratchParseArgs(args []string, table scratchFlagTable) (scratchArgs, bool) {
	out := scratchArgs{values: map[string][]string{}}
	endOfFlags := false
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if endOfFlags || arg == "" || arg == "-" || !strings.HasPrefix(arg, "-") {
			out.operands = append(out.operands, arg)
			continue
		}
		if arg == "--" {
			endOfFlags = true
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, value, attached := strings.Cut(arg, "=")
			switch {
			case table.values[name]:
				if !attached {
					if i+1 >= len(args) {
						return scratchArgs{}, false
					}
					i++
					value = args[i]
				}
				out.values[name] = append(out.values[name], value)
			case table.booleans[name] && !attached:
				out.values[name] = append(out.values[name], "")
			default:
				return scratchArgs{}, false
			}
			continue
		}
		runes := []rune(strings.TrimPrefix(arg, "-"))
		for j := 0; j < len(runes); j++ {
			flag := "-" + string(runes[j])
			switch {
			case table.booleans[flag]:
				out.values[flag] = append(out.values[flag], "")
			case table.values[flag]:
				if rest := string(runes[j+1:]); rest != "" {
					out.values[flag] = append(out.values[flag], rest)
				} else {
					if i+1 >= len(args) {
						return scratchArgs{}, false
					}
					i++
					out.values[flag] = append(out.values[flag], args[i])
				}
				j = len(runes)
			default:
				return scratchArgs{}, false
			}
		}
	}
	return out, true
}

// shellScratchCommands is the one table of command forms this proof reads, with
// the flags each may carry. A form absent from it is refused. New scratch-safe
// forms are added here and nowhere else.
var shellScratchCommands = map[string]scratchFlagTable{
	"rm":       scratchFlags("-r -R -f -i -v -d -P -I --recursive --force --interactive --verbose --one-file-system", ""),
	"rmdir":    scratchFlags("-p -v --parents --verbose --ignore-fail-on-non-empty", ""),
	"mkdir":    scratchFlags("-p -v --parents --verbose", "-m --mode"),
	"touch":    scratchFlags("-a -c -m -h --no-create --no-dereference", "-t -d -r --time --date --reference"),
	"unlink":   scratchFlags("", ""),
	"shred":    scratchFlags("-f -u -v -z --force --remove --verbose --zero", "-n -s --iterations --size"),
	"truncate": scratchFlags("-c -o --no-create --io-blocks", "-r -s --reference --size"),
	"ln":       scratchFlags("-s -f -n -v -i -r -T -P -d --symbolic --force --verbose --relative --no-target-directory --no-dereference", "-t --target-directory"),
	"mv":       scratchFlags("-f -i -n -v -u --force --interactive --no-clobber --verbose --update", "-t --target-directory"),
	"cp":       scratchFlags("-r -R -a -l -s -f -i -n -p -v -u -d -b -L -P --recursive --archive --force --interactive --no-clobber --preserve --verbose --link --symbolic-link --backup", "-t --target-directory -S --suffix"),
	"install":  scratchFlags("-d -D -v -p -b -c -s --directory --verbose --preserve-timestamps --backup", "-m -o -g -t -S -T --mode --owner --group --target-directory --suffix --strip-program"),
	"dd":       scratchFlags("--help --version", ""),
	"tee":      scratchFlags("-a -i -p --append --ignore-interrupts", ""),
	"chmod":    scratchFlags("-R -v -c -f -h --recursive --verbose --changes --silent --quiet --no-dereference", ""),
	"chown":    scratchFlags("-R -v -c -f -h --recursive --verbose --changes --silent --quiet --no-dereference --preserve-root", ""),
	"chgrp":    scratchFlags("-R -v -c -f -h --recursive --verbose --changes --silent --quiet --no-dereference --preserve-root", ""),
	"curl": scratchFlags(
		"-s -S -f -L -k -I -i -v -g -q -N -O -4 -6 -j -# --silent --show-error --fail --fail-with-body --location --insecure --head --include --verbose --globoff --compressed --http1.1 --http2 --no-progress-meter --remote-name --no-buffer",
		"-o --output -H --header -d --data --data-raw --data-binary --data-urlencode --json -X --request -A --user-agent -e --referer -u --user -x --proxy --url --max-time --connect-timeout --retry --retry-delay --retry-max-time -w --write-out -b --cookie --cacert --capath --cert --key --interface --resolve --limit-rate -T --upload-file --max-filesize",
	),
	"wget": scratchFlags(
		"-q -c -N -r -nH -nv -S -4 -6 --no-clobber --continue --quiet --no-verbose --server-response --no-host-directories --timestamping",
		"-O --output-document -P --directory-prefix -o --output-file --tries --timeout --header --user-agent",
	),
	"unzip": scratchFlags("-q -o -j -n -X -v -l -t -p", "-d -x -P -I -O"),
	"tar": scratchFlags(
		"-c -r -u -x -t -z -j -J -a -v -p -k -m --create --append --update --extract --list --get --gzip --bzip2 --xz --zstd --verbose --preserve-permissions --keep-old-files",
		"-C --directory -f --file --strip-components -T --files-from --exclude",
	),
}

// shellScratchGitCloneFlags are the clone flags this proof reads; anything else
// (--mirror, --recurse-submodules, -j) is a form it does not.
var shellScratchGitCloneFlags = scratchFlags(
	"--single-branch --bare -q --quiet -v --verbose -n --no-checkout",
	"-b --branch --depth -c --config --template -o --origin --reference",
)
