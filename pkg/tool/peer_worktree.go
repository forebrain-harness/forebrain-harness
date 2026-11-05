// The same-tree guards' port: what the file tools and the shell tool ask about
// the other agents working in this directory tree (plan 198).
//
// The declarations live here rather than beside the peer tools in pkg/run
// because both layers need them and this one is lower: pkg/run's PeerDirectory
// embeds this interface, the tools' runtime carries it as a field, and the
// composition root's one implementation satisfies both structurally. Nothing in
// this file knows how the answers are produced — no peer protocol, no process
// package, no filesystem of its own.
package tool

import (
	"context"
	"time"
)

// PeerWorkTree is the tools' port onto the other agents working in a directory
// tree. A nil port means this machine has no peer coordination at all, and every
// guard that asks it simply does nothing.
type PeerWorkTree interface {
	// WorkTreePeers lists the other live agents whose directory overlaps root
	// (equal, ancestor or descendant), with the files they edited recently.
	WorkTreePeers(ctx context.Context, root string) []WorkTreePeer
	// NoteWrite records that the calling agent's file tool wrote absPath at at.
	NoteWrite(ctx context.Context, absPath string, at time.Time)
	// TouchesBetween returns other agents' file-tool writes under root in
	// (from, to].
	TouchesBetween(ctx context.Context, root string, from, to time.Time) []PeerTouch
}

// WorkTreePeer is one other agent in this directory tree, as the guards describe
// it: who it is, what it is doing, and what it changed lately.
type WorkTreePeer struct {
	Name   string
	Kind   string
	Status string
	Task   string
	// RecentFiles are the paths it wrote in the last half hour, root-relative,
	// newest first, at most eight; MoreFiles is how many that list leaves out.
	RecentFiles []string
	MoreFiles   int
}

// PeerTouch is one write by another agent: the path it wrote, root-relative,
// who wrote it, and when.
type PeerTouch struct {
	Path string
	// AgentName is the name the other agent answers to, and Task is what it is
	// working on — empty when the agent did not say.
	AgentName string
	Task      string
	At        time.Time
}

// peersOf is the runtime's peer port, nil-safe the way codeIntelOf is: a
// runtime built by hand in a test has no port and the guards stay quiet.
func peersOf(rt *AgentToolRuntime) PeerWorkTree {
	if rt == nil {
		return nil
	}
	return rt.Peers
}

// peerAttributionLimit is how many files a failed command's attribution lists:
// enough to explain a test failure, few enough to stay one screen.
const peerAttributionLimit = 20

// peerChangesDuringRun names the files other agents wrote while a command ran,
// newest first, for the result of a command that failed (plan 198 §3.6). The
// window starts two minutes before the command did, because a write that landed
// just before it is exactly what makes a build or a test fail on files this
// agent never touched.
func peerChangesDuringRun(ctx context.Context, peers PeerWorkTree, root string, startedAt, finishedAt time.Time) []map[string]any {
	if peers == nil || root == "" {
		return nil
	}
	touches := peers.TouchesBetween(ctx, root, startedAt.Add(-2*time.Minute), finishedAt)
	if len(touches) == 0 {
		return nil
	}
	if len(touches) > peerAttributionLimit {
		touches = touches[:peerAttributionLimit]
	}
	changed := make([]map[string]any, 0, len(touches))
	for _, touch := range touches {
		minutes := int(finishedAt.Sub(touch.At).Minutes())
		if minutes < 1 {
			minutes = 1
		}
		changed = append(changed, map[string]any{
			"path":        touch.Path,
			"agent":       touch.AgentName,
			"minutes_ago": minutes,
		})
	}
	return changed
}
