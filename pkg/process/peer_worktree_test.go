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
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

// The peer view may not depend on this process's own registration. When the
// peer service never started — the socket directory is somebody else's, a
// transient failure, a full disk — every caller of WorkTreePeers still has to
// see a neighbour working in this tree (plan 237 §1).
func TestPeerWorkTreeAnswersWhenTheServiceNeverStarted(t *testing.T) {
	env := peerTestEnvFor(t, "tui", func() string { return "cli-guard-down" })
	// StartPeers is exactly what did not happen in this case: the port is
	// installed by hand and nothing is registered.
	env.Deps.Peers = env.peerDirectory()
	port, ok := env.Deps.Peers.(*peerOutbound)
	if !ok || port == nil {
		t.Fatal("this process has no port onto the other processes")
	}
	if env.peers != nil {
		t.Fatal("this case needs a process whose peer service never started")
	}
	dir := peer.NewLayout(false).Dir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A live process of this user in the same tree. Nothing listens on the
	// socket it names, so an answer that dials cannot reach it.
	rec := terminalPeer(filepath.Join(env.dirs.fbSock, "900271.sock"), env.LaunchDir, "editing",
		peer.ExtAgent{ID: "cli-editing", Name: "editing", Kind: "primary", Root: env.LaunchDir,
			Status: peer.StatusBusy, Task: "Rewriting the importer"})
	rec.PID = os.Getppid()
	rec.ProcStart, _ = peer.ProcStart(rec.PID)
	if err := peer.WriteRecord(dir, rec); err != nil {
		t.Fatal(err)
	}

	peers := env.Deps.Peers.WorkTreePeers(context.Background(), env.LaunchDir)
	if len(peers) != 1 || peers[0].Name != "editing" {
		t.Fatalf("peers = %#v, want the peer editing this tree", peers)
	}
	if peers[0].Status != peer.StatusBusy || peers[0].Task != "Rewriting the importer" {
		t.Fatalf("peer = %#v, want its status and task", peers[0])
	}
}

// The service is wired before its first registration (plan 240), so while a
// failed first attempt is being retried env.peers is set but nothing listens.
// That service must answer like no service: the tree's neighbours still come
// from the rendezvous directory (plan 237 §2), never from an empty listing.
func TestPeerWorkTreeAnswersWhileTheFirstRegistrationIsRetried(t *testing.T) {
	env := peerTestEnvFor(t, "tui", func() string { return "cli-guard-retry" })
	blocker := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocker, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FOREBRAIN_PEER_SOCKET_DIR", filepath.Join(blocker, "sock"))

	dir := peer.NewLayout(false).Dir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := terminalPeer(filepath.Join(env.dirs.fbSock, "900272.sock"), env.LaunchDir, "editing",
		peer.ExtAgent{ID: "cli-editing", Name: "editing", Kind: "primary", Root: env.LaunchDir,
			Status: peer.StatusBusy, Task: "Rewriting the importer"})
	rec.PID = os.Getppid()
	rec.ProcStart, _ = peer.ProcStart(rec.PID)
	if err := peer.WriteRecord(dir, rec); err != nil {
		t.Fatal(err)
	}

	stop := env.StartPeers(context.Background(), PeerSurface{Kind: "tui", CurrentSession: func() string { return "cli-guard-retry" }})
	defer stop()
	if env.peers == nil {
		t.Fatal("this case needs the service wired while its registration is retried")
	}
	if rec := ownRecord(t, env.dirs.fbDir); rec != nil {
		t.Fatalf("record = %+v, want the first attempt to have failed", rec)
	}
	peers := env.Deps.Peers.WorkTreePeers(context.Background(), env.LaunchDir)
	if len(peers) != 1 || peers[0].Name != "editing" {
		t.Fatalf("peers = %#v, want the neighbour from the rendezvous directory", peers)
	}
}

// The fallback reads the rendezvous directory and nothing else, so what it
// reports is what the directory says: a live neighbour in the tree is a peer,
// a record whose writer is gone is not, a peer in another tree is not, and this
// process's own record is not somebody else (plan 237 §2).
func TestPeerWorkTreeDirectoryFallbackNeedsNoRegistration(t *testing.T) {
	env := peerTestEnvFor(t, "tui", func() string { return "cli-guard-fallback" })
	port := env.outbound()
	if port == nil {
		t.Fatal("this process has no port onto the other processes")
	}
	if env.peers != nil {
		t.Fatal("this case needs a process whose peer service never started")
	}
	root := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := peer.NewLayout(false).Dir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	plant := func(rec peer.Record) {
		t.Helper()
		if err := peer.WriteRecord(dir, rec); err != nil {
			t.Fatal(err)
		}
	}
	agent := func(name, agentRoot string) peer.ExtAgent {
		return peer.ExtAgent{ID: "cli-" + name, Name: name, Kind: "primary", Root: agentRoot,
			Status: peer.StatusBusy, Task: "Editing " + name}
	}
	socket := filepath.Join(env.dirs.fbSock, "900272.sock")

	// A live process of this user in the same tree — and the files it wrote,
	// keyed the same way the registered path keys them.
	ledger := touchLedger(t)
	appendAgentTouch(ledger, agentTouch{
		path:    resolveTouchPath(filepath.Join(root, "a.go")),
		addr:    peer.EncodeUDS(socket),
		agentID: "cli-editing",
		name:    "editing",
	}, time.Now())
	live := terminalPeer(socket, root, "editing", agent("editing", root))
	live.PID = os.Getppid()
	live.ProcStart, _ = peer.ProcStart(live.PID)
	plant(live)
	peers := port.WorkTreePeers(context.Background(), root)
	if len(peers) != 1 || peers[0].Name != "editing" || peers[0].Kind != peerKindTerminal {
		t.Fatalf("peers = %#v, want the live neighbour", peers)
	}
	if len(peers[0].RecentFiles) != 1 || peers[0].RecentFiles[0] != "a.go" {
		t.Fatalf("recent = %#v, want the file the ledger recorded", peers[0].RecentFiles)
	}

	// A record left behind by a process that is gone: its pid belongs to a
	// different process now, whose start time is not the recorded one.
	dead := terminalPeer(socket, root, "gone", agent("gone", root))
	dead.PID = os.Getppid()
	dead.ProcStart = "Thu Jan  1 00:00:00 1970"
	plant(dead)
	if peers := port.WorkTreePeers(context.Background(), root); len(peers) != 0 {
		t.Fatalf("peers = %#v, want the dead record left out", peers)
	}

	// The same live process, working in another tree, is nobody's business here.
	elsewhere := terminalPeer(socket, t.TempDir(), "elsewhere", agent("elsewhere", t.TempDir()))
	elsewhere.PID = os.Getppid()
	elsewhere.ProcStart, _ = peer.ProcStart(elsewhere.PID)
	plant(elsewhere)
	if peers := port.WorkTreePeers(context.Background(), root); len(peers) != 0 {
		t.Fatalf("peers = %#v, want the peer in another tree left out", peers)
	}

	// This process's own record is not somebody else.
	own := terminalPeer(socket, root, "self", agent("self", root))
	own.PID = os.Getpid()
	own.ProcStart, _ = peer.ProcStart(own.PID)
	plant(own)
	if peers := port.WorkTreePeers(context.Background(), root); len(peers) != 0 {
		t.Fatalf("peers = %#v, want this process's own record left out", peers)
	}

	// A directory that cannot be read answers "nobody" rather than a panic.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if peers := port.WorkTreePeers(context.Background(), root); len(peers) != 0 {
		t.Fatalf("peers = %#v, want nobody from an unreadable directory", peers)
	}
}

// A service that stays unusable costs one warning, not one per command: the
// fallback answers every command, and the log says why once (plan 237 §4).
func TestPeerWorkTreeFallbackWarnsOnce(t *testing.T) {
	env := peerTestEnvFor(t, "tui", func() string { return "cli-guard-warn" })
	port := env.outbound()
	if port == nil {
		t.Fatal("this process has no port onto the other processes")
	}
	capture := &peerWarnCapture{}
	previous := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(previous) })

	root := t.TempDir()
	for i := 0; i < 3; i++ {
		if peers := port.WorkTreePeers(context.Background(), root); len(peers) != 0 {
			t.Fatalf("call %d: peers = %#v, want nobody", i, peers)
		}
	}
	warnings := capture.warnings()
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	if !strings.Contains(warnings[0], "err") {
		t.Fatalf("warning = %q, want the reason it fell back", warnings[0])
	}
}

// peerWarnCapture keeps the warnings this binary logged, as message plus the
// attribute keys, so a test can see both what was said and that the reason rode
// along with it.
type peerWarnCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *peerWarnCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *peerWarnCapture) Handle(_ context.Context, rec slog.Record) error {
	if rec.Level < slog.LevelWarn {
		return nil
	}
	keys := make([]string, 0, 4)
	rec.Attrs(func(attr slog.Attr) bool {
		keys = append(keys, attr.Key)
		return true
	})
	c.mu.Lock()
	c.lines = append(c.lines, rec.Message+" "+strings.Join(keys, ","))
	c.mu.Unlock()
	return nil
}

func (c *peerWarnCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *peerWarnCapture) WithGroup(string) slog.Handler      { return c }

func (c *peerWarnCapture) warnings() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
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

// nestedWorktreeRepo makes root a repository with a linked worktree at
// root/.worktrees/feature, where agents create them, and a submodule-shaped
// checkout at root/vendor/lib. It returns the worktree's resolved path.
func nestedWorktreeRepo(t *testing.T, root string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	linked := filepath.Join(root, ".worktrees", "feature")
	git("worktree", "add", "-q", "--detach", linked, "HEAD")
	submodule := filepath.Join(root, "vendor", "lib")
	for _, dir := range []string{filepath.Join(root, ".git", "modules", "lib"), submodule} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(submodule, ".git"), []byte("gitdir: ../../.git/modules/lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return resolvePath(linked)
}

// TestSameWorkTreeStopsAtALinkedWorktree pins the tree boundary (owner
// 2026-11-09): path containment, except that a linked worktree checked out
// below another agent's directory is a tree of its own, in both directions. A
// submodule stays part of the checkout that contains it.
func TestSameWorkTreeStopsAtALinkedWorktree(t *testing.T) {
	root := resolvePath(t.TempDir())
	linked := nestedWorktreeRepo(t, root)
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{root, root, true},
		{root, filepath.Join(root, "pkg"), true},
		{root, filepath.Join(root, "vendor", "lib"), true},
		{root, linked, false},
		{linked, root, false},
		{filepath.Join(root, "pkg"), filepath.Join(linked, "pkg"), false},
		{root, filepath.Join(linked, "pkg"), false},
		{linked, filepath.Join(linked, "pkg"), true},
		{filepath.Join(root, ".worktrees"), root, true},
		{root, t.TempDir(), false},
	} {
		if got := sameWorkTree(tc.a, tc.b); got != tc.want {
			t.Errorf("sameWorkTree(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestPeerTouchesLeaveOutANestedWorktree checks the ledger readers apply the
// same boundary: another agent's write inside root/.worktrees/feature is not
// news to an agent in root — not for failure attribution, not for the web
// panel — and is news to an agent in that worktree.
func TestPeerTouchesLeaveOutANestedWorktree(t *testing.T) {
	dir := touchLedger(t)
	f := startPeerOutbound(t, "cli-touch-nested", nil)
	ctx := touchContext("cli-touch-nested")
	root := resolvePath(f.env.LaunchDir)
	linked := nestedWorktreeRepo(t, root)
	now := time.Now()
	writeForeignTouch(t, dir, filepath.Join(root, "main.go"), "cli-other", "in-main", now)
	writeForeignTouch(t, dir, filepath.Join(linked, "main.go"), "cli-other", "in-worktree", now)

	window := func(tree string) []string {
		var names []string
		for _, touch := range f.port.TouchesBetween(ctx, tree, now.Add(-time.Minute), now.Add(time.Minute)) {
			names = append(names, touch.AgentName+":"+touch.Path)
		}
		return names
	}
	if got := window(root); len(got) != 1 || got[0] != "in-main:main.go" {
		t.Errorf("touches in the main checkout = %v, want only the write there", got)
	}
	if got := window(linked); len(got) != 1 || got[0] != "in-worktree:main.go" {
		t.Errorf("touches in the worktree = %v, want only the write there", got)
	}
	panel := f.env.RecentPeerFileTouches(root)
	if len(panel) != 1 || panel[0].AgentName != "in-main" {
		t.Errorf("panel for the main checkout = %#v, want only the write there", panel)
	}
}
