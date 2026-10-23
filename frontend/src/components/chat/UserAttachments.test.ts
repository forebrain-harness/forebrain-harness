import { afterEach, beforeAll, describe, expect, it } from 'vitest'
import { config, flushPromises, mount } from '@vue/test-utils'

import tip from '@/directives/tip'
import { setLocale } from '@/locales'
import UserAttachments from './UserAttachments.vue'

config.global.directives = { tip }

/**
 * What a user message attached: each chip downloads by its name and previews by
 * its mark, whether it is an upload (opened by its file id) or a screenshot the
 * terminal pasted into the project (opened by the message that holds it).
 */
describe('UserAttachments', () => {
  beforeAll(() => setLocale('en'))
  afterEach(() => {
    document.body.innerHTML = ''
  })

  const upload = { fileId: 'f1', name: 'shot.png', mediaType: 'image/png' }
  const pasted = { path: 'forebrain-clipboard-1.png', name: 'forebrain-clipboard-1.png', mediaType: 'image/png' }

  function mountChips(attachments: unknown[], props: Record<string, unknown> = {}) {
    return mount(UserAttachments, {
      props: { attachments: attachments as never, sessionId: 's1', rowId: 42, ...props },
      attachTo: document.body,
    })
  }

  it('downloads an upload by its name', () => {
    const wrapper = mountChips([upload])
    const link = wrapper.get('[data-testid="attachment-download"]')
    expect(link.attributes('href')).toBe('/api/files/f1/download')
    expect(link.attributes('download')).toBe('shot.png')
    expect(link.text()).toBe('shot.png')
    wrapper.unmount()
  })

  it('addresses a path-recorded screenshot by its message and place in it', () => {
    const wrapper = mountChips([upload, pasted])
    const links = wrapper.findAll('[data-testid="attachment-download"]')
    expect(links[1]?.attributes('href')).toBe('/api/chat/sessions/s1/messages/42/attachments/1')
    // The mark shows the screenshot itself, drawn in place.
    const thumbs = wrapper.findAll('.u-att-thumb')
    expect(thumbs[1]?.attributes('src')).toBe('/api/chat/sessions/s1/messages/42/attachments/1?disposition=inline')
    wrapper.unmount()
  })

  it('previews on the mark: the file drawn in place, with the download beside it', async () => {
    const wrapper = mountChips([upload])
    await wrapper.get('[data-testid="attachment-preview"]').trigger('click')
    await flushPromises()
    const dialog = document.querySelector('[data-testid="attachment-preview-dialog"]')
    expect(dialog).not.toBeNull()
    expect(dialog?.querySelector('img')?.getAttribute('src')).toBe('/api/files/f1/download?disposition=inline')
    expect(document.querySelector('[data-testid="attachment-preview-download"]')?.getAttribute('href')).toBe('/api/files/f1/download')
    wrapper.unmount()
  })

  it('previews a PDF in a frame and says plainly when a type has nothing to show', async () => {
    const pdf = mountChips([{ fileId: 'f2', name: 'spec.pdf', mediaType: 'application/pdf' }])
    await pdf.get('[data-testid="attachment-preview"]').trigger('click')
    await flushPromises()
    expect(document.querySelector('[data-testid="attachment-preview-dialog"] iframe')?.getAttribute('src')).toBe('/api/files/f2/download?disposition=inline')
    pdf.unmount()

    const other = mountChips([{ fileId: 'f3', name: 'data.bin', mediaType: 'application/octet-stream' }])
    await other.get('[data-testid="attachment-preview"]').trigger('click')
    await flushPromises()
    expect(document.querySelector('[data-testid="attachment-preview-dialog"]')?.textContent).toContain('No preview for this type')
    other.unmount()
  })

  it('is a plain label when nothing can name the file', () => {
    // A path-recorded attachment of a message with no row of its own (just sent).
    const wrapper = mountChips([pasted], { rowId: undefined })
    expect(wrapper.find('[data-testid="attachment-download"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="attachment-preview"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('forebrain-clipboard-1.png')
    wrapper.unmount()
  })

  it('stops offering a file that fails to load', async () => {
    const wrapper = mountChips([pasted])
    await wrapper.get('.u-att-thumb').trigger('error')
    expect(wrapper.find('[data-testid="attachment-download"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="attachment-preview"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
