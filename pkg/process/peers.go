// The process-level peer service (plan 195): every TUI and gateway process
// registers itself as one peer in its current registration mode, keeps the
// record's status and forebrain extension current, and answers the frames
// that arrive on its socket. The wiring lives here because both surfaces
// share it — the TUI and the gateway each hand StartPeers a small surface and
// nothing else.
//
// Receiving is a placeholder in this plan: every user frame that passes the
// source gate is answered with a refused receipt ("inbox not ready"), the
// one line 196 replaces with routing, admission and delivery.
package process

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/peer"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/google/uuid"
)

// Tunables the tests replace: the tick cadence (20ms in a test) and the
// Claude Code detection function (a counter that never stats anything).
var (
	peerTickInterval = time.Second
	detectClaudeCode = peer.ClaudeCodeDetected
)

// The tick multiples the slower duties run on, and the caches' lifetimes.
const (
	peerCheckTicks     = 5  // registration check and Claude Code detection
	peerCleanTicks     = 60 // dead-record cleanup
	peerDirCacheTTL    = time.Second
	peerBranchCacheTTL = 30 * time.Second
	// peerGatewayWindow is how far back the gateway's agent list reaches.
	peerGatewayWindow = 24 * time.Hour
	// peerMaxAgents caps the extension block's agent list.
	peerMaxAgents = 64
)

// ClaudeCodeDetected reports whether this machine has Claude Code installed:
// its session directory exists and is a directory. Only the stat happens —
// nothing is read, written or created — and the answer is what decides
// whether the surfaces show the peers.claude_code switch at all (owner
// 2026-10-30). pkg/tui and pkg/gateway ask through this function; they do
// not import pkg/peer.
func ClaudeCodeDetected() bool { return peer.ClaudeCodeDetected() }

// PeerSurface is what the peer service needs from the process that runs it.
type PeerSurface struct {
	// Kind is "tui" or "gateway"; it names the surface in the record's
	// extension block and picks the entrypoint string.
	Kind string
	// CurrentSession is the conversation on screen, for the TUI; nil for the
	// gateway, whose fixed process id is generated at start.
	CurrentSession func() string
	// Publish is the surface's run-event funnel, the same one the abandoned-run
	// reaper writes through. It carries the two facts about a peer message that
	// belong in the conversation's log rather than in the transcript: that one
	// is being held for the person's decision, and what became of it.
	Publish func(ctx context.Context, sessionID, runID, typ string, payload any) error
	// Wake starts a turn for a peer prompt in a conversation that is idle. A
	// surface that cannot start one — no attached UI, no turn runtime — returns
	// ErrPeerWakeUnavailable, and the message is refused instead.
	Wake func(ctx context.Context, p PeerPrompt) error
}

// PeerPrompt is a peer message handed to a surface to run as a turn of its own:
// the conversation it belongs to, and the envelope the model reads.
type PeerPrompt struct {
	SessionID string
	Text      string
	// Origin is state.MessageOriginPeer: the turn's prompt row was written on
	// the person's behalf by another session.
	Origin   string
	MsgID    string
	FromName string
}

// ParsePeerEnvelope reads one peer message the way a card needs it: the
// sender's display name and the body the envelope carried. It is the thin
// wrapper a surface uses instead of importing pkg/peer itself, so the live line
// and a replay draw the same card from the same parse. Content that is not
// exactly one envelope reports false, and the caller shows it as plain text —
// which is what the receiving model reads in the same case.
func ParsePeerEnvelope(content string) (fromName, body string, ok bool) {
	envelope, ok := peer.Parse(strings.TrimSpace(content))
	if !ok {
		return "", "", false
	}
	return strings.TrimSpace(envelope.FromName), envelope.Body, true
}

// PeerMark is the identity block a surface draws for one agent: the two
// characters it carries and the tone it is drawn in (plan 200 §3). Both are
// derived here, once, so the same agent reads the same everywhere — the top
// bar, a letter, the drawer, an approval card — and no surface has to hash a
// name itself.
//
// name is the agent's own name; parent is the hosting primary agent's name
// when the agent is a subagent, empty otherwise. A primary agent's mark is the
// first two characters of the last dash-separated segment of its name
// (forebrain-harness-a3 → a3, forebrain-gw-3c1a → 3c). A subagent's is the
// initials of the dash-separated segments of the part after its last dot
// (forebrain-gw-3c1a.review-auth → ra), padded from the first segment when
// there is only one initial to read.
//
// The tone is a stable hash of the *process* agent's name — the parent's for a
// subagent, so a subagent wears its parent's colour — taken modulo the four
// identity tones. FNV-1a is used because it is stable across processes and
// platforms, which is the whole requirement: nothing about the mapping is a
// secret, it only has to agree everywhere.
func PeerMark(name, parent string) (mark string, tone int) {
	name = strings.TrimSpace(name)
	parent = strings.TrimSpace(parent)
	toneName := name
	if parent != "" {
		toneName = parent
	}
	if toneName == "" {
		toneName = name
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(toneName))
	tone = int(h.Sum32() % peerToneCount)
	if parent != "" {
		return peerSubagentMark(name), tone
	}
	return peerNameMark(name), tone
}

// peerToneCount is how many identity tones there are; the palette itself is
// the surface's, indexed by the tone a mark carries.
const peerToneCount = 4

// peerNameMark is a primary agent's two characters: the first two of the last
// dash-separated segment of its name.
func peerNameMark(name string) string {
	segments := strings.Split(name, "-")
	for i := len(segments) - 1; i >= 0; i-- {
		if segment := strings.TrimSpace(segments[i]); segment != "" {
			return firstRunes(segment, 2)
		}
	}
	return firstRunes(name, 2)
}

// peerSubagentMark is a subagent's two characters: the initial of each
// dash-separated segment of the name after its last dot, padded from the first
// segment when that yields a single character.
func peerSubagentMark(name string) string {
	base := name
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		base = name[dot+1:]
	}
	segments := strings.Split(base, "-")
	out := make([]rune, 0, 2)
	for _, segment := range segments {
		for _, r := range segment {
			out = append(out, r)
			break
		}
		if len(out) == 2 {
			return string(out)
		}
	}
	if len(out) == 1 {
		// One initial to read: the block still carries two characters, so the
		// second comes after it in the same segment.
		if runes := []rune(segments[0]); len(runes) > 1 {
			out = append(out, runes[1])
		}
	}
	return string(out)
}

// firstRunes is the first n runes of s, or all of them when s is shorter.
func firstRunes(s string, n int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) > n {
		runes = runes[:n]
	}
	return string(runes)
}

// peerService is the running registration: one mode, one socket, one record.
type peerService struct {
	env     *Environment
	surface PeerSurface

	ctx    context.Context
	cancel context.CancelFunc
	// interval is the tick cadence frozen at start, so a test that restores
	// the package variable cannot race the loop's first read.
	interval time.Duration

	hopKey    []byte
	gatewayID string

	startedAt    int64  // ms, the record's StartedAt
	procStart    string // this process's start time, read once
	entrypoint   string
	statusAt     int64
	lastStatus   string
	lastSig      []byte // the record content last written (UpdatedAt zeroed)
	ownAddresses map[string]bool

	// mu guards everything below; the tick loop is the only writer, and the
	// frame handler plus stop read under it.
	mu      sync.Mutex
	running bool
	// modeEntered says the initial registration was entered. The loop runs
	// before it is, so a first attempt that fails is retried instead of
	// leaving this process invisible for its whole life (plan 237 §3).
	modeEntered bool
	// enterWarned makes the retry cost one warning: after the first failure the
	// cadence is a debug line.
	enterWarned bool
	ccMode      bool
	layout     peer.Layout
	server     *peer.Server
	token      string
	socketPath string
	addr       string
	name       string
	nameSource string
	nameSince  int64
	switchOn   bool
	detected   bool
	sinceCheck int
	ticks      int
	branch     map[string]peerBranchEntry

	// dirMu guards the directory listing cache: the frame handler reads it
	// off the tick loop's lock, which the tick loop itself holds.
	dirMu      sync.Mutex
	dirCache   []peer.Record
	dirCacheAt time.Time

	stopOnce sync.Once
	// inbox is the receiving half (plan 196): admission, hold, routing,
	// delivery and receipts. It keeps its own lock, so the frame handler and
	// the tick loop hand it snapshots instead of sharing this one.
	inbox *peerInbox
	// onTeardown runs when the registration is torn down for a reason other
	// than this process exiting normally — and on exit too, with its own
	// reason: what happens to the messages being held and to the subscriptions
	// standing depends on which it is, and both must be answered while the old
	// socket is still open (plan 196 §3.2).
	onTeardown func(reason peerInboxTeardown)
}

// peerBranchEntry is one cached branch name, keyed by work tree root.
type peerBranchEntry struct {
	branch string
	at     time.Time
}

// StartPeers registers this process as a local peer and keeps the
// registration current until the returned stop runs (or the context ends, or
// the environment closes). Cross-process coordination is always on (owner
// 2026-10-31): the only reason this does nothing is a platform the protocol
// does not implement, or an unisolated test binary — peer.Enabled's own
// guard. Failures are logged, never returned: the peer service must not be
// able to keep a TUI or a gateway from starting.
func (env *Environment) StartPeers(ctx context.Context, s PeerSurface) (stop func()) {
	// The tools' port onto the other processes' agents (plan 197), installed
	// before the service decides whether it can run: the pair is always on
	// (owner 2026-10-31), and the tool table sits in the prompt prefix, so the
	// port may not follow the registration's life — a call made while the
	// service is between modes answers that messaging is not available.
	if env != nil {
		env.Deps.Peers = env.peerDirectory()
	}
	svc := &peerService{
		env:          env,
		surface:      s,
		hopKey:       peer.NewHopKey(),
		gatewayID:    uuid.NewString(),
		startedAt:    time.Now().UnixMilli(),
		ownAddresses: map[string]bool{},
		branch:       map[string]peerBranchEntry{},
	}
	svc.procStart, _ = peer.ProcStart(os.Getpid())
	svc.inbox = newPeerInbox(svc, s)
	// The receipts a peer sends about a message this process sent are what the
	// sending conversation hears back (plan 197 §3.6): they go to this
	// process's outbound table, which aggregates them into one note.
	if port := env.outbound(); port != nil {
		svc.inbox.onReceipt = port.tracker.receipt
	}
	svc.onTeardown = svc.inbox.teardown
	switch strings.TrimSpace(s.Kind) {
	case "gateway":
		svc.entrypoint = "forebrain-gateway"
	default:
		svc.entrypoint = "forebrain-tui"
	}
	stop = func() { svc.stop() }
	if env == nil || !peer.Enabled() {
		return stop
	}
	if err := svc.start(ctx); err != nil {
		slog.Warn("peer service did not start; this process is invisible to other local peers", "err", err)
		return stop
	}
	env.setPeerService(svc)
	return stop
}

// setPeerService records the running service so Close can stop it; the
// surface's own stop is the same once-guarded call, so double stopping is
// safe.
func (env *Environment) setPeerService(svc *peerService) {
	env.reloadMu.Lock()
	env.peers = svc
	env.reloadMu.Unlock()
}

// peersConfigSnapshot reads the configuration snapshot in force — the one
// the per-turn sync keeps current — under the lock that applies it.
func (env *Environment) peersConfigSnapshot() (cfg peerConfigView) {
	env.reloadMu.Lock()
	defer env.reloadMu.Unlock()
	if env.RawConfig.Peers.ClaudeCode != nil {
		cfg.on = *env.RawConfig.Peers.ClaudeCode
		cfg.set = true
	}
	return cfg
}

// peerConfigView is the peers section as the service reads it each tick.
type peerConfigView struct {
	set bool
	on  bool
}

func (v peerConfigView) enabled() bool { return v.set && v.on }

// start enters the initial mode and starts the tick loop. The loop starts
// before the first attempt, so an attempt that fails is retried on the tick
// cadence rather than leaving this process unregistered until it exits (plan
// 237 §3); the error is still reported, because being invisible is news.
func (svc *peerService) start(ctx context.Context) error {
	svc.ctx, svc.cancel = context.WithCancel(context.WithoutCancel(ctx))
	svc.interval = peerTickInterval
	switchOn := svc.env.peersConfigSnapshot().enabled()
	target := false
	if switchOn {
		svc.mu.Lock()
		svc.switchOn = true
		svc.detected = detectClaudeCode()
		svc.sinceCheck = 0
		target = svc.detected
		svc.mu.Unlock()
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.running = true
	go svc.tickLoop()
	if err := svc.enterModeLocked(target, false); err != nil {
		return err
	}
	svc.modeEntered = true
	return nil
}

// tickLoop is the service's heartbeat: every peerTickInterval it recomputes
// the target mode and the record.
func (svc *peerService) tickLoop() {
	ticker := time.NewTicker(svc.interval)
	defer ticker.Stop()
	for {
		select {
		case <-svc.ctx.Done():
			return
		case <-ticker.C:
			svc.tick()
		}
	}
}

// tick is one beat: mode first, then the record, then the slower duties.
func (svc *peerService) tick() {
	switchOn := svc.env.peersConfigSnapshot().enabled()
	target := svc.targetMode(switchOn)
	// Before this tick's own lock: the receiving half answers peers over
	// sockets, and a peer that is slow or gone must not hold the registration's
	// lock while it does.
	svc.inbox.tick(svc.ctx)

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if !svc.running {
		return
	}
	if !svc.modeEntered {
		svc.enterPendingModeLocked(target)
		return
	}
	svc.ticks++
	if target != svc.ccMode {
		if err := svc.switchModeLocked(target); err != nil {
			slog.Warn("peer registration mode switch failed; retrying next tick", "err", err, "target_cc", target)
		}
		// The switch is this tick's whole job (plan 195 §3.4).
		return
	}
	svc.refreshRecordLocked()
	if svc.ticks%peerCheckTicks == 0 {
		svc.checkRegistrationLocked()
	}
	if svc.ticks%peerCleanTicks == 0 {
		svc.cleanDeadRecordsLocked()
	}
}

// enterPendingModeLocked retries the registration the first attempt could not
// enter. Until it succeeds the loop has no other job: there is no socket, no
// record and no mode to refresh (plan 237 §3).
func (svc *peerService) enterPendingModeLocked(target bool) {
	if err := svc.enterModeLocked(target, false); err != nil {
		// One warning names the reason; the retries after it are debug lines, so
		// a directory that stays unusable does not fill the log.
		if !svc.enterWarned {
			svc.enterWarned = true
			slog.Warn("peer registration could not be entered; retrying every tick", "err", err, "target_cc", target)
		} else {
			slog.Debug("peer registration retry failed", "err", err, "target_cc", target)
		}
		return
	}
	svc.modeEntered = true
}

// targetMode computes the registration mode the switch and the machine
// justify. The switch off means FB mode without ever statting Claude Code's
// directory; on, the directory is looked at the tick the switch turned on and
// then once every peerCheckTicks ticks.
func (svc *peerService) targetMode(switchOn bool) bool {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	justOn := switchOn && !svc.switchOn
	svc.switchOn = switchOn
	if !switchOn {
		svc.detected = false
		svc.sinceCheck = 0
		return false
	}
	if justOn || svc.sinceCheck >= peerCheckTicks {
		svc.detected = detectClaudeCode()
		svc.sinceCheck = 0
	} else {
		svc.sinceCheck++
	}
	return svc.detected
}

// enterModeLocked sets the registration up in one mode: layout, socket,
// server, name, record and key. keepName carries an already-derived name
// across a mode switch; the name only rolls again when the new directory
// holds a live peer already using it. A failure undoes everything this call
// did, so the next tick's retry starts from a clean slate rather than a
// half-listening process.
func (svc *peerService) enterModeLocked(cc bool, keepName bool) error {
	layout := peer.NewLayout(cc)
	if err := layout.Prepare(); err != nil {
		return fmt.Errorf("prepare %s: %w", layout.SocketDir, err)
	}
	server := &peer.Server{Path: layout.SocketPath(os.Getpid()), Token: peer.NewToken(), Handler: svc}
	if err := server.Start(svc.ctx); err != nil {
		return fmt.Errorf("listen %s: %w", server.Path, err)
	}
	svc.layout = layout
	svc.server = server
	svc.token = server.Token
	svc.socketPath = server.Path
	svc.addr = peer.EncodeUDS(server.Path)
	svc.ownAddresses[svc.addr] = true
	// The receiving half answers on this socket from here on: its receipts and
	// idle notices are sent from the address they belong to, and after a mode
	// switch the old one is what the messages still held arrived on.
	svc.inbox.remember(server.Path, svc.addr)
	svc.resetDirCache()

	switch {
	case !keepName || svc.name == "":
		svc.name = peer.DeriveName(svc.env.LaunchDir, svc.nameTakenLocked)
		svc.nameSource = "derived"
		svc.nameSince = time.Now().UnixMilli()
	case svc.nameTakenLocked(peer.NormalizeName(svc.name)):
		svc.name = peer.DeriveName(svc.env.LaunchDir, svc.nameTakenLocked)
		svc.nameSource = "collision"
		svc.nameSince = time.Now().UnixMilli()
	}

	if err := svc.writeRegistrationLocked(); err != nil {
		// Undo the listening half so a retry cannot stack a second socket.
		svc.teardownSocketLocked()
		return err
	}
	svc.ccMode = cc
	svc.statusAt = svc.startedAt
	svc.lastStatus = ""
	svc.lastSig = nil
	return nil
}

// teardownSocketLocked closes the server (which removes the socket file) and
// clears the listening half of the state.
func (svc *peerService) teardownSocketLocked() {
	if svc.server != nil {
		_ = svc.server.Close()
		svc.server = nil
	}
	if svc.socketPath != "" {
		_ = os.Remove(svc.socketPath)
	}
	svc.socketPath = ""
	svc.addr = ""
}

// switchModeLocked moves the whole registration to the other mode, in the
// order plan 195 §3.4 fixes: hook, old registration out, old socket closed,
// new mode entered, name kept unless taken, new registration written.
func (svc *peerService) switchModeLocked(target bool) error {
	if svc.onTeardown != nil {
		svc.onTeardown(peerTeardownModeSwitch)
	}
	oldDir := svc.layout.Dir
	svc.removeOwnFiles(oldDir)
	svc.teardownSocketLocked()
	return svc.enterModeLocked(target, true)
}

// writeRegistrationLocked writes the record and the key of the current
// registration.
func (svc *peerService) writeRegistrationLocked() error {
	rec := svc.buildRecordLocked()
	rec.UpdatedAt = time.Now().UnixMilli()
	if err := peer.WriteRecord(svc.layout.Dir, rec); err != nil {
		return fmt.Errorf("write record in %s: %w", svc.layout.Dir, err)
	}
	if err := peer.WriteKey(svc.layout.Dir, os.Getpid(), svc.socketPath, svc.token); err != nil {
		return fmt.Errorf("write key in %s: %w", svc.layout.Dir, err)
	}
	sig, _ := json.Marshal(rec)
	sig = clearUpdatedAt(sig)
	svc.lastSig = sig
	return nil
}

// refreshRecordLocked recomputes the record and rewrites it only when its
// content changed: Claude Code's liveness test does not look at updatedAt, so
// an unchanged registration costs no disk write.
func (svc *peerService) refreshRecordLocked() {
	rec := svc.buildRecordLocked()
	sig, err := json.Marshal(rec)
	if err != nil {
		return
	}
	sig = clearUpdatedAt(sig)
	if string(sig) == string(svc.lastSig) {
		return
	}
	rec.UpdatedAt = time.Now().UnixMilli()
	if err := peer.WriteRecord(svc.layout.Dir, rec); err != nil {
		slog.Warn("peer record write failed", "dir", svc.layout.Dir, "err", err)
		return
	}
	svc.lastSig = sig
}

// clearUpdatedAt zeroes the updatedAt field inside a marshaled record, so the
// change comparison ignores the one field that moves with every write.
func clearUpdatedAt(sig []byte) []byte {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(sig, &doc); err != nil {
		return sig
	}
	doc["updatedAt"] = json.RawMessage("0")
	out, err := json.Marshal(doc)
	if err != nil {
		return sig
	}
	return out
}

// checkRegistrationLocked rewrites the registration when the directory no
// longer holds it — a tmp cleaner, or Claude Code wiping its own directory.
// A CC directory that is gone is not recreated here; the next detection falls
// back to FB mode instead.
func (svc *peerService) checkRegistrationLocked() {
	dir := svc.layout.Dir
	if _, err := os.Stat(dir); err != nil {
		return
	}
	recordPath := filepath.Join(dir, fmt.Sprintf("%d.json", os.Getpid()))
	if _, err := os.Stat(recordPath); err == nil && svc.ownKeyPresentLocked(dir) {
		return
	}
	if err := svc.writeRegistrationLocked(); err != nil {
		slog.Warn("peer registration rewrite failed", "dir", dir, "err", err)
	}
}

// ownKeyPresentLocked reports whether the directory holds this pid's key
// file, whatever socket path it hashed.
func (svc *peerService) ownKeyPresentLocked(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	prefix := fmt.Sprintf("%d.", os.Getpid())
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), ".key") {
			return true
		}
	}
	return false
}

// peerKeyPattern matches a pid's key files across socket paths.
var peerKeyPattern = regexp.MustCompile(`^(\d+)\.[0-9a-f]{64}\.key$`)

// removeOwnFiles removes this pid's record and key files from dir, tolerating
// a directory that is already gone.
func (svc *peerService) removeOwnFiles(dir string) {
	if dir == "" {
		return
	}
	pid := fmt.Sprintf("%d", os.Getpid())
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Name() != pid+".json" {
			if match := peerKeyPattern.FindStringSubmatch(entry.Name()); match == nil || match[1] != pid {
				continue
			}
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}

// cleanDeadRecordsLocked removes the dead records this package owns — the
// ones carrying the forebrain extension block — from the current mode's
// directory only. Claude Code's own records are Claude Code's to clean.
func (svc *peerService) cleanDeadRecordsLocked() {
	dir := svc.layout.Dir
	for _, rec := range peer.ReadRecords(dir) {
		if rec.Forebrain == nil || peer.Alive(rec) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, fmt.Sprintf("%d.json", rec.PID)))
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		pid := fmt.Sprintf("%d", rec.PID)
		for _, entry := range entries {
			if match := peerKeyPattern.FindStringSubmatch(entry.Name()); match != nil && match[1] == pid {
				_ = os.Remove(filepath.Join(dir, entry.Name()))
			}
		}
	}
	svc.resetDirCache()
}

// stop tears the registration down: tick loop first, then the mode-switch
// hook, the server and socket, and this pid's files in the current
// directory. Safe to call from the surface and from Close. A process whose
// first mode attempt failed still has a loop to cancel, because running is set
// before the attempt (plan 237 §3).
func (svc *peerService) stop() {
	svc.stopOnce.Do(func() {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		if !svc.running {
			return
		}
		svc.running = false
		if svc.cancel != nil {
			svc.cancel()
		}
		if svc.onTeardown != nil {
			svc.onTeardown(peerTeardownStopped)
		}
		dir := svc.layout.Dir
		svc.teardownSocketLocked()
		svc.removeOwnFiles(dir)
	})
}

// HandleFrame is this process's inbox: 195's source gate first, then the
// receiving pipeline (plan 196), which admits, routes, holds, delivers and
// answers.
//
// The gate is what makes the two registration modes one pipe with two doors. In
// FB mode a frame is served only when its sender is a live forebrain record
// whose socket is the address the frame claims; anyone else's connection is not
// answered at all, because in that mode nothing but forebrain talks to us. In CC
// mode the senders are Claude Code sessions too, so the sender's record is
// looked up by the pid the connection proved, and its features are what a reply
// may rely on.
func (svc *peerService) HandleFrame(ctx context.Context, in peer.Inbound) {
	svc.mu.Lock()
	local := svc.socketPath
	cc := svc.ccMode
	dir := svc.layout.Dir
	svc.mu.Unlock()

	// A frame on a socket this service no longer listens on belongs to the mode
	// it just left.
	if in.Local != local {
		slog.Debug("peer frame on a retired socket ignored", "local", in.Local)
		return
	}
	if in.Frame.Type != peer.FrameTypeUser && in.Frame.Type != peer.FrameTypeControl {
		slog.Debug("peer frame of an unknown type ignored", "type", in.Frame.Type)
		return
	}

	var features []string
	if cc {
		if rec := svc.recordByPID(dir, in.PeerPID); rec != nil {
			features = rec.PeerFeatures
		}
	} else {
		rec := svc.gateRecord(dir, in)
		if rec == nil {
			slog.Debug("peer frame from a non-forebrain process ignored", "peer_pid", in.PeerPID)
			return
		}
		features = rec.PeerFeatures
	}
	if scheme, target := peer.DecodeAddress(in.Frame.From); scheme == "uds" && target != "" {
		if !peer.ReplyTargetAllowed(target, local, os.Getuid(), features) {
			// The frame's sender can never be answered where it asks, so the
			// message is not taken either: a message whose fate cannot be
			// reported would leave its sender waiting forever.
			slog.Debug("peer frame's reply target is not allowed", "from", in.Frame.From)
			return
		}
	}
	svc.inbox.handle(ctx, peerArrival{
		Frame:        in.Frame,
		PeerPID:      in.PeerPID,
		Local:        in.Local,
		Dir:          dir,
		PeerFeatures: features,
	})
}

// gateRecord is the FB-mode source gate: the frame's sender must be a record
// in the FB directory that carries the forebrain extension block, belongs to
// the connected pid, and names the socket the frame says it came from.
func (svc *peerService) gateRecord(dir string, in peer.Inbound) *peer.Record {
	records := svc.cachedRecords(dir)
	for i := range records {
		rec := records[i]
		if rec.PID != in.PeerPID || rec.Forebrain == nil {
			continue
		}
		if peer.EncodeUDS(rec.MessagingSocketPath) == in.Frame.From {
			return &rec
		}
	}
	return nil
}

// recordByPID finds the connected pid's record in dir, whatever process
// wrote it — the CC-mode lookup the receipt's features come from.
func (svc *peerService) recordByPID(dir string, pid int) *peer.Record {
	records := svc.cachedRecords(dir)
	for i := range records {
		if records[i].PID == pid {
			rec := records[i]
			return &rec
		}
	}
	return nil
}

// resetDirCache drops the listing cache — a mode switch changed the
// directory it describes.
func (svc *peerService) resetDirCache() {
	svc.dirMu.Lock()
	svc.dirCache, svc.dirCacheAt = nil, time.Time{}
	svc.dirMu.Unlock()
}

// cachedRecords lists a rendezvous directory at most once per TTL: the gate
// runs on every frame, and the directory is shared state a listing should
// not re-read for each one.
func (svc *peerService) cachedRecords(dir string) []peer.Record {
	svc.dirMu.Lock()
	defer svc.dirMu.Unlock()
	now := time.Now()
	if svc.dirCache != nil && now.Sub(svc.dirCacheAt) < peerDirCacheTTL {
		return svc.dirCache
	}
	records := peer.ReadRecords(dir)
	svc.dirCache, svc.dirCacheAt = records, now
	return records
}

// nameTakenLocked reports whether a normalized name is in use by a live
// record in the current directory, this process's own record excluded.
func (svc *peerService) nameTakenLocked(normalized string) bool {
	for _, rec := range peer.ReadRecords(svc.layout.Dir) {
		if rec.PID == os.Getpid() {
			continue
		}
		if !peer.Alive(rec) {
			continue
		}
		if peer.NormalizeName(rec.Name) == normalized {
			return true
		}
	}
	return false
}

// buildRecordLocked assembles the registration record with the extension
// block, UpdatedAt left zero (the caller stamps it on write).
func (svc *peerService) buildRecordLocked() peer.Record {
	status, agents := svc.statusAndAgentsLocked()
	if status != svc.lastStatus {
		svc.statusAt = time.Now().UnixMilli()
		svc.lastStatus = status
	}
	return peer.Record{
		PID:                 os.Getpid(),
		SessionID:           svc.sessionID(),
		Cwd:                 svc.env.LaunchDir,
		StartedAt:           svc.startedAt,
		ProcStart:           svc.procStart,
		Version:             "forebrain " + AppVersion(),
		PeerProtocol:        1,
		PeerFeatures:        []string{"reply_across_default_dirs"},
		Kind:                "interactive",
		Entrypoint:          svc.entrypoint,
		PIDDomain:           peer.PIDDomain(),
		MessagingSocketPath: svc.socketPath,
		Name:                svc.name,
		NameSource:          svc.nameSource,
		NameSince:           svc.nameSince,
		Status:              status,
		StatusUpdatedAt:     svc.statusAt,
		Forebrain:           &peer.Extension{V: 1, Surface: svc.surface.Kind, Agents: agents},
	}
}

// sessionID is the record's sessionId: the conversation on screen for the
// TUI, the fixed process id for the gateway.
func (svc *peerService) sessionID() string {
	if svc.surface.Kind == "gateway" {
		return svc.gatewayID
	}
	if svc.surface.CurrentSession == nil {
		return ""
	}
	return strings.TrimSpace(svc.surface.CurrentSession())
}

// statusAndAgentsLocked computes the process status and the extension
// block's agent list. The TUI lists the conversation on screen (and its live
// subagents); the gateway lists its recently active sessions, each with
// theirs.
func (svc *peerService) statusAndAgentsLocked() (string, []peer.ExtAgent) {
	if svc.env == nil {
		return peer.StatusIdle, nil
	}
	ctx := svc.ctx
	if svc.surface.Kind == "gateway" {
		return svc.gatewayStatusLocked(ctx)
	}
	sid := svc.sessionID()
	if sid == "" {
		return peer.StatusIdle, nil
	}
	root := svc.env.LaunchDir
	status := svc.sessionStatus(sid)
	primary := peer.ExtAgent{
		ID:     sid,
		Name:   svc.name,
		Kind:   "primary",
		Root:   root,
		Branch: svc.branchFor(root),
		Status: status,
		Task:   svc.sessionTask(sid),
		Mode:   svc.modeFor(ctx, sid),
		Since:  svc.startedAt,
	}
	return status, append([]peer.ExtAgent{primary}, svc.subagentsFor(ctx, sid, svc.name, root)...)
}

// sessionStatus is one session's status as the record reports it: busy while
// its run is live, waiting while it is parked on an approval, idle
// otherwise.
func (svc *peerService) sessionStatus(sid string) string {
	if svc.env.Control == nil {
		return peer.StatusIdle
	}
	if _, phase, ok := svc.env.Control.Find(sid); ok {
		switch phase {
		case run.Running:
			return peer.StatusBusy
		case run.Waiting:
			return peer.StatusWaiting
		}
	}
	return peer.StatusIdle
}

// sessionTask is the session's live primary input — the one line that says
// what it is doing — or empty when it is idle.
func (svc *peerService) sessionTask(sid string) string {
	if svc.env.Deps.RunRT == nil {
		return ""
	}
	task, err := svc.env.Deps.RunRT.LivePrimaryInput(context.Background(), sid)
	if err != nil {
		return ""
	}
	return task
}

// gatewayStatusLocked lists the gateway's recently active sessions, capped
// at peerMaxAgents, and reports busy when any of them is running.
func (svc *peerService) gatewayStatusLocked(ctx context.Context) (string, []peer.ExtAgent) {
	store := svc.env.Deps.SessionStore
	if store == nil {
		return peer.StatusIdle, nil
	}
	sessions, err := store.RecentlyActiveSessions(ctx, time.Now().Add(-peerGatewayWindow), peerMaxAgents)
	if err != nil {
		slog.Debug("peer agent list could not read sessions", "err", err)
		return peer.StatusIdle, nil
	}
	agents := make([]peer.ExtAgent, 0, len(sessions))
	status := peer.StatusIdle
	names := map[string]bool{}
	for _, session := range sessions {
		root := session.Cwd
		name := peerAgentName(root, session.ID)
		for suffix := 2; names[name]; suffix++ {
			name = fmt.Sprintf("%s-%d", name, suffix)
		}
		names[name] = true
		primary := peer.ExtAgent{
			ID:     session.ID,
			Name:   name,
			Kind:   "primary",
			Root:   root,
			Branch: svc.branchFor(root),
			Status: svc.sessionStatus(session.ID),
			Task:   svc.sessionTask(session.ID),
			Mode:   svc.modeFor(ctx, session.ID),
			Since:  session.UpdatedAt * 1000,
		}
		agents = append(agents, primary)
		agents = append(agents, svc.subagentsFor(ctx, session.ID, name, root)...)
		if len(agents) >= peerMaxAgents {
			break
		}
	}
	if status == peer.StatusIdle {
		for _, agent := range agents {
			if agent.Status == peer.StatusBusy {
				status = peer.StatusBusy
			}
		}
	}
	return status, agents
}

// subagentsFor lists one session's live subagents, named after the session's
// own primary agent. A subagent is always busy.
func (svc *peerService) subagentsFor(ctx context.Context, sid, primaryName, root string) []peer.ExtAgent {
	runner, ok := svc.env.BoundRunnerForSession(ctx, sid)
	if !ok || runner == nil {
		return nil
	}
	live := run.LiveSubagents(runner, sid)
	if len(live) == 0 {
		return nil
	}
	mode := svc.modeFor(ctx, sid)
	agents := make([]peer.ExtAgent, 0, len(live))
	names := map[string]bool{}
	for _, sub := range live {
		name := primaryName + "." + peerSlug(sub.Title)
		for suffix := 2; names[name]; suffix++ {
			name = fmt.Sprintf("%s-%d", name, suffix)
		}
		names[name] = true
		agents = append(agents, peer.ExtAgent{
			ID:     sid + "/" + sub.Key,
			Name:   name,
			Kind:   "subagent",
			Parent: sid,
			Root:   root,
			Status: peer.StatusBusy,
			Task:   sub.Title,
			Mode:   mode,
			Since:  sub.StartedAt.UnixMilli(),
		})
	}
	return agents
}

// modeFor is the session's peer mode: bypass when its approval policy never
// asks, prompting otherwise. A session with no runner here is prompting —
// the safer assumption for a sender to see.
func (svc *peerService) modeFor(ctx context.Context, sid string) string {
	runner, ok := svc.env.BoundRunnerForSession(ctx, sid)
	if !ok || runner == nil {
		return "prompting"
	}
	if runner.PermissionSnapshotForSession(sid).ApprovalPolicy.Mode == safety.ApprovalNever {
		return "bypass"
	}
	return "prompting"
}

// branchFor reads the work tree's branch in-process — never by running git —
// and caches the answer per root for peerBranchCacheTTL. Any failure reads
// as empty.
func (svc *peerService) branchFor(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	now := time.Now()
	if entry, ok := svc.branch[root]; ok && now.Sub(entry.at) < peerBranchCacheTTL {
		return entry.branch
	}
	branch := readGitBranch(root)
	svc.branch[root] = peerBranchEntry{branch: branch, at: now}
	return branch
}

// readGitBranch walks up from root looking for .git: a directory whose HEAD
// names refs/heads/<name>, or a worktree's .git file pointing at the real
// gitdir (its path relative to the file when it is not absolute).
func readGitBranch(root string) string {
	dir := strings.TrimSpace(root)
	if dir == "" {
		return ""
	}
	for {
		dot := filepath.Join(dir, ".git")
		info, err := os.Stat(dot)
		if err == nil {
			head := ""
			if info.IsDir() {
				head = readHEADFile(dot)
			} else {
				head = headThroughGitdirFile(dot)
			}
			if branch := branchFromHEAD(head); branch != "" {
				return branch
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// readHEADFile reads <dir>/HEAD, tolerating anything wrong with it.
func readHEADFile(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "HEAD"))
	if err != nil {
		return ""
	}
	return string(data)
}

// headThroughGitdirFile follows a worktree's .git file to the gitdir whose
// HEAD it stands for.
func headThroughGitdirFile(dotFile string) string {
	data, err := os.ReadFile(dotFile)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	target, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return ""
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(dotFile), target)
	}
	return readHEADFile(target)
}

// branchFromHEAD reads a branch name out of a HEAD file: "ref:
// refs/heads/<name>" is the name, anything else is a detached head reported
// by its first seven characters.
func branchFromHEAD(head string) string {
	head = strings.TrimSpace(head)
	if rest, ok := strings.CutPrefix(head, "ref: refs/heads/"); ok {
		name := strings.TrimSpace(rest)
		if name != "" {
			return name
		}
	}
	if head == "" {
		return ""
	}
	if len(head) > 7 {
		return head[:7]
	}
	return head
}

// peerAgentName is a gateway session's agent name: the directory's slug, a
// dash, and the session id's first four characters after any surface prefix.
func peerAgentName(root, sessionID string) string {
	return peerSlug(filepath.Base(strings.TrimSpace(root))) + "-" + sessionTag(sessionID)
}

// sessionTag strips the surface prefixes a session id may carry and keeps
// its first four characters.
func sessionTag(sessionID string) string {
	id := strings.TrimSpace(sessionID)
	for _, prefix := range []string{"cli-", "web-"} {
		if rest, ok := strings.CutPrefix(id, prefix); ok {
			id = rest
			break
		}
	}
	runes := []rune(id)
	if len(runes) > 4 {
		runes = runes[:4]
	}
	if len(runes) == 0 {
		return "0000"
	}
	return string(runes)
}

// peerSlug folds a name fragment the way the peer protocol's own slugs do:
// letters, digits and a few separators survive, everything else folds into a
// dash, and an empty result is "forebrain".
func peerSlug(s string) string {
	var b strings.Builder
	dashed := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
			dashed = false
		default:
			if !dashed {
				b.WriteRune('-')
				dashed = true
			}
		}
	}
	out := strings.Trim(b.String(), "._-")
	if len([]rune(out)) > 20 {
		out = string([]rune(out)[:20])
		out = strings.Trim(out, "._-")
	}
	if out == "" {
		return "forebrain"
	}
	return out
}
