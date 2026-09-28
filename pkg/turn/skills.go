package turn

import (
	"strings"

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
