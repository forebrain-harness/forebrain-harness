package agent

import (
	"strings"
	"testing"
)

func TestScopeDisciplinePromptIsProviderNeutralAndCoversTheFullContract(t *testing.T) {
	// Every stage of the discipline must survive an edit to the prose: locking
	// the contract, reusing what exists, the smallest sufficient change,
	// risk-proportionate proof, resisting review-driven expansion, and the stop
	// rule.
	for _, want := range []string{
		"Scope discipline",
		"Lock the contract before editing",
		"Reuse what the project already has",
		"Choose the smallest sufficient change",
		"Verify in proportion to risk",
		"Resist review-driven expansion",
		"Stop when the contract is met",
	} {
		if !strings.Contains(ScopeDisciplinePrompt, want) {
			t.Fatalf("prompt missing section %q", want)
		}
	}
	// The root-cause rule must stay attached to "smallest", or the prompt reads
	// as a licence for the shallow symptom patches this project forbids.
	if !strings.Contains(ScopeDisciplinePrompt, "least code that resolves the actual cause") {
		t.Fatal("prompt must tie the smallest change to the actual cause, not to the shallowest patch")
	}
	// The guardrail keeps the prompt from cutting rigor along with waste.
	if !strings.Contains(ScopeDisciplinePrompt, "cuts waste, not rigor") {
		t.Fatal("prompt missing the guardrail against skipping mandatory work")
	}
	// It reaches every provider forebrain talks to, so it must not name one model,
	// vendor, or tool.
	for _, banned := range []string{"GPT", "Claude", "Anthropic", "OpenAI", "DeepSeek", "Kimi", "Qwen", "intermediate_tool", "subagent_"} {
		if strings.Contains(ScopeDisciplinePrompt, banned) {
			t.Fatalf("prompt is provider-specific: contains %q", banned)
		}
	}
}

// TestScopeDisciplineReusesBeforeInventing keeps the reuse rule's load-bearing
// facts: the search comes before design, feature and bug work alike, it looks
// by behavior and along the whole path rather than by the new thing's name,
// anything new must say what it passed over, and the project keeps one meaning,
// one implementation and one layer per concern.
func TestScopeDisciplineReusesBeforeInventing(t *testing.T) {
	for name, sentence := range map[string]string{
		"all three kinds of work":  "Before you design a change, build a feature, or locate and fix a bug",
		"whole-project search":     "search the whole project for what already covers the same ground",
		"search by behavior":       "Search by behavior and symptom as well as by name",
		"surrounding layers":       "including the layers around it that retry, recover, translate errors or report",
		"build on it":              "Build on what exists",
		"new needs a reason":       "Add something new only when nothing existing can serve, and say what you found and why it does not fit",
		"plans name what they saw": "a plan that proposes new machinery names the existing pieces it weighed",
		"one of each":              "Keep one meaning per concept, one implementation per behavior, one layer per responsibility",
		"no overlapping layer":     "never add a near-duplicate concept, state, error, message, logic path or overlapping layer",
		"existing duplicates":      "otherwise reported in one line",
	} {
		if !strings.Contains(ScopeDisciplinePrompt, sentence) {
			t.Errorf("ScopeDisciplinePrompt is missing its %s rule: %q", name, sentence)
		}
	}
}

// TestScopeDisciplineReachesOnlyTheSubagentsThatProduceOverengineerableWork
// pins the deliberate split. general-purpose implements and plan proposes an
// implementation, so both can overengineer. The read-only explorer has nothing
// to make smaller, the verification agent's job is adversarial breadth that
// "do not run checks by reflex" would undercut, and the caveman and judge
// agents carry output contracts this block's normal-English prose would break.
func TestScopeDisciplineReachesOnlyTheSubagentsThatProduceOverengineerableWork(t *testing.T) {
	carries := map[string]bool{"general-purpose": true, "plan": true}
	for _, def := range ActiveDefinitions() {
		got := strings.Contains(def.SystemPrompt, ScopeDisciplinePrompt)
		if got != carries[def.Name] {
			t.Errorf("subagent %q carries scope discipline = %v, want %v", def.Name, got, carries[def.Name])
		}
	}
	// The plan agent produces no diff, so it needs the translation that gives
	// the shared block's closing test a referent.
	plan, err := ResolveSubtype("plan")
	if err != nil {
		t.Fatalf("resolve plan: %v", err)
	}
	if !strings.Contains(plan.SystemPrompt, planScopeDisciplineAddendum) {
		t.Fatal("plan subagent missing the planning translation of the scope discipline")
	}
	gp, err := ResolveSubtype("general-purpose")
	if err != nil {
		t.Fatalf("resolve general-purpose: %v", err)
	}
	if strings.Contains(gp.SystemPrompt, planScopeDisciplineAddendum) {
		t.Fatal("general-purpose subagent must not carry the planning-only addendum")
	}
}
