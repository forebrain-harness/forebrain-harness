package channel

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

// AccountKey is the identity of the bot account a project's channel speaks as,
// used to keep one account out of two projects. It is the kind plus the
// credential that names the account, hashed so the state database never holds
// a plaintext secret. A channel that carries no account identity yet — a kind
// whose credential is blank — returns "", which the store does not constrain,
// so a half-configured channel can be saved without claiming an account.
//
// Adding a channel kind means adding its identity here: without one, the
// uniqueness that keeps one bot in one project is inert for that kind.
func AccountKey(kind string, ch appcfg.ChannelsSection) string {
	id := accountIdentity(kind, ch)
	if id == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(kind) + "\x00" + id))
	return strings.TrimSpace(kind) + ":" + hex.EncodeToString(sum[:16])
}

// accountIdentity names the account one kind's section speaks as.
func accountIdentity(kind string, ch appcfg.ChannelsSection) string {
	switch strings.TrimSpace(kind) {
	case "telegram":
		return strings.TrimSpace(ch.Telegram.BotToken)
	case "discord":
		return strings.TrimSpace(ch.Discord.BotToken)
	case "slack":
		return strings.TrimSpace(ch.Slack.BotToken)
	case "whatsapp":
		return strings.TrimSpace(ch.WhatsApp.Token)
	case "signal":
		return strings.TrimSpace(ch.Signal.Token)
	case "mattermost":
		return strings.TrimSpace(ch.Mattermost.Token)
	case "matrix":
		return strings.TrimSpace(ch.Matrix.Token)
	case "homeassistant":
		return strings.TrimSpace(ch.HomeAssistant.Token)
	case "email":
		return strings.TrimSpace(ch.Email.Token)
	case "sms":
		return strings.TrimSpace(ch.SMS.Token)
	case "webhook":
		return strings.TrimSpace(ch.Webhook.Token)
	case "bluebubbles":
		return strings.TrimSpace(ch.BlueBubbles.Token)
	case "wecom":
		return wecomIdentity(ch.WeCom.CorpID, ch.WeCom.CorpSecret, ch.WeCom.AgentID)
	case "wecom_callback":
		return wecomIdentity(ch.WeComCallback.CorpID, ch.WeComCallback.CorpSecret, ch.WeComCallback.AgentID)
	case "weixin":
		return strings.TrimSpace(ch.Weixin.AccountID)
	case "feishu":
		return strings.TrimSpace(ch.Feishu.AppID)
	case "dingtalk":
		return strings.TrimSpace(ch.Dingtalk.ClientID)
	case "qq":
		return strings.TrimSpace(ch.QQ.AppID)
	default:
		return ""
	}
}

func wecomIdentity(corpID, corpSecret string, agentID int64) string {
	corp := strings.TrimSpace(corpID)
	if corp == "" {
		corp = strings.TrimSpace(corpSecret)
	}
	if corp == "" {
		return ""
	}
	return corp + "|" + strconv.FormatInt(agentID, 10)
}
