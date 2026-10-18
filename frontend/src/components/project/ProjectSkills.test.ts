import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

import tip from '@/directives/tip'
import { forebrainApi, type ProjectRecord, type SkillRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import ProjectSkills from './ProjectSkills.vue'

/**
 * The project space's skills tab (plan 073): it carries the list's toolbar, the
 * shared skill table and the pager; the layer filter offers the four layers a
 * project can hold, and every choice is the server's.
 */
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

function skill(overrides: Partial<SkillRecord> = {}): SkillRecord {
  return {
    name: 'proj-skill',
    description: '项目自己的',
    origin: 'project',
    enabled: true,
    rootPath: '/p/skills/proj-skill',
    version: 1,
    state: 'published',
    downloadUrl: '/api/v1/projects/p1/skills/proj-skill/download',
    ...overrides,
  }
}

async function mountTab(trusted = true) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/:pathMatch(.*)*', component: { template: '<div />' } },
    ],
  })
  const wrapper = mount(ProjectSkills, {
    props: { project: project(trusted), projectId: 'p1' },
    global: { plugins: [router], directives: { tip } },
  })
  await flushPromises()
  return wrapper
}

describe('ProjectSkills', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'projectSkillsPage').mockResolvedValue({
      rows: [skill(), skill({ name: 'gateway-ops', origin: 'agent', rootPath: '/s/gateway-ops' })],
      actions: [],
      total: 2,
      page: 1,
      pageSize: 10,
    })
    vi.spyOn(forebrainApi, 'projectSkillsOverview').mockResolvedValue({ installed: [], actions: [] })
    vi.spyOn(forebrainApi, 'projectSkillsToggle').mockResolvedValue({ status: 'ok', skills: [] })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('draws the list toolbar, the table and the pager under a trusted project', async () => {
    const wrapper = await mountTab()

    expect(wrapper.find('.forebrain-list-toolbar input[type="search"]').exists()).toBe(true)
    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.findAll('[data-skill-row]')).toHaveLength(2)
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 2 条')
    expect(wrapper.find('p.forebrain-list-note').text()).toBe(
      '本项目实际能加载的全部技能。同名时按 项目 → 主代理 → 共享 → 内置 覆盖；只有项目技能可在这里修改。',
    )
  })

  it('offers the four layers a project can hold', async () => {
    const wrapper = await mountTab()

    const options = wrapper.findAll('[data-testid="project-skills-source-filter"] option')
    expect(options.map((option) => option.text())).toEqual(['全部来源', '项目', '主代理', '共享', '内置'])
  })

  it('sends the layer filter to the server', async () => {
    const wrapper = await mountTab()
    const page = vi.mocked(forebrainApi.projectSkillsPage)

    await wrapper.find('[data-testid="project-skills-source-filter"]').setValue('project')
    await flushPromises()

    expect(page).toHaveBeenCalledTimes(2)
    // The project id is the method's own argument; the query carries the layer.
    expect(page.mock.calls[1][1]).toMatchObject({ origin: 'project', page: 1 })
  })

  it('keeps the trust gate in front of the table', async () => {
    const wrapper = await mountTab(false)

    expect(wrapper.find('[data-testid="project-trust"]').exists()).toBe(true)
    expect(wrapper.find('.forebrain-table-wrap').exists()).toBe(false)
    expect(wrapper.find('[data-testid="table-pager"]').exists()).toBe(false)
  })
})
