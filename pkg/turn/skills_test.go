package turn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
)

func TestSkillCommandSubmitsTrustedSelectionOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	write := func(body string) {
		t.Helper()
		data := "---\nname: fresh\ndescription: fresh request\n---\n" + body
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("old instructions")
	cmd, ok := skillCommand(path)
	if !ok {
		t.Fatal("expected skill command")
	}
	write("new instructions")
	line := "/fresh first line\n  indented value"
	res := cmd.Handler(Context{}, line, strings.Fields(line))
	if got, want := res.ContinueInput, "first line\n  indented value"; got != want {
		t.Fatalf("input=%q want %q", got, want)
	}
	// The slash phase submits only the trusted selection: which skill and
	// where its file lived at registration time. Reading the file — and
	// reporting it missing or renamed — is the runner preload's job. (The
	// Result no longer even carries a content field.)
	if res.SkillName != "fresh" {
		t.Fatalf("skill name=%q", res.SkillName)
	}
	if res.SkillPath != path {
		t.Fatalf("skill path=%q want %q", res.SkillPath, path)
	}
}

// /skill-generator is not a builtin command: it is the slash command the
// skill-generator system skill derives from its own name. That is what makes
// the two surfaces consistent for free — the TUI and the web both build their
// command list from this one refresh — and what makes disabling the skill take
// the command with it.
func TestRefreshSkillsDerivesTheGeneratorCommand(t *testing.T) {
	home := t.TempDir()
	if err := skill.Install(home); err != nil {
		t.Fatalf("install system skills: %v", err)
	}
	t.Cleanup(ResetDynamic)
	if err := RefreshSkills(home, filepath.Join(home, "workspace"), safety.ProjectContext{}); err != nil {
		t.Fatalf("RefreshSkills: %v", err)
	}
	cmds := Visible(SurfaceTUI)
	names := map[string]bool{}
	for _, cmd := range cmds {
		names[cmd.Name] = true
	}
	if !names["skill-generator"] {
		t.Fatalf("/skill-generator missing from the command list: %v", names)
	}
	// It is a skill-derived command, not a builtin: the builtin table has no
	// such entry.
	if IsBuiltinName("skill-generator") {
		t.Fatal("skill-generator must not be in the builtin command table")
	}

	cmd, ok := Find("skill-generator")
	if !ok {
		t.Fatal("skill-generator command not found")
	}
	res := Execute(Context{Surface: SurfaceTUI, Home: home}, "/skill-generator tui-live-verify")
	if !res.Handled || !res.ShouldContinueRun {
		t.Fatalf("generator command did not hand off to a run: %+v", res)
	}
	if res.SkillName != "skill-generator" {
		t.Fatalf("skill name=%q", res.SkillName)
	}
	// The handler submits the trusted selection only; the path must point at a
	// file that is really there, because the runner loads it.
	if _, err := os.Stat(res.SkillPath); err != nil {
		t.Fatalf("SkillPath %q does not exist: %v", res.SkillPath, err)
	}
	if res.ContinueInput != "tui-live-verify" {
		t.Fatalf("inline argument was not passed through: %q", res.ContinueInput)
	}
	if cmd.Description == "" || !strings.Contains(cmd.Description, "skill") {
		t.Fatalf("command description is not the skill's own: %q", cmd.Description)
	}
}

// Turning the skill off from /skills turns the command off with it: the
// command is derived from the discovered set, so a disabled skill cannot still
// answer to its slash command.
func TestRefreshSkillsDropsTheGeneratorCommandWhenDisabled(t *testing.T) {
	home := t.TempDir()
	if err := skill.Install(home); err != nil {
		t.Fatalf("install system skills: %v", err)
	}
	workspace := filepath.Join(home, "workspace")
	launch := safety.ProjectContext{}
	generatorDir := filepath.Join(home, "skills", ".system", "skill-generator")
	t.Cleanup(ResetDynamic)

	if err := RefreshSkills(home, workspace, launch); err != nil {
		t.Fatal(err)
	}
	if _, ok := Find("skill-generator"); !ok {
		t.Fatal("precondition: the generator command should be registered")
	}
	// Disable every skill there is, which is what /skills does when the user
	// clears the enabled set.
	if err := skill.SetEnabledPathsForWorkspace(home, workspace, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := RefreshSkills(home, workspace, launch); err != nil {
		t.Fatal(err)
	}
	if _, ok := Find("skill-generator"); ok {
		t.Fatal("a disabled generator skill still answers to /skill-generator")
	}
	if err := skill.SetEnabledPathsForWorkspace(home, workspace, "", []string{generatorDir}); err != nil {
		t.Fatal(err)
	}
	if err := RefreshSkills(home, workspace, launch); err != nil {
		t.Fatal(err)
	}
	if _, ok := Find("skill-generator"); !ok {
		t.Fatal("re-enabling the skill did not bring the command back")
	}
}

// registerSkillCommands installs the named skills as dynamic commands whose
// trusted selection points at a real SKILL.md — the same shape RefreshSkills
// registers for installed skills, so a mid-sentence reference resolves to the
// same name and path a leading command would.
func registerSkillCommands(t *testing.T, names ...string) {
	t.Helper()
	ResetDynamic()
	t.Cleanup(ResetDynamic)
	cmds := make([]DynamicCommand, 0, len(names))
	for _, name := range names {
		path := filepath.Join(t.TempDir(), name+".md")
		body := "---\nname: " + name + "\ndescription: " + name + " skill\n---\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, DynamicCommand{
			Command: Command{
				Name: name, Description: name, Category: SkillCategory,
				AllowedSurfaces:    []Surface{SurfaceWebChat, SurfaceTUI},
				SupportsInlineArgs: true, Visibility: VisibilityPublic,
			},
			SkillPath: path,
			Handler: func(_ Context, line string, toks []string) Result {
				request := ""
				if len(toks) > 1 {
					request = strings.TrimSpace(strings.TrimPrefix(line, toks[0]))
				}
				return Result{
					Handled: true, ShouldContinueRun: true,
					ContinueInput: SkillCommandInput(name, request),
					SkillName:     name, SkillPath: path,
				}
			},
		})
	}
	if err := ReplaceDynamicSource("test-skills", cmds); err != nil {
		t.Fatalf("ReplaceDynamicSource: %v", err)
	}
}

func skillNames(sels []SkillSelection) []string {
	out := make([]string, len(sels))
	for i, s := range sels {
		out[i] = s.Name
	}
	return out
}

// A "/name" in the middle of a message names a skill wherever it sits: after a
// space, after a newline, several at once, de-duplicated, and never a built-in
// command (those only mean something at the start).
func TestSkillReferences(t *testing.T) {
	registerSkillCommands(t, "alpha", "beta")

	cases := []struct {
		name string
		text string
		want []string
	}{
		{"one after a space", "please run /alpha now", []string{"alpha"}},
		{"several in order", "use /alpha then /beta", []string{"alpha", "beta"}},
		{"after a newline", "do this\n/beta please", []string{"beta"}},
		{"duplicate collapses", "run /alpha and /alpha again", []string{"alpha"}},
		{"unknown ignored", "run /alpha and /nosuch", []string{"alpha"}},
		{"mid built-in ignored", "switch /plan and /alpha", []string{"alpha"}},
		{"leading token skipped", "/alpha with /beta", []string{"beta"}},
		{"word slash ignored", "see pkg/run and a/b", nil},
		{"trailing comma ends name", "use /alpha, thanks", []string{"alpha"}},
		{"none", "just a plain message", nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := skillNames(SkillReferences(tc.text))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("SkillReferences(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// Execute reports the same list for a plain message and for a leading skill
// command, and the leading command comes first.
func TestExecuteSkillsList(t *testing.T) {
	registerSkillCommands(t, "alpha", "beta")

	mid := Execute(Context{Surface: SurfaceWebChat}, "please run /alpha here")
	if got := skillNames(mid.Skills); strings.Join(got, ",") != "alpha" {
		t.Fatalf("plain message skills = %v, want [alpha]", got)
	}
	if mid.Handled || mid.ShouldContinueRun {
		t.Fatalf("a mid-sentence skill must not make the message a command: %+v", mid)
	}

	lead := Execute(Context{Surface: SurfaceWebChat}, "/alpha and also /beta")
	if got := skillNames(lead.Skills); strings.Join(got, ",") != "alpha,beta" {
		t.Fatalf("leading + mid skills = %v, want [alpha beta]", got)
	}
	if !lead.ShouldContinueRun || lead.SkillName != "alpha" {
		t.Fatalf("leading skill command did not hand off: %+v", lead)
	}
	// The message text is not rewritten: the mid-sentence reference stays in
	// the text the model sees.
	if !strings.Contains(lead.ContinueInput, "and also /beta") {
		t.Fatalf("message text was rewritten: %q", lead.ContinueInput)
	}
}
