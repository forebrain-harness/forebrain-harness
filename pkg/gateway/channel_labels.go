package gateway

// channel_labels.go names every channel kind the way the web pages name it.
//
// The channel table and the channel editor show a kind by its display name, and
// the table is searched on the server, so the server has to know the same names
// the page draws. The single source of those names is
// frontend/src/lib/channelLabels.json: the frontend reads it to draw and this
// file mirrors it to search, and TestChannelLabelsMatchFrontendJSON fails the
// build the moment the two disagree. Adding a kind means adding it to
// channel.ChannelKinds, to that JSON file and to this map.

// channelLabels is {kind: {zh, en}} — the display name in each of the web's two
// languages, in the order the JSON file lists them.
var channelLabels = map[string][2]string{
	"telegram":       {"Telegram", "Telegram"},
	"discord":        {"Discord", "Discord"},
	"slack":          {"Slack", "Slack"},
	"whatsapp":       {"WhatsApp", "WhatsApp"},
	"signal":         {"Signal", "Signal"},
	"mattermost":     {"Mattermost", "Mattermost"},
	"matrix":         {"Matrix", "Matrix"},
	"homeassistant":  {"Home Assistant", "Home Assistant"},
	"email":          {"邮件", "Email"},
	"sms":            {"短信", "SMS"},
	"webhook":        {"Webhook", "Webhook"},
	"bluebubbles":    {"BlueBubbles", "BlueBubbles"},
	"wecom":          {"企业微信", "WeCom"},
	"wecom_callback": {"企业微信（回调）", "WeCom callback"},
	"weixin":         {"微信", "WeChat"},
	"feishu":         {"飞书", "Feishu"},
	"dingtalk":       {"钉钉", "DingTalk"},
	"qq":             {"QQ", "QQ"},
}

// channelLabel returns a kind's display name in each of the web's two
// languages. A kind the table does not know reads as itself, which is what the
// page shows for it too.
func channelLabel(kind string) (zh, en string) {
	if names, ok := channelLabels[kind]; ok {
		return names[0], names[1]
	}
	return kind, kind
}
