import { afterEach, describe, expect, it, vi } from 'vitest'

import { useChatSessions } from './useChatSessions'
import { forebrainApi } from '@/lib/api'

function record(id: string, source: string) {
  return { id, title: `title-${id}`, createTime: '2026-10-04T00:00:00Z', updateTime: '2026-10-04T00:00:00Z', source }
}

describe('useChatSessions', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("asks for the agent's own conversations and lists what it answers as-is", async () => {
    const spy = vi.spyOn(forebrainApi, 'chatSessions').mockResolvedValue({
      records: [record('one', ''), record('two', 'workshop')],
    })
    const store = useChatSessions()
    await store.fetchSessions()

    // The filter is the server's: the call names the purpose, and whatever
    // comes back is the list — the page never filters it again.
    expect(spy).toHaveBeenCalledWith('')
    expect(store.sessions.value.map((row) => row.id)).toEqual(['one', 'two'])
    expect(store.error.value).toBeNull()
  })
})
