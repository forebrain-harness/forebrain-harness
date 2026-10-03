import { t } from '@/locales'

/**
 * The words the permission surfaces use for the engine's rule vocabulary:
 * where a rule comes from, what it does and when the runtime asks. One table,
 * so the agent's permission page and a project's permission tab never name
 * the same fact two ways.
 */

/** Where a rule comes from, in words. */
export function permissionSourceLabel(source?: string): string {
  switch (source) {
    case 'localSettings':
      return t('permissions.source.localSettings')
    case 'projectSettings':
      return t('permissions.source.projectSettings')
    case 'session':
      return t('permissions.source.session')
    default:
      return source || '—'
  }
}

/** When the runtime asks before acting, in words. */
export function permissionModeLabel(mode?: string): string {
  switch (mode) {
    case 'on-request':
      return t('permissions.approval.onRequest')
    case 'never':
      return t('permissions.approval.never')
    case 'unless-trusted':
      return t('permissions.approval.unlessTrusted')
    case 'granular':
      return t('permissions.approval.granular')
    default:
      return mode || '—'
  }
}

/** What a rule does, in words. */
export function permissionBehaviorLabel(behavior?: string): string {
  switch (behavior) {
    case 'allow':
      return t('permissions.behavior.allow')
    case 'deny':
      return t('permissions.behavior.deny')
    case 'ask':
      return t('permissions.behavior.ask')
    default:
      return behavior || '—'
  }
}

export function permissionBehaviorClass(behavior?: string): string {
  switch (String(behavior ?? '').toLowerCase()) {
    case 'allow':
      return 'border-[var(--forebrain-brand-border-strong)] text-[var(--forebrain-brand-1)]'
    case 'deny':
      return 'border-[var(--forebrain-danger)] text-[var(--forebrain-danger)]'
    default:
      return 'border-[var(--forebrain-divider)] text-[var(--forebrain-muted-text)]'
  }
}
