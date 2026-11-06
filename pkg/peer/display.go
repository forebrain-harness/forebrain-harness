package peer

import "strings"

// fromNameAttribute is the opening tag's attribute a card draws its sender
// from.
const fromNameAttribute = ` from-name="`

// maxDisplayUnwrap bounds how many elements the display reader sees through.
// One is a message; two is the receiver's own wrapper around a message whose
// envelope it could not authenticate (process.envelopeFor); the rest is
// headroom for a forwarded message, and stops a pathological input from
// costing more than a few passes.
const maxDisplayUnwrap = 4

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
// DisplayText therefore unwraps anything that is exactly one envelope element
// — an opening tag for this element first, its closing tag last — and reports
// false for everything else, which the caller shows as it arrived. It says
// nothing about what the model reads: a body that escaped itself is still
// plain text there, and this function is not the gate on that.
func DisplayText(content string) (fromName, body string, ok bool) {
	s := strings.TrimSpace(content)
	for depth := 0; depth < maxDisplayUnwrap; depth++ {
		// A known envelope is read by the protocol's own reader, so its answer
		// is the strict one byte for byte; anything else this build cannot
		// place is unwrapped loosely.
		var name, inner string
		if envelope, parsed := Parse(s); parsed {
			name, inner = strings.TrimSpace(envelope.FromName), envelope.Body
		} else if looseName, looseBody, found := unwrapElement(s); found {
			name, inner = looseName, looseBody
		} else {
			break
		}
		if name != "" {
			fromName = name
		}
		body, ok = inner, true
		// This process wraps a message whose envelope it could not
		// authenticate in one of its own (process.envelopeFor), escaping the
		// closing tags inside it. That wrapper is transport too, so a body
		// that is itself one element is read the same way; the words are
		// inside both. The body is trimmed for that reading only — what this
		// call returns for a single element is exactly what Parse said.
		s = strings.TrimSpace(unescapeClosingTags(inner))
	}
	if !ok {
		return "", "", false
	}
	return fromName, body, true
}

// unwrapElement reads one envelope element: the sender it names and the text
// between its tags. It reports false for text that is not exactly one element.
func unwrapElement(s string) (fromName, body string, ok bool) {
	opening := "<" + envelopeTag
	closing := "</" + envelopeTag + ">"
	if !strings.HasPrefix(s, opening) || !strings.HasSuffix(s, closing) {
		return "", "", false
	}
	// The element's own name ends at the first attribute or at the tag's close.
	if rest := s[len(opening):]; rest == "" || (rest[0] != ' ' && rest[0] != '>') {
		return "", "", false
	}
	// An attribute value cannot carry ">", so the opening tag ends at the
	// first one. A text whose first ">" is the closing tag's own has no body.
	end := strings.IndexByte(s, '>')
	if end < 0 || end+1 > len(s)-len(closing) {
		return "", "", false
	}
	return displayFromName(s[:end]), strings.TrimSpace(s[end+1 : len(s)-len(closing)]), true
}

// displayFromName is the sender an opening tag names. It reads the last
// from-name attribute, so a value inside an earlier attribute cannot pass
// itself off as the sender, and an attribute a newer build writes after it
// does not hide the name. Only the name is read this way — the body is
// everything between the tags either way — so a forged name can mislabel a
// letter on screen but cannot change what the letter says.
func displayFromName(openingTag string) string {
	at := strings.LastIndex(openingTag, fromNameAttribute)
	if at < 0 {
		return ""
	}
	value := openingTag[at+len(fromNameAttribute):]
	quote := strings.IndexByte(value, '"')
	if quote < 0 {
		return ""
	}
	return strings.TrimSpace(value[:quote])
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
