package llm

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateBytes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    string
		keep     int
		ellipsis string
		want     string
	}{
		{name: "short input is returned whole", input: "go test", keep: 97, ellipsis: "...", want: "go test"},
		{name: "exact fit is not truncated", input: "abcde", keep: 5, ellipsis: "…", want: "abcde"},
		{name: "ascii cuts at the budget", input: "abcdefghij", keep: 4, ellipsis: "…", want: "abcd…"},
		// "重" is 3 bytes, so keep=4 lands inside the second character.
		{name: "cut inside a rune backs off", input: "重构文案", keep: 4, ellipsis: "…", want: "重…"},
		{name: "cut on a rune boundary is kept", input: "重构文案", keep: 6, ellipsis: "…", want: "重构…"},
		{name: "zero budget is all ellipsis", input: "重构", keep: 0, ellipsis: "…", want: "…"},
		{name: "negative budget is all ellipsis", input: "重构", keep: -5, ellipsis: "…", want: "…"},
		{name: "empty input", input: "", keep: 10, ellipsis: "…", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := TruncateBytes(tc.input, tc.keep, tc.ellipsis)
			if got != tc.want {
				t.Fatalf("TruncateBytes(%q, %d, %q) = %q, want %q", tc.input, tc.keep, tc.ellipsis, got, tc.want)
			}
		})
	}
}

// Text reaching a truncator has often been byte-sliced upstream, so invalid
// input is the normal case rather than the exotic one.
func TestTruncateBytesRepairsInvalidUTF8(t *testing.T) {
	// A lead byte with its continuation bytes lopped off, as a byte-limited
	// read or an earlier s[:n] would leave it.
	broken := "ok \xe4\xb8"
	got := TruncateBytes(broken, 100, "…")
	if !utf8.ValidString(got) {
		t.Fatalf("invalid input passed through: %q", got)
	}
	if !strings.HasPrefix(got, "ok ") {
		t.Fatalf("repair dropped valid text: %q", got)
	}

	if got := TruncateBytes("\xff\xfe"+strings.Repeat("a", 50), 10, "…"); !utf8.ValidString(got) {
		t.Fatalf("invalid prefix survived truncation: %q", got)
	}
}

// Every budget must produce valid UTF-8 that respects the byte bound, not just
// the offsets a table happens to cover.
func TestTruncateBytesNeverSplitsRunesAtAnyBudget(t *testing.T) {
	input := "git commit -m \"重构 approval 文案 with a 长 message\""
	for keep := -1; keep <= len(input)+2; keep++ {
		got := TruncateBytes(input, keep, "…")
		if !utf8.ValidString(got) {
			t.Fatalf("keep=%d produced invalid UTF-8: %q", keep, got)
		}
		if keep > 0 && len(got) > keep+len("…") {
			t.Fatalf("keep=%d produced %d bytes: %q", keep, len(got), got)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		max      int
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			max:      5,
			expected: "",
		},
		{
			name:     "zero max returns empty",
			input:    "hello world",
			max:      0,
			expected: "",
		},
		{
			name:     "no truncation needed",
			input:    "hi",
			max:      5,
			expected: "hi",
		},
		{
			name:     "ASCII truncation",
			input:    "hello world",
			max:      5,
			expected: "hello",
		},
		{
			name:     "UTF-8 multi-byte chars emoji truncation",
			input:    "👋🌍🎉🚀✨",
			max:      3,
			expected: "👋🌍🎉",
		},
		{
			name:     "UTF-8 multi-byte chars fullwidth truncation",
			input:    "ＡＢＣＤ",
			max:      2,
			expected: "ＡＢ",
		},
		{
			name:     "exactly at boundary no truncation",
			input:    "hello",
			max:      5,
			expected: "hello",
		},
		{
			name:     "single rune",
			input:    "a",
			max:      1,
			expected: "a",
		},
		{
			name:     "single emoji",
			input:    "😀",
			max:      1,
			expected: "😀",
		},
		{
			name:     "mixed ASCII and emoji truncation",
			input:    "ab👋cd🌍",
			max:      4,
			expected: "ab👋c",
		},
		{
			name:     "zero max returns empty",
			input:    "hello",
			max:      0,
			expected: "",
		},
		{
			name:     "fullwidth with ASCII mixed truncation",
			input:    "HelloＡＢWorld",
			max:      7,
			expected: "HelloＡＢ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateRunes(tt.input, tt.max)
			if got != tt.expected {
				t.Errorf("TruncateRunes(%q, %v) = %q; want %q",
					tt.input, tt.max, got, tt.expected)
			}
		})
	}
}
