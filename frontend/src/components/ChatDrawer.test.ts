import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { config, flushPromises, mount } from '@vue/test-utils'

import tip from '@/directives/tip'
import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'
import { useChatSessions } from '@/composables/useChatSessions'
import { openSelect, pickOption } from '@/test-utils/formControls'
import ChatDrawer from './ChatDrawer.vue'

config.global.directives = { tip }

const session = (id: string, projectName: string) => ({
  id, title: `chat ${id}`, createTime: '2026-10-23T00:00:00Z', updateTime: '2026-10-23T00:00:00Z', source: '', projectName,
})

/**
 * The drawer's project filter (the prototype's .d-filter): the list is the
 * server's, so choosing a project asks it for that project's page, and "all
 * projects" asks for every one.
 */
describe('ChatDrawer project filter', () => {
  beforeAll(() => setLocale('zh'))
  beforeEach(() => {
    vi.spyOn(forebrainApi, 'projectsList').mockResolvedValue([
      { id: 'p1', name: 'alpha' }, { id: 'p2', name: 'beta' },
    ] as never)
    vi.spyOn(forebrainApi, 'heartbeats').mockResolvedValue([] as never)
  })
  afterEach(() => {
    vi.restoreAllMocks()
    void useChatSessions().fetchSessions('', '')
  })

  // The drawer is teleported to the body, so the filter is found there.
  const filter = () => document.querySelector('[data-testid="drawer-project-filter"]') as HTMLElement

  async function openDrawer() {
    const list = vi.spyOn(forebrainApi, 'chatSessions').mockResolvedValue({
      records: [session('a', 'alpha'), session('b', 'beta')], total: 2, page: 1, pageSize: 100,
    } as never)
    const wrapper = mount(ChatDrawer, { props: { open: false, railWidth: 264, activeSessionId: null }, attachTo: document.body })
    await wrapper.setProps({ open: true })
    await flushPromises()
    return { wrapper, list }
  }

  it('offers every project, and the projects the gateway lists', async () => {
    const { wrapper } = await openDrawer()
    expect(filter().textContent).toContain('全部项目')
    await openSelect(filter())
    const options = [...document.querySelectorAll('[role="option"]')].map((option) => (option.textContent ?? '').trim())
    expect(options).toEqual(['全部项目', 'alpha', 'beta'])
    wrapper.unmount()
  })

  it('asks the server for the chosen project’s conversations, and for all of them again', async () => {
    const { wrapper, list } = await openDrawer()
    list.mockClear()

    await pickOption(filter(), 'beta')
    await flushPromises()
    expect(list).toHaveBeenLastCalledWith('', { page: 1, pageSize: 100, q: '', projectId: 'p2' })

    await pickOption(filter(), '全部项目')
    await flushPromises()
    expect(list).toHaveBeenLastCalledWith('', { page: 1, pageSize: 100, q: '', projectId: undefined })
    wrapper.unmount()
  })

  it('says nothing matches when the chosen project has no conversation', async () => {
    const { wrapper, list } = await openDrawer()
    list.mockResolvedValue({ records: [], total: 0, page: 1, pageSize: 100 } as never)
    await pickOption(filter(), 'alpha')
    await flushPromises()
    expect(document.body.textContent).toContain('没有匹配的会话')
    wrapper.unmount()
  })
})
