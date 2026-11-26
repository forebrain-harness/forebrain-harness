package safety

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// A remembered "don't ask again for commands that start with ..." approval
// persists an allow rule carrying BypassSandbox. A broader rule added earlier
// without that capability must not shadow it: the command would be allowed but
// still sandboxed, the sandbox would deny it, and the escalation path would ask
// for approval again on every call.
func TestEngineAllowRuleSandboxBypassSurvivesEarlierMatchingRule(t *testing.T) {
	s := NewStore()
	s.AddRule(SourceLocalSettings, BehaviorAllow, PermissionRuleValue{ToolName: "Bash", RuleContent: "go build:*"})
	s.AddRule(SourceLocalSettings, BehaviorAllow, PermissionRuleValue{
		ToolName: "Bash", CommandPrefix: []string{"go", "build"}, BypassSandbox: true,
	})

	d := NewEngine().Evaluate(s, "Bash", "go build ./...")
	if d.Behavior != BehaviorAllow {
		t.Fatalf("behavior = %q, want allow", d.Behavior)
	}
	if !d.BypassSandbox {
		t.Fatal("remembered approval lost its sandbox bypass to an earlier matching rule")
	}
}

// The earlier rule still owns the decision when no matching rule grants a
// bypass, so an allow rule never gains a capability nobody granted.
func TestEngineAllowRuleWithoutBypassStaysSandboxed(t *testing.T) {
	s := NewStore()
	s.AddRule(SourceLocalSettings, BehaviorAllow, PermissionRuleValue{ToolName: "Bash", RuleContent: "go build:*"})
	s.AddRule(SourceLocalSettings, BehaviorAllow, PermissionRuleValue{
		ToolName: "Bash", CommandPrefix: []string{"go", "test"}, BypassSandbox: true,
	})

	d := NewEngine().Evaluate(s, "Bash", "go build ./...")
	if d.Behavior != BehaviorAllow || d.BypassSandbox {
		t.Fatalf("decision = %+v, want allow without bypass", d)
	}
}

// A known-safe clause needs no rule of its own, so one `cd <dir> &&` in front of
// the real work no longer defeats a remembered approval for that work.
func TestEngineKnownSafeClauseDoesNotDefeatAllowRule(t *testing.T) {
	s := NewStore()
	s.AddRule(SourceLocalSettings, BehaviorAllow, PermissionRuleValue{
		ToolName: "Bash", CommandPrefix: []string{"go", "test"}, BypassSandbox: true,
	})
	e := NewEngine()

	for _, command := range []string{
		`cd /repo && go test ./internal/gateway/... -v -run "Approval"`,
		"pwd && go test ./...",
		"cd /repo && git status && go test ./...",
	} {
		d := e.Evaluate(s, "Bash", command)
		if d.Behavior != BehaviorAllow || !d.BypassSandbox {
			t.Errorf("%s: decision = %+v (reason %q), want allow with bypass", command, d, d.Reason)
		}
	}
}

// Only clauses that are known-safe carry themselves. Anything else still needs
// its own allow rule.
func TestEngineKnownSafeClauseDoesNotCarryUnapprovedWork(t *testing.T) {
	s := NewStore()
	s.AddRule(SourceLocalSettings, BehaviorAllow, PermissionRuleValue{
		ToolName: "Bash", CommandPrefix: []string{"go", "test"}, BypassSandbox: true,
	})

	d := NewEngine().Evaluate(s, "Bash", "cd /repo && go test ./... && rm -rf /repo/dist")
	if d.Matched != nil {
		t.Fatalf("unapproved clause matched rule %+v", d.Matched.Value)
	}
}

// Ask and deny rules are evaluated before allow rules: a restriction on any
// clause still wins, including on a clause the allow path would have skipped.
func TestEngineKnownSafeClauseKeepsRestrictions(t *testing.T) {
	s := NewStore()
	s.AddRule(SourceLocalSettings, BehaviorAllow, PermissionRuleValue{
		ToolName: "Bash", CommandPrefix: []string{"go", "test"}, BypassSandbox: true,
	})
	s.AddRule(SourceProjectSettings, BehaviorDeny, PermissionRuleValue{ToolName: "Bash", CommandPrefix: []string{"cd"}})

	if d := NewEngine().Evaluate(s, "Bash", "cd /repo && go test ./..."); d.Behavior != BehaviorDeny {
		t.Fatalf("behavior = %q, want deny", d.Behavior)
	}
}

// The amendment names the clause that actually needs the approval, so a
// compound command can be remembered at all.
func TestGetSimpleCommandPrefixNamesTheClauseNeedingApproval(t *testing.T) {
	for _, tc := range []struct{ command, want string }{
		{"cd /repo && go test ./internal/gateway/...", "go test"},
		{"pwd && npm run build", "npm run build"},
		{"cd /repo && git status && npm run build", "npm run build"},
		{"go test ./internal/gateway/...", "go test"},
	} {
		got, ok := GetSimpleCommandPrefix(tc.command)
		if !ok || got != tc.want {
			t.Errorf("GetSimpleCommandPrefix(%q) = %q, %v; want %q, true", tc.command, got, ok, tc.want)
		}
	}

	amendment := proposedExecPolicyAmendment(ToolApprovalSuggestion{PrefixRuleContent: "go test ./x:*"})
	if !slices.Equal(amendment, ExecPolicyAmendment{"go", "test", "./x"}) {
		t.Fatalf("amendment = %#v", []string(amendment))
	}
}

func TestGetSimpleCommandPrefixRefusesUnderivableCommands(t *testing.T) {
	for _, command := range []string{
		// The clause needing approval still cannot carry a substitution or a
		// redirection into a reusable rule.
		"cd $(mktemp -d) && rm -rf .",
		"go test ./... > /tmp/out",
		"cat <<'EOF'\nbody\nEOF",
	} {
		if got, ok := GetSimpleCommandPrefix(command); ok {
			t.Errorf("GetSimpleCommandPrefix(%q) = %q, true; want no prefix", command, got)
		}
	}
}

// A rule must cover everything the command does. When two clauses would each
// need approval, naming one of them would describe less than what runs.
func TestGetSimpleCommandPrefixRefusesToNameOnlyPartOfTheWork(t *testing.T) {
	for _, command := range []string{
		"gofmt -w internal/foo.go && rm -rf /tmp/x",
		"cargo install cargo-insta && rm -rf /tmp/example",
		"cd /repo && npm run build && rm -rf dist",
	} {
		if got, ok := GetSimpleCommandPrefix(command); ok {
			t.Errorf("GetSimpleCommandPrefix(%q) = %q, true; want no prefix", command, got)
		}
	}
}

// A model-supplied prefix has to survive the compound form the model actually
// emits; requiring it to prefix the whole command string rejected all of them.
func TestModelSuppliedPrefixCoversCompoundCommands(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		prefix        []any
		want          string
	}{
		{
			name:    "leading directory change",
			command: "cd /repo && go test ./internal/gateway/...",
			prefix:  []any{"go", "test"},
			want:    "go test:*",
		},
		{
			name:    "known-safe clause before the work",
			command: "pwd && cargo build --release",
			prefix:  []any{"cargo", "build"},
			want:    "cargo build:*",
		},
		{
			name:    "simple command still works",
			command: "cargo install cargo-insta",
			prefix:  []any{"cargo", "install"},
			want:    "cargo install:*",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildToolApprovalSuggestion("shell", map[string]any{
				"command":             tc.command,
				"sandbox_permissions": "require_escalated",
				"prefix_rule":         tc.prefix,
			})
			if got.PrefixRuleContent != tc.want {
				t.Fatalf("prefix rule = %q, want %q", got.PrefixRuleContent, tc.want)
			}
		})
	}
}

// The stored rule must authorize everything the command does, so a clause the
// prefix does not cover and that is not known-safe rejects the proposal.
func TestModelSuppliedPrefixRejectedWhenItLeavesWorkUncovered(t *testing.T) {
	for _, tc := range []struct {
		name, command, notWant string
		prefix                 []any
	}{
		{"uncovered destructive clause", "cargo install cargo-insta && rm -rf /tmp/example", "cargo install:*", []any{"cargo", "install"}},
		{"uncovered clause after a directory change", "cd /repo && npm run build && rm -rf dist", "npm run:*", []any{"npm", "run"}},
		{"prefix matches no clause at all", "cd /repo && go test ./...", "cargo build:*", []any{"cargo", "build"}},
		{"redirection cannot be covered by a prefix", "go test ./... > /tmp/out", "go test:*", []any{"go", "test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildToolApprovalSuggestion("shell", map[string]any{
				"command":             tc.command,
				"sandbox_permissions": "require_escalated",
				"prefix_rule":         tc.prefix,
			})
			if got.PrefixRuleContent == tc.notWant {
				t.Fatalf("model prefix adopted despite uncovered work: %q", got.PrefixRuleContent)
			}
		})
	}
}

func TestShellRuleMatches(t *testing.T) {
	if !ShellRuleMatches("npm run build", "npm run build") {
		t.Fatalf("exact match failed")
	}
	if !ShellRuleMatches("git commit:*", `git commit -m "x"`) {
		t.Fatalf("prefix match failed")
	}
	if ShellRuleMatches("git commit:*", "git status") {
		t.Fatalf("prefix should not match different command")
	}
	if !ShellRuleMatches("npm*build", "npm run build") {
		t.Fatalf("wildcard match failed")
	}
	if ShellRuleMatches(`npm\*build`, "npm run build") {
		t.Fatalf("escaped star should be literal")
	}
}

func TestShellRuleGofmtPrefix(t *testing.T) {
	if !ShellRuleMatches("gofmt:*", "gofmt -w internal/foo.go") {
		t.Fatal("gofmt prefix should match a gofmt invocation")
	}
	if ShellRuleMatches("gofmt:*", "gofmt-check internal/foo.go") {
		t.Fatal("gofmt prefix must preserve the executable boundary")
	}
	if ShellRuleMatches("gofmt:*", "gofmt -w internal/foo.go; rm -rf /tmp/x") {
		t.Fatal("gofmt prefix must reject an uncovered compound command")
	}
}

func TestShellRulePrefixRejectsCompoundCommand(t *testing.T) {
	compounds := []string{
		"git status && rm -rf /tmp/x",
		"git status || rm -rf /tmp/x",
		"git status; rm -rf /tmp/x",
		"git status | grep foo",
		"git status & sleep 1",
		"git status `whoami`",
		"git status $(whoami)",
		"git status ${HOME}",
	}
	for _, cmd := range compounds {
		if ShellRuleMatches("git status:*", cmd) {
			t.Fatalf("prefix rule incorrectly matched compound %q", cmd)
		}
	}
}

func TestShellRulePrefixAllowsQuotedOperators(t *testing.T) {
	if !ShellRuleMatches("echo:*", `echo "a && b"`) {
		t.Fatal("quoted operators must not block prefix match")
	}
	if !ShellRuleMatches("echo:*", `echo 'a | b'`) {
		t.Fatal("single-quoted operators must not block prefix match")
	}
}

func TestShellRuleWildcardRejectsCompoundCommand(t *testing.T) {
	compounds := []string{
		"git status && rm -rf /tmp/x",
		"git status || rm -rf /tmp/x",
		"git status; rm -rf /tmp/x",
		"git status | grep foo",
		"git status & sleep 1",
		"git status `whoami`",
		"git status $(whoami)",
		"git status\nrm -rf /tmp/x",
	}
	for _, cmd := range compounds {
		if ShellRuleMatches("git *", cmd) {
			t.Fatalf("wildcard rule incorrectly matched compound %q", cmd)
		}
	}
}

func TestShellRulePrefixRejectsRedirectFallback(t *testing.T) {
	if ShellRuleMatches("sh:*", "sh -c 'rm' && rm -rf /") {
		t.Fatal("fallback prefix path must reject compound")
	}
}

func TestShellRuleAllowsCleanClauseInWildcard(t *testing.T) {
	if !ShellRuleMatches("git *", "git status") {
		t.Fatal("wildcard must still match clean clause")
	}
	if !ShellRuleMatches("git *", `git commit -m "a && b"`) {
		t.Fatal("quoted operators must not break wildcard match")
	}
}

func TestShellRuleAllPrefixClausesMustMatch(t *testing.T) {
	if ShellRuleMatches("git status:*", "git status; rm") {
		t.Fatal("prefix rule must reject when any clause fails")
	}
	if !ShellRuleMatches("git status:*", "git status -s") {
		t.Fatal("prefix rule must still allow single-clause match")
	}
}

func TestSplitShellClauses(t *testing.T) {
	cases := map[string][]string{
		"a && b":           {"a", "b"},
		"a || b":           {"a", "b"},
		"a; b; c":          {"a", "b", "c"},
		"a | b":            {"a", "b"},
		"a & b":            {"a", "b"},
		"a\nb":             {"a", "b"},
		`echo "a && b"`:    {`echo "a && b"`},
		`echo 'a | b' | c`: {`echo 'a | b'`, "c"},
		"":                 {},
	}
	for in, want := range cases {
		got := SplitShellClauses(in)
		if len(got) != len(want) {
			t.Fatalf("split %q: got %v want %v", in, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("split %q: got %v want %v", in, got, want)
			}
		}
	}
}

func TestCommandHasControlOperator(t *testing.T) {
	if !commandHasControlOperator("a && b") {
		t.Fatal("&& not detected")
	}
	if !commandHasControlOperator("echo $(whoami)") {
		t.Fatal("$( not detected")
	}
	if !commandHasControlOperator(`echo "$(whoami)"`) {
		t.Fatal("double-quoted command substitution not detected")
	}
	if !commandHasControlOperator("echo \"`whoami`\"") {
		t.Fatal("double-quoted backtick substitution not detected")
	}
	if commandHasControlOperator(`echo "a$b"`) {
		t.Fatal("plain $ in double quotes should not trigger")
	}
	if commandHasControlOperator("echo plain") {
		t.Fatal("clean command flagged as compound")
	}
}

func TestShellRulePrefixRejectsRedirectedCommand(t *testing.T) {
	for _, command := range []string{
		"npm run build > /tmp/out.log",
		`git commit -m "x" > /dev/null 2>&1`,
		"cargo test 2>/dev/null",
		"go test ./... >&out.log",
	} {
		if ShellRuleMatches("go test:*", command) || ShellRuleMatches("npm run:*", command) ||
			ShellRuleMatches("git commit:*", command) || ShellRuleMatches("cargo test:*", command) {
			t.Fatalf("prefix rule must not match redirected command %q", command)
		}
	}
}

// Redirecting stderr onto stdout is how the model asks for the output it is
// about to read, so it appears on nearly every command it runs. It names no
// file and no second command, which is what a prefix rule has to withhold, so
// a remembered prefix keeps covering the command it was derived from.
func TestShellRulePrefixMatchesDescriptorDuplication(t *testing.T) {
	for _, command := range []string{
		"go test ./... 2>&1",
		"go test ./pkg/tui -run TestX 2>&1",
		"go test ./... 1>&2",
		"go test ./... 2>&-",
	} {
		if !ShellRuleMatches("go test:*", command) {
			t.Fatalf("prefix rule must match %q", command)
		}
	}
}

// "2>&1" is a redirection operator, not the "&" that separates two commands.
// Splitting it produces a phantom clause that is not known-safe and that no
// rule can ever match, which is what made a remembered approval stop covering
// the command it was granted for.
func TestSplitShellClausesKeepsRedirectionOperators(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"go test ./... 2>&1 | tail -5", []string{"go test ./... 2>&1", "tail -5"}},
		{"mvn clean install 2>&1", []string{"mvn clean install 2>&1"}},
		{"go build &> /tmp/out.log", []string{"go build &> /tmp/out.log"}},
		{"go build 1>&2 && go vet ./...", []string{"go build 1>&2", "go vet ./..."}},
		{"go build & go vet ./...", []string{"go build", "go vet ./..."}},
	}
	for _, tc := range cases {
		got := SplitShellClauses(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("SplitShellClauses(%q) = %q, want %q", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("SplitShellClauses(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
	}
}

func TestShellRuleExactDoesNotMatchRedirected(t *testing.T) {
	if ShellRuleMatches("go test ./...", "go test ./... 2>&1") {
		t.Fatal("exact rule should not match redirected variant")
	}
}

func TestShellRuleRejectsProcessSubstitution(t *testing.T) {
	cases := []struct {
		rule string
		cmd  string
	}{
		{"cat:*", "cat <(rm -rf /)"},
		{"git diff:*", "git diff <(curl evil)"},
		{"tee:*", "tee >(sh)"},
		{"cat *", "cat <(rm -rf /)"},
		{"cmd:*", "cmd 2>(logger)"},
	}
	for _, tc := range cases {
		if ShellRuleMatches(tc.rule, tc.cmd) {
			t.Errorf("rule %q must NOT match process-substitution command %q", tc.rule, tc.cmd)
		}
	}
}

func TestRestrictionRulesIgnoreLeadingEnvironmentAssignments(t *testing.T) {
	t.Parallel()

	if !commandPrefixMatchesRestriction([]string{"rm", "-rf"}, "PATH=/tmp DEBUG=1 rm -rf ./build") {
		t.Fatal("deny prefix was bypassed by leading environment assignments")
	}
	if !shellRuleMatchesRestriction(ParseShellRule("rm:*"), "PATH=/tmp rm -rf ./build") {
		t.Fatal("legacy deny prefix was bypassed by a leading environment assignment")
	}
	if !shellRuleMatchesRestriction(ParseShellRule(`rm "build output"`), `DEBUG=1 rm "build output"`) {
		t.Fatal("exact deny rule was bypassed by a leading environment assignment")
	}
	if !shellRuleMatchesRestriction(ParseShellRule(`rm "build output"`), `rm 'build output'`) {
		t.Fatal("exact deny rule was bypassed by equivalent shell quoting")
	}
	if commandPrefixMatchesRestriction([]string{"PATH=/safe", "rm"}, "PATH=/tmp rm ./build") {
		t.Fatal("an assignment-specific restriction matched a different assignment")
	}
}

func TestShellAssessmentUnknownCommandIsNotWrite(t *testing.T) {
	assessment := AssessShellCommand("go test ./internal/tui ./internal/permissions")
	if assessment.KnownSafe {
		t.Fatal("go test must remain outside known-safe auto-approval")
	}
	if assessment.UsedComplexParsing {
		t.Fatal("plain go test is not complex shell syntax")
	}
	if assessment.Dangerous != nil {
		t.Fatalf("go test should not be classified dangerous: %+v", assessment.Dangerous)
	}
}

func TestShellAssessmentSeparatesComplexityAndDanger(t *testing.T) {
	assessment := AssessShellCommand("echo $(rm -f /tmp/example)")
	if !assessment.UsedComplexParsing {
		t.Fatal("command substitution must fail the simple safety proof")
	}
	if assessment.KnownSafe {
		t.Fatal("complex syntax must never be known-safe")
	}
	if assessment.Dangerous == nil || assessment.Dangerous.Rule != "forced_rm" {
		t.Fatalf("expected forced rm dangerous match, got %+v", assessment.Dangerous)
	}
}

func TestShellAssessmentParsesSafeSequence(t *testing.T) {
	assessment := AssessShellCommand("pwd && git status --short")
	if !assessment.SimpleKnownSafe() {
		t.Fatalf("safe sequence was not assessed as simple known-safe: %+v", assessment)
	}
}

func TestShellAssessmentDoesNotTreatComplexLiteralExtractionAsSafe(t *testing.T) {
	assessment := AssessShellCommand("if true; then go test ./...; fi")
	if assessment.KnownSafe {
		t.Fatal("control flow must not be a known-safe proof")
	}
	if !assessment.UsedComplexParsing {
		t.Fatal("control flow must be marked complex")
	}
}

// The dangerous-command detector is not a blocklist: forced rm is the only
// intrinsic pattern. Its value is unwrapping — finding that rm however many
// layers of sudo/env/trap/sh -c it is buried under. Each layer is exercised
// alone and nested, because a wrapper that only worked at the outermost level
// would leave the nested form undetected.
func TestDangerousCommandUnwrapsWrappers(t *testing.T) {
	dangerous := []string{
		"rm -rf /tmp/x",
		"rm --force /tmp/x",
		"sudo rm -rf /tmp/x",
		"env A=1 rm -rf /tmp/x",
		"env -i rm -rf /tmp/x",
		"trap 'rm -rf /tmp/x' EXIT",
		"trap -- 'rm -rf /tmp/x' EXIT",
		"sh -c 'rm -rf /tmp/x'",
		"sh -lc 'rm -rf /tmp/x'",
		"bash -c \"rm -rf /tmp/x\"",
		"/bin/sh -c 'rm -rf /tmp/x'",
		"sudo sh -c 'rm -rf /tmp/x'",
		"env A=1 bash -c 'rm -rf /tmp/x'",
		"sh -c 'sh -c \"rm -rf /tmp/x\"'",
	}
	for _, command := range dangerous {
		if AssessShellCommand(command).Dangerous == nil {
			t.Errorf("%q should be flagged dangerous", command)
		}
	}

	safe := []string{
		"rm /tmp/x",
		"git status",
		"sh",
		"sh -c 'echo hi'",
		"sudo apt-get install foo",
		"trap - EXIT",
	}
	for _, command := range safe {
		if m := AssessShellCommand(command).Dangerous; m != nil {
			t.Errorf("%q should not be flagged dangerous, got %+v", command, m)
		}
	}
}

func TestSplitTopLevelWithSeparators(t *testing.T) {
	type want struct {
		text string
		sep  string
	}
	cases := []struct {
		name string
		in   string
		out  []want
	}{
		{"single", "git status", []want{{"git status", SepNone}}},
		{"and", "make && make test", []want{{"make", SepAnd}, {"make test", SepNone}}},
		{"or", "a || b", []want{{"a", SepOr}, {"b", SepNone}}},
		{"pipe", "ls | wc -l", []want{{"ls", SepPipe}, {"wc -l", SepNone}}},
		{"semi", "a; b", []want{{"a", SepSemi}, {"b", SepNone}}},
		{"background", "make &", []want{{"make", SepBg}}},
		{"newline", "a\nb", []want{{"a", SepNewline}, {"b", SepNone}}},
		{
			"quoted separators are not split",
			`echo "a && b" && echo 'c | d'`,
			[]want{{`echo "a && b"`, SepAnd}, {`echo 'c | d'`, SepNone}},
		},
		{
			"command substitution is not split",
			`echo $(ls | wc -l) && true`,
			[]want{{`echo $(ls | wc -l)`, SepAnd}, {"true", SepNone}},
		},
		{
			"backticks are not split",
			"echo `ls | head` ; true",
			[]want{{"echo `ls | head`", SepSemi}, {"true", SepNone}},
		},
		{
			"subshell stays one segment",
			"(cd sub && make) | tee",
			[]want{{"(cd sub && make)", SepPipe}, {"tee", SepNone}},
		},
		{
			"escaped separator is literal",
			`echo a \&\& b`,
			[]want{{`echo a \&\& b`, SepNone}},
		},
		{"trailing separator", "a &&", []want{{"a", SepAnd}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitTopLevelWithSeparators(tc.in)
			if len(got) != len(tc.out) {
				t.Fatalf("segments = %d (%+v), want %d", len(got), got, len(tc.out))
			}
			for i := range got {
				if got[i].Text != tc.out[i].text || got[i].Sep != tc.out[i].sep {
					t.Errorf("seg[%d] = (%q,%q), want (%q,%q)", i, got[i].Text, got[i].Sep, tc.out[i].text, tc.out[i].sep)
				}
			}
		})
	}
}

// Offsets must slice back to the exact segment text, since the rewriter splices
// by offset into the original string.
func TestSegmentOffsetsSliceBack(t *testing.T) {
	for _, in := range []string{
		"git status",
		"  make   &&   make test  ",
		`echo "a && b" && git log`,
		"a\n b\tc",
		"(cd x && make) | tee out",
	} {
		for _, seg := range SplitTopLevelWithSeparators(in) {
			if got := in[seg.Start:seg.End]; got != seg.Text {
				t.Errorf("in=%q slice %d:%d = %q, want %q", in, seg.Start, seg.End, got, seg.Text)
			}
		}
	}
}

func TestSplitPipelineStages(t *testing.T) {
	got := SplitPipelineStages("git status | head -5 && echo done | cat")
	want := []string{"git status", "head -5 && echo done", "cat"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSplitWordsQuoteAware(t *testing.T) {
	cases := []struct {
		in  string
		out []string
	}{
		{`git commit -m "a b c"`, []string{"git", "commit", "-m", `"a b c"`}},
		{`git commit -m'a b'`, []string{"git", "commit", `-m'a b'`}},
		{`echo a\ b`, []string{"echo", `a\ b`}},
		{"  spaced   out  ", []string{"spaced", "out"}},
		{`--msg="x y"`, []string{`--msg="x y"`}},
	}
	for _, tc := range cases {
		if got := SplitWords(tc.in); !reflect.DeepEqual(got, tc.out) {
			t.Errorf("SplitWords(%q) = %q, want %q", tc.in, got, tc.out)
		}
	}
}

func TestHasTopLevelRedirection(t *testing.T) {
	cases := map[string]bool{
		"git status":            false,
		"make > out.log":        true,
		"make 2>&1":             true,
		"cat < in.txt":          true,
		`echo "a > b"`:          false,
		`echo 'x < y'`:          false,
		`echo \> literal`:       false,
		"grep -r x . | head -5": false,
	}
	for in, want := range cases {
		if got := HasTopLevelRedirection(in); got != want {
			t.Errorf("HasTopLevelRedirection(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestHasTopLevelCompound(t *testing.T) {
	cases := map[string]bool{
		"git status":     false,
		`echo "a && b"`:  false,
		"a && b":         true,
		"a | b":          true,
		"(a && b)":       false,
		"echo $(a && b)": false,
	}
	for in, want := range cases {
		if got := HasTopLevelCompound(in); got != want {
			t.Errorf("HasTopLevelCompound(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestPeelBalancedWrapper(t *testing.T) {
	cases := map[string]string{
		"(git status)":   "git status",
		"((git status))": "git status",
		"git status":     "git status",
		"(a) && (b)":     "(a) && (b)",
		"(cd x && make)": "cd x && make",
	}
	for in, want := range cases {
		if got := PeelBalancedWrapper(in); got != want {
			t.Errorf("PeelBalancedWrapper(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSegmentIsSubshell(t *testing.T) {
	segs := SplitTopLevelWithSeparators("(cd x && make) | tee")
	if !segs[0].IsSubshell() {
		t.Error("expected first segment to be a subshell")
	}
	if segs[1].IsSubshell() {
		t.Error("expected second segment not to be a subshell")
	}
}

func TestMultibyteRunesAreNotMisread(t *testing.T) {
	in := `echo "路径 → 目标" && git status`
	segs := SplitTopLevelWithSeparators(in)
	if len(segs) != 2 {
		t.Fatalf("segments = %d (%+v), want 2", len(segs), segs)
	}
	if segs[0].Text != `echo "路径 → 目标"` {
		t.Errorf("seg0 = %q", segs[0].Text)
	}
	if got := in[segs[0].Start:segs[0].End]; got != segs[0].Text {
		t.Errorf("offset slice = %q, want %q", got, segs[0].Text)
	}
}

// The helpers below were production functions that only the tests in this
// package ever called. They live here so the production files carry no
// unused code while the tests keep exercising the live code underneath:
// FormatRuleString's round trip, IntersectRequestPermissionsResponseAtCWD
// and SplitTopLevelWithSeparators.

// SplitPipelineStages splits a shell line on top-level pipes only, leaving
// &&, ||, ; and & inside the returned stages.
func SplitPipelineStages(s string) []string {
	segs := SplitTopLevelWithSeparators(s)
	out := make([]string, 0, len(segs))
	var cur strings.Builder
	for _, seg := range segs {
		if cur.Len() > 0 {
			cur.WriteByte(' ')
		}
		cur.WriteString(seg.Text)
		if seg.Sep == SepPipe || seg.Sep == SepNone {
			if v := strings.TrimSpace(cur.String()); v != "" {
				out = append(out, v)
			}
			cur.Reset()
			continue
		}
		cur.WriteByte(' ')
		cur.WriteString(seg.Sep)
	}
	if v := strings.TrimSpace(cur.String()); v != "" {
		out = append(out, v)
	}
	return out
}

// HasTopLevelCompound reports whether the line contains more than one
// top-level segment, i.e. any unquoted &&, ||, ;, |, & or newline.
func HasTopLevelCompound(s string) bool {
	return len(SplitTopLevelWithSeparators(s)) > 1
}

// TestClassifyShellCommand pins the three answers the classifier can give. The
// first group is the reason the type exists: commands that are plainly read-only
// and were nevertheless refused by a whitelist that answered "not proven
// read-only" with "refuses to run".
func TestClassifyShellCommand(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want ShellMutation
	}{
		// Reported false positives, quoted from the session that hit them.
		{
			name: "reported: go env and go list",
			cmd:  `go env GOPATH GOMODCACHE && go list -m -f '{{.Dir}} {{.Version}}' github.com/modelcontextprotocol/go-sdk 2>/dev/null`,
			want: ShellMutationReadOnly,
		},
		{
			name: "reported: git ls-files through awk sort uniq",
			cmd:  `git --no-pager ls-files 'pkg/**/*.go' | awk -F/ '{print $1 "/" $2}' | sort | uniq -c | sort -k2 && git --no-pager ls-files 'pkg/*/*.go' | awk -F/ '{print $1 "/" $2}' | sort | uniq -c`,
			want: ShellMutationReadOnly,
		},
		{name: "reported rewrite: go list module", cmd: "go list -m github.com/modelcontextprotocol/go-sdk", want: ShellMutationReadOnly},
		{name: "awk without a pipe", cmd: `awk -F/ '{print $1}' go.mod`, want: ShellMutationReadOnly},
		{name: "go version", cmd: "go version", want: ShellMutationReadOnly},
		{name: "go doc", cmd: "go doc net/http", want: ShellMutationReadOnly},
		{name: "go mod graph", cmd: "go mod graph", want: ShellMutationReadOnly},
		{name: "jq", cmd: "jq . package.json", want: ShellMutationReadOnly},
		{name: "xargs read-only command", cmd: "xargs wc -l", want: ShellMutationReadOnly},
		{name: "env without a command", cmd: "env", want: ShellMutationReadOnly},

		// Git read-only surface.
		{name: "git status", cmd: "git status --short", want: ShellMutationReadOnly},
		{name: "git diff", cmd: "git diff -- src/main.go", want: ShellMutationReadOnly},
		{name: "git branch list", cmd: "git branch --show-current", want: ShellMutationReadOnly},
		{name: "git worktree list", cmd: "git worktree list --porcelain", want: ShellMutationReadOnly},
		{name: "git ls-tree", cmd: "git ls-tree -r HEAD", want: ShellMutationReadOnly},
		{name: "git cat-file", cmd: "git cat-file -p HEAD", want: ShellMutationReadOnly},
		{name: "git shortlog", cmd: "git shortlog -sn", want: ShellMutationReadOnly},
		{name: "git stash list", cmd: "git stash list", want: ShellMutationReadOnly},
		{name: "git remote -v", cmd: "git remote -v", want: ShellMutationReadOnly},
		{name: "git config get", cmd: "git config --get user.email", want: ShellMutationReadOnly},
		{name: "git tag list", cmd: "git tag -l 'v*'", want: ShellMutationReadOnly},
		{name: "git branch create", cmd: "git branch feature/example", want: ShellMutationWrites},
		{name: "git branch delete", cmd: "git branch -d old-feature", want: ShellMutationWrites},
		{name: "git checkout", cmd: "git checkout main", want: ShellMutationWrites},
		{name: "git commit", cmd: "git commit -m x", want: ShellMutationWrites},
		{name: "git checkout restore", cmd: "git checkout -- .", want: ShellMutationWrites},
		{name: "git config set", cmd: "git config user.email me@example.com", want: ShellMutationWrites},
		{name: "git tag create", cmd: "git tag v1.0.0", want: ShellMutationWrites},
		{name: "git stash push", cmd: "git stash", want: ShellMutationWrites},
		{name: "git remote add", cmd: "git remote add origin git@example.com:x.git", want: ShellMutationWrites},
		{name: "git config override", cmd: "git -c core.pager=cat status", want: ShellMutationUnproven},

		// The reverse hole: these were classified read-only and would run
		// unasked in plan mode.
		{name: "find delete", cmd: "find . -name '*.go' -delete", want: ShellMutationWrites},
		{name: "find exec", cmd: "find . -name '*.go' -exec rm {} ;", want: ShellMutationWrites},
		{name: "sed range to file", cmd: "sed -n '1,5w out.txt' go.mod", want: ShellMutationWrites},
		{name: "sed substitution to file", cmd: "sed 's/a/b/w out.txt' go.mod", want: ShellMutationWrites},
		{name: "sed in place", cmd: "sed -i '' s/a/b/ main.go", want: ShellMutationWrites},
		{name: "sed GNU in place", cmd: "sed -i 's/a/b/' main.go", want: ShellMutationWrites},
		{name: "sed program from file", cmd: "sed -f prog.sed go.mod", want: ShellMutationUnproven},
		{name: "sed e flag", cmd: "sed 's/a/b/e' go.mod", want: ShellMutationUnproven},
		{name: "sed print is read-only", cmd: "sed -n '1,20p' file.txt", want: ShellMutationReadOnly},
		{name: "sed address regex mentioning w", cmd: "sed -n '/w/p' file.txt", want: ShellMutationReadOnly},
		{name: "sed y command", cmd: "sed 'y/ab/wx/' file.txt", want: ShellMutationReadOnly},
		{name: "sed append text", cmd: "sed 'a welcome' file.txt", want: ShellMutationReadOnly},

		// Go writes.
		{name: "go build", cmd: "go build ./...", want: ShellMutationWrites},
		{name: "go test", cmd: "go test ./...", want: ShellMutationUnproven},
		{name: "go mod tidy", cmd: "go mod tidy", want: ShellMutationWrites},
		{name: "go env -w", cmd: "go env -w GOFLAGS=-mod=mod", want: ShellMutationWrites},
		{name: "go fmt", cmd: "go fmt ./...", want: ShellMutationWrites},
		{name: "gofmt -l", cmd: "gofmt -l pkg/tool", want: ShellMutationReadOnly},
		{name: "gofmt -w", cmd: "gofmt -w pkg/tool/shell_tool.go", want: ShellMutationWrites},

		// awk programs that write or execute.
		{name: "awk print to file", cmd: `awk 'BEGIN{print "x" > "/tmp/y"}'`, want: ShellMutationUnproven},
		{name: "awk pipe to command", cmd: `awk '{print | "sort"}' data`, want: ShellMutationUnproven},
		{name: "awk system", cmd: `awk 'BEGIN{system("rm -f /tmp/x")}'`, want: ShellMutationUnproven},
		{name: "awk program file", cmd: "awk -f prog.awk data", want: ShellMutationUnproven},

		// Commands whose effect is not visible from the text.
		{name: "python script", cmd: "python3 script.py", want: ShellMutationUnproven},
		{name: "command substitution", cmd: "cat $(ls)", want: ShellMutationUnproven},
		{name: "variable in double quotes", cmd: `cat "$HOME/notes"`, want: ShellMutationUnproven},
		{name: "backticks", cmd: "echo `date`", want: ShellMutationUnproven},
		{name: "unbalanced quote", cmd: `echo "unterminated`, want: ShellMutationUnproven},

		// Wrappers recurse into the command they run.
		{name: "xargs rm", cmd: "xargs rm", want: ShellMutationWrites},
		{name: "sudo rm", cmd: "sudo rm -rf dir", want: ShellMutationWrites},
		{name: "env prefix read-only", cmd: "env -i ls", want: ShellMutationReadOnly},
		{name: "timeout read-only", cmd: "timeout 5 ls", want: ShellMutationReadOnly},
		{name: "unknown wrapped command", cmd: "sudo python3 script.py", want: ShellMutationUnproven},

		// Output only.
		{name: "echo", cmd: "echo hello world", want: ShellMutationReadOnly},
		{name: "printf", cmd: "printf '%s\n' foo", want: ShellMutationReadOnly},
		{name: "date with format", cmd: "date +%Y-%m-%d", want: ShellMutationReadOnly},
		{name: "which go", cmd: "which go", want: ShellMutationReadOnly},
		{name: "env-var prefix", cmd: "LANG=C ls -la", want: ShellMutationReadOnly},

		// Redirections and pipes.
		{name: "redirection", cmd: "cat file.txt > out.txt", want: ShellMutationWrites},
		{name: "append redirection", cmd: "echo hi >> file.txt", want: ShellMutationWrites},
		{name: "compound write", cmd: "ls && rm file.txt", want: ShellMutationWrites},
		{name: "compound read", cmd: "pwd && git rev-parse --show-toplevel && git worktree list --porcelain", want: ShellMutationReadOnly},
		{name: "pipe read-only", cmd: "ls | grep foo", want: ShellMutationReadOnly},
		{name: "pipe with env prefix", cmd: "LANG=C ls -la 2>/dev/null | grep foo", want: ShellMutationReadOnly},
		{name: "pipe destructive right", cmd: "ls | rm -rf dir", want: ShellMutationWrites},
		{name: "pipe destructive left", cmd: "rm -rf dir | cat", want: ShellMutationWrites},
		{name: "background write", cmd: "rm -rf dir &", want: ShellMutationWrites},
		{name: "devnull redirect stderr", cmd: "ls -d internal/*/ 2>/dev/null", want: ShellMutationReadOnly},
		{name: "devnull redirect merge", cmd: "ls &>/dev/null", want: ShellMutationReadOnly},
		{name: "both streams to file", cmd: "ls &>out.txt", want: ShellMutationWrites},
		{name: "fd dup stderr to stdout", cmd: "git diff -- file 2>&1", want: ShellMutationReadOnly},
		{name: "fd dup stdout to stderr", cmd: "ls 1>&2", want: ShellMutationReadOnly},
		{name: "fd dup close fd", cmd: "ls 2>&-", want: ShellMutationReadOnly},
		{name: "compound fd dup all read", cmd: "cd /tmp && git rev-parse HEAD 2>&1 && git worktree list 2>&1", want: ShellMutationReadOnly},
		{name: "fd dup plus file redirect", cmd: "cd /tmp && git status 2>&1 > out.txt", want: ShellMutationWrites},
		{name: "redirect to real file", cmd: "ls 2>errors.log", want: ShellMutationWrites},
		{name: "input redirect", cmd: "cat < file.txt", want: ShellMutationReadOnly},

		// Writers.
		{name: "mkdir", cmd: "mkdir tmp", want: ShellMutationWrites},
		{name: "rm", cmd: "rm -rf dir", want: ShellMutationWrites},
		{name: "cp", cmd: "cp a b", want: ShellMutationWrites},
		{name: "tee", cmd: "tee out.txt", want: ShellMutationWrites},
		{name: "patch", cmd: "patch -p1 < fix.diff", want: ShellMutationWrites},
		{name: "chmod", cmd: "chmod +x run.sh", want: ShellMutationWrites},
		{name: "sort to file", cmd: "sort -o output.txt input.txt", want: ShellMutationWrites},
		{name: "sort output flag", cmd: "sort --output=out.txt input.txt", want: ShellMutationWrites},
		{name: "base64 to file", cmd: "base64 -o out.bin in.bin", want: ShellMutationWrites},
		{name: "yq in place", cmd: "yq -i '.a=1' config.yaml", want: ShellMutationWrites},
		{name: "unknown command", cmd: "frobnicate --now", want: ShellMutationUnproven},
		{name: "empty command", cmd: "   ", want: ShellMutationUnproven},

		// The read-only table itself still answers read-only.
		{name: "rg", cmd: "rg TODO internal", want: ShellMutationReadOnly},
		{name: "grep pipeline", cmd: `grep -rn "glob\|Glob" --include="*.go" pkg/ | grep -iv "global" | grep -i glob`, want: ShellMutationReadOnly},
		{name: "uniq", cmd: "uniq file.txt", want: ShellMutationReadOnly},
		{name: "cut", cmd: "cut -d: -f1 /etc/passwd", want: ShellMutationReadOnly},
		{name: "diff", cmd: "diff file1 file2", want: ShellMutationReadOnly},
		{name: "tree", cmd: "tree -L 2", want: ShellMutationReadOnly},
		{name: "true", cmd: "true", want: ShellMutationReadOnly},
		{name: "path-prefixed", cmd: "/usr/bin/ls -la", want: ShellMutationReadOnly},
		{name: "sha256sum", cmd: "sha256sum file.bin", want: ShellMutationReadOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyShellCommand(tt.cmd, nil, ""); got != tt.want {
				t.Fatalf("ClassifyShellCommand(%q) = %s, want %s", tt.cmd, got, tt.want)
			}
		})
	}
}

// Paths decide the answer for a read-only command too: reading somewhere no root
// opens is not something to approve on the command's own recognisance.
func TestClassifyShellCommandBoundsPathsToAllowedRoots(t *testing.T) {
	primaryRoot := filepath.Join(t.TempDir(), "primary")
	externalRoot := filepath.Join(t.TempDir(), "external")
	target := filepath.Join(externalRoot, "OpenclawRelayService.java")
	cmd := `rg -n "reasoning_end|reasoning|onReasoning|message_snapshot|op" ` + target

	if got := ClassifyShellCommand(cmd, []string{primaryRoot}, primaryRoot); got != ShellMutationUnproven {
		t.Fatalf("path outside the sole allowed root = %s, want unproven", got)
	}
	if got := ClassifyShellCommand(cmd, []string{primaryRoot, externalRoot}, primaryRoot); got != ShellMutationReadOnly {
		t.Fatalf("read-only rg under an additional allowed root = %s, want read-only", got)
	}
}

func TestClassifyShellCommandAllowsPathsAcrossAllowedRoots(t *testing.T) {
	firstRoot := filepath.Join(t.TempDir(), "first")
	secondRoot := filepath.Join(t.TempDir(), "second")
	cmd := "diff " + filepath.Join(firstRoot, "a.txt") + " " + filepath.Join(secondRoot, "b.txt")

	if got := ClassifyShellCommand(cmd, []string{firstRoot, secondRoot}, firstRoot); got != ShellMutationReadOnly {
		t.Fatalf("read-only command spanning allowed roots = %s, want read-only", got)
	}
}

// A directory reachable by two names is still one directory. The roots arrive
// resolved (macOS /var is really /private/var) while the command's working
// directory keeps the path the shell was started with, so a plain string
// comparison rejects every relative argument the command names - which is how
// both reported commands were refused even though they write nothing.
func TestClassifyShellCommandMatchesRootsAcrossSymlinkedNames(t *testing.T) {
	base := t.TempDir()
	real, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// The root is resolved and the cwd is not, exactly as the tool state and the
	// process supply them.
	root := real
	cwd := link
	for _, cmd := range []string{
		"ls pkg/tool",
		"rg -n needle pkg/tool/shell.go",
		"git status --short",
		"cat ./go.mod",
	} {
		if got := ClassifyShellCommand(cmd, []string{root}, cwd); got != ShellMutationReadOnly {
			t.Fatalf("ClassifyShellCommand(%q) with root=%s cwd=%s = %s, want read-only", cmd, root, cwd, got)
		}
	}
	// A path genuinely outside the root is still refuted, in either spelling.
	if got := ClassifyShellCommand("cat /etc/hosts", []string{root}, cwd); got != ShellMutationUnproven {
		t.Fatalf("absolute path outside the root = %s, want unproven", got)
	}
}

// The allowed root is what makes a parent-directory read legitimate; a bare ".."
// names one just as much as "../x" does, so it has to be checked the same way.
func TestClassifyShellCommandChecksBareParentDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	if got := ClassifyShellCommand("ls ..", []string{root}, root); got != ShellMutationUnproven {
		t.Fatalf("bare .. above the allowed root = %s, want unproven", got)
	}
	if got := ClassifyShellCommand("ls .", []string{root}, root); got != ShellMutationReadOnly {
		t.Fatalf("ls . inside the allowed root = %s, want read-only", got)
	}
}

func TestClassifyShellCommandSupportsGitCOptions(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	tests := []struct {
		name string
		cmd  string
		want ShellMutation
	}{
		{name: "separate path", cmd: "git -C " + root + " status --short", want: ShellMutationReadOnly},
		{name: "attached path", cmd: "git -C" + root + " rev-parse --short HEAD", want: ShellMutationReadOnly},
		{name: "multiple paths", cmd: "git -C " + root + " -C " + root + " status --short", want: ShellMutationReadOnly},
		{name: "compound", cmd: "git -C " + root + " status --short && git -C" + root + " rev-parse --short HEAD", want: ShellMutationReadOnly},
		{name: "outside path", cmd: "git -C " + outside + " status --short", want: ShellMutationUnproven},
		{name: "missing path", cmd: "git -C", want: ShellMutationUnproven},
		{name: "write subcommand", cmd: "git -C " + root + " checkout main", want: ShellMutationWrites},
		{name: "config override", cmd: "git -C " + root + " -c core.pager=cat status", want: ShellMutationUnproven},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyShellCommand(tt.cmd, []string{root}, root); got != tt.want {
				t.Fatalf("ClassifyShellCommand(%q) = %s, want %s", tt.cmd, got, tt.want)
			}
		})
	}
}

// The go toolchain runs code the classifier cannot see: `go test`, `go vet` and
// `go run` write build caches and temp files and then execute whatever the
// packages contain. That is the tier `python3 script.py` already holds - a
// question for the operator, not a proven write that plan mode refuses outright.
// The forms that write to a path chosen on the command line keep the proof.
func TestClassifyGoCommandSeparatesCodeExecutionFromProvenWrites(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		cmd  string
		want ShellMutation
	}{
		{"go test ./pkg/tool", ShellMutationUnproven},
		{"go test -run TestX ./pkg/tool -count=1", ShellMutationUnproven},
		{"go vet ./...", ShellMutationUnproven},
		{"go run .", ShellMutationUnproven},
		{"go test -c -o /tmp/t ./pkg/tool", ShellMutationWrites},
		{"go test -c ./pkg/tool", ShellMutationWrites},
		{"go build ./...", ShellMutationWrites},
		{"go install ./...", ShellMutationWrites},
		{"go generate ./...", ShellMutationWrites},
		{"go mod tidy", ShellMutationWrites},
		{"go fmt ./...", ShellMutationWrites},
	}
	for _, tt := range tests {
		if got := ClassifyShellCommand(tt.cmd, []string{root}, root); got != tt.want {
			t.Fatalf("ClassifyShellCommand(%q) = %s, want %s", tt.cmd, got, tt.want)
		}
	}
}

// The walker now carries each clause's redirection targets and heredoc flag, and
// the tier every existing caller reads must not have moved with them: the clause
// texts stay exactly what they were, so classifyShellClause sees the same words.
func TestSplitShellClassifyClausesRecordsRedirectsWithoutMovingTiers(t *testing.T) {
	clauses, floor, doubt := splitShellClassifyClauses(`echo hi > /tmp/x.txt 2>&1 && cat go.mod | head -2`)
	if floor != ShellMutationWrites || doubt {
		t.Fatalf("floor = %s doubt=%v, want writes/false", floor, doubt)
	}
	want := []shellClause{
		{Text: "echo hi  /tmp/x.txt", Redirects: []string{"/tmp/x.txt"}},
		{Text: "cat go.mod"},
		{Text: "head -2"},
	}
	if len(clauses) != len(want) {
		t.Fatalf("clauses = %#v, want %#v", clauses, want)
	}
	for i, clause := range clauses {
		if clause.Text != want[i].Text || clause.Heredoc {
			t.Fatalf("clause %d = %#v, want %#v", i, clause, want[i])
		}
		if strings.Join(clause.Redirects, "|") != strings.Join(want[i].Redirects, "|") {
			t.Fatalf("clause %d redirects = %v, want %v", i, clause.Redirects, want[i].Redirects)
		}
	}

	// A heredoc whose body only feeds a reader is data: the body is skipped, so
	// the line is one clause and nothing about it is flagged. doubt survives a
	// redirection's higher floor, which is what a tier reader has to see.
	heredocClauses, _, heredocDoubt := splitShellClassifyClauses("cat <<EOF > /tmp/x\nmkdir /tmp/y\nEOF")
	if len(heredocClauses) != 1 || heredocClauses[0].Heredoc {
		t.Fatalf("a data body must be skipped, not flagged: %#v", heredocClauses)
	}
	if heredocDoubt {
		t.Fatal("a heredoc is not an expansion")
	}
	if _, _, expansionDoubt := splitShellClassifyClauses("echo $(date) > /tmp/x"); !expansionDoubt {
		t.Fatal("an expansion under a redirection must still be doubt")
	}
	if got := ClassifyShellCommand("echo $(date) > /tmp/x", nil, ""); got != ShellMutationWrites {
		t.Fatalf("expansion under a redirection = %s, want writes", got)
	}
}

// The go tool's flag that decides where a build operation writes is compared by
// its exact name. A prefix test reads "-overlay" as "-o" and refuses a test run
// that writes nothing; the profile flags and -modfile really do write, and are
// refused.
func TestClassifyGoCommandFlagTiers(t *testing.T) {
	writes := []string{
		"go test -c ./pkg/tool",
		"go test -o /tmp/t ./pkg/tool",
		"go test -o=/tmp/t ./pkg/tool",
		"go test -outputdir ./out ./...",
		"go test -outputdir=./out ./...",
		"go test -coverprofile pkg/tool/c.out ./pkg/tool",
		"go test -cpuprofile pkg/tool/cpu.out ./pkg/tool",
		"go test -memprofile pkg/tool/mem.out ./pkg/tool",
		"go test -mutexprofile pkg/tool/mutex.out ./pkg/tool",
		"go test -blockprofile pkg/tool/block.out ./pkg/tool",
		"go test -trace pkg/tool/trace.out ./pkg/tool",
		"go test -modfile /tmp/go.mod ./pkg/tool",
	}
	for _, cmd := range writes {
		if got := ClassifyShellCommand(cmd, nil, ""); got != ShellMutationWrites {
			t.Fatalf("ClassifyShellCommand(%q) = %s, want writes", cmd, got)
		}
	}
	// -overlay names a JSON config that build operations read; it writes nothing
	// to the path it names.
	unproven := []string{
		"go test -overlay=/tmp/x/overlay.json ./pkg/tui -count=1",
		"go test -overlay /tmp/x/overlay.json ./pkg/tui",
		"go test -tags fts5 -overlay=/tmp/x/overlay.json ./pkg/tui -count=1",
		"go test ./pkg/tool",
	}
	for _, cmd := range unproven {
		if got := ClassifyShellCommand(cmd, nil, ""); got != ShellMutationUnproven {
			t.Fatalf("ClassifyShellCommand(%q) = %s, want unproven", cmd, got)
		}
	}
}

// `sh -c <script>` hides the command that runs in an operand, so the tier has to
// be the script's tier - including the flags-before-`-c` and clustered forms,
// because `-lc` is the same shell form as `-c`. Past the nesting cap the script
// is a write: the proof stopped reading it.
func TestClassifyShellCommandUnwrapsDashCScripts(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		cmd  string
		want ShellMutation
	}{
		{"sh -c 'ls -la'", ShellMutationReadOnly},
		{"sh -c 'rm -rf /tmp/x'", ShellMutationWrites},
		{"sh -c 'rm -rf pkg/tool'", ShellMutationWrites},
		{"bash -lc 'rm -rf pkg/tool'", ShellMutationWrites},
		{"bash -lc 'go test ./pkg'", ShellMutationUnproven},
		{"sh -ec 'rm -rf pkg'", ShellMutationWrites},
		{`sh -c "sh -c 'rm -rf pkg'"`, ShellMutationWrites},
		{"sh script.sh", ShellMutationUnproven},
		{"sh -c 'true'", ShellMutationReadOnly},
	}
	for _, tt := range cases {
		if got := ClassifyShellCommand(tt.cmd, []string{root}, root); got != tt.want {
			t.Fatalf("ClassifyShellCommand(%q) = %s, want %s", tt.cmd, got, tt.want)
		}
	}
	// Past the cap the proof stopped reading the script, so the answer is
	// writes rather than a guess about what the layers below it do.
	if got := shellScriptMutation("rm -rf pkg", []string{root}, root, shellScriptDepthCap+1); got != ShellMutationWrites {
		t.Fatalf("a script past the depth cap = %s, want writes", got)
	}
	// One layer under the cap is still read, which is what makes the cap a bound
	// rather than a refusal.
	if got := shellScriptMutation("ls -la", []string{root}, root, shellScriptDepthCap); got != ShellMutationReadOnly {
		t.Fatalf("a script at the depth cap = %s, want read-only", got)
	}
}

// The clause facts are what the scratch proof reads instead of the flattened
// text: the command word comes from the words the operators did not take, the
// arguments follow it, every substitution source is recorded, and a grouping
// token is flagged.
func TestSplitShellClassifyClausesCommandFacts(t *testing.T) {
	type fact struct {
		text     string
		command  string
		args     string
		subs     string
		grouping bool
	}
	cases := []struct {
		name string
		cmd  string
		want []fact
	}{
		{
			// A redirection's target is not the command word, even when it
			// looks like one, and the operator's own words do not become
			// arguments.
			name: "a redirect before the command",
			cmd:  "> /tmp/cat sh -c 'rm -rf pkg/tool'",
			want: []fact{{text: "/tmp/cat sh -c 'rm -rf pkg/tool'", command: "sh", args: "-c|rm -rf pkg/tool"}},
		},
		{
			name: "a redirect target equal to a later word",
			cmd:  "cmp /tmp/x /etc > /tmp/x",
			want: []fact{{text: "cmp /tmp/x /etc  /tmp/x", command: "cmp", args: "/tmp/x|/etc"}},
		},
		{
			name: "an env assignment is not the command",
			cmd:  "CGO_ENABLED=1 go test ./pkg",
			want: []fact{{text: "CGO_ENABLED=1 go test ./pkg", command: "go", args: "test|./pkg"}},
		},
		{
			name: "a negation is not the command",
			cmd:  "! sed -i '' s/a/b/ main.go",
			want: []fact{{text: "! sed -i '' s/a/b/ main.go", command: "sed", args: "-i|s/a/b/|main.go"}},
		},
		{
			name: "exec is not the command",
			cmd:  "exec go test -c ./pkg/tool",
			want: []fact{{text: "exec go test -c ./pkg/tool", command: "go", args: "test|-c|./pkg/tool"}},
		},
		{
			name: "a reserved word is not the command",
			cmd:  "if true; then rm -rf pkg/tool; fi",
			want: []fact{
				{text: "if true", command: "true"},
				{text: "then rm -rf pkg/tool", command: "rm", args: "-rf|pkg/tool"},
				{text: "fi"},
			},
		},
		{
			name: "a subshell puts its command out of reach",
			cmd:  "(rm -rf pkg/tool)",
			want: []fact{{text: "(rm -rf pkg/tool)", command: "rm", args: "-rf|pkg/tool", grouping: true}},
		},
		{
			// The closing token belongs to a group that simply ends here, so it
			// cannot move where this clause's write lands.
			name: "a closing group token at the end of a clause",
			cmd:  "(cd /tmp && echo hi > x)",
			want: []fact{
				{text: "(cd /tmp", command: "cd", args: "/tmp", grouping: true},
				{text: "echo hi  x)", command: "echo", args: "hi"},
			},
		},
		{
			name: "a word-start tilde",
			cmd:  "ls ~/.forebrain",
			want: []fact{{text: "ls ~/.forebrain", command: "ls", args: "~/.forebrain", grouping: true}},
		},
		{
			name: "a command word is a basename, lowercased",
			cmd:  "/Bin/RM -rf /tmp/x",
			want: []fact{{text: "/Bin/RM -rf /tmp/x", command: "rm", args: "-rf|/tmp/x"}},
		},
		{
			name: "a substitution source is recorded",
			cmd:  "echo $(rm -rf pkg) > /tmp/x",
			want: []fact{{text: "echo $(rm -rf pkg)  /tmp/x", command: "echo", args: "$(rm -rf pkg)", subs: "rm -rf pkg"}},
		},
		{
			name: "a nested substitution inside a parameter expansion",
			cmd:  `echo "${x:-$(rm -rf pkg)}" > /tmp/y`,
			want: []fact{{text: `echo "${x:-$(rm -rf pkg)}"  /tmp/y`, command: "echo", args: "${x:-$(rm -rf pkg)}", subs: "rm -rf pkg"}},
		},
		{
			// ${ … } is a value, and what it contains is the substitution.
			name: "a parameter expansion is not a substitution",
			cmd:  `printf '%s' "$PWD" > /tmp/x`,
			want: []fact{{text: `printf '%s' "$PWD"  /tmp/x`, command: "printf", args: "%s|$PWD"}},
		},
		{
			name: "arithmetic keeps its own brackets out of grouping",
			cmd:  "echo $((1+2)) > /tmp/x",
			want: []fact{{text: "echo $((1+2))  /tmp/x", command: "echo", args: "$((1+2))", subs: "(1+2)"}},
		},
		{
			name: "a trap action is shell source",
			cmd:  "trap 'rm -rf pkg/tool' EXIT",
			want: []fact{{text: "trap 'rm -rf pkg/tool' EXIT", command: "trap", args: "rm -rf pkg/tool|EXIT", subs: "rm -rf pkg/tool"}},
		},
		{
			name: "a quoted substitution keeps its place in the word",
			cmd:  "mkdir /tmp/x-$(date +%s)",
			want: []fact{{text: "mkdir /tmp/x-$(date +%s)", command: "mkdir", args: "/tmp/x-$(date +%s)", subs: "date +%s"}},
		},
		{
			name: "a CDPATH assignment is recorded",
			cmd:  "CDPATH=/x cd tool",
			want: []fact{{text: "CDPATH=/x cd tool", command: "cd", args: "tool"}},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			clauses, _, _ := splitShellClassifyClauses(tt.cmd)
			if len(clauses) != len(tt.want) {
				t.Fatalf("clauses = %#v, want %d", clauses, len(tt.want))
			}
			for i, want := range tt.want {
				clause := clauses[i]
				if clause.Text != want.text {
					t.Fatalf("clause %d text = %q, want %q", i, clause.Text, want.text)
				}
				if clause.Command != want.command {
					t.Fatalf("clause %d command = %q, want %q", i, clause.Command, want.command)
				}
				if got := strings.Join(clause.Args, "|"); got != want.args {
					t.Fatalf("clause %d args = %q, want %q", i, got, want.args)
				}
				if got := strings.Join(clause.Substitutions, "|"); got != want.subs {
					t.Fatalf("clause %d substitutions = %q, want %q", i, got, want.subs)
				}
				if clause.Grouping != want.grouping {
					t.Fatalf("clause %d grouping = %v, want %v", i, clause.Grouping, want.grouping)
				}
			}
		})
	}

	// The CDPATH fact is what stops a relative cd from being followed, so it has
	// to reach the clause.
	clauses, _, _ := splitShellClassifyClauses("CDPATH=/x/pkg cd tool")
	if len(clauses) != 1 || !clauses[0].CDPATH {
		t.Fatalf("CDPATH assignment was not recorded: %#v", clauses)
	}
}

// A heredoc body is data when the whole line only feeds it to a reader, and
// commands when it does not. Which of the two happened is what the flag on the
// clause reports, and the body must never be skipped when anything about the
// line could run it.
func TestSplitShellClassifyClausesHeredocFacts(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		// clauses is the number of clauses the walker produced: a skipped body
		// produces none.
		clauses int
		heredoc bool
	}{
		{
			name:    "a quoted body feeding a scratch write",
			cmd:     "cat > /tmp/x <<'EOF'\nrm -rf pkg/tool\nEOF",
			clauses: 1,
		},
		{
			name:    "an unquoted body holding no expansion",
			cmd:     "cat <<EOF > /tmp/x\nmkdir /tmp/y\nEOF",
			clauses: 1,
		},
		{
			name:    "a body after a cd",
			cmd:     "cd /tmp && cat <<'EOF'\nbody\nEOF",
			clauses: 2,
		},
		{
			name:    "a body piped to a reader",
			cmd:     "cat <<'EOF' | head -1\nrm -rf pkg/tool\nEOF",
			clauses: 2,
		},
		{
			// The shell expands an unquoted body before cat sees it, so the
			// body is commands.
			name:    "an unquoted body that expands",
			cmd:     "cat >> /tmp/x <<EOF\n$(rm -rf pkg/tool)\nEOF",
			clauses: 3,
			heredoc: true,
		},
		{
			name:    "a body piped to a shell",
			cmd:     "cat <<'EOF' | sh\nrm -rf pkg/tool\nEOF",
			clauses: 4,
			heredoc: true,
		},
		{
			name:    "a shell reading a heredoc",
			cmd:     "sh <<'EOF'\nrm -rf \"$d\"/pkg/tool\nEOF",
			clauses: 3,
			heredoc: true,
		},
		{
			name:    "an interpreter reading a heredoc",
			cmd:     "python3 - <<'EOF'\nprint(1)\nEOF",
			clauses: 3,
			heredoc: true,
		},
		{
			// "<-" only strips tabs, so the delimiter is recognised with the
			// tab removed.
			name:    "tab stripping",
			cmd:     "cat <<-EOF\n\tbody\n\tEOF",
			clauses: 1,
		},
		{
			// No terminator: the rest of the command is the body.
			name:    "an unterminated body",
			cmd:     "cat <<EOF\nbody\n",
			clauses: 1,
		},
		{
			// A delimiter with an expansion is not a word this walker can
			// recognise in the body.
			name:    "an expanded delimiter",
			cmd:     "cat <<$D\nbody\nEOF",
			clauses: 3,
			heredoc: true,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			clauses, _, _ := splitShellClassifyClauses(tt.cmd)
			if len(clauses) != tt.clauses {
				t.Fatalf("%q produced %d clauses, want %d: %#v", tt.cmd, len(clauses), tt.clauses, clauses)
			}
			flagged := false
			for _, clause := range clauses {
				flagged = flagged || clause.Heredoc
			}
			if flagged != tt.heredoc {
				t.Fatalf("%q heredoc flag = %v, want %v: %#v", tt.cmd, flagged, tt.heredoc, clauses)
			}
		})
	}

	// A herestring is not a heredoc: nothing follows the line, so no body is
	// skipped and nothing is flagged.
	clauses, _, _ := splitShellClassifyClauses("cat <<< hi > /tmp/x")
	if len(clauses) != 1 || clauses[0].Heredoc {
		t.Fatalf("a herestring must not be flagged as a heredoc: %#v", clauses)
	}
	if got := strings.Join(clauses[0].Args, "|"); got != "" {
		t.Fatalf("a herestring operand is data, not an argument: %q", got)
	}
}

// The shared table is the only answer to "is this read-only?": the assessment
// that gates auto-approval has to agree with the classifier the plan gate uses,
// or the two drift back into disagreeing about the same command.
func TestKnownSafeAgreesWithClassifier(t *testing.T) {
	root := t.TempDir()
	for _, cmd := range []string{
		"git status --short",
		`go env GOPATH GOMODCACHE && go list -m github.com/x/y`,
		"find . -name '*.go' -delete",
		"sed -n '1,5w out.txt' go.mod",
		"go test ./...",
		"python3 script.py",
	} {
		want := ClassifyShellCommand(cmd, []string{root}, root) == ShellMutationReadOnly
		if got := ShellCommandIsKnownSafe(cmd); got != want {
			t.Fatalf("ShellCommandIsKnownSafe(%q) = %v, classifier says %v", cmd, got, want)
		}
	}
}
