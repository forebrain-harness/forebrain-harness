import { describe, expect, it } from 'vitest'

import {
  buildContextDebugModel,
  buildRecoveryEvidence,
  buildTimelineEvidence,
  buildTokenAttribution,
  compactTokenCount,
  normalizeContextSignal,
} from './contextDebug'

describe('context debug', () => {
  it('projects compact checkpoints', () => {
    const data = {
      contextPressure: 'warn',
      modelContextTokens: 200_000,
      projectedContextTokens: 180_000,
      contextTimeline: [{
        kind: 'compact', trigger: 'auto', strategy: 'remote_v2',
        summarySource: 'remote_compaction', scope: 'total', boundaryId: 'window-2',
        windowNumber: 2, replacedItems: 8, tokensBefore: 180_000, tokensAfter: 60_000,
      }],
    }
    expect(buildContextDebugModel(data)).toMatchObject({
      signal: 'warn', usagePercent: 90, recoveryCount: 1, boundaryCount: 1,
    })
    expect(buildRecoveryEvidence(data)).toEqual([expect.objectContaining({
      trigger: 'auto', strategy: 'remote_v2', summarySource: 'remote_compaction',
      boundaryId: 'window-2', replacedItems: 8,
    })])
    expect(buildTimelineEvidence(data)[0]).toMatchObject({ kind: 'compact', title: 'auto', tone: 'healthy' })
  })

  it('attributes checkpoint and freed tokens', () => {
    const got = buildTokenAttribution({
      contextTimeline: [{ kind: 'compact', summary: 'checkpoint text', tokensBefore: 9000, tokensAfter: 2000 }],
      toolResultSpills: [{ path: '/tmp/tool-output.txt' }],
    })
    expect(got.checkpointTokens).toBeGreaterThan(0)
    expect(got.toolReferenceTokens).toBeGreaterThan(0)
    expect(got.freedTokens).toBe(7000)
  })

  it('formats stable signal and token values', () => {
    expect(normalizeContextSignal('BLOCK')).toBe('block')
    expect(compactTokenCount(182000)).toBe('182k')
  })
})
