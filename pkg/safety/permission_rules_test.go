package safety

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"gopkg.in/yaml.v3"
)

// A command no prefix can be derived from — a compound line whose clauses each
// need approval, a redirection — is still rememberable as itself. Without the
// exact-command choice its prompt offered nothing but "yes, this once", and the
// identical command came back on the next call.
func TestBuildApprovalDecisionOptionsOffersExactCommandWhenNoPrefixDerivable(t *testing.T) {
	for _, command := range []string{
		"gofmt -w internal/a.go && rm -rf /tmp/x",
		"go test ./... > /tmp/out",
		"git status && git diff",
	} {
		options := BuildApprovalDecisionOptions("shell", "shell", map[string]any{"command": command})
		if len(options) != 3 || options[0].Decision != DecisionAccept ||
			options[1].Decision != DecisionAcceptAndRemember || options[2].Decision != DecisionCancel {
			t.Fatalf("%q decisions = %#v", command, options)
		}
		if len(proposedExecPolicyAmendment(BuildToolApprovalSuggestion("Bash", map[string]any{"command": command}))) > 0 {
			t.Fatalf("%q was expected to have no derivable prefix", command)
		}
	}
}

// The two persistent command choices are mutually exclusive: a command with a
// derivable prefix proposes the prefix and nothing else.
func TestBuildApprovalDecisionOptionsPrefersThePrefixAmendment(t *testing.T) {
	options := BuildApprovalDecisionOptions("shell", "shell", map[string]any{"command": "go test ./..."})
	if len(options) != 3 || options[1].Decision != DecisionAcceptWithExecPolicyAmendment {
		t.Fatalf("decisions = %#v", options)
	}
	rules, ok := ProposedCommandApprovalRules(BuildToolApprovalSuggestion("Bash", map[string]any{"command": "go test ./..."}))
	if !ok || len(rules) != 1 || len(rules[0].CommandPrefix) == 0 || rules[0].RuleContent != "" {
		t.Fatalf("a command with a derivable prefix must propose that prefix alone: %+v", rules)
	}
}

// A command carrying a glob is still just a command. rule_content would have
// handed it back to the pattern parser, which reads the glob as a wildcard and
// grants every command it happens to match, so such commands used to be
// refused outright and their prompt offered nothing but "yes, this once". The
// typed field says "literally this" and the glob is only text.
func TestProposedCommandApprovalRulesRememberACommandCarryingAGlob(t *testing.T) {
	command := "cp /tmp/scratch/* /tmp/dest && chmod -R 755 /tmp/dest"
	rules, ok := ProposedCommandApprovalRules(BuildToolApprovalSuggestion("Bash", map[string]any{"command": command}))
	if !ok || len(rules) != 1 {
		t.Fatalf("rules = %+v, %v", rules, ok)
	}
	if rules[0].Command != command || rules[0].RuleContent != "" || len(rules[0].CommandPrefix) != 0 {
		t.Fatalf("rule = %+v, want the command in the typed field", rules[0])
	}
	if options := BuildApprovalDecisionOptions("shell", "shell", map[string]any{"command": command}); len(options) != 3 ||
		options[1].Decision != DecisionAcceptAndRemember {
		t.Fatalf("decisions = %#v", options)
	}

	store := NewStore()
	store.ReplaceRules(SourceLocalSettings, BehaviorAllow, rules)
	engine := &Engine{}
	if d := engine.EvaluateForSession(store, "", "Bash", command); d.Behavior != BehaviorAllow || !d.BypassSandbox {
		t.Fatalf("the remembered command does not authorize itself: %+v", d)
	}
	// Everything the glob would have matched had it been read as a pattern.
	for _, other := range []string{
		"cp /tmp/scratch/evil /tmp/dest && chmod -R 755 /tmp/dest",
		"cp /tmp/scratch/../../etc/passwd /tmp/dest && chmod -R 755 /tmp/dest",
		"cp /tmp/scratch/a /tmp/dest",
	} {
		if d := engine.EvaluateForSession(store, "", "Bash", other); d.Behavior == BehaviorAllow {
			t.Fatalf("the literal command leaked to %q: %+v", other, d)
		}
	}
}

// A rule carrying both typed shell fields says two opposite things about the
// same command, so the store refuses it rather than picking one.
func TestStoreRefusesARuleCarryingBothTypedCommandFields(t *testing.T) {
	store := NewStore()
	store.ReplaceRules(SourceLocalSettings, BehaviorAllow, []PermissionRuleValue{{
		ToolName: "Bash", CommandPrefix: []string{"go", "test"}, Command: "go test ./...", BypassSandbox: true,
	}})
	if d := (&Engine{}).EvaluateForSession(store, "", "Bash", "go test ./..."); d.Matched != nil {
		t.Fatalf("a contradictory rule was applied: %+v", d)
	}
}

// The model wraps its real work in the same scaffolding on every call and only
// the middle clause varies, so remembering the scaffolding verbatim and the
// work as a prefix is what makes the second build stop asking. One rule naming
// one clause would not: the engine requires every clause to match a rule.
func TestProposedCommandApprovalRulesCoverEveryClause(t *testing.T) {
	command := `export JAVA_HOME=$(/usr/libexec/java_home -v 17) && mvn clean install -DskipTests 2>&1 | tail -12; echo "EXIT=${PIPESTATUS[0]}"`
	rules, ok := ProposedCommandApprovalRules(BuildToolApprovalSuggestion("Bash", map[string]any{"command": command}))
	if !ok {
		t.Fatal("no rules were proposed")
	}
	want := []PermissionRuleValue{
		{ToolName: "Bash", Command: "export JAVA_HOME=$(/usr/libexec/java_home -v 17)", BypassSandbox: true},
		{ToolName: "Bash", CommandPrefix: []string{"mvn", "clean"}, BypassSandbox: true},
		{ToolName: "Bash", Command: `echo "EXIT=${PIPESTATUS[0]}"`, BypassSandbox: true},
	}
	if len(rules) != len(want) {
		t.Fatalf("rules = %+v", rules)
	}
	for i, rule := range rules {
		if rule.Command != want[i].Command || rule.RuleContent != want[i].RuleContent ||
			!slices.Equal(rule.CommandPrefix, want[i].CommandPrefix) || !rule.BypassSandbox {
			t.Fatalf("rule %d = %+v, want %+v", i, rule, want[i])
		}
	}
	if got := CommandApprovalRulePrefixes(rules); len(got) != 1 || got[0] != "mvn clean" {
		t.Fatalf("prefixes = %q", got)
	}

	// "tail -12" is known-safe and carries itself, so it needs no rule and the
	// pager may change without bringing the prompt back.
	store := NewStore()
	store.ReplaceRules(SourceLocalSettings, BehaviorAllow, rules)
	engine := &Engine{}
	for _, next := range []string{
		command,
		`export JAVA_HOME=$(/usr/libexec/java_home -v 17) && mvn clean package -q 2>&1 | head -20; echo "EXIT=${PIPESTATUS[0]}"`,
	} {
		if d := engine.EvaluateForSession(store, "", "Bash", next); d.Behavior != BehaviorAllow {
			t.Fatalf("%q evaluated as %s/%s, want allow", next, d.Behavior, d.Reason)
		}
	}
	// A different program in the same shape is not covered by any of them.
	for _, other := range []string{
		`export JAVA_HOME=$(/usr/libexec/java_home -v 17) && gradle build 2>&1 | tail -12; echo "EXIT=${PIPESTATUS[0]}"`,
		"mvn clean install -DskipTests && rm -rf /tmp/x",
	} {
		if d := engine.EvaluateForSession(store, "", "Bash", other); d.Behavior == BehaviorAllow {
			t.Fatalf("%q was allowed by the remembered rules", other)
		}
	}
}

// A set with no prefix in it would grant exactly what the single whole-command
// rule grants, spread over more rules, so the whole command stays the proposal.
func TestProposedCommandApprovalRulesKeepTheWholeCommandWithoutAPrefix(t *testing.T) {
	command := "gofmt -w internal/a.go && rm -rf /tmp/x"
	rules, ok := ProposedCommandApprovalRules(BuildToolApprovalSuggestion("Bash", map[string]any{"command": command}))
	if !ok || len(rules) != 1 || rules[0].Command != command {
		t.Fatalf("rules = %+v, %v", rules, ok)
	}
}

// A heredoc body is input to the preceding command, and a loop splits into
// fragments that are not commands. Neither may become a rule.
func TestProposedCommandApprovalRulesRefuseNonClauseText(t *testing.T) {
	for _, command := range []string{
		"python3 - <<'EOF'\nimport os\nEOF\ngo build ./...",
		"for f in a b; do go build ./$f; done",
	} {
		rules, ok := ProposedCommandApprovalRules(BuildToolApprovalSuggestion("Bash", map[string]any{"command": command}))
		if ok && len(rules) > 1 {
			t.Fatalf("%q proposed a clause rule set: %+v", command, rules)
		}
	}
}

// The rule that gets persisted must authorize the command the user saw and
// nothing else.
func TestExactCommandRuleAuthorizesOnlyThatCommand(t *testing.T) {
	command := "gofmt -w internal/a.go && rm -rf /tmp/x"
	rules, ok := ProposedCommandApprovalRules(BuildToolApprovalSuggestion("Bash", map[string]any{"command": command}))
	if !ok || len(rules) != 1 || rules[0].Command != command {
		t.Fatalf("proposed rules = %+v, %v", rules, ok)
	}
	snap := snapshotWithRules(SourceLocalSettings, BehaviorAllow, rules[0])
	if !SnapshotGrantsShellSandboxBypass(snap, command) {
		t.Fatal("the remembered command did not authorize itself on the next call")
	}
	for _, other := range []string{
		"gofmt -w internal/a.go && rm -rf /tmp/x && rm -rf /tmp/y",
		"gofmt -w internal/a.go",
		"rm -rf /tmp/x",
	} {
		if SnapshotGrantsShellSandboxBypass(snap, other) {
			t.Fatalf("exact rule leaked to %q", other)
		}
	}
}

func TestBuildApprovalDecisionOptionsSurfacesWebFetchAndReadRemembering(t *testing.T) {
	web := BuildApprovalDecisionOptions("WebFetch", "WebFetch", map[string]any{"url": "https://example.com/docs"})
	if len(web) != 3 || web[1].Decision != DecisionAcceptAndRemember {
		t.Fatalf("webfetch decisions = %#v", web)
	}
	read := BuildApprovalDecisionOptions("Read", "Read", map[string]any{"file_path": "/tmp/notes.txt"})
	if len(read) != 3 || read[1].Decision != DecisionAcceptForSession {
		t.Fatalf("read decisions = %#v", read)
	}
	write := BuildApprovalDecisionOptions("Write", "Write", map[string]any{"file_path": "/tmp/notes.txt"})
	if len(write) != 3 || write[1].Decision != DecisionAcceptForSession {
		t.Fatalf("write decisions = %#v", write)
	}
}

func TestWithoutPersistentCommandChoicesDropsBothCommandRows(t *testing.T) {
	for _, command := range []string{"go test ./...", "gofmt -w internal/a.go && go mod tidy"} {
		options := WithoutPersistentCommandChoices(
			BuildApprovalDecisionOptions("shell", "shell", map[string]any{"command": command}),
		)
		if len(options) != 2 || options[0].Decision != DecisionAccept || options[1].Decision != DecisionCancel {
			t.Fatalf("%q kept a persistent choice: %#v", command, options)
		}
	}
}

// A deny or ask rule keeps prompting whatever the user remembers, so the
// persistent choices are withdrawn. A matched allow rule is the opposite case:
// the prompt came from the sandbox, and a bypass-carrying rule is the only
// answer that stops it returning on every call.
func TestMatchedRuleBlocksRememberingOnlyForRestrictions(t *testing.T) {
	matched := &PermissionRule{Value: PermissionRuleValue{ToolName: "Bash"}}
	if MatchedRuleBlocksRemembering(Decision{Behavior: BehaviorAllow, Matched: matched}) {
		t.Fatal("a matched allow rule must keep the persistent choices")
	}
	for _, behavior := range []PermissionBehavior{BehaviorDeny, BehaviorAsk} {
		if !MatchedRuleBlocksRemembering(Decision{Behavior: behavior, Matched: matched}) {
			t.Fatalf("a matched %s rule must withdraw the persistent choices", behavior)
		}
	}
	if MatchedRuleBlocksRemembering(Decision{Behavior: BehaviorAsk}) {
		t.Fatal("an unmatched default must keep the persistent choices")
	}
}

// The loop this closes: an allow rule that carries no sandbox bypass lets the
// command through the engine, the sandbox denies it, and the escalation prompt
// arrives with a matched rule. Withdrawing the persistent choices there left
// the same prompt returning on every call with no way to answer it once.
func TestMatchedAllowRuleWithoutBypassKeepsThePersistentChoice(t *testing.T) {
	store := NewStore()
	store.AddRule(SourceLocalSettings, BehaviorAllow, PermissionRuleValue{
		ToolName: "Bash", CommandPrefix: []string{"gofmt"},
	})
	command := "gofmt -w internal/a.go"
	decision := NewEngine().Evaluate(store, "Bash", command)
	if decision.Matched == nil || decision.Behavior != BehaviorAllow || decision.BypassSandbox {
		t.Fatalf("decision = %+v", decision)
	}
	if MatchedRuleBlocksRemembering(decision) {
		t.Fatal("the sandbox escalation prompt lost its only remembering choice")
	}
	options := BuildApprovalDecisionOptions("shell", "shell", map[string]any{"command": command})
	if len(options) != 3 || options[1].Decision != DecisionAcceptAndRemember {
		t.Fatalf("decisions = %#v", options)
	}
	store.AddRule(SourceProjectSettings, BehaviorAsk, PermissionRuleValue{
		ToolName: "Bash", CommandPrefix: []string{"gofmt"},
	})
	if !MatchedRuleBlocksRemembering(NewEngine().Evaluate(store, "Bash", command)) {
		t.Fatal("an ask rule must still withdraw the persistent choices")
	}
}

func TestClassifyCommandCanonical(t *testing.T) {
	cases := []struct {
		in        string
		head      string
		sub       string
		canonical string
	}{
		{"git status", "git", "status", "git status"},
		{"git --no-pager status", "git", "status", "git status"},
		{"git -C /repo status", "git", "status", "git status"},
		{"git -c user.name=x log", "git", "log", "git log"},
		{"git --git-dir=/tmp/x status", "git", "status", "git status"},
		{"/usr/local/bin/git status", "git", "status", "git status"},
		{"sudo apt-get install foo", "apt-get", "install", "apt-get install"},
		{"FOO=1 BAR=2 git status", "git", "status", "git status"},
		{"env FOO=1 git status", "git", "status", "git status"},
		{"go test ./...", "go", "test", "go test"},
		{"ls -la", "ls", "", "ls"},
		{"docker ps -a", "docker", "ps", "docker ps"},
		{"'git' status", "git", "status", "git status"},
		{"./mvnw package", "mvnw", "", "mvnw"},
		{"nice -n 10 cargo build", "cargo", "build", "cargo build"},
		{"kubectl get pods", "kubectl", "get", "kubectl get"},
		// Only the first segment is classified.
		{"git status && npm run build", "git", "status", "git status"},
	}
	for _, tc := range cases {
		got := ClassifyCommand(tc.in)
		if got.Head != tc.head || got.Sub != tc.sub || got.Canonical != tc.canonical {
			t.Errorf("ClassifyCommand(%q) = head=%q sub=%q canonical=%q; want head=%q sub=%q canonical=%q",
				tc.in, got.Head, got.Sub, got.Canonical, tc.head, tc.sub, tc.canonical)
		}
	}
}

func TestClassifyCommandFlags(t *testing.T) {
	if c := ClassifyCommand("sudo systemctl status nginx"); !c.Sudo {
		t.Error("expected Sudo=true")
	}
	if c := ClassifyCommand("FOO=1 BAR=2 git status"); len(c.EnvAssignments) != 2 {
		t.Errorf("expected 2 env assignments, got %v", c.EnvAssignments)
	}
	if c := ClassifyCommand("FOO=1"); !c.AssignmentOnly {
		t.Error("expected AssignmentOnly=true")
	}
	if c := ClassifyCommand("bash -c 'rm -rf x'"); !c.ShellScript {
		t.Error("expected ShellScript=true for bash -c")
	}
	if c := ClassifyCommand("/usr/bin/env sh script.sh"); !c.ShellScript {
		t.Errorf("expected ShellScript=true, got head=%q", c.Head)
	}
	if c := ClassifyCommand("npx prettier --write ."); c.Wrapper != "npx" || c.Head != "prettier" {
		t.Errorf("expected wrapper=npx head=prettier, got wrapper=%q head=%q", c.Wrapper, c.Head)
	}
	if c := ClassifyCommand("pnpm exec prettier --check ."); c.Wrapper != "pnpm" || c.Head != "prettier" {
		t.Errorf("expected wrapper=pnpm head=prettier, got wrapper=%q head=%q", c.Wrapper, c.Head)
	}
	// `run` must NOT be unwrapped: it executes a package script, and a script's
	// name is not the name of a binary. Treating "pnpm run prettier" as prettier
	// would let a prettier rule inject a flag into whatever that script runs.
	for _, in := range []string{"pnpm run prettier --check .", "npm run build", "yarn run test"} {
		c := ClassifyCommand(in)
		if c.Wrapper != "" {
			t.Errorf("ClassifyCommand(%q) unwrapped a package script (wrapper=%q head=%q)", in, c.Wrapper, c.Head)
		}
		if c.Sub != "run" {
			t.Errorf("ClassifyCommand(%q) sub = %q, want run", in, c.Sub)
		}
	}
}

func TestClassifyEmptyAndJunk(t *testing.T) {
	for _, in := range []string{"", "   ", "&&", "|", ";"} {
		if c := ClassifyCommand(in); c.Head != "" {
			t.Errorf("ClassifyCommand(%q) head = %q, want empty", in, c.Head)
		}
	}
}

func TestGitGlobalOptWords(t *testing.T) {
	cases := []struct {
		in  []string
		out string
	}{
		{[]string{"-C", "/repo", "status"}, "status"},
		{[]string{"--no-pager", "log"}, "log"},
		{[]string{"-c", "a=b", "commit"}, "commit"},
		{[]string{"--git-dir=/x", "status"}, "status"},
		{[]string{"status"}, "status"},
	}
	for _, tc := range cases {
		got := tc.in[gitGlobalOptWords(tc.in):]
		if len(got) == 0 || got[0] != tc.out {
			t.Errorf("gitGlobalOptWords(%q) left %q, want first=%q", tc.in, got, tc.out)
		}
	}
}

func TestSubcommandIndex(t *testing.T) {
	cases := []struct {
		in   []string
		want int
	}{
		{[]string{"git", "--no-pager", "revert", "cb9df32f", "--no-edit"}, 2},
		{[]string{"git", "-C", "/repo", "status"}, 3},
		{[]string{"go", "test", "./..."}, 1},
		{[]string{"git"}, -1},
		{[]string{"git", "--no-pager"}, -1},
		{[]string{"gofmt", "-w", "a.go"}, -1},
		{nil, -1},
	}
	for _, tc := range cases {
		if got := SubcommandIndex(tc.in); got != tc.want {
			t.Errorf("SubcommandIndex(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestFirstWordBasename(t *testing.T) {
	cases := map[string]string{
		"/usr/bin/env":     "env",
		"./mvnw":           "mvnw",
		"git":              "git",
		`"git"`:            "git",
		`'/usr/bin/git'`:   "git",
		`C:\tools\git.exe`: "git.exe",
	}
	for in, want := range cases {
		if got := firstWordBasename(in); got != want {
			t.Errorf("firstWordBasename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsAssignmentWord(t *testing.T) {
	cases := map[string]bool{
		"FOO=1":  true,
		"foo=":   true,
		"_x=y":   true,
		"1FOO=x": false,
		"foo":    false,
		"--flag": false,
		"a-b=c":  false,
		"=x":     false,
	}
	for in, want := range cases {
		if got := isAssignmentWord(in); got != want {
			t.Errorf("isAssignmentWord(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestGetSimpleCommandPrefix(t *testing.T) {
	tests := []struct {
		in     string
		want   string
		wantOK bool
	}{
		// A subcommand-style tool is remembered by program and subcommand, so
		// the rule outlives this invocation's operands.
		{`git commit -m "fix typo"`, "git commit", true},
		{`git --no-pager revert cb9df32f --no-edit`, "git --no-pager revert", true},
		{`git -C /repo status`, "git -C /repo status", true},
		{`cargo install cargo-insta`, "cargo install", true},
		// "npm run" is a banned prefix (it would allow every package script),
		// so the whole command stays the proposal.
		{`npm run build`, "npm run build", true},
		{`NODE_ENV=production npm run build`, "NODE_ENV=production npm run build", true},
		{"cat <<'EOF'\nnpm run build\nEOF", "", false},
		{"cat <<-EOF > /tmp/example.go\n\tpackage main\nEOF\ngo run /tmp/example.go", "", false},
		{`go run "literal << marker"`, "go run", true},
		{`go run literal\ \<\<\ marker`, "go run", true},
		// Quoting survives on the commands that keep their operands.
		{`grep -n "foo bar" internal/foo.go`, `grep -n 'foo bar' internal/foo.go`, true},
		{`MY_VAR=1 npm run build`, "MY_VAR=1 npm run build", true},
		{`sudo ls`, "sudo ls", true},
		{`bash -c "echo hi"`, `bash -c 'echo hi'`, true},
		{`gofmt -w internal/foo.go internal/bar.go`, "gofmt -w internal/foo.go internal/bar.go", true},
		{`NODE_ENV=production gofmt -w internal/foo.go`, "NODE_ENV=production gofmt -w internal/foo.go", true},
		{`MY_VAR=1 gofmt -w internal/foo.go`, "MY_VAR=1 gofmt -w internal/foo.go", true},
		{`gofmt -w internal/foo.go && rm -rf /tmp/x`, "", false},
		{`ls -la`, "ls -la", true},
		// A file redirection hands the command a path the prefix does not
		// name, so it keeps the whole command as the proposal.
		{`npm run build > /tmp/out.log`, "", false},
		{`git commit -m "x" > /dev/null 2>&1`, "", false},
		{`cargo test 2>/dev/null`, "", false},
		{`git status &> /dev/null`, "", false},
		{`go test ./... >&out.log`, "", false},
		// A descriptor duplication names no path and no command: it only
		// rewires the streams of the command the prefix already spells out.
		{`go test ./... 2>&1`, "go test", true},
		{`go build 1>&2`, "go build", true},
		{`gofmt -l . 2>&-`, `gofmt -l . '2>&-'`, true},
	}
	for _, tc := range tests {
		got, ok := GetSimpleCommandPrefix(tc.in)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("input=%q want=(%q,%v) got=(%q,%v)", tc.in, tc.want, tc.wantOK, got, ok)
		}
	}
}

// Approval surfaces word the persistent choice from this, so a prefix holding
// the whole command must not be announced as one that commands merely start
// with.
func TestExecPolicyAmendmentTruncatesCommand(t *testing.T) {
	for _, tc := range []struct {
		command string
		prefix  []string
		want    bool
	}{
		{"git --no-pager revert cb9df32f --no-edit", []string{"git", "--no-pager", "revert"}, true},
		{"cd /repo && go test ./...", []string{"go", "test"}, true},
		{"gofmt -w internal/foo.go", []string{"gofmt", "-w", "internal/foo.go"}, false},
		{"sudo ls", []string{"sudo", "ls"}, false},
		{"cd /repo && npm run build", []string{"npm", "run", "build"}, false},
		{"go test ./...", []string{"cargo", "build"}, false},
		{"go test ./...", nil, false},
	} {
		if got := ExecPolicyAmendmentTruncatesCommand(tc.prefix, tc.command); got != tc.want {
			t.Errorf("ExecPolicyAmendmentTruncatesCommand(%q, %q) = %v, want %v", tc.prefix, tc.command, got, tc.want)
		}
	}
}

func TestConvertToRuntimeConfigMergesWorkspaceRoots(t *testing.T) {
	cwd := t.TempDir()
	cfgRoot := t.TempDir()
	tempDir := t.TempDir()
	snap := Snapshot{}
	cfg := ConvertToRuntimeConfig([]SourceSettings{{
		Source:                SettingsLocal,
		RootPath:              cfgRoot,
		SandboxMode:           appcfg.SandboxModeWorkspaceWrite,
		SandboxWorkspaceWrite: appcfg.SandboxWorkspaceWrite{WritableRoots: []string{"./cache"}},
	}}, snap, cwd, cwd, tempDir, []string{filepath.Join(cwd, "allowed-root")})

	assertContains(t, cfg.Filesystem.AllowWrite, cwd)
	assertContains(t, cfg.Filesystem.AllowWrite, tempDir)
	if filepath.Separator == '/' {
		assertContains(t, cfg.Filesystem.AllowWrite, "/tmp")
	}
	assertContains(t, cfg.Filesystem.AllowWrite, filepath.Join(cfgRoot, "cache"))
}

func TestConvertToRuntimeConfigHonorsWorkspaceTempExclusions(t *testing.T) {
	cwd := t.TempDir()
	tempDir := t.TempDir()
	cfg := ConvertToRuntimeConfig([]SourceSettings{{
		Source:      SettingsLocal,
		RootPath:    cwd,
		SandboxMode: appcfg.SandboxModeWorkspaceWrite,
		SandboxWorkspaceWrite: appcfg.SandboxWorkspaceWrite{
			ExcludeTmpdirEnvVar: true,
			ExcludeSlashTmp:     true,
		},
	}}, Snapshot{}, cwd, cwd, tempDir, nil)
	assertNotContains(t, cfg.Filesystem.AllowWrite, tempDir)
	assertNotContains(t, cfg.Filesystem.AllowWrite, "/tmp")
}

func TestConvertToRuntimeConfigAppliesFileSystemGrants(t *testing.T) {
	readPath := filepath.Join(t.TempDir(), "read")
	writePath := filepath.Join(t.TempDir(), "write")
	denyPath := filepath.Join(t.TempDir(), "deny")
	cfg := ConvertToRuntimeConfig(nil, Snapshot{FileSystemGrants: []FileSystemPermissionGrant{
		{Entry: FileSystemPermissionEntry{Path: FileSystemPermissionPath{Path: readPath}, Access: FileSystemAccessRead}, Scope: GrantScopeSession},
		{Entry: FileSystemPermissionEntry{Path: FileSystemPermissionPath{Path: writePath}, Access: FileSystemAccessWrite}, Scope: GrantScopeSession},
		{Entry: FileSystemPermissionEntry{Path: FileSystemPermissionPath{Path: denyPath}, Access: FileSystemAccessDeny}, Scope: GrantScopeSession},
	}}, t.TempDir(), t.TempDir(), t.TempDir(), nil)
	assertContains(t, cfg.Filesystem.AllowRead, readPath)
	assertContains(t, cfg.Filesystem.AllowRead, writePath)
	assertContains(t, cfg.Filesystem.AllowWrite, writePath)
	assertContains(t, cfg.Filesystem.DenyRead, denyPath)
	assertContains(t, cfg.Filesystem.DenyWrite, denyPath)
}

func TestResolveSandboxPathExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	if got := resolveSandboxPath("/workspace", "~/cache"); got != filepath.Join(home, "cache") {
		t.Fatalf("expanded path = %q", got)
	}
}

func assertContains(t *testing.T, list []string, want string) {
	t.Helper()
	for _, item := range list {
		if item == want {
			return
		}
	}
	t.Fatalf("missing %q in %#v", want, list)
}

func assertNotContains(t *testing.T, list []string, want string) {
	t.Helper()
	for _, item := range list {
		if item == want {
			t.Fatalf("did not expect %q in %#v", want, list)
		}
	}
}

func TestEffectiveConfigDerivesOnlyOmittedSandboxMode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		launch   ProjectContext
		wantMode appcfg.SandboxMode
	}{
		{
			name:     "trusted",
			launch:   ProjectContext{Project: Project{Root: t.TempDir(), VersionControlled: true}, TrustLevel: LevelTrusted},
			wantMode: appcfg.SandboxModeWorkspaceWrite,
		},
		{
			// A rejected project is restrained by its approval policy, not by a
			// read-only filesystem.
			name:     "explicitly rejected",
			launch:   ProjectContext{Project: Project{Root: t.TempDir(), VersionControlled: true}, TrustLevel: LevelUntrusted},
			wantMode: appcfg.SandboxModeWorkspaceWrite,
		},
		{
			// Version control is not part of the decision; an explicit judgement is.
			name:     "trusted without version control",
			launch:   ProjectContext{Project: Project{Root: t.TempDir()}, TrustLevel: LevelTrusted},
			wantMode: appcfg.SandboxModeWorkspaceWrite,
		},
		{
			name:     "undecided",
			launch:   ProjectContext{Project: Project{Root: t.TempDir(), VersionControlled: true}},
			wantMode: appcfg.SandboxModeReadOnly,
		},
		{
			name:     "undecided and empty",
			launch:   ProjectContext{},
			wantMode: appcfg.SandboxModeReadOnly,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveConfig(appcfg.Root{}, tc.launch).SandboxMode; got != tc.wantMode {
				t.Fatalf("mode = %q, want %q", got, tc.wantMode)
			}
		})
	}
}

// Windows without a configured sandbox cannot enforce a writable workspace, so
// the derivation stays read-only there no matter what the user decided.
func TestEffectiveSandboxModeRequiresEnforcementOnWindows(t *testing.T) {
	for _, level := range []Level{LevelTrusted, LevelUntrusted} {
		launch := ProjectContext{Project: Project{Root: t.TempDir(), VersionControlled: true}, TrustLevel: level}

		if got := effectiveSandboxMode(launch, PlatformWindows, ""); got != appcfg.SandboxModeReadOnly {
			t.Errorf("%s on windows without a sandbox = %q, want read-only", level, got)
		}
		if got := effectiveSandboxMode(launch, PlatformWindows, appcfg.WindowsSandboxUnelevated); got != appcfg.SandboxModeWorkspaceWrite {
			t.Errorf("%s on windows with a sandbox = %q, want workspace-write", level, got)
		}
		if got := effectiveSandboxMode(launch, PlatformDarwin, ""); got != appcfg.SandboxModeWorkspaceWrite {
			t.Errorf("%s off windows = %q, want workspace-write", level, got)
		}
	}

	undecided := ProjectContext{Project: Project{Root: t.TempDir(), VersionControlled: true}}
	if got := effectiveSandboxMode(undecided, PlatformDarwin, ""); got != appcfg.SandboxModeReadOnly {
		t.Errorf("undecided project = %q, want read-only", got)
	}
}

func TestEffectiveConfigPreservesExplicitModesAndPermissionProfiles(t *testing.T) {
	launch := ProjectContext{Project: Project{VersionControlled: true}, TrustLevel: LevelTrusted}
	for _, mode := range []appcfg.SandboxMode{appcfg.SandboxModeReadOnly, appcfg.SandboxModeWorkspaceWrite, appcfg.SandboxModeDangerFullAccess} {
		if got := EffectiveConfig(appcfg.Root{SandboxMode: mode}, launch).SandboxMode; got != mode {
			t.Fatalf("explicit mode = %q, want %q", got, mode)
		}
	}
	cfg := EffectiveConfig(appcfg.Root{DefaultPermissions: "custom"}, launch)
	if cfg.DefaultPermissions != "custom" || cfg.SandboxMode != "" {
		t.Fatalf("named profile was changed: %+v", cfg)
	}
}

func TestEffectiveConfigDerivesApprovalPolicyFromTrust(t *testing.T) {
	for _, tc := range []struct {
		name   string
		launch ProjectContext
		want   appcfg.ApprovalPolicyMode
	}{
		{
			name:   "trusted project asks only to leave the sandbox",
			launch: ProjectContext{TrustLevel: LevelTrusted},
			want:   appcfg.ApprovalPolicyOnRequest,
		},
		{
			name:   "explicitly rejected project asks for anything unsafe",
			launch: ProjectContext{TrustLevel: LevelUntrusted},
			want:   appcfg.ApprovalPolicyUntrusted,
		},
		{
			// A project nobody has judged is not the same as a rejected one.
			name:   "undecided project keeps the plain default",
			launch: ProjectContext{TrustLevel: LevelUnknown},
			want:   appcfg.ApprovalPolicyOnRequest,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveConfig(appcfg.Root{}, tc.launch).ApprovalPolicy.Mode; got != tc.want {
				t.Fatalf("approval policy = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEffectiveConfigKeepsConfiguredApprovalPolicy(t *testing.T) {
	untrusted := ProjectContext{TrustLevel: LevelUntrusted}
	for _, mode := range []appcfg.ApprovalPolicyMode{
		appcfg.ApprovalPolicyOnRequest, appcfg.ApprovalPolicyNever, appcfg.ApprovalPolicyUntrusted,
	} {
		cfg := appcfg.Root{ApprovalPolicy: appcfg.NewApprovalPolicy(mode)}
		if got := EffectiveConfig(cfg, untrusted).ApprovalPolicy.Mode; got != mode {
			t.Fatalf("configured policy %q became %q", mode, got)
		}
	}

	granular := appcfg.ApprovalPolicyConfig{
		Mode:     appcfg.ApprovalPolicyGranular,
		Granular: appcfg.GranularApprovalConfig{SandboxApproval: true, Rules: true, MCPElicitations: true},
	}
	got := EffectiveConfig(appcfg.Root{ApprovalPolicy: granular}, untrusted).ApprovalPolicy
	if got.Mode != appcfg.ApprovalPolicyGranular || !got.Granular.SandboxApproval {
		t.Fatalf("granular policy was rewritten: %+v", got)
	}
}

// The two derivations are independent: configuring one must not freeze the
// other at its unresolved value.
func TestEffectiveConfigDerivesApprovalPolicyAlongsideExplicitSandbox(t *testing.T) {
	untrusted := ProjectContext{TrustLevel: LevelUntrusted}

	cfg := EffectiveConfig(appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}, untrusted)
	if cfg.ApprovalPolicy.Mode != appcfg.ApprovalPolicyUntrusted {
		t.Errorf("explicit sandbox mode suppressed the approval derivation: %q", cfg.ApprovalPolicy.Mode)
	}
	if cfg.SandboxMode != appcfg.SandboxModeDangerFullAccess {
		t.Errorf("explicit sandbox mode = %q", cfg.SandboxMode)
	}

	cfg = EffectiveConfig(appcfg.Root{DefaultPermissions: "custom"}, untrusted)
	if cfg.ApprovalPolicy.Mode != appcfg.ApprovalPolicyUntrusted {
		t.Errorf("named permission profile suppressed the approval derivation: %q", cfg.ApprovalPolicy.Mode)
	}

	cfg = EffectiveConfig(appcfg.Root{ApprovalPolicy: appcfg.NewApprovalPolicy(appcfg.ApprovalPolicyNever)}, ProjectContext{
		Project: Project{Root: t.TempDir(), VersionControlled: true}, TrustLevel: LevelTrusted,
	})
	if cfg.SandboxMode != appcfg.SandboxModeWorkspaceWrite {
		t.Errorf("explicit approval policy suppressed the sandbox derivation: %q", cfg.SandboxMode)
	}
}

// Persisting the derived value would pin one branch of the derivation, so an
// unconfigured policy must survive a save/load cycle as unconfigured.
func TestUnconfiguredApprovalPolicyIsNotPersisted(t *testing.T) {
	path := t.TempDir() + "/forebrain.yaml"
	if err := appcfg.Save(path, appcfg.Root{}); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := appcfg.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.ApprovalPolicy.Mode != "" {
		t.Fatalf("approval policy was persisted as %q, which disables the trust derivation", loaded.ApprovalPolicy.Mode)
	}
	if got := EffectiveConfig(loaded, ProjectContext{TrustLevel: LevelUntrusted}).ApprovalPolicy.Mode; got != appcfg.ApprovalPolicyUntrusted {
		t.Fatalf("derivation after save/load = %q", got)
	}
}

func TestConvertToRuntimeConfigDeniesMetadataDirsAndGitExecutionSurfaces(t *testing.T) {
	cwd := t.TempDir()
	original := t.TempDir()

	cfg := ConvertToRuntimeConfig(nil, Snapshot{}, cwd, original, t.TempDir(), nil)

	assertContains(t, cfg.Filesystem.DenyWrite, filepath.Join(cwd, ".agents"))
	assertContains(t, cfg.Filesystem.DenyWrite, filepath.Join(cwd, ".forebrain"))
	assertContains(t, cfg.Filesystem.DenyWrite, filepath.Join(cwd, ".claude"))
	assertContains(t, cfg.Filesystem.DenyWrite, filepath.Join(cwd, ".codex"))
	assertContains(t, cfg.Filesystem.DenyWrite, filepath.Join(original, ".forebrain"))
	// Git executes hooks and the programs its config names, on the host and
	// without an approval, so those stay denied in both roots.
	assertContains(t, cfg.Filesystem.DenyWrite, filepath.Join(cwd, ".git", "hooks"))
	assertContains(t, cfg.Filesystem.DenyWrite, filepath.Join(cwd, ".git", "config"))
	assertContains(t, cfg.Filesystem.DenyWrite, filepath.Join(cwd, ".git", "config.worktree"))
	assertContains(t, cfg.Filesystem.DenyWrite, filepath.Join(cwd, ".git", "modules"))
	assertContains(t, cfg.Filesystem.DenyWrite, filepath.Join(original, ".git", "hooks"))
	// The rest of the repository carries the workspace's ordinary write
	// permission: denying it made every git lock file an approval prompt.
	assertNotContains(t, cfg.Filesystem.DenyWrite, filepath.Join(cwd, ".git"))
	assertNotContains(t, cfg.Filesystem.DenyWrite, filepath.Join(cwd, ".git", "index.lock"))
	assertNotContains(t, cfg.Filesystem.DenyWrite, filepath.Join(cwd, ".git", "refs"))
	assertNotContains(t, cfg.Filesystem.DenyWrite, filepath.Join(cwd, ".git", "objects"))
}

func TestConvertToRuntimeConfigDeniesResolvedWorktreeExecutionSurfaces(t *testing.T) {
	// A worktree or submodule checkout keeps its .git as a file naming the real
	// repository directory, which for a submodule sits back inside the
	// workspace. Its hooks and config are the same execution surface.
	root := t.TempDir()
	worktreeGitDir := filepath.Join(root, ".git-data", "worktrees", "main")
	commonDir := filepath.Join(root, ".git-data")
	requireMkdirAll(t, worktreeGitDir)
	requireWriteFile(t, filepath.Join(root, ".git"), "gitdir: .git-data/worktrees/main\n")
	requireWriteFile(t, filepath.Join(worktreeGitDir, "commondir"), "../..\n")

	cfg := ConvertToRuntimeConfig(nil, Snapshot{}, root, root, t.TempDir(), nil)
	assertContains(t, cfg.Filesystem.DenyWrite, filepath.Join(worktreeGitDir, "hooks"))
	assertContains(t, cfg.Filesystem.DenyWrite, filepath.Join(worktreeGitDir, "config"))
	assertContains(t, cfg.Filesystem.DenyWrite, filepath.Join(commonDir, "hooks"))
	assertContains(t, cfg.Filesystem.DenyWrite, filepath.Join(commonDir, "config"))
	assertNotContains(t, cfg.Filesystem.DenyWrite, filepath.Join(root, ".git"))
	assertNotContains(t, cfg.Filesystem.DenyWrite, worktreeGitDir)
	assertNotContains(t, cfg.Filesystem.DenyWrite, commonDir)
}

func requireWriteFile(t *testing.T, path, body string) {
	t.Helper()
	requireMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func requireMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func TestExpandDeniedReadPatternsHonorsDepth(t *testing.T) {
	root := t.TempDir()
	shallow := filepath.Join(root, "one.env")
	deep := filepath.Join(root, "a", "b", "two.env")
	if err := os.MkdirAll(filepath.Dir(deep), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{shallow, deep} {
		if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	depth := 1
	matches, err := ExpandDeniedReadPatterns([]string{filepath.Join(root, "**", "*.env")}, root, &depth)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0] != shallow {
		t.Fatalf("matches=%v want=[%s]", matches, shallow)
	}
}

type stubLLM struct {
	fn func(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error)
}

func (s *stubLLM) Execute(ctx context.Context, msgs []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	return s.fn(ctx, msgs, tools)
}

func okLLM(text string) *stubLLM {
	return &stubLLM{fn: func(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
		return &llm.Result{Message: &llm.Message{Parts: []llm.ContentPart{llm.Text(text)}}}, nil
	}}
}

func errLLM(err error) *stubLLM {
	return &stubLLM{fn: func(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
		return nil, err
	}}
}

type outputFixtureFile struct {
	Cases []struct {
		ID                string `yaml:"id"`
		AssistantText     string `yaml:"assistant_text"`
		ExpectOutputBlock bool   `yaml:"expect_output_block"`
	} `yaml:"cases"`
}

// TestRedteamOutputFixtures exercises the output rail against red-team fixtures.
// The retrieval half lives in internal/guardrails (TestRedteamFixtures).
func TestRedteamOutputFixtures(t *testing.T) {
	raw, err := os.ReadFile("redteam/fixtures.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var f outputFixtureFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	r := StrictDefaults()
	for _, c := range f.Cases {
		if c.AssistantText == "" {
			continue
		}
		t.Run(c.ID, func(t *testing.T) {
			_, err := ApplyOutputRail(r, c.AssistantText)
			blocked := err != nil
			if blocked != c.ExpectOutputBlock {
				t.Fatalf("output block=%v want=%v err=%v", blocked, c.ExpectOutputBlock, err)
			}
		})
	}
}

func TestRunInputRules(t *testing.T) {
	r := StrictDefaults()
	r.InputMaxRunes = 10
	if err := RunInputRules(r, "short"); err != nil {
		t.Fatalf("short input should pass: %v", err)
	}
	if err := RunInputRules(r, "this is way too long"); err == nil {
		t.Fatal("over-length input should be blocked")
	}
	r2 := StrictDefaults()
	r2.InputBlockSubs = []string{"SeCrEt"}
	if err := RunInputRules(r2, "leak the secret now"); err == nil {
		t.Fatal("blocked substring should be rejected (case-insensitive)")
	}
	if err := RunInputRules(r2, "nothing to see"); err != nil {
		t.Fatalf("clean input should pass: %v", err)
	}
}

var errGuard = errors.New("guard blocked")

// passMessageGuard is a MessageGuard that always passes.
func passMessageGuard(_ context.Context, _ []llm.Message) error { return nil }

// blockMessageGuard is a MessageGuard that always blocks.
func blockMessageGuard(_ context.Context, _ []llm.Message) error { return errGuard }

// passResultGuard is a ResultGuard that always passes.
func passResultGuard(_ context.Context, _ *llm.Result) error { return nil }

// blockResultGuard is a ResultGuard that always blocks.
func blockResultGuard(_ context.Context, _ *llm.Result) error { return errGuard }

// TestGuardrails_NoGuards verifies that with no guards the result is passed through unchanged.
func TestGuardrails_NoGuards(t *testing.T) {
	mw := NewGuardrails()
	client := llm.Use(okLLM("hello"), mw)

	result, err := client.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Message.TextContent() != "hello" {
		t.Fatalf("got %q, want %q", result.Message.TextContent(), "hello")
	}
}

// TestGuardrails_MessageGuard_Pass verifies that a passing input guard does not block.
func TestGuardrails_MessageGuard_Pass(t *testing.T) {
	mw := NewGuardrails(WithMessageGuard("allow-all", passMessageGuard))
	client := llm.Use(okLLM("ok"), mw)

	_, err := client.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

// TestGuardrails_MessageGuard_Block verifies that a failing input guard stops execution.
func TestGuardrails_MessageGuard_Block(t *testing.T) {
	mw := NewGuardrails(WithMessageGuard("block", blockMessageGuard))
	reached := false
	inner := &stubLLM{fn: func(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
		reached = true
		return nil, nil
	}}
	client := llm.Use(inner, mw)

	_, err := client.Execute(context.Background(), nil, nil)

	var gErr *GuardrailError
	if !errors.As(err, &gErr) {
		t.Fatalf("got %T %v, want GuardrailError", err, err)
	}
	if gErr.Stage != "input" {
		t.Fatalf("Stage=%q, want %q", gErr.Stage, "input")
	}
	if gErr.Name != "block" {
		t.Fatalf("Name=%q, want %q", gErr.Name, "block")
	}
	if !errors.Is(err, errGuard) {
		t.Fatal("Unwrap should yield errGuard")
	}
	if reached {
		t.Fatal("inner LLM should not have been called")
	}
}

// TestGuardrails_ResultGuard_Pass verifies that a passing output guard lets the result through.
func TestGuardrails_ResultGuard_Pass(t *testing.T) {
	mw := NewGuardrails(WithResultGuard("allow-all", passResultGuard))
	client := llm.Use(okLLM("ok"), mw)

	result, err := client.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Message.TextContent() != "ok" {
		t.Fatalf("got %q, want %q", result.Message.TextContent(), "ok")
	}
}

// TestGuardrails_ResultGuard_Block verifies that a failing output guard rejects the result.
func TestGuardrails_ResultGuard_Block(t *testing.T) {
	mw := NewGuardrails(WithResultGuard("block", blockResultGuard))
	client := llm.Use(okLLM("ok"), mw)

	_, err := client.Execute(context.Background(), nil, nil)

	var gErr *GuardrailError
	if !errors.As(err, &gErr) {
		t.Fatalf("got %T %v, want GuardrailError", err, err)
	}
	if gErr.Stage != "output" {
		t.Fatalf("Stage=%q, want %q", gErr.Stage, "output")
	}
	if gErr.Name != "block" {
		t.Fatalf("Name=%q, want %q", gErr.Name, "block")
	}
	if !errors.Is(err, errGuard) {
		t.Fatal("Unwrap should yield errGuard")
	}
}

// TestGuardrails_InnerError verifies that inner LLM errors bypass result guards.
func TestGuardrails_InnerError(t *testing.T) {
	sentinel := errors.New("inner error")
	mw := NewGuardrails(WithResultGuard("block", blockResultGuard))
	client := llm.Use(errLLM(sentinel), mw)

	_, err := client.Execute(context.Background(), nil, nil)
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want sentinel inner error", err)
	}
}

// TestGuardrails_GuardOrder verifies that guards are checked in the order added.
func TestGuardrails_GuardOrder(t *testing.T) {
	order := []string{}

	guard := func(name string) MessageGuard {
		return func(_ context.Context, _ []llm.Message) error {
			order = append(order, name)
			return nil
		}
	}

	mw := NewGuardrails(
		WithMessageGuard("first", guard("first")),
		WithMessageGuard("second", guard("second")),
	)
	client := llm.Use(okLLM("ok"), mw)

	_, err := client.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("guard order: %v", order)
	}
}

func TestSetMode_OverwritesApprovalPolicy(t *testing.T) {
	store := NewStore()

	// Set up an approval policy
	customPolicy := ApprovalPolicy{
		Mode: ApprovalGranular,
		Granular: GranularApprovalConfig{
			SkillApproval:   true,
			MCPElicitations: true,
		},
	}
	store.SetApprovalPolicy(customPolicy)

	// Verify the policy is set
	policy := store.ApprovalPolicy()
	if policy.Mode != ApprovalGranular {
		t.Fatalf("expected granular mode, got %s", policy.Mode)
	}

	// SetMode (not SetModeWithoutPolicySync) should overwrite policy
	store.SetMode(ModeNever)

	// Verify mode changed
	if store.Mode() != ModeNever {
		t.Fatalf("mode not changed: got %s, want %s", store.Mode(), ModeNever)
	}

	// The approval policy mode should also be updated to match
	policy = store.ApprovalPolicy()
	if policy.Mode != ApprovalNever {
		t.Fatalf("approval policy mode not updated: got %s, want %s", policy.Mode, ApprovalNever)
	}
}

func TestCommandCaptureBoundsMemoryAndSpoolsFullOutput(t *testing.T) {
	dir := t.TempDir()
	capture := newCommandCapture(CommandRequest{OutputSpoolDir: dir})
	input := bytes.Repeat([]byte("0123456789abcdef\n"), 20_000)
	if _, err := capture.writer(OutputStreamStderr).Write(input); err != nil {
		t.Fatal(err)
	}
	result := capture.result()
	if len(result.Stderr) > commandPreviewHeadBytes+commandPreviewTailBytes+100 {
		t.Fatalf("preview retained %d bytes", len(result.Stderr))
	}
	if result.StderrBytes != int64(len(input)) || result.StderrOmittedBytes <= 0 {
		t.Fatalf("accounting=%+v", result)
	}
	if !strings.Contains(result.Stderr, "output bytes omitted") {
		t.Fatalf("missing omission marker: %q", result.Stderr)
	}
	data, err := os.ReadFile(result.OutputSpoolPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, input) {
		t.Fatalf("spool bytes=%d want=%d", len(data), len(input))
	}
	info, err := os.Stat(result.OutputSpoolPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("spool mode=%o", info.Mode().Perm())
	}
}

func TestCommandCaptureRemovesSpoolForSmallOutput(t *testing.T) {
	dir := t.TempDir()
	capture := newCommandCapture(CommandRequest{OutputSpoolDir: dir})
	_, _ = capture.writer(OutputStreamStdout).Write([]byte("ok\n"))
	result := capture.result()
	if result.Stdout != "ok\n" || result.OutputSpoolPath != "" {
		t.Fatalf("result=%+v", result)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("small output left spool files: %v", entries)
	}
}

func TestConvertToRuntimeConfigCompilesNamedPermissionProfile(t *testing.T) {
	cwd := t.TempDir()
	tempDir := t.TempDir()
	extraRoot := filepath.Join(t.TempDir(), "shared")
	depth := 3
	enabled := true
	cfg := &appcfg.Root{
		DefaultPermissions: "locked",
		Permissions: appcfg.PermissionProfiles{"locked": {
			WorkspaceRoots: map[string]bool{extraRoot: true},
			FileSystem: &appcfg.FileSystemPermissions{GlobScanMaxDepth: &depth, Entries: map[string]appcfg.FileSystemPermissionValue{
				":minimal":           {Access: appcfg.FileSystemAccessRead},
				":workspace_roots":   {Access: appcfg.FileSystemAccessWrite},
				":tmpdir":            {Access: appcfg.FileSystemAccessWrite},
				"/private/secret":    {Access: appcfg.FileSystemAccessDeny},
				"/workspace/**/.env": {Access: appcfg.FileSystemAccessDeny},
			}},
			Network: &appcfg.NetworkPermissionConfig{Enabled: &enabled},
		}},
	}
	runtimeCfg := ConvertToRuntimeConfig(LocalConfigSources(cwd, cfg), Snapshot{}, cwd, cwd, tempDir, nil)
	assertContains(t, runtimeCfg.Filesystem.AllowWrite, cwd)
	assertContains(t, runtimeCfg.Filesystem.AllowWrite, extraRoot)
	assertContains(t, runtimeCfg.Filesystem.AllowWrite, tempDir)
	assertContains(t, runtimeCfg.Filesystem.DenyRead, "/private/secret")
	if len(runtimeCfg.Filesystem.DenyReadPatterns) != 1 || runtimeCfg.Filesystem.GlobScanMaxDepth == nil || *runtimeCfg.Filesystem.GlobScanMaxDepth != depth {
		t.Fatalf("glob policy=%+v", runtimeCfg.Filesystem)
	}
	if !runtimeCfg.NetworkEnabled || !runtimeCfg.Filesystem.IncludePlatformDefaults || ProfileForConfig(cfg) != ProfileManaged {
		t.Fatalf("network=%t profile=%s", runtimeCfg.NetworkEnabled, ProfileForConfig(cfg))
	}
}

func TestBuiltInReadOnlyProfileHasNoWritablePaths(t *testing.T) {
	cfg := &appcfg.Root{DefaultPermissions: appcfg.PermissionProfileReadOnly, SandboxMode: appcfg.SandboxModeReadOnly}
	runtimeCfg := ConvertToRuntimeConfig(LocalConfigSources(t.TempDir(), cfg), Snapshot{}, t.TempDir(), t.TempDir(), t.TempDir(), nil)
	if len(runtimeCfg.Filesystem.AllowWrite) != 0 {
		t.Fatalf("allow write=%v", runtimeCfg.Filesystem.AllowWrite)
	}
}

// The three presets and their pairings mirror codex's builtin_approval_presets.
func TestBuiltinApprovalPresets(t *testing.T) {
	presets := BuiltinApprovalPresets()
	if len(presets) != 3 {
		t.Fatalf("expected three presets, got %d", len(presets))
	}
	want := []ApprovalPreset{
		{ID: PresetReadOnly, Label: "Read Only", Approval: ApprovalOnRequest, SandboxMode: appcfg.SandboxModeReadOnly},
		{ID: PresetDefault, Label: "Default", Approval: ApprovalOnRequest, SandboxMode: appcfg.SandboxModeWorkspaceWrite},
		{ID: PresetFullAccess, Label: "Full Access", Approval: ApprovalNever, SandboxMode: appcfg.SandboxModeDangerFullAccess},
	}
	for i, w := range want {
		got := presets[i]
		if got.ID != w.ID || got.Label != w.Label || got.Approval != w.Approval || got.SandboxMode != w.SandboxMode {
			t.Fatalf("preset %d = %+v, want %+v", i, got, w)
		}
		if got.Description == "" {
			t.Fatalf("preset %q has no description", got.ID)
		}
	}
}

func TestApprovalPresetByIDAcceptsIDAndLabel(t *testing.T) {
	for _, raw := range []string{"full-access", "Full Access", "FULL ACCESS"} {
		preset, ok := ApprovalPresetByID(raw)
		if !ok || preset.ID != PresetFullAccess {
			t.Fatalf("ApprovalPresetByID(%q) = %+v, %v", raw, preset, ok)
		}
	}
	for _, raw := range []string{"", "  ", "yolo", "danger-full-access"} {
		if _, ok := ApprovalPresetByID(raw); ok {
			t.Fatalf("ApprovalPresetByID(%q) unexpectedly resolved", raw)
		}
	}
}

func TestMatchApprovalPreset(t *testing.T) {
	// Read Only and Default share an approval mode, so only the sandbox tells
	// them apart — the match has to consider both halves.
	readOnly := &appcfg.Root{SandboxMode: appcfg.SandboxModeReadOnly}
	if preset, ok := MatchApprovalPreset(ModeOnRequest, readOnly); !ok || preset.ID != PresetReadOnly {
		t.Fatalf("read-only match = %+v, %v", preset, ok)
	}
	workspace := &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite}
	if preset, ok := MatchApprovalPreset(ModeOnRequest, workspace); !ok || preset.ID != PresetDefault {
		t.Fatalf("default match = %+v, %v", preset, ok)
	}

	// A state between presets matches none of them rather than the nearest one.
	if preset, ok := MatchApprovalPreset(ModeNever, readOnly); ok {
		t.Fatalf("expected no match for a never-mode read-only sandbox, got %+v", preset)
	}
	if preset, ok := MatchApprovalPreset(ModeUnlessTrusted, workspace); ok {
		t.Fatalf("expected no match for unless-trusted, got %+v", preset)
	}
	if preset, ok := MatchApprovalPreset(ModeOnRequest, nil); ok {
		t.Fatalf("expected no match for a nil config, got %+v", preset)
	}

	// A named profile is finer-grained than any preset expresses.
	named := &appcfg.Root{SandboxMode: appcfg.SandboxModeReadOnly, DefaultPermissions: appcfg.PermissionProfileReadOnly}
	if preset, ok := MatchApprovalPreset(ModeOnRequest, named); ok {
		t.Fatalf("expected no match while a named profile is selected, got %+v", preset)
	}
}

// A named profile outranks sandbox_mode, so applying a preset has to clear it
// or the preset's sandbox would be selected and then ignored.
func TestApprovalPresetApplyToConfigClearsNamedProfile(t *testing.T) {
	cfg := &appcfg.Root{
		SandboxMode:        appcfg.SandboxModeReadOnly,
		DefaultPermissions: appcfg.PermissionProfileReadOnly,
	}
	preset, ok := ApprovalPresetByID(PresetFullAccess)
	if !ok {
		t.Fatal("full-access preset missing")
	}
	preset.ApplyToConfig(cfg)
	if cfg.SandboxMode != appcfg.SandboxModeDangerFullAccess {
		t.Fatalf("sandbox mode = %q", cfg.SandboxMode)
	}
	if cfg.DefaultPermissions != "" {
		t.Fatalf("default permissions = %q, want cleared", cfg.DefaultPermissions)
	}
	preset.ApplyToConfig(nil) // must not panic
}

// A preset picked for one conversation scopes both halves to it: the
// configuration is left alone, the conversation's snapshot carries its own
// approval policy and sandbox, and every other conversation keeps the
// configured ones.
func TestApprovalPresetSessionUpdatesScopeBothHalvesToTheConversation(t *testing.T) {
	t.Setenv(EnvYOLO, "")
	cfg := &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite, ApprovalPolicy: appcfg.NewApprovalPolicy(appcfg.ApprovalPolicyOnRequest)}
	rt := NewRuntime()
	preset, ok := ApprovalPresetByID(PresetFullAccess)
	if !ok {
		t.Fatal("full-access preset missing")
	}
	for _, update := range preset.SessionUpdates("s1") {
		rt.ApplyUpdate(update, cfg, Paths{})
	}

	if cfg.SandboxMode != appcfg.SandboxModeWorkspaceWrite || cfg.ApprovalPolicy.Mode != appcfg.ApprovalPolicyOnRequest {
		t.Fatalf("config moved: sandbox=%q approval=%q", cfg.SandboxMode, cfg.ApprovalPolicy.Mode)
	}
	owning := rt.SnapshotForSession("s1", cfg)
	if owning.Mode != ModeNever || owning.ApprovalPolicy.Mode != ApprovalNever || owning.SandboxMode != appcfg.SandboxModeDangerFullAccess {
		t.Fatalf("owning snapshot = mode %q, policy %q, sandbox %q", owning.Mode, owning.ApprovalPolicy.Mode, owning.SandboxMode)
	}
	if got := ConfigForSnapshot(cfg, owning); got.SandboxMode != appcfg.SandboxModeDangerFullAccess || got == cfg {
		t.Fatalf("owning config = %+v", got)
	}
	other := rt.SnapshotForSession("s2", cfg)
	if other.Mode != ModeOnRequest || other.ApprovalPolicy.Mode != ApprovalOnRequest || other.SandboxMode != "" {
		t.Fatalf("other snapshot = mode %q, policy %q, sandbox %q", other.Mode, other.ApprovalPolicy.Mode, other.SandboxMode)
	}
	if got := ConfigForSnapshot(cfg, other); got != cfg {
		t.Fatal("a conversation without its own sandbox must run under the configuration itself")
	}

	if d := rt.Evaluate("s1", "shell", "rm -rf /tmp/build-output", cfg, false); d.Reason != "danger_full_access" {
		t.Fatalf("owning decision = %+v", d)
	}
	if d := rt.Evaluate("s2", "shell", "rm -rf /tmp/build-output", cfg, false); d.Reason == "danger_full_access" || d.BypassSandbox {
		t.Fatalf("other decision = %+v", d)
	}
	if rt.Store(cfg).sandboxAvailableForSession("s2") != rt.Store(cfg).SandboxAvailable() {
		t.Fatal("another conversation's containment must be the configured one")
	}
	if rt.Store(cfg).sandboxAvailableForSession("s1") {
		t.Fatal("a full-access conversation has no sandbox containing its commands")
	}
}

// Containment for a conversation's own sandbox is derived the same way the
// configured mode's is, and re-derived when the configuration reloads.
func TestSessionSandboxAvailabilityFollowsTheSessionsMode(t *testing.T) {
	t.Setenv(EnvYOLO, "")
	cfg := &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}
	rt := NewRuntime()
	rt.LoadFromDisk(cfg, Paths{})
	if rt.Store(cfg).SandboxAvailable() {
		t.Fatal("full access is never contained")
	}
	rt.ApplyUpdate(PermissionUpdate{Type: UpdateSetSandboxMode, Destination: DestinationSession, SessionID: "s1", SandboxMode: appcfg.SandboxModeReadOnly}, cfg, Paths{})
	want := shellSandboxAvailable(&appcfg.Root{SandboxMode: appcfg.SandboxModeReadOnly})
	if got := rt.Store(cfg).sandboxAvailableForSession("s1"); got != want {
		t.Fatalf("read-only conversation contained = %v, want %v", got, want)
	}
	rt.LoadFromDisk(cfg, Paths{})
	if got := rt.Store(cfg).sandboxAvailableForSession("s1"); got != want {
		t.Fatalf("after reload contained = %v, want %v", got, want)
	}
	if rt.Store(cfg).sandboxAvailableForSession("s2") {
		t.Fatal("another conversation keeps the configured full access")
	}
}

// A sandbox mode is a conversation's own choice or nothing: the configured
// mode lives in the configuration, so any other destination, a missing
// conversation or a mode no preset picks changes nothing.
func TestSessionSandboxModeAcceptsOnlyAConversationsPresetMode(t *testing.T) {
	store := NewStore()
	for _, update := range []PermissionUpdate{
		{Type: UpdateSetSandboxMode, Destination: DestinationLocalSettings, SessionID: "s1", SandboxMode: appcfg.SandboxModeDangerFullAccess},
		{Type: UpdateSetSandboxMode, Destination: DestinationSession, SessionID: " ", SandboxMode: appcfg.SandboxModeDangerFullAccess},
		{Type: UpdateSetSandboxMode, Destination: DestinationSession, SessionID: "s1", SandboxMode: "external-sandbox"},
	} {
		ApplyUpdate(store, update)
	}
	if got := store.SnapshotForSession("s1").SandboxMode; got != "" {
		t.Fatalf("sandbox mode = %q, want none", got)
	}

	ApplyUpdate(store, PermissionUpdate{Type: UpdateSetSandboxMode, Destination: DestinationSession, SessionID: "s1", SandboxMode: appcfg.SandboxModeReadOnly})
	if got := store.SnapshotForSession("s1").SandboxMode; got != appcfg.SandboxModeReadOnly {
		t.Fatalf("sandbox mode = %q", got)
	}
	// Switching the primary agent drops it with every other runtime grant.
	store.ClearRuntimeGrants()
	if got := store.SnapshotForSession("s1").SandboxMode; got != "" {
		t.Fatalf("sandbox mode after clear = %q", got)
	}
}

// YOLO comes from the environment; a conversation's own sandbox never walks
// it back.
func TestConfigForSnapshotLeavesYOLOInCharge(t *testing.T) {
	t.Setenv(EnvYOLO, "1")
	cfg := &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}
	if got := ConfigForSnapshot(cfg, Snapshot{SandboxMode: appcfg.SandboxModeReadOnly}); got != cfg {
		t.Fatalf("config under YOLO = %+v, want the configuration itself", got)
	}
}

func TestResolveFindsGitDirectoryAndWorktreeFile(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		deep := filepath.Join(repo, "a", "b")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := Resolve(deep)
		if err != nil {
			t.Fatal(err)
		}
		want, err := CanonicalPath(repo)
		if err != nil {
			t.Fatal(err)
		}
		if !got.VersionControlled || got.Root != want {
			t.Fatalf("project=%+v", got)
		}
	})
	t.Run("worktree marker", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: elsewhere\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := Resolve(repo)
		if err != nil {
			t.Fatal(err)
		}
		want, err := CanonicalPath(repo)
		if err != nil {
			t.Fatal(err)
		}
		if !got.VersionControlled || got.Root != want {
			t.Fatalf("project=%+v", got)
		}
	})
}

func TestResolveNonGitAndSymlinkAreCanonical(t *testing.T) {
	plain := t.TempDir()
	project, err := Resolve(plain)
	if err != nil {
		t.Fatal(err)
	}
	want, err := CanonicalPath(plain)
	if err != nil {
		t.Fatal(err)
	}
	if project.VersionControlled || project.Root != want {
		t.Fatalf("project=%+v", project)
	}
	parent := t.TempDir()
	link := filepath.Join(parent, "link")
	if err := os.Symlink(plain, link); err != nil {
		t.Fatal(err)
	}
	project, err = Resolve(link)
	if err != nil {
		t.Fatal(err)
	}
	if project.Root != want {
		t.Fatalf("symlink project=%+v", project)
	}
}

func TestMarkTrustedLoadsLegacyStateAndWritesSortedAtomically(t *testing.T) {
	home := t.TempDir()
	first := t.TempDir()
	second := t.TempDir()
	path := StatePath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\n  \"trusted_directories\": [\n    "+quote(second)+"\n  ]\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MarkTrusted(home, Project{Root: first}); err != nil {
		t.Fatal(err)
	}
	if err := MarkTrusted(home, Project{Root: first}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Count(text, filepath.Clean(first)) != 1 || strings.Count(text, filepath.Clean(second)) != 1 {
		t.Fatalf("state=%s", text)
	}
	if strings.Index(text, filepath.Clean(first)) > strings.Index(text, filepath.Clean(second)) && filepath.Clean(first) < filepath.Clean(second) {
		t.Fatalf("state not sorted: %s", text)
	}
	trusted, err := IsTrusted(home, Project{Root: first})
	if err != nil || !trusted {
		t.Fatalf("trusted=%v err=%v", trusted, err)
	}
}

func TestIsTrustedFailsClosedForMalformedState(t *testing.T) {
	home := t.TempDir()
	path := StatePath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	trusted, err := IsTrusted(home, Project{Root: t.TempDir()})
	if err == nil || trusted {
		t.Fatalf("trusted=%v err=%v", trusted, err)
	}
}

func quote(value string) string { return "\"" + strings.ReplaceAll(value, "\\", "\\\\") + "\"" }

type fixtureFile struct {
	Cases []struct {
		ID           string `yaml:"id"`
		ChunkText    string `yaml:"chunk_text"`
		ExpectReject bool   `yaml:"expect_reject"`
	} `yaml:"cases"`
}

// TestRedteamFixtures exercises the retrieval filter against red-team fixtures.
// The output-rail half lives in internal/llm/middleware (TestRedteamOutputFixtures).
func TestRedteamFixtures(t *testing.T) {
	raw, err := os.ReadFile("redteam/fixtures.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var f fixtureFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	r := StrictDefaults()
	for _, c := range f.Cases {
		if c.ChunkText == "" {
			continue
		}
		t.Run(c.ID, func(t *testing.T) {
			kept, rej := EvaluateRetrieval(r, []Chunk{{Text: c.ChunkText, Source: "t"}})
			got := len(kept) == 0 || len(rej) > 0
			if got != c.ExpectReject {
				t.Fatalf("retrieval reject=%v want=%v kept=%d rej=%d", got, c.ExpectReject, len(kept), len(rej))
			}
		})
	}
}

func TestIntersectRequestPermissionsResponse(t *testing.T) {
	root := filepath.Join(t.TempDir(), "external")
	requested := RequestPermissionsResponse{
		Scope: GrantScopeSession,
		Permissions: RequestPermissionProfile{FileSystem: &FileSystemPermissionProfile{Entries: []FileSystemPermissionEntry{
			{Path: FileSystemPermissionPath{Path: root}, Access: FileSystemAccessRead},
			{Path: FileSystemPermissionPath{Path: root}, Access: FileSystemAccessWrite},
		}}},
	}
	granted := RequestPermissionsResponse{
		Scope: GrantScopeTurn,
		Permissions: RequestPermissionProfile{FileSystem: &FileSystemPermissionProfile{Entries: []FileSystemPermissionEntry{
			{Path: FileSystemPermissionPath{Path: filepath.Join(root, "public")}, Access: FileSystemAccessRead},
		}}},
	}
	got, err := IntersectRequestPermissionsResponse(requested, granted)
	if err != nil {
		t.Fatalf("IntersectRequestPermissionsResponse: %v", err)
	}
	if got.Scope != GrantScopeTurn || len(got.Permissions.FileSystem.Entries) != 1 {
		t.Fatalf("got=%+v", got)
	}
}

func TestFileSystemPermissionPathTaggedJSON(t *testing.T) {
	var path FileSystemPermissionPath
	if err := json.Unmarshal([]byte(`{"type":"path","path":"/tmp/data"}`), &path); err != nil {
		t.Fatal(err)
	}
	if path.Type != FileSystemPermissionPathTypePath || path.Path != "/tmp/data" {
		t.Fatalf("path=%+v", path)
	}
	if err := json.Unmarshal([]byte(`{"path":"/tmp/data"}`), &path); err == nil {
		t.Fatal("missing path type was accepted")
	}
	encoded, err := json.Marshal(NewFileSystemPermissionPath("/tmp/data"))
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"type":"path","path":"/tmp/data"}` {
		t.Fatalf("encoded=%s", encoded)
	}
}

func TestFileSystemPermissionSpecialPathsAndGlob(t *testing.T) {
	cwd := t.TempDir()
	project := FileSystemPermissionPath{
		Type: FileSystemPermissionPathTypeSpecial,
		Value: &FileSystemSpecialPath{
			Kind: FileSystemSpecialPathProjectRoots, Subpath: "data",
		},
	}
	want := filepath.Join(cwd, "data")
	if got, ok := project.Resolve(cwd); !ok || got != want {
		t.Fatalf("resolved=%q ok=%t want=%q", got, ok, want)
	}
	secret := filepath.Join(cwd, "nested", "value.env")
	if err := os.MkdirAll(filepath.Dir(secret), 0o755); err != nil {
		t.Fatal(err)
	}
	pattern := FileSystemPermissionPath{
		Type:    FileSystemPermissionPathTypeGlobPattern,
		Pattern: filepath.Join(cwd, "**", "*.env"),
	}
	if !pattern.Matches(secret, cwd) {
		t.Fatalf("glob %q did not match %q", pattern.Pattern, secret)
	}
}

func TestNormalizeRequestPermissionsRejectsNonDenyGlob(t *testing.T) {
	_, err := NormalizeRequestPermissionsResponse(RequestPermissionsResponse{
		Permissions: RequestPermissionProfile{FileSystem: &FileSystemPermissionProfile{Entries: []FileSystemPermissionEntry{{
			Path:   FileSystemPermissionPath{Type: FileSystemPermissionPathTypeGlobPattern, Pattern: "**/*.env"},
			Access: FileSystemAccessRead,
		}}}},
	})
	if err == nil {
		t.Fatal("non-deny glob was accepted")
	}
}

func TestNormalizeRequestPermissionsResolvesRelativePathsAgainstCWD(t *testing.T) {
	cwd := t.TempDir()
	got, err := NormalizeRequestPermissionsResponseAtCWD(RequestPermissionsResponse{
		Permissions: RequestPermissionProfile{FileSystem: &FileSystemPermissionProfile{Entries: []FileSystemPermissionEntry{{
			Path: NewFileSystemPermissionPath(filepath.Join("data", "notes")), Access: FileSystemAccessWrite,
		}}}},
	}, cwd)
	if err != nil {
		t.Fatalf("NormalizeRequestPermissionsResponseAtCWD: %v", err)
	}
	if got.Permissions.FileSystem == nil || len(got.Permissions.FileSystem.Entries) != 1 {
		t.Fatalf("permissions=%+v", got.Permissions)
	}
	want := filepath.Join(cwd, "data", "notes")
	if path := got.Permissions.FileSystem.Entries[0].Path.Path; path != want {
		t.Fatalf("path=%q want=%q", path, want)
	}
}

func TestIntersectRequestPermissionsResponseDropsWidening(t *testing.T) {
	root := filepath.Join(t.TempDir(), "external")
	requested := RequestPermissionsResponse{
		Scope: GrantScopeTurn,
		Permissions: RequestPermissionProfile{FileSystem: &FileSystemPermissionProfile{Entries: []FileSystemPermissionEntry{{
			Path: FileSystemPermissionPath{Path: root}, Access: FileSystemAccessRead,
		}}}},
	}
	granted := RequestPermissionsResponse{Scope: GrantScopeTurn, Permissions: RequestPermissionProfile{
		Network: NewNetworkPermissionProfile(true),
		FileSystem: &FileSystemPermissionProfile{Entries: []FileSystemPermissionEntry{
			{Path: FileSystemPermissionPath{Path: filepath.Dir(root)}, Access: FileSystemAccessRead},
			{Path: FileSystemPermissionPath{Path: root}, Access: FileSystemAccessWrite},
			{Path: FileSystemPermissionPath{Path: filepath.Join(root, "child")}, Access: FileSystemAccessRead},
		}},
	}}
	got, err := IntersectRequestPermissionsResponse(requested, granted)
	if err != nil {
		t.Fatalf("IntersectRequestPermissionsResponse: %v", err)
	}
	if got.Permissions.Network != nil || got.Permissions.FileSystem == nil || len(got.Permissions.FileSystem.Entries) != 1 {
		t.Fatalf("got=%+v", got)
	}
	entry := got.Permissions.FileSystem.Entries[0]
	if entry.Access != FileSystemAccessRead || entry.Path.Path != filepath.Join(root, "child") {
		t.Fatalf("entry=%+v", entry)
	}
}

func TestIntersectRequestPermissionsResponseSupportsNetworkAndResponseScope(t *testing.T) {
	requested := RequestPermissionsResponse{
		Permissions: RequestPermissionProfile{Network: NewNetworkPermissionProfile(true)},
	}
	granted := RequestPermissionsResponse{
		Scope:       GrantScopeSession,
		Permissions: RequestPermissionProfile{Network: NewNetworkPermissionProfile(true)},
	}
	got, err := IntersectRequestPermissionsResponse(requested, granted)
	if err != nil {
		t.Fatalf("IntersectRequestPermissionsResponse: %v", err)
	}
	if got.Scope != GrantScopeSession || !got.Permissions.Network.AllowsNetwork() {
		t.Fatalf("got=%+v", got)
	}
}

func TestIntersectRequestPermissionsResponseWriteCoversReadAndRetainsDeny(t *testing.T) {
	root := filepath.Join(t.TempDir(), "external")
	blocked := filepath.Join(root, "private")
	requested := RequestPermissionsResponse{Permissions: RequestPermissionProfile{FileSystem: &FileSystemPermissionProfile{Entries: []FileSystemPermissionEntry{
		{Path: FileSystemPermissionPath{Path: root}, Access: FileSystemAccessWrite},
		{Path: FileSystemPermissionPath{Path: blocked}, Access: FileSystemAccessDeny},
	}}}}
	granted := RequestPermissionsResponse{Permissions: RequestPermissionProfile{FileSystem: &FileSystemPermissionProfile{Entries: []FileSystemPermissionEntry{
		{Path: FileSystemPermissionPath{Path: root}, Access: FileSystemAccessRead},
	}}}}
	got, err := IntersectRequestPermissionsResponse(requested, granted)
	if err != nil {
		t.Fatalf("IntersectRequestPermissionsResponse: %v", err)
	}
	if got.Permissions.FileSystem == nil || len(got.Permissions.FileSystem.Entries) != 2 {
		t.Fatalf("got=%+v", got)
	}
	if got.Permissions.FileSystem.Entries[0].Access != FileSystemAccessRead || got.Permissions.FileSystem.Entries[1].Access != FileSystemAccessDeny {
		t.Fatalf("entries=%+v", got.Permissions.FileSystem.Entries)
	}
}

func TestIntersectRequestPermissionsResponseUsesRequestCWD(t *testing.T) {
	cwd := t.TempDir()
	depth := 2
	requested := RequestPermissionsResponse{Permissions: RequestPermissionProfile{FileSystem: &FileSystemPermissionProfile{GlobScanMaxDepth: &depth, Entries: []FileSystemPermissionEntry{
		{
			Path: FileSystemPermissionPath{Type: FileSystemPermissionPathTypeSpecial, Value: &FileSystemSpecialPath{
				Kind: FileSystemSpecialPathProjectRoots, Subpath: "data",
			}},
			Access: FileSystemAccessRead,
		},
		{
			Path:   FileSystemPermissionPath{Type: FileSystemPermissionPathTypeGlobPattern, Pattern: "data/private/**"},
			Access: FileSystemAccessDeny,
		},
	}}}}
	grantedPath := filepath.Join(cwd, "data")
	granted := RequestPermissionsResponse{Scope: GrantScopeSession, Permissions: RequestPermissionProfile{FileSystem: &FileSystemPermissionProfile{Entries: []FileSystemPermissionEntry{{
		Path: NewFileSystemPermissionPath(grantedPath), Access: FileSystemAccessRead,
	}}}}}

	got, err := IntersectRequestPermissionsResponseAtCWD(requested, granted, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if got.Permissions.FileSystem == nil || len(got.Permissions.FileSystem.Entries) != 2 {
		t.Fatalf("got=%+v", got)
	}
	if got.Permissions.FileSystem.Entries[0].Path.Path != grantedPath {
		t.Fatalf("grant=%+v", got.Permissions.FileSystem.Entries[0])
	}
	deny := got.Permissions.FileSystem.Entries[1]
	if deny.Path.Pattern != filepath.Join(cwd, "data", "private", "**") {
		t.Fatalf("deny=%+v", deny)
	}
	if got.Permissions.FileSystem.GlobScanMaxDepth == nil || *got.Permissions.FileSystem.GlobScanMaxDepth != depth {
		t.Fatalf("glob_scan_max_depth=%v", got.Permissions.FileSystem.GlobScanMaxDepth)
	}
}

func TestPermissionProtocolAliases(t *testing.T) {
	var entry FileSystemPermissionEntry
	if err := json.Unmarshal([]byte(`{"path":{"type":"special","value":{"kind":"current_working_directory","subpath":"data"}},"access":"none"}`), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Access != FileSystemAccessDeny || entry.Path.Value == nil || entry.Path.Value.Kind != FileSystemSpecialPathProjectRoots {
		t.Fatalf("entry=%+v", entry)
	}
}

func TestPermissionPathSupportedOnPlatformRejectsSlashTmpOnWindows(t *testing.T) {
	path := FileSystemPermissionPath{
		Type:  FileSystemPermissionPathTypeSpecial,
		Value: &FileSystemSpecialPath{Kind: FileSystemSpecialPathSlashTmp},
	}
	if permissionPathSupportedOnPlatform(path, "windows") {
		t.Fatal("slash_tmp must not be granted on Windows")
	}
	if !permissionPathSupportedOnPlatform(path, "linux") {
		t.Fatal("slash_tmp must remain available on Unix")
	}
}

func TestNetworkPermissionDistinguishesMissingAndFalse(t *testing.T) {
	var missing, disabled NetworkPermissionProfile
	if err := json.Unmarshal([]byte(`{}`), &missing); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"enabled":false}`), &disabled); err != nil {
		t.Fatal(err)
	}
	if !missing.IsEmpty() || disabled.IsEmpty() || disabled.AllowsNetwork() {
		t.Fatalf("missing=%+v disabled=%+v", missing, disabled)
	}
}

func TestIntersectRequestPermissionsResponseRejectsGrantInsideRequestedDenyGlob(t *testing.T) {
	cwd := t.TempDir()
	requested := RequestPermissionsResponse{Permissions: RequestPermissionProfile{FileSystem: &FileSystemPermissionProfile{Entries: []FileSystemPermissionEntry{
		{Path: NewFileSystemPermissionPath(cwd), Access: FileSystemAccessRead},
		{Path: FileSystemPermissionPath{Type: FileSystemPermissionPathTypeGlobPattern, Pattern: "private/**"}, Access: FileSystemAccessDeny},
	}}}}
	granted := RequestPermissionsResponse{Permissions: RequestPermissionProfile{FileSystem: &FileSystemPermissionProfile{Entries: []FileSystemPermissionEntry{{
		Path: NewFileSystemPermissionPath(filepath.Join(cwd, "private", "secret.txt")), Access: FileSystemAccessRead,
	}}}}}

	got, err := IntersectRequestPermissionsResponseAtCWD(requested, granted, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if got.Permissions.FileSystem != nil {
		t.Fatalf("denied grant retained: %+v", got)
	}
}

func TestStrictDefaults(t *testing.T) {
	r := StrictDefaults()
	if r.InputMaxRunes != 200000 {
		t.Errorf("InputMaxRunes = %d; want 200000", r.InputMaxRunes)
	}
	if r.RetrievalMaxChunk != 120000 {
		t.Errorf("RetrievalMaxChunk = %d; want 120000", r.RetrievalMaxChunk)
	}
}

func TestResolveNilConfig(t *testing.T) {
	r := ResolveFromRoot(nil)
	if r == nil {
		t.Fatal("expected non-nil result for nil config")
	}
}

func TestResolveEmptyConfig(t *testing.T) {
	r := ResolveGuardrails(appcfg.GuardrailsConfig{})
	if r == nil {
		t.Fatal("expected non-nil result for empty config")
	}
}

func TestResolveCustomMaxRunes(t *testing.T) {
	r := ResolveGuardrails(appcfg.GuardrailsConfig{Input: appcfg.GuardrailsInputConfig{MaxRunes: 50000}})
	if r.InputMaxRunes != 50000 {
		t.Errorf("InputMaxRunes = %d; want 50000", r.InputMaxRunes)
	}
}

func TestResolveBlockSubstrings(t *testing.T) {
	substrs := []string{"secret", "password"}
	r := ResolveGuardrails(appcfg.GuardrailsConfig{Input: appcfg.GuardrailsInputConfig{BlockSubstrings: substrs}})
	if len(r.InputBlockSubs) != 2 {
		t.Fatalf("expected 2 block subs, got %d", len(r.InputBlockSubs))
	}
	if r.InputBlockSubs[0] != "secret" || r.InputBlockSubs[1] != "password" {
		t.Errorf("block subs = %v; want [secret password]", r.InputBlockSubs)
	}
}

func TestResolveInputMaxRunesZeroUsesDefault(t *testing.T) {
	r := ResolveGuardrails(appcfg.GuardrailsConfig{Input: appcfg.GuardrailsInputConfig{MaxRunes: 0}})
	if r.InputMaxRunes != 200000 {
		t.Errorf("InputMaxRunes = %d; want 200000 (default)", r.InputMaxRunes)
	}
}

func TestRewriteAppliesPresentationFlags(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"git gets no-pager after head", "git log", "git --no-pager log"},
		{"git with global opts", "git -C /repo status", "git --no-pager -C /repo status"},
		{"terraform no-color at end", "terraform plan", "terraform plan -no-color"},
		{"cargo color never", "cargo build", "cargo build --color=never"},
		{"env prefix preserved", "FOO=1 git log", "FOO=1 git --no-pager log"},
		{"sudo preserved", "sudo git log", "sudo git --no-pager log"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Rewrite(tc.in)
			if !got.Applied {
				t.Fatalf("Rewrite(%q) not applied (skip=%q)", tc.in, got.SkipReason)
			}
			if got.Command != tc.want {
				t.Errorf("Rewrite(%q) = %q, want %q", tc.in, got.Command, tc.want)
			}
		})
	}
}

// A bare "--" ends the head command's options. Anything after it is forwarded
// to the inner program, so an end-appended flag must land before the "--" or it
// would become an argument of that program and change what the command does.
func TestRewriteKeepsFlagsBeforeEndOfOptions(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"cargo forwards to test binary", "cargo test -- --nocapture", "cargo test --color=never -- --nocapture"},
		{"trailing separator only", "cargo test --", "cargo test --color=never --"},
		{"operands after end of options", "cargo run -- input.txt", "cargo run --color=never -- input.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Rewrite(tc.in)
			if !got.Applied {
				t.Fatalf("Rewrite(%q) not applied (skip=%q)", tc.in, got.SkipReason)
			}
			if got.Command != tc.want {
				t.Errorf("Rewrite(%q) = %q, want %q", tc.in, got.Command, tc.want)
			}
			head, _, _ := strings.Cut(got.Command, " -- ")
			if !strings.Contains(head, "--color=never") {
				t.Errorf("flag landed at or past %q: %q", "--", got.Command)
			}
		})
	}
}

// "--" only ends option parsing as a bare word; lookalikes are ordinary tokens.
func TestRewriteEndOfOptionsRequiresBareToken(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"option is not end of options", "cargo build --release", "cargo build --release --color=never"},
		{"triple dash is an operand", "cargo run ---", "cargo run --- --color=never"},
		{"quoted dashes are an operand", `cargo run "--"`, `cargo run "--" --color=never`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Rewrite(tc.in)
			if got.Command != tc.want {
				t.Errorf("Rewrite(%q) = %q, want %q", tc.in, got.Command, tc.want)
			}
		})
	}
}

// verifySafe must reject an insertion placed past "--" on position alone, even
// though the token itself is allowlisted. This is the backstop that keeps a
// future rule from reintroducing the bug the guard above fixes.
func TestVerifySafeRejectsAllowlistedFlagPastEndOfOptions(t *testing.T) {
	const original = "cargo test -- --nocapture"
	past := "cargo test -- --nocapture --color=never"
	if verifySafe(original, past, []string{"--color=never"}) {
		t.Errorf("verifySafe accepted an insertion past --: %q", past)
	}
	before := "cargo test --color=never -- --nocapture"
	if !verifySafe(original, before, []string{"--color=never"}) {
		t.Errorf("verifySafe rejected a valid insertion before --: %q", before)
	}
}

func TestRewriteRespectsExistingUserIntent(t *testing.T) {
	// When the user already expressed an intent on the same dimension, the
	// rewriter must not second-guess it.
	cases := []string{
		"git --no-pager log",
		"git --paginate log",
		"git -p log",
		"cargo build --color=always",
		"cargo build --color never",
		"terraform plan -no-color",
	}
	for _, in := range cases {
		if got := Rewrite(in); got.Applied {
			t.Errorf("Rewrite(%q) = %q, expected no rewrite", in, got.Command)
		}
	}
}

func TestRewriteDeclinesUnsafeShapes(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		reason string
	}{
		{"redirection", "git log > out.txt", "top_level_redirection"},
		{"stderr redirection", "git log 2>&1", "top_level_redirection"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Rewrite(tc.in)
			if got.Applied {
				t.Fatalf("Rewrite(%q) = %q, expected decline", tc.in, got.Command)
			}
			if got.Command != tc.in {
				t.Errorf("declined rewrite changed command: %q", got.Command)
			}
			if tc.reason != "" && got.SkipReason != tc.reason {
				t.Errorf("skip reason = %q, want %q", got.SkipReason, tc.reason)
			}
		})
	}
}

func TestRewriteLeavesOpaqueProgramsAlone(t *testing.T) {
	// Interpreter invocations and subshells hide their real program from the
	// classifier, so nothing may be spliced into them.
	cases := []string{
		`bash -c "git log"`,
		`sh -c 'cargo build'`,
		"(git log)",
		"npx prettier --write .",
		"pnpm run prettier --check .",
		"FOO=1",
	}
	for _, in := range cases {
		if got := Rewrite(in); got.Applied {
			t.Errorf("Rewrite(%q) = %q, expected no rewrite", in, got.Command)
		}
	}
}

func TestRewriteMultiSegment(t *testing.T) {
	got := Rewrite("git log && cargo build")
	if !got.Applied {
		t.Fatalf("expected rewrite, skip=%q", got.SkipReason)
	}
	want := "git --no-pager log && cargo build --color=never"
	if got.Command != want {
		t.Errorf("got %q, want %q", got.Command, want)
	}
	if len(got.Inserted) != 2 {
		t.Errorf("inserted = %v, want 2 entries", got.Inserted)
	}
}

func TestRewritePreservesUnrelatedSegments(t *testing.T) {
	got := Rewrite("echo hi && git log && ls -la")
	if !got.Applied {
		t.Fatalf("expected rewrite, skip=%q", got.SkipReason)
	}
	want := "echo hi && git --no-pager log && ls -la"
	if got.Command != want {
		t.Errorf("got %q, want %q", got.Command, want)
	}
}

func TestRewriteNoRuleIsIdentity(t *testing.T) {
	for _, in := range []string{"ls -la", "echo hello", "cat file.txt", "grep -r x ."} {
		got := Rewrite(in)
		if got.Applied || got.Command != in {
			t.Errorf("Rewrite(%q) = %q applied=%v, want identity", in, got.Command, got.Applied)
		}
	}
}

func TestRewriteDisabledByEnv(t *testing.T) {
	t.Setenv(RewriteDisableEnv, "0")
	got := Rewrite("git log")
	if got.Applied {
		t.Errorf("expected no rewrite when disabled, got %q", got.Command)
	}
	if got.SkipReason != "disabled_by_env" {
		t.Errorf("skip reason = %q", got.SkipReason)
	}
}

func TestRewritePreservesQuotedArguments(t *testing.T) {
	in := `git commit -m "a && b"`
	got := Rewrite(in)
	if !got.Applied {
		t.Fatalf("expected rewrite, skip=%q", got.SkipReason)
	}
	if !strings.Contains(got.Command, `-m "a && b"`) {
		t.Errorf("quoted argument damaged: %q", got.Command)
	}
	if got.Command != `git --no-pager commit -m "a && b"` {
		t.Errorf("got %q", got.Command)
	}
}

func TestRewriteNoteMentionsInsertedFlags(t *testing.T) {
	got := Rewrite("git log")
	note := got.Note()
	if note == "" {
		t.Fatal("expected a note for an applied rewrite")
	}
	if !strings.Contains(note, "--no-pager") {
		t.Errorf("note = %q, expected it to name the inserted flag", note)
	}
	if Rewrite("ls -la").Note() != "" {
		t.Error("expected empty note when nothing was rewritten")
	}
}

// The rules table is the security boundary: every flag it can insert must be
// on the presentation-only allowlist. This guards against a future rule adding
// a behavior-changing flag.
func TestEveryRuleFlagIsAllowlisted(t *testing.T) {
	for _, r := range rules {
		if _, ok := allowedInsert(r.Flag); !ok {
			t.Errorf("rule %s/%s inserts non-allowlisted flag %q", r.Head, r.Sub, r.Flag)
		}
	}
}

// No allowlisted flag may look like a behavior-changing option. This is a
// tripwire, not a proof, but it catches the obvious mistakes.
func TestAllowlistHasNoDangerousFlags(t *testing.T) {
	banned := []string{"force", "yes", "delete", "remove", "prune", "reset", "hard", "recursive", "overwrite", "insecure", "privileged"}
	check := func(flag string) {
		low := strings.ToLower(flag)
		for _, b := range banned {
			if strings.Contains(low, b) {
				t.Errorf("allowlisted flag %q contains banned substring %q", flag, b)
			}
		}
	}
	for f := range presentationFlags {
		check(f)
	}
	for f := range extraAllowed {
		check(f)
	}
}

func TestVerifySafeRejectsTampering(t *testing.T) {
	cases := []struct {
		name      string
		original  string
		rewritten string
		inserted  []string
	}{
		{"changed head", "git log", "gitx --no-pager log", []string{"--no-pager"}},
		{"dropped operand", "git log -5", "git --no-pager log", []string{"--no-pager"}},
		{"added operand", "git log", "git --no-pager log --all", []string{"--no-pager"}},
		{"non-allowlisted insert", "git clean", "git clean --force", []string{"--force"}},
		{"unreported insert", "git log", "git --no-pager --quiet log", []string{"--no-pager"}},
		{"new segment", "git log", "git --no-pager log && rm -rf /", []string{"--no-pager"}},
		{"new pipe", "git log", "git --no-pager log | sh", []string{"--no-pager"}},
		{"new redirection", "git log", "git --no-pager log > /etc/passwd", []string{"--no-pager"}},
		{"separator changed", "a && b", "a --no-pager || b", []string{"--no-pager"}},
		{"identical", "git log", "git log", []string{"--no-pager"}},
		{"promised but absent", "git log", "git log --quiet", []string{"--no-pager", "--quiet"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if verifySafe(tc.original, tc.rewritten, tc.inserted) {
				t.Errorf("verifySafe accepted a tampered rewrite: %q -> %q", tc.original, tc.rewritten)
			}
		})
	}
}

func TestVerifySafeAcceptsLegitimateRewrites(t *testing.T) {
	cases := []struct {
		original  string
		rewritten string
		inserted  []string
	}{
		{"git log", "git --no-pager log", []string{"--no-pager"}},
		{"terraform plan", "terraform plan -no-color", []string{"-no-color"}},
		{"git log && cargo build", "git --no-pager log && cargo build --color=never", []string{"--no-pager", "--color=never"}},
	}
	for _, tc := range cases {
		if !verifySafe(tc.original, tc.rewritten, tc.inserted) {
			t.Errorf("verifySafe rejected a legitimate rewrite: %q -> %q", tc.original, tc.rewritten)
		}
	}
}

// Every rewrite Rewrite itself produces must pass verification. This is the
// end-to-end statement of the safety property.
func TestAllProducedRewritesVerify(t *testing.T) {
	inputs := []string{
		"git log", "git status", "git -C /repo diff", "FOO=1 git log",
		"sudo git log", "cargo build", "cargo test", "terraform plan",
		"tofu apply", "npm install", "yarn install", "composer install",
		"curl https://example.com", "mvn package",
		"git log && cargo build", "echo hi && git log && ls",
		`git commit -m "a && b"`, "git log | head -5",
	}
	for _, in := range inputs {
		got := Rewrite(in)
		if !got.Applied {
			continue
		}
		if !verifySafe(in, got.Command, got.Inserted) {
			t.Errorf("produced rewrite failed verification: %q -> %q", in, got.Command)
		}
		// Classification must be stable across the rewrite: same command.
		if a, b := ClassifyCommand(in).Canonical, ClassifyCommand(got.Command).Canonical; a != b {
			t.Errorf("rewrite changed canonical form: %q -> %q (%q vs %q)", in, got.Command, a, b)
		}
	}
}

func FuzzRewriteNeverEscalates(f *testing.F) {
	seeds := []string{
		"git log", "git status && cargo build", `echo "a && b"`, "ls -la",
		"terraform plan", "bash -c 'x'", "(git log)", "FOO=1 git log",
		"git log > out", "curl https://x", "npm install", "a|b|c",
		"", "   ", "&&", "$(git log)", "`git log`", "git log 2>&1",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, cmd string) {
		got := Rewrite(cmd)
		if !got.Applied {
			if got.Command != cmd {
				t.Fatalf("declined rewrite mutated command: %q -> %q", cmd, got.Command)
			}
			return
		}
		// An applied rewrite must be verifiable, must not introduce new
		// top-level structure, and must not change the classified command.
		if !verifySafe(cmd, got.Command, got.Inserted) {
			t.Fatalf("applied rewrite failed verification: %q -> %q", cmd, got.Command)
		}
		if HasTopLevelRedirection(got.Command) {
			t.Fatalf("rewrite introduced redirection: %q -> %q", cmd, got.Command)
		}
		if a, b := len(SplitTopLevelWithSeparators(cmd)), len(SplitTopLevelWithSeparators(got.Command)); a != b {
			t.Fatalf("rewrite changed segment count %d -> %d: %q -> %q", a, b, cmd, got.Command)
		}
		if a, b := ClassifyCommand(cmd).Canonical, ClassifyCommand(got.Command).Canonical; a != b {
			t.Fatalf("rewrite changed canonical form %q -> %q: %q -> %q", a, b, cmd, got.Command)
		}
	})
}

func TestRuleParserRoundTrip(t *testing.T) {
	cases := []string{
		"Bash",
		"Bash(npm run build)",
		`Bash(git commit -m "fix\(\)")`,
		`Bash(npm run:\*)`,
	}
	for _, c := range cases {
		v, err := ParseRuleString(c)
		if err != nil {
			t.Fatalf("parse %q: %v", c, err)
		}
		out := FormatRuleString(v)
		v2, err := ParseRuleString(out)
		if err != nil {
			t.Fatalf("reparse %q: %v", out, err)
		}
		if v.ToolName != v2.ToolName || v.RuleContent != v2.RuleContent {
			t.Fatalf("roundtrip mismatch for %q: %#v != %#v", c, v, v2)
		}
	}
}

func TestRuleParserRejectInvalid(t *testing.T) {
	if _, err := ParseRuleString("Bash(npm run"); err == nil {
		t.Fatalf("expected invalid rule error")
	}
}

func snapshotWithRules(src PermissionSource, behavior PermissionBehavior, rules ...PermissionRuleValue) Snapshot {
	return Snapshot{
		Rules: map[PermissionSource]map[PermissionBehavior][]PermissionRuleValue{
			src: {behavior: rules},
		},
	}
}

func TestSnapshotGrantsShellSandboxBypassHonoursRememberedPrefix(t *testing.T) {
	snap := snapshotWithRules(SourceLocalSettings, BehaviorAllow, PermissionRuleValue{
		ToolName: "Bash", CommandPrefix: []string{"go", "test"}, BypassSandbox: true,
	})
	for _, cmd := range []string{
		"go test ./internal/agentrun -run 'Compact|Recoverable'",
		"go test ./internal/agentrun ./internal/tui ./internal/clifacade",
		"go test ./...",
	} {
		if !SnapshotGrantsShellSandboxBypass(snap, cmd) {
			t.Fatalf("remembered `go test` approval did not grant a sandbox bypass for %q", cmd)
		}
	}
	if SnapshotGrantsShellSandboxBypass(snap, "go build ./...") {
		t.Fatal("bypass leaked to a command the prefix does not cover")
	}
}

func TestSnapshotGrantsShellSandboxBypassRequiresBypassCapability(t *testing.T) {
	snap := snapshotWithRules(SourceLocalSettings, BehaviorAllow, PermissionRuleValue{
		ToolName: "Bash", CommandPrefix: []string{"go", "test"},
	})
	if SnapshotGrantsShellSandboxBypass(snap, "go test ./...") {
		t.Fatal("an allow rule without bypass_sandbox must not lift the sandbox")
	}
}

func TestSnapshotGrantsShellSandboxBypassRefusesNonWideningSource(t *testing.T) {
	snap := snapshotWithRules(SourceProjectSettings, BehaviorAllow, PermissionRuleValue{
		ToolName: "Bash", CommandPrefix: []string{"go", "test"}, BypassSandbox: true,
	})
	if SnapshotGrantsShellSandboxBypass(snap, "go test ./...") {
		t.Fatal("a repository-committed rule must not widen the sandbox")
	}
}

func TestSnapshotGrantsShellSandboxBypassRestrictionsWin(t *testing.T) {
	for _, behavior := range []PermissionBehavior{BehaviorDeny, BehaviorAsk} {
		snap := Snapshot{
			Rules: map[PermissionSource]map[PermissionBehavior][]PermissionRuleValue{
				SourceLocalSettings: {
					BehaviorAllow: {{ToolName: "Bash", CommandPrefix: []string{"go", "test"}, BypassSandbox: true}},
				},
				SourceProjectSettings: {
					behavior: {{ToolName: "Bash", RuleContent: "go test:*"}},
				},
			},
		}
		if SnapshotGrantsShellSandboxBypass(snap, "go test ./...") {
			t.Fatalf("a %s rule must refuse the bypass", behavior)
		}
	}
}

// The command the user remembered rarely stands alone: `git add -A && git
// status --short` is one shell call, and the snapshot must answer it the way
// the engine does — clause by clause, with a known-safe clause carrying itself.
func TestSnapshotGrantsShellSandboxBypassCoversCompoundCommands(t *testing.T) {
	snap := snapshotWithRules(SourceLocalSettings, BehaviorAllow, PermissionRuleValue{
		ToolName: "Bash", CommandPrefix: []string{"git", "--no-pager", "add"}, BypassSandbox: true,
	})
	if !SnapshotGrantsShellSandboxBypass(snap, "git --no-pager add -A && git --no-pager status --short") {
		t.Fatal("a known-safe trailing clause dropped the remembered grant")
	}
	if SnapshotGrantsShellSandboxBypass(snap, "git --no-pager add -A && rm -rf build") {
		t.Fatal("bypass leaked to a clause no rule covers")
	}
	if SnapshotGrantsShellSandboxBypass(snap, "git --no-pager status --short && git --no-pager log -1") {
		t.Fatal("known-safe clauses alone must not grant a bypass")
	}
}

// A restriction on any clause refuses the whole command, not just that clause.
func TestSnapshotGrantsShellSandboxBypassRestrictedClauseRefusesCompound(t *testing.T) {
	snap := Snapshot{Rules: map[PermissionSource]map[PermissionBehavior][]PermissionRuleValue{
		SourceLocalSettings: {
			BehaviorAllow: {{ToolName: "Bash", CommandPrefix: []string{"git", "--no-pager", "add"}, BypassSandbox: true}},
			BehaviorDeny:  {{ToolName: "Bash", RuleContent: "git --no-pager push:*"}},
		},
	}}
	if SnapshotGrantsShellSandboxBypass(snap, "git --no-pager add -A && git --no-pager push") {
		t.Fatal("a denied clause must refuse the bypass for the whole command")
	}
}

// TestSpecialPathKindEnumMatchesResolvableKinds ties the `kind` enum advertised
// to callers to the kinds Resolve actually handles.
//
// A kind that validates but falls through Resolve's default (minimal, unknown)
// produces a permission entry that never matches any path — a silent no-op
// grant. Advertising one would invite exactly that. Conversely, a newly
// resolvable kind that never reaches the enum is a capability callers cannot
// discover. Both directions are checked here.
func TestSpecialPathKindEnumMatchesResolvableKinds(t *testing.T) {
	advertised := map[FileSystemSpecialPathKind]bool{}
	for _, kind := range advertisedSpecialPathKinds(t) {
		advertised[kind] = true
	}
	all := []FileSystemSpecialPathKind{
		FileSystemSpecialPathRoot,
		FileSystemSpecialPathMinimal,
		FileSystemSpecialPathProjectRoots,
		FileSystemSpecialPathTmpdir,
		FileSystemSpecialPathSlashTmp,
		FileSystemSpecialPathUnknown,
	}
	for _, kind := range all {
		t.Run(string(kind), func(t *testing.T) {
			if kind == FileSystemSpecialPathSlashTmp && runtime.GOOS == "windows" {
				t.Skip("/tmp is unavailable on Windows")
			}
			// Resolve reads TMPDIR from the environment, which is not set on
			// every platform; pin it so the assertion is about the kind.
			t.Setenv("TMPDIR", t.TempDir())
			value := &FileSystemSpecialPath{Kind: kind}
			if kind == FileSystemSpecialPathUnknown {
				value.Path = "/etc"
			}
			path := FileSystemPermissionPath{Type: FileSystemPermissionPathTypeSpecial, Value: value}
			_, resolved := path.Resolve(t.TempDir())
			if resolved != advertised[kind] {
				if resolved {
					t.Fatalf("kind %q resolves but is not in the advertised enum: callers cannot reach it", kind)
				}
				t.Fatalf("kind %q is advertised but never resolves: it would grant nothing", kind)
			}
		})
	}
}

// advertisedSpecialPathKinds reads the enum out of the struct tag so the test
// tracks the schema callers are actually shown.
func advertisedSpecialPathKinds(t *testing.T) []FileSystemSpecialPathKind {
	t.Helper()
	field, ok := reflect.TypeOf(FileSystemSpecialPath{}).FieldByName("Kind")
	if !ok {
		t.Fatal("FileSystemSpecialPath has no Kind field")
	}
	tag, ok := field.Tag.Lookup("jsonschema")
	if !ok {
		t.Fatal("Kind carries no jsonschema tag: the enum callers see is gone")
	}
	var kinds []FileSystemSpecialPathKind
	for _, part := range strings.Split(tag, ",") {
		if value, found := strings.CutPrefix(strings.TrimSpace(part), "enum="); found {
			kinds = append(kinds, FileSystemSpecialPathKind(value))
		}
	}
	if len(kinds) == 0 {
		t.Fatalf("no enum values in Kind tag %q", tag)
	}
	return kinds
}

func TestBuildToolApprovalSuggestionFromPayload(t *testing.T) {
	got := BuildToolApprovalSuggestion("shell", map[string]any{
		"command": "NODE_ENV=production npm run build",
	})
	if got.PermissionToolName != "Bash" {
		t.Fatalf("unexpected permission tool name: %+v", got)
	}
	if got.PermissionInput != "NODE_ENV=production npm run build" {
		t.Fatalf("unexpected permission input: %+v", got)
	}
	if got.ExactRuleContent != "NODE_ENV=production npm run build" {
		t.Fatalf("unexpected exact rule: %+v", got)
	}
	if got.PrefixRuleContent != "NODE_ENV=production npm run build:*" {
		t.Fatalf("unexpected prefix rule: %+v", got)
	}
	if !got.BypassSandbox {
		t.Fatalf("remembered command approval must carry sandbox bypass: %+v", got)
	}
}

func TestBuildToolApprovalSuggestionCarriesSandboxBypass(t *testing.T) {
	got := BuildToolApprovalSuggestion("shell", map[string]any{
		"command":             "go test ./...",
		"sandbox_permissions": "require_escalated",
	})
	if !got.BypassSandbox {
		t.Fatalf("expected sandbox-bypass approval suggestion: %+v", got)
	}
	if got.PrefixRuleContent != "go test:*" {
		t.Fatalf("unexpected prefix rule: %+v", got)
	}
}

func TestBuildToolApprovalSuggestionUsesValidRequestedPrefix(t *testing.T) {
	got := BuildToolApprovalSuggestion("shell", map[string]any{
		"command":             "cargo install cargo-insta",
		"sandbox_permissions": "require_escalated",
		"prefix_rule":         []any{"cargo", "install"},
	})
	if got.PrefixRuleContent != "cargo install:*" {
		t.Fatalf("prefix rule = %q", got.PrefixRuleContent)
	}

	got = BuildToolApprovalSuggestion("shell", map[string]any{
		"command":     "cargo install cargo-insta && rm -rf /tmp/example",
		"prefix_rule": []any{"cargo", "install"},
	})
	if got.PrefixRuleContent != "" {
		t.Fatalf("unsafe requested prefix must be ignored, got %q", got.PrefixRuleContent)
	}
}

func TestBuildToolApprovalSuggestionRewritesRequestedPrefixToMatchRewrittenCommand(t *testing.T) {
	// Command rewriting inserts `git --no-pager`, so the model's pre-rewrite
	// prefix no longer prefixes the command that runs. The suggestion must
	// rewrite the prefix the same way so the persisted rule keeps matching.
	got := BuildToolApprovalSuggestion("shell", map[string]any{
		"command":             "git --no-pager -C sub add -A",
		"sandbox_permissions": "require_escalated",
		"prefix_rule":         []any{"git", "-C", "sub", "add"},
	})
	if got.PrefixRuleContent != "git --no-pager -C sub add:*" {
		t.Fatalf("prefix rule = %q; want the rewritten prefix", got.PrefixRuleContent)
	}
	amendment := proposedExecPolicyAmendment(got)
	if !slices.Equal(amendment, ExecPolicyAmendment{"git", "--no-pager", "-C", "sub", "add"}) {
		t.Fatalf("amendment = %#v", amendment)
	}
	// The persisted rule must keep matching a later invocation, which is
	// itself rewritten with `git --no-pager` before policy evaluation.
	if !CommandPrefixMatches(amendment, "git --no-pager -C sub add docs/new.md") {
		t.Fatalf("stored prefix %v must match a future rewritten command", amendment)
	}

	// A banned bare prefix must not be laundered into an allowed one by
	// inserting a presentation flag: it falls back to the full command.
	banned := BuildToolApprovalSuggestion("shell", map[string]any{
		"command":     "git --no-pager status",
		"prefix_rule": []any{"git"},
	})
	if banned.PrefixRuleContent != "git --no-pager status:*" {
		t.Fatalf("banned bare prefix must fall back to the full command, got %q", banned.PrefixRuleContent)
	}
}

func TestBuildToolApprovalSuggestionPreservesQuotedRequestedPrefixTokens(t *testing.T) {
	got := BuildToolApprovalSuggestion("shell", map[string]any{
		"command":     `printf '%s' 'a b' ''`,
		"prefix_rule": []any{"printf", "%s", "a b", ""},
	})
	if got.PrefixRuleContent != `printf %s 'a b' '':*` {
		t.Fatalf("prefix rule = %q", got.PrefixRuleContent)
	}
	if amendment := proposedExecPolicyAmendment(got); !slices.Equal(amendment, ExecPolicyAmendment{"printf", "%s", "a b", ""}) {
		t.Fatalf("amendment = %#v", amendment)
	}
}

func TestBuildToolApprovalSuggestionForGofmtPrefix(t *testing.T) {
	command := "gofmt -w internal/foo.go internal/bar.go"
	got := BuildToolApprovalSuggestion("shell", map[string]any{
		"command": command,
	})
	if got.PermissionToolName != "Bash" {
		t.Fatalf("unexpected permission tool name: %+v", got)
	}
	if got.PermissionInput != command || got.ExactRuleContent != command {
		t.Fatalf("unexpected exact command suggestion: %+v", got)
	}
	if got.PrefixRuleContent != "gofmt -w internal/foo.go internal/bar.go:*" {
		t.Fatalf("unexpected prefix rule: %+v", got)
	}
}

func TestBuildToolApprovalSuggestionOmitsHeredocBodyPrefix(t *testing.T) {
	command := "cat <<'EOF' > /tmp/main.go\npackage main\n\nfunc main() {}\nEOF\ngo run /tmp/main.go"
	got := BuildToolApprovalSuggestion("shell", map[string]any{
		"command": command,
	})
	if got.ExactRuleContent != command {
		t.Fatalf("unexpected exact rule: %+v", got)
	}
	if got.PrefixRuleContent != "" {
		t.Fatalf("heredoc body must not produce a prefix rule, got %+v", got)
	}
}

func TestBuildToolApprovalSuggestionUsesFullCommandForPrivilegedCommand(t *testing.T) {
	got := BuildToolApprovalSuggestion("shell", map[string]any{
		"command": "sudo ls",
	})
	if got.PermissionToolName != "Bash" {
		t.Fatalf("unexpected permission tool name: %+v", got)
	}
	if got.PrefixRuleContent != "sudo ls:*" {
		t.Fatalf("expected full command amendment, got %+v", got)
	}
}

func TestBuildToolApprovalSuggestionRejectsBannedRequestedPrefix(t *testing.T) {
	got := BuildToolApprovalSuggestion("shell", map[string]any{
		"command":     "npm run build",
		"prefix_rule": []any{"npm", "run"},
	})
	if got.PrefixRuleContent != "npm run build:*" {
		t.Fatalf("broad requested prefix must fall back to the full command, got %+v", got)
	}
}

func TestBuildToolApprovalSuggestionRejectsBannedRequestedPrefixAfterEnvironment(t *testing.T) {
	got := BuildToolApprovalSuggestion("shell", map[string]any{
		"command":     "PATH=/custom/bin npm run build",
		"prefix_rule": []any{"PATH=/custom/bin", "npm", "run"},
	})
	if got.PrefixRuleContent != "PATH=/custom/bin npm run build:*" {
		t.Fatalf("broad requested prefix must fall back to the full command, got %+v", got)
	}
}

func TestBuildToolApprovalSuggestionForMCPTool(t *testing.T) {
	name := "mcp__code_review_graph__get_minimal_context_tool"
	got := BuildToolApprovalSuggestion(name, map[string]any{"repo_root": "/tmp/repo"})
	if got.PermissionToolName != name {
		t.Fatalf("unexpected permission tool name: %+v", got)
	}
	if got.ExactRuleContent != "*" {
		t.Fatalf("unexpected exact rule: %+v", got)
	}
	if got.PrefixRuleContent != "" {
		t.Fatalf("MCP approval must not suggest a whole-server wildcard: %+v", got)
	}
	if got.PermissionInput == "" {
		t.Fatalf("expected permission input payload")
	}
	prompt := BuildToolApprovalSuggestion(name, map[string]any{"mcp_approval_mode": "prompt"})
	if !prompt.OneShotOnly {
		t.Fatalf("prompt approval must be one-shot: %+v", prompt)
	}
}

func TestBuildToolApprovalSuggestionForRequestPermissions(t *testing.T) {
	got := BuildToolApprovalSuggestion("request_permissions", map[string]any{"request_permissions": true})
	if got.PermissionToolName != "request_permissions" {
		t.Fatalf("unexpected permission tool name: %+v", got)
	}
	if got.PermissionInput != "request_permissions" {
		t.Fatalf("unexpected permission input: %+v", got)
	}
	if got.ExactRuleContent != "" || got.PrefixRuleContent != "" {
		t.Fatalf("request_permissions must not suggest a persistent rule: %+v", got)
	}
}

func TestBuildToolApprovalSuggestionForApplyPatch(t *testing.T) {
	got := BuildToolApprovalSuggestion("apply_patch", map[string]any{
		"resolved_paths": []any{"/repo/a.go", "/repo/b.go", "/repo/a.go"},
	})
	if got.PermissionToolName != "apply_patch" || got.PermissionInput != "/repo/a.go\n/repo/b.go" {
		t.Fatalf("unexpected suggestion: %+v", got)
	}
	if got.ExactRuleContent != "" || got.PrefixRuleContent != "" || got.OneShotOnly {
		t.Fatalf("patch approval must use its affected-path set: %+v", got)
	}
}

func TestBuildToolApprovalSuggestionForFileTools(t *testing.T) {
	cases := []struct {
		name      string
		tool      string
		canonical string
		path      string
		dir       string
		extra     map[string]any
	}{
		{"read", "read_file", "Read", "/repo/src/app.go", "/repo/src", nil},
		{"write", "write_file", "Write", "/repo/docs/x.md", "/repo/docs", map[string]any{"content": "hello"}},
		{"edit", "edit_file", "Edit", "/repo/src/main.go", "/repo/src", map[string]any{"old_string": "a", "new_string": "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{"file_path": tc.path}
			for k, v := range tc.extra {
				payload[k] = v
			}
			got := BuildToolApprovalSuggestion(tc.tool, payload)
			if got.PermissionToolName != tc.canonical {
				t.Fatalf("tool name: got %q want %q", got.PermissionToolName, tc.canonical)
			}
			if got.ExactRuleContent != tc.path {
				t.Fatalf("exact rule: got %q want %q", got.ExactRuleContent, tc.path)
			}
			if got.PrefixRuleContent != tc.dir+"/*" {
				t.Fatalf("prefix rule: got %q want %q/*", got.PrefixRuleContent, tc.dir)
			}
		})
	}
}

func TestBuildToolApprovalSuggestionForWebFetch(t *testing.T) {
	got := BuildToolApprovalSuggestion("web_fetch", map[string]any{"url": "https://example.com/api"})
	if got.PermissionToolName != "WebFetch" {
		t.Fatalf("unexpected tool name: %+v", got)
	}
	if got.ExactRuleContent != "https://example.com/api" {
		t.Fatalf("unexpected exact rule: %+v", got)
	}
	if got.PrefixRuleContent != "domain:example.com" {
		t.Fatalf("expected domain prefix rule, got %+v", got)
	}
}

func TestBuildToolApprovalSuggestionForWebSearchUsesReadableQuery(t *testing.T) {
	got := BuildToolApprovalSuggestion("web_search", `{"query":"golang structured logging","limit":5}`)
	if got.PermissionToolName != "WebSearch" {
		t.Fatalf("unexpected tool name: %+v", got)
	}
	if got.PermissionInput != "golang structured logging" {
		t.Fatalf("permission input exposed transport JSON: %+v", got)
	}
	if got.ExactRuleContent != "golang structured logging" || got.PrefixRuleContent != "" {
		t.Fatalf("unexpected web search rule suggestion: %+v", got)
	}
}

func TestBuildToolApprovalSuggestionForGenericToolUsesExactRuleOnly(t *testing.T) {
	got := BuildToolApprovalSuggestion("custom_tool", "{}")
	if got.PermissionToolName != "custom_tool" {
		t.Fatalf("unexpected permission tool name: %+v", got)
	}
	if got.PermissionInput != "custom_tool" {
		t.Fatalf("unexpected permission input: %+v", got)
	}
	if got.ExactRuleContent != "custom_tool" {
		t.Fatalf("unexpected exact rule: %+v", got)
	}
	if got.PrefixRuleContent != "" {
		t.Fatalf("generic tool should not suggest wildcard prefix: %+v", got)
	}
}

func TestBuildApprovalDecisionOptionsUsesOnlyProposedCapabilities(t *testing.T) {
	command := BuildApprovalDecisionOptions("shell", "shell", map[string]any{"command": "go test ./..."})
	if len(command) != 3 || command[1].Decision != DecisionAcceptWithExecPolicyAmendment ||
		!slices.Equal(command[1].ExecPolicyAmendment, []string{"go", "test"}) {
		t.Fatalf("command decisions = %#v", command)
	}

	// "npm run" is a banned prefix, so the derived prefix keeps every word of
	// the command — which as a prefix rule would authorize "npm run build
	// --anything". The command itself is the proposal instead.
	whole := BuildApprovalDecisionOptions("shell", "shell", map[string]any{"command": "npm run build"})
	if len(whole) != 3 || whole[1].Decision != DecisionAcceptAndRemember ||
		len(whole[1].CommandRules) != 1 || whole[1].CommandRules[0].Command != "npm run build" ||
		len(whole[1].CommandRules[0].CommandPrefix) != 0 {
		t.Fatalf("whole-command decisions = %#v", whole)
	}

	patch := BuildApprovalDecisionOptions("apply_patch", "apply_patch", map[string]any{"resolved_paths": []string{"/tmp/a"}})
	if len(patch) != 3 || patch[1].Decision != DecisionAcceptForSession {
		t.Fatalf("patch decisions = %#v", patch)
	}

	network := BuildApprovalDecisionOptions("shell", "shell", map[string]any{
		"command":                  "curl https://example.com",
		"network_approval_context": map[string]any{"host": "Example.COM.", "protocol": "https"},
	})
	if len(network) != 4 || network[2].NetworkPolicyAmendment == nil || network[2].NetworkPolicyAmendment.Host != "example.com" ||
		network[2].NetworkPolicyAmendment.Action != NetworkPolicyAllow || network[3].Decision != DecisionCancel {
		t.Fatalf("network decisions = %#v", network)
	}
}

func TestCanonicalToolName(t *testing.T) {
	cases := map[string]string{
		"shell":          "Bash",
		"Bash":           "Bash",
		"bash":           "Bash",
		"read_file":      "Read",
		"Read":           "Read",
		"write_file":     "Write",
		"edit_file":      "Edit",
		"multi_edit":     "MultiEdit",
		"web_fetch":      "WebFetch",
		"WebFetch":       "WebFetch",
		"web_search":     "WebSearch",
		"list_directory": "LS",
		"unknown_tool":   "unknown_tool",
		"":               "",
	}
	for in, want := range cases {
		if got := CanonicalToolName(in); got != want {
			t.Fatalf("CanonicalToolName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestEngineMatchesCanonicalRulesAgainstInternalToolName(t *testing.T) {
	cases := []struct {
		name        string
		ruleTool    string
		runtimeTool string
		ruleContent string
		input       string
	}{
		{"Read matches read_file", "Read", "read_file", "/repo/secret", "/repo/secret"},
		{"Write matches write_file", "Write", "write_file", "/repo/out", "/repo/out"},
		{"Edit matches edit_file", "Edit", "edit_file", "/repo/main.go", "/repo/main.go"},
		{"WebFetch matches web_fetch by domain", "WebFetch", "web_fetch", "domain:github.com", "https://github.com/x"},
		{"Bash matches shell prefix", "Bash", "Bash", "git status:*", "git status --short"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStore()
			ApplyUpdate(s, PermissionUpdate{
				Type:        UpdateAddRules,
				Destination: DestinationSession,
				SessionID:   permissionTestSessionID,
				Behavior:    BehaviorAllow,
				Rules:       []PermissionRuleValue{{ToolName: tc.ruleTool, RuleContent: tc.ruleContent}},
			})
			d := NewEngine().EvaluateForSession(s, permissionTestSessionID, tc.runtimeTool, tc.input)
			if d.Behavior != BehaviorAllow {
				t.Fatalf("expected allow via canonical alias, got %s (%s)", d.Behavior, d.Reason)
			}
		})
	}
}

func TestIsSafeReadOnlyToolAcceptsCanonical(t *testing.T) {
	// skill loads a SKILL.md out of the skills this session already discovered,
	// whose names, descriptions and paths every request already carries. A
	// prompt here would gate the instructions while their description flows
	// unprompted, and would turn every skill the model reaches for into an
	// interruption.
	for _, tool := range []string{"Read", "read_file", "LS", "list_directory", "skill"} {
		if !IsSafeReadOnlyTool(tool) {
			t.Fatalf("expected %q to be safe read-only", tool)
		}
	}
	for _, tool := range []string{"Write", "write_file", "Edit", "edit_file", "Bash", "shell"} {
		if IsSafeReadOnlyTool(tool) {
			t.Fatalf("expected %q to NOT be safe read-only", tool)
		}
	}
}

func TestApplyUpdateRules(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationSession,
		SessionID:   permissionTestSessionID,
		Behavior:    BehaviorAllow,
		Rules:       []PermissionRuleValue{{ToolName: "Bash", RuleContent: "npm run:*"}},
	})
	rules := s.SnapshotForSession(permissionTestSessionID).Rules[SourceSession][BehaviorAllow]
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}

	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationSession,
		SessionID:   permissionTestSessionID,
		Behavior:    BehaviorAllow,
		Rules:       []PermissionRuleValue{{ToolName: "Bash", RuleContent: "npm run:*", BypassSandbox: true}},
	})
	rules = s.SnapshotForSession(permissionTestSessionID).Rules[SourceSession][BehaviorAllow]
	if len(rules) != 1 || !rules[0].BypassSandbox {
		t.Fatalf("expected duplicate rule to merge sandbox-bypass capability, got %#v", rules)
	}

	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateRemoveRules,
		Destination: DestinationSession,
		SessionID:   permissionTestSessionID,
		Behavior:    BehaviorAllow,
		Rules:       []PermissionRuleValue{{ToolName: "Bash", RuleContent: "npm run:*"}},
	})
	if got := len(s.SnapshotForSession(permissionTestSessionID).Rules[SourceSession][BehaviorAllow]); got != 0 {
		t.Fatalf("expected rules removed, got %d", got)
	}
}

func TestSessionRulesAreIsolated(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{
		Type:        UpdateAddRules,
		Destination: DestinationSession,
		SessionID:   permissionTestSessionID,
		Behavior:    BehaviorAllow,
		Rules:       []PermissionRuleValue{{ToolName: "Bash", RuleContent: "npm run:*", BypassSandbox: true}},
	})

	engine := NewEngine()
	if got := engine.EvaluateForSession(s, permissionTestSessionID, "Bash", "npm run build"); got.Behavior != BehaviorAllow || !got.BypassSandbox {
		t.Fatalf("expected session rule in owning session, got %+v", got)
	}
	if got := engine.EvaluateForSession(s, "session-2", "Bash", "npm run build"); got.Matched != nil || got.BypassSandbox {
		t.Fatalf("session rule leaked to another session: %+v", got)
	}
	if got := engine.Evaluate(s, "Bash", "npm run build"); got.Matched != nil || got.BypassSandbox {
		t.Fatalf("session rule leaked to global evaluation: %+v", got)
	}
}

func TestApplyUpdateSetMode(t *testing.T) {
	s := NewStore()
	ApplyUpdate(s, PermissionUpdate{Type: UpdateSetMode, Mode: ModeNever})
	if s.Mode() != ModeNever {
		t.Fatalf("expected never mode, got %s", s.Mode())
	}
}

// macOS emits this preamble on every git invocation when xcrun cannot create
// its own cache file.  It contains "Operation not permitted" but says nothing
// about the sandbox policy, so it must never drive the denial classification.
const macOSXcrunNoise = "git: warning: confstr() failed with code 5: couldn't get path of DARWIN_USER_TEMP_DIR; using /tmp instead\n" +
	"git: error: couldn't create cache file '/tmp/xcrun_db-cvapToHC' (errno=Operation not permitted)\n"

func TestIsLikelySandboxDeniedUsesDenialMarkers(t *testing.T) {
	sandboxed := SandboxDecision{UseSandbox: true}
	for _, output := range []string{
		"Operation not permitted",
		"permission denied",
		"read-only file system",
		"seccomp blocked syscall",
		"sandbox policy rejected operation",
		"landlock denied",
		"failed to write file",
	} {
		if !IsLikelySandboxDenied(sandboxed, CommandResult{ExitCode: 1, Stderr: output}) {
			t.Fatalf("marker not classified: %q", output)
		}
	}
	if IsLikelySandboxDenied(sandboxed, CommandResult{ExitCode: 1, Stderr: "unit tests failed"}) {
		t.Fatal("ordinary command failure was misclassified")
	}
	for _, output := range []string{
		"source mentions sandbox policy",
		"fixture says permission denied",
	} {
		if !IsLikelySandboxDenied(sandboxed, CommandResult{ExitCode: 1, Stdout: output}) {
			t.Fatalf("stdout marker was not classified: %q", output)
		}
	}
	if !IsLikelySandboxDenied(sandboxed, CommandResult{ExitCode: 1, AggregatedOutput: "read-only file system"}) {
		t.Fatal("aggregated output marker was not classified")
	}
	for _, code := range []int{2, 126, 127} {
		if IsLikelySandboxDenied(sandboxed, CommandResult{ExitCode: code, Stderr: "command failed"}) {
			t.Fatalf("ordinary exit code %d was misclassified", code)
		}
	}
	if !IsLikelySandboxDenied(SandboxDecision{UseSandbox: true, SandboxType: SandboxTypeLinuxSeccomp}, CommandResult{ExitCode: sandboxSIGSYSExitCode()}) {
		t.Fatal("Linux seccomp SIGSYS exit was not classified")
	}
	if !IsLikelySandboxDenied(SandboxDecision{UseSandbox: true, Backend: BackendBubblewrap}, CommandResult{ExitCode: 127, Stderr: "sandbox: operation not permitted"}) {
		t.Fatal("a denial marker must override a quick-reject exit code")
	}
	if IsLikelySandboxDenied(sandboxed, CommandResult{ExitCode: 1, Stderr: "permission", Stdout: "denied"}) {
		t.Fatal("markers split across output sections must not be combined")
	}
	if IsLikelySandboxDenied(SandboxDecision{UseSandbox: false}, CommandResult{ExitCode: 1, Stderr: "permission denied"}) {
		t.Fatal("host failure was misclassified as a sandbox denial")
	}
	if IsLikelySandboxDenied(sandboxed, CommandResult{ExitCode: 0, Stderr: "permission denied"}) {
		t.Fatal("successful command was misclassified")
	}
}

func TestSandboxDenialDetailReturnsConcreteFailureLine(t *testing.T) {
	result := CommandResult{Stderr: "--- FAIL: TestOAuth\nlistener_test.go:28: listen tcp 127.0.0.1:0: bind: operation not permitted\nFAIL"}
	want := "listener_test.go:28: listen tcp 127.0.0.1:0: bind: operation not permitted"
	if got := SandboxDenialDetail(result); got != want {
		t.Fatalf("SandboxDenialDetail() = %q, want %q", got, want)
	}
}

func TestIsLikelySandboxDeniedIgnoresMacOSXcrunNoise(t *testing.T) {
	sandboxed := SandboxDecision{UseSandbox: true}

	// The exact regression: `git submodule status` fails for a real git reason
	// while macOS noise is present on stderr. This is not a sandbox denial.
	realGitFailure := macOSXcrunNoise + "fatal: no submodule mapping found in .gitmodules for path 'forebrain-harness.github.io'\n"
	if IsLikelySandboxDenied(sandboxed, CommandResult{ExitCode: 1, Stderr: realGitFailure}) {
		t.Fatal("git failure with macOS xcrun noise was misclassified as a sandbox denial")
	}

	// Noise alone, with no other failure detail, is still not a denial.
	if IsLikelySandboxDenied(sandboxed, CommandResult{ExitCode: 1, Stderr: macOSXcrunNoise}) {
		t.Fatal("macOS xcrun noise alone was misclassified as a sandbox denial")
	}
	if IsLikelySandboxDenied(sandboxed, CommandResult{ExitCode: 1, AggregatedOutput: macOSXcrunNoise}) {
		t.Fatal("macOS xcrun noise in aggregated output was misclassified")
	}

	// A genuine denial that arrives alongside the same noise must still be
	// caught: stripping is line-granular, not output-wide.
	genuineDenial := macOSXcrunNoise + "fatal: Unable to create '/repo/.git/index.lock': Operation not permitted\n"
	if !IsLikelySandboxDenied(sandboxed, CommandResult{ExitCode: 1, Stderr: genuineDenial}) {
		t.Fatal("a real denial next to macOS noise must still be classified as denied")
	}
}

func TestStripBenignNoisePreservesRealContent(t *testing.T) {
	if got := stripBenignNoise(""); got != "" {
		t.Fatalf("empty input produced %q", got)
	}
	// Output free of noise markers must pass through byte-for-byte.
	clean := "fatal: not a git repository\npermission denied\n"
	if got := stripBenignNoise(clean); got != clean {
		t.Fatalf("clean output was modified: %q", got)
	}
	got := stripBenignNoise(macOSXcrunNoise + "real error\n")
	if strings.Contains(strings.ToLower(got), "operation not permitted") {
		t.Fatalf("noise line survived stripping: %q", got)
	}
	if !strings.Contains(got, "real error") {
		t.Fatalf("real content was dropped: %q", got)
	}
}

func TestCommandCapturePreservesAggregateWriteOrder(t *testing.T) {
	capture := newCommandCapture(CommandRequest{OutputSpoolDir: t.TempDir()})
	stdoutWriter := capture.writer(OutputStreamStdout)
	stderrWriter := capture.writer(OutputStreamStderr)
	_, _ = stdoutWriter.Write([]byte("one\n"))
	_, _ = stderrWriter.Write([]byte("two\n"))
	_, _ = stdoutWriter.Write([]byte("three\n"))
	if got := capture.result().AggregatedOutput; got != "one\ntwo\nthree\n" {
		t.Fatalf("aggregated output=%q", got)
	}
}

func TestWebFetchRuleMatchesExactURL(t *testing.T) {
	if !WebFetchRuleMatches("https://example.com/api", "https://example.com/api") {
		t.Fatal("exact URL match failed")
	}
	if WebFetchRuleMatches("https://example.com/api", "https://example.com/other") {
		t.Fatal("exact URL match incorrectly broad")
	}
}

func TestWebFetchRuleMatchesDomain(t *testing.T) {
	if !WebFetchRuleMatches("domain:github.com", "https://github.com/foo/bar") {
		t.Fatal("domain rule should match URL host")
	}
	if WebFetchRuleMatches("domain:github.com", "https://gitlab.com/foo/bar") {
		t.Fatal("domain rule must not match different host")
	}
	if WebFetchRuleMatches("domain:github.com", "https://api.github.com/foo") {
		t.Fatal("domain rule must not match subdomain unless wildcard")
	}
}

func TestWebFetchRuleMatchesDomainWildcard(t *testing.T) {
	if !WebFetchRuleMatches("domain:*.github.com", "https://api.github.com/foo") {
		t.Fatal("wildcard subdomain failed")
	}
	if !WebFetchRuleMatches("domain:*.github.com", "https://raw.github.com/foo") {
		t.Fatal("wildcard subdomain failed for second host")
	}
	if WebFetchRuleMatches("domain:*.github.com", "https://github.com/foo") {
		t.Fatal("wildcard subdomain incorrectly matches bare host")
	}
}

func TestWebFetchRuleMatchesIsCaseInsensitiveOnHost(t *testing.T) {
	if !WebFetchRuleMatches("domain:GitHub.com", "https://github.com/foo") {
		t.Fatal("host comparison should be case-insensitive")
	}
}

func TestWebFetchRuleMatchesRejectsInvalidURL(t *testing.T) {
	if WebFetchRuleMatches("domain:github.com", "not-a-url") {
		t.Fatal("invalid URL should not match domain rule")
	}
}

func TestWebFetchRuleMatchesURLWildcard(t *testing.T) {
	if !WebFetchRuleMatches("https://example.com/*", "https://example.com/foo") {
		t.Fatal("URL wildcard match failed")
	}
}

func TestWindowsNetworkPolicyUsesOnlineIdentityOnlyForUnrestrictedNetwork(t *testing.T) {
	policy := windowsNetworkPolicyForRequest(CommandRequest{AllowNetwork: true})
	if policy.Offline {
		t.Fatalf("policy=%+v", policy)
	}
}

func TestWindowsNetworkPolicyLimitsOfflineIdentityToManagedProxy(t *testing.T) {
	policy := windowsNetworkPolicyForRequest(CommandRequest{
		AllowNetwork: true,
		ManagedNetwork: &ManagedNetworkCommand{
			HTTPPort:     3128,
			SOCKSPort:    1080,
			SOCKSUDPPort: 1080,
		},
	})
	if !policy.Offline || policy.AllowAllLoopback || !reflect.DeepEqual(policy.AllowedTCPPorts, []int{1080, 3128}) {
		t.Fatalf("policy=%+v", policy)
	}
	if got := blockedWindowsTCPPorts(policy.AllowedTCPPorts); got != "1-1079,1081-3127,3129-65535" {
		t.Fatalf("blocked ports=%q", got)
	}
	if !reflect.DeepEqual(policy.AllowedUDPPorts, []int{1080}) {
		t.Fatalf("udp ports=%v", policy.AllowedUDPPorts)
	}
}

func TestWindowsNetworkPolicyCanExplicitlyAllowLoopback(t *testing.T) {
	policy := windowsNetworkPolicyForRequest(CommandRequest{
		ManagedNetwork: &ManagedNetworkCommand{AllowLocalBinding: true},
	})
	if !policy.Offline || !policy.AllowAllLoopback {
		t.Fatalf("policy=%+v", policy)
	}
}

func TestBlockedWindowsTCPPortsNormalizesInvalidAndDuplicatePorts(t *testing.T) {
	if got := blockedWindowsTCPPorts([]int{0, 65536, 80, 80, 1, 65535}); got != "2-79,81-65534" {
		t.Fatalf("blocked ports=%q", got)
	}
}

func TestRuntimeYOLOEnabledFromEnv(t *testing.T) {
	t.Setenv(EnvYOLO, "1")
	if !RuntimeYOLOEnabled() {
		t.Fatalf("expected YOLO env to enable runtime override")
	}
}

func TestApplyYOLOForcesDangerFullAccessConfig(t *testing.T) {
	t.Setenv(EnvYOLO, "1")
	cfg := &appcfg.Root{DefaultPermissions: "restricted"}
	if !ApplyYOLO(cfg) {
		t.Fatalf("expected ApplyYOLO to report active override")
	}
	if cfg.DefaultPermissions != "" || cfg.SandboxMode != appcfg.SandboxModeDangerFullAccess || cfg.ApprovalPolicy.Mode != appcfg.ApprovalPolicyNever {
		t.Fatalf("unexpected YOLO config: %+v", cfg)
	}
}

func TestBuildToolApprovalSuggestionReturnsShellPrefix(t *testing.T) {
	got := BuildToolApprovalSuggestion("shell", map[string]any{
		"command": "git commit -m x",
	})
	if got.PrefixRuleContent != "git commit:*" {
		t.Fatalf("expected shell prefix suggestion, got %+v", got)
	}
}

// The helpers below were production functions that only the tests in this
// package ever called. They live here so the production files carry no
// unused code while the tests keep exercising the live code underneath:
// FormatRuleString's round trip, IntersectRequestPermissionsResponseAtCWD
// and SplitTopLevelWithSeparators.

func ParseRuleString(raw string) (PermissionRuleValue, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return PermissionRuleValue{}, fmt.Errorf("empty rule")
	}
	open := firstUnescapedRune(s, '(')
	if open < 0 {
		return PermissionRuleValue{ToolName: s}, nil
	}
	if !strings.HasSuffix(s, ")") {
		return PermissionRuleValue{}, fmt.Errorf("invalid rule syntax: missing closing parenthesis")
	}
	tool := strings.TrimSpace(s[:open])
	if tool == "" {
		return PermissionRuleValue{}, fmt.Errorf("invalid rule syntax: empty tool name")
	}
	inside := s[open+1 : len(s)-1]
	content := unescapeRuleContent(inside)
	if strings.TrimSpace(content) == "" {
		return PermissionRuleValue{ToolName: tool}, nil
	}
	return PermissionRuleValue{
		ToolName:    tool,
		RuleContent: content,
	}, nil
}

func firstUnescapedRune(s string, r rune) int {
	escaped := false
	for i, ch := range s {
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == r {
			return i
		}
	}
	return -1
}

func unescapeRuleContent(s string) string {
	if s == "" {
		return s
	}
	out := strings.Builder{}
	escaped := false
	for _, ch := range s {
		if escaped {
			out.WriteRune(ch)
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		out.WriteRune(ch)
	}
	if escaped {
		out.WriteRune('\\')
	}
	return out.String()
}

// The helpers below were production functions that only the tests in this
// package ever called. They live here so the production files carry no
// unused code while the tests keep exercising the live code underneath:
// FormatRuleString's round trip, IntersectRequestPermissionsResponseAtCWD
// and SplitTopLevelWithSeparators.

// IntersectRequestPermissionsResponse keeps only grants covered by the request.
// A write request covers both read and write grants. Deny entries are retained
// when they constrain an accepted grant.
func IntersectRequestPermissionsResponse(requested, granted RequestPermissionsResponse) (RequestPermissionsResponse, error) {
	cwd, _ := os.Getwd()
	return IntersectRequestPermissionsResponseAtCWD(requested, granted, cwd)
}

// Refusing a memory call must not read as "abandon the turn": the note or
// lookup is incidental to whatever the user asked for, so the negative choice
// is a decline the run survives, never a cancel.
func TestBuildApprovalDecisionOptionsOffersDeclineForMemoryTools(t *testing.T) {
	for _, tool := range []struct {
		name  string
		input map[string]any
	}{
		{"memories_add_ad_hoc_note", map[string]any{"filename": "2026-09-11T08-54-34-note.md", "note": "# Note"}},
		{"memories_read", map[string]any{"path": "MEMORY.md"}},
		{"memories_list", map[string]any{"path": "extensions/ad_hoc/notes"}},
		{"memories_search", map[string]any{"queries": []string{"endpoint"}}},
	} {
		options := BuildApprovalDecisionOptions(tool.name, tool.name, tool.input)
		if len(options) != 2 || options[0].Decision != DecisionAccept || options[1].Decision != DecisionDecline {
			t.Fatalf("%s decisions = %#v", tool.name, options)
		}
	}
}
