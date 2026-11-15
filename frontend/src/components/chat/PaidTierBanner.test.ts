import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale, t } from '@/locales'
import PaidTierBanner from './PaidTierBanner.vue'

describe('PaidTierBanner', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  it('names the reset and offers both answers', async () => {
    const wrapper = mount(PaidTierBanner, {
      props: {
        offer: {
          provider: 'workbuddy',
          freeModel: 'hy4free',
          paidModel: 'hy4std',
          displayName: 'Hy4',
          resetAt: '2026-11-16T00:00:00+08:00',
        },
      },
    })
    const banner = wrapper.find('[data-testid="paid-tier-banner"]')
    expect(banner.exists()).toBe(true)
    expect(banner.attributes('role')).toBe('status')
    expect(banner.text()).toContain('Hy4 free tier spent until')
    expect(banner.text()).toContain('(UTC+8)')

    await wrapper.find('[data-testid="paid-tier-use"]').trigger('click')
    await wrapper.find('[data-testid="paid-tier-dismiss"]').trigger('click')
    expect(wrapper.emitted('use')).toHaveLength(1)
    expect(wrapper.emitted('dismiss')).toHaveLength(1)
    wrapper.unmount()
  })

  it('says what continuing costs when the product named no reset', () => {
    const wrapper = mount(PaidTierBanner, {
      props: { offer: { provider: 'workbuddy', freeModel: 'hy3', paidModel: 'hy3-x', displayName: 'Hy3' } },
    })
    const text = wrapper.find('[data-testid="paid-tier-banner"]').text()
    expect(text).toContain('Hy3 free tier spent')
    expect(text).not.toContain('until')
    expect(text).toContain('paid rate')
    wrapper.unmount()
  })

  it('draws nothing when no offer is waiting', () => {
    const wrapper = mount(PaidTierBanner, { props: { offer: null } })
    expect(wrapper.find('[data-testid="paid-tier-banner"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('names the paid tier on its button', () => {
    setLocale('zh')
    const wrapper = mount(PaidTierBanner, {
      props: { offer: { provider: 'workbuddy', freeModel: 'hy4free', paidModel: 'hy4std', displayName: 'Hy4' } },
    })
    expect(wrapper.find('[data-testid="paid-tier-use"]').text()).toBe(t('chat.paidTier.usePaid', { model: 'Hy4' }))
    expect(wrapper.find('[data-testid="paid-tier-dismiss"]').text()).toBe(t('chat.paidTier.dismiss'))
    wrapper.unmount()
    setLocale('en')
  })
})
