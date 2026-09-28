// Channel binding, route mounting, and the inbound turn path.
package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/config"
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
	out, err := s.Core.Submit(ctx, turn.TurnRequest{
		SessionID: sessionID,
		Origin:    turn.Origin{Surface: turn.SurfaceChannel, ChannelID: channelID},
		UserText:  input,
		RawInput:  rawInput,
		SkillName: skillName,
		SkillPath: skillPath,
	}, nil)
	switch {
	case err != nil:
		slog.Error("inbound run", "channel", channelID, "session", sessionID, "err", err)
		channelBus{s: s}.deliverOutbound(context.Background(), channelID, sessionID, "Request failed: "+err.Error())
		return out, false
	case out.Status == turn.TurnWaitingApproval:
		// The run stays paused and the resume path owns it. Tell the user what
		// is being asked rather than leaking the gate's error text.
		channelBus{s: s}.deliverOutbound(context.Background(), channelID, sessionID, channelApprovalNotice(out))
		return out, false
	case out.Result == nil:
		return out, false
	}
	return out, true
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
