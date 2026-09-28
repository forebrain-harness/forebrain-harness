package gateway

import (
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

func TestNormalizeWSClientMessageMapsControlRequest(t *testing.T) {
	m := wsClientMsg{Type: "control_request"}
	m.Request.Subtype = "interrupt"
	got := normalizeWSClientMessage(m)
	if got.Op != "control_request" {
		t.Fatalf("op = %q, want control_request", got.Op)
	}
}

func TestValidateWSClientMessageAcceptsInterruptControlRequest(t *testing.T) {
	m := wsClientMsg{
		Type:      "control_request",
		RequestID: "req-1",
	}
	m.Request.Subtype = "interrupt"
	m = normalizeWSClientMessage(m)
	if err := validateWSClientMessage(m); err != nil {
		t.Fatalf("validate interrupt control request: %v", err)
	}
}

func TestValidateWSClientMessageRejectsUnsupportedControlSubtype(t *testing.T) {
	m := wsClientMsg{
		Type:      "control_request",
		RequestID: "req-1",
	}
	m.Request.Subtype = "unknown"
	m = normalizeWSClientMessage(m)
	if err := validateWSClientMessage(m); err == nil {
		t.Fatalf("unsupported control subtype must fail validation")
	}
}

func TestValidateWSClientMessageBranches(t *testing.T) {
	valid := []wsClientMsg{
		{Op: "connect"},
		{Op: "connect", ProtocolVersion: wsProtocolVersion},
		{Op: "ping"},
		{Op: "start_run", RequestID: "req", Content: "hello"},
		{Op: "start_run", RequestID: "req", Message: wsClientMessage{Content: "hello"}},
		{Op: "start_run", RequestID: "req", Message: wsClientMessage{Attachments: []string{"file-1"}}},
		{Op: "start_run", RequestID: "req", Message: wsClientMessage{MentionImages: []string{"shots/a.png"}}},
		{Op: "cancel_run", RequestID: "req", RunID: "run"},
		{Op: "approve_action", RequestID: "req", ActionID: "act"},
		{Op: "deny_action", RequestID: "req", ActionID: "act"},
		{Op: "submit_answer", RequestID: "req", ActionID: "act"},
		{Op: "switch_mode", RequestID: "req", Mode: "agent"},
		{Op: "switch_mode", RequestID: "req", Mode: "plan"},
	}
	for _, msg := range valid {
		if err := validateWSClientMessage(msg); err != nil {
			t.Fatalf("validate %#v: %v", msg, err)
		}
	}

	invalid := []wsClientMsg{
		{},
		{Op: "connect", ProtocolVersion: "2099-01-01"},
		{Op: "unknown"},
		{Op: "start_run", Content: "hello"},
		{Op: "start_run", RequestID: "req"},
		{Op: "cancel_run", RequestID: "req"},
		{Op: "approve_action", RequestID: "req"},
		{Op: "deny_action", RequestID: "req"},
		{Op: "submit_answer", RequestID: "req"},
		{Op: "switch_mode", RequestID: "req", Mode: "coordinator"},
		{Op: "switch_mode", RequestID: "req", Mode: "ask"},
		{Op: "switch_mode", RequestID: "req", Mode: "review"},
		{Op: "switch_mode", RequestID: "req", Mode: "invalid"},
		{Op: "switch_mode", RequestID: "req", Mode: "debug"},
		{Op: "control_request", RequestID: "req", Type: event.GatewayControlTypeRequest},
	}
	for _, msg := range invalid {
		if err := validateWSClientMessage(msg); err == nil {
			t.Fatalf("expected invalid message error for %#v", msg)
		}
	}
}

func TestWSRequestCache(t *testing.T) {
	now := time.Now()
	cache := map[string]wsRequestCacheEntry{
		"expired": {Response: wsServerMsg{Op: "old"}, ExpireAt: now.Add(-time.Second)},
	}
	wsRequestCacheSet(cache, " req ", wsServerMsg{Op: "ok"}, time.Minute, now)
	wsRequestCacheSet(cache, " ", wsServerMsg{Op: "ignored"}, time.Minute, now)
	if _, ok := cache[""]; ok {
		t.Fatal("empty request id should not be cached")
	}
	got, ok := wsRequestCacheGet(cache, "req", now)
	if !ok || got.Op != "ok" {
		t.Fatalf("cache hit=%+v ok=%v", got, ok)
	}
	if _, ok := cache["expired"]; ok {
		t.Fatal("expired entry should be swept")
	}
	if _, ok := wsRequestCacheGet(cache, "", now); ok {
		t.Fatal("empty request id should miss")
	}
	if _, ok := wsRequestCacheGet(cache, "req", now.Add(2*time.Minute)); ok {
		t.Fatal("expired request should miss")
	}
}
