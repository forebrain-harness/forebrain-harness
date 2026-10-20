import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

import tip from '@/directives/tip'
import { forebrainApi, type ProjectRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import ProjectsView from './ProjectsView.vue'

/**
 * The projects page (plan 073): no title row — the top bar names the page —
 * a toolbar whose left is the search box, a fixed table whose columns are the
 * project's facts, a pager under it and the page's note below that.
 */
const at = 1760000000

function project(overrides: Partial<ProjectRecord> = {}): ProjectRecord {
  return {
    id: 'p-alpha',
    name: 'alpha',
    icon: '',
    description: '主仓库',
    instructions: '',
    root: '/Users/me/alpha',
    projectKey: 'alpha',
    memoryScope: 'shared',
    resourceAccess: true,
    pinned: false,
    createdAt: at,
    updatedAt: at,
    trustLevel: 'trusted',
    missing: false,
    sessionCount: 4,
    ...overrides,
  }
}

async function mountPage(): Promise<VueWrapper> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/projects/:id', component: { template: '<div />' } },
    ],
  })
  const wrapper = mount(ProjectsView, {
    global: { plugins: [router], directives: { tip }, stubs: { teleport: true } },
  })
  await flushPromises()
  return wrapper
}

describe('ProjectsView', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'projectsPage').mockResolvedValue({
      rows: [
        project({ id: 'p-alpha', name: 'alpha', pinned: true, memoryScope: 'project_only' }),
        project({ id: 'p-beta', name: 'beta', trustLevel: 'untrusted', missing: true, sessionCount: 0 }),
        project({ id: 'p-gamma', name: 'gamma' }),
      ],
      total: 3,
      page: 1,
      pageSize: 10,
    })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('opens on a search toolbar, the table, the pager and the note — no title row', async () => {
    const wrapper = await mountPage()
    expect(wrapper.find('h2').exists()).toBe(false)

    const toolbar = wrapper.find('.forebrain-list-toolbar')
    expect(toolbar.exists()).toBe(true)
    expect(toolbar.find('input[type="search"]').exists()).toBe(true)
    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.findAll('[data-project-row]').length).toBe(3)

    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 3 条')
    expect(wrapper.find('p.forebrain-list-note').text()).toBe('每个项目有独立的项目空间，项目的配置和数据都在那里查看和修改。')
  })

  it('draws a row as its facts: link, badges, path, description, conversations, time', async () => {
    const wrapper = await mountPage()

    const alpha = wrapper.find('[data-project-row="alpha"]')
    expect(alpha.find('.forebrain-badge').text()).toBe('置顶')
    expect(alpha.text()).toContain('仅本项目记忆')
    expect(alpha.text()).toContain('/Users/me/alpha')
    expect(alpha.text()).toContain('主仓库')
    expect(alpha.find('td.forebrain-num').text()).toBe('4')
    expect(alpha.text()).toMatch(/\d{4}-\d{2}-\d{2} \d{2}:\d{2}/)

    const beta = wrapper.find('[data-project-row="beta"]')
    expect(beta.text()).toContain('未信任')
    expect(beta.text()).toContain('目录不存在')
  })

  it('offers the row actions as icon buttons that name the row', async () => {
    const wrapper = await mountPage()

    const alpha = wrapper.find('[data-project-row="alpha"]')
    const labels = alpha.findAll('.forebrain-row-action').map((button) => button.attributes('aria-label'))
    expect(labels).toEqual(['进入项目空间 alpha', '取消置顶 alpha', '删除 alpha'])

    const beta = wrapper.find('[data-project-row="beta"]')
    expect(beta.findAll('.forebrain-row-action').map((button) => button.attributes('aria-label'))).toContain('置顶 beta')
  })

  it('asks the server for the new order when a header is clicked', async () => {
    const wrapper = await mountPage()
    const page = vi.mocked(forebrainApi.projectsPage)
    page.mockClear()

    // The first sortable column is 项目, whose own first direction is A→Z.
    await wrapper.findAll('th.forebrain-sortable button')[0].trigger('click')
    await flushPromises()

    expect(page).toHaveBeenCalledTimes(1)
    expect(page.mock.calls[0][0]).toMatchObject({ sort: 'name', order: 'asc', page: 1, pageSize: 10 })
  })
})
