// Gateway auth and secret settings.
package config

import (
	"strings"
)

func gatewayWebhookInboundPaths(ch ChannelsSection) []string {
	var out []string
	add := func(enabled bool, p string) {
		if !enabled {
			return
		}
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		out = append(out, p)
	}
	add(ch.Webhook.Enabled, ch.Webhook.InboundPath)
	add(ch.BlueBubbles.Enabled, ch.BlueBubbles.InboundPath)
	add(ch.SMS.Enabled, ch.SMS.InboundPath)
	add(ch.Slack.Enabled, ch.Slack.InboundPath)
	add(ch.Email.Enabled, ch.Email.InboundPath)
	add(ch.WhatsApp.Enabled, ch.WhatsApp.InboundPath)
	add(ch.WeCom.Enabled, ch.WeCom.CallbackPath)
	add(ch.WeComCallback.Enabled, ch.WeComCallback.CallbackPath)
	return out
}

// GatewayControlPlaneAuthExemptPath reports whether a request may skip
// control-plane auth. Beyond the health probes, the exempt set is the inbound
// webhook paths of the active primary agent's channels — those endpoints
// authenticate their caller themselves (signature, channel token), and only
// the active agent's channels are mounted, so naming the agent here keeps the
// exemption and the routing table describing the same set of paths.
func GatewayControlPlaneAuthExemptPath(path string, r *Root, agentID string) bool {
	p := strings.TrimSpace(path)
	if p == "/health" || p == "/healthz" || p == "/readyz" {
		return true
	}
	if strings.HasPrefix(p, "/channels/") && strings.HasSuffix(p, "/health") {
		return true
	}
	for _, x := range gatewayWebhookInboundPaths(ChannelsForAgent(r, agentID)) {
		if p == strings.TrimSpace(x) {
			return true
		}
	}
	return false
}

func GatewayAllowsAnonymousGET(path string) bool {
	p := strings.TrimSpace(path)
	if p == "" {
		return false
	}
	if p == "/ws/chat" {
		return false
	}
	if strings.HasPrefix(p, "/api") || strings.HasPrefix(p, "/v1") {
		return false
	}
	return true
}
