import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'

import tip from '@/directives/tip'
import { forebrainApi, type ToolMetaRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import ToolsView from './ToolsView.vue'

/**
 * The runtime's tool table (plan 073): the search, the category and the type
 * are the server's, the row's one icon action opens the tool's inputs under the
 * row, and no title row sits above the toolbar.
 */
const mounted: VueWrapper[] = []

function tool(overrides: Partial<ToolMetaRecord> = {}): ToolMetaRecord {
  return {
    name: 'read_file',
    description: '读取工作区内文件，返回带行号的内容',
    category: 'filesystem',
    readOnly: true,
    destructive: false,
    inputSchema: {
      type: 'object',
      properties: {
        file_path: { type: 'string', description: '文件路径，绝对路径或相对工作区' },
        limit: { type: 'integer', description: '最多读多少行' },
      },
      required: ['file_path'],
    },
    ...overrides,
  }
}

async function mountPage(): Promise<VueWrapper> {
  const wrapper = mount(ToolsView, { global: { directives: { tip } } })
  mounted.push(wrapper)
  await flushPromises()
  return wrapper
}

describe('ToolsView', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'toolsPage').mockResolvedValue({
      rows: [
        tool(),
        tool({ name: 'write_file', description: '写入或新建文件', readOnly: false, destructive: true, inputSchema: undefined }),
      ],
      categories: ['filesystem', 'shell', 'mcp__github'],
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
    const wrapper = await mountPage()
    expect(wrapper.find('h2').exists()).toBe(false)

    const toolbar = wrapper.find('.forebrain-list-toolbar')
    expect(toolbar.find('input[type="search"]').exists()).toBe(true)
    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.findAll('[data-tool-row]')).toHaveLength(2)
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 2 条')
    expect(wrapper.find('p.forebrain-list-note').text()).toBe('本主代理运行时实际暴露给模型的工具。')
  })

  it('offers the server’s categories in Chinese and leaves an unknown one as its key', async () => {
    const wrapper = await mountPage()

    const options = wrapper.findAll('[data-testid="tools-category-filter"] option')
    expect(options.map((option) => option.text())).toEqual(['全部分类', '文件', '命令', 'mcp__github'])
    expect(options.map((option) => (option.element as HTMLOptionElement).value)).toEqual(['', 'filesystem', 'shell', 'mcp__github'])

    expect(wrapper.findAll('[data-testid="tools-type-filter"] option').map((option) => option.text())).toEqual([
      '全部类型',
      '只读',
      '有副作用',
    ])
  })

  it('draws the tool, its description, its category and its type', async () => {
    const wrapper = await mountPage()

    const read = wrapper.find('[data-tool-row="read_file"]')
    expect(read.find('td .forebrain-cell-1').text()).toBe('read_file')
    expect(read.text()).toContain('读取工作区内文件，返回带行号的内容')
    expect(read.text()).toContain('文件')
    expect(read.find('.forebrain-badge').text()).toBe('只读')

    const write = wrapper.find('[data-tool-row="write_file"]')
    expect(write.findAll('.forebrain-badge').map((badge) => badge.text())).toEqual(['有副作用', '破坏性'])
  })

  it('sends the category and the type to the server', async () => {
    const wrapper = await mountPage()
    const page = vi.mocked(forebrainApi.toolsPage)

    await wrapper.find('[data-testid="tools-type-filter"]').setValue('write')
    await flushPromises()
    expect(page.mock.calls[1][0]).toMatchObject({ type: 'write', page: 1 })

    await wrapper.find('[data-testid="tools-category-filter"]').setValue('shell')
    await flushPromises()
    expect(page.mock.calls[2][0]).toMatchObject({ type: 'write', category: 'shell', page: 1 })
  })

  it('opens one tool’s inputs under its row, and closes them again', async () => {
    const wrapper = await mountPage()

    const action = wrapper.find('[data-testid="tool-inputs-read_file"]')
    expect(action.attributes('aria-label')).toBe('查看入参 read_file')
    expect(action.attributes('aria-expanded')).toBe('false')
    expect(wrapper.find('[data-testid="tool-params-read_file"]').exists()).toBe(false)

    await action.trigger('click')

    const params = wrapper.find('[data-testid="tool-params-read_file"]')
    expect(params.exists()).toBe(true)
    expect(params.findAll('th').map((th) => th.text())).toEqual(['参数', '类型', '必填', '说明'])
    const rows = params.findAll('[data-tool-param]')
    expect(rows.map((row) => row.findAll('td').map((cell) => cell.text()))).toEqual([
      ['file_path', 'string', '是', '文件路径，绝对路径或相对工作区'],
      ['limit', 'integer', '否', '最多读多少行'],
    ])
    expect(wrapper.find('[data-testid="tool-inputs-read_file"]').attributes('aria-expanded')).toBe('true')
    expect(wrapper.find('[data-testid="tool-inputs-read_file"]').attributes('aria-label')).toBe('收起 read_file')

    await wrapper.find('[data-testid="tool-inputs-read_file"]').trigger('click')
    expect(wrapper.find('[data-testid="tool-params-read_file"]').exists()).toBe(false)
  })

  it('says 无入参 for a tool whose schema declares none', async () => {
    const wrapper = await mountPage()

    await wrapper.find('[data-testid="tool-inputs-write_file"]').trigger('click')
    const params = wrapper.find('[data-testid="tool-params-write_file"]')
    expect(params.text()).toBe('无入参')
    expect(params.find('table').exists()).toBe(false)
  })

  it('keeps a second row from opening while one is open', async () => {
    const wrapper = await mountPage()

    await wrapper.find('[data-testid="tool-inputs-read_file"]').trigger('click')
    await wrapper.find('[data-testid="tool-inputs-write_file"]').trigger('click')

    expect(wrapper.find('[data-testid="tool-params-read_file"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="tool-params-write_file"]').exists()).toBe(true)
  })
})
