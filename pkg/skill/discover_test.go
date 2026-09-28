package skill

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDiscoverySkill(t *testing.T, dir, frontmatter, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\n"+frontmatter+"\n---\n\n"+body+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDiscoveryIgnoresSkillsWithoutADescription pins the rule in the one place
// every listing now comes through: a SKILL.md with no description is not a
// skill. The description is how a skill reaches the model at all — the catalog
// lists name, description and path, and the model decides from the description
// alone — so a skill without one could never be chosen, and listing it would
// only offer a toggle that changes nothing.
func TestDiscoveryIgnoresSkillsWithoutADescription(t *testing.T) {
	root := t.TempDir()
	writeDiscoverySkill(t, filepath.Join(root, "described"), "name: described\ndescription: Does a thing", "body")
	writeDiscoverySkill(t, filepath.Join(root, "bare"), "name: bare", "body")
	writeDiscoverySkill(t, filepath.Join(root, "blank"), "name: blank\ndescription: \"   \"", "body")

	found := scanSkillRoots([]string{root})
	if len(found) != 1 || found[0].Name != "described" {
		names := make([]string, 0, len(found))
		for _, item := range found {
			names = append(names, item.Name)
		}
		t.Fatalf("scanner returned %v, want only the described skill", names)
	}

	// Every listing is built on that scan, so none of them may disagree.
	discovered, err := (Loader{Roots: []string{root}}).Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered) != 1 || discovered[0].Name != "described" {
		t.Fatalf("runtime discovery returned %+v", discovered)
	}
	managed, err := (&Hub{Roots: []string{root}}).ListManaged()
	if err != nil {
		t.Fatal(err)
	}
	if len(managed) != 1 || managed[0].Name != "described" {
		t.Fatalf("management listing returned %+v", managed)
	}
	catalog, err := Catalog([]string{root}, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(catalog, "bare") || strings.Contains(catalog, "blank") {
		t.Fatalf("prompt catalog describes a skill with no description:\n%s", catalog)
	}
}

// A skill whose allowed-tools uses the YAML list spelling — the one Claude
// Code skills ship — used to fail frontmatter parsing and vanish from every
// listing at once. The scan is the one place that decides what counts as a
// skill, so this pins the spelling at that gate and at the management view
// built on it.
func TestDiscoveryAcceptsListSpelledAllowedTools(t *testing.T) {
	root := t.TempDir()
	writeDiscoverySkill(t, filepath.Join(root, "humanizer-zh"),
		"name: humanizer-zh\ndescription: 去除文本中的 AI 生成痕迹。\nallowed-tools:\n  - Read\n  - Write\n  - Edit",
		"body")

	found := scanSkillRoots([]string{root})
	if len(found) != 1 || found[0].Name != "humanizer-zh" {
		t.Fatalf("scanner returned %+v, want the humanizer-zh skill", found)
	}
	got := found[0].Skill.AllowedTools
	if len(got) != 3 || got[0] != "Read" || got[1] != "Write" || got[2] != "Edit" {
		t.Fatalf("AllowedTools=%#v, want [Read Write Edit]", got)
	}
	managed, err := (&Hub{Roots: []string{root}}).ListManaged()
	if err != nil {
		t.Fatal(err)
	}
	if len(managed) != 1 || managed[0].Name != "humanizer-zh" {
		t.Fatalf("management listing returned %+v", managed)
	}
	if want := "Read, Write, Edit"; managed[0].AllowedTools != want {
		t.Fatalf("display AllowedTools=%q, want %q", managed[0].AllowedTools, want)
	}
}

// TestDiscoveryFindsSkillsInsideABundleOnce covers the layout the built-in
// skills ship in — a bundle directory holding several skills — and pins the
// depth the scan stops at.
func TestDiscoveryFindsSkillsInsideABundleOnce(t *testing.T) {
	root := t.TempDir()
	writeDiscoverySkill(t, filepath.Join(root, ".system", "caveman", "caveman-commit"), "name: caveman-commit\ndescription: Writes commits", "body")
	writeDiscoverySkill(t, filepath.Join(root, ".system", "caveman", "caveman-review"), "name: caveman-review\ndescription: Reviews changes", "body")
	// One level deeper than a bundle is out of reach, deliberately.
	writeDiscoverySkill(t, filepath.Join(root, ".system", "caveman", "extra", "too-deep"), "name: too-deep\ndescription: Too deep", "body")

	found := scanSkillRoots([]string{filepath.Join(root, ".system")})
	names := make([]string, 0, len(found))
	for _, item := range found {
		names = append(names, item.Name)
	}
	want := map[string]bool{"caveman-commit": true, "caveman-review": true}
	for _, name := range names {
		if !want[name] {
			t.Fatalf("scanner returned %v, want only the bundle's own skills", names)
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatalf("scanner returned %v, missing %v", names, want)
	}
	// The skill's real directory is what the catalog hands the model, bundle
	// directory and all.
	for _, item := range found {
		if filepath.Base(filepath.Dir(item.Dir)) != "caveman" {
			t.Fatalf("skill %q resolved to %q, losing the bundle directory", item.Name, item.Dir)
		}
	}
}

func TestValidateSkillContentRequiresADescription(t *testing.T) {
	if err := ValidateSkillContent([]byte("---\nname: demo\ndescription: Does a thing\n---\n\nbody\n")); err != nil {
		t.Fatalf("a complete skill was refused: %v", err)
	}
	if err := ValidateSkillContent([]byte("---\nname: demo\n---\n\nbody\n")); err == nil {
		t.Fatal("a skill with no description was accepted")
	}
	if err := ValidateSkillContent([]byte("no frontmatter at all")); err == nil {
		t.Fatal("a file that is not a skill was accepted")
	}
}

// TestInstallRefusesASkillWithoutADescription pins the entry gate at the one
// boundary every install path crosses: a directory that discovery would refuse
// to load must not land in a skills directory in the first place, where it
// would sit as a skill the user installed and can never use.
func TestInstallRefusesASkillWithoutADescription(t *testing.T) {
	source := filepath.Join(t.TempDir(), "demo")
	writeDiscoverySkill(t, source, "name: demo", "body")
	dest := t.TempDir()

	err := installSkillDirWithLock("", source, dest, "demo", LockEntry{})
	if err == nil {
		t.Fatal("a skill with no description was installed")
	}
	if !errors.Is(err, ErrMissingDescription) {
		t.Fatalf("install error = %v, want the missing-description refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(dest, "demo")); !os.IsNotExist(statErr) {
		t.Fatal("the refused skill was copied into the destination anyway")
	}
}

// A package's install picker offers what can actually be installed.
func TestDiscoverSkillDirsSkipsSkillsWithoutADescription(t *testing.T) {
	repo := t.TempDir()
	writeDiscoverySkill(t, filepath.Join(repo, "good"), "name: good\ndescription: Installable", "body")
	writeDiscoverySkill(t, filepath.Join(repo, "bare"), "name: bare", "body")

	found, err := discoverSkillDirs(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Name != "good" {
		t.Fatalf("package discovery returned %+v", found)
	}
}

// Creating or updating a skill goes through the same gate: an update that drops
// the description would take the skill out of every listing without saying so.
func TestCreateAndUpdateRefuseASkillWithoutADescription(t *testing.T) {
	svc := &Service{Home: t.TempDir()}
	bare := "---\nname: demo\n---\n\nbody\n"
	if _, err := svc.Create(CreateRequest{Name: "demo", Content: bare}); !errors.Is(err, ErrMissingDescription) {
		t.Fatalf("create error = %v, want the missing-description refusal", err)
	}
	if _, err := svc.Update(UpdateRequest{Name: "demo", Content: bare}); !errors.Is(err, ErrMissingDescription) {
		t.Fatalf("update error = %v, want the missing-description refusal", err)
	}
}
