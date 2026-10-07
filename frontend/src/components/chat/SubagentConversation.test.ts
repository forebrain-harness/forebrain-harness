import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import { setLocale } from '@/locales'
import type { SubagentTranscript } from '@/composables/useChatStream'
import PlanProgressSegments from './PlanProgressSegments.vue'
import RunWorkedLine from './RunWorkedLine.vue'
import SubagentConversation from './SubagentConversation.vue'

/**
 * A subagent's view answers the questions the conversation's own view answers:
 * how long this execution has been running, what it is on its checklist, and
 * — when the execution ends — how long it worked, drawn by the same worked
 * line the conversation closes its runs with.
 */
describe('SubagentConversation', () => {
  beforeAll(() => setLocale('en'))
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  function runningRecord(overrides: Partial<SubagentTranscript> = {}): SubagentTranscript {
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

  it('runs a working clock of its own and closes the run with the conversation’s worked line', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(Date.parse('2026-06-14T00:00:00Z'))
    const wrapper = mount(SubagentConversation, { props: { record: runningRecord() } })
    expect(wrapper.text()).toContain('Working (0s)')

    vi.advanceTimersByTime(49_000)
    await nextTick()
    expect(wrapper.text()).toContain('Working (49s)')

    const finished = runningRecord({
      status: 'done',
      blocks: [
        { kind: 'prompt', text: 'review the plan' },
        { kind: 'worked', id: 'worked:exec-1', durationMs: 60_000, finishedAt: new Date().toISOString() },
      ],
    })
    await wrapper.setProps({ record: finished })
    expect(wrapper.text()).not.toContain('Working (')
    expect(wrapper.findComponent(RunWorkedLine).exists()).toBe(true)
    expect(wrapper.text()).toContain('Worked for 1m 00s')
  })

  it('shows checklist progress with its checkbox on both the working and worked lines', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(Date.parse('2026-06-14T00:00:00Z'))
    const plan = { done: 2, total: 5, active: 'Fix the reducer' }
    const wrapper = mount(SubagentConversation, { props: { record: runningRecord({ plan }) } })
    expect(wrapper.text()).toContain('2/5')
    expect(wrapper.text()).toContain('Fix the reducer')
    expect(wrapper.findComponent(PlanProgressSegments).exists()).toBe(true)

    const finished = runningRecord({
      status: 'done',
      plan,
      blocks: [
        { kind: 'prompt', text: 'review the plan' },
        { kind: 'worked', id: 'worked:exec-1', durationMs: 60_000, finishedAt: new Date().toISOString(), plan },
      ],
    })
    await wrapper.setProps({ record: finished })
    const worked = wrapper.getComponent(RunWorkedLine)
    expect(worked.text()).toContain('2/5')
    expect(worked.text()).toContain('Fix the reducer')
    expect(worked.text()).toContain('Worked for 1m 00s')
  })

  it('shows this subagent its own packed context window, from its own budget', () => {
    const wrapper = mount(SubagentConversation, {
      props: {
        record: runningRecord({
          agentType: 'plan-reviewer',
          modelProvider: 'zhipuai',
          model: 'glm-5.3-flash',
          tokenBudget: { percentLeft: 35, contextWindow: 1_000_000 },
        }),
      },
    })
    expect(wrapper.get('[data-testid="subagent-budget"]').text()).toBe('65%/1M')
    // The model it runs on, and the window that model has — not the
    // conversation's.
    expect(wrapper.text()).toContain('zhipuai/glm-5.3-flash')
  })

  it("draws a message the user sent it as the user's own message", () => {
    const wrapper = mount(SubagentConversation, {
      props: {
        record: runningRecord({
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
})
