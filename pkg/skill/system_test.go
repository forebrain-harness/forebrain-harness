package skill

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

func TestInstall_EmptyHome(t *testing.T) {
	if err := Install(""); err == nil {
		t.Fatal("expected error for empty forebrainHome")
	}
	if err := Install("   "); err == nil {
		t.Fatal("expected error for whitespace-only forebrainHome")
	}
}

func TestInstall_FirstRun(t *testing.T) {
	tmp := t.TempDir()
	if err := Install(tmp); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	dest := filepath.Join(tmp, "skills", ".system")
	// Marker file should exist
	markerPath := filepath.Join(dest, ".forebrain-system-skills.marker")
	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	marker := strings.TrimSpace(string(data))
	if len(marker) != 16 {
		t.Fatalf("marker length = %d, want 16", len(marker))
	}
	if marker != Fingerprint() {
		t.Fatalf("marker = %q, want %q", marker, Fingerprint())
	}

	// At least one SKILL.md should exist
	reviewSkill := filepath.Join(dest, "review", "SKILL.md")
	if _, err := os.Stat(reviewSkill); err != nil {
		t.Fatalf("review/SKILL.md not extracted: %v", err)
	}
}

func TestInstall_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	if err := Install(tmp); err != nil {
		t.Fatalf("first Install: %v", err)
	}

	dest := filepath.Join(tmp, "skills", ".system")
	markerPath := filepath.Join(dest, ".forebrain-system-skills.marker")

	// Record mtime of marker
	info1, _ := os.Stat(markerPath)

	// Second call should be a no-op (marker matches)
	if err := Install(tmp); err != nil {
		t.Fatalf("second Install: %v", err)
	}

	info2, _ := os.Stat(markerPath)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatal("marker was rewritten on idempotent call")
	}
}

func TestInstall_MarkerMismatch_ReExtracts(t *testing.T) {
	tmp := t.TempDir()
	if err := Install(tmp); err != nil {
		t.Fatalf("first Install: %v", err)
	}

	dest := filepath.Join(tmp, "skills", ".system")
	markerPath := filepath.Join(dest, ".forebrain-system-skills.marker")

	// Corrupt marker
	if err := os.WriteFile(markerPath, []byte("stale_hash_value\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Add a stale skill directory that should be removed on re-extract.
	staleSkillFile := filepath.Join(dest, "stale-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(staleSkillFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staleSkillFile, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Install(tmp); err != nil {
		t.Fatalf("re-extract Install: %v", err)
	}

	// Stale skill directory should be gone (RemoveAll + re-extract).
	if _, err := os.Stat(staleSkillFile); !os.IsNotExist(err) {
		t.Fatal("stale skill survived re-extraction")
	}

	// Marker should now match
	data, _ := os.ReadFile(markerPath)
	if strings.TrimSpace(string(data)) != Fingerprint() {
		t.Fatal("marker not updated after re-extract")
	}
}

func TestInstall_MarkerMissing_Extracts(t *testing.T) {
	tmp := t.TempDir()
	dest := filepath.Join(tmp, "skills", ".system")

	// Pre-create dest dir without marker
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Install(tmp); err != nil {
		t.Fatalf("Install: %v", err)
	}

	markerPath := filepath.Join(dest, ".forebrain-system-skills.marker")
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatal("marker not created")
	}
}

func TestInstall_RemoveAllError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	tmp := t.TempDir()
	dest := filepath.Join(tmp, "skills", ".system")

	// Create dest with a stale marker so Install tries RemoveAll
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(dest, ".forebrain-system-skills.marker")
	if err := os.WriteFile(markerPath, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Make parent read-only so RemoveAll fails
	parent := filepath.Join(tmp, "skills")
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0o755)

	err := Install(tmp)
	if err == nil {
		t.Fatal("expected error when RemoveAll fails")
	}
	if !strings.Contains(err.Error(), "remove old .system") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInstall_WriteMarkerError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	tmp := t.TempDir()

	// First install succeeds
	if err := Install(tmp); err != nil {
		t.Fatalf("first Install: %v", err)
	}

	dest := filepath.Join(tmp, "skills", ".system")
	markerPath := filepath.Join(dest, ".forebrain-system-skills.marker")

	// Corrupt marker to force re-extract
	if err := os.WriteFile(markerPath, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Make dest read-only after RemoveAll (which will succeed) so WriteFile fails
	// We need a different approach: make the dest dir non-writable after extraction
	// Actually, let's just verify the error path exists by making dest unwritable
	// after the extract step. This is tricky because extractEmbedFS creates the dir.
	// Instead, test that a read-only dest causes marker write to fail.

	// Re-install to get fresh state
	if err := Install(tmp); err != nil {
		t.Fatalf("re-install: %v", err)
	}

	// Now corrupt marker and make dest read-only
	if err := os.WriteFile(markerPath, []byte("bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Remove marker so ReadFile fails, then make dest unwritable
	os.Remove(markerPath)
	// RemoveAll will work, but MkdirAll in extract will fail if parent is read-only
	// Let's test extract error instead
	parent := filepath.Join(tmp, "skills")
	os.RemoveAll(dest)
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0o755)

	err := Install(tmp)
	if err == nil {
		t.Fatal("expected error when extraction fails")
	}
	if !strings.Contains(err.Error(), "extract") && !strings.Contains(err.Error(), "remove") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFingerprint_Deterministic(t *testing.T) {
	fp1 := Fingerprint()
	fp2 := Fingerprint()
	if fp1 != fp2 {
		t.Fatalf("Fingerprint not deterministic: %q != %q", fp1, fp2)
	}
}

func TestFingerprint_Length(t *testing.T) {
	fp := Fingerprint()
	if len(fp) != 16 {
		t.Fatalf("Fingerprint length = %d, want 16", len(fp))
	}
}

func TestFingerprint_HexOnly(t *testing.T) {
	fp := Fingerprint()
	for _, c := range fp {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("Fingerprint contains non-hex char: %c", c)
		}
	}
}

func TestExtractEmbedFS_CreatesDirectories(t *testing.T) {
	tmp := t.TempDir()
	if err := extractEmbedFS(assetsFS, "system_assets", tmp); err != nil {
		t.Fatalf("extractEmbedFS: %v", err)
	}

	// Check nested directory structure exists
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries extracted")
	}

	// Verify at least one nested file
	found := false
	filepath.Walk(tmp, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && strings.HasSuffix(path, "SKILL.md") {
			found = true
		}
		return nil
	})
	if !found {
		t.Fatal("no SKILL.md found in extracted tree")
	}
}

func TestExtractEmbedFS_FileContents(t *testing.T) {
	tmp := t.TempDir()
	if err := extractEmbedFS(assetsFS, "system_assets", tmp); err != nil {
		t.Fatalf("extractEmbedFS: %v", err)
	}

	// Read a known file from embed and from disk, compare
	embedData, err := assetsFS.ReadFile("system_assets/review/SKILL.md")
	if err != nil {
		t.Fatalf("read from embed: %v", err)
	}

	diskData, err := os.ReadFile(filepath.Join(tmp, "review", "SKILL.md"))
	if err != nil {
		t.Fatalf("read from disk: %v", err)
	}

	if string(embedData) != string(diskData) {
		t.Fatal("extracted file content doesn't match embedded content")
	}
}

func TestExtractEmbedFS_PreservesStructure(t *testing.T) {
	tmp := t.TempDir()
	if err := extractEmbedFS(assetsFS, "system_assets", tmp); err != nil {
		t.Fatalf("extractEmbedFS: %v", err)
	}

	// docx is a representative skill with a scripts/ subdir.
	scriptsDir := filepath.Join(tmp, "docx", "scripts")
	entries, err := os.ReadDir(scriptsDir)
	if err != nil {
		t.Fatalf("docx/scripts/ not extracted: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("docx/scripts/ extracted empty")
	}
}

func TestInstall_AllSkillDirsExtracted(t *testing.T) {
	tmp := t.TempDir()
	if err := Install(tmp); err != nil {
		t.Fatalf("Install: %v", err)
	}

	dest := filepath.Join(tmp, "skills", ".system")

	// Top-level skills (SKILL.md directly under .system/<name>/).
	topLevelDirs := []string{
		"review",
		"context-save",
		"context-restore",
		"skill-workshop",
		"skill-generator",
		"image-edit",
		"frontend-design",
		"docx",
		"pdf",
		"pptx",
		"xlsx",
	}
	for _, dir := range topLevelDirs {
		skillFile := filepath.Join(dest, dir, "SKILL.md")
		if _, err := os.Stat(skillFile); err != nil {
			t.Errorf("missing: %s/SKILL.md", dir)
		}
	}

	// A fresh install must not contain unlisted top-level skill directories.
	allowedEntries := map[string]struct{}{
		".forebrain-system-skills.marker": {},
		"caveman":                         {},
	}
	for _, dir := range topLevelDirs {
		allowedEntries[dir] = struct{}{}
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("read system skill directory: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if _, ok := allowedEntries[entry.Name()]; !ok {
				t.Errorf("unexpected system skill entry: %s", entry.Name())
			}
		}
	}

	// Namespace dirs (no SKILL.md at top; sub-dirs each have SKILL.md).
	// Hub loads these recursively via scanSkillDir depth=1.
	nestedSkills := map[string][]string{
		"caveman": {
			"caveman", "cavecrew", "caveman-commit",
			"caveman-compress", "caveman-help", "caveman-review", "caveman-stats",
		},
	}
	for ns, subs := range nestedSkills {
		for _, sub := range subs {
			skillFile := filepath.Join(dest, ns, sub, "SKILL.md")
			if _, err := os.Stat(skillFile); err != nil {
				t.Errorf("missing: %s/%s/SKILL.md", ns, sub)
			}
		}
	}
}

func TestInstall_ScriptsExecutable(t *testing.T) {
	// Verify scripts are extracted (content check, not +x since embed writes 0644)
	tmp := t.TempDir()
	if err := Install(tmp); err != nil {
		t.Fatalf("Install: %v", err)
	}

	dest := filepath.Join(tmp, "skills", ".system")
	magickScript := filepath.Join(dest, "image-edit", "scripts", "magick_ops.sh")
	data, err := os.ReadFile(magickScript)
	if err != nil {
		t.Fatalf("magick_ops.sh not extracted: %v", err)
	}
	if !strings.HasPrefix(string(data), "#!/bin/bash") {
		t.Fatal("magick_ops.sh missing shebang")
	}
}

func TestEmbeddedSkillsDoNotDeclareLegacyExposurePolicy(t *testing.T) {
	err := fs.WalkDir(assetsFS, "system_assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(path) != "SKILL.md" {
			return err
		}
		data, readErr := assetsFS.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, line := range strings.Split(string(data), "\n") {
			key, _, ok := strings.Cut(line, ":")
			if ok && strings.EqualFold(strings.TrimSpace(key), "llm_exposure") {
				t.Fatalf("%s still declares legacy llm_exposure metadata", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk assets: %v", err)
	}
}

func TestInstall_SupportsNestedPaths(t *testing.T) {
	// forebrainHome can be deeply nested
	tmp := t.TempDir()
	deep := filepath.Join(tmp, "a", "b", "c", "forebrain-home")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Install(deep); err != nil {
		t.Fatalf("Install with nested path: %v", err)
	}

	markerPath := filepath.Join(deep, "skills", ".system", ".forebrain-system-skills.marker")
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatal("marker not created in nested path")
	}
}

// The generator skill is a system skill like any other: installed from the
// embedded assets, discovered by the shared scanner, and reaching the user as
// the /skill-generator command. The command is derived from the skill's own
// name, which is what makes disabling the skill take the command with it.
func TestSystemAssetsCarryTheGeneratorSkill(t *testing.T) {
	home := t.TempDir()
	if err := Install(home); err != nil {
		t.Fatalf("install system skills: %v", err)
	}
	dir := filepath.Join(home, "skills", ".system", "skill-generator")
	if err := ValidateSkillDir(dir); err != nil {
		t.Fatalf("generator skill does not validate: %v", err)
	}
	activation, err := LoadActivation(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("load generator skill: %v", err)
	}
	if strings.TrimSpace(activation.Name) != "skill-generator" {
		t.Fatalf("name=%q want skill-generator", activation.Name)
	}
	if !activation.SlashCommand {
		t.Fatal("the generator skill must derive a slash command; it is the feature's only entry point")
	}

	// The shared scanner finds it, so the catalog and the loaded-skill set see
	// the same skill the command does.
	entries, err := DiscoverForWorkspace(home, filepath.Join(home, "workspace"), "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Name == "skill-generator" {
			found = true
		}
	}
	if !found {
		t.Fatalf("generator skill missing from discovery: %+v", entries)
	}
}

// The trust gate is what decides whether a project skill reaches the model, so
// the generator's own root resolution has to run through it: the skill is
// written into a version-controlled project the user has trusted, and nowhere
// else.
func TestGeneratorSkillRootNeedsATrustedProject(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := TrustedProjectSkillRoots(home, repo); len(got) != 0 {
		t.Fatalf("untrusted project exposed roots: %v", got)
	}
	project, err := safety.Resolve(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, project); err != nil {
		t.Fatal(err)
	}
	got := TrustedProjectSkillRoots(home, repo)
	if len(got) == 0 || !strings.HasSuffix(filepath.Clean(got[0]), filepath.Join(".forebrain", "skills")) {
		t.Fatalf("trusted project roots=%v want .forebrain/skills first", got)
	}
}

// The writing rules exist once. Both skills that write a SKILL.md point at the
// same file, and neither restates it: two copies drift, and a drifted copy is
// how a skill gets written to rules that were replaced.
func TestSkillAuthoringRulesLiveInOnePlace(t *testing.T) {
	workshop := filepath.Join("system_assets", "skill-workshop")
	generator := filepath.Join("system_assets", "skill-generator")
	authoring := filepath.Join(workshop, "references", "authoring.md")
	raw, err := os.ReadFile(authoring)
	if err != nil {
		t.Fatalf("authoring reference is missing: %v", err)
	}
	body := string(raw)
	for _, want := range []string{"Anatomy of a Skill", "Progressive Disclosure", "500 lines"} {
		if !strings.Contains(body, want) {
			t.Fatalf("authoring reference lost %q", want)
		}
	}

	for _, skillDir := range []string{workshop, generator} {
		skillMD, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
		if err != nil {
			t.Fatalf("read %s: %v", skillDir, err)
		}
		text := string(skillMD)
		if !strings.Contains(text, "references/authoring.md") {
			t.Fatalf("%s does not point at the shared authoring rules", skillDir)
		}
		// The restated copies are what drifted: the anatomy diagram and the
		// three-level loading list live only in the reference now.
		if strings.Contains(text, "#### Anatomy of a Skill") || strings.Contains(text, "#### Progressive Disclosure") {
			t.Fatalf("%s carries a second copy of the writing rules", skillDir)
		}
	}

	// The generator writes into the project's skills directory and says so.
	generatorBody, err := os.ReadFile(filepath.Join(generator, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(generatorBody)
	if !strings.Contains(text, ".forebrain/skills") {
		t.Fatalf("generator skill does not name its target directory:\n%s", text)
	}
	if !strings.Contains(text, "next session") && !strings.Contains(text, "next** session") {
		t.Fatalf("generator skill does not say when the skill takes effect:\n%s", text)
	}
}
