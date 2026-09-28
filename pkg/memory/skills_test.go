package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func writeAuthoredSkill(t *testing.T, root Root, dirName, frontmatter, body string) string {
	t.Helper()
	dir := filepath.Join(root.AuthoredSkillsRoot(), dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir authored skill: %v", err)
	}
	content := "---\n" + frontmatter + "\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write authored skill: %v", err)
	}
	return dir
}

func testRoot(t *testing.T) Root {
	t.Helper()
	roots, err := ResolveRootsForAgent(t.TempDir())
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	return roots.Scope(Scope{Kind: ScopeProject, Key: "proj"})
}

func TestListAuthoredSkillsReturnsNothingWhenDirectoryIsAbsent(t *testing.T) {
	items, err := ListAuthoredSkills(testRoot(t))
	if err != nil || len(items) != 0 {
		t.Fatalf("items=%v err=%v", items, err)
	}
}

func TestListAuthoredSkillsReadsFrontmatterAndSortsByName(t *testing.T) {
	root := testRoot(t)
	writeAuthoredSkill(t, root, "zebra-dir", "name: zebra\ndescription: Last alphabetically", "Zebra body")
	writeAuthoredSkill(t, root, "alpha-dir", "name: alpha\ndescription: First alphabetically", "Alpha body")

	items, err := ListAuthoredSkills(root)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items=%+v", items)
	}
	if items[0].Name != "alpha" || items[1].Name != "zebra" {
		t.Fatalf("unsorted: %s, %s", items[0].Name, items[1].Name)
	}
	if items[0].Description != "First alphabetically" {
		t.Fatalf("description=%q", items[0].Description)
	}
	if items[0].SkillFile != filepath.Join(items[0].Dir, "SKILL.md") {
		t.Fatalf("skill file=%q dir=%q", items[0].SkillFile, items[0].Dir)
	}
	if items[0].Digest == "" || items[0].Digest == items[1].Digest {
		t.Fatalf("digests %q %q", items[0].Digest, items[1].Digest)
	}
}

// A model writes these files, so one unusable proposal must not hide the gateway.
func TestListAuthoredSkillsSkipsUnusableDirectories(t *testing.T) {
	root := testRoot(t)
	writeAuthoredSkill(t, root, "good", "name: good\ndescription: Usable", "Body")
	if err := os.MkdirAll(filepath.Join(root.AuthoredSkillsRoot(), "no-skill-file"), 0o755); err != nil {
		t.Fatal(err)
	}
	badDir := filepath.Join(root.AuthoredSkillsRoot(), "unparseable")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "SKILL.md"), []byte("no frontmatter here"), 0o644); err != nil {
		t.Fatal(err)
	}

	items, err := ListAuthoredSkills(root)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 || items[0].Name != "good" {
		t.Fatalf("items=%+v", items)
	}
}

func TestListAuthoredSkillsFallsBackToDirectoryName(t *testing.T) {
	root := testRoot(t)
	writeAuthoredSkill(t, root, "folder-name", "description: No name field", "Body")

	items, err := ListAuthoredSkills(root)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if items[0].Name != "folder-name" {
		t.Fatalf("name=%q", items[0].Name)
	}
}

func TestDirectoryDigestIgnoresTimestampsAndTracksContent(t *testing.T) {
	root := testRoot(t)
	dir := writeAuthoredSkill(t, root, "sample", "name: sample\ndescription: Sample", "Body")
	first, err := DirectoryDigest(dir)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	// Rewriting identical bytes changes mtime but must not change the digest.
	writeAuthoredSkill(t, root, "sample", "name: sample\ndescription: Sample", "Body")
	same, err := DirectoryDigest(dir)
	if err != nil || same != first {
		t.Fatalf("digest changed on identical rewrite: %q -> %q (%v)", first, same, err)
	}
	writeAuthoredSkill(t, root, "sample", "name: sample\ndescription: Sample", "Different body")
	changed, err := DirectoryDigest(dir)
	if err != nil || changed == first {
		t.Fatalf("digest did not track content: %q -> %q (%v)", first, changed, err)
	}
	// A supporting file is part of the skill, so it counts too.
	if err := os.WriteFile(filepath.Join(dir, "reference.md"), []byte("extra"), 0o644); err != nil {
		t.Fatal(err)
	}
	withExtra, err := DirectoryDigest(dir)
	if err != nil || withExtra == changed {
		t.Fatalf("digest ignored a supporting file: %q -> %q (%v)", changed, withExtra, err)
	}
}

// Promotion copies through symlinks, so a linked directory must be refused
// outright rather than silently carrying its target's bytes into a skill root
// the model can read.
func TestDirectoryDigestRejectsSymlinks(t *testing.T) {
	root := testRoot(t)
	dir := writeAuthoredSkill(t, root, "linked", "name: linked\ndescription: Linked", "Body")
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "leak.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := DirectoryDigest(dir); err == nil {
		t.Fatal("expected a symlinked entry to be rejected")
	}
	items, err := ListAuthoredSkills(root)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("symlinked skill offered for promotion: %+v", items)
	}
}
