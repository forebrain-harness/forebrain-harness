package telemetry

import (
	"context"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"go.opentelemetry.io/otel/attribute"
)

const toolResultPreviewMaxRunes = 4096

func NewToolTraceMiddleware() llm.ToolMiddleware {
	return func(tool *llm.Tool, next llm.ToolHandler) llm.ToolHandler {
		return func(ctx context.Context, arguments string) (any, error) {
			toolName := ""
			if tool != nil {
				toolName = strings.TrimSpace(tool.Name())
			}
			if toolName == "" {
				toolName = "unknown"
			}
			ctx, step := startToolTraceStep(ctx, toolName,
				attribute.String("forebrain.tool.arguments", arguments),
			)
			out, err := next(ctx, arguments)
			if err != nil {
				step.End(err.Error())
			} else {
				preview, truncated := toolResultPreview(out, toolResultPreviewMaxRunes)
				step.End("",
					attribute.String("forebrain.tool.result_preview", preview),
					attribute.Bool("forebrain.tool.result_truncated", truncated),
				)
			}
			return out, err
		}
	}
}

func toolResultPreview(v any, maxRunes int) (string, bool) {
	if maxRunes <= 0 {
		return "", false
	}
	full := formatToolResultValue(v)
	runes := []rune(full)
	if len(runes) <= maxRunes {
		return full, false
	}
	return string(runes[:maxRunes]) + "…", true
}
