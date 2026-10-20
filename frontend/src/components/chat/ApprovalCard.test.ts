import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import { forebrainApi, type ActionRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import ApprovalCard from './ApprovalCard.vue'

/**
 * An approval card replays the one line the surface printed for the decision:
 * a card with a line shows the line and never the stored reason; a card
 * without one prints nothing.
 *
 * The requests it hosts in the conversation's flow are the run's own questions.
 * A fetch gate is the question the terminal asks - which page, and what to do
 * about it - so it says the page and the three ways out, never the tool's name
 * or the action's id.
 */
describe('ApprovalCard', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))
  afterEach(() => vi.restoreAllMocks())

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

  it('asks about the page a fetch needs, and offers the three answers', async () => {
    vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue([
      {
        id: 'act-fetch',
        kind: 'web_fetch',
        status: 'pending',
        payloadJson: JSON.stringify({ url: 'https://example.com/pricing' }),
        createdAt: 1,
        updatedAt: 1,
        permissionSuggestion: {
          permissionToolName: 'WebFetch',
          permissionInput: 'https://example.com/pricing',
          prefixRuleContent: 'domain:example.com',
          availableDecisions: ['accept', 'accept_and_remember', 'cancel'],
        },
      } as ActionRecord,
    ])
    const wrapper = mount(ApprovalCard, { props: { sessionId: 's1', version: 0 } })
    await flushPromises()

    const gate = wrapper.find('[data-testid="web-fetch-approval"]')
    expect(gate.exists()).toBe(true)
    expect(gate.text()).toContain('Fetch this page from the public web?')
    expect(wrapper.find('[data-testid="web-fetch-page"]').text()).toBe('https://example.com/pricing')
    // Every answer says what it does, and the one that remembers names its scope.
    expect(gate.findAll('button').map((b) => b.text())).toEqual([
      'Fetch the page',
      "Don't ask again for example.com",
      "Don't fetch",
    ])
    // Nothing on the card is the tool's own name or an action id, and a card
    // whose button states the scope does not explain it again in a box.
    expect(gate.text()).not.toContain('web_fetch')
    expect(gate.text()).not.toContain('act-fetch')
    expect(gate.text()).not.toContain('Permission memory')
    wrapper.unmount()
  })
})
