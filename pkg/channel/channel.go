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
	// ProjectID is the project whose channel received this message. It is
	// stamped by the registry from the binding, never by the handler, so the
	// conversation the message opens belongs to that project.
	ProjectID string
	Text      string
	SessionID string
	Raw       any
}

type Outbound struct {
	ChannelID string
	// ProjectID selects the project's own instance of the channel, which is
	// what lets two projects run the same kind of bot without crossing wires.
	ProjectID string
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

// BoundChannel pairs one running handler with the project that owns it. A
// project's channel is a distinct instance even when the kind repeats — two
// projects may each run a Telegram bot — so the project is part of the binding
// rather than of the handler.
type BoundChannel struct {
	ProjectID string
	Handler   Handler
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
	channels []BoundChannel
	routes   map[string]http.HandlerFunc
}

func NewRegistry() *Registry {
	return &Registry{routes: map[string]http.HandlerFunc{}}
}

// BindProjectChannels binds one primary agent's channels without naming the
// projects they serve. It is the plain shape a handler with no project uses.
func (r *Registry) Bind(ctx context.Context, agentID string, handlers []Handler, bus Bus) error {
	bound := make([]BoundChannel, 0, len(handlers))
	for _, h := range handlers {
		bound = append(bound, BoundChannel{Handler: h})
	}
	return r.BindProjects(ctx, agentID, bound, bus)
}

// BindProjects binds one primary agent's project channels, replacing whatever
// the previous agent had bound. Each handler is started with a bus that stamps
// its project onto every inbound message, so a handler that knows nothing of
// projects still opens a conversation in the right one. A handler that fails to
// start is left out of the routing table and reported; the remaining channels
// still come up, because one misconfigured bot token must not take the whole
// gateway down with it.
//
// The whole operation is serialized. Bind detaches the old handlers, starts the
// new ones without holding the data lock, and only then swaps them in, so two
// concurrent binds could otherwise interleave as detach/detach/swap/swap and
// leave the loser's handlers started but unreferenced — running bot pollers
// that nothing will ever Stop, feeding a retired agent's messages into the live
// one. That is precisely what this type exists to prevent, and the gateway can
// reach here concurrently: startup binds while a primary agent switch binds.
func (r *Registry) BindProjects(ctx context.Context, agentID string, channels []BoundChannel, bus Bus) error {
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
	started := make([]BoundChannel, 0, len(channels))
	for _, bc := range channels {
		if bc.Handler == nil {
			continue
		}
		hb := bus
		if pid := strings.TrimSpace(bc.ProjectID); pid != "" && bus != nil {
			hb = projectBus{projectID: pid, inner: bus}
		}
		if err := bc.Handler.Start(ctx, add, hb); err != nil {
			errs = append(errs, fmt.Errorf("channel %s: %w", bc.Handler.ID(), err))
			continue
		}
		started = append(started, bc)
	}
	r.mu.Lock()
	r.agentID = strings.TrimSpace(agentID)
	r.channels = started
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
	channels := r.channels
	r.channels = nil
	r.routes = map[string]http.HandlerFunc{}
	r.agentID = ""
	r.mu.Unlock()
	var errs []error
	for _, bc := range channels {
		if bc.Handler == nil {
			continue
		}
		if err := bc.Handler.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("channel %s: %w", bc.Handler.ID(), err))
		}
	}
	return errors.Join(errs...)
}

// All returns the handlers currently running. Outbound delivery looks a
// channel up here, so it follows the bound agent automatically.
func (r *Registry) All() []Handler {
	bound := r.BoundAll()
	if len(bound) == 0 {
		return nil
	}
	out := make([]Handler, len(bound))
	for i, bc := range bound {
		out[i] = bc.Handler
	}
	return out
}

// BoundAll returns the running channels with the projects they serve. Delivery
// resolves a project's instance from this, so it follows the bound agent.
func (r *Registry) BoundAll() []BoundChannel {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.channels) == 0 {
		return nil
	}
	return append([]BoundChannel(nil), r.channels...)
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
// out.ChannelID and serves out.ProjectID. It is the canonical outbound path:
// the registry is the only thing that knows which handlers are bound to the
// active agent, so resolving a channel id — and, when the channel is a
// project's, that project's instance — belongs here rather than in a surface.
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
	projectID := strings.TrimSpace(out.ProjectID)
	for _, bc := range r.BoundAll() {
		if bc.Handler == nil || strings.TrimSpace(bc.Handler.ID()) != chID {
			continue
		}
		// A project-scoped reply goes to that project's own instance; a reply
		// with no project bound matches the first instance of the kind.
		if projectID != "" && strings.TrimSpace(bc.ProjectID) != projectID {
			continue
		}
		sender, ok := bc.Handler.(OutboundSender)
		if !ok {
			return ErrNotOutbound
		}
		sendCtx, cancel := context.WithTimeout(ctx, DefaultDeliveryTimeout)
		err := sender.DeliverOutbound(sendCtx, Outbound{
			ChannelID: chID,
			ProjectID: projectID,
			SessionID: out.SessionID,
			Text:      text,
		})
		cancel()
		return err
	}
	return ErrNoSuchChannel
}

// ProjectChannelConfig is one project's channel to bind: the project it serves,
// the kind, and the section that carries its credentials.
type ProjectChannelConfig struct {
	ProjectID string
	Kind      string
	Section   appcfg.ChannelsSection
}

// ChannelKinds lists every channel kind the registry can build, in the order a
// surface should present them. It is the one list a page reads to name the
// channels it offers, so adding one cannot leave a surface behind.
var ChannelKinds = []string{
	"telegram", "discord", "slack", "whatsapp", "signal", "mattermost", "matrix",
	"homeassistant", "email", "sms", "webhook", "bluebubbles", "wecom",
	"wecom_callback", "weixin", "feishu", "dingtalk", "qq",
}

// BuildProjectChannel constructs the handler for one kind of a project's
// channels, or nil when the kind is unknown. stateRoot is where a channel that
// keeps local state (weixin's login and per-user context tokens) writes it.
func BuildProjectChannel(kind string, ch appcfg.ChannelsSection, stateRoot string) Handler {
	switch strings.TrimSpace(kind) {
	case "telegram":
		return NewTelegram(TelegramConfig{Enabled: ch.Telegram.Enabled, BotToken: ch.Telegram.BotToken})
	case "discord":
		return NewDiscord(DiscordConfig{Enabled: ch.Discord.Enabled, BotToken: ch.Discord.BotToken})
	case "slack":
		return NewSlack(SlackConfig{
			Enabled: ch.Slack.Enabled, InboundPath: ch.Slack.InboundPath,
			OutboundURL: ch.Slack.OutboundURL, BotToken: ch.Slack.BotToken, Secret: ch.Slack.Secret,
		})
	case "whatsapp":
		return NewWhatsApp(WhatsAppConfig{
			Enabled: ch.WhatsApp.Enabled, InboundPath: ch.WhatsApp.InboundPath,
			OutboundURL: ch.WhatsApp.OutboundURL, Token: ch.WhatsApp.Token, Secret: ch.WhatsApp.Secret,
		})
	case "signal":
		return NewSignal(SignalConfig{Enabled: ch.Signal.Enabled, OutboundURL: ch.Signal.OutboundURL, Token: ch.Signal.Token})
	case "mattermost":
		return NewMattermost(MattermostConfig{Enabled: ch.Mattermost.Enabled, OutboundURL: ch.Mattermost.OutboundURL, Token: ch.Mattermost.Token})
	case "matrix":
		return NewMatrix(MatrixConfig{Enabled: ch.Matrix.Enabled, OutboundURL: ch.Matrix.OutboundURL, Token: ch.Matrix.Token})
	case "homeassistant":
		return NewHomeAssistant(HomeAssistantConfig{Enabled: ch.HomeAssistant.Enabled, OutboundURL: ch.HomeAssistant.OutboundURL, Token: ch.HomeAssistant.Token})
	case "email":
		return NewEmail(EmailConfig{
			Enabled: ch.Email.Enabled, InboundPath: ch.Email.InboundPath,
			OutboundURL: ch.Email.OutboundURL, Token: ch.Email.Token, Secret: ch.Email.Secret,
		})
	case "sms":
		return NewSMS(SMSConfig{
			Enabled: ch.SMS.Enabled, InboundPath: ch.SMS.InboundPath,
			OutboundURL: ch.SMS.OutboundURL, Token: ch.SMS.Token, Secret: ch.SMS.Secret,
		})
	case "webhook":
		return NewWebhook(WebhookConfig{
			Enabled: ch.Webhook.Enabled, InboundPath: ch.Webhook.InboundPath,
			OutboundURL: ch.Webhook.OutboundURL, Token: ch.Webhook.Token, Secret: ch.Webhook.Secret,
		})
	case "bluebubbles":
		return NewBlueBubbles(BlueBubblesConfig{
			Enabled: ch.BlueBubbles.Enabled, InboundPath: ch.BlueBubbles.InboundPath,
			OutboundURL: ch.BlueBubbles.OutboundURL, Token: ch.BlueBubbles.Token, Secret: ch.BlueBubbles.Secret,
		})
	case "wecom":
		return NewWeCom(WeComConfig{
			ChannelID: "wecom", Enabled: ch.WeCom.Enabled, Token: ch.WeCom.Token,
			EncodingAESKey: ch.WeCom.EncodingAESKey, CorpID: ch.WeCom.CorpID,
			CorpSecret: ch.WeCom.CorpSecret, AgentID: ch.WeCom.AgentID, CallbackPath: ch.WeCom.CallbackPath,
		})
	case "wecom_callback":
		return NewWeCom(WeComConfig{
			ChannelID: "wecom_callback", Enabled: ch.WeComCallback.Enabled, Token: ch.WeComCallback.Token,
			EncodingAESKey: ch.WeComCallback.EncodingAESKey, CorpID: ch.WeComCallback.CorpID,
			CorpSecret: ch.WeComCallback.CorpSecret, AgentID: ch.WeComCallback.AgentID, CallbackPath: ch.WeComCallback.CallbackPath,
		})
	case "weixin":
		return NewWeixin(WeixinConfig{
			Enabled: ch.Weixin.Enabled, BaseURL: ch.Weixin.BaseURL, CDNBaseURL: ch.Weixin.CDNBaseURL,
			Token: ch.Weixin.Token, AccountID: ch.Weixin.AccountID, BotType: ch.Weixin.BotType,
			ChannelVersion: ch.Weixin.ChannelVersion, RouteTag: ch.Weixin.RouteTag,
			SilkVoiceDecode: ch.Weixin.SilkVoiceDecode, StateRoot: strings.TrimSpace(stateRoot),
		})
	case "feishu":
		return NewFeishu(FeishuConfig{
			Enabled: ch.Feishu.Enabled, AppID: ch.Feishu.AppID, AppSecret: ch.Feishu.AppSecret,
			Domain: ch.Feishu.Domain, ConnectionMode: ch.Feishu.ConnectionMode,
		})
	case "dingtalk":
		return NewDingTalk(DingTalkConfig{Enabled: ch.Dingtalk.Enabled, ClientID: ch.Dingtalk.ClientID, ClientSecret: ch.Dingtalk.ClientSecret})
	case "qq":
		return NewQQ(QQConfig{Enabled: ch.QQ.Enabled, AppID: ch.QQ.AppID, ClientSecret: ch.QQ.ClientSecret})
	default:
		return nil
	}
}

// BuildProjectChannels builds one handler per configured (project, kind).
// stateRoot is the agent's workspace, where a channel that keeps local state
// writes it.
func BuildProjectChannels(configs []ProjectChannelConfig, stateRoot string) []BoundChannel {
	out := make([]BoundChannel, 0, len(configs))
	for _, cfg := range configs {
		h := BuildProjectChannel(cfg.Kind, cfg.Section, stateRoot)
		if h == nil {
			continue
		}
		out = append(out, BoundChannel{ProjectID: strings.TrimSpace(cfg.ProjectID), Handler: h})
	}
	return out
}

// projectBus stamps the project onto every inbound message the handler beneath
// it publishes, so a handler that knows nothing of projects still opens a
// conversation in the right one.
type projectBus struct {
	projectID string
	inner     Bus
}

func (b projectBus) PublishInbound(ctx context.Context, m Inbound) error {
	if strings.TrimSpace(m.ProjectID) == "" {
		m.ProjectID = b.projectID
	}
	return b.inner.PublishInbound(ctx, m)
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
