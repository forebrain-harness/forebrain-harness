import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'

import tip from '@/directives/tip'
import { forebrainApi, type ListQuery, type MemoryFileRecord, type ProjectRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import ProjectMemory from './ProjectMemory.vue'

/**
 * The project space's memory tab (plan 075): the shared file table under the
 * tab's toolbar, the pager, and the tab's note — no title row, and no memory
 * scope control, which is a project setting.
 */
const mounted: VueWrapper[] = []

const at = 1760000000

function project(overrides: Partial<ProjectRecord> = {}): ProjectRecord {
  return {
    id: 'p1',
    name: 'proj',
    icon: '',
    description: '',
    instructions: '',
    root: '/tmp/proj',
    projectKey: 'proj-key',
    memoryScope: 'shared',
    resourceAccess: false,
    pinned: false,
    createdAt: 0,
    updatedAt: 0,
    trusted: true,
    ...overrides,
  }
}

function file(overrides: Partial<MemoryFileRecord> = {}): MemoryFileRecord {
  return { path: 'notes/habit.md', sizeBytes: 24, createdAt: at, updatedAt: at + 3600, core: false, ...overrides }
}

async function mountTab(overrides: Partial<ProjectRecord> = {}): Promise<VueWrapper> {
  const wrapper = mount(ProjectMemory, {
    props: { project: project(overrides), projectId: 'p1' },
    global: { directives: { tip }, stubs: { teleport: true } },
  })
  mounted.push(wrapper)
  await flushPromises()
  return wrapper
}

describe('ProjectMemory', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'memoryFilesPage').mockImplementation(async (query: ListQuery) => ({
      rows: [file({ path: 'MEMORY.md', core: true }), file()],
      total: 2,
      page: query.page,
      pageSize: query.pageSize,
    }))
    vi.spyOn(forebrainApi, 'memoryFileRead').mockResolvedValue({ path: 'notes/habit.md', content: 'prefers short answers\n', core: false })
  })

  afterEach(() => {
    while (mounted.length) mounted.pop()?.unmount()
    vi.restoreAllMocks()
  })

  it('draws the tab toolbar, the table, the pager and the note', async () => {
    const wrapper = await mountTab()

    expect(wrapper.find('h2').exists()).toBe(false)
    expect(wrapper.find('.forebrain-list-toolbar input[type="search"]').exists()).toBe(true)
    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.findAll('[data-memory-file]')).toHaveLength(2)
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 2 条')
    expect(wrapper.find('p.forebrain-list-note').text()).toBe(
      '可以查看、编辑、删除过时或错误的记忆。核心文件（MEMORY.md、memory_summary.md）只能编辑或清空，不能删除。',
    )
    // The listing is the project's own scope, whatever the project's setting is.
    expect(vi.mocked(forebrainApi.memoryFilesPage).mock.calls[0][0]).toMatchObject({ scope: 'project', projectId: 'p1' })
  })

  it('picks the memory scope nowhere: it is a project setting', async () => {
    const wrapper = await mountTab()

    expect(wrapper.findAll('input[type="radio"]')).toHaveLength(0)
    expect(wrapper.text()).not.toContain('记忆作用域')
    expect(wrapper.text()).not.toContain('仅本项目')
  })

  it('says so when the project has no memory scope of its own', async () => {
    const wrapper = await mountTab({ projectKey: '' })

    expect(wrapper.text()).toContain('该项目未启用独立记忆作用域')
    expect(wrapper.find('.forebrain-table-wrap').exists()).toBe(false)
  })
})
