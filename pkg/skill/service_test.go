package skill_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// workspaceHasSkill asks what this service's workspace can load. It replaces a
// lookup in a package-level registry: a workspace's skills are the workspace's,
// and a process-wide answer to that question was shared by every agent and
// every test in the process. The project scope follows the service's own root,
// the same frozen value a real consumer resolves.
func workspaceHasSkill(t *testing.T, svc *skill.Service, name string) bool {
	t.Helper()
	names, err := skill.DistinctSkillNamesForWorkspace(svc.Home, svc.Workspace(), svc.ProjectRoot)
	if err != nil {
		t.Fatalf("list workspace skills: %v", err)
	}
	return slices.Contains(names, name)
}

func requireWorkspaceSkill(t *testing.T, svc *skill.Service, name string) {
	t.Helper()
	if !workspaceHasSkill(t, svc, name) {
		t.Fatalf("expected the workspace to carry the installed skill %q", name)
	}
}

func testService(home string) *skill.Service {
	svc := skill.NewService(home)
	svc.OnRefresh = func() error { return turn.RefreshSkills(svc.Home, svc.Workspace(), safety.ProjectContext{}) }
	svc.IsBuiltin = turn.IsBuiltinName
	return svc
}

func TestRefreshRegistersInstalledSkillSlashCommands(t *testing.T) {
	home := t.TempDir()
	opsPath := filepath.Join(home, "skills", "opsx-toolkit", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(opsPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(opsPath, []byte(`---
name: opsx-toolkit
description: installed ops
---

body
`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	svc := testService(home)
	if err := svc.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	t.Cleanup(turn.ResetDynamic)

	requireWorkspaceSkill(t, svc, "opsx-toolkit")
	cmd, ok := turn.Find("opsx-toolkit")
	if !ok {
		t.Fatalf("expected auto-derived slash command to be registered")
	}
	if cmd.Description != "installed ops" {
		t.Fatalf("unexpected command: %+v", cmd)
	}
	res := turn.Execute(turn.Context{Home: home, SessionID: "s1", Surface: turn.SurfaceWebChat}, "/opsx-toolkit demo arg")
	if !res.Handled || !res.ShouldContinueRun {
		t.Fatalf("expected auto-derived slash command to handle and continue, got %+v", res)
	}
	if res.ContinueInput != "demo arg" {
		t.Fatalf("expected request to be passed through, got %q", res.ContinueInput)
	}
	if res.SkillName != "opsx-toolkit" {
		t.Fatalf("expected deterministic skill selection, got name=%q", res.SkillName)
	}
	if !strings.HasSuffix(res.SkillPath, filepath.Join("skills", "opsx-toolkit", "SKILL.md")) {
		t.Fatalf("expected trusted skill path, got %q", res.SkillPath)
	}
}

func TestRefreshRegistersSlashCommandsIndependentOfExposureMode(t *testing.T) {
	home := t.TempDir()
	writeSkill := func(dir, body string) {
		path := filepath.Join(home, "skills", dir, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("MkdirAll %s: %v", dir, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", dir, err)
		}
	}
	writeSkill("always-skill", `---
name: always-skill
description: always exposed
metadata:
  llm_exposure:
    mode: always
---

body
`)
	writeSkill("search-skill", `---
name: search-skill
description: search exposed
metadata:
  llm_exposure:
    mode: search
---

body
`)
	writeSkill("default-skill", `---
name: default-skill
description: default exposed
---

body
`)

	svc := testService(home)
	if err := svc.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	t.Cleanup(turn.ResetDynamic)

	for _, name := range []string{"always-skill", "search-skill", "default-skill"} {
		cmd, ok := turn.Find(name)
		if !ok {
			t.Fatalf("expected slash command for %s", name)
		}
		if cmd.Name != name {
			t.Fatalf("unexpected slash command for %s: %+v", name, cmd)
		}
		res := turn.Execute(turn.Context{Home: home, SessionID: "s1", Surface: turn.SurfaceWebChat}, "/"+name+" demo")
		if !res.Handled || !res.ShouldContinueRun {
			t.Fatalf("expected /%s to continue run, got %+v", name, res)
		}
		if res.ContinueInput != "demo" || res.SkillName != name || res.SkillPath == "" {
			t.Fatalf("expected explicit selection for %s, got input=%q name=%q path=%q", name, res.ContinueInput, res.SkillName, res.SkillPath)
		}
	}
}

func TestInstallSkillsShPackageSkillIntoProjectScope(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "anthropics-skills")
	if err := os.MkdirAll(filepath.Join(repo, "pptx"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "pptx", "SKILL.md"), []byte(`---
name: pptx
description: pptx demo
---

body
`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	gitCmd := exec.Command("git", "init")
	gitCmd.Dir = repo
	if out, err := gitCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, string(out))
	}
	gitCmd = exec.Command("git", "add", ".")
	gitCmd.Dir = repo
	if out, err := gitCmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, string(out))
	}
	gitCmd = exec.Command("git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "init")
	gitCmd.Dir = repo
	if out, err := gitCmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v %s", err, string(out))
	}
	if err := os.MkdirAll(filepath.Join(home, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll launch project marker: %v", err)
	}
	project, err := safety.Resolve(home)
	if err != nil {
		t.Fatalf("Resolve project: %v", err)
	}
	if err := safety.MarkTrusted(home, project); err != nil {
		t.Fatalf("MarkTrusted: %v", err)
	}

	// The project scope follows the service's frozen root, and the launch
	// context feeds the derived slash commands — no chdir anywhere.
	launch, err := safety.ResolveProjectContext(home, home)
	if err != nil {
		t.Fatalf("ResolveProjectContext: %v", err)
	}
	svc := testService(home)
	svc.ProjectRoot = home
	svc.OnRefresh = func() error { return turn.RefreshSkills(svc.Home, svc.Workspace(), launch) }
	installed, err := svc.Install(context.Background(), skill.InstallRequest{
		SourceRef: "file://" + repo,
		Skill:     "pptx",
		Name:      "pptx-installed",
		DestScope: skill.ScopeProject,
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	t.Cleanup(turn.ResetDynamic)
	if installed == nil || installed.Metadata == nil {
		t.Fatalf("expected install metadata, got %+v", installed)
	}
	if installed.Count != 1 || len(installed.Installed) != 1 {
		t.Fatalf("expected single installed skill metadata, got %+v", installed)
	}
	if installed.Metadata.Source != "project" {
		t.Fatalf("source=%q want project", installed.Metadata.Source)
	}

	// The package installs into a directory named for the archive; the skill's
	// own name is what the workspace carries it under.
	if !workspaceHasSkill(t, svc, "pptx") {
		t.Fatalf("expected the workspace to carry the installed skill")
	}
	slashCommand, ok := turn.Find("pptx")
	if !ok {
		t.Fatalf("expected installed slash command to be registered")
	}
	if slashCommand.Description != "pptx demo" {
		t.Fatalf("unexpected command description: %+v", slashCommand)
	}
}

func TestInstallSkillsShPackageCollectionIntoWorkspaceScope(t *testing.T) {
	home := t.TempDir()
	t.Cleanup(turn.ResetDynamic)
	repo := filepath.Join(home, "cc-skills-golang")
	requireGitRepoWithSkills := func() {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(repo, "skills", "golang-cli"), 0o755); err != nil {
			t.Fatalf("MkdirAll golang-cli: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(repo, "skills", "golang-testing"), 0o755); err != nil {
			t.Fatalf("MkdirAll golang-testing: %v", err)
		}
		if err := os.WriteFile(filepath.Join(repo, "skills", "golang-cli", "SKILL.md"), []byte(`---
name: golang-cli
description: golang cli
---
body
`), 0o600); err != nil {
			t.Fatalf("WriteFile golang-cli: %v", err)
		}
		if err := os.WriteFile(filepath.Join(repo, "skills", "golang-testing", "SKILL.md"), []byte(`---
name: golang-testing
description: golang testing
---
body
`), 0o600); err != nil {
			t.Fatalf("WriteFile golang-testing: %v", err)
		}
		gitCmd := exec.Command("git", "init")
		gitCmd.Dir = repo
		if out, err := gitCmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, string(out))
		}
		gitCmd = exec.Command("git", "add", ".")
		gitCmd.Dir = repo
		if out, err := gitCmd.CombinedOutput(); err != nil {
			t.Fatalf("git add: %v %s", err, string(out))
		}
		gitCmd = exec.Command("git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "init")
		gitCmd.Dir = repo
		if out, err := gitCmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit: %v %s", err, string(out))
		}
	}
	requireGitRepoWithSkills()

	svc := testService(home)
	installed, err := svc.Install(context.Background(), skill.InstallRequest{
		SourceRef: "file://" + repo,
		DestScope: skill.ScopeWorkspace,
	})
	if err != nil {
		t.Fatalf("Install collection package: %v", err)
	}
	if installed == nil {
		t.Fatal("expected install result")
	}
	if installed.Count != 2 || len(installed.Installed) != 2 {
		t.Fatalf("expected two installed skills, got %+v", installed)
	}
	if _, err := os.Stat(filepath.Join(home, "workspace", "skills", "golang-cli", "SKILL.md")); err != nil {
		t.Fatalf("golang-cli missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "workspace", "skills", "golang-testing", "SKILL.md")); err != nil {
		t.Fatalf("golang-testing missing: %v", err)
	}
	lock, err := skill.ReadLock(filepath.Join(home, "workspace"))
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	entries, _ := lock["entries"].(map[string]any)
	if entries == nil {
		t.Fatalf("expected lock entries, got %+v", lock)
	}
	golangCLI, _ := entries["golang-cli"].(map[string]any)
	if got := golangCLI["source_type"]; got != "git_repo" {
		t.Fatalf("golang-cli source_type=%v want git_repo", got)
	}
	if got := golangCLI["skill_subpath"]; got != "skills/golang-cli" {
		t.Fatalf("golang-cli skill_subpath=%v want skills/golang-cli", got)
	}
}

// An installed skill named like a built-in does not take the built-in over:
// /plan still enters Plan mode.
func TestPlanSkillDoesNotShadowBuiltinPlan(t *testing.T) {
	home := t.TempDir()
	planPath := filepath.Join(home, "skills", ".system", "plan", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(planPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(planPath, []byte(`---
name: plan
description: shadow-plan-skill
---

body
`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	svc := testService(home)
	if err := svc.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	t.Cleanup(turn.ResetDynamic)

	cmd, ok := turn.Find("plan")
	if !ok || cmd.Description == "shadow-plan-skill" {
		t.Fatalf("/plan = %+v, want the built-in", cmd)
	}
	res := turn.Execute(turn.Context{Home: home, SessionID: "s1", Surface: turn.SurfaceWebChat}, "/plan")
	if res.SkillName != "" || !res.ModeChanged {
		t.Fatalf("/plan = %+v, want Plan mode entered", res)
	}
}

func TestRefreshSkipsDisabledSkill(t *testing.T) {
	home := t.TempDir()
	skillDir := filepath.Join(home, "skills", "fooskill")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: fooskill
description: foo description
---

body
`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// disabled.json lives under the agent's workspace state root, which is
	// where Refresh (via s.workspaceRoot()) reads it from.
	if err := skill.SetDisabled(filepath.Join(home, "workspace"), skillDir, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}

	svc := testService(home)
	if err := svc.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	t.Cleanup(turn.ResetDynamic)

	if _, ok := turn.Find("fooskill"); ok {
		t.Fatalf("expected disabled skill to register no slash command")
	}
}

// A meta-skill reached from a builtin command opts out of slash derivation, so
// one capability is not offered twice in the palette (the skill workshop behind
// /skills is the reason this exists).
func TestRefreshSkipsSlashCommandForOptedOutSkill(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "skills", "workshop-like", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(`---
name: workshop-like
description: reached from a builtin command
slash-command: false
---

body
`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	svc := testService(home)
	if err := svc.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	t.Cleanup(turn.ResetDynamic)

	if !workspaceHasSkill(t, svc, "workshop-like") {
		t.Fatalf("opting out of the slash command must not hide the skill itself")
	}
	if cmd, ok := turn.Find("workshop-like"); ok {
		t.Fatalf("expected no slash command, got %+v", cmd)
	}
}

// Project-scope writes follow the service's frozen project root. Two services
// for two projects therefore write into their own projects, which is what a
// gateway serving several projects needs: before this, every one of them wrote
// beside the process that happened to be running.
func TestProjectSkillWritesLandInTheServicesOwnProject(t *testing.T) {
	home := t.TempDir()
	projectA := filepath.Join(home, "project-a")
	projectB := filepath.Join(home, "project-b")
	for _, dir := range []string{projectA, projectB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	content := "---\nname: release-flow\ndescription: How to cut a release here\n---\n\nbody\n"

	svcA := skill.NewServiceForWorkspace(home, filepath.Join(home, "workspace"))
	svcA.ProjectRoot = projectA
	svcB := skill.NewServiceForWorkspace(home, filepath.Join(home, "workspace"))
	svcB.ProjectRoot = projectB

	if _, err := svcA.Create(skill.CreateRequest{Name: "release-flow", Content: content}); err != nil {
		t.Fatalf("create in A: %v", err)
	}
	if _, err := svcB.Create(skill.CreateRequest{Name: "release-flow", Content: content}); err != nil {
		t.Fatalf("create in B: %v", err)
	}

	inA := filepath.Join(projectA, ".forebrain", "skills", "release-flow", "SKILL.md")
	inB := filepath.Join(projectB, ".forebrain", "skills", "release-flow", "SKILL.md")
	for _, path := range []string{inA, inB} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
	// Each project's copy is its own file, not a symlink or a shared path.
	rawA, err := os.ReadFile(inA)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(rawA)) != strings.TrimSpace(content) {
		t.Fatalf("A's copy is not the content that was written: %q", string(rawA))
	}
	// The workspace and home skill directories stay untouched: a project skill
	// belongs to the project.
	for _, stray := range []string{
		filepath.Join(home, "workspace", "skills", "release-flow"),
		filepath.Join(home, "skills", "release-flow"),
		filepath.Join(".forebrain", "skills", "release-flow"),
	} {
		if _, err := os.Stat(stray); err == nil {
			t.Fatalf("project skill leaked outside the project: %s", stray)
		}
	}
}

// A service with no project root refuses, rather than writing beside the
// process. Silently landing a project skill in the process directory is the
// failure this replaced: the user is told it was saved and cannot find it.
func TestProjectSkillWritesRefuseWithoutAProjectRoot(t *testing.T) {
	home := t.TempDir()
	content := "---\nname: nowhere\ndescription: No project here\n---\n\nbody\n"

	svc := skill.NewServiceForWorkspace(home, filepath.Join(home, "workspace"))
	for _, dir := range []string{"", "   ", "."} {
		svc.ProjectRoot = dir
		_, err := svc.Create(skill.CreateRequest{Name: "nowhere", Content: content})
		if err == nil {
			t.Fatalf("create with project root %q must fail", dir)
		}
		if !strings.Contains(err.Error(), "project") {
			t.Fatalf("error should say the project is missing, got %q", err.Error())
		}
	}
	if _, err := os.Stat(filepath.Join(home, "workspace", "skills", "nowhere")); err == nil {
		t.Fatal("a refused create still wrote a skill")
	}
}

// Updates land in the same project-scoped place, and an update for a skill
// that is not there fails instead of creating it somewhere else.
func TestProjectSkillUpdateUsesTheProjectRoot(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	svc := skill.NewServiceForWorkspace(home, filepath.Join(home, "workspace"))
	svc.ProjectRoot = project
	if _, err := svc.Create(skill.CreateRequest{
		Name:    "release-flow",
		Content: "---\nname: release-flow\ndescription: first\n---\n\nbody\n",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(skill.UpdateRequest{
		Name:    "release-flow",
		Content: "---\nname: release-flow\ndescription: second\n---\n\nbody\n",
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(project, ".forebrain", "skills", "release-flow", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "description: second") {
		t.Fatalf("update did not land in the project copy: %q", string(raw))
	}

	svc.ProjectRoot = ""
	if _, err := svc.Update(skill.UpdateRequest{
		Name:    "release-flow",
		Content: "---\nname: release-flow\ndescription: third\n---\n\nbody\n",
	}); err == nil {
		t.Fatal("update without a project root must fail")
	}
}
