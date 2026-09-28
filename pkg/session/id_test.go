package session

import (
	"strings"
	"testing"
)

func TestNewIDCarriesItsSource(t *testing.T) {
	id := NewID("chat")
	if id == "" {
		t.Fatal("NewID returned an empty id")
	}
	if !strings.HasPrefix(id, "chat-") {
		t.Fatalf("id = %q, want it to carry the chat source", id)
	}
	if NewID("chat") == id {
		t.Fatal("two ids from the same source must differ")
	}
}

func TestNewForSurfaceMapsSurfaceAndChannel(t *testing.T) {
	if got := NewForSurface("webchat", ""); !strings.HasPrefix(got, "web-") {
		t.Fatalf("webchat id = %q", got)
	}
	if got := NewForSurface("webchat", "slack"); got == "" {
		t.Fatalf("channel-scoped id = %q", got)
	}
}
