import { beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import ConfirmDialog from './ConfirmDialog.vue'

/** The confirmation dialog, including the retype-to-confirm gate. */
describe('ConfirmDialog', () => {
  beforeAll(() => setLocale('en'))

  function mountDialog(props: Record<string, unknown>) {
    return mount(ConfirmDialog, {
      props: { open: true, title: '删除主代理 docs？', message: '删除后不能恢复。', confirmLabel: 'Delete', ...props },
      global: { stubs: { teleport: true } },
    })
  }

  it('shows the message and confirms', async () => {
    const wrapper = mountDialog({})
    expect(wrapper.find('.forebrain-confirm-message').text()).toBe('删除后不能恢复。')
    const confirm = wrapper.find('[data-testid="confirm-ok"]')
    expect((confirm.element as HTMLButtonElement).disabled).toBe(false)
    await confirm.trigger('click')
    expect(wrapper.emitted('confirm')).toHaveLength(1)
  })

  it('keeps confirm disabled until the typed text matches', async () => {
    const wrapper = mountDialog({ typedConfirm: 'forebrain-harness' })
    const isDisabled = () => (wrapper.find('[data-testid="confirm-ok"]').element as HTMLButtonElement).disabled
    expect(isDisabled()).toBe(true)
    await wrapper.find('input').setValue('forebrain')
    expect(isDisabled()).toBe(true)
    await wrapper.find('input').setValue('forebrain-harness')
    expect(isDisabled()).toBe(false)
  })

  it('emits cancel from the cancel button', async () => {
    const wrapper = mountDialog({})
    await wrapper.findAll('.forebrain-modal-foot button')[0].trigger('click')
    expect(wrapper.emitted('cancel')).toHaveLength(1)
  })
})
