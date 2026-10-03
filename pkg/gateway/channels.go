// Channel binding, route mounting, and the inbound turn path.
package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// bindChannelsTo switches the running channel set to the given primary agent.
// P8-6 leaves the gateway only the optional mounting of channel routes, so the
// build-and-bind operation itself lives on the registry (Registry.BindAgent);
// all that is left here is naming the agent and reporting a failure, because
// one misconfigured bot token must not take the gateway down.
func (s *Server) bindChannelsTo(ctx context.Context, active config.Summary) {
	if s == nil || s.Channels == nil {
		return
	}
	if err := s.Channels.BindAgent(ctx, s.liveCfg(), active.ID, active.WorkspaceRoot, s.bus()); err != nil {
		slog.Error("channel bind", "agent", active.ID, "err", err)
	}
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
func (s *Server) submitChannelTurn(ctx context.Context, channelID, sessionID, input, rawInput string, skillName, skillPath string) (turn.TurnOutcome, bool) {
	if s == nil || s.Core == nil {
		return turn.TurnOutcome{}, false
	}
	// The user's message is the turn's first row, stored as the turn starts the
	// way every other surface stores it: a turn that fails or parks on an
	// approval still has it, and it belongs to the run once the run exists.
	// The run folds a stored trailing message into its own input, so the model
	// is sent exactly what it was sent before.
	var userRowID int64
	if s.Sessions != nil {
		userRowID, _ = turn.PersistUserTurn(ctx, s.Sessions, turn.UserTurn{
			SessionID: sessionID, ModelInput: input, RawInput: rawInput, EnsureSession: true,
		})
	}
	out, err := s.Core.Submit(ctx, turn.TurnRequest{
		SessionID: sessionID,
		Origin:    turn.Origin{Surface: turn.SurfaceChannel, ChannelID: channelID},
		UserText:  input,
		RawInput:  rawInput,
		SkillName: skillName,
		SkillPath: skillPath,
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

// channelApprovalNotice describes a pending approval in one line a channel
// user can act on, without exposing action IDs or internal error text.
func channelApprovalNotice(out turn.TurnOutcome) string {
	name := ""
	if out.Approval != nil {
		name = strings.TrimSpace(out.Approval.PermissionToolName)
	}
	if name == "" {
		name = "a tool"
	}
	return "Waiting for approval to run " + name + ". Approve it in the Forebrain Harness app to continue."
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
