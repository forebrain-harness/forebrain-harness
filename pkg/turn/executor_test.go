package turn

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/stretchr/testify/require"
)

type contextRecordingCompactHandler struct {
	err    error
	called bool
}

func (h *contextRecordingCompactHandler) HandleCompactSlash(ctx context.Context, _, _ string, _ []string) (string, bool) {
	h.called = true
	h.err = ctx.Err()
	return "", true
}

func TestCompactIsRejectedWhileTurnIsRunning(t *testing.T) {
	handler := &contextRecordingCompactHandler{}
	result := Execute(Context{
		CommandContext: context.Background(),
		SessionID:      "s1",
		Channel:        "tui",
		Surface:        SurfaceTUI,
		DuringRun:      true,
		Compact:        handler,
	}, "/compact")

	if !result.Handled || result.Reply != "/compact is unavailable while a task is running." {
		t.Fatalf("unexpected result: %+v", result)
	}
	if handler.called {
		t.Fatal("compact handler ran concurrently with an active turn")
	}
}

func TestCompactUsesSurfaceCommandContext(t *testing.T) {
	commandCtx, cancel := context.WithCancel(context.Background())
	cancel()
	handler := &contextRecordingCompactHandler{}

	result := Execute(Context{
		CommandContext: commandCtx,
		SessionID:      "s1",
		Channel:        "tui",
		Surface:        SurfaceTUI,
		Compact:        handler,
	}, "/compact")

	if !result.Handled {
		t.Fatal("expected /compact to be handled")
	}
	if handler.err != context.Canceled {
		t.Fatalf("compact context error=%v want %v", handler.err, context.Canceled)
	}
}

type fastSlashStub struct {
	reply   string
	handled bool
}

func (f fastSlashStub) HandleFastSlash(sessionID, channel string, args []string) (string, bool) {
	return f.reply, f.handled
}

func TestExecFastRejectsWhenFastAvailableFalse(t *testing.T) {
	res := Execute(Context{
		Surface:       SurfaceTUI,
		FastAvailable: false,
		Fast:          fastSlashStub{reply: "fast: on", handled: true},
	}, "/fast")
	require.True(t, res.Handled)
	require.Equal(t, "fast: unavailable for current model", res.Reply)
}

func TestExecFastRejectsWhenHandlerNil(t *testing.T) {
	res := Execute(Context{
		Surface:       SurfaceTUI,
		FastAvailable: true,
		Fast:          nil,
	}, "/fast")
	require.True(t, res.Handled)
	require.Equal(t, "fast: unavailable (no fast handler wired)", res.Reply)
}

func TestExecFastDelegatesToFastSlashHandler(t *testing.T) {
	res := Execute(Context{
		Surface:       SurfaceTUI,
		FastAvailable: true,
		Fast:          fastSlashStub{reply: "fast: on (Anthropic priority service tier)", handled: true},
	}, "/fast")
	require.True(t, res.Handled)
	require.Equal(t, "fast: on (Anthropic priority service tier)", res.Reply)
}

func TestExecFastFallbackWhenHandlerReturnsUnhandled(t *testing.T) {
	res := Execute(Context{
		Surface:       SurfaceTUI,
		FastAvailable: true,
		Fast:          fastSlashStub{reply: "", handled: false},
	}, "/fast")
	require.True(t, res.Handled)
	require.Equal(t, "fast: unavailable", res.Reply)
}

func TestExecGoalWithObjective(t *testing.T) {
	res := Execute(Context{Surface: SurfaceTUI}, "/goal ship the parser")
	require.True(t, res.ShouldContinueRun, "ShouldContinueRun=false, want true")
	require.Equal(t, "ship the parser", res.GoalObjective)
	require.True(t, strings.Contains(res.ContinueInput, "ship the parser"), "ContinueInput missing objective: %q", res.ContinueInput)
}

func TestExecGoalWithoutObjectiveIsUsageHint(t *testing.T) {
	res := Execute(Context{Surface: SurfaceTUI}, "/goal")
	require.False(t, res.ShouldContinueRun, "ShouldContinueRun=true, want false for empty objective")
	require.True(t, res.Handled, "expected handled")
	require.NotEmpty(t, res.Reply, "expected usage hint reply")
	require.Empty(t, res.GoalObjective)
}

func TestExecInitReturnsInjectPrompt(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	defer func() { _ = os.Chdir(orig) }()

	res := Execute(Context{Surface: SurfaceTUI}, "/init")
	require.False(t, res.Handled, "/init should not be handled inline")
	require.True(t, res.ShouldContinueRun, "/init should request continuation")
	require.NotEmpty(t, res.ContinueInput, "ContinueInput must contain the built prompt")
	require.Contains(t, res.ContinueInput, "FOREBRAIN.md")
	require.Contains(t, res.ContinueInput, "write_file")
}

func TestExecInitRejectsInlineArgs(t *testing.T) {
	// /init has SupportsInlineArgs=false, so the executor rejects "foo"
	// before reaching execInit.
	res := Execute(Context{Surface: SurfaceTUI}, "/init foo")
	require.True(t, res.Handled)
	require.Contains(t, res.Reply, "does not accept inline arguments")
}

// /sandbox shows one report and takes no arguments, so a leftover `doctor` or a
// sandbox mode is rejected here rather than reaching the handler.
func TestExecSandboxRejectsInlineArgs(t *testing.T) {
	for _, line := range []string{"/sandbox doctor", "/sandbox danger-full-access", "/sandbox status"} {
		res := Execute(Context{Surface: SurfaceTUI}, line)
		require.True(t, res.Handled, line)
		require.Contains(t, res.Reply, "does not accept inline arguments", line)
	}
}

func TestBuildInitPromptNewFile(t *testing.T) {
	p := buildInitPrompt("/repo/FOREBRAIN.md", false)
	require.True(t, strings.Contains(p, "create a FOREBRAIN.md"))
	require.True(t, strings.Contains(p, "What to add"))
	require.True(t, strings.Contains(p, "Usage notes"))
	require.True(t, strings.Contains(p, "write_file"))
}

func TestBuildInitPromptExistingFile(t *testing.T) {
	p := buildInitPrompt("/repo/FOREBRAIN.md", true)
	require.True(t, strings.Contains(p, "improve the existing FOREBRAIN.md"))
	require.True(t, strings.Contains(p, "First read the current FOREBRAIN.md"))
}

// fakeNewSessionStore records the id/title pairs passed to Ensure.
type fakeNewSessionStore struct {
	ensuredID       string
	ensuredTitle    string
	copiedModel     bool
	copiedModelFrom string
	copiedModelTo   string
}

func (f *fakeNewSessionStore) Ensure(ctx context.Context, id string, title string) error {
	f.ensuredID = id
	f.ensuredTitle = title
	return nil
}

func (f *fakeNewSessionStore) ListSessionsRecent(ctx context.Context, limit int) ([]state.SessionSummary, error) {
	return nil, nil
}

func (f *fakeNewSessionStore) ForkInto(ctx context.Context, sourceID, targetID string) error {
	return nil
}

func (f *fakeNewSessionStore) ListChildSessionsRecent(ctx context.Context, parentSessionID string, limit int) ([]state.SessionSummary, error) {
	return nil, nil
}

func (f *fakeNewSessionStore) SetTitle(ctx context.Context, id string, title string) error {
	return nil
}

func (f *fakeNewSessionStore) SetParentSessionID(ctx context.Context, id string, parentSessionID string) error {
	return nil
}

func (f *fakeNewSessionStore) CopySessionModelSelection(ctx context.Context, source, target string) (bool, error) {
	f.copiedModelFrom = source
	f.copiedModelTo = target
	return f.copiedModel, nil
}

// TestExecNewPersistsSessionIDAsTitleSentinel guards against regressing to a
// hardcoded placeholder title (e.g. "New Session") on session creation. Every
// other creation path (AppendMessageWithSource, execResume) stores title==id to
// mean "no title yet"; lookupSessionTitle and sessionTitleForTurn rely on
// that sentinel to let the first turn's message-derived title take effect
// during the animated terminal title. Persisting a literal placeholder
// instead would make that stored value look like a real, finalized title and
// mask the correct title for the entire first turn.
func TestExecNewPersistsSessionIDAsTitleSentinel(t *testing.T) {
	store := &fakeNewSessionStore{}
	res := execNew(Context{Surface: SurfaceTUI, Sessions: store})
	if !res.Handled || !res.SessionChanged {
		t.Fatalf("expected handled session-change result, got %+v", res)
	}
	if store.ensuredID == "" {
		t.Fatalf("expected Ensure to be called with a session id")
	}
	if store.ensuredTitle != store.ensuredID {
		t.Fatalf("expected stored title to equal the session id sentinel, got id=%q title=%q", store.ensuredID, store.ensuredTitle)
	}
}

// pickerSessions is a session store with a few conversations and one child.
type pickerSessions struct{ fakeNewSessionStore }

func (pickerSessions) ListSessionsRecent(context.Context, int) ([]state.SessionSummary, error) {
	return []state.SessionSummary{{ID: "here", Title: "this one"}, {ID: "older", Title: "an older chat"}, {ID: "blank", Title: "blank"}}, nil
}

func (pickerSessions) ListChildSessionsRecent(context.Context, string, int) ([]state.SessionSummary, error) {
	return []state.SessionSummary{{ID: "child-1", Title: "explore the repo"}}, nil
}

// On the web, /resume and /subagents offer conversations as pickers — never
// the one the command was typed in — and a pick moves there.
func TestResumeAndSubagentsOfferConversations(t *testing.T) {
	ctx := Context{Surface: SurfaceWebChat, SessionID: "here", Sessions: &pickerSessions{}}
	resume := Execute(ctx, "/resume")
	require.NotNil(t, resume.Picker)
	var ids []string
	for _, item := range resume.Picker.Items {
		ids = append(ids, item.Value)
	}
	require.Equal(t, []string{"older", "blank"}, ids)
	require.Equal(t, "New conversation", resume.Picker.Items[1].Label, "a session titled by its id is untitled")

	sub := Execute(ctx, "/subagents")
	require.NotNil(t, sub.Picker)
	require.Equal(t, "explore the repo", sub.Picker.Items[0].Label)

	moved := Choose(ctx, SlashChoice{Command: "subagents", Value: "child-1"})
	require.True(t, moved.SessionSwitched)
	require.Equal(t, "child-1", moved.SessionID)
}

// /agent offers the primary agents with the one in force marked; picking it
// again changes nothing, picking another switches.
func TestAgentPickerSwitchesPrimaryAgents(t *testing.T) {
	h := &recordingSlashHandlers{}
	ctx := Context{Surface: SurfaceWebChat, SessionID: "s1", Agent: h}
	res := Execute(ctx, "/agent")
	require.NotNil(t, res.Picker)
	require.Equal(t, []PickerItem{{Value: "main", Label: "main", Description: "/w/main", Current: true}, {Value: "ops", Label: "ops", Description: "/w/ops"}}, res.Picker.Items)
	require.Equal(t, "Still on primary agent main.", Choose(ctx, SlashChoice{Command: "agent", Value: "main"}).Reply)
	require.Equal(t, "Switched to primary agent ops, working in /w/ops.", Choose(ctx, SlashChoice{Command: "agent", Value: "ops"}).Reply)
	require.Contains(t, h.calls, "switch(ops,)")
}

type lspSlashStub struct {
	reply   string
	handled bool
}

func (s lspSlashStub) HandleLSPSlash(_, _ string) (string, bool) {
	return s.reply, s.handled
}

// /lsp without a handler answers unavailable; with one it returns its text.
func TestExecLSPSlash(t *testing.T) {
	res := Execute(Context{Surface: SurfaceTUI}, "/lsp")
	require.True(t, res.Handled)
	require.Equal(t, "lsp: unavailable", res.Reply)

	res = Execute(Context{Surface: SurfaceWebChat, LSP: lspSlashStub{reply: "Language servers · 1 configured", handled: true}}, "/lsp")
	require.True(t, res.Handled)
	require.Equal(t, "Language servers · 1 configured", res.Reply)
}
