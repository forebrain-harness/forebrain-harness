package skill

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type regEntry struct {
	Name      string
	SkillPath string
	SkillDir  string
	Enabled   bool
}

func NormalizeToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.Join(strings.Fields(s), "-")
	return s
}

type Registry struct {
	mu      sync.RWMutex
	byToken map[string]regEntry
}

// DistinctSkillNamesForWorkspace lists, in order, the name of every skill one
// workspace can load.
//
// The registry it needs is built here and dropped here. It used to be a
// package-level singleton, which made "whose skills are these" a question of
// who refreshed last: two primary agents in one process shared one answer even
// though a primary agent is a tenant, and so did two tests, which is how a test
// that installs three skills came to see twenty-six.
func DistinctSkillNamesForWorkspace(home, workspaceRoot, projectRoot string) ([]string, error) {
	var registry Registry
	if err := registry.RefreshForWorkspace(home, workspaceRoot, projectRoot); err != nil {
		return nil, err
	}
	return registry.DistinctSkillNames(), nil
}

func (r *Registry) RefreshForWorkspace(home, workspaceRoot, projectRoot string) error {
	by := make(map[string]regEntry)
	home = strings.TrimSpace(home)
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" && home != "" {
		workspaceRoot = filepath.Join(home, "workspace")
	}
	entries, err := DiscoverForWorkspace(home, workspaceRoot, projectRoot)
	if err != nil {
		return err
	}
	for _, item := range entries {
		skillDir := filepath.Clean(strings.TrimSpace(item.Path))
		if skillDir == "" {
			continue
		}
		path := filepath.Join(skillDir, "SKILL.md")
		raw, err := os.ReadFile(path)
		if err != nil || len(raw) == 0 {
			continue
		}
		sk, err := Parse(bytes.NewReader(raw))
		if err != nil || sk == nil {
			continue
		}
		leaf := filepath.Base(skillDir)
		e := regEntry{
			Name:      strings.TrimSpace(item.Name),
			SkillPath: path,
			SkillDir:  skillDir,
			Enabled:   item.Enabled,
		}
		if e.Name == "" {
			e.Name = strings.TrimSpace(sk.Name)
		}
		if e.Name == "" {
			e.Name = leaf
		}
		keys := []string{NormalizeToken(e.Name), NormalizeToken(leaf)}
		for _, k := range keys {
			if k == "" {
				continue
			}
			if _, ok := by[k]; !ok {
				by[k] = e
			}
		}
	}
	r.mu.Lock()
	r.byToken = by
	r.mu.Unlock()
	return nil
}

func (r *Registry) DistinctSkillNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.byToken == nil {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, len(r.byToken))
	for _, e := range r.byToken {
		d := strings.TrimSpace(e.SkillDir)
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		n := strings.TrimSpace(e.Name)
		if n != "" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}
