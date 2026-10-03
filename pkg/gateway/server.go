package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/channel"
	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/session"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type Server struct {
	Home   string
	Core   *turn.Service
	Runner *run.Runner
	Env    *process.Environment
	// LaunchProject is the frozen launch-project trust decision of the process
	// this server runs in. The global skill routes act on it (their project
	// scope writes there), so a request never has to ask where the process was
	// started — a gateway serving several projects reaches each of them
	// through the project-scoped routes instead. Empty when the composition
	// root did not supply one (an embedded or test server).
	LaunchProject safety.ProjectContext
	// Projects is the project store for the active primary agent. Sessions
	// created in a project are bound through it, and the runner pool reads
	// the same binding when serving a session's turns.
	Projects         *state.ProjectStore
	Actions          *state.ActionService
	RunRT            *state.RunStore
	Files            *state.FileStore
	MemoryStore      *memory.Store
	Sessions         *state.SessionStore
	Hooks            *hook.AgentPipeline
	SkillHub         *skill.Hub
	Channels         *channel.Registry
	Notifier         *event.Notifier
	NotificationHook tool.NotificationHook

	StaticDist string
	StaticFS   fs.FS

	runControlMu      sync.Mutex
	runControl        *run.Controller
	runStartedAt      sync.Map
	approvalRecovery  sync.Once
	approvalOwnerOnce sync.Once
	approvalOwner     string

	mcpOAuthPKCEMu sync.Mutex
	mcpOAuthPKCE   map[string]gatewayMcpPKCE

	runEventsOnce sync.Once
	runEvents     *runEventBus
}

// runnerFor resolves the Runner one session's requests must run on. A session
// bound to a project gets that project's pooled runner — its own frozen MCP
// list, project skills, and instruction snapshot — while everything else runs
// on the agent-level base Runner. Callers without a session (rosters, model
// listings, telemetry) keep using s.Runner directly: those read agent-level
// state that is identical across the pool.
func (s *Server) runnerFor(ctx context.Context, sessionID string) *run.Runner {
	if s == nil {
		return nil
	}
	if s.Env != nil {
		return s.Env.RunnerForSession(ctx, strings.TrimSpace(sessionID))
	}
	return s.Runner
}

// projectStore lazily builds the project store for the active agent.
func (s *Server) projectStore() *state.ProjectStore {
	if s == nil {
		return nil
	}
	if s.Projects != nil {
		return s.Projects
	}
	if s.Env != nil && s.Env.SQL != nil {
		s.Projects = state.NewProjectStore(s.Env.SQL, s.Runner.AgentName)
	}
	return s.Projects
}

func (s *Server) approvalResumeOwner() string {
	if s == nil {
		return ""
	}
	s.approvalOwnerOnce.Do(func() { s.approvalOwner = "gateway:" + uuid.NewString() })
	return s.approvalOwner
}

// bindSchedulerTo points the runtime's standing work at one primary agent.
// Cron jobs and heartbeats are tenant data, so they are rebound on a switch the
// same way the channel registry is: the previous agent's jobs stop before this
// agent's start.
func (s *Server) bindSchedulerTo(ctx context.Context, active config.Summary) {
	if s == nil || s.Env == nil {
		return
	}
	s.Env.Cron().Bind(ctx, strings.TrimSpace(active.ID))
}

// cron is the shared standing-work service. The surfaces do not own it: it
// lives in the runtime so the terminal and the web read and write the same
// jobs, with the same rules about when they fire.
func (s *Server) cron() *process.CronService {
	if s == nil || s.Env == nil {
		return nil
	}
	return s.Env.Cron()
}

// RunEvents is the sink the runner publishes its run-event stream into, and the
// bus every websocket connection takes its share of that stream from. It is
// created on first use so a Server assembled field-by-field (tests, and the
// serve entrypoint alike) never has to remember to build it.
func (s *Server) RunEvents() *runEventBus {
	if s == nil {
		return nil
	}
	s.runEventsOnce.Do(func() { s.runEvents = newRunEventBus(s.RunRT) })
	return s.runEvents
}

func (s *Server) runController() *run.Controller {
	if s == nil {
		return nil
	}
	if s.Env != nil && s.Env.Control != nil {
		return s.Env.Control
	}
	s.runControlMu.Lock()
	defer s.runControlMu.Unlock()
	if s.runControl == nil {
		s.runControl = run.NewController()
	}
	return s.runControl
}

func (s *Server) sessionOwned(ctx context.Context, sessionID string) (bool, error) {
	if s == nil || s.Sessions == nil {
		return true, nil
	}
	return s.Sessions.HasSession(ctx, strings.TrimSpace(sessionID))
}

func (s *Server) approvalService() turn.ApprovalService {
	var policy turn.ApprovalPolicy
	var network turn.ApprovalNetwork
	if s != nil && s.Runner != nil {
		policy = s.Runner
		network = s.Env.Tools()
	}
	return turn.ApprovalService{Actions: s.Actions, Policy: policy, Network: network}
}

func actionPayloadBool(raw, key string) bool {
	var payload map[string]any
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return false
	}
	value, _ := payload[key].(bool)
	return value
}

func gatewayToolMetaPayload(meta tool.ToolMeta) map[string]any {
	if strings.TrimSpace(meta.ToolName) == "" &&
		strings.TrimSpace(meta.Status) == "" &&
		strings.TrimSpace(meta.Purpose) == "" &&
		strings.TrimSpace(meta.Invocation) == "" &&
		len(meta.Input) == 0 {
		return nil
	}
	out := map[string]any{}
	if v := strings.TrimSpace(meta.ToolName); v != "" {
		out["tool_name"] = v
	}
	if v := strings.TrimSpace(meta.Status); v != "" {
		out["status"] = v
	}
	if v := strings.TrimSpace(meta.Purpose); v != "" {
		out["purpose"] = v
	}
	if v := strings.TrimSpace(meta.Invocation); v != "" {
		out["invocation"] = v
	}
	if len(meta.Input) > 0 {
		out["input"] = meta.Input
	}
	if v := strings.TrimSpace(meta.AgentID); v != "" {
		out["agent_id"] = v
	}
	if v := strings.TrimSpace(meta.AgentType); v != "" {
		out["agent_type"] = v
	}
	if v := strings.TrimSpace(meta.AgentKind); v != "" {
		out["agent_kind"] = v
	}
	if v := strings.TrimSpace(meta.Category); v != "" {
		out["category"] = v
	}
	if v := strings.TrimSpace(meta.SkillName); v != "" {
		out["skill_name"] = v
	}
	if v := strings.TrimSpace(meta.SkillPath); v != "" {
		out["skill_path"] = v
	}
	if meta.StartedAtMs > 0 {
		out["started_at_ms"] = meta.StartedAtMs
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func gatewayToolStartedStepData(stepID, description, toolName string, input map[string]any, meta tool.ToolMeta) map[string]any {
	data := map[string]any{
		"kind":          event.RunEventToolStarted,
		"step_id":       strings.TrimSpace(stepID),
		"description":   strings.TrimSpace(description),
		"tool_name":     strings.TrimSpace(toolName),
		"current_query": "",
	}
	if len(input) > 0 {
		data["input"] = input
	}
	if toolMeta := gatewayToolMetaPayload(meta); len(toolMeta) > 0 {
		data["tool_meta"] = toolMeta
	}
	return data
}

func gatewayToolCompletedStepData(stepID, description, toolName string, output map[string]any, errText string, displayBody string, actionID string, actionKind string, meta tool.ToolMeta) map[string]any {
	data := map[string]any{
		"kind":             event.RunEventToolCompleted,
		"step_id":          strings.TrimSpace(stepID),
		"description":      strings.TrimSpace(description),
		"duration_seconds": 0,
		"tool_name":        strings.TrimSpace(toolName),
		"error":            strings.TrimSpace(errText),
	}
	if len(output) > 0 {
		data["output"] = output
	}
	if errType := event.ClassifyToolError(errText); errType != "" {
		data["error_type"] = errType
	}
	if body := strings.TrimSpace(displayBody); body != "" {
		data["display_body"] = body
	}
	if v := strings.TrimSpace(actionID); v != "" {
		data["action_id"] = v
	}
	if v := strings.TrimSpace(actionKind); v != "" {
		data["action_kind"] = v
	}
	if toolMeta := gatewayToolMetaPayload(meta); len(toolMeta) > 0 {
		data["tool_meta"] = toolMeta
	}
	return data
}

// withStepSummary attaches the runtime's one-line label for a tool call to the
// step payload. The terminal shows that line on every tool card; sending it
// means the web surface labels the same call with the same words instead of
// inventing its own.
func withStepSummary(data map[string]any, summary string) map[string]any {
	if data == nil {
		return nil
	}
	if s := strings.TrimSpace(summary); s != "" {
		data["summary"] = s
	}
	return data
}

func anyMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

func (s *Server) liveCfg() *config.Root {
	if s == nil || s.Env == nil {
		return nil
	}
	return s.Env.Deps.AppCfg
}

// stateRoot returns the active primary agent's per-agent state root (its
// workspace root), onto which the mode/plan/todo stores join "state". Every
// gateway seam touching per-agent state must use this so a non-main primary
// agent gets its own isolated state and the allowed plan path matches the one
// the agent runtime resolves.
func (s *Server) stateRoot() string {
	if s == nil {
		return ""
	}
	return config.ActiveStateRoot(s.Home, s.liveCfg())
}

// projectKey returns the runner's launch-project scope. Every context this
// server builds must use it: plan files live in <stateRoot>/plans/<key>, so a
// context wired with a different key allows writes to a directory other than
// the one the plan-mode reminder tells the model to write, and every plan write
// is rejected.
func (s *Server) projectKey() string {
	if s == nil || s.Runner == nil {
		return ""
	}
	return strings.TrimSpace(s.Runner.ProjectKey)
}

func (s *Server) bus() channel.Bus {
	return channelBus{s: s}
}

type channelBus struct {
	s *Server
}

func (s *Server) DeliverChannelOutbound(ctx context.Context, channelID, sessionID, text string) {
	if s == nil {
		return
	}
	channelBus{s: s}.deliverOutbound(ctx, channelID, sessionID, text)
}

func (b channelBus) deliverOutbound(ctx context.Context, channelID string, sessionID string, text string) {
	b.deliverOutboundMaybeSanitized(ctx, channelID, sessionID, text, false)
}

func (b channelBus) deliverOutboundPresanitized(ctx context.Context, channelID string, sessionID string, text string) {
	b.deliverOutboundMaybeSanitized(ctx, channelID, sessionID, text, true)
}

// deliverOutboundMaybeSanitized applies the two things that are the gateway's
// to apply — outbound sanitizing, which needs the live config, and unwrapping
// the channel-prefixed session ID — and hands the rest to the registry, which
// owns channel resolution and transport.
//
// The two halves stay here for different reasons, and only one of them is
// enforced. Sanitizing needs pkg/safety, which is Layer 2 to channel's Layer 1:
// moving it would trip TestLayerCeiling. Unwrapping needs pkg/state, which is
// Layer 1 too, so the ceiling would permit it — keeping it out is a choice, to
// avoid a same-layer sibling dependency of the kind C7 forbids for
// skill/memory. If that ever stops being worth it, the honest move is to give
// pkg/channel the channel-session prefix helpers outright (they are about
// channel identity, and pkg/state uses them nowhere else) rather than to have
// channel import state.
func (b channelBus) deliverOutboundMaybeSanitized(ctx context.Context, channelID string, sessionID string, text string, skipOutboundSanitize bool) {
	chID := strings.TrimSpace(channelID)
	if chID == "" || strings.TrimSpace(text) == "" || b.s == nil || b.s.Channels == nil {
		return
	}
	if !skipOutboundSanitize {
		if lc := b.s.liveCfg(); lc != nil {
			text = safety.SanitizeOutbound(lc, text)
		}
	}
	err := b.s.Channels.Deliver(ctx, channel.Outbound{
		ChannelID: chID,
		SessionID: state.UnwrapChannelSession(chID, sessionID),
		Text:      text,
	})
	// An unbound or inbound-only channel is a normal outcome (an agent switch
	// can retire a channel between a reply being produced and being sent), and
	// the pre-move code dropped both silently. Keep that, and keep the log
	// reserved for a send that actually failed.
	if err != nil && !errors.Is(err, channel.ErrNoSuchChannel) && !errors.Is(err, channel.ErrNotOutbound) {
		slog.Error("channel outbound", "channel", chID, "err", err)
	}
}

func (b channelBus) PublishInbound(ctx context.Context, m channel.Inbound) error {
	go func() {
		ch := strings.TrimSpace(m.ChannelID)
		if ch == "" {
			ch = "wecom"
		}
		sid := state.WrapChannelSession(ch, m.SessionID)
		if sid == "" {
			slog.Warn("drop inbound message with empty session id", "channel", ch)
			return
		}
		sc := parseSlashCommand(b.s, sid, ch, m.Text)
		if sc.ModeChanged {
			slog.Info("mode changed", "session", sid, "mode", sc.Mode, "phase", sc.Phase, "source", "channel")
		}
		if sc.ShouldContinueRun {
			input := strings.TrimSpace(sc.ContinueInput)
			if input == "" {
				input = strings.TrimSpace(m.Text)
			}
			if cerr := b.s.autoCompactBeforeUserAppend(context.Background(), sid, ch, input, m.Text); cerr != nil {
				slog.Debug("channel auto compact skipped", "session", sid, "channel", ch, "err", cerr)
			}
			outcome, ok := b.s.submitChannelTurn(context.Background(), ch, sid, input, m.Text, sc.SkillName, sc.SkillPath)
			if !ok {
				return
			}
			res, runID := outcome.Result, outcome.RunID
			{
				ctxBg := context.Background()
				out := res.TextContent()
				if lc := b.s.liveCfg(); lc != nil {
					out = safety.SanitizeOutbound(lc, out)
				}
				finishedAt := time.Now()
				b.s.finishSuccessfulTurn(ctxBg, gatewayPostTurnOptions{
					SessionID:       sid,
					ChannelID:       ch,
					RunID:           runID,
					AssistantText:   out,
					AssistantResult: res,
					AppendAssistant: true,
					RunStartedAt:    finishedAt.Add(-outcome.Duration),
					RunFinishedAt:   finishedAt,
					WorkedMs:        outcome.Duration.Milliseconds(),
				})
				b.deliverOutboundPresanitized(context.Background(), ch, sid, out)
			}
			return
		}
		if sc.Handled {
			reply := sc.Reply
			b.deliverOutbound(context.Background(), ch, sid, reply)
			return
		}
		input := m.Text
		if cerr := b.s.autoCompactBeforeUserAppend(context.Background(), sid, ch, input, ""); cerr != nil {
			slog.Debug("channel auto compact skipped", "session", sid, "channel", ch, "err", cerr)
		}
		if b.s.Sessions != nil {
			transcriptPath, terr := hook.WriteSessionTranscriptArtifact(b.s.stateRoot(), b.s.Sessions, sid)
			if terr != nil {
				slog.Error("channel hook transcript", "session", sid, "channel", ch, "err", terr)
				return
			}
			rt := &hook.Runtime{
				Home:          b.s.Home,
				WorkspaceRoot: b.s.stateRoot(),
				Cfg:           b.s.liveCfg(),
				Sess:          b.s.Sessions,
				NewPromptRunner: func(label string) (hook.PromptRun, error) {
					fac := run.Factory{
						Home:          b.s.Home,
						AgentName:     b.s.Runner.AgentName,
						WorkspaceRoot: b.s.Runner.StateRoot(),
						ProjectRoot:   b.s.Runner.ProjectRoot,
						MemoryStore:   b.s.MemoryStore,
						AppCfg:        b.s.liveCfg(),
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
				RunAgentHook: b.s.Runner.RunAgentHook,
				SessionID:    llm.AgentSessionIDFromContext,
				ToolUseID:    tool.ToolUseIDFromContext,
			}
			hookOut, herr := rt.ExecuteUserPromptSubmit(context.Background(), hook.UserPromptSubmitInput{
				BaseInput: hook.BaseInput{
					HookEventName:  hook.EventUserPromptSubmit,
					SessionID:      sid,
					TranscriptPath: transcriptPath,
					Cwd:            b.s.Home,
					PermissionMode: "on-request",
				},
				Prompt: input,
			})
			if herr != nil {
				slog.Error("channel submit hook", "session", sid, "channel", ch, "err", herr)
				return
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
				b.deliverOutbound(context.Background(), ch, m.SessionID, msg)
				return
			}
		}
		outcome, ok := b.s.submitChannelTurn(context.Background(), ch, sid, input, "", "", "")
		if !ok {
			return
		}
		res, runID := outcome.Result, outcome.RunID
		{
			ctxBg := context.Background()
			out := res.TextContent()
			if lc := b.s.liveCfg(); lc != nil {
				out = safety.SanitizeOutbound(lc, out)
			}
			finishedAt := time.Now()
			b.s.finishSuccessfulTurn(ctxBg, gatewayPostTurnOptions{
				SessionID:       sid,
				ChannelID:       ch,
				RunID:           runID,
				AssistantText:   out,
				AssistantResult: res,
				AppendAssistant: true,
				RunStartedAt:    finishedAt.Add(-outcome.Duration),
				RunFinishedAt:   finishedAt,
				WorkedMs:        outcome.Duration.Milliseconds(),
			})
			b.deliverOutboundPresanitized(context.Background(), ch, m.SessionID, out)
		}
	}()
	return nil
}

func (s *Server) AttachREST(srv *RestServer) {
	ctx := context.Background()
	ok := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
	srv.Get("/health", ok)
	srv.Get("/healthz", ok)
	srv.Get("/readyz", ok)

	s.AttachExtraRoutes(srv)
	// Channels are mounted on the registry's own route table rather than this
	// router, because they have to be replaceable when the primary agent
	// changes: a router registration cannot be withdrawn, so a switched-away
	// agent's inbound endpoints would keep answering.
	if active, err := s.activePrimarySummary(); err == nil {
		s.bindChannelsTo(ctx, active)
		s.bindSchedulerTo(ctx, active)
	} else {
		slog.Error("channel bind: resolve active primary agent", "err", err)
	}
	s.attachStaticUI(srv)
}

// The default origin check applies: a browser handshake must come from the
// same host it shares the session cookie with.
var upgrader = websocket.Upgrader{}

type wsClientMsg struct {
	Op                     string                             `json:"op"`
	ProtocolVersion        string                             `json:"protocol_version"`
	Type                   string                             `json:"type"`
	RequestID              string                             `json:"request_id"`
	RunID                  string                             `json:"run_id"`
	ActionID               string                             `json:"action_id"`
	SessionID              string                             `json:"session_id"`
	CreateBy               string                             `json:"create_by"`
	ModelID                string                             `json:"model_id"`
	Mode                   string                             `json:"mode"`
	Phase                  string                             `json:"phase"`
	Answers                any                                `json:"answers"`
	ApprovalDecision       string                             `json:"approval_decision"`
	NetworkPolicyAmendment *safety.NetworkPolicyAmendment     `json:"network_policy_amendment"`
	ExecPolicyAmendment    safety.ExecPolicyAmendment         `json:"execpolicy_amendment"`
	RequestPermissions     *safety.RequestPermissionsResponse `json:"request_permissions_response"`
	Request                event.GatewayControlPayload        `json:"request"`
	Message                wsClientMessage                    `json:"message"`
	Content                string                             `json:"content"`
	Cursor                 int64                              `json:"cursor"`
}

// wsClientMessage is the user turn a web client submits.
type wsClientMessage struct {
	// Choice is set when the message answers a picker a slash command
	// offered; Content is then that command.
	Choice      *turn.SlashChoice `json:"choice,omitempty"`
	Content     string            `json:"content"`
	Role        string            `json:"role"`
	Attachments []string          `json:"attachments"`
	// MentionImages are workspace-relative paths the composer's @ picker
	// resolved to images. They are sent explicitly rather than parsed back out
	// of the text: a path in a prompt is just a path, on every surface, and the
	// server re-validates these before attaching them.
	MentionImages []string `json:"mention_images"`
	// SkillName/SkillPath activate one skill for this turn — the web workshop's
	// way of doing what the terminal's slash handoff does. The path is
	// validated against the live skill set before the turn starts; a slash
	// command naming a skill still wins.
	SkillName string `json:"skill_name,omitempty"`
	SkillPath string `json:"skill_path,omitempty"`
}

type wsServerMsg struct {
	Op        string `json:"op"`
	RequestID string `json:"request_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
	Text      string `json:"text,omitempty"`
	Message   string `json:"message,omitempty"`
	Data      any    `json:"data,omitempty"`
	Error     string `json:"error,omitempty"`
	// The run's checklist facts, on the three operations that end a run.
	// writeMsg fills them, so no ending can leave them out.
	event.RunPlanFacts
}

func runEventToWSMessage(requestID, traceID string, evt event.RunEvent) wsServerMsg {
	return wsServerMsg{
		Op:        "run_event",
		RequestID: strings.TrimSpace(requestID),
		RunID:     strings.TrimSpace(evt.RunID),
		SessionID: strings.TrimSpace(evt.SessionID),
		TraceID:   strings.TrimSpace(traceID),
		Data:      evt,
	}
}

func (s *Server) HandleChatWS(w http.ResponseWriter, r *http.Request) {
	if s != nil && s.Env != nil && s.Env.Deps.AppCfg != nil {
		if err := authorizeControlPlane(r, s.Env.Deps.AppCfg); err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	c.SetReadLimit(4 << 20)
	_ = c.SetReadDeadline(time.Now().Add(75 * time.Second))
	c.SetPongHandler(func(string) error {
		return c.SetReadDeadline(time.Now().Add(75 * time.Second))
	})

	notifCtx, notifCancel := context.WithCancel(context.Background())
	defer notifCancel()
	notifQ := event.New[event.TaskEvent]()
	notifCh := notifQ.Start(notifCtx)
	// Canonical events are durable before fan-out, so this connection can use a
	// bounded queue. A slow reader is disconnected with an explicit resync hint
	// and can replay from its cursor instead of growing the gateway heap without
	// bound. Task notifications keep their older queue because they do not yet
	// have the durable cursor contract run events have.
	type queuedRunEvent struct {
		event      event.RunEvent
		generation uint64
		// flushed makes the entry a barrier instead of an event: it is closed
		// once everything queued ahead of it has been written.
		flushed chan struct{}
	}
	var runEventBinding atomic.Uint64
	runEvtCh := make(chan queuedRunEvent, 512)
	runEvtOverflow := make(chan queuedRunEvent, 1)
	runEvtSub := s.RunEvents().Subscribe(func(evt event.RunEvent) {
		queued := queuedRunEvent{event: evt, generation: runEventBinding.Load()}
		select {
		case runEvtCh <- queued:
		default:
			select {
			case runEvtOverflow <- queued:
			default:
			}
		}
	})
	defer runEvtSub.Close()
	var wsSid string
	var unsubWS func()
	// The MCP status subscription is per bound session, like unsubWS, and is
	// re-pointed on every switch so a socket only ever hears about the servers of
	// the session it is showing. The callback enqueues rather than writes: it runs
	// on the goroutine a status change came from, and the socket write belongs to
	// this loop.
	var unsubMCPStatus func()
	// mcpBoundSession is which session the MCP subscription is on. It is tracked
	// separately from wsSid because the two are bound by different things: the
	// notification binding only moves when there is a Notifier to subscribe to,
	// and an MCP watch that keyed off it would re-subscribe on every bind for a
	// socket that has none.
	mcpBoundSession := ""
	// The queue dies with the connection: it is started on the same context as
	// the notification queue, so its pump and the writer below exit when this
	// handler returns instead of outliving every socket the process ever served.
	type mcpStatusUpdate struct {
		sessionID string
		snapshot  run.MCPSnapshot
	}
	mcpStatusQ := event.New[mcpStatusUpdate]()
	mcpStatusCh := mcpStatusQ.Start(notifCtx)
	defer func() {
		if unsubMCPStatus != nil {
			unsubMCPStatus()
		}
	}()
	// pushMCPStatusSnapshot sends the generation's current state and then
	// subscribes for later changes, in that order: the snapshot is what the
	// client missed while it was not bound, and subscribing first would only
	// reorder the same two facts.
	//
	// The session id travels with each update rather than being read at write
	// time: the subscription replays immediately, before the bind has finished
	// recording which session this socket is on, and labelling that first
	// snapshot with the session the socket is leaving would file it under the
	// wrong conversation.
	pushMCPStatusSnapshot := func(sid string) {
		if unsubMCPStatus != nil {
			unsubMCPStatus()
			unsubMCPStatus = nil
		}
		runner := s.runnerFor(context.Background(), sid)
		if runner == nil {
			return
		}
		unsubMCPStatus = runner.MCPStartup().Subscribe(func(snapshot run.MCPSnapshot) {
			mcpStatusQ.Push(mcpStatusUpdate{sessionID: sid, snapshot: snapshot})
		})
	}
	var currentRunID string
	var currentTraceID atomic.Value
	currentTraceID.Store("")
	loadCurrentTraceID := func() string {
		traceID, _ := currentTraceID.Load().(string)
		return traceID
	}
	var runEventCancel func()
	connectionID := "conn-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	requestCache := map[string]wsRequestCacheEntry{}
	var writeMu sync.Mutex
	// Binding changes and queued event writes share this lock. It prevents an
	// event already queued for session A from being written after the same
	// socket has acknowledged a switch to session B, without making publishers
	// wait on websocket I/O.
	// commandCancel stops what the connection is doing with a message before
	// its run exists — the slash command, then the compaction the turn waits
	// on. That work runs on the connection's loop, so the stop reaches it
	// through the reader, which keeps reading while the loop is busy.
	var commandMu sync.Mutex
	var commandCancel context.CancelFunc
	var runEventBindingMu sync.Mutex
	boundRunEventSession := ""
	// bindRunEvents points the socket's live run events at a session. Binding
	// to the session it is already bound to changes nothing, so an event the
	// session published in between is not dropped as belonging to an older
	// binding.
	bindRunEvents := func(sid string) {
		sid = strings.TrimSpace(sid)
		runEventBindingMu.Lock()
		defer runEventBindingMu.Unlock()
		if sid == boundRunEventSession {
			return
		}
		boundRunEventSession = sid
		runEventBinding.Add(1)
		runEvtSub.Bind(sid)
	}
	doneCh := make(chan struct{})
	defer close(doneCh)
	// flushRunEvents returns once every run event queued so far has been
	// written. A command's events are queued as it publishes them, but written
	// by their own goroutine; a reply that ends the command is written after
	// them, or the client — which stops reading at the reply — never sees how
	// the command's work ended.
	flushRunEvents := func() {
		flushed := make(chan struct{})
		select {
		case runEvtCh <- queuedRunEvent{flushed: flushed}:
		case <-doneCh:
			return
		}
		select {
		case <-flushed:
		case <-doneCh:
		}
	}
	defer func() {
		if runEventCancel != nil {
			runEventCancel()
		}
	}()
	writeMsg := func(m wsServerMsg) {
		if traceID := loadCurrentTraceID(); m.TraceID == "" && traceID != "" {
			m.TraceID = traceID
		}
		if wsRunEndOps[m.Op] && strings.TrimSpace(m.RunID) != "" {
			m.RunPlanFacts = s.runPlanFacts(context.Background(), m.RunID)
		}
		canonical := canonicalRunEventsFromWS(m)
		writeMu.Lock()
		defer writeMu.Unlock()
		for _, evt := range canonical {
			// Commit before either this legacy-compatible operation or the
			// canonical bus fan-out can become visible. If durability fails, end
			// the client stream explicitly instead of presenting state resume
			// cannot reconstruct.
			if strings.TrimSpace(evt.SessionID) == "" {
				continue
			}
			if err := s.RunEvents().Publish(context.Background(), evt); err != nil {
				_ = c.WriteJSON(wsServerMsg{
					Op: "run_error", RequestID: m.RequestID, RunID: m.RunID, SessionID: m.SessionID,
					Message: "session_event_persistence_failed", Error: err.Error(),
				})
				return
			}
		}
		_ = c.WriteJSON(m)
	}
	writeConnected := func() {
		writeMsg(wsServerMsg{
			Op:      "connected",
			Message: "ok",
			Data: map[string]any{
				"connection_id":      connectionID,
				"canonical_protocol": "websocket",
				"protocol_version":   wsProtocolVersion,
			},
		})
	}
	writeSessionEventSnapshot := func(requestID, sessionID string, cursor int64) (ok, resyncRequired bool) {
		if s == nil {
			return false, false
		}
		sid := strings.TrimSpace(sessionID)
		if sid == "" {
			return false, false
		}
		owned, ownershipErr := s.sessionOwned(context.Background(), sid)
		if ownershipErr != nil || !owned {
			return false, false
		}
		runEventBindingMu.Lock()
		defer runEventBindingMu.Unlock()
		boundRunEventSession = sid
		runEventBinding.Add(1)
		if s.RunRT == nil {
			runEvtSub.Bind(sid)
			bound := wsServerMsg{Op: "session_bound", RequestID: requestID, SessionID: sid, Message: "subscribed"}
			if autoContinue, autoContinuePending := s.autoContinueSnapshot(sid); autoContinuePending {
				bound.Data = map[string]any{"auto_continue": autoContinue}
			}
			writeMsg(bound)
			return true, false
		}
		runEvtSub.BindBuffered(sid)
		highWater, err := s.RunRT.SessionEventHighWater(context.Background(), sid)
		if err != nil {
			runEvtSub.Unbind()
			return false, false
		}
		// A continuation waiting on a usage limit is live state, not history:
		// the page is told whether one is pending now, and the auto-continue
		// events it replays up to highWater only draw what already happened.
		// It is read after highWater, and the engine records a change before
		// it publishes the event, so the snapshot is at least as new as every
		// event the replay holds; anything later arrives live after it.
		autoContinue, autoContinuePending := s.autoContinueSnapshot(sid)
		writeMsg(wsServerMsg{
			Op:        "session_bound",
			RequestID: requestID,
			SessionID: sid,
			Message:   "subscribed",
			Data:      sessionBoundData(cursor, highWater, autoContinue, autoContinuePending),
		})
		next := cursor
		for next < highWater {
			page, pageErr := s.RunRT.ListSessionEvents(context.Background(), sid, next, highWater, 1000)
			if pageErr != nil {
				runEvtSub.Unbind()
				return false, false
			}
			for _, evt := range page.Events {
				writeMsg(runEventToWSMessage(requestID, loadCurrentTraceID(), turn.RunEventFromRecord(evt)))
			}
			if page.NextCursor <= next || !page.HasMore {
				next = highWater
				break
			}
			next = page.NextCursor
		}
		if !runEvtSub.ResumeAfter(highWater) {
			// The bounded snapshot-tail buffer overflowed while the durable
			// prefix was being written. Every omitted event remains in the
			// session log, but this connection must reconnect from its last
			// applied cursor before it can safely observe more live events.
			return false, true
		}
		return true, false
	}
	resumeRunEvents := func(requestID, runID string, cursor int64) (ok, resyncRequired bool) {
		if s == nil || s.RunRT == nil {
			return false, false
		}
		rid := strings.TrimSpace(runID)
		if rid == "" {
			return false, false
		}
		rn, err := s.RunRT.GetRun(context.Background(), rid)
		if err != nil || rn == nil {
			return false, false
		}
		if runEventCancel != nil {
			runEventCancel()
			runEventCancel = nil
		}
		wsSid = strings.TrimSpace(rn.SessionID)
		currentRunID = rid
		if ok, resyncRequired := writeSessionEventSnapshot(requestID, wsSid, cursor); !ok {
			return false, resyncRequired
		}
		return true, false
	}
	writeConnected()

	go func() {
		tk := time.NewTicker(20 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-doneCh:
				return
			case <-tk.C:
				writeMu.Lock()
				_ = c.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(5*time.Second))
				writeMu.Unlock()
			}
		}
	}()

	go func() {
		for evt := range notifCh {
			if b, err := json.Marshal(evt); err == nil {
				writeMsg(wsServerMsg{Op: "task_notification", Data: json.RawMessage(b)})
			}
		}
	}()

	// MCP status is its own outbound op, not a run event: it describes live
	// state, so it is written to the socket and never to the session's log. The
	// writer belongs to the socket, and the JSON encoding happens here rather
	// than inside the subscription callback, which runs under a registry's
	// delivery lock and must not do work that can block.
	go func() {
		for item := range mcpStatusCh {
			writeMsg(wsServerMsg{
				Op:        "mcp_status_event",
				SessionID: item.sessionID,
				Data:      mcpStatusEventData(item.snapshot),
			})
		}
	}()

	go func() {
		for {
			select {
			case <-doneCh:
				return
			case queued := <-runEvtOverflow:
				runEventBindingMu.Lock()
				if queued.generation != runEventBinding.Load() || queued.event.SessionID != boundRunEventSession {
					runEventBindingMu.Unlock()
					continue
				}
				writeMsg(wsServerMsg{Op: "run_warning", SessionID: boundRunEventSession, Message: "session_event_backpressure_resync_required"})
				writeMu.Lock()
				_ = c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "reconnect with cursor"), time.Now().Add(5*time.Second))
				writeMu.Unlock()
				runEventBindingMu.Unlock()
				return
			case queued := <-runEvtCh:
				if queued.flushed != nil {
					close(queued.flushed)
					continue
				}
				runEventBindingMu.Lock()
				if queued.generation != runEventBinding.Load() || queued.event.SessionID != boundRunEventSession {
					runEventBindingMu.Unlock()
					continue
				}
				// writeMsg stamps the connection's current trace id.
				writeMsg(runEventToWSMessage("", "", queued.event))
				runEventBindingMu.Unlock()
			}
		}
	}()

	inbox := newWSInbox()
	readErrors := make(chan error, 1)
	// clientGone closes when the client leaves. The loop below is busy for the
	// whole of a send, so it learns of a departure only by looking.
	clientGone := make(chan struct{})
	leave := func(err error) {
		close(clientGone)
		readErrors <- err
	}
	go func() {
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				leave(err)
				return
			}
			var message wsClientMsg
			if json.Unmarshal(data, &message) != nil {
				continue
			}
			message = normalizeWSClientMessage(message)
			if message.Op == "cancel_command" {
				// Answered here rather than by the loop, which is the one
				// busy with the message and cannot take another until it ends.
				commandMu.Lock()
				stop := commandCancel
				commandMu.Unlock()
				if stop != nil {
					stop()
				}
				continue
			}
			if message.Op == wsOpCancelAutoContinue {
				if validationErr := validateWSClientMessage(message); validationErr != nil {
					writeMsg(wsServerMsg{Op: wsOpAutoContinueCancelAck, RequestID: message.RequestID, SessionID: message.SessionID, Error: validationErr.Error()})
					continue
				}
				writeMsg(s.handleCancelAutoContinueMessage(r.Context(), message))
				continue
			}
			if message.Op == "approve_action" || message.Op == "deny_action" {
				if validationErr := validateWSClientMessage(message); validationErr != nil {
					writeMsg(wsServerMsg{Op: "run_error", RequestID: message.RequestID, Error: validationErr.Error()})
					continue
				}
				approvalErr := s.handleGatewayApprovalMessage(r.Context(), message)
				if approvalErr != nil {
					writeMsg(wsServerMsg{Op: "run_error", RequestID: message.RequestID, Error: approvalErr.Error()})
					continue
				}
				responseOp := "action_approved"
				if message.Op == "deny_action" || strings.EqualFold(strings.TrimSpace(message.ApprovalDecision), "decline") {
					responseOp = "action_denied"
				} else if strings.EqualFold(strings.TrimSpace(message.ApprovalDecision), "cancel") {
					responseOp = "action_cancelled"
				}
				writeMsg(wsServerMsg{Op: responseOp, RequestID: message.RequestID, Data: map[string]any{"action_id": message.ActionID}})
				continue
			}
			if !inbox.put(message) {
				leave(fmt.Errorf("client is %d messages ahead of the connection", wsInboxLimit))
				return
			}
		}
	}()

	for {
		m, ok := inbox.take()
		if !ok {
			select {
			case <-readErrors:
				if unsubWS != nil {
					unsubWS()
				}
				// notifCancel (deferred) stops the lossless queue's pump, which
				// closes notifCh and lets the forwarder goroutine exit.
				return
			case <-inbox.ready:
			}
			continue
		}
		op := m.Op
		if err := validateWSClientMessage(m); err != nil {
			if strings.TrimSpace(m.Type) == event.GatewayControlTypeRequest {
				writeMsg(wsServerMsg{
					Op:        event.GatewayControlTypeResponse,
					RequestID: m.RequestID,
					RunID:     m.RunID,
					SessionID: m.SessionID,
					Data:      event.NewGatewayControlErrorResponse(m.RequestID, err.Error()),
				})
				continue
			}
			writeMsg(wsServerMsg{
				Op:        "run_error",
				RequestID: m.RequestID,
				RunID:     m.RunID,
				SessionID: m.SessionID,
				Message:   "validation_error",
				Error:     err.Error(),
			})
			continue
		}
		controlRequest := op == "control_request"
		if controlRequest && strings.TrimSpace(m.Request.Subtype) == "interrupt" {
			op = "cancel_run"
		}
		if op == "ping" {
			writeMsg(wsServerMsg{
				Op:        "pong",
				RequestID: m.RequestID,
			})
			continue
		}
		if op == "connect" {
			writeConnected()
			continue
		}
		if op == "bind_session" {
			sid := strings.TrimSpace(m.SessionID)
			if sid == "" {
				sid = "default"
			}
			if ok, resyncRequired := writeSessionEventSnapshot(m.RequestID, sid, m.Cursor); !ok {
				message := "session_event_snapshot_unavailable"
				if resyncRequired {
					message = "session_event_backpressure_resync_required"
				}
				writeMsg(wsServerMsg{Op: "run_warning", RequestID: m.RequestID, SessionID: sid, Message: message})
				if resyncRequired {
					writeMu.Lock()
					_ = c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "reconnect with cursor"), time.Now().Add(5*time.Second))
					writeMu.Unlock()
					return
				}
				continue
			}
			// The MCP status follows the binding: the snapshot is sent before the
			// incremental subscription so a client that just bound sees the
			// startup already in progress, and a switch drops the old
			// subscription so it stops hearing about another session's servers.
			if sid != mcpBoundSession {
				pushMCPStatusSnapshot(sid)
				mcpBoundSession = sid
			}
			if sid != wsSid && s.Notifier != nil {
				if unsubWS != nil {
					unsubWS()
				}
				wsSid = sid
				unsubWS = s.Notifier.Subscribe(sid, func(evt event.TaskEvent) {
					// Lossless enqueue: the Notifier marks an event "delivered"
					// as soon as a subscriber handler exists and then calls it,
					// so a dropped send here loses the event permanently (it
					// never enters Notifier.pending for Drain to recover). Push
					// never blocks on the slow WS writer, so no notification is
					// lost when the client falls behind.
					notifQ.Push(evt)
				})
				for _, evt := range s.Notifier.Drain(sid) {
					notifQ.Push(evt)
				}
			}
			wsRequestCacheSet(requestCache, m.RequestID, wsServerMsg{
				Op:        "session_bound",
				RequestID: m.RequestID,
				SessionID: sid,
				Message:   "bound",
			}, 2*time.Minute, time.Now())
			continue
		}
		if op == "resume_connection" {
			if ok, resyncRequired := resumeRunEvents(m.RequestID, m.RunID, m.Cursor); ok {
				continue
			} else if resyncRequired {
				writeMsg(wsServerMsg{
					Op:        "run_warning",
					RequestID: m.RequestID,
					RunID:     m.RunID,
					SessionID: m.SessionID,
					Message:   "session_event_backpressure_resync_required",
				})
				writeMu.Lock()
				_ = c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "reconnect with cursor"), time.Now().Add(5*time.Second))
				writeMu.Unlock()
				return
			}
			writeMsg(wsServerMsg{
				Op:        "run_warning",
				RequestID: m.RequestID,
				RunID:     m.RunID,
				SessionID: m.SessionID,
				Message:   "resume_not_available",
			})
			continue
		}
		if op == "switch_mode" {
			sid := strings.TrimSpace(m.SessionID)
			if sid == "" {
				if wsSid != "" {
					sid = wsSid
				} else {
					sid = "default"
				}
			}
			mode := strings.ToLower(strings.TrimSpace(m.Mode))
			// Switch derives the phase and keeps the plan bookkeeping (pre-plan
			// mode, exited-plan flag) that a full-record Set would erase.
			next, _ := state.Switch(s.stateRoot(), sid, state.CoerceMode(mode))
			if clientPhase := strings.TrimSpace(m.Phase); clientPhase != "" && next.Mode == state.ModePlan && clientPhase != next.Phase {
				next.Phase = clientPhase
				_ = state.Set(s.stateRoot(), sid, next)
			}
			phase := next.Phase
			writeMsg(wsServerMsg{
				Op:        "mode_changed",
				RequestID: m.RequestID,
				SessionID: sid,
				Data: map[string]any{
					"mode":  string(next.Mode),
					"phase": phase,
				},
			})
			wsRequestCacheSet(requestCache, m.RequestID, wsServerMsg{
				Op:        "mode_changed",
				RequestID: m.RequestID,
				SessionID: sid,
				Data: map[string]any{
					"mode":  string(next.Mode),
					"phase": phase,
				},
			}, 2*time.Minute, time.Now())
			continue
		}
		if op == "query_mode" {
			sid := strings.TrimSpace(m.SessionID)
			if sid == "" {
				if wsSid != "" {
					sid = wsSid
				} else {
					sid = "default"
				}
			}
			st, err := state.Get(s.stateRoot(), sid)
			if err != nil {
				writeMsg(wsServerMsg{Op: "run_error", RequestID: m.RequestID, Error: err.Error()})
				continue
			}
			if st.Mode == "" {
				st.Mode = state.ModeAgent
			}
			writeMsg(wsServerMsg{
				Op:        "mode_state",
				RequestID: m.RequestID,
				SessionID: sid,
				Data: map[string]any{
					"mode":       string(st.Mode),
					"phase":      st.Phase,
					"updated_at": st.UpdatedAt,
				},
			})
			continue
		}
		if op == "submit_answer" && s.Actions != nil {
			id := strings.TrimSpace(m.ActionID)
			if id != "" {
				ans := state.AskAnswer{}
				if m.Answers != nil {
					b, _ := json.Marshal(map[string]any{"answers": m.Answers})
					_ = json.Unmarshal(b, &ans)
				}
				raw, _ := json.Marshal(ans)
				if err := s.resolveGatewayApproval(r.Context(), id, turn.ApprovalReply{AskAnswerJSON: string(raw)}); err == nil {
					writeMsg(wsServerMsg{Op: "ask_answered", RequestID: m.RequestID, Data: map[string]any{"action_id": id}})
				} else {
					writeMsg(wsServerMsg{Op: "run_error", RequestID: m.RequestID, Error: err.Error()})
				}
			}
			continue
		}
		if op == "cancel_run" {
			target := strings.TrimSpace(m.RunID)
			if target == "" {
				target = strings.TrimSpace(currentRunID)
			}
			invoked, parked := false, false
			if target != "" {
				invoked, parked = s.cancelRun(r.Context(), wsSid, target)
			}
			if !invoked {
				if rid := strings.TrimSpace(m.RunID); rid != "" && rid != strings.TrimSpace(currentRunID) {
					writeMsg(wsServerMsg{
						Op:        "run_error",
						RequestID: m.RequestID,
						SessionID: wsSid,
						Error:     "unknown run_id for cancel",
					})
				}
				continue
			}
			if parked {
				// A parked run's queue went back as it was finished; the
				// client hears that before it hears the run is over.
				flushRunEvents()
			}
			writeMsg(wsServerMsg{
				Op:        "run_cancelled",
				RequestID: m.RequestID,
				RunID:     target,
				SessionID: wsSid,
				Message:   "cancelled",
			})
			if controlRequest {
				writeMsg(wsServerMsg{
					Op:        event.GatewayControlTypeResponse,
					RequestID: m.RequestID,
					RunID:     target,
					SessionID: wsSid,
					Data: event.NewGatewayControlSuccessResponse(m.RequestID, map[string]any{
						"interrupted": true,
						"run_id":      target,
					}),
				})
			}
			continue
		}
		content := strings.TrimSpace(m.Message.Content)
		attachments := m.Message.Attachments
		mentionImages := m.Message.MentionImages
		if content == "" {
			content = strings.TrimSpace(m.Content)
		}
		if content == "" && len(attachments) == 0 && len(mentionImages) == 0 {
			continue
		}
		originalContent := content
		if rid := strings.TrimSpace(m.RequestID); rid != "" {
			if hit, ok := wsRequestCacheGet(requestCache, rid, time.Now()); ok {
				// This is an idempotent response replay, not a second lifecycle
				// transition. Writing it through writeMsg would mint and persist a
				// fresh canonical event ID for the same logical completion.
				if traceID := loadCurrentTraceID(); hit.TraceID == "" && traceID != "" {
					hit.TraceID = traceID
				}
				writeMu.Lock()
				_ = c.WriteJSON(hit)
				writeMu.Unlock()
				continue
			}
		}
		sid := m.SessionID
		if sid == "" {
			sid = session.NewForSurface("webchat", "")
		}
		// The client learns of the session the moment it is bound and asks for
		// everything session-scoped at once — its approvals, its roster, its
		// workspace — and a slash command may write into it before any turn
		// does (/plan records its mode change in the session's log). So the
		// session is established here, for a minted id and for one the client
		// named alike, and a session another primary agent owns is refused
		// before anything is done in it. A refused message never became a turn
		// in that session, so it is withdrawn back to the composer with the
		// reason — an op that is not recorded in the session's log, which is
		// not this agent's to write, or not there to write to.
		if s.Sessions != nil {
			if err := s.Sessions.Ensure(r.Context(), sid, sid); err != nil {
				reason := err.Error()
				if errors.Is(err, state.ErrSessionNotOwned) {
					reason = "Session belongs to another primary agent"
				}
				writeMsg(wsServerMsg{Op: "turn_withdrawn", RequestID: m.RequestID, SessionID: sid, Error: reason})
				continue
			}
		}
		// A slash command can publish events of its own while it runs — /compact
		// publishes its whole lifecycle — so the socket listens to the session
		// before the command starts, not after it has finished. A command that
		// switches sessions re-binds below.
		bindRunEvents(sid)
		commandCtx, stopCommand := context.WithCancel(context.Background())
		commandMu.Lock()
		commandCancel = stopCommand
		commandMu.Unlock()
		sc := parseSlashCommandWithOptions(s, sid, "webchat", content, turn.Context{
			DuringRun:      strings.TrimSpace(currentRunID) != "",
			RunID:          strings.TrimSpace(currentRunID),
			CommandContext: commandCtx,
		}, m.Message.Choice)
		commandMu.Lock()
		commandCancel = nil
		commandMu.Unlock()
		stopCommand()
		if sc.SessionSwitched && strings.TrimSpace(sc.SessionID) != "" {
			sid = strings.TrimSpace(sc.SessionID)
		}
		sessionBoundMsg := wsServerMsg{
			Op:        "session_bound",
			RequestID: m.RequestID,
			SessionID: sid,
		}
		if sc.SessionSwitched {
			sessionBoundMsg.Message = strings.TrimSpace(sc.Reply)
			sessionBoundMsg.Data = map[string]any{
				"session_switched": true,
			}
		}
		writeMsg(sessionBoundMsg)
		bindRunEvents(sid)
		if sid != wsSid && s.Notifier != nil {
			if unsubWS != nil {
				unsubWS()
			}
			wsSid = sid
			unsubWS = s.Notifier.Subscribe(sid, func(evt event.TaskEvent) {
				// Lossless enqueue - see comment at the first Subscribe site.
				notifQ.Push(evt)
			})
			for _, evt := range s.Notifier.Drain(sid) {
				notifQ.Push(evt)
			}
		}
		if sc.Handled && sc.SessionSwitched {
			wsRequestCacheSet(requestCache, m.RequestID, sessionBoundMsg, 2*time.Minute, time.Now())
			continue
		}
		if sc.ModeChanged {
			writeMsg(wsServerMsg{
				Op:        "mode_changed",
				RequestID: m.RequestID,
				SessionID: sid,
				Data: map[string]any{
					"mode":  sc.Mode,
					"phase": sc.Phase,
				},
			})
		}
		rawContentForRetrieval := ""
		// goalObjective is set by /goal so the post-success continuation loop runs
		// toward the objective on the webchat surface, same as the TUI path.
		goalObjective := strings.TrimSpace(sc.GoalObjective)
		skillName := strings.TrimSpace(sc.SkillName)
		skillPath := strings.TrimSpace(sc.SkillPath)
		if skillName == "" && skillPath == "" {
			// A client may name the skill for this turn directly (the workshop
			// does); a slash handoff above still takes precedence.
			if requestedName, requestedPath := strings.TrimSpace(m.Message.SkillName), strings.TrimSpace(m.Message.SkillPath); requestedName != "" || requestedPath != "" {
				resolvedName, resolvedPath, err := s.resolveExplicitSkillSelection(requestedName, requestedPath)
				if err != nil {
					writeMsg(wsServerMsg{Op: "run_error", RequestID: m.RequestID, SessionID: sid, Error: err.Error()})
					return
				}
				skillName, skillPath = resolvedName, resolvedPath
			}
		}
		if sc.ShouldContinueRun {
			rawContentForRetrieval = originalContent
			content = sc.ContinueInput
			if strings.TrimSpace(content) == "" {
				content = originalContent
			}
		} else if sc.Handled {
			// Slash replies are UI output only (R-model): never appended to the
			// transcript, never sent to the model, no run lifecycle. One dedicated
			// slash_reply message; the client renders it as a system notice card,
			// and a page refresh drops it with the session.
			flushRunEvents()
			reply := wsServerMsg{
				Op:        "slash_reply",
				RequestID: m.RequestID,
				SessionID: sid,
				Text:      sc.Reply,
			}
			if sc.Picker != nil {
				reply.Data = map[string]any{"picker": sc.Picker}
			}
			writeMsg(reply)
			continue
		}
		runStart := time.Now()
		currentTraceID.Store("trace-" + strconv.FormatInt(time.Now().UnixNano(), 10))
		// A client that stops while the conversation is being compacted for
		// its turn — before the turn has started — withdraws the turn, as Esc
		// does in the terminal: the compaction stops with it, its checkpoint
		// is never written, and the message is neither recorded nor answered.
		// The stop is the client's cancel_command, or the client leaving.
		preflightCtx, stopPreflight := context.WithCancel(r.Context())
		commandMu.Lock()
		commandCancel = stopPreflight
		commandMu.Unlock()
		go func() {
			select {
			case <-clientGone:
				stopPreflight()
			case <-preflightCtx.Done():
			}
		}()
		cerr := s.autoCompactBeforeUserAppend(preflightCtx, sid, "webchat", content, rawContentForRetrieval)
		withdrawn := preflightCtx.Err() != nil
		commandMu.Lock()
		commandCancel = nil
		commandMu.Unlock()
		stopPreflight()
		if cerr != nil {
			slog.Debug("gateway auto compact skipped", "session", sid, "err", cerr)
		}
		select {
		case <-clientGone:
			continue
		default:
		}
		if withdrawn {
			// The compaction's cancelled event reaches the client before the
			// reply that ends its send.
			flushRunEvents()
			writeMsg(wsServerMsg{Op: "turn_withdrawn", RequestID: m.RequestID, SessionID: sid})
			continue
		}
		if s.Sessions != nil {
			transcriptPath, terr := hook.WriteSessionTranscriptArtifact(s.stateRoot(), s.Sessions, sid)
			if terr != nil {
				writeMsg(wsServerMsg{
					Op:        "run_completed",
					RequestID: m.RequestID,
					SessionID: sid,
					Message:   terr.Error(),
				})
				continue
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
			hookOut, herr := rt.ExecuteUserPromptSubmit(r.Context(), hook.UserPromptSubmitInput{
				BaseInput: hook.BaseInput{
					HookEventName:  hook.EventUserPromptSubmit,
					SessionID:      sid,
					TranscriptPath: transcriptPath,
					Cwd:            s.Home,
					PermissionMode: "on-request",
				},
				Prompt: content,
			})
			if herr != nil {
				writeMsg(wsServerMsg{
					Op:        "run_completed",
					RequestID: m.RequestID,
					SessionID: sid,
					Message:   herr.Error(),
				})
				continue
			}
			content = hook.AppendAdditionalContext(content, hookOut.AdditionalContext)
			if hookOut.Blocked {
				msg := strings.TrimSpace(hookOut.StopReason)
				if msg == "" {
					msg = strings.TrimSpace(hookOut.SystemMessage)
				}
				if msg == "" {
					msg = "Operation stopped by hook"
				}
				writeMsg(wsServerMsg{
					Op:        "run_completed",
					RequestID: m.RequestID,
					SessionID: sid,
					Message:   msg,
				})
				continue
			}
		}
		// The session was established (or refused) when the message arrived; a
		// slash command that switched it created or checked the one it switched
		// to. The user row is written further down, once the run it belongs to
		// exists.
		// What the message attaches is made available before its turn exists:
		// a file that cannot be never leaves the turn half-sent. The message
		// goes back to the composer whole, with the error that stopped it.
		turnInput, attachErr := s.prepareWebTurnInput(r.Context(), content, attachments, mentionImages)
		if attachErr != nil {
			writeMsg(wsServerMsg{Op: "turn_withdrawn", RequestID: m.RequestID, SessionID: sid, Error: attachErr.Error()})
			continue
		}
		// With a run store the turn is its run row, and a run that could not be
		// recorded is a turn that did not start: an invented id would name a
		// run the store does not hold, and every event and transcript row
		// written under it would be refused, silently. The message goes back to
		// the composer whole, with the store's error. Without a store the turn
		// is tracked in memory only, under an id of its own.
		runID := "ws-" + strconv.FormatInt(time.Now().UnixNano(), 10)
		if s.RunRT != nil {
			rr, err := s.RunRT.CreateRun(r.Context(), sid, content)
			if err != nil {
				writeMsg(wsServerMsg{Op: "turn_withdrawn", RequestID: m.RequestID, SessionID: sid, Error: err.Error()})
				continue
			}
			runID = rr.ID
		}
		s.runStartedAt.Store(runID, runStart)
		currentRunID = runID
		runCtx, cancelCause := context.WithCancelCause(context.Background())
		cancelFn := context.CancelFunc(func() { cancelCause(context.Canceled) })
		s.runController().Track(runID, sid, cancelFn)
		turnInputRT := (*run.TurnInputRuntime)(nil)
		if q, _, ok := s.runController().Queue(runID); ok {
			q.SetChangeHook(func() {
				s.appendPendingInputUpdated(context.Background(), runID, sid, toPendingInputPreview(q.Preview()))
			})
			turnInputRT = q.Runtime()
		}
		writeMsg(wsServerMsg{
			Op:        "run_started",
			RequestID: m.RequestID,
			RunID:     runID,
			SessionID: sid,
		})
		if msg, data, ok := s.runtimeDangerWarning(); ok {
			writeMsg(wsServerMsg{
				Op:        "run_warning",
				RequestID: m.RequestID,
				RunID:     runID,
				SessionID: sid,
				Message:   msg,
				Data:      data,
			})
		}
		modeState, _ := state.Get(s.stateRoot(), sid)
		if modeState.Mode == "" {
			modeState.Mode = state.ModeAgent
		}
		writeMsg(wsServerMsg{
			Op:        "mode_changed",
			RequestID: m.RequestID,
			RunID:     runID,
			SessionID: sid,
			Data: map[string]any{
				"mode":  string(modeState.Mode),
				"phase": modeState.Phase,
			},
		})
		wsRequestCacheSet(requestCache, m.RequestID, wsServerMsg{
			Op:        "run_started",
			RequestID: m.RequestID,
			RunID:     runID,
			SessionID: sid,
		}, 2*time.Minute, time.Now())
		input := turnInput.text
		// Same rule as the terminal: the row's content is display-only and
		// carries what the user typed, while PartsJSON carries what the model
		// is sent. The model is sent exactly what the row replays as, so this
		// request and every later one carry the same message.
		inputParts := run.UserInputParts(r.Context(), s.runnerFor(r.Context(), sid).FileResolver, turnInput.partsJSON)
		if s.Sessions != nil {
			turn.PersistUserTurn(r.Context(), s.Sessions, turn.UserTurn{
				SessionID:  sid,
				RunID:      runID,
				ModelInput: input,
				RawInput:   firstNonBlank(rawContentForRetrieval, turnInput.display),
				PartsJSON:  turnInput.partsJSON,
			})
		}
		hc := hook.HookContext{SessionID: sid, Channel: "webchat", Trigger: "user", RawInput: rawContentForRetrieval}
		agCtx := llm.WithAgentSessionID(runCtx, sid)
		agCtx = tool.WithConversationSessionID(agCtx, sid)
		agCtx = tool.WithRunID(agCtx, runID)
		agCtx = run.WithExplicitSkillSelection(agCtx, skillName, skillPath)
		// Inject PartialSessionCapture so a cancelled webchat run can persist
		// already-completed tool calls/results to the transcript DB.
		gatewayPartialCapture := run.NewPartialSessionCapture()
		agCtx = run.WithPartialSessionCapture(agCtx, gatewayPartialCapture)
		var streamed bool
		streamPartial := &turn.StreamPartial{}
		agCtx = s.withAnswerStream(agCtx, runID, sid, &streamed, streamPartial)
		if turnInputRT != nil {
			agCtx = run.WithTurnInputRuntime(agCtx, turnInputRT)
		}
		if tools := s.Env.Tools(); tools != nil {
			// One wiring for mode + allowed plan path, shared with every other
			// turn. Re-deriving it here is how this path came to allow writes to
			// <stateRoot>/plans while the plan-mode reminder pointed the model at
			// <stateRoot>/plans/<project>, so every plan write was rejected.
			agCtx = process.AgentContextForProject(agCtx, s.stateRoot(), sid, s.projectKey())
		}
		if s.RunRT != nil && s.Runner != nil {
			if err := s.Runner.Load(); err == nil {
				if tools := s.Env.Tools(); tools != nil {
					agCtx = tool.WithNetworkApprovalPromptHook(agCtx, func(approvalCtx context.Context, actionID string, payload map[string]any, request safety.NetworkApprovalRequest) (safety.NetworkApprovalDecision, error) {
						return s.promptGatewayNetworkApproval(approvalCtx, actionID, payload, request, m.RequestID, runID, sid, writeMsg)
					})
					// A subagent this turn dispatches gets its approvals put to
					// this websocket's user the same way, so a fanout child's
					// gate pauses that child instead of failing its task.
					agCtx = tool.WithSubagentApprovalHook(agCtx, func(approvalCtx context.Context, rae *tool.RequiresActionError) (context.Context, error) {
						return s.promptGatewaySubagentApproval(approvalCtx, rae, m.RequestID, sid, writeMsg)
					})
					agCtx = tool.WithStepHook(agCtx, func(stepCtx context.Context, evt tool.StepEvent) {
						rid := tool.RunIDFromContext(stepCtx)
						if rid == "" {
							return
						}
						if strings.TrimSpace(tool.HookAgentIDFromContext(stepCtx)) != "" {
							if canonical, ok := tool.RunEventFromStep(stepCtx, sid, rid, "webchat", evt); ok {
								_ = s.RunEvents().Publish(stepCtx, canonical)
								return
							}
						}
						// An explicit skill step has no transcript tool row, so the
						// ordinary step message below would be lost on reload.
						// Publishing the canonical event persists it to the session
						// event log and delivers the same card live to every
						// subscriber of this session.
						if strings.EqualFold(strings.TrimSpace(evt.Category), "skill") {
							if canonical, ok := tool.RunEventFromStep(stepCtx, sid, rid, "webchat", evt); ok {
								_ = s.RunEvents().Publish(stepCtx, canonical)
								return
							}
						}
						stepID := evt.StepID
						if stepID == "" {
							stepID = fmt.Sprintf("%s:%d", evt.ToolName, time.Now().UnixNano())
						}
						if evt.Kind == event.RunEventPlanUpdated && evt.PlanUpdate != nil {
							writeMsg(runEventToWSMessage(
								m.RequestID,
								loadCurrentTraceID(),
								event.NewRunEvent(
									"evt-"+strconv.FormatInt(time.Now().UnixNano(), 10),
									rid,
									sid,
									event.RunEventPlanUpdated,
									evt.PlanUpdate,
									time.Now().UTC(),
								),
							))
							return
						}
						if !tool.ToolStepUserVisible(evt) {
							return
						}
						if evt.Kind == event.RunEventToolStarted {
							if tool.ToolStepRendersAsPlan(evt) {
								return
							}
							meta := tool.EnrichToolMeta(stepCtx, tool.BuildToolMeta(evt))
							writeMsg(wsServerMsg{
								Op:        "step",
								RequestID: m.RequestID,
								RunID:     rid,
								SessionID: sid,
								Data: withStepSummary(
									gatewayToolStartedStepData(stepID, fmt.Sprintf("tool %s", evt.ToolName), evt.ToolName, evt.Input, meta),
									tool.SummarizeToolStep(evt),
								),
							})
							return
						}
						if evt.Kind == event.RunEventToolCompleted {
							if tool.ToolStepRendersAsPlan(evt) {
								return
							}
							if s.NotificationHook != nil {
								if in, ok := tool.BuildToolStepHookInput(sid, rid, "webchat", evt); ok {
									_ = s.NotificationHook.Notify(stepCtx, in)
								}
							}
							rendered, _ := tool.BuildRenderedToolStepNotification(sid, rid, "webchat", evt)
							rendered.ToolMeta = tool.EnrichToolMeta(stepCtx, tool.BuildToolMeta(evt))
							writeMsg(wsServerMsg{
								Op:        "step",
								RequestID: m.RequestID,
								RunID:     rid,
								SessionID: sid,
								Data: withStepSummary(
									gatewayToolCompletedStepData(
										stepID,
										fmt.Sprintf("tool %s", evt.ToolName),
										evt.ToolName,
										evt.Output,
										evt.Error,
										rendered.DisplayBody,
										evt.ActionID,
										evt.ActionKind,
										rendered.ToolMeta,
									),
									tool.SummarizeToolStep(evt),
								),
							})
							return
						}
					})
				}
			}
		}
		var res *agent.Result
		var agentErr error
		if s.RunRT != nil {
			// P5-12: the websocket turn goes through the canonical turn path.
			// The run already exists (this handler created and tracked it), so
			// ExistingRunID names it and AgentContextIsRunContext tells the
			// executor to use agCtx as-is rather than wrapping it -- wrapping
			// would register a second cancel and orphan the one the controller
			// tracks. Supplying ExistingRunID also leaves the finished
			// transition to this handler, which finalises its queued input
			// first.
			outcome, err := s.Core.Submit(agCtx, turn.TurnRequest{
				SessionID:                sid,
				Origin:                   turn.Origin{Surface: turn.SurfaceWebChat, ChannelID: "webchat"},
				UserText:                 input,
				RawInput:                 rawContentForRetrieval,
				Parts:                    inputParts,
				GoalObjective:            goalObjective,
				SkillName:                skillName,
				SkillPath:                skillPath,
				ExistingRunID:            runID,
				AgentContextIsRunContext: true,
			}, nil)
			res = outcome.Result
			agentErr = err
			if outcome.Status == turn.TurnWaitingApproval && outcome.Resume != nil {
				agentErr = &tool.RequiresActionError{
					RunID:           outcome.RunID,
					ActionID:        outcome.Resume.ActionID,
					ToolName:        outcome.Resume.ToolName,
					SessionSnapshot: outcome.Resume.SessionSnapshot,
				}
			}
		} else {
			res, _, agentErr = run.RunPipeline(agCtx, s.runnerFor(agCtx, sid), s.Hooks, hc, input, inputParts)
		}
		runFinishedAt := time.Now()
		runElapsed := runFinishedAt.Sub(runStart)
		if runElapsed < 0 {
			runElapsed = 0
		}
		if agentErr != nil {
			var rae *tool.RequiresActionError
			if errors.As(agentErr, &rae) {
				stepID := fmt.Sprintf("tool:%s", rae.ToolName)
				desc := fmt.Sprintf("tool %s requires approval", rae.ToolName)
				meta := tool.BuildToolMeta(tool.StepEvent{
					Kind:       tool.StepKindToolCompleted,
					ToolName:   rae.ToolName,
					Input:      anyMap(rae.ToolInput),
					Output:     map[string]any{"requires_action": true},
					ActionID:   rae.ActionID,
					ActionKind: rae.ActionKind,
				})
				approvalData := approvalWSData(s.sessionPermissions(r.Context(), sid), sid, rae.ActionID, rae.ActionKind, rae.ToolName, rae.ToolInput)
				writeMsg(wsServerMsg{
					Op:        "step",
					RequestID: m.RequestID,
					RunID:     runID,
					SessionID: sid,
					Data: gatewayToolCompletedStepData(
						stepID,
						desc,
						rae.ToolName,
						map[string]any{
							"requires_action": map[string]any{
								"id":   rae.ActionID,
								"kind": rae.ActionKind,
							},
						},
						rae.Error(),
						"",
						rae.ActionID,
						rae.ActionKind,
						meta,
					),
				})
				if s.RunRT != nil {
					if s.Sessions != nil && len(rae.SessionSnapshot) > 0 {
						_ = s.Sessions.AppendMessageSequenceForRun(
							agCtx,
							sid,
							runID,
							rae.SessionSnapshot,
							"",
							"",
						)
					}
					inb, _ := json.Marshal(rae.ToolInput)
					_ = s.RunRT.SetWaitingAction(agCtx, runID, state.Wait{
						RunID:           runID,
						ActionID:        rae.ActionID,
						ToolName:        rae.ToolName,
						ToolInputJSON:   string(inb),
						SessionSnapshot: append([]llm.Message(nil), rae.SessionSnapshot...),
						AgentID:         rae.AgentID,
						SubagentType:    rae.SubagentType,
						// rae.RunID is the subagent's own run when this gate
						// was raised inside one (run.Run tags it before the
						// error ever reaches here); persisting it is what lets
						// a resume reconstructed after a restart find the
						// subagent's WorkerSessionID again instead of
						// silently continuing under the top-level session.
						SubagentRunID: strings.TrimSpace(rae.RunID),
					})
				}
				// From here the run is parked: whatever it does next, after the
				// decision, reaches the client only as events. Everything this
				// handler mirrored so far reaches it first, so the client can
				// stop setting the mirror aside once it hears the run parked.
				flushRunEvents()
				writeMsg(wsServerMsg{
					Op:        "requires_action",
					RequestID: m.RequestID,
					RunID:     runID,
					SessionID: sid,
					Data:      approvalData,
					Message:   "Awaiting approval or authorization to continue",
				})
				s.runController().WaitApproval(runID)
				continue
			}
			if errors.Is(agentErr, context.Canceled) {
				// Persist already-completed tool calls/results so the
				// transcript DB stays consistent with what the webchat user
				// saw during the run. Without this, the next round and
				// /resume replay lose all messages from the cancelled turn.
				s.persistCancelledGatewayTurn(sid, runID, turn.RunEnd{StartedAt: runStart, FinishedAt: runFinishedAt, Worked: runElapsed}, gatewayPartialCapture, streamPartial)
				if s.RunRT != nil {
					_ = s.RunRT.SetStatus(agCtx, runID, state.RunStatusCancelled)
					_ = s.RunRT.CancelRunningDescendants(agCtx, runID)
				}
				s.finishRun(context.Background(), sid, runID)
				flushRunEvents()
				writeMsg(wsServerMsg{
					Op:        "run_cancelled",
					RequestID: m.RequestID,
					RunID:     runID,
					SessionID: sid,
					Message:   "cancelled",
				})
				continue
			}
			// Persist already-completed tool calls/results so the transcript
			// DB stays consistent with what the webchat user saw during the
			// run. Without this, a transient LLM error (429, network, etc.)
			// drops every message the assistant produced in this turn.
			s.persistCancelledGatewayTurn(sid, runID, turn.RunEnd{StartedAt: runStart, FinishedAt: runFinishedAt, Worked: runElapsed}, gatewayPartialCapture, streamPartial)
			// An explicit skill load that failed already delivered its Skill
			// failure card through the step hook; a run_error bubble would repeat
			// the same news twice.
			var skillLoadErr *run.ExplicitSkillLoadError
			if errors.As(agentErr, &skillLoadErr) {
				slog.Error("gateway skill load failed", "run_id", runID, "session_id", sid, "skill", skillLoadErr.SkillName, "err", agentErr)
				if s.RunRT != nil {
					_ = s.RunRT.SetStatus(agCtx, runID, state.RunStatusFailed)
					_ = s.RunRT.FailRunningDescendants(agCtx, runID)
				}
				s.finishRun(context.Background(), sid, runID)
				continue
			}
			// The webchat shows what the gateway sends, live and on replay, so
			// the provider failure is described here rather than dumped as the
			// transport error it arrived as. The raw error stays in the log.
			slog.Error("gateway turn failed", "run_id", runID, "session_id", sid, "err", agentErr)
			agentErrText := llm.ExplainError(agentErr)
			agentErrDetail := newTurnErrorDetail(agentErr)
			// Data is an interface: assigning a nil *TurnErrorDetail to it would
			// still serialize a "data": null the clients have to step over.
			var agentErrData any
			if agentErrDetail != nil {
				agentErrData = agentErrDetail
			}
			s.finishRun(context.Background(), sid, runID)
			flushRunEvents()
			writeMsg(wsServerMsg{
				Op:        "run_error",
				RequestID: m.RequestID,
				RunID:     runID,
				SessionID: sid,
				Error:     agentErrText,
				Data:      agentErrData,
			})
			wsRequestCacheSet(requestCache, m.RequestID, wsServerMsg{
				Op:        "run_error",
				RequestID: m.RequestID,
				RunID:     runID,
				SessionID: sid,
				Error:     agentErrText,
				Data:      agentErrData,
			}, 2*time.Minute, time.Now())
			if s.RunRT != nil {
				_ = s.RunRT.SetStatus(agCtx, runID, state.RunStatusFailed)
				_ = s.RunRT.FailRunningDescendants(agCtx, runID)
			}
			continue
		}
		out := ""
		if res != nil {
			out = res.TextContent()
		}
		if lc := s.liveCfg(); lc != nil {
			out = safety.SanitizeOutbound(lc, out)
		}
		s.finishSuccessfulTurn(r.Context(), gatewayPostTurnOptions{
			SessionID:       sid,
			ChannelID:       "webchat",
			RunID:           runID,
			AssistantText:   out,
			RunStartedAt:    runStart,
			RunFinishedAt:   runFinishedAt,
			WorkedMs:        runElapsed.Milliseconds(),
			AssistantResult: res,
			AppendAssistant: true,
		})
		if budgetMsg, ok := s.tokenBudgetWSMessageFromSession(r.Context(), m.RequestID, runID, sid); ok {
			writeMsg(budgetMsg)
		}
		if out != "" && !streamed {
			// An answer that was not streamed — the provider sent it whole, or
			// the output guardrail had to see it whole — reaches the page once,
			// as the event its stream would have been.
			_ = s.RunEvents().Publish(context.Background(), event.NewRunEvent("", runID, sid, event.RunEventAssistantDelta,
				event.AssistantDeltaPayload{Text: out, FirstDeltaMS: time.Since(runStart).Milliseconds()}, time.Now()))
		}
		msg := wsServerMsg{
			Op:        "run_completed",
			RequestID: m.RequestID,
			RunID:     runID,
			SessionID: sid,
			Text:      out,
			Data: map[string]any{
				"elapsed_ms": runElapsed.Milliseconds(),
			},
		}
		// What the run never took goes back before anything reports its end.
		s.finishRun(context.Background(), sid, runID)
		if s.RunRT != nil {
			if events, err := s.RunRT.ListRunEventsOfTypes(agCtx, runID, event.RunEventToolCompleted); err == nil && requiresLintFollowup(events) {
				meta := tool.ToolMeta{
					ToolName:   "lint_feedback",
					Status:     "completed",
					Purpose:    "Remind the user to run verification after code-edit tools.",
					Invocation: "lint_feedback",
				}
				writeMsg(wsServerMsg{
					Op:        "step",
					RequestID: m.RequestID,
					RunID:     runID,
					SessionID: sid,
					Data: gatewayToolCompletedStepData(
						"lint_feedback",
						"post-edit lint feedback",
						"lint_feedback",
						map[string]any{
							"status": "pending_verification",
							"hint":   "run lint or build checks after code-edit tools",
						},
						"",
						"",
						"",
						"",
						meta,
					),
				})
			}
		}
		flushRunEvents()
		writeMsg(msg)
		wsRequestCacheSet(requestCache, m.RequestID, msg, 2*time.Minute, time.Now())
	}
}

// requiresLintFollowup reports whether the run edited or wrote files, which is
// exactly when the surface reminds the user to verify. It reads the run's tool
// completion events, the same record the audit panel is drawn from.
func requiresLintFollowup(events []state.SessionEvent) bool {
	for _, evt := range events {
		var p event.ToolCallCompletedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			continue
		}
		switch strings.TrimSpace(p.ToolName) {
		case "edit_file", "write_file":
			return true
		}
	}
	return false
}

// slashCommandResult is a slash command's outcome as the engine reports it.
type slashCommandResult = turn.Result

func parseSlashCommand(s *Server, sessionID, channel, content string) slashCommandResult {
	return parseSlashCommandWithOptions(s, sessionID, channel, content, turn.Context{}, nil)
}

// parseSlashCommandWithOptions runs a slash command — or, when choice is set,
// applies a choice made in the picker a command offered — through the engine.
func parseSlashCommandWithOptions(s *Server, sessionID, channel, content string, opts turn.Context, choice *turn.SlashChoice) slashCommandResult {
	if s == nil {
		return slashCommandResult{}
	}
	channel = strings.TrimSpace(channel)
	execCtx := turn.Context{
		Home:             s.Home,
		StateRoot:        s.stateRoot(),
		SessionID:        sessionID,
		Channel:          channel,
		SessionSource:    memory.SessionSourceWebchat,
		RunID:            opts.RunID,
		DuringRun:        opts.DuringRun,
		SideConversation: opts.SideConversation,
		CommandContext:   opts.CommandContext,
		Sessions:         s.Sessions,
		Compact:          s,
		Clear:            s,
		ContextDebug:     s,
		Status:           s,
		Permissions:      s,
		MCP:              s,
		Sandbox:          s,
		Diff:             s,
		Model:            s,
		Agent:            s,
		Memories:         s,
	}
	surface, err := slashExecutionSurfaceForChannel(channel)
	if err != nil {
		if !isChannelAdapterSlashChannel(channel) {
			return slashCommandResult{Handled: true, Reply: err.Error()}
		}
		return turn.ExecuteDynamicOnly(execCtx, content)
	}
	execCtx.Surface = surface
	if choice != nil {
		return turn.Choose(execCtx, *choice)
	}
	if s.Core != nil {
		return s.Core.ExecuteSlashCommand(execCtx, content)
	}
	return turn.ExecuteSlashCommand(execCtx, content)
}

func isChannelAdapterSlashChannel(channel string) bool {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "telegram",
		"discord",
		"slack",
		"whatsapp",
		"signal",
		"mattermost",
		"matrix",
		"homeassistant",
		"email",
		"sms",
		"webhook",
		"bluebubbles",
		"wecom",
		"wecom_callback",
		"weixin",
		"feishu",
		"dingtalk",
		"qq",
		"httpbridge",
		"wsbridge":
		return true
	default:
		return false
	}
}

func slashExecutionSurfaceForChannel(channel string) (turn.Surface, error) {
	channel = strings.TrimSpace(channel)
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "webchat":
		return turn.SurfaceWebChat, nil
	case "tui":
		return turn.SurfaceTUI, nil
	default:
		return "", fmt.Errorf("unsupported slash channel %q", channel)
	}
}

func slashDiscoverySurfaceForChannel(channel string) (turn.Surface, error) {
	channel = strings.TrimSpace(channel)
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "webchat":
		return turn.SurfaceWebChat, nil
	case "tui":
		return turn.SurfaceTUI, nil
	default:
		return "", fmt.Errorf("slash discovery is not available for channel %q", channel)
	}
}
