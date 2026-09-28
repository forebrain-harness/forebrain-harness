package session

import (
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// NewID returns a new session identifier for an explicit source.
func NewID(source string) string { return state.NewID(source) }

// NewForSurface preserves the stable source mapping used by existing clients.
func NewForSurface(surface, channel string) string {
	return state.NewForSurface(surface, channel)
}
