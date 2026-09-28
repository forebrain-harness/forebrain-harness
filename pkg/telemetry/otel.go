package telemetry

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type ShutdownFunc func(ctx context.Context) error

func InitFromEnv(_ string) (ShutdownFunc, error) {
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(strings.TrimPrefix(strings.TrimPrefix(endpoint, "http://"), "https://")),
	}
	if strings.HasPrefix(endpoint, "http://") || strings.Contains(endpoint, "127.0.0.1") || strings.Contains(endpoint, "localhost") {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	exp, err := otlptracehttp.New(context.Background(), opts...)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(nil),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

type Recorder struct {
	tracer trace.Tracer
	runID  string

	mu    sync.Mutex
	spans map[string]trace.Span
}

func NewRecorder(runID string) *Recorder {
	return &Recorder{
		tracer: otel.Tracer("forebrain"),
		runID:  runID,
		spans:  make(map[string]trace.Span),
	}
}

func (r *Recorder) OnStepStart(ctx context.Context, stepID, toolName string) context.Context {
	return r.OnStepStartWithAttrs(ctx, stepID, toolName)
}

func (r *Recorder) OnStepStartWithAttrs(ctx context.Context, stepID, toolName string, attrs ...attribute.KeyValue) context.Context {
	if r == nil {
		return ctx
	}
	if stepID == "" {
		stepID = fmt.Sprintf("step-%d", time.Now().UnixNano())
	}
	baseAttrs := []attribute.KeyValue{
		attribute.String("forebrain.run_id", r.runID),
		attribute.String("forebrain.step_id", stepID),
		attribute.String("forebrain.tool", toolName),
	}
	baseAttrs = append(baseAttrs, attrs...)
	ctx2, sp := r.tracer.Start(ctx, "tool."+toolName,
		trace.WithAttributes(baseAttrs...),
	)
	r.mu.Lock()
	r.spans[stepID] = sp
	r.mu.Unlock()
	return ctx2
}

func (r *Recorder) OnStepResult(stepID string, errText string) {
	r.OnStepResultWithAttrs(stepID, errText)
}

func (r *Recorder) OnStepResultWithAttrs(stepID string, errText string, attrs ...attribute.KeyValue) {
	if r == nil || stepID == "" {
		return
	}
	r.mu.Lock()
	sp := r.spans[stepID]
	if sp != nil {
		delete(r.spans, stepID)
	}
	r.mu.Unlock()
	if sp == nil {
		return
	}
	if len(attrs) > 0 {
		sp.SetAttributes(attrs...)
	}
	if strings.TrimSpace(errText) != "" {
		sp.SetAttributes(attribute.Bool("error", true), attribute.String("forebrain.error", errText))
	}
	sp.End()
}

type recorderContextKey struct{}

func ContextWithRecorder(ctx context.Context, recorder *Recorder) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, recorderContextKey{}, recorder)
}

func RecorderFromContext(ctx context.Context) *Recorder {
	if ctx == nil {
		return nil
	}
	rec, _ := ctx.Value(recorderContextKey{}).(*Recorder)
	return rec
}

func StartRunSpan(ctx context.Context, runID, sessionID, trigger string) (context.Context, func()) {
	if strings.TrimSpace(runID) == "" {
		return ctx, func() {}
	}
	tr := otel.Tracer("forebrain")
	ctx2, sp := tr.Start(ctx, "agent.run",
		trace.WithAttributes(
			attribute.String("forebrain.run_id", runID),
			attribute.String("forebrain.session_id", strings.TrimSpace(sessionID)),
			attribute.String("forebrain.trigger", strings.TrimSpace(trigger)),
		),
	)
	ctx2 = ContextWithRecorder(ctx2, NewRecorder(strings.TrimSpace(runID)))
	return ctx2, func() { sp.End() }
}
