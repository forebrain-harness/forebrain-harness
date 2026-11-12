package telemetry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// toolHandler runs one tool call through the middleware with the given tool
// name and arguments, and returns what the wrapped handler returned.
func toolHandler(t *testing.T, name, arguments string, handler llm.ToolHandler) (any, error) {
	t.Helper()
	var tool *llm.Tool
	if name != "" {
		built, err := llm.NewTool(name, "test tool", func(context.Context, *struct{}) (string, error) {
			return "", nil
		})
		if err != nil {
			t.Fatalf("NewTool: %v", err)
		}
		tool = built
	}
	return NewToolEventMiddleware()(tool, handler)(context.Background(), arguments)
}

// onlyToolCall and onlyToolResult are one call's two records.
func onlyToolCall(t *testing.T, root string) Record {
	t.Helper()
	records := recordsWithMsg(t, root, "tool call")
	if len(records) != 1 {
		t.Fatalf("tool call records = %+v, want exactly one", records)
	}
	return records[0]
}

func onlyToolResult(t *testing.T, root string) Record {
	t.Helper()
	records := recordsWithMsg(t, root, "tool result")
	if len(records) != 1 {
		t.Fatalf("tool result records = %+v, want exactly one", records)
	}
	return records[0]
}

// One call is two records that share their call id — the id is what pairs them,
// not the text of the arguments.
func TestToolEventsPairByCallID(t *testing.T) {
	root := exchangeLog(t)
	registerSessions(t)

	var tool *llm.Tool
	built, err := llm.NewTool("shell", "test tool", func(context.Context, *struct{}) (string, error) {
		return "", nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	tool = built
	ctx := context.WithValue(context.Background(), sessionKey{}, "session-tool")
	handler := NewToolEventMiddleware()(tool, func(context.Context, string) (any, error) {
		return "the result", nil
	})
	if out, err := handler(ctx, `{"command":"echo hi"}`); err != nil || out != "the result" {
		t.Fatalf("handler = (%v, %v)", out, err)
	}

	records := recordsRead(t, root)
	if len(records) != 2 {
		t.Fatalf("records = %+v, want the call and its result", records)
	}
	call, result := records[0], records[1]
	if call.Msg != "tool call" || result.Msg != "tool result" {
		t.Fatalf("messages = %q, %q", call.Msg, result.Msg)
	}
	if call.Kind != kindTool || result.Kind != kindTool {
		t.Fatalf("kinds = %q, %q, want %q", call.Kind, result.Kind, kindTool)
	}
	first, _ := call.Attrs["tool_call"].(string)
	second, _ := result.Attrs["tool_call"].(string)
	if first == "" || first != second {
		t.Fatalf("call ids do not pair: %q and %q", first, second)
	}
	if call.Level != "DEBUG" || result.Level != "INFO" {
		t.Fatalf("levels = %q, %q, want DEBUG then INFO", call.Level, result.Level)
	}
	if call.Session != "session-tool" || result.Session != "session-tool" {
		t.Fatalf("sessions = %q, %q, want the context's own", call.Session, result.Session)
	}
	if got := call.Attrs["args"]; got != `{"command":"echo hi"}` {
		t.Fatalf("inline arguments = %v", got)
	}
	if got := result.Attrs["result"]; got != "the result" {
		t.Fatalf("inline result = %v", got)
	}
}

// The arguments are the model's own output: past the inline cap they are kept
// whole in their own file, byte for byte.
func TestToolArgumentsAreKeptWhole(t *testing.T) {
	root := exchangeLog(t)
	arguments := strings.Repeat("a", 200*1024) + `UNIQUE_ARGUMENT_TAIL_MARKER"`
	if out, err := toolHandler(t, "shell", arguments, func(context.Context, string) (any, error) {
		return "ok", nil
	}); err != nil || out != "ok" {
		t.Fatalf("handler = (%v, %v)", out, err)
	}

	call := onlyToolCall(t, root)
	if _, inline := call.Attrs["args"]; inline {
		t.Fatal("a 200 KiB argument list was inlined into the record")
	}
	if preview, _ := call.Attrs["args_preview"].(string); len(preview) == 0 || len(preview) > payloadPreviewBytes {
		t.Fatalf("args_preview = %d bytes, want up to %d", len(preview), payloadPreviewBytes)
	}
	ref := call.Blobs["args"]
	if !ref.Whole || ref.Bytes != int64(len(arguments)) {
		t.Fatalf("blob.args = %+v, want the whole %d bytes", ref, len(arguments))
	}
	if got := gunzipPayload(t, payloadPath(t, root, ref.Name)); got != arguments {
		t.Fatalf("arguments file = %d bytes, want the original %d", len(got), len(arguments))
	}
}

// A result is not model output: it is bounded once, and the record says how
// much that left out.
func TestToolResultIsBoundedOnce(t *testing.T) {
	root := exchangeLog(t)
	result := strings.Repeat("r", 300*1024)
	if _, err := toolHandler(t, "shell", `{}`, func(context.Context, string) (any, error) {
		return result, nil
	}); err != nil {
		t.Fatalf("handler: %v", err)
	}

	call := onlyToolResult(t, root)
	ref := call.Blobs["result"]
	if ref.Whole {
		t.Fatalf("blob.result = %+v, want a bounded result", ref)
	}
	if want := int64(len(result) - DefaultRecordBytes); ref.Omitted != want {
		t.Fatalf("omitted = %d, want %d", ref.Omitted, want)
	}
	text := gunzipPayload(t, payloadPath(t, root, ref.Name))
	if got := strings.Count(text, "log bytes omitted"); got != 1 {
		t.Fatalf("omission markers = %d, want exactly 1", got)
	}
	if len(text) > DefaultRecordBytes+128 {
		t.Fatalf("result payload = %d bytes, want at most %d", len(text), DefaultRecordBytes+128)
	}
}

// A tool that failed is a WARN record carrying its error, and no result.
func TestToolHandlerErrorIsAWarning(t *testing.T) {
	root := exchangeLog(t)
	wantErr := errors.New("handler exploded")
	if _, err := toolHandler(t, "shell", `{}`, func(context.Context, string) (any, error) {
		return nil, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("handler error = %v, want it passed through", err)
	}

	call := onlyToolResult(t, root)
	if call.Level != "WARN" {
		t.Fatalf("level = %q, want WARN", call.Level)
	}
	if call.Err != "handler exploded" {
		t.Fatalf("err = %q", call.Err)
	}
	if _, ok := call.Blobs["result"]; ok {
		t.Fatal("a failed call recorded a result payload")
	}
	if _, ok := call.Attrs["result"]; ok {
		t.Fatal("a failed call recorded an inline result")
	}
}

func TestFormatToolResultValueAndSerializeContentParts(t *testing.T) {
	if got := formatToolResultValue(nil); got != "<nil>" {
		t.Fatalf("nil result = %q", got)
	}
	if got := formatToolResultValue("plain"); got != "plain" {
		t.Fatalf("string result = %q", got)
	}
	parts := []llm.ContentPart{
		llm.Text("hello"),
		llm.ImageURL("https://example.test/image.png"),
		llm.ImageBase64("image/png", "AAAA"),
		{Type: llm.ContentType("custom")},
	}
	got := formatToolResultValue(parts)
	for _, want := range []string{`"type": "text"`, `"text": "hello"`, `"image_url": "https://example.test/image.png"`, `"mime_type": "image/png"`, `"image_base64": "AAAA"`, `"type": "custom"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted content parts missing %q: %s", want, got)
		}
	}
	if got := formatToolResultValue(map[string]any{"b": 2}); !strings.Contains(got, `"b": 2`) {
		t.Fatalf("map formatted as %q", got)
	}
	ch := make(chan int)
	if got := formatToolResultValue(ch); !strings.Contains(got, "chan int") {
		t.Fatalf("non-json result fallback = %q", got)
	}
}
