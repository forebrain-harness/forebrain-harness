// Text truncation by bytes, runes, and tokens.
package llm

import (
	"strings"
	"unicode/utf8"
)

// TruncateBytes clips s to at most keep bytes and appends ellipsis if it had to
// cut. It is the safe replacement for the `s[:keep] + "…"` idiom.
//
// Two properties that idiom lacks:
//
//   - The cut lands on a rune boundary. A fixed byte offset splits multi-byte
//     characters — every CJK string has two chances in three of landing badly —
//     and the terminal draws the fragment as a replacement glyph.
//   - The result is always valid UTF-8, even when s is not. Text that reaches a
//     truncator has often been sliced by something upstream (a byte-limited
//     read, a previous truncation, raw output from a tool that emitted binary),
//     so the damage frequently arrives already done.
//
// keep is a budget for the content only: the ellipsis is appended on top of it,
// matching the idiom this replaces. Callers that must fit a total width should
// pass keep = total - len(ellipsis).
func TruncateBytes(s string, keep int, ellipsis string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	if keep <= 0 {
		return ellipsis
	}
	if len(s) <= keep {
		return s
	}
	cut := keep
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}

func TruncateRunes(s string, max int) string {
	if max <= 0 || s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= max {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
