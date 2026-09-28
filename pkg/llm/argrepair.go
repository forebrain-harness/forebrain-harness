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
	"math"
	"strconv"
	"strings"
)

// maxRepairDepth bounds schema/value recursion while repairing tool
// arguments. Real tool schemas are shallow; the bound protects against
// cyclic $ref chains or pathologically nested payloads.
const maxRepairDepth = 12

// repairToolArguments performs a best-effort, schema-driven repair of the
// JSON arguments a model emitted for a tool.
//
// Tool schemas are normalized (not strict): optional fields stay optional and
// properties carry single, unambiguous types. Still, weaker models sometimes
// emit a value whose JSON type contradicts the schema — for example
// `"prefix_rule": "[]"` or `"prefix_rule": "[\"git\"]"` (a string-encoded
// array) instead of a real JSON array. Parsing those arguments verbatim fails
// the entire tool call even though the model's intent is unambiguous.
//
// The repair only touches values whose JSON type contradicts the tool's own
// input schema; arguments that already conform are returned unchanged
// (byte-for-byte). It returns the repaired JSON and whether anything
// changed.
func repairToolArguments(schema map[string]any, arguments string) (string, bool) {
	if len(schema) == 0 {
		return arguments, false
	}
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" || trimmed[0] != '{' {
		return arguments, false
	}
	if typ, ok := schema["type"].(string); ok && typ != "object" {
		return arguments, false
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return arguments, false
	}
	repaired, changed := repairValue(root, schema, schema, 0)
	if !changed {
		return arguments, false
	}
	out, err := json.Marshal(repaired)
	if err != nil {
		return arguments, false
	}
	return string(out), true
}

// repairValue coerces value toward the declared schema and recurses into
// containers. It reports whether value was modified. root is the full tool
// schema, needed to resolve $ref pointers.
func repairValue(value any, schema map[string]any, root map[string]any, depth int) (any, bool) {
	if depth > maxRepairDepth || schema == nil {
		return value, false
	}
	schema = resolveSchemaRef(schema, root)
	types := schemaAllowedTypes(schema)
	coerced := false
	if len(types) > 0 && !typeMatches(types, value) {
		if fixed, ok := coerceScalar(value, types); ok {
			value = fixed
			coerced = true
		}
	}

	switch v := value.(type) {
	case map[string]any:
		props, ok := schema["properties"].(map[string]any)
		if !ok || len(props) == 0 {
			return value, coerced
		}
		changed := false
		for key, rawPropSchema := range props {
			propSchema, ok := rawPropSchema.(map[string]any)
			if !ok {
				continue
			}
			propValue, present := v[key]
			if !present {
				continue
			}
			fixed, propChanged := repairValue(propValue, propSchema, root, depth+1)
			if propChanged {
				v[key] = fixed
				changed = true
			}
		}
		return value, changed || coerced
	case []any:
		itemSchema, ok := schema["items"].(map[string]any)
		if !ok {
			return value, coerced
		}
		changed := false
		for i, item := range v {
			fixed, itemChanged := repairValue(item, itemSchema, root, depth+1)
			if itemChanged {
				v[i] = fixed
				changed = true
			}
		}
		return value, changed || coerced
	default:
		return value, coerced
	}
}

// coerceScalar converts a value whose JSON type contradicts the schema into
// the declared type. Only unambiguous conversions are performed; anything
// doubtful is left for the regular parser to reject.
func coerceScalar(value any, types []string) (any, bool) {
	s, ok := value.(string)
	if !ok {
		return value, false
	}
	trimmed := strings.TrimSpace(s)

	if hasType(types, "array") {
		if trimmed == "" {
			if hasType(types, "null") {
				return nil, true
			}
			return []any{}, true
		}
		if decoded, ok := decodeJSONValue(trimmed); ok {
			if _, isList := decoded.([]any); isList {
				return decoded, true
			}
		}
		// A bare scalar where an array is expected reads naturally as a
		// single-element list (e.g. prefix_rule "git" -> ["git"]).
		return []any{s}, true
	}
	if hasType(types, "object") {
		if trimmed == "" {
			if hasType(types, "null") {
				return nil, true
			}
			return map[string]any{}, true
		}
		if decoded, ok := decodeJSONValue(trimmed); ok {
			if _, isObject := decoded.(map[string]any); isObject {
				return decoded, true
			}
		}
		return value, false
	}
	if hasType(types, "boolean") {
		switch strings.ToLower(trimmed) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	if hasType(types, "integer") {
		if n, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return n, true
		}
	}
	if hasType(types, "number") {
		if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return f, true
		}
	}
	if trimmed == "" && hasType(types, "null") {
		return nil, true
	}
	return value, false
}

// schemaAllowedTypes collects the JSON type names a schema permits, unioning
// the branches of anyOf when present.
func schemaAllowedTypes(schema map[string]any) []string {
	var out []string
	switch t := schema["type"].(type) {
	case string:
		out = append(out, t)
	case []any:
		for _, entry := range t {
			if name, ok := entry.(string); ok {
				out = append(out, name)
			}
		}
	}
	if anyOf, ok := schema["anyOf"].([]any); ok {
		for _, variant := range anyOf {
			if variantSchema, ok := variant.(map[string]any); ok {
				out = append(out, schemaAllowedTypes(variantSchema)...)
			}
		}
	}
	return out
}

// typeMatches reports whether value's JSON type is permitted by types.
func typeMatches(types []string, value any) bool {
	switch value.(type) {
	case nil:
		return hasType(types, "null")
	case bool:
		return hasType(types, "boolean")
	case string:
		return hasType(types, "string")
	case json.Number:
		if numberIsIntegral(value.(json.Number)) {
			return hasType(types, "integer") || hasType(types, "number")
		}
		return hasType(types, "number")
	case float64:
		return hasType(types, "number") || (hasType(types, "integer") && floatIsIntegral(value.(float64)))
	case []any:
		return hasType(types, "array")
	case map[string]any:
		return hasType(types, "object")
	default:
		// Unknown value shape: give the regular parser the benefit of the doubt.
		return true
	}
}

func hasType(types []string, name string) bool {
	for _, t := range types {
		if t == name {
			return true
		}
	}
	return false
}

func numberIsIntegral(n json.Number) bool {
	if _, err := n.Int64(); err == nil {
		return true
	}
	f, err := n.Float64()
	if err != nil {
		return false
	}
	return floatIsIntegral(f)
}

func floatIsIntegral(f float64) bool {
	return math.Abs(f) <= math.MaxInt64 && f == math.Trunc(f)
}

// decodeJSONValue decodes a JSON document keeping numbers as json.Number so
// large integers survive a repair round-trip.
func decodeJSONValue(s string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, false
	}
	return out, true
}

// resolveSchemaRef follows $ref pointers (only the local "#/..." form is
// supported) so properties defined via $defs participate in repair.
func resolveSchemaRef(schema map[string]any, root map[string]any) map[string]any {
	for i := 0; i < maxRepairDepth; i++ {
		ref, ok := schema["$ref"].(string)
		if !ok {
			return schema
		}
		target := lookupSchemaPointer(root, ref)
		if target == nil {
			return schema
		}
		schema = target
	}
	return schema
}

func lookupSchemaPointer(root map[string]any, ref string) map[string]any {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	var current any = root
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part = strings.ReplaceAll(part, "~1", "/")
		part = strings.ReplaceAll(part, "~0", "~")
		container, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = container[part]
		if !ok {
			return nil
		}
	}
	resolved, _ := current.(map[string]any)
	return resolved
}
