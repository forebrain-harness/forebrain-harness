import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import PeerMessageCard from './PeerMessageCard.vue'

// The markdown renderer the card hands the body to pulls in the design system,
// which a component test has no reason to load: the card's own contract is the
// header, the tone rule and the body it passes on.
vi.mock('@repo/elements/message', () => ({
  MessageResponse: {
    name: 'MessageResponse',
    props: { content: { type: String, default: '' } },
    template: '<div class="md">{{ content }}</div>',
  },
}))

describe('PeerMessageCard', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  it('names the sender, the kind and the directory, and rules the body in their tone', () => {
    const wrapper = mount(PeerMessageCard, {
      props: {
        fromName: 'forebrain-harness-28',
        mark: '28',
        tone: 1,
        body: 'are you still editing open.go?',
        kind: 'terminal',
        sameTree: true,
      },
    })
    const card = wrapper.find('[data-testid="peer-message-28"]')
    expect(card.exists()).toBe(true)
    expect(card.text()).toContain('forebrain-harness-28')
    expect(card.text()).toContain('Terminal')
    expect(card.text()).toContain('Same directory')
    expect(card.text()).toContain('are you still editing open.go?')
    // The tone is the sender's, as a custom property the rule is drawn with.
    expect(card.attributes('style')).toContain('--peer-tone: #6B4C9A')
    wrapper.unmount()
  })

  it('falls back to the row content when the envelope could not be read', () => {
    const wrapper = mount(PeerMessageCard, {
      props: { fromName: 'forebrain-harness-a3', mark: 'a3', tone: 0, content: '<cross-session-message>' },
    })
    expect(wrapper.text()).toContain('<cross-session-message>')
    wrapper.unmount()
  })

  it('carries the row time as the header clock, and no time when the row had none', () => {
    const at = Date.UTC(2026, 9, 30, 6, 32)
    const expected = new Date(at).toLocaleTimeString('en', { hour: '2-digit', minute: '2-digit', hour12: false })
    const wrapper = mount(PeerMessageCard, {
      props: { fromName: 'forebrain-harness-28', mark: '28', body: 'hi', createdAt: at },
    })
    expect(wrapper.find('time').text()).toBe(expected)
    expect(expected).toMatch(/^\d{2}:\d{2}$/)

    const bare = mount(PeerMessageCard, { props: { fromName: 'forebrain-harness-28', mark: '28', body: 'hi' } })
    expect(bare.find('time').exists()).toBe(false)
    wrapper.unmount()
    bare.unmount()
  })

  it('says when the sender is unknown rather than drawing an empty header', () => {
    const wrapper = mount(PeerMessageCard, { props: { mark: '??', body: 'hello' } })
    expect(wrapper.text()).toContain('Unknown agent')
    wrapper.unmount()
  })
})
