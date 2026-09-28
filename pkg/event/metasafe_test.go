package event

import (
	"encoding/json"
	"testing"
)

func TestSanitizeForPublic_RemovesProtoKeysRecursively(t *testing.T) {
	in := map[string]any{
		"ok":                       "yes",
		"_PROTO_secret":            "drop",
		"_SECRET_x":                "drop-secret",
		"_INTERNAL_y":              "drop-internal",
		"forebrain_internal_trace": "drop-trace",
		"nested": map[string]any{
			"_PROTO_url": "drop2",
			"_SECRET_z":  "drop3",
			"keep":       "v",
		},
		"arr": []any{
			map[string]any{"keep": 1, "_PROTO_x": 2, "_INTERNAL_x": 3},
		},
	}
	out, ok := SanitizeForPublic(in).(map[string]any)
	if !ok {
		t.Fatalf("unexpected output type")
	}
	if _, exists := out["_PROTO_secret"]; exists {
		t.Fatalf("top-level proto key leaked")
	}
	if _, exists := out["_SECRET_x"]; exists {
		t.Fatalf("top-level secret key leaked")
	}
	if _, exists := out["_INTERNAL_y"]; exists {
		t.Fatalf("top-level internal key leaked")
	}
	if _, exists := out["forebrain_internal_trace"]; exists {
		t.Fatalf("top-level forebrain internal key leaked")
	}
	nested := out["nested"].(map[string]any)
	if _, exists := nested["_PROTO_url"]; exists {
		t.Fatalf("nested proto key leaked")
	}
	if _, exists := nested["_SECRET_z"]; exists {
		t.Fatalf("nested secret key leaked")
	}
	arrItem := out["arr"].([]any)[0].(map[string]any)
	if _, exists := arrItem["_PROTO_x"]; exists {
		t.Fatalf("array nested proto key leaked")
	}
	if _, exists := arrItem["_INTERNAL_x"]; exists {
		t.Fatalf("array nested internal key leaked")
	}
}

func TestSanitizeRawJSON_RemovesProtoKeys(t *testing.T) {
	raw := json.RawMessage(`{"ok":1,"_PROTO_secret":"x","nest":{"_PROTO_y":2,"z":3}}`)
	out := SanitizeRawJSON(raw)
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, exists := decoded["_PROTO_secret"]; exists {
		t.Fatalf("proto key leaked")
	}
	nest := decoded["nest"].(map[string]any)
	if _, exists := nest["_PROTO_y"]; exists {
		t.Fatalf("nested proto key leaked")
	}
}

func TestSanitizeRawJSONBranches(t *testing.T) {
	empty := json.RawMessage(nil)
	if out := SanitizeRawJSON(empty); out != nil {
		t.Fatalf("empty raw = %q", out)
	}

	invalid := json.RawMessage(`{"bad"`)
	if out := SanitizeRawJSON(invalid); string(out) != string(invalid) {
		t.Fatalf("invalid raw changed: %q", out)
	}

	rawString := json.RawMessage(`"plain"`)
	if out := SanitizeRawJSON(rawString); string(out) != `"plain"` {
		t.Fatalf("raw string = %q", out)
	}

	if got := SanitizeForPublic(123); got != 123 {
		t.Fatalf("default sanitize = %#v", got)
	}

	rawOut, ok := SanitizeForPublic(json.RawMessage(`{"_PROTO_x":1,"keep":2}`)).(json.RawMessage)
	if !ok {
		t.Fatal("raw message branch not used")
	}
	var decoded map[string]any
	if err := json.Unmarshal(rawOut, &decoded); err != nil {
		t.Fatalf("unmarshal raw output: %v", err)
	}
	if _, exists := decoded["_PROTO_x"]; exists {
		t.Fatalf("raw message proto key leaked")
	}
}
