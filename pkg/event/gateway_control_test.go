package event

import (
	"encoding/json"
	"testing"
)

func TestGatewayControlErrorResponseShape(t *testing.T) {
	got := NewGatewayControlErrorResponse("req-1", "unsupported control request subtype: unknown")
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["type"] != "control_response" {
		t.Fatalf("unexpected type: %#v", decoded["type"])
	}
	resp, ok := decoded["response"].(map[string]any)
	if !ok {
		t.Fatalf("missing response object: %#v", decoded["response"])
	}
	if resp["subtype"] != "error" || resp["request_id"] != "req-1" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}
