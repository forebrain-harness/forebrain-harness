package skill

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

// auditSkill parses the built-in audit skill exactly as it ships.
func auditSkill(t *testing.T) *Skill {
	t.Helper()
	raw, err := assetsFS.ReadFile("system_assets/audit/SKILL.md")
	if err != nil {
		t.Fatalf("read audit skill: %v", err)
	}
	parsed, err := Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse audit skill: %v", err)
	}
	if parsed.Name != "audit" || strings.TrimSpace(parsed.Description) == "" {
		t.Fatalf("audit skill frontmatter = %q / %q", parsed.Name, parsed.Description)
	}
	return parsed
}

// TestAuditSkillCoversEveryWorkflow checks that every workflow the skill
// claims to serve is described in it: each variant, all nine audit categories,
// the plan-mode write boundary, the handoff bar every plan meets, the
// unattended selection default and the unattended skip rule for a publication
// that needs a confirmation nobody can give.
func TestAuditSkillCoversEveryWorkflow(t *testing.T) {
	sk := auditSkill(t)
	for name, sentence := range map[string]string{
		"full audit":           "No argument: the full audit - recon, audit, vetting, prioritization and planning",
		"quick":                "- quick: cover only recon hotspots",
		"deep":                 "- deep: cover the whole scope",
		"category":             "- A category (",
		"branch":               "- branch: audit files changed since the merge-base",
		"next":                 "- next: audit direction only",
		"reconcile":            "- reconcile: process the existing plan backlog",
		"issues":               "- issues: publish the backlog's plans that have no issue yet",
		"unknown argument":     "Any other argument is not a variant: list these and ask which one to run.",
		"plan-mode boundary":   "Inside plan mode that is the session's plan file",
		"handoff bar":          "every plan meets the handoff plan quality bar",
		"unattended selection": "select the top 3–5 by the same rule and record that this non-interactive default was used",
		"sensitive skip":       "skip sensitive publication and record the reason instead of asking or publishing anyway",
	} {
		if !strings.Contains(sk.Body, sentence) {
			t.Errorf("audit skill does not describe the %s workflow: missing %q", name, sentence)
		}
	}
	// The category line is the one place the categories are listed, so a
	// category added to the prose cannot be forgotten here.
	start := strings.Index(sk.Body, "- A category (")
	categoryLine := sk.Body[start:]
	if end := strings.IndexByte(categoryLine, '\n'); end >= 0 {
		categoryLine = categoryLine[:end]
	}
	for _, category := range []string{
		"correctness", "security", "performance", "tests for verification coverage", "architecture for tech debt",
		"dependencies for migrations", "tooling for workflow", "docs", "direction, is next",
	} {
		if !strings.Contains(categoryLine, category) {
			t.Errorf("category line does not name %q", category)
		}
	}
}

// TestAuditSkillVariantsAreSingleWords pins the owner's rule for the skill's
// argument: one word per variant, never a combination such as "quick
// security". The argument-hint the surfaces show after /audit lists exactly
// the variants the body defines.
func TestAuditSkillVariantsAreSingleWords(t *testing.T) {
	sk := auditSkill(t)
	hint := strings.TrimSpace(sk.ArgumentHint)
	if !strings.HasPrefix(hint, "[") || !strings.HasSuffix(hint, "]") {
		t.Fatalf("argument-hint = %q, want an optional [a|b|...] list", hint)
	}
	variants := strings.Split(strings.Trim(hint, "[]"), "|")
	want := []string{"quick", "deep", "branch", "next", "reconcile", "issues",
		"correctness", "security", "performance", "tests", "architecture", "dependencies", "tooling", "docs"}
	if strings.Join(variants, "|") != strings.Join(want, "|") {
		t.Errorf("argument-hint variants = %v, want %v", variants, want)
	}
	section := sk.Body[strings.Index(sk.Body, "## Variants"):]
	section = section[:strings.Index(section, "\n## ")]
	for _, variant := range variants {
		if variant == "" || strings.ContainsAny(variant, " \t-") {
			t.Errorf("variant %q is not a single word", variant)
		}
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(variant) + `\b`).MatchString(section) {
			t.Errorf("variant %q is offered but never described", variant)
		}
	}
}

// TestAuditSkillIsSelfContainedAndSourceNeutral keeps the skill usable in every
// project type: it names no other prompt file, provider, model or builtin tool,
// and none of a coding session's vocabulary, since office and batch
// conversations load it too.
func TestAuditSkillIsSelfContainedAndSourceNeutral(t *testing.T) {
	sk := auditSkill(t)
	text := strings.ToLower(sk.Description + "\n" + sk.Body)
	for _, word := range []string{
		"skill.md", ".claude", "~/.forebrain", "forebrain.yaml", "http://", "https://",
		"deepseek", "anthropic", "openai", "chatgpt", "claude", "kimi", "zhipu", "gemini", "gpt",
		"present_files", "web_search", "web_fetch", "user_interaction", "intermediate_tool",
		"enter_plan_mode", "exit_plan_mode", "read_file", "write_file", "edit_file", "apply_patch",
		"subagent_run", "memory_search",
		"codebase", "code review", "refactor", "diff", "pull request", "git", "repository", "monorepo",
	} {
		if strings.Contains(text, word) {
			t.Errorf("audit skill names %q", word)
		}
	}
}

// TestAuditSkillIsDiscoveredAndSlashInvocable runs the path a conversation
// takes: the installed built-in layer is discovered, so the skill reaches the
// catalog the model chooses from, and it activates as a slash command, so a
// user can start it as /audit with arguments.
func TestAuditSkillIsDiscoveredAndSlashInvocable(t *testing.T) {
	home := t.TempDir()
	if err := Install(home); err != nil {
		t.Fatalf("Install: %v", err)
	}
	found, err := (Loader{Roots: AgentSkillRoots(home, home, safety.ProjectContext{}), StateRoot: home}).Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, item := range found {
		activation, err := LoadActivation(item.SkillFile)
		if err != nil || activation.Name != "audit" {
			continue
		}
		if !activation.SlashCommand {
			t.Fatal("the audit skill is not a slash command")
		}
		if !strings.Contains(activation.Content, "# Audit and planning workflows") {
			t.Fatalf("loading the audit skill does not return its workflows: %.200q", activation.Content)
		}
		return
	}
	t.Fatal("the installed audit skill was not discovered")
}
