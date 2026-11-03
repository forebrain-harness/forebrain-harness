// Sending's contract (plan 197 §4 step 2): who is reachable and how a listing
// describes them, how a name resolves to one of them, what a send puts on the
// wire, the pacing that keeps two agents from ping-ponging, what becomes of a
// receipt, and which directory the whole thing reads in each interop mode.
//
// Every case points all four directory overrides at its own temporary
// directories (peerTestDirsFor, from peers_test.go), so nothing here can read
// or write the user's real entries. The other processes in these cases are
// records plus sockets the test owns: a real second peer would be a second
// process, and every process in a test binary shares one pid — which the
// registry keys records by — so the reachable neighbour is written as a live
// record of a pid that is not ours.
package process

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/peer"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// peerOutboundFixture is one running registration plus this process's port onto
// the other processes' agents.
type peerOutboundFixture struct {
	env     *peerTestEnv
	view    *peerTestView
	port    *peerOutbound
	socket  string // the socket this process listens on
	addr    string // the address its peers answer to
	name    string // the name its record advertises
	dir     string // the rendezvous directory in use
	session string
	// notes counts the delivery updates already handed to the surface, so a
	// case that expects two waits for the second one rather than re-reading
	// the first.
	notes int
}

// startPeerOutbound brings up a terminal registration and the port. configure
// runs before the service starts, so a conversation, a session store or the
// interop switch can be in place from the first byte.
//
// The tick is slowed to an hour: these cases write other peers' records beside
// this process's own, and a tick that rewrote our record mid-case would change
// what the listing was being asked about. Nothing here needs the tick.
func startPeerOutbound(t *testing.T, session string, configure func(*peerTestEnv)) *peerOutboundFixture {
	t.Helper()
	env := peerTestEnvFor(t, "tui", func() string { return session })
	peerTickInterval = time.Hour
	if configure != nil {
		configure(env)
	}
	view := &peerTestView{}
	stop := env.StartPeers(context.Background(), view.surface("tui", func() string { return session }))
	t.Cleanup(stop)
	waitFor(t, "the peer registration", func() bool {
		return ownRecord(t, env.dirs.fbDir) != nil || ownRecord(t, env.dirs.ccDir) != nil
	})
	dir := env.dirs.fbDir
	if ownRecord(t, env.dirs.ccDir) != nil {
		dir = env.dirs.ccDir
	}
	rec := ownRecord(t, dir)
	port := env.outbound()
	if port == nil {
		t.Fatal("this process has no port onto the other processes")
	}
	return &peerOutboundFixture{
		env: env, view: view, port: port, socket: rec.MessagingSocketPath,
		addr: peer.EncodeUDS(rec.MessagingSocketPath), name: rec.Name, dir: dir, session: session,
	}
}

// caller is peer_send's view of who is speaking.
func (f *peerOutboundFixture) caller() run.PeerCaller {
	return run.PeerCaller{SessionID: f.session}
}

// peerRemote is another process's agent as this one can reach it: a socket that
// takes what is sent to it, and the frames it received.
type peerRemote struct {
	socket string
	frames chan peer.Frame
}

// peerRemoteHandler keeps the user frames a remote peer received.
type peerRemoteHandler struct {
	frames chan peer.Frame
}

func (h *peerRemoteHandler) HandleFrame(_ context.Context, in peer.Inbound) {
	if in.Frame.Type != peer.FrameTypeUser {
		return
	}
	select {
	case h.frames <- in.Frame:
	default:
	}
}

// startPeerRemote listens where a peer of this machine would.
func startPeerRemote(t *testing.T, socketDir, file string) *peerRemote {
	t.Helper()
	handler := &peerRemoteHandler{frames: make(chan peer.Frame, 64)}
	server := &peer.Server{Path: filepath.Join(socketDir, file+".sock"), Token: peer.NewToken(), Handler: handler}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return &peerRemote{socket: server.Path, frames: handler.frames}
}

// frame waits for the next user frame this remote received.
func (r *peerRemote) frame(t *testing.T, what string) peer.Frame {
	t.Helper()
	select {
	case frame := <-r.frames:
		return frame
	case <-time.After(4 * time.Second):
		t.Fatalf("%s: nothing arrived", what)
	}
	return peer.Frame{}
}

// writePeerRecord registers one more process in the rendezvous directory. Its
// pid is not this process's: a record keyed by our own pid is this process's own
// registration, and nothing here may impersonate it.
func writePeerRecord(t *testing.T, env *peerTestEnv, dir string, rec peer.Record) {
	t.Helper()
	if rec.PID == 0 {
		rec.PID = os.Getppid()
	}
	rec.Kind = "interactive"
	rec.PeerFeatures = []string{"reply_across_default_dirs"}
	rec.StatusUpdatedAt = time.Now().UnixMilli()
	if err := peer.WriteRecord(dir, rec); err != nil {
		t.Fatal(err)
	}
	env.peers.resetDirCache()
}

// terminalPeer is a neighbour that hosts agents, the shape a forebrain process
// advertises.
func terminalPeer(socket, cwd, name string, agents ...peer.ExtAgent) peer.Record {
	return peer.Record{
		Cwd:                 cwd,
		Name:                name,
		MessagingSocketPath: socket,
		Status:              peer.StatusIdle,
		Forebrain:           &peer.Extension{V: 1, Surface: "tui", Agents: agents},
	}
}

// peerNames lists a listing's rows as "name [ref] kind" for assertions.
func peerNames(listing run.PeerListing) []string {
	out := make([]string, 0, len(listing.Peers))
	for _, entry := range listing.Peers {
		line := entry.Name
		if entry.Ref != "" {
			line += " [" + entry.Ref + "]"
		}
		out = append(out, line+" "+entry.Kind)
	}
	return out
}

// A listing describes the agents of another process — the same directory tree
// first — and a send reaches the one the name picks out, addressed to that
// agent rather than to its process.
func TestPeerOutboundListsAndMessagesTheAgentsOfAnotherProcess(t *testing.T) {
	f := startPeerOutbound(t, "cli-out-cross", nil)
	remote := startPeerRemote(t, f.env.dirs.fbSock, "900001")
	writePeerRecord(t, f.env, f.dir, terminalPeer(remote.socket, f.env.LaunchDir, "elsewhere-1",
		peer.ExtAgent{ID: "cli-far-1", Name: "elsewhere-1", Kind: "primary", Root: f.env.LaunchDir,
			Branch: "dev", Status: peer.StatusBusy, Task: "Refactoring the importer"},
		peer.ExtAgent{ID: "cli-far-1/task-watch", Name: "elsewhere-1.task-watch", Kind: "subagent",
			Parent: "cli-far-1", Root: t.TempDir(), Status: peer.StatusBusy, Task: "Watching the tests"},
	))

	listing, err := f.port.List(context.Background(), f.caller())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := peerNames(listing); len(got) != 2 || got[0] != "elsewhere-1 terminal" || got[1] != "elsewhere-1.task-watch subagent in another process" {
		t.Fatalf("listing = %v, want the same-tree primary first and its subagent next", got)
	}
	if !listing.Peers[0].SameTree || listing.Peers[1].SameTree {
		t.Fatalf("same_tree = %v/%v, want the primary's own tree only", listing.Peers[0].SameTree, listing.Peers[1].SameTree)
	}
	if listing.Peers[0].Task != "Refactoring the importer" || listing.Peers[0].Status != peer.StatusBusy {
		t.Fatalf("row = %#v, want the peer's own task and status", listing.Peers[0])
	}
	if listing.Peers[0].ActiveAgo <= 0 || listing.Peers[0].ActiveAgo > time.Minute {
		t.Fatalf("active = %v, want it just did something", listing.Peers[0].ActiveAgo)
	}

	result, err := f.port.Send(context.Background(), f.caller(), "elsewhere-1", "I am about to change pkg/run/runner.go")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result.Name != "elsewhere-1" || result.Status != peer.StatusBusy {
		t.Fatalf("result = %#v, want the recipient named with its own status", result)
	}
	frame := remote.frame(t, "the message")
	if frame.From != f.addr {
		t.Fatalf("frame from = %q, want this process's address %q", frame.From, f.addr)
	}
	if frame.Forebrain == nil || frame.Forebrain.To != "cli-far-1" || frame.Forebrain.From != "cli-out-cross" {
		t.Fatalf("extension = %+v, want the addressed agent and the sender's id", frame.Forebrain)
	}
	envelope, ok := peer.Parse(frame.Message.Content)
	if !ok {
		t.Fatalf("content %q is not an envelope", frame.Message.Content)
	}
	if envelope.From != f.addr || envelope.FromName != f.name || envelope.FromMode != "prompting" {
		t.Fatalf("envelope = %+v, want this process, its name and its mode", envelope)
	}
	if envelope.Body != "I am about to change pkg/run/runner.go" {
		t.Fatalf("body = %q, want the message", envelope.Body)
	}
}

// A message that answers one this process received carries the chain that
// brought it, extended with this process's own token.
func TestPeerSendInheritsTheChainOfTheMessageItAnswers(t *testing.T) {
	var store *state.SessionStore
	f := startPeerOutbound(t, "cli-out-chain", func(env *peerTestEnv) {
		store = openPeerSessions(t, env)
		seedPeerSession(t, store, "cli-out-chain", env.LaunchDir)
	})
	remote := startPeerRemote(t, f.env.dirs.fbSock, "900002")
	writePeerRecord(t, f.env, f.dir, terminalPeer(remote.socket, t.TempDir(), "elsewhere-2",
		peer.ExtAgent{ID: "cli-far-2", Name: "elsewhere-2", Kind: "primary", Root: t.TempDir(), Status: peer.StatusIdle},
	))

	inherited := "aaaaaaaaaaaaaaaaaaaaaaaa"
	arrival := llm.UserMessage(llm.Text(peer.Build(peer.Envelope{
		From: peer.EncodeUDS(remote.socket), FromName: "elsewhere-2",
		HopChain: []string{inherited}, Body: "can you take the importer?",
	})))
	arrival.Origin = state.MessageOriginPeer
	if err := store.AppendMessageSequence(context.Background(), "cli-out-chain", []llm.Message{arrival}, "test-model", ""); err != nil {
		t.Fatalf("append the arrival: %v", err)
	}

	if _, err := f.port.Send(context.Background(), f.caller(), "elsewhere-2", "yes, on it"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	frame := remote.frame(t, "the answer")
	envelope, ok := peer.Parse(frame.Message.Content)
	if !ok {
		t.Fatalf("content %q is not an envelope", frame.Message.Content)
	}
	if len(envelope.HopChain) != 2 || envelope.HopChain[0] != inherited {
		t.Fatalf("hop chain = %v, want the arrival's chain with this process's token appended", envelope.HopChain)
	}
	if envelope.HopChain[1] == "" || envelope.HopChain[1] == inherited {
		t.Fatalf("hop chain = %v, want a token of this process's own", envelope.HopChain)
	}
}

// Two agents can carry one name; the listing then prints a ref beside each, and
// the name alone is refused until it comes with its ref.
func TestPeerSendNeedsTheRefWhenTwoAgentsShareAName(t *testing.T) {
	f := startPeerOutbound(t, "cli-out-dup", nil)
	remote := startPeerRemote(t, f.env.dirs.fbSock, "900003")
	tree := t.TempDir()
	writePeerRecord(t, f.env, f.dir, terminalPeer(remote.socket, tree, "elsewhere-3",
		peer.ExtAgent{ID: "cli-far-3a", Name: "dup-name", Kind: "primary", Root: tree, Status: peer.StatusIdle},
		peer.ExtAgent{ID: "cli-far-3b", Name: "dup-name", Kind: "subagent", Parent: "cli-far-3a", Root: tree, Status: peer.StatusBusy},
	))

	listing, err := f.port.List(context.Background(), f.caller())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := peerNames(listing); len(got) != 2 || !strings.Contains(got[0], "[") || !strings.Contains(got[1], "[") {
		t.Fatalf("listing = %v, want a ref beside both rows", got)
	}

	_, err = f.port.Send(context.Background(), f.caller(), "dup-name", "which of you?")
	if err == nil || !strings.Contains(err.Error(), `"dup-name" matches several agents:`) ||
		!strings.Contains(err.Error(), "Use the name with its [ref].") {
		t.Fatalf("ambiguous send = %v, want the several-agents answer", err)
	}
	if len(remote.frames) != 0 {
		t.Fatalf("%d frames were sent for an ambiguous name", len(remote.frames))
	}

	printed := listing.Peers[1].Name + " [" + listing.Peers[1].Ref + "]"
	result, err := f.port.Send(context.Background(), f.caller(), printed, "are you the subagent?")
	if err != nil {
		t.Fatalf("Send with the ref: %v", err)
	}
	if result.Name != printed {
		t.Fatalf("result name = %q, want %q", result.Name, printed)
	}
	frame := remote.frame(t, "the message to the subagent")
	if frame.Forebrain == nil || frame.Forebrain.To != "cli-far-3b" {
		t.Fatalf("extension = %+v, want the second agent", frame.Forebrain)
	}
}

// A name nobody carries, and an address nobody listens on, are answered the way
// the protocol words them.
func TestPeerSendRefusesNamesAndAddressesNobodyHolds(t *testing.T) {
	f := startPeerOutbound(t, "cli-out-unknown", nil)

	_, err := f.port.Send(context.Background(), f.caller(), "nobody", "hello")
	if err == nil || err.Error() != `No agent named "nobody". Run peer_list to see who is reachable.` {
		t.Fatalf("unknown name = %v", err)
	}

	orphan := filepath.Join(f.env.dirs.fbSock, "900004.sock")
	_, err = f.port.Send(context.Background(), f.caller(), peer.EncodeUDS(orphan), "hello")
	if err == nil || err.Error() != "No agent at "+peer.EncodeUDS(orphan)+". Run peer_list to see who is reachable." {
		t.Fatalf("unknown address = %v", err)
	}
}

// One target has one budget: after a burst, the next message waits, so two
// agents cannot ping-pong the machine into a stall.
func TestPeerSendPacesOneTarget(t *testing.T) {
	f := startPeerOutbound(t, "cli-out-pace", nil)
	remote := startPeerRemote(t, f.env.dirs.fbSock, "900005")
	writePeerRecord(t, f.env, f.dir, terminalPeer(remote.socket, t.TempDir(), "elsewhere-5",
		peer.ExtAgent{ID: "cli-far-5", Name: "elsewhere-5", Kind: "primary", Root: t.TempDir(), Status: peer.StatusIdle},
	))

	for i := 0; i < int(peer.DefaultLimits.BucketCapacity); i++ {
		if _, err := f.port.Send(context.Background(), f.caller(), "elsewhere-5", "message"); err != nil {
			t.Fatalf("send %d of a burst: %v", i+1, err)
		}
	}
	_, err := f.port.Send(context.Background(), f.caller(), "elsewhere-5", "one too many")
	if err == nil || !strings.Contains(err.Error(), "Not sent: too many messages to elsewhere-5 in a short time.") {
		t.Fatalf("the message past the budget = %v, want the pacing refusal", err)
	}
}

// A record that names an agent whose socket is gone is a process that is no
// longer running, and that is what its sender is told.
func TestPeerSendReportsAProcessThatIsNoLongerRunning(t *testing.T) {
	f := startPeerOutbound(t, "cli-out-gone", nil)
	writePeerRecord(t, f.env, f.dir, terminalPeer("", t.TempDir(), "elsewhere-6",
		peer.ExtAgent{ID: "cli-far-6", Name: "elsewhere-gone", Kind: "primary", Root: t.TempDir(), Status: peer.StatusIdle},
	))

	_, err := f.port.Send(context.Background(), f.caller(), "elsewhere-gone", "are you there?")
	if err == nil || err.Error() != "elsewhere-gone is no longer running." {
		t.Fatalf("send to a gone process = %v", err)
	}
}

// A message the receiving side held is reported to the conversation that sent
// it, once, as a delivery update rather than as a message from anyone.
func TestPeerReceiptsReachTheConversationThatSent(t *testing.T) {
	previous := peerReceiptDebounce
	peerReceiptDebounce = 20 * time.Millisecond
	t.Cleanup(func() { peerReceiptDebounce = previous })

	f := startPeerOutbound(t, "cli-out-receipt", func(env *peerTestEnv) {
		store := openPeerSessions(t, env)
		seedPeerSession(t, store, "cli-out-receipt", env.LaunchDir)
	})
	remote := startPeerRemote(t, f.env.dirs.fbSock, "900007")
	writePeerRecord(t, f.env, f.dir, terminalPeer(remote.socket, t.TempDir(), "elsewhere-7",
		peer.ExtAgent{ID: "cli-far-7", Name: "elsewhere-7", Kind: "primary", Root: t.TempDir(), Status: peer.StatusIdle},
	))

	if _, err := f.port.Send(context.Background(), f.caller(), "elsewhere-7", "merge the branch?"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	frame := remote.frame(t, "the message")

	// The receiving side answers from the address it was reached at. Here that
	// is this process's own socket — the receipt's transport is 196's business,
	// and what this case is about is what the sender does with it.
	f.sendReceipt(t, peer.PeerMessageStatus{
		Status: peer.StatusHeld, Reason: "held: permission modes differ", OrigMsgID: frame.MsgID,
	})
	prompt := f.awaitNote(t, "the held receipt")
	if prompt.Origin != state.MessageOriginPeerStatus {
		t.Fatalf("origin = %q, want a delivery update", prompt.Origin)
	}
	if !strings.Contains(prompt.Text, "- to elsewhere-7: held by the receiving session;") {
		t.Fatalf("note = %q, want the held line", prompt.Text)
	}
	if !strings.HasPrefix(prompt.Text, "<system-reminder>\nPeer message delivery update:\n") ||
		!strings.HasSuffix(prompt.Text, "\n</system-reminder>") {
		t.Fatalf("note = %q, want the delivery-update framing", prompt.Text)
	}

	f.sendReceipt(t, peer.PeerMessageStatus{
		Status: peer.StatusDelivered, Reason: "released after approval", OrigMsgID: frame.MsgID,
	})
	prompt = f.awaitNote(t, "the delivered receipt")
	if !strings.Contains(prompt.Text, "- to elsewhere-7: released after approval and delivered.") {
		t.Fatalf("note = %q, want the delivered line", prompt.Text)
	}
}

// sendReceipt posts one receipt to this process's socket, the way a peer that
// received a message answers it.
func (f *peerOutboundFixture) sendReceipt(t *testing.T, status peer.PeerMessageStatus) {
	t.Helper()
	frame := status.Frame()
	frame.From = f.addr
	if err := peer.Send(context.Background(), f.socket, frame, peer.SendOptions{Timeout: 2 * time.Second}); err != nil {
		t.Fatalf("send receipt: %v", err)
	}
}

// awaitNote waits for the next delivery update the surface was asked to run.
func (f *peerOutboundFixture) awaitNote(t *testing.T, what string) PeerPrompt {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		f.view.mu.Lock()
		var notes []PeerPrompt
		for _, prompt := range f.view.wakes {
			if prompt.Origin == state.MessageOriginPeerStatus {
				notes = append(notes, prompt)
			}
		}
		f.view.mu.Unlock()
		if len(notes) > f.notes {
			f.notes++
			return notes[f.notes-1]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s: no delivery update reached the sending conversation", what)
	return PeerPrompt{}
}

// With the interop switch off, which is the default, only forebrain processes
// are reachable: another kind of agent's record is not a candidate, by name or
// by address.
func TestPeerOutboundIgnoresTheOtherDirectoryWithTheSwitchOff(t *testing.T) {
	f := startPeerOutbound(t, "cli-out-fb-only", func(env *peerTestEnv) {
		if err := os.MkdirAll(env.dirs.ccDir, 0o700); err != nil {
			t.Fatal(err)
		}
	})
	remote := startPeerRemote(t, f.env.dirs.ccSock, "900008")
	if err := peer.WriteRecord(f.env.dirs.ccDir, peer.Record{
		PID: os.Getppid(), Kind: "interactive", Cwd: t.TempDir(), Name: "other-agent", Status: peer.StatusBusy,
		MessagingSocketPath: remote.socket, PeerFeatures: []string{"reply_across_default_dirs"},
	}); err != nil {
		t.Fatal(err)
	}

	if listing, err := f.port.List(context.Background(), f.caller()); err != nil || len(listing.Peers) != 0 {
		t.Fatalf("listing = %v, %v, want nobody reachable in FB mode", peerNames(listing), err)
	}
	_, err := f.port.Send(context.Background(), f.caller(), "other-agent", "hello")
	if err == nil || err.Error() != `No agent named "other-agent". Run peer_list to see who is reachable.` {
		t.Fatalf("send by name = %v", err)
	}
	_, err = f.port.Send(context.Background(), f.caller(), peer.EncodeUDS(remote.socket), "hello")
	if err == nil || !strings.HasPrefix(err.Error(), "No agent at ") {
		t.Fatalf("send by address = %v", err)
	}
}

// With the interop switch on and its directory present, this process moves into
// that directory: the agents registered there are reachable as themselves, a
// forebrain process still in the other directory is not, and once it moves too
// the two see each other again.
func TestPeerOutboundFollowsTheInteropSwitch(t *testing.T) {
	f := startPeerOutbound(t, "cli-out-cc", func(env *peerTestEnv) {
		if err := os.MkdirAll(env.dirs.ccDir, 0o700); err != nil {
			t.Fatal(err)
		}
		env.setSwitch(true)
	})
	if f.dir != f.env.dirs.ccDir {
		t.Fatalf("registration directory = %q, want the interop directory", f.dir)
	}

	other := startPeerRemote(t, f.env.dirs.ccSock, "900009")
	if err := peer.WriteRecord(f.env.dirs.ccDir, peer.Record{
		PID: os.Getppid(), Kind: "interactive", Cwd: t.TempDir(), Name: "other-agent", Status: peer.StatusBusy,
		MessagingSocketPath: other.socket, PeerFeatures: []string{"reply_across_default_dirs"},
	}); err != nil {
		t.Fatal(err)
	}
	// A forebrain process that has not moved: reachable only from the directory
	// it registered in, which this process no longer reads.
	if err := os.MkdirAll(f.env.dirs.fbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fbRemote := startPeerRemote(t, f.env.dirs.fbSock, "900010")
	writePeerRecord(t, f.env, f.env.dirs.fbDir, terminalPeer(fbRemote.socket, t.TempDir(), "fb-neighbour",
		peer.ExtAgent{ID: "cli-fb-1", Name: "fb-neighbour", Kind: "primary", Root: t.TempDir(), Status: peer.StatusIdle},
	))

	listing, err := f.port.List(context.Background(), f.caller())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := peerNames(listing); len(got) != 1 || got[0] != "other-agent agent session" {
		t.Fatalf("listing = %v, want the interop directory's agent and nobody else", got)
	}
	if _, err := f.port.Send(context.Background(), f.caller(), "fb-neighbour", "hello"); err == nil ||
		!strings.Contains(err.Error(), `No agent named "fb-neighbour"`) {
		t.Fatalf("send to a forebrain process in the other directory = %v, want it unreachable", err)
	}

	if _, err := f.port.Send(context.Background(), f.caller(), "other-agent", "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	frame := other.frame(t, "the message to the interop peer")
	if frame.From != f.addr || !strings.HasPrefix(f.addr, peer.EncodeUDS(f.env.dirs.ccSock)) {
		t.Fatalf("frame from = %q, want this process's address in %s", frame.From, f.env.dirs.ccSock)
	}
	// The extension block carries who is speaking; its `to` is filled only for a
	// target that is a forebrain agent, and this one hosts none.
	if frame.Forebrain == nil || frame.Forebrain.To != "" || frame.Forebrain.From != "cli-out-cc" {
		t.Fatalf("extension = %+v, want the sender's id and no addressed agent", frame.Forebrain)
	}
	if envelope, ok := peer.Parse(frame.Message.Content); !ok || envelope.From != f.addr {
		t.Fatalf("envelope = %+v, want this process's address as the sender", envelope)
	}

	// The other forebrain process moves too, and the two are visible again. It
	// is a third pid: records are keyed by pid, so reusing the interop peer's
	// would replace it rather than add a process.
	moved := terminalPeer(fbRemote.socket, t.TempDir(), "fb-neighbour",
		peer.ExtAgent{ID: "cli-fb-1", Name: "fb-neighbour", Kind: "primary", Root: t.TempDir(), Status: peer.StatusIdle})
	moved.PID = 1 // every machine has pid 1, and it is never this process
	writePeerRecord(t, f.env, f.env.dirs.ccDir, moved)
	listing, err = f.port.List(context.Background(), f.caller())
	if err != nil {
		t.Fatalf("List after the move: %v", err)
	}
	if got := peerNames(listing); len(got) != 2 ||
		!containsString(got, "fb-neighbour terminal") || !containsString(got, "other-agent agent session") {
		t.Fatalf("listing = %v, want the moved neighbour back beside the interop peer", got)
	}
	if _, err := f.port.Send(context.Background(), f.caller(), "fb-neighbour", "hello again"); err != nil {
		t.Fatalf("Send after the move: %v", err)
	}
	if frame := fbRemote.frame(t, "the message after the move"); frame.From != f.addr {
		t.Fatalf("frame from = %q, want this process's interop address", frame.From)
	}
}

// TestParsePeerReceiptSummary pins the reading the web's delivery card makes of
// the note this process writes (plan 200 §3): one row per recipient, with the
// state the protocol reported and the reason in plain words. The round trip is
// the contract — a summary this package wrote must read back as the rows it was
// built from — and content in any other shape must yield nothing rather than a
// guess.
func TestParsePeerReceiptSummary(t *testing.T) {
	summary := peerReceiptSummary([]string{
		peerReceiptLine("forebrain-harness-28", peer.PeerMessageStatus{Status: peer.StatusHeld, Reason: "held"}),
		peerReceiptLine("forebrain-gw-3c1a", peer.PeerMessageStatus{Status: peer.StatusDelivered}),
		peerReceiptLine("forebrain-harness-a3", peer.PeerMessageStatus{Status: peer.StatusDenied}),
		peerReceiptLine("docs-site-7f", peer.PeerMessageStatus{Status: peer.StatusDropped, DropReason: "rate-limited"}),
	})
	rows := ParsePeerReceiptSummary(summary)
	if len(rows) != 4 {
		t.Fatalf("ParsePeerReceiptSummary returned %d rows, want 4: %+v", len(rows), rows)
	}
	want := []PeerReceiptRow{
		{Name: "forebrain-harness-28", Status: PeerReceiptHeld},
		{Name: "forebrain-gw-3c1a", Status: PeerReceiptDelivered},
		{Name: "forebrain-harness-a3", Status: PeerReceiptDenied},
		{Name: "docs-site-7f", Status: PeerReceiptDropped, Reason: "rate-limited"},
	}
	for i, row := range rows {
		if row.Name != want[i].Name || row.Status != want[i].Status || row.Reason != want[i].Reason {
			t.Fatalf("row %d = %+v, want %+v", i, row, want[i])
		}
	}
	if rows := ParsePeerReceiptSummary("not a delivery note"); len(rows) != 0 {
		t.Fatalf("ParsePeerReceiptSummary read rows out of unrelated content: %+v", rows)
	}
}
