package skill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Entry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"`
	Origin      Origin `json:"origin,omitempty"`
	Enabled     bool   `json:"enabled"`
	// ShadowedBy is the higher-priority copy sharing this skill's name — the
	// one the runtime loads instead of this one.
	ShadowedBy []string `json:"shadowed_by,omitempty"`
	// Shadows are the lower-priority copies sharing this skill's name that
	// this one hides.
	Shadows []string `json:"shadows,omitempty"`
}

// State is the per-primary-agent skill state. It lives under the agent's own
// workspace root (see stateFile), so switching primary agents switches which
// skills are enabled — the setting travels
// together because both describe how that agent's sessions are assembled.
type State struct {
	Disabled map[string]bool `json:"disabled"`
}

// NormalizeSkillPath keys a skill by its canonical directory. It delegates to
// skillroots so the toggle store and the skill runtime cannot disagree about
// which directory a stored flag refers to.
func NormalizeSkillPath(path string) string {
	return CanonicalSkillPath(path)
}

// stateFile returns the skill state path under a per-agent state root (a
// workspace root). Callers pass the active agent's workspace root so the main
// agent and a non-main agent keep separate skill state — both which skills are
// enabled.
func stateFile(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), "state", "skills", "disabled.json")
}

func Load(stateRoot string) (State, error) {
	p := stateFile(stateRoot)
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return State{Disabled: map[string]bool{}}, nil
		}
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return State{Disabled: map[string]bool{}}, nil
	}
	if st.Disabled == nil {
		st.Disabled = map[string]bool{}
	}
	return st, nil
}

func Save(stateRoot string, st State) error {
	if st.Disabled == nil {
		st.Disabled = map[string]bool{}
	}
	p := stateFile(stateRoot)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(raw, '\n'), 0o644)
}

// loadDisabledSkills reads the toggle store once for a whole listing, instead
// of once per skill found. An empty state root means no toggle filtering — a
// listing with no agent behind it must not read a relative path out of the
// process working directory.
func loadDisabledSkills(stateRoot string) map[string]bool {
	if strings.TrimSpace(stateRoot) == "" {
		return nil
	}
	st, err := Load(stateRoot)
	if err != nil {
		return nil
	}
	return st.Disabled
}

func SetEnabledPathsForWorkspace(home string, workspaceRoot string, projectRoot string, enabledPaths []string) error {
	entries, err := DiscoverForWorkspace(home, workspaceRoot, projectRoot)
	if err != nil {
		return err
	}
	return setEnabledPathsForEntries(stateRootForWorkspace(home, workspaceRoot), entries, enabledPaths)
}

func setEnabledPathsForEntries(stateRoot string, entries []Entry, enabledPaths []string) error {
	enabled := make(map[string]struct{}, len(enabledPaths))
	for _, path := range enabledPaths {
		if key := NormalizeSkillPath(path); key != "" {
			enabled[key] = struct{}{}
		}
	}
	st, err := Load(stateRoot)
	if err != nil {
		return err
	}
	if st.Disabled == nil {
		st.Disabled = map[string]bool{}
	}
	for _, entry := range entries {
		key := NormalizeSkillPath(entry.Path)
		if key == "" {
			continue
		}
		if _, ok := enabled[key]; ok {
			delete(st.Disabled, key)
		} else {
			st.Disabled[key] = true
		}
	}
	return Save(stateRoot, st)
}

// mainWorkspaceRoot is the main agent's workspace root (<home>/workspace).
func mainWorkspaceRoot(home string) string {
	home = strings.TrimSpace(home)
	if home == "" {
		return ""
	}
	return filepath.Join(home, "workspace")
}

// stateRootForWorkspace resolves the per-agent state root onto which the
// disabled.json path is built: the agent's workspace root, falling back to
// <home>/workspace for the main agent.
func stateRootForWorkspace(home, workspaceRoot string) string {
	if root := strings.TrimSpace(workspaceRoot); root != "" {
		return root
	}
	return mainWorkspaceRoot(home)
}

func DiscoverForWorkspace(home string, workspaceRoot string, projectRoot string) ([]Entry, error) {
	layers := skillLayers(home, workspaceRoot, TrustedProjectSkillRoots(home, projectRoot))
	origins := layerOrigins(layers)
	stateRoot := stateRootForWorkspace(home, workspaceRoot)
	st, err := Load(stateRoot)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	entries := make([]Entry, 0)
	for _, found := range scanSkillRoots(RootPaths(layers)) {
		// The picker keys a skill by its canonical directory, the same key the
		// toggle store writes, so one skill reached through two roots is one
		// row with one enabled flag.
		skillDir := NormalizeSkillPath(found.Dir)
		if skillDir == "" {
			continue
		}
		if _, ok := seen[skillDir]; ok {
			continue
		}
		seen[skillDir] = struct{}{}
		entries = append(entries, Entry{
			Name:        found.Name,
			Description: strings.TrimSpace(found.Skill.Description),
			Path:        skillDir,
			Origin:      origins[found.Root],
			Enabled:     !st.Disabled[skillDir],
		})
	}
	annotateShadows(entries)
	sort.Slice(entries, func(i, j int) bool {
		li := strings.ToLower(strings.TrimSpace(entries[i].Name))
		lj := strings.ToLower(strings.TrimSpace(entries[j].Name))
		if li == lj {
			return strings.ToLower(strings.TrimSpace(entries[i].Path)) < strings.ToLower(strings.TrimSpace(entries[j].Path))
		}
		return li < lj
	})
	return entries, nil
}

// annotateShadows marks, for every name more than one root offers, the copy
// scanned first — the one the runtime loads (see mergedSkillParser.collect) —
// as shadowing the others.
func annotateShadows(entries []Entry) {
	if len(entries) < 2 {
		return
	}
	byKey := make(map[string][]int)
	for i, e := range entries {
		key := normalizeShadowKey(e.Name)
		if key == "" {
			continue
		}
		byKey[key] = append(byKey[key], i)
	}
	for _, idxs := range byKey {
		if len(idxs) < 2 {
			continue
		}
		winner := idxs[0]
		for _, i := range idxs[1:] {
			entries[winner].Shadows = appendUnique(entries[winner].Shadows, entries[i].Path)
			entries[i].ShadowedBy = appendUnique(entries[i].ShadowedBy, entries[winner].Path)
		}
	}
}

func normalizeShadowKey(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, "_", "-")
	return strings.Join(strings.Fields(s), "-")
}

func appendUnique(list []string, item string) []string {
	item = strings.TrimSpace(item)
	if item == "" {
		return list
	}
	for _, cur := range list {
		if cur == item {
			return list
		}
	}
	return append(list, item)
}
