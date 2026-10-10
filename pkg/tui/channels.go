// Channel management commands and the approval surface.
package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/mattn/go-runewidth"
)

// channelConfigurator edits one channel of one primary agent. It is handed
// that agent's section rather than the whole configuration, and its id, so the
// secrets it stores in ~/.forebrain/.env are named per agent and cannot be picked
// up by another agent running the same kind of channel.
type channelConfigurator func(selector Selector, ch *appcfg.ChannelsSection, home string, agentID string) error

type channelMeta struct {
	ID           string
	Label        string
	Blurb        string
	IsConfigured func(*appcfg.ChannelsSection) bool
	Disable      func(*appcfg.ChannelsSection)
	Configure    channelConfigurator
}

var channelOrder = []string{
	"telegram",
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
}

var channelMetaByID = map[string]channelMeta{
	"telegram": {
		ID:    "telegram",
		Label: "Telegram",
		Blurb: "Bot token polling channel.",
		IsConfigured: func(ch *appcfg.ChannelsSection) bool {
			return ch != nil && ch.Telegram.Enabled
		},
		Disable: func(ch *appcfg.ChannelsSection) {
			if ch != nil {
				ch.Telegram.Enabled = false
			}
		},
		Configure: func(selector Selector, ch *appcfg.ChannelsSection, home string, agentID string) error {
			token, ok, err := promptRetainedInputWithDisplay(selector, "Telegram bot token", ch.Telegram.BotToken, hiddenEnvReferenceDefault(ch.Telegram.BotToken))
			if err != nil || !ok {
				return err
			}
			ref, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, "TELEGRAM_BOT_TOKEN"), token)
			if err != nil {
				return err
			}
			ch.Telegram.BotToken = ref
			ch.Telegram.Enabled = strings.TrimSpace(ref) != ""
			return nil
		},
	},
	"discord": {
		ID:    "discord",
		Label: "Discord",
		Blurb: "Discord gateway bot token.",
		IsConfigured: func(ch *appcfg.ChannelsSection) bool {
			return ch != nil && ch.Discord.Enabled
		},
		Disable: func(ch *appcfg.ChannelsSection) {
			if ch != nil {
				ch.Discord.Enabled = false
			}
		},
		Configure: func(selector Selector, ch *appcfg.ChannelsSection, home string, agentID string) error {
			token, ok, err := promptRetainedInputWithDisplay(selector, "Discord bot token", ch.Discord.BotToken, hiddenEnvReferenceDefault(ch.Discord.BotToken))
			if err != nil || !ok {
				return err
			}
			ref, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, "DISCORD_BOT_TOKEN"), token)
			if err != nil {
				return err
			}
			ch.Discord.BotToken = ref
			ch.Discord.Enabled = strings.TrimSpace(ref) != ""
			return nil
		},
	},
	"slack": {
		ID:    "slack",
		Label: "Slack",
		Blurb: "Slack events inbound + bot outbound.",
		IsConfigured: func(ch *appcfg.ChannelsSection) bool {
			return ch != nil && ch.Slack.Enabled
		},
		Disable: func(ch *appcfg.ChannelsSection) {
			if ch != nil {
				ch.Slack.Enabled = false
			}
		},
		Configure: func(selector Selector, ch *appcfg.ChannelsSection, home string, agentID string) error {
			botToken, ok, err := promptRetainedInputWithDisplay(selector, "Slack bot token", ch.Slack.BotToken, hiddenEnvReferenceDefault(ch.Slack.BotToken))
			if err != nil || !ok {
				return err
			}
			botRef, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, "SLACK_BOT_TOKEN"), botToken)
			if err != nil {
				return err
			}
			secret, ok, err := promptRetainedInputWithDisplay(selector, "Slack signing secret (optional)", ch.Slack.Secret, hiddenEnvReferenceDefault(ch.Slack.Secret))
			if err != nil || !ok {
				return err
			}
			secretRef, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, "SLACK_SECRET"), secret)
			if err != nil {
				return err
			}
			path, ok, err := selector.Input("Slack inbound path", defaultString(ch.Slack.InboundPath, "/channels/slack/inbound"))
			if err != nil || !ok {
				return err
			}
			ch.Slack.BotToken = botRef
			ch.Slack.Secret = secretRef
			ch.Slack.InboundPath = strings.TrimSpace(path)
			ch.Slack.Enabled = strings.TrimSpace(botRef) != ""
			return nil
		},
	},
	"whatsapp": {
		ID:    "whatsapp",
		Label: "WhatsApp",
		Blurb: "Bridge-backed inbound/outbound channel.",
		IsConfigured: func(ch *appcfg.ChannelsSection) bool {
			return ch != nil && ch.WhatsApp.Enabled
		},
		Disable: func(ch *appcfg.ChannelsSection) {
			if ch != nil {
				ch.WhatsApp.Enabled = false
			}
		},
		Configure: func(selector Selector, ch *appcfg.ChannelsSection, home string, agentID string) error {
			outbound, ok, err := selector.Input("WhatsApp outbound URL", defaultString(ch.WhatsApp.OutboundURL, "http://127.0.0.1:3000"))
			if err != nil || !ok {
				return err
			}
			token, ok, err := promptRetainedInputWithDisplay(selector, "WhatsApp token (optional)", ch.WhatsApp.Token, hiddenEnvReferenceDefault(ch.WhatsApp.Token))
			if err != nil || !ok {
				return err
			}
			tokenRef, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, "WHATSAPP_TOKEN"), token)
			if err != nil {
				return err
			}
			secret, ok, err := promptRetainedInputWithDisplay(selector, "WhatsApp secret (optional)", ch.WhatsApp.Secret, hiddenEnvReferenceDefault(ch.WhatsApp.Secret))
			if err != nil || !ok {
				return err
			}
			secretRef, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, "WHATSAPP_SECRET"), secret)
			if err != nil {
				return err
			}
			path, ok, err := selector.Input("WhatsApp inbound path", defaultString(ch.WhatsApp.InboundPath, "/channels/whatsapp/inbound"))
			if err != nil || !ok {
				return err
			}
			ch.WhatsApp.OutboundURL = strings.TrimSpace(outbound)
			ch.WhatsApp.Token = tokenRef
			ch.WhatsApp.Secret = secretRef
			ch.WhatsApp.InboundPath = strings.TrimSpace(path)
			ch.WhatsApp.Enabled = strings.TrimSpace(outbound) != ""
			return nil
		},
	},
	"signal": {
		ID:    "signal",
		Label: "Signal",
		Blurb: "Outbound HTTP bridge with polling/events.",
		IsConfigured: func(ch *appcfg.ChannelsSection) bool {
			return ch != nil && ch.Signal.Enabled
		},
		Disable: func(ch *appcfg.ChannelsSection) {
			if ch != nil {
				ch.Signal.Enabled = false
			}
		},
		Configure: func(selector Selector, ch *appcfg.ChannelsSection, home string, agentID string) error {
			outbound, ok, err := selector.Input("Signal outbound URL", ch.Signal.OutboundURL)
			if err != nil || !ok {
				return err
			}
			token, ok, err := promptRetainedInputWithDisplay(selector, "Signal token (optional)", ch.Signal.Token, hiddenEnvReferenceDefault(ch.Signal.Token))
			if err != nil || !ok {
				return err
			}
			ref, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, "SIGNAL_TOKEN"), token)
			if err != nil {
				return err
			}
			ch.Signal.OutboundURL = strings.TrimSpace(outbound)
			ch.Signal.Token = ref
			ch.Signal.Enabled = strings.TrimSpace(outbound) != ""
			return nil
		},
	},
	"mattermost": simpleOutboundTokenChannel(
		"mattermost",
		"Mattermost",
		"Mattermost outbound bridge.",
		func(ch *appcfg.ChannelsSection) *struct {
			Enabled     *bool
			OutboundURL *string
			Token       *string
		} {
			return &struct {
				Enabled     *bool
				OutboundURL *string
				Token       *string
			}{&ch.Mattermost.Enabled, &ch.Mattermost.OutboundURL, &ch.Mattermost.Token}
		},
		"MATTERMOST_TOKEN",
	),
	"matrix": simpleOutboundTokenChannel(
		"matrix",
		"Matrix",
		"Matrix outbound bridge.",
		func(ch *appcfg.ChannelsSection) *struct {
			Enabled     *bool
			OutboundURL *string
			Token       *string
		} {
			return &struct {
				Enabled     *bool
				OutboundURL *string
				Token       *string
			}{&ch.Matrix.Enabled, &ch.Matrix.OutboundURL, &ch.Matrix.Token}
		},
		"MATRIX_TOKEN",
	),
	"homeassistant": simpleOutboundTokenChannel(
		"homeassistant",
		"Home Assistant",
		"Home Assistant outbound bridge.",
		func(ch *appcfg.ChannelsSection) *struct {
			Enabled     *bool
			OutboundURL *string
			Token       *string
		} {
			return &struct {
				Enabled     *bool
				OutboundURL *string
				Token       *string
			}{&ch.HomeAssistant.Enabled, &ch.HomeAssistant.OutboundURL, &ch.HomeAssistant.Token}
		},
		"HOMEASSISTANT_TOKEN",
	),
	"email": {
		ID:    "email",
		Label: "Email",
		Blurb: "Inbound webhook + SMTP-like outbound bridge.",
		IsConfigured: func(ch *appcfg.ChannelsSection) bool {
			return ch != nil && ch.Email.Enabled
		},
		Disable: func(ch *appcfg.ChannelsSection) {
			if ch != nil {
				ch.Email.Enabled = false
			}
		},
		Configure: func(selector Selector, ch *appcfg.ChannelsSection, home string, agentID string) error {
			inbound, ok, err := selector.Input("Email inbound path", defaultString(ch.Email.InboundPath, "/channels/email/inbound"))
			if err != nil || !ok {
				return err
			}
			outbound, ok, err := selector.Input("Email outbound URL", ch.Email.OutboundURL)
			if err != nil || !ok {
				return err
			}
			token, ok, err := promptRetainedInputWithDisplay(selector, "Email token", ch.Email.Token, hiddenEnvReferenceDefault(ch.Email.Token))
			if err != nil || !ok {
				return err
			}
			tokenRef, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, "EMAIL_TOKEN"), token)
			if err != nil {
				return err
			}
			secret, ok, err := promptRetainedInputWithDisplay(selector, "Email secret", ch.Email.Secret, hiddenEnvReferenceDefault(ch.Email.Secret))
			if err != nil || !ok {
				return err
			}
			secretRef, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, "EMAIL_SECRET"), secret)
			if err != nil {
				return err
			}
			ch.Email.InboundPath = strings.TrimSpace(inbound)
			ch.Email.OutboundURL = strings.TrimSpace(outbound)
			ch.Email.Token = tokenRef
			ch.Email.Secret = secretRef
			ch.Email.Enabled = strings.TrimSpace(inbound) != "" || strings.TrimSpace(outbound) != ""
			return nil
		},
	},
	"sms": {
		ID:    "sms",
		Label: "SMS",
		Blurb: "Inbound webhook + Twilio-style outbound bridge.",
		IsConfigured: func(ch *appcfg.ChannelsSection) bool {
			return ch != nil && ch.SMS.Enabled
		},
		Disable: func(ch *appcfg.ChannelsSection) {
			if ch != nil {
				ch.SMS.Enabled = false
			}
		},
		Configure: func(selector Selector, ch *appcfg.ChannelsSection, home string, agentID string) error {
			inbound, ok, err := selector.Input("SMS inbound path", defaultString(ch.SMS.InboundPath, "/channels/sms/inbound"))
			if err != nil || !ok {
				return err
			}
			outbound, ok, err := selector.Input("SMS outbound URL", ch.SMS.OutboundURL)
			if err != nil || !ok {
				return err
			}
			token, ok, err := promptRetainedInputWithDisplay(selector, "SMS token", ch.SMS.Token, hiddenEnvReferenceDefault(ch.SMS.Token))
			if err != nil || !ok {
				return err
			}
			tokenRef, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, "SMS_TOKEN"), token)
			if err != nil {
				return err
			}
			secret, ok, err := promptRetainedInputWithDisplay(selector, "SMS secret", ch.SMS.Secret, hiddenEnvReferenceDefault(ch.SMS.Secret))
			if err != nil || !ok {
				return err
			}
			secretRef, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, "SMS_SECRET"), secret)
			if err != nil {
				return err
			}
			ch.SMS.InboundPath = strings.TrimSpace(inbound)
			ch.SMS.OutboundURL = strings.TrimSpace(outbound)
			ch.SMS.Token = tokenRef
			ch.SMS.Secret = secretRef
			ch.SMS.Enabled = strings.TrimSpace(inbound) != "" || strings.TrimSpace(outbound) != ""
			return nil
		},
	},
	"webhook": inboundOutboundSecretChannel(
		"webhook",
		"Webhook",
		"Generic inbound/outbound webhook bridge.",
		func(ch *appcfg.ChannelsSection) *struct {
			Enabled     *bool
			InboundPath *string
			OutboundURL *string
			Token       *string
			Secret      *string
		} {
			return &struct {
				Enabled     *bool
				InboundPath *string
				OutboundURL *string
				Token       *string
				Secret      *string
			}{&ch.Webhook.Enabled, &ch.Webhook.InboundPath, &ch.Webhook.OutboundURL, &ch.Webhook.Token, &ch.Webhook.Secret}
		},
		"/channels/webhook/inbound",
		"WEBHOOK_TOKEN",
		"WEBHOOK_SECRET",
	),
	"bluebubbles": inboundOutboundSecretChannel(
		"bluebubbles",
		"BlueBubbles",
		"BlueBubbles inbound/outbound bridge.",
		func(ch *appcfg.ChannelsSection) *struct {
			Enabled     *bool
			InboundPath *string
			OutboundURL *string
			Token       *string
			Secret      *string
		} {
			return &struct {
				Enabled     *bool
				InboundPath *string
				OutboundURL *string
				Token       *string
				Secret      *string
			}{&ch.BlueBubbles.Enabled, &ch.BlueBubbles.InboundPath, &ch.BlueBubbles.OutboundURL, &ch.BlueBubbles.Token, &ch.BlueBubbles.Secret}
		},
		"/channels/bluebubbles/inbound",
		"BLUEBUBBLES_TOKEN",
		"BLUEBUBBLES_SECRET",
	),
	"wecom":          wecomChannelMeta("wecom", "WeCom", false),
	"wecom_callback": wecomChannelMeta("wecom_callback", "WeCom Callback", true),
	"weixin": {
		ID:    "weixin",
		Label: "Weixin",
		Blurb: "Weixin bot bridge.",
		IsConfigured: func(ch *appcfg.ChannelsSection) bool {
			return ch != nil && ch.Weixin.Enabled
		},
		Disable: func(ch *appcfg.ChannelsSection) {
			if ch != nil {
				ch.Weixin.Enabled = false
			}
		},
		Configure: func(selector Selector, ch *appcfg.ChannelsSection, home string, agentID string) error {
			token, ok, err := promptRetainedInputWithDisplay(selector, "Weixin token", ch.Weixin.Token, hiddenEnvReferenceDefault(ch.Weixin.Token))
			if err != nil || !ok {
				return err
			}
			tokenRef, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, "WEIXIN_TOKEN"), token)
			if err != nil {
				return err
			}
			accountID, ok, err := selector.Input("Weixin account ID", ch.Weixin.AccountID)
			if err != nil || !ok {
				return err
			}
			baseURL, ok, err := selector.Input("Weixin base URL", defaultString(ch.Weixin.BaseURL, "https://ilinkai.weixin.qq.com"))
			if err != nil || !ok {
				return err
			}
			ch.Weixin.Token = tokenRef
			ch.Weixin.AccountID = strings.TrimSpace(accountID)
			ch.Weixin.BaseURL = strings.TrimSpace(baseURL)
			ch.Weixin.Enabled = strings.TrimSpace(tokenRef) != ""
			return nil
		},
	},
	"feishu": {
		ID:    "feishu",
		Label: "Feishu",
		Blurb: "Feishu app websocket mode.",
		IsConfigured: func(ch *appcfg.ChannelsSection) bool {
			return ch != nil && ch.Feishu.Enabled
		},
		Disable: func(ch *appcfg.ChannelsSection) {
			if ch != nil {
				ch.Feishu.Enabled = false
			}
		},
		Configure: func(selector Selector, ch *appcfg.ChannelsSection, home string, agentID string) error {
			appID, ok, err := selector.Input("Feishu app ID", ch.Feishu.AppID)
			if err != nil || !ok {
				return err
			}
			secret, ok, err := promptRetainedInputWithDisplay(selector, "Feishu app secret", ch.Feishu.AppSecret, hiddenEnvReferenceDefault(ch.Feishu.AppSecret))
			if err != nil || !ok {
				return err
			}
			secretRef, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, "FEISHU_APP_SECRET"), secret)
			if err != nil {
				return err
			}
			domain, ok, err := selector.Input("Feishu domain", defaultString(ch.Feishu.Domain, "feishu"))
			if err != nil || !ok {
				return err
			}
			mode, ok, err := selector.Input("Feishu connection mode", defaultString(ch.Feishu.ConnectionMode, "websocket"))
			if err != nil || !ok {
				return err
			}
			ch.Feishu.AppID = strings.TrimSpace(appID)
			ch.Feishu.AppSecret = secretRef
			ch.Feishu.Domain = strings.TrimSpace(domain)
			ch.Feishu.ConnectionMode = strings.TrimSpace(mode)
			ch.Feishu.Enabled = strings.TrimSpace(appID) != "" && strings.TrimSpace(secretRef) != ""
			return nil
		},
	},
	"dingtalk": {
		ID:    "dingtalk",
		Label: "Dingtalk",
		Blurb: "Dingtalk bot channel.",
		IsConfigured: func(ch *appcfg.ChannelsSection) bool {
			return ch != nil && ch.Dingtalk.Enabled
		},
		Disable: func(ch *appcfg.ChannelsSection) {
			if ch != nil {
				ch.Dingtalk.Enabled = false
			}
		},
		Configure: func(selector Selector, ch *appcfg.ChannelsSection, home string, agentID string) error {
			clientID, ok, err := promptRetainedInputWithDisplay(selector, "Dingtalk client ID", ch.Dingtalk.ClientID, hiddenEnvReferenceDefault(ch.Dingtalk.ClientID))
			if err != nil || !ok {
				return err
			}
			secret, ok, err := promptRetainedInputWithDisplay(selector, "Dingtalk client secret", ch.Dingtalk.ClientSecret, hiddenEnvReferenceDefault(ch.Dingtalk.ClientSecret))
			if err != nil || !ok {
				return err
			}
			secretRef, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, "DINGTALK_CLIENT_SECRET"), secret)
			if err != nil {
				return err
			}
			ch.Dingtalk.ClientID = strings.TrimSpace(clientID)
			ch.Dingtalk.ClientSecret = secretRef
			ch.Dingtalk.Enabled = strings.TrimSpace(clientID) != "" && strings.TrimSpace(secretRef) != ""
			return nil
		},
	},
	"qq": {
		ID:    "qq",
		Label: "QQ",
		Blurb: "QQ bot channel.",
		IsConfigured: func(ch *appcfg.ChannelsSection) bool {
			return ch != nil && ch.QQ.Enabled
		},
		Disable: func(ch *appcfg.ChannelsSection) {
			if ch != nil {
				ch.QQ.Enabled = false
			}
		},
		Configure: func(selector Selector, ch *appcfg.ChannelsSection, home string, agentID string) error {
			appID, ok, err := selector.Input("QQ app ID", ch.QQ.AppID)
			if err != nil || !ok {
				return err
			}
			secret, ok, err := promptRetainedInputWithDisplay(selector, "QQ client secret", ch.QQ.ClientSecret, hiddenEnvReferenceDefault(ch.QQ.ClientSecret))
			if err != nil || !ok {
				return err
			}
			secretRef, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, "QQ_CLIENT_SECRET"), secret)
			if err != nil {
				return err
			}
			ch.QQ.AppID = strings.TrimSpace(appID)
			ch.QQ.ClientSecret = secretRef
			ch.QQ.Enabled = strings.TrimSpace(appID) != "" && strings.TrimSpace(secretRef) != ""
			return nil
		},
	},
}

func configuredChannelLabels(ch *appcfg.ChannelsSection) []string {
	out := make([]string, 0, len(channelOrder))
	for _, id := range channelOrder {
		meta := channelMetaByID[id]
		if meta.IsConfigured(ch) {
			out = append(out, meta.Label)
		}
	}
	return out
}

func defaultString(current string, fallback string) string {
	if strings.TrimSpace(current) != "" {
		return strings.TrimSpace(current)
	}
	return fallback
}

func promptRetainedInputWithDisplay(selector Selector, label string, current string, displayDefault string) (string, bool, error) {
	if selector == nil {
		return strings.TrimSpace(current), false, nil
	}
	value, ok, err := selector.Secret(label, displayDefault)
	if err != nil || !ok {
		return "", ok, err
	}
	if strings.TrimSpace(value) == "" {
		return strings.TrimSpace(current), true, nil
	}
	return strings.TrimSpace(value), true, nil
}

func hiddenEnvReferenceDefault(current string) string {
	current = strings.TrimSpace(current)
	if isEnvReferenceLike(current) {
		return ""
	}
	return current
}

func isEnvReferenceLike(v string) bool {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "${") || !strings.HasSuffix(v, "}") {
		return false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(v, "${"), "}")
	if name == "" {
		return false
	}
	for i, r := range name {
		isAlpha := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
		isDigitAfterFirst := i > 0 && r >= '0' && r <= '9'
		if r == '_' || isAlpha || isDigitAfterFirst {
			continue
		}
		return false
	}
	return true
}

func simpleOutboundTokenChannel(
	id string,
	label string,
	blurb string,
	target func(*appcfg.ChannelsSection) *struct {
		Enabled     *bool
		OutboundURL *string
		Token       *string
	},
	envName string,
) channelMeta {
	return channelMeta{
		ID:    id,
		Label: label,
		Blurb: blurb,
		IsConfigured: func(ch *appcfg.ChannelsSection) bool {
			return ch != nil && *target(ch).Enabled
		},
		Disable: func(ch *appcfg.ChannelsSection) {
			if ch != nil {
				*target(ch).Enabled = false
			}
		},
		Configure: func(selector Selector, ch *appcfg.ChannelsSection, home string, agentID string) error {
			entry := target(ch)
			outbound, ok, err := selector.Input(label+" outbound URL", *entry.OutboundURL)
			if err != nil || !ok {
				return err
			}
			token, ok, err := promptRetainedInputWithDisplay(selector, label+" token (optional)", *entry.Token, hiddenEnvReferenceDefault(*entry.Token))
			if err != nil || !ok {
				return err
			}
			ref, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, envName), token)
			if err != nil {
				return err
			}
			*entry.OutboundURL = strings.TrimSpace(outbound)
			*entry.Token = ref
			*entry.Enabled = strings.TrimSpace(outbound) != ""
			return nil
		},
	}
}

func inboundOutboundSecretChannel(
	id string,
	label string,
	blurb string,
	target func(*appcfg.ChannelsSection) *struct {
		Enabled     *bool
		InboundPath *string
		OutboundURL *string
		Token       *string
		Secret      *string
	},
	defaultPath string,
	tokenEnv string,
	secretEnv string,
) channelMeta {
	return channelMeta{
		ID:    id,
		Label: label,
		Blurb: blurb,
		IsConfigured: func(ch *appcfg.ChannelsSection) bool {
			return ch != nil && *target(ch).Enabled
		},
		Disable: func(ch *appcfg.ChannelsSection) {
			if ch != nil {
				*target(ch).Enabled = false
			}
		},
		Configure: func(selector Selector, ch *appcfg.ChannelsSection, home string, agentID string) error {
			entry := target(ch)
			inbound, ok, err := selector.Input(label+" inbound path", defaultString(*entry.InboundPath, defaultPath))
			if err != nil || !ok {
				return err
			}
			outbound, ok, err := selector.Input(label+" outbound URL", *entry.OutboundURL)
			if err != nil || !ok {
				return err
			}
			token, ok, err := promptRetainedInputWithDisplay(selector, label+" token (optional)", *entry.Token, hiddenEnvReferenceDefault(*entry.Token))
			if err != nil || !ok {
				return err
			}
			tokenRef, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, tokenEnv), token)
			if err != nil {
				return err
			}
			secret, ok, err := promptRetainedInputWithDisplay(selector, label+" secret (optional)", *entry.Secret, hiddenEnvReferenceDefault(*entry.Secret))
			if err != nil || !ok {
				return err
			}
			secretRef, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, secretEnv), secret)
			if err != nil {
				return err
			}
			*entry.InboundPath = strings.TrimSpace(inbound)
			*entry.OutboundURL = strings.TrimSpace(outbound)
			*entry.Token = tokenRef
			*entry.Secret = secretRef
			*entry.Enabled = strings.TrimSpace(inbound) != "" || strings.TrimSpace(outbound) != ""
			return nil
		},
	}
}

func wecomChannelMeta(id string, label string, callback bool) channelMeta {
	return channelMeta{
		ID:    id,
		Label: label,
		Blurb: "Enterprise WeCom callback channel.",
		IsConfigured: func(ch *appcfg.ChannelsSection) bool {
			if ch == nil {
				return false
			}
			if callback {
				return ch.WeComCallback.Enabled
			}
			return ch.WeCom.Enabled
		},
		Disable: func(ch *appcfg.ChannelsSection) {
			if ch == nil {
				return
			}
			if callback {
				ch.WeComCallback.Enabled = false
				return
			}
			ch.WeCom.Enabled = false
		},
		Configure: func(selector Selector, ch *appcfg.ChannelsSection, home string, agentID string) error {
			if ch == nil {
				return nil
			}
			var (
				token               *string
				aesKey              *string
				corpID              *string
				corpSecret          *string
				callbackPath        *string
				wecomAgentIDCurrent int64
				setEnabled          func(bool)
			)
			if callback {
				token = &ch.WeComCallback.Token
				aesKey = &ch.WeComCallback.EncodingAESKey
				corpID = &ch.WeComCallback.CorpID
				corpSecret = &ch.WeComCallback.CorpSecret
				callbackPath = &ch.WeComCallback.CallbackPath
				wecomAgentIDCurrent = ch.WeComCallback.AgentID
				setEnabled = func(v bool) { ch.WeComCallback.Enabled = v }
			} else {
				token = &ch.WeCom.Token
				aesKey = &ch.WeCom.EncodingAESKey
				corpID = &ch.WeCom.CorpID
				corpSecret = &ch.WeCom.CorpSecret
				callbackPath = &ch.WeCom.CallbackPath
				wecomAgentIDCurrent = ch.WeCom.AgentID
				setEnabled = func(v bool) { ch.WeCom.Enabled = v }
			}
			tokenValue, ok, err := promptRetainedInputWithDisplay(selector, label+" token", *token, hiddenEnvReferenceDefault(*token))
			if err != nil || !ok {
				return err
			}
			tokenRef, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, id+"_TOKEN"), tokenValue)
			if err != nil {
				return err
			}
			aesValue, ok, err := promptRetainedInputWithDisplay(selector, label+" encoding AES key", *aesKey, hiddenEnvReferenceDefault(*aesKey))
			if err != nil || !ok {
				return err
			}
			aesRef, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, id+"_ENCODING_AES_KEY"), aesValue)
			if err != nil {
				return err
			}
			corpIDValue, ok, err := selector.Input(label+" corp ID", *corpID)
			if err != nil || !ok {
				return err
			}
			corpSecretValue, ok, err := promptRetainedInputWithDisplay(selector, label+" corp secret", *corpSecret, hiddenEnvReferenceDefault(*corpSecret))
			if err != nil || !ok {
				return err
			}
			corpSecretRef, err := process.SecretConfigReferenceForOnboard(home, process.ChannelSecretEnvName(agentID, id+"_CORP_SECRET"), corpSecretValue)
			if err != nil {
				return err
			}
			callbackValue, ok, err := selector.Input(label+" callback path", defaultString(*callbackPath, callbackDefaultPath(id)))
			if err != nil || !ok {
				return err
			}
			agentIDValue, ok, err := selector.Input(label+" agent ID", strconv.FormatInt(wecomAgentIDCurrent, 10))
			if err != nil || !ok {
				return err
			}
			wecomAgentID, _ := strconv.ParseInt(strings.TrimSpace(agentIDValue), 10, 64)
			*token = tokenRef
			*aesKey = aesRef
			*corpID = strings.TrimSpace(corpIDValue)
			*corpSecret = corpSecretRef
			*callbackPath = strings.TrimSpace(callbackValue)
			if callback {
				ch.WeComCallback.AgentID = wecomAgentID
			} else {
				ch.WeCom.AgentID = wecomAgentID
			}
			setEnabled(strings.TrimSpace(tokenRef) != "" && strings.TrimSpace(corpIDValue) != "")
			return nil
		},
	}
}

func callbackDefaultPath(id string) string {
	if id == "wecom_callback" {
		return "/wecom_callback/callback"
	}
	return "/wecom/callback"
}

// ApprovalDisplayRecorder persists the approval question and its answer as
// display history, so a gate the user saw is still there after a resume. It is
// the sink's only persistence dependency, and a narrow one: the sink shows the
// request, so the sink is where "shown implies recorded" can be made
// unconditional.
//
// The recorded text is a display record, never an authority on the action's
// stored state - the sink records the decision before the action service
// applies it, and the two can diverge if the process dies in between.
type ApprovalDisplayRecorder interface {
	RecordApprovalGate(req turn.ToolApprovalRequest)
	RecordApprovalDecision(req turn.ToolApprovalRequest, decision turn.ToolApprovalDecision, confirmation string)
}

type ApprovalSink struct {
	out      io.Writer
	rawSel   *rawSelector
	renderer *Renderer
	recorder ApprovalDisplayRecorder
	// notify, when wired, reaches the main loop: an approval asks an open
	// slash panel to yield the composer area (PanelCloseMsg) before it paints.
	notify func(any)
}

// WithRecorder wires the display recorder. Optional: without it the sink is
// exactly as before (print-only), which is what the non-composer surfaces and
// the unit tests that exercise the overlay in isolation want.
func (s *ApprovalSink) WithRecorder(recorder ApprovalDisplayRecorder) *ApprovalSink {
	if s != nil {
		s.recorder = recorder
	}
	return s
}

// WithRenderer wires the renderer so post-decision text (the approval
// confirmation) is printed above the composer with a deterministic clear,
// instead of racing the working-status redraw. Optional; nil keeps the plain
// direct-write path used by tests and non-composer surfaces.
func (s *ApprovalSink) WithRenderer(r *Renderer) *ApprovalSink {
	if s != nil {
		s.renderer = r
	}
	return s
}

func NewInteractiveApprovalSinkWithTTY(out io.Writer, selector Selector) *ApprovalSink {
	if out == nil {
		out = io.Discard
	}
	sink := &ApprovalSink{out: out}
	if rs, ok := selector.(*rawSelector); ok {
		sink.rawSel = rs
	}
	return sink
}

func (s *ApprovalSink) PromptToolApproval(ctx context.Context, req turn.ToolApprovalRequest) (turn.ToolApprovalDecision, error) {
	select {
	case <-ctx.Done():
		return turn.ToolApprovalDecision{}, ctx.Err()
	default:
	}
	if s.rawSel == nil {
		return turn.ToolApprovalDecision{}, fmt.Errorf("tui approvals require an interactive raw terminal")
	}
	// A slash panel shares the composer area with the approval overlay: it
	// yields first, and the approval paints only once the main loop has taken
	// the panel down — otherwise closing the panel would erase the approval.
	if s.notify != nil && uiPanelActive.Load() {
		yielded := make(chan struct{})
		s.notify(PanelCloseMsg{Reason: "approval", Done: yielded})
		select {
		case <-yielded:
		case <-ctx.Done():
			return turn.ToolApprovalDecision{}, ctx.Err()
		}
	}
	if strings.TrimSpace(req.ActionKind) == "user_interaction" {
		overlay, err := newQuestionOverlay(ctx, s.out, s.rawSel, req)
		if err != nil {
			return turn.ToolApprovalDecision{}, err
		}
		// The gate is recorded before the question is drawn, so a user who
		// answers it and quits - or is interrupted while it is open - leaves
		// the fact that it was asked behind.
		if s.recorder != nil {
			s.recorder.RecordApprovalGate(req)
		}
		return overlay.Run()
	}
	overlay := newApprovalOverlay(ctx, s.out, s.rawSel, req, s.printApprovalConfirmation)
	if s.recorder != nil {
		s.recorder.RecordApprovalGate(req)
	}
	decision, err := overlay.Run()
	return decision, err
}

func buildRuleDecisionToDestination(req turn.ToolApprovalRequest, dst safety.PermissionDestination) (turn.ToolApprovalDecision, error) {
	toolName := strings.TrimSpace(req.PermissionToolName)
	if dst == "" {
		dst = req.SuggestedDestination
	}
	if dst == "" && len(req.DestinationOptions) > 0 {
		dst = req.DestinationOptions[0]
	}
	if dst == "" {
		dst = safety.DestinationLocalSettings
	}
	rule := strings.TrimSpace(selectRuleContent(req, dst))
	if toolName == "" || rule == "" {
		return turn.ToolApprovalDecision{}, fmt.Errorf("missing permission rule suggestion")
	}
	permissionRule := safety.PermissionRuleValue{
		ToolName: toolName, RuleContent: rule, BypassSandbox: req.BypassSandbox,
	}
	// A persistent command rule is a token prefix when one could be derived
	// from the command, and the command itself otherwise. Both are typed
	// fields: keeping the command in rule_content would hand it back to the
	// pattern parser, which reads a glob in it as a wildcard and would grant
	// every command that glob happens to match.
	if strings.EqualFold(safety.CanonicalToolName(toolName), "Bash") && dst != safety.DestinationSession {
		if prefix := safety.CommandPrefixFromRuleContent(rule); len(prefix) > 0 {
			permissionRule.CommandPrefix = prefix
		} else {
			permissionRule.Command = rule
		}
		permissionRule.RuleContent = ""
	}
	return turn.ToolApprovalDecision{
		Approved: true,
		Update: &safety.PermissionUpdate{
			Type:        safety.UpdateAddRules,
			Destination: dst,
			Behavior:    safety.BehaviorAllow,
			Rules:       []safety.PermissionRuleValue{permissionRule},
		},
	}, nil
}

func selectRuleContent(req turn.ToolApprovalRequest, dst safety.PermissionDestination) string {
	exact := strings.TrimSpace(req.ExactRuleContent)
	prefix := strings.TrimSpace(req.PrefixRuleContent)
	tool := strings.TrimSpace(req.PermissionToolName)
	// Persistent MCP approval applies to the current MCP tool across future
	// sessions, not the entire MCP server and not this single invocation only.
	if strings.HasPrefix(tool, "mcp__") && dst != safety.DestinationSession {
		if prefix != "" {
			return prefix
		}
	}
	// For persistent approvals, prefer the prefix pattern (e.g. "dir/*" for
	// file tools, "git:*" for Bash, "domain:host" for WebFetch) over the
	// exact input so future commands matching the prefix are auto-approved.
	if prefix != "" && dst != safety.DestinationSession {
		return prefix
	}
	if exact != "" {
		return exact
	}
	return prefix
}

func persistentDestination(req turn.ToolApprovalRequest) safety.PermissionDestination {
	if req.SuggestedDestination == safety.DestinationSession || req.SuggestedDestination == "" {
		for _, dst := range req.DestinationOptions {
			if dst == safety.DestinationLocalSettings || dst == safety.DestinationProjectSettings {
				return dst
			}
		}
		return safety.DestinationLocalSettings
	}
	return req.SuggestedDestination
}

// approvalConfirmationMaxLines caps every approval confirmation at three
// display lines, ellipsizing the gateway. The confirmations name model-authored
// content (a shell command, a host), which has no length bound of its own.
const approvalConfirmationMaxLines = 3

func (s *ApprovalSink) printApprovalConfirmation(req turn.ToolApprovalRequest, decision turn.ToolApprovalDecision) {
	if s == nil || s.out == nil {
		return
	}
	line, ok := approvalConfirmationLine(req, decision)
	if line == "" {
		return
	}
	// Recorded here, before the line reaches the terminal, so drawing and
	// persisting cannot be separated: every confirmFn call site inherits the
	// order, and a user who quits the moment the line appears still gets it
	// back from a resume.
	if s.recorder != nil {
		s.recorder.RecordApprovalDecision(req, decision, line)
	}
	symbol := "\x1b[31m✗\x1b[0m"
	if ok {
		symbol = "\x1b[32m✔\x1b[0m"
	}
	msg := strings.TrimSpace(strings.TrimPrefix(line, confirmationSymbol(ok)))
	if s.renderer != nil {
		// Clears the composer before printing, so the confirmation always
		// lands in scrollback above the repainted composer — no redraw race.
		// The request's AgentID decides which transcript it lands in: a
		// subagent's confirmation belongs in that subagent's view, beside the
		// call it authorises, not in the conversation.
		// A gate whose call holds no card has no parked block to sit above:
		// its line keeps producer order, right after the response that asked.
		// Both identities are checked because the line's own wording keys off
		// the tool name while a replayed record only carries the action kind.
		cardless := approvalGateHoldsNoCard(req.ActionKind) || approvalGateHoldsNoCard(approvalToolName(req))
		s.renderer.PrintApprovalConfirmation(req.AgentID, symbol+" "+msg, approvalConfirmationMaxLines, !cardless)
		return
	}
	// The renderer-less path (tests, non-composer surfaces) writes straight to
	// the terminal, so it has to apply the same wrap and cap itself rather than
	// inherit them. Lines are rejoined with CRLF because this path prints to a
	// raw-mode terminal, where a bare LF would stair-step the output.
	limited := limitVisualLines(wrapToolDisplayLine(symbol+" "+msg, "  "), approvalConfirmationMaxLines)
	_, _ = fmt.Fprintf(s.out, "\r\n%s\r\n\r\n", strings.ReplaceAll(limited, "\n", "\r\n"))
}

// approvalConfirmationLine is the single constructor of a confirmation line:
// the ✔/✗ glyph the decision resolves to, followed by the sentence, already
// cleaned for display. Live printing colorises the glyph and the recorder
// stores the plain one, so what a resume replays is the text the user read,
// and neither path can drift from the other's wording.
func approvalConfirmationLine(req turn.ToolApprovalRequest, decision turn.ToolApprovalDecision) (string, bool) {
	msg, ok := approvalConfirmationText(req, decision)
	if strings.TrimSpace(msg) == "" {
		return "", ok
	}
	return confirmationSymbol(ok) + " " + approvalConfirmationDisplayText(msg), ok
}

func confirmationSymbol(approved bool) string {
	if approved {
		return "✔"
	}
	return "✗"
}

// approvalConfirmationDisplayText makes a confirmation safe to print.
//
// The text interpolates rule content the model authored — a shell command, a
// host, a tool name — so it cannot be assumed inert. It is stripped of
// terminal control sequences and flattened to a single logical line, then
// cleared of format characters (zero-width spaces, bidi overrides) that can
// make a command read as something other than what was approved. Sanitizing
// at the sink rather than in the builder covers every branch of
// approvalConfirmationText and both print paths above.
//
// This also keeps the line cap honest: a surviving ESC makes the wrapper
// measure the message as one unbreakable token, so it emits a single enormous
// line that limitVisualLines then counts as being within budget.
func approvalConfirmationDisplayText(msg string) string {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, sanitizeTerminalInputText(msg))
	return strings.Join(strings.Fields(cleaned), " ")
}

func approvalConfirmationText(req turn.ToolApprovalRequest, decision turn.ToolApprovalDecision) (string, bool) {
	// Requesting a review decides nothing: the approval is still open, and the
	// confirmation says what was set in motion rather than what was allowed.
	if decision.RequestPlanReview != nil {
		return "You asked " + planReviewModelDisplay(
			decision.RequestPlanReview.Provider, decision.RequestPlanReview.Model,
		) + " to review the plan", true
	}

	// Plan-mode entry is an action-level approval, rather than permission to run
	// its JSON input. Match both the internal and canonical tool names and give
	// it precedence over persistent-rule updates so its confirmation is stable.
	if isApprovalEnterPlanMode(req) {
		switch {
		case decision.Approved:
			return "You approved forebrain to enter plan mode", true
		case decision.Denied:
			return "You did not approve forebrain to enter plan mode", false
		case decision.Cancelled:
			// Without this branch the cancellation fell through to the generic
			// wording below, which names the permission input - for plan mode
			// that is the tool's whole argument JSON, so the user was shown a
			// plan document as the thing they had just cancelled.
			return "You canceled forebrain's request to enter plan mode", false
		}
	}

	if isApprovalExitPlanMode(req) {
		switch {
		case decision.Approved:
			return "You approved forebrain to exit plan mode", true
		case decision.Denied:
			return "You did not approve forebrain to exit plan mode", false
		case decision.Cancelled:
			return "You canceled forebrain's request to exit plan mode", false
		}
	}
	if strings.EqualFold(approvalToolName(req), "request_permissions") {
		switch {
		case decision.Approved:
			return "You approved the requested permissions", true
		case decision.Denied:
			return "You did not approve the requested permissions", false
		}
	}
	if isApprovalMemoryNote(req) {
		filename, _, ok := approvalMemoryNoteInput(req)
		target := "this memory note"
		if ok {
			target = "memory note " + filename
		}
		switch {
		case decision.Approved:
			return "You approved saving " + target, true
		case decision.Denied:
			return "You did not approve saving " + target, false
		case decision.Cancelled:
			return "You canceled saving " + target, false
		}
	}
	// Memory queries carry their whole argument JSON as the permission input, so
	// the generic "run <input>" wording below would print that JSON back at the
	// user. Name the action being authorized instead.
	if action := memoryQueryApprovalAction(req); action != "" {
		switch {
		case decision.Approved:
			return "You approved " + action, true
		case decision.Denied:
			return "You did not approve " + action, false
		case decision.Cancelled:
			return "You canceled " + action, false
		}
	}
	if isApprovalWebSearch(req) {
		query := approvalWebSearchQuery(req)
		target := "this web search"
		if query != "" {
			target = "a web search for “" + query + "”"
		}
		switch {
		case decision.Approved:
			return "You approved " + target, true
		case decision.Denied, decision.Cancelled:
			return "You did not approve " + target, false
		}
	}
	if isApprovalRetrieveOutput(req) {
		return retrieveOutputApprovalConfirmationText(req, decision)
	}
	if isApprovalFileMutation(req) {
		return fileMutationApprovalConfirmationText(req, decision)
	}

	update := decision.Update
	if update == nil {
		target := approvalConfirmationTarget(req)
		switch {
		case decision.Approved:
			if target == "" {
				return "You approved this request this time", true
			}
			return "You approved forebrain to run " + target + " this time", true
		case decision.Denied:
			if target == "" {
				return "You did not approve this request", false
			}
			return "You did not approve forebrain to run " + target, false
		case decision.Cancelled:
			if target == "" {
				return "You canceled this request", false
			}
			return "You canceled the request to run " + target, false
		default:
			return "", false
		}
	}
	if update.Type != safety.UpdateAddRules || update.Behavior != safety.BehaviorAllow || len(update.Rules) == 0 {
		return "", false
	}
	// A command remembered as one rule per clause is still one command, so the
	// confirmation names that command and then the part of it left free to
	// vary. Reading only the first rule would name a fragment instead.
	if len(update.Rules) > 1 && approvalIsBash(req) {
		command := approvalBashCommand(req)
		if command == "" {
			return "", false
		}
		prefixes := safety.CommandApprovalRulePrefixes(update.Rules)
		if len(prefixes) == 0 {
			return "You approved forebrain to always run " + command, true
		}
		return "You approved forebrain to always run " + command + " or its " + strings.Join(prefixes, ", ") + " variants", true
	}
	// Which field the rule uses is what it grants, so the sentence reads it
	// rather than re-deriving the answer from the command.
	literalCommand := strings.TrimSpace(update.Rules[0].Command)
	commandPrefix := update.Rules[0].CommandPrefix
	rule := strings.TrimSpace(update.Rules[0].RuleContent)
	switch {
	case rule != "":
	case literalCommand != "":
		rule = literalCommand
	case len(commandPrefix) > 0:
		rule = strings.Join(commandPrefix, " ")
	default:
		rule = strings.TrimSpace(update.Rules[0].ToolName)
	}
	if rule == "" {
		return "", false
	}
	switch update.Destination {
	case safety.DestinationSession:
		return "You approved forebrain to run " + rule + " every time this session", true
	default:
		// A remembered host is not a command; "run domain:example.com" would
		// describe the wrong thing entirely.
		if host := strings.TrimSpace(strings.TrimPrefix(rule, "domain:")); host != rule && host != "" {
			return "You approved forebrain to always fetch " + host, true
		}
		// The confirmation spells the command out; printApprovalConfirmation
		// bounds it to approvalConfirmationMaxLines, so length is not this
		// function's problem.
		if len(commandPrefix) > 0 {
			return "You approved forebrain to always run commands that start with " + rule, true
		}
		return "You approved forebrain to always run " + rule, true
	}
}

func retrieveOutputApprovalConfirmationText(req turn.ToolApprovalRequest, decision turn.ToolApprovalDecision) (string, bool) {
	action := "retrieve saved tool output"
	if input, ok := approvalRetrieveOutputInputValue(req); ok {
		target := fmt.Sprintf("saved output #%d", input.ID)
		switch {
		case input.Query != "":
			action = "search " + target + " for “" + truncateForDisplay(input.Query, 96) + "”"
		case input.Lines != "":
			action = "read " + target + " lines " + truncateForDisplay(input.Lines, 32)
		default:
			action = "retrieve " + target
		}
	}
	switch {
	case decision.Approved:
		suffix := " this time"
		if decision.Update != nil {
			if decision.Update.Destination == safety.DestinationSession {
				suffix = " for this session"
			} else {
				suffix = " without asking again"
			}
		}
		return "You approved Forebrain Harness to " + action + suffix, true
	case decision.Denied:
		return "You did not approve Forebrain Harness to " + action, false
	case decision.Cancelled:
		return "You canceled the request to " + action, false
	default:
		return "", false
	}
}

func fileMutationApprovalConfirmationText(req turn.ToolApprovalRequest, decision turn.ToolApprovalDecision) (string, bool) {
	update := decision.Update
	if update == nil {
		target := approvalFileMutationTarget(req)
		switch {
		case decision.Approved:
			if target == "" {
				return "You approved this file change", true
			}
			return fmt.Sprintf("You approved this change to `%s`", target), true
		case decision.Denied:
			if target == "" {
				return "You did not approve this file change", false
			}
			return fmt.Sprintf("You did not approve this change to `%s`", target), false
		case decision.Cancelled:
			if target == "" {
				return "You canceled this file change", false
			}
			return fmt.Sprintf("You canceled this change to `%s`", target), false
		default:
			return "", false
		}
	}
	if update.Type != safety.UpdateAddRules || update.Behavior != safety.BehaviorAllow || len(update.Rules) == 0 {
		return "", false
	}
	rule := strings.TrimSpace(update.Rules[0].RuleContent)
	if rule == "" {
		rule = approvalFileMutationTarget(req)
	}
	if update.Destination == safety.DestinationSession {
		if rule == "" {
			return "You approved changes to this file for this session", true
		}
		return fmt.Sprintf("You approved changes to `%s` for this session", rule), true
	}
	if rule == "" {
		return "You approved this file change", true
	}
	if fileRuleIsPattern(req, rule) {
		return fmt.Sprintf("You approved changes to files matching `%s`", rule), true
	}
	return fmt.Sprintf("You approved changes to `%s`", rule), true
}

func approvalFileMutationTarget(req turn.ToolApprovalRequest) string {
	if input := strings.TrimSpace(req.PermissionInput); input != "" {
		return input
	}
	return strings.TrimSpace(req.ExactRuleContent)
}

func fileRuleIsPattern(req turn.ToolApprovalRequest, rule string) bool {
	prefix := strings.TrimSpace(req.PrefixRuleContent)
	if prefix != "" && prefix != "*" && rule == prefix {
		return true
	}
	return strings.ContainsAny(rule, "*?[")
}

func approvalConfirmationTarget(req turn.ToolApprovalRequest) string {
	if cmd := approvalBashCommand(req); cmd != "" {
		return cmd
	}
	if input := strings.TrimSpace(req.PermissionInput); input != "" {
		return input
	}
	if exact := strings.TrimSpace(req.ExactRuleContent); exact != "" {
		return exact
	}
	if prefix := strings.TrimSpace(req.PrefixRuleContent); prefix != "" {
		return prefix
	}
	if tool := approvalToolName(req); tool != "" {
		return tool
	}
	return ""
}

// truncate clips overlay body text (tool input JSON, an option description) to
// max terminal columns. Like truncateForDisplay it counts columns rather than
// bytes, and repairs invalid UTF-8, so a non-ASCII path or commit message in
// the approved input is not cut mid-character or drawn as a stray glyph on the
// very screen the user is reading to decide.
func truncate(s string, max int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	if max <= 0 {
		return s
	}
	return runewidth.Truncate(s, max, "...")
}

type SelectItem struct {
	Label       string
	Description string
	Category    string
	Disabled    bool
}

type Selector interface {
	Select(label string, options []string, defaultOption string) (string, bool, error)
	MultiSelect(label string, options []string, defaultOptions []string) ([]string, bool, error)
	Input(label string, defaultValue string) (string, bool, error)
	// Secret asks for a value that must not be shown, such as an API key.
	Secret(label string, defaultValue string) (string, bool, error)
	Confirm(label string, defaultValue bool) (bool, bool, error)
	// SelectRich opens on items[defaultIdx] — the setting in force, or the
	// answer given before on a page shown again — or on the first row when
	// defaultIdx names none (-1).
	SelectRich(label string, items []SelectItem, defaultIdx int) (int, bool, error)
	// Review shows facts to check and the actions to take on them, opening
	// on actions[defaultIdx] (the first when -1), and returns the index of
	// the action chosen.
	Review(label string, facts []turn.StatusFact, actions []string, defaultIdx int) (int, bool, error)
}

// PagedRichSelector is an optional Selector capability for pickers backed by a
// paginated store. When the cursor reaches the last loaded item, the picker
// calls fetchMore for the next page (nil or empty means exhausted) and appends
// the returned rows, so the whole history is browsable with ↓ while memory
// stays bounded by the pages actually visited. Implemented by Forebrain Harness's
// interactive selector without widening the general Selector interface used by
// callers; line-based selectors keep the plain SelectRich single-page behavior.
type PagedRichSelector interface {
	SelectRichPaged(label string, items []SelectItem, defaultIdx int, fetchMore func() []SelectItem) (int, bool, error)
}

// TabbedSelectOptions shapes a tabbed picker.
type TabbedSelectOptions struct {
	// DefaultIdx is the row the picker opens on, an index into items; one that
	// names no selectable row opens the first tab's first row.
	DefaultIdx int
	// Checked are the rows checked when a multi-select opens.
	Checked []int
	// Uncounted are tabs shown without a row count: a tab of actions rather
	// than of things.
	Uncounted []string
}

// TabbedRichSelector is an optional Selector capability for a long list that
// sorts into a few kinds: each item's Category is a tab, one tab is shown at a
// time, and every row is two columns — the label, then the description, each
// left-aligned, a fixed gap apart. Tabs come in the order their category first
// appears in items. Implemented by the interactive selector without widening
// Selector; selectRichTabbed and multiSelectRichTabbed fall back to the plain
// primitives for every other selector.
type TabbedRichSelector interface {
	// SelectRichTabbed returns the index into items of the row chosen.
	SelectRichTabbed(label string, items []SelectItem, opts TabbedSelectOptions) (int, bool, error)
	// MultiSelectRichTabbed returns the indices into items checked when the
	// user confirms, ascending.
	MultiSelectRichTabbed(label string, items []SelectItem, opts TabbedSelectOptions) ([]int, bool, error)
}

type MemorySettingsAction int

const (
	MemorySettingsCancel MemorySettingsAction = iota
	MemorySettingsSave
	// MemorySettingsResetProject clears only the current session's own
	// project scope — the common "forget what you learned about this repo"
	// ask, and the default of the two reset options.
	MemorySettingsResetProject
	// MemorySettingsResetAll clears every project plus global preferences.
	MemorySettingsResetAll
)

type MemorySettingsResult struct {
	Action           MemorySettingsAction
	UseMemories      bool
	GenerateMemories bool
}

// MemorySettingsSelector is an optional capability implemented by Forebrain Harness's
// selectors without widening the general Selector interface used by callers.
type MemorySettingsSelector interface {
	SelectMemorySettings(useMemories, generateMemories bool) (MemorySettingsResult, error)
}

// InfoOverlaySelector shows a read-only report as a dismissable overlay. It is
// optional for the same reason MemorySettingsSelector is: only the raw selector
// owns a terminal it can paint an overlay into, so callers fall back to writing
// the report into the transcript when a selector does not implement it.
type InfoOverlaySelector interface {
	ShowInfo(label string, body string) error
	// ConfirmInfo shows a report the user reads before deciding, with the
	// decision in the same block: true when they take action.
	ConfirmInfo(label string, body string, action string) (bool, error)
}

func NewSelector(out io.Writer, readLine func(context.Context) (string, error)) Selector {
	return &lineSelector{out: out, readLine: readLine}
}

func NewInteractiveSelector(out io.Writer, readLine func(context.Context) (string, error), in *os.File, renderer *Renderer) Selector {
	if in == nil {
		return &lineSelector{out: out, readLine: readLine}
	}
	return newRawSelector(in, renderer)
}

func compactNonEmpty(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item) == "" {
			continue
		}
		out = append(out, strings.TrimSpace(item))
	}
	return out
}

func sanitizeInteractiveOption(input string) string {
	replaced := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(input)
	parts := strings.Fields(replaced)
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

type lineSelector struct {
	out      io.Writer
	readLine func(context.Context) (string, error)
}

func (s *lineSelector) Select(label string, options []string, defaultOption string) (string, bool, error) {
	if len(options) == 0 || s.readLine == nil {
		return "", false, nil
	}
	if s.out != nil {
		if text := strings.TrimSpace(label); text != "" {
			writeSelectorLine(s.out, text)
		}
		for i, option := range options {
			writeSelectorLine(s.out, fmt.Sprintf("  %d) %s", i+1, option))
		}
		_, _ = fmt.Fprint(s.out, "> ")
	}
	line, err := s.readLine(context.Background())
	if err != nil {
		return "", false, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		if strings.TrimSpace(defaultOption) == "" {
			return "", false, nil
		}
		return strings.TrimSpace(defaultOption), true, nil
	}
	var idx int
	if _, scanErr := fmt.Sscanf(line, "%d", &idx); scanErr == nil {
		if idx >= 1 && idx <= len(options) {
			return options[idx-1], true, nil
		}
		return "", false, nil
	}
	for _, option := range options {
		if option == line {
			return option, true, nil
		}
	}
	return "", false, nil
}

func (s *lineSelector) MultiSelect(label string, options []string, defaultOptions []string) ([]string, bool, error) {
	if len(options) == 0 || s.readLine == nil {
		return nil, false, nil
	}
	defaultSet := make(map[string]struct{}, len(defaultOptions))
	for _, option := range defaultOptions {
		option = strings.TrimSpace(option)
		if option != "" {
			defaultSet[option] = struct{}{}
		}
	}
	if s.out != nil {
		if text := strings.TrimSpace(label); text != "" {
			writeSelectorLine(s.out, text)
		}
		for i, option := range options {
			mark := " "
			if _, ok := defaultSet[option]; ok {
				mark = "x"
			}
			writeSelectorLine(s.out, fmt.Sprintf("  %d) [%s] %s", i+1, mark, option))
		}
		writeSelectorLine(s.out, "Enter comma-separated numbers or exact labels. Empty input keeps defaults.")
		_, _ = fmt.Fprint(s.out, "> ")
	}
	line, err := s.readLine(context.Background())
	if err != nil {
		return nil, false, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return append([]string(nil), defaultOptions...), true, nil
	}
	parts := strings.Split(line, ",")
	selected := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var idx int
		if _, scanErr := fmt.Sscanf(part, "%d", &idx); scanErr == nil {
			if idx >= 1 && idx <= len(options) {
				option := options[idx-1]
				if _, ok := seen[option]; !ok {
					seen[option] = struct{}{}
					selected = append(selected, option)
				}
			}
			continue
		}
		for _, option := range options {
			if option != part {
				continue
			}
			if _, ok := seen[option]; ok {
				break
			}
			seen[option] = struct{}{}
			selected = append(selected, option)
			break
		}
	}
	return selected, true, nil
}

func (s *lineSelector) SelectMemorySettings(useMemories, generateMemories bool) (MemorySettingsResult, error) {
	if s == nil || s.readLine == nil {
		return MemorySettingsResult{Action: MemorySettingsCancel}, nil
	}
	for {
		if s.out != nil {
			writeSelectorLine(s.out, "Memories")
			writeSelectorLine(s.out, fmt.Sprintf("  1) [%s] Use memories — Use memories in following threads. Applied at next thread.", memoryCheckbox(useMemories)))
			writeSelectorLine(s.out, fmt.Sprintf("  2) [%s] Generate memories — Generate memories from following threads. Current thread included.", memoryCheckbox(generateMemories)))
			writeSelectorLine(s.out, "  3) Reset memories — Clear local memory files and summaries. Threads remain intact.")
			writeSelectorLine(s.out, "Enter 1 or 2 to toggle, s to save, 3 to reset, or q to cancel.")
			_, _ = fmt.Fprint(s.out, "> ")
		}
		line, err := s.readLine(context.Background())
		if err != nil {
			return MemorySettingsResult{}, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "1", "use", "use memories":
			useMemories = !useMemories
		case "2", "generate", "generate memories":
			generateMemories = !generateMemories
		case "", "s", "save":
			return MemorySettingsResult{Action: MemorySettingsSave, UseMemories: useMemories, GenerateMemories: generateMemories}, nil
		case "q", "quit", "cancel", "esc", "escape":
			return MemorySettingsResult{Action: MemorySettingsCancel}, nil
		case "3", "reset":
			if s.out != nil {
				writeSelectorLine(s.out, "Reset memories")
				writeSelectorLine(s.out, "  1) Reset this project's memories")
				writeSelectorLine(s.out, "  2) Reset everything (every project + global)")
				writeSelectorLine(s.out, "  3) Go back")
				_, _ = fmt.Fprint(s.out, "> ")
			}
			confirm, err := s.readLine(context.Background())
			if err != nil {
				return MemorySettingsResult{}, err
			}
			switch strings.ToLower(strings.TrimSpace(confirm)) {
			case "1", "reset this project's memories", "reset this project":
				return MemorySettingsResult{Action: MemorySettingsResetProject}, nil
			case "2", "reset everything (every project + global)", "reset everything":
				return MemorySettingsResult{Action: MemorySettingsResetAll}, nil
			}
		}
	}
}

func memoryCheckbox(value bool) string {
	if value {
		return "x"
	}
	return " "
}

func writeSelectorLine(out io.Writer, text string) {
	if out == nil {
		return
	}
	_, _ = fmt.Fprintf(out, "%s\r\n", text)
}

func (s *lineSelector) Input(label string, defaultValue string) (string, bool, error) {
	if s.readLine == nil {
		return "", false, nil
	}
	if s.out != nil {
		if text := strings.TrimSpace(label); text != "" {
			_, _ = fmt.Fprint(s.out, text+": ")
		}
	}
	line, err := s.readLine(context.Background())
	if err != nil {
		return "", false, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = strings.TrimSpace(defaultValue)
	}
	return line, true, nil
}

// Secret reads a secret like any other answer: piped input has no screen to
// hide it on.
func (s *lineSelector) Secret(label string, defaultValue string) (string, bool, error) {
	return s.Input(label, defaultValue)
}

func (s *lineSelector) Review(label string, facts []turn.StatusFact, actions []string, defaultIdx int) (int, bool, error) {
	if len(actions) == 0 {
		return -1, false, nil
	}
	if s.out != nil {
		if text := strings.TrimSpace(label); text != "" {
			writeSelectorLine(s.out, text)
		}
		for _, fact := range facts {
			writeSelectorLine(s.out, "  "+fact.Label+": "+fact.Value)
		}
	}
	def := actions[0]
	if defaultIdx >= 0 && defaultIdx < len(actions) {
		def = actions[defaultIdx]
	}
	chosen, ok, err := s.Select("", actions, def)
	if err != nil || !ok {
		return -1, ok, err
	}
	for i, action := range actions {
		if action == chosen {
			return i, true, nil
		}
	}
	return -1, false, nil
}

func (s *lineSelector) Confirm(label string, defaultValue bool) (bool, bool, error) {
	if s.readLine == nil {
		return false, false, nil
	}
	def := "y/N"
	if defaultValue {
		def = "Y/n"
	}
	if s.out != nil {
		_, _ = fmt.Fprintf(s.out, "%s (%s): ", strings.TrimSpace(label), def)
	}
	line, err := s.readLine(context.Background())
	if err != nil {
		return false, false, err
	}
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return defaultValue, true, nil
	}
	switch line {
	case "y", "yes", "true", "1":
		return true, true, nil
	case "n", "no", "false", "0":
		return false, true, nil
	default:
		return defaultValue, false, nil
	}
}

func (s *lineSelector) SelectRich(label string, items []SelectItem, defaultIdx int) (int, bool, error) {
	opts := make([]string, 0, len(items))
	idxMap := make([]int, 0, len(items))
	for i, item := range items {
		if item.Disabled {
			continue
		}
		opts = append(opts, item.Label)
		idxMap = append(idxMap, i)
	}
	if len(opts) == 0 {
		return -1, false, nil
	}
	defOpt := ""
	if defaultIdx >= 0 && defaultIdx < len(items) {
		defOpt = items[defaultIdx].Label
	}
	chosen, ok, err := s.Select(label, opts, defOpt)
	if err != nil || !ok {
		return -1, ok, err
	}
	for j, opt := range opts {
		if opt == chosen {
			return idxMap[j], true, nil
		}
	}
	return -1, false, nil
}
