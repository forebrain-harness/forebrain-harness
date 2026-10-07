package skill

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

// The one table: what the runtime walks, what every listing classifies by,
// and the order both list layers in. A directory added to projectSkillDirs or
// userSkillDirs shows up here, which is the reminder to keep this expectation
// in step with it.
func TestSkillLayersAreTheOneOrderedTable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	ws := t.TempDir()
	repo := CanonicalSkillPath(t.TempDir())
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	want := []Root{
		{Path: filepath.Join(repo, ".forebrain", "skills"), Origin: OriginProject},
		{Path: filepath.Join(repo, ".agents", "skills"), Origin: OriginProject},
		{Path: filepath.Join(repo, ".claude", "skills"), Origin: OriginProject},
		{Path: filepath.Join(repo, ".codex", "skills"), Origin: OriginProject},
		{Path: filepath.Join(ws, "skills"), Origin: OriginAgent},
		{Path: filepath.Join(home, "skills"), Origin: OriginShared},
		{Path: filepath.Join(userHome, ".agents", "skills"), Origin: OriginCrossTool},
		{Path: filepath.Join(userHome, ".claude", "skills"), Origin: OriginCrossTool},
		{Path: filepath.Join(userHome, ".codex", "skills"), Origin: OriginCrossTool},
		{Path: filepath.Join(home, "skills", systemSkillsDirName), Origin: OriginBuiltin},
	}
	got := skillLayers(home, ws, ProjectSkillRootsForDir(repo))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("layers =\n%+v\nwant\n%+v", got, want)
	}
	if roots := agentSkillRoots(home, ws, ProjectSkillRootsForDir(repo)); !reflect.DeepEqual(roots, RootPaths(want)) {
		t.Fatalf("agentSkillRoots = %#v, want %#v", roots, RootPaths(want))
	}
}

// writeOriginLayout trusts repo and seeds one uniquely named skill per layer,
// returning each canonical skill directory keyed by the layer it must be
// classified as.
func writeOriginLayout(t *testing.T, home, ws, repo, userHome string) map[string]Origin {
	t.Helper()
	t.Chdir(repo)
	if err := safety.MarkTrusted(home, safety.Project{Root: repo, VersionControlled: true}); err != nil {
		t.Fatal(err)
	}
	want := map[string]Origin{}
	put := func(origin Origin, dir string) {
		want[writeSkill(t, dir, filepath.Base(dir))] = origin
	}
	for _, dir := range []string{".forebrain", ".agents", ".claude", ".codex"} {
		put(OriginProject, filepath.Join(repo, dir, "skills", "p-"+dir[1:]))
	}
	put(OriginAgent, filepath.Join(ws, "skills", "a-skill"))
	put(OriginShared, filepath.Join(home, "skills", "s-skill"))
	put(OriginCrossTool, filepath.Join(userHome, ".agents", "skills", "c-agents"))
	put(OriginCrossTool, filepath.Join(userHome, ".claude", "skills", "c-claude"))
	put(OriginCrossTool, filepath.Join(userHome, ".codex", "skills", "c-codex"))
	put(OriginBuiltin, filepath.Join(home, "skills", systemSkillsDirName, "b-skill"))
	return want
}

// A skill's layer is the layer of the root it was found under — never a
// second rule about path prefixes — so a directory listing cannot disagree
// with where the runtime loaded a skill from.
func TestDiscoverClassifiesByTheRootFound(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	home := t.TempDir()
	ws := t.TempDir()
	repo := t.TempDir()
	want := writeOriginLayout(t, home, ws, repo, userHome)

	entries, err := DiscoverForWorkspace(home, ws, repo)
	if err != nil {
		t.Fatalf("DiscoverForWorkspace: %v", err)
	}
	for _, entry := range entries {
		if entry.Origin == "" {
			t.Fatalf("%s: origin is empty", entry.Path)
		}
		origin, ok := want[entry.Path]
		if !ok {
			continue
		}
		if entry.Origin != origin {
			t.Fatalf("%s: origin = %q, want %q", entry.Path, entry.Origin, origin)
		}
		delete(want, entry.Path)
	}
	if len(want) != 0 {
		t.Fatalf("skills not discovered: %v", want)
	}
}

// The scanner never descends into a dot-directory, so the built-in root is
// reached only as a root of its own and a Codex-style ~/.codex/skills/.system
// stays out entirely.
func TestScannerNeverDescendsIntoDotDirectories(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	home := t.TempDir()
	ws := t.TempDir()
	writeSkill(t, filepath.Join(userHome, ".codex", "skills", systemSkillsDirName, "imagegen"), "imagegen")
	writeSkill(t, filepath.Join(userHome, ".claude", "skills", ".hidden", "x"), "x")

	entries, err := DiscoverForWorkspace(home, ws, "")
	if err != nil {
		t.Fatalf("DiscoverForWorkspace: %v", err)
	}
	for _, entry := range entries {
		if entry.Name == "imagegen" || entry.Name == "x" {
			t.Fatalf("scanner descended into a dot-directory: %+v", entry)
		}
	}
}

// A skill the user installed always outranks the built-in one of the same
// name, because the built-in directory is scanned as the lowest-priority root
// instead of being descended into from the shared root above it.
func TestUserInstalledSkillOutranksBuiltin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	ws := t.TempDir()
	sharedDir := writeSkill(t, filepath.Join(home, "skills", "frontend-design"), "frontend-design")
	builtinDir := writeSkill(t, filepath.Join(home, "skills", systemSkillsDirName, "frontend-design"), "frontend-design")

	entries, err := DiscoverForWorkspace(home, ws, "")
	if err != nil {
		t.Fatalf("DiscoverForWorkspace: %v", err)
	}
	var shared, builtin *Entry
	for i := range entries {
		switch entries[i].Path {
		case sharedDir:
			shared = &entries[i]
		case builtinDir:
			builtin = &entries[i]
		}
	}
	if shared == nil || builtin == nil {
		t.Fatalf("missing copies: shared=%v builtin=%v", shared, builtin)
	}
	if len(shared.Shadows) != 1 || shared.Shadows[0] != builtinDir {
		t.Fatalf("shared Shadows=%v, want [%s]", shared.Shadows, builtinDir)
	}
	if len(shared.ShadowedBy) != 0 {
		t.Fatalf("the loaded copy is not shadowed: %+v", shared)
	}
	if len(builtin.ShadowedBy) != 1 || builtin.ShadowedBy[0] != sharedDir {
		t.Fatalf("builtin ShadowedBy=%v, want [%s]", builtin.ShadowedBy, sharedDir)
	}
	if len(builtin.Shadows) != 0 {
		t.Fatalf("the shadowed copy shadows nothing: %+v", builtin)
	}

	discovered, err := (Loader{Roots: AgentSkillRoots(home, ws, safety.ProjectContext{}), StateRoot: ws}).Discover()
	if err != nil {
		t.Fatalf("Loader.Discover: %v", err)
	}
	loaded := false
	for _, item := range discovered {
		if item.Name != "frontend-design" {
			continue
		}
		if got, want := CanonicalSkillPath(item.SkillFile), CanonicalSkillPath(filepath.Join(home, "skills", "frontend-design", "SKILL.md")); got != want {
			t.Fatalf("runtime loaded %s, want %s", got, want)
		}
		loaded = true
	}
	if !loaded {
		t.Fatal("frontend-design not discovered by the runtime")
	}
}

// The shadow annotation names the copy the runtime loads: the first root that
// offers the name, not the last one. The listing says so, and the loader
// agrees.
func TestShadowAnnotationNamesTheLoadedCopy(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	home := t.TempDir()
	ws := t.TempDir()
	repo := t.TempDir()
	t.Chdir(repo)
	if err := safety.MarkTrusted(home, safety.Project{Root: repo, VersionControlled: true}); err != nil {
		t.Fatal(err)
	}
	projectDir := writeSkill(t, filepath.Join(repo, ".claude", "skills", "improve"), "improve")
	crossToolDir := writeSkill(t, filepath.Join(userHome, ".agents", "skills", "improve"), "improve")

	entries, err := DiscoverForWorkspace(home, ws, repo)
	if err != nil {
		t.Fatalf("DiscoverForWorkspace: %v", err)
	}
	var project, crossTool *Entry
	for i := range entries {
		switch entries[i].Path {
		case projectDir:
			project = &entries[i]
		case crossToolDir:
			crossTool = &entries[i]
		}
	}
	if project == nil || crossTool == nil {
		t.Fatalf("missing copies: project=%v crossTool=%v", project, crossTool)
	}
	if len(project.Shadows) != 1 || project.Shadows[0] != crossToolDir {
		t.Fatalf("project Shadows=%v, want [%s]", project.Shadows, crossToolDir)
	}
	if len(project.ShadowedBy) != 0 {
		t.Fatalf("the loaded copy is not shadowed: %+v", project)
	}
	if len(crossTool.ShadowedBy) != 1 || crossTool.ShadowedBy[0] != projectDir {
		t.Fatalf("cross-tool ShadowedBy=%v, want [%s]", crossTool.ShadowedBy, projectDir)
	}
	if len(crossTool.Shadows) != 0 {
		t.Fatalf("the shadowed copy shadows nothing: %+v", crossTool)
	}

	discovered, err := (Loader{Roots: agentSkillRoots(home, ws, TrustedProjectSkillRoots(home, repo)), StateRoot: ws}).Discover()
	if err != nil {
		t.Fatalf("Loader.Discover: %v", err)
	}
	loaded := false
	for _, item := range discovered {
		if item.Name != "improve" {
			continue
		}
		if got, want := CanonicalSkillPath(item.SkillFile), CanonicalSkillPath(filepath.Join(projectDir, "SKILL.md")); got != want {
			t.Fatalf("runtime loaded %s, want %s", got, want)
		}
		loaded = true
	}
	if !loaded {
		t.Fatal("improve not discovered by the runtime")
	}
}

// The management view and the picker classify through the same table, so the
// same directory never carries two different layers in the two listings.
func TestHubListsTheSameLayersAsDiscovery(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	home := t.TempDir()
	ws := t.TempDir()
	repo := t.TempDir()
	writeOriginLayout(t, home, ws, repo, userHome)

	entries, err := DiscoverForWorkspace(home, ws, repo)
	if err != nil {
		t.Fatalf("DiscoverForWorkspace: %v", err)
	}
	byPath := make(map[string]Origin, len(entries))
	for _, entry := range entries {
		byPath[entry.Path] = entry.Origin
	}
	managed, err := NewForWorkspace(home, ws, repo).ListManaged()
	if err != nil {
		t.Fatalf("ListManaged: %v", err)
	}
	if len(managed) != len(entries) {
		t.Fatalf("managed listing holds %d skills, discovery %d", len(managed), len(entries))
	}
	for _, item := range managed {
		key := CanonicalSkillPath(item.RootPath)
		origin, ok := byPath[key]
		if !ok {
			t.Fatalf("managed skill %s (%s) is absent from discovery", item.Name, item.RootPath)
		}
		if item.Origin != origin {
			t.Fatalf("%s: managed origin = %q, discovery says %q", item.RootPath, item.Origin, origin)
		}
	}
}
