import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import TodoPanel from './TodoPanel.vue'
import type { PlanUpdateData } from '@/composables/useChatStream'

/** The payload the prototype's todo scenes carry: six rows, two of them done. */
function todo(overrides: Partial<PlanUpdateData> = {}): PlanUpdateData {
  return {
    title: '更新待办',
    items: [
      { content: '读 manifest', status: 'completed' },
      { content: '改容器键', status: 'completed' },
      { content: '跑门禁', status: 'in_progress' },
      { content: '写回归', status: 'pending' },
      { content: '装机', status: 'pending' },
      { content: '旧键清理', status: 'cancelled' },
    ],
    ...overrides,
  } as PlanUpdateData
}

const base = { todo: todo(), unread: [] as number[], flash: [] as number[] }

describe('the panel todo checklist', () => {
  it('draws one row per item with the three-state checkbox and the tally', () => {
    const wrapper = mount(TodoPanel, { props: base })
    const rows = wrapper.findAll('[data-testid="todo-row"]')
    expect(rows).toHaveLength(6)
    expect(wrapper.get('[data-testid="todo-count"]').text()).toBe('2/6')
    const states = rows.map((row) => row.get('[role="checkbox"]').attributes('aria-checked'))
    expect(states).toEqual(['true', 'true', 'mixed', 'false', 'false', 'false'])
    // Cancelled reads as unchecked and struck through, not as done.
    expect(rows[5].classes()).toContain('wv-todo-row--cancel')
    expect(rows[5].get('.wv-todo-text').classes()).toContain('wv-todo-cancel')
  })

  it('keeps a changed row unread until the reader marks it, and reads on click', async () => {
    const wrapper = mount(TodoPanel, { props: { ...base, unread: [2, 3] } })
    const rows = wrapper.findAll('[data-testid="todo-row"]')
    expect(rows[2].classes()).toContain('wv-todo-row--unread')
    expect(rows[2].find('.wv-todo-unread-dot').exists()).toBe(true)
    expect(rows[0].classes()).not.toContain('wv-todo-row--unread')
    await rows[2].trigger('click')
    expect(wrapper.emitted('read')).toEqual([[2]])
    // A row that is not unread does not emit anything when clicked.
    await rows[0].trigger('click')
    expect(wrapper.emitted('read')).toHaveLength(1)
  })

  it('offers mark-all-read only while something is unread', async () => {
    const quiet = mount(TodoPanel, { props: base })
    expect(quiet.find('[data-testid="todo-read-all"]').exists()).toBe(false)
    const wrapper = mount(TodoPanel, { props: { ...base, unread: [2] } })
    await wrapper.get('[data-testid="todo-read-all"]').trigger('click')
    expect(wrapper.emitted('read-all')).toHaveLength(1)
  })

  it('flashes the rows that just changed and closes on the head button', async () => {
    const wrapper = mount(TodoPanel, { props: { ...base, flash: [3] } })
    const rows = wrapper.findAll('[data-testid="todo-row"]')
    expect(rows[3].classes()).toContain('wv-todo-row--flash')
    expect(rows[2].classes()).not.toContain('wv-todo-row--flash')
    await wrapper.get('[data-testid="todo-close"]').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('follows a new payload: the rows and the tally move with the update', async () => {
    const wrapper = mount(TodoPanel, { props: base })
    expect(wrapper.get('[data-testid="todo-count"]').text()).toBe('2/6')
    const next = todo()
    next.items[3].status = 'completed'
    next.items.push({ content: '真机验收', status: 'pending' } as never)
    await wrapper.setProps({ todo: next })
    expect(wrapper.findAll('[data-testid="todo-row"]')).toHaveLength(7)
    expect(wrapper.get('[data-testid="todo-count"]').text()).toBe('3/7')
    expect(wrapper.text()).toContain('真机验收')
  })
})