package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

type UserPromptSubmitOutcome struct {
	Blocked           bool
	StopReason        string
	SystemMessage     string
	AdditionalContext string
}

// AppendAdditionalContext attaches a UserPromptSubmit hook's additional
// context to a turn's input. Every surface renders it the same way through
// this helper, so the terminal and the gateway cannot drift apart on the tag
// or the spacing the model sees.
func AppendAdditionalContext(input, additional string) string {
	additional = strings.TrimSpace(additional)
	if additional == "" {
		return input
	}
	return input + "\n\n<hook_additional_context>\n" + additional + "\n</hook_additional_context>"
}

type PreToolUseOutcome struct {
	Blocked                  bool
	StopReason               string
	SystemMessage            string
	AdditionalContext        string
	PermissionDecision       string
	PermissionDecisionReason string
	UpdatedInput             map[string]any
}

type PermissionRequestOutcome struct {
	Decision string
	Message  string
}

type PostToolUseOutcome struct {
	Blocked              bool
	StopReason           string
	SystemMessage        string
	AdditionalContext    string
	UpdatedMCPToolOutput any
}

type NotificationOutcome struct {
	Blocked           bool
	StopReason        string
	SystemMessage     string
	AdditionalContext string
}

type SessionStartOutcome struct {
	Blocked            bool
	StopReason         string
	SystemMessage      string
	AdditionalContext  string
	InitialUserMessage string
}

type StopOutcome struct {
	Blocked           bool
	StopReason        string
	SystemMessage     string
	AdditionalContext string
}

type matchRequest struct {
	Event   string
	Matcher string
	Hook    appcfg.HookCommand
}

func (r *Runtime) executeEventHooks(ctx context.Context, event string, matchQuery string, payload any) ([]HookResult, error) {
	queries := []string(nil)
	if strings.TrimSpace(matchQuery) != "" {
		queries = []string{matchQuery}
	}
	return r.executeEventHooksWithQueries(ctx, event, queries, payload)
}

func (r *Runtime) executeEventHooksWithQueries(ctx context.Context, event string, matchQueries []string, payload any) ([]HookResult, error) {
	if r == nil || r.Cfg == nil || len(r.Cfg.Hooks) == 0 {
		return nil, nil
	}
	matchers := r.Cfg.Hooks[event]
	if len(matchers) == 0 {
		return nil, nil
	}
	var hooks []matchRequest
	for _, matcher := range matchers {
		matched := len(matchQueries) == 0
		for _, query := range matchQueries {
			if MatchHookMatcher(query, matcher.Matcher) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		for _, hook := range matcher.Hooks {
			if eventRequiresToolIf(event) && strings.TrimSpace(hook.If) != "" {
				toolName, inputText := hookIfToolInput(payload)
				if !appcfg.HookConditionMatches(hook.If, toolName, inputText) {
					continue
				}
			}
			hooks = append(hooks, matchRequest{Event: event, Matcher: matcher.Matcher, Hook: hook})
		}
	}
	out := make([]HookResult, 0, len(hooks))
	for _, item := range hooks {
		var (
			res HookResult
			err error
		)
		switch item.Hook.Type {
		case appcfg.HookTypeCommand:
			res, err = r.executeCommandHook(ctx, item.Hook, payload)
		case appcfg.HookTypePrompt:
			res, err = r.executePromptHook(ctx, item.Hook, payload)
		case appcfg.HookTypeAgent:
			res, err = r.executeAgentHook(ctx, item.Hook, payload)
		case appcfg.HookTypeHTTP:
			res, err = r.executeHTTPHook(ctx, item.Hook, payload)
		default:
			err = fmt.Errorf("unsupported hook type: %s", item.Hook.Type)
		}
		if err != nil {
			if event == EventPermissionRequest {
				continue
			}
			return nil, err
		}
		out = append(out, res)
	}
	return out, nil
}

func (r *Runtime) ExecuteUserPromptSubmit(ctx context.Context, in UserPromptSubmitInput) (UserPromptSubmitOutcome, error) {
	results, err := r.executeEventHooks(ctx, EventUserPromptSubmit, "", in)
	if err != nil {
		return UserPromptSubmitOutcome{}, err
	}
	var out UserPromptSubmitOutcome
	for _, res := range results {
		if res.BlockingError != "" {
			out.Blocked = true
			out.StopReason = res.BlockingError
		}
		if res.Continue != nil && !*res.Continue {
			out.Blocked = true
			out.StopReason = strings.TrimSpace(res.StopReason)
		}
		if strings.TrimSpace(res.SystemMessage) != "" {
			out.SystemMessage = strings.TrimSpace(res.SystemMessage)
		}
		if strings.TrimSpace(res.AdditionalContext) != "" {
			if out.AdditionalContext == "" {
				out.AdditionalContext = strings.TrimSpace(res.AdditionalContext)
			} else {
				out.AdditionalContext += "\n\n" + strings.TrimSpace(res.AdditionalContext)
			}
		}
	}
	return out, nil
}

func (r *Runtime) ExecutePreToolUse(ctx context.Context, in PreToolUseInput) (PreToolUseOutcome, error) {
	results, err := r.executeEventHooks(ctx, EventPreToolUse, in.ToolName, in)
	if err != nil {
		return PreToolUseOutcome{}, err
	}
	var out PreToolUseOutcome
	for _, res := range results {
		if res.BlockingError != "" {
			out.Blocked = true
			out.StopReason = res.BlockingError
		}
		if res.Continue != nil && !*res.Continue {
			out.Blocked = true
			out.StopReason = strings.TrimSpace(res.StopReason)
		}
		decision := strings.ToLower(strings.TrimSpace(res.PermissionDecision))
		if decision == "deny" {
			out.Blocked = true
			if reason := strings.TrimSpace(res.PermissionDecisionReason); reason != "" && out.StopReason == "" {
				out.StopReason = reason
			}
		}
		if !out.Blocked && len(res.UpdatedInput) > 0 {
			out.UpdatedInput = res.UpdatedInput
		}
		if strings.TrimSpace(res.SystemMessage) != "" {
			out.SystemMessage = strings.TrimSpace(res.SystemMessage)
		}
		if strings.TrimSpace(res.AdditionalContext) != "" {
			if out.AdditionalContext == "" {
				out.AdditionalContext = strings.TrimSpace(res.AdditionalContext)
			} else {
				out.AdditionalContext += "\n\n" + strings.TrimSpace(res.AdditionalContext)
			}
		}
	}
	if out.Blocked && strings.TrimSpace(out.StopReason) == "" {
		out.StopReason = "blocked by PreToolUse hook"
	}
	return out, nil
}

func (r *Runtime) ExecutePermissionRequest(ctx context.Context, in PermissionRequestInput) (PermissionRequestOutcome, error) {
	queries := append([]string{in.ToolName}, in.MatcherAliases...)
	results, err := r.executeEventHooksWithQueries(ctx, EventPermissionRequest, queries, in)
	if err != nil {
		return PermissionRequestOutcome{}, err
	}
	var allowed bool
	for _, result := range results {
		decision, message, ok := permissionRequestHookDecision(result)
		if !ok {
			continue
		}
		if decision == "deny" {
			return PermissionRequestOutcome{Decision: decision, Message: message}, nil
		}
		if decision == "allow" {
			allowed = true
		}
	}
	if allowed {
		return PermissionRequestOutcome{Decision: "allow"}, nil
	}
	return PermissionRequestOutcome{}, nil
}

func permissionRequestHookDecision(result HookResult) (string, string, bool) {
	if message := strings.TrimSpace(result.RawStderr); message != "" && result.BlockingError != "" {
		return "deny", message, true
	}
	raw := result.ParsedJSON
	if len(raw) == 0 {
		return "", "", false
	}
	for key := range raw {
		switch key {
		case "continue", "stopReason", "suppressOutput", "systemMessage", "hookSpecificOutput":
		default:
			return "", "", false
		}
	}
	if value, exists := raw["continue"]; exists {
		continueProcessing, ok := value.(bool)
		if !ok || !continueProcessing {
			return "", "", false
		}
	}
	if value, ok := raw["stopReason"]; ok && value != nil {
		return "", "", false
	}
	if value, exists := raw["suppressOutput"]; exists {
		suppress, ok := value.(bool)
		if !ok || suppress {
			return "", "", false
		}
	}
	if value, exists := raw["systemMessage"]; exists && value != nil {
		if _, ok := value.(string); !ok {
			return "", "", false
		}
	}
	specific, ok := raw["hookSpecificOutput"].(map[string]any)
	if !ok {
		return "", "", false
	}
	for key := range specific {
		if key != "hookEventName" && key != "decision" {
			return "", "", false
		}
	}
	if specific["hookEventName"] != EventPermissionRequest {
		return "", "", false
	}
	decision, ok := specific["decision"].(map[string]any)
	if !ok {
		return "", "", false
	}
	for key, value := range decision {
		switch key {
		case "behavior", "message":
		case "updatedInput", "updatedPermissions":
			if value != nil {
				return "", "", false
			}
		case "interrupt":
			if enabled, _ := value.(bool); enabled {
				return "", "", false
			}
		default:
			return "", "", false
		}
	}
	behavior, ok := decision["behavior"].(string)
	if !ok {
		return "", "", false
	}
	if value, exists := decision["message"]; exists && value != nil {
		if _, ok := value.(string); !ok {
			return "", "", false
		}
	}
	if value, exists := decision["interrupt"]; exists {
		interrupt, ok := value.(bool)
		if !ok || interrupt {
			return "", "", false
		}
	}
	switch behavior {
	case "allow":
		return "allow", "", true
	case "deny":
		message, _ := decision["message"].(string)
		message = strings.TrimSpace(message)
		if message == "" {
			message = "PermissionRequest hook denied approval"
		}
		return "deny", message, true
	default:
		return "", "", false
	}
}

func (r *Runtime) ExecutePostToolUse(ctx context.Context, in PostToolUseInput) (PostToolUseOutcome, error) {
	results, err := r.executeEventHooks(ctx, EventPostToolUse, in.ToolName, in)
	if err != nil {
		return PostToolUseOutcome{}, err
	}
	var out PostToolUseOutcome
	for _, res := range results {
		if res.BlockingError != "" {
			out.Blocked = true
			out.StopReason = res.BlockingError
		}
		if res.Continue != nil && !*res.Continue {
			out.Blocked = true
			out.StopReason = strings.TrimSpace(res.StopReason)
		}
		if strings.TrimSpace(res.SystemMessage) != "" {
			out.SystemMessage = strings.TrimSpace(res.SystemMessage)
		}
		if strings.TrimSpace(res.AdditionalContext) != "" {
			if out.AdditionalContext == "" {
				out.AdditionalContext = strings.TrimSpace(res.AdditionalContext)
			} else {
				out.AdditionalContext += "\n\n" + strings.TrimSpace(res.AdditionalContext)
			}
		}
		if res.UpdatedMCPToolOutput != nil {
			out.UpdatedMCPToolOutput = res.UpdatedMCPToolOutput
		}
	}
	return out, nil
}

func (r *Runtime) ExecutePostToolUseFailure(ctx context.Context, in PostToolUseFailureInput) (PostToolUseOutcome, error) {
	results, err := r.executeEventHooks(ctx, EventPostToolUseFailure, in.ToolName, in)
	if err != nil {
		return PostToolUseOutcome{}, err
	}
	var out PostToolUseOutcome
	for _, res := range results {
		if res.BlockingError != "" {
			out.Blocked = true
			out.StopReason = res.BlockingError
		}
		if res.Continue != nil && !*res.Continue {
			out.Blocked = true
			out.StopReason = strings.TrimSpace(res.StopReason)
		}
		if strings.TrimSpace(res.SystemMessage) != "" {
			out.SystemMessage = strings.TrimSpace(res.SystemMessage)
		}
		if strings.TrimSpace(res.AdditionalContext) != "" {
			if out.AdditionalContext == "" {
				out.AdditionalContext = strings.TrimSpace(res.AdditionalContext)
			} else {
				out.AdditionalContext += "\n\n" + strings.TrimSpace(res.AdditionalContext)
			}
		}
	}
	return out, nil
}

func (r *Runtime) ExecuteNotification(ctx context.Context, in NotificationInput) (NotificationOutcome, error) {
	results, err := r.executeEventHooks(ctx, EventNotification, in.NotificationType, in)
	if err != nil {
		return NotificationOutcome{}, err
	}
	var out NotificationOutcome
	for _, res := range results {
		if res.BlockingError != "" {
			out.Blocked = true
			out.StopReason = res.BlockingError
		}
		if res.Continue != nil && !*res.Continue {
			out.Blocked = true
			out.StopReason = strings.TrimSpace(res.StopReason)
		}
		if strings.TrimSpace(res.SystemMessage) != "" {
			out.SystemMessage = strings.TrimSpace(res.SystemMessage)
		}
		if strings.TrimSpace(res.AdditionalContext) != "" {
			if out.AdditionalContext == "" {
				out.AdditionalContext = strings.TrimSpace(res.AdditionalContext)
			} else {
				out.AdditionalContext += "\n\n" + strings.TrimSpace(res.AdditionalContext)
			}
		}
	}
	return out, nil
}

func (r *Runtime) ExecuteSessionStart(ctx context.Context, in SessionStartInput) (SessionStartOutcome, error) {
	results, err := r.executeEventHooks(ctx, EventSessionStart, in.Source, in)
	if err != nil {
		return SessionStartOutcome{}, err
	}
	var out SessionStartOutcome
	for _, res := range results {
		if res.BlockingError != "" {
			out.Blocked = true
			out.StopReason = res.BlockingError
		}
		if res.Continue != nil && !*res.Continue {
			out.Blocked = true
			out.StopReason = strings.TrimSpace(res.StopReason)
		}
		if strings.TrimSpace(res.SystemMessage) != "" {
			out.SystemMessage = strings.TrimSpace(res.SystemMessage)
		}
		if strings.TrimSpace(res.AdditionalContext) != "" {
			if out.AdditionalContext == "" {
				out.AdditionalContext = strings.TrimSpace(res.AdditionalContext)
			} else {
				out.AdditionalContext += "\n\n" + strings.TrimSpace(res.AdditionalContext)
			}
		}
		if strings.TrimSpace(res.InitialUserMessage) != "" {
			out.InitialUserMessage = strings.TrimSpace(res.InitialUserMessage)
		}
	}
	return out, nil
}

func (r *Runtime) ExecuteCompact(ctx context.Context, event string, in CompactInput) (StopOutcome, error) {
	if event != EventPreCompact && event != EventPostCompact {
		return StopOutcome{}, fmt.Errorf("unsupported compact hook event %q", event)
	}
	results, err := r.executeEventHooks(ctx, event, strings.TrimSpace(in.Trigger), in)
	if err != nil {
		return StopOutcome{}, err
	}
	var out StopOutcome
	for _, res := range results {
		if res.BlockingError != "" {
			out.Blocked, out.StopReason = true, res.BlockingError
		}
		if res.Continue != nil && !*res.Continue {
			out.Blocked, out.StopReason = true, strings.TrimSpace(res.StopReason)
		}
	}
	return out, nil
}

func (r *Runtime) ExecuteStop(ctx context.Context, in StopInput) (StopOutcome, error) {
	results, err := r.executeEventHooks(ctx, EventStop, "", in)
	if err != nil {
		return StopOutcome{}, err
	}
	var out StopOutcome
	for _, res := range results {
		if res.BlockingError != "" {
			out.Blocked = true
			out.StopReason = res.BlockingError
		}
		if res.Continue != nil && !*res.Continue {
			out.Blocked = true
			out.StopReason = strings.TrimSpace(res.StopReason)
		}
		if strings.TrimSpace(res.SystemMessage) != "" {
			out.SystemMessage = strings.TrimSpace(res.SystemMessage)
		}
		if strings.TrimSpace(res.AdditionalContext) != "" {
			if out.AdditionalContext == "" {
				out.AdditionalContext = strings.TrimSpace(res.AdditionalContext)
			} else {
				out.AdditionalContext += "\n\n" + strings.TrimSpace(res.AdditionalContext)
			}
		}
	}
	return out, nil
}

func (r *Runtime) ExecuteSubagentStop(ctx context.Context, in SubagentStopInput) (StopOutcome, error) {
	results, err := r.executeEventHooks(ctx, EventSubagentStop, strings.TrimSpace(in.AgentType), in)
	if err != nil {
		return StopOutcome{}, err
	}
	var out StopOutcome
	for _, res := range results {
		if res.BlockingError != "" {
			out.Blocked = true
			out.StopReason = res.BlockingError
		}
		if res.Continue != nil && !*res.Continue {
			out.Blocked = true
			out.StopReason = strings.TrimSpace(res.StopReason)
		}
		if strings.TrimSpace(res.SystemMessage) != "" {
			out.SystemMessage = strings.TrimSpace(res.SystemMessage)
		}
		if strings.TrimSpace(res.AdditionalContext) != "" {
			if out.AdditionalContext == "" {
				out.AdditionalContext = strings.TrimSpace(res.AdditionalContext)
			} else {
				out.AdditionalContext += "\n\n" + strings.TrimSpace(res.AdditionalContext)
			}
		}
	}
	return out, nil
}

func eventRequiresToolIf(event string) bool {
	switch strings.TrimSpace(event) {
	case EventPreToolUse, EventPermissionRequest, EventPostToolUse, EventPostToolUseFailure:
		return true
	default:
		return false
	}
}

func hookIfToolInput(payload any) (string, string) {
	switch in := payload.(type) {
	case PreToolUseInput:
		return strings.TrimSpace(in.ToolName), hookToolInputString(in.ToolInput)
	case PermissionRequestInput:
		return strings.TrimSpace(in.ToolName), hookToolInputString(in.ToolInput)
	case PostToolUseInput:
		return strings.TrimSpace(in.ToolName), hookToolInputString(in.ToolInput)
	case PostToolUseFailureInput:
		return strings.TrimSpace(in.ToolName), hookToolInputString(in.ToolInput)
	default:
		return "", ""
	}
}

func hookToolInputString(v map[string]any) string {
	if len(v) == 0 {
		return ""
	}
	if cmd, ok := v["command"].(string); ok {
		return strings.TrimSpace(cmd)
	}
	b, _ := json.Marshal(v)
	return strings.TrimSpace(string(b))
}

// WriteSessionTranscriptArtifact materialises the session transcript hooks read
// from disk. workspaceRoot must be the owning agent's workspace directory
// (Runtime.StateRoot), never the shared FOREBRAIN_HOME: this file is a verbatim
// dump of one tenant's conversation, and every primary agent can read the home.
func WriteSessionTranscriptArtifact(workspaceRoot string, sess *state.SessionStore, sessionID string) (string, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return "", fmt.Errorf("workspace root required")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "default"
	}
	path := filepath.Join(workspaceRoot, "state", "hook-transcripts", sanitizePathSegment(sessionID)+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if sess == nil {
		return path, os.WriteFile(path, nil, 0o600)
	}
	// Bounded: this artifact is rewritten on every tool call and user prompt
	// (hot path). An unbounded read here is O(N) per call → O(N²) per session
	// and rewrites an ever-growing file. The cap keeps the per-call cost flat;
	// hooks needing the full history can read the session store directly.
	turns, err := sess.ListTranscriptRecent(context.Background(), sessionID, 2000)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	for _, turn := range turns {
		row := map[string]any{
			"id":         turn.ID,
			"role":       turn.Role,
			"content":    turn.Content,
			"created_at": turn.CreatedAt,
		}
		line, _ := json.Marshal(row)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// SidechainTranscriptPath points a SubagentStop hook at the subagent's message
// log. workspaceRoot carries the same tenancy requirement as
// WriteSessionTranscriptArtifact, and must be the SAME root the subagent's fork
// wrote with or the hook is handed a path to a file that does not exist.
func SidechainTranscriptPath(workspaceRoot string, sessionID, agentID string) string {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	sessionID = strings.TrimSpace(sessionID)
	agentID = strings.TrimSpace(agentID)
	if workspaceRoot == "" || sessionID == "" || agentID == "" {
		return ""
	}
	return filepath.Join(workspaceRoot, "state", "fork-sidechain", sanitizePathSegment(sessionID), "subagent-"+sanitizePathSegment(agentID)+".jsonl")
}

func sanitizePathSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
}
