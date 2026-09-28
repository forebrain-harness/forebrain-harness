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
	"fmt"
	"strconv"
	"strings"
)

var emptySchema = map[string]any{
	"additionalProperties": false,
	"type":                 "object",
	"properties":           map[string]any{},
	"required":             []any{},
}

// ensureToolJSONSchema normalizes a tool input schema for model consumption
// without the strict-mode requirement that every property be required. Tool
// schemas therefore keep their optional fields optional (reflected from
// `json:",omitempty"`), which lets models omit escalation-only fields instead
// of being forced to emit them — the root cause of a class of string-encoded
// array/object parse failures. All other normalization (additionalProperties,
// $ref inlining, anyOf/oneOf handling, nil defaults) is shared with strict.
func ensureToolJSONSchema(schema map[string]any) (map[string]any, error) {
	return normalizeJSONSchema(schema, false)
}

func normalizeJSONSchema(schema map[string]any, forceRequired bool) (map[string]any, error) {
	if len(schema) == 0 {
		// Return a copy of the empty schema
		result := make(map[string]any, len(emptySchema))
		for k, v := range emptySchema {
			result[k] = v
		}
		return result, nil
	}
	return ensureStrictJSONSchemaRecursive(schema, nil, schema, map[string]bool{}, forceRequired)
}

// ensureStrictJSONSchemaRecursive recursively processes a JSON schema to make it strict.
//
// expanding tracks the set of $ref targets currently being inlined along the
// active branch (an ancestor stack). It breaks cyclic/recursive $ref chains:
// without it, always-inlining a self-referential schema never terminates.
func ensureStrictJSONSchemaRecursive(jsonSchema any, path []string, root map[string]any, expanding map[string]bool, forceRequired bool) (map[string]any, error) {
	schema, ok := jsonSchema.(map[string]any)
	if !ok {
		return nil, &SchemaExpectedMapError{Got: jsonSchema, Path: path}
	}

	// Process $defs
	if defs, ok := schema["$defs"]; ok {
		if defsMap, ok := defs.(map[string]any); ok {
			for defName, defSchema := range defsMap {
				newPath := append(append([]string{}, path...), "$defs", defName)
				_, err := ensureStrictJSONSchemaRecursive(defSchema, newPath, root, expanding, forceRequired)
				if err != nil {
					return nil, err
				}
			}
		}
	}

	// Process definitions
	if definitions, ok := schema["definitions"]; ok {
		if defsMap, ok := definitions.(map[string]any); ok {
			for defName, defSchema := range defsMap {
				newPath := append(append([]string{}, path...), "definitions", defName)
				_, err := ensureStrictJSONSchemaRecursive(defSchema, newPath, root, expanding, forceRequired)
				if err != nil {
					return nil, err
				}
			}
		}
	}

	// Handle object types and additionalProperties
	typ, hasType := schema["type"]
	if hasType && typ == "object" {
		if _, hasAdditional := schema["additionalProperties"]; !hasAdditional {
			schema["additionalProperties"] = false
		} else if additionalProps := schema["additionalProperties"]; additionalProps == true {
			return nil, ErrSchemaAdditionalPropertiesSet
		}
	}

	// Process properties
	if properties, ok := schema["properties"]; ok {
		if propsMap, ok := properties.(map[string]any); ok {
			// Strict mode forces every property to be required; tool schemas
			// keep the required list produced by reflection (based on
			// `json:",omitempty"`), so optional fields stay optional.
			if forceRequired {
				required := make([]any, 0, len(propsMap))
				for key := range propsMap {
					required = append(required, key)
				}
				schema["required"] = required
			}

			// Recursively process each property
			newPropsMap := make(map[string]any, len(propsMap))
			for key, propSchema := range propsMap {
				newPath := append(append([]string{}, path...), "properties", key)
				processed, err := ensureStrictJSONSchemaRecursive(propSchema, newPath, root, expanding, forceRequired)
				if err != nil {
					return nil, err
				}
				newPropsMap[key] = processed
			}
			schema["properties"] = newPropsMap
		}
	}

	// Process array items
	if items, ok := schema["items"]; ok {
		if _, isMap := items.(map[string]any); isMap {
			newPath := append(append([]string{}, path...), "items")
			processed, err := ensureStrictJSONSchemaRecursive(items, newPath, root, expanding, forceRequired)
			if err != nil {
				return nil, err
			}
			schema["items"] = processed
		}
	}

	// Process anyOf
	if anyOf, ok := schema["anyOf"]; ok {
		if anyOfList, ok := anyOf.([]any); ok {
			newAnyOf := make([]any, len(anyOfList))
			for i, variant := range anyOfList {
				newPath := append(append([]string{}, path...), "anyOf", strconv.Itoa(i))
				processed, err := ensureStrictJSONSchemaRecursive(variant, newPath, root, expanding, forceRequired)
				if err != nil {
					return nil, err
				}
				newAnyOf[i] = processed
			}
			schema["anyOf"] = newAnyOf
		}
	}

	// Convert oneOf to anyOf (oneOf is not supported by OpenAI's structured outputs in nested contexts)
	if oneOf, ok := schema["oneOf"]; ok {
		if oneOfList, ok := oneOf.([]any); ok {
			// Get existing anyOf or start with empty list
			existingAnyOf, _ := schema["anyOf"].([]any)
			if existingAnyOf == nil {
				existingAnyOf = []any{}
			}

			// Process each oneOf variant
			for i, variant := range oneOfList {
				newPath := append(append([]string{}, path...), "oneOf", strconv.Itoa(i))
				processed, err := ensureStrictJSONSchemaRecursive(variant, newPath, root, expanding, forceRequired)
				if err != nil {
					return nil, err
				}
				existingAnyOf = append(existingAnyOf, processed)
			}
			schema["anyOf"] = existingAnyOf
			delete(schema, "oneOf")
		}
	}

	// Process allOf
	if allOf, ok := schema["allOf"]; ok {
		if allOfList, ok := allOf.([]any); ok {
			if len(allOfList) == 1 {
				// Merge single allOf entry into parent
				newPath := append(append([]string{}, path...), "allOf", "0")
				processed, err := ensureStrictJSONSchemaRecursive(allOfList[0], newPath, root, expanding, forceRequired)
				if err != nil {
					return nil, err
				}
				// Update schema with processed allOf content
				for k, v := range processed {
					schema[k] = v
				}
				delete(schema, "allOf")
			} else {
				// Process each allOf entry
				newAllOf := make([]any, len(allOfList))
				for i, entry := range allOfList {
					newPath := append(append([]string{}, path...), "allOf", strconv.Itoa(i))
					processed, err := ensureStrictJSONSchemaRecursive(entry, newPath, root, expanding, forceRequired)
					if err != nil {
						return nil, err
					}
					newAllOf[i] = processed
				}
				schema["allOf"] = newAllOf
			}
		}
	}

	// Strip nil defaults
	if defaultVal, ok := schema["default"]; ok && defaultVal == nil {
		delete(schema, "default")
	}

	// Handle $ref expansion — inline ALL $ref references so providers that
	// cannot resolve $ref/$defs receive a fully self-contained schema.
	if ref, ok := schema["$ref"]; ok {
		refStr, ok := ref.(string)
		if !ok {
			return nil, &SchemaNonStringRefError{RawRef: ref}
		}

		if expanding[refStr] {
			// Cyclic/recursive $ref: this target is already being inlined
			// higher up the active branch. Inlining it again would never
			// terminate, and a recursive schema has no finite fully-inlined
			// form, so leave the $ref in place to break the cycle.
			return schema, nil
		}

		resolved, err := resolveRef(root, refStr)
		if err != nil {
			return nil, err
		}

		resolvedMap, ok := resolved.(map[string]any)
		if !ok {
			return nil, &SchemaExpectedMapError{Got: resolved, Path: path}
		}

		// Mark this ref as being expanded for the duration of the inlined
		// subtree (ancestor-stack semantics); clear it afterwards so the same
		// ref reached via a sibling, non-cyclic branch is still inlined.
		expanding[refStr] = true
		defer delete(expanding, refStr)

		if hasMoreThanNKeys(schema, 1) {
			// $ref with additional properties: merge resolved schema with
			// current schema (current schema takes priority)
			for k, v := range resolvedMap {
				if _, exists := schema[k]; !exists {
					schema[k] = v
				}
			}
			delete(schema, "$ref")
			return ensureStrictJSONSchemaRecursive(schema, path, root, expanding, forceRequired)
		}

		// Pure $ref: replace entire schema with a deep copy of the resolved
		// schema so the $defs entry is not aliased.
		resolvedCopy, err := mapFromJSON(resolvedMap)
		if err != nil {
			return nil, err
		}
		return ensureStrictJSONSchemaRecursive(resolvedCopy, path, root, expanding, forceRequired)
	}

	return schema, nil
}

// resolveRef resolves a JSON schema $ref value.
func resolveRef(root map[string]any, ref string) (any, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, &SchemaUnexpectedRefFormatError{Ref: ref}
	}

	pathParts := strings.Split(ref[2:], "/")
	var resolved any = root

	for _, key := range pathParts {
		resolvedMap, ok := resolved.(map[string]any)
		if !ok {
			return nil, &SchemaNonDictionaryWhileResolvingRefError{
				Ref:      ref,
				Resolved: resolved,
			}
		}

		value, exists := resolvedMap[key]
		if !exists {
			return nil, fmt.Errorf("key %q not found while resolving $ref %q", key, ref)
		}
		resolved = value
	}

	return resolved, nil
}

// hasMoreThanNKeys checks if a map has more than n keys.
func hasMoreThanNKeys(obj map[string]any, n int) bool {
	i := 0
	for range obj {
		i++
		if i > n {
			return true
		}
	}
	return false
}

// jsonEncodeDecode is a helper function that marshals a value to JSON and then unmarshals it into the specified type.
func jsonEncodeDecode[T any](v any) (T, error) {
	var result T
	b, err := json.Marshal(v)
	if err != nil {
		return result, err
	}

	err = json.Unmarshal(b, &result)
	if err != nil {
		return result, err
	}
	return result, nil
}

// mapFromJSON is a helper function that converts a value to a map[string]any using JSON encoding/decoding.
func mapFromJSON(v any) (map[string]any, error) {
	return jsonEncodeDecode[map[string]any](v)
}
