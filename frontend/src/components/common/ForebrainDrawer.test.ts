import { beforeAll, describe, expect, it } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import tip from '@/directives/tip'
import { setLocale } from '@/locales'
import { InlineTeleport } from '@/test-utils/portal'
import ForebrainDrawer from './ForebrainDrawer.vue'
import ForebrainModal from './ForebrainModal.vue'

/** The right-hand drawer: the stack rule it shares with a modal, and its badge. */
describe('ForebrainDrawer', () => {
  beforeAll(() => setLocale('en'))

  function mountDrawer(props: Record<string, unknown> = {}, attach = false) {
    return mount(ForebrainDrawer, {
      props: { open: true, title: 'MCP server', ...props },
      global: { stubs: { teleport: InlineTeleport }, directives: { tip } },
      ...(attach ? { attachTo: document.body } : {}),
    })
  }

  function pressEscape() {
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
  }

  it('renders nothing while closed and the panel while open', async () => {
    const wrapper = mountDrawer({ open: false })
    expect(wrapper.find('.forebrain-drawer').exists()).toBe(false)
    await wrapper.setProps({ open: true })
    const panel = wrapper.find('.forebrain-drawer')
    expect(panel.exists()).toBe(true)
    expect(panel.attributes('role')).toBe('dialog')
    expect(panel.attributes('aria-label')).toBe('MCP server')
    expect(wrapper.find('.forebrain-drawer-head b').text()).toBe('MCP server')
    wrapper.unmount()
  })

  it('closes on Escape and returns focus to its opener', async () => {
    const opener = document.createElement('button')
    document.body.append(opener)
    opener.focus()
    const wrapper = mountDrawer()
    pressEscape()
    await wrapper.vm.$nextTick()
    expect(wrapper.emitted('close')).toHaveLength(1)
    await wrapper.setProps({ open: false })
    expect(document.activeElement).toBe(opener)
    opener.remove()
    wrapper.unmount()
  })

  it('closes on a mask click', async () => {
    // The scrim is the library's own sheet overlay, which reports an outside
    // press as a pointerdown; the panel is attached so that press reaches the
    // document the library listens on.
    const wrapper = mountDrawer({}, true)
    await new Promise((resolve) => setTimeout(resolve, 0))
    const overlays = document.querySelectorAll('[data-slot="sheet-overlay"]')
    overlays[overlays.length - 1].dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await flushPromises()
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })

  it('lets a modal raised over it close alone on Escape', async () => {
    const drawer = mountDrawer()
    const modal = mount(ForebrainModal, {
      props: { open: true, title: 'Delete this server?' },
      global: { stubs: { teleport: InlineTeleport } },
    })
    pressEscape()
    await modal.vm.$nextTick()
    expect(modal.emitted('close')).toHaveLength(1)
    expect(drawer.emitted('close')).toBeUndefined()
    modal.unmount()
    drawer.unmount()
  })

  it('marks a read-only record and draws the footer only when given one', () => {
    const plain = mountDrawer()
    expect(plain.find('.forebrain-drawer-head .forebrain-badge').exists()).toBe(false)
    expect(plain.find('.forebrain-drawer-foot').exists()).toBe(false)
    plain.unmount()

    const readonly = mountDrawer({ readonly: true, title: 'MCP server github' })
    expect(readonly.find('.forebrain-drawer-head .forebrain-badge').text()).toBe('Read-only')
    readonly.unmount()

    const footed = mount(ForebrainDrawer, {
      props: { open: true, title: 'Edit' },
      slots: { footer: '<button type="button" class="probe">Save</button>' },
      global: { stubs: { teleport: InlineTeleport }, directives: { tip } },
    })
    expect(footed.find('.forebrain-drawer-foot .probe').exists()).toBe(true)
    footed.unmount()
  })
})
