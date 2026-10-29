package llm

import "testing"

// TestUsageSnapshotRecordKeepsTheLastSnapshotAndDrains pins the recorder a
// durable consumer of OnUsageSnapshot pairs with the request boundary: the
// gauge may fire once per chunk, but only the last snapshot survives and only
// the first Take after it yields a record.
func TestUsageSnapshotRecordKeepsTheLastSnapshotAndDrains(t *testing.T) {
	var r UsageSnapshotRecord
	r.Observe(1000, 10)
	r.Observe(1000, 20)
	r.Observe(1000, 30)
	in, out, ok := r.Take()
	if !ok || in != 1000 || out != 30 {
		t.Fatalf("Take = %d, %d, %v; want the last snapshot 1000, 30, true", in, out, ok)
	}
	if in, out, ok := r.Take(); ok {
		t.Fatalf("second Take = %d, %d, %v; want drained (false) so both boundaries yield one record", in, out, ok)
	}
	r.Observe(1200, 40)
	if in, out, ok := r.Take(); !ok || in != 1200 || out != 40 {
		t.Fatalf("Take after a new snapshot = %d, %d, %v; want 1200, 40, true", in, out, ok)
	}

	var nilRecord *UsageSnapshotRecord
	nilRecord.Observe(1, 1)
	if in, out, ok := nilRecord.Take(); ok || in != 0 || out != 0 {
		t.Fatalf("nil Take = %d, %d, %v; want zero values without panic", in, out, ok)
	}
}
