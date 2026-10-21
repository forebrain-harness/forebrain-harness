import { afterEach, beforeAll, describe, expect, it } from 'vitest'
import { config, mount } from '@vue/test-utils'
import tip from '@/directives/tip'

import { setLocale } from '@/locales'
import ToolCallCard from './ToolCallCard.vue'
import type { SubagentToolStep } from '@/composables/useChatStream'

// The app registers `tip` once (main.ts); mounting here does the same, so an
// element that names itself with it resolves instead of warning.
config.global.directives = { tip }

function stepOf(overrides: Partial<SubagentToolStep> = {}): SubagentToolStep {
  return {
    stepId: 'call-1',
    toolName: 'read_file',
    summary: 'Read shot.png · image 1.4 MB',
    status: 'completed',
    ...overrides,
  }
}

/**
 * An image step draws its pixels as a thumbnail fetched from the workspace raw
 * endpoint — never base64 in the step payload — and every other step draws no
 * <img> at all.
 */
describe('ToolCallCard', () => {
  beforeAll(() => setLocale('en'))
  afterEach(() => setLocale('en'))

  it('renders a thumbnail pointing at the raw endpoint for an image step', () => {
    const wrapper = mount(ToolCallCard, {
      props: {
        // Content renders only while the card is open.
        open: true,
        scope: { sessionId: 's1' },
        step: stepOf({
          previewKind: 'image',
          previewPath: '/proj/shot.png',
          previewWidth: 1200,
          previewHeight: 600,
          previewBytes: 1450000,
        }),
      },
    })

    const img = wrapper.find('img')
    expect(img.exists()).toBe(true)
    expect(img.attributes('src')).toBe('/api/workspace/file/raw?path=%2Fproj%2Fshot.png&session_id=s1')
    expect(img.attributes('loading')).toBe('lazy')
    expect(img.attributes('alt')).toContain('shot.png')
    expect(img.attributes('alt')).toContain('1200×600')
    // A click opens the full image in a new tab.
    expect(wrapper.find('a').attributes('href')).toBe(img.attributes('src'))
    expect(wrapper.find('a').attributes('target')).toBe('_blank')
  })

  it('renders no thumbnail for a text step', () => {
    const wrapper = mount(ToolCallCard, {
      props: { open: true, scope: { sessionId: 's1' }, step: stepOf() },
    })
    expect(wrapper.find('img').exists()).toBe(false)
  })

  it('renders no thumbnail when the caller supplied no scope', () => {
    const wrapper = mount(ToolCallCard, {
      props: {
        open: true,
        step: stepOf({ previewKind: 'image', previewPath: '/proj/shot.png', previewWidth: 1, previewHeight: 1 }),
      },
    })
    expect(wrapper.find('img').exists()).toBe(false)
  })

  it('captions the thumbnail with dimensions, bytes and scaled, omitting unknowns', () => {
    const wrapper = mount(ToolCallCard, {
      props: {
        open: true,
        scope: { sessionId: 's1' },
        step: stepOf({
          previewKind: 'image',
          previewPath: '/proj/shot.png',
          previewWidth: 1200,
          previewHeight: 600,
          previewBytes: 1_450_000,
          previewScaled: true,
        }),
      },
    })
    // Aligned with the TUI's ▤ line: WxH · bytes · scaled.
    expect(wrapper.text()).toContain('1200×600')
    expect(wrapper.text()).toContain('1.4 MB')
    expect(wrapper.text()).toContain('scaled')

    const partial = mount(ToolCallCard, {
      props: {
        open: true,
        scope: { sessionId: 's1' },
        // A refused or degraded block: dimensions parsed, no bytes, no scale.
        // The summary says no size either, so a stray byte count can only come
        // from the caption.
        step: stepOf({
          summary: 'Read shot.png (saved, not sent)',
          previewKind: 'image',
          previewPath: '/proj/shot.png',
          previewWidth: 1200,
          previewHeight: 600,
        }),
      },
    })
    expect(partial.text()).toContain('1200×600')
    expect(partial.text()).not.toContain('scaled')
    expect(partial.text()).not.toMatch(/\d+(\.\d+)? (KB|MB)/)
  })

  it('never announces 0×0 for an image whose dimensions are unknown', () => {
    const wrapper = mount(ToolCallCard, {
      props: {
        open: true,
        scope: { sessionId: 's1' },
        step: stepOf({ previewKind: 'image', previewPath: '/proj/shot.webp' }),
      },
    })
    const alt = wrapper.find('img').attributes('alt')
    expect(alt).toContain('shot.webp')
    expect(alt).not.toContain('0×0')
  })

  it('draws an answered question as its question and its answers, opened', () => {
    // The runtime's settled body: one line per question, each answer on its
    // own line behind an arrow (pkg/tool/format.go FormatUserInteractionResult).
    const wrapper = mount(ToolCallCard, {
      props: {
        step: stepOf({
          toolName: 'user_interaction',
          summary: 'asked user',
          status: 'completed',
          output: '这次改动怎么交付？\n  → 设计方案并开发实施\n要动哪些范围？\n  → TUI\n  → 网关',
        }),
      },
    })
    const done = wrapper.get('[data-testid="ask-done"]')
    expect(done.findAll('.q').map((q) => q.text())).toEqual(['这次改动怎么交付？', '要动哪些范围？'])
    expect(done.findAll('.ans').map((a) => a.text())).toEqual([
      '→ 设计方案并开发实施',
      '→ TUI',
      '→ 网关',
    ])
    // A settled ask is drawn open: the answer is the point of the card.
    expect(wrapper.get('[data-slot="collapsible-trigger"]').attributes('aria-expanded')).toBe('true')
  })

  it('keeps a question that is still waiting as an ordinary call', () => {
    const wrapper = mount(ToolCallCard, {
      props: {
        step: stepOf({ toolName: 'user_interaction', summary: 'asked user', status: 'awaiting approval', output: '这次改动怎么交付？\n  → 设计方案并开发实施' }),
      },
    })
    expect(wrapper.find('[data-testid="ask-done"]').exists()).toBe(false)
  })

  it('keeps a short summary out of the body: no cmd-full block', () => {
    const wrapper = mount(ToolCallCard, {
      props: { open: true, scope: { sessionId: 's1' }, step: stepOf() },
    })
    expect(wrapper.find('.cmd-full').exists()).toBe(false)
  })

  it('moves a long command verbatim into a cmd-full block', () => {
    const summary = `ran echo ${'x'.repeat(200)}`
    const wrapper = mount(ToolCallCard, {
      props: {
        open: true,
        scope: { sessionId: 's1' },
        step: stepOf({ toolName: 'shell', summary }),
      },
    })
    const block = wrapper.find('.cmd-full')
    expect(block.exists()).toBe(true)
    expect(block.text()).toBe(summary)
  })

  it('carries the full summary in the header tooltip', () => {
    const summary = `ran echo ${'x'.repeat(200)}`
    const wrapper = mount(ToolCallCard, {
      props: {
        open: true,
        scope: { sessionId: 's1' },
        step: stepOf({ toolName: 'shell', summary }),
      },
    })
    // The one-line header truncates; the native title attribute holds the text
    // the ellipsis took away.
    const head = wrapper.find('.truncate')
    expect(head.exists()).toBe(true)
    expect(head.attributes('title')).toBe(summary)
    expect(head.text()).toBe(summary)
  })
})
