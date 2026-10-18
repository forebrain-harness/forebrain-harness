import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'

import tip from '@/directives/tip'
import { forebrainApi, type MemoryFileRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import MemoryFilesPanel from './MemoryFilesPanel.vue'

/**
 * The memory file table (plan 073): one page of rows, its boxes, the two time
 * columns that sort through their headers, and the row's two icon actions. The
 * search box, the bulk buttons and the pager are the page's, never the
 * table's.
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
    preview: 'prefers short answers',
    ...overrides,
  }
}

async function mountPanel(overrides: Record<string, unknown> = {}): Promise<VueWrapper> {
  const wrapper = mount(MemoryFilesPanel, {
    props: {
      scope: 'global',
      rows: [file({ path: 'MEMORY.md', core: true }), file()],
      loading: false,
      sort: 'updated',
      order: 'desc',
      selected: new Set<string>(),
      ...overrides,
    },
    global: { directives: { tip } },
  })
  mounted.push(wrapper)
  await flushPromises()
  return wrapper
}

describe('MemoryFilesPanel', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'memoryFileRead').mockResolvedValue({ path: 'notes/habit.md', content: 'prefers short answers\n', core: false })
  })

  afterEach(() => {
    while (mounted.length) mounted.pop()?.unmount()
    vi.restoreAllMocks()
  })

  it('draws a fixed table of the columns the design gives it, and nothing above it', async () => {
    const wrapper = await mountPanel()

    expect(wrapper.find('table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.findAll('thead th').map((th) => th.text())).toEqual(['', '记忆文件', '内容', '创建时间', '更新时间', '操作'])
    expect(wrapper.findAll('colgroup col').map((col) => col.attributes('style'))).toEqual([
      'width: 40px;',
      'width: 300px;',
      undefined,
      'width: 150px;',
      'width: 150px;',
      'width: 88px;',
    ])
    expect(wrapper.findAll('[data-memory-file]')).toHaveLength(2)

    // No toolbar of its own: no search box, no sort dropdown, no text button.
    expect(wrapper.find('input[type="search"]').exists()).toBe(false)
    expect(wrapper.findAll('select')).toHaveLength(0)
    expect(wrapper.findAll('button.forebrain-btn')).toHaveLength(0)
  })

  it('marks the core file and keeps its delete hole empty', async () => {
    const wrapper = await mountPanel()

    const core = wrapper.find('[data-memory-file="MEMORY.md"]')
    expect(core.find('.forebrain-badge').text()).toBe('核心')
    expect(core.findAll('.forebrain-row-action').map((button) => button.attributes('aria-label'))).toEqual(['编辑 MEMORY.md'])
    expect(core.find('.forebrain-icon-slot').exists()).toBe(true)

    const note = wrapper.find('[data-memory-file="notes/habit.md"]')
    expect(note.find('.forebrain-badge').exists()).toBe(false)
    expect(note.findAll('.forebrain-row-action').map((button) => button.attributes('aria-label'))).toEqual([
      '编辑 notes/habit.md',
      '删除 notes/habit.md',
    ])
  })

  it('formats both time columns to the minute and shows the file’s opening line', async () => {
    const wrapper = await mountPanel()

    const note = wrapper.find('[data-memory-file="notes/habit.md"]')
    const cells = note.findAll('td')
    expect(cells[2].text()).toBe('prefers short answers')
    expect(cells[3].text()).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/)
    expect(cells[4].text()).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/)
    expect(cells[3].classes()).toContain('forebrain-num')
    // The line is the listing's own: the table asks for no body (plan 073
    // moved the preview to the server's read-only field).
    expect(vi.mocked(forebrainApi.memoryFileRead)).not.toHaveBeenCalled()
  })

  it('shows a dash for a file the server gave no preview for', async () => {
    const wrapper = await mountPanel({ rows: [file({ path: 'empty.md', preview: '' })] })

    expect(wrapper.find('[data-memory-file="empty.md"]').findAll('td')[2].text()).toBe('—')
  })

  it('sends the clicked time column and the order it starts with, newest first', async () => {
    const wrapper = await mountPanel()

    const sortable = wrapper.findAll('th.forebrain-sortable button')
    expect(sortable.map((button) => button.text())).toEqual(['创建时间', '更新时间'])

    await sortable[0].trigger('click')
    expect(wrapper.emitted('sort')).toEqual([['created', 'desc']])

    // 更新时间 is the column in force; clicking it turns the order around.
    await sortable[1].trigger('click')
    expect(wrapper.emitted('sort')).toEqual([
      ['created', 'desc'],
      ['updated', 'asc'],
    ])
  })

  it('reports the selection the page keeps, one box or all of them', async () => {
    const wrapper = await mountPanel()

    await wrapper.find('[data-memory-file="notes/habit.md"] input[type="checkbox"]').setValue(true)
    expect(wrapper.emitted('update:selected')?.[0]).toEqual([new Set(['notes/habit.md'])])

    await wrapper.find('[data-testid="memories-select-all"]').setValue(true)
    expect(wrapper.emitted('update:selected')?.[1]).toEqual([new Set(['MEMORY.md', 'notes/habit.md'])])
  })

  it('asks the page to reload after a delete, and says what it did', async () => {
    vi.spyOn(forebrainApi, 'memoryFilesDelete').mockResolvedValue({
      ok: true,
      deleted: 1,
      results: [{ path: 'notes/habit.md', ok: true }],
    })

    const wrapper = await mountPanel()
    await wrapper.find('[data-memory-file="notes/habit.md"] button[aria-label="删除 notes/habit.md"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.memoryFilesDelete).toHaveBeenCalledWith('global', ['notes/habit.md'], undefined)
    expect(wrapper.emitted('notice')).toEqual([['已删除 notes/habit.md']])
    expect(wrapper.emitted('reload')).toHaveLength(1)
  })
})
