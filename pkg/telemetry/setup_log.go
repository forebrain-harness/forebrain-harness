package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// SetupEvent records one setup step: what happened and the fields it reported,
// with the same redaction a token or a secret needed before.
func SetupEvent(home string, event string, fields map[string]any) {
	// The home is no longer where this goes — the event log is — but the
	// parameter stays because every call site passes it and none of them
	// should change an argument for a change of destination (plan 285).
	_ = home
	keys := make([]string, 0, len(fields))
	for key := range fields {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	// Sorted, because a record whose attributes move around from one run to
	// the next cannot be compared or read in a fixed order: the map iteration
	// this replaces was a bug, not a style choice.
	sort.Strings(keys)
	attrs := make([]slog.Attr, 0, len(keys))
	for _, key := range keys {
		attrs = append(attrs, slog.String(strings.TrimSpace(key), sanitizeValue(key, fields[key])))
	}
	Emit(context.Background(), slog.LevelInfo, "setup", sanitizeToken(event), attrs...)
}

func sanitizeValue(key string, value any) string {
	s := fmt.Sprint(value)
	lower := strings.ToLower(strings.TrimSpace(key))
	if strings.HasSuffix(lower, "_set") || strings.HasSuffix(lower, "_present") || strings.HasPrefix(lower, "has_") {
		return s
	}
	if strings.Contains(lower, "key") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") {
		if strings.TrimSpace(s) == "" {
			return ""
		}
		return "[REDACTED]"
	}
	return RedactLogLine(s)
}

func sanitizeToken(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '_' || r == '-' || r == '.':
			return r
		default:
			return '_'
		}
	}, v)
}
