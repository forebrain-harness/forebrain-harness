// The repair the store applies to text before an engine sees it.
//
// The state database runs on SQLite, MySQL 8 or PostgreSQL 14, and the servers
// refuse text SQLite stores happily: both reject invalid UTF-8 bytes (MySQL
// error 1366, PostgreSQL SQLSTATE 22021), and PostgreSQL rejects a NUL inside a
// value. Tools produce exactly that — a GBK or Shift-JIS file read as raw bytes,
// `git ls-files -z`, a program writing its output as binary — and the store is
// the one place every writer passes through, so the repair lives here rather
// than in each tool.
package dbx

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// ReplacementText is what is written where a byte could not be kept: U+FFFD
// REPLACEMENT CHARACTER, the character the rest of this repository substitutes
// for a byte that is not valid UTF-8 (pkg/llm/truncate.go,
// pkg/tool/display_header.go, pkg/turn/batch.go).
const ReplacementText = "\uFFFD"

// nulEscape is how a NUL inside a JSON string reaches the store: encoding/json
// writes it as the six-character escape, so the column value itself is valid
// UTF-8 and free of raw NULs while the document it holds still carries one.
const nulEscape = `\u0000`

// ValidText returns s with every byte an engine may refuse replaced: invalid
// UTF-8 sequences and NULs become U+FFFD.
//
// It runs on all three engines, not only the server that refused the bytes, so
// a conversation's stored text does not depend on which engine it ran on.
// Newlines, carriage returns, tabs and every valid sequence are left exactly as
// they are: the store does not rewrite prose. Text that is already clean is
// returned as the same string, unchanged and unallocated.
func ValidText(s string) string {
	if utf8.ValidString(s) && !strings.ContainsRune(s, 0) {
		return s
	}
	return strings.ReplaceAll(strings.ToValidUTF8(s, ReplacementText), "\x00", ReplacementText)
}

// ValidJSONText is ValidText for a JSON document, plus the escape a NUL takes
// inside one: an unescaped `\u0000` becomes `\uFFFD`.
//
// A raw NUL cannot appear inside a JSON string — a document carrying one is not
// valid JSON at all — so the escape is the form that reaches the store, and it
// is what PostgreSQL refuses when it parses a column for the jsonb cast
// (`unsupported Unicode escape sequence`, measured on 14.24). MySQL 8 accepts
// the same escape; the repair runs on it too, so the document stored for a
// conversation does not depend on which engine it ran on.
//
// Changing the cast is not the fix: `::json` fails identically, because the
// reject comes from the JSON lexer, before either type's storage form matters.
//
// `\\u0000` — a literal backslash followed by the text `u0000` — is a different
// document and is left alone, as is every other escape.
func ValidJSONText(raw []byte) []byte {
	if utf8.Valid(raw) && bytes.IndexByte(raw, 0) < 0 && !bytes.Contains(raw, []byte(nulEscape)) {
		return raw
	}
	text := ValidText(string(raw))
	out := make([]byte, 0, len(text))
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c != '\\' {
			out = append(out, c)
			continue
		}
		// A backslash escapes the byte behind it, so a doubled backslash is a
		// literal one occupying both bytes and the text after it is not an
		// escape. Copying the pair keeps `\\u0000` exactly as it was.
		if i+1 < len(text) && text[i+1] == '\\' {
			out = append(out, c, text[i+1])
			i++
			continue
		}
		if strings.HasPrefix(text[i:], nulEscape) {
			out = append(out, `\uFFFD`...)
			i += len(nulEscape) - 1
			continue
		}
		out = append(out, c)
	}
	return out
}
