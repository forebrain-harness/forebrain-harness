package process

import (
	"github.com/forebrain-harness/forebrain-harness/pkg/peer"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
)

// PeerEnvelopeDisplay reads a peer message for a surface to draw: the sender's
// name and the words inside the envelope, or false for text that is not one
// element (plan 246 §3.1). It is the counterpart of ParsePeerEnvelope, which
// stays the model-facing read: a surface never imports pkg/peer, and the one
// display rule lives behind this wrapper the way the one parse rule does.
func PeerEnvelopeDisplay(content string) (fromName, body string, ok bool) {
	return peer.DisplayText(content)
}

// peerDisplayPort is the composition root's implementation of run.PeerDisplay:
// the one display read above and the one mark derivation beside it, composed
// so the run package can fill a delivered peer event without importing this
// one. It holds no state — the derivation is a pure read of the text.
type peerDisplayPort struct{}

func (peerDisplayPort) Describe(content string) (fromName, body, mark string, tone int, ok bool) {
	fromName, body, ok = PeerEnvelopeDisplay(content)
	if !ok {
		return "", "", "", 0, false
	}
	mark, tone = PeerMark(fromName, "")
	return fromName, body, mark, tone, true
}

// peerDisplay builds this process's display port. Unlike the tools' directory
// it needs no service: reading an envelope and hashing a name work wherever
// this package runs, so a letter is drawn with its sender even on a platform
// whose peer protocol is not implemented.
func (env *Environment) peerDisplay() run.PeerDisplay {
	return peerDisplayPort{}
}
