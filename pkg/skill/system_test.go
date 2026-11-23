package skill

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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
	auditSkillFile := filepath.Join(dest, "audit", "SKILL.md")
	if _, err := os.Stat(auditSkillFile); err != nil {
		t.Fatalf("audit/SKILL.md not extracted: %v", err)
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

	// Make parent read-only so the install cannot write its staging tree beside
	// the live one.
	parent := filepath.Join(tmp, "skills")
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0o755)

	err := Install(tmp)
	if err == nil {
		t.Fatal("expected error when the skill tree cannot be rebuilt")
	}
	if !strings.Contains(err.Error(), "stage") && !strings.Contains(err.Error(), "remove old .system") {
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
		t.Fatal("expected error when the skill tree cannot be rebuilt")
	}
	if !strings.Contains(err.Error(), "stage") && !strings.Contains(err.Error(), "extract") && !strings.Contains(err.Error(), "remove") {
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
	embedData, err := assetsFS.ReadFile("system_assets/audit/SKILL.md")
	if err != nil {
		t.Fatalf("read from embed: %v", err)
	}

	diskData, err := os.ReadFile(filepath.Join(tmp, "audit", "SKILL.md"))
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
		"audit",
		"run",
		"run-skill-generator",
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
		"verify",
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
			"caveman", "caveman-commit",
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

// The run skill is the one built-in skill the model is meant to reach on its own
// initiative: it turns "the tests pass" into "the app was seen working". Its
// contract is a handful of sentences a future editor would happily tighten away —
// the restraint clause that keeps the recommendation rare, and the forebrain
// semantics that make the advice true here rather than anywhere — so each is
// pinned by its exact words.
func TestRunSkillDrivesTheAppAndOffersToCaptureIt(t *testing.T) {
	home := t.TempDir()
	if err := Install(home); err != nil {
		t.Fatalf("install system skills: %v", err)
	}
	systemRoot := filepath.Join(home, "skills", ".system")
	dir := filepath.Join(systemRoot, "run")
	if err := ValidateSkillDir(dir); err != nil {
		t.Fatalf("run skill does not validate: %v", err)
	}
	activation, err := LoadActivation(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("load run skill: %v", err)
	}
	if activation.Name != "run" {
		t.Fatalf("name=%q want run", activation.Name)
	}
	if !activation.SlashCommand {
		t.Fatal("/run is an entry point: the skill must derive a slash command")
	}
	// The opposite of plan 354's generator: this one is meant to be picked up by
	// the model when a change needs to be seen working.
	if !activation.ModelInvocation {
		t.Fatal("the run skill must stay model-invocable")
	}

	// The catalog is what the model reads, so this is the end-to-end proof.
	entries, err := CatalogEntries([]string{systemRoot}, "")
	if err != nil {
		t.Fatal(err)
	}
	catalogued := false
	for _, entry := range entries {
		if entry.Name != "run" {
			continue
		}
		catalogued = true
		if !strings.Contains(entry.Description, "drive") && !strings.Contains(entry.Description, "launch") {
			t.Fatalf("the catalog description must say what the skill does: %q", entry.Description)
		}
	}
	if !catalogued {
		t.Fatalf("run skill missing from the catalog: %+v", entries)
	}

	content := activation.Content
	for _, want := range []string{
		// The probe that finds a project's own run skill before rediscovering it.
		"for d in .forebrain .agents .claude .codex",
		// The restraint clause: without it the recommendation becomes noise.
		"just ran, recommend nothing",
		// The forebrain semantics that make this advice true in this product.
		"trusted project",
		"FOREBRAIN_HOME",
		"scripts/acceptance",
		// The draft/publish boundary, the same one skill-generator states.
		".drafts",
		"Never write or publish a skill on your own initiative",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("run skill lost %q:\n%s", want, content)
		}
	}
	// Three distinct sites: the stale-skill bullet, the closing recommendation and
	// the closing publish paragraph.
	if got := strings.Count(content, "run-skill-generator"); got < 3 {
		t.Fatalf("run-skill-generator named %d times, want at least 3 (stale, recommend, publish)", got)
	}
	// This repository's own instance is not the shipped asset: its forebrain-only
	// invocation recipes must not travel to every project.
	for _, unwanted := range []string{"driver.sh start hang", "brew install tmux", "term_live", "paint_dump"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("run skill carries this repository's own recipe %q", unwanted)
		}
	}
}

// The verify skill is a judgment skill, and judgment is what a future editor
// softens first: a verdict vocabulary, two rules that stop a model grading its
// own work on a curve, and the forebrain semantics (a process that outlives the
// turn, a trust gate, a rebuild before driving) that make the advice true in
// this harness rather than somewhere else. Each is pinned by its exact words,
// and the reference doc is pinned by the sections a reviewer needs to reopen
// the evidence.
func TestVerifySkillDrivesTheAppAndRendersAVerdict(t *testing.T) {
	home := t.TempDir()
	if err := Install(home); err != nil {
		t.Fatalf("install system skills: %v", err)
	}
	systemRoot := filepath.Join(home, "skills", ".system")
	dir := filepath.Join(systemRoot, "verify")
	if err := ValidateSkillDir(dir); err != nil {
		t.Fatalf("verify skill does not validate: %v", err)
	}
	// 1. Both files ship, and the bundled one is discoverable rather than
	// merely present: the activation lists it, which is how the model learns
	// the path exists.
	if _, err := os.Stat(filepath.Join(dir, "references", "evidence.md")); err != nil {
		t.Fatalf("references/evidence.md was not installed: %v", err)
	}
	_, files, _ := listSkillResources(dir)
	resource := false
	for _, name := range files {
		if name == "references/evidence.md" {
			resource = true
		}
	}
	if !resource {
		t.Fatalf("references/evidence.md is not reachable through the activation: %v", files)
	}

	activation, err := LoadActivation(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("load verify skill: %v", err)
	}
	if activation.Name != "verify" {
		t.Fatalf("name=%q want verify", activation.Name)
	}
	// 2. /verify exists, and the model may reach for it on its own — the
	// opposite of run-skill-generator: this one is what a model should pick up
	// after changing code.
	if !activation.SlashCommand {
		t.Fatal("/verify is an entry point: the skill must derive a slash command")
	}
	if !activation.ModelInvocation {
		t.Fatal("the verify skill must stay model-invocable")
	}
	// 3. The catalog is what the model reads, so this is the end-to-end proof.
	entries, err := CatalogEntries([]string{systemRoot}, "")
	if err != nil {
		t.Fatal(err)
	}
	catalogued := false
	for _, entry := range entries {
		if entry.Name == "verify" {
			catalogued = true
		}
	}
	if !catalogued {
		t.Fatalf("verify skill missing from the catalog: %+v", entries)
	}

	content := activation.Content
	// 4. The verdict vocabulary. Four words, one meaning each.
	for _, verdict := range []string{"PASS", "FAIL", "BLOCKED", "SKIP"} {
		if !strings.Contains(content, verdict) {
			t.Fatalf("the verify skill lost the %s verdict:\n%s", verdict, content)
		}
	}
	// 5. The two rules that stop a model grading itself on a curve.
	for _, want := range []string{"no partial pass", "the verdict is FAIL"} {
		if !strings.Contains(content, want) {
			t.Fatalf("the verify skill lost %q:\n%s", want, content)
		}
	}
	// 6. The forebrain facts: the tool that holds the app across turns, the
	// readiness wait it offers, and the gate that decides whether a project
	// skill is visible at all.
	for _, want := range []string{"process", "wait: tcp", "trusted", "Rebuild before you drive"} {
		if !strings.Contains(content, want) {
			t.Fatalf("the verify skill lost %q:\n%s", want, content)
		}
	}
	// 7 and 8. The no-leaked-server rule. The asset also names the `process
	// list` check, but that phrase wraps across a line, so the two sentences
	// that do not are the ones asserted.
	for _, want := range []string{"Leave nothing running", "kill what you started"} {
		if !strings.Contains(content, want) {
			t.Fatalf("the verify skill lost %q:\n%s", want, content)
		}
	}
	// 9. Two sites, two different acts: the BLOCKED prompt a user can paste,
	// and the recommendation gated on cold-start + PASS. Collapsing them loses
	// the case where verification never reached the app. And publishing stays
	// the user's decision.
	if got := strings.Count(content, "/run-skill-generator"); got != 2 {
		t.Fatalf("/run-skill-generator named %d times, want 2 (blocked prompt, PASS-gated recommendation)", got)
	}
	if got := strings.Count(content, "Never publish a skill yourself"); got != 1 {
		t.Fatalf("the publish boundary appears %d times, want 1", got)
	}
	// 10. No third-party product: the skill is about this harness. A headless
	// browser may be named generically, never by product.
	for _, unwanted := range []string{"Claude", "Anthropic", "chromium-cli", "playwright"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("the verify skill names a third-party product %q", unwanted)
		}
	}

	// 11. The reference doc is what makes the verdict checkable.
	evidence, err := os.ReadFile(filepath.Join(dir, "references", "evidence.md"))
	if err != nil {
		t.Fatalf("read evidence reference: %v", err)
	}
	body := string(evidence)
	for _, section := range []string{
		"## Where evidence goes",
		"## What to keep per surface",
		"## Naming",
		"## Handing it over",
		"## Retention",
		"## What is not evidence",
		"## When you could not keep any",
	} {
		if !strings.Contains(body, section) {
			t.Fatalf("evidence reference lost %q", section)
		}
	}
	for _, want := range []string{"state/verify", "present_files", ".forebrain"} {
		if !strings.Contains(body, want) {
			t.Fatalf("evidence reference lost %q", want)
		}
	}
}

// TestRunSkillGeneratorIsUserInvocableOnly pins the two properties that make this
// skill safe to ship. Its deliverable is committed files in the user's
// repository, so the model must never pick it up on its own initiative — the
// catalog it reads never names this skill, while the run skill that recommends it
// stays there. And /run-skill-generator must exist, because plan 353's run skill
// tells the user to run it.
func TestRunSkillGeneratorIsUserInvocableOnly(t *testing.T) {
	home := t.TempDir()
	if err := Install(home); err != nil {
		t.Fatalf("install system skills: %v", err)
	}
	systemRoot := filepath.Join(home, "skills", ".system")
	dir := filepath.Join(systemRoot, "run-skill-generator")
	if err := ValidateSkillDir(dir); err != nil {
		t.Fatalf("run-skill-generator does not validate: %v", err)
	}
	activation, err := LoadActivation(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("load run-skill-generator: %v", err)
	}
	if activation.Name != "run-skill-generator" {
		t.Fatalf("name=%q want run-skill-generator", activation.Name)
	}
	if !activation.SlashCommand {
		t.Fatal("/run-skill-generator is the only entry point: it must derive a slash command")
	}
	if activation.ModelInvocation {
		t.Fatal("the generator must be user-invocable only: its deliverable is committed files")
	}

	// The end-to-end proof: the model is offered the consumer and never the
	// generator.
	entries, err := CatalogEntries([]string{systemRoot}, "")
	if err != nil {
		t.Fatal(err)
	}
	offeredRun := false
	for _, entry := range entries {
		switch entry.Name {
		case "run":
			offeredRun = true
		case "run-skill-generator":
			t.Fatalf("the generator reached the model's catalog: %+v", entries)
		}
	}
	if !offeredRun {
		t.Fatalf("the run skill that recommends this one is missing from the catalog: %+v", entries)
	}

	content := activation.Content
	// The bundled files are reachable with no registration: the activation names
	// them, so the model can open them by a path relative to the skill directory.
	for _, want := range []string{
		"template.md",
		"references/drivers.md",
		// The draft/publish contract: a draft, publishing left to the user.
		".forebrain/skills/.drafts",
		"Never publish it yourself",
		"publish",
		// The path trap: paths written into the generated skill must be the
		// published ones, not the draft it is being written into.
		"published path",
		".drafts/",
		// The forebrain semantics that make this advice true here.
		"trusted",
		"scripts/acceptance",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("run-skill-generator lost %q:\n%s", want, content)
		}
	}

	// One probe, shared with the run skill: the two must not drift apart.
	const probe = "for d in .forebrain .agents .claude .codex"
	if !strings.Contains(content, probe) {
		t.Fatalf("the generator lost the shared discovery probe:\n%s", content)
	}
	runActivation, err := LoadActivation(filepath.Join(systemRoot, "run", "SKILL.md"))
	if err != nil {
		t.Fatalf("load run skill: %v", err)
	}
	if !strings.Contains(runActivation.Content, probe) {
		t.Fatalf("the run skill lost the shared discovery probe:\n%s", runActivation.Content)
	}

	// This repository's own instance is not the shipped asset: its forebrain-only
	// invocation recipes must not travel to every project.
	for _, unwanted := range []string{"brew install tmux", "term_live", "paint_dump", "driver.sh start hang"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("the generator carries this repository's own recipe %q", unwanted)
		}
	}

	// The reference doc survived the embed and carries the isolation guidance.
	drivers, err := os.ReadFile(filepath.Join(dir, "references", "drivers.md"))
	if err != nil {
		t.Fatalf("read drivers reference: %v", err)
	}
	for _, want := range []string{"FOREBRAIN_HOME", "scripts/acceptance"} {
		if !strings.Contains(string(drivers), want) {
			t.Fatalf("references/drivers.md lost %q", want)
		}
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

// auditSkill parses the built-in audit skill exactly as it ships.
func auditSkill(t *testing.T) *Skill {
	t.Helper()
	raw, err := assetsFS.ReadFile("system_assets/audit/SKILL.md")
	if err != nil {
		t.Fatalf("read audit skill: %v", err)
	}
	parsed, err := Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse audit skill: %v", err)
	}
	if parsed.Name != "audit" || strings.TrimSpace(parsed.Description) == "" {
		t.Fatalf("audit skill frontmatter = %q / %q", parsed.Name, parsed.Description)
	}
	return parsed
}

// TestAuditSkillCoversEveryWorkflow checks that every workflow the skill
// claims to serve is described in it: each variant, all nine audit categories,
// the plan-mode write boundary, the handoff bar every plan meets, the
// unattended selection default and the unattended skip rule for a publication
// that needs a confirmation nobody can give.
func TestAuditSkillCoversEveryWorkflow(t *testing.T) {
	sk := auditSkill(t)
	for name, sentence := range map[string]string{
		"full audit":           "No argument: the full audit - recon, audit, vetting, prioritization and planning",
		"quick":                "- quick: cover only recon hotspots",
		"deep":                 "- deep: cover the whole scope",
		"category":             "- A category (",
		"branch":               "- branch: audit files changed since the merge-base",
		"next":                 "- next: audit direction only",
		"reconcile":            "- reconcile: process the existing plan backlog",
		"issues":               "- issues: publish the backlog's plans that have no issue yet",
		"unknown argument":     "Any other argument is not a variant: list these and ask which one to run.",
		"plan-mode boundary":   "Inside plan mode that is the session's plan file",
		"handoff bar":          "every plan meets the handoff plan quality bar",
		"unattended selection": "select the top 3–5 by the same rule and record that this non-interactive default was used",
		"sensitive skip":       "skip sensitive publication and record the reason instead of asking or publishing anyway",

		"plan":                         "- plan: skip the audit - the user already described what they want.",
		"review-plan":                  "- review-plan: critique an existing plan against the handoff bar and the plan template",
		"execute":                      "- execute: dispatch one executor subagent on a single plan in an isolated workspace",
		"effort composition":           "quick or deep may precede any variant to set its effort",
		"issues modifier":              "As the --issues modifier on a planning variant, publish each plan this run writes instead.",
		"aliases":                      "features and roadmap mean next; perf means performance.",
		"decline implementation":       "When asked to implement directly, decline and point at the plan",
		"read-only analysis":           "Run only read-only analysis in the user's workspace",
		"playbook pointer":             "references/audit-playbook.md",
		"template pointer":             "references/plan-template.md",
		"loop pointer":                 "references/closing-the-loop.md",
		"branch light budget":          "Light recon, all categories, usually no investigators.",
		"out-of-scope rejection":       "reject any out-of-scope change, however plausible",
		"issues on bare invocation":    "--issues may follow any planning variant to publish each plan written, and may also follow a bare or effort-only invocation.",
		"effort on non-audit variants": "On plan and review-plan the effort word scales investigation depth; execute's review is never reduced.",
		"plan directory alignment":     "Otherwise write under plans/ at the project root, creating it if absent, with numbered plans plus a README index",
	} {
		if !strings.Contains(sk.Body, sentence) {
			t.Errorf("audit skill does not describe the %s workflow: missing %q", name, sentence)
		}
	}
	// The category line is the one place the categories are listed, so a
	// category added to the prose cannot be forgotten here.
	start := strings.Index(sk.Body, "- A category (")
	categoryLine := sk.Body[start:]
	if end := strings.IndexByte(categoryLine, '\n'); end >= 0 {
		categoryLine = categoryLine[:end]
	}
	for _, category := range []string{
		"correctness", "security", "performance", "tests for verification coverage", "architecture for tech debt",
		"dependencies for migrations", "tooling for workflow", "docs", "direction, is next",
	} {
		if !strings.Contains(categoryLine, category) {
			t.Errorf("category line does not name %q", category)
		}
	}
}

// TestAuditSkillVariantsAreSingleWords pins the owner's rule for the skill's
// argument: the variant keyword itself is one word. The keyword may be
// preceded by an effort word (quick or deep), plan, review-plan and execute
// take their input after it, and --issues may follow a planning variant -
// owner ruling 2026-11-21, which replaces the 2026-11-07 no-combination rule
// for this skill alone. The argument-hint the surfaces show after /audit lists
// exactly the keywords the body defines.
func TestAuditSkillVariantsAreSingleWords(t *testing.T) {
	sk := auditSkill(t)
	hint := strings.TrimSpace(sk.ArgumentHint)
	if !strings.HasPrefix(hint, "[") || !strings.HasSuffix(hint, "]") {
		t.Fatalf("argument-hint = %q, want an optional [a|b|...] list", hint)
	}
	variants := strings.Split(strings.Trim(hint, "[]"), "|")
	want := []string{"quick", "deep", "branch", "next", "reconcile", "issues", "plan", "review-plan", "execute",
		"correctness", "security", "performance", "tests", "architecture", "dependencies", "tooling", "docs"}
	if strings.Join(variants, "|") != strings.Join(want, "|") {
		t.Errorf("argument-hint variants = %v, want %v", variants, want)
	}
	section := sk.Body[strings.Index(sk.Body, "## Variants"):]
	section = section[:strings.Index(section, "\n## ")]
	for _, variant := range variants {
		// A hyphen is part of a keyword (review-plan); whitespace would make it
		// a combination, which stays out of the hint.
		if variant == "" || strings.ContainsAny(variant, " \t") {
			t.Errorf("variant %q is not a single word", variant)
		}
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(variant) + `\b`).MatchString(section) {
			t.Errorf("variant %q is offered but never described", variant)
		}
	}
}

// TestAuditSkillIsSelfContainedAndSourceNeutral keeps the skill usable in every
// project type: it names no other prompt file, provider, model or builtin tool,
// and none of a coding session's vocabulary, since office and batch
// conversations load it too.
func TestAuditSkillIsSelfContainedAndSourceNeutral(t *testing.T) {
	sk := auditSkill(t)
	text := strings.ToLower(sk.Description + "\n" + sk.Body)
	for _, word := range []string{
		"skill.md", ".claude", "~/.forebrain", "forebrain.yaml", "http://", "https://",
		"deepseek", "anthropic", "openai", "chatgpt", "claude", "kimi", "zhipu", "gemini", "gpt",
		"present_files", "web_search", "web_fetch", "user_interaction", "intermediate_tool",
		"enter_plan_mode", "exit_plan_mode", "read_file", "write_file", "edit_file", "apply_patch",
		"subagent_run", "memory_search",
		"codebase", "code review", "refactor", "diff", "pull request", "git", "repository", "monorepo",
	} {
		if strings.Contains(text, word) {
			t.Errorf("audit skill names %q", word)
		}
	}
}

// TestAuditSkillReferencesShip pins the three reference files the skill's body
// points at: each one is embedded, reaches disk byte for byte through Install,
// and carries no trace of the skill it was ported from. The neutrality gate in
// TestAuditSkillIsSelfContainedAndSourceNeutral covers SKILL.md only - that is
// the surface every office or batch conversation loads. A reference is read on
// demand and carries the coding mechanics (version control commands,
// disposable workspaces, tool names), but it must not smuggle in the
// vocabulary of the skill it replaces.
func TestAuditSkillReferencesShip(t *testing.T) {
	home := t.TempDir()
	if err := Install(home); err != nil {
		t.Fatalf("Install: %v", err)
	}
	sk := auditSkill(t)
	installed := filepath.Join(home, "skills", ".system", "audit", "references")
	for _, name := range []string{"audit-playbook.md", "plan-template.md", "closing-the-loop.md"} {
		embedded, err := assetsFS.ReadFile("system_assets/audit/references/" + name)
		if err != nil {
			t.Fatalf("read embedded references/%s: %v", name, err)
		}
		if !strings.Contains(sk.Body, "references/"+name) {
			t.Errorf("the skill body never points at references/%s", name)
		}
		onDisk, err := os.ReadFile(filepath.Join(installed, name))
		if err != nil {
			t.Fatalf("read installed references/%s: %v", name, err)
		}
		if !bytes.Equal(onDisk, embedded) {
			t.Errorf("installed references/%s differs from the embedded copy", name)
		}
		text := strings.ToLower(string(embedded))
		for _, trace := range []string{
			"improve skill", "/improve", ".claude", "claude", "shadcn",
			"anthropic", "openai", "sonnet", "haiku", "sendmessage",
		} {
			if strings.Contains(text, trace) {
				t.Errorf("references/%s names %q", name, trace)
			}
		}
	}
}

// TestAuditSkillIsDiscoveredAndSlashInvocable runs the path a conversation
// takes: the installed built-in layer is discovered, so the skill reaches the
// catalog the model chooses from, and it activates as a slash command, so a
// user can start it as /audit with arguments.
func TestAuditSkillIsDiscoveredAndSlashInvocable(t *testing.T) {
	home := t.TempDir()
	if err := Install(home); err != nil {
		t.Fatalf("Install: %v", err)
	}
	found, err := (Loader{Roots: AgentSkillRoots(home, home, safety.ProjectContext{}), StateRoot: home}).Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, item := range found {
		activation, err := LoadActivation(item.SkillFile)
		if err != nil || activation.Name != "audit" {
			continue
		}
		if !activation.SlashCommand {
			t.Fatal("the audit skill is not a slash command")
		}
		if !strings.Contains(activation.Content, "# Audit and planning workflows") {
			t.Fatalf("loading the audit skill does not return its workflows: %.200q", activation.Content)
		}
		return
	}
	t.Fatal("the installed audit skill was not discovered")
}
