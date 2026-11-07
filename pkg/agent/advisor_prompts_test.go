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
		"PlanHandoffQualityPrompt":        PlanHandoffQualityPrompt,
		"SinglePlanModePrompt":            SinglePlanModePrompt,
		"BugRootCausePrompt":              BugRootCausePrompt,
		"NonFunctionalRequirementsPrompt": NonFunctionalRequirementsPrompt,
		"SubagentUserRulingPrompt":        SubagentUserRulingPrompt,
		"planReviewerDefinitionPrompt":    planReviewerDefinitionPrompt,
	}
}

// TestAdvisorPromptsGolden pins the native plan-workflow prompts byte for
// byte. PlanHandoffQualityPrompt sits in the primary system prompt of every
// project type and therefore in the cached prefix, so a change to it is a
// deliberate contract change: golden in the same diff. The test never rewrites
// the golden.
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

// TestAdvisorPromptSelfContained keeps the workflow rules inside these
// constants. A prompt that points at another prompt file, names a provider or
// model, or names a builtin tool would make the workflow depend on something
// outside the text the model actually receives.
func TestAdvisorPromptSelfContained(t *testing.T) {
	texts := map[string]string{
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
		"account is right":       "its account of the problem is right, and its steps would achieve the request",
		"cause not symptom":      "Judge the solution: does it remove the cause, or only guard against the symptom?",
		"defensive layers":       "Defensive checks, special cases, retries and fallbacks over a cause left in place treat the symptom",
		"same-class search":      "Search the material yourself for other instances of the same or a similar problem",
		"same-class finding":     "Every one the plan neither includes nor excludes with a reason is a finding.",
		"same-class report":      "The same or similar problems the plan leaves out, each with its location, telling the planner to bring it into scope or exclude it with a reason.",
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

// TestBugRootCausePromptRequiresAReproduction pins the coding bug rule in its
// three role parts: diagnosis finished while planning, from a reproduction and
// a cited causal chain; an executor that stops when the diagnosis does not
// hold; a reviewer that diagnoses on its own before judging the plan.
func TestBugRootCausePromptRequiresAReproduction(t *testing.T) {
	if BugRootCausePrompt != bugDiagnosisPrompt+bugExecutionPrompt+codingReviewPrompt {
		t.Fatal("BugRootCausePrompt must be exactly its three role parts")
	}
	for part, sentences := range map[string]map[string]string{
		bugDiagnosisPrompt: {
			"bug requests":          "When finding, locating or fixing a bug, do not work from reading alone.",
			"reproduction kinds":    "a failing unit or integration test, or a real run of the application showing the defect",
			"root cause":            "establish the root cause from that evidence",
			"diagnosis in planning": "Finish the diagnosis while planning; never leave it to the executor",
			"cited causal chain":    "Trace the chain from trigger to symptom through the actual source, citing each link",
			"checked against run":   "check every link against what the reproduction showed",
			"every symptom path":    "Find every path that produces the same symptom",
			"same defect elsewhere": "Search the project for the same defect elsewhere",
			"in scope or excused":   "bring each into the plan or say why it stays out",
			"rivals ruled out":      "Name the competing explanations and the evidence that ruled each out.",
			"evidence in the plan":  "Put the reproduction command, its failing output and the causal chain in the plan, with the reproduction as its first verification step.",
			"no fix on a guess":     "plan the diagnosis - the reproduction, probes that tell the candidates apart, what each outcome means - not a fix built on a guess",
			"real run when needed":  "Use a real run whenever a unit test cannot reach the defect",
			"blocked real run":      "say what blocked it and what must be run by hand",
			"fix breaks the chain":  "A fix is proven only when it breaks the chain at the cause",
			"unverified verdict":    "A fix whose failing case was never reproduced is not verified.",
		},
		bugExecutionPrompt: {
			"executor reproduces": "When you execute a bug-fix plan, run its reproduction before changing anything.",
			"executor stops":      "stop and report the discrepancy with evidence instead of improvising another fix",
		},
		codingReviewPrompt: {
			"anchors are not review": "confirming that cited files, symbols and line numbers still exist is housekeeping, not the review",
			"reviewer re-diagnoses":  "reproduce the bug and find its root cause yourself first",
			"plan does not steer":    "without letting the plan choose where you look",
			"wrong cause is rework":  "a plan built on the wrong cause needs rework",
			"reviewer runs it":       "Run the plan's reproduction and confirm it fails for the stated reason",
			"missing repro finding":  "a missing or unrunnable reproduction is a finding",
			"unverified cause fails": "An unverified root cause is the most serious finding: never call such a plan sound.",
		},
	} {
		for name, sentence := range sentences {
			if !strings.Contains(part, sentence) {
				t.Errorf("bug rule is missing its %s rule: %q", name, sentence)
			}
		}
	}
}

// TestNonFunctionalRequirementsPromptCoversEveryQuality keeps the design bar
// whole: every quality the owner named, a stated check for each one touched,
// leaks, deadlocks and hangs by name, trade-offs said out loud, and the same
// bar for a plan under review.
func TestNonFunctionalRequirementsPromptCoversEveryQuality(t *testing.T) {
	for name, sentence := range map[string]string{
		"features and fixes":  "A feature or fix design is not done when the behaviour works.",
		"risk, choice, check": "name the risk, the design choice that handles it and the check that proves it",
		"latency":             "- Latency:",
		"resources":           "- Resources:",
		"no leaks":            "bounded and released, never leaked",
		"robustness":          "- Robustness:",
		"no deadlocks":        "deadlocks and lock ordering",
		"no hangs":            "no wait that can block forever or hang the process without a timeout or cancel",
		"maintainability":     "- Maintainability:",
		"extensibility":       "- Extensibility:",
		"user experience":     "- User experience:",
		"stated trade-offs":   "A quality the change worsens is a trade-off to state with its reason.",
		"review bar":          "Review a plan against the same bar",
		"review names them":   "a leak, a deadlock, a possible hang",
	} {
		if !strings.Contains(NonFunctionalRequirementsPrompt, sentence) {
			t.Errorf("NonFunctionalRequirementsPrompt is missing its %s rule: %q", name, sentence)
		}
	}
}

// TestCodingRulesReachEveryCodingRole pins which coding subagent carries which
// rule part: plan diagnoses, general-purpose diagnoses and executes, the
// coding plan reviewer diagnoses and reviews; all three carry the
// non-functional bar. The explorer and the verification agent carry none, and
// office and batch keep the source-neutral reviewer.
func TestCodingRulesReachEveryCodingRole(t *testing.T) {
	for part, carriers := range map[string]map[string]bool{
		NonFunctionalRequirementsPrompt: {"general-purpose": true, "plan": true},
		bugDiagnosisPrompt:              {"general-purpose": true, "plan": true},
		bugExecutionPrompt:              {"general-purpose": true},
		codingReviewPrompt:              {},
	} {
		for _, def := range ActiveDefinitions() {
			if got := strings.Contains(def.SystemPrompt, part); got != carriers[def.Name] {
				t.Errorf("built-in %q carries %.40q = %v, want %v", def.Name, part, got, carriers[def.Name])
			}
		}
	}
	reviewer, ok := ProjectTypeSubagentPrompt("plan-reviewer", "coding")
	if !ok || reviewer != codingPlanReviewerPrompt {
		t.Errorf("coding plan reviewer = %q, %v; want codingPlanReviewerPrompt", reviewer, ok)
	}
	for name, part := range map[string]string{
		"shared opening": planReviewerOpening,
		"shared method":  planReviewerMethod,
		"quality bar":    PlanHandoffQualityPrompt,
		"reproduction":   planReviewerReproductionPrompt,
		"non-functional": NonFunctionalRequirementsPrompt,
		"diagnosis":      bugDiagnosisPrompt,
		"review":         codingReviewPrompt,
	} {
		if !strings.Contains(codingPlanReviewerPrompt, part) {
			t.Errorf("coding plan reviewer lacks its %s part", name)
		}
	}
	if strings.Contains(codingPlanReviewerPrompt, bugExecutionPrompt) {
		t.Error("the reviewer executes nothing and must not carry the executor rule")
	}
	for _, projectType := range []string{"office", "batch"} {
		if prompt, ok := ProjectTypeSubagentPrompt("plan-reviewer", projectType); ok {
			t.Errorf("ProjectTypeSubagentPrompt(plan-reviewer, %q) = %q, want the source-neutral built-in", projectType, prompt)
		}
	}
}

// TestPlanReviewerWriteWordingFollowsProjectType keeps each reviewer's prompt
// true to what its run may do: the office and batch reviewer is read-only and
// says so, the coding reviewer may write what reproduces a plan's claims and
// is never told it cannot, and both share every other sentence.
func TestPlanReviewerWriteWordingFollowsProjectType(t *testing.T) {
	for _, sentence := range []string{"same read-only investigation tools", "never edit files and never try to approve or reject the plan yourself"} {
		if !strings.Contains(planReviewerDefinitionPrompt, sentence) {
			t.Errorf("office/batch reviewer lost %q", sentence)
		}
		if strings.Contains(codingPlanReviewerPrompt, sentence) {
			t.Errorf("coding reviewer still says %q, which its write scope contradicts", sentence)
		}
	}
	if strings.Contains(planReviewerDefinitionPrompt, planReviewerReproductionPrompt) {
		t.Error("office/batch reviewer must not be told it may write")
	}
	for name, sentence := range map[string]string{
		"never production":    "never change the project's production code",
		"new tests only":      "New test files in the project, named the way the project names its tests.",
		"existing read-only":  "Existing files, tests included, are read-only; you may change only files you created in this review.",
		"scratch":             "Anything in the scratch directory the task names.",
		"worktree is free":    "a worktree you create in the scratch directory, where any file may change",
		"uncommitted changes": "carry over the uncommitted changes the problem depends on",
		"read-only shell":     "The shell sees the project read-only",
		"no commits":          "Do not commit, push or switch branches.",
		"inventory":           "end your report with a list of it: each path, what it shows, the command that runs it and the output you observed",
	} {
		if !strings.Contains(codingPlanReviewerPrompt, sentence) {
			t.Errorf("coding reviewer is missing its %s rule: %q", name, sentence)
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
		"PlanHandoffQualityPrompt":        PlanHandoffQualityPrompt,
		"SinglePlanModePrompt":            SinglePlanModePrompt,
		"BugRootCausePrompt":              BugRootCausePrompt,
		"NonFunctionalRequirementsPrompt": NonFunctionalRequirementsPrompt,
		"SubagentUserRulingPrompt":        SubagentUserRulingPrompt,
		"planReviewerDefinitionPrompt":    planReviewerDefinitionPrompt,
		"codingPlanReviewerPrompt":        codingPlanReviewerPrompt,
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
// vocabulary of a coding session: PlanHandoffQualityPrompt is appended to the
// office and batch primary system prompts too, and those conversations must
// not read as code changes in disguise.
func TestAdvisorPromptStaysSourceNeutral(t *testing.T) {
	banned := []string{"codebase", "code review", "refactor", "diff", "pull request", "git", "repository", "monorepo"}
	texts := map[string]string{
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

}
