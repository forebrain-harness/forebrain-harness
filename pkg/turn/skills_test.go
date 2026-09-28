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
