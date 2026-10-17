package turn

import "unicode"

// SlashTokenAtCursor reports the "/" token the cursor sits in: the rune offset
// of the sigil, the query text after it, and whether that sigil is the first
// non-space character of the draft.
//
// The rule is TokenAtCursor's, with "/" in place of "@": the sigil only starts
// a token at the start of the draft or after whitespace, and every rune between
// the sigil and the cursor must be one tokenRuneAllowed permits. A "/" inside a
// word — a/b, pkg/run/skills.go — is not a token.
//
// atStart separates the two places a slash command can mean something. A token
// at the start of the message is the whole slash-command surface (built-in
// commands and skills); a token anywhere else is only a skill reference, which
// the composer offers and the engine loads at send time.
//
// Offsets are in runes, not bytes, so callers index the draft the way a text
// cursor does.
func SlashTokenAtCursor(draft string, cursor int) (start int, query string, atStart bool, ok bool) {
	runes := []rune(draft)
	if cursor < 0 || cursor > len(runes) {
		return 0, "", false, false
	}
	at := -1
	for i := cursor - 1; i >= 0; i-- {
		if runes[i] == '/' {
			at = i
			break
		}
	}
	if at == -1 {
		return 0, "", false, false
	}
	if at > 0 && !unicode.IsSpace(runes[at-1]) {
		return 0, "", false, false
	}
	token := runes[at+1 : cursor]
	for _, r := range token {
		if !tokenRuneAllowed(r) {
			return 0, "", false, false
		}
	}
	return at, string(token), at == firstNonSpaceRune(runes), true
}

// firstNonSpaceRune is the index of the first rune unicode.IsSpace does not
// accept, or -1 for a draft that is all whitespace.
func firstNonSpaceRune(runes []rune) int {
	for i, r := range runes {
		if !unicode.IsSpace(r) {
			return i
		}
	}
	return -1
}

// AcceptSlash completes a "/" token with the command name: the runes
// [start, end) of draft — what was typed of the command — become "/name ", and
// the rest of the draft is kept. It returns the new draft and the caret just
// past the inserted space.
//
// What follows the token is the message the command was typed into, never
// part of the command, so it survives the completion: a command typed in front
// of a message takes the message as its arguments. The trailing space puts the
// caret on those arguments; a rest that already starts with a space shares it
// rather than gaining a second.
//
// Offsets are in runes and are clamped to the draft, an end before start
// reading as the end of the draft.
func AcceptSlash(draft string, start, end int, name string) (string, int) {
	runes := []rune(draft)
	start = min(max(start, 0), len(runes))
	if end < start || end > len(runes) {
		end = len(runes)
	}
	insert := "/" + name + " "
	rest := runes[end:]
	if len(rest) > 0 && rest[0] == ' ' {
		rest = rest[1:]
	}
	return string(runes[:start]) + insert + string(rest), start + len([]rune(insert))
}
