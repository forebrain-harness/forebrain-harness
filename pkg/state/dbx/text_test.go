// Text an engine refuses is repaired before it is stored (plan 228).
//
// The helper lives in this package because the store's own file budget cannot
// take another file, and because the repair is a property of the bytes, not of
// a dialect: every engine gets the same normalisation.
package dbx

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestValidTextRepairsWhatAnEngineRefusesAndNothingElse pins both halves of the
// helper: the bytes MySQL and PostgreSQL reject are replaced, and everything
// else — prose, whitespace, a 4-byte character — comes back byte-identical. The
// second half is the one that matters most: a repair that rewrote valid text
// would change every conversation's stored history.
func TestValidTextRepairsWhatAnEngineRefusesAndNothingElse(t *testing.T) {
	unchanged := []string{
		"",
		"plain prose, with punctuation — and an em dash",
		"line one\nline two\r\nindented\tcell",
		"emoji \U0001F680 and a combining mark é",
		"already repaired \uFFFD",
	}
	for _, in := range unchanged {
		if got := ValidText(in); got != in {
			t.Errorf("ValidText(%q) = %q, want it unchanged", in, got)
		}
	}

	repaired := []struct {
		name string
		in   string
		want string
	}{
		{"invalid byte", "before\xffafter", "before" + ReplacementText + "after"},
		// ToValidUTF8 replaces a whole run of invalid bytes with one
		// replacement character, which is what a reader of truncated text wants.
		{"truncated sequence", "before\xe4\xb8after", "before" + ReplacementText + "after"},
		{"NUL", "before\x00after", "before" + ReplacementText + "after"},
		{"NUL beside an invalid byte", "\x00\xff", ReplacementText + ReplacementText},
	}
	for _, tc := range repaired {
		got := ValidText(tc.in)
		if got != tc.want {
			t.Errorf("%s: ValidText(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
		if !utf8.ValidString(got) || strings.ContainsRune(got, 0) {
			t.Errorf("%s: ValidText(%q) = %q, which an engine still refuses", tc.name, tc.in, got)
		}
	}
}

// TestValidJSONTextReplacesTheNULEscapeAndKeepsOtherEscapes pins the JSON form:
// the escape a NUL takes inside a stored document is replaced, an escaped
// backslash followed by the text `u0000` is a different document and survives,
// and every other escape is left to the JSON encoder and decoder.
func TestValidJSONTextReplacesTheNULEscapeAndKeepsOtherEscapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "NUL escape replaced",
			in:   `{"type":"text","text":"before\u0000after"}`,
			want: `{"type":"text","text":"before\uFFFDafter"}`,
		},
		{
			name: "doubled backslash is not the escape",
			in:   `{"text":"literal \\u0000 stays"}`,
			want: `{"text":"literal \\u0000 stays"}`,
		},
		{
			name: "ordinary escapes survive",
			in:   `{"text":"a\nb\tc\r\"d\"é\ud83d\ude80"}`,
			want: `{"text":"a\nb\tc\r\"d\"é\ud83d\ude80"}`,
		},
		{
			name: "an escaped backslash before a real NUL escape",
			in:   `{"text":"a\\\u0000b"}`,
			want: `{"text":"a\\\uFFFDb"}`,
		},
		{
			name: "invalid byte and raw NUL in the document",
			in:   "{\"text\":\"a\xffb\x00c\"}",
			want: `{"text":"a` + ReplacementText + `b` + ReplacementText + `c"}`,
		},
	}
	for _, tc := range cases {
		got := string(ValidJSONText([]byte(tc.in)))
		if got != tc.want {
			t.Errorf("%s: ValidJSONText(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: ValidJSONText(%q) is not valid UTF-8: %q", tc.name, tc.in, got)
		}
		if strings.Contains(got, `\u0000`) && tc.want != tc.in {
			t.Errorf("%s: ValidJSONText(%q) still carries the escape: %q", tc.name, tc.in, got)
		}
	}
	// A repaired document is still a document: the value decodes back with the
	// replacement character where the NUL was.
	var decoded struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(ValidJSONText([]byte(`{"text":"a\u0000b"}`)), &decoded); err != nil {
		t.Fatalf("the repaired document does not parse: %v", err)
	}
	if decoded.Text != "a"+ReplacementText+"b" {
		t.Errorf("repaired document decodes as %q, want a%sb", decoded.Text, ReplacementText)
	}
}
