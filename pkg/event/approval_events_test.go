package event

import (
	"encoding/json"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

func TestApprovalRequestedPayloadEncode(t *testing.T) {
	raw := EncodePayload(ApprovalRequestedPayload{
		RequiresAction: map[string]any{"id": "act-1", "kind": "shell"},
		Message:        "Awaiting approval",
	})
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	ra, ok := got["requires_action"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected requires_action: %#v", got["requires_action"])
	}
	if ra["id"] != "act-1" {
		t.Fatalf("unexpected action id: %#v", ra["id"])
	}
	if got["message"] != "Awaiting approval" {
		t.Fatalf("unexpected message: %#v", got["message"])
	}
}

func TestApprovalRequestedPayloadEncodePermissionSuggestion(t *testing.T) {
	raw := EncodePayload(ApprovalRequestedPayload{
		RequiresAction: map[string]any{"id": "act-1", "kind": "shell"},
		PermissionSuggestion: &PermissionSuggestionPayload{
			PermissionToolName:   "Bash",
			PermissionInput:      "npm run build",
			ExactRuleContent:     "npm run build",
			PrefixRuleContent:    "npm run:*",
			DestinationOptions:   []safety.PermissionDestination{safety.DestinationSession, safety.DestinationLocalSettings},
			SuggestedDestination: safety.DestinationLocalSettings,
			PermissionMode:       safety.ModeOnRequest,
			PermissionReason:     "default_ask",
			AvailableDecisions: []any{
				"accept",
				map[string]any{"accept_with_execpolicy_amendment": map[string]any{"execpolicy_amendment": []string{"npm", "run", "build"}}},
				"cancel",
			},
			ProposedExecPolicyAmendment: safety.ExecPolicyAmendment{"npm", "run", "build"},
		},
	})
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	ps, ok := got["permission_suggestion"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected permission_suggestion: %#v", got["permission_suggestion"])
	}
	if ps["permission_tool_name"] != "Bash" {
		t.Fatalf("unexpected permission tool name: %#v", ps["permission_tool_name"])
	}
	if ps["prefix_rule_content"] != "npm run:*" {
		t.Fatalf("unexpected prefix rule: %#v", ps["prefix_rule_content"])
	}
	if decisions, ok := ps["available_decisions"].([]any); !ok || len(decisions) != 3 {
		t.Fatalf("unexpected available decisions: %#v", ps["available_decisions"])
	}
	if amendment, ok := ps["proposed_execpolicy_amendment"].([]any); !ok || len(amendment) != 3 {
		t.Fatalf("unexpected exec policy amendment: %#v", ps["proposed_execpolicy_amendment"])
	}
}
