package skill

import (
	"path/filepath"
	"sort"
	"strings"
)

type Loader struct {
	Roots []string
	// StateRoot is the per-agent state root (a workspace root) onto which the
	// skill-toggle store joins "state". Must match the root the writer
	// (skilllifecycle) uses, or disabled skills won't be filtered out here.
	StateRoot string
}

type RegistrationFailure struct {
	Name  string
	Path  string
	Stage string
	Err   error
}

type RegistrationError struct {
	Discovered int
	Failures   []RegistrationFailure
}

// DiscoveredSkill is one skill found on disk: its name and where its files
// live. It is described in the prompt catalog and read from disk on demand.
type DiscoveredSkill struct {
	Name        string
	Description string
	RootDir     string
	SkillFile   string
}

// Discover returns the enabled skills under the loader's roots.
//
// Skills are described in the prompt catalog (see RenderCatalog) and read from
// disk on demand, so discovery is all the runtime needs from a skill root.
func (l Loader) Discover() ([]DiscoveredSkill, error) {
	parser := newMergedSkillParser(l.Roots, l.StateRoot)
	entries, err := parser.collect()
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	out := make([]DiscoveredSkill, 0, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.name)
		if name == "" {
			continue
		}
		root := canonicalSkillPath(entry.rootPath)
		out = append(out, DiscoveredSkill{
			Name:        name,
			Description: strings.TrimSpace(entry.description),
			RootDir:     root,
			SkillFile:   filepath.Join(root, "SKILL.md"),
		})
	}
	return out, nil
}

var skillAbs = filepath.Abs

type mergedSkillParser struct {
	roots []string
	// stateRoot is the per-agent workspace root onto which the skill-toggle
	// store joins "state"; used to filter disabled skills consistently with the
	// writer. Empty means no toggle filtering.
	stateRoot string
}

type mergedSkillEntry struct {
	dir         string
	rootPath    string
	name        string
	description string
	body        string
	metadata    map[string]any
}

func newMergedSkillParser(roots []string, stateRoot string) *mergedSkillParser {
	return &mergedSkillParser{roots: append([]string(nil), roots...), stateRoot: strings.TrimSpace(stateRoot)}
}

func (p *mergedSkillParser) collect() ([]mergedSkillEntry, error) {
	if p == nil {
		return nil, nil
	}
	// One state read for the whole walk: the toggle store is a file, and asking
	// it per skill turned a listing into one read per skill on disk.
	disabled := loadDisabledSkills(p.stateRoot)
	seenDirs := make(map[string]struct{})
	seenNames := make(map[string]struct{})
	entries := make([]mergedSkillEntry, 0)
	for _, found := range scanSkillRoots(p.roots) {
		if disabled[NormalizeSkillPath(found.Dir)] {
			continue
		}
		// A skill is identified by where it sits under its root and by its
		// name: the first root that offers either wins, so a higher-priority
		// root shadows a lower one instead of listing the skill twice.
		dirKey := strings.ToLower(strings.TrimSpace(found.Rel))
		if dirKey == "" {
			continue
		}
		if _, ok := seenDirs[dirKey]; ok {
			continue
		}
		nameKey := strings.ToLower(found.Name)
		if _, ok := seenNames[nameKey]; ok {
			continue
		}
		seenDirs[dirKey] = struct{}{}
		seenNames[nameKey] = struct{}{}
		entries = append(entries, mergedSkillEntry{
			dir:         found.Rel,
			rootPath:    found.Dir,
			name:        found.Name,
			description: strings.TrimSpace(found.Skill.Description),
			body:        strings.TrimSpace(found.Skill.Body),
			metadata:    found.Skill.Metadata,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].name) < strings.ToLower(entries[j].name)
	})
	return entries, nil
}

func canonicalSkillPath(path string) string {
	return CanonicalSkillPath(path)
}
