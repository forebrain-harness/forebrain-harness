package telemetry

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	homepkg "github.com/forebrain-harness/forebrain-harness/pkg/home"
)

const SetupLogFile = "info.log"

func SetupEvent(home string, event string, fields map[string]any) {
	home = strings.TrimSpace(home)
	if home == "" {
		return
	}
	dir := filepath.Join(home, homepkg.LogsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, SetupLogFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	var b strings.Builder
	b.WriteString(time.Now().Format(time.RFC3339Nano))
	b.WriteString(" event=")
	b.WriteString(sanitizeToken(event))
	for key, value := range fields {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		b.WriteByte(' ')
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(fmt.Sprintf("%q", sanitizeValue(key, value)))
	}
	b.WriteByte('\n')
	_, _ = f.WriteString(b.String())
	_ = f.Sync()
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
