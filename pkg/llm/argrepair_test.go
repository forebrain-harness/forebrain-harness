// Copyright 2026 Simone Vellei
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package llm

import (
	"encoding/json"
	"testing"
)

func TestRepairToolArguments_StringArrayToArray(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prefix_rule": map[string]any{"type": []any{"array", "null"}, "items": map[string]any{"type": "string"}},
		},
		"required": []any{"prefix_rule"},
	}
	tests := []struct {
		name string
		in   string
		want any
	}{
		{"json-encoded-array", `{"prefix_rule":"[\"git\",\"-C\"]"}`, []any{"git", "-C"}},
		{"empty-json-array", `{"prefix_rule":"[]"}`, []any{}},
		{"bare-scalar", `{"prefix_rule":"git"}`, []any{"git"}},
		{"empty-string-nullable", `{"prefix_rule":""}`, nil},
		{"already-array", `{"prefix_rule":["git"]}`, []any{"git"}},
		{"already-null", `{"prefix_rule":null}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := repairToolArguments(schema, tt.in)
			if tt.name == "already-array" || tt.name == "already-null" {
				if changed {
					t.Fatalf("expected no change, got %q", got)
				}
				if got != tt.in {
					t.Fatalf("expected byte-for-byte identity, got %q", got)
				}
				return
			}
			if !changed {
				t.Fatalf("expected a repair, got none (out=%q)", got)
			}
			var decoded map[string]any
			if err := json.Unmarshal([]byte(got), &decoded); err != nil {
				t.Fatalf("repaired output is not JSON: %v (%q)", err, got)
			}
			wantJSON, _ := json.Marshal(map[string]any{"prefix_rule": tt.want})
			gotJSON, _ := json.Marshal(decoded)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("got %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestRepairToolArguments_NonNullableEmptyArray(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	}
	got, changed := repairToolArguments(schema, `{"tags":""}`)
	if !changed {
		t.Fatal("expected a repair")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatal(err)
	}
	tags, ok := decoded["tags"].([]any)
	if !ok || len(tags) != 0 {
		t.Fatalf("expected empty array, got %#v", decoded["tags"])
	}
}

func TestRepairToolArguments_ScalarCoercions(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"limit":  map[string]any{"type": "integer"},
			"ratio":  map[string]any{"type": "number"},
			"flag":   map[string]any{"type": "boolean"},
			"config": map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
	got, changed := repairToolArguments(schema, `{"limit":"7","ratio":"1.5","flag":"true","config":"{\"a\":1}"}`)
	if !changed {
		t.Fatal("expected a repair")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["limit"] != float64(7) {
		t.Fatalf("limit = %#v", decoded["limit"])
	}
	if decoded["ratio"] != float64(1.5) {
		t.Fatalf("ratio = %#v", decoded["ratio"])
	}
	if decoded["flag"] != true {
		t.Fatalf("flag = %#v", decoded["flag"])
	}
	cfg, ok := decoded["config"].(map[string]any)
	if !ok || cfg["a"] != float64(1) {
		t.Fatalf("config = %#v", decoded["config"])
	}
}

func TestRepairToolArguments_NestedItemsAndRefs(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"$defs": map[string]any{
			"item": map[string]any{
				"type":       "object",
				"properties": map[string]any{"n": map[string]any{"type": "integer"}},
			},
		},
		"properties": map[string]any{
			"items": map[string]any{
				"type":  "array",
				"items": map[string]any{"$ref": "#/$defs/item"},
			},
		},
	}
	got, changed := repairToolArguments(schema, `{"items":"[{\"n\":\"1\"},{\"n\":\"2\"}]"}`)
	if !changed {
		t.Fatal("expected a repair")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatal(err)
	}
	items, ok := decoded["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("items = %#v", decoded["items"])
	}
	first := items[0].(map[string]any)
	if first["n"] != float64(1) {
		t.Fatalf("first.n = %#v", first["n"])
	}
}

func TestRepairToolArguments_NoChangeForValidOrMalformed(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"limit": map[string]any{"type": "integer"},
		},
	}
	for _, in := range []string{
		`{"limit":7}`,
		`{"limit": 7 }`, // whitespace differs from round-trip; must be untouched
		``,
		`not-json`,
		`[1,2,3]`,
		`{"limit":"seven"}`, // unparseable as int; left for the parser to reject
	} {
		got, changed := repairToolArguments(schema, in)
		if changed {
			t.Fatalf("input %q should not change, got %q", in, got)
		}
		if got != in {
			t.Fatalf("input %q should be byte-for-byte identical, got %q", in, got)
		}
	}
}

func TestRepairToolArguments_PreservesLargeIntegers(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{"type": "integer"},
		},
	}
	const big = `{"id":9007199254740993}`
	got, changed := repairToolArguments(schema, big)
	if changed {
		t.Fatalf("expected no change, got %q", got)
	}
	if got != big {
		t.Fatalf("large integer was mutated: %q", got)
	}
}
