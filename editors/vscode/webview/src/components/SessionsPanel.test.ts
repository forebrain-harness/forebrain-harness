import { afterEach, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import type { InternalAxiosRequestConfig, AxiosResponse } from 'axios'
import { setForebrainHttpAdapter } from '@/lib/api'
import SessionsPanel from './SessionsPanel.vue'

/** Dispatches axios traffic by path prefix, the way the host RPC routes it.
 *  A route may queue its answers: each entry carries its own body and status,
 *  the first hit taking the first entry and so on, staying on the last one.
 *  The params ride in `config.params` as the URLSearchParams listParams built,
 *  so the log records them through their string form. */
function stubHttp(routes: Array<{ prefix: string; queue?: Array<{ status?: number; body: unknown }> }>) {
  const paramLog: Array<Record<string, unknown>> = []
  const cursors = new Map<object, number>()
  setForebrainHttpAdapter(async (config: InternalAxiosRequestConfig) => {
    const url = `${String(config.baseURL ?? '')}${String(config.url ?? '')}`
    const raw = config.params
    const search = raw instanceof URLSearchParams ? raw : new URLSearchParams()
    paramLog.push({ __url: url, q: search.get('q'), page: search.get('page') })
    const route = routes.find((r) => url.startsWith(r.prefix))
    let body: unknown = { error: 'no route' }
    let status = route ? 200 : 404
    if (route && route.queue) {
      const taken = cursors.get(route) ?? 0
      cursors.set(route, taken + 1)
      const entry = route.queue[Math.min(taken, route.queue.length - 1)]
      body = entry.body
      status = entry.status ?? 200
    }
    const answer = {
      data: JSON.stringify(body),
      status,
      statusText: '',
      headers: {},
      config,
    } as AxiosResponse
    if (status >= 400) return Promise.reject({ response: answer, config, message: `HTTP ${status}` })
    return Promise.resolve(answer)
  })
  return paramLog
}

const listBody = {
  records: [
    { id: 's-1', title: '评审 391', createTime: '2026-11-27T10:00:00Z', updateTime: '2026-11-27T11:00:00Z', source: '', projectName: 'forebrain-harness' },
    { id: 's-2', title: null, createTime: '2026-11-27T09:00:00Z', updateTime: '2026-11-27T09:30:00Z', source: '', projectName: '' },
  ],
  total: 2,
}

/** The chain from adapter answer to drawn DOM crosses several microtasks and
 *  Vue's own render tick; give it real time. */
async function flush() {
  for (let i = 0; i < 6; i += 1) await new Promise((resolve) => setTimeout(resolve, 25))
}

afterEach(() => setForebrainHttpAdapter(null))

describe('the sessions view', () => {
  it('lists the shared session store: title, project, time, and the row the chat has open', async () => {
    stubHttp([{ prefix: '/api/chat/sessions', queue: [{ body: listBody }] }])
    const wrapper = mount(SessionsPanel, { props: { activeSessionId: 's-1' } })
    await flush()
    const rows = wrapper.findAll('[data-testid="sessions-row"]')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('评审 391')
    expect(rows[0].classes()).toContain('wv-sess-row--on')
    expect(rows[1].text()).toContain('未命名对话')
    expect(wrapper.text()).toContain('forebrain-harness')
  })

  it('opens the picked conversation and offers the new chat', async () => {
    stubHttp([{ prefix: '/api/chat/sessions', queue: [{ body: listBody }] }])
    const wrapper = mount(SessionsPanel, { props: { activeSessionId: '' } })
    await flush()
    await wrapper.findAll('[data-testid="sessions-row"]')[1].trigger('click')
    expect(wrapper.emitted('open')?.[0]).toEqual(['s-2'])
    await wrapper.get('[data-testid="sessions-new-chat"]').trigger('click')
    expect(wrapper.emitted('create')).toHaveLength(1)
  })

  it('says the load failed and gets back in through retry', async () => {
    const paramLog = stubHttp([{ prefix: '/api/chat/sessions', queue: [{ status: 500, body: { error: 'gateway down' } }, { body: listBody }] }])
    const wrapper = mount(SessionsPanel, { props: {} })
    await flush()
    expect(wrapper.get('[data-testid="sessions-error"]').text()).toContain('会话加载失败')
    // The way back in: the retry, hitting the same list again.
    await wrapper.get('[data-testid="sessions-retry"]').trigger('click')
    await flush()
    await flush()
    expect(wrapper.find('[data-testid="sessions-error"]').exists()).toBe(false)
    expect(wrapper.findAll('[data-testid="sessions-row"]')).toHaveLength(2)
    expect(paramLog.some((params) => String(params.__url).startsWith('/api/chat/sessions'))).toBe(true)
    expect(paramLog).toHaveLength(2)
  })

  it('searches server-side and says when nothing matches', async () => {
    const paramLog = stubHttp([{ prefix: '/api/chat/sessions', queue: [{ body: listBody }, { body: { records: [], total: 0 } }] }])
    const wrapper = mount(SessionsPanel, { props: {} })
    await flush()
    const input = wrapper.get('[data-testid="sessions-search"]')
    await input.setValue('391')
    // The debounce settles before the request goes out.
    await new Promise((resolve) => setTimeout(resolve, 400))
    expect(wrapper.get('[data-testid="sessions-empty"]').text()).toContain('没有匹配的会话')
    expect(paramLog.some((params) => params.q === '391')).toBe(true)
  })

  it('pages on load-more when the server says there is more', async () => {
    const paramLog = stubHttp([
      {
        prefix: '/api/chat/sessions',
        queue: [
          { body: { records: [listBody.records[0]], total: 2 } },
          { body: { records: [listBody.records[1]], total: 2 } },
        ],
      },
    ])
    const wrapper = mount(SessionsPanel, { props: {} })
    await flush()
    expect(wrapper.find('[data-testid="sessions-load-more"]').exists()).toBe(true)
    await wrapper.get('[data-testid="sessions-load-more"]').trigger('click')
    await flush()
    expect(wrapper.findAll('[data-testid="sessions-row"]')).toHaveLength(2)
    expect(paramLog.some((params) => params.page === '2')).toBe(true)
  })
})
