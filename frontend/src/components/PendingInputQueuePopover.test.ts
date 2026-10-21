import { beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import PendingInputQueuePopover from './PendingInputQueuePopover.vue'
// The component's own source, for the one thing jsdom cannot show: the rule
// that makes the list scroll. Read through Vite so the assertion is against the
// file that ships, never a copy.
import queueSource from './PendingInputQueuePopover.vue?raw'
import type { PendingInputPreview } from '@/composables/useChatStream'

/**
 * What is waiting behind the running turn, drawn with the library's queue: one
 * section per kind, its count in the section label, and the two ways out above
 * them. The copy is the interface's language — the queue is not a place where a
 * Chinese surface prints English.
 */
describe('PendingInputQueuePopover', () => {
  beforeAll(() => setLocale('en'))

  function preview(overrides: Partial<PendingInputPreview> = {}): PendingInputPreview {
    return {
      pendingSteers: [],
      rejectedSteers: [],
      queuedMessages: [],
      ...overrides,
    }
  }

  it('draws nothing while nothing is waiting', () => {
    const wrapper = mount(PendingInputQueuePopover, { props: { preview: preview() } })
    expect(wrapper.find('[data-testid="pending-queue"]').exists()).toBe(false)
  })

  it('draws one section per kind of waiting message, with its count', () => {
    const wrapper = mount(PendingInputQueuePopover, {
      props: {
        preview: preview({
          pendingSteers: ['look at the tests first'],
          rejectedSteers: ['and then the docs'],
          queuedMessages: ['finally the changelog'],
        }),
      },
    })
    const sections = wrapper.findAll('[data-testid="queue-section"]')
    expect(sections).toHaveLength(3)
    expect(sections.map((section) => section.text())).toEqual([
      expect.stringContaining('1 Sent after the next tool call'),
      expect.stringContaining('1 Sent at the end of the turn'),
      expect.stringContaining('1 Queued follow-ups'),
    ])
    expect(wrapper.text()).toContain('3 pending')
    expect(wrapper.text()).toContain('look at the tests first')
  })

  it('leaves out a section nothing is waiting in', () => {
    const wrapper = mount(PendingInputQueuePopover, {
      props: { preview: preview({ queuedMessages: ['only this one'] }) },
    })
    expect(wrapper.findAll('[data-testid="queue-section"]')).toHaveLength(1)
    expect(wrapper.text()).toContain('Queued follow-ups')
  })

  it('reports the two ways out', async () => {
    const wrapper = mount(PendingInputQueuePopover, {
      props: { preview: preview({ pendingSteers: ['a steer'] }) },
    })
    await wrapper.get('[data-testid="queue-interrupt"]').trigger('click')
    await wrapper.get('[data-testid="queue-edit"]').trigger('click')
    expect(wrapper.emitted('interrupt-run')).toHaveLength(1)
    expect(wrapper.emitted('edit-last-queued')).toHaveLength(1)
  })

  // A row used to carry its own「取回」button, and every one of them sent the same
  // "take the last queued message back" event — a control that promised
  // something it could not do. The head's button is the one real way to take a
  // message back, so a row holds no control at all now.
  it('offers no per-row take-back, only the head\'s own button', async () => {
    const wrapper = mount(PendingInputQueuePopover, {
      props: { preview: preview({ queuedMessages: ['one', 'two', 'three'] }) },
    })
    // No control lives inside a section: a row is a line of text and its dot,
    // and a section's trigger is the fold, which is the section's own header.
    for (const section of wrapper.findAll('[data-testid="queue-section"]')) {
      expect(section.findAll('[data-testid="queue-item"] button')).toHaveLength(0)
    }
    // The one control that names itself is the head's.
    const named = wrapper.findAll('button')
      .map((button) => button.attributes('data-testid'))
      .filter(Boolean)
    expect(named).toEqual(['queue-edit'])
    await wrapper.get('[data-testid="queue-edit"]').trigger('click')
    expect(wrapper.emitted('edit-last-queued')).toHaveLength(1)
  })

  // Long content scrolls; it is never cut at the card's edge (the house rule).
  // jsdom has no layout and the component's stylesheet is not applied in this
  // environment, so the two halves are asserted where each really lives: the
  // element that must scroll is inside the bounded card (structure here), and
  // the scroll contract itself is a rule in this component's own stylesheet.
  it('puts the list in a scroll container inside the bounded card', () => {
    const many = Array.from({ length: 30 }, (_, i) => `queued message ${i + 1}`)
    const wrapper = mount(PendingInputQueuePopover, {
      props: { preview: preview({ queuedMessages: many }) },
    })
    const body = wrapper.get('[data-testid="queue-body"]')
    // The scroller holds every section, and the card holds the scroller while
    // the head sits outside it (so the head never scrolls away).
    expect(body.findAll('[data-testid="queue-section"]')).toHaveLength(1)
    expect(body.element.parentElement?.className).toContain('queue-card')
    expect(body.element.querySelector('[data-testid="queue-section"]')).not.toBeNull()
    expect(wrapper.get('.queue-head').element.contains(body.element)).toBe(false)
    // Nothing is truncated in the DOM: the tail is there to be scrolled to.
    expect(wrapper.text()).toContain('queued message 1')
    expect(wrapper.text()).toContain('queued message 30')

    // The rule that makes it scroll, read from the file that declares it.
    const rule = queueSource.match(/\.queue-body\s*\{[^}]*\}/)?.[0] ?? ''
    expect(rule).toContain('overflow-y: auto')
    // min-height:0 is what lets a flex child shrink below its content; without
    // it the scrollbar never appears and the tail is clipped instead.
    expect(rule).toContain('min-height: 0')
    // The card bounds the height and keeps its own overflow hidden, so the
    // round corners and the head's hairline stay put.
    const card = queueSource.match(/\.queue-card\s*\{[^}]*\}/)?.[0] ?? ''
    expect(card).toContain('max-height: 42vh')
    expect(card).toContain('overflow: hidden')
  })

  it('says the same thing in a Chinese interface', () => {
    setLocale('zh')
    const wrapper = mount(PendingInputQueuePopover, {
      props: { preview: preview({ pendingSteers: ['先看测试'] }) },
    })
    expect(wrapper.text()).toContain('消息队列')
    expect(wrapper.text()).toContain('下一个工具调用之后发出')
    expect(wrapper.text()).not.toContain('Message queue')
    setLocale('en')
  })
})
