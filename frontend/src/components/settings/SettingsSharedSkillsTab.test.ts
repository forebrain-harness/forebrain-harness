import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

import tip from '@/directives/tip'
import { forebrainApi, type SkillRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import SettingsSharedSkillsTab from './SettingsSharedSkillsTab.vue'

/**
 * The shared skills library (plan 073): the table asks the server for its own
 * two layers, so 全部来源 is `shared,builtin` and the filter never hides a row
 * in the browser.
 */
function skill(overrides: Partial<SkillRecord> = {}): SkillRecord {
  return {
    name: 'golang-testing',
    description: '共享',
    origin: 'shared',
    enabled: true,
    rootPath: '/h/golang-testing',
    version: 2,
    state: 'published',
    downloadUrl: '/api/skills/golang-testing/download',
    ...overrides,
  }
}

async function mountTab() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/:pathMatch(.*)*', component: { template: '<div />' } },
    ],
  })
  const wrapper = mount(SettingsSharedSkillsTab, { global: { plugins: [router], directives: { tip } } })
  await flushPromises()
  return wrapper
}

describe('SettingsSharedSkillsTab', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'skillsPage').mockResolvedValue({
      rows: [skill(), skill({ name: 'skill-workshop', origin: 'builtin', rootPath: '/b/skill-workshop' })],
      actions: [],
      total: 2,
      page: 1,
      pageSize: 10,
    })
    vi.spyOn(forebrainApi, 'skillsOverview').mockResolvedValue({ installed: [], actions: [] })
    vi.spyOn(forebrainApi, 'skillsToggle').mockResolvedValue({ status: 'ok', skills: [] })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('draws the list toolbar, the table and the pager', async () => {
    const wrapper = await mountTab()

    expect(wrapper.find('.forebrain-list-toolbar input[type="search"]').exists()).toBe(true)
    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.findAll('[data-skill-row]')).toHaveLength(2)
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 2 条')
  })

  it('says under the pager who the library binds, and that built-ins are read-only', async () => {
    const wrapper = await mountTab()
    expect(wrapper.find('[data-testid="shared-skills-note"]').text()).toBe('对所有主代理生效。内置技能只读。')
  })

  it('offers only the two layers this library holds', async () => {
    const wrapper = await mountTab()

    const options = wrapper.findAll('[data-testid="shared-skills-source-filter"] option')
    expect(options.map((option) => option.text())).toEqual(['全部来源', '共享', '内置'])
    expect(options.map((option) => (option.element as HTMLOptionElement).value)).toEqual(['shared,builtin', 'shared', 'builtin'])
  })

  it('asks for both layers at once, and for one of them when picked', async () => {
    const wrapper = await mountTab()
    const page = vi.mocked(forebrainApi.skillsPage)

    expect(page.mock.calls[0][0]).toMatchObject({ origin: 'shared,builtin', page: 1 })

    await wrapper.find('[data-testid="shared-skills-source-filter"]').setValue('builtin')
    await flushPromises()

    expect(page).toHaveBeenCalledTimes(2)
    expect(page.mock.calls[1][0]).toMatchObject({ origin: 'builtin', page: 1 })
  })
})
