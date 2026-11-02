import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'

import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'
import { mountSettingsTab } from './form/settingsTestHost'
import SettingsSubagentsTab from './SettingsSubagentsTab.vue'

/** The subagents tab (plan 191 §0.14, §4.3): the feature switch and the
 * parallel limit, both saved through the settings interface. */
describe('SettingsSubagentsTab', () => {
  beforeEach(() => {
    setLocale('zh')
    vi.spyOn(forebrainApi, 'settings').mockResolvedValue({
      values: {
        'agents.defaults.enable_subagent': false,
        'agents.defaults.execution.max_parallel_subagents': 7,
      },
      secrets: {},
      // claudeCodeAvailable: false — a machine without Claude Code, so the
      // peers group stays hidden (plan 195) and this tab keeps its two rows.
      meta: { approvalPresetsVisible: true, claudeCodeAvailable: false, builtinHandoffPrompt: 'builtin prompt' },
    })
    vi.spyOn(forebrainApi, 'saveSettings').mockResolvedValue({ saved: true, applies: 'next_turn' })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  async function mountTab() {
    const wrapper = await mountSettingsTab({ tab: 'subagents', component: SettingsSubagentsTab })
    await flushPromises()
    return wrapper
  }

  it('draws the two rows with the prototype\'s words and hints', async () => {
    const wrapper = await mountTab()
    // The tab's only group carries no title of its own (§0.5): the two rows'
    // names are the headings.
    expect(wrapper.find('h3').exists()).toBe(false)
    const enable = wrapper.find('[data-field="agents.defaults.enable_subagent"]')
    expect(enable.find('b').text()).toBe('子代理功能')
    expect(enable.find('.settings-row__hint').text()).toBe('各主代理可在子代理页单独开关')
    expect(enable.find('[data-switch="agents.defaults.enable_subagent"]').attributes('aria-checked')).toBe('false')

    const limit = wrapper.find('[data-field="agents.defaults.execution.max_parallel_subagents"]')
    expect(limit.find('b').text()).toBe('并行上限')
    expect(limit.find('.settings-row__hint').text()).toBe('初始值按本机 CPU 核数减一')
    expect((limit.find('[data-num="agents.defaults.execution.max_parallel_subagents"]').element as HTMLInputElement).value).toBe('7')
  })

  it('opens with the stored switch, saves nothing until it moves, and cancels back', async () => {
    const wrapper = await mountTab()
    const group = wrapper.find('[data-group="sa"]')
    expect(group.find('[data-save="sa"]').exists()).toBe(false)

    await group.find('[data-switch="agents.defaults.enable_subagent"]').trigger('click')
    expect(group.find('[data-save="sa"]').exists()).toBe(true)

    await group.find('.settings-group__cancel').trigger('click')
    expect(group.find('[data-save="sa"]').exists()).toBe(false)
    expect(group.find('[data-switch="agents.defaults.enable_subagent"]').attributes('aria-checked')).toBe('false')
  })

  it('saves the toggled switch through the global default', async () => {
    const wrapper = await mountTab()
    const group = wrapper.find('[data-group="sa"]')
    await group.find('[data-switch="agents.defaults.enable_subagent"]').trigger('click')
    await group.find('[data-save="sa"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.saveSettings).toHaveBeenCalledWith({ 'agents.defaults.enable_subagent': true }, {})
    expect(group.find('.settings-group__saved').text()).toBe('已保存，下一轮生效')
  })
})
