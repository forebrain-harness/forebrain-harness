package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// runtimeGoldenValues is the model-visible text this package sends for office
// and batch conversations, keyed exactly as the golden file is. It exists so
// the golden comparison asserts the key set too: a constant added without a
// golden entry (or the reverse) fails rather than passing by silence.
func runtimeGoldenValues() map[string]any {
	return map[string]any{
		"OfficeWorkPrompt":            OfficeWorkPrompt,
		"BatchTaskPrompt":             BatchTaskPrompt,
		"OfficeNotesPromptSuffix":     OfficeNotesPromptSuffix,
		"officeGeneralPurposePrompt":  officeGeneralPurposePrompt,
		"officeExplorePrompt":         officeExplorePrompt,
		"batchGeneralPurposePrompt":   batchGeneralPurposePrompt,
		"batchExplorePrompt":          batchExplorePrompt,
		"OfficeSubagentEnumSentences": stringsToAny(OfficeSubagentEnumSentences),
		"BatchSubagentEnumSentences":  stringsToAny(BatchSubagentEnumSentences),
	}
}

func stringsToAny(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// TestProjectTypePromptsGolden pins every office and batch prompt byte for
// byte: these constants are the model-visible contract and the prefix-cache
// baseline, so a change to any one of them must be deliberate, in the plan
// text first, and reflected in the golden in the same diff. The test never
// rewrites the golden.
func TestProjectTypePromptsGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "project_type_prompts.golden.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden map[string]any
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	want := runtimeGoldenValues()
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

// TestOfficePromptsSayNothingAboutCode keeps the office and batch wording
// free of the vocabulary of a coding session: these conversations must not
// read as code changes in disguise.
func TestOfficePromptsSayNothingAboutCode(t *testing.T) {
	texts := map[string]string{
		"OfficeWorkPrompt":           OfficeWorkPrompt,
		"BatchTaskPrompt":            BatchTaskPrompt,
		"OfficeNotesPromptSuffix":    OfficeNotesPromptSuffix,
		"officeGeneralPurposePrompt": officeGeneralPurposePrompt,
		"officeExplorePrompt":        officeExplorePrompt,
		"batchGeneralPurposePrompt":  batchGeneralPurposePrompt,
		"batchExplorePrompt":         batchExplorePrompt,
	}
	banned := []string{"code review", "diff", "git", "repository", "codebase", "refactor"}
	for name, text := range texts {
		lower := strings.ToLower(text)
		for _, word := range banned {
			if strings.Contains(lower, word) {
				t.Errorf("%s contains %q", name, word)
			}
		}
	}
}

// TestOfficePromptsNameTheDeliveryTool checks the one tool the main prompts
// name: present_files is how a finished deliverable reaches the user, so the
// main prompts must say so unambiguously.
func TestOfficePromptsNameTheDeliveryTool(t *testing.T) {
	for name, text := range map[string]string{
		"OfficeWorkPrompt": OfficeWorkPrompt,
		"BatchTaskPrompt":  BatchTaskPrompt,
	} {
		if !strings.Contains(text, "present_files") {
			t.Errorf("%s does not name present_files", name)
		}
	}
}

// TestBatchPromptNeverAsksTheUser pins the unattended contract: the batch
// prompts each carry their own never-ask wording and none of them may tell
// the model to ask anyone anything.
func TestBatchPromptNeverAsksTheUser(t *testing.T) {
	if !strings.Contains(BatchTaskPrompt, "You cannot ask anyone anything.") {
		t.Error("BatchTaskPrompt lost its never-ask clause")
	}
	if !strings.Contains(batchGeneralPurposePrompt, "do not ask anyone anything") {
		t.Error("batchGeneralPurposePrompt lost its never-ask clause")
	}
	if !strings.Contains(batchExplorePrompt, "Do not ask questions.") {
		t.Error("batchExplorePrompt lost its never-ask clause")
	}
	for name, text := range map[string]string{
		"BatchTaskPrompt":           BatchTaskPrompt,
		"batchGeneralPurposePrompt": batchGeneralPurposePrompt,
		"batchExplorePrompt":        batchExplorePrompt,
	} {
		if strings.Contains(strings.ToLower(text), "ask the user") {
			t.Errorf("%s tells the model to ask the user", name)
		}
	}
}

// TestBatchSubagentsUseTaskWorkspacePrompts keeps the batch subagents inside
// the task workspace contract, and keeps that wording apart from the office
// one: the two run under different filesystem and interaction rules.
func TestBatchSubagentsUseTaskWorkspacePrompts(t *testing.T) {
	for _, marker := range []string{"inputs/", "outputs/", ".work/"} {
		if !strings.Contains(batchGeneralPurposePrompt, marker) {
			t.Errorf("batchGeneralPurposePrompt does not mention %s", marker)
		}
	}
	if batchGeneralPurposePrompt == officeGeneralPurposePrompt {
		t.Error("batch and office share the general-purpose subagent prompt")
	}
	if !strings.Contains(batchExplorePrompt, "read-only") {
		t.Error("batchExplorePrompt does not say it is read-only")
	}
	if !strings.Contains(batchExplorePrompt, "unable to verify") {
		t.Error("batchExplorePrompt does not report unable-to-verify findings")
	}
}

// TestOfficeSubagentPromptCoversExactlyTheOfficeNames pins the coverage of
// the project-type subagent wording: exactly the two office names have a
// prompt and an enum sentence, and only for office and batch conversations.
func TestOfficeSubagentPromptCoversExactlyTheOfficeNames(t *testing.T) {
	for _, projectType := range []string{"office", "batch"} {
		for _, name := range OfficeSubagentNames {
			if _, ok := ProjectTypeSubagentPrompt(name, projectType); !ok {
				t.Errorf("ProjectTypeSubagentPrompt(%q, %q) has no prompt", name, projectType)
			}
		}
	}
	sentenceSets := map[string]map[string]string{
		"office": OfficeSubagentEnumSentences,
		"batch":  BatchSubagentEnumSentences,
	}
	for projectType, sentences := range sentenceSets {
		for _, name := range OfficeSubagentNames {
			if _, ok := sentences[name]; !ok {
				t.Errorf("%s enum sentences are missing %q", projectType, name)
			}
		}
	}
	if len(OfficeSubagentEnumSentences) != len(OfficeSubagentNames) {
		t.Errorf("OfficeSubagentEnumSentences has %d entries, want %d", len(OfficeSubagentEnumSentences), len(OfficeSubagentNames))
	}
	if len(BatchSubagentEnumSentences) != len(OfficeSubagentNames) {
		t.Errorf("BatchSubagentEnumSentences has %d entries, want %d", len(BatchSubagentEnumSentences), len(OfficeSubagentNames))
	}
	for _, name := range []string{"plan", "verification"} {
		for _, projectType := range []string{"office", "batch"} {
			if prompt, ok := ProjectTypeSubagentPrompt(name, projectType); ok {
				t.Errorf("ProjectTypeSubagentPrompt(%q, %q) = %q, want no prompt", name, projectType, prompt)
			}
		}
		if _, ok := OfficeSubagentEnumSentences[name]; ok {
			t.Errorf("OfficeSubagentEnumSentences has a sentence for %q", name)
		}
		if _, ok := BatchSubagentEnumSentences[name]; ok {
			t.Errorf("BatchSubagentEnumSentences has a sentence for %q", name)
		}
	}
	if prompt, ok := ProjectTypeSubagentPrompt("general-purpose", "coding"); ok {
		t.Errorf("ProjectTypeSubagentPrompt(general-purpose, coding) = %q, want no prompt", prompt)
	}
}
