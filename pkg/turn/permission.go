// Permission evaluation and the snapshot facade.
package turn

import (
	"fmt"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

// PermissionRuleCount returns the number of effective permission rules.
func PermissionRuleCount(snapshot safety.Snapshot) int {
	count := 0
	for _, byBehavior := range snapshot.Rules {
		for _, rules := range byBehavior {
			count += len(rules)
		}
	}
	return count
}

// PermissionsOf resolves what a session's permission state adds up to, the way
// /status and /permissions both name it: the preset its approval mode and
// sandbox match, or — between presets — the two halves it is made of. The
// sandbox is the session's own when it picked one.
func PermissionsOf(snapshot safety.Snapshot, cfg *appcfg.Root) StatusPermissions {
	cfg = safety.ConfigForSnapshot(cfg, snapshot)
	perm := StatusPermissions{Rules: PermissionRuleCount(snapshot)}
	if preset, ok := safety.MatchApprovalPreset(snapshot.Mode, cfg); ok {
		perm.Matched, perm.Preset, perm.Description = true, preset.Label, preset.DescriptionFor(cfg)
		return perm
	}
	perm.Approval = string(snapshot.Mode)
	perm.Sandbox = safety.SandboxModeLabel(cfg)
	return perm
}

// PermissionsReport says what Forebrain Harness may do in a session now: the preset in
// its own words, or the custom state's two halves in words, then the rules
// that refine it and where each comes from.
func PermissionsReport(p StatusPermissions, snapshot safety.Snapshot) string {
	head := p.Preset + ": " + p.Description
	if !p.Matched {
		head = "Custom: " + sandboxInWords(p.Sandbox) + ", and " + approvalInWords(p.Approval) + "."
	}
	return head + "\n\n" + permissionRulesInWords(snapshot)
}

func sandboxInWords(mode string) string {
	switch appcfg.SandboxMode(strings.TrimSpace(mode)) {
	case appcfg.SandboxModeReadOnly:
		return "commands can read files but not change them"
	case appcfg.SandboxModeWorkspaceWrite:
		return "commands can read and edit files in the workspace"
	case appcfg.SandboxModeDangerFullAccess:
		return "commands run without a sandbox"
	case "":
		return "no sandbox is configured"
	}
	return fmt.Sprintf("the %s permission profile decides what commands can reach", strings.TrimSpace(mode))
}

func approvalInWords(mode string) string {
	switch safety.PermissionMode(strings.TrimSpace(mode)) {
	case safety.ModeOnRequest:
		return "Forebrain Harness asks before going further"
	case safety.ModeNever:
		return "Forebrain Harness never asks for approval"
	case safety.ModeUnlessTrusted:
		return "Forebrain Harness asks before running anything not known to be safe"
	case safety.ModeGranular:
		return "approval follows the configured per-kind rules"
	}
	return "the approval policy is " + orDash(mode)
}

// permissionRulesInWords is one sentence naming the allow, deny and ask rules
// in force and where they come from.
func permissionRulesInWords(snapshot safety.Snapshot) string {
	var groups []string
	for _, source := range []safety.PermissionSource{safety.SourceProjectSettings, safety.SourceLocalSettings, safety.SourceSession} {
		byBehavior := snapshot.Rules[source]
		var counts []string
		for _, behavior := range []safety.PermissionBehavior{safety.BehaviorAllow, safety.BehaviorDeny, safety.BehaviorAsk} {
			if n := len(byBehavior[behavior]); n > 0 {
				counts = append(counts, fmt.Sprintf("%d %s", n, behavior))
			}
		}
		if len(counts) == 0 {
			continue
		}
		where := map[safety.PermissionSource]string{
			safety.SourceProjectSettings: "from the project's settings",
			safety.SourceLocalSettings:   "from your local settings",
			safety.SourceSession:         "granted in this session",
		}[source]
		groups = append(groups, strings.Join(counts, " and ")+" "+where)
	}
	if len(groups) == 0 {
		return "No allow, deny or ask rules refine it."
	}
	return "Rules: " + strings.Join(groups, "; ") + "."
}

// FormatPermissionExplain describes a permission decision and its evidence.
func FormatPermissionExplain(tool, input string, result safety.ExplainResult) string {
	tool = strings.TrimSpace(tool)
	input = strings.TrimSpace(input)
	decision := result.Decision
	var b strings.Builder
	b.WriteString("Permissions: " + orDash(tool) + "\n")
	if input != "" {
		b.WriteString("  " + clip(input, 160) + "\n")
	}
	b.WriteString("\n" + permissionOutcomeLine(decision.Behavior) + "\n")
	b.WriteString("  " + permissionOutcomeReason(decision) + "\n")
	matched := make([]safety.ExplainRule, 0, len(result.Rules))
	for _, rule := range result.Rules {
		if rule.Matched {
			matched = append(matched, rule)
		}
	}
	switch {
	case len(matched) > 0:
		b.WriteString("\nMatched rules\n")
		for _, rule := range matched {
			b.WriteString(fmt.Sprintf("  %-5s %s  (%s)\n", orDash(string(rule.Behavior)), orDash(strings.TrimSpace(rule.RuleContent)), permissionSourceName(rule.Source)))
		}
	case len(result.Rules) == 0:
		b.WriteString("\nNo rules are configured for this tool.\n")
	case len(result.Rules) == 1:
		b.WriteString("\nThe one rule for this tool did not match.\n")
	default:
		b.WriteString(fmt.Sprintf("\nNone of the %d rules for this tool matched.\n", len(result.Rules)))
	}
	b.WriteString("\nApproval mode: " + orDash(string(decision.Mode)))
	if reason := strings.TrimSpace(decision.Reason); reason != "" {
		b.WriteString("  ·  reason: " + reason)
	}
	return strings.TrimSpace(b.String())
}

// PermissionExplainUsage is the /permissions explain help text. Both surfaces
// printed it verbatim; it lives here next to the function that consumes it so
// the two cannot drift.
const PermissionExplainUsage = "/permissions explain <tool> [input]\n" +
	"  Shows what would happen if Forebrain Harness ran this, and which rule decided it.\n" +
	"  example: /permissions explain Bash \"npm run build\""

// PermissionsUsage is the /permissions help text. presetPicker says whether the
// surface can open the preset picker; the terminal can and the web chat cannot,
// and that capability gap is the only thing the two legitimately differ on
// here. Expressing it as a flag keeps one copy of the text rather than two that
// drift apart line by line.
func PermissionsUsage(presetPicker bool) string {
	const head = "/permissions | /permissions explain <tool> [input]\n"
	const rules = "  Project rules are declared in <project>/.forebrain/safety.json."
	if !presetPicker {
		return head + rules
	}
	return head +
		"  Bare /permissions opens the preset picker, which selects the sandbox and\n" +
		"  when Forebrain Harness asks for approval together. /sandbox reports the result.\n" +
		rules
}

func ExecutePermissionExplain(facade PermissionFacade, sessionID string, args []string, usage string) string {
	if facade == nil {
		return "permissions: unavailable"
	}
	if len(args) == 0 {
		return usage
	}
	tool := strings.TrimSpace(args[0])
	if tool == "" {
		return usage
	}
	input := ""
	if len(args) > 1 {
		input = strings.TrimSpace(strings.Join(args[1:], " "))
	}
	return FormatPermissionExplain(tool, input, facade.ExplainPermissionForSession(sessionID, tool, input))
}

func permissionOutcomeLine(behavior safety.PermissionBehavior) string {
	switch behavior {
	case safety.BehaviorAllow:
		return "Allowed — runs without asking."
	case safety.BehaviorDeny:
		return "Blocked — refused before it runs."
	default:
		return "Asks first — you approve or reject it when it comes up."
	}
}

func permissionOutcomeReason(decision safety.Decision) string {
	matched := decision.Matched
	if matched == nil {
		switch decision.Behavior {
		case safety.BehaviorAllow:
			return "No rule covers this, so the default for this tool applies."
		case safety.BehaviorDeny:
			return fmt.Sprintf("No rule covers this, and the default under the %s approval mode is to refuse.", orDash(string(decision.Mode)))
		default:
			return "No rule covers this, so it falls back to asking."
		}
	}
	rule := permissionRulePhrase(matched.Behavior) + " in " + permissionSourceLabel(matched.Source)
	if matched.Behavior == decision.Behavior {
		return rule + " matched."
	}
	return fmt.Sprintf("%s matched, and the %s approval mode turned that into %s.", rule, orDash(string(decision.Mode)), decision.Behavior)
}

func permissionRulePhrase(behavior safety.PermissionBehavior) string {
	if behavior == safety.BehaviorAllow || behavior == safety.BehaviorAsk {
		return "An " + string(behavior) + " rule"
	}
	return "A " + string(behavior) + " rule"
}

func permissionSourceName(source safety.PermissionSource) string {
	switch source {
	case safety.SourceSession:
		return "this session"
	case safety.SourceLocalSettings:
		return "your local settings"
	case safety.SourceProjectSettings:
		return "project settings"
	default:
		return orDash(string(source))
	}
}

func permissionSourceLabel(source safety.PermissionSource) string {
	if source == safety.SourceProjectSettings {
		return "project settings (.forebrain/safety.json)"
	}
	return permissionSourceName(source)
}

func clip(text string, limit int) string {
	text = strings.ReplaceAll(text, "\n", " ")
	if limit > 0 && len(text) > limit {
		return llm.TruncateBytes(text, limit, "…")
	}
	return text
}

func orDash(text string) string {
	if text = strings.TrimSpace(text); text == "" {
		return "-"
	}
	return text
}

func (s *Service) PermissionSnapshot() safety.Snapshot {
	if s == nil || s.permissionFacade == nil {
		return safety.Snapshot{
			Mode:  safety.ModeOnRequest,
			Rules: map[safety.PermissionSource]map[safety.PermissionBehavior][]safety.PermissionRuleValue{},
		}
	}
	return s.permissionFacade.PermissionSnapshot()
}

func (s *Service) PermissionSnapshotForSession(sessionID string) safety.Snapshot {
	if s == nil || s.permissionFacade == nil {
		return s.PermissionSnapshot()
	}
	return s.permissionFacade.PermissionSnapshotForSession(sessionID)
}

func (s *Service) EvaluatePermission(toolName, input string) safety.Decision {
	if s == nil || s.permissionFacade == nil {
		return safety.Decision{
			Behavior: safety.BehaviorAsk,
			Mode:     safety.ModeOnRequest,
			Reason:   "permission_facade_unavailable",
		}
	}
	return s.permissionFacade.EvaluatePermission(toolName, input)
}

func (s *Service) EvaluatePermissionForSession(sessionID, toolName, input string) safety.Decision {
	if s == nil || s.permissionFacade == nil {
		return s.EvaluatePermission(toolName, input)
	}
	return s.permissionFacade.EvaluatePermissionForSession(sessionID, toolName, input)
}

func (s *Service) ExplainPermission(toolName, input string) safety.ExplainResult {
	if s == nil || s.permissionFacade == nil {
		return safety.ExplainResult{
			Decision: safety.Decision{
				Behavior: safety.BehaviorAsk,
				Mode:     safety.ModeOnRequest,
				Reason:   "permission_facade_unavailable",
			},
		}
	}
	return s.permissionFacade.ExplainPermission(toolName, input)
}

func (s *Service) ExplainPermissionForSession(sessionID, toolName, input string) safety.ExplainResult {
	if s == nil || s.permissionFacade == nil {
		return s.ExplainPermission(toolName, input)
	}
	return s.permissionFacade.ExplainPermissionForSession(sessionID, toolName, input)
}

func (s *Service) ApplyPermissionUpdate(update safety.PermissionUpdate) {
	if s == nil || s.permissionFacade == nil {
		return
	}
	s.permissionFacade.ApplyPermissionUpdate(update)
}
