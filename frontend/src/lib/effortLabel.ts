import { t } from '@/locales'

/**
 * A reasoning effort's label, the way every chip and pill writes it: the
 * catalog's own four levels, the explicit default ('' — the provider applies
 * its own), and any level the catalog adds later, shown as it is named.
 */
export function effortLevelLabel(id: string): string {
  if (id === 'low') return t('chat.effortLow')
  if (id === 'medium') return t('chat.effortMedium')
  if (id === 'high') return t('chat.effortHigh')
  if (id === 'xhigh') return t('chat.effortXhigh')
  if (id === '') return t('chat.effortDefault')
  return id
}
