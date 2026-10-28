// Channel binding, route mounting, and the inbound turn path.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/channel"
	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// bindChannelsTo switches the running channel set to the given primary agent.
// Channels are project data, so what is bound is the agent's enabled project
// channels from the state database, each as its own instance carrying the
// project it serves. The build-and-bind operation itself lives on the registry
// (Registry.BindProjects); all that is left here is naming the agent and
// reporting a failure, because one misconfigured bot token must not take the
// gateway down.
func (s *Server) bindChannelsTo(ctx context.Context, active config.Summary) {
	if s == nil || s.Channels == nil {
		return
	}
	if err := s.Channels.BindProjects(ctx, active.ID, s.projectChannelBindings(ctx, active), s.bus()); err != nil {
		slog.Error("channel bind", "agent", active.ID, "err", err)
	}
}

// projectChannelBindings reads the active agent's enabled project channels and
// builds the running channel set from them. A channel whose stored config
// cannot be read is reported and skipped rather than taking the whole bind
// down.
func (s *Server) projectChannelBindings(ctx context.Context, active config.Summary) []channel.BoundChannel {
	store := s.projectChannelStore(active.ID)
	if store == nil {
		return nil
	}
	rows, err := store.ListEnabledForAgent(ctx)
	if err != nil {
		slog.Error("channel bind: list project channels", "agent", active.ID, "err", err)
		return nil
	}
	configs := make([]channel.ProjectChannelConfig, 0, len(rows))
	for _, row := range rows {
		section, perr := config.ParseChannelKindJSON(row.Kind, row.ConfigJSON)
		if perr != nil {
			slog.Warn("channel bind: parse channel config", "agent", active.ID, "project", row.ProjectID, "kind", row.Kind, "err", perr)
			continue
		}
		// Weixin keeps its login on disk instead of in the stored section; fold
		// it in the way the pre-project configuration did.
		if row.Kind == "weixin" {
			channel.MergePersistedClawbot(&section, active.WorkspaceRoot)
		}
		configs = append(configs, channel.ProjectChannelConfig{ProjectID: row.ProjectID, Kind: row.Kind, Section: section})
	}
	return channel.BuildProjectChannels(configs, active.WorkspaceRoot)
}

// channelTaskCommandRefusal is what a batch project's channel answers a built-in
// slash command with: a batch project runs the tasks it is handed, and the
// commands that steer a conversation have no conversation to steer.
const channelTaskCommandRefusal = "后台跑批项目只接收任务内容，不支持命令。"

// channelTaskRef is the origin_ref a task that came in through a channel
// carries: which channel carried it in, and the conversation id that channel
// addressed. The end notification answers those two values, and the
// channel-targets endpoint turns them back into a (channel, target) pair a
// project's settings page can offer (plan 151).
type channelTaskRef struct {
	Channel string `json:"channel"`
	Session string `json:"session"`
}

// newChannelTaskRef renders the origin_ref for one inbound channel message.
func newChannelTaskRef(channelID, sessionID string) string {
	raw, err := json.Marshal(channelTaskRef{Channel: strings.TrimSpace(channelID), Session: strings.TrimSpace(sessionID)})
	if err != nil {
		return ""
	}
	return string(raw)
}

// parseChannelTaskRef reads a task's origin_ref back. A ref that is not one of
// these — an older task, or another caller's own correlating record — reads as
// not a channel ref.
func parseChannelTaskRef(raw string) (channelTaskRef, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return channelTaskRef{}, false
	}
	var ref channelTaskRef
	if err := json.Unmarshal([]byte(raw), &ref); err != nil {
		return channelTaskRef{}, false
	}
	ref.Channel = strings.TrimSpace(ref.Channel)
	ref.Session = strings.TrimSpace(ref.Session)
	if ref.Channel == "" || ref.Session == "" {
		return channelTaskRef{}, false
	}
	return ref, true
}

// channelMessageCommandName returns the slash command a message opens with, or
// "" when it opens with none. It reads the name exactly the way the engine's
// dispatcher does, so "is this a command?" means the same thing on both sides.
func channelMessageCommandName(text string) string {
	line := strings.TrimSpace(text)
	if !strings.HasPrefix(line, "/") {
		return ""
	}
	tokens := strings.Fields(line)
	if len(tokens) == 0 {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(tokens[0]), "/")
}

// startChannelTask opens one inbound channel message of a batch project as a
// task of its own: a new task, its first execution, and the unattended turn
// that runs it (plan 151; owner 2026-10-26: every message through a batch
// project's channel is a new task).
//
// The chat is told that the task was taken, not what it produced: a batch
// execution's result arrives later, as its end notification, in whatever chat
// the task came from. The turn's own return value is therefore dropped here.
func (s *Server) startChannelTask(ctx context.Context, channelID, projectID, sessionID, text string) {
	if s == nil {
		return
	}
	content := strings.TrimSpace(text)
	// An empty message is not a task: a channel that reports an empty body (a
	// sticker, an event with no text) must not open one.
	if content == "" {
		return
	}
	bus := channelBus{s: s}
	if turn.IsBuiltinName(channelMessageCommandName(content)) {
		bus.deliverOutbound(ctx, channelID, sessionID, channelTaskCommandRefusal)
		return
	}
	store := s.projectStore()
	if store == nil {
		bus.deliverOutbound(ctx, channelID, sessionID, "任务创建失败：项目服务不可用")
		return
	}
	// workspace is the execution directory this call creates, if it gets that
	// far: every failure below takes it back, so a task the chat was told
	// failed leaves no half-made execution behind.
	workspace := ""
	fail := func(err error) {
		slog.Error("channel batch task", "channel", channelID, "project", projectID, "session", sessionID, "err", err)
		if workspace != "" {
			_ = os.RemoveAll(workspace)
		}
		bus.deliverOutbound(ctx, channelID, sessionID, "任务创建失败："+err.Error())
	}
	project, err := store.Get(ctx, projectID)
	if err != nil {
		fail(err)
		return
	}
	task, err := store.CreateTask(ctx, state.Task{
		ProjectID: project.ID,
		Title:     state.SessionTitleFromContent(content),
		Origin:    state.TaskOriginChannel,
		OriginRef: newChannelTaskRef(channelID, sessionID),
	})
	if err != nil {
		fail(err)
		return
	}
	// The execution is its own conversation with its own workspace, exactly
	// like a task the API opened; its input is fixed by the first turn, which
	// Submit makes one atomic decision with the transition to running.
	newSessionID := state.NewID("batch")
	if s.Sessions == nil {
		fail(errors.New("会话服务不可用"))
		return
	}
	if err := s.Sessions.EnsureAt(ctx, newSessionID, task.Title, state.SessionBirth{
		Cwd:       project.Root,
		GitBranch: memory.GitBranch(project.Root),
		ProjectID: project.ID,
		Source:    state.SessionSourceConversation,
	}); err != nil {
		fail(err)
		return
	}
	if err := s.bindSessionToProject(ctx, newSessionID, project); err != nil {
		fail(err)
		return
	}
	exec, err := store.AddTaskExecution(ctx, task.ID, newSessionID, project.Root)
	if err != nil {
		fail(err)
		return
	}
	workspace = exec.Workspace
	for _, dir := range []string{"inputs", "outputs", ".work"} {
		if err := os.MkdirAll(filepath.Join(exec.Workspace, dir), 0o755); err != nil {
			fail(err)
			return
		}
	}
	bus.deliverOutbound(ctx, channelID, sessionID, fmt.Sprintf("已接收任务：%s（%s），完成后通知。", task.Title, task.ID))
	// The turn runs unattended; submitChannelTurn births the conversation and
	// binds it again (idempotent), admits the batch turn and starts it.
	if _, ok := s.submitChannelTurn(ctx, channelID, projectID, newSessionID, content, content, "", ""); !ok {
		// No run was started, so no end notification will follow: the
		// execution stays pending, which is what the task's own page shows.
		// Its reason was reported into the execution's conversation.
		slog.Warn("channel batch task: turn refused", "channel", channelID, "task", task.ID, "session", newSessionID)
	}
}

const (
	// batchNotifyTimeout bounds one end-of-execution notification, delivery
	// included. It runs detached from the turn, which has already ended, so a
	// channel that stopped answering can never hold anything up.
	batchNotifyTimeout = 30 * time.Second
	// batchNotifyArtifactLimit is how many deliverables a notification names;
	// more than this is described by its count instead of the rest.
	batchNotifyArtifactLimit = 10
	// batchNotifySummaryRunes bounds the conclusion a notification carries,
	// and batchNotifyErrorRunes the failure it explains.
	batchNotifySummaryRunes = 300
	batchNotifyErrorRunes   = 200
	// batchNotifyMessageScan is how far back the conclusion is looked for. The
	// turn's final assistant message is at the very end of a transcript, and a
	// batch turn appends nothing after it.
	batchNotifyMessageScan = 50
)

// batchNotifyHeadline opens a notification with the status the execution ended
// in. The statuses are a whitelist; anything else is a plain ending, which is
// worth saying even if it names no particular outcome.
func batchNotifyHeadline(status string) string {
	switch status {
	case state.TaskStatusCompleted:
		return "【任务完成】"
	case state.TaskStatusFailed:
		return "【任务失败】"
	case state.TaskStatusStopped:
		return "【任务已停止】"
	}
	return "【任务结束】"
}

// notifyBatchExecutionEnd tells whoever asked for a task that its execution
// ended: the chat the task came in through, or the project's own notification
// targets when it did not come in through one (plan 151).
//
// Called by the engine as the execution settles, on the turn's own goroutine.
// The notification is therefore detached — the turn must not wait for a chat
// — and a delivery that fails is recorded on the execution and logged; it
// never touches the task's status, and never panics.
func (s *Server) notifyBatchExecutionEnd(ctx context.Context, end turn.BatchExecutionEnd) {
	if s == nil {
		return
	}
	go func() {
		sendCtx, cancel := context.WithTimeout(context.Background(), batchNotifyTimeout)
		defer cancel()
		s.deliverBatchNotify(sendCtx, end)
	}()
}

// deliverBatchNotify builds the notification and sends it to every target the
// execution's task has, collecting the deliveries that failed.
func (s *Server) deliverBatchNotify(ctx context.Context, end turn.BatchExecutionEnd) {
	if s.Channels == nil {
		return
	}
	if end.Task.Origin == state.TaskOriginChannel {
		ref, ok := parseChannelTaskRef(end.Task.OriginRef)
		if !ok {
			slog.Warn("batch notify: a channel task carries no channel ref", "task", end.Task.ID, "session", end.SessionID)
			return
		}
		// The task came in through a channel, so its answer goes back to the
		// same conversation, through the path every channel reply takes.
		s.DeliverChannelOutbound(ctx, ref.Channel, ref.Session, s.batchNotifyText(ctx, end))
		return
	}
	store := s.projectStore()
	if store == nil {
		return
	}
	project, err := store.Get(ctx, end.Task.ProjectID)
	if err != nil {
		slog.Warn("batch notify: project", "project", end.Task.ProjectID, "err", err)
		return
	}
	if len(project.TaskNotify) == 0 {
		// Nobody asked to be told: a task that came in through the API has no
		// chat waiting for its result.
		return
	}
	text := s.sanitizeBatchNotify(s.batchNotifyText(ctx, end))
	var failures []string
	for _, target := range project.TaskNotify {
		deliveryErr := s.Channels.Deliver(ctx, channel.Outbound{
			ChannelID: target.Channel,
			ProjectID: project.ID,
			SessionID: target.Target,
			Text:      text,
		})
		if deliveryErr != nil {
			failures = append(failures, target.Channel+" "+target.Target+": "+deliveryErr.Error())
		}
	}
	if len(failures) == 0 {
		return
	}
	detail := strings.Join(failures, "; ")
	slog.Warn("batch notify: delivery", "task", end.Task.ID, "session", end.SessionID, "err", detail)
	if err := store.SetExecutionNotifyError(ctx, end.SessionID, detail); err != nil {
		slog.Warn("batch notify: record the failure", "session", end.SessionID, "err", err)
	}
}

// sanitizeBatchNotify runs the outbound sanitizer a chat reply gets, when a
// live configuration is there to run it with.
func (s *Server) sanitizeBatchNotify(text string) string {
	if cfg := s.liveCfg(); cfg != nil {
		return safety.SanitizeOutbound(cfg, text)
	}
	return text
}

// batchNotifyText is the notification's fixed Chinese template. Every field a
// task lacks takes its whole line with it: a task that delivered nothing says
// nothing about deliverables, and a gateway with no public URL carries no
// link.
func (s *Server) batchNotifyText(ctx context.Context, end turn.BatchExecutionEnd) string {
	title := strings.TrimSpace(end.Task.Title)
	if title == "" {
		title = end.Task.ID
	}
	lines := []string{batchNotifyHeadline(end.Status) + title}
	progress := "任务 " + end.Task.ID
	if end.Execution.Seq > 0 {
		progress += fmt.Sprintf(" · 第 %d 次执行", end.Execution.Seq)
	}
	if worked := batchNotifyWorked(end.Execution); worked != "" {
		progress += " · 用时 " + worked
	}
	lines = append(lines, progress)
	if summary := s.batchNotifySummary(ctx, end.SessionID); summary != "" {
		lines = append(lines, "结论："+summary)
	}
	if end.Status == state.TaskStatusFailed {
		if reason := batchNotifyReason(end.ErrorCode, end.ErrorText); reason != "" {
			lines = append(lines, "原因："+reason)
		}
	}
	if deliverables := batchNotifyDeliverables(end.Artifacts); deliverables != "" {
		lines = append(lines, "生成物："+deliverables)
	}
	if link := batchNotifyLink(s.liveCfg(), end.Task.ProjectID, end.Task.ID); link != "" {
		lines = append(lines, "查看："+link)
	}
	return strings.Join(lines, "\n")
}

// batchNotifySummary is the last thing the execution's conversation said: the
// final assistant message, flattened onto one line and cut to what a chat
// message can carry.
func (s *Server) batchNotifySummary(ctx context.Context, sessionID string) string {
	if s == nil || s.Sessions == nil {
		return ""
	}
	rows, err := s.Sessions.ListRecentMessages(ctx, sessionID, batchNotifyMessageScan)
	if err != nil {
		slog.Warn("batch notify: read the conclusion", "session", sessionID, "err", err)
		return ""
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if !strings.EqualFold(strings.TrimSpace(rows[i].Role), llm.RoleAssistant) {
			continue
		}
		if text := flattenProse(rows[i].Content, batchNotifySummaryRunes); text != "" {
			return text
		}
	}
	return ""
}

// batchNotifyWorked is how long the execution ran, from its own clock. An
// execution whose window was never stamped has no duration to report.
func batchNotifyWorked(exec state.TaskExecution) string {
	if exec.StartedAt <= 0 || exec.FinishedAt <= 0 || exec.FinishedAt < exec.StartedAt {
		return ""
	}
	worked := time.Duration(exec.FinishedAt-exec.StartedAt) * time.Second
	return tool.FormatDuration(worked)
}

// batchNotifyReason explains a failed execution in one line: the stable code a
// client switches on, then what the failure said.
func batchNotifyReason(code, text string) string {
	code = strings.TrimSpace(code)
	reason := flattenProse(text, batchNotifyErrorRunes)
	switch {
	case code == "":
		return reason
	case reason == "":
		return code
	default:
		return code + "：" + reason
	}
}

// batchNotifyDeliverables lists what the execution delivered, at most
// batchNotifyArtifactLimit of them: past that the count speaks for the rest.
func batchNotifyDeliverables(artifacts []state.Artifact) string {
	if len(artifacts) == 0 {
		return ""
	}
	named := make([]string, 0, min(len(artifacts), batchNotifyArtifactLimit))
	for _, artifact := range artifacts {
		if len(named) == batchNotifyArtifactLimit {
			break
		}
		name := strings.TrimSpace(artifact.Name)
		if name == "" {
			name = filepath.Base(strings.TrimSpace(artifact.Path))
		}
		if name == "" {
			continue
		}
		named = append(named, name+"（"+tool.HumanSize(artifact.Size)+"）")
	}
	if len(named) == 0 {
		return ""
	}
	list := strings.Join(named, "、")
	if len(artifacts) > len(named) {
		list += fmt.Sprintf(" 等 %d 个", len(artifacts))
	}
	return list
}

// batchNotifyLink is the address a person opens to see the task, or "" when the
// gateway has no public URL to build one from.
func batchNotifyLink(cfg *config.Root, projectID, taskID string) string {
	if cfg == nil {
		return ""
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.Gateway.PublicURL), "/")
	if base == "" || strings.TrimSpace(projectID) == "" || strings.TrimSpace(taskID) == "" {
		return ""
	}
	return base + "/projects/" + strings.TrimSpace(projectID) + "/tasks/" + strings.TrimSpace(taskID)
}

// flattenProse turns free text into one chat line: every run of whitespace —
// the newlines a markdown answer is full of — becomes a single space, and the
// result is cut to limit runes.
func flattenProse(text string, limit int) string {
	flat := strings.Join(strings.Fields(text), " ")
	runes := []rune(flat)
	if len(runes) <= limit {
		return flat
	}
	return strings.TrimSpace(string(runes[:limit])) + "…"
}

// channelHandler mounts the bound channels' inbound endpoints ahead of the
// gateway's own router. This is the optional half: the registry serves the same
// routes on its own with a nil fallthrough, which is how channels run without
// the web UI or WS chat.
func (s *Server) channelHandler(inner http.Handler) http.Handler {
	if s == nil || s.Channels == nil {
		return inner
	}
	return s.Channels.HTTPHandler(inner)
}

// submitChannelTurn runs one inbound channel message through the canonical
// turn path (P8-3: "inbound goes through TurnService.Submit").
//
// It replaces a gateway-local run.Options assembly. Two things improve by
// construction rather than by extra code here:
//
//   - An approval gate is now a pause rather than a failure. The previous path
//     handed run's tool.RequiresActionError to the generic error branch, so a
//     channel user whose turn needed approval was told
//     "Request failed: tool shell: requires action: <uuid>" — an internal
//     error string — and the run was left paused with nobody able to act on it.
//   - The per-session foreground lock is taken once, by Submit, instead of by
//     the gateway's own wrapper.
func (s *Server) submitChannelTurn(ctx context.Context, channelID, projectID, sessionID, input, rawInput string, skillName, skillPath string) (turn.TurnOutcome, bool) {
	if s == nil || s.Core == nil {
		return turn.TurnOutcome{}, false
	}
	// The user's message is the turn's first row, stored as the turn starts the
	// way every other surface stores it: a turn that fails or parks on an
	// approval still has it, and it belongs to the run once the run exists.
	// The run folds a stored trailing message into its own input, so the model
	// is sent exactly what it was sent before.
	//
	// The conversation is born in the project whose channel carried it in: its
	// directory is the project root and it is filed under that project, so it
	// runs on the project's own runner. A message whose project cannot be
	// resolved falls back to the plain birth every ordinary session gets.
	var userRowID int64
	if s.Sessions != nil {
		ensure := true
		if pid := strings.TrimSpace(projectID); pid != "" {
			if p, perr := s.projectStore().Get(ctx, pid); perr == nil {
				birth := state.SessionBirth{Cwd: p.Root, GitBranch: memory.GitBranch(p.Root), ProjectID: p.ID}
				if eerr := s.Sessions.EnsureAt(ctx, sessionID, sessionID, birth); eerr == nil {
					if berr := s.bindSessionToProject(ctx, sessionID, p); berr != nil {
						slog.Error("channel session project bind", "channel", channelID, "session", sessionID, "err", berr)
					}
					ensure = false
				} else {
					slog.Error("channel session birth", "channel", channelID, "session", sessionID, "err", eerr)
				}
			} else {
				slog.Error("channel session project", "channel", channelID, "project", pid, "err", perr)
			}
		}
		userRowID, _ = turn.PersistUserTurn(ctx, s.Sessions, turn.UserTurn{
			SessionID: sessionID, ModelInput: input, RawInput: rawInput, EnsureSession: ensure,
		})
	}
	// A batch execution's turn is admitted before its message is stored: a
	// refused turn must not leave a stored message behind, and a message that
	// arrived as the task's instructions is what fixes the task's input — one
	// atomic decision, made in Submit.
	var batchInput *turn.BatchTurnInput
	if store := s.projectStore(); store != nil {
		if ptype, pErr := store.SessionProjectType(ctx, sessionID); pErr == nil && ptype == state.ProjectTypeBatch {
			batchInput = &turn.BatchTurnInput{Text: input}
			if _, admErr := turn.AdmitBatchTurn(ctx, store, sessionID, input, false); admErr != nil {
				slog.Error("channel batch turn refused", "channel", channelID, "session", sessionID, "err", admErr)
				return turn.TurnOutcome{SessionID: sessionID}, false
			}
		}
	}
	out, err := s.Core.Submit(ctx, turn.TurnRequest{
		SessionID:  sessionID,
		Origin:     turn.Origin{Surface: turn.SurfaceChannel, ChannelID: channelID},
		UserText:   input,
		RawInput:   rawInput,
		SkillName:  skillName,
		SkillPath:  skillPath,
		BatchInput: batchInput,
	}, nil)
	if s.Sessions != nil {
		_ = s.Sessions.BindMessageToRun(ctx, sessionID, userRowID, out.RunID)
	}
	switch {
	case err != nil:
		slog.Error("inbound run", "channel", channelID, "session", sessionID, "err", err)
		// The run ended in failure; its clock is what closes it on replay.
		s.stampChannelRunEnd(ctx, out)
		// What the user reads is the failure explained, as on every surface;
		// the raw error stays in the log.
		channelBus{s: s}.deliverOutbound(context.Background(), channelID, sessionID, llm.ExplainError(err))
		return out, false
	case out.Status == turn.TurnWaitingApproval:
		// The run stays paused and the resume path owns it. Tell the user what
		// is being asked rather than leaking the gate's error text.
		channelBus{s: s}.deliverOutbound(context.Background(), channelID, sessionID, channelApprovalNotice(out))
		return out, false
	case out.Result == nil:
		s.stampChannelRunEnd(ctx, out)
		return out, false
	}
	return out, true
}

// stampChannelRunEnd records the clock of a channel run that ended with
// nothing to append.
func (s *Server) stampChannelRunEnd(ctx context.Context, out turn.TurnOutcome) {
	if s.Sessions == nil {
		return
	}
	finishedAt := time.Now()
	_ = s.Sessions.StampRunTiming(ctx, out.RunID, state.RunTiming{StartedAt: finishedAt.Add(-out.Duration), FinishedAt: finishedAt, Worked: out.Duration})
}

// approvalNoticeText tells a channel user, in one line they can act on, that
// a conversation is waiting for an approval, without exposing action IDs or
// internal error text. An unnamed tool is "a tool".
func approvalNoticeText(toolName string) string {
	name := strings.TrimSpace(toolName)
	if name == "" {
		name = "a tool"
	}
	return "Waiting for approval to run " + name + ". Approve it in the Forebrain Harness app to continue."
}

// channelApprovalNotice describes a pending approval in one line a channel
// user can act on, without exposing action IDs or internal error text.
func channelApprovalNotice(out turn.TurnOutcome) string {
	name := ""
	if out.Approval != nil {
		name = out.Approval.PermissionToolName
	}
	return approvalNoticeText(name)
}

type callback struct {
	Kind       string `json:"kind"`
	JobID      string `json:"job_id,omitempty"`
	WorkItemID string `json:"work_item_id,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	ChannelID  string `json:"channel_id,omitempty"`
	Status     string `json:"status"`
	Title      string `json:"title,omitempty"`
	Message    string `json:"message,omitempty"`
}

func (s *Server) notifyRuntimeHook(ctx context.Context, req tool.NotificationRequest) {
	if s == nil || s.NotificationHook == nil {
		return
	}
	_ = tool.NotifyHook(ctx, s.NotificationHook, req)
	if s.Sessions == nil || s.Runner == nil {
		return
	}
	contractInput, ok := tool.BuildNotificationHookInput(req)
	if !ok {
		return
	}
	transcriptPath, err := hook.WriteSessionTranscriptArtifact(s.stateRoot(), s.Sessions, req.SessionID)
	if err != nil {
		return
	}
	rt := &hook.Runtime{
		Home:          s.Home,
		WorkspaceRoot: s.stateRoot(),
		Cfg:           s.liveCfg(),
		Sess:          s.Sessions,
		NewPromptRunner: func(label string) (hook.PromptRun, error) {
			fac := run.Factory{
				Home:          s.Home,
				AgentName:     s.Runner.AgentName,
				WorkspaceRoot: s.Runner.StateRoot(),
				ProjectRoot:   s.Runner.ProjectRoot,
				MemoryStore:   s.MemoryStore,
				AppCfg:        s.liveCfg(),
			}
			rr := fac.NewIsolatedRunner(label)
			if rr == nil {
				return nil, fmt.Errorf("nil isolated runner")
			}
			if err := rr.Load(); err != nil {
				return nil, err
			}
			return func(ctx context.Context, input string) (string, error) {
				return run.RunText(rr, ctx, input)
			}, nil
		},
		RunAgentHook: s.Runner.RunAgentHook,
		SessionID:    llm.AgentSessionIDFromContext,
		ToolUseID:    tool.ToolUseIDFromContext,
	}
	_, _ = rt.ExecuteNotification(ctx, hook.NotificationInput{
		BaseInput: hook.BaseInput{
			HookEventName:  hook.EventNotification,
			SessionID:      req.SessionID,
			TranscriptPath: transcriptPath,
			Cwd:            s.Home,
		},
		Message:          contractInput.Message,
		Title:            contractInput.Title,
		NotificationType: contractInput.NotificationType,
	})
}

// channelLabels names every channel kind the way the web pages name it.
//
// The channel table and the channel editor show a kind by its display name, and
// the table is searched on the server, so the server has to know the same names
// the page draws. The single source of those names is
// frontend/src/lib/channelLabels.json: the frontend reads it to draw and this
// file mirrors it to search, and TestChannelLabelsMatchFrontendJSON fails the
// build the moment the two disagree. Adding a kind means adding it to
// channel.ChannelKinds, to that JSON file and to this map.

// channelLabels is {kind: {zh, en}} — the display name in each of the web's two
// languages, in the order the JSON file lists them.
var channelLabels = map[string][2]string{
	"telegram":       {"Telegram", "Telegram"},
	"discord":        {"Discord", "Discord"},
	"slack":          {"Slack", "Slack"},
	"whatsapp":       {"WhatsApp", "WhatsApp"},
	"signal":         {"Signal", "Signal"},
	"mattermost":     {"Mattermost", "Mattermost"},
	"matrix":         {"Matrix", "Matrix"},
	"homeassistant":  {"Home Assistant", "Home Assistant"},
	"email":          {"邮件", "Email"},
	"sms":            {"短信", "SMS"},
	"webhook":        {"Webhook", "Webhook"},
	"bluebubbles":    {"BlueBubbles", "BlueBubbles"},
	"wecom":          {"企业微信", "WeCom"},
	"wecom_callback": {"企业微信（回调）", "WeCom callback"},
	"weixin":         {"微信", "WeChat"},
	"feishu":         {"飞书", "Feishu"},
	"dingtalk":       {"钉钉", "DingTalk"},
	"qq":             {"QQ", "QQ"},
}

// channelLabel returns a kind's display name in each of the web's two
// languages. A kind the table does not know reads as itself, which is what the
// page shows for it too.
func channelLabel(kind string) (zh, en string) {
	if names, ok := channelLabels[kind]; ok {
		return names[0], names[1]
	}
	return kind, kind
}
