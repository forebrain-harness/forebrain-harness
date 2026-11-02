import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'

import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'
import { mountSettingsTab } from './form/settingsTestHost'
import SettingsApprovalTab from './SettingsApprovalTab.vue'

/** The approval-and-sandbox tab (plan 191 §0.12, §4.3): the preset with its two
 * described choices, the sandbox group that unfolds only while 默认 is chosen,
 * and the batch project group whose lone switch keeps its name. */
describe('SettingsApprovalTab', () => {
  beforeEach(() => {
    setLocale('zh')
    vi.spyOn(forebrainApi, 'settings').mockResolvedValue({
      values: {
        approval_preset: 'workspace_write',
        'sandbox_workspace_write.writable_roots': ['/Users/doudou/.cache/pnpm'],
        'sandbox_workspace_write.network_access': true,
        'batch.sandbox_default': true,
      },
      secrets: {},
      // claudeCodeAvailable: false — a machine without Claude Code, so the
      // peers group stays hidden (plan 195) and this tab keeps its rows.
      meta: { approvalPresetsVisible: true, claudeCodeAvailable: false, builtinHandoffPrompt: 'builtin prompt' },
    })
    vi.spyOn(forebrainApi, 'saveSettings').mockResolvedValue({ saved: true, applies: 'next_turn' })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  async function mountTab() {
    const wrapper = await mountSettingsTab({ tab: 'approval', component: SettingsApprovalTab })
    await flushPromises()
    return wrapper
  }

  it('is the prototype\'s flat form: no headings beyond the group titles, no card', async () => {
    const wrapper = await mountTab()
    expect(wrapper.find('h2').exists()).toBe(false)
    expect(wrapper.find('.forebrain-card').exists()).toBe(false)
    // The prototype's three groups: 审批预设, 沙箱可写工作区, 跑批项目.
    expect(wrapper.findAll('.settings-group').map((group) => group.attributes('data-group'))).toEqual([
      'preset',
      'sww',
      'batch',
    ])
  })

  it('offers the two presets with the prototype\'s words, and says whom they apply to', async () => {
    const wrapper = await mountTab()
    const radios = wrapper.findAll('[role="radio"]')
    expect(radios).toHaveLength(2)
    expect(radios.map((radio) => radio.attributes('aria-checked'))).toEqual(['true', 'false'])
    expect(wrapper.findAll('.settings-radio').map((row) => row.text())).toEqual([
      '默认可以读写当前工作区的文件并运行命令；修改工作区外的文件需要审批。',
      '完全访问可以修改工作区外的文件并访问互联网，无需审批。谨慎使用。',
    ])
    const preset = wrapper.find('[data-group="preset"]')
    expect(preset.find('h3').text()).toBe('审批预设')
    expect(preset.find('.settings-group__head p').text()).toBe('只对办公创作、创意编程项目生效')
  })

  it('unfolds the sandbox group under 默认 and folds it the moment 完全访问 is chosen', async () => {
    const wrapper = await mountTab()
    expect(wrapper.find('[data-group="sww"]').exists()).toBe(true)
    expect(wrapper.find('[data-group="sww"]').find('h3').text()).toBe('沙箱可写工作区')
    expect(wrapper.find('[data-field="sandbox_workspace_write.network_access"]').exists()).toBe(true)

    // The fold follows the form's own value, not the stored one (§0.12).
    await wrapper.findAll('[role="radio"]')[1].trigger('click')
    expect(wrapper.find('[data-group="sww"]').exists()).toBe(false)

    await wrapper.findAll('[role="radio"]')[0].trigger('click')
    expect(wrapper.find('[data-group="sww"]').exists()).toBe(true)
  })

  it('keeps the save bar off until the choice changes, and cancels back to the stored one', async () => {
    const wrapper = await mountTab()
    const preset = wrapper.find('[data-group="preset"]')
    expect(preset.find('[data-save="preset"]').exists()).toBe(false)

    await wrapper.findAll('[role="radio"]')[1].trigger('click')
    expect(preset.find('[data-save="preset"]').exists()).toBe(true)

    await preset.find('.settings-group__cancel').trigger('click')
    expect(preset.find('[data-save="preset"]').exists()).toBe(false)
    expect(wrapper.findAll('[role="radio"]').map((radio) => radio.attributes('aria-checked'))).toEqual([
      'true',
      'false',
    ])
  })

  it('saves the chosen preset through PATCH /api/settings and says when it applies', async () => {
    const wrapper = await mountTab()
    await wrapper.findAll('[role="radio"]')[1].trigger('click')
    await wrapper.find('[data-group="preset"]').find('[data-save="preset"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.saveSettings).toHaveBeenCalledWith({ approval_preset: 'full_access' }, {})
    expect(wrapper.find('[data-group="preset"]').find('.settings-group__saved').text()).toBe('已保存，下一轮生效')
  })

  it('shows the batch project group with its lone switch\'s own name', async () => {
    const wrapper = await mountTab()
    const batch = wrapper.find('[data-group="batch"]')
    expect(batch.exists()).toBe(true)
    expect(batch.find('h3').text()).toBe('跑批项目')
    // The one single-field group the prototype draws with a row name (§0.5).
    expect(batch.find('[data-field="batch.sandbox_default"]').find('b').text()).toBe('启用沙箱')
  })
})
