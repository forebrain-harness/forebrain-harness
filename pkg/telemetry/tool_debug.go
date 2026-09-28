package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func NewToolDebugFileLogMiddleware() llm.ToolMiddleware {
	return func(tool *llm.Tool, next llm.ToolHandler) llm.ToolHandler {
		return func(ctx context.Context, arguments string) (any, error) {
			name := ""
			if tool != nil {
				name = tool.Name()
			}
			ts := time.Now().Format(time.RFC3339Nano)
			var b strings.Builder
			fmt.Fprintf(&b, "tool_call ts=%s\n", ts)
			fmt.Fprintf(&b, "tool_name=%s\n", name)
			fmt.Fprintf(&b, "arguments_raw:\n%s\n", BoundText(arguments, DefaultRecordBytes))
			writeDebugFileLog("tool_call", b.String())
			out, err := next(ctx, arguments)
			var rb strings.Builder
			fmt.Fprintf(&rb, "tool_result ts=%s\n", time.Now().Format(time.RFC3339Nano))
			fmt.Fprintf(&rb, "tool_name=%s\n", name)
			if err != nil {
				fmt.Fprintf(&rb, "handler_error=%q\n", err.Error())
			} else {
				rb.WriteString("result_value:\n")
				resStr := BoundText(formatToolResultValue(out), DefaultRecordBytes)
				rb.WriteString(resStr)
				if resStr != "" && !strings.HasSuffix(resStr, "\n") {
					rb.WriteByte('\n')
				}
			}
			writeDebugFileLog("tool_result", rb.String())
			return out, err
		}
	}
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
		}
		out = append(out, m)
	}
	return out
}
