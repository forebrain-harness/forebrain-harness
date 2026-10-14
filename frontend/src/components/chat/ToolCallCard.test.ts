import { afterEach, beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import ToolCallCard from './ToolCallCard.vue'
import type { SubagentToolStep } from '@/composables/useChatStream'

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
})
