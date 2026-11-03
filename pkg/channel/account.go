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

// LeaseName is the fb_leases row one account's long connection is held behind:
// the "channel:" prefix over the same account identity AccountKey computes, so
// two projects sharing one bot share one lease too — an account can only be
// connected once. It returns "" for a kind that keeps no connection, and for a
// channel whose credential is blank (AccountKey's ""), because a channel that
// cannot connect has no lease to hold.
func LeaseName(kind string, ch appcfg.ChannelsSection) string {
	if !NeedsLease(kind) {
		return ""
	}
	account := AccountKey(kind, ch)
	if account == "" {
		return ""
	}
	return "channel:" + account
}

// NeedsLease reports whether a kind's adapter keeps a long connection or a
// polling loop of its own, which is what makes it a second receiver when every
// replica of a shared database starts one. A webhook or HTTP-bridge adapter is
// the opposite: the load balancer delivers each message to exactly one replica,
// so it needs no lease.
//
// The classification, adapter by adapter:
//
//	kind             takes a lease   why
//	telegram         yes             long-polling loop against the Bot API
//	discord          yes             gateway websocket loop
//	mattermost       yes             loop over the bot's connection
//	matrix           yes             sync loop
//	homeassistant    yes             polling loop
//	signal           yes             event loop over the signal-cli REST socket
//	weixin           yes             runMonitor loop
//	dingtalk         yes             websocket bridge
//	feishu           yes             websocket bridge, in its default websocket mode
//	qq               yes             websocket bridge
//	whatsapp         yes             poll loop, beside the webhook route it also mounts
//	slack            no              Events API over HTTP; the adapter only mounts routes
//	webhook          no              HTTP bridge; the adapter only mounts routes
//	http_bridge      no              HTTP bridge; the adapter only mounts routes
//	bluebubbles      no              inbound path only
//	email            no              inbound path only
//	sms              no              inbound path only
//	wecom            no              callback routes only
//
// A kind that is neither — a half-configured one, or a new adapter — takes no
// lease, which is the safe default: it mounts routes the way a webhook does
// rather than silently connecting twice.
func NeedsLease(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "telegram", "discord", "mattermost", "matrix", "homeassistant",
		"signal", "weixin", "dingtalk", "feishu", "qq", "whatsapp":
		return true
	default:
		return false
	}
}
