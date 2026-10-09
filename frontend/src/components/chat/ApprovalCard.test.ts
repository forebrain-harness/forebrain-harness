import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import ApprovalCard from './ApprovalCard.vue'

/**
 * An approval card replays the one line the surface printed for the decision:
 * a card with a line shows the line and never the stored reason; a card
 * without one prints nothing.
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

  it('replays the line the surface printed for the decision', () => {
    const got = card({
      actionId: 'act-1',
      actionKind: 'exit_plan_mode',
      status: 'denied',
      confirmation: "✗ You canceled forebrain's request to exit plan mode",
      message: 'canceled by user',
    })
    expect(got.text).toBe("✗ You canceled forebrain's request to exit plan mode")
    expect(got.text).not.toContain('canceled by user')
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
