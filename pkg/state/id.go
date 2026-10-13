package state

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const defaultPrefix = "chat"

var channelPrefixes = map[string]string{
	"webchat":        "web",
	"cli":            "cli",
	"tui":            "cli",
	"gateway":        "chat",
	"weixin":         "wx",
	"dingtalk":       "dd",
	"qq":             "qq",
	"feishu":         "fs",
	"wecom":          "wc",
	"wecom_callback": "wcc",
	"telegram":       "tg",
	"discord":        "dc",
	"slack":          "slk",
	"whatsapp":       "wa",
	"signal":         "sig",
	"matrix":         "mx",
	"mattermost":     "mm",
	"email":          "em",
	"sms":            "sms",
	"webhook":        "wh",
	"bluebubbles":    "bb",
	"homeassistant":  "ha",
}

func PrefixForChannel(channel string) string {
	if p := strings.TrimSpace(channelPrefixes[strings.ToLower(strings.TrimSpace(channel))]); p != "" {
		return p
	}
	return ""
}

func PrefixForSurface(surface string) string {
	switch strings.ToLower(strings.TrimSpace(surface)) {
	case "cli", "tui":
		return "cli"
	case "webchat":
		return "web"
	case "gateway":
		return "chat"
	default:
		return defaultPrefix
	}
}

func PrefixForSurfaceAndChannel(surface string, channel string) string {
	if p := PrefixForChannel(channel); p != "" {
		return p
	}
	if p := PrefixForSurface(surface); p != "" {
		return p
	}
	return defaultPrefix
}

func NewID(prefix string) string {
	p := sanitizePrefix(prefix)
	return fmt.Sprintf("%s-%s", p, uuid.NewString())
}

func NewForSurface(surface string, channel string) string {
	return NewID(PrefixForSurfaceAndChannel(surface, channel))
}

func WrapChannelSession(channelID, rawSessionID string) string {
	raw := strings.TrimSpace(rawSessionID)
	if raw == "" {
		return ""
	}
	prefix := sanitizePrefix(PrefixForChannel(channelID))
	if strings.HasPrefix(strings.ToLower(raw), strings.ToLower(prefix+"-")) {
		return raw
	}
	return prefix + "-" + raw
}

func UnwrapChannelSession(channelID, sessionID string) string {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return ""
	}
	prefix := sanitizePrefix(PrefixForChannel(channelID))
	want := prefix + "-"
	if strings.HasPrefix(strings.ToLower(sid), strings.ToLower(want)) {
		return strings.TrimSpace(sid[len(want):])
	}
	return sid
}

// WrapProjectChannelSession names the conversation a project's channel carries
// an inbound message in. The project is stamped between the channel prefix and
// the peer's own session id so two projects whose bots share a peer id never
// land in the same conversation. The project id is hex-encoded so the id stays
// reversible: it carries no '-', so the first '-' after the prefix delimits it
// and the rest is the peer's own session id.
func WrapProjectChannelSession(kind, projectID, rawSessionID string) string {
	raw := strings.TrimSpace(rawSessionID)
	if raw == "" {
		return ""
	}
	pid := strings.TrimSpace(projectID)
	if pid == "" {
		return WrapChannelSession(kind, raw)
	}
	prefix := sanitizePrefix(PrefixForChannel(kind))
	return prefix + "-" + hex.EncodeToString([]byte(pid)) + "-" + raw
}

// UnwrapProjectChannelSession recovers the project and the peer session id a
// project channel session was wrapped from. A session that was not wrapped for
// a project — no channel prefix, or the plain form WrapChannelSession writes —
// yields no project and the session id unchanged.
func UnwrapProjectChannelSession(kind, sessionID string) (projectID, rawSessionID string) {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return "", ""
	}
	want := sanitizePrefix(PrefixForChannel(kind)) + "-"
	if !strings.HasPrefix(strings.ToLower(sid), strings.ToLower(want)) {
		return "", sid
	}
	rest := sid[len(want):]
	sep := strings.IndexByte(rest, '-')
	if sep <= 0 {
		return "", sid
	}
	decoded, err := hex.DecodeString(rest[:sep])
	if err != nil {
		return "", sid
	}
	return string(decoded), rest[sep+1:]
}

func sanitizePrefix(prefix string) string {
	p := strings.TrimSpace(strings.ToLower(prefix))
	if p == "" {
		return defaultPrefix
	}
	return p
}
