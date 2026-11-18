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
	// inside both. The inner element names a sender other than the one the
	// wrapper vouched for, so its from-name is not adopted and no surface
	// shows a name for this letter (plan 249's single-name rule).
	wrapped := peer.Build(peer.Envelope{
		From: "uds:/tmp/sender.sock",
		Body: peer.EscapeBody(unknown),
	})
	if name, body, ok := PeerEnvelopeDisplay(wrapped); !ok || name != "" || body != "please review line 12" {
		t.Fatalf("PeerEnvelopeDisplay of the receiver's own wrapper = (%q, %q, %v)", name, body, ok)
	}
	// The same wrapper carrying the wrapper's own sender inside: the one
	// inner from-name the single-name rule adopts, so the card gets a name.
	sameSender := peer.Build(peer.Envelope{
		From: "uds:/tmp/a.sock",
		Body: peer.EscapeBody(unknown),
	})
	if name, body, ok := PeerEnvelopeDisplay(sameSender); !ok || name != "builder" || body != "please review line 12" {
		t.Fatalf("PeerEnvelopeDisplay of the same-sender wrapper = (%q, %q, %v)", name, body, ok)
	}
	if got := peerPreview(wrapped); strings.Contains(got, "cross-session-message") {
		t.Fatalf("peerPreview of the receiver's own wrapper kept the markup: %q", got)
	}

	// The in-process interjection (pkg/process's own subagentReportEnvelope
	// and pkg/run's peerMessageText): the screen reads the roster key as the
	// name and the report as the words, while the model-facing read keeps
	// seeing the element verbatim.
	interjection := `<agent-message from="subagent-0f4c9f2e-1b2a-4c3d-8e5f-6a7b8c9d0e1f">` +
		"\nYour subagent finished successfully.\n\nIts final report:\n\nprobe report\n</agent-message>"
	if name, body, ok := PeerEnvelopeDisplay(interjection); !ok ||
		name != "subagent-0f4c9f2e-1b2a-4c3d-8e5f-6a7b8c9d0e1f" ||
		body != "Your subagent finished successfully.\n\nIts final report:\n\nprobe report" {
		t.Fatalf("PeerEnvelopeDisplay of the interjection = (%q, %q, %v)", name, body, ok)
	}
	if _, _, ok := ParsePeerEnvelope(interjection); ok {
		t.Fatal("ParsePeerEnvelope accepted the in-process interjection")
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

	// A held letter whose body is itself the in-process interjection,
	// re-wrapped by envelopeFor: the preview and the body quote the report's
	// own words, and no name is adopted for a sender the wrapper did not
	// vouch for (plan 249's single-name rule).
	held := peer.Build(peer.Envelope{
		From: "uds:/tmp/sender.sock",
		Body: `<agent-message from="subagent-x">first line of the report</agent-message>`,
	})
	if got := peerPreview(held); strings.Contains(got, "agent-message") {
		t.Fatalf("peerPreview of the wrapped interjection kept the markup: %q", got)
	}
	if got := peerEnvelopeBody(held); got != "first line of the report" {
		t.Fatalf("peerEnvelopeBody of the wrapped interjection = %q", got)
	}
	if got := peerDisplayName(held); got != "" {
		t.Fatalf("peerDisplayName of the wrapped interjection = %q, want empty", got)
	}
}
