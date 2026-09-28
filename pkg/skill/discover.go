package skill

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// discoveredSkill is one directory the scanner accepted as a skill: the parsed
// SKILL.md plus where it was found.
type discoveredSkill struct {
	// Skill is the parsed frontmatter and body, with RootPath set to Dir.
	Skill Skill
	// Name is the display name: the frontmatter name, or the directory leaf
	// when the frontmatter does not carry one.
	Name string
	// Dir is the absolute skill directory, and Rel is that directory relative
	// to Root ("caveman/caveman-commit" for a skill inside a bundle).
	Dir  string
	Rel  string
	Root string
}

// scanSkillRoots walks roots in order and returns every skill directory under
// them, in the order the roots were given.
//
// This is the only place that decides what counts as a skill on disk. The
// prompt catalog, the loaded-skill catalog the file tools read through, the
// management listing and the /skills picker all come through here, so a rule
// about what a skill is cannot hold in one listing and quietly not in another.
// Callers still apply their own dedup, ordering and annotation: which of two
// skills sharing a name wins, and what "enabled" means, are their questions,
// not this one's.
//
// The rules:
//   - Only directories, and never a dot-directory — except ".system", which is
//     where the built-in skills are installed.
//   - A directory with a parseable SKILL.md is a skill. A directory without one
//     is a bundle, and is descended into exactly one level, so a bundle's
//     skills are found while the layout stays predictable.
//   - A skill with no description is ignored entirely: it is not loaded, not
//     enabled and not listed anywhere. The description is the whole of how a
//     skill reaches the model — the catalog lists name, description and path,
//     and the model decides from the description alone whether a skill applies
//     — so a skill without one could never be chosen, and listing it would only
//     offer the user a toggle that changes nothing.
func scanSkillRoots(roots []string) []discoveredSkill {
	out := make([]discoveredSkill, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		absRoot, err := skillAbs(root)
		if err != nil {
			absRoot = filepath.Clean(root)
		}
		scanSkillDir(absRoot, absRoot, "", 0, &out)
	}
	return out
}

// maxSkillNestingDepth is how far below a root a skill may live: a bundle
// directory and the skill inside it, and no further.
const maxSkillNestingDepth = 1

func scanSkillDir(root, dir, rel string, depth int, out *[]discoveredSkill) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, item := range items {
		if !item.IsDir() {
			continue
		}
		leaf := strings.TrimSpace(item.Name())
		if leaf == "" || (strings.HasPrefix(leaf, ".") && !strings.EqualFold(leaf, systemSkillsDirName)) {
			continue
		}
		skillDir := filepath.Join(dir, leaf)
		skillRel := leaf
		if strings.TrimSpace(rel) != "" {
			skillRel = filepath.Join(rel, leaf)
		}
		raw, err := os.ReadFile(filepath.Join(skillDir, skillFileName))
		if err != nil || len(raw) == 0 {
			if depth < maxSkillNestingDepth {
				scanSkillDir(root, skillDir, skillRel, depth+1, out)
			}
			continue
		}
		parsed, err := Parse(bytes.NewReader(raw))
		if err != nil || parsed == nil {
			continue
		}
		if strings.TrimSpace(parsed.Description) == "" {
			continue
		}
		absDir, err := skillAbs(skillDir)
		if err != nil {
			absDir = filepath.Clean(skillDir)
		}
		parsed.RootPath = absDir
		name := strings.TrimSpace(parsed.Name)
		if name == "" {
			name = leaf
		}
		*out = append(*out, discoveredSkill{
			Skill: *parsed,
			Name:  name,
			Dir:   absDir,
			Rel:   skillRel,
			Root:  root,
		})
	}
}

// ValidateSkillContent reports whether a SKILL.md may enter forebrain at all. It
// is the entry gate the scanner's rules are stated as: what discovery silently
// skips on disk, this refuses at the moment a skill is created or installed, so
// a skill never lands in a skills directory only to be invisible everywhere.
func ValidateSkillContent(raw []byte) error {
	parsed, err := Parse(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if parsed == nil || strings.TrimSpace(parsed.Description) == "" {
		return ErrMissingDescription
	}
	return nil
}

// ValidateSkillDir applies ValidateSkillContent to a directory's SKILL.md.
func ValidateSkillDir(dir string) error {
	raw, err := os.ReadFile(filepath.Join(strings.TrimSpace(dir), skillFileName))
	if err != nil {
		return err
	}
	return ValidateSkillContent(raw)
}
