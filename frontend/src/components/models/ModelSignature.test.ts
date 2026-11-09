import { mount } from '@vue/test-utils'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'

import { setLocale } from '@/locales'

import ModelSignature from './ModelSignature.vue'

/**
 * How a model is written (plan 266): name and provider on one line, endpoint and
 * API path on the next, so two rows for one model are two distinguishable rows.
 * The variants a picker asks for are the ones tested here: a bare address (the
 * dropdown's narrow line), a list already grouped by provider, a row whose
 * address is the whole request, and a row with no path at all.
 */

beforeAll(() => setLocale('zh'))
afterAll(() => setLocale('en'))

describe('ModelSignature', () => {
  it('shows the four facts: model, provider, address, path', () => {
    const wrapper = mount(ModelSignature, {
      props: {
        model: 'deepseek-v4-pro',
        providerName: '深度求索',
        baseUrl: 'https://api.deepseek.com',
        apiPath: '/chat/completions',
      },
    })

    expect(wrapper.text()).toContain('deepseek-v4-pro')
    expect(wrapper.text()).toContain('深度求索')
    expect(wrapper.text()).toContain('https://api.deepseek.com')
    expect(wrapper.find('.forebrain-path').text()).toBe('/chat/completions')
  })

  it('can drop the scheme for a narrow dropdown', () => {
    const wrapper = mount(ModelSignature, {
      props: {
        model: 'gpt-5',
        baseUrl: 'https://api.openai.com/v1',
        apiPath: '/responses',
        bare: true,
      },
    })

    expect(wrapper.text()).toContain('api.openai.com/v1')
    expect(wrapper.text()).not.toContain('https://')
  })

  it('leaves the provider out of a list already grouped by it', () => {
    const wrapper = mount(ModelSignature, {
      props: { model: 'glm-5.3', providerName: '智谱', baseUrl: 'https://api.z.ai', apiPath: '/chat/completions', hideProvider: true },
    })

    expect(wrapper.text()).not.toContain('智谱')
    expect(wrapper.text()).toContain('glm-5.3')
  })

  it('says 自定义协议 instead of a path the address does not append', () => {
    const wrapper = mount(ModelSignature, {
      props: { model: 'qwen3-coder', baseUrl: 'https://gw.example.com/llm/chat', urlVerbatim: true },
    })

    expect(wrapper.find('.forebrain-path').text()).toBe('自定义协议')
  })

  it('draws no second line for a model whose sign-in decides the endpoint', () => {
    const wrapper = mount(ModelSignature, { props: { model: 'gpt-5.6', providerName: 'ChatGPT' } })

    expect(wrapper.find('.modelsig__addr').exists()).toBe(false)
  })
})
