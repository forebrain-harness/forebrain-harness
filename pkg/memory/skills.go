// Skill-sourced memories and the authored-skill port.
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"go.yaml.in/yaml/v2"
)

// AuthoredSkillsDir is the memories-root-relative directory the consolidation
// agent writes reusable procedures to. The consolidation prompt names this
// layout, so the constant owns the join rather than letting each reader spell
// it out.
const AuthoredSkillsDir = "skills"

// AuthoredSkillsRoot is the directory holding skills the consolidation agent
// wrote. It is a sibling of the workspace skill root, deliberately outside
// every root the skill loader scans: these are model-authored proposals, and
// nothing here reaches the model until a user promotes it.
func (r Root) AuthoredSkillsRoot() string {
	return filepath.Join(r.MemoryRoot, AuthoredSkillsDir)
}

// AuthoredSkill is one promotable skill directory under AuthoredSkillsRoot.
type AuthoredSkill struct {
	// Name is the display name from the frontmatter, falling back to the
	// directory name, matching how the skill hub names an installed skill.
	Name        string
	Description string
	Dir         string
	SkillFile   string
	// Digest identifies the exact contents of Dir, so a caller that copied it
	// earlier can tell whether consolidation has rewritten it since.
	Digest string
}

// ListAuthoredSkills reports the skills consolidation has written, sorted by
// name. A directory whose SKILL.md is missing or unparseable is skipped rather
// than reported as an error: the file is model-authored, and one malformed
// proposal must not hide the gateway.
func ListAuthoredSkills(root Root) ([]AuthoredSkill, error) {
	dir := root.AuthoredSkillsRoot()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]AuthoredSkill, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		skillDir := filepath.Join(dir, entry.Name())
		skillFile := filepath.Join(skillDir, "SKILL.md")
		raw, err := os.ReadFile(skillFile)
		if err != nil || len(raw) == 0 {
			continue
		}
		parsed, err := parseAuthoredSkill(raw)
		if err != nil {
			continue
		}
		digest, err := DirectoryDigest(skillDir)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(parsed.Name)
		if name == "" {
			name = strings.TrimSpace(entry.Name())
		}
		out = append(out, AuthoredSkill{
			Name:        name,
			Description: strings.TrimSpace(parsed.Description),
			Dir:         skillDir,
			SkillFile:   skillFile,
			Digest:      digest,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if strings.EqualFold(out[i].Name, out[j].Name) {
			return out[i].Dir < out[j].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

type authoredSkill struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func parseAuthoredSkill(raw []byte) (authoredSkill, error) {
	text := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(text, "---") {
		return authoredSkill{}, fmt.Errorf("missing frontmatter")
	}
	parts := strings.SplitN(text, "---", 3)
	if len(parts) != 3 {
		return authoredSkill{}, fmt.Errorf("invalid frontmatter")
	}
	var parsed authoredSkill
	if err := yaml.Unmarshal([]byte(parts[1]), &parsed); err != nil {
		return authoredSkill{}, err
	}
	return parsed, nil
}

// DirectoryDigest hashes every regular file under dir by relative path and
// content, so two directories with identical contents hash the same regardless
// of timestamps or the order the filesystem enumerates them.
//
// It fails on anything that is not a regular file or a directory. A symlink
// here is not a curiosity to skip: the copy that promotion performs reads
// through symlinks, so one pointing outside the memory root would land its
// target's contents in a directory the model can read. Refusing the whole
// directory is the only answer that cannot be worked around by placing the
// link where the walk happens to skip it.
func DirectoryDigest(dir string) (string, error) {
	type fileEntry struct {
		rel  string
		body []byte
	}
	var files []fileEntry
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, fileEntry{rel: filepath.ToSlash(rel), body: body})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	sum := sha256.New()
	for _, file := range files {
		fmt.Fprintf(sum, "%s|%d|", file.rel, len(file.body))
		sum.Write(file.body)
		sum.Write([]byte{'\n'})
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// ListAuthoredSkillsForWorkspace merges the authored skills of every scope a
// workspace draws from (its project scope, when it has one, then the agent's
// global scope), project first so a name proposed identically in two scopes
// resolves to the more concrete, project-specific proposal rather than the
// cross-project one.
//
// It implements skill.AuthoredSkillSource. skill cannot import memory
// directly (C7: skill and memory are same-layer siblings; skill reaches
// memory only through a port it defines), so process registers this function
// with skill.SetAuthoredSkillSource once at startup instead — the correct
// standing direction is memory -> skill, not the reverse.
func ListAuthoredSkillsForWorkspace(workspace, projectKey string) ([]skill.AuthoredSkill, error) {
	roots, err := ResolveRootsForAgent(workspace)
	if err != nil {
		return nil, err
	}
	scopes := make([]Scope, 0, 2)
	if key := strings.TrimSpace(projectKey); key != "" {
		scopes = append(scopes, Scope{Kind: ScopeProject, Key: key})
	}
	scopes = append(scopes, GlobalScope())

	seen := make(map[string]struct{})
	var out []skill.AuthoredSkill
	for _, scope := range scopes {
		items, err := ListAuthoredSkills(roots.Scope(scope))
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			key := strings.ToLower(item.Name)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, skill.AuthoredSkill{
				Name:        item.Name,
				Description: item.Description,
				Dir:         item.Dir,
				SkillFile:   item.SkillFile,
				Digest:      item.Digest,
			})
		}
	}
	return out, nil
}
