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
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/invopop/jsonschema"
)

// ToolHandler is the low-level handler signature used by FunctionTool.
//
// arguments is the raw JSON string received from the model.
type ToolHandler func(ctx context.Context, arguments string) (any, error)

// ToolMiddleware wraps a tool handler.
//
// Middlewares can be used to add cross-cutting behavior (e.g. input validation,
// permission checks, logging) without baking that logic into each tool implementation.
//
// Middleware order is preserved: if you call tool.Use(m1, m2), m1 runs before m2.
type ToolMiddleware func(tool *Tool, next ToolHandler) ToolHandler

// Tool is a Tool that wraps a function.
type Tool struct {
	// The name of the tool, as shown to the LLM. Generally the name of the function.
	name string

	// A description of the tool, as shown to the LLM.
	description string

	// The JSON schema for the tool's parameters.
	inputSchema map[string]any

	// Handle calls the underlying function with the given arguments.
	handle ToolHandler

	// middlewares wraps the tool handler to provide cross-cutting behavior
	// (e.g., input validation, permission checks, logging).
	middlewares []ToolMiddleware

	containsExternalContext bool
}

// Name returns the stable name used to identify this tool to the LLM.
func (t *Tool) Name() string {
	return t.name
}

// Description returns the description of the tool, as shown to the LLM.
func (t *Tool) Description() string {
	return t.description
}

// InputSchema returns the JSON schema for the tool's parameters, as shown to the LLM.
func (t *Tool) InputSchema() map[string]any {
	return t.inputSchema
}

// ContainsExternalContext reports whether successful output from this tool can
// introduce information from outside the local conversation and workspace.
func (t *Tool) ContainsExternalContext() bool {
	return t != nil && t.containsExternalContext
}

// SetContainsExternalContext marks successful output from this tool as external
// context for memory-isolation policy.
func (t *Tool) SetContainsExternalContext(value bool) *Tool {
	if t != nil {
		t.containsExternalContext = value
	}
	return t
}

// SetInputSchema replaces the schema sent to the LLM for Forebrain-owned tools.
// It deep-clones and normalizes the schema (without strict-mode forced
// required) so callers cannot mutate tool state.
func (t *Tool) SetInputSchema(schema map[string]any) error {
	if t == nil {
		return ErrToolNameRequired
	}
	schemaMap, err := jsonEncodeDecode[map[string]any](schema)
	if err != nil {
		return &ToolSchemaTransformError{Err: err}
	}
	schemaMap, err = ensureToolJSONSchema(schemaMap)
	if err != nil {
		return &ToolSchemaStrictnessError{Err: err}
	}
	t.inputSchema = schemaMap
	return nil
}

// ToolExecution is the result and executor-owned timing of one tool invocation.
type ToolExecution struct {
	Result any
	Timing ExecutionTiming
}

// Execute calls the complete middleware and handler chain and captures timing at
// that lowest shared execution boundary. Timing is returned for both success and
// failure so callers never need to start an independent timer.
func (t *Tool) Execute(ctx context.Context, arguments string) (execution ToolExecution, err error) {
	startedAt := time.Now()
	defer func() {
		execution.Timing = NewExecutionTiming(startedAt, time.Now())
	}()

	h := t.handle
	for i := len(t.middlewares) - 1; i >= 0; i-- {
		h = t.middlewares[i](t, h)
	}
	// Models occasionally send a value whose JSON type contradicts the schema
	// (e.g. `"prefix_rule": "[]"` — a string-encoded array — instead of a real
	// array). Repair such arguments against the tool's own input schema before
	// dispatch so the call is not lost to a parse error. Well-formed arguments
	// are returned byte-for-byte unchanged.
	arguments, _ = repairToolArguments(t.inputSchema, arguments)
	execution.Result, err = h(ctx, arguments)
	return execution, err
}

// Handle calls the underlying function with the given arguments.
// Deprecated execution callers that need timing should use Execute.
func (t *Tool) Handle(ctx context.Context, arguments string) (any, error) {
	execution, err := t.Execute(ctx, arguments)
	return execution.Result, err
}

// Use appends middleware(s) to this tool.
//
// Middlewares are executed in the order they are added.
func (t *Tool) Use(middlewares ...ToolMiddleware) *Tool {
	t.middlewares = append(t.middlewares, middlewares...)
	return t
}

// Clone returns an independent copy of this tool that shares the underlying
// handler closure (so it still operates on the same tool.State / approval
// hook as the original) but carries a fresh, empty middleware slice and a
// deep-copied input schema.
//
// The middlewares are intentionally reset to empty: the standard registration
// path (toolreg.Register) adds the default telemetry/trace middlewares
// itself, so copying the original's middlewares would double them. Treat
// Clone as "make a fresh tool wrapping the same handler, ready to be
// registered with a new agent".
//
// This is required whenever a tool that belongs to one agent (typically the
// parent's LoadedTools) is about to be re-registered with a different agent
// — a fork subagent or a hook agent. Registering a shared *Tool via
// toolreg.Register appends middlewares to it (and
// forkagent.ToolRegistry.Add does too), which would otherwise mutate the
// original tool's middleware slice and, when several fork subagents run
// concurrently (subagent_fanout), race on that slice — corrupting
// registration and leaving the subagent with no usable tools.
func (t *Tool) Clone() *Tool {
	if t == nil {
		return nil
	}
	schemaClone, err := jsonEncodeDecode[map[string]any](t.inputSchema)
	if err != nil || schemaClone == nil {
		schemaClone = make(map[string]any, len(t.inputSchema))
		for k, v := range t.inputSchema {
			schemaClone[k] = v
		}
	}
	return &Tool{
		name:                    t.name,
		description:             t.description,
		inputSchema:             schemaClone,
		handle:                  t.handle,
		middlewares:             nil,
		containsExternalContext: t.containsExternalContext,
	}
}

// NewTool creates a new Tool with the given name, description, and handler function.
//
// The handler function must be of the form func(context.Context, T) (R, error) where T and R can be any types.
// The input type T is used to generate a JSON schema for the tool's parameters, which is passed to the LLM.
// When the tool is called, the LLM will provide the arguments as a JSON string, which will be unmarshaled into T and passed to the handler.
// The handler's return value R will be returned as the result of the tool call.
//
// Example usage:
//
//	type Input struct {
//		Text string `json:"text"`
//	}
//
//	type Output struct {
//		Reversed string `json:"reversed"`
//	}
//
//	func reverse(ctx context.Context, input Input) (Output, error) {
//		// reverse the input text and return it in Output.Reversed
//	}
//
//	reverseTool, err := NewTool(
//		"reverse",
//		"use this function to reverse a string",
//		reverse,
//	)
//	if err != nil {
//		// handle error
//	}
func NewTool[T, R any](name, description string, handler func(ctx context.Context, args T) (R, error)) (*Tool, error) {
	if name == "" {
		return nil, ErrToolNameRequired
	}

	reflector := &jsonschema.Reflector{
		RequiredFromJSONSchemaTags: false,
		AllowAdditionalProperties:  false,
	}

	var zero T
	t := reflect.TypeOf(zero)
	if t == nil {
		return nil, &ToolNilInputTypeError{ToolName: name}
	}

	schemaType := t
	schemaTarget := any(&zero)
	if t.Kind() == reflect.Pointer && t.Elem().Kind() == reflect.Struct {
		// If the handler takes a pointer-to-struct input (e.g. *Input), we still want
		// to generate a strict object schema based on the underlying struct (Input),
		// not a nullable pointer schema.
		schemaType = t.Elem()
		schemaTarget = reflect.New(schemaType).Interface()
	}
	var schema *jsonschema.Schema
	if schemaType.Kind() == reflect.Struct && schemaType.Name() == "" && schemaType.NumField() == 0 {
		// Avoid panic in jsonschema when reflecting an anonymous empty struct
		schema = &jsonschema.Schema{
			Version:    jsonschema.Version,
			Type:       "object",
			Properties: jsonschema.NewProperties(),
		}
		if !reflector.AllowAdditionalProperties {
			schema.AdditionalProperties = jsonschema.FalseSchema
		}
	} else if schemaType.Kind() == reflect.Struct && schemaType.Name() == "" {
		// invopop/jsonschema v0.13.0 panics when ExpandedStruct is enabled for anonymous
		// struct roots because it looks up an empty definition name. Fall back to
		// non-expanded reflection for anonymous struct inputs.
		schema = reflector.Reflect(schemaTarget)
	} else if schemaType.Kind() == reflect.Struct {
		structReflector := *reflector
		structReflector.ExpandedStruct = true
		schema = structReflector.Reflect(schemaTarget)
	} else {
		schema = reflector.Reflect(schemaTarget)
	}

	schemaMap, err := mapFromJSON(schema)
	if err != nil {
		return nil, &ToolSchemaTransformError{Err: err}
	}

	schemaMap, err = ensureToolJSONSchema(schemaMap)
	if err != nil {
		return nil, &ToolSchemaStrictnessError{Err: err}
	}

	return &Tool{
		name:        name,
		description: description,
		inputSchema: schemaMap,
		handle: func(ctx context.Context, arguments string) (any, error) {
			var args T
			if err := json.Unmarshal([]byte(arguments), &args); err != nil {
				return nil, &ToolArgumentParseError{Err: err, Arguments: arguments}
			}
			return handler(ctx, args)
		},
	}, nil
}

// NewRawTool creates a new Tool from an externally supplied JSON Schema and a raw handler.
//
// Use this when the input schema is already known rather than inferred from a Go type —
// for example when forwarding tools from an MCP server or loading them from configuration.
//
// The provided schema is deep-cloned via a JSON round-trip and then normalized
// through ensureToolJSONSchema (no strict-mode forced required), so the
// caller's original map is never mutated.
//
// The handler receives the raw JSON argument string exactly as sent by the model,
// without any intermediate unmarshaling.
func NewRawTool(name, description string, inputSchema map[string]any, handler ToolHandler) (*Tool, error) {
	if name == "" {
		return nil, ErrToolNameRequired
	}

	// Deep-clone via JSON round-trip so the caller's map is never mutated.
	schemaMap, err := jsonEncodeDecode[map[string]any](inputSchema)
	if err != nil {
		return nil, &ToolSchemaTransformError{Err: err}
	}

	schemaMap, err = ensureToolJSONSchema(schemaMap)
	if err != nil {
		return nil, &ToolSchemaStrictnessError{Err: err}
	}

	return &Tool{
		name:        name,
		description: description,
		inputSchema: schemaMap,
		handle:      handler,
	}, nil
}
