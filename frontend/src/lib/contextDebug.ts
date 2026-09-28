import type { SessionContextDebug, SessionContextTimelineEntry } from './api'

export type ContextSignal = 'ok' | 'warn' | 'block'
export type ContextSignalTone = 'healthy' | 'warning' | 'danger'

export interface ContextDebugViewModel {
  signal: ContextSignal
  tone: ContextSignalTone
  mode: string
  estimate: number | undefined
  window: number | undefined
  availableWindow: number | undefined
  reserve: number | undefined
  remaining: number | undefined
  projected: number | undefined
  usageRatio: number | undefined
  usagePercent: number | undefined
  warningRatio: number | undefined
  blockingRatio: number | undefined
  itemCount: number
  evictionCount: number
  recoveryCount: number
  spillCount: number
  boundaryCount: number
  /** What the conversation already fills: the last reported whole prompt
   * plus its output, and an estimate of what was appended since. */
  conversationTokens: number | undefined
  hasDiagnostics: boolean
  windowNumber: number | undefined
  activeBoundaryId: string
  compactDiffCount: number
  compactAuditAvailable: boolean
}

export interface ContextDebugRecoveryEvidence {
  trigger: string
  strategy: string
  reason: string
  summarySource: string
  scope: string
  windowNumber: number | undefined
  replacedItems: number | undefined
  summary: string
  boundaryId: string
  spilledPath: string
  tokensBefore: number | undefined
  tokensAfter: number | undefined
  reactive: boolean | undefined
  tone: ContextSignalTone
}

export interface ContextDebugTokenAttribution {
  checkpointTokens: number
  toolReferenceTokens: number
  freedTokens: number
}

export interface ContextDebugSpillEvidence {
  toolName: string
  callId: string
  path: string
  pathLabel: string
  bytesLabel: string
  omittedLabel: string
  linesLabel: string
  createdAtLabel: string
}

export interface ContextDebugTimelineEvidence {
  kind: string
  title: string
  detail: string
  meta: string
  tone: ContextSignalTone
}

export function normalizeContextSignal(signal?: string | null): ContextSignal {
  const normalized = String(signal ?? '').trim().toLowerCase()
  if (normalized === 'warn') return 'warn'
  if (normalized === 'block') return 'block'
  return 'ok'
}

export function contextSignalTone(signal: ContextSignal): ContextSignalTone {
  if (signal === 'block') return 'danger'
  if (signal === 'warn') return 'warning'
  return 'healthy'
}

export function formatTokenCount(value?: number | null): string {
  if (!isFiniteNumber(value)) return '—'
  return new Intl.NumberFormat().format(value)
}

export function compactTokenCount(value?: number | null): string {
  if (!isFiniteNumber(value)) return '—'
  const abs = Math.abs(value)
  if (abs >= 1_000_000) return `${trimOneDecimal(value / 1_000_000)}m`
  if (abs >= 1_000) return `${trimOneDecimal(value / 1_000)}k`
  return String(Math.round(value))
}

export function buildContextDebugModel(data: SessionContextDebug | null): ContextDebugViewModel {
  const signal = normalizeContextSignal(data?.contextPressure)
  const estimate = firstFinite(data?.projectedContextTokens, data?.conversationTokens)
  const window = data?.modelContextTokens
  const availableWindow = firstFinite(data?.availableWindowTokens, data?.effectiveContextTokens, window)
  const reserve = firstFinite(data?.reserveTokens, window != null && availableWindow != null ? Math.max(0, window - availableWindow) : undefined)
  const remaining = data?.remainingContextTokens
  const projected = data?.projectedContextTokens
  const itemCount = finiteOr(data?.itemCount, data?.topItems?.length ?? data?.items?.length ?? 0)
  const evictionCount = finiteOr(data?.evictionCount, data?.topEvictions?.length ?? data?.evictionDetails?.length ?? 0)
  const compactEntries = canonicalCompactEntries(data)
  const recoveryCount = compactEntries.length
  const toolResultSpillCount = finiteOr(data?.toolResultSpillCount, data?.toolResultSpills?.length ?? 0)
  const spillCount = toolResultSpillCount
  const boundaryCount = compactEntries.filter((item) => stringPresent(item.boundaryId)).length
  const usageRatio = ratio(projected ?? estimate, window)

  return {
    signal,
    tone: contextSignalTone(signal),
    mode: data?.mode || 'agent',
    estimate,
    window,
    availableWindow,
    reserve,
    remaining,
    projected,
    usageRatio,
    usagePercent: usageRatio == null ? undefined : Math.round(usageRatio * 100),
    warningRatio: ratio(data?.warningThresholdTokens, window),
    blockingRatio: ratio(data?.blockingThresholdTokens, window),
    itemCount,
    evictionCount,
    recoveryCount,
    spillCount,
    boundaryCount,
    conversationTokens: data?.conversationTokens,
    windowNumber: isFiniteNumber(data?.windowNumber) ? data?.windowNumber : undefined,
    activeBoundaryId: String(data?.activeBoundaryId ?? '').trim(),
    compactDiffCount: Array.isArray(data?.compactDiff) ? data?.compactDiff.length : 0,
    compactAuditAvailable: Boolean(data?.compactAudit || data?.compactExport),
    hasDiagnostics: Boolean(
      data &&
      (estimate != null ||
        projected != null ||
        window != null ||
        itemCount > 0 ||
        evictionCount > 0 ||
        recoveryCount > 0 ||
        spillCount > 0 ||
        boundaryCount > 0 ||
        (data.workingSet?.length ?? 0) > 0),
    ),
  }
}

export function buildCompactExportJSON(data: SessionContextDebug | null): string {
  const payload = data?.compactExport ?? data?.compactAudit ?? null
  if (!payload) return ''
  return JSON.stringify(payload, null, 2)
}

export function buildRecoveryEvidence(data: SessionContextDebug | null): ContextDebugRecoveryEvidence[] {
  return canonicalCompactEntries(data).map((item) => {
    const trigger = String(item.trigger ?? '').trim()
    const tone: ContextSignalTone = item.reactive ? 'warning' : 'healthy'
    return {
      trigger: trigger || 'compact',
      strategy: String(item.strategy ?? '').trim(),
      reason: String(item.reason ?? '').trim(),
      summarySource: String(item.summarySource ?? '').trim(),
      scope: String(item.scope ?? '').trim(),
      windowNumber: isFiniteNumber(item.windowNumber) ? item.windowNumber : undefined,
      replacedItems: isFiniteNumber(item.replacedItems) ? item.replacedItems : undefined,
      summary: String(item.summary ?? '').trim(),
      boundaryId: String(item.boundaryId ?? '').trim(),
      spilledPath: String(item.path ?? '').trim(),
      tokensBefore: isFiniteNumber(item.tokensBefore) ? item.tokensBefore : undefined,
      tokensAfter: isFiniteNumber(item.tokensAfter) ? item.tokensAfter : undefined,
      reactive: typeof item.reactive === 'boolean' ? item.reactive : undefined,
      tone,
    }
  })
}

export function buildTokenAttribution(data: SessionContextDebug | null): ContextDebugTokenAttribution {
  const server = data?.tokenAttribution ?? null
  if (server && typeof server === 'object' && !Array.isArray(server)) {
    return {
      checkpointTokens: finiteOrNumber((server as Record<string, unknown>).checkpointTokens),
      toolReferenceTokens: finiteOrNumber((server as Record<string, unknown>).toolReferenceTokens),
      freedTokens: finiteOrNumber((server as Record<string, unknown>).freedTokens),
    }
  }
  const compactEntries = canonicalCompactEntries(data)
  const checkpointTokens = countTokens(compactEntries.map((item) => item.summary).join('\n'))
  const toolReferenceTokens = countTokens(data?.toolResultSpills?.map((item) => item.path).join('\n'))
  const freedTokens = compactEntries.reduce((best, item) => Math.max(best, Math.max(0, finiteOr(item.tokensBefore, 0) - finiteOr(item.tokensAfter, 0))), 0)
  return { checkpointTokens, toolReferenceTokens, freedTokens }
}

export function buildSpillEvidence(data: SessionContextDebug | null): ContextDebugSpillEvidence[] {
  return (data?.toolResultSpills ?? []).map((item) => {
    const path = String(item.path ?? '').trim()
    return {
      toolName: String(item.toolName ?? '').trim() || 'tool',
      callId: String(item.callId ?? '').trim(),
      path,
      pathLabel: pathBasename(path),
      bytesLabel: compactTokenCount(item.originalBytes),
      omittedLabel: compactTokenCount(item.omittedBytes),
      linesLabel: formatTokenCount(item.totalLines),
      createdAtLabel: String(item.createdAtUtc ?? '').trim(),
    }
  })
}

export function buildTimelineEvidence(data: SessionContextDebug | null): ContextDebugTimelineEvidence[] {
  return (data?.contextTimeline ?? []).map((item) => timelineEntryToEvidence(item))
}

function timelineEntryToEvidence(item: SessionContextTimelineEntry): ContextDebugTimelineEvidence {
  const kind = String(item.kind ?? '').trim() || 'event'
  if (kind === 'tool_result_spill') {
    const toolName = String(item.toolName ?? '').trim() || 'tool'
    const detailParts = [
      String(item.path ?? '').trim(),
      item.originalBytes ? `${compactTokenCount(item.originalBytes)} bytes` : '',
      item.omittedBytes ? `${compactTokenCount(item.omittedBytes)} omitted` : '',
      item.totalLines ? `${formatTokenCount(item.totalLines)} lines` : '',
    ].filter(Boolean)
    return {
      kind,
      title: toolName,
      detail: detailParts.join(' · '),
      meta: [String(item.callId ?? '').trim(), String(item.runId ?? '').trim(), String(item.createdAtUtc ?? '').trim()].filter(Boolean).join(' · '),
      tone: 'warning',
    }
  }
  const trigger = String(item.trigger ?? '').trim() || 'compact'
  const meta = [
    String(item.strategy ?? '').trim(),
    String(item.summarySource ?? '').trim(),
    String(item.boundaryId ?? '').trim(),
    String(item.createdAtUtc ?? '').trim(),
  ].filter(Boolean).join(' · ')
  return {
    kind,
    title: trigger,
    detail: [
      String(item.reason ?? '').trim(),
      item.replacedItems ? `${formatTokenCount(item.replacedItems)} items` : '',
      item.tokensBefore ? `${compactTokenCount(item.tokensBefore)} before` : '',
      item.tokensAfter ? `${compactTokenCount(item.tokensAfter)} after` : '',
      String(item.summary ?? '').trim(),
    ].filter(Boolean).join(' · '),
    meta,
    tone: item.reactive ? 'warning' : 'healthy',
  }
}

function canonicalCompactEntries(data: SessionContextDebug | null): SessionContextTimelineEntry[] {
  return (data?.contextTimeline ?? []).filter((item) => String(item.kind ?? '').trim() === 'compact')
}

export function parseTokenAttribution(data: SessionContextDebug | null): ContextDebugTokenAttribution {
  return buildTokenAttribution(data)
}

function firstFinite(...values: Array<number | undefined | null>): number | undefined {
  return values.find(isFiniteNumber)
}

function finiteOr(value: number | undefined | null, fallback: number): number {
  return isFiniteNumber(value) ? value : fallback
}

function ratio(value?: number | null, total?: number | null): number | undefined {
  if (!isFiniteNumber(value) || !isFiniteNumber(total) || total <= 0) return undefined
  return Math.max(0, Math.min(1, value / total))
}

function trimOneDecimal(value: number): string {
  const rounded = Math.round(value * 10) / 10
  return Number.isInteger(rounded) ? String(rounded) : rounded.toFixed(1)
}

function stringPresent(value: unknown): boolean {
  return typeof value === 'string' && value.trim() !== ''
}

function pathBasename(path: string): string {
  const normalized = path.trim().replace(/\\/g, '/').replace(/\/+$/, '')
  if (!normalized) return ''
  const idx = normalized.lastIndexOf('/')
  return idx >= 0 ? normalized.slice(idx + 1) : normalized
}

function countTokens(value?: string | null): number {
  const text = String(value ?? '').trim()
  if (!text) return 0
  return Math.max(1, Math.ceil(text.length / 4))
}

function finiteOrNumber(value: unknown): number {
  return isFiniteNumber(value) ? value : 0
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}
