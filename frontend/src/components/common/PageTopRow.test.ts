import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import PageTopRow from './PageTopRow.vue'

/** A page's one top row: the name, an optional description and its buttons. */
describe('PageTopRow', () => {
  it('renders the title, description and actions', () => {
    const wrapper = mount(PageTopRow, {
      props: { title: '技能', description: '本主代理能使用的全部技能。' },
      slots: { actions: '<button type="button">在线安装</button>' },
    })
    expect(wrapper.find('.forebrain-page-top-title').text()).toBe('技能')
    expect(wrapper.find('.forebrain-page-top-desc').text()).toBe('本主代理能使用的全部技能。')
    expect(wrapper.find('.forebrain-page-top-actions button').text()).toBe('在线安装')
  })

  it('omits the description and actions row when neither is present', () => {
    const wrapper = mount(PageTopRow, { props: { title: '记忆' } })
    expect(wrapper.find('.forebrain-page-top-desc').exists()).toBe(false)
    expect(wrapper.find('.forebrain-page-top-actions').exists()).toBe(false)
  })
})
