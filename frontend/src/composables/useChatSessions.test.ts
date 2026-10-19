import { afterEach, describe, expect, it, vi } from 'vitest'

import { useChatSessions } from './useChatSessions'
import { forebrainApi } from '@/lib/api'

function record(id: string, source: string) {
  return { id, title: `title-${id}`, createTime: '2026-10-04T00:00:00Z', updateTime: '2026-10-04T00:00:00Z', source }
}

/** A full drawer page, so the composable's page-size arithmetic is real. */
function fullPage(prefix: string): ReturnType<typeof record>[] {
  return Array.from({ length: 100 }, (_, i) => record(`${prefix}-${String(i).padStart(3, '0')}`, ''))
}

describe('useChatSessions', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("asks for the agent's own conversations and lists what it answers as-is", async () => {
    const spy = vi.spyOn(forebrainApi, 'chatSessions').mockResolvedValue({
      records: [record('one', ''), record('two', 'workshop')],
      total: 2,
      page: 1,
      pageSize: 100,
    })
    const store = useChatSessions()
    await store.fetchSessions()

    // The filter is the server's: the call names the purpose and the first
    // page, and whatever comes back is the list — the page never filters it
    // again.
    expect(spy).toHaveBeenCalledWith('', { page: 1, pageSize: 100, q: '' })
    expect(store.sessions.value.map((row) => row.id)).toEqual(['one', 'two'])
    expect(store.error.value).toBeNull()
    // The whole answer fit in one page: nothing more to load.
    expect(store.hasMore.value).toBe(false)
  })

  it('loadMore appends the next page deduped by id, and the server says when it ends', async () => {
    const store = useChatSessions()
    vi.spyOn(forebrainApi, 'chatSessions')
      .mockResolvedValueOnce({ records: [record('one', ''), record('two', '')], total: 5, page: 1, pageSize: 100 })
      // The next page brings one new row and one the live list already
      // reordered into it; a short page means the list is over.
      .mockResolvedValueOnce({ records: [record('three', ''), record('one', '')], total: 5, page: 2, pageSize: 100 })
    await store.fetchSessions()
    expect(store.hasMore.value).toBe(true)

    await store.loadMore()
    expect(store.sessions.value.map((row) => row.id)).toEqual(['one', 'two', 'three'])
    // A page that came back short leaves nothing more to ask for.
    expect(store.hasMore.value).toBe(false)
    expect(store.loadingMore.value).toBe(false)
  })

  it('hasMore stays true while full pages keep coming', async () => {
    const store = useChatSessions()
    vi.spyOn(forebrainApi, 'chatSessions')
      .mockResolvedValueOnce({ records: fullPage('p1'), total: 350, page: 1, pageSize: 100 })
      .mockResolvedValueOnce({ records: fullPage('p2'), total: 350, page: 2, pageSize: 100 })
    await store.fetchSessions()
    expect(store.sessions.value).toHaveLength(100)
    expect(store.hasMore.value).toBe(true)

    await store.loadMore()
    // 200 of 350 listed: the button stays.
    expect(store.sessions.value).toHaveLength(200)
    expect(store.page.value).toBe(2)
    expect(store.hasMore.value).toBe(true)
  })

  it('a failed loadMore keeps what is loaded', async () => {
    const store = useChatSessions()
    vi.spyOn(forebrainApi, 'chatSessions')
      .mockResolvedValueOnce({ records: [record('one', '')], total: 3, page: 1, pageSize: 100 })
      .mockRejectedValueOnce(new Error('gateway down'))
    await store.fetchSessions()

    await store.loadMore()
    expect(store.sessions.value.map((row) => row.id)).toEqual(['one'])
    expect(store.hasMore.value).toBe(true)
    expect(store.error.value).toBe('gateway down')
  })

  it('a search refetches the first page with the term', async () => {
    const spy = vi.spyOn(forebrainApi, 'chatSessions').mockResolvedValue({
      records: [record('match', '')],
      total: 1,
      page: 1,
      pageSize: 100,
    })
    const store = useChatSessions()
    await store.fetchSessions('needle')

    expect(spy).toHaveBeenCalledWith('', { page: 1, pageSize: 100, q: 'needle' })
    expect(store.sessions.value.map((row) => row.id)).toEqual(['match'])
    expect(store.hasMore.value).toBe(false)
  })
})
