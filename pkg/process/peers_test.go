// The peer service's contract (plan 195 §4): the default FB registration, the
// source gate, the two modes and the switches between them, the CC side's
// hands-off rule, the placeholder inbox, the status the record carries, and
// the in-process helpers the extension block reads.
//
// Every case points all four directory overrides at its own temporary
// directories — a test binary without them would read and write the user's
// real entries — and both socket directories come from /tmp, whose sticky
// bit is what the socket directory vetting expects.
package process

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/peer"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
)

// peerTestDirs points the four overrides at fresh directories and returns
// them. The FB rendezvous directory and the CC rendezvous directory start
// absent — their creation is the service's (FB) or forbidden (CC).
type peerTestDirs struct {
	fbDir  string
	fbSock string
	ccDir  string
	ccSock string
}

func peerTestDirsFor(t *testing.T) peerTestDirs {
	t.Helper()
	base := t.TempDir()
	fbSock, err := os.MkdirTemp("/tmp", "fbp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(fbSock) })
	ccSock, err := os.MkdirTemp("/tmp", "fbp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(ccSock) })
	dirs := peerTestDirs{
		fbDir:  filepath.Join(base, "fb-sessions"),
		fbSock: fbSock,
		ccDir:  filepath.Join(base, "cc-sessions"),
		ccSock: ccSock,
	}
	t.Setenv("FOREBRAIN_PEERS_DIR", dirs.fbDir)
	t.Setenv("FOREBRAIN_PEER_SOCKET_DIR", dirs.fbSock)
	t.Setenv("FOREBRAIN_PEERS_CC_DIR", dirs.ccDir)
	t.Setenv("FOREBRAIN_PEER_CC_SOCKET_DIR", dirs.ccSock)
	return dirs
}

// peerTestEnv builds the minimal environment the service reads: a launch
// directory and a run controller. The fast tick and the counting detection
// wrapper are installed and restored per test.
type peerTestEnv struct {
	*Environment
	dirs        peerTestDirs
	detectCalls *int
}

func peerTestEnvFor(t *testing.T, kind string, session func() string) *peerTestEnv {
	t.Helper()
	dirs := peerTestDirsFor(t)
	previousTick := peerTickInterval
	peerTickInterval = 20 * time.Millisecond
	t.Cleanup(func() { peerTickInterval = previousTick })
	calls := 0
	previousDetect := detectClaudeCode
	detectClaudeCode = func() bool {
		calls++
		return previousDetect()
	}
	t.Cleanup(func() { detectClaudeCode = previousDetect })
	launch := t.TempDir()
	env := &Environment{Root: t.TempDir(), LaunchDir: launch, Control: run.NewController()}
	if kind == "gateway" {
		// The gateway's agent list reads the session store; its emptiness is
		// what this harness needs.
		env.Deps.RunRT = nil
	}
	return &peerTestEnv{Environment: env, dirs: dirs, detectCalls: &calls}
}

// setSwitch writes the interop switch into the snapshot the service reads.
func (env *peerTestEnv) setSwitch(on bool) {
	env.reloadMu.Lock()
	if on {
		env.RawConfig.Peers.ClaudeCode = appcfg.BoolPtr(true)
	} else {
		env.RawConfig.Peers.ClaudeCode = nil
	}
	env.reloadMu.Unlock()
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ownRecord reads this process's record out of dir.
func ownRecord(t *testing.T, dir string) *peer.Record {
	t.Helper()
	for _, rec := range peer.ReadRecords(dir) {
		if rec.PID == os.Getpid() {
			return &rec
		}
	}
	return nil
}

// dirNames lists a directory's file names, or nil when it does not exist.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}

// TestPeerServiceRegistersInFBModeByDefault pins the default: the switch off
// means FB mode without a single look at Claude Code's directories, with the
// record's protocol values and file modes exactly as the wire expects.
func TestPeerServiceRegistersInFBModeByDefault(t *testing.T) {
	env := peerTestEnvFor(t, "tui", func() string { return "cli-sess-default" })
	if err := os.MkdirAll(env.dirs.ccDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stop := env.StartPeers(context.Background(), PeerSurface{Kind: "tui", CurrentSession: func() string { return "cli-sess-default" }})
	defer stop()

	waitFor(t, "the FB record", func() bool { return ownRecord(t, env.dirs.fbDir) != nil })
	rec := ownRecord(t, env.dirs.fbDir)
	if rec.Kind != "interactive" || rec.Entrypoint != "forebrain-tui" {
		t.Fatalf("kind/entrypoint = %q/%q", rec.Kind, rec.Entrypoint)
	}
	if !strings.HasPrefix(rec.Version, "forebrain ") || rec.PeerProtocol != 1 {
		t.Fatalf("version/protocol = %q/%d", rec.Version, rec.PeerProtocol)
	}
	if len(rec.PeerFeatures) != 1 || rec.PeerFeatures[0] != "reply_across_default_dirs" {
		t.Fatalf("peerFeatures = %v", rec.PeerFeatures)
	}
	if rec.NameSource != "derived" || rec.Name == "" {
		t.Fatalf("name/source = %q/%q", rec.Name, rec.NameSource)
	}
	if rec.Cwd != env.LaunchDir || rec.SessionID != "cli-sess-default" {
		t.Fatalf("cwd/sessionId = %q/%q", rec.Cwd, rec.SessionID)
	}
	if rec.Forebrain == nil || rec.Forebrain.V != 1 || rec.Forebrain.Surface != "tui" || rec.Forebrain.Agents == nil {
		t.Fatalf("extension block = %+v", rec.Forebrain)
	}
	if rec.Status != peer.StatusIdle {
		t.Fatalf("status = %q, want idle", rec.Status)
	}
	if len(rec.Forebrain.Agents) != 1 || rec.Forebrain.Agents[0].ID != "cli-sess-default" || rec.Forebrain.Agents[0].Mode != "prompting" {
		t.Fatalf("agents = %+v", rec.Forebrain.Agents)
	}

	pid := fmt.Sprintf("%d", os.Getpid())
	keys := 0
	for _, name := range dirNames(t, env.dirs.fbDir) {
		if strings.HasPrefix(name, pid+".") && strings.HasSuffix(name, ".key") {
			keys++
			info, err := os.Stat(filepath.Join(env.dirs.fbDir, name))
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("key %s mode = %v err=%v, want 0600", name, info.Mode().Perm(), err)
			}
		}
	}
	if keys != 1 {
		t.Fatalf("found %d key files, want 1", keys)
	}
	socket := filepath.Join(env.dirs.fbSock, pid+".sock")
	info, err := os.Stat(socket)
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, want 0600", info.Mode().Perm())
	}

	// The two CC directories stay empty and the detector never ran.
	if got := dirNames(t, env.dirs.ccDir); len(got) != 0 {
		t.Fatalf("cc rendezvous dir = %v, want empty", got)
	}
	if got := dirNames(t, env.dirs.ccSock); len(got) != 0 {
		t.Fatalf("cc socket dir = %v, want empty", got)
	}
	time.Sleep(120 * time.Millisecond)
	if *env.detectCalls != 0 {
		t.Fatalf("detection ran %d times with the switch off, want 0", *env.detectCalls)
	}

	stop()
	waitFor(t, "the FB record to disappear", func() bool { return ownRecord(t, env.dirs.fbDir) == nil })
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket survived stop: %v", err)
	}
}

// captureHandler collects the control frames a test's sender server receives.
type captureHandler struct {
	frames chan peer.Frame
}

func (h *captureHandler) HandleFrame(ctx context.Context, in peer.Inbound) {
	if in.Frame.Type == peer.FrameTypeControl {
		select {
		case h.frames <- in.Frame:
		default:
		}
	}
}

// startSender listens on its own socket in socketDir and returns its address
// and the frames it receives.
func startSender(t *testing.T, socketDir string) (string, *captureHandler) {
	t.Helper()
	server := &peer.Server{
		Path:    filepath.Join(socketDir, "424242.sock"),
		Token:   peer.NewToken(),
		Handler: &captureHandler{frames: make(chan peer.Frame, 8)},
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	handler := server.Handler.(*captureHandler)
	return server.Path, handler
}

// sendUserFrame sends one user frame from the sender's socket to the
// service's socket.
func sendUserFrame(t *testing.T, from, to string) {
	t.Helper()
	frame := peer.NewUserFrame(peer.UserFrameOptions{From: peer.EncodeUDS(from), Content: "<cross-session-message>hi</cross-session-message>"})
	if err := peer.Send(context.Background(), to, frame, peer.SendOptions{Timeout: 2 * time.Second}); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// TestPeerServiceFBModeSourceGate pins the gate: in FB mode a frame is only
// answered when the sender's pid matches a record carrying the forebrain
// extension block whose socket is the frame's own address. What it is answered
// with is the receiving pipeline's business (plan 196).
func TestPeerServiceFBModeSourceGate(t *testing.T) {
	env := peerTestEnvFor(t, "tui", func() string { return "cli-gate" })
	senderSocket, sender := startSender(t, env.dirs.fbSock)
	stop := env.StartPeers(context.Background(), PeerSurface{Kind: "tui", CurrentSession: func() string { return "cli-gate" }})
	defer stop()
	waitFor(t, "the FB record", func() bool { return ownRecord(t, env.dirs.fbDir) != nil })

	serviceSocket := filepath.Join(env.dirs.fbSock, fmt.Sprintf("%d.sock", os.Getpid()))
	// The sender is this test process: its record takes over this pid's own
	// file, which is exactly what a foreign same-pid entry would look like to
	// the gate.
	writeSenderRecord := func(withExtension bool) {
		t.Helper()
		rec := peer.Record{
			PID: os.Getpid(), Kind: "interactive", Name: "sender-1",
			MessagingSocketPath: senderSocket,
		}
		if withExtension {
			rec.Forebrain = &peer.Extension{V: 1, Surface: "tui"}
		}
		if err := peer.WriteRecord(env.dirs.fbDir, rec); err != nil {
			t.Fatal(err)
		}
		env.peers.resetDirCache()
	}

	writeSenderRecord(false)
	sendUserFrame(t, senderSocket, serviceSocket)
	select {
	case frame := <-sender.frames:
		t.Fatalf("frame without a forebrain record was answered: %+v", frame)
	case <-time.After(300 * time.Millisecond):
	}

	writeSenderRecord(true)
	sendUserFrame(t, senderSocket, serviceSocket)
	select {
	case frame := <-sender.frames:
		if frame.Status != peer.StatusExpired || frame.StatusDetail != peer.StatusDetailRefused {
			t.Fatalf("receipt = %s/%s, want expired/refused", frame.Status, frame.StatusDetail)
		}
		// Nothing is attached to run a turn here, so the pipeline refuses the
		// message instead of taking it into a conversation that cannot run it
		// (plan 196 §3.2 step 7).
		if frame.Reason != "inbox not attached" {
			t.Fatalf("receipt reason = %q", frame.Reason)
		}
		if frame.From != peer.EncodeUDS(serviceSocket) {
			t.Fatalf("receipt from = %q, want the service's own address", frame.From)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the gated frame was never answered")
	}
}

// TestPeerServiceFollowsTheCCDirectory pins the whole mode lifecycle: the
// switch on with no directory stays FB, the directory's appearance moves the
// registration to CC within a check period, its removal moves it back —
// without recreating the directory — and the name survives both moves.
func TestPeerServiceFollowsTheCCDirectory(t *testing.T) {
	env := peerTestEnvFor(t, "tui", func() string { return "cli-cc" })
	stop := env.StartPeers(context.Background(), PeerSurface{Kind: "tui", CurrentSession: func() string { return "cli-cc" }})
	defer stop()
	waitFor(t, "the FB record", func() bool { return ownRecord(t, env.dirs.fbDir) != nil })
	fbName := ownRecord(t, env.dirs.fbDir).Name

	env.setSwitch(true)
	time.Sleep(200 * time.Millisecond)
	if ownRecord(t, env.dirs.fbDir) == nil {
		t.Fatal("left FB mode although the CC directory does not exist")
	}
	if _, err := os.Stat(env.dirs.ccDir); !os.IsNotExist(err) {
		t.Fatalf("CC rendezvous directory was created: %v", err)
	}

	if err := os.MkdirAll(env.dirs.ccDir, 0o700); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the CC record", func() bool { return ownRecord(t, env.dirs.ccDir) != nil })
	ccRec := ownRecord(t, env.dirs.ccDir)
	if ccRec.Name != fbName {
		t.Fatalf("name moved %q -> %q, want it kept", fbName, ccRec.Name)
	}
	if !strings.HasPrefix(ccRec.MessagingSocketPath, env.dirs.ccSock) {
		t.Fatalf("CC socket = %q, want it inside %s", ccRec.MessagingSocketPath, env.dirs.ccSock)
	}
	if ownRecord(t, env.dirs.fbDir) != nil {
		t.Fatal("the FB record survived the switch to CC")
	}
	if got := dirNames(t, env.dirs.fbSock); len(got) != 0 {
		t.Fatalf("FB socket dir = %v, want no file of ours", got)
	}
	ccSocket := ccRec.MessagingSocketPath

	if err := os.RemoveAll(env.dirs.ccDir); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the return to FB", func() bool { return ownRecord(t, env.dirs.fbDir) != nil })
	if _, err := os.Stat(env.dirs.ccDir); !os.IsNotExist(err) {
		t.Fatal("the CC rendezvous directory was recreated")
	}
	if got := dirNames(t, env.dirs.ccSock); len(got) != 0 {
		t.Fatalf("CC socket dir = %v, want the socket gone with the mode", got)
	}
	if name := ownRecord(t, env.dirs.fbDir).Name; name != fbName {
		t.Fatalf("name moved back as %q, want %q", name, fbName)
	}
	_ = ccSocket
}

// TestPeerServiceCCModeLeavesTheFBSideAlone pins the hands-off rule: while
// in CC mode the service does not read, write or clean anything in the FB
// directories, dead record or not.
func TestPeerServiceCCModeLeavesTheFBSideAlone(t *testing.T) {
	env := peerTestEnvFor(t, "tui", func() string { return "cli-hands-off" })
	if err := os.MkdirAll(env.dirs.ccDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(env.dirs.fbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := peer.WriteRecord(env.dirs.fbDir, peer.Record{PID: 4123456, Kind: "interactive", Name: "stranger", MessagingSocketPath: filepath.Join(env.dirs.fbSock, "4123456.sock")}); err != nil {
		t.Fatal(err)
	}
	env.setSwitch(true)
	stop := env.StartPeers(context.Background(), PeerSurface{Kind: "tui", CurrentSession: func() string { return "cli-hands-off" }})
	defer stop()
	waitFor(t, "the CC record", func() bool { return ownRecord(t, env.dirs.ccDir) != nil })

	snapshot := func() (names []string, mtimes map[string]int64) {
		mtimes = map[string]int64{}
		for _, dir := range []string{env.dirs.fbDir, env.dirs.fbSock} {
			entries, err := os.ReadDir(dir)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				t.Fatal(err)
			}
			for _, entry := range entries {
				path := filepath.Join(dir, entry.Name())
				info, err := os.Stat(path)
				if err != nil {
					continue
				}
				names = append(names, path)
				mtimes[path] = info.ModTime().UnixNano()
			}
		}
		return names, mtimes
	}
	beforeNames, beforeTimes := snapshot()
	// 70 ticks cover the dead-record cleanup period (60 ticks) plus margin.
	time.Sleep(70 * 20 * time.Millisecond)
	afterNames, afterTimes := snapshot()
	if len(beforeNames) == 0 || strings.Join(beforeNames, "|") != strings.Join(afterNames, "|") {
		t.Fatalf("FB side changed:\nbefore %v\nafter  %v", beforeNames, afterNames)
	}
	for path, at := range beforeTimes {
		if afterTimes[path] != at {
			t.Fatalf("%s was touched during CC mode", path)
		}
	}
}

// TestPeerServiceCCModeReceivesFrames pins the placeholder inbox in CC mode:
// a sender with no forebrain block — a Claude Code session's shape — is
// answered from the CC address.
func TestPeerServiceCCModeReceivesFrames(t *testing.T) {
	env := peerTestEnvFor(t, "tui", func() string { return "cli-cc-inbox" })
	if err := os.MkdirAll(env.dirs.ccDir, 0o700); err != nil {
		t.Fatal(err)
	}
	env.setSwitch(true)
	stop := env.StartPeers(context.Background(), PeerSurface{Kind: "tui", CurrentSession: func() string { return "cli-cc-inbox" }})
	defer stop()
	waitFor(t, "the CC record", func() bool { return ownRecord(t, env.dirs.ccDir) != nil })
	serviceSocket := ownRecord(t, env.dirs.ccDir).MessagingSocketPath

	senderSocket, sender := startSender(t, env.dirs.ccSock)
	// This test process is the sender; its record takes this pid's file in
	// the CC directory, without the extension block Claude Code would not
	// write either.
	if err := peer.WriteRecord(env.dirs.ccDir, peer.Record{PID: os.Getpid(), Kind: "interactive", Name: "cc-sender", MessagingSocketPath: senderSocket}); err != nil {
		t.Fatal(err)
	}
	env.peers.resetDirCache()

	sendUserFrame(t, senderSocket, serviceSocket)
	select {
	case frame := <-sender.frames:
		if frame.Status != peer.StatusExpired || frame.StatusDetail != peer.StatusDetailRefused {
			t.Fatalf("receipt = %s/%s, want expired/refused", frame.Status, frame.StatusDetail)
		}
		if frame.From != peer.EncodeUDS(serviceSocket) {
			t.Fatalf("receipt from = %q, want the current CC address", frame.From)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the CC-mode frame was never answered")
	}
}

// TestPeerServiceSwitchOffLeavesCC pins the runtime switch-off: the next tick
// returns the registration to FB, the CC side keeps nothing of ours, and the
// detector stops being consulted.
func TestPeerServiceSwitchOffLeavesCC(t *testing.T) {
	env := peerTestEnvFor(t, "tui", func() string { return "cli-off" })
	if err := os.MkdirAll(env.dirs.ccDir, 0o700); err != nil {
		t.Fatal(err)
	}
	env.setSwitch(true)
	stop := env.StartPeers(context.Background(), PeerSurface{Kind: "tui", CurrentSession: func() string { return "cli-off" }})
	defer stop()
	waitFor(t, "the CC record", func() bool { return ownRecord(t, env.dirs.ccDir) != nil })
	ccSocket := ownRecord(t, env.dirs.ccDir).MessagingSocketPath

	env.setSwitch(false)
	waitFor(t, "the return to FB", func() bool { return ownRecord(t, env.dirs.fbDir) != nil })
	if ownRecord(t, env.dirs.ccDir) != nil {
		t.Fatal("the CC record survived the switch-off")
	}
	if _, err := os.Stat(ccSocket); !os.IsNotExist(err) {
		t.Fatal("the CC socket survived the switch-off")
	}
	calls := *env.detectCalls
	time.Sleep(10 * 20 * time.Millisecond)
	if *env.detectCalls != calls {
		t.Fatalf("detector ran %d more times after the switch-off, want 0", *env.detectCalls-calls)
	}
}

// TestPeerServiceStatusFollowsRuns pins the record's status: busy while the
// current session's run is live, idle after it ends, and not one disk write
// in between when nothing changed.
func TestPeerServiceStatusFollowsRuns(t *testing.T) {
	env := peerTestEnvFor(t, "tui", func() string { return "cli-busy" })
	stop := env.StartPeers(context.Background(), PeerSurface{Kind: "tui", CurrentSession: func() string { return "cli-busy" }})
	defer stop()
	waitFor(t, "the FB record", func() bool { return ownRecord(t, env.dirs.fbDir) != nil })
	recordPath := filepath.Join(env.dirs.fbDir, fmt.Sprintf("%d.json", os.Getpid()))

	if _, err := env.Control.Begin(context.Background(), "run-busy-1", "cli-busy"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "status busy", func() bool {
		rec := ownRecord(t, env.dirs.fbDir)
		return rec != nil && rec.Status == peer.StatusBusy
	})
	firstStat, err := os.Stat(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if !env.Control.Finish("run-busy-1") {
		t.Fatal("Finish")
	}
	waitFor(t, "status idle", func() bool {
		rec := ownRecord(t, env.dirs.fbDir)
		return rec != nil && rec.Status == peer.StatusIdle
	})
	secondStat, err := os.Stat(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * 20 * time.Millisecond)
	thirdStat, err := os.Stat(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if secondStat.ModTime() != thirdStat.ModTime() {
		t.Fatal("an unchanged tick rewrote the record")
	}
	_ = firstStat
}

// TestPeerServiceCleansOnlyItsOwnDeadRecords pins the cleanup: a dead record
// carrying the forebrain block disappears, a dead one without it — Claude
// Code's own — stays.
func TestPeerServiceCleansOnlyItsOwnDeadRecords(t *testing.T) {
	env := peerTestEnvFor(t, "tui", func() string { return "cli-clean" })
	if err := os.MkdirAll(env.dirs.fbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dead := peer.Record{PID: 424243, Kind: "interactive", Name: "dead-forebrain", Forebrain: &peer.Extension{V: 1, Surface: "tui"}}
	if err := peer.WriteRecord(env.dirs.fbDir, dead); err != nil {
		t.Fatal(err)
	}
	foreign := peer.Record{PID: 424244, Kind: "interactive", Name: "dead-claude"}
	if err := peer.WriteRecord(env.dirs.fbDir, foreign); err != nil {
		t.Fatal(err)
	}
	stop := env.StartPeers(context.Background(), PeerSurface{Kind: "tui", CurrentSession: func() string { return "cli-clean" }})
	defer stop()
	waitFor(t, "our own record", func() bool { return ownRecord(t, env.dirs.fbDir) != nil })

	deadPath := filepath.Join(env.dirs.fbDir, "424243.json")
	foreignPath := filepath.Join(env.dirs.fbDir, "424244.json")
	waitFor(t, "the dead forebrain record to be cleaned", func() bool {
		_, err := os.Stat(deadPath)
		return os.IsNotExist(err)
	})
	if _, err := os.Stat(foreignPath); err != nil {
		t.Fatalf("the record without an extension block was cleaned: %v", err)
	}
}

// A failed start is not the end of the registration. The loop is running
// before the first attempt is made, so the next tick tries again: a socket
// directory that is unusable once — a transient failure, or one the person
// fixes — must not cost this process its entire life (plan 237 §3).
func TestPeerServiceRetriesAFailedStart(t *testing.T) {
	env := peerTestEnvFor(t, "tui", func() string { return "cli-retry" })
	// A regular file where the socket directory belongs: the first attempt
	// cannot create the directory under it. Removing the file is the person
	// clearing whatever it was.
	blocker := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocker, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FOREBRAIN_PEER_SOCKET_DIR", filepath.Join(blocker, "sock"))

	stop := env.StartPeers(context.Background(), PeerSurface{Kind: "tui", CurrentSession: func() string { return "cli-retry" }})
	defer stop()

	// The first attempt failed before anything was written, and no attempt can
	// succeed while the file is there.
	if rec := ownRecord(t, env.dirs.fbDir); rec != nil {
		t.Fatalf("record = %+v, want the first attempt to have failed", rec)
	}
	time.Sleep(3 * peerTickInterval)
	if rec := ownRecord(t, env.dirs.fbDir); rec != nil {
		t.Fatalf("record = %+v, want nothing registered while the directory is unusable", rec)
	}
	// The port is wired even though the first attempt failed: the loop will
	// register this process on a later tick, and it must be reachable then.
	if env.peerInboxState() == nil {
		t.Fatal("the peer inbox is unwired after a failed first attempt")
	}
	if err := env.ReleasePeerMessage("no-such-message"); err == nil ||
		strings.Contains(err.Error(), "no peer service is running") {
		t.Fatalf("release reached no inbox: %v", err)
	}

	// Without a restart, the loop's next attempt registers the process.
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the retried registration", func() bool { return ownRecord(t, env.dirs.fbDir) != nil })
	// Registered and reachable are the same process here: the record says so and
	// the port answers from the same service.
	if env.peerInboxState() == nil {
		t.Fatal("the peer inbox is unwired after the retried registration")
	}
	rec := ownRecord(t, env.dirs.fbDir)
	if rec.SessionID != "cli-retry" {
		t.Fatalf("sessionId = %q, want the retried registration to be the real one", rec.SessionID)
	}
}

// TestClaudeCodeDetectedOnlyStats pins the display condition helper: an
// existing directory answers true, a missing one false — and the missing one
// stays missing.
func TestClaudeCodeDetectedOnlyStats(t *testing.T) {
	env := peerTestEnvFor(t, "tui", nil)
	if ClaudeCodeDetected() {
		t.Fatal("ClaudeCodeDetected true with no CC directory")
	}
	if _, err := os.Stat(env.dirs.ccDir); !os.IsNotExist(err) {
		t.Fatalf("the missing CC directory was created: %v", err)
	}
	if err := os.MkdirAll(env.dirs.ccDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if !ClaudeCodeDetected() {
		t.Fatal("ClaudeCodeDetected false with the CC directory present")
	}
}

// TestReadGitBranchInProcess pins the branch reader both ways a work tree
// presents itself: a plain .git directory and a linked worktree's .git file.
func TestReadGitBranchInProcess(t *testing.T) {
	plain := t.TempDir()
	gitDir := filepath.Join(plain, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/feat-x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readGitBranch(plain); got != "feat-x" {
		t.Fatalf("plain work tree branch = %q, want feat-x", got)
	}

	tree := t.TempDir()
	realGit := filepath.Join(t.TempDir(), "real-git")
	if err := os.MkdirAll(realGit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realGit, "HEAD"), []byte("ref: refs/heads/feat-x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join(tree, ".git")
	if err := os.WriteFile(rel, []byte("gitdir: "+realGit+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readGitBranch(tree); got != "feat-x" {
		t.Fatalf("linked worktree branch = %q, want feat-x", got)
	}

	if got := readGitBranch(t.TempDir()); got != "" {
		t.Fatalf("no .git anywhere branch = %q, want empty", got)
	}
	detached := t.TempDir()
	detachedGit := filepath.Join(detached, ".git")
	if err := os.MkdirAll(detachedGit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(detachedGit, "HEAD"), []byte("0123456789abcdef\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readGitBranch(detached); got != "0123456" {
		t.Fatalf("detached branch = %q, want 0123456", got)
	}
}

// TestParsePeerEnvelope pins the thin wrapper the terminal reads a peer message
// through (plan 199): the sender's name and the body, and plain text for
// anything that is not exactly one envelope.
func TestParsePeerEnvelope(t *testing.T) {
	envelope := peer.Build(peer.Envelope{From: "uds:/tmp/a.sock", FromName: "builder", Body: "please review line 12"})
	name, body, ok := ParsePeerEnvelope(envelope)
	if !ok {
		t.Fatalf("ParsePeerEnvelope(%q) reported false", envelope)
	}
	if name != "builder" || body != "please review line 12" {
		t.Fatalf("ParsePeerEnvelope = %q, %q; want builder, please review line 12", name, body)
	}
	// The row is stored trimmed: surrounding space must not defeat the parse.
	if _, _, ok := ParsePeerEnvelope("\n  " + envelope + "  \n"); !ok {
		t.Fatal("ParsePeerEnvelope rejected an envelope the row had padded")
	}
	if _, _, ok := ParsePeerEnvelope("just a message"); ok {
		t.Fatal("ParsePeerEnvelope accepted plain text")
	}
	if name, _, ok := ParsePeerEnvelope(peer.Build(peer.Envelope{Body: "no name"})); !ok || name != "" {
		t.Fatalf("envelope without from-name = %q, %v; want empty name and true", name, ok)
	}
}

// TestPeerMark pins the identity block's derivation (plan 200 §3): the two
// characters a surface draws, and the tone that stays with one agent wherever
// it is drawn. The marks are the design's own examples; the tones are only
// pinned as "stable and in range" — the palette's index is an implementation
// choice, the stability is the contract.
func TestPeerMark(t *testing.T) {
	for _, tc := range []struct {
		name   string
		parent string
		mark   string
	}{
		{name: "forebrain-harness-a3", mark: "a3"},
		{name: "forebrain-harness-28", mark: "28"},
		{name: "forebrain-gw-3c1a", mark: "3c"},
		{name: "docs-site-7f", mark: "7f"},
		{name: "forebrain-gw-3c1a.review-auth", parent: "forebrain-gw-3c1a", mark: "ra"},
		{name: "solo", mark: "so"},
		{name: "forebrain-harness-a3.review", parent: "forebrain-harness-a3", mark: "re"},
	} {
		mark, tone := PeerMark(tc.name, tc.parent)
		if mark != tc.mark {
			t.Fatalf("PeerMark(%q, %q) mark = %q, want %q", tc.name, tc.parent, mark, tc.mark)
		}
		if tone < 0 || tone >= peerToneCount {
			t.Fatalf("PeerMark(%q, %q) tone = %d, want 0..%d", tc.name, tc.parent, tone, peerToneCount-1)
		}
	}
	// The same agent reads the same everywhere: the derivation is a pure
	// function of the name.
	first, firstTone := PeerMark("forebrain-harness-a3", "")
	again, againTone := PeerMark("forebrain-harness-a3", "")
	if first != again || firstTone != againTone {
		t.Fatalf("PeerMark is not stable: %q/%d then %q/%d", first, firstTone, again, againTone)
	}
	// A subagent wears its parent's tone, not its own name's.
	_, subTone := PeerMark("forebrain-harness-a3.review-auth", "forebrain-harness-a3")
	_, parentTone := PeerMark("forebrain-harness-a3", "")
	if subTone != parentTone {
		t.Fatalf("subagent tone = %d, parent tone = %d; want them equal", subTone, parentTone)
	}
}
