package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// advisorGoldenValues is the model-visible text of the native plan-workflow
// prompts, keyed exactly as the golden file is. Comparing the key set as well
// as the bytes means a constant added without a golden entry, or a golden entry
// without a constant, fails instead of passing by silence.
func advisorGoldenValues() map[string]any {
	return map[string]any{
		"AdvisorWorkflowPrompt":        AdvisorWorkflowPrompt,
		"PlanHandoffQualityPrompt":     PlanHandoffQualityPrompt,
		"SinglePlanModePrompt":         SinglePlanModePrompt,
		"BugRootCausePrompt":           BugRootCausePrompt,
		"SubagentUserRulingPrompt":     SubagentUserRulingPrompt,
		"planReviewerDefinitionPrompt": planReviewerDefinitionPrompt,
	}
}

// TestAdvisorPromptsGolden pins the three native plan-workflow prompts byte for
// byte. AdvisorWorkflowPrompt and PlanHandoffQualityPrompt sit in the primary
// system prompt of every project type and therefore in the cached prefix, so a
// change to either is a deliberate contract change: plan text first, golden in
// the same diff. The test never rewrites the golden.
func TestAdvisorPromptsGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "advisor_prompts.golden.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden map[string]any
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	want := advisorGoldenValues()
	if len(golden) != len(want) {
		t.Errorf("golden has %d keys, want %d", len(golden), len(want))
	}
	for key := range want {
		if _, ok := golden[key]; !ok {
			t.Errorf("golden is missing key %q", key)
		}
	}
	for key := range golden {
		if _, ok := want[key]; !ok {
			t.Errorf("golden has unexpected key %q", key)
		}
	}
	if !reflect.DeepEqual(golden, want) {
		for key, wantValue := range want {
			gotValue, ok := golden[key]
			if !ok {
				continue
			}
			if !reflect.DeepEqual(gotValue, wantValue) {
				t.Errorf("golden %s differs from the runtime constant", key)
				gotText, _ := json.Marshal(gotValue)
				wantText, _ := json.Marshal(wantValue)
				t.Logf("  golden:  %s", gotText)
				t.Logf("  runtime: %s", wantText)
			}
		}
	}
}

// TestAdvisorPromptCoverage checks that every workflow the shared prompt claims
// to serve is actually described there: each invocation modifier, all nine
// audit categories, the unattended selection default and the unattended skip
// rule for publication that needs a confirmation nobody can give.
func TestAdvisorPromptCoverage(t *testing.T) {
	modifiers := map[string]string{
		"default":         "Default request: run recon, audit, vetting, prioritization and planning.",
		"quick":           "quick: cover only recon hotspots",
		"standard":        "standard (the default): use hotspot-weighted coverage",
		"deep":            "deep: cover the whole requested scope",
		"category focus":  "A category focus (",
		"branch":          "branch: audit files changed since the merge-base",
		"next":            "next, features or roadmap: audit direction only",
		"features":        "next, features or roadmap: audit direction only",
		"roadmap":         "next, features or roadmap: audit direction only",
		"reconcile":       "reconcile: process the existing plan backlog",
		"issues modifier": "--issues is a modifier on a planning workflow",
	}
	for name, sentence := range modifiers {
		if !strings.Contains(AdvisorWorkflowPrompt, sentence) {
			t.Errorf("AdvisorWorkflowPrompt does not describe the %s workflow: missing %q", name, sentence)
		}
	}

	// The category-focus line is the one place the nine audit categories are
	// enumerated, so it is also the gate that a new category cannot be added to
	// the audit prose and forgotten here.
	const focusPrefix = "A category focus ("
	start := strings.Index(AdvisorWorkflowPrompt, focusPrefix)
	if start < 0 {
		t.Fatalf("AdvisorWorkflowPrompt has no category-focus line")
	}
	rest := AdvisorWorkflowPrompt[start:]
	focusLine := rest
	if end := strings.IndexAny(rest, "\n"); end >= 0 {
		focusLine = rest[:end]
	}
	for _, category := range []string{
		"correctness",
		"security",
		"performance",
		"verification coverage",
		"architecture/tech debt",
		"dependencies/migrations",
		"workflow/tooling",
		"documentation",
		"direction",
	} {
		if !strings.Contains(focusLine, category) {
			t.Errorf("category-focus line does not name the %q category", category)
		}
	}

	for name, sentence := range map[string]string{
		"unattended selection default": "select the top 3–5 by the same rule and record that this non-interactive default was used",
		"sensitive publication skip":   "skip sensitive publication and record the reason instead of asking or publishing anyway",
	} {
		if !strings.Contains(AdvisorWorkflowPrompt, sentence) {
			t.Errorf("AdvisorWorkflowPrompt is missing the %s rule: %q", name, sentence)
		}
	}
}

// TestAdvisorPromptSelfContained keeps the workflow rules inside these
// constants. A prompt that points at another prompt file, names a provider or
// model, or names a builtin tool would make the workflow depend on something
// outside the text the model actually receives.
func TestAdvisorPromptSelfContained(t *testing.T) {
	texts := map[string]string{
		"AdvisorWorkflowPrompt":    AdvisorWorkflowPrompt,
		"PlanHandoffQualityPrompt": PlanHandoffQualityPrompt,
		"SinglePlanModePrompt":     SinglePlanModePrompt,
	}
	forbidden := []string{
		// Another prompt or configuration file to go read.
		"SKILL.md", ".claude", "~/.forebrain", "forebrain.yaml",
		"http://", "https://",
		// Providers and models.
		"deepseek", "anthropic", "openai", "chatgpt", "claude", "kimi", "zhipu", "gemini", "gpt",
		// Builtin tool names: the workflow must not assume a particular toolset.
		"present_files", "web_search", "web_fetch", "user_interaction",
		"intermediate_tool", "enter_plan_mode", "exit_plan_mode",
		"read_file", "write_file", "edit_file", "apply_patch", "subagent_run",
		"memory_search",
	}
	for name, text := range texts {
		lower := strings.ToLower(text)
		for _, word := range forbidden {
			if strings.Contains(lower, strings.ToLower(word)) {
				t.Errorf("%s names %q; the workflow must stay self-contained and toolset-neutral", name, word)
			}
		}
	}
}

// TestAdvisorPromptReviewerCarriesTheQualityBar checks that the independent
// plan reviewer measures a plan against the same handoff bar the planner was
// given, that it starts without the planner's context, and that it reports in
// the material's own coordinates instead of assuming a code-only project.
func TestAdvisorPromptReviewerCarriesTheQualityBar(t *testing.T) {
	if !strings.Contains(planReviewerDefinitionPrompt, PlanHandoffQualityPrompt) {
		t.Fatal("the plan reviewer prompt must embed the handoff quality bar verbatim")
	}
	for name, sentence := range map[string]string{
		"fresh context":          "You start with none of the planner's context",
		"investigate first":      "Investigate the request first and form your OWN understanding",
		"handoff gaps":           "Name what the planner filled in from context and never wrote down",
		"quality bar item check": "Check the plan against the handoff quality bar below, item by item",
		"source coordinates":     "`path:line` for source files, file/page/section for documents, sheet/cell for data",
		"word cap":               "under 600 words",
		"read-only role":         "never edit files and never try to approve or reject the plan yourself",
		"no manufactured issues": "do not manufacture findings",
	} {
		if !strings.Contains(planReviewerDefinitionPrompt, sentence) {
			t.Errorf("reviewer prompt is missing its %s rule: %q", name, sentence)
		}
	}
	for _, verdict := range []string{"Verdict: approve", "approve with changes", "rework"} {
		if !strings.Contains(planReviewerDefinitionPrompt, verdict) {
			t.Errorf("reviewer prompt is missing the %q verdict", verdict)
		}
	}
}

// TestBugRootCausePromptRequiresAReproduction keeps the one coding-only rule
// honest: a bug request is answered from a reproduction, not from reading, and
// the plan carries that reproduction as its first proof.
func TestBugRootCausePromptRequiresAReproduction(t *testing.T) {
	for name, sentence := range map[string]string{
		"bug requests":       "When the request is to find, locate or fix a bug",
		"not from reading":   "do not work from reading alone",
		"unit test":          "a failing unit test",
		"integration test":   "an integration test",
		"real run":           "a real run of the application that shows the defect",
		"root cause":         "establish the root cause from that evidence",
		"plan carries it":    "Name the reproduction and the root cause in the plan",
		"first verification": "make the reproduction the first verification step",
		"unverified verdict": "A fix whose failing case was never reproduced is not verified.",
	} {
		if !strings.Contains(BugRootCausePrompt, sentence) {
			t.Errorf("BugRootCausePrompt is missing its %s rule: %q", name, sentence)
		}
	}
}

// TestSubagentUserRulingPromptPinsTheAudienceRule keeps the three load-bearing
// facts of the interactive subagent-audience rule: the user may speak to a
// subagent directly, a ruling that arrives that way binds the whole session,
// and every dispatch must read as a self-contained brief.
func TestSubagentUserRulingPromptPinsTheAudienceRule(t *testing.T) {
	for name, sentence := range map[string]string{
		"user may address a subagent":   "speak to it directly",
		"rulings bind the session":      "binding for the whole session",
		"dispatches are self-contained": "self-contained brief",
	} {
		if !strings.Contains(SubagentUserRulingPrompt, sentence) {
			t.Errorf("SubagentUserRulingPrompt is missing its %s rule: %q", name, sentence)
		}
	}
}

// TestAdvisorPromptHasNoMetaWording keeps self-referential narration out of the
// prompts. The model does not care where a rule came from, whether the workflow
// is built in, or what an earlier section said; a sentence that only explains
// the prompt costs tokens on every request and cannot change what the model
// does. Behavioral rules are the only thing these constants may carry.
func TestAdvisorPromptHasNoMetaWording(t *testing.T) {
	texts := map[string]string{
		"AdvisorWorkflowPrompt":        AdvisorWorkflowPrompt,
		"PlanHandoffQualityPrompt":     PlanHandoffQualityPrompt,
		"SinglePlanModePrompt":         SinglePlanModePrompt,
		"BugRootCausePrompt":           BugRootCausePrompt,
		"SubagentUserRulingPrompt":     SubagentUserRulingPrompt,
		"planReviewerDefinitionPrompt": planReviewerDefinitionPrompt,
	}
	forbidden := []string{
		"native",
		"as discussed",
		"as described above",
		"as mentioned above",
		"as noted above",
		"(see above)",
		"previously described",
		"this conversation's history",
	}
	for name, text := range texts {
		lower := strings.ToLower(text)
		for _, phrase := range forbidden {
			if strings.Contains(lower, phrase) {
				t.Errorf("%s narrates itself: %q", name, phrase)
			}
		}
	}
}

// TestAdvisorPromptStaysSourceNeutral keeps the shared prompts free of the
// vocabulary of a coding session: AdvisorWorkflowPrompt and
// PlanHandoffQualityPrompt are appended to the office and batch primary system
// prompts too, and those conversations must not read as code changes in
// disguise. It also pins the two branch facts the workflow is identified by:
// the plan-mode write boundary and the merge-base scope.
func TestAdvisorPromptStaysSourceNeutral(t *testing.T) {
	banned := []string{"codebase", "code review", "refactor", "diff", "pull request", "git", "repository", "monorepo"}
	texts := map[string]string{
		"AdvisorWorkflowPrompt":    AdvisorWorkflowPrompt,
		"PlanHandoffQualityPrompt": PlanHandoffQualityPrompt,
		"SinglePlanModePrompt":     SinglePlanModePrompt,
		"SubagentUserRulingPrompt": SubagentUserRulingPrompt,
	}
	for name, text := range texts {
		lower := strings.ToLower(text)
		for _, word := range banned {
			if strings.Contains(lower, word) {
				t.Errorf("%s contains %q", name, word)
			}
		}
	}

	for _, sentence := range []string{
		"Inside plan mode that is the session's plan file",
		"merge-base",
	} {
		if !strings.Contains(AdvisorWorkflowPrompt, sentence) {
			t.Errorf("AdvisorWorkflowPrompt is missing %q", sentence)
		}
	}
}
