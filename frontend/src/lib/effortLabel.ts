import { t } from '@/locales'

/**
 * A reasoning effort's label, the way every chip and pill writes it: the seven
 * levels the project settled on (owner 2026-11-07 — 关闭思考 / 极低 / 低 / 中 / 高
 * / 最高 / 极致, Off / Minimal / Low / Medium / High / xHigh / Max), the explicit
 * default ('' — the provider applies its own), and any level a source adds
 * later, shown as it is named.
 *
 * This is the one place the wording lives: the conversation's chip, the
 * subagent drawer, a subagent's run pill and the settings page's model form all
 * call it, so no surface can invent 「最低」 or 「超高」. The English half is the
 * level's own value name (`xhigh` reads xHigh), which is what a provider's
 * documentation uses for it.
 */
const LEVEL_KEYS = {
  none: 'chat.effortNone',
  minimal: 'chat.effortMinimal',
  low: 'chat.effortLow',
  medium: 'chat.effortMedium',
  high: 'chat.effortHigh',
  xhigh: 'chat.effortXhigh',
  max: 'chat.effortMax',
} as const

export function effortLevelLabel(id: string): string {
  if (id === '') return t('chat.effortDefault')
  const key = LEVEL_KEYS[id as keyof typeof LEVEL_KEYS]
  return key ? t(key) : id
}

/** Every level in the order a picker offers them: `none` first, because it is
 *  the absence of thinking rather than a level of it. */
export const EFFORT_LEVELS = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'] as const

/** The levels a user may pick by hand: the seven without `none`, which is not a
 *  level to choose but a capability of its own («允许关闭思考»). */
export const EFFORT_CHOICES: string[] = EFFORT_LEVELS.filter((level) => level !== 'none')
