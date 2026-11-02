// Workspace-scoped tools: turn diffs, and the note a write carries when another
// agent is working in the same file.
package tool

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

// peerTouchWindow is how far back a write hint looks: the same half hour the
// identity ledger keeps (plan 198 §3.4).
const peerTouchWindow = 30 * time.Minute

func attachTurnDiff(output map[string]any, absPath string, before, after []byte) {
	if output == nil {
		return
	}
	summary, err := event.Build(absPath, before, after)
	if err != nil {
		return
	}
	output["turn_diff"] = summary
	// One call == one tool just finished changing this path: the announcement
	// a concurrent shell card subtracts from its own window — in any forebrain
	// process of this user, not just this one (plan 186).
	noteToolFileWrite(absPath, time.Now())
}

// notePeerWrite records this call's write in the peer identity ledger and
// answers with the line the model's result carries when another agent edited the
// same path recently — empty when nobody did, or when this machine has no peer
// coordination at all.
//
// The write happens either way: the announcement is what lets the next agent's
// hint, and its own shell cards, know who changed the file. The note is written
// before the announcement so it reports what others did, not this call.
func notePeerWrite(ctx context.Context, peers PeerWorkTree, absPath string) string {
	if peers == nil {
		return ""
	}
	now := time.Now()
	note := ""
	if root, err := WorkingDir(ctx); err == nil && strings.TrimSpace(root) != "" {
		if touch, ok := recentPeerTouch(ctx, peers, root, absPath, now); ok {
			note = peerWriteNote(touch, now)
		}
	}
	peers.NoteWrite(ctx, absPath, now)
	return note
}

// notePeerFileWrite records one path in the peer identity ledger, when this
// machine has a peer port at all. A path a call announced but never showed as a
// model-facing result — the source of a move — is announced here.
func notePeerFileWrite(ctx context.Context, rt *AgentToolRuntime, absPath string) {
	if peers := peersOf(rt); peers != nil {
		peers.NoteWrite(ctx, absPath, time.Now())
	}
}

// recentPeerTouch is the newest write by another agent to absPath inside the
// hint's window.
func recentPeerTouch(ctx context.Context, peers PeerWorkTree, root, absPath string, now time.Time) (PeerTouch, bool) {
	touches := peers.TouchesBetween(ctx, root, now.Add(-peerTouchWindow), now)
	if len(touches) == 0 {
		return PeerTouch{}, false
	}
	target := filepath.Clean(absPath)
	if rel, err := filepath.Rel(root, absPath); err == nil {
		target = filepath.Clean(rel)
	}
	for _, touch := range touches {
		if filepath.Clean(filepath.FromSlash(touch.Path)) == target {
			return touch, true
		}
	}
	return PeerTouch{}, false
}

// peerWriteNote words the hint: who else touched this file, how long ago, and
// what they were doing. A peer that said nothing about its task gets no
// parenthetical — an empty one would read as a missing fact.
func peerWriteNote(touch PeerTouch, now time.Time) string {
	name := strings.TrimSpace(touch.AgentName)
	if name == "" {
		name = "another agent"
	}
	minutes := int(now.Sub(touch.At).Minutes())
	if minutes < 1 {
		minutes = 1
	}
	note := "Note: " + touch.Path + " was also edited " + strconv.Itoa(minutes) + " min ago by " + name
	if task := strings.TrimSpace(touch.Task); task != "" {
		note += " (task: " + task + ")"
	}
	return note + ". Coordinate with peer_send before changing it further."
}
