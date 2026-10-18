import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import SwitchRow from './SwitchRow.vue'

/** One switch row: the name, what it means, and the switch that changes it. */
describe('SwitchRow', () => {
  function mountRow(props: Record<string, unknown> = {}) {
    return mount(SwitchRow, {
      props: { title: 'Trust this directory', hint: 'Stops asking before running commands here.', modelValue: false, ...props },
    })
  }

  it('draws the name, its hint and the switch, with no frame around the row', () => {
    const wrapper = mountRow()
    expect(wrapper.text()).toContain('Trust this directory')
    expect(wrapper.text()).toContain('Stops asking before running commands here.')
    expect(wrapper.find('.forebrain-switch').exists()).toBe(true)
    // No outer box: the row is separated by a rule, not wrapped in a card.
    expect(wrapper.classes()).not.toContain('forebrain-card')
    expect(wrapper.classes()).toContain('border-t')
  })

  it('names the switch after the row, so a screen reader hears what it turns on', () => {
    expect(mountRow().find('.forebrain-switch').attributes('aria-label')).toBe('Trust this directory')
  })

  it('is off until the model says on, and hands the flip back', async () => {
    const wrapper = mountRow()
    expect(wrapper.find('.forebrain-switch').attributes('aria-checked')).toBe('false')

    await wrapper.find('.forebrain-switch').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([[true]])

    await wrapper.setProps({ modelValue: true })
    expect(wrapper.find('.forebrain-switch').attributes('aria-checked')).toBe('true')
    expect(wrapper.find('.forebrain-switch').classes()).toContain('forebrain-switch--on')
  })

  it('does not flip while it is disabled', async () => {
    const wrapper = mountRow({ disabled: true })
    expect(wrapper.find('.forebrain-switch').attributes('disabled')).toBeDefined()
    await wrapper.find('.forebrain-switch').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })
})
