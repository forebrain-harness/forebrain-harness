import { afterEach, describe, expect, it, vi } from 'vitest'
import { nextTick, ref } from 'vue'

import { readViewMode, useViewMode, viewModeStorageKey, writeViewMode } from './useViewMode'

/**
 * The reading mode is per view and survives the page: the conversation keeps
 * its own under its session, a subagent's tab keeps its own under the session
 * and the agent, and a storage that refuses the read (private browsing throws)
 * reads as the full drawing rather than as an error.
 */
describe('useViewMode', () => {
  afterEach(() => {
    localStorage.clear()
    vi.restoreAllMocks()
  })

  it('names each view under its session and, for a tab, its agent', () => {
    expect(viewModeStorageKey('s1')).toBe('forebrain.viewMode.s1')
    expect(viewModeStorageKey('s1', 'agent-7')).toBe('forebrain.viewMode.s1.agent-7')
    expect(viewModeStorageKey('')).toBe('')
  })

  it('draws everything until the view was left in concise', () => {
    expect(readViewMode('s1')).toBe('default')
    writeViewMode('concise', 's1')
    expect(readViewMode('s1')).toBe('concise')
    // One tab's choice is not another's, and not the conversation's.
    expect(readViewMode('s1', 'agent-7')).toBe('default')
    expect(readViewMode('s2')).toBe('default')
  })

  it('ignores a stored value it does not recognise', () => {
    localStorage.setItem('forebrain.viewMode.s1', 'everything')
    expect(readViewMode('s1')).toBe('default')
  })

  it('reads and writes a storage that throws as the default, without throwing', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('storage is blocked')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('storage is blocked')
    })
    expect(readViewMode('s1')).toBe('default')
    expect(() => writeViewMode('concise', 's1')).not.toThrow()

    const { viewMode, setViewMode } = useViewMode(() => 's1')
    expect(viewMode.value).toBe('default')
    expect(() => setViewMode('concise')).not.toThrow()
    expect(viewMode.value).toBe('concise')
  })

  it('returns to the mode a session was left in when the view switches to it', async () => {
    writeViewMode('concise', 's1')
    const sessionId = ref('s1')
    const { viewMode, setViewMode } = useViewMode(() => sessionId.value)
    expect(viewMode.value).toBe('concise')

    sessionId.value = 's2'
    await nextTick()
    expect(viewMode.value).toBe('default')

    setViewMode('concise')
    expect(readViewMode('s2')).toBe('concise')
    expect(readViewMode('s1')).toBe('concise')
  })
})
