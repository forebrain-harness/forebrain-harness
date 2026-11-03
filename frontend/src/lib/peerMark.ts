/**
 * The identity tones (plan 200 §3): the four colours an agent's block is drawn
 * in, in the order the engine numbers them. They sit in the same low-saturation
 * band as the brand's navy, so a block never shouts over the card it sits on.
 *
 * The engine derives which tone an agent gets; a surface only maps the index to
 * a colour, so the palette can change without a wire change and no surface has
 * to hash a name itself.
 */
import type { I18nKey } from '@/locales'

export const PEER_TONES = ['#0E6E7E', '#6B4C9A', '#45607D', '#4F6B3A'] as const

/** The colour for one tone index. An out-of-range index reads as the third. */
export function peerToneColor(tone: number): string {
  const index = Number.isFinite(tone) ? Math.abs(Math.trunc(tone)) % PEER_TONES.length : 0
  return PEER_TONES[index] ?? PEER_TONES[2]
}

/** The CSS custom property a component paints its tone with. */
export function peerToneVar(tone: number): Record<string, string> {
  return { '--peer-tone': peerToneColor(tone) }
}

/** The clock time a letter's header carries, in the reader's own convention. */
export function peerClockTime(createdAtMs?: number, locale = 'zh-CN'): string {
  const at = Number(createdAtMs ?? 0)
  if (!Number.isFinite(at) || at <= 0) return ''
  const date = new Date(at)
  if (Number.isNaN(date.getTime())) return ''
  return date.toLocaleTimeString(locale, { hour: '2-digit', minute: '2-digit', hour12: false })
}

/**
 * What kind of session an agent is, in the reader's own words. The engine's
 * kinds are protocol strings; a card that printed one raw would be the only
 * place in the interface that spoke English to a Chinese reader.
 */
export function peerKindLabel(
  kind: string | null | undefined,
  t: (key: I18nKey, named?: Record<string, string | number>) => string,
): string {
  switch (String(kind ?? '').trim().toLowerCase()) {
    case 'terminal':
      return t('chat.peerKindTerminal')
    case 'gateway conversation':
      return t('chat.peerKindGateway')
    case 'subagent in another process':
      return t('chat.peerKindSubagent')
    case 'agent session':
      return t('chat.peerKindAgentSession')
    default:
      return String(kind ?? '').trim()
  }
}

/** The state an agent is in, in the reader's own words. */
export function peerStatusLabel(
  status: string | null | undefined,
  t: (key: I18nKey, named?: Record<string, string | number>) => string,
): string {
  switch (String(status ?? '').trim().toLowerCase()) {
    case 'busy':
      return t('chat.peerStatusBusy')
    case 'waiting':
      return t('chat.peerStatusWaiting')
    case 'shell':
      return t('chat.peerStatusShell')
    case 'idle':
      return t('chat.peerStatusIdle')
    default:
      return String(status ?? '').trim()
  }
}

/** How long ago a write happened, in the drawer's own words: "6 分钟前". */
export function peerAgoLabel(createdAtMs: number, nowMs: number, t: (key: I18nKey, named?: Record<string, string | number>) => string): string {
  const then = Number(createdAtMs ?? 0)
  if (!Number.isFinite(then) || then <= 0) return ''
  const minutes = Math.floor(Math.max(0, nowMs - then) / 60_000)
  if (minutes < 1) return t('chat.peerJustNow')
  if (minutes < 60) return t('chat.peerMinutesAgo', { count: minutes })
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return t('chat.peerHoursAgo', { count: hours })
  return t('chat.peerDaysAgo', { count: Math.floor(hours / 24) })
}
