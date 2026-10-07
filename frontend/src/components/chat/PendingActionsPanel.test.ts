import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import { forebrainApi, type ActionRecord, type SessionApprovalRequest } from '@/lib/api'
import { MessageResponse } from '@repo/elements/message'
import { setLocale } from '@/locales'
import PendingActionsPanel from './PendingActionsPanel.vue'

function action(id: string, kind: string, payload: Record<string, unknown>, extra: Partial<ActionRecord> = {}): ActionRecord {
  return { id, kind, status: 'pending', payloadJson: JSON.stringify(payload), createdAt: 1, updatedAt: 1, ...extra }
}

describe('PendingActionsPanel', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))
  afterEach(() => vi.restoreAllMocks())

  it('decides a parked approval in place and reads the set again', async () => {
    const list = vi.spyOn(forebrainApi, 'actionsList')
      .mockResolvedValueOnce([action('a1', 'write_file', { justification: 'write the skill file' })])
      .mockResolvedValueOnce([])
    const approve = vi.spyOn(forebrainApi, 'actionsApprove').mockResolvedValue({} as never)
    const wrapper = mount(PendingActionsPanel, { props: { sessionId: 's1', version: 0 } })
    await flushPromises()

    expect(list).toHaveBeenCalledWith('pending', 's1')
    expect(wrapper.find('[data-testid="pending-approval"]').text()).toContain('write the skill file')
    await wrapper.find('[data-testid="pending-approve"]').trigger('click')
    await flushPromises()

    expect(approve).toHaveBeenCalledWith('a1', {})
    expect(list).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-testid="pending-actions"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('answers a question with the options picked', async () => {
    vi.spyOn(forebrainApi, 'actionsList')
      .mockResolvedValueOnce([action('q1', 'user_interaction', {
        questions: [{ id: 'scope', prompt: 'Which layer?', options: [{ id: 'agent', label: 'Agent' }, { id: 'shared', label: 'Shared' }] }],
      })])
      .mockResolvedValue([])
    const answer = vi.spyOn(forebrainApi, 'actionsAnswer').mockResolvedValue({} as never)
    const wrapper = mount(PendingActionsPanel, { props: { sessionId: 's1', version: 0 } })
    await flushPromises()

    await wrapper.find('input[value="shared"]').setValue(true)
    await wrapper.find('[data-testid="pending-question"] button').trigger('click')
    await flushPromises()

    expect(answer).toHaveBeenCalledWith('q1', { answers: [{ questionId: 'scope', optionIds: ['shared'] }] })
    wrapper.unmount()
  })

  it('reads the set again when the stream says it changed, and forgets it with the session', async () => {
    const list = vi.spyOn(forebrainApi, 'actionsList')
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([action('a2', 'shell', { justification: 'run the tests' })])
      .mockResolvedValueOnce([])
    const wrapper = mount(PendingActionsPanel, { props: { sessionId: 's1', version: 0 } })
    await flushPromises()
    expect(wrapper.find('[data-testid="pending-approval"]').exists()).toBe(false)

    await wrapper.setProps({ version: 1 })
    await flushPromises()
    expect(wrapper.find('[data-testid="pending-approval"]').text()).toContain('run the tests')

    await wrapper.setProps({ sessionId: 's2' })
    await flushPromises()
    expect(list).toHaveBeenLastCalledWith('pending', 's2')
    expect(wrapper.find('[data-testid="pending-approval"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('offers only asks whose payload is the questions the panel draws', async () => {
    vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue([
      action('bad-1', 'user_interaction', {}),
      action('bad-2', 'user_interaction', { questions: 'not a list' }),
      action('bad-3', 'user_interaction', { questions: [{ id: 'scope', prompt: 'Which layer?', options: 'not a list' }] }),
      action('bad-4', 'user_interaction', { questions: [{ id: 'scope', prompt: 'Which layer?' }] }),
      action('good-1', 'user_interaction', {
        questions: [{ id: 'scope', prompt: 'Which layer?', options: [{ id: 'agent', label: 'Agent' }] }],
      }),
    ])
    const wrapper = mount(PendingActionsPanel, { props: { sessionId: 's1', version: 0 } })
    await flushPromises()

    const asks = wrapper.findAll('[data-testid="pending-question"]')
    expect(asks).toHaveLength(1)
    expect(asks[0]?.attributes('data-action-id')).toBe('good-1')
    expect(asks[0]?.text()).toContain('Which layer?')
    expect(asks[0]?.text()).toContain('Agent')
    wrapper.unmount()
  })

  it('names a subagent requester as text where its view cannot be opened', async () => {
    vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue([
      action('a3', 'shell', { justification: 'probe' }, { agentId: 'child-1', subagentType: 'explorer' }),
    ])
    const wrapper = mount(PendingActionsPanel, { props: { sessionId: 's1', version: 0 } })
    await flushPromises()
    const row = wrapper.find('[data-testid="pending-approval"]')
    expect(row.text()).toContain('explorer')
    expect(row.findAll('button').some((b) => b.text().includes('explorer'))).toBe(false)

    await wrapper.setProps({ canOpenAgent: true })
    const requester = wrapper.findAll('[data-testid="pending-approval"] button').find((b) => b.text().includes('explorer'))
    expect(requester).toBeDefined()
    await requester!.trigger('click')
    expect(wrapper.emitted('open-agent')).toEqual([['child-1']])
    wrapper.unmount()
  })

  it('draws the exit-plan card with the plan, the four choices, and the review path', async () => {
    vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue([
      action('a-exit', 'exit_plan_mode', { session_id: 's1' }),
    ])
    vi.spyOn(forebrainApi, 'sessionApprovalRequest').mockResolvedValue({
      actionId: 'a-exit',
      kind: 'exit_plan_mode',
      planText: '# Plan\n\n1. Ship it.',
      planReviewModels: [
        { provider: 'openai', model: 'gpt-5.1', current: true },
        { provider: 'zhipuai', model: 'glm-5.3-flash' },
      ],
      planReviews: [],
      planReviewActive: null,
    } as SessionApprovalRequest)
    const approve = vi.spyOn(forebrainApi, 'actionsApprove').mockResolvedValue({} as never)
    const review = vi.spyOn(forebrainApi, 'actionPlanReview').mockResolvedValue(undefined as never)
    const wrapper = mount(PendingActionsPanel, { props: { sessionId: 's1', version: 0 } })
    await flushPromises()

    const card = wrapper.find('[data-testid="exit-plan-approval"]')
    expect(card.exists()).toBe(true)
    expect(card.text()).toContain('Exit plan mode')
    expect(card.text()).not.toContain('a-exit')
    // The plan body travels through the same markdown component the
    // conversation renders with; its on-screen text is proven by the e2e.
    const planBox = card.find('[data-testid="exit-plan-text"]')
    expect(planBox.exists()).toBe(true)
    expect(planBox.findComponent(MessageResponse).props('content')).toContain('1. Ship it.')
    // The four choices, in the overlay's order.
    const choice = (label: string) => card.findAll('button').find((b) => b.text().includes(label))
    expect(choice('Approve and clear context')).toBeDefined()
    expect(choice('Keep planning')).toBeDefined()

    await choice('Approve and clear context')!.trigger('click')
    await flushPromises()
    expect(approve).toHaveBeenCalledWith('a-exit', { clearContext: true })

    // The review path opens the model list; the current model is marked.
    await choice('Ask another model to review')!.trigger('click')
    await flushPromises()
    const modelRow = card.findAll('[data-testid="plan-review-model"]').find((b) => b.text().includes('gpt-5.1'))
    expect(modelRow).toBeDefined()
    expect(modelRow!.text()).toContain('current')
    await modelRow!.trigger('click')
    await flushPromises()
    expect(review).toHaveBeenCalledWith('a-exit', { provider: 'openai', model: 'gpt-5.1' })
    wrapper.unmount()
  })
})
