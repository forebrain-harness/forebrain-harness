import { beforeAll, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import { Conversation, ConversationScrollButton } from '@repo/elements/conversation'

/**
 * Back to the bottom (owner 2026-10-25): the button shows whenever the reader
 * has scrolled away from the newest content, and one click hands them back to
 * the library's own scroll. The test drives the conversation's real scroll
 * box: the heights jsdom does not lay out are pinned the way a laid-out page
 * would report them, and the scroll events a real page fires are sent by hand.
 */
describe('ConversationScrollButton', () => {
  beforeAll(() => {
    ;(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    // Reduced motion makes the click's scroll land at once, the way a reader
    // who asked for less animation gets there.
    ;(window as unknown as { matchMedia: unknown }).matchMedia = (query: string) => ({
      matches: query.includes('prefers-reduced-motion'),
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    })
  })

  function mountThread() {
    const wrapper = mount({
      components: { Conversation, ConversationScrollButton },
      template: '<Conversation class="thread"><ConversationScrollButton label="Back to the latest" /></Conversation>',
    })
    // The library's own scroll box: the inner div it calls scrollRef (the
    // class lands on its outer wrapper, which does not scroll).
    const scroll = wrapper.element.querySelector('.thread > div > div') as HTMLElement
    // A page taller than its window: 1000px of thread in a 200px box, whose
    // bottom sits at scrollTop 800 (the library's 1px rounding included).
    Object.defineProperty(scroll, 'scrollHeight', { configurable: true, value: 1000 })
    Object.defineProperty(scroll, 'clientHeight', { configurable: true, value: 200 })
    return { wrapper, scroll }
  }

  function scrollTo(scroll: HTMLElement, top: number) {
    scroll.scrollTop = top
    scroll.dispatchEvent(new Event('scroll'))
  }

  it('draws nothing while the reader is at the bottom', async () => {
    const { wrapper, scroll } = mountThread()
    scrollTo(scroll, 999)
    await vi.waitFor(() => {
      expect(wrapper.find('button[aria-label="Back to the latest"]').exists()).toBe(false)
    })
    wrapper.unmount()
  })

  it('draws once the reader has scrolled away, and goes back down on a click', async () => {
    const { wrapper, scroll } = mountThread()
    // Start at the bottom, the way a followed thread opens.
    scrollTo(scroll, 999)
    await vi.waitFor(() => {
      expect(wrapper.find('button[aria-label="Back to the latest"]').exists()).toBe(false)
    })
    // Then the reader scrolls up, away from the newest content.
    scrollTo(scroll, 0)
    await vi.waitFor(() => {
      expect(wrapper.find('button[aria-label="Back to the latest"]').exists()).toBe(true)
    })

    await wrapper.get('button[aria-label="Back to the latest"]').trigger('click')
    await vi.waitFor(() => {
      // The click lands at the thread's bottom (the library's own target).
      expect(scroll.scrollTop).toBeGreaterThan(700)
    })
    // Back at the bottom, the button is gone.
    await vi.waitFor(() => {
      expect(wrapper.find('button[aria-label="Back to the latest"]').exists()).toBe(false)
    })
    wrapper.unmount()
  })
})
