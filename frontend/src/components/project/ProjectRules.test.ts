import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import tip from '@/directives/tip'
import { forebrainApi, type ProjectRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import { openSelect } from '@/test-utils/formControls'
import ProjectRules from './ProjectRules.vue'

/**
 * The project space's rules tab (plan 075): the FOREBRAIN.md chain as a
 * server-paged table, the project instructions gone to 项目设置, and the tab's
 * own note under the pager.
 */
function project(trusted = true): ProjectRecord {
  return {
    id: 'p1',
    name: 'proj',
    icon: '',
    description: '',
    instructions: 'Always answer in Chinese.',
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

async function mountTab(trusted = true) {
  const wrapper = mount(ProjectRules, {
    props: { project: project(trusted), projectId: 'p1' },
    global: { directives: { tip } },
  })
  await flushPromises()
  return wrapper
}

describe('ProjectRules', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'projectRuleFilesPage').mockResolvedValue({
      rows: [
        { dir: '', exists: true, updatedAt: 1_700_000_000 },
        { dir: 'docs', exists: false },
      ],
      create: ['docs', 'pkg'],
      total: 2,
      page: 1,
      pageSize: 10,
    })
    vi.spyOn(forebrainApi, 'projectRuleFile').mockResolvedValue({ exists: false, content: '' })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('draws the tab toolbar, the table, the pager and the note', async () => {
    const wrapper = await mountTab()

    const search = wrapper.find('.forebrain-list-toolbar input[type="search"]')
    expect(search.exists()).toBe(true)
    expect(search.classes()).toContain('h-[34px]')
    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.findAll('[data-testid="rule-file-row"]')).toHaveLength(2)
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 2 条')
    expect(wrapper.find('p.forebrain-list-note').text()).toBe('项目目录里的规则文件，保存后新会话生效。')
  })

  it('has no project instructions any more: they live in the project settings', async () => {
    const wrapper = await mountTab()

    expect(wrapper.find('[data-testid="project-instructions"]').exists()).toBe(false)
    expect(wrapper.find('textarea').exists()).toBe(false)
  })

  it('maps each directory to the purpose the chain gives it', async () => {
    const wrapper = await mountTab()

    const root = wrapper.find('[data-rule-file="FOREBRAIN.md"]')
    expect(root.text()).toContain('项目根目录')

    const docs = wrapper.find('[data-rule-file="docs/FOREBRAIN.md"]')
    expect(docs.text()).toContain('子目录规则')
    // A layer without a file yet says so and is still editable: saving creates it.
    expect(docs.text()).toContain('未创建')
    expect(wrapper.find('[data-testid="rule-edit-docs/FOREBRAIN.md"]').exists()).toBe(true)
  })

  it('offers the edit as an icon action, not a text button in the row', async () => {
    const wrapper = await mountTab()

    const edit = wrapper.find('[data-testid="rule-edit-FOREBRAIN.md"]')
    expect(edit.element.tagName).toBe('BUTTON')
    expect(edit.text()).toBe('')
    expect(edit.attributes('aria-label')).toBe('编辑 FOREBRAIN.md')
    expect(wrapper.find('tbody .forebrain-btn').exists()).toBe(false)
  })

  it('offers the layers without a file in the create modal', async () => {
    // The layer dropdown is the library's select: it measures its viewport
    // with ResizeObserver, which jsdom does not have, and scrolling an item
    // into view is not implemented either.
    ;(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    Element.prototype.scrollIntoView = () => undefined

    const wrapper = await mountTab()
    await wrapper.find('[data-testid="rules-create"]').trigger('click')
    await flushPromises()

    // The modal teleports to body.
    const select = document.body.querySelector('[data-testid="rules-create-select"]') as HTMLElement | null
    expect(select).not.toBeNull()
    await openSelect(select as HTMLElement)
    // Scoped to the list this trigger opened: the modal draws a second select
    // (the one kind of file it creates), and the page a page-size one.
    const list = document.querySelector('[role="listbox"][data-state="open"]')
    expect([...(list?.querySelectorAll('[role="option"]') ?? [])].map((option) => (option.textContent ?? '').trim())).toEqual(['docs', 'pkg'])
  })

  it('keeps the trust gate in front of the table', async () => {
    const wrapper = await mountTab(false)

    expect(wrapper.find('[data-testid="rules-trust"]').exists()).toBe(true)
    expect(wrapper.find('.forebrain-table-wrap').exists()).toBe(false)
    expect(wrapper.find('[data-testid="rules-create"]').exists()).toBe(false)
  })
})
