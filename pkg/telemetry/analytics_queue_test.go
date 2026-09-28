package telemetry

import "testing"

type recordingSink struct {
	events []string
	meta   []Metadata
}

func (s *recordingSink) LogEvent(name string, metadata Metadata) {
	s.events = append(s.events, name)
	s.meta = append(s.meta, metadata)
}

func TestQueueDrainsWhenSinkAttachesOnce(t *testing.T) {
	ResetAnalyticsForTesting()
	LogEvent("before_attach", Metadata{"ok": true})
	s := &recordingSink{}
	AttachSink(s)
	AttachSink(&recordingSink{})
	if len(s.events) != 1 || s.events[0] != "before_attach" {
		t.Fatalf("queued event drain mismatch: %+v", s.events)
	}
}

func TestStripProtoFieldsCopiesOnlyWhenNeeded(t *testing.T) {
	m := Metadata{"a": 1, "_PROTO_name": "pii"}
	got := StripProtoFields(m)
	if _, ok := got["_PROTO_name"]; ok {
		t.Fatalf("proto field leaked: %+v", got)
	}
	if m["a"] != got["a"] {
		t.Fatalf("ordinary metadata changed")
	}

	plain := Metadata{"a": 1}
	if got := StripProtoFields(plain); got["a"] != 1 {
		t.Fatalf("plain metadata changed: %+v", got)
	}
}

func TestSinkReceivesStrippedMetadata(t *testing.T) {
	ResetAnalyticsForTesting()
	s := &recordingSink{}
	AttachSink(s)
	LogEvent("tool", Metadata{"tool": "read_file", "_PROTO_path": "/secret"})
	if len(s.meta) != 1 {
		t.Fatalf("metadata events=%d want 1", len(s.meta))
	}
	if _, ok := s.meta[0]["_PROTO_path"]; ok {
		t.Fatalf("proto metadata leaked to sink: %+v", s.meta[0])
	}
}
