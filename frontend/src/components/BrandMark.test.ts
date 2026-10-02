import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import BrandMark from './BrandMark.vue'

describe('BrandMark', () => {
  it('renders the seal: one rounded square, one glyph path, one seal block', () => {
    const wrapper = mount(BrandMark)
    const svg = wrapper.find('svg')
    expect(svg.exists()).toBe(true)
    expect(svg.findAll('rect[rx="7"]')).toHaveLength(1)
    expect(svg.findAll('path')).toHaveLength(1)
    expect(svg.findAll('rect[rx="0.6"]')).toHaveLength(1)
  })

  it('sizes from the size prop', () => {
    const wrapper = mount(BrandMark, { props: { size: 44 } })
    expect(wrapper.find('svg').attributes('width')).toBe('44px')
    expect(wrapper.find('svg').attributes('height')).toBe('44px')
  })

  it('paints only through CSS variables — no colour literal in the template', () => {
    const html = mount(BrandMark).html()
    expect(/fill="(?!var\()/.test(html)).toBe(false)
  })
})

