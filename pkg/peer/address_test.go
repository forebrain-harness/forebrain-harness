package peer

import (
	"strings"
	"testing"
)

func TestEncodeUDS(t *testing.T) {
	if got := EncodeUDS("/tmp/cc-socks/49280.sock"); got != "uds:/tmp/cc-socks/49280.sock" {
		t.Fatalf("EncodeUDS = %q", got)
	}
	// Bytes outside the address set are percent-encoded, so a path with a
	// space and Chinese characters survives the trip.
	encoded := EncodeUDS("/tmp/a b/中文.sock")
	if want := "uds:/tmp/a%20b/%E4%B8%AD%E6%96%87.sock"; encoded != want {
		t.Fatalf("EncodeUDS = %q, want %q", encoded, want)
	}
	if !ValidAddress(encoded) {
		t.Fatalf("%q is not a valid address", encoded)
	}
	if !ValidAddress(EncodeUDS("/tmp/x%y.sock")) {
		t.Fatalf("%q is not a valid address", EncodeUDS("/tmp/x%y.sock"))
	}

	scheme, target := DecodeAddress(encoded)
	if scheme != "uds" || target != "/tmp/a b/中文.sock" {
		t.Fatalf("DecodeAddress = %q, %q", scheme, target)
	}
}

func TestDecodeAddressKeepsOtherSchemes(t *testing.T) {
	if scheme, target := DecodeAddress("bridge:abc"); scheme != "bridge" || target != "abc" {
		t.Fatalf("bridge = %q, %q", scheme, target)
	}
	if scheme, target := DecodeAddress("did:xyz"); scheme != "did" || target != "xyz" {
		t.Fatalf("did = %q, %q", scheme, target)
	}
	if scheme, target := DecodeAddress("/tmp/plain.sock"); scheme != "" || target != "" {
		t.Fatalf("plain path = %q, %q", scheme, target)
	}
}

func TestValidAddress(t *testing.T) {
	valid := []string{
		"uds:/tmp/cc-socks/49280.sock",
		"uds:/tmp/a%20b.sock",
		"bridge:something",
		"did:abc-123.x",
	}
	for _, addr := range valid {
		if !ValidAddress(addr) {
			t.Fatalf("%q was rejected", addr)
		}
	}
	invalid := []string{
		"",
		"uds:",
		"/tmp/cc-socks/49280.sock",
		"uds:/tmp/a b.sock",
		"tcp://host:1",
		"uds:" + strings.Repeat("a", 204),
	}
	for _, addr := range invalid {
		if ValidAddress(addr) {
			t.Fatalf("%q was accepted", addr)
		}
	}
}

func TestReplyTargetAllowed(t *testing.T) {
	const own = "/tmp/cc-socks/49280.sock"
	features := []string{replyAcrossDefaultDirs}
	uid := 501

	cases := []struct {
		name     string
		target   string
		own      string
		features []string
		uid      int
		want     bool
	}{
		{"same directory", "/tmp/cc-socks/50000.sock", own, nil, uid, true},
		{"same directory sidecar", "/tmp/cc-socks/49280-0a1b2c3d.sock", own, nil, uid, true},
		{"cc-socks default dir", "/tmp/cc-socks/50000.sock", "/run/user/501/cc-socks/1.sock", features, uid, true},
		{"cc-socks per-user dir", "/private/tmp/cc-socks-501/50000.sock", "/run/user/501/cc-socks/1.sock", features, uid, true},
		{"cc-socks wrong uid", "/tmp/cc-socks-777/50000.sock", "/run/user/501/cc-socks/1.sock", features, uid, false},
		{"fb-socks per-user dir", "/tmp/fb-socks-501/50000.sock", own, features, uid, true},
		{"fb-socks xdg dir", "/run/user/501/fb-socks/50000.sock", own, features, uid, true},
		{"fb-socks wrong uid", "/run/user/777/fb-socks/50000.sock", own, features, uid, false},
		{"unknown directory", "/tmp/elsewhere/50000.sock", own, features, uid, false},
		{"default dir without the feature", "/tmp/cc-socks-501/50000.sock", own, nil, uid, false},
		{"not a socket", "/tmp/cc-socks/50000.txt", own, features, uid, false},
		{"bad socket name in a default directory", "/tmp/cc-socks-501/notapid.sock", own, features, uid, false},
	}
	for _, testCase := range cases {
		got := ReplyTargetAllowed(testCase.target, testCase.own, testCase.uid, testCase.features)
		if got != testCase.want {
			t.Fatalf("%s: ReplyTargetAllowed(%q) = %v, want %v", testCase.name, testCase.target, got, testCase.want)
		}
	}
}
