import { mount } from '@vue/test-utils'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'

import type { SubscriptionOffer } from '@/lib/api'
import { setLocale } from '@/locales'

import OfferPicker from './OfferPicker.vue'

/**
 * The models a finished sign-in unlocked (plan 266): each row is the model's
 * signature, and its title is the vendor's own name for the model — the wire id
 * a row is written with is not a name a person reads. A listing that names none
 * leaves the id as the only thing there is to show.
 */

beforeAll(() => setLocale('zh'))
afterAll(() => setLocale('en'))

function offer(overrides: Partial<SubscriptionOffer> = {}): SubscriptionOffer {
  return {
    plan: 'coding-plan',
    planName: 'Coding Plan',
    provider: 'qodercn',
    model: 'qmodel_38max',
    baseUrl: 'https://gateway.qoder.com.cn',
    apiPath: '/algo/api/v2/service/pro/sse/agent_chat_generation',
    endpoint: 'gateway.qoder.com.cn',
    contextWindow: 1_000_000,
    supportsImages: true,
    default: true,
    fromStaticList: false,
    needsKey: false,
    ...overrides,
  }
}

function mountPicker(offers: SubscriptionOffer[]) {
  return mount(OfferPicker, { props: { offers, chosen: [0] } })
}

describe('OfferPicker', () => {
  it('titles a model with the name its listing gave it', () => {
    const wrapper = mountPicker([offer({ name: 'Qwen3.8-Max' })])
    const row = wrapper.get('[data-offer="0"]').element.closest('.orow') as HTMLElement

    expect(row.querySelector('.modelsig__model')?.textContent).toBe('Qwen3.8-Max')
    expect(row.textContent).not.toContain('qmodel_38max')
    // What the vendor says about it is still there, next to the name.
    expect(row.textContent).toContain('1M')
  })

  it('shows the wire id only when the listing named the model nothing', () => {
    const wrapper = mountPicker([offer()])
    expect(wrapper.get('.modelsig__model').text()).toBe('qmodel_38max')
  })

  it('names both rows of one batch the same way', () => {
    const wrapper = mountPicker([offer({ name: 'Qwen3.8-Max' }), offer({ model: 'dmodel', name: 'DeepSeek-V4-Pro' })])
    const names = wrapper.findAll('.modelsig__model').map((node) => node.text())
    expect(names).toEqual(['Qwen3.8-Max', 'DeepSeek-V4-Pro'])
  })
})
