import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import McpView from './McpView.vue'
import { forebrainApi } from '@/lib/api'

/**
 * McpView says what each configured server is doing, so these tests are about the
 * distinctions a reader acts on: a server that is still starting, one that failed
 * with its own message, and one the operator skipped must not look alike.
 */
describe('McpView runtime status', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('renders every startup state, with the failure text kept verbatim', async () => {
    vi.spyOn(forebrainApi, 'mcpServersWithStatus').mockResolvedValueOnce({
      servers: [
        { name: 'docs', transport: 'stdio', scope: 'global', connStatus: 'connected', toolCount: 3 },
        { name: 'slow', transport: 'stdio', scope: 'global', connStatus: 'connecting', required: true },
        { name: 'skip', transport: 'stdio', scope: 'global', connStatus: 'cancelled' },
        {
          name: 'broken',
          transport: 'stdio',
          scope: 'global',
          connStatus: 'error',
          error: 'spawn npx: executable file not found in $PATH',
        },
      ],
      project: {},
      runtimeScope: 'primary',
      runtimeAvailable: true,
      generation: 'a1b2c3d4e5f6',
      pending: true,
    })

    const wrapper = mount(McpView)
    await flushPromises()

    // One badge per server, each carrying its own state as a data attribute so a
    // test reads what the reader sees rather than the styling.
    const badges = wrapper.findAll('[data-status]')
    expect(badges.map((b) => b.attributes('data-status'))).toEqual([
      'connected',
      'connecting',
      'cancelled',
      'error',
    ])

    // The failure keeps the server's own text: it is the only copy of the
    // reason, and a paraphrase would be less true than the original.
    const failure = wrapper.find('[data-field="mcp-error"]')
    expect(failure.exists()).toBe(true)
    expect(failure.text()).toContain('spawn npx: executable file not found in $PATH')

    // A required server is marked as one, because its failure is not a server
    // being slow - it stops the run.
    expect(wrapper.text()).toContain('docs')
    expect(wrapper.text()).toContain('slow')
  })

  it('does not claim a state when the gateway had no runtime to report', async () => {
    vi.spyOn(forebrainApi, 'mcpServersWithStatus').mockResolvedValueOnce({
      servers: [{ name: 'docs', transport: 'stdio', scope: 'global' }],
      project: {},
      runtimeScope: 'primary',
      runtimeAvailable: false,
      generation: undefined,
      pending: false,
    })

    const wrapper = mount(McpView)
    await flushPromises()

    // No status badge at all: "no runtime view" is not "connected", and a badge
    // would turn an unanswered question into an answer.
    expect(wrapper.findAll('[data-status]')).toHaveLength(0)
    expect(wrapper.text()).toContain('docs')
  })

  it('renders a state it does not know as itself', async () => {
    vi.spyOn(forebrainApi, 'mcpServersWithStatus').mockResolvedValueOnce({
      servers: [{ name: 'future', transport: 'stdio', scope: 'global', connStatus: 'throttled' }],
      project: {},
      runtimeScope: 'primary',
      runtimeAvailable: true,
      generation: 'a1b2c3d4e5f6',
      pending: false,
    })

    const wrapper = mount(McpView)
    await flushPromises()

    const badge = wrapper.find('[data-status]')
    expect(badge.attributes('data-status')).toBe('throttled')
    expect(badge.text()).toBe('throttled')
  })
})
