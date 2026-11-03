import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import type { PeerHeldRow } from '@/lib/api'
import PeerHeldBanner from './PeerHeldBanner.vue'

function held(overrides: Partial<PeerHeldRow> = {}): PeerHeldRow {
  return {
    msgId: 'm1',
    from: 'uds:/tmp/a.sock',
    fromName: 'forebrain-harness-28',
    preview: 'are you still editing open.go?',
    cause: 'mode-mismatch',
    heldAtMs: 0,
    mark: '28',
    tone: 1,
    ...overrides,
  }
}

describe('PeerHeldBanner', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))
  beforeEach(() => {
    // jsdom has no matchMedia; the banner asks it whether motion is allowed.
    (window as unknown as { matchMedia: unknown }).matchMedia = (query: string) => ({
      matches: String(query).includes('reduce'),
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
      onchange: null,
    })
  })

  it('names the sender, says why it is held, and offers both answers', () => {
    const wrapper = mount(PeerHeldBanner, { props: { held: held() } })
    const banner = wrapper.find('[data-testid="peer-held-banner"]')
    expect(banner.exists()).toBe(true)
    expect(banner.attributes('aria-label')).toBe('Message waiting for your decision')
    expect(banner.text()).toContain('forebrain-harness-28')
    expect(banner.text()).toContain("You are in full-access mode and the other side is not")
    expect(wrapper.find('[data-testid="peer-held-release"]').text()).toBe('Release')
    expect(wrapper.find('[data-testid="peer-held-deny"]').text()).toBe('Decline')
    wrapper.unmount()
  })

  it('draws nothing when nothing is waiting', () => {
    const wrapper = mount(PeerHeldBanner, { props: { held: null } })
    expect(wrapper.find('[data-testid="peer-held-banner"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('reports a denial, then settles', async () => {
    vi.useFakeTimers()
    try {
      const wrapper = mount(PeerHeldBanner, { props: { held: held() } })
      await wrapper.find('[data-testid="peer-held-deny"]').trigger('click')
      expect(wrapper.emitted('deny')?.[0]).toEqual([held()])
      expect(wrapper.find('.peer-hold').classes()).toContain('peer-hold--gone')
      await vi.advanceTimersByTimeAsync(300)
      expect(wrapper.emitted('settled')).toHaveLength(1)
      wrapper.unmount()
    } finally {
      vi.useRealTimers()
    }
  })

  it('reports a release and settles, without a flight when motion is reduced', async () => {
    vi.useFakeTimers()
    try {
      const wrapper = mount(PeerHeldBanner, {
        props: { held: held(), landingRect: () => null },
      })
      await wrapper.find('[data-testid="peer-held-release"]').trigger('click')
      expect(wrapper.emitted('release')?.[0]).toEqual([held()])
      expect(wrapper.find('.peer-hold').classes()).toContain('peer-hold--gone')
      await vi.advanceTimersByTimeAsync(300)
      expect(wrapper.emitted('settled')).toHaveLength(1)
      // Reduced motion: no clone was left behind.
      expect(document.querySelector('.peer-hold-flyer')).toBeNull()
      wrapper.unmount()
    } finally {
      vi.useRealTimers()
    }
  })

  it('collapses on the deny path without asking the engine twice', async () => {
    const wrapper = mount(PeerHeldBanner, { props: { held: held() } })
    await wrapper.find('[data-testid="peer-held-deny"]').trigger('click')
    await wrapper.find('[data-testid="peer-held-deny"]').trigger('click')
    expect(wrapper.emitted('deny')).toHaveLength(1)
    wrapper.unmount()
  })
})
