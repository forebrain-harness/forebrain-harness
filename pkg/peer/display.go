package peer

import (
	"regexp"
	"strings"
)

// maxDisplayUnwrap bounds how many elements the display reader sees through.
// One is a message; two is the receiver's own wrapper around a message whose
// envelope it could not authenticate (process.envelopeFor); the rest is
// headroom for a forwarded message, and stops a pathological input from
// costing more than a few passes.
const maxDisplayUnwrap = 4

// machineValuePattern is the only shape an attribute value may have for the
// display to drop it unseen: a machine token — ids, leases, versions — short
// and without whitespace, so no sentence hides inside one. An attribute
// carrying anything else keeps its level on the screen whole, because the
// model reads that value and the screen may not show less than the model
// reads.
var machineValuePattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{0,64}$`)

// looseAttributePattern is one attribute of an opening tag this build cannot
// parse strictly. A value may not carry a quote, an angle bracket or a
// newline, so a match cannot reach past its own value — which is what lets
// the reader demand that the matches tile the opening tag completely and
// treat one stray character as prose rather than transport.
var looseAttributePattern = regexp.MustCompile(` ([a-z][a-z0-9-]{0,31})="([^"<>\n\r]*)"`)

// displayAttribute is one attribute of an opening tag, as written on the wire.
type displayAttribute struct{ name, value string }

// displayLevel is one element the display reader sees through.
type displayLevel struct {
	from, fromName string
	attributes     []displayAttribute // every attribute of the opening tag, in order
	body           string             // what the level's reader returned as the body
}

// DisplayText reads an envelope for the screen: who sent it, and the words
// inside it. It is the reader a card, a held preview and a queue row use, and
// it is deliberately looser than Parse.
//
// Parse is the model-facing reader and stays exact: it re-renders the element
// and demands the input back byte for byte, so a hand-assembled or forged
// element is plain text to the receiving model. A surface cannot hold that
// line. The wire format grows attributes — a lease, a plugin, whatever the
// next plan adds — and two sessions of one conversation may run different
// builds, so a receiver can hold an envelope naming an attribute it has never
// heard of. Parse then reports false, and a card drawing the content paints
// the transport's own markup at the reader: the defect plan 246 fixes.
//
// Seeing through the element is still what the screen wants, but a level is
// unwrapped only when every attribute that unwrapping would drop is a machine
// token — the protocol's own address, session, hop chain and mode shapes, or
// a value matching machineValuePattern. A level carrying prose in an
// attribute stays on the screen as it arrived, escaped closing tags and all:
// the hold gate exists so a person can read what the model will read, and a
// preview that shows less than the model gets is no gate at all. The one
// value exempt from the token shape is the sender's name when the card
// prints it, because a name the person sees hides nothing.
//
// The name has a single source, so every surface writes the same one: the
// outermost from-name, or — when that is empty — the first inner from-name on
// a level whose from matches the outer envelope's, the same sender naming
// themselves inside a message this process re-wrapped. A level that names a
// different sender is a forgery shape: its name is not adopted and must be a
// machine token like any other dropped value.
//
// It says nothing about what the model reads: a body that escaped itself is
// still plain text there, and this function is not the gate on that.
func DisplayText(content string) (fromName, body string, ok bool) {
	s := strings.TrimSpace(content)
	outerFrom := ""
	for depth := 0; depth < maxDisplayUnwrap; depth++ {
		level, found := readDisplayLevel(s)
		if !found {
			break
		}
		// Which level's from-name the card prints: the outermost one, or an
		// inner one only while no name was adopted and only from a level
		// vouching for the same sender the outer envelope named.
		adopt := false
		switch {
		case depth == 0:
			adopt = level.fromName != ""
		case fromName == "" && level.fromName != "" && level.from != "" && level.from == outerFrom:
			adopt = true
		}
		// A level is dropped only when nothing dropped could have been words.
		if !level.droppable(adopt) {
			break
		}
		if depth == 0 {
			outerFrom = level.from
		}
		if adopt {
			fromName = level.fromName
		}
		body, ok = level.body, true
		// This process wraps a message whose envelope it could not
		// authenticate in one of its own (process.envelopeFor), escaping the
		// closing tags inside it. That wrapper is transport too, so a body
		// that is itself one element is read the same way; the words are
		// inside both. The body is trimmed for that reading only — what this
		// call returns for a single element is exactly what Parse said.
		s = strings.TrimSpace(unescapeClosingTags(level.body))
	}
	if !ok {
		return "", "", false
	}
	return fromName, body, true
}

// readDisplayLevel reads one element: strictly when Parse accepts it, else
// loosely, an opening tag made of nothing but name="value" attributes. It
// reports false for text that is not exactly one element, so the caller shows
// that text as it arrived.
func readDisplayLevel(s string) (displayLevel, bool) {
	if envelope, ok := Parse(s); ok {
		// The strict reader's attributes are the protocol's own fields, in
		// the order the writer puts them on the wire; a field the element
		// does not carry is not an attribute at all.
		var attributes []displayAttribute
		if envelope.From != "" {
			attributes = append(attributes, displayAttribute{name: "from", value: envelope.From})
		}
		if envelope.FromSession != "" {
			attributes = append(attributes, displayAttribute{name: "from-session", value: envelope.FromSession})
		}
		if chain := strings.Join(envelope.HopChain, ","); chain != "" {
			attributes = append(attributes, displayAttribute{name: "hop-chain", value: chain})
		}
		if envelope.FromName != "" {
			attributes = append(attributes, displayAttribute{name: "from-name", value: envelope.FromName})
		}
		if envelope.FromMode != "" {
			attributes = append(attributes, displayAttribute{name: "from-mode", value: envelope.FromMode})
		}
		if envelope.FromPlugin != "" {
			attributes = append(attributes, displayAttribute{name: "from-plugin", value: envelope.FromPlugin})
		}
		return displayLevel{
			from:       envelope.From,
			fromName:   strings.TrimSpace(envelope.FromName),
			attributes: attributes,
			body:       envelope.Body,
		}, true
	}
	return readLooseLevel(s)
}

// readLooseLevel reads an element naming an attribute this build has never
// heard of, which the strict reader rightly refuses. The opening tag must be
// nothing but attributes — one stray character is prose, not transport — and
// no attribute twice, because nothing says which value to believe.
func readLooseLevel(s string) (displayLevel, bool) {
	opening := "<" + envelopeTag
	closing := "</" + envelopeTag + ">"
	if !strings.HasPrefix(s, opening) || !strings.HasSuffix(s, closing) {
		return displayLevel{}, false
	}
	// An attribute value cannot carry ">", so the first one ends the opening
	// tag. When that ">" is the closing tag's own there is no room for a
	// body, and the text is not an element.
	end := strings.IndexByte(s, '>')
	if end < 0 || end+1 > len(s)-len(closing) {
		return displayLevel{}, false
	}
	inner := s[len(opening):end]
	var level displayLevel
	seen := make(map[string]bool)
	at := 0
	for _, match := range looseAttributePattern.FindAllStringSubmatchIndex(inner, -1) {
		// The matches must tile the opening tag from its start to the ">":
		// a gap is a character the pattern does not cover, and the tag is
		// not a clean attribute list.
		if match[0] != at {
			return displayLevel{}, false
		}
		at = match[1]
		name, value := inner[match[2]:match[3]], inner[match[4]:match[5]]
		if seen[name] {
			return displayLevel{}, false
		}
		seen[name] = true
		switch name {
		case "from":
			level.from = value
		case "from-name":
			level.fromName = strings.TrimSpace(value)
		}
		level.attributes = append(level.attributes, displayAttribute{name: name, value: value})
	}
	if at != len(inner) {
		return displayLevel{}, false
	}
	level.body = strings.TrimSpace(s[end+1 : len(s)-len(closing)])
	return level, true
}

// droppable reports whether every attribute of level is one the display may
// drop unseen: a protocol attribute checked against the shape its own writer
// gives it, and anything else a machine token. nameShown is whether this
// level's from-name is the name the card prints — a name the person sees may
// be any words, because it hides nothing; one that is not shown must be a
// token like every other dropped value.
func (l displayLevel) droppable(nameShown bool) bool {
	for _, attribute := range l.attributes {
		value := attribute.value
		switch attribute.name {
		case "from":
			if !fromAddressPattern.MatchString(value) {
				return false
			}
		case "from-session":
			if !fromSessionPattern.MatchString(value) {
				return false
			}
		case "hop-chain":
			chain := strings.Split(value, ",")
			if len(chain) > maxHopChain {
				return false
			}
			for _, link := range chain {
				if !hopTokenPattern.MatchString(link) {
					return false
				}
			}
		case "from-mode":
			if value != "bypass" && value != "prompting" {
				return false
			}
		case "from-name":
			if !nameShown && !machineValuePattern.MatchString(value) {
				return false
			}
		default:
			if !machineValuePattern.MatchString(value) {
				return false
			}
		}
	}
	return true
}

// unescapeClosingTags removes the backslashes EscapeBody writes before a
// closing tag, and nothing else: a backslash is dropped only where it follows
// one of the "<" forms EscapeBody defuses and begins that tag. Prose keeps its
// own backslashes, and a stray one is never taken as transport.
func unescapeClosingTags(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' && i > 0 && foldsToLessThan(runes[i-1]) && closingTagAt(runes, i+1) {
			continue
		}
		b.WriteRune(runes[i])
	}
	return b.String()
}
