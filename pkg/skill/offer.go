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

Noticing work worth keeping is your call: %s writes the steps that just worked into the project's skills directory, so the next session starts with them.

Propose it when the work took several ordered steps with hard-to-rediscover choices (a non-obvious flag, an isolation trick, a wait or retry condition) that someone starting over would trip on, and is likely to recur here. Not for one-off debugging, answering by reading code, the repository's documented standard commands, or work an existing skill covers.

To propose, end the final message for that work with one sentence naming %s and what rerunning it would save. Do it at most once per session and never again if ignored; when the work is not worth keeping, say nothing about it.`

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
