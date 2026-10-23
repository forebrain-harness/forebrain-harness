import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { config, mount } from '@vue/test-utils'
import { h } from 'vue'
import { PromptInputProvider } from '@repo/elements/prompt-input'

import tip from '@/directives/tip'
import { forebrainApi, type SlashCommandRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import ComposerPlusMenu from './ComposerPlusMenu.vue'

// The app registers `tip` once (main.ts); mounting here does the same, so an
// element that names itself with it resolves instead of warning.
config.global.directives = { tip }

function skillRecord(name: string): SlashCommandRecord {
  return {
    canonicalName: name, name, description: `${name} does its thing`, category: 'skill',
    argumentHint: '', actionKind: 'local', allowedModes: [], supportsInlineArgs: false,
    availableDuringRun: true, availableInSideConversation: true, visibility: 'public',
  } as SlashCommandRecord
}

/**
 * The "+" opens the same sheet in the conversation and in a subagent's tab:
 * attachments and the skills that view may run. The goal belongs to the whole
 * conversation, so a subagent's tab does not offer it.
 */
describe('ComposerPlusMenu', () => {
  beforeAll(() => setLocale('en'))
  afterEach(() => {
    vi.restoreAllMocks()
    setLocale('en')
  })

  function mountMenu(props: Record<string, unknown> = {}) {
    const list = vi.spyOn(forebrainApi, 'slashCommands').mockResolvedValue({
      records: [skillRecord('plan-writing')],
    } as Awaited<ReturnType<typeof forebrainApi.slashCommands>>)
    const wrapper = mount(PromptInputProvider, {
      props: { maxFiles: 5, accept: 'image/*,application/pdf' },
      slots: { default: () => h(ComposerPlusMenu, props) },
      global: { directives: { tip } },
    })
    return { wrapper, menu: wrapper.findComponent(ComposerPlusMenu), list }
  }

  it('offers the goal, the attachment and the skills to the conversation', async () => {
    const { menu, list } = mountMenu()
    await menu.get('[data-testid="composer-plus"]').trigger('click')
    expect(menu.find('[data-testid="plus-goal"]').exists()).toBe(true)
    expect(menu.find('[data-testid="plus-attach"]').exists()).toBe(true)
    expect(menu.find('[data-testid="plus-skill-plan-writing"]').exists()).toBe(true)
    expect(list).toHaveBeenCalledWith('webchat', '', {})
  })

  it('leaves the goal out of a subagent tab and asks for its own skills', async () => {
    const { menu, list } = mountMenu({ subagentView: true })
    await menu.get('[data-testid="composer-plus"]').trigger('click')
    expect(menu.find('[data-testid="plus-goal"]').exists()).toBe(false)
    expect(menu.find('[data-testid="plus-attach"]').exists()).toBe(true)
    expect(menu.find('[data-testid="plus-skill-plan-writing"]').exists()).toBe(true)
    expect(list).toHaveBeenCalledWith('webchat', '', { subagentView: true })
  })

  it('inserts the picked skill’s command and closes the sheet', async () => {
    const { menu } = mountMenu({ subagentView: true })
    await menu.get('[data-testid="composer-plus"]').trigger('click')
    await menu.get('[data-testid="plus-skill-plan-writing"]').trigger('click')
    expect(menu.emitted('insert')).toEqual([['/plan-writing ']])
    expect(menu.find('[data-testid="plus-attach"]').exists()).toBe(false)
  })

  // The sheet opens above the "+" that opened it — its containing block is the
  // button's own wrapper — not above the whole composer, and its width is set
  // for it (a menu, not the composer's width).
  it('anchors the sheet to the plus button and sizes it itself', async () => {
    const { menu } = mountMenu()
    const button = menu.get('[data-testid="composer-plus"]')
    const wrapper = button.element.parentElement as HTMLElement
    expect(wrapper.classList.contains('relative')).toBe(true)
    await button.trigger('click')
    const sheet = menu.get('.forebrain-plus-pop').element as HTMLElement
    expect(wrapper.contains(sheet)).toBe(true)
    expect(sheet.style.width).toBe('440px')
  })
})
