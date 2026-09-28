package telemetry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func TestDebugFileLogEnabled_Default(t *testing.T) {
	// Default should be true when env var is not set
	orig := os.Getenv("FOREBRAIN_LLM_HTTP_DEBUG")
	os.Unsetenv("FOREBRAIN_LLM_HTTP_DEBUG")
	defer func() {
		if orig != "" {
			os.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", orig)
		} else {
			os.Unsetenv("FOREBRAIN_LLM_HTTP_DEBUG")
		}
	}()

	if !debugFileLogEnabled() {
		t.Error("expected default to be true when env var is unset")
	}
}

func TestDebugFileLogEnabled_ExplicitTrue(t *testing.T) {
	os.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "1")
	defer os.Unsetenv("FOREBRAIN_LLM_HTTP_DEBUG")
	if !debugFileLogEnabled() {
		t.Error("expected true for '1'")
	}
}

func TestDebugFileLogEnabled_ExplicitFalse(t *testing.T) {
	os.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")
	defer os.Unsetenv("FOREBRAIN_LLM_HTTP_DEBUG")
	if debugFileLogEnabled() {
		t.Error("expected false for '0'")
	}
}

func TestDebugFileLogEnabled_FalseString(t *testing.T) {
	for _, v := range []string{"false", "False", "FALSE", "no", "No", "off", "Off"} {
		os.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", v)
		if debugFileLogEnabled() {
			t.Errorf("expected false for %q", v)
		}
	}
}

func TestDebugFileLogEnabled_TrueStrings(t *testing.T) {
	for _, v := range []string{"true", "True", "TRUE", "yes", "Yes", "on", "On", "1"} {
		os.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", v)
		if !debugFileLogEnabled() {
			t.Errorf("expected true for %q", v)
		}
	}
}

func TestDebugFileLogEnabled_WhitespaceTrimmed(t *testing.T) {
	os.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "  false  ")
	defer os.Unsetenv("FOREBRAIN_LLM_HTTP_DEBUG")
	if debugFileLogEnabled() {
		t.Error("expected whitespace-trimmed 'false' to return false")
	}
}

func TestDebugFileLogEnabled_ArbitraryValue(t *testing.T) {
	os.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "anything")
	defer os.Unsetenv("FOREBRAIN_LLM_HTTP_DEBUG")
	if !debugFileLogEnabled() {
		t.Error("expected arbitrary non-falsy value to return true")
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

func TestToolDebugFileLogMiddlewareLogsSuccessAndError(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "logs"), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	t.Setenv("FOREBRAIN_HOME", tmp)
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "1")
	resetEmitForTest(t)

	tool, err := llm.NewTool("logged_tool", "desc", func(context.Context, *struct{}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	h := NewToolDebugFileLogMiddleware()(tool, func(context.Context, string) (any, error) {
		return "value", nil
	})
	if out, err := h(context.Background(), `{"x":1}`); err != nil || out != "value" {
		t.Fatalf("handler result = %#v err=%v", out, err)
	}
	raw, err := os.ReadFile(filepath.Join(tmp, "logs", "debug.log"))
	if err != nil {
		t.Fatalf("read debug log: %v", err)
	}
	text := string(raw)
	for _, want := range []string{"tool_call", "tool_result", "tool_name=logged_tool", `arguments_raw:`, `{"x":1}`, "result_value:", "value"} {
		if !strings.Contains(text, want) {
			t.Fatalf("debug log missing %q: %s", want, text)
		}
	}

	wantErr := errors.New("boom")
	h = NewToolDebugFileLogMiddleware()(nil, func(context.Context, string) (any, error) {
		return nil, wantErr
	})
	if _, err := h(context.Background(), `{}`); !errors.Is(err, wantErr) {
		t.Fatalf("handler error = %v, want %v", err, wantErr)
	}
	raw, err = os.ReadFile(filepath.Join(tmp, "logs", "debug.log"))
	if err != nil {
		t.Fatalf("read debug log after error: %v", err)
	}
	if !strings.Contains(string(raw), `handler_error="boom"`) || !strings.Contains(string(raw), "tool_name=") {
		t.Fatalf("error log missing handler error: %s", string(raw))
	}
}

func TestEmitNoopsWhenDisabledOrLogDirectoryMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", tmp)
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")
	resetEmitForTest(t)
	writeDebugFileLog("disabled", "msg")
	if _, err := os.Stat(filepath.Join(tmp, "logs", "debug.log")); !os.IsNotExist(err) {
		t.Fatalf("disabled emit created log, err=%v", err)
	}

	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "1")
	resetEmitForTest(t)
	writeDebugFileLog("missing-dir", "msg")
	if _, err := os.Stat(filepath.Join(tmp, "logs", "debug.log")); !os.IsNotExist(err) {
		t.Fatalf("emit with missing logs dir created log, err=%v", err)
	}
}

func TestEmitRootLookupFailure(t *testing.T) {
	oldRootFn := rootDirFn
	rootDirFn = func() (string, error) { return "", errors.New("boom") }
	defer func() { rootDirFn = oldRootFn }()
	resetEmitForTest(t)
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "1")
	writeDebugFileLog("broken", "msg")
}

func resetEmitForTest(t *testing.T) {
	t.Helper()
	debugFileLogMu.Lock()
	if debugFileLogFile != nil {
		_ = debugFileLogFile.Close()
	}
	debugFileLogFile = nil
	debugFileLogOnce = sync.Once{}
	debugFileLogMu.Unlock()
	t.Cleanup(func() {
		debugFileLogMu.Lock()
		if debugFileLogFile != nil {
			_ = debugFileLogFile.Close()
		}
		debugFileLogFile = nil
		debugFileLogOnce = sync.Once{}
		debugFileLogMu.Unlock()
	})
}
