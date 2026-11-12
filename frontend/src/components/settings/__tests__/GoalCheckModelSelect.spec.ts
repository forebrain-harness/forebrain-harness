import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'
import type { VueWrapper } from '@vue/test-utils'

import { forebrainApi, type LlmModelChoice } from '@/lib/api'
import { setLocale } from '@/locales'
import { mountSettingsTab } from '../form/settingsTestHost'
import SettingsSubagentsTab from '../SettingsSubagentsTab.vue'

/**
 * 设置 · 子代理 · 「/goal 检查模型」(plan 299 §2, §5.2). The block is the picker
 * plus the level that belongs to the chosen row, so the cases here mount the
 * tab it lives in and drive the block: the unconfigured state, the menu and its
 * search, picking a row (and what the level then shows), switching rows,
 * overriding the level and restoring it, the caret that becomes the clear x,
 * and saving/discarding/reopening.
 *
 * What each save would store is the point of several cases, so they assert the
 * payload `PATCH /api/settings` receives: a level equal to the row's own
 * default is stored as "" (a live reading of the model page) and only a level
 * that overrides it is stored as itself.
 */

/** Two rows of one provider/model (told apart by their address), a row whose
 *  levels differ from theirs, and a model with no levels at all. */
const CHOICES: LlmModelChoice[] = [
  {
    id: 'llm-a',
    provider: 'deepseek',
    providerName: '深度求索',
    model: 'deepseek-v4-flash',
    baseUrl: 'https://api.deepseek.com',
    apiPath: '/chat/completions',
    endpoint: 'https://api.deepseek.com/chat/completions',
    label: 'deepseek/deepseek-v4-flash',
    efforts: ['low', 'medium', 'high'],
    defaultEffort: 'high',
    supportsImages: false,
  },
  {
    id: 'llm-a-twin',
    provider: 'deepseek',
    providerName: '深度求索',
    model: 'deepseek-v4-flash',
    baseUrl: 'https://llm-proxy.corp.example.com/deepseek',
    apiPath: '/chat/completions',
    endpoint: 'https://llm-proxy.corp.example.com/deepseek/chat/completions',
    label: 'deepseek/deepseek-v4-flash',
    efforts: ['low', 'high'],
    defaultEffort: 'low',
    supportsImages: false,
  },
  {
    id: 'llm-max',
    provider: 'volcengine',
    providerName: '火山方舟',
    model: 'glm-5.3',
    baseUrl: 'https://ark.cn-beijing.volces.com/api/plan',
    apiPath: '/v1/messages',
    endpoint: 'https://ark.cn-beijing.volces.com/api/plan/v1/messages',
    label: 'volcengine/glm-5.3',
    efforts: ['low', 'high', 'max'],
    defaultEffort: 'max',
    supportsImages: false,
  },
  {
    id: 'llm-plain',
    provider: 'tencent',
    providerName: '腾讯云 TokenHub',
    model: 'hy3',
    baseUrl: 'https://tokenhub.tencentmaas.com/v1',
    apiPath: '/chat/completions',
    endpoint: 'https://tokenhub.tencentmaas.com/v1/chat/completions',
    label: 'tencent/hy3',
    efforts: [],
    defaultEffort: '',
    supportsImages: false,
  },
]

/** The stored configuration one case starts from. */
function settingsWith(values: Record<string, unknown>) {
  return {
    values: {
      'agents.defaults.enable_subagent': false,
      'agents.defaults.execution.max_parallel_subagents': 7,
      'agents.goal_evaluator.model': '',
      'agents.goal_evaluator.reasoning_effort': '',
      ...values,
    },
    secrets: {},
    meta: { approvalPresetsVisible: true, claudeCodeAvailable: false, builtinHandoffPrompt: 'builtin prompt' },
  }
}

describe('GoalCheckModelSelect (设置 · 子代理 · /goal 检查模型)', () => {
  beforeEach(() => {
    setLocale('zh')
    // The picker reads the one list every model picker reads.
    vi.spyOn(forebrainApi, 'llmModelChoices').mockResolvedValue(CHOICES)
    vi.spyOn(forebrainApi, 'saveSettings').mockResolvedValue({ saved: true, applies: 'next_turn' })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  async function open(values: Record<string, unknown>): Promise<VueWrapper> {
    vi.spyOn(forebrainApi, 'settings').mockResolvedValue(settingsWith(values) as never)
    const wrapper = await mountSettingsTab({ tab: 'subagents', component: SettingsSubagentsTab })
    await flushPromises()
    return wrapper
  }

  /** The block, so the tab's other rows never answer these questions. */
  const block = (wrapper: VueWrapper) => wrapper.find('[data-group="goal-check-model"]')
  const effort = (wrapper: VueWrapper) => block(wrapper).find('[data-goal-check-effort]')
  const hint = (wrapper: VueWrapper) => block(wrapper).find('[data-goal-check-hint]')
  const combo = (wrapper: VueWrapper) => block(wrapper).find('[data-goal-check-model]')

  async function pick(wrapper: VueWrapper, id: string): Promise<void> {
    if (!block(wrapper).find('.gcs__menu').exists()) await combo(wrapper).trigger('click')
    await block(wrapper).find(`[data-goal-check-option="${id}"]`).trigger('click')
    await flushPromises()
  }

  async function chooseLevel(wrapper: VueWrapper, level: string): Promise<void> {
    const select = effort(wrapper).element as HTMLSelectElement
    select.value = level
    await effort(wrapper).trigger('change')
    await flushPromises()
  }

  it('opens unconfigured: both dropdowns say 跟随主代理, the level is disabled, no hint, no save bar', async () => {
    const wrapper = await open({})

    expect(block(wrapper).attributes('data-group')).toBe('goal-check-model')
    expect(block(wrapper).find('h3').text()).toBe('/goal 检查模型')
    // The prototype draws no explanation under the group title (§2).
    expect(block(wrapper).find('.gc__head p').exists()).toBe(false)

    expect(combo(wrapper).text()).toContain('跟随主代理')
    const select = effort(wrapper)
    expect(select.attributes('disabled')).toBeDefined()
    expect(select.classes()).toContain('gc__select--unset')
    expect((select.element as HTMLSelectElement).value).toBe('')
    // One placeholder option and no levels: nothing to choose without a model.
    expect(select.findAll('option').map((option) => option.text())).toEqual(['跟随主代理'])

    expect(hint(wrapper).exists()).toBe(false)
    expect(block(wrapper).find('[data-goal-check-reset]').exists()).toBe(false)
    expect(block(wrapper).find('.gc__tray--open').exists()).toBe(false)
    // The frozen prototype draws the menu open.
    expect(block(wrapper).find('.gcs__menu').exists()).toBe(true)
  })

  it('searches the candidates, and offers every row with its own address', async () => {
    const wrapper = await open({})
    const options = () => block(wrapper).findAll('[data-goal-check-option]')

    expect(options()).toHaveLength(CHOICES.length)
    // The two twin rows are told apart by the second line, not by their name.
    expect(block(wrapper).find('[data-goal-check-option="llm-a"]').text()).toContain('https://api.deepseek.com')
    expect(block(wrapper).find('[data-goal-check-option="llm-a-twin"]').text()).toContain('https://llm-proxy.corp.example.com/deepseek')

    await block(wrapper).find('[data-goal-check-search]').setValue('火山')
    expect(options()).toHaveLength(1)
    expect(options()[0].attributes('data-goal-check-option')).toBe('llm-max')

    await block(wrapper).find('[data-goal-check-search]').setValue('nothing matches this')
    expect(options()).toHaveLength(0)
    expect(block(wrapper).find('.gcs__empty').text()).toBe('没有匹配的模型')
  })

  it('picks a row: the combo echoes name + provider, the level follows that row', async () => {
    const wrapper = await open({})
    await pick(wrapper, 'llm-a')

    // Collapsed echo: the model and its provider, and not the address (§2).
    const echo = combo(wrapper).text()
    expect(echo).toContain('deepseek-v4-flash')
    expect(echo).toContain('深度求索')
    expect(echo).not.toContain('https://api.deepseek.com')
    expect(block(wrapper).find('.gcs__menu').exists()).toBe(false)

    // The level is the row's own default, and the select is live again.
    expect(effort(wrapper).attributes('disabled')).toBeUndefined()
    expect(effort(wrapper).classes()).not.toContain('gc__select--unset')
    expect((effort(wrapper).element as HTMLSelectElement).value).toBe('high')
    expect(effort(wrapper).findAll('option').map((option) => option.text())).toEqual([
      '跟随模型设置',
      '低 low',
      '中 medium',
      '高 high',
    ])
    expect(hint(wrapper).text()).toBe('模型页配置的默认强度：高 high')
    expect(block(wrapper).find('[data-goal-check-reset]').exists()).toBe(false)
  })

  it('switches rows: the levels and the shown value move with the new row', async () => {
    const wrapper = await open({})
    await pick(wrapper, 'llm-a')
    expect((effort(wrapper).element as HTMLSelectElement).value).toBe('high')

    await pick(wrapper, 'llm-max')
    expect((effort(wrapper).element as HTMLSelectElement).value).toBe('max')
    expect(effort(wrapper).findAll('option').map((option) => option.text())).toEqual([
      '跟随模型设置',
      '低 low',
      '高 high',
      '极致 max',
    ])
    expect(hint(wrapper).text()).toBe('模型页配置的默认强度：极致 max')
  })

  it('overrides the level, restores it, and stores "" for the row’s own default', async () => {
    const wrapper = await open({})
    await pick(wrapper, 'llm-a')

    await chooseLevel(wrapper, 'low')
    expect(hint(wrapper).text()).toBe('已改为 低 low，仅影响目标检查子代理')
    expect(block(wrapper).find('[data-goal-check-reset]').text()).toBe('恢复为模型默认')
    expect((effort(wrapper).element as HTMLSelectElement).value).toBe('low')

    // 保存 → the override is what is stored.
    await block(wrapper).find('[data-goal-check-save]').trigger('click')
    await flushPromises()
    expect(forebrainApi.saveSettings).toHaveBeenCalledWith(
      {
        'agents.goal_evaluator.model': 'llm-a',
        'agents.goal_evaluator.reasoning_effort': 'low',
      },
      {},
    )

    // 恢复为模型默认 → back to the live reading, and that is stored as "".
    vi.mocked(forebrainApi.saveSettings).mockClear()
    await block(wrapper).find('[data-goal-check-reset]').trigger('click')
    await flushPromises()
    expect(hint(wrapper).text()).toBe('模型页配置的默认强度：高 high')
    expect(block(wrapper).find('[data-goal-check-reset]').exists()).toBe(false)

    await block(wrapper).find('[data-goal-check-save]').trigger('click')
    await flushPromises()
    expect(forebrainApi.saveSettings).toHaveBeenCalledWith(
      { 'agents.goal_evaluator.reasoning_effort': '' },
      {},
    )
  })

  it('stores "" when the chosen level is the row’s own default, and the level otherwise', async () => {
    // A stored override, so both directions travel on the wire.
    const wrapper = await open({
      'agents.goal_evaluator.model': 'llm-a',
      'agents.goal_evaluator.reasoning_effort': 'low',
    })
    expect(hint(wrapper).text()).toBe('已改为 低 low，仅影响目标检查子代理')

    // Choosing the default level explicitly is the same setting as "follow it".
    await chooseLevel(wrapper, 'high')
    await block(wrapper).find('[data-goal-check-save]').trigger('click')
    await flushPromises()
    expect(forebrainApi.saveSettings).toHaveBeenCalledWith(
      { 'agents.goal_evaluator.reasoning_effort': '' },
      {},
    )

    vi.mocked(forebrainApi.saveSettings).mockClear()
    await chooseLevel(wrapper, 'medium')
    await block(wrapper).find('[data-goal-check-save]').trigger('click')
    await flushPromises()
    expect(forebrainApi.saveSettings).toHaveBeenCalledWith(
      { 'agents.goal_evaluator.reasoning_effort': 'medium' },
      {},
    )
  })

  it('clears both controls through the caret’s x and hides the save bar again', async () => {
    const wrapper = await open({
      'agents.goal_evaluator.model': 'llm-max',
      'agents.goal_evaluator.reasoning_effort': 'low',
    })
    expect((effort(wrapper).element as HTMLSelectElement).value).toBe('low')
    expect(block(wrapper).find('.gc__tray--open').exists()).toBe(false)

    await block(wrapper).find('[data-goal-check-clear]').trigger('click')
    await flushPromises()

    expect(combo(wrapper).text()).toContain('跟随主代理')
    expect(combo(wrapper).attributes('data-goal-check-model')).toBe('')
    expect(effort(wrapper).attributes('disabled')).toBeDefined()
    expect((effort(wrapper).element as HTMLSelectElement).value).toBe('')
    expect(hint(wrapper).exists()).toBe(false)
    expect(block(wrapper).find('[data-goal-check-reset]').exists()).toBe(false)
    expect(block(wrapper).find('.gcs__menu').exists()).toBe(false)

    // Clearing is a change: the bar offers to save it.
    expect(block(wrapper).find('.gc__tray--open').exists()).toBe(true)
    await block(wrapper).find('[data-goal-check-save]').trigger('click')
    await flushPromises()
    expect(forebrainApi.saveSettings).toHaveBeenCalledWith(
      {
        'agents.goal_evaluator.model': '',
        'agents.goal_evaluator.reasoning_effort': '',
      },
      {},
    )
  })

  it('saves, shows 已保存, and discards back to the stored values', async () => {
    const wrapper = await open({})
    await pick(wrapper, 'llm-a')
    await block(wrapper).find('[data-goal-check-save]').trigger('click')
    await flushPromises()
    expect(block(wrapper).find('[data-goal-check-saved]').text()).toBe('✓ 已保存，下一轮对话生效')
    expect(block(wrapper).find('.gc__tray--open').exists()).toBe(true)

    // A second change, then 放弃更改: both the model and the level go back.
    await pick(wrapper, 'llm-max')
    await chooseLevel(wrapper, 'low')
    await block(wrapper).find('[data-goal-check-discard]').trigger('click')
    await flushPromises()
    expect((effort(wrapper).element as HTMLSelectElement).value).toBe('high')
    expect(combo(wrapper).text()).toContain('deepseek-v4-flash')
  })

  it('reopens on what was stored: a configured model with an override', async () => {
    const wrapper = await open({
      'agents.goal_evaluator.model': 'llm-max',
      'agents.goal_evaluator.reasoning_effort': 'low',
    })
    expect(combo(wrapper).text()).toContain('glm-5.3')
    expect((effort(wrapper).element as HTMLSelectElement).value).toBe('low')
    expect(hint(wrapper).text()).toBe('已改为 低 low，仅影响目标检查子代理')
    expect(block(wrapper).find('[data-goal-check-reset]').exists()).toBe(true)
    expect(block(wrapper).find('.gc__tray--open').exists()).toBe(false)
  })

  it('keeps twin rows apart: each stores its own row id', async () => {
    const first = await open({})
    await pick(first, 'llm-a')
    await block(first).find('[data-goal-check-save]').trigger('click')
    await flushPromises()
    expect(forebrainApi.saveSettings).toHaveBeenLastCalledWith(
      { 'agents.goal_evaluator.model': 'llm-a' },
      {},
    )
    first.unmount()

    vi.mocked(forebrainApi.saveSettings).mockClear()
    const second = await open({})
    await pick(second, 'llm-a-twin')
    // The twin's own default level, which the echo follows.
    expect((effort(second).element as HTMLSelectElement).value).toBe('low')
    await block(second).find('[data-goal-check-save]').trigger('click')
    await flushPromises()
    expect(forebrainApi.saveSettings).toHaveBeenLastCalledWith(
      { 'agents.goal_evaluator.model': 'llm-a-twin' },
      {},
    )

    // Reopened on the twin, the echo resolves to the twin's row rather than
    // the first of the same name: its levels are the twin's own.
    second.unmount()
    const reopened = await open({ 'agents.goal_evaluator.model': 'llm-a-twin' })
    expect((effort(reopened).element as HTMLSelectElement).value).toBe('low')
    expect(effort(reopened).findAll('option').map((option) => option.text())).toEqual([
      '跟随模型设置',
      '低 low',
      '高 high',
    ])
  })

  it('accepts a model with no levels: only 跟随模型设置, no level, no error', async () => {
    const wrapper = await open({})
    await pick(wrapper, 'llm-plain')

    expect((effort(wrapper).element as HTMLSelectElement).value).toBe('')
    expect(effort(wrapper).findAll('option').map((option) => option.text())).toEqual(['跟随模型设置'])
    // 该模型未配置默认强度 — stated, not left blank and not an error.
    expect(hint(wrapper).text()).toBe('该模型未配置默认强度')
    expect(block(wrapper).find('[data-goal-check-reset]').exists()).toBe(false)
  })
})
