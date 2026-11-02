import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'

import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'
import { mountSettingsTab } from './form/settingsTestHost'
import SettingsSessionTab from './SettingsSessionTab.vue'

/** The conversation's own settings (plan 191 §0.13, §4.3): five groups —
 * compaction, retries and timeouts, the context's headroom, the input
 * guardrails, and the token estimator. The handoff prompt's 恢复默认 only puts
 * the built-in text back on screen; saving is a separate click. */
describe('SettingsSessionTab', () => {
  const BUILTIN_PROMPT = 'You are performing a CONTEXT CHECKPOINT COMPACTION.'

  beforeEach(() => {
    setLocale('zh')
    vi.spyOn(forebrainApi, 'settings').mockResolvedValue({
      values: {
        'compact.auto_compact_percent': 90,
        'compact.prompt': '会话专用交接词',
        'agents.defaults.network_retry_max': 10,
        'agents.defaults.stream_idle_timeout_seconds': 300,
        'agents.defaults.context_inject.model_context_tokens': 200000,
        'agents.defaults.context_inject.warn_remaining_percent': 12,
        'agents.defaults.context_inject.max_pre_hook_rules_chars': 120000,
        'agents.defaults.guardrails.input.max_runes': 200000,
        'agents.defaults.guardrails.input.block_substrings': ['AKIA'],
        'agents.defaults.token_estimate.download': true,
        'agents.defaults.token_estimate.hubs': ['huggingface', 'modelscope'],
        'agents.defaults.token_estimate.huggingface_endpoint': 'https://huggingface.co',
        'agents.defaults.token_estimate.modelscope_endpoint': 'https://modelscope.cn',
        'agents.defaults.token_estimate.default_tokenizer': 'gpt-o200k',
        'agents.defaults.token_estimate.tokenizers': [['glm-', 'glm-4']],
      },
      secrets: {},
      // claudeCodeAvailable: false — a machine without Claude Code, so the
      // peers group stays hidden (plan 195) and the tab keeps its rows.
      meta: { approvalPresetsVisible: true, claudeCodeAvailable: false, builtinHandoffPrompt: BUILTIN_PROMPT },
    })
    vi.spyOn(forebrainApi, 'saveSettings').mockResolvedValue({ saved: true, applies: 'next_turn' })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  async function mountTab() {
    const wrapper = await mountSettingsTab({ tab: 'session', component: SettingsSessionTab })
    await flushPromises()
    return wrapper
  }

  it('draws the prototype\'s five groups in its order', async () => {
    const wrapper = await mountTab()
    expect(wrapper.findAll('.settings-group').map((group) => group.attributes('data-group'))).toEqual([
      'cp',
      'net2',
      'ci',
      'gr',
      'te',
    ])
    expect(wrapper.findAll('.settings-group h3').map((title) => title.text())).toEqual([
      '上下文压缩',
      '重试与超时',
      '上下文余量',
      '护栏',
      '词元估算',
    ])
  })

  it('shows the stored handoff prompt, restores the built-in one on screen only', async () => {
    const wrapper = await mountTab()
    const group = wrapper.find('[data-group="cp"]')
    const prompt = group.find('[data-area="compact.prompt"]')
    expect((prompt.element as HTMLTextAreaElement).value).toBe('会话专用交接词')

    const restore = group.find('[data-restore="compact.prompt"]')
    expect(restore.attributes('disabled')).toBeUndefined()
    await restore.trigger('click')
    expect((prompt.element as HTMLTextAreaElement).value).toBe(BUILTIN_PROMPT)
    // Restoring is an edit, not a save: the bar appears, nothing is sent.
    expect(group.find('[data-save="cp"]').exists()).toBe(true)
    expect(forebrainApi.saveSettings).not.toHaveBeenCalled()
  })

  it('greys 恢复默认 out while the text already is the built-in one', async () => {
    vi.mocked(forebrainApi.settings).mockResolvedValue({
      values: { 'compact.auto_compact_percent': 90, 'compact.prompt': BUILTIN_PROMPT },
      secrets: {},
      meta: { approvalPresetsVisible: true, claudeCodeAvailable: false, builtinHandoffPrompt: BUILTIN_PROMPT },
    })
    const wrapper = await mountTab()
    expect(wrapper.find('[data-restore="compact.prompt"]').attributes('disabled')).toBeDefined()
  })

  it('clamps the compaction threshold to the edge rather than flagging it, and saves it', async () => {
    const wrapper = await mountTab()
    const group = wrapper.find('[data-group="cp"]')
    const threshold = group.find('[data-num="compact.auto_compact_percent"]')
    expect(group.find('.settings-num__range').text()).toBe('50–90')

    await threshold.setValue('70')
    expect((threshold.element as HTMLInputElement).value).toBe('70')

    await threshold.setValue('120')
    expect((threshold.element as HTMLInputElement).value).toBe('90')
    expect(group.find('[data-field="compact.auto_compact_percent"]').find('.settings-num__box').classes()).not.toContain(
      'is-bad',
    )

    await threshold.setValue('70')
    await group.find('[data-save="cp"]').trigger('click')
    await flushPromises()
    expect(forebrainApi.saveSettings).toHaveBeenCalledWith({ 'compact.auto_compact_percent': 70 }, {})
  })

  it('clamps the headroom to its edge the moment a number past it is typed', async () => {
    const wrapper = await mountTab()
    const headroom = wrapper.find('[data-num="agents.defaults.context_inject.warn_remaining_percent"]')
    await headroom.setValue('80')
    const field = wrapper.find('[data-field="agents.defaults.context_inject.warn_remaining_percent"]')
    expect((headroom.element as HTMLInputElement).value).toBe('50')
    expect(field.find('.settings-num__box').classes()).not.toContain('is-bad')
  })

  it('saves the guardrails group\'s changed rows and nothing else', async () => {
    const wrapper = await mountTab()
    const group = wrapper.find('[data-group="gr"]')
    await group.find('[data-num="agents.defaults.guardrails.input.max_runes"]').setValue('100000')
    await group.find('[data-save="gr"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.saveSettings).toHaveBeenCalledWith(
      { 'agents.defaults.guardrails.input.max_runes': 100000 },
      {},
    )
  })

  it('draws the token estimator\'s mapping rows with their two columns', async () => {
    const wrapper = await mountTab()
    const group = wrapper.find('[data-group="te"]')
    const first = group.find('[data-pair="agents.defaults.token_estimate.tokenizers"][data-i="0"][data-c="0"]')
    expect((first.element as HTMLInputElement).value).toBe('glm-')
    expect(group.find('[data-add="agents.defaults.token_estimate.tokenizers"]').text()).toContain('添加映射')
  })
})
