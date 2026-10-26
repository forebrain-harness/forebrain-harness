import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { config, mount } from '@vue/test-utils'
import tip from '@/directives/tip'

import { setLocale, t } from '@/locales'
import { hideTip, useTooltipState } from '@/composables/useTooltip'
import AutoContinueBanner from './AutoContinueBanner.vue'

// The app registers `tip` once (main.ts); mounting here does the same, so an
// element that names itself with it resolves instead of warning.
config.global.directives = { tip }

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

  it('names only the button where there is no composer to type in', async () => {
    hideTip()
    const at = new Date(Date.now() + 60 * 60 * 1000)
    const state = { continueAt: at.toISOString(), code: 'rate_limit_quota' as const }
    // Beside a composer, typing in it cancels the wait as well — the hint says
    // so. In a view with no composer (an idle subagent's tab, plan 143) there
    // is nothing to type into, so the hint names the button alone.
    const typing = mount(AutoContinueBanner, { props: { state } })
    await typing.find('[data-testid="auto-continue-cancel"]').trigger('mouseenter')
    expect(useTooltipState().text).toBe(t('autoContinue.cancelHint'))
    expect(t('autoContinue.cancelHint')).toContain('typing')
    typing.unmount()

    hideTip()
    const alone = mount(AutoContinueBanner, { props: { state, typingCancels: false } })
    await alone.find('[data-testid="auto-continue-cancel"]').trigger('mouseenter')
    expect(useTooltipState().text).toBe(t('autoContinue.cancelHintEscOnly'))
    expect(t('autoContinue.cancelHintEscOnly')).toBe('Cancel auto-continue')
    alone.unmount()
  })
})
