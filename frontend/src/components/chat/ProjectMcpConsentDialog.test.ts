import { beforeAll, describe, expect, it } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import { InlineTeleport } from '@/test-utils/portal'
import ProjectMcpConsentDialog from './ProjectMcpConsentDialog.vue'

/**
 * The project-level MCP entries waiting for an answer: one row per entry, the
 * two answers on that row, and one click deciding one entry.
 */
describe('ProjectMcpConsentDialog', () => {
  beforeAll(() => setLocale('zh'))

  function mountDialog(props: { busy?: boolean; error?: string } = {}) {
    return mount(ProjectMcpConsentDialog, {
      props: {
        open: true,
        pending: [
          { name: 'docs-index', summary: 'docs-index: runs `/usr/local/bin/docs-mcp`', fingerprint: 'fp-docs' },
          { name: 'graph', summary: 'graph: connects to http://127.0.0.1:9000/mcp', fingerprint: 'fp-graph' },
        ],
        ...props,
      },
      global: { stubs: { Teleport: InlineTeleport } },
    })
  }

  it('names every entry that is waiting for an answer', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    expect(wrapper.findAll('[data-testid^="project-mcp-row-"]')).toHaveLength(2)
    expect(wrapper.text()).toContain('docs-index: runs `/usr/local/bin/docs-mcp`')
    expect(wrapper.text()).toContain('graph: connects to http://127.0.0.1:9000/mcp')
    wrapper.unmount()
  })

  it('decides one entry per click and leaves the other one alone', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    await wrapper.find('[data-testid="project-mcp-allow-docs-index"]').trigger('click')
    await flushPromises()

    const decisions = wrapper.emitted('decide') ?? []
    expect(decisions).toHaveLength(1)
    expect(decisions[0][0]).toEqual({ name: 'docs-index', allow: true })

    await wrapper.find('[data-testid="project-mcp-decline-graph"]').trigger('click')
    await flushPromises()

    expect(wrapper.emitted('decide')?.[1]?.[0]).toEqual({ name: 'graph', allow: false })
    wrapper.unmount()
  })

  it('answers nothing while a decision is in flight', async () => {
    const wrapper = mountDialog({ busy: true })
    await flushPromises()

    expect(wrapper.find('[data-testid="project-mcp-allow-docs-index"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="project-mcp-decline-docs-index"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('keeps a failed decision’s own text on screen', async () => {
    const wrapper = mountDialog({ error: '项目文件已变化，请核对后重新确认' })
    await flushPromises()

    expect(wrapper.text()).toContain('项目文件已变化，请核对后重新确认')
    wrapper.unmount()
  })

  it('closes without deciding anything when the shell is closed', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    await wrapper.find('.forebrain-modal-close').trigger('click')
    await flushPromises()

    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(wrapper.emitted('decide')).toBeUndefined()
    wrapper.unmount()
  })

  it('closes on Escape as well', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await wrapper.vm.$nextTick()

    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })
})
