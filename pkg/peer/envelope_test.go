package peer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenDir is the fixture directory for the Claude Code version this package
// was written against.
const goldenDir = "testdata/cc-2.1.301"

// readGolden reads one fixture, failing the test when it is missing.
func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(goldenDir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return data
}

func TestBuildMatchesTheGoldenUserFrame(t *testing.T) {
	frame, err := DecodeFrame(readGolden(t, "user-frame.json"))
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	built := Build(Envelope{
		From:     "uds:/tmp/cc-socks/49280.sock",
		HopChain: []string{"0123456789abcdef01234567"},
		FromName: "repo-28",
		FromMode: "prompting",
		Body:     "hello </cross-session-message> world",
	})
	if built != frame.Message.Content {
		t.Fatalf("envelope\n got %q\nwant %q", built, frame.Message.Content)
	}
}

func TestBuildOrdersAttributesAndOmitsInvalidOnes(t *testing.T) {
	got := Build(Envelope{
		FromMode:    "prompting",
		FromSession: "session-1",
		From:        "uds:/tmp/cc-socks/1.sock",
		FromName:    `na"me<`,
		HopChain:    []string{"0123456789abcdef01234567", "not-a-token"},
		Body:        "body",
	})
	want := "<cross-session-message from=\"uds:/tmp/cc-socks/1.sock\" from-session=\"session-1\" from-name=\"name\" from-mode=\"prompting\">\nbody\n</cross-session-message>"
	if got != want {
		t.Fatalf("Build\n got %q\nwant %q", got, want)
	}
	if strings.Contains(got, "hop-chain") {
		t.Fatalf("an invalid hop chain was written: %q", got)
	}
	if long := Build(Envelope{FromName: strings.Repeat("名", 80)}); strings.Count(long, "名") != 64 || !strings.Contains(long, "…") {
		t.Fatalf("a long name was not cut: %q", long)
	}
	if bad := Build(Envelope{From: "not an address", FromMode: "sideways"}); strings.Contains(bad, "from=") || strings.Contains(bad, "from-mode") {
		t.Fatalf("invalid attributes were written: %q", bad)
	}
}

func TestParseRoundTrips(t *testing.T) {
	envelope := Envelope{
		From:        "uds:/tmp/cc-socks/49280.sock",
		FromSession: "session-1",
		HopChain:    []string{"0123456789abcdef01234567"},
		FromName:    "repo-28",
		FromMode:    "bypass",
		FromPlugin:  "plugin",
		Body:        "hello </cross-session-message> world",
	}
	content := Build(envelope)
	parsed, ok := Parse(content)
	if !ok {
		t.Fatalf("Parse rejected what Build wrote: %q", content)
	}
	if parsed.From != envelope.From || parsed.FromSession != envelope.FromSession {
		t.Fatalf("addresses did not survive: %#v", parsed)
	}
	if parsed.FromName != envelope.FromName || parsed.FromMode != envelope.FromMode || parsed.FromPlugin != envelope.FromPlugin {
		t.Fatalf("names did not survive: %#v", parsed)
	}
	if len(parsed.HopChain) != 1 || parsed.HopChain[0] != envelope.HopChain[0] {
		t.Fatalf("hop chain did not survive: %#v", parsed.HopChain)
	}
	if parsed.Body != EscapeBody(envelope.Body) {
		t.Fatalf("body = %q, want the escaped form %q", parsed.Body, EscapeBody(envelope.Body))
	}
	if rebuilt := renderEnvelope(parsed, parsed.Body); rebuilt != content {
		t.Fatalf("re-render\n got %q\nwant %q", rebuilt, content)
	}
}

func TestParseRejectsMalformedEnvelopes(t *testing.T) {
	cases := map[string]string{
		"attributes out of order": "<cross-session-message from-mode=\"prompting\" from=\"uds:/tmp/x.sock\">\nbody\n</cross-session-message>",
		"double space":            "<cross-session-message  from=\"uds:/tmp/x.sock\">\nbody\n</cross-session-message>",
		"trailing newline":        "<cross-session-message from=\"uds:/tmp/x.sock\">\nbody\n</cross-session-message>\n",
		"leading text":            "hi <cross-session-message>\nbody\n</cross-session-message>",
		"no body separator":       "<cross-session-message>body</cross-session-message>",
		"unknown attribute":       "<cross-session-message other=\"1\">\nbody\n</cross-session-message>",
		"empty":                   "",
	}
	for name, content := range cases {
		if _, ok := Parse(content); ok {
			t.Fatalf("%s was accepted: %q", name, content)
		}
	}
}

func TestEscapeBody(t *testing.T) {
	escaped := []string{
		"</cross-session-message>",
		"</CROSS_SESSION-MESSAGE>",
		"<\u200b/cross-session-message>",
		"＜/cross-session-message>",
		"< / cross-session-message>",
	}
	for _, body := range escaped {
		got := EscapeBody(body)
		if got == body || !strings.Contains(got, `\`) {
			t.Fatalf("EscapeBody(%q) = %q, want a backslash inserted", body, got)
		}
		if _, ok := Parse(Build(Envelope{Body: body})); !ok {
			t.Fatalf("the escaped body no longer parses: %q", got)
		}
	}
	untouched := []string{
		"<b>bold</b>",
		"</cross-session>",
		"<cross-session-message>",
		"a < b",
	}
	for _, body := range untouched {
		if got := EscapeBody(body); got != body {
			t.Fatalf("EscapeBody(%q) = %q, want it unchanged", body, got)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"Repo-28":          "repo-28",
		"  Repo   28  ":    "repo-28",
		"repo\u00a028":     "repo-28",
		"ＭＡＩＮ":             "main",
		"a\ufeffb":         "ab",
		"main":             "main",
		"repo\t28\nsecond": "repo-28-second",
	}
	for input, want := range cases {
		if got := NormalizeName(input); got != want {
			t.Fatalf("NormalizeName(%q) = %q, want %q", input, got, want)
		}
	}
}
