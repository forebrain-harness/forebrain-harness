import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import tip from '@/directives/tip'
import { forebrainApi, type ProjectSessionRow } from '@/lib/api'
import { setLocale } from '@/locales'
import ProjectSessions from './ProjectSessions.vue'

/**
 * The project space's sessions tab (plan 075): the conversation table with the
 * model, the message count and the heartbeat the server derives for it, the two
 * sortable time columns, and the tab's own note.
 */
async function mountTab() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/:pathMatch(.*)*', component: { template: '<div />' } },
    ],
  })
  const wrapper = mount(ProjectSessions, {
    props: { project: {}, projectId: 'p1' },
    global: { plugins: [router], directives: { tip } },
  })
  await flushPromises()
  return wrapper
}

function rows(): ProjectSessionRow[] {
  return [
    {
      id: 's1',
      title: 'First session',
      createdAt: 1_700_000_000,
      updatedAt: 1_700_000_600,
      model: 'gpt-5-codex',
      messageCount: 7,
      heartbeat: { intervalSeconds: 1800, paused: false },
    },
    {
      id: 's2',
      title: 'Second session',
      createdAt: 1_700_001_000,
      updatedAt: 1_700_001_600,
      model: '',
      messageCount: 0,
      heartbeat: { intervalSeconds: 600, paused: true },
    },
    {
      id: 's3',
      title: 'Third session',
      createdAt: 1_700_002_000,
      updatedAt: 1_700_002_600,
      model: 'claude-sonnet',
      messageCount: 2,
    },
  ]
}

describe('ProjectSessions', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'projectSessionsPage').mockResolvedValue({
      rows: rows(),
      total: 3,
      page: 1,
      pageSize: 10,
    })
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
    expect(wrapper.findAll('[data-session-row]')).toHaveLength(3)
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 3 条')
    expect(wrapper.find('p.forebrain-list-note').text()).toBe('项目会话都在项目根目录运行。')
  })

  it('names the columns the table carries', async () => {
    const wrapper = await mountTab()

    const headers = wrapper.findAll('thead th').map((th) => th.text())
    expect(headers.slice(0, 4)).toEqual(['标题', '模型', '消息', '心跳'])
    expect(headers[4]).toContain('创建时间')
    expect(headers[5]).toContain('更新时间')
    expect(headers[6]).toBe('操作')
  })

  it('draws the model, the message count and the heartbeat of each row', async () => {
    const wrapper = await mountTab()

    const first = wrapper.find('[data-session-row="s1"]')
    expect(first.text()).toContain('First session')
    expect(first.text()).toContain('gpt-5-codex')
    expect(first.text()).toContain('7')
    expect(first.text()).toContain('每 30 分钟')

    // A session that never pinned a model answers an empty one: the table says so.
    const second = wrapper.find('[data-session-row="s2"]')
    expect(second.text()).toContain('—')
    expect(second.text()).toContain('已暂停')

    // A conversation without a heartbeat has none of the badges.
    const third = wrapper.find('[data-session-row="s3"]')
    expect(third.findAll('.forebrain-badge')).toHaveLength(0)
  })

  it('orders by the time columns, newest first, update time by default', async () => {
    const wrapper = await mountTab()
    const page = vi.mocked(forebrainApi.projectSessionsPage)

    expect(page.mock.calls[0][1]).toMatchObject({ sort: 'updated', order: 'desc' })
    const updated = wrapper.findAll('thead th.is-on')
    expect(updated).toHaveLength(1)
    expect(updated[0].attributes('aria-sort')).toBe('descending')
    expect(updated[0].text()).toContain('更新时间')

    const created = wrapper.findAll('thead th.forebrain-sortable').find((th) => th.text().includes('创建时间'))
    await created!.find('button').trigger('click')
    await flushPromises()

    expect(page).toHaveBeenCalledTimes(2)
    expect(page.mock.calls[1][1]).toMatchObject({ sort: 'created', order: 'desc', page: 1 })
  })

  it('opens a session from its title link and from the row action, with no text button', async () => {
    const wrapper = await mountTab()

    const title = wrapper.find('[data-session-row="s2"] td a')
    expect(title.text()).toBe('Second session')
    expect(title.attributes('href')).toBe('/?session=s2')

    const open = wrapper.find('[data-testid="project-session-open-s1"]')
    expect(open.element.tagName).toBe('BUTTON')
    expect(open.text()).toBe('')
    expect(open.attributes('aria-label')).toBe('打开 First session')

    await open.trigger('click')
    await flushPromises()
    expect(wrapper.vm.$router.currentRoute.value.query.session).toBe('s1')
  })

  it('creates a project session from the toolbar and opens it', async () => {
    vi.spyOn(forebrainApi, 'projectSessionCreate').mockResolvedValue({ id: 'new-1', title: '' })
    const wrapper = await mountTab()

    await wrapper.find('[data-testid="project-session-create"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.projectSessionCreate).toHaveBeenCalledWith('p1', '')
    expect(wrapper.vm.$router.currentRoute.value.query.session).toBe('new-1')
  })
})
