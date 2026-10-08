import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import ApprovalCard from './ApprovalCard.vue'

/**
 * A delivered plan review closes the approval as a denial whose stored reason
 * is plumbing, not words: the card replays the handoff line the surface
 * printed, and the marker never reaches the screen.
 */
describe('ApprovalCard', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  function card(block: {
    actionId: string
    actionKind: string
    status: string
    confirmation?: string
    message?: string
  }) {
    const wrapper = mount(ApprovalCard, { props: { block } })
    return { wrapper, text: wrapper.text() }
  }

  it('replays the delivered line for a review handed back to the planner', () => {
    const got = card({
      actionId: 'act-1',
      actionKind: 'exit_plan_mode',
      status: 'denied',
      confirmation: 'Plan review delivered — the planner is revising the plan.',
      message: 'plan-review:delivered',
    })
    expect(got.text).toBe('Plan review delivered — the planner is revising the plan.')
    expect(got.text).not.toContain('plan-review:delivered')
    expect(got.wrapper.find('[data-approval-status="denied"]').exists()).toBe(true)
  })

  it('prints nothing for a decision no surface printed a line for', () => {
    const got = card({
      actionId: 'act-2',
      actionKind: 'exit_plan_mode',
      status: 'denied',
      message: 'plan-review:delivered',
    })
    expect(got.wrapper.find('[data-approval-status]').exists()).toBe(false)
  })
})
