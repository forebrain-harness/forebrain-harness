import { describe, expect, it } from 'vitest'

import { blockOnActivityLine, groupActivityRuns } from './activityRuns'

describe('groupActivityRuns', () => {
  it('groups consecutive on-line blocks into one run', () => {
    const blocks = [
      { kind: 'thinking' },
      { kind: 'assistant' },
      { kind: 'tool' },
      { kind: 'tool' },
    ]
    expect(groupActivityRuns(blocks, blockOnActivityLine)).toEqual([
      { line: true, items: blocks },
    ])
  })

  it('breaks the line at a block that is not part of it, keeping the order', () => {
    const blocks = [
      { kind: 'assistant' },
      { kind: 'tool' },
      { kind: 'compaction' },
      { kind: 'tool' },
      { kind: 'error' },
      { kind: 'thinking' },
    ]
    expect(groupActivityRuns(blocks, blockOnActivityLine)).toEqual([
      { line: true, items: [{ kind: 'assistant' }, { kind: 'tool' }] },
      { line: false, item: { kind: 'compaction' } },
      { line: true, items: [{ kind: 'tool' }] },
      { line: false, item: { kind: 'error' } },
      { line: true, items: [{ kind: 'thinking' }] },
    ])
  })

  it('carries an empty list through', () => {
    expect(groupActivityRuns([], blockOnActivityLine)).toEqual([])
  })
})

describe('blockOnActivityLine', () => {
  it('hangs the cards of the turn itself on the line and nothing else', () => {
    for (const kind of ['assistant', 'thinking', 'tool', 'subagent', 'plan', 'approval']) {
      expect(blockOnActivityLine({ kind })).toBe(true)
    }
    for (const kind of ['user', 'compaction', 'goal', 'error', 'gate', 'worked', 'prompt']) {
      expect(blockOnActivityLine({ kind })).toBe(false)
    }
  })
})
