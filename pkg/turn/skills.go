package turn

import (
	"strings"
	"unicode"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
)

// RefreshSkills rebuilds slash commands derived from installed skills.
//
// The skill set comes from the shared discovery in pkg/skill, the same set the
// prompt catalog and the /skills picker see, so a skill cannot be missing from
// the catalog yet still answer to its slash command. The launch context is the
// caller's frozen project trust decision, so the derived commands and the
// catalog always answer for the same project.
func RefreshSkills(home, workspace string, launch safety.ProjectContext) error {
	discovered, err := (skill.Loader{
		Roots:     skill.AgentSkillRoots(home, workspace, launch),
		StateRoot: workspace,
	}).Discover()
	if err != nil {
		return err
	}
	var cmds []DynamicCommand
	seen := map[string]struct{}{}
	for _, item := range discovered {
		cmd, ok := skillCommand(item.SkillFile)
		if !ok {
			continue
		}
		// Two skill names can normalize onto one slash token; the first one
		// discovered keeps the command.
		if _, ok := seen[cmd.Command.Name]; ok {
			continue
		}
		seen[cmd.Command.Name] = struct{}{}
		cmds = append(cmds, cmd)
	}
	return ReplaceDynamicSource(skillSource(home), cmds)
}

func skillSource(home string) string {
	home = strings.TrimSpace(home)
	if home == "" {
		return "skill:default"
	}
	return "skill:" + home
}

func skillCommand(path string) (DynamicCommand, bool) {
	activation, err := skill.LoadActivation(path)
	if err != nil || !activation.SlashCommand {
		return DynamicCommand{}, false
	}
	name := skill.NormalizeToken(activation.Name)
	if name == "" {
		return DynamicCommand{}, false
	}
	// Every discovered skill has a description — a skill without one is not
	// loaded at all — so this is the skill's own words, never a placeholder.
	desc := strings.TrimSpace(activation.Description)
	return DynamicCommand{
		Command: Command{
			Name: name, Description: desc, Category: SkillCategory,
			AllowedSurfaces:    []Surface{SurfaceWebChat, SurfaceTUI},
			SupportsInlineArgs: true, ArgumentHint: "[request]", Visibility: VisibilityPublic,
		},
		SkillPath: path,
		// The handler submits only the trusted selection. The file is read by
		// the runner's preload phase, so a skill replaced or deleted after
		// registration shows up as the same failed Skill card as any other
		// load failure instead of a handler-shaped error message.
		Handler: func(_ Context, line string, toks []string) Result {
			request := ""
			if len(toks) > 1 {
				request = strings.TrimSpace(strings.TrimPrefix(line, toks[0]))
			}
			return Result{
				Handled: true, ShouldContinueRun: true,
				ContinueInput: SkillCommandInput(name, request),
				SkillName:     name, SkillPath: path,
			}
		},
	}, true
}

// SkillCategory is the category every skill's slash command carries, which
// is how a surface tells skills apart from the built-in commands it lists
// first.
const SkillCategory = "skill"

// SkillCommandInput renders the user input for an explicit skill invocation.
func SkillCommandInput(name, request string) string {
	if request = strings.TrimSpace(request); request != "" {
		return request
	}
	invocation := "the selected skill"
	if name = strings.TrimSpace(name); name != "" {
		invocation = "/" + name
	}
	return "I ran " + invocation + " with no additional arguments. " +
		"Carry out the skill's instructions below for this session now. " +
		"Only ask me for more input if its instructions require input that you cannot determine yourself."
}

// SkillSelection names one skill a turn activates by name: the trusted name and
// the SKILL.md it was registered from, exactly the pair a skill's own slash
// command hands the runner.
type SkillSelection struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// SkillReferences returns the skills a message names by a "/name" token that is
// not the message's first token.
//
// A mid-sentence "/" starts a skill reference at the start of a line or after
// whitespace; a "/" inside a word (a/b, pkg/run) is not one. Only names that
// resolve to a skill — not a built-in command — are references, because a
// built-in only ever means something at the start of a message. The token's
// name is the run of tokenRuneAllowed characters, so a trailing comma, quote or
// other punctuation ends it the same way it ends the picker's query.
//
// The leading token is skipped: it is the slash command Execute handles, and its
// selection (if it is a skill) travels as the command's own result.
func SkillReferences(text string) []SkillSelection {
	runes := []rune(text)
	lead := firstNonSpaceRune(runes)
	var out []SkillSelection
	seen := map[string]struct{}{}
	for i := 0; i < len(runes); i++ {
		if runes[i] != '/' {
			continue
		}
		if i > 0 && !unicode.IsSpace(runes[i-1]) {
			continue
		}
		j := i + 1
		for j < len(runes) && tokenRuneAllowed(runes[j]) {
			j++
		}
		if i == lead || j == i+1 {
			i = j - 1
			continue
		}
		sel, ok := skillSelectionForName(string(runes[i+1 : j]))
		if !ok {
			i = j - 1
			continue
		}
		if _, dup := seen[sel.Name]; dup {
			i = j - 1
			continue
		}
		seen[sel.Name] = struct{}{}
		out = append(out, sel)
		i = j - 1
	}
	return out
}

// skillSelectionForName resolves a name to a skill's trusted selection. A
// built-in name, an unknown name, or a skill command without a path is not a
// reference.
func skillSelectionForName(name string) (SkillSelection, bool) {
	dyn, ok := findDynamic(name)
	if !ok || dyn.Command.Category != SkillCategory {
		return SkillSelection{}, false
	}
	path := strings.TrimSpace(dyn.SkillPath)
	if path == "" {
		return SkillSelection{}, false
	}
	return SkillSelection{Name: dyn.Command.Name, Path: path}, true
}
