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
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

type testInput struct {
	Name string `json:"name"`
}

type testOutput struct {
	NickName string `json:"nickName"`
}

type testStringInput string

func TestToolExecuteTimesCompleteMiddlewareAndHandlerChain(t *testing.T) {
	tool, err := NewTool("timed", "", func(_ context.Context, _ *struct{}) (string, error) {
		time.Sleep(10 * time.Millisecond)
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	tool.Use(func(_ *Tool, next ToolHandler) ToolHandler {
		return func(ctx context.Context, arguments string) (any, error) {
			time.Sleep(10 * time.Millisecond)
			return next(ctx, arguments)
		}
	})

	execution, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if execution.Result != "ok" {
		t.Fatalf("result=%#v want ok", execution.Result)
	}
	if !execution.Timing.Valid() || execution.Timing.Duration < 20*time.Millisecond {
		t.Fatalf("timing does not cover middleware + handler: %#v", execution.Timing)
	}
}

func TestToolExecuteReturnsTimingOnErrorAndHandleRemainsCompatible(t *testing.T) {
	sentinel := errors.New("boom")
	tool, err := NewTool("timed_error", "", func(_ context.Context, _ *struct{}) (string, error) {
		return "partial", sentinel
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}

	execution, err := tool.Execute(context.Background(), `{}`)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Execute error=%v want sentinel", err)
	}
	if execution.Result != "partial" || !execution.Timing.Valid() {
		t.Fatalf("execution=%#v", execution)
	}

	result, err := tool.Handle(context.Background(), `{}`)
	if !errors.Is(err, sentinel) || result != "partial" {
		t.Fatalf("Handle result=%#v err=%v", result, err)
	}
}

func TestNewFunctionTool_PointerInput_SchemaIsStrictObject(t *testing.T) {
	tool, err := NewTool("nick", "", func(_ context.Context, in *testInput) (*testOutput, error) {
		if in == nil {
			return nil, nil
		}
		return &testOutput{NickName: in.Name + "y"}, nil
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if got, _ := tool.InputSchema()["type"].(string); got != "object" {
		t.Fatalf("schema type: expected %q, got %#v", "object", tool.InputSchema()["type"])
	}
	if _, ok := tool.InputSchema()["anyOf"]; ok {
		t.Fatalf("expected no top-level anyOf in schema, got: %#v", tool.InputSchema()["anyOf"])
	}

	props, ok := tool.InputSchema()["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties: expected map, got %#v", tool.InputSchema()["properties"])
	}
	if _, ok := props["name"]; !ok {
		t.Fatalf("schema properties: expected key %q, got %#v", "name", props)
	}
}

func TestNewFunctionTool_PointerInput_HandleDecodesIntoPointer(t *testing.T) {
	var gotName string
	tool, err := NewTool("nick", "", func(_ context.Context, in *testInput) (*testOutput, error) {
		if in == nil {
			gotName = "<nil>"
			return &testOutput{NickName: "nil"}, nil
		}
		gotName = in.Name
		return &testOutput{NickName: in.Name + "y"}, nil
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	result, err := tool.Handle(context.Background(), `{"name":"Simone"}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	out, ok := result.(*testOutput)
	if !ok {
		t.Fatalf("expected *testOutput result, got %#v", result)
	}
	if gotName != "Simone" {
		t.Fatalf("handler input: expected %q, got %q", "Simone", gotName)
	}
	if out.NickName != "Simoney" {
		t.Fatalf("output: expected %q, got %q", "Simoney", out.NickName)
	}
}

func TestNewFunctionTool_PointerToAnonymousEmptyStruct_DoesNotPanic(t *testing.T) {
	type empty = struct{}

	tool, err := NewTool("noop", "", func(_ context.Context, _ *empty) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if got, _ := tool.InputSchema()["type"].(string); got != "object" {
		t.Fatalf("schema type: expected %q, got %#v", "object", tool.InputSchema()["type"])
	}
}

func TestNewFunctionTool_PointerToAnonymousStructWithFields_DoesNotPanic(t *testing.T) {
	tool, err := NewTool("echo", "", func(_ context.Context, in *struct {
		Text string `json:"text"`
	}) (string, error) {
		if in == nil {
			return "", nil
		}
		return in.Text, nil
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if got, _ := tool.InputSchema()["type"].(string); got != "object" {
		t.Fatalf("schema type: expected %q, got %#v", "object", tool.InputSchema()["type"])
	}

	props, ok := tool.InputSchema()["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties: expected map, got %#v", tool.InputSchema()["properties"])
	}
	if _, ok := props["text"]; !ok {
		t.Fatalf("schema properties: expected key %q, got %#v", "text", props)
	}
}

func TestNewFunctionTool_NamedNonStructInput_DoesNotPanic(t *testing.T) {
	tool, err := NewTool("echo", "", func(_ context.Context, in testStringInput) (string, error) {
		return string(in), nil
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if got, _ := tool.InputSchema()["type"].(string); got != "string" {
		t.Fatalf("schema type: expected %q, got %#v", "string", tool.InputSchema()["type"])
	}
}

func TestToolMiddleware_Order(t *testing.T) {
	var calls []string
	tool, err := NewTool("nick", "", func(_ context.Context, in *testInput) (*testOutput, error) {
		calls = append(calls, "handler")
		return &testOutput{NickName: in.Name}, nil
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	tool.Use(
		func(_ *Tool, next ToolHandler) ToolHandler {
			return func(ctx context.Context, arguments string) (any, error) {
				calls = append(calls, "mw1")
				return next(ctx, arguments)
			}
		},
		func(_ *Tool, next ToolHandler) ToolHandler {
			return func(ctx context.Context, arguments string) (any, error) {
				calls = append(calls, "mw2")
				return next(ctx, arguments)
			}
		},
	)

	_, err = tool.Handle(context.Background(), `{"name":"Simone"}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !reflect.DeepEqual(calls, []string{"mw1", "mw2", "handler"}) {
		t.Fatalf("unexpected call order: %#v", calls)
	}
}

func TestToolMiddleware_InputValidation_ShortCircuits(t *testing.T) {
	var handled bool
	tool, err := NewTool("nick", "", func(_ context.Context, _ *testInput) (*testOutput, error) {
		handled = true
		return &testOutput{NickName: "x"}, nil
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	tool.Use(func(_ *Tool, next ToolHandler) ToolHandler {
		return func(ctx context.Context, arguments string) (any, error) {
			// For this test we don't need to decode JSON; just reject based on raw args.
			_ = arguments
			return nil, errors.New("invalid")
		}
	})

	_, err = tool.Handle(context.Background(), `{"name":"nope"}`)
	if err == nil {
		t.Fatalf("expected error")
	}
	if handled {
		t.Fatalf("expected handler not to run")
	}
}

func TestToolClone_IsolatesMiddlewaresFromOriginal(t *testing.T) {
	tool, err := NewTool("nick", "desc", func(_ context.Context, in *testInput) (*testOutput, error) {
		return &testOutput{NickName: in.Name}, nil
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	// Original accumulates middlewares (mimicking toolreg.Register on the parent).
	var origCalls []string
	tool.Use(func(_ *Tool, next ToolHandler) ToolHandler {
		return func(ctx context.Context, arguments string) (any, error) {
			origCalls = append(origCalls, "parent-mw")
			return next(ctx, arguments)
		}
	})

	clone := tool.Clone()
	if clone == nil {
		t.Fatalf("clone is nil")
	}
	if clone.Name() != tool.Name() {
		t.Fatalf("clone name = %q, want %q", clone.Name(), tool.Name())
	}
	if clone.Description() != tool.Description() {
		t.Fatalf("clone desc = %q, want %q", clone.Description(), tool.Description())
	}
	// Clone starts with an empty middleware slice so a fresh registration adds
	// its own; it must not inherit the parent's middlewares (which would double
	// when toolreg.Register re-adds the defaults).
	if _, err := clone.Handle(context.Background(), `{"name":"x"}`); err != nil {
		t.Fatalf("clone handle: %v", err)
	}
	if len(origCalls) != 0 {
		t.Fatalf("parent middleware ran on clone: %#v", origCalls)
	}

	// Adding a middleware to the clone must not mutate the original.
	var cloneCalls []string
	clone.Use(func(_ *Tool, next ToolHandler) ToolHandler {
		return func(ctx context.Context, arguments string) (any, error) {
			cloneCalls = append(cloneCalls, "clone-mw")
			return next(ctx, arguments)
		}
	})
	if _, err := tool.Handle(context.Background(), `{"name":"y"}`); err != nil {
		t.Fatalf("original handle after clone mutation: %v", err)
	}
	if len(cloneCalls) != 0 {
		t.Fatalf("clone middleware leaked into original: %#v", cloneCalls)
	}
	// The original's middleware runs once per original Handle. It must have run
	// exactly once here (the clone-mw added to the clone must not have pushed
	// a second invocation through the original).
	if len(origCalls) != 1 {
		t.Fatalf("expected parent mw to run once, got %d", len(origCalls))
	}
}

func TestToolClone_DeepCopiesInputSchema(t *testing.T) {
	tool, err := NewRawTool("raw", "desc", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"x": map[string]any{"type": "string"},
		},
	}, func(_ context.Context, _ string) (any, error) { return nil, nil })
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	clone := tool.Clone()
	props, ok := clone.InputSchema()["properties"].(map[string]any)
	if !ok {
		t.Fatalf("clone schema properties missing: %#v", clone.InputSchema())
	}
	props["injected"] = "tampered"
	// Mutating the clone's schema must not affect the original.
	origProps, ok := tool.InputSchema()["properties"].(map[string]any)
	if !ok {
		t.Fatalf("orig schema properties missing")
	}
	if _, exists := origProps["injected"]; exists {
		t.Fatalf("clone schema mutation leaked into original: %#v", origProps)
	}
}

func TestToolClone_NilSafe(t *testing.T) {
	var nilTool *Tool
	if got := nilTool.Clone(); got != nil {
		t.Fatalf("expected nil clone for nil tool, got %v", got)
	}
}

func TestNewRawTool_PreservesObjectSchema(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city": map[string]any{"type": "string"},
		},
	}
	tool, err := NewRawTool("weather", "get weather", schema, func(_ context.Context, args string) (any, error) {
		return args, nil
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if got, _ := tool.InputSchema()["type"].(string); got != "object" {
		t.Fatalf("schema type: expected %q, got %#v", "object", tool.InputSchema()["type"])
	}
	props, ok := tool.InputSchema()["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map, got %#v", tool.InputSchema()["properties"])
	}
	if _, ok := props["city"]; !ok {
		t.Fatalf("expected 'city' property, got %#v", props)
	}
}

func TestToolSetInputSchemaOverridesAndClonesSchema(t *testing.T) {
	tool, err := NewTool("echo", "echo", func(_ context.Context, in *testInput) (string, error) {
		if in == nil {
			return "", nil
		}
		return in.Name, nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}

	override := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"mode": map[string]any{
				"type": "string",
				"enum": []any{"a", "b"},
			},
		},
	}
	if err := tool.SetInputSchema(override); err != nil {
		t.Fatalf("SetInputSchema: %v", err)
	}
	override["properties"].(map[string]any)["mode"].(map[string]any)["enum"] = []any{"mutated"}

	props, ok := tool.InputSchema()["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", tool.InputSchema()["properties"])
	}
	mode, ok := props["mode"].(map[string]any)
	if !ok {
		t.Fatalf("mode schema = %#v", props["mode"])
	}
	got, ok := mode["enum"].([]any)
	if !ok {
		t.Fatalf("enum = %#v", mode["enum"])
	}
	if fmt.Sprint(got) != "[a b]" {
		t.Fatalf("enum = %#v, want [a b]", got)
	}
	if got := tool.InputSchema()["additionalProperties"]; got != false {
		t.Fatalf("additionalProperties = %#v, want false", got)
	}
}

func TestNewRawTool_HandlerReceivesRawJSON(t *testing.T) {
	var got string
	tool, err := NewRawTool("echo", "", map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}, func(_ context.Context, args string) (any, error) {
		got = args
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	payload := `{"hello":"world"}`
	_, err = tool.Handle(context.Background(), payload)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if got != payload {
		t.Fatalf("handler got %q, want %q", got, payload)
	}
}

func TestNewRawTool_DoesNotMutateCallerSchema(t *testing.T) {
	original := map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": map[string]any{"type": "integer"}},
	}
	_, err := NewRawTool("t", "desc", original, func(_ context.Context, _ string) (any, error) { return nil, nil })
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if _, ok := original["description"]; ok {
		t.Fatalf("original schema was mutated: 'description' key was added")
	}
}
