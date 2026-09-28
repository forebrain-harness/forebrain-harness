package event

import "encoding/json"

// ToolMeta describes a tool in a machine-consumable way.
// It is intentionally UI/IDE friendly (VS Code/web can render forms from schema).
type ToolMeta struct {
	Name              string          `json:"name"`
	Description       string          `json:"description,omitempty"`
	Category          string          `json:"category,omitempty"`
	ReadOnly          bool            `json:"read_only,omitempty"`
	Destructive       bool            `json:"destructive,omitempty"`
	ConcurrencySafe   bool            `json:"concurrency_safe,omitempty"`
	InterruptBehavior string          `json:"interrupt_behavior,omitempty"`
	AlwaysLoad        bool            `json:"always_load,omitempty"`
	InputSchema       json.RawMessage `json:"input_schema,omitempty"`
	// MCP safety annotations preserve whether the server actually supplied a
	// hint. Nil is intentionally different from false: missing annotations are
	// treated conservatively by the approval policy.
	ReadOnlyHint    *bool  `json:"read_only_hint,omitempty"`
	DestructiveHint *bool  `json:"destructive_hint,omitempty"`
	OpenWorldHint   *bool  `json:"open_world_hint,omitempty"`
	IdempotentHint  *bool  `json:"idempotent_hint,omitempty"`
	MCPApprovalMode string `json:"mcp_approval_mode,omitempty"`
}
