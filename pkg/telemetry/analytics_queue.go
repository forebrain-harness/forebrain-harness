package telemetry

import (
	"strings"
	"sync"
)

const maxQueuedAnalyticsEvents = 1024

type Metadata map[string]any

type Sink interface {
	LogEvent(name string, metadata Metadata)
}

type queuedEvent struct {
	name     string
	metadata Metadata
}

var analyticsState = struct {
	mu     sync.Mutex
	sinkMu sync.Mutex
	sink   Sink
	queue  []queuedEvent
}{}

func AttachSink(s Sink) {
	if s == nil {
		return
	}
	analyticsState.mu.Lock()
	if analyticsState.sink != nil {
		analyticsState.mu.Unlock()
		return
	}
	analyticsState.sink = s
	drained := append([]queuedEvent(nil), analyticsState.queue...)
	analyticsState.queue = nil
	analyticsState.mu.Unlock()

	for _, e := range drained {
		logToSink(s, e.name, StripProtoFields(e.metadata))
	}
}

func LogEvent(name string, metadata Metadata) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	metadata = StripProtoFields(metadata)

	analyticsState.mu.Lock()
	s := analyticsState.sink
	if s == nil {
		if len(analyticsState.queue) >= maxQueuedAnalyticsEvents {
			copy(analyticsState.queue, analyticsState.queue[1:])
			analyticsState.queue[len(analyticsState.queue)-1] = queuedEvent{name: name, metadata: metadata}
		} else {
			analyticsState.queue = append(analyticsState.queue, queuedEvent{name: name, metadata: metadata})
		}
		analyticsState.mu.Unlock()
		return
	}
	analyticsState.mu.Unlock()

	logToSink(s, name, metadata)
}

func logToSink(s Sink, name string, metadata Metadata) {
	if s == nil {
		return
	}
	analyticsState.sinkMu.Lock()
	defer analyticsState.sinkMu.Unlock()
	s.LogEvent(name, metadata)
}

func StripProtoFields(metadata Metadata) Metadata {
	if len(metadata) == 0 {
		return metadata
	}
	var out Metadata
	for k := range metadata {
		if strings.HasPrefix(k, "_PROTO_") {
			if out == nil {
				out = cloneMetadata(metadata)
			}
			delete(out, k)
		}
	}
	if out == nil {
		return metadata
	}
	return out
}

func cloneMetadata(m Metadata) Metadata {
	out := make(Metadata, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func ResetAnalyticsForTesting() {
	analyticsState.mu.Lock()
	defer analyticsState.mu.Unlock()
	analyticsState.sink = nil
	analyticsState.queue = nil
}
