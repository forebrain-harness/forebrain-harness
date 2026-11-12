package telemetry

import "testing"

func TestPayloadCaptureEnabled_Default(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "")
	if !payloadCaptureEnabled() {
		t.Error("expected default to be true when env var is unset")
	}
}

func TestPayloadCaptureEnabled_ExplicitTrue(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "1")
	if !payloadCaptureEnabled() {
		t.Error("expected true for '1'")
	}
}

func TestPayloadCaptureEnabled_ExplicitFalse(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")
	if payloadCaptureEnabled() {
		t.Error("expected false for '0'")
	}
}

func TestPayloadCaptureEnabled_FalseString(t *testing.T) {
	for _, v := range []string{"false", "False", "FALSE", "no", "No", "off", "Off"} {
		t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", v)
		if payloadCaptureEnabled() {
			t.Errorf("expected false for %q", v)
		}
	}
}

func TestPayloadCaptureEnabled_TrueStrings(t *testing.T) {
	for _, v := range []string{"true", "True", "TRUE", "yes", "Yes", "on", "On", "1"} {
		t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", v)
		if !payloadCaptureEnabled() {
			t.Errorf("expected true for %q", v)
		}
	}
}

func TestPayloadCaptureEnabled_WhitespaceTrimmed(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "  false  ")
	if payloadCaptureEnabled() {
		t.Error("expected whitespace-trimmed 'false' to return false")
	}
}

func TestPayloadCaptureEnabled_ArbitraryValue(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "anything")
	if !payloadCaptureEnabled() {
		t.Error("expected arbitrary non-falsy value to return true")
	}
}

// LogProviderDebug records one provider note: a DEBUG record whose message is
// the topic and whose detail attribute is the message (plan 285).
func TestLogProviderDebugRecordsATopic(t *testing.T) {
	root := withEventLog(t)
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "1")
	LogProviderDebug("responses.preview", "content part 2 of 3")

	rec := onlyRecord(t, root)
	if rec.Level != "DEBUG" || rec.Kind != "log" || rec.Msg != "responses.preview" {
		t.Fatalf("record = %+v, want a DEBUG log record named for the topic", rec)
	}
	if got := rec.Attrs["detail"]; got != "content part 2 of 3" {
		t.Fatalf("detail = %v", got)
	}
}

// The provider switch still decides whether the note is recorded at all.
func TestLogProviderDebugHonorsTheDebugSwitch(t *testing.T) {
	root := withEventLog(t)
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")
	LogProviderDebug("responses.preview", "not recorded")
	if records := recordsRead(t, root); len(records) != 0 {
		t.Fatalf("records = %+v, want none while the switch is off", records)
	}
}
