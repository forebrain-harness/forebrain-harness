// Channel contracts, the per-agent registry, binding, and outbound delivery.
package channel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

type Inbound struct {
	ChannelID string
	Text      string
	SessionID string
	Raw       any
}

type Outbound struct {
	ChannelID string
	SessionID string
	Text      string
}

type RouteAdder func(method, path string, hf http.HandlerFunc)

type Handler interface {
	ID() string
	Start(ctx context.Context, add RouteAdder, bus Bus) error
	Stop(ctx context.Context) error
}

type Channel = Handler

type Bus interface {
	PublishInbound(ctx context.Context, m Inbound) error
}

// Registry holds the channels of exactly one primary agent: the handlers that
// are running and the HTTP routes they mounted.
//
// Channels belong to a tenant, so "which channels exist" is only ever answered
// relative to an agent. Bind is the single way to populate a Registry, and it
// replaces the previous agent's channels wholesale — stopping its handlers and
// dropping its routes — so an inbound message can never reach an agent other
// than the one whose bot received it. The route table lives here rather than on
// the HTTP router because router registrations cannot be withdrawn, which would
// leave a switched-away agent's endpoints answering.
type Registry struct {
	// bindMu serializes whole Bind/Stop operations. It is separate from mu
	// because a bind starts handlers, which touches the network: holding the
	// data lock across that would stall Route on every inbound request. See
	// Bind for why the two operations have to be serialized at all.
	bindMu   sync.Mutex
	mu       sync.RWMutex
	agentID  string
	handlers []Handler
	routes   map[string]http.HandlerFunc
}

func NewRegistry() *Registry {
	return &Registry{routes: map[string]http.HandlerFunc{}}
}

// Bind stops whatever is currently running and starts handlers as agentID's
// channels. A handler that fails to start is left out of the routing table and
// reported; the remaining channels still come up, because one misconfigured bot
// token must not take the whole gateway down with it.
//
// The whole operation is serialized. Bind detaches the old handlers, starts the
// new ones without holding the data lock, and only then swaps them in, so two
// concurrent binds could otherwise interleave as detach/detach/swap/swap and
// leave the loser's handlers started but unreferenced — running bot pollers
// that nothing will ever Stop, feeding a retired agent's messages into the live
// one. That is precisely what this type exists to prevent, and the gateway can
// reach here concurrently: startup binds while a primary agent switch binds.
func (r *Registry) Bind(ctx context.Context, agentID string, handlers []Handler, bus Bus) error {
	if r == nil {
		return nil
	}
	r.bindMu.Lock()
	defer r.bindMu.Unlock()
	// A handler that misbehaves on the way out must not keep the new agent's
	// channels from coming up: the old set is already detached, so its error is
	// reported alongside the start errors rather than aborting the bind.
	var errs []error
	if err := r.stop(ctx); err != nil {
		errs = append(errs, err)
	}
	routes := map[string]http.HandlerFunc{}
	add := func(method, path string, hf http.HandlerFunc) {
		if hf == nil {
			return
		}
		routes[routeKey(method, path)] = hf
	}
	started := make([]Handler, 0, len(handlers))
	for _, h := range handlers {
		if h == nil {
			continue
		}
		if err := h.Start(ctx, add, bus); err != nil {
			errs = append(errs, fmt.Errorf("channel %s: %w", h.ID(), err))
			continue
		}
		started = append(started, h)
	}
	r.mu.Lock()
	r.agentID = strings.TrimSpace(agentID)
	r.handlers = started
	r.routes = routes
	r.mu.Unlock()
	return errors.Join(errs...)
}

// Stop tears down the currently bound channels and clears their routes.
func (r *Registry) Stop(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.bindMu.Lock()
	defer r.bindMu.Unlock()
	return r.stop(ctx)
}

// stop is Stop's body, called with bindMu already held.
func (r *Registry) stop(ctx context.Context) error {
	r.mu.Lock()
	handlers := r.handlers
	r.handlers = nil
	r.routes = map[string]http.HandlerFunc{}
	r.agentID = ""
	r.mu.Unlock()
	var errs []error
	for _, h := range handlers {
		if h == nil {
			continue
		}
		if err := h.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("channel %s: %w", h.ID(), err))
		}
	}
	return errors.Join(errs...)
}

// All returns the handlers currently running. Outbound delivery looks a
// channel up here, so it follows the bound agent automatically.
func (r *Registry) All() []Handler {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.handlers) == 0 {
		return nil
	}
	return append([]Handler(nil), r.handlers...)
}

// AgentID names the primary agent whose channels are bound, or "" when none
// are.
func (r *Registry) AgentID() string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.agentID
}

// Route returns the handler mounted at method+path by the bound agent's
// channels.
func (r *Registry) Route(method, path string) (http.HandlerFunc, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	hf, ok := r.routes[routeKey(method, path)]
	return hf, ok
}

func routeKey(method, path string) string {
	m := strings.ToUpper(strings.TrimSpace(method))
	if m == "" {
		m = http.MethodGet
	}
	return m + " " + strings.TrimSpace(path)
}

type OutboundSender interface {
	Handler
	DeliverOutbound(ctx context.Context, o Outbound) error
}

const DefaultDeliveryTimeout = 10 * time.Second

// ErrNoSuchChannel reports that no bound handler answers to the outbound
// message's ChannelID. It is a normal outcome, not a fault: a channel can be
// unbound between the moment a turn produces a reply and the moment that reply
// is delivered (a primary agent switch does exactly that), and the reply then
// has nowhere to go.
var ErrNoSuchChannel = errors.New("channel: no bound handler for channel id")

// ErrNotOutbound reports that the handler owning the channel ID exists but
// cannot send. Inbound-only channels (a webhook that only receives) are
// legitimate, so this too is an outcome rather than a fault.
var ErrNotOutbound = errors.New("channel: handler does not send outbound messages")

// Deliver sends one outbound message through the bound handler that owns
// out.ChannelID. It is the canonical outbound path: the registry is the only
// thing that knows which handlers are bound to the active agent, so resolving
// a channel ID to a sender belongs here rather than in a surface.
//
// The message is delivered with exactly one attempt under
// DefaultDeliveryTimeout. Retrying is deliberately not the default — a channel
// provider that times out may still have posted the message, so a blind retry
// risks showing the user the same reply twice. Callers that know their
// provider's delivery is idempotent can use DeliverWithRetry instead.
//
// Text is trimmed and an empty message is dropped, because every provider
// either rejects an empty body or renders it as a blank message. Callers own
// any transformation of the text itself (sanitizing, truncation) and any
// unwrapping of the surface's session ID, since both depend on packages that
// sit above this layer.
func (r *Registry) Deliver(ctx context.Context, out Outbound) error {
	chID := strings.TrimSpace(out.ChannelID)
	text := strings.TrimSpace(out.Text)
	if r == nil || chID == "" || text == "" {
		return nil
	}
	for _, h := range r.All() {
		if h == nil || strings.TrimSpace(h.ID()) != chID {
			continue
		}
		sender, ok := h.(OutboundSender)
		if !ok {
			return ErrNotOutbound
		}
		sendCtx, cancel := context.WithTimeout(ctx, DefaultDeliveryTimeout)
		err := sender.DeliverOutbound(sendCtx, Outbound{
			ChannelID: chID,
			SessionID: out.SessionID,
			Text:      text,
		})
		cancel()
		return err
	}
	return ErrNoSuchChannel
}

// BuildAgentChannels constructs the handlers for agentID's channels.
// workspaceRoot is that agent's workspace, where a channel that keeps local
// state (weixin's login and per-user context tokens) writes it.
func BuildAgentChannels(cfg *appcfg.Root, agentID string, workspaceRoot string) []Handler {
	if cfg == nil || strings.TrimSpace(agentID) == "" {
		return nil
	}
	ch := appcfg.ChannelsForAgent(cfg, agentID)
	return []Handler{
		NewTelegram(TelegramConfig{
			Enabled:  ch.Telegram.Enabled,
			BotToken: ch.Telegram.BotToken,
		}),
		NewDiscord(DiscordConfig{
			Enabled:  ch.Discord.Enabled,
			BotToken: ch.Discord.BotToken,
		}),
		NewSlack(SlackConfig{
			Enabled:     ch.Slack.Enabled,
			InboundPath: ch.Slack.InboundPath,
			OutboundURL: ch.Slack.OutboundURL,
			BotToken:    ch.Slack.BotToken,
			Secret:      ch.Slack.Secret,
		}),
		NewWhatsApp(WhatsAppConfig{
			Enabled:     ch.WhatsApp.Enabled,
			InboundPath: ch.WhatsApp.InboundPath,
			OutboundURL: ch.WhatsApp.OutboundURL,
			Token:       ch.WhatsApp.Token,
			Secret:      ch.WhatsApp.Secret,
		}),
		NewSignal(SignalConfig{
			Enabled:     ch.Signal.Enabled,
			OutboundURL: ch.Signal.OutboundURL,
			Token:       ch.Signal.Token,
		}),
		NewMattermost(MattermostConfig{
			Enabled:     ch.Mattermost.Enabled,
			OutboundURL: ch.Mattermost.OutboundURL,
			Token:       ch.Mattermost.Token,
		}),
		NewMatrix(MatrixConfig{
			Enabled:     ch.Matrix.Enabled,
			OutboundURL: ch.Matrix.OutboundURL,
			Token:       ch.Matrix.Token,
		}),
		NewHomeAssistant(HomeAssistantConfig{
			Enabled:     ch.HomeAssistant.Enabled,
			OutboundURL: ch.HomeAssistant.OutboundURL,
			Token:       ch.HomeAssistant.Token,
		}),
		NewEmail(EmailConfig{
			Enabled:     ch.Email.Enabled,
			InboundPath: ch.Email.InboundPath,
			OutboundURL: ch.Email.OutboundURL,
			Token:       ch.Email.Token,
			Secret:      ch.Email.Secret,
		}),
		NewSMS(SMSConfig{
			Enabled:     ch.SMS.Enabled,
			InboundPath: ch.SMS.InboundPath,
			OutboundURL: ch.SMS.OutboundURL,
			Token:       ch.SMS.Token,
			Secret:      ch.SMS.Secret,
		}),
		NewWebhook(WebhookConfig{
			Enabled:     ch.Webhook.Enabled,
			InboundPath: ch.Webhook.InboundPath,
			OutboundURL: ch.Webhook.OutboundURL,
			Token:       ch.Webhook.Token,
			Secret:      ch.Webhook.Secret,
		}),
		NewBlueBubbles(BlueBubblesConfig{
			Enabled:     ch.BlueBubbles.Enabled,
			InboundPath: ch.BlueBubbles.InboundPath,
			OutboundURL: ch.BlueBubbles.OutboundURL,
			Token:       ch.BlueBubbles.Token,
			Secret:      ch.BlueBubbles.Secret,
		}),
		NewWeCom(WeComConfig{
			ChannelID:      "wecom",
			Enabled:        ch.WeCom.Enabled,
			Token:          ch.WeCom.Token,
			EncodingAESKey: ch.WeCom.EncodingAESKey,
			CorpID:         ch.WeCom.CorpID,
			CorpSecret:     ch.WeCom.CorpSecret,
			AgentID:        ch.WeCom.AgentID,
			CallbackPath:   ch.WeCom.CallbackPath,
		}),
		NewWeCom(WeComConfig{
			ChannelID:      "wecom_callback",
			Enabled:        ch.WeComCallback.Enabled,
			Token:          ch.WeComCallback.Token,
			EncodingAESKey: ch.WeComCallback.EncodingAESKey,
			CorpID:         ch.WeComCallback.CorpID,
			CorpSecret:     ch.WeComCallback.CorpSecret,
			AgentID:        ch.WeComCallback.AgentID,
			CallbackPath:   ch.WeComCallback.CallbackPath,
		}),
		NewWeixin(WeixinConfig{
			Enabled:         ch.Weixin.Enabled,
			BaseURL:         ch.Weixin.BaseURL,
			CDNBaseURL:      ch.Weixin.CDNBaseURL,
			Token:           ch.Weixin.Token,
			AccountID:       ch.Weixin.AccountID,
			BotType:         ch.Weixin.BotType,
			ChannelVersion:  ch.Weixin.ChannelVersion,
			RouteTag:        ch.Weixin.RouteTag,
			SilkVoiceDecode: ch.Weixin.SilkVoiceDecode,
			StateRoot:       strings.TrimSpace(workspaceRoot),
		}),
		NewFeishu(FeishuConfig{
			Enabled:        ch.Feishu.Enabled,
			AppID:          ch.Feishu.AppID,
			AppSecret:      ch.Feishu.AppSecret,
			Domain:         ch.Feishu.Domain,
			ConnectionMode: ch.Feishu.ConnectionMode,
		}),
		NewDingTalk(DingTalkConfig{
			Enabled:      ch.Dingtalk.Enabled,
			ClientID:     ch.Dingtalk.ClientID,
			ClientSecret: ch.Dingtalk.ClientSecret,
		}),
		NewQQ(QQConfig{
			Enabled:      ch.QQ.Enabled,
			AppID:        ch.QQ.AppID,
			ClientSecret: ch.QQ.ClientSecret,
		}),
	}
}

// BindAgent builds agentID's channels from cfg and binds them, replacing
// whatever the previous agent had bound. It is the whole "switch the running
// channel set to this agent" operation: composing BuildAgentChannels with Bind
// belongs here, next to the registry that has to stay consistent across the
// switch, rather than in each surface that can trigger one.
//
// workspaceRoot is the agent's workspace, where a channel that keeps local
// state writes it. A blank agentID binds nothing and reports no error: there is
// no active agent to serve, which is a startup state rather than a fault.
func (r *Registry) BindAgent(ctx context.Context, cfg *appcfg.Root, agentID string, workspaceRoot string, bus Bus) error {
	if r == nil || strings.TrimSpace(agentID) == "" {
		return nil
	}
	id := strings.TrimSpace(agentID)
	return r.Bind(ctx, id, BuildAgentChannels(cfg, id, workspaceRoot), bus)
}

// HTTPHandler serves the bound channels' inbound endpoints, falling through to
// next for everything else. Pass nil next to serve the channels alone, which is
// what running without the web UI or WS chat looks like.
//
// The dispatch is per request rather than per mount because the route table
// lives on the registry: an http.ServeMux registration cannot be withdrawn, so
// mounting the channels directly would leave a switched-away agent's inbound
// endpoints answering after a rebind.
func (r *Registry) HTTPHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if hf, ok := r.Route(req.Method, req.URL.Path); ok {
			hf(w, req)
			return
		}
		if next == nil {
			http.NotFound(w, req)
			return
		}
		next.ServeHTTP(w, req)
	})
}
