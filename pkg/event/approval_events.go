package event

import (
	"encoding/json"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

const (
	ApprovalEventRequested = RunEventApprovalReq
	ApprovalEventResolved  = "approval_resolved"
)

type ApprovalEvent struct {
	RunID     string          `json:"run_id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// ApprovalRequestedPayload records that a surface showed an approval gate.
//
// This log is display history. It is never the authority on an action's stored
// state: a surface records the decision before the action service applies it,
// so a crash in between leaves a resolved-looking record beside a pending
// action. Read the action store, not this event, to learn what an approval's
// outcome actually is.
type ApprovalRequestedPayload struct {
	ActionID     string `json:"action_id,omitempty"`
	ActionKind   string `json:"action_kind,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`
	SubagentType string `json:"subagent_type,omitempty"`
	// ToolStepID names the tool call the gate is holding, so a surface can
	// anchor the record to that call. Empty when nothing resolved the call; a
	// record without one claims no card.
	ToolStepID           string                       `json:"tool_step_id,omitempty"`
	RequiresAction       any                          `json:"requires_action,omitempty"`
	Message              string                       `json:"message,omitempty"`
	PermissionSuggestion *PermissionSuggestionPayload `json:"permission_suggestion,omitempty"`
}

type PermissionSuggestionPayload struct {
	PermissionToolName              string                          `json:"permission_tool_name,omitempty"`
	PermissionInput                 string                          `json:"permission_input,omitempty"`
	ExactRuleContent                string                          `json:"exact_rule_content,omitempty"`
	PrefixRuleContent               string                          `json:"prefix_rule_content,omitempty"`
	DestinationOptions              []safety.PermissionDestination  `json:"destination_options,omitempty"`
	SuggestedDestination            safety.PermissionDestination    `json:"suggested_destination,omitempty"`
	PermissionMode                  safety.PermissionMode           `json:"permission_mode,omitempty"`
	PermissionReason                string                          `json:"permission_reason,omitempty"`
	BypassSandbox                   bool                            `json:"bypass_sandbox,omitempty"`
	OneShotOnly                     bool                            `json:"one_shot_only,omitempty"`
	AvailableDecisions              []any                           `json:"available_decisions,omitempty"`
	ProposedExecPolicyAmendment     safety.ExecPolicyAmendment      `json:"proposed_execpolicy_amendment,omitempty"`
	ProposedNetworkPolicyAmendments []safety.NetworkPolicyAmendment `json:"proposed_network_policy_amendments,omitempty"`
	NetworkApproval                 *safety.NetworkApprovalContext  `json:"network_approval_context,omitempty"`
	NetworkPort                     int                             `json:"network_port,omitempty"`
}

// ApprovalResolvedPayload records that a surface showed an approval's outcome.
// Like the requested payload it is display history, not the action's state.
type ApprovalResolvedPayload struct {
	ActionID     string `json:"action_id,omitempty"`
	ActionKind   string `json:"action_kind,omitempty"`
	Decision     string `json:"decision,omitempty"`
	Destination  string `json:"destination,omitempty"`
	Reason       string `json:"reason,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`
	SubagentType string `json:"subagent_type,omitempty"`
	// ToolStepID names the tool call the decision belongs to, when the surface
	// could resolve it.
	ToolStepID string `json:"tool_step_id,omitempty"`
	// Confirmation is the one-line "✔ You approved …" sentence the surface
	// printed, already sanitized. Empty when the surface printed nothing for
	// this decision - a replay must not invent a line the user never saw.
	Confirmation string `json:"confirmation,omitempty"`
}
