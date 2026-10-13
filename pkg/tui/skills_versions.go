// Skill drafts and versions on the TUI side (plan 037): the thin wrappers the
// /skills panel drives the version store through, and the presentation helpers
// that turn a skill's version state into its status segment, its version
// history page, and a colored diff body. Everything here goes through the Go
// API — pkg/skill's VersionStore and Service — never over HTTP: the gateway
// serves the same operations, but the TUI is an in-process surface.
package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// The two hidden directories the version store keeps beside a skill root. They
// mirror pkg/skill's unexported names; the panel needs to address a draft
// directory and a skill's layer without importing an engine that owns the
// layout, so the two spellings sit here as the store's public shape.
const (
	tuiSkillDraftsDir   = ".drafts"
	tuiSkillVersionsDir = ".versions"
)

// skillUpdateProbeTimeout bounds the one network step the panel can trigger:
// the cached upstream-head probe behind "update available". The probe is
// cached for an hour upstream, and a source that cannot be reached leaves the
// skill without a marker rather than stalling the panel.
const skillUpdateProbeTimeout = 2 * time.Second

// SkillFileDiff is one file's place in a draft/version comparison the panel
// shows: the store's counts plus the unified-diff body itself, which the store
// does not carry. Text is empty when the file did not change in either
// direction (it never is — the store's Diff lists only changed files).
type SkillFileDiff struct {
	Path    string
	Status  string
	Added   int
	Removed int
	Text    string
}

// skillRootAndName splits an entry's path into the skill root that owns its
// versions and the skill's own directory name. A draft-only skill's path is
// <root>/.drafts/<name>; a published skill's is <root>/<name>. draftOnly
// reports which shape the path had.
func skillRootAndName(path string) (root, name string, draftOnly bool) {
	p := filepath.Clean(strings.TrimSpace(path))
	if p == "" || p == "." {
		return "", "", false
	}
	name = filepath.Base(p)
	if filepath.Base(filepath.Dir(p)) == tuiSkillDraftsDir {
		return filepath.Dir(filepath.Dir(p)), name, true
	}
	return filepath.Dir(p), name, false
}

// SkillVersionState reports one entry's draft-and-version standing. The second
// value is false when the entry carries no version history at all — a
// cross-tool directory, a bundle member, a built-in — so a caller can tell
// "no versions" from "not versioned".
func (s *ChatSession) SkillVersionState(entry skill.Entry) (skill.SkillVersionState, bool) {
	root, name, _ := skillRootAndName(entry.Path)
	if root == "" || name == "" {
		return skill.SkillVersionState{}, false
	}
	store := skill.VersionStore{Root: root}
	state, err := store.State(name)
	if err != nil {
		return skill.SkillVersionState{}, false
	}
	if state.StateLabel() == "" {
		return state, false
	}
	state.Update = s.skillUpdateMarker(name, state)
	return state, true
}

// skillUpdateMarker fills the "update available" marker the way the service's
// own withUpdate does: a git-installed skill whose source advertises a newer
// revision than the one its current version recorded. It is duplicated here
// rather than reached through the service because the panel needs the state
// without paying for a whole listing, and the probe is cached.
func (s *ChatSession) skillUpdateMarker(name string, state skill.SkillVersionState) *skill.SkillUpdate {
	if state.Current == 0 || len(state.Versions) == 0 {
		return nil
	}
	latest := state.Versions[len(state.Versions)-1]
	repo, rev := strings.TrimSpace(latest.Source), strings.TrimSpace(latest.UpstreamRev)
	if repo == "" || rev == "" {
		if s != nil {
			if entry, ok := skill.LookupLockEntry(s.skillLifecycleService().Workspace(), name); ok && strings.TrimSpace(entry.SourceRef) != "" {
				repo = strings.TrimSpace(entry.SourceRef)
				if rev == "" {
					rev = strings.TrimSpace(entry.Revision)
				}
			}
		}
	}
	if repo == "" || rev == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), skillUpdateProbeTimeout)
	defer cancel()
	remote, ok := skill.RemoteHead(ctx, repo)
	if !ok || remote == "" || remote == rev {
		return nil
	}
	return &skill.SkillUpdate{Rev: remote}
}

// PublishSkill releases an entry's draft as its next version. It is the one
// operation that writes the formal directory, and the service enables the
// skill as it publishes.
func (s *ChatSession) PublishSkill(entry skill.Entry, note string) (int, error) {
	if s == nil || strings.TrimSpace(s.home()) == "" {
		return 0, fmt.Errorf("skills: unavailable")
	}
	return s.skillLifecycleService().PublishSkill(strings.TrimSpace(entry.Name), strings.TrimSpace(note))
}

// DiscardSkillDraft drops an entry's unpublished changes; a never-published
// skill loses everything it has.
func (s *ChatSession) DiscardSkillDraft(entry skill.Entry) error {
	if s == nil || strings.TrimSpace(s.home()) == "" {
		return fmt.Errorf("skills: unavailable")
	}
	return s.skillLifecycleService().DiscardDraft(strings.TrimSpace(entry.Name))
}

// RestoreSkillVersion copies a published version into the entry's draft,
// replacing an existing draft when replaceDraft says so.
func (s *ChatSession) RestoreSkillVersion(entry skill.Entry, version int, replaceDraft bool) error {
	if s == nil || strings.TrimSpace(s.home()) == "" {
		return fmt.Errorf("skills: unavailable")
	}
	return s.skillLifecycleService().RestoreVersionAsDraft(strings.TrimSpace(entry.Name), version, replaceDraft)
}

// PullSkillUpdate fetches the entry's upstream content into its draft.
func (s *ChatSession) PullSkillUpdate(entry skill.Entry, replaceDraft bool) error {
	if s == nil || strings.TrimSpace(s.home()) == "" {
		return fmt.Errorf("skills: unavailable")
	}
	return s.skillLifecycleService().PullUpdate(context.Background(), strings.TrimSpace(entry.Name), replaceDraft)
}

// SkillDiff compares two snapshots of one entry — published versions by number
// ("v3") and the draft ("draft") — returning each file's counts and its
// unified-diff body. An empty from compares the draft against nothing, which
// is how a never-published skill's changes read.
func (s *ChatSession) SkillDiff(entry skill.Entry, from, to string) ([]SkillFileDiff, error) {
	root, name, _ := skillRootAndName(entry.Path)
	if root == "" || name == "" {
		return nil, fmt.Errorf("skills: cannot resolve %q", strings.TrimSpace(entry.Path))
	}
	draftDir := filepath.Join(root, tuiSkillDraftsDir, name)
	if strings.TrimSpace(from) == "" {
		return draftTreeDiff(draftDir), nil
	}
	store := skill.VersionStore{Root: root}
	fileDiffs, err := store.Diff(name, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]SkillFileDiff, 0, len(fileDiffs))
	for _, fd := range fileDiffs {
		d := SkillFileDiff{Path: fd.Path, Status: fd.Status, Added: fd.Added, Removed: fd.Removed}
		before := readSkillRefFile(store, name, from, draftDir, fd.Path)
		after := readSkillRefFile(store, name, to, draftDir, fd.Path)
		if summary, berr := event.Build(filepath.Base(fd.Path), before, after); berr == nil {
			d.Text = summary.UnifiedDiff
		}
		out = append(out, d)
	}
	return out, nil
}

// DraftOnlySkills lists the skills that exist only as a draft — never
// published, so invisible to discovery and absent from every formal-directory
// scan. They are returned as entries whose Path is the draft directory, ready
// to merge into the panel's list beside the published skills.
func (s *ChatSession) DraftOnlySkills() []skill.Entry {
	if s == nil || strings.TrimSpace(s.home()) == "" {
		return nil
	}
	svc := s.skillLifecycleService()
	layers := skill.VersionedSkillLayers(svc.Home, svc.Workspace(), svc.ProjectRoot)
	var out []skill.Entry
	for _, layer := range layers {
		store := skill.VersionStore{Root: layer.Path}
		entries, err := os.ReadDir(filepath.Join(layer.Path, tuiSkillDraftsDir))
		if err != nil {
			continue
		}
		for _, dir := range entries {
			leaf := strings.TrimSpace(dir.Name())
			if leaf == "" || strings.HasPrefix(leaf, ".") || !dir.IsDir() {
				continue
			}
			state, serr := store.State(leaf)
			if serr != nil || state.Current > 0 || !state.HasDraft {
				continue
			}
			desc := ""
			if parsed, perr := skill.Parse(strings.NewReader(string(mustReadSkillBytes(filepath.Join(state.DraftDir, "SKILL.md"))))); perr == nil && parsed != nil {
				desc = strings.TrimSpace(parsed.Description)
			}
			out = append(out, skill.Entry{
				Name:        leaf,
				Description: desc,
				Path:        state.DraftDir,
				Origin:      layer.Origin,
				Enabled:     false,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if li != lj {
			return li < lj
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func mustReadSkillBytes(path string) []byte {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return raw
}

// readSkillRefFile reads one file out of a snapshot named by ref: the draft
// directory for "draft", a version directory for "vN". A missing file answers
// empty bytes — that is the "added" or "removed" side of a file that only
// exists on one side of the comparison.
func readSkillRefFile(store skill.VersionStore, name, ref, draftDir, rel string) []byte {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(ref), "draft") {
		raw, _ := os.ReadFile(filepath.Join(draftDir, filepath.FromSlash(rel)))
		return raw
	}
	var version int
	if _, err := fmt.Sscanf(strings.ToLower(strings.TrimSpace(ref)), "v%d", &version); err != nil || version <= 0 {
		return nil
	}
	raw, _ := store.ReadVersionFile(name, version, rel)
	return raw
}

// draftTreeDiff reads a draft directory as a never-published skill's changes:
// every file it holds is an addition against nothing.
func draftTreeDiff(draftDir string) []SkillFileDiff {
	var files []string
	_ = filepath.WalkDir(draftDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if rel, rerr := filepath.Rel(draftDir, path); rerr == nil {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(files)
	out := make([]SkillFileDiff, 0, len(files))
	for _, rel := range files {
		raw, err := os.ReadFile(filepath.Join(draftDir, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		summary, _ := event.Build(filepath.Base(rel), nil, raw)
		out = append(out, SkillFileDiff{
			Path:   rel,
			Status: "added",
			Added:  countLines(raw),
			Text:   summary.UnifiedDiff,
		})
	}
	return out
}

// countLines is the line count a diff stat shows for a whole file.
func countLines(raw []byte) int {
	text := strings.TrimRight(string(raw), "\n")
	if text == "" {
		return 0
	}
	return len(strings.Split(text, "\n"))
}

// --- status segments (design 3.1) ---

// skillStatus is one listed skill's version standing, distilled from
// SkillVersionState for the row and the submenu. known is false for a skill
// the version store does not manage; builtin marks the read-only built-in
// layer, which has no versions of its own.
type skillStatus struct {
	known    bool
	builtin  bool
	label    string
	version  int
	versions int
	hasDraft bool
	update   *skill.SkillUpdate
}

// skillStatusFor folds a resolved version state into a skillStatus, and marks
// the built-in layer, which never carries one.
func skillStatusFor(entry skill.Entry, state skill.SkillVersionState, ok bool) skillStatus {
	if entry.Origin == skill.OriginBuiltin {
		return skillStatus{known: true, builtin: true}
	}
	if !ok {
		return skillStatus{}
	}
	return skillStatus{
		known:    true,
		label:    state.StateLabel(),
		version:  state.Current,
		versions: len(state.Versions),
		hasDraft: state.HasDraft,
		update:   state.Update,
	}
}

// skillStatusPrefix is the status segment a row opens with, colored by role:
// the version number dim, "Draft"/"unpublished changes" in the warning color,
// the built-in layer in the glyph color. It ends in " · " so the description
// follows it directly.
func skillStatusPrefix(entry skill.Entry, st skillStatus) string {
	switch {
	case st.builtin:
		return panelGlyphStyle.Render("Built-in") + " · "
	case st.label == skill.SkillStateDraft:
		return panelWarnStyle.Render("Draft") + " · "
	case st.label == skill.SkillStatePending:
		return panelGlyphStyle.Render(fmt.Sprintf("v%d", st.version)) + " · " + panelWarnStyle.Render("unpublished changes") + " · "
	case st.label == skill.SkillStatePublished:
		return panelGlyphStyle.Render(fmt.Sprintf("v%d", st.version)) + " · "
	}
	return ""
}

// skillRowDescriptionWithStatus is skillRowDescription with the version status
// segment in front and the "update available" clause after the description.
// The description still wraps in full; a never-published draft carries no
// "(off)" because a draft is not loaded anywhere to be turned off.
func skillRowDescriptionWithStatus(entry skill.Entry, origins map[string]skill.Origin, withState bool, st skillStatus) string {
	parts := make([]string, 0, 3)
	head := skillStatusPrefix(entry, st)
	if withState && !entry.Enabled && st.label != skill.SkillStateDraft {
		head += "(off) "
	}
	head += strings.TrimSpace(entry.Description)
	if st.update != nil {
		head += " · " + panelAccentStyle.Render("update available")
	}
	if head != "" {
		parts = append(parts, head)
	}
	switch {
	case len(entry.ShadowedBy) > 0:
		parts = append(parts, "· shadowed by the "+origins[entry.ShadowedBy[0]].Label()+" copy")
	case len(entry.Shadows) == 1:
		parts = append(parts, "· shadows the "+origins[entry.Shadows[0]].Label()+" copy")
	case len(entry.Shadows) > 1:
		parts = append(parts, "· shadows "+fmt.Sprintf("%d", len(entry.Shadows))+" other copies")
	}
	return strings.Join(parts, " ")
}

// --- version history (design 3.5) ---

// skillVersionPageSize is how many history rows the panel loads at once; more
// load as the cursor reaches the bottom.
const skillVersionPageSize = 20

// versionRowKind distinguishes the history rows a commit can act on.
type versionRowKind int

const (
	versionRowDraft versionRowKind = iota
	versionRowPublished
)

// versionRow is one line of the version history picker.
type versionRow struct {
	kind    versionRowKind
	version int
}

// skillHistoryItems lays the version history out newest first: the draft row
// (when one exists) then every published version, newest leading. The returned
// rows are parallel to the items, so the picker's index maps to a row.
func (c *commandController) skillHistoryItems(entry skill.Entry, state skill.SkillVersionState) ([]SelectItem, []versionRow) {
	items := make([]SelectItem, 0, len(state.Versions)+1)
	rows := make([]versionRow, 0, len(state.Versions)+1)
	if state.HasDraft {
		items = append(items, SelectItem{Label: "Draft", Description: c.draftHistoryDescription(entry, state)})
		rows = append(rows, versionRow{kind: versionRowDraft, version: state.DraftBase})
	}
	for i := len(state.Versions) - 1; i >= 0; i-- {
		v := state.Versions[i]
		items = append(items, SelectItem{Label: fmt.Sprintf("v%d", v.V), Description: versionHistoryDescription(v, state.Current)})
		rows = append(rows, versionRow{kind: versionRowPublished, version: v.V})
	}
	return items, rows
}

// draftHistoryDescription is the draft row's second column: what it is based
// on and how many files it changes.
func (c *commandController) draftHistoryDescription(entry skill.Entry, state skill.SkillVersionState) string {
	if state.DraftBase <= 0 {
		return "Not published yet"
	}
	count := 0
	if diffs, err := c.session.SkillDiff(entry, fmt.Sprintf("v%d", state.DraftBase), "draft"); err == nil {
		count = len(diffs)
	}
	return fmt.Sprintf("Based on v%d · %s changed", state.DraftBase, fileCountText(count))
}

// versionHistoryDescription is one version row's second column: its date, its
// kind, its note and its stat, with "Current" when it is the live version.
func versionHistoryDescription(v skill.VersionRecord, current int) string {
	parts := make([]string, 0, 5)
	if v.V == current {
		parts = append(parts, "Current")
	}
	if at := formatVersionDate(v.At); at != "" {
		parts = append(parts, at)
	}
	parts = append(parts, versionKindLabel(v.Kind))
	if note := strings.TrimSpace(v.Note); note != "" {
		parts = append(parts, note)
	}
	parts = append(parts, statText(v.Stat.Added, v.Stat.Removed))
	return strings.Join(parts, " · ")
}

func versionKindLabel(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case skill.VersionKindCreated, "":
		return "Created"
	case skill.VersionKindInstalled:
		return "Installed"
	case skill.VersionKindPublished:
		return "Published"
	case skill.VersionKindUpdated:
		return "Updated"
	}
	if kind == "" {
		return "Created"
	}
	return strings.ToUpper(kind[:1]) + kind[1:]
}

// formatVersionDate renders a version's RFC3339 timestamp as "Oct 9 18:20",
// or "" when it cannot be parsed.
func formatVersionDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return ""
	}
	return t.Local().Format("Jan 2 15:04")
}

// fileCountText spells a file count for a menu description.
func fileCountText(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// statText is a diff stat clause: "+12 −3".
func statText(added, removed int) string {
	return fmt.Sprintf("+%d −%d", added, removed)
}

// previousVersion returns the version published immediately before v, or 0
// when v is the first.
func previousVersion(versions []skill.VersionRecord, v int) int {
	for i := range versions {
		if versions[i].V == v {
			if i == 0 {
				return 0
			}
			return versions[i-1].V
		}
	}
	return 0
}

// skillHistoryTitle is the version history picker's head.
func skillHistoryTitle(entry skill.Entry, state skill.SkillVersionState) string {
	name := strings.TrimSpace(entry.Name)
	if state.Current > 0 {
		return name + " · versions\n" + fmt.Sprintf("v%d is current", state.Current)
	}
	return name + " · versions"
}

// --- diff body (design 3.4) ---

// skillChangesBody renders a set of file diffs as the body of the "Show
// changes" page: a header line per file and the diff below it, in the same
// add/del colors the conversation's diff cards use. Every line opens with an
// SGR sequence, so the info panel's bullet handling leaves a leading "- " of a
// removed line alone.
func skillChangesBody(r *Renderer, diffs []SkillFileDiff) string {
	if len(diffs) == 0 {
		return "No changes."
	}
	pal := diffPaletteFor(r.DiffTheme())
	var b strings.Builder
	for i, d := range diffs {
		if i > 0 {
			b.WriteByte('\n')
		}
		header := fmt.Sprintf("▾ %s  %s   %d of %d", d.Path, statText(d.Added, d.Removed), i+1, len(diffs))
		b.WriteString("\x1b[1m" + header + "\x1b[0m")
		b.WriteByte('\n')
		for _, line := range strings.Split(strings.TrimRight(d.Text, "\n"), "\n") {
			b.WriteString(colorDiffLine(line, pal))
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// colorDiffLine paints one unified-diff line: added lines in the palette's
// add color, removed lines in its del color, file and hunk headers in the
// gutter color, and context lines plain. The leading SGR is what keeps the
// info panel from eating a removed line's marker.
func colorDiffLine(line string, pal diffPalette) string {
	if pal.noColor {
		return "\x1b[0m" + line
	}
	switch {
	case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "@@"):
		return pal.meta(line)
	case strings.HasPrefix(line, "+"):
		return "\x1b[38;5;" + pal.addMarker + "m" + line + "\x1b[0m"
	case strings.HasPrefix(line, "-"):
		return "\x1b[38;5;" + pal.delMarker + "m" + line + "\x1b[0m"
	default:
		return "\x1b[0m" + line
	}
}

// skillUpdateDescription is the "Pull the update into a draft" row's second
// column: the upstream revision, its date and its file count when known.
func skillUpdateDescription(u *skill.SkillUpdate) string {
	if u == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if rev := shortRevision(u.Rev); rev != "" {
		parts = append(parts, rev)
	}
	if d := strings.TrimSpace(u.Date); d != "" {
		parts = append(parts, d)
	}
	if u.Files > 0 {
		parts = append(parts, fileCountText(u.Files))
	}
	return strings.Join(parts, " · ")
}

func shortRevision(rev string) string {
	rev = strings.TrimSpace(rev)
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// --- the per-skill sheet (design 3.2) and its sub-flows ---

// skillEntryAction is what one row of the sheet does.
type skillEntryAction int

const (
	skillActRun skillEntryAction = iota
	skillActShowDetails
	skillActToggle
	skillActImprove
	skillActPublish
	skillActShowChanges
	skillActDiscard
	skillActHistory
	skillActPull
)

// skillMenuItem is one row of the sheet before it becomes a SelectItem.
type skillMenuItem struct {
	label  string
	desc   string
	action skillEntryAction
}

// skillEntryMenu lays the sheet out for where the skill stands, in the order
// the design fixes.
func (c *commandController) skillEntryMenu(entry skill.Entry, st skillStatus) []skillMenuItem {
	name := strings.TrimSpace(entry.Name)
	items := make([]skillMenuItem, 0, 8)
	if st.update != nil {
		items = append(items, skillMenuItem{"Pull the update into a draft", skillUpdateDescription(st.update), skillActPull})
	}
	switch {
	case st.builtin:
		items = append(items,
			skillMenuItem{"Run it now", "Load " + name + " into this turn and give it a task", skillActRun},
			toggleMenuItem(entry),
			skillMenuItem{"Show details", "Source, trust, path, and allowed tools", skillActShowDetails},
		)
	case st.label == skill.SkillStateDraft:
		items = append(items,
			skillMenuItem{"Publish v1", "Load it in new sessions, enabled", skillActPublish},
			skillMenuItem{"Show changes", c.draftChangesDescription(entry), skillActShowChanges},
			skillMenuItem{"Continue in workshop", "Pick up where the workshop left off", skillActImprove},
			skillMenuItem{"Discard draft", "Delete the draft", skillActDiscard},
		)
	case st.label == skill.SkillStatePending:
		items = append(items,
			skillMenuItem{fmt.Sprintf("Publish v%d", st.version+1), c.publishChangesDescription(entry, st.version), skillActPublish},
			skillMenuItem{"Show changes", fmt.Sprintf("Compare the draft with v%d", st.version), skillActShowChanges},
			skillMenuItem{"Discard draft", fmt.Sprintf("Keep v%d, drop the unpublished changes", st.version), skillActDiscard},
			skillMenuItem{"Run it now", fmt.Sprintf("Load v%d into this turn and give it a task", st.version), skillActRun},
			skillMenuItem{"Improve it", "Continue in the skill workshop", skillActImprove},
			skillMenuItem{"Version history", skillHistoryDescription(st.versions), skillActHistory},
			toggleMenuItem(entry),
			skillMenuItem{"Show details", "Source, trust, path, and allowed tools", skillActShowDetails},
		)
	default:
		items = append(items,
			skillMenuItem{"Run it now", runNowDescription(name, st), skillActRun},
			skillMenuItem{"Improve it", "Continue in the skill workshop", skillActImprove},
		)
		if st.versions > 0 {
			items = append(items, skillMenuItem{"Version history", skillHistoryDescription(st.versions), skillActHistory})
		}
		items = append(items,
			toggleMenuItem(entry),
			skillMenuItem{"Show details", "Source, trust, path, and allowed tools", skillActShowDetails},
		)
	}
	return items
}

func toggleMenuItem(entry skill.Entry) skillMenuItem {
	if entry.Enabled {
		return skillMenuItem{"Disable this skill", "Stop loading it in new sessions", skillActToggle}
	}
	return skillMenuItem{"Enable this skill", "Load it in new sessions", skillActToggle}
}

// skillEntryTitle is the sheet's head: the skill and its layer, then the
// status line — the toggle result when one was just applied, the row's own
// status and description otherwise.
func skillEntryTitle(entry skill.Entry, origins map[string]skill.Origin, st skillStatus, banner string) string {
	head := strings.TrimSpace(entry.Name) + " · " + entry.Origin.Label()
	second := banner
	if second == "" {
		second = skillRowDescriptionWithStatus(entry, origins, true, st)
	}
	return head + "\n" + second
}

// runNowDescription spells the "Run it now" row: it names the version a
// versioned skill loads, and the skill itself when there is no version.
func runNowDescription(name string, st skillStatus) string {
	if st.version > 0 {
		return fmt.Sprintf("Load v%d into this turn and give it a task", st.version)
	}
	return "Load " + name + " into this turn and give it a task"
}

// skillHistoryDescription names how many versions the history holds.
func skillHistoryDescription(n int) string {
	if n == 1 {
		return "1 version"
	}
	return fmt.Sprintf("%d versions · compare or restore", n)
}

// draftChangesDescription counts a never-published draft's files.
func (c *commandController) draftChangesDescription(entry skill.Entry) string {
	count := 0
	if diffs, err := c.session.SkillDiff(entry, "", "draft"); err == nil {
		count = len(diffs)
	}
	return fileCountText(count)
}

// publishChangesDescription counts the files a draft changes against the
// version it is based on.
func (c *commandController) publishChangesDescription(entry skill.Entry, base int) string {
	count := 0
	if diffs, err := c.session.SkillDiff(entry, fmt.Sprintf("v%d", base), "draft"); err == nil {
		count = len(diffs)
	}
	if count == 0 {
		return fmt.Sprintf("changes since v%d", base)
	}
	return fmt.Sprintf("%s changed since v%d", fileCountText(count), base)
}

// draftBaseRef is the snapshot a draft is compared against: its base version,
// or empty for a never-published draft.
func draftBaseRef(st skillStatus) string {
	if st.label == skill.SkillStatePending && st.version > 0 {
		return fmt.Sprintf("v%d", st.version)
	}
	return ""
}

// showSkillDetails opens the skill's details page: its version, source, path
// and allowed tools, from the same inspection Show details has always used.
func (c *commandController) showSkillDetails(entry skill.Entry, st skillStatus) {
	name := strings.TrimSpace(entry.Name)
	body := "Version: not versioned"
	switch {
	case st.builtin:
		body = "Version: built-in"
	case st.label == skill.SkillStateDraft:
		body = "Version: draft (not published)"
	case st.version > 0 && st.hasDraft:
		body = fmt.Sprintf("Version: v%d · unpublished changes", st.version)
	case st.version > 0:
		body = fmt.Sprintf("Version: v%d", st.version)
	}
	if detail, err := c.session.ApplySkillSelection(name); err == nil {
		body += "\n" + strings.TrimSpace(detail)
	}
	label := name + " · " + entry.Origin.Label()
	if info, ok := c.selector.(InfoOverlaySelector); ok {
		if err := info.ShowInfo(label, body); err != nil {
			c.renderer.PrintError(err)
		}
		return
	}
	c.renderFrameForSkills(label + "\n" + body)
}

// showSkillChanges opens the "Show changes" page: the diff between two
// snapshots of the skill, file by file, in the conversation's diff colors.
func (c *commandController) showSkillChanges(entry skill.Entry, from, to string) {
	name := strings.TrimSpace(entry.Name)
	label := name + " · " + to
	if strings.TrimSpace(from) != "" {
		label = name + " · " + from + " → " + to
	}
	diffs, err := c.session.SkillDiff(entry, from, to)
	if err != nil {
		c.renderer.PrintError(err)
		return
	}
	body := skillChangesBody(c.renderer, diffs)
	if info, ok := c.selector.(InfoOverlaySelector); ok {
		if err := info.ShowInfo(label, body); err != nil {
			c.renderer.PrintError(err)
		}
		return
	}
	c.renderFrameForSkills(label + "\n" + body)
}

// publishSkillFlow runs the publish confirm page, its optional release note
// and the publish itself. It reports whether the sheet should be left: a
// successful publish leaves with the success line, a cancel stays on the
// sheet, and "Show changes" stays after showing the page.
func (c *commandController) publishSkillFlow(entry skill.Entry, st skillStatus) (skillsOutcome, bool) {
	name := strings.TrimSpace(entry.Name)
	next := st.version + 1
	from := draftBaseRef(st)
	diffs, _ := c.session.SkillDiff(entry, from, "draft")
	for {
		label := fmt.Sprintf("Publish %s v%d?", name, next)
		if st.label == skill.SkillStatePending {
			label += fmt.Sprintf("\nNew sessions use v%d; sessions already running keep v%d.", next, st.version)
		} else {
			label += "\nNew sessions load it, enabled."
		}
		actions := []string{fmt.Sprintf("Publish v%d", next), "Show changes", "Cancel"}
		idx, ok, err := c.selector.Review(label, publishFacts(diffs, st), actions, 0)
		if err != nil {
			c.renderer.PrintError(err)
			return skillsDone(), true
		}
		if !ok {
			return skillsBack(), false
		}
		switch idx {
		case 0:
			note, entered, ierr := c.selector.Input(fmt.Sprintf("Release note for v%d (optional)", next), "")
			if ierr != nil {
				c.renderer.PrintError(ierr)
				return skillsDone(), true
			}
			if !entered {
				continue
			}
			version, perr := c.session.PublishSkill(entry, note)
			if perr != nil {
				c.renderer.PrintError(perr)
				return skillsDone(), true
			}
			line := panelOKStyle.Render(fmt.Sprintf("Published %s v%d · new sessions use it", name, version))
			return skillsNotice(line), true
		case 1:
			c.showSkillChanges(entry, from, "draft")
		default:
			return skillsBack(), false
		}
	}
}

// publishFacts is the publish confirm page's fact list: the header naming
// what the changes are against, then each changed file and its stat.
func publishFacts(diffs []SkillFileDiff, st skillStatus) []turn.StatusFact {
	header := "Files"
	if st.label == skill.SkillStatePending {
		header = fmt.Sprintf("Changes since v%d", st.version)
	}
	facts := []turn.StatusFact{{Label: header}}
	for _, d := range diffs {
		facts = append(facts, turn.StatusFact{Label: d.Path, Value: statText(d.Added, d.Removed)})
	}
	return facts
}

// discardDraftFlow confirms and drops the skill's unpublished changes. The
// confirm page opens on Cancel, so a stray Enter never discards anything. It
// reports whether the sheet should be left — a never-published skill that has
// just lost its draft has nothing left to show.
func (c *commandController) discardDraftFlow(entry skill.Entry, st skillStatus) (skillsOutcome, bool) {
	title := "Delete the draft?"
	if st.label == skill.SkillStatePending && st.version > 0 {
		title = fmt.Sprintf("Keep v%d, drop the unpublished changes?", st.version)
	}
	actions := []string{"Discard draft", "Cancel"}
	idx, ok, err := c.selector.Review(title, nil, actions, 1)
	if err != nil {
		c.renderer.PrintError(err)
		return skillsDone(), true
	}
	if !ok || idx != 0 {
		return skillsBack(), false
	}
	if derr := c.session.DiscardSkillDraft(entry); derr != nil {
		c.renderer.PrintError(derr)
		return skillsDone(), true
	}
	if st.label == skill.SkillStateDraft {
		return skillsDone(), true
	}
	return skillsOutcome{}, false
}

// pullUpdateFlow confirms and pulls the skill's upstream change into its
// draft.
func (c *commandController) pullUpdateFlow(entry skill.Entry, st skillStatus) (skillsOutcome, bool) {
	facts := make([]turn.StatusFact, 0, 2)
	if st.update != nil {
		facts = append(facts, turn.StatusFact{Label: "Upstream", Value: skillUpdateDescription(st.update)})
	}
	if st.hasDraft {
		facts = append(facts, turn.StatusFact{Label: "Draft", Value: "The current draft will be replaced."})
	}
	actions := []string{"Pull the update", "Cancel"}
	idx, ok, err := c.selector.Review("Pull the update into a draft?", facts, actions, 0)
	if err != nil {
		c.renderer.PrintError(err)
		return skillsDone(), true
	}
	if !ok || idx != 0 {
		return skillsBack(), false
	}
	if perr := c.session.PullSkillUpdate(entry, true); perr != nil {
		c.renderer.PrintError(perr)
		return skillsDone(), true
	}
	return skillsOutcome{}, false
}

// showSkillVersionHistory opens the version history page: the draft then the
// published versions, newest first, loaded a page at a time. It reports
// whether the sheet should be left — never, so the user can pick another
// version.
func (c *commandController) showSkillVersionHistory(entry skill.Entry) (skillsOutcome, bool) {
	state, ok := c.session.SkillVersionState(entry)
	if !ok {
		return skillsBack(), false
	}
	items, rows := c.skillHistoryItems(entry, state)
	if len(items) == 0 {
		return skillsBack(), false
	}
	page := items
	cursor := len(items)
	if _, paged := c.selector.(PagedRichSelector); paged && len(items) > skillVersionPageSize {
		page = items[:skillVersionPageSize]
		cursor = skillVersionPageSize
	}
	fetchMore := func() []SelectItem {
		if cursor >= len(items) {
			return nil
		}
		end := min(cursor+skillVersionPageSize, len(items))
		next := items[cursor:end]
		cursor = end
		return next
	}
	idx, chosen, err := selectSkillPaged(c.selector, skillHistoryTitle(entry, state), page, 0, fetchMore)
	if err != nil {
		c.renderer.PrintError(err)
		return skillsDone(), true
	}
	if !chosen || idx < 0 || idx >= len(rows) {
		return skillsBack(), false
	}
	row := rows[idx]
	if row.kind == versionRowDraft {
		c.showSkillChanges(entry, draftBaseRef(skillStatusFor(entry, state, true)), "draft")
		return skillsOutcome{}, false
	}
	return c.versionItemFlow(entry, state, row.version)
}

// versionItemFlow is the submenu a version history row opens: compare it,
// restore it as a draft when it is not the current version, or go back.
func (c *commandController) versionItemFlow(entry skill.Entry, state skill.SkillVersionState, v int) (skillsOutcome, bool) {
	name := strings.TrimSpace(entry.Name)
	var labels, kinds []string
	if v == state.Current {
		if prev := previousVersion(state.Versions, v); prev > 0 {
			labels = append(labels, fmt.Sprintf("Compare with v%d", prev))
			kinds = append(kinds, fmt.Sprintf("compare:%d", prev))
		}
		labels = append(labels, "Back")
		kinds = append(kinds, "back")
	} else {
		labels = append(labels, fmt.Sprintf("Compare with v%d", state.Current))
		kinds = append(kinds, fmt.Sprintf("compare:%d", state.Current))
		labels = append(labels, "Restore as a draft")
		kinds = append(kinds, "restore")
		labels = append(labels, "Back")
		kinds = append(kinds, "back")
	}
	items := make([]SelectItem, 0, len(labels))
	for _, label := range labels {
		items = append(items, SelectItem{Label: label})
	}
	idx, ok, err := c.selector.SelectRich(fmt.Sprintf("%s · v%d", name, v), items, 0)
	if err != nil {
		c.renderer.PrintError(err)
		return skillsDone(), true
	}
	if !ok || idx < 0 || idx >= len(kinds) {
		return skillsBack(), false
	}
	switch {
	case strings.HasPrefix(kinds[idx], "compare:"):
		otherVersion, _ := strconv.Atoi(strings.TrimPrefix(kinds[idx], "compare:"))
		from, to := fmt.Sprintf("v%d", v), fmt.Sprintf("v%d", otherVersion)
		if v > otherVersion {
			from, to = fmt.Sprintf("v%d", otherVersion), fmt.Sprintf("v%d", v)
		}
		c.showSkillChanges(entry, from, to)
		return skillsOutcome{}, false
	case kinds[idx] == "restore":
		return c.restoreVersionFlow(entry, state, v)
	}
	return skillsBack(), false
}

// restoreVersionFlow confirms restoring a published version as the draft.
func (c *commandController) restoreVersionFlow(entry skill.Entry, state skill.SkillVersionState, version int) (skillsOutcome, bool) {
	label := fmt.Sprintf("Restore v%d as a draft?", version)
	label += fmt.Sprintf("\nThe draft becomes v%d's content; publish it to make it current.", version)
	if state.HasDraft {
		label += "\nThe current draft will be replaced."
	}
	actions := []string{"Restore as a draft", "Cancel"}
	idx, ok, err := c.selector.Review(label, nil, actions, 0)
	if err != nil {
		c.renderer.PrintError(err)
		return skillsDone(), true
	}
	if !ok || idx != 0 {
		return skillsBack(), false
	}
	if rerr := c.session.RestoreSkillVersion(entry, version, true); rerr != nil {
		c.renderer.PrintError(rerr)
		return skillsDone(), true
	}
	return skillsOutcome{}, false
}

// selectSkillPaged opens a possibly-paged picker, falling back to the plain
// rich picker for a selector that cannot grow its list.
func selectSkillPaged(sel Selector, label string, items []SelectItem, defaultIdx int, fetchMore func() []SelectItem) (int, bool, error) {
	if paged, ok := sel.(PagedRichSelector); ok {
		return paged.SelectRichPaged(label, items, defaultIdx, fetchMore)
	}
	return sel.SelectRich(label, items, defaultIdx)
}

// --- workshop draft note (design 3.6) ---

// noteSkillDraftSaved records that the workshop wrote one skill's draft during
// the current turn. The name arrives from each skill write's structured
// output, so a turn that edited several skills leaves a line for each.
func (s *ChatSession) noteSkillDraftSaved(name string) {
	name = strings.TrimSpace(name)
	if s == nil || name == "" {
		return
	}
	s.skillDraftMu.Lock()
	defer s.skillDraftMu.Unlock()
	for _, existing := range s.skillDraftNotes {
		if existing == name {
			return
		}
	}
	s.skillDraftNotes = append(s.skillDraftNotes, name)
}

// flushSkillDraftNotes takes the names recorded this turn and clears them.
func (s *ChatSession) flushSkillDraftNotes() []string {
	if s == nil {
		return nil
	}
	s.skillDraftMu.Lock()
	defer s.skillDraftMu.Unlock()
	notes := s.skillDraftNotes
	s.skillDraftNotes = nil
	return notes
}

// emitSkillDraftNotes leaves one "Draft <name> saved · /skills to publish"
// line per skill the workshop wrote this turn. It is the only hint the design
// allows — no panel opens on its own.
func (s *ChatSession) emitSkillDraftNotes() {
	for _, name := range s.flushSkillDraftNotes() {
		s.notifyUI(NewMessageMsg{Msg: Message{
			Kind:      MsgKindSystem,
			Title:     "skills",
			Content:   "Draft " + name + " saved · /skills to publish",
			Timestamp: time.Now(),
		}})
	}
}

// skillDraftNameFromStep reads the skill a completed write tool redirected
// into a draft, from the structured output the write tool puts on the step.
func skillDraftNameFromStep(output map[string]any) string {
	if output == nil {
		return ""
	}
	raw, ok := output["skill_draft"]
	if !ok {
		return ""
	}
	switch v := raw.(type) {
	case map[string]any:
		if name, ok := v["skill"].(string); ok {
			return strings.TrimSpace(name)
		}
	case map[string]string:
		return strings.TrimSpace(v["skill"])
	}
	return ""
}
