import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { forebrainApi, type HookRowRecord } from '@/lib/api'
import tip from '@/directives/tip'
import { setLocale } from '@/locales'
import HooksTab from './HooksTab.vue'

/**
 * The settings page's hooks tab (plan 076): the search and the buttons in the
 * toolbar, a fixed table with one icon action pair per row, the pager under it
 * and the tab's note below that. The store is still one map, so an edit and a
 * delete read the whole map first and write it back.
 */
const ROWS: HookRowRecord[] = [
  { event: 'PreToolUse', matcher: 'shell', command: 'scripts/guard.sh', matcherIdx: 0, commandIdx: 0 },
  { event: 'PostToolUse', matcher: '', command: 'scripts/log.sh', matcherIdx: 0, commandIdx: 0 },
]

const MAP = {
  PreToolUse: [{ matcher: 'shell', hooks: [{ type: 'command', command: 'scripts/guard.sh' }] }],
  PostToolUse: [{ hooks: [{ type: 'command', command: 'scripts/log.sh' }] }],
}

function pageOf(rows: HookRowRecord[] = ROWS) {
  return { rows, total: rows.length, page: 1, pageSize: 10, events: ['PreToolUse', 'PostToolUse'], knownTypes: ['command', 'prompt'] }
}

async function mountTab(): Promise<VueWrapper> {
  const wrapper = mount(HooksTab, { global: { directives: { tip }, stubs: { teleport: true } } })
  await flushPromises()
  return wrapper
}

describe('HooksTab', () => {
  beforeEach(() => {
    setLocale('zh')
    vi.spyOn(forebrainApi, 'hooksPage').mockResolvedValue(pageOf())
    vi.spyOn(forebrainApi, 'hooks').mockResolvedValue({ hooks: MAP, events: ['PreToolUse', 'PostToolUse'], knownTypes: ['command', 'prompt'] })
    vi.spyOn(forebrainApi, 'saveHooks').mockResolvedValue({ applied: true })
    vi.spyOn(forebrainApi, 'toolsList').mockResolvedValue([{ name: 'shell' } as never])
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('opens on the search toolbar, the table, the pager and the note — no title row', async () => {
    const wrapper = await mountTab()

    expect(wrapper.find('h2').exists()).toBe(false)
    const search = wrapper.find('.forebrain-list-toolbar input[type="search"]')
    expect(search.attributes('placeholder')).toBe('搜索事件或命令')
    expect(search.classes()).toContain('h-[34px]')
    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 2 条')
    expect(wrapper.find('[data-testid="hooks-note"]').text()).toBe('对所有主代理生效。')
    // The action column is icons only; the words are their tips.
    const actions = wrapper.findAll('.forebrain-row-action')
    expect(actions).toHaveLength(4)
    for (const action of actions) expect(action.text()).toBe('')
  })

  it('renders one row per command, with the matcher in words when it matches every tool', async () => {
    const wrapper = await mountTab()
    const rows = wrapper.findAll('[data-testid="hook-row"]')

    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('PreToolUse')
    expect(rows[0].text()).toContain('shell')
    expect(rows[0].text()).toContain('scripts/guard.sh')
    expect(rows[1].text()).toContain('匹配全部工具')
  })

  it('searches on the server, back at the first page', async () => {
    vi.useFakeTimers()
    const wrapper = await mountTab()
    expect(forebrainApi.hooksPage).toHaveBeenCalledTimes(1)
    expect(vi.mocked(forebrainApi.hooksPage).mock.calls[0][0]).toMatchObject({ q: '', page: 1 })

    await wrapper.find('[data-testid="hooks-search"]').setValue('guard')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()

    expect(forebrainApi.hooksPage).toHaveBeenCalledTimes(2)
    expect(vi.mocked(forebrainApi.hooksPage).mock.calls[1][0]).toMatchObject({ q: 'guard', page: 1 })
    vi.useRealTimers()
  })

  it('edits one command by reading the whole map and writing it back', async () => {
    const wrapper = await mountTab()
    await wrapper.find('[data-testid="hook-edit"]').trigger('click')
    await flushPromises()

    // The row's own kind comes from the whole map, not from the page.
    expect((wrapper.find('[data-testid="hook-type"]').element as HTMLSelectElement).value).toBe('command')
    expect((wrapper.find('[data-testid="hook-command"]').element as HTMLInputElement).value).toBe('scripts/guard.sh')

    await wrapper.find('[data-testid="hook-command"]').setValue('scripts/guard-v2.sh')
    await wrapper.find('[data-testid="hook-save"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.hooks).toHaveBeenCalled()
    expect(forebrainApi.saveHooks).toHaveBeenCalledWith({
      PreToolUse: [{ matcher: 'shell', hooks: [{ type: 'command', command: 'scripts/guard-v2.sh' }] }],
      PostToolUse: [{ hooks: [{ type: 'command', command: 'scripts/log.sh' }] }],
    })
    expect(wrapper.text()).toContain('钩子已保存并生效')
  })

  it('deletes one command from the whole map', async () => {
    const wrapper = await mountTab()
    await wrapper.findAll('[data-testid="hook-remove"]')[1].trigger('click')
    await flushPromises()

    const dialog = wrapper.find('.forebrain-modal')
    expect(dialog.text()).toContain('删除这条钩子？')
    await wrapper.find('[data-testid="confirm-ok"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.saveHooks).toHaveBeenCalledWith({
      PreToolUse: [{ matcher: 'shell', hooks: [{ type: 'command', command: 'scripts/guard.sh' }] }],
    })
  })

  it('adds a command to the map the server keeps', async () => {
    const wrapper = await mountTab()
    await wrapper.find('[data-testid="hooks-add"]').trigger('click')
    expect((wrapper.find('[data-testid="hook-event"]').element as HTMLSelectElement).value).toBe('PreToolUse')

    // The same matcher keeps its commands in order; the new one lands behind them.
    await wrapper.find('[data-testid="hook-matcher"]').setValue('shell')
    await wrapper.find('[data-testid="hook-command"]').setValue('scripts/new.sh')
    await wrapper.find('[data-testid="hook-save"]').trigger('click')
    await flushPromises()

    expect(vi.mocked(forebrainApi.saveHooks).mock.calls[0][0].PreToolUse[0].hooks).toEqual([
      { type: 'command', command: 'scripts/guard.sh' },
      { type: 'command', command: 'scripts/new.sh' },
    ])
  })

  it('says the list is empty rather than drawing a table', async () => {
    vi.mocked(forebrainApi.hooksPage).mockResolvedValue(pageOf([]))
    const wrapper = await mountTab()
    expect(wrapper.find('[data-testid="hooks-empty"]').exists()).toBe(true)
    expect(wrapper.find('.forebrain-table').exists()).toBe(false)
  })
})
