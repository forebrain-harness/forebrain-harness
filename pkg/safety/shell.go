// Shell command safety: parsing, rule matching, prefixes, and rewriting.
package safety

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Separator tokens recognized between top-level segments.
const (
	SepNone    = ""
	SepAnd     = "&&"
	SepOr      = "||"
	SepSemi    = ";"
	SepPipe    = "|"
	SepBg      = "&"
	SepNewline = "\n"
)

// Segment is one top-level command segment of a shell line, along with the
// separator that followed it and its byte offsets in the source string.
//
// Offsets are retained so callers can splice edits back into the original
// string without reformatting anything else. Whitespace-preserving rewriting
// matters here: the user reads the rewritten command in the approval prompt, so
// it must differ from what they wrote only by the tokens that were inserted.
type Segment struct {
	// Text is the trimmed segment source.
	Text string
	// Sep is the separator that followed this segment (SepNone for the last).
	Sep string
	// Start is the byte offset of Text within the original string.
	Start int
	// End is the exclusive byte offset end of Text within the original string.
	End int
}

// IsSubshell reports whether the segment is wrapped in balanced parentheses,
// e.g. "(cd x && make)". Such segments are left alone by the rewriter.
func (s Segment) IsSubshell() bool {
	t := strings.TrimSpace(s.Text)
	return strings.HasPrefix(t, "(") && hasBalancedOuterParens(t)
}

// scanner tracks quoting and nesting state while walking a shell line.
//
// Scanning is byte-wise on purpose. Every shell metacharacter is ASCII, and
// UTF-8 continuation bytes are all >= 0x80, so a byte scan can never mistake
// part of a multi-byte rune for a metacharacter. Working in bytes keeps the
// offsets in Segment directly usable for slicing.
type scanner struct {
	inSingle bool
	inDouble bool
	inBacktk bool
	escaped  bool
	depth    int
}

// quoted reports whether the scanner is currently inside any quoting or
// nesting construct, i.e. whether metacharacters should be ignored.
func (sc *scanner) quoted() bool {
	return sc.inSingle || sc.inDouble || sc.inBacktk || sc.depth > 0
}

// step advances the scanner over s[i] and returns the number of bytes consumed
// and whether the byte was structural (already accounted for by the scanner).
// When structural is false the caller may inspect s[i] as a top-level
// metacharacter, but only if quoted() is false.
func (sc *scanner) step(s string, i int) (advance int, structural bool) {
	ch := s[i]
	if sc.escaped {
		sc.escaped = false
		return 1, true
	}
	if ch == '\\' && !sc.inSingle {
		sc.escaped = true
		return 1, true
	}
	if ch == '\'' && !sc.inDouble && !sc.inBacktk {
		sc.inSingle = !sc.inSingle
		return 1, true
	}
	if ch == '"' && !sc.inSingle && !sc.inBacktk {
		sc.inDouble = !sc.inDouble
		return 1, true
	}
	if sc.inSingle || sc.inDouble {
		return 1, true
	}
	if ch == '`' {
		sc.inBacktk = !sc.inBacktk
		return 1, true
	}
	if sc.inBacktk {
		return 1, true
	}
	// $( and ${ open a nesting level; a bare ( or { does too (subshell,
	// brace group, brace expansion). Tracking them together is sufficient
	// because we only need to know "not at top level".
	if ch == '$' && i+1 < len(s) && (s[i+1] == '(' || s[i+1] == '{') {
		sc.depth++
		return 2, true
	}
	if ch == '(' || ch == '{' {
		sc.depth++
		return 1, true
	}
	if ch == ')' || ch == '}' {
		if sc.depth > 0 {
			sc.depth--
		}
		return 1, true
	}
	if sc.depth > 0 {
		return 1, true
	}
	return 1, false
}

// SplitTopLevelWithSeparators splits a shell line into top-level segments,
// recording the separator after each one. Separators inside quotes, backticks,
// subshells, command substitutions, or brace groups are not split on.
//
// Empty segments produced by trailing or doubled separators are dropped, but
// the separator of a dropped trailing segment is preserved on the segment
// before it, so a backgrounded command keeps its trailing "&".
func SplitTopLevelWithSeparators(s string) []Segment {
	out := make([]Segment, 0, 4)
	var sc scanner
	start := 0

	emit := func(end int, sep string) {
		raw := s[start:end]
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			// Preserve a real separator on the previous segment (e.g. the
			// trailing "&" of "make &") instead of losing it.
			if sep != SepNone && len(out) > 0 && out[len(out)-1].Sep == SepNone {
				out[len(out)-1].Sep = sep
			}
			return
		}
		lead := len(raw) - len(strings.TrimLeft(raw, " \t\r\n"))
		out = append(out, Segment{
			Text:  trimmed,
			Sep:   sep,
			Start: start + lead,
			End:   start + lead + len(trimmed),
		})
	}

	for i := 0; i < len(s); {
		adv, structural := sc.step(s, i)
		if structural {
			i += adv
			continue
		}
		switch {
		case s[i] == '&' && i+1 < len(s) && s[i+1] == '&':
			emit(i, SepAnd)
			i += 2
			start = i
		case s[i] == '|' && i+1 < len(s) && s[i+1] == '|':
			emit(i, SepOr)
			i += 2
			start = i
		case s[i] == '|':
			emit(i, SepPipe)
			i++
			start = i
		case s[i] == '&':
			emit(i, SepBg)
			i++
			start = i
		case s[i] == ';':
			emit(i, SepSemi)
			i++
			start = i
		case s[i] == '\n' || s[i] == '\r':
			emit(i, SepNewline)
			i++
			start = i
		default:
			i += adv
		}
	}
	emit(len(s), SepNone)
	return out
}

// HasTopLevelRedirection reports whether the line contains an unquoted
// redirection operator at top level. Used as a rewrite tripwire: a redirected
// command's output does not reach the model, so rewriting it is pointless, and
// splicing near a redirection is where token-order mistakes would hurt most.
func HasTopLevelRedirection(s string) bool {
	var sc scanner
	for i := 0; i < len(s); {
		adv, structural := sc.step(s, i)
		if structural {
			i += adv
			continue
		}
		if s[i] == '>' || s[i] == '<' {
			return true
		}
		i += adv
	}
	return false
}

// isShellSpace reports whether b separates shell words.
func isShellSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// isIdentStart reports whether b may begin a shell identifier (variable name).
func isIdentStart(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// isIdentChar reports whether b may continue a shell identifier.
func isIdentChar(b byte) bool {
	return isIdentStart(b) || (b >= '0' && b <= '9')
}

// consumeShellWord returns the next word starting at or after offset i, with
// its start and exclusive end byte offsets. Quoting is respected, so
// `--msg="a b"` is one word. ok is false when only trailing space remains.
func consumeShellWord(s string, i int) (word string, start, end int, ok bool) {
	for i < len(s) && isShellSpace(s[i]) {
		i++
	}
	if i >= len(s) {
		return "", i, i, false
	}
	start = i
	var sc scanner
	for i < len(s) {
		if !sc.quoted() && !sc.escaped && isShellSpace(s[i]) {
			break
		}
		adv, _ := sc.step(s, i)
		i += adv
	}
	return s[start:i], start, i, true
}

// SplitWords splits a single command segment into quote-aware words.
func SplitWords(s string) []string {
	out := make([]string, 0, 8)
	i := 0
	for {
		w, _, end, ok := consumeShellWord(s, i)
		if !ok {
			return out
		}
		out = append(out, w)
		i = end
	}
}

// hasBalancedOuterParens reports whether s opens with "(" and that paren
// closes exactly at the end of s, meaning the whole string is one subshell.
func hasBalancedOuterParens(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "(") || !strings.HasSuffix(s, ")") {
		return false
	}
	var sc scanner
	for i := 0; i < len(s); {
		adv, _ := sc.step(s, i)
		i += adv
		// The opening paren was consumed on the first step; depth returning to
		// zero before the end means the outer parens are not a single wrapper.
		if sc.depth == 0 && i < len(s) {
			return false
		}
	}
	return sc.depth == 0
}

// PeelBalancedWrapper strips one layer of fully balanced outer parentheses,
// e.g. "(git status)" becomes "git status". Non-wrapped input is returned
// unchanged.
func PeelBalancedWrapper(s string) string {
	t := strings.TrimSpace(s)
	for hasBalancedOuterParens(t) {
		t = strings.TrimSpace(t[1 : len(t)-1])
	}
	return t
}

// unquoteWord removes surrounding quotes and escape backslashes from a word so
// its head can be compared against known command names. It is deliberately
// approximate: it is used for classification, never for execution.
func unquoteWord(w string) string {
	var b strings.Builder
	b.Grow(len(w))
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < len(w); i++ {
		ch := w[i]
		if escaped {
			b.WriteByte(ch)
			escaped = false
			continue
		}
		switch {
		case ch == '\\' && !inSingle:
			escaped = true
		case ch == '\'' && !inDouble:
			inSingle = !inSingle
		case ch == '"' && !inSingle:
			inDouble = !inDouble
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}

// ShellCommandAssessment is the deterministic, conservative command-safety
// result used by both shell tools. KnownSafe is an auto-approval classification,
// not a statement that the command cannot write; the sandbox remains the
// enforcement boundary for commands that run there.
type ShellCommandAssessment struct {
	Command            string
	Commands           [][]string
	Origin             string
	UsedComplexParsing bool
	KnownSafe          bool
	Dangerous          *DangerousCommandMatch
}

type DangerousCommandMatch struct {
	Rule string
}

func (a ShellCommandAssessment) SimpleKnownSafe() bool {
	return !a.UsedComplexParsing && a.KnownSafe && len(a.Commands) > 0
}

// AssessShellCommand does not treat an unrecognised command as a write. It accepts only a word-only
// sequence joined by &&, ||, ;, or | as evidence for KnownSafe. Everything
// else is complex, although dangerous literals are still inspected separately.
func AssessShellCommand(command string) ShellCommandAssessment {
	command = strings.TrimSpace(command)
	assessment := ShellCommandAssessment{Command: command, Origin: "direct"}
	if command == "" {
		assessment.UsedComplexParsing = true
		return assessment
	}

	commands, ok := parseWordOnlyCommandSequence(command)
	if ok && len(commands) == 1 && shellWrapperScript(commands[0]) != "" {
		assessment.Origin = "posix-shell-wrapper"
		commands, ok = parseWordOnlyCommandSequence(shellWrapperScript(commands[0]))
	}
	if !ok || len(commands) == 0 {
		assessment.UsedComplexParsing = true
		assessment.Dangerous = dangerousCommandFromSource(command)
		return assessment
	}
	assessment.Commands = commands
	for _, words := range commands {
		if containsShellControlKeyword(words) {
			assessment.UsedComplexParsing = true
			assessment.Commands = nil
			assessment.Dangerous = dangerousCommandFromSource(command)
			return assessment
		}
		if !knownSafeWords(words) {
			assessment.Dangerous = dangerousCommandFromCommands(commands)
			return assessment
		}
	}
	assessment.KnownSafe = true
	assessment.Dangerous = dangerousCommandFromCommands(commands)
	return assessment
}

// ShellCommandIsKnownSafe reports whether the command is provably read-only, in
// the sense ClassifyShellCommand uses: it writes nothing, and every path it
// names sits inside no particular root because the caller supplies none.
func ShellCommandIsKnownSafe(command string) bool {
	return ClassifyShellCommand(command, nil, "") == ShellMutationReadOnly
}

// knownSafeWords answers the same question for a command that
// parseWordOnlyCommandSequence has already tokenized: one simple command whose
// clauses the caller has already separated.
func knownSafeWords(words []string) bool {
	return classifyCommandWords(words, nil, "") == ShellMutationReadOnly
}

func containsShellControlKeyword(words []string) bool {
	for _, word := range words {
		switch word {
		case "if", "then", "fi", "for", "while", "until", "do", "done", "case", "esac", "function":
			return true
		}
	}
	return false
}

func shellWrapperScript(words []string) string {
	if len(words) != 3 {
		return ""
	}
	name := strings.ToLower(filepath.Base(words[0]))
	if name != "sh" && name != "bash" && name != "zsh" {
		return ""
	}
	if words[1] != "-c" && words[1] != "-lc" {
		return ""
	}
	return words[2]
}

// parseWordOnlyCommandSequence is deliberately narrower than a general shell
// parser. Quotes and escaped characters form words; only the approved
// sequence operators are accepted. Expansion, redirection, control flow and
// all other punctuation cause a failed safety proof.
func parseWordOnlyCommandSequence(source string) ([][]string, bool) {
	var commands [][]string
	var words []string
	var word strings.Builder
	quoted := rune(0)
	escaped := false
	haveWord := false
	flushWord := func() {
		if haveWord {
			words = append(words, word.String())
			word.Reset()
			haveWord = false
		}
	}
	flushCommand := func() bool {
		flushWord()
		if len(words) == 0 {
			return false
		}
		commands = append(commands, words)
		words = nil
		return true
	}
	runes := []rune(source)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if escaped {
			word.WriteRune(ch)
			haveWord = true
			escaped = false
			continue
		}
		if quoted != 0 {
			switch ch {
			case quoted:
				quoted = 0
			case '$', '`':
				return nil, false
			case '\\':
				if quoted == '\'' {
					word.WriteRune(ch)
					haveWord = true
				} else {
					escaped = true
				}
			default:
				word.WriteRune(ch)
				haveWord = true
			}
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
		case '&':
			if i+1 >= len(runes) || runes[i+1] != '&' || !flushCommand() {
				return nil, false
			}
			i++
		case '|':
			if !flushCommand() {
				return nil, false
			}
			if i+1 < len(runes) && runes[i+1] == '|' {
				i++
			}
		case ';':
			if !flushCommand() {
				return nil, false
			}
		case '$', '`', '<', '>', '(', ')', '{', '}', '[', ']', '\n', '\r', '~', '*', '?', '!':
			return nil, false
		default:
			word.WriteRune(ch)
			haveWord = true
		}
	}
	if quoted != 0 || escaped || !flushCommand() {
		return nil, false
	}
	return commands, true
}

func dangerousCommandFromCommands(commands [][]string) *DangerousCommandMatch {
	return dangerousCommandFromCommandsAtDepth(commands, 0)
}

func dangerousCommandFromCommandsAtDepth(commands [][]string, depth int) *DangerousCommandMatch {
	for _, command := range commands {
		if match := dangerousCommandFromWords(command, depth); match != nil {
			return match
		}
	}
	return nil
}

func dangerousCommandFromWords(command []string, depth int) *DangerousCommandMatch {
	if depth > 8 || len(command) == 0 {
		return nil
	}
	switch strings.ToLower(filepath.Base(command[0])) {
	case "rm":
		for _, arg := range command[1:] {
			if arg == "--" {
				break
			}
			if arg == "--force" || (strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.Contains(arg[1:], "f")) {
				return &DangerousCommandMatch{Rule: "forced_rm"}
			}
		}
	case "sudo":
		return dangerousCommandFromWords(command[1:], depth+1)
	case "env":
		i := 1
		for i < len(command) {
			arg := command[i]
			if arg == "--" {
				i++
				break
			}
			if _, isAssignment := parseEnvAssignment(arg); arg == "-i" || arg == "--ignore-environment" || isAssignment {
				i++
				continue
			}
			break
		}
		return dangerousCommandFromWords(command[i:], depth+1)
	case "trap":
		// A trap action is shell source held in the first operand, which may be
		// preceded by an explicit end-of-options marker.
		i := 1
		if i < len(command) && command[i] == "--" {
			i++
		}
		if i < len(command) && !strings.HasPrefix(command[i], "-") {
			return dangerousCommandFromSource(command[i])
		}
	case "sh", "bash", "zsh", "dash", "ksh":
		// `sh -c <script>` hides its real command in an operand. Unwrapping it
		// at every level is what catches a forced rm nested behind another
		// wrapper, such as `sudo sh -c '...'`; scanning only the outermost
		// string would miss it.
		if script, ok := shellDashCScript(command); ok {
			return dangerousCommandFromSourceAtDepth(script, depth+1)
		}
	}
	return nil
}

// shellDashCScript returns the script operand of a `-c` shell invocation.
func shellDashCScript(command []string) (string, bool) {
	for i := 1; i < len(command); i++ {
		arg := command[i]
		if arg == "-c" {
			if i+1 < len(command) {
				return command[i+1], true
			}
			return "", false
		}
		if !strings.HasPrefix(arg, "-") {
			return "", false
		}
	}
	return "", false
}

// The literal scan is deliberately only a dangerous-command detector. A match
// or a successful scan never establishes KnownSafe for complex source.
func dangerousCommandFromSource(source string) *DangerousCommandMatch {
	return dangerousCommandFromSourceAtDepth(source, 0)
}

func dangerousCommandFromSourceAtDepth(source string, depth int) *DangerousCommandMatch {
	if depth > 8 {
		return nil
	}
	if commands, ok := parseWordOnlyCommandSequence(source); ok {
		return dangerousCommandFromCommandsAtDepth(commands, depth)
	}
	fields := strings.FieldsFunc(source, func(r rune) bool {
		return r == ';' || r == '\n' || r == '\r' || r == '|' || r == '&' || r == '(' || r == ')'
	})
	for _, field := range fields {
		if words, ok := parseWordOnlyCommandSequence(strings.TrimSpace(field)); ok {
			if match := dangerousCommandFromCommandsAtDepth(words, depth); match != nil {
				return match
			}
		}
	}
	return nil
}

type ShellRuleType string

const (
	ShellRuleExact    ShellRuleType = "exact"
	ShellRulePrefix   ShellRuleType = "prefix"
	ShellRuleWildcard ShellRuleType = "wildcard"
)

type ShellRule struct {
	Type  ShellRuleType
	Value string
}

func ParseShellRule(content string) ShellRule {
	v := strings.TrimSpace(content)
	if v == "" {
		return ShellRule{Type: ShellRuleExact, Value: ""}
	}
	if base, ok := shellPrefixBase(v); ok {
		return ShellRule{Type: ShellRulePrefix, Value: base}
	}
	if hasUnescapedStar(v) {
		return ShellRule{Type: ShellRuleWildcard, Value: v}
	}
	return ShellRule{Type: ShellRuleExact, Value: v}
}

func ShellRuleMatches(ruleContent string, command string) bool {
	rule := ParseShellRule(ruleContent)
	cmd := strings.TrimSpace(command)
	if rule.Type == ShellRuleExact {
		return cmd == rule.Value
	}
	clauses := SplitShellClauses(cmd)
	if len(clauses) == 0 {
		clauses = []string{cmd}
	}
	for _, clause := range clauses {
		if !shellRuleMatchesClause(rule, clause) {
			return false
		}
	}
	return true
}

func CommandPrefixFromRuleContent(ruleContent string) []string {
	rule := ParseShellRule(ruleContent)
	if rule.Type != ShellRulePrefix {
		return nil
	}
	return normalizeCommandPrefix(splitShellLike(rule.Value))
}

func CommandPrefixMatches(prefix []string, command string) bool {
	prefix = normalizeCommandPrefix(prefix)
	if len(prefix) == 0 || commandHasControlOperator(command) {
		return false
	}
	return commandTokensHavePrefix(splitShellLike(strings.TrimSpace(command)), prefix)
}

func commandPrefixMatchesRestriction(prefix []string, command string) bool {
	prefix = normalizeCommandPrefix(prefix)
	if len(prefix) == 0 {
		return false
	}
	commandTokens := splitShellLike(strings.TrimSpace(command))
	if _, prefixStartsWithAssignment := parseEnvAssignment(prefix[0]); !prefixStartsWithAssignment {
		commandTokens = stripLeadingEnvAssignments(commandTokens)
	}
	return commandTokensHavePrefix(commandTokens, prefix)
}

// literalShellRule is the rule PermissionRuleValue.Command makes. It is the
// exact kind by construction rather than by ParseShellRule inferring it from
// the text, which is the whole point of the field: the command is compared as
// written, glob characters and all.
func literalShellRule(command string) ShellRule {
	return ShellRule{Type: ShellRuleExact, Value: strings.TrimSpace(command)}
}

func shellRuleMatchesRestriction(rule ShellRule, command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	tokens := splitShellLike(command)
	stripped := stripLeadingEnvAssignments(tokens)
	switch rule.Type {
	case ShellRulePrefix:
		prefix := splitShellLike(rule.Value)
		if len(prefix) == 0 {
			return false
		}
		if _, prefixStartsWithAssignment := parseEnvAssignment(prefix[0]); prefixStartsWithAssignment {
			return commandTokensHavePrefix(tokens, prefix)
		}
		return commandTokensHavePrefix(stripped, prefix)
	case ShellRuleWildcard:
		return wildcardMatch(rule.Value, command) ||
			(len(stripped) != len(tokens) && wildcardMatch(rule.Value, joinShellTokens(stripped)))
	default:
		ruleTokens := splitShellLike(rule.Value)
		return command == rule.Value ||
			(len(tokens) == len(ruleTokens) && commandTokensHavePrefix(tokens, ruleTokens)) ||
			(len(stripped) != len(tokens) && len(stripped) == len(ruleTokens) && commandTokensHavePrefix(stripped, ruleTokens))
	}
}

func stripLeadingEnvAssignments(tokens []string) []string {
	for len(tokens) > 0 {
		if _, ok := parseEnvAssignment(tokens[0]); !ok {
			break
		}
		tokens = tokens[1:]
	}
	return tokens
}

func normalizeCommandPrefix(prefix []string) []string {
	if len(prefix) == 0 {
		return nil
	}
	out := make([]string, 0, len(prefix))
	for index, token := range prefix {
		if strings.ContainsAny(token, "\x00\r\n") || (index == 0 && strings.TrimSpace(token) == "") {
			return nil
		}
		out = append(out, token)
	}
	return out
}

func commandTokensHavePrefix(commandTokens, prefixTokens []string) bool {
	if len(prefixTokens) == 0 || len(commandTokens) < len(prefixTokens) {
		return false
	}
	for index := range prefixTokens {
		if commandTokens[index] != prefixTokens[index] {
			return false
		}
	}
	return true
}

func shellRuleMatchesClause(rule ShellRule, clause string) bool {
	clause = strings.TrimSpace(clause)
	if clause == "" {
		return false
	}
	switch rule.Type {
	case ShellRulePrefix:
		if rule.Value == "" {
			return false
		}
		if commandHasControlOperator(clause) {
			return false
		}
		prefixTokens := splitShellLike(rule.Value)
		commandTokens := splitShellLike(clause)
		return commandTokensHavePrefix(commandTokens, prefixTokens)
	case ShellRuleWildcard:
		if commandHasControlOperator(clause) {
			return false
		}
		return wildcardMatch(rule.Value, clause)
	default:
		return clause == rule.Value
	}
}

func SplitShellClauses(s string) []string {
	out := make([]string, 0, 4)
	cur := strings.Builder{}
	inSingle := false
	inDouble := false
	escaped := false
	runes := []rune(s)
	flush := func() {
		v := strings.TrimSpace(cur.String())
		if v != "" {
			out = append(out, v)
		}
		cur.Reset()
	}
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if escaped {
			cur.WriteRune(ch)
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			cur.WriteRune(ch)
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			cur.WriteRune(ch)
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			cur.WriteRune(ch)
			inDouble = !inDouble
			continue
		}
		if !inSingle && !inDouble {
			if ch == '\n' || ch == '\r' {
				flush()
				continue
			}
			// "&" is only a control operator when it is not part of a
			// redirection. "2>&1" and "&>log" are one command with its output
			// rewired; splitting them yields a phantom clause ("1", "> log")
			// that is not known-safe, matches no rule, and therefore blocks
			// both a remembered approval and the prefix the prompt proposes.
			if ch == '&' && redirectionAmpersand(runes, i) {
				cur.WriteRune(ch)
				continue
			}
			if (ch == '&' || ch == '|') && i+1 < len(runes) && runes[i+1] == ch {
				flush()
				i++
				continue
			}
			if ch == '&' || ch == '|' || ch == ';' {
				flush()
				continue
			}
		}
		cur.WriteRune(ch)
	}
	flush()
	return out
}

// redirectionAmpersand reports whether the "&" at i belongs to a redirection
// operator rather than starting one of the control operators "&" or "&&".
// Shell writes both stream rewirings with it: "2>&1" and ">&2" put it after
// the angle bracket, "&>file" and "&>>file" put it before.
func redirectionAmpersand(runes []rune, i int) bool {
	if i < 0 || i >= len(runes) || runes[i] != '&' {
		return false
	}
	if i > 0 && (runes[i-1] == '>' || runes[i-1] == '<') {
		return true
	}
	return i+1 < len(runes) && runes[i+1] == '>'
}

// fdDuplicationWidth reports the rune width of the file-descriptor duplication
// operator that starts at i (the "&1" of "2>&1" is scanned from its ">"), or 0
// when the text at i is not one. "<&" and ">&" followed by a word instead of a
// descriptor number is an ordinary file redirection and reports 0.
func fdDuplicationWidth(runes []rune, i int) int {
	if i+2 >= len(runes) || (runes[i] != '>' && runes[i] != '<') || runes[i+1] != '&' {
		return 0
	}
	j := i + 2
	if runes[j] == '-' {
		return 3
	}
	for j < len(runes) && runes[j] >= '0' && runes[j] <= '9' {
		j++
	}
	if j == i+2 {
		return 0
	}
	if j < len(runes) && runes[j] == '-' {
		j++
	}
	return j - i
}

func commandHasControlOperator(cmd string) bool {
	inSingle := false
	inDouble := false
	escaped := false
	runes := []rune(cmd)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if inSingle {
			continue
		}
		// Command and parameter substitutions remain active inside double
		// quotes. A reusable prefix must never authorize the nested command.
		if ch == '`' {
			return true
		}
		if ch == '$' && i+1 < len(runes) && (runes[i+1] == '(' || runes[i+1] == '{') {
			return true
		}
		if inDouble {
			continue
		}
		switch ch {
		case '<', '>':
			// A file redirection names a target the reusable prefix does not,
			// so it keeps the clause opaque. A file-descriptor duplication
			// ("2>&1", ">&-") names no file and no command: it rewires the
			// streams of the very command the prefix already spells out.
			if w := fdDuplicationWidth(runes, i); w > 0 {
				i += w - 1
				continue
			}
			return true
		case ';', '|', '&', '\n', '\r':
			return true
		}
	}
	return false
}

func shellPrefixBase(v string) (string, bool) {
	if len(v) < 3 || !strings.HasSuffix(v, ":*") {
		return "", false
	}
	if strings.HasSuffix(v, "\\:*") {
		return "", false
	}
	base := strings.TrimSpace(v[:len(v)-2])
	if base == "" {
		return "", false
	}
	return base, true
}

func hasUnescapedStar(v string) bool {
	escaped := false
	for _, ch := range v {
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '*' {
			return true
		}
	}
	return false
}

func wildcardMatch(pattern, input string) bool {
	p := wildcardTokens(pattern)
	i := []rune(input)
	return wildcardMatchRunes(p, i)
}

func wildcardTokens(pattern string) []rune {
	out := make([]rune, 0, len(pattern))
	escaped := false
	for _, ch := range pattern {
		if escaped {
			out = append(out, ch)
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		out = append(out, ch)
	}
	if escaped {
		out = append(out, '\\')
	}
	return out
}

func wildcardMatchRunes(pattern, input []rune) bool {
	type state struct {
		pi int
		si int
	}
	seen := map[state]struct{}{}
	stack := []state{{}}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := seen[cur]; ok {
			continue
		}
		seen[cur] = struct{}{}
		pi, si := cur.pi, cur.si
		if pi == len(pattern) && si == len(input) {
			return true
		}
		if pi >= len(pattern) {
			continue
		}
		if pattern[pi] == '*' {
			stack = append(stack, state{pi: pi + 1, si: si})
			if si < len(input) {
				stack = append(stack, state{pi: pi, si: si + 1})
			}
			continue
		}
		if si < len(input) && pattern[pi] == input[si] {
			stack = append(stack, state{pi: pi + 1, si: si + 1})
		}
	}
	return false
}

func GetSimpleCommandPrefix(command string) (string, bool) {
	// A heredoc body is input to the preceding command, not a list of shell
	// commands. Do not derive a reusable permission rule from any of its lines.
	if hasShellHeredocOperator(command) {
		return "", false
	}
	lines := executableLines(command)
	if len(lines) != 1 {
		return "", false
	}
	return simpleCommandPrefixFromLine(commandPrefixSubject(lines[0]))
}

// commandPrefixSubject picks the part of a command line a reusable approval
// should be derived from: the one clause that is not already known-safe.
//
// A compound line carries a control operator, so deriving from the whole line
// yields nothing and the persistent choice is never even offered — which is
// most of the time, because the model routinely prefixes its real work with
// `cd <dir> &&`. Known-safe clauses need no approval of their own (the engine
// skips them when matching), so the remaining clause is the one to name.
//
// The rule must cover everything the command does. If two clauses need
// approval, a rule naming one of them would let the user approve
// "gofmt -w foo.go" for a command that also runs "rm -rf /tmp/x": the label
// would describe less than what runs. In that case the whole line is returned
// and its control operator makes the caller derive nothing at all.
//
// A line with no clause separators is returned unchanged, so the simple case
// keeps deriving from the whole command.
func commandPrefixSubject(line string) string {
	clauses := SplitShellClauses(line)
	if len(clauses) < 2 {
		return line
	}
	subject := ""
	for _, clause := range clauses {
		if ShellCommandIsKnownSafe(clause) {
			continue
		}
		if subject != "" {
			return line
		}
		subject = clause
	}
	if subject == "" {
		// Every clause is known-safe and needs no rule; proposing an approval
		// for something that never prompts would be noise.
		return line
	}
	return subject
}

func simpleCommandPrefixFromLine(line string) (string, bool) {
	if commandHasControlOperator(line) {
		return "", false
	}
	tokens := splitShellLike(line)
	if len(tokens) == 0 {
		return "", false
	}
	i := 0
	for i < len(tokens) {
		_, ok := parseEnvAssignment(tokens[i])
		if !ok {
			break
		}
		i++
	}
	if i >= len(tokens) {
		return "", false
	}

	if strings.HasPrefix(strings.TrimSpace(tokens[i]), "-") {
		return "", false
	}
	// Keep leading assignments in the amendment. Dropping them both prevents the
	// approved command from matching its own rule and could let a later command
	// run with materially different PATH or application settings.
	return joinShellTokens(commandIdentityTokens(tokens, i)), true
}

// commandIdentityTokens trims a command down to the part of it worth reusing:
// for a subcommand-style tool it is the program and its subcommand, so
// approving "git revert <sha> --no-edit" remembers "git revert" rather than
// that one commit — a rule that never matches again and reads like a mistake
// in the approval prompt.
//
// head is the index of the program word, so leading assignments are kept.
// Global options between the program and its subcommand are kept too: rules
// match token by token, and the command that runs is the rewritten one
// ("git --no-pager revert ..."), so a prefix that skipped --no-pager would
// match nothing.
//
// Everything else is returned whole. Anything the banned list names — bare
// "git", "npm run", "rm", interpreters — is a prefix broad enough that the
// engine refuses it as a rule, and a program with no subcommand (gofmt, curl,
// make) carries its meaning in operands this function cannot rank, so the
// exact command stays the proposal.
func commandIdentityTokens(tokens []string, head int) []string {
	sub := SubcommandIndex(tokens[head:])
	if sub < 0 {
		return tokens
	}
	identity := tokens[:head+sub+1]
	if bannedExecPolicyPrefix(identity) {
		return tokens
	}
	return identity
}

// ExecPolicyAmendmentTruncatesCommand reports whether prefix names less than
// the command clause it was derived from. Approval surfaces word the persistent
// choice from this: only a real truncation is honestly described as "commands
// that start with ...", while a prefix holding every word of the command
// describes that command alone.
func ExecPolicyAmendmentTruncatesCommand(prefix []string, command string) bool {
	prefix = normalizeCommandPrefix(prefix)
	if len(prefix) == 0 {
		return false
	}
	clauses := SplitShellClauses(strings.TrimSpace(command))
	if len(clauses) == 0 {
		clauses = []string{strings.TrimSpace(command)}
	}
	for _, clause := range clauses {
		tokens := splitShellLike(clause)
		if len(tokens) > len(prefix) && commandTokensHavePrefix(tokens, prefix) {
			return true
		}
	}
	return false
}

func joinShellTokens(tokens []string) string {
	parts := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token == "" {
			parts = append(parts, "''")
			continue
		}
		if strings.IndexFunc(token, func(r rune) bool {
			return !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("_@%+=:,./-", r)
		}) < 0 {
			parts = append(parts, token)
			continue
		}
		parts = append(parts, "'"+strings.ReplaceAll(token, "'", "'\"'\"'")+"'")
	}
	return strings.Join(parts, " ")
}

func executableLines(command string) []string {
	out := make([]string, 0, 4)
	raw := strings.ReplaceAll(command, "\r\n", "\n")
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// hasShellHeredocOperator reports whether command contains an unquoted,
// unescaped << or <<- heredoc operator. It intentionally does not parse a
// complete shell grammar: prefix suggestions are permissions, so false
// positives merely fall back to the exact-command choice while false negatives
// could derive a broad permission from heredoc content.
func hasShellHeredocOperator(command string) bool {
	inSingle := false
	inDouble := false
	escaped := false
	runes := []rune(command)
	for i, ch := range runes {
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if !inSingle && !inDouble && ch == '<' && i+1 < len(runes) && runes[i+1] == '<' {
			return true
		}
	}
	return false
}

func parseEnvAssignment(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	eq := strings.IndexByte(token, '=')
	if eq <= 0 {
		return "", false
	}
	key := token[:eq]
	if !validEnvKey(key) {
		return "", false
	}
	return key, true
}

func validEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i, ch := range k {
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || ch == '_' {
			continue
		}
		if i > 0 && ch >= '0' && ch <= '9' {
			continue
		}
		return false
	}
	return true
}

func splitShellLike(s string) []string {
	out := make([]string, 0, 8)
	cur := strings.Builder{}
	tokenStarted := false
	inSingle := false
	inDouble := false
	escaped := false
	flush := func() {
		if !tokenStarted {
			return
		}
		out = append(out, cur.String())
		cur.Reset()
		tokenStarted = false
	}
	for _, ch := range s {
		if escaped {
			cur.WriteRune(ch)
			tokenStarted = true
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			tokenStarted = true
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			tokenStarted = true
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			tokenStarted = true
			inDouble = !inDouble
			continue
		}
		if (ch == ' ' || ch == '\t') && !inSingle && !inDouble {
			flush()
			continue
		}
		cur.WriteRune(ch)
		tokenStarted = true
	}
	flush()
	return out
}

// RewriteDisableEnv turns rewriting off entirely when set truthy.
const RewriteDisableEnv = "FOREBRAIN_COMMAND_REWRITE"

// RewriteResult reports the outcome of Rewrite.
type RewriteResult struct {
	// Command is the command to execute. It equals the input when no rewrite
	// was applied, so callers can use it unconditionally.
	Command string
	// Applied reports whether Command differs from the input.
	Applied bool
	// Inserted lists the tokens that were inserted, in application order.
	Inserted []string
	// Notes explains each insertion, for the approval prompt.
	Notes []string
	// SkipReason records why rewriting was declined, for telemetry. Empty when
	// a rewrite was applied or when the command simply had no matching rule.
	SkipReason string
}

// Note renders a single human-readable summary line for the approval UI, or ""
// when nothing was rewritten. The user sees this alongside the final command.
func (r RewriteResult) Note() string {
	if !r.Applied || len(r.Notes) == 0 {
		return ""
	}
	return fmt.Sprintf("rewritten for quieter output (%s): %s",
		strings.Join(r.Inserted, " "), strings.Join(r.Notes, "; "))
}

// Rewrite conservatively rewrites a shell command so its output costs fewer
// context tokens, without changing what the command does.
//
// It runs before approval. The returned Command is what the approval prompt
// shows and what executes, so the user always consents to the final form.
//
// Rewriting is declined, returning the input unchanged, when:
//   - the disable env var is set
//   - the line contains a top-level redirection (output is not going to the
//     model anyway, and splicing near redirections is needlessly risky)
//   - a segment is a subshell, an interpreter invocation (bash -c ...), or
//     assignment-only, because its real program is opaque here
//   - any candidate insertion fails the verifySafe round-trip
//
// The verification step is what makes this safe to run ahead of the permission
// layer: a rewrite that cannot be reduced back to the exact original by
// removing only allowlisted presentation flags is discarded.
func Rewrite(command string) RewriteResult {
	res := RewriteResult{Command: command}
	if strings.TrimSpace(command) == "" {
		return res
	}
	if rewriteEnvSet(RewriteDisableEnv) {
		res.SkipReason = "disabled_by_env"
		return res
	}
	if HasTopLevelRedirection(command) {
		res.SkipReason = "top_level_redirection"
		return res
	}

	segs := SplitTopLevelWithSeparators(command)
	if len(segs) == 0 {
		return res
	}

	// Edits are collected as (offset, text) and applied right-to-left so that
	// earlier offsets stay valid. Everything outside the inserted tokens is
	// copied verbatim, which keeps the user's own formatting intact.
	type insertion struct {
		at   int
		text string
		note string
		flag string
	}
	var edits []insertion

	for _, seg := range segs {
		if seg.IsSubshell() {
			continue
		}
		c := ClassifyCommand(seg.Text)
		if c.Head == "" || c.AssignmentOnly || c.ShellScript {
			continue
		}
		// A wrapper (npx/pnpm run/...) forwards its own flags, so inserting on
		// the wrapped tool's behalf could land the flag on the wrapper. Skip.
		if c.Wrapper != "" {
			continue
		}
		for _, r := range rulesFor(c) {
			why, ok := allowedInsert(r.Flag)
			if !ok {
				// A rule referencing a non-allowlisted flag is a programming
				// error; refuse it rather than trusting the table.
				continue
			}
			if conflicted(c.Args, r) {
				continue
			}
			at, before, ok := insertOffset(command, seg, r)
			if !ok {
				continue
			}
			// Inserting ahead of an existing token needs the padding space on
			// the trailing side, or the flag would fuse with that token.
			text := " " + r.Flag
			if before {
				text = r.Flag + " "
			}
			edits = append(edits, insertion{at: at, text: text, note: why, flag: r.Flag})
		}
	}
	if len(edits) == 0 {
		return res
	}

	// Apply from the highest offset down.
	for i := 0; i < len(edits); i++ {
		for j := i + 1; j < len(edits); j++ {
			if edits[j].at > edits[i].at {
				edits[i], edits[j] = edits[j], edits[i]
			}
		}
	}
	out := command
	for _, e := range edits {
		if e.at < 0 || e.at > len(out) {
			res.SkipReason = "offset_out_of_range"
			return RewriteResult{Command: command, SkipReason: res.SkipReason}
		}
		out = out[:e.at] + e.text + out[e.at:]
	}

	inserted := make([]string, 0, len(edits))
	notes := make([]string, 0, len(edits))
	seenNote := map[string]struct{}{}
	// Report in source order for readability.
	for i := len(edits) - 1; i >= 0; i-- {
		inserted = append(inserted, edits[i].flag)
		if _, dup := seenNote[edits[i].note]; !dup {
			seenNote[edits[i].note] = struct{}{}
			notes = append(notes, edits[i].note)
		}
	}

	if !verifySafe(command, out, inserted) {
		// Fail closed: the rewrite could not be proven equivalent.
		return RewriteResult{Command: command, SkipReason: "verification_failed"}
	}
	return RewriteResult{Command: out, Applied: true, Inserted: inserted, Notes: notes}
}

// insertOffset resolves where a rule's flag should be spliced into the original
// command string, returning an absolute byte offset and whether the flag goes
// immediately before the token at that offset (rather than after the preceding
// one), which decides on which side the padding space belongs.
func insertOffset(command string, seg Segment, r Rule) (at int, before bool, ok bool) {
	if seg.Start < 0 || seg.End > len(command) || seg.Start > seg.End {
		return 0, false, false
	}
	if !r.AfterHead {
		// A bare "--" ends the head command's own options: everything after it
		// is an operand forwarded to the inner program (`cargo test -- args`,
		// `npm test -- args`). Appending at the segment end would hand our
		// presentation flag to that program as one of its arguments, changing
		// what the command does. Insert before the "--" so the flag still lands
		// on the head command, where it is inert.
		if off, found := endOfOptionsOffset(command, seg); found {
			return off, true, true
		}
		// End of the segment, before whatever separator follows.
		return seg.End, false, true
	}
	// Immediately after the head word. Locate it within the segment so leading
	// env assignments and sudo are skipped exactly as the classifier saw them.
	local := command[seg.Start:seg.End]
	i := 0
	for {
		w, _, end, found := consumeShellWord(local, i)
		if !found {
			return 0, false, false
		}
		if isAssignmentWord(w) {
			i = end
			continue
		}
		base := strings.ToLower(firstWordBasename(w))
		if _, isSudo := sudoHeads[base]; isSudo {
			i = end
			continue
		}
		if _, isEnvKw := envPrefixKeywords[base]; isEnvKw {
			i = end
			continue
		}
		if base != r.Head {
			return 0, false, false
		}
		return seg.Start + end, false, true
	}
}

// endOfOptionsOffset returns the absolute byte offset of a bare "--" word in
// seg, if one is present. The token must be exactly "--": "--foo" is an option
// and "---" is an operand, neither of which ends option parsing.
func endOfOptionsOffset(command string, seg Segment) (int, bool) {
	local := command[seg.Start:seg.End]
	i := 0
	for {
		w, start, end, ok := consumeShellWord(local, i)
		if !ok {
			return 0, false
		}
		if w == "--" {
			return seg.Start + start, true
		}
		i = end
	}
}

// verifySafe proves that rewritten differs from original only by the insertion
// of allowlisted presentation flags.
//
// The check is deliberately mechanical rather than semantic: tokenize both
// sides, walk them in lockstep, and require that every extra token on the
// rewritten side is allowlisted and accounted for in inserted. Any other
// difference, including a reordered or altered operand, fails.
//
// A tokenized comparison is the right granularity because it catches the
// failure mode that matters: an insertion landing inside a quoted string or
// splitting an existing word would change the token stream in a way byte
// comparison of the concatenation would not reveal.
func verifySafe(original, rewritten string, inserted []string) bool {
	if rewritten == original {
		return false // nothing changed; caller should not have claimed a rewrite
	}
	// Structural shape must be identical: same segment count and separators.
	oSegs := SplitTopLevelWithSeparators(original)
	rSegs := SplitTopLevelWithSeparators(rewritten)
	if len(oSegs) != len(rSegs) {
		return false
	}
	for i := range oSegs {
		if oSegs[i].Sep != rSegs[i].Sep {
			return false
		}
	}
	if HasTopLevelRedirection(rewritten) != HasTopLevelRedirection(original) {
		return false
	}

	budget := map[string]int{}
	for _, f := range inserted {
		if _, ok := allowedInsert(f); !ok {
			return false
		}
		budget[f]++
	}

	for i := range oSegs {
		ow := SplitWords(oSegs[i].Text)
		rw := SplitWords(rSegs[i].Text)
		oi := 0
		pastEndOfOptions := false
		for ri := 0; ri < len(rw); ri++ {
			if oi < len(ow) && rw[ri] == ow[oi] {
				if ow[oi] == "--" {
					// Everything after a bare "--" is an operand of the inner
					// program, not an option of the head command.
					pastEndOfOptions = true
				}
				oi++
				continue
			}
			// Extra token on the rewritten side: must be a budgeted allowlisted flag.
			if budget[rw[ri]] <= 0 {
				return false
			}
			if _, ok := allowedInsert(rw[ri]); !ok {
				return false
			}
			// An insertion past "--" would be forwarded as an argument to the
			// inner program, which changes what the command does even though
			// the token itself is allowlisted. Only position makes it unsafe,
			// so no rule may place one here.
			if pastEndOfOptions {
				return false
			}
			budget[rw[ri]]--
		}
		if oi != len(ow) {
			return false // an original token disappeared
		}
	}
	// Every promised insertion must be present exactly once.
	for _, n := range budget {
		if n != 0 {
			return false
		}
	}
	return true
}

// rewriteEnvSet reports whether an env var is set to a falsey value. Rewriting is
// on by default, so only an explicit off value disables it.
func rewriteEnvSet(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "0", "false", "off", "no", "disable", "disabled":
		return true
	}
	return false
}

// Classification: what one shell command does to the filesystem.
//
// This is the single answer to that question in the tree. The read-only and
// mutating tables live here and nowhere else: a second copy is a copy that will
// eventually describe a decision the runtime does not make, which is how
// `find -delete` came to be read-only for the plan gate that used the other
// table and refused for the approval path that used this one.

// ShellMutation is the three-state answer to "what does this command do to the
// filesystem?". The values are ordered by severity so that the worse of two
// answers is simply the greater one.
//
// The third state carries the point of the type. A yes/no whitelist answers "I
// cannot prove this is read-only" with "this writes", which turns every command
// it has never seen into a refusal; the same collapse in the other direction
// lets a command it does recognise read from wherever it likes. Callers get all
// three answers and decide for themselves: run it, refuse it, or ask.
type ShellMutation int

const (
	// ShellMutationReadOnly is proof that the command writes no file anywhere.
	ShellMutationReadOnly ShellMutation = iota
	// ShellMutationUnproven is neither proof: the command is unrecognised, or
	// something inside it (an expansion, a program read from a file) puts its
	// effect beyond what this package can see. The answer is "ask", not
	// "refuse".
	ShellMutationUnproven
	// ShellMutationWrites is proof that the command writes a file.
	ShellMutationWrites
)

func (m ShellMutation) String() string {
	switch m {
	case ShellMutationReadOnly:
		return "read-only"
	case ShellMutationWrites:
		return "writes"
	default:
		return "unproven"
	}
}

// worseShellMutation returns the more restrictive of two answers: a proven
// write outranks doubt, and doubt outranks proof of read-only.
func worseShellMutation(a, b ShellMutation) ShellMutation {
	if a > b {
		return a
	}
	return b
}

// ClassifyShellCommand reports what one shell command line does to the
// filesystem.
//
// allowedRoots and cwd bound the paths the command may address. A command that
// is read-only in itself but names a path outside every allowed root is
// Unproven rather than ReadOnly: this answer also decides whether the command
// may run unattended, so reading somewhere the operator did not open is not
// something to approve on the command's own recognisance.
func ClassifyShellCommand(cmd string, allowedRoots []string, cwd string) ShellMutation {
	cmd = stripDevNullRedirects(cmd)
	if strings.TrimSpace(cmd) == "" {
		return ShellMutationUnproven
	}
	clauses, floor := splitShellClassifyClauses(cmd)
	worst := floor
	if len(clauses) == 0 {
		return worseShellMutation(worst, ShellMutationUnproven)
	}
	for _, clause := range clauses {
		worst = worseShellMutation(worst, classifyShellClause(clause, allowedRoots, cwd))
	}
	return worst
}

// splitShellClassifyClauses splits a command line into its simple clauses and
// reports the worst tier implied by the structure around them.
//
// that structure is what the clause texts cannot say once the operators are
// gone: a file redirect ("... > out.txt") is a write, and an expansion ("$VAR",
// "$(cmd)", backticks) hides a command or a path this package cannot see. Both
// are reported through the returned floor rather than as a failure to split, so
// the rest of the line is still classified clause by clause.
func splitShellClassifyClauses(cmd string) ([]string, ShellMutation) {
	runes := []rune(cmd)
	buf := make([]rune, 0, len(runes))
	clauses := make([]string, 0, 4)
	floor := ShellMutationReadOnly
	inSingle, inDouble, escaped := false, false, false
	atWordStart := true
	flush := func() {
		if clause := strings.TrimSpace(string(buf)); clause != "" {
			clauses = append(clauses, clause)
		}
		buf = buf[:0]
	}
	write := func(ch rune) {
		buf = append(buf, ch)
		atWordStart = false
	}
	separate := func() {
		flush()
		atWordStart = true
	}
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if escaped {
			write(ch)
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			buf = append(buf, ch)
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			buf = append(buf, ch)
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			buf = append(buf, ch)
			inDouble = !inDouble
			continue
		}
		if inSingle {
			// Single quotes suppress every expansion and every operator.
			write(ch)
			continue
		}
		if inDouble {
			// Inside double quotes only the substitutions stay active; the
			// operators and the redirections are literal text.
			if ch == '$' || ch == '`' {
				floor = worseShellMutation(floor, ShellMutationUnproven)
			}
			write(ch)
			continue
		}
		switch ch {
		case ' ', '\t':
			buf = append(buf, ch)
			atWordStart = true
		case '\n', '\r', ';':
			separate()
		case '|':
			// "|" and "||" both separate two commands.
			separate()
		case '&':
			switch {
			case i+1 < len(runes) && runes[i+1] == '&':
				separate()
				i++
			case i+1 < len(runes) && runes[i+1] == '>':
				// "&>file" and "&>>file" send both streams to a file.
				floor = worseShellMutation(floor, ShellMutationWrites)
				i++
				if i+1 < len(runes) && runes[i+1] == '>' {
					i++
				}
				atWordStart = true
			default:
				// A single "&" backgrounds the clause; it runs either way.
				separate()
			}
		case '>':
			if w := shellFDDupWidth(runes, i); w > 0 {
				buf = trimShellFDNumber(buf)
				i += w - 1
				continue
			}
			floor = worseShellMutation(floor, ShellMutationWrites)
			buf = trimShellFDNumber(buf)
			if i+1 < len(runes) && runes[i+1] == '>' {
				i++
			}
			atWordStart = true
		case '<':
			// Reading a file in changes no file; a descriptor duplication
			// names no file at all.
			if w := shellFDDupWidth(runes, i); w > 0 {
				buf = trimShellFDNumber(buf)
				i += w - 1
				continue
			}
			buf = trimShellFDNumber(buf)
			atWordStart = true
		case '$', '`':
			// Command and parameter substitution: the command that runs, and
			// the paths it touches, are decided while it runs. Both remain
			// active inside double quotes.
			floor = worseShellMutation(floor, ShellMutationUnproven)
			write(ch)
		case '(', ')', '{', '}':
			// Subshell, brace group and brace expansion all move the command
			// out of the shape this scanner can read.
			floor = worseShellMutation(floor, ShellMutationUnproven)
			write(ch)
		case '~':
			if atWordStart {
				// A bare "~" expands to a home directory outside every
				// allowed root.
				floor = worseShellMutation(floor, ShellMutationUnproven)
			}
			write(ch)
		default:
			if ch == '=' || ch == ':' {
				atWordStart = true
			}
			write(ch)
		}
	}
	if escaped || inSingle || inDouble {
		// An unterminated quote or escape hides part of the line from this
		// scanner.
		return nil, worseShellMutation(floor, ShellMutationUnproven)
	}
	flush()
	return clauses, floor
}

// shellFDDupWidth reports the rune width of the file-descriptor duplication
// starting at i (">", ">&2", "2>&1", ">&-"), or 0 when the same bracket starts
// an ordinary file redirect. "<&" and ">&" followed by a word instead of a
// descriptor is a file redirect in both bash and csh lineage, so it reports 0.
func shellFDDupWidth(runes []rune, i int) int {
	if i+1 >= len(runes) || (runes[i] != '>' && runes[i] != '<') || runes[i+1] != '&' {
		return 0
	}
	j := i + 2
	if j >= len(runes) {
		// A dangling ">&": nothing follows to name a file.
		return j - i
	}
	if runes[j] == '-' {
		return 3
	}
	for j < len(runes) && runes[j] >= '0' && runes[j] <= '9' {
		j++
	}
	if j == i+2 {
		return 0
	}
	if j < len(runes) && runes[j] == '-' {
		j++
	}
	return j - i
}

// trimShellFDNumber drops a bare file-descriptor number that a redirect
// operator just turned into a prefix of the next word, so "2>out.txt" leaves
// "out.txt" rather than "2out.txt". A number that is part of an argument
// ("head -2 f") is left alone.
func trimShellFDNumber(buf []rune) []rune {
	j := len(buf)
	for j > 0 && buf[j-1] >= '0' && buf[j-1] <= '9' {
		j--
	}
	if j == len(buf) {
		return buf
	}
	if j > 0 {
		switch buf[j-1] {
		case ' ', '\t', '\n', '\r', ';', '|', '&':
		default:
			return buf
		}
	}
	return buf[:j]
}

// classifyShellClause classifies one clause of a command line.
func classifyShellClause(clause string, allowedRoots []string, cwd string) ShellMutation {
	return classifyCommandWords(SplitShellWords(clause), allowedRoots, cwd)
}

// classifyCommandWords classifies one already-tokenized simple command: it
// skips leading environment assignments, takes the command name from the first
// remaining word, and consults the tables.
func classifyCommandWords(words []string, allowedRoots []string, cwd string) ShellMutation {
	i := 0
	for i < len(words) && shellEnvAssignment(words[i]) {
		i++
	}
	if i >= len(words) {
		return ShellMutationUnproven
	}
	name := strings.ToLower(strings.TrimSpace(words[i]))
	if idx := strings.LastIndexAny(name, `/\`); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" {
		return ShellMutationUnproven
	}
	return classifyCommand(name, words[i+1:], allowedRoots, cwd)
}

// shellReadOnlyCommands are the commands proven to write nothing, whatever
// their arguments. Argument-level forms that do write are handled by the switch
// in classifyCommand, which every name here goes through first.
var shellReadOnlyCommands = map[string]struct{}{
	// File listing, inspection and pagination.
	"pwd": {}, "ls": {}, "cat": {}, "head": {}, "tail": {}, "wc": {}, "file": {}, "stat": {},
	"tree": {}, "less": {}, "more": {}, "du": {}, "df": {},
	// Pattern search.
	"grep": {}, "rg": {}, "egrep": {}, "fgrep": {},
	// Output only: the scanner has already refused a redirect by the time the
	// clause is classified.
	"echo": {}, "printf": {},
	// Host and command lookup.
	"which": {}, "type": {}, "whereis": {}, "where": {}, "date": {}, "uname": {}, "hostname": {},
	"whoami": {}, "id": {}, "groups": {}, "printenv": {}, "ps": {}, "getconf": {}, "locale": {},
	"tty": {}, "sw_vers": {}, "arch": {},
	// Text processing, output only.
	"cut": {}, "tr": {}, "join": {}, "paste": {}, "uniq": {}, "nl": {},
	"seq": {}, "expr": {}, "rev": {}, "column": {}, "expand": {}, "unexpand": {}, "fold": {},
	// Comparison.
	"diff": {}, "cmp": {}, "comm": {},
	// Path utilities.
	"basename": {}, "dirname": {}, "realpath": {}, "readlink": {},
	// Binary analysis, output only.
	"nm": {}, "objdump": {}, "strings": {}, "size": {},
	// Display formatting, output only.
	"od": {}, "xxd": {}, "hexdump": {},
	// Checksums, output only.
	"md5": {}, "md5sum": {}, "sha1sum": {}, "sha256sum": {}, "shasum": {}, "cksum": {},
	// Line counting.
	"tokei": {},
	// File viewers that cannot write.
	"bat": {}, "batcat": {},
	// No-ops.
	"true": {}, "false": {},
}

// shellNumfmtAndTacCommands are GNU tools with no equivalent elsewhere; a
// program that happens to carry the name is not the tool this table describes.
var shellNumfmtAndTacCommands = map[string]struct{}{
	"numfmt": {}, "tac": {},
}

// shellMutatingCommands are the commands whose whole purpose is to write.
var shellMutatingCommands = map[string]struct{}{
	"rm": {}, "rmdir": {}, "mv": {}, "cp": {}, "mkdir": {}, "touch": {}, "ln": {},
	"chmod": {}, "chown": {}, "chgrp": {}, "dd": {}, "truncate": {}, "tee": {},
	"install": {}, "patch": {}, "shred": {}, "unlink": {},
}

// classifyCommand dispatches one command name with its arguments.
func classifyCommand(name string, args []string, allowedRoots []string, cwd string) ShellMutation {
	if _, ok := shellMutatingCommands[name]; ok {
		return ShellMutationWrites
	}
	switch name {
	case "git":
		return classifyGitCommand(args, allowedRoots, cwd)
	case "go":
		return classifyGoCommand(args, allowedRoots, cwd)
	case "gofmt":
		return classifyGofmtCommand(args, allowedRoots, cwd)
	case "sed":
		return classifySedCommand(args, allowedRoots, cwd)
	case "awk", "gawk", "mawk", "nawk":
		return classifyAwkCommand(args, allowedRoots, cwd)
	case "jq":
		return classifyJQCommand(args, allowedRoots, cwd)
	case "yq":
		return classifyYQCommand(args, allowedRoots, cwd)
	case "sort":
		return classifySortCommand(args, allowedRoots, cwd)
	case "base64":
		return classifyBase64Command(args, allowedRoots, cwd)
	case "find":
		return classifyFindCommand(args, allowedRoots, cwd)
	case "fd", "fdfind":
		return classifyFDCommand(args, allowedRoots, cwd)
	case "cd":
		return classifyCDCommand(args, allowedRoots, cwd)
	case "env", "xargs", "nice", "time", "timeout", "sudo", "nohup", "watch", "command", "setsid", "stdbuf":
		return classifyWrappedCommand(name, args, allowedRoots, cwd)
	}
	if _, ok := shellNumfmtAndTacCommands[name]; ok {
		if runtime.GOOS != "linux" {
			return ShellMutationUnproven
		}
		return readOnlyArgsUnderRoots(args, allowedRoots, cwd)
	}
	if _, ok := shellReadOnlyCommands[name]; ok {
		return readOnlyArgsUnderRoots(args, allowedRoots, cwd)
	}
	return ShellMutationUnproven
}

// readOnlyArgsUnderRoots answers ReadOnly for a command the table proves writes
// nothing, once every path-shaped argument resolves inside an allowed root.
func readOnlyArgsUnderRoots(args []string, allowedRoots []string, cwd string) ShellMutation {
	for _, arg := range args {
		a := strings.ToLower(strings.TrimSpace(arg))
		switch {
		case a == "--output" || strings.HasPrefix(a, "--output="):
			// Wherever this flag appears it names a file to write.
			return ShellMutationWrites
		case strings.HasPrefix(a, "--files0-from"):
			// The named file lists further files: what gets read is decided at
			// run time.
			return ShellMutationUnproven
		}
	}
	if !fileArgPathsUnderAllowedRoots(args, allowedRoots, cwd) {
		return ShellMutationUnproven
	}
	return ShellMutationReadOnly
}

// classifyCDCommand handles cd, which changes the working directory and touches
// no file, but which must still land inside an allowed root: everything the
// next clause reads is resolved from there.
func classifyCDCommand(args []string, allowedRoots []string, cwd string) ShellMutation {
	if !cdPathUnderAllowedRoots(args, allowedRoots, cwd) {
		return ShellMutationUnproven
	}
	return readOnlyArgsUnderRoots(args, allowedRoots, cwd)
}

// classifyFindCommand rejects the actions that write or run a command. A find
// that only walks and prints is read-only; the predicates that hand the result
// to a program are not, because that program is not part of this text.
func classifyFindCommand(args []string, allowedRoots []string, cwd string) ShellMutation {
	for _, arg := range args {
		switch strings.TrimSpace(arg) {
		case "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fls", "-fprint", "-fprint0", "-fprintf":
			return ShellMutationWrites
		}
	}
	return readOnlyArgsUnderRoots(args, allowedRoots, cwd)
}

// classifyFDCommand handles fd, whose -x/-X run another command the classifier
// would have to read to judge.
func classifyFDCommand(args []string, allowedRoots []string, cwd string) ShellMutation {
	for _, arg := range args {
		a := strings.TrimSpace(arg)
		switch {
		case a == "-x" || a == "-X" || a == "--exec" || a == "--exec-batch",
			strings.HasPrefix(a, "--exec="), strings.HasPrefix(a, "--exec-batch="):
			return ShellMutationUnproven
		}
	}
	return readOnlyArgsUnderRoots(args, allowedRoots, cwd)
}

// classifySortCommand rejects sort's -o/--output, which writes to a file.
func classifySortCommand(args []string, allowedRoots []string, cwd string) ShellMutation {
	for _, arg := range args {
		a := strings.TrimSpace(arg)
		switch {
		case a == "-o" || a == "--output" || strings.HasPrefix(a, "--output="):
			return ShellMutationWrites
		}
	}
	return readOnlyArgsUnderRoots(args, allowedRoots, cwd)
}

// classifyBase64Command rejects base64's -o/--output, which writes to a file.
func classifyBase64Command(args []string, allowedRoots []string, cwd string) ShellMutation {
	for _, arg := range args {
		a := strings.ToLower(strings.TrimSpace(arg))
		switch {
		case a == "-o" || a == "--output" || strings.HasPrefix(a, "--output="),
			strings.HasPrefix(a, "-o") && len(a) > 2:
			return ShellMutationWrites
		}
	}
	return readOnlyArgsUnderRoots(args, allowedRoots, cwd)
}

// classifyGofmtCommand rejects gofmt -w, which rewrites the files in place.
// Without it gofmt prints its result and writes nothing.
func classifyGofmtCommand(args []string, allowedRoots []string, cwd string) ShellMutation {
	for _, arg := range args {
		a := strings.TrimSpace(arg)
		switch {
		case a == "-w" || strings.HasPrefix(a, "-w="):
			return ShellMutationWrites
		case strings.HasPrefix(a, "-cpuprofile"):
			return ShellMutationUnproven
		}
	}
	return readOnlyArgsUnderRoots(args, allowedRoots, cwd)
}

// classifyGoCommand splits the go tool by what each subcommand does. Building,
// testing and tidying all write; reporting does not.
func classifyGoCommand(args []string, allowedRoots []string, cwd string) ShellMutation {
	if len(args) == 0 {
		return ShellMutationUnproven
	}
	sub := strings.ToLower(strings.TrimSpace(args[0]))
	rest := args[1:]
	switch sub {
	case "env":
		for _, arg := range rest {
			a := strings.TrimSpace(arg)
			if a == "-w" || a == "-u" || (strings.HasPrefix(a, "-w") && len(a) > 2) || (strings.HasPrefix(a, "-u") && len(a) > 2) {
				// Writing the go env file is a write outside the repository,
				// but it is still a mutation the plan gate must not wave
				// through.
				return ShellMutationWrites
			}
		}
		return readOnlyArgsUnderRoots(rest, allowedRoots, cwd)
	case "version", "list", "doc":
		return readOnlyArgsUnderRoots(rest, allowedRoots, cwd)
	case "mod":
		if len(rest) > 0 {
			switch strings.ToLower(strings.TrimSpace(rest[0])) {
			case "graph", "why", "verify":
				return readOnlyArgsUnderRoots(rest[1:], allowedRoots, cwd)
			}
		}
		// tidy, download, edit, init and vendor all rewrite go.mod/go.sum.
		return ShellMutationWrites
	case "build", "test", "install", "generate", "run", "vet", "fix", "clean", "work", "tool", "fmt", "get":
		return ShellMutationWrites
	}
	return ShellMutationUnproven
}

// classifyAwkCommand separates an awk program from the files it reads: the
// program text is not a path, and a program that came from a file cannot be
// inspected at all.
func classifyAwkCommand(args []string, allowedRoots []string, cwd string) ShellMutation {
	program := ""
	haveProgram := false
	operands := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		switch {
		case a == "" || a == "--":
		case a == "-f" || a == "--file" || strings.HasPrefix(a, "--file="), strings.HasPrefix(a, "-f") && len(a) > 2:
			// The program lives in a file this classifier cannot read.
			return ShellMutationUnproven
		case a == "-F" || a == "-v" || a == "-E" || a == "--field-separator" || a == "--assign":
			// The next word is the option's value, not the program.
			i++
		case strings.HasPrefix(a, "-") && len(a) > 1:
		case !haveProgram:
			program = a
			haveProgram = true
		default:
			operands = append(operands, a)
		}
	}
	if !haveProgram {
		return ShellMutationUnproven
	}
	if awkProgramCanWrite(program) {
		return ShellMutationUnproven
	}
	return readOnlyArgsUnderRoots(operands, allowedRoots, cwd)
}

// awkProgramCanWrite reports whether an awk program can reach outside its own
// operands: "print > "file"", "print | "cmd"" and system() write or execute,
// and ENVIRON and /dev/std* read what the arguments did not name. The check is
// deliberately eager - a comparison such as "$1 > 3" is read as a write - so
// the cost of a false positive is a prompt, never a silent write.
func awkProgramCanWrite(program string) bool {
	lower := strings.ToLower(program)
	for _, marker := range []string{">", "|", "system(", "close(", "environ", "/dev/std"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// classifyJQCommand rejects jq -f/--from-file, where the program comes from a
// file that cannot be read here.
func classifyJQCommand(args []string, allowedRoots []string, cwd string) ShellMutation {
	for _, arg := range args {
		a := strings.TrimSpace(arg)
		switch {
		case a == "-f" || a == "--from-file" || strings.HasPrefix(a, "--from-file="),
			strings.HasPrefix(a, "-f") && len(a) > 2:
			return ShellMutationUnproven
		}
	}
	return readOnlyArgsUnderRoots(args, allowedRoots, cwd)
}

// classifyYQCommand rejects yq's in-place flag, which rewrites its input file.
func classifyYQCommand(args []string, allowedRoots []string, cwd string) ShellMutation {
	for _, arg := range args {
		a := strings.ToLower(strings.TrimSpace(arg))
		switch {
		case a == "-i" || a == "--in-place" || a == "--inplace",
			strings.HasPrefix(a, "--in-place="), strings.HasPrefix(a, "--inplace="),
			strings.HasPrefix(a, "-i") && len(a) > 2:
			return ShellMutationWrites
		}
	}
	return readOnlyArgsUnderRoots(args, allowedRoots, cwd)
}

// shellWrapperValueFlags lists, per wrapper, the flags whose value is the next
// token. A flag missing from this table costs a stricter answer only: its value
// is then read as the command name, and an unrecognised command is Unproven.
var shellWrapperValueFlags = map[string]map[string]struct{}{
	"xargs":   {"-n": {}, "-I": {}, "-P": {}, "-s": {}, "-L": {}, "-a": {}, "-d": {}, "-E": {}, "-e": {}},
	"env":     {"-u": {}, "-C": {}, "-S": {}},
	"nice":    {"-n": {}, "--adjustment": {}},
	"timeout": {"-k": {}, "-s": {}, "--signal": {}, "--kill-after": {}},
	"sudo":    {"-u": {}, "-g": {}, "-p": {}, "-C": {}, "-T": {}, "-h": {}, "-r": {}, "-t": {}, "-D": {}, "-R": {}},
	"watch":   {"-n": {}, "-d": {}, "--interval": {}, "--differences": {}},
	"stdbuf":  {"-i": {}, "-o": {}, "-e": {}, "--input": {}, "--output": {}, "--error": {}},
}

// classifyWrappedCommand strips a wrapper's own options and classifies the
// command it runs. Options it cannot account for end the strip, leaving the
// wrapper's own name as the command - which is Unproven, never ReadOnly.
func classifyWrappedCommand(name string, args []string, allowedRoots []string, cwd string) ShellMutation {
	values := shellWrapperValueFlags[name]
	i := 0
	for i < len(args) {
		a := strings.TrimSpace(args[i])
		switch {
		case a == "" || a == "--":
			i++
		case a == "-" || (name == "env" && shellEnvAssignment(a)):
			// `env -` clears the environment, `env NAME=value` sets it.
			i++
		case strings.HasPrefix(a, "-") && len(a) > 1:
			if _, ok := values[a]; ok {
				i += 2
				continue
			}
			i++
		default:
			goto stripped
		}
	}
stripped:
	if i > len(args) {
		i = len(args)
	}
	rest := args[i:]
	if len(rest) == 0 {
		switch name {
		case "env", "xargs":
			// `env` prints the environment; `xargs` with no utility runs echo.
			return ShellMutationReadOnly
		}
		return ShellMutationUnproven
	}
	if name == "timeout" {
		// The duration precedes the command; without one there is nothing run.
		if len(rest) < 2 {
			return ShellMutationUnproven
		}
		rest = rest[1:]
	}
	return classifyCommandWords(rest, allowedRoots, cwd)
}

// classifyGitCommand handles git. Global options that change which repository
// or which program runs are accepted only where they can be bounded: -C must
// name a path inside an allowed root, and anything else an allowlist does not
// cover leaves the command Unproven.
func classifyGitCommand(args []string, allowedRoots []string, cwd string) ShellMutation {
	i := 0
	for i < len(args) {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "-C":
			if i+1 >= len(args) || !gitPathUnderAllowedRoots(args[i+1], allowedRoots, cwd) {
				return ShellMutationUnproven
			}
			i += 2
		case strings.HasPrefix(arg, "-C") && len(arg) > 2:
			if !gitPathUnderAllowedRoots(arg[len("-C"):], allowedRoots, cwd) {
				return ShellMutationUnproven
			}
			i++
		case arg == "--no-pager" || arg == "-P":
			i++
		default:
			goto subcommand
		}
	}
subcommand:
	if i >= len(args) {
		return ShellMutationUnproven
	}
	sub := strings.ToLower(strings.TrimSpace(args[i]))
	subArgs := args[i+1:]
	for _, arg := range subArgs {
		if arg == "-C" || (strings.HasPrefix(arg, "-C") && len(arg) > 2) {
			// A -C after the subcommand means the command this classifier read
			// is not the command git runs.
			return ShellMutationUnproven
		}
	}
	switch sub {
	case "status", "diff", "log", "show", "rev-parse", "ls-files", "grep", "ls-tree", "cat-file",
		"blame", "describe", "shortlog", "for-each-ref", "rev-list", "merge-base", "name-rev",
		"diff-tree", "show-ref", "ls-remote", "count-objects", "verify-pack", "whatchanged", "reflog":
		return gitReadOnlyArgs(subArgs, allowedRoots, cwd)
	case "--version", "version", "--help", "-h", "help":
		return ShellMutationReadOnly
	case "branch":
		return classifyGitBranch(subArgs)
	case "worktree":
		if len(subArgs) > 0 && strings.EqualFold(strings.TrimSpace(subArgs[0]), "list") {
			return gitReadOnlyArgs(subArgs[1:], allowedRoots, cwd)
		}
		return ShellMutationWrites
	case "stash":
		if len(subArgs) > 0 && strings.EqualFold(strings.TrimSpace(subArgs[0]), "list") {
			return gitReadOnlyArgs(subArgs[1:], allowedRoots, cwd)
		}
		return ShellMutationWrites
	case "tag":
		return classifyGitTag(subArgs)
	case "config":
		return classifyGitConfig(subArgs)
	case "remote":
		return classifyGitRemote(subArgs)
	case "notes":
		if len(subArgs) > 0 && strings.EqualFold(strings.TrimSpace(subArgs[0]), "list") {
			return gitReadOnlyArgs(subArgs[1:], allowedRoots, cwd)
		}
		return ShellMutationWrites
	case "symbolic-ref":
		refArgs := make([]string, 0, len(subArgs))
		for _, arg := range subArgs {
			if strings.HasPrefix(strings.TrimSpace(arg), "-") {
				continue
			}
			refArgs = append(refArgs, arg)
		}
		if len(refArgs) > 1 {
			// "git symbolic-ref HEAD refs/heads/x" sets the ref.
			return ShellMutationWrites
		}
		return gitReadOnlyArgs(subArgs, allowedRoots, cwd)
	}
	switch sub {
	case "add", "commit", "checkout", "switch", "restore", "reset", "rm", "mv", "merge", "rebase",
		"cherry-pick", "revert", "clean", "apply", "am", "push", "fetch", "pull", "clone", "init",
		"gc", "prune", "submodule", "replace", "update-ref", "filter-branch", "sparse-checkout",
		"maintenance", "pack-refs", "repack", "fsck":
		return ShellMutationWrites
	}
	return ShellMutationUnproven
}

// gitReadOnlyArgs rejects the git options that run another program, then
// applies the shared path check.
func gitReadOnlyArgs(args []string, allowedRoots []string, cwd string) ShellMutation {
	for _, arg := range args {
		a := strings.ToLower(strings.TrimSpace(arg))
		switch {
		case a == "--output" || strings.HasPrefix(a, "--output="),
			a == "--ext-diff", a == "--textconv", a == "--exec", strings.HasPrefix(a, "--exec="):
			return ShellMutationUnproven
		}
	}
	return readOnlyArgsUnderRoots(args, allowedRoots, cwd)
}

func classifyGitBranch(args []string) ShellMutation {
	allowed := map[string]struct{}{
		"-a": {}, "--all": {}, "-r": {}, "--remotes": {}, "-v": {}, "-vv": {}, "--verbose": {},
		"--show-current": {}, "--merged": {}, "--no-merged": {}, "--contains": {}, "--no-contains": {},
		"--points-at": {}, "--list": {}, "-l": {}, "--color": {}, "--no-color": {}, "--ignore-case": {},
		"--no-column": {}, "--no-abbrev": {}, "--column": {},
	}
	allowedPrefixes := []string{"--format=", "--sort=", "--color=", "--abbrev=", "--column="}
	for _, arg := range args {
		a := strings.ToLower(strings.TrimSpace(arg))
		if a == "" {
			continue
		}
		if _, ok := allowed[a]; ok {
			continue
		}
		if strings.HasPrefix(a, "--") {
			matched := false
			for _, prefix := range allowedPrefixes {
				if strings.HasPrefix(a, prefix) {
					matched = true
					break
				}
			}
			if matched {
				continue
			}
		}
		// Anything else, including a branch name, creates or deletes a ref.
		return ShellMutationWrites
	}
	return ShellMutationReadOnly
}

func classifyGitTag(args []string) ShellMutation {
	readOnlyFlags := map[string]struct{}{
		"-l": {}, "--list": {}, "-n": {}, "--contains": {}, "--no-contains": {}, "--points-at": {},
		"--merged": {}, "--no-merged": {}, "--column": {}, "--no-column": {}, "--ignore-case": {},
		"--omit-empty": {}, "--sort-version": {},
	}
	listing := false
	for _, arg := range args {
		a := strings.ToLower(strings.TrimSpace(arg))
		if a == "" {
			continue
		}
		if _, ok := readOnlyFlags[a]; ok {
			listing = true
			continue
		}
		if strings.HasPrefix(a, "-") {
			switch {
			case strings.HasPrefix(a, "--sort="), strings.HasPrefix(a, "--format="),
				strings.HasPrefix(a, "--contains="), strings.HasPrefix(a, "--points-at="),
				strings.HasPrefix(a, "--merged="), strings.HasPrefix(a, "--no-merged="),
				strings.HasPrefix(a, "--column="):
				listing = true
				continue
			}
			// -d, -a, -s, -m and -f all change the tags.
			return ShellMutationWrites
		}
		if !listing {
			// A bare tag name creates the tag.
			return ShellMutationWrites
		}
	}
	return ShellMutationReadOnly
}

func classifyGitConfig(args []string) ShellMutation {
	readOnlyFlags := map[string]struct{}{
		"--get": {}, "--get-all": {}, "--get-regexp": {}, "--get-urlmatch": {}, "--list": {}, "-l": {},
		"--local": {}, "--global": {}, "--system": {}, "--worktree": {}, "--show-origin": {},
		"--show-scope": {}, "--show-names": {}, "--null": {}, "-z": {}, "--includes": {},
		"--no-includes": {}, "--fixed-value": {}, "--name-only": {},
	}
	valueFlags := map[string]struct{}{"--type": {}, "--default": {}, "--file": {}, "-f": {}}
	operands := 0
	for i := 0; i < len(args); i++ {
		a := strings.ToLower(strings.TrimSpace(args[i]))
		if a == "" {
			continue
		}
		if _, ok := readOnlyFlags[a]; ok {
			continue
		}
		if _, ok := valueFlags[a]; ok {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			switch {
			case strings.HasPrefix(a, "--get"), strings.HasPrefix(a, "--list"),
				strings.HasPrefix(a, "--show-"), strings.HasPrefix(a, "--type="),
				strings.HasPrefix(a, "--default="), strings.HasPrefix(a, "--file="):
				continue
			}
			// -e/--edit, --add, --unset, --replace-all and friends all write.
			return ShellMutationWrites
		}
		operands++
	}
	if operands > 1 {
		// Two operands are a key and the value to store.
		return ShellMutationWrites
	}
	return ShellMutationReadOnly
}

func classifyGitRemote(args []string) ShellMutation {
	if len(args) == 0 {
		return ShellMutationReadOnly
	}
	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "-v", "--verbose", "show", "get-url":
		return ShellMutationReadOnly
	case "add", "remove", "rm", "rename", "set-url", "set-head", "set-branches", "prune", "update":
		return ShellMutationWrites
	}
	return ShellMutationUnproven
}

// classifySedCommand handles sed, whose only in-place form is -i but whose
// script can write a file through the w/W command.
func classifySedCommand(args []string, allowedRoots []string, cwd string) ShellMutation {
	scripts := make([]string, 0, 1)
	files := make([]string, 0, len(args))
	explicitScript := false
	positional := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, args[i:]...)
			break
		}
		lower := strings.ToLower(a)
		switch {
		case lower == "-i" || strings.HasPrefix(lower, "-i"):
			return ShellMutationWrites
		case lower == "--in-place" || strings.HasPrefix(lower, "--in-place="):
			return ShellMutationWrites
		case lower == "-f" || lower == "--file" || strings.HasPrefix(lower, "--file="):
			// The program lives in a file this classifier cannot read.
			return ShellMutationUnproven
		case lower == "-e" || lower == "--expression":
			if i+1 >= len(args) {
				return ShellMutationUnproven
			}
			i++
			scripts = append(scripts, args[i])
			explicitScript = true
		case strings.HasPrefix(lower, "--expression="):
			scripts = append(scripts, strings.TrimSpace(a[len("--expression="):]))
			explicitScript = true
		case strings.HasPrefix(lower, "-e") && len(a) > 2:
			scripts = append(scripts, strings.TrimSpace(a[2:]))
			explicitScript = true
		case lower == "-l" || lower == "--line-length":
			// The next word is the option's value, not a script.
			i++
		case sedBooleanFlag(lower):
		default:
			// An option this table does not know may take a value, which would
			// leave the script position unknown.
			return ShellMutationUnproven
		}
	}
	if !explicitScript {
		if len(positional) == 0 {
			return ShellMutationUnproven
		}
		scripts = append(scripts, positional[0])
		positional = positional[1:]
	}
	files = append(files, positional...)
	for _, script := range scripts {
		switch sedScriptTier(script) {
		case ShellMutationWrites:
			return ShellMutationWrites
		case ShellMutationUnproven:
			return ShellMutationUnproven
		}
	}
	return readOnlyArgsUnderRoots(files, allowedRoots, cwd)
}

// sedBooleanFlag reports whether a sed option is one this table knows takes no
// value.
func sedBooleanFlag(flag string) bool {
	switch flag {
	case "-n", "--quiet", "--silent", "-e", "-E", "-r", "--regexp-extended", "-s", "--separate",
		"-u", "--unbuffered", "-z", "--null-data", "--zero-terminated", "--posix", "--debug",
		"--sandbox", "--follow-symlinks", "-b", "--binary", "--help", "--version":
		return true
	}
	return false
}

// sedScriptTier reports what a sed script text can do: a `w` or `W` command,
// including the "s///w file" flag form, writes a file; an `e` command runs its
// argument as a shell command. Both are read here rather than guessed at, so a
// script that only prints stays read-only however it is spelled.
func sedScriptTier(script string) ShellMutation {
	runes := []rune(script)
	for i := 0; i < len(runes); {
		ch := runes[i]
		switch {
		case ch == '\\':
			i += 2
		case ch == '/':
			// An address regex is not a command.
			next, ok := skipSedDelimited(runes, i, '/')
			if !ok {
				return ShellMutationUnproven
			}
			i = next
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == ';' || ch == '{' || ch == '}' || ch == '!':
			i++
		case ch == '#':
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
		case ch >= '0' && ch <= '9', ch == '$', ch == '~', ch == ',', ch == '+', ch == '=':
			i++
		case ch == 's' || ch == 'y':
			tier, next := sedSubstitutionTier(runes, i)
			if tier != ShellMutationReadOnly {
				return tier
			}
			i = next
		case ch == 'w' || ch == 'W':
			return ShellMutationWrites
		case ch == 'e':
			return ShellMutationUnproven
		case ch == 'r' || ch == 'R' || ch == 'b' || ch == 't' || ch == 'T' || ch == ':',
			ch == 'a' || ch == 'i' || ch == 'c' || ch == 'q' || ch == 'Q', ch == 'l' || ch == 'L':
			// These carry a filename, a label or literal text to the end of
			// the statement; none of it is a command.
			i = skipSedStatement(runes, i+1)
		default:
			i++
		}
	}
	return ShellMutationReadOnly
}

// sedSubstitutionTier reads one s/// or y/// command, returning the tier of its
// flags and the index just past it. A delimiter this scanner cannot follow makes
// the whole script Unproven.
func sedSubstitutionTier(runes []rune, i int) (ShellMutation, int) {
	j := i + 1
	if j >= len(runes) {
		return ShellMutationUnproven, len(runes)
	}
	delim := runes[j]
	switch delim {
	case '\\', '\n', '\r', ' ', '\t', ';':
		return ShellMutationUnproven, len(runes)
	}
	afterFirst, ok := skipSedDelimited(runes, j, delim)
	if !ok {
		return ShellMutationUnproven, len(runes)
	}
	// The delimiter that closed the first field opens the second.
	afterSecond, ok := skipSedDelimited(runes, afterFirst-1, delim)
	if !ok {
		return ShellMutationUnproven, len(runes)
	}
	if runes[i] == 'y' {
		// y takes exactly two fields and no flags.
		return ShellMutationReadOnly, afterSecond
	}
	for k := afterSecond; k < len(runes); k++ {
		switch runes[k] {
		case ' ', '\t', '\n', '\r', ';', '}':
			return ShellMutationReadOnly, k
		case 'w', 'W':
			// "s///w file" writes the result to a file.
			return ShellMutationWrites, skipSedStatement(runes, k+1)
		case 'e':
			// The e flag runs the result as a command.
			return ShellMutationUnproven, skipSedStatement(runes, k+1)
		}
	}
	return ShellMutationReadOnly, len(runes)
}

// skipSedDelimited advances past the field opened by runes[i] == delim and its
// closing delimiter, returning the index just past it. A delimiter inside a
// bracket expression does not close the field. An unterminated field reports
// ok == false, which leaves the script Unproven rather than guessed at.
func skipSedDelimited(runes []rune, i int, delim rune) (int, bool) {
	j := i + 1
	inBracket := false
	for j < len(runes) {
		ch := runes[j]
		if ch == '\\' {
			j += 2
			continue
		}
		if delim == '/' {
			switch ch {
			case '[':
				inBracket = true
			case ']':
				inBracket = false
			}
		}
		if ch == delim && !inBracket {
			return j + 1, true
		}
		j++
	}
	return j, false
}

// skipSedStatement advances past the rest of the current sed statement, which
// is literal text (a filename, a label, appended lines) rather than commands.
func skipSedStatement(runes []rune, i int) int {
	for i < len(runes) && runes[i] != '\n' && runes[i] != '\r' && runes[i] != ';' {
		i++
	}
	return i
}

// stripDevNullRedirects removes harmless /dev/null redirects before safety
// checks. Patterns like 2>/dev/null, >/dev/null, &>/dev/null (and append
// variants, with or without whitespace after the operator) discard output and
// don't make a command destructive.
func stripDevNullRedirects(cmd string) string {
	for _, pat := range []string{
		"2>> /dev/null", "2>>/dev/null",
		"2> /dev/null", "2>/dev/null",
		"1>> /dev/null", "1>>/dev/null",
		"1> /dev/null", "1>/dev/null",
		"&>> /dev/null", "&>>/dev/null",
		"&> /dev/null", "&>/dev/null",
		">> /dev/null", ">>/dev/null",
		"> /dev/null", ">/dev/null",
	} {
		cmd = strings.ReplaceAll(cmd, pat, "")
	}
	return strings.TrimSpace(cmd)
}

// SplitShellWords splits a command text into words the way the shell would for
// the constructs this package reasons about: quotes group and are removed, a
// backslash escapes the next rune, and unquoted whitespace separates words. No
// expansion is performed - a word that expands at run time stays one word here,
// which is exactly what a classifier that cannot run the expansion needs.
func SplitShellWords(s string) []string {
	out := make([]string, 0, 8)
	var cur strings.Builder
	inSingle := false
	inDouble := false
	escaped := false
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		out = append(out, cur.String())
		cur.Reset()
	}
	for _, ch := range s {
		if escaped {
			cur.WriteRune(ch)
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if (ch == ' ' || ch == '\t') && !inSingle && !inDouble {
			flush()
			continue
		}
		cur.WriteRune(ch)
	}
	flush()
	return out
}

// LooksLikeFilePath reports whether a bare shell word names a path, as opposed
// to a pattern, a flag value or an option. It is deliberately eager about the
// forms that walk the tree - "/x", "./x", "../x", "..", "a/b" - because callers
// use it to decide which arguments an allowed root has to bound.
func LooksLikeFilePath(s string) bool {
	if s == ".." {
		return true
	}
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return true
	}
	return strings.Contains(s, "/")
}

// fileArgPathsUnderAllowedRoots checks that non-flag args which look like file
// paths resolve to within an allowed root. Args that start with /, ./, .. or
// contain / are treated as paths; pure words (no slash, not absolute) are
// skipped since they're typically patterns or non-path arguments. An empty
// allowedRoots skips validation (no root to enforce).
func fileArgPathsUnderAllowedRoots(args []string, allowedRoots []string, cwd string) bool {
	roots := cleanShellAllowedRoots(allowedRoots)
	if len(roots) == 0 {
		return true
	}
	for _, arg := range args {
		a := strings.TrimSpace(arg)
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		if !LooksLikeFilePath(a) {
			continue
		}
		var resolved string
		if filepath.IsAbs(a) {
			resolved = filepath.Clean(a)
		} else {
			cw := strings.TrimSpace(cwd)
			if cw == "" {
				continue
			}
			resolved = filepath.Clean(filepath.Join(cw, a))
		}
		if !pathUnderAnyShellRoot(resolved, roots) {
			return false
		}
	}
	return true
}

// cdPathUnderAllowedRoots checks whether a cd target path is under an allowed
// root. Absolute paths are resolved directly. Relative paths are resolved
// against cwd. Empty args (cd to $HOME), cd -, and cd ~user resolve outside
// allowed roots. When allowedRoots is empty there is no workspace root to
// validate against - allow.
func cdPathUnderAllowedRoots(args []string, allowedRoots []string, cwd string) bool {
	roots := cleanShellAllowedRoots(allowedRoots)
	if len(roots) == 0 {
		return true
	}
	// No argument → cd to $HOME → outside allowed roots.
	if len(args) == 0 {
		return false
	}
	target := strings.TrimSpace(args[0])
	if target == "" || target == "-" {
		return false
	}
	if strings.HasPrefix(target, "~") || strings.HasPrefix(target, "$") {
		return false
	}
	var resolved string
	if filepath.IsAbs(target) {
		resolved = filepath.Clean(target)
	} else {
		cw := strings.TrimSpace(cwd)
		if cw == "" {
			return false
		}
		resolved = filepath.Clean(filepath.Join(cw, target))
	}
	return pathUnderAnyShellRoot(resolved, roots)
}

// gitPathUnderAllowedRoots checks a path git was told to work in, with the same
// rule as the other path checks: it has to resolve inside an allowed root.
func gitPathUnderAllowedRoots(path string, allowedRoots []string, cwd string) bool {
	path = strings.TrimSpace(path)
	if path == "" || strings.HasPrefix(path, "-") {
		return false
	}
	roots := cleanShellAllowedRoots(allowedRoots)
	if len(roots) == 0 {
		return false
	}
	var resolved string
	if filepath.IsAbs(path) {
		resolved = filepath.Clean(path)
	} else {
		if strings.TrimSpace(cwd) == "" {
			return false
		}
		resolved = filepath.Clean(filepath.Join(cwd, path))
	}
	return pathUnderAnyShellRoot(resolved, roots)
}

// cleanShellAllowedRoots normalizes the roots a path check compares against.
//
// Each root is also carried in its fully symlink-resolved form. The two forms
// exist because the roots and the command's working directory do not arrive the
// same way: a root is resolved once when the tool state is built (macOS turns
// /var/folders/... into /private/var/folders/...), while the working directory
// comes from the process and keeps the path the shell was started with. A plain
// string comparison of the two therefore fails for every relative path argument
// in a directory reachable by two names, which is most of macOS.
func cleanShellAllowedRoots(allowedRoots []string) []string {
	cleaned := make([]string, 0, len(allowedRoots))
	for _, root := range allowedRoots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		cleaned = append(cleaned, root)
		if resolved := resolveShellPath(root); resolved != root {
			cleaned = append(cleaned, resolved)
		}
	}
	return cleaned
}

// pathUnderAnyShellRoot reports whether path resolves inside one of the roots,
// in whichever of the two forms each side is spelled.
func pathUnderAnyShellRoot(path string, roots []string) bool {
	path = filepath.Clean(path)
	forms := []string{path}
	if resolved := resolveShellPath(path); resolved != path {
		forms = append(forms, resolved)
	}
	for _, form := range forms {
		for _, root := range roots {
			if form == root || strings.HasPrefix(form, root+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

// resolveShellPath expands the symlinks in a path and returns the real location,
// or the cleaned path unchanged when it cannot be resolved.
//
// A path check cannot require the whole path to exist - the argument is often a
// file the command is about to read, or one that is absent - so it resolves the
// deepest ancestor that does exist and re-appends what follows. Without that, a
// root such as /private/var/... could never match the path /var/.../new-file
// spelled under the same directory.
func resolveShellPath(path string) string {
	path = filepath.Clean(path)
	remainder := ""
	for probe := path; ; {
		if resolved, err := filepath.EvalSymlinks(probe); err == nil {
			if remainder == "" {
				return resolved
			}
			return filepath.Join(resolved, remainder)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return path
		}
		remainder = filepath.Join(filepath.Base(probe), remainder)
		probe = parent
	}
}

// shellEnvAssignment reports whether a token is an environment assignment
// ("NAME=value") rather than a command or an argument.
func shellEnvAssignment(token string) bool {
	if token == "" {
		return false
	}
	eq := strings.IndexByte(token, '=')
	if eq <= 0 {
		return false
	}
	key := token[:eq]
	for i, ch := range key {
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || ch == '_' {
			continue
		}
		if i > 0 && ch >= '0' && ch <= '9' {
			continue
		}
		return false
	}
	return true
}
