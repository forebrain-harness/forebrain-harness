package telemetry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	tracetest "go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestStartRunSpanInjectsRecorderIntoContext(t *testing.T) {
	ctx, endRun := StartRunSpan(context.Background(), "run-1", "session-1", "test")
	defer endRun()
	rec := RecorderFromContext(ctx)
	if rec == nil {
		t.Fatal("expected recorder in run context")
	}
	if rec.runID != "run-1" {
		t.Fatalf("recorder runID = %q", rec.runID)
	}
}

func TestToolTraceMiddlewareCreatesSpans(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider()
	tp.RegisterSpanProcessor(sr)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	}()

	ctx, endRun := StartRunSpan(context.Background(), "run-42", "session-7", "test")
	defer endRun()

	tool, err := llm.NewTool("sample_tool", "desc", func(context.Context, *struct{}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	tool.Use(NewToolTraceMiddleware())
	if out, err := tool.Handle(ctx, `{}`); err != nil || out != "ok" {
		t.Fatalf("tool.Handle = %#v err=%v", out, err)
	}

	ended := sr.Ended()
	if len(ended) == 0 {
		t.Fatal("expected at least one ended span")
	}
	var foundTool bool
	for _, sp := range ended {
		if sp.Name() == "tool.sample_tool" {
			foundTool = true
			attrs := sp.Attributes()
			if len(attrs) == 0 {
				t.Fatal("tool span missing attributes")
			}
			var hasArgs bool
			var hasPreview bool
			for _, attr := range attrs {
				switch string(attr.Key) {
				case "forebrain.tool.arguments":
					hasArgs = attr.Value.AsString() == `{}`
				case "forebrain.tool.result_preview":
					hasPreview = attr.Value.AsString() == "ok"
				}
			}
			if !hasArgs || !hasPreview {
				t.Fatalf("tool span missing arguments/result preview attrs: %+v", attrs)
			}
		}
	}
	if !foundTool {
		t.Fatal("tool span not found")
	}
}

func TestToolTraceMiddlewareEndsSpanOnError(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider()
	tp.RegisterSpanProcessor(sr)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	}()

	ctx, endRun := StartRunSpan(context.Background(), "run-err", "session-err", "test")
	defer endRun()

	wantErr := errors.New("boom")
	tool, err := llm.NewTool("failing_tool", "desc", func(context.Context, *struct{}) (string, error) {
		return "", wantErr
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	tool.Use(NewToolTraceMiddleware())
	if _, err := tool.Handle(ctx, `{}`); !errors.Is(err, wantErr) {
		t.Fatalf("tool.Handle err = %v want %v", err, wantErr)
	}

	var foundTool bool
	for _, sp := range sr.Ended() {
		if sp.Name() != "tool.failing_tool" {
			continue
		}
		foundTool = true
		var hasErr bool
		for _, attr := range sp.Attributes() {
			if string(attr.Key) == "forebrain.error" && attr.Value.AsString() == "boom" {
				hasErr = true
				break
			}
		}
		if !hasErr {
			t.Fatal("tool error span missing forebrain.error attribute")
		}
	}
	if !foundTool {
		t.Fatal("failing tool span not found")
	}
}

func TestToolTraceMiddlewareTruncatesLargeResultPreview(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider()
	tp.RegisterSpanProcessor(sr)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	}()

	ctx, endRun := StartRunSpan(context.Background(), "run-big", "session-big", "test")
	defer endRun()

	large := strings.Repeat("x", toolResultPreviewMaxRunes+50)
	tool, err := llm.NewTool("large_tool", "desc", func(context.Context, *struct{}) (string, error) {
		return large, nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	tool.Use(NewToolTraceMiddleware())
	if _, err := tool.Handle(ctx, `{}`); err != nil {
		t.Fatalf("tool.Handle err=%v", err)
	}

	for _, sp := range sr.Ended() {
		if sp.Name() != "tool.large_tool" {
			continue
		}
		var gotPreview string
		var gotTruncated bool
		for _, attr := range sp.Attributes() {
			switch string(attr.Key) {
			case "forebrain.tool.result_preview":
				gotPreview = attr.Value.AsString()
			case "forebrain.tool.result_truncated":
				gotTruncated = attr.Value.AsBool()
			}
		}
		if !gotTruncated {
			t.Fatal("expected truncated preview flag")
		}
		if !strings.HasSuffix(gotPreview, "…") {
			t.Fatalf("expected ellipsis suffix, got %q", gotPreview)
		}
		if len([]rune(gotPreview)) != toolResultPreviewMaxRunes+1 {
			t.Fatalf("unexpected preview rune length = %d", len([]rune(gotPreview)))
		}
		return
	}
	t.Fatal("large tool span not found")
}
