import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { DOMWrapper, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
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

async function mountPage(options: { realTeleport?: boolean } = {}): Promise<VueWrapper> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/projects/:id', component: { template: '<div />' } },
    ],
  })
  const wrapper = mount(ProjectsView, {
    // A stubbed teleport renders nothing of its slot, so a test that reads a
    // dialog's own fields mounts with the real one (the content lands in body).
    global: {
      plugins: [router],
      directives: { tip },
      stubs: options.realTeleport ? {} : { teleport: true },
    },
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

/**
 * The type column and the new-project dialog's type cards (plan 153). The
 * suggestion is the gateway's alone — the web never repeats the `.git` check —
 * so the dialog shows it with its reason, marks it 建议, and gives way the
 * moment a person picks a card.
 */

/** The dialog's debounce is 300ms of real time; a fake clock would also fake
 *  the promise flushing the mount needs, so the wait is real. */
async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 320))
  await flushPromises()
}

/** The dialog's own content is portalled to the document body, so its fields
 *  are read there rather than off the page's wrapper. */
function field(selector: string): DOMWrapper<Element> {
  const element = document.body.querySelector(selector)
  if (!element) throw new Error(`the dialog has no ${selector}`)
  return new DOMWrapper(element)
}

function dialogText(): string {
  return document.body.querySelector('.forebrain-modal')?.textContent ?? ''
}

async function openDialog(wrapper: VueWrapper): Promise<void> {
  await wrapper.find('[data-testid="projects-new"]').trigger('click')
  await flushPromises()
  // The dialog's name and root, so 创建 is about the type here and nothing else.
  await field('[data-testid="project-name"]').setValue('ibs')
}

describe('project types (plan 153)', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'projectsPage').mockResolvedValue({ rows: [project()], total: 1, page: 1, pageSize: 10 })
    vi.spyOn(forebrainApi, 'projectCreate').mockResolvedValue(project({ id: 'p-new', name: 'ibs' }))
    vi.spyOn(forebrainApi, 'getProjectDefaultType').mockResolvedValue({ projectType: 'office', source: 'non_git' })
  })

  afterEach(() => {
    vi.restoreAllMocks()
    // A dialog's content lives in the document body and outlives the wrapper.
    document.body.innerHTML = ''
  })

  it('draws the type as a badge in the table', async () => {
    vi.mocked(forebrainApi.projectsPage).mockResolvedValue({
      rows: [
        project({ id: 'p-office', name: 'office', projectType: 'office' }),
        project({ id: 'p-code', name: 'code', projectType: 'coding' }),
        project({ id: 'p-batch', name: 'batch', projectType: 'batch' }),
      ],
      total: 3,
      page: 1,
      pageSize: 10,
    })
    const wrapper = await mountPage({ realTeleport: true })

    expect(wrapper.find('[data-testid="project-type-badge-p-office"]').text()).toBe('办公创作')
    expect(wrapper.find('[data-testid="project-type-badge-p-code"]').text()).toBe('创意编程')
    expect(wrapper.find('[data-testid="project-type-badge-p-batch"]').text()).toBe('后台跑批')
  })

  it('offers the three kinds with one line each, and will not create a batch project untrusted', async () => {
    const wrapper = await mountPage({ realTeleport: true })
    const create = vi.mocked(forebrainApi.projectCreate)
    await openDialog(wrapper)
    await field('[data-testid="project-root"]').setValue('/tmp/ibs')

    expect(field('[data-testid="project-type-office"]').text()).toContain('文档、表格、幻灯片等办公交付')
    expect(field('[data-testid="project-type-coding"]').text()).toContain('代码与软件项目')
    expect(field('[data-testid="project-type-batch"]').text()).toContain('供业务系统批量执行任务')

    await field('[data-testid="project-type-batch"]').trigger('click')
    await flushPromises()

    // The trust is part of what a batch project is: no tick, no create.
    const createButton = field('[data-testid="project-create"]')
    expect(dialogText()).toContain('跑批任务无人值守')
    expect(createButton.attributes('disabled')).toBeDefined()

    await field('[role="switch"]').trigger('click')
    expect(createButton.attributes('disabled')).toBeUndefined()
    await createButton.trigger('click')
    await flushPromises()

    expect(create).toHaveBeenCalledWith({ name: 'ibs', root: '/tmp/ibs', projectType: 'batch', trust: true })
  })

  it('preselects the folder\'s suggestion and says why, and sends the type it shows', async () => {
    vi.mocked(forebrainApi.getProjectDefaultType).mockResolvedValue({ projectType: 'coding', source: 'git_worktree' })
    const wrapper = await mountPage({ realTeleport: true })
    const create = vi.mocked(forebrainApi.projectCreate)
    await openDialog(wrapper)

    await field('[data-testid="project-root"]').setValue('/Users/me/hundsun')
    await settle()

    expect(forebrainApi.getProjectDefaultType).toHaveBeenCalledWith('/Users/me/hundsun')
    // The suggestion is preselected, marked, and explained by the gateway's own
    // source rather than by a browser-side `.git` look.
    expect(field('[data-testid="project-type-coding"]').attributes('aria-checked')).toBe('true')
    expect(field('[data-testid="project-type-coding"]').text()).toContain('建议')
    expect(field('[data-testid="project-type-hint"]').text()).toContain('Git 工作树，默认创意编程')

    await field('[data-testid="project-create"]').trigger('click')
    await flushPromises()
    expect(create).toHaveBeenCalledWith({ name: 'ibs', root: '/Users/me/hundsun', projectType: 'coding' })
  })

  it('keeps a manual pick when a later folder suggests another type', async () => {
    vi.mocked(forebrainApi.getProjectDefaultType).mockResolvedValue({ projectType: 'coding', source: 'git_worktree' })
    const wrapper = await mountPage({ realTeleport: true })
    const create = vi.mocked(forebrainApi.projectCreate)
    await openDialog(wrapper)

    await field('[data-testid="project-root"]').setValue('/Users/me/hundsun')
    await settle()
    await field('[data-testid="project-type-office"]').trigger('click')
    await flushPromises()

    // Another Git folder: the folder's answer arrives but the picked card stands.
    await field('[data-testid="project-root"]').setValue('/Users/me/other')
    await settle()
    expect(field('[data-testid="project-type-office"]').attributes('aria-checked')).toBe('true')
    expect(field('[data-testid="project-type-hint"]').text()).toContain('已手动改为「办公创作」')
    expect(field('[data-testid="project-type-hint"]').text()).toContain('预选不再覆盖')

    await field('[data-testid="project-create"]').trigger('click')
    await flushPromises()
    expect(create).toHaveBeenCalledWith(expect.objectContaining({ projectType: 'office' }))
  })

  it('drops a suggestion that answers for a folder the dialog has already left', async () => {
    const pending: Array<{ root: string; resolve: (value: { projectType: 'office' | 'coding'; source: 'git_worktree' | 'non_git' }) => void }> = []
    vi.mocked(forebrainApi.getProjectDefaultType).mockImplementation(
      (root: string) => new Promise((resolve) => { pending.push({ root, resolve }) }),
    )
    const wrapper = await mountPage({ realTeleport: true })
    await openDialog(wrapper)

    await field('[data-testid="project-root"]').setValue('/tmp/git')
    await settle()
    await field('[data-testid="project-root"]').setValue('/tmp/plain')
    await settle()
    expect(pending.map((entry) => entry.root)).toEqual(['/tmp/git', '/tmp/plain'])

    // The newest folder answers first, the abandoned one last.
    pending[1].resolve({ projectType: 'office', source: 'non_git' })
    await flushPromises()
    expect(field('[data-testid="project-type-office"]').attributes('aria-checked')).toBe('true')

    pending[0].resolve({ projectType: 'coding', source: 'git_worktree' })
    await flushPromises()
    expect(field('[data-testid="project-type-office"]').attributes('aria-checked')).toBe('true')
    expect(field('[data-testid="project-type-hint"]').text()).toContain('非 Git 目录，默认办公创作')
  })

  it('keeps the current choice and says so when the folder cannot be judged', async () => {
    vi.mocked(forebrainApi.getProjectDefaultType).mockRejectedValue(new Error('no such directory'))
    const wrapper = await mountPage({ realTeleport: true })
    await openDialog(wrapper)

    await field('[data-testid="project-root"]').setValue('/tmp/gone')
    await settle()

    expect(field('[data-testid="project-type-hint"]').text()).toContain('默认类型识别失败，可手动选择')
    expect(field('[data-testid="project-type-coding"]').attributes('aria-checked')).toBe('true')
  })

  it('names the gateway\'s refusals in Chinese', async () => {
    vi.mocked(forebrainApi.projectCreate).mockRejectedValue({
      response: { status: 409, data: { error: 'batch_requires_trust' } },
    })
    const wrapper = await mountPage({ realTeleport: true })
    await openDialog(wrapper)
    await field('[data-testid="project-root"]').setValue('/tmp/ibs')

    await field('[data-testid="project-create"]').trigger('click')
    await flushPromises()

    expect(dialogText()).toContain('跑批项目必须信任此目录，请勾选信任后再创建。')
  })
})
