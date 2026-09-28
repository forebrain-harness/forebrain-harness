package event

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	ToolEventStarted     = RunEventToolStarted
	ToolEventOutputDelta = RunEventToolOutputDelta
	ToolEventCompleted   = RunEventToolCompleted
)

const (
	ToolErrorNone              = ""
	ToolErrorValidation        = "validation"
	ToolErrorPermissionDenied  = "permission_denied"
	ToolErrorPolicyBlocked     = "policy_blocked"
	ToolErrorMCPAuth           = "mcp_auth"
	ToolErrorMCPSessionExpired = "mcp_session_expired"
	ToolErrorExecution         = "execution"
)

type ToolEvent struct {
	RunID     string          `json:"run_id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

type ToolCallStartedPayload struct {
	Kind         string         `json:"kind,omitempty"`
	StepID       string         `json:"step_id,omitempty"`
	Description  string         `json:"description,omitempty"`
	ToolName     string         `json:"tool_name,omitempty"`
	CurrentQuery string         `json:"current_query,omitempty"`
	Input        map[string]any `json:"input,omitempty"`
	// Summary is the single line a surface labels the call with ("running git
	// diff"). The runtime computes it so every surface says the same thing
	// about the same call.
	Summary  string       `json:"summary,omitempty"`
	ToolMeta ToolCallMeta `json:"tool_meta,omitempty"`
}

// ToolCallOutputDeltaPayload carries an incremental tool result.
//
// A delta is a boundary of a call that is still executing, so it carries the
// same summary and meta its start did: a surface that joins the call mid-stream
// — or repaints its card from this event — must be able to say what is running
// and that it is still running, without inferring either.
type ToolCallOutputDeltaPayload struct {
	StepID   string       `json:"step_id,omitempty"`
	ToolName string       `json:"tool_name,omitempty"`
	Text     string       `json:"text,omitempty"`
	AgentID  string       `json:"agent_id,omitempty"`
	Summary  string       `json:"summary,omitempty"`
	ToolMeta ToolCallMeta `json:"tool_meta,omitempty"`
}

type ToolCallMeta struct {
	ToolName   string         `json:"tool_name,omitempty"`
	Status     string         `json:"status,omitempty"`
	Purpose    string         `json:"purpose,omitempty"`
	Invocation string         `json:"invocation,omitempty"`
	Input      map[string]any `json:"input,omitempty"`
	AgentID    string         `json:"agent_id,omitempty"`
	AgentType  string         `json:"agent_type,omitempty"`
	AgentKind  string         `json:"agent_kind,omitempty"`
	Category   string         `json:"category,omitempty"`
	SkillName  string         `json:"skill_name,omitempty"`
	SkillPath  string         `json:"skill_path,omitempty"`
	// ResultLines and ResultOffset are the engine-observed extent of a paging
	// read: how many content lines the engine returned and the 0-based line it
	// started at. A surface labels the card with the returned page (lines
	// 1-200) — never the requested window, and never a count re-derived from
	// rendered rows. Absent on non-paging tools.
	ResultLines  int `json:"result_lines,omitempty"`
	ResultOffset int `json:"result_offset,omitempty"`
	// Origin distinguishes an explicit skill invocation from an LLM-initiated
	// skill read for audit only. Surfaces must not branch on it.
	Origin string `json:"origin,omitempty"`
}

type ToolCallCompletedPayload struct {
	Kind            string         `json:"kind,omitempty"`
	StepID          string         `json:"step_id,omitempty"`
	Description     string         `json:"description,omitempty"`
	DurationSeconds float64        `json:"duration_seconds,omitempty"`
	Data            string         `json:"data,omitempty"`
	ToolName        string         `json:"tool_name,omitempty"`
	Output          map[string]any `json:"output,omitempty"`
	Error           string         `json:"error,omitempty"`
	ErrorType       string         `json:"error_type,omitempty"`
	DisplayBody     string         `json:"display_body,omitempty"`
	ActionID        string         `json:"action_id,omitempty"`
	ActionKind      string         `json:"action_kind,omitempty"`
	RetainAsHistory bool           `json:"retain_as_history,omitempty"`
	// Summary is the single line a surface labels the call with ("ran read_file
	// · 42 bytes"), computed by the runtime so every surface agrees.
	Summary  string       `json:"summary,omitempty"`
	ToolMeta ToolCallMeta `json:"tool_meta,omitempty"`
}

func ClassifyToolError(msg string) string {
	s := strings.ToLower(strings.TrimSpace(msg))
	if s == "" {
		return ToolErrorNone
	}
	switch {
	case strings.Contains(s, "previously denied") ||
		strings.Contains(s, "permission denied") ||
		strings.Contains(s, "denied by permission") ||
		strings.Contains(s, "user denied"):
		return ToolErrorPermissionDenied
	case strings.Contains(s, "classifier blocked") ||
		strings.Contains(s, "policy blocked") ||
		strings.Contains(s, "blocked by policy"):
		return ToolErrorPolicyBlocked
	case strings.Contains(s, "insufficient_scope") ||
		strings.Contains(s, "needs-auth") ||
		strings.Contains(s, "unauthorized") ||
		strings.Contains(s, "oauth"):
		return ToolErrorMCPAuth
	case strings.Contains(s, "-32001") ||
		strings.Contains(s, "session expired") ||
		strings.Contains(s, "session not found"):
		return ToolErrorMCPSessionExpired
	case strings.Contains(s, "invalid tool arguments") ||
		strings.Contains(s, "invalid argument") ||
		strings.Contains(s, "unmarshal") ||
		strings.Contains(s, "parse"):
		return ToolErrorValidation
	default:
		return ToolErrorExecution
	}
}
