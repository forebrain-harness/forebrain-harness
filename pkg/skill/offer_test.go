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

Some work is worth keeping as a reusable project skill, and noticing that is
your judgment, not the user's. /skill-generator exists for it: it writes the steps that just
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
message for that work: name /skill-generator and say in a few words what rerunning it would
save. Propose at most once per session; if the user ignores it, do not raise
it again. When the work is not worth keeping, say nothing about it at all —
never explain that you considered and rejected an offer.`
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
