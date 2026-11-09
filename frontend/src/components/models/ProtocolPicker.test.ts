import { mount } from '@vue/test-utils'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'

import { setLocale } from '@/locales'

import ProtocolPicker from './ProtocolPicker.vue'

/**
 * 「接口协议」 (plan 266): a dropdown whose trigger repeats the protocol's name
 * only, and whose options each name themselves on one line and their path on the
 * next. The custom protocol is a custom provider's fourth choice — the one that
 * makes an address the whole request — and a row already storing another path
 * keeps it as an extra choice, so editing another field cannot drop it.
 */

const PROTOCOLS = [
  { apiPath: '/chat/completions', name: 'OpenAI Chat Completions' },
  { apiPath: '/responses', name: 'OpenAI Responses' },
  { apiPath: '/v1/messages', name: 'Anthropic Messages' },
]

beforeAll(() => setLocale('zh'))
afterAll(() => setLocale('en'))

describe('ProtocolPicker', () => {
  it('shows the protocol name on the trigger and the path only in the list', async () => {
    const wrapper = mount(ProtocolPicker, { props: { modelValue: '/v1/messages', protocols: PROTOCOLS } })
    const trigger = wrapper.find('[data-testid="models-protocol-trigger"]')

    expect(trigger.text()).toContain('Anthropic Messages')
    expect(trigger.text()).not.toContain('/v1/messages')

    await trigger.trigger('click')
    const options = wrapper.findAll('[data-protocol]')
    expect(options).toHaveLength(3)
    expect(options[0].text()).toContain('Anthropic Messages')
    expect(options[0].text()).toContain('/v1/messages')
  })

  it('offers 自定义协议 only to a custom provider, last and behind a divider', async () => {
    const builtin = mount(ProtocolPicker, { props: { modelValue: '', protocols: PROTOCOLS } })
    await builtin.find('[data-testid="models-protocol-trigger"]').trigger('click')
    expect(builtin.findAll('[data-protocol]')).toHaveLength(3)
    expect(builtin.find('.proto__divider').exists()).toBe(false)

    const custom = mount(ProtocolPicker, { props: { modelValue: '', protocols: PROTOCOLS, allowVerbatim: true } })
    await custom.find('[data-testid="models-protocol-trigger"]').trigger('click')
    const options = custom.findAll('[data-protocol]')
    expect(options).toHaveLength(4)
    expect(options[3].attributes('data-protocol')).toBe('@verbatim')
    expect(options[3].text()).toContain('自定义协议')
    expect(custom.find('.proto__divider').exists()).toBe(true)
  })

  it('picking 自定义协议 raises the verbatim flag instead of storing a path', async () => {
    const wrapper = mount(ProtocolPicker, {
      props: { modelValue: '/chat/completions', protocols: PROTOCOLS, allowVerbatim: true },
    })
    await wrapper.find('[data-testid="models-protocol-trigger"]').trigger('click')
    await wrapper.find('[data-protocol="@verbatim"]').trigger('click')

    expect(wrapper.emitted('update:verbatim')?.[0]).toEqual([true])
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('keeps a stored path none of the three names as its own choice', async () => {
    const wrapper = mount(ProtocolPicker, {
      props: { modelValue: '/v2/chat', protocols: PROTOCOLS, storedPath: '/v2/chat' },
    })
    await wrapper.find('[data-testid="models-protocol-trigger"]').trigger('click')

    const all = wrapper.findAll('[data-protocol]')
    const extra = all[all.length - 1]
    expect(extra?.attributes('data-protocol')).toBe('/v2/chat')
    expect(extra?.text()).toContain('当前路径')
    expect(wrapper.find('[data-testid="models-protocol-trigger"]').text()).toContain('当前路径')
  })
})
