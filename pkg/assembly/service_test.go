package assembly

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

type checkpointTestLLM struct{ summary string }

func (f checkpointTestLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(f.summary)})
	return &llm.Result{Message: &msg}, nil
}

// retryingCompactTestLLM rejects any request above tokenBudget, the way a
// provider rejects an oversized prompt.
type retryingCompactTestLLM struct {
	calls       int
	tokenBudget int
	sawTokens   []int
}

func (f *retryingCompactTestLLM) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	f.calls++
	size := llm.EstimateMessages(messages)
	f.sawTokens = append(f.sawTokens, size)
	if size > f.tokenBudget {
		return nil, errors.New("context window exceeded")
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("summary of trimmed history")})
	return &llm.Result{Message: &msg}, nil
}

type remoteFallbackTestLLM struct {
	compactErr     error
	executeSummary string
	executeErr     error
	compactCalls   int
	executeCalls   int
}

func (f *remoteFallbackTestLLM) Compact(context.Context, []llm.Message, []*llm.Tool) (*llm.CompactResult, error) {
	f.compactCalls++
	return nil, f.compactErr
}

func (f *remoteFallbackTestLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	f.executeCalls++
	if f.executeErr != nil {
		return nil, f.executeErr
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(f.executeSummary)})
	return &llm.Result{Message: &msg}, nil
}

func newCompactTestStoreWithDB(t *testing.T) (*state.SessionStore, *sql.DB) {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return state.NewSessionStore(db, "main"), db
}

func newCompactTestStore(t *testing.T) *state.SessionStore {
	t.Helper()
	sess, _ := newCompactTestStoreWithDB(t)
	return sess
}

// TestTryCompactOnMessagesReportsTheTriggeringOccupancy pins one basis for the
// mid-turn card: Before is the request occupancy the checkpoint fired on — the
// same number the compaction threshold and the live gauge read — and After is
// what the next request projects to. A local estimate taken at compaction time
// is a second measure of the same history and reads lower by everything the
// estimator cannot see (the tool schemas, the provider's framing, a reasoning
// block carried back), which is how a card came to claim 313.2k for a request
// that was really 458k.
func TestTryCompactOnMessagesReportsTheTriggeringOccupancy(t *testing.T) {
	ctx := context.Background()
	sink, svc := seedCompactSession(t, "s1")
	seedCompactHistory(t, svc.Sessions)
	history, err := svc.Sessions.ListTranscriptMessages(ctx, "s1", 100)
	if err != nil {
		t.Fatal(err)
	}
	const occupancy = 916_456
	// A learned overhead is the whole difference between a projection and a
	// bare estimate: state one for a model nothing else uses, and the card's
	// after must carry it.
	const overhead = 39_000
	svc.PrimaryModel = func(context.Context) (string, string) { return "cardbasis", "card-model" }
	llm.RecordSuccessFor("cardbasis", "card-model", llm.DefaultEstimator().Name(), overhead+1_000, 1_000)
	if _, ok, err := svc.TryCompactOnMessages(ctx, history, nil, "s1", false, nil, occupancy); err != nil || !ok {
		t.Fatalf("compact: ok=%v err=%v", ok, err)
	}
	events := sink.all()
	var started event.ContextCompactingPayload
	if events[0].Type != event.RunEventContextCompacting || json.Unmarshal(events[0].Payload, &started) != nil {
		t.Fatalf("first event = %s", events[0].Type)
	}
	if started.TokensBefore != occupancy {
		t.Fatalf("running card before = %d, want the triggering occupancy %d", started.TokensBefore, occupancy)
	}
	last := events[len(events)-1]
	var done event.ContextCompactedPayload
	if last.Type != event.RunEventContextCompacted || json.Unmarshal(last.Payload, &done) != nil {
		t.Fatalf("last event = %s", last.Type)
	}
	if done.TokensBefore != occupancy {
		t.Fatalf("compacted card before = %d, want %d (the number the gauge showed)", done.TokensBefore, occupancy)
	}
	if done.TokensAfter < overhead {
		t.Fatalf("compacted card after = %d, want the projection of the replacement (it must carry the %d learned overhead a bare estimate cannot see)", done.TokensAfter, overhead)
	}
	if done.TokensAfter >= occupancy {
		t.Fatalf("compacted card after = %d, want it below the %d it replaced", done.TokensAfter, occupancy)
	}
}

// seedCompactHistory grows a seeded session into a history large enough that a
// summary really is the smaller side, so "after" can be told apart from the
// history it replaced.
func seedCompactHistory(t *testing.T, sess *state.SessionStore) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		_, _ = sess.Append(ctx, "s1", "user", strings.Repeat("requirement detail. ", 40))
		_, _ = sess.Append(ctx, "s1", "assistant", "answer")
	}
}

// TestManualCompactionCardReportsTheGauge pins the manual card's before to the
// occupancy the gauge showed: the two sit next to each other, so a second,
// local measure of the same history would state two different sizes for one
// conversation.
func TestManualCompactionCardReportsTheGauge(t *testing.T) {
	ctx := context.Background()
	sink := &recordingSink{}
	sess := newCompactTestStore(t)
	seedCompactHistory(t, sess)
	const overhead = 39_000
	svc := Service{Sessions: sess, Events: sink, CompactLLM: func(context.Context) llm.LLM { return checkpointTestLLM{summary: "handoff"} },
		PrimaryModel: func(context.Context) (string, string) { return "cardgauge", "card-model" }}
	llm.RecordSuccessFor("cardgauge", "card-model", llm.DefaultEstimator().Name(), overhead+1_000, 1_000)
	// Stated independently of the service: the recent turns counted with the
	// active model's estimator, which is what the gauge counts.
	turns, err := sess.ListRecentMessages(ctx, "s1", 5000)
	if err != nil {
		t.Fatal(err)
	}
	gauge := state.TokenCountWithEstimationFor(turns, llm.DefaultEstimator())
	if gauge <= 0 {
		t.Fatalf("gauge = %d, want a positive occupancy for a seeded session", gauge)
	}
	res, err := svc.ManualCompactSession(ctx, "s1", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if res.TokensBefore != gauge {
		t.Fatalf("card before = %d, want the gauge's %d", res.TokensBefore, gauge)
	}
	if res.TokensAfter < overhead {
		t.Fatalf("card after = %d, want the projection of the replacement (it must carry the %d learned overhead a bare estimate cannot see)", res.TokensAfter, overhead)
	}
	var done event.ContextCompactedPayload
	events := sink.all()
	if end := events[len(events)-1]; json.Unmarshal(end.Payload, &done) != nil || done.TokensBefore != gauge {
		t.Fatalf("published before = %d, want the gauge's %d", done.TokensBefore, gauge)
	}
}

func TestManualCompactPersistsReplacementCheckpoint(t *testing.T) {
	ctx, sess := context.Background(), newCompactTestStore(t)
	_, _ = sess.Append(ctx, "s1", "user", "first request")
	_, _ = sess.Append(ctx, "s1", "assistant", "old answer")
	_, _ = sess.Append(ctx, "s1", "user", "latest request")
	svc := Service{Sessions: sess, CompactLLM: func(context.Context) llm.LLM { return checkpointTestLLM{summary: "handoff state"} }}
	res, err := svc.ManualCompactSession(ctx, "s1", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if res.Strategy != "local" || res.WindowNumber != 1 {
		t.Fatalf("result=%+v", res)
	}
	msgs, err := sess.ListTranscriptMessages(ctx, "s1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("replacement=%+v", msgs)
	}
	if msgs[0].TextContent() != "first request" || msgs[1].TextContent() != "latest request" {
		t.Fatalf("exact user history not retained: %+v", msgs)
	}
	if msgs[2].Role != llm.RoleUser || !strings.HasPrefix(msgs[2].TextContent(), SummaryPrefix) || !strings.Contains(msgs[2].TextContent(), "handoff state") {
		t.Fatalf("summary=%+v", msgs[2])
	}
	if strings.Contains(strings.Join([]string{msgs[0].TextContent(), msgs[1].TextContent(), msgs[2].TextContent()}, "\n"), "old answer") {
		t.Fatal("old assistant message leaked into replacement")
	}
	_, boundary, err := sess.LatestCompactBoundary(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if boundary.WindowNumber != 1 || boundary.WindowID == "" || boundary.FirstWindowID == "" || boundary.FirstWindowID == boundary.WindowID || boundary.PreviousWindowID != boundary.FirstWindowID || len(boundary.ReplacementHistory) != 3 {
		t.Fatalf("boundary=%+v", boundary)
	}
}

func TestResumeStartsAtLatestCheckpointAndReplaysSuffix(t *testing.T) {
	ctx, sess := context.Background(), newCompactTestStore(t)
	_, _ = sess.Append(ctx, "s1", "user", "before")
	svc := Service{Sessions: sess, CompactLLM: func(context.Context) llm.LLM { return checkpointTestLLM{summary: "checkpoint one"} }}
	if _, err := svc.ManualCompactSession(ctx, "s1", "manual"); err != nil {
		t.Fatal(err)
	}
	_, _ = sess.Append(ctx, "s1", "user", "after checkpoint")
	msgs, err := sess.ListTranscriptMessages(ctx, "s1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := msgs[len(msgs)-1].TextContent(); got != "after checkpoint" {
		t.Fatalf("last=%q messages=%+v", got, msgs)
	}
	if _, err := svc.ManualCompactSession(ctx, "s1", "manual"); err != nil {
		t.Fatal(err)
	}
	_, boundary, err := sess.LatestCompactBoundary(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if boundary.WindowNumber != 2 || boundary.PreviousWindowID == "" || boundary.FirstWindowID == boundary.WindowID {
		t.Fatalf("window chain=%+v", boundary)
	}
	all, err := sess.ListAllMessages(ctx, "s1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 4 {
		t.Fatalf("append-only transcript was lost: %+v", all)
	}
}

func TestManualCompactRunsPreAndPostHooks(t *testing.T) {
	ctx, sess := context.Background(), newCompactTestStore(t)
	_, _ = sess.Append(ctx, "s1", "user", "request")
	var calls []string
	svc := Service{
		Sessions: sess, CompactLLM: func(context.Context) llm.LLM { return checkpointTestLLM{summary: "done"} },
		PreCompact:  func(context.Context, string, string) error { calls = append(calls, "pre"); return nil },
		PostCompact: func(context.Context, string, string) error { calls = append(calls, "post"); return nil },
	}
	if _, err := svc.ManualCompactSession(ctx, "s1", "manual"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "pre,post" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestManualCompactEmptyTranscriptUsesStableError(t *testing.T) {
	_, err := (Service{Sessions: newCompactTestStore(t)}).ManualCompactSession(context.Background(), "empty", "manual")
	if err == nil || !strings.Contains(err.Error(), ErrorMessageNotEnoughMessages) {
		t.Fatalf("err=%v", err)
	}
}

// The summarizer must converge onto a history that fits by shrinking the
// history it sends, not by dropping all of it. A prompt-only request would
// "succeed" while summarizing nothing.
func TestLocalCompactShrinksHistoryUntilItFits(t *testing.T) {
	history := realisticAgentHistory(30)
	full := llm.EstimateMessages(history)
	model := &retryingCompactTestLLM{tokenBudget: full / 4}
	svc := Service{}
	replacement, summary, err := svc.localCompact(context.Background(), model, history, nil, llm.DefaultEstimator())
	if err != nil {
		t.Fatal(err)
	}
	if summary != "summary of trimmed history" {
		t.Fatalf("summary=%q — expected a real model summary, not a fallback digest", summary)
	}
	if len(replacement) == 0 {
		t.Fatal("empty replacement")
	}
	// Every request must carry actual history, never just the prompt.
	for i, size := range model.sawTokens {
		if size <= 0 {
			t.Fatalf("request %d carried no content: %v", i, model.sawTokens)
		}
	}
	// Halving must converge in a handful of round-trips, not one per message.
	if model.calls > 4 {
		t.Fatalf("calls=%d sizes=%v — expected logarithmic convergence", model.calls, model.sawTokens)
	}
}

func TestRemoteFailureFallsBackToActivePrimaryLocalSummary(t *testing.T) {
	ctx := context.Background()
	model := &remoteFallbackTestLLM{compactErr: errors.New("context_length_exceeded during compact"), executeSummary: "active primary summary"}
	svc := Service{CompactLLM: func(context.Context) llm.LLM { return model }}
	replacement, res, err := svc.compactMessages(ctx, []llm.Message{llm.UserMessage(llm.Text("important user context"))}, nil, "s1", "auto", "", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if model.compactCalls != 1 || model.executeCalls != 1 {
		t.Fatalf("compactCalls=%d executeCalls=%d", model.compactCalls, model.executeCalls)
	}
	if res.Strategy != "local" || res.SummarySource != "model_summary" || res.Summary != "active primary summary" {
		t.Fatalf("result=%+v", res)
	}
	if len(replacement) != 2 || !strings.Contains(replacement[1].TextContent(), "active primary summary") {
		t.Fatalf("replacement=%+v", replacement)
	}
	if _, ok := any(executeOnlyLLM{inner: model}).(llm.ContextCompactor); ok {
		t.Fatal("local fallback wrapper must not advertise ContextCompactor")
	}
}

func TestRemoteAndLocalFailuresReturnBothReasons(t *testing.T) {
	model := &remoteFallbackTestLLM{compactErr: errors.New("status 500 remote compact"), executeErr: errors.New("local prompt failed")}
	_, _, err := (Service{CompactLLM: func(context.Context) llm.LLM { return model }}).compactMessages(context.Background(), []llm.Message{llm.UserMessage(llm.Text("history"))}, nil, "s1", "auto", "", nil, nil, false)
	if err == nil || !strings.Contains(err.Error(), "status 500 remote compact") || !strings.Contains(err.Error(), "local summary fallback failed") || !strings.Contains(err.Error(), "local prompt failed") {
		t.Fatalf("err=%v", err)
	}
}

func TestRemoteCancellationDoesNotFallback(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(fmt.Sprint(err), func(t *testing.T) {
			model := &remoteFallbackTestLLM{compactErr: err, executeSummary: "should not run"}
			_, _, gotErr := (Service{CompactLLM: func(context.Context) llm.LLM { return model }}).compactMessages(context.Background(), []llm.Message{llm.UserMessage(llm.Text("history"))}, nil, "s1", "auto", "", nil, nil, false)
			if !errors.Is(gotErr, err) || model.executeCalls != 0 {
				t.Fatalf("err=%v executeCalls=%d", gotErr, model.executeCalls)
			}
		})
	}
}

func TestCompactUserTruncationPreservesPrefixAndSuffix(t *testing.T) {
	if got, want := truncateTextToTokenBudget("abcdefghijkl", 2), "abcd…1 tokens truncated…ijkl"; got != want {
		t.Fatalf("truncate=%q want %q", got, want)
	}
}

func TestModelChangeCompactionReasons(t *testing.T) {
	if got := modelChangeCompactReason(1, 90, llm.Hit{CompHash: "old"}, llm.Hit{CompHash: "new"}); got != "comp_hash_changed" {
		t.Fatalf("hash reason=%q", got)
	}
	previous := llm.Hit{ContextWindow: 200}
	current := llm.Hit{ContextWindow: 100}
	if got := modelChangeCompactReason(91, 90, previous, current); got != "model_downshift" {
		t.Fatalf("downshift reason=%q", got)
	}
	if got := modelChangeCompactReason(60, 90, previous, current); got != "" {
		t.Fatalf("usage under the new window and threshold must not compact, got %q", got)
	}
}

// A provider/model switch with no compaction must still declare a prompt-cache
// generation boundary: the new provider's cache has never seen this session's
// prefix, so the next request is a guaranteed full miss. It must NOT force a
// compaction — a checkpoint shrinks the prompt (cost-positive) without
// improving the cache-hit ratio, and it would add a generation of its own.
func TestProviderSwitchDeclaresPrefixGenerationWithoutCompacting(t *testing.T) {
	ctx, sess := context.Background(), newCompactTestStore(t)
	llm.ResetPrefixTracking()
	t.Cleanup(llm.ResetPrefixTracking)

	if _, err := sess.Append(ctx, "s1", "user", "first request"); err != nil {
		t.Fatal(err)
	}
	// The assistant turn records the provider/model that produced it, which is
	// what the switch detector compares the current configuration against.
	if _, err := sess.AppendStructuredMessage(ctx, "s1", "assistant", "old answer",
		"msg-1", `[{"type":"text","text":"old answer"}]`, "gpt-5.6-sol", "", "", "", state.MessageExecTiming{}); err != nil {
		t.Fatal(err)
	}

	svc := Service{
		Sessions:     sess,
		PrimaryModel: func(context.Context) (string, string) { return "zhipuai", "glm-5.3" },
		ModelProvider: func(string) string {
			return "openai"
		},
		CompactLLM: func(context.Context) llm.LLM { return checkpointTestLLM{summary: "handoff"} },
	}

	should, _, err := svc.ShouldAutoCompactSession(ctx, "s1", "")
	if err != nil {
		t.Fatal(err)
	}
	if should {
		t.Fatal("a provider switch must not force compaction on its own")
	}

	snapshot := llm.PrefixCacheSnapshot()
	if len(snapshot) != 1 {
		t.Fatalf("expected one tracked key, got %+v", snapshot)
	}
	if snapshot[0].Generations != 1 {
		t.Fatalf("generations = %d, want 1 (the declared provider switch)", snapshot[0].Generations)
	}
	// Being idempotent matters: this runs on every turn before a switch is
	// acted on, and a repeated declaration would inflate G on its own.
	if _, _, err := svc.ShouldAutoCompactSession(ctx, "s1", ""); err != nil {
		t.Fatal(err)
	}
	if again := llm.PrefixCacheSnapshot(); again[0].Generations != 1 {
		t.Fatalf("repeated check inflated generations to %d", again[0].Generations)
	}
}

// opaqueRemoteCompactor mimics a provider-native compaction: the replacement
// history is an opaque item only the issuing provider can replay.
type opaqueRemoteCompactor struct{ calls int }

func (f *opaqueRemoteCompactor) Compact(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.CompactResult, error) {
	f.calls++
	return &llm.CompactResult{Messages: []llm.Message{
		{Compaction: &llm.CompactionState{ID: "cmp_provider_a", EncryptedContent: "opaque-blob-only-provider-a-can-read"}},
	}}, nil
}

func (f *opaqueRemoteCompactor) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("summary text")})
	return &llm.Result{Message: &msg}, nil
}

// A client that can compact natively does so with no switch to turn on: the
// capability is the configuration. The checkpoint it produces is the provider's
// opaque item, marked with the remote strategy so a later provider switch knows
// the history is not portable.
func TestCapableClientCompactsRemotelyWithoutASwitch(t *testing.T) {
	ctx, sess := context.Background(), newCompactTestStore(t)
	sid := "capable"
	for i := 0; i < 4; i++ {
		if _, err := sess.Append(ctx, sid, "user", strings.Repeat("requirement detail. ", 50)); err != nil {
			t.Fatal(err)
		}
		if _, err := sess.Append(ctx, sid, "assistant", "answer"); err != nil {
			t.Fatal(err)
		}
	}

	remote := &opaqueRemoteCompactor{}
	svc := Service{
		Sessions:     sess,
		CompactLLM:   func(context.Context) llm.LLM { return remote },
		PrimaryModel: func(context.Context) (string, string) { return "openai", "gpt-test" },
	}
	if _, err := svc.ManualCompactSession(ctx, sid, "manual"); err != nil {
		t.Fatal(err)
	}
	if remote.calls != 1 {
		t.Fatalf("remote compaction ran %d times, want the one manual checkpoint", remote.calls)
	}
	_, part, err := sess.LatestCompactBoundary(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if part.Strategy != strategyRemote {
		t.Fatalf("checkpoint strategy = %q, want %q", part.Strategy, strategyRemote)
	}
	if !isRemoteCompactStrategy(part.Strategy) {
		t.Fatal("a provider-native checkpoint must read back as remote")
	}
	opaque := false
	for _, msg := range part.ReplacementHistory {
		if msg.Compaction != nil {
			opaque = true
		}
	}
	if !opaque {
		t.Fatalf("remote checkpoint carries no opaque item: %+v", part.ReplacementHistory)
	}
}

// A client that cannot compact natively is summarized locally, and the
// checkpoint it leaves is plain messages any provider can replay.
func TestLocalClientSummarizesLocally(t *testing.T) {
	ctx, sess := context.Background(), newCompactTestStore(t)
	sid := "local"
	for i := 0; i < 4; i++ {
		if _, err := sess.Append(ctx, sid, "user", strings.Repeat("requirement detail. ", 50)); err != nil {
			t.Fatal(err)
		}
		if _, err := sess.Append(ctx, sid, "assistant", "answer"); err != nil {
			t.Fatal(err)
		}
	}

	svc := Service{
		Sessions:     sess,
		CompactLLM:   func(context.Context) llm.LLM { return checkpointTestLLM{summary: "handoff"} },
		PrimaryModel: func(context.Context) (string, string) { return "deepseek", "deepseek-chat" },
	}
	if _, err := svc.ManualCompactSession(ctx, sid, "manual"); err != nil {
		t.Fatal(err)
	}
	_, part, err := sess.LatestCompactBoundary(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if part.Strategy != strategyLocal {
		t.Fatalf("checkpoint strategy = %q, want %q", part.Strategy, strategyLocal)
	}
	for i, msg := range part.ReplacementHistory {
		if msg.Compaction != nil {
			t.Fatalf("checkpoint message %d carries a provider-specific opaque item", i)
		}
		if strings.TrimSpace(msg.Role) == "" {
			t.Fatalf("checkpoint message %d has no role and cannot be replayed", i)
		}
	}
}

// The cross-provider case: ChatGPT (a native compactor) produced the newest
// checkpoint, and the next turn runs on DeepSeek. Sending that opaque item to
// another provider would be unreadable, so the switch re-summarizes locally
// first — with the current model, not through the compactor — and the request
// that follows carries no opaque item.
func TestCrossProviderSwitchResummarizesLocally(t *testing.T) {
	ctx, sess := context.Background(), newCompactTestStore(t)
	sid := "provider-switch"

	if _, err := sess.Append(ctx, sid, "user", strings.Repeat("early requirement. ", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendStructuredMessage(ctx, sid, "assistant", "early answer",
		"msg-1", `[{"type":"text","text":"early answer"}]`, "gpt-5.6", "", "", "", state.MessageExecTiming{}); err != nil {
		t.Fatal(err)
	}

	// ChatGPT compacts natively: the newest checkpoint is provider-private.
	remote := &opaqueRemoteCompactor{}
	chatgpt := Service{
		Sessions:     sess,
		CompactLLM:   func(context.Context) llm.LLM { return remote },
		PrimaryModel: func(context.Context) (string, string) { return "openai", "gpt-5.6" },
	}
	if _, err := chatgpt.ManualCompactSession(ctx, sid, "manual"); err != nil {
		t.Fatal(err)
	}
	if _, part, err := sess.LatestCompactBoundary(ctx, sid); err != nil {
		t.Fatal(err)
	} else if !isRemoteCompactStrategy(part.Strategy) {
		t.Fatalf("checkpoint strategy = %q, want a remote one", part.Strategy)
	}
	// The conversation keeps going on ChatGPT: one more answered turn, which is
	// what names the provider the next request would have gone to.
	if _, err := sess.Append(ctx, sid, "user", "follow-up question"); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendStructuredMessage(ctx, sid, "assistant", "follow-up answer",
		"msg-2", `[{"type":"text","text":"follow-up answer"}]`, "gpt-5.6", "", "", "", state.MessageExecTiming{}); err != nil {
		t.Fatal(err)
	}

	// The next turn runs on DeepSeek: a different provider.
	local := &countingSummaryLLM{summary: "portable handoff"}
	deepseek := Service{
		Sessions: sess,
		CompactLLM: func(context.Context) llm.LLM {
			return local
		},
		PrimaryModel:  func(context.Context) (string, string) { return "deepseek", "deepseek-chat" },
		ModelProvider: func(string) string { return "openai" },
	}
	decision, err := deepseek.autoCompactDecision(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Should || decision.Reason != reasonRemoteCheckpointProviderSwitch {
		t.Fatalf("decision = %+v, want the provider-switch reason", decision)
	}
	if decision.PreviousModel != "" {
		t.Fatalf("previous model = %q, want empty: the switch summarizes with the current one", decision.PreviousModel)
	}
	if _, did, err := deepseek.AutoCompactSession(ctx, sid, ""); err != nil || !did {
		t.Fatalf("AutoCompactSession did=%v err=%v", did, err)
	}
	if remote.calls != 1 {
		t.Fatalf("the compactor ran %d times; the switch's checkpoint must not go through it", remote.calls)
	}
	if local.calls == 0 {
		t.Fatal("the current model never summarized the switch's checkpoint")
	}
	_, part, err := sess.LatestCompactBoundary(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if part.Strategy != strategyLocal {
		t.Fatalf("checkpoint strategy = %q, want %q", part.Strategy, strategyLocal)
	}
	for i, msg := range part.ReplacementHistory {
		if msg.Compaction != nil {
			t.Fatalf("the checkpoint the next provider would read carries an opaque item at %d", i)
		}
	}
}

// A model switch inside one provider keeps the remote checkpoint: the opaque
// item is still replayable, so the switch is not a reason to re-summarize.
func TestSameProviderModelSwitchKeepsTheRemoteCheckpoint(t *testing.T) {
	ctx, sess := context.Background(), newCompactTestStore(t)
	sid := "same-provider"

	if _, err := sess.Append(ctx, sid, "user", strings.Repeat("early requirement. ", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendStructuredMessage(ctx, sid, "assistant", "early answer",
		"msg-1", `[{"type":"text","text":"early answer"}]`, "gpt-5.6", "", "", "", state.MessageExecTiming{}); err != nil {
		t.Fatal(err)
	}
	remote := &opaqueRemoteCompactor{}
	svc := Service{
		Sessions:     sess,
		CompactLLM:   func(context.Context) llm.LLM { return remote },
		PrimaryModel: func(context.Context) (string, string) { return "openai", "gpt-5.6" },
	}
	if _, err := svc.ManualCompactSession(ctx, sid, "manual"); err != nil {
		t.Fatal(err)
	}

	// Same provider, a different model.
	next := Service{
		Sessions:      sess,
		CompactLLM:    func(context.Context) llm.LLM { return checkpointTestLLM{summary: "unused"} },
		PrimaryModel:  func(context.Context) (string, string) { return "openai", "gpt-5.7" },
		ModelProvider: func(string) string { return "openai" },
	}
	decision, err := next.autoCompactDecision(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Reason == reasonRemoteCheckpointProviderSwitch {
		t.Fatal("a same-provider model switch must keep the remote checkpoint")
	}
	if _, part, err := sess.LatestCompactBoundary(ctx, sid); err != nil {
		t.Fatal(err)
	} else if !isRemoteCompactStrategy(part.Strategy) {
		t.Fatalf("checkpoint strategy = %q, want the remote one to survive", part.Strategy)
	}
}

// countingSummaryLLM is a local summarizer: it counts the calls it answered and
// never advertises provider-native compaction.
type countingSummaryLLM struct {
	summary string
	calls   int
}

func (f *countingSummaryLLM) Execute(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	f.calls++
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(f.summary)})
	return &llm.Result{Message: &msg}, nil
}

// systemOnlyCompactor is a remote ContextCompactor whose replacement history
// survives the non-empty check in compactMessages but is emptied by
// stripInitialContext.
type systemOnlyCompactor struct{ calls int }

func (f *systemOnlyCompactor) Compact(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.CompactResult, error) {
	f.calls++
	return &llm.CompactResult{Messages: []llm.Message{llm.SystemMessage("compacted state")}}, nil
}

func (f *systemOnlyCompactor) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("unused")})
	return &llm.Result{Message: &msg}, nil
}

// A checkpoint whose ReplacementHistory is empty is a DEAD checkpoint:
// modelContextFloor rejects it and silently falls back to reading the FULL
// transcript, so every later round re-summarizes the entire history.
func TestEmptyReplacementMustNotPersistDeadCheckpoint(t *testing.T) {
	ctx := context.Background()
	sess, db := newCompactTestStoreWithDB(t)
	sid := "dead"

	bigUser := strings.Repeat("a long standing requirement sentence. ", 200)
	// A run's usage anchors occupancy only when it belongs to forebrain's own
	// request, so the seed rows must carry a real run id.
	if _, err := sess.Append(ctx, sid, "user", bigUser); err != nil {
		t.Fatal(err)
	}
	run, err := (&state.RunStore{DB: db}).CreateRun(ctx, sid, "seed")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := sess.Append(ctx, sid, "user", bigUser); err != nil {
			t.Fatal(err)
		}
		if _, err := sess.AppendStructuredMessageForRun(ctx, sid, run.ID, "assistant", "answer", "",
			state.ContentPartsJSON(nil, "answer"), "gpt-test",
			`{"input_tokens":900000,"output_tokens":500}`, "", "", state.MessageExecTiming{}); err != nil {
			t.Fatal(err)
		}
	}

	fullHistory, err := sess.ListTranscriptMessages(ctx, sid, 5000)
	if err != nil {
		t.Fatal(err)
	}
	fullTokens := llm.EstimateMessages(fullHistory)
	t.Logf("pre-compact history: %d messages, ~%d tokens", len(fullHistory), fullTokens)

	remote := &systemOnlyCompactor{}
	svc := Service{
		Sessions:     sess,
		CompactLLM:   func(context.Context) llm.LLM { return remote },
		PrimaryModel: func(context.Context) (string, string) { return "openai", "gpt-test" },
	}

	res, did, err := svc.AutoCompactSession(ctx, sid, "")
	t.Logf("compact: did=%v err=%v boundary=%q", did, err, res.BoundaryID)
	if err != nil {
		t.Logf("compaction refused to persist an empty replacement - correct behaviour")
		return
	}
	if !did {
		t.Skip("no compaction happened; nothing to assert")
	}

	// The compaction reported success. The next round must therefore see the
	// compacted window, not the whole transcript.
	afterMessages, err := sess.ListTranscriptMessages(ctx, sid, 5000)
	if err != nil {
		t.Fatal(err)
	}
	afterTokens := llm.EstimateMessages(afterMessages)
	t.Logf("next round would summarize: %d messages, ~%d tokens", len(afterMessages), afterTokens)

	if afterTokens >= fullTokens {
		t.Fatalf("DEAD CHECKPOINT: compaction reported success but the next round still reads the full history "+
			"(%d tokens vs %d before). modelContextFloor rejected the empty ReplacementHistory and silently "+
			"fell back to the entire transcript, so every round re-summarizes everything.", afterTokens, fullTokens)
	}
}

// Migrated usage measured another harness's request; it must not compact the
// session on its first native message. Once forebrain's own response lands, its
// usage anchors again.
func TestMigratedSessionDoesNotCompactOnImportedUsage(t *testing.T) {
	ctx := context.Background()
	sess, db := newCompactTestStoreWithDB(t)
	sid := "migrated"

	if _, err := sess.Append(ctx, sid, "user", "migrated question"); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendStructuredMessage(ctx, sid, "assistant", "imported answer", "",
		state.ContentPartsJSON(nil, "imported answer"), "claude-opus-5-5",
		`{"input_tokens":900089,"output_tokens":1403}`, "", "", state.MessageExecTiming{}); err != nil {
		t.Fatal(err)
	}

	svc := Service{
		Sessions:     sess,
		PrimaryModel: func(context.Context) (string, string) { return "test-provider", "test-model" },
	}
	if should, usage, err := svc.ShouldAutoCompactSession(ctx, sid, ""); err != nil || should {
		t.Fatalf("imported usage compacted a migrated session: should=%v usage=%d err=%v", should, usage, err)
	}

	seedRun, err := (&state.RunStore{DB: db}).CreateRun(ctx, sid, "seed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendStructuredMessageForRun(ctx, sid, seedRun.ID, "assistant", "native answer", "",
		state.ContentPartsJSON(nil, "native answer"), "test-model",
		`{"input_tokens":190000,"output_tokens":100}`, "", "", state.MessageExecTiming{}); err != nil {
		t.Fatal(err)
	}
	should, _, err := svc.ShouldAutoCompactSession(ctx, sid, "")
	if err != nil {
		t.Fatal(err)
	}
	if !should {
		t.Fatal("native usage above the threshold must compact")
	}
}

// scaledEstimator counts 100 tokens per byte, so a test can move one fixed
// history across a threshold by switching the estimator alone.
type scaledEstimator struct{}

func (scaledEstimator) Count(text string) int { return len(text) * 100 }
func (scaledEstimator) Name() string          { return "scaled-100" }

// The pre-turn checkpoint must count the history with the session's own model's
// tokenizer, not a process-wide default: only the estimator decides these two
// outcomes, and the same rows must cross the threshold under the session model
// and stay under it otherwise.
func TestAutoCompactDecisionUsesSessionModelEstimator(t *testing.T) {
	ctx, sess := context.Background(), newCompactTestStore(t)
	sid := "estimator"
	// Two rows whose default (rune/4) occupancy is ~1.5k, far below the
	// 180000-token threshold of an uncatalogued model. Under the 100x
	// estimator the same rows run to ~600k and cross it.
	for i := 0; i < 2; i++ {
		if _, err := sess.Append(ctx, sid, "user", strings.Repeat("x", 3000)); err != nil {
			t.Fatal(err)
		}
	}
	svc := Service{
		Sessions:     sess,
		PrimaryModel: func(context.Context) (string, string) { return "test-provider", "model-x" },
	}
	if should, _, err := svc.ShouldAutoCompactSession(ctx, sid, ""); err != nil || should {
		t.Fatalf("default estimator compacted below the threshold: should=%v err=%v", should, err)
	}

	restore := llm.SetEstimatorForTest("test-provider", "model-x", scaledEstimator{})
	should, _, err := svc.ShouldAutoCompactSession(ctx, sid, "")
	if err != nil {
		t.Fatal(err)
	}
	if !should {
		t.Fatal("the session model's estimator did not drive the compaction decision")
	}
	restore()
	if should, _, err := svc.ShouldAutoCompactSession(ctx, sid, ""); err != nil || should {
		t.Fatalf("restored estimator still compacted: should=%v err=%v", should, err)
	}
}

// The compaction card must report the occupancy the decision used — the same
// number the gauge reads — not a fresh local estimate that would contradict it.
func TestAutoCompactCardReportsDecisionUsage(t *testing.T) {
	ctx := context.Background()
	sess, db := newCompactTestStoreWithDB(t)
	sid := "card"

	if _, err := sess.Append(ctx, sid, "user", "migrated question"); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendStructuredMessage(ctx, sid, "assistant", "imported answer", "",
		state.ContentPartsJSON(nil, "imported answer"), "claude-opus-5-5",
		`{"input_tokens":900089,"output_tokens":1403}`, "", "", state.MessageExecTiming{}); err != nil {
		t.Fatal(err)
	}
	seedRun, err := (&state.RunStore{DB: db}).CreateRun(ctx, sid, "seed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendStructuredMessageForRun(ctx, sid, seedRun.ID, "assistant", "native answer", "",
		state.ContentPartsJSON(nil, "native answer"), "test-model",
		`{"input_tokens":190000,"output_tokens":100}`, "", "", state.MessageExecTiming{}); err != nil {
		t.Fatal(err)
	}

	sink := &recordingSink{}
	svc := Service{
		Sessions:     sess,
		Events:       sink,
		CompactLLM:   func(context.Context) llm.LLM { return checkpointTestLLM{summary: "handoff state"} },
		PrimaryModel: func(context.Context) (string, string) { return "test-provider", "test-model" },
	}
	should, decisionUsage, err := svc.ShouldAutoCompactSession(ctx, sid, "")
	if err != nil {
		t.Fatal(err)
	}
	if !should {
		t.Fatal("expected the native usage to schedule a compaction")
	}

	res, did, err := svc.AutoCompactSession(ctx, sid, "")
	if err != nil {
		t.Fatal(err)
	}
	if !did {
		t.Fatal("expected an automatic compaction")
	}
	if res.TokensBefore != decisionUsage {
		t.Fatalf("result TokensBefore=%d want the decision usage %d", res.TokensBefore, decisionUsage)
	}

	var started event.ContextCompactingPayload
	var done event.ContextCompactedPayload
	sawStarted, sawDone := false, false
	for _, evt := range sink.all() {
		switch evt.Type {
		case event.RunEventContextCompacting:
			sawStarted = json.Unmarshal(evt.Payload, &started) == nil
		case event.RunEventContextCompacted:
			sawDone = json.Unmarshal(evt.Payload, &done) == nil
		}
	}
	if !sawStarted || !sawDone {
		t.Fatal("compaction lifecycle events missing")
	}
	if started.TokensBefore != decisionUsage || done.TokensBefore != decisionUsage {
		t.Fatalf("card numbers: started=%d done=%d want %d", started.TokensBefore, done.TokensBefore, decisionUsage)
	}
}

// realisticAgentHistory mirrors a long agent run: system prefix, user turn,
// then many assistant(tool_calls)/tool(result) pairs.
func realisticAgentHistory(pairs int) []llm.Message {
	msgs := []llm.Message{
		llm.SystemMessage("system prompt"),
		llm.UserMessage(llm.Text("user request")),
	}
	for i := 0; i < pairs; i++ {
		id := "call_" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		msgs = append(msgs, llm.AssistantMessage(
			[]llm.ContentPart{llm.Text("thinking")},
			llm.ToolCall{ID: id, Type: "function", Function: llm.FunctionCall{Name: "grep", Arguments: `{"q":"x"}`}},
		))
		msgs = append(msgs, llm.ToolResultMessage(id, llm.Text(strings.Repeat("tool output ", 50))))
	}
	return msgs
}

// providerLikeLLM rejects structurally invalid histories the way real providers
// do, and rejects oversized histories with a context-window error.
type providerLikeLLM struct {
	limitTokens int
	calls       int
	lastErr     string
}

func (p *providerLikeLLM) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	p.calls++
	// Structural rule 1: a tool result must follow an assistant tool_call with the same ID.
	open := map[string]bool{}
	for _, m := range messages {
		switch m.Role {
		case llm.RoleAssistant:
			for _, tc := range m.ToolCalls {
				open[tc.ID] = true
			}
		case llm.RoleTool:
			if !open[m.ToolCallID] {
				p.lastErr = "orphan"
				return nil, errors.New(`400 invalid_request_error: messages: unexpected tool_call_id "` + m.ToolCallID + `" not preceded by tool_use`)
			}
		}
	}
	// Structural rule 2: first non-system message must be user role.
	for _, m := range messages {
		if m.Role == llm.RoleSystem {
			continue
		}
		if m.Role != llm.RoleUser {
			p.lastErr = "first-not-user"
			return nil, errors.New("400 invalid_request_error: messages: first message must use the user role")
		}
		break
	}
	if llm.EstimateMessages(messages) > p.limitTokens {
		p.lastErr = "overflow"
		// Verbatim shape of the reported failure: no limit number anywhere.
		return nil, errors.New(`{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","param":"input"}`)
	}
	p.lastErr = ""
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("summary of the run")})
	return &llm.Result{Message: &msg}, nil
}

// TestLocalCompactOnRealisticOverflowHistory is the regression test for the
// reported bug: on a provider context overflow the compact path must produce a
// checkpoint instead of failing with a structural 400 that ends the turn.
func TestLocalCompactOnRealisticOverflowHistory(t *testing.T) {
	history := realisticAgentHistory(40)
	full := llm.EstimateMessages(history)
	model := &providerLikeLLM{limitTokens: full / 8}
	svc := Service{}
	replacement, summary, err := svc.localCompact(context.Background(), model, history, nil, llm.DefaultEstimator())
	t.Logf("calls=%d lastErr=%q err=%v", model.calls, model.lastErr, err)
	if err != nil {
		t.Fatalf("localCompact failed: %v (calls=%d, lastErr=%s)", err, model.calls, model.lastErr)
	}
	if summary != "summary of the run" {
		t.Fatalf("summary=%q — expected a real model summary", summary)
	}
	if len(replacement) == 0 {
		t.Fatalf("empty replacement")
	}
	// The old implementation dropped one message per round-trip: 80+ calls, each
	// a real API request, and it aborted partway on a structural 400.
	if model.calls > 5 {
		t.Fatalf("localCompact needed %d provider round-trips to converge", model.calls)
	}
	if model.lastErr == "orphan" || model.lastErr == "first-not-user" {
		t.Fatalf("trimming produced a structurally invalid history: %s", model.lastErr)
	}
}

// Every intermediate trim must be a history the provider would accept, so a
// size retry never turns into a structural 400.
func TestTrimLadderIsAlwaysStructurallyValid(t *testing.T) {
	history := realisticAgentHistory(40)
	full := llm.EstimateMessages(history)
	for budget := full; budget > 0; budget /= 2 {
		trimmed := trimHistoryToFit(history, budget, llm.DefaultEstimator())
		if len(trimmed) == 0 {
			continue
		}
		model := &providerLikeLLM{limitTokens: 1 << 30}
		if _, err := model.Execute(context.Background(), trimmed, nil); err != nil {
			t.Fatalf("budget=%d produced invalid history: %v", budget, err)
		}
	}
}

// tenfoldEstimator counts ten times what the default estimator counts, so a
// test can make the session model's estimator disagree with the default one.
type tenfoldEstimator struct{}

func (tenfoldEstimator) Count(text string) int { return 10 * llm.DefaultEstimator().Count(text) }
func (tenfoldEstimator) Name() string          { return "tenfold" }

// recordingSummaryLLM answers every request with a summary and records how
// many messages each request carried.
type recordingSummaryLLM struct{ sizes []int }

func (r *recordingSummaryLLM) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	r.sizes = append(r.sizes, len(messages))
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("summary of the trimmed history")})
	return &llm.Result{Message: &msg}, nil
}

// Whether the history fits the summary request is judged with the session
// model's estimator. A history that fits by the default estimate but not by
// the model's own is not sent as the conversation's request; it is trimmed to
// the budget and summarized on its own.
func TestLocalCompactBudgetsWithSessionEstimator(t *testing.T) {
	llm.ResetObservedForTest()
	t.Cleanup(llm.ResetObservedForTest)
	const provider, model = "test-provider", "budget-model"
	// The model is not in the catalog; an accepted 100k-token prompt gives it a
	// known window, and so a summary budget.
	llm.RecordSuccess(provider, model, 100_000, 0)
	ctx := context.Background()
	svc := Service{PrimaryModel: func(context.Context) (string, string) { return provider, model }}
	budget := svc.summaryInputBudget(ctx, nil)
	if budget <= 0 {
		t.Fatalf("fixture: %s/%s has no summary budget", provider, model)
	}

	// A short opening request, then 50 turns sized so the default estimate is
	// about half the budget and the tenfold one about five times it.
	const turns = 50
	history := []llm.Message{llm.SystemMessage("system prompt"), llm.UserMessage(llm.Text("user request"))}
	body := budget * 4 / 2 / turns // bytes: rune/4 makes each turn budget/(2*turns) tokens
	for i := 0; i < turns; i++ {
		text := fmt.Sprintf("turn %d ", i) + strings.Repeat("x", body)
		if i%2 == 0 {
			history = append(history, llm.AssistantMessage([]llm.ContentPart{llm.Text(text)}))
		} else {
			history = append(history, llm.UserMessage(llm.Text(text)))
		}
	}
	if size := llm.EstimateMessagesWith(llm.DefaultEstimator(), history); size > budget {
		t.Fatalf("fixture: default estimate %d must fit the budget %d", size, budget)
	}
	tenfold := llm.EstimateMessagesWith(tenfoldEstimator{}, history)
	if tenfold <= budget {
		t.Fatalf("fixture: tenfold estimate %d must exceed the budget %d", tenfold, budget)
	}

	t.Cleanup(llm.SetEstimatorForTest(provider, model, tenfoldEstimator{}))
	conversationCalls := 0
	conversation := func(context.Context, llm.Message) (*llm.Result, error) {
		conversationCalls++
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("conversation summary")})
		return &llm.Result{Message: &msg}, nil
	}
	client := &recordingSummaryLLM{}
	replacement, result, err := svc.compactMessages(ctx, history, nil, "s1", "auto", "", client, conversation, false)
	if err != nil {
		t.Fatal(err)
	}
	if conversationCalls != 0 {
		t.Fatalf("the conversation's request was used %d times for a history that does not fit by the session estimator", conversationCalls)
	}
	if len(client.sizes) != 1 || client.sizes[0]-1 >= len(history) {
		t.Fatalf("summary requests carried %v messages (prompt included), want one request trimmed below the %d-message history", client.sizes, len(history))
	}
	if result.Summary != "summary of the trimmed history" || len(replacement) == 0 {
		t.Fatalf("result = %+v, replacement %d messages; want the trimmed request's summary", result, len(replacement))
	}
	// The card's numbers are in the same unit.
	if result.TokensBefore != tenfold {
		t.Fatalf("TokensBefore = %d, want the session estimator's %d", result.TokensBefore, tenfold)
	}
	if want := llm.EstimateMessagesWith(tenfoldEstimator{}, replacement); result.TokensAfter != want {
		t.Fatalf("TokensAfter = %d, want the session estimator's %d", result.TokensAfter, want)
	}
}

// trimHistoryToFit sizes the history with the estimator it is given: under an
// estimator that counts more, fewer groups fit the same budget.
func TestTrimHistoryToFitUsesGivenEstimator(t *testing.T) {
	history := realisticAgentHistory(40)
	budget := llm.EstimateMessagesWith(llm.DefaultEstimator(), history) / 2
	byDefault := trimHistoryToFit(history, budget, llm.DefaultEstimator())
	byTenfold := trimHistoryToFit(history, budget, tenfoldEstimator{})
	if len(byTenfold) == 0 || len(byTenfold) >= len(byDefault) {
		t.Fatalf("kept %d messages under the tenfold estimator and %d under the default, want 0 < tenfold < default",
			len(byTenfold), len(byDefault))
	}
	if size := llm.EstimateMessagesWith(tenfoldEstimator{}, byTenfold); size > budget {
		t.Fatalf("tenfold trim measures %d by its own estimator, over the budget %d", size, budget)
	}
}
