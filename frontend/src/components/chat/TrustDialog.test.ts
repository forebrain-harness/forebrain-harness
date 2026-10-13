import { beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import TrustDialog from './TrustDialog.vue'

/** The one-time trust prompt: the folder, the project it would create or open,
 * and the two answers. */
describe('TrustDialog', () => {
  beforeAll(() => setLocale('en'))

  it('names the folder and the project it will create', () => {
    const wrapper = mount(TrustDialog, {
      props: { target: { folder: '/work/photo', name: 'photo', root: '/work/photo', isNew: true, trustLevel: 'unknown' } },
    })
    expect(wrapper.find('.modal-h').text()).toBe('Trust this folder?')
    expect(wrapper.text()).toContain('/work/photo')
    expect(wrapper.text()).toContain('Will create “photo”')
    expect(wrapper.text()).toContain('Asked once')
  })

  it('names an existing project by its name', () => {
    const wrapper = mount(TrustDialog, {
      props: { target: { projectId: 'p1', name: 'alpha', root: '/work/alpha', isNew: false, trustLevel: 'unknown' } },
    })
    expect(wrapper.text()).toContain('alpha')
    expect(wrapper.text()).not.toContain('Will create')
  })

  it('emits confirm and cancel', async () => {
    const wrapper = mount(TrustDialog, {
      props: { target: { folder: '/work/x', name: 'x', root: '/work/x', isNew: true, trustLevel: 'unknown' } },
    })
    const buttons = wrapper.findAll('.modal-f .btn')
    await buttons[1].trigger('click')
    expect(wrapper.emitted('confirm')).toBeTruthy()
    await buttons[0].trigger('click')
    expect(wrapper.emitted('cancel')).toBeTruthy()
  })
})
