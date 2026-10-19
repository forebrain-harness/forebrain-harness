import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, h, nextTick } from 'vue'
import { PromptInputProvider, usePromptInput, type PromptInputContext } from '@repo/elements/prompt-input'

import tip from '@/directives/tip'
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

  function mountComposer(props: Record<string, unknown> = {}) {
    let context!: PromptInputContext
    const Probe = defineComponent({
      setup() {
        context = usePromptInput()
        return () => null
      },
    })
    const wrapper = mount(PromptInputProvider, {
      props: { maxFiles: 5, accept: 'image/*,application/pdf' },
      slots: { default: () => [h(ForebrainPromptTextarea, props), h(Probe)] },
      global: { directives: { tip } },
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
    expect(chips[3].attributes('title')).toBeUndefined()
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
      expect(scrolled.mock.contexts[scrolled.mock.contexts.length - 1]).toBe(items[2].element)
    } finally {
      list.mockRestore()
      Element.prototype.scrollIntoView = scrollIntoView
    }
  })
  // Typing, and Escape with no run to interrupt, are how the reader stops a
  // continuation waiting on a usage limit; a message restored into the
  // composer is not the reader typing and must not stop it.
  it('reports the reader typing and an idle Escape', async () => {
    const { composer } = mountComposer()
    composer.vm.restoreSubmission({ text: 'restored', attachments: [], mentionImages: [] }, [])
    await nextTick()
    expect(composer.emitted('typed')).toBeUndefined()

    const textarea = composer.find('textarea')
    await textarea.setValue('a')
    expect(composer.emitted('typed')).toHaveLength(1)

    await textarea.trigger('keydown', { key: 'Escape' })
    expect(composer.emitted('escape')).toHaveLength(1)
    expect(composer.emitted('interrupt-run')).toBeUndefined()
  })

  it('recalls the newest queued message on Shift+← whenever the queue shows one', async () => {
    const shown = mountComposer({ queueVisible: true })
    await shown.composer.find('textarea').trigger('keydown', { key: 'ArrowLeft', shiftKey: true })
    expect(shown.composer.emitted('edit-last-queued')).toHaveLength(1)

    const empty = mountComposer()
    await empty.composer.find('textarea').trigger('keydown', { key: 'ArrowLeft', shiftKey: true })
    expect(empty.composer.emitted('edit-last-queued')).toBeUndefined()
  })

  // A "/" that is not the start of the message lists only skills, and an accept
  // replaces just the token — the rest of the message survives.
  it('lists only skills mid-message and replaces just the token', async () => {
    const record = (name: string, category: string, supportsInlineArgs: boolean): SlashCommandRecord => ({
      canonicalName: name, name, description: `${name} does its thing`, category,
      argumentHint: '', actionKind: 'local', allowedModes: [], supportsInlineArgs,
      availableDuringRun: true, availableInSideConversation: true, visibility: 'public',
    } as SlashCommandRecord)
    const list = vi.spyOn(forebrainApi, 'slashCommands').mockResolvedValue({
      records: [record('plan', 'mode', true), record('model-tuning', 'skill', true)],
    } as Awaited<ReturnType<typeof forebrainApi.slashCommands>>)
    try {
      const { composer, context } = mountComposer()
      const textarea = composer.find('textarea')
      ;(textarea.element as HTMLTextAreaElement).value = '看下 /m'
      ;(textarea.element as HTMLTextAreaElement).setSelectionRange(5, 5)
      await textarea.trigger('input')
      await vi.waitFor(() => expect(composer.findAll('[role="menuitem"]')).toHaveLength(1))
      // The built-in command is filtered out; only the skill remains.
      expect(composer.findAll('[role="menuitem"]').map((item) => item.find('.font-medium').text())).toEqual([
        '/model-tuning',
      ])
      await textarea.trigger('keydown', { key: 'Enter' })
      await nextTick()
      expect(context().textInput.value).toBe('看下 /model-tuning ')
    } finally {
      list.mockRestore()
    }
  })

  // A command typed in front of a message already in the composer completes in
  // place: the message stays, as the command's arguments.
  it('keeps the message a leading command was typed in front of', async () => {
    const list = vi.spyOn(forebrainApi, 'slashCommands').mockResolvedValue({
      records: [{
        canonicalName: 'plan', name: 'plan', description: 'plan does its thing', category: 'mode',
        argumentHint: '', actionKind: 'local', allowedModes: [], supportsInlineArgs: false,
        availableDuringRun: true, availableInSideConversation: true, visibility: 'public',
      } as SlashCommandRecord],
    } as Awaited<ReturnType<typeof forebrainApi.slashCommands>>)
    try {
      const { composer, context } = mountComposer()
      const textarea = composer.find('textarea')
      ;(textarea.element as HTMLTextAreaElement).value = '/pl commit 1527'
      ;(textarea.element as HTMLTextAreaElement).setSelectionRange(3, 3)
      await textarea.trigger('input')
      await vi.waitFor(() => expect(composer.findAll('[role="menuitem"]')).toHaveLength(1))
      expect(list).toHaveBeenLastCalledWith('webchat', 'pl', expect.anything())
      await textarea.trigger('keydown', { key: 'Tab' })
      await nextTick()
      expect(context().textInput.value).toBe('/plan commit 1527')
    } finally {
      list.mockRestore()
    }
  })

  // A subagent's tab lists the commands that view may run, so the composer
  // says which view it belongs to on every "/" request.
  it('asks for a subagent view’s commands when it belongs to a subagent tab', async () => {
    const list = vi.spyOn(forebrainApi, 'slashCommands').mockResolvedValue({
      records: [{
        canonicalName: 'compact', name: 'compact', description: 'compact does its thing', category: 'subagent',
        argumentHint: '', actionKind: 'local', allowedModes: [], supportsInlineArgs: false,
        availableDuringRun: true, availableInSideConversation: true, visibility: 'public',
      } as SlashCommandRecord],
    } as Awaited<ReturnType<typeof forebrainApi.slashCommands>>)
    try {
      const { composer } = mountComposer({ subagentView: true })
      const textarea = composer.find('textarea')
      ;(textarea.element as HTMLTextAreaElement).value = '/'
      ;(textarea.element as HTMLTextAreaElement).setSelectionRange(1, 1)
      await textarea.trigger('input')
      await vi.waitFor(() => expect(composer.findAll('[role="menuitem"]')).toHaveLength(1))
      expect(list).toHaveBeenLastCalledWith('webchat', '', expect.objectContaining({ subagentView: true }))
    } finally {
      list.mockRestore()
    }
  })

  // A mid-word "@" (foo@bar) is not a mention: neither picker opens.
  it('ignores a mid-word @', async () => {
    const search = vi.spyOn(forebrainApi, 'mentionSearch').mockResolvedValue({ query: '', records: [] })
    try {
      const { composer } = mountComposer()
      const textarea = composer.find('textarea')
      ;(textarea.element as HTMLTextAreaElement).value = 'foo@bar'
      ;(textarea.element as HTMLTextAreaElement).setSelectionRange(7, 7)
      await textarea.trigger('input')
      await nextTick()
      expect(composer.findAll('[role="menu"]')).toHaveLength(0)
      expect(search).not.toHaveBeenCalled()
    } finally {
      search.mockRestore()
    }
  })

  // The @ picker resolves against the conversation's file view, so its scope
  // rides every candidate request: the composer is told which project or
  // session it belongs to and passes it straight through.
  it('resolves the @ picker against the conversation scope', async () => {
    const search = vi.spyOn(forebrainApi, 'mentionSearch').mockResolvedValue({ query: '', records: [] })
    try {
      const { composer } = mountComposer({ scope: { sessionId: 's1' } })
      const textarea = composer.find('textarea')
      ;(textarea.element as HTMLTextAreaElement).value = '@'
      ;(textarea.element as HTMLTextAreaElement).setSelectionRange(1, 1)
      await textarea.trigger('input')
      await textarea.trigger('keydown', { key: 'ArrowDown' })
      await textarea.trigger('keydown', { key: 'Enter' })
      await vi.waitFor(() => expect(search).toHaveBeenCalled())
      expect(search).toHaveBeenLastCalledWith('', { sessionId: 's1' })
    } finally {
      search.mockRestore()
    }
  })
})
