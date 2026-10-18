import { beforeEach, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import AppTooltip from '@/components/common/AppTooltip.vue'
import { hideTip, showTip } from '@/composables/useTooltip'

/** The one tip: where it sits against its anchor's rectangle. */
describe('AppTooltip', () => {
  beforeEach(() => hideTip())

  function mountTip() {
    return mount(AppTooltip, { global: { stubs: { teleport: true } } })
  }

  /** An anchor with a rectangle at a known place in the window. */
  function anchorAt(top: number, left = 100, size = 28) {
    const anchor = document.createElement('button')
    anchor.getBoundingClientRect = () =>
      ({
        top,
        bottom: top + size,
        left,
        right: left + size,
        width: size,
        height: size,
        x: left,
        y: top,
        toJSON: () => ({}),
      }) as DOMRect
    return anchor
  }

  it('draws nothing until a tip is asked for', () => {
    expect(mountTip().find('.forebrain-tip').exists()).toBe(false)
  })

  it('sits above an anchor that has room above it', async () => {
    const wrapper = mountTip()
    showTip(anchorAt(200), 'Delete', 'top')
    await nextTick()
    const tip = wrapper.find('.forebrain-tip')
    expect(tip.text()).toBe('Delete')
    expect(tip.attributes('role')).toBe('tooltip')
    expect(tip.classes()).toContain('forebrain-tip--above')
  })

  it('drops below an anchor with no room above it', async () => {
    const wrapper = mountTip()
    showTip(anchorAt(2), 'Delete', 'top')
    await nextTick()
    expect(wrapper.find('.forebrain-tip').classes()).toContain('forebrain-tip--below')
  })

  it('sits to the right of an anchor when told to', async () => {
    const wrapper = mountTip()
    showTip(anchorAt(200), 'Refresh', 'right')
    await nextTick()
    const tip = wrapper.find('.forebrain-tip')
    expect(tip.classes()).toContain('forebrain-tip--right')
    expect((tip.element as HTMLElement).style.left).toBe('136px')
  })

  it('goes away when the tip is cleared', async () => {
    const wrapper = mountTip()
    showTip(anchorAt(200), 'Delete', 'top')
    await nextTick()
    expect(wrapper.find('.forebrain-tip').exists()).toBe(true)
    hideTip()
    await nextTick()
    expect(wrapper.find('.forebrain-tip').exists()).toBe(false)
  })
})
