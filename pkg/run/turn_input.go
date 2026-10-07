// Turn input: the runtime, its queue, and steer/follow-up handling.
package run

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

type TurnInputMode string

const (
	TurnInputModeSteer    TurnInputMode = "steer"
	TurnInputModeFollowUp TurnInputMode = "follow_up"
)

type TurnInputEntry struct {
	Mode  TurnInputMode
	Parts []llm.ContentPart
	// Seq is the queue's shared enqueue clock, carried into the runtime so the
	// queue can reconcile its own lanes when the runtime reports a delivery.
	Seq int
}

type TurnInputRuntime struct {
	mu       sync.Mutex
	pending  []TurnInputEntry
	onChange []func(delivered []TurnInputEntry)
}

func NewTurnInputRuntime() *TurnInputRuntime {
	return &TurnInputRuntime{}
}

func (r *TurnInputRuntime) Enqueue(mode TurnInputMode, parts []llm.ContentPart) {
	r.enqueueEntry(TurnInputEntry{Mode: mode, Parts: parts})
}

// enqueueEntry stamps the entry with the shared clock before it joins the
// runtime's pending list.
func (r *TurnInputRuntime) enqueueEntry(entry TurnInputEntry) {
	if r == nil || len(entry.Parts) == 0 {
		return
	}
	cloned := append([]llm.ContentPart(nil), entry.Parts...)
	r.mu.Lock()
	r.pending = append(r.pending, TurnInputEntry{Mode: entry.Mode, Parts: cloned, Seq: entry.Seq})
	r.mu.Unlock()
}

func (r *TurnInputRuntime) DrainSteers() []TurnInputEntry {
	return r.drainSteers(nil)
}

func (r *TurnInputRuntime) drainSteers(ctx context.Context) []TurnInputEntry {
	out := r.takeSteers(ctx)
	r.notifyDelivered(out)
	return out
}

// takeSteers removes the queued steers without reporting them as delivered.
func (r *TurnInputRuntime) takeSteers(ctx context.Context) []TurnInputEntry {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if len(r.pending) == 0 || (ctx != nil && ctx.Err() != nil) {
		r.mu.Unlock()
		return nil
	}
	out := make([]TurnInputEntry, 0, len(r.pending))
	rest := make([]TurnInputEntry, 0, len(r.pending))
	for _, entry := range r.pending {
		if entry.Mode == TurnInputModeSteer {
			out = append(out, TurnInputEntry{
				Mode:  entry.Mode,
				Parts: append([]llm.ContentPart(nil), entry.Parts...),
				Seq:   entry.Seq,
			})
			continue
		}
		rest = append(rest, entry)
	}
	r.pending = rest
	r.mu.Unlock()
	return out
}

func (r *TurnInputRuntime) notifyDelivered(delivered []TurnInputEntry) {
	if r == nil || len(delivered) == 0 {
		return
	}
	r.mu.Lock()
	notify := append([]func([]TurnInputEntry){}, r.onChange...)
	r.mu.Unlock()
	for _, hook := range notify {
		if hook != nil {
			hook(delivered)
		}
	}
}

// restoreSteers puts entries back at the head of the queue. Steers are drained
// FIFO, so entries that came out first must go back in first for the next drain
// to hand the model the same order the user typed.
func (r *TurnInputRuntime) restoreSteers(entries []TurnInputEntry) {
	if r == nil || len(entries) == 0 {
		return
	}
	r.mu.Lock()
	restored := make([]TurnInputEntry, 0, len(entries)+len(r.pending))
	restored = append(restored, entries...)
	restored = append(restored, r.pending...)
	r.pending = restored
	r.mu.Unlock()
}

// SteerDelivery is one in-flight handoff of queued steers to the model. The
// entries have left the queue - they cannot be retracted while a model call is
// carrying them - but they are not reported as delivered until Commit.
//
// Delivery is not settled by the drain itself. The orchestration loop drains at
// a tool boundary, appends the entries to the session, and only then makes the
// model call that carries them; that call can still fail before the provider
// responds (429, 5xx, network) or be cancelled. Reporting the drain as delivery
// strands the message: the surface takes "delivered" to mean the model has it,
// so it drops the message from the queue preview and renders it into the
// transcript as a sent user message - and then nothing answers it and no turn
// boundary resubmits it. The first model-output event commits delivery; failure
// before that boundary rolls it back.
type SteerDelivery struct {
	rt      *TurnInputRuntime
	entries []TurnInputEntry
	mu      sync.Mutex
	settled bool
}

// BeginSteerDelivery takes the queued steers for a model call that is about to
// be made, without yet reporting them as delivered. It returns nil when nothing
// is queued or ctx is already cancelled, so callers can treat a nil delivery as
// "no steers to carry". Every non-nil delivery must be settled with exactly one
// effective Commit (the call began producing output or returned successfully) or
// Rollback (it failed before either boundary).
//
// The cancellation check is a shortcut, not the safety net: a cancellation that
// lands after the entries are taken is caught by the failing call's Rollback
// like any other failure. It matters because the window is wide in practice -
// esc cancels the run while tools are executing, those tools return their
// context errors, and the loop reaches the tool boundary before the next model
// call observes the cancellation - so skipping the take avoids churning the
// queue on a turn that is already over.
func (r *TurnInputRuntime) BeginSteerDelivery(ctx context.Context) *SteerDelivery {
	entries := r.takeSteers(ctx)
	if len(entries) == 0 {
		return nil
	}
	return &SteerDelivery{rt: r, entries: entries}
}

// Entries returns the steers this delivery is carrying.
func (d *SteerDelivery) Entries() []TurnInputEntry {
	if d == nil {
		return nil
	}
	return d.entries
}

// Commit reports the entries as delivered. It is called before the first model
// output event is forwarded, or after a successful non-streaming
// call. It returns true only for the transition that committed the delivery.
func (d *SteerDelivery) Commit() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	if d.settled {
		d.mu.Unlock()
		return false
	}
	d.settled = true
	d.mu.Unlock()
	d.rt.notifyDelivered(d.entries)
	return true
}

// Rollback returns the entries to the queue because the call that was to carry
// them failed before establishing delivery. No change hook fires: the surface
// was never told they left the queue, so its mirror still holds them and the
// turn boundary resubmits them from there. It returns true only when this call
// won the settlement and actually restored the entries.
func (d *SteerDelivery) Rollback() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	if d.settled {
		d.mu.Unlock()
		return false
	}
	d.settled = true
	d.mu.Unlock()
	d.rt.restoreSteers(d.entries)
	return true
}

// RetractLastSteer removes and returns the most recently enqueued steer entry
// that has not yet been drained. It returns false when no steer is pending
// (already delivered at a tool boundary, or never queued), which lets callers
// distinguish a still-editable steer from one the agent has already consumed.
func (r *TurnInputRuntime) RetractLastSteer() ([]llm.ContentPart, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	for i := len(r.pending) - 1; i >= 0; i-- {
		if r.pending[i].Mode != TurnInputModeSteer {
			continue
		}
		parts := append([]llm.ContentPart(nil), r.pending[i].Parts...)
		r.pending = append(r.pending[:i], r.pending[i+1:]...)
		r.mu.Unlock()
		return parts, true
	}
	r.mu.Unlock()
	return nil, false
}

func (r *TurnInputRuntime) DrainAll() []TurnInputEntry {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if len(r.pending) == 0 {
		r.mu.Unlock()
		return nil
	}
	out := make([]TurnInputEntry, 0, len(r.pending))
	for _, entry := range r.pending {
		out = append(out, TurnInputEntry{
			Mode:  entry.Mode,
			Parts: append([]llm.ContentPart(nil), entry.Parts...),
			Seq:   entry.Seq,
		})
	}
	r.pending = nil
	notify := append([]func([]TurnInputEntry){}, r.onChange...)
	r.mu.Unlock()
	for _, hook := range notify {
		if hook != nil {
			hook(out)
		}
	}
	return out
}

func (r *TurnInputRuntime) SetChangeHook(fn func(delivered []TurnInputEntry)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if fn == nil {
		r.onChange = nil
	} else {
		r.onChange = []func([]TurnInputEntry){fn}
	}
	r.mu.Unlock()
}

// AddChangeHook registers an additional delivery observer.
func (r *TurnInputRuntime) AddChangeHook(fn func(delivered []TurnInputEntry)) {
	if r == nil || fn == nil {
		return
	}
	r.mu.Lock()
	r.onChange = append(r.onChange, fn)
	r.mu.Unlock()
}

func (r *TurnInputRuntime) Snapshot() []TurnInputEntry {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pending) == 0 {
		return nil
	}
	out := make([]TurnInputEntry, 0, len(r.pending))
	for _, entry := range r.pending {
		out = append(out, TurnInputEntry{
			Mode:  entry.Mode,
			Parts: append([]llm.ContentPart(nil), entry.Parts...),
			Seq:   entry.Seq,
		})
	}
	return out
}

func (r *TurnInputRuntime) HasSteers() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, entry := range r.pending {
		if entry.Mode == TurnInputModeSteer {
			return true
		}
	}
	return false
}

func (r *TurnInputRuntime) HasFollowUps() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, entry := range r.pending {
		if entry.Mode == TurnInputModeFollowUp {
			return true
		}
	}
	return false
}

type turnInputRuntimeCtxKey struct{}

func WithTurnInputRuntime(ctx context.Context, rt *TurnInputRuntime) context.Context {
	if rt == nil {
		return ctx
	}
	return context.WithValue(ctx, turnInputRuntimeCtxKey{}, rt)
}

func TurnInputRuntimeFromContext(ctx context.Context) *TurnInputRuntime {
	if ctx == nil {
		return nil
	}
	rt, _ := ctx.Value(turnInputRuntimeCtxKey{}).(*TurnInputRuntime)
	return rt
}

// QueuePreview is the transport-neutral view of queued turn input.
type QueuePreview struct {
	Steers   []string
	Rejected []string
	FollowUp []string
}

// Visible reports whether the preview contains queued input.
func (p QueuePreview) Visible() bool {
	return len(p.Steers) > 0 || len(p.Rejected) > 0 || len(p.FollowUp) > 0
}

// InputQueue is the queue semantics one conversation's user input follows:
// what steers the active turn, what was refused and waits as a rejected
// steer, what waits as an ordinary follow-up, and what comes back when a
// turn ends. The TUI's behavior is the spec (plan 004): one shared enqueue
// clock orders every message across the three lanes, recall picks the newest
// by that clock, and the boundary a turn ended on decides what is sent next
// versus restored to the composer. Surfaces render the preview and merge
// their own Payload; they never pick among queued messages themselves.
//
// The queue belongs to the conversation; the steer runtime belongs to the
// turn. Attach connects this turn's runtime at the turn's start and Detach
// retires it at the turn's end, so input queued for one turn can never be
// delivered into the next: the next turn attaches a fresh runtime, and a
// steer that arrives while no runtime is attached is refused into the
// rejected lane.
type InputQueue struct {
	mu        sync.Mutex
	rt        *TurnInputRuntime
	steers    []Input // undelivered steers in enqueue order; mirrors the attached runtime while one is attached
	rejected  []Input // steers the run refused or that outlived their turn
	followUp  []Input // ordinary follow-ups
	delivered []Input // steers the model received, waiting for the surface to render them
	seq       int
	hook      func()
}

// NewInputQueue creates an empty, detached conversation queue.
func NewInputQueue() *InputQueue {
	return &InputQueue{}
}

// Attach connects the queue to this turn's steer runtime: steers enqueued
// while it is attached reach the model at that turn's tool boundaries.
// Attach also retires a runtime still connected by a turn that ended without
// Detach, the same way Detach does.
func (q *InputQueue) Attach(rt *TurnInputRuntime) {
	if q == nil || rt == nil {
		return
	}
	q.mu.Lock()
	q.rt = rt
	hook := q.hook
	q.mu.Unlock()
	rt.AddChangeHook(q.steersDelivered)
	if hook != nil {
		hook()
	}
}

// Detach retires the attached turn's runtime. Undelivered steers stay in the
// queue — the turn's boundary decides what follows them — but without a
// runtime they can no longer reach any model: the next turn attaches a fresh
// runtime, so a steer queued for the ended turn is never delivered into the
// next one.
func (q *InputQueue) Detach() {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.rt = nil
	hook := q.hook
	q.mu.Unlock()
	if hook != nil {
		hook()
	}
}

// steersDelivered reconciles the steer lane with what the attached runtime
// just handed to the model, moving those steers onto the delivered list so
// the surface can render them as the messages they are, whole (Payload
// included).
func (q *InputQueue) steersDelivered(entries []TurnInputEntry) {
	if q == nil || len(entries) == 0 {
		return
	}
	q.mu.Lock()
	var delivered []Input
	for _, entry := range entries {
		if entry.Mode != TurnInputModeSteer {
			continue
		}
		for i, in := range q.steers {
			if in.Seq != entry.Seq {
				continue
			}
			q.steers = append(q.steers[:i], q.steers[i+1:]...)
			delivered = append(delivered, in)
			break
		}
	}
	q.delivered = append(q.delivered, delivered...)
	hook := q.hook
	q.mu.Unlock()
	if len(delivered) > 0 && hook != nil {
		hook()
	}
}

// TakeDelivered hands the surface the steers the model has received, in
// delivery order, so it can render them into the transcript instead of
// letting them silently leave the queue preview.
func (q *InputQueue) TakeDelivered() []Input {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	out := q.delivered
	q.delivered = nil
	q.mu.Unlock()
	return out
}

// Runtime returns the steer runtime attached to the current turn, or nil
// between turns.
func (q *InputQueue) Runtime() *TurnInputRuntime {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.rt
}

// SetChangeHook runs after the queue changes: a steer enqueued, refused,
// recalled or delivered, a follow-up queued, or a boundary consuming
// entries. Adapters use it to republish the queue preview.
func (q *InputQueue) SetChangeHook(hook func()) {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.hook = hook
	q.mu.Unlock()
}

// Steer queues a message to reach the model at the attached turn's next tool
// boundary. Any message with parts steers — what it attaches travels in
// Payload, so it can still be handed back whole; the old refusal of attached
// messages existed only because a steer could not carry them back, and the
// TUI (the spec) has always steered them. A queue with no attached runtime
// has no turn to deliver into: the message is kept as a rejected steer
// rather than waiting for a turn it was never queued for.
func (q *InputQueue) Steer(in Input) bool {
	if q == nil {
		return false
	}
	in = cloneInput(in)
	in.Text = cleanInputText(in)
	if in.Text == "" {
		in.Text = strings.Join(strings.Fields(llm.TextContent(in.Parts...)), " ")
	}
	in.Attachments = cleanAttachments(in.Attachments)
	in.MentionImages = cleanAttachments(in.MentionImages)
	if in.Text == "" && len(in.Parts) == 0 && len(in.Attachments) == 0 && len(in.MentionImages) == 0 {
		return false
	}
	q.mu.Lock()
	q.seq++
	in.Seq = q.seq
	if q.rt == nil {
		in.Rejected = true
		q.rejected = append(q.rejected, in)
		hook := q.hook
		q.mu.Unlock()
		if hook != nil {
			hook()
		}
		return false
	}
	parts := append([]llm.ContentPart(nil), in.Parts...)
	if len(parts) == 0 {
		parts = []llm.ContentPart{llm.Text(in.Text)}
	}
	q.steers = append(q.steers, in)
	q.rt.enqueueEntry(TurnInputEntry{Mode: TurnInputModeSteer, Parts: parts, Seq: in.Seq})
	hook := q.hook
	q.mu.Unlock()
	if hook != nil {
		hook()
	}
	return true
}

// FollowUp queues an input for after the current turn. An input marked
// Rejected is a steer the active run refused — an earlier failed delivery —
// and is kept in its own lane so the boundary sends it first.
func (q *InputQueue) FollowUp(in Input) bool {
	if q == nil {
		return false
	}
	in = cloneInput(in)
	in.Text = cleanInputText(in)
	in.Attachments = cleanAttachments(in.Attachments)
	in.MentionImages = cleanAttachments(in.MentionImages)
	if in.Text == "" && len(in.Attachments) == 0 && len(in.MentionImages) == 0 && len(in.Parts) == 0 {
		return false
	}
	q.mu.Lock()
	q.seq++
	in.Seq = q.seq
	if in.Rejected {
		q.rejected = append(q.rejected, in)
	} else {
		q.followUp = append(q.followUp, in)
	}
	hook := q.hook
	q.mu.Unlock()
	if hook != nil {
		hook()
	}
	return true
}

type queueLane int

const (
	laneSteer queueLane = iota
	laneRejected
	laneFollowUp
)

// newestLocked returns the lane and index of the highest-sequence message.
// Each lane is append-only in sequence order, so only its tail has to be
// considered.
func (q *InputQueue) newestLocked(skipSteers bool) (queueLane, int) {
	best := queueLane(-1)
	bestIdx, bestSeq := -1, 0
	consider := func(lane queueLane, idx, seq int) {
		if best < 0 || seq > bestSeq {
			best, bestIdx, bestSeq = lane, idx, seq
		}
	}
	if !skipSteers && len(q.steers) > 0 {
		consider(laneSteer, len(q.steers)-1, q.steers[len(q.steers)-1].Seq)
	}
	if len(q.rejected) > 0 {
		consider(laneRejected, len(q.rejected)-1, q.rejected[len(q.rejected)-1].Seq)
	}
	if len(q.followUp) > 0 {
		consider(laneFollowUp, len(q.followUp)-1, q.followUp[len(q.followUp)-1].Seq)
	}
	return best, bestIdx
}

// Recall pulls the most recently queued message back out for editing.
// "Most recently queued" is decided by the shared enqueue clock, not by lane
// precedence: the lanes are separate FIFOs, so picking one of them first
// would recall an older message whenever the newest happened to land in
// another lane. A pending steer also lives in the attached runtime, so that
// copy is retracted first; a retraction that fails means the run already
// delivered it to the model — and since the runtime drains FIFO, every older
// steer is gone too — so the whole steer lane is skipped rather than handing
// back an editable copy of a message that is being answered.
func (q *InputQueue) Recall() (Input, bool) {
	if q == nil {
		return Input{}, false
	}
	q.mu.Lock()
	skipSteers := false
	for {
		lane, idx := q.newestLocked(skipSteers)
		if lane < 0 {
			q.mu.Unlock()
			return Input{}, false
		}
		var laneEntries *[]Input
		switch lane {
		case laneSteer:
			if q.rt == nil {
				skipSteers = true
				continue
			}
			if _, ok := q.rt.RetractLastSteer(); !ok {
				skipSteers = true
				continue
			}
			laneEntries = &q.steers
		case laneRejected:
			laneEntries = &q.rejected
		default:
			laneEntries = &q.followUp
		}
		in := (*laneEntries)[idx]
		*laneEntries = append((*laneEntries)[:idx], (*laneEntries)[idx+1:]...)
		hook := q.hook
		q.mu.Unlock()
		if hook != nil {
			hook()
		}
		return in, true
	}
}

// Boundary is how one turn of a conversation ended, as far as its queue is
// concerned.
type Boundary int

const (
	// BoundaryCompleted means the turn ran to its end.
	BoundaryCompleted Boundary = iota
	// BoundaryInterruptToSend is an interrupt issued precisely to flush the
	// steers queued behind it: they are sent now, as one fresh turn.
	BoundaryInterruptToSend
	// BoundaryInterrupted is any other interruption or cancellation.
	BoundaryInterrupted
)

// Next decides what follows a turn that ended at b. Send is the input to run
// as the next turn, listed in engine order for the surface to merge into one
// message; Restore is every entry that goes back to the composer instead, in
// the order they were written. At most one of the two is non-empty.
func (q *InputQueue) Next(b Boundary) (send, restore []Input) {
	if q == nil {
		return nil, nil
	}
	q.mu.Lock()
	changed := false
	switch b {
	case BoundaryCompleted:
		// Rejected steers are an earlier failed delivery, so they go first;
		// then the steers the ended turn never delivered; then the first
		// queued follow-up. The caller returns at the next boundary until
		// both are empty, which is what drains the queue one turn at a time.
		if len(q.rejected) > 0 {
			send, q.rejected, changed = q.rejected, nil, true
			break
		}
		if len(q.steers) > 0 {
			q.consumeSteersLocked()
			send, q.steers, changed = q.steers, nil, true
			break
		}
		if len(q.followUp) > 0 {
			send = q.followUp[:1]
			q.followUp = q.followUp[1:]
			changed = true
		}
	case BoundaryInterruptToSend:
		if len(q.steers) > 0 {
			q.consumeSteersLocked()
			send, q.steers, changed = q.steers, nil, true
		}
	case BoundaryInterrupted:
		// Everything goes back to the composer; the surface appends its own
		// live draft after these, in write order.
		restore = append(restore, q.rejected...)
		restore = append(restore, q.steers...)
		q.consumeSteersLocked()
		restore = append(restore, q.followUp...)
		q.rejected, q.steers, q.followUp = nil, nil, nil
		changed = len(restore) > 0
	}
	hook := q.hook
	q.mu.Unlock()
	if changed && hook != nil {
		hook()
	}
	return send, restore
}

// consumeSteersLocked removes the undelivered steers from the attached
// runtime without reporting them as delivered: a boundary is consuming them,
// so they must not also reach the model through the runtime they were queued
// for.
func (q *InputQueue) consumeSteersLocked() {
	if q.rt != nil {
		q.rt.takeSteers(context.Background())
	}
}

// Discard empties every lane and returns how many messages were dropped.
// Pending steers are retracted from the attached runtime first: clearing
// only the queue would leave the runtime free to hand them to the model.
// Steers already delivered cannot be retracted and are not counted — the
// model has them, so they are not lost.
func (q *InputQueue) Discard() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	dropped := 0
	for len(q.steers) > 0 && q.rt != nil {
		if _, ok := q.rt.RetractLastSteer(); !ok {
			break
		}
		q.steers = q.steers[:len(q.steers)-1]
		dropped++
	}
	dropped += len(q.rejected) + len(q.followUp)
	q.steers, q.rejected, q.followUp, q.delivered = nil, nil, nil, nil
	hook := q.hook
	q.mu.Unlock()
	if hook != nil {
		hook()
	}
	return dropped
}

// TakeAll removes every queued entry and returns them ordered by the shared
// enqueue clock — the order the user wrote them. Steers are retracted from
// the attached runtime first, so nothing that left the queue can still reach
// the model. The withdrawal path merges them with the withdrawn message and
// the live draft into one editable composer payload.
func (q *InputQueue) TakeAll() []Input {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	q.consumeSteersLocked()
	all := make([]Input, 0, len(q.steers)+len(q.rejected)+len(q.followUp))
	all = append(all, q.steers...)
	all = append(all, q.rejected...)
	all = append(all, q.followUp...)
	sort.SliceStable(all, func(i, j int) bool { return all[i].Seq < all[j].Seq })
	q.steers, q.rejected, q.followUp, q.delivered = nil, nil, nil, nil
	hook := q.hook
	q.mu.Unlock()
	if len(all) > 0 && hook != nil {
		hook()
	}
	return all
}

// Preview returns a snapshot of queued input. Each entry's text is its
// surface's preview form: the TUI sets Text to its composer display text
// (keeping "[Image #1]"-style placeholders); the model-facing content is in
// Parts.
func (q *InputQueue) Preview() QueuePreview {
	if q == nil {
		return QueuePreview{}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.previewLocked()
}

func (q *InputQueue) previewLocked() QueuePreview {
	out := QueuePreview{Steers: []string{}, Rejected: []string{}, FollowUp: []string{}}
	for _, in := range q.steers {
		if text := previewInput(in); text != "" {
			out.Steers = append(out.Steers, text)
		}
	}
	for _, in := range q.rejected {
		if text := previewInput(in); text != "" {
			out.Rejected = append(out.Rejected, text)
		}
	}
	for _, in := range q.followUp {
		if text := previewInput(in); text != "" {
			out.FollowUp = append(out.FollowUp, text)
		}
	}
	return out
}

func cleanInputText(in Input) string { return strings.Join(strings.Fields(in.Text), " ") }

func cleanAttachments(in []string) []string {
	out := make([]string, 0, len(in))
	for _, item := range in {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func previewInput(in Input) string {
	if in.Text != "" {
		return in.Text
	}
	switch attached := len(in.Attachments) + len(in.MentionImages); attached {
	case 1:
		return "1 attachment"
	case 0:
		return ""
	default:
		return strconv.Itoa(attached) + " attachments"
	}
}

func cloneInput(in Input) Input {
	in.Parts = append([]llm.ContentPart(nil), in.Parts...)
	in.Attachments = append([]string(nil), in.Attachments...)
	in.MentionImages = append([]string(nil), in.MentionImages...)
	return in
}
