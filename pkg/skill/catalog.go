package skill

import (
	"path/filepath"
	"sort"
	"strings"
)

// CatalogEntry is one skill as the model sees it in the catalog: a name, a
// description, and the path of its SKILL.md. The name is what the model loads
// the skill by; the path is there so it can reach the skill's bundled files and
// recognize one of them in a diff or a stack trace.
type CatalogEntry struct {
	Name        string
	Description string
	SkillFile   string
}

const catalogIntro = "A skill is a set of local instructions to follow that is stored in a `SKILL.md` file. " +
	"The rules for using them come first, then the list of every skill available here."

// catalogUsage is the protocol the model applies to the list: decide from a
// description whether a skill applies, then load that skill's instructions with
// the skill tool before doing the work.
//
// It is rendered BEFORE the list, not after. The list is data — one line per
// skill, and long enough on a well-stocked install to put thousands of tokens
// between the section heading and anything that reads as an instruction. Behind
// the list, the obligation to consult a skill was reached only by a model that
// had already read past the point where it decides what to do, which is how a
// catalog full of matching skills ended up never being used at all.
const catalogUsage = `- Trigger: Before starting the work, check the list below. If the user names a skill, or a skill's description matches the task, you must use that skill for this turn. Several matches mean use them all. Do not carry a skill into later turns unless it matches again there.
- Loading: Call the ` + "`skill`" + ` tool with the skill's exact name from the list. It returns that skill's complete instructions plus the paths of the files bundled with it. Follow those instructions for this turn. A name and a description are not instructions — never act on a skill without loading it.
- Bundled files: Resolve every relative path in a skill's instructions against that skill's own directory, which the load reports. Read a ` + "`references/`" + ` file yourself before acting on it, and do not delegate reading or interpreting skill instructions to a subagent — a subagent may still do task work the skill allows. Run or patch a ` + "`scripts/`" + ` file instead of retyping it, and reuse an ` + "`assets/`" + ` template instead of recreating it.
- Restraint: Load the smallest set of skills that covers the request, and only the bundled files those skills route you to. Do not chase references deeper than the task needs.
- Missing or unusable: If a named skill is not in the list, or the load fails, or the instructions cannot be applied cleanly, say so in one line and continue with the best fallback.
- Announce: Name the skills you are using, and why, in one short line before you use them.`

// RenderCatalog renders the skills catalog injected ahead of the conversation.
//
// This is how a skill is described to the model; the skill tool is how one is
// loaded. Descriptions stay here rather than in that tool's schema because the
// tool array renders ahead of the system prompt: a schema carrying the skill
// list would re-bill the whole cached prefix every time a skill is installed,
// renamed or toggled, while this section sits behind the system breakpoint and
// costs only itself. The catalog is still in the cached prefix, which is why it
// must render identically for a whole session.
//
// The output is a pure function of the entries, and entries arrive sorted, so
// the same skill set renders byte-identically every time. Callers must still
// render this once per session and reuse that exact string — see
// skillCatalogLLM, which freezes it — because the set itself can change on
// disk mid-session and the catalog sits in the cached prefix.
//
// Returns "" when there are no skills, so no empty section is injected.
func RenderCatalog(entries []CatalogEntry) string {
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := catalogField(entry.Name)
		if name == "" {
			continue
		}
		line := "- " + name + ":"
		if desc := catalogField(entry.Description); desc != "" {
			line += " " + desc
		}
		if path := catalogField(entry.SkillFile); path != "" {
			line += " (" + path + ")"
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Skills\n")
	b.WriteString(catalogIntro)
	b.WriteString("\n### How to use skills\n")
	b.WriteString(catalogUsage)
	b.WriteString("\n### Available skills\n")
	b.WriteString(strings.Join(lines, "\n"))
	return b.String()
}

// catalogField flattens one field of a catalog line onto a single line.
//
// The list is read as one line per skill, and every field of it comes from
// disk: frontmatter a skill author wrote and the directory names around it.
// A newline in any of them writes a second entry — a skill the session does not
// have, with a path and instructions of its own — into the catalog the model
// trusts, and detaches the real skill's path from its name. Anyone who can
// install a skill directory can write that newline, so flattening here is the
// boundary, not a formatting nicety.
func catalogField(raw string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < ' ' || r == '\u007f' {
			return ' '
		}
		return r
	}, raw)
	return strings.Join(strings.Fields(cleaned), " ")
}

// Catalog discovers the skills under roots and renders the catalog for them.
// stateRoot is the per-agent workspace root whose toggle store decides which
// skills are enabled, so a disabled skill is not described to the model at all.
func Catalog(roots []string, stateRoot string) (string, error) {
	entries, err := CatalogEntries(roots, stateRoot)
	if err != nil {
		return "", err
	}
	return RenderCatalog(entries), nil
}

// CatalogEntries discovers the enabled skills under roots.
//
// Ordering is by lowercased name and then by path, which the parser already
// applies for the name; the path tiebreak is added here so two skills sharing a
// name cannot swap places between runs and silently invalidate the prefix.
func CatalogEntries(roots []string, stateRoot string) ([]CatalogEntry, error) {
	parser := newMergedSkillParser(roots, strings.TrimSpace(stateRoot))
	entries, err := parser.collect()
	if err != nil {
		return nil, err
	}
	out := make([]CatalogEntry, 0, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.name)
		if name == "" {
			continue
		}
		root := canonicalSkillPath(entry.rootPath)
		out = append(out, CatalogEntry{
			Name:        name,
			Description: strings.TrimSpace(entry.description),
			SkillFile:   filepath.Join(root, "SKILL.md"),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if li != lj {
			return li < lj
		}
		return out[i].SkillFile < out[j].SkillFile
	})
	return out, nil
}
