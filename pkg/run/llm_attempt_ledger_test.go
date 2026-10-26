package run

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// ledgerRetryLLM is the vertical slice's provider in miniature: attempt 1 is
// refused with an HTTP 503, attempt 2 answers with usage.
type ledgerRetryLLM struct{ calls int }

func (m *ledgerRetryLLM) Execute(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	time.Sleep(10 * time.Millisecond)
	if m.calls == 1 {
		return nil, &llm.APIError{StatusCode: 503, Message: "model overloaded"}
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("full answer")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{InputTokens: 120, OutputTokens: 30}}, nil
}

// TestLLMAttemptLedgerRecordsRetryThenSuccess pins the slice plan 165 asked
// for: a request that fails once and is retried leaves two attempt rows — the
// 503 with its wait and no usage (a transport failure reports none), and the
// successful resend with the response's own usage. Both rows share one call
// number; the attempts advance.
func TestLLMAttemptLedgerRecordsRetryThenSuccess(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const sid = "ledger-slice"
	if err := state.NewSessionStore(db, "main").Ensure(ctx, sid, sid); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	runs := &state.RunStore{DB: db}
	rr, err := runs.CreateRun(ctx, sid, "hi")
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	inner := &ledgerRetryLLM{}
	wrapped := wrapNetworkRetryLLM(inner, 3, 0, newLLMAttemptLedger(runs))

	callCtx := tool.WithRunID(ctx, rr.ID)
	// A sink is what makes WithStreamProgress install its record on the
	// context — the same shape the real chain runs with.
	callCtx = llm.WithStreamSink(callCtx, &llm.StreamSink{})
	callCtx, _ = llm.WithStreamProgress(callCtx)
	res, err := wrapped.Execute(callCtx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "full answer" {
		t.Fatalf("result = %+v, want the retried attempt's answer", res)
	}
	if inner.calls != 2 {
		t.Fatalf("provider calls = %d, want 2 (refused + re-sent)", inner.calls)
	}

	rows, err := runs.ListLLMAttempts(ctx, rr.ID, 0)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("attempt rows = %d, want 2 (the failure and the success):\n%+v", len(rows), rows)
	}
	failed, ok := rows[0], rows[1]
	// The refused attempt: 503, a back-off ahead of it, no usage — NULL, never
	// an estimate.
	if failed.Call != 1 || failed.Attempt != 1 || failed.Status != 503 || failed.Outcome != state.LLMAttemptFailed {
		t.Fatalf("failure row = %+v, want call 1 attempt 1 status 503 failed", failed)
	}
	if failed.WaitMs <= 0 {
		t.Fatalf("failure row wait_ms = %d, want the back-off before the resend", failed.WaitMs)
	}
	if failed.ElapsedMs < 5 {
		t.Fatalf("failure row elapsed_ms = %d, want the dead attempt's own time", failed.ElapsedMs)
	}
	if failed.InputTokens != nil || failed.OutputTokens != nil {
		t.Fatalf("failure row usage = %v/%v, want nil (a refused request reports none)", failed.InputTokens, failed.OutputTokens)
	}
	// The attempt that settled the request: success with the response's usage.
	if ok.Call != 1 || ok.Attempt != 2 || ok.Outcome != state.LLMAttemptSuccess {
		t.Fatalf("success row = %+v, want call 1 attempt 2 success", ok)
	}
	if ok.InputTokens == nil || *ok.InputTokens != 120 || ok.OutputTokens == nil || *ok.OutputTokens != 30 {
		t.Fatalf("success row usage = %v/%v, want 120/30 from the response", ok.InputTokens, ok.OutputTokens)
	}
	if ok.ElapsedMs < 5 {
		t.Fatalf("success row elapsed_ms = %d, want the attempt's own time", ok.ElapsedMs)
	}
}

// ledgerOverloadedLLM refuses the first request with a 503 and answers the
// resend.
type ledgerOverloadedLLM struct{ calls int }

func (m *ledgerOverloadedLLM) Execute(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	time.Sleep(10 * time.Millisecond)
	if m.calls == 1 {
		return nil, &llm.APIError{StatusCode: 503, Message: "model overloaded"}
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("answered")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{InputTokens: 80, OutputTokens: 12}}, nil
}

// TestLLMAttemptLedgerFillsFromASupervisedRun runs a whole supervised turn —
// the real chain Load builds, a real run row, a provider that refuses once —
// and reads the ledger back the way the cost summary does. The run's own
// usage_llm_calls counts physical attempts (2), the same unit the ledger
// holds; the token columns count only what the provider billed (the refusal
// died before any usage was reported), which here is the answer that landed.
func TestLLMAttemptLedgerFillsFromASupervisedRun(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const sid = "ledger-supervised"
	if err := state.NewSessionStore(db, "main").Ensure(ctx, sid, sid); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	runs := &state.RunStore{DB: db}

	model := &ledgerOverloadedLLM{}
	restore := SetLLMOverrideForTest(model)
	defer restore()
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "gpt-main", APIKey: "k", BaseURL: "http://127.0.0.1:9/v1"}}},
	}}}
	r := &Runner{Deps: &Deps{Home: home, AgentName: "main", AppCfg: cfg, SessionStore: state.NewSessionStore(db, "main")}, FileResolver: PathResolver{}}
	r.RunRT = runs
	if err := r.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	var runID string
	res, _, err := Run(Options{
		RunRT:    runs,
		Runner:   r,
		Hooks:    hook.NewAgentPipeline(),
		AgBase:   context.Background(),
		HC:       hook.HookContext{SessionID: sid, Trigger: "user"},
		Input:    "hello",
		RunIDOut: &runID,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res == nil || res.TextContent() != "answered" {
		t.Fatalf("result = %+v, want the retried answer", res)
	}
	if model.calls != 2 {
		t.Fatalf("provider calls = %d, want 2 (refused + answered)", model.calls)
	}

	// The read side's own lookup: the session's latest primary run is the one
	// the ledger rows belong to.
	latest, err := runs.LatestPrimaryRunIDForSession(ctx, sid)
	if err != nil {
		t.Fatalf("latest primary run: %v", err)
	}
	if latest != runID {
		t.Fatalf("latest primary run = %q, want %q", latest, runID)
	}
	rows, err := runs.ListLLMAttempts(ctx, runID, 0)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("attempt rows = %d, want 2 (the 503 and the answer):\n%+v", len(rows), rows)
	}
	if rows[0].Attempt != 1 || rows[0].Status != 503 || rows[0].Outcome != state.LLMAttemptFailed {
		t.Fatalf("first row = %+v, want attempt 1 status 503 failed", rows[0])
	}
	if rows[1].Attempt != 2 || rows[1].Outcome != state.LLMAttemptSuccess ||
		rows[1].InputTokens == nil || *rows[1].InputTokens != 80 || rows[1].OutputTokens == nil || *rows[1].OutputTokens != 12 {
		t.Fatalf("second row = %+v, want attempt 2 success with the answer's usage", rows[1])
	}
	// Not in contradiction with the run's own figures: two physical
	// attempts, of which only the answer that landed billed anything (the
	// 503 died before the provider reported usage).
	var llmCalls, promptTokens, completionTokens int
	if err := db.QueryRowContext(ctx,
		`SELECT usage_llm_calls, usage_prompt_tokens, usage_completion_tokens FROM fb_runs WHERE id=?`, runID).
		Scan(&llmCalls, &promptTokens, &completionTokens); err != nil {
		t.Fatalf("read run usage: %v", err)
	}
	if llmCalls != 2 {
		t.Fatalf("usage_llm_calls = %d, want 2 physical attempts", llmCalls)
	}
	if promptTokens != 80 || completionTokens != 12 {
		t.Fatalf("run usage = %d/%d, want 80/12 — only the answer that billed", promptTokens, completionTokens)
	}
}
