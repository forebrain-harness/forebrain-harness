import { toCamelCase } from './case'
import { currentLocale, tIn, type Locale } from '@/locales'

/**
 * A conversation stopped by a usage limit, waiting to continue by itself.
 *
 * The runtime owns the wait (`pkg/turn` auto-continue): it schedules the
 * continuation when a turn fails on a spent allowance with a known reset, and
 * submits it when the reset passes. The page only shows the wait and lets the
 * reader cancel it — the terminal shows and cancels the very same one.
 */
export interface AutoContinueState {
  /** When the continuation is submitted, RFC 3339. */
  continueAt: string
  /** When the provider said the allowance returns, RFC 3339. */
  resetAt?: string
  /** 'rate_limit_quota' for a spent allowance, 'rate_limit_throttle' for a rate limit. */
  code: string
  plan?: string
  attempt?: number
}

/** The auto-continue lifecycle events, all handled apart from any run's timeline. */
export const AUTO_CONTINUE_EVENT_TYPES = new Set([
  'auto_continue_scheduled',
  'auto_continue_started',
  'auto_continue_cancelled',
])

/**
 * parseAutoContinue reads a scheduled continuation from an event payload or
 * from the snapshot a session binding carries. Either spelling of the keys is
 * accepted: event payloads reach the page camel-cased, the binding snapshot
 * does not. Anything without a readable continuation time is not a wait.
 */
export function parseAutoContinue(raw: unknown): AutoContinueState | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
  const data = toCamelCase(raw as Record<string, unknown>) as Record<string, unknown>
  const continueAt = String(data.continueAt ?? '').trim()
  if (!continueAt || !Number.isFinite(Date.parse(continueAt))) return null
  const attempt = Number(data.attempt)
  return {
    continueAt,
    resetAt: String(data.resetAt ?? '').trim() || undefined,
    code: String(data.code ?? '').trim() || 'rate_limit_quota',
    plan: String(data.plan ?? '').trim() || undefined,
    attempt: Number.isFinite(attempt) && attempt > 0 ? attempt : undefined,
  }
}

/** A continuation and the agent whose view it belongs to ("" the conversation). */
export interface AutoContinueEntry extends AutoContinueState {
  agentId: string
}

/**
 * parseAutoContinueEntries reads the binding snapshot's list of continuations,
 * one per agent: the conversation's own and each of its subagents'. Anything
 * without a readable wait is dropped.
 */
export function parseAutoContinueEntries(raw: unknown): AutoContinueEntry[] {
  if (!Array.isArray(raw)) return []
  const out: AutoContinueEntry[] = []
  for (const item of raw) {
    const state = parseAutoContinue(item)
    if (!state) continue
    const data = toCamelCase((item ?? {}) as Record<string, unknown>) as Record<string, unknown>
    out.push({ ...state, agentId: String(data.agentId ?? '').trim() })
  }
  return out
}

/**
 * formatAutoContinueNotice is the one line the page shows while the
 * conversation waits: what stopped it and when it picks up again, on the
 * viewer's own clock.
 */
export function formatAutoContinueNotice(
  state: AutoContinueState,
  locale: Locale = currentLocale.value,
  now: Date = new Date(),
): string {
  const headline = state.code === 'rate_limit_throttle'
    ? tIn(locale, 'autoContinue.throttle')
    : tIn(locale, 'autoContinue.quota')
  const time = formatAutoContinueTime(new Date(state.continueAt), locale, now)
  return tIn(locale, 'autoContinue.notice', { headline, time })
}

/**
 * formatAutoContinueTime names the continuation time as coarsely as stays
 * unambiguous: the time alone today, with the weekday within the week, with
 * the date beyond it.
 */
export function formatAutoContinueTime(at: Date, locale: Locale = currentLocale.value, now: Date = new Date()): string {
  const tag = locale === 'zh' ? 'zh-CN' : 'en-US'
  const clock: Intl.DateTimeFormatOptions = { hour: 'numeric', minute: '2-digit' }
  if (sameDay(at, now)) {
    return new Intl.DateTimeFormat(tag, clock).format(at)
  }
  const ahead = at.getTime() - now.getTime()
  if (ahead > 0 && ahead < 6 * 24 * 3600 * 1000) {
    return new Intl.DateTimeFormat(tag, { ...clock, weekday: 'short' }).format(at)
  }
  const withYear = at.getFullYear() !== now.getFullYear()
  return new Intl.DateTimeFormat(tag, {
    ...clock,
    month: 'short',
    day: 'numeric',
    ...(withYear ? { year: 'numeric' as const } : {}),
  }).format(at)
}

function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()
}
