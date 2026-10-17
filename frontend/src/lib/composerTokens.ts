/**
 * The one definition of a composer token, shared with the Go engine through
 * pkg/turn/testdata/composer_tokens.json. Every case in that table is asserted
 * against these functions (composerTokens.test.ts) and against their Go twins
 * (pkg/turn/composer_tokens_test.go and turn.TokenAtCursor), so the terminal and
 * the web composer cannot drift on what a "/" or "@" token is.
 *
 * Offsets are UTF-16 code-unit indices, which is what a textarea's
 * selectionStart uses. The table holds only BMP content, so its rune offsets
 * equal these code-unit offsets.
 */

export type SlashToken = { query: string; start: number; atStart: boolean }
export type MentionToken = { query: string; start: number }

/** Punctuation a token may hold, matching the engine's tokenRuneAllowed. */
const TOKEN_PUNCT = new Set(['_', '-', '.', '/', '\\', '(', ')', '[', ']', '~', ':'])

function tokenRuneAllowed(ch: string): boolean {
  if (ch === '') return false
  if (/[\p{L}\p{N}\p{M}]/u.test(ch)) return true
  return TOKEN_PUNCT.has(ch)
}

/** True when every code point of token is allowed between a sigil and the cursor. */
function tokenAllAllowed(token: string): boolean {
  for (const ch of token) {
    if (!tokenRuneAllowed(ch)) return false
  }
  return true
}

function isSpace(ch: string): boolean {
  return /\s/.test(ch)
}

/** Index of the first non-space code unit, or -1 for an all-whitespace string. */
function firstNonSpace(text: string): number {
  for (let i = 0; i < text.length; i++) {
    if (!isSpace(text[i])) return i
  }
  return -1
}

/**
 * The "/" token the cursor sits in, mirroring turn.SlashTokenAtCursor. A "/"
 * only starts a token at the start of the draft or after whitespace, and only
 * tokenRuneAllowed runs count. atStart is true when the sigil is the first
 * non-space character — the only place a built-in command, not just a skill,
 * is in play.
 */
export function parseSlash(text: string, cursor: number): SlashToken | null {
  if (cursor <= 0 || cursor > text.length) return null
  const at = text.lastIndexOf('/', cursor - 1)
  if (at === -1) return null
  if (at > 0 && !isSpace(text[at - 1])) return null
  const token = text.slice(at + 1, cursor)
  if (!tokenAllAllowed(token)) return null
  return { query: token, start: at, atStart: at === firstNonSpace(text) }
}

/**
 * Completes a "/" token with the command name, mirroring turn.AcceptSlash:
 * [start, end) of text — what was typed of the command — becomes "/name ", and
 * the rest of the text is kept, so a command typed in front of a message takes
 * the message as its arguments. A rest that already starts with a space shares
 * it rather than gaining a second. cursor is the caret just past the inserted
 * space; an end before start reads as the end of the text.
 */
export function acceptSlash(text: string, start: number, end: number, name: string): { text: string; cursor: number } {
  const from = Math.min(Math.max(start, 0), text.length)
  const to = end < from || end > text.length ? text.length : end
  const insert = `/${name} `
  let rest = text.slice(to)
  if (rest.startsWith(' ')) rest = rest.slice(1)
  return { text: text.slice(0, from) + insert + rest, cursor: from + insert.length }
}

/**
 * The "@" (or full-width "＄") token the cursor sits in, mirroring
 * turn.TokenAtCursor: the sigil only starts a token at the start of the draft
 * or after whitespace, a mid-word "@" (user@host) is not a mention, and a
 * closed quote ends it. The full-width sigil is a web affordance the engine
 * does not carry.
 */
export function parseMention(text: string, cursor: number): MentionToken | null {
  if (cursor <= 0 || cursor > text.length) return null
  let at = -1
  for (let i = cursor - 1; i >= 0; i--) {
    const ch = text[i]
    if (ch === '@' || ch === '\uFF04') {
      at = i
      break
    }
  }
  if (at === -1) return null
  if (at > 0 && !isSpace(text[at - 1])) return null
  const token = text.slice(at + 1, cursor)
  if (token.startsWith('"')) {
    const inner = token.slice(1)
    if (inner.includes('"')) return null
    return { query: inner, start: at }
  }
  if (!tokenAllAllowed(token)) return null
  return { query: token, start: at }
}
