package process

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/session"
	state "github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/testutil"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// fakeLLMServer answers OpenAI chat/completions with one canned assistant
// message, so a turn can run end to end without a provider.
func fakeLLMServer(t *testing.T, reply string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo the prompt back alongside the canned reply, so a test comparing
		// two surfaces' transcripts can see any difference in what was sent.
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		// Join every user message, not just the last one: the last is the
		// injected environment context, which is identical across surfaces and
		// would hide a difference in the actual prompt.
		userTexts := make([]string, 0, len(req.Messages))
		for _, m := range req.Messages {
			if m.Role != "user" {
				continue
			}
			if text, ok := m.Content.(string); ok {
				userTexts = append(userTexts, text)
			}
		}
		lastUser := strings.Join(userTexts, "\u0000")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "gpt-test",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": reply + "|" + lastUser},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func envWithFakeLLM(t *testing.T, reply string) *Environment {
	t.Helper()
	return envWithFakeLLMForSource(t, reply, "tui")
}

// ensureExecutorSession creates the conversation a submitted turn runs in;
// the executor's run row belongs to it by foreign key.
func ensureExecutorSession(t *testing.T, env *Environment, sid string) {
	t.Helper()
	if err := env.Deps.SessionStore.Ensure(context.Background(), sid, sid); err != nil {
		t.Fatal(err)
	}
}

func envWithFakeLLMForSource(t *testing.T, reply, sessionSource string) *Environment {
	t.Helper()
	t.Setenv("FOREBRAIN_EXECUTOR_TEST_KEY", "test-key")
	srv := fakeLLMServer(t, reply)
	home := t.TempDir()
	// The background memory pipeline is off for these tests. It is fire-and-
	// forget (memory.Pipeline has no Stop or Wait), so its goroutines outlive
	// Environment.Close and keep writing under the home directory — which
	// races t.TempDir's removal and, more seriously, keeps using the SQL
	// handle Close just shut. That lifecycle gap is real and worth fixing, but
	// it is not what these tests are about, and leaving it in the way would
	// make them flaky for a reason unrelated to the adapter.
	cfg := "features:\n" +
		"  memories: false\n" +
		"agents:\n" +
		"  definitions:\n" +
		"    main:\n" +
		"      llm_providers:\n" +
		"        - provider: openai\n" +
		"          model: gpt-test\n" +
		"          api_key: ${FOREBRAIN_EXECUTOR_TEST_KEY}\n" +
		"          base_url: " + srv.URL + "/v1\n" +
		// The compat client streams by default; the canned server above answers
		// with a plain JSON body, so the turn must ask for a non-streamed reply.
		"          params:\n" +
		"            stream: false\n"
	if err := os.WriteFile(filepath.Join(home, "forebrain.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := Open(context.Background(), OpenOptions{
		Home:          home,
		ConfigPath:    filepath.Join(home, "forebrain.yaml"),
		LaunchDir:     t.TempDir(),
		SessionSource: sessionSource,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(env.Close)
	return env
}

type collectingSink struct {
	mu  sync.Mutex
	got []event.RunEvent
}

func (s *collectingSink) Publish(_ context.Context, e event.RunEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, e)
	return nil
}

func (s *collectingSink) events() []event.RunEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]event.RunEvent(nil), s.got...)
}

// TestSubmitRunsATurnThroughTheProcessExecutor is the point of the adapter:
// turn.Service.Submit had no production RunExecutor behind it, so the
// canonical turn path was defined but never executed. This drives a real turn
// end to end through Submit — real Environment, real runner, real run store,
// fake provider — and is the test that would have failed before the adapter
// existed, since Submit returned ErrNoRunExecutor.
func TestSubmitRunsATurnThroughTheProcessExecutor(t *testing.T) {
	env := envWithFakeLLM(t, "hello from the model")
	svc := turn.New(
		turn.WithSessionStore(env.Deps.SessionStore),
		turn.WithForegroundLocker(env.Foreground),
		turn.WithRunExecutor(env.NewRunExecutor("tui", RunExecutorOptions{})),
	)
	sink := &collectingSink{}

	ensureExecutorSession(t, env, "session-1")
	out, err := svc.Submit(context.Background(), turn.TurnRequest{
		SessionID: "session-1",
		UserText:  "hi",
	}, sink)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if out.Status != turn.TurnCompleted {
		t.Fatalf("status = %q, want completed (error: %v)", out.Status, out.Error)
	}
	if got := out.Result.TextContent(); !strings.Contains(got, "hello from the model") {
		t.Fatalf("result = %q, want the model's reply", got)
	}

	// The run ID must be the one the store assigned, not turn's synthetic
	// "run-N" fallback: every event Submit publishes is keyed by it, and the
	// run row has to be findable under the same ID.
	if strings.HasPrefix(out.RunID, "run-") || strings.TrimSpace(out.RunID) == "" {
		t.Fatalf("RunID = %q, want the store-assigned id rather than the synthetic fallback", out.RunID)
	}
	stored, err := env.Deps.RunRT.GetRun(context.Background(), out.RunID)
	if err != nil || stored == nil {
		t.Fatalf("GetRun(%q) = %v, %v; the outcome's run ID does not exist in the store", out.RunID, stored, err)
	}
	if stored.SessionID != "session-1" {
		t.Fatalf("stored run session = %q, want session-1", stored.SessionID)
	}

	// Every event must carry that same run ID, which is the property the
	// callback exists for.
	evts := sink.events()
	if len(evts) == 0 {
		t.Fatal("no events published")
	}
	var sawStarted, sawCompleted bool
	for _, e := range evts {
		if e.RunID != out.RunID {
			t.Fatalf("event %q carries run ID %q, want %q: the stream is split across two ids",
				e.Type, e.RunID, out.RunID)
		}
		switch e.Type {
		case event.RunEventTurnStarted:
			sawStarted = true
		case event.RunEventTurnCompleted:
			sawCompleted = true
		}
	}
	if !sawStarted || !sawCompleted {
		t.Fatalf("events = %+v, want both turn_started and turn_completed", evts)
	}

	// The controller must be idle again, otherwise the next turn on this
	// session is refused.
	if n := env.Control.Active(); n != 0 {
		t.Fatalf("controller reports %d active runs after a completed turn, want 0", n)
	}
}

// The adapter must not take the foreground lock: Submit already holds it for
// the whole call, and session.Locker is a per-id semaphore rather than a
// reentrant mutex, so a second acquisition inside the executor would deadlock
// against the first and hang the turn forever.
func TestExecutorDoesNotRelockTheForegroundLock(t *testing.T) {
	env := envWithFakeLLM(t, "ok")
	svc := turn.New(
		turn.WithSessionStore(env.Deps.SessionStore),
		turn.WithForegroundLocker(env.Foreground),
		turn.WithRunExecutor(env.NewRunExecutor("tui", RunExecutorOptions{})),
	)

	ensureExecutorSession(t, env, "s-lock")
	done := make(chan error, 1)
	go func() {
		_, err := svc.Submit(context.Background(), turn.TurnRequest{SessionID: "s-lock", UserText: "hi"}, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Submit did not return: the executor most likely re-acquired the foreground lock Submit already holds")
	}

	// And the lock is released afterwards, so a second turn can run.
	unlock, err := env.Foreground.Lock(context.Background(), "s-lock")
	if err != nil {
		t.Fatalf("foreground lock still held after the turn: %v", err)
	}
	unlock()
	var _ *session.Locker = env.Foreground
}

// BeforeAgent is the surface's hook onto the new run, and it must fire with
// the same run ID the outcome reports — the TUI attaches its event mirror
// there, so a mismatch would mirror a run nobody is watching.
func TestExecutorBeforeAgentSeesTheRealRunID(t *testing.T) {
	env := envWithFakeLLM(t, "ok")
	var seen []string
	svc := turn.New(
		turn.WithSessionStore(env.Deps.SessionStore),
		turn.WithForegroundLocker(env.Foreground),
		turn.WithRunExecutor(env.NewRunExecutor("tui", RunExecutorOptions{
			BeforeAgent: func(_ context.Context, runID, sessionID, _ string) error {
				seen = append(seen, runID+"/"+sessionID)
				return nil
			},
		})),
	)

	ensureExecutorSession(t, env, "s-hook")
	out, err := svc.Submit(context.Background(), turn.TurnRequest{SessionID: "s-hook", UserText: "hi"}, nil)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("BeforeAgent fired %d times, want exactly 1: %v", len(seen), seen)
	}
	if want := out.RunID + "/s-hook"; seen[0] != want {
		t.Fatalf("BeforeAgent saw %q, want %q", seen[0], want)
	}
}

// toolCallLLMServer answers the first request with a tool call the default
// approval policy gates, so the turn stops at an approval gate instead of
// completing.
func toolCallLLMServer(t *testing.T) *httptest.Server {
	t.Helper()
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "gpt-test",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "done"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
		}
		once.Do(func() {
			body["choices"] = []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role": "assistant", "content": "",
					"tool_calls": []map[string]any{{
						"id": "call-1", "type": "function",
						"function": map[string]any{
							"name":      "shell",
							"arguments": `{"command":"rm -rf /tmp/whatever"}`,
						},
					}},
				},
				"finish_reason": "tool_calls",
			}}
		})
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A run that stops at an approval gate is paused, not finished. If the
// executor called Controller.Finish on it, the session would look idle while a
// gate is still open and a concurrent turn could start against it — and the
// resume path, which expects to claim the run itself, would find it already
// finished.
func TestExecutorDoesNotFinishARunPausedAtAnApprovalGate(t *testing.T) {
	t.Setenv("FOREBRAIN_EXECUTOR_TEST_KEY", "test-key")
	srv := toolCallLLMServer(t)
	home := t.TempDir()
	// The background memory pipeline is off for these tests. It is fire-and-
	// forget (memory.Pipeline has no Stop or Wait), so its goroutines outlive
	// Environment.Close and keep writing under the home directory — which
	// races t.TempDir's removal and, more seriously, keeps using the SQL
	// handle Close just shut. That lifecycle gap is real and worth fixing, but
	// it is not what these tests are about, and leaving it in the way would
	// make them flaky for a reason unrelated to the adapter.
	cfg := "features:\n" +
		"  memories: false\n" +
		"agents:\n" +
		"  definitions:\n" +
		"    main:\n" +
		"      llm_providers:\n" +
		"        - provider: openai\n" +
		"          model: gpt-test\n" +
		"          api_key: ${FOREBRAIN_EXECUTOR_TEST_KEY}\n" +
		"          base_url: " + srv.URL + "/v1\n" +
		"          params:\n" +
		"            stream: false\n"
	if err := os.WriteFile(filepath.Join(home, "forebrain.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := Open(context.Background(), OpenOptions{
		Home: home, ConfigPath: filepath.Join(home, "forebrain.yaml"),
		LaunchDir: t.TempDir(), SessionSource: "tui",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(env.Close)

	svc := turn.New(
		turn.WithSessionStore(env.Deps.SessionStore),
		turn.WithForegroundLocker(env.Foreground),
		turn.WithRunExecutor(env.NewRunExecutor("tui", RunExecutorOptions{})),
	)
	ensureExecutorSession(t, env, "s-gate")
	out, _ := svc.Submit(context.Background(), turn.TurnRequest{SessionID: "s-gate", UserText: "delete it"}, nil)

	// run raises tool.RequiresActionError; Submit only understands
	// turn.WaitingError. Without the adapter translating between them, a
	// perfectly normal approval pause is reported as a failed turn.
	if out.Status != turn.TurnWaitingApproval {
		t.Fatalf("status = %q (error %v), want waiting_approval: an approval gate is a pause, not a failure", out.Status, out.Error)
	}
	if out.Approval == nil || strings.TrimSpace(out.Approval.PermissionToolName) == "" {
		t.Fatalf("outcome carries no approval request (%+v); the surface has nothing to render", out.Approval)
	}
	// The resume state must survive the trip through Submit. This is what
	// ChatSession needs after a gated turn (persistRequiresActionSnapshot and
	// attachRunWaitForRequiresAction both read the snapshot), and it is
	// deliberately not on Approval, which is the render projection.
	if out.Resume == nil {
		t.Fatal("outcome carries no resume state; a surface cannot continue past the gate")
	}
	if strings.TrimSpace(out.Resume.ActionID) == "" {
		t.Fatalf("resume state has no action ID: %+v", out.Resume)
	}
	if out.Resume.ToolName != "shell" {
		t.Fatalf("resume tool name = %q, want the tool that raised the gate", out.Resume.ToolName)
	}
	if len(out.Resume.SessionSnapshot) == 0 {
		t.Fatal("resume snapshot is empty; resuming would replay nothing and re-execute the gated call")
	}
	// The snapshot has to contain the assistant tool_use that triggered the
	// gate, otherwise the replay cannot match the pending call.
	var sawToolUse bool
	for _, m := range out.Resume.SessionSnapshot {
		if len(m.ToolCalls) > 0 {
			sawToolUse = true
		}
	}
	if !sawToolUse {
		t.Fatalf("resume snapshot has no assistant tool_use: %+v", out.Resume.SessionSnapshot)
	}

	// The run must still be active: the resume path owns it from here.
	if n := env.Control.Active(); n == 0 {
		t.Fatal("controller reports no active runs while the turn waits at an approval gate: the paused run was finished, so the session looks idle and resume has nothing to claim")
	}
}

// A caller that installed its own PartialSessionCapture must keep it. The TUI
// does exactly that: it holds the capture so it can render and persist the
// partial turn itself. If the executor overwrote the context value, the
// caller's capture would stay empty and a cancelled turn would silently lose
// every message the user had already watched appear.
func TestExecutorKeepsACallerSuppliedPartialCapture(t *testing.T) {
	env := envWithFakeLLM(t, "ok")
	mine := run.NewPartialSessionCapture()
	ctx := run.WithPartialSessionCapture(context.Background(), mine)

	var seen *run.PartialSessionCapture
	svc := turn.New(
		turn.WithSessionStore(env.Deps.SessionStore),
		turn.WithForegroundLocker(env.Foreground),
		turn.WithRunExecutor(env.NewRunExecutor("tui", RunExecutorOptions{
			BeforeAgent: func(agentCtx context.Context, _, _, _ string) error {
				seen = run.PartialSessionCaptureFromContext(agentCtx)
				return nil
			},
		})),
	)
	ensureExecutorSession(t, env, "s-capture")
	if _, err := svc.Submit(ctx, turn.TurnRequest{SessionID: "s-capture", UserText: "hi"}, nil); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if seen == nil {
		t.Fatal("no capture reached the agent context at all")
	}
	if seen != mine {
		t.Fatal("the executor replaced the caller's capture; the caller's copy would stay empty and a cancelled turn would lose its partial transcript")
	}
}

// With no capture supplied the executor installs its own, so a surface that
// does not care still gets partial-turn persistence.
func TestExecutorInstallsACaptureWhenTheCallerSuppliesNone(t *testing.T) {
	env := envWithFakeLLM(t, "ok")
	var seen *run.PartialSessionCapture
	svc := turn.New(
		turn.WithSessionStore(env.Deps.SessionStore),
		turn.WithForegroundLocker(env.Foreground),
		turn.WithRunExecutor(env.NewRunExecutor("tui", RunExecutorOptions{
			BeforeAgent: func(agentCtx context.Context, _, _, _ string) error {
				seen = run.PartialSessionCaptureFromContext(agentCtx)
				return nil
			},
		})),
	)
	ensureExecutorSession(t, env, "s-nocapture")
	if _, err := svc.Submit(context.Background(), turn.TurnRequest{SessionID: "s-nocapture", UserText: "hi"}, nil); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if seen == nil {
		t.Fatal("the executor installed no capture, so a cancelled turn would persist nothing")
	}
}

// TestSameTurnRequestProducesTheSameScenarioAcrossSurfaces is P5-14: the same
// TurnRequest, run through the terminal's Core and the gateway's Core, must
// leave the same transcript, runs and steps behind.
//
// This is only meaningful now that both surfaces submit through the canonical
// path (P5-11/P5-12). Before that they assembled run.Options separately, and
// each divergence found since -- the reasoning-only echo the gateway persisted
// and the terminal did not, the attachment references the webchat user row
// dropped, the raw-vs-expanded command in the content column -- was exactly the
// kind of drift two independent implementations produce. This test is what
// makes the next such drift fail rather than ship.
//
// The surfaces differ in their session source and surface label, which is what
// the two Cores below reproduce; everything the transcript records has to come
// out identical regardless.
func TestSameTurnRequestProducesTheSameScenarioAcrossSurfaces(t *testing.T) {
	const reply = "the same answer either way"
	const sessionID = "contract-session"

	runOne := func(sessionSource string, surface turn.Surface, channel string) testutil.ScenarioResult {
		t.Helper()
		env := envWithFakeLLMForSource(t, reply, sessionSource)
		core := turn.New(
			turn.WithSessionStore(env.Deps.SessionStore),
			turn.WithSessionSource(sessionSource),
			turn.WithRunExecutor(env.NewRunExecutor(channel, RunExecutorOptions{PreviewMax: 4096})),
		)
		const userText = "say the same thing"
		// Both surfaces write the user row through the shared helper before
		// submitting, so the contract covers that row too.
		turn.PersistUserTurn(context.Background(), env.Deps.SessionStore, turn.UserTurn{
			SessionID:     sessionID,
			ModelInput:    userText,
			EnsureSession: true,
		})
		out, err := core.Submit(context.Background(), turn.TurnRequest{
			SessionID: sessionID,
			Origin:    turn.Origin{Surface: surface, ChannelID: channel},
			UserText:  userText,
		}, nil)
		if err != nil {
			t.Fatalf("%s Submit: %v", sessionSource, err)
		}
		if out.Status != turn.TurnCompleted {
			t.Fatalf("%s status = %v, want completed", sessionSource, out.Status)
		}
		// Both surfaces write the assistant outcome through the shared helper
		// after Submit returns, so the contract covers those rows too.
		turn.PersistAssistantTurn(context.Background(), env.Deps.SessionStore, turn.AssistantTurn{
			SessionID: sessionID,
			Result:    out.Result,
			Model:     "gpt-test",
		})
		got, err := testutil.CaptureScenario(context.Background(), testutil.ScenarioSource{
			Sessions: env.Deps.SessionStore,
			Runs:     env.Deps.RunRT,
			Actions:  env.Deps.Actions,
		}, sessionID)
		if err != nil {
			t.Fatalf("%s CaptureScenario: %v", sessionSource, err)
		}
		return got
	}

	terminal := runOne("tui", turn.SurfaceTUI, "tui")
	web := runOne("web", turn.SurfaceWebChat, "webchat")

	if len(terminal.Runs) != len(web.Runs) {
		t.Fatalf("run count: terminal=%d web=%d", len(terminal.Runs), len(web.Runs))
	}
	if len(terminal.Runs) != 1 {
		t.Fatalf("expected exactly one run per surface, got %d", len(terminal.Runs))
	}
	if terminal.Runs[0].Status != web.Runs[0].Status {
		t.Fatalf("run status: terminal=%q web=%q", terminal.Runs[0].Status, web.Runs[0].Status)
	}

	// The transcript is the contract that matters: the same roles in the same
	// order, carrying the same content.
	termRoles := messageShape(terminal.Messages)
	webRoles := messageShape(web.Messages)
	if !reflect.DeepEqual(termRoles, webRoles) {
		t.Fatalf("transcript shape differs:\n terminal=%v\n web=%v", termRoles, webRoles)
	}

	if got := eventTypes(terminal.RunEvents); !reflect.DeepEqual(got, eventTypes(web.RunEvents)) {
		t.Fatalf("run event types differ:\n terminal=%v\n web=%v", got, eventTypes(web.RunEvents))
	}
}

// messageShape reduces a transcript to the part two surfaces must agree on:
// role and content, in order. Ids and timestamps are per-run and are not part
// of the contract.
func messageShape(msgs []state.Message) [][2]string {
	out := make([][2]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, [2]string{m.Role, strings.TrimSpace(m.Content)})
	}
	return out
}

func eventTypes(events []event.RunEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Type)
	}
	return out
}
