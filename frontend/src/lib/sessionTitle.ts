/** The engine's session-title rule (pkg/state `generateSessionTitle`): the
 *  content is trimmed, its first line is kept, and a line longer than 50
 *  characters keeps 50 and ends in "...". The web surface applies the same rule
 *  so a conversation the server has not titled yet reads the way it will once
 *  it has — the top bar never shows a second, different name (plan 072). */

/** The engine's cap: 50 characters (runes), then "...". */
const MAX_TITLE_RUNES = 50

export function sessionTitleFromContent(content: string): string {
  const trimmed = String(content ?? '').trim()
  if (!trimmed) return ''
  // The engine takes the first line (strings.IndexAny with "\r\n").
  const breakAt = trimmed.search(/[\r\n]/)
  const line = breakAt > 0 ? trimmed.slice(0, breakAt) : trimmed
  // Runes, not code units: a Chinese character or an emoji is one character,
  // and a cut never lands inside one.
  const runes = Array.from(line)
  if (runes.length <= MAX_TITLE_RUNES) return line
  return `${runes.slice(0, MAX_TITLE_RUNES).join('')}...`
}
