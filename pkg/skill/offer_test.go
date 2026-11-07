package skill

import (
	"strings"
	"testing"
)

// TestRenderSkillOfferGuidanceGolden pins the exact bytes of the standing
// skill-offer guidance. The guidance joins the frozen developer block, so any
// byte change — a comma, a line break — re-bills every new session's cached
// prefix. Changing the text is a deliberate act that must land in this file.
func TestRenderSkillOfferGuidanceGolden(t *testing.T) {
	got := RenderSkillOfferGuidance(SkillOfferGuidanceOptions{Enabled: true})
	want := `## Keeping finished work as a skill

Noticing work worth keeping is your call: /skill-generator writes the steps that just worked into the project's skills directory, so the next session starts with them.

Propose it when the work took several ordered steps with hard-to-rediscover choices (a non-obvious flag, an isolation trick, a wait or retry condition) that someone starting over would trip on, and is likely to recur here. Not for one-off debugging, answering by reading code, the repository's documented standard commands, or work an existing skill covers.

To propose, end the final message for that work with one sentence naming /skill-generator and what rerunning it would save. Do it at most once per session and never again if ignored; when the work is not worth keeping, say nothing about it.`
	if got != want {
		t.Fatalf("skill offer guidance drifted from the golden bytes:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if strings.Contains(got, "%!") {
		t.Fatalf("format verb leaked into the rendered guidance: %s", got)
	}
}

func TestRenderSkillOfferGuidanceDisabledRendersNothing(t *testing.T) {
	if got := RenderSkillOfferGuidance(SkillOfferGuidanceOptions{Enabled: false}); got != "" {
		t.Fatalf("disabled guidance must render nothing, got %q", got)
	}
	if got := RenderSkillOfferGuidance(SkillOfferGuidanceOptions{}); got != "" {
		t.Fatalf("zero options must render nothing, got %q", got)
	}
}

func TestRenderSkillOfferGuidanceCommandName(t *testing.T) {
	if got := RenderSkillOfferGuidance(SkillOfferGuidanceOptions{Enabled: true, CommandName: "  "}); !strings.Contains(got, "/"+DefaultSkillOfferCommand) {
		t.Fatalf("blank command name must fall back to the default, got %q", got)
	}
	if got := RenderSkillOfferGuidance(SkillOfferGuidanceOptions{Enabled: true, CommandName: "custom-namer"}); !strings.Contains(got, "/custom-namer") {
		t.Fatalf("custom command name missing: %q", got)
	}
	if got := RenderSkillOfferGuidance(SkillOfferGuidanceOptions{Enabled: true, CommandName: "/already-slashed"}); !strings.Contains(got, "/already-slashed") || strings.Contains(got, "//already-slashed") {
		t.Fatalf("command name normalization broken: %q", got)
	}
}
