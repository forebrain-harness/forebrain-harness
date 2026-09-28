package state

import (
	"strings"
	"testing"
)

func TestPrefixForSurfaceAndChannel(t *testing.T) {
	tests := []struct {
		surface string
		channel string
		want    string
	}{
		{surface: "cli", want: "cli"},
		{surface: "tui", want: "cli"},
		{surface: "tui", want: "cli"},
		{surface: "webchat", want: "web"},
		{surface: "gateway", channel: "weixin", want: "wx"},
		{surface: "gateway", channel: "dingtalk", want: "dd"},
		{surface: "gateway", channel: "qq", want: "qq"},
		{surface: "gateway", channel: "feishu", want: "fs"},
	}
	for _, tt := range tests {
		if got := PrefixForSurfaceAndChannel(tt.surface, tt.channel); got != tt.want {
			t.Fatalf("PrefixForSurfaceAndChannel(%q,%q)=%q want %q", tt.surface, tt.channel, got, tt.want)
		}
	}
}

func TestWrapAndUnwrapChannelSession(t *testing.T) {
	if got := WrapChannelSession("weixin", "user:u1"); got != "wx-user:u1" {
		t.Fatalf("WrapChannelSession weixin=%q", got)
	}
	if got := WrapChannelSession("weixin", ""); got != "" {
		t.Fatalf("WrapChannelSession empty=%q", got)
	}
	if got := UnwrapChannelSession("weixin", "wx-user:u1"); got != "user:u1" {
		t.Fatalf("UnwrapChannelSession weixin=%q", got)
	}
	if got := WrapChannelSession("qq", "group:g1"); got != "qq-group:g1" {
		t.Fatalf("WrapChannelSession qq=%q", got)
	}
	if got := UnwrapChannelSession("qq", "qq-group:g1"); got != "group:g1" {
		t.Fatalf("UnwrapChannelSession qq=%q", got)
	}
	if got := NewID("cli"); !strings.HasPrefix(got, "cli-") {
		t.Fatalf("NewID(cli)=%q", got)
	}
}
