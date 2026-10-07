// The chat session: construction, config reload, permissions, and shell input.
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	homepkg "github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/session"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

func runnerProjectKey(r *run.Runner) string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.ProjectKey)
}

// runUsageCarrier shuttles per-turn token usage from the supervisorrun result
// into the deferred RunEndedMsg emission. Phase 2 Tracker subscribes to those
// fields to fold provider-reported tokens into the session counter snapshot.
type runUsageCarrier struct {
	runID string
	in    int
	out   int
}

var projectContextGetwd = os.Getwd

func shouldEmitRunEndedOnRequiresAction(attached bool) bool {
	return !attached
}

type ChatSession struct {
	// Env owns every path, config and store this session runs on. The session
	// used to hold a second copy of each (Home/Config/Sandbox/SQL/MemoryStore/
	// SessStore/ActionSvc/Runner/Pipe/RunSvc/CtxHook); it now reads through the
	// accessors below instead. A config reload swaps run.Deps.AppCfg for a new
	// pointer and re-points every holder, so a duplicate field here would keep
	// serving the pre-reload value -- which is exactly what P2-8 removes.
	Env  *process.Environment
	Core *turn.Service
	// WorkingDir is the TUI launch cwd, captured once at start. Its git-root
	// derives the session's fixed project identity for tools and agent.
	WorkingDir    string
	LaunchProject safety.ProjectContext
	chatLog       *chatDiskLog
	tuiMu         sync.Mutex
	uiNotify      func(any)
	// uiSessionID is the conversation this surface is currently looking at.
	// Events the event funnel receives for any other session are persisted
	// to their own log but never painted here: a process-wide reaper and
	// async subagents of other conversations would otherwise draw ghosts on
	// whichever screen happens to be open. Empty while no session is
	// attached — boot-time events still paint, as they always did.
	uiSessionID string
	// uiDispatch* serializes notification delivery independently of the caller.
	// Producers only enqueue, so a slow or blocked surface callback can never
	// stall an agent/tool StepHook. The worker reads uiNotify under tuiMu at
	// delivery time, which lets Set/Prepend/Clear update the sink safely.
	uiDispatchMu      sync.Mutex
	uiDispatchQueue   []any
	uiDispatchWake    chan struct{}
	uiDispatchStop    chan struct{}
	uiDispatchStarted bool
	uiDispatchStopped bool

	// mcpWatch* hold the one MCP startup subscription this session has open.
	// It is re-armed per session id (see WatchMCPStartup), so the fields record
	// which conversation the live subscription belongs to and how to end it.
	mcpWatchMu      sync.Mutex
	mcpWatchSession string
	mcpWatchCancel  func()

	approvalMu      sync.Mutex
	approvalPending *chatApprovalResume
	// approvalRecovered names the sessions whose durable approval outbox this
	// process has already drained, keyed by session id because the drain is
	// scoped to the conversation the surface has open.
	approvalRecoveryMu sync.Mutex
	approvalRecovered  map[string]bool
	// stopAbandonedRunReaper ends this process's share of abandoned-run
	// reaping (and its approval-continuation recovery pass). Close stops it
	// before the environment: the owner lease must outlive the reaper that
	// may still report on this process's behalf.
	stopAbandonedRunReaper func()

	// planReviewerFactory overrides how a reviewer is built for a chosen model.
	// Left nil outside tests, where the shared reviewer in pkg/process is used.
	// The reviews themselves are not surface state: they live on the
	// conversation's plan_reviewed events, read back through
	// turn.PlanReviewsForAction, so a restart or another surface serves them.
	planReviewerFactory func(turn.Model) (turn.Reviewer, error)

	// fastMode persists the per-session /fast toggle (Anthropic service_tier=auto).
	// Loaded lazily from $FOREBRAIN_HOME/state/fast/<session>.json on first
	// access and rewritten on Set. The bool is read inside
	// dispatchUserTurnContent to seed llm.WithFast(ctx) before the agent runs.
	fastMu      sync.RWMutex
	fastSession string
	fastEnabled bool
	fastLoaded  bool

	tuiRunMu   sync.Mutex
	tuiControl *run.Controller
	// planReviewCancel stops an in-flight plan review. The review runs between
	// two approval prompts, when the run that asked for the approval has already
	// returned, so it is the only thing a user interrupt has left to cancel.
	// Guarded by tuiRunMu together with the run cancel it sits beside.
	planReviewCancel context.CancelFunc
	tuiTurnInputRT   *run.TurnInputRuntime
	// tuiTurnInputQueue is the conversation queue the current turn's runtime
	// is attached to, so the turn's end can detach it without knowing the
	// conversation it ran in. Guarded by tuiRunMu.
	tuiTurnInputQueue *run.InputQueue
	tuiUserShells     map[string][]*userShellExecution
	dispatchTurnMu    sync.Mutex

	// tuiPartialCapture mirrors the orchestration session for the active TUI
	// run so a cancelled turn can persist already-completed messages. Set in
	// prepareTUIAgentBase, read in the cancel path. Guarded by dispatchTurnMu
	// (turns are serialized), no separate lock needed.
	// turnBeforeAgent is the hook the executor calls once the run row exists.
	// It is per-turn state rather than a constructor option because it closes
	// over this turn's compaction preflight and event mirror; dispatchTurnMu
	// already serialises turns, so one slot is enough.
	turnBeforeAgent   func(ctx context.Context, runID string) error
	tuiPartialCapture *run.PartialSessionCapture
	tuiStreamAccum    *turn.StreamPartial

	toolApprovalMu    sync.Mutex
	toolApprovalSink  turn.ToolApprovalDecisionSink
	networkApprovalMu sync.Mutex
	// subagentApprovalMu keeps the overlay single-file while several subagents
	// run at once: subagent_fanout dispatches its children concurrently, so two
	// of them can reach an approval gate in the same instant and the user must
	// answer them one after another rather than have one overlay replace the
	// other mid-question.
	subagentApprovalMu sync.Mutex
	surfaceSyncResume  atomic.Bool
	userHooks          *hook.Runtime

	// config reload (TUI): the fsnotify watcher that used to live here was
	// deleted once process.Environment became the only session constructor —
	// it never ran in production and left two watcher implementations in the
	// tree (P3-5 requires exactly one; its idempotency tests moved to
	// pkg/process alongside the surviving watcher, per P3-1). The
	// defer-while-running coordinator that used to live beside it went the
	// same way once the environment became mandatory: process.ConfigManager is
	// the only one, and the session merely tells it whether a run is in
	// flight. What remains here is the explicit reload seam: /connect and the
	// config-writing slash commands call ReloadConfig / reloadConfigFromDisk
	// directly. configApplyMu serializes reloadConfigFromDisk.
	configApplyMu sync.Mutex
}

// approvalResumeOwner is the process identity the approval-continuation
// lease is claimed under. It is the same owner that vouches for this
// process's runs: one process has one answer to "is it still alive", so the
// continuation fence and the run lease can never disagree about it.
func (s *ChatSession) approvalResumeOwner() string {
	if s == nil || s.runSvc() == nil {
		return ""
	}
	return s.runSvc().Owner
}

// OpenChatSessionWithConfigForProject opens a session using a fixed launch cwd,
// deriving trust-based sandbox defaults before startup checks and Runner.Load.
func OpenChatSessionWithConfigForProject(ctx context.Context, cfg appcfg.Root, cwd string) (*ChatSession, error) {
	return openProcessChatSession(ctx, cfg, cwd)
}

func (s *ChatSession) projectContextPreHook(ctx context.Context, phase string, hc hook.HookContext, text string) (hook.PreHookResult, error) {
	out := hook.PreHookResult{Text: text}
	if s == nil {
		return out, nil
	}
	cwd := strings.TrimSpace(s.WorkingDir)
	if cwd == "" {
		return out, nil
	}
	projectRoot := strings.TrimSpace(memory.ProjectRoot(cwd))
	if projectRoot == "" {
		projectRoot = cwd
	}
	currentWorkingDir, ok := terminalCurrentWorkingDir()
	if !ok {
		return out, nil
	}
	lines := []string{
		"<project_context>",
		"project_root: " + projectRoot,
		"current_working_directory: " + currentWorkingDir,
		"Use project_root for project-relative shell commands and file paths. Do not assume /workspace/forebrain or any other container path unless it is explicitly shown here.",
	}
	if sameProjectContextPath(currentWorkingDir, projectRoot) {
		lines = append(lines, "The terminal is already at project_root; run project-root commands without `cd "+projectRoot+" &&`.")
	}
	lines = append(lines, "</project_context>")
	out.SystemAddendum = strings.Join(lines, "\n")
	return out, nil
}

func terminalCurrentWorkingDir() (string, bool) {
	wd, err := projectContextGetwd()
	if err != nil || strings.TrimSpace(wd) == "" {
		return "", false
	}
	return wd, true
}

func sameProjectContextPath(a, b string) bool {
	aa := normalizeProjectContextPath(a)
	bb := normalizeProjectContextPath(b)
	return aa != "" && bb != "" && aa == bb
}

func normalizeProjectContextPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	path = filepath.Clean(path)
	if runtime.GOOS == "darwin" {
		switch {
		case path == "/var":
			path = "/private/var"
		case strings.HasPrefix(path, "/var/"):
			path = "/private" + path
		case path == "/tmp":
			path = "/private/tmp"
		case strings.HasPrefix(path, "/tmp/"):
			path = "/private" + path
		case path == "/etc":
			path = "/private/etc"
		case strings.HasPrefix(path, "/etc/"):
			path = "/private" + path
		}
	}
	if eval, err := filepath.EvalSymlinks(path); err == nil && strings.TrimSpace(eval) != "" {
		path = filepath.Clean(eval)
	}
	return path
}

func (s *ChatSession) ResumeSession(ctx context.Context, sessionID string) (string, string, string, error) {
	if s == nil || s.sessStore() == nil {
		return "", "", "", fmt.Errorf("session store unavailable")
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return "", "", "", fmt.Errorf("session id required")
	}
	ok, err := s.sessStore().HasSession(ctx, sid)
	if err != nil {
		return "", "", "", err
	}
	if !ok {
		return "", "", "", fmt.Errorf("resume: no session matches %q", sid)
	}
	title := sid
	if t, err := s.sessStore().SessionTitle(ctx, sid); err == nil && t != "" {
		title = t
	}
	if err := s.sessStore().Ensure(ctx, sid, title); err != nil {
		return "", "", "", err
	}
	warning, err := s.ActivateSessionModel(ctx, sid)
	if err != nil {
		return "", "", "", err
	}
	return sid, title, warning, nil
}

// ActivateSessionModel puts the runner on sessionID's own model selection:
// a stored choice is restored exactly, a session without one resets to the
// config default, and a stored choice the config no longer offers falls back
// to the default with a non-fatal warning. Every fatal path restores the
// outgoing selection first, so an aborted switch leaves the outgoing session
// and its runtime intact.
func (s *ChatSession) ActivateSessionModel(ctx context.Context, sessionID string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("session unavailable")
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return "", fmt.Errorf("session id required")
	}
	store := s.sessStore()
	r := s.runner()
	if store == nil || r == nil {
		// A surface assembled without a runner (or a store) is the documented
		// no-op configuration run.Run also accepts: there is no live model to
		// activate and no stored selection to restore.
		return "", nil
	}
	outgoing := run.PrimaryModelSelection(r)
	sel, hasRow, err := store.SessionModelSelection(ctx, sid)
	if err != nil {
		return "", err
	}
	if !hasRow {
		// A session that never chose runs on the latest file default.
		if err := r.ResetPrimaryModel(); err != nil {
			return "", s.restoreAfterActivationFailure(r, outgoing, err)
		}
		return "", nil
	}
	effort := sel.Effort
	err = r.SetPrimaryModel(sel.Provider, sel.Model, &effort)
	if err == nil {
		return "", nil
	}
	if !run.IsPrimaryModelNotConfigured(err) {
		return "", s.restoreAfterActivationFailure(r, outgoing, err)
	}
	// The stored pair is gone from the config: fall back to the new default,
	// overwrite the stale row, and continue with a warning.
	if resetErr := r.ResetPrimaryModel(); resetErr != nil {
		return "", s.restoreAfterActivationFailure(r, outgoing, fmt.Errorf("%v (resetting to the default then failed: %w)", err, resetErr))
	}
	fallback := run.PrimaryModelSelection(r)
	if _, saveErr := store.SaveSessionModelSelection(ctx, sid, state.SessionModelSelection{
		Provider: fallback.Provider,
		Model:    fallback.Model,
		Effort:   fallback.Effort,
	}); saveErr != nil {
		return "", s.restoreAfterActivationFailure(r, outgoing, fmt.Errorf("the stored model %s/%s is no longer configured and the fallback could not be saved: %w", sel.Provider, sel.Model, saveErr))
	}
	return fmt.Sprintf("The model saved for this conversation (%s / %s) is no longer configured; it now runs on %s / %s.", sel.Provider, sel.Model, fallback.Provider, fallback.Model), nil
}

// restoreAfterActivationFailure rebuilds the outgoing selection a fatal
// activation error interrupted, wrapping the original error as
// runtime-uncertain when even that restoration fails.
func (s *ChatSession) restoreAfterActivationFailure(r *run.Runner, outgoing run.PrimaryModelState, cause error) error {
	var restoreErr error
	if outgoing.Set {
		effort := outgoing.Effort
		restoreErr = r.SetPrimaryModel(outgoing.Provider, outgoing.Model, &effort)
	} else {
		restoreErr = r.ResetPrimaryModel()
	}
	if restoreErr == nil {
		return cause
	}
	return run.NewRuntimeUncertainError(cause, restoreErr)
}

const tuiBrowseStateSurface = "tui"

func (s *ChatSession) SaveSurfaceBrowseState(ctx context.Context, sessionID string, browse RendererBrowseState) error {
	if s == nil || s.sessStore() == nil {
		return nil
	}
	raw, err := json.Marshal(browse)
	if err != nil {
		return err
	}
	return s.sessStore().SaveSessionUIState(ctx, sessionID, tuiBrowseStateSurface, raw)
}

func (s *ChatSession) LoadSurfaceBrowseState(ctx context.Context, sessionID string) (RendererBrowseState, bool, error) {
	if s == nil || s.sessStore() == nil {
		return RendererBrowseState{}, false, nil
	}
	raw, ok, err := s.sessStore().LoadSessionUIState(ctx, sessionID, tuiBrowseStateSurface)
	if err != nil || !ok {
		return RendererBrowseState{}, ok, err
	}
	var browse RendererBrowseState
	if err := json.Unmarshal(raw, &browse); err != nil {
		return RendererBrowseState{}, false, err
	}
	return browse, true, nil
}

// The session's dependencies all live on process.Environment, which is their
// single owner (P2-8). These accessors are the only way the TUI reaches them,
// so a reload that re-points Environment.Deps is seen everywhere at once.
//
// Each tolerates a nil session and a nil environment because the TUI builds
// partial sessions before Open succeeds and tears them down after Close nils
// Env out; that is the same contract the zero-valued fields used to give.

func (s *ChatSession) deps() *run.Deps {
	if s == nil || s.Env == nil {
		return nil
	}
	return &s.Env.Deps
}

func (s *ChatSession) home() string {
	d := s.deps()
	if d == nil {
		return ""
	}
	return d.Home
}

// cfg returns the live configuration -- the one the environment owns, not a
// copy. Returning a fabricated empty Root when there is no environment would
// silently swallow every write through it, so a session without one gets nil
// and fails loudly instead.
func (s *ChatSession) cfg() *appcfg.Root {
	d := s.deps()
	if d == nil {
		return nil
	}
	return d.AppCfg
}

func (s *ChatSession) sandbox() *safety.Manager {
	if s == nil || s.Env == nil {
		return nil
	}
	return s.Env.Sandbox
}

func (s *ChatSession) memoryStore() *memory.Store {
	d := s.deps()
	if d == nil {
		return nil
	}
	return d.MemoryStore
}

func (s *ChatSession) sessStore() *state.SessionStore {
	d := s.deps()
	if d == nil {
		return nil
	}
	return d.SessionStore
}

func (s *ChatSession) actionSvc() *state.ActionService {
	d := s.deps()
	if d == nil {
		return nil
	}
	return d.Actions
}

func (s *ChatSession) runner() *run.Runner {
	if s == nil || s.Env == nil {
		return nil
	}
	return s.Env.Runner
}

func (s *ChatSession) pipe() *hook.AgentPipeline {
	if s == nil || s.Env == nil {
		return nil
	}
	return s.Env.Hooks
}

func (s *ChatSession) runSvc() *state.RunStore {
	d := s.deps()
	if d == nil {
		return nil
	}
	return d.RunRT
}

func (s *ChatSession) ctxHook() *assembly.Hook {
	if s == nil {
		return nil
	}
	return s.Env.ContextHook()
}

func (s *ChatSession) Close() error {
	if s == nil {
		return nil
	}
	// A continuation still waiting on a usage limit dies with the terminal:
	// nothing would be left to show it or to run it into.
	if s.Core != nil {
		s.Core.StopAutoContinue()
	}
	s.ClearUINotify()
	s.stopUINotificationDispatcher()
	// The reaper speaks through this session's publishers; stop it before the
	// environment it reads from and writes to goes away.
	if s.stopAbandonedRunReaper != nil {
		s.stopAbandonedRunReaper()
		s.stopAbandonedRunReaper = nil
	}
	var errs []error
	if s.chatLog != nil {
		if err := s.chatLog.Close(); err != nil {
			errs = append(errs, err)
		}
		s.chatLog = nil
	}
	if s.Env != nil {
		s.Env.Close()
		s.Env = nil
	}
	return errors.Join(errs...)
}

func (s *ChatSession) ensureSessionStartHooks(sessionID string) {
	if s == nil || s.userHooks == nil || s.sessStore() == nil {
		return
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		sid = "default"
	}
	turns, err := s.sessStore().ListRecentMessages(context.Background(), sid, 1)
	if err != nil || len(turns) > 0 {
		return
	}
	transcriptPath, err := hook.WriteSessionTranscriptArtifact(s.userHooks.StateRoot(), s.sessStore(), sid)
	if err != nil {
		return
	}
	out, err := s.userHooks.ExecuteSessionStart(context.Background(), hook.SessionStartInput{
		BaseInput: hook.BaseInput{
			HookEventName:  hook.EventSessionStart,
			SessionID:      sid,
			TranscriptPath: transcriptPath,
			Cwd:            s.home(),
		},
		Source: "startup",
	})
	if err != nil {
		return
	}
	if strings.TrimSpace(out.InitialUserMessage) != "" {
		_, _ = s.sessStore().Append(context.Background(), sid, "assistant", out.InitialUserMessage)
	}
}

func (s *ChatSession) PrependUINotify(fn func(any)) {
	if s == nil || fn == nil {
		return
	}
	s.tuiMu.Lock()
	prev := s.uiNotify
	s.uiNotify = func(m any) {
		fn(m)
		if prev != nil {
			prev(m)
		}
	}
	s.tuiMu.Unlock()
}

// StartApprovalRecovery drains the decisions of one conversation that were
// committed before a TUI process exited. The durable wait is the continuation
// outbox; the surface calls this once it has opened a session, so recovery both
// has somewhere to send its notifications and knows whose they are.
//
// The session id is the whole point of the entry: recovery does not silently
// tidy rows, it fails the run it recovers and tells the user an approval of
// theirs was interrupted. Draining the machine-wide outbox here greeted a fresh
// session with another conversation's interrupted subagent, and — had the
// continuation been resumable rather than uncertain — would have replayed
// another conversation's tool call into it. Continuations of other sessions
// stay in the outbox for the surface that opens them.
//
// Recovery runs once per session per process: switching to another session
// drains that one, and switching back does not repeat the pass.
func (s *ChatSession) StartApprovalRecovery(sessionID string) {
	if s == nil {
		return
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return
	}
	s.approvalRecoveryMu.Lock()
	if s.approvalRecovered == nil {
		s.approvalRecovered = map[string]bool{}
	}
	already := s.approvalRecovered[sid]
	s.approvalRecovered[sid] = true
	s.approvalRecoveryMu.Unlock()
	if already {
		return
	}
	go func() {
		s.recoverResolvedApprovalWaitsOnce(sid)
		timer := time.NewTimer(state.WaitResumeLease + 250*time.Millisecond)
		defer timer.Stop()
		<-timer.C
		s.recoverResolvedApprovalWaitsOnce(sid)
	}()
}

func (s *ChatSession) recoverResolvedApprovalWaitsOnce(sessionID string) {
	if s == nil || s.runSvc() == nil || s.actionSvc() == nil {
		return
	}
	ids, err := s.runSvc().ListResolvedWaitActionIDs(context.Background(), sessionID, 0)
	if err != nil {
		return
	}
	if uncertain, uncertainErr := s.runSvc().ListUncertainResolvedWaits(context.Background(), sessionID, 0); uncertainErr == nil {
		for _, wait := range uncertain {
			s.interruptUncertainTUIApproval(sessionID, wait)
		}
	}
	for _, actionID := range ids {
		action, err := s.actionSvc().Get(context.Background(), actionID)
		if err != nil || action == nil {
			continue
		}
		switch action.Status {
		case state.ActionApproved, state.ActionAnswered:
			s.resumeAfterApproval(actionID)
		case state.ActionDenied:
			s.resumeAfterDenial(actionID)
		case state.ActionCancelled:
			s.abortPendingApproval(sessionID, actionID)
		case state.ActionExpired:
			reason := strings.TrimSpace(action.Error)
			if reason == "" {
				reason = "approval ttl expired"
			}
			s.failRecoveredApproval(sessionID, actionID, reason)
		case state.ActionError:
			s.failRecoveredApproval(sessionID, actionID, action.Error)
		}
	}
}

func (s *ChatSession) interruptUncertainTUIApproval(sessionID string, wait state.Wait) {
	if s == nil || s.runSvc() == nil || strings.TrimSpace(wait.RunID) == "" {
		return
	}
	const reason = "approval continuation was interrupted after execution began; tool outcome is uncertain and was not retried"
	bg := context.Background()
	if err := s.runSvc().MarkWaitResumeUncertain(bg, wait.RunID, wait.ActionID); err != nil {
		return
	}
	_ = s.runSvc().SetStatus(bg, wait.RunID, state.RunStatusFailed)
	_ = s.publishTUIRunEvent(bg, sessionID, wait.RunID, "turn_error", event.TurnErrorPayload{Error: reason, Message: reason})
	if strings.TrimSpace(wait.AgentID) != "" {
		run, _ := s.runSvc().GetRun(bg, wait.RunID)
		parentRunID, sid := "", ""
		if run != nil {
			parentRunID, sid = run.ParentRunID, run.SessionID
		}
		_ = s.publishRunEvent(bg, event.NewRunEvent(
			"approval-uncertain:"+wait.ActionID, wait.RunID, sid, event.RunEventSubagentEnded,
			event.SubagentEndedPayload{AgentID: wait.AgentID, AgentType: wait.SubagentType, TaskID: wait.AgentID, Status: "interrupted", Error: reason, ParentRunID: parentRunID, ExecutionID: wait.RunID}, time.Now(),
		))
	}
	s.tuiController().Cancel(wait.RunID, errors.New(reason))
	s.tuiController().Finish(wait.RunID)
	// Publishing the turn error drew it in the conversation already. A
	// subagent's own view is a separate screen the event does not reach, so
	// that view is told here; the conversation is not told twice.
	if strings.TrimSpace(wait.AgentID) != "" {
		s.notifyUI(NewMessageMsg{Msg: Message{Kind: MsgKindError, Content: reason, AgentID: wait.AgentID, RunID: wait.RunID, Timestamp: time.Now()}})
	}
}

func (s *ChatSession) failRecoveredApproval(sessionID, actionID, reason string) {
	if s == nil || s.runSvc() == nil {
		return
	}
	runID, _, err := s.runSvc().FindRunByAction(context.Background(), strings.TrimSpace(actionID))
	if err != nil || strings.TrimSpace(runID) == "" {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "approval action failed"
	}
	bg := context.Background()
	_ = s.runSvc().ClearWait(bg, runID)
	_ = s.runSvc().SetStatus(bg, runID, state.RunStatusFailed)
	// Publishing the turn error is what draws it, live as on replay.
	_ = s.publishTUIRunEvent(bg, sessionID, runID, "turn_error", event.TurnErrorPayload{Error: reason, Message: reason})
	s.tuiController().Cancel(runID, errors.New(reason))
	s.tuiController().Finish(runID)
	s.clearPendingApproval()
}

func (s *ChatSession) ClearUINotify() {
	if s == nil {
		return
	}
	s.tuiMu.Lock()
	s.uiNotify = nil
	s.tuiMu.Unlock()
}

// SetViewingSession records the conversation this surface is attached to.
// The event funnel consults it to keep other sessions' events off this
// screen (they still land in their own session's log).
func (s *ChatSession) SetViewingSession(sessionID string) {
	if s == nil {
		return
	}
	s.tuiMu.Lock()
	s.uiSessionID = strings.TrimSpace(sessionID)
	s.tuiMu.Unlock()
}

// notifyUI appends a foreground-visible notification to ChatSession's
// serialized, nonblocking UI dispatcher. Producers never invoke the surface
// callback directly, so a slow UI sink cannot stall tool execution. FIFO order
// is the order in which producers acquire uiDispatchMu.
func (s *ChatSession) notifyUI(msg any) {
	if s == nil {
		return
	}
	// Preserve the no-sink behavior of the previous direct path: do not retain
	// notifications emitted before a surface installs a callback.
	s.tuiMu.Lock()
	hasSink := s.uiNotify != nil
	s.tuiMu.Unlock()
	if !hasSink {
		return
	}
	s.enqueueUINotification(msg)
}

// notifyUIForSession paints one event-funnel message when it belongs to the
// conversation this surface is looking at. Empty sessionID (an event from
// before any conversation started) and an unattached surface paint as they
// always did; anything else for another session stays in that session's log
// alone.
func (s *ChatSession) notifyUIForSession(sessionID string, msg any) {
	s.tuiMu.Lock()
	viewing := s.uiSessionID
	s.tuiMu.Unlock()
	if viewing != "" && strings.TrimSpace(sessionID) != "" && strings.TrimSpace(sessionID) != viewing {
		return
	}
	s.notifyUI(msg)
}

func (s *ChatSession) enqueueUINotification(msg any) {
	if s == nil {
		return
	}
	s.uiDispatchMu.Lock()
	if s.uiDispatchStopped {
		s.uiDispatchMu.Unlock()
		return
	}
	if !s.uiDispatchStarted {
		s.uiDispatchWake = make(chan struct{}, 1)
		s.uiDispatchStop = make(chan struct{})
		s.uiDispatchStarted = true
		go s.runUINotificationDispatcher(s.uiDispatchWake, s.uiDispatchStop)
	}
	if coalesceQueuedToolOutput(s.uiDispatchQueue, msg) {
		s.uiDispatchMu.Unlock()
		return
	}
	s.uiDispatchQueue = append(s.uiDispatchQueue, msg)
	wake := s.uiDispatchWake
	s.uiDispatchMu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

const (
	maxQueuedLiveOutputBytes = 32 * 1024
	maxUIDispatchQueueItems  = 4096
)

// coalesceQueuedToolOutput replaces an older, not-yet-delivered live preview
// for the same tool step. Output deltas are state snapshots; lifecycle and
// approval notifications are control messages and remain strictly lossless.
func coalesceQueuedToolOutput(queue []any, incoming any) bool {
	msg, ok := incoming.(NewMessageMsg)
	if !ok || !msg.Msg.ToolOutputDelta {
		return false
	}
	stepID := strings.TrimSpace(msg.Msg.StepID)
	runID := strings.TrimSpace(msg.Msg.RunID)
	agentID := strings.TrimSpace(msg.Msg.AgentID)
	for i := len(queue) - 1; i >= 0; i-- {
		current, currentOK := queue[i].(NewMessageMsg)
		if !currentOK || !current.Msg.ToolOutputDelta {
			continue
		}
		if strings.TrimSpace(current.Msg.StepID) != stepID || strings.TrimSpace(current.Msg.RunID) != runID || strings.TrimSpace(current.Msg.AgentID) != agentID {
			continue
		}
		content := current.Msg.Content + msg.Msg.Content
		if len(content) > maxQueuedLiveOutputBytes {
			content = "[queued live output coalesced]\n" + content[len(content)-maxQueuedLiveOutputBytes:]
		}
		msg.Msg.Content = content
		queue[i] = msg
		return true
	}
	// Under an already-overloaded UI, a fresh live preview may be skipped. The
	// bounded completed event will still finalize the tool card.
	return len(queue) >= maxUIDispatchQueueItems
}

func (s *ChatSession) runUINotificationDispatcher(wake <-chan struct{}, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-wake:
		}
		for {
			s.uiDispatchMu.Lock()
			if s.uiDispatchStopped || len(s.uiDispatchQueue) == 0 {
				s.uiDispatchMu.Unlock()
				break
			}
			msg := s.uiDispatchQueue[0]
			s.uiDispatchQueue[0] = nil
			s.uiDispatchQueue = s.uiDispatchQueue[1:]
			s.uiDispatchMu.Unlock()
			s.dispatchUINotification(msg)
		}
	}
}

func (s *ChatSession) dispatchUINotification(msg any) {
	if s == nil {
		return
	}
	s.tuiMu.Lock()
	fn := s.uiNotify
	s.tuiMu.Unlock()
	if fn == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			telemetry.Log(s.home(), "tui.ChatSession.uiNotificationDispatcher", r)
		}
	}()
	fn(msg)
}

// stopUINotificationDispatcher prevents future enqueueing and asks the worker
// to stop. It deliberately does not wait: an external UI callback may itself
// be blocked, and Close must never deadlock on that callback.
func (s *ChatSession) stopUINotificationDispatcher() {
	if s == nil {
		return
	}
	s.uiDispatchMu.Lock()
	if s.uiDispatchStopped {
		s.uiDispatchMu.Unlock()
		return
	}
	s.uiDispatchStopped = true
	s.uiDispatchQueue = nil
	stop := s.uiDispatchStop
	started := s.uiDispatchStarted
	s.uiDispatchMu.Unlock()
	if started && stop != nil {
		close(stop)
	}
}

func (s *ChatSession) tuiControlLocked() *run.Controller {
	if s.tuiControl == nil {
		s.tuiControl = run.NewController()
	}
	return s.tuiControl
}

func (s *ChatSession) tuiController() *run.Controller {
	if s == nil {
		return nil
	}
	s.tuiRunMu.Lock()
	defer s.tuiRunMu.Unlock()
	return s.tuiControlLocked()
}

func (s *ChatSession) tuiRunIDLocked(sessionID string) string {
	ctl := s.tuiControlLocked()
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		id, _, ok := ctl.Find(sessionID)
		if ok {
			return id
		}
		return ""
	}
	id, _, _, ok := ctl.Current()
	if ok {
		return id
	}
	return ""
}

func (s *ChatSession) tuiFinish(runID string) {
	if s == nil {
		return
	}
	s.tuiRunMu.Lock()
	finished := s.tuiControlLocked().Finish(runID)
	if finished {
		s.tuiTurnInputRT = nil
		// The run is over, so its runtime must stop accepting steers even
		// before the surface turn's own boundary retires the queue: whatever
		// the run never delivered stays queued for the boundary to decide.
		if s.tuiTurnInputQueue != nil {
			s.tuiTurnInputQueue.Detach()
		}
	}
	s.tuiRunMu.Unlock()
	if finished {
		s.reloadConfigAfterRun()
	}
}

// tuiWait ends the executing run without discarding the queued turn
// input. A tool approval is not a pause in place: the run that hit the gate
// returns RequiresAction and is resumed under the same run ID after the user
// decides. Messages the user queued during that run are held in
// tuiTurnInputRT, so tearing it down here would drop them - they would never
// reach the model, and the composer's queue preview would empty itself as soon
// as it reconciled against the resumed run's fresh runtime. The queue is
// instead discarded at the surface-turn boundary (see DispatchSurfaceTurn).
func (s *ChatSession) tuiWait(runID string) {
	if s == nil {
		return
	}
	s.tuiRunMu.Lock()
	suspended := s.tuiControlLocked().WaitApproval(runID)
	s.tuiRunMu.Unlock()
	if suspended {
		s.reloadConfigAfterRun()
	}
}

func (s *ChatSession) CancelActiveRun() bool {
	if s == nil {
		return false
	}
	s.tuiRunMu.Lock()
	ctl := s.tuiControlLocked()
	runID := s.tuiRunIDLocked("")
	reviewCancel := s.planReviewCancel
	var shellCancels []context.CancelFunc
	for _, executions := range s.tuiUserShells {
		for _, exec := range executions {
			if exec != nil && exec.cancel != nil {
				shellCancels = append(shellCancels, exec.cancel)
			}
		}
	}
	s.tuiRunMu.Unlock()
	cancelled := false
	if runID != "" {
		cancelled = ctl.Cancel(runID, context.Canceled)
	}
	if reviewCancel != nil {
		reviewCancel()
		cancelled = true
	}
	for _, cancel := range shellCancels {
		cancel()
		cancelled = true
	}
	return cancelled
}

func (s *ChatSession) tuiRunActive() bool {
	if s == nil {
		return false
	}
	s.tuiRunMu.Lock()
	defer s.tuiRunMu.Unlock()
	if _, _, phase, active := s.tuiControlLocked().Current(); active && phase == run.Running {
		return true
	}
	// The shell registry is written under tuiRunMu by the shells themselves,
	// so it is read under it too.
	for _, executions := range s.tuiUserShells {
		if len(executions) > 0 {
			return true
		}
	}
	return false
}

func (s *ChatSession) ensureTUITurnInputRuntime(sessionID string) *run.TurnInputRuntime {
	if s == nil {
		return nil
	}
	s.tuiRunMu.Lock()
	defer s.tuiRunMu.Unlock()
	if s.tuiTurnInputRT == nil {
		s.tuiTurnInputRT = run.NewTurnInputRuntime()
	}
	rt := s.tuiTurnInputRT
	// The queue belongs to the conversation; the runtime belongs to this
	// turn. Connecting them here is what makes a steer enqueued from the
	// surface reach this turn's tool boundaries, including the window after
	// the turn began but before the engine registered its run.
	q := s.tuiControlLocked().SessionQueue(strings.TrimSpace(sessionID))
	s.tuiTurnInputQueue = q
	q.Attach(rt)
	// Delivery moves a steer from the queue onto its delivered list; the
	// change hook is what tells the surface to render that message into the
	// transcript instead of leaving the pending preview claiming it is still
	// editable.
	channel := "tui"
	q.SetChangeHook(func() {
		s.notifyUI(InputQueueChangedMsg{
			SessionID: strings.TrimSpace(sessionID),
			Channel:   channel,
		})
	})
	return rt
}

// discardTUITurnInput drops the queued turn input at the end of a surface
// turn. Runs no longer own the queue outright (tuiWait keeps it alive
// across an approval gate), so this is the boundary that guarantees input
// queued for one turn cannot be delivered into the next one - which would
// duplicate the copy the surface restores into its composer.
func (s *ChatSession) discardTUITurnInput() {
	if s == nil {
		return
	}
	s.tuiRunMu.Lock()
	s.tuiTurnInputRT = nil
	q := s.tuiTurnInputQueue
	s.tuiTurnInputQueue = nil
	s.tuiRunMu.Unlock()
	// Detach the queue this turn attached: undelivered steers stay in it —
	// the turn's boundary decides what follows them — but without a runtime
	// they can no longer reach any model, so the next turn's fresh runtime
	// can never deliver them.
	if q != nil {
		q.Detach()
	}
}

// SurfaceInputQueue returns the conversation's input queue. All queue
// semantics — admission, recall, boundary decisions, preview — live in it;
// the surface never picks among queued messages itself.
func (s *ChatSession) SurfaceInputQueue(sessionID string) *run.InputQueue {
	if s == nil {
		return nil
	}
	return s.tuiController().SessionQueue(strings.TrimSpace(sessionID))
}

// SendToSubagent hands what the user typed in a subagent's own view to that
// subagent through the engine's channel: it starts the subagent's next
// execution when it is idle, and reaches it at its next tool boundary (or waits
// for the execution after it) when it is running. The surface frames each
// execution it drives with the same tool-audit step hook a turn installs, and
// forwards the queue's boundary decision back to the loop as a UI message.
func (s *ChatSession) SendToSubagent(sessionID, agentKey string, submission ComposerSubmission, followUp bool) (run.SubagentDelivery, error) {
	r := s.runner()
	if r == nil {
		return "", errors.New("no runner")
	}
	mode := run.TurnInputModeSteer
	if followUp {
		mode = run.TurnInputModeFollowUp
	}
	in := run.Input{
		Text:      composerSubmissionPreview(submission),
		Parts:     append([]llm.ContentPart(nil), submission.Parts...),
		Payload:   submission,
		SkillName: strings.TrimSpace(submission.SkillName),
		SkillPath: strings.TrimSpace(submission.SkillPath),
	}
	if in.Text == "" {
		in.Text = strings.Join(strings.Fields(llm.TextContent(submission.Parts...)), " ")
	}
	return run.SendToSubagent(context.Background(), r, s.subagentSurface(sessionID), strings.TrimSpace(sessionID), strings.TrimSpace(agentKey), in, mode)
}

// SubagentInputPreview is the subagent's queued input as its own view shows it.
func (s *ChatSession) SubagentInputPreview(sessionID, agentKey string) ComposerPendingInputPreview {
	preview := run.SubagentInputPreview(s.runner(), strings.TrimSpace(sessionID), strings.TrimSpace(agentKey))
	return ComposerPendingInputPreview{
		PendingSteers:  preview.Steers,
		RejectedSteers: preview.Rejected,
		QueuedMessages: preview.FollowUp,
	}
}

// RecallSubagentInput pulls the subagent's newest queued message back out for
// editing, whole.
func (s *ChatSession) RecallSubagentInput(sessionID, agentKey string) (ComposerSubmission, bool) {
	in, ok := run.RecallSubagentInput(s.runner(), strings.TrimSpace(sessionID), strings.TrimSpace(agentKey))
	if !ok {
		return ComposerSubmission{}, false
	}
	return subagentSubmissionFromInput(in)
}

// InterruptSubagentToSend stops the subagent's running execution to send the
// steers queued behind it (Esc's second meaning in its view).
func (s *ChatSession) InterruptSubagentToSend(sessionID, agentKey string) bool {
	return run.InterruptSubagentToSend(s.runner(), strings.TrimSpace(sessionID), strings.TrimSpace(agentKey))
}

// WithdrawSubagentInput takes a just-sent message back before the subagent
// answered, returning it plus everything queued after it.
func (s *ChatSession) WithdrawSubagentInput(sessionID, agentKey string) ([]ComposerSubmission, bool) {
	inputs, ok := run.WithdrawSubagentInput(s.runner(), strings.TrimSpace(sessionID), strings.TrimSpace(agentKey))
	if !ok {
		return nil, false
	}
	return subagentSubmissions(inputs), true
}

// DiscardSubagentInput empties the subagent's queued input and returns how many
// messages were dropped — the same discard the conversation's own queue gets
// when its conversation is left.
func (s *ChatSession) DiscardSubagentInput(sessionID, agentKey string) int {
	return run.DiscardSubagentInput(s.runner(), strings.TrimSpace(sessionID), strings.TrimSpace(agentKey))
}

// CompactSubagent compacts the subagent's own context from its view: it runs
// against that subagent's worker session, on the model that subagent runs on,
// and refuses while the subagent is running (the way /compact is refused while
// the conversation's own run is).
func (s *ChatSession) CompactSubagent(ctx context.Context, sessionID, agentKey string) (string, bool) {
	r := s.runner()
	if r == nil {
		return "compact: unavailable", true
	}
	subCtx, workerSessionID, err := run.SubagentCompactTarget(ctx, r, strings.TrimSpace(sessionID), strings.TrimSpace(agentKey))
	if err != nil {
		if errors.Is(err, run.ErrSubagentRunning) {
			return "Wait for this subagent to finish before compacting it.", true
		}
		return "compact: " + err.Error(), true
	}
	result := turn.ExecuteCompact(subCtx, workerSessionID, run.CompactionService(r, s.sessStore()))
	s.tuiMu.Lock()
	hasTUI := s.uiNotify != nil
	s.tuiMu.Unlock()
	if hasTUI {
		// The compaction's own events draw its card in this subagent's view.
		return "", true
	}
	return result.Reply, true
}

// SubagentContextReport answers /context for the subagent whose view it is run
// from: the same report as the conversation's, fed the subagent's own worker
// session, model and occupancy.
func (s *ChatSession) SubagentContextReport(sessionID, agentKey string) (string, bool) {
	r := s.runner()
	if r == nil {
		return "context: unavailable", true
	}
	workerSessionID, provider, model, used, explicitLimit, err := run.SubagentContextGauge(context.Background(), r, strings.TrimSpace(sessionID), strings.TrimSpace(agentKey))
	if err != nil {
		return "context: " + err.Error(), true
	}
	src := turn.ContextSources{
		Snapshots: s.Env.Tools(),
		Filtering: tool.CompressorFor(r.StateRoot()).Store(),
	}
	if runs := s.runSvc(); runs != nil {
		src.Compactions = runs
	}
	src.Gauge = turn.ContextGaugeOf(provider, model, used, explicitLimit)
	return turn.ContextReport(context.Background(), src, workerSessionID), true
}

// SubagentComposerTokenStats is the subagent's own footer gauge — how much of
// that subagent's context window is left, computed on the model it runs on.
func (s *ChatSession) SubagentComposerTokenStats(sessionID, agentKey string) ComposerTokenStats {
	payload, ok := run.SubagentContextBudget(context.Background(), s.runner(), strings.TrimSpace(sessionID), strings.TrimSpace(agentKey))
	if !ok {
		return ComposerTokenStats{}
	}
	return composerTokenStatsFromBudget(tokenBudgetMsgFromPayload(payload))
}

// subagentSurface frames each execution the user drives from a subagent's view
// and hands the queue's boundary decision back to the loop.
//
// The step hook is built with a nil predecessor on purpose: a subagent's tool
// steps carry their roster key on the context, so this hook publishes them
// itself, and chaining the process-wide (conversation) hook would publish the
// same step a second time.
func (s *ChatSession) subagentSurface(sessionID string) run.SubagentSurface {
	hook := s.runAuditStepHook(sessionID, func() tool.StepHook { return nil })
	return run.SubagentSurface{
		Frame: func(ctx context.Context) context.Context {
			if hook == nil {
				return ctx
			}
			return tool.WithStepHook(ctx, hook)
		},
		OnBoundary: func(agentKey string, send, restore []run.Input) {
			s.notifyUI(SubagentInputBoundaryMsg{
				AgentKey: strings.TrimSpace(agentKey),
				Send:     subagentSubmissions(send),
				Restore:  subagentSubmissions(restore),
			})
		},
	}
}

// subagentSubmissions recovers the surface's own records from a slice of the
// engine's queue inputs, so a recalled, released or restored message comes back
// whole (text, attachments, folded pastes and all).
func subagentSubmissions(inputs []run.Input) []ComposerSubmission {
	if len(inputs) == 0 {
		return nil
	}
	out := make([]ComposerSubmission, 0, len(inputs))
	for _, in := range inputs {
		if sub, ok := subagentSubmissionFromInput(in); ok {
			out = append(out, sub)
		}
	}
	return out
}

func subagentSubmissionFromInput(in run.Input) (ComposerSubmission, bool) {
	if sub, ok := in.Payload.(ComposerSubmission); ok {
		return sub, true
	}
	// A message without our payload (a foreign or legacy producer) is rebuilt
	// from the queue's own record so it is still editable.
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return ComposerSubmission{}, false
	}
	return ComposerSubmission{Text: text, DisplayText: text, Parts: append([]llm.ContentPart(nil), in.Parts...)}, true
}

func (s *ChatSession) NewSessionID(prefix string) string {
	return session.NewID(prefix)
}

func (s *ChatSession) submitAskUserQuestionApproval(ctx context.Context, sub ApprovalSubmit) {
	actionID := strings.TrimSpace(sub.ActionID)
	service := turn.ApprovalService{Actions: s.actionSvc()}
	result, err := service.Decide(ctx, actionID, turn.ApprovalReply{AskAnswerJSON: sub.AskAnswerJSON})
	if err != nil {
		s.notifyUI(NewMessageMsg{Msg: Message{Kind: MsgKindError, Content: fmt.Sprintf("failed to submit ask answer: %v", err), Timestamp: time.Now()}})
		return
	}
	if result.Idempotent {
		return
	}
	s.recordAskUserQuestionDecision(ctx, result.Action)
	if result.Cancelled {
		s.abortPendingApproval(result.Action.SessionID, actionID)
		return
	}
	s.resumeAfterApproval(actionID)
}

// RecordApprovalGate persists the approval question as display history. The
// sink calls it before the overlay is drawn, so the question the user is looking
// at is already in the log by the time they answer it - including when they
// answer by quitting the process.
func (s *ChatSession) RecordApprovalGate(req turn.ToolApprovalRequest) {
	actionID := strings.TrimSpace(req.ActionID)
	if s == nil || actionID == "" {
		return
	}
	s.appendApprovalDisplayRecord("approval-requested:"+actionID, strings.TrimSpace(req.SessionID), strings.TrimSpace(req.RunID),
		event.RunEventApprovalReq, event.ApprovalRequestedPayload{
			ActionID: actionID, ActionKind: strings.TrimSpace(req.ActionKind),
			AgentID:      strings.TrimSpace(req.AgentID),
			SubagentType: strings.TrimSpace(req.SubagentType),
			ToolStepID:   strings.TrimSpace(req.ToolStepID),
			Message:      firstNonEmpty(strings.TrimSpace(req.Description), strings.TrimSpace(req.Reason)),
		})
}

// RecordApprovalDecision persists the answer together with the confirmation line
// the sink is about to print, so resume replays the sentence the user read
// instead of one reconstructed from the action.
//
// The request's agent is carried verbatim: a subagent's approval belongs to that
// subagent's view, and this record is written first, so a stripped agent id here
// would route the child's own decision back into the conversation.
func (s *ChatSession) RecordApprovalDecision(req turn.ToolApprovalRequest, decision turn.ToolApprovalDecision, confirmation string) {
	actionID := strings.TrimSpace(req.ActionID)
	if s == nil || actionID == "" {
		return
	}
	id, reported, ok := approvalDisplayRecord(actionID, req.ActionKind, decision)
	if !ok {
		return
	}
	s.appendApprovalDisplayRecord(id, strings.TrimSpace(req.SessionID), strings.TrimSpace(req.RunID),
		event.RunEventApprovalResolved, event.ApprovalResolvedPayload{
			ActionID: actionID, ActionKind: strings.TrimSpace(req.ActionKind),
			Decision:     reported,
			Reason:       strings.TrimSpace(decision.DenyReason),
			AgentID:      strings.TrimSpace(req.AgentID),
			SubagentType: strings.TrimSpace(req.SubagentType),
			ToolStepID:   strings.TrimSpace(req.ToolStepID),
			Confirmation: confirmation,
		})
}

// recordAskUserQuestionDecision persists the display record for an answered
// AskUserQuestion. Its overlay paints no confirmation line and the sink has no
// decision hook for it, so without this the log would show a question was asked
// and never that it was answered - only the canceled ones would survive. The
// status is the action's own, so a question that was resolved some other way
// (the action cancelled underneath the answer) is recorded as what it is.
func (s *ChatSession) recordAskUserQuestionDecision(ctx context.Context, act *state.Action) {
	if s == nil || act == nil {
		return
	}
	actionID := strings.TrimSpace(act.ID)
	if actionID == "" {
		return
	}
	status := strings.TrimSpace(string(act.Status))
	if status == "" || status == string(state.ActionPending) {
		status = string(state.ActionAnswered)
	}
	runID := ""
	if s.runSvc() != nil {
		if found, _, err := s.runSvc().FindRunByAction(ctx, actionID); err == nil {
			runID = strings.TrimSpace(found)
		}
	}
	agentID, subagentType := turn.ActionSubagent(act)
	sessionID := act.SessionID
	if sessionID == "" {
		// The action carries the session it was raised for; when it does not,
		// the surface still knows which conversation is holding this gate, and
		// the record belongs there rather than nowhere.
		if p := s.peekPendingApproval(); p != nil {
			sessionID = strings.TrimSpace(p.SessionID)
		}
	}
	s.appendApprovalDisplayRecord(approvalResolvedEventID(actionID, status), sessionID, runID,
		event.RunEventApprovalResolved, event.ApprovalResolvedPayload{
			ActionID: actionID, ActionKind: strings.TrimSpace(act.Kind), Decision: status,
			AgentID: agentID, SubagentType: subagentType,
		})
}

// appendApprovalDisplayRecord writes one approval display record. It is
// append-only by construction - the store keeps the first event for an id, so a
// second writer of the same record is a no-op - and it never borrows the turn's
// cancellable context: a display record has to survive the cancellation that
// often follows it.
func (s *ChatSession) appendApprovalDisplayRecord(id, sessionID, runID, eventType string, payload any) {
	if s == nil || s.runSvc() == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = s.runSvc().AppendSessionEvent(context.Background(), state.SessionEvent{
		ID: id, RunID: strings.TrimSpace(runID), SessionID: strings.TrimSpace(sessionID),
		Type: eventType, Payload: raw, CreatedAt: time.Now(),
	})
}

func (s *ChatSession) ApproveCallback() ApproveFn {
	if s == nil || s.actionSvc() == nil {
		return nil
	}
	return func(sub ApprovalSubmit) {
		actionID := strings.TrimSpace(sub.ActionID)
		if s.chatLog != nil {
			s.chatLog.DebugForceFlushf("forebrain tui tool_approval ui_submit approved=%t cancelled=%t key_chord=%q action_id=%s policy_kind=%q reason=%q detail_preview=%q",
				sub.Approved, sub.Cancelled, sub.KeyChord, actionID, strings.TrimSpace(sub.PolicyKind), strings.TrimSpace(sub.Reason), strings.TrimSpace(sub.DetailPreview))
		}
		decision := turn.ToolApprovalDecision{Approved: sub.Approved, Cancelled: sub.Cancelled, DenyReason: sub.Reason, AskAnswerJSON: sub.AskAnswerJSON}
		if err := s.completeSurfaceApproval(context.Background(), actionID, decision, &sub); err != nil {
			s.notifyUI(NewMessageMsg{Msg: Message{Kind: MsgKindError, Content: fmt.Sprintf("tool approval failed: %v", err), Timestamp: time.Now()}})
		}
	}
}

func (s *ChatSession) slashCommandResult(ctx context.Context, sessionID, channel, line string) (turn.Result, bool) {
	if !strings.HasPrefix(line, "/") {
		return turn.Result{}, false
	}
	toks := strings.Fields(line)
	if len(toks) == 0 {
		return turn.Result{}, false
	}
	if llm.LeadingTokenLooksLikeFilesystemPath(toks[0]) {
		return turn.Result{}, false
	}
	slashCtx, err := s.slashContext(ctx, sessionID, channel)
	if err != nil {
		return turn.Result{Handled: true, Reply: err.Error()}, true
	}
	command := turn.ExecuteSlashCommand
	if s.Core != nil {
		command = s.Core.ExecuteSlashCommand
	}
	return command(slashCtx, line), true
}

// slashContext is what a slash command, or a choice made in the picker one
// offered, runs with on this surface.
func (s *ChatSession) slashContext(ctx context.Context, sessionID, channel string) (turn.Context, error) {
	channel = strings.TrimSpace(channel)
	runID := ""
	if strings.EqualFold(channel, "tui") {
		s.tuiRunMu.Lock()
		runID = s.tuiRunIDLocked(sessionID)
		s.tuiRunMu.Unlock()
	}
	surface, err := slashExecutionSurfaceForChannel(channel)
	if err != nil {
		return turn.Context{}, err
	}
	return turn.Context{
		CommandContext: ctx,
		Home:           s.home(),
		StateRoot:      s.stateRoot(),
		ProjectKey:     runnerProjectKey(s.runner()),
		SessionID:      sessionID,
		Channel:        channel,
		Surface:        surface,
		SessionSource:  memory.SessionSourceTUI,
		RunID:          runID,
		DuringRun:      strings.EqualFold(channel, "tui") && s.tuiRunActive(),
		FastAvailable:  llm.IsOpusLabel(s.CurrentModelOption()),
		Sessions:       s.sessStore(),
		Compact:        s,
		Clear:          s,
		ContextDebug:   s,
		Status:         s,
		Permissions:    s,
		MCP:            s,
		LSP:            s,
		Sandbox:        s,
		Model:          s,
		Agent:          s,
		Fast:           s,
		Memories:       s,
	}, nil
}

func (s *ChatSession) ExecuteSurfaceSlash(ctx context.Context, sessionID string, line string) (turn.SlashOutcome, bool) {
	return s.slashCommandResult(ctx, strings.TrimSpace(sessionID), "tui", strings.TrimSpace(line))
}

// ChooseSurfaceSlash applies a choice made in the picker a slash command
// offered.
func (s *ChatSession) ChooseSurfaceSlash(ctx context.Context, sessionID string, choice turn.SlashChoice) turn.SlashOutcome {
	slashCtx, err := s.slashContext(ctx, strings.TrimSpace(sessionID), "tui")
	if err != nil {
		return turn.SlashOutcome{Handled: true, Reply: err.Error()}
	}
	return turn.Choose(slashCtx, choice)
}

func slashExecutionSurfaceForChannel(channel string) (turn.Surface, error) {
	channel = strings.TrimSpace(channel)
	switch strings.ToLower(channel) {
	case "webchat":
		return turn.SurfaceWebChat, nil
	case "tui":
		return turn.SurfaceTUI, nil
	default:
		return "", fmt.Errorf("unsupported slash channel %q", channel)
	}
}

// requiresActionErrorFromOutcome reconstructs the *tool.RequiresActionError
// that errors.As used to find directly on the run's error chain before
// Core.Submit started reporting an approval gate as a TurnOutcome instead.
// Submit splits that gate into two typed pieces: Resume is execution state
// (ActionID, ToolName, the session snapshot needed to continue) and
// deliberately carries nothing else, while Approval is the surface-facing
// request and is where the tool's real input JSON, AgentID and SubagentType
// still live. Rebuilding the error from Resume alone silently drops those —
// downstream, attachRunWaitForRequiresAction re-marshals rae.ToolInput, and
// marshaling the resulting nil interface succeeds with the literal JSON text
// "null" rather than failing, so the approval overlay renders "Command: null"
// and, because the parsed input no longer carries a real command, offers only
// the fallback cancel option instead of the tool's real decision set.
func requiresActionErrorFromOutcome(outcome turn.TurnOutcome) *tool.RequiresActionError {
	if outcome.Status != turn.TurnWaitingApproval || outcome.Resume == nil {
		return nil
	}
	// RunID prefers Approval's over the top-level outcome's: for a gate raised
	// inside a subagent, run.Run tags the RAE with the subagent's own run ID
	// before it ever reaches the top-level run (tagRAEWithRunID only fills an
	// empty field), and waitingError in process/run_executor.go carries that
	// same value into Approval.RunID. outcome.RunID is always the top-level
	// run instead, so preferring it here would silently forget which run
	// actually owns the paused tool call - the run resumeAgentContext needs
	// to recover the subagent's WorkerSessionID and continue its own session
	// scope instead of falling back to the top-level chat session.
	runID := outcome.RunID
	if outcome.Approval != nil {
		if approvalRunID := strings.TrimSpace(outcome.Approval.RunID); approvalRunID != "" {
			runID = approvalRunID
		}
	}
	rae := &tool.RequiresActionError{
		RunID:           runID,
		ActionID:        outcome.Resume.ActionID,
		ToolName:        outcome.Resume.ToolName,
		SessionSnapshot: outcome.Resume.SessionSnapshot,
	}
	if outcome.Approval != nil {
		rae.ActionKind = outcome.Approval.ActionKind
		rae.AgentID = outcome.Approval.AgentID
		rae.SubagentType = outcome.Approval.SubagentType
		rae.ToolInput = json.RawMessage(outcome.Approval.ToolInputJSON)
	}
	return rae
}

func (s *ChatSession) attachRunWaitForRequiresAction(runID, sessionID, channel, input string, rae *tool.RequiresActionError, turnStartedAt time.Time) {
	if s == nil || rae == nil || s.runSvc() == nil || strings.TrimSpace(runID) == "" {
		return
	}
	toolIn := "{}"
	if b, jerr := json.Marshal(rae.ToolInput); jerr == nil {
		toolIn = string(b)
	}
	_ = s.runSvc().SetWaitingAction(context.Background(), runID, state.Wait{
		RunID:           runID,
		ActionID:        rae.ActionID,
		ToolName:        rae.ToolName,
		ToolInputJSON:   toolIn,
		SessionSnapshot: append([]llm.Message(nil), rae.SessionSnapshot...),
		AgentID:         rae.AgentID,
		SubagentType:    rae.SubagentType,
		// Persisted (not just kept in the in-memory pending approval) so a
		// resume reconstructed after a process restart can still recover the
		// subagent's WorkerSessionID via agent.GetMerged, which reads the
		// durable subagent-history ledger startSubagent writes at spawn time.
		SubagentRunID: strings.TrimSpace(rae.RunID),
	})
	s.setPendingApproval(&chatApprovalResume{
		ActionID: rae.ActionID,
		RunID:    runID,
		// SubagentRunID is rae.RunID, not runID: when the gate was raised
		// inside a subagent, run.Run tags the RAE with that subagent's own
		// run ID (see requiresActionErrorFromOutcome), which is how
		// resumeAgentContext finds its way back to the subagent's live
		// registry entry and its WorkerSessionID. runID here is always the
		// top-level run driving Submit/ExistingRunID, which for a
		// subagent-originated gate is a different run entirely.
		SubagentRunID:   strings.TrimSpace(rae.RunID),
		SessionID:       sessionID,
		Channel:         channel,
		Input:           input,
		ToolStepID:      originatingApprovalToolStepID(rae),
		ToolName:        rae.ToolName,
		AgentID:         rae.AgentID,
		SubagentType:    rae.SubagentType,
		SessionSnapshot: rae.SessionSnapshot,
		TurnStartedAt:   turnStartedAt,
	})
}

func originatingApprovalToolStepID(rae *tool.RequiresActionError) string {
	if rae == nil {
		return ""
	}
	if id := strings.TrimSpace(rae.OriginatingToolCallID); id != "" {
		return id
	}
	return turn.PendingApprovalToolStepID(rae.SessionSnapshot, rae.ToolName)
}

func (s *ChatSession) persistRequiresActionSnapshot(sessionID, runID string, snapshot []llm.Message) {
	if s == nil || s.sessStore() == nil || len(snapshot) == 0 {
		return
	}
	_ = s.sessStore().AppendMessageSequenceForRun(
		context.Background(),
		sessionID,
		runID,
		snapshot,
		cliResultModel(s),
		"",
	)
}

// persistReplayResults writes the tool-result messages captured during a
// resume replay to the session state. persistRequiresActionSnapshot saved the
// RAE snapshot (which includes the assistant tool_calls row but no tool result)
// when the approval gate fired. During resume, the orchestration LLM replays
// the tool and produces a result, but that result lives only in its internal
// session and would be discarded. Without persisting it here, the store is left
// with a dangling tool_calls row that RepairDanglingToolResults strips on the
// next turn - causing the LLM to lose knowledge that the tool was called and
// approved (e.g. exit_plan_mode).
func (s *ChatSession) persistReplayResults(sessionID, runID string, capture *tool.ReplayResultCapture) {
	if s == nil || s.sessStore() == nil || capture == nil {
		return
	}
	entries := capture.Entries()
	if len(entries) == 0 {
		return
	}
	msgs := make([]llm.Message, 0, len(entries))
	for _, e := range entries {
		msg := llm.ToolResultMessage(e.ToolCallID, llm.Text(e.Content))
		if e.ToolDisplay != nil {
			display := *e.ToolDisplay
			msg.ToolDisplay = &display
		}
		if e.Timing.Valid() {
			timing := e.Timing
			msg.ToolExecutionTiming = &timing
		}
		msgs = append(msgs, msg)
	}
	// These results answer calls the gate stored before it suspended, so they
	// are new rows by construction and carry no shared history to reconcile.
	_ = s.sessStore().AppendNewMessages(
		context.Background(),
		sessionID,
		runID,
		msgs,
		cliResultModel(s),
		"",
	)
}

// persistCancelledTurnOutcome writes the partial content a cancelled run
// already produced and displayed to the user, so it is not lost from the
// transcript DB. It combines:
//   - the orchestration's partial session (completed assistant tool_calls +
//     tool results captured before the cancellation), and
//   - the streamed partial assistant text + reasoning (which never entered
//     the orchestration session because the LLM call was interrupted).
//
// The two sources must not overlap. The streamed-text buffer is cleared each
// time the orchestration loop appends a finished response to the session (see
// StreamSink.OnResponseCompleted), so by the time a cancel lands it holds
// only the interrupted call's text and the capture owns everything before it.
// Without that boundary the buffer would span the whole turn and every
// assistant message would be written twice — once inside the capture and again
// as the trailing plain-text row, which is what resume replay then showed.
//
// RepairDanglingToolResults then strips any assistant tool_calls row whose
// results did not arrive (e.g. the tool was mid-execution at cancel time),
// keeping the transcript valid for the next round.
func (s *ChatSession) persistCancelledTurnOutcome(sessionID, runID string, completion turnCompletion) {
	if s == nil || s.sessStore() == nil {
		return
	}
	var captured []llm.Message
	if cap := s.tuiPartialCapture; cap != nil {
		captured = cap.Snapshot()
	}
	var partialText, partialReasoning string
	if acc := s.tuiStreamAccum; acc != nil {
		partialText = acc.Content()
		partialReasoning = acc.Reasoning()
	}
	turn.PersistCancelledTurn(context.Background(), s.sessStore(), turn.CancelledTurn{
		SessionID:        sessionID,
		RunID:            runID,
		Captured:         captured,
		PartialText:      partialText,
		PartialReasoning: partialReasoning,
		Model:            cliResultModel(s),
		End:              completion.runEnd(),
		OnRepairError: func(err error) {
			if s.chatLog != nil {
				s.chatLog.Debugf("forebrain chat dangling tool_calls repair failed session=%s err=%v", sessionID, err)
			}
		},
	})
}

func (s *ChatSession) dispatchUserTurnContent(ctx context.Context, sessionID, channel, input string, rawInput string, inputParts []llm.ContentPart, userPartsJSON string, goalObjective string, skillName string, skillPath string, callerRendersReturnedError bool, out io.Writer) (retErr error) {
	s.dispatchTurnMu.Lock()
	defer s.dispatchTurnMu.Unlock()
	foreground := foregroundTurnFrom(ctx)
	// Durable transcript work — the dangling-tool-call repair, the user row
	// itself and the withdrawal cleanup — must never be interrupted
	// half-applied: a partial rewrite both corrupts the transcript and forks the
	// provider's cached prompt prefix earlier than the message being withdrawn.
	// This is the context those steps already ran on before withdrawal existed;
	// Esc is observed at the boundaries between them instead of inside them.
	durable := context.WithoutCancel(ctx)
	var userRowID int64
	var persistErr error
	t0 := time.Now()
	var runUsage runUsageCarrier
	var completion turnCompletion
	emitRunEnded := true
	defer func() {
		if !emitRunEnded {
			return
		}
		s.notifyUI(RunEndedMsg{
			turn:           foreground,
			RunID:          runUsage.runID,
			WorkedDuration: completion.Duration,
			InputTokens:    runUsage.in,
			OutputTokens:   runUsage.out,
		})
	}()
	// This runs before RunEnded and before either ownership lock is released.
	// Cancellation must not cancel the durable cleanup itself.
	defer func() {
		if !foreground.finish() {
			return
		}
		retErr = nil
		var cause error
		if userRowID > 0 {
			cause = s.sessStore().WithdrawUserTurn(durable, sessionID, userRowID)
		} else if persistErr != nil {
			cause = persistErr
		}
		if cause == nil {
			return
		}
		// The user sees one sentence saying what is and is not true; the store's
		// own error text is a transport detail and belongs in the log, not the
		// transcript.
		if s.chatLog != nil {
			s.chatLog.Errorf("forebrain chat withdraw failed session=%s row_id=%d err=%v", sessionID, userRowID, cause)
		}
		retErr = errWithdrawNotDurable
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.chatLog != nil {
		prev := llm.TruncateBytes(input, 512, "...")
		s.chatLog.Debugf("forebrain chat turn start session=%s channel=%s input_bytes=%d preview=%q", sessionID, channel, len(input), prev)
	}
	if s.hasPendingToolApproval() {
		return nil
	}
	if s.runSvc() != nil {
		waiting, werr := s.runSvc().SessionHasRunWaitingOnToolApproval(durable, sessionID)
		if werr == nil && waiting {
			return nil
		}
	}
	// A cancelled run or a dismissed tool approval can leave the persisted
	// transcript ending in an assistant message whose tool_calls were never
	// answered. Re-sending that to the provider fails with "An assistant message
	// with 'tool_calls' must be followed by tool messages responding to each
	// 'tool_call_id'." Repair the transcript before it is read for this turn.
	if s.sessStore() != nil {
		if repaired, rerr := s.sessStore().RepairDanglingToolResults(durable, sessionID); rerr != nil {
			if s.chatLog != nil {
				s.chatLog.Debugf("forebrain chat dangling tool_calls repair failed session=%s err=%v", sessionID, rerr)
			}
		} else if repaired > 0 && s.chatLog != nil {
			s.chatLog.Debugf("forebrain chat repaired dangling tool_calls session=%s rows=%d", sessionID, repaired)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// The pre-turn compaction runs on the turn's own context, so Esc stops it
	// along with the turn it was making room for. Stopping it cannot leave
	// half a rewrite behind — the checkpoint commits in one transaction or not
	// at all — and the history stays as it was until the next turn compacts.
	if cerr := s.maybeAutoCompactBeforeAppend(ctx, sessionID, channel, input, rawInput); cerr != nil && s.chatLog != nil {
		s.chatLog.Debugf("forebrain chat auto compact skipped session=%s err=%v", sessionID, cerr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.userHooks != nil {
		transcriptPath, terr := hook.WriteSessionTranscriptArtifact(s.userHooks.StateRoot(), s.sessStore(), sessionID)
		if terr != nil {
			return terr
		}
		hookOut, herr := s.userHooks.ExecuteUserPromptSubmit(durable, hook.UserPromptSubmitInput{
			BaseInput: hook.BaseInput{
				HookEventName:  hook.EventUserPromptSubmit,
				SessionID:      strings.TrimSpace(sessionID),
				TranscriptPath: transcriptPath,
				Cwd:            s.home(),
				PermissionMode: "on-request",
			},
			Prompt: input,
		})
		if herr != nil {
			return herr
		}
		input = hook.AppendAdditionalContext(input, hookOut.AdditionalContext)
		if hookOut.Blocked {
			msg := strings.TrimSpace(hookOut.StopReason)
			if msg == "" {
				msg = strings.TrimSpace(hookOut.SystemMessage)
			}
			if msg == "" {
				msg = "Operation stopped by hook"
			}
			return nil
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// A slash command (e.g. /plan foo) reaches here with input = the expanded
	// prompt fed to the model and rawInput = the original command the user typed.
	// The model context is reconstructed from PartsJSON, not content (see
	// ParseMessageParts, which prefers parts and treats content as a fallback), so
	// the row's content field is display-only for user turns. Store the raw
	// command there so resume replay shows what the user actually typed, while
	// PartsJSON keeps carrying the expanded prompt for the model.
	userRowID, persistErr = turn.PersistUserTurn(durable, s.sessStore(), turn.UserTurn{
		SessionID:  sessionID,
		ModelInput: input,
		RawInput:   rawInput,
		Parts:      inputParts,
		PartsJSON:  userPartsJSON,
	})
	if persistErr != nil {
		return persistErr
	}
	// From here the turn is cancellable again: agBase carries this dispatch's
	// context so a withdrawal decided before the run is registered still stops it.
	agBase := process.AgentContextForProject(ctx, s.stateRoot(), sessionID, runnerProjectKey(s.runner()))
	agBase = run.WithExplicitSkillSelection(agBase, skillName, skillPath)
	if s.IsFastMode() && llm.IsOpusLabel(s.CurrentModelOption()) {
		agBase = llm.WithFast(agBase, true)
	}
	agWrapped, streamFlag, cleanup := s.prepareTUIAgentBase(sessionID, agBase)
	defer cleanup()
	var runIDOut string
	s.turnBeforeAgent = func(agentCtx context.Context, runID string) error {
		// Set the reducer/tracker active run before any tool or usage event for
		// this run can reach it, so per-run counters (the "Worked for" tools/
		// tokens line) key off the real run ID instead of falling back to "".
		if err := agentCtx.Err(); err != nil {
			return err
		}
		// The message was stored before the engine created the run it starts.
		// It is that run's first row all the same: a run that ends before
		// writing anything of its own closes after it on replay.
		if err := s.sessStore().BindMessageToRun(durable, sessionID, userRowID, runID); err != nil && s.chatLog != nil {
			s.chatLog.Errorf("forebrain chat bind user row session=%s row_id=%d run=%s failed: %v", sessionID, userRowID, runID, err)
		}
		s.notifyUI(RunStartedMsg{RunID: runID, turn: foreground})
		return nil
	}
	defer func() { s.turnBeforeAgent = nil }()
	outcome, err := s.Core.Submit(agWrapped, turn.TurnRequest{
		SessionID:     sessionID,
		Origin:        turn.Origin{Surface: turn.SurfaceTUI, ChannelID: channel},
		UserText:      input,
		RawInput:      rawInput,
		Parts:         inputParts,
		GoalObjective: strings.TrimSpace(goalObjective),
		SkillName:     skillName,
		SkillPath:     skillPath,
	}, nil)
	res := outcome.Result
	runIDOut = outcome.RunID
	// Also closes the window for non-streamed outcomes before publication or
	// persistence; the stream callback normally acknowledged much earlier.
	if foreground.finish() {
		// The request went out and was billed even though nothing is shown for
		// it, so its usage still reaches the tracker through RunEndedMsg. Only
		// conversation rows are withdrawn; token accounting stays complete,
		// which is what the session totals and the prompt-cache hit-rate
		// numbers are computed from.
		runUsage.runID = strings.TrimSpace(runIDOut)
		if res != nil && res.Summary != nil {
			runUsage.in = res.Summary.Usage.InputTokens
			runUsage.out = res.Summary.Usage.OutputTokens
		}
		s.hydrateRunUsage(&runUsage)
		return nil
	}
	// Submit reports an approval gate as a pause with typed resume state; the
	// rest of this function still reasons in terms of that state, so unwrap it
	// back into the same shape the branches below expect.
	var rae *tool.RequiresActionError
	if r := requiresActionErrorFromOutcome(outcome); r != nil {
		rae = r
		err = rae
	}
	if errors.As(err, &rae) {
		runID := strings.TrimSpace(runIDOut)
		if runID == "" && rae != nil {
			runID = strings.TrimSpace(rae.RunID)
		}
		s.tuiWait(runID)
	}
	// No tuiFinish here: the executor owns the finished-transition and calls
	// it through RunExecutorOptions.Finish, so it has already run by now.
	completion = completeTurn(t0)
	runUsage.runID = strings.TrimSpace(runIDOut)
	if res != nil && res.Summary != nil {
		runUsage.in = res.Summary.Usage.InputTokens
		runUsage.out = res.Summary.Usage.OutputTokens
	}
	s.hydrateRunUsage(&runUsage)
	streamAssistantViaTUI := streamFlag != nil && *streamFlag
	if err != nil {
		var rae *tool.RequiresActionError
		if errors.As(err, &rae) && rae != nil && s.runSvc() != nil {
			rid := strings.TrimSpace(runIDOut)
			if rid == "" {
				rid = strings.TrimSpace(rae.RunID)
			}
			if rid != "" {
				s.persistRequiresActionSnapshot(sessionID, rid, rae.SessionSnapshot)
				s.attachRunWaitForRequiresAction(rid, sessionID, channel, input, rae, t0)
				s.emitPartialAssistantFromRAE(rae, streamAssistantViaTUI)
				emitRunEnded = shouldEmitRunEndedOnRequiresAction(true)
				if s.chatLog != nil {
					s.chatLog.Debugf("forebrain chat turn awaiting approval session=%s run_id=%s action_id=%s", sessionID, rid, rae.ActionID)
				}
				return nil
			}
		}
		if errors.Is(err, state.ErrSessionBusy) {
			// The session was refused: another live process owns its turn, so
			// no run of this message exists and none may be recorded for it.
			// The user row written on the way in comes back out the same way
			// Esc takes a message out of the conversation, and the caller
			// takes the submission back rather than reporting a failed run.
			if userRowID > 0 {
				if werr := s.sessStore().WithdrawUserTurn(durable, sessionID, userRowID); werr != nil && s.chatLog != nil {
					s.chatLog.Errorf("forebrain chat withdraw refused-turn row session=%s row_id=%d err=%v", sessionID, userRowID, werr)
				}
			}
			return err
		}
		s.clearPendingApproval()
		if errors.Is(err, context.Canceled) {
			if s.chatLog != nil {
				s.chatLog.Infof("forebrain chat turn cancelled session=%s elapsed=%s", sessionID, time.Since(t0))
			}
			// Persist what the user already saw: completed tool calls/results
			// (from the partial session capture) and the streamed partial
			// assistant text/reasoning. Without this a cancelled run loses every
			// message it produced - the next round and /resume only see the
			// pre-run user message.
			s.persistCancelledTurnOutcome(sessionID, runUsage.runID, completion)
			s.notifyUI(StreamResetMsg{turn: foreground})
			return nil
		}
		// Persist the partial session capture (completed tool calls/results
		// from the last successful iteration) so the next turn or /resume
		// does not lose context. Without this a transient LLM error (429,
		// network, etc.) drops every message the assistant produced in this
		// turn — the session store only has the user message, and the LLM
		// rebuilds context from scratch on the next attempt, losing the
		// work already done (tool calls executed, files read, etc.).
		s.persistCancelledTurnOutcome(sessionID, runUsage.runID, completion)
		// An explicit skill load that failed already showed its own Skill
		// failure card before the first LLM request; a generic error card
		// would repeat the same news twice.
		var skillLoadErr *run.ExplicitSkillLoadError
		if errors.As(err, &skillLoadErr) {
			if s.chatLog != nil {
				s.chatLog.Errorf("forebrain chat turn skill load failed session=%s skill=%s elapsed=%s err=%v", sessionID, skillLoadErr.SkillName, time.Since(t0), err)
			}
			return err
		}
		if s.chatLog != nil {
			s.chatLog.Errorf("forebrain chat turn error session=%s elapsed=%s err=%v", sessionID, time.Since(t0), err)
			s.chatLog.Debugf("forebrain chat turn error session=%s elapsed=%s err=%v", sessionID, time.Since(t0), err)
		}
		explained := llm.ExplainError(err)
		if !callerRendersReturnedError {
			s.notifyUI(NewMessageMsg{Msg: Message{
				Kind:      MsgKindError,
				Content:   explained,
				Timestamp: time.Now(),
			}})
		}
		s.persistRunTurnError(sessionID, runUsage.runID, explained)
		return err
	}
	s.clearPendingApproval()
	s.appendAssistantOutcome(sessionID, runIDOut, channel, input, completion, res, streamAssistantViaTUI, out)
	return nil
}

// IsFastMode reports the persisted /fast toggle for the active state.
// Reads $FOREBRAIN_HOME/state/fast/<session>.json once per session and caches
// the result; subsequent reads return the cached value until the session
// changes or SetFastMode is called. Returns false on any read error.
func (s *ChatSession) IsFastMode() bool {
	if s == nil {
		return false
	}
	sid := s.preferredSessionIDForFast()
	s.fastMu.RLock()
	if s.fastLoaded && s.fastSession == sid {
		v := s.fastEnabled
		s.fastMu.RUnlock()
		return v
	}
	s.fastMu.RUnlock()
	s.fastMu.Lock()
	defer s.fastMu.Unlock()
	if s.fastLoaded && s.fastSession == sid {
		return s.fastEnabled
	}
	st, err := state.Fast(s.stateRoot(), sid)
	if err != nil {
		s.fastSession = sid
		s.fastEnabled = false
		s.fastLoaded = true
		return false
	}
	s.fastSession = sid
	s.fastEnabled = st.Enabled
	s.fastLoaded = true
	return s.fastEnabled
}

// SetFastMode persists the /fast toggle for the active session and updates
// the in-memory cache. Caller is responsible for gating on model capability
// (Opus only) before invoking this method.
func (s *ChatSession) SetFastMode(enabled bool) error {
	if s == nil {
		return fmt.Errorf("chat session: nil")
	}
	sid := s.preferredSessionIDForFast()
	if err := state.SetFast(s.stateRoot(), sid, enabled); err != nil {
		return err
	}
	s.fastMu.Lock()
	s.fastSession = sid
	s.fastEnabled = enabled
	s.fastLoaded = true
	s.fastMu.Unlock()
	return nil
}

func (s *ChatSession) preferredSessionIDForFast() string {
	if s == nil {
		return "default"
	}
	sid := strings.TrimSpace(s.PreferredSurfaceTranscriptSessionID(context.Background()))
	if sid == "" {
		sid = "default"
	}
	return sid
}

// ReloadConfig applies the current config file immediately when no turn is
// running. Connect uses this explicit seam so a successful provider setup is
// reflected by the already-open session before the next message.
//
// A run in flight defers the reload the same way a file event does rather than
// dropping it: the caller has just rewritten the config, so leaving the session
// on the old one until something else happens to touch the file again would
// strand the change behind a best-effort watcher.
func (s *ChatSession) ReloadConfig() error {
	if s == nil {
		return fmt.Errorf("chat session unavailable")
	}
	// The environment owns the defer-while-running coordinator; the session
	// only decides whether a run is in flight, because its notion of "in
	// flight" spans the interactive approval loop that sits above the run.
	if s.tuiRunActive() {
		if err := s.Env.RequestConfigReload(); err != nil {
			return err
		}
		return fmt.Errorf("config reload deferred until the current task finishes")
	}
	return s.Env.RequestConfigReload()
}

// reloadConfigFromDisk re-reads the config file and republishes it to everything
// holding a view of it. configApplyMu serializes the whole read-apply sequence:
// the watcher's timer goroutine, /connect, and the config-writing slash commands
// all reach it, and two of them interleaving would leave the environment and
// the runner describing different files.
func (s *ChatSession) reloadConfigFromDisk() error {
	if s == nil {
		return fmt.Errorf("chat session unavailable")
	}
	if err := s.applyConfigFromDisk(); err != nil {
		return err
	}
	// Hand the applied config to the environment, which resolves the active
	// agent's state root from it for the run executor. This happens after
	// applyConfigFromDisk has released configApplyMu on purpose: reloadConfig
	// calls OnConfigReload, which takes configApplyMu, while holding reloadMu,
	// so acquiring reloadMu from under configApplyMu would invert the order.
	s.Env.AdoptConfig(s.cfg())
	return nil
}

// applyConfigFromDisk validates the file and swaps it into the session and the
// runner, rolling back together if the runner rejects it. The pointer swap
// and the rebuild live inside Runner.LoadConfig, under the runner's load
// lock; this function only adopts the config the runner accepted.
func (s *ChatSession) applyConfigFromDisk() error {
	s.configApplyMu.Lock()
	defer s.configApplyMu.Unlock()
	cfgPath, err := homepkg.ResolveConfigPath(strings.TrimSpace(s.home()))
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	loaded, err := appcfg.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load %s: %w", cfgPath, err)
	}
	safety.ApplyYOLO(&loaded)
	loaded = safety.EffectiveConfig(loaded, s.LaunchProject)
	if err := safety.NewManager().StartupCheck(&loaded); err != nil {
		return fmt.Errorf("sandbox startup check: %w", err)
	}
	deps := s.deps()
	if deps == nil {
		return fmt.Errorf("chat session has no environment")
	}
	// The runner shares this very Deps, so publishing here publishes to it too;
	// there is no second copy left to keep in step. MCPServers is deliberately
	// NOT taken from the reloaded file: the effective MCP list (global entries
	// plus gated, consented project entries) was resolved once for this
	// session and is frozen, so a file change lands in the next session and
	// /mcp reports it as pending. LoadConfig therefore only restores the
	// config pointer on failure.
	if r := s.runner(); r != nil {
		if rerr := r.LoadConfig(&loaded); rerr != nil {
			return fmt.Errorf("load runner: %w", rerr)
		}
	} else {
		deps.AppCfg = &loaded
	}
	s.adoptConfig(&loaded)
	if st := s.sessStore(); st != nil {
		st.SetMemoryMode(memory.SessionModeForConfig(s.cfg()))
	}
	return nil
}

// adoptConfig re-points the config pointers the session itself owns. A reload
// swaps run.Deps.AppCfg for a freshly loaded value rather than overwriting it
// in place, so every holder has to be told: the environment re-points its own
// hooks and the runner, and these are the TUI's.
func (s *ChatSession) adoptConfig(next *appcfg.Root) {
	if s == nil || next == nil {
		return
	}
	if s.userHooks != nil {
		s.userHooks.Cfg = next
	}
}

// reloadConfigAfterRun applies a deferred config reload when a TUI run
// completes. If a reload was deferred because a run was active, it applies it
// now. Safe to call unconditionally after every run end.
func (s *ChatSession) reloadConfigAfterRun() {
	if s == nil {
		return
	}
	if err := s.Env.ConfigIdle(); err != nil {
		slog.Error("config hot reload: apply", "err", err)
		// A deferred reload is invisible otherwise: nobody asked for it at this
		// moment, so a config that failed to apply under the session has to say
		// so rather than leave the old one running silently.
		s.notifyUI(NewMessageMsg{Msg: Message{
			Kind:      MsgKindError,
			Content:   "config reload failed: " + err.Error(),
			Timestamp: time.Now(),
		}})
	}
}

// PermissionPresets lists the built-in presets described under this
// session's settings, and names the one the session is running under, or ""
// when its approval mode and sandbox do not add up to one.
func (s *ChatSession) PermissionPresets(sessionID string) ([]safety.ApprovalPreset, string) {
	presets := safety.BuiltinApprovalPresets()
	for i := range presets {
		presets[i].Description = presets[i].DescriptionFor(s.cfg())
	}
	snap := s.runner().PermissionSnapshotForSession(sessionID)
	preset, ok := safety.MatchApprovalPreset(snap.Mode, safety.ConfigForSnapshot(s.cfg(), snap))
	if !ok {
		return presets, ""
	}
	return presets, preset.ID
}

// ApplyPermissionPreset switches the session to one of the built-in approval
// presets and reports the resulting state.
//
// Both halves of the preset are scoped to this session in the permission
// store; nothing is written to forebrain.yaml or to the live config. A preset
// picked to get through one task therefore governs neither the other
// conversations of this process nor the next session, and a reload of the
// config cannot undo it. Editing forebrain.yaml is what persisting a choice is
// for.
func (s *ChatSession) ApplyPermissionPreset(sessionID, presetID string) (string, error) {
	if s == nil || s.runner() == nil {
		return "", fmt.Errorf("permissions: unavailable")
	}
	preset, ok := safety.ApprovalPresetByID(presetID)
	if !ok {
		return "", fmt.Errorf("permissions: unknown preset %q", strings.TrimSpace(presetID))
	}
	for _, update := range preset.SessionUpdates(sessionID) {
		s.runner().ApplyPermissionUpdate(update)
	}
	cfg := safety.ConfigForSnapshot(s.cfg(), s.runner().PermissionSnapshotForSession(sessionID))
	return preset.Label + " for this session: " + preset.DescriptionFor(cfg), nil
}

type userShellExecution struct {
	cancel    context.CancelFunc
	auxiliary bool
}

func (s *ChatSession) RunSurfaceShellCommand(ctx context.Context, sessionID string, channel string, rawInput string) error {
	if s == nil {
		return nil
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		sid = "default"
	}
	ch := strings.TrimSpace(channel)
	if ch == "" {
		ch = "tui"
	}
	command := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rawInput), "!"))
	if command == "" {
		return nil
	}
	return s.runSurfaceUserShellCommand(ctx, sid, ch, command, rawInput)
}

func (s *ChatSession) runSurfaceUserShellCommand(ctx context.Context, sessionID string, channel string, command string, rawInput string) error {
	auxiliary := false
	if strings.EqualFold(channel, "tui") {
		auxiliary = s.tuiRunActive()
	}
	runCtx, cancel := context.WithCancel(ctx)
	exec := s.beginUserShellExecution(sessionID, cancel, auxiliary)
	defer s.endUserShellExecution(sessionID, exec)

	turnStartedAt := time.Now()
	if !auxiliary {
		s.notifyUI(RunStartedMsg{RunID: userShellRunID(command)})
		defer func() {
			completion := completeTurn(turnStartedAt)
			s.notifyUI(RunEndedMsg{
				RunID:          userShellRunID(command),
				WorkedDuration: completion.Duration,
			})
		}()
	}

	startedAt := time.Now()
	stepID := userShellStepID(command)
	s.notifyUI(NewMessageMsg{Msg: Message{
		Kind:      MsgKindTool,
		StepID:    stepID,
		ToolName:  "shell",
		Summary:   "running " + strings.TrimSpace(command),
		Timestamp: startedAt,
		ToolMeta: tool.ToolMeta{
			ToolName:   "shell",
			Status:     "running",
			Purpose:    "Run a shell command requested by the user.",
			Invocation: strings.TrimSpace(command),
			Input: map[string]any{
				"command": strings.TrimSpace(command),
			},
		},
	}})

	onOutput := func(_ safety.OutputStream, chunk []byte) {
		if len(chunk) == 0 {
			return
		}
		s.notifyUI(NewMessageMsg{Msg: Message{
			Kind:            MsgKindTool,
			StepID:          stepID,
			ToolName:        "shell",
			Content:         string(chunk),
			Summary:         "running " + strings.TrimSpace(command),
			ToolOutputDelta: true,
			Timestamp:       time.Now(),
			ToolMeta: tool.ToolMeta{
				ToolName:   "shell",
				Status:     "running",
				Purpose:    "Run a shell command requested by the user.",
				Invocation: strings.TrimSpace(command),
				Input:      map[string]any{"command": strings.TrimSpace(command)},
			},
		}})
	}
	res, meta, err := s.executeUserShellCommand(runCtx, command, onOutput)
	timing := res.ExecutionTiming
	body := formatUserShellResultForHistory(command, res, timing.StartedAt, timing.CompletedAt, err)
	if s.sessStore() != nil {
		// Each append upserts the session row in its own transaction.
		if strings.TrimSpace(rawInput) != "" {
			_, _ = s.sessStore().Append(context.Background(), sessionID, "user", strings.TrimSpace(rawInput))
		}
		metaJSON := mustMarshalUserShellMeta(meta)
		_, _ = s.sessStore().AppendShellCommandTurn(context.Background(), sessionID, body, metaJSON, timing)
	}

	content := renderUserShellResultForUI(res, err)
	s.notifyUI(NewMessageMsg{Msg: Message{
		Kind:      MsgKindTool,
		StepID:    stepID,
		ToolName:  "shell",
		Content:   content,
		Summary:   userShellSummary(command, res, err),
		Duration:  timing.Duration,
		Timestamp: timing.CompletedAt,
		ToolMeta:  meta,
	}})

	return err
}

func (s *ChatSession) executeUserShellCommand(ctx context.Context, command string, onOutput func(safety.OutputStream, []byte)) (safety.CommandResult, tool.ToolMeta, error) {
	if s != nil {
		s.refreshSandboxRuntime()
	}
	manager := safety.NewManager()
	if s != nil && s.sandbox() != nil {
		manager = s.sandbox()
	}
	cfg := s.cfg()
	workDir, _ := os.Getwd()
	env := homepkg.SafeSubprocessEnv(os.Environ(), homepkg.Options{
		ExplicitEnv: map[string]string{
			"FOREBRAIN_HOME":       strings.TrimSpace(s.home()),
			"FOREBRAIN_SESSION_ID": strings.TrimSpace(s.preferredSessionIDForFast()),
		},
	})
	res, decision, err := manager.RunCommand(ctx, cfg, safety.CommandRequest{
		ToolKind: safety.ToolKindUserShell,
		Command:  strings.TrimSpace(command),
		WorkDir:  workDir,
		Timeout:  time.Hour,
		Env:      env,
		OnOutput: onOutput,
	})
	meta := tool.ToolMeta{
		ToolName:   "shell",
		Status:     userShellStatus(err, res.ExitCode),
		Purpose:    "Run a shell command requested by the user.",
		Invocation: strings.TrimSpace(command),
		Input: map[string]any{
			"command":      strings.TrimSpace(command),
			"sandbox_mode": decision.ModeString(),
		},
	}
	return res, meta, err
}

func (s *ChatSession) beginUserShellExecution(sessionID string, cancel context.CancelFunc, auxiliary bool) *userShellExecution {
	if s == nil {
		return nil
	}
	s.tuiRunMu.Lock()
	defer s.tuiRunMu.Unlock()
	if s.tuiUserShells == nil {
		s.tuiUserShells = make(map[string][]*userShellExecution)
	}
	exec := &userShellExecution{cancel: cancel, auxiliary: auxiliary}
	s.tuiUserShells[strings.TrimSpace(sessionID)] = append(s.tuiUserShells[strings.TrimSpace(sessionID)], exec)
	return exec
}

func (s *ChatSession) endUserShellExecution(sessionID string, exec *userShellExecution) {
	if s == nil || exec == nil {
		return
	}
	s.tuiRunMu.Lock()
	defer s.tuiRunMu.Unlock()
	key := strings.TrimSpace(sessionID)
	items := s.tuiUserShells[key]
	if len(items) == 0 {
		return
	}
	next := items[:0]
	for _, item := range items {
		if item != exec {
			next = append(next, item)
		}
	}
	if len(next) == 0 {
		delete(s.tuiUserShells, key)
		return
	}
	s.tuiUserShells[key] = next
}

func mustMarshalUserShellMeta(meta tool.ToolMeta) string {
	if strings.TrimSpace(meta.ToolName) == "" {
		return ""
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(b)
}

func formatUserShellResultForHistory(command string, res safety.CommandResult, startedAt time.Time, finishedAt time.Time, err error) string {
	var out strings.Builder
	out.WriteString("<user_shell_command>\n")
	out.WriteString("<command>\n")
	out.WriteString(strings.TrimSpace(command))
	out.WriteString("\n</command>\n")
	out.WriteString("<result>\n")
	out.WriteString(fmt.Sprintf("Exit code: %d\n", res.ExitCode))
	out.WriteString(fmt.Sprintf("Duration: %.4f seconds\n", finishedAt.Sub(startedAt).Seconds()))
	body := strings.TrimSpace(strings.TrimSpace(res.Stdout + "\n" + res.Stderr))
	if body == "" && err != nil {
		body = strings.TrimSpace(err.Error())
	}
	if body != "" {
		out.WriteString("Output:\n")
		out.WriteString(body)
		out.WriteString("\n")
	}
	out.WriteString("</result>\n")
	out.WriteString("</user_shell_command>")
	return out.String()
}

func renderUserShellResultForUI(res safety.CommandResult, err error) string {
	body := strings.TrimSpace(strings.TrimSpace(res.Stdout + "\n" + res.Stderr))
	if body == "" && err != nil {
		return strings.TrimSpace(err.Error())
	}
	return body
}

func userShellSummary(command string, res safety.CommandResult, err error) string {
	if err != nil {
		return "failed: " + strings.TrimSpace(command)
	}
	if res.ExitCode != 0 {
		return fmt.Sprintf("ran %s · exit %d", strings.TrimSpace(command), res.ExitCode)
	}
	return "ran " + strings.TrimSpace(command)
}

func userShellStatus(err error, exitCode int) string {
	if err != nil || exitCode != 0 {
		return "failed"
	}
	return "completed"
}

func userShellRunID(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return "user-shell"
	}
	return "user-shell:" + command
}

func userShellStepID(command string) string {
	return userShellRunID(command) + ":tool"
}

type chatDiskLog struct {
	home string
	logs *telemetry.LevelLogger
}

func newChatDiskLog(home string) (*chatDiskLog, error) {
	dir := filepath.Join(home, homepkg.LogsDir)
	infoPath := filepath.Join(dir, "info.log")
	debugPath := filepath.Join(dir, "debug.log")
	logs, err := telemetry.OpenLevelLogger(infoPath, debugPath, telemetry.Options{})
	if err != nil {
		return nil, fmt.Errorf("chat logs: %w", err)
	}
	return &chatDiskLog{home: home, logs: logs}, nil
}

func (l *chatDiskLog) Close() error {
	if l == nil {
		return nil
	}
	return l.logs.Close()
}

func (l *chatDiskLog) Errorf(format string, args ...any) {
	if l == nil {
		return
	}
	err := fmt.Errorf(format, args...)
	telemetry.ErrorSkip(l.home, err, "chatDiskLog", 1)
}

func (l *chatDiskLog) Infof(format string, args ...any) {
	if l == nil {
		return
	}
	l.logs.Infof(format, args...)
}

func (l *chatDiskLog) Debugf(format string, args ...any) {
	if l == nil {
		return
	}
	l.logs.Debugf(format, args...)
}

func (l *chatDiskLog) DebugForceFlushf(format string, args ...any) {
	if l == nil {
		return
	}
	l.logs.Debugf(format, args...)
}

// --- panel support (/status, /mcp interactive panels) ---

// NotifyUIMessage posts one UI notification through the session's serialized
// dispatcher. The main loop wires it into surfaces that must reach the loop
// from outside its goroutine (approval sink → panel close).
func (s *ChatSession) NotifyUIMessage(msg any) {
	if s == nil {
		return
	}
	s.notifyUI(msg)
}

// PanelStatusReport assembles the /status projection for the panel.
func (s *ChatSession) PanelStatusReport(sessionID string) (turn.StatusReport, error) {
	cfg := mcpConfigFromChatSession(s)
	if cfg == nil || s.runner() == nil {
		return turn.StatusReport{}, errPanelUnavailable
	}
	return turn.BuildStatusReport(context.Background(), s.statusSource(cfg, sessionID, false)), nil
}

// PanelMCPInventory assembles the /mcp projection for the panel. It never
// waits on an MCP startup in flight.
func (s *ChatSession) PanelMCPInventory() (*turn.MCPInventory, error) {
	if s == nil || s.runner() == nil {
		return nil, errPanelUnavailable
	}
	inv := turn.BuildMCPInventory(s.mcpInventorySource())
	return &inv, nil
}

// PanelAuthenticateMCP runs the local OAuth flow for one server off the main
// loop, reporting the URL to open and then the outcome as MCPAuthMsg.
func (s *ChatSession) PanelAuthenticateMCP(serverName string) {
	go func() {
		url, err := s.startMCPLocalOAuth(serverName, func(result string) {
			s.notifyUI(MCPAuthMsg{Server: serverName, Result: result})
		})
		if err != nil {
			s.notifyUI(MCPAuthMsg{Server: serverName, Result: err.Error()})
			return
		}
		s.notifyUI(MCPAuthMsg{Server: serverName, URL: url})
	}()
}

// PanelSetMCPDisabled records a server's Disable/Enable choice for the next
// session in the agent's store, the same action the web runs. The user's YAML
// is never rewritten, and this session's servers and tool table do not move.
func (s *ChatSession) PanelSetMCPDisabled(serverName string, disable bool) (string, error) {
	r := s.runner()
	if r == nil {
		return "", errPanelUnavailable
	}
	return turn.SetMCPServerDisabled(r.WorkspaceRoot, r.MCPServers, r.MCPDisabled, serverName, disable)
}

// PanelListMCPResources fetches one server's resource list off the main loop
// and reports it as MCPResourcesMsg.
func (s *ChatSession) PanelListMCPResources(serverName string) {
	r := s.runner()
	go func() {
		msg := MCPResourcesMsg{Server: serverName}
		reg := r.MCPRegistry()
		if reg == nil {
			msg.Err = "mcp: no live connection"
			s.notifyUI(msg)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		items, err := reg.ResourceInfos(ctx, serverName)
		if err != nil {
			msg.Err = err.Error()
		}
		msg.Items = items
		s.notifyUI(msg)
	}()
}

// SubscribeMCPStatusTick forwards MCP startup state changes to the UI loop so
// an open /mcp panel repaints in place. The callback only posts a message; it
// never blocks the registry. The tool table is published after the last
// server settles, so a settled snapshot also posts one more tick once the
// table is there — the panel's tool counts would otherwise wait for a key.
func (s *ChatSession) SubscribeMCPStatusTick() (func(), bool) {
	r := s.runner()
	if r == nil {
		return nil, false
	}
	cancel := r.MCPStartup().Subscribe(func(snap run.MCPSnapshot) {
		s.notifyUI(MCPStatusTickMsg{})
		if !snap.Pending {
			go func() {
				_ = r.LoadedTools()
				s.notifyUI(MCPStatusTickMsg{})
			}()
		}
	})
	return cancel, true
}

var errPanelUnavailable = errors.New("panel unavailable")

// lspControl is the /lsp panel's slice of the session's runner: nil when this
// runtime has no language servers.
func (s *ChatSession) lspControl() tool.CodeIntelControl {
	r := s.runner()
	if r == nil {
		return nil
	}
	return r.CodeIntelControl
}

// PanelLSPSnapshot answers what /lsp shows. The snapshot never blocks on a
// probe or an install: both report through SubscribeLSPStatus.
func (s *ChatSession) PanelLSPSnapshot() (event.LSPSnapshot, error) {
	ctl := s.lspControl()
	if ctl == nil {
		return event.LSPSnapshot{}, errPanelUnavailable
	}
	return ctl.Snapshot(), nil
}

// PanelSetLSPEnabled enables or disables one server. The reply is the sentence
// the panel shows; the control plane owns what the change does.
func (s *ChatSession) PanelSetLSPEnabled(serverID string, enabled bool) (string, error) {
	ctl := s.lspControl()
	if ctl == nil {
		return "", errPanelUnavailable
	}
	if err := ctl.SetEnabled(serverID, enabled); err != nil {
		return "", err
	}
	if enabled {
		return "Enabled. Diagnostics start with the next edit; the lsp tool appears in new sessions.", nil
	}
	return "Disabled. Its instances in this project have stopped.", nil
}

// PanelRestartLSP restarts one server's instances in this project.
func (s *ChatSession) PanelRestartLSP(serverID string) (string, error) {
	ctl := s.lspControl()
	if ctl == nil {
		return "", errPanelUnavailable
	}
	if err := ctl.Restart(serverID); err != nil {
		return "", err
	}
	return "Restarting…", nil
}

// PanelInstallLSP starts the server's install recipe off the main loop. Every
// call returns at once; output and outcome arrive through the snapshot.
func (s *ChatSession) PanelInstallLSP(serverID string) (string, error) {
	ctl := s.lspControl()
	if ctl == nil {
		return "", errPanelUnavailable
	}
	command := ""
	for _, srv := range ctl.Snapshot().Servers {
		if srv.ID == serverID {
			command = srv.InstallCommand
			break
		}
	}
	go func() { _ = ctl.Install(context.Background(), serverID, nil) }()
	return "Installing: " + command, nil
}

// PanelResetLSPRecommendations turns recommendations back on.
func (s *ChatSession) PanelResetLSPRecommendations() (string, error) {
	ctl := s.lspControl()
	if ctl == nil {
		return "", errPanelUnavailable
	}
	if err := ctl.ResetRecommendations(); err != nil {
		return "", err
	}
	return "Recommendations are back on.", nil
}

// SubscribeLSPStatus forwards language-server snapshot changes to the UI loop
// so an open /lsp panel repaints in place. The callback only posts a message.
func (s *ChatSession) SubscribeLSPStatus() (func(), bool) {
	ctl := s.lspControl()
	if ctl == nil {
		return nil, false
	}
	return ctl.Subscribe(func(event.LSPSnapshot) { s.notifyUI(LSPStatusTickMsg{}) }), true
}

// DecideLSPRecommendation applies the user's answer and returns the line
// the transcript shows for it ("" when there is nothing to say).
func (s *ChatSession) DecideLSPRecommendation(rec event.LSPRecommendation, choice event.LSPRecommendationChoice) (string, error) {
	ctl := s.lspControl()
	if ctl == nil {
		return "", errPanelUnavailable
	}
	if choice == event.LSPChoiceInstall {
		// Decide first, watch after: the decision is what starts the
		// install, and the watcher's subscription must never observe the
		// pre-start snapshot of a retry (a previous attempt's failure).
		if err := ctl.DecideRecommendation(rec.ID, choice); err != nil {
			return "", err
		}
		s.watchLSPInstall(ctl, rec)
		return "Installing " + rec.DisplayName + ": " + rec.InstallCommand, nil
	}
	if err := ctl.DecideRecommendation(rec.ID, choice); err != nil {
		return "", err
	}
	switch choice {
	case event.LSPChoiceEnable:
		return rec.DisplayName + " enabled for " + strings.Join(rec.Languages, ", ") + ". Diagnostics start with the next edit.", nil
	case event.LSPChoiceNotNow:
		if ctl.Snapshot().RecommendationsDisabled {
			return "Language server recommendations are now off (dismissed 5 times in a row). Turn them back on in /lsp.", nil
		}
		return "Not now. No language server will be suggested again in this session.", nil
	case event.LSPChoiceNever:
		return rec.ServerID + " will not be suggested again. /lsp can still enable it.", nil
	case event.LSPChoiceDisableAll:
		return "Language server recommendations are off. Turn them back on in /lsp.", nil
	}
	return "", nil
}

// watchLSPInstall follows the install a recommendation started: every new
// output line becomes the transient status line, and the outcome — the
// enabled line or the failure tail — becomes one transcript frame. The
// snapshot is the same one every surface reads, so the /lsp panel shows the
// same install this reports. The returned stop ends the watch early.
func (s *ChatSession) watchLSPInstall(ctl tool.CodeIntelControl, rec event.LSPRecommendation) (stop func()) {
	var (
		lastLine   string
		wasRunning bool
		done       bool
		mu         sync.Mutex
	)
	var cancel func()
	cancel = ctl.Subscribe(func(snap event.LSPSnapshot) {
		mu.Lock()
		defer mu.Unlock()
		if done {
			return
		}
		for _, srv := range snap.Servers {
			if srv.ID != rec.ServerID {
				continue
			}
			if srv.Installing {
				wasRunning = true
				if n := len(srv.InstallLog); n > 0 && srv.InstallLog[n-1] != lastLine {
					lastLine = srv.InstallLog[n-1]
					s.notifyUI(LSPInstallProgressMsg{ServerID: srv.ID, Line: lastLine})
				}
				return
			}
			if srv.InstallError != "" && !wasRunning {
				// The install finished (or never truly started) between two
				// snapshots: the failure it carries is already terminal. Report
				// it without having seen Installing — otherwise the watch would
				// wait for an end that already happened.
				done = true
				cancel()
				s.notifyUI(LSPInstallDoneMsg{ServerID: srv.ID, Err: srv.InstallError})
				return
			}
			if wasRunning {
				done = true
				cancel()
				if srv.InstallError != "" {
					s.notifyUI(LSPInstallDoneMsg{ServerID: srv.ID, Err: srv.InstallError})
				} else {
					s.notifyUI(LSPInstallDoneMsg{ServerID: srv.ID, Text: rec.DisplayName + " installed and enabled for " + strings.Join(rec.Languages, ", ") + ". Diagnostics start with the next edit."})
				}
			}
			return
		}
	})
	return func() {
		mu.Lock()
		done = true
		mu.Unlock()
		cancel()
	}
}
