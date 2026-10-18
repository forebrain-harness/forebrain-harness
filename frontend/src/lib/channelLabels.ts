import { currentLocale } from '@/locales'

import labels from './channelLabels.json'

/**
 * A channel kind's display name (plan 075). `channelLabels.json` is the one
 * source of those names: the Go side mirrors it so the channel table can be
 * searched by what the page shows, and its test fails the build when the two
 * disagree. Each entry is `[中文, English]`.
 */
const table: Record<string, string[]> = labels

/** How the channel table names a kind: its display name in the active
 * language, and the kind itself for a kind the table does not name. */
export function channelLabel(kind: string): string {
  const names = table[kind]
  if (!names || names.length < 2) return kind
  return currentLocale.value === 'zh' ? names[0] : names[1]
}
