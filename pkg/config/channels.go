package config

import (
	"encoding/json"
	"reflect"
	"strings"
)

// ChannelsSection is one project's delivery channels.
//
// Channels are project data, not agent-level settings: the bot that carries a
// conversation in from Feishu or Telegram belongs to exactly one project, the
// same way that project's conversations, memories and skills do. A section is
// therefore stored per project (in the state database, as this struct's JSON)
// rather than under agents.definitions.<id>; there is deliberately no
// agent-level channel configuration for a caller to read.
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

// NormalizeChannels trims every free-form field and fills in the defaults for
// one project's channels. A section read back from the state database goes
// through this so a stored channel is rendered with the same defaults a
// freshly written one has.
func NormalizeChannels(c *ChannelsSection) {
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

// ParseChannelsSectionJSON reads one project's channel section from the JSON
// the state database stores: it fills in the kind defaults and resolves the
// ${VAR} references a secret field carries against the process environment,
// which the loader has already merged ~/.forebrain/.env into.
func ParseChannelsSectionJSON(s string) (ChannelsSection, error) {
	var sec ChannelsSection
	if raw := strings.TrimSpace(s); raw != "" && raw != "{}" {
		if err := json.Unmarshal([]byte(raw), &sec); err != nil {
			return ChannelsSection{}, err
		}
	}
	NormalizeChannels(&sec)
	ExpandChannelEnvReferences(&sec)
	return sec, nil
}

// ParseChannelKindJSON reads one kind's stored config — the JSON of that kind's
// own struct, which is what a (project, kind) row holds — as a section with
// that kind filled in. It is the read path for a bound channel.
func ParseChannelKindJSON(kind, s string) (ChannelsSection, error) {
	raw := strings.TrimSpace(s)
	if raw == "" || raw == "{}" {
		return ChannelsSection{}, nil
	}
	return ParseChannelsSectionJSON(`{"` + strings.TrimSpace(kind) + `":` + raw + `}`)
}

// MarshalChannelsSection renders a section as the JSON the state database
// stores. Secret fields keep their ${VAR} references, never the value.
func MarshalChannelsSection(sec ChannelsSection) (string, error) {
	b, err := json.Marshal(sec)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ChannelKindDefaults returns one kind's fields as a section with every default
// filled in, so a surface can render a card for a kind that has nothing stored
// yet without hard-coding the fields.
func ChannelKindDefaults(kind string) map[string]any {
	var sec ChannelsSection
	NormalizeChannels(&sec)
	raw, err := json.Marshal(sec)
	if err != nil {
		return map[string]any{}
	}
	var all map[string]map[string]any
	if err := json.Unmarshal(raw, &all); err != nil {
		return map[string]any{}
	}
	if m, ok := all[strings.TrimSpace(kind)]; ok {
		return m
	}
	return map[string]any{}
}

// ChannelKindFields returns one kind's whole field set — every key its
// configuration struct declares, with the kind's defaults where there are any
// and an empty value otherwise. ChannelKindDefaults alone is not enough to fill
// in a form: it marshals with `omitempty`, so a kind nobody has configured yet
// answers with only the fields that carry a default (usually just `enabled`),
// and a page asked to edit it would have nothing to type into.
func ChannelKindFields(kind string) map[string]any {
	fields := ChannelKindDefaults(kind)
	target := strings.TrimSpace(kind)
	section := reflect.TypeOf(ChannelsSection{})
	for i := 0; i < section.NumField(); i++ {
		field := section.Field(i)
		if jsonName, _, _ := strings.Cut(field.Tag.Get("json"), ","); jsonName != target {
			continue
		}
		kindType := field.Type
		for j := 0; j < kindType.NumField(); j++ {
			inner := kindType.Field(j)
			jsonName, _, _ := strings.Cut(inner.Tag.Get("json"), ",")
			if jsonName == "" || jsonName == "-" {
				continue
			}
			if _, ok := fields[jsonName]; ok {
				continue
			}
			fields[jsonName] = zeroChannelField(inner.Type)
		}
		return fields
	}
	return fields
}

// zeroChannelField is the value a form shows for a field that has none: an
// empty string for text, an empty list for a list, and the zero for whatever
// else the struct declares.
func zeroChannelField(t reflect.Type) any {
	switch t.Kind() {
	case reflect.Slice:
		return []any{}
	case reflect.Bool:
		return false
	case reflect.Int, reflect.Int64:
		return 0
	case reflect.Ptr:
		return nil
	}
	return ""
}

// ExpandChannelEnvReferences replaces the ${VAR} references a channel section
// carries with the values of the process environment, the same expansion the
// loader applies to forebrain.yaml. A channel read back from the state
// database resolves its secrets by this path.
func ExpandChannelEnvReferences(ch *ChannelsSection) {
	if ch == nil {
		return
	}
	expandStringEnvReferences(reflect.ValueOf(ch))
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
