import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'

import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'
import { mountSettingsTab } from './form/settingsTestHost'
import MemorySwitchesTab from './MemorySwitchesTab.vue'

/** The memory tab (plan 191 §0.16, §4.3): the one switch drawn in the
 * group-title type with its own explanation, the three groups that follow it
 * only while the switch is on — following the form's value, not the stored
 * one — and 重置记忆, which is always there. */
describe('MemorySwitchesTab', () => {
  beforeEach(() => {
    setLocale('zh')
    vi.spyOn(forebrainApi, 'settings').mockResolvedValue({
      values: {
        'features.memories': true,
        'memories.extract_model': '',
        'memories.consolidation_model': '',
        'memories.search_top_k': 5,
        'memories.max_raw_memories_for_consolidation': 256,
        'memories.max_unused_days': 30,
        'memories.max_rollout_age_days': 10,
        'memories.max_rollouts_per_startup': 2,
        'memories.min_rollout_idle_hours': 6,
      },
      secrets: {},
      // claudeCodeAvailable: false — a machine without Claude Code, so the
      // peers group stays hidden (plan 195) and this tab keeps its rows.
      meta: { approvalPresetsVisible: true, claudeCodeAvailable: false, builtinHandoffPrompt: 'builtin prompt' },
    })
    vi.spyOn(forebrainApi, 'saveSettings').mockResolvedValue({ saved: true, applies: 'next_turn' })
    vi.spyOn(forebrainApi, 'resetMemories').mockResolvedValue({ ok: true })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  async function mountTab() {
    const wrapper = await mountSettingsTab({ tab: 'memory', component: MemorySwitchesTab })
    await flushPromises()
    return wrapper
  }

  it('draws the switch in the group-title type with the prototype\'s explanation', async () => {
    const wrapper = await mountTab()
    const head = wrapper.find('[data-group="ms"]')
    expect(head.find('h3').exists()).toBe(false)
    const name = head.find('[data-field="features.memories"] b')
    expect(name.text()).toBe('记忆功能')
    expect(name.classes()).toContain('settings-row__title')
    expect(head.find('[data-field="features.memories"]').find('.settings-row__hint').text()).toBe(
      '记下你的偏好与要点，供以后的会话使用；仅对办公创作和创意编程类项目生效。跑批项目无记忆功能',
    )
  })

  it('shows the three groups while the switch is on and folds them the moment it goes off', async () => {
    const wrapper = await mountTab()
    expect(wrapper.find('[data-group="mr"]').find('h3').text()).toBe('模型')
    expect(wrapper.find('[data-group="mq"]').find('h3').text()).toBe('检索与整合')
    const sessions = wrapper.find('[data-group="mo"]')
    expect(sessions.find('h3').text()).toBe('会话处理')
    expect(sessions.find('.settings-group__head p').text()).toContain('你在终端或网页的会话里每发一条消息')

    // The fold follows the form's own value, before anything is saved (§0.16).
    await wrapper.find('[data-switch="features.memories"]').trigger('click')
    expect(wrapper.find('[data-group="mr"]').exists()).toBe(false)
    expect(wrapper.find('[data-group="mq"]').exists()).toBe(false)
    expect(wrapper.find('[data-group="mo"]').exists()).toBe(false)

    await wrapper.find('[data-switch="features.memories"]').trigger('click')
    expect(wrapper.find('[data-group="mr"]').exists()).toBe(true)
  })

  it('saves the switch off through the settings interface and says when it applies', async () => {
    const wrapper = await mountTab()
    const head = wrapper.find('[data-group="ms"]')
    await head.find('[data-switch="features.memories"]').trigger('click')
    await head.find('[data-save="ms"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.saveSettings).toHaveBeenCalledWith({ 'features.memories': false }, {})
    expect(head.find('.settings-group__saved').text()).toBe('已保存，下一轮生效')
  })

  it('keeps 重置记忆 behind its confirmation, always drawn', async () => {
    const wrapper = await mountTab()
    const reset = wrapper.find('[data-testid="memory-switches-reset"]')
    expect(reset.exists()).toBe(true)

    vi.spyOn(window, 'confirm').mockReturnValue(false)
    await reset.trigger('click')
    expect(forebrainApi.resetMemories).not.toHaveBeenCalled()

    vi.spyOn(window, 'confirm').mockReturnValue(true)
    await reset.trigger('click')
    await flushPromises()
    expect(forebrainApi.resetMemories).toHaveBeenCalled()
  })
})
