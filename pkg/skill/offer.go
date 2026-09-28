package skill

import (
	"fmt"
	"strings"
)

// DefaultSkillOfferCommand is the slash command the standing offer guidance
// names. The skill-generator skill derives this command from its own name;
// the constant only exists so the guidance has a stable default to spell out.
const DefaultSkillOfferCommand = "skill-generator"

// SkillOfferGuidanceOptions carries the two inputs the standing guidance
// depends on: whether the offer feature is on, and the slash command to name.
type SkillOfferGuidanceOptions struct {
	// Enabled gates the whole section. Off renders an empty string, so a
	// disabled feature leaves the frozen developer block byte-identical to a
	// build without the feature.
	Enabled bool
	// CommandName is the slash command the guidance tells the model to
	// propose. Blank falls back to DefaultSkillOfferCommand.
	CommandName string
}

// offerGuidanceTemplate is the standing criteria for turning a finished piece
// of work into a project skill. It is a per-session constant: it describes
// what kind of work is worth keeping, not anything about the current task, so
// it belongs to the frozen prompt prefix and its bytes must never drift
// between sessions or builds.
const offerGuidanceTemplate = `## Keeping finished work as a skill

Some work is worth keeping as a reusable project skill, and noticing that is
your judgment, not the user's. %s exists for it: it writes the steps that just
worked in this session into the project's skills directory, so the next
session starts with them already written down.

Worth proposing: the work took several steps in a specific order; it involved
choices that are hard to rediscover (a non-obvious flag, an isolation trick,
a wait or retry condition); someone starting from scratch would hit the same
walls; and this kind of task will plausibly recur in this repository.

Not worth proposing: one-off debugging; answering a question by reading code;
steps that are already the repository's documented standard commands; work an
existing skill already covers.

When you do propose, do it in a single sentence at the very end of the final
message for that work: name %s and say in a few words what rerunning it would
save. Propose at most once per session; if the user ignores it, do not raise
it again. When the work is not worth keeping, say nothing about it at all —
never explain that you considered and rejected an offer.`

// RenderSkillOfferGuidance renders the standing skill-offer criteria appended
// to the frozen skills catalog. It is a pure function with fixed output: the
// only variation is the command name, and a disabled feature renders nothing,
// which is what lets the rendered bytes freeze per session the same way the
// catalog itself does.
func RenderSkillOfferGuidance(opts SkillOfferGuidanceOptions) string {
	if !opts.Enabled {
		return ""
	}
	command := strings.TrimSpace(opts.CommandName)
	if command == "" {
		command = DefaultSkillOfferCommand
	}
	command = "/" + strings.TrimPrefix(command, "/")
	return fmt.Sprintf(offerGuidanceTemplate, command, command)
}
