// Git commands that sweep a work tree: the ones that throw away or carry off
// changes somebody else may have in flight (plan 198 §3.5).
//
// This is a pure classifier over the command string: it runs nothing, reads no
// repository, and asks no peer anything. It answers one question — would this
// command, if it ran here, be able to discard or commit changes the model did
// not make? — so the shell tool can turn that answer into an approval that even
// full access cannot answer for the user.
//
// The table is deliberately conservative (design §6, plan 198 §8): `git add .`
// and `git restore --staged` are in it. A false positive costs one approval; a
// false negative costs somebody's uncommitted work.
package safety

import (
	"path/filepath"
	"strings"
)

// The rules a sweeping git command is reported under. The name travels in the
// approval payload, the way a dangerous shell rule does, so a surface reads the
// fact from the classifier instead of re-deriving it.
const (
	GitSweepStash          = "git_stash"
	GitSweepCheckoutPaths  = "git_checkout_paths"
	GitSweepCheckoutForce  = "git_checkout_force"
	GitSweepRestore        = "git_restore"
	GitSweepResetHard      = "git_reset_hard"
	GitSweepClean          = "git_clean"
	GitSweepCommitAll      = "git_commit_all"
	GitSweepAddAll         = "git_add_all"
	GitSweepSwitchDiscard  = "git_switch_discard"
	gitSweepMaxWrapperDeep = 8
)

// gitGlobalOptionTakesValue names the git global options whose value is the
// next word rather than part of this one. Everything else that starts with a
// dash is a flag, and the subcommand is the first word that is neither.
var gitGlobalOptionTakesValue = map[string]struct{}{
	"-C": {}, "-c": {},
	"--git-dir": {}, "--work-tree": {}, "--namespace": {}, "--super-prefix": {},
	"--config-env": {}, "--exec-path": {}, "--attr-source": {},
}

// GitSweepsWorkTree reports the rule a command matches when it can discard or
// carry off a work tree's changes, and false for every command that cannot.
// `sudo`, `env` and `sh -c` wrappers are unwrapped, so the answer is the same
// however many layers the git call sits under.
func GitSweepsWorkTree(command string) (string, bool) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", false
	}
	return gitSweepFromCommands(gitSweepWords(command), 0)
}

// gitSweepFromCommands answers for a sequence of commands: the first one that
// sweeps decides, because the approval names one rule.
func gitSweepFromCommands(commands [][]string, depth int) (string, bool) {
	for _, command := range commands {
		if rule, hit := gitSweepFromWords(command, depth); hit {
			return rule, true
		}
	}
	return "", false
}

// gitSweepFromWords answers for one command, unwrapping the wrappers that hide
// another command inside themselves.
func gitSweepFromWords(command []string, depth int) (string, bool) {
	if depth > gitSweepMaxWrapperDeep || len(command) == 0 {
		return "", false
	}
	switch strings.ToLower(filepath.Base(command[0])) {
	case "git":
		return gitSweepFromGitArgs(command[1:])
	case "sudo":
		return gitSweepFromWords(command[1:], depth+1)
	case "env":
		i := 1
		for i < len(command) {
			arg := command[i]
			if arg == "--" {
				i++
				break
			}
			_, isAssignment := parseEnvAssignment(arg)
			if arg == "-i" || arg == "--ignore-environment" || isAssignment {
				i++
				continue
			}
			break
		}
		return gitSweepFromWords(command[i:], depth+1)
	case "sh", "bash", "zsh", "dash", "ksh":
		if script, ok := shellDashCScript(command); ok {
			return gitSweepFromSource(script, depth+1)
		}
	}
	return "", false
}

// gitSweepFromSource reads a script operand the way the outer command string is
// read, so `sh -c 'git clean -fd'` is answered by the command it runs.
func gitSweepFromSource(source string, depth int) (string, bool) {
	if depth > gitSweepMaxWrapperDeep {
		return "", false
	}
	return gitSweepFromCommands(gitSweepWords(source), depth)
}

// gitSweepWords words one command string. The strict word-only parser the
// dangerous-command detector reads comes first; when it refuses the source, a
// tolerant split reads it instead. The fallback exists because a revision or a
// glob is not shell complexity: `git reset --hard HEAD~1` and
// `git checkout -- pkg/*.go` are ordinary commands, and a classifier that could
// not read their arguments would miss the one form of reset that throws work
// away. Tolerating more source can only add hits, which for this table costs an
// approval rather than a lost change.
func gitSweepWords(source string) [][]string {
	if commands, ok := parseWordOnlyCommandSequence(source); ok {
		return commands
	}
	var commands [][]string
	var words []string
	var word strings.Builder
	haveWord := false
	quoted := rune(0)
	escaped := false
	flushWord := func() {
		if !haveWord {
			return
		}
		words = append(words, word.String())
		word.Reset()
		haveWord = false
	}
	flushCommand := func() {
		flushWord()
		if len(words) > 0 {
			commands = append(commands, words)
			words = nil
		}
	}
	for _, ch := range source {
		if escaped {
			word.WriteRune(ch)
			haveWord = true
			escaped = false
			continue
		}
		if quoted != 0 {
			if ch == quoted {
				quoted = 0
				haveWord = true
				continue
			}
			if ch == '\\' && quoted == '"' {
				escaped = true
				continue
			}
			word.WriteRune(ch)
			haveWord = true
			continue
		}
		switch ch {
		case '\'', '"':
			quoted = ch
			haveWord = true
		case '\\':
			escaped = true
		case ' ', '\t':
			flushWord()
		case ';', '\n', '\r', '|', '&', '(', ')':
			flushCommand()
		default:
			word.WriteRune(ch)
			haveWord = true
		}
	}
	flushCommand()
	return commands
}

// gitSweepFromGitArgs answers for one git invocation: the global options are
// skipped to reach the subcommand, and the subcommand decides.
func gitSweepFromGitArgs(args []string) (string, bool) {
	i := 0
	for i < len(args) {
		arg := strings.TrimSpace(args[i])
		if !strings.HasPrefix(arg, "-") {
			break
		}
		name, hasValue := arg, false
		if eq := strings.IndexByte(arg, '='); eq >= 0 {
			name, hasValue = arg[:eq], true
		}
		if _, takesValue := gitGlobalOptionTakesValue[name]; takesValue && !hasValue {
			i += 2
			continue
		}
		i++
	}
	if i >= len(args) {
		return "", false
	}
	return gitSweepFromSubcommand(strings.ToLower(strings.TrimSpace(args[i])), args[i+1:])
}

// gitSweepFromSubcommand applies the rule table to one subcommand and its
// arguments.
func gitSweepFromSubcommand(sub string, args []string) (string, bool) {
	switch sub {
	case "stash":
		// `stash list` and `stash show` only read. Everything else — push, pop,
		// apply, drop, clear, branch, and a bare `git stash` — moves the
		// working tree's changes out from under whoever was editing.
		if action := firstNonOptionArg(args); action == "list" || action == "show" {
			return "", false
		}
		return GitSweepStash, true
	case "checkout":
		if argIs(args, "--") || argIs(args, ".") {
			return GitSweepCheckoutPaths, true
		}
		if shortFlagWith(args, 'f') || argIs(args, "--force") {
			return GitSweepCheckoutForce, true
		}
	case "restore":
		// Every form of restore writes the working tree or the index back to a
		// committed state, including --staged.
		return GitSweepRestore, true
	case "reset":
		if argIs(args, "--hard") || argIs(args, "--merge") || argIs(args, "--keep") {
			return GitSweepResetHard, true
		}
	case "clean":
		if argIs(args, "--force") || shortFlagWith(args, 'f') {
			return GitSweepClean, true
		}
	case "commit":
		if argIs(args, "--all") || shortFlagWith(args, 'a') {
			return GitSweepCommitAll, true
		}
	case "add":
		if argIs(args, "--all") || argIs(args, "--update") || argIs(args, ".") ||
			shortFlagWith(args, 'A') || shortFlagWith(args, 'u') {
			return GitSweepAddAll, true
		}
	case "switch":
		if argIs(args, "--discard-changes") || argIs(args, "--force") || shortFlagWith(args, 'f') {
			return GitSweepSwitchDiscard, true
		}
	}
	return "", false
}

// argIs reports whether one of the arguments is exactly want.
func argIs(args []string, want string) bool {
	for _, arg := range args {
		if strings.TrimSpace(arg) == want {
			return true
		}
	}
	return false
}

// shortFlagWith reports whether one of the arguments is a short-option cluster
// carrying flag, which is how `-fd` and `-am` spell two flags at once. Long
// options are never scanned: `--force` carries an 'f' that means nothing here.
func shortFlagWith(args []string, flag rune) bool {
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || arg == "-" {
			continue
		}
		if strings.ContainsRune(arg[1:], flag) {
			return true
		}
	}
	return false
}

// firstNonOptionArg is the first argument that is not an option, lowercased:
// the sub-action of a subcommand that has one (`stash list`).
func firstNonOptionArg(args []string) string {
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" || strings.HasPrefix(arg, "-") {
			continue
		}
		return strings.ToLower(arg)
	}
	return ""
}
