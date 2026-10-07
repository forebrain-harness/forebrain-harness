package skill

import (
	"path/filepath"
	"strings"
)

// Origin is the layer a skill comes from. It is the layer of the root the
// scanner found the skill under — read from the same table the scanner walks
// (skillLayers) — so what a listing calls a skill and where it was loaded from
// can never disagree. Both surfaces show it through Label.
type Origin string

const (
	OriginProject   Origin = "project"    // <project>/.forebrain|.agents|.claude|.codex/skills
	OriginAgent     Origin = "agent"      // <workspace>/skills: the primary agent's own
	OriginShared    Origin = "shared"     // $FOREBRAIN_HOME/skills
	OriginCrossTool Origin = "cross-tool" // ~/.agents|.claude|.codex/skills, shared with other agents
	OriginBuiltin   Origin = "builtin"    // $FOREBRAIN_HOME/skills/.system
)

// Origins lists the layers highest priority first: the order the scanner
// walks them, and the order every surface lists them in.
func Origins() []Origin {
	return []Origin{OriginProject, OriginAgent, OriginShared, OriginCrossTool, OriginBuiltin}
}

// Label is the layer's display name.
func (o Origin) Label() string {
	switch o {
	case OriginProject:
		return "Project"
	case OriginAgent:
		return "Agent"
	case OriginShared:
		return "Shared"
	case OriginCrossTool:
		return "Cross-tool"
	case OriginBuiltin:
		return "Built-in"
	}
	return string(o)
}

// Rank is the layer's place in Origins(); an origin outside it sorts last.
func (o Origin) Rank() int {
	for i, known := range Origins() {
		if o == known {
			return i
		}
	}
	return len(Origins())
}

// Root is one directory the scanner walks, and the layer of every skill found
// under it.
type Root struct {
	Path   string
	Origin Origin
}

// RootPaths drops the layers, for callers that only walk or guard the
// directories.
func RootPaths(roots []Root) []string {
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		out = append(out, root.Path)
	}
	return out
}

// skillLayers is the one table of skill roots, highest priority first. The
// runtime walks it (through agentSkillRoots) and every listing classifies by
// it, so there is no second rule to drift from the first. projectRoots are the
// caller's: trusted ones for what the model may load, all of them for the
// management view.
func skillLayers(home string, workspaceRoot string, projectRoots []string) []Root {
	h := strings.TrimSpace(home)
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	out := make([]Root, 0, len(projectRoots)+len(userSkillDirs)+3)
	for _, root := range projectRoots {
		out = append(out, Root{Path: root, Origin: OriginProject})
	}
	if workspaceRoot != "" {
		out = append(out, Root{Path: filepath.Join(workspaceRoot, "skills"), Origin: OriginAgent})
	}
	if h != "" {
		out = append(out, Root{Path: filepath.Join(h, "skills"), Origin: OriginShared})
	}
	for _, root := range UserSkillRoots() {
		out = append(out, Root{Path: root, Origin: OriginCrossTool})
	}
	if h != "" {
		out = append(out, Root{Path: filepath.Join(h, "skills", systemSkillsDirName), Origin: OriginBuiltin})
	}
	return out
}

// scanRootKey is the spelling scanSkillRoots records a root under
// (discoveredSkill.Root), so a found skill can be matched back to its layer.
func scanRootKey(root string) string {
	root = strings.TrimSpace(root)
	if abs, err := skillAbs(root); err == nil {
		return abs
	}
	return filepath.Clean(root)
}

// layerOrigins maps each root's scan key to its layer. A directory listed
// twice (a project opened at the user's home makes ~/.claude/skills both a
// project and a cross-tool root) keeps the higher-priority layer, the one the
// scanner reaches it through first.
func layerOrigins(roots []Root) map[string]Origin {
	out := make(map[string]Origin, len(roots))
	for _, root := range roots {
		key := scanRootKey(root.Path)
		if _, ok := out[key]; !ok {
			out[key] = root.Origin
		}
	}
	return out
}
