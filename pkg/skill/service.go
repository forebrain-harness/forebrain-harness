package skill

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Scope string

const (
	ScopeGlobal    Scope = "global"
	ScopeWorkspace Scope = "workspace"
	ScopeProject   Scope = "project"
)

type InstallRequest struct {
	SourceRef string
	Skill     string
	Name      string
	Ref       string
	DestScope Scope
}

type InstallProgress struct {
	Phase          string   `json:"phase"`
	PhaseLabel     string   `json:"phase_label,omitempty"`
	Progress       float64  `json:"progress"`
	SourceRef      string   `json:"source_ref,omitempty"`
	DestScope      Scope    `json:"dest_scope,omitempty"`
	Skill          string   `json:"skill,omitempty"`
	Name           string   `json:"name,omitempty"`
	Count          int      `json:"count,omitempty"`
	InstalledNames []string `json:"installed_names,omitempty"`
}

// Describe is the one human phrase every surface uses for an install phase,
// so a package installed from the terminal and one installed from the web
// report the same thing in the same words.
func (p InstallProgress) Describe() string {
	switch strings.TrimSpace(p.Phase) {
	case "downloading":
		return "downloading the package"
	case "discovered":
		if p.Count == 1 {
			return "found 1 skill"
		}
		if p.Count > 1 {
			return fmt.Sprintf("found %d skills", p.Count)
		}
		return "reading the package"
	case "loading":
		return "loading into the catalog"
	case "completed":
		return "installed"
	}
	if label := strings.TrimSpace(p.PhaseLabel); label != "" {
		return strings.ToLower(label)
	}
	return "preparing"
}

type InstallResult struct {
	Name      string           `json:"name"`
	DestScope Scope            `json:"dest_scope"`
	Source    string           `json:"source"`
	SourceRef string           `json:"source_ref"`
	Skill     string           `json:"skill,omitempty"`
	SkillPath string           `json:"skill_path"`
	Metadata  *SkillDTO        `json:"metadata,omitempty"`
	Installed []InstalledSkill `json:"installed,omitempty"`
	Count     int              `json:"count,omitempty"`
}

type InstalledSkill struct {
	Name      string    `json:"name"`
	SkillPath string    `json:"skill_path"`
	Metadata  *SkillDTO `json:"metadata,omitempty"`
}

type CreateRequest struct {
	Name    string
	Content string
}

type UpdateRequest struct {
	Name    string
	Content string
}

type InspectResult struct {
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	Origin           Origin    `json:"origin"`
	Path             string    `json:"path"`
	AllowedTools     string    `json:"allowed_tools,omitempty"`
	AvailableActions []string  `json:"available_actions,omitempty"`
	Metadata         *SkillDTO `json:"metadata,omitempty"`
}

type LifecycleOverview struct {
	Installed []SkillDTO `json:"installed"`
	Actions   []string   `json:"actions"`
}

// Service mutates the skill store on disk. It deliberately has no hook for
// rebuilding the running agent: the skills catalog sits in the prompt prefix,
// so applying a skill change to
// the live session would invalidate the whole cached prefix. Every operation
// here writes to disk and refreshes the process-wide skill metadata and
// slash-command table, and the change becomes a registered tool in the next
// session, when Runner.Load rescans the roots.
type Service struct {
	Home          string
	WorkspaceRoot string
	// ProjectKey is the calling session's project identity (memories.ProjectKey).
	// It selects which project's proposed skills ListMemorySkills and
	// PromoteMemorySkills draw from, alongside the agent's global scope. Empty
	// means the session has no project identity, so only global-scope
	// proposals are visible.
	ProjectKey string
	// ProjectRoot is the project this service's project-scope operations read
	// and write: Create/Update land in <ProjectRoot>/.forebrain/skills, and a
	// project-scope Install clones there too. It is the caller's frozen launch
	// project — a project is session state, and re-deriving it from the
	// process working directory wrote one session's skills into whichever
	// directory the process happened to start in. Empty fails every
	// project-scope write; it never falls back to the process cwd.
	ProjectRoot       string
	OnInstallProgress func(InstallProgress)
	OnRefresh         func() error
	IsBuiltin         func(string) bool
}

func NewService(home string) *Service {
	home = strings.TrimSpace(home)
	workspaceRoot := ""
	if home != "" {
		workspaceRoot = filepath.Join(home, "workspace")
	}
	return NewServiceForWorkspace(home, workspaceRoot)
}

func NewServiceForWorkspace(home string, workspaceRoot string) *Service {
	return &Service{Home: strings.TrimSpace(home), WorkspaceRoot: strings.TrimSpace(workspaceRoot)}
}

func (s *Service) NewHub() *Hub {
	return NewForWorkspace(s.Home, s.workspaceRoot(), s.ProjectRoot)
}

func (s *Service) List() ([]SkillDTO, error) {
	return s.NewHub().ListManagedDTO()
}

func (s *Service) Inspect(name string) (*InspectResult, error) {
	list, err := s.List()
	if err != nil {
		return nil, err
	}
	for i := range list {
		item := list[i]
		if strings.EqualFold(strings.TrimSpace(item.Name), strings.TrimSpace(name)) {
			return &InspectResult{
				Name:             strings.TrimSpace(item.Name),
				Description:      strings.TrimSpace(item.Description),
				Origin:           item.Origin,
				Path:             strings.TrimSpace(item.RootPath),
				AllowedTools:     strings.TrimSpace(item.AllowedTools),
				AvailableActions: inspectActionsForOrigin(item.Origin),
				Metadata:         &item,
			}, nil
		}
	}
	return nil, fmt.Errorf("skill not found")
}

func (s *Service) Overview() (*LifecycleOverview, error) {
	list, err := s.List()
	if err != nil {
		return nil, err
	}
	return &LifecycleOverview{
		Installed: list,
		Actions: []string{
			"inspect",
			"create",
			"update",
			"install",
			"toggle",
		},
	}, nil
}

// Refresh re-reads what this workspace's skills are.
//
// It used to warm a package-level registry first. Nothing read that registry —
// its one reader builds its own and reads it on the next line — so the warm-up
// bought nothing and cost a process-wide answer to a per-workspace question.
//
// It also brings the versioned layers' skills under version management: every
// formal skill with no history yet is bootstrapped as v1, so a skill that
// predates drafts-and-versions — or one some older tool dropped into the
// directory — still has a current version for sessions to pin and a history
// for the publish flow to build on. The v1 record says where the skill came
// from when the workspace lock knows it (kind=installed, the source, the
// upstream revision), and "created" when it does not.
func (s *Service) Refresh() error {
	s.bootstrapVersions()
	if s.OnRefresh != nil {
		return s.OnRefresh()
	}
	return nil
}

// bootstrapVersions records v1 for every formal skill under the versioned
// roots that has no version history yet. It is idempotent and per-skill
// failure-tolerant by design: one unreadable legacy skill must not make every
// later skill operation fail, so failures are logged and skipped.
func (s *Service) bootstrapVersions() {
	if s == nil {
		return
	}
	workspace := s.workspaceRoot()
	for _, root := range VersionedSkillRoots(s.Home, workspace, s.ProjectRoot) {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			leaf := strings.TrimSpace(entry.Name())
			if leaf == "" || strings.HasPrefix(leaf, ".") || !entry.IsDir() {
				// Dot directories are the built-in layer and the version
				// store's own homes; neither is a skill to version.
				continue
			}
			store := VersionStore{Root: root}
			if _, err := store.State(leaf); err != nil {
				slog.Debug("skill versions: bootstrap state", "skill", leaf, "err", err)
				continue
			}
			if _, err := store.Bootstrap(leaf, s.bootstrapRecord(workspace, leaf)); err != nil {
				slog.Debug("skill versions: bootstrap", "skill", leaf, "err", err)
			}
		}
	}
}

// bootstrapRecord is the v1 record a bootstrapped skill carries: what the
// workspace lock knows about where it came from, or "created" when the lock
// has no entry — the skill was made here, not installed.
func (s *Service) bootstrapRecord(workspace, name string) VersionRecord {
	rec := VersionRecord{Kind: VersionKindCreated}
	if entry, ok := LookupLockEntry(workspace, name); ok {
		rec.Kind = VersionKindInstalled
		rec.Source = entry.SourceRef
		rec.UpstreamRev = entry.Revision
	}
	return rec
}

func (s *Service) Create(req CreateRequest) (*InspectResult, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, fmt.Errorf("skill name required")
	}
	if err := ValidateSkillContent([]byte(req.Content)); err != nil {
		return nil, fmt.Errorf("cannot create %q: %w", name, err)
	}
	dir, mdPath, err := s.projectSkillPath(name, true)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(mdPath, []byte(req.Content), 0o644); err != nil {
		return nil, err
	}
	if err := s.Refresh(); err != nil {
		return nil, err
	}
	res, err := s.Inspect(name)
	if err == nil && res != nil && strings.TrimSpace(res.Path) == "" {
		res.Path = filepath.Join(dir, "SKILL.md")
	}
	return res, err
}

func (s *Service) Update(req UpdateRequest) (*InspectResult, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, fmt.Errorf("skill name required")
	}
	// An update that drops the description would take the skill out of every
	// listing without saying so; refuse it the same way a create is refused.
	if err := ValidateSkillContent([]byte(req.Content)); err != nil {
		return nil, fmt.Errorf("cannot update %q: %w", name, err)
	}
	// A versioned skill's change lands in its draft: the formal directory is
	// the published version sessions read, and an update here is one of the
	// workshop edits a publish carries, not a publish itself.
	if store, _, ok := s.resolveVersionedSkill(name); ok {
		draft, err := store.EnsureDraft(name)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(draft, "SKILL.md"), []byte(req.Content), 0o644); err != nil {
			return nil, err
		}
		if err := s.Refresh(); err != nil {
			return nil, err
		}
		return s.Inspect(name)
	}
	_, mdPath, err := s.projectSkillPath(name, false)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(mdPath, []byte(req.Content), 0o644); err != nil {
		return nil, err
	}
	if err := s.Refresh(); err != nil {
		return nil, err
	}
	return s.Inspect(name)
}

// Install fetches and installs a skill package. It is cancellable because the
// surfaces run it in the background: the TUI keeps the composer live while a
// clone is in flight, and abandoning the session must abandon the clone.
func (s *Service) Install(ctx context.Context, req InstallRequest) (*InstallResult, error) {
	emitProgress := func(progress InstallProgress) {
		if s == nil || s.OnInstallProgress == nil {
			return
		}
		progress.SourceRef = strings.TrimSpace(req.SourceRef)
		progress.DestScope = req.DestScope
		if strings.TrimSpace(progress.Skill) == "" {
			progress.Skill = strings.TrimSpace(req.Skill)
		}
		if strings.TrimSpace(progress.Name) == "" {
			progress.Name = strings.TrimSpace(req.Name)
		}
		s.OnInstallProgress(progress)
	}
	destDir := filepath.Join(s.Home, "skills")
	switch req.DestScope {
	case ScopeProject:
		// Project scope writes into this session's own project, never beside
		// the process: the project root is frozen launch state.
		root := strings.TrimSpace(s.ProjectRoot)
		if root == "" {
			return nil, fmt.Errorf("project install unavailable: this session has no project root")
		}
		destDir = filepath.Join(root, ".forebrain", "skills")
	case ScopeWorkspace:
		destDir = filepath.Join(s.workspaceRoot(), "skills")
	default:
		destDir = filepath.Join(s.Home, "skills")
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, err
	}
	workspace := s.workspaceRoot()
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, err
	}
	sourceKind, err := inferSourceKind(strings.TrimSpace(req.SourceRef))
	if err != nil {
		return nil, err
	}
	installedNames := []string{}
	switch sourceKind {
	case "git_repo":
		var installErr error
		installedNames, installErr = installFromGitRepo(ctx, workspace, req.SourceRef, destDir, req.Skill, req.Name, req.Ref, emitProgress)
		if installErr != nil {
			return nil, installErr
		}
	case "skills_sh":
		var installErr error
		installedNames, installErr = installFromSkillsSh(ctx, workspace, req.SourceRef, destDir, req.Skill, req.Name, req.Ref, emitProgress)
		if installErr != nil {
			return nil, installErr
		}
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedSource, req.SourceRef)
	}
	emitProgress(InstallProgress{
		Phase:          "loading",
		PhaseLabel:     "Loading",
		Progress:       0.85,
		Count:          len(installedNames),
		InstalledNames: append([]string(nil), installedNames...),
	})
	if err := s.Refresh(); err != nil {
		return nil, err
	}
	if len(installedNames) == 0 {
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = defaultInstallName(strings.TrimSpace(req.Skill), strings.TrimSpace(req.SourceRef))
		}
		installedNames = []string{name}
	}
	name := installedNames[0]
	res := &InstallResult{
		Name:      name,
		DestScope: req.DestScope,
		Source:    sourceKind,
		SourceRef: strings.TrimSpace(req.SourceRef),
		Skill:     strings.TrimSpace(req.Skill),
		SkillPath: filepath.Join(destDir, name, "SKILL.md"),
		Count:     len(installedNames),
	}
	if meta, ok := s.lookupInstalledSkill(name, strings.TrimSpace(req.Skill), res.SkillPath); ok {
		res.Metadata = meta
	}
	if len(installedNames) > 0 {
		res.Installed = make([]InstalledSkill, 0, len(installedNames))
		for _, installedName := range installedNames {
			item := InstalledSkill{
				Name:      strings.TrimSpace(installedName),
				SkillPath: filepath.Join(destDir, strings.TrimSpace(installedName), "SKILL.md"),
			}
			if meta, ok := s.lookupInstalledSkill(item.Name, item.Name, item.SkillPath); ok {
				item.Metadata = meta
			}
			res.Installed = append(res.Installed, item)
		}
	}
	emitProgress(InstallProgress{
		Phase:          "completed",
		PhaseLabel:     "Completed",
		Progress:       1,
		Name:           name,
		Count:          len(installedNames),
		InstalledNames: append([]string(nil), installedNames...),
	})
	return res, nil
}

func (s *Service) lookupInstalledSkill(name string, skillName string, skillPath string) (*SkillDTO, bool) {
	list, err := s.NewHub().ListManagedDTO()
	if err != nil {
		return nil, false
	}
	cleanSkillDir := filepath.Clean(filepath.Dir(strings.TrimSpace(skillPath)))
	if cleanSkillDir != "." && cleanSkillDir != "" {
		if abs, err := filepath.Abs(cleanSkillDir); err == nil {
			cleanSkillDir = filepath.Clean(abs)
		}
	}
	for i := range list {
		if cleanSkillDir != "." && cleanSkillDir != "" &&
			strings.EqualFold(filepath.Clean(strings.TrimSpace(list[i].RootPath)), cleanSkillDir) {
			item := list[i]
			return &item, true
		}
	}
	for i := range list {
		if strings.TrimSpace(name) != "" &&
			strings.EqualFold(strings.TrimSpace(list[i].Name), strings.TrimSpace(name)) {
			item := list[i]
			return &item, true
		}
	}
	for i := range list {
		if strings.TrimSpace(skillName) != "" &&
			strings.EqualFold(strings.TrimSpace(list[i].Name), strings.TrimSpace(skillName)) {
			item := list[i]
			return &item, true
		}
	}
	return nil, false
}

func defaultInstallName(skillName, sourceRef string) string {
	if strings.TrimSpace(skillName) != "" {
		return strings.TrimSpace(skillName)
	}
	if strings.Contains(strings.TrimSpace(sourceRef), "://") {
		return filepath.Base(strings.TrimSuffix(strings.TrimSpace(sourceRef), ".git"))
	}
	parts := strings.Split(strings.Trim(sourceRef, "/"), "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

// projectSkillPath resolves the project skill directory and SKILL.md path for
// one skill name, under this service's frozen project root. An empty project
// root is an error, never a fallback to the process working directory: a
// project skill written beside the gateway process instead of into the
// session's project is lost work the user believes was saved.
func (s *Service) projectSkillPath(name string, create bool) (string, string, error) {
	base := ""
	if s != nil {
		base = filepath.Clean(strings.TrimSpace(s.ProjectRoot))
	}
	if base == "" || base == "." {
		return "", "", fmt.Errorf("project skill unavailable: this session has no project root")
	}
	base = filepath.Join(base, ".forebrain", "skills")
	dir := filepath.Join(base, filepath.FromSlash(strings.Trim(strings.TrimSpace(name), `/\`)))
	if strings.TrimSpace(name) == "" || strings.Contains(name, "..") {
		return "", "", fmt.Errorf("invalid skill path")
	}
	if create {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", "", err
		}
	}
	md := filepath.Join(dir, "SKILL.md")
	if !create {
		if _, err := os.Stat(md); err != nil {
			return "", "", err
		}
	}
	return dir, md, nil
}

func inspectActionsForOrigin(origin Origin) []string {
	actions := []string{"inspect", "toggle"}
	switch origin {
	case OriginProject:
		actions = append(actions, "update")
	}
	return actions
}

func (s *Service) SetEnabledPaths(paths []string) error {
	// Only a published skill can be enabled or disabled: a draft is not
	// loaded anywhere, so a toggle on it would be a promise about nothing.
	for _, path := range paths {
		if name, root, ok := draftOrUnpublishedSkill(path, VersionedSkillRoots(s.Home, s.workspaceRoot(), s.ProjectRoot)); ok {
			return fmt.Errorf("%w: %s", ErrDraftNotPublished, filepath.Join(root, name))
		}
	}
	if err := SetEnabledPathsForWorkspace(strings.TrimSpace(s.Home), s.workspaceRoot(), s.ProjectRoot, paths); err != nil {
		return err
	}
	return s.Refresh()
}

// draftOrUnpublishedSkill reports whether an enable path addresses a skill
// with no published version: a draft directory, or a formal directory whose
// manifest names no current version.
func draftOrUnpublishedSkill(path string, roots []string) (name string, root string, ok bool) {
	target := CanonicalSkillPath(path)
	if target == "" {
		return "", "", false
	}
	for _, root := range roots {
		root = CanonicalSkillPath(root)
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(root, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		segments := strings.Split(rel, string(filepath.Separator))
		if len(segments) < 2 {
			continue
		}
		if segments[0] == draftsDirName {
			return segments[1], root, true
		}
		if segments[0] == versionsDirName || strings.HasPrefix(segments[0], ".") {
			continue
		}
		name := segments[0]
		if current, _ := CurrentVersion(root, name); current == 0 {
			return name, root, true
		}
	}
	return "", "", false
}

func (s *Service) workspaceRoot() string {
	if s == nil {
		return ""
	}
	if root := strings.TrimSpace(s.WorkspaceRoot); root != "" {
		return root
	}
	home := strings.TrimSpace(s.Home)
	if home == "" {
		return filepath.Join("workspace")
	}
	return filepath.Join(home, "workspace")
}

// Workspace returns the resolved workspace skill root.
func (s *Service) Workspace() string { return s.workspaceRoot() }

// The draft-and-version lifecycle the skill surfaces drive. Everything here
// addresses a skill by name and settles the layer it lives in through the
// versioned roots, so the TUI panel, the gateway routes and any future
// surface agree on which copy of a shared name a publish or a discard hits.

// resolveVersionedSkill finds the versioned layer a skill name belongs to:
// the first of the project, agent and shared roots that holds the skill's
// formal directory, its draft, or its history.
func (s *Service) resolveVersionedSkill(name string) (VersionStore, SkillVersionState, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return VersionStore{}, SkillVersionState{}, false
	}
	for _, root := range VersionedSkillRoots(s.Home, s.workspaceRoot(), s.ProjectRoot) {
		store := VersionStore{Root: root}
		state, err := store.State(name)
		if err != nil {
			continue
		}
		if state.Current > 0 || state.HasDraft {
			return store, state, true
		}
		if info, statErr := os.Stat(filepath.Join(root, name)); statErr == nil && info.IsDir() {
			return store, state, true
		}
	}
	return VersionStore{}, SkillVersionState{}, false
}

// SkillVersionRow is one skill as the versioned listing shows it: the
// listing's own facts plus the version state and, when the skill was
// installed online and upstream has moved, the update marker.
type SkillVersionRow struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// RootPath is the formal directory, or the draft directory for a skill
	// that was never published.
	RootPath string            `json:"root_path"`
	Origin   Origin            `json:"origin,omitempty"`
	Enabled  bool              `json:"enabled"`
	State    SkillVersionState `json:"state"`
}

// ListVersioned returns every skill under the versioned layers with its
// version state: published skills from the formal directories, plus the
// skills that exist only as a draft, which no formal-directory scan can see
// and which the listing owes a row ("Draft · …") all the same.
//
// ctx carries the deadline for the upstream update probes. A probe that
// fails, or a source that cannot be probed, simply leaves the row without an
// update marker.
func (s *Service) ListVersioned(ctx context.Context) ([]SkillVersionRow, error) {
	if s == nil {
		return nil, fmt.Errorf("nil service")
	}
	workspace := s.workspaceRoot()
	disabled := loadDisabledSkills(stateRootForWorkspace(s.Home, workspace))
	var out []SkillVersionRow
	seen := map[string]struct{}{}
	for _, layer := range VersionedSkillLayers(s.Home, workspace, s.ProjectRoot) {
		store := VersionStore{Root: layer.Path}
		// Formal skills: the same scan discovery applies, so a draft or a
		// version copy is never offered as a skill of its own.
		for _, found := range scanSkillRoots([]string{layer.Path}) {
			name := strings.TrimSpace(found.Name)
			if name == "" {
				continue
			}
			dirName := filepath.Base(found.Dir)
			if _, ok := seen[dirName]; ok {
				continue
			}
			state, err := store.State(dirName)
			if err != nil {
				continue
			}
			seen[dirName] = struct{}{}
			out = append(out, SkillVersionRow{
				Name:        name,
				Description: strings.TrimSpace(found.Skill.Description),
				RootPath:    found.Dir,
				Origin:      layer.Origin,
				Enabled:     !disabled[NormalizeSkillPath(found.Dir)],
				State:       s.withUpdate(ctx, workspace, dirName, state),
			})
		}
		// Draft-only skills: never published, invisible to the scan above,
		// and not enabled anywhere because there is nothing to load.
		draftEntries, err := os.ReadDir(filepath.Join(layer.Path, draftsDirName))
		if err != nil {
			continue
		}
		for _, entry := range draftEntries {
			leaf := strings.TrimSpace(entry.Name())
			if leaf == "" || strings.HasPrefix(leaf, ".") || !entry.IsDir() {
				continue
			}
			if _, ok := seen[leaf]; ok {
				continue
			}
			state, err := store.State(leaf)
			if err != nil || state.Current > 0 || !state.HasDraft {
				continue
			}
			seen[leaf] = struct{}{}
			row := SkillVersionRow{
				Name:     leaf,
				RootPath: state.DraftDir,
				Origin:   layer.Origin,
				State:    state,
			}
			if parsed, err := Parse(bytes.NewReader(mustReadSkillFile(filepath.Join(state.DraftDir, "SKILL.md")))); err == nil && parsed != nil {
				row.Description = strings.TrimSpace(parsed.Description)
			}
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if li != lj {
			return li < lj
		}
		return out[i].RootPath < out[j].RootPath
	})
	return out, nil
}

// SkillVersionDetail is one skill's version state for the detail routes: the
// row ListVersioned would show, resolved by name.
func (s *Service) SkillVersionDetail(ctx context.Context, name string) (*SkillVersionRow, error) {
	rows, err := s.ListVersioned(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if strings.EqualFold(strings.TrimSpace(rows[i].Name), strings.TrimSpace(name)) {
			return &rows[i], nil
		}
	}
	// A formal skill the scan refuses (a broken SKILL.md, say) still owns
	// its history; the detail route answers for it.
	if store, state, ok := s.resolveVersionedSkill(name); ok {
		origin := OriginShared
		for _, layer := range VersionedSkillLayers(s.Home, s.workspaceRoot(), s.ProjectRoot) {
			if layer.Path == store.Root {
				origin = layer.Origin
			}
		}
		return &SkillVersionRow{
			Name:     strings.TrimSpace(name),
			RootPath: filepath.Join(store.Root, strings.TrimSpace(name)),
			Origin:   origin,
			State:    s.withUpdate(ctx, s.workspaceRoot(), strings.TrimSpace(name), state),
		}, nil
	}
	return nil, fmt.Errorf("skill not found")
}

// VersionDir resolves one published version's directory for a skill name: the
// complete copy a download or a comparison reads, and the address a pinned
// session loads from.
func (s *Service) VersionDir(name string, version int) (string, error) {
	if version <= 0 {
		return "", fmt.Errorf("version must be positive")
	}
	store, _, ok := s.resolveVersionedSkill(name)
	if !ok {
		return "", fmt.Errorf("skill %q not found", strings.TrimSpace(name))
	}
	dir := store.versionDir(strings.TrimSpace(name), version)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("skill %q has no v%d", strings.TrimSpace(name), version)
	}
	return dir, nil
}

// withUpdate fills the upstream marker for a git-installed skill: the newest
// revision its source advertises, when that differs from the revision the
// current version recorded. Anything unreadable or unprobed leaves the skill
// without a marker — the plan's "failure is not an update".
func (s *Service) withUpdate(ctx context.Context, workspace, name string, state SkillVersionState) SkillVersionState {
	if ctx == nil || state.Current == 0 || len(state.Versions) == 0 {
		return state
	}
	latest := state.Versions[len(state.Versions)-1]
	repo, rev := latest.Source, latest.UpstreamRev
	if strings.TrimSpace(repo) == "" || strings.TrimSpace(rev) == "" {
		if entry, ok := LookupLockEntry(workspace, name); ok && strings.TrimSpace(entry.SourceRef) != "" {
			repo = entry.SourceRef
			if strings.TrimSpace(rev) == "" {
				rev = entry.Revision
			}
		}
	}
	if strings.TrimSpace(repo) == "" || strings.TrimSpace(rev) == "" {
		return state
	}
	remote, ok := RemoteHead(ctx, repo)
	if !ok || remote == "" || remote == rev {
		return state
	}
	state.Update = &SkillUpdate{Rev: remote}
	return state
}

// mustReadSkillFile is the tolerant read a listing's decoration uses: a file
// that cannot be read has no description to show, not a listing that fails.
func mustReadSkillFile(path string) []byte {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return raw
}

// PublishSkill releases the skill's draft as its next version. The draft must
// hold a loadable skill — the same content gate every write to a skill
// directory passes — because publishing is the one moment the formal
// directory changes, and an unreadable SKILL.md there would make the skill
// invisible everywhere at once. A published skill is enabled: publishing is
// the act that makes a skill loadable.
func (s *Service) PublishSkill(name, note string) (int, error) {
	store, state, ok := s.resolveVersionedSkill(name)
	if !ok {
		return 0, fmt.Errorf("skill %q not found", strings.TrimSpace(name))
	}
	name = strings.TrimSpace(name)
	if err := validateDraftForPublish(store, name); err != nil {
		return 0, err
	}
	version, err := store.Publish(name, note)
	if err != nil {
		return 0, err
	}
	_ = state
	// Publishing enables: clear the toggle the skill may have carried, so
	// the next session loads the version that was just released.
	formal := filepath.Join(store.Root, name)
	if err := enableSkillDir(s.Home, s.workspaceRoot(), formal); err != nil {
		slog.Debug("skill versions: enable after publish", "skill", name, "err", err)
	}
	if err := s.Refresh(); err != nil {
		slog.Debug("skill versions: refresh after publish", "skill", name, "err", err)
	}
	return version, nil
}

// validateDraftForPublish applies the content gate to the draft about to
// become the formal directory.
func validateDraftForPublish(store VersionStore, name string) error {
	draft, err := store.EnsureDraft(name)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(draft, "SKILL.md"))
	if err != nil {
		return fmt.Errorf("the draft has no SKILL.md to publish: %w", err)
	}
	if err := ValidateSkillContent(raw); err != nil {
		return fmt.Errorf("cannot publish: %w", err)
	}
	parsed, err := Parse(bytes.NewReader(raw))
	if err != nil || parsed == nil {
		return fmt.Errorf("cannot publish: %w", err)
	}
	if frontName := strings.TrimSpace(parsed.Name); frontName != "" && !strings.EqualFold(frontName, name) {
		return fmt.Errorf("cannot publish: frontmatter name %q does not match the skill directory %q", frontName, name)
	}
	return nil
}

// enableSkillDir clears a skill directory's disabled flag in the per-agent
// toggle store.
func enableSkillDir(home, workspaceRoot, dir string) error {
	stateRoot := stateRootForWorkspace(home, workspaceRoot)
	st, err := Load(stateRoot)
	if err != nil {
		return err
	}
	key := NormalizeSkillPath(dir)
	if !st.Disabled[key] {
		return nil
	}
	delete(st.Disabled, key)
	return Save(stateRoot, st)
}

// DiscardDraft drops the skill's unpublished changes. A never-published skill
// loses everything it has; that is what discarding means for it, and the
// surface asking is the one responsible for the confirmation.
func (s *Service) DiscardDraft(name string) error {
	store, _, ok := s.resolveVersionedSkill(name)
	if !ok {
		return fmt.Errorf("skill %q not found", strings.TrimSpace(name))
	}
	if err := store.Discard(strings.TrimSpace(name)); err != nil {
		return err
	}
	return s.Refresh()
}

// RestoreVersionAsDraft copies one published version into the skill's draft,
// replacing a draft that already exists only when replaceDraft says so.
func (s *Service) RestoreVersionAsDraft(name string, version int, replaceDraft bool) error {
	name = strings.TrimSpace(name)
	store, state, ok := s.resolveVersionedSkill(name)
	if !ok {
		return fmt.Errorf("skill %q not found", name)
	}
	if state.HasDraft && !replaceDraft {
		return fmt.Errorf("skill %q already has a draft: confirm replacing it to restore v%d", name, version)
	}
	return store.RestoreAsDraft(name, version)
}

// DiffSkill compares two snapshots of one skill — published versions by
// number ("v3") and the draft ("draft").
func (s *Service) DiffSkill(name, from, to string) ([]FileDiff, error) {
	store, _, ok := s.resolveVersionedSkill(name)
	if !ok {
		return nil, fmt.Errorf("skill %q not found", strings.TrimSpace(name))
	}
	return store.Diff(strings.TrimSpace(name), from, to)
}

// PullUpdate fetches the skill's upstream content into its draft. The source
// and the subpath it installs from are the ones the workspace lock recorded
// at install time; an existing draft is replaced only when replaceDraft says
// so. The pulled draft carries the upstream revision, so publishing it
// records the version as an update rather than an ordinary release.
func (s *Service) PullUpdate(ctx context.Context, name string, replaceDraft bool) error {
	name = strings.TrimSpace(name)
	store, state, ok := s.resolveVersionedSkill(name)
	if !ok {
		return fmt.Errorf("skill %q not found", name)
	}
	workspace := s.workspaceRoot()
	entry, haveLock := LookupLockEntry(workspace, name)
	repo := strings.TrimSpace(entry.SourceRef)
	if !haveLock || repo == "" {
		if state.Current > 0 && len(state.Versions) > 0 {
			repo = latestSource(SkillVersionManifest{
				Versions: state.Versions,
			})
		}
	}
	if repo == "" {
		return fmt.Errorf("skill %q has no recorded source to pull from", name)
	}
	if state.HasDraft && !replaceDraft {
		return fmt.Errorf("skill %q already has a draft: confirm replacing it to pull the update", name)
	}
	if err := ensureGitAvailable(); err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp("", "forebrain-skill-pull-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	repoRoot := filepath.Join(tmpDir, "repo")
	if _, err := gitOut(ctx, tmpDir, "clone", "--depth", "1", repo, "repo"); err != nil {
		return err
	}
	rev := gitRevision(ctx, repoRoot)
	if strings.TrimSpace(rev) == "" {
		// STOP 3 territory: the clone succeeded but its commit cannot be
		// read. Report it rather than record a pull with no revision.
		return fmt.Errorf("pulled %s but could not read its commit: %w", repo, ErrNoUpstreamRevision)
	}
	subpath := strings.TrimSpace(entry.SkillSubpath)
	if subpath == "" {
		subpath = name
	}
	skillDir, err := resolveSkillSubdir(repoRoot, subpath)
	if err != nil {
		return err
	}
	if err := ValidateSkillDir(skillDir.Path); err != nil {
		return fmt.Errorf("cannot pull %q: %w", name, err)
	}
	if err := os.MkdirAll(filepath.Join(store.Root, draftsDirName), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(store.Root, draftsDirName, fmt.Sprintf(".tmp-%s-%d", name, time.Now().UnixNano()))
	if err := copySkillTree(skillDir.Path, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	_ = os.RemoveAll(store.draftDir(name))
	if err := os.Rename(tmp, store.draftDir(name)); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	m, err := store.State(name)
	if err != nil {
		return err
	}
	manifest, _ := store.readManifest(name)
	manifest.DraftBase = m.Current
	manifest.DraftRev = rev
	if err := store.writeManifest(manifest); err != nil {
		return err
	}
	return s.Refresh()
}
