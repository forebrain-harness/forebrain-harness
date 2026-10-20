import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { config, mount } from '@vue/test-utils'

import tip from '@/directives/tip'
import { setLocale } from '@/locales'
import type { PlanTab } from '@/composables/useSessionPlans'
import type { SubagentTranscript } from '@/composables/useChatStream'
import SubagentDrawer from './SubagentDrawer.vue'

config.global.directives = { tip }

/**
 * The drawer is a shell: tabs in the order they were opened, the "+" that lists
 * this conversation's subagents, and the way back out. It never decides what a
 * tab shows.
 */
describe('SubagentDrawer', () => {
  beforeAll(() => setLocale('en'))
  afterEach(() => {
    vi.restoreAllMocks()
    setLocale('en')
  })

  function record(agentId: string, overrides: Partial<SubagentTranscript> = {}): SubagentTranscript {
    return {
      agentId,
      agentType: 'general-purpose',
      title: `task ${agentId}`,
      task: 'do the thing',
      status: 'done',
      blocks: [],
      inputTokens: 0,
      outputTokens: 0,
      updatedSeq: 1,
      ...overrides,
    }
  }

  const tabs = [record('task-1'), record('task-2')]
  const records = [...tabs, record('task-3')]

  function mountDrawer(props: Record<string, unknown> = {}) {
    return mount(SubagentDrawer, {
      props: { tabs, active: 'task-1', records, seen: {}, overlay: false, ...props },
      slots: { default: '<div data-testid="tab-body">the tab</div>' },
    })
  }

  it('lists the open tabs in the order they were opened, one selected', () => {
    const wrapper = mountDrawer()
    const tabEls = wrapper.findAll('[role="tab"]')
    expect(tabEls.map((tab) => tab.text())).toEqual(['general-purpose · task task-1', 'general-purpose · task task-2'])
    expect(tabEls.map((tab) => tab.attributes('aria-selected'))).toEqual(['true', 'false'])
    expect(wrapper.get('[data-testid="tab-body"]').text()).toBe('the tab')
  })

  it('marks a tab that produced something since the user last read it', async () => {
    const wrapper = mountDrawer({ seen: { 'task-1': 1, 'task-2': 1 } })
    expect(wrapper.find('[aria-label="New activity"]').exists()).toBe(false)

    await wrapper.setProps({ records: [tabs[0], { ...tabs[1], updatedSeq: 4 }], tabs: [tabs[0], { ...tabs[1], updatedSeq: 4 }] })
    expect(wrapper.findAll('[aria-label="New activity"]')).toHaveLength(1)
    // The tab in front never carries the dot: its reader is already there.
    expect(wrapper.get('[data-testid="subagent-tab-task-1"]').find('[aria-label="New activity"]').exists()).toBe(false)
  })

  it('closes a tab and collapses the drawer when asked', async () => {
    const wrapper = mountDrawer()
    await wrapper.get('[data-testid="subagent-tab-close-task-2"]').trigger('click')
    expect(wrapper.emitted('close-tab')).toEqual([['task-2']])
    expect(wrapper.get('[data-testid="subagent-tab-close-task-2"]').attributes('aria-label')).toBe('Close general-purpose · task task-2')

    await wrapper.get('[data-testid="subagent-drawer-collapse"]').trigger('click')
    expect(wrapper.emitted('collapse')).toHaveLength(1)
  })

  it('lists every subagent of the conversation in the "+" menu', async () => {
    const wrapper = mountDrawer({ active: 'task-1', records: [...records, record('task-3')] })
    expect(wrapper.find('[data-testid="subagent-drawer-menu"]').exists()).toBe(false)

    await wrapper.get('[data-testid="subagent-drawer-add"]').trigger('click')
    const menu = wrapper.get('[data-testid="subagent-drawer-menu"]')
    expect(menu.text()).toContain('Subagents in this conversation · 4')
    const rows = menu.findAll('[role="menuitem"]')
    expect(rows).toHaveLength(4)
    // Already open, or how it ended.
    expect(rows[0].text()).toContain('Open')
    expect(rows[2].text()).toContain('done')

    await rows[1].trigger('click')
    expect(wrapper.emitted('open')).toEqual([['task-2']])
    expect(wrapper.find('[data-testid="subagent-drawer-menu"]').exists()).toBe(false)
  })

  it('says how long an open subagent has been running in the menu', async () => {
    const running = record('task-3', { status: 'running', startedAt: new Date(60_000).toISOString() })
    const wrapper = mountDrawer({ records: [...records.slice(0, 2), running], now: 120_000 })
    await wrapper.get('[data-testid="subagent-drawer-add"]').trigger('click')
    expect(wrapper.get('[data-testid="subagent-menu-task-3"]').text()).toContain('1m 00s')
  })

  it('spends Escape on the menu before the drawer', async () => {
    const wrapper = mountDrawer()
    await wrapper.get('[data-testid="subagent-drawer-add"]').trigger('click')

    const first = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    wrapper.get('[data-testid="subagent-drawer"]').element.dispatchEvent(first)
    await wrapper.vm.$nextTick()
    expect(first.defaultPrevented).toBe(true)
    expect(wrapper.find('[data-testid="subagent-drawer-menu"]').exists()).toBe(false)
    expect(wrapper.emitted('escape-key')).toBeUndefined()

    const second = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    wrapper.get('[data-testid="subagent-drawer"]').element.dispatchEvent(second)
    await wrapper.vm.$nextTick()
    expect(second.defaultPrevented).toBe(true)
    expect(wrapper.emitted('escape-key')).toHaveLength(1)
  })

  it('carries the overlay treatment on a narrow screen', () => {
    expect(mountDrawer().get('[data-testid="subagent-drawer"]').classes()).not.toContain('subagent-drawer--overlay')
    expect(mountDrawer({ overlay: true }).get('[data-testid="subagent-drawer"]').classes()).toContain('subagent-drawer--overlay')
  })

  // A plan document opened from the workbench is a tab in this same drawer, so
  // a reader keeps one place for everything the conversation opened. It draws
  // after the subagents and carries no unread dot: a document does not say
  // anything new.
  function planTab(id: string, label: string): PlanTab {
    return { id, label, path: `plans/proj/s1/${id.replace('plan:', '')}`, at: '10:31', done: 1, total: 3, markdown: '# Plan' }
  }

  it('draws the open plan documents after the subagent tabs, without an unread dot', () => {
    const wrapper = mountDrawer({ planTabs: [planTab('plan:a.md', 'Plan · Add auth'), planTab('plan:b.md', 'Plan · Add logs')] })
    const tabEls = wrapper.findAll('[role="tab"]')
    expect(tabEls.map((tab) => tab.text())).toEqual([
      'general-purpose · task task-1',
      'general-purpose · task task-2',
      'Plan · Add auth',
      'Plan · Add logs',
    ])
    // None of them is in front: a subagent tab is.
    expect(tabEls.map((tab) => tab.attributes('aria-selected'))).toEqual(['true', 'false', 'false', 'false'])
    expect(wrapper.get('[data-testid="plan-tab-plan:a.md"]').find('[aria-label="New activity"]').exists()).toBe(false)
  })

  it('selects and closes a plan tab the way a subagent tab is selected and closed', async () => {
    const wrapper = mountDrawer({ planTabs: [planTab('plan:a.md', 'Plan · Add auth')], activePlan: 'plan:a.md' })
    const planTabEl = wrapper.get('[data-testid="plan-tab-plan:a.md"]')
    // In front: it reads as selected and carries the open treatment.
    expect(planTabEl.get('[role="tab"]').attributes('aria-selected')).toBe('true')
    expect(planTabEl.classes()).toContain('is-on')
    // The subagent tab lost the front, and the drawer still lists it.
    expect(wrapper.get('[data-testid="subagent-tab-task-1"]').get('[role="tab"]').attributes('aria-selected')).toBe('false')

    await planTabEl.get('[role="tab"]').trigger('click')
    expect(wrapper.emitted('select-plan')).toEqual([['plan:a.md']])

    await wrapper.get('[data-testid="plan-tab-close-plan:a.md"]').trigger('click')
    expect(wrapper.emitted('close-plan')).toEqual([['plan:a.md']])
    expect(wrapper.get('[data-testid="plan-tab-close-plan:a.md"]').attributes('aria-label')).toBe('Close Plan · Add auth')
    // Closing the plan tab asks for nothing from the subagent tabs.
    expect(wrapper.emitted('close-tab')).toBeUndefined()
  })
})
