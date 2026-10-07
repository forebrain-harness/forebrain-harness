import { currentLocale, tIn, type I18nKey, type Locale } from '@/locales'

/**
 * ProviderErrorDetail is the machine-readable half of a failed turn, as the
 * gateway sends it (`event.TurnErrorDetail`).
 *
 * The runtime also sends a rendered English sentence, but it cannot know which
 * language this browser is showing, so the facts travel separately and the
 * sentence is written here. That is also what lets an error already on screen
 * re-render when the viewer switches language.
 */
export interface ProviderErrorDetail {
  /** Stable classifier, e.g. 'rate_limit_quota' or 'credentials'. */
  code: string
  /** HTTP status the provider answered with, when it had one. */
  status?: number
  /** Subscription tier a spent allowance belongs to, e.g. 'plus'. */
  plan?: string
  /** The provider's own sentence, already unwrapped from its JSON envelope. */
  providerMessage?: string
  /** When a spent allowance returns, RFC 3339. */
  resetAt?: string
  /** How long that wait was when the response arrived. */
  retryAfterSeconds?: number
}

type Translate = (key: I18nKey, params?: Record<string, string | number>) => string

/** Failures whose whole explanation is one headline plus the provider's words. */
const HEADLINE_KEYS: Record<string, I18nKey> = {
  credentials: 'providerError.credentials',
  billing: 'providerError.billing',
  unknown_model: 'providerError.unknownModel',
  timeout: 'providerError.timeout',
  provider_down: 'providerError.providerDown',
}

/**
 * Failures of a scheduled run — a run that went wrong, or standing work that
 * never got one — whose whole explanation is one sentence.
 */
const RUN_ERROR_KEYS: Record<string, I18nKey> = {
  run_failed: 'runError.runFailed',
  run_stopped: 'runError.runStopped',
  run_abandoned: 'runError.runAbandoned',
  no_runtime: 'runError.noRuntime',
  no_delivery_channel: 'runError.noDeliveryChannel',
}

/**
 * Failures that carry words of their own: the reason a scheduled run never
 * started, or a channel's refusal to take the answer. The sentence says what
 * happened; those words are quoted on the line below it, unlabeled — they are
 * the channel's own sentence, not the model service's and not one forebrain
 * writes, so the quotation prefix the provider's words get would misattribute
 * them.
 */
const RUN_ERROR_QUOTED_KEYS: Record<string, I18nKey> = {
  fire_start_failed: 'runError.fireStartFailed',
  delivery_failed: 'runError.deliveryFailed',
}

/**
 * parseProviderErrorDetail reads the detail off an event payload. It returns
 * null for anything without a code, including the older events that carry only
 * a rendered sentence.
 */
export function parseProviderErrorDetail(raw: unknown): ProviderErrorDetail | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
  const data = raw as Record<string, unknown>
  const code = String(data.code ?? '').trim()
  if (!code) return null
  const status = Number(data.status)
  const retryAfterSeconds = Number(data.retryAfterSeconds)
  return {
    code,
    status: Number.isFinite(status) && status > 0 ? status : undefined,
    plan: String(data.plan ?? '').trim() || undefined,
    providerMessage: String(data.providerMessage ?? '').trim() || undefined,
    resetAt: String(data.resetAt ?? '').trim() || undefined,
    retryAfterSeconds:
      Number.isFinite(retryAfterSeconds) && retryAfterSeconds > 0 ? retryAfterSeconds : undefined,
  }
}

/**
 * formatProviderError writes a coded turn failure in the viewer's language.
 * Most codes describe a provider's refusal, but not every failed turn reached
 * a provider — the runtime also refuses a turn outright, a session parked on
 * an approval being the one this build codes — and those codes are written
 * here the same way, so every failed turn says its one sentence.
 *
 * Every code renders as exactly one sentence. A failed turn interrupts what the
 * reader was doing, so the wording says what happened and how it recovers in a
 * single line and stops there. The provider's own words, when they are quoted
 * at all, still get a line of their own -- that is a quotation, not part of the
 * sentence forebrain writes.
 *
 * It returns null for a code this build does not know, which is how a frontend
 * older than its gateway stays correct: the caller falls back to the sentence
 * the runtime rendered rather than inventing one.
 */
export function formatProviderError(
  detail: ProviderErrorDetail,
  locale: Locale = currentLocale.value,
  now: Date = new Date(),
): string | null {
  const tr: Translate = (key, params) => tIn(locale, key, params)
  if (detail.code === 'rate_limit_quota' || detail.code === 'rate_limit_throttle') {
    return formatRateLimit(detail, tr, locale, now)
  }
  if (detail.code === 'context_window') {
    return tr('providerError.contextWindow')
  }
  if (detail.code === 'session_awaiting_approval') {
    return tr('runError.sessionAwaitingApproval')
  }
  if (detail.code === 'session_running') {
    return tr('runError.sessionRunning')
  }
  const runKey = RUN_ERROR_KEYS[detail.code]
  if (runKey) {
    return tr(runKey)
  }
  const runQuotedKey = RUN_ERROR_QUOTED_KEYS[detail.code]
  if (runQuotedKey) {
    const sentence = tr(runQuotedKey)
    return detail.providerMessage ? `${sentence}\n${detail.providerMessage}` : sentence
  }
  const headline = HEADLINE_KEYS[detail.code]
  if (headline) {
    const lines = [withStatus(tr(headline), detail, tr)]
    if (detail.providerMessage) {
      lines.push(tr('providerError.said', { message: detail.providerMessage }))
    }
    return lines.join('\n')
  }
  if (detail.code === 'rejected') {
    return withStatus(detail.providerMessage || tr('providerError.rejected'), detail, tr)
  }
  return null
}

/**
 * formatRateLimit says what ran out and when it comes back, in one sentence
 * assembled from a subject and a recovery clause. The wait is recomputed from
 * the absolute reset time whenever there is one, so an error left on screen --
 * or re-rendered after a language switch -- counts down instead of repeating
 * the wait that applied when the response arrived.
 *
 * The subject and the clause are separate keys because the two languages join
 * them differently; the joining key owns the punctuation, so neither half has
 * to guess where it sits in the sentence.
 */
function formatRateLimit(
  detail: ProviderErrorDetail,
  tr: Translate,
  locale: Locale,
  now: Date,
): string {
  const quota = detail.code === 'rate_limit_quota'
  let subject: string
  if (quota && detail.plan) {
    subject = tr('providerError.quotaWithPlan', { plan: detail.plan })
  } else if (quota) {
    subject = tr('providerError.quota')
  } else {
    subject = tr('providerError.throttle')
  }

  const resetAt = parseResetAt(detail.resetAt)
  const wait = remainingSeconds(detail, resetAt, now)
  let reset: string
  if (resetAt && wait <= 0) {
    reset = tr('providerError.resetPassed', { time: formatResetTime(resetAt, locale) })
  } else if (resetAt) {
    reset = tr('providerError.resetAtKnown', {
      wait: formatWait(wait, tr),
      time: formatResetTime(resetAt, locale),
    })
  } else if (wait > 0) {
    reset = tr('providerError.resetInKnown', { wait: formatWait(wait, tr) })
  } else {
    reset = tr('providerError.resetUnknown')
  }

  const sentence = tr('providerError.sentence', { subject, reset })
  // The provider's own sentence is quoted on a line of its own rather than
  // spliced into that one: it is written in the provider's language, not the
  // viewer's, and a sentence that changes language halfway reads as one claim
  // by forebrain instead of as a quotation. It is quoted only for a throttle --
  // once the allowance and its reset are known, the sentence above states them
  // outright and repeating the original adds nothing.
  if (quota || !detail.providerMessage) return sentence
  return sentence + '\n' + tr('providerError.said', { message: detail.providerMessage })
}

function withStatus(text: string, detail: ProviderErrorDetail, tr: Translate): string {
  if (!detail.status) return text
  return text + tr('providerError.statusSuffix', { status: `HTTP ${detail.status}` })
}

function parseResetAt(value?: string): Date | null {
  if (!value) return null
  const parsed = Date.parse(value)
  return Number.isFinite(parsed) ? new Date(parsed) : null
}

function remainingSeconds(detail: ProviderErrorDetail, resetAt: Date | null, now: Date): number {
  if (resetAt) return Math.round((resetAt.getTime() - now.getTime()) / 1000)
  return detail.retryAfterSeconds ?? 0
}

/** formatWait renders a wait as coarsely as the number deserves. */
function formatWait(totalSeconds: number, tr: Translate): string {
  const seconds = Math.max(0, Math.round(totalSeconds))
  if (seconds <= 0) return tr('providerError.waitBrief')
  if (seconds < 60) return tr('providerError.waitSeconds', { seconds })
  if (seconds < 3600) {
    const minutes = Math.floor(seconds / 60)
    const rest = seconds % 60
    return rest > 0
      ? tr('providerError.waitMinutesSeconds', { minutes, seconds: rest })
      : tr('providerError.waitMinutes', { minutes })
  }
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  return minutes > 0
    ? tr('providerError.waitHoursMinutes', { hours, minutes })
    : tr('providerError.waitHours', { hours })
}

/** The reset is shown in the viewer's own time zone, which is the one they read a clock in. */
function formatResetTime(reset: Date, locale: Locale): string {
  return new Intl.DateTimeFormat(locale === 'zh' ? 'zh-CN' : 'en-US', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(reset)
}
