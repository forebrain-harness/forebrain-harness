import { beforeAll, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import ForebrainModal from './ForebrainModal.vue'

/** The shared modal shell: sizes, the close affordances and the stack rule. */
describe('ForebrainModal', () => {
  beforeAll(() => setLocale('en'))

  function mountModal(open: boolean, title: string) {
    return mount(ForebrainModal, {
      props: { open, title },
      global: { stubs: { teleport: true } },
    })
  }

  it('renders nothing while closed and the panel while open', async () => {
    const wrapper = mountModal(false, '新建主代理')
    expect(wrapper.find('.forebrain-modal').exists()).toBe(false)
    await wrapper.setProps({ open: true })
    expect(wrapper.find('.forebrain-modal').exists()).toBe(true)
    expect(wrapper.find('.forebrain-modal-title').text()).toBe('新建主代理')
  })

  it('emits close when the mask is clicked, but only for the top modal', async () => {
    const bottom = mountModal(true, 'bottom')
    const top = mountModal(true, 'top')
    await bottom.find('.forebrain-modal-mask').trigger('click')
    expect(bottom.emitted('close')).toBeUndefined()
    await top.find('.forebrain-modal-mask').trigger('click')
    expect(top.emitted('close')).toHaveLength(1)
  })

  it('closes only the topmost modal on Escape', async () => {
    const bottom = mountModal(true, 'bottom')
    const top = mountModal(true, 'top')
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await bottom.vm.$nextTick()
    expect(top.emitted('close')).toHaveLength(1)
    expect(bottom.emitted('close')).toBeUndefined()
  })

  it('returns focus to the opener when it closes', async () => {
    const opener = document.createElement('button')
    document.body.append(opener)
    opener.focus()
    const wrapper = mountModal(true, 'panel')
    expect(wrapper.find('.forebrain-modal').exists()).toBe(true)
    await wrapper.setProps({ open: false })
    expect(document.activeElement).toBe(opener)
    opener.remove()
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
