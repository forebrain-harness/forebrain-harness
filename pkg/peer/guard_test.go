package peer

import (
	"fmt"
	"testing"
	"time"
)

// admitTestBase is a fixed instant: the guard tests never sleep.
var admitTestBase = time.Date(2026, 10, 30, 6, 47, 30, 0, time.UTC)

func TestAdmissionTokenBucket(t *testing.T) {
	admission := NewAdmission(DefaultLimits)
	now := admitTestBase

	for i := 0; i < int(DefaultLimits.BucketCapacity); i++ {
		ok, reason := admission.Admit("from:uds:/tmp/cc-socks/1.sock", bodyFor(i), nil, nil, 0, now)
		if !ok {
			t.Fatalf("message %d was refused: %q", i, reason)
		}
	}
	ok, reason := admission.Admit("from:uds:/tmp/cc-socks/1.sock", bodyFor(999), nil, nil, 0, now)
	if ok || reason != ReasonRateLimited {
		t.Fatalf("the 31st message = %v, %q; want rate-limited", ok, reason)
	}
	// The refusal did not charge a token: two seconds of refill buys exactly
	// one message, and the next one is refused again.
	later := now.Add(2 * time.Second)
	if ok, reason := admission.Admit("from:uds:/tmp/cc-socks/1.sock", bodyFor(1000), nil, nil, 0, later); !ok {
		t.Fatalf("the refilled message was refused: %q", reason)
	}
	if ok, reason := admission.Admit("from:uds:/tmp/cc-socks/1.sock", bodyFor(1001), nil, nil, 0, later); ok || reason != ReasonRateLimited {
		t.Fatalf("a second message on one refilled token = %v, %q", ok, reason)
	}
}

func TestAdmissionDeduplicates(t *testing.T) {
	admission := NewAdmission(DefaultLimits)
	now := admitTestBase
	const sender = "from:uds:/tmp/cc-socks/1.sock"

	if ok, reason := admission.Admit(sender, "same body", nil, nil, 0, now); !ok {
		t.Fatalf("the first message was refused: %q", reason)
	}
	if ok, reason := admission.Admit(sender, "same body", nil, nil, 0, now.Add(29*time.Second)); ok || reason != ReasonDuplicate {
		t.Fatalf("a repeat inside the window = %v, %q; want duplicate", ok, reason)
	}
	if ok, reason := admission.Admit(sender, "same body", nil, nil, 0, now.Add(31*time.Second)); !ok {
		t.Fatalf("a repeat past the window was refused: %q", reason)
	}
	if ok, reason := admission.Admit(sender, "other body", nil, nil, 0, now.Add(31*time.Second)); !ok {
		t.Fatalf("a different body was refused: %q", reason)
	}
}

func TestAdmissionSendersAreIndependent(t *testing.T) {
	admission := NewAdmission(DefaultLimits)
	now := admitTestBase
	for i := 0; i < int(DefaultLimits.BucketCapacity); i++ {
		if ok, reason := admission.Admit("from:a", bodyFor(i), nil, nil, 0, now); !ok {
			t.Fatalf("sender a message %d: %q", i, reason)
		}
	}
	if ok, _ := admission.Admit("from:a", "one too many", nil, nil, 0, now); ok {
		t.Fatal("sender a was not limited")
	}
	if ok, reason := admission.Admit("from:b", "hello", nil, nil, 0, now); !ok {
		t.Fatalf("sender b was limited by sender a: %q", reason)
	}
}

func TestAdmissionQueueFull(t *testing.T) {
	admission := NewAdmission(DefaultLimits)
	ok, reason := admission.Admit("from:a", "hello", nil, nil, DefaultLimits.MaxQueuedPeerMessages, admitTestBase)
	if ok || reason != ReasonQueueFull {
		t.Fatalf("a full queue = %v, %q; want queue-full", ok, reason)
	}
	if ok, reason := admission.Admit("from:a", "hello", nil, nil, DefaultLimits.MaxQueuedPeerMessages-1, admitTestBase); !ok {
		t.Fatalf("one below the queue cap was refused: %q", reason)
	}
}

func TestAdmissionChainIsCheckedBeforeTheBucket(t *testing.T) {
	admission := NewAdmission(DefaultLimits)
	chain := make([]string, DefaultLimits.MaxChainLength+1)
	for i := range chain {
		chain[i] = "aaaaaaaaaaaaaaaaaaaaaaaa"
	}
	if ok, reason := admission.Admit("from:a", "hello", chain, nil, 0, admitTestBase); ok || reason != ReasonHopRunaway {
		t.Fatalf("a runaway chain = %v, %q", ok, reason)
	}
}

func TestAdmissionForgetsTheLeastRecentlyUsedSender(t *testing.T) {
	admission := NewAdmission(DefaultLimits)
	now := admitTestBase
	if ok, reason := admission.Admit("from:sender-0", "first", nil, nil, 0, now); !ok {
		t.Fatalf("sender 0: %q", reason)
	}
	for i := 1; i < DefaultLimits.MaxTrackedSenders; i++ {
		if ok, reason := admission.Admit(fmt.Sprintf("from:sender-%d", i), "hello", nil, nil, 0, now); !ok {
			t.Fatalf("sender %d: %q", i, reason)
		}
	}
	// One more sender pushes the table past its cap, evicting sender 0 — the
	// least recently used. Its dedup memory went with it.
	if ok, reason := admission.Admit("from:sender-last", "hello", nil, nil, 0, now); !ok {
		t.Fatalf("sender last: %q", reason)
	}
	// Being refused here as a duplicate is how a retained bucket would show
	// up: the evicted sender must start over with a full bucket and no memory.
	if ok, reason := admission.Admit("from:sender-0", "first", nil, nil, 0, now); !ok {
		t.Fatalf("sender 0 was not evicted: %q", reason)
	}
}

func TestPacerReserve(t *testing.T) {
	pacer := NewPacer(DefaultLimits)
	now := admitTestBase
	const target = "/tmp/cc-socks/50000.sock"

	for i := 0; i < int(DefaultLimits.BucketCapacity); i++ {
		if ok, _ := pacer.Reserve(target, now); !ok {
			t.Fatalf("reservation %d was refused", i)
		}
	}
	if ok, _ := pacer.Reserve(target, now); ok {
		t.Fatal("the pacer let a 31st message through")
	}
	// A refund hands the charge back, but only once.
	refunded := NewPacer(DefaultLimits)
	var refund func()
	for i := 0; i < int(DefaultLimits.BucketCapacity); i++ {
		_, refund = refunded.Reserve(target, now)
	}
	if refund == nil {
		t.Fatal("the last reservation returned no refund")
	}
	refund()
	refund()
	if ok, _ := refunded.Reserve(target, now); !ok {
		t.Fatal("a refunded token was not usable")
	}
	if ok, _ := refunded.Reserve(target, now); ok {
		t.Fatal("a doubled refund bought two messages")
	}
	// Another target has its own budget.
	if ok, _ := refunded.Reserve("/tmp/cc-socks/50001.sock", now); !ok {
		t.Fatal("a second target inherited the first target's spending")
	}
	// Two seconds of refill buys one more.
	if ok, _ := refunded.Reserve(target, now.Add(2*time.Second)); !ok {
		t.Fatal("the refilled reservation was refused")
	}
}

// bodyFor is a distinct body per index, so the dedup rule never fires in the
// bucket tests.
func bodyFor(i int) string {
	return fmt.Sprintf("body %d", i)
}
