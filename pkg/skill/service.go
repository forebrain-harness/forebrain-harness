package skill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
func (s *Service) Refresh() error {
	if s.OnRefresh != nil {
		return s.OnRefresh()
	}
	return nil
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
	if err := SetEnabledPathsForWorkspace(strings.TrimSpace(s.Home), s.workspaceRoot(), s.ProjectRoot, paths); err != nil {
		return err
	}
	return s.Refresh()
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
