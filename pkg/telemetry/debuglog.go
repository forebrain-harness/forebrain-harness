package telemetry

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// payloadCaptureEnabled reports whether model and tool payloads are kept:
// FOREBRAIN_LLM_HTTP_DEBUG=0/false/no/off turns the payload files off.
//
// It is the switch for payloads only. The records themselves are written
// either way — who called, which model, the status, the outcome and the timings
// are what makes a call diagnosable, and losing them because someone turned the
// bodies off would be the old debug.log switch taking the metadata with it
// (plan 286).
func payloadCaptureEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("FOREBRAIN_LLM_HTTP_DEBUG")))
	if v == "0" || v == "false" || v == "no" || v == "off" {
		return false
	}
	return true
}

// LogProviderDebug records one provider debug line under topic. It replaces a
// per-call-site helper (LogOpenAIResponsesContentPreview) so that provider
// clients, which may not import this package, can emit debug output through
// llm's port instead.
//
// It writes a record rather than a block in debug.log: the model and tool
// exchanges that log kept in full are 286's subject, and this is a provider's
// note about one call (plan 285).
func LogProviderDebug(topic, message string) {
	if !payloadCaptureEnabled() {
		return
	}
	Emit(context.Background(), slog.LevelDebug, "log", topic, slog.String("detail", message))
}
