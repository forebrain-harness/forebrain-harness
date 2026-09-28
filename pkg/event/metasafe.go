package event

import (
	"encoding/json"
	"strings"
)

var reservedPublicPrefixes = []string{
	"_PROTO_",
	"_SECRET_",
	"_INTERNAL_",
	"forebrain_internal_",
}

func SanitizeForPublic(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return sanitizeMap(x)
	case []any:
		out := make([]any, 0, len(x))
		for _, item := range x {
			out = append(out, SanitizeForPublic(item))
		}
		return out
	case json.RawMessage:
		return sanitizeRaw(x)
	default:
		return v
	}
}

func SanitizeRawJSON(raw json.RawMessage) json.RawMessage {
	return sanitizeRaw(raw)
}

func sanitizeRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return raw
	}
	sanitized := SanitizeForPublic(decoded)
	b, _ := json.Marshal(sanitized)
	return json.RawMessage(b)
}

func sanitizeMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if hasReservedPrefix(k) {
			continue
		}
		out[k] = SanitizeForPublic(v)
	}
	return out
}

func hasReservedPrefix(key string) bool {
	for _, prefix := range reservedPublicPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}
