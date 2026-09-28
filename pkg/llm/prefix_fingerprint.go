// Request-level prompt-prefix fingerprinting and cache-miss attribution.
//
// Providers with implicit prefix caching (Zhipu GLM, DeepSeek, Kimi, ...) serve
// a request from cache exactly when its rendered bytes extend a prefix they
// have already seen. This tracker answers, per request, the one question that
// decides whether a bad cache-hit number is our fault or the provider's:
// did this request's prompt extend the previous one, and if not, where did it
// diverge?
//
// It works entirely on hashes. The chain for one request is
//
//	[model, tools[0], tools[1], ..., messages[0], messages[1], ...]
//
// where every entry carries a cumulative hash and cumulative byte count of the
// rendered bytes through that block, mirroring the tools → system → messages
// serialization order OpenAI-compatible providers cache on. Two chains that
// agree on the first k blocks and differ at block k mean the prompt forked at
// k: everything before k was cacheable, everything after is a fresh miss, and
// the fork's label names the field responsible.
//
// With the provider's usage in hand, the tracker classifies each request:
//
//   - fork               → prompt_cache_break      (we changed the prefix)
//   - append-only, but the provider served far less cache than the previous
//     request's total prompt → prompt_cache_anomaly (provider-side miss)
//   - an externally declared boundary (provider switch, compaction, ...) →
//     prefix_generation, recorded by the layer that knows the reason
//
// and keeps the per-session G/N ratio (prefix generations per request) that
// the hit-rate equation  hit ≈ 1 − G/N  is governed by. Events are emitted as
// single-line JSON through LogDebug; nothing is retained but hashes and
// counters.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"
)

const (
	// prefixBranchesPerKey bounds how many distinct conversations may share
	// one cache key. A main session and its subagents deliberately share the
	// key (WithPromptCacheKey), so the tracker keeps one branch per
	// conversation and matches each request against the branch it extends.
	prefixBranchesPerKey = 8
	// prefixTrackedKeys bounds the registry; the least recently used key is
	// evicted whole.
	prefixTrackedKeys = 256
	// prefixMinSharedBlocks is the common-prefix length two chains must share
	// before they are considered related. Sharing just the model block (1)
	// already means something: a tools-table change or reorder forks exactly
	// there, and providers bill a full miss for it. Zero shared blocks means
	// not even the model matched — an unrelated conversation.
	prefixMinSharedBlocks = 1
	// prefixAnomalyDenominator: an append-only request whose cached tokens
	// fall below expectedHit/2 is a provider-side anomaly, not our change.
	prefixAnomalyDenominator = 2
)

type prefixBlock struct {
	Label    string
	Sum      uint64
	CumBytes int64
}

type prefixChain []prefixBlock

// prefixBranch is one conversation's lineage under a cache key.
type prefixBranch struct {
	chain prefixChain
	// lastPrompt is the previous request's total prompt tokens as the provider
	// reported them (cached + uncached + cache-creation); lastBytes is the
	// same request's total chain bytes. Their ratio calibrates byte→token
	// estimates for fork points without running a tokenizer.
	lastPrompt int
	lastBytes  int64
	lastSeen   time.Time
}

type prefixKeyState struct {
	key   string
	group string

	// branches holds one entry per tracked conversation. Pointers, not
	// values: appending a new conversation can reallocate the slice, and an
	// in-flight observation must keep writing to its own branch, not to a
	// stale copy left behind by the move.
	branches    []*prefixBranch
	requests    int
	generations int
	sumPrompt   int64
	sumCached   int64
	breaks      int64
	anomalies   int64
	// expectedCold marks that the next request is *known* to start a new
	// generation (a provider switch, a compaction), so its lineage start must
	// not be counted twice — RecordPrefixGeneration already counted it.
	expectedCold bool
	// lastGenKey dedupes RecordPrefixGeneration repeats across turns.
	lastGenKey string
	lastSeen   time.Time
}

var prefixRegistry = struct {
	sync.Mutex
	keys map[string]*prefixKeyState
}{keys: make(map[string]*prefixKeyState)}

// PrefixSessionStats is a read-only snapshot of one cache key's accounting.
type PrefixSessionStats struct {
	Key         string `json:"key"`
	Group       string `json:"group"`
	Requests    int    `json:"requests"`
	Generations int    `json:"generations"`
	Breaks      int64  `json:"breaks"`
	Anomalies   int64  `json:"anomalies"`
	SumPrompt   int64  `json:"sum_prompt_tokens"`
	SumCached   int64  `json:"sum_cached_tokens"`
}

// Wire-shaped projections used only for hashing. They carry exactly the
// fields the OpenAI-compatible request path sends, so a chain fork names the
// field that actually changed on the wire. Large opaque payloads (base64
// images, encrypted compaction items) are folded to a digest to keep block
// rendering cheap; the digest still changes when the payload does.
type prefixWireTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type prefixWireToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function string `json:"function,omitempty"`
	Args     string `json:"args,omitempty"`
}

type prefixWirePart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	ImageRef string `json:"image_ref,omitempty"`
}

type prefixWireMessage struct {
	Role       string               `json:"role"`
	Name       string               `json:"name,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
	ToolCalls  []prefixWireToolCall `json:"tool_calls,omitempty"`
	Parts      []prefixWirePart     `json:"parts,omitempty"`
	OpaqueRef  string               `json:"opaque_ref,omitempty"`
}

func prefixDigest(b []byte) string {
	h := fnv.New64a()
	_, _ = h.Write(b)
	return fmt.Sprintf("%016x", h.Sum64())
}

func prefixToolBlocks(tools []*Tool, add func(label string, data []byte)) {
	for i, t := range tools {
		if t == nil {
			continue
		}
		wire := prefixWireTool{Name: t.Name(), Description: t.Description(), Parameters: t.InputSchema()}
		data, err := json.Marshal(wire)
		if err != nil {
			data = []byte(wire.Name)
		}
		add(fmt.Sprintf("tools[%d]:%s", i, wire.Name), data)
	}
}

func prefixMessageBlock(m Message) []byte {
	wire := prefixWireMessage{
		Role:       m.Role,
		Name:       m.Name,
		ToolCallID: m.ToolCallID,
	}
	if len(m.ToolCalls) > 0 {
		wire.ToolCalls = make([]prefixWireToolCall, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			wire.ToolCalls = append(wire.ToolCalls, prefixWireToolCall{
				ID:       tc.ID,
				Type:     tc.Type,
				Function: tc.Function.Name,
				Args:     tc.Function.Arguments,
			})
		}
	}
	if len(m.Parts) > 0 {
		wire.Parts = make([]prefixWirePart, 0, len(m.Parts))
		for _, p := range m.Parts {
			part := prefixWirePart{Type: string(p.Type), Text: p.Text, ImageURL: p.ImageURL}
			if p.ImageBase64 != "" {
				part.ImageRef = prefixDigest([]byte(p.MIMEType + ":" + p.ImageBase64))
			}
			wire.Parts = append(wire.Parts, part)
		}
	}
	if m.Compaction != nil {
		wire.OpaqueRef = prefixDigest([]byte(m.Compaction.EncryptedContent))
	}
	data, err := json.Marshal(wire)
	if err != nil {
		return []byte(m.Role)
	}
	return data
}

// buildPrefixChain hashes the request's rendered shape into an append-only
// block chain.
func buildPrefixChain(model string, tools []*Tool, messages []Message) prefixChain {
	h := fnv.New64a()
	var chain prefixChain
	var cum int64
	add := func(label string, data []byte) {
		_, _ = h.Write(data)
		cum += int64(len(data))
		chain = append(chain, prefixBlock{Label: label, Sum: h.Sum64(), CumBytes: cum})
	}
	add("model", []byte(strings.TrimSpace(model)))
	prefixToolBlocks(tools, add)
	for i, m := range messages {
		add(fmt.Sprintf("messages[%d]:%s", i, m.Role), prefixMessageBlock(m))
	}
	return chain
}

func prefixCommonLen(a, b prefixChain) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i].Sum != b[i].Sum {
			return i
		}
	}
	return n
}

// PrefixObservation is the in-flight record of one request. Begin creates it
// before the call; Finish classifies the outcome once usage is known.
type PrefixObservation struct {
	key   string
	group string
	chain prefixChain

	first     bool
	forkIndex int
	forkLabel string
	// forkBytes is the matched branch's cumulative byte count at the fork
	// point; with its lastPrompt/lastBytes it estimates the cacheable prefix.
	forkBytes  int64
	prevPrompt int
	prevBytes  int64

	branch *prefixBranch
	state  *prefixKeyState
}

// BeginPrefixObservation records the outgoing prompt shape against the cache
// key's previous requests. It must be called with the exact message and tool
// slices the provider will render (after any send-time trimming), so the chain
// reflects the bytes on the wire.
func BeginPrefixObservation(ctx context.Context, model string, tools []*Tool, messages []Message) *PrefixObservation {
	key := PromptCacheKeyFromContext(ctx)
	chain := buildPrefixChain(model, tools, messages)

	prefixRegistry.Lock()
	defer prefixRegistry.Unlock()
	st := prefixStateFor(key)
	st.lastSeen = time.Now()
	st.requests++

	obs := &PrefixObservation{key: st.key, group: st.group, chain: chain, forkIndex: -1}

	best := -1
	bestLen := 0
	for i := range st.branches {
		if l := prefixCommonLen(st.branches[i].chain, chain); l > bestLen {
			best = i
			bestLen = l
		}
	}
	obs.state = st
	// startLineage registers this request as a conversation the key has not
	// seen. When a RecordPrefixGeneration already declared the boundary
	// (provider switch, compaction), it counted the generation; counting it
	// again here would double G.
	startLineage := func() {
		if st.expectedCold {
			st.expectedCold = false
		} else {
			st.generations++
		}
		branch := &prefixBranch{chain: chain, lastSeen: st.lastSeen}
		st.branches = append(st.branches, branch)
		prefixTrimBranches(st)
		obs.first = true
		obs.branch = branch
	}
	if best < 0 || bestLen < prefixMinSharedBlocks {
		startLineage()
		return obs
	}
	matched := st.branches[best]
	old := matched.chain
	obs.prevPrompt = matched.lastPrompt
	obs.prevBytes = matched.lastBytes
	matched.lastSeen = st.lastSeen

	if bestLen >= len(old) && len(chain) >= len(old) {
		// Append-only: the previous request's whole prompt is cacheable, and
		// this request extends the same lineage.
		matched.chain = chain
		obs.branch = matched
		obs.forkBytes = old[len(old)-1].CumBytes
		return obs
	}
	// The chains diverge at block bestLen. Label from whichever chain still
	// has that block so a shrunken prompt still names what disappeared.
	divergeLabel := old[bestLen].Label
	if bestLen < len(chain) {
		divergeLabel = chain[bestLen].Label
	}
	if strings.HasPrefix(divergeLabel, "messages[0]") {
		// A different first message is a different conversation — a subagent
		// on the shared cache key, or a fresh agent — not a mid-conversation
		// edit. It starts its own lineage; the matched lineage stays tracked
		// so the parent conversation still matches its own history.
		startLineage()
		return obs
	}
	// A fork at any other block is a prefix change inside a related
	// conversation: a tools-table change, an edited or dropped middle
	// message. Providers bill a full miss from that point, so this request
	// starts a new lineage while the old one stays tracked for a caller that
	// continues on the previous prefix.
	obs.forkLabel = divergeLabel
	obs.forkIndex = bestLen
	if bestLen > 0 {
		obs.forkBytes = old[bestLen-1].CumBytes
	}
	branch := &prefixBranch{chain: chain, lastSeen: st.lastSeen}
	st.branches = append(st.branches, branch)
	prefixTrimBranches(st)
	obs.branch = branch
	st.generations++
	st.breaks++
	return obs
}

// Finish classifies the completed request against its provider usage. A nil
// usage (call failed before any usage arrived) only refreshes recency.
func (o *PrefixObservation) Finish(usage *Usage) {
	if o == nil {
		return
	}
	prefixRegistry.Lock()
	defer prefixRegistry.Unlock()
	st := o.state
	if st == nil {
		return
	}
	st.lastSeen = time.Now()
	if o.branch != nil {
		o.branch.lastSeen = st.lastSeen
	}
	if usage == nil {
		return
	}
	promptTotal := int64(usage.InputTokens) + int64(usage.CacheReadInputTokens) + int64(usage.CacheCreationInputTokens)
	cached := int64(usage.CacheReadInputTokens)
	st.sumPrompt += promptTotal
	st.sumCached += cached

	expected := o.expectedHit()
	if o.forkIndex >= 0 {
		prefixEmit("prompt_cache_break", map[string]any{
			"key": st.key, "group": st.group,
			"fork_index": o.forkIndex, "fork_label": o.forkLabel,
			"expected_prefix_tokens": expected,
			"prompt_tokens":          promptTotal, "cached_tokens": cached,
			"reason": "prefix_changed",
		})
	} else if !o.first && st.expectedCold {
		st.expectedCold = false
	} else if !o.first && expected > 0 && cached*prefixAnomalyDenominator < expected {
		st.generations++
		st.anomalies++
		prefixEmit("prompt_cache_anomaly", map[string]any{
			"key": st.key, "group": st.group,
			"expected_cached_tokens": expected,
			"cached_tokens":          cached, "prompt_tokens": promptTotal,
			"reason": "provider",
		})
	}
	if o.branch != nil {
		o.branch.lastPrompt = int(promptTotal)
		if n := len(o.chain); n > 0 {
			o.branch.lastBytes = o.chain[n-1].CumBytes
		}
	}
	if o.first || o.forkIndex >= 0 || st.requests%25 == 0 {
		prefixEmit("prompt_cache_stats", prefixStatsEvent(st))
	}
}

// expectedHit estimates how many tokens this request should have been served
// from cache. Append-only requests should hit the previous request's entire
// prompt; a fork should hit the bytes before the fork point, scaled by the
// previous request's byte→token ratio.
func (o *PrefixObservation) expectedHit() int64 {
	if o.first {
		return 0
	}
	if o.forkIndex < 0 {
		return int64(o.prevPrompt)
	}
	if o.prevBytes <= 0 || o.forkBytes <= 0 {
		return 0
	}
	return int64(o.prevPrompt) * o.forkBytes / o.prevBytes
}

// RecordPrefixGeneration declares a generation boundary the request stream
// cannot see for itself: the model or provider under a session changed, a
// compaction rewrote history, anything that makes the next request's full
// miss expected rather than anomalous. Repeats of the same reason and detail
// within a key are ignored, so a per-turn check can call it every turn.
func RecordPrefixGeneration(key, reason string, tokens int, detail string) {
	key = strings.TrimSpace(key)
	detail = strings.TrimSpace(detail)
	prefixRegistry.Lock()
	defer prefixRegistry.Unlock()
	st := prefixStateFor(key)
	genKey := reason + "\x00" + detail
	if st.lastGenKey == genKey {
		return
	}
	st.lastGenKey = genKey
	st.lastSeen = time.Now()
	st.generations++
	st.expectedCold = true
	// The declared boundary orphans every tracked lineage under this key;
	// dropping them keeps the next request from being misread as a fork of a
	// conversation that no longer exists.
	st.branches = nil
	prefixEmit("prefix_generation", map[string]any{
		"key": st.key, "group": st.group,
		"reason": reason, "tokens": tokens, "detail": detail,
	})
}

// PrefixCacheSnapshot copies the registry's per-key accounting, oldest key
// last. It exists for tests and status surfaces; nothing else should read the
// registry directly.
func PrefixCacheSnapshot() []PrefixSessionStats {
	prefixRegistry.Lock()
	defer prefixRegistry.Unlock()
	out := make([]PrefixSessionStats, 0, len(prefixRegistry.keys))
	for _, st := range prefixRegistry.keys {
		out = append(out, PrefixSessionStats{
			Key: st.key, Group: st.group,
			Requests: st.requests, Generations: st.generations,
			Breaks: st.breaks, Anomalies: st.anomalies,
			SumPrompt: st.sumPrompt, SumCached: st.sumCached,
		})
	}
	return out
}

// ResetPrefixTracking clears all tracker state. Tests use it for isolation.
func ResetPrefixTracking() {
	prefixRegistry.Lock()
	defer prefixRegistry.Unlock()
	prefixRegistry.keys = make(map[string]*prefixKeyState)
}

func prefixStateFor(key string) *prefixKeyState {
	key = strings.TrimSpace(key)
	st, ok := prefixRegistry.keys[key]
	if !ok {
		st = &prefixKeyState{key: key, group: prefixGroupFor(key)}
		prefixRegistry.keys[key] = st
		prefixTrimKeys()
	}
	return st
}

func prefixGroupFor(key string) string {
	if key == "" {
		// Requests with no session identity are the auxiliary one-shots:
		// memory extraction, consolidation, guardian reviews.
		return "aux"
	}
	return "main"
}

func prefixTrimKeys() {
	if len(prefixRegistry.keys) <= prefixTrackedKeys {
		return
	}
	var oldest string
	var oldestAt time.Time
	for k, st := range prefixRegistry.keys {
		if oldest == "" || st.lastSeen.Before(oldestAt) {
			oldest, oldestAt = k, st.lastSeen
		}
	}
	if oldest != "" {
		delete(prefixRegistry.keys, oldest)
	}
}

func prefixTrimBranches(st *prefixKeyState) {
	if len(st.branches) <= prefixBranchesPerKey {
		return
	}
	oldest := 0
	for i := range st.branches {
		if st.branches[i].lastSeen.Before(st.branches[oldest].lastSeen) {
			oldest = i
		}
	}
	st.branches = append(st.branches[:oldest], st.branches[oldest+1:]...)
}

func prefixStatsEvent(st *prefixKeyState) map[string]any {
	gn := 0.0
	if st.requests > 0 {
		gn = float64(st.generations) / float64(st.requests)
	}
	hit := 0.0
	if st.sumPrompt > 0 {
		hit = float64(st.sumCached) / float64(st.sumPrompt)
	}
	return map[string]any{
		"key": st.key, "group": st.group,
		"requests": st.requests, "generations": st.generations,
		"generations_per_request": gn,
		"sum_prompt_tokens":       st.sumPrompt, "sum_cached_tokens": st.sumCached,
		"cache_hit_rate": hit,
		"breaks":         st.breaks, "anomalies": st.anomalies,
	}
}

func prefixEmit(topic string, event map[string]any) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	LogDebug(topic, string(data))
}
