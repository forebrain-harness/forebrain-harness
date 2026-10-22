import { beforeAll, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import ForebrainModal from './ForebrainModal.vue'

/** The shared modal shell: sizes, the close affordances and the stack rule. */
describe('ForebrainModal', () => {
  beforeAll(() => setLocale('en'))

  // The shell is the library's Dialog, which portals the panel into the
  // document; the wrapper no longer holds it.
  function panel(): HTMLElement | null {
    return document.querySelector('.forebrain-modal')
  }

  function mountModal(open: boolean, title: string) {
    return mount(ForebrainModal, { props: { open, title } })
  }

  /**
   * A press on the scrim — the shell's own mask, not the panel — which is what
   * the shell treats as "click the mask".
   */
  async function pressOutside() {
    // The panel appears once the library's portal mounts, and the library arms
    // its document listener a task after that.
    await new Promise((resolve) => setTimeout(resolve, 0))
    await flushPromises()
    const masks = document.querySelectorAll('.forebrain-modal-mask')
    masks[masks.length - 1].dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await flushPromises()
  }

  it('renders nothing while closed and the panel while open', async () => {
    const wrapper = mountModal(false, '新建主代理')
    expect(panel()).toBeNull()
    await wrapper.setProps({ open: true })
    expect(panel()).not.toBeNull()
    expect(document.querySelector('.forebrain-modal-title')?.textContent?.trim()).toBe('新建主代理')
    wrapper.unmount()
  })

  it('emits close when the mask is clicked, but only for the top modal', async () => {
    const bottom = mountModal(true, 'bottom')
    const top = mountModal(true, 'top')
    await pressOutside()
    expect(bottom.emitted('close')).toBeUndefined()
    expect(top.emitted('close')).toHaveLength(1)
    bottom.unmount()
    top.unmount()
  })

  it('closes only the topmost modal on Escape', async () => {
    const bottom = mountModal(true, 'bottom')
    const top = mountModal(true, 'top')
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await bottom.vm.$nextTick()
    expect(top.emitted('close')).toHaveLength(1)
    expect(bottom.emitted('close')).toBeUndefined()
    bottom.unmount()
    top.unmount()
  })

  it('returns focus to the opener when it closes', async () => {
    const opener = document.createElement('button')
    document.body.append(opener)
    opener.focus()
    const wrapper = mountModal(true, 'panel')
    // The panel is portalled, so it lands in the document a tick after mount.
    await flushPromises()
    expect(panel()).not.toBeNull()
    await wrapper.setProps({ open: false })
    // Focus comes back once the panel is gone from the document.
    await flushPromises()
    expect(document.activeElement).toBe(opener)
    opener.remove()
    wrapper.unmount()
  })

  it('registers and unregisters its Escape listener', () => {
    const add = vi.spyOn(document, 'addEventListener')
    const remove = vi.spyOn(document, 'removeEventListener')
    const wrapper = mountModal(true, 'panel')
    expect(add).toHaveBeenCalledWith('keydown', expect.any(Function), true)
    wrapper.unmount()
    expect(remove).toHaveBeenCalledWith('keydown', expect.any(Function), true)
    add.mockRestore()
    remove.mockRestore()
  })
})
