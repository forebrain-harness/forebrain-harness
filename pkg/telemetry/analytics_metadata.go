package telemetry

import "strings"

func CounterMetadata(counter string) Metadata {
	return Metadata{"counter": strings.TrimSpace(counter)}
}
