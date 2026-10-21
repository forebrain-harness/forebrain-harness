import { beforeAll, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import ThinkingCard from './ThinkingCard.vue'

/**
 * The thinking card is the prototype's .think: one caption line that folds the
 * prose away, closed by default on every surface that draws it — the
 * conversation, a subagent's tab and the workshop all mount this component,
 * and its fold, state and content come from the library's Reasoning card.
 */
describe('ThinkingCard', () => {
  beforeAll(() => setLocale('en'))

  const block = {
    kind: 'thinking' as const,
    text: '  the reducer should own the timeline\nand the view should read it  ',
    open: false,
    startedAt: 0,
    durationMs: 4200,
  }

  it('folds the prose behind a caption and starts closed', () => {
    const wrapper = mount(ThinkingCard, { props: { block } })
    const trigger = wrapper.get('[data-slot="collapsible-trigger"]')
    expect(trigger.text()).toBe('Thought for 4s')
    expect(trigger.attributes('aria-expanded')).toBe('false')
    expect(wrapper.get('[data-slot="collapsible-content"]').attributes('hidden')).toBeDefined()
  })

  it('names a block whose duration was never measured neutrally', () => {
    const wrapper = mount(ThinkingCard, { props: { block: { ...block, durationMs: undefined } } })
    expect(wrapper.get('[data-slot="collapsible-trigger"]').text()).toBe('Thinking')
  })

  it('opens on the trigger and shows the reasoning', async () => {
    const wrapper = mount(ThinkingCard, { props: { block } })
    await wrapper.get('[data-slot="collapsible-trigger"]').trigger('click')
    expect(wrapper.get('[data-slot="collapsible-trigger"]').attributes('aria-expanded')).toBe('true')
    // The body is the library's Markdown content, which types itself out.
    await vi.waitFor(
      () => expect(wrapper.text()).toContain('the reducer should own the timeline'),
      { timeout: 2000 },
    )
    expect(wrapper.text()).toContain('and the view should read it')
  })
})
