import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'

import tip from '@/directives/tip'
import { forebrainApi, type ListQuery, type MemoryFileRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import MemoriesView from './MemoriesView.vue'

/**
 * The memories page (plan 073): no title row — the top bar names the page — a
 * toolbar whose left is the search box and whose right holds the two bulk
 * actions, the shared memory table, a pager under it and the page's note below
 * that. The order is a column header's, never a dropdown's.
 */
const mounted: VueWrapper[] = []

const at = 1760000000

function file(overrides: Partial<MemoryFileRecord> = {}): MemoryFileRecord {
  return {
    path: 'notes/habit.md',
    sizeBytes: 24,
    createdAt: at,
    updatedAt: at + 3600,
    core: false,
    ...overrides,
  }
}

function rows(): MemoryFileRecord[] {
  return [file({ path: 'MEMORY.md', core: true }), file()]
}

async function mountPage(): Promise<VueWrapper> {
  const wrapper = mount(MemoriesView, { global: { directives: { tip }, stubs: { teleport: true } } })
  mounted.push(wrapper)
  await flushPromises()
  return wrapper
}

describe('MemoriesView', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'memoryFilesPage').mockImplementation(async (query: ListQuery) => ({
      rows: rows(),
      total: 23,
      page: query.page,
      pageSize: query.pageSize,
    }))
    vi.spyOn(forebrainApi, 'memoryFileRead').mockResolvedValue({ path: 'notes/habit.md', content: 'prefers short answers\n', core: false })
  })

  afterEach(() => {
    while (mounted.length) mounted.pop()?.unmount()
    vi.restoreAllMocks()
  })

  it('opens on a search toolbar, the table, the pager and the note — no title row', async () => {
    const wrapper = await mountPage()

    expect(wrapper.find('h2').exists()).toBe(false)

    const toolbar = wrapper.find('.forebrain-list-toolbar')
    expect(toolbar.find('input[type="search"]').attributes('placeholder')).toBe('搜索记忆内容关键词')
    expect(toolbar.find('[data-testid="memories-reset-all"]').text()).toBe('重置全部')
    // Nothing narrows the list but the search box: the old sort dropdown is gone.
    expect(toolbar.findAll('select')).toHaveLength(0)
    expect(wrapper.find('[data-testid="memories-sort"]').exists()).toBe(false)

    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.findAll('[data-memory-file]')).toHaveLength(2)
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 23 条')
    expect(wrapper.find('p.forebrain-list-note').text()).toBe(
      '本主代理的通用记忆文件（不属于任何项目），可以查看、编辑、删除过时或错误的记忆。核心文件（MEMORY.md、memory_summary.md）可以编辑、清空内容，但不能删除；每次保存或删除都会在记忆目录里留下一个版本。记忆功能的开关在“设置 → 记忆”，项目记忆在各项目空间。',
    )
    expect(vi.mocked(forebrainApi.memoryFilesPage).mock.calls[0][0]).toMatchObject({
      page: 1,
      pageSize: 10,
      sort: 'updated',
      order: 'desc',
      scope: 'global',
    })
  })

  it('offers the row actions as icon buttons only, never a text button', async () => {
    const wrapper = await mountPage()

    expect(wrapper.findAll('.forebrain-table button.forebrain-btn')).toHaveLength(0)
    const note = wrapper.find('[data-memory-file="notes/habit.md"]')
    expect(note.findAll('.forebrain-row-action').map((button) => button.attributes('aria-label'))).toEqual([
      '编辑 notes/habit.md',
      '删除 notes/habit.md',
    ])
  })

  it('keeps 删除所选 off until something is selected, then deletes what is', async () => {
    vi.spyOn(forebrainApi, 'memoryFilesDelete').mockResolvedValue({ ok: true, deleted: 1, results: [{ path: 'notes/habit.md', ok: true }] })

    const wrapper = await mountPage()
    const button = wrapper.find('[data-testid="memories-delete-selected"]')
    expect(button.attributes('disabled')).toBeDefined()
    expect(wrapper.find('.forebrain-list-toolbar').text()).not.toContain('已选')

    await wrapper.find('[data-memory-file="notes/habit.md"] input[type="checkbox"]').setValue(true)
    expect(wrapper.find('.forebrain-list-toolbar').text()).toContain('已选 1 个')
    expect(wrapper.find('[data-testid="memories-delete-selected"]').attributes('disabled')).toBeUndefined()

    await wrapper.find('[data-testid="memories-delete-selected"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.memoryFilesDelete).toHaveBeenCalledWith('global', ['notes/habit.md'])
    expect(wrapper.text()).toContain('已删除 1 项')
  })

  it('sorts on the header and goes back to the first page for the new order', async () => {
    const wrapper = await mountPage()
    const page = vi.mocked(forebrainApi.memoryFilesPage)

    await wrapper.find('[data-testid="pager-page-2"]').trigger('click')
    await flushPromises()
    expect(page.mock.calls[1][0]).toMatchObject({ page: 2 })

    // 创建时间's own first direction is newest first.
    await wrapper.findAll('th.forebrain-sortable button')[0].trigger('click')
    await flushPromises()

    expect(page.mock.calls[2][0]).toMatchObject({ sort: 'created', order: 'desc', page: 1, pageSize: 10 })
    expect(wrapper.find('th.forebrain-sortable.is-on').text()).toBe('创建时间')
  })

  it('resets the whole scope only after the confirm', async () => {
    vi.spyOn(forebrainApi, 'resetMemories').mockResolvedValue({ ok: true, scope: 'global' })

    const wrapper = await mountPage()
    await wrapper.find('[data-testid="memories-reset-all"]').trigger('click')
    await flushPromises()

    const confirm = wrapper.find('[data-testid="confirm-ok"]')
    expect(confirm.exists()).toBe(true)
    expect(confirm.text()).toBe('确认重置')
    expect(wrapper.find('.forebrain-confirm-message').text()).toContain('将删除本主代理全部记忆文件')

    await confirm.trigger('click')
    await flushPromises()

    expect(forebrainApi.resetMemories).toHaveBeenCalledWith({ scope: 'global' })
    expect(wrapper.text()).toContain('记忆已重置。')
  })
})
