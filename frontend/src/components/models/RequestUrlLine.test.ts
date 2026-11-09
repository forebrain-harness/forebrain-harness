import { mount } from '@vue/test-utils'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'

import { setLocale } from '@/locales'

import RequestUrlLine from './RequestUrlLine.vue'

/**
 * 「实际请求」 (plan 266): the address a request really goes to, with the path
 * highlighted after it. It is the one place the two are shown joined — and a
 * 「自定义协议」 row says the address is the whole request instead of pretending a
 * path is appended.
 */

beforeAll(() => setLocale('zh'))
afterAll(() => setLocale('en'))

describe('RequestUrlLine', () => {
  it('joins the address and the protocol’s path, highlighting the path', () => {
    const wrapper = mount(RequestUrlLine, { props: { baseUrl: 'https://api.deepseek.com', apiPath: '/chat/completions' } })

    expect(wrapper.text()).toContain('实际请求')
    expect(wrapper.text()).toContain('https://api.deepseek.com')
    expect(wrapper.find('em').text()).toBe('/chat/completions')
  })

  it('says 实际请求（原样） and appends nothing for a verbatim row', () => {
    const wrapper = mount(RequestUrlLine, {
      props: { baseUrl: 'https://gw.example.com/llm/chat', apiPath: '/chat/completions', verbatim: true },
    })

    expect(wrapper.text()).toContain('实际请求（原样）')
    expect(wrapper.find('em').exists()).toBe(false)
    expect(wrapper.text()).toContain('https://gw.example.com/llm/chat')
  })

  it('asks for an address and a protocol while the form is empty', () => {
    const wrapper = mount(RequestUrlLine, { props: { baseUrl: '', apiPath: '' } })

    expect(wrapper.text()).toContain('（填接口地址）')
    expect(wrapper.find('em').text()).toBe('选择接口协议')
  })
})
