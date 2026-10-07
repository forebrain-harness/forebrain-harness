import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import { setLocale } from '@/locales'
import SubagentCallCard from './SubagentCallCard.vue'
import { subagentCallView, type SubagentToolStep } from '@/composables/useChatStream'

function stepOf(overrides: Partial<SubagentToolStep> = {}): SubagentToolStep {
  return {
    stepId: 'call-fanout-1',
    toolName: 'subagent_fanout',
    summary: 'run 2 tasks',
    status: 'completed',
    ...overrides,
  }
}

/**
 * The card is the conversation's whole account of a subagent_* call: one
 * sentence of a header, one row per task, nothing raw. These tests hold the
 * promise the audit made — no JSON, no task id, no status=, no tool-use
 * tally line — plus the row clock and the way into the subagent's view.
 */
describe('SubagentCallCard', () => {
  beforeAll(() => setLocale('en'))
  afterEach(() => {
    vi.useRealTimers()
    setLocale('en')
  })

  const fanoutStep = stepOf({
    subagentCall: {
      verb: 'run',
      tasks: [
        { index: 0, key: 'subagent-8c5c', title: '任务16 migrate 导入', agentType: 'general-purpose', status: 'done' },
        { index: 1, title: '任务17 扩展目录', agentType: 'general-purpose', status: 'skipped', error: 'skipped: empty prompt' },
      ],
    },
  })

  it('renders the settled card without one raw fact of the call', () => {
    const wrapper = mount(SubagentCallCard, { props: { step: fanoutStep } })
    const text = wrapper.text()
    expect(text).toContain('Ran 2 general-purpose tasks')
    expect(text).toContain('1 done, 1 skipped')
    expect(text).toContain('任务16 migrate 导入')
    expect(text).toContain('skipped: empty prompt')
    expect(text).not.toContain('{')
    expect(text).not.toContain('subagent-')
    expect(text).not.toContain('status=')
    // No line is a standalone tool-use tally.
    for (const line of text.split('\n')) {
      expect(line.trim()).not.toMatch(/^\+?\d*\s*tool uses?$/)
      expect(line.trim()).not.toMatch(/^另 \d+ 次工具调用$/)
    }
  })

  it('ticks a running row\'s clock itself when no clock is handed to it', async () => {
    vi.useFakeTimers({ now: Date.parse('2026-10-07T08:00:00.000Z') })
    const step = stepOf({
      status: 'running',
      subagentCall: {
        verb: 'run',
        tasks: [{ index: 0, key: 'subagent-8c5c', title: '任务16 migrate 导入', agentType: 'general-purpose', status: 'running', startedAt: Math.floor(Date.parse('2026-10-07T08:00:00.000Z') / 1000) - 3 }],
      },
    })
    const wrapper = mount(SubagentCallCard, { props: { step } })
    const clock = () => wrapper.get('[data-testid="subagent-row-clock"]').text()
    expect(clock()).toBe('· 3s')
    vi.advanceTimersByTime(3_000)
    await vi.advanceTimersByTimeAsync(0)
    expect(clock()).toBe('· 6s')
    wrapper.unmount()
  })

  it('stops its clock when nothing on the card runs anymore', async () => {
    vi.useFakeTimers({ now: Date.parse('2026-10-07T08:00:00.000Z') })
    const running = stepOf({
      status: 'running',
      subagentCall: {
        verb: 'run',
        tasks: [{ index: 0, key: 'subagent-8c5c', title: '任务16 migrate 导入', agentType: 'general-purpose', status: 'running', startedAt: Math.floor(Date.parse('2026-10-07T08:00:00.000Z') / 1000) - 3 }],
      },
    })
    const wrapper = mount(SubagentCallCard, { props: { step: running } })
    expect(vi.getTimerCount()).toBeGreaterThan(0)
    await wrapper.setProps({ step: fanoutStep })
    const ticksAfterSettle = vi.getTimerCount()
    // The interval is gone once the last running row settled.
    expect(ticksAfterSettle).toBe(0)
    wrapper.unmount()
  })

  it('opens the subagent view from a bound row and only from one', async () => {
    const wrapper = mount(SubagentCallCard, { props: { step: fanoutStep } })
    const rows = wrapper.findAll('[data-testid="subagent-task-row"]')
    expect(rows).toHaveLength(2)
    await rows[0].trigger('click')
    expect(wrapper.emitted('open')).toEqual([['subagent-8c5c']])
    // A skipped task has no agent to open: the row is not a button at all.
    expect(rows[1].attributes('disabled')).toBeDefined()
    await rows[1].trigger('click')
    expect(wrapper.emitted('open')).toHaveLength(1)
  })

  it('says the header in the viewer\'s language', async () => {
    const wrapper = mount(SubagentCallCard, { props: { step: fanoutStep } })
    expect(wrapper.text()).toContain('Ran 2 general-purpose tasks')
    setLocale('zh')
    await nextTick()
    await nextTick()
    expect(wrapper.text()).toContain('已运行 2 个 general-purpose 任务')
    expect(wrapper.text()).toContain('1 个完成、1 个跳过')
  })

  it('shows the shared empty line for a card with no rows', () => {
    const wrapper = mount(SubagentCallCard, {
      props: { step: stepOf({ subagentCall: { verb: 'list', tasks: [] } }) },
    })
    expect(wrapper.text()).toContain('Listed 0 tasks')
    expect(wrapper.text()).toContain('(no output)')
  })

  it('prints the call\'s own failure under the header, verbatim', () => {
    const wrapper = mount(SubagentCallCard, {
      props: {
        step: stepOf({
          status: 'failed',
          error: 'unknown public subagent_type "genral-purpose"',
          subagentCall: { verb: 'send', tasks: [{ index: 0, title: '计划001 Go车道实施', agentType: 'genral-purpose', status: 'failed' }] },
        }),
      },
    })
    expect(wrapper.text()).toContain('Failed to start 1 genral-purpose task in background')
    expect(wrapper.text()).toContain('unknown public subagent_type "genral-purpose"')
  })

  it('names the latest tool of a running dispatch and its further calls', () => {
    const runningStep = stepOf({
      status: 'running',
      subagentCall: { verb: 'run', tasks: [{ index: 0, key: 'subagent-8c5c', title: '任务16 migrate 导入', agentType: 'general-purpose', status: 'running' }] },
    })
    const live = new Map([[0, {
      agentId: 'subagent-8c5c', status: 'running', toolCount: 18,
      latestTool: 'edit pkg/turn/submit.go',
    }]])
    const view = subagentCallView(runningStep, live, Date.now())
    expect(view.rows[0].latestTool).toBe('edit pkg/turn/submit.go')
    expect(view.rows[0].moreToolUses).toBe(17)
    const wrapper = mount(SubagentCallCard, { props: { step: runningStep, live, now: Date.now() } })
    expect(wrapper.text()).toContain('latest: edit pkg/turn/submit.go · +17 tool uses')
    wrapper.unmount()
  })
})
