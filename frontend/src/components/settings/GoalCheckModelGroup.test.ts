import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, onMounted } from 'vue'

import { forebrainApi, type LlmModelChoice } from '@/lib/api'
import { setLocale } from '@/locales'
import { useSettingsForm } from './form/useSettingsForm'
import GoalCheckModelGroup from './GoalCheckModelGroup.vue'

/** The /goal check's block (plan 299): one global row — a configured model row
 * and a thinking level — saved through the settings interface as the two
 * `agents.goal_evaluator.*` paths. The level is a picker over the chosen row's
 * own levels (plan 319), never free text. */
describe('GoalCheckModelGroup', () => {
  const rows: LlmModelChoice[] = [
    {
      id: 'llm-row-1',
      provider: 'openai',
      providerName: 'OpenAI',
      model: 'gpt-main',
      baseUrl: 'https://api.openai.test/v1',
      apiPath: '/chat/completions',
      endpoint: 'https://api.openai.test/v1/chat/completions',
      label: 'openai/gpt-main',
      efforts: ['low', 'high'],
      defaultEffort: 'low',
      supportsImages: false,
    },
    // A row the API says can turn thinking off: Off is then one of the row's
    // own levels, and the block adds nothing beside it.
    {
      id: 'llm-row-off',
      provider: 'openai',
      providerName: 'OpenAI',
      model: 'gpt-off',
      baseUrl: 'https://api.openai.test/v1',
      apiPath: '/chat/completions',
      endpoint: 'https://api.openai.test/v1/chat/completions',
      label: 'openai/gpt-off',
      efforts: ['none', 'high'],
      defaultEffort: '',
      supportsImages: false,
    },
  ]

  beforeEach(() => {
    setLocale('zh')
    vi.spyOn(forebrainApi, 'settings').mockResolvedValue({
      values: {
        'agents.goal_evaluator.model': 'llm-row-1',
        'agents.goal_evaluator.reasoning_effort': '',
      },
      secrets: {},
      meta: { approvalPresetsVisible: true, claudeCodeAvailable: false, builtinHandoffPrompt: 'builtin prompt' },
    })
    vi.spyOn(forebrainApi, 'llmModelChoices').mockResolvedValue(rows)
    vi.spyOn(forebrainApi, 'saveSettings').mockResolvedValue({ saved: true, applies: 'next_turn' })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  /** Mounts the block with the page's own form behind it, as SettingsView does. */
  async function mountGroup() {
    const Host = defineComponent({
      components: { UnderTest: GoalCheckModelGroup },
      setup() {
        const form = useSettingsForm()
        onMounted(() => void form.load())
        return { form }
      },
      template: '<UnderTest :form="form" />',
    })
    const wrapper = mount(Host)
    await flushPromises()
    await wrapper.vm.$nextTick()
    return wrapper
  }

  /** The block's effort select, as the values a reader can pick. */
  function effortValues(wrapper: Awaited<ReturnType<typeof mountGroup>>): string[] {
    return wrapper
      .find('[data-goal-check-effort]')
      .findAll('option')
      .map((option) => (option.element as HTMLOptionElement).value)
  }

  it('draws the model and a level taken from that model', async () => {
    const wrapper = await mountGroup()
    expect(wrapper.find('[data-group="goal-check-model"] h3').text()).toBe('/goal 检查模型')
    expect(wrapper.text()).toContain('思考强度')
    // The empty value first (follow the row), then exactly the row's levels.
    expect(effortValues(wrapper)).toEqual(['', 'low', 'high'])
    // Nothing differs from the stored values: the save bar stays collapsed.
    expect(wrapper.find('.gc__tray').classes()).not.toContain('gc__tray--open')
  })

  it('offers Off as one of the row\'s own levels when the row lists it', async () => {
    const wrapper = await mountGroup()
    // The menu opens on mount (the prototype's state), so the row is one click.
    await wrapper.find('[data-goal-check-option="llm-row-off"]').trigger('click')
    expect(effortValues(wrapper)).toEqual(['', 'none', 'high'])
  })

  it('waits for a model, and says so', async () => {
    vi.mocked(forebrainApi.settings).mockResolvedValue({
      values: { 'agents.goal_evaluator.model': '', 'agents.goal_evaluator.reasoning_effort': '' },
      secrets: {},
      meta: { approvalPresetsVisible: true, claudeCodeAvailable: false, builtinHandoffPrompt: 'builtin prompt' },
    })
    const wrapper = await mountGroup()
    expect((wrapper.find('[data-goal-check-effort]').element as HTMLSelectElement).disabled).toBe(true)
    expect(wrapper.find('[data-goal-check-hint]').text()).toBe('先指定模型再选思考强度')
    expect(effortValues(wrapper)).toEqual([''])
  })

  it('clears the level when the model changes, and saves what was picked', async () => {
    const wrapper = await mountGroup()
    await wrapper.find('[data-goal-check-effort]').setValue('high')
    expect((wrapper.find('[data-goal-check-effort]').element as HTMLSelectElement).value).toBe('high')

    // A level belongs to the row it was chosen for: another row starts over.
    await wrapper.find('[data-goal-check-option="llm-row-off"]').trigger('click')
    expect((wrapper.find('[data-goal-check-effort]').element as HTMLSelectElement).value).toBe('')

    await wrapper.find('[data-goal-check-effort]').setValue('none')
    await wrapper.find('[data-goal-check-save]').trigger('click')
    await flushPromises()
    expect(forebrainApi.saveSettings).toHaveBeenCalledWith(
      {
        'agents.goal_evaluator.model': 'llm-row-off',
        'agents.goal_evaluator.reasoning_effort': 'none',
      },
      {},
    )
    expect(wrapper.find('[data-goal-check-saved]').exists()).toBe(true)
  })

  it('saves the row default as an empty level, not as a level of its own', async () => {
    vi.mocked(forebrainApi.settings).mockResolvedValue({
      values: {
        'agents.goal_evaluator.model': 'llm-row-1',
        'agents.goal_evaluator.reasoning_effort': 'high',
      },
      secrets: {},
      meta: { approvalPresetsVisible: true, claudeCodeAvailable: false, builtinHandoffPrompt: 'builtin prompt' },
    })
    const wrapper = await mountGroup()
    await wrapper.find('[data-goal-check-effort]').setValue('low')
    await wrapper.find('[data-goal-check-save]').trigger('click')
    await flushPromises()
    // Only the level differs from the stored row, so it is the one path saved.
    expect(forebrainApi.saveSettings).toHaveBeenCalledWith(
      { 'agents.goal_evaluator.reasoning_effort': '' },
      {},
    )
  })
})
