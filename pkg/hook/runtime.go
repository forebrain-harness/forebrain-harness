package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

const (
	EventPreToolUse         = "PreToolUse"
	EventPermissionRequest  = "PermissionRequest"
	EventPostToolUse        = "PostToolUse"
	EventPostToolUseFailure = "PostToolUseFailure"
	EventNotification       = "Notification"
	EventUserPromptSubmit   = "UserPromptSubmit"
	EventSessionStart       = "SessionStart"
	EventStop               = "Stop"
	EventSubagentStop       = "SubagentStop"
	EventPreCompact         = "PreCompact"
	EventPostCompact        = "PostCompact"
)

type hookContextKey struct{}

type Runtime struct {
	Home string
	// WorkspaceRoot is the workspace directory of the primary agent this
	// runtime serves. Hook artifacts (session transcripts, fork sidechains)
	// are tenant data and hang off it; Home stays the shared install root and
	// is only used for cwd-style defaults. Empty falls back to main's
	// workspace, which is what a single-agent install has anyway.
	WorkspaceRoot         string
	Cfg                   *appcfg.Root
	Sess                  *state.SessionStore
	Actions               *state.ActionService
	NewPromptRunner       func(label string) (PromptRun, error)
	HookPromptQuerySource string
	RunAgentHook          func(context.Context, string) (string, error)
	SessionID             func(context.Context) string
	ToolUseID             func(context.Context) string
	PermissionMode        func() string
}

type PromptRun func(context.Context, string) (string, error)

type BaseInput struct {
	HookEventName  string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	PermissionMode string `json:"permission_mode,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	AgentType      string `json:"agent_type,omitempty"`
}

type HookResult struct {
	Continue                 *bool          `json:"continue,omitempty"`
	SuppressOutput           bool           `json:"suppressOutput,omitempty"`
	StopReason               string         `json:"stopReason,omitempty"`
	Decision                 string         `json:"decision,omitempty"`
	Reason                   string         `json:"reason,omitempty"`
	SystemMessage            string         `json:"systemMessage,omitempty"`
	HookSpecificOutput       map[string]any `json:"hookSpecificOutput,omitempty"`
	RawStdout                string         `json:"-"`
	RawStderr                string         `json:"-"`
	BlockingError            string         `json:"-"`
	PermissionDecision       string         `json:"-"`
	PermissionDecisionReason string         `json:"-"`
	UpdatedInput             map[string]any `json:"-"`
	AdditionalContext        string         `json:"-"`
	UpdatedMCPToolOutput     any            `json:"-"`
	InitialUserMessage       string         `json:"-"`
	ParsedJSON               map[string]any `json:"-"`
}

type ToolContext struct {
	SessionID      string
	PermissionMode string
	AgentID        string
	AgentType      string
	RunID          string
}

type UserPromptSubmitInput struct {
	BaseInput
	Prompt string `json:"prompt"`
}

type PreToolUseInput struct {
	BaseInput
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
	ToolUseID string         `json:"tool_use_id"`
}

type PermissionRequestInput struct {
	BaseInput
	TurnID         string         `json:"turn_id"`
	Model          string         `json:"model"`
	ToolName       string         `json:"tool_name"`
	ToolInput      map[string]any `json:"tool_input"`
	MatcherAliases []string       `json:"-"`
}

func (in PermissionRequestInput) MarshalJSON() ([]byte, error) {
	var transcriptPath any
	if value := strings.TrimSpace(in.TranscriptPath); value != "" {
		transcriptPath = value
	}
	payload := struct {
		SessionID      string         `json:"session_id"`
		TurnID         string         `json:"turn_id"`
		AgentID        string         `json:"agent_id,omitempty"`
		AgentType      string         `json:"agent_type,omitempty"`
		TranscriptPath any            `json:"transcript_path"`
		Cwd            string         `json:"cwd"`
		HookEventName  string         `json:"hook_event_name"`
		Model          string         `json:"model"`
		PermissionMode string         `json:"permission_mode"`
		ToolName       string         `json:"tool_name"`
		ToolInput      map[string]any `json:"tool_input"`
	}{
		SessionID: in.SessionID, TurnID: in.TurnID, AgentID: in.AgentID, AgentType: in.AgentType,
		TranscriptPath: transcriptPath, Cwd: in.Cwd, HookEventName: in.HookEventName,
		Model: in.Model, PermissionMode: in.PermissionMode, ToolName: in.ToolName, ToolInput: in.ToolInput,
	}
	return json.Marshal(payload)
}

type PostToolUseInput struct {
	BaseInput
	ToolName     string         `json:"tool_name"`
	ToolInput    map[string]any `json:"tool_input"`
	ToolResponse any            `json:"tool_response"`
	ToolUseID    string         `json:"tool_use_id"`
}

type PostToolUseFailureInput struct {
	BaseInput
	ToolName    string         `json:"tool_name"`
	ToolInput   map[string]any `json:"tool_input"`
	ToolUseID   string         `json:"tool_use_id"`
	Error       string         `json:"error"`
	IsInterrupt bool           `json:"is_interrupt,omitempty"`
}

type NotificationInput struct {
	BaseInput
	Message          string `json:"message"`
	Title            string `json:"title,omitempty"`
	NotificationType string `json:"notification_type"`
	ToolName         string `json:"tool_name,omitempty"`
	ToolUseID        string `json:"tool_use_id,omitempty"`
}

type SessionStartInput struct {
	BaseInput
	Source string `json:"source"`
	Model  string `json:"model,omitempty"`
}

type CompactInput struct {
	BaseInput
	Model   string `json:"model,omitempty"`
	Trigger string `json:"trigger"`
}

type StopInput struct {
	BaseInput
	StopHookActive       bool   `json:"stop_hook_active"`
	LastAssistantMessage string `json:"last_assistant_message,omitempty"`
}

type SubagentStopInput struct {
	BaseInput
	StopHookActive       bool   `json:"stop_hook_active"`
	AgentTranscriptPath  string `json:"agent_transcript_path"`
	LastAssistantMessage string `json:"last_assistant_message,omitempty"`
}

var matcherSimple = regexp.MustCompile(`^[a-zA-Z0-9_|]+$`)

func MatchHookMatcher(query, matcher string) bool {
	matcher = strings.TrimSpace(matcher)
	if matcher == "" || matcher == "*" {
		return true
	}
	query = strings.TrimSpace(query)
	if matcherSimple.MatchString(matcher) {
		if strings.Contains(matcher, "|") {
			for _, part := range strings.Split(matcher, "|") {
				if strings.EqualFold(strings.TrimSpace(part), query) {
					return true
				}
			}
			return false
		}
		return strings.EqualFold(matcher, query)
	}
	re, err := regexp.Compile(matcher)
	if err != nil {
		return false
	}
	return re.MatchString(query)
}

func withHookExecution(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, hookContextKey{}, true)
}

func inHookExecution(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(hookContextKey{}).(bool)
	return v
}

func (r *Runtime) executeCommandHook(ctx context.Context, hook appcfg.HookCommand, payload any) (HookResult, error) {
	var result HookResult
	b, err := json.Marshal(payload)
	if err != nil {
		return result, err
	}
	timeout := 10 * time.Minute
	if hook.Timeout > 0 {
		timeout = time.Duration(hook.Timeout * float64(time.Second))
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	shell := strings.TrimSpace(hook.Shell)
	if shell == "" {
		shell = "bash"
	}
	var c *exec.Cmd
	switch shell {
	case "powershell":
		c = exec.CommandContext(cmdCtx, "pwsh", "-NoProfile", "-NonInteractive", "-Command", hook.Command)
	default:
		c = exec.CommandContext(cmdCtx, "/bin/sh", "-lc", hook.Command)
	}
	c.Stdin = bytes.NewReader(b)
	c.Dir = r.hookWorkingDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	err = c.Run()
	result.RawStdout = stdout.String()
	result.RawStderr = stderr.String()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
			result.BlockingError = strings.TrimSpace(stderr.String())
			if result.BlockingError == "" {
				result.BlockingError = strings.TrimSpace(stdout.String())
			}
			if result.BlockingError == "" {
				result.BlockingError = "blocked by hook"
			}
			return result, nil
		}
		return result, err
	}
	if parsed, ok := parseHookJSON(stdout.Bytes()); ok {
		return parsed, nil
	}
	return result, nil
}

func (r *Runtime) executePromptHook(ctx context.Context, hook appcfg.HookCommand, payload any) (HookResult, error) {
	var result HookResult
	prompt := strings.TrimSpace(hook.Prompt)
	if prompt == "" {
		return result, fmt.Errorf("prompt hook requires prompt")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return result, err
	}
	prompt = strings.ReplaceAll(prompt, "$ARGUMENTS", string(b))
	if r.NewPromptRunner == nil {
		return result, fmt.Errorf("hook prompt runner unavailable")
	}
	run, err := r.NewPromptRunner("hook-prompt")
	if err != nil {
		return result, err
	}
	runCtx := withHookExecution(context.Background())
	text, err := run(runCtx, prompt)
	if err != nil {
		return result, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return result, nil
	}
	if parsed, ok := parseHookJSON([]byte(text)); ok {
		return parsed, nil
	}
	result.RawStdout = text
	return result, nil
}

func (r *Runtime) executeAgentHook(ctx context.Context, hook appcfg.HookCommand, payload any) (HookResult, error) {
	var result HookResult
	if r.RunAgentHook == nil {
		return result, fmt.Errorf("agent hook requires owner runner")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return result, err
	}
	task := strings.ReplaceAll(strings.TrimSpace(hook.Prompt), "$ARGUMENTS", string(b))
	if task == "" {
		return result, fmt.Errorf("agent hook requires prompt")
	}
	text, err := r.RunAgentHook(withHookExecution(ctx), task)
	if err != nil {
		return result, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return result, nil
	}
	if parsed, ok := parseHookJSON([]byte(text)); ok {
		return parsed, nil
	}
	result.RawStdout = text
	return result, nil
}

func (r *Runtime) executeHTTPHook(ctx context.Context, hook appcfg.HookCommand, payload any) (HookResult, error) {
	var result HookResult
	b, err := json.Marshal(payload)
	if err != nil {
		return result, err
	}
	timeout := 10 * time.Minute
	if hook.Timeout > 0 {
		timeout = time.Duration(hook.Timeout * float64(time.Second))
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.URL, bytes.NewReader(b))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hook.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	var body bytes.Buffer
	_, _ = body.ReadFrom(resp.Body)
	result.RawStdout = body.String()
	if parsed, ok := parseHookJSON(body.Bytes()); ok {
		return parsed, nil
	}
	return result, nil
}

func parseHookJSON(raw []byte) (HookResult, bool) {
	var out HookResult
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return out, false
	}
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return HookResult{}, false
	}
	if err := json.Unmarshal([]byte(trimmed), &out.ParsedJSON); err != nil {
		return HookResult{}, false
	}
	if out.HookSpecificOutput != nil {
		if v, ok := out.HookSpecificOutput["permissionDecision"].(string); ok {
			out.PermissionDecision = strings.TrimSpace(v)
		}
		if v, ok := out.HookSpecificOutput["permissionDecisionReason"].(string); ok {
			out.PermissionDecisionReason = strings.TrimSpace(v)
		}
		if v, ok := out.HookSpecificOutput["additionalContext"].(string); ok {
			out.AdditionalContext = v
		}
		if v, ok := out.HookSpecificOutput["updatedInput"].(map[string]any); ok {
			out.UpdatedInput = v
		}
		if v, ok := out.HookSpecificOutput["updatedMCPToolOutput"]; ok {
			out.UpdatedMCPToolOutput = v
		}
		if v, ok := out.HookSpecificOutput["initialUserMessage"].(string); ok {
			out.InitialUserMessage = v
		}
	}
	return out, true
}

// StateRoot resolves the root that per-agent hook artifacts hang off. Callers
// must route every "state/..." path through here rather than reaching for
// Home, so a transcript this runtime writes and a transcript its tool
// middleware writes land on the same file for the same agent.
func (r *Runtime) StateRoot() string {
	if r == nil {
		return ""
	}
	if ws := strings.TrimSpace(r.WorkspaceRoot); ws != "" {
		return ws
	}
	if home := strings.TrimSpace(r.Home); home != "" {
		return filepath.Join(home, "workspace")
	}
	return ""
}

func (r *Runtime) hookWorkingDir() string {
	if wd, err := filepath.Abs("."); err == nil && strings.TrimSpace(wd) != "" {
		return wd
	}
	return strings.TrimSpace(r.Home)
}
