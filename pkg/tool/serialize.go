// Structured output encodings: TOON and TOML.
package tool

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// toonIndent is the spec default indentation unit (§1.3).
const toonIndent = "  "

// EncodeTOON renders a decoded JSON value as a TOON document. The input must
// be the output of encoding/json unmarshalling into `any` (map[string]any,
// []any, string, float64, bool, nil).
func EncodeTOON(v any) string {
	var sb strings.Builder
	writeTOONRoot(&sb, v)
	return strings.TrimRight(sb.String(), "\n")
}

// JSONToTOON converts a JSON document to TOON. It returns ok=false when the
// input is not valid JSON or when TOON would not be smaller, so callers can
// keep the original bytes (fail-open).
func JSONToTOON(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return raw, false
	}
	// Only objects and arrays carry structural overhead worth removing.
	if c := trimmed[0]; c != '{' && c != '[' {
		return raw, false
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return raw, false
	}
	// Reject trailing content: a concatenation of documents is not a value.
	if dec.More() {
		return raw, false
	}
	out := EncodeTOON(v)
	if out == "" || len(out) >= len(trimmed) {
		return raw, false
	}
	return out, true
}

func writeTOONRoot(sb *strings.Builder, v any) {
	switch val := v.(type) {
	case map[string]any:
		writeTOONObjectBody(sb, val, 0)
	case []any:
		writeTOONArray(sb, "", val, 0)
	default:
		sb.WriteString(toonScalar(v))
		sb.WriteByte('\n')
	}
}

// writeTOONObjectBody emits an object's entries at the given depth. Keys keep
// their insertion-independent sorted order: Go maps have no stable order, so
// sorting is what makes the encoding deterministic (the spec permits any key
// order as long as decoders preserve what they read).
func writeTOONObjectBody(sb *strings.Builder, obj map[string]any, depth int) {
	if len(obj) == 0 {
		return
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Keyed tabular form: every value is a uniform non-empty object (§9.5).
	if fields, ok := toonKeyedFields(obj, keys); ok {
		writeIndent(sb, depth)
		fmt.Fprintf(sb, "[%d:]{%s}:\n", len(keys), strings.Join(fields, ","))
		for _, k := range keys {
			row := obj[k].(map[string]any)
			writeIndent(sb, depth+1)
			sb.WriteString(toonKey(k))
			sb.WriteString(": ")
			sb.WriteString(toonRow(row, fields))
			sb.WriteByte('\n')
		}
		return
	}

	for _, k := range keys {
		writeTOONEntry(sb, k, obj[k], depth)
	}
}

func writeTOONEntry(sb *strings.Builder, key string, v any, depth int) {
	switch val := v.(type) {
	case map[string]any:
		writeIndent(sb, depth)
		sb.WriteString(toonKey(key))
		sb.WriteString(":\n")
		writeTOONObjectBody(sb, val, depth+1)
	case []any:
		writeTOONArray(sb, key, val, depth)
	default:
		writeIndent(sb, depth)
		sb.WriteString(toonKey(key))
		sb.WriteString(": ")
		sb.WriteString(toonScalar(v))
		sb.WriteByte('\n')
	}
}

// writeTOONArray picks the array form per §9: empty -> `[]`, all primitives ->
// inline, uniform objects -> tabular, anything else -> list.
func writeTOONArray(sb *strings.Builder, key string, arr []any, depth int) {
	prefix := ""
	if key != "" {
		prefix = toonKey(key)
	}
	if len(arr) == 0 {
		writeIndent(sb, depth)
		if prefix != "" {
			sb.WriteString(prefix)
			sb.WriteString(": []\n")
		} else {
			sb.WriteString("[]\n")
		}
		return
	}
	if toonAllPrimitive(arr) {
		writeIndent(sb, depth)
		fmt.Fprintf(sb, "%s[%d]: %s\n", prefix, len(arr), toonInline(arr))
		return
	}
	if fields, ok := toonTabularFields(arr); ok {
		writeIndent(sb, depth)
		fmt.Fprintf(sb, "%s[%d]{%s}:\n", prefix, len(arr), strings.Join(fields, ","))
		for _, item := range arr {
			row := item.(map[string]any)
			writeIndent(sb, depth+1)
			sb.WriteString(toonRow(row, fields))
			sb.WriteByte('\n')
		}
		return
	}
	// List form (§9.2/§9.4): one `- ` item per element.
	writeIndent(sb, depth)
	fmt.Fprintf(sb, "%s[%d]:\n", prefix, len(arr))
	for _, item := range arr {
		writeTOONListItem(sb, item, depth+1)
	}
}

func writeTOONListItem(sb *strings.Builder, item any, depth int) {
	switch val := item.(type) {
	case map[string]any:
		if len(val) == 0 {
			writeIndent(sb, depth)
			sb.WriteString("-\n")
			return
		}
		// Render the object one level deeper, then splice "- " onto its first
		// line so the item marker and first field share a line (§10).
		var inner strings.Builder
		writeTOONObjectBody(&inner, val, depth+1)
		spliceListMarker(sb, inner.String(), depth)
	case []any:
		var inner strings.Builder
		writeTOONArray(&inner, "", val, depth+1)
		spliceListMarker(sb, inner.String(), depth)
	default:
		writeIndent(sb, depth)
		sb.WriteString("- ")
		sb.WriteString(toonScalar(item))
		sb.WriteByte('\n')
	}
}

// spliceListMarker rewrites the first line of an already-indented block so it
// begins with the list marker at the item's own depth.
func spliceListMarker(sb *strings.Builder, block string, depth int) {
	lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	if len(lines) == 0 {
		return
	}
	writeIndent(sb, depth)
	sb.WriteString("- ")
	sb.WriteString(strings.TrimLeft(lines[0], " "))
	sb.WriteByte('\n')
	for _, line := range lines[1:] {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
}

func writeIndent(sb *strings.Builder, depth int) {
	for i := 0; i < depth; i++ {
		sb.WriteString(toonIndent)
	}
}

// toonAllPrimitive reports whether every element is a scalar, making the array
// eligible for inline form.
func toonAllPrimitive(arr []any) bool {
	for _, v := range arr {
		switch v.(type) {
		case map[string]any, []any:
			return false
		}
	}
	return true
}

// toonTabularFields returns the shared field list when arr is a non-empty array
// of objects that all have the same keys and only scalar values. Nested field
// groups are intentionally not emitted: they complicate the header for little
// gain on real MCP payloads, and falling back to list form stays conformant.
func toonTabularFields(arr []any) ([]string, bool) {
	if len(arr) < 2 {
		// A single row costs more in header than it saves.
		return nil, false
	}
	first, ok := arr[0].(map[string]any)
	if !ok || len(first) == 0 {
		return nil, false
	}
	fields := make([]string, 0, len(first))
	for k, v := range first {
		if !toonScalarValue(v) {
			return nil, false
		}
		fields = append(fields, k)
	}
	sort.Strings(fields)
	for _, item := range arr[1:] {
		obj, ok := item.(map[string]any)
		if !ok || len(obj) != len(fields) {
			return nil, false
		}
		for _, f := range fields {
			v, present := obj[f]
			if !present || !toonScalarValue(v) {
				return nil, false
			}
		}
	}
	return fields, true
}

// toonKeyedFields returns the shared field list when every value of obj is a
// uniform non-empty object of scalars, enabling keyed tabular form (§9.5).
func toonKeyedFields(obj map[string]any, keys []string) ([]string, bool) {
	if len(keys) < 2 {
		return nil, false
	}
	first, ok := obj[keys[0]].(map[string]any)
	if !ok || len(first) == 0 {
		return nil, false
	}
	fields := make([]string, 0, len(first))
	for k, v := range first {
		if !toonScalarValue(v) {
			return nil, false
		}
		fields = append(fields, k)
	}
	sort.Strings(fields)
	for _, k := range keys[1:] {
		nested, ok := obj[k].(map[string]any)
		if !ok || len(nested) != len(fields) {
			return nil, false
		}
		for _, f := range fields {
			v, present := nested[f]
			if !present || !toonScalarValue(v) {
				return nil, false
			}
		}
	}
	return fields, true
}

func toonScalarValue(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return false
	}
	return true
}

func toonRow(obj map[string]any, fields []string) string {
	cells := make([]string, len(fields))
	for i, f := range fields {
		cells[i] = toonCell(obj[f])
	}
	return strings.Join(cells, ",")
}

func toonInline(arr []any) string {
	cells := make([]string, len(arr))
	for i, v := range arr {
		cells[i] = toonCell(v)
	}
	return strings.Join(cells, ",")
}

// toonScalar renders a value in key-value position (no delimiter in scope).
func toonScalar(v any) string { return toonValue(v, false) }

// toonCell renders a value inside a row or inline array, where the active
// delimiter forces extra quoting.
func toonCell(v any) string { return toonValue(v, true) }

func toonValue(v any, inRow bool) string {
	switch val := v.(type) {
	case nil:
		return "null"
	case bool:
		if val {
			return "true"
		}
		return "false"
	case json.Number:
		return toonNumberToken(val.String())
	case float64:
		return toonFloat(val)
	case string:
		return toonString(val, inRow)
	default:
		// Unknown Go type: round-trip through JSON so output stays valid.
		b, err := json.Marshal(val)
		if err != nil {
			return "null"
		}
		return toonString(string(b), inRow)
	}
}

// toonNumberToken passes through a JSON number when it is already canonical,
// otherwise re-renders it. Keeping the original digits matters for large
// integers that would lose precision through float64.
func toonNumberToken(s string) string {
	if s == "" {
		return "0"
	}
	if toonCanonicalNumber(s) {
		return s
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return toonQuote(s)
	}
	return toonFloat(f)
}

// toonCanonicalNumber reports whether s is already in TOON canonical form: an
// optional minus, no leading zeros, no exponent, no trailing fractional zeros.
func toonCanonicalNumber(s string) bool {
	body := strings.TrimPrefix(s, "-")
	if body == "" || strings.ContainsAny(body, "eE+") {
		return false
	}
	intPart, fracPart, hasFrac := strings.Cut(body, ".")
	if intPart == "" || (len(intPart) > 1 && intPart[0] == '0') {
		return false
	}
	if hasFrac && (fracPart == "" || strings.HasSuffix(fracPart, "0")) {
		return false
	}
	if s == "-0" {
		return false
	}
	for i := 0; i < len(body); i++ {
		if c := body[i]; (c < '0' || c > '9') && c != '.' {
			return false
		}
	}
	return true
}

// toonFloat renders a float in canonical decimal form, using exponent notation
// only outside the spec's canonical range (§2).
func toonFloat(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null" // §3: non-finite normalizes to null
	}
	if f == 0 {
		return "0" // also normalizes -0
	}
	if abs := math.Abs(f); abs < 1e-6 || abs >= 1e21 {
		return strconv.FormatFloat(f, 'e', -1, 64)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// toonKey renders an object key, quoting it when it is not a bare identifier.
func toonKey(k string) string {
	if toonBareKey(k) {
		return k
	}
	return toonQuote(k)
}

func toonBareKey(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_', c == '-', c == '.':
		default:
			return false
		}
	}
	return true
}

// toonString renders a string value with minimal quoting: quotes are added only
// when leaving the token bare would change how a decoder reads it.
func toonString(s string, inRow bool) string {
	if toonNeedsQuote(s, inRow) {
		return toonQuote(s)
	}
	return s
}

func toonNeedsQuote(s string, inRow bool) bool {
	if s == "" {
		return true
	}
	// Would be read back as a non-string literal.
	switch s {
	case "true", "false", "null", "[]", "-":
		return true
	}
	if toonLooksNumeric(s) {
		return true
	}
	// Structural characters and anything needing escapes.
	if strings.ContainsAny(s, "\"\\\n\r\t:{}[]|") {
		return true
	}
	if inRow && strings.Contains(s, ",") {
		return true
	}
	// Leading/trailing space would be lost by a decoder trimming the token.
	if strings.TrimSpace(s) != s {
		return true
	}
	// A bare "- " prefix reads as a list marker.
	if strings.HasPrefix(s, "- ") {
		return true
	}
	return false
}

// toonLooksNumeric matches the decoder's number grammar (§4) so any string that
// would decode as a number gets quoted.
func toonLooksNumeric(s string) bool {
	body := strings.TrimPrefix(s, "-")
	if body == "" {
		return false
	}
	mantissa, exponent, hasExp := strings.Cut(strings.ToLower(body), "e")
	if hasExp {
		exponent = strings.TrimPrefix(strings.TrimPrefix(exponent, "+"), "-")
		if !toonAllDigits(exponent) {
			return false
		}
	}
	intPart, fracPart, hasFrac := strings.Cut(mantissa, ".")
	if !toonAllDigits(intPart) {
		return false
	}
	if hasFrac && !toonAllDigits(fracPart) {
		return false
	}
	return true
}

func toonAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// toonQuote emits a double-quoted token using the spec's narrow escape set
// (§7.1). json.Marshal produces exactly that set for strings.
func toonQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// toml.go implements a deliberately small TOML subset parser covering the
// constructs both consumers need — Boost-style filter files and foreign
// agent configuration files (Codex's config.toml):
//
//   - table headers [a.b] and arrays of tables [[a.b]]; segments may be
//     quoted, so ["browser@openai-bundled"], [projects."/path/含中文"] and
//     [mcp_servers.node_repl.env] all parse
//   - key = value, for the value forms below
//   - full-line comments (#) and trailing comments outside strings
//
// The accepted value forms. This block is indented so it is preformatted:
// doc-comment prose gets typographic substitution, which rewrites the pair of
// single quotes in a multi-line literal delimiter into a curly quote.
//
//	basic string           "..."
//	literal string         '...'
//	multi-line basic       """..."""
//	multi-line literal     '''...'''
//	integers, booleans     1, true
//	arrays                 [ ... ]  (may span lines, may hold inline tables)
//	inline tables          { k = v, ... }
//
// It is NOT a general-purpose TOML parser: no dates, no floats beyond what
// parseScalar accepts, no dotted keys inside a table.
type tomlValue = any

// ParseTOMLDocument parses a TOML document into nested tables:
// map[string]any all the way down, arrays of tables as []any whose elements
// are map[string]any. Dotted headers create the intermediate tables; an
// array-of-tables header appends a fresh table to the named array.
//
// This is the one shared entry point for TOML in the codebase. Filter file
// parsing (parseFilterTOML below) and foreign-agent configuration reading
// both go through it, so the quoted-header and value forms live in exactly
// one place.
func ParseTOMLDocument(src string) (map[string]tomlValue, error) {
	p := &tomlParser{src: src}
	doc := map[string]tomlValue{}
	target := doc
	for {
		p.skipBlankAndComments()
		if p.eof() {
			return doc, nil
		}
		if p.peek() == '[' {
			segments, array, err := p.parseHeader()
			if err != nil {
				return nil, err
			}
			next, err := descendTable(doc, segments, array)
			if err != nil {
				return nil, err
			}
			target = next
			continue
		}
		key, val, err := p.parseKeyValue()
		if err != nil {
			return nil, err
		}
		if target == nil {
			return nil, fmt.Errorf("key %q outside of any table", key)
		}
		target[key] = val
	}
}

// descendTable walks (creating as needed) the table named by segments and
// returns the innermost table keys are written into. With array set, the last
// segment names an array of tables and a freshly appended element is returned.
func descendTable(doc map[string]tomlValue, segments []string, array bool) (map[string]tomlValue, error) {
	if len(segments) == 0 {
		return nil, fmt.Errorf("empty table header")
	}
	current := doc
	for i, segment := range segments {
		last := i == len(segments)-1
		if last && array {
			list, _ := current[segment].([]tomlValue)
			fresh := map[string]tomlValue{}
			current[segment] = append(list, fresh)
			return fresh, nil
		}
		switch existing := current[segment].(type) {
		case map[string]tomlValue:
			current = existing
		case nil:
			fresh := map[string]tomlValue{}
			current[segment] = fresh
			current = fresh
		default:
			return nil, fmt.Errorf("table header [%s] conflicts with a value", strings.Join(segments, "."))
		}
	}
	return current, nil
}

// rawFilterFile parses one filter file into a generic structure:
// filter fields plus zero or more test vectors.
type rawFilterFile struct {
	Name   string
	Fields map[string]tomlValue
	Tests  []rawTest
}

type rawTest struct {
	Fields map[string]tomlValue
}

func parseFilterTOML(src string) (*rawFilterFile, error) {
	doc, err := ParseTOMLDocument(src)
	if err != nil {
		return nil, err
	}
	out := &rawFilterFile{Fields: map[string]tomlValue{}}
	// Top-level keys belong to the filter itself (the historical shape kept
	// them legal before the first header).
	for key, val := range doc {
		if key == "filters" || key == "tests" {
			continue
		}
		if _, isTable := val.(map[string]tomlValue); isTable {
			continue
		}
		out.Fields[key] = val
	}
	filters, _ := doc["filters"].(map[string]tomlValue)
	if len(filters) > 1 {
		return nil, fmt.Errorf("expected exactly one [filters.<name>] table")
	}
	for name, table := range filters {
		out.Name = name
		fields, _ := table.(map[string]tomlValue)
		for key, val := range fields {
			if _, isTable := val.(map[string]tomlValue); isTable {
				continue
			}
			out.Fields[key] = val
		}
	}
	// Tests arrive as [[tests.<filter-name>]] arrays of tables; every list
	// under the tests table is a test vector sequence.
	if tests, ok := doc["tests"].(map[string]tomlValue); ok {
		names := make([]string, 0, len(tests))
		for name := range tests {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			list, _ := tests[name].([]tomlValue)
			for _, entry := range list {
				fields, _ := entry.(map[string]tomlValue)
				out.Tests = append(out.Tests, rawTest{Fields: fields})
			}
		}
	}
	if out.Name == "" {
		return nil, fmt.Errorf("missing [filters.<name>] header")
	}
	return out, nil
}

type tomlParser struct {
	src string
	pos int
}

func (p *tomlParser) eof() bool { return p.pos >= len(p.src) }

func (p *tomlParser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.src[p.pos]
}

func (p *tomlParser) skipBlankAndComments() {
	for !p.eof() {
		c := p.src[p.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			p.pos++
		case c == '#':
			for !p.eof() && p.src[p.pos] != '\n' {
				p.pos++
			}
		default:
			return
		}
	}
}

func (p *tomlParser) skipInlineSpace() {
	for !p.eof() {
		c := p.src[p.pos]
		if c == ' ' || c == '\t' || c == '\r' {
			p.pos++
			continue
		}
		return
	}
}

// parseHeader reads a table header and returns its dotted segments, each
// unquoted. Quoted segments (["name@mp"], [projects."/path with spaces or ."])
// keep their quotes' content verbatim, including '.' and ']' characters, so
// foreign configuration files with path or slug table names parse correctly.
func (p *tomlParser) parseHeader() (segments []string, arrayOfTables bool, err error) {
	if p.peek() != '[' {
		return nil, false, fmt.Errorf("expected table header at offset %d", p.pos)
	}
	p.pos++
	if p.peek() == '[' {
		arrayOfTables = true
		p.pos++
	}
	for {
		p.skipInlineSpace()
		var segment string
		switch c := p.peek(); {
		case c == '"' || c == '\'':
			segment, err = p.parseString()
			if err != nil {
				return nil, false, err
			}
		case p.eof():
			return nil, false, fmt.Errorf("unterminated table header")
		default:
			start := p.pos
			for !p.eof() {
				c := p.src[p.pos]
				if c == '.' || c == ']' {
					break
				}
				p.pos++
			}
			segment = strings.TrimSpace(p.src[start:p.pos])
		}
		if segment == "" {
			return nil, false, fmt.Errorf("empty segment in table header at offset %d", p.pos)
		}
		segments = append(segments, segment)
		p.skipInlineSpace()
		if p.eof() {
			return nil, false, fmt.Errorf("unterminated table header")
		}
		if p.src[p.pos] == '.' {
			p.pos++
			continue
		}
		if p.src[p.pos] != ']' {
			return nil, false, fmt.Errorf("malformed table header at offset %d", p.pos)
		}
		break
	}
	p.pos++ // consume ]
	if arrayOfTables {
		if p.eof() || p.src[p.pos] != ']' {
			return nil, false, fmt.Errorf("malformed array-of-tables header")
		}
		p.pos++
	}
	return segments, arrayOfTables, nil
}

func (p *tomlParser) parseKeyValue() (string, tomlValue, error) {
	p.skipInlineSpace()
	start := p.pos
	for !p.eof() && p.src[p.pos] != '=' && p.src[p.pos] != '\n' {
		p.pos++
	}
	if p.eof() || p.src[p.pos] != '=' {
		return "", nil, fmt.Errorf("expected key = value at offset %d", start)
	}
	key := strings.TrimSpace(p.src[start:p.pos])
	key = strings.Trim(key, `"'`)
	p.pos++ // consume =
	p.skipInlineSpace()
	val, err := p.parseValue()
	if err != nil {
		return "", nil, err
	}
	// consume trailing comment / newline
	p.skipInlineSpace()
	if !p.eof() && p.src[p.pos] == '#' {
		for !p.eof() && p.src[p.pos] != '\n' {
			p.pos++
		}
	}
	return key, val, nil
}

func (p *tomlParser) parseValue() (tomlValue, error) {
	if p.eof() {
		return nil, fmt.Errorf("unexpected end of input in value")
	}
	c := p.peek()
	switch {
	case c == '"' || c == '\'':
		return p.parseString()
	case c == '[':
		return p.parseArray()
	case c == '{':
		return p.parseInlineTable()
	default:
		return p.parseScalar()
	}
}

func (p *tomlParser) parseScalar() (tomlValue, error) {
	start := p.pos
	for !p.eof() {
		c := p.src[p.pos]
		if c == '\n' || c == ',' || c == ']' || c == '}' || c == '#' {
			break
		}
		p.pos++
	}
	raw := strings.TrimSpace(p.src[start:p.pos])
	if raw == "true" {
		return true, nil
	}
	if raw == "false" {
		return false, nil
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return n, nil
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil {
		return f, nil
	}
	return nil, fmt.Errorf("unsupported scalar %q at offset %d", raw, start)
}

// parseString handles all four TOML string forms. On entry p.peek() is the
// opening quote character.
func (p *tomlParser) parseString() (string, error) {
	quote := p.peek()
	// multi-line forms?
	if p.pos+2 < len(p.src) && p.src[p.pos+1] == quote && p.src[p.pos+2] == quote {
		p.pos += 3
		return p.parseMultilineString(quote)
	}
	p.pos++
	var sb strings.Builder
	for !p.eof() {
		c := p.src[p.pos]
		if c == quote {
			p.pos++
			return sb.String(), nil
		}
		if quote == '"' && c == '\\' {
			esc, n, err := p.parseEscape()
			if err != nil {
				return "", err
			}
			sb.WriteString(esc)
			p.pos += n
			continue
		}
		if c == '\n' {
			return "", fmt.Errorf("unterminated single-line string")
		}
		sb.WriteByte(c)
		p.pos++
	}
	return "", fmt.Errorf("unterminated string")
}

func (p *tomlParser) parseMultilineString(quote byte) (string, error) {
	// A newline immediately following the opening delimiter is trimmed.
	if !p.eof() && p.src[p.pos] == '\n' {
		p.pos++
	} else if p.pos+1 < len(p.src) && p.src[p.pos] == '\r' && p.src[p.pos+1] == '\n' {
		p.pos += 2
	}
	var sb strings.Builder
	for !p.eof() {
		// closing delimiter: 3+ quotes
		if p.src[p.pos] == quote && p.pos+2 < len(p.src) && p.src[p.pos+1] == quote && p.src[p.pos+2] == quote {
			// allow up to two extra quote chars as content (TOML spec)
			end := p.pos + 3
			extra := 0
			for end < len(p.src) && p.src[end] == quote && extra < 2 {
				extra++
				end++
			}
			for i := 0; i < extra; i++ {
				sb.WriteByte(quote)
			}
			p.pos = end
			return sb.String(), nil
		}
		c := p.src[p.pos]
		if quote == '"' && c == '\\' {
			// line-ending backslash trims whitespace through the next line
			j := p.pos + 1
			for j < len(p.src) && (p.src[j] == ' ' || p.src[j] == '\t' || p.src[j] == '\r' || p.src[j] == '\n') {
				j++
			}
			if j > p.pos+1 {
				p.pos = j
				continue
			}
			esc, n, err := p.parseEscape()
			if err != nil {
				return "", err
			}
			sb.WriteString(esc)
			p.pos += n
			continue
		}
		sb.WriteByte(c)
		p.pos++
	}
	return "", fmt.Errorf("unterminated multi-line string")
}

func (p *tomlParser) parseEscape() (string, int, error) {
	// p.src[p.pos] == '\\'
	if p.pos+1 >= len(p.src) {
		return "", 0, fmt.Errorf("dangling escape")
	}
	c := p.src[p.pos+1]
	switch c {
	case 'n':
		return "\n", 2, nil
	case 't':
		return "\t", 2, nil
	case 'r':
		return "\r", 2, nil
	case '"':
		return `"`, 2, nil
	case '\\':
		return `\`, 2, nil
	case 'b':
		return "\b", 2, nil
	case 'f':
		return "\f", 2, nil
	case 'u', 'U':
		hexLen := 4
		if c == 'U' {
			hexLen = 8
		}
		if p.pos+2+hexLen > len(p.src) {
			return "", 0, fmt.Errorf("truncated unicode escape")
		}
		n, err := strconv.ParseUint(p.src[p.pos+2:p.pos+2+hexLen], 16, 32)
		if err != nil {
			return "", 0, fmt.Errorf("bad unicode escape: %v", err)
		}
		return string(rune(n)), 2 + hexLen, nil
	default:
		return "", 0, fmt.Errorf("unsupported escape \\%c", c)
	}
}

func (p *tomlParser) parseArray() ([]tomlValue, error) {
	if p.peek() != '[' {
		return nil, fmt.Errorf("expected array")
	}
	p.pos++
	var out []tomlValue
	for {
		p.skipBlankAndComments()
		if p.eof() {
			return nil, fmt.Errorf("unterminated array")
		}
		if p.peek() == ']' {
			p.pos++
			return out, nil
		}
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.skipBlankAndComments()
		if p.peek() == ',' {
			p.pos++
			continue
		}
	}
}

func (p *tomlParser) parseInlineTable() (map[string]tomlValue, error) {
	if p.peek() != '{' {
		return nil, fmt.Errorf("expected inline table")
	}
	p.pos++
	out := map[string]tomlValue{}
	for {
		p.skipInlineSpace()
		if p.eof() {
			return nil, fmt.Errorf("unterminated inline table")
		}
		if p.peek() == '}' {
			p.pos++
			return out, nil
		}
		start := p.pos
		for !p.eof() && p.src[p.pos] != '=' && p.src[p.pos] != '}' {
			p.pos++
		}
		if p.eof() || p.src[p.pos] != '=' {
			return nil, fmt.Errorf("expected key = value in inline table at offset %d", start)
		}
		key := strings.TrimSpace(p.src[start:p.pos])
		key = strings.Trim(key, `"'`)
		p.pos++
		p.skipInlineSpace()
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		out[key] = v
		p.skipInlineSpace()
		if p.peek() == ',' {
			p.pos++
			continue
		}
	}
}
