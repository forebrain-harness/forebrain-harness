import { describe, expect, it } from 'vitest'

import { blockVisibleIn } from './viewMode'
import type { TimelineBlock } from '@/composables/useChatStream'

/**
 * The one table both reading modes share. Concise keeps the answers and the
 * gates — an approval card is a gate, and hiding it would hide the thing
 * waiting for the person — and withholds the run's own cards. Everything else,
 * including an error, is drawn in both modes: nothing is hidden that the reader
 * did not ask to skip.
 */
const KINDS: TimelineBlock['kind'][] = [
  'prompt',
  'user',
  'assistant',
  'thinking',
  'tool',
  'plan',
  'worked',
  'approval',
  'error',
  'compaction',
  'goal',
  'subagent',
  'gate',
]

const CONCISE_HIDDEN: TimelineBlock['kind'][] = ['thinking', 'tool', 'plan', 'subagent']

describe('blockVisibleIn', () => {
  it('draws every kind in the default mode', () => {
    for (const kind of KINDS) expect(blockVisibleIn('default', kind)).toBe(true)
  })

  it('withholds only the cards concise is meant to spare the reader', () => {
    const hidden = KINDS.filter((kind) => !blockVisibleIn('concise', kind))
    expect(hidden).toEqual(CONCISE_HIDDEN)
  })

  it('keeps the answers and the gates', () => {
    expect(blockVisibleIn('concise', 'assistant')).toBe(true)
    expect(blockVisibleIn('concise', 'approval')).toBe(true)
  })

  it('keeps what is not a card of the run', () => {
    for (const kind of ['user', 'prompt', 'error', 'compaction', 'goal', 'worked'] as TimelineBlock['kind'][]) {
      expect(blockVisibleIn('concise', kind)).toBe(true)
    }
  })
})
