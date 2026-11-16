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

/**
 * The row's edit icon renames the conversation in place: the title swaps for a
 * field, Enter or blur saves it through the server, Escape cancels. The list
 * keeps the old title until the server agrees, so a refusal never leaves a
 * title the agent does not have.
 */
describe('ChatDrawer inline rename', () => {
  beforeAll(() => setLocale('zh'))
  beforeEach(() => {
    vi.spyOn(forebrainApi, 'projectsList').mockResolvedValue([{ id: 'p1', name: 'alpha' }] as never)
    vi.spyOn(forebrainApi, 'heartbeats').mockResolvedValue([] as never)
  })
  afterEach(() => {
    vi.restoreAllMocks()
    void useChatSessions().fetchSessions('', '')
  })

  async function openDrawer() {
    vi.spyOn(forebrainApi, 'chatSessions').mockResolvedValue({
      records: [session('a', 'alpha'), session('b', 'beta')], total: 2, page: 1, pageSize: 100,
    } as never)
    const wrapper = mount(ChatDrawer, { props: { open: false, railWidth: 264, activeSessionId: null }, attachTo: document.body })
    await wrapper.setProps({ open: true })
    await flushPromises()
    return wrapper
  }

  // The drawer is teleported to the body, so its rows are found there.
  const rows = () => [...document.querySelectorAll('[data-testid="drawer-session-row"]')]
  const editIcon = (index: number) => rows()[index].querySelector('[data-testid="drawer-rename-button"]') as HTMLElement
  const renameInput = () => document.querySelector('[data-testid="drawer-rename-input"]') as HTMLInputElement | null

  function type(input: HTMLInputElement, value: string) {
    input.value = value
    input.dispatchEvent(new Event('input', { bubbles: true }))
  }
  const press = (input: HTMLInputElement, key: string) =>
    input.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }))

  it('swaps a row’s title for a field pre-filled with the current title', async () => {
    const wrapper = await openDrawer()
    editIcon(0).click()
    await flushPromises()
    expect(renameInput()?.value).toBe('chat a')
    expect(document.activeElement).toBe(renameInput())
    wrapper.unmount()
  })

  it('saves the new title on Enter and reflects it in the row', async () => {
    const rename = vi.spyOn(forebrainApi, 'chatSessionTitle').mockResolvedValue({ ok: true, id: 'a', title: 'renamed' } as never)
    const wrapper = await openDrawer()
    editIcon(0).click()
    await flushPromises()
    const input = renameInput()!
    type(input, 'renamed')
    press(input, 'Enter')
    await flushPromises()
    expect(rename).toHaveBeenCalledWith('a', 'renamed')
    expect(renameInput()).toBeNull()
    expect(rows()[0].textContent).toContain('renamed')
    wrapper.unmount()
  })

  it('cancels on Escape without calling the server', async () => {
    const rename = vi.spyOn(forebrainApi, 'chatSessionTitle')
    const wrapper = await openDrawer()
    editIcon(0).click()
    await flushPromises()
    const input = renameInput()!
    type(input, 'discard me')
    press(input, 'Escape')
    await flushPromises()
    expect(rename).not.toHaveBeenCalled()
    expect(renameInput()).toBeNull()
    expect(rows()[0].textContent).toContain('chat a')
    wrapper.unmount()
  })

  it('keeps the old title and shows the failure when the server refuses', async () => {
    vi.spyOn(forebrainApi, 'chatSessionTitle').mockRejectedValue(new Error('gateway down'))
    const wrapper = await openDrawer()
    editIcon(0).click()
    await flushPromises()
    const input = renameInput()!
    type(input, 'renamed')
    press(input, 'Enter')
    await flushPromises()
    const err = document.querySelector('[data-testid="drawer-rename-error"]')
    expect(err?.textContent).toContain('重命名失败')
    expect(err?.textContent).toContain('gateway down')
    expect(rows()[0].textContent).toContain('chat a')
    wrapper.unmount()
  })

  it('does not open the conversation when the edit icon is clicked', async () => {
    const wrapper = await openDrawer()
    editIcon(0).click()
    await flushPromises()
    expect(wrapper.emitted('select')).toBeUndefined()
    wrapper.unmount()
  })
})
