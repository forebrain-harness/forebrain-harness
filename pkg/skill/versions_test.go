package skill

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeVersionedSkill(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(fmt.Sprintf(
		"---\nname: %s\ndescription: test skill %s\n---\n\n%s\n", name, name, body)), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestScanSkipsVersionAndDraftDirectories locks the rule the whole version
// layout depends on: .versions and .drafts are hidden directories beside the
// skills, and discovery must never offer their contents as skills — a
// published copy or a draft is not a second skill.
func TestScanSkipsVersionAndDraftDirectories(t *testing.T) {
	root := t.TempDir()
	writeVersionedSkill(t, root, "real", "body")
	// A version directory and a draft directory that each hold a perfectly
	// loadable skill — exactly what they hold in production.
	writeVersionedSkill(t, filepath.Join(root, versionsDirName, "real"), "real", "v1 body")
	writeVersionedSkill(t, filepath.Join(draftsDirNameOf(root), "other"), "other", "draft body")

	found := scanSkillRoots([]string{root})
	if len(found) != 1 || found[0].Name != "real" {
		names := make([]string, 0, len(found))
		for _, item := range found {
			names = append(names, item.Name)
		}
		t.Fatalf("scanner returned %v, want only the formal skill", names)
	}
	discovered, err := (Loader{Roots: []string{root}}).Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered) != 1 || discovered[0].Name != "real" {
		t.Fatalf("runtime discovery returned %+v", discovered)
	}
	// A skill that exists only as a draft is not in the catalog either: it
	// has no published version, so there is nothing to load.
	catalog, err := Catalog([]string{root}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(catalog, "real") || strings.Contains(catalog, "other") {
		t.Fatalf("catalog %q must list only the published skill", catalog)
	}
}

func draftsDirNameOf(root string) string {
	return filepath.Join(root, draftsDirName)
}

// TestBootstrapRecordsExistingSkillAsV1 pins the migration story for a skill
// that predates the version store: Refresh bootstraps it as v1, keyed by the
// install source the workspace lock kept, and a second run changes nothing.
func TestBootstrapRecordsExistingSkillAsV1(t *testing.T) {
	root := t.TempDir()
	dir := writeVersionedSkill(t, root, "legacy", "body")
	wantAt := "2026-10-01T09:15:00Z"

	store := VersionStore{Root: root}
	v, err := store.Bootstrap("legacy", VersionRecord{
		Kind:   VersionKindInstalled,
		Source: "https://github.com/example/skills.git",
		At:     wantAt,
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if v != 1 {
		t.Fatalf("Bootstrap version = %d, want 1", v)
	}
	state, err := store.State("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if state.Current != 1 || len(state.Versions) != 1 {
		t.Fatalf("state after bootstrap: %+v", state)
	}
	rec := state.Versions[0]
	if rec.V != 1 || rec.Kind != VersionKindInstalled || rec.Source != "https://github.com/example/skills.git" || rec.At != wantAt {
		t.Fatalf("bootstrap record: %+v", rec)
	}
	if rec.Stat.Files != 1 || rec.Stat.Added == 0 || rec.Stat.Removed != 0 {
		t.Fatalf("bootstrap stat: %+v", rec.Stat)
	}
	// The version directory holds a copy of the formal directory.
	raw, err := os.ReadFile(filepath.Join(root, versionsDirName, "legacy", "v1", "SKILL.md"))
	if err != nil {
		t.Fatalf("read v1 copy: %v", err)
	}
	if !strings.Contains(string(raw), "test skill legacy") {
		t.Fatalf("v1 copy does not match the formal directory: %q", raw)
	}
	_ = dir

	// Idempotent: a skill that already has a history is left alone.
	again, err := store.Bootstrap("legacy", VersionRecord{Kind: VersionKindCreated})
	if err != nil {
		t.Fatalf("second Bootstrap: %v", err)
	}
	if again != 0 {
		t.Fatalf("second Bootstrap reported a new version %d", again)
	}
	state2, _ := store.State("legacy")
	if state2.Versions[0].Kind != VersionKindInstalled {
		t.Fatalf("second Bootstrap rewrote the history: %+v", state2.Versions[0])
	}
}

func publishDraft(t *testing.T, store VersionStore, name, body string) int {
	t.Helper()
	draft, err := store.EnsureDraft(name)
	if err != nil {
		t.Fatalf("EnsureDraft: %v", err)
	}
	if err := os.WriteFile(filepath.Join(draft, "SKILL.md"), []byte(fmt.Sprintf(
		"---\nname: %s\ndescription: test skill %s\n---\n\n%s\n", name, name, body)), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := store.Publish(name, "note")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return v
}

// TestPublishSwapsFormalDirectoryAndClearsDraft walks the whole happy path:
// a draft on top of v1 publishes as v2, the formal directory becomes the new
// content, the draft is gone, and the manifest tells the story.
func TestPublishSwapsFormalDirectoryAndClearsDraft(t *testing.T) {
	root := t.TempDir()
	writeVersionedSkill(t, root, "gateway-ops", "v1 body")
	store := VersionStore{Root: root}
	if _, err := store.Bootstrap("gateway-ops", VersionRecord{Kind: VersionKindCreated}); err != nil {
		t.Fatal(err)
	}

	if v := publishDraft(t, store, "gateway-ops", "v2 body"); v != 2 {
		t.Fatalf("published version = %d, want 2", v)
	}
	formal, err := os.ReadFile(filepath.Join(root, "gateway-ops", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(formal), "v2 body") {
		t.Fatalf("formal directory still holds the old content: %q", formal)
	}
	if _, err := os.Stat(filepath.Join(root, draftsDirName, "gateway-ops")); !os.IsNotExist(err) {
		t.Fatalf("draft survived publish: %v", err)
	}
	state, _ := store.State("gateway-ops")
	if state.Current != 2 || state.HasDraft || len(state.Versions) != 2 {
		t.Fatalf("state after publish: %+v", state)
	}
	rec := state.Versions[1]
	if rec.Kind != VersionKindPublished || rec.Note != "note" {
		t.Fatalf("publish record: %+v", rec)
	}
	// stat is relative to the previous version: the whole file changed.
	if rec.Stat.Added == 0 || rec.Stat.Removed == 0 {
		t.Fatalf("publish stat: %+v", rec.Stat)
	}
	// The old version is still readable.
	old, err := store.ReadVersionFile("gateway-ops", 1, "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(old), "v1 body") {
		t.Fatalf("v1 no longer holds its own content: %q", old)
	}
	// Publishing with no draft is an error, not a republish.
	if _, err := store.Publish("gateway-ops", ""); err == nil {
		t.Fatal("publish without a draft succeeded")
	}
}

// TestPublishFailureLeavesFormalAndManifestUntouched is the fault-injection
// test the plan demands: fail one step of the formal-directory swap and prove
// the formal directory and manifest.json are exactly what they were.
func TestPublishFailureLeavesFormalAndManifestUntouched(t *testing.T) {
	for name, fail := range map[string]func(ops *versionOps){
		"copy for the formal swap": func(ops *versionOps) {
			calls := 0
			ops.copyTree = func(src, dst string) error {
				calls++
				// The first copy is the draft into v2; fail the second,
				// which prepares the formal swap.
				if calls == 2 {
					return errors.New("injected copy failure")
				}
				return copySkillTree(src, dst)
			}
		},
		"rename formal aside": func(ops *versionOps) {
			ops.rename = func(old, new string) error {
				if strings.Contains(new, ".old-") {
					return errors.New("injected rename failure")
				}
				return os.Rename(old, new)
			}
		},
		"rename new copy into place": func(ops *versionOps) {
			ops.rename = func(old, new string) error {
				if strings.Contains(old, ".tmp-formal-") {
					return errors.New("injected rename failure")
				}
				return os.Rename(old, new)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeVersionedSkill(t, root, "gateway-ops", "v1 body")
			store := VersionStore{Root: root}
			if _, err := store.Bootstrap("gateway-ops", VersionRecord{Kind: VersionKindCreated}); err != nil {
				t.Fatal(err)
			}
			manifestBefore, err := os.ReadFile(filepath.Join(root, versionsDirName, "gateway-ops", manifestName))
			if err != nil {
				t.Fatal(err)
			}
			formalBefore, err := os.ReadFile(filepath.Join(root, "gateway-ops", "SKILL.md"))
			if err != nil {
				t.Fatal(err)
			}

			draft, err := store.EnsureDraft("gateway-ops")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(draft, "SKILL.md"), []byte("---\nname: gateway-ops\ndescription: test skill\n---\n\nv2 body\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			// The snapshot the failed publish must not move is the one
			// taken after EnsureDraft: creating a draft legitimately adds
			// draft_base to the manifest, and that state — draft present,
			// manifest naming it — is what a failed publish leaves behind.
			manifestBefore, err = os.ReadFile(filepath.Join(root, versionsDirName, "gateway-ops", manifestName))
			if err != nil {
				t.Fatal(err)
			}
			failing := VersionStore{Root: root, ops: &versionOps{}}
			fail(failing.ops)
			if _, err := failing.Publish("gateway-ops", "note"); err == nil {
				t.Fatal("publish with an injected failure succeeded")
			}

			manifestAfter, err := os.ReadFile(filepath.Join(root, versionsDirName, "gateway-ops", manifestName))
			if err != nil {
				t.Fatal(err)
			}
			if string(manifestAfter) != string(manifestBefore) {
				t.Fatalf("manifest changed:\nbefore: %s\nafter:  %s", manifestBefore, manifestAfter)
			}
			formalAfter, err := os.ReadFile(filepath.Join(root, "gateway-ops", "SKILL.md"))
			if err != nil {
				t.Fatal(err)
			}
			if string(formalAfter) != string(formalBefore) {
				t.Fatalf("formal directory changed:\nbefore: %s\nafter:  %s", formalBefore, formalAfter)
			}
			// A retry with the failure gone publishes cleanly to v2, so a
			// failed publish never burns a version number.
			if v := publishDraftRestart(t, store, "gateway-ops"); v != 2 {
				t.Fatalf("retry published version %d, want 2", v)
			}
		})
	}
}

// publishDraftRestart re-publishes using the existing draft directory (a
// failed publish leaves it in place).
func publishDraftRestart(t *testing.T, store VersionStore, name string) int {
	t.Helper()
	draft, err := store.EnsureDraft(name)
	if err != nil {
		t.Fatalf("EnsureDraft: %v", err)
	}
	if err := os.WriteFile(filepath.Join(draft, "SKILL.md"), []byte(fmt.Sprintf(
		"---\nname: %s\ndescription: test skill\n---\n\nv2 body\n", name)), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := store.Publish(name, "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return v
}

// TestDiscardDraftKeepsPublishedVersion pins the semantics of "all changes
// only ever land in the draft": discarding drops the unpublished changes and
// the published version never moved.
func TestDiscardDraftKeepsPublishedVersion(t *testing.T) {
	root := t.TempDir()
	writeVersionedSkill(t, root, "gateway-ops", "v1 body")
	store := VersionStore{Root: root}
	store.Bootstrap("gateway-ops", VersionRecord{Kind: VersionKindCreated})

	draft, err := store.EnsureDraft("gateway-ops")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(draft, "SKILL.md"), []byte("broken draft"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Discard("gateway-ops"); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, err := os.Stat(draft); !os.IsNotExist(err) {
		t.Fatalf("draft survived discard: %v", err)
	}
	state, _ := store.State("gateway-ops")
	if state.HasDraft || state.DraftBase != 0 {
		t.Fatalf("state after discard: %+v", state)
	}
	formal, _ := os.ReadFile(filepath.Join(root, "gateway-ops", "SKILL.md"))
	if !strings.Contains(string(formal), "v1 body") {
		t.Fatalf("discard touched the published content: %q", formal)
	}
}

// TestRestoreAsDraftReplacesDraftWithOldVersion covers the history view's
// restore action: v2's content becomes the draft, publish makes it current
// again.
func TestRestoreAsDraftReplacesDraftWithOldVersion(t *testing.T) {
	root := t.TempDir()
	writeVersionedSkill(t, root, "gateway-ops", "v1 body")
	store := VersionStore{Root: root}
	store.Bootstrap("gateway-ops", VersionRecord{Kind: VersionKindCreated})
	publishDraft(t, store, "gateway-ops", "v2 body")

	if err := store.RestoreAsDraft("gateway-ops", 1); err != nil {
		t.Fatalf("RestoreAsDraft: %v", err)
	}
	state, _ := store.State("gateway-ops")
	if !state.HasDraft || state.DraftBase != 1 || state.Current != 2 {
		t.Fatalf("state after restore: %+v", state)
	}
	draftBody, err := os.ReadFile(filepath.Join(root, draftsDirName, "gateway-ops", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(draftBody), "v1 body") {
		t.Fatalf("draft does not hold v1's content: %q", draftBody)
	}
	if err := store.RestoreAsDraft("gateway-ops", 9); err == nil {
		t.Fatal("restoring a version that does not exist succeeded")
	}
}

// TestDiffBetweenVersionsAndDraft covers the comparison the version views
// render: added, removed and modified files with line counts.
func TestDiffBetweenVersionsAndDraft(t *testing.T) {
	root := t.TempDir()
	dir := writeVersionedSkill(t, root, "gateway-ops", "line one\nline two\nline three")
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("kept\nremoved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := VersionStore{Root: root}
	store.Bootstrap("gateway-ops", VersionRecord{Kind: VersionKindCreated})

	draft, _ := store.EnsureDraft("gateway-ops")
	if err := os.WriteFile(filepath.Join(draft, "SKILL.md"), []byte("---\nname: gateway-ops\ndescription: d\n---\n\nline one\nline two changed\nline three\nline four\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(draft, "new.txt"), []byte("brand new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(draft, "old.txt")); err != nil {
		t.Fatal(err)
	}

	diff, err := store.Diff("gateway-ops", "v1", "draft")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	byPath := map[string]FileDiff{}
	for _, item := range diff {
		byPath[item.Path] = item
	}
	skilled := byPath["SKILL.md"]
	// The draft changed the description line (+1 −1), "line two" (+1 −1)
	// and added "line four" (+1): 3 added, 2 removed.
	if skilled.Status != "modified" || skilled.Added != 3 || skilled.Removed != 2 {
		t.Fatalf("SKILL.md diff: %+v", skilled)
	}
	if got := byPath["new.txt"]; got.Status != "added" || got.Added != 1 {
		t.Fatalf("new.txt diff: %+v", got)
	}
	if got := byPath["old.txt"]; got.Status != "removed" || got.Removed != 2 {
		t.Fatalf("old.txt diff: %+v", got)
	}
	if _, err := store.Diff("gateway-ops", "v9", "draft"); err == nil {
		t.Fatal("diff against a missing version succeeded")
	}
	if _, err := store.Diff("gateway-ops", "v1", "v1"); err != nil {
		t.Fatalf("diff of a version against itself: %v", err)
	}
}

// TestDiffFileCarriesTheLineDiff covers the one-file answer the compare view
// draws: the same counts Diff lists, plus the unified diff the web renders —
// and the reasons a file can have none (unchanged, binary, too large,
// missing).
func TestDiffFileCarriesTheLineDiff(t *testing.T) {
	root := t.TempDir()
	dir := writeVersionedSkill(t, root, "gateway-ops", "line one\nline two\nline three")
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("kept\nremoved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "same.txt"), []byte("same\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := VersionStore{Root: root}
	store.Bootstrap("gateway-ops", VersionRecord{Kind: VersionKindCreated})

	draft, _ := store.EnsureDraft("gateway-ops")
	if err := os.WriteFile(filepath.Join(draft, "SKILL.md"), []byte("---\nname: gateway-ops\ndescription: d\n---\n\nline one\nline two changed\nline three\nline four\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(draft, "new.txt"), []byte("brand new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(draft, "blob.bin"), []byte("a\x00b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(draft, "big.txt"), []byte(strings.Repeat("x\n", 20001)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(draft, ".hidden"), []byte("h\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(draft, "old.txt")); err != nil {
		t.Fatal(err)
	}

	skilled, err := store.DiffFile("gateway-ops", "v1", "draft", "SKILL.md")
	if err != nil {
		t.Fatalf("DiffFile SKILL.md: %v", err)
	}
	if skilled.Status != "modified" || skilled.Added != 3 || skilled.Removed != 2 {
		t.Fatalf("SKILL.md diff: %+v", skilled)
	}
	if want := "--- a/SKILL.md\n+++ b/SKILL.md\n@@ -1,8 +1,9 @@\n ---\n name: gateway-ops\n-description: test skill gateway-ops\n+description: d\n ---\n \n line one\n-line two\n+line two changed\n line three\n+line four\n"; skilled.UnifiedDiff != want {
		t.Fatalf("SKILL.md unified diff:\n got %q\nwant %q", skilled.UnifiedDiff, want)
	}

	added, err := store.DiffFile("gateway-ops", "v1", "draft", "new.txt")
	if err != nil {
		t.Fatalf("DiffFile new.txt: %v", err)
	}
	if added.Status != "added" || added.Added != 1 {
		t.Fatalf("new.txt diff: %+v", added)
	}
	if want := "--- a/new.txt\n+++ b/new.txt\n@@ -0,0 +1 @@\n+brand new\n"; added.UnifiedDiff != want {
		t.Fatalf("new.txt unified diff:\n got %q\nwant %q", added.UnifiedDiff, want)
	}

	removed, err := store.DiffFile("gateway-ops", "v1", "draft", "old.txt")
	if err != nil {
		t.Fatalf("DiffFile old.txt: %v", err)
	}
	if removed.Status != "removed" || removed.Removed != 2 {
		t.Fatalf("old.txt diff: %+v", removed)
	}
	if want := "--- a/old.txt\n+++ b/old.txt\n@@ -1,2 +0,0 @@\n-kept\n-removed\n"; removed.UnifiedDiff != want {
		t.Fatalf("old.txt unified diff:\n got %q\nwant %q", removed.UnifiedDiff, want)
	}

	same, err := store.DiffFile("gateway-ops", "v1", "draft", "same.txt")
	if err != nil {
		t.Fatalf("DiffFile same.txt: %v", err)
	}
	if same.Status != "unchanged" || same.UnifiedDiff != "" {
		t.Fatalf("same.txt diff: %+v", same)
	}

	blob, err := store.DiffFile("gateway-ops", "v1", "draft", "blob.bin")
	if err != nil {
		t.Fatalf("DiffFile blob.bin: %v", err)
	}
	if blob.Status != "added" || !blob.Binary || blob.UnifiedDiff != "" {
		t.Fatalf("blob.bin diff: %+v", blob)
	}

	big, err := store.DiffFile("gateway-ops", "v1", "draft", "big.txt")
	if err != nil {
		t.Fatalf("DiffFile big.txt: %v", err)
	}
	if big.Status != "added" || !big.TooLarge || big.UnifiedDiff != "" {
		t.Fatalf("big.txt diff: %+v", big)
	}

	for _, path := range []string{".hidden", "../gateway-ops/SKILL.md", "nope.md"} {
		if _, err := store.DiffFile("gateway-ops", "v1", "draft", path); !errors.Is(err, ErrDiffFileNotFound) {
			t.Fatalf("DiffFile %q: err=%v", path, err)
		}
	}
	if _, err := store.DiffFile("gateway-ops", "v9", "draft", "SKILL.md"); err == nil || errors.Is(err, ErrDiffFileNotFound) {
		t.Fatalf("DiffFile against a missing version: err=%v", err)
	}
}

// TestUnifiedLineDiffSplitsDistantChangesIntoHunks pins the hunk shape the
// web's parser reads: two edits far apart become two hunks with three lines
// of context each, and the web draws one gap row between them.
func TestUnifiedLineDiffSplitsDistantChangesIntoHunks(t *testing.T) {
	oldLines := make([]string, 20)
	newLines := make([]string, 20)
	for i := range oldLines {
		oldLines[i] = fmt.Sprintf("l%d", i+1)
		newLines[i] = oldLines[i]
	}
	newLines[1] = "L2"
	newLines[17] = "L18"

	got := unifiedLineDiff("f", oldLines, newLines)
	want := "--- a/f\n+++ b/f\n@@ -1,5 +1,5 @@\n l1\n-l2\n+L2\n l3\n l4\n l5\n@@ -15,6 +15,6 @@\n l15\n l16\n l17\n-l18\n+L18\n l19\n l20\n"
	if got != want {
		t.Fatalf("unified diff:\n got %q\nwant %q", got, want)
	}
}

// TestResolveDraftTarget pins the redirection every write into a formal skill
// directory goes through: formal paths land in the draft, .versions is
// refused, .drafts passes through, and unrelated paths are not claimed.
func TestResolveDraftTarget(t *testing.T) {
	// The canonical spelling, because a returned draft target lives under
	// the canonical root the store operates on.
	root := CanonicalSkillPath(t.TempDir())
	writeVersionedSkill(t, root, "gateway-ops", "v1 body")
	store := VersionStore{Root: root}
	store.Bootstrap("gateway-ops", VersionRecord{Kind: VersionKindCreated})
	roots := []string{root}

	target, handled, err := ResolveDraftTarget(roots, filepath.Join(root, "gateway-ops", "SKILL.md"))
	if err != nil || !handled {
		t.Fatalf("formal write: handled=%v err=%v", handled, err)
	}
	if target.SkillName != "gateway-ops" {
		t.Fatalf("target: %+v", target)
	}
	wantDraft := filepath.Join(root, draftsDirName, "gateway-ops", "SKILL.md")
	if target.Target != wantDraft {
		t.Fatalf("target path = %s, want %s", target.Target, wantDraft)
	}
	if target.Source != filepath.Join(root, "gateway-ops", "SKILL.md") {
		t.Fatalf("source path: %+v", target)
	}
	// The draft was created from the current version, so it holds the
	// published content.
	raw, err := os.ReadFile(wantDraft)
	if err != nil || !strings.Contains(string(raw), "v1 body") {
		t.Fatalf("draft content: %q err=%v", raw, err)
	}

	// .versions is read-only history.
	if _, handled, err := ResolveDraftTarget(roots, filepath.Join(root, versionsDirName, "gateway-ops", "manifest.json")); err == nil || !handled || !errors.Is(err, ErrVersionStoreReadOnly) {
		t.Fatalf("versions write: handled=%v err=%v", handled, err)
	}
	// .drafts passes through unchanged.
	target, handled, err = ResolveDraftTarget(roots, filepath.Join(root, draftsDirName, "gateway-ops", "SKILL.md"))
	if err != nil || !handled || target.Target != filepath.Join(root, draftsDirName, "gateway-ops", "SKILL.md") || target.Source != "" {
		t.Fatalf("draft write: %+v handled=%v err=%v", target, handled, err)
	}
	// The skill directory itself is the skills list's business.
	if _, handled, err := ResolveDraftTarget(roots, filepath.Join(root, "gateway-ops")); err == nil || !handled || !errors.Is(err, ErrSkillDirDeletion) {
		t.Fatalf("whole-directory delete: handled=%v err=%v", handled, err)
	}
	// A path outside every root, and one inside another hidden directory, is
	// not claimed.
	if _, handled, err := ResolveDraftTarget(roots, filepath.Join(t.TempDir(), "elsewhere.txt")); err != nil || handled {
		t.Fatalf("unrelated path: handled=%v err=%v", handled, err)
	}
	if _, handled, err := ResolveDraftTarget(roots, filepath.Join(root, ".system", "caveman", "SKILL.md")); err != nil || handled {
		t.Fatalf("builtin path: handled=%v err=%v", handled, err)
	}
}

// TestCopySkillTreePreservesPermissionsAndSkipsGitAndLinks pins the copy
// rules: a bundled script keeps its execute bit, the install transport's .git
// stays behind, and a symlink pointing outside the tree is not followed.
func TestCopySkillTreePreservesPermissionsAndSkipsGitAndLinks(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "scripts", "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".git", "HEAD"), []byte("ref"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(src, "leak.txt")); err != nil {
		t.Skip("symlinks unavailable")
	}

	dst := filepath.Join(t.TempDir(), "copy")
	if err := copySkillTree(src, dst); err != nil {
		t.Fatalf("copySkillTree: %v", err)
	}
	info, err := os.Stat(filepath.Join(dst, "scripts", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("script mode = %v, want 0755", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git was copied: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "leak.txt")); !os.IsNotExist(err) {
		t.Fatalf("symlink was copied: %v", err)
	}
}

// --- Step 2: bootstrap, install-as-v1, the versioned listing, publish, pull.

// versionTestService builds a service over an isolated home, with the agent
// workspace layer under <home>/workspace/skills.
func versionTestService(t *testing.T) (*Service, string, string) {
	t.Helper()
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := NewServiceForWorkspace(home, workspace)
	return svc, home, filepath.Join(workspace, "skills")
}

// TestRefreshBootstrapsLegacySkillsAsV1 pins the migration: Refresh records
// v1 for a formal skill with no history, taking kind, source and upstream
// revision from the workspace lock when it has an entry and "created" when it
// does not. Built-in directories are never versioned.
func TestRefreshBootstrapsLegacySkillsAsV1(t *testing.T) {
	svc, home, agentRoot := versionTestService(t)
	writeVersionedSkill(t, agentRoot, "handmade", "body")
	writeVersionedSkill(t, agentRoot, "from-repo", "body")
	if err := os.MkdirAll(filepath.Join(home, "skills", systemSkillsDirName, "builtin-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "skills", systemSkillsDirName, "builtin-skill", "SKILL.md"), []byte("---\nname: builtin-skill\ndescription: built in\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MergeLockEntryV2(workspace2(home), "from-repo", LockEntry{
		SourceType: "git_repo",
		SourceRef:  "https://example.invalid/repo.git",
		Revision:   "d41c8e0",
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	store := VersionStore{Root: agentRoot}
	handmade, err := store.State("handmade")
	if err != nil {
		t.Fatal(err)
	}
	if handmade.Current != 1 || handmade.Versions[0].Kind != VersionKindCreated || handmade.Versions[0].Source != "" {
		t.Fatalf("handmade bootstrap: %+v", handmade.Versions[0])
	}
	installed, err := store.State("from-repo")
	if err != nil {
		t.Fatal(err)
	}
	rec := installed.Versions[0]
	if rec.Kind != VersionKindInstalled || rec.Source != "https://example.invalid/repo.git" || rec.UpstreamRev != "d41c8e0" {
		t.Fatalf("installed bootstrap record: %+v", rec)
	}
	// The built-in layer is read-only: no hidden directory is created in it.
	if _, err := os.Stat(filepath.Join(home, "skills", versionsDirName)); !os.IsNotExist(err) {
		t.Fatalf("shared root gained a version store for built-ins: %v", err)
	}
	// Idempotent: a second Refresh changes neither record.
	if err := svc.Refresh(); err != nil {
		t.Fatal(err)
	}
	again, _ := store.State("handmade")
	if len(again.Versions) != 1 {
		t.Fatalf("second Refresh rewrote the history: %+v", again.Versions)
	}
}

func workspace2(home string) string {
	return filepath.Join(home, "workspace")
}

// TestInstallRecordsV1WithUpstreamRev pins the install door: installing from
// a git source records v1 with kind=installed and the upstream commit.
func TestInstallRecordsV1WithUpstreamRev(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	svc, _, agentRoot := versionTestService(t)
	repo := initGitRepoWithSkill(t, "pptx", "pptx demo", "v1 body")

	if _, err := svc.Install(context.Background(), InstallRequest{
		SourceRef: "file://" + repo,
		Skill:     "pptx",
		Name:      "pptx",
		DestScope: ScopeWorkspace,
	}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	state, err := (VersionStore{Root: agentRoot}).State("pptx")
	if err != nil {
		t.Fatal(err)
	}
	if state.Current != 1 || len(state.Versions) != 1 {
		t.Fatalf("installed skill state: %+v", state)
	}
	rec := state.Versions[0]
	if rec.Kind != VersionKindInstalled || rec.Source != "file://"+repo || rec.UpstreamRev == "" {
		t.Fatalf("install v1 record: %+v", rec)
	}
}

// initGitRepoWithSkill builds a local git repository holding one skill
// directory, and returns its path.
func initGitRepoWithSkill(t *testing.T, name, description, body string) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, name, "SKILL.md"), []byte(fmt.Sprintf(
		"---\nname: %s\ndescription: %s\n---\n\n%s\n", name, description, body)), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init")
	run("add", ".")
	run("commit", "-m", "init")
	return repo
}

// TestListVersionedCoversPublishedPendingAndDraftOnly pins the listing the
// skill surfaces build on: every state a versioned skill can be in has a row,
// draft-only skills included, with the state the row reports.
func TestListVersionedCoversPublishedPendingAndDraftOnly(t *testing.T) {
	svc, _, agentRoot := versionTestService(t)
	writeVersionedSkill(t, agentRoot, "published", "body")
	writeVersionedSkill(t, agentRoot, "pending", "body")
	store := VersionStore{Root: agentRoot}
	store.Bootstrap("published", VersionRecord{Kind: VersionKindCreated})
	store.Bootstrap("pending", VersionRecord{Kind: VersionKindCreated})
	if _, err := store.EnsureDraft("pending"); err != nil {
		t.Fatal(err)
	}
	// A draft-only skill: never published, invisible to the formal scan.
	draftOnly, err := store.EnsureDraft("draft-only")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(draftOnly, "SKILL.md"), []byte("---\nname: draft-only\ndescription: not published yet\n---\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rows, err := svc.ListVersioned(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]SkillVersionRow{}
	for _, row := range rows {
		byName[row.Name] = row
	}
	if row, ok := byName["published"]; !ok || row.State.Current != 1 || row.State.StateLabel() != SkillStatePublished {
		t.Fatalf("published row: %+v ok=%v", row, ok)
	}
	if row, ok := byName["pending"]; !ok || !row.State.HasDraft || row.State.StateLabel() != SkillStatePending {
		t.Fatalf("pending row: %+v ok=%v", row, ok)
	}
	row, ok := byName["draft-only"]
	if !ok || row.State.StateLabel() != SkillStateDraft || row.Description != "not published yet" {
		t.Fatalf("draft-only row: %+v ok=%v", row, ok)
	}
	if !strings.Contains(row.RootPath, draftsDirName) {
		t.Fatalf("draft-only row root = %s, want the draft directory", row.RootPath)
	}
	// The upstream probe is part of the listing contract only when a source
	// was recorded: none of these skills has one, so no update markers.
	for _, row := range rows {
		if row.State.Update != nil {
			t.Fatalf("unexpected update marker: %+v", row.State.Update)
		}
	}
}

// TestPublishSkillValidatesAndEnables pins the publish door's two side rules:
// a draft whose SKILL.md would not load is refused, and a published skill
// comes back enabled even if it had been disabled.
func TestPublishSkillValidatesAndEnables(t *testing.T) {
	svc, _, agentRoot := versionTestService(t)
	writeVersionedSkill(t, agentRoot, "gateway-ops", "v1 body")
	if err := SetEnabledPathsForWorkspace(svc.Home, filepath.Join(svc.Home, "workspace"), "", nil); err != nil {
		t.Fatal(err)
	}
	// Disable the skill, then publish a draft on top of it.
	enabled := []string{}
	if err := svc.SetEnabledPaths(enabled); err != nil {
		t.Fatal(err)
	}
	store := VersionStore{Root: agentRoot}
	store.Bootstrap("gateway-ops", VersionRecord{Kind: VersionKindCreated})
	draft, err := store.EnsureDraft("gateway-ops")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(draft, "SKILL.md"), []byte("---\nname: gateway-ops\ndescription: no frontmatter description below would fail too\n---\n\nv2 body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An unreadable draft is refused: a formal directory nobody can load is
	// worse than no publish at all.
	if err := os.WriteFile(filepath.Join(draft, "SKILL.md"), []byte("no frontmatter at all\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PublishSkill("gateway-ops", ""); err == nil {
		t.Fatal("publishing an unparseable draft succeeded")
	}
	if err := os.WriteFile(filepath.Join(draft, "SKILL.md"), []byte("---\nname: gateway-ops\ndescription: v2\n---\n\nv2 body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := svc.PublishSkill("gateway-ops", "note")
	if err != nil {
		t.Fatalf("PublishSkill: %v", err)
	}
	if v != 2 {
		t.Fatalf("published version = %d, want 2", v)
	}
	entries, err := DiscoverForWorkspace(svc.Home, filepath.Join(svc.Home, "workspace"), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name != "gateway-ops" {
			continue
		}
		if !entry.Enabled {
			t.Fatalf("publishing left the skill disabled: %+v", entry)
		}
	}
}

// TestSetEnabledPathsRefusesDraftOnlySkill pins the toggle rule: a skill with
// no published version cannot be enabled, because no session could load it.
func TestSetEnabledPathsRefusesDraftOnlySkill(t *testing.T) {
	svc, _, agentRoot := versionTestService(t)
	writeVersionedSkill(t, agentRoot, "real", "body")
	draft, err := (VersionStore{Root: agentRoot}).EnsureDraft("unpublished")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(draft, "SKILL.md"), []byte("---\nname: unpublished\ndescription: draft\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = svc.SetEnabledPaths([]string{filepath.Join(agentRoot, "real"), draft})
	if err == nil || !strings.Contains(err.Error(), "publish it first") {
		t.Fatalf("enabling a draft-only skill: err=%v", err)
	}
	if err := svc.SetEnabledPaths([]string{filepath.Join(agentRoot, "real")}); err != nil {
		t.Fatalf("enabling a published skill: %v", err)
	}
}

// TestRestoreVersionAsDraftRequiresConfirmation pins the replace_draft
// contract: an existing draft is replaced only when the caller says so.
func TestRestoreVersionAsDraftRequiresConfirmation(t *testing.T) {
	svc, _, agentRoot := versionTestService(t)
	writeVersionedSkill(t, agentRoot, "gateway-ops", "v1 body")
	store := VersionStore{Root: agentRoot}
	store.Bootstrap("gateway-ops", VersionRecord{Kind: VersionKindCreated})
	publishDraftRestart(t, store, "gateway-ops")
	if _, err := store.EnsureDraft("gateway-ops"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestoreVersionAsDraft("gateway-ops", 1, false); err == nil {
		t.Fatal("restoring over a draft without confirmation succeeded")
	}
	if err := svc.RestoreVersionAsDraft("gateway-ops", 1, true); err != nil {
		t.Fatalf("restore with confirmation: %v", err)
	}
	state, _ := store.State("gateway-ops")
	if !state.HasDraft || state.DraftBase != 1 {
		t.Fatalf("state after restore: %+v", state)
	}
}

// TestPullUpdateFetchesUpstreamIntoDraft pins the update path: a change
// upstream lands in the skill's draft carrying its revision, and publishing
// the pulled draft records the version as an update.
func TestPullUpdateFetchesUpstreamIntoDraft(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	svc, _, agentRoot := versionTestService(t)
	repo := initGitRepoWithSkill(t, "upstream-skill", "demo", "v1 body")
	if _, err := svc.Install(context.Background(), InstallRequest{
		SourceRef: "file://" + repo,
		Skill:     "upstream-skill",
		Name:      "upstream-skill",
		DestScope: ScopeWorkspace,
	}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	// Move upstream on.
	if err := os.WriteFile(filepath.Join(repo, "upstream-skill", "SKILL.md"), []byte(
		"---\nname: upstream-skill\ndescription: demo\n---\n\nv2 body upstream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("add", ".")
	run("commit", "-m", "v2")

	store := VersionStore{Root: agentRoot}
	state, _ := store.State("upstream-skill")
	if state.Update != nil {
		t.Fatalf("update marker before pull: %+v", state.Update)
	}
	if err := svc.PullUpdate(context.Background(), "upstream-skill", true); err != nil {
		t.Fatalf("PullUpdate: %v", err)
	}
	state, _ = store.State("upstream-skill")
	if !state.HasDraft {
		t.Fatalf("pull left no draft: %+v", state)
	}
	raw, err := os.ReadFile(filepath.Join(agentRoot, draftsDirName, "upstream-skill", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "v2 body upstream") {
		t.Fatalf("draft does not hold the upstream content: %q", raw)
	}
	// An unconfirmed second pull over an existing draft is refused.
	if err := svc.PullUpdate(context.Background(), "upstream-skill", false); err == nil {
		t.Fatal("pull over an existing draft without confirmation succeeded")
	}
	// Publishing the pulled draft records kind=updated.
	if _, err := svc.PublishSkill("upstream-skill", "pull upstream"); err != nil {
		t.Fatalf("publish pulled draft: %v", err)
	}
	state, _ = store.State("upstream-skill")
	rec := state.Versions[len(state.Versions)-1]
	if rec.Kind != VersionKindUpdated || rec.UpstreamRev == "" {
		t.Fatalf("pulled publish record: %+v", rec)
	}
}

// TestUpdateMarkerAppearsWhenUpstreamMoves pins the list-side of the update
// contract: a git-installed skill whose upstream advertises a different
// revision carries the marker, and a failed probe leaves it absent.
func TestUpdateMarkerAppearsWhenUpstreamMoves(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	svc, _, _ := versionTestService(t)
	repo := initGitRepoWithSkill(t, "tracked-skill", "demo", "v1 body")
	if _, err := svc.Install(context.Background(), InstallRequest{
		SourceRef: "file://" + repo,
		Skill:     "tracked-skill",
		Name:      "tracked-skill",
		DestScope: ScopeWorkspace,
	}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	ForgetRemoteHeadCache()
	// No upstream change: no marker.
	rows, err := svc.ListVersioned(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name == "tracked-skill" && row.State.Update != nil {
			t.Fatalf("marker without an upstream change: %+v", row.State.Update)
		}
	}
	// Move upstream, forget the cache, list again.
	if err := os.WriteFile(filepath.Join(repo, "tracked-skill", "SKILL.md"), []byte(
		"---\nname: tracked-skill\ndescription: demo\n---\n\nmoved on\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("add", ".")
	run("commit", "-m", "move on")
	ForgetRemoteHeadCache()
	rows, err = svc.ListVersioned(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var marker *SkillUpdate
	for _, row := range rows {
		if row.Name == "tracked-skill" {
			marker = row.State.Update
		}
	}
	if marker == nil || marker.Rev == "" {
		t.Fatalf("no update marker after upstream moved: %+v", marker)
	}
}
