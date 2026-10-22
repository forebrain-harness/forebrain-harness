import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

import tip from '@/directives/tip'
import { forebrainApi, type ProjectRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import ProjectOverview from './ProjectOverview.vue'

/**
 * The project's settings tab (plan 075): the form lies flat in the tab — no
 * card, no form title — in the preview's order; the memory scope is a radio
 * group whose help text follows the choice; saving sends the instructions and,
 * only when it was touched, the trust in the directory; deleting asks for the
 * project's name in the shared dialog.
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
    trusted: true,
    ...overrides,
  }
}

async function mountTab(overrides: Partial<ProjectRecord> = {}): Promise<VueWrapper> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/:pathMatch(.*)*', component: { template: '<div />' } },
    ],
  })
  const wrapper = mount(ProjectOverview, {
    props: { project: project(overrides), projectId: 'p-alpha' },
    global: { plugins: [router], directives: { tip } },
  })
  await flushPromises()
  return wrapper
}

function labels(wrapper: VueWrapper): string[] {
  return wrapper.findAll('form .forebrain-form-field > span:first-child').map((span) => span.text())
}

describe('ProjectOverview', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'projectUpdate').mockResolvedValue(project())
    vi.spyOn(forebrainApi, 'projectDelete').mockResolvedValue({ ok: true })
    vi.spyOn(forebrainApi, 'projectDeletePreview').mockResolvedValue({ sessions: 2, cron_jobs: 1, running: false })
    vi.spyOn(forebrainApi, 'hostTrustSet').mockResolvedValue({ ok: true, level: 'untrusted' })
  })

  afterEach(() => {
    vi.restoreAllMocks()
    document.body.innerHTML = ''
  })

  it('lies flat: no card and no title, the fields in the preview\'s order', async () => {
    const wrapper = await mountTab()

    expect(wrapper.find('h2').exists()).toBe(false)
    expect(wrapper.find('h3').exists()).toBe(false)
    expect(wrapper.find('.forebrain-card').exists()).toBe(false)

    expect(labels(wrapper)).toEqual([
      '项目名',
      '绑定的工作目录',
      '说明',
      '图标',
      '项目键',
      '项目指令',
      '记忆作用域',
    ])

    // The root and the key are the project's identity: shown, never edited.
    // Read off the field itself — the library's Input keeps the value as its
    // own model, so there is no `value` attribute to read.
    const root = wrapper.find('[data-testid="project-settings-root"]')
    expect(root.attributes('readonly')).toBeDefined()
    expect((root.element as HTMLInputElement).value).toBe('/Users/me/alpha')
    const key = wrapper.find('[data-testid="project-settings-key"]')
    expect(key.attributes('readonly')).toBeDefined()
    expect((key.element as HTMLInputElement).value).toBe('alpha')

    // Two switch rows carry the trust and the reading-outside choice.
    const switches = wrapper.findAll('[role="switch"]')
    expect(switches.map((row) => row.attributes('aria-label'))).toEqual([
      '信任此目录',
      '允许读取项目目录之外的资料',
    ])
    expect(switches[0].attributes('aria-checked')).toBe('true')

    // 保存 first, 删除项目 on its right.
    const html = wrapper.html()
    expect(html.indexOf('project-settings-save')).toBeLessThan(html.indexOf('project-settings-delete'))
    expect(wrapper.find('[data-testid="project-settings-delete"]').text()).toBe('删除项目')
  })

  it('keeps 保存 disabled until something changes, then sends instructions alone', async () => {
    const wrapper = await mountTab()
    const save = wrapper.find('[data-testid="project-settings-save"]')
    const update = vi.mocked(forebrainApi.projectUpdate)

    expect(save.attributes('disabled')).toBeDefined()

    await wrapper.find('[data-testid="project-instructions"]').setValue('改代码前先写计划。')
    expect(save.attributes('disabled')).toBeUndefined()

    // The button is the form's submit: a click or Enter in a field saves.
    expect(save.attributes('type')).toBe('submit')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(update).toHaveBeenCalledTimes(1)
    const [id, payload] = update.mock.calls[0]
    expect(id).toBe('p-alpha')
    expect(payload).toEqual({
      name: 'alpha',
      icon: '',
      description: '主仓库',
      instructions: '改代码前先写计划。',
      memoryScope: 'shared',
      resourceAccess: true,
    })
    // Untouched trust is not sent: the switch is not what this save is about.
    expect(payload).not.toHaveProperty('trust')
    expect(forebrainApi.hostTrustSet).not.toHaveBeenCalled()

    expect(wrapper.emitted('saved')).toHaveLength(1)
    expect(wrapper.find('[data-testid="project-settings-saved"]').text()).toBe('项目设置已保存。')
  })

  it('sends the trust with the save when the switch was turned, and withdraws it for real', async () => {
    const wrapper = await mountTab()
    const update = vi.mocked(forebrainApi.projectUpdate)

    await wrapper.findAll('[role="switch"]')[0].trigger('click')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(update.mock.calls[0][1]).toMatchObject({ trust: false })
    // The project's PATCH records a trust but cannot withdraw one; the
    // directory's own record is what the gates read.
    expect(forebrainApi.hostTrustSet).toHaveBeenCalledWith('/Users/me/alpha', false)
    expect(wrapper.emitted('saved')).toHaveLength(1)
  })

  it('explains the memory scope that is chosen', async () => {
    const wrapper = await mountTab()

    const hint = () => wrapper.find('[role="radiogroup"] .forebrain-list-note').text()
    expect(wrapper.find('[role="radiogroup"]').exists()).toBe(true)
    expect(hint()).toBe('本项目的会话也能召回其他记忆；修改只在新会话生效。')

    await wrapper.find('[role="radio"][value="project_only"]').trigger('click')
    expect(hint()).toBe('只召回本项目自己的记忆；修改只在新会话生效。')

    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(vi.mocked(forebrainApi.projectUpdate).mock.calls[0][1]).toMatchObject({ memoryScope: 'project_only' })
  })

  it('asks for the project name in the shared dialog before deleting it', async () => {
    const wrapper = await mountTab()

    await wrapper.find('[data-testid="project-settings-delete"]').trigger('click')
    await flushPromises()

    // The dialog lives in a Teleport at the document's body.
    const modal = document.body.querySelector('.forebrain-modal')
    expect(modal?.textContent).toContain('删除项目「alpha」？')
    expect(modal?.textContent).toContain('将同时删除 2 个对话和 1 个定时任务，无法恢复。')
    expect((document.body.querySelector('[data-testid="confirm-ok"]') as HTMLButtonElement).disabled).toBe(true)
    expect(forebrainApi.projectDeletePreview).toHaveBeenCalledWith('p-alpha')
  })
})
