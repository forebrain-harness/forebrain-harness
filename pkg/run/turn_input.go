// Turn input: the runtime, its queue, and steer/follow-up handling.
package run

import (
	"context"
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
	if r == nil || len(parts) == 0 {
		return
	}
	cloned := append([]llm.ContentPart(nil), parts...)
	r.mu.Lock()
	r.pending = append(r.pending, TurnInputEntry{Mode: mode, Parts: cloned})
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

// WithoutTurnInputRuntime returns a context whose TurnInputRuntime slot is
// cleared. Subagents inherit the primary agent's context (and therefore its
// TurnInputRuntime), but steers enqueued by the user target the primary
// agent's loop — a subagent that drains them would both steal messages meant
// for the parent and render them prematurely in the transcript (the change
// hook fires on drain). Strip the runtime at every subagent entry point so
// the subagent's toolOrchestrationLLM never drains the parent's queue.
func WithoutTurnInputRuntime(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithValue(ctx, turnInputRuntimeCtxKey{}, (*TurnInputRuntime)(nil))
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

// InputQueue owns one turn's steer and follow-up queues.
type InputQueue struct {
	mu       sync.Mutex
	rt       *TurnInputRuntime
	followUp []Input
	quiet    bool
	hook     func()
}

// NewInputQueue creates an empty turn input queue.
func NewInputQueue() *InputQueue {
	return NewInputQueueWithRuntime(nil)
}

// NewInputQueueWithRuntime creates a queue around rt. Nil creates a new runtime.
func NewInputQueueWithRuntime(rt *TurnInputRuntime) *InputQueue {
	if rt == nil {
		rt = NewTurnInputRuntime()
	}
	q := &InputQueue{rt: rt}
	q.rt.AddChangeHook(func([]TurnInputEntry) {
		q.mu.Lock()
		quiet, hook := q.quiet, q.hook
		q.mu.Unlock()
		if !quiet && hook != nil {
			hook()
		}
	})
	return q
}

// Runtime returns the model-facing steer runtime.
func (q *InputQueue) Runtime() *TurnInputRuntime {
	if q == nil {
		return nil
	}
	return q.rt
}

// SetChangeHook runs after a steer is delivered to the model.
func (q *InputQueue) SetChangeHook(hook func()) {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.hook = hook
	q.mu.Unlock()
}

// Steer queues a text-only message for the next model boundary. A message
// that attaches files or workspace images waits for the next turn instead: a
// steer reaches the model as bare parts, and could not be handed back to its
// composer with what it attached.
func (q *InputQueue) Steer(in Input) bool {
	if q == nil || q.rt == nil {
		return false
	}
	text := cleanInputText(in)
	if text == "" {
		text = strings.Join(strings.Fields(llm.TextContent(in.Parts...)), " ")
	}
	if (text == "" && len(in.Parts) == 0) || len(cleanAttachments(in.Attachments)) > 0 || len(cleanAttachments(in.MentionImages)) > 0 {
		return false
	}
	parts := append([]llm.ContentPart(nil), in.Parts...)
	if len(parts) == 0 {
		parts = []llm.ContentPart{llm.Text(text)}
	}
	q.rt.Enqueue(TurnInputModeSteer, parts)
	return true
}

// FollowUp queues an input for the next turn.
func (q *InputQueue) FollowUp(in Input) bool {
	if q == nil {
		return false
	}
	in.Text = cleanInputText(in)
	in.Attachments = cleanAttachments(in.Attachments)
	in.MentionImages = cleanAttachments(in.MentionImages)
	if in.Text == "" && len(in.Attachments) == 0 && len(in.MentionImages) == 0 {
		return false
	}
	q.mu.Lock()
	q.followUp = append(q.followUp, cloneInput(in))
	q.mu.Unlock()
	return true
}

// RejectSteers moves undelivered steers behind the current turn.
func (q *InputQueue) RejectSteers() QueuePreview {
	if q == nil || q.rt == nil {
		return QueuePreview{}
	}
	q.mu.Lock()
	q.quiet = true
	q.mu.Unlock()
	entries := q.rt.DrainSteers()
	q.mu.Lock()
	q.quiet = false
	for _, entry := range entries {
		text := strings.Join(strings.Fields(llm.TextContent(entry.Parts...)), " ")
		if text != "" {
			q.followUp = append(q.followUp, Input{Text: text, Rejected: true})
		}
	}
	out := q.previewLocked()
	q.mu.Unlock()
	return out
}

// PopLatest returns the newest editable input. Pending steers take priority.
func (q *InputQueue) PopLatest() (Input, QueuePreview, bool) {
	if q == nil {
		return Input{}, QueuePreview{}, false
	}
	if q.rt != nil {
		if parts, ok := q.rt.RetractLastSteer(); ok {
			return Input{Text: strings.Join(strings.Fields(llm.TextContent(parts...)), " "), Parts: parts}, q.Preview(), true
		}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, rejected := range []bool{false, true} {
		for i := len(q.followUp) - 1; i >= 0; i-- {
			if q.followUp[i].Rejected != rejected {
				continue
			}
			in := q.followUp[i]
			q.followUp = append(q.followUp[:i], q.followUp[i+1:]...)
			return cloneInput(in), q.previewLocked(), true
		}
	}
	return Input{}, q.previewLocked(), false
}

// RetractSteer removes the latest steer that has not reached the model.
func (q *InputQueue) RetractSteer() (Input, bool) {
	if q == nil || q.rt == nil {
		return Input{}, false
	}
	parts, ok := q.rt.RetractLastSteer()
	if !ok {
		return Input{}, false
	}
	return Input{Text: strings.Join(strings.Fields(llm.TextContent(parts...)), " "), Parts: parts}, true
}

// DrainSteers removes queued steers in FIFO order.
func (q *InputQueue) DrainSteers() []Input {
	if q == nil || q.rt == nil {
		return nil
	}
	entries := q.rt.DrainSteers()
	out := make([]Input, 0, len(entries))
	for _, entry := range entries {
		out = append(out, Input{
			Text:  strings.Join(strings.Fields(llm.TextContent(entry.Parts...)), " "),
			Parts: append([]llm.ContentPart(nil), entry.Parts...),
		})
	}
	return out
}

// PopNext returns rejected steers as one input before ordinary follow-ups.
func (q *InputQueue) PopNext() (Input, bool) {
	if q == nil {
		return Input{}, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.followUp) == 0 {
		return Input{}, false
	}
	// Rejected steers merge into one message, and everything each one
	// attached travels with it.
	var texts []string
	merged := Input{Rejected: true}
	rest := make([]Input, 0, len(q.followUp))
	for _, in := range q.followUp {
		if !in.Rejected {
			rest = append(rest, in)
			continue
		}
		if in.Text != "" {
			texts = append(texts, in.Text)
		}
		merged.Attachments = append(merged.Attachments, in.Attachments...)
		merged.MentionImages = append(merged.MentionImages, in.MentionImages...)
	}
	if len(rest) < len(q.followUp) {
		q.followUp = rest
		merged.Text = strings.Join(texts, "\n\n")
		return merged, true
	}
	in := q.followUp[0]
	q.followUp = q.followUp[1:]
	return cloneInput(in), true
}

// Drain empties the queue: undelivered steers merged into one message first,
// then queued messages in the order they were sent.
func (q *InputQueue) Drain() []Input {
	if q == nil {
		return nil
	}
	q.RejectSteers()
	var drained []Input
	for {
		in, ok := q.PopNext()
		if !ok {
			return drained
		}
		drained = append(drained, in)
	}
}

// Preview returns a snapshot of queued input.
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
	if q.rt != nil {
		for _, entry := range q.rt.Snapshot() {
			text := strings.Join(strings.Fields(llm.TextContent(entry.Parts...)), " ")
			if entry.Mode == TurnInputModeSteer && text != "" {
				out.Steers = append(out.Steers, text)
			}
		}
	}
	for _, in := range q.followUp {
		text := previewInput(in)
		if text == "" {
			continue
		}
		if in.Rejected {
			out.Rejected = append(out.Rejected, text)
		} else {
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
