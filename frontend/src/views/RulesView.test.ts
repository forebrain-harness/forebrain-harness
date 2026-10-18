import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'

import tip from '@/directives/tip'
import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'
import RulesView from './RulesView.vue'

/**
 * The primary agent's rule files (plan 073): a search toolbar, a fixed table,
 * a pager and the page's note — and no title row, because the top bar already
 * names the page.
 */
const at = 1760000000
const mounted: VueWrapper[] = []

async function mountPage(): Promise<VueWrapper> {
  const wrapper = mount(RulesView, { global: { directives: { tip } } })
  mounted.push(wrapper)
  await flushPromises()
  return wrapper
}

describe('RulesView', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'agentRuleFilesPage').mockResolvedValue({
      rows: [
        { name: 'AGENTS.md', exists: true, updatedAt: at },
        { name: 'USER.md', exists: false },
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
    const wrapper = await mountPage()
    expect(wrapper.find('h2').exists()).toBe(false)

    const toolbar = wrapper.find('.forebrain-list-toolbar')
    expect(toolbar.find('input[type="search"]').exists()).toBe(true)
    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.findAll('[data-testid="rule-file-row"]').length).toBe(2)
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 2 条')
    expect(wrapper.find('p.forebrain-list-note').text()).toBe(
      '本主代理的规则文件，在主代理工作区根目录，每个会话开头注入；保存后新会话生效。',
    )
  })

  it('draws a file with its purpose and a grey badge when it does not exist yet', async () => {
    const wrapper = await mountPage()

    const agents = wrapper.find('[data-rule-file="AGENTS.md"]')
    expect(agents.find('.font-mono').text()).toBe('AGENTS.md')
    expect(agents.text()).toContain('行为准则')
    expect(agents.text()).toMatch(/\d{4}-\d{2}-\d{2} \d{2}:\d{2}/)
    expect(agents.find('.forebrain-badge').exists()).toBe(false)
    expect(agents.find('.forebrain-row-action').attributes('aria-label')).toBe('编辑 AGENTS.md')

    const user = wrapper.find('[data-rule-file="USER.md"]')
    expect(user.find('.forebrain-badge').text()).toBe('未创建')
  })

  it('offers only the files that do not exist yet, read from the whole list', async () => {
    // The page holds one page of rows; the modal's list is the whole one, so a
    // search on screen never narrows what can be created.
    vi.spyOn(forebrainApi, 'agentRuleFiles').mockResolvedValue({
      files: [
        { name: 'AGENTS.md', exists: true },
        { name: 'SOUL.md', exists: true },
        { name: 'USER.md', exists: false },
      ],
    })
    const wrapper = await mountPage()

    await wrapper.find('[data-testid="rules-create"]').trigger('click')
    await flushPromises()

    // The modal renders into a Teleport at the document's body.
    const select = document.body.querySelector('[data-testid="rules-create-select"]') as HTMLSelectElement | null
    expect(select).not.toBeNull()
    expect(Array.from(select!.options).map((option) => option.textContent?.trim())).toEqual(['USER.md'])
    expect(select!.value).toBe('USER.md')
  })
})