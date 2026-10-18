import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'

import { hideTip, useTooltipState } from '@/composables/useTooltip'
import tip from '@/directives/tip'
import { forebrainApi, type ProjectChannelRow } from '@/lib/api'
import { setLocale } from '@/locales'

import ProjectChannels from './ProjectChannels.vue'

/**
 * A project's channels tab (plan 075): the table the server pages, one row per
 * channel kind — named the way the table names it, identified by the kind a
 * scheduled task delivers to, its configuration in the channel's own keys — and
 * a switch that cannot be turned on before there is something to turn on. The
 * page has no save button: the switch and the editor each write on their own.
 */
function feishuRow(overrides: Partial<ProjectChannelRow> = {}): ProjectChannelRow {
  return {
    kind: 'feishu',
    labelZh: '飞书',
    labelEn: 'Feishu',
    enabled: true,
    configured: true,
    fields: { app_id: 'cli_a1b2c3d4', app_secret: '[REDACTED]', domain: '', enabled: true },
    secretKeys: ['app_secret'],
    secretHints: { app_secret: '••••8c21' },
    ...overrides,
  }
}

function slackRow(): ProjectChannelRow {
  return {
    kind: 'slack',
    labelZh: 'Slack',
    labelEn: 'Slack',
    enabled: false,
    configured: false,
    fields: { bot_token: '', secret: '', outbound_url: '', enabled: false },
    secretKeys: ['bot_token', 'secret'],
    secretHints: {},
  }
}

async function mountTab(): Promise<VueWrapper> {
  vi.mocked(forebrainApi.projectChannelsPage).mockResolvedValue({
    rows: [feishuRow(), slackRow()],
    total: 2,
    page: 1,
    pageSize: 10,
  })
  const wrapper = mount(ProjectChannels, {
    props: { project: {}, projectId: 'p1' },
    global: { stubs: { teleport: true }, directives: { tip } },
  })
  await flushPromises()
  return wrapper
}

describe('ProjectChannels', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    hideTip()
    vi.spyOn(forebrainApi, 'projectChannelsPage')
    vi.spyOn(forebrainApi, 'projectChannelsRaw').mockResolvedValue({
      kinds: ['feishu', 'telegram', 'slack'],
      channels: {
        feishu: { app_id: 'cli_a1b2c3d4', app_secret: '[REDACTED]', domain: '', enabled: true },
        telegram: { bot_token: '[REDACTED]', enabled: true },
        slack: { bot_token: '', secret: '', outbound_url: '', enabled: false },
      },
    })
    vi.spyOn(forebrainApi, 'saveProjectChannels').mockResolvedValue({ applied: true })
  })

  afterEach(() => {
    hideTip()
    vi.restoreAllMocks()
  })

  it('draws the channels as a table with the toolbar, the pager and the note', async () => {
    const wrapper = await mountTab()

    const search = wrapper.find('.forebrain-list-toolbar input[type="search"]')
    expect(search.attributes('placeholder')).toBe('搜索通道')
    expect(search.classes()).toContain('h-[34px]')
    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.findAll('[data-channel-row]')).toHaveLength(2)
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 2 条')
    expect(wrapper.find('p.forebrain-list-note').text()).toBe('本项目的消息通道；定时任务可投递到已启用的通道。')
  })

  it('names the columns the table carries', async () => {
    const wrapper = await mountTab()

    expect(wrapper.findAll('thead th').map((th) => th.text())).toEqual(['通道', '投递标识', '配置', '启用', '操作'])
  })

  it('shows a configured channel by its configuration and an empty one as unconfigured', async () => {
    const wrapper = await mountTab()

    const feishu = wrapper.find('[data-channel-row="feishu"]')
    expect(feishu.text()).toContain('飞书')
    expect(feishu.text()).toContain('feishu')
    // The masked secret is shown by the hint the server sent, not by a value.
    expect(feishu.text()).toContain('app_id cli_a1b2c3d4')
    expect(feishu.text()).toContain('app_secret ••••8c21')
    // An empty field is left out of the summary.
    expect(feishu.text()).not.toContain('domain')

    const slack = wrapper.find('[data-channel-row="slack"]')
    expect(slack.text()).toContain('未配置')
    expect(slack.text()).toContain('—')
    expect(slack.text()).not.toContain('bot_token')
  })

  it('keeps the switch of an unconfigured channel off, saying why', async () => {
    const wrapper = await mountTab()

    const slack = wrapper.find('[data-testid="channel-toggle-slack"]')
    expect(slack.attributes('disabled')).toBeDefined()

    await wrapper.find('[data-testid="channel-switch-slack"]').trigger('mouseenter')
    expect(useTooltipState().text).toBe('先编辑，填好配置')

    const feishu = wrapper.find('[data-testid="channel-toggle-feishu"]')
    expect(feishu.attributes('disabled')).toBeUndefined()
    expect(feishu.attributes('aria-checked')).toBe('true')
  })

  it('has no page-level save: the switch writes at once, the whole map with it', async () => {
    const wrapper = await mountTab()

    expect(wrapper.find('[data-testid="channels-save"]').exists()).toBe(false)

    await wrapper.find('[data-testid="channel-toggle-feishu"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.projectChannelsRaw).toHaveBeenCalledWith('p1')
    const [projectId, map] = vi.mocked(forebrainApi.saveProjectChannels).mock.calls[0]
    expect(projectId).toBe('p1')
    // Only this kind's switch moved; its secret stays masked and every other
    // kind travels back as it was read.
    expect(map.feishu).toMatchObject({ app_id: 'cli_a1b2c3d4', app_secret: '[REDACTED]', enabled: false })
    expect(map.telegram).toEqual({ bot_token: '[REDACTED]', enabled: true })
    expect(map.slack).toEqual({ bot_token: '', secret: '', outbound_url: '', enabled: false })
    // And the table behind it is read again.
    expect(forebrainApi.projectChannelsPage).toHaveBeenCalledTimes(2)
  })

  it('edits one channel in the dialog it opens for that row', async () => {
    const wrapper = await mountTab()

    expect(wrapper.find('[data-testid="channel-save"]').exists()).toBe(false)

    await wrapper.find('[data-testid="channel-edit-feishu"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('.forebrain-modal-title').text()).toBe('编辑通道 · 飞书')
    expect(wrapper.find('[data-testid="channel-field-app_secret"]').attributes('type')).toBe('password')
  })
})
