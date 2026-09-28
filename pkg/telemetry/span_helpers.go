package telemetry

import (
	"context"
	"fmt"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
)

var (
	toolTraceSeq atomic.Uint64
	httpTraceSeq atomic.Uint64
)

type spanStep struct {
	recorder *Recorder
	stepID   string
}

func (s spanStep) End(errText string, attrs ...attribute.KeyValue) {
	if s.recorder == nil || s.stepID == "" {
		return
	}
	s.recorder.OnStepResultWithAttrs(s.stepID, errText, attrs...)
}

func startToolTraceStep(ctx context.Context, toolName string, attrs ...attribute.KeyValue) (context.Context, spanStep) {
	recorder := RecorderFromContext(ctx)
	if recorder == nil {
		return ctx, spanStep{}
	}
	stepID := fmt.Sprintf("tool-%d", toolTraceSeq.Add(1))
	ctx = recorder.OnStepStartWithAttrs(ctx, stepID, toolName, attrs...)
	return ctx, spanStep{recorder: recorder, stepID: stepID}
}

func startHTTPTraceStep(ctx context.Context, attrs ...attribute.KeyValue) (context.Context, spanStep) {
	recorder := RecorderFromContext(ctx)
	if recorder == nil {
		return ctx, spanStep{}
	}
	stepID := fmt.Sprintf("http-%d", httpTraceSeq.Add(1))
	ctx = recorder.OnStepStartWithAttrs(ctx, stepID, "llm_http", attrs...)
	return ctx, spanStep{recorder: recorder, stepID: stepID}
}
