import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'

import tip from '@/directives/tip'
import { forebrainApi, type ProjectChannelRow } from '@/lib/api'
import { setLocale } from '@/locales'

import ChannelEditDialog from './ChannelEditDialog.vue'

/**
 * One channel's configuration (plan 075): every field the channel driver reads,
 * named by its own key, in two columns. A stored secret is never typed back —
 * its field starts empty under a placeholder that says leaving it alone keeps
 * the value — and a save writes the whole map so no other channel is cleared.
 */
function feishuRow(overrides: Partial<ProjectChannelRow> = {}): ProjectChannelRow {
  return {
    kind: 'feishu',
    labelZh: '飞书',
    labelEn: 'Feishu',
    enabled: true,
    configured: true,
    fields: { app_id: 'cli_a1b2c3d4', app_secret: '[REDACTED]', domain: '' },
    secretKeys: ['app_secret'],
    secretHints: { app_secret: '••••8c21' },
    ...overrides,
  }
}

/** The whole stored map, as the raw read answers it for every kind. */
function storedChannels() {
  return {
    kinds: ['feishu', 'telegram', 'slack'],
    channels: {
      feishu: { app_id: 'cli_a1b2c3d4', app_secret: '[REDACTED]', domain: '', enabled: true },
      telegram: { bot_token: '[REDACTED]', enabled: true },
      slack: { bot_token: '', secret: '', outbound_url: '', enabled: false },
    },
  }
}

function mountDialog(row: ProjectChannelRow = feishuRow()): VueWrapper {
  return mount(ChannelEditDialog, {
    props: { projectId: 'p1', row },
    global: { stubs: { teleport: true }, directives: { tip } },
  })
}

describe('ChannelEditDialog', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'projectChannelsRaw').mockResolvedValue(storedChannels())
    vi.spyOn(forebrainApi, 'saveProjectChannels').mockResolvedValue({ applied: true })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('names itself after the channel and every field by the key it reads', () => {
    const wrapper = mountDialog()

    expect(wrapper.find('.forebrain-modal-title').text()).toBe('编辑通道 · 飞书')
    const labels = wrapper.findAll('label > span').map((span) => span.text())
    expect(labels).toEqual(['app_id', 'app_secret', 'domain'])
    expect(wrapper.find('[data-testid="channel-field-app_id"]').attributes('value')).toBeUndefined()
    expect((wrapper.find('[data-testid="channel-field-app_id"]').element as HTMLInputElement).value).toBe('cli_a1b2c3d4')
  })

  it('leaves a stored secret empty behind a placeholder and says why', () => {
    const wrapper = mountDialog()

    const secret = wrapper.find('[data-testid="channel-field-app_secret"]')
    expect((secret.element as HTMLInputElement).value).toBe('')
    expect(secret.attributes('type')).toBe('password')
    expect(secret.attributes('placeholder')).toBe('已保存，不修改即保留')
    expect(wrapper.find('[data-testid="channel-secret-hint"]').text()).toBe('密钥已脱敏，不修改即保留原值。')

    // A channel with no stored secret has nothing to keep, so no hint either.
    const blank = mountDialog(feishuRow({ fields: { app_id: '', app_secret: '', domain: '' } }))
    expect(blank.find('[data-testid="channel-secret-hint"]').exists()).toBe(false)
  })

  it('saves the whole map, leaving an untouched secret masked and other channels as they were', async () => {
    const wrapper = mountDialog()

    await wrapper.find('[data-testid="channel-field-app_id"]').setValue('cli_typed')
    await wrapper.find('[data-testid="channel-save"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.projectChannelsRaw).toHaveBeenCalledWith('p1')
    expect(forebrainApi.saveProjectChannels).toHaveBeenCalledTimes(1)
    const [projectId, map] = vi.mocked(forebrainApi.saveProjectChannels).mock.calls[0]
    expect(projectId).toBe('p1')
    // The edited channel keeps the mask for the secret nobody typed over.
    expect(map.feishu).toMatchObject({ app_id: 'cli_typed', app_secret: '[REDACTED]', domain: '', enabled: true })
    // Every other kind travels back unchanged: the PUT clears what it drops.
    expect(map.telegram).toEqual({ bot_token: '[REDACTED]', enabled: true })
    expect(map.slack).toEqual({ bot_token: '', secret: '', outbound_url: '', enabled: false })
    expect(wrapper.emitted('saved')).toHaveLength(1)
  })

  it('sends a secret that was typed over and keeps the switch it found', async () => {
    const wrapper = mountDialog(feishuRow({ enabled: false }))

    await wrapper.find('[data-testid="channel-field-app_secret"]').setValue('typed-secret')
    await wrapper.find('[data-testid="channel-save"]').trigger('click')
    await flushPromises()

    const [, map] = vi.mocked(forebrainApi.saveProjectChannels).mock.calls[0]
    expect(map.feishu).toMatchObject({ app_secret: 'typed-secret', enabled: false })
  })

  it('reports a refused save instead of closing', async () => {
    vi.mocked(forebrainApi.saveProjectChannels).mockRejectedValue(new Error('taken'))
    const wrapper = mountDialog()

    await wrapper.find('[data-testid="channel-save"]').trigger('click')
    await flushPromises()

    expect(wrapper.emitted('saved')).toBeUndefined()
    expect(wrapper.text()).toContain('taken')
  })
})
