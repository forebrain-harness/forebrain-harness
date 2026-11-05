// The receiving half of the peer service (plan 196): what happens to a frame
// that arrives on this process's socket once 195's source gate has let it in.
//
// One pipeline, shared by both surfaces. A peer message is admitted by the
// protocol's own anti-ping-pong rules, routed to the conversation it belongs
// to, held when the two sides' permission modes disagree, delivered into the
// turn that is running or as a turn of its own when the conversation is idle,
// and answered with the receipt the peer protocol asks for. A surface supplies
// only two things: where run events go (Publish) and how to start a turn
// (Wake).
//
// The model sees the envelope exactly as it arrived, and the row it lands in is
// meta with the origin "peer": a peer message is context the conversation was
// given, not something somebody typed at this terminal. It does not go through
// the surface's turn parsing — no slash commands, no skill references, no
// attachments (plan 196 §1).
package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/peer"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// The protocol's own numbers, and this plan's two.
const (
	// peerHeldLimit is how many messages may wait for the person's decision. A
	// message past the cap pushes the oldest one out, which is then expired —
	// the same rule Claude Code applies to its own hold buffer.
	peerHeldLimit = 20
	// peerHeldPreviewRunes bounds the opening a held message shows.
	peerHeldPreviewRunes = 200
	// peerSubscriptionLimit and peerSubscriptionTTL bound the idle
	// subscriptions one process stands behind.
	peerSubscriptionLimit = 32
	peerSubscriptionTTL   = 12 * time.Hour
	// peerReceiptTimeout is one receipt's own budget. A receipt is a courtesy
	// the sender may no longer be listening for; the pipeline never waits for
	// it beyond this.
	peerReceiptTimeout = 2 * time.Second
	// peerTeardownBudget is the whole budget for answering everything still
	// held or subscribed when the registration goes away, so an exiting
	// process cannot hang on peers that are gone.
	peerTeardownBudget = time.Second
)

// peerForwardedToDispatcherNote is what the dispatching agent reads when a
// message addressed to one of its subagents arrives after that subagent
// finished. It is model-visible, so it names no product, model or tool beyond
// forebrain's own subagent_continue, and it tells the dispatcher what its
// options are rather than deciding for it.
const peerForwardedToDispatcherNote = "<system-reminder>\nA peer message addressed to your subagent %s arrived after that subagent finished, so it is delivered to you. Decide whether to continue the subagent with subagent_continue or to answer the sender yourself.\n</system-reminder>"

// ErrPeerWakeUnavailable is what a surface returns from Wake when it cannot
// start a turn for a peer prompt — no attached UI, no turn runtime. The message
// is then refused rather than silently kept.
var ErrPeerWakeUnavailable = errors.New("peer: this surface cannot start a turn")

// The reasons a receipt carries. Model-invisible: they are sentences for
// whoever reads the sender's log, and the wire value is what the protocol
// speaks.
const (
	peerReceiptHeldModeMismatch    = "held: permission modes differ"
	peerReceiptHeldNoMode          = "held: this session holds every peer message"
	peerReceiptDelivered           = "released after approval"
	peerReceiptDenied              = "declined by the user"
	peerReceiptExpired             = "expired without approval"
	peerRefusedUnknownAgent        = "unknown agent"
	peerRefusedNoConversation      = "no conversation in this work tree"
	peerRefusedNoScreenSession     = "no conversation on screen"
	peerRefusedBatch               = "batch conversations take no input"
	peerRefusedBusyElsewhere       = "busy in another process"
	peerRefusedInboxNotAttached    = "inbox not attached"
	peerRefusedAgentNotRunning     = "subagent is no longer running"
	peerRefusedAgentGone           = "subagent is gone"
	peerIdleNoticeStateIdle        = "idle"
	peerIdleNoticeStateExited      = "exited"
	peerIdleNoticeStateUnavailable = "unavailable"
)

// peerInboxTeardown says why the registration is going away, which decides what
// the peers still waiting on us are told.
type peerInboxTeardown int

const (
	// peerTeardownModeSwitch is the interop switch moving this process to the
	// other directory: nothing is wrong with the conversation, so an idle
	// subscription is told the answer is unavailable for now.
	peerTeardownModeSwitch peerInboxTeardown = iota
	// peerTeardownStopped is this process exiting: it will not answer again.
	peerTeardownStopped
)

// HeldPeerMessage is one peer message waiting for the person to release or deny
// it, as a surface shows it.
type HeldPeerMessage struct {
	MsgID    string
	From     string
	FromName string
	Preview  string
	// Body is the message itself, with the envelope's own markup taken off it.
	// Preview is the envelope's opening — what the terminal's picker row shows
	// — and a banner that quotes the message needs the words, not the wrapper
	// (plan 200 §3).
	Body string
	// Cause is why it was held: event.PeerHoldCause*.
	Cause    string
	HeldAtMs int64
}

// peerArrival is one frame that reached the pipeline, with everything the
// receiving side needs to answer it: the socket it arrived on (which is where
// every receipt goes back from), and the sender's protocol features.
type peerArrival struct {
	Frame        peer.Frame
	PeerPID      int
	Local        string
	Dir          string
	PeerFeatures []string
}

// peerTarget is where a message is going inside this process.
type peerTarget struct {
	// SessionID is the conversation. AgentKey names one of its subagents, and
	// is empty for the conversation's own agent.
	SessionID string
	AgentKey  string
	// AgentName is what the recipient is called, for the forwarding note.
	AgentName string
}

// heldPeerMessage is one message inside the hold buffer: the arrival it must be
// answered on, the envelope to deliver if it is released, and the target.
type heldPeerMessage struct {
	msgID    string
	arrival  peerArrival
	envelope string
	from     string
	fromName string
	target   peerTarget
	cause    string
	heldAt   time.Time
}

// peerIdleSubscription is one peer waiting to be told when a conversation goes
// idle, and the socket to tell it on.
type peerIdleSubscription struct {
	origMsgID string
	replyTo   string
	arrival   peerArrival
	sessionID string
	createdAt time.Time
}

// peerInbox is the receiving state of one process's peer service.
type peerInbox struct {
	svc       *peerService
	surface   PeerSurface
	admission *peer.Admission
	// onReceipt takes the receipts a peer sends us about messages we sent. Plan
	// 196 leaves it logging; 197 replaces it with the outbound tracker.
	onReceipt func(peer.PeerMessageStatus, string)

	mu sync.Mutex
	// local and addr are this process's current socket and address, so a
	// teardown can answer the peers still waiting even though it runs under the
	// service's own lock.
	local string
	addr  string
	held  []*heldPeerMessage
	// subscriptions are the peers waiting for a conversation to go idle, and
	// idleSince is when each conversation was last seen to have no run.
	subscriptions map[string]*peerIdleSubscription
	idleSince     map[string]int64
	// handed counts the messages this process gave to a conversation since that
	// conversation was last idle: the protocol's queue depth is what this
	// process still owes a model, not what a turn has already taken.
	handed map[string]int
}

// newPeerInbox builds the receiving state for a service.
func newPeerInbox(svc *peerService, surface PeerSurface) *peerInbox {
	return &peerInbox{
		svc:           svc,
		surface:       surface,
		admission:     peer.NewAdmission(peer.DefaultLimits),
		onReceipt:     logPeerReceipt,
		subscriptions: map[string]*peerIdleSubscription{},
		idleSince:     map[string]int64{},
		handed:        map[string]int{},
	}
}

// logPeerReceipt is what this plan does with a receipt: says so in the log.
// Plan 197 turns them into a meta message for the conversation that sent them.
func logPeerReceipt(status peer.PeerMessageStatus, from string) {
	slog.Debug("peer receipt for a message this process sent", "from", from, "status", status.Status,
		"orig_msg_id", status.OrigMsgID, "detail", status.StatusDetail, "reason", status.Reason)
}

// remember records the socket this process is listening on now. It is called
// from the service's mode entry, under the service's lock, so the inbox never
// has to take that lock to answer a teardown.
func (in *peerInbox) remember(local, addr string) {
	in.mu.Lock()
	in.local, in.addr = local, addr
	in.mu.Unlock()
}

// handle is one arrived frame, already through 195's source gate.
func (in *peerInbox) handle(ctx context.Context, a peerArrival) {
	switch a.Frame.Type {
	case peer.FrameTypeUser:
		in.handleUserFrame(ctx, a)
	case peer.FrameTypeControl:
		in.handleControlFrame(ctx, a)
	default:
		slog.Debug("peer frame of an unknown type ignored", "type", a.Frame.Type)
	}
}

// handleUserFrame is a message: shape, envelope, admission, routing, policy,
// delivery, receipt — in that order, so nothing is delivered that the protocol
// would not have admitted and nothing is answered that was never a message.
func (in *peerInbox) handleUserFrame(ctx context.Context, a peerArrival) {
	content := a.Frame.Message.Content
	if strings.TrimSpace(content) == "" {
		// Not a message at all: there is nothing to answer, and a receipt for
		// it would only teach a broken sender that we took something.
		slog.Debug("peer user frame without content ignored", "peer_pid", a.PeerPID)
		return
	}
	if raw, ok := a.Frame.Raw("session_id"); ok {
		if want := frameString(raw); want != "" && want != in.svc.sessionID() {
			// The sender aimed at a conversation this process is not showing.
			slog.Debug("peer user frame for another conversation ignored", "session_id", want)
			return
		}
	}
	envelope := in.envelopeFor(content, a.Frame.From)
	parsed, _ := peer.Parse(envelope)

	queued := in.queued()
	senderKey := in.senderKey(a.Frame.From, a.PeerPID)
	if ok, reason := in.admission.Admit(senderKey, envelope, parsed.HopChain, in.ownHopTokens(), queued, time.Now()); !ok {
		in.sendReceipt(ctx, a, peer.PeerMessageStatus{
			Status:        peer.StatusDropped,
			Reason:        peer.DropReasons[reason],
			OrigMsgID:     a.Frame.MsgID,
			DropReason:    reason,
			DroppedMsgIDs: []string{},
		})
		return
	}

	target, refusal := in.route(ctx, a, parsed)
	if refusal != "" {
		in.refuse(ctx, a, refusal)
		return
	}
	if cause := in.holdCause(ctx, a, parsed, target); cause != "" {
		in.hold(ctx, a, envelope, parsed, target, cause)
		return
	}
	if refusal := in.deliverTo(ctx, a, target, envelope, a.Frame.MsgID, parsed.FromName); refusal != "" {
		in.refuse(ctx, a, refusal)
	}
}

// envelopeFor is the text the model reads: the envelope as it arrived, or, when
// the content was not one, a fresh envelope wrapping it so the model can tell a
// peer wrote it.
func (in *peerInbox) envelopeFor(content, from string) string {
	if _, ok := peer.Parse(content); ok {
		return content
	}
	envelope := peer.Envelope{Body: peer.EscapeBody(content)}
	if peer.ValidAddress(from) {
		envelope.From = from
	}
	return peer.Build(envelope)
}

// senderKey is the admission bucket's key: the sender's address, or its pid
// when it claims none (Claude Code's own rule).
func (in *peerInbox) senderKey(from string, pid int) string {
	if strings.TrimSpace(from) != "" {
		return "from:" + from
	}
	return fmt.Sprintf("pid:%d", pid)
}

// ownHopTokens is every hop token this process would put on a message it sent:
// one per address it has ever used, so a message that has already been through
// here is recognised however the process has moved.
func (in *peerInbox) ownHopTokens() map[string]bool {
	in.svc.mu.Lock()
	addresses := make([]string, 0, len(in.svc.ownAddresses))
	for addr := range in.svc.ownAddresses {
		addresses = append(addresses, addr)
	}
	key := in.svc.hopKey
	in.svc.mu.Unlock()
	tokens := make(map[string]bool, len(addresses))
	for _, addr := range addresses {
		tokens[peer.HopToken(key, addr)] = true
	}
	return tokens
}

// queued is how many peer messages this process has taken and not yet handed to
// a model: everything held, plus everything given to a conversation whose run
// has not ended since.
func (in *peerInbox) queued() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	total := len(in.held)
	for _, count := range in.handed {
		total += count
	}
	return total
}

// route decides which of this process's agents the message belongs to, or why
// none does (design §5.1).
func (in *peerInbox) route(ctx context.Context, a peerArrival, parsed peer.Envelope) (peerTarget, string) {
	if a.Frame.Forebrain != nil {
		if to := strings.TrimSpace(a.Frame.Forebrain.To); to != "" {
			return in.resolveAddressed(ctx, to)
		}
	}
	if in.svc.surface.Kind == "gateway" {
		return in.routeByWorkTree(ctx, a)
	}
	sid := strings.TrimSpace(in.svc.sessionID())
	if sid == "" {
		return peerTarget{}, peerRefusedNoScreenSession
	}
	return in.primaryTarget(ctx, sid)
}

// resolveAddressed resolves a message that names its recipient. An agent id is
// either a conversation (its primary agent) or "<conversation>/<roster key>"
// (one of that conversation's subagents).
func (in *peerInbox) resolveAddressed(ctx context.Context, to string) (peerTarget, string) {
	sessionID, agentKey, isSubagent := strings.Cut(to, "/")
	if !in.hostsSession(ctx, sessionID) {
		return peerTarget{}, peerRefusedUnknownAgent
	}
	if isSubagent {
		agentKey = strings.TrimSpace(agentKey)
		if agentKey == "" {
			return peerTarget{}, peerRefusedUnknownAgent
		}
		return peerTarget{SessionID: sessionID, AgentKey: agentKey, AgentName: agentKey}, ""
	}
	return in.primaryTarget(ctx, sessionID)
}

// hostsSession reports whether this process can answer for a conversation: the
// one on screen, one it is running a turn for, or (for the gateway) one of the
// conversations it lists as its own.
func (in *peerInbox) hostsSession(ctx context.Context, sessionID string) bool {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return false
	}
	if sid == strings.TrimSpace(in.svc.sessionID()) {
		return true
	}
	if in.svc.env != nil && in.svc.env.Control != nil {
		if _, _, ok := in.svc.env.Control.Find(sid); ok {
			return true
		}
	}
	if in.svc.surface.Kind != "gateway" {
		return false
	}
	for _, session := range in.recentSessions(ctx) {
		if session.ID == sid {
			return true
		}
	}
	return false
}

// primaryTarget is a conversation's own agent, refused when the conversation is
// a batch task: a batch task runs unattended, so nobody there takes input from
// anywhere (design §5.1, owner 2026-10-31).
func (in *peerInbox) primaryTarget(ctx context.Context, sessionID string) (peerTarget, string) {
	if in.batchSession(ctx, sessionID) {
		return peerTarget{}, peerRefusedBatch
	}
	return peerTarget{SessionID: sessionID}, ""
}

// routeByWorkTree picks the conversation in this gateway that lives in the
// sender's work tree: the most recently active one that is neither a batch task
// nor being run by another process.
func (in *peerInbox) routeByWorkTree(ctx context.Context, a peerArrival) (peerTarget, string) {
	workTree := in.senderWorkTree(a)
	if workTree == "" {
		return peerTarget{}, peerRefusedNoConversation
	}
	for _, session := range in.recentSessions(ctx) {
		if !sameWorkTree(workTree, session.Cwd) {
			continue
		}
		// A conversation another process is running takes no message from here:
		// that process owns the turn, and its own inbox is where the sender
		// should have aimed.
		if in.svc.env != nil && in.svc.env.Deps.RunRT != nil {
			if err := in.svc.env.Deps.RunRT.SessionBusy(ctx, session.ID); err != nil {
				if !in.runningHere(session.ID) {
					continue
				}
			}
		}
		if in.batchSession(ctx, session.ID) {
			continue
		}
		return in.primaryTarget(ctx, session.ID)
	}
	return peerTarget{}, peerRefusedNoConversation
}

// senderWorkTree is the work tree the sender says it is working in: the agent
// its extension block names, when that agent has a root, else the directory its
// registration records.
func (in *peerInbox) senderWorkTree(a peerArrival) string {
	rec := in.senderRecord(a)
	if rec == nil {
		return ""
	}
	if a.Frame.Forebrain != nil && rec.Forebrain != nil {
		from := strings.TrimSpace(a.Frame.Forebrain.From)
		for _, agent := range rec.Forebrain.Agents {
			if from != "" && agent.ID == from && strings.TrimSpace(agent.Root) != "" {
				return strings.TrimSpace(agent.Root)
			}
		}
	}
	return strings.TrimSpace(rec.Cwd)
}

// senderRecord is the sender's registration in the directory in force.
func (in *peerInbox) senderRecord(a peerArrival) *peer.Record {
	return in.svc.recordByPID(a.Dir, a.PeerPID)
}

// recentSessions is the gateway's own conversation list, newest first.
func (in *peerInbox) recentSessions(ctx context.Context) []state.RecentSession {
	if in.svc.env == nil || in.svc.env.Deps.SessionStore == nil {
		return nil
	}
	sessions, err := in.svc.env.Deps.SessionStore.RecentlyActiveSessions(ctx, time.Now().Add(-peerGatewayWindow), peerMaxAgents)
	if err != nil {
		slog.Debug("peer routing could not read this process's conversations", "err", err)
		return nil
	}
	return sessions
}

// runningHere reports whether a conversation's live run belongs to this
// process.
func (in *peerInbox) runningHere(sessionID string) bool {
	if in.svc.env == nil || in.svc.env.Control == nil {
		return false
	}
	_, _, ok := in.svc.env.Control.Find(sessionID)
	return ok
}

// batchSession reports whether a conversation is a batch task's.
func (in *peerInbox) batchSession(ctx context.Context, sessionID string) bool {
	if in.svc.env == nil || in.svc.env.SQL == nil {
		return false
	}
	projectType, err := state.NewProjectStore(in.svc.env.SQL, in.svc.env.Deps.AgentName).SessionProjectType(ctx, sessionID)
	if err != nil {
		// Unreadable is treated as an ordinary conversation: refusing every
		// message because a lookup failed would cut a working session off.
		return false
	}
	return projectType == state.ProjectTypeBatch
}

// holdCause is the inbound policy (design §3.10). There is no configured
// setting — cross-process coordination is always on (owner 2026-10-31) — so the
// policy is derived from the receiving conversation's own approval mode: the
// same mode accepts, a different one is held for the person, and a session that
// never asks the person only takes a message that says which mode it assumed.
func (in *peerInbox) holdCause(ctx context.Context, a peerArrival, parsed peer.Envelope, target peerTarget) string {
	self := in.svc.modeFor(ctx, target.SessionID)
	from := strings.TrimSpace(parsed.FromMode)
	switch {
	case from == "" && self == "bypass":
		// This session never asks the person anything, and the message does not
		// say which mode it assumed: nobody here can tell whether sending it
		// straight to the model would be honouring a decision this session never
		// made.
		return event.PeerHoldCauseNoModeAsserted
	case from == "" || from == self:
		return ""
	default:
		return event.PeerHoldCauseModeMismatch
	}
}

// hold parks a message until the person releases or denies it.
func (in *peerInbox) hold(ctx context.Context, a peerArrival, envelope string, parsed peer.Envelope, target peerTarget, cause string) {
	held := &heldPeerMessage{
		msgID:    a.Frame.MsgID,
		arrival:  a,
		envelope: envelope,
		from:     strings.TrimSpace(parsed.From),
		fromName: strings.TrimSpace(parsed.FromName),
		target:   target,
		cause:    cause,
		heldAt:   time.Now(),
	}
	in.mu.Lock()
	in.held = append(in.held, held)
	var evicted *heldPeerMessage
	if len(in.held) > peerHeldLimit {
		evicted = in.held[0]
		in.held = in.held[1:]
	}
	in.mu.Unlock()

	// Answered outside the lock: a receipt goes out over a socket, and a peer
	// that is gone must not be able to hold up the pipeline.
	if evicted != nil {
		in.settle(evicted, event.PeerOutcomeExpired, peerReceiptExpired)
	}
	in.sendReceipt(ctx, a, peer.PeerMessageStatus{
		Status:    peer.StatusHeld,
		Reason:    peerHeldReason(cause),
		OrigMsgID: a.Frame.MsgID,
	})
	slog.Debug("peer message held for the person", "msg_id", a.Frame.MsgID, "cause", cause, "session_id", target.SessionID)
	in.publishHeld(ctx, held)
}

// peerHeldReason is the sentence a hold receipt carries.
func peerHeldReason(cause string) string {
	if cause == event.PeerHoldCauseNoModeAsserted {
		return peerReceiptHeldNoMode
	}
	return peerReceiptHeldModeMismatch
}

// publishHeld tells the surface a message is waiting for the person.
func (in *peerInbox) publishHeld(ctx context.Context, held *heldPeerMessage) {
	in.publish(ctx, held.target.SessionID, event.RunEventPeerMessageHeld, event.PeerMessageHeldPayload{
		MsgID:    held.msgID,
		From:     held.from,
		FromName: held.fromName,
		Preview:  peerPreview(held.envelope),
		PeerBody: peerEnvelopeBody(held.envelope),
		Cause:    held.cause,
		HeldAtMs: held.heldAt.UnixMilli(),
	})
}

// publishSettled tells the surface how a held message ended.
func (in *peerInbox) publishSettled(ctx context.Context, sessionID, msgID, outcome string) {
	in.publish(ctx, sessionID, event.RunEventPeerMessageSettled, event.PeerMessageSettledPayload{
		MsgID:   msgID,
		Outcome: outcome,
	})
}

// publish sends one peer fact into the surface's event funnel. A peer message
// belongs to no run, so the run id is empty: the fact lands on the
// conversation's log, which is where replay reads it.
func (in *peerInbox) publish(ctx context.Context, sessionID, typ string, payload any) {
	if in.surface.Publish == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	if err := in.surface.Publish(ctx, sessionID, "", typ, payload); err != nil {
		slog.Debug("peer run event could not be published", "type", typ, "session_id", sessionID, "err", err)
	}
}

// peerEnvelopeBody is the message inside an envelope, or "" when the content
// was not one. A banner quotes the words the person is being asked about, and
// the envelope's own markup is not those words (plan 200 §3).
func peerEnvelopeBody(envelope string) string {
	if _, body, ok := ParsePeerEnvelope(envelope); ok {
		return body
	}
	return ""
}

// peerPreview is a message's opening, for a card that says what arrived.
func peerPreview(envelope string) string {
	runes := []rune(strings.TrimSpace(envelope))
	if len(runes) <= peerHeldPreviewRunes {
		return string(runes)
	}
	return string(runes[:peerHeldPreviewRunes]) + "…"
}

// settle answers a held message's sender with what became of it, and tells the
// surface.
func (in *peerInbox) settle(held *heldPeerMessage, outcome, reason string) {
	status := peer.PeerMessageStatus{
		Status:    outcome,
		Reason:    reason,
		OrigMsgID: held.msgID,
	}
	if outcome == event.PeerOutcomeExpired {
		status.Status = peer.StatusExpired
	}
	in.sendReceipt(context.Background(), held.arrival, status)
	in.publishSettled(context.Background(), held.target.SessionID, held.msgID, outcome)
}

// deliverTo hands the message to the agent it is addressed to, and reports why
// it could not when it cannot (design §5.2).
func (in *peerInbox) deliverTo(ctx context.Context, a peerArrival, target peerTarget, envelope, msgID, fromName string) string {
	if target.AgentKey != "" {
		return in.deliverToSubagent(ctx, a, target, envelope, msgID, fromName)
	}
	return in.deliverToPrimary(ctx, target.SessionID, envelope, msgID, fromName)
}

// deliverToSubagent steers a subagent that is running now. A subagent that has
// finished is no longer anyone's to steer: it handed its result back, so the
// message goes to the agent that dispatched it, with a note saying what
// happened.
func (in *peerInbox) deliverToSubagent(ctx context.Context, a peerArrival, target peerTarget, envelope, msgID, fromName string) string {
	runner, ok := in.svc.env.BoundRunnerForSession(ctx, target.SessionID)
	if !ok || runner == nil {
		return peerRefusedNoConversation
	}
	// The message is wrapped as the peer envelope it is, and marked meta with
	// its origin: an agent reading it must see what the sender wrote and where
	// it came from, and must not have its own view re-pointed at this surface.
	_, err := run.SteerLiveSubagent(runner, target.SessionID, target.AgentKey, peerInput(envelope, state.MessageOriginPeer))
	switch {
	case err == nil:
		return ""
	case errors.Is(err, run.ErrSubagentNotRunning):
		note := fmt.Sprintf(peerForwardedToDispatcherNote, target.AgentName)
		if refusal := in.deliverToPrimary(ctx, target.SessionID, note, msgID, fromName); refusal != "" {
			return refusal
		}
		return in.deliverToPrimary(ctx, target.SessionID, envelope, msgID, fromName)
	case errors.Is(err, run.ErrSubagentNotFound):
		return peerRefusedAgentGone
	default:
		slog.Debug("peer message could not reach a subagent", "session_id", target.SessionID, "agent", target.AgentKey, "err", err)
		return peerRefusedAgentNotRunning
	}
}

// deliverToPrimary hands the message to a conversation's own agent: into the
// turn that is running, behind the gate it is parked on, or as a turn of its own
// when the conversation is idle.
func (in *peerInbox) deliverToPrimary(ctx context.Context, sessionID, envelope, msgID, fromName string) string {
	return in.deliverPrimary(ctx, sessionID, PeerPrompt{
		SessionID: sessionID,
		Text:      envelope,
		Origin:    state.MessageOriginPeer,
		MsgID:     msgID,
		FromName:  fromName,
	})
}

// deliverNote hands one of this process's own notes to a conversation — what
// became of the messages it sent (plan 197 §3.6) — the same way a peer message
// goes: into the running turn, behind the gate, or as a turn of its own. Its
// origin is what makes it a delivery update rather than a message from
// somebody.
func (in *peerInbox) deliverNote(ctx context.Context, sessionID, text string) string {
	return in.deliverPrimary(ctx, sessionID, PeerPrompt{
		SessionID: sessionID,
		Text:      text,
		Origin:    state.MessageOriginPeerStatus,
	})
}

// deliverPrimary is the one delivery table a conversation's own agent is
// reached through, whatever the message is: a peer's envelope or a note this
// process wrote about one.
func (in *peerInbox) deliverPrimary(ctx context.Context, sessionID string, prompt PeerPrompt) string {
	if in.svc.env == nil || in.svc.env.Control == nil {
		return peerRefusedInboxNotAttached
	}
	if runID, phase, ok := in.svc.env.Control.Find(sessionID); ok {
		switch phase {
		case run.Running:
			if in.svc.env.Control.Steer(runID, peerInput(prompt.Text, prompt.Origin)) {
				in.noteHanded(sessionID)
				return ""
			}
		case run.Waiting, run.Finishing:
			if in.svc.env.Control.FollowUp(runID, peerInput(prompt.Text, prompt.Origin)) {
				in.noteHanded(sessionID)
				return ""
			}
		}
		// The run ended between the lookup and the delivery: the message is a
		// turn of its own from here, exactly as if it had arrived a moment later.
	}
	if in.svc.env.Deps.RunRT != nil {
		if err := in.svc.env.Deps.RunRT.SessionBusy(ctx, sessionID); err != nil {
			return peerRefusedBusyElsewhere
		}
	}
	if in.surface.Wake == nil {
		return peerRefusedInboxNotAttached
	}
	if err := in.surface.Wake(ctx, prompt); err != nil {
		if !errors.Is(err, ErrPeerWakeUnavailable) {
			slog.Debug("peer wake failed", "session_id", sessionID, "err", err)
		}
		return peerRefusedInboxNotAttached
	}
	in.noteHanded(sessionID)
	return ""
}

// peerInput is a peer message as the engine's input: the envelope as the text,
// one text part, and the origin that marks the row as written elsewhere.
func peerInput(envelope, origin string) run.Input {
	return run.Input{
		Text:   envelope,
		Parts:  []llm.ContentPart{llm.Text(envelope)},
		Origin: origin,
	}
}

// noteHanded records that one more peer message is now the conversation's turn
// to run.
func (in *peerInbox) noteHanded(sessionID string) {
	in.mu.Lock()
	in.handed[sessionID]++
	in.mu.Unlock()
}

// refuse answers a message this process will not take. The wire value is
// "expired" with the detail "refused" — Claude Code's own encoding.
func (in *peerInbox) refuse(ctx context.Context, a peerArrival, reason string) {
	in.sendReceipt(ctx, a, peer.RefusedStatus(reason, peer.EncodeUDS(a.Local), a.Frame.MsgID))
	slog.Debug("peer message refused", "reason", reason, "msg_id", a.Frame.MsgID, "from", a.Frame.From)
}

// sendReceipt answers a frame's sender, and only where a reply may go: back into
// the same socket directory, or into a default one this user owns when the peer
// said it takes replies there.
func (in *peerInbox) sendReceipt(ctx context.Context, a peerArrival, status peer.PeerMessageStatus) {
	scheme, target := peer.DecodeAddress(a.Frame.From)
	if scheme != "uds" || target == "" {
		return
	}
	if !peer.ReplyTargetAllowed(target, a.Local, os.Getuid(), a.PeerFeatures) {
		slog.Debug("peer receipt has nowhere allowed to go", "from", a.Frame.From)
		return
	}
	status.From = peer.EncodeUDS(a.Local)
	if ctx == nil {
		ctx = context.Background()
	}
	sendCtx, cancel := context.WithTimeout(ctx, peerReceiptTimeout)
	defer cancel()
	if err := peer.Send(sendCtx, target, status.Frame(), peer.SendOptions{
		NoFollowSymlink: true,
		ExpectPID:       a.PeerPID,
		Timeout:         peerReceiptTimeout,
	}); err != nil {
		slog.Debug("peer receipt could not be sent", "to", target, "status", status.Status, "err", err)
	}
}

// handleControlFrame is everything a control frame asks for.
func (in *peerInbox) handleControlFrame(ctx context.Context, a peerArrival) {
	switch a.Frame.Action {
	case peer.ActionPeerMessageStatus:
		in.handleReceipt(ctx, a)
	case peer.ActionNotifyWhenIdle:
		in.handleNotifyWhenIdle(ctx, a)
	case peer.ActionPeerIdleNotice:
		slog.Debug("peer idle notice ignored; this process subscribes to nobody", "from", a.Frame.From)
	case peer.ActionRename:
		slog.Debug("peer rename ignored", "from", a.Frame.From)
	default:
		slog.Debug("peer control action ignored", "action", a.Frame.Action)
	}
}

// handleReceipt is a peer's answer about a message this process sent.
func (in *peerInbox) handleReceipt(ctx context.Context, a peerArrival) {
	if in.onReceipt == nil {
		return
	}
	in.onReceipt(peer.PeerMessageStatus{
		Status:        a.Frame.Status,
		Reason:        a.Frame.Reason,
		From:          a.Frame.From,
		OrigMsgID:     a.Frame.OrigMsgID,
		StatusDetail:  a.Frame.StatusDetail,
		DropReason:    a.Frame.DropReason,
		DroppedMsgIDs: a.Frame.DroppedMsgIDs,
	}, a.Frame.From)
}

// handleNotifyWhenIdle subscribes a peer to this conversation going idle, or
// answers at once when it already is.
func (in *peerInbox) handleNotifyWhenIdle(ctx context.Context, a peerArrival) {
	scheme, replyTo := peer.DecodeAddress(a.Frame.From)
	if scheme != "uds" || replyTo == "" {
		return
	}
	if !peer.ReplyTargetAllowed(replyTo, a.Local, os.Getuid(), a.PeerFeatures) {
		slog.Debug("peer idle subscription has nowhere allowed to answer", "from", a.Frame.From)
		return
	}
	target, refusal := in.route(ctx, a, peer.Envelope{})
	if refusal != "" {
		slog.Debug("peer idle subscription refused", "reason", refusal, "from", a.Frame.From)
		return
	}
	sid := target.SessionID
	if !in.runningHere(sid) {
		notice := peer.PeerIdleNotice{
			OrigMsgID:  a.Frame.MsgID,
			State:      peerIdleNoticeStateIdle,
			FinishedAt: in.finishedAtMs(ctx, sid),
		}
		in.sendIdleNotice(ctx, a, replyTo, notice)
		return
	}
	in.mu.Lock()
	if len(in.subscriptions) >= peerSubscriptionLimit {
		// The oldest subscription is the one most likely abandoned; a peer that
		// still cares will ask again.
		var oldest *peerIdleSubscription
		var oldestID string
		for id, sub := range in.subscriptions {
			if oldest == nil || sub.createdAt.Before(oldest.createdAt) {
				oldest, oldestID = sub, id
			}
		}
		delete(in.subscriptions, oldestID)
	}
	in.subscriptions[a.Frame.MsgID] = &peerIdleSubscription{
		origMsgID: a.Frame.MsgID,
		replyTo:   replyTo,
		arrival:   a,
		sessionID: sid,
		createdAt: time.Now(),
	}
	in.mu.Unlock()
}

// finishedAtMs is when the conversation's last turn ended, for an idle notice.
func (in *peerInbox) finishedAtMs(ctx context.Context, sessionID string) *int64 {
	in.mu.Lock()
	at, known := in.idleSince[sessionID]
	in.mu.Unlock()
	if !known {
		at = 0
		if in.svc.env != nil && in.svc.env.Deps.SessionStore != nil {
			if last, err := in.svc.env.Deps.SessionStore.LastActiveByIDs(ctx, []string{sessionID}); err == nil {
				// Session rows carry seconds; the notice carries milliseconds.
				at = last[sessionID] * 1000
			}
		}
	}
	if at <= 0 {
		return nil
	}
	return &at
}

// sendIdleNotice tells a subscriber what became of its wait.
func (in *peerInbox) sendIdleNotice(ctx context.Context, a peerArrival, replyTo string, notice peer.PeerIdleNotice) {
	if ctx == nil {
		ctx = context.Background()
	}
	sendCtx, cancel := context.WithTimeout(ctx, peerReceiptTimeout)
	defer cancel()
	if err := peer.Send(sendCtx, replyTo, notice.Frame(), peer.SendOptions{
		NoFollowSymlink: true,
		ExpectPID:       a.PeerPID,
		Timeout:         peerReceiptTimeout,
	}); err != nil {
		slog.Debug("peer idle notice could not be sent", "to", replyTo, "state", notice.State, "err", err)
	}
}

// tick is the slow duty: notice a conversation that went idle, let go of
// subscriptions nobody can answer for any more, and forget the messages a
// conversation with no run has already run. It takes only its own lock, so the
// service's tick loop can call it before it takes its own.
func (in *peerInbox) tick(ctx context.Context) {
	now := time.Now()
	type notice struct {
		arrival peerArrival
		to      string
		notice  peer.PeerIdleNotice
	}
	var notices []notice

	in.mu.Lock()
	for id, sub := range in.subscriptions {
		if now.Sub(sub.createdAt) < peerSubscriptionTTL {
			continue
		}
		delete(in.subscriptions, id)
	}
	// A conversation that has just stopped running answers every peer waiting
	// on it, and is owed nothing more: what it was handed has been consumed.
	idleNow := map[string][]*peerIdleSubscription{}
	for id, sub := range in.subscriptions {
		if in.runningHere(sub.sessionID) {
			delete(in.idleSince, sub.sessionID)
			continue
		}
		delete(in.subscriptions, id)
		idleNow[sub.sessionID] = append(idleNow[sub.sessionID], sub)
	}
	for sessionID := range in.idleSince {
		if !in.runningHere(sessionID) {
			continue
		}
		delete(in.idleSince, sessionID)
	}
	for sessionID := range idleNow {
		in.idleSince[sessionID] = now.UnixMilli()
	}
	in.mu.Unlock()

	// What a conversation was handed is owed to the run it was handed to, so a
	// conversation with no run here has run it and is queued for nobody. This
	// walks `handed` itself: `idleSince` only holds conversations a subscription
	// made idle, and one that was merely handed messages has no key there.
	in.mu.Lock()
	for sessionID := range in.handed {
		if !in.runningHere(sessionID) {
			delete(in.handed, sessionID)
		}
	}
	in.mu.Unlock()

	for sessionID, subs := range idleNow {
		finished := now.UnixMilli()
		for _, sub := range subs {
			notices = append(notices, notice{
				arrival: sub.arrival,
				to:      sub.replyTo,
				notice: peer.PeerIdleNotice{
					OrigMsgID:  sub.origMsgID,
					State:      peerIdleNoticeStateIdle,
					FinishedAt: &finished,
				},
			})
		}
		// The conversation is between turns: whatever it was handed has been
		// run, so it no longer owes the protocol a queue.
		in.mu.Lock()
		delete(in.handed, sessionID)
		in.mu.Unlock()
	}
	for _, n := range notices {
		in.sendIdleNotice(ctx, n.arrival, n.to, n.notice)
	}
}

// teardown answers everything still waiting when the registration goes away:
// held messages expire, and subscriptions are told whether this process will be
// back. It runs under the service's own lock and before the old socket closes,
// so both answers go out on the socket they belong to.
func (in *peerInbox) teardown(reason peerInboxTeardown) {
	ctx, cancel := context.WithTimeout(context.Background(), peerTeardownBudget)
	defer cancel()

	in.mu.Lock()
	held := in.held
	in.held = nil
	subs := in.subscriptions
	in.subscriptions = map[string]*peerIdleSubscription{}
	in.handed = map[string]int{}
	in.mu.Unlock()

	for _, holding := range held {
		in.sendReceipt(ctx, holding.arrival, peer.PeerMessageStatus{
			Status:    peer.StatusExpired,
			Reason:    peerReceiptExpired,
			OrigMsgID: holding.msgID,
		})
		in.publishSettled(ctx, holding.target.SessionID, holding.msgID, event.PeerOutcomeExpired)
	}
	state := peerIdleNoticeStateUnavailable
	if reason == peerTeardownStopped {
		state = peerIdleNoticeStateExited
	}
	for _, sub := range subs {
		in.sendIdleNotice(ctx, sub.arrival, sub.replyTo, peer.PeerIdleNotice{
			OrigMsgID: sub.origMsgID,
			State:     state,
		})
	}
}

// ReleasePeerMessage delivers a held peer message: the person allowed it.
func (env *Environment) ReleasePeerMessage(msgID string) error {
	in := env.peerInboxState()
	if in == nil {
		return errors.New("peer: no peer service is running")
	}
	return in.release(msgID)
}

// DenyPeerMessage answers a held peer message with a refusal: the person
// declined it.
func (env *Environment) DenyPeerMessage(msgID string) error {
	in := env.peerInboxState()
	if in == nil {
		return errors.New("peer: no peer service is running")
	}
	return in.deny(msgID)
}

// HeldPeerMessages lists the peer messages waiting for the person's decision in
// one conversation, oldest first.
func (env *Environment) HeldPeerMessages(sessionID string) []HeldPeerMessage {
	in := env.peerInboxState()
	if in == nil {
		return nil
	}
	return in.heldMessages(sessionID)
}

// release delivers one held message and answers its sender.
func (in *peerInbox) release(msgID string) error {
	held := in.takeHeld(msgID)
	if held == nil {
		return fmt.Errorf("peer: no held message %q", msgID)
	}
	ctx := context.Background()
	if refusal := in.deliverTo(ctx, held.arrival, held.target, held.envelope, held.msgID, held.fromName); refusal != "" {
		in.sendReceipt(ctx, held.arrival, peer.RefusedStatus(refusal, peer.EncodeUDS(held.arrival.Local), held.msgID))
		in.publishSettled(ctx, held.target.SessionID, held.msgID, event.PeerOutcomeDenied)
		return errors.New("peer: " + refusal)
	}
	in.sendReceipt(ctx, held.arrival, peer.PeerMessageStatus{
		Status:    peer.StatusDelivered,
		Reason:    peerReceiptDelivered,
		OrigMsgID: held.msgID,
	})
	in.publishSettled(ctx, held.target.SessionID, held.msgID, event.PeerOutcomeReleased)
	return nil
}

// deny answers one held message with the person's refusal.
func (in *peerInbox) deny(msgID string) error {
	held := in.takeHeld(msgID)
	if held == nil {
		return fmt.Errorf("peer: no held message %q", msgID)
	}
	ctx := context.Background()
	in.sendReceipt(ctx, held.arrival, peer.PeerMessageStatus{
		Status:    peer.StatusDenied,
		Reason:    peerReceiptDenied,
		OrigMsgID: held.msgID,
	})
	in.publishSettled(ctx, held.target.SessionID, held.msgID, event.PeerOutcomeDenied)
	return nil
}

// takeHeld removes one message from the hold buffer.
func (in *peerInbox) takeHeld(msgID string) *heldPeerMessage {
	in.mu.Lock()
	defer in.mu.Unlock()
	for i, held := range in.held {
		if held.msgID != msgID {
			continue
		}
		in.held = append(in.held[:i], in.held[i+1:]...)
		return held
	}
	return nil
}

// heldMessages is the hold buffer as a surface shows it.
func (in *peerInbox) heldMessages(sessionID string) []HeldPeerMessage {
	in.mu.Lock()
	defer in.mu.Unlock()
	out := make([]HeldPeerMessage, 0, len(in.held))
	for _, held := range in.held {
		if sessionID != "" && held.target.SessionID != sessionID {
			continue
		}
		out = append(out, HeldPeerMessage{
			MsgID:    held.msgID,
			From:     held.from,
			FromName: held.fromName,
			Preview:  peerPreview(held.envelope),
			Body:     peerEnvelopeBody(held.envelope),
			Cause:    held.cause,
			HeldAtMs: held.heldAt.UnixMilli(),
		})
	}
	return out
}

// peerInboxState is the running service's inbox, or nil when no peer service is
// running in this process.
func (env *Environment) peerInboxState() *peerInbox {
	if env == nil {
		return nil
	}
	env.reloadMu.Lock()
	svc := env.peers
	env.reloadMu.Unlock()
	if svc == nil {
		return nil
	}
	return svc.inbox
}

// sameWorkTree reports whether two directories are one work tree: the same
// directory once links are resolved, or one inside the other.
func sameWorkTree(a, b string) bool {
	left, right := resolvePath(a), resolvePath(b)
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}
	return strings.HasPrefix(left, right+string(filepath.Separator)) ||
		strings.HasPrefix(right, left+string(filepath.Separator))
}

// resolvePath is a directory with its links resolved, for comparing two
// spellings of one place. A path that does not resolve is compared as written.
func resolvePath(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(dir)
}

// frameString reads a frame field this package does not model as a string. A
// value that is not a JSON string reads as empty, which is what a sender that
// wrote something else there meant.
func frameString(raw []byte) string {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return ""
	}
	return strings.TrimSpace(text)
}
