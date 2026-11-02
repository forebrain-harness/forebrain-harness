// The receiving pipeline's contract (plan 196 §4 step 3): admission, routing,
// the inbound policy and its hold buffer, delivery into a running turn or as a
// turn of its own, receipts, and idle subscriptions.
//
// Every case points all four directory overrides at its own temporary
// directories (peerTestDirsFor, from peers_test.go) — a test binary without them
// would read and write the user's real entries — and both socket directories
// live under /tmp, whose sticky bit is what the socket directory vetting
// expects.
package process

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/peer"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// peerTestView is the surface a test hands the service: it records what the
// pipeline published and what it asked to be run, and can refuse to run
// anything.
type peerTestView struct {
	mu       sync.Mutex
	events   []peerTestEvent
	wakes    []PeerPrompt
	unusable bool
}

// peerTestEvent is one run event the pipeline published.
type peerTestEvent struct {
	sessionID string
	typ       string
	payload   any
}

func (v *peerTestView) publish(_ context.Context, sessionID, _ string, typ string, payload any) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.events = append(v.events, peerTestEvent{sessionID: sessionID, typ: typ, payload: payload})
	return nil
}

func (v *peerTestView) wake(_ context.Context, p PeerPrompt) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.unusable {
		return ErrPeerWakeUnavailable
	}
	v.wakes = append(v.wakes, p)
	return nil
}

// surface is the port the service is started with.
func (v *peerTestView) surface(kind string, current func() string) PeerSurface {
	return PeerSurface{
		Kind:           kind,
		CurrentSession: current,
		Publish:        v.publish,
		Wake:           v.wake,
	}
}

// wakeCount is how many turns the pipeline asked this surface to start.
func (v *peerTestView) wakeCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.wakes)
}

// texts lists what the surface was asked to run, in order.
func (v *peerTestView) texts() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]string, 0, len(v.wakes))
	for _, wake := range v.wakes {
		out = append(out, wake.Text)
	}
	return out
}

// eventTypes lists what was published, in order.
func (v *peerTestView) eventTypes() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]string, 0, len(v.events))
	for _, evt := range v.events {
		out = append(out, evt.typ)
	}
	return out
}

// settledOutcomes lists every held-message outcome published, in order.
func (v *peerTestView) settledOutcomes() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]string, 0, len(v.events))
	for _, evt := range v.events {
		if evt.typ != event.RunEventPeerMessageSettled {
			continue
		}
		if settled, ok := evt.payload.(event.PeerMessageSettledPayload); ok {
			out = append(out, settled.Outcome)
		}
	}
	return out
}

// eventOf finds the first published payload of a type.
func (v *peerTestView) eventOf(typ string) (any, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, evt := range v.events {
		if evt.typ == typ {
			return evt.payload, true
		}
	}
	return nil, false
}

// peerTestService is one running service plus the peer that talks to it.
type peerTestService struct {
	env      *peerTestEnv
	view     *peerTestView
	sender   *captureHandler
	sendFrom string
	socket   string // the service's own socket
	// dir and cwd are the sender's registration: the directory it is written
	// in, and the work tree it claims. The sender is this test process, so the
	// service's own record file is the one it takes — and the service rewrites
	// that file whenever its own record changes, which is why every frame
	// restores it first.
	dir  string
	cwd  string
	stop func()
}

// refreshSender restores the sender's registration before a frame is sent. A
// real peer is a different process with its own record file; here the two share
// a pid, so the file is the sender's only until the service writes its own. A
// harness that manages the sender's record itself leaves dir empty and restores
// nothing.
func (s *peerTestService) refreshSender(t *testing.T) {
	t.Helper()
	if s.dir == "" {
		return
	}
	// Written until it stays written: a tick that rewrites the service's own
	// record would otherwise take the file back between this call and the frame.
	deadline := time.Now().Add(time.Second)
	for {
		writeSenderRecord(t, s.env, s.dir, s.sendFrom, s.cwd)
		rec := ownRecord(t, s.dir)
		if rec != nil && rec.MessagingSocketPath == s.sendFrom {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the sender's registration keeps being overwritten")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// startPeerTestService brings up a service with a fake surface and a sender
// whose record makes it a valid forebrain peer. configure runs before the
// service starts, so a runner (and with it the conversation's approval mode) can
// be in place from the first frame.
//
// The service's tick is slowed to an hour by default: the sender and the service
// are one process here, so they take turns owning the one record file a pid has,
// and a tick that rewrites the service's own record mid-frame would make the
// source gate reject the sender. A case that needs the tick — a conversation
// going idle, a subscription expiring — sets peerTickInterval itself in
// configure.
func startPeerTestService(t *testing.T, kind, session string, view *peerTestView, configure func(*peerTestEnv)) *peerTestService {
	t.Helper()
	env := peerTestEnvFor(t, kind, func() string { return session })
	peerTickInterval = time.Hour
	if configure != nil {
		configure(env)
	}
	var current func() string
	if kind != "gateway" {
		current = func() string { return session }
	}
	stop := env.StartPeers(context.Background(), view.surface(kind, current))
	t.Cleanup(stop)
	// The registration lands in whichever directory the mode decided on.
	waitFor(t, "the peer registration", func() bool {
		return ownRecord(t, env.dirs.fbDir) != nil || ownRecord(t, env.dirs.ccDir) != nil
	})
	// The sender registers where the service did, and listens in the same
	// socket namespace.
	dir, socketDir := env.dirs.fbDir, env.dirs.fbSock
	if ownRecord(t, env.dirs.ccDir) != nil {
		dir, socketDir = env.dirs.ccDir, env.dirs.ccSock
	}
	serviceSocket := ownRecord(t, dir).MessagingSocketPath
	sendFrom, sender := startSender(t, socketDir)
	writeSenderRecord(t, env, dir, sendFrom, "")
	return &peerTestService{
		env: env, view: view, sender: sender, sendFrom: sendFrom,
		socket: serviceSocket, dir: dir, stop: stop,
	}
}

// writeSenderRecord registers this test process as the peer talking to the
// service: the same pid, its own socket, a work tree it claims, and the
// extension block that makes it a forebrain peer in FB mode. It takes the
// record file of the process's own pid, which is exactly what a foreign
// same-pid entry looks like to the gate.
func writeSenderRecord(t *testing.T, env *peerTestEnv, dir, socket, cwd string) {
	t.Helper()
	writeSenderRecordShaped(t, env, dir, socket, cwd, true)
}

// writeSenderRecordShaped writes the sender's record with or without the
// forebrain extension block: Claude Code writes none, and in CC mode its records
// are the ones the source gate admits beside our own.
func writeSenderRecordShaped(t *testing.T, env *peerTestEnv, dir, socket, cwd string, withExtension bool) {
	t.Helper()
	rec := peer.Record{
		PID:                 os.Getpid(),
		Kind:                "interactive",
		Name:                "sender-1",
		Cwd:                 cwd,
		MessagingSocketPath: socket,
		PeerFeatures:        []string{"reply_across_default_dirs"},
	}
	if withExtension {
		rec.Forebrain = &peer.Extension{V: 1, Surface: "tui"}
	}
	if err := peer.WriteRecord(dir, rec); err != nil {
		t.Fatal(err)
	}
	if env.peers != nil {
		env.peers.resetDirCache()
	}
}

// envelopeOf is the frame content a test sends.
func envelopeOf(from, body string, options ...func(*peer.Envelope)) string {
	envelope := peer.Envelope{From: from, Body: body}
	for _, option := range options {
		option(&envelope)
	}
	return peer.Build(envelope)
}

// withFromMode asserts the sender's permission mode in the envelope.
func withFromMode(mode string) func(*peer.Envelope) {
	return func(e *peer.Envelope) { e.FromMode = mode }
}

// withFromName names the sender in the envelope.
func withFromName(name string) func(*peer.Envelope) {
	return func(e *peer.Envelope) { e.FromName = name }
}

// sendFrame posts one user frame to the service and returns its message id.
func (s *peerTestService) sendFrame(t *testing.T, content string, extension *peer.FrameExtension) string {
	t.Helper()
	s.refreshSender(t)
	frame := peer.NewUserFrame(peer.UserFrameOptions{From: peer.EncodeUDS(s.sendFrom), Content: content})
	if extension != nil {
		frame.Forebrain = extension
	}
	if err := peer.Send(context.Background(), s.socket, frame, peer.SendOptions{Timeout: 2 * time.Second}); err != nil {
		t.Fatalf("send: %v", err)
	}
	return frame.MsgID
}

// sendControl posts one control frame.
func (s *peerTestService) sendControl(t *testing.T, frame peer.Frame) {
	t.Helper()
	s.refreshSender(t)
	if strings.TrimSpace(frame.From) == "" {
		frame.From = peer.EncodeUDS(s.sendFrom)
	}
	if err := peer.Send(context.Background(), s.socket, frame, peer.SendOptions{Timeout: 2 * time.Second}); err != nil {
		t.Fatalf("send control: %v", err)
	}
}

// receipt waits for the next receipt the service sends back.
func (s *peerTestService) receipt(t *testing.T, what string) peer.Frame {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case frame := <-s.sender.frames:
			if frame.Action != peer.ActionPeerMessageStatus {
				t.Fatalf("%s: got a %q control frame, want a receipt", what, frame.Action)
			}
			return frame
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("%s: no receipt arrived", what)
	return peer.Frame{}
}

// noReceipt fails the test when the service answers within a short window.
func (s *peerTestService) noReceipt(t *testing.T, what string) {
	t.Helper()
	select {
	case frame := <-s.sender.frames:
		t.Fatalf("%s: unexpected frame %+v", what, frame)
	case <-time.After(400 * time.Millisecond):
	}
}

// idleNotice waits for the next idle notice the service sends back.
func (s *peerTestService) idleNotice(t *testing.T, what string) peer.Frame {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case frame := <-s.sender.frames:
			if frame.Action == peer.ActionPeerIdleNotice {
				return frame
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("%s: no idle notice arrived", what)
	return peer.Frame{}
}

// noFrame fails the test when any frame arrives within a short window.
func (s *peerTestService) noFrame(t *testing.T, what string) {
	t.Helper()
	select {
	case frame := <-s.sender.frames:
		t.Fatalf("%s: unexpected frame %+v", what, frame)
	case <-time.After(400 * time.Millisecond):
	}
}

// fastPeerTick puts the service's tick back on the harness cadence, for a case
// that needs it to notice something.
func fastPeerTick(*peerTestEnv) { peerTickInterval = 20 * time.Millisecond }

// containsString reports whether values holds want.
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// approvalNeverRunner is a conversation that never asks the person anything:
// the "bypass" side of the inbound policy. The mode is set on the conversation,
// which is how a terminal in full access carries it.
func approvalNeverRunner(t *testing.T, home, sessionID string) *run.Runner {
	t.Helper()
	runner := promptingRunner(home)
	runner.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type:        safety.UpdateSetMode,
		Destination: safety.DestinationSession,
		SessionID:   sessionID,
		Mode:        safety.ModeNever,
	})
	if got := runner.PermissionSnapshotForSession(sessionID).ApprovalPolicy.Mode; got != safety.ApprovalNever {
		t.Fatalf("precondition: approval mode = %q, want never", got)
	}
	return runner
}

// promptingRunner is a conversation that asks: the "prompting" side.
func promptingRunner(home string) *run.Runner {
	return &run.Runner{Deps: &run.Deps{Home: home, AppCfg: &appcfg.Root{}}}
}

// openPeerSessions gives the environment a session store, which is what the
// gateway's routing reads.
func openPeerSessions(t *testing.T, env *peerTestEnv) *state.SessionStore {
	t.Helper()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	env.SQL = db
	store := state.NewSessionStore(db, "main")
	env.Deps.SessionStore = store
	return store
}

// seedPeerSession adds one conversation to the store with a directory, and
// returns its id.
func seedPeerSession(t *testing.T, store *state.SessionStore, id, cwd string) {
	t.Helper()
	if err := store.EnsureAt(context.Background(), id, id, state.SessionBirth{Cwd: cwd}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// seedPeerSessionOfType adds a conversation frozen to one project type. The row
// is what routing reads, so it is written where routing reads it.
func seedPeerSessionOfType(t *testing.T, store *state.SessionStore, id, cwd, projectType string) {
	t.Helper()
	seedPeerSession(t, store, id, cwd)
	if _, err := store.DB().ExecContext(context.Background(),
		`UPDATE fb_sessions SET project_type=? WHERE id=? AND agent_id=?`, projectType, id, store.AgentID()); err != nil {
		t.Fatalf("freeze %s as %s: %v", id, projectType, err)
	}
}

func TestPeerInboundWakesAnIdleSession(t *testing.T) {
	view := &peerTestView{}
	svc := startPeerTestService(t, "tui", "cli-idle-session", view, nil)
	envelope := envelopeOf(peer.EncodeUDS(svc.sendFrom), "please review the router change")
	svc.sendFrame(t, envelope, nil)

	waitFor(t, "the wake", func() bool { return view.wakeCount() == 1 })
	view.mu.Lock()
	prompt := view.wakes[0]
	view.mu.Unlock()
	if prompt.SessionID != "cli-idle-session" {
		t.Fatalf("woke %q, want the conversation on screen", prompt.SessionID)
	}
	if prompt.Text != envelope {
		t.Fatalf("wake text = %q, want the envelope verbatim", prompt.Text)
	}
	if prompt.Origin != state.MessageOriginPeer {
		t.Fatalf("wake origin = %q, want %q", prompt.Origin, state.MessageOriginPeer)
	}
	// Accepting a message is not an event: only holds, releases and refusals
	// are answered.
	svc.noReceipt(t, "an accepted message")
}

func TestPeerInboundSteersARunningSession(t *testing.T) {
	view := &peerTestView{}
	svc := startPeerTestService(t, "tui", "cli-running-session", view, nil)
	queue := svc.env.Control.SessionQueue("cli-running-session")
	queue.Attach(run.NewTurnInputRuntime())
	if !svc.env.Control.TrackRuntime("run-1", "cli-running-session", func() {}, queue.Runtime()) {
		t.Fatal("the test run was not tracked")
	}

	envelope := envelopeOf(peer.EncodeUDS(svc.sendFrom), "one more thing")
	svc.sendFrame(t, envelope, nil)

	waitFor(t, "the steer", func() bool { return len(queue.Preview().Steers) == 1 })
	// The queue's preview normalises whitespace the way it does for every steer;
	// what reaches the model is the parts, and those carry the envelope as it
	// arrived.
	entries := queue.Runtime().Snapshot()
	if len(entries) != 1 {
		t.Fatalf("the runtime holds %d entries, want 1", len(entries))
	}
	if got := llm.TextContent(entries[0].Parts...); got != envelope {
		t.Fatalf("steer parts = %q, want the envelope verbatim", got)
	}
	if entries[0].Origin != state.MessageOriginPeer {
		t.Fatalf("steer origin = %q", entries[0].Origin)
	}
	svc.noReceipt(t, "an accepted message")
	if view.wakeCount() != 0 {
		t.Fatal("a running conversation was woken as well as steered")
	}
}

func TestPeerInboundHoldsReleasesAndDenies(t *testing.T) {
	view := &peerTestView{}
	svc := startPeerTestService(t, "tui", "cli-held", view, func(env *peerTestEnv) {
		// This conversation never asks the person anything, so a message from a
		// conversation that does is held for them.
		env.Runner = approvalNeverRunner(t, env.Root, "cli-held")
	})
	from := peer.EncodeUDS(svc.sendFrom)

	first := svc.sendFrame(t, envelopeOf(from, "hold me", withFromMode("prompting"), withFromName("planner")), nil)
	receipt := svc.receipt(t, "the hold")
	if receipt.Status != peer.StatusHeld || receipt.OrigMsgID != first {
		t.Fatalf("receipt = %s/%s, want held for %s", receipt.Status, receipt.OrigMsgID, first)
	}
	if receipt.Reason != "held: permission modes differ" {
		t.Fatalf("hold reason = %q", receipt.Reason)
	}
	waitFor(t, "the held event", func() bool { return len(view.eventTypes()) > 0 })
	payload, _ := view.eventOf(event.RunEventPeerMessageHeld)
	held, ok := payload.(event.PeerMessageHeldPayload)
	if !ok || held.MsgID != first || held.Cause != event.PeerHoldCauseModeMismatch {
		t.Fatalf("held payload = %#v", payload)
	}
	if held.FromName != "planner" || !strings.Contains(held.Preview, "hold me") {
		t.Fatalf("held payload = %#v", held)
	}
	if view.wakeCount() != 0 {
		t.Fatal("a held message was delivered anyway")
	}
	if got := svc.env.HeldPeerMessages("cli-held"); len(got) != 1 || got[0].MsgID != first {
		t.Fatalf("held list = %#v", got)
	}

	if err := svc.env.ReleasePeerMessage(first); err != nil {
		t.Fatalf("release: %v", err)
	}
	receipt = svc.receipt(t, "the release")
	if receipt.Status != peer.StatusDelivered || receipt.Reason != "released after approval" {
		t.Fatalf("release receipt = %s/%q", receipt.Status, receipt.Reason)
	}
	waitFor(t, "the released wake", func() bool { return view.wakeCount() == 1 })
	waitFor(t, "the released event", func() bool {
		return containsString(view.settledOutcomes(), event.PeerOutcomeReleased)
	})
	if got := svc.env.HeldPeerMessages("cli-held"); len(got) != 0 {
		t.Fatalf("the release left %#v held", got)
	}

	second := svc.sendFrame(t, envelopeOf(from, "deny me", withFromMode("prompting")), nil)
	if receipt := svc.receipt(t, "the second hold"); receipt.Status != peer.StatusHeld || receipt.OrigMsgID != second {
		t.Fatalf("second receipt = %s/%s", receipt.Status, receipt.OrigMsgID)
	}
	if err := svc.env.DenyPeerMessage(second); err != nil {
		t.Fatalf("deny: %v", err)
	}
	receipt = svc.receipt(t, "the denial")
	if receipt.Status != peer.StatusDenied || receipt.Reason != "declined by the user" {
		t.Fatalf("denial receipt = %s/%q", receipt.Status, receipt.Reason)
	}

	// Whatever is still held when the service stops is expired rather than left
	// waiting: the process will not answer again.
	third := svc.sendFrame(t, envelopeOf(from, "left behind", withFromMode("prompting")), nil)
	if receipt := svc.receipt(t, "the third hold"); receipt.Status != peer.StatusHeld || receipt.OrigMsgID != third {
		t.Fatalf("third receipt = %s/%s", receipt.Status, receipt.OrigMsgID)
	}
	svc.stop()
	receipt = svc.receipt(t, "the expiry on stop")
	if receipt.Status != peer.StatusExpired || receipt.OrigMsgID != third {
		t.Fatalf("expiry receipt = %s/%s", receipt.Status, receipt.OrigMsgID)
	}
	waitFor(t, "the expiry event", func() bool {
		return containsString(view.settledOutcomes(), event.PeerOutcomeExpired)
	})
}

// The hold buffer is capped: a message past the cap pushes the oldest one out,
// and that one is expired rather than silently dropped.
func TestPeerInboundHoldBufferIsCapped(t *testing.T) {
	view := &peerTestView{}
	svc := startPeerTestService(t, "tui", "cli-held-cap", view, func(env *peerTestEnv) {
		env.Runner = approvalNeverRunner(t, env.Root, "cli-held-cap")
	})
	from := peer.EncodeUDS(svc.sendFrom)
	ids := make([]string, 0, peerHeldLimit+1)
	evicted := ""
	for i := 0; i <= peerHeldLimit; i++ {
		id := svc.sendFrame(t, envelopeOf(from, fmt.Sprintf("held %d", i), withFromMode("prompting")), nil)
		ids = append(ids, id)
		for {
			receipt := svc.receipt(t, "the hold")
			if receipt.OrigMsgID == id {
				if receipt.Status != peer.StatusHeld {
					t.Fatalf("hold %d = %s", i, receipt.Status)
				}
				break
			}
			// The message the new one pushed out is expired, not dropped.
			if receipt.Status != peer.StatusExpired {
				t.Fatalf("the evicted message answered %s, want expired", receipt.Status)
			}
			evicted = receipt.OrigMsgID
		}
	}
	if evicted != ids[0] {
		t.Fatalf("evicted %q, want the oldest message %q", evicted, ids[0])
	}
	held := svc.env.HeldPeerMessages("cli-held-cap")
	if len(held) != peerHeldLimit {
		t.Fatalf("hold buffer = %d messages, want %d", len(held), peerHeldLimit)
	}
}

// The inbound policy is derived from the receiving conversation's own approval
// mode; there is no configured setting to read (owner 2026-10-31).
func TestPeerInboundPolicyFollowsTheSessionMode(t *testing.T) {
	bypass := func(session string) func(*peerTestEnv) {
		return func(env *peerTestEnv) { env.Runner = approvalNeverRunner(t, env.Root, session) }
	}
	prompting := func(env *peerTestEnv) { env.Runner = promptingRunner(env.Root) }

	t.Run("a mode mismatch is held", func(t *testing.T) {
		view := &peerTestView{}
		svc := startPeerTestService(t, "tui", "cli-policy-mismatch", view, bypass("cli-policy-mismatch"))
		svc.sendFrame(t, envelopeOf(peer.EncodeUDS(svc.sendFrom), "hello", withFromMode("prompting")), nil)
		if receipt := svc.receipt(t, "the hold"); receipt.Status != peer.StatusHeld {
			t.Fatalf("status = %s, want held", receipt.Status)
		}
	})
	t.Run("a bypass session holds a message that asserts no mode", func(t *testing.T) {
		view := &peerTestView{}
		svc := startPeerTestService(t, "tui", "cli-policy-nomode", view, bypass("cli-policy-nomode"))
		svc.sendFrame(t, envelopeOf(peer.EncodeUDS(svc.sendFrom), "hello"), nil)
		if receipt := svc.receipt(t, "the hold"); receipt.Status != peer.StatusHeld {
			t.Fatalf("status = %s, want held", receipt.Status)
		}
		waitFor(t, "the held event", func() bool { return len(view.eventTypes()) > 0 })
		payload, _ := view.eventOf(event.RunEventPeerMessageHeld)
		if held, ok := payload.(event.PeerMessageHeldPayload); !ok || held.Cause != event.PeerHoldCauseNoModeAsserted {
			t.Fatalf("cause = %#v", payload)
		}
	})
	t.Run("a prompting session takes a message that asserts no mode", func(t *testing.T) {
		view := &peerTestView{}
		svc := startPeerTestService(t, "tui", "cli-policy-accept", view, prompting)
		svc.sendFrame(t, envelopeOf(peer.EncodeUDS(svc.sendFrom), "hello"), nil)
		waitFor(t, "the wake", func() bool { return view.wakeCount() == 1 })
		svc.noReceipt(t, "an accepted message")
	})
	t.Run("matching modes are accepted", func(t *testing.T) {
		view := &peerTestView{}
		svc := startPeerTestService(t, "tui", "cli-policy-same", view, bypass("cli-policy-same"))
		svc.sendFrame(t, envelopeOf(peer.EncodeUDS(svc.sendFrom), "hello", withFromMode("bypass")), nil)
		waitFor(t, "the wake", func() bool { return view.wakeCount() == 1 })
		svc.noReceipt(t, "an accepted message")
	})
}

func TestPeerInboundAdmissionLimits(t *testing.T) {
	t.Run("the rate limit drops the thirty-first burst", func(t *testing.T) {
		view := &peerTestView{}
		svc := startPeerTestService(t, "tui", "cli-rate", view, nil)
		from := peer.EncodeUDS(svc.sendFrom)
		for i := 0; i < 30; i++ {
			svc.sendFrame(t, envelopeOf(from, fmt.Sprintf("burst %d", i)), nil)
		}
		svc.sendFrame(t, envelopeOf(from, "burst one too many"), nil)
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case frame := <-svc.sender.frames:
				if frame.Status != peer.StatusDropped {
					continue
				}
				if frame.DropReason != peer.ReasonRateLimited {
					t.Fatalf("drop reason = %q, want %q", frame.DropReason, peer.ReasonRateLimited)
				}
				if frame.Reason != peer.DropReasons[peer.ReasonRateLimited] {
					t.Fatalf("drop sentence = %q", frame.Reason)
				}
				if len(frame.DroppedMsgIDs) != 0 {
					t.Fatalf("dropped_msg_ids = %v, want none", frame.DroppedMsgIDs)
				}
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
		t.Fatal("the burst was never refused")
	})
	t.Run("a repeat inside the window is a duplicate", func(t *testing.T) {
		view := &peerTestView{}
		svc := startPeerTestService(t, "tui", "cli-dup", view, nil)
		from := peer.EncodeUDS(svc.sendFrom)
		body := envelopeOf(from, "the very same words")
		svc.sendFrame(t, body, nil)
		svc.sendFrame(t, body, nil)
		receipt := svc.receipt(t, "the duplicate receipt")
		if receipt.Status != peer.StatusDropped || receipt.DropReason != peer.ReasonDuplicate {
			t.Fatalf("receipt = %s/%q", receipt.Status, receipt.DropReason)
		}
	})
	t.Run("a message that has been through this process ten times is a loop", func(t *testing.T) {
		view := &peerTestView{}
		svc := startPeerTestService(t, "tui", "cli-hop", view, nil)
		svc.env.peers.mu.Lock()
		addr := svc.env.peers.addr
		key := svc.env.peers.hopKey
		svc.env.peers.mu.Unlock()
		token := peer.HopToken(key, addr)
		chain := make([]string, 0, peer.DefaultLimits.MaxSelfHops)
		for i := 0; i < peer.DefaultLimits.MaxSelfHops; i++ {
			chain = append(chain, token)
		}
		content := peer.Build(peer.Envelope{From: peer.EncodeUDS(svc.sendFrom), Body: "round and round", HopChain: chain})
		svc.sendFrame(t, content, nil)
		receipt := svc.receipt(t, "the hop-loop receipt")
		if receipt.Status != peer.StatusDropped || receipt.DropReason != peer.ReasonHopLoop {
			t.Fatalf("receipt = %s/%q", receipt.Status, receipt.DropReason)
		}
	})
}

func TestPeerInboundRefusesAnUnknownAgent(t *testing.T) {
	view := &peerTestView{}
	svc := startPeerTestService(t, "tui", "cli-unknown", view, nil)
	svc.sendFrame(t, envelopeOf(peer.EncodeUDS(svc.sendFrom), "for nobody"), &peer.FrameExtension{V: 1, To: "cli-somebody-else"})
	receipt := svc.receipt(t, "the refusal")
	if receipt.Status != peer.StatusExpired || receipt.StatusDetail != peer.StatusDetailRefused {
		t.Fatalf("receipt = %s/%s", receipt.Status, receipt.StatusDetail)
	}
	if receipt.Reason != peerRefusedUnknownAgent {
		t.Fatalf("reason = %q", receipt.Reason)
	}
	if view.wakeCount() != 0 {
		t.Fatal("a message for an unknown agent started a turn")
	}
}

// A gateway has many conversations and no screen: the message goes to the one
// living in the sender's work tree.
func TestPeerInboundGatewayRoutesByWorkTree(t *testing.T) {
	workTree := t.TempDir()
	inside := filepath.Join(workTree, "pkg", "nested")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()

	view := &peerTestView{}
	env := peerTestEnvFor(t, "gateway", nil)
	// The tick is not what these cases test, and a tick that rewrites the
	// service's own record would take back the sender's record file mid-test.
	peerTickInterval = time.Hour
	store := openPeerSessions(t, env)
	seedPeerSession(t, store, "web-unrelated", elsewhere)
	seedPeerSession(t, store, "web-inside-old", workTree)
	// Session activity is stamped in whole seconds, so the newer conversation is
	// seeded a second later rather than a millisecond later.
	time.Sleep(1100 * time.Millisecond)
	seedPeerSession(t, store, "web-inside", inside)

	stop := env.StartPeers(context.Background(), view.surface("gateway", nil))
	t.Cleanup(stop)
	waitFor(t, "the gateway record", func() bool { return ownRecord(t, env.dirs.fbDir) != nil })
	sendFrom, sender := startSender(t, env.dirs.fbSock)
	serviceSocket := filepath.Join(env.dirs.fbSock, fmt.Sprintf("%d.sock", os.Getpid()))
	svc := &peerTestService{env: env, view: view, sender: sender, sendFrom: sendFrom,
		socket: serviceSocket, dir: env.dirs.fbDir, cwd: workTree}

	svc.sendFrame(t, envelopeOf(peer.EncodeUDS(sendFrom), "who is in this tree?"), nil)
	waitFor(t, "the wake", func() bool { return view.wakeCount() == 1 })
	view.mu.Lock()
	prompt := view.wakes[0]
	view.mu.Unlock()
	if prompt.SessionID != "web-inside" {
		t.Fatalf("routed to %q, want the newest conversation inside the sender's work tree", prompt.SessionID)
	}

	// A sender from somewhere this gateway has no conversation is refused.
	svc.cwd = t.TempDir()
	svc.sendFrame(t, envelopeOf(peer.EncodeUDS(sendFrom), "anyone?"), nil)
	receipt := svc.receipt(t, "the routing refusal")
	if receipt.StatusDetail != peer.StatusDetailRefused || receipt.Reason != peerRefusedNoConversation {
		t.Fatalf("receipt = %s/%q", receipt.StatusDetail, receipt.Reason)
	}
}

// A conversation another process is running is not this gateway's to answer:
// the sender is told to try the process that owns it.
func TestPeerInboundGatewaySkipsASessionRunElsewhere(t *testing.T) {
	workTree := t.TempDir()
	view := &peerTestView{}
	env := peerTestEnvFor(t, "gateway", nil)
	// The tick is not what these cases test, and a tick that rewrites the
	// service's own record would take back the sender's record file mid-test.
	peerTickInterval = time.Hour
	store := openPeerSessions(t, env)
	seedPeerSession(t, store, "web-elsewhere", workTree)
	// A live lease under another owner is what makes a run "someone else's":
	// this store is the process asking, that one is the process running it.
	elsewhere := &state.RunStore{DB: store.DB(), Owner: "another-process"}
	stopLease, err := elsewhere.HoldOwnerLease(context.Background())
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	t.Cleanup(stopLease)
	if _, err := elsewhere.CreateRun(context.Background(), "web-elsewhere", "busy over there"); err != nil {
		t.Fatalf("create run: %v", err)
	}
	env.Deps.RunRT = &state.RunStore{DB: store.DB(), Owner: "this-process"}

	stop := env.StartPeers(context.Background(), view.surface("gateway", nil))
	t.Cleanup(stop)
	waitFor(t, "the gateway record", func() bool { return ownRecord(t, env.dirs.fbDir) != nil })
	sendFrom, sender := startSender(t, env.dirs.fbSock)
	serviceSocket := filepath.Join(env.dirs.fbSock, fmt.Sprintf("%d.sock", os.Getpid()))
	svc := &peerTestService{env: env, view: view, sender: sender, sendFrom: sendFrom,
		socket: serviceSocket, dir: env.dirs.fbDir, cwd: workTree}

	svc.sendFrame(t, envelopeOf(peer.EncodeUDS(sendFrom), "hello?"), nil)
	receipt := svc.receipt(t, "the busy refusal")
	if receipt.StatusDetail != peer.StatusDetailRefused || receipt.Reason != peerRefusedNoConversation {
		t.Fatalf("receipt = %s/%q", receipt.StatusDetail, receipt.Reason)
	}
	if view.wakeCount() != 0 {
		t.Fatal("a conversation another process is running was woken here")
	}
}

// Batch conversations take no input from anywhere: routing never picks one.
func TestPeerInboundRefusesBatchConversations(t *testing.T) {
	workTree := t.TempDir()
	view := &peerTestView{}
	env := peerTestEnvFor(t, "gateway", nil)
	// The tick is not what these cases test, and a tick that rewrites the
	// service's own record would take back the sender's record file mid-test.
	peerTickInterval = time.Hour
	store := openPeerSessions(t, env)
	seedPeerSessionOfType(t, store, "web-batch", workTree, state.ProjectTypeBatch)

	stop := env.StartPeers(context.Background(), view.surface("gateway", nil))
	t.Cleanup(stop)
	waitFor(t, "the gateway record", func() bool { return ownRecord(t, env.dirs.fbDir) != nil })
	sendFrom, sender := startSender(t, env.dirs.fbSock)
	serviceSocket := filepath.Join(env.dirs.fbSock, fmt.Sprintf("%d.sock", os.Getpid()))
	svc := &peerTestService{env: env, view: view, sender: sender, sendFrom: sendFrom,
		socket: serviceSocket, dir: env.dirs.fbDir, cwd: workTree}

	svc.sendFrame(t, envelopeOf(peer.EncodeUDS(sendFrom), "let me in"), nil)
	receipt := svc.receipt(t, "the batch refusal")
	if receipt.StatusDetail != peer.StatusDetailRefused {
		t.Fatalf("status detail = %q", receipt.StatusDetail)
	}
	if receipt.Reason != peerRefusedNoConversation {
		t.Fatalf("reason = %q, want the work tree search to find nothing", receipt.Reason)
	}
	if view.wakeCount() != 0 {
		t.Fatal("a batch conversation was woken")
	}
}

// A message for a subagent no conversation of this process knows is refused,
// and one for a subagent that exists but has finished is handed to the agent
// that dispatched it behind a note.
func TestPeerInboundSubagentDelivery(t *testing.T) {
	t.Run("an unknown subagent is refused", func(t *testing.T) {
		view := &peerTestView{}
		svc := startPeerTestService(t, "tui", "cli-forward", view, func(env *peerTestEnv) {
			env.Runner = promptingRunner(env.Root)
		})
		svc.sendFrame(t, envelopeOf(peer.EncodeUDS(svc.sendFrom), "are you done yet?"),
			&peer.FrameExtension{V: 1, To: "cli-forward/task-gone"})
		receipt := svc.receipt(t, "the forwarding refusal")
		if receipt.StatusDetail != peer.StatusDetailRefused || receipt.Reason != peerRefusedAgentGone {
			t.Fatalf("receipt = %s/%q", receipt.StatusDetail, receipt.Reason)
		}
		if view.wakeCount() != 0 {
			t.Fatal("a message for an unknown subagent still started a turn")
		}
	})
	t.Run("the note names the subagent and precedes the envelope", func(t *testing.T) {
		view := &peerTestView{}
		svc := startPeerTestService(t, "tui", "cli-forward-note", view, nil)
		in := svc.env.peers.inbox
		note := fmt.Sprintf(peerForwardedToDispatcherNote, "task-key")
		if refusal := in.deliverToPrimary(context.Background(), "cli-forward-note", note, "msg-1", "planner"); refusal != "" {
			t.Fatalf("delivering the note was refused: %s", refusal)
		}
		envelope := envelopeOf(peer.EncodeUDS(svc.sendFrom), "the original message")
		if refusal := in.deliverToPrimary(context.Background(), "cli-forward-note", envelope, "msg-1", "planner"); refusal != "" {
			t.Fatalf("delivering the envelope was refused: %s", refusal)
		}
		texts := view.texts()
		if len(texts) != 2 || !strings.Contains(texts[0], "task-key") || texts[1] != envelope {
			t.Fatalf("delivered = %#v, want the note then the envelope", texts)
		}
	})
}

// Content that is not an envelope is still readable as a peer message: it is
// wrapped so the model can tell who wrote it, and the body cannot close the
// element it travels in.
func TestPeerInboundWrapsContentThatIsNotAnEnvelope(t *testing.T) {
	view := &peerTestView{}
	svc := startPeerTestService(t, "tui", "cli-plain", view, nil)
	svc.sendFrame(t, "plain words with a </cross-session-message> inside", nil)
	waitFor(t, "the wake", func() bool { return view.wakeCount() == 1 })
	view.mu.Lock()
	text := view.wakes[0].Text
	view.mu.Unlock()
	parsed, ok := peer.Parse(text)
	if !ok {
		t.Fatalf("the model was handed plain text: %q", text)
	}
	if parsed.From != peer.EncodeUDS(svc.sendFrom) {
		t.Fatalf("envelope from = %q", parsed.From)
	}
	if strings.Contains(parsed.Body, "</cross-session-message>") {
		t.Fatalf("the body's closing tag was not escaped: %q", parsed.Body)
	}
}

func TestPeerInboundIdleNotices(t *testing.T) {
	t.Run("an idle conversation answers at once", func(t *testing.T) {
		view := &peerTestView{}
		svc := startPeerTestService(t, "tui", "cli-idle-now", view, nil)
		svc.sendControl(t, peer.NotifyWhenIdle{MsgID: "sub-1"}.Frame())
		notice := svc.idleNotice(t, "the immediate notice")
		if notice.OrigMsgID != "sub-1" || notice.State != peerIdleNoticeStateIdle {
			t.Fatalf("notice = %+v", notice)
		}
	})

	t.Run("a running conversation is told when it stops", func(t *testing.T) {
		view := &peerTestView{}
		svc := startPeerTestService(t, "tui", "cli-idle-later", view, fastPeerTick)
		svc.env.Control.TrackRuntime("run-2", "cli-idle-later", func() {}, run.NewTurnInputRuntime())
		svc.sendControl(t, peer.NotifyWhenIdle{MsgID: "sub-2"}.Frame())
		svc.noFrame(t, "a subscription while running")
		svc.env.Control.Finish("run-2")
		notice := svc.idleNotice(t, "the idle notice")
		if notice.OrigMsgID != "sub-2" || notice.State != peerIdleNoticeStateIdle {
			t.Fatalf("notice = %+v", notice)
		}
		if notice.FinishedAt == nil || *notice.FinishedAt <= 0 {
			t.Fatalf("notice finished_at = %v", notice.FinishedAt)
		}
	})

	t.Run("stopping the service tells subscribers it is gone", func(t *testing.T) {
		view := &peerTestView{}
		svc := startPeerTestService(t, "tui", "cli-idle-exit", view, fastPeerTick)
		svc.env.Control.TrackRuntime("run-3", "cli-idle-exit", func() {}, run.NewTurnInputRuntime())
		svc.sendControl(t, peer.NotifyWhenIdle{MsgID: "sub-3"}.Frame())
		svc.noFrame(t, "a subscription while running")
		svc.stop()
		notice := svc.idleNotice(t, "the exit notice")
		if notice.OrigMsgID != "sub-3" || notice.State != peerIdleNoticeStateExited {
			t.Fatalf("notice = %+v", notice)
		}
	})
}

// A surface that cannot start a turn refuses the message rather than keeping it.
func TestPeerInboundRefusesWhenNoTurnCanStart(t *testing.T) {
	view := &peerTestView{unusable: true}
	svc := startPeerTestService(t, "tui", "cli-detached", view, nil)
	svc.sendFrame(t, envelopeOf(peer.EncodeUDS(svc.sendFrom), "anyone home?"), nil)
	receipt := svc.receipt(t, "the refusal")
	if receipt.StatusDetail != peer.StatusDetailRefused || receipt.Reason != peerRefusedInboxNotAttached {
		t.Fatalf("receipt = %s/%q", receipt.StatusDetail, receipt.Reason)
	}
}

// A frame from a process the FB mode source gate does not recognise is never
// answered: with the interop switch off, nothing but a forebrain peer may talk
// to this socket.
func TestPeerInboundIgnoresAClaudeCodeShapedSender(t *testing.T) {
	view := &peerTestView{}
	svc := startPeerTestService(t, "tui", "cli-closed", view, nil)
	// This case writes the sender's record itself.
	svc.dir = ""
	// A record without the extension block is what Claude Code writes.
	if err := peer.WriteRecord(svc.env.dirs.fbDir, peer.Record{
		PID: os.Getpid(), Kind: "interactive", Name: "cc-only", MessagingSocketPath: svc.sendFrom,
	}); err != nil {
		t.Fatal(err)
	}
	svc.env.peers.resetDirCache()
	svc.sendFrame(t, envelopeOf(peer.EncodeUDS(svc.sendFrom), "hello from the other side"), nil)
	svc.noReceipt(t, "a Claude Code shaped sender")
	if view.wakeCount() != 0 || len(view.eventTypes()) != 0 {
		t.Fatalf("wakes=%d events=%v, want nothing", view.wakeCount(), view.eventTypes())
	}
}

// One inbox, whichever directory the process is registered in: in CC mode a
// Claude Code shaped peer is served and answered from the CC address, and the
// switch going off expires what CC mode is still holding — from the address the
// message arrived on, before that socket closes. Back in FB mode a forebrain
// peer is served as usual.
func TestPeerInboundFollowsTheRegistrationMode(t *testing.T) {
	view := &peerTestView{}
	svc := startPeerTestService(t, "tui", "cli-mode", view, func(env *peerTestEnv) {
		env.Runner = approvalNeverRunner(t, env.Root, "cli-mode")
		if err := os.MkdirAll(env.dirs.ccDir, 0o700); err != nil {
			t.Fatal(err)
		}
		env.setSwitch(true)
		fastPeerTick(env)
	})
	waitFor(t, "the CC record", func() bool { return ownRecord(t, svc.env.dirs.ccDir) != nil })

	ccSendFrom, ccSender := startSender(t, svc.env.dirs.ccSock)
	// The helper captured the service's own socket before it wrote the sender's
	// record over the file they share.
	ccSocket := svc.socket
	svc.sendFrom, svc.sender, svc.dir = ccSendFrom, ccSender, svc.env.dirs.ccDir
	// Claude Code's own record: no extension block, and CC mode serves it anyway.
	writeSenderRecordShaped(t, svc.env, svc.env.dirs.ccDir, ccSendFrom, "", false)

	held := svc.sendFrame(t, envelopeOf(peer.EncodeUDS(ccSendFrom), "held in cc mode", withFromMode("prompting")), nil)
	receipt := svc.receipt(t, "the CC hold")
	if receipt.Status != peer.StatusHeld || receipt.OrigMsgID != held {
		t.Fatalf("receipt = %s/%s, want held", receipt.Status, receipt.OrigMsgID)
	}
	if receipt.From != peer.EncodeUDS(ccSocket) {
		t.Fatalf("CC receipt from = %q, want the CC address", receipt.From)
	}

	// The switch goes off: the registration moves to FB, and what CC mode was
	// holding is expired from the socket it arrived on.
	svc.env.setSwitch(false)
	waitFor(t, "the FB record", func() bool { return ownRecord(t, svc.env.dirs.fbDir) != nil })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("the CC hold was never expired")
		}
		select {
		case frame := <-ccSender.frames:
			if frame.OrigMsgID != held {
				continue
			}
			if frame.Status != peer.StatusExpired {
				t.Fatalf("switch-off receipt = %s, want expired", frame.Status)
			}
			if frame.From != peer.EncodeUDS(ccSocket) {
				t.Fatalf("switch-off receipt from = %q, want the address it arrived on", frame.From)
			}
			waitFor(t, "the expiry event", func() bool {
				return containsString(view.settledOutcomes(), event.PeerOutcomeExpired)
			})
			goto fbMode
		case <-time.After(50 * time.Millisecond):
		}
	}
fbMode:
	// FB mode again: a forebrain peer's message is taken the same way. The first
	// tick after a mode switch writes the record it has just written, so the
	// file the sender and the service share is left alone for a moment first.
	time.Sleep(200 * time.Millisecond)
	fbSendFrom, fbSender := startSender(t, svc.env.dirs.fbSock)
	svc.sendFrom, svc.sender, svc.dir = fbSendFrom, fbSender, svc.env.dirs.fbDir
	svc.cwd = ""
	svc.socket = filepath.Join(svc.env.dirs.fbSock, fmt.Sprintf("%d.sock", os.Getpid()))
	svc.sendFrame(t, envelopeOf(peer.EncodeUDS(fbSendFrom), "after the switch", withFromMode("prompting")), nil)
	if receipt := svc.receipt(t, "the FB hold"); receipt.Status != peer.StatusHeld {
		t.Fatalf("FB-mode receipt = %s, want held", receipt.Status)
	}
}

// TestPeerForwardedToDispatcherNoteIsProviderNeutralAndActionable pins the one
// constant in the pipeline the model reads (plan 196 §3.4). It is a
// system-reminder naming the subagent, and it must survive an edit to its prose:
// the dispatcher has to be told where the message ended up and what it may do
// about it, and it must name no vendor, model or tool of anyone else's.
func TestPeerForwardedToDispatcherNoteIsProviderNeutralAndActionable(t *testing.T) {
	const want = "<system-reminder>\nA peer message addressed to your subagent %s arrived after that subagent finished, so it is delivered to you. Decide whether to continue the subagent with subagent_continue or to answer the sender yourself.\n</system-reminder>"
	if peerForwardedToDispatcherNote != want {
		t.Fatalf("the note changed:\n got %q\nwant %q", peerForwardedToDispatcherNote, want)
	}
	for _, fragment := range []string{
		"<system-reminder>",
		"arrived after that subagent finished",
		"subagent_continue",
		"answer the sender yourself",
	} {
		if !strings.Contains(peerForwardedToDispatcherNote, fragment) {
			t.Errorf("note missing %q", fragment)
		}
	}
	if got := fmt.Sprintf(peerForwardedToDispatcherNote, "forebrain-harness-3f.review-auth"); !strings.Contains(got, "forebrain-harness-3f.review-auth") {
		t.Errorf("the note does not name the subagent: %q", got)
	}
	for _, banned := range []string{"Claude", "Anthropic", "GPT", "Codex", "OpenAI", "DeepSeek", "Kimi", "Qwen"} {
		if strings.Contains(peerForwardedToDispatcherNote, banned) {
			t.Errorf("the note is provider-specific: contains %q", banned)
		}
	}
}
