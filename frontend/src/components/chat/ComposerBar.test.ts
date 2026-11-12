import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { config, flushPromises, mount } from '@vue/test-utils'
import tip from '@/directives/tip'
import { h, ref } from 'vue'

import { PromptInputProvider } from '@repo/elements/prompt-input'
import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'
import { usePendingActions } from '@/composables/usePendingActions'
import { PendingActionsKey } from '@/composables/providePendingActions'
import ComposerPlusMenu from './ComposerPlusMenu.vue'
import ModeChip from './ModeChip.vue'
import EffortChip from './EffortChip.vue'
import ContextRing from './ContextRing.vue'
import McpNotice from './McpNotice.vue'
import ApprovalCard from './ApprovalCard.vue'

// The app registers `tip` once (main.ts); mounting here does the same, so an
// element that names itself with it resolves instead of warning.
config.global.directives = { tip }

/**
 * The composer's new bottom bar, one test per rule the plan sets: the "+"
 * menu inserts the goal command and the session's skills, the mode chip is
 * disabled while a run is going, the context ring turns at 80%, the effort
 * chip offers only the levels the catalog reports, and the MCP notice keeps
 * the raw error behind "查看详情".
 */

beforeAll(() => {
  setLocale('zh')
})
afterAll(async () => {
  await vi.dynamicImportSettled()
  setLocale('en')
})
afterEach(() => vi.restoreAllMocks())

describe('ComposerPlusMenu', () => {
  // The attachments entry goes through the composer's own provider, exactly
  // as it sits in the page's footer.
  function mountMenu() {
    return mount(PromptInputProvider, {
      slots: { default: () => h(ComposerPlusMenu) },
      props: { onSubmit: () => {} },
    })
  }

  it('inserts /goal and a picked skill as commands', async () => {
    vi.spyOn(forebrainApi, 'slashCommands').mockResolvedValue({
      surface: 'webchat',
      query: '',
      options: { duringRun: false, sideConversation: false },
      records: [
        { canonicalName: 'goal', name: 'goal', description: '目标', category: 'command', argumentHint: '', actionKind: '', allowedModes: [], supportsInlineArgs: true, availableDuringRun: true, availableInSideConversation: true, visibility: '' },
        { canonicalName: 'improve', name: 'improve', description: '审查计划', category: 'skill', argumentHint: '', actionKind: '', allowedModes: [], supportsInlineArgs: true, availableDuringRun: true, availableInSideConversation: true, visibility: '' },
      ],
    } as never)
    const wrapper = mountMenu()
    await flushPromises()

    const menu = wrapper.findComponent(ComposerPlusMenu)
    await menu.find('[data-testid="composer-plus"]').trigger('click')
    await menu.find('[data-testid="plus-goal"]').trigger('click')
    await menu.find('[data-testid="composer-plus"]').trigger('click')
    await menu.find('[data-testid="plus-skill-improve"]').trigger('click')

    expect(menu.emitted('insert')).toEqual([['/goal '], ['/improve ']])
    wrapper.unmount()
  })
})

describe('ModeChip', () => {
  it('is disabled while a run is going and selects through the page', async () => {
    const wrapper = mount(ModeChip, { props: { mode: 'agent', disabled: true } })
    expect(wrapper.find('[data-testid="mode-chip"]').attributes('disabled')).toBeDefined()

    await wrapper.setProps({ disabled: false })
    await wrapper.find('[data-testid="mode-chip"]').trigger('click')
    await wrapper.find('[data-testid="mode-option-plan"]').trigger('click')
    expect(wrapper.emitted('select')).toEqual([['plan']])
    wrapper.unmount()
  })
})

describe('ContextRing', () => {
  it('stays quiet below 80 and names its percentage at or above it', () => {
    const calm = mount(ContextRing, { props: { percent: 79 } })
    expect(calm.find('.forebrain-ctx-ring-pc').exists()).toBe(false)
    calm.unmount()

    const warn = mount(ContextRing, { props: { percent: 86 } })
    expect(warn.find('.forebrain-ctx-ring-pc').text()).toBe('86%')
    warn.unmount()
  })
})

describe('EffortChip', () => {
  async function openMenu(props: { effort: string; efforts: string[]; defaultEffort?: string }) {
    const wrapper = mount(EffortChip, { props })
    await wrapper.find('[data-testid="effort-chip"]').trigger('click')
    return wrapper
  }
  const ids = (wrapper: Awaited<ReturnType<typeof openMenu>>) =>
    wrapper.findAll('.forebrain-chip-menu-opt').map((option) => option.attributes('data-testid'))

  // The model page configures a level: the menu is those levels alone, in the
  // project's own order, with the configured one marked — and no 「默认」 row,
  // because default-styled placeholders are not offered (plan 300, and the
  // owner's own rule that the value in force is pre-selected instead).
  it('offers the model’s levels when the model is configured with one', async () => {
    const wrapper = await openMenu({ effort: 'high', efforts: ['high', 'low'], defaultEffort: 'high' })

    expect(ids(wrapper)).toEqual(['effort-option-low', 'effort-option-high'])
    expect(wrapper.find('[data-testid="effort-option-default"]').exists()).toBe(false)
    const tags = wrapper.findAll('[data-testid="effort-setting-tag"]')
    expect(tags).toHaveLength(1)
    expect(tags[0].element.closest('[role="option"]')!.getAttribute('data-testid')).toBe('effort-option-high')
    expect(wrapper.find('[data-testid="effort-option-high"]').attributes('aria-selected')).toBe('true')

    await wrapper.find('[data-testid="effort-option-low"]').trigger('click')
    expect(wrapper.emitted('select')).toEqual([['low']])
    wrapper.unmount()
  })

  // A model that configures no level: 「默认」 is what the provider will apply,
  // so it is a real row, selected, and nothing claims the model setting.
  it('offers 「默认」 when the model configures no level', async () => {
    const wrapper = await openMenu({ effort: '', efforts: ['low', 'high'], defaultEffort: '' })

    expect(ids(wrapper)).toEqual(['effort-option-default', 'effort-option-low', 'effort-option-high'])
    expect(wrapper.find('[data-testid="effort-option-default"]').attributes('aria-selected')).toBe('true')
    expect(wrapper.find('[data-testid="effort-setting-tag"]').exists()).toBe(false)
    wrapper.unmount()
  })

  // A conversation whose row stores no level keeps reading 「默认」 even after
  // the model page configured one: the row is what it runs on, and history is
  // not rewritten (the setting only applies at the moment a model is picked).
  it('keeps showing a stored empty level as 「默认」', async () => {
    const wrapper = await openMenu({ effort: '', efforts: ['low', 'high'], defaultEffort: 'high' })

    expect(wrapper.find('[data-testid="effort-option-default"]').attributes('aria-selected')).toBe('true')
    expect(wrapper.find('[data-testid="effort-option-high"] [data-testid="effort-setting-tag"]').exists()).toBe(true)
    wrapper.unmount()
  })

  // A model that allows turning thinking off puts 「关闭思考」 first: the catalog
  // already lists none first, and the menu keeps the project's order.
  it('leads with 关闭思考 for a model that may switch thinking off', async () => {
    const wrapper = await openMenu({ effort: 'low', efforts: ['low', 'none'], defaultEffort: 'low' })

    const options = wrapper.findAll('.forebrain-chip-menu-opt')
    expect(options[0].attributes('data-testid')).toBe('effort-option-none')
    expect(options[0].text()).toContain('关闭思考')
    wrapper.unmount()
  })
})

describe('McpNotice', () => {
  it('announces the failed servers once and keeps their words behind the toggle', async () => {
    const wrapper = mount(McpNotice, { props: { failures: [
      { name: 'playwright', error: 'startup timed out after 30s' },
      { name: 'github', error: 'handshake refused' },
    ] } })

    const notice = wrapper.find('[data-testid="mcp-notice"]')
    expect(notice.text()).toContain('playwright、github')
    expect(notice.text()).toContain('修好后新开对话即可。')
    expect(wrapper.find('[data-testid="mcp-notice-detail"]').isVisible()).toBe(false)

    await wrapper.find('[data-testid="mcp-notice-toggle"]').trigger('click')
    const details = wrapper.findAll('[data-testid="mcp-notice-detail"]')
    expect(details).toHaveLength(2)
    expect(details[0].text()).toContain('startup timed out after 30s')

    await wrapper.find('[data-testid="mcp-notice-toggle"]').trigger('click')
    expect(wrapper.find('[data-testid="mcp-notice-detail"]').isVisible()).toBe(false)
    wrapper.unmount()
  })

  it('draws nothing when every server started', () => {
    const wrapper = mount(McpNotice, { props: { failures: [] } })
    expect(wrapper.find('[data-testid="mcp-notice"]').exists()).toBe(false)
    wrapper.unmount()
  })
})

describe('ApprovalCard hosting the pending set', () => {
  it('answers a parked approval from the thread, with the panel\'s own control', async () => {
    vi.spyOn(forebrainApi, 'actionsList')
      .mockResolvedValueOnce([{
        id: 'a1', kind: 'shell', status: 'pending',
        payloadJson: JSON.stringify({ command: 'go test ./...', justification: 'run the tests' }),
        createdAt: 1, updatedAt: 1,
        permissionSuggestion: { permissionToolName: 'Bash', availableDecisions: ['accept', 'cancel'] },
      }])
      .mockResolvedValue([])
    const approve = vi.spyOn(forebrainApi, 'actionsApprove').mockResolvedValue({} as never)
    // The card reads the page's one store, provided the way the page provides
    // it (plan 138) — the tail frame is its fallback surface.
    const store = usePendingActions({ sessionId: ref('s1'), version: ref(0), contextUsedPercent: ref(null) })
    const wrapper = mount(ApprovalCard, { global: { provide: { [PendingActionsKey]: store } } })
    await flushPromises()

    const card = wrapper.find('[data-testid="pending-approval"]')
    expect(card.exists()).toBe(true)
    expect(card.text()).toContain('要运行这条命令吗？')
    await card.find('[data-testid="pending-decision-accept"]').trigger('click')
    await flushPromises()

    expect(approve).toHaveBeenCalledWith('a1', { reason: 'web:accept', decision: 'accept' })
    expect(wrapper.find('[data-testid="pending-approval"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
