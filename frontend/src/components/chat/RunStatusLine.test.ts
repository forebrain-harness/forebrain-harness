import { beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import PlanProgressSegments from './PlanProgressSegments.vue'
import RunStatusLine from './RunStatusLine.vue'

/**
 * The line above a running composer reads in the terminal's order — the state,
 * its checklist, then whatever else the run measured — and joins its segments
 * with the same grey dot the terminal uses.
 */
describe('RunStatusLine', () => {
  beforeAll(() => setLocale('en'))

  it('draws the state, the checklist and the tail in that order', () => {
    const wrapper = mount(RunStatusLine, {
      props: {
        label: 'Working (1m 12s • esc to interrupt)',
        plan: { done: 3, total: 5, active: 'fix the middleware' },
        tail: 'first token 1.2s',
      },
    })
    const segments = wrapper.findAll('.run-status-seg').map((seg) => seg.text())
    expect(segments[0]).toBe('Working (1m 12s • esc to interrupt)')
    expect(segments[1]).toContain('3/5')
    expect(segments[1]).toContain('fix the middleware')
    expect(segments[2]).toBe('first token 1.2s')
    expect(wrapper.findAll('.run-status-dot').map((dot) => dot.text())).toEqual(['·', '·'])
    expect(wrapper.attributes('role')).toBe('status')
  })

  it('draws the state alone when the run has no checklist and no tail', () => {
    const wrapper = mount(RunStatusLine, { props: { label: 'Working (3s • esc to interrupt)' } })
    expect(wrapper.findAll('.run-status-dot')).toHaveLength(0)
    expect(wrapper.findAll('.run-status-tail')).toHaveLength(0)
    expect(wrapper.findComponent(PlanProgressSegments).exists()).toBe(false)
    expect(wrapper.get('[role="status"]').text()).toBe('Working (3s • esc to interrupt)')
  })

  it('keeps one dot between the checklist and the tail when there is no plan', () => {
    const wrapper = mount(RunStatusLine, { props: { label: 'Working (3s)', tail: 'first token 0.9s' } })
    expect(wrapper.findAll('.run-status-dot')).toHaveLength(1)
    expect(wrapper.findAll('.run-status-seg').map((seg) => seg.text())).toEqual(['Working (3s)', 'first token 0.9s'])
  })
})
