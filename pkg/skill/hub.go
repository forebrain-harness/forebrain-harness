package skill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

type Hub struct {
	Home          string
	WorkspaceRoot string
	// ProjectRoot is the project this hub's project-scope skills belong to.
	// It is the caller's frozen launch project: project skills are project
	// state, and a hub that re-derived the root from the process working
	// directory listed whichever project the process happened to start in.
	ProjectRoot string
	// Layers is the table the hub lists, highest priority first.
	Layers []Root
}

func NewForWorkspace(home string, workspaceRoot string, projectRoot string) *Hub {
	h := &Hub{
		Home:          strings.TrimSpace(home),
		WorkspaceRoot: strings.TrimSpace(workspaceRoot),
		ProjectRoot:   strings.TrimSpace(projectRoot),
	}
	// Same roots, same order as the runtime (skillLayers owns the table), with
	// one deliberate difference: the project roots are listed without the trust
	// gate. This is the management view, and a skill the user cannot see is a
	// skill they cannot decide about; what the model may load stays gated.
	// First root scanned wins on name collision (dedup by skill name).
	h.Layers = skillLayers(h.Home, h.WorkspaceRoot, ProjectSkillRootsForDir(h.ProjectRoot))
	return h
}

func (h *Hub) workspaceRoot() string {
	if h == nil {
		return ""
	}
	if root := strings.TrimSpace(h.WorkspaceRoot); root != "" {
		return root
	}
	home := strings.TrimSpace(h.Home)
	if home == "" {
		return ""
	}
	return filepath.Join(home, "workspace")
}

type SkillDTO struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	AllowedTools string `json:"allowed_tools,omitempty"`
	RootPath     string `json:"root_path,omitempty"`
	Origin       Origin `json:"origin,omitempty"`
	Enabled      bool   `json:"enabled"`
}

func (h *Hub) ListDTO() ([]SkillDTO, error) {
	list, err := h.ListEnabledDTO()
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (h *Hub) ListEnabledDTO() ([]SkillDTO, error) {
	list, err := h.ListManaged()
	if err != nil {
		return nil, err
	}
	out := make([]SkillDTO, 0, len(list))
	for _, item := range list {
		if !item.Enabled {
			continue
		}
		out = append(out, SkillDTO{
			Name:         item.Name,
			Description:  item.Description,
			AllowedTools: item.AllowedTools,
			RootPath:     item.RootPath,
			Origin:       item.Origin,
			Enabled:      true,
		})
	}
	return out, nil
}

func (h *Hub) ListManagedDTO() ([]SkillDTO, error) {
	list, err := h.ListManaged()
	if err != nil {
		return nil, err
	}
	out := make([]SkillDTO, 0, len(list))
	for _, item := range list {
		out = append(out, SkillDTO{
			Name:         item.Name,
			Description:  item.Description,
			AllowedTools: item.AllowedTools,
			RootPath:     item.RootPath,
			Origin:       item.Origin,
			Enabled:      item.Enabled,
		})
	}
	return out, nil
}

func (h *Hub) List() ([]Skill, error) {
	managed, err := h.ListManaged()
	if err != nil {
		return nil, err
	}
	all := make([]Skill, 0, len(managed))
	for _, item := range managed {
		if !item.Enabled {
			continue
		}
		all = append(all, item.Skill)
	}
	return all, nil
}

type ManagedSkill struct {
	Skill        Skill
	Name         string
	Description  string
	AllowedTools string
	RootPath     string
	Origin       Origin
	Enabled      bool
}

func (h *Hub) ListManaged() ([]ManagedSkill, error) {
	workspaceRoot := h.workspaceRoot()
	origins := layerOrigins(h.Layers)
	disabled := loadDisabledSkills(workspaceRoot)
	seen := make(map[string]struct{})
	all := make([]ManagedSkill, 0)
	for _, found := range scanSkillRoots(RootPaths(h.Layers)) {
		// The management view keys a skill by display name: two roots offering
		// the same name are one skill, and the higher-priority root wins.
		nameKey := strings.ToLower(found.Name)
		if _, ok := seen[nameKey]; ok {
			continue
		}
		seen[nameKey] = struct{}{}
		all = append(all, ManagedSkill{
			Skill:        found.Skill,
			Name:         found.Name,
			Description:  strings.TrimSpace(found.Skill.Description),
			AllowedTools: strings.Join(found.Skill.AllowedTools, ", "),
			RootPath:     found.Dir,
			Origin:       origins[found.Root],
			Enabled:      !disabled[NormalizeSkillPath(found.Dir)],
		})
	}
	sort.Slice(all, func(i, j int) bool {
		li := strings.ToLower(strings.TrimSpace(all[i].Name))
		lj := strings.ToLower(strings.TrimSpace(all[j].Name))
		if li == lj {
			return strings.ToLower(strings.TrimSpace(all[i].RootPath)) < strings.ToLower(strings.TrimSpace(all[j].RootPath))
		}
		return li < lj
	})
	return all, nil
}

func ReadLock(workspace string) (map[string]any, error) {
	p := filepath.Join(workspace, ".forebrainhub", "lock.json")
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func entriesMap(lock map[string]any) map[string]any {
	raw, ok := lock["entries"]
	if !ok || raw == nil {
		return map[string]any{}
	}
	switch m := raw.(type) {
	case map[string]any:
		return m
	default:
		return map[string]any{}
	}
}

func MergeLockEntry(workspace string, key string, rec map[string]any) error {
	lock, err := ReadLock(workspace)
	if err != nil {
		lock = map[string]any{}
	}
	entries := entriesMap(lock)
	entries[key] = rec
	lock["entries"] = entries
	return WriteLock(workspace, lock)
}

func WriteLock(workspace string, m map[string]any) error {
	p := filepath.Join(workspace, ".forebrainhub", "lock.json")
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o644)
}

func InstallFromDir(srcDir, destSkillsDir, name string) error {
	if name == "" {
		name = filepath.Base(strings.TrimSuffix(srcDir, string(filepath.Separator)))
	}
	dst := filepath.Join(destSkillsDir, name)
	return copyDir(srcDir, dst)
}

// DirectoryDigest is the exported form of the content-addressed directory
// hash the promotion path uses. Importers that copy a skill directory once
// and must not copy it again on a re-run compare this digest of the source
// and the installed copy; equal means the copy still matches, so a repeat
// import is a no-op rather than a blind overwrite.
func DirectoryDigest(dir string) (string, error) {
	return directoryDigest(dir)
}

func copyDir(src, dst string) error {
	src = filepath.Clean(src)
	dst = filepath.Clean(dst)
	if err := home.SafeMkdirAllUnderRoot(filepath.Dir(dst), filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if err := home.ValidateArchiveRelPath(filepath.ToSlash(rel)); err != nil && rel != "." {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return home.SafeMkdirAllUnderRoot(filepath.Dir(dst), target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := home.SafeMkdirAllUnderRoot(filepath.Dir(dst), filepath.Dir(target), 0o755); err != nil {
			return err
		}
		f, err := home.OpenNoFollowForWrite(target, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := f.Write(b); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
		return nil
	})
}
