import { beforeEach, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, h, withDirectives } from 'vue'

import { hideTip, useTooltipState, type TipPlacement } from '@/composables/useTooltip'
import tip from '@/directives/tip'

/** The `v-tip` directive: when it shows, where, and what it leaves behind. */
describe('v-tip', () => {
  beforeEach(() => hideTip())

  /** A host whose one element is bound with the directive, props driving it. */
  function mountHost(props: { text: string; trunc: boolean; placement: TipPlacement }) {
    const Host = defineComponent({
      props: {
        text: { type: String, required: true },
        trunc: { type: Boolean, required: true },
        placement: { type: String, required: true },
      },
      setup: (hostProps) => () =>
        withDirectives(h('span', { class: 'target' }, hostProps.text), [
          [tip, hostProps.text, hostProps.placement as string, { trunc: hostProps.trunc }],
        ]),
    })
    return mount(Host, { props })
  }

  /** jsdom lays nothing out, so a width pair is declared outright. */
  function setWidths(element: Element, scrollWidth: number, clientWidth: number) {
    Object.defineProperty(element, 'scrollWidth', { configurable: true, value: scrollWidth })
    Object.defineProperty(element, 'clientWidth', { configurable: true, value: clientWidth })
  }

  it('shows the binding value on hover and takes it away on leave', async () => {
    const wrapper = mountHost({ text: 'Refresh', trunc: false, placement: 'top' })
    await wrapper.find('.target').trigger('mouseenter')
    expect(useTooltipState().text).toBe('Refresh')
    await wrapper.find('.target').trigger('mouseleave')
    expect(useTooltipState().text).toBe('')
  })

  it('shows on keyboard focus as well', async () => {
    const wrapper = mountHost({ text: 'Refresh', trunc: false, placement: 'top' })
    await wrapper.find('.target').trigger('focus')
    expect(useTooltipState().text).toBe('Refresh')
    await wrapper.find('.target').trigger('blur')
    expect(useTooltipState().text).toBe('')
  })

  it('keeps quiet on a `.trunc` element whose text fits', async () => {
    const wrapper = mountHost({ text: 'a short cell', trunc: true, placement: 'top' })
    setWidths(wrapper.find('.target').element, 80, 80)
    await wrapper.find('.target').trigger('mouseenter')
    expect(useTooltipState().text).toBe('')
  })

  it('speaks up on a `.trunc` element whose text is cut off', async () => {
    const wrapper = mountHost({ text: 'a cell that does not fit', trunc: true, placement: 'top' })
    setWidths(wrapper.find('.target').element, 240, 80)
    await wrapper.find('.target').trigger('mouseenter')
    expect(useTooltipState().text).toBe('a cell that does not fit')
  })

  it('takes the placement from its argument', async () => {
    const wrapper = mountHost({ text: 'Refresh', trunc: false, placement: 'bottom' })
    await wrapper.find('.target').trigger('mouseenter')
    expect(useTooltipState().placement).toBe('bottom')
  })

  it('reads a changed binding value on the next hover', async () => {
    const wrapper = mountHost({ text: 'First', trunc: false, placement: 'top' })
    await wrapper.find('.target').trigger('mouseenter')
    expect(useTooltipState().text).toBe('First')
    await wrapper.find('.target').trigger('mouseleave')
    await wrapper.setProps({ text: 'Second' })
    await wrapper.find('.target').trigger('mouseenter')
    expect(useTooltipState().text).toBe('Second')
  })

  it('drops a tip whose anchor is unmounted', async () => {
    const wrapper = mountHost({ text: 'Refresh', trunc: false, placement: 'top' })
    const element = wrapper.find('.target').element
    await wrapper.find('.target').trigger('mouseenter')
    expect(useTooltipState().anchor).toBe(element)
    wrapper.unmount()
    expect(useTooltipState().anchor).toBeNull()
    expect(useTooltipState().text).toBe('')
  })
})
