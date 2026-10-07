import { toCamelCase } from './case'

/**
 * One language-server recommendation: the runtime's offer to enable (or
 * install and enable) a server the first time a session edits a file of its
 * language. The answer goes back through the same API the terminal modal
 * uses; the offer itself never reaches the model.
 */
export interface LspRecommendation {
  id: string
  serverId: string
  displayName: string
  languages: string[]
  triggerExtension: string
  mode: 'enable' | 'install'
  binaryPath?: string
  version?: string
  installCommand?: string
}

export type LspRecommendationChoice = 'enable' | 'install' | 'not_now' | 'never' | 'disable_all'

export const LSP_RECOMMENDATION_EVENT_TYPE = 'lsp_recommendation'

const LSP_RECOMMENDATION_MODES = new Set(['enable', 'install'])

/**
 * parseLspRecommendation accepts snake_case or camelCase keys; null when
 * id/serverId/mode are missing or mode is unknown.
 */
export function parseLspRecommendation(raw: unknown): LspRecommendation | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
  const data = toCamelCase(raw as Record<string, unknown>) as Record<string, unknown>
  const id = String(data.id ?? '').trim()
  const serverId = String(data.serverId ?? '').trim()
  const mode = String(data.mode ?? '').trim()
  if (!id || !serverId || !LSP_RECOMMENDATION_MODES.has(mode)) return null
  const languages = Array.isArray(data.languages) ? data.languages.map((l) => String(l)) : []
  return {
    id,
    serverId,
    displayName: String(data.displayName ?? '').trim() || serverId,
    languages,
    triggerExtension: String(data.triggerExtension ?? '').trim(),
    mode: mode as LspRecommendation['mode'],
    binaryPath: String(data.binaryPath ?? '').trim() || undefined,
    version: String(data.version ?? '').trim() || undefined,
    installCommand: String(data.installCommand ?? '').trim() || undefined,
  }
}
