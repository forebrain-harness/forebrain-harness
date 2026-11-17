import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import tip from '@/directives/tip'
import { forebrainApi, type LogPage, type LogRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import { InlineTeleport } from '@/test-utils/portal'

import LogsTab from './LogsTab.vue'

/**
 * Settings · Advanced · Logs (plan 288): the page is a shell over `GET
 * /api/logs`, so what these cases pin is the request each change makes and the
 * row each record draws — the gateway's own querying, filtering and counting
 * are plan 287's tests.
 */

beforeAll(() => setLocale('zh'))
afterEach(() => {
  vi.restoreAllMocks()
  // A row's drawer is portalled to the body; a case that fails before its own
  // unmount must not leave a panel behind for the next one to find.
  document.body.innerHTML = ''
})

function record(patch: Partial<LogRecord> & { id: string }): LogRecord {
  return {
    day: '2026-11-11',
    source: 'tui',
    host: 'mbp-dev',
    pid: 4242,
    t: new Date(2026, 10, 11, 11, 20, 57, 545).toISOString(),
    lvl: 'INFO',
    kind: 'log',
    msg: 'agent load',
    ...patch,
  }
}

function page(patch: Partial<LogPage> = {}): LogPage {
  return {
    records: [],
    total: 0,
    page: 1,
    pageSize: 50,
    levelCounts: { debug: 0, info: 0, warn: 0, error: 0 },
    buckets: [],
    truncated: false,
    ...patch,
  }
}

const CALL = record({
  id: 'call-response',
  kind: 'llm',
  msg: 'llm response',
  lvl: 'INFO',
  session: 's1',
  sessionTitle: '修复页脚 token 统计',
  run: 'run-1',
  attrs: { x: 'gw-1-000001', method: 'POST', url: 'https://api.example.com/v1/chat', model: 'glm-5.3', status: 200, durMs: 1200, respBytes: 2048 },
})

const TURN = record({ id: 'turn-start', kind: 'turn', msg: '轮次开始', session: 's1', sessionTitle: '修复页脚 token 统计', run: 'run-1' })

/** Mount the tab with every endpoint it reads stubbed, one page of rows. */
function mountTab(
  answer: LogPage = page({ records: [CALL, TURN], total: 2, levelCounts: { debug: 9, info: 2, warn: 1, error: 0 } }),
  props: Record<string, unknown> = {},
): VueWrapper {
  if (!vi.isMockFunction(forebrainApi.logs)) vi.spyOn(forebrainApi, 'logs').mockResolvedValue(answer)
  if (!vi.isMockFunction(forebrainApi.chatSessions)) {
    vi.spyOn(forebrainApi, 'chatSessions').mockResolvedValue({
      records: [{ id: 's1', title: '修复页脚 token 统计', createTime: '', updateTime: '', source: 'tui', projectName: 'forebrain-harness' }],
      total: 1,
      page: 1,
      pageSize: 20,
    })
  }
  if (!vi.isMockFunction(forebrainApi.projectsPage)) {
    vi.spyOn(forebrainApi, 'projectsPage').mockResolvedValue({ rows: [{ id: 'p1', name: 'forebrain-harness' } as never], total: 1, page: 1, pageSize: 100 })
  }
  return mount(LogsTab, {
    props,
    attachTo: document.body,
    global: { stubs: { teleport: InlineTeleport }, directives: { tip } },
  })
}

function lastQuery(): Record<string, unknown> {
  const calls = vi.mocked(forebrainApi.logs).mock.calls
  return calls[calls.length - 1][0] as unknown as Record<string, unknown>
}

beforeEach(() => {
  vi.restoreAllMocks()
})

describe('LogsTab', () => {
  it('reads the last hour of the loud three levels once, when the page opens', async () => {
    const logs = vi.spyOn(forebrainApi, 'logs').mockResolvedValue(page())
    const wrapper = mountTab(page())
    await flushPromises()

    expect(logs).toHaveBeenCalledTimes(1)
    const query = lastQuery()
    expect(query.level).toBe('INFO,WARN,ERROR')
    expect(query.page).toBe(1)
    expect(query.page_size).toBe(50)
    const span = Date.parse(String(query.to)) - Date.parse(String(query.from))
    expect(span).toBe(60 * 60 * 1000)
    wrapper.unmount()
  })

  it('draws a call’s own numbers, and the level buttons’ counts from the answer', async () => {
    const wrapper = mountTab()
    await flushPromises()

    const row = wrapper.find('[data-log-row="call-response"]')
    expect(row.text()).toContain('200')
    expect(row.text()).toContain('1.2s')
    expect(row.text()).toContain('model=glm-5.3')
    expect(row.text()).toContain('修复页脚 token 统计')
    expect(wrapper.find('[data-testid="logs-level-debug"] em').text()).toBe('9')
    wrapper.unmount()
  })

  it('asks for all four levels from the first page when 调试 is switched on', async () => {
    const logs = vi.spyOn(forebrainApi, 'logs').mockResolvedValue(page({ total: 120, page: 2 }))
    const wrapper = mountTab()
    await flushPromises()
    // A read that served page 2 leaves the page on 2 …
    expect(wrapper.find('[data-testid="logs-level-debug"]').attributes('aria-pressed')).toBe('false')

    await wrapper.find('[data-testid="logs-level-debug"]').trigger('click')
    await flushPromises()

    expect(logs).toHaveBeenLastCalledWith(expect.objectContaining({ level: 'DEBUG,INFO,WARN,ERROR', page: 1 }))
    expect(wrapper.find('[data-testid="logs-level-debug"]').attributes('aria-pressed')).toBe('true')
    wrapper.unmount()
  })

  it('sends each dimension the reader narrows by', async () => {
    const logs = vi.spyOn(forebrainApi, 'logs').mockResolvedValue(page())
    const wrapper = mountTab()
    await flushPromises()

    await wrapper.find('[data-testid="logs-source-gateway"]').trigger('click')
    await flushPromises()
    expect(lastQuery().source).toBe('gateway')

    await wrapper.find('[data-testid="logs-project"]').setValue('p1')
    await flushPromises()
    expect(lastQuery().project).toBe('p1')

    await wrapper.find('[data-testid="logs-kind"]').setValue('llm')
    await flushPromises()
    expect(lastQuery().kind).toBe('llm')

    // The conversation menu is the server's own search, one page of it.
    await wrapper.find('[data-testid="logs-session"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="logs-session-option-s1"]').trigger('click')
    await flushPromises()
    expect(lastQuery().session).toBe('s1')

    expect(logs).toHaveBeenCalledTimes(5)
    wrapper.unmount()
  })

  it('asks the gateway once after the search box pauses', async () => {
    const logs = vi.spyOn(forebrainApi, 'logs').mockResolvedValue(page())
    const wrapper = mountTab()
    await flushPromises()

    const search = wrapper.find('[data-testid="logs-search"]')
    await search.setValue('g')
    await search.setValue('go')
    await search.setValue('gone')
    await new Promise((resolve) => setTimeout(resolve, 400))
    await flushPromises()

    expect(logs).toHaveBeenCalledTimes(2)
    expect(lastQuery().q).toBe('gone')
    wrapper.unmount()
  })

  it('narrows to a row’s conversation from its own icon, without opening the call', async () => {
    const logs = vi.spyOn(forebrainApi, 'logs').mockResolvedValue(page({ records: [CALL], total: 1 }))
    vi.spyOn(forebrainApi, 'logExchange').mockResolvedValue({
      id: 'gw-1-000001',
      day: '2026-11-11',
      request: null,
      response: null,
      scope: { source: 'tui', host: 'mbp-dev', pid: 4242 },
      summary: null,
      concurrent: [],
    })
    const wrapper = mountTab(page({ records: [CALL], total: 1 }))
    await flushPromises()

    await wrapper.find('[data-testid="logs-only-session-call-response"]').trigger('click')
    await flushPromises()

    expect(logs).toHaveBeenLastCalledWith(expect.objectContaining({ session: 's1' }))
    expect(forebrainApi.logExchange).not.toHaveBeenCalled()
    expect(document.querySelector('[data-testid="log-drawer-title"]')).toBeNull()
    wrapper.unmount()
  })

  it('opens a call’s drawer from anywhere else on its row', async () => {
    const exchange = vi.spyOn(forebrainApi, 'logExchange').mockResolvedValue({
      id: 'gw-1-000001',
      day: '2026-11-11',
      request: { t: CALL.t, method: 'POST', url: 'https://api.example.com/v1/chat', model: 'glm-5.3', bytes: 120, whole: true },
      response: { t: CALL.t, status: 200, durMs: 1200, ttfbMs: 400, maxGapMs: 200, bytes: 2048 },
      scope: { source: 'tui', host: 'mbp-dev', pid: 4242, session: 's1', sessionTitle: '修复页脚 token 统计', run: 'run-1' },
      summary: null,
      concurrent: [],
    })
    const wrapper = mountTab(page({ records: [CALL], total: 1 }))
    await flushPromises()

    await wrapper.find('[data-log-row="call-response"]').trigger('click')
    await flushPromises()

    expect(exchange).toHaveBeenCalledWith('2026-11-11', 'gw-1-000001')
    expect(document.querySelector('[data-testid="log-drawer-title"]')?.textContent).toContain('模型调用 修复页脚 token 统计')
    wrapper.unmount()
  })

  it('turns a dragged segment of the histogram into that range', async () => {
    const logs = vi.spyOn(forebrainApi, 'logs').mockResolvedValue(page())
    const wrapper = mountTab()
    await flushPromises()
    const before = lastQuery()
    const from = Date.parse(String(before.from))
    const to = Date.parse(String(before.to))

    const bars = wrapper.find('[data-testid="logs-histogram"]')
    bars.element.getBoundingClientRect = () => ({ left: 0, width: 600, top: 0, height: 56, right: 600, bottom: 56, x: 0, y: 0, toJSON: () => ({}) }) as DOMRect
    await bars.trigger('mousedown', { clientX: 150 })
    window.dispatchEvent(new MouseEvent('mousemove', { clientX: 300 }))
    window.dispatchEvent(new MouseEvent('mouseup'))
    await flushPromises()

    const after = lastQuery()
    const dragged = { from: Date.parse(String(after.from)), to: Date.parse(String(after.to)) }
    // The quarter of the range the drag covered, to within the clock's own drift.
    expect(Math.abs(dragged.from - (from + (to - from) * 0.25))).toBeLessThan(2000)
    expect(Math.abs(dragged.to - (from + (to - from) * 0.5))).toBeLessThan(2000)
    expect(logs).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('says a range has nothing, and offers the three ways to widen it', async () => {
    const wrapper = mountTab(page({ records: [], total: 0 }))
    await flushPromises()

    expect(wrapper.find('[data-testid="logs-empty"]').text()).toContain('这个范围内没有符合条件的日志')
    expect(wrapper.find('[data-testid="logs-empty-week"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="logs-empty-levels"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="logs-empty-reset"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('keeps the filters when the request fails, and retries them', async () => {
    const logs = vi.spyOn(forebrainApi, 'logs')
    logs.mockRejectedValueOnce(new Error('from is after to'))
    logs.mockResolvedValue(page({ records: [TURN], total: 1 }))
    const wrapper = mountTab(page({ records: [TURN], total: 1 }))
    await flushPromises()

    expect(wrapper.find('[data-testid="logs-error"]').text()).toContain('from is after to')
    await wrapper.find('[data-testid="logs-retry"]').trigger('click')
    await flushPromises()

    expect(logs).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-testid="logs-error"]').exists()).toBe(false)
    expect(wrapper.find('[data-log-row="turn-start"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('says 「以上」 where the scan stopped at its ceiling', async () => {
    const answer = page({ records: [TURN], total: 120, truncated: true })
    const wrapper = mountTab(answer)
    await flushPromises()

    expect(wrapper.find('[data-testid="logs-histogram-count"]').text()).toBe('120 条以上')
    expect(wrapper.find('[data-testid="pager-total"]').text()).toBe('120 条以上')
    wrapper.unmount()
  })

  it('opens filtered to the conversation and range the URL names', async () => {
    vi.spyOn(forebrainApi, 'logs').mockResolvedValue(page())
    const wrapper = mountTab(page(), {
      session: 's1',
      from: new Date(Date.UTC(2026, 10, 11, 3, 0, 0)).toISOString(),
      to: new Date(Date.UTC(2026, 10, 11, 4, 0, 0)).toISOString(),
    })
    await flushPromises()

    const query = lastQuery()
    expect(query.session).toBe('s1')
    expect(query.from).toBe('2026-11-11T03:00:00Z')
    expect(query.to).toBe('2026-11-11T04:00:00Z')
    expect(wrapper.find('[data-testid="logs-session"]').text()).toContain('修复页脚 token 统计')
    wrapper.unmount()
  })
})
