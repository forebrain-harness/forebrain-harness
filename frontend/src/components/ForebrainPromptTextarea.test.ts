import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, h, nextTick } from 'vue'
import { PromptInputProvider, usePromptInput, type PromptInputContext } from '@repo/elements/prompt-input'

import { forebrainApi, type SlashCommandRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import ForebrainPromptTextarea from './ForebrainPromptTextarea.vue'

/**
 * A message the composer is given back — withdrawn before its turn began, or
 * recalled from the queue — comes back whole: its text, the files it uploaded,
 * the images the @ picker attached, and any picked file that never uploaded.
 * Everything it holds besides text is shown, and can be taken off again.
 */
describe('ForebrainPromptTextarea', () => {
  beforeAll(() => {
    setLocale('en')
    URL.createObjectURL = vi.fn(() => 'blob:composer')
    URL.revokeObjectURL = vi.fn()
  })
  afterAll(() => setLocale('en'))

  function mountComposer() {
    let context!: PromptInputContext
    const Probe = defineComponent({
      setup() {
        context = usePromptInput()
        return () => null
      },
    })
    const wrapper = mount(PromptInputProvider, {
      props: { maxFiles: 5, accept: 'image/*,application/pdf' },
      slots: { default: () => [h(ForebrainPromptTextarea), h(Probe)] },
    })
    const composer = wrapper.findComponent(ForebrainPromptTextarea)
    return { wrapper, composer, context: () => context }
  }

  it('takes a message back ahead of the draft, with everything it attached', async () => {
    const { composer, context } = mountComposer()
    const picked = new File(['x'], 'later.png', { type: 'image/png' })
    context().setTextInput('typed since')
    context().addFiles([picked])
    const unsent = new File(['y'], 'unsent.pdf', { type: 'application/pdf' })

    composer.vm.restoreSubmission({
      text: 'look at these',
      attachments: [{ fileId: 'file-1', filename: 'spec.pdf', mediaType: 'application/pdf' }],
      mentionImages: ['shots/diagram.png'],
    }, [unsent])
    await nextTick()

    expect((composer.find('textarea').element as HTMLTextAreaElement).value).toBe('look at these\ntyped since')
    // The file that never uploaded is picked again, ahead of the one picked since.
    expect(context().files.value.map((file) => file.file)).toEqual([unsent, picked])
    // Everything the message carries besides text sits in one row: the files
    // still to upload, the uploads it came back with, and its @ images.
    const chips = composer.findAll('li')
    expect(chips.map((chip) => chip.text().replace(/^Remove/, ''))).toEqual(['unsent.pdf', 'later.png', 'spec.pdf', 'diagram.png'])
    expect(chips[3].attributes('title')).toBe('shots/diagram.png')
    expect(composer.vm.hasAttached()).toBe(true)
  })

  it('attaches an image picked from the workspace', async () => {
    const accept = vi.spyOn(forebrainApi, 'mentionAccept').mockResolvedValue({ draft: 'see ', cursor: 4, imagePath: 'shots/diagram.png', keepOpen: false })
    try {
      const { composer, context } = mountComposer()
      context().setTextInput('see ')
      await nextTick()
      await composer.vm.insertWorkspacePath('shots/diagram.png')
      await nextTick()
      expect(composer.findAll('li').map((chip) => chip.text())).toEqual(['diagram.png'])
      expect(composer.vm.takeAttached().mentionImages).toEqual(['shots/diagram.png'])
    } finally {
      accept.mockRestore()
    }
  })

  it('lets each attached item be taken off, and hands the rest over once', async () => {
    const { composer } = mountComposer()
    composer.vm.restoreSubmission({
      text: '',
      attachments: [{ fileId: 'file-1', filename: '', mediaType: '' }],
      mentionImages: ['a.png', 'b.png'],
    })
    await nextTick()
    // An upload the gateway could not describe is still shown, by a generic name.
    expect(composer.findAll('li').map((chip) => chip.text())).toEqual(['Attachment', 'a.png', 'b.png'])

    await composer.find('button[aria-label="Remove a.png"]').trigger('click')
    expect(composer.findAll('li').map((chip) => chip.text())).toEqual(['Attachment', 'b.png'])

    expect(composer.vm.takeAttached()).toEqual({
      attachments: [{ fileId: 'file-1', filename: '', mediaType: '' }],
      mentionImages: ['b.png'],
    })
    await nextTick()
    expect(composer.vm.hasAttached()).toBe(false)
    expect(composer.findAll('li')).toHaveLength(0)
  })

  it('lists built-in commands and skills as two groups, and keeps the highlight in view', async () => {
    const record = (name: string, category: string): SlashCommandRecord => ({
      canonicalName: name,
      name,
      description: `${name} does its thing`,
      category,
      argumentHint: '',
      actionKind: 'local',
      allowedModes: [],
      supportsInlineArgs: false,
      availableDuringRun: true,
      availableInSideConversation: true,
      visibility: 'public',
    } as SlashCommandRecord)
    const list = vi.spyOn(forebrainApi, 'slashCommands').mockResolvedValue({
      records: [record('model', 'model'), record('mcp', 'tools'), record('model-tuning', 'skill')],
    } as Awaited<ReturnType<typeof forebrainApi.slashCommands>>)
    const scrolled = vi.fn()
    const scrollIntoView = Element.prototype.scrollIntoView
    Element.prototype.scrollIntoView = scrolled
    try {
      const { composer } = mountComposer()
      const textarea = composer.find('textarea')
      ;(textarea.element as HTMLTextAreaElement).value = '/m'
      ;(textarea.element as HTMLTextAreaElement).setSelectionRange(2, 2)
      await textarea.trigger('input')
      await vi.waitFor(() => expect(composer.findAll('[role="menuitem"]')).toHaveLength(3))
      expect(list).toHaveBeenLastCalledWith('webchat', 'm', expect.anything())
      expect(composer.findAll('.slash-group').map((group) => group.text())).toEqual(['Commands', 'Skills'])
      expect(composer.findAll('[role="menuitem"]').map((item) => item.find('.font-medium').text())).toEqual(['/model', '/mcp', '/model-tuning'])

      await textarea.trigger('keydown', { key: 'ArrowDown' })
      await textarea.trigger('keydown', { key: 'ArrowDown' })
      await nextTick()
      const items = composer.findAll('[role="menuitem"]')
      expect(items.map((item) => item.attributes('data-highlighted'))).toEqual([undefined, undefined, 'true'])
      expect(scrolled).toHaveBeenCalledWith({ block: 'nearest' })
      expect(scrolled.mock.contexts.at(-1)).toBe(items[2].element)
    } finally {
      list.mockRestore()
      Element.prototype.scrollIntoView = scrollIntoView
    }
  })
})
