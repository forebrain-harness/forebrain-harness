package assembly

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

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

func (f *remoteFallbackTestLLM) Compact(context.Context, []llm.Message, []*llm.Tool, llm.CompactMode) (*llm.CompactResult, error) {
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

func newCompactTestStore(t *testing.T) *state.SessionStore {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return state.NewSessionStore(db, "main")
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
	replacement, summary, err := svc.localCompact(context.Background(), model, history, nil)
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
	svc := Service{CompactLLM: func(context.Context) llm.LLM { return model }, RemoteCompaction: true}
	replacement, res, err := svc.compactMessages(ctx, []llm.Message{llm.UserMessage(llm.Text("important user context"))}, nil, "s1", "auto", "", nil, nil)
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
	_, _, err := (Service{CompactLLM: func(context.Context) llm.LLM { return model }, RemoteCompaction: true}).compactMessages(context.Background(), []llm.Message{llm.UserMessage(llm.Text("history"))}, nil, "s1", "auto", "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "status 500 remote compact") || !strings.Contains(err.Error(), "local summary fallback failed") || !strings.Contains(err.Error(), "local prompt failed") {
		t.Fatalf("err=%v", err)
	}
}

func TestRemoteCancellationDoesNotFallback(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(fmt.Sprint(err), func(t *testing.T) {
			model := &remoteFallbackTestLLM{compactErr: err, executeSummary: "should not run"}
			_, _, gotErr := (Service{CompactLLM: func(context.Context) llm.LLM { return model }, RemoteCompaction: true}).compactMessages(context.Background(), []llm.Message{llm.UserMessage(llm.Text("history"))}, nil, "s1", "auto", "", nil, nil)
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
	if got := modelChangeCompactReason(1, "total", 90, llm.Hit{CompHash: "old"}, llm.Hit{CompHash: "new"}); got != "comp_hash_changed" {
		t.Fatalf("hash reason=%q", got)
	}
	previous := llm.Hit{ContextWindow: 200}
	current := llm.Hit{ContextWindow: 100}
	if got := modelChangeCompactReason(91, "total", 90, previous, current); got != "model_downshift" {
		t.Fatalf("downshift reason=%q", got)
	}
	if got := modelChangeCompactReason(91, "body_after_prefix", 90, previous, current); got != "" {
		t.Fatalf("body scope must wait for hard cap, got %q", got)
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

func (f *opaqueRemoteCompactor) Compact(_ context.Context, _ []llm.Message, _ []*llm.Tool, _ llm.CompactMode) (*llm.CompactResult, error) {
	f.calls++
	return &llm.CompactResult{Messages: []llm.Message{
		{Compaction: &llm.CompactionState{ID: "cmp_provider_a", EncryptedContent: "opaque-blob-only-provider-a-can-read"}},
	}}, nil
}

func (f *opaqueRemoteCompactor) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("summary text")})
	return &llm.Result{Message: &msg}, nil
}

// Compaction must support multi-round conversations that switch provider and
// model. With remote compaction left at its default (disabled), the checkpoint
// is built by local summarization and is therefore plain, portable messages
// that any provider can replay.
func TestDefaultCheckpointIsPortableAcrossProviders(t *testing.T) {
	ctx, sess := context.Background(), newCompactTestStore(t)
	sid := "portable"

	for i := 0; i < 4; i++ {
		if _, err := sess.Append(ctx, sid, "user", strings.Repeat("requirement detail. ", 50)); err != nil {
			t.Fatal(err)
		}
		if _, err := sess.Append(ctx, sid, "assistant", "answer"); err != nil {
			t.Fatal(err)
		}
	}

	remote := &opaqueRemoteCompactor{}
	// Default Service: RemoteCompaction is not set, so it stays disabled.
	svc := Service{
		Sessions:     sess,
		CompactLLM:   func(context.Context) llm.LLM { return remote },
		PrimaryModel: func(context.Context) (string, string) { return "openai", "gpt-test" },
	}

	if _, err := svc.ManualCompactSession(ctx, sid, "manual"); err != nil {
		t.Fatal(err)
	}
	if remote.calls != 0 {
		t.Fatalf("remote compaction ran %d times despite being disabled by default", remote.calls)
	}

	_, part, err := sess.LatestCompactBoundary(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(part.ReplacementHistory) == 0 {
		t.Fatal("checkpoint has no replacement history: model-context readers would fall back to the full transcript")
	}
	for i, msg := range part.ReplacementHistory {
		if msg.Compaction != nil {
			t.Fatalf("checkpoint message %d carries a provider-specific opaque item, so switching provider would strand the session", i)
		}
		if strings.TrimSpace(msg.Role) == "" {
			t.Fatalf("checkpoint message %d has no role and cannot be replayed", i)
		}
	}

	// The projection must be usable regardless of which provider runs next.
	projected, err := sess.ListTranscriptMessages(ctx, sid, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected) == 0 {
		t.Fatal("projection is empty after compaction")
	}
	t.Logf("portable checkpoint: %d messages, ~%d tokens", len(projected), llm.EstimateMessages(projected))
}

// systemOnlyCompactor is a remote ContextCompactor whose replacement history
// survives the non-empty check in compactMessages but is emptied by
// stripInitialContext.
type systemOnlyCompactor struct{ calls int }

func (f *systemOnlyCompactor) Compact(_ context.Context, _ []llm.Message, _ []*llm.Tool, _ llm.CompactMode) (*llm.CompactResult, error) {
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
	ctx, sess := context.Background(), newCompactTestStore(t)
	sid := "dead"

	bigUser := strings.Repeat("a long standing requirement sentence. ", 200)
	for i := 0; i < 6; i++ {
		if _, err := sess.Append(ctx, sid, "user", bigUser); err != nil {
			t.Fatal(err)
		}
		if _, err := sess.AppendStructuredMessage(ctx, sid, "assistant", "answer", "",
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
		Sessions:         sess,
		CompactLLM:       func(context.Context) llm.LLM { return remote },
		PrimaryModel:     func(context.Context) (string, string) { return "openai", "gpt-test" },
		RemoteCompaction: true, // opt in to remote path
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
	replacement, summary, err := svc.localCompact(context.Background(), model, history, nil)
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
		trimmed := trimHistoryToFit(history, budget)
		if len(trimmed) == 0 {
			continue
		}
		model := &providerLikeLLM{limitTokens: 1 << 30}
		if _, err := model.Execute(context.Background(), trimmed, nil); err != nil {
			t.Fatalf("budget=%d produced invalid history: %v", budget, err)
		}
	}
}
