package telemetry

import "regexp"

var bearerLike = regexp.MustCompile(`(?i)\bBearer\s+\S+`)

func RedactLogLine(s string) string {
	s = bearerLike.ReplaceAllString(s, "Bearer [REDACTED]")
	return s
}
