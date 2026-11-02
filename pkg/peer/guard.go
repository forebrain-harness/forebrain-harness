package peer

import (
	"container/list"
	"sync"
	"time"
)

// The short names of the drop reasons. The wire carries these and a human
// sentence alongside; both come from here so a receipt and a log agree.
const (
	ReasonRateLimited = "rate-limited"
	ReasonDuplicate   = "duplicate"
	ReasonHopLoop     = "hop-loop"
	ReasonHopRunaway  = "hop-runaway"
	ReasonQueueFull   = "queue-full"
)

// DropReasons is the sentence that accompanies each drop reason on a receipt.
var DropReasons = map[string]string{
	ReasonRateLimited: "sender exceeded the peer message rate limit",
	ReasonDuplicate:   "identical to the previous message from this sender",
	ReasonHopLoop:     "message has already passed through this session (a peer messaging loop)",
	ReasonHopRunaway:  "peer relay chain is too long (runaway forwarding)",
	ReasonQueueFull:   "this session has too many undelivered peer messages queued",
}

// Limits are the protocol's anti-ping-pong numbers: Claude Code's own, and not
// something to tune — the two sides have to agree on them.
type Limits struct {
	BucketCapacity        float64
	RefillPerSecond       float64
	DedupWindow           time.Duration
	MaxSelfHops           int
	MaxChainLength        int
	MaxTrackedSenders     int
	MaxQueuedPeerMessages int
}

// DefaultLimits is Claude Code 2.1.301's configuration.
var DefaultLimits = Limits{
	BucketCapacity:        30,
	RefillPerSecond:       0.5,
	DedupWindow:           30 * time.Second,
	MaxSelfHops:           10,
	MaxChainLength:        28,
	MaxTrackedSenders:     256,
	MaxQueuedPeerMessages: 50,
}

// senderBucket is one sender's token bucket and the last message it sent.
type senderBucket struct {
	key    string
	tokens float64
	refill time.Time
	body   string
	bodyAt time.Time
}

// bucketTable is the LRU of sender buckets. Senders come and go, so the table
// is capped: the least recently seen sender is evicted to keep a long-lived
// process from growing one entry per stranger it ever heard from.
type bucketTable struct {
	mu      sync.Mutex
	limits  Limits
	entries map[string]*senderBucket
	order   *list.List
}

// newBucketTable builds an empty table.
func newBucketTable(limits Limits) *bucketTable {
	return &bucketTable{
		limits:  limits,
		entries: map[string]*senderBucket{},
		order:   list.New(),
	}
}

// touchLocked returns the sender's bucket, refilled to now and moved to the
// front of the LRU. The caller holds the table's lock.
func (t *bucketTable) touchLocked(key string, now time.Time) *senderBucket {
	bucket, ok := t.entries[key]
	if !ok {
		bucket = &senderBucket{key: key, tokens: t.limits.BucketCapacity, refill: now}
		t.entries[key] = bucket
		t.order.PushFront(key)
		t.evictLocked()
		return bucket
	}
	elapsed := now.Sub(bucket.refill).Seconds()
	if elapsed > 0 {
		bucket.tokens += elapsed * t.limits.RefillPerSecond
		if bucket.tokens > t.limits.BucketCapacity {
			bucket.tokens = t.limits.BucketCapacity
		}
	}
	bucket.refill = now
	for element := t.order.Front(); element != nil; element = element.Next() {
		if element.Value == key {
			t.order.MoveToFront(element)
			break
		}
	}
	return bucket
}

// evictLocked drops the least recently used senders beyond the cap.
func (t *bucketTable) evictLocked() {
	for t.order.Len() > t.limits.MaxTrackedSenders {
		oldest := t.order.Back()
		if oldest == nil {
			return
		}
		t.order.Remove(oldest)
		delete(t.entries, oldest.Value.(string))
	}
}

// Admission decides whether an incoming peer message may be delivered. One
// Admission belongs to one process, and one sender's messages affect only that
// sender's budget.
type Admission struct {
	table *bucketTable
}

// NewAdmission builds an admission gate with limits.
func NewAdmission(limits Limits) *Admission {
	return &Admission{table: newBucketTable(limits)}
}

// Admit applies the incoming rules in order: the hop chain, then a repeat of
// the sender's previous body inside the dedup window, then the sender's token
// bucket, then the queue depth. It reports the drop reason when it refuses, and
// only a message that passes is charged a token and remembered as the sender's
// last body.
func (a *Admission) Admit(senderKey, body string, chain []string, ownTokens map[string]bool, queued int, now time.Time) (bool, string) {
	if reason := CheckChain(chain, ownTokens, a.table.limits); reason != "" {
		return false, reason
	}
	a.table.mu.Lock()
	defer a.table.mu.Unlock()
	bucket := a.table.touchLocked(senderKey, now)
	if !bucket.bodyAt.IsZero() && bucket.body == body && now.Sub(bucket.bodyAt) < a.table.limits.DedupWindow {
		return false, ReasonDuplicate
	}
	if bucket.tokens < 1 {
		return false, ReasonRateLimited
	}
	if queued >= a.table.limits.MaxQueuedPeerMessages {
		return false, ReasonQueueFull
	}
	bucket.tokens--
	bucket.body = body
	bucket.bodyAt = now
	return true, ""
}

// Pacer bounds what this process sends to one peer, with the same bucket the
// inbound side uses so a conversation cannot accelerate in either direction.
type Pacer struct {
	table *bucketTable
}

// NewPacer builds an outbound pacer with limits.
func NewPacer(limits Limits) *Pacer {
	return &Pacer{table: newBucketTable(limits)}
}

// Reserve charges one message to target, or reports that its budget is spent.
// The returned refund hands the charge back when the send never happened.
func (p *Pacer) Reserve(target string, now time.Time) (bool, func()) {
	p.table.mu.Lock()
	defer p.table.mu.Unlock()
	bucket := p.table.touchLocked(target, now)
	if bucket.tokens < 1 {
		return false, nil
	}
	bucket.tokens--
	var once sync.Once
	return true, func() {
		once.Do(func() {
			p.table.mu.Lock()
			defer p.table.mu.Unlock()
			if bucket.tokens < p.table.limits.BucketCapacity {
				bucket.tokens++
			}
		})
	}
}
