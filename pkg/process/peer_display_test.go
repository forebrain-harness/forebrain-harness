package process

import (
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/peer"
)

// TestPeerEnvelopeDisplay mirrors TestParsePeerEnvelope for the reader a
// surface draws through (plan 246 §3.1): the same answers for an envelope this
// build parses, and a body and sender for one that names an attribute it has
// never heard of, which the strict reader still refuses.
func TestPeerEnvelopeDisplay(t *testing.T) {
	envelope := peer.Build(peer.Envelope{From: "uds:/tmp/a.sock", FromName: "builder", Body: "please review line 12"})
	name, body, ok := PeerEnvelopeDisplay(envelope)
	if !ok {
		t.Fatalf("PeerEnvelopeDisplay(%q) reported false", envelope)
	}
	if name != "builder" || body != "please review line 12" {
		t.Fatalf("PeerEnvelopeDisplay = %q, %q; want builder, please review line 12", name, body)
	}
	if _, _, ok := PeerEnvelopeDisplay("\n  " + envelope + "  \n"); !ok {
		t.Fatal("PeerEnvelopeDisplay rejected an envelope the row had padded")
	}
	if _, _, ok := PeerEnvelopeDisplay("just a message"); ok {
		t.Fatal("PeerEnvelopeDisplay accepted plain text")
	}
	if name, _, ok := PeerEnvelopeDisplay(peer.Build(peer.Envelope{Body: "no name"})); !ok || name != "" {
		t.Fatalf("envelope without from-name = %q, %v; want empty name and true", name, ok)
	}

	// A newer build's attribute: the screen still reads the letter, while the
	// model-facing read keeps treating it as plain text.
	unknown := `<cross-session-message from="uds:/tmp/a.sock" from-name="builder" from-lease="l-42">` +
		"\nplease review line 12\n</cross-session-message>"
	if name, body, ok := PeerEnvelopeDisplay(unknown); !ok || name != "builder" || body != "please review line 12" {
		t.Fatalf("PeerEnvelopeDisplay of an unknown attribute = (%q, %q, %v)", name, body, ok)
	}
	if _, _, ok := ParsePeerEnvelope(unknown); ok {
		t.Fatal("ParsePeerEnvelope accepted an envelope carrying an unknown attribute")
	}

	// The shape this process actually stores for that message: envelopeFor
	// wraps what it could not authenticate in an envelope of its own, escaping
	// the closing tags inside. Both elements are transport; the words are
	// inside both.
	wrapped := peer.Build(peer.Envelope{
		From: "uds:/tmp/sender.sock",
		Body: peer.EscapeBody(unknown),
	})
	if name, body, ok := PeerEnvelopeDisplay(wrapped); !ok || name != "builder" || body != "please review line 12" {
		t.Fatalf("PeerEnvelopeDisplay of the receiver's own wrapper = (%q, %q, %v)", name, body, ok)
	}
	if got := peerPreview(wrapped); strings.Contains(got, "cross-session-message") {
		t.Fatalf("peerPreview of the receiver's own wrapper kept the markup: %q", got)
	}
}

// TestPeerEnvelopeBodyUnwrapsAnEnvelopeThisBuildCannotParse pins the held
// banner's own body: the words the person is being asked about, never the
// transport markup, whichever build wrote the envelope.
func TestPeerEnvelopeBodyUnwrapsAnEnvelopeThisBuildCannotParse(t *testing.T) {
	unknown := `<cross-session-message from="uds:/tmp/a.sock" from-name="builder" from-lease="l-42">` +
		"\nthe question\n</cross-session-message>"
	if got := peerEnvelopeBody(unknown); got != "the question" {
		t.Fatalf("peerEnvelopeBody = %q, want the envelope body", got)
	}
	if got := peerEnvelopeBody("just a message"); got != "" {
		t.Fatalf("peerEnvelopeBody of plain text = %q, want empty", got)
	}
}

// TestPeerPreviewShowsTheEnvelopeBody pins the held row's opening: it quotes
// the letter, not the element it arrived in, and still drops the control
// characters a sender could otherwise draw with.
func TestPeerPreviewShowsTheEnvelopeBody(t *testing.T) {
	unknown := `<cross-session-message from="uds:/tmp/a.sock" from-name="builder" from-lease="l-42">` +
		"\nfirst\x1b[31m line\n</cross-session-message>"
	preview := peerPreview(unknown)
	if strings.Contains(preview, "cross-session-message") || strings.Contains(preview, "from-lease") {
		t.Fatalf("peerPreview kept the transport markup: %q", preview)
	}
	if !strings.Contains(preview, "first") || strings.Contains(preview, "\x1b") {
		t.Fatalf("peerPreview = %q, want the letter with no control characters", preview)
	}

	long := `<cross-session-message from-name="builder">` + "\n" + strings.Repeat("字", peerHeldPreviewRunes+5) +
		"\n</cross-session-message>"
	runes := []rune(peerPreview(long))
	if len(runes) != peerHeldPreviewRunes+1 || !strings.HasSuffix(string(runes), "…") {
		t.Fatalf("peerPreview of a long letter = %d runes, want %d plus an ellipsis",
			len(runes), peerHeldPreviewRunes+1)
	}

	// Text that is not an envelope is shown as it arrived, as it always was.
	if got := peerPreview("just a message"); got != "just a message" {
		t.Fatalf("peerPreview of plain text = %q", got)
	}
}
