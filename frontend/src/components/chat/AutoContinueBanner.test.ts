import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import AutoContinueBanner from './AutoContinueBanner.vue'

describe('AutoContinueBanner', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  it('says when the conversation continues and offers to cancel it', async () => {
    const at = new Date(Date.now() + 60 * 60 * 1000)
    const wrapper = mount(AutoContinueBanner, {
      props: { state: { continueAt: at.toISOString(), code: 'rate_limit_quota' } },
    })
    const banner = wrapper.find('[data-testid="auto-continue-banner"]')
    expect(banner.exists()).toBe(true)
    expect(banner.attributes('role')).toBe('status')
    expect(banner.text()).toContain('Usage limit reached · continuing automatically at')

    await wrapper.find('[data-testid="auto-continue-cancel"]').trigger('click')
    expect(wrapper.emitted('cancel')).toHaveLength(1)
    wrapper.unmount()
  })

  it('draws nothing when no continuation is waiting', () => {
    const wrapper = mount(AutoContinueBanner, { props: { state: null } })
    expect(wrapper.find('[data-testid="auto-continue-banner"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
