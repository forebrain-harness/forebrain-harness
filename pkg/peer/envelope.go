package peer

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// envelopeTag is the element name of a peer message envelope.
const envelopeTag = "cross-session-message"

// maxEnvelopeName is the length an attribute name is cut to, in runes.
const maxEnvelopeName = 64

// maxFromAddress is the longest sender address an attribute carries.
const maxFromAddress = 300

// maxHopChain is the longest hop chain an attribute carries.
const maxHopChain = 32

// attribute patterns from the wire format: an attribute that does not match is
// omitted rather than written and rejected.
var (
	fromAddressPattern  = regexp.MustCompile(`^[A-Za-z0-9%:_/.\-]{1,300}$`)
	fromSessionPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
	hopTokenPattern     = regexp.MustCompile(`^[0-9a-f]{24}$`)
	envelopeNamePattern = `[^"<>\n\r]+`
)

// envelopePattern parses a whole envelope. A body that does not match it
// exactly — or that does not re-render to the same bytes — is not an envelope,
// and the receiving model is shown plain text instead.
var envelopePattern = regexp.MustCompile(`^<` + envelopeTag +
	`(?: from="([A-Za-z0-9%:_/.\-]+)")?` +
	`(?: from-session="([A-Za-z0-9_-]{1,80})")?` +
	`(?: hop-chain="([0-9a-f]{24}(?:,[0-9a-f]{24}){0,31})")?` +
	`(?: from-name="(` + envelopeNamePattern + `)")?` +
	`(?: from-mode="(bypass|prompting)")?` +
	`(?: from-plugin="(` + envelopeNamePattern + `)")?` +
	`>\n([\s\S]*)\n</` + envelopeTag + `>$`)

// Envelope is one peer message as it travels: who sent it, the hop chain that
// proves it has not looped, and the escaped body.
type Envelope struct {
	From        string
	FromSession string
	HopChain    []string
	FromName    string
	FromMode    string
	FromPlugin  string
	Body        string
}

// Build renders the envelope, escaping the body so that it cannot close the
// element it travels in. Attributes keep their fixed order, each preceded by
// one space, and one that would not survive the wire format is left out.
func Build(e Envelope) string {
	return renderEnvelope(e, EscapeBody(e.Body))
}

// renderEnvelope writes the element around an already-escaped body. Parse
// re-renders with the body it extracted, which must reproduce the input
// byte for byte.
func renderEnvelope(e Envelope, body string) string {
	var b strings.Builder
	b.WriteString("<")
	b.WriteString(envelopeTag)
	if fromAddressPattern.MatchString(e.From) {
		writeAttribute(&b, "from", e.From)
	}
	if fromSessionPattern.MatchString(e.FromSession) {
		writeAttribute(&b, "from-session", e.FromSession)
	}
	if chain := strings.Join(e.HopChain, ","); validHopChain(e.HopChain) && chain != "" {
		writeAttribute(&b, "hop-chain", chain)
	}
	if name := envelopeName(e.FromName); name != "" {
		writeAttribute(&b, "from-name", name)
	}
	if e.FromMode == "bypass" || e.FromMode == "prompting" {
		writeAttribute(&b, "from-mode", e.FromMode)
	}
	if plugin := envelopeName(e.FromPlugin); plugin != "" {
		writeAttribute(&b, "from-plugin", plugin)
	}
	b.WriteString(">\n")
	b.WriteString(body)
	b.WriteString("\n</")
	b.WriteString(envelopeTag)
	b.WriteString(">")
	return b.String()
}

// writeAttribute writes one " name=\"value\"" attribute.
func writeAttribute(b *strings.Builder, name, value string) {
	b.WriteString(" ")
	b.WriteString(name)
	b.WriteString(`="`)
	b.WriteString(value)
	b.WriteString(`"`)
}

// validHopChain reports whether every link is a hop token and the chain fits.
func validHopChain(chain []string) bool {
	if len(chain) > maxHopChain {
		return false
	}
	for _, link := range chain {
		if !hopTokenPattern.MatchString(link) {
			return false
		}
	}
	return true
}

// envelopeName is an attribute's text: quote and angle brackets removed (they
// would end the attribute), then control and format characters, trimmed, and
// cut to 64 runes with an ellipsis.
func envelopeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch r {
		case '"', '<', '>':
			continue
		}
		if isControlOrFormat(r) {
			continue
		}
		b.WriteRune(r)
	}
	trimmed := strings.TrimSpace(b.String())
	runes := []rune(trimmed)
	if len(runes) > maxEnvelopeName {
		return string(runes[:maxEnvelopeName]) + "…"
	}
	return trimmed
}

// EscapeBody defuses every "<" that could open the closing tag, by putting a
// backslash after it. A character NFKC folds to "<" — the full-width form, for
// one — counts as a "<" too: what the receiver reads is the folded text.
func EscapeBody(s string) string {
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(runes); i++ {
		b.WriteRune(runes[i])
		if foldsToLessThan(runes[i]) && closingTagAt(runes, i+1) {
			b.WriteString(`\`)
		}
	}
	return b.String()
}

// foldsToLessThan reports whether r is a "<" once NFKC has folded it.
func foldsToLessThan(r rune) bool {
	return r == '<' || norm.NFKC.String(string(r)) == "<"
}

// closingTagAt reports whether the runes from index j on spell the rest of the
// closing tag: "/cross-session-message", with the tolerance the wire format
// requires — invisible characters and spaces between the pieces, either case,
// and "-" written as "_" (and full-width forms, which NFKC folds back).
func closingTagAt(runes []rune, j int) bool {
	for j < len(runes) && isInvisible(runes[j]) {
		j++
	}
	if j >= len(runes) || runes[j] != '/' {
		return false
	}
	j++
	for j < len(runes) && isInvisible(runes[j]) {
		j++
	}
	end := j + 2*len(envelopeTag)
	if end > len(runes) {
		end = len(runes)
	}
	candidate := string(runes[j:end])
	candidate = stripInvisible(norm.NFKC.String(candidate))
	candidate = strings.ToLower(candidate)
	candidate = strings.ReplaceAll(candidate, "_", "-")
	return strings.HasPrefix(candidate, envelopeTag)
}

// Parse reads an envelope, and reports false for anything that is not exactly
// one as this protocol's own writer produces it: the body must already be
// escaped, and the whole thing must re-render to the input byte for byte. An
// envelope that was assembled by hand — a body carrying its own closing tag, a
// malformed or forged element — is treated as plain text by the receiver, so
// nothing a sender wrote can become text outside the element it travels in.
func Parse(content string) (Envelope, bool) {
	match := envelopePattern.FindStringSubmatch(content)
	if match == nil {
		return Envelope{}, false
	}
	envelope := Envelope{
		From:        match[1],
		FromSession: match[2],
		FromName:    match[4],
		FromMode:    match[5],
		FromPlugin:  match[6],
		Body:        match[7],
	}
	if match[3] != "" {
		envelope.HopChain = strings.Split(match[3], ",")
	}
	// An escaped body is what Build writes, so requiring it costs an honest
	// sender nothing and refuses one that would close the element from inside.
	bodyIsEscaped := EscapeBody(envelope.Body) == envelope.Body
	if !bodyIsEscaped {
		return Envelope{}, false
	}
	if renderEnvelope(envelope, envelope.Body) != content {
		return Envelope{}, false
	}
	return envelope, true
}

// ParseFrom reads an envelope and holds it to the sender the socket layer
// authenticated for the frame it arrived in: an envelope whose from attribute
// names someone else is not one, and the receiver treats it as plain text.
//
// Only the FB-mode source gate proves a frame's sender, by matching the
// connecting pid's record against the address the frame carries (the FB-mode
// source gate in pkg/process, gateRecord). In CC mode the sender's record is
// looked up by pid for its features alone, so a frame's from is unproven there:
// ParseFrom strengthens the FB path and is not a claim the transport covers CC
// mode.
func ParseFrom(content, from string) (Envelope, bool) {
	envelope, ok := Parse(content)
	if !ok {
		return Envelope{}, false
	}
	if envelope.From != "" && envelope.From != from {
		return Envelope{}, false
	}
	return envelope, true
}

// NormalizeName folds a name to the form names are compared in: NFKC, then
// control and format characters dropped (whitespace kept), trimmed, lowercased,
// and inner whitespace runs turned into single dashes.
func NormalizeName(s string) string {
	folded := norm.NFKC.String(s)
	var b strings.Builder
	for _, r := range folded {
		if isControlOrFormat(r) && !unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(r)
	}
	fields := strings.Fields(strings.ToLower(b.String()))
	return strings.Join(fields, "-")
}

// isInvisible reports whether r may hide between the pieces of a closing tag:
// whitespace and Unicode format characters (zero-width space and friends).
func isInvisible(r rune) bool {
	return unicode.IsSpace(r) || unicode.Is(unicode.Cf, r)
}

// stripInvisible removes the same characters from a candidate fragment.
func stripInvisible(s string) string {
	var b strings.Builder
	for _, r := range s {
		if isInvisible(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isControlOrFormat reports whether r is a control, format, surrogate or
// line/paragraph separator character.
func isControlOrFormat(r rune) bool {
	return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Cs, unicode.Zl, unicode.Zp)
}
