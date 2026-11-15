import { tIn, type Locale } from '@/locales'

/**
 * A spent free tier whose model row the product also sells as a paid twin.
 *
 * The runtime owns the choice (`pkg/turn`'s paid tier): it detects the refusal,
 * publishes the offer, records the reader's answer and resends the turn through
 * the same port auto-continue uses. The page only shows it and answers — the
 * terminal answers the same offer, so taking it on either surface takes it on
 * both.
 */
export interface PaidTierOffer {
  /** The model row's provider. */
  provider: string
  /** The row's own model: the free tier that ran out. */
  freeModel: string
  /** The twin the row is sent as once the reader agrees. */
  paidModel: string
  /** The product's name for the pair ("Hy4"). */
  displayName?: string
  /** When the free tier returns, RFC 3339; absent when the product named none. */
  resetAt?: string
}

/** The event that announces one, apart from any run's timeline. */
export const PAID_TIER_EVENT_TYPE = 'paid_tier_offered'

function toCamel(key: string): string {
  return key.replace(/_([a-z0-9])/g, (_, c: string) => c.toUpperCase())
}

/**
 * parsePaidTierOffer reads one offer from an event payload or from the REST
 * answer. Either spelling of the keys is accepted — event payloads reach the page
 * snake_cased, the binding's snapshot does not — and anything that does not name
 * both ids is not an offer.
 */
export function parsePaidTierOffer(raw: unknown): PaidTierOffer | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
  const source = raw as Record<string, unknown>
  const data: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(source)) data[toCamel(key)] = value
  const freeModel = String(data.freeModel ?? '').trim()
  const paidModel = String(data.paidModel ?? '').trim()
  if (!freeModel || !paidModel) return null
  const resetAt = String(data.resetAt ?? '').trim()
  return {
    provider: String(data.provider ?? '').trim(),
    freeModel,
    paidModel,
    displayName: String(data.displayName ?? '').trim() || undefined,
    resetAt: resetAt && Number.isFinite(Date.parse(resetAt)) ? resetAt : undefined,
  }
}

/**
 * formatPaidTierNotice is the one line the page shows: when the free tier comes
 * back (the product writes its stamps in UTC+8, so the notice says which zone it
 * names), and what the two buttons do.
 */
export function formatPaidTierNotice(offer: PaidTierOffer, locale: Locale): string {
  const paid = offer.displayName || offer.paidModel
  if (!offer.resetAt) return tIn(locale, 'chat.paidTier.noticeNoReset', { model: paid })
  const reset = new Date(offer.resetAt)
  const stamp = new Intl.DateTimeFormat('en-CA', {
    timeZone: 'Asia/Shanghai',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  }).format(reset)
  return tIn(locale, 'chat.paidTier.notice', { model: paid, time: `${stamp} (UTC+8)` })
}
