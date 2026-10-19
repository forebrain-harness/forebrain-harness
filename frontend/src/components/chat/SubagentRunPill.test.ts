import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import type { SubagentTranscript } from '@/composables/useChatStream'
import tip from '@/directives/tip'
import { setLocale } from '@/locales'
import SubagentRunPill from './SubagentRunPill.vue'

/**
 * The pill in a subagent's composer bar: its context ring with the percentage
 * always written, the model it was dispatched on, the effort it was given, and
 * — opened — the whole reading in a card with no control in it.
 */
describe('SubagentRunPill', () => {
  beforeAll(() => setLocale('en'))
  afterEach(() => {
    setLocale('en')
    vi.restoreAllMocks()
  })

  function record(overrides: Partial<SubagentTranscript> = {}): SubagentTranscript {
    return {
      agentId: 'task-1',
      agentType: 'plan-reviewer',
      task: 'review the plan',
      status: 'running',
      blocks: [],
      inputTokens: 0,
      outputTokens: 0,
      updatedSeq: 1,
      ...overrides,
    }
  }

  const dispatched = record({
    modelProvider: 'zhipuai',
    model: 'glm-5.3-flash',
    reasoningEffort: 'high',
    inputTokens: 12_400,
    outputTokens: 900,
    tokenBudget: { percentLeft: 35, contextWindow: 1_000_000, tokenUsage: 650_000 },
  })

  function mountPill(props: Record<string, unknown> = {}) {
    return mount(SubagentRunPill, { props: { record: dispatched, ...props }, global: { directives: { tip } } })
  }

  it('writes the ring, its percentage, the model and the effort in one pill', () => {
    const wrapper = mountPill()
    const pill = wrapper.get('[data-testid="subagent-run-pill"]')
    expect(wrapper.get('[data-testid="subagent-budget"]').text()).toBe('65%')
    expect(pill.text()).toContain('glm-5.3-flash')
    expect(pill.text()).toContain('High')
    expect(pill.attributes('aria-expanded')).toBe('false')
  })

  it('opens the whole reading above the pill', async () => {
    const wrapper = mountPill()
    await wrapper.get('[data-testid="subagent-run-pill"]').trigger('click')

    const card = wrapper.get('[data-testid="subagent-run-card"]')
    expect(card.attributes('role')).toBe('dialog')
    expect(card.text()).toContain('65%')
    expect(card.text()).toContain('650k / 1M')
    expect(card.text()).toContain('zhipuai/glm-5.3-flash')
    expect(card.text()).toContain('High')
    expect(wrapper.get('[data-testid="subagent-context-window"]').text()).toBe('1M')
    expect(card.text()).toContain('12.4k in · 900 out')
    expect(card.text()).toContain('Model and reasoning are fixed at dispatch')
    // Read-only: the card offers nothing to press.
    expect(card.findAll('button, a')).toHaveLength(0)
  })

  it('warns once the window is nearly full, and says how to compact it', async () => {
    const wrapper = mountPill({
      record: record({
        ...dispatched,
        tokenBudget: { percentLeft: 16, contextWindow: 1_000_000, tokenUsage: 840_000 },
      }),
    })
    expect(wrapper.get('[data-testid="subagent-budget"]').text()).toBe('84%')
    expect(wrapper.get('[data-testid="subagent-budget"]').classes()).toContain('subagent-run-pc')
    expect(wrapper.get('.subagent-run-pill').classes()).toContain('is-warn')

    await wrapper.get('[data-testid="subagent-run-pill"]').trigger('click')
    expect(wrapper.get('[data-testid="subagent-run-near-full"]').text()).toContain('/compact')
  })

  it('says the model alone when no budget ever arrived', () => {
    const wrapper = mountPill({
      record: record({ model: 'glm-5.3-flash', modelProvider: 'zhipuai' }),
    })
    expect(wrapper.find('[data-testid="subagent-budget"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="subagent-run-pill"]').text()).toContain('glm-5.3-flash')
  })

  it('draws nothing when there is neither a budget nor a model', () => {
    const wrapper = mountPill({ record: record() })
    expect(wrapper.find('[data-testid="subagent-run-pill"]').exists()).toBe(false)
    expect(wrapper.text()).toBe('')
  })

  it('closes the card on Escape and on a click outside it', async () => {
    const wrapper = mountPill()
    await wrapper.get('[data-testid="subagent-run-pill"]').trigger('click')
    expect(wrapper.find('[data-testid="subagent-run-card"]').exists()).toBe(true)

    const escape = new KeyboardEvent('keydown', { key: 'Escape', cancelable: true })
    document.dispatchEvent(escape)
    await wrapper.vm.$nextTick()
    expect(escape.defaultPrevented).toBe(true)
    expect(wrapper.find('[data-testid="subagent-run-card"]').exists()).toBe(false)

    await wrapper.get('[data-testid="subagent-run-pill"]').trigger('click')
    document.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }))
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[data-testid="subagent-run-card"]').exists()).toBe(false)
  })

  it('leaves out the effort when the spawn reported none', () => {
    const wrapper = mountPill({ record: record({ model: 'glm-5.3-flash', reasoningEffort: '' }) })
    expect(wrapper.get('[data-testid="subagent-run-pill"]').text()).not.toContain('·')
    expect(wrapper.get('[data-testid="subagent-run-pill"]').text()).not.toContain('Default')
  })
})
