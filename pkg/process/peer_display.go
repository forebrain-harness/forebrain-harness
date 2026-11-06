package process

import "github.com/forebrain-harness/forebrain-harness/pkg/peer"

// PeerEnvelopeDisplay reads a peer message for a surface to draw: the sender's
// name and the words inside the envelope, or false for text that is not one
// element (plan 246 §3.1). It is the counterpart of ParsePeerEnvelope, which
// stays the model-facing read: a surface never imports pkg/peer, and the one
// display rule lives behind this wrapper the way the one parse rule does.
func PeerEnvelopeDisplay(content string) (fromName, body string, ok bool) {
	return peer.DisplayText(content)
}
