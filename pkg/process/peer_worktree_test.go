// The identity ledger's contract (plan 198 §4 step 2): what a write records, who
// reads it back, whose writes are left out, and which peers count as being in
// the same directory tree.
//
// Every case points the ledger override at its own temporary directory, so
// nothing here can append to or read the developer's real ledger. Somebody
// else's write is written with appendAgentTouch and an address no peer of this
// process answers on, which is exactly what a line from a second process looks
// like.
package process

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/peer"
)

// touchLedger installs the ledger override for one case and returns its
// directory.
func touchLedger(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(agentTouchDirEnv, dir)
	return dir
}

// touchContext is a file tool's context for one conversation.
func touchContext(session string) context.Context {
	return llm.WithAgentSessionID(context.Background(), session)
}

// foreignAddress is another process's address: no peer of this one answers on
// it, so a line written from it is another agent's write.
func foreignAddress() string {
	return peer.EncodeUDS(filepath.Join("/tmp", "fbp-other", "900.sock"))
}

// writeForeignTouch records one other agent's write in the ledger.
func writeForeignTouch(t *testing.T, dir, path, agentID, name string, at time.Time) {
	t.Helper()
	appendAgentTouch(dir, agentTouch{
		path: resolveTouchPath(path), addr: foreignAddress(), agentID: agentID, name: name,
	}, at)
}

// This agent's own write is not news to itself: the tool already knows it wrote
// the file. Another agent's write to the same path is.
func TestPeerTouchesExcludeOwnWrites(t *testing.T) {
	dir := touchLedger(t)
	f := startPeerOutbound(t, "cli-touch-self", nil)
	ctx := touchContext("cli-touch-self")
	root := f.env.LaunchDir
	own := filepath.Join(root, "a.go")
	if err := os.WriteFile(own, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	f.port.NoteWrite(ctx, own, now)
	if got := f.port.TouchesBetween(ctx, root, now.Add(-time.Minute), now.Add(time.Minute)); len(got) != 0 {
		t.Fatalf("touches = %#v, want this agent's own write left out", got)
	}

	writeForeignTouch(t, dir, own, "cli-other", "elsewhere", now)
	got := f.port.TouchesBetween(ctx, root, now.Add(-time.Minute), now.Add(time.Minute))
	if len(got) != 1 {
		t.Fatalf("touches = %#v, want the other agent's write", got)
	}
	if got[0].Path != "a.go" || got[0].AgentName != "elsewhere" {
		t.Fatalf("touch = %#v, want the root-relative path and the writer's name", got[0])
	}
	if !got[0].At.Equal(now) {
		t.Fatalf("touch at = %v, want %v", got[0].At, now)
	}
}

// A registration mode switch moves the socket, and with it the address. Writes
// this process made from the address it no longer answers on are still its own.
func TestPeerTouchesExcludeWritesFromAnAddressThisProcessUsed(t *testing.T) {
	dir := touchLedger(t)
	f := startPeerOutbound(t, "cli-touch-moved", nil)
	ctx := touchContext("cli-touch-moved")
	root := f.env.LaunchDir
	previous := peer.EncodeUDS(filepath.Join("/tmp", "fbp-previous", "1.sock"))
	svc := f.env.peers
	if svc == nil {
		t.Fatal("this process is not registered as a peer")
	}
	svc.mu.Lock()
	svc.ownAddresses[previous] = true
	svc.mu.Unlock()

	now := time.Now()
	for i, addr := range []string{f.addr, previous} {
		appendAgentTouch(dir, agentTouch{
			path:    resolveTouchPath(filepath.Join(root, "b"+string(rune('0'+i))+".go")),
			addr:    addr,
			agentID: "cli-touch-moved",
			name:    f.name,
		}, now)
	}
	if got := f.port.TouchesBetween(ctx, root, now.Add(-time.Minute), now.Add(time.Minute)); len(got) != 0 {
		t.Fatalf("touches = %#v, want both of this agent's addresses left out", got)
	}

	// The same agent id from an address this process never answered on is
	// another process, and its write is news.
	appendAgentTouch(dir, agentTouch{
		path: resolveTouchPath(filepath.Join(root, "c.go")), addr: foreignAddress(),
		agentID: "cli-touch-moved", name: "elsewhere",
	}, now)
	got := f.port.TouchesBetween(ctx, root, now.Add(-time.Minute), now.Add(time.Minute))
	if len(got) != 1 || got[0].Path != "c.go" {
		t.Fatalf("touches = %#v, want the foreign process's write only", got)
	}
}

// What falls outside a window, outside the tree, or outside the line format is
// not evidence of anything.
func TestPeerTouchesIgnoreOldOutsideAndBrokenLines(t *testing.T) {
	dir := touchLedger(t)
	f := startPeerOutbound(t, "cli-touch-window", nil)
	ctx := touchContext("cli-touch-window")
	root := f.env.LaunchDir
	now := time.Now()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}

	writeForeignTouch(t, dir, filepath.Join(root, "pkg", "fresh.go"), "cli-other", "elsewhere", now)
	writeForeignTouch(t, dir, filepath.Join(root, "pkg", "stale.go"), "cli-other", "elsewhere", now.Add(-45*time.Minute))
	writeForeignTouch(t, dir, filepath.Join(t.TempDir(), "outside.go"), "cli-other", "elsewhere", now)
	// Lines an older or a newer writer might leave: a torn append, a line with
	// the wrong column count, and a path that no longer parses.
	// The garbage goes into an older shard: the shard of this minute holds the
	// fresh line above, and truncating it would delete the very write under test.
	if err := os.WriteFile(filepath.Join(dir, agentTouchShardName(now.Add(-5*time.Minute).Unix()/60)), []byte(
		"not-a-line\n"+
			"\t\t\n"+
			"12345\t\"unterminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := f.port.TouchesBetween(ctx, root, now.Add(-agentTouchRetention), now.Add(time.Minute))
	if len(got) != 1 || got[0].Path != "pkg/fresh.go" {
		t.Fatalf("touches = %#v, want only the fresh write inside the tree", got)
	}
}

// A process with no registration writes nothing at all: a line it could not
// attribute would come back to it as somebody else's.
func TestPeerNoteWriteNeedsARegistration(t *testing.T) {
	dir := touchLedger(t)
	port := &peerOutbound{env: &Environment{}}
	port.NoteWrite(touchContext("cli-touch-unregistered"), filepath.Join(t.TempDir(), "a.go"), time.Now())
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("ledger holds %d entries, want none", len(entries))
	}
}

// Being in the same directory tree is what makes a peer somebody to coordinate
// with: the same directory, one inside the other, or one reached through a link.
// A peer in an unrelated tree is not.
func TestPeerWorkTreePeersListOnlyTheAgentsInThisTree(t *testing.T) {
	ledger := touchLedger(t)
	f := startPeerOutbound(t, "cli-work-tree", nil)
	ctx := touchContext("cli-work-tree")
	tree := t.TempDir()
	root := filepath.Join(tree, "proj")
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tree, "linked-proj")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	remote := startPeerRemote(t, f.env.dirs.fbSock, "900410")
	writePeerRecord(t, f.env, f.dir, terminalPeer(remote.socket, root, "same-tree",
		peer.ExtAgent{ID: "peer-same", Name: "same-tree", Kind: "primary", Root: root,
			Status: peer.StatusBusy, Task: "Refactoring the importer"},
		peer.ExtAgent{ID: "peer-child", Name: "same-tree.child", Kind: "subagent", Parent: "peer-same",
			Root: filepath.Join(root, "pkg"), Status: peer.StatusBusy},
		peer.ExtAgent{ID: "peer-parent", Name: "same-tree.parent", Kind: "primary", Root: tree, Status: peer.StatusIdle},
		peer.ExtAgent{ID: "peer-linked", Name: "same-tree.linked", Kind: "primary", Root: link, Status: peer.StatusIdle},
		peer.ExtAgent{ID: "peer-elsewhere", Name: "elsewhere", Kind: "primary", Root: t.TempDir(), Status: peer.StatusIdle},
	))
	now := time.Now()
	address := peer.EncodeUDS(remote.socket)
	for i := 0; i < 10; i++ {
		appendAgentTouch(ledger, agentTouch{
			path:    resolveTouchPath(filepath.Join(root, "pkg", "f"+string(rune('0'+i))+".go")),
			addr:    address,
			agentID: "peer-same",
			name:    "same-tree",
		}, now.Add(-time.Duration(i)*time.Second))
	}

	peers := f.port.WorkTreePeers(ctx, root)
	names := make([]string, 0, len(peers))
	for _, p := range peers {
		names = append(names, p.Name)
	}
	want := []string{"same-tree", "same-tree.child", "same-tree.parent", "same-tree.linked"}
	if len(names) != len(want) {
		t.Fatalf("peers = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("peers = %v, want %v", names, want)
		}
	}
	primary := peers[0]
	if primary.Kind != peerKindTerminal || primary.Status != peer.StatusBusy || primary.Task != "Refactoring the importer" {
		t.Fatalf("primary = %#v, want its kind, status and task", primary)
	}
	if len(primary.RecentFiles) != peerWorkTreeRecentFiles || primary.MoreFiles != 2 {
		t.Fatalf("recent = %d files, %d more; want %d and 2",
			len(primary.RecentFiles), primary.MoreFiles, peerWorkTreeRecentFiles)
	}
	// Newest first: the first write above is the most recent.
	if primary.RecentFiles[0] != "pkg/f0.go" {
		t.Fatalf("recent = %v, want the newest write first", primary.RecentFiles)
	}
	for _, p := range peers[1:] {
		if len(p.RecentFiles) != 0 || p.MoreFiles != 0 {
			t.Fatalf("%s reports %v/%d, want nothing: the ledger never saw it", p.Name, p.RecentFiles, p.MoreFiles)
		}
	}
}

