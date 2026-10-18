import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'

import tip from '@/directives/tip'
import { forebrainApi, type AgentRosterRow } from '@/lib/api'
import { setLocale } from '@/locales'
import SubagentsView from './SubagentsView.vue'

/**
 * The running subagents (plan 073): the server answers the running set, the
 * page draws it as a fixed table with a link back into each session and a
 * clock it keeps moving between loads — and no title row above it.
 */
const mounted: VueWrapper[] = []

function subagent(overrides: Partial<AgentRosterRow> = {}): AgentRosterRow {
  return {
    id: 'sub-1',
    kind: 'subagent',
    label: 'explore',
    status: 'running',
    title: '扫描鉴权调用点',
    sessionId: 's-1',
    parentSessionId: 'p-1',
    sessionTitle: '重构 gateway 鉴权中间件',
    elapsedSeconds: 41,
    ...overrides,
  }
}

async function mountPage(): Promise<{ wrapper: VueWrapper; router: Router }> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/:pathMatch(.*)*', component: { template: '<div />' } },
    ],
  })
  const wrapper = mount(SubagentsView, { global: { plugins: [router], directives: { tip } } })
  mounted.push(wrapper)
  await flushPromises()
  return { wrapper, router }
}

describe('SubagentsView', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'agentRosterPage').mockResolvedValue({
      rows: [
        subagent(),
        subagent({ id: 'sub-2', label: 'review', sessionId: '', parentSessionId: '', sessionTitle: '', elapsedSeconds: 12 }),
      ],
      total: 2,
      page: 1,
      pageSize: 10,
    })
  })

  afterEach(() => {
    while (mounted.length) mounted.pop()?.unmount()
    vi.restoreAllMocks()
  })

  it('opens on a search toolbar, the table, the pager and the note — no title row', async () => {
    const { wrapper } = await mountPage()
    expect(wrapper.find('h2').exists()).toBe(false)

    const toolbar = wrapper.find('.forebrain-list-toolbar')
    expect(toolbar.find('input[type="search"]').exists()).toBe(true)
    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 2 条')
    expect(wrapper.find('p.forebrain-list-note').text()).toBe('本主代理所有会话里正在运行的子代理。')
  })

  it('asks the server for the running set alone, and for a search', async () => {
    const { wrapper } = await mountPage()
    const page = vi.mocked(forebrainApi.agentRosterPage)

    expect(page).toHaveBeenCalledTimes(1)
    expect(page.mock.calls[0][0]).toMatchObject({ running: 1, page: 1, pageSize: 10 })

    await wrapper.find('input[type="search"]').setValue('review')
    await new Promise((resolve) => setTimeout(resolve, 350))
    await flushPromises()

    expect(page).toHaveBeenCalledTimes(2)
    expect(page.mock.calls[1][0]).toMatchObject({ q: 'review', page: 1 })
  })

  it('draws the subagent, its task, its session, its status and its clock', async () => {
    const { wrapper } = await mountPage()

    const row = wrapper.find('[data-subagent-row="sub-1"]')
    expect(row.find('td.font-mono').text()).toBe('explore')
    expect(row.text()).toContain('扫描鉴权调用点')
    // 所在会话 names the conversation the subagent ran in.
    expect(row.find('[data-testid="subagent-session-sub-1"]').text()).toBe('重构 gateway 鉴权中间件')
    expect(row.find('.forebrain-badge').text()).toBe('运行中')
    expect(row.text()).toContain('00:41')

    const labels = row.findAll('.forebrain-row-action').map((button) => button.attributes('aria-label'))
    expect(labels).toEqual(['打开会话 explore', '停止 explore'])
  })

  it('links 所在会话 to the parent conversation and 打开会话 to the worker session', async () => {
    const { wrapper, router } = await mountPage()
    const row = wrapper.find('[data-subagent-row="sub-1"]')

    // A subagent's own session is a conversation the operator never opened, so
    // the column goes to the dialogue it ran in.
    await row.find('[data-testid="subagent-session-sub-1"]').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.query.session).toBe('p-1')

    await row.find('[data-testid="subagent-open-sub-1"]').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.query.session).toBe('s-1')
  })

  it('keeps the action column lined up for a row with no session to open', async () => {
    const { wrapper } = await mountPage()

    const row = wrapper.find('[data-subagent-row="sub-2"]')
    expect(row.find('[data-testid="subagent-session-sub-2"]').exists()).toBe(false)
    expect(row.find('.forebrain-icon-slot').exists()).toBe(true)
    expect(row.findAll('.forebrain-row-action').map((button) => button.attributes('aria-label'))).toEqual(['停止 review'])
  })
})
