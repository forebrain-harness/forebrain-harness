import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { config, flushPromises, mount } from '@vue/test-utils'
import tip from '@/directives/tip'
import { h } from 'vue'

import { PromptInputProvider } from '@repo/elements/prompt-input'
import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'
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
  it('offers the default plus exactly the levels the model supports', async () => {
    const wrapper = mount(EffortChip, { props: { effort: '', efforts: ['low', 'high'] } })
    await wrapper.find('[data-testid="effort-chip"]').trigger('click')

    const options = wrapper.findAll('.forebrain-chip-menu-opt')
    expect(options.map((option) => option.text())).toEqual(['默认模型默认档位', '低更快', '高更深入'])

    await options[2].trigger('click')
    expect(wrapper.emitted('select')).toEqual([['high']])
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
    const wrapper = mount(ApprovalCard, { props: { sessionId: 's1', version: 0 } })
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
