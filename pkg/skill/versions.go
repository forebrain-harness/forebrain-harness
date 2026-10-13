// Skill drafts and published versions: the on-disk store, the atomic
// publish, and the draft redirection every skill write goes through.
package skill

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

// The two hidden directories a skill root carries beside its skill
// directories. Both are invisible to discovery (scanSkillDir skips dot
// directories), so a version history or a draft is never mistaken for a
// skill, and neither is reachable as a skill bundle.
const (
	versionsDirName = ".versions"
	draftsDirName   = ".drafts"
	manifestName    = "manifest.json"
)

// Version record kinds. A version's kind says which door produced it: created
// and installed are the two spellings of v1 (bootstrap of a skill someone
// made versus one the installer copied in), published is every draft the user
// released, updated is a draft that arrived by pulling an upstream change.
const (
	VersionKindCreated   = "created"
	VersionKindInstalled = "installed"
	VersionKindPublished = "published"
	VersionKindUpdated   = "updated"
)

// VersionStat is the change summary one version carries relative to the
// version before it: how many files the version holds, and how many lines
// were added and removed to get there from the previous version.
type VersionStat struct {
	Files   int `json:"files"`
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// VersionRecord is one published version in a skill's manifest.
type VersionRecord struct {
	V           int         `json:"v"`
	At          string      `json:"at"`
	Kind        string      `json:"kind"`
	Source      string      `json:"source,omitempty"`
	UpstreamRev string      `json:"upstream_rev,omitempty"`
	Note        string      `json:"note,omitempty"`
	Stat        VersionStat `json:"stat"`
}

// SkillVersionManifest is the whole history of one skill: the published
// versions, which one is current, and — only while a draft exists — what the
// draft is based on and, when it arrived by pulling upstream, which revision
// it came from.
type SkillVersionManifest struct {
	Name       string          `json:"name"`
	Current    int             `json:"current"`
	Versions   []VersionRecord `json:"versions"`
	DraftBase  int             `json:"draft_base,omitempty"`
	DraftRev   string          `json:"draft_upstream_rev,omitempty"`
	DirtyDraft bool            `json:"-"` // reconstructed, never stored
}

// SkillUpdate is the upstream marker: the newest revision on the source a
// git-installed skill came from, newer than the revision the current version
// recorded. Date and files are filled when the local checkout can read them;
// a source that cannot be probed that far still names its revision.
type SkillUpdate struct {
	Rev   string `json:"rev"`
	Date  string `json:"date,omitempty"`
	Files int    `json:"files,omitempty"`
}

// SkillVersionState is everything a listing or a version view needs about one
// skill: its current version, its history, whether a draft sits on top, and —
// filled in by the service layer, which owns the network — whether upstream
// has moved. Current 0 with HasDraft true is a skill that was never
// published; Current 0 without a draft is not versioned at all.
type SkillVersionState struct {
	Name      string          `json:"name"`
	Current   int             `json:"current"`
	Versions  []VersionRecord `json:"versions"`
	HasDraft  bool            `json:"has_draft"`
	DraftBase int             `json:"draft_base,omitempty"`
	DraftDir  string          `json:"draft_dir,omitempty"`
	Update    *SkillUpdate    `json:"update,omitempty"`
}

// SkillVersionStateLabels are the three states a listed skill can be in.
const (
	SkillStateDraft     = "draft"     // never published: only a draft exists
	SkillStatePublished = "published" // a current version, no draft
	SkillStatePending   = "pending"   // a current version plus a draft
)

// StateLabel names the listing state this version state adds up to, or ""
// when the skill carries no version history at all (a foreign layer, a bundle
// member — anything this store does not manage).
func (st SkillVersionState) StateLabel() string {
	if st.Current == 0 && !st.HasDraft {
		return ""
	}
	if st.Current == 0 {
		return SkillStateDraft
	}
	if st.HasDraft {
		return SkillStatePending
	}
	return SkillStatePublished
}

// FileDiff is one file's place in a version-to-version comparison. Added and
// Removed are line counts; a file whose pair is missing counts every line it
// has.
type FileDiff struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // added | removed | modified
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// VersionStore addresses the versions and drafts of the skills under one
// skill root. Root is a layer's skill directory (the agent's
// <workspace>/skills, a project's <project>/.forebrain/skills, or the shared
// <home>/skills); a skill named name lives at <root>/<name> with its history
// at <root>/.versions/<name> and its draft at <root>/.drafts/<name>.
//
// ops is nil in production and carries overridable file operations so the
// publish sequence can be fault-injected in tests; see Publish.
type VersionStore struct {
	Root string
	ops  *versionOps
}

// versionOps are the mutating primitives Publish runs, overridable one by one
// so a test can fail exactly one step of the sequence and prove the formal
// directory survived it.
type versionOps struct {
	rename    func(old, new string) error
	removeAll func(path string) error
	copyTree  func(src, dst string) error
	writeFile func(path string, data []byte) error
}

func (v VersionStore) o() versionOps {
	if v.ops != nil {
		ops := *v.ops
		if ops.rename == nil {
			ops.rename = os.Rename
		}
		if ops.removeAll == nil {
			ops.removeAll = os.RemoveAll
		}
		if ops.copyTree == nil {
			ops.copyTree = copySkillTree
		}
		if ops.writeFile == nil {
			ops.writeFile = defaultVersionOpsWriteFile
		}
		return ops
	}
	return versionOps{
		rename:    os.Rename,
		removeAll: os.RemoveAll,
		copyTree:  copySkillTree,
		writeFile: defaultVersionOpsWriteFile,
	}
}

func defaultVersionOpsWriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (v VersionStore) formal(name string) string {
	return filepath.Join(v.Root, name)
}

func (v VersionStore) historyDir(name string) string {
	return filepath.Join(v.Root, versionsDirName, name)
}

func (v VersionStore) versionDir(name string, version int) string {
	return filepath.Join(v.historyDir(name), fmt.Sprintf("v%d", version))
}

func (v VersionStore) manifestPath(name string) string {
	return filepath.Join(v.historyDir(name), manifestName)
}

func (v VersionStore) draftDir(name string) string {
	return filepath.Join(v.Root, draftsDirName, name)
}

// readManifest returns the skill's manifest, or a zero manifest when none has
// been written yet. A malformed manifest is an error, not a fresh start:
// silently replacing it would hide a history the skill's versions still point
// at.
func (v VersionStore) readManifest(name string) (SkillVersionManifest, error) {
	raw, err := os.ReadFile(v.manifestPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return SkillVersionManifest{Name: strings.TrimSpace(name)}, nil
		}
		return SkillVersionManifest{}, err
	}
	var m SkillVersionManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return SkillVersionManifest{}, fmt.Errorf("read %s: %w", v.manifestPath(name), err)
	}
	if m.Name == "" {
		m.Name = strings.TrimSpace(name)
	}
	return m, nil
}

// writeManifest replaces the manifest atomically: the new bytes land in a
// temporary file beside it and one rename swaps them in, so a reader never
// sees a half-written manifest.
func (v VersionStore) writeManifest(m SkillVersionManifest) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	path := v.manifestPath(m.Name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp-%d", path, time.Now().UnixNano())
	if err := v.o().writeFile(tmp, raw); err != nil {
		return err
	}
	if err := v.o().rename(tmp, path); err != nil {
		_ = v.o().removeAll(tmp)
		return err
	}
	return nil
}

// State reports where one skill stands: its current version, its history,
// whether a draft exists and what it is based on. Update is left nil here;
// the service layer owns the upstream probe that fills it.
func (v VersionStore) State(name string) (SkillVersionState, error) {
	name = strings.TrimSpace(name)
	m, err := v.readManifest(name)
	if err != nil {
		return SkillVersionState{}, err
	}
	draft := v.draftDir(name)
	hasDraft := false
	if info, statErr := os.Stat(draft); statErr == nil && info.IsDir() {
		hasDraft = true
	}
	return SkillVersionState{
		Name:      m.Name,
		Current:   m.Current,
		Versions:  m.Versions,
		HasDraft:  hasDraft,
		DraftBase: m.DraftBase,
		DraftDir:  draft,
	}, nil
}

// EnsureDraft returns the skill's draft directory, creating it from the
// current version when there is none yet. A skill that was never published
// gets an empty draft. The copy lands in a temporary directory that is
// renamed into place, and a concurrent creator that wins the rename is
// invisible to the loser: the loser's rename fails, and the existing draft is
// returned as the answer.
func (v VersionStore) EnsureDraft(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("skill name required")
	}
	draft := v.draftDir(name)
	if info, err := os.Stat(draft); err == nil && info.IsDir() {
		return draft, nil
	}
	src := ""
	m, err := v.readManifest(name)
	if err != nil {
		return "", err
	}
	switch {
	case m.Current > 0:
		if info, statErr := os.Stat(v.versionDir(name, m.Current)); statErr == nil && info.IsDir() {
			src = v.versionDir(name, m.Current)
		}
	}
	if src == "" {
		// No published version to copy from: a skill that predates the
		// version store (bootstrap has not run yet) still has its formal
		// directory, and that is what a draft starts from.
		if info, statErr := os.Stat(v.formal(name)); statErr == nil && info.IsDir() {
			src = v.formal(name)
		}
	}
	if err := os.MkdirAll(filepath.Join(v.Root, draftsDirName), 0o755); err != nil {
		return "", err
	}
	tmp := filepath.Join(v.Root, draftsDirName, fmt.Sprintf(".tmp-%s-%d", name, time.Now().UnixNano()))
	if src != "" {
		if err := v.o().copyTree(src, tmp); err != nil {
			_ = v.o().removeAll(tmp)
			return "", err
		}
	} else if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", err
	}
	if err := v.o().rename(tmp, draft); err != nil {
		_ = v.o().removeAll(tmp)
		if info, statErr := os.Stat(draft); statErr == nil && info.IsDir() {
			return draft, nil
		}
		return "", err
	}
	// draft_base exists only while a draft does. A never-published skill has
	// no manifest to record it in, which is the manifest's own rule: a skill
	// with no versions may have no manifest at all.
	if m.Current > 0 {
		m.DraftBase = m.Current
		m.DraftRev = ""
		if err := v.writeManifest(m); err != nil {
			return "", err
		}
	}
	return draft, nil
}

// Publish releases the skill's draft as its next version. The sequence is the
// one the plan fixed, and every step is arranged so a failure leaves the
// formal directory and the manifest exactly as they were:
//
//  1. the draft is copied to .versions/<name>/v<N+1>/ and the new manifest is
//     written (temporary files, renamed into place);
//  2. a second copy of v<N+1> is prepared beside the formal directory, the
//     formal directory is renamed aside to .versions/<name>/.old-<ts>, and
//     the prepared copy is renamed into its place;
//  3. the draft and the renamed-aside old directory are deleted.
//
// A failure in step 2 rolls everything back: the old manifest bytes are
// restored, v<N+1> is removed, and the formal directory is renamed back from
// .old-<ts>. What cannot roll back is a crash between two renames — the
// window is one rename wide, and every directory involved is a whole copy,
// never a half-written skill.
func (v VersionStore) Publish(name, note string) (int, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("skill name required")
	}
	ops := v.o()
	m, err := v.readManifest(name)
	if err != nil {
		return 0, err
	}
	draft := v.draftDir(name)
	if info, statErr := os.Stat(draft); statErr != nil || !info.IsDir() {
		return 0, fmt.Errorf("skill %q has no draft to publish", name)
	}
	next := m.Current + 1

	// --- step 1: the new version directory and the new manifest.
	if err := os.MkdirAll(v.historyDir(name), 0o755); err != nil {
		return 0, err
	}
	tmpVersion := filepath.Join(v.historyDir(name), fmt.Sprintf(".tmp-v%d-%d", next, time.Now().UnixNano()))
	if err := ops.copyTree(draft, tmpVersion); err != nil {
		_ = ops.removeAll(tmpVersion)
		return 0, err
	}
	if err := ops.rename(tmpVersion, v.versionDir(name, next)); err != nil {
		_ = ops.removeAll(tmpVersion)
		return 0, err
	}
	rollbackVersion := func() {
		_ = ops.removeAll(v.versionDir(name, next))
	}
	stat, err := v.statBetween(name, m.Current, draft)
	if err != nil {
		rollbackVersion()
		return 0, err
	}
	prevRaw, prevErr := os.ReadFile(v.manifestPath(name))
	published := SkillVersionManifest{
		Name:    m.Name,
		Current: next,
		Versions: append(append([]VersionRecord(nil), m.Versions...), VersionRecord{
			V:           next,
			At:          time.Now().UTC().Format(time.RFC3339),
			Kind:        VersionKindPublished,
			Source:      latestSource(m),
			UpstreamRev: m.DraftRev,
			Note:        strings.TrimSpace(note),
			Stat:        stat,
		}),
	}
	if m.DraftRev != "" {
		published.Versions[len(published.Versions)-1].Kind = VersionKindUpdated
	}
	if err := v.writeManifest(published); err != nil {
		rollbackVersion()
		return 0, err
	}
	restoreManifest := func() {
		if prevErr != nil {
			// There was no manifest before (a first publish); removing the
			// new one restores that exactly.
			_ = ops.removeAll(v.manifestPath(name))
			return
		}
		tmp := v.manifestPath(name) + ".rollback"
		if err := ops.writeFile(tmp, prevRaw); err == nil {
			_ = ops.rename(tmp, v.manifestPath(name))
		}
	}

	// --- step 2: swap the formal directory for a copy of the new version.
	formal := v.formal(name)
	tmpFormal := filepath.Join(v.Root, fmt.Sprintf(".tmp-formal-%s-%d", name, time.Now().UnixNano()))
	if err := ops.copyTree(v.versionDir(name, next), tmpFormal); err != nil {
		_ = ops.removeAll(tmpFormal)
		restoreManifest()
		rollbackVersion()
		return 0, err
	}
	oldDir := ""
	formalExisted := false
	if info, statErr := os.Stat(formal); statErr == nil && info.IsDir() {
		formalExisted = true
		oldDir = filepath.Join(v.historyDir(name), fmt.Sprintf(".old-%d", time.Now().UnixNano()))
		if err := ops.rename(formal, oldDir); err != nil {
			_ = ops.removeAll(tmpFormal)
			restoreManifest()
			rollbackVersion()
			return 0, err
		}
	}
	if err := ops.rename(tmpFormal, formal); err != nil {
		_ = ops.removeAll(tmpFormal)
		if formalExisted {
			// Put the previous content back before anything else observes
			// the gap.
			_ = ops.rename(oldDir, formal)
		}
		restoreManifest()
		rollbackVersion()
		return 0, err
	}

	// --- step 3: the draft and the replaced directory are no longer needed.
	// These are best-effort: the new version is already current, and a
	// leftover .old- directory or draft is a wart the next publish clears,
	// not a broken skill.
	_ = ops.removeAll(draft)
	if formalExisted {
		_ = ops.removeAll(oldDir)
	}
	return next, nil
}

// latestSource carries the install source forward through published versions:
// a skill installed from a repo keeps naming that repo in every record, so the
// pull path can always find where to pull from.
func latestSource(m SkillVersionManifest) string {
	for i := len(m.Versions) - 1; i >= 0; i-- {
		if src := strings.TrimSpace(m.Versions[i].Source); src != "" {
			return src
		}
	}
	return ""
}

// Discard deletes the skill's draft. The published version — the thing
// sessions load — is untouched; only the unpubished changes go.
func (v VersionStore) Discard(name string) error {
	m, err := v.readManifest(name)
	if err != nil {
		return err
	}
	if err := v.o().removeAll(v.draftDir(name)); err != nil {
		return err
	}
	if m.DraftBase != 0 || m.DraftRev != "" {
		m.DraftBase = 0
		m.DraftRev = ""
		if err := v.writeManifest(m); err != nil {
			return err
		}
	}
	return nil
}

// RestoreAsDraft replaces the skill's draft with a copy of one published
// version. The caller decides whether an existing draft may be replaced; this
// method always does, because the confirmation question belongs to the
// surface asking it.
func (v VersionStore) RestoreAsDraft(name string, version int) error {
	name = strings.TrimSpace(name)
	if version <= 0 {
		return fmt.Errorf("version must be positive")
	}
	src := v.versionDir(name, version)
	if info, err := os.Stat(src); err != nil || !info.IsDir() {
		return fmt.Errorf("skill %q has no v%d", name, version)
	}
	m, err := v.readManifest(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(v.Root, draftsDirName), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(v.Root, draftsDirName, fmt.Sprintf(".tmp-%s-%d", name, time.Now().UnixNano()))
	if err := v.o().copyTree(src, tmp); err != nil {
		_ = v.o().removeAll(tmp)
		return err
	}
	_ = v.o().removeAll(v.draftDir(name))
	if err := v.o().rename(tmp, v.draftDir(name)); err != nil {
		_ = v.o().removeAll(tmp)
		return err
	}
	m.DraftBase = version
	m.DraftRev = ""
	return v.writeManifest(m)
}

// Diff compares two snapshots of a skill: from and to each name a published
// version ("v3") or the draft ("draft"). The answer is one row per file that
// differs, sorted by path.
func (v VersionStore) Diff(name string, from, to string) ([]FileDiff, error) {
	fromDir, err := v.snapshotDir(name, from)
	if err != nil {
		return nil, err
	}
	toDir, err := v.snapshotDir(name, to)
	if err != nil {
		return nil, err
	}
	before, err := readTreeLines(fromDir)
	if err != nil {
		return nil, err
	}
	after, err := readTreeLines(toDir)
	if err != nil {
		return nil, err
	}
	paths := make(map[string]struct{}, len(before)+len(after))
	for path := range before {
		paths[path] = struct{}{}
	}
	for path := range after {
		paths[path] = struct{}{}
	}
	out := make([]FileDiff, 0, len(paths))
	for path := range paths {
		oldLines, hadOld := before[path]
		newLines, hadNew := after[path]
		switch {
		case hadOld && !hadNew:
			out = append(out, FileDiff{Path: path, Status: "removed", Removed: len(oldLines)})
		case !hadOld && hadNew:
			out = append(out, FileDiff{Path: path, Status: "added", Added: len(newLines)})
		default:
			added, removed := lineDiffCounts(oldLines, newLines)
			if added == 0 && removed == 0 {
				continue
			}
			out = append(out, FileDiff{Path: path, Status: "modified", Added: added, Removed: removed})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (v VersionStore) snapshotDir(name, ref string) (string, error) {
	ref = strings.TrimSpace(strings.ToLower(ref))
	if ref == "draft" {
		dir := v.draftDir(name)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return "", fmt.Errorf("skill %q has no draft", name)
		}
		return dir, nil
	}
	var version int
	if _, err := fmt.Sscanf(ref, "v%d", &version); err != nil || version <= 0 {
		return "", fmt.Errorf("version reference must be \"v<N>\" or \"draft\", got %q", ref)
	}
	dir := v.versionDir(name, version)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("skill %q has no %s", name, ref)
	}
	return dir, nil
}

// ReadVersionFile reads one file out of one published version, with the same
// path validation an archive entry goes through: rel may not climb out of the
// version directory, and symlinks are not followed.
func (v VersionStore) ReadVersionFile(name string, version int, rel string) ([]byte, error) {
	if version <= 0 {
		return nil, fmt.Errorf("version must be positive")
	}
	if err := home.ValidateArchiveRelPath(rel); err != nil {
		return nil, fmt.Errorf("%w: %q", err, rel)
	}
	dir := v.versionDir(name, version)
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	resolved, err := home.ResolveWithinRoots(abs, []string{dir})
	if err != nil {
		return nil, fmt.Errorf("%w: %q", err, rel)
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", rel)
	}
	return os.ReadFile(resolved)
}

// Bootstrap records a skill that already exists on disk as v1. It is
// idempotent — a skill that already has a history is left alone — and never
// touches the formal directory. rec.V is ignored; rec.At, when zero, becomes
// the formal directory's own modification time, which is the closest thing to
// a creation time the filesystem kept.
func (v VersionStore) Bootstrap(name string, rec VersionRecord) (int, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("skill name required")
	}
	if info, err := os.Stat(v.historyDir(name)); err == nil && info.IsDir() {
		return 0, nil
	}
	formal := v.formal(name)
	info, err := os.Stat(formal)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("%s is not a skill directory", formal)
	}
	stat, err := v.statBetween(name, 0, formal)
	if err != nil {
		return 0, err
	}
	rec.V = 1
	if strings.TrimSpace(rec.At) == "" {
		rec.At = info.ModTime().UTC().Format(time.RFC3339)
	}
	if strings.TrimSpace(rec.Kind) == "" {
		rec.Kind = VersionKindCreated
	}
	rec.Stat = stat
	if err := os.MkdirAll(v.historyDir(name), 0o755); err != nil {
		return 0, err
	}
	tmp := filepath.Join(v.historyDir(name), fmt.Sprintf(".tmp-v1-%d", time.Now().UnixNano()))
	if err := v.o().copyTree(formal, tmp); err != nil {
		_ = v.o().removeAll(tmp)
		return 0, err
	}
	if err := v.o().rename(tmp, v.versionDir(name, 1)); err != nil {
		_ = v.o().removeAll(tmp)
		return 0, err
	}
	m := SkillVersionManifest{
		Name:     name,
		Current:  1,
		Versions: []VersionRecord{rec},
	}
	// The manifest lands last: until it does, the formal directory is the
	// only copy and nothing has claimed otherwise.
	if err := v.writeManifest(m); err != nil {
		_ = v.o().removeAll(v.versionDir(name, 1))
		return 0, err
	}
	return 1, nil
}

// statBetween summarizes the tree at dir against the version before it: how
// many files it holds and how many lines moved relative to version (0 means
// against nothing, so every file counts as added in full).
func (v VersionStore) statBetween(name string, version int, dir string) (VersionStat, error) {
	after, err := readTreeLines(dir)
	if err != nil {
		return VersionStat{}, err
	}
	stat := VersionStat{Files: len(after)}
	before := map[string][]string{}
	if version > 0 {
		prev, err := readTreeLines(v.versionDir(name, version))
		if err != nil {
			if !os.IsNotExist(err) {
				return VersionStat{}, err
			}
			prev = map[string][]string{}
		}
		before = prev
	}
	for path, lines := range after {
		if old, ok := before[path]; ok {
			added, removed := lineDiffCounts(old, lines)
			stat.Added += added
			stat.Removed += removed
			continue
		}
		stat.Added += len(lines)
	}
	for path, lines := range before {
		if _, ok := after[path]; !ok {
			stat.Removed += len(lines)
		}
	}
	return stat, nil
}

// CurrentVersion reports the published version of the skill directory name
// under root, and where its history lives. A skill with no manifest — a
// foreign layer, a bundle member, anything this store does not manage —
// answers 0 with an empty root, which every reader takes as "read the formal
// directory".
func CurrentVersion(root, name string) (version int, versionsRoot string) {
	root = strings.TrimSpace(root)
	name = strings.TrimSpace(name)
	if root == "" || name == "" {
		return 0, ""
	}
	m, err := (VersionStore{Root: root}).readManifest(name)
	if err != nil || m.Current <= 0 {
		return 0, ""
	}
	return m.Current, filepath.Join(root, versionsDirName, name)
}

// CurrentVersionFor resolves the published version of one discovered skill
// directory against the roots it may live under. A skill that is a direct
// child of a root carries that root's version history; a bundle member, a
// cross-tool directory or a built-in — anything that is not a versioned
// layer's own skill — answers 0 with an empty root, which readers take as
// "read the formal directory".
func CurrentVersionFor(roots []string, skillDir string) (version int, versionsRoot string) {
	skillDir = CanonicalSkillPath(skillDir)
	if skillDir == "" {
		return 0, ""
	}
	base := filepath.Base(skillDir)
	for _, root := range roots {
		root = CanonicalSkillPath(root)
		if root == "" || filepath.Dir(skillDir) != root {
			continue
		}
		return CurrentVersion(root, base)
	}
	return 0, ""
}

// VersionedSkillRoots lists the skill roots whose skills carry versions and
// drafts: the project's own .forebrain/skills, the primary agent's skills
// directory, and the shared home skills — highest priority first, empty
// pieces skipped, duplicates collapsed. The other roots forebrain reads
// (cross-tool user directories, the .agents/.claude/.codex project variants,
// the built-in layer) belong to other tools or ship read-only, so writes
// aimed at them are not redirected and no hidden directory is created in
// them.
func VersionedSkillRoots(home, workspaceRoot, projectRoot string) []string {
	layers := VersionedSkillLayers(home, workspaceRoot, projectRoot)
	out := make([]string, 0, len(layers))
	for _, layer := range layers {
		out = append(out, layer.Path)
	}
	return out
}

// VersionedSkillLayers is VersionedSkillRoots with each root's layer, so a
// listing can say where a versioned skill lives in the same breath as its
// version state.
func VersionedSkillLayers(home, workspaceRoot, projectRoot string) []Root {
	var out []Root
	seen := map[string]struct{}{}
	add := func(dir string, origin Origin) {
		dir = CanonicalSkillPath(dir)
		if dir == "" {
			return
		}
		if _, ok := seen[dir]; ok {
			return
		}
		seen[dir] = struct{}{}
		out = append(out, Root{Path: dir, Origin: origin})
	}
	if root := strings.TrimSpace(projectRoot); root != "" {
		add(filepath.Join(CanonicalSkillPath(root), ".forebrain", "skills"), OriginProject)
	}
	if root := strings.TrimSpace(workspaceRoot); root != "" {
		add(filepath.Join(root, "skills"), OriginAgent)
	}
	if root := strings.TrimSpace(home); root != "" {
		add(filepath.Join(root, "skills"), OriginShared)
	}
	return out
}

// Draft-target redirection (the write side of "every change goes to the
// draft").

var (
	// ErrVersionStoreReadOnly is the refusal a write into .versions meets:
	// published versions are history, and history is changed by publishing a
	// new version, never by editing an old one in place.
	ErrVersionStoreReadOnly = errors.New("version records are read-only: publish a new version or restore one as a draft instead")

	// ErrSkillDirDeletion is the refusal a whole-directory operation on a
	// skill meets: deleting a skill touches its versions and its toggle
	// state too, which is the skills list's decision to make, not a file
	// write's.
	ErrSkillDirDeletion = errors.New("the skill directory itself is managed by the skills list: edit files inside it, or delete the skill from there")

	// ErrDraftNotPublished is the refusal a toggle on a never-published
	// skill meets: a draft is not loaded anywhere, so an enable or disable
	// would promise a change no session can observe. Publishing is what
	// makes a skill loadable, and publishing enables it.
	ErrDraftNotPublished = errors.New("a draft has no published version to enable: publish it first")
)

// DraftTarget is where a write aimed at a skill lands: the draft path, and
// the formal path it was aimed at.
type DraftTarget struct {
	// SkillName is the skill's directory name under its root.
	SkillName string
	// Target is the path the write lands on (the draft path, or the
	// unchanged path when it already was inside .drafts).
	Target string
	// Source is the formal path the write addressed, empty when the write
	// addressed the draft directly.
	Source string
	// Root is the skill root the skill lives under.
	Root string
}

// ResolveDraftTarget settles one write path against the versioned skill
// roots. handled is false when the path is nobody's business here — an
// ordinary path outside every skill root, or inside another dot-directory
// such as the built-in layer — and the caller resolves it by its ordinary
// rules. An error is a refusal: the path names something in .versions, or the
// skill directory itself.
//
// The draft is created here for a path that needs one: the caller is about to
// write, and the write lands in a draft that holds the current version's
// content. Concurrency is the same answer EnsureDraft gives — one creator
// wins, the rest receive the winner.
func ResolveDraftTarget(roots []string, path string) (target DraftTarget, handled bool, err error) {
	abs := path
	if resolved, absErr := filepath.Abs(strings.TrimSpace(path)); absErr == nil {
		abs = filepath.Clean(resolved)
	}
	// The roots are canonical (symlink-resolved) spellings, so the write
	// path must be too: a session holding /var/... next to a root resolved
	// to /private/var/... is a workspace symlink away from being missed,
	// and a missed root means a write straight into the formal directory.
	if canon := CanonicalSkillPath(abs); canon != "" {
		abs = canon
	}
	for _, root := range roots {
		root = CanonicalSkillPath(root)
		if root == "" {
			continue
		}
		rel, relErr := filepath.Rel(root, abs)
		if relErr != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		segments := strings.Split(rel, string(filepath.Separator))
		first := segments[0]
		switch {
		case first == versionsDirName:
			return DraftTarget{}, true, fmt.Errorf("%w: %s", ErrVersionStoreReadOnly, abs)
		case first == draftsDirName:
			return DraftTarget{SkillName: draftLeaf(segments), Target: abs, Root: root}, true, nil
		case strings.HasPrefix(first, "."):
			// Another hidden directory (the built-in layer, a foreign
			// tool's): not versioned, not ours to redirect.
			return DraftTarget{}, false, nil
		}
		if len(segments) == 1 {
			return DraftTarget{}, true, fmt.Errorf("%w: %s", ErrSkillDirDeletion, abs)
		}
		name := first
		rest := filepath.Join(segments[1:]...)
		store := VersionStore{Root: root}
		draft, ensureErr := store.EnsureDraft(name)
		if ensureErr != nil {
			return DraftTarget{}, true, ensureErr
		}
		return DraftTarget{
			SkillName: name,
			Target:    filepath.Join(draft, rest),
			Source:    filepath.Join(root, name, rest),
			Root:      root,
		}, true, nil
	}
	return DraftTarget{}, false, nil
}

func draftLeaf(segments []string) string {
	if len(segments) > 1 {
		return segments[1]
	}
	return ""
}

// copySkillTree copies one skill directory tree. File permissions are kept
// (a script the skill ships must stay executable); symbolic links and .git
// directories are left behind — a link is a path someone else chose, and the
// install transport is not skill content.
func copySkillTree(src, dst string) error {
	src = filepath.Clean(src)
	dst = filepath.Clean(dst)
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			info, infoErr := d.Info()
			if infoErr != nil {
				return infoErr
			}
			return os.MkdirAll(filepath.Join(dst, rel), info.Mode().Perm())
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		target := filepath.Join(dst, rel)
		f, openErr := home.OpenNoFollowForWrite(target, info.Mode().Perm())
		if openErr != nil {
			return openErr
		}
		_, writeErr := f.Write(raw)
		cerr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		return cerr
	})
}

// readTreeLines reads a directory tree into a path → line-slice map. The
// paths are slash-separated and relative to the root; dot files, dot
// directories and .git are skipped, exactly the set discovery and the
// resource listing already hide.
func readTreeLines(root string) (map[string][]string, error) {
	out := map[string][]string{}
	root = strings.TrimSpace(root)
	if root == "" {
		return out, nil
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || rel == "." {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		out[filepath.ToSlash(rel)] = splitLines(raw)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func splitLines(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	return lines
}

// lineDiffLimit caps the dynamic-programming table: beyond it the counts
// degrade to whole-file added+removed, which keeps the summary bounded on a
// pathologically large skill instead of costing memory in the middle of a
// publish.
const lineDiffLimit = 20000

// lineDiffCounts counts added and removed lines between two line slices with
// a longest-common-subsequence table on the slices themselves.
func lineDiffCounts(oldLines, newLines []string) (added, removed int) {
	if equalLines(oldLines, newLines) {
		return 0, 0
	}
	if len(oldLines)*len(newLines) > lineDiffLimit*lineDiffLimit/64 {
		return len(newLines), len(oldLines)
	}
	// lcs[i][j] is the LCS length of oldLines[i:] and newLines[j:], computed
	// row by row from the ends.
	prev := make([]int, len(newLines)+1)
	cur := make([]int, len(newLines)+1)
	for i := len(oldLines) - 1; i >= 0; i-- {
		for j := len(newLines) - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				cur[j] = prev[j+1] + 1
			} else if prev[j] >= cur[j+1] {
				cur[j] = prev[j]
			} else {
				cur[j] = cur[j+1]
			}
		}
		prev, cur = cur, prev
	}
	common := prev[0]
	return len(newLines) - common, len(oldLines) - common
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
