package lsp

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Encoding is a negotiated LSP position encoding.
type Encoding string

const (
	EncodingUTF8  Encoding = "utf-8"
	EncodingUTF16 Encoding = "utf-16"
	EncodingUTF32 Encoding = "utf-32"
)

// ParseEncoding maps a capability value to an Encoding; unknown or empty is UTF-16.
func ParseEncoding(s string) Encoding {
	switch Encoding(s) {
	case EncodingUTF8, EncodingUTF16, EncodingUTF32:
		return Encoding(s)
	}
	return EncodingUTF16
}

// LineOutOfRangeError: the requested 1-based line is past the end.
type LineOutOfRangeError struct{ Line, Lines int }

func (e *LineOutOfRangeError) Error() string {
	return fmt.Sprintf("line %d is past the end of the file (%d lines)", e.Line, e.Lines)
}

// SymbolNotFoundError: AnchorSymbol found no match within ±3 lines.
type SymbolNotFoundError struct {
	Symbol         string
	Line, From, To int
}

func (e *SymbolNotFoundError) Error() string {
	return fmt.Sprintf("symbol %q not found on line %d (searched lines %d-%d)", e.Symbol, e.Line, e.From, e.To)
}

var utf8BOM = [...]byte{0xEF, 0xBB, 0xBF}

// splitLines splits content into its 1-based lines: cut at \n, drop one
// trailing \r per line, and drop a leading UTF-8 BOM, which never counts as a
// column of line 1. A trailing newline does not start a new line.
func splitLines(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	if len(content) >= 3 && content[0] == utf8BOM[0] && content[1] == utf8BOM[1] && content[2] == utf8BOM[2] {
		content = content[3:]
		if len(content) == 0 {
			return nil
		}
	}
	parts := strings.Split(string(content), "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	for i, p := range parts {
		parts[i] = strings.TrimSuffix(p, "\r")
	}
	return parts
}

// LineCount counts lines the way read_file numbers them (a trailing newline
// does not start a new line).
func LineCount(content []byte) int {
	return len(splitLines(content))
}

// LineText returns 1-based line n without its line terminator.
func LineText(content []byte, n int) (string, bool) {
	lines := splitLines(content)
	if n < 1 || n > len(lines) {
		return "", false
	}
	return lines[n-1], true
}

// ToLSPPosition converts a 1-based line and 1-based character column (spec §8.2).
// clamped reports that column was past the end of the line.
func ToLSPPosition(content []byte, line, column int, enc Encoding) (pos Position, clamped bool, err error) {
	lines := splitLines(content)
	if line < 1 || line > len(lines) {
		return Position{}, false, &LineOutOfRangeError{Line: line, Lines: len(lines)}
	}
	text := lines[line-1]
	cut := 0
	switch {
	case column < 1:
		clamped = true
	default:
		off, ok := codePointOffset(text, column)
		if !ok {
			off, clamped = len(text), true
		}
		cut = off
	}
	return Position{Line: uint32(line - 1), Character: uint32(encodedLength(text[:cut], enc))}, clamped, nil
}

// codePointOffset returns the byte offset of the (column-1)-th code point of
// line; ok is false when column is more than one code point past the end.
func codePointOffset(line string, column int) (off int, ok bool) {
	remaining := column - 1
	for off < len(line) && remaining > 0 {
		_, size := utf8.DecodeRuneInString(line[off:])
		off += size
		remaining--
	}
	return off, remaining == 0
}

// encodedLength measures a code-point-aligned prefix of a line in the
// negotiated encoding: bytes for utf-8, units for utf-16 (U+10000 and up are
// two), code points for utf-32. An invalid UTF-8 byte is one code point of
// UTF-8 length 1 and UTF-16 length 1.
func encodedLength(prefix string, enc Encoding) int {
	units := 0
	for i := 0; i < len(prefix); {
		r, size := utf8.DecodeRuneInString(prefix[i:])
		switch enc {
		case EncodingUTF8:
			units += size
		case EncodingUTF32:
			units++
		default: // utf-16
			if r >= 0x10000 && !(r == utf8.RuneError && size == 1) {
				units += 2
			} else {
				units++
			}
		}
		i += size
	}
	return units
}

// FromLSPPosition converts back to a 1-based line and 1-based character column.
// A position past the end of the content maps to line+1 / character+1 unchanged.
func FromLSPPosition(content []byte, pos Position, enc Encoding) (line, column int) {
	lines := splitLines(content)
	if pos.Line >= uint32(len(lines)) {
		return int(pos.Line) + 1, int(pos.Character) + 1
	}
	text := lines[pos.Line]
	units := int(pos.Character)
	codePoints := 0
	i := 0
	for i < len(text) {
		r, size := utf8.DecodeRuneInString(text[i:])
		next := 1
		switch enc {
		case EncodingUTF8:
			next = size
		case EncodingUTF32:
			next = 1
		default: // utf-16
			if r >= 0x10000 && !(r == utf8.RuneError && size == 1) {
				next = 2
			}
		}
		if units < next {
			break // the position splits a code point; stop before it
		}
		units -= next
		codePoints++
		i += size
	}
	return int(pos.Line) + 1, codePoints + 1
}

// AnchorSymbol finds symbol on 1-based line (or within ±3 lines, spec §8.2) and
// returns the line it was found on and its 1-based column.
func AnchorSymbol(content []byte, line int, symbol string) (foundLine, column int, err error) {
	lines := splitLines(content)
	name := anchorName(symbol)
	for _, candidate := range []int{line, line - 1, line + 1, line - 2, line + 2, line - 3, line + 3} {
		if candidate < 1 || candidate > len(lines) {
			continue
		}
		if col, ok := matchSymbol(lines[candidate-1], name); ok {
			return candidate, col, nil
		}
	}
	from, to := line-3, line+3
	if from < 1 {
		from = 1
	}
	if to > len(lines) {
		to = len(lines)
	}
	return 0, 0, &SymbolNotFoundError{Symbol: name, Line: line, From: from, To: to}
}

// anchorName trims the symbol and keeps its last segment: Foo.bar, pkg::name,
// obj->field, and Class#method all anchor on their last part.
func anchorName(symbol string) string {
	symbol = strings.TrimSpace(symbol)
	end := 0
	for _, sep := range []string{"::", "->", ".", "#"} {
		if i := strings.LastIndex(symbol, sep); i >= 0 && i+len(sep) > end {
			end = i + len(sep)
		}
	}
	if end == 0 {
		return symbol
	}
	return symbol[end:]
}

// matchSymbol reports the 1-based code-point column of the first occurrence
// of symbol in line whose neighbouring characters are not identifier
// characters (a Unicode letter, digit, _, or $).
func matchSymbol(line, symbol string) (int, bool) {
	if symbol == "" {
		return 0, false
	}
	from := 0
	for {
		i := strings.Index(line[from:], symbol)
		if i < 0 {
			return 0, false
		}
		i += from
		before, _ := utf8.DecodeLastRuneInString(line[:i])
		after, _ := utf8.DecodeRuneInString(line[i+len(symbol):])
		if !isIdentRune(before) && !isIdentRune(after) {
			return utf8.RuneCountInString(line[:i]) + 1, true
		}
		from = i + 1
	}
}

func isIdentRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$'
}

// PathToURI converts an absolute local path to a file URI.
func PathToURI(abs string) string {
	return pathToURI(abs, runtime.GOOS)
}

// URIToPath converts a file URI back to a local path.
func URIToPath(uri string) (string, error) {
	return uriToPath(uri, runtime.GOOS)
}

func pathToURI(abs, goos string) string {
	if goos == "windows" {
		return "file://" + windowsURIPath(abs)
	}
	return "file://" + encodePath(abs, unixPathSafe)
}

// windowsURIPath renders the part after "file://": drive paths keep their
// leading slash (file:///C:/...), UNC paths open with the host as the
// authority (file://host/share/...).
func windowsURIPath(abs string) string {
	if strings.HasPrefix(abs, `\\`) {
		return encodePath(strings.ReplaceAll(abs[2:], `\`, "/"), windowsPathSafe)
	}
	if len(abs) >= 2 && abs[1] == ':' && isASCIILetter(abs[0]) {
		abs = string(upperByte(abs[0])) + abs[1:]
	}
	return "/" + encodePath(strings.ReplaceAll(abs, `\`, "/"), windowsPathSafe)
}

func uriToPath(uri, goos string) (string, error) {
	if !strings.HasPrefix(uri, "file:") {
		return "", fmt.Errorf("lsp: not a file URI: %q", uri)
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(uri, "file:"), "//")
	authority := ""
	if rest != "" && rest[0] != '/' {
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			authority, rest = rest[:i], rest[i:]
		} else {
			authority, rest = rest, ""
		}
	}
	if strings.EqualFold(authority, "localhost") {
		authority = ""
	}
	path := decodePath(rest)
	if goos != "windows" {
		if authority != "" {
			return "", fmt.Errorf("lsp: file URI with a remote authority is not a local path: %q", uri)
		}
		return path, nil
	}
	if authority != "" {
		return `\\` + authority + strings.ReplaceAll(path, "/", `\`), nil
	}
	path = strings.TrimPrefix(path, "/")
	if len(path) >= 2 && path[1] == ':' && isASCIILetter(path[0]) {
		path = string(upperByte(path[0])) + path[1:]
	}
	return strings.ReplaceAll(path, "/", `\`), nil
}

// Bytes that stay literal in a URI path. The colon stays literal so a Unix
// path like /tmp/go-build:x keeps its shape; on Windows it is the drive
// colon.
const (
	unixPathSafe    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~/:"
	windowsPathSafe = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~:/"
)

const upperHex = "0123456789ABCDEF"

// encodePath percent-encodes every byte outside safe, with uppercase hex.
func encodePath(p, safe string) string {
	var b strings.Builder
	b.Grow(len(p))
	for i := 0; i < len(p); i++ {
		c := p[i]
		if strings.IndexByte(safe, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperHex[c>>4])
		b.WriteByte(upperHex[c&0xF])
	}
	return b.String()
}

// decodePath percent-decodes a URI path; a % that does not start a valid
// escape stays literal.
func decodePath(path string) string {
	if !strings.Contains(path, "%") {
		return path
	}
	var b strings.Builder
	b.Grow(len(path))
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c == '%' && i+2 < len(path) {
			hi, okHi := unhex(path[i+1])
			lo, okLo := unhex(path[i+2])
			if okHi && okLo {
				b.WriteByte(hi<<4 | lo)
				i += 2
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func isASCIILetter(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

func upperByte(c byte) byte {
	if 'a' <= c && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}

// SamePath compares two local paths after filepath.Clean, case-insensitively
// on darwin and windows.
func SamePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
