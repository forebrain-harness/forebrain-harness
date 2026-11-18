import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, onMounted } from 'vue'

import { forebrainApi, type LlmModelChoice } from '@/lib/api'
import { setLocale } from '@/locales'
import { useSettingsForm } from './form/useSettingsForm'
import PlanReviewerModelGroup from './PlanReviewerModelGroup.vue'

/** The exit-plan reviewer's block (plan 313): one global row — a configured
 * model row and a thinking level — saved through the settings interface as the
 * two `agents.plan_reviewer.*` paths, and nothing else. */
describe('PlanReviewerModelGroup', () => {
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
    // A row the API says can turn thinking off, and one it says nothing about:
    // the picker is the answer's, never a list of its own (plan 319).
    {
      id: 'llm-row-off',
      provider: 'openai',
      providerName: 'OpenAI',
      model: 'gpt-off',
      baseUrl: 'https://api.openai.test/v1',
      apiPath: '/chat/completions',
      endpoint: 'https://api.openai.test/v1/chat/completions',
      label: 'openai/gpt-off',
      efforts: ['none', 'high', 'xhigh'],
      defaultEffort: '',
      supportsImages: false,
    },
    {
      id: 'llm-row-plain',
      provider: 'openai',
      providerName: 'OpenAI',
      model: 'gpt-plain',
      baseUrl: 'https://api.openai.test/v1',
      apiPath: '/chat/completions',
      endpoint: 'https://api.openai.test/v1/chat/completions',
      label: 'openai/gpt-plain',
      efforts: [],
      defaultEffort: '',
      supportsImages: false,
    },
  ]

  beforeEach(() => {
    setLocale('zh')
    vi.spyOn(forebrainApi, 'settings').mockResolvedValue({
      values: {
        'agents.plan_reviewer.model': 'llm-row-1',
        'agents.plan_reviewer.reasoning_effort': '',
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
      components: { UnderTest: PlanReviewerModelGroup },
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

  it('draws the two rows, in the goal check\'s words one row down', async () => {
    const wrapper = await mountGroup()
    expect(wrapper.find('[data-group="plan-reviewer-model"] h3').text()).toBe('/plan 复核模型')
    expect(wrapper.text()).toContain('使用的模型')
    expect(wrapper.text()).toContain('思考强度')
    // The stored row is echoed, and its own default level is what the select
    // shows while no override is stored.
    expect((wrapper.find('[data-plan-reviewer-effort]').element as HTMLSelectElement).value).toBe('low')
    // Nothing differs from the stored values: the save bar stays collapsed
    // (its buttons are always in the DOM — the tray is what opens).
    expect(wrapper.find('.pr__tray').classes()).not.toContain('pr__tray--open')
  })

  it('choosing a model writes the row id and clears the level', async () => {
    const wrapper = await mountGroup()
    // An override first, so clearing it is what the test can see.
    await wrapper.find('[data-plan-reviewer-effort]').setValue('high')
    expect((wrapper.find('[data-plan-reviewer-effort]').element as HTMLSelectElement).value).toBe('high')

    // The clear x answers "" — the same event as choosing nothing.
    await wrapper.find('[data-goal-check-clear]').trigger('click')
    expect((wrapper.find('[data-plan-reviewer-effort]').element as HTMLSelectElement).value).toBe('')

    await wrapper.find('[data-goal-check-model]').trigger('click')
    await wrapper.find('[data-goal-check-option="llm-row-1"]').trigger('click')
    expect((wrapper.find('[data-plan-reviewer-effort]').element as HTMLSelectElement).value).toBe('low')
  })

  it('saves the row id and the level as exactly the two plan-reviewer paths', async () => {
    // Nothing configured yet, so both paths are what the save carries.
    vi.mocked(forebrainApi.settings).mockResolvedValue({
      values: { 'agents.plan_reviewer.model': '', 'agents.plan_reviewer.reasoning_effort': '' },
      secrets: {},
      meta: { approvalPresetsVisible: true, claudeCodeAvailable: false, builtinHandoffPrompt: 'builtin prompt' },
    })
    const wrapper = await mountGroup()
    // The menu opens on mount (the prototype's state), so the row is one click.
    await wrapper.find('[data-goal-check-option="llm-row-1"]').trigger('click')
    await wrapper.find('[data-plan-reviewer-effort]').setValue('high')
    await wrapper.find('[data-plan-reviewer-save]').trigger('click')
    await flushPromises()
    expect(forebrainApi.saveSettings).toHaveBeenCalledWith(
      {
        'agents.plan_reviewer.model': 'llm-row-1',
        'agents.plan_reviewer.reasoning_effort': 'high',
      },
      {},
    )
    expect(wrapper.find('[data-plan-reviewer-saved]').exists()).toBe(true)
  })

  it('keeps the draft and shows a refusal in the gateway\'s own words', async () => {
    vi.mocked(forebrainApi.saveSettings).mockRejectedValue({
      response: { status: 400, data: { path: 'agents.plan_reviewer.model', message: '这个设置项要选一个已配置的模型' } },
    })
    const wrapper = await mountGroup()
    await wrapper.find('[data-plan-reviewer-effort]').setValue('high')
    await wrapper.find('[data-plan-reviewer-save]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[role="alert"]').text()).toBe('这个设置项要选一个已配置的模型')
    expect((wrapper.find('[data-plan-reviewer-effort]').element as HTMLSelectElement).value).toBe('high')
    expect(wrapper.find('.pr__tray').classes()).toContain('pr__tray--open')
  })

  it('restores the row\'s own default level with 「恢复为模型默认」', async () => {
    const wrapper = await mountGroup()
    await wrapper.find('[data-plan-reviewer-effort]').setValue('high')
    await wrapper.find('[data-plan-reviewer-reset]').trigger('click')
    expect((wrapper.find('[data-plan-reviewer-effort]').element as HTMLSelectElement).value).toBe('low')
    // Back at the stored value: nothing left to save.
    expect(wrapper.find('.pr__tray').classes()).not.toContain('pr__tray--open')
  })

  /** One save's effort select, as the values a reader can pick. */
  function effortValues(wrapper: Awaited<ReturnType<typeof mountGroup>>): string[] {
    return wrapper
      .find('[data-plan-reviewer-effort]')
      .findAll('option')
      .map((option) => (option.element as HTMLOptionElement).value)
  }

  // The levels are the chosen row's own answer — the API's list, in the API's
  // order — and the empty value is the row's default, nothing else (plan 319).
  it('offers the levels the chosen row answers, Off only where it lists it', async () => {
    const wrapper = await mountGroup()
    // The first row has two levels and no off switch: the follow-row value plus
    // exactly those two, and no invented "none".
    expect(effortValues(wrapper)).toEqual(['', 'low', 'high'])

    // The menu opens on mount (the prototype's state), so the row is one click.
    await wrapper.find('[data-goal-check-option="llm-row-off"]').trigger('click')
    // This row does list `none`: it is offered as one of its levels.
    expect(effortValues(wrapper)).toEqual(['', 'none', 'high', 'xhigh'])

    await wrapper.find('[data-goal-check-model]').trigger('click')
    await wrapper.find('[data-goal-check-option="llm-row-plain"]').trigger('click')
    // A row with no levels answers an empty list: the follow-row value alone.
    expect(effortValues(wrapper)).toEqual([''])
  })

  // A level is the model row's, so the control waits for one and says which
  // setting comes first rather than offering levels nothing can back.
  it('waits for a model, and says so', async () => {
    vi.mocked(forebrainApi.settings).mockResolvedValue({
      values: { 'agents.plan_reviewer.model': '', 'agents.plan_reviewer.reasoning_effort': '' },
      secrets: {},
      meta: { approvalPresetsVisible: true, claudeCodeAvailable: false, builtinHandoffPrompt: 'builtin prompt' },
    })
    const wrapper = await mountGroup()
    expect((wrapper.find('[data-plan-reviewer-effort]').element as HTMLSelectElement).disabled).toBe(true)
    expect(wrapper.find('[data-plan-reviewer-hint]').text()).toBe('先指定模型再选思考强度')
  })
})
