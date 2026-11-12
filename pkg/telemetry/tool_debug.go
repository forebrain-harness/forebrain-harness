package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// kindTool is the record kind of a tool call, told apart from kindLLM, kindHTTP
// (the access log) and kindLog.
const kindTool = "tool"

// NewToolEventMiddleware records one tool call as two records sharing a call id:
// the model's own arguments before the handler runs, and the result after it
// (plans/286).
//
// The id is what pairs them. A tool result can quote the arguments, and a model
// may write anything at all into them, so matching on text — which is what the
// debug.log blocks did — pairs the two halves wrongly exactly when the call is
// interesting.
func NewToolEventMiddleware() llm.ToolMiddleware {
	return func(tool *llm.Tool, next llm.ToolHandler) llm.ToolHandler {
		return func(ctx context.Context, arguments string) (any, error) {
			log := DefaultEventLog()
			if log == nil {
				return next(ctx, arguments) // no log: nothing to say, nothing to compute
			}
			name := ""
			if tool != nil {
				name = tool.Name()
			}
			id := nextCallID(log, "t", &toolCallSeq)
			started := time.Now()
			emitToolArguments(ctx, log, id, name, arguments)
			out, err := next(ctx, arguments)
			emitToolResult(ctx, log, id, name, time.Since(started), out, err)
			return out, err
		}
	}
}

// emitToolArguments records what the model asked the tool to do. The arguments
// are the model's output, so they are kept whole: up to a record attribute's
// own limit inline, and past it the whole text in the call's own file with a
// preview left in the record.
func emitToolArguments(ctx context.Context, log *EventLog, id, name, arguments string) {
	attrs := []slog.Attr{
		slog.String("tool", name),
		slog.String("tool_call", id),
		slog.Int64("args_bytes", int64(len(arguments))),
	}
	if len(arguments) <= payloadInlineMaxBytes {
		attrs = append(attrs, slog.String("args", arguments))
	} else {
		attrs = append(attrs, slog.String("args_preview", previewText(arguments, payloadPreviewBytes)))
		if ref, err := writeToolPayload(log, id+".args.gz", []byte(arguments), true, 0); err != nil {
			attrs = append(attrs, slog.String(attrBlobError, err.Error()))
		} else {
			attrs = append(attrs, slog.Any(blobAttrPrefix+"args", ref))
		}
	}
	Emit(ctx, slog.LevelDebug, kindTool, "tool call", attrs...)
}

// emitToolResult records what the tool gave back. A result is not model output,
// so it is bounded once (DefaultRecordBytes, as debug.log always did): inline up
// to a record attribute's limit, and past it in the call's own file with the
// record saying how much was left out.
//
// A call whose handler failed records the error instead of a result: there is
// no result, and "<nil>" is not one.
func emitToolResult(ctx context.Context, log *EventLog, id, name string, dur time.Duration, out any, handlerErr error) {
	level := slog.LevelInfo
	attrs := []slog.Attr{
		slog.String("tool", name),
		slog.String("tool_call", id),
		slog.Int64("dur_ms", dur.Milliseconds()),
	}
	switch {
	case handlerErr != nil:
		level = slog.LevelWarn
		attrs = append(attrs, slog.Any(attrErr, handlerErr))
	default:
		full := formatToolResultValue(out)
		bounded := BoundText(full, DefaultRecordBytes)
		attrs = append(attrs, slog.Int64("result_bytes", int64(len(full))))
		if len(bounded) <= payloadInlineMaxBytes {
			attrs = append(attrs, slog.String("result", bounded))
		} else {
			omitted := int64(len(full) - DefaultRecordBytes)
			if omitted < 0 {
				omitted = 0
			}
			attrs = append(attrs, slog.String("result_preview", previewText(bounded, payloadPreviewBytes)))
			if ref, err := writeToolPayload(log, id+".result.gz", []byte(bounded), omitted == 0, omitted); err != nil {
				attrs = append(attrs, slog.String(attrBlobError, err.Error()))
			} else {
				attrs = append(attrs, slog.Any(blobAttrPrefix+"result", ref))
			}
		}
	}
	Emit(ctx, level, kindTool, "tool result", attrs...)
}

func formatToolResultValue(v any) string {
	if v == nil {
		return "<nil>"
	}
	if s, ok := v.(string); ok {
		return s
	}
	if parts, ok := v.([]llm.ContentPart); ok {
		ser := serializeContentParts(parts)
		raw, _ := json.MarshalIndent(ser, "", "  ")
		return string(raw)
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return string(raw)
}

func serializeContentParts(parts []llm.ContentPart) []map[string]any {
	out := make([]map[string]any, 0, len(parts))
	for _, p := range parts {
		m := map[string]any{"type": string(p.Type)}
		switch p.Type {
		case llm.ContentTypeText:
			m["text"] = p.Text
		case llm.ContentTypeImageURL:
			m["image_url"] = p.ImageURL
		case llm.ContentTypeImageBase64:
			m["mime_type"] = p.MIMEType
			m["image_base64"] = p.ImageBase64
		case llm.ContentTypeFileBase64:
			// A file part is recorded by what it was and how big, never by
			// what was in it: this log is read far more often than the file.
			for key, value := range llm.FilePartSummary(p) {
				m[key] = value
			}
		}
		out = append(out, m)
	}
	return out
}
