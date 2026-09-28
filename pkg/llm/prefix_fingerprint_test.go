package llm

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func prefixTestMessages(n int) []Message {
	out := make([]Message, 0, n)
	out = append(out, SystemMessage("fixed system"))
	for i := 0; i < n; i++ {
		out = append(out, UserMessage(Text("turn "+strconv.Itoa(i))))
	}
	return out
}

func resetAndSetDebug(t *testing.T) *[]string {
	t.Helper()
	ResetPrefixTracking()
	events := &[]string{}
	SetDebugLogger(func(topic, message string) {
		*events = append(*events, topic+" "+message)
	})
	t.Cleanup(func() { SetDebugLogger(nil); ResetPrefixTracking() })
	return events
}

func findEvent(t *testing.T, events *[]string, topic string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, e := range *events {
		if strings.HasPrefix(e, topic+" ") {
			var m map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(e, topic+" ")), &m); err == nil {
				found = m
			}
		}
	}
	return found
}

// An append-only request must never be classified as a break: the whole
// previous prompt is theoretically cacheable.
func TestPrefixTrackingAppendOnly(t *testing.T) {
	resetAndSetDebug(t)
	ctx := WithAgentSessionID(context.Background(), "s1")

	o1 := BeginPrefixObservation(ctx, "m", nil, prefixTestMessages(3))
	o1.Finish(&Usage{InputTokens: 900})
	o2 := BeginPrefixObservation(ctx, "m", nil, prefixTestMessages(4))
	o2.Finish(&Usage{InputTokens: 100, CacheReadInputTokens: 900})

	if o2.forkIndex >= 0 {
		t.Fatalf("append-only request reported fork at %d (%s)", o2.forkIndex, o2.forkLabel)
	}
	if got := o2.expectedHit(); got != 900 {
		t.Fatalf("expectedHit = %d, want 900", got)
	}
	stats := PrefixCacheSnapshot()
	if len(stats) != 1 || stats[0].Requests != 2 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats[0].Generations != 1 {
		t.Fatalf("generations = %d, want 1", stats[0].Generations)
	}
}

// Changing a middle message is a fork: the tracker must name the block where
// the chains diverge and count a break, not an anomaly.
func TestPrefixTrackingForkBreak(t *testing.T) {
	events := resetAndSetDebug(t)
	ctx := WithAgentSessionID(context.Background(), "s1")

	msgs := prefixTestMessages(4)
	o1 := BeginPrefixObservation(ctx, "m", nil, msgs)
	o1.Finish(&Usage{InputTokens: 1000})

	msgs[2] = UserMessage(Text("edited turn"))
	o2 := BeginPrefixObservation(ctx, "m", nil, msgs)
	o2.Finish(&Usage{InputTokens: 1000})

	if o2.forkIndex < 0 {
		t.Fatal("fork not detected")
	}
	if !strings.HasPrefix(o2.forkLabel, "messages[2]") {
		t.Fatalf("fork label = %q, want messages[2]:*", o2.forkLabel)
	}
	brk := findEvent(t, events, "prompt_cache_break")
	if brk == nil {
		t.Fatal("no prompt_cache_break event")
	}
	if brk["fork_label"] != o2.forkLabel {
		t.Fatalf("event fork_label = %v", brk["fork_label"])
	}
	stats := PrefixCacheSnapshot()
	if stats[0].Breaks != 1 || stats[0].Anomalies != 0 {
		t.Fatalf("breaks=%d anomalies=%d", stats[0].Breaks, stats[0].Anomalies)
	}
}

// A byte-identical prefix that suddenly serves no cache is a provider-side
// anomaly, not our change: the tracker must attribute it to the provider and
// count the generation it consumed.
func TestPrefixTrackingProviderAnomaly(t *testing.T) {
	events := resetAndSetDebug(t)
	ctx := WithAgentSessionID(context.Background(), "s1")

	msgs := prefixTestMessages(4)
	o1 := BeginPrefixObservation(ctx, "m", nil, msgs)
	o1.Finish(&Usage{InputTokens: 1000})

	o2 := BeginPrefixObservation(ctx, "m", nil, msgs)
	o2.Finish(&Usage{InputTokens: 294_000})

	anom := findEvent(t, events, "prompt_cache_anomaly")
	if anom == nil {
		t.Fatal("no prompt_cache_anomaly event")
	}
	if anom["reason"] != "provider" {
		t.Fatalf("reason = %v", anom["reason"])
	}
	if brk := findEvent(t, events, "prompt_cache_break"); brk != nil {
		t.Fatalf("misclassified as break: %v", brk)
	}
	stats := PrefixCacheSnapshot()
	if stats[0].Anomalies != 1 {
		t.Fatalf("anomalies = %d", stats[0].Anomalies)
	}
	if stats[0].Generations != 2 {
		t.Fatalf("generations = %d, want 2 (start + anomaly)", stats[0].Generations)
	}
}

// A tools-array change is a fork at the tools block; tool order matters to
// providers, so reordering must fork too.
func TestPrefixTrackingToolsFork(t *testing.T) {
	events := resetAndSetDebug(t)
	ctx := WithAgentSessionID(context.Background(), "s1")
	msgs := prefixTestMessages(2)

	shell, err := NewRawTool("shell", "run", map[string]any{"type": "object"}, func(ctx context.Context, args string) (any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewRawTool("read", "read", map[string]any{"type": "object"}, func(ctx context.Context, args string) (any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}

	o1 := BeginPrefixObservation(ctx, "m", []*Tool{shell, reader}, msgs)
	o1.Finish(&Usage{InputTokens: 500})
	o2 := BeginPrefixObservation(ctx, "m", []*Tool{reader, shell}, msgs)
	o2.Finish(&Usage{InputTokens: 500})

	if o2.forkIndex != 1 { // model=0, tools[0]=1
		t.Fatalf("fork index = %d, want 1", o2.forkIndex)
	}
	if o2.forkLabel != "tools[0]:read" {
		t.Fatalf("fork label = %q", o2.forkLabel)
	}
	if findEvent(t, events, "prompt_cache_break") == nil {
		t.Fatal("no prompt_cache_break event")
	}
}

// RecordPrefixGeneration declares a boundary (provider switch); the next
// request's full miss must not be counted twice nor reported as an anomaly.
func TestPrefixRecordGenerationExpectedCold(t *testing.T) {
	events := resetAndSetDebug(t)
	ctx := WithAgentSessionID(context.Background(), "s1")

	msgs := prefixTestMessages(4)
	o1 := BeginPrefixObservation(ctx, "m", nil, msgs)
	o1.Finish(&Usage{InputTokens: 286_000})

	RecordPrefixGeneration("s1", "provider_switch", 286_000, "gpt-5.6-sol->glm-5.3")
	RecordPrefixGeneration("s1", "provider_switch", 286_000, "gpt-5.6-sol->glm-5.3") // dedupe

	o2 := BeginPrefixObservation(ctx, "glm-5.3", nil, msgs)
	o2.Finish(&Usage{InputTokens: 286_000})

	if o2.forkIndex >= 0 {
		t.Fatalf("post-boundary request misread as fork at %d", o2.forkIndex)
	}
	if findEvent(t, events, "prompt_cache_anomaly") != nil {
		t.Fatal("expected cold start reported as anomaly")
	}
	gen := findEvent(t, events, "prefix_generation")
	if gen == nil || gen["reason"] != "provider_switch" {
		t.Fatalf("prefix_generation event = %v", gen)
	}
	stats := PrefixCacheSnapshot()
	if stats[0].Generations != 2 {
		t.Fatalf("generations = %d, want 2 (start + declared boundary)", stats[0].Generations)
	}
}

// The per-request stats event carries the G/N ratio the hit-rate equation is
// governed by.
func TestPrefixStatsEventHasGenerationsPerRequest(t *testing.T) {
	events := resetAndSetDebug(t)
	ctx := WithAgentSessionID(context.Background(), "s1")

	msgs := prefixTestMessages(3)
	o := BeginPrefixObservation(ctx, "m", nil, msgs)
	o.Finish(&Usage{InputTokens: 1000})
	// A steady run of identical requests must stay one generation; the stats
	// event fires on the 25th request of the lineage.
	for i := 0; i < 24; i++ {
		o = BeginPrefixObservation(ctx, "m", nil, msgs)
		o.Finish(&Usage{InputTokens: 0, CacheReadInputTokens: 1000})
	}

	st := findEvent(t, events, "prompt_cache_stats")
	if st == nil {
		t.Fatal("no prompt_cache_stats event")
	}
	if st["requests"] != float64(25) {
		t.Fatalf("requests = %v, want 25", st["requests"])
	}
	if st["generations_per_request"] != 1.0/25.0 {
		t.Fatalf("generations_per_request = %v", st["generations_per_request"])
	}
	if st["cache_hit_rate"] != 24_000.0/25_000.0 {
		t.Fatalf("cache_hit_rate = %v", st["cache_hit_rate"])
	}
}

// Requests with no session identity are grouped as auxiliary one-shots; the
// per-call system prefix they share still accumulates per call, but the
// grouping keeps them out of the main session's ratio.
func TestPrefixNoSessionKeyGroupsAux(t *testing.T) {
	resetAndSetDebug(t)
	o := BeginPrefixObservation(context.Background(), "m", nil, prefixTestMessages(2))
	o.Finish(&Usage{InputTokens: 8_000})
	stats := PrefixCacheSnapshot()
	if len(stats) != 1 || stats[0].Group != "aux" {
		t.Fatalf("stats = %+v", stats)
	}
}

// A subagent shares its parent's prompt-cache key; its distinct conversation
// must be tracked as its own branch, not a fork of the parent's.
func TestPrefixSubagentBranchNotFork(t *testing.T) {
	resetAndSetDebug(t)
	ctx := WithPromptCacheKey(WithAgentSessionID(context.Background(), "sub-1"), "root")

	main := []Message{SystemMessage("sys"), UserMessage(Text("main turn"))}
	o1 := BeginPrefixObservation(ctx, "m", nil, main)
	o1.Finish(&Usage{InputTokens: 400})

	sub := []Message{SystemMessage("subagent sys"), UserMessage(Text("sub task"))}
	o2 := BeginPrefixObservation(ctx, "m", nil, sub)
	o2.Finish(&Usage{InputTokens: 50})

	if o2.forkIndex >= 0 {
		t.Fatalf("subagent conversation misread as fork at %d (%s)", o2.forkIndex, o2.forkLabel)
	}
	if !o2.first {
		t.Fatal("subagent conversation should be a new lineage")
	}
	stats := PrefixCacheSnapshot()
	if stats[0].Generations != 2 {
		t.Fatalf("generations = %d, want 2 (main + subagent)", stats[0].Generations)
	}
}

// Withdrawing an unanswered submission and resending a corrected one is the one
// interactive flow that deliberately drops a message the provider has already
// been shown. The cost of that has to stay bounded to the message itself:
// the resend must fork at the FINAL message block, so everything the
// conversation had built up before it still serves from the provider's cache,
// and no prefix generation may be declared for it — declaring one drops every
// tracked lineage and sets expectedCold, inflating G in hit = 1 - G/N and
// hiding real regressions behind an "expected" full miss.
func TestPrefixWithdrawnResendForksOnlyAtTheLastBlock(t *testing.T) {
	events := resetAndSetDebug(t)
	ctx := WithAgentSessionID(context.Background(), "s-withdraw")

	base := prefixTestMessages(4)
	o1 := BeginPrefixObservation(ctx, "m", nil, base)
	o1.Finish(&Usage{InputTokens: 200, CacheReadInputTokens: 800})

	// The submission that is about to be withdrawn extends the prefix.
	withdrawn := append(append([]Message(nil), base...), UserMessage(Text("the wrong question")))
	o2 := BeginPrefixObservation(ctx, "m", nil, withdrawn)
	if o2.forkIndex >= 0 {
		t.Fatalf("the withdrawn request appended to the prefix and must not fork: index=%d label=%q", o2.forkIndex, o2.forkLabel)
	}
	o2.Finish(&Usage{InputTokens: 20, CacheReadInputTokens: 1000})

	genBefore := PrefixCacheSnapshot()[0].Generations
	breaksBefore := PrefixCacheSnapshot()[0].Breaks

	// Withdrawal removes exactly that row, so the resend carries the base
	// conversation plus the corrected message in its place.
	resend := append(append([]Message(nil), base...), UserMessage(Text("the corrected question")))
	o3 := BeginPrefixObservation(ctx, "m", nil, resend)
	o3.Finish(&Usage{InputTokens: 20, CacheReadInputTokens: 1000})

	if o3.forkIndex < 0 {
		t.Fatal("replacing the last message is a fork; the tracker must see it")
	}
	// Everything before the withdrawn message must still match: the fork index
	// is the last block of the chain, not an earlier one.
	if want := len(o3.chain) - 1; o3.forkIndex != want {
		t.Fatalf("fork at index %d (%q), want the final block %d: the withdrawal perturbed history it had no business touching",
			o3.forkIndex, o3.forkLabel, want)
	}
	if want := "messages[" + strconv.Itoa(len(resend)-1) + "]"; !strings.HasPrefix(o3.forkLabel, want) {
		t.Fatalf("fork label = %q, want %s:*", o3.forkLabel, want)
	}

	// The fork itself legitimately costs one generation and one break: the
	// suffix after the withdrawn message really does have to be re-created, and
	// the emitted break carries a byte-weighted estimate of that true cost. What
	// must NOT happen is a *declared* generation — RecordPrefixGeneration drops
	// every tracked lineage under the key and sets expectedCold, which would
	// both overstate G and excuse the next full miss as expected.
	stats := PrefixCacheSnapshot()[0]
	if stats.Generations != genBefore+1 || stats.Breaks != breaksBefore+1 {
		t.Fatalf("resend accounting = %d generations / %d breaks, want exactly one more of each",
			stats.Generations-genBefore, stats.Breaks-breaksBefore)
	}
	if gen := findEvent(t, events, "prefix_generation"); gen != nil {
		t.Fatalf("prefix_generation declared for a withdrawal: %v", gen)
	}
	brk := findEvent(t, events, "prompt_cache_break")
	if brk == nil {
		t.Fatal("the resend's fork must be reported as a break so its cost is attributed")
	}

	// Nothing was blown away: the resend established a live lineage, so the next
	// turn built on it is append-only rather than another cold start.
	next := append(append([]Message(nil), resend...), AssistantMessage([]ContentPart{Text("an answer")}))
	o4 := BeginPrefixObservation(ctx, "m", nil, next)
	if o4.forkIndex >= 0 {
		t.Fatalf("the turn after a withdrawal forked at %d (%q); the withdrawal orphaned its own lineage",
			o4.forkIndex, o4.forkLabel)
	}
	o4.Finish(&Usage{InputTokens: 10, CacheReadInputTokens: 1100})
}
