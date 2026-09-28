package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/channel"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/stretchr/testify/require"
)

// twoAgentChannelServer builds a gateway whose two primary agents each own a
// webhook channel on their own inbound path.
func twoAgentChannelServer(t *testing.T, home string) *Server {
	t.Helper()
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main": {Channels: appcfg.ChannelsSection{
			Webhook: appcfg.Webhook{Enabled: true, InboundPath: "/main/hook"},
		}},
		"acme": {Primary: true, Channels: appcfg.ChannelsSection{
			Webhook: appcfg.Webhook{Enabled: true, InboundPath: "/acme/hook"},
		}},
	}
	require.NoError(t, os.MkdirAll(filepath.Join(home, "state"), 0o755))
	return &Server{
		Home:     home,
		Channels: channel.NewRegistry(),
		Env:      &process.Environment{Deps: run.Deps{AppCfg: cfg}},
	}
}

func summaryFor(home, id string) appcfg.Summary {
	root := filepath.Join(home, "workspace")
	if id != "main" {
		root = filepath.Join(home, "workspaces", id)
	}
	return appcfg.Summary{ID: id, WorkspaceRoot: root}
}

// Switching the active primary agent has to move the channels with it. A bot
// or inbound endpoint that outlived the switch would keep delivering its
// owner's messages into the agent that just became active.
func TestPrimaryAgentSwitchRebindsChannelsToTheNewAgent(t *testing.T) {
	home := t.TempDir()
	s := twoAgentChannelServer(t, home)

	s.bindChannelsTo(context.Background(), summaryFor(home, "main"))
	require.Equal(t, "main", s.Channels.AgentID())
	_, mounted := s.Channels.Route(http.MethodPost, "/main/hook")
	require.True(t, mounted, "main's inbound path should be mounted")
	_, mounted = s.Channels.Route(http.MethodPost, "/acme/hook")
	require.False(t, mounted, "another agent's inbound path must not be mounted")

	require.NoError(t, s.applyPrimaryAgent(summaryFor(home, "acme")))

	require.Equal(t, "acme", s.Channels.AgentID())
	_, mounted = s.Channels.Route(http.MethodPost, "/acme/hook")
	require.True(t, mounted, "acme's inbound path should be mounted after the switch")
	_, mounted = s.Channels.Route(http.MethodPost, "/main/hook")
	require.False(t, mounted, "the previous agent's inbound path survived the switch")
}

// Requests to a mounted channel path are served by the registry rather than by
// the static router, which cannot withdraw a route when the agent changes.
func TestChannelRoutesDispatchToTheBoundAgentsChannels(t *testing.T) {
	home := t.TempDir()
	s := twoAgentChannelServer(t, home)
	s.bindChannelsTo(context.Background(), summaryFor(home, "main"))

	fellThrough := false
	handler := s.channelHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fellThrough = true
		w.WriteHeader(http.StatusTeapot)
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/main/hook", nil))
	require.False(t, fellThrough, "a mounted channel path must not reach the static router")

	// A path belonging to another agent is not mounted, so it falls through
	// and 404s like any unknown route.
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/acme/hook", nil))
	require.True(t, fellThrough)
	require.Equal(t, http.StatusTeapot, rr.Code)
}

// Control-plane auth exemption follows the same agent: an inbound path is
// exempt only while the agent that owns it is active.
func TestControlPlaneExemptionFollowsTheActiveAgentsChannels(t *testing.T) {
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main": {Channels: appcfg.ChannelsSection{
			Webhook: appcfg.Webhook{Enabled: true, InboundPath: "/main/hook"},
		}},
		"acme": {Primary: true},
	}
	require.True(t, appcfg.GatewayControlPlaneAuthExemptPath("/main/hook", cfg, "main"))
	require.False(t, appcfg.GatewayControlPlaneAuthExemptPath("/main/hook", cfg, "acme"))
}
