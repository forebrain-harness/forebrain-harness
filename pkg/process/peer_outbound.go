// Sending: what peer_send does when the name belongs to an agent in another
// process, and what becomes of the receipts that come back.
//
// The two tools live in pkg/run and cannot import this package or pkg/peer, so
// everything they need from the protocol arrives through run.PeerDirectory,
// which this file implements. The port is created once per process and reads
// the registration this process runs *now*: a tool table built before the
// service started stays usable, and a call made while the service is between
// modes (or stopped) answers that messaging is not available rather than
// reaching into a half-torn-down registration.
package process

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/peer"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// peerReceiptDebounce is how long a batch of receipts waits for its siblings:
// one message per burst reaches the sending model instead of one per receipt
// (Claude Code's own aggregation, design §3.11).
var peerReceiptDebounce = 750 * time.Millisecond

// The tunables of the sending half.
const (
	// peerSentLimit is how many outbound messages this process remembers for
	// its receipts (design §3.11).
	peerSentLimit = 200
	// peerActiveWindow bounds the "active N ago" a listing row prints: past it
	// the peer is simply quiet, and the number stops meaning anything.
	peerActiveWindow = 7 * 24 * time.Hour
	// peerRecentRowScan is how many recent rows of a conversation the hop
	// chain's inheritance looks back through.
	peerRecentRowScan = 20
)

// errPeerMessagingUnavailable is what a tool call answers when this process is
// not registered as a peer right now: the platform has no protocol, or the
// service is between modes. The tools stay in the table either way — the
// prompt prefix may not change — so this is a refusal, not a hidden tool.
var errPeerMessagingUnavailable = errors.New("Peer messaging is not available right now.")

// The kinds a listing row reports (plan 197 §3.3). They say what sort of
// session the agent belongs to, which is what a sender needs to judge whether
// the message is worth sending.
const (
	peerKindTerminal  = "terminal"
	peerKindGateway   = "gateway conversation"
	peerKindSubagent  = "subagent in another process"
	peerKindAgentSess = "agent session"
	peerRefHashDomain = "peer"
)

// peerRefPattern is a name with its ref beside it, the way a listing prints an
// ambiguous one.
var peerRefPattern = regexp.MustCompile(`^(.*\S)\s*\[([0-9a-f]{6,12})\]$`)

// peerOutbound is the tools' port onto this process's peer service.
type peerOutbound struct {
	env     *Environment
	pacer   *peer.Pacer
	tracker *peerTracker

	// mu guards warnedUnavailable: the same-tree guard asks on every command,
	// and a directory that stays unusable must cost one warning, not one per
	// command (plan 237 §4).
	mu                sync.Mutex
	warnedUnavailable bool
}

// warnUnavailableOnce names the reason the guard had to answer from the
// rendezvous directory, once per process.
func (o *peerOutbound) warnUnavailableOnce(err error) {
	o.mu.Lock()
	warned := o.warnedUnavailable
	o.warnedUnavailable = true
	o.mu.Unlock()
	if warned {
		return
	}
	slog.Warn("peer service unavailable; the same-tree guard answers from the rendezvous directory", "err", err)
}

// peerDirectory builds this process's port, once. It is nil when the protocol
// is not implemented here, which is the only thing that keeps the two tools out
// of the table.
func (env *Environment) peerDirectory() run.PeerDirectory {
	if env == nil || !peer.Enabled() {
		return nil
	}
	env.reloadMu.Lock()
	defer env.reloadMu.Unlock()
	if env.peerOut == nil {
		env.peerOut = &peerOutbound{
			env:     env,
			pacer:   peer.NewPacer(peer.DefaultLimits),
			tracker: newPeerTracker(env),
		}
	}
	return env.peerOut
}

// outbound returns this process's port for the service to wire its receipts
// into, or nil when this platform has no peer messaging at all.
func (env *Environment) outbound() *peerOutbound {
	if env == nil {
		return nil
	}
	port, _ := env.peerDirectory().(*peerOutbound)
	return port
}

// service is the registration running now, or nil.
func (o *peerOutbound) service() *peerService {
	o.env.reloadMu.Lock()
	defer o.env.reloadMu.Unlock()
	return o.env.peers
}

// peerOutboundView is the registration as one send sees it: the values are
// read once, under the service's lock, so a mode switch halfway through a send
// cannot mix two sockets into one envelope.
type peerOutboundView struct {
	dir       string
	cc        bool
	addr      string
	socket    string
	hopKey    []byte
	name      string
	launchDir string
	agents    []peer.ExtAgent
	mode      string
}

// view snapshots the registration this process is answering on. A process
// that is not listening on a socket right now — no service, a service whose
// first registration is still being retried (plan 237 §3, wired early by plan
// 240), or one whose mode switch failed — answers errPeerMessagingUnavailable:
// it has no directory to list, no address to send from and no name to sign
// with, and the error is what sends WorkTreePeers to the rendezvous directory.
// The session is the conversation that is speaking: its approval mode is what
// the recipient reads in from-mode, and a session this process does not run
// reads as prompting.
func (o *peerOutbound) view(sessionID string) (*peerOutboundView, error) {
	svc := o.service()
	if svc == nil {
		return nil, errPeerMessagingUnavailable
	}
	svc.mu.Lock()
	if !svc.running || svc.addr == "" {
		svc.mu.Unlock()
		return nil, errPeerMessagingUnavailable
	}
	view := &peerOutboundView{
		dir:    svc.layout.Dir,
		cc:     svc.ccMode,
		addr:   svc.addr,
		socket: svc.socketPath,
		hopKey: svc.hopKey,
		name:   svc.name,
	}
	if svc.env != nil {
		view.launchDir = svc.env.LaunchDir
		// The extension block is this process's own agent list — the very one
		// the other processes read — so the sender names itself with the
		// identity they know.
		_, view.agents = svc.statusAndAgentsLocked()
	}
	svc.mu.Unlock()
	view.mode = svc.modeFor(context.Background(), sessionID)
	return view, nil
}

// peerCandidate is one agent another process hosts, resolved from its record.
type peerCandidate struct {
	name      string // the name alone
	ref       string // the [ref] a listing shows when the name is not unique
	record    string // the name of the process that hosts it, for an address
	kind      string
	status    string
	task      string
	cwd       string
	sameTree  bool
	activeAgo time.Duration
	socket    string
	agentID   string // the forebrain agent id; empty for a non-forebrain peer
}

// printed is the address a listing prints and peer_send accepts for this
// candidate.
func (c peerCandidate) printed() string {
	if c.ref == "" {
		return c.name
	}
	return c.name + " [" + c.ref + "]"
}

// candidates lists the agents other processes host right now, the ones in this
// process's own directory tree first (design §3.4). A record with the forebrain
// extension block is expanded into the agents it hosts; a record without one is
// a peer of another kind, which only the Claude Code directory holds.
func (v *peerOutboundView) candidates() []peerCandidate {
	records := peer.ReadRecords(v.dir)
	out := make([]peerCandidate, 0, len(records))
	now := time.Now()
	for _, rec := range records {
		if rec.PID == os.Getpid() || !peer.Alive(rec) {
			continue
		}
		activeAgo := peerActiveAgo(now, rec.StatusUpdatedAt, rec.StartedAt)
		sameTree := sameWorkTree(rec.Cwd, v.launchDir)
		if rec.Forebrain == nil {
			if !v.cc {
				// In FB mode a record without the extension block is not a
				// forebrain process, and nobody else is reachable.
				continue
			}
			name := strings.TrimSpace(rec.Name)
			if name == "" {
				name = peerSlug(filepath.Base(strings.TrimSpace(rec.Cwd)))
			}
			out = append(out, peerCandidate{
				name:      name,
				record:    name,
				kind:      peerKindAgentSess,
				status:    rec.Status,
				cwd:       rec.Cwd,
				sameTree:  sameTree,
				activeAgo: activeAgo,
				socket:    rec.MessagingSocketPath,
			})
			continue
		}
		surface := strings.TrimSpace(rec.Forebrain.Surface)
		for _, agent := range rec.Forebrain.Agents {
			candidate := peerCandidate{
				name:      strings.TrimSpace(agent.Name),
				record:    strings.TrimSpace(agent.Name),
				kind:      peerKindOf(surface, agent),
				status:    strings.TrimSpace(agent.Status),
				task:      strings.TrimSpace(agent.Task),
				cwd:       strings.TrimSpace(agent.Root),
				activeAgo: activeAgo,
				socket:    rec.MessagingSocketPath,
				agentID:   strings.TrimSpace(agent.ID),
			}
			if candidate.cwd == "" {
				candidate.cwd = rec.Cwd
			}
			// One process can host conversations in several work trees, so
			// "same tree" is the agent's own root, not the process's cwd.
			candidate.sameTree = sameWorkTree(candidate.cwd, v.launchDir)
			out = append(out, candidate)
		}
	}
	peerAssignRefs(out)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].sameTree != out[j].sameTree {
			return out[i].sameTree
		}
		return false
	})
	return out
}

// peerKindOf names the sort of session an agent belongs to.
func peerKindOf(surface string, agent peer.ExtAgent) string {
	if strings.EqualFold(strings.TrimSpace(agent.Kind), "subagent") {
		return peerKindSubagent
	}
	if strings.EqualFold(strings.TrimSpace(surface), "gateway") {
		return peerKindGateway
	}
	return peerKindTerminal
}

// peerAssignRefs gives every agent whose normalized name another agent also
// carries a ref: two rows that read the same are two different agents, and the
// ref is what tells them apart.
func peerAssignRefs(candidates []peerCandidate) {
	byName := map[string]int{}
	for _, candidate := range candidates {
		byName[peer.NormalizeName(candidate.name)]++
	}
	if len(byName) == 0 {
		return
	}
	ids := make([]string, 0, len(candidates))
	for i, candidate := range candidates {
		ids = append(ids, candidate.agentID+"\x00"+strconv.Itoa(i))
	}
	for i := range candidates {
		if byName[peer.NormalizeName(candidates[i].name)] < 2 {
			continue
		}
		candidates[i].ref = peer.Ref(peerRefHashDomain, ids[i], ids)
	}
}

// peerActiveAgo is how long ago a record said anything about itself.
func peerActiveAgo(now time.Time, stampMs, startedMs int64) time.Duration {
	stamp := stampMs
	if stamp == 0 {
		stamp = startedMs
	}
	if stamp == 0 {
		return 0
	}
	ago := now.Sub(time.UnixMilli(stamp))
	if ago < 0 || ago > peerActiveWindow {
		return 0
	}
	return ago
}

// List is peer_list's cross-process half: the agents other processes host, as
// the calling agent sees them.
func (o *peerOutbound) List(ctx context.Context, from run.PeerCaller) (run.PeerListing, error) {
	view, err := o.view(from.SessionID)
	if err != nil {
		return run.PeerListing{}, err
	}
	candidates := view.candidates()
	entries := make([]run.PeerEntry, 0, len(candidates))
	self := view.selfID(from)
	for _, candidate := range candidates {
		if candidate.agentID != "" && candidate.agentID == self {
			continue
		}
		entries = append(entries, run.PeerEntry{
			Name:      candidate.name,
			Ref:       candidate.ref,
			Kind:      candidate.kind,
			Status:    candidate.status,
			Task:      candidate.task,
			Cwd:       candidate.cwd,
			SameTree:  candidate.sameTree,
			ActiveAgo: candidate.activeAgo,
		})
	}
	return run.PeerListing{Peers: entries}, nil
}

// Send is peer_send's cross-process half: resolve the name, pace it, and put
// one envelope frame on the recipient's socket.
func (o *peerOutbound) Send(ctx context.Context, from run.PeerCaller, to, message string) (run.PeerSendResult, error) {
	view, err := o.view(from.SessionID)
	if err != nil {
		return run.PeerSendResult{}, err
	}
	candidates := view.candidates()
	self := view.selfID(from)
	kept := candidates[:0]
	for _, candidate := range candidates {
		if candidate.agentID != "" && candidate.agentID == self {
			continue
		}
		kept = append(kept, candidate)
	}
	target, resolveErr := resolvePeerCandidate(kept, to)
	if resolveErr != nil {
		return run.PeerSendResult{}, resolveErr
	}
	refund := func() {}
	if ok, undo := o.pacer.Reserve(target.socket, time.Now()); ok {
		refund = undo
	} else {
		return run.PeerSendResult{}, fmt.Errorf(
			"Not sent: too many messages to %s in a short time. Wait a little and send one combined message.", target.printed())
	}
	senderName, senderID := view.sender(from)
	frame := peer.NewUserFrame(peer.UserFrameOptions{
		From:      view.addr,
		Content:   peer.Build(peer.Envelope{From: view.addr, FromName: senderName, FromMode: view.mode, HopChain: view.hopChain(ctx, o.env, from.SessionID), Body: message}),
		FromAgent: senderID,
		ToAgent:   target.agentID,
	})
	if err := peer.Send(ctx, target.socket, frame, peer.SendOptions{NoFollowSymlink: true, KeyDir: view.dir}); err != nil {
		refund()
		if errors.Is(err, peer.ErrPeerGone) {
			return run.PeerSendResult{}, fmt.Errorf("%s is no longer running.", target.printed())
		}
		return run.PeerSendResult{}, fmt.Errorf("Not sent: %v", err)
	}
	o.tracker.remember(frame.MsgID, from.SessionID, target.printed(), target.socket)
	return run.PeerSendResult{Name: target.printed(), Status: target.status}, nil
}

// selfID is the calling agent's id inside this process: the conversation for a
// primary agent, the conversation and the roster key for a subagent.
func (v *peerOutboundView) selfID(from run.PeerCaller) string {
	sessionID := strings.TrimSpace(from.SessionID)
	if sessionID == "" {
		return ""
	}
	if key := strings.TrimSpace(from.AgentKey); key != "" {
		return sessionID + "/" + key
	}
	return sessionID
}

// sender is the name and id the recipient reads: the extension block's own
// entry when this process lists the calling agent, and a derived name
// otherwise — a session the TUI is not showing has no entry to read.
func (v *peerOutboundView) sender(from run.PeerCaller) (string, string) {
	id := v.selfID(from)
	for _, agent := range v.agents {
		if candidateID := strings.TrimSpace(agent.ID); candidateID != "" && candidateID == id {
			return strings.TrimSpace(agent.Name), id
		}
	}
	if strings.TrimSpace(from.AgentKey) != "" {
		return v.name + "." + peerSlug(from.AgentKey), id
	}
	if v.name != "" {
		return v.name, id
	}
	return peerSlug(filepath.Base(strings.TrimSpace(v.launchDir))), id
}

// hopChain is the chain the message carries: the one the conversation's last
// peer message brought, extended with this process's own token (design §3.8).
func (v *peerOutboundView) hopChain(ctx context.Context, env *Environment, sessionID string) []string {
	inherited := lastPeerHopChain(ctx, env, sessionID)
	if len(v.hopKey) == 0 {
		return inherited
	}
	return peer.ExtendChain(inherited, peer.HopToken(v.hopKey, v.addr))
}

// lastPeerHopChain reads the chain out of the newest peer message the
// conversation received. The row's text is the envelope exactly as it arrived,
// so the chain is where the sender put it; a row that does not parse is not one
// this process can inherit from, and the chain starts fresh.
func lastPeerHopChain(ctx context.Context, env *Environment, sessionID string) []string {
	if env == nil || env.Deps.SessionStore == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	rows, err := env.Deps.SessionStore.ListRecentMessages(ctx, sessionID, peerRecentRowScan)
	if err != nil {
		slog.Debug("peer hop chain could not read the conversation", "session_id", sessionID, "err", err)
		return nil
	}
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		if !strings.EqualFold(strings.TrimSpace(row.Role), "user") {
			continue
		}
		if state.MessageOrigin(row.PartsJSON) != state.MessageOriginPeer {
			continue
		}
		envelope, ok := peer.Parse(row.Content)
		if !ok {
			slog.Debug("peer row is not an envelope; starting a fresh hop chain", "session_id", sessionID)
			return nil
		}
		return envelope.HopChain
	}
	return nil
}

// resolvePeerCandidate turns what the model typed into one candidate: an
// address, a name with its ref, or a name that is unique on its own (design
// §3.4).
func resolvePeerCandidate(candidates []peerCandidate, to string) (peerCandidate, error) {
	address := strings.TrimSpace(to)
	if strings.HasPrefix(address, "uds:") {
		scheme, path := peer.DecodeAddress(address)
		if scheme == "uds" && path != "" {
			for _, candidate := range candidates {
				if candidate.socket != path {
					continue
				}
				candidate.name = candidate.record
				candidate.ref = ""
				candidate.agentID = ""
				return candidate, nil
			}
		}
		return peerCandidate{}, fmt.Errorf("No agent at %s. Run peer_list to see who is reachable.", address)
	}
	if match := peerRefPattern.FindStringSubmatch(address); match != nil {
		name, ref := match[1], match[2]
		for _, candidate := range candidates {
			if candidate.ref == ref && peer.NormalizeName(candidate.name) == peer.NormalizeName(name) {
				return candidate, nil
			}
		}
		return peerCandidate{}, fmt.Errorf("No agent named %q. Run peer_list to see who is reachable.", address)
	}
	normalized := peer.NormalizeName(address)
	var matches []peerCandidate
	for _, candidate := range candidates {
		if peer.NormalizeName(candidate.name) == normalized {
			matches = append(matches, candidate)
		}
	}
	switch len(matches) {
	case 0:
		return peerCandidate{}, fmt.Errorf("No agent named %q. Run peer_list to see who is reachable.", address)
	case 1:
		return matches[0], nil
	default:
		printed := make([]string, 0, len(matches))
		for _, match := range matches {
			printed = append(printed, match.printed())
		}
		return peerCandidate{}, fmt.Errorf("%q matches several agents: %s. Use the name with its [ref].",
			address, strings.Join(printed, ", "))
	}
}

// peerTracker is this process's outbound table and the receipts that answer it:
// one entry per message sent, and one aggregated note per conversation that
// hears back (design §3.11).
type peerTracker struct {
	env *Environment

	mu      sync.Mutex
	sent    map[string]peerSentMessage
	order   []string
	pending map[string][]string
	timers  map[string]*time.Timer
}

// peerSentMessage is one message this process sent and may still hear about.
type peerSentMessage struct {
	sessionID string
	name      string
	socket    string
	at        time.Time
}

// newPeerTracker builds an empty table.
func newPeerTracker(env *Environment) *peerTracker {
	return &peerTracker{
		env:     env,
		sent:    map[string]peerSentMessage{},
		pending: map[string][]string{},
		timers:  map[string]*time.Timer{},
	}
}

// remember records one sent message, evicting the oldest past the table's cap.
func (t *peerTracker) remember(msgID, sessionID, name, socket string) {
	if t == nil || strings.TrimSpace(msgID) == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sent[msgID] = peerSentMessage{sessionID: sessionID, name: name, socket: socket, at: time.Now()}
	t.order = append(t.order, msgID)
	for len(t.order) > peerSentLimit {
		oldest := t.order[0]
		t.order = t.order[1:]
		delete(t.sent, oldest)
	}
}

// receipt takes one receipt a peer sent about a message this process sent. A
// receipt nobody is waiting for — an entry that aged out of the table, or a
// message another process sent — is dropped.
func (t *peerTracker) receipt(status peer.PeerMessageStatus, _ string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	sent, ok := t.sent[status.OrigMsgID]
	if !ok {
		t.mu.Unlock()
		return
	}
	line := peerReceiptLine(sent.name, status)
	if line == "" {
		t.mu.Unlock()
		return
	}
	sessionID := sent.sessionID
	t.pending[sessionID] = append(t.pending[sessionID], line)
	if t.timers[sessionID] == nil {
		t.timers[sessionID] = time.AfterFunc(peerReceiptDebounce, func() { t.flush(sessionID) })
	}
	t.mu.Unlock()
}

// flush hands one conversation everything that arrived about its messages in
// this burst, as a single note.
func (t *peerTracker) flush(sessionID string) {
	t.mu.Lock()
	lines := t.pending[sessionID]
	delete(t.pending, sessionID)
	delete(t.timers, sessionID)
	t.mu.Unlock()
	if len(lines) == 0 {
		return
	}
	text := peerReceiptSummary(lines)
	in := t.env.peerInboxState()
	if in == nil {
		return
	}
	if refusal := in.deliverNote(context.Background(), sessionID, text); refusal != "" {
		slog.Debug("peer receipt summary could not be delivered", "session_id", sessionID, "reason", refusal)
	}
}

// The receipt lines a summary carries, one per state the protocol reports
// (design §3.11). Claude Code's own wording is its own; these say the same
// thing in forebrain's neutral voice.
const (
	peerSummaryHeld      = "- to %s: held by the receiving session; it has not seen your message yet. Usually the two sessions run with different permission modes, or the receiving session holds every peer message."
	peerSummaryDelivered = "- to %s: released after approval and delivered."
	peerSummaryDenied    = "- to %s: declined by the recipient's user; it was not delivered."
	peerSummaryExpired   = "- to %s: expired without approval; it was not delivered."
	peerSummaryRefused   = "- to %s: refused (%s); it was not delivered."
	peerSummaryDropped   = "- to %s: dropped (%s); it was not delivered."
	peerSummaryTemplate  = "<system-reminder>\nPeer message delivery update:\n%s\n</system-reminder>"
)

// peerReceiptLine is one receipt as the sending model reads it, or "" for a
// state this process does not report.
func peerReceiptLine(name string, status peer.PeerMessageStatus) string {
	switch status.Status {
	case peer.StatusHeld:
		return fmt.Sprintf(peerSummaryHeld, name)
	case peer.StatusDelivered:
		return fmt.Sprintf(peerSummaryDelivered, name)
	case peer.StatusDenied:
		return fmt.Sprintf(peerSummaryDenied, name)
	case peer.StatusExpired:
		if status.StatusDetail == peer.StatusDetailRefused {
			return fmt.Sprintf(peerSummaryRefused, name, peerReceiptReason(status.Reason, "refused"))
		}
		return fmt.Sprintf(peerSummaryExpired, name)
	case peer.StatusDropped:
		return fmt.Sprintf(peerSummaryDropped, name, peerReceiptReason(status.DropReason, peerReceiptReason(status.Reason, "dropped")))
	}
	return ""
}

// peerReceiptReason is the text a receipt's reason carries, or the fallback the
// line names.
func peerReceiptReason(reason, fallback string) string {
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		return trimmed
	}
	return fallback
}

// peerReceiptSummary wraps the lines into the note the sending conversation
// reads.
func peerReceiptSummary(lines []string) string {
	return fmt.Sprintf(peerSummaryTemplate, strings.Join(lines, "\n"))
}

// PeerReceiptRow is one recipient's answer to a message this process sent, as
// the delivery card reads it (plan 200 §3). Status is the state the protocol
// reported — held, delivered, denied, expired, refused or dropped — and Reason
// is the plain words that came with it, empty for the states that carry none.
type PeerReceiptRow struct {
	Name   string
	Status string
	Reason string
}

// The states a receipt row reports, named as the protocol names them.
const (
	PeerReceiptHeld      = "held"
	PeerReceiptDelivered = "delivered"
	PeerReceiptDenied    = "denied"
	PeerReceiptExpired   = "expired"
	PeerReceiptRefused   = "refused"
	PeerReceiptDropped   = "dropped"
)

// ParsePeerReceiptSummary reads a delivery note back into the rows a surface
// draws, so the card does not have to read English prose to know which recipient
// got which state. It is the inverse of peerReceiptSummary for everything this
// process writes; content in any other shape yields no rows, and the caller
// shows it whole rather than guessing (the terminal already does).
func ParsePeerReceiptSummary(content string) []PeerReceiptRow {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	rows := make([]PeerReceiptRow, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- to ") {
			continue
		}
		rest := strings.TrimPrefix(trimmed, "- to ")
		name, state, found := strings.Cut(rest, ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		rows = append(rows, parsePeerReceiptRow(name, strings.TrimSpace(state)))
	}
	return rows
}

// parsePeerReceiptRow classifies one receipt line's state sentence. The
// sentences are this package's own (peerReceiptLine), so the match is on their
// leading words; anything unrecognised reads as the state the protocol would
// have reported for it, with the sentence kept as the reason.
func parsePeerReceiptRow(name, state string) PeerReceiptRow {
	switch {
	case strings.HasPrefix(state, "held by the receiving session"):
		return PeerReceiptRow{Name: name, Status: PeerReceiptHeld}
	case strings.HasPrefix(state, "released after approval and delivered"):
		return PeerReceiptRow{Name: name, Status: PeerReceiptDelivered}
	case strings.HasPrefix(state, "declined by the recipient's user"):
		return PeerReceiptRow{Name: name, Status: PeerReceiptDenied}
	case strings.HasPrefix(state, "expired without approval"):
		return PeerReceiptRow{Name: name, Status: PeerReceiptExpired}
	case strings.HasPrefix(state, "refused ("):
		return PeerReceiptRow{Name: name, Status: PeerReceiptRefused, Reason: parenthesised(state)}
	case strings.HasPrefix(state, "dropped ("):
		return PeerReceiptRow{Name: name, Status: PeerReceiptDropped, Reason: parenthesised(state)}
	default:
		// A sentence this package did not write. It still names a recipient,
		// and the card's grey bucket is where an unrecognised outcome belongs.
		return PeerReceiptRow{Name: name, Status: PeerReceiptExpired, Reason: state}
	}
}

// parenthesised is the text inside a receipt line's trailing parentheses.
func parenthesised(state string) string {
	open := strings.Index(state, "(")
	close := strings.LastIndex(state, ")")
	if open < 0 || close <= open {
		return state
	}
	return strings.TrimSpace(state[open+1 : close])
}
