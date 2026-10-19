import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import type { SubagentTranscript } from '@/composables/useChatStream'
import RunWorkedLine from './RunWorkedLine.vue'
import SubagentConversation from './SubagentConversation.vue'
import SubagentCallCard from './SubagentCallCard.vue'

/**
 * A subagent's tab draws the transcript and closes an ended run with the
 * conversation's own worked line. Everything about the execution itself — its
 * clock, its checklist, its model and its context budget — belongs to the
 * composer bar of the tab, never to this content.
 */
describe('SubagentConversation', () => {
  beforeAll(() => setLocale('en'))
  afterEach(() => {
    setLocale('en')
    vi.restoreAllMocks()
  })

  function record(overrides: Partial<SubagentTranscript> = {}): SubagentTranscript {
    return {
      agentId: 'task-r',
      agentType: 'plan-reviewer',
      task: 'review the plan',
      status: 'running',
      blocks: [{ kind: 'prompt', text: 'review the plan' }],
      inputTokens: 0,
      outputTokens: 0,
      startedAt: new Date().toISOString(),
      updatedSeq: 1,
      ...overrides,
    }
  }

  it('closes an ended run with the conversation’s worked line', () => {
    const wrapper = mount(SubagentConversation, {
      props: {
        record: record({
          status: 'done',
          plan: { done: 2, total: 5, active: 'Fix the reducer' },
          blocks: [
            { kind: 'prompt', text: 'review the plan' },
            { kind: 'worked', id: 'worked:exec-1', durationMs: 60_000, finishedAt: new Date().toISOString(), plan: { done: 2, total: 5, active: 'Fix the reducer' } },
          ],
        }),
      },
    })
    const worked = wrapper.getComponent(RunWorkedLine)
    expect(worked.text()).toContain('2/5')
    expect(worked.text()).toContain('Fix the reducer')
    expect(worked.text()).toContain('Worked for 1m 00s')
  })

  it('opens the task it was handed as a collapsed card and reports the fold', async () => {
    const long = 'review the plan '.repeat(40)
    const wrapper = mount(SubagentConversation, {
      props: { record: record({ blocks: [{ kind: 'prompt', text: long }] }) },
    })
    const card = wrapper.get('[data-testid="subagent-task-card"]')
    expect(card.text()).toContain('Its task')
    expect(card.classes()).not.toContain('is-open')
    const toggle = wrapper.get('[data-testid="subagent-task-toggle"]')
    expect(toggle.attributes('aria-label')).toBe('Show the whole task')
    expect(toggle.attributes('aria-expanded')).toBe('false')

    await toggle.trigger('click')
    expect(wrapper.emitted('fold-change')).toEqual([[{ id: 'prompt:0', open: true }]])

    // The saved fold state is what reopens it, so the reading survives a reload.
    await wrapper.setProps({ foldState: { 'prompt:0': true } })
    expect(wrapper.get('[data-testid="subagent-task-card"]').classes()).toContain('is-open')
    expect(wrapper.get('[data-testid="subagent-task-toggle"]').attributes('aria-label')).toBe('Collapse the task')

    await wrapper.get('[data-testid="subagent-task-toggle"]').trigger('click')
    expect(wrapper.emitted('fold-change')?.slice(-1)).toEqual([[{ id: 'prompt:0', open: false }]])
  })

  it('leaves the working line, the budget and the header to the tab that holds it', () => {
    const wrapper = mount(SubagentConversation, {
      props: {
        record: record({
          agentType: 'plan-reviewer',
          modelProvider: 'zhipuai',
          model: 'glm-5.3-flash',
          tokenBudget: { percentLeft: 35, contextWindow: 1_000_000 },
          plan: { done: 1, total: 3 },
        }),
      },
    })
    expect(wrapper.text()).not.toContain('Working (')
    expect(wrapper.find('[data-testid="subagent-budget"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('zhipuai/glm-5.3-flash')
    // No header and no way back out: the drawer owns both.
    expect(wrapper.find('header').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('Back')
  })

  it('says it has no answer yet once an ended run produced none', () => {
    const running = mount(SubagentConversation, { props: { record: record() } })
    expect(running.text()).not.toContain('No answer yet')

    const ended = mount(SubagentConversation, { props: { record: record({ status: 'cancelled' }) } })
    expect(ended.text()).toContain('No answer yet')
  })

  it("draws a message the user sent it as the user's own message", () => {
    const wrapper = mount(SubagentConversation, {
      props: {
        record: record({
          blocks: [
            { kind: 'prompt', text: 'answer briefly' },
            { kind: 'user', id: 'u1', text: 'continue' },
          ],
        }),
      },
    })
    const user = wrapper.get('[data-testid="subagent-user-message"]')
    expect(user.text()).toContain('continue')
  })

  it('marks the nested dispatch the drawer has open', () => {
    const wrapper = mount(SubagentConversation, {
      props: {
        record: record({
          blocks: [
            { kind: 'prompt', text: 'split the work' },
            {
              kind: 'subagent',
              id: 'call-1',
              stepId: 'call-1',
              step: {
                stepId: 'call-1',
                toolName: 'subagent_run',
                summary: 'run 1 task',
                status: 'running',
                subagentCall: { verb: 'run', tasks: [{ index: 0, key: 'subagent-9a1f', title: 'look at the middleware', agentType: 'explore', status: 'running' }] },
              },
            },
          ],
        }),
        activeAgentId: 'subagent-9a1f',
      },
    })
    const rows = wrapper.findAllComponents(SubagentCallCard).flatMap((card) => card.findAll('[data-testid="subagent-task-row"]'))
    expect(rows).toHaveLength(1)
    expect(rows[0].attributes('aria-current')).toBe('true')
  })
})
