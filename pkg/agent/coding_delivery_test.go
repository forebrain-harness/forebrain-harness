package agent

import (
	"strings"
	"testing"
)

// TestCodingDeliveryPromptNamesTheDeliveryTool checks the one tool the
// coding delivery rule names: present_files is how a finished deliverable
// reaches the user, so the rule must say so unambiguously.
func TestCodingDeliveryPromptNamesTheDeliveryTool(t *testing.T) {
	if !strings.Contains(CodingDeliveryPrompt, "present_files") {
		t.Error("CodingDeliveryPrompt does not name present_files")
	}
}

// TestCodingDeliveryPromptCoversTheCompletenessRule pins the owner's
// completeness ruling (2026-11-18): every file the turn touched is a
// deliverable — no kind excluded — with the boundaries that keep the set
// honest (not another agent's work, deleted files named in prose, turns
// that changed nothing answer directly).
func TestCodingDeliveryPromptCoversTheCompletenessRule(t *testing.T) {
	for _, want := range []string{
		"every file this turn created or modified",
		"No kind of file is excluded",
		"A turn that changed no files answers directly",
		"another agent's concurrent changes",
		"A deleted file cannot be presented",
		"deliver exactly those",
		"When you judge the turn's work complete",
	} {
		if !strings.Contains(CodingDeliveryPrompt, want) {
			t.Errorf("CodingDeliveryPrompt missing %q", want)
		}
	}
}

// TestCodingDeliveryPromptIsProviderNeutralAndNamesNoOtherTool keeps the
// rule reachable by every provider and free of tool names it does not own:
// naming another tool would teach the model the wrong call.
func TestCodingDeliveryPromptIsProviderNeutralAndNamesNoOtherTool(t *testing.T) {
	lower := strings.ToLower(CodingDeliveryPrompt)
	for _, banned := range []string{
		"deepseek", "anthropic", "openai", "chatgpt", "kimi", "zhipu", "gemini",
		"gpt", "claude", "skill.md", ".claude", "~/.forebrain", "forebrain.yaml",
		"http://", "https://",
		"write_file", "edit_file", "read_file", "apply_patch", "subagent_run", "git",
	} {
		if strings.Contains(lower, banned) {
			t.Errorf("CodingDeliveryPrompt is not neutral: contains %q", banned)
		}
	}
}
