package config

import (
	"reflect"
	"strings"
)

// ChannelsSection is one primary agent's delivery channels.
//
// Channels are tenant data, not process-wide settings: the bot that carries a
// conversation in from Feishu or Telegram belongs to exactly one primary agent,
// the same way that agent's workspace, memories, sessions and skills do. They
// therefore live under agents.definitions.<id>.channels and are reached only
// through ChannelsForAgent — there is deliberately no Root-level channel
// configuration for a caller to read without naming an agent.
type ChannelsSection struct {
	WeCom         WeCom         `yaml:"wecom,omitempty" json:"wecom,omitempty"`
	WeComCallback WeComCallback `yaml:"wecom_callback,omitempty" json:"wecom_callback,omitempty"`
	Telegram      Telegram      `yaml:"telegram,omitempty" json:"telegram,omitempty"`
	Discord       Discord       `yaml:"discord,omitempty" json:"discord,omitempty"`
	Slack         Slack         `yaml:"slack,omitempty" json:"slack,omitempty"`
	WhatsApp      WhatsApp      `yaml:"whatsapp,omitempty" json:"whatsapp,omitempty"`
	Signal        Signal        `yaml:"signal,omitempty" json:"signal,omitempty"`
	Mattermost    Mattermost    `yaml:"mattermost,omitempty" json:"mattermost,omitempty"`
	Matrix        Matrix        `yaml:"matrix,omitempty" json:"matrix,omitempty"`
	HomeAssistant HomeAssistant `yaml:"homeassistant,omitempty" json:"homeassistant,omitempty"`
	Email         Email         `yaml:"email,omitempty" json:"email,omitempty"`
	SMS           SMS           `yaml:"sms,omitempty" json:"sms,omitempty"`
	Webhook       Webhook       `yaml:"webhook,omitempty" json:"webhook,omitempty"`
	BlueBubbles   BlueBubbles   `yaml:"bluebubbles,omitempty" json:"bluebubbles,omitempty"`
	Weixin        Weixin        `yaml:"weixin,omitempty" json:"weixin,omitempty"`
	Feishu        Feishu        `yaml:"feishu,omitempty" json:"feishu,omitempty"`
	Dingtalk      Dingtalk      `yaml:"dingtalk,omitempty" json:"dingtalk,omitempty"`
	QQ            QQ            `yaml:"qq,omitempty" json:"qq,omitempty"`
}

// ChannelsForAgent returns the channels configured for one primary agent.
//
// An agent with no definition, or a definition with no channels, has no
// channels: the result is the zero section and never another agent's
// configuration. Falling back to "main" here would restore exactly the global
// behavior this scoping exists to remove.
func ChannelsForAgent(r *Root, agentID string) ChannelsSection {
	if r == nil {
		return ChannelsSection{}
	}
	return r.Agents.Definitions[strings.TrimSpace(agentID)].Channels
}

// IsZero reports whether an agent declared no channels at all. It is how
// validation tells "this definition has channels" from "this definition has
// none", so it must be asked before normalizeChannels fills in any defaults.
func (c ChannelsSection) IsZero() bool {
	return reflect.DeepEqual(c, ChannelsSection{})
}

// normalizeChannels trims every free-form field and fills in the defaults for
// one agent's channels. Only primary agents are normalized (see normalize):
// materializing channel defaults onto a subagent definition would write
// configuration that can never take effect into forebrain.yaml.
func normalizeChannels(c *ChannelsSection) {
	if c == nil {
		return
	}
	c.WeCom.CallbackPath = strings.TrimSpace(c.WeCom.CallbackPath)
	c.WeComCallback.CallbackPath = strings.TrimSpace(c.WeComCallback.CallbackPath)
	c.Slack.InboundPath = strings.TrimSpace(c.Slack.InboundPath)
	c.WhatsApp.InboundPath = strings.TrimSpace(c.WhatsApp.InboundPath)
	c.WhatsApp.OutboundURL = strings.TrimSpace(c.WhatsApp.OutboundURL)
	c.Signal.OutboundURL = strings.TrimSpace(c.Signal.OutboundURL)
	c.Mattermost.OutboundURL = strings.TrimSpace(c.Mattermost.OutboundURL)
	c.Matrix.OutboundURL = strings.TrimSpace(c.Matrix.OutboundURL)
	c.HomeAssistant.OutboundURL = strings.TrimSpace(c.HomeAssistant.OutboundURL)
	c.Email.InboundPath = strings.TrimSpace(c.Email.InboundPath)
	c.SMS.InboundPath = strings.TrimSpace(c.SMS.InboundPath)
	c.Webhook.InboundPath = strings.TrimSpace(c.Webhook.InboundPath)
	c.BlueBubbles.InboundPath = strings.TrimSpace(c.BlueBubbles.InboundPath)
	c.Weixin.BaseURL = strings.TrimSpace(c.Weixin.BaseURL)
	c.Weixin.DmPolicy = strings.TrimSpace(c.Weixin.DmPolicy)
	c.Feishu.Domain = strings.TrimSpace(c.Feishu.Domain)
	c.Feishu.ConnectionMode = strings.TrimSpace(c.Feishu.ConnectionMode)
	c.Feishu.GroupPolicy = strings.TrimSpace(c.Feishu.GroupPolicy)
	c.Dingtalk.DmPolicy = strings.TrimSpace(c.Dingtalk.DmPolicy)
	c.Dingtalk.GroupPolicy = strings.TrimSpace(c.Dingtalk.GroupPolicy)

	setDefault(&c.Slack.InboundPath, "/channels/slack/inbound")
	setDefault(&c.WhatsApp.InboundPath, "/channels/whatsapp/inbound")
	setDefault(&c.WhatsApp.OutboundURL, "http://127.0.0.1:3000")
	setDefault(&c.HomeAssistant.OutboundURL, "http://homeassistant.local:8123")
	setDefault(&c.Email.InboundPath, "/channels/email/inbound")
	setDefault(&c.SMS.InboundPath, "/channels/sms/inbound")
	setDefault(&c.Webhook.InboundPath, "/channels/webhook/inbound")
	setDefault(&c.BlueBubbles.InboundPath, "/channels/bluebubbles/inbound")
	setDefault(&c.WeCom.CallbackPath, "/wecom/callback")
	setDefault(&c.WeComCallback.CallbackPath, "/wecom_callback/callback")
	setDefault(&c.Feishu.Domain, "feishu")
	setDefault(&c.Feishu.ConnectionMode, "websocket")
	setDefault(&c.Feishu.GroupPolicy, "allowlist")
	setDefault(&c.Dingtalk.DmPolicy, "open")
	setDefault(&c.Dingtalk.GroupPolicy, "open")
	setDefault(&c.Weixin.DmPolicy, "open")
	setDefault(&c.Weixin.BaseURL, "https://ilinkai.weixin.qq.com")
}

func setDefault(dst *string, fallback string) {
	if *dst == "" {
		*dst = fallback
	}
}

// SetChannelsForAgent writes a section back onto an agent definition. Agent
// definitions live in a map, so their fields are not addressable and every
// read-modify-write of a section (first-run setup, external configuration
// edits, or a Weixin credential merge) has to round-trip through here.
func SetChannelsForAgent(r *Root, agentID string, ch ChannelsSection) {
	if r == nil {
		return
	}
	id := strings.TrimSpace(agentID)
	if id == "" {
		return
	}
	if r.Agents.Definitions == nil {
		r.Agents.Definitions = map[string]AgentDefinition{}
	}
	def := r.Agents.Definitions[id]
	def.Channels = ch
	r.Agents.Definitions[id] = def
}

type Feishu struct {
	Enabled        bool     `yaml:"enabled" json:"enabled"`
	AppID          string   `yaml:"app_id,omitempty" json:"app_id,omitempty"`
	AppSecret      string   `yaml:"app_secret,omitempty" json:"app_secret,omitempty"`
	Domain         string   `yaml:"domain,omitempty" json:"domain,omitempty"`
	ConnectionMode string   `yaml:"connection_mode,omitempty" json:"connection_mode,omitempty"`
	GroupPolicy    string   `yaml:"group_policy,omitempty" json:"group_policy,omitempty"`
	GroupAllowFrom []string `yaml:"group_allow_from,omitempty" json:"group_allow_from,omitempty"`
}

type Dingtalk struct {
	Enabled        bool     `yaml:"enabled" json:"enabled"`
	ClientID       string   `yaml:"client_id,omitempty" json:"client_id,omitempty"`
	ClientSecret   string   `yaml:"client_secret,omitempty" json:"client_secret,omitempty"`
	DmPolicy       string   `yaml:"dm_policy,omitempty" json:"dm_policy,omitempty"`
	GroupPolicy    string   `yaml:"group_policy,omitempty" json:"group_policy,omitempty"`
	AllowFrom      []string `yaml:"allow_from,omitempty" json:"allow_from,omitempty"`
	GroupAllowFrom []string `yaml:"group_allow_from,omitempty" json:"group_allow_from,omitempty"`
}

type QQ struct {
	Enabled      bool     `yaml:"enabled" json:"enabled"`
	AppID        string   `yaml:"app_id,omitempty" json:"app_id,omitempty"`
	ClientSecret string   `yaml:"client_secret,omitempty" json:"client_secret,omitempty"`
	AllowFrom    []string `yaml:"allow_from,omitempty" json:"allow_from,omitempty"`
}

type Weixin struct {
	Enabled         bool     `yaml:"enabled" json:"enabled"`
	BaseURL         string   `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	CDNBaseURL      string   `yaml:"cdn_base_url,omitempty" json:"cdn_base_url,omitempty"`
	Token           string   `yaml:"token,omitempty" json:"token,omitempty"`
	AccountID       string   `yaml:"account_id,omitempty" json:"account_id,omitempty"`
	BotType         string   `yaml:"bot_type,omitempty" json:"bot_type,omitempty"`
	ChannelVersion  string   `yaml:"channel_version,omitempty" json:"channel_version,omitempty"`
	RouteTag        string   `yaml:"route_tag,omitempty" json:"route_tag,omitempty"`
	SilkVoiceDecode bool     `yaml:"silk_voice_decode" json:"silk_voice_decode"`
	DmPolicy        string   `yaml:"dm_policy,omitempty" json:"dm_policy,omitempty"`
	AllowFrom       []string `yaml:"allow_from,omitempty" json:"allow_from,omitempty"`
}

type WeCom struct {
	Enabled        bool   `yaml:"enabled" json:"enabled"`
	Token          string `yaml:"token,omitempty" json:"token,omitempty"`
	EncodingAESKey string `yaml:"encoding_aes_key,omitempty" json:"encoding_aes_key,omitempty"`
	CorpID         string `yaml:"corp_id,omitempty" json:"corp_id,omitempty"`
	CorpSecret     string `yaml:"corp_secret,omitempty" json:"corp_secret,omitempty"`
	AgentID        int64  `yaml:"agent_id,omitempty" json:"agent_id,omitempty"`
	CallbackPath   string `yaml:"callback_path,omitempty" json:"callback_path,omitempty"`
}

type WeComCallback struct {
	Enabled        bool   `yaml:"enabled" json:"enabled"`
	Token          string `yaml:"token,omitempty" json:"token,omitempty"`
	EncodingAESKey string `yaml:"encoding_aes_key,omitempty" json:"encoding_aes_key,omitempty"`
	CorpID         string `yaml:"corp_id,omitempty" json:"corp_id,omitempty"`
	CorpSecret     string `yaml:"corp_secret,omitempty" json:"corp_secret,omitempty"`
	AgentID        int64  `yaml:"agent_id,omitempty" json:"agent_id,omitempty"`
	CallbackPath   string `yaml:"callback_path,omitempty" json:"callback_path,omitempty"`
}

type Telegram struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	BotToken string `yaml:"bot_token,omitempty" json:"bot_token,omitempty"`
}

type Discord struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	BotToken string `yaml:"bot_token,omitempty" json:"bot_token,omitempty"`
}

type Slack struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	InboundPath string `yaml:"inbound_path,omitempty" json:"inbound_path,omitempty"`
	OutboundURL string `yaml:"outbound_url,omitempty" json:"outbound_url,omitempty"`
	BotToken    string `yaml:"bot_token,omitempty" json:"bot_token,omitempty"`
	Secret      string `yaml:"secret,omitempty" json:"secret,omitempty"`
}

type WhatsApp struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	InboundPath string `yaml:"inbound_path,omitempty" json:"inbound_path,omitempty"`
	OutboundURL string `yaml:"outbound_url,omitempty" json:"outbound_url,omitempty"`
	Token       string `yaml:"token,omitempty" json:"token,omitempty"`
	Secret      string `yaml:"secret,omitempty" json:"secret,omitempty"`
}

type Signal struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	OutboundURL string `yaml:"outbound_url,omitempty" json:"outbound_url,omitempty"`
	Token       string `yaml:"token,omitempty" json:"token,omitempty"`
}

type Mattermost struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	OutboundURL string `yaml:"outbound_url,omitempty" json:"outbound_url,omitempty"`
	Token       string `yaml:"token,omitempty" json:"token,omitempty"`
}

type Matrix struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	OutboundURL string `yaml:"outbound_url,omitempty" json:"outbound_url,omitempty"`
	Token       string `yaml:"token,omitempty" json:"token,omitempty"`
}

type HomeAssistant struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	OutboundURL string `yaml:"outbound_url,omitempty" json:"outbound_url,omitempty"`
	Token       string `yaml:"token,omitempty" json:"token,omitempty"`
}

type Email struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	InboundPath string `yaml:"inbound_path,omitempty" json:"inbound_path,omitempty"`
	OutboundURL string `yaml:"outbound_url,omitempty" json:"outbound_url,omitempty"`
	Token       string `yaml:"token,omitempty" json:"token,omitempty"`
	Secret      string `yaml:"secret,omitempty" json:"secret,omitempty"`
}

type SMS struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	InboundPath string `yaml:"inbound_path,omitempty" json:"inbound_path,omitempty"`
	OutboundURL string `yaml:"outbound_url,omitempty" json:"outbound_url,omitempty"`
	Token       string `yaml:"token,omitempty" json:"token,omitempty"`
	Secret      string `yaml:"secret,omitempty" json:"secret,omitempty"`
}

type Webhook struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	InboundPath string `yaml:"inbound_path,omitempty" json:"inbound_path,omitempty"`
	OutboundURL string `yaml:"outbound_url,omitempty" json:"outbound_url,omitempty"`
	Token       string `yaml:"token,omitempty" json:"token,omitempty"`
	Secret      string `yaml:"secret,omitempty" json:"secret,omitempty"`
}

type BlueBubbles struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	InboundPath string `yaml:"inbound_path,omitempty" json:"inbound_path,omitempty"`
	OutboundURL string `yaml:"outbound_url,omitempty" json:"outbound_url,omitempty"`
	Token       string `yaml:"token,omitempty" json:"token,omitempty"`
	Secret      string `yaml:"secret,omitempty" json:"secret,omitempty"`
}
