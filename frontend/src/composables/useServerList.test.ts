import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'
import { nextTick } from 'vue'

import { listParams, type ListQuery } from '@/lib/api'
import { useServerList } from './useServerList'

interface Row {
  id: string
}

type Answer = { rows: Row[]; total: number; page: number; pageSize: number }

/** A fetch that records every query it was asked, and answers one page. */
function recordingFetch(answer: (query: ListQuery) => Answer = (query) => answerOnePage(query)) {
  const calls: ListQuery[] = []
  const fetch = async (query: ListQuery): Promise<Answer> => {
    calls.push(query)
    return answer(query)
  }
  return { calls, fetch }
}

function answerOnePage(query: ListQuery, rows: Row[] = [{ id: 'a' }]): Answer {
  return { rows, total: rows.length, page: query.page, pageSize: query.pageSize }
}

/** The list's own state: which page it asks for, and when. */
describe('useServerList', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('loads on demand, from the first page', async () => {
    const { calls, fetch } = recordingFetch()
    const list = useServerList<Row>(fetch)
    expect(calls).toHaveLength(0)
    await list.load()
    expect(calls).toHaveLength(1)
    expect(calls[0]).toMatchObject({ page: 1, pageSize: 10, q: '' })
    expect(list.rows.value).toEqual([{ id: 'a' }])
    expect(list.total.value).toBe(1)
  })

  it('asks once, after the search pauses', async () => {
    vi.useFakeTimers()
    const { calls, fetch } = recordingFetch()
    const list = useServerList<Row>(fetch)

    list.q.value = 'abc'
    await nextTick()
    await vi.advanceTimersByTimeAsync(299)
    expect(calls).toHaveLength(0)
    await vi.advanceTimersByTimeAsync(1)
    expect(calls).toHaveLength(1)
    expect(calls[0]).toMatchObject({ q: 'abc', page: 1 })
  })

  it('returns to the first page for a new search', async () => {
    vi.useFakeTimers()
    const { calls, fetch } = recordingFetch()
    const list = useServerList<Row>(fetch)

    list.page.value = 3
    await nextTick()
    await flushPromises()
    expect(calls).toHaveLength(1)
    expect(list.page.value).toBe(3)

    list.q.value = 'abc'
    await nextTick()
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    expect(calls).toHaveLength(2)
    expect(calls[1]).toMatchObject({ q: 'abc', page: 1 })
    expect(list.page.value).toBe(1)
  })

  it('returns to the first page when a filter, the order or the page size changes', async () => {
    const { calls, fetch } = recordingFetch()
    const list = useServerList<Row>(fetch)

    list.page.value = 4
    await flushPromises()
    expect(calls).toHaveLength(1)

    list.filters.value = { scope: 'global' }
    await flushPromises()
    expect(calls).toHaveLength(2)
    expect(calls[1]).toMatchObject({ page: 1, scope: 'global' })

    list.sort.value = 'updated'
    await flushPromises()
    list.order.value = 'asc'
    await flushPromises()
    list.pageSize.value = 50
    await flushPromises()
    expect(calls).toHaveLength(5)
    expect(calls[4]).toMatchObject({ page: 1, pageSize: 50, sort: 'updated', order: 'asc' })
  })

  it('takes the page the server served without asking for it again', async () => {
    const { calls, fetch } = recordingFetch(() => ({ rows: [], total: 5, page: 2, pageSize: 10 }))
    const list = useServerList<Row>(fetch)

    list.page.value = 3
    await flushPromises()
    expect(calls).toHaveLength(1)
    expect(list.page.value).toBe(2)
    await flushPromises()
    expect(calls).toHaveLength(1)
  })

  it('applies only the newest answer', async () => {
    let releaseFirst: (answer: Answer) => void = () => {}
    const first = new Promise<Answer>((resolve) => {
      releaseFirst = resolve
    })
    const calls: ListQuery[] = []
    const fetch = async (query: ListQuery): Promise<Answer> => {
      calls.push(query)
      if (calls.length === 1) return first
      return { rows: [{ id: 'new' }], total: 1, page: query.page, pageSize: query.pageSize }
    }
    const list = useServerList<Row>(fetch)

    const older = list.load()
    const newer = list.load()
    await newer
    releaseFirst({ rows: [{ id: 'old' }], total: 9, page: 1, pageSize: 10 })
    await older
    await flushPromises()

    expect(list.rows.value).toEqual([{ id: 'new' }])
    expect(list.total.value).toBe(1)
  })

  it('keeps the message a failed load was given', async () => {
    const list = useServerList<Row>(async () => {
      throw new Error('boom')
    })
    await list.load()
    expect(list.error.value).toBe('boom')
  })
})

/** The query string those requests travel as. */
describe('listParams', () => {
  it('carries the page, the order and the filters in snake case, and skips what is unset', () => {
    const params = listParams({
      q: '',
      sort: 'updated',
      order: 'asc',
      page: 2,
      pageSize: 50,
      scope: 'global',
      projectId: 'p1',
      empty: '',
    })
    expect(params.get('page')).toBe('2')
    expect(params.get('page_size')).toBe('50')
    expect(params.get('sort')).toBe('updated')
    expect(params.get('order')).toBe('asc')
    expect(params.get('scope')).toBe('global')
    expect(params.get('project_id')).toBe('p1')
    expect(params.has('q')).toBe(false)
    expect(params.has('empty')).toBe(false)
  })
})
