package tui

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/session"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/stretchr/testify/require"
)

func newSurfaceTestSession(t *testing.T) (*ChatSession, func()) {
	t.Helper()
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.db"))
	require.NoError(t, err)
	sess := sessionEnv{
		Home:      home,
		SQL:       db,
		SessStore: state.NewSessionStore(db, "main"),
	}.session()
	// No Runner: these tests exercise what the surface persists and hands on,
	// not what an agent does with it. run.Run treats a runner-less setup as a
	// no-op turn, so the dispatch still succeeds and only the user's message
	// is written -- which is exactly what they assert. The executor is still
	// wired, because Submit is the path under test.
	env := sess.Env
	env.Control = run.NewController()
	env.Foreground = session.NewLocker()
	sess.tuiControl = env.Control
	sess.Core = turn.New(
		turn.WithSessionStore(sess.sessStore()),
		turn.WithSessionSource(memory.SessionSourceTUI),
		turn.WithRunExecutor(env.NewRunExecutor(memory.SessionSourceTUI, process.RunExecutorOptions{
			PreviewMax: 4096,
			Finish:     sess.tuiFinish,
		})),
	)
	return sess, func() {
		_ = db.Close()
	}
}

func dispatchSurfaceText(ctx context.Context, s *ChatSession, sessionID, text string) error {
	return s.DispatchSurfaceTurn(ctx, turn.TurnSubmission{
		SessionID: sessionID,
		UserText:  text,
	})
}

type scriptedUsageLLM struct {
	result *llm.Result
	err    error
}

func (m scriptedUsageLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	return m.result, m.err
}

func requireUINotify(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for UI notification")
	}
}

func requireUIMessage[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(time.Second):
		var zero T
		t.Fatal("timed out waiting for UI notification")
		return zero
	}
}

func TestDispatchSurfaceTurnPermissionsOpensDialogWithoutTranscriptAppend(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()
	s.Env.Runner = &run.Runner{Deps: &run.Deps{Home: s.home()}}
	opened := make(chan struct{}, 1)
	s.PrependUINotify(func(msg any) {
		if _, ok := msg.(PermissionManagementRequestedMsg); ok {
			opened <- struct{}{}
		}
	})

	err := dispatchSurfaceText(context.Background(), s, "linked-1", "/permissions")
	require.NoError(t, err)
	requireUINotify(t, opened)

	turns, err := s.sessStore().ListRecentMessages(context.Background(), "linked-1", 8)
	require.NoError(t, err)
	require.Len(t, turns, 0)
}

// A subagent's ending must reach the surface exactly once. Its terminal state
// is recorded once — as the canonical run event persisted per session and
// replayed on resume — and the surface turns it into exactly one lifecycle
// notification; a second record of the same transition is what once built two
// closing cards and painted "subagent failed" twice in the subagent's own view.
func TestSubagentEndingReachesTheSurfaceOnce(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()
	const rosterKey = "subagent-5aae6b91"
	const failure = "the provider stopped accepting requests"
	notified := make(chan any, 8)
	s.PrependUINotify(func(msg any) {
		switch msg.(type) {
		case SubagentEndedMsg, RunEndedMsg:
			notified <- msg
		}
	})

	require.NoError(t, s.publishRunEvent(context.Background(), event.NewRunEvent(
		"subagent-lifecycle:run-child:exec-1:ended", "run-child", "linked-1", event.RunEventSubagentEnded,
		event.SubagentEndedPayload{
			AgentID: rosterKey, AgentType: "explore", TaskID: rosterKey,
			Status: "failed", Error: failure, ExecutionID: "exec-1",
		}, time.Now(),
	)))
	// The dispatcher is FIFO, so a second ending would arrive ahead of this
	// sentinel rather than after the test stopped looking.
	s.notifyUI(RunEndedMsg{RunID: "run-child"})

	ended, ok := requireUIMessage(t, notified).(SubagentEndedMsg)
	require.True(t, ok, "the ending must be announced before the sentinel")
	require.Equal(t, rosterKey, ended.AgentID)
	require.Equal(t, failure, ended.Error)
	require.Equal(t, "exec-1", ended.ExecutionID, "the canonical run event is the one that carries execution identity")
	_, sentinel := requireUIMessage(t, notified).(RunEndedMsg)
	require.True(t, sentinel, "a subagent's ending must be announced once")
}

func TestPrepareTUIAgentBaseForwardsReasoningDone(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()
	done := make(chan struct{}, 1)
	s.PrependUINotify(func(msg any) {
		if _, ok := msg.(ReasoningDoneMsg); ok {
			done <- struct{}{}
		}
	})

	ctx, _, cleanupStream := s.prepareTUIAgentBase("sid", context.Background())
	defer cleanupStream()
	sink := llm.StreamSinkFrom(ctx)
	require.NotNil(t, sink)
	require.NotNil(t, sink.OnReasoningDone)
	sink.OnReasoningDone()

	requireUINotify(t, done)
}

func TestPrepareTUIAgentBaseInstallsRuntimeBeforeTrack(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()

	ctx, _, cleanupStream := s.prepareTUIAgentBase("sid", context.Background())
	defer cleanupStream()
	rt := run.TurnInputRuntimeFromContext(ctx)
	require.NotNil(t, rt)

	s.tuiTrack("run-1", "sid", func() {})
	t.Cleanup(func() { s.tuiFinish("run-1") })
	require.True(t, s.SurfaceInputQueue("sid").Steer(run.Input{Parts: []llm.ContentPart{llm.Text("please adjust")}}))

	entries := rt.DrainSteers()
	require.Len(t, entries, 1)
	require.Equal(t, "please adjust", llm.TextContent(entries[0].Parts...))
}

func TestDispatchSurfaceTurnExitRequestsQuit(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()

	quitCh := make(chan QuitRequestedMsg, 1)
	s.PrependUINotify(func(msg any) {
		if q, ok := msg.(QuitRequestedMsg); ok {
			quitCh <- q
		}
	})

	err := dispatchSurfaceText(context.Background(), s, "linked-2", "/exit")
	require.NoError(t, err)
	quit := requireUIMessage(t, quitCh)
	require.Contains(t, quit.Reason, "Closing Forebrain Harness.")

	// R-model: the reply is UI output only — the transcript stays empty.
	turns, err := s.sessStore().ListRecentMessages(context.Background(), "linked-2", 8)
	require.NoError(t, err)
	require.Len(t, turns, 0)
}

func TestDispatchSurfaceTurnNewRequestsSessionSwitch(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()

	switchCh := make(chan SessionSwitchedMsg, 1)
	s.PrependUINotify(func(msg any) {
		if m, ok := msg.(SessionSwitchedMsg); ok {
			switchCh <- m
		}
	})

	err := dispatchSurfaceText(context.Background(), s, "linked-3", "/new")
	require.NoError(t, err)
	switched := requireUIMessage(t, switchCh)
	require.NotEmpty(t, switched.SessionID)
	require.Equal(t, "New Session", switched.Title)
	require.True(t, switched.Select)
}

func TestPrepareTUIAgentBasePreservesWhitespaceOnlyMarkdownDeltas(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()

	msgCh := make(chan Message, 8)
	s.PrependUINotify(func(msg any) {
		if m, ok := msg.(NewMessageMsg); ok {
			msgCh <- m.Msg
		}
	})

	ctx, _, _ := s.prepareTUIAgentBase("sid", context.Background())
	sink := llm.StreamSinkFrom(ctx)
	require.NotNil(t, sink)

	sink.OnDelta("Review: working tree\n")
	sink.OnDelta("\n")
	sink.OnDelta("### P1\n")
	sink.OnDelta("\n- item")

	var parts []string
	deadline := time.After(2 * time.Second)
	for len(parts) < 4 {
		select {
		case msg := <-msgCh:
			if msg.Kind == MsgKindAssistant {
				parts = append(parts, msg.Content)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for assistant deltas, got %q", strings.Join(parts, ""))
		}
	}

	got := strings.Join(parts, "")
	require.Equal(t, "Review: working tree\n\n### P1\n\n- item", got)
}

func TestPrepareTUIAgentBaseInternalUsageWrapperPublishesUsageDeltas(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()

	usageCh := make(chan TokenUsageDeltaMsg, 4)
	s.PrependUINotify(func(msg any) {
		if m, ok := msg.(TokenUsageDeltaMsg); ok {
			usageCh <- m
		}
	})

	ctx, _, _ := s.prepareTUIAgentBase("sid", context.Background())
	// The runner installs a run-scoped accumulator around the main agent LLM
	// call (RunContent). usageAccountingLLM only forwards usage to the TUI sink
	// when an accumulator is present - it suppresses sink reporting for
	// accumulator-less external calls such as the goal-continuation evaluator -
	// so simulate the runner scope here.
	ctx = run.WithInternalLLMUsageAccumulator(ctx, llm.NewUsageAccumulator())
	client := run.WrapUsageAccountingLLMForTest(scriptedUsageLLM{
		result: &llm.Result{
			Message: func() *llm.Message {
				msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("ok")})
				return &msg
			}(),
			Usage: &llm.Usage{InputTokens: 9, OutputTokens: 4},
		},
	})
	_, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	require.NoError(t, err)

	select {
	case got := <-usageCh:
		require.Equal(t, 9, got.InputTokens)
		require.Equal(t, 4, got.OutputTokens)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for usage delta")
	}
}

func TestPrepareTUIAgentBaseUsesAbsoluteUsageSnapshotForTokenBudget(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()

	budgetCh := make(chan TokenBudgetUpdatedMsg, 4)
	s.PrependUINotify(func(msg any) {
		if m, ok := msg.(TokenBudgetUpdatedMsg); ok {
			budgetCh <- m
		}
	})

	ctx, _, cleanupStream := s.prepareTUIAgentBase("sid", context.Background())
	defer cleanupStream()
	sink := llm.StreamSinkFrom(ctx)
	require.NotNil(t, sink)
	require.NotNil(t, sink.OnUsage)
	require.NotNil(t, sink.OnUsageSnapshot)

	// Mirrors opus.log: Anthropic first reports the full cached prompt, then
	// only the newly generated output delta. The latter must not replace the
	// footer with a near-zero token count.
	sink.OnUsage(36412, 1)
	sink.OnUsageSnapshot(36412, 1)
	first := requireUIMessage(t, budgetCh)
	sink.OnUsage(0, 221)
	sink.OnUsageSnapshot(36412, 222)
	second := requireUIMessage(t, budgetCh)

	require.Equal(t, 36413, first.TokenUsage)
	require.Equal(t, 36634, second.TokenUsage)
	require.LessOrEqual(t, second.PercentLeft, first.PercentLeft)
}

// The composer resolves mentions before submitting: a picked file arrives as a
// bare path and a picked image arrives as an attachment. The surface must pass
// that through untouched rather than re-expanding anything.
func TestDispatchSurfaceTurnPassesResolvedMentionsThroughVerbatim(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.txt"), []byte("mention context"), 0o600))
	imgData, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	require.NoError(t, err)
	clip := filepath.Join(root, "clip.png")
	require.NoError(t, os.WriteFile(clip, imgData, 0o600))

	err = s.DispatchSurfaceTurn(context.Background(), turn.TurnSubmission{
		SessionID: "linked-mention-image",
		UserText:  "review notes.txt",
		Attachments: []turn.InputAttachment{
			{Path: clip, MIMEType: "image/png", Label: "clip.png"},
		},
	})
	require.NoError(t, err)

	msgs, err := s.sessStore().ListTranscriptMessages(context.Background(), "linked-mention-image", 8)
	require.NoError(t, err)
	require.NotEmpty(t, msgs)
	require.Equal(t, llm.RoleUser, msgs[0].Role)
	require.NotEmpty(t, msgs[0].Parts)

	// The mentioned path costs nothing but its own characters, and what is
	// stored is byte-identical to what was sent, so the next turn replays this
	// one from a cached prefix instead of a perturbed one.
	require.Equal(t, "review notes.txt\n[Image #1]", msgs[0].Parts[0].Text)
	require.NotContains(t, llm.TextContent(msgs[0].Parts...), "mention context")

	// The image still rides along as an attachment, since no tool reads pixels.
	require.GreaterOrEqual(t, len(msgs[0].Parts), 2)
	require.Contains(t, llm.TextContent(msgs[0].Parts...), "[attachment]")
}

// TestBuildSurfaceUserPartsJSONPersistsCanonicalAttachmentParts moved to
// pkg/turn/input_parts_test.go (P5-2): it exercises turn.BuildSurfaceUserPartsJSON
// directly and has no ChatSession dependency.

func TestHydrateRunUsageLoadsPersistedFailedRunUsage(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()

	s.Env.Deps.RunRT = &state.RunStore{DB: s.sqlDB()}
	require.NoError(t, s.sessStore().Ensure(context.Background(), "sid", "sid"))
	run, err := s.runSvc().CreateRun(context.Background(), "sid", "review")
	require.NoError(t, err)
	require.NoError(t, s.runSvc().SetRunUsage(context.Background(), run.ID, state.LastRunUsage{PromptTokens: 7578, CompletionTokens: 111}))

	usage := runUsageCarrier{runID: run.ID}
	s.hydrateRunUsage(&usage)

	require.Equal(t, 7578, usage.in)
	require.Equal(t, 111, usage.out)
}

func TestDispatchSurfaceTurnBlocksLifecycleSlashDuringRun(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()
	s.tuiTrack("run-1", "sid", func() {})
	defer s.tuiFinish("run-1")

	err := dispatchSurfaceText(context.Background(), s, "linked-running", "/new")
	require.NoError(t, err)

	// R-model: the refusal is UI output only — the transcript stays empty.
	turns, err := s.sessStore().ListRecentMessages(context.Background(), "linked-running", 8)
	require.NoError(t, err)
	require.Len(t, turns, 0)
}

func TestDispatchSurfaceTurnRenamePublishesSessionTitleUpdate(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()
	require.NoError(t, s.sessStore().Ensure(context.Background(), "linked-4", "Old title"))

	switchCh := make(chan SessionSwitchedMsg, 1)
	s.PrependUINotify(func(msg any) {
		if m, ok := msg.(SessionSwitchedMsg); ok {
			switchCh <- m
		}
	})

	err := dispatchSurfaceText(context.Background(), s, "linked-4", "/rename Better title")
	require.NoError(t, err)
	switched := requireUIMessage(t, switchCh)
	require.Equal(t, "linked-4", switched.SessionID)
	require.Equal(t, "Better title", switched.Title)
	require.False(t, switched.Select)
}

func TestDispatchSurfaceTurnModelOpensDialogWithoutTranscriptAppend(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()

	cfg := appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "gpt-5"}}},
	}}}
	require.NoError(t, appcfg.Save(filepath.Join(s.home(), "forebrain.yaml"), cfg))
	*s.cfg() = cfg

	opened := make(chan *turn.Picker, 1)
	s.PrependUINotify(func(msg any) {
		if m, ok := msg.(SlashPickerRequestedMsg); ok {
			opened <- m.Picker
		}
	})

	err := dispatchSurfaceText(context.Background(), s, "linked-model", "/model")
	require.NoError(t, err)
	select {
	case picker := <-opened:
		require.Equal(t, "model", picker.Command)
		require.NotEmpty(t, picker.Items)
	case <-time.After(2 * time.Second):
		t.Fatal("the /model picker was never asked for")
	}

	turns, err := s.sessStore().ListRecentMessages(context.Background(), "linked-model", 8)
	require.NoError(t, err)
	require.Len(t, turns, 0)
}

func TestDispatchSurfaceTurnResumeOpensDialogWithoutTranscriptAppend(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()
	require.NoError(t, s.sessStore().Ensure(context.Background(), "linked-resume", "Resume me"))

	opened := make(chan struct{}, 1)
	s.PrependUINotify(func(msg any) {
		if _, ok := msg.(SessionSelectionRequestedMsg); ok {
			opened <- struct{}{}
		}
	})

	err := dispatchSurfaceText(context.Background(), s, "linked-resume", "/resume")
	require.NoError(t, err)
	requireUINotify(t, opened)

	turns, err := s.sessStore().ListRecentMessages(context.Background(), "linked-resume", 8)
	require.NoError(t, err)
	require.Len(t, turns, 0)
}

func TestDispatchSurfaceTurnSkillsOpensMenuWithoutTranscriptAppend(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()
	opened := make(chan struct{}, 1)
	s.PrependUINotify(func(msg any) {
		if _, ok := msg.(SkillSelectionRequestedMsg); ok {
			opened <- struct{}{}
		}
	})

	err := dispatchSurfaceText(context.Background(), s, "linked-skills", "/skills")
	require.NoError(t, err)
	requireUINotify(t, opened)

	turns, err := s.sessStore().ListRecentMessages(context.Background(), "linked-skills", 8)
	require.NoError(t, err)
	require.Len(t, turns, 0)
}

func TestDispatchSurfaceTurnBangShellPersistsUserShellRecord(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()
	s.Env.Sandbox = safety.NewManager()
	s.refreshSandboxRuntime()

	messages := make(chan Message, 4)
	s.PrependUINotify(func(msg any) {
		if m, ok := msg.(NewMessageMsg); ok && m.Msg.Kind == MsgKindTool {
			messages <- m.Msg
		}
	})

	err := dispatchSurfaceText(context.Background(), s, "linked-shell", "!printf hello")
	require.NoError(t, err)

	var toolMessages []Message
	require.Eventually(t, func() bool {
		for {
			select {
			case msg := <-messages:
				toolMessages = append(toolMessages, msg)
			default:
				return len(toolMessages) >= 3 &&
					toolMessages[len(toolMessages)-1].ToolMeta.Status == "completed"
			}
		}
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, toolMessages[0].StepID, toolMessages[1].StepID)
	require.Equal(t, toolMessages[0].StepID, toolMessages[len(toolMessages)-1].StepID)
	require.NotEmpty(t, toolMessages[0].StepID)
	require.True(t, toolMessages[1].ToolOutputDelta)
	require.Equal(t, "hello", toolMessages[1].Content)
	completed := toolMessages[len(toolMessages)-1]
	require.Equal(t, "completed", completed.ToolMeta.Status)
	require.Greater(t, completed.Duration, time.Duration(0))

	turns, err := s.sessStore().ListRecentMessages(context.Background(), "linked-shell", 8)
	require.NoError(t, err)
	require.Len(t, turns, 2)
	require.Equal(t, "user", turns[0].Role)
	require.Equal(t, "!printf hello", turns[0].Content)
	require.Equal(t, "user", turns[1].Role)
	require.Contains(t, turns[1].Content, "<user_shell_command>")
	require.Contains(t, turns[1].Content, "<command>\nprintf hello\n</command>")
	require.Contains(t, turns[1].Content, "Exit code: 0")
	require.Contains(t, turns[1].Content, "Output:\nhello")
	require.Contains(t, turns[1].ToolMetaJSON, `"tool_name":"shell"`)
	require.Greater(t, turns[1].ExecDurationMs, int64(0))
	require.NotZero(t, turns[1].ExecStartedAtMs)
	require.NotZero(t, turns[1].ExecFinishedAtMs)
}

func TestDispatchSurfaceTurnBangShellHelpDoesNotAppendTranscript(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()

	msgCh := make(chan string, 1)
	s.PrependUINotify(func(msg any) {
		if m, ok := msg.(NewMessageMsg); ok {
			select {
			case msgCh <- m.Msg.Content:
			default:
			}
		}
	})

	err := dispatchSurfaceText(context.Background(), s, "linked-shell-help", "!")
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
	select {
	case msg := <-msgCh:
		t.Fatalf("expected foreground system help to be suppressed, got %q", msg)
	default:
	}

	turns, err := s.sessStore().ListRecentMessages(context.Background(), "linked-shell-help", 8)
	require.NoError(t, err)
	require.Len(t, turns, 0)
}

func TestDispatchSurfaceTurnUserPromptSubmitHookBlocksBeforeTranscriptAppend(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()
	s.Env.Deps.AppCfg = ptrTo(appcfg.Root{
		Hooks: appcfg.HooksSettings{
			"UserPromptSubmit": {{
				Hooks: []appcfg.HookCommand{{
					Type:    appcfg.HookTypeCommand,
					Command: "echo '{\"continue\":false,\"stopReason\":\"blocked in tui\"}'",
				}},
			}},
		},
	})
	s.userHooks = &hook.Runtime{
		Home:          s.home(),
		WorkspaceRoot: s.StateRoot(),
		Cfg:           s.cfg(),
		Sess:          s.sessStore(),
	}

	msgCh := make(chan string, 1)
	s.PrependUINotify(func(msg any) {
		if m, ok := msg.(NewMessageMsg); ok {
			select {
			case msgCh <- m.Msg.Content:
			default:
			}
		}
	})

	err := dispatchSurfaceText(context.Background(), s, "linked-hook", "please run")
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
	select {
	case msg := <-msgCh:
		t.Fatalf("expected blocked hook foreground system message to be suppressed, got %q", msg)
	default:
	}

	turns, err := s.sessStore().ListRecentMessages(context.Background(), "linked-hook", 8)
	require.NoError(t, err)
	require.Len(t, turns, 0)
	// The transcript replays this agent's conversation, so it belongs to the
	// agent workspace; the shared home must stay empty of it.
	_, statErr := os.Stat(filepath.Join(s.userHooks.StateRoot(), "state", "hook-transcripts", "linked-hook.jsonl"))
	require.NoError(t, statErr)
	require.NoDirExists(t, filepath.Join(s.home(), "state", "hook-transcripts"))
}

func TestDispatchSurfaceTurnInitInjectsPromptNotRawSlash(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	defer func() { _ = os.Chdir(orig) }()

	s.Env.Runner = &run.Runner{Deps: &run.Deps{Home: s.home()}}
	s.Env.Hooks = hook.NewAgentPipeline()

	_ = s.DispatchSurfaceTurn(context.Background(), turn.TurnSubmission{
		SessionID: "init-test",
		UserText:  "/init",
	})

	turns, err := s.sessStore().ListRecentMessages(context.Background(), "init-test", 8)
	require.NoError(t, err)
	require.NotEmpty(t, turns, "expected at least one turn")
	require.Equal(t, "user", turns[0].Role)
	// content is display-only for user turns and is what resume replay shows, so
	// it must be the raw command the user typed.
	require.Equal(t, "/init", turns[0].Content, "resume replay must show the raw /init command the user typed")
	// The model context is reconstructed from PartsJSON, which must carry the
	// built prompt (not the raw command) so /init still injects its instructions.
	require.NotContains(t, turns[0].PartsJSON, `"/init"`, "PartsJSON must not carry the raw command as the model input")
	require.Contains(t, turns[0].PartsJSON, "FOREBRAIN.md", "PartsJSON must carry the built prompt for the model")
}

// TestDispatchSurfaceTurnDisplayTextNeverOverridesModelInput locks the
// tui-path fix: when the TUI feeds a prompt-inject result as
// UserText=<built prompt> with DisplayText="/init", the DisplayText must
// NOT override UserText as the model input.
func TestDispatchSurfaceTurnDisplayTextNeverOverridesModelInput(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	defer func() { _ = os.Chdir(orig) }()

	s.Env.Runner = &run.Runner{Deps: &run.Deps{Home: s.home()}}
	s.Env.Hooks = hook.NewAgentPipeline()

	// Simulate the tui path: UserText is the already-injected prompt,
	// DisplayText is the original "/init" shown in the TUI.
	_ = s.DispatchSurfaceTurn(context.Background(), turn.TurnSubmission{
		SessionID:   "init-tui",
		UserText:    "Please analyze this codebase and create a FOREBRAIN.md",
		DisplayText: "/init",
	})

	turns, err := s.sessStore().ListRecentMessages(context.Background(), "init-tui", 8)
	require.NoError(t, err)
	require.NotEmpty(t, turns, "expected at least one turn")
	require.Equal(t, "user", turns[0].Role)
	require.NotEqual(t, "/init", turns[0].Content, "DisplayText must not override UserText as model input")
	require.Equal(t, "Please analyze this codebase and create a FOREBRAIN.md", turns[0].Content)
}

// TestDispatchSurfaceTurnRawInputPersistsRawCommand locks the resume-replay
// fix: when the tui/TUI path expands a slash command up front, it passes
// UserText=<built prompt> and RawInput="/init". The persisted content (what
// resume replay shows) must be the raw command the user typed, while PartsJSON
// (the model context) must carry the built prompt.
func TestClearSurfaceSessionPreservesReplayResetsModelContextAndFooter(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()
	ctx := context.Background()
	sid := "clear-preserve-replay"
	require.NoError(t, s.sessStore().Ensure(ctx, sid, "Keep this title"))
	_, err := s.sessStore().AppendStructuredMessage(ctx, sid, "user", "old question", "", state.MessagePartsJSON(llm.UserMessage(llm.Text("old question")), "old question"), "", "", "", "", state.MessageExecTiming{})
	require.NoError(t, err)
	_, err = s.sessStore().AppendStructuredMessage(ctx, sid, "assistant", "old answer", "", state.MessagePartsJSON(llm.AssistantMessage([]llm.ContentPart{llm.Text("old answer")}), "old answer"), "model", "", "", "", state.MessageExecTiming{})
	require.NoError(t, err)
	// The first user turn auto-generates a title; explicitly rename afterwards
	// so the test verifies clear preserves the established session title.
	require.NoError(t, s.sessStore().SetTitle(ctx, sid, "Keep this title"))

	budgetCh := make(chan TokenBudgetUpdatedMsg, 1)
	s.PrependUINotify(func(msg any) {
		if budget, ok := msg.(TokenBudgetUpdatedMsg); ok {
			budgetCh <- budget
		}
	})
	require.NoError(t, s.ClearSurfaceSession(ctx, sid))

	active, err := s.SurfaceActiveContextMessages(ctx, sid)
	require.NoError(t, err)
	require.Empty(t, active, "model context should be empty immediately after clear")
	full, err := s.SurfaceTranscriptMessages(ctx, sid)
	require.NoError(t, err)
	require.Len(t, full, 2, "resume replay must preserve every pre-clear turn")
	require.Equal(t, "old question", full[0].Content)
	require.Equal(t, "old answer", full[1].Content)

	resumedID, title, warning, err := s.ResumeSession(ctx, sid)
	require.NoError(t, err, "/resume and --resume lookup must still work")
	require.Empty(t, warning, "a resumed session with a configured model has no fallback warning")
	require.Equal(t, sid, resumedID)
	require.Equal(t, "Keep this title", title)

	select {
	case budget := <-budgetCh:
		require.Equal(t, 0, budget.TokenUsage)
		require.Equal(t, 100, budget.PercentLeft)
		require.Greater(t, budget.ContextWindow, 0)
	case <-time.After(time.Second):
		t.Fatal("clear did not publish token budget reset")
	}
}

func TestSurfaceTranscriptMessagesOmitsRuntimeGeneratedUserMessages(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()

	ctx := context.Background()
	sid := "runtime-meta-replay"
	realUser := llm.UserMessage(llm.Text("original user message"))
	_, err := s.sessStore().AppendStructuredMessage(ctx, sid, "user", realUser.TextContent(), "", state.MessagePartsJSON(realUser, realUser.TextContent()), "", "", "", "", state.MessageExecTiming{})
	require.NoError(t, err)

	runtimeUser := llm.UserMessage(llm.Text("<forebrain_environment_context>\nruntime only\n</forebrain_environment_context>"))
	runtimeUser.IsMeta = true
	_, err = s.sessStore().AppendStructuredMessage(ctx, sid, "user", runtimeUser.TextContent(), "", state.MessagePartsJSON(runtimeUser, runtimeUser.TextContent()), "", "", "", "", state.MessageExecTiming{})
	require.NoError(t, err)

	// A genuine user message is identified structurally, not by matching the
	// runtime tag text, so user-authored lookalike content remains visible.
	lookalike := llm.UserMessage(llm.Text("<forebrain_environment_context>user-authored</forebrain_environment_context>"))
	_, err = s.sessStore().AppendStructuredMessage(ctx, sid, "user", lookalike.TextContent(), "", state.MessagePartsJSON(lookalike, lookalike.TextContent()), "", "", "", "", state.MessageExecTiming{})
	require.NoError(t, err)
	assistant := llm.AssistantMessage([]llm.ContentPart{llm.Text("assistant reply")})
	_, err = s.sessStore().AppendStructuredMessage(ctx, sid, "assistant", assistant.TextContent(), "", state.MessagePartsJSON(assistant, assistant.TextContent()), "", "", "", "", state.MessageExecTiming{})
	require.NoError(t, err)

	modelTurns, err := s.sessStore().ListAllMessages(ctx, sid, 0)
	require.NoError(t, err)
	require.Len(t, modelTurns, 4, "runtime meta message must remain available to model context")

	replayTurns, err := s.SurfaceTranscriptMessages(ctx, sid)
	require.NoError(t, err)
	require.Len(t, replayTurns, 3)
	require.Equal(t, "original user message", replayTurns[0].Content)
	require.Equal(t, lookalike.TextContent(), replayTurns[1].Content)
	require.Equal(t, "assistant reply", replayTurns[2].Content)
}

// The context-clear anchor persists a copy of the gated assistant row for the
// model's sake — the provider needs the tool_call its replayed result answers —
// but no surface ever drew that copy: the live run rendered the pre-clear row
// once and the resume continued silently. The replay must paint the same
// picture: the anchor and its assistant copy stay out of the visual timeline.
func TestSurfaceTranscriptMessagesOmitsTheContextClearAnchorCopy(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()

	ctx := context.Background()
	sid := "cleared-anchor-replay"
	call := llm.ToolCall{ID: "call-exit-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "exit_plan_mode", Arguments: `{}`}}
	append := func(msg llm.Message, role string) {
		t.Helper()
		_, err := s.sessStore().AppendStructuredMessage(ctx, sid, role, msg.TextContent(), "", state.MessagePartsJSON(msg, msg.TextContent()), "", "", "", "", state.MessageExecTiming{})
		require.NoError(t, err)
	}
	append(llm.UserMessage(llm.Text("plan something")), "user")
	gated := llm.AssistantMessage([]llm.ContentPart{llm.Text("calling exit_plan_mode")}, call)
	append(gated, "assistant")

	anchor := llm.UserMessage(llm.Text("Context cleared by user. Proceed with the approved action."))
	anchor.IsMeta = true
	append(anchor, "user")
	gatedCopy := llm.AssistantMessage([]llm.ContentPart{llm.Text("calling exit_plan_mode")}, call)
	gatedCopy.IsMeta = true
	append(gatedCopy, "assistant")

	result := `{"message":"Exited plan mode. You can now make edits.","mode":"agent","plan_file":"/state/plans/proj/plan.md"}`
	append(llm.ToolResultMessage("call-exit-1", llm.Text(result)), "tool")
	append(llm.AssistantMessage([]llm.ContentPart{llm.Text("done")}), "assistant")

	modelTurns, err := s.sessStore().ListAllMessages(ctx, sid, 0)
	require.NoError(t, err)
	require.Len(t, modelTurns, 6, "the anchor pair must remain in the model transcript")

	replayTurns, err := s.SurfaceTranscriptMessages(ctx, sid)
	require.NoError(t, err)
	require.Len(t, replayTurns, 4, "the anchor and its assistant copy never rendered live")
	require.Equal(t, "calling exit_plan_mode", replayTurns[1].Content)

	// What the resume paints from those rows is the 1:1 property itself: the
	// gated assistant text appears exactly as many times as the live run
	// showed it — once.
	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)
	replayTurnsToRenderer(renderer, replayTurns)
	painted := stripANSI(strings.Join(renderViewport(&renderer.vm, 120, 30, 0, DiffThemeDark).lines, "\n"))
	if n := strings.Count(painted, "calling exit_plan_mode"); n != 1 {
		t.Fatalf("the gated assistant text painted %d times, want once (live rendered it once):\n%s", n, painted)
	}
}

func TestDispatchSurfaceTurnRawInputPersistsRawCommand(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()

	s.Env.Runner = &run.Runner{Deps: &run.Deps{Home: s.home()}}
	s.Env.Hooks = hook.NewAgentPipeline()

	// Simulate the tui path after slash expansion: UserText is the
	// built prompt fed to the model, RawInput is the "/init" the user typed.
	_ = s.DispatchSurfaceTurn(context.Background(), turn.TurnSubmission{
		SessionID: "raw-input-tui",
		UserText:  "Please analyze this codebase and create a FOREBRAIN.md",
		RawInput:  "/init",
	})

	turns, err := s.sessStore().ListRecentMessages(context.Background(), "raw-input-tui", 8)
	require.NoError(t, err)
	require.NotEmpty(t, turns, "expected at least one turn")
	require.Equal(t, "user", turns[0].Role)
	// content is display-only for user turns and is what resume replay shows, so
	// it must be the raw command the user typed, not the expanded prompt.
	require.Equal(t, "/init", turns[0].Content, "resume replay must show the raw /init command the user typed")
	// PartsJSON is the model context and must carry the built prompt.
	require.Contains(t, turns[0].PartsJSON, "FOREBRAIN.md", "PartsJSON must carry the built prompt for the model")
	require.NotContains(t, turns[0].PartsJSON, `"/init"`, "PartsJSON must not carry the raw command as the model input")
}

func TestCapturedAssistantTextForCurrentTurn(t *testing.T) {
	t.Run("empty snapshot", func(t *testing.T) {
		require.Equal(t, "", turn.CapturedAssistantTextForTurn(nil))
		require.Equal(t, "", turn.CapturedAssistantTextForTurn([]llm.Message{}))
	})
	t.Run("no user message yields no window", func(t *testing.T) {
		msgs := []llm.Message{llm.AssistantMessage([]llm.ContentPart{llm.Text("hello")})}
		require.Equal(t, "", turn.CapturedAssistantTextForTurn(msgs))
	})
	t.Run("collects only text after the last user message", func(t *testing.T) {
		msgs := []llm.Message{
			llm.AssistantMessage([]llm.ContentPart{llm.Text("previous turn")}),
			llm.UserMessage(llm.Text("user input")),
			assistantWithToolCall("first response\n\n", "tc1"),
			llm.ToolResultMessage("tc1", llm.Text("result")),
			assistantWithToolCall("second response\n\n", "tc2"),
			llm.ToolResultMessage("tc2", llm.Text("result")),
		}
		require.Equal(t, "first response\n\nsecond response", turn.CapturedAssistantTextForTurn(msgs))
	})
	t.Run("meta user rows do not close the window", func(t *testing.T) {
		env := llm.UserMessage(llm.Text("<forebrain_environment_context>"))
		env.IsMeta = true
		msgs := []llm.Message{
			llm.UserMessage(llm.Text("real user")),
			env,
			assistantWithToolCall("response text", "tc1"),
		}
		require.Equal(t, "response text", turn.CapturedAssistantTextForTurn(msgs))
	})
}

// TestPersistCancelledTurnDropsFullyCapturedBuffer reproduces the resume-replay
// duplication: a buffer that spans the whole turn repeats the assistant text the
// partial capture already carries, so it must not be persisted a second time.
func TestPersistCancelledTurnDropsFullyCapturedBuffer(t *testing.T) {
	captured := []llm.Message{
		llm.UserMessage(llm.Text("user input")),
		assistantWithToolCall("openpyxl 缺失，先安装：\n\n", "tc1"),
		llm.ToolResultMessage("tc1", llm.Text("ok")),
		assistantWithToolCall("Switching to zipfile.\n\n", "tc2"),
		llm.ToolResultMessage("tc2", llm.Text("ok")),
	}
	buffered := strings.TrimSpace("openpyxl 缺失，先安装：\n\nSwitching to zipfile.\n\n")
	require.Equal(t, turn.CapturedAssistantTextForTurn(captured), buffered,
		"a whole-turn buffer must be recognised as already captured")
}

// TestPersistCancelledTurnKeepsInterruptedText covers the normal path: the
// orchestration loop cleared the buffer at the last response boundary, so what
// remains is text from the interrupted call that nothing else persists.
func TestPersistCancelledTurnKeepsInterruptedText(t *testing.T) {
	captured := []llm.Message{
		llm.UserMessage(llm.Text("user input")),
		assistantWithToolCall("first\n\n", "tc1"),
		llm.ToolResultMessage("tc1", llm.Text("ok")),
	}
	buffered := "partial text from the interrupted call"
	require.NotEqual(t, turn.CapturedAssistantTextForTurn(captured), buffered,
		"genuinely new interrupted text must survive and be persisted")
}

func assistantWithToolCall(text string, callID string) llm.Message {
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(text)})
	msg.ToolCalls = []llm.ToolCall{{ID: callID, Function: llm.FunctionCall{Name: "shell"}}}
	return msg
}

// A tool call an approval stands in front of reports several boundaries under
// one step id: the execution that ran before the gate and is kept as history,
// the gate itself, and the completion of the replay the granted approval
// starts. Each is its own event and must reach the surface carrying its own
// payload. While they all claimed "tool:<run>:<step>:<kind>", the session event
// log — idempotent by id — kept the first and handed its payload back for the
// two that followed, so the subagent's view painted the superseded sandbox
// attempt three times and never showed what the command actually returned.
func TestApprovalGatedToolBoundariesKeepTheirOwnIdentity(t *testing.T) {
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	runs := &state.RunStore{DB: db}
	s := sessionEnv{Home: home, SQL: db, RunSvc: runs}.session()
	ctx := context.Background()
	require.NoError(t, state.NewSessionStore(db, "main").Ensure(ctx, "linked-1", "linked-1"))
	child, err := runs.CreateRun(ctx, "linked-1", "child")
	require.NoError(t, err)
	const (
		rosterKey = "subagent-b74a2164"
		stepID    = "call_yABz2DZUBq6P9yIjT6AfW4Ho"
		actionID  = "6190cdc6-4716-4920-b3fa-d09594ec9c57"
	)
	completed := make(chan NewMessageMsg, 8)
	s.PrependUINotify(func(msg any) {
		if m, ok := msg.(NewMessageMsg); ok && m.Msg.ToolPhase == event.RunEventToolCompleted {
			completed <- m
		}
	})

	agentCtx := tool.WithHookAgentID(context.Background(), rosterKey)
	ids := map[string]struct{}{}
	publish := func(ctx context.Context, evt tool.StepEvent) {
		canonical, ok := tool.RunEventFromStep(ctx, "linked-1", child.ID, "tui", evt)
		require.True(t, ok, "a shell boundary must project into a run event")
		ids[canonical.ID] = struct{}{}
		require.NoError(t, s.publishRunEvent(context.Background(), canonical))
	}
	boundary := tool.StepEvent{
		Kind: event.RunEventToolCompleted, StepID: stepID, ToolName: "shell",
		Input: map[string]any{"command": "rg -n security src"},
	}

	attempt := boundary
	attempt.Output = map[string]any{"stdout": "the sandboxed attempt", "exit_code": 2, "sandbox_denied": true}
	attempt.RetainAsHistory = true
	attempt.Attempt = 1
	publish(agentCtx, attempt)

	gate := boundary
	gate.Output = map[string]any{"requires_action": true, "sandbox_retry": true}
	gate.ActionID = actionID
	gate.ActionKind = "shell"
	publish(agentCtx, gate)

	// The replay runs under the approval that authorised it, which is what
	// names its boundary apart from the gate's.
	result := boundary
	result.Output = map[string]any{"stdout": "what the command really returned", "exit_code": 0}
	publish(tool.WithApprovedActionID(agentCtx, actionID), result)

	require.Len(t, ids, 3, "each boundary of one call needs an event id of its own")

	first := requireUIMessage(t, completed).Msg
	require.True(t, first.RetainAsHistory, "the superseded attempt is the one kept as history")
	require.Contains(t, first.Content, "the sandboxed attempt")

	second := requireUIMessage(t, completed).Msg
	require.False(t, second.RetainAsHistory)
	require.NotContains(t, second.Content, "the sandboxed attempt", "the gate must not be echoed as the attempt")

	third := requireUIMessage(t, completed).Msg
	require.False(t, third.RetainAsHistory, "the real result must replace the card, not append beside it")
	require.Contains(t, third.Content, "what the command really returned")

	page, err := runs.ListSessionEvents(context.Background(), "linked-1", 0, 0, 16)
	require.NoError(t, err)
	require.Len(t, page.Events, 3, "every boundary must be recorded, so a resumed session replays the result")
}

// A tool call streams its output while it is still executing, and the delta
// that carries a chunk is a boundary of a running call. The subagent's view
// reached it through the canonical event, which serialized only the text — so
// the card it painted knew neither that the call was running nor which command
// it was running, and the header read "Ran" over output that was still
// arriving. The primary conversation, whose hook stamped the status itself,
// read "Running" for the same call.
func TestSubagentToolOutputDeltaReadsAsRunning(t *testing.T) {
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	runs := &state.RunStore{DB: db}
	s := sessionEnv{Home: home, SQL: db, RunSvc: runs}.session()
	ctx := context.Background()
	require.NoError(t, state.NewSessionStore(db, "main").Ensure(ctx, "linked-1", "linked-1"))
	child, err := runs.CreateRun(ctx, "linked-1", "child")
	require.NoError(t, err)
	const rosterKey = "subagent-b74a2164"

	deltas := make(chan NewMessageMsg, 8)
	s.PrependUINotify(func(msg any) {
		if m, ok := msg.(NewMessageMsg); ok && m.Msg.ToolOutputDelta {
			deltas <- m
		}
	})

	agentCtx := tool.WithHookAgentID(context.Background(), rosterKey)
	canonical, ok := tool.RunEventFromStep(agentCtx, "linked-1", child.ID, "tui", tool.StepEvent{
		Kind: event.RunEventToolOutputDelta, StepID: "call_stream", ToolName: "shell",
		Input:  map[string]any{"command": "node scripts/build-all.mjs"},
		Output: map[string]any{"chunk": "[build-all] tsdown\n"},
	})
	require.True(t, ok, "a streaming shell boundary must project into a run event")
	require.NoError(t, s.publishRunEvent(context.Background(), canonical))

	var got NewMessageMsg
	select {
	case got = <-deltas:
	case <-time.After(2 * time.Second):
		t.Fatal("the streaming boundary never reached the surface")
	}
	require.Equal(t, rosterKey, strings.TrimSpace(got.Msg.AgentID))

	var r Reducer
	frames := r.Reduce(got).Frames
	require.Len(t, frames, 1)
	require.Equal(t, "Running node scripts/build-all.mjs", ToolDisplayHeader(frames[0], home))
}

// Approval recovery is not silent bookkeeping: it fails the run it recovers and
// tells the user an approval of theirs was interrupted. The durable outbox it
// drains is shared by every conversation on the machine, so draining all of it
// through whichever surface happened to attach opened a brand-new session with
// two messages from an older one — and, for a continuation that was resumable
// rather than uncertain, would have replayed that conversation's tool call into
// it. The conversation the surface has open is the boundary.
func TestApprovalRecoveryReportsOnlyTheOpenConversation(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	runs := &state.RunStore{DB: db}
	actions := &state.ActionService{DB: db}
	require.NoError(t, state.NewSessionStore(db, "main").Ensure(ctx, "cli-earlier", "cli-earlier"))
	s := sessionEnv{Home: home, SQL: db, RunSvc: runs, ActionSvc: actions}.session()

	// An earlier conversation whose subagent was left waiting at a gate nobody
	// answered before the process it ran in went away.
	prior, err := runs.CreateRun(ctx, "cli-earlier", "review the diff")
	require.NoError(t, err)
	child, err := runs.CreateSubagentRun(ctx, prior.ID, "cli-earlier", "verification")
	require.NoError(t, err)
	action, err := actions.CreatePending(ctx, "cli-earlier", "shell", map[string]any{"session_id": "cli-earlier"})
	require.NoError(t, err)
	require.NoError(t, runs.SetWaitingAction(ctx, child.ID, state.Wait{
		RunID: child.ID, ActionID: action.ID, ToolName: "shell", ToolInputJSON: "{}",
		AgentID: "subagent-ced9a279", SubagentType: "verification",
	}))
	expired, err := actions.ExpireIfPending(ctx, action.ID, "the process that asked is gone")
	require.NoError(t, err)
	require.True(t, expired)

	failures := make(chan NewMessageMsg, 4)
	s.PrependUINotify(func(msg any) {
		if m, ok := msg.(NewMessageMsg); ok && m.Msg.Kind == MsgKindError {
			failures <- m
		}
	})

	s.StartApprovalRecovery("cli-fresh")
	select {
	case m := <-failures:
		t.Fatalf("a new session was told about another conversation's approval: %q", m.Msg.Content)
	case <-time.After(300 * time.Millisecond):
	}
	parked, err := runs.GetRun(ctx, child.ID)
	require.NoError(t, err)
	require.NotEqual(t, state.RunStatusFailed, parked.Status, "another conversation's run is not this surface's to end")

	// Opening the conversation that owns it is what reports it, in the place it
	// belongs.
	s.StartApprovalRecovery("cli-earlier")
	reported := requireUIMessage(t, failures)
	require.Contains(t, reported.Msg.Content, "the process that asked is gone")
	recovered, err := runs.GetRun(ctx, child.ID)
	require.NoError(t, err)
	require.Equal(t, state.RunStatusFailed, recovered.Status)
}
