import { beforeAll, describe, expect, it } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import { InlineTeleport } from '@/test-utils/portal'
import ProjectServersConsentDialog from './ProjectServersConsentDialog.vue'

/**
 * The project's undecided MCP and language-server entries on one panel: every
 * entry opens checked, the two linkage rules the TUI panel has, one button
 * answering the whole list, and a close that answers the same way.
 */
describe('ProjectServersConsentDialog', () => {
  beforeAll(() => setLocale('zh'))

  const mcp = [
    { name: 'caveman', detail: 'npx -y caveman-shrink', fingerprint: 'fp-caveman' },
    { name: 'webtool', detail: 'https://api.example.com/mcp', fingerprint: 'fp-webtool' },
  ]
  const lsp = [{ name: 'gopls', detail: 'gopls serve', fingerprint: 'fp-gopls' }]

  function mountDialog(props: { busy?: boolean; error?: string } = {}) {
    return mount(ProjectServersConsentDialog, {
      props: { open: true, mcp, lsp, ...props },
      global: { stubs: { Teleport: InlineTeleport } },
    })
  }

  const box = (wrapper: ReturnType<typeof mountDialog>, testid: string) =>
    wrapper.find(`[data-testid="${testid}"]`)

  it('names both groups and opens with every entry checked and the switch off', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    expect(wrapper.text()).toContain('3 个服务要在本项目运行')
    expect(wrapper.text()).toContain('MCP')
    expect(wrapper.text()).toContain('语言服务器')
    expect(wrapper.text()).toContain('caveman')
    expect(wrapper.text()).toContain('npx -y caveman-shrink')
    expect(wrapper.text()).toContain('gopls serve')
    expect(wrapper.findAll('[data-testid^="project-servers-row-"]')).toHaveLength(3)
    for (const name of ['caveman', 'webtool', 'gopls']) {
      expect(box(wrapper, `project-servers-check-${name}`).attributes('data-state')).toBe('checked')
    }
    expect(box(wrapper, 'project-servers-always').attributes('data-state')).toBe('unchecked')
    wrapper.unmount()
  })

  it('links the switch and the entries both ways, as the TUI panel does', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    // Unchecking an entry turns the switch off with it.
    await box(wrapper, 'project-servers-always').trigger('click')
    expect(box(wrapper, 'project-servers-always').attributes('data-state')).toBe('checked')
    await box(wrapper, 'project-servers-check-webtool').trigger('click')
    expect(box(wrapper, 'project-servers-check-webtool').attributes('data-state')).toBe('unchecked')
    expect(box(wrapper, 'project-servers-always').attributes('data-state')).toBe('unchecked')

    // The switch back on checks every entry again.
    await box(wrapper, 'project-servers-always').trigger('click')
    for (const name of ['caveman', 'webtool', 'gopls']) {
      expect(box(wrapper, `project-servers-check-${name}`).attributes('data-state')).toBe('checked')
    }
    wrapper.unmount()
  })

  it('answers the whole list as shown when 确定 is clicked', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    await box(wrapper, 'project-servers-check-webtool').trigger('click')
    await box(wrapper, 'project-servers-always').trigger('click')
    await box(wrapper, 'project-servers-confirm').trigger('click')

    const answers = wrapper.emitted('confirm') ?? []
    expect(answers).toHaveLength(1)
    expect(answers[0][0]).toEqual({
      allowed: { caveman: true, webtool: true, gopls: true },
      alwaysAllow: true,
    })
    wrapper.unmount()
  })

  it('declines every listed entry when 全部不允许 is clicked', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    await box(wrapper, 'project-servers-decline-all').trigger('click')

    expect(wrapper.emitted('declineAll')).toHaveLength(1)
    // The dialog itself decides nothing: the caller records the decline.
    expect(wrapper.emitted('confirm')).toBeUndefined()
    wrapper.unmount()
  })

  it('answers nothing while an answer is in flight', async () => {
    const wrapper = mountDialog({ busy: true })
    await flushPromises()

    expect(box(wrapper, 'project-servers-confirm').attributes('disabled')).toBeDefined()
    expect(box(wrapper, 'project-servers-decline-all').attributes('disabled')).toBeDefined()
    expect(box(wrapper, 'project-servers-check-caveman').attributes('disabled')).toBeDefined()
    expect(box(wrapper, 'project-servers-always').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('keeps a failed answer’s own text on screen', async () => {
    const wrapper = mountDialog({ error: '项目文件已变化，请核对后重新确认' })
    await flushPromises()

    expect(wrapper.text()).toContain('项目文件已变化，请核对后重新确认')
    wrapper.unmount()
  })

  it('closes through the shell — the same decline, by the caller’s wiring', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    await wrapper.find('.forebrain-modal-close').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await wrapper.vm.$nextTick()
    expect(wrapper.emitted('close')).toHaveLength(2)
    wrapper.unmount()
  })
})
