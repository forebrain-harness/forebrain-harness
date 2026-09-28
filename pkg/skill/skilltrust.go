package skill

import (
	"path/filepath"
	"strings"
)

type Source string

const (
	SourceGlobal    Source = "global"
	SourceWorkspace Source = "workspace"
	SourceProject   Source = "project"
	SourceLocal     Source = "local"
)

func SourceForPath(home, projectRoot, abs string) Source {
	home = strings.TrimSpace(home)
	workspaceRoot := ""
	if home != "" {
		workspaceRoot = filepath.Join(home, "workspace")
	}
	return SourceForPathWithWorkspace(home, workspaceRoot, projectRoot, abs)
}

func SourceForPathWithWorkspace(home, workspaceRoot, projectRoot, abs string) Source {
	abs = filepath.Clean(strings.TrimSpace(abs))
	if abs == "" {
		return SourceLocal
	}
	home = strings.TrimSpace(home)
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	switch {
	case home != "" && hasPathPrefix(abs, filepath.Join(home, "skills")):
		return SourceGlobal
	case workspaceRoot != "" && hasPathPrefix(abs, filepath.Join(workspaceRoot, "skills")):
		return SourceWorkspace
	case withinProjectSkillRoots(abs, projectRoot):
		return SourceProject
	default:
		return SourceLocal
	}
}

// withinProjectSkillRoots classifies by location only — a skill in an
// untrusted checkout is still a project skill, it just never reaches the model.
// Both project directories count: forebrain's own and the cross-tool .agents one.
// The project root is the caller's own; an empty one simply classifies nothing
// as project-local.
func withinProjectSkillRoots(abs, projectRoot string) bool {
	for _, root := range ProjectSkillRootsForDir(projectRoot) {
		if hasPathPrefix(abs, root) {
			return true
		}
	}
	return false
}

// hasPathPrefix compares canonical paths. Classifying a skill by where it
// lives only works if both sides are spelled the same way, and skill
// directories reach this function already canonicalized (absolute and
// symlink-resolved) while the roots come straight from configuration — on
// macOS that is the difference between /var and /private/var, which would
// silently downgrade every skill to "local".
func hasPathPrefix(abs, prefix string) bool {
	abs = CanonicalSkillPath(abs)
	prefix = CanonicalSkillPath(prefix)
	if abs == "" || prefix == "" {
		return false
	}
	if abs == prefix {
		return true
	}
	rel, err := filepath.Rel(prefix, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
