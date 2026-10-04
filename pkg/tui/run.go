// The interactive run loop and its output helpers.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	statepkg "github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

var tuiReadInputEvents = readInputEvents

func Run(ctx context.Context, opts Options) error {
	if opts.Session == nil {
		return fmt.Errorf("stream terminal requires a chat session")
	}
	in := opts.In
	if in == nil {
		in = os.Stdin
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	errOut := opts.Err
	if errOut == nil {
		errOut = os.Stderr
	}
	out = newTTYNewlineWriter(out)
	errOut = newTTYNewlineWriter(errOut)
	var ttySession *rawTerminalSession
	var cancelInput context.CancelFunc
	// exitSessionID is captured by exit paths and printed after the
	// alt-screen is torn down so the hint lands on the normal screen.
	var exitSessionID string
	// sessionTouched tracks whether at least one user turn was dispatched.
	// A fresh session that was never used is not persisted in the store, so
	// offering a resume command for it would be misleading.
	var sessionTouched bool

	t := ResolveDiffTheme()
	ApplyDiffTheme(t)

	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		if tty, ttyErr := newRawTerminalSession(f); ttyErr == nil && tty != nil {
			ttySession = tty
			_, _ = fmt.Fprint(out, enableBracketedPasteSeq+enableFocusReportingSeq+setComposerCursorColorSeq+enableMouseSeq)
			defer func() {
				shutdownTerminalInput(out, ttySession, cancelInput)
			}()
		}
	}
	if ttySession == nil {
		return fmt.Errorf("tui requires an interactive terminal")
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	// Wrap stdout/stderr in a shared syncWriter so concurrent producers
	// (renderer and transient status goroutine) never interleave bytes on the
	// terminal fd. Single mutex shared across all writers.
	outSync := newSyncWriter(out)
	var errSync io.Writer = outSync
	if errOut != out {
		errSync = newSyncWriter(errOut)
	}
	// The animated terminal window/tab title shares the synced stdout so its
	// OSC writes never interleave with renderer output. Cleared on exit so
	// the emulator falls back to its own title.
	titleAnimator := newTerminalTitleAnimator(outSync)
	defer titleAnimator.Clear()

	// Resolve and activate the initial session's model before any startup
	// chrome is drawn: the footer names the model every turn runs on, and the
	// token budget is sized by its context window, so both must read the
	// activated runner, not the pre-restore one.
	initialSessionID := strings.TrimSpace(opts.InitialSessionID)
	var initialSessionTitle string
	var resumeWarning string
	if initialSessionID != "" {
		resumedID, resumedTitle, warning, err := opts.Session.ResumeSession(ctx, initialSessionID)
		if err != nil {
			return err
		}
		if sid := strings.TrimSpace(resumedID); sid != "" {
			initialSessionID = sid
		}
		initialSessionTitle = strings.TrimSpace(resumedTitle)
		if initialSessionTitle == initialSessionID {
			initialSessionTitle = ""
		}
		resumeWarning = strings.TrimSpace(warning)
	}
	if initialSessionID == "" {
		initialSessionID = strings.TrimSpace(opts.Session.NewSessionID("cli"))
	}

	renderer := NewRenderer(outSync, errSync).WithDiffTheme(t).WithWorkingDir(strings.TrimSpace(opts.WorkingDirectory))
	renderer.SetComposerFooter(ComposerFooter{
		Model:           summarizeComposerModel(opts.Session),
		ReasoningEffort: summarizeComposerReasoningEffort(opts.Session),
		Directory:       startupWorkingDirectoryDisplay(opts.WorkingDirectory),
	})
	renderer.SetSubagentModels(subagentModelsByType(opts.Session))
	renderer.SetComposerTokenStats(initialComposerTokenStats(opts.Session))
	startupInfo := StartupInfo{
		Version:   strings.TrimSpace(opts.Version),
		Directory: startupWorkingDirectoryDisplay(opts.WorkingDirectory),
	}
	state := streamState{
		sessionID:       initialSessionID,
		terminalFocused: true,
		home:            strings.TrimSpace(opts.Home),
		workspaceRoot:   strings.TrimSpace(opts.WorkspaceRoot),
		workingDir:      strings.TrimSpace(opts.WorkingDirectory),
		startupInfo:     startupInfo,
		session:         opts.Session,
		titleAnimator:   titleAnimator,
	}
	// Seed the idle terminal title with the resumed session's title (or the
	// app name for a fresh session); turns animate it from here on.
	titleAnimator.Settle(initialSessionTitle)
	state.agentRoster = opts.Session.AgentRosterSnapshot(state.sessionID)
	if state.workingDir == "" {
		if wd, err := os.Getwd(); err == nil {
			state.workingDir = wd
		}
	}
	setMentionRoot(state.workingDir)
	go turn.Prewarm(state.workingDir)
	if planModeRequiredFromEnv() && strings.TrimSpace(state.stateRoot()) != "" && strings.TrimSpace(state.sessionID) != "" {
		if cur, _ := statepkg.Get(state.stateRoot(), state.sessionID); cur.Mode != statepkg.ModePlan {
			_, _ = statepkg.Switch(state.stateRoot(), state.sessionID, statepkg.ModePlan)
		}
	}
	syncPlanModeIndicator(renderer, &state)
	// The resumed session's model may have fallen back to the configured
	// default; say so once the startup chrome this note lives under is ready.
	if resumeWarning != "" {
		renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "model", Content: resumeWarning, Final: true})
	}

	historyStore := newRawInputHistoryStore(opts.InputHistoryPath)
	state.inputHistoryStore = historyStore
	inputCtx, stopInput := context.WithCancel(ctx)
	cancelInput = stopInput
	events := tuiReadInputEvents(inputCtx, in, outSync, ttySession, historyStore)
	readLine := func(ctx context.Context) (string, error) {
		for {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case ev, ok := <-events:
				if !ok {
					return "", io.EOF
				}
				if ev.kind == inputEventDone {
					if ev.err != nil {
						return "", ev.err
					}
					return "", io.EOF
				}
				if ev.kind == inputEventLine {
					return ev.line, nil
				}
			}
		}
	}
	selector := NewSelector(out, readLine)
	if f, ok := in.(*os.File); ok {
		selector = NewInteractiveSelector(out, readLine, f, renderer)
	}
	cmds := newCommandController(opts.Session, renderer, selector, readLine, strings.TrimSpace(opts.Home))
	approvalSink := NewInteractiveApprovalSinkWithTTY(out, selector).WithRenderer(renderer).WithRecorder(opts.Session)
	if notifier, ok := opts.Session.(interface{ NotifyUIMessage(any) }); ok {
		approvalSink.notify = notifier.NotifyUIMessage
	}
	cmds.openPanel = func(kind string) bool {
		if !renderer.viewportMode {
			return false
		}
		opened := false
		switch kind {
		case "status":
			opened = openStatusPanel(&state)
		case "mcp":
			opened = openMCPPanel(&state)
		case "lsp":
			opened = openLSPPanel(&state)
		}
		// Painted at once: a panel opened during a run has no idle-loop
		// iteration coming to draw it.
		if opened {
			drawUIPanel(renderer, &state)
		}
		return opened
	}
	opts.Session.SetToolApprovalSink(approvalSink)

	tracker := NewTracker()

	var reducer Reducer
	reducer.WithTracker(tracker)
	var turnWorkedStatus string

	// UI notifications are serialized through a queue and drained on the main
	// event loop. Producers (streaming tokens, tool events, subagent lifecycle)
	// deliver from whatever goroutine they run on; without serialization here
	// they race concurrently on the reducer (no mutex) and streamState (no
	// mutex), corrupt internal state, and can simultaneously trigger modal
	// selectors that deadlock competing for stdin.
	// Routing through the queue guarantees every notification is processed on
	// the single main-loop goroutine — the same goroutine that owns state and
	// the reducer — eliminating the data races and the selector deadlock.
	notifyQ := event.New[any]()
	notifyCh := notifyQ.Start(ctx)
	// The MCP startup line, which is driven by status messages but has to keep
	// its own clock: the snapshot only changes when a server does, and a line
	// that says "0s" for the thirty seconds a server takes to answer is worse
	// than one that says nothing.
	mcpLine := &mcpStatusLine{}
	processUINotification := func(m any) {
		switch msg := m.(type) {
		case PanelCloseMsg:
			if state.panel != nil {
				closeUIPanel(renderer, &state)
				renderComposerWithState(renderer, &state)
			}
			if msg.Done != nil {
				close(msg.Done)
			}
			return
		case MCPStatusTickMsg:
			if state.panel != nil && state.panel.kind == "mcp" {
				state.panel.refreshInventory()
				drawUIPanel(renderer, &state)
			}
			return
		case LSPStatusTickMsg:
			if state.panel != nil && state.panel.kind == "lsp" {
				state.panel.refreshLSP()
				drawUIPanel(renderer, &state)
			}
			return
		case MCPAuthMsg:
			if state.panel != nil {
				state.panel.applyAuth(msg)
				drawUIPanel(renderer, &state)
			}
			return
		case MCPResourcesMsg:
			if state.panel != nil {
				state.panel.applyResources(msg)
				drawUIPanel(renderer, &state)
			}
			return
		}
		if handleAutoContinueNotification(renderer, &state, m) {
			return
		}
		ev := reducer.Reduce(m)
		shouldRefreshWorking := false
		rosterChanged := false
		// A subagent's own view outlives its roster row, so the renderer is told
		// what agent it belongs to directly rather than inferring it from the
		// roster.
		if msg, ok := m.(SubagentSpawnedMsg); ok {
			renderer.NoteSubagentSpawned(msg.AgentID, msg.AgentType)
		}
		// The model entering or leaving Plan mode moves the footer at once,
		// not only when its turn ends.
		if nm, ok := m.(NewMessageMsg); ok && nm.Msg.ToolPhase == event.RunEventToolCompleted {
			if name := strings.TrimSpace(nm.Msg.ToolName); strings.EqualFold(name, "enter_plan_mode") || strings.EqualFold(name, "exit_plan_mode") {
				syncPlanModeIndicator(renderer, &state)
			}
		}
		switch m.(type) {
		case TokenUsageDeltaMsg:
			shouldRefreshWorking = true
		case RunEndedMsg:
			state.refreshAgentRoster(&reducer)
			if !agentRosterHasRunningSubagent(state.agentRoster) {
				state.agentRosterFocused = false
			}
			rosterChanged = true
		case SubagentSpawnedMsg:
			state.refreshAgentRoster(&reducer)
			rosterChanged = true
		case SubagentEndedMsg:
			state.refreshAgentRoster(&reducer)
			// Completion changes lifecycle state, not navigation. Keep a user
			// who is reading this agent in its alt-screen VM; Escape/roster/card
			// navigation still returns to the primary conversation.
			rosterChanged = true
		}
		// A switched result's success frames render only after the switch
		// succeeded, in the session they describe; a failed activation discards
		// them so the surface never says the user is somewhere they are not.
		holdFramesForSwitch := strings.TrimSpace(ev.SwitchSessionID) != ""
		if !holdFramesForSwitch {
			for _, frame := range ev.Frames {
				renderer.RenderFrame(frame)
			}
		}
		// When a run is aborted (user cancels approval, Ctrl+C, etc.) the
		// reducer's StreamResetMsg clears the assistant buffer but emits no
		// frames. Any in-flight tool frame (running / awaiting approval) is
		// now stale - finalize it so the transcript shows "Canceled" instead
		// of a perpetual "Running" block.
		if reset, ok := m.(StreamResetMsg); ok && !reset.turn.isWithdrawn() && renderer != nil {
			renderer.FinalizePendingTools()
		}
		if ev.WorkedStatus != "" {
			turnWorkedStatus = ev.WorkedStatus
			// Clear the live "Working" transient line before appending the
			// persistent "Worked for" status frame; otherwise both messages are
			// visible simultaneously at the end of a turn. Only the working
			// source is ended: an MCP startup still running keeps its line.
			renderer.FinishTransientStatus(transientSourceWorking)
			// Append to viewport history so "Worked for" persists
			// across turns and is visible in scrollback.
			renderer.RenderFrame(Frame{Kind: FrameStatus, Title: ev.WorkedStatus, Final: true})
			// The reducer reconciled session token totals in ObserveRunEndForRun
			// just before producing WorkedStatus. Refresh the composer footer's
			// in/out figures so they stay in lockstep with the "Worked for" line.
			// Without this the footer lags behind because TokenBudgetUpdatedMsg
			// (which otherwise drives footer updates) arrives *before* RunEndedMsg,
			// capturing the pre-reconciliation stream deltas instead of the final
			// authoritative provider usage.
			sess := tracker.SnapshotSession()
			renderer.RefreshComposerTokenUsage(sess.InputTokens, sess.OutputTokens)
		}
		if ev.ComposerTokenStats != nil {
			stats := *ev.ComposerTokenStats
			snap := tracker.SnapshotSession()
			stats.InputTokens = snap.InputTokens
			stats.OutputTokens = snap.OutputTokens
			renderer.SetComposerTokenStats(stats)
		}
		if steerMsg, ok := m.(PendingSteersChangedMsg); ok {
			if sid := strings.TrimSpace(steerMsg.SessionID); sid == "" || sid == state.sessionID {
				if ch := strings.TrimSpace(steerMsg.Channel); ch == "" || ch == "tui" {
					// Steers just delivered to the model move from the pending
					// preview into the transcript: drop them from the queue
					// preview and render them as user-history messages so they
					// don't silently disappear.
					renderDeliveredSteerMessages(renderer, state.reconcilePendingSteersCount(steerMsg.Count))
					renderComposerWithState(renderer, &state)
				}
			}
		}
		// MCP startup owns its own transient line for as long as it is running:
		// it is the reason the first frame is up before the servers are, and the
		// last thing the reader is waiting on before the model can answer.
		// Ending it is driven by the same state that installed it, so the line
		// disappears exactly when the last server settles.
		if mcpStatus, ok := m.(MCPStatusMsg); ok {
			mcpLine.apply(renderer, mcpStatus)
		}
		if shouldRefreshWorking {
			// Keep the composer footer's in/out in lockstep with the working
			// line. Both read the same per-turn token counters, but the footer
			// otherwise only refreshes on the (less frequent) budget message, so
			// it trailed the working line mid-turn. PercentLeft is preserved.
			// The working line itself needs no nudge: it is rendered from the
			// tracker on every paint.
			sess := tracker.SnapshotSession()
			renderer.RefreshComposerTokenUsage(sess.InputTokens, sess.OutputTokens)
		}
		if rosterChanged {
			renderComposerWithState(renderer, &state)
		}
		if sid := strings.TrimSpace(ev.SwitchSessionID); sid != "" {
			switched, _, _ := switchStreamSession(ctx, &state, renderer, tracker, sid)
			if switched {
				for _, frame := range ev.Frames {
					renderer.RenderFrame(frame)
				}
			}
		}
		// A modal selection changes the settings the next turn will run under
		// (session, model, permissions, skills). Suppress queue autosend for the
		// duration so a queued follow-up cannot be drained into a turn that uses
		// settings the user is still choosing. Resume draining only after the
		// modal has been applied or dismissed.
		// These notifications are processed on the main loop, so a selection
		// raised mid-run resolves before the drain either way; the guard keeps
		// that ordering an explicit invariant instead of an incidental one.
		if ev.RequestSessionSelect || ev.RequestPicker != nil || ev.RequestPermissionMgmt || ev.RequestSkillSelect {
			state.deferQueueAutosendUntilSelectionApplied()
			// Released unconditionally: a stuck suppression would stall the queue
			// for the rest of the state.
			defer state.resumeQueueAutosend()
			if ev.RequestSessionSelect {
				if selected, ok := cmds.handleResume(context.Background(), state.sessionID); ok {
					switchStreamSession(context.Background(), &state, renderer, tracker, selected)
					cmds.printSessionResumeContext(selected)
				}
			}
			if ev.RequestPicker != nil {
				// Like the skills menu below, a pick that produces work is
				// queued and drained like any other follow-up.
				if _, continueRun, sub, _ := runSlashPicker(ctx, cmds, renderer, tracker, &state, ev.RequestPicker); continueRun && len(sub.Parts) > 0 {
					state.enqueueTurn(sub, queuedSubmissionActionTurn)
				}
			}
			if ev.RequestPermissionMgmt {
				_ = cmds.handlePermissions(state.sessionID)
			}
			if ev.RequestSkillSelect {
				// This path has no direct turn to return into, so an action
				// that produces work (running a skill, handing off to the
				// workshop) is queued and drained like any other follow-up.
				if sub, ok := cmds.handleSkills(ctx); ok {
					state.enqueueTurn(sub, queuedSubmissionActionTurn)
				}
			}
		}
		if ev.QuitRequested {
			state.quitRequested = true
		}
		// Migration runs on its own goroutine and speaks through these
		// three notifications only. Progress paints the transient status
		// line; the preview asks for confirmation; the done notice renders
		// the report the session already persisted (shown implies recorded).
		switch migration := m.(type) {
		case MigrationProgressMsg:
			line := migration.Stage
			if migration.Total > 0 {
				line = fmt.Sprintf("%s %d/%d", migration.Stage, migration.Done, migration.Total)
			}
			if detail := strings.TrimSpace(migration.Detail); detail != "" {
				line += " · " + detail
			}
			renderer.RenderTransientStatus(transientSourceMigrate, "migrate: "+line)
		case MigrationPreviewMsg:
			renderer.FinishTransientStatus(transientSourceMigrate)
			planText := strings.TrimSpace(migration.Plan)
			var confirm bool
			var err error
			if overlay, ok := selector.(InfoOverlaySelector); ok {
				confirm, err = overlay.ConfirmInfo("Migration preview", planText, "import")
			} else {
				renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "Migration preview", Content: planText, Final: true})
				confirm, _, err = selector.Confirm("Import now?", true)
			}
			switch {
			case err != nil:
				renderer.PrintError(err)
			case !confirm || migration.Run == nil:
				renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "migrate", Content: "Migration cancelled; nothing was imported.", Final: true})
			default:
				renderer.RenderTransientStatus(transientSourceMigrate, "migrate: importing…")
				migration.Run()
			}
		case MigrationDoneMsg:
			// The report stays in the conversation, exactly as /resume
			// replays it: a modal would take it away with the next key.
			renderer.FinishTransientStatus(transientSourceMigrate)
			title := strings.TrimSpace(migration.Title)
			if migration.Err != nil {
				renderer.RenderFrame(Frame{Kind: FrameError, Title: title, Content: migration.Err.Error(), Final: true})
				return
			}
			renderer.RenderFrame(migrationReportFrame(title, migration.Report))
		}
		// A language-server recommendation is a modal like the migration
		// preview: it can open mid-turn, the agent keeps working, and queued
		// input waits until the answer is applied. The install it can start
		// owns the transient line for as long as it runs.
		switch lspMsg := m.(type) {
		case LSPRecommendationMsg:
			if decider, ok := state.session.(lspRecommendationDecider); ok {
				handleLSPRecommendation(renderer, selector, &state, decider, lspMsg.Rec)
			}
		case LSPInstallProgressMsg:
			renderer.RenderTransientStatus(transientSourceLSP, "lsp: installing "+lspMsg.ServerID+" · "+lspMsg.Line)
		case LSPInstallDoneMsg:
			renderer.FinishTransientStatus(transientSourceLSP)
			if lspMsg.Err != "" {
				renderer.RenderFrame(Frame{Kind: FrameError, Title: lspMsg.ServerID + " install failed", Content: lspMsg.Err, Final: true})
				return
			}
			if lspMsg.Text != "" {
				renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "lsp", Content: lspMsg.Text, Final: true})
			}
		}
		// A skill install runs in the background and owns one card in the
		// transcript: every checkpoint replaces it, and the finished install
		// replaces it a last time with its own result. Nothing about it
		// touches the composer, which stays live for the whole install.
		switch install := m.(type) {
		case SkillInstallProgressMsg:
			renderer.RenderFrame(Frame{
				Kind:     FrameSkillInstall,
				StepID:   strings.TrimSpace(install.ID),
				Title:    strings.TrimSpace(install.SourceRef),
				Summary:  "installing",
				Content:  strings.TrimSpace(install.Phase),
				Duration: install.Elapsed,
			})
		case SkillInstallDoneMsg:
			frame := Frame{
				Kind:     FrameSkillInstall,
				StepID:   strings.TrimSpace(install.ID),
				Title:    strings.TrimSpace(install.SourceRef),
				Summary:  strings.TrimSpace(install.Summary),
				Content:  strings.TrimSpace(install.Report),
				Duration: install.Elapsed,
				Final:    true,
			}
			if install.Err != nil {
				// The card body is the error exactly as it was reported. An
				// install fails for reasons only the underlying tool can name
				// — a private repository, a proxy, a bad reference — so the
				// surface adds nothing to it.
				frame.Summary = skillInstallFailedSummary
				frame.Content = strings.TrimSpace(install.Err.Error())
			}
			renderer.RenderFrame(frame)
		}
	}
	opts.Session.PrependUINotify(func(m any) {
		// Lossless enqueue. This is the ONLY entry point from the agent run
		// goroutines into the UI main loop. Dropping here loses control
		// messages (compact banner, footer budget refresh) that have no
		// later "next one will refresh" recovery - compaction fires once
		// per threshold crossing, so a dropped ContextCompactedMsg leaves
		// the footer stuck on the pre-compact budget forever. Push never
		// blocks on the consumer, so the agent run is not stalled.
		notifyQ.Push(m)
	})

	// The MCP startup watch is armed strictly after the notify sink exists. Its
	// subscription replays the generation's current state, and that replay is
	// the only delivery a startup in flight will produce until something changes
	// — a startup that takes a minute produces nothing else for a minute — so a
	// replay delivered before there is anywhere to deliver it is a status line
	// that simply never appears.
	watchMCPStartup := func(sessionID string) {
		if watcher, ok := opts.Session.(mcpStartupWatcher); ok {
			watcher.WatchMCPStartup(sessionID)
		}
	}
	watchMCPStartup(initialSessionID)

	// Enter the alt-screen virtual viewport for interactive terminals BEFORE
	// seeding the banner/startup info/hint and replaying any resumed
	// transcript, so all of that content lands inside the viewport (visible at
	// startup and scrollable) rather than on the normal scrollback that the
	// alt-screen hides. Non-TTY runs skip this and keep the plain append stream.
	if ttySession != nil {
		// Switch to a software-rendered caret so it keeps blinking during
		// streaming. Terminal.app/iTerm2 freeze the hardware caret solid on
		// continuous output; the software cursor bypasses that mechanism.
		// It is chosen before the viewport's first paint, which would
		// otherwise show the hardware caret for a frame.
		renderer.EnableSoftwareCursor()
		renderer.EnableViewportMode()
		clipProbe, _ := opts.Clipboard.(ClipboardImageProbe)
		if opts.Clipboard == nil {
			clipProbe = systemClipboardImageReader{}
		}
		if clipProbe != nil {
			watchCtx, stopWatch := context.WithCancel(ctx)
			defer stopWatch()
			state.clipboardWatch = newClipboardImageWatcher(clipProbe, renderer)
			go state.clipboardWatch.run(watchCtx)
		}
		// Print resume hint AFTER DisableViewportMode exits the alt-screen
		// buffer; writing into the alt-screen is discarded when it closes.
		defer func() {
			if sessionTouched {
				printResumeHint(out, exitSessionID)
			}
		}()
		defer renderer.DisableViewportMode()
		defer func() {
			persistSurfaceBrowseState(opts.Session, renderer, state.sessionID)
		}()
		watchTerminalResize(inputCtx, renderer)
	}

	// Seed the welcome chrome into the viewport now that it owns the screen.
	// In non-viewport runs these fall back to inline writes.
	renderer.Banner(startupInfo)
	renderer.StartupInfo(startupInfo)
	renderer.StartupHint()

	if strings.TrimSpace(opts.InitialSessionID) != "" {
		cmds.printSessionResumeContext(state.sessionID)
	}

	// After the replay, so an approval this session left unfinished is reported
	// below the history it belongs to rather than above it.
	opts.Session.StartApprovalRecovery(state.sessionID)

	// pendingWheelNonWheel holds a non-wheel event drained from the events
	// channel during mouse-wheel coalescing (see drainWheelCoalesce). It is
	// processed on the next idle-loop iteration instead of calling
	// awaitInputOrSignal again.
	var pendingNonWheel inputEvent
	var hasPendingNonWheel bool

	for {
		if state.quitRequested {
			exitSessionID = state.sessionID
			return nil
		}
		// Drain any notifications that arrived while the loop was processing
		// the previous iteration (e.g. while rendering or executing a slash
		// command). This prevents notification buildup between idle iterations.
		drainNotifications(notifyCh, processUINotification)
		// A usage limit that ended the last turn is announced now, below that
		// turn's own error, rather than above it while the turn was ending.
		state.announceAutoContinue(renderer)
		if state.panel != nil {
			state.panel.refreshInventory()
			drawUIPanel(renderer, &state)
		} else {
			renderComposerWithState(renderer, &state)
		}
		// The limit has reset: the continuation runs as the next turn, the
		// way a message the reader sent would. It waits while a panel is open,
		// since the panel owns the keys a running turn would listen for.
		if state.panel == nil {
			if continuation, due := state.takeAutoContinueDue(); due {
				sessionTouched = true
				disposition, err := executeComposerSubmission(ctx, sigCh, events, notifyCh, processUINotification, opts.Session, renderer, tracker, &state, continuation, opts.Clipboard, cmds, true, &turnWorkedStatus)
				if err != nil {
					return err
				}
				for {
					next, ok := nextAutomaticSubmission(&state, disposition)
					if !ok {
						break
					}
					disposition, err = executeComposerSubmission(ctx, sigCh, events, notifyCh, processUINotification, opts.Session, renderer, tracker, &state, next, opts.Clipboard, cmds, true, &turnWorkedStatus)
					if err != nil {
						return err
					}
					continuation = next
				}
				postTurnCompletionNotification(outSync, disposition, state.terminalFocused, continuation)
				continue
			}
		}
		line := state.composer.Text
		if state.holdComposer {
			line = ""
		} else {
			state.composer.Text = ""
		}
		if strings.TrimSpace(line) == "" {
			state.resumeInputReadIfNeeded()
			var ev inputEvent
			var ok bool
			if hasPendingNonWheel {
				ev = pendingNonWheel
				hasPendingNonWheel = false
				ok = true
			} else {
				var err error
				ev, ok, err = awaitInputOrSignal(ctx, sigCh, events, notifyCh, processUINotification, state.autoContinueWake)
				if err != nil {
					return err
				}
				if !ok {
					renderer.Newline()
					exitSessionID = state.sessionID
					return nil
				}
			}
			if ev.kind == inputEventWake {
				continue
			}
			if state.updateTerminalFocus(ev) {
				continue
			}
			if ev.kind == inputEventMouseClick {
				renderer.ViewportClickToggle(ev.mouseCol, ev.mouseRow)
				continue
			}
			if ev.kind == inputEventMousePress {
				renderer.ViewportSelectStart(ev.mouseCol, ev.mouseRow)
				continue
			}
			if ev.kind == inputEventMouseDrag {
				renderer.ViewportSelectDrag(ev.mouseCol, ev.mouseRow)
				continue
			}
			if ev.kind == inputEventMouseRelease {
				renderer.ViewportSelectEnd(ev.mouseCol, ev.mouseRow)
				continue
			}
			if ev.kind == inputEventMouseMove {
				renderer.ViewportHover(ev.mouseCol, ev.mouseRow)
				continue
			}
			if ev.kind == inputEventMouseWheel {
				// Coalesce consecutive wheel events to avoid O(n) render
				// thrashing when scrolling fast through a large state.
				// Each ViewportScrollAt triggers a full renderViewport that
				// iterates over ALL blocks; without coalescing, a burst of
				// wheel events (buffer 8) causes 8 full renders, blocking the
				// input pipeline. With coalescing, only one render runs per
				// idle-loop iteration.
				delta := ev.wheelDelta
				col, row := ev.mouseCol, ev.mouseRow
				for {
					select {
					case ev2 := <-events:
						if ev2.kind == inputEventMouseWheel {
							delta += ev2.wheelDelta
							col, row = ev2.mouseCol, ev2.mouseRow
							continue
						}
						pendingNonWheel = ev2
						hasPendingNonWheel = true
					default:
					}
					break
				}
				renderer.ViewportScrollAt(delta, col, row)
				continue
			}
			if state.panel != nil {
				// The panel is the key target while it is open. It handles
				// navigation, tab switching, confirm and dismiss; any other
				// key is ignored, never fed to the composer underneath.
				if !handleUIPanelKey(renderer, &state, ev) {
					closeUIPanel(renderer, &state)
					renderComposerWithState(renderer, &state)
				}
				continue
			}
			if ev.kind == inputEventHotkey {
				if ev.hotkey == hotkeyOverlayUp || ev.hotkey == hotkeyOverlayDown {
					if state.handleOverlayNav(ev.hotkey, renderer.ActiveView()) {
						renderComposerWithState(renderer, &state)
					}
					continue
				}
				if ev.hotkey == hotkeyEscapeInterrupt && state.composer.MentionOverlay != nil {
					state.composer.MentionOverlay = nil
					state.composer.MentionDismissedDraft = state.composer.DraftText
					renderComposerWithState(renderer, &state)
					continue
				}
				if ev.hotkey == hotkeyEscapeInterrupt && state.composer.SlashOverlay != nil {
					state.composer.SlashOverlay = nil
					renderComposerWithState(renderer, &state)
					continue
				}
				if ev.hotkey == hotkeyEscapeInterrupt && state.agentRosterFocused {
					state.agentRosterFocused = false
					renderComposerWithState(renderer, &state)
					continue
				}
				if ev.hotkey == hotkeyEscapeInterrupt && renderer.ActiveView() == "" && state.cancelAutoContinue(renderer) {
					renderComposerWithState(renderer, &state)
					continue
				}
				if ev.hotkey == hotkeyOverlayAccept {
					if newDraft, ok := state.composer.AcceptSlashSelection(); ok {
						state.composer.DraftText = newDraft
						state.composer.Cursor = len([]rune(newDraft))
						state.composer.HandleDraftUpdate(newDraft, opts.Session)
						seedInteractiveInput(newDraft, seedCursorEnd)
						renderComposerWithState(renderer, &state)
						continue
					}
					if acc, ok := state.composer.AcceptMentionSelection(); ok {
						state.applyMentionAcceptance(acc)
						renderComposerWithState(renderer, &state)
					}
					continue
				}
				if handleIdleHotkey(ctx, opts.Session, renderer, &state, opts.Clipboard, selector, ev.hotkey) {
					if state.quitRequested {
						renderer.Newline()
						exitSessionID = state.sessionID
						return nil
					}
					renderComposerWithState(renderer, &state)
					continue
				}
				continue
			} else {
				if ev.kind == inputEventDraft {
					draft, cursor := sanitizeTerminalDraft(ev.draft, ev.cursor)
					// Typing is the other way to cancel a pending continuation:
					// the reader is taking the conversation somewhere themselves.
					if strings.TrimSpace(draft) != "" {
						state.cancelAutoContinue(renderer)
					}
					state.prepareHistoryDraft(ev, draft)
					state.syncPendingPastesWithDraft(draft)
					state.syncAttachmentsWithDraft(draft)
					state.composer.Cursor = composerCursorClamp(draft, cursor)
					state.composer.HandleDraftUpdate(draft, opts.Session)
					if state.holdComposer {
						state.holdComposer = false
						state.composer.Text = ""
					}
					renderComposerWithState(renderer, &state)
					continue
				}
				if ev.kind == inputEventLine {
					if acc, ok := state.composer.AcceptMentionSelection(); ok {
						state.applyMentionAcceptance(acc)
						renderComposerWithState(renderer, &state)
						continue
					}
				}
				line = ev.line
				if ev.kind == inputEventLine {
					state.enrichSubmittedInputHistory(ev)
				}
				if ev.kind == inputEventLine && state.handleAgentRosterLineInput(opts.Session, renderer, tracker, strings.TrimSpace(line)) {
					renderComposerWithState(renderer, &state)
					continue
				}
			}
			if ev.kind == inputEventPaste {
				state.cancelAutoContinue(renderer)
				state.appendPaste(ev.paste)
				continue
			}
			if state.holdComposer && (strings.TrimSpace(state.composer.Text) != "" || len(state.composer.PendingPastes) > 0 || len(state.composer.Attachments) > 0) {
				// A prior paste merged content into state.composer.Text and set
				// holdComposer. The raw reader's line buffer only reflects the
				// chars it personally tracked (typically the typed prefix), so
				// prefer the merged composer text on submission.
				if merged := state.composer.Text; strings.TrimSpace(merged) != "" {
					line = merged
				}
				state.composer.Text = ""
				state.holdComposer = false
			}
		}
		if strings.TrimSpace(line) == "" && len(state.composer.Attachments) == 0 && len(state.composer.PendingPastes) == 0 {
			state.resumeInputReadIfNeeded()
			renderComposerWithState(renderer, &state)
			continue
		}
		// Enter on Active overlay substitutes line with /<selected-cmd>. The
		// user typed "/" or "/prefix" and pressed Enter; pick the highlighted
		// row from Visible and dispatch that command.
		if overlay := state.composer.SlashOverlay; overlay != nil && overlay.Active &&
			len(overlay.Visible) > 0 && strings.HasPrefix(strings.TrimSpace(line), "/") {
			idx := overlay.SelectedIdx
			if idx >= 0 && idx < len(overlay.Visible) {
				line = "/" + overlay.Visible[idx].Name
			}
		}
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "exit" || lower == "quit" {
			state.resumeInputReadIfNeeded()
			exitSessionID = state.sessionID
			return nil
		}
		toks := strings.Fields(line)
		// The line is now this loop's to execute, so the composer no longer
		// holds it. Take the submission it stands for, then clear and repaint
		// the composer before any handler runs. The reader does queue an empty
		// draft behind every submitted line, but that event is only processed
		// once the handler returns: a command that opens a picker or starts a
		// job would otherwise be presented behind a composer still showing the
		// text that launched it, under a slash popup that outlived its own
		// submission.
		state.resumeInputReadIfNeeded()
		state.composer.Text = line
		submission, submittable := state.submissionFromCurrentDraft()
		state.resetComposerForActiveRunDraft()
		renderComposerWithState(renderer, &state)
		if len(toks) > 0 && strings.HasPrefix(strings.TrimSpace(toks[0]), "/") {
			cmd := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(toks[0]), "/"))
			var handled, continueRun, exitRequested bool
			var slashSubmission ComposerSubmission
			if cmd == "compact" {
				handled, continueRun, slashSubmission, exitRequested = dispatchCancelableStreamSlashCommand(
					ctx, sigCh, events, notifyCh, processUINotification,
					cmds, renderer, tracker, &state, line,
				)
			} else {
				handled, continueRun, slashSubmission, exitRequested = dispatchStreamSlashCommand(ctx, cmds, renderer, tracker, &state, line)
			}
			if handled {
				if exitRequested {
					exitSessionID = state.sessionID
					return nil
				}
				if continueRun {
					state.resumeInputReadIfNeeded()
					if len(slashSubmission.Parts) == 0 {
						renderComposerWithState(renderer, &state)
						continue
					}
					activeEvents := events
					sessionTouched = true
					disposition, err := executeComposerSubmission(ctx, sigCh, activeEvents, notifyCh, processUINotification, opts.Session, renderer, tracker, &state, slashSubmission, opts.Clipboard, cmds, false, &turnWorkedStatus)
					if err != nil {
						return err
					}
					for {
						next, ok := nextAutomaticSubmission(&state, disposition)
						if !ok {
							break
						}
						disposition, err = executeComposerSubmission(ctx, sigCh, activeEvents, notifyCh, processUINotification, opts.Session, renderer, tracker, &state, next, opts.Clipboard, cmds, true, &turnWorkedStatus)
						if err != nil {
							return err
						}
						slashSubmission = next
					}
					postTurnCompletionNotification(outSync, disposition, state.terminalFocused, slashSubmission)
					continue
				}
				state.resumeInputReadIfNeeded()
				renderComposerWithState(renderer, &state)
				continue
			}
		}
		if !submittable {
			continue
		}
		activeEvents := events
		sessionTouched = true
		disposition, err := executeComposerSubmission(ctx, sigCh, activeEvents, notifyCh, processUINotification, opts.Session, renderer, tracker, &state, submission, opts.Clipboard, cmds, false, &turnWorkedStatus)
		if err != nil {
			return err
		}
		for {
			next, ok := nextAutomaticSubmission(&state, disposition)
			if !ok {
				break
			}
			disposition, err = executeComposerSubmission(ctx, sigCh, activeEvents, notifyCh, processUINotification, opts.Session, renderer, tracker, &state, next, opts.Clipboard, cmds, true, &turnWorkedStatus)
			if err != nil {
				return err
			}
			submission = next
		}
		postTurnCompletionNotification(outSync, disposition, state.terminalFocused, submission)
	}
}

type terminalInputShutdown interface {
	FlushInput() error
	Restore() error
}

// shutdownTerminalInput stops terminal-generated input before restoring the
// shell's termios state. A long blocking operation can leave SGR mouse reports
// queued behind the reader's event buffer; if those bytes survive teardown,
// the shell displays tails such as "64;52;32M" as apparent garbage.
func shutdownTerminalInput(out io.Writer, tty terminalInputShutdown, cancelInput context.CancelFunc) {
	if cancelInput != nil {
		cancelInput()
	}
	if out != nil {
		_, _ = fmt.Fprint(out, "\x1b[0m\x1b[?25h"+disableMouseSeq+disableFocusReportingSeq+disableBracketedPasteSeq+resetComposerCursorColorSeq)
	}
	if tty == nil {
		return
	}
	// Disable reports first so no new mouse/focus bytes can arrive between the
	// flush and Restore handing stdin back to the shell.
	_ = tty.FlushInput()
	_ = tty.Restore()
}

func planModeRequiredFromEnv() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("FOREBRAIN_PLAN_MODE_REQUIRED")))
	switch v {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func startupWorkingDirectoryDisplay(workingDir string) string {
	dir := strings.TrimSpace(workingDir)
	if dir == "" {
		if cwd, err := os.Getwd(); err == nil {
			dir = cwd
		}
	}
	if dir == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Clean(dir)
	}
	return abbreviateHomePath(dir, home)
}

func summarizeComposerModel(session Session) string {
	if session == nil {
		return ""
	}
	if current := strings.TrimSpace(session.CurrentModelOption()); current != "" {
		return strings.ReplaceAll(current, " / ", "/")
	}
	summary := strings.ReplaceAll(strings.TrimSpace(session.ModelSummaryString()), "\r", "")
	if summary == "" {
		return ""
	}
	var provider string
	var model string
	lines := strings.Split(summary, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "provider="); ok {
			provider = strings.TrimSpace(after)
			continue
		}
		if after, ok := strings.CutPrefix(line, "model="); ok {
			model = strings.TrimSpace(after)
			continue
		}
	}
	if provider != "" && model != "" {
		return provider + "/" + model
	}
	if model != "" {
		return model
	}
	return strings.Join(strings.Fields(summary), " ")
}

// subagentModelsByType lists the built-in subagent types that run on a model of
// their own, as agents.definitions[<type>].llm_providers configures them. It
// mirrors the runtime's per-type client map: a type with no chain of its own is
// left out, and the subagent view's footer then shows the primary agent's
// model, which is what that subagent is actually running on.
func subagentModelsByType(session Session) map[string]ComposerFooter {
	if session == nil {
		return nil
	}
	models := make(map[string]ComposerFooter)
	for _, agentType := range agent.PublicTypeNames() {
		model, effort, ok := session.SubagentModelSummary(agentType)
		if !ok {
			continue
		}
		models[agentType] = ComposerFooter{
			Model:           strings.ReplaceAll(strings.TrimSpace(model), " / ", "/"),
			ReasoningEffort: strings.TrimSpace(effort),
		}
	}
	return models
}

func summarizeComposerReasoningEffort(session Session) string {
	if session == nil {
		return ""
	}
	return strings.TrimSpace(session.CurrentModelReasoningEffort())
}

func initialComposerTokenStats(session Session) ComposerTokenStats {
	if session == nil {
		return ComposerTokenStats{}
	}
	// A fresh session holds nothing yet, so the footer shows the full budget
	// from the first frame rather than the "compact pending" placeholder.
	return session.SurfaceComposerTokenStats(0)
}

func abbreviateHomePath(path string, home string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	home = filepath.Clean(strings.TrimSpace(home))
	if path == "" || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	sepHome := home + string(os.PathSeparator)
	if strings.HasPrefix(path, sepHome) {
		return "~" + string(os.PathSeparator) + strings.TrimPrefix(path, sepHome)
	}
	return path
}

func dispatchStreamSlashCommand(ctx context.Context, cmds *commandController, renderer *Renderer, tracker *Tracker, state *streamState, line string) (handled bool, continueRun bool, submission ComposerSubmission, exitRequested bool) {
	if cmds == nil || state == nil {
		return false, false, ComposerSubmission{}, false
	}
	// /plan and a session change can move the conversation's mode.
	defer syncPlanModeIndicator(renderer, state)
	line = strings.TrimSpace(line)
	if line == "" || !strings.HasPrefix(line, "/") {
		return false, false, ComposerSubmission{}, false
	}
	toks := strings.Fields(line)
	cmd := ""
	if len(toks) > 0 {
		cmd = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(toks[0]), "/"))
	}
	args := []string{}
	if len(toks) > 1 {
		args = toks[1:]
	}
	// A command that takes no inline arguments refuses them with the same
	// sentence on every surface, before any picker or panel opens.
	if reply, refuse := turn.InlineArgsRefusal(cmd, args); refuse {
		renderer.RenderFrame(Frame{Kind: FrameSystem, Title: cmd, Content: reply, Final: true})
		return true, false, ComposerSubmission{}, false
	}
	switch cmd {
	case "", "help":
		renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "commands", Content: turn.CommandCatalog(turn.SurfaceTUI, turn.DiscoveryOptions{FastAvailable: sessionFastAvailable(cmds.session)}), Final: true})
		return true, false, ComposerSubmission{}, false
	case "resume":
		if selected, ok := cmds.handleResume(ctx, state.sessionID); ok {
			switchStreamSession(ctx, state, renderer, tracker, selected)
			cmds.printSessionResumeContext(selected)
		}
		return true, false, ComposerSubmission{}, false
	case "permissions":
		// Bare /permissions is the preset picker; its one inline form,
		// explain <tool>, is answered as a report.
		if len(args) > 0 {
			_ = cmds.handlePermissionsReport(state.sessionID, args)
			return true, false, ComposerSubmission{}, false
		}
		_ = cmds.handlePermissions(state.sessionID)
		return true, false, ComposerSubmission{}, false
	case "skills":
		if sub, ok := cmds.handleSkills(ctx); ok {
			state.resetComposerForActiveRunDraft()
			return true, true, sub, false
		}
		return true, false, ComposerSubmission{}, false
	case "connect":
		_ = cmds.handleConnect(ctx, state.sessionID)
		return true, false, ComposerSubmission{}, false
	case "sandbox":
		_ = cmds.handleSandbox(state.sessionID)
		return true, false, ComposerSubmission{}, false
	case "memories":
		_ = cmds.handleMemories(state.sessionID)
		return true, false, ComposerSubmission{}, false
	case "status":
		_ = cmds.handleStatus(state.sessionID)
		return true, false, ComposerSubmission{}, false
	case "mcp":
		_ = cmds.handleMCP(state.sessionID)
		return true, false, ComposerSubmission{}, false
	case "lsp":
		_ = cmds.handleLSP(state.sessionID)
		return true, false, ComposerSubmission{}, false
	case "migrate":
		_ = cmds.handleMigrate(ctx, state.sessionID)
		return true, false, ComposerSubmission{}, false
	case "diff":
		_ = cmds.handleDiff(state.sessionID, args)
		return true, false, ComposerSubmission{}, false
	case "rename":
		// Bare /rename asks for the name in place, saying what the
		// conversation is called now, instead of answering with its usage.
		if len(args) == 0 && cmds.selector != nil {
			label := "Rename conversation · type the new name"
			if current := lookupSessionTitle(ctx, cmds.session, state.sessionID); current != "" && current != state.sessionID {
				label = "Rename “" + current + "” · type the new name"
			}
			name, ok, err := cmds.selector.Input(label, "")
			if err != nil {
				renderer.PrintError(err)
				return true, false, ComposerSubmission{}, false
			}
			if !ok || strings.TrimSpace(name) == "" {
				return true, false, ComposerSubmission{}, false
			}
			line = "/rename " + strings.TrimSpace(name)
		}
	}
	outcome, ok := cmds.session.ExecuteSurfaceSlash(ctx, state.sessionID, line)
	// Inject-prompt commands (/init, /review, /plan <desc>) report
	// Handled:false with ShouldContinueRun:true — the built prompt is fed to
	// the model rather than answered inline. Gate on both so they are not
	// mistaken for unhandled input and sent to the LLM verbatim (mirrors the
	// gateway path, which checks ShouldContinueRun before Handled).
	if !ok || (!outcome.Handled && !outcome.ShouldContinueRun) {
		if _, known := turn.Find(cmd); !known {
			renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "commands", Content: turn.UnknownCommandReply(turn.SurfaceTUI, cmd, turn.DiscoveryOptions{FastAvailable: sessionFastAvailable(cmds.session)}), Final: true})
			return true, false, ComposerSubmission{}, false
		}
		return false, false, ComposerSubmission{}, false
	}
	return applySlashOutcome(ctx, cmds, renderer, tracker, state, cmd, line, outcome)
}

// dispatchCancelableStreamSlashCommand keeps the TUI event loop alive while a
// blocking slash handler runs. Manual compaction can spend a long time reading
// a large transcript and waiting on the provider; executing it inline makes the
// viewport, input, and Ctrl+C handling appear frozen for that entire lifetime.
//
// Only /compact currently uses this path. Other slash commands are either
// immediate or own a modal selector and therefore must remain on the main UI
// goroutine. The slash handler receives opCtx, so Ctrl+C/Esc cancels the actual
// provider/database operation instead of merely queuing input for later.
func dispatchCancelableStreamSlashCommand(
	ctx context.Context,
	sigCh <-chan os.Signal,
	events <-chan inputEvent,
	notifyCh <-chan any,
	processNotify func(any),
	cmds *commandController,
	renderer *Renderer,
	tracker *Tracker,
	state *streamState,
	line string,
) (handled bool, continueRun bool, submission ComposerSubmission, exitRequested bool) {
	if cmds == nil || cmds.session == nil || state == nil {
		return false, false, ComposerSubmission{}, false
	}
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 {
		return false, false, ComposerSubmission{}, false
	}
	cmd := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	if cmd != "compact" {
		return false, false, ComposerSubmission{}, false
	}

	opCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type slashResult struct {
		outcome SlashOutcome
		ok      bool
	}
	resultCh := make(chan slashResult, 1)
	sessionID := state.sessionID
	go func() {
		outcome, ok := cmds.session.ExecuteSurfaceSlash(opCtx, sessionID, line)
		resultCh <- slashResult{outcome: outcome, ok: ok}
	}()

	ctxDone := ctx.Done()
	cancelRequested := false
	// The compaction's own card reports the cancellation when it lands, so
	// nothing is printed here.
	requestCancel := func() {
		if cancelRequested {
			return
		}
		cancelRequested = true
		cancel()
	}

	for {
		select {
		case result := <-resultCh:
			// The compact handler emits its completed/failed notification before
			// returning. Drain anything already forwarded so the running card is
			// replaced promptly; a notification still in the session dispatcher is
			// picked up by the normal idle loop on the next iteration.
			drainNotifications(notifyCh, processNotify)
			if !result.ok || (!result.outcome.Handled && !result.outcome.ShouldContinueRun) {
				return false, false, ComposerSubmission{}, false
			}
			return applySlashOutcome(ctx, cmds, renderer, tracker, state, cmd, line, result.outcome)
		case <-ctxDone:
			// Keep waiting for the handler's synchronous cleanup/persistence
			// barrier, but do not spin on an already-closed context channel.
			ctxDone = nil
			requestCancel()
		case <-sigCh:
			requestCancel()
		case m, ok := <-notifyCh:
			if !ok {
				notifyCh = nil
				continue
			}
			if processNotify != nil {
				processNotify(m)
			}
		case ev, ok := <-events:
			if !ok || ev.kind == inputEventDone {
				events = nil
				requestCancel()
				continue
			}
			if handleBlockingSlashInput(cmds.session, renderer, state, ev) {
				requestCancel()
			}
		}
	}
}

// handleBlockingSlashInput processes the input that must stay responsive while
// /compact is in flight. It deliberately does not dispatch another command or
// turn concurrently with compaction; submitted text is retained in the
// composer and handled by the normal loop immediately after compact finishes.
// The return value requests cancellation of the blocking operation.
func handleBlockingSlashInput(session Session, renderer *Renderer, state *streamState, ev inputEvent) bool {
	if state == nil {
		return false
	}
	if state.updateTerminalFocus(ev) {
		return false
	}
	switch ev.kind {
	case inputEventMouseClick:
		renderer.ViewportClickToggle(ev.mouseCol, ev.mouseRow)
	case inputEventMousePress:
		renderer.ViewportSelectStart(ev.mouseCol, ev.mouseRow)
	case inputEventMouseDrag:
		renderer.ViewportSelectDrag(ev.mouseCol, ev.mouseRow)
	case inputEventMouseRelease:
		renderer.ViewportSelectEnd(ev.mouseCol, ev.mouseRow)
	case inputEventMouseMove:
		renderer.ViewportHover(ev.mouseCol, ev.mouseRow)
	case inputEventMouseWheel:
		renderer.ViewportScrollAt(ev.wheelDelta, ev.mouseCol, ev.mouseRow)
	case inputEventDraft:
		draft, cursor := sanitizeTerminalDraft(ev.draft, ev.cursor)
		state.prepareHistoryDraft(ev, draft)
		state.syncPendingPastesWithDraft(draft)
		state.syncAttachmentsWithDraft(draft)
		state.composer.Cursor = composerCursorClamp(draft, cursor)
		state.composer.HandleDraftUpdate(draft, session)
		state.composer.Text = ""
		renderComposerWithState(renderer, state)
	case inputEventPaste:
		state.appendPaste(ev.paste)
		renderComposerWithState(renderer, state)
	case inputEventLine:
		if strings.TrimSpace(ev.line) == "" {
			return false
		}
		state.enrichSubmittedInputHistory(ev)
		state.composer.Text = ev.line
		state.composer.DraftText = ev.line
		state.composer.Cursor = len([]rune(ev.line))
		state.composer.HandleDraftUpdate(ev.line, session)
		renderComposerWithState(renderer, state)
	case inputEventHotkey:
		switch ev.hotkey {
		case hotkeyInterrupt, hotkeyEscapeInterrupt:
			return true
		case hotkeyClearInput:
			state.clearComposer()
			renderComposerWithState(renderer, state)
		case hotkeyJumpToBottom:
			renderer.ViewportJumpToBottom()
		case hotkeyRedraw:
			renderer.ForceRepaint()
		}
	}
	return false
}

// errWithdrawNotDurable is what the user is told when Esc took the message out
// of the conversation but the store would not take it out of saved history. It
// says what is and is not true in one sentence and carries no transport detail:
// the store's own error text goes to the log instead.
var errWithdrawNotDurable = errors.New("the message was removed from this conversation but not from its saved history, so it may come back on resume; your draft is safe")

// foregroundTurn arbitrates Esc against the producer's first output, not the
// asynchronous UI queue. Its identity survives queued notifications and is never
// rebound to whichever run happens to be active when those notifications arrive.
type foregroundTurn struct {
	mu        sync.Mutex
	responded bool
	withdrawn bool
	finished  bool
	cancel    context.CancelFunc

	// The remaining fields are owned exclusively by the UI loop.
	submission ComposerSubmission
	restored   bool
	stopStatus func()
}

type foregroundTurnKey struct{}

func foregroundTurnFrom(ctx context.Context) *foregroundTurn {
	t, _ := ctx.Value(foregroundTurnKey{}).(*foregroundTurn)
	return t
}

func (t *foregroundTurn) acknowledgeResponse() bool {
	if t == nil {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.withdrawn {
		return false
	}
	t.responded = true
	return true
}

func (t *foregroundTurn) withdraw() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.withdrawn {
		return true
	}
	if t.responded || t.finished {
		return false
	}
	t.withdrawn = true
	if t.cancel != nil {
		t.cancel()
	}
	return true
}

func (t *foregroundTurn) isWithdrawn() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.withdrawn
}

// lspRecommendationDecider is the session slice of the recommendation
// modal: the answer goes back through the control plane, and the line it
// returns is the transcript's record of what was decided.
type lspRecommendationDecider interface {
	DecideLSPRecommendation(event.LSPRecommendation, event.LSPRecommendationChoice) (string, error)
}

// handleLSPRecommendation asks the recommendation's modal question and
// applies the answer. Like the migration preview, the modal can open while
// a turn runs: the agent keeps working, and queued input waits until the
// answer is applied. The result line goes to the transcript only — it never
// reaches the model's context.
func handleLSPRecommendation(renderer *Renderer, selector Selector, state *streamState, decider lspRecommendationDecider, rec event.LSPRecommendation) {
	state.deferQueueAutosendUntilSelectionApplied()
	defer state.resumeQueueAutosend()
	label := "LSP recommendation\nA language server gives the agent diagnostics after its edits and lets it find definitions and references by symbol. Enable this language server?"
	found := ""
	if rec.Mode == "install" {
		found = "Not installed. Install with: " + rec.InstallCommand
	} else {
		found = tildePath(rec.BinaryPath)
		if rec.Version != "" {
			found += " (" + rec.Version + ")"
		}
	}
	facts := []turn.StatusFact{
		{Label: "Server", Value: rec.DisplayName + " (" + strings.Join(rec.Languages, ", ") + ")"},
		{Label: "Found", Value: found},
		{Label: "Triggered by", Value: rec.TriggerExtension + " files"},
		{Label: "Runs", Value: "in this trusted project, outside the sandbox"},
	}
	primaryAction, primaryChoice := "Yes, enable", event.LSPChoiceEnable
	defaultIdx := 0
	if rec.Mode == "install" {
		// Running an install command must not be one keypress away.
		primaryAction, primaryChoice = "Yes, install and enable", event.LSPChoiceInstall
		defaultIdx = 1
	}
	actions := []string{primaryAction, "No, not now", "Never for " + rec.ServerID, "Disable all LSP recommendations"}
	idx, confirmed, err := selector.Review(label, facts, actions, defaultIdx)
	choice := event.LSPChoiceNotNow
	switch {
	case err != nil:
		renderer.PrintError(err)
	case confirmed:
		switch idx {
		case 0:
			choice = primaryChoice
		case 2:
			choice = event.LSPChoiceNever
		case 3:
			choice = event.LSPChoiceDisableAll
		}
	}
	text, decideErr := decider.DecideLSPRecommendation(rec, choice)
	if decideErr != nil {
		renderer.PrintError(decideErr)
		return
	}
	if text != "" {
		renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "lsp", Content: text, Final: true})
	}
}

// tildePath shortens the user's own home-directory prefix to ~, the way a
// prompt shows a path that lives there.
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || p == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

// finish closes the withdrawal window before ordinary outcome persistence. If
// Esc won, the dispatcher must instead complete exact-row withdrawal cleanup.
func (t *foregroundTurn) finish() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.finished = true
	return t.withdrawn
}

type streamState struct {
	activeForeground *foregroundTurn
	sessionID        string
	session          Session
	composer         ComposerState
	// autoContinue is the continuation the engine armed after a usage limit,
	// while it waits; autoContinueDue is that continuation once its wait ended,
	// until the idle loop submits it.
	autoContinue    *autoContinueView
	autoContinueDue *ComposerSubmission
	// panel is the interactive slash panel (/status, /mcp) owned by this
	// loop. It is composer state, not a modal: keys arrive through the same
	// event stream, notifications keep flowing, and the transcript keeps
	// streaming above it.
	panel         *uiPanel
	pendingSteers []ComposerSubmission
	// queuedTurns is the FIFO ordinary follow-up queue. Its newest item is
	// destructively recalled for editing.
	queuedTurns []queuedSubmission
	// rejectedSteers are inputs that were accepted as steers but rejected by
	// the active run. They are kept separate from ordinary follow-ups.
	rejectedSteers []queuedSubmission
	// queueSeq is the shared enqueue clock across all three queues. It only has
	// to order messages against each other, so it is never reset.
	queueSeq int
	// terminalFocused follows CSI I / CSI O focus reports. It starts true so
	// terminals without focus-reporting support do not produce noisy bells.
	terminalFocused bool
	// clipboardWatch follows the same focus reports: it probes the clipboard
	// for the paste hint only while the terminal has focus. Nil when no
	// watcher runs (non-TTY sessions, tests).
	clipboardWatch                 *clipboardImageWatcher
	restoredQueuedSubmission       ComposerSubmission
	restoredQueuedSubmissionActive bool
	inputHistoryStore              rawInputHistoryStore
	historyBrowseActive            bool
	historyDraftAttachments        []InputAttachment
	historyDraftPastes             []PendingPaste
	quitRequested                  bool
	resumeRawRead                  func()
	nextPasteID                    int
	holdComposer                   bool
	home                           string
	workspaceRoot                  string
	workingDir                     string
	startupInfo                    StartupInfo
	// titleAnimator drives the animated terminal window/tab title while a
	// turn is in flight; nil in tests that construct streamState directly.
	titleAnimator                *terminalTitleAnimator
	activeRunCtrlCArmed          bool
	activeRunCtrlCExitArmed      bool
	activeRunCtrlCCancelNoReplay bool
	agentControlPrefixArmed      bool
	agentRoster                  AgentRosterSnapshot
	agentRosterSelected          int
	// agentRosterFocused is true once the user has moved keyboard focus onto
	// the roster panel via Down. While false, arrows control input history as
	// usual and the roster shows no per-row hint; Enter/x on the roster are
	// inert until focus is entered.
	agentRosterFocused bool
	// userInterrupted is set when the user interrupts an active run with esc.
	// The run's cancellation error is swallowed downstream and surfaces as a nil
	// error, so runTurn relies on this flag to report runTurnInterrupted (which
	// restores queued work into the composer) instead of runTurnCompleted (which
	// would auto-submit it as an ordinary follow-up).
	userInterrupted bool
	// submitPendingSteersAfterInterrupt records whether pressing esc should
	// flush pending steers. When the user presses esc while
	// steers are still pending, the interrupt exists specifically to flush them,
	// so the next interrupted boundary merges those steers and submits them as
	// one fresh user turn instead of restoring them into the composer.
	submitPendingSteersAfterInterrupt bool
	// activeRunIsUserShell is true while the in-flight turn is a user shell
	// command (`!cmd`) rather than an agent turn. A plain message typed during a user
	// shell run cannot steer that run, so it is queued as an ordinary follow-up.
	activeRunIsUserShell bool
	// suppressQueueAutosend is set
	// when a slash command defers to a modal selection (session switch,
	// permissions, skills, model) so queued input is not drained into a turn
	// before the user's selection has been applied.
	suppressQueueAutosend bool
}

// stateRoot returns the per-agent state root (the active agent's workspace
// root) onto which the mode store joins "state". The plan-mode hotkey uses it
// so the TUI and the agent runtime agree on where mode lives. Falls back to
// <home>/workspace for the main agent when WorkspaceRoot was not provided.
func (s *streamState) stateRoot() string {
	if s == nil {
		return ""
	}
	if ws := strings.TrimSpace(s.workspaceRoot); ws != "" {
		return ws
	}
	if home := strings.TrimSpace(s.home); home != "" {
		return filepath.Join(home, "workspace")
	}
	return ""
}

// syncPlanModeIndicator reads the conversation's mode — which the hotkey,
// /plan, the model's enter_plan_mode and exit_plan_mode and a session change
// can all move — and has the footer show it.
func syncPlanModeIndicator(renderer *Renderer, state *streamState) {
	if renderer == nil || state == nil || strings.TrimSpace(state.sessionID) == "" {
		return
	}
	st, _ := statepkg.Get(state.stateRoot(), state.sessionID)
	renderer.SetPlanMode(st.Mode == statepkg.ModePlan)
}

// switchStreamSession activates the target session's own model selection and,
// only once that succeeded, moves the surface onto it. Activation happens
// before any outgoing UI state is persisted or queued input is dropped, so a
// failed activation leaves the outgoing session — its queued input, its
// browse state and its runtime — exactly as it was.
func switchStreamSession(ctx context.Context, state *streamState, renderer *Renderer, tracker *Tracker, sessionID string) (switched bool, warning string, err error) {
	if state == nil {
		return false, "", nil
	}
	next := strings.TrimSpace(sessionID)
	if next == "" || next == strings.TrimSpace(state.sessionID) {
		return false, "", nil
	}
	activationCtx, cancel := context.WithTimeout(ctx, sessionModelActivationTimeout)
	defer cancel()
	warning, err = state.session.ActivateSessionModel(activationCtx, next)
	if err != nil {
		if renderer != nil {
			content := "Could not open that conversation: " + err.Error() +
				"\nThe current conversation and its model are unchanged."
			if run.IsPrimaryModelRuntimeUncertain(err) {
				content = "Could not open that conversation, and the previous model could not be restored either: " + err.Error() +
					"\nReload the configuration or restart before sending another message."
			}
			renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "session switch failed", Content: content, Final: true})
		}
		return false, "", err
	}
	persistSurfaceBrowseState(state.session, renderer, state.sessionID)
	// Queued input belongs to the conversation it was typed into, so it cannot
	// follow the user to another state. Retract what the outgoing run has not
	// delivered yet — otherwise the mirror is cleared here while the runtime
	// still holds the steer, and it reaches the old session's model without ever
	// appearing in the transcript — then report what was dropped, because
	// silently discarding queued messages is indistinguishable from losing them.
	dropped := state.discardQueuedInput()
	// A continuation waiting on a usage limit belongs to the conversation
	// being left. It is cancelled rather than carried along: it would resume a
	// conversation the reader is no longer looking at.
	state.leaveAutoContinue(renderer)
	state.sessionID = next
	syncPlanModeIndicator(renderer, state)
	// The MCP watch follows the conversation: its failures belong to the session
	// the user has open, and the progress line describes that session's runner.
	if watcher, ok := state.session.(mcpStartupWatcher); ok {
		watcher.WatchMCPStartup(next)
	}
	if state.titleAnimator != nil {
		state.titleAnimator.Settle(lookupSessionTitle(context.Background(), state.session, next))
	}
	state.resetComposerForActiveRunDraft()
	if dropped > 0 && renderer != nil {
		renderer.RenderFrame(Frame{
			Kind:    FrameStatus,
			Title:   "queued input discarded",
			Content: fmt.Sprintf("%s not carried into the new session", pluralizeMessages(dropped)),
			Final:   true,
		})
	}
	state.quitRequested = false
	state.resumeInputReadIfNeeded()
	state.resetActiveRunCtrlCSequence()
	state.activeRunCtrlCCancelNoReplay = false
	state.agentControlPrefixArmed = false
	state.userInterrupted = false
	state.submitPendingSteersAfterInterrupt = false
	state.activeRunIsUserShell = false
	state.suppressQueueAutosend = false
	if state.session != nil {
		state.agentRoster = state.session.AgentRosterSnapshot(next)
		// The conversation the surface speaks for has changed, so its own
		// unfinished approvals are now this surface's to recover.
		state.session.StartApprovalRecovery(next)
	} else {
		state.agentRoster = AgentRosterSnapshot{Rows: []AgentRosterRow{}}
	}
	state.agentRosterSelected = 0
	state.agentRosterFocused = false
	if tracker != nil {
		tracker.Reset()
	}
	if renderer != nil {
		// A newly selected session has no usage yet, but its footer must still
		// show the current model's full context budget immediately. This also
		// clears prior-session in/out usage without waiting for a provider update.
		renderer.SetComposerTokenStats(initialComposerTokenStats(state.session))
		// A session switch invalidates every line that was on screen: the MCP
		// startup, a migration and the previous session's working line all
		// belonged to the session that just went away.
		renderer.FinishAllTransientStatus()
		renderer.ViewportResetSession()
		// Re-seed the welcome banner so a fresh session (e.g. /new) opens with
		// the same startup chrome the initial launch shows. ViewportResetSession
		// clears the retained banner block, so nothing else redraws it.
		renderer.Banner(state.startupInfo)
		// The activated session may run a different model (or a fallback with
		// a warning); the footer and the budget follow it at once.
		refreshSessionFooter(renderer, state.session)
		if warning != "" {
			renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "model", Content: warning, Final: true})
		}
	}
	return true, warning, nil
}

// sessionModelActivationTimeout bounds the store reads and the runner
// rebuild a session switch performs before the surface moves.
const sessionModelActivationTimeout = 30 * time.Second

func persistSurfaceBrowseState(session Session, renderer *Renderer, sessionID string) {
	if session == nil || renderer == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	saver, ok := session.(interface {
		SaveSurfaceBrowseState(context.Context, string, RendererBrowseState) error
	})
	if !ok {
		return
	}
	_ = saver.SaveSurfaceBrowseState(context.Background(), sessionID, renderer.SnapshotBrowseState())
}

func (s *streamState) selectedAgentRosterRow() (AgentRosterRow, bool) {
	if s == nil || len(s.agentRoster.Rows) == 0 {
		return AgentRosterRow{}, false
	}
	idx := s.agentRosterSelected
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s.agentRoster.Rows) {
		idx = len(s.agentRoster.Rows) - 1
	}
	return s.agentRoster.Rows[idx], true
}

// handleOverlayNav routes hotkeyOverlayUp and hotkeyOverlayDown to the active
// overlay (slash picker > mention picker > agent roster) and returns true if
// navigation occurred (so the caller can re-render). Shared between the idle
// event loop and handleActiveRunInput (live turn).
func (s *streamState) handleOverlayNav(hotkey inputHotkey, activeView string) bool {
	if s == nil {
		return false
	}
	delta := 1
	if hotkey == hotkeyOverlayUp {
		delta = -1
	}
	if overlay := s.composer.SlashOverlay; overlay != nil && overlay.Active && len(overlay.Visible) > 0 {
		overlay.SelectedIdx += delta
		if overlay.SelectedIdx < 0 {
			overlay.SelectedIdx = len(overlay.Visible) - 1
		}
		if overlay.SelectedIdx >= len(overlay.Visible) {
			overlay.SelectedIdx = 0
		}
		return true
	} else if overlay := s.composer.MentionOverlay; overlay != nil && overlay.Active && len(overlay.Visible) > 0 {
		overlay.MoveSelection(delta)
		return true
	} else if agentRosterHasRunningSubagent(s.agentRoster) {
		rows := s.agentRoster.Rows
		if hotkey == hotkeyOverlayDown {
			if !s.agentRosterFocused {
				s.agentRosterFocused = true
				// Focus lands where the cursor already points — the agent whose
				// transcript is on screen — so taking the keyboard onto the
				// roster does not move the cursor to a different agent.
				s.agentRosterSelected = maxInt(0, agentRosterIndexForView(s.agentRoster, activeView))
			} else {
				s.agentRosterSelected++
				if s.agentRosterSelected >= len(rows) {
					s.agentRosterSelected = 0
				}
			}
		} else { // hotkeyOverlayUp
			if s.agentRosterFocused {
				if s.agentRosterSelected == 0 {
					s.agentRosterFocused = false
				} else {
					s.agentRosterSelected--
				}
			}
		}
		return true
	}
	return false
}

func (s *streamState) handleAgentRosterLineInput(session Session, renderer *Renderer, tracker *Tracker, line string) bool {
	if !s.agentRosterFocused {
		// Enter/x on the roster are inert until the user has actually
		// navigated onto it with Down, matching the focus-conditional hint.
		return false
	}
	line = strings.TrimSpace(line)
	row, ok := s.selectedAgentRosterRow()
	if !ok {
		return false
	}
	switch strings.ToLower(line) {
	case "":
		// Enter on the selected roster row: switch the active viewport
		// view to that agent (primary "" or subagent by roster key).
		// Does NOT switch the chat state.
		viewKey := ""
		if strings.TrimSpace(row.Kind) == "subagent" {
			viewKey = strings.TrimSpace(row.ID)
		}
		renderer.SetActiveView(viewKey)
		return true
	case "x":
		switch strings.TrimSpace(row.Kind) {
		case "subagent":
			cancelled := session.CancelSubagent(SubagentControlQuery{
				AgentID: strings.TrimSpace(row.ID),
				RunID:   strings.TrimSpace(row.RunID),
			})
			if cancelled {
				renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "subagent cancelled", Content: strings.TrimSpace(row.Label)})
			} else {
				renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "subagent", Content: "no cancellable run"})
			}
			return true
		case "primary":
			if session.CancelActiveRun() {
				renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "cancelling run"})
			} else {
				renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "agent", Content: "no cancellable run"})
			}
			return true
		}
	}
	return false
}

// refreshAgentRoster folds the reducer's subagent rows into the roster and
// keeps the selection inside it. Every message that can add or remove a row
// goes through here, so the clamp cannot be forgotten on one of them.
func (s *streamState) refreshAgentRoster(reducer *Reducer) {
	if s == nil || reducer == nil {
		return
	}
	s.agentRoster = mergeAgentRoster(s.agentRoster, reducer.AgentRosterSnapshot())
	if s.agentRosterSelected >= len(s.agentRoster.Rows) {
		s.agentRosterSelected = maxInt(0, len(s.agentRoster.Rows)-1)
	}
}

// mergeAgentRoster keeps the primary-agent row from the existing roster and
// merges in subagent rows from the reducer snapshot. The reducer only tracks
// subagents, so replacing the full roster loses the primary entry.
func mergeAgentRoster(current, fromReducer AgentRosterSnapshot) AgentRosterSnapshot {
	// Keep the primary row(s) from the current roster (set from session snapshot).
	primaryRows := make([]AgentRosterRow, 0, 1)
	for _, row := range current.Rows {
		if strings.EqualFold(strings.TrimSpace(row.Kind), "primary") {
			primaryRows = append(primaryRows, row)
		}
	}
	// Subagent rows come from the reducer (live status).
	out := AgentRosterSnapshot{
		Rows: make([]AgentRosterRow, 0, len(primaryRows)+len(fromReducer.Rows)),
	}
	out.Rows = append(out.Rows, primaryRows...)
	out.Rows = append(out.Rows, fromReducer.Rows...)
	return out
}

type ComposerSubmission struct {
	PendingPastes []PendingPaste
	Text          string
	Parts         []llm.ContentPart
	DisplayText   string
	// RawInput carries the original slash command the user typed (e.g.
	// "/init") when Parts/Text already hold the expanded prompt fed to the
	// model. It flows to TurnSubmission.RawInput for display-only
	// persistence and is never sent to the model. Empty for plain messages.
	RawInput    string
	Attachments []InputAttachment
	// GoalObjective carries the /goal objective through the TUI surface path
	// so it reaches DispatchSurfaceTurn and enables goal continuation.
	GoalObjective string
	// SkillName and SkillPath carry the trusted explicit skill selection from
	// slash parsing to the surface dispatcher; the runner loads the skill.
	SkillName string
	SkillPath string
	// QueueSeq orders this message against everything else queued during the
	// same run. The three queues are separate FIFOs, so without a shared clock
	// "edit last queued message" can only guess which queue holds the newest
	// message. Stamped by the enqueue helpers; meaningless (zero) for a
	// submission that never sat in a queue.
	QueueSeq int
}

type queuedSubmissionAction string

const (
	queuedSubmissionActionTurn          queuedSubmissionAction = "turn"
	queuedSubmissionActionRejectedSteer queuedSubmissionAction = "rejected_steer"
	queuedSubmissionActionShell         queuedSubmissionAction = "shell"
)

type queuedSubmission struct {
	Action     queuedSubmissionAction
	Submission ComposerSubmission
}

func (s *streamState) resumeInputReadIfNeeded() {
	if s == nil || s.resumeRawRead == nil {
		return
	}
	s.resumeRawRead()
	s.resumeRawRead = nil
}

// markUserInterrupt records that the user interrupted the active run with esc,
// so the next runTurn return reports runTurnInterrupted even when the
// cancellation error is swallowed downstream and surfaces as nil. The flag is
// the only evidence that the terminal boundary was user-initiated rather than
// natural completion.
func (s *streamState) markUserInterrupt() {
	if s != nil {
		s.userInterrupted = true
	}
}

// consumeUserInterrupt reads and clears the user-interrupt flag.
func (s *streamState) consumeUserInterrupt() bool {
	if s == nil {
		return false
	}
	v := s.userInterrupted
	s.userInterrupted = false
	return v
}

// markSubmitPendingSteersAfterInterrupt records that the pending esc interrupt
// exists to flush queued steers, so the interrupted boundary submits them
// immediately as one fresh turn.
func (s *streamState) markSubmitPendingSteersAfterInterrupt() {
	if s != nil {
		s.submitPendingSteersAfterInterrupt = true
	}
}

// consumeSubmitPendingSteersAfterInterrupt reads and clears the flag.
func (s *streamState) consumeSubmitPendingSteersAfterInterrupt() bool {
	if s == nil {
		return false
	}
	v := s.submitPendingSteersAfterInterrupt
	s.submitPendingSteersAfterInterrupt = false
	return v
}

// onlyUserShellCommandsRunning reports whether the in-flight work is limited to
// a user shell command.
func (s *streamState) onlyUserShellCommandsRunning() bool {
	return s != nil && s.activeRunIsUserShell
}

// deferQueueAutosendUntilSelectionApplied suppresses queue draining until a
// a pending modal selection has been applied.
func (s *streamState) deferQueueAutosendUntilSelectionApplied() {
	if s != nil {
		s.suppressQueueAutosend = true
	}
}

// resumeQueueAutosend clears the autosend suppression once the modal selection
// has been applied or dismissed.
func (s *streamState) resumeQueueAutosend() {
	if s != nil {
		s.suppressQueueAutosend = false
	}
}

func (s *streamState) clearComposer() bool {
	if s == nil {
		return false
	}
	cleared := false
	if strings.TrimSpace(s.composer.Text) != "" {
		s.composer.Text = ""
		cleared = true
	}
	if strings.TrimSpace(s.composer.DraftText) != "" {
		s.composer.DraftText = ""
		cleared = true
	}
	s.composer.Cursor = 0
	if len(s.composer.Attachments) > 0 {
		s.composer.Attachments = nil
		cleared = true
	}
	if len(s.composer.PendingPastes) > 0 {
		s.composer.PendingPastes = nil
		cleared = true
	}
	s.clearRestoredQueuedSubmission()
	if s.holdComposer {
		s.holdComposer = false
	}
	s.clearHistoryBrowse()
	return cleared
}

func (s *streamState) armActiveRunCtrlC() {
	if s != nil {
		s.activeRunCtrlCArmed = true
		s.activeRunCtrlCExitArmed = false
	}
}

func (s *streamState) armActiveRunCtrlCExit() {
	if s != nil {
		s.activeRunCtrlCArmed = false
		s.activeRunCtrlCExitArmed = true
	}
}

func (s *streamState) resetActiveRunCtrlCSequence() {
	if s != nil {
		s.activeRunCtrlCArmed = false
		s.activeRunCtrlCExitArmed = false
	}
}

func (s *streamState) isActiveRunCtrlCArmed() bool {
	return s != nil && s.activeRunCtrlCArmed
}

func (s *streamState) isActiveRunCtrlCExitArmed() bool {
	return s != nil && s.activeRunCtrlCExitArmed
}

func (s *streamState) markActiveRunCtrlCCancelNoReplay() {
	if s != nil {
		s.activeRunCtrlCCancelNoReplay = true
	}
}

func (s *streamState) consumeActiveRunCtrlCCancelNoReplay() bool {
	if s == nil {
		return false
	}
	v := s.activeRunCtrlCCancelNoReplay
	s.activeRunCtrlCCancelNoReplay = false
	return v
}

func (s *streamState) composerDisplay() string {
	if s == nil {
		return ""
	}
	return buildComposerDisplayText(s.composer.DraftText, s.composer.PendingPastes, s.composer.Attachments)
}

func (s *streamState) composerDisplayCursor() int {
	if s == nil {
		return 0
	}
	cursor := composerCursorClamp(s.composer.DraftText, s.composer.Cursor)
	prefix := composerTextRunesPrefix(s.composer.DraftText, cursor)
	displayPrefix := renderInlineAttachmentPlaceholders(prefix, s.composer.Attachments, renderComposerImagePlaceholder)
	displayPrefix = renderInlinePastePlaceholders(displayPrefix, s.composer.PendingPastes, renderComposerPastePlaceholder)
	return len([]rune(displayPrefix))
}

func (s *streamState) composerPendingInputPreview() ComposerPendingInputPreview {
	if s == nil {
		return ComposerPendingInputPreview{}
	}
	preview := ComposerPendingInputPreview{
		PendingSteers:  make([]string, 0, len(s.pendingSteers)),
		RejectedSteers: make([]string, 0, len(s.rejectedSteers)),
		QueuedMessages: make([]string, 0, len(s.queuedTurns)),
	}
	for _, submission := range s.pendingSteers {
		if text := composerSubmissionPreview(submission); text != "" {
			preview.PendingSteers = append(preview.PendingSteers, text)
		}
	}
	for _, item := range s.rejectedSteers {
		if text := composerSubmissionPreview(item.Submission); text != "" {
			preview.RejectedSteers = append(preview.RejectedSteers, text)
		}
	}
	for _, item := range s.queuedTurns {
		if text := composerSubmissionPreview(item.Submission); text != "" {
			preview.QueuedMessages = append(preview.QueuedMessages, text)
		}
	}
	return preview
}

func composerSubmissionPreview(submission ComposerSubmission) string {
	text := strings.TrimSpace(submission.DisplayText)
	if text == "" {
		text = strings.TrimSpace(submission.Text)
	}
	if text != "" {
		return strings.Join(strings.Fields(text), " ")
	}
	if len(submission.Attachments) == 1 {
		return "1 attachment"
	}
	if len(submission.Attachments) > 1 {
		return fmt.Sprintf("%d attachments", len(submission.Attachments))
	}
	return ""
}

func (s *streamState) composerSubmission() (ComposerSubmission, bool) {
	if s == nil {
		return ComposerSubmission{}, false
	}
	attachments := append([]InputAttachment(nil), s.composer.Attachments...)
	textWithPastePlaceholders := buildComposerText(s.composer.Text, s.composer.PendingPastes, nil)
	displayText := buildComposerText(s.composer.Text, s.composer.PendingPastes, s.composer.Attachments)
	actualText := expandComposerText(textWithPastePlaceholders, s.composer.PendingPastes)
	actualText = stripComposerAttachmentPlaceholders(actualText, attachments)
	submission, ok := buildComposerSubmission(actualText, displayText, attachments)
	submission.PendingPastes = append([]PendingPaste(nil), s.composer.PendingPastes...)
	return submission, ok
}

func buildComposerSubmission(actualText string, displayText string, attachments []InputAttachment) (ComposerSubmission, bool) {
	parts := make([]llm.ContentPart, 0, 1+len(attachments))
	if strings.TrimSpace(actualText) != "" {
		parts = append(parts, llm.Text(actualText))
	}
	for _, att := range attachments {
		if strings.TrimSpace(att.Path) == "" {
			continue
		}
		part, err := llm.ImageFile(strings.TrimSpace(att.Path))
		if err != nil {
			continue
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return ComposerSubmission{}, false
	}
	return ComposerSubmission{
		Text:        actualText,
		Parts:       parts,
		DisplayText: displayText,
		Attachments: append([]InputAttachment(nil), attachments...),
	}, true
}

func (s *streamState) commitDraftForSubmission() {
	if s == nil {
		return
	}
	// A recalled item was removed from its queue before it entered the
	// composer. Keep composer.Text empty until Enter/queue explicitly commits
	// it; otherwise an interrupted run can mistake the visible draft for an
	// automatic submission.
	if s.restoredQueuedSubmissionActive {
		return
	}
	if strings.TrimSpace(s.composer.Text) == "" {
		s.composer.Text = strings.TrimSpace(s.composer.DraftText)
	}
}

func (s *streamState) rememberRestoredQueuedSubmission(submission ComposerSubmission) {
	if s == nil {
		return
	}
	s.restoredQueuedSubmission = ComposerSubmission{
		PendingPastes: append([]PendingPaste(nil), submission.PendingPastes...),
		Text:          strings.TrimSpace(submission.Text),
		Parts:         append([]llm.ContentPart(nil), submission.Parts...),
		DisplayText:   strings.TrimSpace(submission.DisplayText),
		RawInput:      strings.TrimSpace(submission.RawInput),
		Attachments:   append([]InputAttachment(nil), submission.Attachments...),
		GoalObjective: strings.TrimSpace(submission.GoalObjective),
		SkillName:     strings.TrimSpace(submission.SkillName),
		SkillPath:     strings.TrimSpace(submission.SkillPath),
	}
	s.restoredQueuedSubmissionActive = true
}

func (s *streamState) clearRestoredQueuedSubmission() {
	if s == nil {
		return
	}
	s.restoredQueuedSubmission = ComposerSubmission{}
	s.restoredQueuedSubmissionActive = false
}

func (s *streamState) restoreQueuedSubmission(submission ComposerSubmission) {
	if s == nil {
		return
	}
	s.resetComposerForActiveRunDraft()
	// DraftText is display state only. The original structured submission lives
	// in recall metadata and is selected explicitly on submit.
	s.composer.DraftText = strings.TrimSpace(submission.DisplayText)
	s.composer.Cursor = len([]rune(s.composer.DraftText))
	s.composer.Attachments = append([]InputAttachment(nil), submission.Attachments...)
	s.composer.PendingPastes = append([]PendingPaste(nil), submission.PendingPastes...)
	if s.composer.DraftText == "" {
		s.composer.DraftText = strings.TrimSpace(s.composer.Text)
		s.composer.Cursor = len([]rune(s.composer.DraftText))
	}
	s.rememberRestoredQueuedSubmission(submission)
}

// enqueuePendingSteer, enqueueRejectedSteer and enqueueTurn are the only ways
// into the three queues. They exist so every queued message is stamped with the
// shared clock: an unstamped message would sort as the oldest and could never be
// recalled while anything else was queued.
func (s *streamState) enqueuePendingSteer(submission ComposerSubmission) {
	if s == nil {
		return
	}
	submission.QueueSeq = s.nextQueueSeq()
	s.pendingSteers = append(s.pendingSteers, submission)
}

func (s *streamState) enqueueRejectedSteer(submission ComposerSubmission) {
	if s == nil {
		return
	}
	submission.QueueSeq = s.nextQueueSeq()
	s.rejectedSteers = append(s.rejectedSteers, queuedSubmission{
		Action:     queuedSubmissionActionRejectedSteer,
		Submission: submission,
	})
}

func (s *streamState) enqueueTurn(submission ComposerSubmission, action queuedSubmissionAction) {
	if s == nil {
		return
	}
	submission.QueueSeq = s.nextQueueSeq()
	s.queuedTurns = append(s.queuedTurns, queuedSubmission{Action: action, Submission: submission})
}

func (s *streamState) nextQueueSeq() int {
	s.queueSeq++
	return s.queueSeq
}

// discardQueuedInput empties every queue and returns how many messages were
// discarded. Pending steers are retracted from the run's input runtime first:
// clearing only the local mirror would leave the runtime free to hand them to
// the model, with nothing left on this side to render them into the transcript.
// Steers already delivered cannot be retracted and are not counted — the model
// has them, so they are not lost.
func (s *streamState) discardQueuedInput() int {
	if s == nil {
		return 0
	}
	dropped := 0
	for i := len(s.pendingSteers) - 1; i >= 0; i-- {
		if s.session == nil || !s.session.RetractSurfaceSteer(s.sessionID, "tui") {
			break
		}
		dropped++
	}
	s.pendingSteers = nil
	dropped += len(s.queuedTurns) + len(s.rejectedSteers)
	s.queuedTurns = nil
	s.rejectedSteers = nil
	return dropped
}

func pluralizeMessages(n int) string {
	if n == 1 {
		return "1 queued message"
	}
	return fmt.Sprintf("%d queued messages", n)
}

// reconcilePendingSteers drops steers from the local mirror that the active run
// has already delivered to the model at a tool boundary, and returns them so the
// caller can move them into the transcript. The mirror is only cleared wholesale
// at turn boundaries, so without this it keeps showing delivered steers as
// "submitted after next tool call" — they can no longer be retracted for
// editing, and any resubmission stacks on top of the stale entry instead of
// replacing it.
func (s *streamState) reconcilePendingSteers(session Session) []ComposerSubmission {
	if s == nil || session == nil || len(s.pendingSteers) == 0 {
		return nil
	}
	live, ok := session.SurfacePendingSteerCount(s.sessionID, "tui")
	if !ok {
		return nil
	}
	return s.reconcilePendingSteersCount(live)
}

// reconcilePendingSteersCount is the same reconciliation driven by the live
// count carried on PendingSteersChangedMsg. Steers are enqueued into the runtime
// in lockstep with the mirror and drained in FIFO order, so the surviving
// runtime steers are always a suffix of the mirror: keep that suffix and hand
// back the delivered prefix.
func (s *streamState) reconcilePendingSteersCount(live int) []ComposerSubmission {
	if s == nil || len(s.pendingSteers) == 0 {
		return nil
	}
	if live < 0 {
		live = 0
	}
	if live >= len(s.pendingSteers) {
		return nil
	}
	cut := len(s.pendingSteers) - live
	delivered := append([]ComposerSubmission(nil), s.pendingSteers[:cut]...)
	s.pendingSteers = append([]ComposerSubmission(nil), s.pendingSteers[cut:]...)
	return delivered
}

// renderDeliveredSteerMessages renders each just-delivered steer as a user
// transcript message ("you", gray composer style) so a steer that was queued
// and then handed to the model at a tool boundary appears in history instead of
// silently vanishing from the pending preview. Both reconcile paths pop the
// mirror destructively, so whichever observes the delivery first renders it and
// the other finds nothing left to render.
func renderDeliveredSteerMessages(renderer *Renderer, delivered []ComposerSubmission) {
	if renderer == nil {
		return
	}
	for _, submission := range delivered {
		if text := submissionTranscriptText(submission); text != "" {
			renderer.RenderFrame(Frame{Kind: FrameUser, Title: "you", Content: text, Final: true})
		}
	}
}

// submissionTranscriptText is what a submission looks like in history: the
// composer's display form, which keeps "[Image #1]" and "[Pasted Content N
// chars]" placeholders that Text/Parts have already stripped or expanded for the
// model. Falls back to the model-facing text only when there is no display form.
func submissionTranscriptText(submission ComposerSubmission) string {
	if text := strings.TrimSpace(submission.DisplayText); text != "" {
		return text
	}
	return strings.TrimSpace(submission.Text)
}

// restoreLatestQueuedEditableSubmission pulls the most recently queued message
// back into the composer for editing. "Most recently queued" is decided by the
// shared enqueue clock, not by queue precedence: the three queues are separate
// FIFOs, so picking one of them first would recall an older message whenever the
// newest one happened to land in another queue.
//
// The message is removed from its queue before it is restored, so clearing the
// composer afterwards cancels it rather than leaving a ghost submission behind.
func (s *streamState) restoreLatestQueuedEditableSubmission(session Session) bool {
	if s == nil {
		return false
	}
	// A pending steer also lives in the run's TurnInputRuntime, so the runtime
	// copy has to be retracted before the message can be handed back. A failed
	// retract means the run already delivered it to the model: skip it and try
	// the next-newest candidate rather than handing the user an editable copy of
	// a message that is already being answered.
	// skipPendingSteers is scratch for this attempt only. Nothing about a failed
	// retract is worth remembering: the queues move underneath us as the run
	// delivers messages, so a count kept across attempts would go stale and hide
	// steers that are perfectly recallable.
	skipPendingSteers := 0
	for {
		newest, ok := s.newestQueuedSubmission(skipPendingSteers)
		if !ok {
			return false
		}
		if newest.queue == queueKindPendingSteer {
			if session == nil || !session.RetractSurfaceSteer(s.sessionID, "tui") {
				// Undeliverable, and the runtime drains FIFO, so every older
				// pending steer is already gone too. Skip the whole queue rather
				// than retrying entries that cannot come back.
				skipPendingSteers = len(s.pendingSteers)
				continue
			}
		}
		s.restoreRecalledSubmission(s.takeQueuedSubmission(newest))
		return true
	}
}

type queueKind int

const (
	queueKindPendingSteer queueKind = iota
	queueKindRejectedSteer
	queueKindTurn
)

type queuePosition struct {
	queue queueKind
	index int
	seq   int
}

// newestQueuedSubmission returns the position of the highest-sequence message
// across the three queues. Each queue is append-only in sequence order, so only
// the tail of each has to be considered.
func (s *streamState) newestQueuedSubmission(skipPendingSteers int) (queuePosition, bool) {
	var best queuePosition
	found := false
	consider := func(queue queueKind, index int, seq int) {
		if !found || seq > best.seq {
			best = queuePosition{queue: queue, index: index, seq: seq}
			found = true
		}
	}
	if n := len(s.pendingSteers) - skipPendingSteers; n > 0 {
		consider(queueKindPendingSteer, n-1, s.pendingSteers[n-1].QueueSeq)
	}
	if n := len(s.rejectedSteers); n > 0 {
		consider(queueKindRejectedSteer, n-1, s.rejectedSteers[n-1].Submission.QueueSeq)
	}
	if n := len(s.queuedTurns); n > 0 {
		consider(queueKindTurn, n-1, s.queuedTurns[n-1].Submission.QueueSeq)
	}
	return best, found
}

func (s *streamState) takeQueuedSubmission(pos queuePosition) ComposerSubmission {
	switch pos.queue {
	case queueKindPendingSteer:
		submission := s.pendingSteers[pos.index]
		s.pendingSteers = append(s.pendingSteers[:pos.index], s.pendingSteers[pos.index+1:]...)
		return submission
	case queueKindRejectedSteer:
		item := s.rejectedSteers[pos.index]
		s.rejectedSteers = append(s.rejectedSteers[:pos.index], s.rejectedSteers[pos.index+1:]...)
		return item.Submission
	default:
		item := s.queuedTurns[pos.index]
		s.queuedTurns = append(s.queuedTurns[:pos.index], s.queuedTurns[pos.index+1:]...)
		return item.Submission
	}
}

func (s *streamState) restoreRecalledSubmission(submission ComposerSubmission) {
	s.restoreQueuedSubmission(submission)
	if strings.TrimSpace(s.composer.DraftText) != "" {
		seedInteractiveInput(s.composer.DraftText, s.composer.Cursor)
	}
}

func (s *streamState) restoreDraftSubmission(submission ComposerSubmission) {
	if s == nil {
		return
	}
	s.resetComposerForActiveRunDraft()
	s.composer.DraftText = strings.TrimSpace(submission.DisplayText)
	s.composer.Cursor = len([]rune(s.composer.DraftText))
	s.composer.Attachments = append([]InputAttachment(nil), submission.Attachments...)
	s.composer.PendingPastes = append([]PendingPaste(nil), submission.PendingPastes...)
	if s.composer.DraftText == "" {
		s.composer.DraftText = strings.TrimSpace(submission.Text)
		s.composer.Cursor = len([]rune(s.composer.DraftText))
	}
	// Retain the original structured payload for an unchanged resend. Edited
	// drafts rebuild from the recovered attachment and folded-paste references.
	s.rememberRestoredQueuedSubmission(submission)
	s.holdComposer = true
	if strings.TrimSpace(s.composer.DraftText) != "" {
		seedInteractiveInput(s.composer.DraftText, s.composer.Cursor)
	}
}

// restoreWithdrawnWork recovers the withdrawn input, queued work in arrival
// order, and the newer draft as ONE editable draft. It is called immediately on
// Esc so the composer comes back without waiting for the run to drain, then
// again at the cleanup barrier to fold in anything that arrived while it
// drained.
//
// Merging rather than re-queuing is the deliberate choice. Every follow-up
// behind the withdrawn message was written as a follow-up TO it, so putting
// those back on the queue would leave them answering a message that no longer
// exists — and the recovery target is a composer, which has no way to hold
// several distinct pending turns around a hole. One draft in written order is
// the state the user can actually edit and resend.
//
// Ordinary cancellation is unchanged: it still restores the queue as a queue and
// still honours interrupt-and-send. This path is reached only by a withdrawal.
func (s *streamState) restoreWithdrawnWork(session Session) {
	foreground := s.activeForeground
	if foreground == nil {
		return
	}
	first := !foreground.restored
	queuedCount := len(s.pendingSteers) + len(s.rejectedSteers) + len(s.queuedTurns)
	if !first && queuedCount == 0 {
		return
	}
	// oldest leads the merge. On the first pass that is the withdrawn message
	// itself; on a later pass it is whatever the first pass already recovered
	// into the composer, which predates everything queued since.
	var oldest, rest []ComposerSubmission
	if first {
		original := cloneComposerSubmission(foreground.submission)
		if original.RawInput != "" {
			original.Text = original.RawInput
			original.DisplayText = original.RawInput
			original.Parts = []llm.ContentPart{llm.Text(original.RawInput)}
			original.RawInput, original.GoalObjective, original.SkillName, original.SkillPath = "", "", "", ""
		}
		oldest = append(oldest, original)
		foreground.restored = true
	} else if draft, ok := s.currentDraftSubmission(); ok {
		oldest = append(oldest, draft)
	}
	queued := append([]ComposerSubmission(nil), s.pendingSteers...)
	for range s.pendingSteers {
		if session != nil {
			session.RetractSurfaceSteer(s.sessionID, "tui")
		}
	}
	for _, item := range s.rejectedSteers {
		queued = append(queued, item.Submission)
	}
	for _, item := range s.queuedTurns {
		queued = append(queued, item.Submission)
	}
	sort.SliceStable(queued, func(i, j int) bool { return queued[i].QueueSeq < queued[j].QueueSeq })
	rest = append(rest, queued...)
	s.pendingSteers, s.rejectedSteers, s.queuedTurns = nil, nil, nil
	if first {
		// Uncommitted typing is the newest thing the user did, so it trails.
		if draft, ok := s.currentDraftSubmission(); ok {
			rest = append(rest, draft)
		}
	}
	s.submitPendingSteersAfterInterrupt = false
	s.userInterrupted = false
	s.restoreDraftSubmission(mergeComposerSubmissions(append(oldest, rest...)))
}

// currentDraftSubmission reads what the composer holds without committing it —
// the interrupted-turn path uses it to fold the visible draft into the work it
// restores. It must apply the same recall semantics as the submit path: a
// recalled message deliberately leaves composer.Text empty (so an interrupt
// cannot auto-submit it), so building straight from the composer would report
// "nothing here" and the interrupt would then overwrite the draft with the rest
// of the restored queue, losing the recalled message outright.
func (s *streamState) currentDraftSubmission() (ComposerSubmission, bool) {
	if s == nil {
		return ComposerSubmission{}, false
	}
	composer := s.composer
	hold := s.holdComposer
	submission, ok := s.submissionFromCurrentDraft()
	s.composer = composer
	s.holdComposer = hold
	return submission, ok
}

// submissionFromCurrentDraft explicitly commits a recalled draft. An unchanged
// recall returns the original structured payload; an edited recall is rebuilt
// from the current composer state. Empty text and attachments cancel cleanly.
func (s *streamState) submissionFromCurrentDraft() (ComposerSubmission, bool) {
	if s == nil {
		return ComposerSubmission{}, false
	}
	if s.restoredQueuedSubmissionActive {
		original := s.restoredQueuedSubmission
		display := buildComposerDisplayText(s.composer.DraftText, s.composer.PendingPastes, s.composer.Attachments)
		if strings.TrimSpace(display) == strings.TrimSpace(original.DisplayText) &&
			inputAttachmentsEqual(s.composer.Attachments, original.Attachments) {
			return cloneComposerSubmission(original), len(original.Parts) > 0
		}
		// The recalled item was edited. Build a new payload from the edited
		// draft, explicitly committing its text for this user action only.
		if strings.TrimSpace(s.composer.Text) == "" {
			s.composer.Text = strings.TrimSpace(s.composer.DraftText)
		}
	}
	composer := s.composer
	s.commitDraftForSubmission()
	submission, ok := s.composerSubmission()
	s.composer = composer
	return submission, ok
}

func cloneComposerSubmission(in ComposerSubmission) ComposerSubmission {
	in.PendingPastes = append([]PendingPaste(nil), in.PendingPastes...)
	in.Parts = append([]llm.ContentPart(nil), in.Parts...)
	in.Attachments = append([]InputAttachment(nil), in.Attachments...)
	return in
}

func inputAttachmentsEqual(a, b []InputAttachment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *streamState) resetComposerForActiveRunDraft() {
	if s == nil {
		return
	}
	s.composer.Text = ""
	s.composer.DraftText = ""
	s.composer.Cursor = 0
	s.composer.PendingPastes = nil
	s.composer.Attachments = nil
	s.composer.SlashOverlay = nil
	s.holdComposer = false
	s.clearHistoryBrowse()
	s.clearRestoredQueuedSubmission()
}

// renumberImagePlaceholders shifts a submission's "[Image #N]" markers by offset
// so they address the merged attachment list instead of the submission's own.
// One Replacer pass, so a rewritten marker is never re-matched by a later pair.
func renumberImagePlaceholders(display string, offset int, count int) string {
	if offset <= 0 || count <= 0 || display == "" {
		return display
	}
	pairs := make([]string, 0, 2*count)
	for i := 0; i < count; i++ {
		pairs = append(pairs, imageAttachmentPlaceholder(i), imageAttachmentPlaceholder(offset+i))
	}
	return strings.NewReplacer(pairs...).Replace(display)
}

func mergeComposerSubmissions(items []ComposerSubmission) ComposerSubmission {
	if len(items) == 0 {
		return ComposerSubmission{}
	}
	if len(items) == 1 {
		return items[0]
	}
	var displayParts []string
	var textParts []string
	var rawParts []string
	merged := ComposerSubmission{
		Parts:       make([]llm.ContentPart, 0),
		Attachments: make([]InputAttachment, 0),
	}
	for _, item := range items {
		if strings.TrimSpace(item.Text) != "" {
			textParts = append(textParts, strings.TrimSpace(item.Text))
		}
		if strings.TrimSpace(item.DisplayText) != "" {
			// Each item numbered its own placeholders from #1, so concatenating
			// display texts verbatim yields several "[Image #1]" and no "[Image
			// #2]". The composer resolves attachments by placeholder index, so
			// syncAttachmentsWithDraft would then drop every attachment past the
			// first as soon as the merged draft is edited. Renumber against the
			// merged attachment list instead.
			display := renumberImagePlaceholders(strings.TrimSpace(item.DisplayText), len(merged.Attachments), len(item.Attachments))
			// Folded pastes also have per-submission names. Rename in one pass,
			// longest first, so same-sized pastes remain independently editable.
			pastes := append([]PendingPaste(nil), item.PendingPastes...)
			sort.SliceStable(pastes, func(i, j int) bool { return len(pastes[i].Placeholder) > len(pastes[j].Placeholder) })
			var replacements []string
			for _, paste := range pastes {
				if paste.Placeholder == "" {
					continue
				}
				old := paste.Placeholder
				paste.Placeholder = nextLargePastePlaceholderForCount(len([]rune(paste.Content)), merged.PendingPastes)
				merged.PendingPastes = append(merged.PendingPastes, paste)
				replacements = append(replacements, old, paste.Placeholder)
			}
			if len(replacements) > 0 {
				display = strings.NewReplacer(replacements...).Replace(display)
			}
			displayParts = append(displayParts, display)
		}
		if strings.TrimSpace(item.RawInput) != "" {
			rawParts = append(rawParts, strings.TrimSpace(item.RawInput))
		}
		// A turn carries at most one goal objective and one explicit skill
		// activation, so keep the first of each rather than dropping them.
		// Without this, merging queued/rejected input silently degrades /goal
		// into a plain prompt (no continuation loop) and /skill into untrusted
		// prompt text. Slash commands are expanded before queueing, so the
		// structured payload has to survive the merge.
		if merged.GoalObjective == "" && strings.TrimSpace(item.GoalObjective) != "" {
			merged.GoalObjective = strings.TrimSpace(item.GoalObjective)
		}
		if merged.SkillName == "" && strings.TrimSpace(item.SkillName) != "" {
			merged.SkillName = strings.TrimSpace(item.SkillName)
			merged.SkillPath = strings.TrimSpace(item.SkillPath)
		}
		merged.Attachments = append(merged.Attachments, item.Attachments...)
	}
	merged.Text = strings.Join(textParts, "\n\n")
	merged.DisplayText = strings.Join(displayParts, "\n")
	merged.RawInput = strings.Join(rawParts, "\n")
	if merged.Text != "" {
		merged.Parts = append(merged.Parts, llm.Text(merged.Text))
	}
	for _, att := range merged.Attachments {
		if strings.TrimSpace(att.Path) == "" {
			continue
		}
		part, err := llm.ImageFile(strings.TrimSpace(att.Path))
		if err != nil {
			continue
		}
		merged.Parts = append(merged.Parts, part)
	}
	return merged
}

// renderComposerWithState renders the composer plus any active slash overlay
// rows by translating SlashOverlay state into ComposerRenderState. Falls back
// to the legacy single-line composer render when the overlay is nil. Also
// keeps the slashOverlayActive atomic gate in sync with the live overlay so
// the raw reader routes arrow keys to overlay nav vs. history nav correctly.
func renderComposerWithState(renderer *Renderer, state *streamState) {
	if renderer == nil || state == nil {
		return
	}
	overlay := state.composer.SlashOverlay
	mentionOv := state.composer.MentionOverlay
	slashOverlayActive.Store(overlay != nil && overlay.Active)
	mentionOverlayActive.Store(mentionOv != nil && mentionOv.Active)
	// Queue recall is handled by explicit modified key sequences; no global
	// bare-Up queue gate is maintained.
	rosterActive := agentRosterHasRunningSubagent(state.agentRoster)
	if !rosterActive {
		// Guards against stale focus if the subagent finishes mid-navigation.
		state.agentRosterFocused = false
	}
	agentRosterActive.Store(rosterActive)
	agentRosterFocused.Store(state.agentRosterFocused)
	rosterStopArmed := false
	if state.agentRosterFocused && rosterActive {
		if row, ok := state.selectedAgentRosterRow(); ok && strings.EqualFold(strings.TrimSpace(row.Kind), "subagent") {
			rosterStopArmed = true
		}
	}
	agentRosterStopArmed.Store(rosterStopArmed)
	if overlay == nil || !overlay.Active {
		var hint string
		if overlay != nil && overlay.HintCommand != nil && showArgumentHint(state.composer.DraftText, overlay.HintCommand) {
			hint = overlay.HintCommand.ArgumentHint
		}
		var rows []OverlayRow
		if mentionOv != nil && mentionOv.Active {
			rows = overlayRowsFromMention(mentionOv)
		}
		cursor := state.composerDisplayCursor()
		renderer.RenderComposerState(ComposerRenderState{
			Text:           state.composerDisplay(),
			Cursor:         &cursor,
			PendingInput:   state.composerPendingInputPreview(),
			AgentRoster:    state.agentRoster,
			RosterSelected: state.agentRosterSelected,
			RosterFocused:  state.agentRosterFocused,
			ArgumentHint:   hint,
			OverlayRows:    rows,
		})
		return
	}
	menu := slashMenuPanel(overlay)
	cursor := state.composerDisplayCursor()
	renderer.RenderComposerState(ComposerRenderState{
		Text:           state.composerDisplay(),
		Cursor:         &cursor,
		PendingInput:   state.composerPendingInputPreview(),
		AgentRoster:    state.agentRoster,
		RosterSelected: state.agentRosterSelected,
		RosterFocused:  state.agentRosterFocused,
		SlashMenu:      &menu,
	})
}

// slashMenuPanel is the slash menu as a slash panel under the composer: the
// built-in commands and the skills, each group under its heading, every
// command's description in one column beside the names.
func slashMenuPanel(o *SlashOverlay) slashPanel {
	b := newPanelBuilder()
	b.hint("↑/↓ to navigate · Tab to complete · Enter to run · Esc to close")
	if o == nil || len(o.Visible) == 0 {
		b.text(panelIndent, "No matching commands", nil)
		return b.panel()
	}
	nameWidth := 0
	for _, cmd := range o.Visible {
		nameWidth = max(nameWidth, runewidth.StringWidth("/"+cmd.Name))
	}
	group := ""
	for i, cmd := range o.Visible {
		if g := slashGroupOf(cmd); g != group {
			b.group(g)
			group = g
		}
		name := "/" + cmd.Name
		lead := name + strings.Repeat(" ", nameWidth-runewidth.StringWidth(name)+2)
		b.selectable(i == o.SelectedIdx, lead, strings.TrimSpace(cmd.Description))
	}
	return b.panel()
}

func (s *streamState) appendPaste(content string) {
	if s == nil {
		return
	}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	content = sanitizeTerminalInputText(content)
	if content == "" {
		return
	}
	// Paste invalidates slash overlay: text no longer starts with bare "/".
	s.composer.SlashOverlay = nil
	if shouldFoldLargePaste(content) {
		s.nextPasteID++
		placeholder := nextLargePastePlaceholderForCount(len([]rune(content)), s.composer.PendingPastes)
		s.composer.PendingPastes = append(s.composer.PendingPastes, PendingPaste{
			ID:          fmt.Sprintf("paste-%d", s.nextPasteID),
			Content:     content,
			Placeholder: placeholder,
		})
		// Insert the placeholder into the editable draft like image
		// attachments do: syncPendingPastesWithDraft drops any pending paste
		// whose placeholder is missing from the draft, so a placeholder that
		// lives only in PendingPastes would not survive the next draft event.
		line, cursor := insertRunesAtCursor([]rune(s.composer.DraftText), s.composer.Cursor, []rune(placeholder))
		s.composer.Text = ""
		s.composer.DraftText = string(line)
		s.composer.Cursor = cursor
		s.holdComposer = true
		seedInteractiveInput(s.composer.DraftText, s.composer.Cursor)
		return
	}
	if strings.TrimSpace(s.composer.DraftText) == "" {
		s.composer.DraftText = content
		s.composer.Text = content
		s.composer.Cursor = len([]rune(s.composer.DraftText))
		s.holdComposer = true
		seedInteractiveInput(s.composer.DraftText, s.composer.Cursor)
		return
	}
	line, cursor := insertRunesAtCursor([]rune(s.composer.DraftText), s.composer.Cursor, []rune(content))
	draft := string(line)
	s.composer.DraftText = draft
	s.composer.Text = draft
	s.composer.Cursor = cursor
	if s.composer.Cursor > len([]rune(draft)) {
		s.composer.Cursor = len([]rune(draft))
	}
	s.holdComposer = true
	seedInteractiveInput(s.composer.DraftText, s.composer.Cursor)
}

// prepareHistoryDraft restores the non-text composer state associated with an
// arrow-key history item. The raw input buffer only contains its visible
// placeholders, so attachments and folded paste bodies must travel alongside
// the draft. When Down returns to the user's original draft, restore the
// metadata that was present before history browsing began.
func (s *streamState) prepareHistoryDraft(ev inputEvent, draft string) {
	if s == nil {
		return
	}
	if ev.historyNavigation {
		if ev.historyIndex >= 0 {
			if !s.historyBrowseActive {
				s.historyDraftAttachments = append([]InputAttachment(nil), s.composer.Attachments...)
				s.historyDraftPastes = append([]PendingPaste(nil), s.composer.PendingPastes...)
			}
			s.historyBrowseActive = true
			s.composer.Attachments = append([]InputAttachment(nil), ev.historyEntry.Attachments...)
			s.composer.PendingPastes = append([]PendingPaste(nil), ev.historyEntry.PendingPastes...)
			return
		}
		if s.historyBrowseActive {
			s.composer.Attachments = append([]InputAttachment(nil), s.historyDraftAttachments...)
			s.composer.PendingPastes = append([]PendingPaste(nil), s.historyDraftPastes...)
		}
		s.clearHistoryBrowse()
		return
	}
	// Cursor-only draft events do not leave history browsing. Actual editing
	// does, and subsequent sync calls prune metadata whose placeholder was
	// deleted by that edit.
	if s.historyBrowseActive && draft != s.composer.DraftText {
		s.clearHistoryBrowse()
	}
}

func (s *streamState) enrichSubmittedInputHistory(ev inputEvent) {
	if s == nil || !ev.historyEntryRecorded || ev.historyIndex < 0 {
		return
	}
	enrichRawInputHistory(s.inputHistoryStore, ev.historyIndex, rawInputHistoryEntry{
		Text:          ev.line,
		Attachments:   append([]InputAttachment(nil), s.composer.Attachments...),
		PendingPastes: append([]PendingPaste(nil), s.composer.PendingPastes...),
	})
}

func (s *streamState) clearHistoryBrowse() {
	if s == nil {
		return
	}
	s.historyBrowseActive = false
	s.historyDraftAttachments = nil
	s.historyDraftPastes = nil
}

func (s *streamState) syncPendingPastesWithDraft(draft string) {
	if s == nil || len(s.composer.PendingPastes) == 0 {
		return
	}
	draft = strings.TrimSpace(draft)
	if draft == "" {
		s.composer.PendingPastes = nil
		return
	}
	pastes := s.composer.PendingPastes
	for len(pastes) > 0 {
		placeholder := strings.TrimSpace(pastes[len(pastes)-1].Placeholder)
		if placeholder == "" {
			pastes = pastes[:len(pastes)-1]
			continue
		}
		if strings.Contains(draft, placeholder) {
			break
		}
		pastes = pastes[:len(pastes)-1]
	}
	s.composer.PendingPastes = pastes
}

func (s *streamState) syncAttachmentsWithDraft(draft string) {
	if s == nil || len(s.composer.Attachments) == 0 {
		return
	}
	draft = strings.TrimSpace(draft)
	if draft == "" {
		s.composer.Attachments = nil
		return
	}
	attachments := s.composer.Attachments
	for len(attachments) > 0 {
		if strings.Contains(draft, imageAttachmentPlaceholder(len(attachments)-1)) {
			break
		}
		attachments = attachments[:len(attachments)-1]
	}
	s.composer.Attachments = attachments
}

// applyMentionAcceptance installs an accepted mention into the composer. An
// image selection is attached rather than named, so its placeholder lands
// exactly where the token it replaced used to be.
func (s *streamState) applyMentionAcceptance(acc MentionAcceptance) {
	if s == nil {
		return
	}
	s.composer.DraftText = acc.Draft
	s.composer.Cursor = composerCursorClamp(acc.Draft, acc.Cursor)
	if strings.TrimSpace(acc.ImagePath) != "" {
		s.appendImageAttachment(InputAttachment{
			Path:  acc.ImagePath,
			Label: filepath.Base(acc.ImagePath),
		})
		return
	}
	seedInteractiveInput(s.composer.DraftText, s.composer.Cursor)
}

func (s *streamState) appendImageAttachment(att InputAttachment) string {
	if s == nil {
		return ""
	}
	s.composer.SlashOverlay = nil
	s.composer.Attachments = append(s.composer.Attachments, att)
	placeholder := imageAttachmentPlaceholder(len(s.composer.Attachments) - 1)
	line, cursor := insertRunesAtCursor([]rune(s.composer.DraftText), s.composer.Cursor, []rune(placeholder))
	s.composer.Text = ""
	s.composer.DraftText = string(line)
	s.composer.Cursor = cursor
	s.holdComposer = true
	seedInteractiveInput(s.composer.DraftText, s.composer.Cursor)
	return placeholder
}

// awaitInputOrSignal blocks until the reader does something, processing
// notifications while it waits. wake, when non-nil, is asked after each
// notification whether the loop has work of its own now — a continuation whose
// wait just ended — and a true answer returns an inputEventWake.
func awaitInputOrSignal(ctx context.Context, sigCh <-chan os.Signal, events <-chan inputEvent, notifyCh <-chan any, processNotify func(any), wake func() bool) (inputEvent, bool, error) {
	for {
		// Drain any pending event before checking notifications so that user
		// input is never starved by a burst of streaming-token notifications.
		select {
		case item, ok := <-events:
			if !ok {
				return inputEvent{}, false, nil
			}
			if item.kind == inputEventDone {
				if err := item.err; err != nil && !errors.Is(err, io.EOF) {
					return inputEvent{}, false, err
				}
				return inputEvent{}, false, nil
			}
			return item, true, nil
		default:
		}
		select {
		case <-ctx.Done():
			return inputEvent{}, false, nil
		case <-sigCh:
			return inputEvent{kind: inputEventHotkey, hotkey: hotkeyInterrupt}, true, nil
		case item, ok := <-events:
			if !ok {
				return inputEvent{}, false, nil
			}
			if item.kind == inputEventDone {
				if err := item.err; err != nil && !errors.Is(err, io.EOF) {
					return inputEvent{}, false, err
				}
				return inputEvent{}, false, nil
			}
			return item, true, nil
		case m := <-notifyCh:
			if processNotify != nil {
				processNotify(m)
			}
			if wake != nil && wake() {
				return inputEvent{kind: inputEventWake}, true, nil
			}
			continue
		}
	}
}

// drainNotifications processes all immediately-available notifications without
// blocking. Used at the top of each idle-loop iteration to flush notifications
// that arrived while the loop was busy with other work (rendering, slash
// commands, etc.).
func drainNotifications(notifyCh <-chan any, processNotify func(any)) {
	if notifyCh == nil || processNotify == nil {
		return
	}
	for {
		select {
		case m, ok := <-notifyCh:
			if !ok {
				return
			}
			processNotify(m)
		default:
			return
		}
	}
}

type runTurnDisposition string

const (
	runTurnCompleted   runTurnDisposition = "completed"
	runTurnInterrupted runTurnDisposition = "interrupted"
	runTurnCancelled   runTurnDisposition = "cancelled"
	runTurnWithdrawn   runTurnDisposition = "withdrawn"
)

// runTurnWithSkill drives one agent turn. resetCtrlCOnEntry normalizes the
// Ctrl+C cancel/quit escalation sequence at the start of the turn: true for
// every direct caller (a freshly started turn, isolated from whatever came
// before) and for a fresh user-initiated submission via
// executeComposerSubmission. It must be false only for a turn that
// executeComposerSubmission starts as an automatic continuation
// (preserveComposer true - a queued/pending submission replayed by
// nextAutomaticSubmission right after the previous turn ended), because that
// continuation is not a new user action: resetting there would silently
// discard cancel/quit progress the user already built up with Ctrl+C,
// forcing the escalation to restart from "cancel" on every chained turn and
// making sustained Ctrl+C presses unable to ever reach "quit".
func runTurnWithSkill(ctx context.Context, sigCh <-chan os.Signal, events <-chan inputEvent, notifyCh <-chan any, processNotify func(any), session Session, renderer *Renderer, tracker *Tracker, state *streamState, line string, displayText string, rawInput string, attachments []InputAttachment, goalObjective string, skillName string, skillPath string, clipboard ClipboardImageReader, cmds *commandController, turnWorkedStatus *string, resetCtrlCOnEntry bool) (runTurnDisposition, error) {
	errCh := make(chan error, 1)
	ctxDone := ctx.Done()
	signalInterrupted := false
	sessionID := state.sessionID
	if state != nil {
		if resetCtrlCOnEntry {
			state.resetActiveRunCtrlCSequence()
		}
		state.activeRunCtrlCCancelNoReplay = false
		state.userInterrupted = false
		state.submitPendingSteersAfterInterrupt = false
		// Record whether this turn is a user shell command so input arriving
		// during it is queued rather than steered.
		state.activeRunIsUserShell = strings.HasPrefix(strings.TrimSpace(line), "!")
	}
	if state != nil {
		renderComposerWithState(renderer, state)
	}
	stopStatus := renderer.StartTimedTransientStatus(transientSourceWorking, workingStatusFormatter(tracker.SnapshotActiveRun, tracker.SnapshotAgentRun))
	// Animate the terminal window/tab title for the duration of the turn,
	// mirroring the transient "Working" line. On exit, settle back to the
	// static session title — refreshed, because a first turn generates it.
	if state != nil && state.titleAnimator != nil {
		turnTitle := sessionTitleForTurn(ctx, session, sessionID, line)
		state.titleAnimator.Animate(turnTitle)
		defer func() {
			// Look the stored title up on a context this turn's own withdrawal
			// cannot close, and never settle back onto the provisional title
			// derived from a message that was withdrawn: the store has just
			// released the name that message gave the session, and the window
			// title is the last place it would otherwise survive.
			if t := lookupSessionTitle(context.WithoutCancel(ctx), session, sessionID); t != "" {
				turnTitle = t
			} else if foregroundTurnFrom(ctx).isWithdrawn() {
				turnTitle = ""
			}
			state.titleAnimator.Settle(turnTitle)
		}()
	}
	statusStopped := false
	stopStatusOnce := func() {
		if !statusStopped {
			statusStopped = true
			stopStatus("")
		}
	}
	defer func() {
		renderer.FinishTransientStatus(transientSourceWorking)
	}()
	defer stopStatusOnce()
	foreground := foregroundTurnFrom(ctx)
	if foreground != nil {
		foreground.stopStatus = stopStatusOnce
	}
	go func() {
		if strings.HasPrefix(strings.TrimSpace(line), "!") {
			errCh <- session.RunSurfaceShellCommand(ctx, sessionID, "tui", line)
			return
		}
		err := session.DispatchSurfaceTurn(ctx, turn.TurnSubmission{
			SessionID:     sessionID,
			Channel:       "tui",
			UserText:      line,
			DisplayText:   "",
			RawInput:      rawInput,
			Attachments:   attachments,
			GoalObjective: goalObjective,
			SkillName:     skillName,
			SkillPath:     skillPath,
			// ChatSession returns runtime failures to this loop and leaves display
			// ownership here, avoiding a duplicate asynchronous MsgKindError.
			CallerRendersReturnedError: true,
		})
		foreground.finish()
		errCh <- err
	}()
	for {
		// Drain pending user input before notifications so that keyboard
		// and mouse events are never starved by streaming-token traffic.
		select {
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			handleActiveRunInput(ctx, session, renderer, tracker, state, clipboard, ev, cmds)
			if state != nil && state.quitRequested {
				// A repeated Ctrl+C asks the TUI to quit, but DispatchSurfaceTurn
				// still owns the cancellation cleanup that persists streamed text,
				// reasoning, and completed tool messages. Stop accepting input and
				// wait for that goroutine to return before allowing the session DB
				// to close.
				events = nil
				continue
			}
			if state != nil {
				renderComposerWithState(renderer, state)
			}
			continue
		default:
		}
		select {
		case <-ctxDone:
			// Context shutdown is also a persistence barrier. The dispatch
			// cancellation branch writes its partial transcript synchronously,
			// so never abandon the dispatch goroutine here.
			ctxDone = nil
			if foreground.isWithdrawn() {
				continue
			}
			signalInterrupted = true
			if session.CancelActiveRun() {
				renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "cancelling run"})
			}
			continue
		case <-sigCh:
			signalInterrupted = true
			if session.CancelActiveRun() {
				if state != nil {
					state.markUserInterrupt()
					if len(state.pendingSteers) > 0 {
						state.markSubmitPendingSteersAfterInterrupt()
					}
				}
				renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "cancelling run"})
			}
			// As with keyboard Esc/Ctrl+C, wait for DispatchSurfaceTurn to
			// finish its durable cancellation cleanup before returning.
			sigCh = nil
			continue
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			handleActiveRunInput(ctx, session, renderer, tracker, state, clipboard, ev, cmds)
			if state != nil && state.quitRequested {
				events = nil
				continue
			}
			if state != nil {
				renderComposerWithState(renderer, state)
			}
		case m, ok := <-notifyCh:
			if !ok {
				notifyCh = nil
				continue
			}
			// Stop the animated "Working" ticker as soon as the run ends so it
			// cannot repaint over the "Worked for" status that is about to be
			// rendered by processNotify.
			if ended, ok := m.(RunEndedMsg); ok && (ended.turn == nil || ended.turn == foreground) {
				stopStatusOnce()
			}
			processNotify(m)
		case err := <-errCh:
			events = drainActiveRunEvents(ctx, events, notifyCh, processNotify, session, renderer, tracker, state, clipboard, cmds)
			if foreground.isWithdrawn() {
				state.restoreWithdrawnWork(session)
				if err != nil && !errors.Is(err, context.Canceled) {
					renderer.PrintError(err)
				}
				renderComposerWithState(renderer, state)
				return runTurnWithdrawn, nil
			}
			// Last reconcile while the run's input runtime is still observable.
			// A steer delivered at the final tool boundary may still be in the
			// mirror if its notification has not been consumed yet; leaving it
			// there makes the turn-boundary handler treat an answered message as
			// undelivered and resubmit it, so the model sees it twice.
			renderDeliveredSteerMessages(renderer, state.reconcilePendingSteers(session))
			if state != nil {
				renderComposerWithState(renderer, state)
			}
			interrupted := state != nil && state.consumeUserInterrupt()
			cancelledNoReplay := state != nil && state.consumeActiveRunCtrlCCancelNoReplay()
			quitRequested := state != nil && state.quitRequested
			if err != nil && errors.Is(err, context.Canceled) {
				if cancelledNoReplay || quitRequested {
					return runTurnCancelled, nil
				}
				return runTurnInterrupted, nil
			}
			if err != nil {
				// An explicit skill load that failed already showed its own Skill
				// failure card before the first LLM request; a generic error card
				// would repeat the same news twice.
				var skillLoadErr *run.ExplicitSkillLoadError
				if errors.As(err, &skillLoadErr) {
					return runTurnCompleted, nil
				}
				// ChatSession delegates returned runtime errors to tui via
				// TurnSubmission.CallerRendersReturnedError. Generic Session
				// implementations and preflight failures also land here, so this
				// remains the single terminal-side fallback.
				renderer.PrintError(err)
			}
			// The run's cancellation error is swallowed downstream (surfaces as a
			// nil error), so a user-initiated cancel with queued steers must be
			// reported as interrupted here to resubmit them rather than completed
			// (which discards them).
			if interrupted && err == nil {
				return runTurnInterrupted, nil
			}
			if cancelledNoReplay && err == nil {
				return runTurnCancelled, nil
			}
			if quitRequested {
				return runTurnCancelled, nil
			}
			if signalInterrupted {
				return runTurnInterrupted, nil
			}
			return runTurnCompleted, nil
		}
	}
}

func executeComposerSubmission(ctx context.Context, sigCh <-chan os.Signal, events <-chan inputEvent, notifyCh <-chan any, processNotify func(any), session Session, renderer *Renderer, tracker *Tracker, state *streamState, submission ComposerSubmission, clipboard ClipboardImageReader, cmds *commandController, preserveComposer bool, turnWorkedStatus *string) (runTurnDisposition, error) {
	// The turn may have entered or left Plan mode; the footer follows.
	defer syncPlanModeIndicator(renderer, state)
	if turnWorkedStatus != nil {
		*turnWorkedStatus = ""
	}
	actualText := llm.TextContent(submission.Parts...)
	displayText := submission.DisplayText
	attachments := append([]InputAttachment(nil), submission.Attachments...)
	var foreground *foregroundTurn
	if !strings.HasPrefix(strings.TrimSpace(actualText), "!") {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		foreground = &foregroundTurn{cancel: cancel, submission: cloneComposerSubmission(submission)}
		ctx = context.WithValue(ctx, foregroundTurnKey{}, foreground)
	}
	state.activeForeground = foreground
	defer func() { state.activeForeground = nil }()
	if !preserveComposer {
		state.resetComposerForActiveRunDraft()
	}
	renderer.RenderFrame(Frame{Kind: FrameUser, Title: "you", Content: displayText, Final: true, turn: foreground})
	// preserveComposer marks an automatically chained continuation (a
	// queued/pending submission nextAutomaticSubmission replays right after
	// the previous turn ended) rather than a fresh user action, so it must
	// not reset the Ctrl+C escalation sequence - see the comment on
	// runTurnWithSkill's resetCtrlCOnEntry parameter.
	return runTurnWithSkill(ctx, sigCh, events, notifyCh, processNotify, session, renderer, tracker, state, actualText, displayText, submission.RawInput, attachments, submission.GoalObjective, submission.SkillName, submission.SkillPath, clipboard, cmds, turnWorkedStatus, !preserveComposer)
}

func drainActiveRunEvents(ctx context.Context, events <-chan inputEvent, notifyCh <-chan any, processNotify func(any), session Session, renderer *Renderer, tracker *Tracker, state *streamState, clipboard ClipboardImageReader, cmds *commandController) <-chan inputEvent {
	if events == nil && notifyCh == nil {
		return nil
	}
	for {
		select {
		case m, ok := <-notifyCh:
			if !ok {
				notifyCh = nil
				continue
			}
			if processNotify != nil {
				processNotify(m)
			}
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			if ev.kind == inputEventDone {
				return nil
			}
			if ev.kind == inputEventMouseWheel {
				// Coalesce consecutive wheel events so a fast scroll burst
				// triggers one viewport repaint instead of one per event.
				delta := ev.wheelDelta
				col, row := ev.mouseCol, ev.mouseRow
				flush := func() {
					handleActiveRunInput(ctx, session, renderer, tracker, state, clipboard, inputEvent{kind: inputEventMouseWheel, wheelDelta: delta, mouseCol: col, mouseRow: row}, cmds)
				}
			coalesceWheel:
				for {
					select {
					case ev2, ok2 := <-events:
						if !ok2 || ev2.kind == inputEventDone {
							flush()
							return nil
						}
						if ev2.kind == inputEventMouseWheel {
							delta += ev2.wheelDelta
							col, row = ev2.mouseCol, ev2.mouseRow
							continue
						}
						flush()
						handleActiveRunInput(ctx, session, renderer, tracker, state, clipboard, ev2, cmds)
						break coalesceWheel
					default:
						flush()
						break coalesceWheel
					}
				}
			} else {
				handleActiveRunInput(ctx, session, renderer, tracker, state, clipboard, ev, cmds)
			}
		default:
			return events
		}
	}
}

func nextAutomaticSubmission(state *streamState, disposition runTurnDisposition) (ComposerSubmission, bool) {
	if state == nil || state.quitRequested || disposition == runTurnWithdrawn {
		return ComposerSubmission{}, false
	}
	popOrdinary := func() (ComposerSubmission, bool) {
		if len(state.queuedTurns) == 0 {
			return ComposerSubmission{}, false
		}
		next := state.queuedTurns[0]
		state.queuedTurns = state.queuedTurns[1:]
		if next.Action == queuedSubmissionActionShell {
			return ComposerSubmission{
				Text:        next.Submission.Text,
				Parts:       []llm.ContentPart{llm.Text(next.Submission.Text)},
				DisplayText: next.Submission.DisplayText,
				Attachments: append([]InputAttachment(nil), next.Submission.Attachments...),
			}, true
		}
		return next.Submission, len(next.Submission.Parts) > 0
	}
	mergeAndRestoreInterruptedWork := func() {
		// Restore rejected steers first, then runtime-pending
		// steers, ordinary queued input, then the visible composer draft.
		items := make([]ComposerSubmission, 0, len(state.rejectedSteers)+len(state.pendingSteers)+len(state.queuedTurns)+1)
		for _, item := range state.rejectedSteers {
			items = append(items, item.Submission)
		}
		state.rejectedSteers = nil
		items = append(items, state.pendingSteers...)
		state.pendingSteers = nil
		for _, item := range state.queuedTurns {
			items = append(items, item.Submission)
		}
		state.queuedTurns = nil
		if current, ok := state.currentDraftSubmission(); ok {
			items = append(items, current)
		}
		if merged := mergeComposerSubmissions(items); len(merged.Parts) > 0 {
			state.restoreDraftSubmission(merged)
		}
	}

	// A pending modal selection (session switch, permissions, skill, model) has
	// not been applied yet, so draining the queue now would run a turn against
	// settings the user is still choosing.
	if state.suppressQueueAutosend {
		return ComposerSubmission{}, false
	}

	switch disposition {
	case runTurnInterrupted, runTurnCancelled:
		// When the interrupt was issued precisely to
		// flush pending steers ("esc to interrupt and send immediately"), those
		// steers are merged and submitted as one fresh turn. Everything else about
		// an interrupted boundary restores work into the composer instead.
		if state.consumeSubmitPendingSteersAfterInterrupt() && disposition == runTurnInterrupted {
			if len(state.pendingSteers) > 0 {
				merged := mergeComposerSubmissions(state.pendingSteers)
				state.pendingSteers = nil
				if len(merged.Parts) > 0 {
					return merged, true
				}
			}
		}
		mergeAndRestoreInterruptedWork()
		return ComposerSubmission{}, false
	case runTurnCompleted:
		// Rejected steers represent an earlier failed delivery and therefore take
		// priority. Merge them into one replacement turn.
		if len(state.rejectedSteers) > 0 {
			items := make([]ComposerSubmission, 0, len(state.rejectedSteers))
			for _, item := range state.rejectedSteers {
				items = append(items, item.Submission)
			}
			state.rejectedSteers = nil
			if merged := mergeComposerSubmissions(items); len(merged.Parts) > 0 {
				return merged, true
			}
		}
		// A runtime-pending steer which was never delivered must not disappear at
		// a terminal boundary. Retry it before ordinary follow-ups.
		if len(state.pendingSteers) > 0 {
			merged := mergeComposerSubmissions(state.pendingSteers)
			state.pendingSteers = nil
			if len(merged.Parts) > 0 {
				return merged, true
			}
		}
		return popOrdinary()
	}
	return ComposerSubmission{}, false
}

func postTurnCompletionNotification(out io.Writer, disposition runTurnDisposition, terminalFocused bool, submission ComposerSubmission) bool {
	if out == nil || disposition != runTurnCompleted || terminalFocused {
		return false
	}
	if strings.HasPrefix(strings.TrimSpace(llm.TextContent(submission.Parts...)), "!") {
		return false
	}
	_, err := io.WriteString(out, "\x07")
	return err == nil
}

func (s *streamState) updateTerminalFocus(ev inputEvent) bool {
	if s == nil {
		return false
	}
	switch ev.kind {
	case inputEventFocusGained:
		s.terminalFocused = true
		s.clipboardWatch.SetFocused(true)
		return true
	case inputEventFocusLost:
		s.terminalFocused = false
		s.clipboardWatch.SetFocused(false)
		return true
	default:
		return false
	}
}

func printResumeHint(out io.Writer, sessionID string) {
	if out == nil {
		return
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return
	}
	_, _ = fmt.Fprintln(out, "Resume this session with:")
	_, _ = fmt.Fprintln(out, "forebrain resume "+sid)
}

// mcpStartupWatcher is the session-side half of the MCP status path: it records
// one runner's MCP startup against the conversation that is open. Looked up on
// the concrete session for the same reason as the skipper below.
type mcpStartupWatcher interface {
	WatchMCPStartup(sessionID string)
}

// mcpStartupSkipper is the optional half of the MCP ready barrier: cancelling
// the servers the run can do without. A surface that does not offer the skip (a
// gateway session, which shares its runner with other sessions) simply does not
// implement it.
type mcpStartupSkipper interface {
	SkipOptionalMCPStartup() bool
}

// mcpStatusLine owns the MCP startup status line for one terminal.
//
// The line is installed as a timed source rather than as fixed text because its
// two halves change on different clocks: the servers change when one of them
// settles, and the elapsed time changes continuously. Installing the rendered
// text on each status message froze the clock at whatever it read when the last
// server moved.
//
// apply runs only on the main loop, so stop needs no lock; snapshot and
// progress are read by the formatter on the renderer's paint goroutine, so they
// do.
type mcpStatusLine struct {
	mu       sync.Mutex
	snapshot run.MCPSnapshot
	progress run.MCPStartupProgress
	stop     func(finalText string)
}

// apply takes one status message: it starts the line when a startup is in
// flight and ends it when the generation settles.
func (l *mcpStatusLine) apply(renderer *Renderer, msg MCPStatusMsg) {
	if l == nil || renderer == nil {
		return
	}
	l.mu.Lock()
	l.snapshot, l.progress = msg.Snapshot, msg.Progress
	l.mu.Unlock()
	if msg.Progress.InFlight > 0 {
		if l.stop == nil {
			l.stop = renderer.StartTimedTransientStatus(transientSourceMCP, l.render)
		}
		return
	}
	if l.stop != nil {
		l.stop("")
		l.stop = nil
	}
	renderer.FinishTransientStatus(transientSourceMCP)
}

// render is the formatter the renderer evaluates on every paint. The elapsed
// time it reports is the generation's, not the line's: a line installed a
// second late must not restart the clock the reader is watching.
func (l *mcpStatusLine) render(time.Duration, int, string) string {
	l.mu.Lock()
	snapshot, progress := l.snapshot, l.progress
	l.mu.Unlock()
	return mcpStartupStatusText(progress, snapshot)
}

// mcpStartupStatusText renders the MCP startup line, or "" when there is
// nothing to say.
//
// It names the servers that are still connecting rather than only counting
// them: the reader is waiting on a specific server — the one they just added,
// usually — and a bare "2/3" does not tell them which one is slow. The skip
// hint is offered only when there is something to skip: with only required
// servers left, Escape cannot do what the line would be promising, so promising
// it would be worse than saying nothing.
func mcpStartupStatusText(progress run.MCPStartupProgress, snapshot run.MCPSnapshot) string {
	if progress.InFlight <= 0 {
		return ""
	}
	parts := make([]string, 0, 4)
	parts = append(parts, fmt.Sprintf("mcp %d/%d", progress.Settled(), progress.Total))
	names := make([]string, 0, 2)
	for _, rec := range snapshot.Servers {
		if rec.ConnStatus == mcp.ConnStatusConnecting {
			names = append(names, rec.Name)
		}
		if len(names) == 2 {
			break
		}
	}
	if len(names) > 0 {
		line := strings.Join(names, ", ")
		if progress.InFlight > len(names) {
			line += fmt.Sprintf(" +%d", progress.InFlight-len(names))
		}
		parts = append(parts, "connecting "+line)
	} else {
		parts = append(parts, "connecting")
	}
	if !progress.StartedAt.IsZero() {
		parts = append(parts, formatWorkingElapsed(time.Since(progress.StartedAt)))
	}
	if progress.CanSkipOptional() {
		parts = append(parts, "esc to skip")
	}
	return strings.Join(parts, " · ")
}

// workingStatusFormatter renders the live status line for whichever view it is
// painted into.
//
// In the conversation it reports the turn the user is waiting on: its clock,
// that Escape interrupts it, and the run's counters. Inside a subagent's view
// it reports that subagent — its own clock and, when it runs one, its own
// checklist — because
// the transcript on screen is that agent's work, not the conversation's, and
// the two agree about nothing: they started at different moments and spent
// different amounts. The interrupt hint is dropped there because Escape returns
// to the conversation from a subagent view rather than cancelling anything.
//
// A finished subagent shows no line at all: the "Worked for" status that closed
// its own transcript is the last word on it, and a line that kept counting
// would contradict it.
func workingStatusFormatter(counterSnapshot func() Counters, agentSnapshot func(string) (AgentRunSnapshot, bool)) transientStatusFormatter {
	return func(elapsed time.Duration, tick int, view string) string {
		if view = strings.TrimSpace(view); view != "" {
			if agentSnapshot == nil {
				return ""
			}
			snap, ok := agentSnapshot(view)
			if !ok || snap.Ended {
				return ""
			}
			title := transientSpinnerFrame(tick) + " Working (" + formatWorkingElapsed(snap.Elapsed) + ")"
			if counters := formatWorkingCounters(snap.Counters); counters != "" {
				title += " · " + counters
			}
			return title
		}
		counters := Counters{}
		if counterSnapshot != nil {
			counters = counterSnapshot()
		}
		working := "Working"
		switch {
		case counters.GoalChecking:
			working = fmt.Sprintf("Checking whether the goal is met · after round %d", counters.GoalRound)
		case counters.GoalRound > 0:
			working = fmt.Sprintf("Working toward the goal · round %d", counters.GoalRound)
		}
		title := transientSpinnerFrame(tick) + " " + working + " (" + formatWorkingElapsed(elapsed) + " • esc to interrupt)"
		if c := formatWorkingCounters(counters); c != "" {
			title += " · " + c
		}
		return title
	}
}

func formatWorkingCounters(c Counters) string {
	// The counters report the checklist and nothing else: ☑N/M progress and,
	// when tasks are in flight, the shortest active title. The ☑ (U+2611) is
	// plain standard Unicode — every terminal renders it with its default text
	// style and no font ships with the binary. Token spend and tool/agent
	// counts are tracked but no longer painted.
	if c.PlanTotal <= 0 {
		return ""
	}
	progress := "☑" + strconv.Itoa(c.PlanDone) + "/" + strconv.Itoa(c.PlanTotal)
	if active := strings.TrimSpace(c.PlanActive); active != "" {
		progress += " · " + active
	}
	return progress
}

func formatTokensCompact(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	if n < 10000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	if n < 1000000 {
		return strconv.Itoa(n/1000) + "k"
	}
	// M tier for context windows and budgets at or above one million tokens;
	// trailing zeros are trimmed so 1,000,000 renders as "1M", 1,050,000 as "1.05M".
	m := fmt.Sprintf("%.2f", float64(n)/1e6)
	m = strings.TrimRight(m, "0")
	return strings.TrimRight(m, ".") + "M"
}

func handleActiveRunInput(ctx context.Context, session Session, renderer *Renderer, tracker *Tracker, state *streamState, clipboard ClipboardImageReader, ev inputEvent, cmds *commandController) {
	if state.updateTerminalFocus(ev) {
		return
	}
	// A slash panel opened during the run is the key target exactly as it is
	// between runs: keys drive the panel — Esc and Ctrl+C close it rather than
	// interrupt the run — while the run keeps streaming above it. Mouse events
	// stay with the renderer, which scrolls a tall panel under the wheel.
	if state.panel != nil && !isMouseInputEvent(ev) {
		if handleUIPanelKey(renderer, state, ev) {
			drawUIPanel(renderer, state)
		} else {
			closeUIPanel(renderer, state)
		}
		return
	}
	// Drop steers the run has already delivered before acting on or rendering
	// the queue, so editing/resubmitting operates on what is genuinely pending.
	// This can observe a delivery before its notification reaches the loop, so
	// it owns moving those steers into the transcript too.
	renderDeliveredSteerMessages(renderer, state.reconcilePendingSteers(session))
	// Shift+Left is decoded by the raw reader; bare Up remains history
	// navigation.
	switch ev.kind {
	case inputEventMouseClick:
		renderer.ViewportClickToggle(ev.mouseCol, ev.mouseRow)
	case inputEventMousePress:
		renderer.ViewportSelectStart(ev.mouseCol, ev.mouseRow)
	case inputEventMouseDrag:
		renderer.ViewportSelectDrag(ev.mouseCol, ev.mouseRow)
	case inputEventMouseRelease:
		renderer.ViewportSelectEnd(ev.mouseCol, ev.mouseRow)
	case inputEventMouseMove:
		renderer.ViewportHover(ev.mouseCol, ev.mouseRow)
	case inputEventMouseWheel:
		renderer.ViewportScrollAt(ev.wheelDelta, ev.mouseCol, ev.mouseRow)
	case inputEventDraft:
		state.resetActiveRunCtrlCSequence()
		draft, cursor := sanitizeTerminalDraft(ev.draft, ev.cursor)
		state.prepareHistoryDraft(ev, draft)
		state.syncPendingPastesWithDraft(draft)
		state.syncAttachmentsWithDraft(draft)
		state.composer.Cursor = composerCursorClamp(draft, cursor)
		state.composer.HandleDraftUpdate(draft, session)
		if state.holdComposer {
			state.holdComposer = false
			// Clear composer.Text (do NOT set it to the draft). The idle
			// loop's top reads composer.Text and auto-submits any non-empty
			// value as a new turn when the run ends. Setting it to the draft
			// here would auto-submit text the user typed during the run but
			// never submitted with Enter. The draft survives in DraftText
			// and the raw reader's line buffer (seeded by appendPaste /
			// appendImageAttachment), so Enter still submits the full text
			// via inputEventLine, which overwrites composer.Text with
			// ev.line. Mirrors the idle-path draft handler (line ~508).
			state.composer.Text = ""
		}
	case inputEventLine:
		state.resetActiveRunCtrlCSequence()
		state.enrichSubmittedInputHistory(ev)
		text := ev.line
		if state.agentRosterFocused {
			if state.handleAgentRosterLineInput(session, renderer, tracker, strings.TrimSpace(text)) {
				return
			}
		}
		if acc, ok := state.composer.AcceptMentionSelection(); ok {
			state.applyMentionAcceptance(acc)
			return
		}
		if strings.TrimSpace(text) == "" {
			return
		}
		commandText := strings.TrimSpace(text)
		if strings.HasPrefix(commandText, "/") {
			// Same rule as the idle loop: the submitted line has left the
			// composer, so clear and repaint it before the handler runs
			// rather than leaving the command on screen underneath whatever
			// the handler puts up.
			state.resetComposerForActiveRunDraft()
			renderComposerWithState(renderer, state)
			handleActiveRunSlash(ctx, session, renderer, tracker, state, cmds, commandText)
			return
		}
		if strings.HasPrefix(commandText, "!") {
			state.composer.Text = text
			state.composer.DraftText = text
			state.composer.SlashOverlay = nil
			renderer.RenderFrame(Frame{Kind: FrameUser, Title: "you", Content: text, Final: true})
			go func(line string) {
				if err := session.RunSurfaceShellCommand(ctx, state.sessionID, "tui", line); err != nil && !errors.Is(err, context.Canceled) {
					renderer.PrintError(err)
				}
			}(text)
			state.resetComposerForActiveRunDraft()
			return
		}
		state.composer.Text = text
		state.composer.DraftText = text
		state.composer.SlashOverlay = nil
		state.composer.MentionOverlay = nil
		submission, ok := state.submissionFromCurrentDraft()
		if !ok {
			return
		}
		// A user shell command is not steerable: steering it would attach the
		// message to a shell invocation that never consults the model. Queue it as
		// an ordinary follow-up so it runs as its own agent turn.
		if state.onlyUserShellCommandsRunning() {
			state.enqueueTurn(submission, queuedSubmissionActionTurn)
			state.resetComposerForActiveRunDraft()
			return
		}
		if state.activeForeground.isWithdrawn() {
			state.enqueueTurn(submission, queuedSubmissionActionTurn)
			state.resetComposerForActiveRunDraft()
			return
		}
		if session.SteerSurfaceRun(state.sessionID, "tui", submission.Parts) {
			state.enqueuePendingSteer(submission)
			state.resetComposerForActiveRunDraft()
			return
		}
		state.enqueueRejectedSteer(submission)
		state.resetComposerForActiveRunDraft()
	case inputEventPaste:
		state.resetActiveRunCtrlCSequence()
		state.appendPaste(ev.paste)
	case inputEventHotkey:
		if ev.hotkey == hotkeyOverlayDown || ev.hotkey == hotkeyOverlayUp {
			state.handleOverlayNav(ev.hotkey, renderer.ActiveView())
			return
		}
		if ev.hotkey == hotkeyOverlayAccept {
			if newDraft, ok := state.composer.AcceptSlashSelection(); ok {
				state.composer.DraftText = newDraft
				state.composer.Cursor = len([]rune(newDraft))
				state.composer.HandleDraftUpdate(newDraft, session)
				seedInteractiveInput(newDraft, seedCursorEnd)
				return
			}
			if acc, ok := state.composer.AcceptMentionSelection(); ok {
				state.applyMentionAcceptance(acc)
				return
			}
			return
		}
		if ev.hotkey == hotkeyEscapeInterrupt {
			// When roster has focus, Esc unfocuses it rather than
			// cancelling the active run (matches idle-loop behaviour).
			if state.agentRosterFocused {
				state.agentRosterFocused = false
				return
			}
			// Same precedence for a subagent view: Esc backs out of what the
			// user is looking at before it means "cancel the run", so opening a
			// subagent mid-turn is never a one-way door (matches handleIdleHotkey).
			if renderer != nil && renderer.ActiveView() != "" {
				renderer.SetActiveView("")
				return
			}
			state.resetActiveRunCtrlCSequence()
			// Esc while the MCP startup barrier is still open means "do not make
			// me wait for the servers I can do without": it skips the optional
			// ones and keeps this submission, so the turn proceeds now with the
			// tools that are ready. It sits above the withdraw branch below on
			// purpose — the barrier is waited on in exactly the window where a
			// submission has been committed and the model has produced nothing
			// yet, which is the window withdraw() claims — and it deliberately
			// does not withdraw or cancel the turn, because the user asked to
			// skip servers and not to take their message back.
			//
			// With nothing optional left to skip it falls through: a required
			// server cannot be skipped, and pretending otherwise would leave the
			// user believing tools were dropped when they were not.
			//
			// The method is looked up on the concrete session rather than added
			// to the Session interface: it is the MCP half of the runtime, not
			// part of what a surface must be able to do, and every
			// implementation of the interface would otherwise have to grow it.
			if skipper, ok := session.(mcpStartupSkipper); ok && skipper.SkipOptionalMCPStartup() {
				return
			}
			// Esc before the model has produced anything withdraws the
			// submission instead of cancelling around it: no "cancelling run",
			// no user card left behind, and the text back in the composer to
			// edit. A repeated press while cleanup drains is a no-op rather
			// than a second recovery or a noisy cancel.
			if foreground := state.activeForeground; foreground.withdraw() {
				if !foreground.restored {
					if foreground.stopStatus != nil {
						foreground.stopStatus()
					}
					renderer.FinishTransientStatus(transientSourceWorking)
					renderer.withdrawSubmission(foreground)
					state.restoreWithdrawnWork(session)
					renderComposerWithState(renderer, state)
					// withdraw() already cancelled this turn's own context, which
					// is what stops a run that is not registered yet; this stops
					// one that is. Neither is enough on its own.
					session.CancelActiveRun()
				}
				return
			}
			// Esc cancels the in-flight run. Discard only the committed
			// composer.Text (set by a paste/attachment via holdComposer) so
			// the idle loop does not auto-submit it as the next turn once the
			// run ends. The live draft (DraftText) and the raw reader's line
			// buffer are preserved so the user can keep editing the content
			// they were in the middle of typing. Pending steers and queued
			// turns are preserved and resubmitted via
			// nextAutomaticSubmission.
			state.composer.Text = ""
			state.holdComposer = false
			// Every esc is a user-initiated interrupt. The flag must be set
			// regardless of what is queued: the cancellation error is swallowed
			// downstream and surfaces as nil, so without it runTurn reports
			// runTurnCompleted and the queue is auto-submitted as an ordinary
			// follow-up instead of being restored into the composer.
			state.markUserInterrupt()
			if len(state.pendingSteers) > 0 {
				// "esc to interrupt and send immediately": the interrupt exists to
				// flush the pending steers, so they are submitted as one fresh turn
				// at the interrupted boundary.
				state.markSubmitPendingSteersAfterInterrupt()
				if !session.CancelActiveRun() {
					// Nothing was cancelled, so no interrupted boundary will arrive
					// to consume the flags, so roll them back immediately.
					state.submitPendingSteersAfterInterrupt = false
					state.userInterrupted = false
					return
				}
				renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "cancelling run"})
				return
			}
			if !session.CancelActiveRun() {
				state.userInterrupted = false
				return
			}
			renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "cancelling run"})
			return
		}
		if ev.hotkey == hotkeyInterrupt {
			if state.clearComposer() {
				state.armActiveRunCtrlC()
				return
			}
			if state.isActiveRunCtrlCArmed() {
				state.armActiveRunCtrlCExit()
				state.markActiveRunCtrlCCancelNoReplay()
				if session.CancelActiveRun() {
					renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "cancelling run"})
				}
				return
			}
			if state.isActiveRunCtrlCExitArmed() {
				state.resetActiveRunCtrlCSequence()
				state.quitRequested = true
				return
			}
			state.armActiveRunCtrlCExit()
			state.markActiveRunCtrlCCancelNoReplay()
			if session.CancelActiveRun() {
				renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "cancelling run"})
			}
			return
		}
		if ev.hotkey == hotkeyClearInput {
			if state.clearComposer() {
				state.armActiveRunCtrlC()
			}
			return
		}
		if ev.hotkey == hotkeyQueueFollowUp {
			state.resetActiveRunCtrlCSequence()
			state.commitDraftForSubmission()
			submission, ok := state.submissionFromCurrentDraft()
			if !ok {
				return
			}
			action := queuedSubmissionActionTurn
			if strings.HasPrefix(strings.TrimSpace(submission.Text), "!") && len(submission.Attachments) == 0 {
				action = queuedSubmissionActionShell
				submission.Parts = []llm.ContentPart{llm.Text(submission.Text)}
			}
			state.enqueueTurn(submission, action)
			state.resetComposerForActiveRunDraft()
			return
		}
		if ev.hotkey == hotkeyEditLastQueued {
			state.resetActiveRunCtrlCSequence()
			state.restoreLatestQueuedEditableSubmission(session)
			return
		}
		if ev.hotkey == hotkeyBackground {
			state.resetActiveRunCtrlCSequence()
			// Ctrl+B belongs to a concrete foreground task. A global assistant
			// turn must never be detached as a substitute for that task path.
			return
		}
		if ev.hotkey == hotkeyRosterStop {
			// Bare 'x' intercepted by the raw reader while the roster is
			// focused on a subagent row. Delegate to the same cancel path
			// used by line-submission "x" + Enter.
			state.resetActiveRunCtrlCSequence()
			state.handleAgentRosterLineInput(session, renderer, tracker, "x")
			return
		}
		_ = handleIdleHotkey(ctx, session, renderer, state, clipboard, nil, ev.hotkey)
	}
}

func handleActiveRunSlash(ctx context.Context, session Session, renderer *Renderer, tracker *Tracker, state *streamState, cmds *commandController, line string) {
	if state == nil {
		return
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if cmds != nil {
		handled, continueRun, submission, _ := dispatchStreamSlashCommand(ctx, cmds, renderer, tracker, state, line)
		if handled {
			if continueRun {
				// A steer joins an already-built agent session and cannot safely add
				// trusted run metadata. Queue explicit skill invocations as the next
				// turn so their activation is injected before that turn's first LLM
				// call instead of degrading back to prompt text.
				if strings.TrimSpace(submission.SkillPath) != "" || state.onlyUserShellCommandsRunning() {
					state.enqueueTurn(submission, queuedSubmissionActionTurn)
					state.resetComposerForActiveRunDraft()
					return
				}
				if session.SteerSurfaceRun(state.sessionID, "tui", submission.Parts) {
					state.enqueuePendingSteer(submission)
					state.resetComposerForActiveRunDraft()
					return
				}
				state.enqueueRejectedSteer(submission)
				state.resetComposerForActiveRunDraft()
				return
			}
			state.resetComposerForActiveRunDraft()
			return
		}
	}
	state.composer.Text = line
	state.composer.DraftText = line
	state.composer.Cursor = len([]rune(line))
	submission, ok := state.submissionFromCurrentDraft()
	if !ok {
		return
	}
	state.enqueueRejectedSteer(submission)
	state.resetComposerForActiveRunDraft()
	_ = ctx
}

// runSlashPicker shows the picker a slash command offered and applies what
// is picked through the engine — the same choices, from the same data, the
// web shows as a card — following any picker the choice offers in turn.
func runSlashPicker(ctx context.Context, cmds *commandController, renderer *Renderer, tracker *Tracker, state *streamState, picker *turn.Picker) (handled bool, continueRun bool, submission ComposerSubmission, exitRequested bool) {
	// The picker opens on the item in force.
	items := make([]SelectItem, len(picker.Items))
	current := -1
	for i, item := range picker.Items {
		items[i] = SelectItem{Label: item.Label, Description: item.Description}
		if item.Current {
			current = i
		}
	}
	label := picker.Title
	if hint := strings.TrimSpace(picker.Hint); hint != "" {
		label += "\n" + hint
	}
	idx, ok, err := cmds.selector.SelectRich(label, items, current)
	if err != nil {
		renderer.PrintError(err)
		return true, false, ComposerSubmission{}, false
	}
	if !ok || idx < 0 || idx >= len(picker.Items) {
		return true, false, ComposerSubmission{}, false
	}
	outcome := cmds.session.ChooseSurfaceSlash(ctx, state.sessionID, turn.SlashChoice{Command: picker.Command, Value: picker.Items[idx].Value})
	// A model, an effort or an agent may have changed under the footer.
	refreshSessionFooter(renderer, cmds.session)
	return applySlashOutcome(ctx, cmds, renderer, tracker, state, strings.TrimSuffix(picker.Command, "-effort"), "", outcome)
}

// refreshSessionFooter redraws what the footer says about the session: its
// model and effort, the subagents' models, and the context budget.
func refreshSessionFooter(renderer *Renderer, session Session) {
	if renderer == nil || session == nil {
		return
	}
	renderer.SetComposerFooter(ComposerFooter{
		Model:           summarizeComposerModel(session),
		ReasoningEffort: summarizeComposerReasoningEffort(session),
		Directory:       renderer.footer.Directory,
	})
	renderer.SetSubagentModels(subagentModelsByType(session))
	renderer.SetComposerTokenStats(initialComposerTokenStats(session))
}

func applySlashOutcome(ctx context.Context, cmds *commandController, renderer *Renderer, tracker *Tracker, state *streamState, title string, rawLine string, outcome SlashOutcome) (handled bool, continueRun bool, submission ComposerSubmission, exitRequested bool) {
	if state == nil {
		return false, false, ComposerSubmission{}, false
	}
	reply := Frame{Kind: FrameSystem, Title: strings.TrimSpace(title), Content: strings.TrimSpace(outcome.Reply), Final: true}
	if outcome.SessionSwitched && strings.TrimSpace(outcome.SessionID) != "" {
		// The reply is only true once the switch itself succeeded: the
		// activation runs first, and a failure keeps the outgoing session —
		// reporting the created target as recoverable rather than entered.
		switched, _, switchErr := switchStreamSession(ctx, state, renderer, tracker, outcome.SessionID)
		if !switched {
			if switchErr == nil {
				// The switch was a no-op (same session): still say the reply.
				if reply.Content != "" && renderer != nil {
					renderer.RenderFrame(reply)
				}
				return true, false, ComposerSubmission{}, outcome.ExitRequested
			}
			if reply.Content != "" && renderer != nil {
				renderer.RenderFrame(Frame{
					Kind:    FrameSystem,
					Title:   reply.Title,
					Content: strings.TrimSpace(outcome.SessionTitle) + " was created but not entered; it stays in /resume and can be opened once the configuration is fixed.",
					Final:   true,
				})
			}
			return true, false, ComposerSubmission{}, outcome.ExitRequested
		}
		// The switch cleared the screen, so the reply is said in the
		// conversation it moved to: under the history a fork carries over,
		// or on its own in a new one.
		replayed := false
		if cmds != nil {
			cmds.replaySession(state.sessionID, func(int) *Frame {
				replayed = true
				if reply.Content == "" {
					return nil
				}
				return &reply
			})
		}
		if !replayed && reply.Content != "" && renderer != nil {
			renderer.RenderFrame(reply)
		}
	} else if reply.Content != "" && renderer != nil {
		renderer.RenderFrame(reply)
	}
	if outcome.SelectSession && cmds != nil {
		if selected, ok := cmds.handleResume(ctx, state.sessionID); ok && strings.TrimSpace(selected) != "" {
			switchStreamSession(ctx, state, renderer, tracker, selected)
			cmds.printSessionResumeContext(state.sessionID)
		}
	}
	if outcome.ManagePermissions && cmds != nil {
		_ = cmds.handlePermissions(state.sessionID)
	}
	if outcome.SelectSkill && cmds != nil {
		if sub, ok := cmds.handleSkills(ctx); ok {
			state.resetComposerForActiveRunDraft()
			return true, true, sub, outcome.ExitRequested
		}
	}
	if outcome.Picker != nil && cmds != nil {
		return runSlashPicker(ctx, cmds, renderer, tracker, state, outcome.Picker)
	}
	if outcome.ShouldContinueRun {
		next := strings.TrimSpace(outcome.ContinueInput)
		if next == "" {
			return true, false, ComposerSubmission{}, outcome.ExitRequested
		}
		displayText := strings.TrimSpace(rawLine)
		if displayText == "" {
			displayText = next
		}
		sub, ok := buildComposerSubmission(next, displayText, nil)
		if !ok {
			return true, false, ComposerSubmission{}, outcome.ExitRequested
		}
		// Carry the raw command (e.g. "/init") so it is persisted as the
		// display-only turn content and resume replay shows what the user
		// typed, while Parts keeps the expanded prompt for the model.
		if raw := strings.TrimSpace(rawLine); raw != "" {
			sub.RawInput = raw
		}
		sub.SkillName = strings.TrimSpace(outcome.SkillName)
		sub.SkillPath = strings.TrimSpace(outcome.SkillPath)
		sub.GoalObjective = strings.TrimSpace(outcome.GoalObjective)
		state.resetComposerForActiveRunDraft()
		return true, true, sub, outcome.ExitRequested
	}
	return true, false, ComposerSubmission{}, outcome.ExitRequested
}

func handleIdleHotkey(ctx context.Context, session Session, renderer *Renderer, state *streamState, clipboard ClipboardImageReader, selector Selector, hotkey inputHotkey) bool {
	switch hotkey {
	case hotkeyJumpToBottom:
		renderer.ViewportJumpToBottom()
		return true
	case hotkeyRedraw:
		// Also reached mid-run: the active-run hotkey chain falls through to
		// here for every hotkey it does not consume itself.
		renderer.ForceRepaint()
		return true
	case hotkeyEscapeInterrupt:
		// If viewing a subagent alt-screen, Escape returns to the primary view.
		if renderer != nil && renderer.ActiveView() != "" {
			renderer.SetActiveView("")
			return true
		}
		// The same skip the active-run path offers, for the window before the
		// first submission: the startup line is on screen from the moment the
		// servers begin connecting, and it says "esc to skip" there too. Offering
		// it only after a submission would make the hint a lie for exactly as long
		// as the user has not typed anything yet.
		if skipper, ok := session.(mcpStartupSkipper); ok && skipper.SkipOptionalMCPStartup() {
			return true
		}
		return true
	case hotkeyBackground:
		// No backgroundable foreground task is focused at idle.
		state.agentControlPrefixArmed = false
		return true
	case hotkeyClearInput:
		state.agentControlPrefixArmed = false
		state.clearComposer()
		return true
	case hotkeyInterrupt:
		state.agentControlPrefixArmed = false
		if session.CancelActiveRun() {
			renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "cancelling run"})
			return true
		}
		if state.clearComposer() {
			return true
		}
		state.quitRequested = true
		return true
	case hotkeyAgentControlPrefix:
		state.agentControlPrefixArmed = true
		renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "agents", Content: "ctrl+k cancels all agents"})
		return true
	case hotkeyAgentStopAll:
		if !state.agentControlPrefixArmed {
			return false
		}
		state.agentControlPrefixArmed = false
		summary := session.CancelAllAgents()
		renderer.RenderFrame(Frame{
			Kind:    FrameStatus,
			Title:   "agents cancelled",
			Content: fmt.Sprintf("primary %d · subagents %d", summary.Main, summary.Subagents),
		})
		return true
	case hotkeyTogglePlanMode:
		state.agentControlPrefixArmed = false
		st, _ := statepkg.Get(state.stateRoot(), state.sessionID)
		target := statepkg.ModePlan
		if st.Mode == statepkg.ModePlan {
			target = st.PrePlanMode
			if strings.TrimSpace(string(target)) == "" {
				target = statepkg.ModeAgent
			}
		}
		// Switch keeps the pre-plan mode and the exited-plan flag intact, so
		// toggling by hotkey behaves like enter_plan_mode / exit_plan_mode.
		// The footer is the answer: it names Plan mode while it lasts.
		if _, err := statepkg.Switch(state.stateRoot(), state.sessionID, target); err != nil {
			renderer.PrintError(err)
			return true
		}
		syncPlanModeIndicator(renderer, state)
		return true
	case hotkeyPasteImage:
		state.agentControlPrefixArmed = false
		if clipboard == nil {
			clipboard = DefaultClipboardImageReader()
		}
		att, err := clipboard.ReadClipboardImage(ctx)
		if errors.Is(err, errClipboardNoImage) || (err == nil && strings.TrimSpace(att.Path) == "") {
			// The clipboard holds text rather than an image: ctrl+v is simply
			// not its paste key, so the keystroke does nothing at all.
			return true
		}
		if err != nil {
			renderer.PrintError(err)
			return true
		}
		if strings.TrimSpace(att.Label) == "" {
			att.Label = filepath.Base(att.Path)
		}
		placeholder := state.appendImageAttachment(att)
		if state == nil {
			renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "image attached", Content: placeholder})
		}
		return true
	default:
		return false
	}
}

func transientSpinnerFrame(tick int) string {
	frames := []string{"|", "/", "-", `\`}
	if tick < 0 {
		tick = 0
	}
	return frames[tick%len(frames)]
}

func formatWorkingElapsed(elapsed time.Duration) string {
	totalSeconds := int(elapsed / time.Second)
	if totalSeconds < 60 {
		return fmt.Sprintf("%ds", totalSeconds)
	}
	seconds := totalSeconds % 60
	totalMinutes := totalSeconds / 60
	if totalMinutes < 60 {
		return fmt.Sprintf("%dm %02ds", totalMinutes, seconds)
	}
	minutes := totalMinutes % 60
	hours := totalMinutes / 60
	return fmt.Sprintf("%dh %02dm %02ds", hours, minutes, seconds)
}

func streamTimeAgo(ts int64) string {
	if ts <= 0 {
		return ""
	}
	d := time.Since(time.Unix(ts, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/24/30))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/24/365))
	}
}

// resumeSessionPageSize is the /resume picker's store page size. The picker
// loads one page up front and fetches the next one only when the cursor
// reaches the last loaded row (↓ at the bottom), so browsing the full history
// never materializes it in memory.
const resumeSessionPageSize = 20

// promptRecentSession is /resume's picker over the other sessions, newest
// first; current, the one this chat is in, is not listed, since resuming it
// would change nothing. listed is false when there was nothing else to pick.
func promptRecentSession(ctx context.Context, session Session, selector Selector, readLine func(context.Context) (string, error), current string) (sid string, listed bool) {
	if session == nil || readLine == nil {
		return "", false
	}
	// Surfaces with a real session store page through it; anything else keeps
	// the single-shot recent list.
	pagedLister, canPage := session.(interface {
		ListSessionsRecentPaged(ctx context.Context, limit, offset int) ([]SessionSummary, error)
	})
	var list []SessionSummary
	var err error
	if canPage {
		list, err = pagedLister.ListSessionsRecentPaged(ctx, resumeSessionPageSize, 0)
	} else {
		list, err = session.ListSessionsRecent(ctx, resumeSessionPageSize)
	}
	if err != nil {
		return "", false
	}
	current = strings.TrimSpace(current)
	var (
		options    []SelectItem
		sessionIDs []string
	)
	appendPage := func(page []SessionSummary) {
		for _, item := range page {
			if strings.TrimSpace(item.ID) == current {
				continue
			}
			// A session still titled by its id has not been named by a first
			// message yet.
			title := strings.TrimSpace(item.Title)
			if title == "" || title == strings.TrimSpace(item.ID) {
				title = "New conversation"
			}
			sessionIDs = append(sessionIDs, strings.TrimSpace(item.ID))
			// Fixed-width time column ("just now" is the widest label) keeps
			// rows aligned across pages appended at different times.
			options = append(options, SelectItem{
				Label: fmt.Sprintf("%-8s %s", streamTimeAgo(item.UpdatedAt), title),
			})
		}
	}
	appendPage(list)
	if len(options) == 0 {
		return "", false
	}
	var idx int
	var ok bool
	if pagedSelector, canScroll := selector.(PagedRichSelector); canScroll && canPage {
		offset := len(list)
		exhausted := len(list) < resumeSessionPageSize
		fetchMore := func() []SelectItem {
			if exhausted {
				return nil
			}
			page, pageErr := pagedLister.ListSessionsRecentPaged(ctx, resumeSessionPageSize, offset)
			if pageErr != nil || len(page) == 0 {
				exhausted = true
				return nil
			}
			first := len(options)
			appendPage(page)
			offset += len(page)
			if len(page) < resumeSessionPageSize {
				exhausted = true
			}
			return options[first:]
		}
		idx, ok, err = pagedSelector.SelectRichPaged("Resume\nPick a conversation to continue", options, -1, fetchMore)
	} else {
		idx, ok, err = selector.SelectRich("Resume\nPick a conversation to continue", options, -1)
	}
	if err != nil || !ok || idx < 0 || idx >= len(sessionIDs) {
		return "", true
	}
	return sessionIDs[idx], true
}

func (s *ChatSession) hydrateRunUsage(runUsage *runUsageCarrier) {
	if s == nil || s.runSvc() == nil || runUsage == nil {
		return
	}
	if runUsage.in > 0 || runUsage.out > 0 {
		return
	}
	runID := strings.TrimSpace(runUsage.runID)
	if runID == "" {
		return
	}
	rn, err := s.runSvc().GetRun(context.Background(), runID)
	if err != nil || rn == nil {
		return
	}
	if rn.UsagePromptTokens > 0 {
		runUsage.in = rn.UsagePromptTokens
	}
	if rn.UsageCompletionTokens > 0 {
		runUsage.out = rn.UsageCompletionTokens
	}
}

// --- interactive slash panels (/status, /mcp) ---

// uiPanelPageRows is how many rows one PgUp/PgDn moves.
const uiPanelPageRows = 10

// uiPanel is one interactive panel owned by the main loop. It is composer
// state, not a modal: keys arrive through the loop's own event stream, UI
// notifications keep flowing (so the transcript above keeps streaming), and
// the panel yields the moment an approval or question needs the composer area.
type uiPanel struct {
	kind string // "status" | "mcp" | "lsp"
	tab  int    // status: 0 = Status, 1 = Usage
	// mcp navigation: page is "list", "detail", "tools", "tool" or
	// "resources"; lsp: "list" or "detail"; cursor indexes the page's
	// selectable rows. server is the open server's id on both panels.
	page   string
	cursor int
	server string
	tool   string
	// top asks the next draw to show the page from its first row: a page just
	// opened or a tab just switched.
	top bool
	// Per-server results the detail page shows in place: the last
	// Disable/Enable reply, an OAuth flow's URL and outcome, a resource list.
	notice    map[string]string
	auth      map[string]*panelAuthFlow
	resources map[string]*panelResources

	status    *turn.StatusReport
	inv       *turn.MCPInventory
	cancelMCP func() // stops the MCP status subscription
	// lsp is the open /lsp panel's snapshot; lspSession runs its actions,
	// cancelLSP stops the snapshot subscription.
	lsp        *event.LSPSnapshot
	lspSession lspPanelSession
	cancelLSP  func()
	session    panelSession
	sessionID  string
}

// panelAuthFlow is one OAuth flow started from the panel.
type panelAuthFlow struct {
	url    string
	result string // the outcome sentence; empty while the flow runs
}

// panelResources is one server's resource list, fetched off the main loop.
type panelResources struct {
	loading bool
	items   []mcp.ResourceInfo
	err     string
}

// panelSession is what a session must expose for the panels. Nothing here
// blocks the main loop: the OAuth flow and the resource list run off it and
// report back as UI notifications.
type panelSession interface {
	PanelStatusReport(sessionID string) (turn.StatusReport, error)
	PanelMCPInventory() (*turn.MCPInventory, error)
	PanelAuthenticateMCP(serverName string)
	PanelSetMCPDisabled(serverName string, disable bool) (string, error)
	PanelListMCPResources(serverName string)
	SubscribeMCPStatusTick() (cancel func(), ok bool)
}

// lspPanelSession is what a session must expose for the /lsp panel. It is a
// separate interface so the status and mcp test doubles do not have to grow
// LSP methods; every call returns at once — an install runs in the background
// and reports through the snapshot.
type lspPanelSession interface {
	PanelLSPSnapshot() (event.LSPSnapshot, error)
	PanelSetLSPEnabled(serverID string, enabled bool) (string, error)
	PanelRestartLSP(serverID string) (string, error)
	PanelInstallLSP(serverID string) (string, error)
	PanelResetLSPRecommendations() (string, error)
	SubscribeLSPStatus() (cancel func(), ok bool)
}

// openStatusPanel builds the /status panel. Called from the slash dispatch on
// the main loop. A panel never opens while a modal owns the input.
func openStatusPanel(state *streamState) bool {
	ps, ok := state.session.(panelSession)
	if !ok || interactiveInputPauseDepth.Load() > 0 {
		return false
	}
	rep, err := ps.PanelStatusReport(state.sessionID)
	if err != nil {
		return false
	}
	setUIPanelActive(true)
	state.panel = &uiPanel{kind: "status", status: &rep, session: ps, sessionID: state.sessionID, top: true}
	return true
}

// openMCPPanel builds the /mcp panel and subscribes to MCP status changes so
// the list repaints in place while a startup settles.
func openMCPPanel(state *streamState) bool {
	ps, ok := state.session.(panelSession)
	if !ok || interactiveInputPauseDepth.Load() > 0 {
		return false
	}
	inv, err := ps.PanelMCPInventory()
	if err != nil {
		return false
	}
	setUIPanelActive(true)
	panel := &uiPanel{
		kind: "mcp", page: "list", inv: inv, session: ps, sessionID: state.sessionID, top: true,
		notice: map[string]string{}, auth: map[string]*panelAuthFlow{}, resources: map[string]*panelResources{},
	}
	if cancel, subscribed := ps.SubscribeMCPStatusTick(); subscribed {
		panel.cancelMCP = cancel
	}
	state.panel = panel
	return true
}

// openLSPPanel builds the /lsp panel and subscribes to snapshot changes so a
// starting, indexing or installing server repaints in place.
func openLSPPanel(state *streamState) bool {
	ls, ok := state.session.(lspPanelSession)
	if !ok || interactiveInputPauseDepth.Load() > 0 {
		return false
	}
	snap, err := ls.PanelLSPSnapshot()
	if err != nil {
		return false
	}
	setUIPanelActive(true)
	panel := &uiPanel{
		kind: "lsp", page: "list", lsp: &snap, lspSession: ls, sessionID: state.sessionID, top: true,
		notice: map[string]string{},
	}
	if cancel, subscribed := ls.SubscribeLSPStatus(); subscribed {
		panel.cancelLSP = cancel
	}
	state.panel = panel
	return true
}

// closeUIPanel tears the panel down and hands the composer block back:
// EndOverlay is what erases the panel's taller block before the composer
// repaints underneath it.
func closeUIPanel(renderer *Renderer, state *streamState) {
	setUIPanelActive(false)
	if state.panel != nil {
		if state.panel.cancelMCP != nil {
			state.panel.cancelMCP()
		}
		if state.panel.cancelLSP != nil {
			state.panel.cancelLSP()
		}
	}
	state.panel = nil
	if renderer != nil {
		renderer.EndOverlay()
	}
}

// refreshInventory rebuilds the mcp panel's data from the session. Building it
// never waits on the MCP startup, so the main loop calls it before every draw.
func (panel *uiPanel) refreshInventory() {
	if panel == nil || panel.kind != "mcp" || panel.session == nil {
		return
	}
	if inv, err := panel.session.PanelMCPInventory(); err == nil {
		panel.inv = inv
	}
}

// refreshLSP re-reads the /lsp panel's snapshot. It never blocks: an install
// or a probe still in flight reports through the next LSPStatusTickMsg.
func (panel *uiPanel) refreshLSP() {
	if panel == nil || panel.kind != "lsp" || panel.lspSession == nil {
		return
	}
	if snap, err := panel.lspSession.PanelLSPSnapshot(); err == nil {
		panel.lsp = &snap
	}
}

// handleUIPanelKey routes one event into the panel. Returns true while the
// panel stays open; false closes it.
func handleUIPanelKey(renderer *Renderer, state *streamState, ev inputEvent) bool {
	panel := state.panel
	if panel == nil {
		return false
	}
	if ev.kind != inputEventHotkey {
		// The raw reader keeps text out of its buffer while a panel is open,
		// so nothing else reaches here that the panel could act on.
		return true
	}
	switch ev.hotkey {
	case hotkeyEscapeInterrupt:
		// Esc returns to the previous level and closes only at the top.
		return panel.back()
	case hotkeyInterrupt:
		// Ctrl+C closes outright from any depth.
		return false
	case hotkeyPanelLeft:
		if panel.kind == "status" {
			panel.switchTab()
			return true
		}
		panel.back()
		return true
	case hotkeyPanelRight, hotkeyPanelTab:
		if panel.kind == "status" {
			panel.switchTab()
		}
		return true
	case hotkeyOverlayUp, hotkeyOverlayDown, hotkeyPanelPageUp, hotkeyPanelPageDown:
		step := 1
		if ev.hotkey == hotkeyPanelPageUp || ev.hotkey == hotkeyPanelPageDown {
			step = uiPanelPageRows
		}
		if ev.hotkey == hotkeyOverlayUp || ev.hotkey == hotkeyPanelPageUp {
			step = -step
		}
		if rows := panelSelectableRows(panel); len(rows) > 0 {
			panel.cursor = clampInt(panel.cursor+step, 0, len(rows)-1)
			return true
		}
		// A page without a selection scrolls its content instead.
		if renderer != nil {
			renderer.ViewportScrollOverlay(step)
		}
		return true
	case hotkeyPanelAccept:
		panelAccept(panel)
		return true
	}
	return true
}

// back returns to the previous mcp or lsp page; at the top it reports false,
// which closes the panel.
func (panel *uiPanel) back() bool {
	if panel.kind == "lsp" {
		if panel.page != "detail" {
			return false
		}
		panel.page = "list"
		panel.cursor = 0
		// The cursor lands back on the row that was opened.
		for i, row := range panelSelectableRows(panel) {
			if row.action == panelOpenDetail && row.value == panel.server {
				panel.cursor = i
				break
			}
		}
		panel.top = true
		return true
	}
	if panel.kind != "mcp" {
		return false
	}
	previous := map[string]string{"detail": "list", "tools": "detail", "resources": "detail", "tool": "tools"}
	to, ok := previous[panel.page]
	if !ok {
		return false
	}
	// The cursor lands back on the row that was opened.
	from := panel.page
	panel.page = to
	panel.cursor = 0
	for i, row := range panelSelectableRows(panel) {
		if (from == "detail" && row.value == panel.server && row.action == panelOpenDetail) ||
			(from == "tool" && row.value == panel.tool) ||
			(from == "tools" && row.action == panelOpenTools) ||
			(from == "resources" && row.action == panelOpenResources) {
			panel.cursor = i
			break
		}
	}
	panel.top = true
	return true
}

func (panel *uiPanel) switchTab() {
	panel.tab = (panel.tab + 1) % 2
	panel.top = true
}

// isMouseInputEvent reports whether ev is a pointer event, which stays with
// the renderer even while a panel is the key target.
func isMouseInputEvent(ev inputEvent) bool {
	switch ev.kind {
	case inputEventMouseClick, inputEventMouseWheel, inputEventMouseMove,
		inputEventMousePress, inputEventMouseDrag, inputEventMouseRelease:
		return true
	}
	return false
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// panelAccept runs the highlighted row's action on the mcp or lsp panel.
func panelAccept(panel *uiPanel) {
	if panel.kind == "lsp" {
		panelAcceptLSP(panel)
		return
	}
	if panel.kind != "mcp" {
		return
	}
	rows := panelSelectableRows(panel)
	if panel.cursor < 0 || panel.cursor >= len(rows) {
		return
	}
	row := rows[panel.cursor]
	open := func(page string) {
		panel.page = page
		panel.cursor = 0
		panel.top = true
	}
	switch row.action {
	case panelOpenDetail:
		panel.server = row.value
		open("detail")
	case panelOpenTools:
		open("tools")
	case panelOpenTool:
		panel.tool = row.value
		open("tool")
	case panelOpenResources:
		panel.resources[panel.server] = &panelResources{loading: true}
		panel.session.PanelListMCPResources(panel.server)
		open("resources")
	case panelDisable, panelEnable:
		reply, err := panel.session.PanelSetMCPDisabled(row.value, row.action == panelDisable)
		if err != nil {
			reply = err.Error()
		}
		panel.notice[row.value] = reply
		panel.refreshInventory()
	case panelAuth:
		panel.auth[row.value] = &panelAuthFlow{}
		panel.session.PanelAuthenticateMCP(row.value)
	}
}

// applyAuth records an OAuth flow's progress for the detail page.
func (panel *uiPanel) applyAuth(msg MCPAuthMsg) {
	if panel == nil || panel.kind != "mcp" {
		return
	}
	st := panel.auth[msg.Server]
	if st == nil {
		st = &panelAuthFlow{}
		panel.auth[msg.Server] = st
	}
	if msg.URL != "" {
		st.url = msg.URL
	}
	if msg.Result != "" {
		st.result = msg.Result
	}
}

// applyResources records a fetched resource list for the resources page.
func (panel *uiPanel) applyResources(msg MCPResourcesMsg) {
	if panel == nil || panel.kind != "mcp" {
		return
	}
	panel.resources[msg.Server] = &panelResources{items: msg.Items, err: msg.Err}
}

type panelRowAction string

const (
	panelOpenDetail    panelRowAction = "detail"
	panelOpenTools     panelRowAction = "tools"
	panelOpenTool      panelRowAction = "tool"
	panelOpenResources panelRowAction = "resources"
	panelAuth          panelRowAction = "auth"
	panelDisable       panelRowAction = "disable"
	panelEnable        panelRowAction = "enable"

	panelLSPEnable    panelRowAction = "lsp-enable"
	panelLSPDisable   panelRowAction = "lsp-disable"
	panelLSPRestart   panelRowAction = "lsp-restart"
	panelLSPInstall   panelRowAction = "lsp-install"
	panelLSPResetRecs panelRowAction = "lsp-reset-recommendations"
)

// panelAcceptLSP runs the highlighted /lsp row: navigation moves pages, every
// action goes through the session and lands its reply (or its error text) in
// the notice the page shows in place.
func panelAcceptLSP(panel *uiPanel) {
	rows := panelSelectableRows(panel)
	if panel.cursor < 0 || panel.cursor >= len(rows) {
		return
	}
	row := rows[panel.cursor]
	run := func(key string, reply string, err error) {
		if err != nil {
			reply = err.Error()
		}
		panel.notice[key] = reply
		panel.refreshLSP()
	}
	switch row.action {
	case panelOpenDetail:
		panel.server = row.value
		panel.page = "detail"
		panel.cursor = 0
		panel.top = true
	case panelLSPEnable, panelLSPDisable:
		reply, err := panel.lspSession.PanelSetLSPEnabled(row.value, row.action == panelLSPEnable)
		run(row.value, reply, err)
	case panelLSPRestart:
		reply, err := panel.lspSession.PanelRestartLSP(row.value)
		run(row.value, reply, err)
	case panelLSPInstall:
		reply, err := panel.lspSession.PanelInstallLSP(row.value)
		run(row.value, reply, err)
	case panelLSPResetRecs:
		reply, err := panel.lspSession.PanelResetLSPRecommendations()
		run("", reply, err)
	}
}

// panelRow is one selectable row of an mcp page.
type panelRow struct {
	label  string
	action panelRowAction
	value  string
}

// panelSelectableRows returns the page's cursor-addressable rows, and only
// those: text a row cannot act on (a group title, "Required by config") is
// drawn around them, never selected.
func panelSelectableRows(panel *uiPanel) []panelRow {
	if panel == nil {
		return nil
	}
	if panel.kind == "lsp" {
		return lspPanelRows(panel)
	}
	if panel.inv == nil {
		return nil
	}
	switch panel.page {
	case "list":
		var rows []panelRow
		for _, group := range panel.inv.Groups {
			for _, e := range group.Entries {
				rows = append(rows, panelRow{label: e.Name, action: panelOpenDetail, value: e.Name})
			}
		}
		return rows
	case "detail":
		entry := panel.inv.Entry(panel.server)
		if entry == nil {
			return nil
		}
		var rows []panelRow
		if entry.ToolCount > 0 {
			rows = append(rows, panelRow{label: "View tools", action: panelOpenTools, value: entry.Name})
		}
		if entry.Resources {
			rows = append(rows, panelRow{label: "View resources", action: panelOpenResources, value: entry.Name})
		}
		if entry.CanAuth {
			rows = append(rows, panelRow{label: "Authenticate", action: panelAuth, value: entry.Name})
		}
		switch {
		case entry.Required:
		case entry.DisabledNext:
			rows = append(rows, panelRow{label: "Enable (takes effect in a new session)", action: panelEnable, value: entry.Name})
		default:
			rows = append(rows, panelRow{label: "Disable (takes effect in a new session)", action: panelDisable, value: entry.Name})
		}
		return rows
	case "tools":
		entry := panel.inv.Entry(panel.server)
		if entry == nil {
			return nil
		}
		rows := make([]panelRow, 0, len(entry.Tools))
		for _, t := range entry.Tools {
			rows = append(rows, panelRow{label: t.Name, action: panelOpenTool, value: t.Name})
		}
		return rows
	}
	return nil
}

// lspPanelRows returns the /lsp panel's rows: the list offers one row per
// server (snapshot order, enabled first) plus the recommendations switch-back
// when they are off; the detail page offers only the actions its state makes
// sense of.
func lspPanelRows(panel *uiPanel) []panelRow {
	if panel.lsp == nil {
		return nil
	}
	switch panel.page {
	case "list":
		rows := make([]panelRow, 0, len(panel.lsp.Servers)+1)
		for _, s := range panel.lsp.Servers {
			rows = append(rows, panelRow{label: s.ID, action: panelOpenDetail, value: s.ID})
		}
		if panel.lsp.RecommendationsDisabled {
			rows = append(rows, panelRow{label: "Turn recommendations back on", action: panelLSPResetRecs})
		}
		return rows
	case "detail":
		s := lspPanelServer(panel)
		if s == nil {
			return nil
		}
		var rows []panelRow
		if !s.Enabled && s.State != event.LSPStateBlocked {
			rows = append(rows, panelRow{label: "Enable", action: panelLSPEnable, value: s.ID})
		}
		if s.Enabled {
			rows = append(rows, panelRow{label: "Disable", action: panelLSPDisable, value: s.ID})
		}
		if s.Enabled {
			switch s.State {
			case event.LSPStateReady, event.LSPStateIndexing, event.LSPStateStarting, event.LSPStateFailed:
				rows = append(rows, panelRow{label: "Restart", action: panelLSPRestart, value: s.ID})
			}
		}
		if s.State == event.LSPStateNotInstalled && s.InstallCommand != "" && !s.Installing {
			rows = append(rows, panelRow{label: "Install: " + s.InstallCommand, action: panelLSPInstall, value: s.ID})
		}
		return rows
	}
	return nil
}

// lspPanelServer finds the detail page's server in the current snapshot.
func lspPanelServer(panel *uiPanel) *event.LSPServerStatus {
	if panel == nil || panel.lsp == nil {
		return nil
	}
	for i := range panel.lsp.Servers {
		if panel.lsp.Servers[i].ID == panel.server {
			return &panel.lsp.Servers[i]
		}
	}
	return nil
}
