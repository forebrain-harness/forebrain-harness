package skill

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
)

const (
	skillResourceMaxEntries = 128
	skillResourceMaxDepth   = 5
)

var errSkillResourceLimit = errors.New("skill resource listing limit reached")

// Activation is the fully rendered, model-facing context for one installed
//
//	Explicit slash invocations carry this value through the product
//
// surface and inject Content before the user's request, so the model receives
// the skill's instructions without having to locate and read the file itself.
type Activation struct {
	Name string
	// Slug is Name reduced to the identifier grammar shared with slash
	// commands and step records.
	Slug        string
	Description string
	Content     string
	// SlashCommand reports whether this skill wants its own auto-derived
	// slash command. False only when SKILL.md sets `slash-command: false`.
	SlashCommand bool
}

type skillActivationContext struct {
	Name          string   `json:"name"`
	RootDir       string   `json:"root_dir"`
	SkillFile     string   `json:"skill_file"`
	Access        string   `json:"access"`
	RelativePaths string   `json:"relative_paths"`
	Directories   []string `json:"directories"`
	Files         []string `json:"files"`
	Truncated     bool     `json:"truncated,omitempty"`
}

// LoadActivation reads an installed SKILL.md and renders the same context that
// the catalog describes. Callers use this only for explicit user
// invocation; otherwise the model finds the skill through the prompt catalog.
func LoadActivation(skillFile string) (Activation, error) {
	skillFile = strings.TrimSpace(skillFile)
	if skillFile == "" {
		return Activation{}, fmt.Errorf("skill file required")
	}
	raw, err := os.ReadFile(skillFile)
	if err != nil {
		return Activation{}, err
	}
	sk, err := Parse(bytes.NewReader(raw))
	if err != nil {
		return Activation{}, err
	}
	name := strings.TrimSpace(sk.Name)
	if name == "" {
		return Activation{}, fmt.Errorf("skill name required")
	}
	root := canonicalSkillPath(filepath.Dir(skillFile))
	entry := mergedSkillEntry{
		rootPath:    root,
		name:        name,
		description: strings.TrimSpace(sk.Description),
		body:        strings.TrimSpace(sk.Body),
		metadata:    sk.Metadata,
	}
	slashCommand := true
	if sk.SlashCommand != nil {
		slashCommand = *sk.SlashCommand
	}
	return Activation{
		Name:         name,
		Slug:         agent.SanitizeToolName(name),
		Description:  strings.TrimSpace(sk.Description),
		Content:      renderSkillActivation(entry),
		SlashCommand: slashCommand,
	}, nil
}

func renderSkillActivation(entry mergedSkillEntry) string {
	root := canonicalSkillPath(entry.rootPath)
	dirs, files, truncated := listSkillResources(root)
	ctx := skillActivationContext{
		Name:          strings.TrimSpace(entry.name),
		RootDir:       root,
		SkillFile:     filepath.Join(root, "SKILL.md"),
		Access:        "read-only",
		RelativePaths: "Resolve every relative path in the skill instructions against root_dir.",
		Directories:   dirs,
		Files:         files,
		Truncated:     truncated,
	}
	raw, _ := json.Marshal(ctx)
	// codex's shape: one <skill> element carrying the name, the path to the
	// SKILL.md, and the body. The resource listing rides along as <context> so
	// the model can resolve the skill's relative paths without a directory
	// walk of its own.
	return "<skill>\n<name>" + ctx.Name + "</name>\n<path>" + ctx.SkillFile + "</path>\n" +
		"<context>" + string(raw) + "</context>\n" +
		strings.TrimSpace(entry.body) + "\n</skill>"
}

func listSkillResources(root string) (dirs []string, files []string, truncated bool) {
	dirs = []string{}
	files = []string{}
	root = strings.TrimSpace(root)
	if root == "" {
		return dirs, files, false
	}
	count := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		depth := strings.Count(rel, "/") + 1
		base := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(base, ".") || depth > skillResourceMaxDepth {
				return filepath.SkipDir
			}
		} else if d.Type()&fs.ModeSymlink != 0 || strings.HasPrefix(base, ".") {
			return nil
		}
		if strings.EqualFold(rel, "SKILL.md") {
			return nil
		}
		if count >= skillResourceMaxEntries {
			return errSkillResourceLimit
		}
		if d.IsDir() {
			dirs = append(dirs, rel)
		} else {
			files = append(files, rel)
		}
		count++
		return nil
	})
	if errors.Is(err, errSkillResourceLimit) {
		truncated = true
	}
	sort.Strings(dirs)
	sort.Strings(files)
	return dirs, files, truncated
}
