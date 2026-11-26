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
// where a command's writes land; the seams that enforce it (the shell gate, the
// read-only sandbox profile's write exceptions, GuardWrite) consult them instead
// of describing them again.
//
// This proof is deliberately not a tier of ClassifyShellCommand. That function
// answers "does this line write at all", a three-state answer about structure;
// this one answers "does everything it writes land in the scratch regions", a
// question about named targets, and it answers it one clause at a time.

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

// ShellScratchWriteState is what the scratch proof proved about one command
// line.
//
// The proof answers "does everything this line writes land in the scratch
// regions", and it answers it with the four states such a question really has -
// not with a boolean, which forced every unreadable clause to look like a proven
// project write. The line's answer is its worst clause's, and the gate turns one
// state into one behaviour, so nothing else has to guess what "not confined"
// meant.
type ShellScratchWriteState int

const (
	// ShellScratchWritesClean means every clause was read and everything it
	// writes stays inside the regions.
	ShellScratchWritesClean ShellScratchWriteState = iota
	// ShellScratchWritesUnread means nothing is proven to write, but at least
	// one clause is a command the classifier cannot read: a question for the
	// operator, never a refusal.
	ShellScratchWritesUnread
	// ShellScratchWritesOutside means a write is proven to land outside every
	// region.
	ShellScratchWritesOutside
	// ShellScratchWritesUnplaceable means a write exists whose target this
	// proof cannot read: a grouping token, a heredoc body it could not skip, an
	// unknown directory, a non-literal path, a substitution that writes.
	ShellScratchWritesUnplaceable
)

// String names the state for a failure message.
func (s ShellScratchWriteState) String() string {
	switch s {
	case ShellScratchWritesClean:
		return "clean"
	case ShellScratchWritesUnread:
		return "unread"
	case ShellScratchWritesOutside:
		return "outside"
	}
	return "unplaceable"
}

// worseShellScratchWrite is the line's answer from two clauses' answers.
func worseShellScratchWrite(a, b ShellScratchWriteState) ShellScratchWriteState {
	if a > b {
		return a
	}
	return b
}

// ShellScratchWriteVerdict reports whether every file this command line writes
// lands strictly inside one of roots.
//
// The answer is Unread whenever nothing is proven to write but a clause cannot be
// read, and Unplaceable whenever a write exists whose target the proof cannot
// name: expansions, an unknown command with a redirect, an unknown flag, a
// substitution that writes, a grouping token, a heredoc body it could not skip,
// or a directory it stopped following. That is the point - "cannot prove" must
// not become "may write", and it must not be reported as a proven project write
// either.
func ShellScratchWriteVerdict(cmd string, roots []string, cwd string) ShellScratchWriteState {
	return shellScratchWriteVerdictDepth(cmd, roots, cwd, 0)
}

// shellScratchWriteVerdictDepth is ShellScratchWriteVerdict for a line read out
// of a `sh -c` script, one layer down. Only the outermost line narrows the
// regions against its cwd: a script runs in the directory the clause tracked,
// which is usually inside the regions it may write.
func shellScratchWriteVerdictDepth(cmd string, roots []string, cwd string, depth int) ShellScratchWriteState {
	if depth > shellScriptDepthCap {
		// The proof stopped reading the script, so it proves nothing about it.
		return ShellScratchWritesUnplaceable
	}
	cmd = stripDevNullRedirects(cmd)
	if strings.TrimSpace(cmd) == "" {
		// An empty line runs nothing, so it writes nothing.
		return ShellScratchWritesClean
	}
	if depth == 0 {
		// A region that contains the directory the command starts in cannot be
		// scratch for it: the project outranks the region. A checkout under
		// $TMPDIR - a test fixture, a throwaway clone - is still the project,
		// and plan mode's wall is around the project rather than around a
		// directory name.
		roots = shellScratchRootsOutside(cwd, cleanShellAllowedRoots(roots))
	}
	clauses, _, _ := splitShellClassifyClauses(cmd)
	if len(clauses) == 0 {
		// The walker could not read the line at all - an unterminated quote or
		// escape - which is not proof of anything.
		return ShellScratchWritesUnread
	}
	// CDPATH decides where a relative cd lands, so it is read once for the line
	// rather than per clause: an assignment anywhere on the line moves every
	// relative lookup the shell makes.
	cdpath := strings.TrimSpace(os.Getenv("CDPATH")) != ""
	for _, clause := range clauses {
		cdpath = cdpath || clause.CDPATH
	}
	dir, known := shellScratchStartDir(cwd)
	state := ShellScratchWritesClean
	frames := make([]shellSubshellFrame, 0, 2)
	for _, clause := range clauses {
		for open := 0; open < clause.SubshellOpen; open++ {
			// A "(" opens a subshell, and a cd runs in it: the directory the
			// walk has reached by the matching ")" is not one the next clause
			// runs in, so the pair is saved here and restored at the close.
			frames = append(frames, shellSubshellFrame{dir: dir, known: known})
		}
		next, nextKnown, clauseState := shellScratchClauseWrite(clause, dir, known, cdpath, roots, depth)
		for close := 0; close < clause.SubshellClose; close++ {
			if len(frames) == 0 {
				// A ")" whose "(" this walk never saw: the directory the rest
				// of the line runs in is not in the text.
				next, nextKnown = "", false
				break
			}
			frame := frames[len(frames)-1]
			frames = frames[:len(frames)-1]
			next, nextKnown = frame.dir, frame.known
		}
		dir, known = next, nextKnown
		state = worseShellScratchWrite(state, clauseState)
	}
	return state
}

// shellSubshellFrame is the directory the walk had before a clause opened a
// subshell. bash runs that clause's cd inside the subshell, so the directory it
// reaches ends with the closing token and the next clause starts from the frame.
type shellSubshellFrame struct {
	dir   string
	known bool
}

// shellScratchClauseWrite decides one clause and reports the directory the next
// clause starts in. The rules run in the order this proof needs them, and the
// first one that answers does.
func shellScratchClauseWrite(clause shellClause, dir string, known, cdpath bool, roots []string, depth int) (nextDir string, nextKnown bool, state ShellScratchWriteState) {
	base := dir
	if !known {
		base = ""
	}
	if clause.Unread {
		return dir, known, ShellScratchWritesUnplaceable
	}
	// The source of every substitution runs before this clause does, so it is
	// judged with no allowed roots at all: a write it proves is a write this
	// clause performs somewhere this proof never sees. Read-only and unproven
	// sources are fine - they are not proof of a write.
	for _, source := range clause.Substitutions {
		if classifyShellCommandDepth(source, nil, "", 0) == ShellMutationWrites {
			return dir, known, ShellScratchWritesUnplaceable
		}
	}
	// cd writes nothing: it decides where the next clause's relative target
	// lands, so the proof follows it there. pushd and popd write nothing either
	// and leave a directory this walk cannot follow, which is why they are not
	// followed. Both answer Clean, because neither writes.
	switch clause.Command {
	case "cd":
		target, followed := shellScratchFollowCD(clause.Args, base, known, cdpath)
		return target, followed, ShellScratchWritesClean
	case "pushd", "popd":
		return dir, false, ShellScratchWritesClean
	}
	// What this clause writes is its own command, read from the words the
	// operators did not take - never from the flattened text, whose first word
	// in "> f cmd" is the target - plus its redirects.
	tier := classifyCommandDepth(clause.Command, clause.Args, roots, base, 0)
	writes := len(clause.Redirects) > 0 || shellScratchWritesByName(clause.Command) || tier == ShellMutationWrites
	script, hasScript := shellScriptFromClause(clause.Command, clause.Args)
	// A wrapper runs the command in its own arguments, so the inner command is
	// what this clause runs: `nohup tar -czf /tmp/a.tgz .` is the tar clause,
	// and judging the wrapper's own name would call every wrapped form
	// unreadable. The inner clause answers for itself, -c script included.
	if _, ok := shellWrapperCommands[clause.Command]; ok {
		if rest := stripWrapperArgs(clause.Command, clause.Args); len(rest) > 0 {
			inner := shellClause{
				Command:  strings.ToLower(filepath.Base(rest[0])),
				Args:     rest[1:],
				Grouping: clause.Grouping,
				Heredoc:  clause.Heredoc,
			}
			outer := ShellScratchWritesClean
			if len(clause.Redirects) > 0 {
				// The wrapper's own redirect is this clause's write; the inner
				// command cannot see it.
				outer = shellScratchWrittenTargets(clause, base, known, roots)
			}
			_, _, innerState := shellScratchClauseWrite(inner, base, known, cdpath, roots, depth+1)
			return dir, known, worseShellScratchWrite(outer, innerState)
		}
	}
	if !writes && !hasScript {
		// Nothing this clause names is written, so what is left to decide is
		// whether it was read at all.
		if clause.Command == "" || shellScratchReadOnlyCommand(clause.Command) {
			// A bare assignment writes nothing; the read-only table is the
			// classifier's own, not a copy.
			return dir, known, ShellScratchWritesClean
		}
		return dir, known, ShellScratchWritesUnread
	}
	state = ShellScratchWritesClean
	if writes {
		state = shellScratchWrittenTargets(clause, base, known, roots)
	}
	if hasScript {
		// The clause's own redirect is placed above, before the script gets a
		// say: "> pkg/tool/x sh -c 'true'" writes the project whatever the
		// script does, while "sh -c 'echo hi > /tmp/x'" writes only scratch.
		return dir, known, worseShellScratchWrite(state, shellScratchWriteVerdictDepth(script, roots, base, depth+1))
	}
	if state != ShellScratchWritesClean {
		return dir, known, state
	}
	if strings.ContainsAny(clause.Command, "$`") {
		// The name itself is decided while the line runs.
		return dir, known, ShellScratchWritesUnplaceable
	}
	if clause.Command == "git" {
		// The one git form whose writes the command line names is clone; every
		// other subcommand writes into a repository this proof cannot see.
		if shellScratchGitCloneConfined(clause.Args, base, roots) {
			return dir, known, ShellScratchWritesClean
		}
		return dir, known, ShellScratchWritesOutside
	}
	if table, ok := shellScratchCommands[clause.Command]; ok {
		parsed, ok := scratchParseArgs(clause.Args, table)
		if !ok {
			return dir, known, ShellScratchWritesUnplaceable
		}
		return dir, known, shellScratchOperandState(clause.Command, parsed, base, roots, len(clause.Redirects) > 0)
	}
	if shellScratchReadOnlyCommand(clause.Command) {
		return dir, known, ShellScratchWritesClean
	}
	switch tier {
	case ShellMutationReadOnly:
		return dir, known, ShellScratchWritesClean
	case ShellMutationWrites:
		return dir, known, ShellScratchWritesUnplaceable
	}
	if len(clause.Redirects) > 0 {
		// A command the proof cannot read, writing a file it did name: the
		// target is bounded above, the command is not.
		return dir, known, ShellScratchWritesUnplaceable
	}
	return dir, known, ShellScratchWritesUnread
}

// shellScratchWritesByName reports whether the command's own name proves a write
// whatever its arguments. It is the scratch table rather than the classifier's
// mutating table because forms such as curl, unzip and tar write only through
// the operands this proof reads next.
func shellScratchWritesByName(name string) bool {
	_, ok := shellScratchCommands[name]
	return ok
}

// shellScratchWrittenTargets bounds where a clause's own operators make it
// write: the grouping it sits in, a heredoc body the proof could not skip, and
// the directory it resolves against must all be readable, and every redirect
// target must be a literal path inside the regions. A target the walker could
// not read is recorded empty and fails here, which is the whole point: the line
// that named no file must not read as one that writes nothing.
func shellScratchWrittenTargets(clause shellClause, dir string, known bool, roots []string) ShellScratchWriteState {
	if clause.Grouping || clause.Heredoc || !known {
		// Where this clause writes is decided by something the proof cannot
		// read: a grouping token whose cd it cannot scope, a body it did not
		// skip, or a directory it stopped following.
		return ShellScratchWritesUnplaceable
	}
	state := ShellScratchWritesClean
	for _, raw := range clause.Redirects {
		state = worseShellScratchWrite(state, shellScratchTargetState(raw, dir, roots))
	}
	return state
}

// shellScratchTargetState places one written target: a literal word, resolved
// through the walk, strictly inside a region.
func shellScratchTargetState(raw, dir string, roots []string) ShellScratchWriteState {
	words := SplitShellWords(raw)
	if len(words) != 1 {
		return ShellScratchWritesUnplaceable
	}
	if !shellScratchLiteralTarget(words[0]) {
		// An expansion decides the path while the line runs.
		return ShellScratchWritesUnplaceable
	}
	abs, ok := shellScratchAbsPath(words[0], dir)
	if !ok {
		return ShellScratchWritesUnplaceable
	}
	if !shellScratchFileConfined(abs, roots) {
		return ShellScratchWritesOutside
	}
	return ShellScratchWritesClean
}

// shellScratchLiteralTarget reports whether a written path is the path in the
// text. "~", a glob, a parameter and a substitution all give the shell a name
// this proof cannot resolve, so none of them may reach a comparison against a
// region. eval reads its operand through this same test: the characters that
// make a path unreadable are the ones that make a script unreadable.
func shellScratchLiteralTarget(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	return !strings.ContainsAny(path, "$`~*?[{")
}

// shellScratchStartDir is the directory the walk begins in, and whether it is
// known at all: a caller with no cwd (the approval boundary) knows none.
func shellScratchStartDir(cwd string) (string, bool) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" || !filepath.IsAbs(cwd) {
		return "", false
	}
	return cwd, true
}

// shellScratchFollowCD follows a cd clause: one plain path operand, and none of
// the forms whose destination is not in the text (no operand at all, "-", a
// flag, an expansion, pushd/popd).
//
// A relative operand is resolved from the tracked directory - unless CDPATH is
// in play, in which case bash searches it first and the directory the shell lands
// in is not the one this walk followed.
func shellScratchFollowCD(args []string, dir string, known, cdpath bool) (string, bool) {
	if len(args) != 1 {
		return dir, false
	}
	target := strings.TrimSpace(args[0])
	if target == "" || target == "-" || strings.HasPrefix(target, "-") || !shellScratchLiteralTarget(target) {
		return dir, false
	}
	base := dir
	if !known {
		base = ""
	}
	if !filepath.IsAbs(target) && (cdpath || base == "") {
		return dir, false
	}
	next, ok := shellScratchAbsPath(target, base)
	if !ok {
		return dir, false
	}
	return next, true
}

// shellScratchOperandsState applies one table entry's operand rules.
// redirected reports that the clause also wrote a file through a redirection,
// which makes the destination-picking rules below read every operand instead.
func shellScratchOperandState(name string, parsed scratchArgs, dir string, roots []string, redirected bool) ShellScratchWriteState {
	// A flag's value can name a file the rules below do not look at, so an
	// expanded one leaves the write unplaceable: "curl -o $X" is the same write
	// as "-o /tmp/x" only when X is known.
	for _, values := range parsed.values {
		for _, value := range values {
			if value == "" {
				// A boolean flag's value is empty by construction.
				continue
			}
			if !shellScratchLiteralTarget(value) {
				return ShellScratchWritesUnplaceable
			}
		}
	}
	switch name {
	case "rm", "rmdir", "mkdir", "touch", "unlink", "shred", "truncate", "tee":
		return shellScratchPathsState(parsed.operands, dir, roots)
	case "ln", "mv":
		// Every operand is written, and so is the directory -t names: an
		// operand-picking rule that ignored -t admitted `ln -t pkg/tool /tmp/x`
		// while mv and cp honoured theirs.
		return shellScratchPathsState(append(parsed.value("-t", "--target-directory"), parsed.operands...), dir, roots)
	case "chmod", "chown", "chgrp":
		// The first operand is the mode or the owner, the rest are targets.
		if len(parsed.operands) < 2 {
			return ShellScratchWritesUnplaceable
		}
		return shellScratchPathsState(parsed.operands[1:], dir, roots)
	case "cp", "install":
		// Sources are reads; only where the copy lands is bounded. The
		// destination is the last operand - unless a redirection also named a
		// file, because then the last operand may be that file rather than the
		// destination, and every operand has to be inside the regions instead.
		if targets := parsed.value("-t", "--target-directory"); len(targets) > 0 {
			if redirected {
				return shellScratchPathsState(append(targets, parsed.operands...), dir, roots)
			}
			return shellScratchPathsState(targets, dir, roots)
		}
		if len(parsed.operands) < 2 {
			return ShellScratchWritesUnplaceable
		}
		if redirected {
			return shellScratchPathsState(parsed.operands, dir, roots)
		}
		return shellScratchPathsState(parsed.operands[len(parsed.operands)-1:], dir, roots)
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
				return ShellScratchWritesUnplaceable
			}
		}
		if len(targets) == 0 {
			// No of=: dd copies to stdout.
			return ShellScratchWritesClean
		}
		return shellScratchPathsState(targets, dir, roots)
	case "curl":
		// -o/--output names the file; every other option that writes a file is
		// absent from the table (--output-dir, -c/--cookie-jar,
		// -D/--dump-header, --etag-save, --hsts, --alt-svc, --stderr,
		// --trace, --trace-ascii, -K/--config, --libcurl), so an unknown flag
		// fails this clause instead of being waved through.
		state := ShellScratchWritesClean
		if outputs := parsed.value("-o", "--output"); len(outputs) > 0 {
			state = worseShellScratchWrite(state, shellScratchPathsState(outputs, dir, roots))
		}
		if parsed.any("-O", "--remote-name") {
			// -O names the saved file after the URL, in the working directory.
			state = worseShellScratchWrite(state, shellScratchCWDState(dir, roots))
		}
		return state
	case "wget":
		documents := parsed.value("-O", "--output-document")
		state := ShellScratchWritesClean
		if len(documents) > 0 {
			state = worseShellScratchWrite(state, shellScratchPathsState(documents, dir, roots))
		}
		if prefixes := parsed.value("-P", "--directory-prefix"); len(prefixes) > 0 {
			state = worseShellScratchWrite(state, shellScratchDirsState(prefixes, dir, roots))
		}
		if logs := parsed.value("-o", "--output-file"); len(logs) > 0 {
			state = worseShellScratchWrite(state, shellScratchPathsState(logs, dir, roots))
		}
		if len(documents) == 0 {
			// Without -O wget saves into the working directory.
			state = worseShellScratchWrite(state, shellScratchCWDState(dir, roots))
		}
		return state
	case "unzip":
		if dirs := parsed.value("-d"); len(dirs) > 0 {
			return shellScratchDirsState(dirs, dir, roots)
		}
		if parsed.any("-c", "-p", "-l", "-t", "-v") {
			// These modes send what they read to stdout and write nothing to
			// disk, so no directory has to be inside a region.
			return ShellScratchWritesClean
		}
		// Without -d, members land in the working directory.
		return shellScratchCWDState(dir, roots)
	case "tar":
		creates := parsed.any("-c", "--create", "-r", "--append", "-u", "--update")
		reads := parsed.any("-x", "--extract", "-t", "--list")
		if creates == reads {
			// No mode, or two of them: the archive's role is not read.
			return ShellScratchWritesUnplaceable
		}
		if !creates && parsed.any("-t", "--list") {
			// A listing writes nothing anywhere, whatever -C says: -C only moves
			// the directory the members are read from.
			return ShellScratchWritesClean
		}
		state := ShellScratchWritesClean
		if creates {
			// Writing an archive is a write, and -f names it: -C only moves the
			// directory the members are read from, which is a read.
			return shellScratchPathsState(parsed.value("-f", "--file"), dir, roots)
		}
		if dirs := parsed.value("-C", "--directory"); len(dirs) > 0 {
			state = worseShellScratchWrite(state, shellScratchDirsState(dirs, dir, roots))
		} else {
			// Extraction without -C unpacks into the working directory.
			state = worseShellScratchWrite(state, shellScratchCWDState(dir, roots))
		}
		return state
	}
	return ShellScratchWritesUnplaceable
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

// shellScratchPathsState places a list of written files: each one strictly
// inside a scratch root, never the root itself.
func shellScratchPathsState(paths []string, dir string, roots []string) ShellScratchWriteState {
	if len(paths) == 0 {
		return ShellScratchWritesUnplaceable
	}
	state := ShellScratchWritesClean
	for _, path := range paths {
		state = worseShellScratchWrite(state, shellScratchTargetState(path, dir, roots))
	}
	return state
}

// shellScratchDirsState places a list of written directories: a root itself is
// inside the region, because writing "into" a directory writes its members.
func shellScratchDirsState(paths []string, dir string, roots []string) ShellScratchWriteState {
	if len(paths) == 0 {
		return ShellScratchWritesUnplaceable
	}
	state := ShellScratchWritesClean
	for _, path := range paths {
		if !shellScratchLiteralTarget(path) {
			state = worseShellScratchWrite(state, ShellScratchWritesUnplaceable)
			continue
		}
		abs, ok := shellScratchAbsPath(path, dir)
		if !ok {
			state = worseShellScratchWrite(state, ShellScratchWritesUnplaceable)
			continue
		}
		if !shellScratchDirConfined(abs, roots) {
			state = worseShellScratchWrite(state, ShellScratchWritesOutside)
		}
	}
	return state
}

// shellScratchCWDState places a command that names no target and writes into the
// directory it runs in. The directory goes through the same walk as a written
// path: a tracked cwd reached through a link is the link's target.
func shellScratchCWDState(dir string, roots []string) ShellScratchWriteState {
	return shellScratchDirsState([]string{dir}, "", roots)
}

// shellScratchPathsConfined reports whether every written file stays inside the
// regions.
func shellScratchPathsConfined(paths []string, dir string, roots []string) bool {
	return shellScratchPathsState(paths, dir, roots) == ShellScratchWritesClean
}

// shellScratchCWDConfined reports whether the directory a command runs in is
// inside the regions.
func shellScratchCWDConfined(dir string, roots []string) bool {
	return shellScratchCWDState(dir, roots) == ShellScratchWritesClean
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
// shellScratchCWDState (a directory), so the input carries no ".." left to
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
	"unzip": scratchFlags("-q -o -j -n -X -v -l -t -p -c", "-d -x -P -I -O"),
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
