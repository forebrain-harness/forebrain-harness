import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'

import tip from '@/directives/tip'
import { forebrainApi, type ProjectChannelRow, type ProjectRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import { InlineTeleport } from '@/test-utils/portal'
import ProjectTaskNotify from './ProjectTaskNotify.vue'

/**
 * A batch project's 任务通知 (plan 153 A12): what is stored is exactly the list
 * on screen, so adding a target and deleting one both write the whole list back
 * through the project's PATCH. The channel dropdown only offers the channels
 * this project has enabled, and the recipients come from the project's own
 * channel targets — with typing one in always allowed.
 */
const at = 1760000000

function project(overrides: Partial<ProjectRecord> = {}): ProjectRecord {
  return {
    id: 'p1',
    name: '投行底稿审核',
    icon: '',
    description: '',
    instructions: '',
    root: '/tmp/ibs',
    projectKey: 'ibs',
    memoryScope: 'shared',
    resourceAccess: false,
    pinned: false,
    createdAt: at,
    updatedAt: at,
    projectType: 'batch',
    taskNotify: [{ channel: 'feishu', target: 'oc_1' }],
    ...overrides,
  }
}

function channelRow(overrides: Partial<ProjectChannelRow> = {}): ProjectChannelRow {
  return {
    kind: 'feishu',
    labelZh: '飞书',
    labelEn: 'Feishu',
    enabled: true,
    configured: true,
    fields: { enabled: true },
    secretKeys: [],
    secretHints: {},
    ...overrides,
  }
}

async function mountSection(overrides: Partial<ProjectRecord> = {}): Promise<VueWrapper> {
  const wrapper = mount(ProjectTaskNotify, {
    props: { project: project(overrides), projectId: 'p1' },
    global: { directives: { tip }, stubs: { teleport: InlineTeleport } },
    attachTo: document.body,
  })
  await flushPromises()
  return wrapper
}

describe('ProjectTaskNotify', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'projectUpdate').mockResolvedValue(project())
    vi.spyOn(forebrainApi, 'projectChannelsPage').mockResolvedValue({
      rows: [channelRow(), channelRow({ kind: 'email', enabled: false })],
      total: 2,
      page: 1,
      pageSize: 50,
    })
    vi.spyOn(forebrainApi, 'getChannelTargets').mockResolvedValue([
      { channel: 'feishu', target: 'oc_1', label: '投行底稿审核组' },
      { channel: 'email', target: 'audit-ibs@example.com', label: '审计组' },
    ])
  })

  afterEach(() => {
    vi.restoreAllMocks()
    document.body.innerHTML = ''
  })

  it('lists the targets the project already notifies', async () => {
    const wrapper = await mountSection()

    const row = wrapper.get('[data-testid="task-notify-row-0"]')
    expect(row.text()).toContain('飞书 · oc_1')
    expect(row.text()).toContain('任务结束（成功或失败）时通知')
    expect(wrapper.text()).toContain('每条任务结束（成功或失败）后通知这里的目标；定时任务也走同一份配置。')
    wrapper.unmount()
  })

  it('adds a target by writing the whole list, choosing a channel that is enabled', async () => {
    const wrapper = await mountSection({ taskNotify: [] })
    const update = vi.spyOn(forebrainApi, 'projectUpdate')

    expect(wrapper.get('[data-testid="task-notify-empty"]').text()).toBe('还没有通知目标')
    await wrapper.get('[data-testid="task-notify-add"]').trigger('click')
    await flushPromises()

    await wrapper.get('[data-testid="task-notify-recipient"]').setValue('oc_9')
    await wrapper.get('[data-testid="task-notify-submit"]').trigger('click')
    await flushPromises()

    // The enabled channel is the dialog's default, and the body is the whole
    // list — a PATCH replaces it.
    expect(update).toHaveBeenCalledWith('p1', { taskNotify: [{ channel: 'feishu', target: 'oc_9' }] })
    wrapper.unmount()
  })

  it('deletes one target and keeps the rest', async () => {
    const wrapper = await mountSection({
      taskNotify: [{ channel: 'feishu', target: 'oc_1' }, { channel: 'email', target: 'audit-ibs@example.com' }],
    })
    const update = vi.spyOn(forebrainApi, 'projectUpdate')

    await wrapper.get('[data-testid="task-notify-delete-0"]').trigger('click')
    await flushPromises()

    expect(update).toHaveBeenCalledWith('p1', { taskNotify: [{ channel: 'email', target: 'audit-ibs@example.com' }] })
    wrapper.unmount()
  })

  it('picks a recipient the project\'s channels have already spoken to', async () => {
    const wrapper = await mountSection({ taskNotify: [] })

    await wrapper.get('[data-testid="task-notify-add"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="task-notify-known"]').text()).toContain('飞书 · oc_1')

    await wrapper.get('[data-testid="task-notify-pick-email"]').trigger('click')
    await nextTick()
    expect((wrapper.get('[data-testid="task-notify-recipient"]').element as HTMLInputElement).value).toBe('audit-ibs@example.com')
    wrapper.unmount()
  })

  it('refuses to add without a recipient', async () => {
    const wrapper = await mountSection({ taskNotify: [] })
    const update = vi.spyOn(forebrainApi, 'projectUpdate')

    await wrapper.get('[data-testid="task-notify-add"]').trigger('click')
    await flushPromises()
    // The submit stays gray until there is a recipient at all.
    expect(wrapper.get('[data-testid="task-notify-submit"]').attributes('disabled')).toBeDefined()
    expect(update).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('says nothing is enabled when the project has no channel to send on', async () => {
    vi.spyOn(forebrainApi, 'projectChannelsPage').mockResolvedValue({
      rows: [channelRow({ enabled: false })],
      total: 1,
      page: 1,
      pageSize: 50,
    })
    const wrapper = await mountSection({ taskNotify: [] })

    await wrapper.get('[data-testid="task-notify-add"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('（本项目还没有启用通道）')
    expect(wrapper.get('[data-testid="task-notify-submit"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })
})
