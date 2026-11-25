package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

func TestDiscoverAnnotatesShadowedSkills(t *testing.T) {
	home := t.TempDir()

	systemDir := filepath.Join(home, "skills", ".system", "plan")
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		t.Fatalf("MkdirAll system: %v", err)
	}
	if err := os.WriteFile(filepath.Join(systemDir, "SKILL.md"), []byte(`---
name: plan
description: builtin plan
---
body
`), 0o600); err != nil {
		t.Fatalf("WriteFile system: %v", err)
	}

	userDir := filepath.Join(home, "skills", "plan")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatalf("MkdirAll user: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "SKILL.md"), []byte(`---
name: plan
description: user plan
---
body
`), 0o600); err != nil {
		t.Fatalf("WriteFile user: %v", err)
	}

	entries, err := Discover(home)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	var planEntries []Entry
	for _, e := range entries {
		if e.Name == "plan" {
			planEntries = append(planEntries, e)
		}
	}
	if len(planEntries) != 2 {
		t.Fatalf("expected 2 plan entries, got %d", len(planEntries))
	}

	var withShadowedBy, withShadows int
	for _, e := range planEntries {
		if len(e.ShadowedBy) > 0 {
			withShadowedBy++
		}
		if len(e.Shadows) > 0 {
			withShadows++
		}
	}
	if withShadowedBy != 1 || withShadows != 1 {
		t.Fatalf("expected one entry with ShadowedBy and one with Shadows; got shadowedBy=%d shadows=%d", withShadowedBy, withShadows)
	}
}

func TestDiscoverNoShadowsForUniqueNames(t *testing.T) {
	home := t.TempDir()
	skillDir := filepath.Join(home, "skills", "unique-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: unique-skill
description: only one
---
body
`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	entries, err := Discover(home)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, e := range entries {
		if len(e.ShadowedBy) > 0 || len(e.Shadows) > 0 {
			t.Fatalf("unexpected shadow on unique entry: %+v", e)
		}
	}
}

func writeSkill(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: " + name + " skill\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return NormalizeSkillPath(dir)
}

// A repository can ship skills in any of the directories the agent ecosystem
// has settled on — forebrain's own, the tool-neutral .agents one, or the ones
// Claude Code and Codex use. Once the project is trusted they are all ordinary
// project skills: labelled "project".
func TestDiscoverIncludesEveryTrustedProjectSkillDir(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	if err := safety.MarkTrusted(home, safety.Project{Root: repo, VersionControlled: true}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, dir := range []string{".forebrain", ".agents", ".claude", ".codex"} {
		name := "skill-from-" + strings.TrimPrefix(dir, ".")
		want[writeSkill(t, filepath.Join(repo, dir, "skills", name), name)] = name
	}

	entries, err := DiscoverForWorkspace(home, filepath.Join(home, "workspace"), repo)
	if err != nil {
		t.Fatalf("DiscoverForWorkspace: %v", err)
	}
	for _, entry := range entries {
		name, ok := want[entry.Path]
		if !ok {
			continue
		}
		if entry.Origin != OriginProject {
			t.Fatalf("%s: origin = %q, want project", name, entry.Origin)
		}
		delete(want, entry.Path)
	}
	if len(want) != 0 {
		t.Fatalf("project skills not discovered: %v", want)
	}
}

// The same four directories count in a plain directory too: trust is what
// makes a project, not version control. This is the listing path behind the
// /skills picker and the slash commands, so a skill installed into a trusted
// non-checkout folder must be listed here exactly as in a repository.
func TestDiscoverIncludesSkillsFromTrustedPlainDirectory(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	t.Chdir(dir)
	if err := safety.MarkTrusted(home, safety.Project{Root: dir}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, sub := range []string{".forebrain", ".agents", ".claude", ".codex"} {
		name := "plain-skill-from-" + strings.TrimPrefix(sub, ".")
		want[writeSkill(t, filepath.Join(dir, sub, "skills", name), name)] = name
	}

	entries, err := DiscoverForWorkspace(home, filepath.Join(home, "workspace"), dir)
	if err != nil {
		t.Fatalf("DiscoverForWorkspace: %v", err)
	}
	for _, entry := range entries {
		name, ok := want[entry.Path]
		if !ok {
			continue
		}
		if entry.Origin != OriginProject {
			t.Fatalf("%s: origin = %q, want project", name, entry.Origin)
		}
		if !entry.Enabled {
			t.Fatalf("%s: discovered project skill must default to enabled", name)
		}
		delete(want, entry.Path)
	}
	if len(want) != 0 {
		t.Fatalf("plain-directory project skills not discovered: %v", want)
	}
}

// The helpers below were production functions that only the tests in this
// package ever called: each is a home-scoped wrapper over the
// workspace-scoped function that actually runs, now that skill state is
// stored per primary agent under that agent's workspace root. They live
// here so the production files carry no unused code while the tests keep
// exercising the live functions underneath.

func SetDisabled(stateRoot, skillPath string, disabled bool) error {
	st, err := Load(stateRoot)
	if err != nil {
		return err
	}
	key := NormalizeSkillPath(skillPath)
	if key == "" {
		return nil
	}
	if disabled {
		st.Disabled[key] = true
	} else {
		delete(st.Disabled, key)
	}
	return Save(stateRoot, st)
}

func Discover(home string) ([]Entry, error) {
	home = strings.TrimSpace(home)
	return DiscoverForWorkspace(home, mainWorkspaceRoot(home), "")
}

// TestDiscoverForTrustedProjectListsTheProjectLayerWithoutARecord pins the
// batch exemption on the discovery side (owner 2026-11-25): the caller resolved
// that this project is trusted — a batch project is, by its type — so the
// project layer is listed with no record in the store. The same project asked
// through DiscoverForWorkspace, which reads the record, lists nothing.
func TestDiscoverForTrustedProjectListsTheProjectLayerWithoutARecord(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	project := t.TempDir()
	projectSkill := writeSkill(t, filepath.Join(project, ".forebrain", "skills", "batch-work"), "batch-work")

	entries, err := DiscoverForTrustedProject(home, ws, project)
	if err != nil {
		t.Fatalf("DiscoverForTrustedProject: %v", err)
	}
	found := false
	for _, entry := range entries {
		if entry.Path == projectSkill {
			found = true
			if entry.Origin != OriginProject {
				t.Fatalf("%s origin = %q, want project", entry.Path, entry.Origin)
			}
		}
	}
	if !found {
		t.Fatalf("the project layer is missing without a record: %+v", entries)
	}

	recorded, err := DiscoverForWorkspace(home, ws, project)
	if err != nil {
		t.Fatalf("DiscoverForWorkspace: %v", err)
	}
	for _, entry := range recorded {
		if entry.Path == projectSkill {
			t.Fatalf("an untrusted project's skill reached the listing: %+v", recorded)
		}
	}
}

// TestServiceDiscoverHonorsTheCallersTrustDecision pins that a Service carrying
// the caller's answer lists the project layer without a record, and that the
// same Service with the answer off does not fall back to anything else.
func TestServiceDiscoverHonorsTheCallersTrustDecision(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	projectSkill := writeSkill(t, filepath.Join(project, ".forebrain", "skills", "batch-work"), "batch-work")

	trusted := NewServiceForWorkspace(home, filepath.Join(home, "workspace"))
	trusted.ProjectRoot = project
	trusted.ProjectTrusted = true
	entries, err := trusted.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	found := false
	for _, entry := range entries {
		if entry.Path == projectSkill {
			found = true
		}
	}
	if !found {
		t.Fatalf("ProjectTrusted did not reach discovery: %+v", entries)
	}

	untrusted := NewServiceForWorkspace(home, filepath.Join(home, "workspace"))
	untrusted.ProjectRoot = project
	entries, err = untrusted.Discover()
	if err != nil {
		t.Fatalf("Discover (untrusted): %v", err)
	}
	for _, entry := range entries {
		if entry.Path == projectSkill {
			t.Fatalf("the project skill reached an untrusted service: %+v", entries)
		}
	}
}

// TestServiceSetEnabledPathsWritesThroughTheCallersTrustDecision pins the write
// half of the batch exemption (owner 2026-11-25): a project whose trust the
// caller resolved can have its skills toggled with no record, and the toggle is
// stored. The write path must discover through the service — discovering on its
// own, from the record, would leave the project's skills out of the entries and
// silently write nothing.
func TestServiceSetEnabledPathsWritesThroughTheCallersTrustDecision(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	ws := filepath.Join(home, "workspace")
	projectSkill := writeSkill(t, filepath.Join(project, ".forebrain", "skills", "batch-work"), "batch-work")
	disabled := func() map[string]bool {
		t.Helper()
		st, err := Load(ws)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return st.Disabled
	}

	svc := NewServiceForWorkspace(home, ws)
	svc.ProjectRoot = project
	svc.ProjectTrusted = true
	if err := svc.SetEnabledPaths(nil); err != nil {
		t.Fatalf("SetEnabledPaths: %v", err)
	}
	if !disabled()[projectSkill] {
		t.Fatalf("the project skill was not disabled: %+v", disabled())
	}
	if err := svc.SetEnabledPaths([]string{projectSkill}); err != nil {
		t.Fatalf("SetEnabledPaths (enable): %v", err)
	}
	if disabled()[projectSkill] {
		t.Fatalf("the project skill stayed disabled: %+v", disabled())
	}

	// The same service without the caller's answer writes nothing for it: the
	// store does not trust this project.
	other := NewServiceForWorkspace(home, ws)
	other.ProjectRoot = project
	if err := other.SetEnabledPaths(nil); err != nil {
		t.Fatalf("SetEnabledPaths (untrusted): %v", err)
	}
	if disabled()[projectSkill] {
		t.Fatalf("an untrusted service wrote the project skill: %+v", disabled())
	}
}
