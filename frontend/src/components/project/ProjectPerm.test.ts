import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'

import tip from '@/directives/tip'
import { forebrainApi, type PermissionRuleRecord, type ProjectRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import ProjectPerm from './ProjectPerm.vue'

/**
 * The project space's permissions tab (plan 075): the project's own rules as a
 * server-paged table under the tab's toolbar (the match test and the add button
 * moved there), the pager, and the note that names the file in monospace.
 */
const mounted: VueWrapper[] = []

function project(trusted = true): ProjectRecord {
  return {
    id: 'p1',
    name: 'proj',
    icon: '',
    description: '',
    instructions: '',
    root: '/tmp/proj',
    projectKey: 'proj',
    memoryScope: '',
    resourceAccess: false,
    pinned: false,
    createdAt: 0,
    updatedAt: 0,
    trusted,
  }
}

function rule(overrides: Partial<PermissionRuleRecord> = {}): PermissionRuleRecord {
  return {
    source: 'projectSettings',
    behavior: 'deny',
    toolName: 'shell',
    ruleContent: 'rm -rf *',
    rule: { toolName: 'shell', ruleContent: 'rm -rf *' },
    ...overrides,
  }
}

async function mountTab(trusted = true, applies = true): Promise<VueWrapper> {
  vi.mocked(forebrainApi.projectPermissionRulesPage).mockResolvedValue({
    rows: [rule({ behavior: 'deny', toolName: 'shell', ruleContent: 'rm -rf *' }), rule({ behavior: 'ask', toolName: 'read_file', ruleContent: '/etc/**' })],
    applies,
    total: 2,
    page: 1,
    pageSize: 10,
  })
  const wrapper = mount(ProjectPerm, {
    props: { project: project(trusted), projectId: 'p1' },
    global: { directives: { tip }, stubs: { teleport: true } },
  })
  mounted.push(wrapper)
  await flushPromises()
  return wrapper
}

describe('ProjectPerm', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'projectPermissionRulesPage').mockResolvedValue({
      rows: [],
      applies: true,
      total: 0,
      page: 1,
      pageSize: 10,
    })
    vi.spyOn(forebrainApi, 'toolsList').mockResolvedValue([{ name: 'shell' } as never])
    vi.spyOn(forebrainApi, 'projectPermissionUpdate').mockResolvedValue({ ok: true, rules: [] })
    vi.spyOn(forebrainApi, 'projectPermissionExplain').mockResolvedValue({ decision: { behavior: 'ask' } })
  })

  afterEach(() => {
    while (mounted.length) mounted.pop()?.unmount()
    vi.restoreAllMocks()
  })

  it('draws the tab toolbar, the table, the pager and the note', async () => {
    const wrapper = await mountTab()

    const search = wrapper.find('.forebrain-list-toolbar input[type="search"]')
    expect(search.exists()).toBe(true)
    expect(search.classes()).toContain('h-[34px]')
    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.findAll('[data-testid="project-rule"]')).toHaveLength(2)
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 2 条')

    const note = wrapper.find('p.forebrain-list-note')
    expect(note.text()).toBe('项目已信任时生效；规则写在 .forebrain/safety.json。')
    expect(note.find('span.font-mono').text()).toBe('.forebrain/safety.json')
  })

  it('keeps the match test and the add button in the toolbar', async () => {
    const wrapper = await mountTab()

    const toolbar = wrapper.find('.forebrain-list-toolbar')
    expect(toolbar.find('[data-testid="perm-verify"]').text()).toBe('规则匹配测试')
    expect(toolbar.find('[data-testid="add-rule"]').text()).toBe('添加规则')
  })

  it('offers edit and delete as icon actions, not text buttons in the row', async () => {
    const wrapper = await mountTab()

    const row = wrapper.findAll('[data-testid="project-rule"]')[0]
    const edit = row.find('[data-testid="edit-rule"]')
    expect(edit.element.tagName).toBe('BUTTON')
    expect(edit.text()).toBe('')
    expect(edit.attributes('aria-label')).toBe('编辑规则 rm -rf *')
    expect(row.find('[data-testid="remove-rule"]').attributes('aria-label')).toBe('删除 rm -rf *')
    expect(row.find('td:last-child').findAll('.forebrain-row-action')).toHaveLength(2)
    expect(row.find('.forebrain-btn').exists()).toBe(false)
  })

  it('searches on the server', async () => {
    const wrapper = await mountTab()
    const page = vi.mocked(forebrainApi.projectPermissionRulesPage)

    await wrapper.find('.forebrain-list-toolbar input[type="search"]').setValue('/etc')
    await new Promise((resolve) => setTimeout(resolve, 350))
    await flushPromises()

    expect(page).toHaveBeenCalledTimes(2)
    expect(page.mock.calls[1][1]).toMatchObject({ q: '/etc', page: 1 })
  })

  it('says when the engine would not take rules from this project', async () => {
    const wrapper = await mountTab(true, false)

    expect(wrapper.find('[data-testid="perm-not-applied"]').exists()).toBe(true)
    expect(wrapper.find('.forebrain-table-wrap').exists()).toBe(false)
    expect(wrapper.find('[data-testid="add-rule"]').exists()).toBe(false)
  })

  it('keeps the trust gate in front of the table', async () => {
    const wrapper = await mountTab(false)

    expect(wrapper.find('[data-testid="perm-trust"]').exists()).toBe(true)
    expect(wrapper.find('.forebrain-list-toolbar').exists()).toBe(false)
    expect(wrapper.find('.forebrain-table-wrap').exists()).toBe(false)
  })
})
