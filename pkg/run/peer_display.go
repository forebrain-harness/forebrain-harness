package run

// PeerDisplay is the port that reads a peer message for a surface to draw:
// the sender's name, the words inside the envelope, and the identity block
// (mark and tone) the engine derives from that name. The composition root
// implements it — the one display read and the one mark derivation live in
// pkg/process, which this package may not import — and hands it to the runner
// as Deps.PeerDisplay, the way the peer tools' directory arrives as Deps.Peers.
// Nil means no derivation is available: a delivered peer message then carries
// its origin and text alone, and the surface draws the letter with no sender
// identity rather than deriving one itself.
type PeerDisplay interface {
	// Describe reads content as one peer envelope. ok is false for text that
	// is not one element, and the other answers are zero then; a nameless
	// envelope still reports true, with an empty fromName.
	Describe(content string) (fromName, body, mark string, tone int, ok bool)
}
